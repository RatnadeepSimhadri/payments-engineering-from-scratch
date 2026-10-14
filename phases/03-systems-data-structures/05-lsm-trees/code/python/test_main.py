"""Tests for phases/03-systems-data-structures/05-lsm-trees. Run: python3 -m unittest -v"""
import unittest

from main import (Entry, Hashing, IllegalStateError, JavaRandom, LsmTree, Memtable, SSTable, SimulatedDisk,
                  read_utf, write_utf)


class MemtableTest(unittest.TestCase):
    def test_sorted_and_overwrite(self):
        mem = Memtable()
        mem.put("b", Entry.of_value("2", 1))
        mem.put("a", Entry.of_value("1", 2))
        mem.put("b", Entry.of_value("22", 3))
        self.assertEqual(list(mem.entries().keys()), ["a", "b"])
        self.assertTrue(mem.size() == 2 and mem.get("b").value == "22" and mem.get("b").seq == 3)


class WireFormatTest(unittest.TestCase):
    def test_hash_matches_java_long(self):
        # FNV-1a 64 of "a" is the published test vector; the Java long is the same 64 bits.
        self.assertEqual(Hashing.fnv1a64("a"), 0xAF63DC4C8601EC8C)

    def test_modified_utf8_round_trip(self):
        out = bytearray()
        write_utf(out, "ké\u0000")
        self.assertEqual(bytes(out), b"\x00\x05k\xc3\xa9\xc0\x80")  # NUL is two bytes, as in DataOutputStream
        self.assertEqual(read_utf(bytes(out), 0), ("ké\u0000", 7))


class SSTableTest(unittest.TestCase):
    def setUp(self):
        self.disk = SimulatedDisk()
        run = {f"k{i:03d}": Entry.of_value(f"v{i}", i) for i in range(100)}
        run["k050"] = Entry.of_tombstone(500)
        self.run = dict(sorted(run.items()))
        self.sst = SSTable.write(self.disk, "t0.db", self.run)

    def test_encode_decode(self):
        sst = self.sst
        self.assertTrue(sst.entry_count == 100 and sst.min_key == "k000" and sst.max_key == "k099"
                        and sst.block_count() == 7)
        self.assertTrue(sst.get("k007").value == "v7" and sst.get("k007").seq == 7)
        self.assertTrue(sst.get("k050").tombstone and sst.get("k050").seq == 500)
        self.assertEqual(sst.get("k099").value, "v99")
        blocks_before = sst.block_reads
        self.assertTrue(sst.get("z") is None and sst.get("a") is None and sst.block_reads == blocks_before
                        and sst.range_negatives == 2)
        self.assertTrue(sst.block_for("k000") == 0 and sst.block_for("k015") == 0
                        and sst.block_for("k016") == 1 and sst.block_for("k099") == 6)

    def test_bloom_filter(self):
        sst = self.sst
        negatives = false_positives = 0
        for i in range(2_000):
            missing = f"k{i % 100:03d}z"  # inside range, never written
            before = sst.bloom_negatives
            e = sst.get(missing)
            if sst.bloom_negatives > before:
                negatives += 1
            elif e is None:
                false_positives += 1
        self.assertGreater(negatives, 1_900)
        self.assertLess(false_positives, 100)

    def test_read_all_and_immutability(self):
        self.assertEqual(self.sst.read_all(), self.run)
        with self.assertRaises(IllegalStateError):
            self.disk.write("t0.db", bytes(1))


class LsmBasicsTest(unittest.TestCase):
    def test_put_get_delete_through_flushes(self):
        lsm = LsmTree(SimulatedDisk(), 10, 3)
        for i in range(25):
            lsm.put(f"key{i}", f"v{i}")
        self.assertTrue(lsm.flushes == 2 and lsm.memtable_size() == 5)
        self.assertTrue(lsm.get("key24") == "v24" and lsm.get("key3") == "v3" and lsm.get("key12") == "v12")
        self.assertIsNone(lsm.get("nope"))
        lsm.put("key3", "new3")
        self.assertEqual(lsm.get("key3"), "new3")
        lsm.delete("key12")
        self.assertIsNone(lsm.get("key12"))
        lsm.flush()
        self.assertTrue(lsm.get("key12") is None and lsm.get("key3") == "new3")
        with self.assertRaises(ValueError):
            lsm.put("k", None)


