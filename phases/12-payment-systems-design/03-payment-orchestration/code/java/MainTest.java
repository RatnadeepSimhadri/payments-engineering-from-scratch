// Tests for phases/12-payment-systems-design/03-payment-orchestration. Run: javac -d .out *.java && java -cp .out MainTest
import java.util.ArrayList;
import java.util.List;

public final class MainTest {
    private static int passed = 0;
    private static final List<String> failures = new ArrayList<>();

    static void check(String name, boolean condition) {
        if (condition) passed++; else failures.add(name);
    }

    static void checkThrows(String name, Class<? extends Throwable> type, Runnable action) {
        try {
            action.run();
            failures.add(name + " (no exception)");
        } catch (Throwable t) {
            if (type.isInstance(t)) passed++; else failures.add(name + " (wrong exception " + t.getClass().getSimpleName() + ": " + t.getMessage() + ")");
        }
    }

    static final Currency USD = Currency.USD;

    /** One fresh world per scenario: store, two processors, ledger, orchestrator with a budget of 3. */
    static final class World {
        final FakeClock clock = new FakeClock(1_000);
        final SagaStore store = new SagaStore();
        final FakeProcessor vault = new FakeProcessor("vault");
        final FakeProcessor acquirer = new FakeProcessor("acquirer");
        final Ledger ledger = new Ledger();
        final Orchestrator orch = new Orchestrator(store, vault, acquirer, ledger, clock, 3);
        SagaRecord start(String id) { return orch.start(id, "pay_" + id, Money.ofMinor(4999, USD), "4242424242424242"); }
    }

    static boolean allDone(SagaRecord r) {
        for (SagaStep s : SagaStep.values()) if (r.steps.get(s) != StepStatus.DONE) return false;
        return true;
    }

    static boolean nothingLeftDone(SagaRecord r) {
        for (SagaStep s : SagaStep.values()) if (r.steps.get(s) == StepStatus.DONE) return false;
        return true;
    }

