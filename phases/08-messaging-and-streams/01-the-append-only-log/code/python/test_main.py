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
    Segment,
    Topic,
    decode,
    encode,
    fnv1a32,
    frame_length_at,
    frame_size,
)

BIG = LogConfig(segment_bytes=1 << 20, index_interval_bytes=4096, retention_bytes=1 << 30)


def partition(pid: int, config: LogConfig) -> Partition:
    return Partition(pid, config, InMemoryStorage())


def filled_partition(n: int = 50) -> Partition:
    p = partition(0, BIG)
    for i in range(n):
        p.append(f"k{i % 3}".encode(), f"v{i}".encode(), 1000 + i)
    return p


class CodecTest(unittest.TestCase):
    def test_frame_layout_and_round_trip(self):
        frame = encode(42, 1234, b"k", b"value")
        self.assertEqual(len(frame), 4 + 24 + 1 + 5)
        self.assertEqual(frame_size(b"k", b"value"), len(frame))
        rec = decode(frame, 0)
        self.assertEqual((rec.offset, rec.timestamp, rec.key, rec.value), (42, 1234, b"k", b"value"))
        self.assertEqual(frame_length_at(frame, 0), len(frame))

    def test_null_key_is_minus_one(self):
        frame = encode(1, 1, None, b"v")
        self.assertEqual(len(frame), 4 + 24 + 1)
        self.assertIsNone(decode(frame, 0).key)

    def test_fnv1a32_known_vectors(self):
        self.assertEqual(fnv1a32(b""), 0x811C9DC5)
        self.assertEqual(fnv1a32(b"a"), 0xE40C292C)


class OffsetTest(unittest.TestCase):
    def test_offsets_monotonic_and_contiguous(self):
        p = partition(0, BIG)
        offsets = [p.append(b"k", f"v{i}".encode(), i) for i in range(50)]
        self.assertEqual(offsets, list(range(50)))
        self.assertEqual((p.log_start_offset(), p.log_end_offset()), (0, 50))
        self.assertEqual(p.segment_count(), 1)

    def test_read_from_arbitrary_offset(self):
        p = filled_partition()
        mid = p.read(17, 5)
        self.assertEqual([r.offset for r in mid], [17, 18, 19, 20, 21])
        self.assertEqual(mid[0].value_string(), "v17")
        self.assertEqual(mid[0].timestamp, 1017)

    def test_read_at_end_is_empty_and_beyond_is_error(self):
        p = filled_partition()
        self.assertEqual(p.read(50, 10), [])
        with self.assertRaises(OffsetOutOfRangeError):
            p.read(51, 1)
        with self.assertRaises(OffsetOutOfRangeError):
            p.read(-1, 1)


class SparseIndexTest(unittest.TestCase):
    def test_index_is_sparse_and_every_offset_is_addressable(self):
        seg = Segment(100, InMemoryStorage().create(0, 100), 1 << 20, 100)
        for i in range(40):
            seg.append(b"key", f"payload-{i}".encode(), i)
        entries = seg.index_entries()
        self.assertGreaterEqual(len(entries), 2)
        self.assertLess(len(entries), 40)
        self.assertEqual(entries[0], IndexEntry(0, 0))
        for off in range(100, 140):
            one = seg.read(off, 1)
            self.assertEqual(len(one), 1)
            self.assertEqual(one[0].offset, off)
            self.assertEqual(one[0].value_string(), f"payload-{off - 100}")


class SegmentRollTest(unittest.TestCase):
    def test_segments_roll_at_size_limit(self):
        p = partition(1, LogConfig(120, 40, 10_000))
        for i in range(10):
            p.append(b"k", b"0123456789", i)  # 39-byte frames: three per 120-byte segment
        self.assertEqual(p.segment_count(), 4)
        self.assertEqual(p.segment_base_offsets(), [0, 3, 6, 9])

    def test_read_crosses_segment_boundaries(self):
        p = partition(1, LogConfig(120, 40, 10_000))
        for i in range(10):
            p.append(b"k", b"0123456789", i)
        self.assertEqual([r.offset for r in p.read(2, 4)], [2, 3, 4, 5])
        self.assertEqual(len(p.read(9, 10)), 1)

    def test_oversized_record_lands_in_empty_segment(self):
        p = partition(2, LogConfig(50, 10, 10_000))
        off = p.append(b"k", b"this value is much longer than the fifty byte segment limit", 1)
        self.assertEqual(off, 0)
        self.assertEqual(len(p.read(0, 1)), 1)


