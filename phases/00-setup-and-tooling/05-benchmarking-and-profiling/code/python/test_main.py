"""Tests for phases/00-setup-and-tooling/05-benchmarking-and-profiling. Run: python3 -m unittest -v"""
import unittest

from main import (Benchmark, BenchmarkResult, Blackhole, FakeTicker, Harness, HarnessConfig, LatencyRecorder,
                  SampleProfile, Stats, seeded_ints)

TEN = [10, 20, 30, 40, 50, 60, 70, 80, 90, 100]


class StatsTest(unittest.TestCase):
    def test_nearest_rank_percentiles(self):
        self.assertEqual(Stats.percentile(TEN, 50), 50)  # the 5th value, not interpolated
        self.assertEqual(Stats.percentile(TEN, 90), 90)
        self.assertEqual(Stats.percentile(TEN, 99), 100)
        self.assertEqual(Stats.percentile(TEN, 0), 10)
        self.assertEqual(Stats.percentile(TEN, 100), 100)

    def test_percentile_does_not_mutate_input(self):
        ten = list(TEN)
        Stats.percentile(ten, 50)
        self.assertEqual(ten[0], 10)
        self.assertEqual(ten[9], 100)
        unsorted = [90, 10, 50]
        self.assertEqual(Stats.percentile(unsorted, 50), 50)
        self.assertEqual(unsorted[0], 90)

    def test_single_sample(self):
        self.assertEqual(Stats.percentile([7], 99), 7)
        self.assertEqual(Stats.percentile([7], 1), 7)

    def test_mean_stddev_cv(self):
        self.assertEqual(Stats.mean(TEN), 55.0)
        self.assertEqual(Stats.stddev([5, 5, 5]), 0.0)
        self.assertEqual(Stats.coefficient_of_variation([5, 5, 5]), 0.0)
        self.assertLess(abs(Stats.coefficient_of_variation([10, 20]) - Stats.coefficient_of_variation([1000, 2000])), 1e-12)

    def test_percentile_rejects_bad_input(self):
        with self.assertRaises(ValueError):
            Stats.percentile([], 50)
        with self.assertRaises(ValueError):
            Stats.percentile(TEN, 101)


class HarnessTest(unittest.TestCase):
    def test_scripted_single_fork(self):
        # 1 fork, 2 warm-up, 3 measured, 10 ops/iteration -> 10 readings. Durations: 1000, 900 | 100, 200, 300.
        ticker = FakeTicker(0, 1000, 1000, 1900, 1900, 2000, 2000, 2200, 2200, 2500)
        harness = Harness(ticker, HarnessConfig(2, 3, 1, 10))
        calls = [0]

        def body(bh):
            calls[0] += 1
            bh.consume(calls[0])

        result = harness.run(Benchmark("scripted", body))
        self.assertEqual(calls[0], (2 + 3) * 10)
        self.assertEqual(ticker.readings_consumed(), 10)
        fork = result.forks[0]
        self.assertEqual(fork.warmup_ns_per_op, [100, 90])
        self.assertEqual(fork.measured_ns_per_op, [10, 20, 30])
        self.assertEqual(len(result.pooled_ns_per_op), 3)
        self.assertEqual(result.min(), 10)
        self.assertEqual(result.max(), 30)
        self.assertEqual(result.p50(), 20)
        self.assertNotEqual(harness.blackhole().value(), 0)
        self.assertEqual(result.fork_spread(), 0.0)
        self.assertTrue(result.stable())

    def test_forks_that_disagree_are_flagged(self):
        two = FakeTicker(0, 100, 100, 200,      # fork 1: measured 100 ns/op
                         200, 300, 300, 1000)   # fork 2: measured 700
        result = Harness(two, HarnessConfig(1, 1, 2, 1)).run(Benchmark("drift", lambda bh: bh.consume(1)))
        self.assertEqual(result.fork_medians(), [100, 700])
        self.assertFalse(result.stable())
        self.assertGreater(result.fork_spread(), BenchmarkResult.UNSTABLE_CV)
        self.assertIn("UNSTABLE", result.row())
        self.assertEqual(len(result.pooled_ns_per_op), 2)

    def test_ticker_failures_are_loud(self):
        with self.assertRaises(RuntimeError):
            Harness(FakeTicker(0, 10), HarnessConfig(0, 2, 1, 1)).run(Benchmark("short", lambda bh: bh.consume(1)))
        with self.assertRaises(RuntimeError):
            Harness(FakeTicker(500, 400), HarnessConfig(0, 1, 1, 1)).run(Benchmark("back", lambda bh: bh.consume(1)))

    def test_config_and_benchmark_validation(self):
        with self.assertRaises(ValueError):
            HarnessConfig(1, 0, 1, 1)
        with self.assertRaises(ValueError):
            HarnessConfig(1, 1, 0, 1)
        with self.assertRaises(ValueError):
            Benchmark(" ", lambda bh: None)


