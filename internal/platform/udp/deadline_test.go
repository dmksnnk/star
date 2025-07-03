package udp

import (
	"testing"
	"time"
)

func TestDeadline(t *testing.T) {
	t.Run("zero time", func(t *testing.T) {
		ctx, cancel := newDeadline(time.Time{})

		select {
		case <-ctx.Done():
			t.Error("ctx is done, but shouldn't")
		default:
		}

		cancel()

		select {
		case <-ctx.Done():
		default:
			t.Error("ctx is not done")
		}
	})

	t.Run("time in the past", func(t *testing.T) {
		ctx, cancel := newDeadline(time.Now().Add(-time.Minute))
		defer cancel()

		select {
		case <-ctx.Done():
		default:
			t.Error("ctx is not done")
		}
	})

	t.Run("time in the future", func(t *testing.T) {
		ctx, cancel := newDeadline(time.Now().Add(time.Millisecond))
		defer cancel()

		select {
		case <-ctx.Done():
		case <-time.After(2 * time.Millisecond):
			t.Error("ctx is not done after deadline")
		}
	})
}
