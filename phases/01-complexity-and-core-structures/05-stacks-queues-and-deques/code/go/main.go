// Lesson: phases/01-complexity-and-core-structures/05-stacks-queues-and-deques/docs/en.md
// Stacks, Queues, and Ring Buffers
// Run:  go run .
// Test: go test ./...
// Sources: Knuth, TAOCP Vol. 1, 2.2.1-2.2.2 (stacks, queues, deques; circular buffers);
//
//	LMAX Disruptor technical paper (2011) for sequence-numbered ring buffers; Apache Kafka docs (offsets).
//
// Go 1.22+, standard library only. Same design as the Java and Python versions.
package main

import (
	"errors"
	"fmt"
	"strings"
)

var (
	// ErrEmpty is returned by Pop and Peek on an empty stack.
	ErrEmpty = errors.New("stack is empty")
	// ErrIndexOutOfBounds is returned by RingBuffer.Get for an index outside 0..size-1.
	ErrIndexOutOfBounds = errors.New("index out of bounds")
	// ErrCapacity is returned when a structure is built with capacity < 1.
	ErrCapacity = errors.New("capacity must be >= 1")
)

// ArrayStack is a LIFO stack over a growable slice. Push/Pop/Peek are O(1) amortized; the top is always at
// index size-1, so there is no wraparound and no head pointer: the dynamic array from Lesson 03 is already a stack.
type ArrayStack[T any] struct {
	elements []T // len(elements) is the capacity
	size     int
}

// NewArrayStack allocates the initial capacity.
func NewArrayStack[T any](initialCapacity int) (*ArrayStack[T], error) {
	if initialCapacity < 1 {
		return nil, fmt.Errorf("initialCapacity must be >= 1: %w", ErrCapacity)
	}
	return &ArrayStack[T]{elements: make([]T, initialCapacity)}, nil
}

func mustStack[T any](initialCapacity int) *ArrayStack[T] {
	s, err := NewArrayStack[T](initialCapacity)
	if err != nil {
		panic(err)
	}
	return s
}

func (s *ArrayStack[T]) Size() int     { return s.size }
func (s *ArrayStack[T]) IsEmpty() bool { return s.size == 0 }
func (s *ArrayStack[T]) Capacity() int { return len(s.elements) }

// Push appends at the top, doubling the backing slice when full.
func (s *ArrayStack[T]) Push(value T) {
	if s.size == len(s.elements) {
		grown := make([]T, len(s.elements)*2)
		copy(grown, s.elements)
		s.elements = grown
	}
	s.elements[s.size] = value
	s.size++
}

// Pop removes the top.
func (s *ArrayStack[T]) Pop() (T, error) {
	var zero T
	if s.size == 0 {
		return zero, ErrEmpty
	}
	s.size--
	v := s.elements[s.size]
	s.elements[s.size] = zero // do not pin the popped object
	return v, nil
}

// Peek reads the top without removing it.
func (s *ArrayStack[T]) Peek() (T, error) {
	if s.size == 0 {
		var zero T
		return zero, ErrEmpty
	}
	return s.elements[s.size-1], nil
}

// Each iterates from the top down: the order Pop would return.
func (s *ArrayStack[T]) Each(visit func(T)) {
	for i := s.size - 1; i >= 0; i-- {
		visit(s.elements[i])
	}
}

// OverflowPolicy is what a bounded buffer does when a producer offers into a full buffer. BLOCK belongs to Phase 4.
type OverflowPolicy int

const (
	Reject     OverflowPolicy = iota // offer returns false; the producer must slow down or retry: backpressure
	DropOldest                       // overwrite the oldest element; the consumer loses history but the producer never waits
)

