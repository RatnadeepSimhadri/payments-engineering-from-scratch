"""Tests for phases/02-algorithms/13-interval-problems. Run: python3 -m unittest -v"""

import unittest

from main import EUR, USD, Hold, HoldBook, Interval, Intervals, Money, Peak, PeakExposure, sample_holds, t


class JavaRandom:
    """java.util.Random (48-bit LCG), so the 100 random instances are the same as the Java test's seed 13."""

    def __init__(self, seed: int) -> None:
        self._seed = (seed ^ 0x5DEECE66D) & ((1 << 48) - 1)

    def _next(self, bits: int) -> int:
        self._seed = (self._seed * 0x5DEECE66D + 0xB) & ((1 << 48) - 1)
        return self._seed >> (48 - bits)

    def next_int(self, bound: int) -> int:
        if bound & (bound - 1) == 0:
            return (bound * self._next(31)) >> 31
        while True:
            bits = self._next(31)
            val = bits % bound
            if bits - val + (bound - 1) < (1 << 31):
                return val


def iv(s, e):
    return Interval(s, e)


def brute_force_max(ivs):
    """Largest pairwise-disjoint subset by trying every subset (n <= 10)."""
    best = 0
    for mask in range(1 << len(ivs)):
        chosen = [ivs[i] for i in range(len(ivs)) if mask & (1 << i)]
        if all(not chosen[i].overlaps(chosen[j]) for i in range(len(chosen)) for j in range(i + 1, len(chosen))):
            best = max(best, len(chosen))
    return best


def brute_force_overlap(ivs):
    """Max number of intervals containing any single instant, by checking every instant."""
    return max(sum(1 for x in ivs if x.contains(tt)) for tt in range(40))


def brute_force_coverage(ivs):
    return sum(1 for tt in range(40) if any(x.contains(tt) for x in ivs))


WINDOWS = [iv(840, 930), iv(900, 960), iv(960, 1005), iv(540, 600), iv(1080, 1080)]
SLOTS = [iv(1, 4), iv(3, 5), iv(0, 6), iv(5, 7), iv(3, 9), iv(5, 9), iv(6, 10), iv(8, 11), iv(8, 12), iv(2, 14),
         iv(12, 16)]


class IntervalTypeTest(unittest.TestCase):
    def test_half_open_semantics(self):
        self.assertEqual(iv(3, 10).length(), 7)
        self.assertFalse(iv(0, 10).overlaps(iv(10, 20)))
        self.assertTrue(iv(0, 10).overlaps_or_touches(iv(10, 20)))
        self.assertTrue(iv(0, 10).overlaps(iv(5, 15)) and iv(5, 15).overlaps(iv(0, 10)))
        self.assertTrue(iv(0, 10).contains(0) and iv(0, 10).contains(9) and not iv(0, 10).contains(10))
        self.assertEqual(iv(0, 10).intersect(iv(5, 15)), iv(5, 10))
        self.assertTrue(iv(0, 5).intersect(iv(7, 9)).is_empty())
        self.assertTrue(iv(1, 5).compare_to(iv(1, 7)) < 0 and iv(2, 3).compare_to(iv(1, 9)) > 0)
        with self.assertRaises(ValueError):
            iv(5, 4)
        self.assertTrue(iv(5, 5).is_empty())


class MergeTest(unittest.TestCase):
    def test_merge(self):
        self.assertEqual(Intervals.merge(WINDOWS), [iv(540, 600), iv(840, 1005)])
        self.assertEqual(Intervals.merge([iv(0, 5), iv(5, 10)]), [iv(0, 10)])
        self.assertEqual(Intervals.merge([iv(0, 100), iv(10, 20)]), [iv(0, 100)])
        self.assertEqual(Intervals.merge([iv(50, 60), iv(0, 10), iv(5, 55)]), [iv(0, 60)])
        self.assertEqual(Intervals.merge([]), [])
        self.assertEqual(Intervals.merge([iv(3, 3), iv(7, 7)]), [])
        self.assertEqual(WINDOWS[0], iv(840, 930))  # input untouched
        merged = Intervals.merge(WINDOWS)
        self.assertTrue(all(merged[i - 1].end < merged[i].start for i in range(1, len(merged))))

    def test_coverage_and_gaps(self):
        self.assertEqual(Intervals.coverage(WINDOWS), 225)
        self.assertEqual(Intervals.gaps(WINDOWS, iv(480, 1140)), [iv(480, 540), iv(600, 840), iv(1005, 1140)])
        self.assertEqual(Intervals.gaps([iv(0, 10)], iv(2, 8)), [])
        self.assertEqual(Intervals.gaps([], iv(0, 10)), [iv(0, 10)])
        self.assertEqual(Intervals.gaps([iv(100, 200)], iv(0, 10)), [iv(0, 10)])


