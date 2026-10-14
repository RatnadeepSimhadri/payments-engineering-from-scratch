"""Tests for phases/02-algorithms/14-randomized-algorithms. Run: python3 -m unittest -v"""

import math
import unittest

from main import (
    USD,
    MillerRabin,
    Money,
    Quickselect,
    ReservoirSampler,
    Rng,
    Shuffle,
    WeightedReservoirSampler,
    day_of_transactions,
    to_int64,
)


def sample_ids(rng):
    s = ReservoirSampler(3, rng)
    for t in day_of_transactions(Rng(0), 500):
        s.offer(t)
    return [t.id for t in s.sample()]


class RngTest(unittest.TestCase):
    """The generator: deterministic, in range, not visibly biased, and bit-identical to the Java SplitMix64."""

    def test_generator(self):
        self.assertTrue(Rng(42).next_long() == Rng(42).next_long() and Rng(42).next_long() != Rng(43).next_long())
        self.assertEqual(Rng(0).next_long(), to_int64(0xE220A8397B1DCDAF))  # SplitMix64 reference value
        r = Rng(1)
        in_range = True
        buckets = [0] * 7
        for _ in range(70_000):
            v = r.next_int(7)
            in_range &= 0 <= v < 7
            buckets[v] += 1
        self.assertTrue(in_range)
        self.assertTrue(all(abs(b - 10_000) < 500 for b in buckets))  # each bucket within 5% of 10,000
        self.assertTrue(Rng(9).next_int(1) == 0 and Rng(10).next_int(1) == 0)
        with self.assertRaises(ValueError):
            Rng(1).next_int(0)
        self.assertTrue(all(0.0 <= r.next_double() < 1.0 for _ in range(10_000)))

    def test_signed_view_matches_java_long(self):
        self.assertLess(Rng(0).next_long(), 0)  # 0xE220... has the top bit set: negative as a Java long
        self.assertEqual(str(Money.of_minor(-150, USD)), "-1.50 USD")  # Java's truncating / and %


class ReservoirTest(unittest.TestCase):
    def test_invariant_by_frequency(self):
        picked: dict[int, int] = {}
        rr = Rng(123)
        for _ in range(30_000):
            s = ReservoirSampler(3, rr)
            for item in range(1, 11):
                s.offer(item)
            for v in s.sample():
                picked[v] = picked.get(v, 0) + 1
        # each of 10 items should be in the 3-slot reservoir with probability 0.3 -> 9,000 of 30,000, +-5%
        self.assertTrue(len(picked) == 10 and all(abs(c - 9_000) < 450 for c in picked.values()))
        self.assertLess(abs(picked[10] - picked[1]), 600)  # the last item is not favoured

    def test_reservoir_edges(self):
        small = ReservoirSampler(5, Rng(1))
        for s in ("a", "b", "c"):
            small.offer(s)
        self.assertTrue(list(small.sample()) == ["a", "b", "c"] and small.seen() == 3)
        exact = ReservoirSampler(4, Rng(1))
        for i in range(1000):
            exact.offer(i)
        self.assertTrue(len(exact.sample()) == 4 and exact.seen() == 1000)
        self.assertTrue(len(set(exact.sample())) == 4 and all(0 <= v < 1000 for v in exact.sample()))
        self.assertEqual(sample_ids(Rng(77)), sample_ids(Rng(77)))
        with self.assertRaises(ValueError):
            ReservoirSampler(0, Rng(1))


class WeightedTest(unittest.TestCase):
    def test_weighted(self):
        wpicked: dict[str, int] = {}
        wr = Rng(5)
        for _ in range(20_000):
            w = WeightedReservoirSampler(1, wr)
            w.offer("small", 1)
            w.offer("large", 9)
            key = w.sample()[0]
            wpicked[key] = wpicked.get(key, 0) + 1
        self.assertLess(abs(wpicked.get("large", 0) - 18_000), 400)  # 9:1 weights give ~90% large
        with self.assertRaises(ValueError):
            WeightedReservoirSampler(1, Rng(1)).offer("x", 0)


