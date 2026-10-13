// Lesson: phases/12-payment-systems-design/02-payment-state-machine/docs/en.md
// The Payment State Machine
// Run:  go run .
// Test: go test ./...
// Sources: Stripe API reference (PaymentIntent status lifecycle); EMV 3-D Secure protocol spec (challenge flow);
//          Kung & Robinson, "On Optimistic Methods for Concurrency Control" (1981). Go 1.22+, stdlib only.
package main

import (
	"errors"
	"fmt"
	"strings"
)

// ---------------------------------------------------------------------------------------------
// Money: integer minor units + ISO 4217 currency (Phase 10, Lesson 01). Copied, not imported.
// ---------------------------------------------------------------------------------------------

type Currency struct {
	Code     string
	Exponent int
}

var (
	USD = Currency{"USD", 2}
	EUR = Currency{"EUR", 2}
)

func (c Currency) MinorUnitsPerMajor() int64 {
	scale := int64(1)
	for i := 0; i < c.Exponent; i++ {
		scale *= 10
	}
	return scale
}

var ErrCurrencyMismatch = errors.New("currency mismatch")

type Money struct {
	MinorUnits int64
	Currency   Currency
}

func OfMinor(minorUnits int64, currency Currency) Money { return Money{minorUnits, currency} }
func Zero(currency Currency) Money                        { return Money{0, currency} }

func (m Money) requireSameCurrency(other Money) error {
	if m.Currency != other.Currency {
		return fmt.Errorf("%w: cannot combine %s with %s", ErrCurrencyMismatch, m.Currency.Code, other.Currency.Code)
	}
	return nil
}

func (m Money) Plus(other Money) (Money, error) {
	if err := m.requireSameCurrency(other); err != nil {
		return Money{}, err
	}
	return Money{m.MinorUnits + other.MinorUnits, m.Currency}, nil
}

func (m Money) Minus(other Money) (Money, error) {
	if err := m.requireSameCurrency(other); err != nil {
		return Money{}, err
	}
	return Money{m.MinorUnits - other.MinorUnits, m.Currency}, nil
}

// mustPlus is for same-currency values the aggregate already validated; a mismatch here is a bug.
func mustPlus(a, b Money) Money {
	s, err := a.Plus(b)
	if err != nil {
		panic(err)
	}
	return s
}

func (m Money) IsPositive() bool { return m.MinorUnits > 0 }
func (m Money) IsZero() bool     { return m.MinorUnits == 0 }

func (m Money) String() string {
	magnitude := m.MinorUnits
	if magnitude < 0 {
		magnitude = -magnitude
	}
	scale := m.Currency.MinorUnitsPerMajor()
	result := fmt.Sprintf("%d", magnitude/scale)
	if m.Currency.Exponent > 0 {
		frac := fmt.Sprintf("%d", magnitude%scale)
		result += "." + strings.Repeat("0", m.Currency.Exponent-len(frac)) + frac
	}
	if m.MinorUnits < 0 {
		result = "-" + result
	}
	return result + " " + m.Currency.Code
}

// ---------------------------------------------------------------------------------------------
// Time is injected.
// ---------------------------------------------------------------------------------------------

type Clock interface{ NowMillis() int64 }

type FakeClock struct{ now int64 }

func NewFakeClock(start int64) *FakeClock   { return &FakeClock{now: start} }
func (c *FakeClock) NowMillis() int64       { return c.now }
func (c *FakeClock) Advance(millis int64)   { c.now += millis }

// ---------------------------------------------------------------------------------------------
// States and events.
// ---------------------------------------------------------------------------------------------

type PaymentState int

const (
	Created PaymentState = iota
	RequiresAction
	Processing
	Succeeded
	Refunding
	Refunded
	Failed
	Canceled
)

var AllStates = []PaymentState{Created, RequiresAction, Processing, Succeeded, Refunding, Refunded, Failed, Canceled}

