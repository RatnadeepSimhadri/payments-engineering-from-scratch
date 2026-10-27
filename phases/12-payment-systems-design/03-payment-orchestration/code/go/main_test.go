// Tests for phases/12-payment-systems-design/03-payment-orchestration. Run: go test ./...
package main

import (
	"errors"
	"strings"
	"testing"
)

// world is one fresh world per scenario: store, two processors, ledger, orchestrator with a budget of 3.
type world struct {
	clock    *FakeClock
	store    *SagaStore
	vault    *FakeProcessor
	acquirer *FakeProcessor
	ledger   *Ledger
	orch     *Orchestrator
}

func newWorld() *world {
	w := &world{clock: NewFakeClock(1_000), store: NewSagaStore(), vault: NewFakeProcessor("vault"), acquirer: NewFakeProcessor("acquirer"), ledger: NewLedger()}
	w.orch = NewOrchestrator(w.store, w.vault, w.acquirer, w.ledger, w.clock, 3)
	return w
}

func (w *world) start(t *testing.T, id string) *SagaRecord {
	t.Helper()
	r, err := w.orch.Start(id, "pay_"+id, OfMinor(4999, USD), "4242424242424242")
	if err != nil {
		t.Fatalf("start %s: %v", id, err)
	}
	return r
}

func (w *world) load(t *testing.T, id string) *SagaRecord {
	t.Helper()
	r, err := w.store.Load(id)
	if err != nil {
		t.Fatalf("load %s: %v", id, err)
	}
	return r
}

func check(t *testing.T, name string, condition bool) {
	t.Helper()
	if !condition {
		t.Errorf("FAIL: %s", name)
	}
}

func allDone(r *SagaRecord) bool {
	for _, s := range SagaSteps {
		if r.Steps[s] != DONE {
			return false
		}
	}
	return true
}

func nothingLeftDone(r *SagaRecord) bool {
	for _, s := range SagaSteps {
		if r.Steps[s] == DONE {
			return false
		}
	}
	return true
}

func indexOf(lines []string, want string) int {
	for i, l := range lines {
		if l == want {
			return i
		}
	}
	return -1
}

func TestHappyPath(t *testing.T) {
	w := newWorld()
	r := w.start(t, "s1")
	check(t, "happy path completes", r.State == COMPLETED && allDone(r))
	check(t, "each step carries its downstream reference", strings.HasPrefix(r.References[TOKENIZE], "vault_tokenize") &&
		strings.HasPrefix(r.References[AUTHORIZE], "acquirer_authorize") &&
		strings.HasPrefix(r.References[CAPTURE], "acquirer_capture") &&
		strings.HasPrefix(r.References[LEDGER_POST], "le_"))
	check(t, "ledger credited merchant_payable once", w.ledger.NetMerchantPayable("pay_s1", USD) == OfMinor(4999, USD) && len(w.ledger.Entries()) == 1)
	check(t, "exactly one side effect per step", w.vault.SideEffects("tokenize") == 1 && w.acquirer.SideEffects("authorize") == 1 && w.acquirer.SideEffects("capture") == 1)
	check(t, "record persisted once per step plus start and completion", w.load(t, "s1").Version == 6)
	again, err := w.orch.Run("s1")
	check(t, "running a COMPLETED saga again is a no-op", err == nil && again.State == COMPLETED && w.acquirer.SideEffects("capture") == 1 && w.acquirer.Replays() == 0)
}

