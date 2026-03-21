package platform

import (
	"math/rand/v2"
	"testing"
)

func TestQueue_ZeroValue(t *testing.T) {
	var queue Queue[int]

	_, ok := queue.Peek()
	if ok {
		t.Fatal("expected Front() to return false on zero value")
	}

	_, ok = queue.Pop()
	if ok {
		t.Fatal("expected Pop() to return false on zero value")
	}
}

func TestQueue_FIFOOrder(t *testing.T) {
	var queue Queue[int]

	for i := range 100 {
		queue.Push(i)
	}

	for i := range 100 {
		v, ok := queue.Peek()
		if !ok {
			t.Fatalf("expected element at index %d", i)
		}
		if v != i {
			t.Fatalf("index %d: expected %d, got %d", i, i, v)
		}

		v, ok = queue.Pop()
		if !ok {
			t.Fatalf("Pop: expected element at index %d", i)
		}
		if v != i {
			t.Fatalf("Pop: index %d: expected %d, got %d", i, i, v)
		}
	}

	_, ok := queue.Peek()
	if ok {
		t.Fatal("expected empty buffer after draining")
	}
}

func TestQueue_DropFront(t *testing.T) {
	var queue Queue[int]

	queue.Push(10)
	queue.Push(20)

	v, ok := queue.Pop()
	if !ok {
		t.Fatal("expected element on first Pop")
	}
	if v != 10 {
		t.Fatalf("expected 10, got %d", v)
	}

	v, ok = queue.Peek()
	if !ok {
		t.Fatal("expected element after one Pop")
	}
	if v != 20 {
		t.Fatalf("expected 20, got %d", v)
	}

	v, ok = queue.Pop()
	if !ok {
		t.Fatal("expected element on second Pop")
	}
	if v != 20 {
		t.Fatalf("expected 20, got %d", v)
	}

	_, ok = queue.Peek()
	if ok {
		t.Fatal("expected empty buffer")
	}

	_, ok = queue.Pop()
	if ok {
		t.Fatal("expected Pop() to return false on empty queue")
	}
}

func TestQueue_InterleavedAddDrop(t *testing.T) {
	var queue Queue[int]

	// Fill and drain repeatedly to exercise head wrapping
	for round := range 5 {
		for i := range 10 {
			queue.Push(round*10 + i)
		}
		for i := range 10 {
			want := round*10 + i

			v, ok := queue.Peek()
			if !ok {
				t.Fatalf("round %d, index %d: expected element", round, i)
			}
			if v != want {
				t.Fatalf("round %d, index %d: expected %d, got %d", round, i, want, v)
			}

			v, ok = queue.Pop()
			if !ok {
				t.Fatalf("Pop: round %d, index %d: expected element", round, i)
			}
			if v != want {
				t.Fatalf("Pop: round %d, index %d: expected %d, got %d", round, i, want, v)
			}
		}
	}
}

func TestQueue_GrowPreservesHead(t *testing.T) {
	for range 100 {
		var rb Queue[int]

		n1 := rand.IntN(100) + 1 // to avoid 0
		n2 := rand.IntN(100)
		dropped := rand.IntN(n1)

		// add elements and drop to move the head forward
		for i := range n1 {
			rb.Push(i)
		}
		for i := range dropped {
			v, ok := rb.Pop()
			if !ok {
				t.Fatalf("Pop: expected element at drop index %d", i)
			}
			if v != i {
				t.Fatalf("Pop: drop index %d: expected %d, got %d", i, i, v)
			}
		}

		// add more elements to trigger growth
		for i := n1; i < n1+n2; i++ {
			rb.Push(i)
		}

		for i := dropped; i < n1+n2; i++ {
			v, ok := rb.Peek()
			if !ok {
				t.Fatalf("expected element %d", i)
			}
			if v != i {
				t.Fatalf("expected %d, got %d", i, v)
			}

			v, ok = rb.Pop()
			if !ok {
				t.Fatalf("Pop: expected element %d", i)
			}
			if v != i {
				t.Fatalf("Pop: expected %d, got %d", i, v)
			}
		}
	}
}
