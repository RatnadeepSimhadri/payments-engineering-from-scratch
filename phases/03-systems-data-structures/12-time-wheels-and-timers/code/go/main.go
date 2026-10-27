// Lesson: phases/03-systems-data-structures/12-time-wheels-and-timers/docs/en.md
// Hierarchical Timing Wheels
// Run:  go run .
// Test: go test ./...
// Sources: Varghese & Lauck, "Hashed and Hierarchical Timing Wheels" (SOSP 1987); Apache Kafka
// TimingWheel / SystemTimer (purgatory); Netty HashedWheelTimer. Go 1.22+, standard library only. Same design as the Java and Python versions.
package main

import (
	"container/heap"
	"errors"
	"fmt"
	"math"
	"math/bits"
	"slices"
	"strings"
)

var (
	// ErrInvalidArgument is returned where the Java throws IllegalArgumentException.
	ErrInvalidArgument = errors.New("invalid argument")
	// ErrOverflow is returned where the Java's Math.addExact / multiplyExact throw ArithmeticException.
	ErrOverflow = errors.New("long overflow")
)

// ---------------------------------------------------------------- integer helpers ----

// floorDiv and floorMod are Math.floorDiv / Math.floorMod: Go's / and % truncate toward zero.
func floorDiv(a, b int64) int64 {
	q := a / b
	if (a%b != 0) && ((a < 0) != (b < 0)) {
		q--
	}
	return q
}

func floorMod(a, b int64) int64 { return a - floorDiv(a, b)*b }

// ceilDiv is Math.ceilDiv.
func ceilDiv(a, b int64) int64 { return -floorDiv(-a, b) }

func addChecked(a, b int64) (int64, error) {
	r := a + b
	if (a >= 0) == (b >= 0) && (r >= 0) != (a >= 0) {
		return 0, ErrOverflow
	}
	return r, nil
}

func mulChecked(a, b int64) (int64, error) {
	if a == 0 || b == 0 {
		return 0, nil
	}
	r := a * b
	if r/b != a || (a == -1 && b == math.MinInt64) || (b == -1 && a == math.MinInt64) {
		return 0, ErrOverflow
	}
	return r, nil
}

// ---------------------------------------------------------------- Clock ----

// Clock is the injected time source. Tests and the demo use ManualClock; production would read time.Now().
type Clock interface {
	NowMillis() int64
}

type ManualClock struct{ now int64 }

func NewManualClock(startMillis int64) *ManualClock { return &ManualClock{now: startMillis} }

func (c *ManualClock) NowMillis() int64 { return c.now }

func (c *ManualClock) Advance(millis int64) error {
	if millis < 0 {
		return fmt.Errorf("%w: time does not go backwards", ErrInvalidArgument)
	}
	c.now += millis
	return nil
}

// ---------------------------------------------------------------- TimerEntry ----

// TimerEntry is a scheduled callback. Doubly linked so cancellation unlinks in O(1) without searching the bucket.
// DeadlineMillis is the tick boundary the entry fires on (the requested time rounded up to the wheel's tick).
type TimerEntry struct {
	ID             int64
	DeadlineMillis int64
	Task           func()
	Prev, Next     *TimerEntry
	Bucket         *Bucket
	cancelled      bool
	fired          bool
}

func NewTimerEntry(id, deadlineMillis int64, task func()) *TimerEntry {
	if task == nil {
		panic("task must not be nil") // Objects.requireNonNull: a programming error
	}
	return &TimerEntry{ID: id, DeadlineMillis: deadlineMillis, Task: task}
}

// Cancel is O(1): unlink from whatever bucket holds this entry. Returns false if already cancelled or already fired.
func (e *TimerEntry) Cancel() bool {
	if e.cancelled || e.fired {
		return false
	}
	e.cancelled = true
	if e.Bucket != nil {
		e.Bucket.Remove(e)
	}
	return true
}

// Fire runs the task once. Returns false if the entry was cancelled or has already fired.
func (e *TimerEntry) Fire() bool {
	if e.cancelled || e.fired {
		return false
	}
	e.fired = true
	e.Task()
	return true
}

func (e *TimerEntry) IsCancelled() bool { return e.cancelled }
func (e *TimerEntry) HasFired() bool    { return e.fired }