func (s PaymentState) String() string {
	return [...]string{"CREATED", "REQUIRES_ACTION", "PROCESSING", "SUCCEEDED", "REFUNDING", "REFUNDED", "FAILED", "CANCELED"}[s]
}

func (s PaymentState) IsTerminal() bool { return s == Refunded || s == Failed || s == Canceled }

type EventKind int

const (
	Confirm EventKind = iota
	RequireAction
	ActionCompleted
	ProcessorApproved
	ProcessorDeclined
	Cancel
	RefundRequested
	RefundSucceeded
	RefundFailed
)

var AllKinds = []EventKind{Confirm, RequireAction, ActionCompleted, ProcessorApproved, ProcessorDeclined, Cancel, RefundRequested, RefundSucceeded, RefundFailed}

func (k EventKind) String() string {
	return [...]string{"CONFIRM", "REQUIRE_ACTION", "ACTION_COMPLETED", "PROCESSOR_APPROVED", "PROCESSOR_DECLINED", "CANCEL",
		"REFUND_REQUESTED", "REFUND_SUCCEEDED", "REFUND_FAILED"}[k]
}

// PaymentEvent carries an id so a redelivered callback is recognised; Amount is set only for refund requests.
type PaymentEvent struct {
	EventID string
	Kind    EventKind
	Amount  *Money
	Detail  string
}

func NewEvent(eventID string, kind EventKind, detail string) PaymentEvent {
	return PaymentEvent{EventID: eventID, Kind: kind, Detail: detail}
}

func NewRefundRequested(eventID string, amount Money) PaymentEvent {
	return PaymentEvent{EventID: eventID, Kind: RefundRequested, Amount: &amount}
}

func (e PaymentEvent) validate() error {
	if e.EventID == "" {
		return errors.New("event id required")
	}
	if e.Kind == RefundRequested && e.Amount == nil {
		return errors.New("REFUND_REQUESTED needs an amount")
	}
	if e.Kind != RefundRequested && e.Amount != nil {
		return fmt.Errorf("%s does not carry an amount", e.Kind)
	}
	return nil
}

// ---------------------------------------------------------------------------------------------
// The transition table: data, not code.
// ---------------------------------------------------------------------------------------------

type Guard int

const (
	Always Guard = iota
	RefundWithinBalance
	RefundLeavesBalance
	RefundClearsBalance
)

func (g Guard) String() string {
	return [...]string{"ALWAYS", "REFUND_WITHIN_BALANCE", "REFUND_LEAVES_BALANCE", "REFUND_CLEARS_BALANCE"}[g]
}

type Rule struct {
	From  PaymentState
	Kind  EventKind
	Guard Guard
	To    PaymentState
}

var Rules = []Rule{
	{Created, Confirm, Always, Processing},
	{Created, Cancel, Always, Canceled},
	{Processing, RequireAction, Always, RequiresAction},
	{Processing, ProcessorApproved, Always, Succeeded},
	{Processing, ProcessorDeclined, Always, Failed},
	{RequiresAction, ActionCompleted, Always, Processing},
	{RequiresAction, Cancel, Always, Canceled},
	{Succeeded, RefundRequested, RefundWithinBalance, Refunding},
	{Refunding, RefundSucceeded, RefundLeavesBalance, Succeeded},
	{Refunding, RefundSucceeded, RefundClearsBalance, Refunded},
	{Refunding, RefundFailed, Always, Succeeded},
}

var ErrIllegalTransition = errors.New("illegal transition")
var ErrConcurrencyConflict = errors.New("concurrency conflict")

func HasRow(from PaymentState, kind EventKind) bool {
	for _, r := range Rules {
		if r.From == from && r.Kind == kind {
			return true
		}
	}
	return false
}

func guardHolds(g Guard, p *Payment, e PaymentEvent) (bool, error) {
	switch g {
	case Always:
		return true, nil
	case RefundWithinBalance:
		after, err := p.Refunded.Plus(*e.Amount)
		if err != nil {
			return false, err
		}
		return e.Amount.IsPositive() && after.MinorUnits <= p.Amount.MinorUnits, nil
	case RefundLeavesBalance:
		return mustPlus(p.Refunded, p.PendingRefund).MinorUnits < p.Amount.MinorUnits, nil
	default: // RefundClearsBalance
		return mustPlus(p.Refunded, p.PendingRefund) == p.Amount, nil
	}
}

