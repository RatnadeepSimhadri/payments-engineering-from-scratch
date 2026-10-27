// Lesson: phases/08-messaging-and-streams/09-backpressure/docs/en.md
// Backpressure and Flow Control
// Run:  go run .
// Test: go test ./...
// Go 1.22+, standard library only. Same design as the Java and Python versions. Pull-based demand follows the
// Reactive Streams specification (Subscription.request(n), rule 3.17: demand is additive and never exceeds Long.MAX_VALUE).
package main

import (
	"errors"
	"fmt"
	"math"
	"sort"
)

// PaymentEvent is a payment event flowing from the authorization service to the ledger poster.
type PaymentEvent struct {
	ID             int64
	ProducedAtTick int64
	Amount         Money
}

// OverflowPolicy is what a bounded queue does when it is full and another item is offered.
type OverflowPolicy int

const (
	// Block: refuse the item; the caller keeps it and must retry. The producer slows down.
	Block OverflowPolicy = iota
	// DropNewest: refuse and discard the new item. Freshest data lost.
	DropNewest
	// DropOldest: evict the head to make room. Oldest data lost.
	DropOldest
)

// OfferResult is the outcome of a single offer.
type OfferResult int

const (
	Accepted OfferResult = iota
	RejectedFull
	DroppedNewest
	EvictedOldest
)

// IntMax models the "unbounded" queue capacity, as Java's Integer.MAX_VALUE does.
const IntMax = math.MaxInt32

// BoundedQueue is a FIFO queue with a hard capacity and an explicit overflow policy. Capacity IntMax models the
// "unbounded" queue that every system has by default and that is the thing this lesson argues against.
type BoundedQueue[T any] struct {
	items    []T
	capacity int
	policy   OverflowPolicy
	dropped  int64
	maxDepth int
}

func NewBoundedQueue[T any](capacity int, policy OverflowPolicy) (*BoundedQueue[T], error) {
	if capacity <= 0 {
		return nil, errors.New("capacity must be positive")
	}
	return &BoundedQueue[T]{capacity: capacity, policy: policy}, nil
}

func (q *BoundedQueue[T]) Offer(item T) OfferResult {
	if len(q.items) < q.capacity {
		q.items = append(q.items, item)
		q.maxDepth = max(q.maxDepth, len(q.items))
		return Accepted
	}
	switch q.policy {
	case DropNewest:
		q.dropped++
		return DroppedNewest
	case DropOldest:
		q.items = q.items[1:]
		q.dropped++
		q.items = append(q.items, item)
		return EvictedOldest
	default:
		return RejectedFull
	}
}

// Poll removes and returns the head; ok is false when the queue is empty (Java returns null).
func (q *BoundedQueue[T]) Poll() (T, bool) {
	var zero T
	if len(q.items) == 0 {
		return zero, false
	}
	head := q.items[0]
	q.items[0] = zero // let the GC reclaim it
	q.items = q.items[1:]
	return head, true
}

func (q *BoundedQueue[T]) Size() int { return len(q.items) }

func (q *BoundedQueue[T]) IsFull() bool { return len(q.items) >= q.capacity }

func (q *BoundedQueue[T]) Capacity() int { return q.capacity }

func (q *BoundedQueue[T]) Dropped() int64 { return q.dropped }

func (q *BoundedQueue[T]) MaxDepth() int { return q.maxDepth }

// ErrInvalidDemand: request(n) with n <= 0 (Reactive Streams rule 3.9).
var ErrInvalidDemand = errors.New("request(n) requires n > 0 (Reactive Streams rule 3.9)")

// DemandSubscription is pull-based demand, as in Reactive Streams. The consumer calls Request(n) to say how many more
// items it can take; the producer may emit only while outstanding demand is positive. Demand is additive and
// saturates at math.MaxInt64 instead of overflowing (spec rule 3.17).
type DemandSubscription struct {
	demand int64
}

func (s *DemandSubscription) Request(n int64) error {
	if n <= 0 {
		return ErrInvalidDemand
	}
	next := s.demand + n
	if next < 0 { // wrapped: saturate, never wrap
		next = math.MaxInt64
	}
	s.demand = next
	return nil
}

func (s *DemandSubscription) HasDemand() bool { return s.demand > 0 }

// TryConsumeOne consumes one unit of demand; returns false if none was available.
func (s *DemandSubscription) TryConsumeOne() bool {
	if s.demand <= 0 {
		return false
	}
	if s.demand != math.MaxInt64 {
		s.demand--
	}
	return true
}

