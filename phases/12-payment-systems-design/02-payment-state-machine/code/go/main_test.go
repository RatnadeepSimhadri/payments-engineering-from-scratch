// Tests for phases/12-payment-systems-design/02-payment-state-machine. Run: go test ./...
package main

import (
	"errors"
	"fmt"
	"testing"
)

var counter int

func newID() string {
	counter++
	return fmt.Sprintf("evt_%d", counter)
}

func event(kind EventKind) PaymentEvent {
	if kind == RefundRequested {
		return NewRefundRequested(newID(), OfMinor(100, USD))
	}
	return NewEvent(newID(), kind, "")
}

func mustApply(t *testing.T, p *Payment, e PaymentEvent, reason string) {
	t.Helper()
	if _, err := p.Apply(e, reason); err != nil {
		t.Fatalf("apply %s: %v", e.Kind, err)
	}
}

// paymentIn drives a fresh 100.00 USD payment into the requested state along the canonical path.
func paymentIn(t *testing.T, target PaymentState, clock Clock) *Payment {
	t.Helper()
	p := must(NewPayment("pay_t", OfMinor(10_000, USD), clock))
	switch target {
	case Processing:
		mustApply(t, p, event(Confirm), "t")
	case RequiresAction:
		mustApply(t, p, event(Confirm), "t")
		mustApply(t, p, event(RequireAction), "t")
	case Succeeded:
		mustApply(t, p, event(Confirm), "t")
		mustApply(t, p, event(ProcessorApproved), "t")
	case Failed:
		mustApply(t, p, event(Confirm), "t")
		mustApply(t, p, event(ProcessorDeclined), "t")
	case Canceled:
		mustApply(t, p, event(Cancel), "t")
	case Refunding:
		mustApply(t, p, event(Confirm), "t")
		mustApply(t, p, event(ProcessorApproved), "t")
		mustApply(t, p, NewRefundRequested(newID(), OfMinor(2_500, USD)), "t")
	case Refunded:
		mustApply(t, p, event(Confirm), "t")
		mustApply(t, p, event(ProcessorApproved), "t")
		mustApply(t, p, NewRefundRequested(newID(), OfMinor(10_000, USD)), "t")
		mustApply(t, p, event(RefundSucceeded), "t")
	}
	return p
}

// --- transition table ------------------------------------------------------------------------

func TestTableHasElevenRules(t *testing.T) {
	if len(Rules) != 11 {
		t.Fatalf("expected 11 rules, got %d", len(Rules))
	}
}

func TestEveryRuleMovesAsWritten(t *testing.T) {
	clock := NewFakeClock(1_000)
	for _, r := range Rules {
		var p *Payment
		var e PaymentEvent
		if r.Guard == RefundClearsBalance {
			p = paymentIn(t, Succeeded, clock)
			mustApply(t, p, NewRefundRequested(newID(), OfMinor(10_000, USD)), "full")
			e = event(RefundSucceeded)
		} else {
			p = paymentIn(t, r.From, clock)
			e = event(r.Kind)
		}
		before := p.Version
		outcome, err := p.Apply(e, "legal")
		if err != nil || outcome != Applied {
			t.Fatalf("rule %v: outcome %v err %v", r, outcome, err)
		}
		if p.State != r.To || p.Version != before+1 {
			t.Fatalf("rule %v: state %s version %d", r, p.State, p.Version)
		}
	}
}

func TestEveryPairOutsideTheTableIsRefusedUntouched(t *testing.T) {
	clock := NewFakeClock(1_000)
	illegal := 0
	for _, from := range AllStates {
		for _, kind := range AllKinds {
			if HasRow(from, kind) {
				continue
			}
			p := paymentIn(t, from, clock)
			state, version := p.State, p.Version
			e := event(kind)
			if p.CanApply(e) {
				t.Fatalf("%s + %s should not be applicable", from, kind)
			}
			_, err := p.Apply(e, "illegal")
			if !errors.Is(err, ErrIllegalTransition) {
				t.Fatalf("%s + %s: expected ErrIllegalTransition, got %v", from, kind, err)
			}
			if p.State != state || p.Version != version || len(p.Uncommitted()) != version {
				t.Fatalf("%s + %s mutated the payment", from, kind)
			}
			illegal++
		}
	}
	if illegal != 62 {
		t.Fatalf("8 states x 9 kinds - 10 legal pairs = 62, got %d", illegal)
	}
}

