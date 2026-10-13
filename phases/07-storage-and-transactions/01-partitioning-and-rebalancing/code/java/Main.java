// Lesson: phases/07-storage-and-transactions/01-partitioning-and-rebalancing/docs/en.md
// Partitioning Strategies and Rebalancing
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: Karger et al., "Consistent Hashing and Random Trees" (STOC 1997); DeCandia et al., "Dynamo" (SOSP 2007);
//          FNV-1a hash (Fowler, Noll, Vo). Java 21, standard library only.

import java.util.ArrayList;
import java.util.Collections;
import java.util.HashMap;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.Objects;
import java.util.TreeMap;

/** Maps a key to a logical partition number in [0, partitionCount). */
interface Partitioner {
    int partitionOf(String key);

    int partitionCount();

    /** FNV-1a, 64-bit. Deterministic across JVMs and languages, unlike String.hashCode's sign and the JDK's murmur. */
    static long fnv1a64(String key) {
        long hash = 0xcbf29ce484222325L;
        for (int i = 0; i < key.length(); i++) {
            hash ^= key.charAt(i);
            hash *= 0x100000001b3L;
        }
        return hash;
    }

    /** FNV-1a mixes low bits well but leaves the high bits of short keys correlated; this finaliser (from SplitMix64) fixes that. */
    static long hash64(String key) {
        long z = fnv1a64(key);
        z = (z ^ (z >>> 30)) * 0xbf58476d1ce4e5b9L;
        z = (z ^ (z >>> 27)) * 0x94d049bb133111ebL;
        return z ^ (z >>> 31);
    }
}

/** Key ranges: partition i holds keys in [splitPoints[i-1], splitPoints[i]). Preserves sort order, invites hot ranges. */
final class RangePartitioner implements Partitioner {
    private final List<String> splitPoints;

    RangePartitioner(List<String> splitPoints) {
        for (int i = 1; i < splitPoints.size(); i++) {
            if (splitPoints.get(i - 1).compareTo(splitPoints.get(i)) >= 0) {
                throw new IllegalArgumentException("split points must be strictly increasing");
            }
        }
        this.splitPoints = List.copyOf(splitPoints);
    }

    @Override
    public int partitionOf(String key) {
        int lo = 0, hi = splitPoints.size();     // find the number of split points <= key
        while (lo < hi) {
            int mid = (lo + hi) >>> 1;
            if (splitPoints.get(mid).compareTo(key) <= 0) lo = mid + 1; else hi = mid;
        }
        return lo;
    }

    @Override
    public int partitionCount() { return splitPoints.size() + 1; }

    List<String> splitPoints() { return splitPoints; }
}

/** Hash of the key modulo a FIXED partition count. Spreads keys uniformly; destroys range scans. */
final class HashPartitioner implements Partitioner {
    private final int partitions;

    HashPartitioner(int partitions) {
        if (partitions <= 0) throw new IllegalArgumentException("partition count must be positive");
        this.partitions = partitions;
    }

    @Override
    public int partitionOf(String key) {
        return (int) Long.remainderUnsigned(Partitioner.hash64(key), partitions);
    }

    @Override
    public int partitionCount() { return partitions; }
}

/** Consistent hashing: nodes own arcs of a 64-bit ring; adding a node moves only the keys on the arcs it takes. */
final class ConsistentHashRing {
    private final TreeMap<Long, String> ring = new TreeMap<>();
    private final int virtualNodes;

    ConsistentHashRing(int virtualNodes) {
        if (virtualNodes <= 0) throw new IllegalArgumentException("virtual nodes must be positive");
        this.virtualNodes = virtualNodes;
    }

    void addNode(String node) {
        for (int v = 0; v < virtualNodes; v++) ring.put(Partitioner.hash64(node + "#" + v), node);
    }

    void removeNode(String node) {
        for (int v = 0; v < virtualNodes; v++) ring.remove(Partitioner.hash64(node + "#" + v));
    }

