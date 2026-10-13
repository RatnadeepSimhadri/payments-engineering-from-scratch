// Lesson: phases/00-setup-and-tooling/05-benchmarking-and-profiling/docs/en.md
// Benchmarking and Profiling Without Lying to Yourself: a JMH-free harness with warm-up, forks,
// percentiles, dead-code sinks, coordinated-omission correction, and a sampling-profile aggregator.
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: Gil Tene, "How NOT to Measure Latency" (Strange Loop 2015) for coordinated omission;
//          OpenJDK JMH documentation for warm-up/fork/blackhole rationale; JEP 328 (Java Flight Recorder).
// Java 21, standard library only.

import java.util.ArrayList;
import java.util.Arrays;
import java.util.Collections;
import java.util.HashMap;
import java.util.LinkedHashMap;
import java.util.LinkedList;
import java.util.List;
import java.util.Map;
import java.util.Random;
import java.util.TreeMap;

/** A monotonic nanosecond source. Production uses SystemTicker; tests hand the harness a FakeTicker. */
interface Ticker {
    long nanoTime();
}

/** System.nanoTime: monotonic, not wall-clock, the only correct source for elapsed time. */
final class SystemTicker implements Ticker {
    @Override
    public long nanoTime() {
        return System.nanoTime();
    }
}

/** Returns scripted readings in order. The harness reads the ticker exactly twice per iteration. */
final class FakeTicker implements Ticker {
    private final long[] readings;
    private int next = 0;

    FakeTicker(long... readings) {
        this.readings = readings.clone();
    }

    @Override
    public long nanoTime() {
        if (next >= readings.length) throw new IllegalStateException("FakeTicker exhausted after " + readings.length + " readings");
        return readings[next++];
    }

    int readingsConsumed() {
        return next;
    }
}

/**
 * A sink the JIT cannot prove unused. Every benchmark body hands its result here; without a sink
 * the compiler is entitled to delete a computation whose result nobody reads, and the benchmark
 * then measures an empty loop.
 */
final class Blackhole {
    private volatile long sink;

    void consume(long value) {
        sink ^= value;
    }

    void consume(Object value) {
        if (value != null) sink ^= value.hashCode();
    }

    long value() {
        return sink;
    }
}

/** A unit of work to time. The body must hand its result to the blackhole. */
record Benchmark(String name, BenchmarkBody body) {
    Benchmark {
        if (name == null || name.isBlank()) throw new IllegalArgumentException("benchmark needs a name");
        if (body == null) throw new IllegalArgumentException("benchmark needs a body");
    }
}

@FunctionalInterface
interface BenchmarkBody {
    void run(Blackhole bh);
}

/** Order statistics over a sample. Percentiles use nearest-rank on the sorted copy; never interpolated. */
final class Stats {
    private Stats() {}

    /** Nearest-rank percentile: the smallest value such that at least p% of samples are <= it. */
    static long percentile(long[] samples, double p) {
        if (samples.length == 0) throw new IllegalArgumentException("no samples");
        if (p < 0 || p > 100) throw new IllegalArgumentException("percentile must be in 0..100: " + p);
        long[] sorted = samples.clone();
        Arrays.sort(sorted);
        if (p == 0) return sorted[0];
        int rank = (int) Math.ceil(p / 100.0 * sorted.length);   // 1-based rank
        return sorted[rank - 1];
    }

    static long min(long[] samples) {
        return percentile(samples, 0);
    }

    static long max(long[] samples) {
        return percentile(samples, 100);
    }

    static double mean(long[] samples) {
        if (samples.length == 0) throw new IllegalArgumentException("no samples");
        double sum = 0;
        for (long s : samples) sum += s;
        return sum / samples.length;
    }

    /** Population standard deviation. */
    static double stddev(long[] samples) {
        double mean = mean(samples);
        double acc = 0;
        for (long s : samples) acc += (s - mean) * (s - mean);
        return Math.sqrt(acc / samples.length);
    }

