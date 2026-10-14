"""Tests for phases/06-replication-and-consensus/03-quorums. Run: python3 -m unittest -v"""
import unittest

from main import NONE, Experiment, QuorumConfig, QuorumNotReached, QuorumStore, Versioned, java_string_hash

KEY = "merchant:m-77:settlement-currency"
STRICT_222 = QuorumConfig(3, 2, 2, False, False)


def home_of(key: str = KEY) -> list:
    return QuorumStore(5, STRICT_222, 1, 0.0).home_nodes(key)


class ConfigAndVersionTest(unittest.TestCase):
    def test_overlap_predicate(self):
        self.assertTrue(STRICT_222.overlaps())
        self.assertFalse(QuorumConfig(3, 1, 2, False, False).overlaps())

    def test_invalid_configs_rejected(self):
        with self.assertRaises(ValueError):
            QuorumConfig(3, 4, 1, False, False)
        with self.assertRaises(ValueError):
            QuorumConfig(3, 1, 0, False, False)
        with self.assertRaises(ValueError):
            QuorumStore(2, STRICT_222, 1, 0.0)

    def test_lww_versions(self):
        self.assertTrue(Versioned("b", 2, 0).newer_than(Versioned("a", 1, 9)))
        self.assertTrue(Versioned("b", 2, 3).newer_than(Versioned("a", 2, 1)))
        self.assertFalse(Versioned("a", 2, 1).newer_than(Versioned("a", 2, 1)))
        self.assertTrue(Versioned("a", 0, 0).newer_than(NONE))

    def test_java_string_hash_matches_java(self):
        self.assertEqual(java_string_hash(""), 0)
        self.assertEqual(java_string_hash("hello"), 99162322)
        self.assertEqual(java_string_hash("polygenelubricants"), -2147483648)


class PlacementAndQuorumTest(unittest.TestCase):
    def test_placement(self):
        s = QuorumStore(5, STRICT_222, 1, 0.0)
        self.assertEqual(len(s.home_nodes(KEY)), 3)
        self.assertTrue(len(s.fallback_nodes(KEY)) == 2 and s.home_nodes(KEY)[0] not in s.fallback_nodes(KEY))
        self.assertEqual(s.home_nodes(KEY), QuorumStore(5, STRICT_222, 99, 0.5).home_nodes(KEY))

    def test_strict_quorum_lifecycle(self):
        s = QuorumStore(5, STRICT_222, 1, 0.0)
        home = s.home_nodes(KEY)
        self.assertEqual(s.put(home[0], KEY, "EUR"), 3)
        r = s.get(home[2], KEY)
        self.assertTrue(r.value() == "EUR" and r.responses == 2)
        self.assertEqual(r.respondents[0], home[2])
        self.assertEqual(s.replicas_holding(KEY, "EUR"), 3)

        # the guarantee: R + W > N with strict quorums never misses the last write, even with a node down
        s.network.crash(home[1])
        s.put(home[0], KEY, "GBP")
        s.network.recover(home[1])
        s.network.crash(home[0])
        overlapping = s.get(home[1], KEY)
        self.assertEqual(overlapping.value(), "GBP")
        self.assertEqual(overlapping.responses, 2)

        # failure is reported, never faked
        s.network.crash(home[2])
        with self.assertRaises(QuorumNotReached):
            s.put(home[1], KEY, "CHF")
        with self.assertRaises(QuorumNotReached):
            s.get(home[1], KEY)
        self.assertEqual(s.nodes[home[1]].data[KEY].value, "CHF")

    def test_weak_quorums_miss_each_other(self):
        home = home_of()
        weak = QuorumStore(5, QuorumConfig(3, 1, 1, False, False), 1, 0.0)
        weak.put(home[0], KEY, "EUR")
        weak.network.crash(home[2])
        weak.put(home[1], KEY, "GBP")
        weak.network.recover(home[2])
        self.assertEqual(weak.get(home[2], KEY).value(), "EUR")
        self.assertEqual(weak.get(home[0], KEY).value(), "GBP")


