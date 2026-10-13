// Tests for phases/02-algorithms/03-two-pointers-and-sliding-windows. Run: javac -d .out *.java && java -cp .out MainTest
import java.util.ArrayList;
import java.util.Arrays;
import java.util.List;
import java.util.Random;

public final class MainTest {
    private static int passed = 0;
    private static final List<String> failures = new ArrayList<>();

    static void check(String name, boolean condition) {
        if (condition) passed++; else failures.add(name);
    }

    static boolean throwsIAE(Runnable r) {
        try { r.run(); return false; } catch (IllegalArgumentException e) { return true; }
    }

    // brute-force oracles
    static long bruteMaxSumFixed(long[] a, int k) {
        long best = Long.MIN_VALUE;
        for (int i = 0; i + k <= a.length; i++) { long s = 0; for (int j = i; j < i + k; j++) s += a[j]; best = Math.max(best, s); }
        return best;
    }

    static int bruteShortestRun(long[] a, long target) {
        int best = 0;
        for (int i = 0; i < a.length; i++) {
            long s = 0;
            for (int j = i; j < a.length; j++) { s += a[j]; if (s >= target) { int len = j - i + 1; if (best == 0 || len < best) best = len; break; } }
        }
        return best;
    }

    static long[] bruteMaxOfEachWindow(long[] a, int k) {
        long[] out = new long[a.length - k + 1];
        for (int i = 0; i + k <= a.length; i++) { long m = a[i]; for (int j = i; j < i + k; j++) m = Math.max(m, a[j]); out[i] = m; }
        return out;
    }