// Resolve picks the one rule whose guard holds, or returns ErrIllegalTransition with the precise reason.
func Resolve(p *Payment, e PaymentEvent) (Rule, error) {
	if p.State.IsTerminal() {
		return Rule{}, fmt.Errorf("%w: %s is %s (terminal); %s refused", ErrIllegalTransition, p.ID, p.State, e.Kind)
	}
	var candidates []Rule
	for _, r := range Rules {
		if r.From == p.State && r.Kind == e.Kind {
			candidates = append(candidates, r)
		}
	}
	if len(candidates) == 0 {
		return Rule{}, fmt.Errorf("%w: %s: %s is not legal from %s", ErrIllegalTransition, p.ID, e.Kind, p.State)
	}
	for _, r := range candidates {
		ok, err := guardHolds(r.Guard, p, e)
		if err != nil {
			return Rule{}, err
		}
		if ok {
			return r, nil
		}
	}
	g := candidates[0].Guard
	why := "balance guard failed"
	if g == RefundWithinBalance && !e.Amount.IsPositive() {
		why = "refund amount must be positive"
	} else if g == RefundWithinBalance {
		why = fmt.Sprintf("refund %s exceeds refundable %s", *e.Amount, p.Refundable())
	}
	return Rule{}, fmt.Errorf("%w: %s: %s from %s refused by guard %s (%s)", ErrIllegalTransition, p.ID, e.Kind, p.State, g, why)
}

// ---------------------------------------------------------------------------------------------
// The persisted transition and the aggregate.
// ---------------------------------------------------------------------------------------------

type Transition struct {
	PaymentID string
	Version   int
	EventID   string
	Kind      EventKind
	FromState PaymentState
	ToState   PaymentState
	Amount    *Money
	Reason    string
	AtMillis  int64
}

func (t Transition) String() string {
	return fmt.Sprintf("v%-2d %-20s %-16s -> %-16s %s", t.Version, t.Kind, t.FromState, t.ToState, t.Reason)
}

type ApplyOutcome int

const (
	Applied ApplyOutcome = iota
	Duplicate
)

func (o ApplyOutcome) String() string { return [...]string{"APPLIED", "DUPLICATE"}[o] }

type Payment struct {
	ID            string
	Amount        Money
	State         PaymentState
	Refunded      Money
	PendingRefund Money
	Version       int
	clock         Clock
	history       []Transition
	uncommitted   []Transition
	appliedIDs    map[string]bool
}

func NewPayment(id string, amount Money, clock Clock) (*Payment, error) {
	if !amount.IsPositive() {
		return nil, fmt.Errorf("amount must be positive: %s", amount)
	}
	return &Payment{ID: id, Amount: amount, State: Created, Refunded: Zero(amount.Currency),
		PendingRefund: Zero(amount.Currency), clock: clock, appliedIDs: map[string]bool{}}, nil
}

func (p *Payment) Refundable() Money {
	r, _ := p.Amount.Minus(p.Refunded)
	r, _ = r.Minus(p.PendingRefund)
	return r
}

func (p *Payment) History() []Transition     { return append([]Transition(nil), p.history...) }
func (p *Payment) Uncommitted() []Transition { return append([]Transition(nil), p.uncommitted...) }

func (p *Payment) CanApply(e PaymentEvent) bool {
	if p.appliedIDs[e.EventID] {
		return false
	}
	if err := e.validate(); err != nil {
		return false
	}
	_, err := Resolve(p, e)
	return err == nil
}

