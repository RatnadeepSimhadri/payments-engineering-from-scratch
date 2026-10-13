// Lesson: phases/14-payments-at-scale/04-reconciliation-at-scale/docs/en.md
// Reconciliation at Scale: Streams, Buckets, and Breaks
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: ISO 20022 camt.053 (statement structure); Merkle, "A Digital Signature Based on a
// Conventional Encryption Function" (hash-based set comparison); Phase 10 Lesson 08 (Reconciliation).
// Java 21, standard library only. Money is integer minor units + currency (Phase 10, Lesson 01).

import java.time.LocalDate;
import java.util.ArrayList;
import java.util.Comparator;
import java.util.EnumMap;
import java.util.HashMap;
import java.util.Iterator;
import java.util.List;
import java.util.Map;
import java.util.Objects;
import java.util.Random;
import java.util.TreeMap;

/** ISO 4217 currency: three-letter code and minor-unit exponent. Copied from Phase 10, Lesson 01. */
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

/** Exact amount of one currency as a signed count of minor units. Copied from Phase 10, Lesson 01. */
record Money(long minorUnits, Currency currency) implements Comparable<Money> {
    Money { Objects.requireNonNull(currency, "currency"); }

    static Money ofMinor(long minorUnits, Currency currency) { return new Money(minorUnits, currency); }

    static Money zero(Currency currency) { return new Money(0, currency); }

    private void requireSameCurrency(Money other) {
        if (!currency.equals(other.currency)) throw new CurrencyMismatchException(currency, other.currency);
    }

    Money plus(Money other) {
        requireSameCurrency(other);
        return new Money(Math.addExact(minorUnits, other.minorUnits), currency);
    }

    Money minus(Money other) {
        requireSameCurrency(other);
        return new Money(Math.subtractExact(minorUnits, other.minorUnits), currency);
    }

    boolean isZero() { return minorUnits == 0; }

    @Override
    public int compareTo(Money other) {
        requireSameCurrency(other);
        return Long.compare(minorUnits, other.minorUnits);
    }

    @Override
    public String toString() {
        long magnitude = Math.abs(minorUnits);
        long scale = currency.minorUnitsPerMajor();
        String whole = Long.toString(magnitude / scale);
        String result = whole;
        if (currency.exponent() > 0) {
            String frac = Long.toString(magnitude % scale);
            StringBuilder padded = new StringBuilder();
            for (int i = frac.length(); i < currency.exponent(); i++) padded.append('0');
            result = whole + "." + padded + frac;
        }
        return (minorUnits < 0 ? "-" : "") + result + " " + currency.code();
    }
}

/** Which feed a record came from. INTERNAL is our ledger; PROCESSOR is the acquirer's settlement file. */
enum Side { INTERNAL, PROCESSOR }

/**
 * One line of a feed. {@code ref} is the shared identifier both sides carry (the processor's
 * transaction reference, which the gateway stored at capture time). {@code txDate} is the date of
 * the underlying transaction, which both sides know and which is therefore safe to join on.
 * {@code valueDate} is the date this side booked the record: capture date for us, settlement date
 * for the processor. The two value dates legitimately differ by a day or two.
 */
record Rec(String ref, LocalDate txDate, LocalDate valueDate, Money amount, Side side) {
    Rec {
        Objects.requireNonNull(ref, "ref");
        Objects.requireNonNull(txDate, "txDate");
        Objects.requireNonNull(valueDate, "valueDate");
        Objects.requireNonNull(amount, "amount");
    }

    BucketKey bucket() { return new BucketKey(txDate.toEpochDay(), amount.currency().code(), amount.minorUnits()); }

    /** Days between the transaction and this side booking it; a processor lag beyond tolerance is a TIMING break. */
    long lagDays() { return valueDate.toEpochDay() - txDate.toEpochDay(); }
}

/**
 * The join key of the streaming merge: (txDate, currency, amount). Both feeds must be sorted by this
 * key; the reconciler verifies the ordering and refuses to proceed on a violation, because a merge
 * join over an unsorted feed silently reports every record as a break.
 */
record BucketKey(long epochDay, String currency, long amountMinor) implements Comparable<BucketKey> {
    static final Comparator<BucketKey> ORDER = Comparator.comparingLong(BucketKey::epochDay)
            .thenComparing(BucketKey::currency)
            .thenComparingLong(BucketKey::amountMinor);

