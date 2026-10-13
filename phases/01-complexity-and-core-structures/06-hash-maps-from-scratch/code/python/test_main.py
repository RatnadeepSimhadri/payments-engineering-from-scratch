"""Tests for phases/01-complexity-and-core-structures/06-hash-maps-from-scratch. Run: python3 -m unittest -v"""
import unittest

from main import (
    ChainedHashMap,
    IdempotencyStore,
    OpenAddressingHashMap,
    StoredResponse,
    constant_hasher,
    fnv1a,
    index_for,
    keys,
)


class HasherTest(unittest.TestCase):
    def test_fnv1a_empty_is_offset_basis(self):
        self.assertEqual(fnv1a(""), 0x811C9DC5)

    def test_fnv1a_published_vector(self):
        self.assertEqual(fnv1a("a"), 0xE40C292C)

    def test_index_for_stays_in_table(self):
        for cap in (8, 16, 1024):
            idx = index_for(0xDEADBEEF, cap)
            self.assertTrue(0 <= idx < cap)

    def test_non_power_of_two_capacity_rejected(self):
        with self.assertRaises(ValueError):
            ChainedHashMap(fnv1a, 12)
        with self.assertRaises(ValueError):
            OpenAddressingHashMap(fnv1a, 12)


class SharedContractMixin:
    """Both tables must pass the same tests: a learner switching implementations should notice nothing."""

    def make(self):
        raise NotImplementedError

    def test_empty_map(self):
        m = self.make()
        self.assertEqual(m.size(), 0)
        self.assertIsNone(m.get("nope"))
        self.assertFalse(m.contains_key("nope"))

    def test_put_get_replace_remove(self):
        m = self.make()
        self.assertIsNone(m.put("a", 1))
        self.assertEqual(m.put("a", 2), 1)
        self.assertEqual(m.size(), 1)
        self.assertEqual(m.get("a"), 2)
        self.assertTrue(m.contains_key("a"))
        self.assertEqual(m.remove("a"), 2)
        self.assertIsNone(m.remove("a"))
        self.assertEqual(m.size(), 0)

    def test_none_key_rejected(self):
        with self.assertRaises(TypeError):
            self.make().put(None, 1)

    def test_resize_preserves_every_entry(self):
        m = self.make()
        ks = keys(1_000)
        for i, k in enumerate(ks):
            m.put(k, i)
        self.assertEqual(m.size(), 1000)
        self.assertGreater(m.capacity(), 8)
        self.assertLessEqual(m.load_factor(), 0.75)
        for i, k in enumerate(ks):
            self.assertEqual(m.get(k), i, k)

    def test_removals_leave_other_keys_intact(self):
        m = self.make()
        ks = keys(1_000)
        for i, k in enumerate(ks):
            m.put(k, i)
        for k in ks[::3]:
            m.remove(k)
        for i, k in enumerate(ks):
            if i % 3 == 0:
                self.assertIsNone(m.get(k), k)
            else:
                self.assertEqual(m.get(k), i, k)
        self.assertEqual(m.size(), 1000 - 334)


class ChainedContractTest(SharedContractMixin, unittest.TestCase):
    def make(self):
        return ChainedHashMap(fnv1a)

    def test_resizes_when_seventh_key_exceeds_load(self):
        m = ChainedHashMap(fnv1a)
        for i in range(7):
            m.put(f"k{i}", i)
        self.assertEqual(m.resizes(), 1)
        self.assertEqual(m.capacity(), 16)


class OpenAddressingContractTest(SharedContractMixin, unittest.TestCase):
    def make(self):
        return OpenAddressingHashMap(fnv1a)

    def test_resizes_when_fifth_key_exceeds_load(self):
        m = OpenAddressingHashMap(fnv1a)
        for i in range(5):
            m.put(f"k{i}", i)
        self.assertEqual(m.resizes(), 1)
        self.assertEqual(m.capacity(), 16)


