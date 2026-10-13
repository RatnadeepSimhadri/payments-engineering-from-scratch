// Lesson: phases/10-money-and-ledgers/07-holds-and-available-balance/docs/en.md
// Holds, Pending Transactions, and Available Balance
// Run:  go run .
// Test: go test ./...
// Sources: Visa Core Rules (authorization hold / incremental authorization / reversal); Regulation E 12 CFR 1005.
// Money type copied from Phase 10, Lesson 01. Go 1.22+, standard library only. Same design as Java and Python.
package main

import (
	"errors"
	"fmt"
	"strings"
	"sync"
)

// ---------------------------------------------------------------------------------------------
// Money (copied from Phase 10, Lesson 01). Integer minor units + ISO 4217 currency. Never a float.
// ---------------------------------------------------------------------------------------------

type Currency struct {
	Code     string
	Exponent int
}

var (
	USD = Currency{"USD", 2}
	EUR = Currency{"EUR", 2}
	JPY = Currency{"JPY", 0}
)

func (c Currency) MinorUnitsPerMajor() int64 {
	scale := int64(1)
	for i := 0; i < c.Exponent; i++ {
		scale *= 10
	}
	return scale
}

var ErrCurrencyMismatch = errors.New("currency mismatch")
var ErrOverflow = errors.New("int64 overflow")

type Money struct {
	MinorUnits int64
	Currency   Currency
}

func OfMinor(minorUnits int64, currency Currency) Money { return Money{minorUnits, currency} }
func Zero(currency Currency) Money                      { return Money{0, currency} }

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

func (m Money) Negate() Money    { return Money{-m.MinorUnits, m.Currency} }
func (m Money) IsZero() bool     { return m.MinorUnits == 0 }
func (m Money) IsNegative() bool { return m.MinorUnits < 0 }
func (m Money) IsPositive() bool { return m.MinorUnits > 0 }

// Compare panics on a currency mismatch: a comparison cannot return an error and the mismatch is a bug.
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

func abs64(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}

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

func must(m Money, err error) Money {
	if err != nil {
		panic(err)
	}
	return m
}

// ---------------------------------------------------------------------------------------------
// Time is injected. Nothing in this lesson reads the wall clock.
// ---------------------------------------------------------------------------------------------

// Clock is a source of the current time in whole seconds since the epoch.
type Clock interface {
	Now() int64
}

// ManualClock is a clock the test moves by hand. Expiry is a function of this value and nothing else.
// It is read from many goroutines in the concurrency test, so reads and writes take a mutex.
type ManualClock struct {
	mu  sync.Mutex
	now int64
}

func NewManualClock(start int64) *ManualClock { return &ManualClock{now: start} }

func (c *ManualClock) Now() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *ManualClock) Advance(seconds int64) error {
	if seconds < 0 {
		return errors.New("clocks do not run backwards in this lesson")
	}
	c.mu.Lock()
	c.now += seconds
	c.mu.Unlock()
	return nil
}

// ---------------------------------------------------------------------------------------------
// Holds.
// ---------------------------------------------------------------------------------------------

type HoldStatus int

const (
	Active HoldStatus = iota
	Captured
	Voided
	Expired
)

func (s HoldStatus) String() string {
	return [...]string{"ACTIVE", "CAPTURED", "VOIDED", "EXPIRED"}[s]
}

// Hold is an authorization hold: Amount is reserved until ExpiresAt. It is not a ledger posting.
type Hold struct {
	ID        string
	Amount    Money
	CreatedAt int64
	ExpiresAt int64
	Status    HoldStatus
	Captured  Money
}

func newHold(id string, amount Money, createdAt, expiresAt int64) (*Hold, error) {
	if strings.TrimSpace(id) == "" {
		return nil, errors.New("hold id required")
	}
	if !amount.IsPositive() {
		return nil, fmt.Errorf("hold amount must be positive, got %s", amount)
	}
	if expiresAt <= createdAt {
		return nil, errors.New("hold must expire after it is created")
	}
	return &Hold{ID: id, Amount: amount, CreatedAt: createdAt, ExpiresAt: expiresAt, Status: Active, Captured: Zero(amount.Currency)}, nil
}

func (h *Hold) IsActive() bool { return h.Status == Active }

// IsStale is true when the clock has passed the expiry and the hold is still marked active.
func (h *Hold) IsStale(now int64) bool { return h.Status == Active && now >= h.ExpiresAt }

