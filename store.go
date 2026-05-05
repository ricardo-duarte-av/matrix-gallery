package main

import (
	"context"
	"fmt"
	"log"
	"sort"
	"sync"
	"time"
)

// Store holds all fetched media items and manages incremental loading from Matrix.
// Items are ordered newest-first, matching the backward-pagination order.
type Store struct {
	mu      sync.RWMutex
	items   []MediaItem
	cursor  string // Matrix pagination token for the next backward batch
	sync    string // Matrix pagination token for the next forward batch
	done    bool   // true when there are no more events to fetch
	loading bool

	fetcher  *MatrixFetcher
	precache chan MediaItem
}

type ThumbPrecacher interface {
	Precache(server, mediaID string)
}

func newStore(fetcher *MatrixFetcher, precacher ThumbPrecacher) *Store {
	s := &Store{
		fetcher:  fetcher,
		precache: make(chan MediaItem, 500),
	}
	if precacher != nil {
		for i := 0; i < 5; i++ {
			go s.runPrecacheWorker(precacher)
		}
	}
	return s
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
		items, nextCursor, _, err := s.fetcher.FetchBatch(ctx, cursor, 100)

		s.mu.Lock()
		defer s.mu.Unlock()
		s.loading = false

		if err != nil {
			log.Printf("Error fetching batch from Matrix: %v", err)
			return
		}

		s.items = append(s.items, items...)
		sort.Slice(s.items, func(i, j int) bool {
			return s.items[i].Timestamp > s.items[j].Timestamp
		})

		if nextCursor == "" || len(items) == 0 {
			s.done = true
		} else {
			s.cursor = nextCursor
			for _, item := range items {
				select {
				case s.precache <- item:
				default:
					// queue full, skip
				}
			}
		}
		log.Printf("Fetched %d media items (total: %d, exhausted: %v)", len(items), len(s.items), s.done)
	}()
}

// SyncLoop runs a continuous long-poll connection to Matrix /sync.
// It blocks until ctx is cancelled or an unrecoverable error occurs.
func (s *Store) SyncLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			log.Println("Sync loop stopped")
			return
		default:
		}

		syncToken, err := s.ensureSyncToken(ctx)
		if err != nil {
			log.Printf("Sync error: %v, retrying in 5s", err)
			select {
			case <-time.After(5 * time.Second):
			case <-ctx.Done():
				return
			}
			continue
		}

		items, nextSync, err := s.fetcher.SyncOnce(ctx, syncToken)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("Sync error: %v, retrying in 5s", err)
			select {
			case <-time.After(5 * time.Second):
			case <-ctx.Done():
				return
			}
			continue
		}

		if len(items) > 0 {
			s.mu.Lock()
			s.items = append(items, s.items...)
			sort.Slice(s.items, func(i, j int) bool {
				return s.items[i].Timestamp > s.items[j].Timestamp
			})
			s.sync = nextSync
			s.mu.Unlock()

			log.Printf("Sync found %d new media items (total: %d)", len(items), len(s.items))
			for _, item := range items {
				select {
				case s.precache <- item:
				default:
				}
			}
		} else if nextSync != "" {
			s.mu.Lock()
			s.sync = nextSync
			s.mu.Unlock()
		}
	}
}

func (s *Store) ensureSyncToken(ctx context.Context) (string, error) {
	s.mu.RLock()
	token := s.sync
	s.mu.RUnlock()

	if token != "" {
		return token, nil
	}

	now, err := s.fetcher.GetNowToken(ctx)
	if err != nil {
		return "", err
	}
	if now == "" {
		return "", fmt.Errorf("empty sync token from server")
	}

	s.mu.Lock()
	if s.sync == "" {
		s.sync = now
	}
	token = s.sync
	s.mu.Unlock()

	return token, nil
}