// Apply: exactly one of the state moves and one transition is recorded; Duplicate and nothing changes;
// or an error and nothing changes.
func (p *Payment) Apply(e PaymentEvent, reason string) (ApplyOutcome, error) {
	if err := e.validate(); err != nil {
		return Duplicate, err
	}
	if p.appliedIDs[e.EventID] {
		return Duplicate, nil
	}
	rule, err := Resolve(p, e)
	if err != nil {
		return Duplicate, err
	}
	t := Transition{p.ID, p.Version + 1, e.EventID, e.Kind, p.State, rule.To, e.Amount, reason, p.clock.NowMillis()}
	p.mutate(t)
	p.uncommitted = append(p.uncommitted, t)
	return Applied, nil
}

// mutate is the single place state changes; Apply and Replay share it so they agree by construction.
func (p *Payment) mutate(t Transition) {
	switch t.Kind {
	case RefundRequested:
		p.PendingRefund = mustPlus(p.PendingRefund, *t.Amount)
	case RefundSucceeded:
		p.Refunded = mustPlus(p.Refunded, p.PendingRefund)
		p.PendingRefund = Zero(p.Amount.Currency)
	case RefundFailed:
		p.PendingRefund = Zero(p.Amount.Currency)
	}
	p.State = t.ToState
	p.Version = t.Version
	p.appliedIDs[t.EventID] = true
	p.history = append(p.history, t)
}

func (p *Payment) MarkCommitted() { p.uncommitted = nil }

// Replay rebuilds a payment from persisted history. A gap or a mismatched FromState is corruption.
func Replay(id string, amount Money, transitions []Transition, clock Clock) (*Payment, error) {
	p, err := NewPayment(id, amount, clock)
	if err != nil {
		return nil, err
	}
	for _, t := range transitions {
		if t.Version != p.Version+1 {
			return nil, fmt.Errorf("%s: history gap, expected v%d got v%d", id, p.Version+1, t.Version)
		}
		if t.FromState != p.State {
			return nil, fmt.Errorf("%s: v%d claims fromState %s but payment is %s", id, t.Version, t.FromState, p.State)
		}
		p.mutate(t)
	}
	return p, nil
}

// ---------------------------------------------------------------------------------------------
// The store: append-only log per payment, guarded by a version check.
// ---------------------------------------------------------------------------------------------

type created struct {
	amount    Money
	createdAt int64
}

type PaymentStore struct {
	clock   Clock
	created map[string]created
	log     map[string][]Transition
}

func NewPaymentStore(clock Clock) *PaymentStore {
	return &PaymentStore{clock: clock, created: map[string]created{}, log: map[string][]Transition{}}
}

func (s *PaymentStore) Create(id string, amount Money) (*Payment, error) {
	if _, ok := s.created[id]; ok {
		return nil, fmt.Errorf("%s already exists", id)
	}
	p, err := NewPayment(id, amount, s.clock)
	if err != nil {
		return nil, err
	}
	s.created[id] = created{amount, s.clock.NowMillis()}
	s.log[id] = nil
	return p, nil
}

func (s *PaymentStore) History(id string) ([]Transition, error) {
	h, ok := s.log[id]
	if !ok {
		return nil, fmt.Errorf("unknown payment %s", id)
	}
	return append([]Transition(nil), h...), nil
}

func (s *PaymentStore) CurrentVersion(id string) int { return len(s.log[id]) }

// Load replays the log. Two callers that load the same payment get two independent copies.
func (s *PaymentStore) Load(id string) (*Payment, error) {
	c, ok := s.created[id]
	if !ok {
		return nil, fmt.Errorf("unknown payment %s", id)
	}
	return Replay(id, c.amount, s.log[id], s.clock)
}

// Save appends the uncommitted transitions; each must be exactly CurrentVersion+1. All-or-nothing.
func (s *PaymentStore) Save(p *Payment) error {
	target, ok := s.log[p.ID]
	if !ok {
		return fmt.Errorf("unknown payment %s", p.ID)
	}
	expected := len(target)
	for _, t := range p.uncommitted {
		if t.Version != expected+1 {
			return fmt.Errorf("%w: %s: expected to write v%d but store is at v%d; reload and retry",
				ErrConcurrencyConflict, p.ID, t.Version, len(target))
		}
		expected++
	}
	s.log[p.ID] = append(target, p.uncommitted...)
	p.MarkCommitted()
	return nil
}

