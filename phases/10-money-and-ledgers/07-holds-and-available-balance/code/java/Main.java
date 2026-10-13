// Lesson: phases/10-money-and-ledgers/07-holds-and-available-balance/docs/en.md
// Holds, Pending Transactions, and Available Balance
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: Visa Core Rules (authorization hold / incremental authorization / reversal); Regulation E 12 CFR 1005
// for consumer disclosure of pending transactions; Money type copied from Phase 10, Lesson 01. Java 21, stdlib only.

import java.util.ArrayList;
import java.util.Collections;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.Objects;

// ---------------------------------------------------------------------------------------------
// Money (copied from Phase 10, Lesson 01). Integer minor units + ISO 4217 currency. Never a float.
// ---------------------------------------------------------------------------------------------

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

final class CurrencyMismatchException extends IllegalArgumentException {
    CurrencyMismatchException(Currency a, Currency b) { super("cannot combine " + a.code() + " with " + b.code()); }
}

record Money(long minorUnits, Currency currency) implements Comparable<Money> {
    Money { Objects.requireNonNull(currency, "currency"); }

    static Money ofMinor(long minorUnits, Currency currency) { return new Money(minorUnits, currency); }

    static Money zero(Currency currency) { return new Money(0, currency); }

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
            throw new ArithmeticException(text + " has more decimals than " + currency.code() + " allows (" + currency.exponent() + ")");
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
    Money negate() { return new Money(Math.negateExact(minorUnits), currency); }
    boolean isZero() { return minorUnits == 0; }
    boolean isNegative() { return minorUnits < 0; }
    boolean isPositive() { return minorUnits > 0; }

    @Override public int compareTo(Money other) { requireSameCurrency(other); return Long.compare(minorUnits, other.minorUnits); }

    @Override public String toString() {
        long magnitude = Math.abs(minorUnits);
        long scale = currency.minorUnitsPerMajor();
        String result = Long.toString(magnitude / scale);
        if (currency.exponent() > 0) {
            String frac = Long.toString(magnitude % scale);
            StringBuilder padded = new StringBuilder();
            for (int i = frac.length(); i < currency.exponent(); i++) padded.append('0');
            result = result + "." + padded + frac;
        }
        return (minorUnits < 0 ? "-" : "") + result + " " + currency.code();
    }
}

// ---------------------------------------------------------------------------------------------
// Time is injected. Nothing in this lesson reads the wall clock.
// ---------------------------------------------------------------------------------------------

/** A source of the current time in whole seconds since the epoch. */
interface Clock {
    long now();
}

/** A clock the test moves by hand. Expiry is a function of this value and nothing else. */
final class ManualClock implements Clock {
    private volatile long now;   // read from many threads in the concurrency test; written by one

    ManualClock(long start) { this.now = start; }

    @Override public long now() { return now; }

    synchronized void advance(long seconds) {
        if (seconds < 0) throw new IllegalArgumentException("clocks do not run backwards in this lesson");
        now += seconds;
    }
}

// ---------------------------------------------------------------------------------------------
// Holds.
// ---------------------------------------------------------------------------------------------

/** The four terminal-or-not states of a hold. Only ACTIVE holds reduce the available balance. */
enum HoldStatus { ACTIVE, CAPTURED, VOIDED, EXPIRED }

/**
 * An authorization hold: a promise that `amount` is reserved until `expiresAt`. A hold is not a
 * ledger posting. It never changes the ledger balance; it only reduces what is available.
 */
final class Hold {
    final String id;
    final long createdAt;
    final long expiresAt;
    private Money amount;
    private HoldStatus status = HoldStatus.ACTIVE;
    private Money captured;

    Hold(String id, Money amount, long createdAt, long expiresAt) {
        if (id == null || id.isBlank()) throw new IllegalArgumentException("hold id required");
        if (!amount.isPositive()) throw new IllegalArgumentException("hold amount must be positive, got " + amount);
        if (expiresAt <= createdAt) throw new IllegalArgumentException("hold must expire after it is created");
        this.id = id;
        this.amount = amount;
        this.createdAt = createdAt;
        this.expiresAt = expiresAt;
        this.captured = Money.zero(amount.currency());
    }

    Money amount() { return amount; }
    HoldStatus status() { return status; }
    Money captured() { return captured; }
    boolean isActive() { return status == HoldStatus.ACTIVE; }

