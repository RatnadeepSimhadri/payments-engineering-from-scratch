// Lesson: phases/11-payment-rails/02-card-authorization-lifecycle/docs/en.md
// Authorization, Capture, Void, Refund: The Card Lifecycle State Machine
// Run:  go run .
// Test: go test ./...
// Sources: ISO 8583:1987 message classes (01xx authorization, 02xx financial, 04xx reversal); Visa Core Rules and
// Mastercard Transaction Processing Rules on authorization validity, incremental and estimated authorizations.
// Go 1.22+, standard library only. Same design as the Java and Python versions.
package main

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------------------------------------------
// Money, copied from Phase 10 Lesson 01 and trimmed to what this lesson needs.
// ---------------------------------------------------------------------------------------------------------------

// Currency is an ISO 4217 currency: a three-letter code and the number of decimals in its minor unit.
type Currency struct {
	Code     string
	Exponent int
}

var (
	USD = Currency{"USD", 2}
	EUR = Currency{"EUR", 2}
	JPY = Currency{"JPY", 0}
)

// MinorUnitsPerMajor is 10^exponent: 100 for USD, 1 for JPY.
func (c Currency) MinorUnitsPerMajor() int64 {
	scale := int64(1)
	for i := 0; i < c.Exponent; i++ {
		scale *= 10
	}
	return scale
}

// Rounding says how to resolve a fractional minor unit.
type Rounding int

const (
	HalfEven Rounding = iota
	HalfUp
	Down
	Up
)

// ErrCurrencyMismatch is returned when two amounts of different currencies are combined.
var ErrCurrencyMismatch = errors.New("currency mismatch")

// ErrOverflow is returned when an int64 would wrap. Money never silently wraps.
var ErrOverflow = errors.New("int64 overflow")

// Money is an exact amount of one currency stored as a signed count of minor units.
type Money struct {
	MinorUnits int64
	Currency   Currency
}

func OfMinor(minorUnits int64, currency Currency) Money { return Money{minorUnits, currency} }

// Parse reads "12.34", "-0.5", "1200" exactly and rejects more decimals than the currency allows.
func Parse(text string, currency Currency) (Money, error) {
	s := strings.TrimSpace(text)
	negative := strings.HasPrefix(s, "-")
	if negative || strings.HasPrefix(s, "+") {
		s = s[1:]
	}
	if s == "" {
		return Money{}, errors.New("empty amount")
	}
	whole, frac, _ := strings.Cut(s, ".")
	if whole == "" {
		whole = "0"
	}
	if !allDigits(whole) || (frac != "" && !allDigits(frac)) {
		return Money{}, fmt.Errorf("not a decimal amount: %q", text)
	}
	if len(frac) > currency.Exponent {
		return Money{}, fmt.Errorf("%s has more decimals than %s allows (%d)", text, currency.Code, currency.Exponent)
	}
	frac += strings.Repeat("0", currency.Exponent-len(frac))
	var minor int64
	for _, ch := range whole + frac {
		next := minor*10 + int64(ch-'0')
		if next < minor {
			return Money{}, ErrOverflow
		}
		minor = next
	}
	if negative {
		minor = -minor
	}
	return Money{minor, currency}, nil
}

func allDigits(s string) bool {
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	return len(s) > 0
}

func (m Money) requireSameCurrency(other Money) error {
	if m.Currency != other.Currency {
		return fmt.Errorf("%w: cannot combine %s with %s", ErrCurrencyMismatch, m.Currency.Code, other.Currency.Code)
	}
	return nil
}

func addChecked(a, b int64) (int64, error) {
	sum := a + b
	if (a > 0 && b > 0 && sum < 0) || (a < 0 && b < 0 && sum >= 0) {
		return 0, ErrOverflow
	}
	return sum, nil
}