func (h *Hold) String() string {
	tail := ""
	if h.Status == Captured {
		tail = " captured=" + h.Captured.String()
	}
	return fmt.Sprintf("%s %s %s%s", h.ID, h.Amount, h.Status, tail)
}

// Sentinel errors: callers use errors.Is.
var (
	ErrInsufficientAvailable = errors.New("insufficient available balance")
	ErrOverCapture           = errors.New("capture exceeds hold")
	ErrHoldNotActive         = errors.New("hold not active")
	ErrUnknownHold           = errors.New("unknown hold")
	ErrDuplicateHold         = errors.New("hold id already used")
	ErrInvalidAmount         = errors.New("amount must be positive")
)

// Account is one account with a settled (ledger) balance and a set of holds.
//
// Invariant proved by the tests:
//
//	available == ledger - sum(amount of ACTIVE holds)   and   available >= 0
//
// Every exported method expires stale holds against the injected clock before it reads or writes.
//
// Every exported method takes mu. The check "amount <= available" and the insertion of the hold
// must be one atomic step; two goroutines that both read available = 100.00 and both insert a
// 100.00 hold would leave available at -100.00. sync.Mutex is not reentrant, so the exported
// methods lock once and call the unexported *Locked helpers, which assume the lock is held.
type Account struct {
	ID       string
	Currency Currency
	mu       sync.Mutex
	clock    Clock
	ledger   Money
	holds    map[string]*Hold
	order    []string // insertion order, so iteration is deterministic
	events   []string
}

func NewAccount(id string, currency Currency, clock Clock) *Account {
	return &Account{ID: id, Currency: currency, clock: clock, ledger: Zero(currency), holds: map[string]*Hold{}}
}

// ExpireStale marks every ACTIVE hold whose expiry has passed as EXPIRED. Returns how many it released.
func (a *Account) ExpireStale() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.expireStaleLocked()
}

func (a *Account) expireStaleLocked() int {
	now := a.clock.Now()
	released := 0
	for _, id := range a.order {
		h := a.holds[id]
		if h.IsStale(now) {
			h.Status = Expired
			released++
			a.events = append(a.events, fmt.Sprintf("t=%d EXPIRED %s released %s", now, h.ID, h.Amount))
		}
	}
	return released
}

// LedgerBalance is the settled balance. Holds never change this number.
func (a *Account) LedgerBalance() Money {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.ledger
}

// HeldTotal is the sum of ACTIVE holds after sweeping expiries.
func (a *Account) HeldTotal() Money {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.heldTotalLocked()
}

func (a *Account) heldTotalLocked() Money {
	a.expireStaleLocked()
	total := Zero(a.Currency)
	for _, id := range a.order {
		if h := a.holds[id]; h.IsActive() {
			total = must(total.Plus(h.Amount))
		}
	}
	return total
}

// AvailableBalance is what may still be promised to someone else.
func (a *Account) AvailableBalance() Money {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.availableLocked()
}

func (a *Account) availableLocked() Money { return must(a.ledger.Minus(a.heldTotalLocked())) }

func (a *Account) ActiveHolds() []*Hold {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.expireStaleLocked()
	var active []*Hold
	for _, id := range a.order {
		if h := a.holds[id]; h.IsActive() {
			active = append(active, h)
		}
	}
	return active
}

func (a *Account) Hold(id string) (*Hold, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.holdLocked(id)
}

func (a *Account) holdLocked(id string) (*Hold, error) {
	h, ok := a.holds[id]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknownHold, id)
	}
	return h, nil
}

func (a *Account) Events() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.events...)
}

// Credit is a settled credit (deposit, incoming transfer, refund). Raises both ledger and available.
func (a *Account) Credit(amount Money) error {
	if err := a.requirePositive(amount); err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.ledger = must(a.ledger.Plus(amount))
	a.events = append(a.events, fmt.Sprintf("t=%d CREDIT %s", a.clock.Now(), amount))
	return nil
}

// Debit is a settled debit that was never authorized (a fee, a direct debit). Must fit inside available.
func (a *Account) Debit(amount Money) error {
	if err := a.requirePositive(amount); err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	available := a.availableLocked()
	if amount.Compare(available) > 0 {
		return fmt.Errorf("%w: requested %s but only %s is available", ErrInsufficientAvailable, amount, available)
	}
	a.ledger = must(a.ledger.Minus(amount))
	a.events = append(a.events, fmt.Sprintf("t=%d DEBIT %s", a.clock.Now(), amount))
	return nil
}

