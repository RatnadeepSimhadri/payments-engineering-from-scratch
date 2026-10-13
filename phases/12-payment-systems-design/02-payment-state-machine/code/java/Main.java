// Lesson: phases/12-payment-systems-design/02-payment-state-machine/docs/en.md
// The Payment State Machine
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: Stripe API reference (PaymentIntent status lifecycle); EMV 3-D Secure protocol spec (challenge flow);
//          Kung & Robinson, "On Optimistic Methods for Concurrency Control" (1981). Java 21, stdlib only.

import java.util.ArrayDeque;
import java.util.ArrayList;
import java.util.Collections;
import java.util.Deque;
import java.util.HashMap;
import java.util.HashSet;
import java.util.List;
import java.util.Map;
import java.util.Objects;
import java.util.Set;

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

// ---------------------------------------------------------------------------------------------
// Time is injected: the transition log carries deterministic timestamps and the fake processor
// delivers its callbacks when the clock, not a thread, says so.
// ---------------------------------------------------------------------------------------------

interface Clock { long nowMillis(); }

final class FakeClock implements Clock {
    private long now;
    FakeClock(long startMillis) { now = startMillis; }
    @Override public long nowMillis() { return now; }
    void advance(long millis) { now += millis; }
}

// ---------------------------------------------------------------------------------------------
// States and events. Three states are terminal: nothing moves a payment out of them.
// ---------------------------------------------------------------------------------------------

enum PaymentState {
    CREATED, REQUIRES_ACTION, PROCESSING, SUCCEEDED, REFUNDING, REFUNDED, FAILED, CANCELED;

    boolean isTerminal() { return this == REFUNDED || this == FAILED || this == CANCELED; }
}

enum EventKind {
    CONFIRM, REQUIRE_ACTION, ACTION_COMPLETED, PROCESSOR_APPROVED, PROCESSOR_DECLINED, CANCEL,
    REFUND_REQUESTED, REFUND_SUCCEEDED, REFUND_FAILED
}

/** An event carries an id so a redelivered callback is recognised, and an amount only for refund requests. */
record PaymentEvent(String eventId, EventKind kind, Money amount, String detail) {
    PaymentEvent {
        Objects.requireNonNull(eventId, "eventId");
        Objects.requireNonNull(kind, "kind");
        if (kind == EventKind.REFUND_REQUESTED && amount == null) {
            throw new IllegalArgumentException("REFUND_REQUESTED needs an amount");
        }
        if (kind != EventKind.REFUND_REQUESTED && amount != null) {
            throw new IllegalArgumentException(kind + " does not carry an amount");
        }
    }

    static PaymentEvent of(String eventId, EventKind kind) { return new PaymentEvent(eventId, kind, null, ""); }
    static PaymentEvent of(String eventId, EventKind kind, String detail) { return new PaymentEvent(eventId, kind, null, detail); }
    static PaymentEvent refundRequested(String eventId, Money amount) { return new PaymentEvent(eventId, EventKind.REFUND_REQUESTED, amount, ""); }
}

// ---------------------------------------------------------------------------------------------
// The transition table: data, not code. A rule fires when the payment is in `from`, the event is
// of `kind`, and the guard holds. Two rules may share (from, kind) with mutually exclusive guards.
// ---------------------------------------------------------------------------------------------

enum Guard {
    ALWAYS,                 // no condition
    REFUND_WITHIN_BALANCE,  // event.amount > 0 and refunded + event.amount <= amount
    REFUND_LEAVES_BALANCE,  // after settling the pending refund, refunded < amount
    REFUND_CLEARS_BALANCE   // after settling the pending refund, refunded == amount
}

record Rule(PaymentState from, EventKind kind, Guard guard, PaymentState to) {}

final class IllegalTransitionException extends IllegalStateException {
    IllegalTransitionException(String message) { super(message); }
}

