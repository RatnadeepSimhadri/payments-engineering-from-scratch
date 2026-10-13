// Lesson: phases/08-messaging-and-streams/01-the-append-only-log/docs/en.md
// The Append-Only Log: Partitions, Offsets, and Segments
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: Apache Kafka documentation, section 5.4 "Log" (segment files, sparse offset index, retention);
//          Kreps, Narkhede, Rao, "Kafka: a Distributed Messaging System for Log Processing" (NetDB 2011).
// Java 21, standard library only. Segments live behind an injected SegmentStorage; the in-memory one holds byte arrays
// with the exact frame layout a file would hold.

import java.nio.ByteBuffer;
import java.nio.charset.StandardCharsets;
import java.util.ArrayList;
import java.util.Collections;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.Objects;
import java.util.TreeMap;

/** Injected time source so demos and tests are deterministic. */
interface Clock {
    long now();
}

/** A clock that advances one millisecond per call, starting at a fixed epoch. */
final class TickClock implements Clock {
    private long next;

    TickClock(long startMillis) { this.next = startMillis; }

    @Override
    public long now() { return next++; }
}

/** Sizing rules for every partition of a topic. */
record LogConfig(int segmentBytes, int indexIntervalBytes, long retentionBytes) {
    LogConfig {
        if (segmentBytes <= 0) throw new IllegalArgumentException("segmentBytes must be positive");
        if (indexIntervalBytes <= 0) throw new IllegalArgumentException("indexIntervalBytes must be positive");
        if (retentionBytes <= 0) throw new IllegalArgumentException("retentionBytes must be positive");
    }
}

/** What a consumer receives: the record plus the coordinates the log assigned to it. */
record StoredRecord(long offset, long timestamp, byte[] key, byte[] value) {
    String keyString() { return key == null ? "null" : new String(key, StandardCharsets.UTF_8); }

    String valueString() { return new String(value, StandardCharsets.UTF_8); }
}

/** Where a produced record landed. */
record RecordMetadata(int partition, long offset) {}

/** One entry of the sparse offset index: "offset baseOffset+relativeOffset starts at byte position". */
record IndexEntry(int relativeOffset, int position) {}

final class OffsetOutOfRangeException extends RuntimeException {
    OffsetOutOfRangeException(long offset, long start, long end) {
        super("offset " + offset + " is outside [" + start + ", " + end + "]");
    }
}

final class SegmentFullException extends RuntimeException {
    SegmentFullException(long baseOffset) { super("segment " + baseOffset + " is full"); }
}

/**
 * The bytes of one segment, as a file would present them: append at the end, read any range. The Segment never
 * touches a byte array directly, so the same Segment logic works over a file, a block store, or the in-memory version.
 */
interface SegmentData {
    void append(byte[] bytes);

    int size();

    byte[] read(int position, int length);
}

/** Creates, lists, and deletes segment data by base offset. Retention goes through here so a file-backed store can unlink. */
interface SegmentStorage {
    SegmentData create(int partition, long baseOffset);

    void delete(int partition, long baseOffset);

    List<Long> baseOffsets(int partition);
}

/** The in-memory storage used by the demo and the tests. One growable byte array per segment. */
final class InMemoryStorage implements SegmentStorage {
    /** partition -> baseOffset -> bytes; the same shape as Kafka's "<topic>-<partition>/<baseOffset>.log" directories. */
    private final Map<Integer, TreeMap<Long, InMemorySegmentData>> partitions = new HashMap<>();

    private TreeMap<Long, InMemorySegmentData> dir(int partition) {
        return partitions.computeIfAbsent(partition, p -> new TreeMap<>());
    }

    @Override
    public SegmentData create(int partition, long baseOffset) {
        if (dir(partition).containsKey(baseOffset)) throw new IllegalStateException("segment " + partition + "/" + baseOffset + " already exists");
        InMemorySegmentData data = new InMemorySegmentData();
        dir(partition).put(baseOffset, data);
        return data;
    }

