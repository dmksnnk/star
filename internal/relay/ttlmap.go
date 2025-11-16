package relay

import (
	"sync"
	"time"
)

type TTLMap[K comparable, V any] struct {
	mu      sync.Mutex
	entries map[K]*ttlValue[V]

	wg   sync.WaitGroup
	done chan struct{}
}

type ttlValue[T any] struct {
	value        T
	lastAccessed int64
}

func NewTTLMap[K comparable, V any](ttl time.Duration) *TTLMap[K, V] {
	m := TTLMap[K, V]{
		entries: make(map[K]*ttlValue[V]),
		done:    make(chan struct{}),
	}

	m.wg.Add(1)
	go func() {
		defer m.wg.Done()

		ttlNs := ttl.Nanoseconds()

		ticker := time.NewTicker(ttl / 2)
		defer ticker.Stop()

		for {
			select {
			case <-m.done:
				return
			case <-ticker.C:
				now := time.Now().UnixNano()

				m.mu.Lock()
				for k, entry := range m.entries {
					if now-entry.lastAccessed > ttlNs {
						delete(m.entries, k)
					}
				}
				m.mu.Unlock()
			}
		}
	}()

	return &m
}

func (m *TTLMap[K, V]) Set(key K, value V) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.entries[key] = &ttlValue[V]{
		value:        value,
		lastAccessed: time.Now().UnixNano(),
	}
}

func (m *TTLMap[K, V]) Get(key K) (V, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	value, ok := m.entries[key]
	if !ok {
		var zero V
		return zero, false
	}

	value.lastAccessed = time.Now().UnixNano()

	return value.value, ok
}

func (m *TTLMap[K, V]) Close() {
	close(m.done)
	m.wg.Wait()
}