func mulChecked(a, b int64) (int64, error) {
	if a == 0 || b == 0 {
		return 0, nil
	}
	product := a * b
	if product/b != a || (a == -1 && b == math.MinInt64) || (b == -1 && a == math.MinInt64) {
		return 0, ErrOverflow
	}
	return product, nil
}

func (m Money) Plus(other Money) (Money, error) {
	if err := m.requireSameCurrency(other); err != nil {
		return Money{}, err
	}
	sum, err := addChecked(m.MinorUnits, other.MinorUnits)
	return Money{sum, m.Currency}, err
}

func (m Money) Minus(other Money) (Money, error) {
	if err := m.requireSameCurrency(other); err != nil {
		return Money{}, err
	}
	diff, err := addChecked(m.MinorUnits, -other.MinorUnits)
	return Money{diff, m.Currency}, err
}

func (m Money) IsZero() bool     { return m.MinorUnits == 0 }
func (m Money) IsPositive() bool { return m.MinorUnits > 0 }

// DivideRounded is integer division with an explicit rounding rule (Go's / truncates like Java's).
func DivideRounded(dividend, divisor int64, rounding Rounding) int64 {
	quotient := dividend / divisor
	remainder := dividend % divisor
	if remainder == 0 {
		return quotient
	}
	sign := int64(1)
	if (dividend < 0) != (divisor < 0) {
		sign = -1
	}
	twice := abs64(remainder) * 2
	absDivisor := abs64(divisor)
	switch rounding {
	case Down:
		return quotient
	case Up:
		return quotient + sign
	case HalfUp:
		if twice >= absDivisor {
			return quotient + sign
		}
		return quotient
	default: // HalfEven
		if twice > absDivisor {
			return quotient + sign
		}
		if twice < absDivisor {
			return quotient
		}
		if quotient%2 == 0 {
			return quotient
		}
		return quotient + sign
	}
}

func abs64(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}

// Times multiplies by an exact rational and rounds once; used for the over-capture ceiling.
func (m Money) Times(numerator, denominator int64, rounding Rounding) (Money, error) {
	if denominator <= 0 {
		return Money{}, errors.New("denominator must be positive")
	}
	product, err := mulChecked(m.MinorUnits, numerator)
	if err != nil {
		return Money{}, err
	}
	return Money{DivideRounded(product, denominator, rounding), m.Currency}, nil
}

// Compare returns -1, 0, or 1; it panics on a currency mismatch because that is a programming error.
func (m Money) Compare(other Money) int {
	if err := m.requireSameCurrency(other); err != nil {
		panic(err)
	}
	switch {
	case m.MinorUnits < other.MinorUnits:
		return -1
	case m.MinorUnits > other.MinorUnits:
		return 1
	}
	return 0
}

// String renders "12.34 USD", "-0.50 EUR", "1200 JPY".
func (m Money) String() string {
	magnitude := abs64(m.MinorUnits)
	scale := m.Currency.MinorUnitsPerMajor()
	text := fmt.Sprintf("%d", magnitude/scale)
	if m.Currency.Exponent > 0 {
		text += fmt.Sprintf(".%0*d", m.Currency.Exponent, magnitude%scale)
	}
	if m.MinorUnits < 0 {
		text = "-" + text
	}
	return text + " " + m.Currency.Code
}

// ---------------------------------------------------------------------------------------------------------------
// Clock
// ---------------------------------------------------------------------------------------------------------------

// Clock is the injected time source. Production uses time.Now; tests and the demo use FakeClock.
type Clock interface {
	Now() time.Time
}

// FakeClock is a clock that only moves when the test tells it to.
type FakeClock struct {
	current time.Time
}

func NewFakeClock(start time.Time) *FakeClock { return &FakeClock{current: start} }
func (c *FakeClock) Now() time.Time           { return c.current }
func (c *FakeClock) Advance(d time.Duration)  { c.current = c.current.Add(d) }

// ---------------------------------------------------------------------------------------------------------------
// The state machine
// ---------------------------------------------------------------------------------------------------------------