func (s *DemandSubscription) Outstanding() int64 { return s.demand }

// Strategy is one of the five ways to connect a fast producer to a slow consumer.
type Strategy int

const (
	Unbounded Strategy = iota
	StrategyDropNewest
	StrategyDropOldest
	StrategyBlock
	Pull
)

// Strategies lists every strategy in declaration order, like Java's Strategy.values().
var Strategies = []Strategy{Unbounded, StrategyDropNewest, StrategyDropOldest, StrategyBlock, Pull}

func (s Strategy) String() string {
	switch s {
	case Unbounded:
		return "UNBOUNDED"
	case StrategyDropNewest:
		return "DROP_NEWEST"
	case StrategyDropOldest:
		return "DROP_OLDEST"
	case StrategyBlock:
		return "BLOCK"
	default:
		return "PULL"
	}
}

// Stats is what happened over a run. Every count is exact and the two accounting identities tie them together:
//
//	OfferedLoad == Produced + Throttled                     (every request was either admitted or turned away)
//	Produced == Delivered + Dropped + InFlight + InQueue    (every admitted event is in exactly one place)
type Stats struct {
	Strategy             Strategy
	OfferedLoad          int64
	Produced             int64
	Throttled            int64
	Delivered            int64
	Dropped              int64
	InFlight             int64
	InQueue              int64
	RejectedOffers       int64
	ProducerBlockedTicks int64
	MaxQueueDepth        int
	MaxLatencyTicks      int64
	P99LatencyTicks      int64
	DeliveredAmount      Money
}

func (s Stats) Conserves() bool {
	return s.OfferedLoad == s.Produced+s.Throttled && s.Produced == s.Delivered+s.Dropped+s.InFlight+s.InQueue
}

// FlowSimulation is a deterministic tick-based simulation of one producer, one queue, one consumer. Upstream requests
// arrive at ~producerRate per tick with seeded jitter; the consumer drains consumerRate per tick but stalls (processes
// nothing) on a seeded fraction of ticks, like a GC pause or a slow downstream call. The producer is a request
// handler: while it is blocked holding an event the queue refused, it cannot admit new requests, and those are counted
// as throttled (the HTTP 429 / slow-accept the caller sees). No goroutines, no sleeping: the tick counter is the clock.
type FlowSimulation struct {
	strategy                 Strategy
	producerRate             int
	producerJitter           int
	consumerRate             int
	consumerStallProbability float64
	queueCapacity            int
	random                   *JavaRandom
}

