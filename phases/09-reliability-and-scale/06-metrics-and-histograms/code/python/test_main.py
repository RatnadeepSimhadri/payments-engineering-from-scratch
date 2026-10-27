"""Tests for phases/09-reliability-and-scale/06-metrics-and-histograms. Run: python3 -m unittest -v"""
import unittest

from main import (MAX_MICROS, Counter, Gauge, LogLinearHistogram, MetricsRegistry, Rng, authorization_latencies,
                  exact_percentile)

MAX = MAX_MICROS


class CounterAndGaugeTest(unittest.TestCase):
    def test_counter(self):
        c = Counter()
        c.inc()
        c.inc(4)
        self.assertEqual(c.value(), 5)
        with self.assertRaises(ValueError):
            c.inc(-1)
        self.assertEqual(Counter.rate(1000, 1250, 10), 25.0)
        self.assertEqual(Counter.rate(1250, 40, 10), 4.0)  # a reset assumes restart from zero
        with self.assertRaises(ValueError):
            Counter.rate(0, 1, 0)

    def test_gauge(self):
        g = Gauge()
        g.set(10)
        g.inc()
        g.dec()
        g.dec()
        self.assertEqual(g.value(), 9)


class BucketLayoutTest(unittest.TestCase):
    def test_index_and_bounds_are_inverses_and_tile_the_range(self):
        h = LogLinearHistogram(7, MAX)
        inverse = contiguous = width_bound = True
        for i in range(h.bucket_count() - 1):
            lo, hi = h.lower_bound(i), h.upper_bound(i)
            if h.index_of(lo) != i or h.index_of(hi) != i:
                inverse = False
            if h.lower_bound(i + 1) != hi + 1:
                contiguous = False
            if lo >= 128 and (hi - lo + 1) * 128 > lo:
                width_bound = False
        self.assertTrue(inverse)
        self.assertTrue(contiguous)
        self.assertTrue(width_bound)
        self.assertTrue(h.index_of(0) == 0 and h.index_of(127) == 127 and h.upper_bound(127) == 127)
        self.assertTrue(h.lower_bound(128) == 128 and h.index_of(128) == 128)
        i = h.index_of(1_000_000)
        self.assertEqual(h.upper_bound(i) - h.lower_bound(i) + 1, 4096)
        self.assertEqual(h.index_of(MAX), h.bucket_count() - 1)

    def test_rejections(self):
        h = LogLinearHistogram(7, MAX)
        with self.assertRaises(ValueError):
            h.record(-1)
        with self.assertRaises(ValueError):
            h.record(MAX + 1)
        with self.assertRaises(ValueError):
            LogLinearHistogram(0, MAX)


class PercentileTest(unittest.TestCase):
    def test_small_exact_dataset(self):
        h = LogLinearHistogram(7, MAX)
        for v in range(1, 101):
            h.record(v)
        self.assertEqual(h.count(), 100)
        self.assertTrue(h.min() == 1 and h.max() == 100)
        self.assertEqual(h.mean(), 50.5)
        self.assertEqual(h.value(50), 50)
        self.assertEqual(h.value(99), 99)
        self.assertEqual(h.value(100), 100)
        self.assertEqual(h.value(0), 1)
        self.assertTrue(h.percentile(50).lower == 50 and h.percentile(50).upper == 50)
        self.assertEqual(h.fraction_at_or_below(25), 0.25)
        empty = LogLinearHistogram(7, MAX)
        self.assertTrue(empty.count() == 0 and empty.value(99) == 0 and empty.mean() == 0 and empty.min() == 0)
        with self.assertRaises(ValueError):
            h.percentile(101)

    def test_error_bound_holds_on_skewed_distribution(self):
        rng = Rng(2024)
        samples = authorization_latencies(100_000, 0.01, rng)
        h = LogLinearHistogram(7, MAX)
        for s in samples:
            h.record(s)
        ordered = sorted(samples)
        bracketed = bounded = True
        for p in (1, 10, 50, 90, 99, 99.9, 99.99):
            e = h.percentile(p)
            exact = exact_percentile(ordered, p)
            if exact < e.lower or exact > e.upper:
                bracketed = False
            if e.relative_error() > 1.0 / 128:
                bounded = False
        self.assertTrue(bracketed)
        self.assertTrue(bounded)
        self.assertLess(h.mean() * 10, h.value(99))  # averages lie
        self.assertTrue(11_000 < h.value(50) < 13_500)
        self.assertGreater(h.value(99), 1_000_000)
        sli = h.fraction_at_or_below(100_000)
        exact_at_or_below = sum(1 for v in ordered if v <= 100_000)
        self.assertLess(abs(sli - exact_at_or_below / 100_000.0), 0.001)
        fine = LogLinearHistogram(10, MAX)
        for s in samples:
            fine.record(s)
        self.assertTrue(fine.percentile(99).relative_error() <= 1.0 / 1024 and fine.bucket_count() > h.bucket_count())


