// Lesson: phases/02-algorithms/14-randomized-algorithms/docs/en.md
// Randomized Algorithms and Reservoir Sampling
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: Vitter, "Random sampling with a reservoir" (ACM TOMS 1985), Algorithm R; Knuth, TAOCP vol. 2, 3.4.2
// (Fisher-Yates / Algorithm P); Hoare, "Algorithm 65: Find" (CACM 1961); Steele, Lea, Flood, "Fast splittable
// pseudorandom number generators" (OOPSLA 2014) for SplitMix64; Rabin, "Probabilistic algorithm for testing
// primality" (J. Number Theory 1980). Java 21, standard library only.

import java.util.ArrayList;
import java.util.Arrays;
import java.util.Collections;
import java.util.List;
import java.util.Objects;
import java.util.PriorityQueue;

/** ISO 4217 currency, copied from Phase 10, Lesson 01. */
record Currency(String code, int exponent) {
    static final Currency USD = new Currency("USD", 2);
    Currency {
        if (code == null || code.length() != 3) throw new IllegalArgumentException("currency code must be three letters: " + code);
        if (exponent < 0 || exponent > 4) throw new IllegalArgumentException("exponent must be 0..4: " + exponent);
    }
}

/** Exact amount in minor units. Subset of Phase 10, Lesson 01. */
record Money(long minorUnits, Currency currency) {
    Money { Objects.requireNonNull(currency, "currency"); }
    static Money ofMinor(long minorUnits, Currency currency) { return new Money(minorUnits, currency); }
    @Override public String toString() {
        long scale = 1;
        for (int i = 0; i < currency.exponent(); i++) scale *= 10;
        return String.format("%d.%0" + currency.exponent() + "d %s", minorUnits / scale, Math.abs(minorUnits % scale), currency.code());
    }
}

/**
 * SplitMix64: a small, fast, well-tested generator with a 64-bit state. Written out so the sequence is
 * identical in every language the course ships (java.util.Random, Python's random, and Go's math/rand all
 * use different algorithms, so seeding them identically does not give identical streams). Not for
 * cryptography: given one output the whole future is predictable. For keys and tokens use SecureRandom.
 */
final class Rng {
    private long state;

    Rng(long seed) { this.state = seed; }

    long nextLong() {
        long z = (state += 0x9E3779B97F4A7C15L);
        z = (z ^ (z >>> 30)) * 0xBF58476D1CE4E5B9L;
        z = (z ^ (z >>> 27)) * 0x94D049BB133111EBL;
        return z ^ (z >>> 31);
    }

    /**
     * Uniform integer in [0, bound). Rejection sampling, not `nextLong() % bound`: the modulo of a 64-bit
     * value is biased toward small residues whenever bound does not divide 2^64, and that bias is exactly
     * what a uniformity test on a shuffle or a sample will detect.
     */
    int nextInt(int bound) {
        if (bound <= 0) throw new IllegalArgumentException("bound must be positive: " + bound);
        long mask = Long.highestOneBit(bound) * 2 - 1;      // smallest 2^k - 1 >= bound - 1
        while (true) {
            long candidate = nextLong() & mask;
            if (candidate < bound) return (int) candidate;
        }
    }

    /** Uniform double in [0, 1) with 53 bits of randomness. */
    double nextDouble() { return (nextLong() >>> 11) * 0x1.0p-53; }
}

/**
 * Reservoir sampling, Vitter's Algorithm R: a uniform sample of k items from a stream of unknown length,
 * in O(k) memory and one pass. Invariant after n items: every item seen so far is in the reservoir with
 * probability exactly min(1, k / n).
 */
final class ReservoirSampler<T> {
    private final int k;
    private final Rng rng;
    private final List<T> reservoir = new ArrayList<>();
    private long seen = 0;

    ReservoirSampler(int k, Rng rng) {
        if (k <= 0) throw new IllegalArgumentException("k must be positive: " + k);
        this.k = k;
        this.rng = Objects.requireNonNull(rng);
    }

