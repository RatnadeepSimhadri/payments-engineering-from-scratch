// Lesson: phases/12-payment-systems-design/03-payment-orchestration/docs/en.md
// Payment Orchestration Across Processors
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: Garcia-Molina & Salem, "Sagas" (SIGMOD 1987); IETF draft-ietf-httpapi-idempotency-key-header;
//          Stripe API reference (PaymentIntent capture, Refund). Java 21, stdlib only.

import java.util.ArrayDeque;
import java.util.ArrayList;
import java.util.Collections;
import java.util.Deque;
import java.util.EnumMap;
import java.util.HashMap;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.Objects;

// ---------------------------------------------------------------------------------------------
// Money: integer minor units + ISO 4217 currency (Phase 10, Lesson 01). Copied, not imported.
// ---------------------------------------------------------------------------------------------

record Currency(String code, int exponent) {
    static final Currency USD = new Currency("USD", 2);
    static final Currency EUR = new Currency("EUR", 2);

    Currency {
        if (code == null || code.length() != 3 || !code.chars().allMatch(Character::isUpperCase)) {
            throw new IllegalArgumentException("currency code must be three uppercase letters: " + code);
        }
        if (exponent < 0 || exponent > 4) throw new IllegalArgumentException("exponent must be 0..4: " + exponent);
    }

    long minorUnitsPerMajor() {
        long scale = 1;
        for (int i = 0; i < exponent; i++) scale *= 10;
        return scale;
    }
}

final class CurrencyMismatchException extends IllegalArgumentException {
    CurrencyMismatchException(Currency a, Currency b) { super("cannot combine " + a.code() + " with " + b.code()); }
}

record Money(long minorUnits, Currency currency) implements Comparable<Money> {
    Money { Objects.requireNonNull(currency, "currency"); }

    static Money ofMinor(long minorUnits, Currency currency) { return new Money(minorUnits, currency); }
    static Money zero(Currency currency) { return new Money(0, currency); }

    private void requireSameCurrency(Money other) {
        if (!currency.equals(other.currency)) throw new CurrencyMismatchException(currency, other.currency);
    }

    Money plus(Money other) { requireSameCurrency(other); return new Money(Math.addExact(minorUnits, other.minorUnits), currency); }
    Money minus(Money other) { requireSameCurrency(other); return new Money(Math.subtractExact(minorUnits, other.minorUnits), currency); }
    Money negate() { return new Money(Math.negateExact(minorUnits), currency); }
    boolean isPositive() { return minorUnits > 0; }
    boolean isZero() { return minorUnits == 0; }

    @Override public int compareTo(Money other) { requireSameCurrency(other); return Long.compare(minorUnits, other.minorUnits); }

    @Override public String toString() {
        long magnitude = Math.abs(minorUnits);
        long scale = currency.minorUnitsPerMajor();
        String result = Long.toString(magnitude / scale);
        if (currency.exponent() > 0) {
            String frac = Long.toString(magnitude % scale);
            result = result + "." + "0".repeat(currency.exponent() - frac.length()) + frac;
        }
        return (minorUnits < 0 ? "-" : "") + result + " " + currency.code();
    }
}

interface Clock { long nowMillis(); }

final class FakeClock implements Clock {
    private long now;
    FakeClock(long startMillis) { now = startMillis; }
    @Override public long nowMillis() { return now; }
    void advance(long millis) { now += millis; }
}

// ---------------------------------------------------------------------------------------------
// The saga vocabulary. Four forward steps in a fixed order; each has exactly one compensation.
// ---------------------------------------------------------------------------------------------

enum SagaStep {
    TOKENIZE("vault", "deleteToken"),
    AUTHORIZE("acquirer", "voidAuth"),
    CAPTURE("acquirer", "refund"),
    LEDGER_POST("ledger", "reverse");

    final String system;
    final String compensation;
    SagaStep(String system, String compensation) { this.system = system; this.compensation = compensation; }
}

enum SagaState {
    RUNNING, COMPLETED, COMPENSATING, COMPENSATED, DEAD_LETTERED;

    boolean isTerminal() { return this == COMPLETED || this == COMPENSATED; }
}

enum StepStatus { PENDING, DONE, COMPENSATED }

/** Thrown when a downstream system cannot be reached. The call may or may not have happened. */
final class ProcessorUnavailableException extends RuntimeException {
    ProcessorUnavailableException(String message) { super(message); }
}