final class TransitionTable {
    static final List<Rule> RULES = List.of(
            new Rule(PaymentState.CREATED, EventKind.CONFIRM, Guard.ALWAYS, PaymentState.PROCESSING),
            new Rule(PaymentState.CREATED, EventKind.CANCEL, Guard.ALWAYS, PaymentState.CANCELED),
            new Rule(PaymentState.PROCESSING, EventKind.REQUIRE_ACTION, Guard.ALWAYS, PaymentState.REQUIRES_ACTION),
            new Rule(PaymentState.PROCESSING, EventKind.PROCESSOR_APPROVED, Guard.ALWAYS, PaymentState.SUCCEEDED),
            new Rule(PaymentState.PROCESSING, EventKind.PROCESSOR_DECLINED, Guard.ALWAYS, PaymentState.FAILED),
            new Rule(PaymentState.REQUIRES_ACTION, EventKind.ACTION_COMPLETED, Guard.ALWAYS, PaymentState.PROCESSING),
            new Rule(PaymentState.REQUIRES_ACTION, EventKind.CANCEL, Guard.ALWAYS, PaymentState.CANCELED),
            new Rule(PaymentState.SUCCEEDED, EventKind.REFUND_REQUESTED, Guard.REFUND_WITHIN_BALANCE, PaymentState.REFUNDING),
            new Rule(PaymentState.REFUNDING, EventKind.REFUND_SUCCEEDED, Guard.REFUND_LEAVES_BALANCE, PaymentState.SUCCEEDED),
            new Rule(PaymentState.REFUNDING, EventKind.REFUND_SUCCEEDED, Guard.REFUND_CLEARS_BALANCE, PaymentState.REFUNDED),
            new Rule(PaymentState.REFUNDING, EventKind.REFUND_FAILED, Guard.ALWAYS, PaymentState.SUCCEEDED));

    /** True when (from, kind) appears in the table at all, ignoring guards. */
    static boolean hasRow(PaymentState from, EventKind kind) {
        return RULES.stream().anyMatch(r -> r.from() == from && r.kind() == kind);
    }

    /** Pick the one rule whose guard holds, or explain precisely why the event is refused. */
    static Rule resolve(Payment p, PaymentEvent event) {
        if (p.state().isTerminal()) {
            throw new IllegalTransitionException(p.id() + " is " + p.state() + " (terminal); " + event.kind() + " refused");
        }
        List<Rule> candidates = RULES.stream().filter(r -> r.from() == p.state() && r.kind() == event.kind()).toList();
        if (candidates.isEmpty()) {
            throw new IllegalTransitionException(p.id() + ": " + event.kind() + " is not legal from " + p.state());
        }
        for (Rule r : candidates) {
            if (holds(r.guard(), p, event)) return r;
        }
        throw new IllegalTransitionException(p.id() + ": " + event.kind() + " from " + p.state() + " refused by guard "
                + candidates.get(0).guard() + " (" + guardFailure(candidates.get(0).guard(), p, event) + ")");
    }

    static boolean holds(Guard g, Payment p, PaymentEvent e) {
        return switch (g) {
            case ALWAYS -> true;
            case REFUND_WITHIN_BALANCE -> e.amount().isPositive() && p.refunded().plus(e.amount()).compareTo(p.amount()) <= 0;
            case REFUND_LEAVES_BALANCE -> p.refunded().plus(p.pendingRefund()).compareTo(p.amount()) < 0;
            case REFUND_CLEARS_BALANCE -> p.refunded().plus(p.pendingRefund()).equals(p.amount());
        };
    }

    private static String guardFailure(Guard g, Payment p, PaymentEvent e) {
        if (g == Guard.REFUND_WITHIN_BALANCE && !e.amount().isPositive()) return "refund amount must be positive";
        if (g == Guard.REFUND_WITHIN_BALANCE) return "refund " + e.amount() + " exceeds refundable " + p.refundable();
        return "balance guard failed";
    }
}

// ---------------------------------------------------------------------------------------------
// The persisted transition. Version n is the n-th change to the payment; the store refuses to
// append version n unless it currently holds n-1 (optimistic concurrency control).
// ---------------------------------------------------------------------------------------------

record Transition(String paymentId, int version, String eventId, EventKind kind, PaymentState fromState,
                  PaymentState toState, Money amount, String reason, long atMillis) {
    @Override public String toString() {
        return String.format("v%-2d %-20s %-16s -> %-16s %s", version, kind, fromState, toState, reason);
    }
}

final class ConcurrencyConflictException extends IllegalStateException {
    ConcurrencyConflictException(String message) { super(message); }
}

enum ApplyOutcome { APPLIED, DUPLICATE }

