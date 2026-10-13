// Tests for phases/03-systems-data-structures/11-rate-limiter-algorithms. Run: go test ./...
package main

import (
	"errors"
	"testing"
)

func TestDecisionRetryAfterHeader(t *testing.T) {
	cases := map[int64]int64{1: 1, 1000: 1, 1001: 2}
	for ms, want := range cases {
		if got := Deny(ms).RetryAfterHeader(); got != want {
			t.Errorf("Deny(%d).RetryAfterHeader() = %d, want %d", ms, got, want)
		}
	}
	if Allow().RetryAfterHeader() != 0 || Deny(0).RetryAfterMillis != 1 || Deny(5).Allowed {
		t.Errorf("Decision basics wrong")
	}
	if d := AllowAfter(250); !d.Allowed || d.DelayMillis != 250 {
		t.Errorf("AllowAfter wrong: %+v", d)
	}
}

func TestTokenBucketBurstAndRefill(t *testing.T) {
	tb, _ := NewTokenBucket(5, 2, NewManualClock(0))
	for i := 0; i < 7; i++ {
		if tb.TryAcquire().Allowed != (i < 5) {
			t.Fatalf("request %d: wrong admission", i)
		}
	}
	clock := NewManualClock(0)
	tb2, _ := NewTokenBucket(5, 2, clock)
	for i := 0; i < 5; i++ {
		tb2.TryAcquire()
	}
	clock.Advance(499)
	if tb2.TryAcquire().Allowed {
		t.Errorf("0.998 tokens should deny")
	}
	clock.Advance(1)
	if !tb2.TryAcquire().Allowed {
		t.Errorf("exactly one token at 500 ms should allow")
	}
	clock.Advance(60_000)
	if tb2.Tokens() != 5.0 {
		t.Errorf("tokens exceeded capacity: %f", tb2.Tokens())
	}
}

func TestTokenBucketRetryAfter(t *testing.T) {
	clock := NewManualClock(0)
	tb, _ := NewTokenBucket(1, 4, clock)
	tb.TryAcquire()
	if d := tb.TryAcquire(); d.Allowed || d.RetryAfterMillis != 250 {
		t.Errorf("Retry-After = %+v", d)
	}
	clock.Advance(250)
	if !tb.TryAcquire().Allowed {
		t.Errorf("should admit after Retry-After")
	}
	if _, err := NewTokenBucket(0, 1, clock); !errors.Is(err, ErrBadParameter) {
		t.Errorf("zero capacity accepted")
	}
	if _, err := NewTokenBucket(1, 0, clock); !errors.Is(err, ErrBadParameter) {
		t.Errorf("zero rate accepted")
	}
}

func TestLeakyBucketShapesOutput(t *testing.T) {
	lb, _ := NewLeakyBucket(3, 10, NewManualClock(0))
	for i, want := range []int64{0, 100, 200} {
		if d := lb.TryAcquire(); !d.Allowed || d.DelayMillis != want {
			t.Fatalf("request %d: %+v", i, d)
		}
	}
	if d := lb.TryAcquire(); d.Allowed || d.RetryAfterMillis != 100 {
		t.Errorf("full queue: %+v", d)
	}
	if lb.Queued() != 3 {
		t.Errorf("queued = %d", lb.Queued())
	}
}

func TestLeakyBucketDrains(t *testing.T) {
	clock := NewManualClock(0)
	lb, _ := NewLeakyBucket(3, 10, clock)
	for i := 0; i < 3; i++ {
		lb.TryAcquire()
	}
	clock.Advance(100)
	if d := lb.TryAcquire(); !d.Allowed || d.DelayMillis != 200 {
		t.Errorf("after 100 ms: %+v", d)
	}
	clock.Advance(10_000)
	if lb.Queued() != 0 || lb.TryAcquire().DelayMillis != 0 {
		t.Errorf("should be empty after a pause")
	}
	if _, err := NewLeakyBucket(3, 7, clock); !errors.Is(err, ErrBadParameter) {
		t.Errorf("rate 7 accepted")
	}
	if _, err := NewLeakyBucket(0, 10, clock); !errors.Is(err, ErrBadParameter) {
		t.Errorf("zero queue accepted")
	}
}

func TestFixedWindowResetsAndDoubles(t *testing.T) {
	clock := NewManualClock(0)
	fw, _ := NewFixedWindow(2, 1000, clock)
	if !fw.TryAcquire().Allowed || !fw.TryAcquire().Allowed {
		t.Errorf("should admit up to limit")
	}
	if d := fw.TryAcquire(); d.Allowed || d.RetryAfterMillis != 1000 {
		t.Errorf("deny: %+v", d)
	}
	clock.Set(999)
	if fw.TryAcquire().RetryAfterMillis != 1 {
		t.Errorf("Retry-After should shrink to 1")
	}
	clock.Set(1000)
	if !fw.TryAcquire().Allowed {
		t.Errorf("should reset at the boundary")
	}
	// The failure the lesson warns about.
	clock2 := NewManualClock(0)
	fw2, _ := NewFixedWindow(10, 1000, clock2)
	log, _ := NewSlidingWindowLog(10, 1000, clock2)
	admittedFw, admittedLog := 0, 0
	for ts := int64(950); ts < 1050; ts += 5 {
		clock2.Set(ts)
		if fw2.TryAcquire().Allowed {
			admittedFw++
		}
		if log.TryAcquire().Allowed {
			admittedLog++
		}
	}
	if admittedFw != 20 || admittedLog != 10 {
		t.Errorf("fixed window %d, sliding log %d", admittedFw, admittedLog)
	}
}

