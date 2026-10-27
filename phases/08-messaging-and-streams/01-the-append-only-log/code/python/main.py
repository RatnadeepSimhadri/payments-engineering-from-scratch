"""Lesson: phases/08-messaging-and-streams/01-the-append-only-log/docs/en.md
The Append-Only Log: Partitions, Offsets, and Segments
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: Apache Kafka documentation, section 5.4 "Log" (segment files, sparse offset index, retention);
         Kreps, Narkhede, Rao, "Kafka: a Distributed Messaging System for Log Processing" (NetDB 2011).
Python 3.11+, standard library only. Same design as the Java version: segments live behind an injected
SegmentStorage; the in-memory one holds bytearrays with the exact frame layout a file would hold.
"""

from __future__ import annotations

import struct
from dataclasses import dataclass
from typing import Dict, List, Optional


class Clock:
    """Injected time source so demos and tests are deterministic."""

    def now(self) -> int:
        raise NotImplementedError


class TickClock(Clock):
    """A clock that advances one millisecond per call, starting at a fixed epoch."""

    def __init__(self, start_millis: int) -> None:
        self._next = start_millis

    def now(self) -> int:
        value = self._next
        self._next += 1
        return value


@dataclass(frozen=True)
class LogConfig:
    """Sizing rules for every partition of a topic."""

    segment_bytes: int
    index_interval_bytes: int
    retention_bytes: int

    def __post_init__(self) -> None:
        if self.segment_bytes <= 0:
            raise ValueError("segmentBytes must be positive")
        if self.index_interval_bytes <= 0:
            raise ValueError("indexIntervalBytes must be positive")
        if self.retention_bytes <= 0:
            raise ValueError("retentionBytes must be positive")


@dataclass(frozen=True)
class StoredRecord:
    """What a consumer receives: the record plus the coordinates the log assigned to it."""

    offset: int
    timestamp: int
    key: Optional[bytes]
    value: bytes

    def key_string(self) -> str:
        return "null" if self.key is None else self.key.decode("utf-8")

    def value_string(self) -> str:
        return self.value.decode("utf-8")


@dataclass(frozen=True)
class RecordMetadata:
    """Where a produced record landed."""

    partition: int
    offset: int


@dataclass(frozen=True)
class IndexEntry:
    """One entry of the sparse offset index: "offset baseOffset+relativeOffset starts at byte position"."""

    relative_offset: int
    position: int


class OffsetOutOfRangeError(Exception):
    def __init__(self, offset: int, start: int, end: int) -> None:
        super().__init__(f"offset {offset} is outside [{start}, {end}]")


class SegmentFullError(Exception):
    def __init__(self, base_offset: int) -> None:
        super().__init__(f"segment {base_offset} is full")


class SegmentData:
    """The bytes of one segment, as a file would present them: append at the end, read any range."""

    def append(self, data: bytes) -> None:
        raise NotImplementedError

    def size(self) -> int:
        raise NotImplementedError

    def read(self, position: int, length: int) -> bytes:
        raise NotImplementedError


class SegmentStorage:
    """Creates, lists, and deletes segment data by base offset. Retention goes through here so a file-backed store can unlink."""

    def create(self, partition: int, base_offset: int) -> SegmentData:
        raise NotImplementedError

    def delete(self, partition: int, base_offset: int) -> None:
        raise NotImplementedError

    def base_offsets(self, partition: int) -> List[int]:
        raise NotImplementedError


class InMemorySegmentData(SegmentData):
    def __init__(self) -> None:
        self._data = bytearray()

    def append(self, data: bytes) -> None:
        self._data.extend(data)

    def size(self) -> int:
        return len(self._data)

    def read(self, position: int, length: int) -> bytes:
        if position < 0 or length < 0 or position + length > len(self._data):
            raise IndexError(f"read [{position}, {position + length}) of {len(self._data)}")
        return bytes(self._data[position:position + length])


