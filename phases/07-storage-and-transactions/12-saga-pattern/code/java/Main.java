// Lesson: phases/07-storage-and-transactions/12-saga-pattern/docs/en.md
// The Saga Pattern: Compensations Instead of Locks
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: Garcia-Molina & Salem, "Sagas" (SIGMOD 1987); Richardson, "Microservices Patterns" ch. 4 (semantic lock).
// Java 21, standard library only. Money is integer minor units (Phase 10, Lesson 01).

import java.util.ArrayList;
import java.util.HashMap;
import java.util.LinkedHashMap;
import java.util.LinkedHashSet;
import java.util.List;
import java.util.Map;
import java.util.Objects;
import java.util.Set;

/** ISO 4217 currency, copied from Phase 10 Lesson 01. */
record Currency(String code, int exponent) {
    static final Currency USD = new Currency("USD", 2);

    long minorUnitsPerMajor() {
        long scale = 1;
        for (int i = 0; i < exponent; i++) scale *= 10;
        return scale;
    }
}

final class CurrencyMismatchException extends IllegalArgumentException {
    CurrencyMismatchException(Currency a, Currency b) { super("cannot combine " + a.code() + " with " + b.code()); }
}

/** Exact amount of one currency in signed minor units. Copied from Phase 10 Lesson 01. */
record Money(long minorUnits, Currency currency) implements Comparable<Money> {
    Money { Objects.requireNonNull(currency, "currency"); }

    static Money ofMinor(long minorUnits, Currency currency) { return new Money(minorUnits, currency); }
    static Money zero(Currency currency) { return new Money(0, currency); }

    private void requireSameCurrency(Money other) {
        if (!currency.equals(other.currency)) throw new CurrencyMismatchException(currency, other.currency);
    }

    Money plus(Money other) { requireSameCurrency(other); return new Money(Math.addExact(minorUnits, other.minorUnits), currency); }
    Money minus(Money other) { requireSameCurrency(other); return new Money(Math.subtractExact(minorUnits, other.minorUnits), currency); }
    boolean isNegative() { return minorUnits < 0; }

    @Override public int compareTo(Money other) { requireSameCurrency(other); return Long.compare(minorUnits, other.minorUnits); }

    @Override public String toString() {
        long magnitude = Math.abs(minorUnits);
        long scale = currency.minorUnitsPerMajor();
        String text = Long.toString(magnitude / scale);
        if (currency.exponent() > 0) {
            String frac = Long.toString(magnitude % scale);
            text = text + "." + "0".repeat(currency.exponent() - frac.length()) + frac;
        }
        return (minorUnits < 0 ? "-" : "") + text + " " + currency.code();
    }
}

/** A clock that only moves when the simulation says so. Timestamps in the saga log come from here. */
final class SimClock {
    private long nowMs;
    SimClock(long startMs) { this.nowMs = startMs; }
    long now() { return nowMs; }
    void advance(long ms) { nowMs += ms; }
}

/** A step refused to do its work. The saga reacts by compensating everything before it. */
final class StepFailedException extends RuntimeException {
    StepFailedException(String message) { super(message); }
}

/** Thrown at an injected crash point. The simulation catches it where the real process would die. */
final class CrashException extends RuntimeException {
    CrashException(String where) { super("crash at " + where); }
}

/** Where the orchestrator dies: after a step's side effect happened but before the log recorded it. */
record CrashPoint(String step, boolean duringCompensation) {
    static final CrashPoint NONE = new CrashPoint("", false);
    static CrashPoint afterExecute(String step) { return new CrashPoint(step, false); }
    static CrashPoint afterCompensate(String step) { return new CrashPoint(step, true); }
}

/** The business request the saga carries from step to step. */
record Order(String sagaId, String customer, String sku, int quantity, Money amount) { }

enum SagaStatus { RUNNING, COMPENSATING, COMPLETED, COMPENSATED }

/** One durable saga-log record. The orchestrator appends one before and one after every step and compensation. */
record SagaRecord(long seq, long atMs, String sagaId, String kind, String step, String detail, Order order) {
    @Override public String toString() {
        return seq + " t=" + atMs + " " + sagaId + " " + kind + (step.isEmpty() ? "" : " " + step) + (detail.isEmpty() ? "" : " (" + detail + ")");
    }
}