// ---------------------------------------------------------------------------------------------
// The aggregate. Every change goes through `apply`; nothing else mutates state.
// ---------------------------------------------------------------------------------------------

final class Payment {
    private final String id;
    private final Money amount;
    private final Clock clock;
    private PaymentState state = PaymentState.CREATED;
    private Money refunded;
    private Money pendingRefund;
    private int version = 0;
    private final List<Transition> history = new ArrayList<>();
    private final List<Transition> uncommitted = new ArrayList<>();
    private final Set<String> appliedEventIds = new HashSet<>();

    Payment(String id, Money amount, Clock clock) {
        if (!amount.isPositive()) throw new IllegalArgumentException("amount must be positive: " + amount);
        this.id = id;
        this.amount = amount;
        this.clock = clock;
        this.refunded = Money.zero(amount.currency());
        this.pendingRefund = Money.zero(amount.currency());
    }

    String id() { return id; }
    Money amount() { return amount; }
    Money refunded() { return refunded; }
    Money pendingRefund() { return pendingRefund; }
    Money refundable() { return amount.minus(refunded).minus(pendingRefund); }
    PaymentState state() { return state; }
    int version() { return version; }
    List<Transition> history() { return Collections.unmodifiableList(history); }
    List<Transition> uncommitted() { return Collections.unmodifiableList(uncommitted); }

    /** Pure check: would this event be accepted right now? */
    boolean canApply(PaymentEvent event) {
        if (appliedEventIds.contains(event.eventId())) return false;
        try { TransitionTable.resolve(this, event); return true; } catch (IllegalTransitionException e) { return false; }
    }

    /**
     * Exactly one of three things happens: the state moves and one transition is recorded; the event id
     * was already applied and nothing changes (DUPLICATE); or IllegalTransitionException and nothing changes.
     */
    ApplyOutcome apply(PaymentEvent event, String reason) {
        if (appliedEventIds.contains(event.eventId())) return ApplyOutcome.DUPLICATE;
        Rule rule = TransitionTable.resolve(this, event);          // throws before any mutation
        Transition t = new Transition(id, version + 1, event.eventId(), event.kind(), state, rule.to(),
                event.amount(), reason, clock.nowMillis());
        mutate(t);
        uncommitted.add(t);
        return ApplyOutcome.APPLIED;
    }

    /** The single place state changes. Used by apply() and by replay(), so both agree by construction. */
    private void mutate(Transition t) {
        switch (t.kind()) {
            case REFUND_REQUESTED -> pendingRefund = pendingRefund.plus(t.amount());
            case REFUND_SUCCEEDED -> { refunded = refunded.plus(pendingRefund); pendingRefund = Money.zero(amount.currency()); }
            case REFUND_FAILED -> pendingRefund = Money.zero(amount.currency());
            default -> { }
        }
        state = t.toState();
        version = t.version();
        appliedEventIds.add(t.eventId());
        history.add(t);
    }

    /** Called by the store after it accepted every uncommitted transition. */
    void markCommitted() { uncommitted.clear(); }

    /** Rebuild a payment from its persisted history. A gap or a mismatched fromState is corruption, not data. */
    static Payment replay(String id, Money amount, List<Transition> transitions, Clock clock) {
        Payment p = new Payment(id, amount, clock);
        for (Transition t : transitions) {
            if (t.version() != p.version + 1) {
                throw new IllegalStateException(id + ": history gap, expected v" + (p.version + 1) + " got v" + t.version());
            }
            if (t.fromState() != p.state) {
                throw new IllegalStateException(id + ": v" + t.version() + " claims fromState " + t.fromState() + " but payment is " + p.state);
            }
            p.mutate(t);
        }
        return p;
    }
}

// ---------------------------------------------------------------------------------------------
// The store: an append-only transition log per payment, guarded by a version check. In production
// this is a table with a UNIQUE (payment_id, version) index; here it is a map and an `if`.
// ---------------------------------------------------------------------------------------------

final class PaymentStore {
    private record Created(Money amount, long createdAtMillis) {}

    private final Map<String, Created> created = new HashMap<>();
    private final Map<String, List<Transition>> log = new HashMap<>();
    private final Clock clock;

    PaymentStore(Clock clock) { this.clock = clock; }

    Payment create(String id, Money amount) {
        if (created.containsKey(id)) throw new IllegalStateException(id + " already exists");
        created.put(id, new Created(amount, clock.nowMillis()));
        log.put(id, new ArrayList<>());
        return new Payment(id, amount, clock);
    }

    int currentVersion(String id) { return history(id).size(); }

    List<Transition> history(String id) {
        List<Transition> h = log.get(id);
        if (h == null) throw new IllegalStateException("unknown payment " + id);
        return Collections.unmodifiableList(h);
    }

    /** Load by replaying the log. Two callers that load the same payment get two independent copies. */
    Payment load(String id) {
        Created c = created.get(id);
        if (c == null) throw new IllegalStateException("unknown payment " + id);
        return Payment.replay(id, c.amount(), history(id), clock);
    }

    /**
     * Append the payment's uncommitted transitions. Each must be exactly currentVersion + 1; otherwise
     * someone else committed first and the caller must reload and decide again. All-or-nothing.
     */
    void save(Payment p) {
        List<Transition> target = log.get(p.id());
        if (target == null) throw new IllegalStateException("unknown payment " + p.id());
        int expected = target.size();
        for (Transition t : p.uncommitted()) {
            if (t.version() != expected + 1) {
                throw new ConcurrencyConflictException(p.id() + ": expected to write v" + t.version()
                        + " but store is at v" + target.size() + "; reload and retry");
            }
            expected++;
        }
        target.addAll(p.uncommitted());
        p.markCommitted();
    }
}

// ---------------------------------------------------------------------------------------------
// The processor adapter. The real one is an HTTP client; the fake one answers from a script and
// delivers callbacks only when the injected clock has passed their due time.
// ---------------------------------------------------------------------------------------------

enum ProcessorOutcome { APPROVED, DECLINED, CHALLENGE, REFUND_OK, REFUND_FAILED }

record ProcessorCallback(String reference, String paymentId, ProcessorOutcome outcome, String detail, long dueAtMillis) {}

interface ProcessorAdapter {
    /** Start an authorization; the result arrives later as a callback carrying this reference. */
    String authorize(String paymentId, Money amount);
    /** Start a refund; the result arrives later as a callback. */
    String refund(String paymentId, Money amount);
    /** Callbacks whose due time has passed, in due order. Each is delivered once. */
    List<ProcessorCallback> dueCallbacks();
}

final class FakeProcessor implements ProcessorAdapter {
    private final Clock clock;
    private final long latencyMillis;
    private final Map<String, Deque<ProcessorOutcome>> script = new HashMap<>();
    private final List<ProcessorCallback> pending = new ArrayList<>();
    private final List<String> calls = new ArrayList<>();
    private int sequence = 0;

    FakeProcessor(Clock clock, long latencyMillis) { this.clock = clock; this.latencyMillis = latencyMillis; }

    /** Queue outcomes for a payment; each authorize/refund consumes the next one. Default: APPROVED / REFUND_OK. */
    FakeProcessor script(String paymentId, ProcessorOutcome... outcomes) {
        script.computeIfAbsent(paymentId, k -> new ArrayDeque<>()).addAll(List.of(outcomes));
        return this;
    }

    List<String> calls() { return Collections.unmodifiableList(calls); }

    @Override public String authorize(String paymentId, Money amount) {
        return schedule(paymentId, amount, "authorize", ProcessorOutcome.APPROVED);
    }

    @Override public String refund(String paymentId, Money amount) {
        return schedule(paymentId, amount, "refund", ProcessorOutcome.REFUND_OK);
    }

    private String schedule(String paymentId, Money amount, String op, ProcessorOutcome fallback) {
        Deque<ProcessorOutcome> q = script.get(paymentId);
        ProcessorOutcome outcome = (q == null || q.isEmpty()) ? fallback : q.poll();
        String reference = String.format("pr_%04d", ++sequence);
        calls.add(op + " " + paymentId + " " + amount + " -> " + reference);
        String detail = outcome == ProcessorOutcome.DECLINED ? "51 insufficient funds" : outcome.name().toLowerCase();
        pending.add(new ProcessorCallback(reference, paymentId, outcome, detail, clock.nowMillis() + latencyMillis));
        return reference;
    }

    @Override public List<ProcessorCallback> dueCallbacks() {
        long now = clock.nowMillis();
        List<ProcessorCallback> due = new ArrayList<>();
        for (ProcessorCallback cb : pending) if (cb.dueAtMillis() <= now) due.add(cb);
        pending.removeAll(due);
        return due;
    }
}

// ---------------------------------------------------------------------------------------------
// The service: the only code that talks to both the store and the processor. Load, decide, save.
// ---------------------------------------------------------------------------------------------

final class PaymentService {
    private final PaymentStore store;
    private final ProcessorAdapter processor;

    PaymentService(PaymentStore store, ProcessorAdapter processor) { this.store = store; this.processor = processor; }

    Payment create(String id, Money amount) { return store.create(id, amount); }

    Payment confirm(String id, String eventId) {
        Payment p = store.load(id);
        p.apply(PaymentEvent.of(eventId, EventKind.CONFIRM), "merchant confirmed");
        store.save(p);                                    // persist PROCESSING before calling out
        processor.authorize(id, p.amount());
        return p;
    }

    Payment completeAction(String id, String eventId) {
        Payment p = store.load(id);
        p.apply(PaymentEvent.of(eventId, EventKind.ACTION_COMPLETED), "customer completed challenge");
        store.save(p);
        processor.authorize(id, p.amount());              // re-present with the authentication result
        return p;
    }

    Payment cancel(String id, String eventId) {
        Payment p = store.load(id);
        p.apply(PaymentEvent.of(eventId, EventKind.CANCEL), "merchant canceled");
        store.save(p);
        return p;
    }

    Payment requestRefund(String id, String eventId, Money amount) {
        Payment p = store.load(id);
        p.apply(PaymentEvent.refundRequested(eventId, amount), "refund requested");
        store.save(p);
        processor.refund(id, amount);
        return p;
    }

    /** Drain due processor callbacks into events. The callback reference is the event id, so redelivery is a no-op. */
    int pump() {
        int applied = 0;
        for (ProcessorCallback cb : processor.dueCallbacks()) {
            if (deliver(cb) == ApplyOutcome.APPLIED) applied++;
        }
        return applied;
    }

    ApplyOutcome deliver(ProcessorCallback cb) {
        EventKind kind = switch (cb.outcome()) {
            case APPROVED -> EventKind.PROCESSOR_APPROVED;
            case DECLINED -> EventKind.PROCESSOR_DECLINED;
            case CHALLENGE -> EventKind.REQUIRE_ACTION;
            case REFUND_OK -> EventKind.REFUND_SUCCEEDED;
            case REFUND_FAILED -> EventKind.REFUND_FAILED;
        };
        Payment p = store.load(cb.paymentId());
        ApplyOutcome outcome = p.apply(PaymentEvent.of(cb.reference(), kind, cb.detail()), "processor: " + cb.detail());
        if (outcome == ApplyOutcome.APPLIED) store.save(p);
        return outcome;
    }
}

// ---------------------------------------------------------------------------------------------
// Demo
// ---------------------------------------------------------------------------------------------

public final class Main {
    public static void main(String[] args) {
        FakeClock clock = new FakeClock(1_700_000_000_000L);
        Currency USD = Currency.USD;
        PaymentStore store = new PaymentStore(clock);
        FakeProcessor processor = new FakeProcessor(clock, 800);
        PaymentService service = new PaymentService(store, processor);

        System.out.println("== The transition table ==");
        for (Rule r : TransitionTable.RULES) {
            System.out.printf("%-16s + %-19s [%-22s] -> %s%n", r.from(), r.kind(), r.guard(), r.to());
        }

        System.out.println();
        System.out.println("== A 3-D Secure payment: created -> processing -> requires_action -> processing -> succeeded ==");
        processor.script("pay_1001", ProcessorOutcome.CHALLENGE, ProcessorOutcome.APPROVED);
        service.create("pay_1001", Money.ofMinor(4999, USD));
        service.confirm("pay_1001", "evt_confirm_1");
        System.out.println("after confirm:   " + store.load("pay_1001").state() + " v" + store.currentVersion("pay_1001"));
        System.out.println("pump before latency elapsed -> " + service.pump() + " callbacks applied");
        clock.advance(800);
        System.out.println("pump at +800ms              -> " + service.pump() + " callbacks applied; state " + store.load("pay_1001").state());
        clock.advance(8_400);
        service.completeAction("pay_1001", "evt_action_1");
        clock.advance(800);
        service.pump();
        Payment p = store.load("pay_1001");
        System.out.println("after approval:  " + p.state() + " v" + p.version());

        System.out.println();
        System.out.println("== The processor redelivers the approval: recognised by event id, not applied twice ==");
        ProcessorCallback again = new ProcessorCallback("pr_0002", "pay_1001", ProcessorOutcome.APPROVED, "approved", clock.nowMillis());
        System.out.println("deliver(pr_0002 again) -> " + service.deliver(again) + ", version still " + store.currentVersion("pay_1001"));

        System.out.println();
        System.out.println("== Refunds: SUCCEEDED -> REFUNDING -> SUCCEEDED (partial) or REFUNDED (full) ==");
        clock.advance(86_400_000L);
        service.requestRefund("pay_1001", "evt_refund_1", Money.ofMinor(1500, USD));
        p = store.load("pay_1001");
        System.out.println("after request:   " + p.state() + ", refundable = " + p.refundable() + " (15.00 pending at the processor)");
        try {
            service.requestRefund("pay_1001", "evt_refund_2", Money.ofMinor(100, USD));
        } catch (IllegalTransitionException e) {
            System.out.println("refused: " + e.getMessage());
        }
        clock.advance(800);
        service.pump();
        p = store.load("pay_1001");
        System.out.println("after settle:    " + p.state() + ", refunded = " + p.refunded() + ", refundable = " + p.refundable());
        try {
            service.requestRefund("pay_1001", "evt_refund_3", Money.ofMinor(4000, USD));
        } catch (IllegalTransitionException e) {
            System.out.println("refused: " + e.getMessage());
        }
        service.requestRefund("pay_1001", "evt_refund_4", Money.ofMinor(3499, USD));
        clock.advance(800);
        service.pump();
        p = store.load("pay_1001");
        System.out.println("after full:      " + p.state() + " (terminal: " + p.state().isTerminal() + "), refunded = " + p.refunded());

        System.out.println();
        store.create("pay_2002", Money.ofMinor(2500, USD));
        System.out.println("== Optimistic concurrency: two handlers load pay_2002 at v" + store.currentVersion("pay_2002") + "; only one may write v" + (store.currentVersion("pay_2002") + 1) + " ==");
        Payment handlerA = store.load("pay_2002");
        Payment handlerB = store.load("pay_2002");
        handlerA.apply(PaymentEvent.of("evt_a", EventKind.CONFIRM), "handler A: confirm");
        handlerB.apply(PaymentEvent.of("evt_b", EventKind.CANCEL), "handler B: cancel");
        store.save(handlerA);
        System.out.println("handler A saved CONFIRM as v1");
        try {
            store.save(handlerB);
        } catch (ConcurrencyConflictException e) {
            System.out.println("handler B refused: " + e.getMessage());
        }
        System.out.println("stored state is " + store.load("pay_2002").state() + "; B reloads and finds CANCEL is now illegal: canApply="
                + store.load("pay_2002").canApply(PaymentEvent.of("evt_b2", EventKind.CANCEL)));

        System.out.println();
        System.out.println("== Illegal transitions are refused before any mutation ==");
        Payment fresh = store.create("pay_3003", Money.ofMinor(100, USD));
        for (EventKind k : List.of(EventKind.PROCESSOR_APPROVED, EventKind.REFUND_SUCCEEDED, EventKind.ACTION_COMPLETED)) {
            try {
                fresh.apply(PaymentEvent.of("evt_x_" + k, k), "spoofed");
            } catch (IllegalTransitionException e) {
                System.out.println("refused: " + e.getMessage());
            }
        }
        System.out.println("pay_3003 still " + fresh.state() + " v" + fresh.version() + ", uncommitted = " + fresh.uncommitted().size());

        System.out.println();
        System.out.println("== Replay: the history of pay_1001 rebuilds the same payment ==");
        for (Transition t : store.history("pay_1001")) System.out.println(t);
        Payment rebuilt = Payment.replay("pay_1001", Money.ofMinor(4999, USD), store.history("pay_1001"), clock);
        System.out.println("replayed -> " + rebuilt.state() + " v" + rebuilt.version() + ", refunded " + rebuilt.refunded());
        System.out.println("processor calls: " + processor.calls().size());
    }
}
