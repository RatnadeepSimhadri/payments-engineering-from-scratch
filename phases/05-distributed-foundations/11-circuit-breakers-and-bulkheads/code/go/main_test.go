// Tests for phases/05-distributed-foundations/11-circuit-breakers-and-bulkheads. Run: go test -race ./...
// Same cases as MainTest.java and test_main.py.
package main

import (
	"errors"
	"sync"
	"testing"
)

// 50% of at least 10 calls in a 5 s window of 1 s buckets; open for 3 s; 3 half-open probes.
var config = mustConfig(50, 10, 5_000, 1_000, 3_000, 3)

func drive(cb *CircuitBreaker, successes, failures int) {
	for i := 0; i < successes; i++ {
		cb.TryAcquire()
		cb.OnSuccess()
	}
	for i := 0; i < failures; i++ {
		cb.TryAcquire()
		cb.OnFailure()
	}
}

func seededWindow(t *testing.T) *SlidingWindow {
	w, err := NewSlidingWindow(5_000, 1_000)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		w.Record(0, true)
	}
	for i := 0; i < 12; i++ {
		w.Record(1_500, false)
	}
	return w
}

func TestWindowCountsAndIntegerRate(t *testing.T) {
	w := seededWindow(t)
	if w.Total(1_500) != 20 || w.Failures(1_500) != 12 || w.FailureRatePercent(1_500) != 60 {
		t.Fatalf("got total=%d failures=%d rate=%d", w.Total(1_500), w.Failures(1_500), w.FailureRatePercent(1_500))
	}
}

func TestWindowOldBucketAgesOut(t *testing.T) {
	w := seededWindow(t)
	if w.Total(5_000) != 12 || w.FailureRatePercent(5_000) != 100 {
		t.Fatalf("at t=5000 got total=%d rate=%d", w.Total(5_000), w.FailureRatePercent(5_000))
	}
}

func TestWindowEmptyRateIsZero(t *testing.T) {
	w := seededWindow(t)
	if w.Total(6_500) != 0 || w.FailureRatePercent(6_500) != 0 {
		t.Fatalf("at t=6500 got total=%d rate=%d", w.Total(6_500), w.FailureRatePercent(6_500))
	}
}

func TestWindowReusedBucketStartsFromZero(t *testing.T) {
	w := seededWindow(t)
	w.Record(5_000, true) // slot 0 is reused for the period starting at 5000
	if w.Total(5_000) != 13 || w.Failures(5_000) != 12 {
		t.Fatalf("got total=%d failures=%d", w.Total(5_000), w.Failures(5_000))
	}
	w.Reset()
	if w.Total(5_000) != 0 {
		t.Fatal("reset should empty every bucket")
	}
}

func TestWindowMustBeMultipleOfBucket(t *testing.T) {
	if _, err := NewSlidingWindow(5_000, 1_500); !errors.Is(err, ErrConfig) {
		t.Fatalf("expected ErrConfig, got %v", err)
	}
}

func TestStaysClosedBelowMinimumCalls(t *testing.T) {
	cb := NewCircuitBreaker(config, NewFakeClock(0))
	drive(cb, 5, 4)
	if cb.State() != Closed || cb.FailureRatePercent() != 44 {
		t.Fatalf("state=%s rate=%d", cb.State(), cb.FailureRatePercent())
	}
}

func TestOpensAtThresholdAndRecordsTransition(t *testing.T) {
	cb := NewCircuitBreaker(config, NewFakeClock(0))
	drive(cb, 5, 5)
	if cb.State() != Open {
		t.Fatalf("state=%s", cb.State())
	}
	tr := cb.Transitions()
	if len(tr) != 1 || tr[0] != (Transition{Closed, Open, 0}) {
		t.Fatalf("transitions=%v", tr)
	}
}

func TestOpenRejectsUntilDurationElapses(t *testing.T) {
	clock := NewFakeClock(0)
	cb := NewCircuitBreaker(config, clock)
	drive(cb, 5, 5)
	if cb.TryAcquire() || cb.Rejected() != 1 {
		t.Fatal("OPEN must reject immediately and count it")
	}
	clock.Advance(2_999)
	if cb.TryAcquire() || cb.State() != Open {
		t.Fatal("still OPEN at 2999 ms")
	}
	clock.Advance(1)
	if !cb.TryAcquire() || cb.State() != HalfOpen {
		t.Fatal("first caller at 3000 ms performs OPEN->HALF_OPEN and is admitted")
	}
}