// ---------------------------------------------------------------------------------------------
// The processor adapter and the clock-driven fake.
// ---------------------------------------------------------------------------------------------

type ProcessorOutcome int

const (
	OutcomeApproved ProcessorOutcome = iota
	OutcomeDeclined
	OutcomeChallenge
	OutcomeRefundOK
	OutcomeRefundFailed
)

func (o ProcessorOutcome) String() string {
	return [...]string{"approved", "declined", "challenge", "refund_ok", "refund_failed"}[o]
}

type ProcessorCallback struct {
	Reference   string
	PaymentID   string
	Outcome     ProcessorOutcome
	Detail      string
	DueAtMillis int64
}

type ProcessorAdapter interface {
	Authorize(paymentID string, amount Money) string
	Refund(paymentID string, amount Money) string
	DueCallbacks() []ProcessorCallback
}

type FakeProcessor struct {
	clock    Clock
	latency  int64
	script   map[string][]ProcessorOutcome
	pending  []ProcessorCallback
	Calls    []string
	sequence int
}

func NewFakeProcessor(clock Clock, latencyMillis int64) *FakeProcessor {
	return &FakeProcessor{clock: clock, latency: latencyMillis, script: map[string][]ProcessorOutcome{}}
}

func (f *FakeProcessor) Script(paymentID string, outcomes ...ProcessorOutcome) *FakeProcessor {
	f.script[paymentID] = append(f.script[paymentID], outcomes...)
	return f
}

func (f *FakeProcessor) Authorize(paymentID string, amount Money) string {
	return f.schedule(paymentID, amount, "authorize", OutcomeApproved)
}

func (f *FakeProcessor) Refund(paymentID string, amount Money) string {
	return f.schedule(paymentID, amount, "refund", OutcomeRefundOK)
}

func (f *FakeProcessor) schedule(paymentID string, amount Money, op string, fallback ProcessorOutcome) string {
	outcome := fallback
	if q := f.script[paymentID]; len(q) > 0 {
		outcome = q[0]
		f.script[paymentID] = q[1:]
	}
	f.sequence++
	reference := fmt.Sprintf("pr_%04d", f.sequence)
	f.Calls = append(f.Calls, fmt.Sprintf("%s %s %s -> %s", op, paymentID, amount, reference))
	detail := outcome.String()
	if outcome == OutcomeDeclined {
		detail = "51 insufficient funds"
	}
	f.pending = append(f.pending, ProcessorCallback{reference, paymentID, outcome, detail, f.clock.NowMillis() + f.latency})
	return reference
}

func (f *FakeProcessor) DueCallbacks() []ProcessorCallback {
	now := f.clock.NowMillis()
	var due, keep []ProcessorCallback
	for _, cb := range f.pending {
		if cb.DueAtMillis <= now {
			due = append(due, cb)
		} else {
			keep = append(keep, cb)
		}
	}
	f.pending = keep
	return due
}

// ---------------------------------------------------------------------------------------------
// The service: load, decide, save; then talk to the processor.
// ---------------------------------------------------------------------------------------------

type PaymentService struct {
	store     *PaymentStore
	processor ProcessorAdapter
}

func NewPaymentService(store *PaymentStore, processor ProcessorAdapter) *PaymentService {
	return &PaymentService{store, processor}
}

func (s *PaymentService) Create(id string, amount Money) (*Payment, error) { return s.store.Create(id, amount) }

func (s *PaymentService) applyAndSave(id string, e PaymentEvent, reason string) (*Payment, error) {
	p, err := s.store.Load(id)
	if err != nil {
		return nil, err
	}
	if _, err := p.Apply(e, reason); err != nil {
		return nil, err
	}
	if err := s.store.Save(p); err != nil {
		return nil, err
	}
	return p, nil
}

