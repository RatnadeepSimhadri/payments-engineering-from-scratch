// Lesson: phases/12-payment-systems-design/06-refunds-and-partial-refunds/docs/en.md
// Refunds and Partial Refunds
// Run:  go run .
// Test: go test ./...
// Sources: Phase 10 Lesson 03 (double-entry ledger); Stripe Refunds API reference (amount_refunded, partial refunds);
// Visa Core Rules on refund timeframes. Go 1.22+, stdlib only. Same design as the Java version.
package main

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// ErrInvalidArgument and ErrIllegalState are the Go spellings of the two Java runtime exceptions.
var (
	ErrInvalidArgument = errors.New("invalid argument")
	ErrIllegalState    = errors.New("illegal state")
)

// ---------------------------------------------------------------------------------------------
// Time is injected as a calendar date (Java's LocalDate): midnight UTC time.Time values.
// ---------------------------------------------------------------------------------------------

type Clock interface{ Today() time.Time }

func Date(year int, month time.Month, day int) time.Time {
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
}

type FakeClock struct{ today time.Time }

func NewFakeClock(start time.Time) *FakeClock { return &FakeClock{today: start} }
func (c *FakeClock) Today() time.Time         { return c.today }
func (c *FakeClock) AdvanceDays(days int)     { c.today = c.today.AddDate(0, 0, days) }

func fmtDate(t time.Time) string { return t.Format("2006-01-02") }

func daysBetween(from, to time.Time) int64 { return int64(to.Sub(from).Hours() / 24) }

// ---------------------------------------------------------------------------------------------
// A minimal double-entry ledger (Phase 10, Lesson 03): balanced entries, append-only, reversals
// instead of edits. Every refund movement below is one of these entries.
// ---------------------------------------------------------------------------------------------

type Side int

const (
	DEBIT Side = iota
	CREDIT
)

func (s Side) Opposite() Side {
	if s == DEBIT {
		return CREDIT
	}
	return DEBIT
}

type AccountType int

const (
	ASSET AccountType = iota
	LIABILITY
	REVENUE
	EXPENSE
)

// Normal is the side on which the account's balance is positive.
func (a AccountType) Normal() Side {
	if a == ASSET || a == EXPENSE {
		return DEBIT
	}
	return CREDIT
}

type Posting struct {
	Account string
	Side    Side
	Amount  Money
}

func Debit(account string, amount Money) Posting  { return Posting{account, DEBIT, amount} }
func Credit(account string, amount Money) Posting { return Posting{account, CREDIT, amount} }

func (p Posting) String() string {
	prefix := "      Cr "
	if p.Side == DEBIT {
		prefix = "  Dr "
	}
	return prefix + p.Account + "  " + p.Amount.String()
}

type JournalEntry struct {
	ID          string
	Description string
	Postings    []Posting
}

// NewJournalEntry validates what the Java compact constructor validated: positive postings, two or more, balanced.
func NewJournalEntry(id, description string, postings []Posting) (JournalEntry, error) {
	if len(postings) < 2 {
		return JournalEntry{}, fmt.Errorf("%w: entry %s needs at least two postings", ErrInvalidArgument, id)
	}
	var net int64
	for _, p := range postings {
		if !p.Amount.IsPositive() {
			return JournalEntry{}, fmt.Errorf("%w: posting amount must be positive: %v", ErrInvalidArgument, p.Amount)
		}
		if p.Side == DEBIT {
			net += p.Amount.MinorUnits
		} else {
			net -= p.Amount.MinorUnits
		}
	}
	if net != 0 {
		return JournalEntry{}, fmt.Errorf("%w: entry %s is unbalanced by %d minor units", ErrInvalidArgument, id, net)
	}
	return JournalEntry{id, description, append([]Posting(nil), postings...)}, nil
}

func (e JournalEntry) Reversal(newID, why string) JournalEntry {
	flipped := make([]Posting, len(e.Postings))
	for i, p := range e.Postings {
		flipped[i] = Posting{p.Account, p.Side.Opposite(), p.Amount}
	}
	return JournalEntry{newID, why + " (reverses " + e.ID + ")", flipped}
}