    String nodeFor(String key) {
        if (ring.isEmpty()) throw new IllegalStateException("ring has no nodes");
        Map.Entry<Long, String> e = ring.ceilingEntry(Partitioner.hash64(key));
        return e != null ? e.getValue() : ring.firstEntry().getValue();
    }

    int size() { return ring.size(); }
}

/**
 * Versioned partition -> node map. Every change bumps the version so a client holding an old copy can be
 * told exactly that: "your table is stale, refresh". Fixed partition count, so rebalancing moves whole partitions.
 */
final class RoutingTable {
    private final Partitioner partitioner;
    private final String[] owner;
    private long version = 1;

    RoutingTable(Partitioner partitioner, List<String> nodes) {
        if (nodes.isEmpty()) throw new IllegalArgumentException("need at least one node");
        this.partitioner = partitioner;
        this.owner = new String[partitioner.partitionCount()];
        for (int p = 0; p < owner.length; p++) owner[p] = nodes.get(p % nodes.size());   // round-robin initial layout
    }

    long version() { return version; }

    Partitioner partitioner() { return partitioner; }

    int partitionCount() { return owner.length; }

    String ownerOf(int partition) { return owner[partition]; }

    String nodeFor(String key) { return owner[partitioner.partitionOf(key)]; }

    void assign(int partition, String node) {
        Objects.requireNonNull(node);
        if (!node.equals(owner[partition])) {
            owner[partition] = node;
            version++;
        }
    }

    List<String> nodes() {
        List<String> out = new ArrayList<>();
        for (String n : owner) if (!out.contains(n)) out.add(n);
        Collections.sort(out);
        return out;
    }

    Map<String, Integer> partitionsPerNode() {
        Map<String, Integer> counts = new TreeMap<>();
        for (String n : owner) counts.merge(n, 1, Integer::sum);
        return counts;
    }

    /** Partitions currently owned by node, in ascending order. */
    List<Integer> partitionsOf(String node) {
        List<Integer> out = new ArrayList<>();
        for (int p = 0; p < owner.length; p++) if (owner[p].equals(node)) out.add(p);
        return out;
    }
}

/** Plans the minimum set of partition moves that makes a routing table balanced after a node joins or leaves. */
final class Rebalancer {
    record Move(int partition, String from, String to) {}

    /** Add node; repeatedly take one partition from the most loaded node until no node has 2+ more than any other. */
    static List<Move> planAddNode(RoutingTable table, String newNode) {
        if (table.nodes().contains(newNode)) throw new IllegalArgumentException("node already present: " + newNode);
        List<Move> moves = new ArrayList<>();
        Map<String, List<Integer>> owned = new LinkedHashMap<>();
        for (String n : table.nodes()) owned.put(n, new ArrayList<>(table.partitionsOf(n)));
        owned.put(newNode, new ArrayList<>());
        int nodes = owned.size();
        int target = table.partitionCount() / nodes;   // the new node gets floor(P / N)
        while (owned.get(newNode).size() < target) {
            String donor = mostLoaded(owned);
            List<Integer> donorParts = owned.get(donor);
            int partition = donorParts.remove(donorParts.size() - 1);   // take the highest-numbered partition: deterministic
            owned.get(newNode).add(partition);
            moves.add(new Move(partition, donor, newNode));
        }
        return moves;
    }

    /** Remove node; hand each of its partitions to the currently least loaded survivor. */
    static List<Move> planRemoveNode(RoutingTable table, String leaving) {
        if (!table.nodes().contains(leaving)) throw new IllegalArgumentException("node not present: " + leaving);
        if (table.nodes().size() == 1) throw new IllegalStateException("cannot remove the last node");
        List<Move> moves = new ArrayList<>();
        Map<String, List<Integer>> owned = new LinkedHashMap<>();
        for (String n : table.nodes()) if (!n.equals(leaving)) owned.put(n, new ArrayList<>(table.partitionsOf(n)));
        for (int partition : table.partitionsOf(leaving)) {
            String receiver = leastLoaded(owned);
            owned.get(receiver).add(partition);
            moves.add(new Move(partition, leaving, receiver));
        }
        return moves;
    }

