// Tests for phases/08-messaging-and-streams/01-the-append-only-log. Run: javac -d .out *.java && java -cp .out MainTest
import java.nio.charset.StandardCharsets;
import java.util.ArrayList;
import java.util.Arrays;
import java.util.HashMap;
import java.util.List;
import java.util.Map;

public final class MainTest {
    private static int passed = 0;
    private static final List<String> failures = new ArrayList<>();

    static void check(String name, boolean condition) {
        if (condition) passed++; else failures.add(name);
    }

    static void checkThrows(String name, Class<? extends Throwable> type, Runnable action) {
        try {
            action.run();
            failures.add(name + " (no exception)");
        } catch (Throwable t) {
            if (type.isInstance(t)) passed++; else failures.add(name + " (wrong exception " + t.getClass().getSimpleName() + ")");
        }
    }

    static byte[] b(String s) { return s == null ? null : s.getBytes(StandardCharsets.UTF_8); }

    static Partition partition(int id, LogConfig config) { return new Partition(id, config, new InMemoryStorage()); }

    public static void main(String[] args) {
        // codec round trip and frame layout
        byte[] frame = RecordCodec.encode(42, 1234, b("k"), b("value"));
        check("frame size = 4 + 24 + key + value", frame.length == 4 + 24 + 1 + 5 && RecordCodec.frameSize(b("k"), b("value")) == frame.length);
        StoredRecord decoded = RecordCodec.decode(frame, 0);
        check("decode offset", decoded.offset() == 42);
        check("decode timestamp", decoded.timestamp() == 1234);
        check("decode key and value", decoded.keyString().equals("k") && decoded.valueString().equals("value"));
        byte[] nullKey = RecordCodec.encode(1, 1, null, b("v"));
        check("null key encodes as -1 and decodes as null", RecordCodec.decode(nullKey, 0).key() == null && nullKey.length == 4 + 24 + 1);
        check("frameLengthAt matches encoded length", RecordCodec.frameLengthAt(frame, 0) == frame.length);
        check("fnv1a32 of empty is offset basis", RecordCodec.fnv1a32(new byte[0]) == 0x811C9DC5);
        check("fnv1a32('a') known vector", RecordCodec.fnv1a32(b("a")) == 0xE40C292C);

        // offsets are monotonic and contiguous within a partition
        LogConfig big = new LogConfig(1 << 20, 4096, 1 << 30);
        Partition p = partition(0, big);
        long previous = -1;
        boolean contiguous = true;
        for (int i = 0; i < 50; i++) {
            long off = p.append(b("k" + (i % 3)), b("v" + i), 1000 + i);
            if (off != previous + 1) contiguous = false;
            previous = off;
        }
        check("offsets start at 0 and increase by exactly one", contiguous && previous == 49);
        check("logEndOffset is next offset to assign", p.logEndOffset() == 50 && p.logStartOffset() == 0);
        check("single large segment, no roll", p.segmentCount() == 1);

        // read from an arbitrary offset, bounded by maxRecords, empty at the end
        List<StoredRecord> mid = p.read(17, 5);
        check("read(17, 5) returns offsets 17..21", mid.size() == 5 && mid.get(0).offset() == 17 && mid.get(4).offset() == 21);
        check("read returns the stored value", mid.get(0).valueString().equals("v17") && mid.get(0).timestamp() == 1017);
        check("read at logEndOffset is empty", p.read(50, 10).isEmpty());
        checkThrows("read above logEndOffset is out of range", OffsetOutOfRangeException.class, () -> p.read(51, 1));
        checkThrows("read below logStartOffset is out of range", OffsetOutOfRangeException.class, () -> p.read(-1, 1));

        // sparse index: far fewer entries than records, and every record still addressable
        Segment seg = new Segment(100, new InMemoryStorage().create(0, 100), 1 << 20, 100);
        for (int i = 0; i < 40; i++) seg.append(b("key"), b("payload-" + i), i);
        check("sparse index has fewer entries than records", seg.indexEntries().size() < 40 && seg.indexEntries().size() >= 2);
        check("first index entry is relative offset 0 at position 0", seg.indexEntries().get(0).equals(new IndexEntry(0, 0)));
        boolean everyOffsetReadable = true;
        for (long off = 100; off < 140; off++) {
            List<StoredRecord> one = seg.read(off, 1);
            if (one.size() != 1 || one.get(0).offset() != off || !one.get(0).valueString().equals("payload-" + (off - 100))) everyOffsetReadable = false;
        }
        check("every offset is found through the sparse index", everyOffsetReadable);

        // segment roll: small segments, base offsets line up with record counts
        LogConfig small = new LogConfig(120, 40, 10_000);
        Partition rolled = partition(1, small);
        for (int i = 0; i < 10; i++) rolled.append(b("k"), b("0123456789"), i); // frame = 4+24+1+10 = 39 bytes; 3 per segment
        check("segments roll at the size limit", rolled.segmentCount() == 4);
        check("segment base offsets are 0,3,6,9", rolled.segmentBaseOffsets().equals(List.of(0L, 3L, 6L, 9L)));
        List<StoredRecord> across = rolled.read(2, 4);
        check("read crosses segment boundaries", across.size() == 4 && across.get(0).offset() == 2 && across.get(3).offset() == 5);
        check("read into the last partial segment", rolled.read(9, 10).size() == 1);
        Partition oversized = partition(2, new LogConfig(50, 10, 10_000));
        long bigOffset = oversized.append(b("k"), b("this value is much longer than the fifty byte segment limit"), 1);
        check("an oversized record still lands in an empty segment", bigOffset == 0 && oversized.read(0, 1).size() == 1);

        // storage interface: segments are created and deleted through it, never around it
        InMemoryStorage storage = new InMemoryStorage();
        Partition stored = new Partition(7, new LogConfig(120, 40, 250), storage);
        for (int i = 0; i < 10; i++) stored.append(b("k"), b("0123456789"), i);
        check("storage holds one entry per segment", storage.baseOffsets(7).equals(List.of(0L, 3L, 6L, 9L)));
        check("storage is keyed by partition", storage.baseOffsets(8).isEmpty());

        // retention drops whole segments from the oldest end and moves logStartOffset
        long bytesBefore = stored.sizeBytes();
        int dropped = stored.enforceRetention();
        check("retention removed oldest segments", dropped == 2 && stored.segmentCount() == 2 && stored.sizeBytes() < bytesBefore && stored.sizeBytes() <= 250);
        check("logStartOffset advanced to the surviving segment", stored.logStartOffset() == 6 && stored.logEndOffset() == 10);
        check("retention deleted the segments from storage", storage.baseOffsets(7).equals(List.of(6L, 9L)));
        checkThrows("reading a retired offset is out of range", OffsetOutOfRangeException.class, () -> stored.read(2, 1));
        check("surviving records are intact", stored.read(6, 1).get(0).offset() == 6);
        Partition tiny = partition(4, new LogConfig(50, 10, 10));
        tiny.append(b("k"), b("0123456789"), 1);
        tiny.append(b("k"), b("0123456789"), 2);
        tiny.enforceRetention();
        check("active segment is never dropped", tiny.segmentCount() == 1 && tiny.logEndOffset() == 2);

        // key-based partitioning: same key -> same partition, order preserved per key
        Topic topic = new Topic("t", 4, big, new InMemoryStorage());
        String[] keys = {"alice", "bob", "carol", "dave", "erin"};
        Map<String, List<Integer>> seen = new HashMap<>();
        boolean stable = true;
        for (int i = 0; i < 100; i++) {
            String k = keys[i % keys.length];
            RecordMetadata m = topic.append(b(k), b("v" + i), i);
            int expected = topic.partitionFor(b(k));
            if (m.partition() != expected) stable = false;
            seen.computeIfAbsent(k, x -> new ArrayList<>()).add(m.partition());
        }
        boolean oneHomeEachKey = seen.values().stream().allMatch(list -> list.stream().distinct().count() == 1);
        check("every key maps to exactly one partition", stable && oneHomeEachKey);
        int home = topic.partitionFor(b("alice"));
        List<StoredRecord> all = topic.partition(home).read(0, 1000);
        boolean ordered = true;
        int lastAlice = -1;
        for (StoredRecord rec : all) {
            if (rec.keyString().equals("alice")) {
                int n = Integer.parseInt(rec.valueString().substring(1));
                if (n <= lastAlice) ordered = false;
                lastAlice = n;
            }
        }
        check("records for one key are in production order within its partition", ordered && lastAlice == 95);
        check("partitionFor is a pure function of the key", topic.partitionFor(b("bob")) == topic.partitionFor(b("bob")));
        check("partitionFor is within range", topic.partitionFor(b("anything")) >= 0 && topic.partitionFor(b("anything")) < 4);
        Topic unkeyed = new Topic("u", 3, big, new InMemoryStorage());
        int[] rr = {unkeyed.partitionFor(null), unkeyed.partitionFor(null), unkeyed.partitionFor(null), unkeyed.partitionFor(null)};
        check("null keys rotate round-robin", Arrays.equals(rr, new int[]{0, 1, 2, 0}));

        // consumer position semantics
        OffsetStore offsets = new OffsetStore();
        Consumer c = new Consumer(p, "g1", offsets);
        check("a group with no commit starts at logStartOffset", c.position() == 0);
        List<StoredRecord> first = c.poll(10);
        check("poll advances position past the last delivered record", first.size() == 10 && c.position() == 10);
        c.seek(45);
        check("seek then poll reads from the new position and stops at the end", c.poll(10).size() == 5 && c.position() == 50);
        check("poll at the end is empty and does not move", c.poll(10).isEmpty() && c.position() == 50);
        checkThrows("seek outside the log is rejected", OffsetOutOfRangeException.class, () -> c.seek(99));

        // commit / resume: the committed value is the next offset to read, and uncommitted work is redelivered
        Consumer worker = new Consumer(p, "g2", offsets);
        worker.poll(10);
        worker.commit();
        check("commit stores the next offset to read", offsets.committed("g2", 0) == 10);
        worker.poll(5);
        check("polling past the commit does not move the committed offset", worker.position() == 15 && offsets.committed("g2", 0) == 10);
        Consumer resumed = new Consumer(p, "g2", offsets);
        check("a restarted consumer resumes at the committed offset, redelivering 10..14", resumed.position() == 10 && resumed.poll(5).get(0).offset() == 10);
        check("another group is unaffected", new Consumer(p, "g3", offsets).position() == 0);
        check("no commit for an unknown group", offsets.committed("nobody", 0) == OffsetStore.NONE);
        OffsetStore retired = new OffsetStore();
        retired.commit("g4", 7, 2);
        check("a committed offset retired by retention resets to logStartOffset", new Consumer(stored, "g4", retired).position() == 6);
        checkThrows("config rejects non-positive segment size", IllegalArgumentException.class, () -> new LogConfig(0, 1, 1));

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }
}