    @Override
    public int compareTo(BucketKey other) { return ORDER.compare(this, other); }

    @Override
    public String toString() {
        return LocalDate.ofEpochDay(epochDay) + "/" + currency + "/" + amountMinor;
    }
}

/**
 * Order-independent digest of a set of references. Each reference is mixed through a 64-bit
 * finaliser and the results are summed; equal sets give equal digests regardless of order, and
 * the count travels alongside so that a digest collision also has to match the count.
 */
record BucketDigest(long hash, int count, long totalMinor) {
    static final BucketDigest EMPTY = new BucketDigest(0, 0, 0);

    static long mix(String ref) {
        long h = 0x9E3779B97F4A7C15L;
        for (int i = 0; i < ref.length(); i++) h = (h ^ ref.charAt(i)) * 0xBF58476D1CE4E5B9L;
        h ^= h >>> 31;
        h *= 0x94D049BB133111EBL;
        h ^= h >>> 29;
        return h;
    }

    BucketDigest add(Rec r) {
        return new BucketDigest(hash + mix(r.ref()), count + 1, Math.addExact(totalMinor, r.amount().minorUnits()));
    }
}

/** How a break is explained. The order matters: the classifier tries them from most to least specific. */
enum BreakType {
    DUPLICATE,              // the same ref appears twice on one side
    AMOUNT_MISMATCH,        // same ref on both sides, different amount
    TIMING,                 // same ref and amount, dates differ within the tolerance window
    MISSING_AT_PROCESSOR,   // we have it, they do not (unsettled capture, lost file line)
    MISSING_INTERNAL        // they have it, we do not (unknown settlement, lost ledger write)
}

/** Age bands for open breaks: how long since the record's value date, as of the run date. */
enum AgeBand {
    D0_1, D2_7, D8_30, D30_PLUS;

    static AgeBand of(long days) {
        if (days <= 1) return D0_1;
        if (days <= 7) return D2_7;
        if (days <= 30) return D8_30;
        return D30_PLUS;
    }
}

/** One unexplained difference. {@code internal} or {@code processor} is null when that side is absent. */
record Break(BreakType type, String ref, Rec internal, Rec processor, long ageDays) {
    /** The amount a side contributed to this break; used by the tie-out, so each side counts its own. */
    Money internalAmount(Currency c) { return internal == null ? Money.zero(c) : internal.amount(); }
    Money processorAmount(Currency c) { return processor == null ? Money.zero(c) : processor.amount(); }
    AgeBand band() { return AgeBand.of(ageDays); }

    @Override
    public String toString() {
        return type + " ref=" + ref
                + (internal == null ? "" : " internal=" + internal.amount() + "@" + internal.valueDate())
                + (processor == null ? "" : " processor=" + processor.amount() + "@" + processor.valueDate())
                + " age=" + ageDays + "d";
    }
}

/** The result of one reconciliation run. Totals are kept per side so the tie-out can be checked. */
final class ReconciliationReport {
    final Currency currency;
    final LocalDate asOf;
    long bucketsSeen, bucketsFastMatched, bucketsInspected;
    long matchedCount;
    Money matchedTotal;
    long internalCount, processorCount;
    Money internalTotal, processorTotal;
    final List<Break> breaks = new ArrayList<>();

    ReconciliationReport(Currency currency, LocalDate asOf) {
        this.currency = currency;
        this.asOf = asOf;
        this.matchedTotal = Money.zero(currency);
        this.internalTotal = Money.zero(currency);
        this.processorTotal = Money.zero(currency);
    }

    Map<BreakType, Long> countByType() {
        Map<BreakType, Long> m = new EnumMap<>(BreakType.class);
        for (Break b : breaks) m.merge(b.type(), 1L, Long::sum);
        return m;
    }

    Map<AgeBand, Long> countByAge() {
        Map<AgeBand, Long> m = new EnumMap<>(AgeBand.class);
        for (Break b : breaks) m.merge(b.band(), 1L, Long::sum);
        return m;
    }

    /** Sum of internal-side amounts sitting in breaks (the "unexplained" column of our ledger). */
    Money internalInBreaks() {
        Money m = Money.zero(currency);
        for (Break b : breaks) m = m.plus(b.internalAmount(currency));
        return m;
    }

