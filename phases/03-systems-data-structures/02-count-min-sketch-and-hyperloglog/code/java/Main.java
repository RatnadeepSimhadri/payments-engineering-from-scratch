// Lesson: phases/03-systems-data-structures/02-count-min-sketch-and-hyperloglog/docs/en.md
// Count-Min Sketch and HyperLogLog: frequency and cardinality in constant memory
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: Cormode & Muthukrishnan (2005), "An Improved Data Stream Summary: The Count-Min Sketch";
//          Flajolet, Fusy, Gandouet, Meunier (2007), "HyperLogLog"; Heule, Nunkesser, Hall (2013), "HyperLogLog in Practice".
// Java 21, standard library only.

import java.nio.charset.StandardCharsets;
import java.util.Arrays;
import java.util.HashMap;
import java.util.HashSet;
import java.util.Map;
import java.util.Random;
import java.util.Set;

/** The two 64-bit hash functions both sketches are built on. Pure functions over bytes. */
final class Hashing {
    private static final long FNV_OFFSET = 0xcbf29ce484222325L;
    private static final long FNV_PRIME = 0x100000001b3L;

    private Hashing() {}

    /** FNV-1a over the UTF-8 bytes of the key. Fast, simple, and identical in every language. */
    static long fnv1a64(String key) {
        long h = FNV_OFFSET;
        for (byte b : key.getBytes(StandardCharsets.UTF_8)) {
            h ^= (b & 0xff);
            h *= FNV_PRIME;
        }
        return h;
    }

    /** MurmurHash3's fmix64 finalizer: a bijection that spreads every input bit to every output bit. */
    static long mix64(long h) {
        h ^= h >>> 33;
        h *= 0xff51afd7ed558ccdL;
        h ^= h >>> 33;
        h *= 0xc4ceb9fe1a85ec53L;
        h ^= h >>> 33;
        return h;
    }

    /** A well-mixed 64-bit hash: FNV-1a alone has weak low bits for short keys, so finalize it. */
    static long hash64(String key) {
        return mix64(fnv1a64(key));
    }
}

/**
 * Count-Min Sketch: depth rows of width counters. Every key maps to one counter per row; the
 * estimate is the minimum over its counters. Never underestimates; overestimates by at most
 * epsilon * N with probability at least 1 - delta, where N is the total count added.
 */
final class CountMinSketch {
    final int width;   // counters per row; controls epsilon = e / width
    final int depth;   // rows; controls delta = e^(-depth)
    private final long[][] table;
    private long total;

    CountMinSketch(int width, int depth) {
        if (width <= 0) throw new IllegalArgumentException("width must be positive: " + width);
        if (depth <= 0) throw new IllegalArgumentException("depth must be positive: " + depth);
        this.width = width;
        this.depth = depth;
        this.table = new long[depth][width];
    }

    /** Size a sketch so that estimate <= true + epsilon * N with probability >= 1 - delta. */
    static CountMinSketch forErrorBounds(double epsilon, double delta) {
        if (!(epsilon > 0 && epsilon < 1)) throw new IllegalArgumentException("epsilon must be in (0,1): " + epsilon);
        if (!(delta > 0 && delta < 1)) throw new IllegalArgumentException("delta must be in (0,1): " + delta);
        int width = (int) Math.ceil(Math.E / epsilon);
        int depth = (int) Math.ceil(Math.log(1.0 / delta));
        return new CountMinSketch(width, depth);
    }

    /** Column for key in row i, by double hashing: h1 + i * h2 (Kirsch and Mitzenmacher). */
    int column(long h1, long h2, int row) {
        long combined = h1 + (long) row * h2;
        return (int) Long.remainderUnsigned(combined, width);
    }

    void add(String key, long count) {
        if (count < 0) throw new IllegalArgumentException("count must be non-negative: " + count);
        long h1 = Hashing.hash64(key);
        long h2 = Hashing.mix64(h1);
        for (int row = 0; row < depth; row++) {
            table[row][column(h1, h2, row)] += count;
        }
        total += count;
    }

    void add(String key) { add(key, 1); }