    void offer(T item) {
        seen++;
        if (reservoir.size() < k) {
            reservoir.add(item);
            return;
        }
        // Item number `seen` is kept with probability k / seen: pick a slot in [0, seen); keep if it is < k.
        long slot = nextLong(seen);
        if (slot < k) reservoir.set((int) slot, item);
    }

    private long nextLong(long bound) {
        if (bound <= Integer.MAX_VALUE) return rng.nextInt((int) bound);
        long mask = Long.highestOneBit(bound) * 2 - 1;
        while (true) {
            long c = rng.nextLong() & mask;
            if (c >= 0 && c < bound) return c;
        }
    }

    List<T> sample() { return Collections.unmodifiableList(reservoir); }
    long seen() { return seen; }
    int capacity() { return k; }
}

/**
 * Weighted reservoir sampling (Efraimidis and Spirakis, A-Res): each item gets key u^(1/w) for u uniform in
 * (0,1); keep the k largest keys. An item's chance of selection is proportional to its weight. Used to
 * audit transactions with probability proportional to amount, so large payments are rarely missed.
 */
final class WeightedReservoirSampler<T> {
    private record Entry<T>(double key, T item) {}

    private final int k;
    private final Rng rng;
    private final PriorityQueue<Entry<T>> heap = new PriorityQueue<>((a, b) -> Double.compare(a.key, b.key));

    WeightedReservoirSampler(int k, Rng rng) {
        if (k <= 0) throw new IllegalArgumentException("k must be positive: " + k);
        this.k = k;
        this.rng = Objects.requireNonNull(rng);
    }

    void offer(T item, long weight) {
        if (weight <= 0) throw new IllegalArgumentException("weight must be positive: " + weight);
        double u = rng.nextDouble();
        if (u == 0.0) u = Double.MIN_NORMAL;
        double key = Math.pow(u, 1.0 / weight);
        if (heap.size() < k) {
            heap.add(new Entry<>(key, item));
        } else if (key > heap.peek().key) {
            heap.poll();
            heap.add(new Entry<>(key, item));
        }
    }

    List<T> sample() {
        List<T> out = new ArrayList<>();
        for (Entry<T> e : heap) out.add(e.item);
        return out;
    }
}

/** Shuffles: the correct one and the common wrong one, kept side by side so a test can tell them apart. */
final class Shuffle {
    private Shuffle() {}

    /** Fisher-Yates (Knuth's Algorithm P): for i from n-1 down to 1, swap a[i] with a[j], j uniform in [0, i]. Every permutation equally likely. */
    static <T> void fisherYates(List<T> a, Rng rng) {
        for (int i = a.size() - 1; i > 0; i--) {
            int j = rng.nextInt(i + 1);
            Collections.swap(a, i, j);
        }
    }

    /** The tempting mistake: swap every position with a uniformly random position in the WHOLE list. n^n outcomes cannot map evenly onto n! permutations. */
    static <T> void naiveShuffle(List<T> a, Rng rng) {
        for (int i = 0; i < a.size(); i++) {
            int j = rng.nextInt(a.size());
            Collections.swap(a, i, j);
        }
    }

    /** Shuffle [a, b, c] `trials` times and count how often each of the 6 permutations (in lexicographic order) appears. */
    static int[] permutationCounts(int trials, Rng rng, boolean fisher) {
        List<String> perms = List.of("abc", "acb", "bac", "bca", "cab", "cba");
        int[] counts = new int[6];
        for (int t = 0; t < trials; t++) {
            List<Character> a = new ArrayList<>(List.of('a', 'b', 'c'));
            if (fisher) fisherYates(a, rng); else naiveShuffle(a, rng);
            String s = "" + a.get(0) + a.get(1) + a.get(2);
            counts[perms.indexOf(s)]++;
        }
        return counts;
    }
}

