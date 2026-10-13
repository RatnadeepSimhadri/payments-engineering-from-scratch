// Lesson: phases/03-systems-data-structures/05-lsm-trees/docs/en.md
// LSM Trees: Memtables, SSTables, and Compaction
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: O'Neil, Cheng, Gawlick, O'Neil (1996), "The Log-Structured Merge-Tree"; Chang et al. (2006), "Bigtable";
//          RocksDB wiki (Compaction, Bloom Filter); Apache Cassandra docs (SizeTieredCompactionStrategy). Bloom (1970).
// Java 21, standard library only.

import java.io.ByteArrayOutputStream;
import java.io.DataInputStream;
import java.io.DataOutputStream;
import java.io.IOException;
import java.io.ByteArrayInputStream;
import java.nio.charset.StandardCharsets;
import java.util.ArrayList;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.NavigableMap;
import java.util.PriorityQueue;
import java.util.Random;
import java.util.TreeMap;

/** A value or a tombstone, stamped with the sequence number of the write that produced it. Higher seq wins. */
record Entry(String value, boolean tombstone, long seq) {
    static Entry value(String v, long seq) { return new Entry(v, false, seq); }
    static Entry tombstone(long seq) { return new Entry(null, true, seq); }
}

/** FNV-1a and a 64-bit mixer, as in Lessons 01 and 02. */
final class Hashing {
    private Hashing() {}

    static long fnv1a64(String key) {
        long h = 0xcbf29ce484222325L;
        for (byte b : key.getBytes(StandardCharsets.UTF_8)) { h ^= (b & 0xff); h *= 0x100000001b3L; }
        return h;
    }

    static long mix64(long h) {
        h ^= h >>> 33; h *= 0xff51afd7ed558ccdL;
        h ^= h >>> 33; h *= 0xc4ceb9fe1a85ec53L;
        h ^= h >>> 33;
        return h;
    }
}

/** Bloom filter sized for ~1% false positives: 10 bits per key, 7 hashes, double hashing. */
final class BloomFilter {
    private final long[] words;
    private final long bits;
    private final int hashes;

    BloomFilter(int expectedKeys) {
        this.bits = Math.max(64, (long) expectedKeys * 10);
        this.words = new long[(int) ((bits + 63) / 64)];
        this.hashes = 7;
    }

    private long index(long h1, long h2, int i) { return Long.remainderUnsigned(h1 + i * h2, bits); }

    void add(String key) {
        long h1 = Hashing.mix64(Hashing.fnv1a64(key)), h2 = Hashing.mix64(h1);
        for (int i = 0; i < hashes; i++) { long b = index(h1, h2, i); words[(int) (b >>> 6)] |= 1L << (b & 63); }
    }

    boolean mightContain(String key) {
        long h1 = Hashing.mix64(Hashing.fnv1a64(key)), h2 = Hashing.mix64(h1);
        for (int i = 0; i < hashes; i++) { long b = index(h1, h2, i); if ((words[(int) (b >>> 6)] & (1L << (b & 63))) == 0) return false; }
        return true;
    }
}

/** A directory of immutable byte-array files with I/O counters. Stands in for the file system. */
final class SimulatedDisk {
    private final Map<String, byte[]> files = new LinkedHashMap<>();
    long bytesWritten, bytesRead, filesWritten, filesDeleted;

    void write(String name, byte[] data) {
        if (files.containsKey(name)) throw new IllegalStateException("files are immutable: " + name);
        files.put(name, data.clone());
        bytesWritten += data.length;
        filesWritten++;
    }

    byte[] read(String name) {
        byte[] data = files.get(name);
        if (data == null) throw new IllegalStateException("no such file: " + name);
        bytesRead += data.length;
        return data.clone();
    }

    void delete(String name) { if (files.remove(name) != null) filesDeleted++; }

    int fileCount() { return files.size(); }

    long totalBytes() { long t = 0; for (byte[] f : files.values()) t += f.length; return t; }
}

