// Tests for phases/05-distributed-foundations/08-timeouts-and-deadlines. Run: go test ./...
package main

import (
	"errors"
	"math"
	"reflect"
	"testing"
)

func expectPanic(t *testing.T, name string, action func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Errorf("%s: expected a panic", name)
		}
	}()
	action()
}

func fromWire(t *testing.T, id, header string, clock Clock) Context {
	t.Helper()
	ctx, err := FromWire(id, header, clock)
	if err != nil {
		t.Fatalf("FromWire(%q): %v", header, err)
	}
	return ctx
}

func TestDeadlineArithmetic(t *testing.T) {
	clock := NewFakeClock(1_000)
	d := DeadlineAfter(clock, 500)
	if d.AtMs != 1_500 {
		t.Error("deadline is absolute")
	}
	if d.RemainingMs(clock) != 500 {
		t.Error("remaining at creation")
	}
	clock.Advance(200)
	if d.RemainingMs(clock) != 300 {
		t.Error("remaining shrinks as time passes")
	}
	clock.Advance(400)
	if d.RemainingMs(clock) != 0 {
		t.Error("remaining clamps at zero, never negative")
	}
	if !d.IsExpired(clock) {
		t.Error("expired once now >= at")
	}
	if DeadlineNone.IsExpired(clock) || DeadlineNone.RemainingMs(clock) != math.MaxInt64 {
		t.Error("NONE never expires")
	}
	if (Deadline{10}).Earliest(Deadline{5}).AtMs != 5 {
		t.Error("earliest picks the tighter deadline")
	}
	if SaturatingAdd(math.MaxInt64-1, 10) != math.MaxInt64 {
		t.Error("saturating add does not wrap")
	}
	if SaturatingAdd(5, 7) != 12 || SaturatingAdd(-5, 3) != -2 {
		t.Error("saturating add is plain addition when nothing overflows")
	}
	expectPanic(t, "negative timeout rejected", func() { DeadlineAfter(clock, -1) })
	expectPanic(t, "clock cannot move backwards", func() { clock.Advance(-1) })
}

func TestContextChildrenTightenNeverExtend(t *testing.T) {
	c := NewFakeClock(0)
	parent := Root("r").WithTimeout(c, 500)
	child := parent.WithTimeout(c, 2_000)
	if child.Deadline.AtMs != 500 {
		t.Error("child cannot extend parent's deadline")
	}
	if tighter := parent.WithTimeout(c, 100); tighter.Deadline.AtMs != 100 {
		t.Error("child can tighten parent's deadline")
	}
	if child.RequestID != "r" {
		t.Error("requestId travels with the context")
	}
	var dle *DeadlineExceededError
	err := parent.RequireBudget(c, 600, "test")
	if !errors.Is(err, ErrDeadlineExceeded) || !errors.As(err, &dle) || dle.RemainingMs != 500 {
		t.Errorf("requireBudget refuses work that cannot fit and reports what remains: %v", err)
	}
	if err := parent.Checkpoint(c, "test"); err != nil {
		t.Error("checkpoint passes while the deadline is live")
	}
	c.Advance(500)
	if err := parent.Checkpoint(c, "test"); !errors.Is(err, ErrDeadlineExceeded) {
		t.Error("checkpoint fails once expired")
	}
	if parent.WithoutDeadline().Deadline != DeadlineNone {
		t.Error("withoutDeadline drops the budget")
	}
}

func TestWireEncodingIsRelative(t *testing.T) {
	sender := NewFakeClock(1_000_000)
	receiver := NewFakeClock(7) // wildly different clock: must not matter
	out := Root("w").WithTimeout(sender, 250)
	sender.Advance(50)
	header := out.ToWireTimeout(sender)
	if header != "200m" {
		t.Errorf("wire header carries remaining ms with unit: %q", header)
	}
	in := fromWire(t, "w", header, receiver)
	if in.RemainingMs(receiver) != 200 || in.Deadline.AtMs != 207 {
		t.Error("receiver reconstructs the remaining budget on its own clock")
	}
	if Root("x").ToWireTimeout(sender) != "" || !fromWire(t, "x", "", receiver).Deadline.IsNone() {
		t.Error("no deadline encodes as absent header")
	}
	units := []struct {
		header string
		wantMs int64
	}{{"2S", 2_000}, {"1500u", 1}, {"3M", 180_000}, {"1H", 3_600_000}, {"2500000n", 2}}
	for _, u := range units {
		t.Run(u.header, func(t *testing.T) {
			if got := fromWire(t, "x", u.header, receiver).RemainingMs(receiver); got != u.wantMs {
				t.Errorf("%s decodes to %d ms, want %d", u.header, got, u.wantMs)
			}
		})
	}
	for _, bad := range []string{"5x", "m", "123456789m", "-5m", " 5m", "1_0m"} {
		if _, err := FromWire("x", bad, receiver); !errors.Is(err, ErrBadHeader) {
			t.Errorf("header %q should be rejected: %v", bad, err)
		}
	}
}

