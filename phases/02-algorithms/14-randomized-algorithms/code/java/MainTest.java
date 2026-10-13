// Tests for phases/02-algorithms/14-randomized-algorithms. Run: javac -d .out *.java && java -cp .out MainTest
import java.util.ArrayList;
import java.util.Arrays;
import java.util.HashMap;
import java.util.HashSet;
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

    public static void main(String[] args) {
        // the generator: deterministic, in range, and not visibly biased
        check("same seed, same stream", new Rng(42).nextLong() == new Rng(42).nextLong() && new Rng(42).nextLong() != new Rng(43).nextLong());
        check("splitmix64 reference value for seed 0", new Rng(0).nextLong() == 0xE220A8397B1DCDAFL);
        Rng r = new Rng(1);
        boolean inRange = true;
        int[] buckets = new int[7];
        for (int i = 0; i < 70_000; i++) {
            int v = r.nextInt(7);
            inRange &= v >= 0 && v < 7;
            buckets[v]++;
        }
        check("nextInt in range", inRange);
        check("nextInt(7) roughly uniform (each bucket within 5% of 10,000)", Arrays.stream(buckets).allMatch(b -> Math.abs(b - 10_000) < 500));
        check("nextInt(1) is always 0", new Rng(9).nextInt(1) == 0 && new Rng(10).nextInt(1) == 0);
        checkThrows("nextInt rejects zero bound", IllegalArgumentException.class, () -> new Rng(1).nextInt(0));
        boolean doubles = true;
        for (int i = 0; i < 10_000; i++) { double d = r.nextDouble(); doubles &= d >= 0.0 && d < 1.0; }
        check("nextDouble in [0,1)", doubles);

        // reservoir: the invariant, by frequency over many trials
        Map<Integer, Integer> picked = new HashMap<>();
        Rng rr = new Rng(123);
        int trials = 30_000;
        for (int t = 0; t < trials; t++) {
            ReservoirSampler<Integer> s = new ReservoirSampler<>(3, rr);
            for (int item = 1; item <= 10; item++) s.offer(item);
            for (int v : s.sample()) picked.merge(v, 1, Integer::sum);
        }
        // each of 10 items should be in the 3-slot reservoir with probability 0.3 -> 9,000 of 30,000, +-5%
        check("reservoir: every item selected with probability k/n", picked.size() == 10
                && picked.values().stream().allMatch(c -> Math.abs(c - 9_000) < 450));
        check("reservoir: the last item is not favoured", Math.abs(picked.get(10) - picked.get(1)) < 600);
        ReservoirSampler<String> small = new ReservoirSampler<>(5, new Rng(1));
        for (String s : List.of("a", "b", "c")) small.offer(s);
        check("reservoir: fewer items than k keeps them all in order", small.sample().equals(List.of("a", "b", "c")) && small.seen() == 3);
        ReservoirSampler<Integer> exact = new ReservoirSampler<>(4, new Rng(1));
        for (int i = 0; i < 1000; i++) exact.offer(i);
        check("reservoir: size never exceeds k", exact.sample().size() == 4 && exact.seen() == 1000);
        check("reservoir: sampled items are distinct stream members", new HashSet<>(exact.sample()).size() == 4 && exact.sample().stream().allMatch(v -> v >= 0 && v < 1000));
        check("reservoir: deterministic under a seed", sampleIds(new Rng(77)).equals(sampleIds(new Rng(77))));
        checkThrows("reservoir rejects k = 0", IllegalArgumentException.class, () -> new ReservoirSampler<Integer>(0, new Rng(1)));

        // weighted reservoir: heavy items are selected far more often
        Map<String, Integer> wpicked = new HashMap<>();
        Rng wr = new Rng(5);
        for (int t = 0; t < 20_000; t++) {
            WeightedReservoirSampler<String> w = new WeightedReservoirSampler<>(1, wr);
            w.offer("small", 1);
            w.offer("large", 9);
            wpicked.merge(w.sample().get(0), 1, Integer::sum);
        }
        check("weighted: 9:1 weights give ~90% large (within 2%)", Math.abs(wpicked.getOrDefault("large", 0) - 18_000) < 400);
        checkThrows("weighted rejects zero weight", IllegalArgumentException.class, () -> new WeightedReservoirSampler<String>(1, new Rng(1)).offer("x", 0));

        // shuffles: Fisher-Yates is uniform, the naive shuffle is not
        int[] fy = Shuffle.permutationCounts(60_000, new Rng(7), true);
        int[] nv = Shuffle.permutationCounts(60_000, new Rng(7), false);
        check("fisher-yates: all 6 permutations within 4% of 10,000", Arrays.stream(fy).allMatch(c -> Math.abs(c - 10_000) < 400));
        check("naive: acb, bac, bca near 5/27 (11,111)", Math.abs(nv[1] - 11_111) < 400 && Math.abs(nv[2] - 11_111) < 400 && Math.abs(nv[3] - 11_111) < 400);
        check("naive: abc, cab, cba near 4/27 (8,889)", Math.abs(nv[0] - 8_889) < 400 && Math.abs(nv[4] - 8_889) < 400 && Math.abs(nv[5] - 8_889) < 400);
        List<Integer> perm = new ArrayList<>(List.of(1, 2, 3, 4, 5, 6, 7, 8));
        Shuffle.fisherYates(perm, new Rng(3));
        check("fisher-yates is a permutation", new HashSet<>(perm).equals(new HashSet<>(List.of(1, 2, 3, 4, 5, 6, 7, 8))));
        List<Integer> one = new ArrayList<>(List.of(42));
        Shuffle.fisherYates(one, new Rng(1));
        check("fisher-yates on one element", one.equals(List.of(42)));
        Shuffle.fisherYates(new ArrayList<Integer>(), new Rng(1));
        check("fisher-yates on empty list does nothing", true);

        // quickselect: always the right answer (Las Vegas)
        long[] data = {9, 1, 8, 2, 7, 3, 6, 4, 5, 0};
        boolean allRanks = true;
        for (int k = 0; k < data.length; k++) allRanks &= Quickselect.select(data.clone(), k, new Rng(k)) == k;
        check("quickselect: every rank of 0..9", allRanks);
        check("quickselect: duplicates", Quickselect.select(new long[] {5, 5, 5, 1, 5}, 0, new Rng(1)) == 1 && Quickselect.select(new long[] {5, 5, 5, 1, 5}, 3, new Rng(1)) == 5);
        check("quickselect: single element", Quickselect.select(new long[] {7}, 0, new Rng(1)) == 7);
        check("quickselect: already sorted and reversed", Quickselect.select(new long[] {1, 2, 3, 4, 5}, 4, new Rng(1)) == 5 && Quickselect.select(new long[] {5, 4, 3, 2, 1}, 0, new Rng(1)) == 1);
        checkThrows("quickselect rejects k out of range", IllegalArgumentException.class, () -> Quickselect.select(new long[] {1, 2}, 2, new Rng(1)));
        checkThrows("quickselect rejects empty", IllegalArgumentException.class, () -> Quickselect.select(new long[0], 0, new Rng(1)));
        long[] amounts = new long[5000];
        Rng ar = new Rng(99);
        for (int i = 0; i < amounts.length; i++) amounts[i] = ar.nextInt(1_000_000);
        long[] sorted = amounts.clone();
        Arrays.sort(sorted);
        check("quickselect median matches sort", Quickselect.median(amounts.clone(), new Rng(1)) == sorted[(sorted.length - 1) / 2]);
        check("quickselect p99 matches sort", Quickselect.percentile(amounts.clone(), 0.99, new Rng(1)) == sorted[(int) Math.ceil(0.99 * sorted.length) - 1]);
        check("quickselect p100 is the max", Quickselect.percentile(amounts.clone(), 1.0, new Rng(1)) == sorted[sorted.length - 1]);
        boolean seeds = true;
        for (int seed = 0; seed < 20; seed++) seeds &= Quickselect.select(amounts.clone(), 1234, new Rng(seed)) == sorted[1234];
        check("quickselect: same answer under 20 different seeds", seeds);

        // Miller-Rabin: Monte Carlo, with the one-sided error in the direction the lesson states
        check("powMod", MillerRabin.powMod(2, 10, 1000) == 24 && MillerRabin.powMod(3, 0, 7) == 1);
        check("primes are probable primes", MillerRabin.isProbablePrime(2, 5, new Rng(1)) && MillerRabin.isProbablePrime(7919, 5, new Rng(1)) && MillerRabin.isProbablePrime(2_147_483_647L, 5, new Rng(1)));
        check("composites are rejected", !MillerRabin.isProbablePrime(1, 5, new Rng(1)) && !MillerRabin.isProbablePrime(1_000_001, 5, new Rng(1)) && !MillerRabin.isProbablePrime(7919 * 7927L, 5, new Rng(1)));
        check("Carmichael number 561 is rejected", !MillerRabin.isProbablePrime(561, 10, new Rng(1)));
        boolean agrees = true;
        for (long n = 2; n < 5000; n++) agrees &= MillerRabin.isProbablePrime(n, 8, new Rng(n)) == MillerRabin.isPrimeByTrialDivision(n);
        check("agrees with trial division on 2..4999 with 8 rounds", agrees);
        boolean oneSided = true;
        for (long n = 2; n < 5000; n++) if (MillerRabin.isPrimeByTrialDivision(n)) oneSided &= MillerRabin.isProbablePrime(n, 1, new Rng(n));
        check("a true prime is never declared composite, even with 1 round", oneSided);
        checkThrows("rejects n >= 2^31", IllegalArgumentException.class, () -> MillerRabin.isProbablePrime(1L << 31, 1, new Rng(1)));

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }

    static List<String> sampleIds(Rng rng) {
        ReservoirSampler<Transaction> s = new ReservoirSampler<>(3, rng);
        for (Transaction t : Main.dayOfTransactions(new Rng(0), 500)) s.offer(t);
        return s.sample().stream().map(Transaction::id).toList();
    }
}