/** The in-memory write buffer: a sorted map, so a flush is a sequential scan that produces a sorted run. */
final class Memtable {
    private final TreeMap<String, Entry> map = new TreeMap<>();
    private long approximateBytes;

    void put(String key, Entry entry) {
        Entry old = map.put(key, entry);
        approximateBytes += key.length() + (entry.value() == null ? 0 : entry.value().length()) + 16;
        if (old != null) approximateBytes -= key.length() + (old.value() == null ? 0 : old.value().length()) + 16;
    }

    Entry get(String key) { return map.get(key); }

    int size() { return map.size(); }

    long approximateBytes() { return approximateBytes; }

    boolean isEmpty() { return map.isEmpty(); }

    NavigableMap<String, Entry> entries() { return map; }
}

/**
 * Sorted String Table: an immutable sorted run on disk with a Bloom filter and a sparse index in memory.
 * Lookup: Bloom says no -> skip the file; else binary search the sparse index, then scan one block.
 */
final class SSTable {
    static final int BLOCK_ENTRIES = 16;

    final String fileName;
    final int entryCount;
    final long bytes;
    final String minKey, maxKey;
    private final BloomFilter bloom;
    private final List<String> sparseKeys = new ArrayList<>();   // first key of every block
    private final List<Integer> sparseOffsets = new ArrayList<>(); // byte offset of every block
    private final SimulatedDisk disk;
    long bloomNegatives, rangeNegatives, blockReads;

    private SSTable(String fileName, SimulatedDisk disk, int entryCount, long bytes, String minKey, String maxKey, BloomFilter bloom) {
        this.fileName = fileName; this.disk = disk; this.entryCount = entryCount; this.bytes = bytes;
        this.minKey = minKey; this.maxKey = maxKey; this.bloom = bloom;
    }

    /** Write a sorted sequence of entries as one file and return the handle. */
    static SSTable write(SimulatedDisk disk, String fileName, NavigableMap<String, Entry> sorted) {
        try {
            ByteArrayOutputStream bytes = new ByteArrayOutputStream();
            DataOutputStream out = new DataOutputStream(bytes);
            BloomFilter bloom = new BloomFilter(sorted.size());
            List<String> sparseKeys = new ArrayList<>();
            List<Integer> sparseOffsets = new ArrayList<>();
            out.writeInt(sorted.size());
            int i = 0;
            for (Map.Entry<String, Entry> e : sorted.entrySet()) {
                if (i % BLOCK_ENTRIES == 0) { sparseKeys.add(e.getKey()); sparseOffsets.add(out.size()); }
                out.writeUTF(e.getKey());
                out.writeLong(e.getValue().seq());
                out.writeBoolean(e.getValue().tombstone());
                out.writeUTF(e.getValue().tombstone() ? "" : e.getValue().value());
                bloom.add(e.getKey());
                i++;
            }
            out.flush();
            byte[] data = bytes.toByteArray();
            disk.write(fileName, data);
            SSTable table = new SSTable(fileName, disk, sorted.size(), data.length,
                    sorted.isEmpty() ? null : sorted.firstKey(), sorted.isEmpty() ? null : sorted.lastKey(), bloom);
            table.sparseKeys.addAll(sparseKeys);
            table.sparseOffsets.addAll(sparseOffsets);
            return table;
        } catch (IOException e) {
            throw new IllegalStateException(e);
        }
    }

    /** Point lookup: Bloom, then key range, then one block. Returns null when the key is not in this table. */
    Entry get(String key) {
        if (minKey == null || key.compareTo(minKey) < 0 || key.compareTo(maxKey) > 0) { rangeNegatives++; return null; }
        if (!bloom.mightContain(key)) { bloomNegatives++; return null; }
        int block = blockFor(key);
        for (Map.Entry<String, Entry> e : readBlock(block).entrySet()) {
            int c = e.getKey().compareTo(key);
            if (c == 0) return e.getValue();
            if (c > 0) break;
        }
        return null;
    }