class SloppyRepairAndEntropyTest(unittest.TestCase):
    def test_sloppy_quorum_and_hinted_handoff(self):
        home = home_of()
        sloppy = QuorumStore(5, QuorumConfig(3, 2, 2, True, False), 1, 0.0)
        sloppy.network.crash(home[1])
        sloppy.network.crash(home[2])
        acks = sloppy.put(home[0], KEY, "EUR")
        self.assertTrue(acks >= 2 and sloppy.hinted_writes == 2)
        self.assertEqual(sloppy.nodes[sloppy.fallback_nodes(KEY)[0]].hint_count(), 1)
        self.assertEqual(sloppy.replicas_holding(KEY, "EUR"), 1)
        strict_same = QuorumStore(5, STRICT_222, 1, 0.0)
        strict_same.network.crash(home[1])
        strict_same.network.crash(home[2])
        with self.assertRaises(QuorumNotReached):
            strict_same.put(home[0], KEY, "EUR")
        sloppy.network.recover(home[1])
        sloppy.network.recover(home[2])
        sloppy.network.crash(home[0])
        self.assertIsNone(sloppy.get(home[1], KEY).value())
        self.assertEqual(sloppy.handoff(), 2)
        self.assertEqual(sloppy.get(home[1], KEY).value(), "EUR")
        self.assertEqual(sloppy.nodes[sloppy.fallback_nodes(KEY)[0]].hint_count(), 0)
        self.assertEqual(sloppy.handoff(), 0)

    def test_read_repair(self):
        home = home_of()
        rr = QuorumStore(5, QuorumConfig(3, 2, 2, False, True), 1, 0.0)
        rr.network.crash(home[2])
        rr.put(home[0], KEY, "EUR")
        rr.network.recover(home[2])
        self.assertEqual(rr.replicas_holding(KEY, "EUR"), 2)
        rr.network.crash(home[0])
        repaired = rr.get(home[1], KEY)
        self.assertTrue(repaired.repaired == 1 and rr.replicas_holding(KEY, "EUR") == 3)
        self.assertEqual(rr.get(home[1], KEY).repaired, 0)
        self.assertFalse(rr.nodes[home[1]].put(KEY, Versioned("OLD", 0, 0)))

    def test_lww_loses_concurrent_update(self):
        lww = QuorumStore(5, STRICT_222, 1, 0.0)
        bal = "wallet:w-9:balance"
        lww.put(0, bal, "100.00 USD")
        lww.put(1, bal, "70.00 USD")
        lww.put(3, bal, "150.00 USD")
        self.assertEqual(lww.get(2, bal).value(), "150.00 USD")
        self.assertEqual(lww.replicas_holding(bal, "70.00 USD"), 0)

    def test_anti_entropy(self):
        ae = QuorumStore(5, QuorumConfig(3, 2, 2, True, False), 1, 0.0)
        ae.network.crash(2)
        for i in range(8):
            ae.put(0 if i % 5 == 2 else i % 5, f"cfg:{i}", f"value{i}")
        ae.network.recover(2)
        disagree_before = sum(1 for i in range(8) if not ae.replicas_agree(f"cfg:{i}"))
        ae.anti_entropy()
        disagree_after = sum(1 for i in range(8) if not ae.replicas_agree(f"cfg:{i}"))
        self.assertGreater(disagree_before, 0)
        self.assertEqual(disagree_after, 0)
        self.assertEqual(ae.anti_entropy(), 0)


class PropertyTest(unittest.TestCase):
    def test_stale_reads_across_seeds(self):
        strict_stale = weak_stale = sloppy_stale = 0
        for seed in range(1, 13):
            strict_stale += Experiment.run(STRICT_222, 5, seed, 0.05, 300, 20).stale_reads
            strict_stale += Experiment.run(QuorumConfig(3, 3, 1, False, False), 5, seed, 0.05, 300, 20).stale_reads
            weak_stale += Experiment.run(QuorumConfig(3, 1, 1, False, False), 5, seed, 0.05, 300, 20).stale_reads
            sloppy_stale += Experiment.run(QuorumConfig(3, 2, 2, True, False), 5, seed, 0.05, 300, 20).stale_reads
        self.assertEqual(strict_stale, 0)
        self.assertGreater(weak_stale, 0)
        self.assertGreater(sloppy_stale, 0)

    def test_determinism(self):
        a = Experiment.run(QuorumConfig(3, 2, 2, True, True), 5, 42, 0.1, 200, 15)
        b = Experiment.run(QuorumConfig(3, 2, 2, True, True), 5, 42, 0.1, 200, 15)
        self.assertEqual(a, b)

    def test_partition(self):
        home = home_of()
        part = QuorumStore(5, STRICT_222, 1, 0.0)
        part.network.partition({home[0]})
        with self.assertRaises(QuorumNotReached):
            part.put(home[0], KEY, "EUR")
        self.assertEqual(part.put(home[1], KEY, "EUR"), 2)
        part.network.heal()
        self.assertEqual(part.get(home[0], KEY).responses, 2)


if __name__ == "__main__":
    unittest.main()