    /** The minimum of the key's counters. Each counter is true count plus collisions, so min is the tightest. */
    long estimate(String key) {
        long h1 = Hashing.hash64(key);
        long h2 = Hashing.mix64(h1);
        long best = Long.MAX_VALUE;
        for (int row = 0; row < depth; row++) {
            best = Math.min(best, table[row][column(h1, h2, row)]);
        }
        return best;
    }

    long total() { return total; }

    /** Bound on overestimate per the paper: epsilon * N, with epsilon = e / width. */
    double errorBound() { return Math.E / width * total; }

    /** Failure probability per the paper: e^(-depth). */
    double failureProbability() { return Math.exp(-depth); }

    /** Counter-wise sum. Valid only for sketches with identical width and depth (same hash layout). */
    CountMinSketch merge(CountMinSketch other) {
        if (other.width != width || other.depth != depth) {
            throw new IllegalArgumentException("cannot merge sketches of different shape");
        }
        CountMinSketch out = new CountMinSketch(width, depth);
        for (int r = 0; r < depth; r++) {
            for (int c = 0; c < width; c++) out.table[r][c] = table[r][c] + other.table[r][c];
        }
        out.total = total + other.total;
        return out;
    }

    long bytes() { return (long) width * depth * Long.BYTES; }
}

/**
 * HyperLogLog: m = 2^precision registers, each holding the longest run of leading zeros seen in
 * the hash bits that follow the register index. Cardinality is a bias-corrected harmonic mean.
 * Standard error is about 1.04 / sqrt(m).
 */
final class HyperLogLog {
    final int precision;      // p in the paper; 4..16
    final int registers;      // m = 2^p
    private final byte[] register;

    HyperLogLog(int precision) {
        if (precision < 4 || precision > 16) throw new IllegalArgumentException("precision must be 4..16: " + precision);
        this.precision = precision;
        this.registers = 1 << precision;
        this.register = new byte[registers];
    }

    /** Position of the first 1-bit in the low 64-p bits, counted from 1. Zero bits give the maximum 65-p. */
    static int rho(long hash, int precision) {
        long rest = hash << precision;                 // discard the index bits; the rest now leads
        if (rest == 0) return 64 - precision + 1;      // all remaining bits were zero
        return Long.numberOfLeadingZeros(rest) + 1;
    }

    void add(String key) {
        long h = Hashing.hash64(key);
        int index = (int) (h >>> (64 - precision));     // top p bits pick the register
        int r = rho(h, precision);
        if (r > register[index]) register[index] = (byte) r;
    }

    /** alpha_m from the paper: the constant that corrects the harmonic mean's multiplicative bias. */
    static double alpha(int m) {
        return switch (m) {
            case 16 -> 0.673;
            case 32 -> 0.697;
            case 64 -> 0.709;
            default -> 0.7213 / (1.0 + 1.079 / m);
        };
    }

    /** Raw estimate: alpha_m * m^2 / sum(2^-M[j]). The harmonic mean tames outlier registers. */
    double rawEstimate() {
        double sum = 0.0;
        for (byte r : register) sum += Math.pow(2.0, -r);
        return alpha(registers) * registers * (double) registers / sum;
    }

    int zeroRegisters() {
        int zeros = 0;
        for (byte r : register) if (r == 0) zeros++;
        return zeros;
    }

    /**
     * Bias-corrected cardinality. For small sets the raw estimate is biased high, so when
     * E <= 2.5m and some registers are still empty, fall back to linear counting over the
     * number of empty registers: m * ln(m / V). With a 64-bit hash no large-range correction is needed.
     */
    long cardinality() {
        double estimate = rawEstimate();
        if (estimate <= 2.5 * registers) {
            int zeros = zeroRegisters();
            if (zeros > 0) estimate = registers * Math.log((double) registers / zeros);
        }
        return Math.round(estimate);
    }

    /** Relative standard error from the paper: 1.04 / sqrt(m). */
    double standardError() { return 1.04 / Math.sqrt(registers); }

    /** Register-wise maximum. The merged sketch is exactly the sketch of the union. */
    HyperLogLog merge(HyperLogLog other) {
        if (other.precision != precision) throw new IllegalArgumentException("cannot merge different precisions");
        HyperLogLog out = new HyperLogLog(precision);
        for (int i = 0; i < registers; i++) out.register[i] = (byte) Math.max(register[i], other.register[i]);
        return out;
    }

    byte registerAt(int i) { return register[i]; }

