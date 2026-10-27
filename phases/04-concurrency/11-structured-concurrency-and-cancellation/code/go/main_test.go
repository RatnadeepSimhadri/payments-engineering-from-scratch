// Tests for phases/04-concurrency/11-structured-concurrency-and-cancellation. Run: go test -race ./...
package main

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func fork[T any](t *testing.T, scope *StructuredScope, name string, body Task[T]) *Subtask[T] {
	t.Helper()
	st, err := Fork(scope, name, body)
	if err != nil {
		t.Fatalf("Fork(%s): %v", name, err)
	}
	return st
}

func ms(n int) time.Duration { return time.Duration(n) * time.Millisecond }

func TestDeadlineCompositionAndExpiry(t *testing.T) {
	clock := NewManualClock()
	d200 := DeadlineAfter(clock, ms(200))
	d800 := DeadlineAfter(clock, ms(800))
	if d800.Earliest(d200) != d200 || d200.Earliest(d800) != d200 {
		t.Error("earliest picks the tighter deadline")
	}
	if DeadlineNone.Earliest(d200) != d200 {
		t.Error("NONE never wins over a real deadline")
	}
	if d200.Remaining(clock).Milliseconds() != 200 {
		t.Error("remaining before expiry")
	}
	if d200.IsExpired(clock) {
		t.Error("not expired yet")
	}
	clock.Advance(ms(200))
	if !d200.IsExpired(clock) || d200.Remaining(clock) != 0 {
		t.Error("expired exactly at the instant")
	}
	if DeadlineNone.IsExpired(clock) || !DeadlineNone.IsNone() {
		t.Error("NONE is never expired")
	}
	defer func() {
		if recover() == nil {
			t.Error("an overflowing deadline is a programming error and panics")
		}
	}()
	DeadlineAfter(clock, time.Duration(noDeadline))
}

func TestCancellationTokenTree(t *testing.T) {
	c2 := NewManualClock()
	root := Root(c2)
	child := root.Child(DeadlineAfter(c2, ms(100)))
	grandchild := child.Child(DeadlineAfter(c2, ms(900)))
	if grandchild.DeadlineOf() != child.DeadlineOf() {
		t.Error("child deadline is inherited by grandchild")
	}
	var callbacks atomic.Int32
	grandchild.OnCancel(func() { callbacks.Add(1) })
	if root.IsCancelled() || child.IsCancelled() || grandchild.IsCancelled() {
		t.Error("not cancelled initially")
	}
	if !root.Cancel("operator abort") || root.Cancel("second reason") {
		t.Error("first cancel wins")
	}
	if root.Reason() != "operator abort" {
		t.Error("reason is the first one")
	}
	if !child.IsCancelled() || !grandchild.IsCancelled() {
		t.Error("cancel propagates down two levels")
	}
	if callbacks.Load() != 1 {
		t.Error("callback ran exactly once")
	}
	grandchild.OnCancel(func() { callbacks.Add(1) })
	if callbacks.Load() != 2 {
		t.Error("late registration runs immediately")
	}
	err := grandchild.CheckCancelled()
	var ce *CancelledError
	if !errors.Is(err, ErrCancelled) || !errors.As(err, &ce) || ce.Reason != "operator abort" {
		t.Errorf("CheckCancelled returns a CancelledError: %v", err)
	}
	if !errors.Is(grandchild.Err(), context.Canceled) {
		t.Error("as a context.Context, an explicit cancel is context.Canceled")
	}

	parent2 := Root(c2)
	kid := parent2.ChildNone()
	kid.Cancel("child only")
	if !kid.IsCancelled() || parent2.IsCancelled() {
		t.Error("cancelling a child never cancels the parent")
	}
	lateKid := kid.ChildNone()
	if !lateKid.IsCancelled() || lateKid.Reason() != "child only" {
		t.Error("child of a cancelled token is born cancelled")
	}

	c3 := NewManualClock()
	timed := Root(c3).Child(DeadlineAfter(c3, ms(50)))
	c3.Advance(ms(49))
	if timed.IsCancelled() || timed.Err() != nil {
		t.Error("deadline not yet reached")
	}
	c3.Advance(ms(1))
	if !timed.IsCancelled() || timed.Reason() != "deadline exceeded" {
		t.Error("deadline expiry becomes a cancellation")
	}
	if !errors.Is(timed.Err(), context.DeadlineExceeded) {
		t.Error("as a context.Context, an expired deadline is context.DeadlineExceeded")
	}
}

func TestTokenIsAContext(t *testing.T) {
	clock := NewManualClock()
	token := Root(clock).Child(DeadlineAfter(clock, ms(10)))
	if at, ok := token.Deadline(); !ok || at.UnixNano() != int64(ms(10)) {
		t.Error("Deadline() exposes the instant on the injected clock")
	}
	ctx, cancel := context.WithCancel(token) // a standard child derived from the token
	defer cancel()
	select {
	case <-ctx.Done():
		t.Fatal("derived context is live while the token is live")
	default:
	}
	clock.Advance(ms(10))
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the derived context follows the token's cancellation")
	}
	if !errors.Is(ctx.Err(), context.Canceled) && !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Errorf("derived context reports an error: %v", ctx.Err())
	}
}