class TombstoneTest(unittest.TestCase):
    """A constant hasher makes every slot position predictable."""

    def setUp(self):
        self.t = OpenAddressingHashMap(constant_hasher, 16)
        self.t.put("A", "a")
        self.t.put("B", "b")
        self.t.put("C", "c")

    def test_cluster_occupies_consecutive_slots(self):
        self.assertEqual([self.t.slot_of(k) for k in "ABC"], [0, 1, 2])

    def test_remove_leaves_tombstone_and_keeps_chain_reachable(self):
        self.t.remove("B")
        self.assertEqual(self.t.tombstones(), 1)
        self.assertEqual(self.t.size(), 2)
        self.assertEqual(self.t.get("C"), "c")  # probe walks through B's grave
        self.assertIsNone(self.t.get("B"))
        self.assertFalse(self.t.contains_key("B"))

    def test_insert_reuses_tombstone(self):
        self.t.remove("B")
        self.t.put("D", "d")
        self.assertEqual(self.t.slot_of("D"), 1)
        self.assertEqual(self.t.tombstones(), 0)
        self.assertEqual(self.t.collisions(), 0 + 1 + 2 + 1)
        self.t.put("C", "c2")  # replace must find C in slot 2, not create a duplicate
        self.assertEqual(self.t.slot_of("C"), 2)
        self.assertEqual(self.t.get("C"), "c2")
        self.assertEqual(self.t.size(), 3)

    def test_resize_drops_tombstones(self):
        r = OpenAddressingHashMap(fnv1a, 8)
        for i in range(4):
            r.put(f"x{i}", i)
        for i in range(4):
            r.remove(f"x{i}")
        self.assertEqual((r.tombstones(), r.size()), (4, 0))
        r.put("y0", 0)  # 0 + 4 + 1 > 0.5 * 8 forces a resize
        r.put("y1", 1)
        self.assertEqual(r.tombstones(), 0)
        self.assertEqual(r.capacity(), 16)
        self.assertEqual(r.get("y1"), 1)


class WorstCaseTest(unittest.TestCase):
    def setUp(self):
        self.ks = keys(64)
        self.bad_chain = ChainedHashMap(constant_hasher)
        self.bad_open = OpenAddressingHashMap(constant_hasher)
        for i, k in enumerate(self.ks):
            self.bad_chain.put(k, i)
            self.bad_open.put(k, i)

    def test_chaining_degrades_to_one_list_but_stays_correct(self):
        for i, k in enumerate(self.ks):
            self.assertEqual(self.bad_chain.get(k), i)
        self.assertEqual(self.bad_chain.collisions(), 63)
        self.assertEqual(self.bad_chain.max_chain_length(), 64)

    def test_probing_degrades_to_one_cluster_but_stays_correct(self):
        for i, k in enumerate(self.ks):
            self.assertEqual(self.bad_open.get(k), i)
        self.assertEqual(self.bad_open.collisions(), sum(range(64)))  # 2016
        self.assertEqual(self.bad_open.max_probe_run(), 64)

    def test_good_hash_is_measurably_better(self):
        good = ChainedHashMap(fnv1a)
        for i, k in enumerate(self.ks):
            good.put(k, i)
        self.assertLess(good.collisions(), self.bad_chain.collisions() // 2)
        self.assertLessEqual(good.max_chain_length(), 4)


class IdempotencyStoreTest(unittest.TestCase):
    def test_handler_runs_once_per_key(self):
        store = IdempotencyStore(OpenAddressingHashMap(fnv1a))
        runs = []
        first = store.handle("k1", lambda: (runs.append(1), StoredResponse(201, "created"))[1])
        second = store.handle("k1", lambda: (runs.append(1), StoredResponse(500, "should not run"))[1])
        self.assertFalse(first.replayed)
        self.assertEqual(first.response.status, 201)
        self.assertTrue(second.replayed)
        self.assertEqual(second.response, first.response)
        self.assertEqual(len(runs), 1)
        self.assertEqual(store.handler_runs(), 1)

    def test_different_key_is_fresh(self):
        store = IdempotencyStore(ChainedHashMap(fnv1a))
        store.handle("k1", lambda: StoredResponse(201, "a"))
        other = store.handle("k2", lambda: StoredResponse(201, "b"))
        self.assertFalse(other.replayed)
        self.assertEqual(store.size(), 2)


if __name__ == "__main__":
    unittest.main()
