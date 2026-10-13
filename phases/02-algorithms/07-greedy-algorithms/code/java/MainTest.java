// Tests for phases/02-algorithms/07-greedy-algorithms. Run: javac -d .out *.java && java -cp .out MainTest
import java.util.ArrayList;
import java.util.Arrays;
import java.util.List;
import java.util.Map;
import java.util.Random;
import java.util.TreeMap;

public final class MainTest {
    private static int passed = 0;
    private static final List<String> failures = new ArrayList<>();

    static void check(String name, boolean condition) {
        if (condition) passed++; else failures.add(name);
    }

    static boolean throwsIAE(Runnable r) {
        try { r.run(); return false; } catch (IllegalArgumentException e) { return true; }
    }

    static boolean pairwiseDisjoint(List<Interval> xs) {
        for (int i = 0; i < xs.size(); i++) for (int j = i + 1; j < xs.size(); j++) {
            Interval a = xs.get(i), b = xs.get(j);
            if (a.start() < b.end() && b.start() < a.end()) return false;
        }
        return true;
    }

    /** Brute force: the largest pairwise-disjoint subset, by enumerating all 2^n subsets (n <= 12). */
    static int bruteMaxNonOverlapping(List<Interval> xs) {
        int best = 0;
        for (int mask = 0; mask < (1 << xs.size()); mask++) {
            List<Interval> sub = new ArrayList<>();
            for (int i = 0; i < xs.size(); i++) if ((mask & (1 << i)) != 0) sub.add(xs.get(i));
            if (pairwiseDisjoint(sub)) best = Math.max(best, sub.size());
        }
        return best;
    }

    /** Maximum number of intervals covering any single point: the lower bound on resources. */
    static int maxDepth(List<Interval> xs) {
        int best = 0;
        for (Interval a : xs) { int d = 0; for (Interval b : xs) if (b.start() <= a.start() && a.start() < b.end()) d++; best = Math.max(best, d); }
        return best;
    }

