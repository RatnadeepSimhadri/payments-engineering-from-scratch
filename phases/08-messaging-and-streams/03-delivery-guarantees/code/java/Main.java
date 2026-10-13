// Lesson: phases/08-messaging-and-streams/03-delivery-guarantees/docs/en.md
// At-Most-Once, At-Least-Once, and Effectively-Once
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: KIP-98 (Exactly Once Delivery and Transactional Messaging in Kafka); Kafka docs section 4.6 "Message Delivery
//          Semantics"; Helland, "Idempotence Is Not a Medical Condition" (ACM Queue, 2012). Money follows Phase 10, Lesson 01.
// Java 21, standard library only. Crashes are injected by a FaultInjector at a chosen offset; nothing is random.

import java.util.ArrayList;
import java.util.Collections;
import java.util.HashSet;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.Objects;
import java.util.Set;

// ---------------------------------------------------------------------------------------------------------------------
// Money, copied from Phase 10, Lesson 01: integer minor units plus a currency. Never a float.
// ---------------------------------------------------------------------------------------------------------------------

record Currency(String code, int exponent) {
    static final Currency USD = new Currency("USD", 2);

    Currency {
        if (code == null || code.length() != 3) throw new IllegalArgumentException("currency code must be three letters");
        if (exponent < 0 || exponent > 4) throw new IllegalArgumentException("exponent must be 0..4");
    }

    long minorUnitsPerMajor() {
        long scale = 1;
        for (int i = 0; i < exponent; i++) scale *= 10;
        return scale;
    }
}

final class CurrencyMismatchException extends RuntimeException {
    CurrencyMismatchException(Currency a, Currency b) { super("cannot combine " + a.code() + " with " + b.code()); }
}

record Money(long minorUnits, Currency currency) {
    Money { Objects.requireNonNull(currency, "currency"); }

    static Money ofMinor(long minorUnits, Currency currency) { return new Money(minorUnits, currency); }

    static Money zero(Currency currency) { return new Money(0, currency); }

    static Money parse(String text, Currency currency) {
        String s = text.strip();
        boolean negative = s.startsWith("-");
        if (negative || s.startsWith("+")) s = s.substring(1);
        String whole = s, frac = "";
        int dot = s.indexOf('.');
        if (dot >= 0) { whole = s.substring(0, dot); frac = s.substring(dot + 1); }
        if (whole.isEmpty()) whole = "0";
        if (frac.length() > currency.exponent()) throw new ArithmeticException(text + " has too many decimals for " + currency.code());
        StringBuilder padded = new StringBuilder(frac);
        while (padded.length() < currency.exponent()) padded.append('0');
        long minor = Math.addExact(Math.multiplyExact(Long.parseLong(whole), currency.minorUnitsPerMajor()),
                padded.length() == 0 ? 0 : Long.parseLong(padded.toString()));
        return new Money(negative ? -minor : minor, currency);
    }

    Money plus(Money other) {
        if (!currency.equals(other.currency)) throw new CurrencyMismatchException(currency, other.currency);
        return new Money(Math.addExact(minorUnits, other.minorUnits), currency);
    }

    @Override
    public String toString() {
        long scale = currency.minorUnitsPerMajor();
        long abs = Math.abs(minorUnits);
        String sign = minorUnits < 0 ? "-" : "";
        if (currency.exponent() == 0) return sign + abs + " " + currency.code();
        String frac = String.valueOf(abs % scale);
        while (frac.length() < currency.exponent()) frac = "0" + frac;
        return sign + (abs / scale) + "." + frac + " " + currency.code();
    }
}

// ---------------------------------------------------------------------------------------------------------------------
// The log and the records on it. One partition is enough; the delivery question is per partition anyway.
// ---------------------------------------------------------------------------------------------------------------------

/** One captured payment on the log. paymentId is the business key that makes idempotent processing possible. */
record Record(long offset, String paymentId, Money amount) {}

/** A single-partition append-only log, the shape of Phase 8, Lesson 01 reduced to what this lesson needs. */
final class Log {
    private final List<Record> records = new ArrayList<>();

    long append(String paymentId, Money amount) {
        long offset = records.size();
        records.add(new Record(offset, paymentId, amount));
        return offset;
    }

    long endOffset() { return records.size(); }

    /** Up to maxRecords from fromOffset; empty at the end offset. */
    List<Record> read(long fromOffset, int maxRecords) {
        if (fromOffset < 0 || fromOffset > records.size()) throw new IllegalArgumentException("offset " + fromOffset + " out of range");
        int to = (int) Math.min(records.size(), fromOffset + maxRecords);
        return new ArrayList<>(records.subList((int) fromOffset, to));
    }
}

// ---------------------------------------------------------------------------------------------------------------------
// Faults. The process dies at a chosen point; the test chooses the point.
// ---------------------------------------------------------------------------------------------------------------------

/** Thrown when the simulated process dies. Everything not yet durably committed is gone. */
final class CrashException extends RuntimeException {
    CrashException(long offset) { super("process crashed after the side effect for offset " + offset + ", before the commit"); }
}