func (s *PaymentService) Confirm(id, eventID string) (*Payment, error) {
	p, err := s.applyAndSave(id, NewEvent(eventID, Confirm, ""), "merchant confirmed")
	if err != nil {
		return nil, err
	}
	s.processor.Authorize(id, p.Amount) // only after PROCESSING is persisted
	return p, nil
}

func (s *PaymentService) CompleteAction(id, eventID string) (*Payment, error) {
	p, err := s.applyAndSave(id, NewEvent(eventID, ActionCompleted, ""), "customer completed challenge")
	if err != nil {
		return nil, err
	}
	s.processor.Authorize(id, p.Amount)
	return p, nil
}

func (s *PaymentService) Cancel(id, eventID string) (*Payment, error) {
	return s.applyAndSave(id, NewEvent(eventID, Cancel, ""), "merchant canceled")
}

func (s *PaymentService) RequestRefund(id, eventID string, amount Money) (*Payment, error) {
	p, err := s.applyAndSave(id, NewRefundRequested(eventID, amount), "refund requested")
	if err != nil {
		return nil, err
	}
	s.processor.Refund(id, amount)
	return p, nil
}

var outcomeToKind = map[ProcessorOutcome]EventKind{
	OutcomeApproved: ProcessorApproved, OutcomeDeclined: ProcessorDeclined, OutcomeChallenge: RequireAction,
	OutcomeRefundOK: RefundSucceeded, OutcomeRefundFailed: RefundFailed,
}

// Pump drains due callbacks into events. The reference is the event id, so redelivery is a no-op.
func (s *PaymentService) Pump() (int, error) {
	applied := 0
	for _, cb := range s.processor.DueCallbacks() {
		outcome, err := s.Deliver(cb)
		if err != nil {
			return applied, err
		}
		if outcome == Applied {
			applied++
		}
	}
	return applied, nil
}