class SizeTieredTest(unittest.TestCase):
    def test_compaction_agrees_with_model(self):
        d3 = SimulatedDisk()
        tiered = LsmTree(d3, 100, 4)
        rng = JavaRandom(5)
        model: dict[str, str] = {}
        for i in range(5_000):
            k = f"acct_{rng.next_int(800)}"
            if rng.next_int(10) == 0:
                tiered.delete(k)
                model.pop(k, None)
            else:
                v = f"bal_{i}"
                tiered.put(k, v)
                model[k] = v
        tiered.flush()
        self.assertTrue(tiered.compactions > 0 and tiered.check_invariants())
        self.assertLess(tiered.table_count(), tiered.flushes)
        self.assertTrue(all(tiered.get(f"acct_{i}") == model.get(f"acct_{i}") for i in range(800)))
        expected_scan = {k: model[k] for k in sorted(model) if "acct_10" <= k <= "acct_19"}
        self.assertEqual(tiered.range("acct_10", "acct_19"), expected_scan)
        self.assertGreater(tiered.write_amplification(), 1.0)
        self.assertTrue(d3.file_count() == tiered.table_count() and d3.files_deleted > 0)


class RecencyTrapTest(unittest.TestCase):
    def test_highest_seq_wins_across_tiers(self):
        trap = LsmTree(SimulatedDisk(), 3, 2)
        for k, v in (("x", "old"), ("p", "1"), ("o", "1")):       # table A (3 entries)
            trap.put(k, v)
        for k in ("q", "r", "s"):                                   # table B (3) -> AB (6)
            trap.put(k, "1")
        self.assertTrue(trap.table_count() == 1 and trap.get("x") == "old" and trap.tables()[0].entry_count == 6)
        for k, v in (("x", "new"), ("y", "1"), ("z", "1")):       # table C (3): separate tier
            trap.put(k, v)
        self.assertTrue(trap.table_count() == 2 and trap.check_invariants())
        self.assertEqual(trap.get("x"), "new")
        for k, v in (("x", "newest"), ("u", "1"), ("v", "1")):    # table D -> CD (5) -> joins AB -> one table
            trap.put(k, v)
        self.assertTrue(trap.table_count() == 1 and trap.get("x") == "newest"
                        and trap.tables()[0].entry_count == 10 and trap.check_invariants())


class TombstoneLifecycleTest(unittest.TestCase):
    def test_full_compaction_drops_tombstone(self):
        tomb = LsmTree(SimulatedDisk(), 2, 2)
        tomb.put("a", "1")
        tomb.put("b", "2")
        tomb.delete("a")
        tomb.put("c", "3")
        self.assertTrue(tomb.table_count() == 1 and tomb.get("a") is None and tomb.tables()[0].entry_count == 2)

    def test_partial_compaction_keeps_tombstone(self):
        keep = LsmTree(SimulatedDisk(), 2, 3)
        for k, v in (("a", "1"), ("b", "2"), ("c", "3"), ("d", "4"), ("e", "5"), ("f", "6"), ("g", "7"), ("h", "8")):
            keep.put(k, v)
        keep.delete("a")
        keep.put("i", "9")
        self.assertTrue(keep.table_count() == 3 and keep.get("a") is None)
        keep.put("j", "1")
        keep.put("k", "1")
        tombstone_kept = any((e := t.get("a")) is not None and e.tombstone for t in keep.tables())
        self.assertTrue(tombstone_kept and keep.get("a") is None)


class LimitsTest(unittest.TestCase):
    def test_limits(self):
        with self.assertRaises(ValueError):
            LsmTree(SimulatedDisk(), 0, 4)
        with self.assertRaises(ValueError):
            LsmTree(SimulatedDisk(), 10, 1)


if __name__ == "__main__":
    unittest.main()