// RingBuffer is a bounded double-ended queue over a fixed slice, addressed by monotonically increasing sequence numbers.
// head is the sequence of the oldest element, tail the sequence the next OfferLast will take; size = tail - head.
// A sequence maps to a slot with seq % capacity, so the buffer wraps around without ever moving an element.
// Because size is a difference of sequences, full (size == capacity) and empty (size == 0) are distinct
// without wasting a slot, and the sequences double as consumer offsets: this is the shape of a broker partition.
type RingBuffer[T any] struct {
	slots   []T
	policy  OverflowPolicy
	head    int64 // sequence of the first (oldest) element
	tail    int64 // sequence one past the last (newest) element
	dropped int64 // elements overwritten under DropOldest
}

// NewRingBuffer allocates capacity slots.
func NewRingBuffer[T any](capacity int, policy OverflowPolicy) (*RingBuffer[T], error) {
	if capacity < 1 {
		return nil, ErrCapacity
	}
	return &RingBuffer[T]{slots: make([]T, capacity), policy: policy}, nil
}

func mustRing[T any](capacity int, policy OverflowPolicy) *RingBuffer[T] {
	r, err := NewRingBuffer[T](capacity, policy)
	if err != nil {
		panic(err)
	}
	return r
}

func (r *RingBuffer[T]) Capacity() int       { return len(r.slots) }
func (r *RingBuffer[T]) Size() int           { return int(r.tail - r.head) }
func (r *RingBuffer[T]) IsEmpty() bool       { return r.head == r.tail }
func (r *RingBuffer[T]) IsFull() bool        { return r.tail-r.head == int64(len(r.slots)) }
func (r *RingBuffer[T]) HeadSequence() int64 { return r.head }
func (r *RingBuffer[T]) TailSequence() int64 { return r.tail }
func (r *RingBuffer[T]) Dropped() int64      { return r.dropped }

// IndexOf is the slot for a sequence number. With a power-of-two capacity this could be int(seq & (capacity-1)).
func (r *RingBuffer[T]) IndexOf(seq int64) int { return int(seq % int64(len(r.slots))) }

// OfferLast enqueues at the back. Returns false only under Reject when full. O(1), no element ever moves.
func (r *RingBuffer[T]) OfferLast(value T) bool {
	if r.IsFull() {
		if r.policy == Reject {
			return false
		}
		var zero T
		r.slots[r.IndexOf(r.head)] = zero // DropOldest: the oldest slot is exactly the one tail is about to reuse
		r.head++
		r.dropped++
	}
	r.slots[r.IndexOf(r.tail)] = value
	r.tail++
	return true
}

// OfferFirst enqueues at the front, stealing the slot just before head. Under DropOldest when full, drops the newest.
func (r *RingBuffer[T]) OfferFirst(value T) bool {
	if r.IsFull() {
		if r.policy == Reject {
			return false
		}
		r.tail--
		var zero T
		r.slots[r.IndexOf(r.tail)] = zero
		r.dropped++
	}
	r.head-- // may go negative conceptually; see the rebase guard below
	if r.head < 0 {
		r.rebase()
	}
	r.slots[r.IndexOf(r.head)] = value
	return true
}

// rebase keeps sequences non-negative so % is a valid index (Go's % truncates toward zero like Java's).
func (r *RingBuffer[T]) rebase() {
	r.head += int64(len(r.slots))
	r.tail += int64(len(r.slots))
}

// PollFirst dequeues the oldest element; ok is false when empty.
func (r *RingBuffer[T]) PollFirst() (T, bool) {
	var zero T
	if r.IsEmpty() {
		return zero, false
	}
	i := r.IndexOf(r.head)
	v := r.slots[i]
	r.slots[i] = zero
	r.head++
	return v, true
}

// PollLast dequeues the newest element; ok is false when empty.
func (r *RingBuffer[T]) PollLast() (T, bool) {
	var zero T
	if r.IsEmpty() {
		return zero, false
	}
	r.tail--
	i := r.IndexOf(r.tail)
	v := r.slots[i]
	r.slots[i] = zero
	return v, true
}

// PeekFirst reads the oldest element.
func (r *RingBuffer[T]) PeekFirst() (T, bool) {
	if r.IsEmpty() {
		var zero T
		return zero, false
	}
	return r.slots[r.IndexOf(r.head)], true
}