    /** Coefficient of variation: stddev / mean. Dimensionless, so comparable across benchmarks. */
    static double coefficientOfVariation(long[] samples) {
        double mean = mean(samples);
        return mean == 0 ? 0 : stddev(samples) / mean;
    }
}

/** How a benchmark is run. Forks are repeated runs that each start cold on the harness's warm-up schedule. */
record HarnessConfig(int warmupIterations, int measuredIterations, int forks, int opsPerIteration) {
    HarnessConfig {
        if (warmupIterations < 0) throw new IllegalArgumentException("warmupIterations must be >= 0");
        if (measuredIterations < 1) throw new IllegalArgumentException("measuredIterations must be >= 1");
        if (forks < 1) throw new IllegalArgumentException("forks must be >= 1");
        if (opsPerIteration < 1) throw new IllegalArgumentException("opsPerIteration must be >= 1");
    }

    /** Cheap bodies (a get, a relink): many ops per iteration so a sample is not dominated by the two ticker reads. */
    static final HarnessConfig QUICK = new HarnessConfig(10, 7, 3, 5_000);
    /** Bodies that already walk thousands of elements: fewer ops per iteration keeps the demo under ten seconds. */
    static final HarnessConfig HEAVY = new HarnessConfig(5, 7, 3, 200);
}

/** One fork's measured samples, in nanoseconds per operation, plus the discarded warm-up samples for inspection. */
record ForkResult(int fork, long[] warmupNsPerOp, long[] measuredNsPerOp) {
    long median() {
        return Stats.percentile(measuredNsPerOp, 50);
    }
}

/** Every fork's samples pooled, plus the cross-fork spread that tells you whether to believe the number. */
record BenchmarkResult(String name, List<ForkResult> forks, long[] pooledNsPerOp) {
    static final double UNSTABLE_CV = 0.10;

    long p50() { return Stats.percentile(pooledNsPerOp, 50); }
    long p90() { return Stats.percentile(pooledNsPerOp, 90); }
    long p99() { return Stats.percentile(pooledNsPerOp, 99); }
    long min() { return Stats.min(pooledNsPerOp); }
    long max() { return Stats.max(pooledNsPerOp); }

    long[] forkMedians() {
        long[] medians = new long[forks.size()];
        for (int i = 0; i < medians.length; i++) medians[i] = forks.get(i).median();
        return medians;
    }

    /** Spread of the per-fork medians. Above UNSTABLE_CV the forks disagree and the result is noise. */
    double forkSpread() {
        return forks.size() < 2 ? 0 : Stats.coefficientOfVariation(forkMedians());
    }

    boolean stable() {
        return forkSpread() <= UNSTABLE_CV;
    }

    String row() {
        return String.format("%-32s p50 %8d  p90 %8d  p99 %8d  max %9d  forks %s  spread %5.1f%% %s",
                name, p50(), p90(), p99(), max(), Arrays.toString(forkMedians()), forkSpread() * 100,
                stable() ? "" : "UNSTABLE");
    }
}

/**
 * The harness. For each fork: run warmupIterations (timed, discarded), then measuredIterations (kept).
 * One iteration = opsPerIteration calls of the body between two ticker readings; the sample is ns/op.
 * Invariant: exactly 2 * (warmup + measured) * forks ticker readings per benchmark, so a FakeTicker
 * can script every sample.
 */
final class Harness {
    private final Ticker ticker;
    private final HarnessConfig config;
    private final Blackhole blackhole = new Blackhole();

    Harness(Ticker ticker, HarnessConfig config) {
        this.ticker = ticker;
        this.config = config;
    }

    BenchmarkResult run(Benchmark benchmark) {
        List<ForkResult> forkResults = new ArrayList<>();
        List<Long> pooled = new ArrayList<>();
        for (int fork = 1; fork <= config.forks(); fork++) {
            long[] warm = new long[config.warmupIterations()];
            for (int i = 0; i < warm.length; i++) warm[i] = timeIteration(benchmark.body());
            long[] measured = new long[config.measuredIterations()];
            for (int i = 0; i < measured.length; i++) {
                measured[i] = timeIteration(benchmark.body());
                pooled.add(measured[i]);
            }
            forkResults.add(new ForkResult(fork, warm, measured));
        }
        long[] pooledArray = pooled.stream().mapToLong(Long::longValue).toArray();
        return new BenchmarkResult(benchmark.name(), forkResults, pooledArray);
    }