    /** Index of the last sparse key <= key (binary search over the in-memory index). */
    int blockFor(String key) {
        int lo = 0, hi = sparseKeys.size() - 1;
        while (lo < hi) {
            int mid = (lo + hi + 1) >>> 1;
            if (sparseKeys.get(mid).compareTo(key) <= 0) lo = mid; else hi = mid - 1;
        }
        return lo;
    }

    /** Decode one block of up to BLOCK_ENTRIES entries starting at the block's offset. */
    NavigableMap<String, Entry> readBlock(int block) {
        blockReads++;
        byte[] data = disk.read(fileName);
        try {
            DataInputStream in = new DataInputStream(new ByteArrayInputStream(data));
            in.skipBytes(sparseOffsets.get(block));
            int remaining = Math.min(BLOCK_ENTRIES, entryCount - block * BLOCK_ENTRIES);
            TreeMap<String, Entry> out = new TreeMap<>();
            for (int i = 0; i < remaining; i++) {
                String k = in.readUTF();
                long seq = in.readLong();
                boolean tomb = in.readBoolean();
                String v = in.readUTF();
                out.put(k, tomb ? Entry.tombstone(seq) : Entry.value(v, seq));
            }
            return out;
        } catch (IOException e) {
            throw new IllegalStateException(e);
        }
    }

    /** Every entry, in key order. Used by compaction and range scans. */
    NavigableMap<String, Entry> readAll() {
        TreeMap<String, Entry> out = new TreeMap<>();
        for (int b = 0; b < sparseKeys.size(); b++) out.putAll(readBlock(b));
        return out;
    }

    int blockCount() { return sparseKeys.size(); }
}

/**
 * The LSM tree: one mutable memtable, a list of immutable SSTables newest-first, and size-tiered compaction.
 * Writes are O(log n) in memory and sequential on disk; reads consult the memtable then each table until a hit.
 */
final class LsmTree {
    final SimulatedDisk disk;
    final int memtableLimit;        // entries before a flush
    final int tierThreshold;        // tables of similar size that trigger a compaction
    private Memtable memtable = new Memtable();
    private final List<SSTable> tables = new ArrayList<>();   // every on-disk run; seq numbers, not position, decide recency
    private long seq, nextFileId;
    long flushes, compactions, bytesCompacted, userBytesWritten, tablesConsulted, gets;

    LsmTree(SimulatedDisk disk, int memtableLimit, int tierThreshold) {
        if (memtableLimit <= 0 || tierThreshold < 2) throw new IllegalArgumentException("bad limits");
        this.disk = disk; this.memtableLimit = memtableLimit; this.tierThreshold = tierThreshold;
    }

    void put(String key, String value) {
        if (value == null) throw new IllegalArgumentException("use delete for tombstones");
        userBytesWritten += key.length() + value.length();
        memtable.put(key, Entry.value(value, ++seq));
        maybeFlush();
    }

    void delete(String key) {
        userBytesWritten += key.length();
        memtable.put(key, Entry.tombstone(++seq));      // a delete is a write
        maybeFlush();
    }

    private void maybeFlush() { if (memtable.size() >= memtableLimit) flush(); }

    /** Freeze the memtable, write it as a new SSTable, start an empty memtable, then check for compaction. */
    void flush() {
        if (memtable.isEmpty()) return;
        SSTable t = SSTable.write(disk, "sst-" + (nextFileId++) + ".db", memtable.entries());
        tables.add(t);
        memtable = new Memtable();
        flushes++;
        maybeCompact();
    }

    /**
     * Read path: the memtable answers outright. Otherwise every table whose Bloom filter and key range admit
     * the key is consulted and the entry with the highest seq wins. Size-tiered compaction merges tables of
     * similar size regardless of age, so file order alone cannot say which copy is newest; seq can.
     */
    String get(String key) {
        gets++;
        Entry e = memtable.get(key);
        if (e != null) return e.tombstone() ? null : e.value();
        Entry best = null;
        for (SSTable t : tables) {
            tablesConsulted++;
            Entry found = t.get(key);
            if (found != null && (best == null || found.seq() > best.seq())) best = found;
        }
        return best == null || best.tombstone() ? null : best.value();
    }