func TestHalfOpenAdmitsExactlyThePermits(t *testing.T) {
	clock := NewFakeClock(0)
	cb := NewCircuitBreaker(config, clock)
	drive(cb, 0, 10)
	clock.Advance(3_000)
	got := []bool{cb.TryAcquire(), cb.TryAcquire(), cb.TryAcquire(), cb.TryAcquire()}
	want := []bool{true, true, true, false}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("admissions=%v", got)
		}
	}
	if cb.Rejected() != 1 {
		t.Fatalf("rejected=%d", cb.Rejected())
	}
}

func TestProbeFailureReopensAndRestartsTimer(t *testing.T) {
	clock := NewFakeClock(0)
	cb := NewCircuitBreaker(config, clock)
	drive(cb, 0, 10)
	clock.Advance(3_000)
	cb.TryAcquire()
	cb.OnFailure()
	if cb.State() != Open {
		t.Fatal("one failed probe re-opens")
	}
	clock.Advance(2_999)
	if cb.TryAcquire() {
		t.Fatal("re-opened timer runs from the failure, not the original trip")
	}
	clock.Advance(1)
	if !cb.TryAcquire() || cb.State() != HalfOpen {
		t.Fatal("half-open again after a full open duration")
	}
}

func TestAllProbesSucceedClosesAndResetsWindow(t *testing.T) {
	clock := NewFakeClock(0)
	cb := NewCircuitBreaker(config, clock)
	drive(cb, 0, 10)
	clock.Advance(3_000)
	cb.TryAcquire()
	cb.OnSuccess()
	if cb.State() != HalfOpen {
		t.Fatal("one of three probes is not enough")
	}
	cb.TryAcquire()
	cb.OnSuccess()
	cb.TryAcquire()
	cb.OnSuccess()
	if cb.State() != Closed || cb.FailureRatePercent() != 0 {
		t.Fatalf("state=%s rate=%d", cb.State(), cb.FailureRatePercent())
	}
	tr := cb.Transitions()
	if len(tr) != 3 || tr[2] != (Transition{HalfOpen, Closed, 3_000}) {
		t.Fatalf("transitions=%v", tr)
	}
}

func TestLateSuccessWhileOpenIsIgnored(t *testing.T) {
	cb := NewCircuitBreaker(config, NewFakeClock(0))
	drive(cb, 0, 10)
	cb.OnSuccess()
	if cb.State() != Open || len(cb.Transitions()) != 1 {
		t.Fatal("a late result from before the trip must not change state")
	}
}

func TestConfigValidation(t *testing.T) {
	cases := []struct {
		threshold, min       int
		window, bucket, open int64
		permits              int
	}{
		{0, 10, 5_000, 1_000, 3_000, 3},
		{50, 10, 5_000, 1_500, 3_000, 3},
		{50, 10, 5_000, 1_000, 3_000, 0},
	}
	for _, c := range cases {
		if _, err := NewBreakerConfig(c.threshold, c.min, c.window, c.bucket, c.open, c.permits); !errors.Is(err, ErrConfig) {
			t.Fatalf("expected ErrConfig for %+v, got %v", c, err)
		}
	}
}

func TestBulkheadAdmitsUpToLimitThenRejects(t *testing.T) {
	bh, _ := NewBulkhead(3)
	got := []bool{bh.TryAcquire(), bh.TryAcquire(), bh.TryAcquire(), bh.TryAcquire()}
	if got[0] != true || got[1] != true || got[2] != true || got[3] != false {
		t.Fatalf("admissions=%v", got)
	}
	if bh.InFlight() != 3 || bh.Available() != 0 || bh.Rejected() != 1 {
		t.Fatalf("inFlight=%d available=%d rejected=%d", bh.InFlight(), bh.Available(), bh.Rejected())
	}
	if err := bh.Release(); err != nil || bh.Available() != 1 || !bh.TryAcquire() {
		t.Fatal("release returns one permit")
	}
}

func TestBulkheadReleaseWithoutAcquireIsAnError(t *testing.T) {
	bh, _ := NewBulkhead(1)
	if err := bh.Release(); !errors.Is(err, ErrReleaseWithoutAcquire) {
		t.Fatalf("got %v", err)
	}
	if _, err := NewBulkhead(0); !errors.Is(err, ErrConfig) {
		t.Fatalf("got %v", err)
	}
}

func TestBulkheadIsSafeUnderConcurrentUse(t *testing.T) {
	bh, _ := NewBulkhead(4)
	var wg sync.WaitGroup
	var mu sync.Mutex
	maxSeen := 0
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				if bh.TryAcquire() {
					mu.Lock()
					if n := bh.InFlight(); n > maxSeen {
						maxSeen = n
					}
					mu.Unlock()
					_ = bh.Release()
				}
			}
		}()
	}
	wg.Wait()
	if maxSeen > 4 || bh.InFlight() != 0 {
		t.Fatalf("maxSeen=%d inFlight=%d", maxSeen, bh.InFlight())
	}
}