func TestTerminalStates(t *testing.T) {
	for _, s := range AllStates {
		want := s == Refunded || s == Failed || s == Canceled
		if s.IsTerminal() != want {
			t.Fatalf("%s terminal = %v", s, s.IsTerminal())
		}
	}
	if HasRow(Processing, Cancel) {
		t.Fatal("cancel must not be legal while the processor is working")
	}
}

// --- refund guards -----------------------------------------------------------------------------

func TestOverBalanceZeroAndWrongCurrencyAreRefused(t *testing.T) {
	p := paymentIn(t, Succeeded, NewFakeClock(1_000))
	if _, err := p.Apply(NewRefundRequested(newID(), OfMinor(10_001, USD)), "x"); !errors.Is(err, ErrIllegalTransition) {
		t.Fatalf("over balance: %v", err)
	}
	if _, err := p.Apply(NewRefundRequested(newID(), OfMinor(0, USD)), "x"); !errors.Is(err, ErrIllegalTransition) {
		t.Fatalf("zero: %v", err)
	}
	if _, err := p.Apply(NewRefundRequested(newID(), OfMinor(1, EUR)), "x"); !errors.Is(err, ErrCurrencyMismatch) {
		t.Fatalf("currency: %v", err)
	}
	if p.State != Succeeded || p.Version != 2 {
		t.Fatalf("refused events mutated the payment: %s v%d", p.State, p.Version)
	}
}

func TestPartialThenFailedThenFull(t *testing.T) {
	p := paymentIn(t, Succeeded, NewFakeClock(1_000))
	mustApply(t, p, NewRefundRequested(newID(), OfMinor(4_000, USD)), "partial")
	if p.PendingRefund != OfMinor(4_000, USD) || p.Refundable() != OfMinor(6_000, USD) {
		t.Fatalf("pending %s refundable %s", p.PendingRefund, p.Refundable())
	}
	if _, err := p.Apply(NewRefundRequested(newID(), OfMinor(1, USD)), "second while pending"); !errors.Is(err, ErrIllegalTransition) {
		t.Fatalf("second refund while pending: %v", err)
	}
	mustApply(t, p, event(RefundSucceeded), "settled")
	if p.State != Succeeded || p.Refunded != OfMinor(4_000, USD) || !p.PendingRefund.IsZero() {
		t.Fatalf("after partial settle: %s refunded %s pending %s", p.State, p.Refunded, p.PendingRefund)
	}
	mustApply(t, p, NewRefundRequested(newID(), OfMinor(6_000, USD)), "rest")
	mustApply(t, p, event(RefundFailed), "processor refused")
	if p.State != Succeeded || p.Refundable() != OfMinor(6_000, USD) {
		t.Fatalf("after refund failure: %s refundable %s", p.State, p.Refundable())
	}
	mustApply(t, p, NewRefundRequested(newID(), OfMinor(6_000, USD)), "rest again")
	mustApply(t, p, event(RefundSucceeded), "settled")
	if p.State != Refunded || !p.Refundable().IsZero() || p.Refunded != p.Amount {
		t.Fatalf("after full: %s refunded %s", p.State, p.Refunded)
	}
	if _, err := p.Apply(NewRefundRequested(newID(), OfMinor(1, USD)), "x"); !errors.Is(err, ErrIllegalTransition) {
		t.Fatalf("refund after REFUNDED: %v", err)
	}
}