    @Override
    public void delete(int partition, long baseOffset) {
        if (dir(partition).remove(baseOffset) == null) throw new IllegalStateException("no segment " + partition + "/" + baseOffset);
    }

    @Override
    public List<Long> baseOffsets(int partition) { return new ArrayList<>(dir(partition).keySet()); }

    static final class InMemorySegmentData implements SegmentData {
        private byte[] data = new byte[256];
        private int size = 0;

        @Override
        public void append(byte[] bytes) {
            int needed = size + bytes.length;
            if (needed > data.length) {
                byte[] bigger = new byte[Math.max(needed, data.length * 2)];
                System.arraycopy(data, 0, bigger, 0, size);
                data = bigger;
            }
            System.arraycopy(bytes, 0, data, size, bytes.length);
            size += bytes.length;
        }

        @Override
        public int size() { return size; }

        @Override
        public byte[] read(int position, int length) {
            if (position < 0 || length < 0 || position + length > size) {
                throw new IndexOutOfBoundsException("read [" + position + ", " + (position + length) + ") of " + size);
            }
            byte[] out = new byte[length];
            System.arraycopy(data, position, out, 0, length);
            return out;
        }
    }
}

/**
 * The byte layout of one record ("frame"), big-endian, exactly as it would sit in a segment file:
 *
 *   int32 frameLength   bytes after this field
 *   int64 offset
 *   int64 timestamp
 *   int32 keyLength     -1 when the key is null
 *   bytes key
 *   int32 valueLength
 *   bytes value
 *
 * The length prefix is what lets a reader skip a record it does not care about without parsing it.
 */
final class RecordCodec {
    static final int LENGTH_PREFIX = 4;
    static final int HEADER = 8 + 8 + 4 + 4; // offset, timestamp, keyLength, valueLength

    static int frameSize(byte[] key, byte[] value) {
        return LENGTH_PREFIX + HEADER + (key == null ? 0 : key.length) + value.length;
    }

    static byte[] encode(long offset, long timestamp, byte[] key, byte[] value) {
        Objects.requireNonNull(value, "value");
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

    /** Decode the frame starting at {@code position}; the caller advances by {@link #frameLengthAt}. */
    static StoredRecord decode(byte[] data, int position) {
        ByteBuffer buf = ByteBuffer.wrap(data, position, data.length - position);
        int frameLength = buf.getInt();
        if (frameLength < HEADER) throw new IllegalStateException("corrupt frame at " + position);
        long offset = buf.getLong();
        long timestamp = buf.getLong();
        int keyLength = buf.getInt();
        byte[] key = null;
        if (keyLength >= 0) {
            key = new byte[keyLength];
            buf.get(key);
        }
        int valueLength = buf.getInt();
        byte[] value = new byte[valueLength];
        buf.get(value);
        return new StoredRecord(offset, timestamp, key, value);
    }

    /** Total bytes occupied by the frame at {@code position}, including its own length prefix. */
    static int frameLengthAt(byte[] data, int position) {
        return LENGTH_PREFIX + ByteBuffer.wrap(data, position, 4).getInt();
    }

    /** FNV-1a 32-bit. Chosen over Kafka's murmur2 because it is five lines and identical in all three languages. */
    static int fnv1a32(byte[] bytes) {
        int hash = 0x811C9DC5;
        for (byte b : bytes) {
            hash ^= (b & 0xFF);
            hash *= 0x01000193;
        }
        return hash;
    }
}

/**
 * A contiguous run of records with offsets [baseOffset, nextOffset). Bytes are appended and never modified.
 * The sparse index records one (relativeOffset, position) pair roughly every indexIntervalBytes.
 */
final class Segment {
    final long baseOffset;
    private final SegmentData data;
    private final int maxBytes;
    private final int indexIntervalBytes;
    private long nextOffset;
    private final List<IndexEntry> index = new ArrayList<>();
    private int bytesSinceIndexEntry = Integer.MAX_VALUE; // force an entry for the first record

