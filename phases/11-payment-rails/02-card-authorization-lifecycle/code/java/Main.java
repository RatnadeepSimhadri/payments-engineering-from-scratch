// Lesson: phases/11-payment-rails/02-card-authorization-lifecycle/docs/en.md
// Authorization, Capture, Void, Refund: The Card Lifecycle State Machine
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: ISO 8583:1987 message classes (01xx authorization, 02xx financial, 04xx reversal); Visa Core Rules and
// Mastercard Transaction Processing Rules on authorization validity, incremental and estimated authorizations.
// Java 21, standard library only.

import java.time.Duration;
import java.time.Instant;
import java.util.ArrayList;
import java.util.Collections;
import java.util.EnumMap;
import java.util.EnumSet;
import java.util.List;
import java.util.Map;
import java.util.Objects;
import java.util.Set;

/** ISO 4217 currency, copied from Phase 10 Lesson 01: code plus the number of decimals in the minor unit. */
record Currency(String code, int exponent) {
    static final Currency USD = new Currency("USD", 2);
    static final Currency EUR = new Currency("EUR", 2);
    static final Currency JPY = new Currency("JPY", 0);

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

enum Rounding { HALF_EVEN, HALF_UP, DOWN, UP }

final class CurrencyMismatchException extends IllegalArgumentException {
    CurrencyMismatchException(Currency a, Currency b) { super("cannot combine " + a.code() + " with " + b.code()); }
}

/** Exact amount in minor units of one currency (Phase 10 Lesson 01 design, trimmed to what this lesson needs). */
record Money(long minorUnits, Currency currency) implements Comparable<Money> {
    Money { Objects.requireNonNull(currency, "currency"); }

    static Money ofMinor(long minorUnits, Currency currency) { return new Money(minorUnits, currency); }

    static Money parse(String text, Currency currency) {
        String s = text.strip();
        boolean negative = s.startsWith("-");
        if (negative || s.startsWith("+")) s = s.substring(1);
        if (s.isEmpty()) throw new NumberFormatException("empty amount");
        String whole = s, frac = "";
        int dot = s.indexOf('.');
        if (dot >= 0) { whole = s.substring(0, dot); frac = s.substring(dot + 1); }
        if (whole.isEmpty()) whole = "0";
        if (!whole.chars().allMatch(Character::isDigit) || !frac.chars().allMatch(Character::isDigit)) {
            throw new NumberFormatException("not a decimal amount: " + text);
        }
        if (frac.length() > currency.exponent()) {
            throw new ArithmeticException(text + " has more decimals than " + currency.code() + " allows");
        }
        StringBuilder padded = new StringBuilder(frac);
        while (padded.length() < currency.exponent()) padded.append('0');
        long minor = Math.addExact(Math.multiplyExact(Long.parseLong(whole), currency.minorUnitsPerMajor()),
                padded.length() == 0 ? 0 : Long.parseLong(padded.toString()));
        return new Money(negative ? -minor : minor, currency);
    }

    private void requireSameCurrency(Money other) {
        if (!currency.equals(other.currency)) throw new CurrencyMismatchException(currency, other.currency);
    }

    Money plus(Money other) { requireSameCurrency(other); return new Money(Math.addExact(minorUnits, other.minorUnits), currency); }
    Money minus(Money other) { requireSameCurrency(other); return new Money(Math.subtractExact(minorUnits, other.minorUnits), currency); }
    boolean isZero() { return minorUnits == 0; }
    boolean isPositive() { return minorUnits > 0; }

    /** Multiply by an exact rational and round once; used for the over-capture ceiling (authorized * 11500 / 10000). */
    Money times(long numerator, long denominator, Rounding rounding) {
        if (denominator <= 0) throw new ArithmeticException("denominator must be positive");
        return new Money(divideRounded(Math.multiplyExact(minorUnits, numerator), denominator, rounding), currency);
    }