    Money processorInBreaks() {
        Money m = Money.zero(currency);
        for (Break b : breaks) m = m.plus(b.processorAmount(currency));
        return m;
    }

    /**
     * The tie-out: every record on each side is either matched or inside exactly one break.
     * internalTotal == matchedTotal + internalInBreaks and processorTotal == matchedTotal + processorInBreaks.
     * A report that does not tie out has lost or double-counted a record and must not be published.
     */
    boolean tiesOut() {
        return internalTotal.equals(matchedTotal.plus(internalInBreaks()))
                && processorTotal.equals(matchedTotal.plus(processorInBreaks()));
    }

    String summary() {
        StringBuilder sb = new StringBuilder();
        sb.append("as of ").append(asOf).append('\n');
        sb.append(String.format("internal:  %,d records  %s%n", internalCount, internalTotal));
        sb.append(String.format("processor: %,d records  %s%n", processorCount, processorTotal));
        sb.append(String.format("matched:   %,d records  %s%n", matchedCount, matchedTotal));
        sb.append(String.format("buckets: %,d seen, %,d fast-matched by digest, %,d inspected record by record%n",
                bucketsSeen, bucketsFastMatched, bucketsInspected));
        sb.append(String.format("breaks: %,d  (internal side %s, processor side %s)%n",
                breaks.size(), internalInBreaks(), processorInBreaks()));
        for (Map.Entry<BreakType, Long> e : countByType().entrySet()) {
            sb.append(String.format("  %-22s %,d%n", e.getKey(), e.getValue()));
        }
        sb.append("aging: ");
        for (Map.Entry<AgeBand, Long> e : countByAge().entrySet()) {
            sb.append(e.getKey()).append('=').append(e.getValue()).append(' ');
        }
        sb.append('\n');
        sb.append("ties out: ").append(tiesOut()).append('\n');
        return sb.toString();
    }
}

/**
 * Streaming merge-join reconciler. Consumes two feeds sorted by BucketKey, matches whole buckets by
 * digest when they agree, inspects a bucket record by record only when the digests differ, and
 * holds in memory only the current bucket plus the unmatched leftovers. Memory is proportional to
 * the number of breaks, not the number of records.
 */
final class StreamingReconciler {
    private final Currency currency;
    private final int timingToleranceDays;

    StreamingReconciler(Currency currency, int timingToleranceDays) {
        if (timingToleranceDays < 0) throw new IllegalArgumentException("tolerance must be >= 0");
        this.currency = currency;
        this.timingToleranceDays = timingToleranceDays;
    }

    /** A peekable, order-checking cursor over one sorted feed. */
    private static final class Cursor {
        private final Iterator<Rec> it;
        private final Side side;
        private Rec head;
        private BucketKey last;

        Cursor(Iterator<Rec> it, Side side) {
            this.it = it;
            this.side = side;
            advance();
        }

        boolean hasNext() { return head != null; }

        BucketKey key() { return head.bucket(); }

        /**
         * Drain every record of the current bucket into {@code out} and fold it into the digest,
         * except records the {@code defer} test selects, which go to {@code deferred} undigested.
         */
        BucketDigest drainBucket(BucketKey key, List<Rec> out, java.util.function.Predicate<Rec> defer, List<Rec> deferred) {
            BucketDigest d = BucketDigest.EMPTY;
            while (head != null && head.bucket().equals(key)) {
                if (defer.test(head)) {
                    deferred.add(head);
                } else {
                    out.add(head);
                    d = d.add(head);
                }
                advance();
            }
            return d;
        }

        private void advance() {
            if (!it.hasNext()) { head = null; return; }
            Rec next = it.next();
            if (next.side() != side) throw new IllegalArgumentException("record " + next.ref() + " is on the wrong feed");
            BucketKey k = next.bucket();
            if (last != null && k.compareTo(last) < 0) {
                throw new IllegalStateException(side + " feed is not sorted: " + k + " after " + last);
            }
            last = k;
            head = next;
        }
    }

