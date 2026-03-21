package platform

// Queue is a simple FIFO queue backed by a ring buffer.
// Zero value is a Queue backed by empty slice.
type Queue[T any] struct {
	buf  []T
	head int
	len  int
}

// Peek returns the first element of the queue without removing it, or false if the queue is empty.
func (rb *Queue[T]) Peek() (T, bool) {
	if rb.len == 0 {
		var zero T
		return zero, false
	}

	return rb.buf[rb.head], true
}

// Pop removes and returns the first element of the queue, or false if the queue is empty.
func (rb *Queue[T]) Pop() (T, bool) {
	var zero T
	if rb.len == 0 {
		return zero, false
	}

	item := rb.buf[rb.head]
	rb.buf[rb.head] = zero // clear reference to the popped item for GC
	rb.head = (rb.head + 1) % cap(rb.buf)
	rb.len--

	return item, true
}

// Push adds an element to the end of the queue.
func (rb *Queue[T]) Push(item T) {
	if rb.len == cap(rb.buf) {
		rb.grow()
	}

	pos := (rb.head + rb.len) % cap(rb.buf)
	rb.buf[pos] = item
	rb.len++
}

func (rb *Queue[T]) grow() {
	buf := make([]T, (cap(rb.buf)+1)*2)
	for i := range rb.len {
		pos := (rb.head + i) % cap(rb.buf)
		buf[i] = rb.buf[pos]
	}

	rb.buf = buf
	rb.head = 0
}
