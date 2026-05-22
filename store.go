package main

import (
	"context"
	"log"
	"sort"
	"sync"
	"time"

	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// Store holds all fetched media items and manages incremental loading from Matrix for a specific room.
// Items are ordered newest-first, matching the backward-pagination order.
type Store struct {
	roomID    id.RoomID
	mu        sync.RWMutex
	items     []MediaItem
	reactions map[string]map[string]int // eventID -> emoji -> count
	cursor    string                    // Matrix pagination token for the next backward batch
	done      bool                      // true when there are no more events to fetch
	loading   bool

	fetcher  *MatrixFetcher
	precache chan MediaItem
}

type ThumbPrecacher interface {
	Precache(server, mediaID string)
}

func newStore(fetcher *MatrixFetcher, precacher ThumbPrecacher, roomID id.RoomID) *Store {
	s := &Store{
		roomID:    roomID,
		fetcher:   fetcher,
		reactions: make(map[string]map[string]int),
		precache:  make(chan MediaItem, 500),
	}
	if precacher != nil {
		for i := 0; i < 5; i++ {
			go s.runPrecacheWorker(precacher)
		}
	}
	return s
}

// mergeReactions merges newReactions into s.reactions and updates s.items in-place.
// Must be called with s.mu held for writing.
func (s *Store) mergeReactions(newReactions map[string]map[string]int) {
	if len(newReactions) == 0 {
		return
	}
	for targetID, emojis := range newReactions {
		if s.reactions[targetID] == nil {
			s.reactions[targetID] = make(map[string]int)
		}
		for emoji, count := range emojis {
			s.reactions[targetID][emoji] += count
		}
	}
	for i, item := range s.items {
		if r, ok := s.reactions[item.EventID]; ok {
			s.items[i].Reactions = r
		}
	}
}

func (s *Store) runPrecacheWorker(p ThumbPrecacher) {
	for item := range s.precache {
		if item.ThumbServer != "" && item.ThumbMediaID != "" {
			p.Precache(item.ThumbServer, item.ThumbMediaID)
		}
	}
}

// GetPage returns a slice of items at [offset, offset+limit) and whether more exist.
func (s *Store) GetPage(offset, limit int) (items []MediaItem, hasMore bool, total int) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	n := len(s.items)
	if offset >= n {
		return []MediaItem{}, !s.done, n
	}
	end := offset + limit
	if end > n {
		end = n
	}
	result := make([]MediaItem, end-offset)
	copy(result, s.items[offset:end])
	return result, !s.done || end < n, n
}

// NeedsMore returns true when the store should load another batch to cover offset+limit.
func (s *Store) NeedsMore(offset, limit int) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return !s.done && offset+limit >= len(s.items)-20
}

// IsLoading reports whether a fetch is currently in progress.
func (s *Store) IsLoading() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.loading
}

// IsDone reports whether all room history has been fetched.
func (s *Store) IsDone() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.done
}

// TriggerLoad starts a background fetch if one is not already running.
func (s *Store) TriggerLoad(ctx context.Context) {
	s.mu.Lock()
	if s.loading || s.done {
		s.mu.Unlock()
		return
	}
	s.loading = true
	cursor := s.cursor
	s.mu.Unlock()

	go func() {
		items, newReactions, nextCursor, _, err := s.fetcher.FetchBatch(ctx, s.roomID, cursor, 100)

		s.mu.Lock()
		defer s.mu.Unlock()
		s.loading = false

		if err != nil {
			log.Printf("Error fetching batch from Matrix for %s: %v", s.roomID, err)
			return
		}

		s.mergeReactions(newReactions)
		// Apply accumulated reactions to newly fetched items before appending.
		for i, item := range items {
			if r, ok := s.reactions[item.EventID]; ok {
				items[i].Reactions = r
			}
		}
		s.items = append(s.items, items...)
		sort.Slice(s.items, func(i, j int) bool {
			return s.items[i].Timestamp > s.items[j].Timestamp
		})

		if nextCursor == "" {
			s.done = true
		} else {
			s.cursor = nextCursor
			if len(items) > 0 {
				for _, item := range items {
					select {
					case s.precache <- item:
					default:
						// queue full, skip
					}
				}
			}
		}
		log.Printf("[%s] Fetched %d media items (total: %d, exhausted: %v)", s.roomID, len(items), len(s.items), s.done)
	}()
}

