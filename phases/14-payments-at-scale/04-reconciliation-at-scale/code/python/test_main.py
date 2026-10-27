"""Tests for phases/14-payments-at-scale/04-reconciliation-at-scale. Run: python3 -m unittest -v"""
import unittest
from datetime import date

from main import (
    USD, AgeBand, BreakType, BucketDigest, FeedGenerator, JavaRandom, Money, Rec, ReconciliationReport, Side,
    StreamingReconciler,
)

AS_OF = date(2026, 3, 31)


def inn(ref: str, day: str, cents: int) -> Rec:
    return Rec(ref, date.fromisoformat(day), date.fromisoformat(day), Money.of_minor(cents, USD), Side.INTERNAL)


def pr(ref: str, tx_date: str, cents_or_value, cents: int = None) -> Rec:
    if cents is None:
        return Rec(ref, date.fromisoformat(tx_date), date.fromisoformat(tx_date), Money.of_minor(cents_or_value, USD), Side.PROCESSOR)
    return Rec(ref, date.fromisoformat(tx_date), date.fromisoformat(cents_or_value), Money.of_minor(cents, USD), Side.PROCESSOR)


def run(a: list, b: list, tolerance: int) -> ReconciliationReport:
    x, y = list(a), list(b)
    FeedGenerator.sort_feed(x)
    FeedGenerator.sort_feed(y)
    return StreamingReconciler(USD, tolerance).reconcile(iter(x), iter(y), AS_OF)


def count(r: ReconciliationReport, t: BreakType) -> int:
    return r.count_by_type().get(t, 0)


class DigestTest(unittest.TestCase):
    def test_digest(self):
        d1 = BucketDigest.EMPTY.add(inn("A", "2026-03-01", 100)).add(inn("B", "2026-03-01", 100))
        d2 = BucketDigest.EMPTY.add(inn("B", "2026-03-01", 100)).add(inn("A", "2026-03-01", 100))
        self.assertEqual(d1, d2, "digest is order independent")
        self.assertNotEqual(d1, BucketDigest.EMPTY.add(inn("A", "2026-03-01", 100)).add(inn("C", "2026-03-01", 100)))
        self.assertEqual(d1.count, 2)
        self.assertEqual(d1.total_minor, 200)

    def test_mix_wraps_to_64_bits(self):
        """The Java finaliser relies on long overflow; the masked port must stay inside 64 bits and be stable."""
        h = BucketDigest.mix("TXGJDI6L")
        self.assertTrue(0 <= h < 2 ** 64)
        self.assertEqual(h, BucketDigest.mix("TXGJDI6L"))


class MatchingTest(unittest.TestCase):
    def test_identical_feeds_match_on_fast_path(self):
        a = [inn("A", "2026-03-01", 1999), inn("B", "2026-03-01", 1999), inn("C", "2026-03-02", 500)]
        b = [pr("A", "2026-03-01", 1999), pr("B", "2026-03-01", 1999), pr("C", "2026-03-02", 500)]
        r = run(a, b, 2)
        self.assertEqual(r.matched_count, 3)
        self.assertEqual(r.breaks, [])
        self.assertEqual(r.buckets_fast_matched, 2)
        self.assertEqual(r.buckets_inspected, 0)
        self.assertEqual(r.internal_total, r.processor_total)
        self.assertEqual(r.matched_total.minor_units, 4498)
        self.assertTrue(r.ties_out())

    def test_missing_sides(self):
        r = run([inn("A", "2026-03-01", 100)], [], 2)
        self.assertEqual(count(r, BreakType.MISSING_AT_PROCESSOR), 1)
        self.assertIsNone(r.breaks[0].processor)
        self.assertTrue(r.ties_out())
        self.assertEqual(r.internal_in_breaks().minor_units, 100)
        r = run([], [pr("Z", "2026-03-01", 4200)], 2)
        self.assertEqual(count(r, BreakType.MISSING_INTERNAL), 1)
        self.assertEqual(r.processor_in_breaks().minor_units, 4200)
        self.assertTrue(r.ties_out())

    def test_amount_mismatch(self):
        r = run([inn("C", "2026-03-02", 1250)], [pr("C", "2026-03-02", 1200)], 2)
        self.assertEqual(count(r, BreakType.AMOUNT_MISMATCH), 1)
        self.assertEqual(r.breaks[0].internal.amount.minor_units, 1250)
        self.assertEqual(r.breaks[0].processor.amount.minor_units, 1200)
        self.assertTrue(r.ties_out())
        self.assertEqual(r.matched_count, 0)

    def test_timing(self):
        r = run([inn("B", "2026-03-01", 5000)], [pr("B", "2026-03-01", "2026-03-02", 5000)], 2)
        self.assertEqual(r.matched_count, 1, "one day lag inside tolerance is matched on the fast path")
        self.assertEqual(r.breaks, [])
        self.assertEqual(r.buckets_fast_matched, 1)
        self.assertTrue(r.ties_out())
        r = run([inn("B", "2026-03-01", 5000)], [pr("B", "2026-03-01", "2026-03-06", 5000)], 2)
        self.assertEqual(count(r, BreakType.TIMING), 1, "five day lag is a TIMING break")
        self.assertTrue(r.ties_out())
        self.assertEqual(r.processor_in_breaks().minor_units, 5000)
        r = run([inn("B", "2026-03-01", 5000)], [pr("B", "2026-03-01", "2026-03-02", 5000)], 0)
        self.assertEqual(count(r, BreakType.TIMING), 1, "zero tolerance turns the same lag into a break")

    def test_duplicates(self):
        r = run([inn("A", "2026-03-01", 100)], [pr("A", "2026-03-01", 100), pr("A", "2026-03-01", "2026-03-02", 100)], 2)
        self.assertEqual(count(r, BreakType.DUPLICATE), 1)
        self.assertEqual(r.matched_count, 1)
        self.assertTrue(r.ties_out())
        self.assertIsNone(r.breaks[0].internal, "duplicate break names the processor side")
        self.assertIsNotNone(r.breaks[0].processor)
        r = run([inn("A", "2026-03-01", 100), inn("A", "2026-03-01", 100)], [], 2)
        self.assertEqual(count(r, BreakType.MISSING_AT_PROCESSOR), 1)
        self.assertEqual(count(r, BreakType.DUPLICATE), 1)
        self.assertTrue(r.ties_out())