func TestEventAndPaymentValidation(t *testing.T) {
	p := paymentIn(t, Succeeded, NewFakeClock(1_000))
	if _, err := p.Apply(PaymentEvent{EventID: "e", Kind: RefundRequested}, "x"); err == nil {
		t.Fatal("refund request without an amount must be rejected")
	}
	amt := OfMinor(1, USD)
	if _, err := p.Apply(PaymentEvent{EventID: "e", Kind: Confirm, Amount: &amt}, "x"); err == nil {
		t.Fatal("CONFIRM with an amount must be rejected")
	}
	if _, err := NewPayment("p", OfMinor(0, USD), NewFakeClock(0)); err == nil {
		t.Fatal("zero amount payment must be rejected")
	}
}

// --- history and replay ------------------------------------------------------------------------

func loggedPayment(t *testing.T) (*Payment, *FakeClock) {
	t.Helper()
	clock := NewFakeClock(1_000)
	p := must(NewPayment("pay_log", OfMinor(4_999, USD), clock))
	mustApply(t, p, NewEvent("e1", Confirm, ""), "confirmed")
	clock.Advance(500)
	mustApply(t, p, NewEvent("e2", RequireAction, ""), "3ds")
	mustApply(t, p, NewEvent("e3", ActionCompleted, ""), "challenge done")
	mustApply(t, p, NewEvent("e4", ProcessorApproved, ""), "auth 00")
	mustApply(t, p, NewRefundRequested("e5", OfMinor(1_500, USD)), "partial")
	return p, clock
}

func TestLogIsCompleteContiguousAndChained(t *testing.T) {
	p, _ := loggedPayment(t)
	log := p.History()
	if len(log) != 5 || p.Version != 5 {
		t.Fatalf("len %d version %d", len(log), p.Version)
	}
	for i, tr := range log {
		if tr.Version != i+1 {
			t.Fatalf("entry %d has version %d", i, tr.Version)
		}
		if i > 0 && tr.FromState != log[i-1].ToState {
			t.Fatalf("entry %d is not chained", i)
		}
	}
	if log[0].FromState != Created || log[4].ToState != p.State {
		t.Fatal("chain endpoints wrong")
	}
	if log[1].Reason != "3ds" || log[4].EventID != "e5" || *log[4].Amount != OfMinor(1_500, USD) {
		t.Fatal("transition payload wrong")
	}
	if log[0].AtMillis != 1_000 || log[1].AtMillis != 1_500 {
		t.Fatalf("timestamps %d %d", log[0].AtMillis, log[1].AtMillis)
	}
}

func TestDuplicateEventIDIsANoop(t *testing.T) {
	p, _ := loggedPayment(t)
	outcome, err := p.Apply(NewEvent("e4", ProcessorApproved, ""), "redelivery")
	if err != nil || outcome != Duplicate {
		t.Fatalf("outcome %v err %v", outcome, err)
	}
	if p.Version != 5 || p.State != Refunding {
		t.Fatalf("duplicate mutated: %s v%d", p.State, p.Version)
	}
	if p.CanApply(NewEvent("e99", ProcessorApproved, "")) {
		t.Fatal("PROCESSOR_APPROVED must not be applicable from REFUNDING")
	}
}

func TestReplayReproducesThePayment(t *testing.T) {
	p, clock := loggedPayment(t)
	rebuilt, err := Replay("pay_log", OfMinor(4_999, USD), p.History(), clock)
	if err != nil {
		t.Fatal(err)
	}
	if rebuilt.State != p.State || rebuilt.Version != 5 || rebuilt.PendingRefund != OfMinor(1_500, USD) {
		t.Fatalf("rebuilt %s v%d pending %s", rebuilt.State, rebuilt.Version, rebuilt.PendingRefund)
	}
	if outcome, _ := rebuilt.Apply(NewEvent("e4", ProcessorApproved, ""), "x"); outcome != Duplicate {
		t.Fatal("replayed payment must remember applied event ids")
	}
	if len(rebuilt.Uncommitted()) != 0 {
		t.Fatal("replay must not produce uncommitted transitions")
	}
}

