// Tests for phases/08-messaging-and-streams/09-backpressure. Run: go test ./...
package main

import (
	"errors"
	"math"
	"testing"
)

func check(t *testing.T, name string, condition bool) {
	t.Helper()
	if !condition {
		t.Errorf("%s", name)
	}
}

func queue(t *testing.T, capacity int, policy OverflowPolicy) *BoundedQueue[string] {
	t.Helper()
	q, err := NewBoundedQueue[string](capacity, policy)
	if err != nil {
		t.Fatal(err)
	}
	return q
}

func poll(q *BoundedQueue[string]) string {
	v, ok := q.Poll()
	if !ok {
		return "<empty>"
	}
	return v
}

// overloaded: ~12/tick in, 10/tick out with 10% stalls, capacity 64, 2000 ticks, seed 42: the demo's load.
func overloaded(s Strategy) Stats { return NewFlowSimulation(s, 10, 4, 10, 0.10, 64, 42).Run(2000) }

func comfortable(s Strategy) Stats { return NewFlowSimulation(s, 10, 4, 13, 0.0, 64, 42).Run(2000) }

func TestBoundedQueuePolicies(t *testing.T) {
	block := queue(t, 2, Block)
	check(t, "offer accepted below capacity", block.Offer("a") == Accepted && block.Offer("b") == Accepted)
	check(t, "queue reports full", block.IsFull())
	check(t, "BLOCK refuses when full", block.Offer("c") == RejectedFull)
	check(t, "BLOCK does not count a refusal as a drop", block.Dropped() == 0 && block.Size() == 2)
	check(t, "BLOCK preserves FIFO order", poll(block) == "a" && poll(block) == "b" && poll(block) == "<empty>")

	newest := queue(t, 2, DropNewest)
	newest.Offer("a")
	newest.Offer("b")
	check(t, "DROP_NEWEST discards the offered item", newest.Offer("c") == DroppedNewest)
	check(t, "DROP_NEWEST keeps the old items", newest.Dropped() == 1 && poll(newest) == "a" && poll(newest) == "b")

	oldest := queue(t, 2, DropOldest)
	oldest.Offer("a")
	oldest.Offer("b")
	check(t, "DROP_OLDEST evicts the head", oldest.Offer("c") == EvictedOldest)
	check(t, "DROP_OLDEST keeps the newest items", oldest.Dropped() == 1 && poll(oldest) == "b" && poll(oldest) == "c")
	check(t, "maxDepth never exceeds capacity", oldest.MaxDepth() == 2)
	_, err := NewBoundedQueue[string](0, Block)
	check(t, "zero capacity rejected", err != nil)
	check(t, "capacity is reported", block.Capacity() == 2)
}

func TestDemandSubscription(t *testing.T) {
	sub := &DemandSubscription{}
	check(t, "no demand before request", !sub.HasDemand() && !sub.TryConsumeOne())
	sub.Request(2)
	check(t, "demand is additive", sub.Outstanding() == 2)
	sub.Request(3)
	check(t, "demand accumulates", sub.Outstanding() == 5)
	for i := 0; i < 5; i++ {
		sub.TryConsumeOne()
	}
	check(t, "demand is consumed one emission at a time", !sub.HasDemand() && !sub.TryConsumeOne())
	check(t, "request(0) is a spec violation", errors.Is(sub.Request(0), ErrInvalidDemand))
	check(t, "request(-1) is a spec violation", errors.Is(sub.Request(-1), ErrInvalidDemand))
	sub.Request(math.MaxInt64)
	sub.Request(1)
	check(t, "demand saturates at MaxInt64 instead of wrapping (rule 3.17)", sub.Outstanding() == math.MaxInt64)
	sub.TryConsumeOne()
	check(t, "unbounded demand stays unbounded", sub.Outstanding() == math.MaxInt64)
}

func TestJavaRandomMatchesTheJDK(t *testing.T) {
	// new java.util.Random(42): nextInt(5) sequence 0,3,0,... and nextDouble 0.7275636800328681 from a fresh seed.
	r := NewJavaRandom(42)
	check(t, "first nextDouble of seed 42", math.Abs(r.NextDouble()-0.7275636800328681) < 1e-15)
	r2 := NewJavaRandom(42)
	check(t, "nextInt(bound) stays in range", r2.NextInt(5) >= 0 && r2.NextInt(5) < 5 && r2.NextInt(64) < 64)
}