    ReconciliationReport reconcile(Iterator<Rec> internal, Iterator<Rec> processor, LocalDate asOf) {
        ReconciliationReport r = new ReconciliationReport(currency, asOf);
        Cursor a = new Cursor(internal, Side.INTERNAL);
        Cursor b = new Cursor(processor, Side.PROCESSOR);
        // Unmatched records, keyed by ref, waiting for a partner from a different bucket.
        Map<String, List<Rec>> leftoverInternal = new HashMap<>();
        Map<String, List<Rec>> leftoverProcessor = new HashMap<>();
        List<Rec> bufA = new ArrayList<>();
        List<Rec> bufB = new ArrayList<>();
        List<Rec> late = new ArrayList<>();
        java.util.function.Predicate<Rec> never = x -> false;
        java.util.function.Predicate<Rec> settledLate = x -> x.lagDays() > timingToleranceDays;

        while (a.hasNext() || b.hasNext()) {
            BucketKey key;
            if (!a.hasNext()) key = b.key();
            else if (!b.hasNext()) key = a.key();
            else key = a.key().compareTo(b.key()) <= 0 ? a.key() : b.key();
            r.bucketsSeen++;

            bufA.clear();
            bufB.clear();
            late.clear();
            BucketDigest da = a.hasNext() && a.key().equals(key) ? a.drainBucket(key, bufA, never, late) : BucketDigest.EMPTY;
            BucketDigest db = b.hasNext() && b.key().equals(key) ? b.drainBucket(key, bufB, settledLate, late) : BucketDigest.EMPTY;
            r.internalCount += da.count();
            r.processorCount += db.count() + late.size();
            r.internalTotal = r.internalTotal.plus(Money.ofMinor(da.totalMinor(), currency));
            r.processorTotal = r.processorTotal.plus(Money.ofMinor(db.totalMinor(), currency));
            for (Rec y : late) {
                r.processorTotal = r.processorTotal.plus(y.amount());
                leftoverProcessor.computeIfAbsent(y.ref(), k -> new ArrayList<>()).add(y);
            }

            if (da.equals(db) && late.isEmpty()) {
                // Fast path: same count, same digest, same total -> the bucket matches as a whole.
                r.bucketsFastMatched++;
                r.matchedCount += da.count();
                r.matchedTotal = r.matchedTotal.plus(Money.ofMinor(da.totalMinor(), currency));
                continue;
            }
            r.bucketsInspected++;
            inspectBucket(bufA, bufB, r, leftoverInternal, leftoverProcessor, asOf);
        }
        classifyLeftovers(leftoverInternal, leftoverProcessor, r, asOf);
        r.breaks.sort(Comparator.comparing(Break::type).thenComparing(Break::ref));
        return r;
    }

    /**
     * Within one bucket, pair records by ref. A second copy of a ref already seen in this bucket is
     * a DUPLICATE on that side; anything unpaired is parked as a leftover for the cross-bucket pass.
     */
    private void inspectBucket(List<Rec> bufA, List<Rec> bufB, ReconciliationReport r,
                               Map<String, List<Rec>> leftoverInternal, Map<String, List<Rec>> leftoverProcessor,
                               LocalDate asOf) {
        Map<String, Rec> byRef = new HashMap<>();
        java.util.Set<String> matchedHere = new java.util.HashSet<>();
        for (Rec x : bufA) {
            Rec prev = byRef.putIfAbsent(x.ref(), x);
            if (prev != null) r.breaks.add(new Break(BreakType.DUPLICATE, x.ref(), x, null, ageDays(x.txDate(), asOf)));
        }
        for (Rec y : bufB) {
            Rec x = byRef.remove(y.ref());
            if (x != null) {
                r.matchedCount += 1;                               // count the pair once
                r.matchedTotal = r.matchedTotal.plus(x.amount());  // one amount, matched on both sides
                matchedHere.add(y.ref());
            } else if (matchedHere.contains(y.ref())) {
                r.breaks.add(new Break(BreakType.DUPLICATE, y.ref(), null, y, ageDays(y.txDate(), asOf)));
            } else {
                leftoverProcessor.computeIfAbsent(y.ref(), k -> new ArrayList<>()).add(y);
            }
        }
        for (Rec x : byRef.values()) leftoverInternal.computeIfAbsent(x.ref(), k -> new ArrayList<>()).add(x);
    }