    public static void main(String[] args) {
        // --- happy path ---------------------------------------------------------------------------
        {
            World w = new World();
            SagaRecord r = w.start("s1");
            check("happy path completes", r.state == SagaState.COMPLETED && allDone(r));
            check("each step carries its downstream reference", r.references.get(SagaStep.TOKENIZE).startsWith("vault_tokenize")
                    && r.references.get(SagaStep.AUTHORIZE).startsWith("acquirer_authorize")
                    && r.references.get(SagaStep.CAPTURE).startsWith("acquirer_capture")
                    && r.references.get(SagaStep.LEDGER_POST).startsWith("le_"));
            check("ledger credited merchant_payable once", w.ledger.netMerchantPayable("pay_s1", USD).equals(Money.ofMinor(4999, USD)) && w.ledger.entries().size() == 1);
            check("exactly one side effect per step", w.vault.sideEffects("tokenize") == 1 && w.acquirer.sideEffects("authorize") == 1 && w.acquirer.sideEffects("capture") == 1);
            check("record persisted once per step plus start and completion", w.store.load("s1").version == 6);
            check("running a COMPLETED saga again is a no-op", w.orch.run("s1").state == SagaState.COMPLETED && w.acquirer.sideEffects("capture") == 1 && w.acquirer.replays() == 0);
        }

        // --- compensation order and semantics ---------------------------------------------------
        {
            World w = new World();
            w.acquirer.failNext("capture", Fault.DECLINE);
            SagaRecord r = w.start("s2");
            check("decline at CAPTURE ends COMPENSATED", r.state == SagaState.COMPENSATED && nothingLeftDone(r));
            check("failure reason names the step and code", r.failureReason.equals("CAPTURE declined with code auth_expired"));
            check("compensations ran in reverse order", r.journal.indexOf("AUTHORIZE compensated via voidAuth") < r.journal.indexOf("TOKENIZE compensated via deleteToken"));
            check("void and delete token each called once; no refund", w.acquirer.sideEffects("voidAuth") == 1 && w.vault.sideEffects("deleteToken") == 1 && w.acquirer.sideEffects("refund") == 0);
            check("CAPTURE never done so never compensated", r.steps.get(SagaStep.CAPTURE) == StepStatus.PENDING && r.steps.get(SagaStep.LEDGER_POST) == StepStatus.PENDING);
            check("no ledger entries for a payment that never captured", w.ledger.entries().isEmpty());
        }
        {
            World w = new World();
            w.vault.failNext("tokenize", Fault.DECLINE);
            SagaRecord r = w.start("s3");
            check("decline at first step compensates nothing", r.state == SagaState.COMPENSATED && w.acquirer.sideEffects("authorize") == 0 && w.vault.sideEffects("deleteToken") == 0);
        }
        {
            World w = new World();
            w.ledger.failNext(Fault.OUTAGE).failNext(Fault.OUTAGE).failNext(Fault.OUTAGE);
            SagaRecord r = w.start("s4");
            check("ledger outage past the budget compensates the capture with a refund", r.state == SagaState.COMPENSATED && w.acquirer.sideEffects("refund") == 1);
            check("a captured authorization is not voided", w.acquirer.sideEffects("voidAuth") == 0 && r.steps.get(SagaStep.AUTHORIZE) == StepStatus.COMPENSATED);
            check("money conserved: merchant_payable net zero", w.ledger.netMerchantPayable("pay_s4", USD).isZero());
        }

        // --- retry budget --------------------------------------------------------------------------
        {
            World w = new World();
            w.acquirer.failNext("authorize", Fault.OUTAGE).failNext("authorize", Fault.OUTAGE);
            SagaRecord r = w.start("s5");
            check("two outages then success completes within the budget", r.state == SagaState.COMPLETED && r.forwardAttempts.get(SagaStep.AUTHORIZE) == 3);
            check("outage retries did not create extra authorizations", w.acquirer.sideEffects("authorize") == 1);
        }
        {
            World w = new World();
            w.acquirer.failNext("authorize", Fault.OUTAGE).failNext("authorize", Fault.OUTAGE).failNext("authorize", Fault.OUTAGE);
            SagaRecord r = w.start("s6");
            check("three outages exhaust the budget and compensate", r.state == SagaState.COMPENSATED && r.forwardAttempts.get(SagaStep.AUTHORIZE) == 3);
            check("exhausted step compensates only what was done", w.vault.sideEffects("deleteToken") == 1 && w.acquirer.sideEffects("voidAuth") == 0);
        }

        // --- crash recovery and idempotent steps -------------------------------------------------
        {
            World w = new World();
            w.acquirer.failNext("authorize", Fault.CRASH_AFTER_COMMIT);
            checkThrows("crash after commit propagates out of run", ProcessCrashException.class, () -> w.start("s7"));
            SagaRecord persisted = w.store.load("s7");
            check("persisted record does not know about the authorization", persisted.state == SagaState.RUNNING && persisted.steps.get(SagaStep.AUTHORIZE) == StepStatus.PENDING && persisted.steps.get(SagaStep.TOKENIZE) == StepStatus.DONE);
            check("acquirer holds exactly one authorization", w.acquirer.sideEffects("authorize") == 1);
            Orchestrator restarted = new Orchestrator(w.store, w.vault, w.acquirer, w.ledger, w.clock, 3);
            check("recover resumes exactly one unfinished saga", restarted.recover() == 1);
            SagaRecord r = w.store.load("s7");
            check("recovered saga completes", r.state == SagaState.COMPLETED && allDone(r));
            check("recovery replayed the stored response instead of authorizing again", w.acquirer.sideEffects("authorize") == 1 && w.acquirer.replays() == 1);
            check("recovered reference equals the one the acquirer committed", r.references.get(SagaStep.AUTHORIZE).equals("acquirer_authorize_0001"));
            check("recover with nothing unfinished resumes zero", restarted.recover() == 0);
        }
        {
            World w = new World();
            w.ledger.failNext(Fault.CRASH_AFTER_COMMIT);
            checkThrows("crash after ledger commit propagates", ProcessCrashException.class, () -> w.start("s8"));
            new Orchestrator(w.store, w.vault, w.acquirer, w.ledger, w.clock, 3).recover();
            check("ledger post not duplicated after recovery", w.ledger.entries().size() == 1 && w.store.load("s8").state == SagaState.COMPLETED);
        }
        {
            World w = new World();
            w.acquirer.failNext("capture", Fault.DECLINE).failNext("voidAuth", Fault.CRASH_AFTER_COMMIT);
            checkThrows("crash during a compensation propagates", ProcessCrashException.class, () -> w.start("s9"));
            check("persisted state is COMPENSATING with AUTHORIZE still DONE", w.store.load("s9").state == SagaState.COMPENSATING && w.store.load("s9").steps.get(SagaStep.AUTHORIZE) == StepStatus.DONE);
            new Orchestrator(w.store, w.vault, w.acquirer, w.ledger, w.clock, 3).recover();
            check("compensation resumes and finishes without a second void", w.store.load("s9").state == SagaState.COMPENSATED && w.acquirer.sideEffects("voidAuth") == 1);
        }

        // --- idempotency keys ----------------------------------------------------------------------
        {
            SagaRecord r = new SagaRecord("sx", "pay_x", Money.ofMinor(100, USD), "4242");
            check("forward and compensation keys differ", !Orchestrator.key(r, SagaStep.AUTHORIZE, false).equals(Orchestrator.key(r, SagaStep.AUTHORIZE, true)));
            check("key is a pure function of saga and step", Orchestrator.key(r, SagaStep.CAPTURE, false).equals("sx:capture:fwd"));
            checkThrows("saga amount must be positive", IllegalArgumentException.class, () -> new SagaRecord("sy", "pay_y", Money.zero(USD), "4242"));
        }

        // --- dead-letter path ----------------------------------------------------------------------
        {
            World w = new World();
            w.acquirer.failNext("capture", Fault.DECLINE);
            for (int i = 0; i < 3; i++) w.acquirer.failNext("voidAuth", Fault.OUTAGE);
            SagaRecord r = w.start("s10");
            check("compensation outage past the budget dead-letters", r.state == SagaState.DEAD_LETTERED && r.compensationAttempts.get(SagaStep.AUTHORIZE) == 3);
            check("dead letter names the saga and the failing compensation", w.orch.deadLetters().size() == 1 && w.orch.deadLetters().get(0).step() == SagaStep.AUTHORIZE && w.orch.deadLetters().get(0).sagaId().equals("s10"));
            check("dead-lettered saga keeps AUTHORIZE as DONE for the operator", r.steps.get(SagaStep.AUTHORIZE) == StepStatus.DONE && r.steps.get(SagaStep.TOKENIZE) == StepStatus.DONE);
            check("recover does not touch dead letters", w.orch.recover() == 0);
            SagaRecord redriven = w.orch.redrive("s10");
            check("redrive finishes the compensation once the acquirer is back", redriven.state == SagaState.COMPENSATED && w.acquirer.sideEffects("voidAuth") == 1 && w.orch.deadLetters().isEmpty());
            checkThrows("redrive refuses a saga that is not dead-lettered", IllegalStateException.class, () -> w.orch.redrive("s10"));
        }
        {
            World w = new World();
            w.acquirer.failNext("capture", Fault.DECLINE).failNext("voidAuth", Fault.DECLINE);
            SagaRecord r = w.start("s11");
            check("a refused compensation dead-letters immediately", r.state == SagaState.DEAD_LETTERED && r.journal.get(r.journal.size() - 1).contains("already_captured"));
        }

        // --- the store models persistence -----------------------------------------------------------
        {
            SagaStore store = new SagaStore();
            SagaRecord r = new SagaRecord("s12", "pay_12", Money.ofMinor(100, USD), "4242");
            store.save(r);
            r.markDone(SagaStep.TOKENIZE, "tok");
            check("an unsaved mutation is invisible to load", store.load("s12").steps.get(SagaStep.TOKENIZE) == StepStatus.PENDING);
            checkThrows("loading an unknown saga fails", IllegalStateException.class, () -> store.load("nope"));
        }

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        for (String f : failures) System.out.println("FAIL: " + f);
        if (!failures.isEmpty()) System.exit(1);
    }
}