type Ledger struct {
	order    []string
	accounts map[string]AccountType
	journal  []JournalEntry
	balances map[string]int64
	currency Currency
}

func NewLedger(currency Currency) *Ledger {
	return &Ledger{accounts: map[string]AccountType{}, balances: map[string]int64{}, currency: currency}
}

func (l *Ledger) Open(account string, t AccountType) {
	if _, exists := l.accounts[account]; !exists {
		l.order = append(l.order, account)
	}
	l.accounts[account] = t
	l.balances[account] = 0
}

func (l *Ledger) Post(e JournalEntry) error {
	for _, p := range e.Postings {
		if _, ok := l.accounts[p.Account]; !ok {
			return fmt.Errorf("%w: unknown account %s", ErrInvalidArgument, p.Account)
		}
		if p.Amount.Currency != l.currency {
			return fmt.Errorf("%w: cannot combine %s with %s", ErrCurrencyMismatch, l.currency.Code, p.Amount.Currency.Code)
		}
	}
	for _, p := range e.Postings {
		signed := p.Amount.MinorUnits
		if p.Side != l.accounts[p.Account].Normal() {
			signed = -signed
		}
		if _, err := addChecked(l.balances[p.Account], signed); err != nil {
			return err
		}
	}
	l.journal = append(l.journal, e)
	for _, p := range e.Postings {
		signed := p.Amount.MinorUnits
		if p.Side != l.accounts[p.Account].Normal() {
			signed = -signed
		}
		l.balances[p.Account] += signed
	}
	return nil
}

// Balance on the account's normal side: a positive liability is money we owe.
func (l *Ledger) Balance(account string) Money { return OfMinor(l.balances[account], l.currency) }

// Balances: sum of debit-normal balances equals sum of credit-normal balances, or the ledger is corrupt.
func (l *Ledger) Balances() bool {
	var debitSide, creditSide int64
	for _, a := range l.order {
		if l.accounts[a].Normal() == DEBIT {
			debitSide += l.balances[a]
		} else {
			creditSide += l.balances[a]
		}
	}
	return debitSide == creditSide
}

func (l *Ledger) Journal() []JournalEntry { return append([]JournalEntry(nil), l.journal...) }
func (l *Ledger) Accounts() []string      { return append([]string(nil), l.order...) }

// ---------------------------------------------------------------------------------------------
// Captures and refunds.
// ---------------------------------------------------------------------------------------------

// Capture is settled money a refund can be issued against. The fee is what the processor kept on capture.
type Capture struct {
	ID         string
	PaymentID  string
	Amount     Money
	Fee        Money
	CapturedOn time.Time
}

type RefundState int

const (
	PENDING RefundState = iota
	SUCCEEDED
	FAILED
)

func (s RefundState) String() string { return [...]string{"PENDING", "SUCCEEDED", "FAILED"}[s] }

// FeeRefundPolicy: whether the processor gives back its fee when a payment is refunded. Contracts differ; the ledger must not care.
type FeeRefundPolicy int

const (
	NONE FeeRefundPolicy = iota
	PROPORTIONAL
)

type RefundPolicy struct {
	WindowDays int
	FeePolicy  FeeRefundPolicy
}

func NewRefundPolicy(windowDays int, feePolicy FeeRefundPolicy) (RefundPolicy, error) {
	if windowDays < 0 {
		return RefundPolicy{}, fmt.Errorf("%w: windowDays must be >= 0", ErrInvalidArgument)
	}
	return RefundPolicy{windowDays, feePolicy}, nil
}

type Refund struct {
	ID            string
	CaptureID     string
	Amount        Money
	Reason        string
	RequestedOn   time.Time
	State         RefundState
	FeeReturned   Money
	FailureReason string
}

func NewRefund(id, captureID string, amount Money, reason string, requestedOn time.Time) *Refund {
	return &Refund{ID: id, CaptureID: captureID, Amount: amount, Reason: reason, RequestedOn: requestedOn, State: PENDING, FeeReturned: Zero(amount.Currency)}
}