func (s *PaymentService) Deliver(cb ProcessorCallback) (ApplyOutcome, error) {
	p, err := s.store.Load(cb.PaymentID)
	if err != nil {
		return Duplicate, err
	}
	outcome, err := p.Apply(NewEvent(cb.Reference, outcomeToKind[cb.Outcome], cb.Detail), "processor: "+cb.Detail)
	if err != nil {
		return Duplicate, err
	}
	if outcome == Applied {
		if err := s.store.Save(p); err != nil {
			return Duplicate, err
		}
	}
	return outcome, nil
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

func main() {
	clock := NewFakeClock(1_700_000_000_000)
	store := NewPaymentStore(clock)
	processor := NewFakeProcessor(clock, 800)
	service := NewPaymentService(store, processor)

	fmt.Println("== The transition table ==")
	for _, r := range Rules {
		fmt.Printf("%-16s + %-19s [%-22s] -> %s\n", r.From, r.Kind, r.Guard, r.To)
	}

	fmt.Println("\n== A 3-D Secure payment: created -> processing -> requires_action -> processing -> succeeded ==")
	processor.Script("pay_1001", OutcomeChallenge, OutcomeApproved)
	must(service.Create("pay_1001", OfMinor(4999, USD)))
	must(service.Confirm("pay_1001", "evt_confirm_1"))
	fmt.Printf("after confirm:   %s v%d\n", must(store.Load("pay_1001")).State, store.CurrentVersion("pay_1001"))
	fmt.Printf("pump before latency elapsed -> %d callbacks applied\n", must(service.Pump()))
	clock.Advance(800)
	n := must(service.Pump())
	fmt.Printf("pump at +800ms              -> %d callbacks applied; state %s\n", n, must(store.Load("pay_1001")).State)
	clock.Advance(8_400)
	must(service.CompleteAction("pay_1001", "evt_action_1"))
	clock.Advance(800)
	must(service.Pump())
	p := must(store.Load("pay_1001"))
	fmt.Printf("after approval:  %s v%d\n", p.State, p.Version)

	fmt.Println("\n== The processor redelivers the approval: recognised by event id, not applied twice ==")
	again := ProcessorCallback{"pr_0002", "pay_1001", OutcomeApproved, "approved", clock.NowMillis()}
	fmt.Printf("deliver(pr_0002 again) -> %s, version still %d\n", must(service.Deliver(again)), store.CurrentVersion("pay_1001"))

	fmt.Println("\n== Refunds: SUCCEEDED -> REFUNDING -> SUCCEEDED (partial) or REFUNDED (full) ==")
	clock.Advance(86_400_000)
	must(service.RequestRefund("pay_1001", "evt_refund_1", OfMinor(1500, USD)))
	p = must(store.Load("pay_1001"))
	fmt.Printf("after request:   %s, refundable = %s (15.00 pending at the processor)\n", p.State, p.Refundable())
	if _, err := service.RequestRefund("pay_1001", "evt_refund_2", OfMinor(100, USD)); err != nil {
		fmt.Println("refused:", strings.TrimPrefix(err.Error(), "illegal transition: "))
	}
	clock.Advance(800)
	must(service.Pump())
	p = must(store.Load("pay_1001"))
	fmt.Printf("after settle:    %s, refunded = %s, refundable = %s\n", p.State, p.Refunded, p.Refundable())
	if _, err := service.RequestRefund("pay_1001", "evt_refund_3", OfMinor(4000, USD)); err != nil {
		fmt.Println("refused:", strings.TrimPrefix(err.Error(), "illegal transition: "))
	}
	must(service.RequestRefund("pay_1001", "evt_refund_4", OfMinor(3499, USD)))
	clock.Advance(800)
	must(service.Pump())
	p = must(store.Load("pay_1001"))
	fmt.Printf("after full:      %s (terminal: %v), refunded = %s\n", p.State, p.State.IsTerminal(), p.Refunded)

	must(store.Create("pay_2002", OfMinor(2500, USD)))
	v := store.CurrentVersion("pay_2002")
	fmt.Printf("\n== Optimistic concurrency: two handlers load pay_2002 at v%d; only one may write v%d ==\n", v, v+1)
	handlerA := must(store.Load("pay_2002"))
	handlerB := must(store.Load("pay_2002"))
	must(handlerA.Apply(NewEvent("evt_a", Confirm, ""), "handler A: confirm"))
	must(handlerB.Apply(NewEvent("evt_b", Cancel, ""), "handler B: cancel"))
	if err := store.Save(handlerA); err == nil {
		fmt.Println("handler A saved CONFIRM as v1")
	}
	if err := store.Save(handlerB); err != nil {
		fmt.Println("handler B refused:", strings.TrimPrefix(err.Error(), "concurrency conflict: "))
	}
	fmt.Printf("stored state is %s; B reloads and finds CANCEL is now illegal: canApply=%v\n",
		must(store.Load("pay_2002")).State, must(store.Load("pay_2002")).CanApply(NewEvent("evt_b2", Cancel, "")))

	fmt.Println("\n== Illegal transitions are refused before any mutation ==")
	fresh := must(store.Create("pay_3003", OfMinor(100, USD)))
	for _, k := range []EventKind{ProcessorApproved, RefundSucceeded, ActionCompleted} {
		if _, err := fresh.Apply(NewEvent("evt_x_"+k.String(), k, ""), "spoofed"); err != nil {
			fmt.Println("refused:", strings.TrimPrefix(err.Error(), "illegal transition: "))
		}
	}
	fmt.Printf("pay_3003 still %s v%d, uncommitted = %d\n", fresh.State, fresh.Version, len(fresh.Uncommitted()))

	fmt.Println("\n== Replay: the history of pay_1001 rebuilds the same payment ==")
	history := must(store.History("pay_1001"))
	for _, t := range history {
		fmt.Println(t)
	}
	rebuilt := must(Replay("pay_1001", OfMinor(4999, USD), history, clock))
	fmt.Printf("replayed -> %s v%d, refunded %s\n", rebuilt.State, rebuilt.Version, rebuilt.Refunded)
	fmt.Printf("processor calls: %d\n", len(processor.Calls))
}