class ShuffleTest(unittest.TestCase):
    def test_fisher_yates_uniform_naive_not(self):
        fy = Shuffle.permutation_counts(60_000, Rng(7), True)
        nv = Shuffle.permutation_counts(60_000, Rng(7), False)
        self.assertTrue(all(abs(c - 10_000) < 400 for c in fy))
        self.assertTrue(all(abs(nv[i] - 11_111) < 400 for i in (1, 2, 3)))  # acb, bac, bca near 5/27
        self.assertTrue(all(abs(nv[i] - 8_889) < 400 for i in (0, 4, 5)))  # abc, cab, cba near 4/27

    def test_fisher_yates_edges(self):
        perm = [1, 2, 3, 4, 5, 6, 7, 8]
        Shuffle.fisher_yates(perm, Rng(3))
        self.assertEqual(set(perm), {1, 2, 3, 4, 5, 6, 7, 8})
        one = [42]
        Shuffle.fisher_yates(one, Rng(1))
        self.assertEqual(one, [42])
        empty: list = []
        Shuffle.fisher_yates(empty, Rng(1))
        self.assertEqual(empty, [])


class QuickselectTest(unittest.TestCase):
    def test_always_right(self):
        data = [9, 1, 8, 2, 7, 3, 6, 4, 5, 0]
        self.assertTrue(all(Quickselect.select(list(data), k, Rng(k)) == k for k in range(len(data))))
        self.assertTrue(Quickselect.select([5, 5, 5, 1, 5], 0, Rng(1)) == 1
                        and Quickselect.select([5, 5, 5, 1, 5], 3, Rng(1)) == 5)
        self.assertEqual(Quickselect.select([7], 0, Rng(1)), 7)
        self.assertTrue(Quickselect.select([1, 2, 3, 4, 5], 4, Rng(1)) == 5
                        and Quickselect.select([5, 4, 3, 2, 1], 0, Rng(1)) == 1)
        with self.assertRaises(ValueError):
            Quickselect.select([1, 2], 2, Rng(1))
        with self.assertRaises(ValueError):
            Quickselect.select([], 0, Rng(1))

    def test_against_sort(self):
        ar = Rng(99)
        amounts = [ar.next_int(1_000_000) for _ in range(5000)]
        sorted_ = sorted(amounts)
        self.assertEqual(Quickselect.median(list(amounts), Rng(1)), sorted_[(len(sorted_) - 1) // 2])
        self.assertEqual(Quickselect.percentile(list(amounts), 0.99, Rng(1)), sorted_[math.ceil(0.99 * len(sorted_)) - 1])
        self.assertEqual(Quickselect.percentile(list(amounts), 1.0, Rng(1)), sorted_[-1])
        self.assertTrue(all(Quickselect.select(list(amounts), 1234, Rng(seed)) == sorted_[1234] for seed in range(20)))


class MillerRabinTest(unittest.TestCase):
    def test_miller_rabin(self):
        self.assertTrue(MillerRabin.pow_mod(2, 10, 1000) == 24 and MillerRabin.pow_mod(3, 0, 7) == 1)
        self.assertTrue(MillerRabin.is_probable_prime(2, 5, Rng(1)) and MillerRabin.is_probable_prime(7919, 5, Rng(1))
                        and MillerRabin.is_probable_prime(2_147_483_647, 5, Rng(1)))
        self.assertTrue(not MillerRabin.is_probable_prime(1, 5, Rng(1))
                        and not MillerRabin.is_probable_prime(1_000_001, 5, Rng(1))
                        and not MillerRabin.is_probable_prime(7919 * 7927, 5, Rng(1)))
        self.assertFalse(MillerRabin.is_probable_prime(561, 10, Rng(1)))  # Carmichael number
        self.assertTrue(all(MillerRabin.is_probable_prime(n, 8, Rng(n)) == MillerRabin.is_prime_by_trial_division(n)
                            for n in range(2, 5000)))
        # one-sided error: a true prime is never declared composite, even with 1 round
        self.assertTrue(all(MillerRabin.is_probable_prime(n, 1, Rng(n))
                            for n in range(2, 5000) if MillerRabin.is_prime_by_trial_division(n)))
        with self.assertRaises(ValueError):
            MillerRabin.is_probable_prime(1 << 31, 1, Rng(1))


if __name__ == "__main__":
    unittest.main()