class BlackholeTest(unittest.TestCase):
    def test_xor_fold(self):
        bh = Blackhole()
        bh.consume(0b1010)
        self.assertEqual(bh.value(), 0b1010)
        bh.consume(0b1010)
        self.assertEqual(bh.value(), 0)
        bh.consume(None)
        self.assertEqual(bh.value(), 0)


class CoordinatedOmissionTest(unittest.TestCase):
    def test_stall_back_fills(self):
        rec = LatencyRecorder()
        rec.record(5_000, 1_000)
        self.assertEqual(rec.raw_count(), 1)
        self.assertEqual(rec.corrected_count(), 5)
        corrected = rec.corrected_samples()
        self.assertEqual(corrected[0], 5_000)
        self.assertEqual(corrected[1], 4_000)
        self.assertEqual(corrected[4], 1_000)

    def test_boundaries(self):
        fine = LatencyRecorder()
        fine.record(999, 1_000)
        self.assertEqual(fine.corrected_count(), 1)
        exact = LatencyRecorder()
        exact.record(2_000, 1_000)
        self.assertEqual(exact.corrected_count(), 2)
        self.assertEqual(exact.corrected_samples()[1], 1_000)

    def test_raw_p99_hides_stall(self):
        load = LatencyRecorder()
        for _ in range(999):
            load.record(1_000, 1_000)
        load.record(1_000_000, 1_000)
        self.assertEqual(Stats.percentile(load.raw_samples(), 99), 1_000)
        self.assertGreater(Stats.percentile(load.corrected_samples(), 99), 900_000)
        self.assertEqual(load.corrected_count(), 1000 + 999)

    def test_rejects_non_positive_interval(self):
        with self.assertRaises(ValueError):
            LatencyRecorder().record(1, 0)


class SampleProfileTest(unittest.TestCase):
    def test_self_and_inclusive(self):
        profile = SampleProfile()
        profile.record("main", "a", "b")
        profile.record("main", "a", "b")
        profile.record("main", "a", "c")
        profile.record("main", "d")
        self.assertEqual(profile.samples(), 4)
        self.assertEqual(profile.self_count("b"), 2)
        self.assertEqual(profile.self_count("a"), 0)
        self.assertEqual(profile.self_count("main"), 0)
        self.assertEqual(profile.inclusive_count("a"), 3)
        self.assertEqual(profile.inclusive_count("main"), 4)
        self.assertEqual(profile.top_self(3)[0][0], "b")
        self.assertEqual(profile.top_self(3)[1][0], "c")  # tie with d broken by name
        self.assertEqual(profile.top_inclusive(1)[0][0], "main")

    def test_recursion_counted_once(self):
        recursive = SampleProfile()
        recursive.record("main", "f", "f", "f")
        self.assertEqual(recursive.inclusive_count("f"), 1)
        self.assertEqual(recursive.self_count("f"), 1)

    def test_empty_stack_rejected(self):
        with self.assertRaises(ValueError):
            SampleProfile().record()


class SeededTest(unittest.TestCase):
    def test_seeded_ints_deterministic_and_java_compatible(self):
        self.assertEqual(seeded_ints(100, 42), seeded_ints(100, 42))
        # First values of java.util.Random(42).nextInt(1000), checked against the JDK.
        self.assertEqual(seeded_ints(1000, 42)[:5], [130, 763, 248, 884, 970])


if __name__ == "__main__":
    unittest.main()