/** Simulates our own process dying after the downstream committed but before we persisted. */
final class ProcessCrashException extends RuntimeException {
    ProcessCrashException(String message) { super(message); }
}

record ProcessorResponse(boolean approved, String reference, String code) {
    static ProcessorResponse approved(String reference) { return new ProcessorResponse(true, reference, "00"); }
    static ProcessorResponse declined(String code) { return new ProcessorResponse(false, null, code); }
}

// ---------------------------------------------------------------------------------------------
// The persisted saga record. Every field is written to the store after every step; a crash at any
// point leaves a record that `recover` can resume from.
// ---------------------------------------------------------------------------------------------

final class SagaRecord {
    final String sagaId;
    final String paymentId;
    final Money amount;
    final String pan;
    SagaState state = SagaState.RUNNING;
    final EnumMap<SagaStep, StepStatus> steps = new EnumMap<>(SagaStep.class);
    final EnumMap<SagaStep, String> references = new EnumMap<>(SagaStep.class);
    final EnumMap<SagaStep, Integer> forwardAttempts = new EnumMap<>(SagaStep.class);
    final EnumMap<SagaStep, Integer> compensationAttempts = new EnumMap<>(SagaStep.class);
    String failureReason = null;
    int version = 0;
    final List<String> journal = new ArrayList<>();

    SagaRecord(String sagaId, String paymentId, Money amount, String pan) {
        if (!amount.isPositive()) throw new IllegalArgumentException("amount must be positive: " + amount);
        this.sagaId = sagaId;
        this.paymentId = paymentId;
        this.amount = amount;
        this.pan = pan;
        for (SagaStep s : SagaStep.values()) {
            steps.put(s, StepStatus.PENDING);
            forwardAttempts.put(s, 0);
            compensationAttempts.put(s, 0);
        }
    }

    /** The first step not yet DONE, in forward order; null when every step is DONE. */
    SagaStep nextForward() {
        for (SagaStep s : SagaStep.values()) if (steps.get(s) == StepStatus.PENDING) return s;
        return null;
    }

    /** The last DONE step, in reverse order; null when nothing remains to undo. */
    SagaStep nextCompensation() {
        SagaStep[] all = SagaStep.values();
        for (int i = all.length - 1; i >= 0; i--) if (steps.get(all[i]) == StepStatus.DONE) return all[i];
        return null;
    }

    void markDone(SagaStep step, String reference) {
        steps.put(step, StepStatus.DONE);
        references.put(step, reference);
        journal.add(step + " done -> " + reference);
    }

    void markCompensated(SagaStep step, String how) {
        steps.put(step, StepStatus.COMPENSATED);
        journal.add(step + " compensated via " + how);
    }

    void beginCompensation(String reason) {
        state = SagaState.COMPENSATING;
        failureReason = reason;
        journal.add("compensating: " + reason);
    }

    SagaRecord copy() {
        SagaRecord c = new SagaRecord(sagaId, paymentId, amount, pan);
        c.state = state;
        c.steps.putAll(steps);
        c.references.putAll(references);
        c.forwardAttempts.putAll(forwardAttempts);
        c.compensationAttempts.putAll(compensationAttempts);
        c.failureReason = failureReason;
        c.version = version;
        c.journal.addAll(journal);
        return c;
    }

    @Override public String toString() {
        StringBuilder sb = new StringBuilder(sagaId + " " + state + " v" + version + " [");
        for (SagaStep s : SagaStep.values()) sb.append(s.name().charAt(0)).append('=').append(steps.get(s).name().charAt(0)).append(' ');
        sb.setLength(sb.length() - 1);
        return sb.append(']').toString();
    }
}

/** Durable storage for saga records. Stores copies so an in-flight mutation that never reaches save() is lost on crash, as it would be in a database. */
final class SagaStore {
    private final Map<String, SagaRecord> records = new LinkedHashMap<>();

    void save(SagaRecord r) {
        r.version++;
        records.put(r.sagaId, r.copy());
    }

    SagaRecord load(String sagaId) {
        SagaRecord r = records.get(sagaId);
        if (r == null) throw new IllegalStateException("unknown saga " + sagaId);
        return r.copy();
    }