    /** True when the clock has passed the expiry and the hold is still marked active. */
    boolean isStale(long now) { return status == HoldStatus.ACTIVE && now >= expiresAt; }

    // Package-private transitions. Only Account calls these, after it has checked the invariant.
    void increment(Money extra) { amount = amount.plus(extra); }
    void markCaptured(Money capturedAmount) { captured = capturedAmount; status = HoldStatus.CAPTURED; }
    void markVoided() { status = HoldStatus.VOIDED; }
    void markExpired() { status = HoldStatus.EXPIRED; }

    @Override public String toString() {
        return id + " " + amount + " " + status + (status == HoldStatus.CAPTURED ? " captured=" + captured : "");
    }
}

class HoldException extends IllegalStateException {
    HoldException(String message) { super(message); }
}

final class InsufficientAvailableException extends HoldException {
    InsufficientAvailableException(Money requested, Money available) {
        super("requested " + requested + " but only " + available + " is available");
    }
}

final class OverCaptureException extends HoldException {
    OverCaptureException(String holdId, Money requested, Money held) {
        super("hold " + holdId + ": cannot capture " + requested + " against a hold of " + held);
    }
}

final class HoldNotActiveException extends HoldException {
    HoldNotActiveException(String holdId, HoldStatus status) {
        super("hold " + holdId + " is " + status + ", not ACTIVE");
    }
}

final class UnknownHoldException extends HoldException {
    UnknownHoldException(String holdId) { super("unknown hold: " + holdId); }
}

final class DuplicateHoldException extends HoldException {
    DuplicateHoldException(String holdId) { super("hold id already used: " + holdId); }
}

/**
 * One account with a settled (ledger) balance and a set of holds. The invariant the tests prove:
 *
 *     available == ledger - sum(amount of ACTIVE holds)      and      available >= 0
 *
 * Every public method expires stale holds against the injected clock before it reads or writes,
 * so the invariant is evaluated against the current time, never against a cached view.
 *
 * Every public method is synchronized on the account. The check "amount <= available" and the
 * insertion of the hold must be one atomic step; two threads that both read available = 100.00 and
 * both insert a 100.00 hold would leave available at -100.00. The lock is per account, so holds
 * against different accounts do not contend. Java's monitor is reentrant, which is why
 * authorize() can call availableBalance() can call heldTotal() can call expireStale() safely.
 */
final class Account {
    final String id;
    final Currency currency;
    private final Clock clock;
    private Money ledger;
    private final Map<String, Hold> holds = new LinkedHashMap<>();
    private final List<String> events = new ArrayList<>();

    Account(String id, Currency currency, Clock clock) {
        this.id = Objects.requireNonNull(id);
        this.currency = Objects.requireNonNull(currency);
        this.clock = Objects.requireNonNull(clock);
        this.ledger = Money.zero(currency);
    }

    /** Sweep: any ACTIVE hold whose expiry has passed becomes EXPIRED and stops reducing available. */
    synchronized int expireStale() {
        long now = clock.now();
        int released = 0;
        for (Hold h : holds.values()) {
            if (h.isStale(now)) {
                h.markExpired();
                released++;
                events.add("t=" + now + " EXPIRED " + h.id + " released " + h.amount());
            }
        }
        return released;
    }

    /** The settled balance: what the ledger says the account holds. Holds never change this number. */
    synchronized Money ledgerBalance() { return ledger; }

    /** Sum of ACTIVE holds after sweeping expiries. */
    synchronized Money heldTotal() {
        expireStale();
        Money total = Money.zero(currency);
        for (Hold h : holds.values()) if (h.isActive()) total = total.plus(h.amount());
        return total;
    }

    /** What may still be promised to someone else: ledger minus everything already promised. */
    synchronized Money availableBalance() { return ledger.minus(heldTotal()); }

    synchronized List<Hold> activeHolds() {
        expireStale();
        List<Hold> active = new ArrayList<>();
        for (Hold h : holds.values()) if (h.isActive()) active.add(h);
        return Collections.unmodifiableList(active);
    }

    synchronized Hold hold(String holdId) {
        Hold h = holds.get(holdId);
        if (h == null) throw new UnknownHoldException(holdId);
        return h;
    }

    synchronized List<String> events() { return Collections.unmodifiableList(events); }