    static long divideRounded(long dividend, long divisor, Rounding rounding) {
        long quotient = dividend / divisor, remainder = dividend % divisor;
        if (remainder == 0) return quotient;
        int sign = (dividend < 0) ^ (divisor < 0) ? -1 : 1;
        long twice = Math.abs(remainder) * 2, absDivisor = Math.abs(divisor);
        return switch (rounding) {
            case DOWN -> quotient;
            case UP -> quotient + sign;
            case HALF_UP -> twice >= absDivisor ? quotient + sign : quotient;
            case HALF_EVEN -> twice > absDivisor ? quotient + sign
                    : twice < absDivisor ? quotient : (quotient % 2 == 0 ? quotient : quotient + sign);
        };
    }

    @Override public int compareTo(Money other) { requireSameCurrency(other); return Long.compare(minorUnits, other.minorUnits); }

    @Override public String toString() {
        long magnitude = Math.abs(minorUnits), scale = currency.minorUnitsPerMajor();
        String text = Long.toString(magnitude / scale);
        if (currency.exponent() > 0) {
            String frac = Long.toString(magnitude % scale);
            text = text + "." + "0".repeat(currency.exponent() - frac.length()) + frac;
        }
        return (minorUnits < 0 ? "-" : "") + text + " " + currency.code();
    }
}

/** Injected time source. Production uses the system clock; tests and the demo use FakeClock. */
interface Clock {
    Instant now();
}

final class FakeClock implements Clock {
    private Instant current;
    FakeClock(Instant start) { this.current = start; }
    @Override public Instant now() { return current; }
    void advance(Duration d) { current = current.plus(d); }
}

/** The seven states a card transaction can be in. Three are terminal: VOIDED, EXPIRED (never captured), REFUNDED. */
enum TransactionState {
    AUTHORIZED, PARTIALLY_CAPTURED, CAPTURED, VOIDED, EXPIRED, PARTIALLY_REFUNDED, REFUNDED;

    boolean isTerminal() { return this == VOIDED || this == EXPIRED || this == REFUNDED; }
}

/** Everything that can happen to a transaction. AUTHORIZE creates it; the other six are transitions on it. */
enum Action { AUTHORIZE, INCREMENT, CAPTURE, CLOSE_CAPTURE, VOID, EXPIRE, REFUND }

/** The reason a transition was attempted from a state that does not allow it. */
final class IllegalTransitionException extends IllegalStateException {
    final TransactionState from;
    final Action action;
    IllegalTransitionException(TransactionState from, Action action) {
        super(action + " is not allowed in state " + from);
        this.from = from;
        this.action = action;
    }
}

/** An amount rule was broken: capture above the ceiling, refund above captured, non-positive amount. */
final class LimitExceededException extends IllegalArgumentException {
    LimitExceededException(String message) { super(message); }
}

/**
 * Per-merchant settings: how long an authorization stays valid and how far a capture may exceed it.
 * The tolerance is in basis points (1500 = 15%). Network rulebooks grant tolerances to merchant categories where
 * the final amount is unknown at authorization time (gratuities, fuel); the exact figures and MCC lists are
 * scheme-specific and change, so they live in configuration, not code.
 */
record LifecyclePolicy(Duration authValidity, long overCaptureToleranceBps) {
    static final LifecyclePolicy STANDARD = new LifecyclePolicy(Duration.ofDays(7), 0);

    LifecyclePolicy {
        if (authValidity.isNegative() || authValidity.isZero()) throw new IllegalArgumentException("validity must be positive");
        if (overCaptureToleranceBps < 0 || overCaptureToleranceBps > 10_000) {
            throw new IllegalArgumentException("tolerance must be 0..10000 bps");
        }
    }

    /** Illustrative mapping: gratuity-heavy categories get a 15% tolerance; everyone else gets none. */
    static LifecyclePolicy forMcc(String mcc) {
        return switch (mcc) {
            case "5812", "5813", "5814", "4121", "7230" -> new LifecyclePolicy(Duration.ofDays(7), 1500);
            default -> STANDARD;
        };
    }

