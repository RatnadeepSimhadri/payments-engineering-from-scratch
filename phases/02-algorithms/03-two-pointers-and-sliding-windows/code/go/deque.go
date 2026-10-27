package main

// Deque is a growable ring buffer with O(1) push and pop at both ends: the role java.util.ArrayDeque plays.
// Go's standard library has no deque; a slice used as a queue with q = q[1:] keeps the whole backing array alive.
type Deque[T any] struct {
	buf        []T
	head, size int
}

// NewDeque returns an empty deque.
func NewDeque[T any]() *Deque[T] { return &Deque[T]{buf: make([]T, 8)} }

// Len is the number of elements.
func (d *Deque[T]) Len() int { return d.size }

// IsEmpty is true when Len() == 0.
func (d *Deque[T]) IsEmpty() bool { return d.size == 0 }

func (d *Deque[T]) grow() {
	if d.size < len(d.buf) {
		return
	}
	next := make([]T, 2*len(d.buf))
	for i := 0; i < d.size; i++ {
		next[i] = d.buf[(d.head+i)%len(d.buf)]
	}
	d.buf, d.head = next, 0
}

// AddLast appends at the back.
func (d *Deque[T]) AddLast(v T) {
	d.grow()
	d.buf[(d.head+d.size)%len(d.buf)] = v
	d.size++
}

// PeekFirst returns the front element; the deque must be non-empty.
func (d *Deque[T]) PeekFirst() T { return d.buf[d.head] }

// PeekLast returns the back element; the deque must be non-empty.
func (d *Deque[T]) PeekLast() T { return d.buf[(d.head+d.size-1)%len(d.buf)] }

// PollFirst removes and returns the front element; the deque must be non-empty.
func (d *Deque[T]) PollFirst() T {
	v := d.buf[d.head]
	var zero T
	d.buf[d.head] = zero
	d.head = (d.head + 1) % len(d.buf)
	d.size--
	return v
}

// PollLast removes and returns the back element; the deque must be non-empty.
func (d *Deque[T]) PollLast() T {
	i := (d.head + d.size - 1) % len(d.buf)
	v := d.buf[i]
	var zero T
	d.buf[i] = zero
	d.size--
	return v
}