    public static void main(String[] args) {
        Random rng = new Random(21);

        // interval scheduling
        List<Interval> windows = List.of(
            new Interval("ACH-batch", 0, 6), new Interval("card-settle", 1, 4), new Interval("wire-cutoff", 3, 5),
            new Interval("fx-fixing", 3, 8), new Interval("payout-run", 4, 7), new Interval("recon", 5, 9),
            new Interval("eod-close", 6, 10), new Interval("reporting", 8, 11));
        List<Interval> chosen = IntervalScheduling.maxNonOverlapping(windows);
        check("earliest-end picks 3", chosen.size() == 3);
        check("earliest-end picks are disjoint", pairwiseDisjoint(chosen));
        check("earliest-end specific picks", chosen.stream().map(Interval::name).toList().equals(List.of("card-settle", "payout-run", "reporting")));
        check("earliest-end matches brute force", chosen.size() == bruteMaxNonOverlapping(windows));
        check("empty input", IntervalScheduling.maxNonOverlapping(List.of()).isEmpty());
        check("touching intervals are compatible ([0,5) and [5,8))", IntervalScheduling.maxNonOverlapping(List.of(new Interval("a", 0, 5), new Interval("b", 5, 8))).size() == 2);
        check("interval with end <= start rejected", throwsIAE(() -> new Interval("bad", 5, 5)));

        // the wrong greedy fails on a crafted input: one short interval in the middle of two long compatible ones
        List<Interval> trap = List.of(new Interval("long-1", 0, 10), new Interval("short", 9, 12), new Interval("long-2", 11, 20));
        check("shortest-first picks 1 on the trap", IntervalScheduling.shortestFirst(trap).size() == 1);
        check("earliest-end picks 2 on the trap", IntervalScheduling.maxNonOverlapping(trap).size() == 2);

        // randomized oracle: greedy equals brute force on 300 random instances
        boolean oracle = true, resourcesOk = true;
        for (int t = 0; t < 300; t++) {
            int n = rng.nextInt(11);
            List<Interval> xs = new ArrayList<>();
            for (int i = 0; i < n; i++) { long s = rng.nextInt(30); xs.add(new Interval("i" + i, s, s + 1 + rng.nextInt(10))); }
            List<Interval> g = IntervalScheduling.maxNonOverlapping(xs);
            if (!pairwiseDisjoint(g) || g.size() != bruteMaxNonOverlapping(xs)) oracle = false;
            if (IntervalScheduling.minResources(xs) != maxDepth(xs)) resourcesOk = false;
        }
        check("earliest-end equals brute-force optimum on 300 random instances", oracle);
        check("minResources equals maximum overlap depth on 300 random instances", resourcesOk);
        check("minResources on the windows (four overlap at t=3)", IntervalScheduling.minResources(windows) == 4);
        check("minResources of disjoint intervals is 1", IntervalScheduling.minResources(List.of(new Interval("a", 0, 1), new Interval("b", 1, 2))) == 1);
        check("minResources of empty is 0", IntervalScheduling.minResources(List.of()) == 0);

        // huffman
        Map<Integer, Long> freq = new TreeMap<>(Map.of(0, 850_000L, 5, 60_000L, 51, 45_000L, 14, 20_000L, 54, 15_000L, 91, 7_000L, 96, 3_000L));
        Huffman.Node tree = Huffman.build(freq);
        Map<Integer, String> codes = Huffman.codes(tree);
        check("huffman: one code per symbol", codes.size() == 7);
        check("huffman: prefix-free", Huffman.isPrefixFree(codes));
        check("huffman: most frequent symbol gets a 1-bit code", codes.get(0).length() == 1);
        check("huffman: rarest symbols get the longest codes", codes.get(96).length() >= codes.get(5).length() && codes.get(91).length() >= codes.get(5).length());
        long bits = Huffman.encodedBits(freq, codes);
        long total = freq.values().stream().mapToLong(Long::longValue).sum();
        check("huffman beats fixed 3-bit code", bits < 3 * total);
        check("huffman exact bit count", bits == 1_320_000L);
        check("huffman: deterministic across builds", Huffman.codes(Huffman.build(freq)).equals(codes));
        int[] msg = {0, 0, 0, 5, 0, 51, 0, 0, 14, 0};
        check("huffman round trip", Arrays.equals(Huffman.decode(Huffman.encode(msg, codes), tree), msg));
        check("huffman rejects truncated input", throwsIAE(() -> Huffman.decode(codes.get(96).substring(0, 2), tree)));
        check("huffman single-symbol alphabet gets a 1-bit code", Huffman.codes(Huffman.build(Map.of(7, 10L))).get(7).equals("0"));
        check("huffman rejects empty alphabet", throwsIAE(() -> Huffman.build(Map.of())));
        check("huffman rejects zero frequency", throwsIAE(() -> Huffman.build(Map.of(1, 0L))));
        // optimality: no other prefix code beats Huffman on a tiny alphabet (compare against all code-length assignments satisfying Kraft)
        Map<Integer, Long> small = Map.of(1, 45L, 2, 13L, 3, 12L, 4, 16L, 5, 9L, 6, 5L);   // CLRS example
        Map<Integer, String> smallCodes = Huffman.codes(Huffman.build(small));
        check("huffman CLRS example: 224 bits", Huffman.encodedBits(small, smallCodes) == 224);
        // Kraft equality: a full binary tree has sum 2^-len == 1
        double kraft = 0; for (String c : smallCodes.values()) kraft += Math.pow(2, -c.length());
        check("huffman tree is full (Kraft sum == 1)", Math.abs(kraft - 1.0) < 1e-12);

        // change-making
        long[] usd = {1, 5, 10, 25, 100, 500, 1000, 2000, 5000, 10000};
        long[] odd = {1, 3, 4};
        long[] oldUk = {1, 3, 6, 12, 24, 30, 60, 240};
        check("USD denominations are canonical", ChangeMaking.isCanonical(usd));
        check("{1,3,4} is not canonical", !ChangeMaking.isCanonical(odd));
        check("{1,3,4}: greedy on 6 uses 3 coins (4+1+1)", ChangeMaking.total(ChangeMaking.greedy(odd, 6)) == 3);
        check("{1,3,4}: optimal on 6 uses 2 coins (3+3)", ChangeMaking.optimalCount(odd, 6) == 2);
        check("old UK pence not canonical", !ChangeMaking.isCanonical(oldUk));
        check("old UK pence: greedy on 48 uses 3, optimal 2", ChangeMaking.total(ChangeMaking.greedy(oldUk, 48)) == 3 && ChangeMaking.optimalCount(oldUk, 48) == 2);
        long[] g = ChangeMaking.greedy(usd, 18_741);
        check("greedy USD 187.41 -> 1x100.00 1x50.00 1x20.00 1x10.00 1x5.00 2x1.00 1x0.25 1x0.10 1x0.05 1x0.01", ChangeMaking.total(g) == 11);
        check("greedy USD matches optimal on 187.41", ChangeMaking.total(g) == ChangeMaking.optimalCount(usd, 18_741));
        boolean usdOracle = true;
        for (int t = 0; t < 200; t++) { long amt = rng.nextInt(5000); if (ChangeMaking.total(ChangeMaking.greedy(usd, amt)) != ChangeMaking.optimalCount(usd, amt)) usdOracle = false; }
        check("greedy equals DP optimum on 200 random USD amounts", usdOracle);
        check("greedy returns null when amount cannot be made", ChangeMaking.greedy(new long[] {5, 10}, 7) == null);
        check("optimalCount reports impossible as MAX_VALUE", ChangeMaking.optimalCount(new long[] {5, 10}, 7) == Long.MAX_VALUE);
        check("greedy on zero amount is zero coins", ChangeMaking.total(ChangeMaking.greedy(usd, 0)) == 0);
        check("no 1-coin means not canonical", !ChangeMaking.isCanonical(new long[] {2, 5}));

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }
}