/** Fires exactly once: after the side effect for crashAtOffset has been performed and before the offset commit. */
final class FaultInjector {
    private final long crashAtOffset;
    private boolean armed = true;

    FaultInjector(long crashAtOffset) { this.crashAtOffset = crashAtOffset; }

    static FaultInjector none() { return new FaultInjector(-1); }

    void afterSideEffect(long offset) {
        if (armed && offset == crashAtOffset) {
            armed = false;
            throw new CrashException(offset);
        }
    }

    boolean fired() { return !armed; }
}

// ---------------------------------------------------------------------------------------------------------------------
// The durable store: ledger postings, the processed-id set, and the committed offset, with optional atomicity.
// ---------------------------------------------------------------------------------------------------------------------

record Posting(String paymentId, Money amount) {}

/**
 * Everything the consumer persists lives here, so a transaction can cover any combination of it. Writes made directly
 * are durable immediately (one write, one fsync, one failure point each). Writes made through a Transaction become
 * durable together or not at all.
 */
final class Store {
    private final List<Posting> postings = new ArrayList<>();
    private final Set<String> processed = new HashSet<>();
    private long committedOffset = 0;

    // --- direct, non-atomic writes -----------------------------------------------------------------------------------

    void post(Posting posting) { postings.add(posting); }

    /** Insert-if-absent on the business key, in the same durable store as the postings. Returns false on a duplicate. */
    boolean postIfNew(Posting posting) {
        if (!processed.add(posting.paymentId())) return false;
        postings.add(posting);
        return true;
    }

    void commitOffset(long nextOffset) {
        if (nextOffset < 0) throw new IllegalArgumentException("offset must be non-negative");
        committedOffset = nextOffset;
    }

    // --- atomic writes -------------------------------------------------------------------------------------------------

    Transaction begin() { return new Transaction(this); }

    /** Buffers writes and applies them all in commit(). A crash before commit() leaves the store untouched. */
    static final class Transaction {
        private final Store store;
        private final List<Posting> bufferedPostings = new ArrayList<>();
        private long bufferedOffset = -1;
        private boolean done = false;

        private Transaction(Store store) { this.store = store; }

        void post(Posting posting) { requireOpen(); bufferedPostings.add(posting); }

        void commitOffset(long nextOffset) { requireOpen(); bufferedOffset = nextOffset; }

        void commit() {
            requireOpen();
            store.postings.addAll(bufferedPostings);
            for (Posting p : bufferedPostings) store.processed.add(p.paymentId());
            if (bufferedOffset >= 0) store.committedOffset = bufferedOffset;
            done = true;
        }

        private void requireOpen() { if (done) throw new IllegalStateException("transaction already committed"); }
    }

    // --- reads ---------------------------------------------------------------------------------------------------------

    long committedOffset() { return committedOffset; }

    List<Posting> postings() { return Collections.unmodifiableList(postings); }

    Money balance(Currency currency) {
        Money total = Money.zero(currency);
        for (Posting p : postings) total = total.plus(p.amount());
        return total;
    }
}

// ---------------------------------------------------------------------------------------------------------------------
// The four ways to order "process" and "commit".
// ---------------------------------------------------------------------------------------------------------------------

enum Strategy {
    /** Commit first, then process. A crash loses whatever was committed but not yet processed. */
    AT_MOST_ONCE,
    /** Process first, then commit. A crash re-delivers whatever was processed but not yet committed. */
    AT_LEAST_ONCE,
    /** At-least-once offsets, but the side effect is an insert-if-absent on the business key. */
    AT_LEAST_ONCE_IDEMPOTENT,
    /** Side effect and offset commit in one atomic transaction per record. */
    TRANSACTIONAL
}

/** One consumer process. It starts from the store's committed offset and runs until the log is exhausted or it crashes. */
final class Consumer {
    private final Strategy strategy;
    private final Log log;
    private final Store store;
    private final FaultInjector faults;
    private final int batchSize;

    Consumer(Strategy strategy, Log log, Store store, FaultInjector faults, int batchSize) {
        if (batchSize <= 0) throw new IllegalArgumentException("batchSize must be positive");
        this.strategy = strategy;
        this.log = log;
        this.store = store;
        this.faults = faults;
        this.batchSize = batchSize;
    }

    void runToEnd() {
        long position = store.committedOffset();
        while (true) {
            List<Record> batch = log.read(position, batchSize);
            if (batch.isEmpty()) return;
            long batchEnd = batch.get(batch.size() - 1).offset() + 1;
            switch (strategy) {
                case AT_MOST_ONCE -> {
                    store.commitOffset(batchEnd);
                    for (Record r : batch) {
                        store.post(new Posting(r.paymentId(), r.amount()));
                        faults.afterSideEffect(r.offset());
                    }
                }
                case AT_LEAST_ONCE -> {
                    for (Record r : batch) {
                        store.post(new Posting(r.paymentId(), r.amount()));
                        faults.afterSideEffect(r.offset());
                    }
                    store.commitOffset(batchEnd);
                }
                case AT_LEAST_ONCE_IDEMPOTENT -> {
                    for (Record r : batch) {
                        store.postIfNew(new Posting(r.paymentId(), r.amount()));
                        faults.afterSideEffect(r.offset());
                    }
                    store.commitOffset(batchEnd);
                }
                case TRANSACTIONAL -> {
                    for (Record r : batch) {
                        Store.Transaction tx = store.begin();
                        tx.post(new Posting(r.paymentId(), r.amount()));
                        tx.commitOffset(r.offset() + 1);
                        faults.afterSideEffect(r.offset());
                        tx.commit();
                    }
                }
            }
            position = batchEnd;
        }
    }
}