    /** The most a merchant may capture in total against an authorization of `authorized`, rounded toward the merchant's disadvantage. */
    Money captureCeiling(Money authorized) {
        return authorized.times(10_000 + overCaptureToleranceBps, 10_000, Rounding.DOWN);
    }
}

/** One persisted transition. The log of these is the transaction; the state is derived from it. */
record TransactionEvent(int sequence, Action action, Money amount, TransactionState from, TransactionState to,
                        Instant at, String reason) {
    TransactionEvent {
        if (reason == null || reason.isBlank()) throw new IllegalArgumentException("every transition needs a reason");
    }

    @Override public String toString() {
        return String.format("#%d %-13s %12s  %s -> %s  @%s  \"%s\"", sequence, action, amount,
                from == null ? "(new)" : from, to, at, reason);
    }
}

/**
 * The lifecycle of one card authorization. Every mutation goes through `transition`, which checks the legal-move
 * table, checks the amount rule, appends an event, and only then changes state. Amounts are Money, never floats.
 */
final class CardTransaction {
    /** The legal-move table: which actions each state accepts. Everything not listed throws. */
    static final Map<TransactionState, Set<Action>> LEGAL = new EnumMap<>(TransactionState.class);
    static {
        LEGAL.put(TransactionState.AUTHORIZED, EnumSet.of(Action.INCREMENT, Action.CAPTURE, Action.VOID, Action.EXPIRE));
        LEGAL.put(TransactionState.PARTIALLY_CAPTURED, EnumSet.of(Action.CAPTURE, Action.CLOSE_CAPTURE, Action.EXPIRE));
        LEGAL.put(TransactionState.CAPTURED, EnumSet.of(Action.REFUND));
        LEGAL.put(TransactionState.PARTIALLY_REFUNDED, EnumSet.of(Action.REFUND));
        LEGAL.put(TransactionState.VOIDED, EnumSet.noneOf(Action.class));
        LEGAL.put(TransactionState.EXPIRED, EnumSet.noneOf(Action.class));
        LEGAL.put(TransactionState.REFUNDED, EnumSet.noneOf(Action.class));
    }

    private final String id;
    private final Clock clock;
    private final LifecyclePolicy policy;
    private final List<TransactionEvent> events = new ArrayList<>();
    private TransactionState state;
    private Money authorized;
    private Money captured;
    private Money refunded;
    private Instant expiresAt;

    private CardTransaction(String id, Money amount, Clock clock, LifecyclePolicy policy, String reason) {
        if (!amount.isPositive()) throw new LimitExceededException("authorization amount must be positive: " + amount);
        this.id = id;
        this.clock = clock;
        this.policy = policy;
        this.authorized = amount;
        this.captured = Money.ofMinor(0, amount.currency());
        this.refunded = Money.ofMinor(0, amount.currency());
        this.state = TransactionState.AUTHORIZED;
        Instant now = clock.now();
        this.expiresAt = now.plus(policy.authValidity());
        events.add(new TransactionEvent(1, Action.AUTHORIZE, amount, null, TransactionState.AUTHORIZED, now, reason));
    }

    static CardTransaction authorize(String id, Money amount, Clock clock, LifecyclePolicy policy, String reason) {
        return new CardTransaction(id, amount, clock, policy, reason);
    }

    // ---- read side -------------------------------------------------------------------------------------------

    String id() { return id; }
    TransactionState state() { return state; }
    Money authorizedAmount() { return authorized; }
    Money capturedAmount() { return captured; }
    Money refundedAmount() { return refunded; }
    Instant expiresAt() { return expiresAt; }
    List<TransactionEvent> events() { return Collections.unmodifiableList(events); }

    /** What is still held on the cardholder's account: authorized minus captured while the hold is open, else zero. */
    Money remainingAuthorization() {
        if (state != TransactionState.AUTHORIZED && state != TransactionState.PARTIALLY_CAPTURED) {
            return Money.ofMinor(0, authorized.currency());
        }
        Money rest = authorized.minus(captured);
        return rest.isPositive() ? rest : Money.ofMinor(0, authorized.currency());
    }