func NewFlowSimulation(strategy Strategy, producerRate, producerJitter, consumerRate int, consumerStallProbability float64, queueCapacity int, seed int64) *FlowSimulation {
	return &FlowSimulation{strategy, producerRate, producerJitter, consumerRate, consumerStallProbability, queueCapacity, NewJavaRandom(seed)}
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func (f *FlowSimulation) Run(ticks int) Stats {
	policy := Block
	switch f.strategy {
	case StrategyDropNewest:
		policy = DropNewest
	case StrategyDropOldest:
		policy = DropOldest
	}
	capacity := f.queueCapacity
	if f.strategy == Unbounded {
		capacity = IntMax
	}
	queue := must(NewBoundedQueue[PaymentEvent](capacity, policy))
	subscription := &DemandSubscription{}
	if f.strategy == Pull {
		must(0, subscription.Request(int64(f.queueCapacity))) // initial demand = buffer size
	}

	var inFlight *PaymentEvent // produced, refused by the queue, held by the blocked producer
	latencies := []int64{}
	var offered, produced, throttled, delivered, rejected, blockedTicks int64
	nextID := int64(1)
	deliveredAmount := Zero(USD)

	for tick := 0; tick < ticks; tick++ {
		blockedThisTick := false

		// Producer, step 1: retry the event it is holding from last tick, if any.
		if inFlight != nil {
			if queue.Offer(*inFlight) == RejectedFull {
				rejected++
				blockedThisTick = true
			} else {
				inFlight = nil
			}
		}

		// Producer, step 2: admit this tick's requests, one at a time, until blocked.
		want := f.producerRate
		if f.producerJitter != 0 {
			want += int(f.random.NextInt(int32(f.producerJitter) + 1))
		}
		for i := 0; i < want; i++ {
			offered++
			cents := 100 + int64(f.random.NextInt(9900)) // drawn for every request so all strategies see the identical load
			if inFlight != nil {                         // BLOCK: handler is stuck, caller sees 429 / slow accept
				throttled++
				continue
			}
			if f.strategy == Pull && !subscription.TryConsumeOne() { // PULL: no demand, refuse before producing
				throttled++
				blockedThisTick = true
				continue
			}
			e := PaymentEvent{nextID, int64(tick), OfMinor(cents, USD)}
			nextID++
			produced++
			if queue.Offer(e) == RejectedFull {
				rejected++
				inFlight = &e
				blockedThisTick = true
			}
			// Accepted, DroppedNewest, EvictedOldest: the queue took responsibility for the event
		}
		if blockedThisTick {
			blockedTicks++
		}

		// Consumer: drain up to consumerRate, unless stalled this tick.
		stalled := f.random.NextDouble() < f.consumerStallProbability
		processed := 0
		if !stalled {
			for i := 0; i < f.consumerRate; i++ {
				e, ok := queue.Poll()
				if !ok {
					break
				}
				delivered++
				processed++
				latencies = append(latencies, int64(tick)-e.ProducedAtTick)
				deliveredAmount = must(deliveredAmount.Plus(e.Amount))
			}
		}
		if f.strategy == Pull && processed > 0 {
			must(0, subscription.Request(int64(processed))) // demand = capacity just freed
		}
	}

	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	var maxLat, p99 int64
	if len(latencies) > 0 {
		maxLat = latencies[len(latencies)-1]
		p99 = latencies[min(len(latencies)-1, int(math.Floor(float64(len(latencies))*0.99)))]
	}
	var inFlightCount int64
	if inFlight != nil {
		inFlightCount = 1
	}
	return Stats{f.strategy, offered, produced, throttled, delivered, queue.Dropped(), inFlightCount,
		int64(queue.Size()), rejected, blockedTicks, queue.MaxDepth(), maxLat, p99, deliveredAmount}
}

func row(s Stats) string {
	return fmt.Sprintf("%-12s offered=%6d produced=%6d throttled=%5d delivered=%6d dropped=%5d inQueue=%5d rejectedOffers=%5d maxDepth=%6d maxLat=%5d p99Lat=%4d",
		s.Strategy, s.OfferedLoad, s.Produced, s.Throttled, s.Delivered, s.Dropped, s.InQueue,
		s.RejectedOffers, s.MaxQueueDepth, s.MaxLatencyTicks, s.P99LatencyTicks)
}

func main() {
	// Authorization service emits ~12 events/tick (10 + jitter 0..4); ledger poster handles 10/tick and stalls 10% of ticks.
	// Sustained overload: the consumer cannot keep up and something has to give.
	const producerRate, jitter, consumerRate, capacity, ticks = 10, 4, 10, 64, 2000
	const stall = 0.10
	const seed = 42

	fmt.Printf("== Fast producer (~12/tick), slow consumer (10/tick, 10%% stalls), queue capacity %d, %d ticks, seed %d ==\n", capacity, ticks, seed)
	fmt.Println("One tick is one scheduling quantum; latency is in ticks from production to delivery.")
	fmt.Println()
	for _, s := range Strategies {
		st := NewFlowSimulation(s, producerRate, jitter, consumerRate, stall, capacity, seed).Run(ticks)
		fmt.Printf("%s  conserves=%t\n", row(st), st.Conserves())
	}

	fmt.Println()
	fmt.Println("== Same strategies when the consumer is fast enough (12/tick, no stalls): backpressure costs nothing ==")
	for _, s := range Strategies {
		st := NewFlowSimulation(s, producerRate, jitter, 13, 0.0, capacity, seed).Run(ticks)
		fmt.Println(row(st))
	}

	fmt.Println()
	fmt.Println("== Reading the table ==")
	fmt.Println("UNBOUNDED : nothing dropped, nothing throttled, queue depth and latency grow without limit. This is the OOM at 3 a.m.")
	fmt.Println("DROP_*    : bounded memory and latency, but admitted payment events are gone. Dropped money is a ledger incident.")
	fmt.Println("BLOCK     : bounded memory and latency, nothing admitted is lost; overload surfaces as throttled requests, discovered by collision (rejectedOffers).")
	fmt.Println("PULL      : same bounds and the same throttling, but the producer knows the demand up front and never builds an event it cannot deliver.")
}