func TestScopeAllSucceed(t *testing.T) {
	sys := NewSystemClock()
	scope := NewStructuredScope("ok", Root(sys), DeadlineNone, sys)
	defer scope.Close()
	a := fork(t, scope, "a", func(*CancellationToken) (int, error) { return 1, nil })
	b := fork(t, scope, "b", func(*CancellationToken) (int, error) { return 2, nil })
	scope.Join()
	if err := scope.Err(); err != nil {
		t.Fatalf("Err after a clean join: %v", err)
	}
	if a.State() != Success || b.State() != Success {
		t.Error("both subtasks succeed")
	}
	av, _ := a.Get()
	bv, _ := b.Get()
	if av+bv != 3 {
		t.Error("values available after join")
	}
	if scope.Active() != 0 {
		t.Error("nothing active after join")
	}
	if _, err := Fork(scope, "late", func(*CancellationToken) (int, error) { return 3, nil }); !errors.Is(err, ErrScopeClosed) {
		t.Errorf("fork after join is rejected: %v", err)
	}
}

func TestScopeErrBeforeJoin(t *testing.T) {
	sys := NewSystemClock()
	scope := NewStructuredScope("early", Root(sys), DeadlineNone, sys)
	defer scope.Close()
	if !errors.Is(scope.Err(), ErrNotJoined) {
		t.Error("Err before Join is an error")
	}
}

func TestScopeOneFailureCancelsSiblings(t *testing.T) {
	sys := NewSystemClock()
	var cleanups atomic.Int32
	var siblingReady sync.WaitGroup
	siblingReady.Add(1)
	scope := NewStructuredScope("fail", Root(sys), DeadlineNone, sys)
	defer scope.Close()
	slow := fork(t, scope, "slow", func(tok *CancellationToken) (int, error) {
		tok.OnCancel(func() { cleanups.Add(1) })
		siblingReady.Done()
		if err := WaitForSignalOrCancel(tok); err != nil {
			return 0, err
		}
		return 1, nil
	})
	fraud := errors.New("fraud rule tripped")
	boom := fork(t, scope, "boom", func(*CancellationToken) (int, error) {
		siblingReady.Wait()
		return 0, fraud
	})
	scope.Join()
	if boom.State() != Failed || !errors.Is(boom.Exception(), fraud) {
		t.Error("failed subtask is FAILED")
	}
	if slow.State() != Cancelled {
		t.Error("sibling is CANCELLED, not FAILED")
	}
	if cleanups.Load() != 1 {
		t.Error("sibling cleanup ran once")
	}
	if !contains(scope.CancellationReason(), "boom") {
		t.Error("scope reason names the failed task")
	}
	if _, err := slow.Get(); !errors.Is(err, ErrNotSuccess) {
		t.Error("get on a cancelled subtask is an error")
	}
	var exec *ExecutionError
	if err := scope.Err(); !errors.As(err, &exec) || exec.Cause.Error() != "fraud rule tripped" || !errors.Is(err, fraud) {
		t.Errorf("Err returns the first failure with the cause unwrapped: %v", err)
	}
	if scope.Active() != 0 {
		t.Error("nothing active after a failed join")
	}
}

func TestScopePanicIsAFailure(t *testing.T) {
	sys := NewSystemClock()
	scope := NewStructuredScope("panic", Root(sys), DeadlineNone, sys)
	defer scope.Close()
	p := fork(t, scope, "p", func(*CancellationToken) (int, error) { panic("nil map write") })
	scope.Join()
	if p.State() != Failed || !contains(p.Exception().Error(), "nil map write") {
		t.Errorf("a panicking body is FAILED, not a crash of the process: %v %v", p.State(), p.Exception())
	}
}

func TestScopeDeadlineOnManualClock(t *testing.T) {
	mc := NewManualClock()
	var started sync.WaitGroup
	started.Add(1)
	scope := NewStructuredScope("deadline", Root(mc), DeadlineAfter(mc, ms(300)), mc)
	defer scope.Close()
	straggler := fork(t, scope, "straggler", func(tok *CancellationToken) (int, error) {
		started.Done()
		if err := WaitForSignalOrCancel(tok); err != nil {
			return 0, err
		}
		return 1, nil
	})
	started.Wait()
	mc.Advance(ms(301)) // the clock moves; nobody sleeps
	scope.Join()
	if straggler.State() != Cancelled {
		t.Error("straggler cancelled by the deadline")
	}
	if scope.CancellationReason() != "deadline exceeded" {
		t.Error("scope reports deadline exceeded")
	}
	if !errors.Is(scope.Err(), context.DeadlineExceeded) {
		t.Errorf("Err turns a deadline into context.DeadlineExceeded: %v", scope.Err())
	}
}