    static void apply(RoutingTable table, List<Move> moves) {
        for (Move m : moves) table.assign(m.partition(), m.to());
    }

    private static String mostLoaded(Map<String, List<Integer>> owned) {
        String best = null;
        for (Map.Entry<String, List<Integer>> e : owned.entrySet()) {
            if (best == null || e.getValue().size() > owned.get(best).size()) best = e.getKey();
        }
        return best;
    }

    private static String leastLoaded(Map<String, List<Integer>> owned) {
        String best = null;
        for (Map.Entry<String, List<Integer>> e : owned.entrySet()) {
            if (best == null || e.getValue().size() < owned.get(best).size()) best = e.getKey();
        }
        return best;
    }
}

/** Counts requests per partition and reports skew: how much hotter the hottest partition is than the average. */
final class HotKeyDetector {
    private final long[] hits;
    private long total;

    HotKeyDetector(int partitions) { this.hits = new long[partitions]; }

    void record(int partition) {
        hits[partition]++;
        total++;
    }

    int hottestPartition() {
        int best = 0;
        for (int p = 1; p < hits.length; p++) if (hits[p] > hits[best]) best = p;
        return best;
    }

    long hits(int partition) { return hits[partition]; }

    /** max / mean, so 1.0 is perfectly balanced and N means one partition takes everything. */
    double skew() {
        if (total == 0) return 1.0;
        double mean = (double) total / hits.length;
        return hits[hottestPartition()] / mean;
    }

    /** Append a small salt derived from a secondary field so one logical key spreads over `fanout` partitions. */
    static String salt(String key, String secondary, int fanout) {
        int s = (int) Long.remainderUnsigned(Partitioner.hash64(secondary), fanout);
        return key + "#" + s;
    }
}

/** Thrown by a node that receives a request for a partition it no longer owns; carries the version the client should fetch. */
final class WrongOwnerException extends RuntimeException {
    final int partition;
    final long currentVersion;

    WrongOwnerException(int partition, long currentVersion) {
        super("partition " + partition + " not owned here; routing table is now at version " + currentVersion);
        this.partition = partition;
        this.currentVersion = currentVersion;
    }
}

/** An in-memory storage node that only answers for partitions the routing table says it owns. */
final class StorageNode {
    final String name;
    private final Map<String, String> data = new TreeMap<>();
    private final RoutingTable table;

    StorageNode(String name, RoutingTable table) {
        this.name = name;
        this.table = table;
    }

    void put(String key, String value) {
        requireOwner(key);
        data.put(key, value);
    }

    String get(String key) {
        requireOwner(key);
        return data.get(key);
    }

    /** Used by the migration itself, which legitimately writes into a node that is not yet the owner. */
    void putDirect(String key, String value) { data.put(key, value); }

    Map<String, String> snapshotOfPartition(int partition) {
        Map<String, String> out = new TreeMap<>();
        for (Map.Entry<String, String> e : data.entrySet()) {
            if (table.partitioner().partitionOf(e.getKey()) == partition) out.put(e.getKey(), e.getValue());
        }
        return out;
    }

    int dropPartition(int partition) {
        int removed = 0;
        for (String k : new ArrayList<>(data.keySet())) {
            if (table.partitioner().partitionOf(k) == partition) { data.remove(k); removed++; }
        }
        return removed;
    }

    int size() { return data.size(); }

    private void requireOwner(String key) {
        int p = table.partitioner().partitionOf(key);
        if (!table.ownerOf(p).equals(name)) throw new WrongOwnerException(p, table.version());
    }
}

/**
 * Moves one partition from source to destination without refusing writes: bulk copy, dual-write for the
 * writes that arrive meanwhile, cutover by bumping the routing table, then drop the old copy.
 */
