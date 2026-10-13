// Lesson: phases/01-complexity-and-core-structures/09-heaps-and-priority-queues/docs/en.md
// Heaps and Priority Queues from Scratch
// Run:  go run .
// Test: go test ./...
// Sources: Williams, "Algorithm 232: Heapsort" (CACM 1964); Floyd, "Algorithm 245: Treesort 3" (CACM 1964, the O(n) heapify);
// CLRS ch. 6. FakeClock and SeededRandom copied from Phase 0, Lesson 03. Go 1.22+, standard library only.
package main

import (
	"errors"
	"fmt"
	"strings"
)

// Clock is a source of time that tests can control. Copied from Phase 0, Lesson 03.
type Clock interface{ NowMillis() int64 }

// FakeClock moves only when told to.
type FakeClock struct{ now int64 }

func NewFakeClock(startMillis int64) *FakeClock { return &FakeClock{now: startMillis} }
func (c *FakeClock) NowMillis() int64           { return c.now }
func (c *FakeClock) Advance(millis int64) error {
	if millis < 0 {
		return fmt.Errorf("cannot advance by a negative duration: %d", millis)
	}
	c.now += millis
	return nil
}

// SeededRandom is SplitMix64, copied from Phase 0, Lesson 03: the same sequence for the same seed in Java, Python, and Go.
type SeededRandom struct{ state uint64 }

func NewSeededRandom(seed int64) *SeededRandom { return &SeededRandom{state: uint64(seed)} }

func (r *SeededRandom) nextUint64() uint64 {
	r.state += 0x9E3779B97F4A7C15
	z := r.state
	z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
	z = (z ^ (z >> 27)) * 0x94D049BB133111EB
	return z ^ (z >> 31)
}

func (r *SeededRandom) NextInt(bound int) int {
	if bound <= 0 {
		panic(fmt.Sprintf("bound must be positive: %d", bound))
	}
	return int((r.nextUint64() >> 33) % uint64(bound))
}

var ErrEmpty = errors.New("heap is empty")

// MinHeap is a binary min-heap in a slice. Invariant: for every index i > 0, compare(items[(i-1)/2], items[i]) <= 0.
// The root (index 0) is therefore the minimum. Heap order is partial: siblings are unordered.
type MinHeap[T any] struct {
	items   []T
	compare func(a, b T) int
}

func NewMinHeap[T any](compare func(a, b T) int) *MinHeap[T] {
	return &MinHeap[T]{compare: compare}
}

// Heapify is Floyd's bottom-up construction: sift down every internal node from the last parent to the root. O(n).
func Heapify[T any](source []T, compare func(a, b T) int) *MinHeap[T] {
	h := &MinHeap[T]{items: append([]T(nil), source...), compare: compare}
	for i := len(h.items)/2 - 1; i >= 0; i-- {
		h.siftDown(i)
	}
	return h
}

func (h *MinHeap[T]) Push(item T) {
	h.items = append(h.items, item)
	h.siftUp(len(h.items) - 1)
}

func (h *MinHeap[T]) Peek() (T, error) {
	if len(h.items) == 0 {
		var zero T
		return zero, ErrEmpty
	}
	return h.items[0], nil
}

func (h *MinHeap[T]) Pop() (T, error) {
	if len(h.items) == 0 {
		var zero T
		return zero, ErrEmpty
	}
	top := h.items[0]
	last := h.items[len(h.items)-1]
	h.items = h.items[:len(h.items)-1]
	if len(h.items) > 0 {
		h.items[0] = last
		h.siftDown(0)
	}
	return top, nil
}

func (h *MinHeap[T]) Size() int     { return len(h.items) }
func (h *MinHeap[T]) IsEmpty() bool { return len(h.items) == 0 }
func (h *MinHeap[T]) swap(a, b int) { h.items[a], h.items[b] = h.items[b], h.items[a] }

func (h *MinHeap[T]) siftUp(i int) {
	for i > 0 {
		parent := (i - 1) / 2
		if h.compare(h.items[parent], h.items[i]) <= 0 {
			return
		}
		h.swap(parent, i)
		i = parent
	}
}