/**
 * Randomized quickselect (Hoare's Find with a random pivot): the k-th smallest element in expected O(n),
 * worst case O(n^2) with vanishing probability. A Las Vegas algorithm: the answer is always right, only the
 * running time is random. Reorders the array it is given; callers pass a copy if they care.
 */
final class Quickselect {
    private Quickselect() {}

    static long select(long[] a, int k, Rng rng) {
        if (a.length == 0) throw new IllegalArgumentException("empty array");
        if (k < 0 || k >= a.length) throw new IllegalArgumentException("k out of range: " + k);
        int lo = 0, hi = a.length - 1;
        while (lo < hi) {
            int pivotIndex = lo + rng.nextInt(hi - lo + 1);
            int p = partition(a, lo, hi, pivotIndex);
            if (k == p) return a[k];
            if (k < p) hi = p - 1; else lo = p + 1;
        }
        return a[lo];
    }

    /** Lomuto partition around a[pivotIndex]; returns the pivot's final position. Everything left is <=, right is >. */
    static int partition(long[] a, int lo, int hi, int pivotIndex) {
        long pivot = a[pivotIndex];
        swap(a, pivotIndex, hi);
        int store = lo;
        for (int i = lo; i < hi; i++) {
            if (a[i] < pivot) swap(a, i, store++);
        }
        swap(a, store, hi);
        return store;
    }

    private static void swap(long[] a, int i, int j) { long t = a[i]; a[i] = a[j]; a[j] = t; }

    /** Lower median: element at index (n-1)/2 of the sorted order. */
    static long median(long[] a, Rng rng) { return select(a, (a.length - 1) / 2, rng); }

    /** The value at or above which (1 - q) of the data lies, nearest-rank: index ceil(q * n) - 1. */
    static long percentile(long[] a, double q, Rng rng) {
        if (q <= 0 || q > 1) throw new IllegalArgumentException("q must be in (0, 1]: " + q);
        int idx = (int) Math.ceil(q * a.length) - 1;
        return select(a, Math.max(0, idx), rng);
    }
}

/**
 * Miller-Rabin primality test: a Monte Carlo algorithm. "Composite" is always right; "probably prime" is
 * wrong with probability at most 4^-rounds. Restricted to n < 2^31 so that a*b fits in a long without a
 * mulmod routine; BigInteger.isProbablePrime is the production version.
 */
final class MillerRabin {
    private MillerRabin() {}

    static boolean isProbablePrime(long n, int rounds, Rng rng) {
        if (n >= (1L << 31)) throw new IllegalArgumentException("n must be below 2^31: " + n);
        if (n < 2) return false;
        if (n < 4) return true;
        if (n % 2 == 0) return false;
        long d = n - 1;
        int s = 0;
        while (d % 2 == 0) { d /= 2; s++; }                    // n - 1 = 2^s * d with d odd
        for (int r = 0; r < rounds; r++) {
            long a = 2 + (long) rng.nextInt((int) (n - 3));    // witness candidate in [2, n-2]
            if (isWitness(a, d, s, n)) return false;           // a proves n composite
        }
        return true;                                           // no witness found in `rounds` tries
    }

    /** True if a is a witness to n's compositeness: a^d != 1 and a^(2^i d) != -1 for all i < s. */
    static boolean isWitness(long a, long d, int s, long n) {
        long x = powMod(a, d, n);
        if (x == 1 || x == n - 1) return false;
        for (int i = 1; i < s; i++) {
            x = (x * x) % n;
            if (x == n - 1) return false;
        }
        return true;
    }

    static long powMod(long base, long exp, long mod) {
        long result = 1;
        base %= mod;
        while (exp > 0) {
            if ((exp & 1) == 1) result = (result * base) % mod;
            base = (base * base) % mod;
            exp >>= 1;
        }
        return result;
    }

    /** Deterministic reference for small n, used by the tests as the oracle. */
    static boolean isPrimeByTrialDivision(long n) {
        if (n < 2) return false;
        for (long f = 2; f * f <= n; f++) if (n % f == 0) return false;
        return true;
    }
}