func TestSustainedOverload(t *testing.T) {
	unbounded := overloaded(Unbounded)
	dropNewest := overloaded(StrategyDropNewest)
	dropOldest := overloaded(StrategyDropOldest)
	blocked := overloaded(StrategyBlock)
	pulled := overloaded(Pull)
	for _, s := range []Stats{unbounded, dropNewest, dropOldest, blocked, pulled} {
		t.Run(s.Strategy.String(), func(t *testing.T) {
			check(t, "conservation holds for "+s.Strategy.String(), s.Conserves())
		})
	}
	check(t, "identical seeded load across strategies", unbounded.OfferedLoad == blocked.OfferedLoad && unbounded.OfferedLoad == pulled.OfferedLoad)

	// UNBOUNDED: nothing lost, nothing throttled, but memory and latency are unbounded
	check(t, "UNBOUNDED drops nothing and throttles nothing", unbounded.Dropped == 0 && unbounded.Throttled == 0)
	check(t, "UNBOUNDED queue depth grows far past the bounded capacity", unbounded.MaxQueueDepth > 64*10)
	check(t, "UNBOUNDED latency grows with the backlog", unbounded.MaxLatencyTicks > 100)
	check(t, "UNBOUNDED backlog is the undelivered events", unbounded.InQueue == unbounded.Produced-unbounded.Delivered)

	// DROP_*: bounded memory and latency, but payment events are destroyed
	check(t, "DROP_NEWEST bounds queue depth", dropNewest.MaxQueueDepth == 64)
	check(t, "DROP_NEWEST loses events", dropNewest.Dropped > 0 && dropNewest.Throttled == 0)
	check(t, "DROP_OLDEST loses the same number of events", dropOldest.Dropped == dropNewest.Dropped)
	check(t, "DROP_OLDEST has lower latency than DROP_NEWEST (fresh data wins)", dropOldest.MaxLatencyTicks < dropNewest.MaxLatencyTicks)

	// BLOCK and PULL: bounded memory, bounded latency, nothing lost; overload moves to the producer
	check(t, "BLOCK drops nothing", blocked.Dropped == 0)
	check(t, "BLOCK bounds queue depth", blocked.MaxQueueDepth == 64)
	check(t, "BLOCK bounds latency", blocked.MaxLatencyTicks <= 64/10*3)
	check(t, "BLOCK throttles upstream requests instead", blocked.Throttled > 0)
	check(t, "BLOCK discovers overload by collision", blocked.RejectedOffers > 0)
	check(t, "PULL drops nothing", pulled.Dropped == 0)
	check(t, "PULL bounds queue depth to the initial demand", pulled.MaxQueueDepth == 64)
	check(t, "PULL never offers into a full queue", pulled.RejectedOffers == 0)
	diff := pulled.Throttled - blocked.Throttled
	check(t, "PULL throttles the same load BLOCK does, give or take the in-flight item", diff <= 1 && diff >= -1)
	check(t, "every strategy delivers the same number of events (the consumer is the bottleneck)",
		blocked.Delivered == unbounded.Delivered && pulled.Delivered == unbounded.Delivered)
	check(t, "Money sums stay exact under load", blocked.DeliveredAmount.Currency == USD && blocked.DeliveredAmount.MinorUnits > 0)

	// the exact counts the Java demo prints, reproduced through JavaRandom
	check(t, "seeded load matches Java: offered=23969 delivered=18150", unbounded.OfferedLoad == 23969 && unbounded.Delivered == 18150)
	check(t, "seeded BLOCK matches Java: throttled=5764 rejectedOffers=1977", blocked.Throttled == 5764 && blocked.RejectedOffers == 1977)
	check(t, "seeded DROP matches Java: dropped=5765", dropNewest.Dropped == 5765)
}

func TestComfortableLoad(t *testing.T) {
	for _, s := range Strategies {
		t.Run(s.String(), func(t *testing.T) {
			c := comfortable(s)
			check(t, "comfortable load: "+s.String()+" delivers everything", c.Delivered == c.OfferedLoad && c.Dropped == 0 && c.Throttled == 0)
		})
	}
}

func TestDeterminism(t *testing.T) {
	check(t, "simulation is deterministic", overloaded(StrategyBlock) == overloaded(StrategyBlock))
}