// ---------------------------------------------------------------- Bucket ----

// Bucket is one slot of a wheel: a circular doubly linked list with a sentinel. Add/Remove are O(1); Flush is O(k).
type Bucket struct {
	sentinel *TimerEntry
	size     int
}

func NewBucket() *Bucket {
	s := NewTimerEntry(-1, math.MinInt64, func() {})
	s.Prev, s.Next = s, s
	return &Bucket{sentinel: s}
}

func (b *Bucket) Add(e *TimerEntry) {
	e.Bucket = b
	e.Prev = b.sentinel.Prev
	e.Next = b.sentinel
	b.sentinel.Prev.Next = e
	b.sentinel.Prev = e
	b.size++
}

func (b *Bucket) Remove(e *TimerEntry) {
	if e.Bucket != b {
		return
	}
	e.Prev.Next = e.Next
	e.Next.Prev = e.Prev
	e.Prev, e.Next = nil, nil
	e.Bucket = nil
	b.size--
}

// Flush removes every entry and hands each to the consumer, in insertion order.
func (b *Bucket) Flush(consumer func(*TimerEntry)) {
	e := b.sentinel.Next
	for e != b.sentinel {
		next := e.Next
		b.Remove(e)
		consumer(e)
		e = next
	}
}

func (b *Bucket) Size() int { return b.size }

// ---------------------------------------------------------------- TimingWheel ----

// TimingWheel is one level of the hierarchy. A wheel of WheelSize buckets, each spanning TickMillis, so the wheel
// covers Interval = TickMillis * WheelSize. Deadlines beyond the interval go to a lazily created overflow wheel whose
// tick is this wheel's whole interval. currentTime is always a multiple of TickMillis.
type TimingWheel struct {
	TickMillis  int64
	WheelSize   int
	Interval    int64
	currentTime int64
	buckets     []*Bucket
	overflow    *TimingWheel
	Cascades    int // entries moved down from this wheel's overflow
}

func NewTimingWheel(tickMillis int64, wheelSize int, startMillis int64) (*TimingWheel, error) {
	if tickMillis <= 0 || wheelSize <= 1 {
		return nil, fmt.Errorf("%w: tick must be > 0 and wheelSize > 1", ErrInvalidArgument)
	}
	interval, err := mulChecked(tickMillis, int64(wheelSize))
	if err != nil {
		return nil, err
	}
	w := &TimingWheel{TickMillis: tickMillis, WheelSize: wheelSize, Interval: interval,
		currentTime: startMillis - floorMod(startMillis, tickMillis), buckets: make([]*Bucket, wheelSize)}
	for i := range w.buckets {
		w.buckets[i] = NewBucket()
	}
	return w, nil
}

func (w *TimingWheel) CurrentTime() int64 { return w.currentTime }

func (w *TimingWheel) Levels() int {
	if w.overflow == nil {
		return 1
	}
	return 1 + w.overflow.Levels()
}

// Add places an entry. Returns false if its deadline falls inside the current tick (already due): the caller
// runs it. Otherwise it lands in this wheel's bucket or recursively in an overflow wheel.
func (w *TimingWheel) Add(e *TimerEntry) bool {
	deadline := e.DeadlineMillis
	if e.IsCancelled() {
		return true // nothing to do, nothing to run
	}
	if deadline < w.currentTime+w.TickMillis {
		return false // due now
	}
	if deadline < w.currentTime+w.Interval {
		virtualID := floorDiv(deadline, w.TickMillis)
		w.buckets[floorMod(virtualID, int64(w.WheelSize))].Add(e)
		return true
	}
	if w.overflow == nil {
		// Interval * WheelSize cannot overflow for any workload this lesson runs; a wheel that deep is a programming error.
		overflow, err := NewTimingWheel(w.Interval, w.WheelSize, w.currentTime)
		if err != nil {
			panic(err)
		}
		w.overflow = overflow
	}
	return w.overflow.Add(e)
}

