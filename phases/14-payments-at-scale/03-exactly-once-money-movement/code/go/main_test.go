// Tests for phases/14-payments-at-scale/03-exactly-once-money-movement. Run: go test ./...
// Same test cases as MainTest.java and test_main.py: every fault scenario leaves exactly one posting and conserved money.
package main

import (
	"errors"
	"strings"
	"testing"
)

func request(key string, cents int64) PaymentRequest {
	return PaymentRequest{key, "cust-1", "merch-1", OfMinor(cents, USD)}
}

// pipeline wires a full pipeline whose only fault is the given point, firing every time until disarmed.
type pipeline struct {
	faults     *FaultInjector
	gateway    *GatewayStore
	api        *PaymentApi
	bus        *Bus
	relay      *OutboxRelay
	downstream *LedgerStore
	consumer   *LedgerConsumer
}

func alwaysFailingAt(point FaultPoint, defences Defences) *pipeline {
	faults := NewFaultInjector(1, map[FaultPoint]float64{point: 1.0})
	gateway := NewGatewayStore()
	bus := NewBus(faults)
	downstream := NewLedgerStore()
	return &pipeline{faults, gateway, NewPaymentApi(gateway, faults, defences), bus,
		NewOutboxRelay(gateway, bus, faults), downstream, NewLedgerConsumer(downstream, faults, defences)}
}

// drainAndCount drains relay + bus with faults off, then returns how many times the one event was posted downstream.
func (p *pipeline) drainAndCount(t *testing.T, eventID string) int {
	p.faults.Disarm()
	for len(p.gateway.Unpublished()) > 0 || p.bus.Pending() > 0 {
		if _, err := p.relay.RelayOnce(); err != nil {
			t.Fatalf("relay failed while disarmed: %v", err)
		}
		if _, err := p.bus.Deliver(p.consumer.Handle, 64); err != nil {
			t.Fatalf("deliver failed while disarmed: %v", err)
		}
	}
	return p.downstream.Ledger.CountEntries(eventID)
}

func TestMoneyDiscipline(t *testing.T) {
	sum, err := OfMinor(1999, USD).Plus(OfMinor(1, USD))
	if err != nil || sum.MinorUnits != 2000 {
		t.Fatalf("plus: %v %v", sum, err)
	}
	if OfMinor(1999, USD).String() != "19.99 USD" {
		t.Fatalf("String: %s", OfMinor(1999, USD))
	}
	if _, err := OfMinor(1, USD).Plus(OfMinor(1, EUR)); !errors.Is(err, ErrCurrencyMismatch) {
		t.Fatalf("expected currency mismatch, got %v", err)
	}
}

func TestLedgerBalancesAndNoDedupe(t *testing.T) {
	ledger := NewLedger(USD)
	entry, _ := Transfer("e1", "asset:a", "liability:b", OfMinor(500, USD))
	if err := ledger.Post(entry); err != nil {
		t.Fatal(err)
	}
	if !ledger.TrialBalance().IsZero() {
		t.Fatalf("trial balance %v", ledger.TrialBalance())
	}
	if ledger.Balance("asset:a").MinorUnits != 500 || ledger.Balance("liability:b").MinorUnits != -500 {
		t.Fatal("balances not signed as expected")
	}
	if ledger.CreditTotal("liability:").MinorUnits != 500 {
		t.Fatalf("creditTotal %v", ledger.CreditTotal("liability:"))
	}
	if _, err := Transfer("e2", "a", "b", Zero(USD)); err == nil {
		t.Fatal("non-positive transfer accepted")
	}
	ledger.Post(entry)
	if ledger.CountEntries("e1") != 2 {
		t.Fatal("the ledger itself must not dedupe; that is the layer in front")
	}
}

func TestIdempotentApiHappyPath(t *testing.T) {
	store := NewGatewayStore()
	api := NewPaymentApi(store, NoFaults(), AllDefences())
	first, _ := api.CreatePayment(request("k1", 1000))
	again, _ := api.CreatePayment(request("k1", 1000))
	if first.Replayed || !again.Replayed || again.Payment.PaymentID != first.Payment.PaymentID {
		t.Fatalf("replay semantics wrong: %+v %+v", first, again)
	}
	if len(store.Payments) != 1 || len(store.IdempotencyKeys) != 1 || len(store.Outbox) != 1 || len(store.Ledger.Journal) != 1 {
		t.Fatal("expected one payment, one key, one outbox row, one ledger entry")
	}
	if store.Ledger.Balance(Receivable+"cust-1").MinorUnits != 1000 || store.Ledger.Balance(GatewayClearing).MinorUnits != -1000 {
		t.Fatal("gateway ledger must debit receivable and credit clearing")
	}
	if _, err := api.CreatePayment(request("k1", 1001)); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("expected conflict, got %v", err)
	}
	if len(store.Payments) != 1 || store.Commits != 1 {
		t.Fatal("conflict must write nothing")
	}
}

