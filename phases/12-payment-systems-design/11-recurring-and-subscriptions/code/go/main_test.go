// Tests for phases/12-payment-systems-design/11-recurring-and-subscriptions. Run: go test ./...
package main

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func check(t *testing.T, name string, condition bool) {
	t.Helper()
	if !condition {
		t.Errorf("FAIL: %s", name)
	}
}

func checkErr(t *testing.T, name string, err error, want error) {
	t.Helper()
	if err == nil {
		t.Errorf("FAIL: %s (no error)", name)
	} else if !errors.Is(err, want) {
		t.Errorf("FAIL: %s (wrong error %v)", name, err)
	}
}

type rig struct {
	clock   *FakeClock
	gateway *ScriptedGateway
	updater *MapCardUpdater
	engine  *BillingEngine
}

func newRig(start time.Time, policy DunningPolicy) *rig {
	clock, gateway, updater := NewFakeClock(start), NewScriptedGateway(), NewMapCardUpdater()
	return &rig{clock, gateway, updater, NewBillingEngine(clock, gateway, updater, policy)}
}

func (r *rig) runDays(days int) {
	for i := 0; i < days; i++ {
		r.engine.RunDay()
		r.clock.AdvanceDays(1)
	}
}

func (r *rig) subscribe(t *testing.T, id, customer string, plan Plan, token string, trialDays int) *Subscription {
	t.Helper()
	s, err := r.engine.Subscribe(id, customer, plan, token, trialDays)
	if err != nil {
		t.Fatalf("subscribe %s: %v", id, err)
	}
	return s
}

func periodStart(t *testing.T, anchor time.Time, interval Interval, n int64) time.Time {
	t.Helper()
	d, err := PeriodStart(anchor, interval, n)
	if err != nil {
		t.Fatalf("period start %d: %v", n, err)
	}
	return d
}

func proration(t *testing.T, oldPlan, newPlan Plan, start, end, change time.Time) ProrationResult {
	t.Helper()
	p, err := ComputeProration(oldPlan, newPlan, start, end, change)
	if err != nil {
		t.Fatalf("proration: %v", err)
	}
	return p
}

var jan31 = Date(2026, 1, 31)

func TestBillingCyclesAreComputedFromTheAnchor(t *testing.T) {
	cases := []struct {
		name     string
		anchor   time.Time
		interval Interval
		n        int64
		want     time.Time
	}{
		{"anchor period 1 clamps to Feb 28", jan31, MONTHLY, 1, Date(2026, 2, 28)},
		{"anchor period 2 returns to Mar 31", jan31, MONTHLY, 2, Date(2026, 3, 31)},
		{"leap year: Jan 31 anchor -> Feb 29 2028", Date(2028, 1, 31), MONTHLY, 1, Date(2028, 2, 29)},
		{"yearly Feb 29 anchor -> Feb 28 in a common year", Date(2028, 2, 29), YEARLY, 1, Date(2029, 2, 28)},
		{"yearly Feb 29 anchor -> Feb 29 again four years on", Date(2028, 2, 29), YEARLY, 4, Date(2032, 2, 29)},
		{"quarterly from Nov 30 -> Feb 28", Date(2025, 11, 30), QUARTERLY, 1, Date(2026, 2, 28)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { check(t, c.name, periodStart(t, c.anchor, c.interval, c.n).Equal(c.want)) })
	}
	check(t, "chained dates drift to Mar 28", ChainedStarts(jan31, MONTHLY, 3)[2].Equal(Date(2026, 3, 28)))
	end, err := PeriodEnd(jan31, MONTHLY, 0)
	check(t, "period end is the next period's start", err == nil && end.Equal(Date(2026, 2, 28)))
	_, err = PeriodStart(jan31, MONTHLY, -1)
	checkErr(t, "negative period index rejected", err, ErrInvalidArgument)
}

