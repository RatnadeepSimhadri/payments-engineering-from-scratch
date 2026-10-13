// Lesson: phases/08-messaging-and-streams/10-dead-letter-queues-and-poison-messages/docs/en.md
// Dead-Letter Queues and Poison Messages
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Java 21, standard library only. Retry classification follows the transient/permanent split used by
// AWS SQS redrive policies (maxReceiveCount) and Kafka Connect's errors.tolerance / errors.deadletterqueue.

import java.util.ArrayList;
import java.util.Collections;
import java.util.HashMap;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.Objects;
import java.util.Random;
import java.util.TreeMap;

/** ISO 4217 currency: three-letter code plus minor-unit exponent. Copied from Phase 10, Lesson 01. */
record Currency(String code, int exponent) {
    static final Currency USD = new Currency("USD", 2);
    static final Currency JPY = new Currency("JPY", 0);

    Currency {
        if (code == null || code.length() != 3 || !code.chars().allMatch(Character::isUpperCase)) {
            throw new IllegalArgumentException("currency code must be three uppercase letters: " + code);
        }
        if (exponent < 0 || exponent > 4) throw new IllegalArgumentException("exponent must be 0..4");
    }

    static Currency of(String code) {
        return switch (code) {
            case "USD" -> USD;
            case "JPY" -> JPY;
            default -> throw new IllegalArgumentException("unknown currency: " + code);
        };
    }

    long minorUnitsPerMajor() {
        long scale = 1;
        for (int i = 0; i < exponent; i++) scale *= 10;
        return scale;
    }
}

/** Integer minor units plus a currency. Never a float. parse() rejects excess precision instead of rounding. */
record Money(long minorUnits, Currency currency) {
    Money { Objects.requireNonNull(currency, "currency"); }

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

    Money plus(Money other) {
        if (!currency.equals(other.currency)) {
            throw new IllegalArgumentException("cannot combine " + currency.code() + " with " + other.currency.code());
        }
        return new Money(Math.addExact(minorUnits, other.minorUnits), currency);
    }

    @Override public String toString() {
        long scale = currency.minorUnitsPerMajor();
        long abs = Math.abs(minorUnits);
        String frac = currency.exponent() == 0 ? "" : "." + String.format("%0" + currency.exponent() + "d", abs % scale);
        return (minorUnits < 0 ? "-" : "") + (abs / scale) + frac + " " + currency.code();
    }
}

/**
 * A posting event as it sits on the partition: offset, the account it belongs to (the ordering key),
 * and the raw payload fields exactly as the producer serialized them. Parsing happens in the handler,
 * because a poison message is by definition one that cannot be parsed.
 */
record Event(long offset, String accountKey, String amountText, String currencyCode) {}

/** Whether a failure might succeed on retry (TRANSIENT) or never will without a code or data change (PERMANENT). */
enum FailureKind { TRANSIENT, PERMANENT }

/** The handler's one way to say no. Carries the classification the consumer's retry policy depends on. */
final class HandlerException extends RuntimeException {
    final FailureKind kind;

    HandlerException(FailureKind kind, String message) {
        super(message);
        this.kind = kind;
    }
}

/** Deterministic simulated clock; the consumer advances it by the backoff instead of sleeping. */
final class SimClock {
    private long now = 0;

    long now() { return now; }

    void advance(long millis) {
        if (millis < 0) throw new IllegalArgumentException("cannot advance backwards");
        now += millis;
    }
}

/** Seeded transient fault injector: the ledger database times out on a fixed fraction of calls. */
final class FaultInjector {
    private final Random random;
    private final double failureRate;
    private boolean enabled = true;

    FaultInjector(long seed, double failureRate) {
        this.random = new Random(seed);
        this.failureRate = failureRate;
    }

    boolean shouldFail() { return enabled && random.nextDouble() < failureRate; }

    void disable() { enabled = false; }
}

interface Handler {
    /** Process one event or throw HandlerException. Must be idempotent per offset, because retries re-invoke it. */
    void handle(Event event);
}

