# The Append-Only Log: Partitions, Offsets, and Segments

> A log is not a queue: nothing is removed when it is read, the position belongs to the reader, and the only write is an append.

**Type:** Build
**Languages:** Java, Python, Go
**Prerequisites:** Phase 1 Lesson 07 (Hash Functions and Collisions), Phase 7 Lesson 02 (Write-Ahead Logs)
**Time:** ~120 minutes

## Learning Objectives

- Explain why a partitioned, append-only log replaces a message queue when more than one consumer needs the same records and replay must be possible
- Build a topic with N partitions, a hash-mod-N partitioner, and per-partition offsets that are dense and monotonic
- Implement segments that roll at a byte limit, a sparse offset index, and a read path that resolves any offset in O(log segments + log index + bounded scan)
- Implement retention that drops whole segments from the oldest end and moves the log start offset, and show what that does to a lagging consumer
- State the consumer position contract (committed offset is the next offset to read) and reproduce at-least-once redelivery after a crash

## The Problem

A payment authorization service publishes one event per approved authorization to a message broker. Three downstream services subscribe: the ledger poster, the fraud scorer, and a nightly archiver that writes to cold storage. The broker is a traditional queue: a message is delivered to one consumer and then deleted. To fan out to three services, the platform team runs three queues and the producer publishes every event three times. On a Tuesday afternoon the publish to the ledger queue times out for forty seconds while the other two succeed. Four thousand authorizations are now in the fraud and archive queues and not in the ledger. Nobody notices until the next morning, when reconciliation finds four thousand captures that never had an authorization posted. There is no way to replay: the queue deleted the messages the fraud service consumed, and the producer's retry logic gave up after three attempts.

The second failure is ordering. The fraud scorer runs eight workers pulling from one queue. Merchant `m-coffee` sends an authorization and, 200 ms later, a reversal. Worker 3 pulls the authorization and stalls on a garbage-collection pause. Worker 5 pulls the reversal and processes it first. The scorer now sees a reversal for an authorization it has never heard of and raises an alert. The queue made no ordering promise across workers, and it could not: a shared queue with competing consumers destroys order the moment the second consumer connects.

Both failures come from the same design choice: the broker owned the position. It decided what "consumed" meant, deleted accordingly, and could not tell three readers apart. The fix is to turn the broker into an append-only log. Records are appended, assigned a dense offset, and kept until a retention rule says otherwise. Every reader owns its own offset and can rewind. Records with the same key go to the same partition, so one merchant's events are always in production order. This lesson builds that log: topic, partitions, segments, a sparse index, retention, and the consumer position contract, in the shape Apache Kafka uses, with in-memory byte segments that have the exact layout a file would.

## The Concept

### From queue to log

| Property | Queue (AMQP-style) | Log (Kafka-style) |
|----------|--------------------|-------------------|
| Read removes the message | Yes (ack deletes) | No; retention deletes |
| Multiple independent readers | Need multiple queues or fan-out exchange | Any number, each with its own offset |
| Replay from the past | No | Yes, to the log start offset |
| Ordering | Per queue with one consumer; lost with competing consumers | Per partition, always |
| Position owned by | Broker | Reader (committed to the broker) |
| Parallelism | Competing consumers on one queue | One consumer per partition |

The one-sentence mental model: **a log is an array of records that only grows at the end, and a partition is one such array; the offset is the array index.** Everything else in this lesson is about making that array survive on disk (segments), making index lookup cheap without storing an entry per record (sparse index), bounding its size (retention), and letting readers remember where they are (committed offsets).

### Topic, partition, offset

A topic is a name and a list of partitions. A partition is an ordered, immutable sequence of records with offsets `0, 1, 2, ...` with no gaps. Two invariants hold per partition and never across partitions:

1. **Offsets are dense and monotonic.** The record appended after offset `k` receives `k + 1`. There is no offset that was skipped and no offset that was reused.
2. **Order is production order.** If record A was appended before record B in partition 3, every reader of partition 3 sees A before B.

Across partitions there is no order at all. Offset 5 in partition 0 and offset 5 in partition 1 have no relationship. That is the price of parallelism, and it is why the partitioner matters.

### The partitioner

A record with a non-null key goes to partition `hash(key) mod N`. The hash must be deterministic across producers, languages, and restarts; this lesson uses FNV-1a (32-bit) because it is five lines and bit-identical in Java, Python, and Go. Kafka's default Java producer uses murmur2 with the sign bit masked off; the shape is the same.

Because the partition is a pure function of the key, all records for `m-coffee` land in the same partition and therefore have a total order among themselves. That is the ordering guarantee a payments system actually needs: order per merchant, per card, or per account, with parallelism across them. Records with a null key are not ordered with respect to anything, so they rotate round-robin to spread load.

The partitioner has one consequence people forget: **changing N changes every key's home.** `hash("m-coffee") mod 3` and `hash("m-coffee") mod 4` are different numbers. Adding a partition to a live topic splits each key's history across two partitions, and ordering across that boundary is gone. Phase 3, Lesson 06 (Consistent Hashing) is the structure that avoids this for caches; Kafka simply tells you not to do it to a keyed topic.

### Segments

A partition that holds a year of authorizations is tens of gigabytes. One file of that size cannot be deleted from the front, and an index over it would be huge. So the partition is cut into segments: contiguous runs of records, each with a base offset (the offset of its first record) and a byte limit. The last segment is active and accepts appends; every earlier segment is closed and immutable. When the active segment cannot fit the next record, a new segment is created whose base offset is the current log end offset.

```mermaid
flowchart LR
  subgraph P["partition 1 (segmentBytes = 160)"]
    S0["segment base=0<br/>offsets 0,1<br/>closed"] --> S2["segment base=2<br/>offsets 2,3<br/>closed"] --> S4["segment base=4<br/>offsets 4,5<br/>active"]
  end
  R["read(3)"] -->|binary search base offsets: 2 <= 3 < 4| S2
  Ret["retention: bytes > 400"] -. "drop oldest whole segment" .-> S0
```