class MergeTest(unittest.TestCase):
    def test_merge_is_exact_and_order_independent(self):
        r1, r2 = Rng(7), Rng(8)
        a = authorization_latencies(90_000, 0.0, r1)
        b = authorization_latencies(10_000, 0.10, r2)
        ha, hb, everything = LogLinearHistogram(7, MAX), LogLinearHistogram(7, MAX), LogLinearHistogram(7, MAX)
        for s in a:
            ha.record(s)
            everything.record(s)
        for s in b:
            hb.record(s)
            everything.record(s)
        merged = ha.copy()
        merged.merge(hb)
        self.assertEqual(merged.count(), 100_000)
        self.assertTrue(merged.value(99) == everything.value(99) and merged.value(50) == everything.value(50)
                        and merged.mean() == everything.mean())
        other = hb.copy()
        other.merge(ha)
        self.assertTrue(other.value(99) == merged.value(99) and other.min() == merged.min() and other.max() == merged.max())
        avg_of_p99 = (ha.value(99) + hb.value(99)) // 2
        self.assertGreater(abs(avg_of_p99 - merged.value(99)), merged.value(99) // 4)
        self.assertTrue(ha.value(99) < merged.value(99) < hb.value(99))
        self.assertTrue(ha.copy().count() == ha.count() and merged.count() != ha.count())
        with self.assertRaises(ValueError):
            ha.merge(LogLinearHistogram(8, MAX))


class RecordManyAndExportTest(unittest.TestCase):
    def test_record_many_and_cumulative(self):
        h = LogLinearHistogram(7, MAX)
        h.record_many(10, 5)
        h.record_many(1_000, 3)
        h.record_many(5_000_000, 2)
        self.assertTrue(h.count() == 10 and h.mean() == (50 + 3_000 + 10_000_000) / 10.0)
        cum = h.cumulative([10, 999, 1_003, 1_000_000])
        self.assertEqual(cum, [5, 5, 8, 8])
        with self.assertRaises(ValueError):
            h.record_many(1, 0)

    def test_registry_and_exposition_format(self):
        reg = MetricsRegistry(7, MAX)
        reg.counter("req").inc(3)
        reg.gauge("inflight").set(2)
        reg.histogram("lat").record(500)
        reg.histogram("lat").record(2_000)
        self.assertTrue(reg.counter("req") is reg.counter("req") and reg.histogram("lat").count() == 2)
        text = reg.render([1_000, 10_000])
        self.assertIn("# TYPE req_total counter\nreq_total 3\n", text)
        self.assertIn("# TYPE inflight gauge\ninflight 2\n", text)
        self.assertTrue('lat_bucket{le="1000"} 1\n' in text and 'lat_bucket{le="10000"} 2\n' in text
                        and 'lat_bucket{le="+Inf"} 2\n' in text)
        self.assertTrue("lat_sum 2500\n" in text and "lat_count 2\n" in text)


class RngTest(unittest.TestCase):
    def test_generator_is_deterministic(self):
        self.assertEqual(Rng(42).next_long(), -4767286540954276203)
        self.assertGreater(Rng(1).next_log_normal_micros(12_000, 0.5), 0)

    def test_parity_with_the_java_demo(self):
        h = LogLinearHistogram(7, MAX)
        for s in authorization_latencies(100_000, 0.01, Rng(2024)):
            h.record(s)
        self.assertEqual(h.value(99), 1_155_071)
        self.assertEqual(h.cumulative([100_000]), [98_956])


if __name__ == "__main__":
    unittest.main()