// TransactionState is one of the seven states a card transaction can be in. Three are terminal.
type TransactionState int

const (
	Authorized TransactionState = iota
	PartiallyCaptured
	Captured
	Voided
	Expired
	PartiallyRefunded
	Refunded
)

var stateNames = [...]string{"AUTHORIZED", "PARTIALLY_CAPTURED", "CAPTURED", "VOIDED", "EXPIRED", "PARTIALLY_REFUNDED", "REFUNDED"}

func (s TransactionState) String() string { return stateNames[s] }

func (s TransactionState) IsTerminal() bool { return s == Voided || s == Expired || s == Refunded }

// Action is everything that can happen to a transaction. Authorize creates it; the other six are transitions.
type Action int

const (
	ActAuthorize Action = iota
	ActIncrement
	ActCapture
	ActCloseCapture
	ActVoid
	ActExpire
	ActRefund
)

var actionNames = [...]string{"AUTHORIZE", "INCREMENT", "CAPTURE", "CLOSE_CAPTURE", "VOID", "EXPIRE", "REFUND"}

func (a Action) String() string { return actionNames[a] }

// IllegalTransitionError reports a transition attempted from a state that does not allow it.
type IllegalTransitionError struct {
	From   TransactionState
	Action Action
}

func (e *IllegalTransitionError) Error() string {
	return fmt.Sprintf("%s is not allowed in state %s", e.Action, e.From)
}

// ErrIllegalTransition lets callers use errors.Is without caring about the from/action details.
var ErrIllegalTransition = errors.New("illegal transition")

func (e *IllegalTransitionError) Is(target error) bool { return target == ErrIllegalTransition }

// ErrLimitExceeded is returned when an amount rule is broken: capture above the ceiling, refund above captured,
// non-positive amount.
var ErrLimitExceeded = errors.New("limit exceeded")

// LifecyclePolicy holds per-merchant settings: how long an authorization stays valid and how far (in basis
// points) a capture may exceed it. Network tolerances are scheme-specific and change, so they live in
// configuration, not code.
type LifecyclePolicy struct {
	AuthValidity            time.Duration
	OverCaptureToleranceBps int64
}

// Standard is seven days of validity and no over-capture tolerance.
var Standard = LifecyclePolicy{AuthValidity: 7 * 24 * time.Hour, OverCaptureToleranceBps: 0}

// NewLifecyclePolicy validates the two settings.
func NewLifecyclePolicy(validity time.Duration, toleranceBps int64) (LifecyclePolicy, error) {
	if validity <= 0 {
		return LifecyclePolicy{}, errors.New("validity must be positive")
	}
	if toleranceBps < 0 || toleranceBps > 10_000 {
		return LifecyclePolicy{}, errors.New("tolerance must be 0..10000 bps")
	}
	return LifecyclePolicy{validity, toleranceBps}, nil
}

// PolicyForMCC is an illustrative mapping: gratuity-heavy categories get a 15% tolerance; everyone else none.
func PolicyForMCC(mcc string) LifecyclePolicy {
	switch mcc {
	case "5812", "5813", "5814", "4121", "7230":
		return LifecyclePolicy{7 * 24 * time.Hour, 1500}
	}
	return Standard
}

// CaptureCeiling is the most that may be captured in total, rounded toward the merchant's disadvantage.
func (p LifecyclePolicy) CaptureCeiling(authorized Money) Money {
	ceiling, err := authorized.Times(10_000+p.OverCaptureToleranceBps, 10_000, Down)
	if err != nil {
		panic(err)
	}
	return ceiling
}

// TransactionEvent is one persisted transition. The log of these is the transaction; state is derived from it.
type TransactionEvent struct {
	Sequence int
	Action   Action
	Amount   Money
	From     *TransactionState // nil for the creating AUTHORIZE event
	To       TransactionState
	At       time.Time
	Reason   string
}

