// Tests for phases/12-payment-systems-design/06-refunds-and-partial-refunds. Run: go test ./...
package main

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func usd(minor int64) Money { return OfMinor(minor, USD) }

func check(t *testing.T, name string, condition bool) {
	t.Helper()
	if !condition {
		t.Errorf("FAIL: %s", name)
	}
}

type fixture struct {
	clock  *FakeClock
	ledger *Ledger
	svc    *RefundService
}

func newFixture(t *testing.T, fees FeeRefundPolicy) *fixture {
	t.Helper()
	clock := NewFakeClock(Date(2026, time.March, 1))
	ledger := NewLedger(USD)
	policy, err := NewRefundPolicy(180, fees)
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{clock, ledger, NewRefundService(ledger, clock, policy)}
}

func (f *fixture) capture(t *testing.T, id, pay string, amount Money) *Capture {
	t.Helper()
	c, err := f.svc.Capture(id, pay, amount, 290, 30)
	if err != nil {
		t.Fatalf("capture %s: %v", id, err)
	}
	return c
}

func (f *fixture) request(t *testing.T, id, cap string, amount Money, reason string) RefundResult {
	t.Helper()
	r, err := f.svc.RequestRefund(id, cap, amount, reason)
	if err != nil {
		t.Fatalf("request %s: %v", id, err)
	}
	return r
}

func (f *fixture) succeed(t *testing.T, id string) bool {
	t.Helper()
	ok, err := f.svc.Succeed(id)
	if err != nil {
		t.Fatalf("succeed %s: %v", id, err)
	}
	return ok
}

func (f *fixture) refund(t *testing.T, id string) *Refund {
	t.Helper()
	r, err := f.svc.Refund(id)
	if err != nil {
		t.Fatalf("refund %s: %v", id, err)
	}
	return r
}