    List<SagaRecord> unfinished() {
        List<SagaRecord> out = new ArrayList<>();
        for (SagaRecord r : records.values()) if (!r.state.isTerminal() && r.state != SagaState.DEAD_LETTERED) out.add(r.copy());
        return out;
    }
}

// ---------------------------------------------------------------------------------------------
// Downstream systems. Each operation takes an idempotency key: the same key returns the stored
// response without repeating the side effect. Faults are scripted per operation.
// ---------------------------------------------------------------------------------------------

enum Fault { DECLINE, OUTAGE, CRASH_AFTER_COMMIT }

interface Processor {
    String name();
    ProcessorResponse tokenize(String idempotencyKey, String pan);
    ProcessorResponse deleteToken(String idempotencyKey, String token);
    ProcessorResponse authorize(String idempotencyKey, String token, Money amount);
    ProcessorResponse voidAuth(String idempotencyKey, String authRef);
    ProcessorResponse capture(String idempotencyKey, String authRef, Money amount);
    ProcessorResponse refund(String idempotencyKey, String captureRef, Money amount);
}

final class FakeProcessor implements Processor {
    private final String name;
    private final Map<String, ProcessorResponse> byKey = new HashMap<>();
    private final Map<String, Deque<Fault>> faults = new HashMap<>();
    private final Map<String, Integer> sideEffects = new HashMap<>();
    private int replays = 0;
    private int sequence = 0;

    FakeProcessor(String name) { this.name = name; }

    @Override public String name() { return name; }

    FakeProcessor failNext(String operation, Fault fault) {
        faults.computeIfAbsent(operation, k -> new ArrayDeque<>()).add(fault);
        return this;
    }

    int sideEffects(String operation) { return sideEffects.getOrDefault(operation, 0); }
    int replays() { return replays; }

    private ProcessorResponse call(String operation, String key, String declineCode) {
        ProcessorResponse stored = byKey.get(key);
        if (stored != null) { replays++; return stored; }
        Deque<Fault> q = faults.get(operation);
        Fault fault = (q == null || q.isEmpty()) ? null : q.poll();
        if (fault == Fault.OUTAGE) throw new ProcessorUnavailableException(name + "." + operation + ": connection reset");
        ProcessorResponse response;
        if (fault == Fault.DECLINE) {
            response = ProcessorResponse.declined(declineCode);
        } else {
            sideEffects.merge(operation, 1, Integer::sum);
            response = ProcessorResponse.approved(String.format("%s_%s_%04d", name, operation, ++sequence));
        }
        byKey.put(key, response);
        if (fault == Fault.CRASH_AFTER_COMMIT) throw new ProcessCrashException("process died after " + name + "." + operation + " committed");
        return response;
    }

    @Override public ProcessorResponse tokenize(String key, String pan) { return call("tokenize", key, "invalid_card"); }
    @Override public ProcessorResponse deleteToken(String key, String token) { return call("deleteToken", key, "token_not_found"); }
    @Override public ProcessorResponse authorize(String key, String token, Money amount) { return call("authorize", key, "05"); }
    @Override public ProcessorResponse voidAuth(String key, String authRef) { return call("voidAuth", key, "already_captured"); }
    @Override public ProcessorResponse capture(String key, String authRef, Money amount) { return call("capture", key, "auth_expired"); }
    @Override public ProcessorResponse refund(String key, String captureRef, Money amount) { return call("refund", key, "insufficient_merchant_balance"); }
}

/** A two-account ledger with idempotent postings and reversals. The full ledger is Phase 10, Lesson 03. */
final class Ledger {
    record Entry(String entryId, String paymentId, String debitAccount, String creditAccount, Money amount) {}

    private final Map<String, Entry> byKey = new HashMap<>();
    private final List<Entry> entries = new ArrayList<>();
    private final Deque<Fault> faults = new ArrayDeque<>();
    private int sequence = 0;

    Ledger failNext(Fault fault) { faults.add(fault); return this; }

    String post(String key, String paymentId, Money amount) {
        return append(key, paymentId, "acquirer_receivable", "merchant_payable", amount);
    }

    String reverse(String key, String paymentId, Money amount) {
        return append(key, paymentId, "merchant_payable", "acquirer_receivable", amount);
    }

