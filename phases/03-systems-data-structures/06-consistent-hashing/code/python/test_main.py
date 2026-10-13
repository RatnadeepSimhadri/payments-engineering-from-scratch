"""Tests for phases/03-systems-data-structures/06-consistent-hashing. Run: python3 -m unittest -v"""

import unittest

from main import HashRing, ModNSharder, account_ids, fraction_moved, hash_key, imbalance, load_per_node

SHARDS = [f"ledger-shard-{c}" for c in "abcdefghij"]


def ring_with(nodes, virtual_nodes=100):
    ring = HashRing(virtual_nodes=virtual_nodes)
    for node in nodes:
        ring.add_node(node)
    return ring


class HashingTest(unittest.TestCase):
    def test_hash_key_matches_java_and_go(self) -> None:
        self.assertEqual(hash_key("acct-000123"), 0xD43CBEF4D0200057)
        self.assertEqual(HashRing.point_hash("ledger-shard-a", 0), 0xFB3BE259E2C64489)

    def test_lookup_matches_java_and_go(self) -> None:
        ring = ring_with(SHARDS)
        self.assertEqual(ring.lookup("acct-000123"), "ledger-shard-c")
        self.assertEqual(ring.lookup_n("acct-000123", 3), ["ledger-shard-c", "ledger-shard-d", "ledger-shard-e"])
        small = ring_with(["alpha", "beta", "gamma"], virtual_nodes=3)
        self.assertEqual(small.lookup("acct-000001"), "beta")
        self.assertEqual(small.lookup_n("acct-000001", 3), ["beta", "alpha", "gamma"])


class RingStructureTest(unittest.TestCase):
    def test_points_are_sorted_and_counted(self) -> None:
        ring = ring_with(SHARDS[:3], virtual_nodes=50)
        self.assertEqual(ring.point_count(), 150)
        self.assertEqual(ring._hashes, sorted(ring._hashes))
        self.assertEqual(ring.nodes(), SHARDS[:3])

    def test_add_duplicate_and_remove_missing_are_errors(self) -> None:
        ring = ring_with(["alpha"])
        with self.assertRaises(ValueError):
            ring.add_node("alpha")
        with self.assertRaises(ValueError):
            ring.remove_node("beta")
        with self.assertRaises(ValueError):
            HashRing(virtual_nodes=0)

    def test_empty_ring_lookup_is_an_error(self) -> None:
        with self.assertRaises(LookupError):
            HashRing().lookup("acct-000001")
        with self.assertRaises(LookupError):
            HashRing().lookup_n("acct-000001", 2)

    def test_single_node_owns_everything(self) -> None:
        ring = ring_with(["only"])
        for key in account_ids(200):
            self.assertEqual(ring.lookup(key), "only")

    def test_lookup_is_deterministic_and_wraps(self) -> None:
        ring = ring_with(SHARDS)
        keys = account_ids(500)
        first = ring.assign(keys)
        self.assertEqual(first, ring.assign(keys))
        # A key hashing above the highest point wraps to the first point.
        above = [k for k in account_ids(20_000) if hash_key(k) > ring._hashes[-1]]
        self.assertGreater(len(above), 0)
        for key in above:
            self.assertEqual(ring.lookup(key), ring._owners[0])


class MonotonicityTest(unittest.TestCase):
    """The invariant Karger et al. call monotonicity: membership changes only move keys to or from
    the node that changed."""

    def test_adding_a_node_only_moves_keys_to_it(self) -> None:
        keys = account_ids(5_000)
        ring = ring_with(SHARDS)
        before = ring.assign(keys)
        ring.add_node("ledger-shard-k")
        after = ring.assign(keys)
        for key in keys:
            self.assertTrue(after[key] == before[key] or after[key] == "ledger-shard-k", key)

    def test_removing_a_node_only_moves_its_keys(self) -> None:
        keys = account_ids(5_000)
        ring = ring_with(SHARDS)
        before = ring.assign(keys)
        ring.remove_node("ledger-shard-c")
        after = ring.assign(keys)
        for key in keys:
            if before[key] != "ledger-shard-c":
                self.assertEqual(after[key], before[key], key)
            else:
                self.assertNotEqual(after[key], "ledger-shard-c")

    def test_fraction_moved_is_about_one_over_n_plus_one(self) -> None:
        keys = account_ids(20_000)
        ring = ring_with(SHARDS)
        before = ring.assign(keys)
        ring.add_node("ledger-shard-k")
        moved = fraction_moved(before, ring.assign(keys))
        self.assertGreater(moved, 0.05)
        self.assertLess(moved, 0.14)  # ideal 1/11 = 0.091

    def test_mod_n_moves_almost_everything(self) -> None:
        keys = account_ids(20_000)
        before = ModNSharder(SHARDS).assign(keys)
        after = ModNSharder(SHARDS + ["ledger-shard-k"]).assign(keys)
        self.assertGreater(fraction_moved(before, after), 0.85)  # expected 10/11 = 0.909

    def test_fraction_moved_of_identical_assignments_is_zero(self) -> None:
        keys = account_ids(100)
        ring = ring_with(SHARDS)
        self.assertEqual(fraction_moved(ring.assign(keys), ring.assign(keys)), 0.0)
        self.assertEqual(fraction_moved({}, {}), 0.0)


class BalanceTest(unittest.TestCase):
    def test_virtual_nodes_improve_balance(self) -> None:
        keys = account_ids(20_000)
        one = imbalance(ring_with(SHARDS, virtual_nodes=1).assign(keys), SHARDS)
        hundred = imbalance(ring_with(SHARDS, virtual_nodes=100).assign(keys), SHARDS)
        self.assertGreater(one[0], 2.0)      # one point per node: the busiest shard has 3x its share
        self.assertLess(hundred[0], 1.25)    # 100 points per node: within 25% of ideal
        self.assertLess(hundred[1], one[1])

    def test_every_node_gets_some_load(self) -> None:
        keys = account_ids(20_000)
        counts = load_per_node(ring_with(SHARDS).assign(keys), SHARDS)
        self.assertEqual(sum(counts.values()), 20_000)
        self.assertTrue(all(c > 0 for c in counts.values()))


class ReplicationTest(unittest.TestCase):
    def test_lookup_n_returns_distinct_nodes_starting_with_owner(self) -> None:
        ring = ring_with(SHARDS)
        for key in account_ids(300):
            prefs = ring.lookup_n(key, 3)
            self.assertEqual(len(prefs), 3)
            self.assertEqual(len(set(prefs)), 3)
            self.assertEqual(prefs[0], ring.lookup(key))

    def test_lookup_n_caps_at_node_count(self) -> None:
        ring = ring_with(SHARDS[:3])
        prefs = ring.lookup_n("acct-000001", 8)
        self.assertEqual(sorted(prefs), SHARDS[:3])
        with self.assertRaises(ValueError):
            ring.lookup_n("acct-000001", 0)

    def test_replicas_survive_losing_the_owner(self) -> None:
        ring = ring_with(SHARDS)
        key = "acct-000123"
        prefs = ring.lookup_n(key, 3)
        ring.remove_node(prefs[0])
        # the first replica becomes the owner: the data is already there
        self.assertEqual(ring.lookup(key), prefs[1])


if __name__ == "__main__":
    unittest.main()