    /** A settled credit (deposit, incoming transfer, refund). Raises both ledger and available. */
    synchronized void credit(Money amount) {
        requirePositive(amount);
        ledger = ledger.plus(amount);
        events.add("t=" + clock.now() + " CREDIT " + amount);
    }

    /** A settled debit that was never authorized (a fee, a direct debit). Must fit inside available. */
    synchronized void debit(Money amount) {
        requirePositive(amount);
        Money available = availableBalance();
        if (amount.compareTo(available) > 0) throw new InsufficientAvailableException(amount, available);
        ledger = ledger.minus(amount);
        events.add("t=" + clock.now() + " DEBIT " + amount);
    }

    /** Place a hold. Rejected if it would push available below zero. The ledger balance does not move. */
    synchronized Hold authorize(String holdId, Money amount, long ttlSeconds) {
        requirePositive(amount);
        if (holds.containsKey(holdId)) throw new DuplicateHoldException(holdId);
        Money available = availableBalance();
        if (amount.compareTo(available) > 0) throw new InsufficientAvailableException(amount, available);
        long now = clock.now();
        Hold h = new Hold(holdId, amount, now, now + ttlSeconds);
        holds.put(holdId, h);
        events.add("t=" + now + " AUTHORIZE " + holdId + " " + amount + " until t=" + h.expiresAt);
        return h;
    }

    /** Hold stacking by incremental authorization: the same hold grows, subject to the same check. */
    synchronized Hold increment(String holdId, Money extra) {
        requirePositive(extra);
        Hold h = requireActive(holdId);
        Money available = availableBalance();
        if (extra.compareTo(available) > 0) throw new InsufficientAvailableException(extra, available);
        h.increment(extra);
        events.add("t=" + clock.now() + " INCREMENT " + holdId + " +" + extra + " -> " + h.amount());
        return h;
    }

    /**
     * Capture: the merchant presents the final amount. The ledger moves by exactly `amount`; the whole
     * hold stops reducing available, so a partial capture releases the remainder in the same step.
     */
    synchronized Money capture(String holdId, Money amount) {
        requirePositive(amount);
        Hold h = requireActive(holdId);
        if (amount.compareTo(h.amount()) > 0) throw new OverCaptureException(holdId, amount, h.amount());
        ledger = ledger.minus(amount);
        h.markCaptured(amount);
        Money released = h.amount().minus(amount);
        events.add("t=" + clock.now() + " CAPTURE " + holdId + " " + amount + " released " + released);
        return released;
    }

    /** Void (authorization reversal): the hold is cancelled and its whole amount returns to available. */
    synchronized Money voidHold(String holdId) {
        Hold h = requireActive(holdId);
        h.markVoided();
        events.add("t=" + clock.now() + " VOID " + holdId + " released " + h.amount());
        return h.amount();
    }

    /** Recompute the invariant from scratch. Tests call this after every operation. */
    synchronized boolean checkInvariant() {
        expireStale();
        Money sum = Money.zero(currency);
        for (Hold h : holds.values()) if (h.status() == HoldStatus.ACTIVE) sum = sum.plus(h.amount());
        Money available = availableBalance();
        return available.equals(ledger.minus(sum)) && !available.isNegative();
    }

    private Hold requireActive(String holdId) {
        expireStale();
        Hold h = hold(holdId);
        if (!h.isActive()) throw new HoldNotActiveException(holdId, h.status());
        return h;
    }

    private void requirePositive(Money amount) {
        if (!amount.currency().equals(currency)) throw new CurrencyMismatchException(currency, amount.currency());
        if (!amount.isPositive()) throw new IllegalArgumentException("amount must be positive, got " + amount);
    }
}

public final class Main {
    static final long HOUR = 3600, DAY = 24 * HOUR;

    static void show(String label, Account a) {
        System.out.printf("%-44s ledger=%-11s held=%-11s available=%-11s invariant=%s%n",
                label, a.ledgerBalance(), a.heldTotal(), a.availableBalance(), a.checkInvariant());
    }

