// Tests for phases/11-payment-rails/02-card-authorization-lifecycle. Run: go test ./...
package main

import (
	"errors"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)

func usd(t *testing.T, s string) Money {
	t.Helper()
	m, err := Parse(s, USD)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return m
}

func auth(t *testing.T, amount string, clock Clock, policy LifecyclePolicy) *CardTransaction {
	t.Helper()
	if clock == nil {
		clock = NewFakeClock(t0)
	}
	txn, err := Authorize("t", usd(t, amount), clock, policy, "test authorization")
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	return txn
}

func wantIllegal(t *testing.T, name string, err error) {
	t.Helper()
	if !errors.Is(err, ErrIllegalTransition) {
		t.Errorf("%s: want IllegalTransitionError, got %v", name, err)
	}
}

func wantLimit(t *testing.T, name string, err error) {
	t.Helper()
	if !errors.Is(err, ErrLimitExceeded) {
		t.Errorf("%s: want ErrLimitExceeded, got %v", name, err)
	}
}

func TestAuthorizeCreatesAuthorizedWithOneEvent(t *testing.T) {
	txn := auth(t, "100.00", nil, Standard)
	if txn.State() != Authorized {
		t.Fatalf("state = %v", txn.State())
	}
	ev := txn.Events()
	if len(ev) != 1 || ev[0].Action != ActAuthorize || ev[0].From != nil || ev[0].Reason != "test authorization" {
		t.Fatalf("first event = %+v", ev)
	}
	if !txn.ExpiresAt().Equal(t0.Add(7 * 24 * time.Hour)) {
		t.Fatalf("expiresAt = %v", txn.ExpiresAt())
	}
	if !txn.CapturedAmount().IsZero() || txn.RemainingAuthorization() != usd(t, "100.00") {
		t.Fatalf("captured=%v remaining=%v", txn.CapturedAmount(), txn.RemainingAuthorization())
	}
}

func TestAuthorizeRejectsZeroAmountAndBlankReason(t *testing.T) {
	_, err := Authorize("z", usd(t, "0.00"), NewFakeClock(t0), Standard, "r")
	wantLimit(t, "zero amount", err)
	if _, err := Authorize("z", usd(t, "1.00"), NewFakeClock(t0), Standard, " "); err == nil {
		t.Fatal("blank reason accepted")
	}
}

func TestFullCapture(t *testing.T) {
	txn := auth(t, "100.00", nil, Standard)
	if err := txn.Capture(usd(t, "100.00"), "ship"); err != nil {
		t.Fatal(err)
	}
	if txn.State() != Captured || txn.CapturedAmount() != usd(t, "100.00") || !txn.RemainingAuthorization().IsZero() {
		t.Fatalf("state=%v captured=%v remaining=%v", txn.State(), txn.CapturedAmount(), txn.RemainingAuthorization())
	}
}

func TestPartialThenRest(t *testing.T) {
	txn := auth(t, "100.00", nil, Standard)
	if err := txn.Capture(usd(t, "40.00"), "first shipment"); err != nil {
		t.Fatal(err)
	}
	if txn.State() != PartiallyCaptured || txn.RemainingAuthorization() != usd(t, "60.00") {
		t.Fatalf("state=%v remaining=%v", txn.State(), txn.RemainingAuthorization())
	}
	if err := txn.Capture(usd(t, "60.00"), "second shipment"); err != nil {
		t.Fatal(err)
	}
	if txn.State() != Captured {
		t.Fatalf("state=%v", txn.State())
	}
}

func TestPartialThenCloseReleasesRemainder(t *testing.T) {
	txn := auth(t, "100.00", nil, Standard)
	check(txn.Capture(usd(t, "35.50"), "only item in stock"))
	check(txn.CloseCapture("rest cancelled"))
	ev := txn.Events()
	if txn.State() != Captured || ev[2].Action != ActCloseCapture || ev[2].Amount != usd(t, "64.50") {
		t.Fatalf("state=%v event=%v", txn.State(), ev[2])
	}
	if txn.CapturedAmount() != usd(t, "35.50") || !txn.RemainingAuthorization().IsZero() {
		t.Fatalf("captured=%v remaining=%v", txn.CapturedAmount(), txn.RemainingAuthorization())
	}
}

func TestVoid(t *testing.T) {
	txn := auth(t, "25.00", nil, Standard)
	check(txn.VoidAuthorization("customer cancelled"))
	if txn.State() != Voided || !txn.State().IsTerminal() {
		t.Fatalf("state=%v", txn.State())
	}
}

func TestRefundArithmetic(t *testing.T) {
	txn := auth(t, "100.00", nil, Standard)
	check(txn.Capture(usd(t, "60.00"), "ship"))
	check(txn.CloseCapture("done"))
	check(txn.Refund(usd(t, "25.00"), "one item returned"))
	if txn.State() != PartiallyRefunded || txn.NetSettled() != usd(t, "35.00") {
		t.Fatalf("state=%v net=%v", txn.State(), txn.NetSettled())
	}
	check(txn.Refund(usd(t, "35.00"), "second item returned"))
	if txn.State() != Refunded || !txn.NetSettled().IsZero() {
		t.Fatalf("state=%v net=%v", txn.State(), txn.NetSettled())
	}
	wantIllegal(t, "refund after REFUNDED", txn.Refund(usd(t, "0.01"), "again"))
}