class InMemoryStorage(SegmentStorage):
    """The in-memory storage used by the demo and the tests. One growable bytearray per segment."""

    def __init__(self) -> None:
        # partition -> baseOffset -> bytes; the same shape as Kafka's "<topic>-<partition>/<baseOffset>.log" directories.
        self._partitions: Dict[int, Dict[int, InMemorySegmentData]] = {}

    def _dir(self, partition: int) -> Dict[int, InMemorySegmentData]:
        return self._partitions.setdefault(partition, {})

    def create(self, partition: int, base_offset: int) -> SegmentData:
        if base_offset in self._dir(partition):
            raise RuntimeError(f"segment {partition}/{base_offset} already exists")
        data = InMemorySegmentData()
        self._dir(partition)[base_offset] = data
        return data

    def delete(self, partition: int, base_offset: int) -> None:
        if self._dir(partition).pop(base_offset, None) is None:
            raise RuntimeError(f"no segment {partition}/{base_offset}")

    def base_offsets(self, partition: int) -> List[int]:
        return sorted(self._dir(partition).keys())


_LENGTH_PREFIX = struct.Struct(">i")
_HEAD = struct.Struct(">qqi")  # offset, timestamp, keyLength


class RecordCodec:
    """The byte layout of one record ("frame"), big-endian, exactly as it would sit in a segment file:

        int32 frameLength   bytes after this field
        int64 offset
        int64 timestamp
        int32 keyLength     -1 when the key is null
        bytes key
        int32 valueLength
        bytes value

    The length prefix is what lets a reader skip a record it does not care about without parsing it.
    """

    LENGTH_PREFIX = 4
    HEADER = 8 + 8 + 4 + 4  # offset, timestamp, keyLength, valueLength

    @staticmethod
    def frame_size(key: Optional[bytes], value: bytes) -> int:
        return RecordCodec.LENGTH_PREFIX + RecordCodec.HEADER + (0 if key is None else len(key)) + len(value)

    @staticmethod
    def encode(offset: int, timestamp: int, key: Optional[bytes], value: bytes) -> bytes:
        if value is None:
            raise TypeError("value")
        size = RecordCodec.frame_size(key, value)
        out = bytearray()
        out += _LENGTH_PREFIX.pack(size - RecordCodec.LENGTH_PREFIX)
        if key is None:
            out += _HEAD.pack(offset, timestamp, -1)
        else:
            out += _HEAD.pack(offset, timestamp, len(key))
            out += key
        out += _LENGTH_PREFIX.pack(len(value))
        out += value
        return bytes(out)

    @staticmethod
    def decode(data: bytes, position: int) -> StoredRecord:
        """Decode the frame starting at position; the caller advances by frame_length_at."""
        (frame_length,) = _LENGTH_PREFIX.unpack_from(data, position)
        if frame_length < RecordCodec.HEADER:
            raise RuntimeError(f"corrupt frame at {position}")
        pos = position + RecordCodec.LENGTH_PREFIX
        offset, timestamp, key_length = _HEAD.unpack_from(data, pos)
        pos += _HEAD.size
        key: Optional[bytes] = None
        if key_length >= 0:
            key = bytes(data[pos:pos + key_length])
            pos += key_length
        (value_length,) = _LENGTH_PREFIX.unpack_from(data, pos)
        pos += RecordCodec.LENGTH_PREFIX
        value = bytes(data[pos:pos + value_length])
        return StoredRecord(offset, timestamp, key, value)

    @staticmethod
    def frame_length_at(data: bytes, position: int) -> int:
        """Total bytes occupied by the frame at position, including its own length prefix."""
        return RecordCodec.LENGTH_PREFIX + _LENGTH_PREFIX.unpack_from(data, position)[0]

    @staticmethod
    def fnv1a32(data: bytes) -> int:
        """FNV-1a 32-bit. Chosen over Kafka's murmur2 because it is five lines and identical in all three languages.
        Python ints do not wrap, so the multiply is masked to 32 bits; the result is unsigned (0..2**32-1)."""
        h = 0x811C9DC5
        for b in data:
            h ^= b
            h = (h * 0x01000193) & 0xFFFFFFFF
        return h


