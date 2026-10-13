// Lesson: phases/09-reliability-and-scale/06-metrics-and-histograms/docs/en.md
// Metrics: Counters, Gauges, and Histograms Without Averages
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: Gil Tene, HdrHistogram (log-linear bucketing, "How NOT to Measure Latency"); Prometheus exposition format
//          and metric types documentation; Dean & Barroso, "The Tail at Scale" (CACM 2013). Java 21, standard library only.

import java.util.ArrayList;
import java.util.Arrays;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;

/** SplitMix64: a tiny seeded generator so every number in the demo and tests is reproducible. */
final class Rng {
    private long state;
    Rng(long seed) { this.state = seed; }

    long nextLong() {
        state += 0x9E3779B97F4A7C15L;
        long z = state;
        z = (z ^ (z >>> 30)) * 0xBF58476D1CE4E5B9L;
        z = (z ^ (z >>> 27)) * 0x94D049BB133111EBL;
        return z ^ (z >>> 31);
    }

    double nextDouble() { return (nextLong() >>> 11) * 0x1.0p-53; }

    /** Standard normal via Box-Muller; one draw per call, the second is discarded for simplicity. */
    double nextGaussian() {
        double u1 = 1.0 - nextDouble(), u2 = nextDouble();
        return Math.sqrt(-2.0 * Math.log(u1)) * Math.cos(2 * Math.PI * u2);
    }

    /** Log-normal: the usual shape of service latency, a hump with a long right tail. */
    long nextLogNormalMicros(double medianMicros, double sigma) {
        return Math.max(1, Math.round(medianMicros * Math.exp(sigma * nextGaussian())));
    }
}

/** A monotonically non-decreasing count. Rates are derived by the scraper, never stored here. */
final class Counter {
    private long value;

    void inc() { inc(1); }

    void inc(long n) {
        if (n < 0) throw new IllegalArgumentException("counter can only go up: " + n);
        value = Math.addExact(value, n);
    }

    long value() { return value; }

    /**
     * The scraper's rate calculation over two samples. A counter that went down was reset by a process restart,
     * and the only honest assumption is that it restarted from zero.
     */
    static double rate(long previous, long current, double seconds) {
        if (seconds <= 0) throw new IllegalArgumentException("interval must be positive");
        long delta = current >= previous ? current - previous : current;
        return delta / seconds;
    }
}

/** A value that can go up or down: queue depth, in-flight requests, pending outbox rows. */
final class Gauge {
    private long value;
    void set(long v) { value = v; }
    void inc() { value++; }
    void dec() { value--; }
    long value() { return value; }
}

/** A percentile estimate with the bucket bounds that contain the true value. */
record Estimate(double percentile, long lower, long upper) {
    /** The worst-case relative error of reporting `upper` as the answer. */
    double relativeError() { return lower == 0 ? 0.0 : (double) (upper - lower) / lower; }
    @Override public String toString() { return String.format("p%s in [%d, %d]", trim(percentile), lower, upper); }
    static String trim(double p) { return p == Math.rint(p) ? Long.toString((long) p) : Double.toString(p); }
}

/**
 * A log-linear histogram in the style of HdrHistogram. Values below 2^precisionBits are counted exactly, one
 * bucket per integer. Above that, each power-of-two range [2^e, 2^(e+1)) is split into 2^precisionBits equal
 * sub-buckets, so every bucket's width is at most 2^-precisionBits of its lower bound: the relative error of any
 * percentile is bounded by that fraction regardless of how large the value is. Memory is fixed at construction.
 */
final class LogLinearHistogram {
    final int precisionBits;
    final long highestTrackable;
    private final int linear;              // 2^precisionBits sub-buckets per power of two
    private final long[] counts;
    private long total;
    private long sum;                      // exact, for the mean: kept so the demo can show what the mean hides
    private long min = Long.MAX_VALUE;
    private long max = Long.MIN_VALUE;

    LogLinearHistogram(int precisionBits, long highestTrackable) {
        if (precisionBits < 1 || precisionBits > 16) throw new IllegalArgumentException("precisionBits must be 1..16");
        if (highestTrackable < (1L << precisionBits)) throw new IllegalArgumentException("highestTrackable too small");
        this.precisionBits = precisionBits;
        this.highestTrackable = highestTrackable;
        this.linear = 1 << precisionBits;
        this.counts = new long[indexOf(highestTrackable) + 1];
    }

