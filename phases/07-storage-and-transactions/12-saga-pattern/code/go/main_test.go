// Tests for phases/07-storage-and-transactions/12-saga-pattern. Run: go test ./...
package main

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func assert(t *testing.T, name string, condition bool) {
	t.Helper()
	if !condition {
		t.Errorf("%s", name)
	}
}

// world is a fresh world: alice has 100.00 USD, five widgets in stock, one orchestrator over one log.
type world struct {
	clock    *SimClock
	wallet   *WalletService
	shipping *ShippingService
	log      *SagaLog
	saga     *SagaOrchestrator
}

func newWorld() *world {
	w := &world{clock: NewSimClock(0)}
	w.wallet = NewWalletService(map[string]Money{"alice": usd("100.00")})
	w.shipping = NewShippingService(map[string]int{"widget": 5})
	w.log = NewSagaLog(w.clock)
	w.saga = NewSagaOrchestrator(w.log, []SagaStep{ReserveFunds{w.wallet}, CaptureFunds{w.wallet}, ShipOrder{w.shipping}}, w.clock, 10)
	return w
}

func (w *world) order(id string, qty int, amount string) Order {
	return Order{id, "alice", "widget", qty, usd(amount)}
}

func (w *world) kinds(sagaID string) []string {
	out := []string{}
	for _, r := range w.log.ForSaga(sagaID) {
		k := r.Kind
		if r.Step != "" {
			k += ":" + r.Step
		}
		out = append(out, k)
	}
	return out
}

func run(t *testing.T, w *world, o Order) SagaOutcome {
	t.Helper()
	out, err := w.saga.Run(o)
	if err != nil {
		t.Fatalf("run %s: %v", o.SagaID, err)
	}
	return out
}

func recoverAll(t *testing.T, w *world) []SagaOutcome {
	t.Helper()
	out, err := w.saga.Recover()
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	return out
}

func TestHappyPath(t *testing.T) {
	w := newWorld()
	out := run(t, w, w.order("s1", 2, "30.00"))
	assert(t, "happy path completes", out.Status == Completed)
	assert(t, "funds captured", w.wallet.Balance("alice") == usd("70.00"))
	assert(t, "no hold remains", !w.wallet.HasHold("s1") && w.wallet.Available("alice") == usd("70.00"))
	assert(t, "stock shipped", w.shipping.StockOf("widget") == 3 && w.shipping.IsShipped("s1"))
	assert(t, "log has a started and completed record per step", slices.Equal(w.kinds("s1"), []string{
		"SAGA_STARTED", "STEP_STARTED:reserve-funds", "STEP_COMPLETED:reserve-funds",
		"STEP_STARTED:capture-funds", "STEP_COMPLETED:capture-funds",
		"STEP_STARTED:ship-order", "STEP_COMPLETED:ship-order", "SAGA_COMPLETED"}))
	assert(t, "clock advanced once per step", w.clock.Now() == 30)
	assert(t, "records print as seq t=ms saga kind step", w.log.ForSaga("s1")[1].String() == "2 t=0 s1 STEP_STARTED reserve-funds")
	assert(t, "outcome prints as saga -> status", out.String() == "s1 -> COMPLETED")
}

func TestFailureAtStepThreeCompensatesInReverse(t *testing.T) {
	w := newWorld()
	out := run(t, w, w.order("s2", 9, "40.00"))
	assert(t, "out of stock compensates", out.Status == Compensated && out.FailedStep == "ship-order")
	assert(t, "balance restored by refund", w.wallet.Balance("alice") == usd("100.00"))
	assert(t, "hold released", w.wallet.Available("alice") == usd("100.00") && !w.wallet.HasHold("s2"))
	assert(t, "stock untouched", w.shipping.StockOf("widget") == 5)
	kinds := w.kinds("s2")
	assert(t, "compensations run in reverse order", slices.Index(kinds, "COMP_COMPLETED:capture-funds") < slices.Index(kinds, "COMP_STARTED:reserve-funds"))
	assert(t, "failed step is not compensated", !slices.Contains(kinds, "COMP_STARTED:ship-order"))
	assert(t, "saga ends COMPENSATED", kinds[len(kinds)-1] == "SAGA_COMPENSATED")
	assert(t, "outcome names the failed step and reason", out.String() == "s2 -> COMPENSATED (failed at ship-order: out of stock: widget has 5, need 9)")
}

func TestFailureAtStepOne(t *testing.T) {
	w := newWorld()
	out := run(t, w, w.order("s3", 1, "250.00"))
	assert(t, "insufficient funds fails at reserve", out.Status == Compensated && out.FailedStep == "reserve-funds")
	none := true
	for _, k := range w.kinds("s3") {
		if strings.HasPrefix(k, "COMP_") {
			none = false
		}
	}
	assert(t, "no compensation records when nothing completed", none)
	assert(t, "balance untouched", w.wallet.Balance("alice") == usd("100.00"))
}