class StorageAndRetentionTest(unittest.TestCase):
    def setUp(self):
        self.storage = InMemoryStorage()
        self.p = Partition(7, LogConfig(120, 40, 250), self.storage)
        for i in range(10):
            self.p.append(b"k", b"0123456789", i)

    def test_storage_holds_one_entry_per_segment_keyed_by_partition(self):
        self.assertEqual(self.storage.base_offsets(7), [0, 3, 6, 9])
        self.assertEqual(self.storage.base_offsets(8), [])

    def test_retention_drops_oldest_segments_through_storage(self):
        before = self.p.size_bytes()
        dropped = self.p.enforce_retention()
        self.assertEqual(dropped, 2)
        self.assertEqual(self.p.segment_count(), 2)
        self.assertLess(self.p.size_bytes(), before)
        self.assertLessEqual(self.p.size_bytes(), 250)
        self.assertEqual((self.p.log_start_offset(), self.p.log_end_offset()), (6, 10))
        self.assertEqual(self.storage.base_offsets(7), [6, 9])
        with self.assertRaises(OffsetOutOfRangeError):
            self.p.read(2, 1)
        self.assertEqual(self.p.read(6, 1)[0].offset, 6)

    def test_active_segment_is_never_dropped(self):
        p = partition(4, LogConfig(50, 10, 10))
        p.append(b"k", b"0123456789", 1)
        p.append(b"k", b"0123456789", 2)
        p.enforce_retention()
        self.assertEqual(p.segment_count(), 1)
        self.assertEqual(p.log_end_offset(), 2)

    def test_committed_offset_retired_by_retention_resets_to_log_start(self):
        self.p.enforce_retention()
        offsets = OffsetStore()
        offsets.commit("g4", 7, 2)
        self.assertEqual(Consumer(self.p, "g4", offsets).position(), 6)


class PartitioningTest(unittest.TestCase):
    def test_same_key_same_partition_in_order(self):
        topic = Topic("t", 4, BIG, InMemoryStorage())
        keys = ["alice", "bob", "carol", "dave", "erin"]
        homes: dict[str, set[int]] = {}
        for i in range(100):
            k = keys[i % len(keys)]
            meta = topic.append(k.encode(), f"v{i}".encode(), i)
            self.assertEqual(meta.partition, topic.partition_for(k.encode()))
            homes.setdefault(k, set()).add(meta.partition)
        self.assertTrue(all(len(s) == 1 for s in homes.values()))
        home = topic.partition_for(b"alice")
        alice = [int(r.value_string()[1:]) for r in topic.partition(home).read(0, 1000) if r.key == b"alice"]
        self.assertEqual(alice, sorted(alice))
        self.assertEqual(alice[-1], 95)

    def test_partition_for_is_pure_and_in_range(self):
        topic = Topic("t", 4, BIG, InMemoryStorage())
        self.assertEqual(topic.partition_for(b"bob"), topic.partition_for(b"bob"))
        self.assertIn(topic.partition_for(b"anything"), range(4))

    def test_null_keys_round_robin(self):
        topic = Topic("u", 3, BIG, InMemoryStorage())
        self.assertEqual([topic.partition_for(None) for _ in range(4)], [0, 1, 2, 0])


class ConsumerTest(unittest.TestCase):
    def test_new_group_starts_at_log_start_and_poll_advances(self):
        c = Consumer(filled_partition(), "g1", OffsetStore())
        self.assertEqual(c.position(), 0)
        self.assertEqual(len(c.poll(10)), 10)
        self.assertEqual(c.position(), 10)

    def test_seek_then_poll_to_end(self):
        c = Consumer(filled_partition(), "g1", OffsetStore())
        c.seek(45)
        self.assertEqual(len(c.poll(10)), 5)
        self.assertEqual(c.position(), 50)
        self.assertEqual(c.poll(10), [])
        self.assertEqual(c.position(), 50)

    def test_seek_outside_log_rejected(self):
        c = Consumer(filled_partition(), "g1", OffsetStore())
        with self.assertRaises(OffsetOutOfRangeError):
            c.seek(99)

    def test_commit_then_resume_redelivers_uncommitted(self):
        p = filled_partition()
        offsets = OffsetStore()
        worker = Consumer(p, "g2", offsets)
        worker.poll(10)
        worker.commit()
        self.assertEqual(offsets.committed("g2", 0), 10)
        worker.poll(5)
        self.assertEqual(worker.position(), 15)
        self.assertEqual(offsets.committed("g2", 0), 10)
        resumed = Consumer(p, "g2", offsets)
        self.assertEqual(resumed.position(), 10)
        self.assertEqual(resumed.poll(5)[0].offset, 10)
        self.assertEqual(Consumer(p, "g3", offsets).position(), 0)
        self.assertEqual(offsets.committed("nobody", 0), OffsetStore.NONE)

    def test_config_validation(self):
        with self.assertRaises(ValueError):
            LogConfig(0, 1, 1)
        with self.assertRaises(ValueError):
            OffsetStore().commit("g", 0, -5)


if __name__ == "__main__":
    unittest.main()
