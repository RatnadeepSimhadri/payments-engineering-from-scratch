"""Tests for phases/07-storage-and-transactions/10-secondary-indexes. Run: python3 -m unittest -v"""
import unittest

from main import (
    Currency,
    GlobalIndex,
    Hashing,
    IndexMode,
    Money,
    PartitionedStore,
    Payment,
    sample_data,
    to_int64,
)


def pay(id: str, customer: str, cents: int, at: int) -> Payment:
    return Payment(id, customer, "m_test", Money.of_minor(cents, Currency.USD), at)


def ids(r) -> list[str]:
    return [p.id for p in r.payments]


def loaded(shards: int, mode: IndexMode) -> PartitionedStore:
    s = PartitionedStore(shards, 2, mode)
    for p in sample_data():
        s.put(p)
    return s


class HashingTest(unittest.TestCase):
    def test_fnv_vectors_and_floor_mod(self):
        self.assertEqual(Hashing.fnv1a64(""), to_int64(0xCBF29CE484222325))
        self.assertEqual(Hashing.fnv1a64("a"), to_int64(0xAF63DC4C8601EC8C))
        self.assertEqual(Hashing.partition_for("pay_001", 4), Hashing.partition_for("pay_001", 4))
        self.assertTrue(0 <= Hashing.partition_for("pay_002", 4) < 4)

    def test_signed_floor_mod_differs_from_unsigned_mod(self):
        # Python-only: the hash is negative as a Java long, and floorMod(signed) != unsigned % n in general.
        h = Hashing.fnv1a64("")
        self.assertLess(h, 0)
        unsigned = h & 0xFFFFFFFFFFFFFFFF
        self.assertNotEqual(Hashing.partition_for("", 3), unsigned % 3)  # 2**64 % 3 == 1, so they must differ
        self.assertEqual(Hashing.partition_for("", 3), (unsigned % 3 - 1) % 3)


class PrimaryKeyTest(unittest.TestCase):
    def test_get_is_one_request(self):
        sync = loaded(4, IndexMode.SYNC)
        before = sync.total_shard_requests()
        self.assertEqual(sync.get("pay_005").customer_id, "cust_alice")
        self.assertEqual(sync.total_shard_requests() - before, 1)
        self.assertIsNotNone(sync.shard(sync.shard_for("pay_005")).get("pay_005"))
        self.assertIsNone(sync.get("pay_999"))


class QueryStrategyTest(unittest.TestCase):
    def test_same_rows_same_order_different_bills(self):
        sync = loaded(4, IndexMode.SYNC)
        scatter = sync.find_by_customer_scatter_gather("cust_alice")
        glob = sync.find_by_customer_global("cust_alice")
        self.assertEqual(len(scatter.payments), 5)
        self.assertEqual(glob.payments, scatter.payments)
        self.assertEqual(ids(scatter), ["pay_001", "pay_003", "pay_005", "pay_008", "pay_011"])
        self.assertTrue(scatter.shard_requests == 4 and scatter.index_requests == 0)
        self.assertEqual(glob.index_requests, 1)
        self.assertTrue(1 <= glob.shard_requests <= 4)

    def test_single_and_unknown_customers(self):
        sync = loaded(4, IndexMode.SYNC)
        self.assertEqual(sync.find_by_customer_scatter_gather("cust_erin").total_requests(), 4)
        erin = sync.find_by_customer_global("cust_erin")
        self.assertTrue(erin.total_requests() == 2 and len(erin.payments) == 1)
        nobody = sync.find_by_customer_global("cust_nobody")
        self.assertTrue(not nobody.payments and sync.find_by_customer_global("cust_nobody").shard_requests == 0)
        self.assertFalse(sync.find_by_customer_scatter_gather("cust_nobody").payments)


