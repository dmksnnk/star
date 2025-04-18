package platform

import (
	"sync"
)

// Map is a thread-safe generic map.
type Map[K comparable, V any] struct {
	mutex sync.RWMutex
	data  map[K]V

	onAdd    []func(key K, value V)
	onDelete []func(key K, value V, reason error)
}

// NewMap creates a new thread-safe generic map.
func NewMap[K comparable, V any]() *Map[K, V] {
	return &Map[K, V]{
		data:     make(map[K]V),
		onAdd:    []func(key K, value V){},
		onDelete: []func(key K, value V, reason error){},
	}
}

// Get retrieves a value from the map.
func (m *Map[K, V]) Get(key K) (V, bool) {
	m.mutex.RLock()
	defer m.mutex.RUnlock()

	val, ok := m.data[key]
	return val, ok
}

// Put stores a value in the map.
func (m *Map[K, V]) Put(key K, val V) {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	_, exists := m.data[key]
	m.data[key] = val

	if !exists {
		for _, fn := range m.onAdd {
			fn(key, val)
		}
	}
}

// Delete removes a value from the map.
func (m *Map[K, V]) Delete(key K, reason error) {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	if val, exists := m.data[key]; exists {
		delete(m.data, key)
		for _, fn := range m.onDelete {
			fn(key, val, reason)
		}
	}
}

// ForEach executes a function for each key-value pair in the map.
func (m *Map[K, V]) ForEach(fn func(K, V)) {
	m.mutex.RLock()
	defer m.mutex.RUnlock()

	for k, v := range m.data {
		fn(k, v)
	}
}

// NotifyAdd adds a hook function to be called when a new key is added.
func (m *Map[K, V]) NotifyAdd(fn func(key K, value V)) {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	m.onAdd = append(m.onAdd, fn)
}

// NotifyDelete adds a hook function to be called when a key is deleted.
func (m *Map[K, V]) NotifyDelete(fn func(key K, value V, reason error)) {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	m.onDelete = append(m.onDelete, fn)
}

// Map2 is a thread-safe generic map with two keys.
type Map2[K1, K2 comparable, V any] struct {
	mutex sync.RWMutex
	data  map[K1]map[K2]V

	onAdd    []func(key1 K1, key2 K2, value V)
	onDelete []func(key1 K1, key2 K2, value V, reason error)
}

// NewMap2 creates a new thread-safe generic map with two keys.
func NewMap2[K1, K2 comparable, V any]() *Map2[K1, K2, V] {
	return &Map2[K1, K2, V]{
		data:     make(map[K1]map[K2]V),
		onAdd:    []func(key1 K1, key2 K2, value V){},
		onDelete: []func(key1 K1, key2 K2, value V, reason error){},
	}
}

// Get retrieves a value from the map.
func (m *Map2[K1, K2, V]) Get(key1 K1, key2 K2) (V, bool) {
	m.mutex.RLock()
	defer m.mutex.RUnlock()

	if subMap, ok := m.data[key1]; ok {
		val, ok := subMap[key2]
		return val, ok
	}

	var zero V
	return zero, false
}

// Put stores a value in the map.
func (m *Map2[K1, K2, V]) Put(key1 K1, key2 K2, val V) {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	if _, ok := m.data[key1]; !ok {
		m.data[key1] = make(map[K2]V)
	}

	_, exists := m.data[key1][key2]
	m.data[key1][key2] = val

	if !exists {
		for _, fn := range m.onAdd {
			fn(key1, key2, val)
		}
	}
}

// Delete removes a value from the map.
func (m *Map2[K1, K2, V]) Delete(key1 K1, key2 K2, reason error) bool {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	if subMap, ok := m.data[key1]; ok {
		if val, exists := subMap[key2]; exists {
			delete(subMap, key2)
			for _, fn := range m.onDelete {
				fn(key1, key2, val, reason)
			}

			if len(subMap) == 0 {
				delete(m.data, key1)
			}
			return true
		}
	}

	return false
}

// ForEach executes a function for each key-value pair in the map.
func (m *Map2[K1, K2, V]) ForEach(key1 K1, fn func(K2, V)) {
	m.mutex.RLock()
	defer m.mutex.RUnlock()

	for k, v := range m.data[key1] {
		fn(k, v)
	}
}

// Keys returns all secondary keys for a key.
func (m *Map2[K1, K2, V]) Keys(key1 K1) []K2 {
	m.mutex.RLock()
	defer m.mutex.RUnlock()

	if subMap, ok := m.data[key1]; ok {
		keys := make([]K2, 0, len(subMap))
		for k := range subMap {
			keys = append(keys, k)
		}
		return keys
	}

	return []K2{}
}

// NotifyAdd adds a hook function to be called when a new key pair is added.
func (m *Map2[K1, K2, V]) NotifyAdd(fn func(key1 K1, key2 K2, value V)) {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	m.onAdd = append(m.onAdd, fn)
}

// NotifyDelete adds a hook function to be called when a key pair is deleted.
func (m *Map2[K1, K2, V]) NotifyDelete(fn func(key1 K1, key2 K2, value V, reason error)) {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	m.onDelete = append(m.onDelete, fn)
}
