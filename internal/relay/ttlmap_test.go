package relay_test

import (
	"testing"
	"time"

	"github.com/dmksnnk/star/internal/relay"
)

func TestTTLMap_SetGet(t *testing.T) {
	ttlMap := relay.NewTTLMap[string, int](100 * time.Millisecond)
	t.Cleanup(ttlMap.Close)

	key := "testKey"
	value := 42

	t.Run("evicts entries after TTL", func(t *testing.T) {
		ttlMap.Set(key, value)

		retrievedValue, ok := ttlMap.Get(key)
		if !ok {
			t.Fatalf("expected to find key %q", key)
		}
		if retrievedValue != value {
			t.Fatalf("expected value %d, got %d", value, retrievedValue)
		}

		time.Sleep(150 * time.Millisecond)

		_, ok = ttlMap.Get(key)
		if ok {
			t.Fatalf("expected key %q to be evicted", key)
		}
	})

	t.Run("updates last accessed time on Get", func(t *testing.T) {
		ttlMap.Set(key, value)

		time.Sleep(80 * time.Millisecond)

		retrievedValue, ok := ttlMap.Get(key)
		if !ok {
			t.Fatalf("expected to find key %q", key)
		}
		if retrievedValue != value {
			t.Fatalf("expected value %d, got %d", value, retrievedValue)
		}

		time.Sleep(50 * time.Millisecond) // total sleep now longer than TTL

		retrievedValue, ok = ttlMap.Get(key)
		if !ok {
			t.Fatalf("expected to find key %q after accessing it", key)
		}
		if retrievedValue != value {
			t.Fatalf("expected value %d, got %d", value, retrievedValue)
		}
	})
}
