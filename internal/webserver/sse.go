package webserver

import (
	"bytes"
	"container/ring"
	"context"
	"log/slog"
	"sync"
)

// SSEHandler is a slog.Handler that broadcasts log lines to SSE subscribers.
// It also maintains a ring buffer for history replay on new connections.
type SSEHandler struct {
	mu          sync.Mutex
	buf         bytes.Buffer
	handler     slog.Handler
	history     ringBuffer
	subscribers map[chan string]struct{}
}

var _ slog.Handler = (*SSEHandler)(nil)

// NewSSEHandler creates a new SSE log handler with the given buffer size.
func NewSSEHandler(bufferSize int) *SSEHandler {
	h := &SSEHandler{
		history:     newRingBuffer(bufferSize),
		subscribers: make(map[chan string]struct{}),
	}

	h.handler = slog.NewTextHandler(&h.buf, &slog.HandlerOptions{
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if a.Key == slog.TimeKey && len(groups) == 0 {
				return slog.String(a.Key, a.Value.Time().Format("15:04:05"))
			}
			return a
		},
	})

	return h
}

func (h *SSEHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *SSEHandler) WithAttrs([]slog.Attr) slog.Handler       { return h }
func (h *SSEHandler) WithGroup(string) slog.Handler            { return h }

func (h *SSEHandler) Handle(ctx context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.buf.Reset()
	if err := h.handler.Handle(ctx, r); err != nil {
		return err
	}

	line := h.buf.String()

	h.history.add(line)

	for ch := range h.subscribers {
		select {
		case ch <- line:
		default: // drop if subscriber is slow
		}
	}
	return nil
}

// Subscribe returns buffered history and a channel for new log lines.
func (h *SSEHandler) Subscribe() ([]string, chan string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	ch := make(chan string, 100)
	h.subscribers[ch] = struct{}{}
	return h.history.entries(), ch
}

// Unsubscribe removes a subscriber channel.
func (h *SSEHandler) Unsubscribe(ch chan string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.subscribers, ch)
	close(ch)
}

// Reset clears the buffer and disconnects all subscribers.
func (h *SSEHandler) Reset() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.history.reset()
	for ch := range h.subscribers {
		close(ch)
		delete(h.subscribers, ch)
	}
}

// ringBuffer is a fixed-size circular buffer of strings.
type ringBuffer struct {
	r        *ring.Ring
	count    int // number of entries written, capped at capacity
	capacity int
}

func newRingBuffer(capacity int) ringBuffer {
	return ringBuffer{
		r:        ring.New(capacity),
		capacity: capacity,
	}
}

// add appends a string to the ring buffer, overwriting the oldest entry if full.
func (rb *ringBuffer) add(s string) {
	rb.r.Value = s
	rb.r = rb.r.Next()
	if rb.count < rb.capacity {
		rb.count++
	}
}

// entries returns all stored strings in insertion order.
func (rb *ringBuffer) entries() []string {
	result := make([]string, 0, rb.count)
	r := rb.r.Move(-rb.count)
	for range rb.count {
		result = append(result, r.Value.(string))
		r = r.Next()
	}
	return result
}

// reset clears all entries, keeping the same capacity.
func (rb *ringBuffer) reset() {
	rb.r = ring.New(rb.capacity)
	rb.count = 0
}