    private String append(String key, String paymentId, String debit, String credit, Money amount) {
        Entry stored = byKey.get(key);
        if (stored != null) return stored.entryId();
        Fault f = faults.poll();
        if (f == Fault.OUTAGE) throw new ProcessorUnavailableException("ledger: write timeout");
        Entry e = new Entry(String.format("le_%04d", ++sequence), paymentId, debit, credit, amount);
        byKey.put(key, e);
        entries.add(e);
        if (f == Fault.CRASH_AFTER_COMMIT) throw new ProcessCrashException("process died after ledger post committed");
        return e.entryId();
    }

    /** Net movement into merchant_payable for one payment; zero after a completed compensation. */
    Money netMerchantPayable(String paymentId, Currency currency) {
        Money net = Money.zero(currency);
        for (Entry e : entries) {
            if (!e.paymentId().equals(paymentId)) continue;
            if (e.creditAccount().equals("merchant_payable")) net = net.plus(e.amount());
            if (e.debitAccount().equals("merchant_payable")) net = net.minus(e.amount());
        }
        return net;
    }

    List<Entry> entries() { return Collections.unmodifiableList(entries); }
}

// ---------------------------------------------------------------------------------------------
// Dead letters: sagas the orchestrator could not finish compensating. A human, not a loop, owns them.
// ---------------------------------------------------------------------------------------------

record DeadLetter(String sagaId, SagaStep step, String reason, long atMillis) {}

// ---------------------------------------------------------------------------------------------
// The orchestrator. Load, do one thing, save. Repeat until terminal or until a crash propagates.
// ---------------------------------------------------------------------------------------------

final class Orchestrator {
    private final SagaStore store;
    private final Processor vault;
    private final Processor acquirer;
    private final Ledger ledger;
    private final Clock clock;
    private final int maxAttempts;
    private final List<DeadLetter> deadLetters = new ArrayList<>();

    Orchestrator(SagaStore store, Processor vault, Processor acquirer, Ledger ledger, Clock clock, int maxAttempts) {
        this.store = store; this.vault = vault; this.acquirer = acquirer; this.ledger = ledger; this.clock = clock; this.maxAttempts = maxAttempts;
    }

    List<DeadLetter> deadLetters() { return Collections.unmodifiableList(deadLetters); }

    /** The idempotency key for a step is a pure function of the saga and the step: retries and recoveries reuse it. */
    static String key(SagaRecord r, SagaStep step, boolean compensation) {
        return r.sagaId + ":" + step.name().toLowerCase() + (compensation ? ":comp" : ":fwd");
    }

    SagaRecord start(String sagaId, String paymentId, Money amount, String pan) {
        SagaRecord r = new SagaRecord(sagaId, paymentId, amount, pan);
        store.save(r);
        return run(sagaId);
    }

    /** Drive a saga until it is terminal or dead-lettered. A ProcessCrashException propagates: the process is gone. */
    SagaRecord run(String sagaId) {
        SagaRecord r = store.load(sagaId);
        while (true) {
            switch (r.state) {
                case RUNNING -> {
                    SagaStep step = r.nextForward();
                    if (step == null) { r.state = SagaState.COMPLETED; r.journal.add("completed"); store.save(r); return r; }
                    r.forwardAttempts.merge(step, 1, Integer::sum);
                    ProcessorResponse response;
                    try {
                        response = forward(step, r);
                    } catch (ProcessorUnavailableException e) {
                        r.journal.add(step + " attempt " + r.forwardAttempts.get(step) + " failed: " + e.getMessage());
                        if (r.forwardAttempts.get(step) >= maxAttempts) r.beginCompensation(step + " exhausted " + maxAttempts + " attempts: " + e.getMessage());
                        store.save(r);
                        continue;
                    }
                    if (!response.approved()) {
                        r.beginCompensation(step + " declined with code " + response.code());
                        store.save(r);
                        continue;
                    }
                    r.markDone(step, response.reference());
                    store.save(r);
                }
                case COMPENSATING -> {
                    SagaStep step = r.nextCompensation();
                    if (step == null) { r.state = SagaState.COMPENSATED; r.journal.add("compensated"); store.save(r); return r; }
                    r.compensationAttempts.merge(step, 1, Integer::sum);
                    ProcessorResponse response;
                    try {
                        response = compensate(step, r);
                    } catch (ProcessorUnavailableException e) {
                        r.journal.add(step.compensation + " attempt " + r.compensationAttempts.get(step) + " failed: " + e.getMessage());
                        if (r.compensationAttempts.get(step) >= maxAttempts) deadLetter(r, step, e.getMessage());
                        store.save(r);
                        if (r.state == SagaState.DEAD_LETTERED) return r;
                        continue;
                    }
                    if (!response.approved()) {
                        deadLetter(r, step, step.compensation + " refused with code " + response.code());
                        store.save(r);
                        return r;
                    }
                    r.markCompensated(step, response.reference().startsWith("noop:") ? response.reference() : step.compensation);
                    store.save(r);
                }
                case COMPLETED, COMPENSATED, DEAD_LETTERED -> { return r; }
            }
        }
    }