// AdvanceTo advances to timeMillis, tick by tick. Each tick expires the lowest wheel's current bucket, and
// when the time crosses an overflow wheel's tick boundary, that wheel's bucket cascades down one level.
// Entries that come due are handed to due in deadline-tick order, insertion order within a tick.
// Only the lowest wheel is driven this way; it drives the wheels above it.
func (w *TimingWheel) AdvanceTo(timeMillis int64, due func(*TimerEntry)) {
	for w.currentTime+w.TickMillis <= timeMillis {
		w.currentTime += w.TickMillis
		if w.overflow != nil {
			w.overflow.cascadeTo(w.currentTime, w, due)
		}
		w.buckets[floorMod(floorDiv(w.currentTime, w.TickMillis), int64(w.WheelSize))].Flush(func(e *TimerEntry) {
			if !e.IsCancelled() {
				due(e)
			}
		})
	}
}

// cascadeTo is called by the wheel below when the lowest wheel reaches a time that may be one of our tick boundaries.
// Entries in the bucket for the new current tick are re-inserted from the lowest wheel (root); each
// lands in a finer bucket or, if its deadline is inside the current lowest tick, fires now.
func (w *TimingWheel) cascadeTo(timeMillis int64, root *TimingWheel, due func(*TimerEntry)) {
	if timeMillis < w.currentTime+w.TickMillis {
		return // not our boundary yet
	}
	w.currentTime = timeMillis - floorMod(timeMillis, w.TickMillis)
	if w.overflow != nil {
		w.overflow.cascadeTo(w.currentTime, root, due)
	}
	w.buckets[floorMod(floorDiv(w.currentTime, w.TickMillis), int64(w.WheelSize))].Flush(func(e *TimerEntry) {
		if e.IsCancelled() {
			return
		}
		root.Cascades++
		if !root.Add(e) {
			due(e) // deadline inside the current lowest tick
		}
	})
}

func (w *TimingWheel) Pending() int {
	n := 0
	for _, b := range w.buckets {
		n += b.Size()
	}
	if w.overflow != nil {
		n += w.overflow.Pending()
	}
	return n
}

// ---------------------------------------------------------------- HierarchicalTimer ----

// HierarchicalTimer is the public face: schedule by delay, cancel by handle, and drive from an injected clock.
type HierarchicalTimer struct {
	clock  Clock
	wheel  *TimingWheel
	nextID int64
	fired  int64
}

func NewHierarchicalTimer(clock Clock, tickMillis int64, wheelSize int) (*HierarchicalTimer, error) {
	wheel, err := NewTimingWheel(tickMillis, wheelSize, clock.NowMillis())
	if err != nil {
		return nil, err
	}
	return &HierarchicalTimer{clock: clock, wheel: wheel, nextID: 1}, nil
}

// Schedule a task for now + delay. The wheel fires only on tick boundaries, so the deadline is rounded UP
// to the next multiple of the tick: a timer is never early, and at most one tick late.
func (t *HierarchicalTimer) Schedule(delayMillis int64, task func()) (*TimerEntry, error) {
	if delayMillis < 0 {
		return nil, fmt.Errorf("%w: delay must be >= 0", ErrInvalidArgument)
	}
	deadline, err := addChecked(t.clock.NowMillis(), delayMillis)
	if err != nil {
		return nil, err
	}
	tick := t.wheel.TickMillis
	rounded, err := mulChecked(ceilDiv(deadline, tick), tick)
	if err != nil {
		return nil, err
	}
	e := NewTimerEntry(t.nextID, rounded, task)
	t.nextID++
	if !t.wheel.Add(e) && e.Fire() { // already due: run inline
		t.fired++
	}
	return e, nil
}

// Poll processes everything due at or before the clock's current time.
func (t *HierarchicalTimer) Poll() {
	t.wheel.AdvanceTo(t.clock.NowMillis(), func(e *TimerEntry) {
		if e.Fire() {
			t.fired++
		}
	})
}

func (t *HierarchicalTimer) Pending() int      { return t.wheel.Pending() }
func (t *HierarchicalTimer) Fired() int64      { return t.fired }
func (t *HierarchicalTimer) Levels() int       { return t.wheel.Levels() }
func (t *HierarchicalTimer) Cascades() int     { return t.wheel.Cascades }
func (t *HierarchicalTimer) TickMillis() int64 { return t.wheel.TickMillis }

// ---------------------------------------------------------------- HeapTimer ----