    private long timeIteration(BenchmarkBody body) {
        long start = ticker.nanoTime();
        for (int op = 0; op < config.opsPerIteration(); op++) body.run(blackhole);
        long end = ticker.nanoTime();
        long elapsed = end - start;
        if (elapsed < 0) throw new IllegalStateException("ticker went backwards: " + start + " -> " + end);
        return elapsed / config.opsPerIteration();
    }

    Blackhole blackhole() {
        return blackhole;
    }

    static String header() {
        return String.format("%-32s %-13s %-13s %-13s %-14s %s", "benchmark (ns/op)", "p50", "p90", "p99", "max", "per-fork medians");
    }
}

/**
 * Records latencies from a load generator that sends one request per expectedInterval.
 * Coordinated omission: when a request stalls for longer than the interval, the requests that should
 * have been sent during the stall were never sent, so their (also high) latencies are missing from
 * the sample. recordCorrected back-fills them the way HdrHistogram's recordValueWithExpectedInterval does.
 */
final class LatencyRecorder {
    private final List<Long> raw = new ArrayList<>();
    private final List<Long> corrected = new ArrayList<>();

    void record(long latencyNanos, long expectedIntervalNanos) {
        if (latencyNanos < 0) throw new IllegalArgumentException("latency must be >= 0");
        if (expectedIntervalNanos <= 0) throw new IllegalArgumentException("expected interval must be > 0");
        raw.add(latencyNanos);
        corrected.add(latencyNanos);
        for (long missing = latencyNanos - expectedIntervalNanos; missing >= expectedIntervalNanos; missing -= expectedIntervalNanos) {
            corrected.add(missing);
        }
    }

    long[] rawSamples() {
        return raw.stream().mapToLong(Long::longValue).toArray();
    }

    long[] correctedSamples() {
        return corrected.stream().mapToLong(Long::longValue).toArray();
    }

    int rawCount() { return raw.size(); }
    int correctedCount() { return corrected.size(); }
}

/**
 * What a sampling profiler (JFR, async-profiler) computes: for each periodic stack snapshot, the leaf
 * frame gets one unit of self time and every frame on the stack gets one unit of inclusive time.
 * Stacks are given root-first. Deterministic given the same samples.
 */
final class SampleProfile {
    private final Map<String, Integer> self = new HashMap<>();
    private final Map<String, Integer> inclusive = new HashMap<>();
    private int samples = 0;

    void record(String... stackRootFirst) {
        if (stackRootFirst.length == 0) throw new IllegalArgumentException("empty stack");
        samples++;
        self.merge(stackRootFirst[stackRootFirst.length - 1], 1, Integer::sum);
        for (String frame : new java.util.LinkedHashSet<>(Arrays.asList(stackRootFirst))) {
            inclusive.merge(frame, 1, Integer::sum);
        }
    }

    int samples() { return samples; }
    int selfCount(String frame) { return self.getOrDefault(frame, 0); }
    int inclusiveCount(String frame) { return inclusive.getOrDefault(frame, 0); }

    /** Frames by self time, descending; ties broken by name so the output is deterministic. */
    List<Map.Entry<String, Integer>> topSelf(int n) {
        return top(self, n);
    }

    List<Map.Entry<String, Integer>> topInclusive(int n) {
        return top(inclusive, n);
    }

    private static List<Map.Entry<String, Integer>> top(Map<String, Integer> counts, int n) {
        List<Map.Entry<String, Integer>> entries = new ArrayList<>(counts.entrySet());
        entries.sort((a, b) -> b.getValue().equals(a.getValue()) ? a.getKey().compareTo(b.getKey()) : b.getValue() - a.getValue());
        return entries.subList(0, Math.min(n, entries.size()));
    }