// InjectEvents handles new events from a global sync.
func (s *Store) InjectEvents(ctx context.Context, events []*event.Event) {
	var items []MediaItem
	reactions := make(map[string]map[string]int)

	for _, evt := range events {
		if evt.Type == event.EventReaction {
			if err := evt.Content.ParseRaw(evt.Type); err != nil {
				continue
			}
			r := evt.Content.AsReaction()
			rel := r.RelatesTo
			if rel.Type == event.RelAnnotation && rel.EventID != "" && rel.Key != "" {
				targetID := rel.EventID.String()
				if reactions[targetID] == nil {
					reactions[targetID] = make(map[string]int)
				}
				reactions[targetID][rel.Key]++
			}
			continue
		}
		if evt.Type != event.EventMessage && evt.Type != event.EventSticker {
			continue
		}
		if err := evt.Content.ParseRaw(evt.Type); err != nil {
			continue
		}
		msg := evt.Content.AsMessage()
		item, ok := extractMediaItem(evt, msg)
		if !ok {
			continue
		}
		m := s.fetcher.getMember(ctx, s.roomID, evt.Sender)
		item.DisplayName = m.DisplayName
		item.SenderAvatarURL = m.AvatarURL
		items = append(items, item)
	}

	if len(items) > 0 || len(reactions) > 0 {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.mergeReactions(reactions)
		for i, item := range items {
			if r, ok := s.reactions[item.EventID]; ok {
				items[i].Reactions = r
			}
		}
		s.items = append(items, s.items...)
		sort.Slice(s.items, func(i, j int) bool {
			return s.items[i].Timestamp > s.items[j].Timestamp
		})

		log.Printf("[%s] Sync found %d new media items (total: %d)", s.roomID, len(items), len(s.items))
		for _, item := range items {
			select {
			case s.precache <- item:
			default:
			}
		}
	}
}

type StoreManager struct {
	mu        sync.RWMutex
	stores    map[id.RoomID]*Store
	fetcher   *MatrixFetcher
	precacher ThumbPrecacher
	syncToken string
}

func NewStoreManager(fetcher *MatrixFetcher, precacher ThumbPrecacher) *StoreManager {
	return &StoreManager{
		stores:    make(map[id.RoomID]*Store),
		fetcher:   fetcher,
		precacher: precacher,
	}
}

func (m *StoreManager) GetStore(roomID id.RoomID) *Store {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.stores[roomID]; ok {
		return s
	}
	s := newStore(m.fetcher, m.precacher, roomID)
	m.stores[roomID] = s
	return s
}

func (m *StoreManager) SyncLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		if m.syncToken == "" {
			tok, err := m.fetcher.GetNowToken(ctx)
			if err != nil {
				log.Printf("Sync error (get token): %v, retrying in 5s", err)
				time.Sleep(5 * time.Second)
				continue
			}
			m.syncToken = tok
		}

		resp, err := m.fetcher.client.SyncRequest(ctx, 30000, m.syncToken, "", false, "")
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("Sync error: %v, retrying in 5s", err)
			time.Sleep(5 * time.Second)
			continue
		}
		m.syncToken = resp.NextBatch

		m.mu.RLock()
		for roomID, joinedRoom := range resp.Rooms.Join {
			if s, ok := m.stores[roomID]; ok {
				s.InjectEvents(ctx, joinedRoom.Timeline.Events)
			}
		}
		m.mu.RUnlock()
	}
}