    /** Which bucket holds `v`. Exact below 2^precisionBits; log-linear above. */
    int indexOf(long v) {
        if (v < linear) return (int) v;
        int e = 63 - Long.numberOfLeadingZeros(v);                  // floor(log2 v), e >= precisionBits
        long sub = (v - (1L << e)) >> (e - precisionBits);          // which of the 2^precisionBits slices of [2^e, 2^(e+1))
        return (int) ((long) (e - precisionBits + 1) * linear + sub);
    }

    /** The smallest value that lands in bucket `index`. Inverse of indexOf. */
    long lowerBound(int index) {
        if (index < linear) return index;
        int k = index / linear;
        int e = k + precisionBits - 1;
        long sub = index % linear;
        return (1L << e) + (sub << (e - precisionBits));
    }

    /** The largest value that lands in bucket `index`. */
    long upperBound(int index) { return lowerBound(index + 1) - 1; }

    void record(long v) { recordMany(v, 1); }

    void recordMany(long v, long n) {
        if (v < 0 || v > highestTrackable) throw new IllegalArgumentException("value out of range: " + v);
        if (n <= 0) throw new IllegalArgumentException("count must be positive");
        counts[indexOf(v)] += n;
        total = Math.addExact(total, n);
        sum = Math.addExact(sum, Math.multiplyExact(v, n));
        if (v < min) min = v;
        if (v > max) max = v;
    }

    long count() { return total; }
    long min() { return total == 0 ? 0 : min; }
    long max() { return total == 0 ? 0 : max; }
    double mean() { return total == 0 ? 0 : (double) sum / total; }
    int bucketCount() { return counts.length; }
    long countAt(int index) { return counts[index]; }

    /**
     * Nearest-rank percentile: the bucket containing the value at rank ceil(p/100 * count). The estimate carries
     * the bucket's bounds; the true value lies inside them. Reports `upper` as the point value, never below the truth.
     */
    Estimate percentile(double p) {
        if (p < 0 || p > 100) throw new IllegalArgumentException("percentile must be 0..100");
        if (total == 0) return new Estimate(p, 0, 0);
        long rank = Math.max(1, (long) Math.ceil(p / 100.0 * total));
        long seen = 0;
        for (int i = 0; i < counts.length; i++) {
            seen += counts[i];
            if (seen >= rank) return new Estimate(p, Math.max(lowerBound(i), min), Math.min(upperBound(i), max));
        }
        return new Estimate(p, max, max);
    }

    long value(double p) { return percentile(p).upper(); }

    /** Fraction of recorded values at or below `threshold`: an SLO attainment query answered from the buckets. */
    double fractionAtOrBelow(long threshold) {
        if (total == 0) return 1.0;
        int idx = indexOf(Math.min(threshold, highestTrackable));
        long seen = 0;
        for (int i = 0; i <= idx; i++) seen += counts[i];
        return (double) seen / total;
    }

    /** Merge another histogram with identical layout into this one. Percentiles of the merge are exact to the same bound. */
    void merge(LogLinearHistogram other) {
        if (other.precisionBits != precisionBits || other.highestTrackable != highestTrackable) {
            throw new IllegalArgumentException("cannot merge histograms with different layouts");
        }
        for (int i = 0; i < counts.length; i++) counts[i] = Math.addExact(counts[i], other.counts[i]);
        total = Math.addExact(total, other.total);
        sum = Math.addExact(sum, other.sum);
        if (other.total > 0) { min = Math.min(min, other.min); max = Math.max(max, other.max); }
    }

    LogLinearHistogram copy() {
        LogLinearHistogram h = new LogLinearHistogram(precisionBits, highestTrackable);
        h.merge(this);
        return h;
    }

    /** Cumulative counts at the given upper bounds: the shape Prometheus classic histograms export (`le`). */
    long[] cumulative(long[] boundaries) {
        long[] out = new long[boundaries.length];
        for (int b = 0; b < boundaries.length; b++) {
            int idx = indexOf(Math.min(boundaries[b], highestTrackable));
            long seen = 0;
            for (int i = 0; i <= idx; i++) seen += counts[i];
            out[b] = seen;
        }
        return out;
    }
}