    /**
     * Start `threads` threads that each try to place one hold of `amount` at the same instant, and
     * return {accepted, rejected}. With 500.00 available and 50 x 20.00 requested, exactly 25 can
     * succeed; the lock in Account is what makes that "exactly", not "about".
     */
    static int[] raceAuthorizations(Account account, int threads, Money amount) {
        java.util.concurrent.CountDownLatch start = new java.util.concurrent.CountDownLatch(1);
        java.util.concurrent.atomic.AtomicInteger accepted = new java.util.concurrent.atomic.AtomicInteger();
        java.util.concurrent.atomic.AtomicInteger rejected = new java.util.concurrent.atomic.AtomicInteger();
        List<Thread> workers = new ArrayList<>();
        for (int i = 0; i < threads; i++) {
            final String holdId = "race-" + i;
            Thread t = new Thread(() -> {
                try {
                    start.await();
                    account.authorize(holdId, amount, 3600);
                    accepted.incrementAndGet();
                } catch (InsufficientAvailableException e) {
                    rejected.incrementAndGet();
                } catch (InterruptedException e) {
                    Thread.currentThread().interrupt();
                }
            });
            workers.add(t);
            t.start();
        }
        start.countDown();
        for (Thread t : workers) {
            try { t.join(); } catch (InterruptedException e) { Thread.currentThread().interrupt(); }
        }
        return new int[] {accepted.get(), rejected.get()};
    }

    public static void main(String[] args) {
        Currency USD = Currency.USD;
        ManualClock clock = new ManualClock(1_700_000_000L);        // fixed epoch; never the wall clock
        Account card = new Account("debit-4242", USD, clock);

        System.out.println("== A debit card account: payroll lands, then two authorizations ==");
        card.credit(Money.parse("500.00", USD));
        show("payroll credit 500.00", card);
        card.authorize("auth-hotel", Money.parse("300.00", USD), 7 * DAY);
        show("hotel authorizes 300.00 (7-day hold)", card);
        card.authorize("auth-fuel", Money.parse("125.00", USD), 2 * HOUR);
        show("fuel pump pre-authorizes 125.00 (2-hour hold)", card);

        System.out.println("\n== Available, not ledger, is what the next request sees ==");
        try {
            card.debit(Money.parse("80.00", USD));
        } catch (InsufficientAvailableException e) {
            System.out.println("rejected: " + e.getMessage() + "  (ledger still says 500.00)");
        }

        System.out.println("\n== Partial capture releases the remainder in the same step ==");
        Money released = card.capture("auth-fuel", Money.parse("42.17", USD));
        System.out.println("fuel captures 42.17, released " + released);
        show("after fuel capture", card);

        System.out.println("\n== Expiry: the clock moves 8 days; nobody captured the hotel hold ==");
        clock.advance(8 * DAY);
        show("8 days later (hotel hold expired on read)", card);
        try {
            card.capture("auth-hotel", Money.parse("300.00", USD));
        } catch (HoldNotActiveException e) {
            System.out.println("rejected: " + e.getMessage());
        }

        System.out.println("\n== Hold stacking: a new hotel authorization, then an incremental ==");
        card.authorize("auth-hotel-2", Money.parse("310.00", USD), 7 * DAY);
        card.increment("auth-hotel-2", Money.parse("45.00", USD));
        show("310.00 + 45.00 incremental", card);
        try {
            card.capture("auth-hotel-2", Money.parse("400.00", USD));
        } catch (OverCaptureException e) {
            System.out.println("rejected: " + e.getMessage());
        }
        card.capture("auth-hotel-2", Money.parse("355.00", USD));
        show("hotel captures the full 355.00", card);

        System.out.println("\n== Void: an authorization reversal returns the whole amount ==");
        card.authorize("auth-coffee", Money.parse("4.50", USD), DAY);
        show("coffee authorizes 4.50", card);
        card.voidHold("auth-coffee");
        show("coffee voided", card);

        System.out.println("\n== Concurrency: 50 threads race to authorize 20.00 against 500.00 ==");
        Account shared = new Account("shared", USD, clock);
        shared.credit(Money.parse("500.00", USD));
        int[] outcome = raceAuthorizations(shared, 50, Money.parse("20.00", USD));
        System.out.println("accepted=" + outcome[0] + " rejected=" + outcome[1]
                + " available=" + shared.availableBalance() + " invariant=" + shared.checkInvariant());

        System.out.println("\n== Event log ==");
        for (String e : card.events()) System.out.println(e);
        System.out.println("\nfinal: " + card.ledgerBalance() + " ledger, " + card.availableBalance()
                + " available, invariant=" + card.checkInvariant());
    }
}