func TestProrationWorkedExampleToTheCent(t *testing.T) {
	p := proration(t, BASIC, PRO, Date(2026, 3, 1), Date(2026, 4, 1), Date(2026, 3, 11))
	check(t, "used 10 days", p.UsedDays == 10)
	check(t, "remaining 21 of 31 days", p.RemainingDays == 21 && p.PeriodDays == 31)
	check(t, "credit 20.32", p.Credit == mustParse("20.32", USD))
	check(t, "charge 33.87", p.Charge == mustParse("33.87", USD))
	check(t, "net 13.55", p.Net() == mustParse("13.55", USD))
	down := proration(t, PRO, BASIC, Date(2026, 3, 1), Date(2026, 4, 1), Date(2026, 3, 11))
	check(t, "downgrade net is negative 13.55", down.Net() == mustParse("-13.55", USD))
	check(t, "change on the first day credits the whole old price",
		proration(t, BASIC, PRO, Date(2026, 3, 1), Date(2026, 4, 1), Date(2026, 3, 1)).Credit == BASIC.Price)

	// The invariant: used portion + credit == price, for every day of every month length.
	conserved := true
	odd := must(NewPlan("odd", mustParse("9.99", USD), MONTHLY))
	for _, start := range []time.Time{Date(2026, 2, 1), Date(2026, 4, 1), Date(2026, 1, 1), Date(2028, 2, 1)} {
		end := plusMonths(start, 1)
		for day := start; day.Before(end); day = day.AddDate(0, 0, 1) {
			r := proration(t, odd, PRO, start, end, day)
			split := must(odd.Price.Allocate(r.UsedDays, r.RemainingDays))
			conserved = conserved && mustPlus(split[0], split[1]) == odd.Price && split[1] == r.Credit
		}
	}
	check(t, "allocation conserves the price for every change day", conserved)
	halves := must(odd.Price.Allocate(15, 15))
	check(t, "9.99 split 15:15 is 5.00 + 4.99", halves[0].MinorUnits == 500 && halves[1].MinorUnits == 499)
	_, err := ComputeProration(BASIC, PRO, Date(2026, 3, 1), Date(2026, 4, 1), Date(2026, 4, 1))
	checkErr(t, "change date outside the period rejected", err, ErrInvalidArgument)
	eu := must(NewPlan("eu", mustParse("30.00", EUR), MONTHLY))
	_, err = ComputeProration(BASIC, eu, Date(2026, 3, 1), Date(2026, 4, 1), Date(2026, 3, 11))
	checkErr(t, "cross-currency plan change rejected", err, ErrCurrencyMismatch)
}

func TestStateMachine(t *testing.T) {
	check(t, "PAST_DUE -> ACTIVE allowed", TransitionAllowed(PAST_DUE, ACTIVE))
	check(t, "ACTIVE -> UNPAID not allowed (must go through PAST_DUE)", !TransitionAllowed(ACTIVE, UNPAID))
	check(t, "CANCELED is terminal", len(AllowedTransitions[CANCELED]) == 0)
	r := newRig(jan31, StandardDunningPolicy())
	s := r.subscribe(t, "s", "c", BASIC, "tok", 0)
	mustOK(r.engine.Cancel("s", false))
	checkErr(t, "illegal transition fails", s.Transition(ACTIVE), ErrIllegalTransition)
}

func TestDunningScheduleThenExhaustion(t *testing.T) {
	r := newRig(jan31, StandardDunningPolicy())
	r.gateway.Script("tok_broke", INSUFFICIENT_FUNDS, INSUFFICIENT_FUNDS, INSUFFICIENT_FUNDS, INSUFFICIENT_FUNDS, INSUFFICIENT_FUNDS)
	r.subscribe(t, "sub_trial", "cus", PRO, "tok_broke", 14)
	check(t, "trial starts TRIALING", r.engine.Subscription("sub_trial").Status == TRIALING)
	r.runDays(14)
	check(t, "no charge during the trial", len(r.gateway.Charges()) == 0)
	r.runDays(1) // Feb 14
	inv := r.engine.Invoice("sub_trial:2026-02-14")
	if inv == nil {
		t.Fatal("invoice sub_trial:2026-02-14 missing")
	}
	check(t, "first failure moves TRIALING -> PAST_DUE", r.engine.Subscription("sub_trial").Status == PAST_DUE)
	check(t, "first retry is +1 day", inv.NextAttempt.Equal(Date(2026, 2, 15)))
	r.runDays(1)
	check(t, "second retry is +3 days", inv.NextAttempt.Equal(Date(2026, 2, 17)))
	r.runDays(13) // Feb 16 .. Feb 28: attempts on Feb 17, Feb 21, Feb 28
	check(t, "five attempts in total", inv.Attempts == StandardDunningPolicy().MaxAttempts())
	check(t, "invoice UNCOLLECTIBLE after exhaustion", inv.Status == UNCOLLECTIBLE)
	check(t, "subscription CANCELED after exhaustion", r.engine.Subscription("sub_trial").Status == CANCELED)
	r.runDays(60)
	check(t, "a canceled subscription is never billed again", len(r.gateway.Charges()) == 5)
}