/**
 * Posts an amount to an in-memory per-account ledger. Parse failures are PERMANENT (the bytes will never
 * parse differently). Injected database timeouts are TRANSIENT. The handler is idempotent: an offset that
 * has already been applied is a no-op, so a retry after a late success cannot double-post.
 */
final class LedgerPostingHandler implements Handler {
    private final Map<String, Money> balances = new TreeMap<>();
    private final Map<Long, Boolean> applied = new HashMap<>();
    private final FaultInjector faults;

    LedgerPostingHandler(FaultInjector faults) { this.faults = faults; }

    @Override public void handle(Event e) {
        Currency currency;
        Money amount;
        try {
            currency = Currency.of(e.currencyCode());
            amount = Money.parse(e.amountText(), currency);
        } catch (IllegalArgumentException | ArithmeticException ex) {   // NumberFormatException is an IllegalArgumentException
            throw new HandlerException(FailureKind.PERMANENT, ex.getMessage());
        }
        if (faults.shouldFail()) throw new HandlerException(FailureKind.TRANSIENT, "ledger db timeout");
        if (applied.containsKey(e.offset())) return;                      // idempotent replay
        balances.merge(e.accountKey(), amount, Money::plus);
        applied.put(e.offset(), Boolean.TRUE);
    }

    Money balance(String accountKey) { return balances.get(accountKey); }

    Map<String, Money> balances() { return Collections.unmodifiableMap(new TreeMap<>(balances)); }   // sorted: deterministic output
}

/** Why an event was parked. */
enum ParkReason { PERMANENT_FAILURE, MAX_ATTEMPTS_EXCEEDED, KEY_QUARANTINED }

/** A parked event with everything an operator needs to decide what to do with it. */
record DeadLetter(Event event, ParkReason reason, int attempts, String lastError, long parkedAt) {}

/** Append-only parking lot with a per-key view, so replay can be done one account at a time, in offset order. */
final class DeadLetterQueue {
    private final List<DeadLetter> entries = new ArrayList<>();

    void park(DeadLetter letter) { entries.add(letter); }

    List<DeadLetter> all() { return List.copyOf(entries); }

    List<DeadLetter> forKey(String key) {
        List<DeadLetter> out = new ArrayList<>();
        for (DeadLetter d : entries) if (d.event().accountKey().equals(key)) out.add(d);
        return out;
    }

    /** Remove every entry for the key; called when a replay for that key begins. */
    void clear(String key) { entries.removeIf(d -> d.event().accountKey().equals(key)); }

    int size() { return entries.size(); }
}

/** Counters that make the behaviour measurable. */
record ConsumerStats(long processed, long parked, long quarantined, long retries, long committedOffset) {}

/**
 * A consumer that never stalls the partition. For each event in offset order:
 *   1. If the event's key is quarantined, park it as KEY_QUARANTINED without calling the handler, so per-key
 *      ordering is preserved (a later posting must not land before an earlier one that is still parked).
 *   2. Otherwise call the handler. PERMANENT failure: park at once. TRANSIENT failure: back off (simulated
 *      clock) and retry up to maxAttempts, then park as MAX_ATTEMPTS_EXCEEDED.
 *   3. Parking quarantines the key. Either way the offset is committed and the next event is processed.
 * Invariant: processed + parked == events consumed, and committedOffset == last offset + 1.
 */
final class ResilientConsumer {
    private final Handler handler;
    private final DeadLetterQueue dlq;
    private final SimClock clock;
    private final int maxAttempts;
    private final long baseBackoffMillis;
    private final Map<String, Long> quarantinedKeys = new LinkedHashMap<>();   // key -> offset that caused it
    private long processed = 0, parked = 0, quarantined = 0, retries = 0, committedOffset = 0;

    ResilientConsumer(Handler handler, DeadLetterQueue dlq, SimClock clock, int maxAttempts, long baseBackoffMillis) {
        if (maxAttempts < 1) throw new IllegalArgumentException("maxAttempts must be >= 1");
        this.handler = handler;
        this.dlq = dlq;
        this.clock = clock;
        this.maxAttempts = maxAttempts;
        this.baseBackoffMillis = baseBackoffMillis;
    }

