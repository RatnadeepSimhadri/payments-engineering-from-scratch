// Lesson: phases/03-systems-data-structures/01-bloom-filters/docs/en.md
// Bloom Filters from Scratch
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: Bloom (1970), CACM 13(7); Kirsch & Mitzenmacher (2006), "Less Hashing, Same Performance";
//          FNV-1a (Fowler/Noll/Vo, IETF draft-eastlake-fnv); Swamidass & Baldi (2007) for the cardinality estimate.
// Java 21, standard library only.

import java.nio.charset.StandardCharsets;

/** The two 64-bit hash functions the filter is built on. Pure functions over bytes. */
final class Hashing {
    static final long FNV_OFFSET = 0xcbf29ce484222325L;
    static final long FNV_PRIME = 0x100000001b3L;

    private Hashing() {}

    /** FNV-1a, 64-bit: xor the byte in, then multiply by the prime. The long wraps, which is the intent. */
    static long fnv1a64(byte[] data) {
        long h = FNV_OFFSET;
        for (byte b : data) {
            h ^= (b & 0xff);
            h *= FNV_PRIME;
        }
        return h;
    }

    /**
     * MurmurHash3's fmix64 finalizer: a bijection on 64-bit words that spreads every input bit to
     * every output bit. Used to derive the second hash from the first (double hashing).
     */
    static long mix64(long h) {
        h ^= h >>> 33;
        h *= 0xff51afd7ed558ccdL;
        h ^= h >>> 33;
        h *= 0xc4ceb9fe1a85ec53L;
        h ^= h >>> 33;
        return h;
    }
}

/**
 * A fixed-size bit array with k hash functions. Answers "definitely not present" or
 * "possibly present"; never "definitely present". No deletes.
 */
final class BloomFilter {
    private final long[] words;
    final long m;          // number of bits
    final int k;           // number of hash functions
    long insertions;       // how many add() calls so far

    BloomFilter(long bits, int hashes) {
        if (bits <= 0) throw new IllegalArgumentException("bits must be positive: " + bits);
        if (hashes <= 0) throw new IllegalArgumentException("hashes must be positive: " + hashes);
        this.m = bits;
        this.k = hashes;
        this.words = new long[(int) ((bits + 63) / 64)];
    }

    // ----- sizing -------------------------------------------------------------------------

    /** m = -n ln p / (ln 2)^2, rounded up. */
    static long optimalBits(long expectedItems, double falsePositiveRate) {
        if (expectedItems <= 0) throw new IllegalArgumentException("expectedItems must be positive");
        if (!(falsePositiveRate > 0.0 && falsePositiveRate < 1.0)) {
            throw new IllegalArgumentException("falsePositiveRate must be in (0, 1)");
        }
        double ln2 = Math.log(2);
        return (long) Math.ceil(-expectedItems * Math.log(falsePositiveRate) / (ln2 * ln2));
    }

    /** k = (m / n) ln 2, rounded to the nearest integer, at least 1. */
    static int optimalHashes(long bits, long expectedItems) {
        return Math.max(1, (int) Math.round((double) bits / expectedItems * Math.log(2)));
    }

    /** p = (1 - e^(-kn/m))^k: the classic approximation, exact enough for sizing. */
    static double expectedFalsePositiveRate(long bits, int hashes, long items) {
        if (items == 0) return 0.0;
        return Math.pow(1.0 - Math.exp(-(double) hashes * items / bits), hashes);
    }

    static BloomFilter create(long expectedItems, double falsePositiveRate) {
        long bits = optimalBits(expectedItems, falsePositiveRate);
        return new BloomFilter(bits, optimalHashes(bits, expectedItems));
    }

    // ----- hashing -----------------------------------------------------------------------

    /**
     * The k bit positions for a key: index_i = (h1 + i * h2) mod m, Kirsch-Mitzenmacher double
     * hashing. Two 64-bit hashes stand in for k independent ones. Unsigned remainder, so the
     * wrapped long is treated as the 64-bit value it is.
     */
    long[] indexes(byte[] key) {
        long h1 = Hashing.fnv1a64(key);
        long h2 = Hashing.mix64(h1);
        long[] out = new long[k];
        for (int i = 0; i < k; i++) {
            out[i] = Long.remainderUnsigned(h1 + i * h2, m);
        }
        return out;
    }

    // ----- membership ----------------------------------------------------------------------

    /**
     * Set the k bits. Returns true if any bit changed (the key was definitely new), false if every
     * bit was already set (the key is possibly a duplicate).
     */
    boolean add(byte[] key) {
        boolean changed = false;
        for (long idx : indexes(key)) {
            int word = (int) (idx >>> 6);
            long mask = 1L << (idx & 63);
            if ((words[word] & mask) == 0) {
                words[word] |= mask;
                changed = true;
            }
        }
        insertions++;
        return changed;
    }

    boolean add(String key) { return add(key.getBytes(StandardCharsets.UTF_8)); }

    /** false means definitely never added. true means possibly added. */
    boolean mightContain(byte[] key) {
        for (long idx : indexes(key)) {
            if ((words[(int) (idx >>> 6)] & (1L << (idx & 63))) == 0) return false;
        }
        return true;
    }

    boolean mightContain(String key) { return mightContain(key.getBytes(StandardCharsets.UTF_8)); }

    // ----- introspection -------------------------------------------------------------------

    /** Number of set bits (the filter's fill). */
    long bitCount() {
        long total = 0;
        for (long w : words) total += Long.bitCount(w);
        return total;
    }