class SweepAndSchedulingTest(unittest.TestCase):
    def test_sweep_line(self):
        self.assertEqual(Intervals.max_overlap(WINDOWS), Peak(2, 900))
        self.assertEqual(Intervals.max_overlap([iv(0, 10), iv(10, 20)]).count, 1)  # touching counts as 1
        self.assertEqual(Intervals.max_overlap([iv(0, 10), iv(2, 8), iv(4, 6)]), Peak(3, 4))
        self.assertEqual(Intervals.max_overlap([]), Peak(0, 0))
        self.assertEqual(Intervals.max_overlap([iv(0, 5), iv(1, 5), iv(10, 15), iv(11, 15)]), Peak(2, 1))

    def test_scheduling(self):
        chosen = Intervals.max_non_overlapping(SLOTS)
        self.assertEqual(chosen, [iv(1, 4), iv(5, 7), iv(8, 11), iv(12, 16)])
        self.assertEqual(Intervals.max_overlap(chosen).count, 1)
        self.assertEqual(len(Intervals.max_non_overlapping([iv(0, 5), iv(5, 10)])), 2)
        self.assertEqual(len(Intervals.max_non_overlapping([iv(0, 100), iv(1, 2), iv(3, 4)])), 2)
        self.assertEqual(Intervals.max_non_overlapping([]), [])

    def test_random_instances_against_brute_force(self):
        rng = JavaRandom(13)
        optimal = True
        for _ in range(100):
            rand = []
            n = 1 + rng.next_int(10)
            for _ in range(n):
                s = rng.next_int(20)
                rand.append(iv(s, s + 1 + rng.next_int(8)))
            optimal &= len(Intervals.max_non_overlapping(rand)) == brute_force_max(rand)
            optimal &= Intervals.max_overlap(rand).count == brute_force_overlap(rand)
            optimal &= Intervals.coverage(rand) == brute_force_coverage(rand)
        self.assertTrue(optimal, "greedy size, sweep peak, and coverage match brute force")

    def test_stabbing(self):
        self.assertEqual(Intervals.stabbing(SLOTS, 5), [iv(0, 6), iv(2, 14), iv(3, 9), iv(5, 7), iv(5, 9)])


class HoldBookTest(unittest.TestCase):
    def test_exposure(self):
        book = sample_holds()
        self.assertEqual(book.exposure_at(t(12, 30)), Money.of_minor(83_000, USD))
        self.assertEqual(book.exposure_at(t(17, 0)), Money.of_minor(600, USD))  # half-open: hotel gone, coffee on
        self.assertEqual(book.exposure_at(0), Money.zero(USD))
        peak = book.peak_exposure()
        self.assertEqual(peak, PeakExposure(Money.of_minor(87_500, USD), t(11, 0), 3))
        self.assertEqual(book.exposure_at(peak.at), peak.amount)
        self.assertEqual(book.busy_periods(), [iv(t(9, 0), t(17, 15))])
        self.assertEqual([h.id for h in book.expired_by(t(14, 0))], ["fuel", "restaurant", "parking"])

    def test_validation(self):
        with self.assertRaises(ValueError):
            Hold("x", Money.zero(USD), iv(0, 1))
        with self.assertRaises(ValueError):
            HoldBook(USD).add(Hold("x", Money.of_minor(1, EUR), iv(0, 1)))
        self.assertEqual(HoldBook(USD).peak_exposure(), PeakExposure(Money.zero(USD), 0, 0))
        self.assertEqual(str(Money.of_minor(87_500, USD)), "875.00 USD")
        with self.assertRaises(TypeError):  # Python-only guard: a float amount fails at construction
            Money(875.0, USD)


if __name__ == "__main__":
    unittest.main()