class WriteModeTest(unittest.TestCase):
    def test_sync(self):
        sync = loaded(4, IndexMode.SYNC)
        self.assertGreaterEqual(sync.global_index().total_requests(), 12)
        self.assertEqual(sync.pending_index_updates(), 0)
        self.assertTrue(sync.audit_global_index().consistent())
        self.assertEqual(sync.global_index().entry_count(), 12)

    def test_async_lag_drain_staleness_and_dangling(self):
        a = loaded(4, IndexMode.ASYNC)
        self.assertEqual(a.global_index().total_requests(), 0)
        self.assertEqual(a.pending_index_updates(), 12)
        self.assertFalse(a.find_by_customer_global("cust_alice").payments)
        self.assertEqual(len(a.find_by_customer_scatter_gather("cust_alice").payments), 5)
        self.assertEqual(len(a.audit_global_index().missing), 12)
        self.assertTrue(a.drain_index_updates(3) == 3 and a.pending_index_updates() == 9)
        self.assertTrue(a.drain_all() == 9 and a.pending_index_updates() == 0)
        self.assertEqual(a.find_by_customer_global("cust_alice").payments,
                         a.find_by_customer_scatter_gather("cust_alice").payments)
        self.assertTrue(a.audit_global_index().consistent())

        # staleness window on a fresh write
        a.put(pay("pay_013", "cust_erin", 1800, 13))
        self.assertIsNotNone(a.get("pay_013"))
        self.assertEqual(len(a.find_by_customer_global("cust_erin").payments), 1)
        self.assertEqual(len(a.find_by_customer_scatter_gather("cust_erin").payments), 2)
        a.drain_all()
        self.assertEqual(len(a.find_by_customer_global("cust_erin").payments), 2)

        # dangling entries: delete then query before the index hears about it
        self.assertTrue(a.delete("pay_013"))
        self.assertFalse(a.delete("pay_013"))
        self.assertEqual(len(a.global_index().lookup("cust_erin")), 2)
        self.assertEqual(len(a.audit_global_index().dangling), 1)
        self.assertEqual(len(a.find_by_customer_global("cust_erin").payments), 1)
        a.drain_all()
        self.assertEqual(len(a.global_index().lookup("cust_erin")), 1)
        local = a.shard(a.shard_for("pay_013")).lookup_customer("cust_erin")
        self.assertTrue(all(p.id != "pay_013" for p in local))


class ReattributionTest(unittest.TestCase):
    def test_changing_indexed_attribute(self):
        move = PartitionedStore(2, 2, IndexMode.ASYNC)
        move.put(pay("pay_100", "cust_x", 100, 1))
        move.drain_all()
        move.put(pay("pay_100", "cust_y", 100, 1))
        self.assertEqual(move.pending_index_updates(), 2)
        self.assertEqual(len(move.global_index().lookup("cust_x")), 1)
        audit = move.audit_global_index()
        self.assertTrue(len(audit.dangling) == 1 and len(audit.missing) == 1)
        move.drain_all()
        self.assertEqual(move.global_index().lookup("cust_x"), [])
        self.assertEqual(len(move.global_index().lookup("cust_y")), 1)
        self.assertTrue(not move.find_by_customer_scatter_gather("cust_x").payments
                        and len(move.find_by_customer_scatter_gather("cust_y").payments) == 1)

    def test_same_customer_overwrite_queues_nothing(self):
        s = PartitionedStore(2, 2, IndexMode.ASYNC)
        s.put(pay("pay_200", "cust_x", 100, 1))
        s.drain_all()
        s.put(pay("pay_200", "cust_x", 250, 1))
        self.assertTrue(s.pending_index_updates() == 0 and s.get("pay_200").amount.minor_units == 250)


class RebuildTest(unittest.TestCase):
    def test_rebuild_from_scan(self):
        rebuild = loaded(3, IndexMode.ASYNC)
        self.assertEqual(rebuild.rebuild_global_index(), 12)
        self.assertEqual(rebuild.pending_index_updates(), 0)
        self.assertTrue(rebuild.audit_global_index().consistent())
        self.assertTrue(rebuild.rebuild_global_index() == 12 and rebuild.global_index().entry_count() == 12)
        gi = rebuild.global_index()
        p0 = gi.partition_for("cust_alice")
        before = gi.requests(p0)
        gi.lookup("cust_alice")
        self.assertEqual(gi.requests(p0), before + 1)


class GuardsTest(unittest.TestCase):
    def test_money_and_constructors(self):
        self.assertEqual(str(Money.of_minor(12414, Currency.USD)), "124.14 USD")
        with self.assertRaises(ValueError):
            Money.of_minor(1, Currency.USD).plus(Money.of_minor(1, Currency.EUR))
        with self.assertRaises(ValueError):
            PartitionedStore(0, 1, IndexMode.SYNC)
        with self.assertRaises(ValueError):
            GlobalIndex(0)
        with self.assertRaises(TypeError):  # Java: NullPointerException
            Payment("p", None, "m", Money.of_minor(1, Currency.USD), 1)


if __name__ == "__main__":
    unittest.main()