    Segment(long baseOffset, SegmentData data, int maxBytes, int indexIntervalBytes) {
        this.baseOffset = baseOffset;
        this.data = data;
        this.nextOffset = baseOffset;
        this.maxBytes = maxBytes;
        this.indexIntervalBytes = indexIntervalBytes;
    }

    int sizeBytes() { return data.size(); }

    long nextOffset() { return nextOffset; }

    int recordCount() { return (int) (nextOffset - baseOffset); }

    boolean isEmpty() { return data.size() == 0; }

    List<IndexEntry> indexEntries() { return Collections.unmodifiableList(index); }

    boolean hasRoomFor(int frameBytes) { return data.size() + frameBytes <= maxBytes; }

    /** Append one record and return its offset. An empty segment accepts any size so oversized records still land. */
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

    /** Bytes occupied by the frame at {@code position}, learned by reading only its 4-byte length prefix. */
    private int frameLengthAt(int position) {
        return RecordCodec.frameLengthAt(data.read(position, RecordCodec.LENGTH_PREFIX), 0);
    }

    /**
     * Byte position of the record at {@code offset}: binary-search the sparse index for the last entry at or
     * before the target, then scan forward frame by frame. The scan is bounded by indexIntervalBytes.
     */
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

    /** Read up to maxRecords starting at {@code offset}, stopping at the end of this segment. */
    List<StoredRecord> read(long offset, int maxRecords) {
        List<StoredRecord> out = new ArrayList<>();
        if (offset >= nextOffset || maxRecords <= 0) return out;
        int position = positionOf(offset);
        while (position < data.size() && out.size() < maxRecords) {
            int length = frameLengthAt(position);
            out.add(RecordCodec.decode(data.read(position, length), 0));
            position += length;
        }
        return out;
    }
}

/** An ordered list of segments. The last segment is active; older ones are immutable and eligible for retention. */
final class Partition {
    final int id;
    private final LogConfig config;
    private final SegmentStorage storage;
    private final List<Segment> segments = new ArrayList<>();

    Partition(int id, LogConfig config, SegmentStorage storage) {
        this.id = id;
        this.config = config;
        this.storage = storage;
        segments.add(newSegment(0));
    }

    private Segment newSegment(long baseOffset) {
        return new Segment(baseOffset, storage.create(id, baseOffset), config.segmentBytes(), config.indexIntervalBytes());
    }

    private Segment active() { return segments.get(segments.size() - 1); }

    long logStartOffset() { return segments.get(0).baseOffset; }

    /** The offset the next record will receive; also "high watermark" in a single-replica log. */
    long logEndOffset() { return active().nextOffset(); }

    int segmentCount() { return segments.size(); }

    List<Long> segmentBaseOffsets() {
        List<Long> out = new ArrayList<>();
        for (Segment s : segments) out.add(s.baseOffset);
        return out;
    }

    long sizeBytes() {
        long total = 0;
        for (Segment s : segments) total += s.sizeBytes();
        return total;
    }

    /** Append and return the assigned offset. Rolls to a new segment when the active one cannot take the frame. */
    long append(byte[] key, byte[] value, long timestamp) {
        Segment active = active();
        int frame = RecordCodec.frameSize(key, value);
        if (!active.isEmpty() && !active.hasRoomFor(frame)) {
            active = newSegment(active.nextOffset());
            segments.add(active);
        }
        return active.append(key, value, timestamp);
    }

    /** Index of the segment whose offset range contains {@code offset}: the last segment with baseOffset <= offset. */
    private int segmentIndexFor(long offset) {
        int lo = 0, hi = segments.size() - 1, best = 0;
        while (lo <= hi) {
            int mid = (lo + hi) >>> 1;
            if (segments.get(mid).baseOffset <= offset) { best = mid; lo = mid + 1; } else { hi = mid - 1; }
        }
        return best;
    }

