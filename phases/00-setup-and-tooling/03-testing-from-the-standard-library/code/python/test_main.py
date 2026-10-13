"""Tests for phases/00-setup-and-tooling/03-testing-from-the-standard-library. Run: python3 -m unittest -v

This file is the lesson's Python harness: unittest.TestCase classes, assertEqual / assertRaises / assertTrue,
subTest for table-driven cases, and setUp for shared fixtures. `python3 -m unittest` discovers test_*.py
files and test_* methods by name; a failing assertion raises, the runner counts it, and the process exits 1.
"""
import threading
import unittest

from main import Deadline, FakeClock, SeededRandom, backoff_millis


class SeededRandomTest(unittest.TestCase):
    def test_seed_42_sequence_matches_java_and_go(self):
        rng = SeededRandom(42)
        expected = [-4767286540954276203, 2949826092126892291, 5139283748462763858, 6349198060258255764, 701532786141963250]
        self.assertEqual([rng.next_long() for _ in range(5)], expected)

    def test_published_splitmix64_reference_vector(self):
        rng = SeededRandom(1234567)
        self.assertEqual(rng.next_long(), 6457827717110365317)
        self.assertEqual(rng.next_long(), 3203168211198807973)

    def test_same_seed_same_sequence_different_seed_differs(self):
        self.assertEqual(SeededRandom(99).next_long(), SeededRandom(99).next_long())
        self.assertNotEqual(SeededRandom(1).next_long(), SeededRandom(2).next_long())
        self.assertEqual(SeededRandom(42).seed, 42)

    def test_next_int_sequence_and_range(self):
        rng = SeededRandom(42)
        self.assertEqual([rng.next_int(100) for _ in range(8)], [51, 53, 71, 35, 65, 97, 82, 63])
        rng = SeededRandom(42)
        for _ in range(10_000):
            self.assertTrue(0 <= rng.next_int(7) < 7)

    def test_next_int_rejects_non_positive_bound(self):
        with self.assertRaises(ValueError):
            SeededRandom(1).next_int(0)

    def test_next_double_range_and_first_value(self):
        rng = SeededRandom(42)
        self.assertAlmostEqual(rng.next_double(), 0.7415648787718233, places=15)
        for _ in range(10_000):
            self.assertTrue(0.0 <= rng.next_double() < 1.0)

    def test_shuffle_is_deterministic_and_a_permutation(self):
        deck = list(range(10))
        SeededRandom(7).shuffle(deck)
        self.assertEqual(deck, [3, 2, 1, 6, 7, 5, 4, 8, 9, 0])
        self.assertEqual(sorted(deck), list(range(10)))


class FakeClockTest(unittest.TestCase):
    def setUp(self):
        self.clock = FakeClock(1_000)

    def test_starts_where_told(self):
        self.assertEqual(self.clock.now_millis(), 1_000)

    def test_advance_adds_and_zero_is_allowed(self):
        self.clock.advance(500)
        self.assertEqual(self.clock.now_millis(), 1_500)
        self.clock.advance(0)
        self.assertEqual(self.clock.now_millis(), 1_500)

    def test_advance_to_sets_and_same_instant_is_allowed(self):
        self.clock.advance_to(2_000)
        self.clock.advance_to(2_000)
        self.assertEqual(self.clock.now_millis(), 2_000)

    def test_never_goes_backwards(self):
        self.clock.advance_to(2_000)
        with self.assertRaises(ValueError):
            self.clock.advance(-1)
        with self.assertRaises(ValueError):
            self.clock.advance_to(1_999)
        self.assertEqual(self.clock.now_millis(), 2_000, "rejected moves leave the clock untouched")

    def test_clocks_are_independent(self):
        self.assertEqual(FakeClock(5).now_millis(), 5)
        self.assertEqual(FakeClock(6).now_millis(), 6)

    def test_concurrent_advances_are_not_lost(self):
        shared = FakeClock(0)
        threads, per_thread = 8, 1_000

        def worker():
            for _ in range(per_thread):
                shared.advance(1)

        pool = [threading.Thread(target=worker) for _ in range(threads)]
        for t in pool:
            t.start()
        for t in pool:
            t.join()
        self.assertEqual(shared.now_millis(), threads * per_thread)


class DeadlineTest(unittest.TestCase):
    def test_expiry_without_sleeping(self):
        clock = FakeClock(10_000)
        deadline = Deadline(clock, 10_000 + 30_000)
        self.assertFalse(deadline.expired())
        self.assertEqual(deadline.remaining_millis(), 30_000)
        clock.advance(29_999)
        self.assertFalse(deadline.expired())
        clock.advance(1)
        self.assertTrue(deadline.expired(), "expired exactly at the deadline (>=)")
        clock.advance(5_000)
        self.assertEqual(deadline.remaining_millis(), 0, "remaining never negative")


class BackoffTest(unittest.TestCase):
    def test_sequence_for_seed_42(self):
        rng = SeededRandom(42)
        self.assertEqual([backoff_millis(a, 100, 2000, rng) for a in range(5)], [51, 153, 171, 735, 365])

    def test_never_exceeds_cap_even_at_large_attempt(self):
        rng = SeededRandom(3)
        for attempt in range(200):
            with self.subTest(attempt=attempt):
                self.assertLess(backoff_millis(attempt, 100, 2000, rng), 2000)

    def test_attempt_zero_stays_inside_base(self):
        rng = SeededRandom(5)
        for _ in range(1_000):
            self.assertLess(backoff_millis(0, 100, 2000, rng), 100)

    def test_rejects_bad_arguments(self):
        with self.assertRaises(ValueError):
            backoff_millis(-1, 100, 2000, SeededRandom(1))
        with self.assertRaises(ValueError):
            backoff_millis(0, 100, 50, SeededRandom(1))


class HarnessSelfTest(unittest.TestCase):
    def test_assert_raises_notices_a_missing_exception(self):
        with self.assertRaises(AssertionError):
            with self.assertRaises(ValueError):
                pass  # no exception: assertRaises itself must fail


if __name__ == "__main__":
    unittest.main()