func TestCompensationOrderAndSemantics(t *testing.T) {
	t.Run("decline at CAPTURE", func(t *testing.T) {
		w := newWorld()
		w.acquirer.FailNext("capture", DECLINE)
		r := w.start(t, "s2")
		check(t, "decline at CAPTURE ends COMPENSATED", r.State == COMPENSATED && nothingLeftDone(r))
		check(t, "failure reason names the step and code", r.FailureReason == "CAPTURE declined with code auth_expired")
		check(t, "compensations ran in reverse order", indexOf(r.Journal, "AUTHORIZE compensated via voidAuth") < indexOf(r.Journal, "TOKENIZE compensated via deleteToken"))
		check(t, "void and delete token each called once; no refund", w.acquirer.SideEffects("voidAuth") == 1 && w.vault.SideEffects("deleteToken") == 1 && w.acquirer.SideEffects("refund") == 0)
		check(t, "CAPTURE never done so never compensated", r.Steps[CAPTURE] == PENDING && r.Steps[LEDGER_POST] == PENDING)
		check(t, "no ledger entries for a payment that never captured", len(w.ledger.Entries()) == 0)
	})
	t.Run("decline at first step", func(t *testing.T) {
		w := newWorld()
		w.vault.FailNext("tokenize", DECLINE)
		r := w.start(t, "s3")
		check(t, "decline at first step compensates nothing", r.State == COMPENSATED && w.acquirer.SideEffects("authorize") == 0 && w.vault.SideEffects("deleteToken") == 0)
	})
	t.Run("ledger outage past the budget", func(t *testing.T) {
		w := newWorld()
		w.ledger.FailNext(OUTAGE).FailNext(OUTAGE).FailNext(OUTAGE)
		r := w.start(t, "s4")
		check(t, "ledger outage past the budget compensates the capture with a refund", r.State == COMPENSATED && w.acquirer.SideEffects("refund") == 1)
		check(t, "a captured authorization is not voided", w.acquirer.SideEffects("voidAuth") == 0 && r.Steps[AUTHORIZE] == STEP_COMPENSATED)
		check(t, "money conserved: merchant_payable net zero", w.ledger.NetMerchantPayable("pay_s4", USD).IsZero())
	})
}

func TestRetryBudget(t *testing.T) {
	t.Run("within budget", func(t *testing.T) {
		w := newWorld()
		w.acquirer.FailNext("authorize", OUTAGE).FailNext("authorize", OUTAGE)
		r := w.start(t, "s5")
		check(t, "two outages then success completes within the budget", r.State == COMPLETED && r.ForwardAttempts[AUTHORIZE] == 3)
		check(t, "outage retries did not create extra authorizations", w.acquirer.SideEffects("authorize") == 1)
	})
	t.Run("budget exhausted", func(t *testing.T) {
		w := newWorld()
		w.acquirer.FailNext("authorize", OUTAGE).FailNext("authorize", OUTAGE).FailNext("authorize", OUTAGE)
		r := w.start(t, "s6")
		check(t, "three outages exhaust the budget and compensate", r.State == COMPENSATED && r.ForwardAttempts[AUTHORIZE] == 3)
		check(t, "exhausted step compensates only what was done", w.vault.SideEffects("deleteToken") == 1 && w.acquirer.SideEffects("voidAuth") == 0)
	})
}

func TestCrashRecoveryAndIdempotentSteps(t *testing.T) {
	t.Run("crash after acquirer commit", func(t *testing.T) {
		w := newWorld()
		w.acquirer.FailNext("authorize", CRASH_AFTER_COMMIT)
		_, err := w.orch.Start("s7", "pay_s7", OfMinor(4999, USD), "4242424242424242")
		check(t, "crash after commit propagates out of run", errors.Is(err, ErrProcessCrash))
		persisted := w.load(t, "s7")
		check(t, "persisted record does not know about the authorization", persisted.State == RUNNING && persisted.Steps[AUTHORIZE] == PENDING && persisted.Steps[TOKENIZE] == DONE)
		check(t, "acquirer holds exactly one authorization", w.acquirer.SideEffects("authorize") == 1)
		restarted := NewOrchestrator(w.store, w.vault, w.acquirer, w.ledger, w.clock, 3)
		resumed, err := restarted.Recover()
		check(t, "recover resumes exactly one unfinished saga", err == nil && resumed == 1)
		r := w.load(t, "s7")
		check(t, "recovered saga completes", r.State == COMPLETED && allDone(r))
		check(t, "recovery replayed the stored response instead of authorizing again", w.acquirer.SideEffects("authorize") == 1 && w.acquirer.Replays() == 1)
		check(t, "recovered reference equals the one the acquirer committed", r.References[AUTHORIZE] == "acquirer_authorize_0001")
		resumed, _ = restarted.Recover()
		check(t, "recover with nothing unfinished resumes zero", resumed == 0)
	})
	t.Run("crash after ledger commit", func(t *testing.T) {
		w := newWorld()
		w.ledger.FailNext(CRASH_AFTER_COMMIT)
		_, err := w.orch.Start("s8", "pay_s8", OfMinor(4999, USD), "4242424242424242")
		check(t, "crash after ledger commit propagates", errors.Is(err, ErrProcessCrash))
		NewOrchestrator(w.store, w.vault, w.acquirer, w.ledger, w.clock, 3).Recover()
		check(t, "ledger post not duplicated after recovery", len(w.ledger.Entries()) == 1 && w.load(t, "s8").State == COMPLETED)
	})
	t.Run("crash during a compensation", func(t *testing.T) {
		w := newWorld()
		w.acquirer.FailNext("capture", DECLINE).FailNext("voidAuth", CRASH_AFTER_COMMIT)
		_, err := w.orch.Start("s9", "pay_s9", OfMinor(4999, USD), "4242424242424242")
		check(t, "crash during a compensation propagates", errors.Is(err, ErrProcessCrash))
		check(t, "persisted state is COMPENSATING with AUTHORIZE still DONE", w.load(t, "s9").State == COMPENSATING && w.load(t, "s9").Steps[AUTHORIZE] == DONE)
		NewOrchestrator(w.store, w.vault, w.acquirer, w.ledger, w.clock, 3).Recover()
		check(t, "compensation resumes and finishes without a second void", w.load(t, "s9").State == COMPENSATED && w.acquirer.SideEffects("voidAuth") == 1)
	})
}