func TestCrashAfterExecuteBeforeLog(t *testing.T) {
	w := newWorld()
	w.saga.CrashPoint = AfterExecute("capture-funds")
	_, err := w.saga.Run(w.order("s4", 1, "25.00"))
	assert(t, "crash point fires", errors.Is(err, ErrCrash))
	var crash *CrashError
	assert(t, "crash error says where", errors.As(err, &crash) && crash.Where == "after capture-funds executed, before STEP_COMPLETED logged")
	assert(t, "a crash is not a step failure", !errors.Is(err, ErrStepFailed))
	assert(t, "capture already happened before the crash", w.wallet.Balance("alice") == usd("75.00"))
	assert(t, "log shows the saga still RUNNING", StatusOf(w.log.ForSaga("s4")) == Running)
	callsBefore := w.wallet.Calls
	resumed := recoverAll(t, w)
	assert(t, "recovery completes the saga", len(resumed) == 1 && resumed[0].Status == Completed)
	assert(t, "capture was replayed exactly once", w.wallet.Calls == callsBefore+1)
	assert(t, "no double debit", w.wallet.Balance("alice") == usd("75.00"))
	assert(t, "shipping happened after recovery", w.shipping.StockOf("widget") == 4)
	assert(t, "recovery is idempotent", len(recoverAll(t, w)) == 0)
	assert(t, "recovery clears the crash point", w.saga.CrashPoint == NoCrash)
}

func TestCrashDuringCompensation(t *testing.T) {
	w := newWorld()
	w.saga.CrashPoint = AfterCompensate("capture-funds")
	_, err := w.saga.Run(w.order("s5", 9, "10.00"))
	assert(t, "crash during compensation", errors.Is(err, ErrCrash))
	assert(t, "refund already applied", w.wallet.Balance("alice") == usd("100.00"))
	assert(t, "log shows COMPENSATING", StatusOf(w.log.ForSaga("s5")) == Compensating)
	resumed := recoverAll(t, w)
	assert(t, "recovery finishes compensation", len(resumed) == 1 && resumed[0].Status == Compensated && resumed[0].FailedStep == "ship-order")
	assert(t, "no double refund", w.wallet.Balance("alice") == usd("100.00"))
	assert(t, "hold released by resumed compensation", !w.wallet.HasHold("s5") && w.wallet.Available("alice") == usd("100.00"))
}

func TestSemanticLock(t *testing.T) {
	w := newWorld()
	w.saga.CrashPoint = AfterExecute("reserve-funds")
	_, err := w.saga.Run(w.order("A", 1, "80.00"))
	assert(t, "saga A stalls after reserving", errors.Is(err, ErrCrash))
	assert(t, "balance unchanged but available reduced", w.wallet.Balance("alice") == usd("100.00") && w.wallet.Available("alice") == usd("20.00"))
	b := run(t, w, w.order("B", 1, "50.00"))
	assert(t, "saga B refused by the semantic lock", b.Status == Compensated && strings.Contains(b.Reason, "insufficient available"))
	recoverAll(t, w)
	assert(t, "saga A completed after recovery and the lock is gone", w.wallet.Balance("alice") == usd("20.00") && w.wallet.Available("alice") == usd("20.00"))
}

func TestServiceIdempotency(t *testing.T) {
	wallet := NewWalletService(map[string]Money{"alice": usd("10.00")})
	assert(t, "first reserve succeeds", wallet.Reserve("k", "alice", usd("4.00")) == nil)
	assert(t, "duplicate reserve is accepted", wallet.Reserve("k", "alice", usd("4.00")) == nil)
	assert(t, "duplicate reserve holds once", wallet.Available("alice") == usd("6.00"))
	assert(t, "capture succeeds twice", wallet.Capture("k") == nil && wallet.Capture("k") == nil)
	assert(t, "duplicate capture debits once", wallet.Balance("alice") == usd("6.00"))
	wallet.Refund("k", "alice")
	wallet.Refund("k", "alice")
	assert(t, "duplicate refund credits once", wallet.Balance("alice") == usd("10.00"))
	err := wallet.Capture("never-reserved")
	assert(t, "capture without hold fails the step", errors.Is(err, ErrStepFailed) && err.Error() == "no hold to capture for never-reserved")
	wallet.Release("never-reserved")
	assert(t, "release of a hold that never existed is a no-op", wallet.Available("alice") == usd("10.00"))
	assert(t, "reserve on an unknown account fails the step", errors.Is(wallet.Reserve("z", "zed", usd("1.00")), ErrStepFailed))
	shipping := NewShippingService(map[string]int{"widget": 1})
	assert(t, "ship then duplicate ship consumes stock once", shipping.Ship("s", "widget", 1) == nil && shipping.Ship("s", "widget", 1) == nil && shipping.StockOf("widget") == 0)
	shipping.CancelShipment("s", "widget")
	shipping.CancelShipment("s", "widget")
	assert(t, "duplicate cancel restocks once", shipping.StockOf("widget") == 1 && !shipping.IsShipped("s"))
}

func TestMoneyDiscipline(t *testing.T) {
	_, err := usd("1.00").Plus(OfMinor(100, Currency{"EUR", 2}))
	assert(t, "currency mismatch is rejected", errors.Is(err, ErrCurrencyMismatch))
	assert(t, "toString pads minor units", usd("0.05").String() == "0.05 USD")
	_, err = OfMinor(1<<62, USD).Plus(OfMinor(1<<62, USD))
	assert(t, "overflow is an error, not a wrap", errors.Is(err, ErrOverflow))
	assert(t, "compare orders amounts", usd("1.00").Compare(usd("2.00")) < 0 && usd("2.00").Compare(usd("2.00")) == 0)
}