// PeekLast reads the newest element.
func (r *RingBuffer[T]) PeekLast() (T, bool) {
	if r.IsEmpty() {
		var zero T
		return zero, false
	}
	return r.slots[r.IndexOf(r.tail-1)], true
}

// Get is O(1) random access to the i-th element from the front: the thing a linked deque cannot do.
func (r *RingBuffer[T]) Get(i int) (T, error) {
	if i < 0 || i >= r.Size() {
		var zero T
		return zero, fmt.Errorf("%w: index %d size %d", ErrIndexOutOfBounds, i, r.Size())
	}
	return r.slots[r.IndexOf(r.head+int64(i))], nil
}

// Clear empties the buffer and resets the sequences.
func (r *RingBuffer[T]) Clear() {
	var zero T
	for i := range r.slots {
		r.slots[i] = zero
	}
	r.head, r.tail = 0, 0
}

// Each visits every element in logical (sequence) order.
func (r *RingBuffer[T]) Each(visit func(T)) {
	for seq := r.head; seq < r.tail; seq++ {
		visit(r.slots[r.IndexOf(seq)])
	}
}

// SlotsView is a debug view of the raw slots, in physical order, with the head and tail positions marked.
func (r *RingBuffer[T]) SlotsView() string {
	var sb strings.Builder
	for i := range r.slots {
		if i > 0 {
			sb.WriteByte(' ')
		}
		mark := ""
		if !r.IsEmpty() && i == r.IndexOf(r.head) {
			mark += "H"
		}
		if i == r.IndexOf(r.tail) && !r.IsFull() {
			mark += "T"
		}
		if r.IsFull() && i == r.IndexOf(r.head) {
			mark += "T"
		}
		cell := fmt.Sprint(r.slots[i])
		if r.slotIsEmpty(i) {
			cell = "."
		}
		sb.WriteString("[" + cell + "]" + mark)
	}
	return sb.String()
}

// slotIsEmpty reports whether the physical slot holds no live element.
func (r *RingBuffer[T]) slotIsEmpty(i int) bool {
	for seq := r.head; seq < r.tail; seq++ {
		if r.IndexOf(seq) == i {
			return false
		}
	}
	return true
}

func (r *RingBuffer[T]) String() string {
	var sb strings.Builder
	sb.WriteString("[")
	for s := r.head; s < r.tail; s++ {
		fmt.Fprint(&sb, r.slots[r.IndexOf(s)])
		if s+1 < r.tail {
			sb.WriteString(", ")
		}
	}
	fmt.Fprintf(&sb, "] head=%d tail=%d size=%d", r.head, r.tail, r.Size())
	return sb.String()
}

// BracketsBalanced is the classic stack use: check that brackets in a message body are balanced.
// O(n) with the stack holding open brackets.
func BracketsBalanced(text string) bool {
	open := mustStack[rune](8)
	for _, c := range text {
		switch c {
		case '(', '[', '{':
			open.Push(c)
		case ')', ']', '}':
			if open.IsEmpty() {
				return false
			}
			o, _ := open.Pop()
			if (c == ')' && o != '(') || (c == ']' && o != '[') || (c == '}' && o != '{') {
				return false
			}
		}
	}
	return open.IsEmpty()
}