func TestCrashBeforeCommitWritesNothing(t *testing.T) {
	p := alwaysFailingAt(ApiBeforeCommit, AllDefences())
	if _, err := p.api.CreatePayment(request("k1", 1000)); !IsCrash(err) {
		t.Fatalf("expected crash, got %v", err)
	}
	if len(p.gateway.Payments) != 0 || len(p.gateway.Outbox) != 0 || len(p.gateway.Ledger.Journal) != 0 {
		t.Fatal("crash before commit must write nothing")
	}
}

func TestCrashAfterCommitRetryPostsOnce(t *testing.T) {
	q := alwaysFailingAt(ApiAfterCommit, AllDefences())
	if _, err := q.api.CreatePayment(request("k1", 1000)); !IsCrash(err) {
		t.Fatalf("expected crash, got %v", err)
	}
	if len(q.gateway.Payments) != 1 || len(q.gateway.Outbox) != 1 || len(q.gateway.Ledger.Journal) != 1 {
		t.Fatal("crash after commit must keep payment, ledger entry, and outbox row together")
	}
	q.faults.Disarm()
	retried, err := q.api.CreatePayment(request("k1", 1000))
	if err != nil || !retried.Replayed || len(q.gateway.Payments) != 1 {
		t.Fatalf("retry after lost response must be a replay: %+v %v", retried, err)
	}
	if n := q.drainAndCount(t, "evt-"+retried.Payment.PaymentID); n != 1 {
		t.Fatalf("posted %d times downstream, want 1", n)
	}
}

func TestRelayCrashAfterPublishIsAbsorbedByInbox(t *testing.T) {
	r := alwaysFailingAt(RelayAfterPublish, AllDefences())
	rp, _ := r.api.CreatePayment(request("k1", 1000))
	if _, err := r.relay.RelayOnce(); !IsCrash(err) {
		t.Fatalf("expected crash, got %v", err)
	}
	if r.bus.Pending() != 1 || len(r.gateway.Unpublished()) != 1 {
		t.Fatal("event should be on the bus with the row still unpublished")
	}
	if n := r.drainAndCount(t, "evt-"+rp.Payment.PaymentID); n != 1 {
		t.Fatalf("posted %d times downstream, want 1", n)
	}
	if r.consumer.SkippedDuplicates != 1 || r.bus.Published != 2 {
		t.Fatalf("consumer should have skipped exactly one duplicate: skipped=%d published=%d", r.consumer.SkippedDuplicates, r.bus.Published)
	}
}

func TestBusDuplicatePostsOnce(t *testing.T) {
	d := alwaysFailingAt(BusDuplicate, AllDefences())
	dp, _ := d.api.CreatePayment(request("k1", 1000))
	d.relay.RelayOnce()
	if d.bus.Pending() != 2 || d.bus.Duplicated != 1 {
		t.Fatal("bus should have duplicated the publish")
	}
	if n := d.drainAndCount(t, "evt-"+dp.Payment.PaymentID); n != 1 {
		t.Fatalf("posted %d times downstream, want 1", n)
	}
}

func TestConsumerCrashBeforeCommitRedeliversAndPostsOnce(t *testing.T) {
	cb := alwaysFailingAt(ConsumerBeforeCommit, AllDefences())
	cbp, _ := cb.api.CreatePayment(request("k1", 1000))
	cb.relay.RelayOnce()
	if _, err := cb.bus.Deliver(cb.consumer.Handle, 8); !IsCrash(err) {
		t.Fatalf("expected crash, got %v", err)
	}
	if cb.bus.Pending() != 1 || cb.bus.Redelivered != 1 || len(cb.downstream.Ledger.Journal) != 0 {
		t.Fatal("message should be put back for redelivery with nothing written")
	}
	if n := cb.drainAndCount(t, "evt-"+cbp.Payment.PaymentID); n != 1 {
		t.Fatalf("posted %d times downstream, want 1", n)
	}
}