/** Append-only saga log. Append is the durability point. The SAGA_STARTED record carries the Order so recovery can rebuild it. */
final class SagaLog {
    static final String SAGA_STARTED = "SAGA_STARTED", STEP_STARTED = "STEP_STARTED", STEP_COMPLETED = "STEP_COMPLETED",
            STEP_FAILED = "STEP_FAILED", COMP_STARTED = "COMP_STARTED", COMP_COMPLETED = "COMP_COMPLETED",
            SAGA_COMPLETED = "SAGA_COMPLETED", SAGA_COMPENSATED = "SAGA_COMPENSATED";

    private final List<SagaRecord> records = new ArrayList<>();
    private final SimClock clock;
    SagaLog(SimClock clock) { this.clock = clock; }

    void append(String sagaId, String kind, String step, String detail, Order order) {
        records.add(new SagaRecord(records.size() + 1, clock.now(), sagaId, kind, step, detail, order));
    }
    void append(String sagaId, String kind, String step, String detail) { append(sagaId, kind, step, detail, null); }
    List<SagaRecord> records() { return List.copyOf(records); }
    List<SagaRecord> forSaga(String sagaId) { return records.stream().filter(r -> r.sagaId().equals(sagaId)).toList(); }
    Set<String> sagaIds() {
        Set<String> ids = new LinkedHashSet<>();
        for (SagaRecord r : records) ids.add(r.sagaId());
        return ids;
    }
}

/**
 * The wallet service. A reservation is a hold: it reduces what is available without moving the balance.
 * Every mutating call takes an idempotency key (the saga id) and replays its earlier answer on a repeat.
 */
final class WalletService {
    private final Map<String, Money> balances = new LinkedHashMap<>();
    private final Map<String, String> holdAccount = new LinkedHashMap<>();   // key -> account
    private final Map<String, Money> holdAmount = new LinkedHashMap<>();     // key -> amount still held
    private final Map<String, Money> captured = new LinkedHashMap<>();       // key -> amount captured
    private final Set<String> released = new LinkedHashSet<>();
    private final Set<String> refunded = new LinkedHashSet<>();
    int calls = 0;

    WalletService(Map<String, Money> opening) { balances.putAll(opening); }

    Money balance(String account) { return balances.get(account); }

    /** Balance minus every live hold on the account: the number a second saga must consult. */
    Money available(String account) {
        Money a = balances.get(account);
        for (Map.Entry<String, String> e : holdAccount.entrySet()) {
            if (e.getValue().equals(account)) a = a.minus(holdAmount.get(e.getKey()));
        }
        return a;
    }

    boolean hasHold(String key) { return holdAmount.containsKey(key) && !holdAmount.get(key).equals(Money.zero(holdAmount.get(key).currency())); }
    boolean isCaptured(String key) { return captured.containsKey(key) && !refunded.contains(key); }

    /** Step 1: reserve. Fails if the available balance cannot cover the amount. Idempotent on key. */
    void reserve(String key, String account, Money amount) {
        calls++;
        if (holdAccount.containsKey(key)) return;                               // replay
        if (!balances.containsKey(account)) throw new StepFailedException("unknown account " + account);
        if (available(account).compareTo(amount) < 0) {
            throw new StepFailedException("insufficient available funds: " + available(account) + " < " + amount);
        }
        holdAccount.put(key, account);
        holdAmount.put(key, amount);
    }

    /** Compensation of reserve: drop the hold. Legal even if the hold never existed (the step may not have run). */
    void release(String key) {
        calls++;
        if (released.contains(key)) return;
        if (holdAccount.containsKey(key)) {
            holdAmount.put(key, Money.zero(holdAmount.get(key).currency()));
            holdAccount.remove(key);
        }
        released.add(key);
    }

    /** Step 2: capture. Turns the hold into a debit. Idempotent on key. */
    void capture(String key) {
        calls++;
        if (captured.containsKey(key)) return;                                  // replay
        if (!holdAccount.containsKey(key)) throw new StepFailedException("no hold to capture for " + key);
        String account = holdAccount.get(key);
        Money amount = holdAmount.get(key);
        balances.put(account, balances.get(account).minus(amount));
        holdAmount.put(key, Money.zero(amount.currency()));
        holdAccount.remove(key);
        captured.put(key, amount);
    }