record Transaction(String id, Money amount) {}

public final class Main {
    static List<Transaction> dayOfTransactions(Rng rng, int count) {
        List<Transaction> out = new ArrayList<>(count);
        for (int i = 0; i < count; i++) {
            // most payments small, a few large: 90% in [1, 200], 10% in [200, 5000] dollars
            long dollars = rng.nextInt(10) == 0 ? 200 + rng.nextInt(4801) : 1 + rng.nextInt(200);
            long cents = rng.nextInt(100);
            out.add(new Transaction(String.format("txn-%05d", i), Money.ofMinor(dollars * 100 + cents, Currency.USD)));
        }
        return out;
    }

    public static void main(String[] args) {
        Rng rng = new Rng(2026);
        List<Transaction> day = dayOfTransactions(rng, 10_000);

        System.out.println("== Reservoir sampling: 5 of 10,000 transactions for audit, one pass, O(k) memory ==");
        ReservoirSampler<Transaction> uniform = new ReservoirSampler<>(5, new Rng(1));
        for (Transaction t : day) uniform.offer(t);
        System.out.println("seen " + uniform.seen() + ", sample: " + uniform.sample().stream().map(Transaction::id).toList());

        System.out.println();
        System.out.println("== Weighted reservoir: probability proportional to amount ==");
        WeightedReservoirSampler<Transaction> weighted = new WeightedReservoirSampler<>(5, new Rng(1));
        for (Transaction t : day) weighted.offer(t, t.amount().minorUnits());
        for (Transaction t : weighted.sample()) System.out.println("  " + t.id() + "  " + t.amount());

        System.out.println();
        System.out.println("== Fisher-Yates vs the naive shuffle: 60,000 shuffles of [a, b, c] ==");
        System.out.printf("%-6s %10s %10s%n", "perm", "fisher", "naive");
        int[] fy = Shuffle.permutationCounts(60_000, new Rng(7), true), nv = Shuffle.permutationCounts(60_000, new Rng(7), false);
        String[] perms = {"abc", "acb", "bac", "bca", "cab", "cba"};
        for (int i = 0; i < 6; i++) System.out.printf("%-6s %10d %10d%n", perms[i], fy[i], nv[i]);
        System.out.println("expected 10,000 each; naive gives acb, bac, bca 5/27 each (11,111) and the others 4/27 (8,889): 27 outcomes cannot split evenly over 6");

        System.out.println();
        System.out.println("== Quickselect: median and p99 amount without sorting 10,000 values ==");
        long[] amounts = new long[day.size()];
        for (int i = 0; i < amounts.length; i++) amounts[i] = day.get(i).amount().minorUnits();
        long median = Quickselect.median(amounts.clone(), new Rng(3));
        long p99 = Quickselect.percentile(amounts.clone(), 0.99, new Rng(3));
        long[] sorted = amounts.clone();
        Arrays.sort(sorted);
        System.out.println("median " + Money.ofMinor(median, Currency.USD) + " (sorted says " + Money.ofMinor(sorted[(sorted.length - 1) / 2], Currency.USD) + ")");
        System.out.println("p99    " + Money.ofMinor(p99, Currency.USD) + " (sorted says " + Money.ofMinor(sorted[(int) Math.ceil(0.99 * sorted.length) - 1], Currency.USD) + ")");

        System.out.println();
        System.out.println("== Miller-Rabin: Monte Carlo primality, 561 is a Carmichael number that fools Fermat ==");
        for (long n : new long[] {561, 1_000_003, 2_147_483_647L, 1_000_001, 7919 * 7927L}) {
            System.out.printf("%12d  probable prime after 10 rounds: %-5s  trial division: %s%n", n,
                    MillerRabin.isProbablePrime(n, 10, new Rng(5)), MillerRabin.isPrimeByTrialDivision(n));
        }
    }
}