// Authorize places a hold. Rejected if it would push available below zero. The ledger does not move.
// The availability check and the insertion happen under one lock acquisition: that is the whole point.
func (a *Account) Authorize(id string, amount Money, ttlSeconds int64) (*Hold, error) {
	if err := a.requirePositive(amount); err != nil {
		return nil, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, exists := a.holds[id]; exists {
		return nil, fmt.Errorf("%w: %s", ErrDuplicateHold, id)
	}
	available := a.availableLocked()
	if amount.Compare(available) > 0 {
		return nil, fmt.Errorf("%w: requested %s but only %s is available", ErrInsufficientAvailable, amount, available)
	}
	now := a.clock.Now()
	h, err := newHold(id, amount, now, now+ttlSeconds)
	if err != nil {
		return nil, err
	}
	a.holds[id] = h
	a.order = append(a.order, id)
	a.events = append(a.events, fmt.Sprintf("t=%d AUTHORIZE %s %s until t=%d", now, id, amount, h.ExpiresAt))
	return h, nil
}

// Increment is hold stacking by incremental authorization: the same hold grows, subject to the same check.
func (a *Account) Increment(id string, extra Money) (*Hold, error) {
	if err := a.requirePositive(extra); err != nil {
		return nil, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	h, err := a.requireActiveLocked(id)
	if err != nil {
		return nil, err
	}
	available := a.availableLocked()
	if extra.Compare(available) > 0 {
		return nil, fmt.Errorf("%w: requested %s but only %s is available", ErrInsufficientAvailable, extra, available)
	}
	h.Amount = must(h.Amount.Plus(extra))
	a.events = append(a.events, fmt.Sprintf("t=%d INCREMENT %s +%s -> %s", a.clock.Now(), id, extra, h.Amount))
	return h, nil
}

// Capture moves the ledger by exactly amount; the rest of the hold is released in the same step.
func (a *Account) Capture(id string, amount Money) (Money, error) {
	if err := a.requirePositive(amount); err != nil {
		return Money{}, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	h, err := a.requireActiveLocked(id)
	if err != nil {
		return Money{}, err
	}
	if amount.Compare(h.Amount) > 0 {
		return Money{}, fmt.Errorf("%w: hold %s: cannot capture %s against a hold of %s", ErrOverCapture, id, amount, h.Amount)
	}
	a.ledger = must(a.ledger.Minus(amount))
	h.Captured = amount
	h.Status = Captured
	released := must(h.Amount.Minus(amount))
	a.events = append(a.events, fmt.Sprintf("t=%d CAPTURE %s %s released %s", a.clock.Now(), id, amount, released))
	return released, nil
}

// VoidHold is an authorization reversal: the hold is cancelled and its whole amount returns to available.
func (a *Account) VoidHold(id string) (Money, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	h, err := a.requireActiveLocked(id)
	if err != nil {
		return Money{}, err
	}
	h.Status = Voided
	a.events = append(a.events, fmt.Sprintf("t=%d VOID %s released %s", a.clock.Now(), id, h.Amount))
	return h.Amount, nil
}

// CheckInvariant recomputes the invariant from scratch. Tests call this after every operation.
func (a *Account) CheckInvariant() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.expireStaleLocked()
	total := Zero(a.Currency)
	for _, id := range a.order {
		if h := a.holds[id]; h.Status == Active {
			total = must(total.Plus(h.Amount))
		}
	}
	available := a.availableLocked()
	return available == must(a.ledger.Minus(total)) && !available.IsNegative()
}

func (a *Account) requireActiveLocked(id string) (*Hold, error) {
	a.expireStaleLocked()
	h, err := a.holdLocked(id)
	if err != nil {
		return nil, err
	}
	if !h.IsActive() {
		return nil, fmt.Errorf("%w: hold %s is %s", ErrHoldNotActive, id, h.Status)
	}
	return h, nil
}

// requirePositive reads only immutable fields, so it needs no lock.
func (a *Account) requirePositive(amount Money) error {
	if amount.Currency != a.Currency {
		return fmt.Errorf("%w: cannot combine %s with %s", ErrCurrencyMismatch, a.Currency.Code, amount.Currency.Code)
	}
	if !amount.IsPositive() {
		return fmt.Errorf("%w: got %s", ErrInvalidAmount, amount)
	}
	return nil
}

// RaceAuthorizations starts `goroutines` goroutines that each try to place one hold of `amount` at
// the same instant and returns (accepted, rejected). With 500.00 available and 50 x 20.00 requested,
// exactly 25 succeed; the mutex in Account is what makes that "exactly", not "about".
func RaceAuthorizations(account *Account, goroutines int, amount Money) (accepted, rejected int) {
	var wg sync.WaitGroup
	var tally sync.Mutex
	start := make(chan struct{})
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			<-start
			_, err := account.Authorize(id, amount, 3600)
			tally.Lock()
			defer tally.Unlock()
			switch {
			case err == nil:
				accepted++
			case errors.Is(err, ErrInsufficientAvailable):
				rejected++
			}
		}(fmt.Sprintf("race-%d", i))
	}
	close(start)
	wg.Wait()
	return accepted, rejected
}