// entryHeap orders by deadline, then by id: the PriorityQueue comparator from the Java.
type entryHeap []*TimerEntry

func (h entryHeap) Len() int { return len(h) }
func (h entryHeap) Less(i, j int) bool {
	if h[i].DeadlineMillis != h[j].DeadlineMillis {
		return h[i].DeadlineMillis < h[j].DeadlineMillis
	}
	return h[i].ID < h[j].ID
}
func (h entryHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *entryHeap) Push(x any)   { *h = append(*h, x.(*TimerEntry)) }
func (h *entryHeap) Pop() any {
	old := *h
	x := old[len(old)-1]
	*h = old[:len(old)-1]
	return x
}

// HeapTimer is the structure a wheel is usually compared with: a binary heap keyed by deadline, with lazy cancellation.
type HeapTimer struct {
	clock  Clock
	heap   entryHeap
	nextID int64
	fired  int64
}

func NewHeapTimer(clock Clock) *HeapTimer { return &HeapTimer{clock: clock, nextID: 1} }

func (t *HeapTimer) Schedule(delayMillis int64, task func()) (*TimerEntry, error) {
	deadline, err := addChecked(t.clock.NowMillis(), delayMillis)
	if err != nil {
		return nil, err
	}
	e := NewTimerEntry(t.nextID, deadline, task)
	t.nextID++
	heap.Push(&t.heap, e) // O(log n)
	return e, nil
}

// Poll: cancelled entries stay in the heap until they surface: O(1) cancel, memory until the deadline.
func (t *HeapTimer) Poll() {
	now := t.clock.NowMillis()
	for t.heap.Len() > 0 && t.heap[0].DeadlineMillis <= now {
		e := heap.Pop(&t.heap).(*TimerEntry)
		if e.Fire() {
			t.fired++
		}
	}
}

func (t *HeapTimer) Pending() int { return t.heap.Len() }
func (t *HeapTimer) Fired() int64 { return t.fired }

// ---------------------------------------------------------------- Demo ----