func (r *Refund) String() string {
	return fmt.Sprintf("%-8s %-10v %-12s fee back %-10s %s", r.ID, r.State, r.Amount, r.FeeReturned, r.Reason)
}

// Refund errors. ErrRefund is the base (Java's RefundException); the three sentinels below are its
// subclasses, so errors.Is(err, ErrRefund) is true for every one of them.
var (
	ErrRefund              = errors.New("refund error")
	ErrOverRefund          = errors.New("over-refund")
	ErrWindowClosed        = errors.New("refund window closed")
	ErrIdempotencyConflict = errors.New("refund idempotency conflict")
)

type refundError struct {
	kind error
	msg  string
}

func (e *refundError) Error() string { return e.msg }
func (e *refundError) Is(target error) bool {
	return target == ErrRefund || target == e.kind
}

func newRefundError(kind error, msg string) error { return &refundError{kind, msg} }

type RefundResult struct {
	Refund   *Refund
	Replayed bool
}

const (
	SCHEME   = "scheme_settlement" // asset: what the card scheme owes / has paid us
	MERCHANT = "merchant_payable"  // liability: what we owe the merchant
	REFUNDS  = "refunds_payable"   // liability: refunds promised, not yet sent
	FEES     = "fee_revenue"       // revenue: processing fees we kept
)

// RefundService: every public method takes the mutex. The check "does the remaining balance cover this
// refund" and the reservation that consumes it must be one atomic step, or two concurrent partial refunds
// can both pass the check. Go's sync.Mutex is not re-entrant, so the public methods lock and delegate to
// unexported *Locked helpers. In a database this is one conditional UPDATE (see the docs).
type RefundService struct {
	mu               sync.Mutex
	ledger           *Ledger
	clock            Clock
	policy           RefundPolicy
	captureOrder     []string
	captures         map[string]*Capture
	refunds          map[string]*Refund
	refundsByCapture map[string][]*Refund
	entrySeq         int
}

func NewRefundService(ledger *Ledger, clock Clock, policy RefundPolicy) *RefundService {
	s := &RefundService{ledger: ledger, clock: clock, policy: policy, captures: map[string]*Capture{}, refunds: map[string]*Refund{}, refundsByCapture: map[string][]*Refund{}}
	ledger.Open(SCHEME, ASSET)
	ledger.Open(MERCHANT, LIABILITY)
	ledger.Open(REFUNDS, LIABILITY)
	ledger.Open(FEES, REVENUE)
	return s
}

func (s *RefundService) nextEntryID() string {
	s.entrySeq++
	return fmt.Sprintf("je_%03d", s.entrySeq)
}

func (s *RefundService) post(id, description string, postings ...Posting) error {
	e, err := NewJournalEntry(id, description, postings)
	if err != nil {
		return err
	}
	return s.ledger.Post(e)
}

// Capture records a capture: the scheme owes us the gross, we owe the merchant the net, and we keep the fee.
func (s *RefundService) Capture(captureID, paymentID string, amount Money, feeBps, fixedFeeMinor int64) (*Capture, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.captures[captureID]; exists {
		return nil, fmt.Errorf("%w: capture exists: %s", ErrIllegalState, captureID)
	}
	if !amount.IsPositive() {
		return nil, fmt.Errorf("%w: capture amount must be positive", ErrInvalidArgument)
	}
	variable := floorDiv(amount.MinorUnits*feeBps+5_000, 10_000)
	fee := OfMinor(variable+fixedFeeMinor, amount.Currency)
	c := &Capture{captureID, paymentID, amount, fee, s.clock.Today()}
	if err := s.post(s.nextEntryID(), "capture "+captureID,
		Debit(SCHEME, amount), Credit(MERCHANT, mustMinus(amount, fee)), Credit(FEES, fee)); err != nil {
		return nil, err
	}
	s.captures[captureID] = c
	s.captureOrder = append(s.captureOrder, captureID)
	s.refundsByCapture[captureID] = []*Refund{}
	return c, nil
}

