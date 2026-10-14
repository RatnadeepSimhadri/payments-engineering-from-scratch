"""Tests for phases/07-storage-and-transactions/01-partitioning-and-rebalancing. Run: python3 -m unittest -v"""
import unittest

from main import (
    ConsistentHashRing,
    HashPartitioner,
    HotKeyDetector,
    PartitionMigration,
    Partitioner,
    RangePartitioner,
    Rebalancer,
    RoutingTable,
    StorageNode,
    WrongOwnerException,
    moved_fraction,
    payment_ids,
)

KEYS = payment_ids(20_000)


class RangePartitionerTest(unittest.TestCase):
    def test_boundaries_are_half_open(self):
        r = RangePartitioner(["f", "m", "s"])
        self.assertEqual(r.partition_count(), 4)
        self.assertEqual(r.partition_of("acme"), 0)
        self.assertEqual(r.partition_of("f"), 1)
        self.assertEqual(r.partition_of("megamart"), 2)
        self.assertEqual(r.partition_of("zenith"), 3)
        self.assertLessEqual(r.partition_of("alpha"), r.partition_of("omega"))

    def test_rejects_unsorted_split_points(self):
        with self.assertRaises(ValueError):
            RangePartitioner(["m", "f"])


class HashPartitionerTest(unittest.TestCase):
    def test_deterministic_and_fnv_basis(self):
        h = HashPartitioner(16)
        self.assertEqual(h.partition_of("pay_1"), h.partition_of("pay_1"))
        self.assertEqual(Partitioner.fnv1a64(""), 0xCBF29CE484222325)

    def test_in_range_and_uniform(self):
        h = HashPartitioner(16)
        spread = HotKeyDetector(16)
        in_range = True
        for k in KEYS:
            p = h.partition_of(k)
            if p < 0 or p >= 16:
                in_range = False
            spread.record(p)
        self.assertTrue(in_range)
        self.assertLess(spread.skew(), 1.2)

    def test_rejects_zero_partitions(self):
        with self.assertRaises(ValueError):
            HashPartitioner(0)


class HotKeyTest(unittest.TestCase):
    def test_single_key_skew_is_partition_count(self):
        h = HashPartitioner(16)
        hot = HotKeyDetector(16)
        for _ in range(1000):
            hot.record(h.partition_of("megamart"))
        self.assertEqual(hot.skew(), 16.0)

    def test_salting_spreads_and_is_stable(self):
        h = HashPartitioner(16)
        salted = HotKeyDetector(16)
        for i in range(1000):
            salted.record(h.partition_of(HotKeyDetector.salt("megamart", f"pay_{i}", 8)))
        touched = sum(1 for p in range(16) if salted.hits(p) > 0)
        self.assertTrue(touched >= 4 and salted.skew() < 8.0)
        self.assertEqual(HotKeyDetector.salt("m", "x", 8), HotKeyDetector.salt("m", "x", 8))
        self.assertTrue(HotKeyDetector.salt("megamart", "pay_1", 8).startswith("megamart#"))


class RoutingTableTest(unittest.TestCase):
    def test_versions_and_assignment(self):
        t = RoutingTable(HashPartitioner(8), ["a", "b"])
        self.assertTrue(t.owner_of(0) == "a" and t.owner_of(1) == "b" and t.owner_of(7) == "b")
        self.assertEqual(t.version(), 1)
        t.assign(0, "b")
        self.assertTrue(t.version() == 2 and t.owner_of(0) == "b")
        t.assign(0, "b")
        self.assertEqual(t.version(), 2)
        self.assertEqual(t.node_for("pay_42"), t.owner_of(t.partitioner().partition_of("pay_42")))