class AgingTest(unittest.TestCase):
    def test_aging(self):
        r = run([inn("D", "2026-03-01", 700), inn("E", "2026-03-30", 700)], [], 2)
        ages = r.count_by_age()
        self.assertEqual(ages.get(AgeBand.D8_30, 0), 1)
        self.assertEqual(ages.get(AgeBand.D0_1, 0), 1)
        self.assertIs(AgeBand.of(1), AgeBand.D0_1)
        self.assertIs(AgeBand.of(2), AgeBand.D2_7)
        self.assertIs(AgeBand.of(7), AgeBand.D2_7)
        self.assertIs(AgeBand.of(8), AgeBand.D8_30)
        self.assertIs(AgeBand.of(31), AgeBand.D30_PLUS)
        self.assertEqual(StreamingReconciler.age_days(date(2026, 4, 30), AS_OF), 0, "age never negative")


class RefusalTest(unittest.TestCase):
    def test_unsorted_feed_refused(self):
        unsorted = [inn("B", "2026-03-02", 100), inn("A", "2026-03-01", 100)]
        with self.assertRaises(RuntimeError):
            StreamingReconciler(USD, 2).reconcile(iter(unsorted), iter([]), AS_OF)

    def test_wrong_side_refused(self):
        with self.assertRaises(ValueError):
            StreamingReconciler(USD, 2).reconcile(iter([pr("A", "2026-03-01", 1)]), iter([]), AS_OF)


class SeededRunTest(unittest.TestCase):
    def test_large_run(self):
        feeds = FeedGenerator.generate(7, 100_000, date(2026, 3, 1), 30, USD, 0.001)
        big = StreamingReconciler(USD, 2).reconcile(iter(feeds.internal), iter(feeds.processor), AS_OF)
        self.assertTrue(big.ties_out())
        self.assertEqual(len(big.breaks), feeds.injected_breaks, "every injected break is found, nothing invented")
        self.assertEqual(big.matched_count + sum(1 for x in big.breaks if x.internal is not None), len(feeds.internal))
        self.assertGreater(big.buckets_fast_matched, big.buckets_inspected * 10)
        self.assertEqual(FeedGenerator.generate(7, 100_000, date(2026, 3, 1), 30, USD, 0.001).injected_breaks,
                         feeds.injected_breaks, "determinism: same seed, same report")

    def test_java_random_sequence(self):
        """Values taken from `new java.util.Random(2026)` on the JDK; they pin the LCG port that seeds the feeds."""
        r = JavaRandom(2026)
        self.assertEqual([r.next_int(66), r.next_int(100)], [29, 50])
        self.assertEqual(r.next(32), -647640901)


if __name__ == "__main__":
    unittest.main()