// floorDiv is Java's Math.floorDiv: rounds toward negative infinity, unlike Go's / which truncates.
func floorDiv(a, b int64) int64 {
	q := a / b
	if (a%b != 0) && ((a < 0) != (b < 0)) {
		q--
	}
	return q
}

func (s *RefundService) Refunded(captureID string) (Money, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sumLocked(captureID, SUCCEEDED)
}

func (s *RefundService) Pending(captureID string) (Money, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sumLocked(captureID, PENDING)
}

// RemainingRefundable is captured - succeeded - pending. A pending refund already consumes balance: that is what stops the race.
func (s *RefundService) RemainingRefundable(captureID string) (Money, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.remainingLocked(captureID)
}

func (s *RefundService) remainingLocked(captureID string) (Money, error) {
	c, err := s.captureLocked(captureID)
	if err != nil {
		return Money{}, err
	}
	refunded, _ := s.sumLocked(captureID, SUCCEEDED)
	pending, _ := s.sumLocked(captureID, PENDING)
	return mustMinus(mustMinus(c.Amount, refunded), pending), nil
}

func (s *RefundService) FeeReturned(captureID string) (Money, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.feeReturnedLocked(captureID)
}

func (s *RefundService) feeReturnedLocked(captureID string) (Money, error) {
	c, err := s.captureLocked(captureID)
	if err != nil {
		return Money{}, err
	}
	total := Zero(c.Amount.Currency)
	for _, r := range s.refundsByCapture[captureID] {
		total = mustPlus(total, r.FeeReturned)
	}
	return total, nil
}

func (s *RefundService) Refund(refundID string) (*Refund, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.refundLocked(refundID)
}

func (s *RefundService) refundLocked(refundID string) (*Refund, error) {
	r, ok := s.refunds[refundID]
	if !ok {
		return nil, fmt.Errorf("%w: unknown refund %s", ErrInvalidArgument, refundID)
	}
	return r, nil
}

func (s *RefundService) RefundsFor(captureID string) ([]*Refund, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.captureLocked(captureID)
	if err != nil {
		return nil, err
	}
	return append([]*Refund(nil), s.refundsByCapture[c.ID]...), nil
}

func (s *RefundService) captureLocked(captureID string) (*Capture, error) {
	c, ok := s.captures[captureID]
	if !ok {
		return nil, fmt.Errorf("%w: unknown capture %s", ErrInvalidArgument, captureID)
	}
	return c, nil
}

func (s *RefundService) sumLocked(captureID string, state RefundState) (Money, error) {
	c, err := s.captureLocked(captureID)
	if err != nil {
		return Money{}, err
	}
	total := Zero(c.Amount.Currency)
	for _, r := range s.refundsByCapture[captureID] {
		if r.State == state {
			total = mustPlus(total, r.Amount)
		}
	}
	return total, nil
}