    /**
     * Read up to maxRecords from {@code fromOffset}, crossing segment boundaries. Reading at logEndOffset returns
     * an empty list (nothing yet); reading below logStartOffset or above logEndOffset is an error, as in Kafka.
     */
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

    /** Drop whole segments, oldest first, until the partition fits in retentionBytes. The active segment is never dropped. */
    int enforceRetention() {
        int removed = 0;
        while (segments.size() > 1 && sizeBytes() > config.retentionBytes()) {
            Segment oldest = segments.remove(0);
            storage.delete(id, oldest.baseOffset);
            removed++;
        }
        return removed;
    }
}

/** A named set of partitions. Records with the same non-null key always land in the same partition. */
final class Topic {
    final String name;
    private final List<Partition> partitions = new ArrayList<>();
    private long roundRobin = 0;

    Topic(String name, int partitionCount, LogConfig config, SegmentStorage storage) {
        if (partitionCount <= 0) throw new IllegalArgumentException("need at least one partition");
        this.name = name;
        for (int i = 0; i < partitionCount; i++) partitions.add(new Partition(i, config, storage));
    }

    int partitionCount() { return partitions.size(); }

    Partition partition(int id) { return partitions.get(id); }

    /** Keyed records hash; unkeyed records rotate. The hash is masked to non-negative before the modulo. */
    int partitionFor(byte[] key) {
        if (key == null) return (int) (roundRobin++ % partitions.size());
        return (RecordCodec.fnv1a32(key) & 0x7FFFFFFF) % partitions.size();
    }

    RecordMetadata append(byte[] key, byte[] value, long timestamp) {
        int p = partitionFor(key);
        return new RecordMetadata(p, partitions.get(p).append(key, value, timestamp));
    }
}

/**
 * Committed consumer positions, keyed by (group, partition). In Kafka this is the __consumer_offsets topic; here it is
 * a map, but the contract is the same: the committed value is the NEXT offset to read, not the last one processed.
 */
final class OffsetStore {
    static final long NONE = -1;
    private final Map<String, Long> committed = new HashMap<>();

    private static String key(String group, int partition) { return group + "/" + partition; }

    void commit(String group, int partition, long nextOffset) {
        if (nextOffset < 0) throw new IllegalArgumentException("offset must be non-negative");
        committed.put(key(group, partition), nextOffset);
    }

    long committed(String group, int partition) { return committed.getOrDefault(key(group, partition), NONE); }
}

/**
 * A single-partition reader that belongs to a consumer group. Position is the next offset to read. A new Consumer
 * resumes from the group's committed offset; if there is none, or it has been retired by retention, it starts at
 * logStartOffset (Kafka's auto.offset.reset=earliest).
 */
final class Consumer {
    private final Partition partition;
    private final String group;
    private final OffsetStore offsets;
    private long position;

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

    void seek(long offset) {
        if (offset < partition.logStartOffset() || offset > partition.logEndOffset()) {
            throw new OffsetOutOfRangeException(offset, partition.logStartOffset(), partition.logEndOffset());
        }
        this.position = offset;
    }

    long position() { return position; }

    List<StoredRecord> poll(int maxRecords) {
        List<StoredRecord> batch = partition.read(position, maxRecords);
        if (!batch.isEmpty()) position = batch.get(batch.size() - 1).offset() + 1;
        return batch;
    }

    /** Record that everything below {@code position} has been processed. Anything polled but not committed is redelivered after a restart. */
    void commit() { offsets.commit(group, partition.id, position); }
}

public final class Main {
    static byte[] utf8(String s) { return s == null ? null : s.getBytes(StandardCharsets.UTF_8); }