const (
	Hour int64 = 3600
	Day        = 24 * Hour
)

func show(label string, a *Account) {
	fmt.Printf("%-44s ledger=%-11s held=%-11s available=%-11s invariant=%v\n",
		label, a.LedgerBalance(), a.HeldTotal(), a.AvailableBalance(), a.CheckInvariant())
}

func usd(text string) Money { return must(Parse(text, USD)) }

func main() {
	clock := NewManualClock(1_700_000_000) // fixed epoch; never the wall clock
	card := NewAccount("debit-4242", USD, clock)

	fmt.Println("== A debit card account: payroll lands, then two authorizations ==")
	_ = card.Credit(usd("500.00"))
	show("payroll credit 500.00", card)
	_, _ = card.Authorize("auth-hotel", usd("300.00"), 7*Day)
	show("hotel authorizes 300.00 (7-day hold)", card)
	_, _ = card.Authorize("auth-fuel", usd("125.00"), 2*Hour)
	show("fuel pump pre-authorizes 125.00 (2-hour hold)", card)

	fmt.Println("\n== Available, not ledger, is what the next request sees ==")
	if err := card.Debit(usd("80.00")); err != nil {
		fmt.Println("rejected:", err, " (ledger still says 500.00)")
	}

	fmt.Println("\n== Partial capture releases the remainder in the same step ==")
	released, _ := card.Capture("auth-fuel", usd("42.17"))
	fmt.Println("fuel captures 42.17, released", released)
	show("after fuel capture", card)

	fmt.Println("\n== Expiry: the clock moves 8 days; nobody captured the hotel hold ==")
	_ = clock.Advance(8 * Day)
	show("8 days later (hotel hold expired on read)", card)
	if _, err := card.Capture("auth-hotel", usd("300.00")); err != nil {
		fmt.Println("rejected:", err)
	}

	fmt.Println("\n== Hold stacking: a new hotel authorization, then an incremental ==")
	_, _ = card.Authorize("auth-hotel-2", usd("310.00"), 7*Day)
	_, _ = card.Increment("auth-hotel-2", usd("45.00"))
	show("310.00 + 45.00 incremental", card)
	if _, err := card.Capture("auth-hotel-2", usd("400.00")); err != nil {
		fmt.Println("rejected:", err)
	}
	_, _ = card.Capture("auth-hotel-2", usd("355.00"))
	show("hotel captures the full 355.00", card)

	fmt.Println("\n== Void: an authorization reversal returns the whole amount ==")
	_, _ = card.Authorize("auth-coffee", usd("4.50"), Day)
	show("coffee authorizes 4.50", card)
	_, _ = card.VoidHold("auth-coffee")
	show("coffee voided", card)

	fmt.Println("\n== Concurrency: 50 goroutines race to authorize 20.00 against 500.00 ==")
	shared := NewAccount("shared", USD, clock)
	_ = shared.Credit(usd("500.00"))
	accepted, rejected := RaceAuthorizations(shared, 50, usd("20.00"))
	fmt.Printf("accepted=%d rejected=%d available=%s invariant=%v\n", accepted, rejected, shared.AvailableBalance(), shared.CheckInvariant())

	fmt.Println("\n== Event log ==")
	for _, e := range card.Events() {
		fmt.Println(e)
	}
	fmt.Printf("\nfinal: %s ledger, %s available, invariant=%v\n", card.LedgerBalance(), card.AvailableBalance(), card.CheckInvariant())
}