    double fillRatio() { return (double) bitCount() / m; }

    /** The formula applied to this filter's own insertion count. */
    double estimatedFalsePositiveRate() { return expectedFalsePositiveRate(m, k, insertions); }

    /**
     * Swamidass & Baldi: n* = -(m / k) ln(1 - X / m) where X is the number of set bits.
     * Lets a filter rebuilt from a snapshot report how full it is without a counter.
     */
    long approximateCount() {
        long x = bitCount();
        if (x >= m) return m; // saturated; the estimate is unbounded
        return Math.round(-((double) m / k) * Math.log(1.0 - (double) x / m));
    }

    /** OR two filters built with identical m and k. The result answers for both key sets. */
    BloomFilter union(BloomFilter other) {
        if (m != other.m || k != other.k) {
            throw new IllegalArgumentException("cannot union filters with different parameters: ("
                    + m + "," + k + ") vs (" + other.m + "," + other.k + ")");
        }
        BloomFilter result = new BloomFilter(m, k);
        for (int i = 0; i < words.length; i++) result.words[i] = words[i] | other.words[i];
        result.insertions = insertions + other.insertions;
        return result;
    }

    double bitsPerItem(long items) { return (double) m / items; }

    @Override
    public String toString() {
        return "BloomFilter(m=" + m + ", k=" + k + ", insertions=" + insertions + ", set_bits=" + bitCount() + ")";
    }
}

public final class Main {

    /**
     * Insert `inserted` synthetic keys, then probe `probes` keys that were never inserted.
     * Deterministic: keys are "k-<i>" and "q-<i>", so every language measures the same number.
     */
    static double measureFalsePositiveRate(BloomFilter bf, int inserted, int probes) {
        for (int i = 0; i < inserted; i++) bf.add("k-" + i);
        int positives = 0;
        for (int i = 0; i < probes; i++) if (bf.mightContain("q-" + i)) positives++;
        return (double) positives / probes;
    }

    public static void main(String[] args) {
        System.out.println("== Sizing a filter for 10,000 idempotency keys at 1% false positives ==");
        int n = 10_000;
        double p = 0.01;
        long m = BloomFilter.optimalBits(n, p);
        int k = BloomFilter.optimalHashes(m, n);
        System.out.printf("m = %d bits (%.1f KiB), k = %d hashes, %.2f bits per key%n", m, m / 8.0 / 1024, k, (double) m / n);
        System.out.printf("formula predicts p = %.5f%n", BloomFilter.expectedFalsePositiveRate(m, k, n));

        System.out.println("\n== Where a key lands ==");
        BloomFilter small = new BloomFilter(1000, 3);
        byte[] key = "idem-key-1".getBytes(StandardCharsets.UTF_8);
        System.out.printf("fnv1a64(\"idem-key-1\") = 0x%016x%n", Hashing.fnv1a64(key));
        System.out.printf("mix64(h1)             = 0x%016x%n", Hashing.mix64(Hashing.fnv1a64(key)));
        System.out.println("indexes in m=1000, k=3: " + java.util.Arrays.toString(small.indexes(key)));

        System.out.println("\n== Definitely not seen vs possibly seen ==");
        BloomFilter idem = BloomFilter.create(n, p);
        for (String req : new String[] {"pay_7f3a", "pay_7f3b", "pay_7f3c"}) {
            System.out.printf("add(\"%s\") -> changed bits: %b%n", req, idem.add(req));
        }
        for (String req : new String[] {"pay_7f3a", "pay_9999"}) {
            boolean maybe = idem.mightContain(req);
            System.out.printf("mightContain(\"%s\") = %b: %s%n", req, maybe,
                    maybe ? "possibly seen -> check the database" : "definitely new -> skip the lookup");
        }
        System.out.printf("re-adding \"pay_7f3a\" changes bits: %b%n", idem.add("pay_7f3a"));

        System.out.println("\n== Measured false-positive rate against the formula ==");
        BloomFilter probe = BloomFilter.create(n, p);
        double measured = measureFalsePositiveRate(probe, n, 50_000);
        System.out.println(probe);
        System.out.printf("fill ratio = %.4f (theory: 1 - e^(-kn/m) = %.4f)%n", probe.fillRatio(), 1 - Math.exp(-(double) k * n / m));
        System.out.printf("measured p = %.5f, formula p = %.5f%n", measured, probe.estimatedFalsePositiveRate());
        System.out.printf("approximateCount = %d (actual %d)%n", probe.approximateCount(), n);

        System.out.println("\n== What happens when you overfill it ==");
        for (int factor : new int[] {1, 2, 5, 10}) {
            BloomFilter over = new BloomFilter(m, k);
            for (int i = 0; i < n * factor; i++) over.add("k-" + i);
            System.out.printf("%2dx capacity: fill = %.3f, predicted p = %.4f%n", factor, over.fillRatio(), over.estimatedFalsePositiveRate());
        }

        System.out.println("\n== Union of two shards' filters ==");
        BloomFilter a = new BloomFilter(4096, 4), b = new BloomFilter(4096, 4);
        a.add("shard-a-key");
        b.add("shard-b-key");
        BloomFilter both = a.union(b);
        System.out.printf("union mightContain shard-a-key: %b, shard-b-key: %b%n", both.mightContain("shard-a-key"), both.mightContain("shard-b-key"));
        try {
            a.union(new BloomFilter(2048, 4));
        } catch (IllegalArgumentException exc) {
            System.out.println("rejected: " + exc.getMessage());
        }
    }
}
