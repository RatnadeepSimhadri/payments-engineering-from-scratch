"""Tests for phases/03-systems-data-structures/11-rate-limiter-algorithms. Run: python3 -m unittest -v"""

import unittest

from main import (
    Decision,
    FixedWindow,
    LeakyBucket,
    ManualClock,
    SlidingWindowCounter,
    SlidingWindowLog,
    TokenBucket,
    make_limiters,
    max_in_window,
    replay,
    retry_storm_trace,
)


class DecisionTest(unittest.TestCase):
    def test_retry_after_header_rounds_up_to_whole_seconds(self) -> None:
        self.assertEqual(Decision.deny(1).retry_after_header(), 1)
        self.assertEqual(Decision.deny(1000).retry_after_header(), 1)
        self.assertEqual(Decision.deny(1001).retry_after_header(), 2)
        self.assertEqual(Decision.allow().retry_after_header(), 0)

    def test_deny_never_reports_zero_wait(self) -> None:
        self.assertEqual(Decision.deny(0).retry_after_millis, 1)
        self.assertFalse(Decision.deny(5).allowed)
        self.assertTrue(Decision.allow(250).allowed)
        self.assertEqual(Decision.allow(250).delay_millis, 250)


class TokenBucketTest(unittest.TestCase):
    def test_starts_full_and_admits_a_burst_up_to_capacity(self) -> None:
        tb = TokenBucket(capacity=5, refill_per_second=2, clock=ManualClock())
        results = [tb.try_acquire().allowed for _ in range(7)]
        self.assertEqual(results, [True] * 5 + [False] * 2)

    def test_refills_at_the_configured_rate_and_caps_at_capacity(self) -> None:
        clock = ManualClock()
        tb = TokenBucket(capacity=5, refill_per_second=2, clock=clock)
        for _ in range(5):
            tb.try_acquire()
        clock.advance(499)
        self.assertFalse(tb.try_acquire().allowed)   # 0.998 tokens
        clock.advance(1)
        self.assertTrue(tb.try_acquire().allowed)    # exactly 1.0 token at 500 ms
        clock.advance(60_000)
        self.assertEqual(tb.tokens(), 5.0)           # never above capacity

    def test_retry_after_is_time_to_the_next_token(self) -> None:
        clock = ManualClock()
        tb = TokenBucket(capacity=1, refill_per_second=4, clock=clock)
        tb.try_acquire()
        d = tb.try_acquire()
        self.assertEqual(d.retry_after_millis, 250)
        clock.advance(250)
        self.assertTrue(tb.try_acquire().allowed)

    def test_rejects_bad_parameters(self) -> None:
        with self.assertRaises(ValueError):
            TokenBucket(0, 1, ManualClock())
        with self.assertRaises(ValueError):
            TokenBucket(1, 0, ManualClock())


class LeakyBucketTest(unittest.TestCase):
    def test_spaces_output_at_the_leak_rate(self) -> None:
        lb = LeakyBucket(queue_capacity=3, leak_per_second=10, clock=ManualClock())
        delays = [lb.try_acquire().delay_millis for _ in range(3)]
        self.assertEqual(delays, [0, 100, 200])
        d = lb.try_acquire()
        self.assertFalse(d.allowed)
        self.assertEqual(d.retry_after_millis, 100)
        self.assertEqual(lb.queued(), 3)

    def test_drains_as_time_passes(self) -> None:
        clock = ManualClock()
        lb = LeakyBucket(queue_capacity=3, leak_per_second=10, clock=clock)
        for _ in range(3):
            lb.try_acquire()
        clock.advance(100)
        d = lb.try_acquire()
        self.assertTrue(d.allowed)
        self.assertEqual(d.delay_millis, 200)
        clock.advance(10_000)
        self.assertEqual(lb.queued(), 0)
        self.assertEqual(lb.try_acquire().delay_millis, 0)

    def test_rejects_rates_that_do_not_divide_a_second(self) -> None:
        with self.assertRaises(ValueError):
            LeakyBucket(3, 7, ManualClock())
        with self.assertRaises(ValueError):
            LeakyBucket(0, 10, ManualClock())


class FixedWindowTest(unittest.TestCase):
    def test_counts_reset_at_the_boundary(self) -> None:
        clock = ManualClock()
        fw = FixedWindow(limit=2, window_millis=1000, clock=clock)
        self.assertTrue(fw.try_acquire().allowed)
        self.assertTrue(fw.try_acquire().allowed)
        d = fw.try_acquire()
        self.assertFalse(d.allowed)
        self.assertEqual(d.retry_after_millis, 1000)
        clock.set(999)
        self.assertEqual(fw.try_acquire().retry_after_millis, 1)
        clock.set(1000)
        self.assertTrue(fw.try_acquire().allowed)

    def test_admits_double_the_limit_across_a_boundary(self) -> None:
        # The failure the lesson warns about.
        clock = ManualClock()
        fw = FixedWindow(limit=10, window_millis=1000, clock=clock)
        admitted = 0
        for t in list(range(950, 1000, 5)) + list(range(1000, 1050, 5)):
            clock.set(t)
            admitted += fw.try_acquire().allowed
        self.assertEqual(admitted, 20)


