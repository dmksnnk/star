package platform

import (
	"container/list"
	"sync"
	"time"
)

// TTLMap is a map where items are evicted after they are not accessed for a certain duration.
type TTLMap[K comparable, V any] struct {
	mu    sync.Mutex
	ttl   time.Duration
	list  list.List
	items map[K]*list.Element
}

// NewTTLMap creates a new TTLMap with the given time-to-live for items.
func NewTTLMap[K comparable, V any](ttl time.Duration) *TTLMap[K, V] {
	return &TTLMap[K, V]{
		ttl:   ttl,
		items: make(map[K]*list.Element),
	}
}

type item[K comparable, V any] struct {
	key   K
	value V
	t     time.Time
}

// GetOrSet gets a value for a key or creates a new one if it doesn't exist.
// It also evicts expired items.
func (m *TTLMap[K, V]) GetOrSet(k K, newV func() V) V {
	m.mu.Lock()
	defer m.mu.Unlock()

	// drop expired items
	front := m.list.Front()
	for front != nil {
		it := front.Value.(*item[K, V])
		if time.Since(it.t) < m.ttl {
			break
		}

		next := front.Next() // get next before removing
		m.list.Remove(front)
		delete(m.items, it.key)
		front = next
	}

	v, ok := m.get(k)
	if ok {
		return v
	}

	v = newV()
	m.set(k, v)

	return v
}

func (m *TTLMap[K, V]) get(key K) (V, bool) {
	elem, ok := m.items[key]
	if !ok {
		var zero V
		return zero, false
	}

	it := elem.Value.(*item[K, V])
	it.t = time.Now()
	m.list.MoveToBack(elem)

	return it.value, true
}

func (m *TTLMap[K, V]) set(k K, v V) {
	e := &item[K, V]{
		key:   k,
		value: v,
		t:     time.Now(),
	}

	el := m.list.PushBack(e)
	m.items[k] = el
}