class Segment:
    """A contiguous run of records with offsets [baseOffset, nextOffset). Bytes are appended and never modified.
    The sparse index records one (relativeOffset, position) pair roughly every indexIntervalBytes."""

    def __init__(self, base_offset: int, data: SegmentData, max_bytes: int, index_interval_bytes: int) -> None:
        self.base_offset = base_offset
        self._data = data
        self._next_offset = base_offset
        self._max_bytes = max_bytes
        self._index_interval_bytes = index_interval_bytes
        self._index: List[IndexEntry] = []
        self._bytes_since_index_entry = 2**31 - 1  # force an entry for the first record

    def size_bytes(self) -> int:
        return self._data.size()

    def next_offset(self) -> int:
        return self._next_offset

    def record_count(self) -> int:
        return self._next_offset - self.base_offset

    def is_empty(self) -> bool:
        return self._data.size() == 0

    def index_entries(self) -> List[IndexEntry]:
        return list(self._index)

    def has_room_for(self, frame_bytes: int) -> bool:
        return self._data.size() + frame_bytes <= self._max_bytes

    def append(self, key: Optional[bytes], value: bytes, timestamp: int) -> int:
        """Append one record and return its offset. An empty segment accepts any size so oversized records still land."""
        frame = RecordCodec.frame_size(key, value)
        if not self.is_empty() and not self.has_room_for(frame):
            raise SegmentFullError(self.base_offset)
        offset = self._next_offset
        encoded = RecordCodec.encode(offset, timestamp, key, value)
        if self._bytes_since_index_entry >= self._index_interval_bytes:
            self._index.append(IndexEntry(offset - self.base_offset, self._data.size()))
            self._bytes_since_index_entry = 0
        self._data.append(encoded)
        self._bytes_since_index_entry += len(encoded)
        self._next_offset += 1
        return offset

    def _frame_length_at(self, position: int) -> int:
        """Bytes occupied by the frame at position, learned by reading only its 4-byte length prefix."""
        return RecordCodec.frame_length_at(self._data.read(position, RecordCodec.LENGTH_PREFIX), 0)

    def position_of(self, offset: int) -> int:
        """Byte position of the record at offset: binary-search the sparse index for the last entry at or
        before the target, then scan forward frame by frame. The scan is bounded by indexIntervalBytes."""
        if offset < self.base_offset or offset >= self._next_offset:
            raise OffsetOutOfRangeError(offset, self.base_offset, self._next_offset - 1)
        relative = offset - self.base_offset
        lo, hi, best = 0, len(self._index) - 1, 0
        while lo <= hi:
            mid = (lo + hi) // 2
            if self._index[mid].relative_offset <= relative:
                best = mid
                lo = mid + 1
            else:
                hi = mid - 1
        entry = self._index[best]
        position = entry.position
        current = self.base_offset + entry.relative_offset
        while current < offset:
            position += self._frame_length_at(position)
            current += 1
        return position

    def read(self, offset: int, max_records: int) -> List[StoredRecord]:
        """Read up to max_records starting at offset, stopping at the end of this segment."""
        out: List[StoredRecord] = []
        if offset >= self._next_offset or max_records <= 0:
            return out
        position = self.position_of(offset)
        while position < self._data.size() and len(out) < max_records:
            length = self._frame_length_at(position)
            out.append(RecordCodec.decode(self._data.read(position, length), 0))
            position += length
        return out


