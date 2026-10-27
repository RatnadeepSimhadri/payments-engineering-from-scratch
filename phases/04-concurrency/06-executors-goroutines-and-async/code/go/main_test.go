// Tests for phases/04-concurrency/06-executors-goroutines-and-async. Run: go test -race ./...
package main

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func check(t *testing.T, name string, ok bool) {
	t.Helper()
	if !ok {
		t.Error(name)
	}
}

func mustPanic(t *testing.T, name string, f func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Errorf("%s: expected panic", name)
		}
	}()
	f()
}

var request = PaymentRequest{"pay_001", OfMinor(12_999, USD)}

func usd(minor int64) Money { return OfMinor(minor, USD) }

// throwingAdapter is an adapter whose Quote fails synchronously, as a client library with a broken configuration would.
type throwingAdapter struct{}

func (throwingAdapter) Name() string { return "broken" }
func (throwingAdapter) Quote(PaymentRequest) (*Future[Quote], error) {
	return nil, errors.New("no API key configured")
}

func TestMoney(t *testing.T) {
	check(t, "String pads minor units", usd(305).String() == "3.05 USD")
	check(t, "Compare orders fees", usd(275).Compare(usd(329)) < 0)
	mustPanic(t, "compare across currencies is a programming error", func() { usd(1).Compare(OfMinor(1, EUR)) })
	_, err := usd(1).Plus(OfMinor(1, EUR))
	check(t, "plus across currencies is an error", errors.Is(err, ErrCurrencyMismatch))
}

func TestManualTimer(t *testing.T) {
	timer := &ManualTimer{}
	a := timer.After(100)
	b := timer.After(50)
	check(t, "alarms are not done before their deadline", !a.IsDone() && !b.IsDone() && timer.Outstanding() == 2)
	timer.Advance(49)
	check(t, "advance short of the deadline fires nothing", !b.IsDone() && timer.NowMillis() == 49)
	timer.Advance(1)
	check(t, "advance to the deadline fires exactly that alarm", b.IsDone() && !a.IsDone() && timer.Outstanding() == 1)
	timer.Advance(1000)
	check(t, "a large advance fires everything due", a.IsDone() && timer.Outstanding() == 0)
	var order []string
	timer.After(30).OnComplete(func(struct{}, error) { order = append(order, "30") })
	timer.After(10).OnComplete(func(struct{}, error) { order = append(order, "10") })
	timer.After(10).OnComplete(func(struct{}, error) { order = append(order, "10b") })
	timer.Advance(100)
	check(t, "alarms fire in deadline order, ties in creation order", reflect.DeepEqual(order, []string{"10", "10b", "30"}))
	cancelled := timer.After(5)
	cancelled.Cancel()
	check(t, "a cancelled alarm is not outstanding", timer.Outstanding() == 0)
	timer.Advance(10)
	check(t, "advancing past a cancelled alarm is harmless", cancelled.IsCancelled())
	mustPanic(t, "negative delay rejected", func() { timer.After(-1) })
}

func TestFuture(t *testing.T) {
	f := NewFuture[int]()
	check(t, "first completion wins", f.Complete(1) && !f.Complete(2) && !f.Cancel())
	v, err := f.Get()
	check(t, "get returns the first value", err == nil && v == 1)
	c := NewFuture[int]()
	check(t, "cancel settles with ErrCancelled", c.Cancel() && c.IsCancelled())
	_, err = c.Get()
	check(t, "cancelled get reports ErrCancelled", errors.Is(err, ErrCancelled))
	ran := false
	f.OnComplete(func(int, error) { ran = true })
	check(t, "callback on a settled future runs at once", ran)
	check(t, "AllOf of nothing is an empty list", len(AllOf[int](nil).MustGet()) == 0)
}

func TestWithTimeout(t *testing.T) {
	timer := &ManualTimer{}
	immediate := WithTimeout(Immediate("alpha", usd(329)), request, 800, timer).MustGet()
	check(t, "immediate adapter yields OK with its fee", immediate.Outcome == OK && immediate.Fee == usd(329) && immediate.Processor == "alpha")
	check(t, "an answered call cancels its alarm", timer.Outstanding() == 0)

	beta := Scripted("beta")
	betaQuote := WithTimeout(beta, request, 800, timer)
	timer.Advance(799)
	check(t, "not timed out one ms before the deadline", !betaQuote.IsDone())
	beta.Respond(usd(275))
	check(t, "answer just before the deadline is OK", betaQuote.MustGet().Outcome == OK && betaQuote.MustGet().Fee == usd(275))
	check(t, "alarm cancelled after a late answer", timer.Outstanding() == 0)

	gamma := Scripted("gamma")
	gammaQuote := WithTimeout(gamma, request, 800, timer)
	timer.Advance(800)
	check(t, "silent adapter is TIMED_OUT at the deadline", gammaQuote.IsDone() && gammaQuote.MustGet().Outcome == TimedOut)
	check(t, "timeout detail names the budget", gammaQuote.MustGet().Detail == "no answer within 800 ms")
	check(t, "timeout does not cancel the underlying request", gamma.Outstanding() == 1)
	gamma.Respond(usd(1))
	check(t, "a late answer after timeout is ignored", gammaQuote.MustGet().Outcome == TimedOut)

	delta := Scripted("delta")
	deltaQuote := WithTimeout(delta, request, 800, timer)
	delta.Fail("HTTP 503 from processor")
	check(t, "failed completion becomes FAILED with the cause", deltaQuote.MustGet().Outcome == Failed && deltaQuote.MustGet().Detail == "HTTP 503 from processor")

	broken := WithTimeout(throwingAdapter{}, request, 800, timer).MustGet()
	check(t, "a synchronous error is FAILED, not a crashed fan-out", broken.Outcome == Failed && strings.HasPrefix(broken.Detail, "no API key"))
}