func TestIdempotencyKeys(t *testing.T) {
	r, err := NewSagaRecord("sx", "pay_x", OfMinor(100, USD), "4242")
	if err != nil {
		t.Fatal(err)
	}
	check(t, "forward and compensation keys differ", Key(r, AUTHORIZE, false) != Key(r, AUTHORIZE, true))
	check(t, "key is a pure function of saga and step", Key(r, CAPTURE, false) == "sx:capture:fwd")
	_, err = NewSagaRecord("sy", "pay_y", Zero(USD), "4242")
	check(t, "saga amount must be positive", errors.Is(err, ErrInvalidAmount))
}

func TestDeadLetterPath(t *testing.T) {
	t.Run("outage past the budget then redrive", func(t *testing.T) {
		w := newWorld()
		w.acquirer.FailNext("capture", DECLINE)
		for i := 0; i < 3; i++ {
			w.acquirer.FailNext("voidAuth", OUTAGE)
		}
		r := w.start(t, "s10")
		check(t, "compensation outage past the budget dead-letters", r.State == DEAD_LETTERED && r.CompensationAttempts[AUTHORIZE] == 3)
		dl := w.orch.DeadLetters()
		check(t, "dead letter names the saga and the failing compensation", len(dl) == 1 && dl[0].Step == AUTHORIZE && dl[0].SagaID == "s10")
		check(t, "dead-lettered saga keeps AUTHORIZE as DONE for the operator", r.Steps[AUTHORIZE] == DONE && r.Steps[TOKENIZE] == DONE)
		resumed, _ := w.orch.Recover()
		check(t, "recover does not touch dead letters", resumed == 0)
		redriven, err := w.orch.Redrive("s10")
		check(t, "redrive finishes the compensation once the acquirer is back", err == nil && redriven.State == COMPENSATED && w.acquirer.SideEffects("voidAuth") == 1 && len(w.orch.DeadLetters()) == 0)
		_, err = w.orch.Redrive("s10")
		check(t, "redrive refuses a saga that is not dead-lettered", errors.Is(err, ErrIllegalState))
	})
	t.Run("refused compensation", func(t *testing.T) {
		w := newWorld()
		w.acquirer.FailNext("capture", DECLINE).FailNext("voidAuth", DECLINE)
		r := w.start(t, "s11")
		check(t, "a refused compensation dead-letters immediately", r.State == DEAD_LETTERED && strings.Contains(r.Journal[len(r.Journal)-1], "already_captured"))
	})
}

func TestStoreModelsPersistence(t *testing.T) {
	store := NewSagaStore()
	r, _ := NewSagaRecord("s12", "pay_12", OfMinor(100, USD), "4242")
	store.Save(r)
	r.MarkDone(TOKENIZE, "tok")
	loaded, _ := store.Load("s12")
	check(t, "an unsaved mutation is invisible to load", loaded.Steps[TOKENIZE] == PENDING)
	_, err := store.Load("nope")
	check(t, "loading an unknown saga fails", errors.Is(err, ErrIllegalState))
}