func TestRecoveryIdempotentDaysAndCatchUp(t *testing.T) {
	r := newRig(jan31, StandardDunningPolicy())
	r.gateway.Script("tok_flaky", APPROVED, INSUFFICIENT_FUNDS, APPROVED)
	r.subscribe(t, "s", "c", BASIC, "tok_flaky", 0)
	r.engine.RunDay()
	r.engine.RunDay()
	check(t, "same-day rerun does not charge twice", len(r.gateway.Charges()) == 1)
	r.clock.Set(Date(2026, 2, 28))
	r.engine.RunDay()
	check(t, "renewal failure -> PAST_DUE", r.engine.Subscription("s").Status == PAST_DUE)
	r.clock.Set(Date(2026, 3, 1))
	r.engine.RunDay()
	check(t, "retry success -> ACTIVE", r.engine.Subscription("s").Status == ACTIVE)
	check(t, "retry used a new reference", strings.HasPrefix(r.gateway.Charges()[2], "s:2026-02-28#2 "))
	r.clock.Set(Date(2026, 6, 15))
	r.engine.RunDay()
	check(t, "catch-up bills each missed period exactly once",
		r.engine.Invoice("s:2026-03-31") != nil && r.engine.Invoice("s:2026-04-30") != nil &&
			r.engine.Invoice("s:2026-05-31") != nil && len(r.engine.Invoices()) == 5)
	check(t, "after catch-up the next bill is the anchored Jun 30", r.engine.Subscription("s").CurrentPeriodEnd.Equal(Date(2026, 6, 30)))
}

func TestHardDeclinesAndTheCardUpdater(t *testing.T) {
	r := newRig(jan31, StandardDunningPolicy())
	r.gateway.Script("tok_stolen", DO_NOT_RETRY)
	r.gateway.Script("tok_expired", EXPIRED_CARD)
	r.gateway.Script("tok_old", EXPIRED_CARD)
	r.updater.Replace("tok_old", "tok_new")
	r.subscribe(t, "stolen", "c1", BASIC, "tok_stolen", 0)
	r.subscribe(t, "expired", "c2", BASIC, "tok_expired", 0)
	r.subscribe(t, "updated", "c3", BASIC, "tok_old", 0)
	r.engine.RunDay()
	check(t, "DO_NOT_RETRY exhausts after one attempt", r.engine.Invoice("stolen:2026-01-31").Attempts == 1 &&
		r.engine.Subscription("stolen").Status == CANCELED)
	check(t, "expired card with no update is not retried", r.engine.Invoice("expired:2026-01-31").Status == UNCOLLECTIBLE)
	check(t, "card updater replaces the token", r.engine.Subscription("updated").CardToken == "tok_new")
	check(t, "updated card charged the same day", r.engine.Invoice("updated:2026-01-31").Status == PAID)
	check(t, "updater was consulted for both expired cards", r.updater.Calls() == 2)
}

func TestUnpaidPolicyKeepsTheSubscriptionButStopsBilling(t *testing.T) {
	r := newRig(jan31, must(NewDunningPolicy([]int{2}, UNPAID)))
	r.gateway.Script("tok_x", INSUFFICIENT_FUNDS, INSUFFICIENT_FUNDS)
	r.subscribe(t, "u", "c", BASIC, "tok_x", 0)
	r.runDays(40)
	check(t, "exhausted with UNPAID policy -> UNPAID", r.engine.Subscription("u").Status == UNPAID)
	check(t, "UNPAID creates no further invoices", len(r.engine.Invoices()) == 1)
}

func TestCancelAtPeriodEndAndPlanChanges(t *testing.T) {
	r := newRig(Date(2026, 3, 1), StandardDunningPolicy())
	r.subscribe(t, "up", "c", BASIC, "tok", 0)
	r.subscribe(t, "dn", "c", PRO, "tok", 0)
	r.subscribe(t, "bye", "c", BASIC, "tok", 0)
	r.engine.RunDay()
	r.clock.Set(Date(2026, 3, 11))
	must(r.engine.ChangePlan("up", PRO))
	up := r.engine.Invoice("up:proration:2026-03-11")
	check(t, "upgrade invoices the net now", up != nil && up.Amount == mustParse("13.55", USD) && up.Status == PAID)
	must(r.engine.ChangePlan("dn", BASIC))
	check(t, "downgrade becomes credit", r.engine.Subscription("dn").CreditBalance == mustParse("13.55", USD))
	mustOK(r.engine.Cancel("bye", true))
	r.clock.Set(Date(2026, 4, 1))
	r.engine.RunDay()
	check(t, "credit reduces the next invoice", r.engine.Invoice("dn:2026-04-01").Amount == mustParse("16.45", USD))
	check(t, "upgraded plan bills the new price at renewal", r.engine.Invoice("up:2026-04-01").Amount == PRO.Price)
	check(t, "cancel at period end -> CANCELED, no new invoice",
		r.engine.Subscription("bye").Status == CANCELED && r.engine.Invoice("bye:2026-04-01") == nil)
}

func TestPolicyValidationAndMoneyDiscipline(t *testing.T) {
	_, err := NewDunningPolicy([]int{3, 3}, CANCELED)
	checkErr(t, "non-increasing retry offsets rejected", err, ErrInvalidArgument)
	_, err = NewDunningPolicy([]int{1}, ACTIVE)
	checkErr(t, "exhaustion must end CANCELED or UNPAID", err, ErrInvalidArgument)
	_, err = BASIC.Price.Plus(mustParse("1.00", EUR))
	checkErr(t, "Money rejects currency mix", err, ErrCurrencyMismatch)
}