func TestRefundAboveCapturedRejectedAndLeavesState(t *testing.T) {
	txn := auth(t, "100.00", nil, Standard)
	check(txn.Capture(usd(t, "100.00"), "ship"))
	wantLimit(t, "refund above captured", txn.Refund(usd(t, "100.01"), "too much"))
	if txn.State() != Captured || !txn.RefundedAmount().IsZero() {
		t.Fatalf("state=%v refunded=%v", txn.State(), txn.RefundedAmount())
	}
	wantLimit(t, "refund of zero", txn.Refund(usd(t, "0.00"), "zero"))
}

func TestCaptureCeilingWithoutTolerance(t *testing.T) {
	txn := auth(t, "100.00", nil, Standard)
	wantLimit(t, "capture above authorized", txn.Capture(usd(t, "100.01"), "tip"))
	if txn.State() != Authorized || len(txn.Events()) != 1 {
		t.Fatalf("failed capture changed state: %v / %d events", txn.State(), len(txn.Events()))
	}
}

func TestCaptureCeilingWithTolerance(t *testing.T) {
	tips := PolicyForMCC("5812")
	if tips.CaptureCeiling(usd(t, "100.00")) != usd(t, "115.00") {
		t.Fatalf("ceiling = %v", tips.CaptureCeiling(usd(t, "100.00")))
	}
	if tips.CaptureCeiling(usd(t, "0.33")) != usd(t, "0.37") { // 37.95 cents rounds DOWN
		t.Fatalf("ceiling rounding = %v", tips.CaptureCeiling(usd(t, "0.33")))
	}
	if PolicyForMCC("5999").OverCaptureToleranceBps != 0 {
		t.Fatal("unknown MCC should get the standard policy")
	}
	ok := auth(t, "100.00", nil, tips)
	check(ok.Capture(usd(t, "115.00"), "bill plus tip"))
	if ok.State() != Captured || ok.CapturedAmount() != usd(t, "115.00") {
		t.Fatalf("state=%v captured=%v", ok.State(), ok.CapturedAmount())
	}
	tooMuch := auth(t, "100.00", nil, tips)
	wantLimit(t, "above tolerance", tooMuch.Capture(usd(t, "115.01"), "tip"))
	cumulative := auth(t, "100.00", nil, tips)
	check(cumulative.Capture(usd(t, "90.00"), "part"))
	wantLimit(t, "cumulative above ceiling", cumulative.Capture(usd(t, "25.01"), "part two"))
}

func TestIncrementalAuthorization(t *testing.T) {
	clock := NewFakeClock(t0)
	txn := auth(t, "50.00", clock, Standard)
	clock.Advance(2 * 24 * time.Hour)
	check(txn.IncrementAuthorization(usd(t, "20.00"), "extended stay"))
	if txn.AuthorizedAmount() != usd(t, "70.00") || txn.State() != Authorized {
		t.Fatalf("authorized=%v state=%v", txn.AuthorizedAmount(), txn.State())
	}
	if !txn.ExpiresAt().Equal(t0.Add(9 * 24 * time.Hour)) {
		t.Fatalf("expiry not reset: %v", txn.ExpiresAt())
	}
	check(txn.Capture(usd(t, "70.00"), "checkout"))
	if txn.State() != Captured {
		t.Fatalf("state=%v", txn.State())
	}
	partial := auth(t, "10.00", nil, Standard)
	check(partial.Capture(usd(t, "4.00"), "part"))
	wantIllegal(t, "increment after partial capture", partial.IncrementAuthorization(usd(t, "1.00"), "late"))
}

func TestEachForbiddenEdge(t *testing.T) {
	full := auth(t, "100.00", nil, Standard)
	check(full.Capture(usd(t, "100.00"), "ship"))
	voided := auth(t, "25.00", nil, Standard)
	check(voided.VoidAuthorization("cancel"))
	fresh := auth(t, "10.00", nil, Standard)
	partial := auth(t, "10.00", nil, Standard)
	check(partial.Capture(usd(t, "4.00"), "part"))

	wantIllegal(t, "void after capture", full.VoidAuthorization("late"))
	wantIllegal(t, "capture after void", voided.Capture(usd(t, "1.00"), "late"))
	wantIllegal(t, "refund from AUTHORIZED", fresh.Refund(usd(t, "1.00"), "early"))
	wantIllegal(t, "refund from PARTIALLY_CAPTURED", partial.Refund(usd(t, "1.00"), "early"))
	wantIllegal(t, "close capture from AUTHORIZED", fresh.CloseCapture("nothing"))
	wantIllegal(t, "capture after full capture", full.Capture(usd(t, "1.00"), "again"))
	wantIllegal(t, "void after void", voided.VoidAuthorization("again"))

	var ite *IllegalTransitionError
	if err := full.VoidAuthorization("late"); !errors.As(err, &ite) || ite.From != Captured || ite.Action != ActVoid {
		t.Fatalf("error does not carry from/action: %v", err)
	}
	if err := fresh.Capture(must(Parse("1.00", EUR)), "wrong currency"); !errors.Is(err, ErrCurrencyMismatch) {
		t.Fatalf("want currency mismatch, got %v", err)
	}
	for _, s := range []TransactionState{Voided, Expired, Refunded} {
		if len(Legal[s]) != 0 || !s.IsTerminal() {
			t.Fatalf("terminal state %v accepts actions", s)
		}
	}
}

