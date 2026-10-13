// Lesson: phases/03-systems-data-structures/06-consistent-hashing/docs/en.md
// Consistent Hashing and Virtual Nodes
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: Karger et al. (1997), "Consistent Hashing and Random Trees"; DeCandia et al. (2007), "Dynamo";
//          FNV-1a (IETF draft-eastlake-fnv) and MurmurHash3 fmix64 for the point hash.
// Java 21, standard library only.

import java.nio.charset.StandardCharsets;
import java.util.ArrayList;
import java.util.Collection;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.TreeMap;
import java.util.TreeSet;

/** Position of a key or a virtual node on the ring: a 64-bit value compared as unsigned. */
final class Hashing {
    static final long FNV_OFFSET = 0xcbf29ce484222325L;
    static final long FNV_PRIME = 0x100000001b3L;

    private Hashing() {}

    static long fnv1a64(byte[] data) {
        long h = FNV_OFFSET;
        for (byte b : data) {
            h ^= (b & 0xff);
            h *= FNV_PRIME;
        }
        return h;
    }

    static long mix64(long h) {
        h ^= h >>> 33;
        h *= 0xff51afd7ed558ccdL;
        h ^= h >>> 33;
        h *= 0xc4ceb9fe1a85ec53L;
        h ^= h >>> 33;
        return h;
    }

    /** FNV-1a for the bytes, fmix64 to spread the low bits that short, similar keys leave correlated. */
    static long hashKey(String key) {
        return mix64(fnv1a64(key.getBytes(StandardCharsets.UTF_8)));
    }
}

/**
 * A sorted array of (hash, node) points. Each physical node owns `virtualNodes` points.
 * A key belongs to the first point clockwise from its own hash; the ring wraps.
 */
final class HashRing {
    final int virtualNodes;
    private final List<Long> hashes = new ArrayList<>();   // sorted ascending (unsigned); parallel to owners
    private final List<String> owners = new ArrayList<>();
    private final TreeSet<String> nodes = new TreeSet<>();

    HashRing() { this(100); }

    HashRing(int virtualNodes) {
        if (virtualNodes <= 0) throw new IllegalArgumentException("virtualNodes must be positive: " + virtualNodes);
        this.virtualNodes = virtualNodes;
    }

    // ----- membership of the ring ------------------------------------------------------------

    /** The i-th virtual node of `node` sits at hash(node + "#" + i). */
    static long pointHash(String node, int replica) {
        return Hashing.hashKey(node + "#" + replica);
    }

    /** First index whose hash is >= h in unsigned order, or hashes.size() if none. */
    private int bisectLeft(long h) {
        int lo = 0, hi = hashes.size();
        while (lo < hi) {
            int mid = (lo + hi) >>> 1;
            if (Long.compareUnsigned(hashes.get(mid), h) < 0) lo = mid + 1; else hi = mid;
        }
        return lo;
    }

    void addNode(String node) {
        if (nodes.contains(node)) throw new IllegalArgumentException("node already on ring: " + node);
        for (int i = 0; i < virtualNodes; i++) {
            long h = pointHash(node, i);
            int pos = bisectLeft(h);
            // Ties on a 64-bit hash are practically impossible; order them by owner name so every
            // language builds the identical ring.
            while (pos < hashes.size() && hashes.get(pos) == h && owners.get(pos).compareTo(node) < 0) pos++;
            hashes.add(pos, h);
            owners.add(pos, node);
        }
        nodes.add(node);
    }

    void removeNode(String node) {
        if (!nodes.contains(node)) throw new IllegalArgumentException("node not on ring: " + node);
        for (int i = owners.size() - 1; i >= 0; i--) {
            if (owners.get(i).equals(node)) {
                owners.remove(i);
                hashes.remove(i);
            }
        }
        nodes.remove(node);
    }

    List<String> nodes() { return new ArrayList<>(nodes); }

    int pointCount() { return hashes.size(); }

    long hashAt(int index) { return hashes.get(index); }