func TestConsumerCrashAfterCommitLostAckPostsOnce(t *testing.T) {
	ca := alwaysFailingAt(ConsumerAfterCommit, AllDefences())
	cap, _ := ca.api.CreatePayment(request("k1", 1000))
	ca.relay.RelayOnce()
	if _, err := ca.bus.Deliver(ca.consumer.Handle, 8); !IsCrash(err) {
		t.Fatalf("expected crash, got %v", err)
	}
	if len(ca.downstream.Ledger.Journal) != 1 || ca.bus.Pending() != 1 {
		t.Fatal("committed once, yet the message must be redelivered")
	}
	if n := ca.drainAndCount(t, "evt-"+cap.Payment.PaymentID); n != 1 {
		t.Fatalf("posted %d times downstream, want 1", n)
	}
}

func TestReorderedBatchPostsEveryEventOnce(t *testing.T) {
	ro := alwaysFailingAt(BusReorder, AllDefences())
	for i := int64(0); i < 5; i++ {
		ro.api.CreatePayment(request("k"+string(rune('0'+i)), 100+i))
	}
	ro.relay.RelayOnce()
	ro.bus.Deliver(ro.consumer.Handle, 8)
	if ro.bus.ReorderedBatches != 1 || len(ro.downstream.Ledger.Journal) != 5 {
		t.Fatalf("reordered=%d entries=%d", ro.bus.ReorderedBatches, len(ro.downstream.Ledger.Journal))
	}
	if ro.downstream.Ledger.CreditTotal(Payable).MinorUnits != 510 || ro.downstream.Ledger.Balance(DownstreamClearing).MinorUnits != 510 {
		t.Fatal("downstream totals wrong after reordering")
	}
}

func TestHarnessUniformScheduleHoldsAcrossSeeds(t *testing.T) {
	crashes, duplicates := 0, 0
	for seed := int64(1); seed <= 12; seed++ {
		report := RunHarness(StandardConfig(seed, 1500))
		if !report.Passed() {
			t.Fatalf("seed %d: %v", seed, report.Violations[:min(3, len(report.Violations))])
		}
		crashes += report.Crashes
		duplicates += report.BusDuplicated
	}
	if crashes <= 100 || duplicates <= 10 {
		t.Fatalf("the schedule did not inject enough faults: crashes=%d duplicates=%d", crashes, duplicates)
	}
}

func TestHarnessRandomSchedulesHold(t *testing.T) {
	for seed := int64(100); seed < 120; seed++ {
		if report := RunHarness(RandomScheduleConfig(seed, 1000)); !report.Passed() {
			t.Fatalf("seed %d: %v", seed, report.Violations[:min(3, len(report.Violations))])
		}
	}
}

func TestHarnessReportAndReproducibility(t *testing.T) {
	one := RunHarness(StandardConfig(5, 1500))
	if !one.GatewayTrialBalance.IsZero() || !one.DownstreamTrialBalance.IsZero() {
		t.Fatal("trial balances must be zero on both ledgers")
	}
	if one.GatewayEntries != one.AcceptedPayments || one.DownstreamEntries != one.AcceptedPayments {
		t.Fatal("entry counts must match accepted payments")
	}
	if RunHarness(StandardConfig(5, 1500)).Summary() != one.Summary() {
		t.Fatal("same seed must reproduce the same run")
	}
}

func TestWithoutApiDedupe(t *testing.T) {
	report := RunHarness(StandardConfig(1, 1500).Without("apiDedupe"))
	if report.Passed() || report.FirstBrokenInvariant() != "one-payment-per-key" {
		t.Fatalf("expected one-payment-per-key to break, got %q", report.FirstBrokenInvariant())
	}
}

func TestWithoutAtomicOutbox(t *testing.T) {
	report := RunHarness(StandardConfig(1, 1500).Without("atomicOutbox"))
	if report.Passed() || report.FirstBrokenInvariant() != "payment-has-outbox-row" {
		t.Fatalf("expected payment-has-outbox-row to break, got %q", report.FirstBrokenInvariant())
	}
}

func TestWithoutConsumerDedupe(t *testing.T) {
	report := RunHarness(StandardConfig(1, 1500).Without("consumerDedupe"))
	if report.Passed() || report.FirstBrokenInvariant() != "posted-once-downstream" {
		t.Fatalf("expected posted-once-downstream to break, got %q", report.FirstBrokenInvariant())
	}
	conserved := false
	for _, v := range report.Violations {
		if strings.HasPrefix(v, "[money-conserved]") {
			conserved = true
		}
	}
	if !conserved {
		t.Fatal("without consumer dedupe money must no longer be conserved")
	}
	if _, err := AllDefences().Without("ledger"); err == nil {
		t.Fatal("unknown defence name must be rejected")
	}
}
