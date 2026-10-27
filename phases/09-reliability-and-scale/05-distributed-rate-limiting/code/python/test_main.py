"""Tests for phases/09-reliability-and-scale/05-distributed-rate-limiting. Run: python3 -m unittest -v"""
import unittest

from main import (CAPACITY, RATE, CentralizedLimiter, FakeClock, LocalSyncLimiter, SharedStore,
                  SlidingWindowLogLimiter, TokenBucket, compare)


class TokenBucketTest(unittest.TestCase):
    def test_arithmetic(self):
        clock = FakeClock(0)
        bucket = TokenBucket(10, 5, clock.now_millis())
        burst = 0
        while bucket.try_acquire(1, clock.now_millis()):
            burst += 1
        self.assertEqual(burst, 10)
        self.assertFalse(bucket.try_acquire(1, clock.now_millis()))
        clock.advance(100)
        self.assertEqual(bucket.available_tokens(clock.now_millis()), 0)  # 500 milli-tokens accrue, 0 whole
        clock.advance(100)
        self.assertEqual(bucket.available_tokens(clock.now_millis()), 1)
        clock.advance(60_000)
        self.assertEqual(bucket.available_tokens(clock.now_millis()), 10)  # clamps at capacity
        self.assertTrue(bucket.try_acquire(10, clock.now_millis()) and not bucket.try_acquire(1, clock.now_millis()))

    def test_guards(self):
        with self.assertRaises(ValueError):
            TokenBucket(0, 1, 0)
        with self.assertRaises(ValueError):
            FakeClock(0).advance(-1)


class CentralizedTest(unittest.TestCase):
    def test_exact_across_nodes(self):
        store = SharedStore(3)
        central = CentralizedLimiter(store, 100, 100)
        admitted = sum(1 for i in range(400) if central.try_acquire(i % 4, "k", 0).allowed)
        self.assertEqual(admitted, 100)
        self.assertEqual(central.try_acquire(0, "k", 0).latency_millis, 3)
        self.assertEqual(store.calls(), 401)
        admitted = sum(1 for i in range(400) if central.try_acquire(i % 4, "k", 500).allowed)
        self.assertEqual(admitted, 50)
        self.assertTrue(central.try_acquire(0, "other", 500).allowed)


class SlidingWindowLogTest(unittest.TestCase):
    def test_exact_over_window_no_burst(self):
        log_store = SharedStore(1)
        log = SlidingWindowLogLimiter(log_store, 5, 1000)
        admitted = sum(1 for t in range(10) if log.try_acquire(t % 2, "k", t * 100).allowed)
        self.assertEqual(admitted, 5)
        self.assertEqual(log.log_size("k"), 5)
        self.assertTrue(log.try_acquire(0, "k", 1000).allowed)
        self.assertFalse(log.try_acquire(0, "k", 1000).allowed)  # window [1, 1000] still holds 5


class LocalSyncTest(unittest.TestCase):
    def test_cheap_and_approximate(self):
        sync_store = SharedStore(2)
        local = LocalSyncLimiter(sync_store, 4, 100, 100, 1000)
        self.assertEqual(local.try_acquire(0, "k", 0).latency_millis, 0)
        admitted = sum(1 for _ in range(200) if local.try_acquire(0, "k", 0).allowed)
        self.assertEqual(admitted, 24)  # 1/N share is 25 tokens, one already spent
        self.assertTrue(local.try_acquire(1, "k", 0).allowed)
        for _ in range(50):
            local.try_acquire(0, "k", 999)
        self.assertEqual(local.syncs(), 0)
        local.try_acquire(0, "k", 1000)
        self.assertEqual(local.syncs(), 1)
        self.assertGreater(local.rate_of("k", 0), 25)
        self.assertEqual(local.rate_of("k", 2), 6)
        total = sum(local.rate_of("k", n) for n in range(4))
        self.assertLessEqual(total, 100)

    def test_rejects_zero_nodes(self):
        with self.assertRaises(ValueError):
            LocalSyncLimiter(SharedStore(1), 0, 100, 100, 1000)


class SeededLoadTest(unittest.TestCase):
    def test_measured_trade_off(self):
        even = compare(400, 0.25, 7)
        skew = compare(400, 0.85, 7)
        central_even, local_even, log_even = even[0], even[1], even[3]
        central_skew, local_slow, local_fast, log_skew = skew[0], skew[1], skew[2], skew[3]
        self.assertLessEqual(central_even.peak_per_second(), RATE + CAPACITY)
        self.assertTrue(log_even.peak_per_second() <= RATE and log_skew.peak_per_second() <= RATE)
        self.assertLessEqual(abs(central_even.versus_ideal(RATE, CAPACITY)), 1)
        self.assertLess(local_slow.admitted, central_skew.admitted)
        self.assertGreater(local_fast.admitted, local_slow.admitted)
        self.assertGreater(local_fast.store_calls, local_slow.store_calls)
        self.assertLess(local_even.mean_latency_millis(), central_even.mean_latency_millis())
        self.assertTrue(central_even.offered == local_even.offered == log_even.offered)
        again = compare(400, 0.85, 7)
        self.assertTrue(again[1].admitted == local_slow.admitted and again[0].rejected == central_skew.rejected)
        light = compare(60, 0.25, 7)
        self.assertTrue(all(r.rejected == 0 for r in light))

    def test_java_random_parity_with_the_docs_table(self):
        even = compare(400, 0.25, 7)
        self.assertEqual([r.offered for r in even], [3893] * 4)
        self.assertEqual([r.admitted for r in even], [1099, 1079, 1078, 1000])


if __name__ == "__main__":
    unittest.main()