    /** Compensation of capture: credit the money back. Not an undo: the debit and the refund both stay in history. */
    void refund(String key, String account) {
        calls++;
        if (refunded.contains(key)) return;
        if (captured.containsKey(key)) balances.put(account, balances.get(account).plus(captured.get(key)));
        refunded.add(key);
    }
}

/** The shipping service: stock per SKU and shipments keyed by saga id. */
final class ShippingService {
    private final Map<String, Integer> stock = new LinkedHashMap<>();
    private final Map<String, Integer> shipments = new LinkedHashMap<>();     // key -> quantity shipped
    private final Set<String> cancelled = new LinkedHashSet<>();
    int calls = 0;

    ShippingService(Map<String, Integer> opening) { stock.putAll(opening); }

    int stockOf(String sku) { return stock.getOrDefault(sku, 0); }
    boolean isShipped(String key) { return shipments.containsKey(key) && !cancelled.contains(key); }

    /** Step 3: ship. Fails when stock is short. Idempotent on key. */
    void ship(String key, String sku, int quantity) {
        calls++;
        if (shipments.containsKey(key)) return;                                 // replay
        if (stockOf(sku) < quantity) throw new StepFailedException("out of stock: " + sku + " has " + stockOf(sku) + ", need " + quantity);
        stock.put(sku, stockOf(sku) - quantity);
        shipments.put(key, quantity);
    }

    /** Compensation of ship: cancel and restock. */
    void cancelShipment(String key, String sku) {
        calls++;
        if (cancelled.contains(key)) return;
        if (shipments.containsKey(key)) stock.put(sku, stockOf(sku) + shipments.get(key));
        cancelled.add(key);
    }
}

/** A saga step: a forward action and the compensation that semantically undoes it. Both must be idempotent. */
interface SagaStep {
    String name();
    void execute(Order order);
    void compensate(Order order);
}

final class ReserveFunds implements SagaStep {
    private final WalletService wallet;
    ReserveFunds(WalletService wallet) { this.wallet = wallet; }
    public String name() { return "reserve-funds"; }
    public void execute(Order o) { wallet.reserve(o.sagaId(), o.customer(), o.amount()); }
    public void compensate(Order o) { wallet.release(o.sagaId()); }
}

final class CaptureFunds implements SagaStep {
    private final WalletService wallet;
    CaptureFunds(WalletService wallet) { this.wallet = wallet; }
    public String name() { return "capture-funds"; }
    public void execute(Order o) { wallet.capture(o.sagaId()); }
    public void compensate(Order o) { wallet.refund(o.sagaId(), o.customer()); }
}

final class ShipOrder implements SagaStep {
    private final ShippingService shipping;
    ShipOrder(ShippingService shipping) { this.shipping = shipping; }
    public String name() { return "ship-order"; }
    public void execute(Order o) { shipping.ship(o.sagaId(), o.sku(), o.quantity()); }
    public void compensate(Order o) { shipping.cancelShipment(o.sagaId(), o.sku()); }
}

/** What the orchestrator reports. */
record SagaOutcome(String sagaId, SagaStatus status, String failedStep, String reason) {
    @Override public String toString() { return sagaId + " -> " + status + (failedStep.isEmpty() ? "" : " (failed at " + failedStep + ": " + reason + ")"); }
}

/**
 * Orchestrated saga. Runs the steps in order; on failure runs the compensations of every completed step in
 * reverse order. The log is appended before and after every action so a restart can resume from the log alone.
 */
final class SagaOrchestrator {
    private final SagaLog log;
    private final List<SagaStep> steps;
    private final SimClock clock;
    private final long stepLatencyMs;
    CrashPoint crashPoint = CrashPoint.NONE;

    SagaOrchestrator(SagaLog log, List<SagaStep> steps, SimClock clock, long stepLatencyMs) {
        this.log = log; this.steps = List.copyOf(steps); this.clock = clock; this.stepLatencyMs = stepLatencyMs;
    }

    SagaLog log() { return log; }

    SagaOutcome run(Order order) {
        log.append(order.sagaId(), SagaLog.SAGA_STARTED, "", "", order);
        return forward(order, 0);
    }