    String report(int n) {
        StringBuilder sb = new StringBuilder();
        sb.append(String.format("%d samples%n%-28s %6s %6s%n", samples, "frame", "self", "incl"));
        for (Map.Entry<String, Integer> e : topSelf(n)) {
            sb.append(String.format("%-28s %5.1f%% %5.1f%%%n", e.getKey(),
                    100.0 * e.getValue() / samples, 100.0 * inclusiveCount(e.getKey()) / samples));
        }
        return sb.toString();
    }
}

public final class Main {
    public static void main(String[] args) {
        System.out.println("== 1. Why warm-up matters: the first iterations are the interpreter, not your code ==");
        Harness warmDemo = new Harness(new SystemTicker(), new HarnessConfig(5, 5, 1, 20_000));
        int[] data = seededInts(1_000, 7);
        BenchmarkResult sum = warmDemo.run(new Benchmark("sum of 1000 ints", bh -> {
            long acc = 0;
            for (int v : data) acc += v;
            bh.consume(acc);
        }));
        ForkResult only = sum.forks().get(0);
        System.out.println("warm-up samples (discarded) ns/op: " + Arrays.toString(only.warmupNsPerOp()));
        System.out.println("measured samples           ns/op: " + Arrays.toString(only.measuredNsPerOp()));
        System.out.println("The first warm-up sample is usually several times the last measured one. JMH hides this; you should see it once.");

        System.out.println();
        System.out.println("== 2. ArrayList vs LinkedList, honestly ==");
        Harness harness = new Harness(new SystemTicker(), HarnessConfig.QUICK);
        Harness heavy = new Harness(new SystemTicker(), HarnessConfig.HEAVY);
        int n = 2_000;
        List<Integer> arrayList = new ArrayList<>();
        List<Integer> linkedList = new LinkedList<>();
        for (int i = 0; i < n; i++) { arrayList.add(i); linkedList.add(i); }
        int[] indices = seededInts(n, 42);
        System.out.println(Harness.header());
        for (BenchmarkResult r : List.of(
                harness.run(new Benchmark("ArrayList.get(random)", new RandomGet(arrayList, indices))),
                harness.run(new Benchmark("LinkedList.get(random)", new RandomGet(linkedList, indices))),
                harness.run(new Benchmark("ArrayList add(0)+remove(0)", bh -> { arrayList.add(0, -1); bh.consume(arrayList.remove(0)); })),
                harness.run(new Benchmark("LinkedList add(0)+remove(0)", bh -> { linkedList.add(0, -1); bh.consume(linkedList.remove(0)); })),
                heavy.run(new Benchmark("ArrayList iterate+sum", bh -> { long acc = 0; for (int v : arrayList) acc += v; bh.consume(acc); })),
                heavy.run(new Benchmark("LinkedList iterate+sum", bh -> { long acc = 0; for (int v : linkedList) acc += v; bh.consume(acc); })))) {
            System.out.println(r.row());
        }
        System.out.println("get(i): O(1) vs O(n) pointer chase. add(0): O(n) shift vs O(1) relink. Iteration: both O(n); the array wins on cache locality.");
        System.out.println("A benchmark that shows only one row is an argument, not a measurement.");

        System.out.println();
        System.out.println("== 3. HashMap vs TreeMap ==");
        Map<Integer, Integer> hashMap = new HashMap<>();
        Map<Integer, Integer> treeMap = new TreeMap<>();
        for (int i = 0; i < n; i++) { hashMap.put(i * 7919 % 100_003, i); treeMap.put(i * 7919 % 100_003, i); }
        int[] keys = new int[n];
        for (int i = 0; i < n; i++) keys[i] = (indices[i] % n) * 7919 % 100_003;
        System.out.println(Harness.header());
        for (BenchmarkResult r : List.of(
                harness.run(new Benchmark("HashMap.get", new MapGet(hashMap, keys))),
                harness.run(new Benchmark("TreeMap.get", new MapGet(treeMap, keys))),
                heavy.run(new Benchmark("HashMap keys in order (sort)", bh -> {
                    List<Integer> ks = new ArrayList<>(hashMap.keySet());
                    Collections.sort(ks);
                    bh.consume(ks.get(0));
                })),
                heavy.run(new Benchmark("TreeMap keys in order (iterate)", bh -> {
                    Integer first = null;
                    for (Integer k : treeMap.keySet()) { if (first == null) first = k; }
                    bh.consume(first);
                })))) {
            System.out.println(r.row());
        }
        System.out.println("get: O(1) expected vs O(log n). Ordered traversal: O(n log n) sort vs O(n) walk. Pick the map for the access pattern, not the headline.");

        System.out.println();
        System.out.println("== 4. Coordinated omission: one stall, two very different p99s (scripted, deterministic) ==");
        LatencyRecorder recorder = new LatencyRecorder();
        long interval = 1_000_000;                 // the load generator sends one request per 1 ms
        for (int i = 0; i < 999; i++) recorder.record(200_000, interval);   // 999 requests at 0.2 ms
        recorder.record(2_000_000_000L, interval);                           // one 2 s stall (a GC pause, a lock)
        long[] raw = recorder.rawSamples();
        long[] corrected = recorder.correctedSamples();
        System.out.printf("raw:       %d samples, p50 %.1f ms, p99 %.1f ms, max %.0f ms%n",
                raw.length, Stats.percentile(raw, 50) / 1e6, Stats.percentile(raw, 99) / 1e6, Stats.max(raw) / 1e6);
        System.out.printf("corrected: %d samples, p50 %.1f ms, p99 %.1f ms, max %.0f ms%n",
                corrected.length, Stats.percentile(corrected, 50) / 1e6, Stats.percentile(corrected, 99) / 1e6, Stats.max(corrected) / 1e6);
        System.out.println("During the 2 s stall the generator should have sent 1,999 more requests, each of which would have waited.");
        System.out.println("Raw p99 says the service is fine. Corrected p99 says a user hitting the stall waited over a second.");

        System.out.println();
        System.out.println("== 5. What a sampling profiler computes (scripted stacks) ==");
        SampleProfile profile = new SampleProfile();
        for (int i = 0; i < 60; i++) profile.record("main", "settleBatch", "computeFees", "Money.times");
        for (int i = 0; i < 25; i++) profile.record("main", "settleBatch", "writeFile", "FileChannel.write");
        for (int i = 0; i < 10; i++) profile.record("main", "settleBatch", "computeFees", "HashMap.get");
        for (int i = 0; i < 5; i++) profile.record("main", "settleBatch", "parseInput");
        System.out.print(profile.report(5));
        System.out.println("Self time names the hot leaf; inclusive time names the caller worth restructuring. computeFees is 70% inclusive, 0% self.");
    }

    /** Deterministic pseudo-random ints in [0, bound). */
    static int[] seededInts(int bound, long seed) {
        Random random = new Random(seed);
        int[] out = new int[bound];
        for (int i = 0; i < bound; i++) out[i] = random.nextInt(bound);
        return out;
    }

    /** Reads list.get(indices[cursor]) and advances; a named class so the body does not capture a mutable local. */
    static final class RandomGet implements BenchmarkBody {
        private final List<Integer> list;
        private final int[] indices;
        private int cursor = 0;

        RandomGet(List<Integer> list, int[] indices) {
            this.list = list;
            this.indices = indices;
        }

        @Override
        public void run(Blackhole bh) {
            bh.consume(list.get(indices[cursor]));
            cursor = (cursor + 1) % indices.length;
        }
    }

    static final class MapGet implements BenchmarkBody {
        private final Map<Integer, Integer> map;
        private final int[] keys;
        private int cursor = 0;

        MapGet(Map<Integer, Integer> map, int[] keys) {
            this.map = map;
            this.keys = keys;
        }

        @Override
        public void run(Blackhole bh) {
            bh.consume(map.get(keys[cursor]));
            cursor = (cursor + 1) % keys.length;
        }
    }
}