    private ProcessorResponse forward(SagaStep step, SagaRecord r) {
        String k = key(r, step, false);
        return switch (step) {
            case TOKENIZE -> vault.tokenize(k, r.pan);
            case AUTHORIZE -> acquirer.authorize(k, r.references.get(SagaStep.TOKENIZE), r.amount);
            case CAPTURE -> acquirer.capture(k, r.references.get(SagaStep.AUTHORIZE), r.amount);
            case LEDGER_POST -> ProcessorResponse.approved(ledger.post(k, r.paymentId, r.amount));
        };
    }

    private ProcessorResponse compensate(SagaStep step, SagaRecord r) {
        String k = key(r, step, true);
        return switch (step) {
            case TOKENIZE -> vault.deleteToken(k, r.references.get(SagaStep.TOKENIZE));
            // A captured authorization is consumed; the refund already returned the money. Voiding it would be
            // refused by the acquirer, so the compensation is semantic: nothing to undo.
            case AUTHORIZE -> r.steps.get(SagaStep.CAPTURE) == StepStatus.COMPENSATED
                    ? ProcessorResponse.approved("noop:auth-consumed-by-capture")
                    : acquirer.voidAuth(k, r.references.get(SagaStep.AUTHORIZE));
            case CAPTURE -> acquirer.refund(k, r.references.get(SagaStep.CAPTURE), r.amount);
            case LEDGER_POST -> ProcessorResponse.approved(ledger.reverse(k, r.paymentId, r.amount));
        };
    }

    private void deadLetter(SagaRecord r, SagaStep step, String reason) {
        r.state = SagaState.DEAD_LETTERED;
        r.journal.add("dead-lettered at " + step.compensation + ": " + reason);
        deadLetters.add(new DeadLetter(r.sagaId, step, reason, clock.nowMillis()));
    }

    /** After a restart: resume every saga the store says is unfinished. Each resumes from its persisted step. */
    int recover() {
        int resumed = 0;
        for (SagaRecord r : store.unfinished()) { run(r.sagaId); resumed++; }
        return resumed;
    }

    /** Operator action on a dead letter: the downstream is back, try the remaining compensations again. */
    SagaRecord redrive(String sagaId) {
        SagaRecord r = store.load(sagaId);
        if (r.state != SagaState.DEAD_LETTERED) throw new IllegalStateException(sagaId + " is " + r.state + ", not DEAD_LETTERED");
        r.state = SagaState.COMPENSATING;
        for (SagaStep s : SagaStep.values()) r.compensationAttempts.put(s, 0);
        r.journal.add("redriven by operator");
        store.save(r);
        deadLetters.removeIf(d -> d.sagaId().equals(sagaId));
        return run(sagaId);
    }
}

// ---------------------------------------------------------------------------------------------
// Demo
// ---------------------------------------------------------------------------------------------

