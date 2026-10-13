// Lesson: phases/04-concurrency/05-producers-consumers-and-bounded-queues/docs/en.md
// Producers, Consumers, and Bounded Queues
// Run:  go run .
// Test: go test -race ./...
// Sources: sync.Cond docs (no timed wait; Broadcast/Signal semantics); Hoare, "Monitors: An Operating System
// Structuring Concept" (1974); Go Memory Model (go.dev/ref/mem) for the Unlock/Lock edge.
// Go 1.22+, standard library only. Same design as the Java and Python versions.
package main

import (
	"errors"
	"fmt"
	"runtime"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Money (copied from Phase 10, Lesson 01)
// ---------------------------------------------------------------------------

type Currency struct {
	Code     string
	Exponent int
}

var USD = Currency{"USD", 2}

func (c Currency) MinorUnitsPerMajor() int64 {
	scale := int64(1)
	for i := 0; i < c.Exponent; i++ {
		scale *= 10
	}
	return scale
}

// Money is an exact amount in minor units of one currency; a value type, so safe to hand between goroutines.
type Money struct {
	MinorUnits int64
	Currency   Currency
}

func OfMinor(minorUnits int64, currency Currency) Money { return Money{minorUnits, currency} }

func (m Money) Plus(other Money) (Money, error) {
	if m.Currency != other.Currency {
		return Money{}, fmt.Errorf("cannot combine %s with %s", m.Currency.Code, other.Currency.Code)
	}
	return Money{m.MinorUnits + other.MinorUnits, m.Currency}, nil
}

func (m Money) String() string {
	magnitude := m.MinorUnits
	if magnitude < 0 {
		magnitude = -magnitude
	}
	scale := m.Currency.MinorUnitsPerMajor()
	text := fmt.Sprintf("%d", magnitude/scale)
	if m.Currency.Exponent > 0 {
		text += fmt.Sprintf(".%0*d", m.Currency.Exponent, magnitude%scale)
	}
	if m.MinorUnits < 0 {
		text = "-" + text
	}
	return text + " " + m.Currency.Code
}

// Payment is the unit of work flowing through the queue: an authorization waiting to be posted to the ledger.
type Payment struct {
	ID     string
	Amount Money
}

// ---------------------------------------------------------------------------
// Time, injected
// ---------------------------------------------------------------------------

// Clock reads time and performs a timed wait on a condition. sync.Cond has no timed wait, so the
// system clock arms a timer that broadcasts at the deadline; the fake clock advances its own time
// by the requested amount and returns at once, which is what an unsignalled timeout looks like.
type Clock interface {
	Nanos() int64
	Wait(cond *sync.Cond, nanos int64)
}

type SystemClock struct{ start time.Time }

func NewSystemClock() *SystemClock { return &SystemClock{start: time.Now()} }

func (c *SystemClock) Nanos() int64 { return int64(time.Since(c.start)) }

// Wait must be called with cond.L held, like cond.Wait. The timer callback takes the lock before
// broadcasting so it cannot fire in the window between the caller's check and its Wait.
func (c *SystemClock) Wait(cond *sync.Cond, nanos int64) {
	timer := time.AfterFunc(time.Duration(nanos), func() {
		cond.L.Lock()
		cond.Broadcast()
		cond.L.Unlock()
	})
	cond.Wait()
	timer.Stop()
}

type FakeClock struct{ now int64 }

func NewFakeClock(startNanos int64) *FakeClock { return &FakeClock{now: startNanos} }

func (c *FakeClock) Nanos() int64 { return c.now }

// Wait does no real waiting: the clock jumps to the deadline, as if the wait timed out unsignalled.
func (c *FakeClock) Wait(cond *sync.Cond, nanos int64) {
	if nanos > 0 {
		c.now += nanos
	}
}

// QueueStats are counters the queue keeps under its lock. Wait counts are events; waiting counts are current.
type QueueStats struct {
	ProducerWaits    int64
	ConsumerWaits    int64
	HighWaterMark    int
	WaitingProducers int
	WaitingConsumers int
}

// ---------------------------------------------------------------------------
// The queue
// ---------------------------------------------------------------------------

var ErrCapacity = errors.New("capacity must be positive")

// BoundedBlockingQueue is a FIFO ring buffer with a fixed capacity, built from one mutex and two
// condition variables. Put blocks while full, Take blocks while empty; Offer and Poll give up after
// a timeout. The bound is the point: a producer that outruns the consumer is slowed down, not buffered forever.
type BoundedBlockingQueue[T any] struct {
	items            []T
	head, tail       int // next take, next put
	count            int
	mu               sync.Mutex
	notFull          *sync.Cond
	notEmpty         *sync.Cond
	clock            Clock
	producerWaits    int64
	consumerWaits    int64
	highWaterMark    int
	waitingProducers int
	waitingConsumers int
}

func NewBoundedBlockingQueue[T any](capacity int, clock Clock) (*BoundedBlockingQueue[T], error) {
	if capacity <= 0 {
		return nil, fmt.Errorf("%w: %d", ErrCapacity, capacity)
	}
	if clock == nil {
		clock = NewSystemClock()
	}
	q := &BoundedBlockingQueue[T]{items: make([]T, capacity), clock: clock}
	q.notFull = sync.NewCond(&q.mu)
	q.notEmpty = sync.NewCond(&q.mu)
	return q, nil
}

func (q *BoundedBlockingQueue[T]) Capacity() int { return len(q.items) }

func (q *BoundedBlockingQueue[T]) Size() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.count
}