class Partition:
    """An ordered list of segments. The last segment is active; older ones are immutable and eligible for retention."""

    def __init__(self, id: int, config: LogConfig, storage: SegmentStorage) -> None:
        self.id = id
        self._config = config
        self._storage = storage
        self._segments: List[Segment] = []
        self._segments.append(self._new_segment(0))

    def _new_segment(self, base_offset: int) -> Segment:
        return Segment(base_offset, self._storage.create(self.id, base_offset),
                       self._config.segment_bytes, self._config.index_interval_bytes)

    def _active(self) -> Segment:
        return self._segments[-1]

    def log_start_offset(self) -> int:
        return self._segments[0].base_offset

    def log_end_offset(self) -> int:
        """The offset the next record will receive; also "high watermark" in a single-replica log."""
        return self._active().next_offset()

    def segment_count(self) -> int:
        return len(self._segments)

    def segment_base_offsets(self) -> List[int]:
        return [s.base_offset for s in self._segments]

    def size_bytes(self) -> int:
        return sum(s.size_bytes() for s in self._segments)

    def append(self, key: Optional[bytes], value: bytes, timestamp: int) -> int:
        """Append and return the assigned offset. Rolls to a new segment when the active one cannot take the frame."""
        active = self._active()
        frame = RecordCodec.frame_size(key, value)
        if not active.is_empty() and not active.has_room_for(frame):
            active = self._new_segment(active.next_offset())
            self._segments.append(active)
        return active.append(key, value, timestamp)

    def _segment_index_for(self, offset: int) -> int:
        """Index of the segment whose offset range contains offset: the last segment with baseOffset <= offset."""
        lo, hi, best = 0, len(self._segments) - 1, 0
        while lo <= hi:
            mid = (lo + hi) // 2
            if self._segments[mid].base_offset <= offset:
                best = mid
                lo = mid + 1
            else:
                hi = mid - 1
        return best

    def read(self, from_offset: int, max_records: int) -> List[StoredRecord]:
        """Read up to max_records from from_offset, crossing segment boundaries. Reading at log_end_offset returns
        an empty list (nothing yet); reading below log_start_offset or above log_end_offset is an error, as in Kafka."""
        start, end = self.log_start_offset(), self.log_end_offset()
        if from_offset < start or from_offset > end:
            raise OffsetOutOfRangeError(from_offset, start, end)
        out: List[StoredRecord] = []
        if from_offset == end:
            return out
        nxt = from_offset
        i = self._segment_index_for(from_offset)
        while i < len(self._segments) and len(out) < max_records:
            chunk = self._segments[i].read(nxt, max_records - len(out))
            out.extend(chunk)
            if chunk:
                nxt = chunk[-1].offset + 1
            i += 1
        return out

    def enforce_retention(self) -> int:
        """Drop whole segments, oldest first, until the partition fits in retention_bytes. The active segment is never dropped."""
        removed = 0
        while len(self._segments) > 1 and self.size_bytes() > self._config.retention_bytes:
            oldest = self._segments.pop(0)
            self._storage.delete(self.id, oldest.base_offset)
            removed += 1
        return removed


class Topic:
    """A named set of partitions. Records with the same non-null key always land in the same partition."""

    def __init__(self, name: str, partition_count: int, config: LogConfig, storage: SegmentStorage) -> None:
        if partition_count <= 0:
            raise ValueError("need at least one partition")
        self.name = name
        self._partitions: List[Partition] = [Partition(i, config, storage) for i in range(partition_count)]
        self._round_robin = 0

    def partition_count(self) -> int:
        return len(self._partitions)

    def partition(self, id: int) -> Partition:
        return self._partitions[id]

    def partition_for(self, key: Optional[bytes]) -> int:
        """Keyed records hash; unkeyed records rotate. The hash is masked to non-negative before the modulo."""
        if key is None:
            p = self._round_robin % len(self._partitions)
            self._round_robin += 1
            return p
        return (RecordCodec.fnv1a32(key) & 0x7FFFFFFF) % len(self._partitions)

    def append(self, key: Optional[bytes], value: bytes, timestamp: int) -> RecordMetadata:
        p = self.partition_for(key)
        return RecordMetadata(p, self._partitions[p].append(key, value, timestamp))


class OffsetStore:
    """Committed consumer positions, keyed by (group, partition). In Kafka this is the __consumer_offsets topic; here it is
    a dict, but the contract is the same: the committed value is the NEXT offset to read, not the last one processed."""

    NONE = -1

    def __init__(self) -> None:
        self._committed: Dict[str, int] = {}

    @staticmethod
    def _key(group: str, partition: int) -> str:
        return f"{group}/{partition}"

    def commit(self, group: str, partition: int, next_offset: int) -> None:
        if next_offset < 0:
            raise ValueError("offset must be non-negative")
        self._committed[self._key(group, partition)] = next_offset

    def committed(self, group: str, partition: int) -> int:
        return self._committed.get(self._key(group, partition), OffsetStore.NONE)


class Consumer:
    """A single-partition reader that belongs to a consumer group. Position is the next offset to read. A new Consumer
    resumes from the group's committed offset; if there is none, or it has been retired by retention, it starts at
    log_start_offset (Kafka's auto.offset.reset=earliest)."""

    def __init__(self, partition: Partition, group: str, offsets: OffsetStore) -> None:
        self._partition = partition
        self._group = group
        self._offsets = offsets
        committed = offsets.committed(group, partition.id)
        if committed == OffsetStore.NONE or committed < partition.log_start_offset() or committed > partition.log_end_offset():
            self._position = partition.log_start_offset()
        else:
            self._position = committed

    def seek(self, offset: int) -> None:
        if offset < self._partition.log_start_offset() or offset > self._partition.log_end_offset():
            raise OffsetOutOfRangeError(offset, self._partition.log_start_offset(), self._partition.log_end_offset())
        self._position = offset

    def position(self) -> int:
        return self._position

    def poll(self, max_records: int) -> List[StoredRecord]:
        batch = self._partition.read(self._position, max_records)
        if batch:
            self._position = batch[-1].offset + 1
        return batch

    def commit(self) -> None:
        """Record that everything below position has been processed. Anything polled but not committed is redelivered after a restart."""
        self._offsets.commit(self._group, self._partition.id, self._position)