    /**
     * Size-tiered: group tables whose entry counts are less than twice the smallest in the group.
     * When any tier has tierThreshold tables, merge them into one. Repeat until no tier is full.
     */
    void maybeCompact() {
        while (true) {
            List<SSTable> tier = fullTier();
            if (tier == null) return;
            compact(tier);
        }
    }

    private List<SSTable> fullTier() {
        List<SSTable> sorted = new ArrayList<>(tables);
        sorted.sort((a, b) -> Integer.compare(a.entryCount, b.entryCount));
        for (int i = 0; i < sorted.size(); i++) {
            List<SSTable> tier = new ArrayList<>();
            int base = Math.max(1, sorted.get(i).entryCount);
            for (int j = i; j < sorted.size() && sorted.get(j).entryCount < 2L * base; j++) tier.add(sorted.get(j));
            if (tier.size() >= tierThreshold) return tier;
        }
        return null;
    }

    /**
     * k-way merge of the chosen tables. For each key the highest seq wins. A tombstone can be dropped only
     * when no table outside this compaction could still hold an older value for its key; the simple safe
     * rule used here is: drop tombstones only when every table is being compacted.
     */
    void compact(List<SSTable> chosen) {
        boolean everything = chosen.size() == tables.size();
        record Cursor(Map.Entry<String, Entry> head, java.util.Iterator<Map.Entry<String, Entry>> rest) {}
        PriorityQueue<Cursor> heap = new PriorityQueue<>((a, b) -> {
            int c = a.head.getKey().compareTo(b.head.getKey());
            return c != 0 ? c : Long.compare(b.head.getValue().seq(), a.head.getValue().seq());   // newest first on ties
        });
        for (SSTable t : chosen) {
            bytesCompacted += t.bytes;
            var it = t.readAll().entrySet().iterator();
            if (it.hasNext()) heap.add(new Cursor(it.next(), it));
        }
        TreeMap<String, Entry> merged = new TreeMap<>();
        String lastKey = null;
        while (!heap.isEmpty()) {
            Cursor c = heap.poll();
            String k = c.head.getKey();
            if (!k.equals(lastKey)) {                              // first occurrence of k is the newest
                lastKey = k;
                Entry e = c.head.getValue();
                if (!(e.tombstone() && everything)) merged.put(k, e);
            }
            if (c.rest.hasNext()) heap.add(new Cursor(c.rest.next(), c.rest));
        }
        for (SSTable t : chosen) { tables.remove(t); disk.delete(t.fileName); }
        if (!merged.isEmpty()) tables.add(SSTable.write(disk, "sst-" + (nextFileId++) + ".db", merged));
        compactions++;
    }

    /** Keys in [lo, hi] with their live values: merge of memtable and every table, highest seq wins, tombstones hidden. */
    NavigableMap<String, String> range(String lo, String hi) {
        TreeMap<String, Entry> merged = new TreeMap<>();
        for (SSTable t : tables) {
            for (Map.Entry<String, Entry> e : t.readAll().subMap(lo, true, hi, true).entrySet()) merged.merge(e.getKey(), e.getValue(), (a, b) -> a.seq() >= b.seq() ? a : b);
        }
        for (Map.Entry<String, Entry> e : memtable.entries().subMap(lo, true, hi, true).entrySet()) merged.merge(e.getKey(), e.getValue(), (a, b) -> a.seq() >= b.seq() ? a : b);
        TreeMap<String, String> out = new TreeMap<>();
        for (Map.Entry<String, Entry> e : merged.entrySet()) if (!e.getValue().tombstone()) out.put(e.getKey(), e.getValue().value());
        return out;
    }

    int tableCount() { return tables.size(); }
    List<SSTable> tables() { return List.copyOf(tables); }
    int memtableSize() { return memtable.size(); }

    /** Bytes written to disk divided by bytes the user wrote: the cost of compaction. */
    double writeAmplification() { return userBytesWritten == 0 ? 0 : (double) disk.bytesWritten / userBytesWritten; }

