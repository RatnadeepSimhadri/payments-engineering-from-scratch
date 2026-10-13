// Tests for phases/03-systems-data-structures/05-lsm-trees. Run: javac -d .out *.java && java -cp .out MainTest
import java.util.ArrayList;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.NavigableMap;
import java.util.Random;
import java.util.TreeMap;

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

    public static void main(String[] args) {
        // ----- memtable -----
        Memtable mem = new Memtable();
        mem.put("b", Entry.value("2", 1));
        mem.put("a", Entry.value("1", 2));
        mem.put("b", Entry.value("22", 3));
        check("memtable keeps keys sorted", new ArrayList<>(mem.entries().keySet()).equals(List.of("a", "b")));
        check("memtable overwrite keeps latest and size", mem.size() == 2 && mem.get("b").value().equals("22") && mem.get("b").seq() == 3);

        // ----- SSTable: encode, decode, bloom, sparse index -----
        SimulatedDisk disk = new SimulatedDisk();
        TreeMap<String, Entry> run = new TreeMap<>();
        for (int i = 0; i < 100; i++) run.put(String.format("k%03d", i), Entry.value("v" + i, i));
        run.put("k050", Entry.tombstone(500));
        SSTable sst = SSTable.write(disk, "t0.db", run);
        check("sstable records count, range, and block count", sst.entryCount == 100 && sst.minKey.equals("k000") && sst.maxKey.equals("k099") && sst.blockCount() == 7);
        check("sstable get decodes a value", sst.get("k007").value().equals("v7") && sst.get("k007").seq() == 7);
        check("sstable get decodes a tombstone", sst.get("k050").tombstone() && sst.get("k050").seq() == 500);
        check("sstable get on last key in last block", sst.get("k099").value().equals("v99"));
        long blocksBefore = sst.blockReads;
        check("sstable get outside range is null without I/O", sst.get("z") == null && sst.get("a") == null && sst.blockReads == blocksBefore && sst.rangeNegatives == 2);
        check("blockFor picks the block whose first key <= key", sst.blockFor("k000") == 0 && sst.blockFor("k015") == 0 && sst.blockFor("k016") == 1 && sst.blockFor("k099") == 6);
        long negatives = 0, falsePositives = 0;
        for (int i = 0; i < 2_000; i++) {
            String missing = String.format("k%03dz", i % 100);   // inside range, never written
            long before = sst.bloomNegatives;
            Entry e = sst.get(missing);
            if (sst.bloomNegatives > before) negatives++; else if (e == null) falsePositives++;
        }
        check("bloom filter rejects most absent keys without reading a block", negatives > 1_900);
        check("bloom false positives are rare (~1%)", falsePositives < 100);
        check("readAll round-trips every entry", sst.readAll().equals(run));
        checkThrows("sstable files are immutable", IllegalStateException.class, () -> disk.write("t0.db", new byte[1]));

        // ----- LSM: put/get/delete through flushes -----
        SimulatedDisk d2 = new SimulatedDisk();
        LsmTree lsm = new LsmTree(d2, 10, 3);
        for (int i = 0; i < 25; i++) lsm.put("key" + i, "v" + i);
        check("25 puts with limit 10 flush twice and leave 5 in the memtable", lsm.flushes == 2 && lsm.memtableSize() == 5);
        check("get finds keys in memtable and in tables", lsm.get("key24").equals("v24") && lsm.get("key3").equals("v3") && lsm.get("key12").equals("v12"));
        check("get of an unknown key is null", lsm.get("nope") == null);
        lsm.put("key3", "new3");
        check("newer write in memtable shadows older table value", lsm.get("key3").equals("new3"));
        lsm.delete("key12");
        check("tombstone in memtable hides table value", lsm.get("key12") == null);
        lsm.flush();
        check("tombstone persists across flush", lsm.get("key12") == null && lsm.get("key3").equals("new3"));
        checkThrows("null value is not a delete", IllegalArgumentException.class, () -> lsm.put("k", null));

        // ----- size-tiered compaction -----
        SimulatedDisk d3 = new SimulatedDisk();
        LsmTree tiered = new LsmTree(d3, 100, 4);
        Random rng = new Random(5);
        Map<String, String> model = new HashMap<>();
        for (int i = 0; i < 5_000; i++) {
            String k = "acct_" + rng.nextInt(800);
            if (rng.nextInt(10) == 0) { tiered.delete(k); model.remove(k); }
            else { String v = "bal_" + i; tiered.put(k, v); model.put(k, v); }
        }
        tiered.flush();
        check("compaction ran and no tier is full afterwards", tiered.compactions > 0 && tiered.checkInvariants());
        check("fewer tables than flushes after compaction", tiered.tableCount() < tiered.flushes);
        boolean agrees = true;
        for (int i = 0; i < 800; i++) {
            String k = "acct_" + i;
            String expected = model.get(k);
            String actual = tiered.get(k);
            if (expected == null ? actual != null : !expected.equals(actual)) { agrees = false; break; }
        }
        check("every key agrees with the model after flushes and compactions (highest seq wins)", agrees);
        NavigableMap<String, String> scanned = tiered.range("acct_10", "acct_19");
        TreeMap<String, String> expectedScan = new TreeMap<>();
        for (Map.Entry<String, String> e : model.entrySet()) if (e.getKey().compareTo("acct_10") >= 0 && e.getKey().compareTo("acct_19") <= 0) expectedScan.put(e.getKey(), e.getValue());
        check("range scan merges levels and hides tombstones", scanned.equals(expectedScan));
        check("write amplification is above 1 (compaction rewrites data)", tiered.writeAmplification() > 1.0);
        check("disk holds exactly the live tables (inputs deleted)", d3.fileCount() == tiered.tableCount() && d3.filesDeleted > 0);

        // ----- recency across tables of different ages: the size-tiered trap -----
        SimulatedDisk d4 = new SimulatedDisk();
        LsmTree trap = new LsmTree(d4, 3, 2);
        trap.put("x", "old"); trap.put("p", "1"); trap.put("o", "1");     // table A (3 entries)
        trap.put("q", "1"); trap.put("r", "1"); trap.put("s", "1");       // table B (3) -> same tier -> AB (6)
        check("two same-size tables compacted into one", trap.tableCount() == 1 && trap.get("x").equals("old") && trap.tables().get(0).entryCount == 6);
        trap.put("x", "new"); trap.put("y", "1"); trap.put("z", "1");     // table C (3): 6 is not < 2*3, so AB stays apart
        check("tables of different sizes are left in separate tiers", trap.tableCount() == 2 && trap.checkInvariants());
        check("newer value in the small table beats older value in the big table", trap.get("x").equals("new"));
        trap.put("x", "newest"); trap.put("u", "1"); trap.put("v", "1");  // table D (3) -> CD (5) -> joins AB's tier -> one table
        check("cascading compaction keeps the highest-seq value", trap.tableCount() == 1 && trap.get("x").equals("newest") && trap.tables().get(0).entryCount == 10 && trap.checkInvariants());

        // ----- tombstone lifecycle -----
        SimulatedDisk d5 = new SimulatedDisk();
        LsmTree tomb = new LsmTree(d5, 2, 2);
        tomb.put("a", "1"); tomb.put("b", "2");                      // table 1
        tomb.delete("a"); tomb.put("c", "3");                        // table 2 -> compaction of everything
        check("full compaction drops the tombstone and the deleted key", tomb.tableCount() == 1 && tomb.get("a") == null && tomb.tables().get(0).entryCount == 2);
        SimulatedDisk d6 = new SimulatedDisk();
        LsmTree keep = new LsmTree(d6, 2, 3);
        keep.put("a", "1"); keep.put("b", "2");                      // table 1
        keep.put("c", "3"); keep.put("d", "4");                      // table 2
        keep.put("e", "5"); keep.put("f", "6");                      // table 3 -> compaction (all three)
        keep.put("g", "7"); keep.put("h", "8");                      // table 4 (2 entries); the merged one has 6
        keep.delete("a"); keep.put("i", "9");                        // table 5 (2 entries) -> tier of two 2-entry tables, threshold 3: no compaction
        check("partial state: tombstone lives in a small table while value lives in the big one", keep.tableCount() == 3 && keep.get("a") == null);
        keep.put("j", "1"); keep.put("k", "1");                      // third small table -> compaction of the three small ones, not the big one
        boolean tombstoneKept = false;
        for (SSTable t : keep.tables()) { Entry e = t.get("a"); if (e != null && e.tombstone()) tombstoneKept = true; }
        check("tombstone survives a compaction that excludes the table holding the old value", tombstoneKept && keep.get("a") == null);

        // ----- limits -----
        checkThrows("memtable limit must be positive", IllegalArgumentException.class, () -> new LsmTree(new SimulatedDisk(), 0, 4));
        checkThrows("tier threshold must be at least 2", IllegalArgumentException.class, () -> new LsmTree(new SimulatedDisk(), 10, 1));

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }
}