func (f *fixture) remaining(t *testing.T, cap string) Money {
	t.Helper()
	m, err := f.svc.RemainingRefundable(cap)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func (f *fixture) refunded(t *testing.T, cap string) Money {
	t.Helper()
	m, err := f.svc.Refunded(cap)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func (f *fixture) pending(t *testing.T, cap string) Money {
	t.Helper()
	m, err := f.svc.Pending(cap)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func (f *fixture) feeReturned(t *testing.T, cap string) Money {
	t.Helper()
	m, err := f.svc.FeeReturned(cap)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func (f *fixture) refundsFor(t *testing.T, cap string) []*Refund {
	t.Helper()
	rs, err := f.svc.RefundsFor(cap)
	if err != nil {
		t.Fatal(err)
	}
	return rs
}

func TestCaptureAndPartialRefunds(t *testing.T) {
	f := newFixture(t, PROPORTIONAL)
	c := f.capture(t, "cap", "pay", usd(10_000))
	check(t, "fee 2.90% + 0.30 on 100.00 = 3.20", c.Fee == usd(320))
	check(t, "capture: scheme owes gross, merchant owed net, fee kept",
		f.ledger.Balance(SCHEME) == usd(10_000) && f.ledger.Balance(MERCHANT) == usd(9_680) && f.ledger.Balance(FEES) == usd(320))
	check(t, "remaining refundable starts at captured", f.remaining(t, "cap") == usd(10_000))

	r1 := f.request(t, "r1", "cap", usd(3_000), "partial")
	check(t, "request -> PENDING, not replayed", r1.Refund.State == PENDING && !r1.Replayed)
	check(t, "pending consumes remaining", f.remaining(t, "cap") == usd(7_000) && f.pending(t, "cap") == usd(3_000))
	check(t, "pending moved to refunds_payable", f.ledger.Balance(REFUNDS) == usd(3_000) && f.ledger.Balance(MERCHANT) == usd(6_680))
	check(t, "succeed -> true", f.succeed(t, "r1"))
	check(t, "refunded 30.00, nothing pending", f.refunded(t, "cap") == usd(3_000) && f.pending(t, "cap").IsZero())
	check(t, "proportional fee share floor(320 x 3000 / 10000) = 96", f.refund(t, "r1").FeeReturned == usd(96))
	check(t, "redelivered success is a no-op", !f.succeed(t, "r1") && f.feeReturned(t, "cap") == usd(96))
	f.request(t, "r2", "cap", usd(2_000), "second partial")
	f.succeed(t, "r2")
	check(t, "multiple partials accumulate", f.refunded(t, "cap") == usd(5_000) && f.remaining(t, "cap") == usd(5_000))
	check(t, "invariants hold mid-way", len(f.svc.CheckInvariants()) == 0)

	// --- over-refund
	_, err := f.svc.RequestRefund("r3", "cap", usd(5_001), "too much")
	check(t, "refund above remaining refused", errors.Is(err, ErrOverRefund) && errors.Is(err, ErrRefund))
	f.request(t, "r4", "cap", usd(4_000), "pending big one")
	_, err = f.svc.RequestRefund("r5", "cap", usd(1_001), "race")
	check(t, "pending counts: 10.01 over 10.00 remaining refused", errors.Is(err, ErrOverRefund))
	check(t, "refused requests left no refund record", len(f.refundsFor(t, "cap")) == 3)
	exact := f.request(t, "r6", "cap", usd(1_000), "exact remainder")
	check(t, "exact remainder accepted", exact.Refund.State == PENDING && f.remaining(t, "cap").IsZero())
	_, err = f.svc.RequestRefund("r7", "cap", usd(1), "x")
	check(t, "one minor unit more refused", errors.Is(err, ErrOverRefund))

	// --- failure releases the reservation
	if err := f.svc.Fail("r4", "issuer rejected credit"); err != nil {
		t.Fatal(err)
	}
	check(t, "failed refund -> FAILED and balance refundable again", f.refund(t, "r4").State == FAILED && f.remaining(t, "cap") == usd(4_000))
	check(t, "refunds_payable back to the still-pending r6 only", f.ledger.Balance(REFUNDS) == usd(1_000))
	_, err = f.svc.Succeed("r4")
	check(t, "cannot succeed a FAILED refund", errors.Is(err, ErrRefund) && !errors.Is(err, ErrOverRefund))
	check(t, "cannot fail a SUCCEEDED refund", errors.Is(f.svc.Fail("r1", "late"), ErrRefund))

	// --- full refund: fee shares sum to the fee exactly
	f.succeed(t, "r6")
	f.request(t, "r8", "cap", usd(4_000), "rest")
	f.succeed(t, "r8")
	check(t, "fully refunded", f.refunded(t, "cap") == usd(10_000) && f.remaining(t, "cap").IsZero())
	check(t, "fee returned exactly 3.20", f.feeReturned(t, "cap") == usd(320))
	check(t, "merchant and fee accounts back to zero", f.ledger.Balance(MERCHANT).IsZero() && f.ledger.Balance(FEES).IsZero() && f.ledger.Balance(SCHEME).IsZero())
	check(t, "invariants hold after full refund", len(f.svc.CheckInvariants()) == 0)
}

func TestRoundingDrift(t *testing.T) {
	g := newFixture(t, PROPORTIONAL)
	small := g.capture(t, "c2", "p2", usd(1_000))
	check(t, "10.00 capture fee = 0.59", small.Fee == usd(59))
	for i, part := range []int64{333, 333, 334} {
		id := fmt.Sprintf("s%d", i)
		g.request(t, id, "c2", usd(part), "split")
		g.succeed(t, id)
	}
	check(t, "cumulative shares 0.19, 0.20, 0.20", g.refund(t, "s0").FeeReturned == usd(19) && g.refund(t, "s1").FeeReturned == usd(20) && g.refund(t, "s2").FeeReturned == usd(20))
	check(t, "shares sum to the fee, no lost cents", g.feeReturned(t, "c2") == usd(59))
	h := newFixture(t, PROPORTIONAL)
	h.capture(t, "c3", "p3", usd(1_000))
	for i := 0; i < 1_000; i++ {
		id := fmt.Sprintf("u%d", i)
		h.request(t, id, "c3", usd(1), "cent")
		h.succeed(t, id)
	}
	check(t, "1000 one-cent refunds still return exactly 0.59", h.feeReturned(t, "c3") == usd(59) && len(h.svc.CheckInvariants()) == 0)
}

func TestFeePolicyNone(t *testing.T) {
	n := newFixture(t, NONE)
	n.capture(t, "cn", "pn", usd(10_000))
	n.request(t, "rn", "cn", usd(10_000), "all")
	n.succeed(t, "rn")
	check(t, "NONE: no fee returned", n.feeReturned(t, "cn").IsZero())
	check(t, "NONE: merchant owes the fee (negative payable)", n.ledger.Balance(MERCHANT) == usd(-320) && n.ledger.Balances())
}

func TestIdempotencyAndValidation(t *testing.T) {
	i := newFixture(t, PROPORTIONAL)
	i.capture(t, "ci", "pi", usd(5_000))
	first := i.request(t, "ri", "ci", usd(1_000), "a")
	replay := i.request(t, "ri", "ci", usd(1_000), "a retry")
	check(t, "same id, same amount -> replay of the same refund", replay.Replayed && replay.Refund == first.Refund)
	check(t, "replay did not reserve twice", i.pending(t, "ci") == usd(1_000) && i.ledger.Balance(REFUNDS) == usd(1_000))
	_, err := i.svc.RequestRefund("ri", "ci", usd(2_000), "bug")
	check(t, "same id, different amount -> conflict", errors.Is(err, ErrIdempotencyConflict))

	_, err = i.svc.RequestRefund("z", "ci", usd(0), "x")
	check(t, "zero refund refused", errors.Is(err, ErrInvalidArgument))
	_, err = i.svc.RequestRefund("e", "ci", OfMinor(100, EUR), "x")
	check(t, "EUR refund on USD capture refused", errors.Is(err, ErrCurrencyMismatch))
	_, err = i.svc.RequestRefund("u", "nope", usd(1), "x")
	check(t, "unknown capture refused", errors.Is(err, ErrInvalidArgument))
	_, err = NewJournalEntry("bad", "x", []Posting{Debit("a", usd(2)), Credit("b", usd(1))})
	check(t, "unbalanced journal entry refused", errors.Is(err, ErrInvalidArgument))
	_, err = i.svc.Capture("ci", "pi", usd(1), 290, 30)
	check(t, "duplicate capture id refused", errors.Is(err, ErrIllegalState))
}

func TestRefundWindow(t *testing.T) {
	w := newFixture(t, PROPORTIONAL)
	w.capture(t, "cw", "pw", usd(5_000))
	w.clock.AdvanceDays(180)
	check(t, "day 180 is inside a 180-day window", w.request(t, "w1", "cw", usd(100), "edge").Refund.State == PENDING)
	w.clock.AdvanceDays(1)
	_, err := w.svc.RequestRefund("w2", "cw", usd(100), "late")
	check(t, "day 181 is outside", errors.Is(err, ErrWindowClosed))
	check(t, "a pending refund requested in-window can still settle after it closes", w.succeed(t, "w1"))
}

// Ten goroutines race for 15.00 each on a 100.00 capture; the mutex makes check-then-reserve atomic.
func TestConcurrentPartialRefunds(t *testing.T) {
	cc := newFixture(t, PROPORTIONAL)
	cc.capture(t, "cr", "pr", usd(10_000))
	var accepted, refused atomic.Int32
	start := make(chan struct{})
	var wg sync.WaitGroup
	for id := 0; id < 10; id++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			<-start
			_, err := cc.svc.RequestRefund(fmt.Sprintf("race%d", id), "cr", usd(1_500), "race")
			switch {
			case err == nil:
				accepted.Add(1)
			case errors.Is(err, ErrOverRefund):
				refused.Add(1)
			default:
				t.Errorf("unexpected error: %v", err)
			}
		}(id)
	}
	close(start)
	wg.Wait()
	check(t, "exactly 6 of 10 concurrent 15.00 refunds fit in 100.00", accepted.Load() == 6 && refused.Load() == 4)
	check(t, "remaining after the race is 10.00", cc.remaining(t, "cr") == usd(1_000) && len(cc.svc.CheckInvariants()) == 0)
}