    /** Execute steps from index `from`. A failure at step i compensates i-1 .. 0. */
    private SagaOutcome forward(Order order, int from) {
        for (int i = from; i < steps.size(); i++) {
            SagaStep step = steps.get(i);
            log.append(order.sagaId(), SagaLog.STEP_STARTED, step.name(), "");
            clock.advance(stepLatencyMs);
            try {
                step.execute(order);
            } catch (StepFailedException e) {
                log.append(order.sagaId(), SagaLog.STEP_FAILED, step.name(), e.getMessage());
                return backward(order, i - 1, step.name(), e.getMessage());
            }
            if (crashPoint.equals(CrashPoint.afterExecute(step.name()))) throw new CrashException("after " + step.name() + " executed, before STEP_COMPLETED logged");
            log.append(order.sagaId(), SagaLog.STEP_COMPLETED, step.name(), "");
        }
        log.append(order.sagaId(), SagaLog.SAGA_COMPLETED, "", "");
        return new SagaOutcome(order.sagaId(), SagaStatus.COMPLETED, "", "");
    }

    /** Compensate steps from index `from` down to 0. Compensations are not allowed to fail; they are retried. */
    private SagaOutcome backward(Order order, int from, String failedStep, String reason) {
        for (int i = from; i >= 0; i--) {
            SagaStep step = steps.get(i);
            log.append(order.sagaId(), SagaLog.COMP_STARTED, step.name(), "");
            clock.advance(stepLatencyMs);
            step.compensate(order);
            if (crashPoint.equals(CrashPoint.afterCompensate(step.name()))) throw new CrashException("after " + step.name() + " compensated, before COMP_COMPLETED logged");
            log.append(order.sagaId(), SagaLog.COMP_COMPLETED, step.name(), "");
        }
        log.append(order.sagaId(), SagaLog.SAGA_COMPENSATED, "", "");
        return new SagaOutcome(order.sagaId(), SagaStatus.COMPENSATED, failedStep, reason);
    }

    /** Where a saga stands according to the log alone. */
    static SagaStatus statusOf(List<SagaRecord> records) {
        SagaStatus status = SagaStatus.RUNNING;
        for (SagaRecord r : records) {
            switch (r.kind()) {
                case SagaLog.STEP_FAILED, SagaLog.COMP_STARTED -> status = SagaStatus.COMPENSATING;
                case SagaLog.SAGA_COMPLETED -> status = SagaStatus.COMPLETED;
                case SagaLog.SAGA_COMPENSATED -> status = SagaStatus.COMPENSATED;
                default -> { }
            }
        }
        return status;
    }

    /**
     * Resume every unfinished saga from the log. A STEP_STARTED without STEP_COMPLETED is re-executed: the step
     * may or may not have happened, and idempotency makes re-execution safe either way. Likewise for compensations.
     */
    List<SagaOutcome> recover() {
        crashPoint = CrashPoint.NONE;
        List<SagaOutcome> resumed = new ArrayList<>();
        for (String sagaId : log.sagaIds()) {
            List<SagaRecord> records = log.forSaga(sagaId);
            SagaStatus status = statusOf(records);
            if (status == SagaStatus.COMPLETED || status == SagaStatus.COMPENSATED) continue;
            Order order = records.get(0).order();
            Map<String, Integer> index = new HashMap<>();
            for (int i = 0; i < steps.size(); i++) index.put(steps.get(i).name(), i);
            int lastCompleted = -1, lastCompensated = steps.size();
            String failedStep = "", reason = "";
            for (SagaRecord r : records) {
                switch (r.kind()) {
                    case SagaLog.STEP_COMPLETED -> lastCompleted = Math.max(lastCompleted, index.get(r.step()));
                    case SagaLog.STEP_FAILED -> { failedStep = r.step(); reason = r.detail(); }
                    case SagaLog.COMP_COMPLETED -> lastCompensated = Math.min(lastCompensated, index.get(r.step()));
                    default -> { }
                }
            }
            if (status == SagaStatus.RUNNING) {
                resumed.add(forward(order, lastCompleted + 1));
            } else {
                int from = Math.min(lastCompensated - 1, failedStep.isEmpty() ? steps.size() - 1 : index.get(failedStep) - 1);
                resumed.add(backward(order, from, failedStep, reason.isEmpty() ? "resumed after crash" : reason));
            }
        }
        return resumed;
    }
}

public final class Main {
    static Money usd(String text) {
        boolean neg = text.startsWith("-");
        String s = neg ? text.substring(1) : text;
        long minor = Long.parseLong(s.replace(".", ""));
        return Money.ofMinor(neg ? -minor : minor, Currency.USD);
    }

