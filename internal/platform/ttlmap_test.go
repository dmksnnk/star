package platform_test

import (
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/dmksnnk/star/internal/platform"
)

func TestTTLMap_GetOrSet(t *testing.T) {
	t.Run("miss", func(t *testing.T) {
		m := platform.NewTTLMap[string, int](time.Minute)

		calls := 0
		v := m.GetOrSet("key", func() int {
			calls++
			return 42
		})

		if v != 42 {
			t.Fatalf("expected 42, got %d", v)
		}
		if calls != 1 {
			t.Fatalf("expected newV called once, got %d", calls)
		}
	})

	t.Run("hit", func(t *testing.T) {
		m := platform.NewTTLMap[string, int](time.Minute)

		m.GetOrSet("key", func() int { return 42 })

		calls := 0
		v := m.GetOrSet("key", func() int {
			calls++
			return 99
		})

		if v != 42 {
			t.Fatalf("expected 42, got %d", v)
		}
		if calls != 0 {
			t.Fatalf("expected newV not called, got %d calls", calls)
		}
	})

	t.Run("expired", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			m := platform.NewTTLMap[string, int](time.Second)

			m.GetOrSet("key", func() int { return 1 })

			time.Sleep(2 * time.Second) // wait for the item to expire

			// trigger cleanup by adding a new key.
			m.GetOrSet("other", func() int { return 0 })

			calls := 0
			v := m.GetOrSet("key", func() int {
				calls++
				return 2
			})

			if v != 2 {
				t.Fatalf("expected 2, got %d", v)
			}
			if calls != 1 {
				t.Fatalf("expected newV called once after expiry, got %d", calls)
			}
		})
	})

	// check that it removes all expired items, not just the first one it encounters
	t.Run("multiple expired", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			m := platform.NewTTLMap[string, int](time.Second)

			m.GetOrSet("a", func() int { return 1 })
			m.GetOrSet("b", func() int { return 2 })

			time.Sleep(2 * time.Second)

			calls := 0
			v := m.GetOrSet("b", func() int {
				calls++
				return 99
			})

			if v != 99 {
				t.Fatalf("expected 99, got %d", v)
			}

			if calls != 1 {
				t.Fatalf("expected new item created after expiry (newV called once), got %d", calls)
			}
		})
	})

	t.Run("concurrent", func(t *testing.T) {
		m := platform.NewTTLMap[int, int](time.Minute)

		var wg sync.WaitGroup
		for i := range 100 {
			wg.Add(1)
			go func(key int) {
				defer wg.Done()
				m.GetOrSet(key%10, func() int { return key })
			}(i)
		}
		wg.Wait()
	})
}
