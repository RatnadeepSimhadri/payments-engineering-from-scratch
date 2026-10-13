// Tests for phases/03-systems-data-structures/02-count-min-sketch-and-hyperloglog. Run: javac -d .out *.java && java -cp .out MainTest
import java.util.ArrayList;
import java.util.HashMap;
import java.util.HashSet;
import java.util.List;
import java.util.Map;
import java.util.Random;
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

    public static void main(String[] args) {
        Random rng = new Random(7);

        // ----- hashing -----
        check("fnv1a64 is deterministic", Hashing.fnv1a64("tok_1") == Hashing.fnv1a64("tok_1"));
        check("hash64 separates neighbours", Hashing.hash64("tok_1") != Hashing.hash64("tok_2"));
        check("mix64 is a bijection on zero", Hashing.mix64(0L) == 0L);

        // ----- Count-Min Sketch: sizing -----
        CountMinSketch sized = CountMinSketch.forErrorBounds(0.01, 0.01);
        check("width = ceil(e / epsilon) = 272", sized.width == 272);
        check("depth = ceil(ln(1/delta)) = 5", sized.depth == 5);
        check("bytes = width * depth * 8", sized.bytes() == 272L * 5 * 8);
        checkThrows("epsilon must be in (0,1)", IllegalArgumentException.class, () -> CountMinSketch.forErrorBounds(1.5, 0.1));
        checkThrows("delta must be in (0,1)", IllegalArgumentException.class, () -> CountMinSketch.forErrorBounds(0.1, 0.0));
        checkThrows("width must be positive", IllegalArgumentException.class, () -> new CountMinSketch(0, 3));

        // ----- Count-Min Sketch: exactness on small inputs, never-underestimate on large -----
        CountMinSketch cms = new CountMinSketch(1024, 4);
        cms.add("m1", 5);
        cms.add("m2", 3);
        cms.add("m1");
        check("estimate counts increments", cms.estimate("m1") == 6);
        check("estimate second key", cms.estimate("m2") == 3);
        check("total tracks every add", cms.total() == 9);
        check("unseen key with no collisions is zero", cms.estimate("never") == 0);
        checkThrows("negative counts rejected", IllegalArgumentException.class, () -> cms.add("m1", -1));

        CountMinSketch busy = CountMinSketch.forErrorBounds(0.005, 0.001);
        Map<String, Long> truth = new HashMap<>();
        for (int i = 0; i < 100_000; i++) {
            String key = "merchant_" + (int) (Math.abs(rng.nextGaussian()) * 300);
            busy.add(key);
            truth.merge(key, 1L, Long::sum);
        }
        boolean neverUnder = true, withinBound = true;
        int violations = 0;
        for (Map.Entry<String, Long> e : truth.entrySet()) {
            long est = busy.estimate(e.getKey());
            if (est < e.getValue()) neverUnder = false;
            if (est > e.getValue() + busy.errorBound()) violations++;
        }
        check("count-min never underestimates (invariant)", neverUnder);
        check("overestimate within epsilon*N for (almost) every key", violations <= Math.max(1, (int) (truth.size() * busy.failureProbability() * 10)));
        check("errorBound is e/width * N", Math.abs(busy.errorBound() - Math.E / busy.width * 100_000) < 1e-6);
        check("failure probability is e^-depth", Math.abs(busy.failureProbability() - Math.exp(-busy.depth)) < 1e-12);

        // ----- Count-Min Sketch: merge -----
        CountMinSketch left = new CountMinSketch(256, 4), right = new CountMinSketch(256, 4), both = new CountMinSketch(256, 4);
        for (int i = 0; i < 2_000; i++) {
            String k = "k" + rng.nextInt(50);
            left.add(k); both.add(k);
            String j = "k" + rng.nextInt(50);
            right.add(j); both.add(j);
        }
        CountMinSketch merged = left.merge(right);
        boolean sameAsBoth = merged.total() == both.total();
        for (int i = 0; i < 50; i++) sameAsBoth &= merged.estimate("k" + i) == both.estimate("k" + i);
        check("merge equals sketch of the concatenated stream", sameAsBoth);
        checkThrows("merge rejects different shapes", IllegalArgumentException.class, () -> left.merge(new CountMinSketch(128, 4)));

        // ----- HyperLogLog: rho and registers -----
        check("rho: first rest bit set -> 1", HyperLogLog.rho(0x0008000000000000L, 12) == 1);
        check("rho: eighth rest bit set -> 8", HyperLogLog.rho(0x0000100000000000L, 12) == 8);
        check("rho: all rest bits zero -> 65 - p", HyperLogLog.rho(0L, 12) == 53);
        check("rho ignores the index bits", HyperLogLog.rho(0xFFF0000000000000L, 12) == 53);
        checkThrows("precision below 4 rejected", IllegalArgumentException.class, () -> new HyperLogLog(3));
        checkThrows("precision above 16 rejected", IllegalArgumentException.class, () -> new HyperLogLog(17));
        check("alpha for m=16 is 0.673", HyperLogLog.alpha(16) == 0.673);
        check("alpha for large m approaches 0.7213", Math.abs(HyperLogLog.alpha(4096) - 0.7213 / (1 + 1.079 / 4096)) < 1e-12);

        // ----- HyperLogLog: cardinality -----
        HyperLogLog empty = new HyperLogLog(10);
        check("empty sketch has cardinality 0", empty.cardinality() == 0);
        HyperLogLog one = new HyperLogLog(10);
        one.add("tok_a");
        one.add("tok_a");
        one.add("tok_a");
        check("repeats of one key count once", one.cardinality() == 1);
        HyperLogLog small = new HyperLogLog(12);
        for (int i = 0; i < 100; i++) small.add("card_" + i);
        check("linear counting is near-exact on 100 items", Math.abs(small.cardinality() - 100) <= 2);

        HyperLogLog hll = new HyperLogLog(12);
        Set<String> distinct = new HashSet<>();
        for (int i = 0; i < 200_000; i++) {
            String card = "tok_" + rng.nextInt(60_000);
            hll.add(card);
            distinct.add(card);
        }
        double relErr = Math.abs((double) hll.cardinality() - distinct.size()) / distinct.size();
        check("standard error is 1.04/sqrt(m) = 1.625% at p=12", Math.abs(hll.standardError() - 0.01625) < 1e-9);
        check("estimate within 3 standard errors of the truth", relErr < 3 * hll.standardError());
        check("sketch uses exactly m bytes", hll.bytes() == 4096);

        // ----- HyperLogLog: merge is union -----
        HyperLogLog a = new HyperLogLog(12), b = new HyperLogLog(12), union = new HyperLogLog(12);
        for (int i = 0; i < 20_000; i++) { a.add("c" + i); union.add("c" + i); }
        for (int i = 10_000; i < 30_000; i++) { b.add("c" + i); union.add("c" + i); }
        HyperLogLog ab = a.merge(b);
        boolean registersMatch = true;
        for (int i = 0; i < ab.registers; i++) registersMatch &= ab.registerAt(i) == union.registerAt(i);
        check("merged registers equal the union's registers exactly", registersMatch);
        check("merged cardinality equals the union's", ab.cardinality() == union.cardinality());
        check("merge is commutative", b.merge(a).cardinality() == ab.cardinality());
        checkThrows("merge rejects different precision", IllegalArgumentException.class, () -> a.merge(new HyperLogLog(10)));

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }
}
