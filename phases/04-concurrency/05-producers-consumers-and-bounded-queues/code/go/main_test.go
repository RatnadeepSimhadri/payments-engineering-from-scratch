// Tests for phases/04-concurrency/05-producers-consumers-and-bounded-queues. Run: go test -race ./...
package main

import (
	"errors"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func spinUntil(t *testing.T, what string, predicate func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !predicate() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		runtime.Gosched()
	}
}

func TestFifoOrderAndWraparound(t *testing.T) {
	q, _ := NewBoundedBlockingQueue[string](3, nil)
	q.Put("a")
	q.Put("b")
	q.Put("c")
	if q.Size() != 3 || !q.IsFull() {
		t.Fatalf("size %d", q.Size())
	}
	if q.Take() != "a" || q.Take() != "b" {
		t.Fatal("not FIFO")
	}
	q.Put("d")
	q.Put("e") // tail wraps around the array
	if q.Take() != "c" || q.Take() != "d" || q.Take() != "e" || !q.IsEmpty() {
		t.Fatal("ring buffer wraparound broke order")
	}
	if q.Stats().HighWaterMark != 3 {
		t.Fatalf("high-water mark %d", q.Stats().HighWaterMark)
	}
}

func TestOfferAndPollWithoutWaiting(t *testing.T) {
	q, _ := NewBoundedBlockingQueue[string](2, nil)
	if !q.Offer("x", 0) || !q.Offer("y", 0) {
		t.Fatal("offer must accept while there is room")
	}
	if q.Offer("z", 0) || q.Size() != 2 {
		t.Fatal("offer must refuse when full and leave size unchanged")
	}
	x, ok1 := q.Poll(0)
	y, ok2 := q.Poll(0)
	_, ok3 := q.Poll(0)
	if x != "x" || !ok1 || y != "y" || !ok2 || ok3 {
		t.Fatal("poll must return items then report empty")
	}
}

func TestTimedOfferOnFullQueueTimesOutExactly(t *testing.T) {
	clock := NewFakeClock(1_000)
	q, _ := NewBoundedBlockingQueue[string](1, clock)
	q.Put("only")
	if q.Offer("more", 250_000_000) {
		t.Fatal("offer on a full queue must time out")
	}
	if clock.Nanos() != 1_000+250_000_000 {
		t.Fatalf("clock advanced to %d", clock.Nanos())
	}
	if s := q.Stats(); s.ProducerWaits != 1 || s.WaitingProducers != 0 {
		t.Fatalf("stats %+v", s)
	}
}

func TestTimedPollReturnsAtOnceWhenItemPresent(t *testing.T) {
	clock := NewFakeClock(1_000)
	q, _ := NewBoundedBlockingQueue[string](1, clock)
	q.Put("only")
	before := clock.Nanos()
	if item, ok := q.Poll(250_000_000); !ok || item != "only" || clock.Nanos() != before {
		t.Fatal("poll must not wait when an item is present")
	}
}

func TestTimedPollOnEmptyQueueTimesOutExactly(t *testing.T) {
	clock := NewFakeClock(1_000)
	q, _ := NewBoundedBlockingQueue[string](1, clock)
	before := clock.Nanos()
	if _, ok := q.Poll(40_000_000); ok || clock.Nanos() != before+40_000_000 {
		t.Fatalf("poll should time out after exactly 40 ms, clock %d", clock.Nanos())
	}
	if q.Stats().ConsumerWaits != 1 {
		t.Fatalf("consumer waits %d", q.Stats().ConsumerWaits)
	}
}

func TestTimedOfferOnNonFullQueueDoesNotWait(t *testing.T) {
	clock := NewFakeClock(1_000)
	q, _ := NewBoundedBlockingQueue[string](1, clock)
	before := clock.Nanos()
	if !q.Offer("back", 250_000_000) || clock.Nanos() != before {
		t.Fatal("offer must not wait when there is room")
	}
}

func TestCapacityMustBePositive(t *testing.T) {
	if _, err := NewBoundedBlockingQueue[string](0, nil); !errors.Is(err, ErrCapacity) {
		t.Fatalf("expected ErrCapacity, got %v", err)
	}
	if _, err := NewBoundedBlockingQueue[string](-1, nil); !errors.Is(err, ErrCapacity) {
		t.Fatalf("expected ErrCapacity, got %v", err)
	}
}