func (h *MinHeap[T]) siftDown(i int) {
	n := len(h.items)
	for {
		left, right, smallest := 2*i+1, 2*i+2, i
		if left < n && h.compare(h.items[left], h.items[smallest]) < 0 {
			smallest = left
		}
		if right < n && h.compare(h.items[right], h.items[smallest]) < 0 {
			smallest = right
		}
		if smallest == i {
			return
		}
		h.swap(i, smallest)
		i = smallest
	}
}

// IsHeap checks the invariant over the whole slice. O(n); for tests and assertions, not for production paths.
func (h *MinHeap[T]) IsHeap() bool {
	for i := 1; i < len(h.items); i++ {
		if h.compare(h.items[(i-1)/2], h.items[i]) > 0 {
			return false
		}
	}
	return true
}

func NaturalOrder(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func ReverseOrder(a, b int) int { return NaturalOrder(b, a) }

// Entry is what an indexed heap stores: a unique key, its priority, and the sequence number that breaks ties.
type Entry[K comparable] struct {
	Key      K
	Priority int64
	Seq      int64
}

// CompareEntries orders lower priority first; equal priorities fall back to insertion order, which makes Pop stable.
func CompareEntries[K comparable](a, b Entry[K]) int {
	switch {
	case a.Priority < b.Priority:
		return -1
	case a.Priority > b.Priority:
		return 1
	case a.Seq < b.Seq:
		return -1
	case a.Seq > b.Seq:
		return 1
	}
	return 0
}

// IndexedMinHeap is a min-heap of unique keys with a position index, so DecreaseKey(key) is O(log n) instead of O(n).
// Invariant: index[entries[i].Key] == i for every i, in addition to heap order.
type IndexedMinHeap[K comparable] struct {
	entries []Entry[K]
	index   map[K]int
	nextSeq int64
}

func NewIndexedMinHeap[K comparable]() *IndexedMinHeap[K] {
	return &IndexedMinHeap[K]{index: make(map[K]int)}
}

func (h *IndexedMinHeap[K]) Push(key K, priority int64) error {
	if _, dup := h.index[key]; dup {
		return fmt.Errorf("key already present: %v", key)
	}
	h.entries = append(h.entries, Entry[K]{key, priority, h.nextSeq})
	h.nextSeq++
	h.index[key] = len(h.entries) - 1
	h.siftUp(len(h.entries) - 1)
	return nil
}

// DecreaseKey lowers the priority of an existing key. The entry keeps its Seq, so its tie-break rank is unchanged.
func (h *IndexedMinHeap[K]) DecreaseKey(key K, newPriority int64) error {
	i, ok := h.index[key]
	if !ok {
		return fmt.Errorf("key not present: %v", key)
	}
	current := h.entries[i]
	if newPriority > current.Priority {
		return fmt.Errorf("DecreaseKey cannot raise %v from %d to %d", key, current.Priority, newPriority)
	}
	h.entries[i] = Entry[K]{key, newPriority, current.Seq}
	h.siftUp(i)
	return nil
}

func (h *IndexedMinHeap[K]) Peek() (Entry[K], error) {
	if len(h.entries) == 0 {
		return Entry[K]{}, ErrEmpty
	}
	return h.entries[0], nil
}

func (h *IndexedMinHeap[K]) Pop() (Entry[K], error) {
	if len(h.entries) == 0 {
		return Entry[K]{}, ErrEmpty
	}
	top := h.entries[0]
	last := h.entries[len(h.entries)-1]
	h.entries = h.entries[:len(h.entries)-1]
	delete(h.index, top.Key)
	if len(h.entries) > 0 {
		h.entries[0] = last
		h.index[last.Key] = 0
		h.siftDown(0)
	}
	return top, nil
}

func (h *IndexedMinHeap[K]) Contains(key K) bool {
	_, ok := h.index[key]
	return ok
}

func (h *IndexedMinHeap[K]) PriorityOf(key K) (int64, error) {
	i, ok := h.index[key]
	if !ok {
		return 0, fmt.Errorf("key not present: %v", key)
	}
	return h.entries[i].Priority, nil
}

func (h *IndexedMinHeap[K]) Size() int     { return len(h.entries) }
func (h *IndexedMinHeap[K]) IsEmpty() bool { return len(h.entries) == 0 }

func (h *IndexedMinHeap[K]) siftUp(i int) {
	for i > 0 {
		parent := (i - 1) / 2
		if CompareEntries(h.entries[parent], h.entries[i]) <= 0 {
			return
		}
		h.swap(parent, i)
		i = parent
	}
}

func (h *IndexedMinHeap[K]) siftDown(i int) {
	n := len(h.entries)
	for {
		left, right, smallest := 2*i+1, 2*i+2, i
		if left < n && CompareEntries(h.entries[left], h.entries[smallest]) < 0 {
			smallest = left
		}
		if right < n && CompareEntries(h.entries[right], h.entries[smallest]) < 0 {
			smallest = right
		}
		if smallest == i {
			return
		}
		h.swap(i, smallest)
		i = smallest
	}
}

// swap is the one place the index map is maintained: every move of an entry updates its recorded position.
func (h *IndexedMinHeap[K]) swap(a, b int) {
	h.entries[a], h.entries[b] = h.entries[b], h.entries[a]
	h.index[h.entries[a].Key] = a
	h.index[h.entries[b].Key] = b
}

// IsConsistent checks both invariants: heap order and index consistency. For tests.
func (h *IndexedMinHeap[K]) IsConsistent() bool {
	for i := 1; i < len(h.entries); i++ {
		if CompareEntries(h.entries[(i-1)/2], h.entries[i]) > 0 {
			return false
		}
	}
	if len(h.index) != len(h.entries) {
		return false
	}
	for i, e := range h.entries {
		if recorded, ok := h.index[e.Key]; !ok || recorded != i {
			return false
		}
	}
	return true
}

// RetryJob is a retry due at a point in time: which payment, which attempt this will be.
type RetryJob struct {
	PaymentID   string
	Attempt     int
	DueAtMillis int64
}

const (
	BaseMillis int64 = 1_000
	CapMillis  int64 = 60_000
)

// RetryScheduler: the heap orders pending retries by due time, earliest first; ties (same millisecond)
// go in the order they were scheduled. Backoff is deterministic: base * 2^attempt, capped.
type RetryScheduler struct {
	clock Clock
	heap  *IndexedMinHeap[string]
	jobs  map[string]RetryJob
}

func NewRetryScheduler(clock Clock) *RetryScheduler {
	return &RetryScheduler{clock: clock, heap: NewIndexedMinHeap[string](), jobs: make(map[string]RetryJob)}
}

func BackoffMillis(attempt int) (int64, error) {
	if attempt < 0 {
		return 0, errors.New("attempt must be >= 0")
	}
	shift := attempt
	if shift > 20 {
		shift = 20
	}
	delay := BaseMillis << shift
	if delay > CapMillis {
		delay = CapMillis
	}
	return delay, nil
}

// ScheduleRetry schedules the next attempt for a payment after a failure and returns the due time.
func (s *RetryScheduler) ScheduleRetry(paymentID string, attempt int) (int64, error) {
	backoff, err := BackoffMillis(attempt)
	if err != nil {
		return 0, err
	}
	dueAt := s.clock.NowMillis() + backoff
	if err := s.heap.Push(paymentID, dueAt); err != nil {
		return 0, err
	}
	s.jobs[paymentID] = RetryJob{paymentID, attempt, dueAt}
	return dueAt, nil
}

// Expedite pulls a retry forward (an operator or a webhook says "try now"). It never pushes one later.
func (s *RetryScheduler) Expedite(paymentID string, newDueAtMillis int64) error {
	if err := s.heap.DecreaseKey(paymentID, newDueAtMillis); err != nil {
		return err
	}
	job := s.jobs[paymentID]
	s.jobs[paymentID] = RetryJob{paymentID, job.Attempt, newDueAtMillis}
	return nil
}

// PollDue returns every job whose due time has arrived, earliest first, and removes them from the schedule.
func (s *RetryScheduler) PollDue() []RetryJob {
	due := []RetryJob{}
	now := s.clock.NowMillis()
	for !s.heap.IsEmpty() {
		top, _ := s.heap.Peek()
		if top.Priority > now {
			break
		}
		e, _ := s.heap.Pop()
		due = append(due, s.jobs[e.Key])
		delete(s.jobs, e.Key)
	}
	return due
}

func (s *RetryScheduler) NextDueAtMillis() (int64, error) {
	top, err := s.heap.Peek()
	return top.Priority, err
}

func (s *RetryScheduler) Pending() int                      { return s.heap.Size() }
func (s *RetryScheduler) IsScheduled(paymentID string) bool { return s.heap.Contains(paymentID) }

func describe(jobs []RetryJob) string {
	parts := make([]string, len(jobs))
	for i, j := range jobs {
		parts[i] = fmt.Sprintf("%s(attempt %d)", j.PaymentID, j.Attempt)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func main() {
	fmt.Println("== heapify in O(n), then pop in sorted order ==")
	raw := []int{9, 4, 7, 1, 8, 2, 6, 3, 5}
	heap := Heapify(raw, NaturalOrder)
	fmt.Printf("input      = %v\n", formatInts(raw))
	var popped []int
	for !heap.IsEmpty() {
		popped = append(popped, must(heap.Pop()))
	}
	fmt.Printf("pop order  = %v\n", formatInts(popped))

	fmt.Println("\n== 1,000 seeded random pushes and pops keep the heap property ==")
	rng := NewSeededRandom(42)
	stress := NewMinHeap(NaturalOrder)
	pushes, pops := 0, 0
	for i := 0; i < 1_000; i++ {
		if stress.IsEmpty() || rng.NextInt(3) != 0 {
			stress.Push(rng.NextInt(10_000))
			pushes++
		} else {
			must(stress.Pop())
			pops++
		}
	}
	fmt.Printf("pushes=%d pops=%d size=%d isHeap=%v min=%d\n", pushes, pops, stress.Size(), stress.IsHeap(), must(stress.Peek()))

	fmt.Println("\n== Retry scheduler: earliest due first, ties in scheduling order ==")
	clock := NewFakeClock(1_700_000_000_000)
	scheduler := NewRetryScheduler(clock)
	fmt.Printf("t+0ms: pay-A attempt 2 due at +%dms\n", must(scheduler.ScheduleRetry("pay-A", 2))-clock.NowMillis())
	fmt.Printf("t+0ms: pay-B attempt 0 due at +%dms\n", must(scheduler.ScheduleRetry("pay-B", 0))-clock.NowMillis())
	fmt.Printf("t+0ms: pay-C attempt 0 due at +%dms (same due time as pay-B)\n", must(scheduler.ScheduleRetry("pay-C", 0))-clock.NowMillis())
	fmt.Printf("t+0ms: pay-D attempt 6 due at +%dms (capped at 60s)\n", must(scheduler.ScheduleRetry("pay-D", 6))-clock.NowMillis())
	if err := scheduler.Expedite("pay-D", clock.NowMillis()+500); err != nil {
		panic(err)
	}
	fmt.Println("t+0ms: operator expedites pay-D to +500ms (decreaseKey)")
	fmt.Printf("next due in %dms, pending=%d\n", must(scheduler.NextDueAtMillis())-clock.NowMillis(), scheduler.Pending())

	_ = clock.Advance(1_000)
	fmt.Printf("t+1000ms: due = %s\n", describe(scheduler.PollDue()))
	_ = clock.Advance(3_000)
	fmt.Printf("t+4000ms: due = %s\n", describe(scheduler.PollDue()))
	fmt.Printf("pending=%d\n", scheduler.Pending())
}

// formatInts prints a slice as [1, 2, 3], matching Java's List.toString and Python's list repr.
func formatInts(xs []int) string {
	parts := make([]string, len(xs))
	for i, x := range xs {
		parts[i] = fmt.Sprint(x)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}
