"""Tests for phases/09-reliability-and-scale/03-caching-strategies. Run: python3 -m unittest -v"""
import unittest

from main import (USD, BackingStore, CacheAside, Currency, CurrencyMismatchError, ManualClock, Money,
                  ReadThrough, TtlCache, Workload, WriteBehind, WriteThrough)


def usd(minor: int) -> Money:
    return Money.of_minor(minor, USD)


class TtlCacheTest(unittest.TestCase):
    def test_expiry_is_lazy_and_clock_driven(self):
        clock = ManualClock(1_000)
        ttl = TtlCache(clock)
        ttl.put("a", usd(100), 500)
        self.assertEqual(ttl.get("a"), usd(100))
        clock.advance(499)
        self.assertTrue(ttl.contains("a"))
        clock.advance(1)
        self.assertIsNone(ttl.get("a"))
        self.assertEqual(ttl.size(), 0)
        ttl.put("b", usd(1), TtlCache.NO_EXPIRY)
        clock.advance(1_000_000_000)
        self.assertTrue(ttl.contains("b"))
        self.assertTrue(ttl.invalidate("b") and not ttl.invalidate("b"))
        with self.assertRaises(ValueError):
            clock.advance(-1)


class CacheAsideTest(unittest.TestCase):
    def test_ttl_only_serves_stale_balance(self):
        c1 = ManualClock(0)
        s1 = BackingStore(20, 25)
        s1.write("acct", usd(10_000))
        ttl_only = CacheAside(s1, c1, 30_000, False, 1)
        self.assertTrue(ttl_only.read("acct") == usd(10_000) and ttl_only.stats().misses == 1)
        self.assertTrue(ttl_only.read("acct") == usd(10_000) and ttl_only.stats().hits == 1)
        s1.write("acct", usd(2_000))  # out-of-band debit straight to the store
        self.assertEqual(ttl_only.read_measured("acct"), usd(10_000))
        self.assertEqual(ttl_only.stats().stale_reads, 1)
        c1.advance(30_000)
        self.assertTrue(ttl_only.read_measured("acct") == usd(2_000) and ttl_only.stats().stale_reads == 1)

    def test_invalidate_on_write_is_never_stale(self):
        c2 = ManualClock(0)
        s2 = BackingStore(20, 25)
        s2.write("acct", usd(10_000))
        inval = CacheAside(s2, c2, 30_000, True, 1)
        inval.read("acct")
        inval.write("acct", usd(2_000))
        self.assertFalse(inval.cache().contains("acct"))
        self.assertTrue(inval.read_measured("acct") == usd(2_000) and inval.stats().stale_reads == 0)
        self.assertEqual(inval.stats().misses, 2)


class ReadThroughTest(unittest.TestCase):
    def test_same_read_semantics_write_deletes(self):
        c3 = ManualClock(0)
        s3 = BackingStore(20, 25)
        s3.write("acct", usd(500))
        rt = ReadThrough(s3, c3, 1_000, 1)
        self.assertTrue(rt.read("acct") == usd(500) and s3.reads == 1)
        rt.read("acct")
        self.assertTrue(s3.reads == 1 and rt.stats().hits == 1)
        rt.write("acct", usd(400))
        self.assertTrue(not rt.cache().contains("acct") and rt.read("acct") == usd(400))
        self.assertTrue(rt.read("nope") is None and not rt.cache().contains("nope"))


class WriteThroughTest(unittest.TestCase):
    def test_writes_populate_cache_synchronously(self):
        c4 = ManualClock(0)
        s4 = BackingStore(20, 25)
        wt = WriteThrough(s4, c4, 1_000, 1)
        wt.write("acct", usd(777))
        self.assertTrue(s4.peek("acct") == usd(777) and wt.cache().contains("acct"))
        self.assertTrue(wt.read("acct") == usd(777) and wt.stats().hits == 1 and wt.stats().misses == 0)
        self.assertEqual(s4.writes, 1)


class WriteBehindTest(unittest.TestCase):
    def test_coalescing_flush_order_and_loss_on_crash(self):
        c5 = ManualClock(0)
        s5 = BackingStore(20, 25)
        s5.write("a", usd(100))
        s5.write("b", usd(200))
        s5.writes = 0
        wb = WriteBehind(s5, c5, 1)
        wb.write("a", usd(90))
        wb.write("b", usd(190))
        wb.write("a", usd(80))
        self.assertEqual(wb.read("a"), usd(80))
        self.assertTrue(s5.writes == 0 and s5.peek("a") == usd(100))
        self.assertEqual(wb.dirty_count(), 2)
        self.assertTrue(wb.read_measured("a") == usd(80) and wb.stats().stale_reads == 1)
        flushed = wb.flush()
        self.assertTrue(flushed == 2 and s5.writes == 2)
        self.assertTrue(s5.peek("a") == usd(80) and s5.peek("b") == usd(190))
        self.assertEqual(wb.dirty_count(), 0)
        wb.write("a", usd(70))
        wb.crash()
        self.assertTrue(wb.lost_writes == 1 and s5.peek("a") == usd(80))
        self.assertEqual(wb.read("a"), usd(80))


class WorkloadTest(unittest.TestCase):
    def test_determinism_and_measured_comparison(self):
        ops1 = Workload.generate(42, 2_000, 100, 0.15, 0.2, 0.8)
        ops2 = Workload.generate(42, 2_000, 100, 0.15, 0.2, 0.8)
        self.assertEqual(ops1, ops2)
        hot_reads = sum(1 for o in ops1 if int(o.account[5:]) < 20)
        self.assertGreater(hot_reads, len(ops1) * 0.7)

        w1 = ManualClock(0)
        m_ttl = CacheAside(Workload.seeded_store(100, 20, 25), w1, 5_000, False, 1)
        Workload.run(m_ttl, ops1, w1, 10)
        w2 = ManualClock(0)
        m_inv = CacheAside(Workload.seeded_store(100, 20, 25), w2, 5_000, True, 1)
        Workload.run(m_inv, ops1, w2, 10)
        w3 = ManualClock(0)
        m_wt = WriteThrough(Workload.seeded_store(100, 20, 25), w3, 5_000, 1)
        Workload.run(m_wt, ops1, w3, 10)
        self.assertGreater(m_ttl.stats().stale_reads, 0)
        self.assertEqual(m_inv.stats().stale_reads, 0)
        self.assertEqual(m_wt.stats().stale_reads, 0)
        self.assertGreaterEqual(m_ttl.stats().hit_rate(), m_inv.stats().hit_rate())
        self.assertLess(m_inv.store().reads, len(ops1))
        self.assertGreaterEqual(m_wt.stats().hit_rate(), m_inv.stats().hit_rate())

    def test_java_random_parity(self):
        # The seed-42 workload drives the numbers in the docs; pin the first op so an RNG drift is caught here.
        ops = Workload.generate(42, 20_000, 500, 0.15, 0.20, 0.80)
        writes = sum(1 for o in ops if o.is_write)
        self.assertEqual(writes, 2980)


class MoneyTest(unittest.TestCase):
    def test_guard_rails_hold_inside_the_cache(self):
        with self.assertRaises(CurrencyMismatchError):
            usd(1).plus(Money.of_minor(1, Currency("EUR", 2)))
        self.assertEqual(str(usd(-7)), "-0.07 USD")


if __name__ == "__main__":
    unittest.main()