func TestBlockedProducerWakesWhenSlotOpens(t *testing.T) {
	q, _ := NewBoundedBlockingQueue[string](1, nil)
	q.Put("first")
	done := make(chan struct{})
	go func() {
		q.Put("second")
		close(done)
	}()
	spinUntil(t, "producer to block", func() bool { return q.Stats().WaitingProducers == 1 })
	if q.Size() != 1 {
		t.Fatalf("size %d", q.Size())
	}
	if q.Take() != "first" {
		t.Fatal("wrong item")
	}
	<-done
	if q.Take() != "second" || q.Stats().WaitingProducers != 0 {
		t.Fatal("blocked producer did not complete its put")
	}
}

func TestBlockedConsumerWakesWhenItemArrives(t *testing.T) {
	q, _ := NewBoundedBlockingQueue[string](1, nil)
	received := make(chan string, 1)
	go func() { received <- q.Take() }()
	spinUntil(t, "consumer to block", func() bool { return q.Stats().WaitingConsumers == 1 })
	q.Put("hello")
	if got := <-received; got != "hello" {
		t.Fatalf("got %q", got)
	}
	if q.Stats().ConsumerWaits < 1 {
		t.Fatal("consumer wait not recorded")
	}
}

func TestSystemClockTimedOfferReallyTimesOut(t *testing.T) {
	// The one test that uses the real clock: a short timeout on a full queue must return false
	// and must not hang, which exercises the AfterFunc-plus-Broadcast implementation of Wait.
	q, _ := NewBoundedBlockingQueue[string](1, nil)
	q.Put("only")
	start := time.Now()
	if q.Offer("more", int64(5*time.Millisecond)) {
		t.Fatal("offer must time out")
	}
	if time.Since(start) < 5*time.Millisecond {
		t.Fatal("offer returned before the timeout elapsed")
	}
}

func TestFastProducerIsHeldAtTheBound(t *testing.T) {
	pipe, _ := NewBoundedBlockingQueue[Payment](4, nil)
	script := Script("auth", 20, 1_999)
	var done sync.WaitGroup
	done.Add(1)
	go Producer(pipe, script, &done)
	spinUntil(t, "producer to hit the bound", func() bool { return pipe.Stats().WaitingProducers == 1 })
	if pipe.Size() != 4 {
		t.Fatalf("size %d while producer waits", pipe.Size())
	}
	posted := OfMinor(0, USD)
	for _, expected := range script {
		p := pipe.Take()
		if p.ID != expected.ID {
			t.Fatalf("expected %s, got %s", expected.ID, p.ID)
		}
		posted, _ = posted.Plus(p.Amount)
	}
	done.Wait()
	if posted != OfMinor(20*1_999, USD) {
		t.Fatalf("posted %v", posted)
	}
	if s := pipe.Stats(); s.HighWaterMark != 4 || s.ProducerWaits < 1 || s.WaitingProducers != 0 {
		t.Fatalf("stats %+v", s)
	}
}

func TestManyProducersManyConsumers(t *testing.T) {
	shared, _ := NewBoundedBlockingQueue[Payment](8, nil)
	producers, perProducer, consumers := 4, 500, 2
	total := producers * perProducer
	var producersDone sync.WaitGroup
	producersDone.Add(producers)
	for p := 0; p < producers; p++ {
		go Producer(shared, Script("p"+strconv.Itoa(p), perProducer, 1), &producersDone)
	}
	seen := make([][]Payment, consumers)
	var consumersDone sync.WaitGroup
	consumersDone.Add(consumers)
	for c := 0; c < consumers; c++ {
		go func(slot int) {
			defer consumersDone.Done()
			for i := 0; i < total/consumers; i++ {
				seen[slot] = append(seen[slot], shared.Take())
			}
		}(c)
	}
	producersDone.Wait()
	consumersDone.Wait()
	ids := map[string]bool{}
	for _, consumed := range seen {
		last := map[string]int{}
		for _, p := range consumed {
			ids[p.ID] = true
			producer, seqText, _ := strings.Cut(p.ID, "-")
			seq, _ := strconv.Atoi(seqText)
			if prev, ok := last[producer]; ok && seq <= prev {
				t.Fatalf("consumer saw %s after %s-%d", p.ID, producer, prev)
			}
			last[producer] = seq
		}
	}
	if len(ids) != total || !shared.IsEmpty() {
		t.Fatalf("consumed %d distinct of %d, empty=%v", len(ids), total, shared.IsEmpty())
	}
	if shared.Stats().HighWaterMark > 8 {
		t.Fatalf("high-water mark %d exceeded capacity", shared.Stats().HighWaterMark)
	}
}
