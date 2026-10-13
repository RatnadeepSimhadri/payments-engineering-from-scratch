// Tests for phases/14-payments-at-scale/04-reconciliation-at-scale. Run: javac -d .out *.java && java -cp .out MainTest
import java.time.LocalDate;
import java.util.ArrayList;
import java.util.List;
import java.util.Map;

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
            if (type.isInstance(t)) passed++; else failures.add(name + " (wrong exception " + t.getClass().getSimpleName() + ")");
        }
    }

    static Rec in(String ref, String date, long cents) { return new Rec(ref, LocalDate.parse(date), LocalDate.parse(date), Money.ofMinor(cents, Currency.USD), Side.INTERNAL); }
    static Rec pr(String ref, String date, long cents) { return new Rec(ref, LocalDate.parse(date), LocalDate.parse(date), Money.ofMinor(cents, Currency.USD), Side.PROCESSOR); }
    static Rec pr(String ref, String txDate, String valueDate, long cents) { return new Rec(ref, LocalDate.parse(txDate), LocalDate.parse(valueDate), Money.ofMinor(cents, Currency.USD), Side.PROCESSOR); }

    static ReconciliationReport run(List<Rec> a, List<Rec> b, int tolerance) {
        List<Rec> x = new ArrayList<>(a), y = new ArrayList<>(b);
        FeedGenerator.sortFeed(x);
        FeedGenerator.sortFeed(y);
        return new StreamingReconciler(Currency.USD, tolerance).reconcile(x.iterator(), y.iterator(), LocalDate.parse("2026-03-31"));
    }

    static long count(ReconciliationReport r, BreakType t) { return r.countByType().getOrDefault(t, 0L); }

    public static void main(String[] args) {
        // digest: order independent, count aware
        BucketDigest d1 = BucketDigest.EMPTY.add(in("A", "2026-03-01", 100)).add(in("B", "2026-03-01", 100));
        BucketDigest d2 = BucketDigest.EMPTY.add(in("B", "2026-03-01", 100)).add(in("A", "2026-03-01", 100));
        check("digest is order independent", d1.equals(d2));
        check("digest differs on a different ref", !d1.equals(BucketDigest.EMPTY.add(in("A", "2026-03-01", 100)).add(in("C", "2026-03-01", 100))));
        check("digest counts records", d1.count() == 2 && d1.totalMinor() == 200);

        // happy path: identical feeds match entirely on the fast path
        List<Rec> a = List.of(in("A", "2026-03-01", 1999), in("B", "2026-03-01", 1999), in("C", "2026-03-02", 500));
        List<Rec> b = List.of(pr("A", "2026-03-01", 1999), pr("B", "2026-03-01", 1999), pr("C", "2026-03-02", 500));
        ReconciliationReport r = run(a, b, 2);
        check("all matched", r.matchedCount == 3 && r.breaks.isEmpty());
        check("fast path used for every bucket", r.bucketsFastMatched == 2 && r.bucketsInspected == 0);
        check("totals agree", r.internalTotal.equals(r.processorTotal) && r.matchedTotal.minorUnits() == 4498);
        check("ties out", r.tiesOut());

        // missing at processor
        r = run(List.of(in("A", "2026-03-01", 100)), List.of(), 2);
        check("missing at processor", count(r, BreakType.MISSING_AT_PROCESSOR) == 1 && r.breaks.get(0).processor() == null);
        check("missing at processor ties out", r.tiesOut() && r.internalInBreaks().minorUnits() == 100);

        // missing internal
        r = run(List.of(), List.of(pr("Z", "2026-03-01", 4200)), 2);
        check("missing internal", count(r, BreakType.MISSING_INTERNAL) == 1 && r.processorInBreaks().minorUnits() == 4200 && r.tiesOut());

        // amount mismatch: same ref lands in different buckets, paired in the cross-bucket pass
        r = run(List.of(in("C", "2026-03-02", 1250)), List.of(pr("C", "2026-03-02", 1200)), 2);
        check("amount mismatch classified", count(r, BreakType.AMOUNT_MISMATCH) == 1);
        check("amount mismatch carries both sides", r.breaks.get(0).internal().amount().minorUnits() == 1250 && r.breaks.get(0).processor().amount().minorUnits() == 1200);
        check("amount mismatch ties out", r.tiesOut() && r.matchedCount == 0);

        // timing: within tolerance is matched, beyond tolerance is a TIMING break
        r = run(List.of(in("B", "2026-03-01", 5000)), List.of(pr("B", "2026-03-01", "2026-03-02", 5000)), 2);
        check("one day lag inside tolerance is matched on the fast path", r.matchedCount == 1 && r.breaks.isEmpty() && r.bucketsFastMatched == 1 && r.tiesOut());
        r = run(List.of(in("B", "2026-03-01", 5000)), List.of(pr("B", "2026-03-01", "2026-03-06", 5000)), 2);
        check("five day lag is a TIMING break", count(r, BreakType.TIMING) == 1 && r.tiesOut() && r.processorInBreaks().minorUnits() == 5000);
        r = run(List.of(in("B", "2026-03-01", 5000)), List.of(pr("B", "2026-03-01", "2026-03-02", 5000)), 0);
        check("zero tolerance turns the same lag into a break", count(r, BreakType.TIMING) == 1);

        // duplicates on one side
        r = run(List.of(in("A", "2026-03-01", 100)), List.of(pr("A", "2026-03-01", 100), pr("A", "2026-03-01", "2026-03-02", 100)), 2);
        check("duplicate settlement line", count(r, BreakType.DUPLICATE) == 1 && r.matchedCount == 1 && r.tiesOut());
        check("duplicate break names the processor side", r.breaks.get(0).internal() == null && r.breaks.get(0).processor() != null);
        r = run(List.of(in("A", "2026-03-01", 100), in("A", "2026-03-01", 100)), List.of(), 2);
        check("two internal copies with no partner: one missing, one duplicate",
                count(r, BreakType.MISSING_AT_PROCESSOR) == 1 && count(r, BreakType.DUPLICATE) == 1 && r.tiesOut());

        // aging
        r = run(List.of(in("D", "2026-03-01", 700), in("E", "2026-03-30", 700)), List.of(), 2);
        Map<AgeBand, Long> ages = r.countByAge();
        check("aging bands", ages.getOrDefault(AgeBand.D8_30, 0L) == 1 && ages.getOrDefault(AgeBand.D0_1, 0L) == 1);
        check("age band boundaries", AgeBand.of(1) == AgeBand.D0_1 && AgeBand.of(2) == AgeBand.D2_7 && AgeBand.of(7) == AgeBand.D2_7
                && AgeBand.of(8) == AgeBand.D8_30 && AgeBand.of(31) == AgeBand.D30_PLUS);
        check("age never negative", StreamingReconciler.ageDays(LocalDate.parse("2026-04-30"), LocalDate.parse("2026-03-31")) == 0);

        // the failure the lesson warns about: an unsorted feed must be refused
        List<Rec> unsorted = List.of(in("B", "2026-03-02", 100), in("A", "2026-03-01", 100));
        checkThrows("unsorted feed refused", IllegalStateException.class,
                () -> new StreamingReconciler(Currency.USD, 2).reconcile(unsorted.iterator(), List.<Rec>of().iterator(), LocalDate.parse("2026-03-31")));
        checkThrows("wrong-side record refused", IllegalArgumentException.class,
                () -> new StreamingReconciler(Currency.USD, 2).reconcile(List.of(pr("A", "2026-03-01", 1)).iterator(), List.<Rec>of().iterator(), LocalDate.parse("2026-03-31")));

        // seeded large run: every injected situation is surfaced, nothing invented, tie-out holds
        FeedGenerator.Feeds feeds = FeedGenerator.generate(7, 100_000, LocalDate.parse("2026-03-01"), 30, Currency.USD, 0.001);
        ReconciliationReport big = new StreamingReconciler(Currency.USD, 2).reconcile(feeds.internal().iterator(), feeds.processor().iterator(), LocalDate.parse("2026-03-31"));
        check("large run ties out", big.tiesOut());
        check("large run finds every injected break", big.breaks.size() == feeds.injectedBreaks());
        check("large run matched + breaks cover internal feed",
                big.matchedCount + big.breaks.stream().filter(x -> x.internal() != null).count() == feeds.internal().size());
        check("large run mostly fast path", big.bucketsFastMatched > big.bucketsInspected * 10);
        check("determinism: same seed, same report", FeedGenerator.generate(7, 100_000, LocalDate.parse("2026-03-01"), 30, Currency.USD, 0.001).injectedBreaks() == feeds.injectedBreaks());

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }
}
