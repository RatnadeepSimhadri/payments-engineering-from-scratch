// Tests for phases/05-distributed-foundations/10-retries-backoff-and-jitter. Run: go test ./...
package main

import (
	"errors"
	"slices"
	"testing"
)

var plain = mustPolicy(100, 10_000, 6, None)

func TestRngMatchesJavaAndPython(t *testing.T) {
	rng := NewRng(42)
	if got := rng.NextLong(); got != 6255019084209693600 {
		t.Fatalf("first value: got %d", got)
	}
	if got := rng.NextLong(); got != -4016670646968046118 {
		t.Fatalf("second value: got %d", got)
	}
	if v := NewRng(9).NextBelow(7); v < 0 || v >= 7 {
		t.Fatalf("NextBelow out of range: %d", v)
	}
	if NewRng(123).NextLong() != NewRng(123).NextLong() {
		t.Fatal("rng is not replayable")
	}
}

func TestRngRejectsNonPositiveBound(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic for bound 0")
		}
	}()
	NewRng(1).NextBelow(0)
}

func TestExponentialBackoffAndCap(t *testing.T) {
	if plain.Exponential(0) != 100 || plain.Exponential(1) != 200 || plain.Exponential(4) != 1600 {
		t.Fatal("exponential should double from base")
	}
	if mustPolicy(100, 1_000, 8, None).Exponential(7) != 1000 {
		t.Fatal("exponential must not exceed the cap")
	}
	if mustPolicy(100, 10_000, 100, None).Exponential(99) != 10_000 {
		t.Fatal("exponential must not overflow at large attempt counts")
	}
	if !slices.Equal(plain.Schedule(NewRng(42)), []int64{100, 200, 400, 800, 1600}) {
		t.Fatalf("NONE schedule: %v", plain.Schedule(NewRng(42)))
	}
	if len(plain.Schedule(NewRng(1))) != 5 {
		t.Fatal("schedule must have MaxAttempts-1 entries")
	}
}

func TestPolicyValidation(t *testing.T) {
	if _, err := NewRetryPolicy(100, 50, 3, None); !errors.Is(err, ErrInvalidPolicy) {
		t.Fatal("cap below base must be rejected")
	}
	if _, err := NewRetryPolicy(100, 100, 0, None); !errors.Is(err, ErrInvalidPolicy) {
		t.Fatal("zero attempts must be rejected")
	}
}

func TestJitterSchedulesSeed42(t *testing.T) {
	cases := []struct {
		j    Jitter
		want []int64
	}{
		{Full, []int64{46, 26, 131, 226, 241}},
		{Equal, []int64{68, 179, 379, 654, 994}},
		{Decorrelated, []int64{190, 474, 675, 569, 810}},
	}
	for _, c := range cases {
		got := mustPolicy(100, 10_000, 6, c.j).Schedule(NewRng(42))
		if !slices.Equal(got, c.want) {
			t.Errorf("%v schedule: got %v want %v", c.j, got, c.want)
		}
	}
}

func TestJitterBoundsHoldForManySeeds(t *testing.T) {
	full := mustPolicy(100, 10_000, 6, Full)
	equal := mustPolicy(100, 10_000, 6, Equal)
	decorr := mustPolicy(100, 10_000, 6, Decorrelated)
	for seed := int64(1); seed <= 200; seed++ {
		rng := NewRng(seed)
		for attempt := 0; attempt < 6; attempt++ {
			exp := plain.Exponential(attempt)
			if f := full.Delay(attempt, 100, rng); f < 0 || f > exp {
				t.Fatalf("FULL out of [0, %d]: %d", exp, f)
			}
			if e := equal.Delay(attempt, 100, rng); e < exp/2 || e > exp {
				t.Fatalf("EQUAL out of [%d, %d]: %d", exp/2, exp, e)
			}
			if d := decorr.Delay(attempt, 300, rng); d < 100 || d > 900 {
				t.Fatalf("DECORRELATED out of [100, 900]: %d", d)
			}
		}
	}
	if d := mustPolicy(100, 500, 6, Decorrelated).Delay(3, 400, NewRng(5)); d > 500 {
		t.Fatalf("DECORRELATED must respect the cap: %d", d)
	}
}

func TestRetrierSucceedsOnThirdAttempt(t *testing.T) {
	clock := NewFakeClock(0)
	flap := []Outcome{Timeout, Timeout, OK}
	r := (&Retrier{plain, nil, clock, NewRng(7)}).Execute(func(a int) Outcome { return flap[min(a, len(flap)-1)] })
	if !r.Succeeded() || r.Attempts != 3 {
		t.Fatalf("expected success on attempt 3: %+v", r)
	}
	if r.TotalDelayMs != 300 || clock.NowMs() != 300 {
		t.Fatalf("expected 300 ms on the fake clock: %d / %d", r.TotalDelayMs, clock.NowMs())
	}
	if !slices.Equal(r.Delays, []int64{100, 200}) {
		t.Fatalf("delays: %v", r.Delays)
	}
}

func TestNonRetryableOutcomesAreNotRetried(t *testing.T) {
	r := (&Retrier{plain, nil, NewFakeClock(0), NewRng(7)}).Execute(func(int) Outcome { return CardDeclined })
	if r.StopReason != NonRetryable || r.Attempts != 1 || r.TotalDelayMs != 0 {
		t.Fatalf("declined card must not be retried: %+v", r)
	}
	r = (&Retrier{plain, nil, NewFakeClock(0), NewRng(7)}).Execute(func(int) Outcome { return BadRequest })
	if r.StopReason != NonRetryable {
		t.Fatalf("400 must not be retried: %+v", r)
	}
	if !Timeout.Retryable() || !RateLimited.Retryable() || Unauthorized.Retryable() || OK.Retryable() {
		t.Fatal("retryable classification is wrong")
	}
}