    /** What the merchant will actually be paid: captured minus refunded. */
    Money netSettled() { return captured.minus(refunded); }

    // ---- write side ------------------------------------------------------------------------------------------

    /** Raise the authorized amount while the authorization is still open; resets the expiry window. */
    void incrementAuthorization(Money additional, String reason) {
        expireIfDue(reason);
        require(Action.INCREMENT);
        requirePositive(additional);
        authorized = authorized.plus(additional);
        expiresAt = clock.now().plus(policy.authValidity());
        transition(Action.INCREMENT, additional, TransactionState.AUTHORIZED, reason);
    }

    /** Capture part or all of the authorization. Cumulative captures may not exceed the policy's ceiling. */
    void capture(Money amount, String reason) {
        expireIfDue(reason);
        require(Action.CAPTURE);
        requirePositive(amount);
        Money ceiling = policy.captureCeiling(authorized);
        Money total = captured.plus(amount);
        if (total.compareTo(ceiling) > 0) {
            throw new LimitExceededException("capture of " + amount + " would bring total to " + total
                    + ", above ceiling " + ceiling + " (authorized " + authorized + ", tolerance "
                    + policy.overCaptureToleranceBps() + " bps)");
        }
        captured = total;
        TransactionState next = captured.compareTo(authorized) >= 0 ? TransactionState.CAPTURED : TransactionState.PARTIALLY_CAPTURED;
        transition(Action.CAPTURE, amount, next, reason);
    }

    /** Declare a partially captured transaction complete: the uncaptured remainder is released to the cardholder. */
    void closeCapture(String reason) {
        expireIfDue(reason);
        require(Action.CLOSE_CAPTURE);
        Money released = remainingAuthorization();
        transition(Action.CLOSE_CAPTURE, released, TransactionState.CAPTURED, reason);
    }

    /** Release the whole hold before anything is captured. Not available once money has moved. */
    void voidAuthorization(String reason) {
        expireIfDue(reason);
        require(Action.VOID);
        transition(Action.VOID, authorized, TransactionState.VOIDED, reason);
    }

    /** Return captured money to the cardholder. Cumulative refunds may not exceed what was captured. */
    void refund(Money amount, String reason) {
        require(Action.REFUND);
        requirePositive(amount);
        Money total = refunded.plus(amount);
        if (total.compareTo(captured) > 0) {
            throw new LimitExceededException("refund of " + amount + " would bring total refunded to " + total
                    + ", above captured " + captured);
        }
        refunded = total;
        TransactionState next = refunded.compareTo(captured) == 0 ? TransactionState.REFUNDED : TransactionState.PARTIALLY_REFUNDED;
        transition(Action.REFUND, amount, next, reason);
    }

    /**
     * The clock-driven transition. If the authorization window has passed and the state still allows EXPIRE,
     * record the expiry and return true. Called before every merchant-initiated action so that a late capture
     * fails with a precise reason instead of silently succeeding against a dead hold.
     * A never-captured authorization becomes EXPIRED (terminal). A partially captured one keeps its captured
     * funds: the uncaptured remainder lapses and the transaction becomes CAPTURED, exactly as closeCapture would
     * leave it, so the captured money stays refundable.
     */
    boolean expireIfDue(String attemptedReason) {
        if (!LEGAL.get(state).contains(Action.EXPIRE)) return false;
        if (clock.now().isBefore(expiresAt)) return false;
        TransactionState next = state == TransactionState.PARTIALLY_CAPTURED ? TransactionState.CAPTURED : TransactionState.EXPIRED;
        transition(Action.EXPIRE, remainingAuthorization(), next,
                "authorization validity of " + policy.authValidity().toDays() + " days elapsed (while attempting: " + attemptedReason + ")");
        return true;
    }

    // ---- the single choke point ------------------------------------------------------------------------------

    private void require(Action action) {
        if (!LEGAL.get(state).contains(action)) throw new IllegalTransitionException(state, action);
    }