def utf8(s: Optional[str]) -> Optional[bytes]:
    return None if s is None else s.encode("utf-8")


def main() -> None:
    clock = TickClock(1_700_000_000_000)
    config = LogConfig(160, 64, 400)
    storage = InMemoryStorage()
    topic = Topic("payments.authorized", 3, config, storage)

    print("== Producing 12 keyed records into 3 partitions ==")
    merchants = ["m-coffee", "m-books", "m-grocer", "m-fuel"]
    for i in range(12):
        key = merchants[i % len(merchants)]
        value = f"auth-{1000 + i} 19.99 USD"
        meta = topic.append(utf8(key), utf8(value), clock.now())
        print(f"{key:<9} -> partition {meta.partition} offset {meta.offset}")

    print("\n== Partition layout after segment rolls (segmentBytes=160) ==")
    for p in range(topic.partition_count()):
        part = topic.partition(p)
        print(f"partition {p}: logStart={part.log_start_offset()} logEnd={part.log_end_offset()} "
              f"segments(base offsets)={part.segment_base_offsets()} bytes={part.size_bytes()}")

    p0 = topic.partition(0)
    print("\n== Reading partition 0 from offset 1, three at a time ==")
    offsets = OffsetStore()
    consumer = Consumer(p0, "ledger-poster", offsets)
    consumer.seek(1)
    while True:
        batch = consumer.poll(3)
        if not batch:
            break
        for r in batch:
            print(f"offset {r.offset} ts {r.timestamp} key {r.key_string()} value {r.value_string()}")
        print(f"  position is now {consumer.position()}")

    print("\n== Commit, crash, resume (partition 1, six records) ==")
    p1 = topic.partition(1)
    worker = Consumer(p1, "ledger-poster", offsets)
    print(f"fresh consumer with no commit starts at logStart: position {worker.position()}")
    worker.poll(2)
    worker.commit()
    print(f"processed 2 and committed: committed offset = {offsets.committed('ledger-poster', 1)}")
    worker.poll(2)
    print(f"processed 2 more without committing: position {worker.position()} (then the process dies)")
    restarted = Consumer(p1, "ledger-poster", offsets)
    print(f"restart resumes at the committed offset: position {restarted.position()}"
          " -> offsets 2 and 3 are delivered again (at-least-once)")

    print("\n== Retention: keep at most 400 bytes per partition, whole segments only ==")
    archiver = Consumer(p0, "archiver", offsets)
    archiver.poll(2)
    archiver.commit()
    for i in range(20):
        p0.append(utf8("m-coffee"), utf8(f"auth-{2000 + i} 5.00 USD"), clock.now())
    print(f"before: segments={p0.segment_base_offsets()} bytes={p0.size_bytes()}")
    dropped = p0.enforce_retention()
    print(f"after : dropped {dropped} segment(s), segments={p0.segment_base_offsets()} "
          f"bytes={p0.size_bytes()} logStart={p0.log_start_offset()}")
    try:
        p0.read(0, 1)
    except OffsetOutOfRangeError as e:
        print(f"read(0) rejected: {e}")
    first = p0.read(p0.log_start_offset(), 1)[0]
    print(f"earliest readable record: offset {first.offset} {first.value_string()}")
    lagging = Consumer(p0, "archiver", offsets)
    print(f"archiver committed {offsets.committed('archiver', 0)}, which retention retired: "
          f"it resets to logStart, position {lagging.position()}")

    print("\n== The frame on the wire ==")
    frame = RecordCodec.encode(7, 1_700_000_000_000, utf8("m-fuel"), utf8("ok"))
    print(f"{len(frame)} bytes: {frame.hex()}")


if __name__ == "__main__":
    main()