func TestExhaustsAttemptsWithoutFinalSleep(t *testing.T) {
	r := (&Retrier{mustPolicy(100, 10_000, 4, None), nil, NewFakeClock(0), NewRng(7)}).Execute(func(int) Outcome { return Timeout })
	if r.StopReason != AttemptsExhausted || r.Attempts != 4 {
		t.Fatalf("expected exhaustion after 4: %+v", r)
	}
	if r.TotalDelayMs != 100+200+400 {
		t.Fatalf("no sleep after the final attempt: %d", r.TotalDelayMs)
	}
}

func TestRetryBudgetDepositsAndCap(t *testing.T) {
	budget, err := NewRetryBudget(20, 10)
	if err != nil {
		t.Fatal(err)
	}
	if budget.Tokens() != 0 || budget.TryWithdraw() {
		t.Fatal("budget must start empty")
	}
	for i := 0; i < 5; i++ {
		budget.Deposit()
	}
	if budget.Tokens() != 1 || !budget.TryWithdraw() || budget.TryWithdraw() {
		t.Fatal("five 20% deposits make exactly one token")
	}
	for i := 0; i < 1000; i++ {
		budget.Deposit()
	}
	if budget.Tokens() != 10 {
		t.Fatalf("budget must cap at 10 tokens: %d", budget.Tokens())
	}
	if _, err := NewRetryBudget(101, 1); err == nil {
		t.Fatal("percent above 100 must be rejected")
	}
}

func TestRetryBudgetStopsRetriesOnceDrained(t *testing.T) {
	budget, _ := NewRetryBudget(20, 10)
	for i := 0; i < 1000; i++ {
		budget.Deposit()
	}
	retrier := &Retrier{mustPolicy(100, 1_000, 3, None), budget, NewFakeClock(0), NewRng(1)}
	spent := 0
	var last StopReason
	for i := 0; i < 8; i++ {
		rr := retrier.Execute(func(int) Outcome { return ServerError })
		spent += rr.Attempts - 1
		last = rr.StopReason
	}
	if last != BudgetExhausted || budget.Tokens() != 0 {
		t.Fatalf("budget should be drained: last=%v tokens=%d", last, budget.Tokens())
	}
	if spent > 10+8 {
		t.Fatalf("retries exceeded banked tokens plus deposits: %d", spent)
	}
}

func TestStormLockstepWithoutJitter(t *testing.T) {
	s := RunStorm(200, 40, 100, 1_000, 60, mustPolicy(100, 5_000, 8, None), 2024)
	if s.PeakLoad() != 200 || s.PeakLoadFrom(1_000) != 200 {
		t.Fatalf("lockstep peak should be 200: %d / %d", s.PeakLoad(), s.PeakLoadFrom(1_000))
	}
	if s.Succeeded != 80 || s.BusyBuckets() != 6 {
		t.Fatalf("lockstep: succeeded=%d busy=%d", s.Succeeded, s.BusyBuckets())
	}
	if s.Succeeded+s.GaveUp+s.StillWaiting != 200 {
		t.Fatal("client accounting does not add up")
	}
}

func TestStormJitterLowersPeak(t *testing.T) {
	sNone := RunStorm(200, 40, 100, 1_000, 60, mustPolicy(100, 5_000, 8, None), 2024)
	sFull := RunStorm(200, 40, 100, 1_000, 60, mustPolicy(100, 5_000, 8, Full), 2024)
	sDec := RunStorm(200, 40, 100, 1_000, 60, mustPolicy(100, 5_000, 8, Decorrelated), 2024)
	if sFull.PeakLoadFrom(1_000) >= sNone.PeakLoadFrom(1_000) {
		t.Fatalf("full jitter should lower the peak: %d vs %d", sFull.PeakLoadFrom(1_000), sNone.PeakLoadFrom(1_000))
	}
	if sDec.PeakLoadFrom(1_000) >= sNone.PeakLoadFrom(1_000) || sDec.Succeeded != 200 {
		t.Fatalf("decorrelated jitter should also beat lockstep: %+v", sDec)
	}
	if sFull.BusyBuckets() <= sNone.BusyBuckets() {
		t.Fatal("jitter should spread load over more buckets")
	}
	if sFull.Succeeded != 200 || sFull.StillWaiting != 0 {
		t.Fatalf("everyone should succeed with jitter: %+v", sFull)
	}
	var sum int64
	for _, l := range sFull.LoadPerBucket {
		sum += l
	}
	if sum != sFull.TotalAttempts {
		t.Fatal("bucket loads must sum to total attempts")
	}
}

func TestStormIsDeterministicForSeed(t *testing.T) {
	a := RunStorm(200, 40, 100, 1_000, 60, mustPolicy(100, 5_000, 8, Full), 2024)
	b := RunStorm(200, 40, 100, 1_000, 60, mustPolicy(100, 5_000, 8, Full), 2024)
	if a.PeakLoadFrom(1_000) != b.PeakLoadFrom(1_000) || a.TotalAttempts != b.TotalAttempts {
		t.Fatal("same seed must give the same storm")
	}
}