class RebalancerTest(unittest.TestCase):
    def test_add_then_remove(self):
        fixed = RoutingTable(HashPartitioner(64), ["a", "b", "c", "d"])
        before = {k: fixed.node_for(k) for k in KEYS}
        plan = Rebalancer.plan_add_node(fixed, "e")
        self.assertEqual(len(plan), 12)
        self.assertTrue(all(m.to == "e" for m in plan))
        Rebalancer.apply(fixed, plan)
        load = fixed.partitions_per_node().values()
        self.assertLessEqual(max(load) - min(load), 1)
        moved = moved_fraction(KEYS, before.get, fixed.node_for)
        self.assertTrue(0.15 < moved < 0.23)
        self.assertTrue(all(before[k] == fixed.node_for(k) for k in KEYS if fixed.node_for(k) != "e"))
        with self.assertRaises(ValueError):
            Rebalancer.plan_add_node(fixed, "a")

        removal = Rebalancer.plan_remove_node(fixed, "e")
        self.assertTrue(len(removal) == 12 and all(m.from_ == "e" for m in removal))
        Rebalancer.apply(fixed, removal)
        self.assertTrue(len(fixed.nodes()) == 4 and "e" not in fixed.nodes())

    def test_cannot_remove_last_node(self):
        with self.assertRaises(RuntimeError):
            Rebalancer.plan_remove_node(RoutingTable(HashPartitioner(4), ["only"]), "only")


class ModNVersusRingTest(unittest.TestCase):
    def test_mod_n_moves_most_keys(self):
        mod4, mod5 = HashPartitioner(4), HashPartitioner(5)
        naive = moved_fraction(KEYS, lambda k: str(mod4.partition_of(k)), lambda k: str(mod5.partition_of(k)))
        self.assertGreater(naive, 0.70)

    def test_ring_moves_about_one_fifth(self):
        ring = ConsistentHashRing(150)
        for n in ["a", "b", "c", "d"]:
            ring.add_node(n)
        self.assertEqual(ring.size(), 600)
        before = {k: ring.node_for(k) for k in KEYS}
        ring.add_node("e")
        moved = moved_fraction(KEYS, before.get, ring.node_for)
        self.assertTrue(0.14 < moved < 0.27)
        self.assertTrue(all(ring.node_for(k) == "e" for k in KEYS if before[k] != ring.node_for(k)))
        ring.remove_node("e")
        self.assertTrue(all(before[k] == ring.node_for(k) for k in KEYS))


class MigrationTest(unittest.TestCase):
    def test_copy_dual_write_cutover_drop(self):
        t2 = RoutingTable(HashPartitioner(8), ["a", "b"])
        a, e = StorageNode("a", t2), StorageNode("e", t2)
        p7 = [k for k in KEYS if t2.partitioner().partition_of(k) == 6]
        for k in p7[:100]:
            a.put(k, "v1")
        with self.assertRaises(WrongOwnerException):
            e.put(p7[0], "x")
        mig = PartitionMigration(6, a, e, t2)
        self.assertIs(mig.state(), PartitionMigration.State.PLANNED)
        with self.assertRaises(RuntimeError):
            mig.cut_over()
        mig.write(p7[100], "pre-copy")
        mig.start_copy()
        self.assertTrue(mig.copied == 101 and mig.state() is PartitionMigration.State.DUAL_WRITE)
        mig.write(p7[101], "during")
        mig.write(p7[0], "v1-updated")
        self.assertEqual(mig.dual_writes, 2)
        self.assertEqual(a.get(p7[0]), "v1-updated")
        outside = next(k for k in KEYS if t2.partitioner().partition_of(k) != 6)
        with self.assertRaises(ValueError):
            mig.write(outside, "x")
        v_before = t2.version()
        mig.cut_over()
        self.assertTrue(t2.version() == v_before + 1 and t2.owner_of(6) == "e")
        with self.assertRaises(WrongOwnerException) as ctx:
            a.get(p7[0])
        self.assertTrue(ctx.exception.current_version == t2.version() and ctx.exception.partition == 6)
        self.assertTrue(e.get(p7[100]) == "pre-copy" and e.get(p7[101]) == "during" and e.get(p7[0]) == "v1-updated")
        mig.write(p7[102], "post-cutover")
        self.assertTrue(e.get(p7[102]) == "post-cutover" and a.size() == 102)
        dropped = mig.drop_source()
        self.assertTrue(dropped == 102 and a.size() == 0 and mig.state() is PartitionMigration.State.DONE)
        self.assertEqual(e.size(), 103)
        with self.assertRaises(ValueError):
            PartitionMigration(6, a, e, t2)


if __name__ == "__main__":
    unittest.main()