func (e TransactionEvent) String() string {
	origin := "(new)"
	if e.From != nil {
		origin = e.From.String()
	}
	return fmt.Sprintf("#%d %-13s %12s  %s -> %s  @%s  %q", e.Sequence, e.Action, e.Amount, origin, e.To,
		e.At.Format(time.RFC3339), e.Reason)
}

// Legal is the legal-move table: which actions each state accepts. Everything not listed is an error.
var Legal = map[TransactionState]map[Action]bool{
	Authorized:        {ActIncrement: true, ActCapture: true, ActVoid: true, ActExpire: true},
	PartiallyCaptured: {ActCapture: true, ActCloseCapture: true, ActExpire: true},
	Captured:          {ActRefund: true},
	PartiallyRefunded: {ActRefund: true},
	Voided:            {},
	Expired:           {},
	Refunded:          {},
}

// CardTransaction is the lifecycle of one card authorization. Every mutation goes through transition, which
// runs only after the legal-move table and the amount rule have both passed, so a failed call leaves nothing.
type CardTransaction struct {
	id         string
	clock      Clock
	policy     LifecyclePolicy
	events     []TransactionEvent
	state      TransactionState
	authorized Money
	captured   Money
	refunded   Money
	expiresAt  time.Time
}

// Authorize creates a transaction in state AUTHORIZED with its first event.
func Authorize(id string, amount Money, clock Clock, policy LifecyclePolicy, reason string) (*CardTransaction, error) {
	if !amount.IsPositive() {
		return nil, fmt.Errorf("%w: authorization amount must be positive: %v", ErrLimitExceeded, amount)
	}
	if strings.TrimSpace(reason) == "" {
		return nil, errors.New("every transition needs a reason")
	}
	now := clock.Now()
	t := &CardTransaction{
		id:         id,
		clock:      clock,
		policy:     policy,
		state:      Authorized,
		authorized: amount,
		captured:   OfMinor(0, amount.Currency),
		refunded:   OfMinor(0, amount.Currency),
		expiresAt:  now.Add(policy.AuthValidity),
	}
	t.events = append(t.events, TransactionEvent{1, ActAuthorize, amount, nil, Authorized, now, reason})
	return t, nil
}

// ---- read side ----

func (t *CardTransaction) ID() string              { return t.id }
func (t *CardTransaction) State() TransactionState { return t.state }
func (t *CardTransaction) AuthorizedAmount() Money { return t.authorized }
func (t *CardTransaction) CapturedAmount() Money   { return t.captured }
func (t *CardTransaction) RefundedAmount() Money   { return t.refunded }
func (t *CardTransaction) ExpiresAt() time.Time    { return t.expiresAt }

// Events returns a copy of the log so callers cannot mutate it.
func (t *CardTransaction) Events() []TransactionEvent {
	out := make([]TransactionEvent, len(t.events))
	copy(out, t.events)
	return out
}

// RemainingAuthorization is authorized minus captured while the hold is open; zero once it is closed.
func (t *CardTransaction) RemainingAuthorization() Money {
	zero := OfMinor(0, t.authorized.Currency)
	if t.state != Authorized && t.state != PartiallyCaptured {
		return zero
	}
	rest, _ := t.authorized.Minus(t.captured)
	if rest.IsPositive() {
		return rest
	}
	return zero
}

// NetSettled is what the merchant will actually be paid: captured minus refunded.
func (t *CardTransaction) NetSettled() Money {
	net, _ := t.captured.Minus(t.refunded)
	return net
}

// ---- write side ----

// IncrementAuthorization raises the authorized amount while the hold is still open; resets the expiry window.
func (t *CardTransaction) IncrementAuthorization(additional Money, reason string) error {
	t.ExpireIfDue(reason)
	if err := t.require(ActIncrement); err != nil {
		return err
	}
	if err := t.requirePositive(additional); err != nil {
		return err
	}
	sum, err := t.authorized.Plus(additional)
	if err != nil {
		return err
	}
	t.authorized = sum
	t.expiresAt = t.clock.Now().Add(t.policy.AuthValidity)
	return t.transition(ActIncrement, additional, Authorized, reason)
}

