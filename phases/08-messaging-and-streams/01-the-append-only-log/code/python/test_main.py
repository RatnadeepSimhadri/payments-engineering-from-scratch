"""Tests for phases/08-messaging-and-streams/01-the-append-only-log. Run: python3 -m unittest -v"""
import unittest

from main import (
    Consumer,
    IndexEntry,
    InMemoryStorage,
    LogConfig,
    OffsetOutOfRangeError,
    OffsetStore,
    Partition,
    RecordCodec,
    Segment,
    Topic,
)


def b(s):
    return None if s is None else s.encode("utf-8")


def partition(id, config):
    return Partition(id, config, InMemoryStorage())


BIG = LogConfig(1 << 20, 4096, 1 << 30)


def filled_partition():
    p = partition(0, BIG)
    for i in range(50):
        p.append(b(f"k{i % 3}"), b(f"v{i}"), 1000 + i)
    return p


class CodecTest(unittest.TestCase):
    def test_round_trip_and_frame_layout(self):
        frame = RecordCodec.encode(42, 1234, b("k"), b("value"))
        self.assertEqual(len(frame), 4 + 24 + 1 + 5)
        self.assertEqual(RecordCodec.frame_size(b("k"), b("value")), len(frame))
        decoded = RecordCodec.decode(frame, 0)
        self.assertEqual(decoded.offset, 42)
        self.assertEqual(decoded.timestamp, 1234)
        self.assertEqual(decoded.key_string(), "k")
        self.assertEqual(decoded.value_string(), "value")
        self.assertEqual(RecordCodec.frame_length_at(frame, 0), len(frame))

    def test_null_key_encodes_as_minus_one(self):
        null_key = RecordCodec.encode(1, 1, None, b("v"))
        self.assertIsNone(RecordCodec.decode(null_key, 0).key)
        self.assertEqual(len(null_key), 4 + 24 + 1)

    def test_fnv1a32_vectors(self):
        self.assertEqual(RecordCodec.fnv1a32(b""), 0x811C9DC5)
        self.assertEqual(RecordCodec.fnv1a32(b("a")), 0xE40C292C)


class OffsetTest(unittest.TestCase):
    def test_offsets_are_monotonic_and_contiguous(self):
        p = partition(0, BIG)
        previous = -1
        contiguous = True
        for i in range(50):
            off = p.append(b(f"k{i % 3}"), b(f"v{i}"), 1000 + i)
            if off != previous + 1:
                contiguous = False
            previous = off
        self.assertTrue(contiguous)
        self.assertEqual(previous, 49)
        self.assertEqual(p.log_end_offset(), 50)
        self.assertEqual(p.log_start_offset(), 0)
        self.assertEqual(p.segment_count(), 1)

    def test_read_from_arbitrary_offset(self):
        p = filled_partition()
        mid = p.read(17, 5)
        self.assertEqual(len(mid), 5)
        self.assertEqual(mid[0].offset, 17)
        self.assertEqual(mid[4].offset, 21)
        self.assertEqual(mid[0].value_string(), "v17")
        self.assertEqual(mid[0].timestamp, 1017)
        self.assertEqual(p.read(50, 10), [])
        with self.assertRaises(OffsetOutOfRangeError):
            p.read(51, 1)
        with self.assertRaises(OffsetOutOfRangeError):
            p.read(-1, 1)


class SparseIndexTest(unittest.TestCase):
    def test_sparse_index_addresses_every_record(self):
        seg = Segment(100, InMemoryStorage().create(0, 100), 1 << 20, 100)
        for i in range(40):
            seg.append(b("key"), b(f"payload-{i}"), i)
        self.assertLess(len(seg.index_entries()), 40)
        self.assertGreaterEqual(len(seg.index_entries()), 2)
        self.assertEqual(seg.index_entries()[0], IndexEntry(0, 0))
        for off in range(100, 140):
            one = seg.read(off, 1)
            self.assertEqual(len(one), 1)
            self.assertEqual(one[0].offset, off)
            self.assertEqual(one[0].value_string(), f"payload-{off - 100}")


class SegmentRollTest(unittest.TestCase):
    def test_segments_roll_at_size_limit(self):
        rolled = partition(1, LogConfig(120, 40, 10_000))
        for i in range(10):
            rolled.append(b("k"), b("0123456789"), i)  # frame = 4+24+1+10 = 39 bytes; 3 per segment
        self.assertEqual(rolled.segment_count(), 4)
        self.assertEqual(rolled.segment_base_offsets(), [0, 3, 6, 9])
        across = rolled.read(2, 4)
        self.assertEqual(len(across), 4)
        self.assertEqual(across[0].offset, 2)
        self.assertEqual(across[3].offset, 5)
        self.assertEqual(len(rolled.read(9, 10)), 1)

    def test_oversized_record_lands_in_empty_segment(self):
        oversized = partition(2, LogConfig(50, 10, 10_000))
        big_offset = oversized.append(b("k"), b("this value is much longer than the fifty byte segment limit"), 1)
        self.assertEqual(big_offset, 0)
        self.assertEqual(len(oversized.read(0, 1)), 1)