func TestExpiryViaInjectedClock(t *testing.T) {
	clock := NewFakeClock(t0)
	txn := auth(t, "45.00", clock, Standard)
	clock.Advance(7*24*time.Hour - time.Second)
	if txn.ExpireIfDue("probe") || txn.State() != Authorized {
		t.Fatal("expired one second early")
	}
	clock.Advance(time.Second)
	wantIllegal(t, "capture at deadline", txn.Capture(usd(t, "45.00"), "late shipment"))
	if txn.State() != Expired {
		t.Fatalf("state=%v", txn.State())
	}
	ev := txn.Events()
	if len(ev) != 2 || ev[1].Action != ActExpire || ev[1].Amount != usd(t, "45.00") || !strings.Contains(ev[1].Reason, "late shipment") {
		t.Fatalf("expiry event = %+v", ev)
	}
	wantIllegal(t, "void after expiry", txn.VoidAuthorization("late"))
	if txn.ExpireIfDue("probe") || len(txn.Events()) != 2 {
		t.Fatal("ExpireIfDue not idempotent on terminal state")
	}
}

func TestExpiredPartialCaptureLandsInCapturedAndStaysRefundable(t *testing.T) {
	clock := NewFakeClock(t0)
	txn := auth(t, "100.00", clock, Standard)
	check(txn.Capture(usd(t, "30.00"), "first"))
	clock.Advance(8 * 24 * time.Hour)
	wantIllegal(t, "second capture after expiry", txn.Capture(usd(t, "70.00"), "second"))
	if txn.State() != Captured {
		t.Fatalf("state=%v, want Captured (captured funds must stay refundable)", txn.State())
	}
	expiry := txn.Events()[2]
	if expiry.Action != ActExpire || expiry.Amount != usd(t, "70.00") || expiry.To != Captured {
		t.Fatalf("expiry event = %+v", expiry)
	}
	if txn.CapturedAmount() != usd(t, "30.00") || !txn.RemainingAuthorization().IsZero() {
		t.Fatalf("captured=%v remaining=%v", txn.CapturedAmount(), txn.RemainingAuthorization())
	}
	check(txn.Refund(usd(t, "30.00"), "customer returned the shipped item"))
	if txn.State() != Refunded || !txn.NetSettled().IsZero() {
		t.Fatalf("state=%v net=%v", txn.State(), txn.NetSettled())
	}
}

func TestConfigurableValidity(t *testing.T) {
	clock := NewFakeClock(t0)
	policy, err := NewLifecyclePolicy(time.Hour, 0)
	if err != nil {
		t.Fatal(err)
	}
	txn := auth(t, "10.00", clock, policy)
	clock.Advance(59 * time.Minute)
	check(txn.Capture(usd(t, "10.00"), "in time"))
	if txn.State() != Captured {
		t.Fatalf("state=%v", txn.State())
	}
}

func TestEventLogReplaysToTotals(t *testing.T) {
	txn := auth(t, "120.00", nil, Standard)
	check(txn.IncrementAuthorization(usd(t, "30.00"), "minibar"))
	check(txn.Capture(usd(t, "100.00"), "folio"))
	check(txn.CloseCapture("checkout"))
	check(txn.Refund(usd(t, "20.00"), "goodwill"))
	log := txn.Events()
	if len(log) != 5 || log[0].Sequence != 1 || log[4].Sequence != 5 {
		t.Fatalf("log = %v", log)
	}
	want := []TransactionState{Authorized, Authorized, PartiallyCaptured, Captured, PartiallyRefunded}
	for i, e := range log {
		if e.To != want[i] {
			t.Fatalf("event %d to=%v want %v", i, e.To, want[i])
		}
	}
	if sum, _ := log[0].Amount.Plus(log[1].Amount); sum != txn.AuthorizedAmount() {
		t.Fatalf("authorized does not replay: %v", sum)
	}
	if log[2].Amount != txn.CapturedAmount() || log[4].Amount != txn.RefundedAmount() {
		t.Fatal("captured/refunded do not replay")
	}
	log[0].Reason = "tampered"
	if txn.Events()[0].Reason == "tampered" {
		t.Fatal("Events() must return a copy")
	}
}

func TestPolicyValidation(t *testing.T) {
	if _, err := NewLifecyclePolicy(0, 0); err == nil {
		t.Fatal("zero validity accepted")
	}
	if _, err := NewLifecyclePolicy(24*time.Hour, 10_001); err == nil {
		t.Fatal("tolerance above 100% accepted")
	}
}