// RequestRefund checks, in order: idempotency (same id), amount positive and same currency, the refund
// window, and the remaining refundable balance. Only after every check passes does anything change: the
// refund is recorded PENDING and the merchant's balance is moved into refunds_payable.
func (s *RefundService) RequestRefund(refundID, captureID string, amount Money, reason string) (RefundResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.refunds[refundID]; ok {
		if existing.CaptureID == captureID && existing.Amount == amount {
			return RefundResult{existing, true}, nil
		}
		return RefundResult{}, newRefundError(ErrIdempotencyConflict, fmt.Sprintf("refund id %s was already used for %v on %s", refundID, existing.Amount, existing.CaptureID))
	}
	c, err := s.captureLocked(captureID)
	if err != nil {
		return RefundResult{}, err
	}
	if !amount.IsPositive() {
		return RefundResult{}, fmt.Errorf("%w: refund amount must be positive: %v", ErrInvalidArgument, amount)
	}
	if amount.Currency != c.Amount.Currency {
		return RefundResult{}, fmt.Errorf("%w: cannot combine %s with %s", ErrCurrencyMismatch, c.Amount.Currency.Code, amount.Currency.Code)
	}
	today := s.clock.Today()
	deadline := c.CapturedOn.AddDate(0, 0, s.policy.WindowDays)
	if today.After(deadline) {
		return RefundResult{}, newRefundError(ErrWindowClosed, fmt.Sprintf("capture %s on %s: refund window of %d days closed on %s (today %s, %d days)",
			captureID, fmtDate(c.CapturedOn), s.policy.WindowDays, fmtDate(deadline), fmtDate(today), daysBetween(c.CapturedOn, today)))
	}
	remaining, _ := s.remainingLocked(captureID)
	if amount.Compare(remaining) > 0 {
		refunded, _ := s.sumLocked(captureID, SUCCEEDED)
		pending, _ := s.sumLocked(captureID, PENDING)
		return RefundResult{}, newRefundError(ErrOverRefund, fmt.Sprintf("refund %v exceeds remaining refundable %v on %s (captured %v, refunded %v, pending %v)",
			amount, remaining, captureID, c.Amount, refunded, pending))
	}
	r := NewRefund(refundID, captureID, amount, reason, today)
	if err := s.post(reserveEntryID(refundID), "refund "+refundID+" requested on "+captureID,
		Debit(MERCHANT, amount), Credit(REFUNDS, amount)); err != nil {
		return RefundResult{}, err
	}
	s.refunds[refundID] = r
	s.refundsByCapture[captureID] = append(s.refundsByCapture[captureID], r)
	return RefundResult{r, false}, nil
}

func reserveEntryID(refundID string) string { return "je_req_" + refundID }

// Succeed: the processor confirmed the refund. Money leaves: refunds_payable is cleared against the scheme
// account, and under PROPORTIONAL the fee share is returned from fee_revenue to the merchant.
// Returns false for a redelivered confirmation (already SUCCEEDED).
func (s *RefundService) Succeed(refundID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.refundLocked(refundID)
	if err != nil {
		return false, err
	}
	if r.State == SUCCEEDED {
		return false, nil
	}
	if r.State != PENDING {
		return false, newRefundError(ErrRefund, fmt.Sprintf("refund %s is %v; cannot succeed", refundID, r.State))
	}
	c, _ := s.captureLocked(r.CaptureID)
	feeBack := Zero(r.Amount.Currency)
	if s.policy.FeePolicy == PROPORTIONAL {
		if feeBack, err = s.feeShareLocked(c, r.Amount); err != nil {
			return false, err
		}
	}
	postings := []Posting{Debit(REFUNDS, r.Amount), Credit(SCHEME, r.Amount)}
	if feeBack.IsPositive() {
		postings = append(postings, Debit(FEES, feeBack), Credit(MERCHANT, feeBack))
	}
	if err := s.post(s.nextEntryID(), "refund "+refundID+" settled", postings...); err != nil {
		return false, err
	}
	r.State = SUCCEEDED
	r.FeeReturned = feeBack
	return true, nil
}

// feeShareLocked: fee to return for a refund that is about to succeed, computed cumulatively so the rounding
// never drifts: target(after) - returned(so far), where target(x) = floor(fee * x / captured). When the
// capture is fully refunded, target is exactly the fee, so the sum of the shares is exactly the fee.
func (s *RefundService) feeShareLocked(c *Capture, refundAmount Money) (Money, error) {
	refunded, _ := s.sumLocked(c.ID, SUCCEEDED)
	after := mustPlus(refunded, refundAmount).MinorUnits
	scaled, err := mulChecked(c.Fee.MinorUnits, after)
	if err != nil {
		return Money{}, err
	}
	target := floorDiv(scaled, c.Amount.MinorUnits)
	returned, _ := s.feeReturnedLocked(c.ID)
	return mustMinus(OfMinor(target, c.Fee.Currency), returned), nil
}

