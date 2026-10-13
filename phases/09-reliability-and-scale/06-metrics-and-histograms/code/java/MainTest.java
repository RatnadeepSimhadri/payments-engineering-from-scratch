// Tests for phases/09-reliability-and-scale/06-metrics-and-histograms. Run: javac -d .out *.java && java -cp .out MainTest
import java.util.ArrayList;
import java.util.Arrays;
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
        final long MAX = Main.MAX_MICROS;

        { // counters
            Counter c = new Counter();
            c.inc(); c.inc(4);
            check("counter accumulates", c.value() == 5);
            checkThrows("counter rejects negative increments", IllegalArgumentException.class, () -> c.inc(-1));
            check("rate over an interval", Counter.rate(1000, 1250, 10) == 25.0);
            check("rate after a reset assumes restart from zero", Counter.rate(1250, 40, 10) == 4.0);
            checkThrows("rate rejects a zero interval", IllegalArgumentException.class, () -> Counter.rate(0, 1, 0));
        }
        { // gauges
            Gauge g = new Gauge();
            g.set(10); g.inc(); g.dec(); g.dec();
            check("gauge goes up and down", g.value() == 9);
        }
        { // bucket layout: indexOf and lowerBound are inverses, buckets tile the range, width bound holds
            LogLinearHistogram h = new LogLinearHistogram(7, MAX);
            boolean inverse = true, contiguous = true, widthBound = true;
            for (int i = 0; i < h.bucketCount() - 1; i++) {
                long lo = h.lowerBound(i), hi = h.upperBound(i);
                if (h.indexOf(lo) != i || h.indexOf(hi) != i) inverse = false;
                if (h.lowerBound(i + 1) != hi + 1) contiguous = false;
                if (lo >= 128 && (hi - lo + 1) * 128 > lo) widthBound = false;
            }
            check("indexOf(lowerBound(i)) == i and indexOf(upperBound(i)) == i for every bucket", inverse);
            check("buckets are contiguous with no gaps", contiguous);
            check("every bucket above the exact range is at most 1/128 of its lower bound wide", widthBound);
            check("values below 2^precisionBits are exact", h.indexOf(0) == 0 && h.indexOf(127) == 127 && h.upperBound(127) == 127);
            check("first log-linear bucket starts at 128", h.lowerBound(128) == 128 && h.indexOf(128) == 128);
            check("bucket for 1,000,000 is 4,096 wide", h.upperBound(h.indexOf(1_000_000)) - h.lowerBound(h.indexOf(1_000_000)) + 1 == 4096);
            check("highest trackable value has a bucket", h.indexOf(MAX) == h.bucketCount() - 1);
            checkThrows("negative values are rejected", IllegalArgumentException.class, () -> h.record(-1));
            checkThrows("values above highestTrackable are rejected", IllegalArgumentException.class, () -> h.record(MAX + 1));
            checkThrows("precision must be at least 1 bit", IllegalArgumentException.class, () -> new LogLinearHistogram(0, MAX));
        }
        { // percentiles on a small exact dataset
            LogLinearHistogram h = new LogLinearHistogram(7, MAX);
            for (long v = 1; v <= 100; v++) h.record(v);
            check("count", h.count() == 100);
            check("min and max", h.min() == 1 && h.max() == 100);
            check("mean is exact", h.mean() == 50.5);
            check("p50 of 1..100 is 50", h.value(50) == 50);
            check("p99 of 1..100 is 99", h.value(99) == 99);
            check("p100 is the max", h.value(100) == 100);
            check("p0 is the min", h.value(0) == 1);
            check("exact-range estimate has zero width", h.percentile(50).lower() == 50 && h.percentile(50).upper() == 50);
            check("fraction at or below 25 is 0.25", h.fractionAtOrBelow(25) == 0.25);
            LogLinearHistogram empty = new LogLinearHistogram(7, MAX);
            check("empty histogram reports zeros", empty.count() == 0 && empty.value(99) == 0 && empty.mean() == 0 && empty.min() == 0);
            checkThrows("percentile above 100 rejected", IllegalArgumentException.class, () -> h.percentile(101));
        }
        { // the error bound holds against exact percentiles on a skewed distribution
            Rng rng = new Rng(2024);
            long[] samples = Main.authorizationLatencies(100_000, 0.01, rng);
            LogLinearHistogram h = new LogLinearHistogram(7, MAX);
            for (long s : samples) h.record(s);
            long[] sorted = samples.clone();
            Arrays.sort(sorted);
            boolean bracketed = true, bounded = true;
            for (double p : new double[]{1, 10, 50, 90, 99, 99.9, 99.99}) {
                Estimate e = h.percentile(p);
                long exact = Main.exactPercentile(sorted, p);
                if (exact < e.lower() || exact > e.upper()) bracketed = false;
                if (e.relativeError() > 1.0 / 128) bounded = false;
            }
            check("every exact percentile lies inside the histogram's bucket bounds", bracketed);
            check("relative error of every estimate is at most 1/128", bounded);
            check("mean sits far below p99 on the skewed data (averages lie)", h.mean() * 10 < h.value(99));
            check("p50 is near 12 ms", h.value(50) > 11_000 && h.value(50) < 13_500);
            check("p99 is over a second", h.value(99) > 1_000_000);
            double sli = h.fractionAtOrBelow(100_000);
            long exactAtOrBelow = Arrays.stream(sorted).filter(v -> v <= 100_000).count();
            check("SLI from buckets within one bucket of the exact fraction", Math.abs(sli - exactAtOrBelow / 100_000.0) < 0.001);
            LogLinearHistogram fine = new LogLinearHistogram(10, MAX);
            for (long s : samples) fine.record(s);
            check("more precision bits give a tighter p99 bracket", fine.percentile(99).relativeError() <= 1.0 / 1024 && fine.bucketCount() > h.bucketCount());
        }
        { // merge: exact and order-independent; averaging percentiles is not
            Rng r1 = new Rng(7), r2 = new Rng(8);
            long[] a = Main.authorizationLatencies(90_000, 0.0, r1), b = Main.authorizationLatencies(10_000, 0.10, r2);
            LogLinearHistogram ha = new LogLinearHistogram(7, MAX), hb = new LogLinearHistogram(7, MAX), all = new LogLinearHistogram(7, MAX);
            for (long s : a) { ha.record(s); all.record(s); }
            for (long s : b) { hb.record(s); all.record(s); }
            LogLinearHistogram merged = ha.copy();
            merged.merge(hb);
            check("merged count is the sum", merged.count() == 100_000);
            check("merge equals recording everything into one histogram", merged.value(99) == all.value(99) && merged.value(50) == all.value(50) && merged.mean() == all.mean());
            LogLinearHistogram other = hb.copy();
            other.merge(ha);
            check("merge is commutative", other.value(99) == merged.value(99) && other.min() == merged.min() && other.max() == merged.max());
            long avgOfP99 = (ha.value(99) + hb.value(99)) / 2;
            check("average of per-host p99s is not the fleet p99", Math.abs(avgOfP99 - merged.value(99)) > merged.value(99) / 4);
            check("fleet p99 is between the two host p99s", merged.value(99) > ha.value(99) && merged.value(99) < hb.value(99));
            check("copy does not alias the source", ha.copy().count() == ha.count() && merged.count() != ha.count());
            checkThrows("merging different layouts is rejected", IllegalArgumentException.class, () -> ha.merge(new LogLinearHistogram(8, MAX)));
        }
        { // recordMany and cumulative export
            LogLinearHistogram h = new LogLinearHistogram(7, MAX);
            h.recordMany(10, 5);
            h.recordMany(1_000, 3);
            h.recordMany(5_000_000, 2);
            check("recordMany counts and sums", h.count() == 10 && h.mean() == (50 + 3_000 + 10_000_000) / 10.0);
            long[] cum = h.cumulative(new long[]{10, 999, 1_003, 1_000_000});
            check("cumulative counts are monotone and match", cum[0] == 5 && cum[1] == 5 && cum[2] == 8 && cum[3] == 8);
            checkThrows("recordMany rejects zero count", IllegalArgumentException.class, () -> h.recordMany(1, 0));
        }
        { // registry and exposition format
            MetricsRegistry reg = new MetricsRegistry(7, MAX);
            reg.counter("req").inc(3);
            reg.gauge("inflight").set(2);
            reg.histogram("lat").record(500);
            reg.histogram("lat").record(2_000);
            check("registry returns the same metric for the same name", reg.counter("req") == reg.counter("req") && reg.histogram("lat").count() == 2);
            String text = reg.render(new long[]{1_000, 10_000});
            check("counter rendered with _total", text.contains("# TYPE req_total counter\nreq_total 3\n"));
            check("gauge rendered", text.contains("# TYPE inflight gauge\ninflight 2\n"));
            check("histogram buckets are cumulative", text.contains("lat_bucket{le=\"1000\"} 1\n") && text.contains("lat_bucket{le=\"10000\"} 2\n") && text.contains("lat_bucket{le=\"+Inf\"} 2\n"));
            check("histogram sum and count rendered", text.contains("lat_sum 2500\n") && text.contains("lat_count 2\n"));
        }
        { // the generator is deterministic
            check("splitmix64 seed 42 first value", new Rng(42).nextLong() == -4767286540954276203L);
            check("log-normal samples are positive", new Rng(1).nextLogNormalMicros(12_000, 0.5) > 0);
        }

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }
}