func (q *BoundedBlockingQueue[T]) IsEmpty() bool { return q.Size() == 0 }
func (q *BoundedBlockingQueue[T]) IsFull() bool  { return q.Size() == len(q.items) }

func (q *BoundedBlockingQueue[T]) Stats() QueueStats {
	q.mu.Lock()
	defer q.mu.Unlock()
	return QueueStats{q.producerWaits, q.consumerWaits, q.highWaterMark, q.waitingProducers, q.waitingConsumers}
}

// Put blocks while the queue is full. This is backpressure: the caller's goroutine is the buffer.
func (q *BoundedBlockingQueue[T]) Put(item T) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for q.count == len(q.items) { // for, not if: wakeups can be spurious or stolen
		q.producerWaits++
		q.waitingProducers++
		q.notFull.Wait()
		q.waitingProducers--
	}
	q.enqueue(item)
}

// Take blocks while the queue is empty.
func (q *BoundedBlockingQueue[T]) Take() T {
	q.mu.Lock()
	defer q.mu.Unlock()
	for q.count == 0 {
		q.consumerWaits++
		q.waitingConsumers++
		q.notEmpty.Wait()
		q.waitingConsumers--
	}
	return q.dequeue()
}

// Offer waits at most timeoutNanos for space; false means shed the load: tell the caller to retry later.
func (q *BoundedBlockingQueue[T]) Offer(item T, timeoutNanos int64) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	deadline := q.clock.Nanos() + timeoutNanos
	for q.count == len(q.items) {
		remaining := deadline - q.clock.Nanos()
		if remaining <= 0 {
			return false
		}
		q.producerWaits++
		q.waitingProducers++
		q.clock.Wait(q.notFull, remaining)
		q.waitingProducers--
	}
	q.enqueue(item)
	return true
}

// Poll waits at most timeoutNanos for an item; ok=false means the consumer can do housekeeping and come back.
func (q *BoundedBlockingQueue[T]) Poll(timeoutNanos int64) (item T, ok bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	deadline := q.clock.Nanos() + timeoutNanos
	for q.count == 0 {
		remaining := deadline - q.clock.Nanos()
		if remaining <= 0 {
			var zero T
			return zero, false
		}
		q.consumerWaits++
		q.waitingConsumers++
		q.clock.Wait(q.notEmpty, remaining)
		q.waitingConsumers--
	}
	return q.dequeue(), true
}

// Both helpers assume the lock is held and the precondition (space / an item) was checked.
func (q *BoundedBlockingQueue[T]) enqueue(item T) {
	q.items[q.tail] = item
	q.tail = (q.tail + 1) % len(q.items)
	q.count++
	if q.count > q.highWaterMark {
		q.highWaterMark = q.count
	}
	q.notEmpty.Signal() // one waiting consumer is enough: one item arrived
}

func (q *BoundedBlockingQueue[T]) dequeue() T {
	item := q.items[q.head]
	var zero T
	q.items[q.head] = zero // let the GC have it
	q.head = (q.head + 1) % len(q.items)
	q.count--
	q.notFull.Signal() // one waiting producer is enough: one slot opened
	return item
}

