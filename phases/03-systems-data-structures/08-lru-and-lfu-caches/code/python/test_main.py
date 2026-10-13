"""Tests for phases/03-systems-data-structures/08-lru-and-lfu-caches. Run: python3 -m unittest -v"""

import unittest

from main import LfuCache, LruCache, ManualClock, Money, Rng, bin_trace, replay


class LruTest(unittest.TestCase):
    def test_get_moves_to_front_and_put_evicts_least_recent(self) -> None:
        c: LruCache[str, str] = LruCache(3)
        c.put("a", "1")
        c.put("b", "2")
        c.put("c", "3")
        self.assertEqual(c.keys(), ["c", "b", "a"])
        self.assertEqual(c.get("a"), "1")
        self.assertEqual(c.keys(), ["a", "c", "b"])
        c.put("d", "4")
        self.assertEqual(c.keys(), ["d", "a", "c"])
        self.assertIsNone(c.get("b"))
        self.assertEqual(c.evictions, 1)

    def test_put_existing_key_updates_value_without_eviction(self) -> None:
        c: LruCache[str, str] = LruCache(2)
        c.put("a", "1")
        c.put("b", "2")
        c.put("a", "one")
        self.assertEqual(len(c), 2)
        self.assertEqual(c.get("a"), "one")
        self.assertEqual(c.evictions, 0)
        self.assertEqual(c.keys(), ["a", "b"])

    def test_capacity_is_never_exceeded(self) -> None:
        c: LruCache[int, int] = LruCache(10)
        for i in range(1_000):
            c.put(i, i)
            self.assertLessEqual(len(c), 10)
        self.assertEqual(c.keys(), list(range(999, 989, -1)))
        self.assertEqual(c.evictions, 990)

    def test_remove(self) -> None:
        c: LruCache[str, str] = LruCache(2)
        c.put("a", "1")
        self.assertTrue(c.remove("a"))
        self.assertFalse(c.remove("a"))
        self.assertEqual(len(c), 0)
        self.assertIsNone(c.get("a"))

    def test_capacity_one_and_bad_capacity(self) -> None:
        c: LruCache[str, str] = LruCache(1)
        c.put("a", "1")
        c.put("b", "2")
        self.assertIsNone(c.get("a"))
        self.assertEqual(c.get("b"), "2")
        with self.assertRaises(ValueError):
            LruCache(0)

    def test_hit_and_miss_counters(self) -> None:
        c: LruCache[str, str] = LruCache(2)
        c.put("a", "1")
        c.get("a")
        c.get("zz")
        self.assertEqual((c.hits, c.misses), (1, 1))
        self.assertAlmostEqual(c.hit_rate(), 0.5)
        self.assertEqual(LruCache(2).hit_rate(), 0.0)


class TtlTest(unittest.TestCase):
    def test_entry_expires_at_exactly_ttl(self) -> None:
        clock = ManualClock(1_000)
        c: LruCache[str, str] = LruCache(4, clock)
        c.put("cfg", "v1", ttl_millis=5_000)
        clock.advance(4_999)
        self.assertEqual(c.get("cfg"), "v1")
        clock.advance(1)
        self.assertIsNone(c.get("cfg"))
        self.assertEqual(c.expirations, 1)
        self.assertEqual(len(c), 0)

    def test_expired_entry_frees_its_slot(self) -> None:
        clock = ManualClock()
        c: LruCache[str, str] = LruCache(2, clock)
        c.put("short", "x", ttl_millis=10)
        c.put("long", "y")
        clock.advance(10)
        self.assertIsNone(c.get("short"))
        c.put("new", "z")
        self.assertEqual(c.get("long"), "y")
        self.assertEqual(c.evictions, 0)

    def test_expired_entry_still_counts_toward_capacity_until_touched(self) -> None:
        # The failure the lesson warns about: lazy expiry means a dead entry can evict a live one.
        clock = ManualClock()
        c: LruCache[str, str] = LruCache(2, clock)
        c.put("dead", "x", ttl_millis=10)
        c.put("live", "y")
        clock.advance(10)
        c.put("new", "z")  # 'dead' is the least recent and is evicted, not 'live'
        self.assertEqual(c.get("live"), "y")
        self.assertEqual(c.evictions, 1)
        self.assertEqual(c.expirations, 0)

    def test_rejects_non_positive_ttl_and_backwards_clock(self) -> None:
        c: LruCache[str, str] = LruCache(2)
        with self.assertRaises(ValueError):
            c.put("a", "1", ttl_millis=0)
        with self.assertRaises(ValueError):
            ManualClock().advance(-1)