Finding the segment for an offset is a binary search over base offsets for the last base `<= offset`. Retention becomes trivial: drop whole segments from the front, oldest first, never the active one. Deleting a segment is one unlink, not a rewrite, and the log start offset simply becomes the base offset of the new oldest segment.

### The frame

Each record is stored as a self-describing frame so a reader can skip it without decoding it:

| Field | Size | Meaning |
|-------|------|---------|
| frameLength | int32 | bytes after this field |
| offset | int64 | assigned by the partition |
| timestamp | int64 | from the injected clock |
| keyLength | int32 | -1 for a null key |
| key | keyLength bytes | |
| valueLength | int32 | |
| value | valueLength bytes | |

A record with key `m-fuel` (6 bytes) and value `ok` (2 bytes) occupies `4 + 24 + 6 + 2 = 36` bytes. The length prefix is the whole reason a sparse index works: from any frame boundary, the reader can hop to the next boundary by reading four bytes.

### The sparse index

An index with one entry per record costs 12 bytes per record and defeats the point of storing records compactly. Instead, each segment keeps an entry `(relativeOffset, bytePosition)` roughly every `indexIntervalBytes` of appended data, always starting with `(0, 0)`. To find offset `k`:

1. Binary-search the index for the last entry with `relativeOffset <= k - baseOffset`.
2. Jump to its byte position.
3. Hop frame by frame, reading only each length prefix, until the target offset is reached.

Step 3 is bounded by `indexIntervalBytes`, so the lookup is `O(log segments) + O(log indexEntries) + O(indexIntervalBytes / minFrameSize)`. Kafka's default `log.index.interval.bytes` is 4096, and the same three-step lookup lives in `OffsetIndex.lookup` followed by a scan in `LogSegment.translateOffset`.

### Worked example by hand

Configuration: `segmentBytes = 120`, `indexIntervalBytes = 40`, key `"k"` (1 byte), value `"0123456789"` (10 bytes). Every frame is `4 + 24 + 1 + 10 = 39` bytes.

```text
append #1  offset 0  segment base 0  size 0 -> 39    index: (0, 0)   bytesSinceIndex 39
append #2  offset 1  size 39 -> 78                    39 < 40, no entry; bytesSinceIndex 78
append #3  offset 2  size 78 -> 117                   78 >= 40 -> index (2, 78); reset
append #4  offset 3  117 + 39 = 156 > 120 -> roll     new segment base 3; index (0, 0)
...
after 10 appends: segments base 0, 3, 6, 9  (3 records each, last has 1)
```

Now `read(fromOffset = 2, maxRecords = 4)`:

```text
segmentIndexFor(2): bases [0,3,6,9], last base <= 2 is 0  -> segment 0
segment 0 positionOf(2): relative 2; index [(0,0),(2,78)] -> entry (2,78); no scan needed
decode frame at 78 -> offset 2; position 117 == size, segment exhausted
continue in segment base 3 from offset 3: positionOf(3) = relative 0 -> position 0
decode 0 -> offset 3, decode 39 -> offset 4, decode 78 -> offset 5; have 4 records, stop
result: offsets 2, 3, 4, 5
```

Now retention with `retentionBytes = 250`. Total bytes are `117 + 117 + 117 + 39 = 390`. Drop base 0: 273, still over. Drop base 3: 156, under. Two segments removed; `logStartOffset` is now 6; `read(2, 1)` is out of range; `read(6, 1)` returns offset 6.

### The consumer position contract

A consumer belongs to a group and holds a position: the next offset it will read. `poll(max)` reads from the position and advances it past the last record returned. `commit()` writes the position to an offset store keyed by `(group, partition)`. The committed value is **the next offset to read, not the last one processed**; Kafka's `__consumer_offsets` topic and `OffsetAndMetadata` follow exactly this convention, and off-by-one bugs here are the most common cause of a duplicated or skipped record in a new consumer.