public final class Main {
    public static void main(String[] args) {
        Currency USD = Currency.USD;
        FakeClock clock = new FakeClock(1_700_000_000_000L);
        SagaStore store = new SagaStore();
        FakeProcessor vault = new FakeProcessor("vault");
        FakeProcessor acquirer = new FakeProcessor("acquirer");
        Ledger ledger = new Ledger();
        Orchestrator orch = new Orchestrator(store, vault, acquirer, ledger, clock, 3);

        System.out.println("== The saga: four steps, four compensations ==");
        for (SagaStep s : SagaStep.values()) System.out.printf("%-12s on %-9s undone by %s%n", s, s.system, s.compensation);

        System.out.println();
        System.out.println("== Happy path: tokenize -> authorize -> capture -> ledger post ==");
        SagaRecord r = orch.start("saga_1", "pay_1001", Money.ofMinor(4999, USD), "4242424242424242");
        System.out.println(r);
        for (String line : r.journal) System.out.println("  " + line);
        System.out.println("merchant_payable for pay_1001: " + ledger.netMerchantPayable("pay_1001", USD));

        System.out.println();
        System.out.println("== Decline at CAPTURE: compensations run in reverse order (void, delete token) ==");
        acquirer.failNext("capture", Fault.DECLINE);
        r = orch.start("saga_2", "pay_1002", Money.ofMinor(1250, USD), "4000000000000002");
        System.out.println(r + "  reason: " + r.failureReason);
        for (String line : r.journal) System.out.println("  " + line);
        System.out.println("merchant_payable for pay_1002: " + ledger.netMerchantPayable("pay_1002", USD));

        System.out.println();
        System.out.println("== Outage at AUTHORIZE: retried up to the budget, then compensated ==");
        acquirer.failNext("authorize", Fault.OUTAGE).failNext("authorize", Fault.OUTAGE);
        r = orch.start("saga_3", "pay_1003", Money.ofMinor(800, USD), "4242424242424242");
        System.out.println(r + "  authorize attempts: " + r.forwardAttempts.get(SagaStep.AUTHORIZE));
        acquirer.failNext("authorize", Fault.OUTAGE).failNext("authorize", Fault.OUTAGE).failNext("authorize", Fault.OUTAGE);
        r = orch.start("saga_4", "pay_1004", Money.ofMinor(800, USD), "4242424242424242");
        System.out.println(r + "  reason: " + r.failureReason);

        System.out.println();
        System.out.println("== Crash after the acquirer committed the authorization but before we saved ==");
        int authsBefore = acquirer.sideEffects("authorize");
        acquirer.failNext("authorize", Fault.CRASH_AFTER_COMMIT);
        try {
            orch.start("saga_5", "pay_1005", Money.ofMinor(2000, USD), "4242424242424242");
        } catch (ProcessCrashException e) {
            System.out.println("crash: " + e.getMessage());
        }
        SagaRecord persisted = store.load("saga_5");
        System.out.println("persisted after crash: " + persisted + "  (AUTHORIZE still PENDING; acquirer holds 1 auth)");
        Orchestrator restarted = new Orchestrator(store, vault, acquirer, ledger, clock, 3);
        System.out.println("recover() resumed " + restarted.recover() + " saga(s)");
        r = store.load("saga_5");
        System.out.println("after recovery: " + r + "  acquirer authorizations: " + (acquirer.sideEffects("authorize") - authsBefore)
                + ", idempotent replays: " + acquirer.replays());

        System.out.println();
        System.out.println("== Ledger post fails: the capture must be refunded, not voided ==");
        ledger.failNext(Fault.OUTAGE).failNext(Fault.OUTAGE).failNext(Fault.OUTAGE);
        r = orch.start("saga_6", "pay_1006", Money.ofMinor(3000, USD), "4242424242424242");
        System.out.println(r + "  reason: " + r.failureReason);
        for (String line : r.journal) if (line.contains("compensated via")) System.out.println("  " + line);
        System.out.println("acquirer refunds so far: " + acquirer.sideEffects("refund") + ", merchant_payable for pay_1006: " + ledger.netMerchantPayable("pay_1006", USD));

        System.out.println();
        System.out.println("== Compensation itself fails: dead letter, then operator redrive ==");
        acquirer.failNext("capture", Fault.DECLINE);
        for (int i = 0; i < 3; i++) acquirer.failNext("voidAuth", Fault.OUTAGE);
        r = orch.start("saga_7", "pay_1007", Money.ofMinor(5500, USD), "4242424242424242");
        System.out.println(r);
        for (DeadLetter d : orch.deadLetters()) System.out.println("  dead letter: " + d.sagaId() + " at " + d.step().compensation + " (" + d.reason() + ")");
        clock.advance(15 * 60_000L);
        r = orch.redrive("saga_7");
        System.out.println("after redrive: " + r + "  dead letters remaining: " + orch.deadLetters().size());

        System.out.println();
        System.out.println("== Running a finished saga again is a no-op ==");
        int before = acquirer.sideEffects("capture");
        orch.run("saga_1");
        System.out.println("saga_1 still " + store.load("saga_1").state + ", captures unchanged: " + (acquirer.sideEffects("capture") == before));
    }
}