    /** Cross-bucket pass: pair leftovers by ref and name the break. Runs once, over breaks only. */
    private void classifyLeftovers(Map<String, List<Rec>> li, Map<String, List<Rec>> lp,
                                   ReconciliationReport r, LocalDate asOf) {
        List<String> refs = new ArrayList<>(li.keySet());
        for (String ref : lp.keySet()) if (!li.containsKey(ref)) refs.add(ref);
        for (String ref : refs) {
            List<Rec> xs = li.getOrDefault(ref, List.of());
            List<Rec> ys = lp.getOrDefault(ref, List.of());
            int pairs = Math.min(xs.size(), ys.size());
            for (int i = 0; i < pairs; i++) {
                Rec x = xs.get(i), y = ys.get(i);
                long age = ageDays(x.txDate(), asOf);
                if (!x.amount().equals(y.amount())) {
                    r.breaks.add(new Break(BreakType.AMOUNT_MISMATCH, ref, x, y, age));
                } else if (Math.abs(x.valueDate().toEpochDay() - y.valueDate().toEpochDay()) <= timingToleranceDays) {
                    // Same ref, same amount, dates within tolerance: matched, but recorded for visibility.
                    r.matchedCount += 1;
                    r.matchedTotal = r.matchedTotal.plus(x.amount());
                } else {
                    r.breaks.add(new Break(BreakType.TIMING, ref, x, y, age));
                }
            }
            for (int i = pairs; i < xs.size(); i++) {
                Rec x = xs.get(i);
                BreakType t = (i > 0 || pairs > 0) ? BreakType.DUPLICATE : BreakType.MISSING_AT_PROCESSOR;
                r.breaks.add(new Break(t, ref, x, null, ageDays(x.txDate(), asOf)));
            }
            for (int i = pairs; i < ys.size(); i++) {
                Rec y = ys.get(i);
                BreakType t = (i > 0 || pairs > 0) ? BreakType.DUPLICATE : BreakType.MISSING_INTERNAL;
                r.breaks.add(new Break(t, ref, null, y, ageDays(y.txDate(), asOf)));
            }
        }
    }

    static long ageDays(LocalDate recordDate, LocalDate asOf) {
        return Math.max(0, asOf.toEpochDay() - recordDate.toEpochDay());
    }
}

/**
 * Seeded generator of two feeds with a known number of injected breaks, so the demo and the tests
 * can assert exactly what the reconciler must find. Each feed is returned sorted by BucketKey.
 */
final class FeedGenerator {
    record Feeds(List<Rec> internal, List<Rec> processor, int injectedBreaks) {}

    static Feeds generate(long seed, int records, LocalDate start, int days, Currency c, double breakRate) {
        Random rnd = new Random(seed);
        List<Rec> internal = new ArrayList<>(records);
        List<Rec> processor = new ArrayList<>(records);
        int injected = 0;
        for (int i = 0; i < records; i++) {
            String ref = "TX" + Long.toString(1_000_000_000L + i, 36).toUpperCase();
            LocalDate d = start.plusDays(rnd.nextInt(days));
            long cents = 100 + rnd.nextInt(50_000);                 // 1.00 .. 500.99
            if (rnd.nextInt(5) == 0) cents = (cents / 100) * 100;   // round prices collide in buckets
            Money amt = Money.ofMinor(cents, c);
            internal.add(new Rec(ref, d, d, amt, Side.INTERNAL));
            if (rnd.nextDouble() < breakRate) {
                injected++;
                switch (rnd.nextInt(4)) {
                    case 0 -> { /* missing at processor: emit nothing */ }
                    case 1 -> processor.add(new Rec(ref, d, d.plusDays(1), Money.ofMinor(cents + 1, c), Side.PROCESSOR)); // off by a cent
                    case 2 -> processor.add(new Rec(ref, d, d.plusDays(5), amt, Side.PROCESSOR));                      // settled late
                    default -> {                                                                                      // file line twice
                        processor.add(new Rec(ref, d, d.plusDays(1), amt, Side.PROCESSOR));
                        processor.add(new Rec(ref, d, d.plusDays(1), amt, Side.PROCESSOR));
                    }
                }
            } else {
                // Normal case: processor settles same day or next day (within tolerance).
                processor.add(new Rec(ref, d, d.plusDays(rnd.nextInt(2)), amt, Side.PROCESSOR));
            }
        }
        // A handful of records only the processor knows about.
        for (int i = 0; i < Math.max(1, records / 200_000); i++) {
            injected++;
            LocalDate d = start.plusDays(rnd.nextInt(days));
            processor.add(new Rec("UNKNOWN" + i, d, d.plusDays(1), Money.ofMinor(999, c), Side.PROCESSOR));
        }
        sortFeed(internal);
        sortFeed(processor);
        return new Feeds(internal, processor, injected);
    }