class StorageAndRetentionTest(unittest.TestCase):
    def setUp(self):
        self.storage = InMemoryStorage()
        self.stored = Partition(7, LogConfig(120, 40, 250), self.storage)
        for i in range(10):
            self.stored.append(b("k"), b("0123456789"), i)

    def test_storage_holds_one_entry_per_segment(self):
        self.assertEqual(self.storage.base_offsets(7), [0, 3, 6, 9])
        self.assertEqual(self.storage.base_offsets(8), [])

    def test_retention_drops_oldest_segments(self):
        bytes_before = self.stored.size_bytes()
        dropped = self.stored.enforce_retention()
        self.assertEqual(dropped, 2)
        self.assertEqual(self.stored.segment_count(), 2)
        self.assertLess(self.stored.size_bytes(), bytes_before)
        self.assertLessEqual(self.stored.size_bytes(), 250)
        self.assertEqual(self.stored.log_start_offset(), 6)
        self.assertEqual(self.stored.log_end_offset(), 10)
        self.assertEqual(self.storage.base_offsets(7), [6, 9])
        with self.assertRaises(OffsetOutOfRangeError):
            self.stored.read(2, 1)
        self.assertEqual(self.stored.read(6, 1)[0].offset, 6)

    def test_active_segment_is_never_dropped(self):
        tiny = partition(4, LogConfig(50, 10, 10))
        tiny.append(b("k"), b("0123456789"), 1)
        tiny.append(b("k"), b("0123456789"), 2)
        tiny.enforce_retention()
        self.assertEqual(tiny.segment_count(), 1)
        self.assertEqual(tiny.log_end_offset(), 2)

    def test_committed_offset_retired_by_retention_resets_to_log_start(self):
        self.stored.enforce_retention()
        retired = OffsetStore()
        retired.commit("g4", 7, 2)
        self.assertEqual(Consumer(self.stored, "g4", retired).position(), 6)


class PartitionerTest(unittest.TestCase):
    def test_same_key_same_partition_order_preserved(self):
        topic = Topic("t", 4, BIG, InMemoryStorage())
        keys = ["alice", "bob", "carol", "dave", "erin"]
        seen = {}
        stable = True
        for i in range(100):
            k = keys[i % len(keys)]
            m = topic.append(b(k), b(f"v{i}"), i)
            if m.partition != topic.partition_for(b(k)):
                stable = False
            seen.setdefault(k, []).append(m.partition)
        self.assertTrue(stable)
        self.assertTrue(all(len(set(v)) == 1 for v in seen.values()))
        home = topic.partition_for(b("alice"))
        last_alice = -1
        ordered = True
        for rec in topic.partition(home).read(0, 1000):
            if rec.key_string() == "alice":
                n = int(rec.value_string()[1:])
                if n <= last_alice:
                    ordered = False
                last_alice = n
        self.assertTrue(ordered)
        self.assertEqual(last_alice, 95)
        self.assertEqual(topic.partition_for(b("bob")), topic.partition_for(b("bob")))
        self.assertTrue(0 <= topic.partition_for(b("anything")) < 4)

    def test_null_keys_rotate_round_robin(self):
        unkeyed = Topic("u", 3, BIG, InMemoryStorage())
        rr = [unkeyed.partition_for(None) for _ in range(4)]
        self.assertEqual(rr, [0, 1, 2, 0])


class ConsumerTest(unittest.TestCase):
    def setUp(self):
        self.p = filled_partition()
        self.offsets = OffsetStore()

    def test_position_semantics(self):
        c = Consumer(self.p, "g1", self.offsets)
        self.assertEqual(c.position(), 0)
        first = c.poll(10)
        self.assertEqual(len(first), 10)
        self.assertEqual(c.position(), 10)
        c.seek(45)
        self.assertEqual(len(c.poll(10)), 5)
        self.assertEqual(c.position(), 50)
        self.assertEqual(c.poll(10), [])
        self.assertEqual(c.position(), 50)
        with self.assertRaises(OffsetOutOfRangeError):
            c.seek(99)

    def test_commit_and_resume(self):
        worker = Consumer(self.p, "g2", self.offsets)
        worker.poll(10)
        worker.commit()
        self.assertEqual(self.offsets.committed("g2", 0), 10)
        worker.poll(5)
        self.assertEqual(worker.position(), 15)
        self.assertEqual(self.offsets.committed("g2", 0), 10)
        resumed = Consumer(self.p, "g2", self.offsets)
        self.assertEqual(resumed.position(), 10)
        self.assertEqual(resumed.poll(5)[0].offset, 10)
        self.assertEqual(Consumer(self.p, "g3", self.offsets).position(), 0)
        self.assertEqual(self.offsets.committed("nobody", 0), OffsetStore.NONE)

    def test_config_rejects_non_positive_segment_size(self):
        with self.assertRaises(ValueError):
            LogConfig(0, 1, 1)


if __name__ == "__main__":
    unittest.main()
