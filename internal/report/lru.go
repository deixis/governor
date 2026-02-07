package report

import "sync"

// LRUStore is an in-memory LRU cache that delegates to a backing Store on miss.
type LRUStore struct {
	mu   sync.Mutex
	cap  int
	back Store

	// Doubly-linked list for LRU ordering (most recent at head).
	head, tail *lruEntry
	items      map[string]*lruEntry
}

type lruEntry struct {
	key    string
	result *RunResult
	prev   *lruEntry
	next   *lruEntry
}

// NewLRUStore creates an LRU cache with the given capacity that delegates
// to back on cache misses. Capacity must be >= 1.
func NewLRUStore(cap int, back Store) *LRUStore {
	if cap < 1 {
		cap = 1
	}
	return &LRUStore{
		cap:   cap,
		back:  back,
		items: make(map[string]*lruEntry, cap),
	}
}

// Save writes the result to the LRU cache and delegates to the backing store.
func (s *LRUStore) Save(result *RunResult) error {
	s.mu.Lock()
	// Update or insert into the LRU cache.
	if e, ok := s.items[result.ID]; ok {
		e.result = result
		s.moveToFront(e)
	} else {
		e := &lruEntry{key: result.ID, result: result}
		s.items[result.ID] = e
		s.pushFront(e)
		if len(s.items) > s.cap {
			s.evict()
		}
	}
	s.mu.Unlock()

	// Delegate to backing store.
	return s.back.Save(result)
}

// Load checks the LRU cache first. On miss, loads from the backing store
// and promotes the result into the cache.
func (s *LRUStore) Load(runID string) (*RunResult, error) {
	s.mu.Lock()
	if e, ok := s.items[runID]; ok {
		s.moveToFront(e)
		r := e.result
		s.mu.Unlock()
		return r, nil
	}
	s.mu.Unlock()

	// Cache miss — load from backing store.
	result, err := s.back.Load(runID)
	if err != nil {
		return nil, err
	}

	// Promote into cache.
	s.mu.Lock()
	if e, ok := s.items[runID]; ok {
		// Concurrent load already inserted it.
		e.result = result
		s.moveToFront(e)
	} else {
		e := &lruEntry{key: runID, result: result}
		s.items[runID] = e
		s.pushFront(e)
		if len(s.items) > s.cap {
			s.evict()
		}
	}
	s.mu.Unlock()

	return result, nil
}

func (s *LRUStore) pushFront(e *lruEntry) {
	e.prev = nil
	e.next = s.head
	if s.head != nil {
		s.head.prev = e
	}
	s.head = e
	if s.tail == nil {
		s.tail = e
	}
}

func (s *LRUStore) moveToFront(e *lruEntry) {
	if s.head == e {
		return
	}
	s.remove(e)
	s.pushFront(e)
}

func (s *LRUStore) remove(e *lruEntry) {
	if e.prev != nil {
		e.prev.next = e.next
	} else {
		s.head = e.next
	}
	if e.next != nil {
		e.next.prev = e.prev
	} else {
		s.tail = e.prev
	}
	e.prev = nil
	e.next = nil
}

func (s *LRUStore) evict() {
	if s.tail == nil {
		return
	}
	e := s.tail
	s.remove(e)
	delete(s.items, e.key)
}