func TestBulkheadRejectionIsNotADependencyFailure(t *testing.T) {
	cb := NewCircuitBreaker(config, NewFakeClock(0))
	bh, _ := NewBulkhead(2)
	g := &GuardedCall{cb, bh}
	if g.Begin() != Admitted || g.Begin() != Admitted || g.Begin() != RejectedBulkhead {
		t.Fatal("two admitted, third rejected by the bulkhead")
	}
	if cb.FailureRatePercent() != 0 || len(cb.Transitions()) != 0 {
		t.Fatal("bulkhead rejections must not enter the breaker's window")
	}
	g.Complete(false)
	g.Complete(false)
	if bh.InFlight() != 0 || cb.FailureRatePercent() != 100 {
		t.Fatalf("inFlight=%d rate=%d", bh.InFlight(), cb.FailureRatePercent())
	}
}

func TestRejectedOpenReturnsTheBulkheadPermit(t *testing.T) {
	cb := NewCircuitBreaker(config, NewFakeClock(0))
	bh, _ := NewBulkhead(2)
	g := &GuardedCall{cb, bh}
	for i := 0; i < 10; i++ {
		g.Call(func() bool { return false })
	}
	if cb.State() != Open {
		t.Fatal("ten failures trip the breaker")
	}
	if g.Begin() != RejectedOpen || bh.InFlight() != 0 {
		t.Fatal("REJECTED_OPEN must release the permit it took")
	}
	if g.Call(func() bool { return true }) != OutcomeRejectedOpen {
		t.Fatal("Call while OPEN reports REJECTED_OPEN")
	}
}

func TestPanicIsAFailureAndTrueIsASuccess(t *testing.T) {
	cb := NewCircuitBreaker(config, NewFakeClock(0))
	bh, _ := NewBulkhead(1)
	g := &GuardedCall{cb, bh}
	if g.Call(func() bool { panic("boom") }) != OutcomeFailure {
		t.Fatal("a panic in the operation is a failure")
	}
	if g.Call(func() bool { return true }) != OutcomeSuccess || bh.InFlight() != 0 {
		t.Fatal("a normal operation is a success and releases its permit")
	}
}

func TestSimulationTotalsMatchJavaAndPython(t *testing.T) {
	r := RunOutageSimulation(config, 10, 50, 25_000, 5_000, 15_000, 2_000)
	if r.TotalSuccess != 260 || r.TotalFailure != 23 || r.TotalRejectedOpen != 157 || r.TotalRejectedBulkhead != 60 {
		t.Fatalf("ok=%d fail=%d rejOpen=%d rejBulk=%d", r.TotalSuccess, r.TotalFailure, r.TotalRejectedOpen, r.TotalRejectedBulkhead)
	}
}

func TestSimulationTripsAt9sAndRecovers(t *testing.T) {
	r := RunOutageSimulation(config, 10, 50, 25_000, 5_000, 15_000, 2_000)
	if r.Transitions[0] != (Transition{Closed, Open, 9_000}) {
		t.Fatalf("first transition %v", r.Transitions[0])
	}
	if len(r.Transitions) != 5 || r.Transitions[4].To != Closed || r.Rows[24].StateAtEnd != Closed {
		t.Fatalf("transitions=%v", r.Transitions)
	}
}

func TestNoCallWaitsOnDeadProcessorOnceOpen(t *testing.T) {
	r := RunOutageSimulation(config, 10, 50, 25_000, 5_000, 15_000, 2_000)
	if r.CountIn(9_000, 14_000, OutcomeFailure) != 10 || r.CountIn(15_000, 17_000, OutcomeFailure) != 0 {
		t.Fatalf("failures 9-14s=%d, 15-17s=%d", r.CountIn(9_000, 14_000, OutcomeFailure), r.CountIn(15_000, 17_000, OutcomeFailure))
	}
}

func TestWithoutBreakerMoreCallsBurnOnTimeouts(t *testing.T) {
	neverTrips := mustConfig(50, 1_000_000, 5_000, 1_000, 3_000, 3)
	with := RunOutageSimulation(config, 10, 50, 25_000, 5_000, 15_000, 2_000)
	without := RunOutageSimulation(neverTrips, 10, 50, 25_000, 5_000, 15_000, 2_000)
	if without.TotalFailure != 50 || without.TotalRejectedOpen != 0 || without.TotalFailure <= with.TotalFailure {
		t.Fatalf("without breaker: fail=%d rejOpen=%d; with breaker fail=%d", without.TotalFailure, without.TotalRejectedOpen, with.TotalFailure)
	}
}