// Fail: the processor refused the refund: the reservation is reversed and the balance becomes refundable again.
func (s *RefundService) Fail(refundID, why string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.refundLocked(refundID)
	if err != nil {
		return err
	}
	if r.State != PENDING {
		return newRefundError(ErrRefund, fmt.Sprintf("refund %s is %v; cannot fail", refundID, r.State))
	}
	var reserve *JournalEntry
	for i := range s.ledger.journal {
		if s.ledger.journal[i].ID == reserveEntryID(refundID) {
			reserve = &s.ledger.journal[i]
			break
		}
	}
	if reserve == nil {
		return fmt.Errorf("%w: no reservation entry for %s", ErrIllegalState, refundID)
	}
	if err := s.ledger.Post(reserve.Reversal(s.nextEntryID(), "refund "+refundID+" failed: "+why)); err != nil {
		return err
	}
	r.State = FAILED
	r.FailureReason = why
	return nil
}

// CheckInvariants: the invariants the tests assert after every operation. Returns an empty slice when all hold.
func (s *RefundService) CheckInvariants() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	broken := []string{}
	if !s.ledger.Balances() {
		broken = append(broken, "ledger does not balance")
	}
	pendingTotal := Zero(s.ledger.Balance(REFUNDS).Currency)
	for _, id := range s.captureOrder {
		c := s.captures[id]
		refunded, _ := s.sumLocked(c.ID, SUCCEEDED)
		pending, _ := s.sumLocked(c.ID, PENDING)
		feeReturned, _ := s.feeReturnedLocked(c.ID)
		if mustPlus(refunded, pending).Compare(c.Amount) > 0 {
			broken = append(broken, c.ID+": refunded + pending exceeds captured")
		}
		if feeReturned.Compare(c.Fee) > 0 {
			broken = append(broken, c.ID+": fee returned exceeds fee charged")
		}
		if refunded == c.Amount && s.policy.FeePolicy == PROPORTIONAL && feeReturned != c.Fee {
			broken = append(broken, c.ID+": fully refunded but fee not fully returned")
		}
		pendingTotal = mustPlus(pendingTotal, pending)
	}
	if s.ledger.Balance(REFUNDS) != pendingTotal {
		broken = append(broken, fmt.Sprintf("refunds_payable %v != pending %v", s.ledger.Balance(REFUNDS), pendingTotal))
	}
	return broken
}

// ---------------------------------------------------------------------------------------------
// Demo
// ---------------------------------------------------------------------------------------------

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func orPanic(err error) {
	if err != nil {
		panic(err)
	}
}

func balances(l *Ledger) {
	for _, a := range l.Accounts() {
		fmt.Printf("   %-18s %12s\n", a, l.Balance(a))
	}
	fmt.Println("   ledger balances:", l.Balances())
}

