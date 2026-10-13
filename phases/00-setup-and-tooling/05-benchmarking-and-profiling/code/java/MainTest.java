// Tests for phases/00-setup-and-tooling/05-benchmarking-and-profiling. Run: javac -d .out *.java && java -cp .out MainTest
import java.util.ArrayList;
import java.util.List;

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
        // Stats: nearest-rank percentiles on real numbers
        long[] ten = {10, 20, 30, 40, 50, 60, 70, 80, 90, 100};
        check("p50 of 1..10 is the 5th value (nearest rank, not interpolated)", Stats.percentile(ten, 50) == 50);
        check("p90 of ten values is the 9th", Stats.percentile(ten, 90) == 90);
        check("p99 of ten values is the max", Stats.percentile(ten, 99) == 100);
        check("p0 is the min", Stats.percentile(ten, 0) == 10);
        check("p100 is the max", Stats.percentile(ten, 100) == 100);
        check("percentile does not mutate input", ten[0] == 10 && ten[9] == 100);
        long[] unsorted = {90, 10, 50};
        check("percentile sorts a copy", Stats.percentile(unsorted, 50) == 50 && unsorted[0] == 90);
        check("single sample: every percentile is that sample", Stats.percentile(new long[]{7}, 99) == 7 && Stats.percentile(new long[]{7}, 1) == 7);
        check("mean", Stats.mean(ten) == 55.0);
        check("stddev of constant sample is zero", Stats.stddev(new long[]{5, 5, 5}) == 0.0);
        check("cv of constant sample is zero", Stats.coefficientOfVariation(new long[]{5, 5, 5}) == 0.0);
        check("cv is dimensionless: scaling samples leaves it unchanged",
                Math.abs(Stats.coefficientOfVariation(new long[]{10, 20}) - Stats.coefficientOfVariation(new long[]{1000, 2000})) < 1e-12);
        checkThrows("percentile rejects empty sample", IllegalArgumentException.class, () -> Stats.percentile(new long[0], 50));
        checkThrows("percentile rejects p > 100", IllegalArgumentException.class, () -> Stats.percentile(ten, 101));

        // Harness with a scripted ticker: warm-up discarded, measured kept, ns/op division, fork pooling
        // 1 fork, 2 warm-up, 3 measured, 10 ops/iteration -> 10 readings. Durations: 1000, 900 | 100, 200, 300.
        FakeTicker ticker = new FakeTicker(0, 1000, 1000, 1900, 1900, 2000, 2000, 2200, 2200, 2500);
        Harness harness = new Harness(ticker, new HarnessConfig(2, 3, 1, 10));
        int[] calls = {0};
        BenchmarkResult result = harness.run(new Benchmark("scripted", bh -> { calls[0]++; bh.consume(calls[0]); }));
        check("body ran (warmup + measured) * ops times", calls[0] == (2 + 3) * 10);
        check("ticker read exactly twice per iteration", ticker.readingsConsumed() == 10);
        ForkResult fork = result.forks().get(0);
        check("warm-up samples are kept for inspection but not pooled", fork.warmupNsPerOp().length == 2 && fork.warmupNsPerOp()[0] == 100 && fork.warmupNsPerOp()[1] == 90);
        check("measured samples are ns/op (elapsed / ops)", fork.measuredNsPerOp()[0] == 10 && fork.measuredNsPerOp()[1] == 20 && fork.measuredNsPerOp()[2] == 30);
        check("pooled excludes warm-up", result.pooledNsPerOp().length == 3 && result.min() == 10 && result.max() == 30);
        check("p50 of measured", result.p50() == 20);
        check("blackhole saw the results", harness.blackhole().value() != 0);
        check("single fork has zero spread and is stable", result.forkSpread() == 0.0 && result.stable());

        // Two forks that disagree are flagged
        FakeTicker two = new FakeTicker(
                0, 100, 100, 200,      // fork 1: measured 100 -> 100 ns/op (ops=1), warmup 0
                200, 300, 300, 1000);  // fork 2: measured 700
        BenchmarkResult twoForks = new Harness(two, new HarnessConfig(1, 1, 2, 1)).run(new Benchmark("drift", bh -> bh.consume(1)));
        check("per-fork medians reported in order", twoForks.forkMedians()[0] == 100 && twoForks.forkMedians()[1] == 700);
        check("forks that disagree by 7x are UNSTABLE", !twoForks.stable() && twoForks.forkSpread() > BenchmarkResult.UNSTABLE_CV);
        check("row() carries the UNSTABLE marker", twoForks.row().contains("UNSTABLE"));
        check("pooled samples span both forks", twoForks.pooledNsPerOp().length == 2);

        // Boundary and failure: ticker exhaustion and backwards time are errors, not silent zeros
        checkThrows("exhausted ticker fails loudly", IllegalStateException.class,
                () -> new Harness(new FakeTicker(0, 10), new HarnessConfig(0, 2, 1, 1)).run(new Benchmark("short", bh -> bh.consume(1))));
        checkThrows("ticker going backwards is an error", IllegalStateException.class,
                () -> new Harness(new FakeTicker(500, 400), new HarnessConfig(0, 1, 1, 1)).run(new Benchmark("back", bh -> bh.consume(1))));
        checkThrows("config rejects zero measured iterations", IllegalArgumentException.class, () -> new HarnessConfig(1, 0, 1, 1));
        checkThrows("config rejects zero forks", IllegalArgumentException.class, () -> new HarnessConfig(1, 1, 0, 1));
        checkThrows("benchmark rejects blank name", IllegalArgumentException.class, () -> new Benchmark(" ", bh -> {}));

        // Blackhole: consuming changes state, so a body that consumes cannot be dead code
        Blackhole bh = new Blackhole();
        bh.consume(0b1010L);
        check("blackhole xors values in", bh.value() == 0b1010L);
        bh.consume(0b1010L);
        check("blackhole state is a fold of everything consumed", bh.value() == 0);
        bh.consume((Object) null);
        check("blackhole tolerates null", bh.value() == 0);

        // Coordinated omission: a stall back-fills the requests that were never sent
        LatencyRecorder rec = new LatencyRecorder();
        rec.record(5_000, 1_000);
        check("a 5 ms latency at a 1 ms interval records 5 corrected samples", rec.rawCount() == 1 && rec.correctedCount() == 5);
        long[] corrected = rec.correctedSamples();
        check("back-filled samples step down by the interval", corrected[0] == 5_000 && corrected[1] == 4_000 && corrected[4] == 1_000);
        LatencyRecorder fine = new LatencyRecorder();
        fine.record(999, 1_000);
        check("latency below the interval adds nothing", fine.correctedCount() == 1);
        LatencyRecorder exact = new LatencyRecorder();
        exact.record(2_000, 1_000);
        check("latency of exactly two intervals back-fills one", exact.correctedCount() == 2 && exact.correctedSamples()[1] == 1_000);
        LatencyRecorder load = new LatencyRecorder();
        for (int i = 0; i < 999; i++) load.record(1_000, 1_000);
        load.record(1_000_000, 1_000);
        check("raw p99 hides a one-second stall", Stats.percentile(load.rawSamples(), 99) == 1_000);
        check("corrected p99 exposes it", Stats.percentile(load.correctedSamples(), 99) > 900_000);
        check("corrected count = raw + missing requests", load.correctedCount() == 1000 + 999);
        checkThrows("recorder rejects non-positive interval", IllegalArgumentException.class, () -> new LatencyRecorder().record(1, 0));

        // Sampling profile: leaf gets self, every frame gets inclusive, deterministic ordering
        SampleProfile profile = new SampleProfile();
        profile.record("main", "a", "b");
        profile.record("main", "a", "b");
        profile.record("main", "a", "c");
        profile.record("main", "d");
        check("samples counted", profile.samples() == 4);
        check("self time goes to the leaf only", profile.selfCount("b") == 2 && profile.selfCount("a") == 0 && profile.selfCount("main") == 0);
        check("inclusive time goes to every frame on the stack", profile.inclusiveCount("a") == 3 && profile.inclusiveCount("main") == 4);
        check("topSelf orders by count then name", profile.topSelf(3).get(0).getKey().equals("b") && profile.topSelf(3).get(1).getKey().equals("c"));
        check("topInclusive puts the root first", profile.topInclusive(1).get(0).getKey().equals("main"));
        SampleProfile recursive = new SampleProfile();
        recursive.record("main", "f", "f", "f");
        check("recursive frame counted once per sample in inclusive time", recursive.inclusiveCount("f") == 1 && recursive.selfCount("f") == 1);
        checkThrows("empty stack rejected", IllegalArgumentException.class, () -> new SampleProfile().record());

        // Seeded data is reproducible
        check("seededInts is deterministic", java.util.Arrays.equals(Main.seededInts(100, 42), Main.seededInts(100, 42)));

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }
}