func TestQuoteAllAndCheapest(t *testing.T) {
	timer := &ManualTimer{}
	alpha := Immediate("alpha", usd(329))
	beta := Scripted("beta")
	gamma := Scripted("gamma")
	delta := Scripted("delta")
	all := QuoteAll(adapters(alpha, beta, gamma, delta), request, 800, timer)
	check(t, "every adapter called exactly once", alpha.Calls() == 1 && beta.Calls() == 1 && gamma.Calls() == 1 && delta.Calls() == 1)
	check(t, "combined future waits for the slowest", !all.IsDone())
	timer.Advance(250)
	beta.Respond(usd(275))
	timer.Advance(300)
	delta.Fail("HTTP 503 from processor")
	check(t, "still waiting on the silent adapter", !all.IsDone())
	timer.Advance(250)
	check(t, "combined future completes at the deadline", all.IsDone())
	quotes := all.MustGet()
	check(t, "results keep adapter order", quotes[0].Processor == "alpha" && quotes[1].Processor == "beta" && quotes[2].Processor == "gamma" && quotes[3].Processor == "delta")
	check(t, "outcomes per adapter", quotes[0].Outcome == OK && quotes[1].Outcome == OK && quotes[2].Outcome == TimedOut && quotes[3].Outcome == Failed)
	cheapest, ok := Cheapest(quotes)
	check(t, "cheapest ignores non-OK quotes and picks the lowest fee", ok && cheapest.Processor == "beta")
	_, ok = Cheapest([]Quote{quotes[2], quotes[3]})
	check(t, "cheapest of nothing is absent", !ok)
	check(t, "fan-out over zero adapters completes with an empty list", len(QuoteAll(nil, request, 800, timer).MustGet()) == 0)
}

func TestFirstOK(t *testing.T) {
	timer := &ManualTimer{}
	slow := Scripted("slow")
	fast := Scripted("fast")
	first := FirstOK(adapters(slow, fast), request, 800, timer)
	check(t, "no winner yet", !first.IsDone())
	fast.Respond(usd(350))
	check(t, "first OK answer wins immediately", first.IsDone() && first.MustGet().Processor == "fast")
	slow.Respond(usd(1))
	check(t, "a later, cheaper answer does not change the winner", first.MustGet().Processor == "fast")

	timer2 := &ManualTimer{}
	a := Scripted("a")
	b := Scripted("b")
	none := FirstOK(adapters(a, b), request, 800, timer2)
	a.Fail("down")
	check(t, "one failure is not a verdict", !none.IsDone())
	timer2.Advance(800)
	check(t, "all failed or timed out yields nil", none.IsDone() && none.MustGet() == nil)
}

func TestExecutorModels(t *testing.T) {
	pool2 := NewFixedPool(2)
	check(t, "fixed pool of 2: only 2 of 3 blocking tasks start", CountStartedBeforeRelease(pool2, 3, 2) == 2)
	pool2.Close()
	pool4 := NewFixedPool(4)
	check(t, "fixed pool of 4: all 3 start", CountStartedBeforeRelease(pool4, 3, 3) == 3)
	pool4.Close()
	perTask := &GoroutinePerTask{}
	check(t, "goroutine per task: all 3 start", CountStartedBeforeRelease(perTask, 3, 3) == 3)
	check(t, "goroutine per task: 200 blocked tasks all start", CountStartedBeforeRelease(perTask, 200, 200) == 200)
	perTask.Close()
}

func TestBlockingFanOut(t *testing.T) {
	perTask := &GoroutinePerTask{}
	defer perTask.Close()
	alpha := Immediate("alpha", usd(329))
	beta := Immediate("beta", usd(301))
	quotes := BlockingQuoteAll(perTask, adapters(alpha, throwingAdapter{}, beta), request)
	check(t, "blocking fan-out keeps order", quotes[0].Processor == "alpha" && quotes[2].Processor == "beta")
	check(t, "blocking fan-out reports a failed task as FAILED under the adapter's name", quotes[1].Outcome == Failed && quotes[1].Processor == "broken")
	cheapest, _ := Cheapest(quotes)
	check(t, "blocking fan-out cheapest", cheapest.Processor == "beta")
}
