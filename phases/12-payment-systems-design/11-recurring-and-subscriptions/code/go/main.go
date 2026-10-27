// Lesson: phases/12-payment-systems-design/11-recurring-and-subscriptions/docs/en.md
// Recurring Payments, Subscriptions, and Dunning
// Run:  go run .
// Test: go test ./...
// Sources: Visa/Mastercard stored-credential (credential-on-file) framework; Visa Account Updater;
// Stripe Billing docs (subscription statuses, proration). Go 1.22+, standard library only.
package main

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	// ErrInvalidArgument is Java's IllegalArgumentException: bad plans, policies, period indexes, change dates.
	ErrInvalidArgument = errors.New("invalid argument")
	// ErrIllegalState is Java's IllegalStateException: duplicate subscriptions, plan changes off ACTIVE.
	ErrIllegalState = errors.New("illegal state")
	// ErrIllegalTransition is the subscription state machine refusing a move the table does not allow.
	ErrIllegalTransition = fmt.Errorf("%w: illegal transition", ErrIllegalState)
)

// ---------------------------------------------------------------------------------------------
// Time is injected. Billing runs at day granularity on the merchant's calendar. Dates are
// time.Time values at midnight UTC (Java's LocalDate).
// ---------------------------------------------------------------------------------------------

type Clock interface{ Today() time.Time }

type FakeClock struct{ today time.Time }

func NewFakeClock(start time.Time) *FakeClock { return &FakeClock{start} }
func (c *FakeClock) Today() time.Time         { return c.today }
func (c *FakeClock) AdvanceDays(days int)     { c.today = c.today.AddDate(0, 0, days) }
func (c *FakeClock) Set(date time.Time)       { c.today = date }
func Date(year int, month time.Month, day int) time.Time {
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
}

func fmtDate(t time.Time) string { return t.Format("2006-01-02") }

