// Project: projects/idempotent-payment-gateway — Stage 02: payment state machine (Go).
// A Payment moves through Created -> Processing -> Succeeded | Failed (or Created -> Canceled), and a Succeeded
// payment can be refunded in parts until the refunded total equals the captured amount, when it becomes Refunded.
// Every move is checked against one transition table and appended to an immutable log.
// Go 1.22+, standard library only. Same design as the Java and Python solutions.
package main

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

type PaymentState int

const (
	Created PaymentState = iota
	Processing
	Succeeded
	Failed
	Canceled
	Refunded
)

func (s PaymentState) String() string {
	return [...]string{"CREATED", "PROCESSING", "SUCCEEDED", "FAILED", "CANCELED", "REFUNDED"}[s]
}

// IsTerminal: terminal states accept no further transition (a fully refunded payment is done).
func (s PaymentState) IsTerminal() bool { return s == Failed || s == Canceled || s == Refunded }

// Wire is the lower-case name used in API bodies and webhook payloads: "succeeded".
func (s PaymentState) Wire() string { return strings.ToLower(s.String()) }

// ErrIllegalTransition wraps ErrIllegalState, as IllegalTransitionException extends IllegalStateException.
var ErrIllegalTransition = fmt.Errorf("%w: illegal transition", ErrIllegalState)

func illegalTransition(paymentID string, from, to PaymentState) error {
	return fmt.Errorf("%w: %s: %v -> %v", ErrIllegalTransition, paymentID, from, to)
}

// ErrInvalidRefund is a refund that is not positive, or more than what is still refundable.
var ErrInvalidRefund = errors.New("invalid refund")

// Transition is one entry of the log. Partial refunds are logged as Succeeded -> Succeeded with the amount in the reason.
type Transition struct {
	From     PaymentState
	To       PaymentState
	Reason   string
	AtMillis int64
}

// transitionTable is the single source of truth for which moves are legal (PaymentStateMachine in Java).
var transitionTable = map[PaymentState][]PaymentState{
	Created:    {Processing, Canceled},
	Processing: {Succeeded, Failed},
	Succeeded:  {Refunded},
	Failed:     {},
	Canceled:   {},
	Refunded:   {},
}

func CanTransition(from, to PaymentState) bool { return slices.Contains(transitionTable[from], to) }

// NextStates is a copy of the legal targets from a state.
func NextStates(from PaymentState) []PaymentState { return slices.Clone(transitionTable[from]) }

// Payment is a single payment attempt and its money: what was captured and how much of it has been given back.
type Payment struct {
	id                 string
	amount             Money
	clock              Clock
	state              PaymentState
	captured           Money
	refunded           Money
	processorReference string
	version            int64 // bumps on every recorded transition: the optimistic-locking token a database row would carry
	history            []Transition
}

func NewPayment(id string, amount Money, clock Clock) (*Payment, error) {
	if strings.TrimSpace(id) == "" {
		return nil, errors.New("id is required")
	}
	if clock == nil {
		return nil, errors.New("clock is required")
	}
	if !amount.IsPositive() {
		return nil, fmt.Errorf("amount must be positive: %v", amount)
	}
	return &Payment{id: id, amount: amount, clock: clock, state: Created,
		captured: OfMinor(0, amount.Currency), refunded: OfMinor(0, amount.Currency), version: 1}, nil
}

func (p *Payment) ID() string                 { return p.id }
func (p *Payment) Amount() Money              { return p.amount }
func (p *Payment) State() PaymentState        { return p.state }
func (p *Payment) Captured() Money            { return p.captured }
func (p *Payment) Refunded() Money            { return p.refunded }
func (p *Payment) ProcessorReference() string { return p.processorReference }
func (p *Payment) Version() int64             { return p.version }
func (p *Payment) History() []Transition      { return slices.Clone(p.history) }

// Refundable is what can still be refunded: captured minus refunded (zero unless Succeeded).
func (p *Payment) Refundable() Money { return must(p.captured.Minus(p.refunded)) }

func (p *Payment) apply(to PaymentState, reason string) error {
	if !CanTransition(p.state, to) {
		return illegalTransition(p.id, p.state, to)
	}
	p.record(Transition{p.state, to, reason, p.clock.NowMillis()})
	p.state = to
	return nil
}

func (p *Payment) record(t Transition) {
	p.history = append(p.history, t)
	p.version++
}

func (p *Payment) StartProcessing() error { return p.apply(Processing, "sent to processor") }

// Succeed: the processor approved; the full amount is captured.
func (p *Payment) Succeed(reference string) error {
	if err := p.apply(Succeeded, "approved "+reference); err != nil {
		return err
	}
	p.processorReference = reference
	p.captured = p.amount
	return nil
}

func (p *Payment) Fail(reason string) error { return p.apply(Failed, reason) }

func (p *Payment) Cancel(reason string) error { return p.apply(Canceled, reason) }

// Refund part or all of the captured amount. Only a Succeeded payment can be refunded; the amount must be
// positive, in the payment's currency, and no more than what is still refundable. Returns what remains.
func (p *Payment) Refund(refundAmount Money, reason string) (Money, error) {
	if p.state != Succeeded {
		return Money{}, illegalTransition(p.id, p.state, Refunded)
	}
	if !refundAmount.IsPositive() {
		return Money{}, fmt.Errorf("%w: refund must be positive: %v", ErrInvalidRefund, refundAmount)
	}
	remaining := p.Refundable()
	if refundAmount.Currency != remaining.Currency {
		return Money{}, fmt.Errorf("%w: cannot combine %s with %s", ErrCurrencyMismatch, refundAmount.Currency.Code, remaining.Currency.Code)
	}
	if refundAmount.Compare(remaining) > 0 {
		return Money{}, fmt.Errorf("%w: refund %v exceeds refundable %v", ErrInvalidRefund, refundAmount, remaining)
	}
	p.refunded = must(p.refunded.Plus(refundAmount))
	note := fmt.Sprintf("%s (%v)", reason, refundAmount)
	if p.refunded == p.captured {
		if err := p.apply(Refunded, note); err != nil {
			return Money{}, err
		}
	} else {
		p.record(Transition{Succeeded, Succeeded, note, p.clock.NowMillis()})
	}
	return p.Refundable(), nil
}

func (p *Payment) String() string {
	return fmt.Sprintf("%s[%s v%d %v captured=%v refunded=%v]", p.id, p.state.Wire(), p.version, p.amount, p.captured, p.refunded)
}
