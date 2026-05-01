package main

import (
	"context"
	"log"
	"sync"
)

// Store holds all fetched media items and manages incremental loading from Matrix.
// Items are ordered newest-first, matching the backward-pagination order.
type Store struct {
	mu      sync.RWMutex
	items   []MediaItem
	cursor  string // Matrix pagination token for the next backward batch
	done    bool   // true when there are no more events to fetch
	loading bool

	fetcher *MatrixFetcher
}

func newStore(fetcher *MatrixFetcher) *Store {
	return &Store{fetcher: fetcher}
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
		items, nextCursor, err := s.fetcher.FetchBatch(ctx, cursor, 100)

		s.mu.Lock()
		defer s.mu.Unlock()
		s.loading = false

		if err != nil {
			log.Printf("Error fetching batch from Matrix: %v", err)
			return
		}

		s.items = append(s.items, items...)
		if nextCursor == "" || len(items) == 0 {
			s.done = true
		} else {
			s.cursor = nextCursor
		}
		log.Printf("Fetched %d media items (total: %d, exhausted: %v)", len(items), len(s.items), s.done)
	}()
}