func TestReplayDetectsCorruption(t *testing.T) {
	p, clock := loggedPayment(t)
	log := p.History()
	gap := append(append([]Transition{}, log[:2]...), log[3:]...)
	if _, err := Replay("pay_log", OfMinor(4_999, USD), gap, clock); err == nil {
		t.Fatal("history gap must be detected")
	}
	swapped := append([]Transition{}, log...)
	swapped[1] = Transition{"pay_log", 2, "e2", RequireAction, Succeeded, RequiresAction, nil, "forged", 0}
	if _, err := Replay("pay_log", OfMinor(4_999, USD), swapped, clock); err == nil {
		t.Fatal("mismatched fromState must be detected")
	}
}

// --- store and optimistic concurrency ----------------------------------------------------------

func newStore(t *testing.T) (*PaymentStore, *Payment) {
	t.Helper()
	store := NewPaymentStore(NewFakeClock(0))
	a := must(store.Create("pay_s", OfMinor(2_500, USD)))
	mustApply(t, a, NewEvent("s1", Confirm, ""), "c")
	if err := store.Save(a); err != nil {
		t.Fatal(err)
	}
	return store, a
}

func TestSaveLoadRoundTrip(t *testing.T) {
	store, a := newStore(t)
	if len(a.Uncommitted()) != 0 || store.CurrentVersion("pay_s") != 1 {
		t.Fatal("save did not commit")
	}
	loaded := must(store.Load("pay_s"))
	if loaded.State != Processing || loaded.Version != 1 {
		t.Fatalf("loaded %s v%d", loaded.State, loaded.Version)
	}
	if must(store.Load("pay_s")) == must(store.Load("pay_s")) {
		t.Fatal("each load must return an independent copy")
	}
}

func TestOptimisticConcurrencyRejectsTheSecondWriter(t *testing.T) {
	store, _ := newStore(t)
	h1 := must(store.Load("pay_s"))
	h2 := must(store.Load("pay_s"))
	mustApply(t, h1, NewEvent("s2", ProcessorApproved, ""), "approved")
	mustApply(t, h2, NewEvent("s3", ProcessorDeclined, ""), "declined")
	if err := store.Save(h1); err != nil {
		t.Fatal(err)
	}
	if err := store.Save(h2); !errors.Is(err, ErrConcurrencyConflict) {
		t.Fatalf("second writer: %v", err)
	}
	if store.CurrentVersion("pay_s") != 2 || must(store.Load("pay_s")).State != Succeeded {
		t.Fatal("store must hold exactly the first writer's transition")
	}
	if must(store.Load("pay_s")).CanApply(NewEvent("s4", ProcessorDeclined, "")) {
		t.Fatal("after reload the loser's event must be illegal")
	}
}

func TestMultipleUncommittedSaveAtomically(t *testing.T) {
	store, _ := newStore(t)
	h := must(store.Load("pay_s"))
	mustApply(t, h, NewEvent("s2", ProcessorApproved, ""), "approved")
	mustApply(t, h, NewRefundRequested("s5", OfMinor(500, USD)), "r")
	mustApply(t, h, NewEvent("s6", RefundSucceeded, ""), "ok")
	if err := store.Save(h); err != nil {
		t.Fatal(err)
	}
	if store.CurrentVersion("pay_s") != 4 || must(store.Load("pay_s")).Refunded != OfMinor(500, USD) {
		t.Fatal("batch save did not land")
	}
}

func TestUnknownAndDuplicateIDs(t *testing.T) {
	store, _ := newStore(t)
	if _, err := store.Create("pay_s", OfMinor(1, USD)); err == nil {
		t.Fatal("duplicate id must be rejected")
	}
	if _, err := store.Load("nope"); err == nil {
		t.Fatal("unknown id must be rejected")
	}
}

// --- service and processor ---------------------------------------------------------------------

func newService() (*FakeClock, *PaymentStore, *FakeProcessor, *PaymentService) {
	clock := NewFakeClock(0)
	store := NewPaymentStore(clock)
	processor := NewFakeProcessor(clock, 1_000)
	return clock, store, processor, NewPaymentService(store, processor)
}