    public static void main(String[] args) {
        Clock clock = new TickClock(1_700_000_000_000L);
        LogConfig config = new LogConfig(160, 64, 400);
        InMemoryStorage storage = new InMemoryStorage();
        Topic topic = new Topic("payments.authorized", 3, config, storage);

        System.out.println("== Producing 12 keyed records into 3 partitions ==");
        String[] merchants = {"m-coffee", "m-books", "m-grocer", "m-fuel"};
        for (int i = 0; i < 12; i++) {
            String key = merchants[i % merchants.length];
            String value = "auth-" + (1000 + i) + " 19.99 USD";
            RecordMetadata meta = topic.append(utf8(key), utf8(value), clock.now());
            System.out.printf("%-9s -> partition %d offset %d%n", key, meta.partition(), meta.offset());
        }

        System.out.println("\n== Partition layout after segment rolls (segmentBytes=160) ==");
        for (int p = 0; p < topic.partitionCount(); p++) {
            Partition part = topic.partition(p);
            System.out.printf("partition %d: logStart=%d logEnd=%d segments(base offsets)=%s bytes=%d%n",
                    p, part.logStartOffset(), part.logEndOffset(), part.segmentBaseOffsets(), part.sizeBytes());
        }

        Partition p0 = topic.partition(0);
        System.out.println("\n== Reading partition 0 from offset 1, three at a time ==");
        OffsetStore offsets = new OffsetStore();
        Consumer consumer = new Consumer(p0, "ledger-poster", offsets);
        consumer.seek(1);
        List<StoredRecord> batch;
        while (!(batch = consumer.poll(3)).isEmpty()) {
            for (StoredRecord r : batch) {
                System.out.printf("offset %d ts %d key %s value %s%n", r.offset(), r.timestamp(), r.keyString(), r.valueString());
            }
            System.out.println("  position is now " + consumer.position());
        }

        System.out.println("\n== Commit, crash, resume (partition 1, six records) ==");
        Partition p1 = topic.partition(1);
        Consumer worker = new Consumer(p1, "ledger-poster", offsets);
        System.out.println("fresh consumer with no commit starts at logStart: position " + worker.position());
        worker.poll(2);
        worker.commit();
        System.out.println("processed 2 and committed: committed offset = " + offsets.committed("ledger-poster", 1));
        worker.poll(2);
        System.out.println("processed 2 more without committing: position " + worker.position() + " (then the process dies)");
        Consumer restarted = new Consumer(p1, "ledger-poster", offsets);
        System.out.println("restart resumes at the committed offset: position " + restarted.position()
                + " -> offsets 2 and 3 are delivered again (at-least-once)");

        System.out.println("\n== Retention: keep at most 400 bytes per partition, whole segments only ==");
        Consumer archiver = new Consumer(p0, "archiver", offsets);
        archiver.poll(2);
        archiver.commit();
        for (int i = 0; i < 20; i++) p0.append(utf8("m-coffee"), utf8("auth-" + (2000 + i) + " 5.00 USD"), clock.now());
        System.out.printf("before: segments=%s bytes=%d%n", p0.segmentBaseOffsets(), p0.sizeBytes());
        int dropped = p0.enforceRetention();
        System.out.printf("after : dropped %d segment(s), segments=%s bytes=%d logStart=%d%n",
                dropped, p0.segmentBaseOffsets(), p0.sizeBytes(), p0.logStartOffset());
        try {
            p0.read(0, 1);
        } catch (OffsetOutOfRangeException e) {
            System.out.println("read(0) rejected: " + e.getMessage());
        }
        StoredRecord first = p0.read(p0.logStartOffset(), 1).get(0);
        System.out.println("earliest readable record: offset " + first.offset() + " " + first.valueString());
        Consumer lagging = new Consumer(p0, "archiver", offsets);
        System.out.println("archiver committed " + offsets.committed("archiver", 0) + ", which retention retired: it resets to logStart, position " + lagging.position());

        System.out.println("\n== The frame on the wire ==");
        byte[] frame = RecordCodec.encode(7, 1_700_000_000_000L, utf8("m-fuel"), utf8("ok"));
        StringBuilder hex = new StringBuilder();
        for (byte b : frame) hex.append(String.format("%02x", b));
        System.out.println(frame.length + " bytes: " + hex);
    }
}
