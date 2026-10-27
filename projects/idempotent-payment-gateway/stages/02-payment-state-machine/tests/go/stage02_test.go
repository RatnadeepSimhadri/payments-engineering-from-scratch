// Stage 2 tests for projects/idempotent-payment-gateway (Go): the transition table, versioned history, terminal
// states, and partial refunds in integer minor units. Port of Stage02Test.java: same checks, same names, same order.
package main

import (
	"errors"
	"strings"
	"testing"
)

func s02check(t *testing.T, name string, condition bool) {
	t.Helper()
	if !condition {
		t.Errorf("FAIL: %s", name)
	}
}

func s02checkErr(t *testing.T, name string, err, want error) {
	t.Helper()
	if err == nil {
		t.Errorf("FAIL: %s (no error)", name)
		return
	}
	if want != nil && !errors.Is(err, want) {
		t.Errorf("FAIL: %s (wrong error: %v)", name, err)
	}
}

func s02usd(text string) Money { return must(Parse(text, USD)) }

// s02refund folds the refund result into a Money; an error becomes a zero Money that no check expects.
func s02refund(p *Payment, amount Money, reason string) Money {
	remaining, err := p.Refund(amount, reason)
	if err != nil {
		return Money{}
	}
	return remaining
}

func s02payment(t *testing.T, id, amount string, clock Clock) *Payment {
	t.Helper()
	p, err := NewPayment(id, s02usd(amount), clock)
	if err != nil {
		t.Fatalf("NewPayment(%s): %v", id, err)
	}
	return p
}

func TestStage02Table(t *testing.T) {
	// The table is the single source of truth
	s02check(t, "created -> processing", CanTransition(Created, Processing))
	s02check(t, "created -> canceled", CanTransition(Created, Canceled))
	s02check(t, "created -/-> succeeded", !CanTransition(Created, Succeeded))
	s02check(t, "processing -> succeeded|failed", len(NextStates(Processing)) == 2 && CanTransition(Processing, Succeeded) && CanTransition(Processing, Failed))
	s02check(t, "processing -/-> canceled", !CanTransition(Processing, Canceled))
	s02check(t, "succeeded -> refunded only", len(NextStates(Succeeded)) == 1 && CanTransition(Succeeded, Refunded))
	s02check(t, "terminal states have no exits", len(NextStates(Failed)) == 0 && len(NextStates(Canceled)) == 0 && len(NextStates(Refunded)) == 0)
	s02check(t, "IsTerminal", Failed.IsTerminal() && Canceled.IsTerminal() && Refunded.IsTerminal() && !Succeeded.IsTerminal() && !Created.IsTerminal() && !Processing.IsTerminal())
	s02check(t, "wire names are lower-case", Succeeded.Wire() == "succeeded" && Canceled.Wire() == "canceled")
	next := NextStates(Created)
	next[0] = Refunded
	s02check(t, "NextStates returns a copy", NextStates(Created)[0] == Processing)
}