class SlidingWindowLogTest(unittest.TestCase):
    def test_exactly_limit_in_any_window(self) -> None:
        clock = ManualClock()
        log = SlidingWindowLog(limit=10, window_millis=1000, clock=clock)
        admitted = 0
        for t in list(range(950, 1000, 5)) + list(range(1000, 1050, 5)):
            clock.set(t)
            admitted += log.try_acquire().allowed
        self.assertEqual(admitted, 10)

    def test_retry_after_is_when_the_oldest_entry_leaves(self) -> None:
        clock = ManualClock()
        log = SlidingWindowLog(limit=2, window_millis=1000, clock=clock)
        clock.set(100)
        log.try_acquire()
        clock.set(400)
        log.try_acquire()
        clock.set(500)
        d = log.try_acquire()
        self.assertFalse(d.allowed)
        self.assertEqual(d.retry_after_millis, 600)
        clock.set(1100)
        self.assertTrue(log.try_acquire().allowed)
        self.assertEqual(len(log), 2)

    def test_entry_at_exactly_window_age_has_left(self) -> None:
        clock = ManualClock()
        log = SlidingWindowLog(limit=1, window_millis=1000, clock=clock)
        log.try_acquire()
        clock.set(999)
        self.assertFalse(log.try_acquire().allowed)
        clock.set(1000)
        self.assertTrue(log.try_acquire().allowed)


class SlidingWindowCounterTest(unittest.TestCase):
    def test_weights_the_previous_window(self) -> None:
        clock = ManualClock()
        sc = SlidingWindowCounter(limit=10, window_millis=1000, clock=clock)
        for t in range(0, 1000, 100):
            clock.set(t)
            sc.try_acquire()
        clock.set(1250)
        self.assertEqual(sc.weighted_count_scaled(), 7500)  # 10 * 0.75 = 7.5
        self.assertTrue(sc.try_acquire().allowed)
        clock.set(1500)
        self.assertEqual(sc.weighted_count_scaled(), 6000)  # 10 * 0.5 + 1
        self.assertTrue(sc.try_acquire().allowed)

    def test_denies_at_the_boundary_when_previous_window_was_full(self) -> None:
        clock = ManualClock()
        sc = SlidingWindowCounter(limit=10, window_millis=1000, clock=clock)
        for t in range(0, 1000, 100):
            clock.set(t)
            sc.try_acquire()
        clock.set(1000)
        d = sc.try_acquire()
        self.assertFalse(d.allowed)
        self.assertEqual(d.retry_after_millis, 100)  # 10*(1000-e) + 1000 <= 10000 needs e >= 100

    def test_a_gap_clears_the_previous_window(self) -> None:
        clock = ManualClock()
        sc = SlidingWindowCounter(limit=2, window_millis=1000, clock=clock)
        sc.try_acquire()
        sc.try_acquire()
        clock.set(5000)
        self.assertEqual(sc.weighted_count_scaled(), 0)
        self.assertTrue(sc.try_acquire().allowed)


class TraceTest(unittest.TestCase):
    def test_trace_shape(self) -> None:
        trace = retry_storm_trace()
        self.assertEqual(len(trace), 99)
        self.assertEqual(trace, sorted(trace))
        self.assertEqual(sum(1 for t in trace if 3000 <= t < 3020), 40)

    def test_max_in_window(self) -> None:
        self.assertEqual(max_in_window([0, 100, 999, 1000], 1000), 3)
        self.assertEqual(max_in_window([], 1000), 0)

    def test_retry_storm_summary_matches_java_and_go(self) -> None:
        expected = {
            "token bucket": (69, 30, 10, 19, 95, 0),
            "leaky bucket": (69, 30, 11, 10, 195, 995),
            "fixed window": (60, 39, 10, 19, 995, 0),
            "sliding log": (60, 39, 1, 10, 100, 0),
            "sliding counter": (55, 44, 1, 10, 112, 0),
        }
        trace = retry_storm_trace()
        for limiter, clock in make_limiters():
            s = replay(limiter, clock, trace, 3000, 3020)
            got = (s.allowed, s.denied, s.allowed_in_burst, s.max_in_any_second, s.max_retry_after_millis, s.max_delay_millis)
            self.assertEqual(got, expected[s.name], s.name)

    def test_only_the_windowed_limiters_hold_the_one_second_ceiling(self) -> None:
        trace = retry_storm_trace()
        for limiter, clock in make_limiters():
            s = replay(limiter, clock, trace, 3000, 3020)
            if s.name in ("token bucket", "fixed window"):
                self.assertGreater(s.max_in_any_second, 10, s.name)
            else:
                self.assertEqual(s.max_in_any_second, 10, s.name)


if __name__ == "__main__":
    unittest.main()