func main() {
	clock := NewFakeClock(Date(2026, time.January, 10))
	ledger := NewLedger(USD)
	svc := NewRefundService(ledger, clock, must(NewRefundPolicy(180, PROPORTIONAL)))

	fmt.Println("== Capture 100.00 USD at 2.90% + 0.30 ==")
	cap := must(svc.Capture("cap_1", "pay_1", OfMinor(10_000, USD), 290, 30))
	fmt.Printf("fee %v, merchant owed %v\n", cap.Fee, mustMinus(cap.Amount, cap.Fee))
	balances(ledger)

	fmt.Println()
	fmt.Println("== Partial refund 30.00, settled ==")
	clock.AdvanceDays(5)
	must(svc.RequestRefund("re_1", "cap_1", OfMinor(3_000, USD), "one item returned"))
	fmt.Println("pending: remaining refundable", must(svc.RemainingRefundable("cap_1")))
	must(svc.Succeed("re_1"))
	fmt.Printf("settled: %v; remaining %v\n", must(svc.Refund("re_1")), must(svc.RemainingRefundable("cap_1")))

	fmt.Println()
	fmt.Println("== A second partial is pending; an over-refund is refused; a retry replays ==")
	must(svc.RequestRefund("re_2", "cap_1", OfMinor(3_000, USD), "second item returned"))
	if _, err := svc.RequestRefund("re_3", "cap_1", OfMinor(5_000, USD), "customer asked for the rest"); errors.Is(err, ErrOverRefund) {
		fmt.Println("refused:", err)
	}
	again := must(svc.RequestRefund("re_2", "cap_1", OfMinor(3_000, USD), "client retry"))
	fmt.Printf("re_2 again -> replayed=%t, state %v\n", again.Replayed, again.Refund.State)
	if _, err := svc.RequestRefund("re_2", "cap_1", OfMinor(3_500, USD), "client bug"); errors.Is(err, ErrIdempotencyConflict) {
		fmt.Println("refused:", err)
	}

	fmt.Println()
	fmt.Println("== The processor fails re_2: the reservation is reversed ==")
	orPanic(svc.Fail("re_2", "card account closed"))
	fmt.Printf("re_2 %v; remaining refundable %v\n", must(svc.Refund("re_2")).State, must(svc.RemainingRefundable("cap_1")))

	fmt.Println()
	fmt.Println("== Refund the remaining 70.00: the fee shares sum to exactly the fee ==")
	must(svc.RequestRefund("re_4", "cap_1", OfMinor(7_000, USD), "order canceled"))
	must(svc.Succeed("re_4"))
	fmt.Println("redelivered confirmation applied again?", must(svc.Succeed("re_4")))
	for _, r := range must(svc.RefundsFor("cap_1")) {
		fmt.Println("   " + r.String())
	}
	fmt.Printf("refunded %v, fee returned %v of %v, remaining %v\n", must(svc.Refunded("cap_1")), must(svc.FeeReturned("cap_1")), cap.Fee,
		must(svc.RemainingRefundable("cap_1")))
	balances(ledger)
	fmt.Println("journal for cap_1:")
	for _, e := range ledger.Journal() {
		fmt.Printf("   %s  %s\n", e.ID, e.Description)
		for _, p := range e.Postings {
			fmt.Println("   " + p.String())
		}
	}

	fmt.Println()
	fmt.Println("== The same full refund under FeeRefundPolicy.NONE: the merchant ends up owing the fee ==")
	noneLedger := NewLedger(USD)
	none := NewRefundService(noneLedger, clock, must(NewRefundPolicy(180, NONE)))
	must(none.Capture("cap_n", "pay_n", OfMinor(10_000, USD), 290, 30))
	must(none.RequestRefund("re_n", "cap_n", OfMinor(10_000, USD), "order canceled"))
	must(none.Succeed("re_n"))
	fmt.Printf("merchant_payable %v, fee_revenue %v, ledger balances: %t\n", noneLedger.Balance(MERCHANT), noneLedger.Balance(FEES), noneLedger.Balances())

	fmt.Println()
	fmt.Println("== Rounding: 10.00 USD capture, fee 0.59, refunded as 3.33 + 3.33 + 3.34 ==")
	small := must(svc.Capture("cap_2", "pay_2", OfMinor(1_000, USD), 290, 30))
	for _, amt := range []int64{333, 333, 334} {
		id := fmt.Sprintf("re_s%d_%d", amt, len(must(svc.RefundsFor("cap_2"))))
		must(svc.RequestRefund(id, "cap_2", OfMinor(amt, USD), "split"))
		must(svc.Succeed(id))
	}
	var shares []string
	for _, r := range must(svc.RefundsFor("cap_2")) {
		shares = append(shares, r.FeeReturned.String())
	}
	fmt.Printf("shares: [%s] sum %v (fee %v; per-refund floor would give 0.57)\n", strings.Join(shares, ", "), must(svc.FeeReturned("cap_2")), small.Fee)

	fmt.Println()
	fmt.Println("== The refund window: 180 days ==")
	must(svc.Capture("cap_3", "pay_3", OfMinor(2_500, USD), 290, 30))
	clock.AdvanceDays(180)
	must(svc.RequestRefund("re_w1", "cap_3", OfMinor(500, USD), "day 180"))
	fmt.Println("day 180: accepted, remaining", must(svc.RemainingRefundable("cap_3")))
	clock.AdvanceDays(1)
	if _, err := svc.RequestRefund("re_w2", "cap_3", OfMinor(500, USD), "day 181"); errors.Is(err, ErrWindowClosed) {
		fmt.Println("refused:", err)
	}

	fmt.Println()
	fmt.Printf("invariants broken: [%s]\n", strings.Join(svc.CheckInvariants(), ", "))
}