// SameSetFired: within a tick the two structures may order differently, so compare the fired sets, not the sequences.
func SameSetFired(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	sa, sb := slices.Clone(a), slices.Clone(b)
	slices.Sort(sa)
	slices.Sort(sb)
	return slices.Equal(sa, sb)
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func mustHold(err error) {
	if err != nil {
		panic(err)
	}
}

func joinAny[T any](xs []T) string {
	parts := make([]string, len(xs))
	for i, x := range xs {
		parts[i] = fmt.Sprint(x)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func main() {
	fmt.Println("== 1. Authorization holds: schedule, fire, cancel on capture ==")
	clock := NewManualClock(1_000_000)
	timer := must(NewHierarchicalTimer(clock, 1, 8)) // 1 ms tick, 8 buckets per level
	log := []string{}
	must(timer.Schedule(3, func() { log = append(log, "auth-A expired") }))
	authB := must(timer.Schedule(5, func() { log = append(log, "auth-B expired") }))
	must(timer.Schedule(5, func() { log = append(log, "auth-C expired") }))
	must(timer.Schedule(12, func() { log = append(log, "auth-D expired (beyond the 8 ms wheel: overflow level)") }))
	fmt.Printf("  levels after scheduling = %d, pending = %d\n", timer.Levels(), timer.Pending())
	mustHold(clock.Advance(4))
	timer.Poll()
	fmt.Println("  t+4:  " + joinAny(log))
	cancelled := authB.Cancel()
	fmt.Printf("  auth-B captured, cancel() = %t (O(1) unlink), pending = %d\n", cancelled, timer.Pending())
	mustHold(clock.Advance(8))
	timer.Poll()
	fmt.Println("  t+12: " + joinAny(log))
	fmt.Printf("  cascades from the overflow wheel = %d\n", timer.Cascades())

	fmt.Println()
	fmt.Println("== 2. Three levels: 1 ms x 8 covers 8 ms, 8 ms x 8 covers 64 ms, 64 ms x 8 covers 512 ms ==")
	clock2 := NewManualClock(0)
	t2 := must(NewHierarchicalTimer(clock2, 1, 8))
	fireTimes := []int64{}
	delays := []int64{1, 7, 8, 9, 63, 64, 65, 300, 511}
	for _, d := range delays {
		must(t2.Schedule(d, func() { fireTimes = append(fireTimes, clock2.NowMillis()) }))
	}
	fmt.Printf("  delays %s -> levels = %d\n", joinAny(delays), t2.Levels())
	last := int64(0)
	for _, d := range delays {
		mustHold(clock2.Advance(d - last))
		last = d
		t2.Poll()
	}
	fmt.Println("  fired at   " + joinAny(fireTimes) + " (each at exactly its deadline)")
	fmt.Printf("  cascades = %d (one per entry per level it descended)\n", t2.Cascades())

	fmt.Println()
	fmt.Println("== 3. Tick granularity: a 10 ms tick cannot tell 23 ms from 29 ms ==")
	clock3 := NewManualClock(0)
	coarse := must(NewHierarchicalTimer(clock3, 10, 16))
	order := []string{}
	must(coarse.Schedule(29, func() { order = append(order, "29ms") }))
	must(coarse.Schedule(23, func() { order = append(order, "23ms") }))
	mustHold(clock3.Advance(25))
	coarse.Poll()
	fmt.Println("  at t=25 fired: " + joinAny(order) + " (nothing: both sit in the tick that ends at 30)")
	mustHold(clock3.Advance(5))
	coarse.Poll()
	fmt.Println("  at t=30 fired: " + joinAny(order) + " (insertion order, not deadline order, within a tick)")

	fmt.Println()
	fmt.Println("== 4. Wheel versus heap on the same seeded workload ==")
	rng := NewJavaRandom(2024)
	wc, hc := NewManualClock(0), NewManualClock(0)
	wheelT := must(NewHierarchicalTimer(wc, 1, 64))
	heapT := NewHeapTimer(hc)
	wheelOrder, heapOrder := []int64{}, []int64{}
	n := 200_000
	wheelEntries, heapEntries := make([]*TimerEntry, 0, n), make([]*TimerEntry, 0, n)
	for i := 0; i < n; i++ {
		delay := 1 + int64(rng.NextIntN(600_000)) // up to 10 minutes
		id := int64(i)
		wheelEntries = append(wheelEntries, must(wheelT.Schedule(delay, func() { wheelOrder = append(wheelOrder, id) })))
		heapEntries = append(heapEntries, must(heapT.Schedule(delay, func() { heapOrder = append(heapOrder, id) })))
	}
	cancelledCount := 0
	for i := 0; i < n; i += 3 { // capture one in three holds
		wheelEntries[i].Cancel()
		heapEntries[i].Cancel()
		cancelledCount++
	}
	fmt.Printf("  scheduled %d, cancelled %d -> wheel pending %d (freed on cancel), heap pending %d (freed lazily when polled)\n",
		n, cancelledCount, wheelT.Pending(), heapT.Pending())
	fmt.Printf("  wheel levels = %d for deadlines up to 600000 ms with 1 ms x 64 buckets\n", wheelT.Levels())
	for step := 0; step < 600; step++ {
		mustHold(wc.Advance(1_000))
		mustHold(hc.Advance(1_000))
		wheelT.Poll()
		heapT.Poll()
	}
	mustHold(wc.Advance(1_000))
	mustHold(hc.Advance(1_000))
	wheelT.Poll()
	heapT.Poll()
	fmt.Printf("  fired: wheel %d, heap %d, expected %d\n", wheelT.Fired(), heapT.Fired(), n-cancelledCount)
	fmt.Printf("  same set of timers fired = %t\n", SameSetFired(wheelOrder, heapOrder))
	fmt.Printf("  cascades = %d (each entry moves down at most levels-1 times)\n", wheelT.Cascades())

	fmt.Println()
	fmt.Println("== 5. Cost model ==")
	fmt.Println("  wheel: insert O(1), cancel O(1), tick O(1 + due); an idle wheel still pays one tick per tickMillis")
	fmt.Println("  heap:  insert O(log n), cancel O(1) lazy or O(n) eager, poll O(log n) per due; idle heap costs nothing")
	fmt.Printf("  with n = %d: log2(n) = %d comparisons per heap insert versus one bucket link\n", n, bits.Len64(uint64(n)))
}