// Capture takes part or all of the authorization. Cumulative captures may not exceed the policy's ceiling.
func (t *CardTransaction) Capture(amount Money, reason string) error {
	t.ExpireIfDue(reason)
	if err := t.require(ActCapture); err != nil {
		return err
	}
	if err := t.requirePositive(amount); err != nil {
		return err
	}
	ceiling := t.policy.CaptureCeiling(t.authorized)
	total, err := t.captured.Plus(amount)
	if err != nil {
		return err
	}
	if total.Compare(ceiling) > 0 {
		return fmt.Errorf("%w: capture of %v would bring total to %v, above ceiling %v (authorized %v, tolerance %d bps)",
			ErrLimitExceeded, amount, total, ceiling, t.authorized, t.policy.OverCaptureToleranceBps)
	}
	t.captured = total
	next := PartiallyCaptured
	if t.captured.Compare(t.authorized) >= 0 {
		next = Captured
	}
	return t.transition(ActCapture, amount, next, reason)
}

// CloseCapture declares a partially captured transaction complete: the uncaptured remainder is released.
func (t *CardTransaction) CloseCapture(reason string) error {
	t.ExpireIfDue(reason)
	if err := t.require(ActCloseCapture); err != nil {
		return err
	}
	released := t.RemainingAuthorization()
	return t.transition(ActCloseCapture, released, Captured, reason)
}

// VoidAuthorization releases the whole hold before anything is captured.
func (t *CardTransaction) VoidAuthorization(reason string) error {
	t.ExpireIfDue(reason)
	if err := t.require(ActVoid); err != nil {
		return err
	}
	return t.transition(ActVoid, t.authorized, Voided, reason)
}

// Refund returns captured money to the cardholder. Cumulative refunds may not exceed what was captured.
func (t *CardTransaction) Refund(amount Money, reason string) error {
	if err := t.require(ActRefund); err != nil {
		return err
	}
	if err := t.requirePositive(amount); err != nil {
		return err
	}
	total, err := t.refunded.Plus(amount)
	if err != nil {
		return err
	}
	if total.Compare(t.captured) > 0 {
		return fmt.Errorf("%w: refund of %v would bring total refunded to %v, above captured %v",
			ErrLimitExceeded, amount, total, t.captured)
	}
	t.refunded = total
	next := PartiallyRefunded
	if t.refunded.Compare(t.captured) == 0 {
		next = Refunded
	}
	return t.transition(ActRefund, amount, next, reason)
}

// ExpireIfDue is the clock-driven transition. It records EXPIRE and returns true if the window has passed and
// the state still allows it. Called before every merchant-initiated action.
func (t *CardTransaction) ExpireIfDue(attemptedReason string) bool {
	if !Legal[t.state][ActExpire] {
		return false
	}
	if t.clock.Now().Before(t.expiresAt) {
		return false
	}
	days := int64(t.policy.AuthValidity / (24 * time.Hour))
	_ = t.transition(ActExpire, t.RemainingAuthorization(), Expired,
		fmt.Sprintf("authorization validity of %d days elapsed (while attempting: %s)", days, attemptedReason))
	return true
}

// ---- the single choke point ----

func (t *CardTransaction) require(action Action) error {
	if !Legal[t.state][action] {
		return &IllegalTransitionError{From: t.state, Action: action}
	}
	return nil
}

func (t *CardTransaction) requirePositive(amount Money) error {
	if amount.Currency != t.authorized.Currency {
		return fmt.Errorf("%w: cannot combine %s with %s", ErrCurrencyMismatch, t.authorized.Currency.Code, amount.Currency.Code)
	}
	if !amount.IsPositive() {
		return fmt.Errorf("%w: amount must be positive: %v", ErrLimitExceeded, amount)
	}
	return nil
}