// ---------------------------------------------------------------------------------------------------------------------
// The experiment: run a strategy, crash it once, restart it, and count what the ledger ended up with.
// ---------------------------------------------------------------------------------------------------------------------

record Outcome(Strategy strategy, int posted, int duplicates, int lost, int crashes, Money balance) {
    boolean effectivelyOnce() { return duplicates == 0 && lost == 0; }
}

final class Simulation {
    static Outcome run(Strategy strategy, Log log, long crashAtOffset, int batchSize) {
        Store store = new Store();
        FaultInjector faults = new FaultInjector(crashAtOffset);
        int crashes = 0;
        while (true) {
            try {
                new Consumer(strategy, log, store, faults, batchSize).runToEnd();
                break;
            } catch (CrashException e) {
                crashes++;   // the process is gone; a fresh one starts from store.committedOffset()
            }
        }
        return measure(strategy, log, store, crashes);
    }

    static Outcome measure(Strategy strategy, Log log, Store store, int crashes) {
        Map<String, Integer> counts = new LinkedHashMap<>();
        for (Record r : log.read(0, (int) log.endOffset())) counts.put(r.paymentId(), 0);
        for (Posting p : store.postings()) counts.merge(p.paymentId(), 1, Integer::sum);
        int duplicates = 0, lost = 0;
        for (int c : counts.values()) {
            if (c == 0) lost++;
            if (c > 1) duplicates += c - 1;
        }
        return new Outcome(strategy, store.postings().size(), duplicates, lost, crashes, store.balance(Currency.USD));
    }
}

public final class Main {
    static Log sampleLog(int count) {
        Log log = new Log();
        for (int i = 0; i < count; i++) {
            log.append("pay-" + (1000 + i), Money.parse(String.format("%d.%02d", 10 + i, (i * 37) % 100), Currency.USD));
        }
        return log;
    }

    public static void main(String[] args) {
        Log log = sampleLog(10);
        Money expected = Money.zero(Currency.USD);
        for (Record r : log.read(0, 10)) expected = expected.plus(r.amount());
        System.out.println("== 10 captured payments on the log, batch size 4, expected ledger total " + expected + " ==");
        for (Record r : log.read(0, 10)) System.out.printf("  offset %d  %s  %s%n", r.offset(), r.paymentId(), r.amount());

        System.out.println("\n== Crash after the side effect for offset 5, before its commit; then restart ==");
        System.out.printf("  %-26s %-7s %-11s %-5s %-8s %s%n", "strategy", "posted", "duplicates", "lost", "crashes", "ledger total");
        for (Strategy s : Strategy.values()) {
            Outcome o = Simulation.run(s, log, 5, 4);
            System.out.printf("  %-26s %-7d %-11d %-5d %-8d %s%s%n", s, o.posted(), o.duplicates(), o.lost(), o.crashes(), o.balance(),
                    o.effectivelyOnce() ? "  <- matches" : "  <- " + (o.lost() > 0 ? "money missing" : "money counted twice"));
        }

        System.out.println("\n== Why: the order of 'process' and 'commit' around the crash ==");
        System.out.println("  AT_MOST_ONCE    commit(8) -> post 4 -> post 5 -> CRASH -> restart at 8          : offsets 6,7 never posted");
        System.out.println("  AT_LEAST_ONCE   post 4 -> post 5 -> CRASH -> restart at 4 -> post 4,5 again      : offsets 4,5 posted twice");
        System.out.println("  IDEMPOTENT      same as above, but postIfNew(pay-1004) and (pay-1005) return false : duplicates absorbed");
        System.out.println("  TRANSACTIONAL   tx{post 5, commit 6} -> CRASH before tx.commit -> restart at 5     : nothing to absorb");

        System.out.println("\n== No crash at all: every strategy agrees ==");
        for (Strategy s : Strategy.values()) {
            Outcome o = Simulation.run(s, log, -1, 4);
            System.out.printf("  %-26s posted=%d duplicates=%d lost=%d%n", s, o.posted(), o.duplicates(), o.lost());
        }

        System.out.println("\n== Crash at every offset 0..9: how many runs end with the right ledger? ==");
        for (Strategy s : Strategy.values()) {
            int correct = 0;
            for (long crashAt = 0; crashAt < 10; crashAt++) if (Simulation.run(s, log, crashAt, 4).effectivelyOnce()) correct++;
            System.out.printf("  %-26s %d of 10%n", s, correct);
        }
    }
}