func TestNestedScopesInheritTighterBudget(t *testing.T) {
	nc := NewManualClock()
	outer := NewStructuredScope("request", Root(nc), DeadlineAfter(nc, ms(100)), nc)
	defer outer.Close()
	inner := NewStructuredScope("bank-call", outer.Token(), DeadlineAfter(nc, ms(5000)), nc)
	defer inner.Close()
	if inner.Deadline() != outer.Deadline() {
		t.Error("inner scope cannot outlive its parent's deadline")
	}
	inner.Join()
	outer.Join()
}

func TestExternalCancelStopsRunningScope(t *testing.T) {
	sys := NewSystemClock()
	external := Root(sys)
	var running sync.WaitGroup
	running.Add(1)
	scope := NewStructuredScope("external", external, DeadlineNone, sys)
	defer scope.Close()
	worker := fork(t, scope, "worker", func(tok *CancellationToken) (int, error) {
		running.Done()
		if err := WaitForSignalOrCancel(tok); err != nil {
			return 0, err
		}
		return 1, nil
	})
	running.Wait()
	external.Cancel("client disconnected")
	scope.Join()
	if worker.State() != Cancelled {
		t.Error("external cancel reaches the subtask")
	}
	if scope.CancellationReason() != "client disconnected" {
		t.Error("reason is propagated unchanged")
	}
}

func TestCloseWithoutJoin(t *testing.T) {
	sys := NewSystemClock()
	var completedNormally atomic.Int32
	var allStarted sync.WaitGroup
	allStarted.Add(3)
	leaked := func() *StructuredScope {
		scope := NewStructuredScope("leaky", Root(sys), DeadlineNone, sys)
		defer scope.Close()
		for i := 0; i < 3; i++ {
			fork(t, scope, "w", func(tok *CancellationToken) (int, error) {
				allStarted.Done()
				if err := WaitForSignalOrCancel(tok); err != nil {
					return 0, err
				}
				completedNormally.Add(1)
				return 1, nil
			})
		}
		allStarted.Wait()
		return scope
	}()
	if !leaked.IsCancelled() || !contains(leaked.CancellationReason(), "closed without join") {
		t.Error("close cancels an un-joined scope")
	}
	if leaked.Active() != 0 {
		t.Error("no subtask is still running after close")
	}
	for _, s := range leaked.Subtasks() {
		if s.State() != Cancelled {
			t.Errorf("every subtask ended CANCELLED: %s is %s", s.Name(), s.State())
		}
	}
	if completedNormally.Load() != 0 {
		t.Error("none completed normally")
	}
	if _, err := Fork(leaked, "late", func(*CancellationToken) (int, error) { return 1, nil }); !errors.Is(err, ErrScopeClosed) {
		t.Error("fork after close is rejected")
	}
}

func TestForkUnderCancelledTokenNeverRuns(t *testing.T) {
	sys := NewSystemClock()
	dead := Root(sys)
	dead.Cancel("already dead")
	var bodyRuns atomic.Int32
	scope := NewStructuredScope("dead", dead, DeadlineNone, sys)
	defer scope.Close()
	never := fork(t, scope, "never", func(*CancellationToken) (int, error) { bodyRuns.Add(1); return 1, nil })
	scope.Join()
	if bodyRuns.Load() != 0 || never.State() != Cancelled {
		t.Error("body of a task under a cancelled token never runs")
	}
}

func TestAuthorizeOutcomes(t *testing.T) {
	balance, amount := OfMinor(10_000, USD), OfMinor(2_500, USD)
	cases := []struct {
		name       string
		fraudFails bool
		slow       bool
		want       string
		cleanups   int
	}{
		{"fraud fails", true, false, "DECLINED (velocity rule tripped)", 2},
		{"slow bank", false, true, "TIMEOUT (authorize: deadline exceeded)", 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			log := &Log{}
			var clock Clock = NewSystemClock()
			var slow *ManualClock
			if c.slow {
				slow = NewManualClock()
				clock = slow
			}
			budget := 5 * time.Second
			if c.slow {
				budget = ms(300)
			}
			got, err := Authorize(Root(clock), clock, budget, balance, amount, c.fraudFails, slow, log)
			if err != nil {
				t.Fatal(err)
			}
			if got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
			if len(log.Sorted()) != c.cleanups {
				t.Errorf("cleanups ran: %v", log.Sorted())
			}
		})
	}
}

func TestMoneyGuard(t *testing.T) {
	if ok, err := OfMinor(10_000, USD).Covers(OfMinor(2_500, USD)); err != nil || !ok {
		t.Error("covers compares minor units")
	}
	if OfMinor(2_505, USD).String() != "25.05 USD" {
		t.Error("String pads minor units")
	}
	if _, err := OfMinor(1, USD).Covers(OfMinor(1, Currency{"EUR", 2})); !errors.Is(err, ErrCurrencyMismatch) {
		t.Error("covers rejects currency mismatch")
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }
