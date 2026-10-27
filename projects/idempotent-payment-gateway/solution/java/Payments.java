// Project: projects/idempotent-payment-gateway — Stage 02: payment state machine.
// A Payment moves through CREATED -> PROCESSING -> SUCCEEDED | FAILED (or CREATED -> CANCELED), and a SUCCEEDED
// payment can be refunded in parts until the refunded total equals the captured amount, when it becomes REFUNDED.
// Every move is checked against one transition table and appended to an immutable log.
// Java 21, standard library only. Same design as the Python port in solution/python; Go port follows later.

import java.util.ArrayList;
import java.util.Collections;
import java.util.EnumMap;
import java.util.EnumSet;
import java.util.List;
import java.util.Map;
import java.util.Objects;
import java.util.Set;

enum PaymentState {
    CREATED, PROCESSING, SUCCEEDED, FAILED, CANCELED, REFUNDED;

    /** Terminal states accept no further transition (a fully refunded payment is done). */
    boolean isTerminal() {
        return this == FAILED || this == CANCELED || this == REFUNDED;
    }

    /** Lower-case name used in API bodies and webhook payloads: "succeeded". */
    String wire() {
        return name().toLowerCase();
    }
}

final class IllegalTransitionException extends IllegalStateException {
    IllegalTransitionException(String paymentId, PaymentState from, PaymentState to) {
        super(paymentId + ": illegal transition " + from + " -> " + to);
    }
}

/** One entry of the transition log. Partial refunds are logged as SUCCEEDED -> SUCCEEDED with the amount in the reason. */
record Transition(PaymentState from, PaymentState to, String reason, long atMillis) {}

/** The single source of truth for which moves are legal. */
final class PaymentStateMachine {
    private static final Map<PaymentState, Set<PaymentState>> TABLE = new EnumMap<>(PaymentState.class);

    static {
        TABLE.put(PaymentState.CREATED, EnumSet.of(PaymentState.PROCESSING, PaymentState.CANCELED));
        TABLE.put(PaymentState.PROCESSING, EnumSet.of(PaymentState.SUCCEEDED, PaymentState.FAILED));
        TABLE.put(PaymentState.SUCCEEDED, EnumSet.of(PaymentState.REFUNDED));
        TABLE.put(PaymentState.FAILED, EnumSet.noneOf(PaymentState.class));
        TABLE.put(PaymentState.CANCELED, EnumSet.noneOf(PaymentState.class));
        TABLE.put(PaymentState.REFUNDED, EnumSet.noneOf(PaymentState.class));
    }

    private PaymentStateMachine() {}

    static boolean canTransition(PaymentState from, PaymentState to) {
        return TABLE.get(from).contains(to);
    }

    static Set<PaymentState> nextStates(PaymentState from) {
        return Collections.unmodifiableSet(TABLE.get(from));
    }
}

/** A single payment attempt and its money: what was captured and how much of it has been given back. */
final class Payment {
    private final String id;
    private final Money amount;
    private final Clock clock;
    private PaymentState state = PaymentState.CREATED;
    private Money captured;
    private Money refunded;
    private String processorReference = "";
    private long version = 1;   // bumps on every recorded transition: the optimistic-locking token a database row would carry
    private final List<Transition> history = new ArrayList<>();

    Payment(String id, Money amount, Clock clock) {
        if (id == null || id.isBlank()) throw new IllegalArgumentException("id is required");
        this.id = id;
        this.amount = Objects.requireNonNull(amount, "amount");
        this.clock = Objects.requireNonNull(clock, "clock");
        if (!amount.isPositive()) throw new IllegalArgumentException("amount must be positive: " + amount);
        this.captured = Money.ofMinor(0, amount.currency());
        this.refunded = Money.ofMinor(0, amount.currency());
    }

    String id() { return id; }
    Money amount() { return amount; }
    PaymentState state() { return state; }
    Money captured() { return captured; }
    Money refunded() { return refunded; }
    String processorReference() { return processorReference; }
    long version() { return version; }
    List<Transition> history() { return Collections.unmodifiableList(history); }

    /** What can still be refunded: captured minus refunded (zero unless SUCCEEDED). */
    Money refundable() {
        return captured.minus(refunded);
    }

    private void apply(PaymentState to, String reason) {
        if (!PaymentStateMachine.canTransition(state, to)) throw new IllegalTransitionException(id, state, to);
        record(new Transition(state, to, reason, clock.nowMillis()));
        state = to;
    }

    private void record(Transition transition) {
        history.add(transition);
        version++;
    }

    void startProcessing() {
        apply(PaymentState.PROCESSING, "sent to processor");
    }

    /** The processor approved: the full amount is captured. */
    void succeed(String reference) {
        apply(PaymentState.SUCCEEDED, "approved " + Objects.requireNonNull(reference, "reference"));
        this.processorReference = reference;
        this.captured = amount;
    }

    void fail(String reason) {
        apply(PaymentState.FAILED, Objects.requireNonNull(reason, "reason"));
    }

    void cancel(String reason) {
        apply(PaymentState.CANCELED, Objects.requireNonNull(reason, "reason"));
    }

    /**
     * Refund part or all of the captured amount. Only a SUCCEEDED payment can be refunded; the amount must be
     * positive, in the payment's currency, and no more than what is still refundable. Returns what remains.
     */
    Money refund(Money refundAmount, String reason) {
        Objects.requireNonNull(refundAmount, "refundAmount");
        Objects.requireNonNull(reason, "reason");
        if (state != PaymentState.SUCCEEDED) throw new IllegalTransitionException(id, state, PaymentState.REFUNDED);
        if (!refundAmount.isPositive()) throw new IllegalArgumentException("refund must be positive: " + refundAmount);
        Money remaining = refundable();
        if (refundAmount.compareTo(remaining) > 0) {
            throw new IllegalArgumentException("refund " + refundAmount + " exceeds refundable " + remaining);
        }
        refunded = refunded.plus(refundAmount);
        if (refunded.equals(captured)) {
            apply(PaymentState.REFUNDED, reason + " (" + refundAmount + ")");
        } else {
            record(new Transition(PaymentState.SUCCEEDED, PaymentState.SUCCEEDED, reason + " (" + refundAmount + ")", clock.nowMillis()));
        }
        return refundable();
    }

    @Override
    public String toString() {
        return id + "[" + state.wire() + " v" + version + " " + amount + " captured=" + captured + " refunded=" + refunded + "]";
    }
}