final class PartitionMigration {
    enum State { PLANNED, COPYING, DUAL_WRITE, CUT_OVER, DONE }

    final int partition;
    final StorageNode source;
    final StorageNode destination;
    private final RoutingTable table;
    private State state = State.PLANNED;
    int copied;
    int dualWrites;

    PartitionMigration(int partition, StorageNode source, StorageNode destination, RoutingTable table) {
        if (!table.ownerOf(partition).equals(source.name)) {
            throw new IllegalArgumentException(source.name + " does not own partition " + partition);
        }
        this.partition = partition;
        this.source = source;
        this.destination = destination;
        this.table = table;
    }

    State state() { return state; }

    /** Phase 1: snapshot the partition and copy it. Writes keep going to source only. */
    void startCopy() {
        requireState(State.PLANNED);
        state = State.COPYING;
        for (Map.Entry<String, String> e : source.snapshotOfPartition(partition).entrySet()) {
            destination.putDirect(e.getKey(), e.getValue());
            copied++;
        }
        state = State.DUAL_WRITE;
    }

    /** Phase 2: every write to this partition goes to both nodes until cutover. Reads still go to source. */
    void write(String key, String value) {
        if (table.partitioner().partitionOf(key) != partition) throw new IllegalArgumentException("key not in partition");
        if (state == State.DUAL_WRITE) {
            source.put(key, value);
            destination.putDirect(key, value);
            dualWrites++;
        } else if (state == State.PLANNED || state == State.COPYING) {
            source.put(key, value);
        } else {
            destination.put(key, value);
        }
    }

    /** Phase 3: flip the routing table. From now on source rejects the partition with WrongOwnerException. */
    void cutOver() {
        requireState(State.DUAL_WRITE);
        table.assign(partition, destination.name);
        state = State.CUT_OVER;
    }

    /** Phase 4: once no client can still be mid-write to source, free the old copy. */
    int dropSource() {
        requireState(State.CUT_OVER);
        int dropped = source.dropPartition(partition);
        state = State.DONE;
        return dropped;
    }

    private void requireState(State expected) {
        if (state != expected) throw new IllegalStateException("expected " + expected + " but migration is " + state);
    }
}

public final class Main {

    /** Fraction of keys whose owner changes when the layout goes from `before` to `after`. */
    static double movedFraction(List<String> keys, java.util.function.Function<String, String> before,
                                java.util.function.Function<String, String> after) {
        int moved = 0;
        for (String k : keys) if (!before.apply(k).equals(after.apply(k))) moved++;
        return (double) moved / keys.size();
    }

    static List<String> paymentIds(int n) {
        List<String> ids = new ArrayList<>(n);
        for (int i = 0; i < n; i++) ids.add(String.format("pay_%08d", i));
        return ids;
    }