func main() {
	fmt.Println("== Stack: compensating a saga in reverse order ==")
	compensations := mustStack[string](2)
	steps := []string{"reserve funds on wallet", "post ledger hold", "request capture at acquirer"}
	undo := []string{"release wallet reservation", "reverse ledger hold", "void acquirer authorization"}
	for i := range steps {
		fmt.Printf("step %d: %s  -> push undo: %s\n", i+1, steps[i], undo[i])
		compensations.Push(undo[i])
	}
	fmt.Printf("step 4 failed; unwinding %d compensations (capacity grew to %d):\n", compensations.Size(), compensations.Capacity())
	for !compensations.IsEmpty() {
		v, _ := compensations.Pop()
		fmt.Println("  pop -> " + v)
	}
	fmt.Printf("brackets balanced in {\"a\":[1,2,{\"b\":3}]} ? %v\n", BracketsBalanced("{\"a\":[1,2,{\"b\":3}]}"))
	fmt.Printf("brackets balanced in {\"a\":[1,2}   ? %v\n", BracketsBalanced("{\"a\":[1,2}"))
	fmt.Println()

	fmt.Println("== Ring buffer: wraparound with capacity 4 (sequence -> slot = seq % 4) ==")
	ring := mustRing[string](4, Reject)
	for _, e := range []string{"e0", "e1", "e2", "e3"} {
		ring.OfferLast(e)
	}
	fmt.Printf("offer e0..e3   : %s   %v\n", ring.SlotsView(), ring)
	fmt.Printf("offer e4 (full): accepted=%v  (REJECT: producer must back off)\n", ring.OfferLast("e4"))
	p1, _ := ring.PollFirst()
	p2, _ := ring.PollFirst()
	fmt.Printf("poll -> %s, poll -> %s\n", p1, p2)
	fmt.Printf("after 2 polls  : %s   %v\n", ring.SlotsView(), ring)
	ring.OfferLast("e4")
	ring.OfferLast("e5")
	fmt.Printf("offer e4, e5   : %s   %v\n", ring.SlotsView(), ring)
	fmt.Printf("e5 has sequence 5 and lives in slot 5 %% 4 = %d; logical order is still %v\n", ring.IndexOf(5), ring)
	g0, _ := ring.Get(0)
	g3, _ := ring.Get(3)
	fmt.Printf("get(0)=%s get(3)=%s  (O(1) by index, through the wrap)\n", g0, g3)
	fmt.Println()

	fmt.Println("== Deque: both ends in O(1) ==")
	deque := mustRing[int](5, Reject)
	deque.OfferLast(2)
	deque.OfferLast(3)
	deque.OfferFirst(1)
	deque.OfferFirst(0)
	fmt.Printf("offerLast 2,3 then offerFirst 1,0 -> %v\n", deque)
	last, _ := deque.PollLast()
	first, _ := deque.PollFirst()
	fmt.Printf("pollLast -> %d, pollFirst -> %d, now %v\n", last, first, deque)
	fmt.Println()

	fmt.Println("== Bounded queue semantics: a slow consumer under two policies ==")
	rejecting := mustRing[string](4, Reject)
	dropping := mustRing[string](4, DropOldest)
	rejected := 0
	for i := 0; i < 10; i++ {
		event := fmt.Sprintf("payment.%d", i)
		if !rejecting.OfferLast(event) {
			rejected++
		}
		dropping.OfferLast(event)
	}
	fmt.Println("10 events offered, consumer read none:")
	fmt.Printf("  REJECT      : holds %v; rejected=%d (the producer saw every rejection)\n", rejecting, rejected)
	fmt.Printf("  DROP_OLDEST : holds %v; dropped=%d (the consumer resumes at offset %d)\n", dropping, dropping.Dropped(), dropping.HeadSequence())
	fmt.Println()

	fmt.Println("== Broker framing: sequences are offsets ==")
	partition := mustRing[string](8, DropOldest)
	for i := 0; i < 12; i++ {
		partition.OfferLast(fmt.Sprintf("msg%d", i))
	}
	fmt.Printf("12 messages into a capacity-8 log: retained offsets %d..%d, evicted %d\n",
		partition.HeadSequence(), partition.TailSequence()-1, partition.Dropped())
	consumerOffset := int64(2)
	fmt.Printf("a consumer at offset %d is behind the retention window: it must reset to %d and has lost %d messages\n",
		consumerOffset, partition.HeadSequence(), partition.HeadSequence()-consumerOffset)
	consumerOffset = 9
	next, _ := partition.Get(int(consumerOffset - partition.HeadSequence()))
	fmt.Printf("a consumer at offset %d reads %s next; %d messages of lag\n", consumerOffset, next, partition.TailSequence()-consumerOffset)
}