    private void requirePositive(Money amount) {
        if (!amount.currency().equals(authorized.currency())) throw new CurrencyMismatchException(authorized.currency(), amount.currency());
        if (!amount.isPositive()) throw new LimitExceededException("amount must be positive: " + amount);
    }

    private void transition(Action action, Money amount, TransactionState to, String reason) {
        TransactionState from = state;
        events.add(new TransactionEvent(events.size() + 1, action, amount, from, to, clock.now(), reason));
        state = to;
    }

    void printEvents() {
        System.out.println("transaction " + id + " [" + state + "] authorized=" + authorized + " captured=" + captured
                + " refunded=" + refunded + " net=" + netSettled());
        for (TransactionEvent e : events) System.out.println("  " + e);
    }
}

public final class Main {
    public static void main(String[] args) {
        FakeClock clock = new FakeClock(Instant.parse("2026-03-01T09:00:00Z"));
        Currency USD = Currency.USD;

        System.out.println("== Hotel stay: estimated auth, incremental auth, partial capture, refund ==");
        CardTransaction hotel = CardTransaction.authorize("txn-hotel-001", Money.parse("120.00", USD), clock,
                LifecyclePolicy.STANDARD, "check-in: estimated one night plus incidentals");
        clock.advance(Duration.ofDays(1));
        hotel.incrementAuthorization(Money.parse("30.00", USD), "minibar and late checkout");
        clock.advance(Duration.ofHours(6));
        hotel.capture(Money.parse("100.00", USD), "folio: room night 100.00");
        hotel.closeCapture("checkout complete; release the unused 50.00 hold");
        clock.advance(Duration.ofDays(3));
        hotel.refund(Money.parse("20.00", USD), "goodwill: noisy room complaint");
        hotel.printEvents();
        System.out.println("  remaining authorization: " + hotel.remainingAuthorization());

        System.out.println("\n== Restaurant (MCC 5812): tip pushes the capture 12% above the authorization ==");
        CardTransaction dinner = CardTransaction.authorize("txn-dinner-002", Money.parse("80.00", USD), clock,
                LifecyclePolicy.forMcc("5812"), "pre-tip bill presented");
        System.out.println("  capture ceiling with 15% tolerance: " + LifecyclePolicy.forMcc("5812").captureCeiling(dinner.authorizedAmount()));
        dinner.capture(Money.parse("89.60", USD), "bill 80.00 plus 9.60 tip");
        dinner.printEvents();
        try {
            dinner.refund(Money.parse("100.00", USD), "customer asked for more than they paid");
        } catch (LimitExceededException e) {
            System.out.println("  rejected: " + e.getMessage());
        }

        System.out.println("\n== Same merchant without the tolerance flag ==");
        CardTransaction strict = CardTransaction.authorize("txn-strict-003", Money.parse("80.00", USD), clock,
                LifecyclePolicy.STANDARD, "pre-tip bill presented");
        try {
            strict.capture(Money.parse("89.60", USD), "bill 80.00 plus 9.60 tip");
        } catch (LimitExceededException e) {
            System.out.println("  rejected: " + e.getMessage());
        }

        System.out.println("\n== Forgotten authorization: capture attempted on day 8 ==");
        CardTransaction stale = CardTransaction.authorize("txn-stale-004", Money.parse("45.00", USD), clock,
                LifecyclePolicy.STANDARD, "online order placed");
        clock.advance(Duration.ofDays(8));
        try {
            stale.capture(Money.parse("45.00", USD), "order shipped");
        } catch (IllegalTransitionException e) {
            System.out.println("  rejected: " + e.getMessage());
        }
        stale.printEvents();

        System.out.println("\n== Illegal moves are exceptions, not silent no-ops ==");
        try {
            hotel.voidAuthorization("oops");
        } catch (IllegalTransitionException e) {
            System.out.println("  rejected: " + e.getMessage());
        }
        try {
            hotel.capture(Money.parse("1.00", USD), "one more");
        } catch (IllegalTransitionException e) {
            System.out.println("  rejected: " + e.getMessage());
        }
    }
}