func fmtDates(dates []time.Time) string {
	parts := make([]string, len(dates))
	for i, d := range dates {
		parts[i] = fmtDate(d)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// daysBetween is ChronoUnit.DAYS.between for midnight-UTC dates.
func daysBetween(from, to time.Time) int64 { return int64(to.Sub(from).Hours() / 24) }

// plusMonths is LocalDate.plusMonths: the day-of-month is clamped to the target month's length.
// time.Time.AddDate normalizes instead (Jan 31 + 1 month = Mar 3), which is exactly the drift bug.
func plusMonths(d time.Time, months int64) time.Time {
	y, m, day := d.Date()
	total := int64(y)*12 + int64(m-1) + months
	ny, nm := total/12, total%12
	if nm < 0 { // floor division, so dates before year 0 would still land in the right month
		nm += 12
		ny--
	}
	last := time.Date(int(ny), time.Month(nm+2), 0, 0, 0, 0, 0, time.UTC).Day() // day 0 of next month = last day
	if day > last {
		day = last
	}
	return Date(int(ny), time.Month(nm+1), day)
}

// ---------------------------------------------------------------------------------------------
// Billing cycles: every period boundary is computed from the anchor, never from the previous boundary.
// ---------------------------------------------------------------------------------------------

type Interval int

const (
	MONTHLY Interval = iota
	QUARTERLY
	YEARLY
)

var intervalMonths = [...]int64{1, 3, 12}

func (i Interval) Months() int64  { return intervalMonths[i] }
func (i Interval) String() string { return [...]string{"MONTHLY", "QUARTERLY", "YEARLY"}[i] }

type Plan struct {
	ID       string
	Price    Money
	Interval Interval
}

func NewPlan(id string, price Money, interval Interval) (Plan, error) {
	if price.IsNegative() {
		return Plan{}, fmt.Errorf("%w: price must be non-negative", ErrInvalidArgument)
	}
	return Plan{id, price, interval}, nil
}

// PeriodStart is the start of period n (n = 0 is the anchor). plusMonths clamps to month end, e.g. Jan 31 + 1 month = Feb 28.
func PeriodStart(anchor time.Time, interval Interval, n int64) (time.Time, error) {
	if n < 0 {
		return time.Time{}, fmt.Errorf("%w: period index must be >= 0", ErrInvalidArgument)
	}
	months, err := mulChecked(n, interval.Months())
	if err != nil {
		return time.Time{}, err
	}
	return plusMonths(anchor, months), nil
}

func PeriodEnd(anchor time.Time, interval Interval, n int64) (time.Time, error) {
	return PeriodStart(anchor, interval, n+1)
}

// ChainedStarts is the bug this lesson warns about: each boundary computed from the previous one drifts after a short month.
func ChainedStarts(anchor time.Time, interval Interval, count int) []time.Time {
	out := make([]time.Time, 0, count)
	d := anchor
	for i := 0; i < count; i++ {
		out = append(out, d)
		d = plusMonths(d, interval.Months())
	}
	return out
}

func AnchoredStarts(anchor time.Time, interval Interval, count int) []time.Time {
	out := make([]time.Time, 0, count)
	for i := 0; i < count; i++ {
		start, _ := PeriodStart(anchor, interval, int64(i)) // i >= 0, so the only error path cannot trigger
		out = append(out, start)
	}
	return out
}

// ---------------------------------------------------------------------------------------------
// Proration: unused time on the old plan is credited, remaining time on the new plan is charged.
// ---------------------------------------------------------------------------------------------

type ProrationResult struct {
	UsedDays      int64
	RemainingDays int64
	PeriodDays    int64
	Credit        Money
	Charge        Money
}

func (p ProrationResult) Net() Money { return mustMinus(p.Charge, p.Credit) }

// ComputeProration is day-based proration with exact allocation. The old price is split used:remaining
// with largest-remainder allocation, so usedPortion + credit == old price to the minor unit.
func ComputeProration(oldPlan, newPlan Plan, periodStart, periodEnd, changeDate time.Time) (ProrationResult, error) {
	if changeDate.Before(periodStart) || !changeDate.Before(periodEnd) {
		return ProrationResult{}, fmt.Errorf("%w: change date %s outside period [%s, %s)", ErrInvalidArgument,
			fmtDate(changeDate), fmtDate(periodStart), fmtDate(periodEnd))
	}
	if oldPlan.Price.Currency != newPlan.Price.Currency {
		return ProrationResult{}, fmt.Errorf("%w: cannot combine %s with %s", ErrCurrencyMismatch,
			oldPlan.Price.Currency.Code, newPlan.Price.Currency.Code)
	}
	period := daysBetween(periodStart, periodEnd)
	used := daysBetween(periodStart, changeDate)
	remaining := period - used
	credit, err := oldPlan.Price.Allocate(used, remaining)
	if err != nil {
		return ProrationResult{}, err
	}
	charge, err := newPlan.Price.Allocate(used, remaining)
	if err != nil {
		return ProrationResult{}, err
	}
	return ProrationResult{used, remaining, period, credit[1], charge[1]}, nil
}

// ---------------------------------------------------------------------------------------------
// The subscription state machine.
// ---------------------------------------------------------------------------------------------

type SubscriptionStatus int

const (
	TRIALING SubscriptionStatus = iota
	ACTIVE
	PAST_DUE
	UNPAID
	CANCELED
)

func (s SubscriptionStatus) String() string {
	return [...]string{"TRIALING", "ACTIVE", "PAST_DUE", "UNPAID", "CANCELED"}[s]
}

// AllowedTransitions is the state table: from -> the set of states it may move to.
var AllowedTransitions = map[SubscriptionStatus][]SubscriptionStatus{
	TRIALING: {ACTIVE, PAST_DUE, CANCELED},
	ACTIVE:   {PAST_DUE, CANCELED},
	PAST_DUE: {ACTIVE, UNPAID, CANCELED},
	UNPAID:   {ACTIVE, CANCELED},
	CANCELED: {},
}

func TransitionAllowed(from, to SubscriptionStatus) bool {
	for _, s := range AllowedTransitions[from] {
		if s == to {
			return true
		}
	}
	return false
}

type Subscription struct {
	ID                 string
	CustomerID         string
	Anchor             time.Time
	Plan               Plan
	CardToken          string
	Status             SubscriptionStatus
	PeriodIndex        int64 // -1 while trialing: period 0 starts at the anchor
	CurrentPeriodStart time.Time
	CurrentPeriodEnd   time.Time // the next billing date
	CancelAtPeriodEnd  bool
	CreditBalance      Money
}

func NewSubscription(id, customerID string, plan Plan, cardToken string, today, anchor time.Time) *Subscription {
	status := ACTIVE
	if anchor.After(today) {
		status = TRIALING
	}
	return &Subscription{ID: id, CustomerID: customerID, Anchor: anchor, Plan: plan, CardToken: cardToken, Status: status,
		PeriodIndex: -1, CurrentPeriodStart: today, CurrentPeriodEnd: anchor, CreditBalance: Zero(plan.Price.Currency)}
}

func (s *Subscription) Transition(to SubscriptionStatus) error {
	if s.Status == to {
		return nil
	}
	if !TransitionAllowed(s.Status, to) {
		return fmt.Errorf("%w %v -> %v", ErrIllegalTransition, s.Status, to)
	}
	s.Status = to
	return nil
}

func (s *Subscription) Bills() bool { return s.Status != CANCELED && s.Status != UNPAID }

// ---------------------------------------------------------------------------------------------
// Invoices, the gateway, and the card-updater hook.
// ---------------------------------------------------------------------------------------------

type InvoiceStatus int

const (
	OPEN InvoiceStatus = iota
	PAID
	UNCOLLECTIBLE
)

func (s InvoiceStatus) String() string { return [...]string{"OPEN", "PAID", "UNCOLLECTIBLE"}[s] }

// Invoice: FirstFailure and NextAttempt are the zero time.Time where Java held null.
type Invoice struct {
	ID             string
	SubscriptionID string
	PeriodStart    time.Time
	PeriodEnd      time.Time
	Amount         Money
	Description    string
	Status         InvoiceStatus
	Attempts       int
	FirstFailure   time.Time
	NextAttempt    time.Time
	LastOutcome    ChargeOutcome // meaningful once Attempts > 0
}

func NewInvoice(id, subscriptionID string, periodStart, periodEnd time.Time, amount Money, description string, due time.Time) *Invoice {
	return &Invoice{ID: id, SubscriptionID: subscriptionID, PeriodStart: periodStart, PeriodEnd: periodEnd,
		Amount: amount, Description: description, Status: OPEN, NextAttempt: due}
}

type ChargeOutcome int

const (
	APPROVED ChargeOutcome = iota
	INSUFFICIENT_FUNDS
	DO_NOT_HONOR
	EXPIRED_CARD
	DO_NOT_RETRY
)

func (o ChargeOutcome) String() string {
	return [...]string{"APPROVED", "INSUFFICIENT_FUNDS", "DO_NOT_HONOR", "EXPIRED_CARD", "DO_NOT_RETRY"}[o]
}

func (o ChargeOutcome) Retryable() bool { return o == INSUFFICIENT_FUNDS || o == DO_NOT_HONOR }

// ChargeGateway is a merchant-initiated charge on a stored credential; the reference is the gateway's idempotency key.
type ChargeGateway interface {
	Charge(reference, cardToken string, amount Money) ChargeOutcome
}

// CardUpdater is Visa Account Updater / Mastercard ABU style: given an old credential, maybe a replacement.
type CardUpdater interface {
	Refresh(customerID, oldCardToken string) (string, bool)
}

type DunningPolicy struct {
	RetryOffsetsDays []int
	WhenExhausted    SubscriptionStatus
}

func NewDunningPolicy(retryOffsetsDays []int, whenExhausted SubscriptionStatus) (DunningPolicy, error) {
	offsets := append([]int{}, retryOffsetsDays...)
	for i, o := range offsets {
		if o <= 0 || (i > 0 && o <= offsets[i-1]) {
			return DunningPolicy{}, fmt.Errorf("%w: retry offsets must be positive and strictly increasing: %v", ErrInvalidArgument, offsets)
		}
	}
	if whenExhausted != CANCELED && whenExhausted != UNPAID {
		return DunningPolicy{}, fmt.Errorf("%w: exhausted dunning ends CANCELED or UNPAID", ErrInvalidArgument)
	}
	return DunningPolicy{offsets, whenExhausted}, nil
}

func StandardDunningPolicy() DunningPolicy {
	return must(NewDunningPolicy([]int{1, 3, 7, 14}, CANCELED))
}

func (p DunningPolicy) MaxAttempts() int { return 1 + len(p.RetryOffsetsDays) }

// ---------------------------------------------------------------------------------------------
// The engine: one RunDay() per calendar day, safe to call more than once on the same day.
// ---------------------------------------------------------------------------------------------

type BillingEngine struct {
	clock         Clock
	gateway       ChargeGateway
	updater       CardUpdater
	policy        DunningPolicy
	subscriptions map[string]*Subscription
	subOrder      []string // insertion order, like Java's LinkedHashMap
	invoices      map[string]*Invoice
	invoiceOrder  []string
	log           []string
}

func NewBillingEngine(clock Clock, gateway ChargeGateway, updater CardUpdater, policy DunningPolicy) *BillingEngine {
	return &BillingEngine{clock: clock, gateway: gateway, updater: updater, policy: policy,
		subscriptions: map[string]*Subscription{}, invoices: map[string]*Invoice{}}
}

func (e *BillingEngine) Log() []string                        { return e.log }
func (e *BillingEngine) Subscription(id string) *Subscription { return e.subscriptions[id] }
func (e *BillingEngine) Invoice(id string) *Invoice           { return e.invoices[id] }

func (e *BillingEngine) Invoices() []*Invoice {
	out := make([]*Invoice, 0, len(e.invoiceOrder))
	for _, id := range e.invoiceOrder {
		out = append(out, e.invoices[id])
	}
	return out
}

func (e *BillingEngine) Subscribe(id, customerID string, plan Plan, cardToken string, trialDays int) (*Subscription, error) {
	if trialDays < 0 {
		return nil, fmt.Errorf("%w: trialDays must be >= 0", ErrInvalidArgument)
	}
	if _, dup := e.subscriptions[id]; dup {
		return nil, fmt.Errorf("%w: duplicate subscription %s", ErrIllegalState, id)
	}
	today := e.clock.Today()
	s := NewSubscription(id, customerID, plan, cardToken, today, today.AddDate(0, 0, trialDays))
	e.subscriptions[id] = s
	e.subOrder = append(e.subOrder, id)
	e.note(fmt.Sprintf("%s created %v, anchor %s", s.ID, s.Status, fmtDate(s.Anchor)))
	return s, nil
}

func (e *BillingEngine) Cancel(subscriptionID string, atPeriodEnd bool) error {
	s := e.subscriptions[subscriptionID]
	if atPeriodEnd {
		s.CancelAtPeriodEnd = true
		e.note(fmt.Sprintf("%s will cancel at %s", s.ID, fmtDate(s.CurrentPeriodEnd)))
		return nil
	}
	if err := s.Transition(CANCELED); err != nil {
		return err
	}
	e.note(s.ID + " canceled now")
	return nil
}

// ChangePlan is an immediate plan change with proration. A positive net is invoiced now; a negative net becomes credit.
func (e *BillingEngine) ChangePlan(subscriptionID string, newPlan Plan) (ProrationResult, error) {
	s := e.subscriptions[subscriptionID]
	if s.Status != ACTIVE {
		return ProrationResult{}, fmt.Errorf("%w: plan changes require ACTIVE, was %v", ErrIllegalState, s.Status)
	}
	if newPlan.Interval != s.Plan.Interval {
		return ProrationResult{}, fmt.Errorf("%w: interval changes reset the anchor; not modeled", ErrInvalidArgument)
	}
	today := e.clock.Today()
	p, err := ComputeProration(s.Plan, newPlan, s.CurrentPeriodStart, s.CurrentPeriodEnd, today)
	if err != nil {
		return ProrationResult{}, err
	}
	old := s.Plan
	s.Plan = newPlan
	e.note(fmt.Sprintf("%s %s -> %s: credit %v, charge %v, net %v", s.ID, old.ID, newPlan.ID, p.Credit, p.Charge, p.Net()))
	if p.Net().IsPositive() {
		inv := e.openInvoice(s.ID+":proration:"+fmtDate(today), s, s.CurrentPeriodStart, s.CurrentPeriodEnd, p.Net(), "proration", today)
		e.attempt(inv, s, today)
	} else if p.Net().IsNegative() {
		s.CreditBalance = mustMinus(s.CreditBalance, p.Net())
	}
	return p, nil
}

// RunDay runs everything due today. Idempotent: a second call on the same day changes nothing.
func (e *BillingEngine) RunDay() {
	today := e.clock.Today()
	for _, id := range e.subOrder {
		s := e.subscriptions[id]
		for s.Bills() && !today.Before(s.CurrentPeriodEnd) {
			if s.CancelAtPeriodEnd {
				mustOK(s.Transition(CANCELED))
				e.note(s.ID + " canceled at period end")
				break
			}
			e.rollPeriod(s)
		}
	}
	for _, inv := range e.Invoices() {
		if inv.Status == OPEN && !inv.NextAttempt.IsZero() && !today.Before(inv.NextAttempt) {
			s := e.subscriptions[inv.SubscriptionID]
			if s.Status == CANCELED {
				continue
			}
			e.attempt(inv, s, today)
		}
	}
}

func (e *BillingEngine) rollPeriod(s *Subscription) {
	s.PeriodIndex++
	s.CurrentPeriodStart = must(PeriodStart(s.Anchor, s.Plan.Interval, s.PeriodIndex))
	s.CurrentPeriodEnd = must(PeriodEnd(s.Anchor, s.Plan.Interval, s.PeriodIndex))
	due := s.Plan.Price
	applied := s.CreditBalance
	if due.Compare(s.CreditBalance) < 0 {
		applied = due
	}
	s.CreditBalance = mustMinus(s.CreditBalance, applied)
	amount := mustMinus(due, applied)
	id := s.ID + ":" + fmtDate(s.CurrentPeriodStart)
	inv := e.openInvoice(id, s, s.CurrentPeriodStart, s.CurrentPeriodEnd, amount, s.Plan.ID, s.CurrentPeriodStart)
	if amount.IsZero() {
		inv.Status = PAID
		inv.NextAttempt = time.Time{}
		e.note(id + " covered by credit")
	}
}

func (e *BillingEngine) openInvoice(id string, s *Subscription, start, end time.Time, amount Money, description string, due time.Time) *Invoice {
	if existing, ok := e.invoices[id]; ok {
		return existing
	}
	inv := NewInvoice(id, s.ID, start, end, amount, description, due)
	e.invoices[id] = inv
	e.invoiceOrder = append(e.invoiceOrder, id)
	e.note(fmt.Sprintf("invoice %s %v for [%s, %s)", id, amount, fmtDate(start), fmtDate(end)))
	return inv
}

func (e *BillingEngine) attempt(inv *Invoice, s *Subscription, today time.Time) {
	inv.Attempts++
	reference := fmt.Sprintf("%s#%d", inv.ID, inv.Attempts)
	outcome := e.gateway.Charge(reference, s.CardToken, inv.Amount)
	if outcome == EXPIRED_CARD {
		if fresh, ok := e.updater.Refresh(s.CustomerID, s.CardToken); ok && fresh != s.CardToken {
			e.note(fmt.Sprintf("%s EXPIRED_CARD; card updater %s -> %s", reference, s.CardToken, fresh))
			s.CardToken = fresh
			inv.Attempts++
			reference = fmt.Sprintf("%s#%d", inv.ID, inv.Attempts)
			outcome = e.gateway.Charge(reference, s.CardToken, inv.Amount)
		}
	}
	inv.LastOutcome = outcome
	if outcome == APPROVED {
		inv.Status = PAID
		inv.NextAttempt = time.Time{}
		if s.Status != ACTIVE {
			mustOK(s.Transition(ACTIVE))
		}
		e.note(fmt.Sprintf("%s APPROVED %v -> %s %v", reference, inv.Amount, s.ID, s.Status))
		return
	}
	if inv.FirstFailure.IsZero() {
		inv.FirstFailure = today
	}
	if s.Status == ACTIVE || s.Status == TRIALING {
		mustOK(s.Transition(PAST_DUE))
	}
	retriesUsed := e.retriesUsed(inv)
	exhausted := !outcome.Retryable() || retriesUsed >= len(e.policy.RetryOffsetsDays)
	if exhausted {
		inv.Status = UNCOLLECTIBLE
		inv.NextAttempt = time.Time{}
		mustOK(s.Transition(e.policy.WhenExhausted))
		e.note(fmt.Sprintf("%s %v; dunning exhausted -> invoice UNCOLLECTIBLE, %s %v", reference, outcome, s.ID, s.Status))
		return
	}
	inv.NextAttempt = inv.FirstFailure.AddDate(0, 0, e.policy.RetryOffsetsDays[retriesUsed])
	e.note(fmt.Sprintf("%s %v -> %s %v, next attempt %s", reference, outcome, s.ID, s.Status, fmtDate(inv.NextAttempt)))
}

// retriesUsed: scheduled retries consumed so far: every dunning-day attempt after the first failure day.
func (e *BillingEngine) retriesUsed(inv *Invoice) int {
	used := 0
	for _, offset := range e.policy.RetryOffsetsDays {
		if !e.clock.Today().Before(inv.FirstFailure.AddDate(0, 0, offset)) {
			used++
		}
	}
	return used
}

func (e *BillingEngine) note(line string) { e.log = append(e.log, fmtDate(e.clock.Today())+"  "+line) }

// ---------------------------------------------------------------------------------------------
// Fakes: a scripted gateway that dedupes by reference, and a card updater backed by a map.
// ---------------------------------------------------------------------------------------------

type ScriptedGateway struct {
	scripts     map[string][]ChargeOutcome
	byReference map[string]ChargeOutcome
	charges     []string
}

func NewScriptedGateway() *ScriptedGateway {
	return &ScriptedGateway{scripts: map[string][]ChargeOutcome{}, byReference: map[string]ChargeOutcome{}}
}

func (g *ScriptedGateway) Script(cardToken string, outcomes ...ChargeOutcome) {
	g.scripts[cardToken] = append([]ChargeOutcome{}, outcomes...)
}

func (g *ScriptedGateway) Charge(reference, cardToken string, amount Money) ChargeOutcome {
	if prior, ok := g.byReference[reference]; ok {
		return prior
	}
	outcome := APPROVED
	if q := g.scripts[cardToken]; len(q) > 0 {
		outcome = q[0]
		g.scripts[cardToken] = q[1:]
	}
	g.byReference[reference] = outcome
	g.charges = append(g.charges, fmt.Sprintf("%s %s %v %v", reference, cardToken, amount, outcome))
	return outcome
}

func (g *ScriptedGateway) Charges() []string { return g.charges }

func (g *ScriptedGateway) ApprovedCount() int {
	n := 0
	for _, c := range g.charges {
		if strings.HasSuffix(c, "APPROVED") {
			n++
		}
	}
	return n
}

type MapCardUpdater struct {
	replacements map[string]string
	calls        int
}

func NewMapCardUpdater() *MapCardUpdater                    { return &MapCardUpdater{replacements: map[string]string{}} }
func (u *MapCardUpdater) Replace(oldToken, newToken string) { u.replacements[oldToken] = newToken }
func (u *MapCardUpdater) Calls() int                        { return u.calls }

func (u *MapCardUpdater) Refresh(customerID, oldCardToken string) (string, bool) {
	u.calls++
	fresh, ok := u.replacements[oldCardToken]
	return fresh, ok
}

// ---------------------------------------------------------------------------------------------
// Demo.
// ---------------------------------------------------------------------------------------------

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func mustOK(err error) {
	if err != nil {
		panic(err)
	}
}

// mustMinus is for same-currency values the engine already validated; a mismatch is a bug.
func mustMinus(a, b Money) Money { return must(a.Minus(b)) }

var (
	BASIC = must(NewPlan("basic", mustParse("30.00", USD), MONTHLY))
	PRO   = must(NewPlan("pro", mustParse("50.00", USD), MONTHLY))
)

func main() {
	fmt.Println("== Anchored billing dates vs chained dates (anchor 2026-01-31) ==")
	jan31 := Date(2026, 1, 31)
	fmt.Println("anchored: " + fmtDates(AnchoredStarts(jan31, MONTHLY, 6)))
	fmt.Println("chained:  " + fmtDates(ChainedStarts(jan31, MONTHLY, 6)) + "  <- drifted to the 28th forever")

	fmt.Println("\n== Proration: basic 30.00 -> pro 50.00 on 2026-03-11, period [2026-03-01, 2026-04-01) ==")
	p := must(ComputeProration(BASIC, PRO, Date(2026, 3, 1), Date(2026, 4, 1), Date(2026, 3, 11)))
	fmt.Printf("used %d / remaining %d / period %d days\n", p.UsedDays, p.RemainingDays, p.PeriodDays)
	basicSplit := must(BASIC.Price.Allocate(p.UsedDays, p.RemainingDays))
	fmt.Printf("basic split used:unused = %v + %v = %v\n", basicSplit[0], basicSplit[1], mustPlus(basicSplit[0], basicSplit[1]))
	fmt.Printf("credit %v, charge %v, net due now %v\n", p.Credit, p.Charge, p.Net())
	odd := must(NewPlan("odd", mustParse("9.99", USD), MONTHLY))
	halves := must(odd.Price.Allocate(15, 15))
	fmt.Printf("9.99 split 15:15 days = %v + %v (independent half-up rounding would give 5.00 + 5.00 = 10.00)\n", halves[0], halves[1])

	fmt.Println("\n== Billing engine: trial, renewals, dunning, card updater ==")
	clock := NewFakeClock(Date(2026, 1, 31))
	gateway := NewScriptedGateway()
	updater := NewMapCardUpdater()
	engine := NewBillingEngine(clock, gateway, updater, StandardDunningPolicy())

	gateway.Script("tok_flaky", APPROVED, INSUFFICIENT_FUNDS, INSUFFICIENT_FUNDS, APPROVED)
	gateway.Script("tok_old", APPROVED, EXPIRED_CARD)
	updater.Replace("tok_old", "tok_reissued")
	gateway.Script("tok_broke", INSUFFICIENT_FUNDS, INSUFFICIENT_FUNDS, INSUFFICIENT_FUNDS, INSUFFICIENT_FUNDS, INSUFFICIENT_FUNDS)

	must(engine.Subscribe("sub_flaky", "cus_a", BASIC, "tok_flaky", 0))
	must(engine.Subscribe("sub_updater", "cus_b", BASIC, "tok_old", 0))
	must(engine.Subscribe("sub_trial", "cus_c", PRO, "tok_broke", 14))
	for day := 0; day < 75; day++ {
		engine.RunDay()
		engine.RunDay() // a second run on the same day must be a no-op
		clock.AdvanceDays(1)
	}
	for _, line := range engine.Log() {
		fmt.Println(line)
	}

	fmt.Println("\n== Final state ==")
	for _, id := range []string{"sub_flaky", "sub_updater", "sub_trial"} {
		s := engine.Subscription(id)
		fmt.Printf("%-12s %-9v card=%-13s next bill %s\n", id, s.Status, s.CardToken, fmtDate(s.CurrentPeriodEnd))
	}
	fmt.Printf("gateway calls = %d, approved = %d\n", len(gateway.Charges()), gateway.ApprovedCount())
}