    static void sortFeed(List<Rec> feed) {
        feed.sort(Comparator.comparing(Rec::bucket, BucketKey.ORDER).thenComparing(Rec::ref));
    }
}

public final class Main {
    public static void main(String[] args) {
        Currency USD = Currency.USD;
        LocalDate asOf = LocalDate.of(2026, 3, 31);

        System.out.println("== A small run you can read ==");
        LocalDate m28 = LocalDate.of(2026, 3, 28), m29 = LocalDate.of(2026, 3, 29), m1 = LocalDate.of(2026, 3, 1);
        List<Rec> internal = new ArrayList<>(List.of(
                new Rec("A1", m28, m28, Money.ofMinor(1999, USD), Side.INTERNAL),
                new Rec("A2", m28, m28, Money.ofMinor(1999, USD), Side.INTERNAL),
                new Rec("B1", m28, m28, Money.ofMinor(5000, USD), Side.INTERNAL),
                new Rec("C1", m29, m29, Money.ofMinor(1250, USD), Side.INTERNAL),
                new Rec("D1", m1, m1, Money.ofMinor(700, USD), Side.INTERNAL)));
        List<Rec> processor = new ArrayList<>(List.of(
                new Rec("A1", m28, m29, Money.ofMinor(1999, USD), Side.PROCESSOR),   // settled T+1: fine
                new Rec("A2", m28, m29, Money.ofMinor(1999, USD), Side.PROCESSOR),
                new Rec("B1", m28, LocalDate.of(2026, 4, 3), Money.ofMinor(5000, USD), Side.PROCESSOR), // settled T+6: TIMING
                new Rec("C1", m29, m29, Money.ofMinor(1200, USD), Side.PROCESSOR),   // 12.00 vs 12.50: AMOUNT_MISMATCH
                new Rec("E9", m29, m29, Money.ofMinor(4200, USD), Side.PROCESSOR))); // we never saw it: MISSING_INTERNAL
        FeedGenerator.sortFeed(internal);
        FeedGenerator.sortFeed(processor);
        StreamingReconciler rec = new StreamingReconciler(USD, 2);
        ReconciliationReport small = rec.reconcile(internal.iterator(), processor.iterator(), asOf);
        System.out.print(small.summary());
        for (Break b : small.breaks) System.out.println("  " + b);

        System.out.println("\n== 1,000,000 records, seeded, 0.1% injected breaks ==");
        long t0 = System.nanoTime();
        FeedGenerator.Feeds feeds = FeedGenerator.generate(42, 500_000, LocalDate.of(2026, 3, 1), 30, USD, 0.001);
        long t1 = System.nanoTime();
        ReconciliationReport big = rec.reconcile(feeds.internal().iterator(), feeds.processor().iterator(), asOf);
        long t2 = System.nanoTime();
        System.out.printf("generated %,d + %,d records in %d ms (%d break situations injected)%n",
                feeds.internal().size(), feeds.processor().size(), (t1 - t0) / 1_000_000, feeds.injectedBreaks());
        System.out.printf("reconciled in %d ms%n", (t2 - t1) / 1_000_000);
        System.out.print(big.summary());
        System.out.println("first three breaks: ");
        for (Break b : big.breaks.subList(0, Math.min(3, big.breaks.size()))) System.out.println("  " + b);

        System.out.println("\n== An unsorted feed is refused, not silently mis-reconciled ==");
        try {
            List<Rec> shuffled = new ArrayList<>(internal);
            java.util.Collections.swap(shuffled, 0, shuffled.size() - 1);
            rec.reconcile(shuffled.iterator(), processor.iterator(), asOf);
        } catch (IllegalStateException e) {
            System.out.println("rejected: " + e.getMessage());
        }
        if (!big.tiesOut() || !small.tiesOut()) {
            System.out.println("TIE-OUT FAILED");
            System.exit(1);
        }
    }
}