class LfuTest(unittest.TestCase):
    def test_evicts_least_frequent(self) -> None:
        c: LfuCache[str, str] = LfuCache(2)
        c.put("a", "1")
        c.put("b", "2")
        c.get("a")
        c.put("c", "3")
        self.assertIsNone(c.get("b"))
        self.assertEqual(c.get("a"), "1")
        self.assertEqual(c.get("c"), "3")
        self.assertEqual(c.evictions, 1)

    def test_ties_break_least_recently_used(self) -> None:
        c: LfuCache[str, str] = LfuCache(2)
        c.put("a", "1")
        c.put("b", "2")
        c.put("c", "3")  # a and b both have frequency 1; a is older
        self.assertIsNone(c.get("a"))
        self.assertEqual(c.get("b"), "2")

    def test_frequency_and_min_frequency_tracking(self) -> None:
        c: LfuCache[str, str] = LfuCache(3)
        c.put("a", "1")
        c.put("a", "1")
        c.put("a", "1")
        c.put("b", "2")
        c.get("b")
        c.put("c", "3")
        self.assertEqual((c.frequency("a"), c.frequency("b"), c.frequency("c")), (3, 2, 1))
        self.assertEqual(c.min_frequency(), 1)
        c.get("c")
        self.assertEqual(c.min_frequency(), 2)
        self.assertEqual(c.frequency("missing"), 0)

    def test_new_insert_resets_min_frequency_to_one(self) -> None:
        c: LfuCache[str, str] = LfuCache(2)
        c.put("a", "1")
        c.get("a")
        c.get("a")
        c.put("b", "2")
        c.get("b")
        self.assertEqual(c.min_frequency(), 2)
        c.put("c", "3")  # evicts b (freq 2 < a's 3); c enters at 1
        self.assertEqual(c.min_frequency(), 1)
        self.assertIsNone(c.get("b"))

    def test_remove_and_bad_capacity(self) -> None:
        c: LfuCache[str, str] = LfuCache(2)
        c.put("a", "1")
        c.get("a")
        c.put("b", "2")
        self.assertTrue(c.remove("b"))
        self.assertFalse(c.remove("b"))
        self.assertEqual(c.min_frequency(), 2)
        self.assertEqual(len(c), 1)
        with self.assertRaises(ValueError):
            LfuCache(0)

    def test_capacity_is_never_exceeded(self) -> None:
        c: LfuCache[int, int] = LfuCache(10)
        for i in range(1_000):
            c.put(i, i)
            self.assertLessEqual(len(c), 10)


class TraceTest(unittest.TestCase):
    def test_rng_matches_java_and_go(self) -> None:
        self.assertEqual(Rng(42).next(), 0xBDD732262FEB6E95)
        r = Rng(42)
        self.assertEqual([r.next_int(100) for _ in range(5)], [51, 53, 71, 35, 65])
        self.assertEqual(bin_trace(1, 6, 0), ["bin-8", "bin-12", "bin-1271", "bin-15", "bin-7", "bin-382"])

    def test_lfu_survives_a_scan_and_lru_does_not(self) -> None:
        lru: LruCache[str, str] = LruCache(32)
        lfu: LfuCache[str, str] = LfuCache(32)
        phase1 = bin_trace(1, 6000, hot_offset=0)
        scan = [f"scan-{i}" for i in range(500)]
        after = bin_trace(2, 2000, hot_offset=0)[:200]
        for keys in (phase1, scan):
            replay(lru, keys)
            replay(lfu, keys)
        lru_after, lfu_after = replay(lru, after), replay(lfu, after)
        self.assertGreater(lfu_after, lru_after + 0.1)
        self.assertAlmostEqual(lru_after, 0.570, places=3)
        self.assertAlmostEqual(lfu_after, 0.725, places=3)

    def test_lru_adapts_to_a_shifted_hot_set_and_lfu_lags(self) -> None:
        lru: LruCache[str, str] = LruCache(32)
        lfu: LfuCache[str, str] = LfuCache(32)
        replay(lru, bin_trace(1, 6000, hot_offset=0))
        replay(lfu, bin_trace(1, 6000, hot_offset=0))
        phase2 = bin_trace(3, 6000, hot_offset=20)
        lru_rate, lfu_rate = replay(lru, phase2), replay(lfu, phase2)
        self.assertGreater(lru_rate, 0.7)
        self.assertLess(lfu_rate, 0.5)


class MoneyTest(unittest.TestCase):
    def test_stale_cached_balance_approves_an_overdraft(self) -> None:
        cache: LruCache[str, Money] = LruCache(4)
        ledger = Money(10_000, "USD")
        cache.put("acct-7", ledger)
        ledger = ledger.minus(Money(8_000, "USD"))
        cached = cache.get("acct-7")
        self.assertEqual(str(cached), "100.00 USD")
        self.assertEqual(str(ledger), "20.00 USD")
        self.assertGreaterEqual(cached.minus(Money(5_000, "USD")).minor_units, 0)   # cache approves
        self.assertLess(ledger.minus(Money(5_000, "USD")).minor_units, 0)           # ledger would decline
        with self.assertRaises(ValueError):
            ledger.plus(Money(1, "EUR"))


if __name__ == "__main__":
    unittest.main()