    public static void main(String[] args) {
        Currency usd = Currency.USD;
        Random rng = new Random(11);

        // two pointers
        long[] sorted = {75, 500, 500, 1999, 1999, 2575, 4500, 12000};
        int[] p = TwoPointers.pairWithSum(sorted, 4574);
        check("pairWithSum finds a pair", p != null && sorted[p[0]] + sorted[p[1]] == 4574 && p[0] < p[1]);
        check("pairWithSum with duplicates", TwoPointers.pairWithSum(sorted, 1000) != null);
        check("pairWithSum none", TwoPointers.pairWithSum(sorted, 1) == null);
        check("pairWithSum empty", TwoPointers.pairWithSum(new long[0], 5) == null);
        check("pairWithSum single element", TwoPointers.pairWithSum(new long[] {5}, 10) == null);
        boolean pairOracle = true;
        for (int t = 0; t < 100; t++) {
            long[] a = new long[rng.nextInt(12)];
            for (int i = 0; i < a.length; i++) a[i] = rng.nextInt(30);
            Arrays.sort(a);
            long target = rng.nextInt(60);
            boolean exists = false;
            for (int i = 0; i < a.length; i++) for (int j = i + 1; j < a.length; j++) if (a[i] + a[j] == target) exists = true;
            int[] got = TwoPointers.pairWithSum(a, target);
            if (exists != (got != null)) pairOracle = false;
            if (got != null && a[got[0]] + a[got[1]] != target) pairOracle = false;
        }
        check("pairWithSum agrees with brute force on 100 random arrays", pairOracle);

        long[] dupes = {1, 1, 2, 3, 3, 3, 7};
        int n = TwoPointers.dedupeSorted(dupes);
        check("dedupeSorted length", n == 4);
        check("dedupeSorted prefix", Arrays.equals(Arrays.copyOf(dupes, n), new long[] {1, 2, 3, 7}));
        check("dedupeSorted empty", TwoPointers.dedupeSorted(new long[0]) == 0);
        check("dedupeSorted all equal", TwoPointers.dedupeSorted(new long[] {4, 4, 4}) == 1);
        check("mergeSorted", Arrays.equals(TwoPointers.mergeSorted(new long[] {1, 4, 7}, new long[] {2, 3, 9}), new long[] {1, 2, 3, 4, 7, 9}));
        check("mergeSorted one empty", Arrays.equals(TwoPointers.mergeSorted(new long[0], new long[] {2, 3}), new long[] {2, 3}));

        // fixed window
        long[] daily = {12000, 4500, 80000, 2575, 30000, 1999, 500, 75};
        check("maxSumFixed k=3", SlidingWindow.maxSumFixed(daily, 3) == 112_575);
        check("maxSumFixed k=1 is max element", SlidingWindow.maxSumFixed(daily, 1) == 80000);
        check("maxSumFixed k=n is total", SlidingWindow.maxSumFixed(daily, daily.length) == 131_649);
        check("maxSumFixed rejects k=0", throwsIAE(() -> SlidingWindow.maxSumFixed(daily, 0)));
        check("maxSumFixed rejects k>n", throwsIAE(() -> SlidingWindow.maxSumFixed(daily, 9)));
        boolean fixedOracle = true;
        for (int t = 0; t < 100; t++) {
            long[] a = new long[1 + rng.nextInt(20)];
            for (int i = 0; i < a.length; i++) a[i] = rng.nextInt(200) - 50;   // negatives allowed here
            int k = 1 + rng.nextInt(a.length);
            if (SlidingWindow.maxSumFixed(a, k) != bruteMaxSumFixed(a, k)) fixedOracle = false;
        }
        check("maxSumFixed agrees with brute force (negatives included)", fixedOracle);

        // variable window
        check("shortestRun exact", SlidingWindow.shortestRunWithSumAtLeast(daily, 100_000) == 3);
        check("shortestRun single element suffices", SlidingWindow.shortestRunWithSumAtLeast(daily, 80_000) == 1);
        check("shortestRun impossible is 0", SlidingWindow.shortestRunWithSumAtLeast(daily, 1_000_000) == 0);
        check("shortestRun whole array", SlidingWindow.shortestRunWithSumAtLeast(new long[] {1, 1, 1}, 3) == 3);
        check("shortestRun rejects negatives (window not monotone)", throwsIAE(() -> SlidingWindow.shortestRunWithSumAtLeast(new long[] {5, -1, 5}, 4)));
        boolean varOracle = true;
        for (int t = 0; t < 200; t++) {
            long[] a = new long[rng.nextInt(15)];
            for (int i = 0; i < a.length; i++) a[i] = rng.nextInt(10);
            long target = 1 + rng.nextInt(30);
            if (SlidingWindow.shortestRunWithSumAtLeast(a, target) != bruteShortestRun(a, target)) varOracle = false;
        }
        check("shortestRun agrees with brute force on 200 random arrays", varOracle);
        check("longestWithoutRepeat abcabcbb", SlidingWindow.longestWithoutRepeat("abcabcbb") == 3);
        check("longestWithoutRepeat bbbb", SlidingWindow.longestWithoutRepeat("bbbb") == 1);
        check("longestWithoutRepeat pwwkew", SlidingWindow.longestWithoutRepeat("pwwkew") == 3);
        check("longestWithoutRepeat empty", SlidingWindow.longestWithoutRepeat("") == 0);
        check("longestWithoutRepeat jumps left past the repeat, not by one", SlidingWindow.longestWithoutRepeat("abba") == 2);

        // monotonic deque
        check("maxOfEachWindow classic", Arrays.equals(SlidingWindow.maxOfEachWindow(new long[] {1, 3, -1, -3, 5, 3, 6, 7}, 3), new long[] {3, 3, 5, 5, 6, 7}));
        check("maxOfEachWindow k=1 is identity", Arrays.equals(SlidingWindow.maxOfEachWindow(daily, 1), daily));
        check("maxOfEachWindow k=n is one global max", Arrays.equals(SlidingWindow.maxOfEachWindow(daily, daily.length), new long[] {80000}));
        check("maxOfEachWindow descending input", Arrays.equals(SlidingWindow.maxOfEachWindow(new long[] {9, 8, 7, 6}, 2), new long[] {9, 8, 7}));
        check("maxOfEachWindow equal values", Arrays.equals(SlidingWindow.maxOfEachWindow(new long[] {5, 5, 5, 5}, 2), new long[] {5, 5, 5}));
        boolean dequeOracle = true;
        for (int t = 0; t < 200; t++) {
            long[] a = new long[1 + rng.nextInt(25)];
            for (int i = 0; i < a.length; i++) a[i] = rng.nextInt(10);   // small range: many ties
            int k = 1 + rng.nextInt(a.length);
            if (!Arrays.equals(SlidingWindow.maxOfEachWindow(a, k), bruteMaxOfEachWindow(a, k))) dequeOracle = false;
        }
        check("maxOfEachWindow agrees with brute force on 200 random arrays", dequeOracle);

        // velocity checker
        VelocityChecker vc = new VelocityChecker(60_000, 4, Money.ofMinor(50_000, usd));
        long t0 = 1_700_000_000_000L;
        check("vc 1st allowed", vc.record(new Event(t0, Money.ofMinor(12_000, usd))) == VelocityChecker.Decision.ALLOW);
        check("vc 2nd allowed", vc.record(new Event(t0 + 10_000, Money.ofMinor(4_500, usd))) == VelocityChecker.Decision.ALLOW);
        check("vc 3rd allowed", vc.record(new Event(t0 + 20_000, Money.ofMinor(25_000, usd))) == VelocityChecker.Decision.ALLOW);
        check("vc 4th allowed (count == max is fine)", vc.record(new Event(t0 + 25_000, Money.ofMinor(1_999, usd))) == VelocityChecker.Decision.ALLOW);
        check("vc 5th within window denied by count", vc.record(new Event(t0 + 30_000, Money.ofMinor(100, usd))) == VelocityChecker.Decision.DENY_COUNT);
        check("vc denied event still counted", vc.countInWindow() == 5 && vc.totalInWindow().minorUnits() == 43_599);
        check("vc at t0+70s two events aged out, amount rule fires", vc.record(new Event(t0 + 70_000, Money.ofMinor(30_000, usd))) == VelocityChecker.Decision.DENY_AMOUNT);
        check("vc total after eviction", vc.countInWindow() == 4 && vc.totalInWindow().minorUnits() == 25_000 + 1_999 + 100 + 30_000);
        check("vc far future clears most of the window", vc.record(new Event(t0 + 125_000, Money.ofMinor(75, usd))) == VelocityChecker.Decision.ALLOW && vc.countInWindow() == 2);
        check("vc rejects out-of-order events", throwsIAE(() -> vc.record(new Event(t0, Money.ofMinor(1, usd)))));
        check("vc rejects non-positive window", throwsIAE(() -> new VelocityChecker(0, 3, Money.ofMinor(1, usd))));

        // boundary: an event exactly windowMs after another is outside the trailing window (strict)
        VelocityChecker edge = new VelocityChecker(60_000, 1, Money.ofMinor(1_000_000, usd));
        edge.record(new Event(t0, Money.ofMinor(1, usd)));
        check("vc event at exactly +window evicts the old one", edge.record(new Event(t0 + 60_000, Money.ofMinor(1, usd))) == VelocityChecker.Decision.ALLOW);
        VelocityChecker edge2 = new VelocityChecker(60_000, 1, Money.ofMinor(1_000_000, usd));
        edge2.record(new Event(t0, Money.ofMinor(1, usd)));
        check("vc event at +window-1ms is still inside", edge2.record(new Event(t0 + 59_999, Money.ofMinor(1, usd))) == VelocityChecker.Decision.DENY_COUNT);

        // money discipline
        check("Money plus/minus exact", Money.ofMinor(1999, usd).plus(Money.ofMinor(1, usd)).minus(Money.ofMinor(2000, usd)).minorUnits() == 0);
        check("Money rejects mixed currencies", throwsIAE(() -> Money.ofMinor(1, usd).plus(Money.ofMinor(1, new Currency("EUR", 2)))));

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }
}
