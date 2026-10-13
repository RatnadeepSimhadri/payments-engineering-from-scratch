// Tests for phases/02-algorithms/13-interval-problems. Run: javac -d .out *.java && java -cp .out MainTest
import java.util.ArrayList;
import java.util.List;
import java.util.Random;

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

    static Interval iv(long s, long e) { return new Interval(s, e); }

    public static void main(String[] args) {
        // the interval type and half-open semantics
        check("length", iv(3, 10).length() == 7);
        check("touching intervals do not overlap", !iv(0, 10).overlaps(iv(10, 20)));
        check("touching intervals do touch", iv(0, 10).overlapsOrTouches(iv(10, 20)));
        check("overlap is symmetric", iv(0, 10).overlaps(iv(5, 15)) && iv(5, 15).overlaps(iv(0, 10)));
        check("contains is half-open", iv(0, 10).contains(0) && iv(0, 10).contains(9) && !iv(0, 10).contains(10));
        check("intersect", iv(0, 10).intersect(iv(5, 15)).equals(iv(5, 10)));
        check("intersect of disjoint is empty", iv(0, 5).intersect(iv(7, 9)).isEmpty());
        check("sort by start then end", iv(1, 5).compareTo(iv(1, 7)) < 0 && iv(2, 3).compareTo(iv(1, 9)) > 0);
        checkThrows("end before start rejected", IllegalArgumentException.class, () -> iv(5, 4));
        check("empty interval allowed", iv(5, 5).isEmpty());

        // merge
        List<Interval> windows = List.of(iv(840, 930), iv(900, 960), iv(960, 1005), iv(540, 600), iv(1080, 1080));
        check("merge: hand trace", Intervals.merge(windows).equals(List.of(iv(540, 600), iv(840, 1005))));
        check("merge: touching intervals merge", Intervals.merge(List.of(iv(0, 5), iv(5, 10))).equals(List.of(iv(0, 10))));
        check("merge: contained interval absorbed", Intervals.merge(List.of(iv(0, 100), iv(10, 20))).equals(List.of(iv(0, 100))));
        check("merge: unsorted input", Intervals.merge(List.of(iv(50, 60), iv(0, 10), iv(5, 55))).equals(List.of(iv(0, 60))));
        check("merge: empty input", Intervals.merge(List.of()).isEmpty());
        check("merge: empty intervals dropped", Intervals.merge(List.of(iv(3, 3), iv(7, 7))).isEmpty());
        check("merge: input untouched", windows.get(0).equals(iv(840, 930)));
        List<Interval> merged = Intervals.merge(windows);
        boolean disjoint = true;
        for (int i = 1; i < merged.size(); i++) disjoint &= merged.get(i - 1).end() < merged.get(i).start();
        check("merge: output is strictly disjoint and sorted", disjoint);

        // coverage and gaps
        check("coverage 225", Intervals.coverage(windows) == 225);
        check("gaps in 08:00-19:00", Intervals.gaps(windows, iv(480, 1140)).equals(List.of(iv(480, 540), iv(600, 840), iv(1005, 1140))));
        check("gaps: fully covered range has none", Intervals.gaps(List.of(iv(0, 10)), iv(2, 8)).isEmpty());
        check("gaps: nothing covered is one gap", Intervals.gaps(List.of(), iv(0, 10)).equals(List.of(iv(0, 10))));
        check("gaps: interval outside the range ignored", Intervals.gaps(List.of(iv(100, 200)), iv(0, 10)).equals(List.of(iv(0, 10))));

        // sweep line
        check("maxOverlap: hand trace", Intervals.maxOverlap(windows).equals(new Peak(2, 900)));
        check("maxOverlap: touching intervals count as 1", Intervals.maxOverlap(List.of(iv(0, 10), iv(10, 20))).count() == 1);
        check("maxOverlap: three nested", Intervals.maxOverlap(List.of(iv(0, 10), iv(2, 8), iv(4, 6))).equals(new Peak(3, 4)));
        check("maxOverlap: empty", Intervals.maxOverlap(List.of()).equals(new Peak(0, 0)));
        check("maxOverlap: first instant reported", Intervals.maxOverlap(List.of(iv(0, 5), iv(1, 5), iv(10, 15), iv(11, 15))).equals(new Peak(2, 1)));

        // interval scheduling
        List<Interval> slots = List.of(iv(1, 4), iv(3, 5), iv(0, 6), iv(5, 7), iv(3, 9), iv(5, 9), iv(6, 10), iv(8, 11), iv(8, 12), iv(2, 14), iv(12, 16));
        List<Interval> chosen = Intervals.maxNonOverlapping(slots);
        check("scheduling: textbook answer has 4", chosen.equals(List.of(iv(1, 4), iv(5, 7), iv(8, 11), iv(12, 16))));
        check("scheduling: chosen are pairwise disjoint", Intervals.maxOverlap(chosen).count() == 1);
        check("scheduling: touching allowed", Intervals.maxNonOverlapping(List.of(iv(0, 5), iv(5, 10))).size() == 2);
        check("scheduling: earliest-start greedy would be wrong here", Intervals.maxNonOverlapping(List.of(iv(0, 100), iv(1, 2), iv(3, 4))).size() == 2);
        check("scheduling: empty", Intervals.maxNonOverlapping(List.of()).isEmpty());

        // greedy optimality cross-checked against brute force on 100 seeded instances
        Random rng = new Random(13);
        boolean optimal = true;
        for (int trial = 0; trial < 100; trial++) {
            List<Interval> rand = new ArrayList<>();
            int n = 1 + rng.nextInt(10);
            for (int i = 0; i < n; i++) {
                long s = rng.nextInt(20);
                rand.add(iv(s, s + 1 + rng.nextInt(8)));
            }
            optimal &= Intervals.maxNonOverlapping(rand).size() == bruteForceMax(rand);
            optimal &= Intervals.maxOverlap(rand).count() == bruteForceOverlap(rand);
            optimal &= Intervals.coverage(rand) == bruteForceCoverage(rand);
        }
        check("100 random instances: greedy size, sweep peak, and coverage match brute force", optimal);

        // stabbing
        check("stabbing", Intervals.stabbing(slots, 5).equals(List.of(iv(0, 6), iv(2, 14), iv(3, 9), iv(5, 7), iv(5, 9))));

        // holds and exposure
        HoldBook book = Main.sampleHolds();
        Currency usd = Currency.USD;
        check("exposure at 12:30", book.exposureAt(Main.t(12, 30)).equals(Money.ofMinor(83_000, usd)));
        check("exposure at 17:00 is half-open: hotel gone, coffee on", book.exposureAt(Main.t(17, 0)).equals(Money.ofMinor(600, usd)));
        check("exposure before any hold", book.exposureAt(0).equals(Money.zero(usd)));
        PeakExposure peak = book.peakExposure();
        check("peak exposure 875.00 at 11:00 with 3 holds", peak.equals(new PeakExposure(Money.ofMinor(87_500, usd), Main.t(11, 0), 3)));
        check("peak exposure equals exposureAt(peak.at)", book.exposureAt(peak.at()).equals(peak.amount()));
        check("busy periods merge touching holds", book.busyPeriods().equals(List.of(iv(Main.t(9, 0), Main.t(17, 15)))));
        check("expired by 14:00", book.expiredBy(Main.t(14, 0)).stream().map(Hold::id).toList().equals(List.of("fuel", "restaurant", "parking")));
        checkThrows("hold amount must be positive", IllegalArgumentException.class, () -> new Hold("x", Money.zero(usd), iv(0, 1)));
        checkThrows("currency mismatch on the book", IllegalArgumentException.class,
                () -> new HoldBook(usd).add(new Hold("x", Money.ofMinor(1, Currency.EUR), iv(0, 1))));
        check("empty book peak is zero", new HoldBook(usd).peakExposure().equals(new PeakExposure(Money.zero(usd), 0, 0)));
        check("money toString", Money.ofMinor(87_500, usd).toString().equals("875.00 USD"));

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }

    /** Largest pairwise-disjoint subset by trying every subset (n <= 10). */
    static int bruteForceMax(List<Interval> ivs) {
        int best = 0;
        for (int mask = 0; mask < (1 << ivs.size()); mask++) {
            boolean ok = true;
            for (int i = 0; i < ivs.size() && ok; i++) {
                if ((mask & (1 << i)) == 0) continue;
                for (int j = i + 1; j < ivs.size(); j++) {
                    if ((mask & (1 << j)) != 0 && ivs.get(i).overlaps(ivs.get(j))) { ok = false; break; }
                }
            }
            if (ok) best = Math.max(best, Integer.bitCount(mask));
        }
        return best;
    }

    /** Max number of intervals containing any single instant, by checking every instant. */
    static int bruteForceOverlap(List<Interval> ivs) {
        int best = 0;
        for (long t = 0; t < 40; t++) {
            int c = 0;
            for (Interval iv : ivs) if (iv.contains(t)) c++;
            best = Math.max(best, c);
        }
        return best;
    }

    static long bruteForceCoverage(List<Interval> ivs) {
        long c = 0;
        for (long t = 0; t < 40; t++) for (Interval iv : ivs) if (iv.contains(t)) { c++; break; }
        return c;
    }
}