When a consumer starts, it reads the committed offset for its group. If there is none, or if retention has moved the log start past it, it resets to the log start offset (Kafka's `auto.offset.reset=earliest`). The consequence that matters for payments: everything polled after the last commit is redelivered after a crash. That is at-least-once delivery, and it is why every consumer of a payments log must be idempotent (Phase 8, Lesson 03) or must commit transactionally with its side effect.

### What the log does not do

- It does not deduplicate. Two appends of the same payload are two records with two offsets. Idempotency belongs to the producer (idempotent producer sequence numbers) or the consumer.
- It does not replicate. A single-replica log loses data when its disk dies; Phase 6 covers replication and the high watermark that this lesson's `logEndOffset` stands in for.
- It does not coordinate consumers. Which consumer in a group owns which partition is Lesson 02's rebalancing protocol. Here a `Consumer` is bound to one partition explicitly.
- It does not compact. Kafka's log compaction (keep the latest record per key) is a different retention policy; this lesson implements size-based retention only.

## Build It

The primary walkthrough is in Java. Python and Go implement the same types with the same names; the subsections at the end show only what differs.

### Step 1: Configuration, records, and the storage boundary

```java
record LogConfig(int segmentBytes, int indexIntervalBytes, long retentionBytes) {
    LogConfig {
        if (segmentBytes <= 0) throw new IllegalArgumentException("segmentBytes must be positive");
        if (indexIntervalBytes <= 0) throw new IllegalArgumentException("indexIntervalBytes must be positive");
        if (retentionBytes <= 0) throw new IllegalArgumentException("retentionBytes must be positive");
    }
}

record StoredRecord(long offset, long timestamp, byte[] key, byte[] value) { }
record RecordMetadata(int partition, long offset) {}
record IndexEntry(int relativeOffset, int position) {}

interface SegmentData {
    void append(byte[] bytes);
    int size();
    byte[] read(int position, int length);
}

interface SegmentStorage {
    SegmentData create(int partition, long baseOffset);
    void delete(int partition, long baseOffset);
    List<Long> baseOffsets(int partition);
}
```

`SegmentData` is the byte-level contract a file would satisfy: append at the end, report the size, read a range. `SegmentStorage` creates and deletes segment data by `(partition, baseOffset)`, which is exactly the `<topic>-<partition>/<baseOffset>.log` directory layout on a Kafka broker. The rest of the code never touches a byte array directly, so swapping `InMemoryStorage` for a `FileChannel`-backed implementation changes nothing above this line. `InMemoryStorage` (in `Main.java`) keeps a `TreeMap<Long, InMemorySegmentData>` per partition and grows each byte array by doubling.

**Predict:** `storage.create(1, 0)` is called twice with the same arguments. What should happen, and why would silently returning the existing data be dangerous?

### Step 2: The frame codec

```java
static byte[] encode(long offset, long timestamp, byte[] key, byte[] value) {
    ByteBuffer buf = ByteBuffer.allocate(frameSize(key, value));
    buf.putInt(buf.capacity() - LENGTH_PREFIX);
    buf.putLong(offset);
    buf.putLong(timestamp);
    if (key == null) {
        buf.putInt(-1);
    } else {
        buf.putInt(key.length);
        buf.put(key);
    }
    buf.putInt(value.length);
    buf.put(value);
    return buf.array();
}

static int frameLengthAt(byte[] data, int position) {
    return LENGTH_PREFIX + ByteBuffer.wrap(data, position, 4).getInt();
}
```

`ByteBuffer` defaults to big-endian, which is network byte order and what Kafka's wire protocol uses. `-1` as the key length distinguishes "no key" from "empty key", a distinction the partitioner needs. `frameLengthAt` reads four bytes and returns the total frame size including its own prefix, which is the hop the sparse-index scan performs.

The hash used by the partitioner lives beside the codec:

```java
static int fnv1a32(byte[] bytes) {
    int hash = 0x811C9DC5;
    for (byte b : bytes) {
        hash ^= (b & 0xFF);
        hash *= 0x01000193;
    }
    return hash;
}
```

Java `int` multiplication wraps at 32 bits, which is what FNV-1a specifies. The `& 0xFF` matters: a Java `byte` is signed, and XOR-ing in `-56` instead of `200` gives a different hash from Python and Go.

**Predict:** `fnv1a32(new byte[0])`. What does it return, and is that a bug?

### Step 3: Segment append with a sparse index

```java
long append(byte[] key, byte[] value, long timestamp) {
    int frame = RecordCodec.frameSize(key, value);
    if (!isEmpty() && !hasRoomFor(frame)) throw new SegmentFullException(baseOffset);
    long offset = nextOffset;
    byte[] encoded = RecordCodec.encode(offset, timestamp, key, value);
    if (bytesSinceIndexEntry >= indexIntervalBytes) {
        index.add(new IndexEntry((int) (offset - baseOffset), data.size()));
        bytesSinceIndexEntry = 0;
    }
    data.append(encoded);
    bytesSinceIndexEntry += encoded.length;
    nextOffset++;
    return offset;
}
```

Three details carry the invariants. `nextOffset` starts at `baseOffset` and increments by exactly one per append, so offsets are dense. `bytesSinceIndexEntry` is initialised to `Integer.MAX_VALUE` so the very first record always gets an index entry at `(0, 0)`; without that, `positionOf` would have nothing to binary-search. And an empty segment accepts a frame larger than `maxBytes`: a 2 MB record on a 1 MB segment limit still lands, alone in its own segment, rather than being rejected forever.

### Step 4: Resolving an offset to a byte position

```java
int positionOf(long offset) {
    if (offset < baseOffset || offset >= nextOffset) throw new OffsetOutOfRangeException(offset, baseOffset, nextOffset - 1);
    int relative = (int) (offset - baseOffset);
    int lo = 0, hi = index.size() - 1, best = 0;
    while (lo <= hi) {
        int mid = (lo + hi) >>> 1;
        if (index.get(mid).relativeOffset() <= relative) { best = mid; lo = mid + 1; } else { hi = mid - 1; }
    }
    IndexEntry entry = index.get(best);
    int position = entry.position();
    long current = baseOffset + entry.relativeOffset();
    while (current < offset) {
        position += frameLengthAt(position);
        current++;
    }
    return position;
}
```

The binary search is the "last entry `<=` target" variant, not an exact match, because most offsets have no index entry. The forward scan reads one length prefix per hop and is bounded by the number of frames that fit in `indexIntervalBytes`. `(lo + hi) >>> 1` avoids the overflow that `(lo + hi) / 2` has at two billion entries; it is a habit worth keeping even when the index is small.

**Predict:** with `indexIntervalBytes = 100` and 39-byte frames, what is the maximum number of hops the scan performs?

### Step 5: Partition append, roll, and cross-segment read

```java
long append(byte[] key, byte[] value, long timestamp) {
    Segment active = active();
    int frame = RecordCodec.frameSize(key, value);
    if (!active.isEmpty() && !active.hasRoomFor(frame)) {
        active = newSegment(active.nextOffset());
        segments.add(active);
    }
    return active.append(key, value, timestamp);
}

List<StoredRecord> read(long fromOffset, int maxRecords) {
    long start = logStartOffset(), end = logEndOffset();
    if (fromOffset < start || fromOffset > end) throw new OffsetOutOfRangeException(fromOffset, start, end);
    List<StoredRecord> out = new ArrayList<>();
    if (fromOffset == end) return out;
    long next = fromOffset;
    for (int i = segmentIndexFor(fromOffset); i < segments.size() && out.size() < maxRecords; i++) {
        List<StoredRecord> chunk = segments.get(i).read(next, maxRecords - out.size());
        out.addAll(chunk);
        if (!chunk.isEmpty()) next = chunk.get(chunk.size() - 1).offset() + 1;
    }
    return out;
}
```

The roll is decided by the partition, not the segment, because the partition owns the list; the new segment's base offset is the old one's `nextOffset`, which keeps offsets dense across the boundary. The read distinguishes three cases the way Kafka does: `fromOffset == logEndOffset` returns an empty list (nothing yet, poll again); below `logStartOffset` or above `logEndOffset` is an `OffsetOutOfRangeException`; anything else walks segments from `segmentIndexFor(fromOffset)` (a binary search over base offsets) until it has `maxRecords` or runs out.

### Step 6: Retention and the partitioner

```java
int enforceRetention() {
    int removed = 0;
    while (segments.size() > 1 && sizeBytes() > config.retentionBytes()) {
        Segment oldest = segments.remove(0);
        storage.delete(id, oldest.baseOffset);
        removed++;
    }
    return removed;
}

int partitionFor(byte[] key) {
    if (key == null) return (int) (roundRobin++ % partitions.size());
    return (RecordCodec.fnv1a32(key) & 0x7FFFFFFF) % partitions.size();
}
```

Retention removes whole segments from index 0 and tells storage to delete them; the active segment is never removed even if it alone exceeds the limit, because deleting the only segment would lose the offset counter. `logStartOffset()` is simply the base of whatever segment is now first, so it advances for free. The partitioner masks the sign bit before the modulo because Java's `%` on a negative `int` yields a negative result, and a negative partition index is an `ArrayIndexOutOfBoundsException` in production at 3 a.m.

**Predict:** a topic has 3 partitions and you add a fourth. Which keys keep their partition?

### Step 7: Consumer position and commit

```java
Consumer(Partition partition, String group, OffsetStore offsets) {
    this.partition = partition;
    this.group = group;
    this.offsets = offsets;
    long committed = offsets.committed(group, partition.id);
    if (committed == OffsetStore.NONE || committed < partition.logStartOffset() || committed > partition.logEndOffset()) {
        position = partition.logStartOffset();
    } else {
        position = committed;
    }
}

List<StoredRecord> poll(int maxRecords) {
    List<StoredRecord> batch = partition.read(position, maxRecords);
    if (!batch.isEmpty()) position = batch.get(batch.size() - 1).offset() + 1;
    return batch;
}

void commit() { offsets.commit(group, partition.id, position); }
```

`position` is always "next to read". `poll` advances it to last-delivered plus one; `commit` stores exactly that number. The constructor handles both reset cases: no commit yet, and a commit that retention has retired (committed 2, log now starts at 17). The demo's "commit, crash, resume" section shows offsets 2 and 3 being delivered twice; the test asserts it.

### Step 8: Run it

```bash
cd phases/08-messaging-and-streams/01-the-append-only-log/code/java
javac -d .out *.java && java -cp .out Main
java -cp .out MainTest
```

Expected output, abbreviated:

```console
== Producing 12 keyed records into 3 partitions ==
m-coffee  -> partition 1 offset 0
m-books   -> partition 2 offset 0
m-grocer  -> partition 1 offset 1
m-fuel    -> partition 0 offset 0
...
== Partition layout after segment rolls (segmentBytes=160) ==
partition 0: logStart=0 logEnd=3 segments(base offsets)=[0] bytes=159
partition 1: logStart=0 logEnd=6 segments(base offsets)=[0, 2, 4] bytes=330
partition 2: logStart=0 logEnd=3 segments(base offsets)=[0, 2] bytes=162

== Commit, crash, resume (partition 1, six records) ==
processed 2 and committed: committed offset = 2
processed 2 more without committing: position 4 (then the process dies)
restart resumes at the committed offset: position 2 -> offsets 2 and 3 are delivered again (at-least-once)

== Retention: keep at most 400 bytes per partition, whole segments only ==
before: segments=[0, 3, 5, 7, 9, 11, 13, 15, 17, 19, 21] bytes=1239
after : dropped 8 segment(s), segments=[17, 19, 21] bytes=324 logStart=17
read(0) rejected: offset 0 is outside [17, 23]

== The frame on the wire ==
36 bytes: 0000002000000000000000070000018bcfe56800000000066d2d6675656c000000026f6b
```

Read the hex: `00000020` is 32, the bytes after the prefix; `0000000000000007` is offset 7; `00000006 6d2d6675656c` is a 6-byte key `m-fuel`; `00000002 6f6b` is the value `ok`.

### In Python

The Python port (`code/python/main.py`) is the same design with the same type names and four differences. **`struct` instead of `ByteBuffer`:** two precompiled `struct.Struct` objects, `">i"` (big-endian int32) and `">qqi"` (int64, int64, int32), pack and unpack the frame; `unpack_from(data, position)` reads at an offset without copying, which is what `frame_length_at` needs. **Integers do not wrap:** FNV-1a depends on 32-bit overflow, so `fnv1a32` masks with `& 0xFFFFFFFF` after every multiply; forget the mask and the hash diverges from Java and Go after the first byte, and every key lands in a different partition than the other two implementations would choose. **Iterating `bytes` yields ints:** `for b in data` already gives `0..255`, so there is no signed-byte problem to fix; Java needed `& 0xFF` for the same reason. **Exceptions are `OffsetOutOfRangeError` and `SegmentFullError`**, subclasses of `Exception`, raised where Java throws; `LogConfig` validation raises `ValueError`.

Run it from `code/python/` with `python3 main.py`; test with `python3 -m unittest -v`.

#### Step 1: Configuration, records, and the storage boundary

Frozen dataclasses play the role of the Java records, with the validation in `__post_init__`. `StoredRecord.key` is `Optional[bytes]` because `None` means "no key", exactly the distinction the partitioner needs. The two storage contracts are plain classes whose methods raise `NotImplementedError`; `InMemoryStorage` (further down in `main.py`) keeps a `dict` of `dict`s, partition to base offset to `bytearray`, which is the `<topic>-<partition>/<baseOffset>.log` layout, and raises `RuntimeError` on a duplicate `create`.

```python
@dataclass(frozen=True)
class LogConfig:
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
    offset: int
    timestamp: int
    key: Optional[bytes]
    value: bytes


class SegmentStorage:
    def create(self, partition: int, base_offset: int) -> SegmentData:
        raise NotImplementedError

    def delete(self, partition: int, base_offset: int) -> None:
        raise NotImplementedError

    def base_offsets(self, partition: int) -> List[int]:
        raise NotImplementedError
```

#### Step 2: The frame codec

`encode` builds the frame in a `bytearray` and returns immutable `bytes`. The null key is `_HEAD.pack(offset, timestamp, -1)`; `struct` writes `-1` as `ffffffff`, so the bytes are identical to Java's `putInt(-1)`. `frame_length_at` reads only the four-byte prefix and adds it back on, which is the hop the sparse-index scan performs. The hash sits beside the codec as a static method; the `& 0xFFFFFFFF` after the multiply is the whole difference from Java.

```python
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
    def frame_length_at(data: bytes, position: int) -> int:
        return RecordCodec.LENGTH_PREFIX + _LENGTH_PREFIX.unpack_from(data, position)[0]
```

```python
    @staticmethod
    def fnv1a32(data: bytes) -> int:
        h = 0x811C9DC5
        for b in data:
            h ^= b
            h = (h * 0x01000193) & 0xFFFFFFFF
        return h
```

#### Step 3: Segment append with a sparse index

Line for line the Java method. `_bytes_since_index_entry` is initialised to `2**31 - 1` in `__init__` so the first record always gets the index entry at `(0, 0)`, and the `not self.is_empty()` guard is what lets an oversized frame land alone in an empty segment instead of being rejected forever.

```python
    def append(self, key: Optional[bytes], value: bytes, timestamp: int) -> int:
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
```

#### Step 4: Resolving an offset to a byte position

The "last entry `<=` target" binary search, then a forward scan that reads one length prefix per hop through `_frame_length_at`, which asks the storage for exactly four bytes. Python's `(lo + hi) // 2` cannot overflow, so the `>>> 1` trick is not needed; the search is otherwise identical.

```python
    def position_of(self, offset: int) -> int:
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
```

#### Step 5: Partition append, roll, and cross-segment read

The partition decides the roll because it owns the list; the new segment's base is the old one's `next_offset()`, so offsets stay dense across the boundary. `read` keeps Kafka's three cases: `from_offset == end` returns `[]`, outside `[start, end]` raises `OffsetOutOfRangeError`, and anything else walks segments from `_segment_index_for` (a binary search over base offsets) in a `while` loop until it has `max_records` or runs out.

```python
    def append(self, key: Optional[bytes], value: bytes, timestamp: int) -> int:
        active = self._active()
        frame = RecordCodec.frame_size(key, value)
        if not active.is_empty() and not active.has_room_for(frame):
            active = self._new_segment(active.next_offset())
            self._segments.append(active)
        return active.append(key, value, timestamp)

    def read(self, from_offset: int, max_records: int) -> List[StoredRecord]:
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
```

#### Step 6: Retention and the partitioner

`pop(0)` drops the oldest segment and the storage is told to delete it; the `len(self._segments) > 1` guard keeps the active segment alive so the offset counter survives. `partition_for` lives on `Topic`: the round-robin counter is advanced by hand (no `++` in Python), and the hash is masked with `0x7FFFFFFF` before the modulo even though Python's `%` of a non-negative number is already non-negative, so that all three languages pick the same partition for the same key.

```python
    def enforce_retention(self) -> int:
        removed = 0
        while len(self._segments) > 1 and self.size_bytes() > self._config.retention_bytes:
            oldest = self._segments.pop(0)
            self._storage.delete(self.id, oldest.base_offset)
            removed += 1
        return removed
```

```python
    def partition_for(self, key: Optional[bytes]) -> int:
        if key is None:
            p = self._round_robin % len(self._partitions)
            self._round_robin += 1
            return p
        return (RecordCodec.fnv1a32(key) & 0x7FFFFFFF) % len(self._partitions)
```

#### Step 7: Consumer position and commit

The constructor covers both reset cases (no commit yet, or a commit that retention has retired) by falling back to `log_start_offset()`. `poll` advances `_position` to last-delivered plus one, and `commit` stores exactly that number in the `OffsetStore`, whose `NONE` sentinel is `-1` as in Java.

```python
    def __init__(self, partition: Partition, group: str, offsets: OffsetStore) -> None:
        self._partition = partition
        self._group = group
        self._offsets = offsets
        committed = offsets.committed(group, partition.id)
        if committed == OffsetStore.NONE or committed < partition.log_start_offset() or committed > partition.log_end_offset():
            self._position = partition.log_start_offset()
        else:
            self._position = committed

    def poll(self, max_records: int) -> List[StoredRecord]:
        batch = self._partition.read(self._position, max_records)
        if batch:
            self._position = batch[-1].offset + 1
        return batch

    def commit(self) -> None:
        self._offsets.commit(self._group, self._partition.id, self._position)
```

#### Step 8: Run it

```bash
cd phases/08-messaging-and-streams/01-the-append-only-log/code/python
python3 main.py
python3 -m unittest -v
```

The output is identical to the Java run: the same partition assignments, the same segment layout, the same byte counts, and the same frame hex.

```console
== Partition layout after segment rolls (segmentBytes=160) ==
partition 0: logStart=0 logEnd=3 segments(base offsets)=[0] bytes=159
partition 1: logStart=0 logEnd=6 segments(base offsets)=[0, 2, 4] bytes=330
partition 2: logStart=0 logEnd=3 segments(base offsets)=[0, 2] bytes=162

== Commit, crash, resume (partition 1, six records) ==
fresh consumer with no commit starts at logStart: position 0
processed 2 and committed: committed offset = 2
processed 2 more without committing: position 4 (then the process dies)
restart resumes at the committed offset: position 2 -> offsets 2 and 3 are delivered again (at-least-once)

== The frame on the wire ==
36 bytes: 0000002000000000000000070000018bcfe56800000000066d2d6675656c000000026f6b
```

### In Go

The Go port (`code/go/main.go`) keeps the design and changes how failure is reported. **Errors are values:** `Segment.Append`, `Read`, `PositionOf`, `SeekTo`, and `Commit` return `error`; `outOfRange(...)` wraps the sentinel `ErrOffsetOutOfRange` so tests use `errors.Is` where Java uses `checkThrows`. `SeekTo` is Java's `seek`, renamed because `go vet` reserves the name `Seek` for `io.Seeker`'s signature. `Partition.Append` returns a plain `int64`: a fresh segment accepts any frame, so the roll can never fail. **`binary.BigEndian.AppendUint32`** (Go 1.19+) builds the frame without a `bytes.Buffer`; `-1` for a null key is written as `uint32(0xFFFFFFFF)` and read back as `int32`, which is the two's-complement round trip Java gets for free. **`nil` versus empty slice:** a nil key means "no key"; an empty `[]byte{}` is a zero-length key with its own partition, and `Decode` returns `nil` when `keyLength` is -1 so the partitioner's `key == nil` check agrees with Java's `null`. **`Fnv1a32` returns `uint32`**, so `PartitionFor` casts to `int` after masking the top bit; no negative modulo is possible.

Run it from `code/go/` with `go run .`; test with `go test ./...`.

#### Step 1: Configuration, records, and the storage boundary

Structs with exported fields replace the records; a struct literal has no constructor, so `NewLogConfig` is the validating path and returns an error where Java's compact constructor throws. `StoredRecord.Key` is a `[]byte` whose `nil` value means "no key". The two storage contracts are interfaces; `InMemoryStorage` (also in `main.go`) keeps `map[int]map[int64]*InMemorySegmentData` and panics on a duplicate `Create`, because that is a programming error, as Java's `IllegalStateException` says.

```go
type LogConfig struct {
	SegmentBytes       int
	IndexIntervalBytes int
	RetentionBytes     int64
}

func NewLogConfig(segmentBytes, indexIntervalBytes int, retentionBytes int64) (LogConfig, error) {
	if segmentBytes <= 0 {
		return LogConfig{}, errors.New("segmentBytes must be positive")
	}
	if indexIntervalBytes <= 0 {
		return LogConfig{}, errors.New("indexIntervalBytes must be positive")
	}
	if retentionBytes <= 0 {
		return LogConfig{}, errors.New("retentionBytes must be positive")
	}
	return LogConfig{segmentBytes, indexIntervalBytes, retentionBytes}, nil
}

type StoredRecord struct {
	Offset    int64
	Timestamp int64
	Key       []byte
	Value     []byte
}

type SegmentData interface {
	Append(bytes []byte)
	Size() int
	Read(position, length int) []byte
}

type SegmentStorage interface {
	Create(partition int, baseOffset int64) SegmentData
	Delete(partition int, baseOffset int64)
	BaseOffsets(partition int) []int64
}
```

#### Step 2: The frame codec

`Encode` appends each field to a slice pre-sized by `FrameSize`; every integer goes through `binary.BigEndian`, which is network byte order and what Kafka's wire protocol uses. `FrameLengthAt` converts the prefix through `int32` before `int` so a corrupt negative length is seen as negative rather than as four billion. `Fnv1a32` needs no mask: `uint32` multiplication wraps at 32 bits by definition, and `uint32(b)` on a Go `byte` is already `0..255`.

```go
func Encode(offset, timestamp int64, key, value []byte) []byte {
	if value == nil {
		panic("value must not be nil")
	}
	buf := make([]byte, 0, FrameSize(key, value))
	buf = binary.BigEndian.AppendUint32(buf, uint32(FrameSize(key, value)-lengthPrefix))
	buf = binary.BigEndian.AppendUint64(buf, uint64(offset))
	buf = binary.BigEndian.AppendUint64(buf, uint64(timestamp))
	if key == nil {
		buf = binary.BigEndian.AppendUint32(buf, uint32(0xFFFFFFFF))
	} else {
		buf = binary.BigEndian.AppendUint32(buf, uint32(len(key)))
		buf = append(buf, key...)
	}
	buf = binary.BigEndian.AppendUint32(buf, uint32(len(value)))
	buf = append(buf, value...)
	return buf
}

func FrameLengthAt(data []byte, position int) int {
	return lengthPrefix + int(int32(binary.BigEndian.Uint32(data[position:])))
}
```

```go
func Fnv1a32(bytes []byte) uint32 {
	hash := uint32(0x811C9DC5)
	for _, b := range bytes {
		hash ^= uint32(b)
		hash *= 0x01000193
	}
	return hash
}
```

#### Step 3: Segment append with a sparse index

The same three invariants: `nextOffset` starts at `BaseOffset` (set in `NewSegment`), `bytesSinceIndexEntry` starts at the largest `int` (`int(^uint(0) >> 1)`) so the first record is indexed at `(0, 0)`, and an empty segment accepts any frame. A full segment returns `ErrSegmentFull` wrapped with the base offset instead of throwing.

```go
func (s *Segment) Append(key, value []byte, timestamp int64) (int64, error) {
	frame := FrameSize(key, value)
	if !s.IsEmpty() && !s.HasRoomFor(frame) {
		return 0, fmt.Errorf("%w: segment %d", ErrSegmentFull, s.BaseOffset)
	}
	offset := s.nextOffset
	encoded := Encode(offset, timestamp, key, value)
	if s.bytesSinceIndexEntry >= s.indexIntervalBytes {
		s.index = append(s.index, IndexEntry{int(offset - s.BaseOffset), s.data.Size()})
		s.bytesSinceIndexEntry = 0
	}
	s.data.Append(encoded)
	s.bytesSinceIndexEntry += len(encoded)
	s.nextOffset++
	return offset, nil
}
```

#### Step 4: Resolving an offset to a byte position

`int(uint(lo+hi) >> 1)` is the Go spelling of Java's `>>> 1`: the unsigned shift keeps the midpoint correct even if `lo + hi` would overflow a signed `int`. The rest is the same last-entry-`<=`-target search followed by a forward scan that reads one length prefix per hop; an out-of-range offset returns the wrapped sentinel rather than panicking.

```go
func (s *Segment) PositionOf(offset int64) (int, error) {
	if offset < s.BaseOffset || offset >= s.nextOffset {
		return 0, outOfRange(offset, s.BaseOffset, s.nextOffset-1)
	}
	relative := int(offset - s.BaseOffset)
	lo, hi, best := 0, len(s.index)-1, 0
	for lo <= hi {
		mid := int(uint(lo+hi) >> 1)
		if s.index[mid].RelativeOffset <= relative {
			best = mid
			lo = mid + 1
		} else {
			hi = mid - 1
		}
	}
	entry := s.index[best]
	position := entry.Position
	current := s.BaseOffset + int64(entry.RelativeOffset)
	for current < offset {
		position += s.frameLengthAt(position)
		current++
	}
	return position, nil
}
```

#### Step 5: Partition append, roll, and cross-segment read

`Partition.Append` has no error return: after a roll the active segment is empty and accepts any frame, so the `panic(err)` branch is unreachable and says so. `Read` keeps the three Kafka cases and threads each segment's error upward; note that it returns a non-nil empty slice at `LogEndOffset` so callers can `range` over it without a nil check.

```go
func (p *Partition) Append(key, value []byte, timestamp int64) int64 {
	active := p.active()
	frame := FrameSize(key, value)
	if !active.IsEmpty() && !active.HasRoomFor(frame) {
		active = p.newSegment(active.NextOffset())
		p.segments = append(p.segments, active)
	}
	offset, err := active.Append(key, value, timestamp)
	if err != nil {
		panic(err) // unreachable: a fresh segment accepts any size
	}
	return offset
}

func (p *Partition) Read(fromOffset int64, maxRecords int) ([]StoredRecord, error) {
	start, end := p.LogStartOffset(), p.LogEndOffset()
	if fromOffset < start || fromOffset > end {
		return nil, outOfRange(fromOffset, start, end)
	}
	out := []StoredRecord{}
	if fromOffset == end {
		return out, nil
	}
	next := fromOffset
	for i := p.segmentIndexFor(fromOffset); i < len(p.segments) && len(out) < maxRecords; i++ {
		chunk, err := p.segments[i].Read(next, maxRecords-len(out))
		if err != nil {
			return nil, err
		}
		out = append(out, chunk...)
		if len(chunk) > 0 {
			next = chunk[len(chunk)-1].Offset + 1
		}
	}
	return out, nil
}
```

#### Step 6: Retention and the partitioner

Reslicing `p.segments[1:]` drops the oldest segment in O(1); the `len(p.segments) > 1` guard protects the active segment and its offset counter. `PartitionFor` masks the `uint32` hash to 31 bits and converts to `int` before the modulo, so the result is non-negative without the sign-bit worry Java has; the round-robin counter is an `int64` field on `Topic`.

```go
func (p *Partition) EnforceRetention() int {
	removed := 0
	for len(p.segments) > 1 && p.SizeBytes() > p.config.RetentionBytes {
		oldest := p.segments[0]
		p.segments = p.segments[1:]
		p.storage.Delete(p.ID, oldest.BaseOffset)
		removed++
	}
	return removed
}
```

```go
func (t *Topic) PartitionFor(key []byte) int {
	if key == nil {
		p := int(t.roundRobin % int64(len(t.partitions)))
		t.roundRobin++
		return p
	}
	return int(Fnv1a32(key)&0x7FFFFFFF) % len(t.partitions)
}
```

#### Step 7: Consumer position and commit

`NewConsumer` applies the same two reset rules and falls back to `LogStartOffset`; `None` is the package-level `-1` sentinel that `OffsetStore.Committed` returns for a group that never committed. `Poll` propagates the read error and otherwise advances `position` to last-delivered plus one; `Commit` returns whatever error the store returns (only a negative offset can fail).

```go
func NewConsumer(partition *Partition, group string, offsets *OffsetStore) *Consumer {
	c := &Consumer{partition: partition, group: group, offsets: offsets}
	committed := offsets.Committed(group, partition.ID)
	if committed == None || committed < partition.LogStartOffset() || committed > partition.LogEndOffset() {
		c.position = partition.LogStartOffset()
	} else {
		c.position = committed
	}
	return c
}

func (c *Consumer) Poll(maxRecords int) ([]StoredRecord, error) {
	batch, err := c.partition.Read(c.position, maxRecords)
	if err != nil {
		return nil, err
	}
	if len(batch) > 0 {
		c.position = batch[len(batch)-1].Offset + 1
	}
	return batch, nil
}

func (c *Consumer) Commit() error { return c.offsets.Commit(c.group, c.partition.ID, c.position) }
```

#### Step 8: Run it

```bash
cd phases/08-messaging-and-streams/01-the-append-only-log/code/go
go run .
go test ./...
```

The output is identical to the Java and Python runs, down to the frame hex:

```console
== Partition layout after segment rolls (segmentBytes=160) ==
partition 0: logStart=0 logEnd=3 segments(base offsets)=[0] bytes=159
partition 1: logStart=0 logEnd=6 segments(base offsets)=[0, 2, 4] bytes=330
partition 2: logStart=0 logEnd=3 segments(base offsets)=[0, 2] bytes=162

== Reading partition 0 from offset 1, three at a time ==
offset 1 ts 1700000000007 key m-fuel value auth-1007 19.99 USD
offset 2 ts 1700000000011 key m-fuel value auth-1011 19.99 USD

== The frame on the wire ==
36 bytes: 0000002000000000000000070000018bcfe56800000000066d2d6675656c000000026f6b
```

## Use It

### Apache Kafka

Everything here has a direct counterpart in a Kafka broker. `Partition` is `UnifiedLog`; `Segment` is `LogSegment` with a `.log` file and a `.index` file; `indexIntervalBytes` is `log.index.interval.bytes` (default 4096); `segmentBytes` is `log.segment.bytes` (default 1 GiB); `retentionBytes` is `log.retention.bytes`, alongside the time-based `log.retention.ms` this lesson leaves as an exercise. `logStartOffset` and `logEndOffset` are Kafka's names. The consumer's committed offset is stored in the internal `__consumer_offsets` topic rather than a map, and `auto.offset.reset` chooses between `earliest` (what this lesson does) and `latest`.

What Kafka adds that the from-scratch version made visible by omission: replication with a high watermark (readers see only records all in-sync replicas have), a time index (`.timeindex`) for `offsetsForTimes`, record batching with compression and per-batch CRC, an idempotent producer with sequence numbers to deduplicate retries, and log compaction. Kafka's default partitioner also changed in 3.x: unkeyed records use a "sticky" partitioner that fills one batch per partition before moving on, rather than per-record round robin, because batching matters more than per-record spread.

### Other logs

Apache Pulsar separates the log (BookKeeper ledgers) from the serving layer, so segments live on different machines from the brokers. Amazon Kinesis calls partitions shards and offsets sequence numbers, with a hard 24-hour to 365-day retention window. Redis Streams (`XADD`, `XREAD`, consumer groups with `XACK`) is a single-node log with the same offset-owned-by-reader model and IDs of the form `timestamp-sequence`. PostgreSQL's write-ahead log is a single-partition append-only log with LSNs as offsets and `pg_wal` segment files of 16 MB, which is why Phase 7, Lesson 02 is a prerequisite here: a WAL is a log with one reader (recovery) and a database is a log with a very elaborate index.

### The trap

The commonest production mistake with any of these is treating the committed offset as "processed up to and including". A consumer that commits `lastOffset` instead of `lastOffset + 1` re-processes one record after every restart; one that commits `lastOffset + 1` before the side effect completes loses one record on every crash. Kafka's `commitSync(Map<TopicPartition, OffsetAndMetadata>)` documentation states the convention in one sentence, and it is the sentence to read twice.

## Ship It

`outputs/checklist-log-topic-design.md` is a design-review checklist for any new topic that will carry money-moving events: partition key choice, partition count, retention versus the slowest consumer, offset commit discipline, and the questions to ask before changing any of them in production. Attach it to the design review for the next topic your team creates.

## Exercises

1. **Reinforce.** Add time-based retention: `LogConfig.retentionMillis` and `enforceRetention(nowMillis)` that drops a closed segment when its newest record's timestamp is older than `now - retentionMillis`. Keep size-based retention; a segment goes when either rule says so. Test with the `TickClock`.
2. **Apply.** Implement `offsetForTimestamp(long ts)` on `Partition`: the first offset whose record timestamp is `>= ts`, or `logEndOffset` if none. Do it with a per-segment time index `(timestamp, relativeOffset)` written at the same interval as the offset index, then binary search, then scan. This is Kafka's `offsetsForTimes`, the mechanism behind "replay everything since 02:00".
3. **Extend.** Replace `InMemoryStorage` with a file-backed `SegmentStorage` that writes each segment to `<dir>/<partition>/<baseOffset>.log` using the WAL discipline from Phase 7, Lesson 02 (append, then `fsync` before acknowledging). Rebuild the sparse index on startup by scanning frames. Then measure: how many appends per second do you lose by fsyncing every record versus every 10 ms, and which would you choose for a ledger topic?

## Key Terms

| Term | What people say | What it actually means |
|------|-----------------|------------------------|
| Offset | "the message ID" | The dense, zero-based index of a record within one partition; meaningless across partitions |
| Partition | "a shard of the topic" | An independent append-only log with its own offsets and total order; the unit of parallelism and of ordering |
| Segment | "a chunk of the log" | A contiguous run of records starting at a base offset, stored as one file; the unit of retention |
| Sparse index | "the offset index" | One `(relativeOffset, position)` entry every N bytes, resolved by binary search plus a bounded forward scan |
| Log start / end offset | "earliest / latest" | The oldest offset still retained and the offset the next append will receive; a read at the end offset is empty, not an error |
| Committed offset | "where the consumer is" | The next offset the group will read, stored by the broker; everything after it is redelivered on restart |
| Retention | "how long we keep messages" | A rule (bytes, time, or compaction) that deletes whole closed segments from the oldest end; never touches the active segment |
| Partitioner | "the hashing" | A pure function from key to partition index; same key, same partition, for as long as the partition count does not change |

## Further Reading

- [Apache Kafka documentation, section 4 "Design" and 5.4 "Log"](https://kafka.apache.org/documentation/#log) — the segment file layout, the sparse index, and the exact read algorithm this lesson reproduces.
- [Kreps, Narkhede, Rao, "Kafka: a Distributed Messaging System for Log Processing" (NetDB 2011)](https://www.microsoft.com/en-us/research/wp-content/uploads/2017/09/Kafka.pdf) — the original paper; section 3.1 explains why the broker keeps no per-message state and the consumer owns the offset.
- [Jay Kreps, "The Log: What every software engineer should know about real-time data's unifying abstraction" (2013)](https://engineering.linkedin.com/distributed-systems/log-what-every-software-engineer-should-know-about-real-time-datas-unifying) — the argument that the log, not the queue, is the primitive for data integration.
- [Kafka `KafkaConsumer` Javadoc: offsets and consumer position](https://kafka.apache.org/documentation/#consumerconfigs_auto.offset.reset) — the "committed offset is the next offset to read" convention and the `auto.offset.reset` behaviours.
- [Fowler and Noll, FNV hash specification](http://www.isthe.com/chongo/tech/comp/fnv/) — the FNV-1a constants and algorithm used by the partitioner, including why the 32-bit variant multiplies by 16777619.
- [PostgreSQL documentation, chapter 30 "Reliability and the Write-Ahead Log"](https://www.postgresql.org/docs/current/wal-intro.html) — a single-partition log with 16 MB segment files and LSNs as offsets; compare with Kafka's layout.