    long bytes() { return registers; }
}

public final class Main {

    static String cardToken(Random rng) {
        // A tokenized PAN stands in for the card: 16 digits, never a real number.
        return "tok_" + String.format("%016d", Math.abs(rng.nextLong()) % 10_000_000_000_000_000L);
    }

    public static void main(String[] args) {
        Random rng = new Random(42);

        System.out.println("== Count-Min Sketch: authorizations per merchant, 11 KB instead of a hash map ==");
        CountMinSketch cms = CountMinSketch.forErrorBounds(0.01, 0.01);
        System.out.printf("width=%d depth=%d bytes=%d (epsilon=0.01, delta=0.01)%n", cms.width, cms.depth, cms.bytes());

        Map<String, Long> exact = new HashMap<>();
        String[] merchants = new String[1_000];
        for (int i = 0; i < merchants.length; i++) merchants[i] = "merchant_" + i;
        for (int i = 0; i < 200_000; i++) {
            // skewed traffic: a few merchants dominate, which is where the min-of-rows matters
            int m = (int) Math.min(merchants.length - 1, Math.floor(Math.abs(rng.nextGaussian()) * 120));
            cms.add(merchants[m]);
            exact.merge(merchants[m], 1L, Long::sum);
        }
        System.out.printf("total events = %d, error bound = %.0f%n", cms.total(), cms.errorBound());
        for (String m : new String[]{"merchant_0", "merchant_5", "merchant_50", "merchant_300", "merchant_999"}) {
            long truth = exact.getOrDefault(m, 0L);
            System.out.printf("%-14s true=%6d  estimate=%6d  over by %d%n", m, truth, cms.estimate(m), cms.estimate(m) - truth);
        }

        System.out.println();
        System.out.println("== HyperLogLog: distinct cards per merchant, 4 KB instead of a set ==");
        HyperLogLog hll = new HyperLogLog(12);
        Set<String> distinct = new HashSet<>();
        String[] cards = new String[50_000];
        for (int i = 0; i < cards.length; i++) cards[i] = cardToken(rng);
        for (int i = 0; i < 300_000; i++) {
            String card = cards[rng.nextInt(cards.length)];
            hll.add(card);
            distinct.add(card);
        }
        System.out.printf("precision=%d registers=%d bytes=%d standard error=%.2f%%%n",
                hll.precision, hll.registers, hll.bytes(), hll.standardError() * 100);
        System.out.printf("300000 events, true distinct = %d, HLL estimate = %d (%.2f%% off)%n",
                distinct.size(), hll.cardinality(), 100.0 * (hll.cardinality() - distinct.size()) / distinct.size());

        System.out.println();
        System.out.println("== Small sets: linear counting takes over ==");
        HyperLogLog small = new HyperLogLog(12);
        for (int i = 1; i <= 100; i++) small.add("card_" + i);
        System.out.printf("100 distinct cards -> raw %.1f, corrected %d%n", small.rawEstimate(), small.cardinality());

        System.out.println();
        System.out.println("== Merging two merchants' sketches is the union, not the sum ==");
        HyperLogLog a = new HyperLogLog(12), b = new HyperLogLog(12);
        for (int i = 0; i < 20_000; i++) a.add(cards[i]);
        for (int i = 10_000; i < 30_000; i++) b.add(cards[i]);
        System.out.printf("a=%d b=%d merged=%d (true union 30000, sum would say %d)%n",
                a.cardinality(), b.cardinality(), a.merge(b).cardinality(), a.cardinality() + b.cardinality());

        System.out.println();
        System.out.println("== Memory: exact vs sketch for 1,000 merchants x 50,000 cards ==");
        long exactBytes = 1_000L * 50_000L * 40;   // ~40 bytes per set entry is optimistic
        System.out.printf("exact sets  ~ %,d bytes%n", exactBytes);
        System.out.printf("1000 HLLs    = %,d bytes%n", 1_000L * hll.bytes());
        System.out.println(Arrays.toString(new int[]{HyperLogLog.rho(0x0008000000000000L, 12), HyperLogLog.rho(0x0000100000000000L, 12), HyperLogLog.rho(0L, 12)})
                + " <- rho when the first rest-bit is 1, when it is the 8th, and when all 52 are zero");
    }
}