    String ownerAt(int index) { return owners.get(index); }

    // ----- lookups ---------------------------------------------------------------------------

    /** Binary search for the first point with hash >= h, wrapping to 0 past the end. */
    private int firstIndexAtOrAfter(long h) {
        int pos = bisectLeft(h);
        return pos == hashes.size() ? 0 : pos;
    }

    /** The node that owns `key`: O(log P) for P points. */
    String lookup(String key) {
        if (hashes.isEmpty()) throw new IllegalStateException("ring is empty");
        return owners.get(firstIndexAtOrAfter(Hashing.hashKey(key)));
    }

    /**
     * The owner plus the next n-1 distinct nodes clockwise: a replication preference list.
     * Returns fewer than n if the ring has fewer than n nodes.
     */
    List<String> lookupN(String key, int n) {
        if (n <= 0) throw new IllegalArgumentException("n must be positive");
        if (hashes.isEmpty()) throw new IllegalStateException("ring is empty");
        int start = firstIndexAtOrAfter(Hashing.hashKey(key));
        List<String> result = new ArrayList<>();
        int total = hashes.size();
        for (int step = 0; step < total; step++) {
            String owner = owners.get((start + step) % total);
            if (!result.contains(owner)) {
                result.add(owner);
                if (result.size() == n || result.size() == nodes.size()) break;
            }
        }
        return result;
    }

    Map<String, String> assign(Collection<String> keys) {
        Map<String, String> out = new LinkedHashMap<>();
        for (String key : keys) out.put(key, lookup(key));
        return out;
    }
}

/** The naive alternative: shard = hash(key) mod N over a sorted node list. Adding a node renumbers almost every key. */
final class ModNSharder {
    private final List<String> nodes;

    ModNSharder(Collection<String> nodes) {
        this.nodes = new ArrayList<>(new TreeSet<>(nodes));
        if (this.nodes.isEmpty()) throw new IllegalArgumentException("need at least one node");
    }

    String lookup(String key) {
        return nodes.get((int) Long.remainderUnsigned(Hashing.hashKey(key), nodes.size()));
    }

    Map<String, String> assign(Collection<String> keys) {
        Map<String, String> out = new LinkedHashMap<>();
        for (String key : keys) out.put(key, lookup(key));
        return out;
    }
}

/** Measurement helpers shared by the demo and the tests. */
final class Measure {
    private Measure() {}

    /** Share of keys whose owner changed between two assignments of the same key set. */
    static double fractionMoved(Map<String, String> before, Map<String, String> after) {
        if (before.isEmpty()) return 0.0;
        int moved = 0;
        for (Map.Entry<String, String> e : before.entrySet()) {
            if (!after.get(e.getKey()).equals(e.getValue())) moved++;
        }
        return (double) moved / before.size();
    }

    static Map<String, Integer> loadPerNode(Map<String, String> assignment, Collection<String> nodes) {
        Map<String, Integer> counts = new TreeMap<>();
        for (String node : nodes) counts.put(node, 0);
        for (String owner : assignment.values()) counts.merge(owner, 1, Integer::sum);
        return counts;
    }

    /** {max load / mean load, coefficient of variation}. 1.0 and 0.0 would be perfect balance. */
    static double[] imbalance(Map<String, String> assignment, Collection<String> nodes) {
        Collection<Integer> counts = loadPerNode(assignment, nodes).values();
        double mean = 0, max = 0;
        for (int c : counts) { mean += c; max = Math.max(max, c); }
        mean /= counts.size();
        double variance = 0;
        for (int c : counts) variance += (c - mean) * (c - mean);
        variance /= counts.size();
        return new double[] {max / mean, Math.sqrt(variance) / mean};
    }

    static List<String> accountIds(int count) {
        List<String> ids = new ArrayList<>(count);
        for (int i = 0; i < count; i++) ids.add(String.format("acct-%06d", i));
        return ids;
    }
}

