// Tests for phases/07-storage-and-transactions/01-partitioning-and-rebalancing. Run: javac -d .out *.java && java -cp .out MainTest
import java.util.ArrayList;
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

    public static void main(String[] args) {
        List<String> keys = Main.paymentIds(20_000);

        // range partitioner: boundaries are half-open [lo, hi)
        RangePartitioner range = new RangePartitioner(List.of("f", "m", "s"));
        check("range: 4 partitions from 3 split points", range.partitionCount() == 4);
        check("range: 'acme' before f -> 0", range.partitionOf("acme") == 0);
        check("range: 'f' exactly on split -> 1 (half-open)", range.partitionOf("f") == 1);
        check("range: 'megamart' -> 2", range.partitionOf("megamart") == 2);
        check("range: 'zenith' -> 3", range.partitionOf("zenith") == 3);
        check("range: preserves order", range.partitionOf("alpha") <= range.partitionOf("omega"));
        checkThrows("range: rejects unsorted split points", IllegalArgumentException.class,
                () -> new RangePartitioner(List.of("m", "f")));

        // hash partitioner: deterministic, in range, roughly uniform
        HashPartitioner hash = new HashPartitioner(16);
        check("hash: deterministic", hash.partitionOf("pay_1") == hash.partitionOf("pay_1"));
        check("hash: fnv1a of empty string is offset basis", Partitioner.fnv1a64("") == 0xcbf29ce484222325L);
        boolean inRange = true;
        HotKeyDetector spread = new HotKeyDetector(16);
        for (String k : keys) {
            int p = hash.partitionOf(k);
            if (p < 0 || p >= 16) inRange = false;
            spread.record(p);
        }
        check("hash: every partition in [0,16)", inRange);
        check("hash: 20k keys over 16 partitions skew < 1.2x", spread.skew() < 1.2);
        checkThrows("hash: rejects zero partitions", IllegalArgumentException.class, () -> new HashPartitioner(0));

        // hot key detection and salting
        HotKeyDetector hot = new HotKeyDetector(16);
        for (int i = 0; i < 1000; i++) hot.record(hash.partitionOf("megamart"));
        check("hot key: one key sends everything to one partition, skew 16x", hot.skew() == 16.0);
        HotKeyDetector salted = new HotKeyDetector(16);
        for (int i = 0; i < 1000; i++) salted.record(hash.partitionOf(HotKeyDetector.salt("megamart", "pay_" + i, 8)));
        int touched = 0;
        for (int p = 0; p < 16; p++) if (salted.hits(p) > 0) touched++;
        check("hot key: salt x8 spreads over at least 4 partitions and cuts skew in half", touched >= 4 && salted.skew() < 8.0);
        check("salt: deterministic for the same secondary", HotKeyDetector.salt("m", "x", 8).equals(HotKeyDetector.salt("m", "x", 8)));
        check("salt: keeps the logical key as prefix", HotKeyDetector.salt("megamart", "pay_1", 8).startsWith("megamart#"));

        // routing table: versions, assignment, round robin
        RoutingTable table = new RoutingTable(new HashPartitioner(8), List.of("a", "b"));
        check("table: round robin owners", table.ownerOf(0).equals("a") && table.ownerOf(1).equals("b") && table.ownerOf(7).equals("b"));
        check("table: starts at version 1", table.version() == 1);
        table.assign(0, "b");
        check("table: assign bumps version", table.version() == 2 && table.ownerOf(0).equals("b"));
        table.assign(0, "b");
        check("table: no-op assign does not bump version", table.version() == 2);
        check("table: nodeFor agrees with partitioner", table.nodeFor("pay_42").equals(table.ownerOf(table.partitioner().partitionOf("pay_42"))));

        // rebalancer: minimal moves, balanced result, deterministic plan
        RoutingTable fixed = new RoutingTable(new HashPartitioner(64), List.of("a", "b", "c", "d"));
        Map<String, String> before = new HashMap<>();
        for (String k : keys) before.put(k, fixed.nodeFor(k));
        List<Rebalancer.Move> plan = Rebalancer.planAddNode(fixed, "e");
        check("rebalance add: moves exactly floor(64/5)=12 partitions", plan.size() == 12);
        check("rebalance add: every move targets the new node", plan.stream().allMatch(m -> m.to().equals("e")));
        Rebalancer.apply(fixed, plan);
        Map<String, Integer> load = fixed.partitionsPerNode();
        int max = load.values().stream().mapToInt(Integer::intValue).max().orElse(0);
        int min = load.values().stream().mapToInt(Integer::intValue).min().orElse(0);
        check("rebalance add: max-min load <= 1", max - min <= 1);
        double moved = Main.movedFraction(keys, before::get, fixed::nodeFor);
        check("rebalance add: about 12/64 of keys moved", moved > 0.15 && moved < 0.23);
        check("rebalance add: keys not in moved partitions stayed put",
                keys.stream().filter(k -> !fixed.nodeFor(k).equals("e")).allMatch(k -> before.get(k).equals(fixed.nodeFor(k))));
        checkThrows("rebalance add: rejects duplicate node", IllegalArgumentException.class, () -> Rebalancer.planAddNode(fixed, "a"));

        List<Rebalancer.Move> removal = Rebalancer.planRemoveNode(fixed, "e");
        check("rebalance remove: moves exactly the leaving node's partitions", removal.size() == 12 && removal.stream().allMatch(m -> m.from().equals("e")));
        Rebalancer.apply(fixed, removal);
        check("rebalance remove: node gone, 4 nodes remain", fixed.nodes().size() == 4 && !fixed.nodes().contains("e"));
        checkThrows("rebalance remove: cannot remove last node", IllegalStateException.class,
                () -> Rebalancer.planRemoveNode(new RoutingTable(new HashPartitioner(4), List.of("only")), "only"));

        // naive mod-N vs consistent hashing
        HashPartitioner mod4 = new HashPartitioner(4), mod5 = new HashPartitioner(5);
        double naive = Main.movedFraction(keys, k -> "" + mod4.partitionOf(k), k -> "" + mod5.partitionOf(k));
        check("mod-N resize moves most keys (> 70%)", naive > 0.70);
        ConsistentHashRing ring = new ConsistentHashRing(150);
        for (String n : List.of("a", "b", "c", "d")) ring.addNode(n);
        check("ring: 4 nodes x 150 vnodes", ring.size() == 600);
        Map<String, String> ringBefore = new HashMap<>();
        for (String k : keys) ringBefore.put(k, ring.nodeFor(k));
        ring.addNode("e");
        double ringMoved = Main.movedFraction(keys, ringBefore::get, ring::nodeFor);
        check("ring: adding 5th node moves roughly 1/5 of keys", ringMoved > 0.14 && ringMoved < 0.27);
        check("ring: keys that moved all moved to the new node",
                keys.stream().filter(k -> !ringBefore.get(k).equals(ring.nodeFor(k))).allMatch(k -> ring.nodeFor(k).equals("e")));
        ring.removeNode("e");
        check("ring: removing the node restores every key's owner", keys.stream().allMatch(k -> ringBefore.get(k).equals(ring.nodeFor(k))));

        // live migration: copy, dual-write, cutover, drop; no key lost, stale clients redirected
        RoutingTable t2 = new RoutingTable(new HashPartitioner(8), List.of("a", "b"));
        StorageNode a = new StorageNode("a", t2), e = new StorageNode("e", t2);
        List<String> p7 = keys.stream().filter(k -> t2.partitioner().partitionOf(k) == 6).toList();
        for (String k : p7.subList(0, 100)) a.put(k, "v1");
        checkThrows("node refuses writes for partitions it does not own", WrongOwnerException.class, () -> e.put(p7.get(0), "x"));
        PartitionMigration mig = new PartitionMigration(6, a, e, t2);
        check("migration starts PLANNED", mig.state() == PartitionMigration.State.PLANNED);
        checkThrows("migration: cannot cut over before copying", IllegalStateException.class, mig::cutOver);
        mig.write(p7.get(100), "pre-copy");
        mig.startCopy();
        check("migration: copied all 101 keys", mig.copied == 101 && mig.state() == PartitionMigration.State.DUAL_WRITE);
        mig.write(p7.get(101), "during");
        mig.write(p7.get(0), "v1-updated");
        check("migration: dual writes counted", mig.dualWrites == 2);
        check("migration: reads before cutover still served by source", a.get(p7.get(0)).equals("v1-updated"));
        checkThrows("migration: rejects key outside the partition", IllegalArgumentException.class,
                () -> mig.write(keys.stream().filter(k -> t2.partitioner().partitionOf(k) != 6).findFirst().get(), "x"));
        long vBefore = t2.version();
        mig.cutOver();
        check("migration: cutover bumps routing version and flips owner", t2.version() == vBefore + 1 && t2.ownerOf(6).equals("e"));
        checkThrows("migration: stale client at source gets WrongOwner", WrongOwnerException.class, () -> a.get(p7.get(0)));
        try { a.get(p7.get(0)); } catch (WrongOwnerException ex) {
            check("migration: WrongOwner carries current version", ex.currentVersion == t2.version() && ex.partition == 6);
        }
        check("migration: destination has the pre-copy, during, and updated values",
                e.get(p7.get(100)).equals("pre-copy") && e.get(p7.get(101)).equals("during") && e.get(p7.get(0)).equals("v1-updated"));
        mig.write(p7.get(102), "post-cutover");
        check("migration: post-cutover writes go to destination only", e.get(p7.get(102)).equals("post-cutover") && a.size() == 102);
        int dropped = mig.dropSource();
        check("migration: drop removes exactly the old copy", dropped == 102 && a.size() == 0 && mig.state() == PartitionMigration.State.DONE);
        check("migration: destination ends with 103 keys", e.size() == 103);
        checkThrows("migration: constructor checks source owns partition", IllegalArgumentException.class,
                () -> new PartitionMigration(6, a, e, t2));

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }
}