/** A name-to-metric registry with a text exposition in the Prometheus format. */
final class MetricsRegistry {
    private final Map<String, Counter> counters = new LinkedHashMap<>();
    private final Map<String, Gauge> gauges = new LinkedHashMap<>();
    private final Map<String, LogLinearHistogram> histograms = new LinkedHashMap<>();
    final int precisionBits;
    final long highestTrackable;

    MetricsRegistry(int precisionBits, long highestTrackable) { this.precisionBits = precisionBits; this.highestTrackable = highestTrackable; }

    Counter counter(String name) { return counters.computeIfAbsent(name, n -> new Counter()); }
    Gauge gauge(String name) { return gauges.computeIfAbsent(name, n -> new Gauge()); }
    LogLinearHistogram histogram(String name) { return histograms.computeIfAbsent(name, n -> new LogLinearHistogram(precisionBits, highestTrackable)); }

    /** Prometheus text format. Histograms are exported at the given `le` boundaries plus +Inf, _sum and _count. */
    String render(long[] histogramBoundaries) {
        StringBuilder sb = new StringBuilder();
        for (Map.Entry<String, Counter> e : counters.entrySet()) {
            sb.append("# TYPE ").append(e.getKey()).append("_total counter\n");
            sb.append(e.getKey()).append("_total ").append(e.getValue().value()).append('\n');
        }
        for (Map.Entry<String, Gauge> e : gauges.entrySet()) {
            sb.append("# TYPE ").append(e.getKey()).append(" gauge\n");
            sb.append(e.getKey()).append(' ').append(e.getValue().value()).append('\n');
        }
        for (Map.Entry<String, LogLinearHistogram> e : histograms.entrySet()) {
            LogLinearHistogram h = e.getValue();
            sb.append("# TYPE ").append(e.getKey()).append(" histogram\n");
            long[] cum = h.cumulative(histogramBoundaries);
            for (int i = 0; i < histogramBoundaries.length; i++) {
                sb.append(e.getKey()).append("_bucket{le=\"").append(histogramBoundaries[i]).append("\"} ").append(cum[i]).append('\n');
            }
            sb.append(e.getKey()).append("_bucket{le=\"+Inf\"} ").append(h.count()).append('\n');
            sb.append(e.getKey()).append("_sum ").append(Math.round(h.mean() * h.count())).append('\n');
            sb.append(e.getKey()).append("_count ").append(h.count()).append('\n');
        }
        return sb.toString();
    }
}

public final class Main {
    static final long MAX_MICROS = 60L * 1_000_000;    // one minute

    /** Exact nearest-rank percentile on raw samples, the ground truth the histogram is compared against. */
    static long exactPercentile(long[] sorted, double p) {
        int rank = (int) Math.max(1, Math.ceil(p / 100.0 * sorted.length));
        return sorted[rank - 1];
    }

    static String ms(long micros) { return String.format("%.1f ms", micros / 1000.0); }

    /** A payments authorization endpoint: most calls are fast, a slice hit a slow issuer path. */
    static long[] authorizationLatencies(int n, double slowFraction, Rng rng) {
        long[] out = new long[n];
        for (int i = 0; i < n; i++) {
            boolean slow = rng.nextDouble() < slowFraction;
            out[i] = slow ? rng.nextLogNormalMicros(2_000_000, 0.3) : rng.nextLogNormalMicros(12_000, 0.5);
        }
        return out;
    }