public final class Main {
    public static void main(String[] args) {
        List<String> shards = new ArrayList<>();
        for (char c = 'a'; c <= 'j'; c++) shards.add("ledger-shard-" + c);
        List<String> keys = Measure.accountIds(20_000);

        System.out.println("== Where an account lands ==");
        HashRing ring = new HashRing(100);
        for (String s : shards) ring.addNode(s);
        String key = "acct-000123";
        System.out.printf("hashKey(\"%s\") = 0x%016x%n", key, Hashing.hashKey(key));
        System.out.printf("%d points on the ring for %d shards%n", ring.pointCount(), ring.nodes().size());
        System.out.printf("lookup(\"%s\") = %s%n", key, ring.lookup(key));
        System.out.printf("lookupN(\"%s\", 3) = %s  (owner + 2 replicas)%n", key, ring.lookupN(key, 3));

        System.out.println("\n== Adding an eleventh shard: how many accounts move? ==");
        Map<String, String> beforeRing = ring.assign(keys);
        Map<String, String> beforeMod = new ModNSharder(shards).assign(keys);
        ring.addNode("ledger-shard-k");
        Map<String, String> afterRing = ring.assign(keys);
        List<String> eleven = new ArrayList<>(shards);
        eleven.add("ledger-shard-k");
        Map<String, String> afterMod = new ModNSharder(eleven).assign(keys);
        System.out.printf("consistent hash ring: %.3f of keys moved (ideal 1/11 = %.3f)%n", Measure.fractionMoved(beforeRing, afterRing), 1.0 / 11);
        System.out.printf("hash(key) mod N:      %.3f of keys moved (expected 10/11 = %.3f)%n", Measure.fractionMoved(beforeMod, afterMod), 10.0 / 11);
        int movedToNew = 0, movedElsewhere = 0;
        for (String k : keys) {
            if (!afterRing.get(k).equals(beforeRing.get(k))) {
                if (afterRing.get(k).equals("ledger-shard-k")) movedToNew++; else movedElsewhere++;
            }
        }
        System.out.printf("ring: %d keys moved to the new shard, %d moved anywhere else%n", movedToNew, movedElsewhere);

        System.out.println("\n== Removing a shard: only its keys move ==");
        ring.removeNode("ledger-shard-c");
        Map<String, String> afterRemove = ring.assign(keys);
        int stayed = 0, notOnC = 0, onC = 0;
        for (String k : keys) {
            if (afterRing.get(k).equals("ledger-shard-c")) { onC++; continue; }
            notOnC++;
            if (afterRemove.get(k).equals(afterRing.get(k))) stayed++;
        }
        System.out.printf("%d keys not on shard-c kept their owner (out of %d)%n", stayed, notOnC);
        System.out.printf("%.3f of keys moved (shard-c held %.3f)%n", Measure.fractionMoved(afterRing, afterRemove), (double) onC / keys.size());

        System.out.println("\n== Virtual nodes and balance (10 shards, 20,000 accounts) ==");
        System.out.printf("%6s %9s %20s%n", "vnodes", "max/mean", "coeff. of variation");
        for (int v : new int[] {1, 10, 100, 1000}) {
            HashRing r = new HashRing(v);
            for (String s : shards) r.addNode(s);
            double[] stats = Measure.imbalance(r.assign(keys), shards);
            System.out.printf("%6d %9.3f %20.3f%n", v, stats[0], stats[1]);
        }

        System.out.println("\n== Replication: the preference list for an account ==");
        HashRing r = new HashRing(100);
        for (String s : shards.subList(0, 5)) r.addNode(s);
        for (String k : new String[] {"acct-000001", "acct-000002"}) {
            System.out.printf("lookupN(\"%s\", 3) = %s%n", k, r.lookupN(k, 3));
        }
        System.out.printf("lookupN with n > nodes returns every node once: %s%n", r.lookupN("acct-000001", 8));
        try {
            new HashRing().lookup("acct-000001");
        } catch (IllegalStateException exc) {
            System.out.println("rejected: " + exc.getMessage());
        }
    }
}