// Producer puts a scripted list of payments, then signals done.
func Producer(queue *BoundedBlockingQueue[Payment], script []Payment, done *sync.WaitGroup) {
	defer done.Done()
	for _, p := range script {
		queue.Put(p)
	}
}

func Script(prefix string, n int, minorUnitsEach int64) []Payment {
	script := make([]Payment, 0, n)
	for i := 0; i < n; i++ {
		script = append(script, Payment{fmt.Sprintf("%s-%d", prefix, i), OfMinor(minorUnitsEach, USD)})
	}
	return script
}

// UntilProducerWaiting yields (never sleeps for a duration) until the queue reports a waiting
// producer; bounded so a bug cannot hang the demo.
func UntilProducerWaiting[T any](queue *BoundedBlockingQueue[T]) {
	deadline := time.Now().Add(5 * time.Second)
	for queue.Stats().WaitingProducers == 0 {
		if time.Now().After(deadline) {
			panic("producer never blocked")
		}
		runtime.Gosched()
	}
}

func main() {
	fmt.Println("== A full queue refuses, it does not grow ==")
	ingest, _ := NewBoundedBlockingQueue[Payment](3, nil)
	for i := 1; i <= 5; i++ {
		p := Payment{fmt.Sprintf("auth-%d", i), OfMinor(1_999, USD)}
		verdict := "REFUSED: reply 429 Retry-After"
		if ingest.Offer(p, 0) {
			verdict = "accepted"
		}
		fmt.Printf("offer %s -> %s  (size %d/%d)\n", p.ID, verdict, ingest.Size(), ingest.Capacity())
	}

	fmt.Println("\n== A timed offer against a full queue, with a fake clock ==")
	clock := NewFakeClock(0)
	timed, _ := NewBoundedBlockingQueue[Payment](1, clock)
	timed.Put(Payment{"auth-a", OfMinor(100, USD)})
	accepted := timed.Offer(Payment{"auth-b", OfMinor(100, USD)}, 250_000_000)
	fmt.Printf("offer with 250 ms timeout -> %v; clock advanced %d ms; producer waits = %d\n",
		accepted, clock.Nanos()/1_000_000, timed.Stats().ProducerWaits)
	first, ok1 := timed.Poll(0)
	_, ok2 := timed.Poll(0)
	fmt.Printf("poll(0) on the one-item queue -> %s; poll(0) again -> %s\n", describe(first.ID, ok1), describe("", ok2))

	fmt.Println("\n== Backpressure: a fast producer, a slow consumer, capacity 4 ==")
	pipe, _ := NewBoundedBlockingQueue[Payment](4, nil)
	script := Script("auth", 20, 1_999)
	var producerDone sync.WaitGroup
	producerDone.Add(1)
	go Producer(pipe, script, &producerDone)
	UntilProducerWaiting(pipe) // the consumer does not start until the producer is stuck
	fmt.Printf("producer blocked with %d/%d items queued; it cannot run ahead of the ledger\n", pipe.Size(), pipe.Capacity())
	posted := OfMinor(0, USD)
	fifo := true
	for _, expected := range script {
		p := pipe.Take()
		fifo = fifo && p.ID == expected.ID
		posted, _ = posted.Plus(p.Amount)
	}
	producerDone.Wait()
	stats := pipe.Stats()
	fmt.Printf("consumed %d payments in FIFO order: %v; posted %v\n", len(script), fifo, posted)
	fmt.Printf("high-water mark %d (never above capacity), producer waited %d times, consumer waited %d times\n",
		stats.HighWaterMark, stats.ProducerWaits, stats.ConsumerWaits)

	fmt.Println("\n== Without the bound, the backlog lives in your heap ==")
	wide, _ := NewBoundedBlockingQueue[Payment](1_000, nil)
	for _, p := range script {
		wide.Put(p)
	}
	fmt.Printf("capacity 1000: all 20 queued, producer waited %d times, high-water mark %d: nothing told the producer to slow down\n",
		wide.Stats().ProducerWaits, wide.Stats().HighWaterMark)
}

func describe(id string, ok bool) string {
	if !ok {
		return "empty"
	}
	return id
}