    static void show(WalletService wallet, ShippingService shipping) {
        System.out.println("  alice balance=" + wallet.balance("alice") + " available=" + wallet.available("alice")
                + " | widget stock=" + shipping.stockOf("widget"));
    }

    static void printLog(SagaLog log, String sagaId) {
        for (SagaRecord r : log.forSaga(sagaId)) System.out.println("    " + r);
    }

    public static void main(String[] args) {
        SimClock clock = new SimClock(0);
        WalletService wallet = new WalletService(Map.of("alice", usd("100.00")));
        ShippingService shipping = new ShippingService(Map.of("widget", 5));
        SagaLog log = new SagaLog(clock);
        List<SagaStep> steps = List.of(new ReserveFunds(wallet), new CaptureFunds(wallet), new ShipOrder(shipping));
        SagaOrchestrator saga = new SagaOrchestrator(log, steps, clock, 10);

        System.out.println("== 1. Happy path: alice buys 2 widgets for 30.00 USD ==");
        System.out.println("  " + saga.run(new Order("saga-1", "alice", "widget", 2, usd("30.00"))));
        show(wallet, shipping);
        printLog(log, "saga-1");

        System.out.println("\n== 2. Step three fails: 4 widgets wanted, 3 in stock. Compensate in reverse ==");
        System.out.println("  " + saga.run(new Order("saga-2", "alice", "widget", 4, usd("40.00"))));
        show(wallet, shipping);
        printLog(log, "saga-2");

        System.out.println("\n== 3. Crash after capture executed, before it was logged. Recovery re-runs capture; the key makes it a no-op ==");
        saga.crashPoint = CrashPoint.afterExecute("capture-funds");
        try {
            saga.run(new Order("saga-3", "alice", "widget", 1, usd("25.00")));
        } catch (CrashException e) {
            System.out.println("  orchestrator: " + e.getMessage());
        }
        System.out.println("  before recovery: balance=" + wallet.balance("alice") + " (already captured), log status="
                + SagaOrchestrator.statusOf(log.forSaga("saga-3")));
        System.out.println("  recover -> " + saga.recover());
        show(wallet, shipping);
        System.out.println("  wallet calls for saga-3 capture replayed once; balance debited once");

        System.out.println("\n== 4. Crash mid-compensation: refund done, not logged. Recovery re-runs refund; no double credit ==");
        saga.crashPoint = CrashPoint.afterCompensate("capture-funds");
        try {
            saga.run(new Order("saga-4", "alice", "widget", 9, usd("10.00")));
        } catch (CrashException e) {
            System.out.println("  orchestrator: " + e.getMessage());
        }
        System.out.println("  before recovery: balance=" + wallet.balance("alice") + " hold=" + wallet.hasHold("saga-4"));
        System.out.println("  recover -> " + saga.recover());
        show(wallet, shipping);

        System.out.println("\n== 5. The semantic lock: a hold is visible to a concurrent saga, and the trade-off it forces ==");
        WalletService w2 = new WalletService(Map.of("alice", usd("100.00")));
        ShippingService s2 = new ShippingService(Map.of("widget", 0));
        SagaOrchestrator saga2 = new SagaOrchestrator(new SagaLog(clock), List.of(new ReserveFunds(w2), new CaptureFunds(w2), new ShipOrder(s2)), clock, 10);
        saga2.crashPoint = CrashPoint.afterExecute("reserve-funds");
        try {
            saga2.run(new Order("saga-A", "alice", "widget", 1, usd("80.00")));
        } catch (CrashException e) {
            System.out.println("  saga-A reserved 80.00 and its orchestrator died: " + e.getMessage());
        }
        System.out.println("  alice balance=" + w2.balance("alice") + " available=" + w2.available("alice") + "  <- the hold is the semantic lock");
        System.out.println("  saga-B wants 50.00 while A is pending: " + saga2.run(new Order("saga-B", "alice", "widget", 1, usd("50.00"))));
        System.out.println("  saga-A resumes, fails at shipping, compensates: " + saga2.recover());
        System.out.println("  alice balance=" + w2.balance("alice") + " available=" + w2.available("alice"));
        System.out.println("  B was refused on a hold that A later released: that is the price of the lock. Without it B would have read 100.00 and alice would be overdrawn.");
        System.out.println("\nclock=" + clock.now() + " ms, log records=" + log.records().size());
    }
}