    long bloomNegatives() { long n = 0; for (SSTable t : tables) n += t.bloomNegatives; return n; }
    long rangeNegatives() { long n = 0; for (SSTable t : tables) n += t.rangeNegatives; return n; }
    long blockReads() { long n = 0; for (SSTable t : tables) n += t.blockReads; return n; }

    /** No empty tables; no tier at or above the threshold after compaction; disk holds exactly the live tables. */
    boolean checkInvariants() {
        for (SSTable t : tables) if (t.entryCount == 0) throw new IllegalStateException("empty table " + t.fileName);
        if (fullTier() != null) throw new IllegalStateException("a tier is still full after compaction");
        if (disk.fileCount() != tables.size()) throw new IllegalStateException("disk files " + disk.fileCount() + " != tables " + tables.size());
        return true;
    }
}

public final class Main {

    public static void main(String[] args) {
        Random rng = new Random(42);
        SimulatedDisk disk = new SimulatedDisk();
        LsmTree lsm = new LsmTree(disk, 1_000, 4);

        System.out.println("== Payment status store: 20,000 payments, each updated three times ==");
        String[] states = {"created", "authorized", "captured"};
        for (String state : states) {
            for (int i = 0; i < 20_000; i++) lsm.put(String.format("pay_%06d", i), state + "@" + rng.nextInt(1_000_000));
        }
        System.out.printf("flushes=%d compactions=%d tables=%d memtable=%d entries%n",
                lsm.flushes, lsm.compactions, lsm.tableCount(), lsm.memtableSize());
        System.out.printf("disk: %d files, %,d bytes; user wrote %,d bytes; write amplification %.2fx%n",
                disk.fileCount(), disk.totalBytes(), lsm.userBytesWritten, lsm.writeAmplification());
        System.out.println("table sizes (entries): " + lsm.tables().stream().map(t -> t.entryCount).toList());

        System.out.println();
        System.out.println("== Read path: highest seq wins, Bloom filters skip the rest ==");
        String v = lsm.get("pay_000042");
        System.out.println("pay_000042 -> " + v + "  (the third write; earlier versions are shadowed)");
        long before = lsm.tablesConsulted, blocksBefore = lsm.blockReads();
        for (int i = 0; i < 1_000; i++) lsm.get(String.format("pay_%06dx", i));   // never written
        System.out.printf("1000 misses consulted %d tables: %d rejected by key range, %d by Bloom, %d block reads (false positives)%n",
                lsm.tablesConsulted - before, lsm.rangeNegatives(), lsm.bloomNegatives(), lsm.blockReads() - blocksBefore);

        System.out.println();
        System.out.println("== Deletes are writes: tombstones ==");
        lsm.delete("pay_000042");
        System.out.println("after delete, pay_000042 -> " + lsm.get("pay_000042") + " (tombstone in the memtable shadows the table)");
        lsm.flush();
        System.out.println("after flush, pay_000042 -> " + lsm.get("pay_000042") + " (tombstone now in the newest table)");

        System.out.println();
        System.out.println("== Range scan merges every level ==");
        System.out.println("pay_000040..pay_000044 -> " + lsm.range("pay_000040", "pay_000044").keySet() + "  (42 is gone)");

        System.out.println();
        System.out.println("== Forcing compaction down to one table drops the tombstone ==");
        SimulatedDisk disk2 = new SimulatedDisk();
        LsmTree small = new LsmTree(disk2, 4, 2);
        small.put("a", "1"); small.put("b", "2"); small.put("c", "3"); small.put("d", "4");          // flush 1
        small.delete("b"); small.put("e", "5"); small.put("f", "6"); small.put("g", "7");            // flush 2 -> compaction
        System.out.printf("tables=%d, keys=%s, b -> %s%n", small.tableCount(), small.range("a", "z").keySet(), small.get("b"));
        System.out.printf("files written=%d deleted=%d (compaction rewrote, then removed the inputs)%n", disk2.filesWritten, disk2.filesDeleted);
    }
}
