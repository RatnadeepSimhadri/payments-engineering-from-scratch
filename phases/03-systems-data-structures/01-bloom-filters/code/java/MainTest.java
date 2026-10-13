// Tests for phases/03-systems-data-structures/01-bloom-filters. Run: javac -d .out *.java && java -cp .out MainTest
import java.nio.charset.StandardCharsets;
import java.util.ArrayList;
import java.util.Arrays;
import java.util.HashSet;
import java.util.List;
import java.util.Set;

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

    static byte[] utf8(String s) { return s.getBytes(StandardCharsets.UTF_8); }

    public static void main(String[] args) {
        // hashing: known vectors and cross-language agreement
        check("fnv1a64 empty", Hashing.fnv1a64(utf8("")) == 0xcbf29ce484222325L);
        check("fnv1a64 'a'", Hashing.fnv1a64(utf8("a")) == 0xaf63dc4c8601ec8cL);
        check("fnv1a64 'foobar'", Hashing.fnv1a64(utf8("foobar")) == 0x85944171f73967e8L);
        check("mix64 fixes zero", Hashing.mix64(0) == 0);
        check("mix64(1)", Hashing.mix64(1) == 0xb456bcfc34c2cb2cL);
        Set<Long> mixed = new HashSet<>();
        for (long i = 0; i < 10_000; i++) mixed.add(Hashing.mix64(i));
        check("mix64 is injective on a sample", mixed.size() == 10_000);
        BloomFilter small = new BloomFilter(1000, 3);
        check("indexes match Python and Go (idem-key-1)", Arrays.equals(small.indexes(utf8("idem-key-1")), new long[] {994, 570, 146}));
        check("indexes match Python and Go (card-4111)", Arrays.equals(small.indexes(utf8("card-4111")), new long[] {343, 535, 727}));
        BloomFilter odd = new BloomFilter(97, 5);
        boolean inRange = true;
        for (int i = 0; i < 500; i++) {
            long[] idx = odd.indexes(utf8("key-" + i));
            if (idx.length != 5) inRange = false;
            for (long x : idx) if (x < 0 || x >= 97) inRange = false;
        }
        check("indexes are k long and within [0, m)", inRange);

        // sizing formulas
        check("optimalBits(10000, 0.01) = 95851", BloomFilter.optimalBits(10_000, 0.01) == 95_851);
        check("optimalHashes(95851, 10000) = 7", BloomFilter.optimalHashes(95_851, 10_000) == 7);
        check("optimalBits(1000, 0.001) = 14378", BloomFilter.optimalBits(1_000, 0.001) == 14_378);
        check("optimalHashes(14378, 1000) = 10", BloomFilter.optimalHashes(14_378, 1_000) == 10);
        check("expected rate formula", Math.abs(BloomFilter.expectedFalsePositiveRate(95_851, 7, 10_000) - 0.01004) < 0.00005);
        check("expected rate with zero items is 0", BloomFilter.expectedFalsePositiveRate(100, 3, 0) == 0.0);
        BloomFilter created = BloomFilter.create(10_000, 0.01);
        check("create uses the formulas", created.m == 95_851 && created.k == 7);
        checkThrows("rejects zero bits", IllegalArgumentException.class, () -> new BloomFilter(0, 3));
        checkThrows("rejects zero hashes", IllegalArgumentException.class, () -> new BloomFilter(100, 0));
        checkThrows("rejects p outside (0,1)", IllegalArgumentException.class, () -> BloomFilter.optimalBits(100, 1.5));

        // membership
        BloomFilter empty = new BloomFilter(1024, 3);
        check("empty filter contains nothing", !empty.mightContain("anything") && empty.bitCount() == 0);
        BloomFilter noFn = BloomFilter.create(1_000, 0.01);
        for (int i = 0; i < 1_000; i++) noFn.add("pay_" + i);
        boolean allPresent = true;
        for (int i = 0; i < 1_000; i++) if (!noFn.mightContain("pay_" + i)) allPresent = false;
        check("no false negatives", allPresent);
        BloomFilter changes = new BloomFilter(4096, 4);
        check("first add changes bits", changes.add("pay_7f3a"));
        check("second add of the same key changes nothing", !changes.add("pay_7f3a"));
        check("insertions counts every add", changes.insertions == 2);
        check("k distinct bits set for one key", changes.bitCount() == 4);
        check("String and byte[] keys agree", changes.mightContain(utf8("pay_7f3a")));

        // measured false-positive rate against the formula
        BloomFilter probe = BloomFilter.create(10_000, 0.01);
        double measured = Main.measureFalsePositiveRate(probe, 10_000, 20_000);
        double predicted = probe.estimatedFalsePositiveRate();
        check("measured p within 50% of formula", measured > 0.5 * predicted && measured < 1.5 * predicted);
        check("fill ratio matches 1 - e^(-kn/m)", Math.abs(probe.fillRatio() - (1 - Math.exp(-7.0 * 10_000 / 95_851))) < 0.01);
        check("approximateCount within 3%", Math.abs(probe.approximateCount() - 10_000) < 300);
        check("approximateCount of empty filter is 0", new BloomFilter(64, 2).approximateCount() == 0);

        // the failure the lesson warns about: overfilling
        BloomFilter over = new BloomFilter(95_851, 7);
        for (int i = 0; i < 100_000; i++) over.add("k-" + i);
        check("10x overfill: predicted p above 0.9", over.estimatedFalsePositiveRate() > 0.9);
        check("10x overfill: nearly every bit set", over.fillRatio() > 0.99);

        // union
        BloomFilter a = new BloomFilter(4096, 4), b = new BloomFilter(4096, 4);
        a.add("shard-a-key");
        b.add("shard-b-key");
        BloomFilter both = a.union(b);
        check("union answers for both sets", both.mightContain("shard-a-key") && both.mightContain("shard-b-key"));
        check("union still rejects an absent key", !both.mightContain("shard-c-key"));
        check("union sums insertions", both.insertions == 2);
        checkThrows("union rejects different m", IllegalArgumentException.class, () -> a.union(new BloomFilter(2048, 4)));
        checkThrows("union rejects different k", IllegalArgumentException.class, () -> a.union(new BloomFilter(4096, 3)));

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }
}