func stateOf(t *testing.T, store *PaymentStore, id string) PaymentState {
	t.Helper()
	return must(store.Load(id)).State
}

func TestThreeDSFlowIsDrivenByTheClock(t *testing.T) {
	clock, store, processor, svc := newService()
	processor.Script("pay_v", OutcomeChallenge, OutcomeApproved)
	must(svc.Create("pay_v", OfMinor(4_999, USD)))
	must(svc.Confirm("pay_v", "c1"))
	if stateOf(t, store, "pay_v") != Processing || len(processor.Calls) != 1 {
		t.Fatal("confirm must persist PROCESSING and call the processor once")
	}
	if must(svc.Pump()) != 0 {
		t.Fatal("nothing is due before latency elapses")
	}
	clock.Advance(999)
	if must(svc.Pump()) != 0 {
		t.Fatal("nothing is due at 999ms")
	}
	clock.Advance(1)
	if must(svc.Pump()) != 1 || stateOf(t, store, "pay_v") != RequiresAction {
		t.Fatal("challenge callback must land at exactly the latency")
	}
	must(svc.CompleteAction("pay_v", "a1"))
	clock.Advance(1_000)
	if must(svc.Pump()) != 1 || stateOf(t, store, "pay_v") != Succeeded || store.CurrentVersion("pay_v") != 4 {
		t.Fatal("approval must land as v4")
	}
	again := ProcessorCallback{"pr_0002", "pay_v", OutcomeApproved, "approved", 0}
	if must(svc.Deliver(again)) != Duplicate || store.CurrentVersion("pay_v") != 4 {
		t.Fatal("redelivered callback must be a no-op")
	}
}

func TestRefundFailureThenFullRefund(t *testing.T) {
	clock, store, processor, svc := newService()
	must(svc.Create("pay_v", OfMinor(4_999, USD)))
	must(svc.Confirm("pay_v", "c1"))
	clock.Advance(1_000)
	must(svc.Pump())
	processor.Script("pay_v", OutcomeRefundFailed)
	must(svc.RequestRefund("pay_v", "r1", OfMinor(999, USD)))
	clock.Advance(1_000)
	must(svc.Pump())
	if stateOf(t, store, "pay_v") != Succeeded || must(store.Load("pay_v")).Refundable() != OfMinor(4_999, USD) {
		t.Fatal("failed refund must restore the full refundable balance")
	}
	must(svc.RequestRefund("pay_v", "r2", OfMinor(4_999, USD)))
	clock.Advance(1_000)
	must(svc.Pump())
	if stateOf(t, store, "pay_v") != Refunded {
		t.Fatal("full refund must reach REFUNDED")
	}
	if _, err := svc.RequestRefund("pay_v", "r3", OfMinor(1, USD)); !errors.Is(err, ErrIllegalTransition) {
		t.Fatalf("refund after REFUNDED: %v", err)
	}
	if len(processor.Calls) != 3 {
		t.Fatalf("processor calls = %d", len(processor.Calls))
	}
}

func TestDeclineAndCancelInProcessing(t *testing.T) {
	clock, store, processor, svc := newService()
	processor.Script("pay_d", OutcomeDeclined)
	must(svc.Create("pay_d", OfMinor(100, USD)))
	must(svc.Confirm("pay_d", "d1"))
	clock.Advance(1_000)
	must(svc.Pump())
	if stateOf(t, store, "pay_d") != Failed {
		t.Fatal("decline must reach FAILED")
	}
	must(svc.Create("pay_c", OfMinor(100, USD)))
	must(svc.Confirm("pay_c", "cc1"))
	if _, err := svc.Cancel("pay_c", "cc2"); !errors.Is(err, ErrIllegalTransition) {
		t.Fatalf("cancel while processing: %v", err)
	}
	if stateOf(t, store, "pay_c") != Processing {
		t.Fatal("refused cancel must leave PROCESSING")
	}
}