    public static void main(String[] args) {
        System.out.println("== 1. Bucket layout: precisionBits=7 means at most 1/128 = 0.78% relative error ==");
        LogLinearHistogram layout = new LogLinearHistogram(7, MAX_MICROS);
        for (long v : new long[]{5, 127, 128, 129, 255, 256, 1_000, 12_345, 1_000_000, 2_000_000}) {
            int i = layout.indexOf(v);
            System.out.printf("  value %,10d -> bucket %5d covering [%,d, %,d]  width %,d%n", v, i, layout.lowerBound(i), layout.upperBound(i), layout.upperBound(i) - layout.lowerBound(i) + 1);
        }
        System.out.println("  buckets allocated for values up to one minute in microseconds: " + layout.bucketCount());

        System.out.println("\n== 2. Averages lie: 100,000 authorizations, 1% hit a slow issuer path ==");
        Rng rng = new Rng(2024);
        long[] samples = authorizationLatencies(100_000, 0.01, rng);
        LogLinearHistogram h = new LogLinearHistogram(7, MAX_MICROS);
        for (long s : samples) h.record(s);
        long[] sorted = samples.clone();
        Arrays.sort(sorted);
        System.out.println("  mean   = " + ms(Math.round(h.mean())) + "   <- under a 100 ms SLO, this looks healthy");
        for (double p : new double[]{50, 90, 99, 99.9}) {
            Estimate e = h.percentile(p);
            System.out.printf("  p%-5s = %-12s histogram says [%s, %s], exact = %s, bound %.2f%%%n", Estimate.trim(p), ms(e.upper()), ms(e.lower()), ms(e.upper()), ms(exactPercentile(sorted, p)), e.relativeError() * 100);
        }
        System.out.printf("  fraction of requests at or below 100 ms = %.3f%%  (the SLI, answered from the buckets)%n", 100 * h.fractionAtOrBelow(100_000));
        System.out.println("  one in a hundred customers waited more than a second; the average never showed it");

        System.out.println("\n== 3. Averaging percentiles is wrong; merging histograms is right ==");
        Rng r1 = new Rng(7), r2 = new Rng(8);
        LogLinearHistogram hostA = new LogLinearHistogram(7, MAX_MICROS), hostB = new LogLinearHistogram(7, MAX_MICROS);
        for (long s : authorizationLatencies(90_000, 0.0, r1)) hostA.record(s);          // healthy host, lots of traffic
        for (long s : authorizationLatencies(10_000, 0.10, r2)) hostB.record(s);         // sick host, little traffic
        LogLinearHistogram fleet = hostA.copy();
        fleet.merge(hostB);
        System.out.println("  host A (90,000 requests): p99 = " + ms(hostA.value(99)) + "   host B (10,000 requests, 10% slow): p99 = " + ms(hostB.value(99)));
        System.out.println("  average of the two p99s      = " + ms((hostA.value(99) + hostB.value(99)) / 2) + "   <- meaningless");
        System.out.println("  max of the two p99s          = " + ms(Math.max(hostA.value(99), hostB.value(99))) + "   <- pessimistic");
        System.out.println("  p99 of the merged histogram  = " + ms(fleet.value(99)) + "   <- the fleet p99, computed correctly");
        System.out.println("  average of means (A, B)      = " + ms(Math.round((hostA.mean() + hostB.mean()) / 2)) + "   vs true fleet mean " + ms(Math.round(fleet.mean())) + "  (unweighted means are wrong too)");

        System.out.println("\n== 4. Counters only go up; the scraper computes the rate and must handle a restart ==");
        Counter auths = new Counter();
        auths.inc(1_000);
        long t0 = auths.value();
        auths.inc(250);
        System.out.printf("  authorizations_total %d -> %d over 10 s: rate = %.1f/s%n", t0, auths.value(), Counter.rate(t0, auths.value(), 10));
        Counter afterRestart = new Counter();
        afterRestart.inc(40);
        System.out.printf("  after a process restart the counter reads %d: naive rate = %.1f/s, reset-aware rate = %.1f/s%n",
                afterRestart.value(), (afterRestart.value() - auths.value()) / 10.0, Counter.rate(auths.value(), afterRestart.value(), 10));

        System.out.println("\n== 5. Exposition: what a scraper sees ==");
        MetricsRegistry reg = new MetricsRegistry(7, MAX_MICROS);
        reg.counter("auth_requests").inc(100_000);
        reg.counter("auth_failures").inc(312);
        reg.gauge("auth_inflight").set(17);
        reg.histogram("auth_latency_micros").merge(h);
        System.out.print(reg.render(new long[]{10_000, 25_000, 50_000, 100_000, 250_000, 1_000_000, 5_000_000}).indent(2));
    }
}