func TestSlidingWindowLog(t *testing.T) {
	clock := NewManualClock(0)
	log, _ := NewSlidingWindowLog(2, 1000, clock)
	clock.Set(100)
	log.TryAcquire()
	clock.Set(400)
	log.TryAcquire()
	clock.Set(500)
	if d := log.TryAcquire(); d.Allowed || d.RetryAfterMillis != 600 {
		t.Errorf("deny: %+v", d)
	}
	clock.Set(1100)
	if !log.TryAcquire().Allowed || log.Len() != 2 {
		t.Errorf("should admit once the oldest entry has aged out")
	}
	clock2 := NewManualClock(0)
	one, _ := NewSlidingWindowLog(1, 1000, clock2)
	one.TryAcquire()
	clock2.Set(999)
	if one.TryAcquire().Allowed {
		t.Errorf("entry at 999 ms age should still count")
	}
	clock2.Set(1000)
	if !one.TryAcquire().Allowed {
		t.Errorf("entry at exactly window age should have left")
	}
}

func TestSlidingWindowCounter(t *testing.T) {
	clock := NewManualClock(0)
	sc, _ := NewSlidingWindowCounter(10, 1000, clock)
	for ts := int64(0); ts < 1000; ts += 100 {
		clock.Set(ts)
		sc.TryAcquire()
	}
	clock.Set(1250)
	if sc.WeightedCountScaled() != 7500 || !sc.TryAcquire().Allowed {
		t.Errorf("weighted at 1250 = %d", sc.WeightedCountScaled())
	}
	clock.Set(1500)
	if sc.WeightedCountScaled() != 6000 || !sc.TryAcquire().Allowed {
		t.Errorf("weighted at 1500 = %d", sc.WeightedCountScaled())
	}
	clock2 := NewManualClock(0)
	sc2, _ := NewSlidingWindowCounter(10, 1000, clock2)
	for ts := int64(0); ts < 1000; ts += 100 {
		clock2.Set(ts)
		sc2.TryAcquire()
	}
	clock2.Set(1000)
	if d := sc2.TryAcquire(); d.Allowed || d.RetryAfterMillis != 100 {
		t.Errorf("boundary after a full window: %+v", d)
	}
	clock3 := NewManualClock(0)
	sc3, _ := NewSlidingWindowCounter(2, 1000, clock3)
	sc3.TryAcquire()
	sc3.TryAcquire()
	clock3.Set(5000)
	if sc3.WeightedCountScaled() != 0 || !sc3.TryAcquire().Allowed {
		t.Errorf("a gap should clear the previous window")
	}
}

func TestTraceShapeAndMaxInWindow(t *testing.T) {
	trace := RetryStormTrace()
	inBurst := 0
	for _, ts := range trace {
		if ts >= 3000 && ts < 3020 {
			inBurst++
		}
	}
	if len(trace) != 99 || inBurst != 40 {
		t.Errorf("trace len %d burst %d", len(trace), inBurst)
	}
	if MaxInWindow([]int64{0, 100, 999, 1000}, 1000) != 3 || MaxInWindow(nil, 1000) != 0 {
		t.Errorf("MaxInWindow wrong")
	}
}

func TestRetryStormSummaryMatchesJavaAndPython(t *testing.T) {
	expected := map[string]ReplaySummary{
		"token bucket":    {"token bucket", 69, 30, 10, 19, 95, 0},
		"leaky bucket":    {"leaky bucket", 69, 30, 11, 10, 195, 995},
		"fixed window":    {"fixed window", 60, 39, 10, 19, 995, 0},
		"sliding log":     {"sliding log", 60, 39, 1, 10, 100, 0},
		"sliding counter": {"sliding counter", 55, 44, 1, 10, 112, 0},
	}
	trace := RetryStormTrace()
	for _, l := range MakeLimiters() {
		s := Replay(l.Limiter, l.Clock, trace, 3000, 3020)
		if s != expected[s.Name] {
			t.Errorf("%s: got %+v want %+v", s.Name, s, expected[s.Name])
		}
		windowed := s.Name != "token bucket" && s.Name != "fixed window"
		if windowed && s.MaxInAnySecond != 10 {
			t.Errorf("%s should hold the one-second ceiling, got %d", s.Name, s.MaxInAnySecond)
		}
		if !windowed && s.MaxInAnySecond <= 10 {
			t.Errorf("%s should exceed the one-second ceiling, got %d", s.Name, s.MaxInAnySecond)
		}
	}
}