    void consume(List<Event> partition) {
        for (Event e : partition) {
            if (quarantinedKeys.containsKey(e.accountKey())) {
                dlq.park(new DeadLetter(e, ParkReason.KEY_QUARANTINED, 0,
                        "earlier offset " + quarantinedKeys.get(e.accountKey()) + " for this key is parked", clock.now()));
                parked++;
                quarantined++;
            } else {
                attempt(e);
            }
            committedOffset = e.offset() + 1;
        }
    }

    /** Try the handler with retries; returns true on success, false if the event was parked. */
    private boolean attempt(Event e) {
        int attempts = 0;
        while (true) {
            attempts++;
            try {
                handler.handle(e);
                processed++;
                return true;
            } catch (HandlerException ex) {
                if (ex.kind == FailureKind.PERMANENT) {
                    park(e, ParkReason.PERMANENT_FAILURE, attempts, ex.getMessage());
                    return false;
                }
                if (attempts >= maxAttempts) {
                    park(e, ParkReason.MAX_ATTEMPTS_EXCEEDED, attempts, ex.getMessage());
                    return false;
                }
                retries++;
                clock.advance(baseBackoffMillis << (attempts - 1));   // exponential backoff: base, 2x, 4x, ...
            }
        }
    }

    private void park(Event e, ParkReason reason, int attempts, String error) {
        dlq.park(new DeadLetter(e, reason, attempts, error, clock.now()));
        parked++;
        quarantinedKeys.put(e.accountKey(), e.offset());
    }

    /**
     * Replay every parked event for one key, in offset order, after the operator has fixed the cause.
     * `corrections` maps an offset to a repaired event (for poison payloads); other events replay as-is.
     * Stops at the first event that fails again, leaving it and its followers parked. On full success the
     * key leaves quarantine.
     */
    int replay(String key, Map<Long, Event> corrections) {
        List<DeadLetter> letters = dlq.forKey(key);
        letters.sort((a, b) -> Long.compare(a.event().offset(), b.event().offset()));
        dlq.clear(key);
        quarantinedKeys.remove(key);
        int replayed = 0;
        for (int i = 0; i < letters.size(); i++) {
            Event e = corrections.getOrDefault(letters.get(i).event().offset(), letters.get(i).event());
            parked--;                                 // leaving the DLQ, one way or the other
            if (attempt(e)) {
                replayed++;
            } else {
                // Re-park the rest in order, preserving per-key ordering behind the failure.
                for (int j = i + 1; j < letters.size(); j++) {
                    Event rest = letters.get(j).event();
                    dlq.park(new DeadLetter(rest, ParkReason.KEY_QUARANTINED, 0,
                            "replay of offset " + e.offset() + " failed again", clock.now()));
                    quarantined++;
                }
                break;
            }
        }
        return replayed;
    }

    boolean isQuarantined(String key) { return quarantinedKeys.containsKey(key); }

    ConsumerStats stats() { return new ConsumerStats(processed, parked, quarantined, retries, committedOffset); }
}

/**
 * The alternative most systems ship by accident: on any failure, stop and retry the same offset forever.
 * Records how far the partition got before it stalled.
 */
final class StopTheWorldConsumer {
    private final Handler handler;
    private final int maxAttempts;
    private long processed = 0, committedOffset = 0;
    private Event stuckOn = null;

    StopTheWorldConsumer(Handler handler, int maxAttempts) {
        this.handler = handler;
        this.maxAttempts = maxAttempts;
    }

    void consume(List<Event> partition) {
        for (Event e : partition) {
            int attempts = 0;
            while (true) {
                attempts++;
                try {
                    handler.handle(e);
                    processed++;
                    committedOffset = e.offset() + 1;
                    break;
                } catch (HandlerException ex) {
                    if (ex.kind == FailureKind.PERMANENT || attempts >= maxAttempts) {
                        stuckOn = e;
                        return;                       // the partition is now stalled behind this offset
                    }
                }
            }
        }
    }