func (t *CardTransaction) transition(action Action, amount Money, to TransactionState, reason string) error {
	if strings.TrimSpace(reason) == "" {
		return errors.New("every transition needs a reason")
	}
	from := t.state
	t.events = append(t.events, TransactionEvent{len(t.events) + 1, action, amount, &from, to, t.clock.Now(), reason})
	t.state = to
	return nil
}

// PrintEvents writes the transaction summary and its log to stdout.
func (t *CardTransaction) PrintEvents() {
	fmt.Printf("transaction %s [%s] authorized=%v captured=%v refunded=%v net=%v\n", t.id, t.state, t.authorized,
		t.captured, t.refunded, t.NetSettled())
	for _, e := range t.events {
		fmt.Printf("  %s\n", e)
	}
}

func must(m Money, err error) Money {
	if err != nil {
		panic(err)
	}
	return m
}

func check(err error) {
	if err != nil {
		panic(err)
	}
}

func main() {
	clock := NewFakeClock(time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC))

	fmt.Println("== Hotel stay: estimated auth, incremental auth, partial capture, refund ==")
	hotel, err := Authorize("txn-hotel-001", must(Parse("120.00", USD)), clock, Standard,
		"check-in: estimated one night plus incidentals")
	check(err)
	clock.Advance(24 * time.Hour)
	check(hotel.IncrementAuthorization(must(Parse("30.00", USD)), "minibar and late checkout"))
	clock.Advance(6 * time.Hour)
	check(hotel.Capture(must(Parse("100.00", USD)), "folio: room night 100.00"))
	check(hotel.CloseCapture("checkout complete; release the unused 50.00 hold"))
	clock.Advance(3 * 24 * time.Hour)
	check(hotel.Refund(must(Parse("20.00", USD)), "goodwill: noisy room complaint"))
	hotel.PrintEvents()
	fmt.Printf("  remaining authorization: %v\n", hotel.RemainingAuthorization())

	fmt.Println("\n== Restaurant (MCC 5812): tip pushes the capture 12% above the authorization ==")
	tips := PolicyForMCC("5812")
	dinner, err := Authorize("txn-dinner-002", must(Parse("80.00", USD)), clock, tips, "pre-tip bill presented")
	check(err)
	fmt.Printf("  capture ceiling with 15%% tolerance: %v\n", tips.CaptureCeiling(dinner.AuthorizedAmount()))
	check(dinner.Capture(must(Parse("89.60", USD)), "bill 80.00 plus 9.60 tip"))
	dinner.PrintEvents()
	if err := dinner.Refund(must(Parse("100.00", USD)), "customer asked for more than they paid"); err != nil {
		fmt.Println("  rejected:", err)
	}

	fmt.Println("\n== Same merchant without the tolerance flag ==")
	strict, err := Authorize("txn-strict-003", must(Parse("80.00", USD)), clock, Standard, "pre-tip bill presented")
	check(err)
	if err := strict.Capture(must(Parse("89.60", USD)), "bill 80.00 plus 9.60 tip"); err != nil {
		fmt.Println("  rejected:", err)
	}

	fmt.Println("\n== Forgotten authorization: capture attempted on day 8 ==")
	stale, err := Authorize("txn-stale-004", must(Parse("45.00", USD)), clock, Standard, "online order placed")
	check(err)
	clock.Advance(8 * 24 * time.Hour)
	if err := stale.Capture(must(Parse("45.00", USD)), "order shipped"); err != nil {
		fmt.Println("  rejected:", err)
	}
	stale.PrintEvents()

	fmt.Println("\n== Illegal moves are errors, not silent no-ops ==")
	if err := hotel.VoidAuthorization("oops"); err != nil {
		fmt.Println("  rejected:", err)
	}
	if err := hotel.Capture(must(Parse("1.00", USD)), "one more"); err != nil {
		fmt.Println("  rejected:", err)
	}
}
