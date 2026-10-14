"""Tests for phases/03-systems-data-structures/07-skip-lists. Run: python3 -m unittest -v"""
import bisect
import unittest

from main import JavaRandom, SkipList, java_shuffle


def build_sample():
    lst = SkipList(16, 0.5, JavaRandom(42))
    for k in (742, 118, 905, 367, 512, 288, 674, 59, 831, 456):
        lst.put(k, f"m{k}")
    return lst


class ConstructionTest(unittest.TestCase):
    def test_construction(self):
        with self.assertRaises(ValueError):
            SkipList(16, 1.0, JavaRandom(1))
        with self.assertRaises(ValueError):
            SkipList(0, 0.5, JavaRandom(1))
        self.assertEqual(SkipList.default_max_level(100_000, 0.5), 17)
        self.assertEqual(SkipList.default_max_level(100_000, 0.25), 9)


class MapBehaviourTest(unittest.TestCase):
    def test_basic_map(self):
        empty = SkipList(16, 0.5, JavaRandom(42))
        self.assertTrue(empty.size() == 0 and empty.level() == 1 and empty.get(5) is None)
        lst = build_sample()
        self.assertEqual(lst.keys(), [59, 118, 288, 367, 456, 512, 674, 742, 831, 905])
        self.assertTrue(lst.get(512) == "m512" and lst.get(59) == "m59" and lst.get(905) == "m905")
        self.assertTrue(lst.get(1) is None and lst.get(500) is None and lst.get(1000) is None)
        lst.put(512, "updated")
        self.assertTrue(lst.get(512) == "updated" and lst.size() == 10)
        self.assertTrue(lst.check_invariants())

    def test_rank_and_by_rank(self):
        lst = build_sample()
        self.assertTrue(lst.rank(59) == 1 and lst.rank(512) == 6 and lst.rank(905) == 10)
        self.assertTrue(lst.rank(500) == 0 and lst.rank(0) == 0)
        self.assertTrue(lst.by_rank(1) == 59 and lst.by_rank(6) == 512 and lst.by_rank(10) == 905)
        self.assertTrue(lst.by_rank(0) is None and lst.by_rank(11) is None)
        self.assertTrue(all(lst.rank(lst.by_rank(r)) == r for r in range(1, 11)))

    def test_range(self):
        lst = build_sample()
        self.assertEqual(lst.range(300, 700), [367, 456, 512, 674])
        self.assertEqual(lst.range(118, 288), [118, 288])
        self.assertTrue(lst.range(900, 904) == [] and lst.range(1000, 2000) == [])

    def test_remove(self):
        lst = build_sample()
        self.assertTrue(lst.remove(512) and lst.get(512) is None and lst.size() == 9)
        self.assertTrue(not lst.remove(512) and not lst.remove(12345) and lst.size() == 9)
        self.assertTrue(lst.rank(674) == 6 and lst.by_rank(6) == 674)
        self.assertTrue(lst.check_invariants())
        one = SkipList(16, 0.5, JavaRandom(3))
        one.put(1, "a")
        one.remove(1)
        self.assertTrue(one.size() == 0 and one.level() == 1 and one.keys() == [] and one.check_invariants())


class LargeRandomizedTest(unittest.TestCase):
    def test_against_sorted_model(self):
        n = 50_000
        rng = JavaRandom(7)
        big = SkipList(SkipList.default_max_level(n, 0.5), 0.5, rng)
        model: dict[int, int] = {}
        keys = [i * 3 for i in range(n)]
        java_shuffle(keys, rng)
        for k in keys:
            big.put(k, k)
            model[k] = k
        ordered = sorted(model)  # the TreeMap stand-in
        self.assertTrue(big.keys() == ordered and big.size() == n)
        self.assertTrue(big.check_invariants())
        self.assertTrue(all(big.rank(k) == bisect.bisect_right(ordered, k) for k in keys[:1_000]))
        self.assertEqual(big.range(3_000, 3_300), [k for k in ordered if 3_000 <= k <= 3_300])
        for i in range(0, n, 2):
            big.remove(keys[i])
            model.pop(keys[i])
        ordered = sorted(model)
        self.assertTrue(big.keys() == ordered and big.check_invariants())
        self.assertTrue(big.by_rank(1) == ordered[0] and big.by_rank(big.size()) == ordered[-1])

        # the probabilistic shape
        hist = big.level_histogram()
        self.assertTrue(0.45 * big.size() < hist[1] < 0.55 * big.size())
        self.assertTrue(0.20 * big.size() < hist[2] < 0.30 * big.size())
        self.assertTrue(10 <= big.level() <= big.max_level)

        # expected O(log n): measured comparisons vs Pugh
        big.search_steps = 0
        for _ in range(5_000):
            big.get(rng.next_int(3 * n))
        avg = big.search_steps / 5_000.0
        expected = SkipList.expected_steps(big.size(), 0.5)
        self.assertTrue(0.5 * expected < avg < 1.5 * expected)

    def test_sorted_input_same_cost(self):
        n = 50_000
        sorted_in = SkipList(17, 0.5, JavaRandom(11))
        for i in range(n):
            sorted_in.put(i, i)
        sorted_in.search_steps = 0
        for i in range(5_000):
            sorted_in.get(i * 7 % n)
        self.assertLess(sorted_in.search_steps / 5_000.0, 1.5 * SkipList.expected_steps(n, 0.5))


class BiasedCoinTest(unittest.TestCase):
    def test_coin_that_never_promotes(self):
        flat = SkipList.with_level_source(16, 0.5, lambda: 1)
        for i in range(2_000):
            flat.put(i, i)
        flat.search_steps = 0
        for _ in range(100):
            flat.get(1_999)
        self.assertTrue(flat.level() == 1 and flat.search_steps / 100.0 > 1_900)
        self.assertTrue(flat.get(1_234) == 1_234 and flat.rank(1_234) == 1_235 and flat.check_invariants())

    def test_same_seed_same_shape(self):
        a, b = SkipList(16, 0.5, JavaRandom(99)), SkipList(16, 0.5, JavaRandom(99))
        for i in range(1_000):
            a.put(i, i)
            b.put(i, i)
        self.assertEqual(a.level_histogram(), b.level_histogram())

    def test_java_random_next_double_stream(self):
        # First doubles of new Random(42) in Java: 0.7275636800328681, 0.6832234717598454.
        rng = JavaRandom(42)
        self.assertEqual(rng.next_double(), 0.7275636800328681)
        self.assertEqual(rng.next_double(), 0.6832234717598454)


if __name__ == "__main__":
    unittest.main()