    long processed() { return processed; }

    long committedOffset() { return committedOffset; }

    Event stuckOn() { return stuckOn; }
}

public final class Main {
    static List<Event> partition() {
        return List.of(
                new Event(0, "acct-A", "100.00", "USD"),
                new Event(1, "acct-B", "25.50", "USD"),
                new Event(2, "acct-A", "12.345", "USD"),    // poison: three decimals in a two-decimal currency
                new Event(3, "acct-C", "1200", "JPY"),
                new Event(4, "acct-A", "-40.00", "USD"),     // fine on its own, but acct-A is quarantined behind offset 2
                new Event(5, "acct-B", "10.00", "USD"),
                new Event(6, "acct-D", "7.00", "US$"),       // poison: unknown currency code
                new Event(7, "acct-C", "300", "JPY"),
                new Event(8, "acct-B", "4.25", "USD"),
                new Event(9, "acct-A", "5.00", "USD")        // also quarantined behind offset 2
        );
    }

    public static void main(String[] args) {
        List<Event> events = partition();

        System.out.println("== Stop-the-world consumer: retry the same offset until it works ==");
        var naive = new StopTheWorldConsumer(new LedgerPostingHandler(new FaultInjector(7, 0.0)), 3);
        naive.consume(events);
        System.out.println("processed " + naive.processed() + " of " + events.size() + ", committed offset " + naive.committedOffset()
                + ", stuck on offset " + naive.stuckOn().offset() + " (" + naive.stuckOn().accountKey() + " " + naive.stuckOn().amountText() + ")");
        System.out.println("accounts B, C, and D never see another posting until someone wakes up.");
        System.out.println();

        System.out.println("== Resilient consumer: park the poison, quarantine its key, keep every other account moving ==");
        var clock = new SimClock();
        var faults = new FaultInjector(11, 0.25);    // one ledger call in four times out
        var handler = new LedgerPostingHandler(faults);
        var dlq = new DeadLetterQueue();
        var consumer = new ResilientConsumer(handler, dlq, clock, 3, 100);
        consumer.consume(events);
        ConsumerStats s = consumer.stats();
        System.out.println("processed=" + s.processed() + " parked=" + s.parked() + " (of which quarantined followers=" + s.quarantined()
                + ") transient retries=" + s.retries() + " committed offset=" + s.committedOffset() + " simulated clock=" + clock.now() + "ms");
        System.out.println("balances: " + handler.balances());
        System.out.println("dead letters:");
        for (DeadLetter d : dlq.all()) {
            System.out.printf("  offset %d %-6s %-8s %-4s reason=%-22s attempts=%d error=%s%n", d.event().offset(), d.event().accountKey(),
                    d.event().amountText(), d.event().currencyCode(), d.reason(), d.attempts(), d.lastError());
        }
        System.out.println();

        System.out.println("== Replay acct-A after correcting offset 2 (12.345 -> 12.34, approved by finance) ==");
        faults.disable();
        int replayedA = consumer.replay("acct-A", Map.of(2L, new Event(2, "acct-A", "12.34", "USD")));
        System.out.println("replayed " + replayedA + " events in offset order; acct-A quarantined=" + consumer.isQuarantined("acct-A"));
        System.out.println("acct-A balance = " + handler.balance("acct-A") + "  (100.00 + 12.34 - 40.00 + 5.00)");
        System.out.println();

        System.out.println("== Replay acct-B after the ledger database recovered: no correction needed ==");
        int replayedB = consumer.replay("acct-B", Map.of());
        System.out.println("replayed " + replayedB + " events; acct-B balance = " + handler.balance("acct-B") + "  (25.50 + 10.00 + 4.25)");
        System.out.println("dead letters remaining: " + dlq.size() + " (offset 6, acct-D, awaiting a currency-code fix)");
        System.out.println("stats: " + consumer.stats());
    }
}