    public static void main(String[] args) {
        List<String> keys = paymentIds(100_000);
        List<String> nodes = List.of("node-a", "node-b", "node-c", "node-d");

        System.out.println("== Range vs hash: where do merchants land? ==");
        RangePartitioner byMerchantPrefix = new RangePartitioner(List.of("f", "m", "s"));   // [..f) [f..m) [m..s) [s..)
        HashPartitioner byHash = new HashPartitioner(4);
        for (String m : List.of("acme", "globex", "megamart", "zenith")) {
            System.out.printf("  merchant %-9s range-partition=%d hash-partition=%d%n", m,
                    byMerchantPrefix.partitionOf(m), byHash.partitionOf(m));
        }

        System.out.println();
        System.out.println("== Hot key: one merchant is 60% of traffic ==");
        HotKeyDetector plain = new HotKeyDetector(16);
        HotKeyDetector salted = new HotKeyDetector(16);
        HashPartitioner p16 = new HashPartitioner(16);
        for (int i = 0; i < 10_000; i++) {
            String merchant = i % 10 < 6 ? "megamart" : "merchant-" + (i % 97);
            plain.record(p16.partitionOf(merchant));
            salted.record(p16.partitionOf(HotKeyDetector.salt(merchant, "pay_" + i, 8)));
        }
        System.out.printf("  unsalted: hottest partition %d takes %d of 10000 (skew %.1fx)%n",
                plain.hottestPartition(), plain.hits(plain.hottestPartition()), plain.skew());
        System.out.printf("  salted x8: hottest partition %d takes %d of 10000 (skew %.1fx); reads for megamart now fan out to 8 partitions%n",
                salted.hottestPartition(), salted.hits(salted.hottestPartition()), salted.skew());

        System.out.println();
        System.out.println("== Adding a 5th node: how many keys move? ==");
        HashPartitioner mod4 = new HashPartitioner(4);
        HashPartitioner mod5 = new HashPartitioner(5);
        double naive = movedFraction(keys, k -> "n" + mod4.partitionOf(k), k -> "n" + mod5.partitionOf(k));
        System.out.printf("  hash mod N, N 4 -> 5:               %.1f%% of keys move (almost everything)%n", naive * 100);

        RoutingTable fixed = new RoutingTable(new HashPartitioner(64), nodes);
        Map<String, String> ownerBefore = new HashMap<>();
        for (String k : keys) ownerBefore.put(k, fixed.nodeFor(k));
        List<Rebalancer.Move> moves = Rebalancer.planAddNode(fixed, "node-e");
        Rebalancer.apply(fixed, moves);
        double fixedMoved = movedFraction(keys, ownerBefore::get, fixed::nodeFor);
        System.out.printf("  64 fixed partitions, 4 -> 5 nodes:  %.1f%% of keys move (%d partition moves, layout %s)%n",
                fixedMoved * 100, moves.size(), fixed.partitionsPerNode());

        ConsistentHashRing ring = new ConsistentHashRing(150);
        for (String n : nodes) ring.addNode(n);
        Map<String, String> ringBefore = new HashMap<>();
        for (String k : keys) ringBefore.put(k, ring.nodeFor(k));
        ring.addNode("node-e");
        double ringMoved = movedFraction(keys, ringBefore::get, ring::nodeFor);
        System.out.printf("  consistent hashing, 150 vnodes:     %.1f%% of keys move (ideal 1/5 = 20.0%%)%n", ringMoved * 100);

        System.out.println();
        System.out.println("== Moving partition 6 from node-a to node-e without downtime ==");
        RoutingTable table = new RoutingTable(new HashPartitioner(8), List.of("node-a", "node-b"));
        StorageNode a = new StorageNode("node-a", table);
        StorageNode e = new StorageNode("node-e", table);
        int loaded = 0;
        for (String k : keys.subList(0, 2_000)) {
            if (table.partitioner().partitionOf(k) == 6) { a.put(k, "v1"); loaded++; }
        }
        System.out.printf("  partition 6 has %d keys on node-a; routing table v%d%n", loaded, table.version());
        PartitionMigration mig = new PartitionMigration(6, a, e, table);
        mig.startCopy();
        System.out.printf("  bulk copy done: %d keys copied, state %s%n", mig.copied, mig.state());
        String live = keys.subList(2_000, 100_000).stream().filter(k -> table.partitioner().partitionOf(k) == 6).findFirst().orElseThrow();
        mig.write(live, "v2-written-during-migration");
        System.out.printf("  live write to %s during migration went to both nodes (dual writes: %d)%n", live, mig.dualWrites);
        mig.cutOver();
        System.out.printf("  cut over: routing table v%d, partition 6 owner = %s%n", table.version(), table.ownerOf(6));
        try {
            a.get(live);
        } catch (WrongOwnerException ex) {
            System.out.println("  stale client asked node-a: " + ex.getMessage());
        }
        System.out.printf("  node-e serves %s = %s%n", live, e.get(live));
        System.out.printf("  dropped %d keys from node-a; migration %s%n", mig.dropSource(), mig.state());
    }
}