func TestStage02Payment(t *testing.T) {
	// Happy path with versions and history
	clock := NewFakeClock(5_000)
	p := s02payment(t, "pay_1", "50.00", clock)
	s02check(t, "new payment is CREATED v1", p.State() == Created && p.Version() == 1 && len(p.History()) == 0)
	s02check(t, "nothing captured yet", p.Captured().IsZero() && p.Refunded().IsZero() && p.Refundable().IsZero())
	s02check(t, "StartProcessing succeeds", p.StartProcessing() == nil)
	s02check(t, "processing v2", p.State() == Processing && p.Version() == 2)
	clock.Advance(250)
	s02check(t, "Succeed succeeds", p.Succeed("auth-77") == nil)
	s02check(t, "succeeded v3", p.State() == Succeeded && p.Version() == 3)
	s02check(t, "captured equals amount", p.Captured() == s02usd("50.00") && p.Refundable() == s02usd("50.00"))
	s02check(t, "processor reference kept", p.ProcessorReference() == "auth-77")
	history := p.History()
	if len(history) != 2 {
		t.Fatalf("history has two entries: got %d", len(history))
	}
	s02check(t, "history has two entries", len(history) == 2)
	s02check(t, "history records from/to", history[0].From == Created && history[0].To == Processing && history[1].From == Processing && history[1].To == Succeeded)
	s02check(t, "history timestamps come from the clock", history[0].AtMillis == 5_000 && history[1].AtMillis == 5_250)
	history[0] = Transition{}
	s02check(t, "history is a copy", p.History()[0].From == Created)

	// Illegal moves are rejected and leave the payment untouched
	s02checkErr(t, "succeed twice", p.Succeed("again"), ErrIllegalTransition)
	s02checkErr(t, "fail after success", p.Fail("late"), ErrIllegalTransition)
	s02checkErr(t, "cancel after success", p.Cancel("late"), ErrIllegalTransition)
	s02check(t, "rejected moves do not bump the version", p.Version() == 3 && p.State() == Succeeded)
	s02checkErr(t, "ErrIllegalTransition is an ErrIllegalState", p.Succeed("again"), ErrIllegalState)

	fresh := s02payment(t, "pay_2", "10.00", clock)
	s02checkErr(t, "succeed from CREATED", fresh.Succeed("x"), ErrIllegalTransition)
	s02checkErr(t, "fail from CREATED", fresh.Fail("x"), ErrIllegalTransition)
	_ = fresh.Cancel("customer left")
	s02check(t, "canceled from CREATED", fresh.State() == Canceled && fresh.Version() == 2)
	s02checkErr(t, "nothing leaves CANCELED", fresh.StartProcessing(), ErrIllegalTransition)

	declined := s02payment(t, "pay_3", "10.00", clock)
	_ = declined.StartProcessing()
	s02checkErr(t, "cancel while PROCESSING", declined.Cancel("too late"), ErrIllegalTransition)
	_ = declined.Fail("declined decl-3")
	s02check(t, "failed payment captured nothing", declined.State() == Failed && declined.Captured().IsZero())
	_, err := declined.Refund(s02usd("1.00"), "no")
	s02checkErr(t, "refund a FAILED payment", err, ErrIllegalTransition)

	// Refunds: partial, then full, never more than captured
	s02check(t, "partial refund returns remaining", s02refund(p, s02usd("20.00"), "customer request") == s02usd("30.00"))
	s02check(t, "still SUCCEEDED after partial refund", p.State() == Succeeded && p.Refunded() == s02usd("20.00"))
	h := p.History()
	s02check(t, "partial refund bumps version and logs SUCCEEDED -> SUCCEEDED", p.Version() == 4 && len(h) == 3 &&
		h[2].From == Succeeded && h[2].To == Succeeded && strings.Contains(h[2].Reason, "20.00 USD"))
	_, err = p.Refund(s02usd("30.01"), "too much")
	s02checkErr(t, "refund more than refundable", err, ErrInvalidRefund)
	_, err = p.Refund(s02usd("0.00"), "zero")
	s02checkErr(t, "refund zero", err, ErrInvalidRefund)
	_, err = p.Refund(s02usd("-1.00"), "negative")
	s02checkErr(t, "refund negative", err, ErrInvalidRefund)
	_, err = p.Refund(must(Parse("1.00", EUR)), "eur")
	s02checkErr(t, "refund in another currency", err, ErrCurrencyMismatch)
	s02check(t, "rejected refunds change nothing", p.Refunded() == s02usd("20.00") && p.Version() == 4)
	s02check(t, "second partial refund", s02refund(p, s02usd("29.99"), "goodwill") == s02usd("0.01"))
	s02check(t, "final cent closes the payment", s02refund(p, s02usd("0.01"), "last cent").IsZero() && p.State() == Refunded)
	s02check(t, "refunded equals captured", p.Refunded() == p.Captured() && p.Version() == 6)
	h = p.History()
	s02check(t, "last transition is SUCCEEDED -> REFUNDED", h[len(h)-1].To == Refunded)
	_, err = p.Refund(s02usd("0.01"), "again")
	s02checkErr(t, "nothing leaves REFUNDED", err, ErrIllegalTransition)

	// Construction guards
	_, err = NewPayment("pay_x", s02usd("0.00"), clock)
	s02checkErr(t, "amount must be positive", err, nil)
	_, err = NewPayment(" ", s02usd("1.00"), clock)
	s02checkErr(t, "id is required", err, nil)
}