func TestDownstreamCallThatOutlivesItsCaller(t *testing.T) {
	c1 := NewFakeClock(0)
	log1 := &EffectLog{}
	o1 := NewClient(c1).Invoke(BuildChain(c1, log1, false), "p1", 500)
	if o1.Status != TimedOut {
		t.Error("without propagation the caller times out")
	}
	if !log1.Contains("CHARGE") {
		t.Error("without propagation the charge still happened")
	}
	if log1.All()[0].AtMs <= 500 {
		t.Error("the charge landed after the caller gave up")
	}
	if o1.ElapsedMs != 620 || o1.Detail != "response discarded: arrived 120 ms late" {
		t.Errorf("timing matches the Java demo: %+v", o1)
	}

	c2 := NewFakeClock(0)
	log2 := &EffectLog{}
	o2 := NewClient(c2).Invoke(BuildChain(c2, log2, true), "p2", 500)
	if o2.Status != FailedFast {
		t.Error("with propagation the processor refuses before charging")
	}
	if len(log2.All()) != 0 {
		t.Error("with propagation no side effect is recorded")
	}
	if !(o2.ElapsedMs < 500 && o2.ElapsedMs == 220) {
		t.Error("fail-fast returns well inside the budget")
	}

	c3 := NewFakeClock(0)
	log3 := &EffectLog{}
	o3 := NewClient(c3).Invoke(BuildChain(c3, log3, true), "p3", 700)
	if o3.Status != OK || len(log3.All()) != 1 {
		t.Error("a budget that fits the chain succeeds")
	}
	if o3.Status.String() != "OK" || TimedOut.String() != "TIMED_OUT" {
		t.Error("statuses print by name")
	}
}

func TestRetriesInsideADeadline(t *testing.T) {
	c4 := NewFakeClock(0)
	r4 := NewDeadlineAwareRetrier(c4, 300, 50, 50).Call(NewFlakyService(c4, 150, 2), Root("rt").WithTimeout(c4, 1_000))
	if r4.Status != OK || r4.Attempts != 3 {
		t.Error("retrier succeeds on the third attempt within a generous deadline")
	}
	if r4.ElapsedMs != 150+50+150+50+150 {
		t.Error("retrier elapsed is attempts plus backoffs")
	}

	c5 := NewFakeClock(0)
	r5 := NewDeadlineAwareRetrier(c5, 300, 50, 50).Call(NewFlakyService(c5, 150, 2), Root("rt").WithTimeout(c5, 400))
	if r5.Status != FailedFast || r5.Attempts != 2 {
		t.Error("retrier stops at the deadline instead of starting a doomed attempt")
	}
	if !reflect.DeepEqual(r5.AttemptTimeoutsMs, []int64{300, 200}) {
		t.Error("second attempt budget was clipped to the remaining time")
	}
	if c5.NowMs() > 400 {
		t.Error("retrier never overran the deadline")
	}

	c6 := NewFakeClock(0)
	r6 := NewDeadlineAwareRetrier(c6, 300, 50, 50).Call(NewFlakyService(c6, 150, 0), Root("rt").WithTimeout(c6, 100))
	if r6.Status != FailedFast || c6.NowMs() != 0 {
		t.Error("an attempt that cannot fit is refused before any work")
	}

	c7 := NewFakeClock(0)
	flaky := NewFlakyService(c7, 10, 1)
	if _, err := flaky.Call(Root("u").WithTimeout(c7, 100)); !errors.Is(err, ErrUnavailable) {
		t.Error("a flaky failure is a retryable UnavailableError")
	}
	if o := NewClient(c7).Invoke(NewFlakyService(c7, 10, 1), "u", 100); o.Status != Unavailable {
		t.Error("the client reports a retryable error as UNAVAILABLE")
	}
}

func TestHedging(t *testing.T) {
	cases := []struct {
		name                        string
		primary, backup, hedgeAfter int64
		wantLatency                 int64
		wantRequests                int
		wantWinner                  string
	}{
		{"hedge wins when the primary stalls", 800, 40, 60, 100, 2, "backup"},
		{"no hedge sent when the primary is fast", 30, 40, 60, 30, 1, "primary"},
		{"primary still wins when the backup is slower", 70, 500, 60, 70, 2, "primary"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Hedge(c.primary, c.backup, c.hedgeAfter)
			if got != (HedgeResult{c.wantLatency, c.wantRequests, c.wantWinner}) {
				t.Errorf("got %+v", got)
			}
		})
	}
	s := RunStudy(42, 10_000, 60)
	if s.P99Hedged*4 >= s.P99 {
		t.Error("hedging cuts p99 by a large factor")
	}
	if s.ExtraLoadPercent >= 10.0 {
		t.Error("hedging costs only a few percent extra load")
	}
	if RunStudy(42, 10_000, 60) != s {
		t.Error("study is deterministic under the seed")
	}
	// java.util.Random parity: the Java and Python demos print p50=41 p99=919 hedged p99=113.
	if s.P50 != 41 || s.P99 != 919 || s.P99Hedged != 113 {
		t.Errorf("seed 42 percentiles match the Java demo: %+v", s)
	}
}
