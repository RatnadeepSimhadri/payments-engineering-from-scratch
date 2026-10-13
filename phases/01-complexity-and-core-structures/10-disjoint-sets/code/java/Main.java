// Lesson: phases/01-complexity-and-core-structures/10-disjoint-sets/docs/en.md
// Disjoint Sets (Union-Find) with Path Compression
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: Tarjan, "Efficiency of a Good But Not Linear Set Union Algorithm", JACM 22(2), 1975;
//          Cormen et al., "Introduction to Algorithms", ch. 19 (data structures for disjoint sets).
// Java 21, standard library only.

import java.util.ArrayList;
import java.util.Comparator;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.Random;
import java.util.TreeMap;

/**
 * Disjoint-set forest over the integers 0..n-1.
 *
 * Invariant: parent[] encodes a forest; every element reaches exactly one root by following parent
 * pointers, and two elements are in the same set iff they reach the same root. rank[r] is an upper
 * bound on the height of the tree rooted at r and is only meaningful at roots. size[r] is the exact
 * number of elements in r's set.
 *
 * The two heuristics can be switched off independently so the lesson can measure what each buys.
 */
final class DisjointSet {
    private final int[] parent;
    private final int[] rank;
    private final int[] size;
    private int components;
    private final boolean unionByRank;
    private final boolean pathCompression;
    private long hops; // instrumentation: parent pointers followed inside find

    DisjointSet(int n) { this(n, true, true); }

    DisjointSet(int n, boolean unionByRank, boolean pathCompression) {
        if (n < 0) throw new IllegalArgumentException("n must be >= 0");
        parent = new int[n];
        rank = new int[n];
        size = new int[n];
        for (int i = 0; i < n; i++) { parent[i] = i; size[i] = 1; }
        components = n;
        this.unionByRank = unionByRank;
        this.pathCompression = pathCompression;
    }

    int elements() { return parent.length; }
    int componentCount() { return components; }
    long hops() { return hops; }
    void resetHops() { hops = 0; }

    private void checkIndex(int x) {
        if (x < 0 || x >= parent.length) throw new IndexOutOfBoundsException("element " + x + " not in 0.." + (parent.length - 1));
    }

    /**
     * Representative of x's set. Two passes: walk to the root, then point every node on the path
     * straight at it. Iterative, so a long uncompressed chain cannot overflow the stack.
     */
    int find(int x) {
        checkIndex(x);
        int root = x;
        while (parent[root] != root) { root = parent[root]; hops++; }
        if (pathCompression) {
            while (parent[x] != root) {
                int next = parent[x];
                parent[x] = root;
                x = next;
            }
        }
        return root;
    }

    /** Merge the sets containing a and b. Returns false if they were already the same set. */
    boolean union(int a, int b) {
        int ra = find(a), rb = find(b);
        if (ra == rb) return false;
        if (unionByRank) {
            if (rank[ra] < rank[rb]) { int t = ra; ra = rb; rb = t; } // ra is now the taller (or equal) root
            parent[rb] = ra;
            if (rank[ra] == rank[rb]) rank[ra]++;
        } else {
            parent[rb] = ra; // naive: always hang b's root under a's root
        }
        size[ra] += size[rb];
        components--;
        return true;
    }

    boolean connected(int a, int b) { return find(a) == find(b); }

    /** Number of elements in x's set. */
    int componentSize(int x) { return size[find(x)]; }

    /** Rank of the root of x's set: an upper bound on the height of its tree. */
    int rankOf(int x) { return rank[find(x)]; }

    /** Actual depth of x: how many parent pointers separate it from its root. Does not compress. */
    int depth(int x) {
        checkIndex(x);
        int d = 0;
        while (parent[x] != x) { x = parent[x]; d++; }
        return d;
    }

    /** Deepest element in the forest. O(n * depth); diagnostic only. */
    int maxDepth() {
        int max = 0;
        for (int i = 0; i < parent.length; i++) max = Math.max(max, depth(i));
        return max;
    }

    /** Verifies the forest invariants: parents are in range, every walk terminates, sizes and component count agree. */
    boolean checkInvariant() {
        int roots = 0;
        long total = 0;
        int[] counted = new int[parent.length];
        for (int i = 0; i < parent.length; i++) {
            if (parent[i] < 0 || parent[i] >= parent.length) return false;
            int x = i, steps = 0;
            while (parent[x] != x) { x = parent[x]; if (++steps > parent.length) return false; } // cycle guard
            counted[x]++;
            if (parent[i] == i) { roots++; total += size[i]; }
        }
        if (roots != components || total != parent.length) return false;
        for (int i = 0; i < parent.length; i++) if (parent[i] == i && counted[i] != size[i]) return false;
        return true;
    }
}

/**
 * Entity linking for fraud: every identifier (account, device, card fingerprint, email) is an element,
 * every shared identifier is a union, and a "ring" is a connected component. Names map to indices
 * lazily so callers never see integers.
 */
final class EntityLinker {
    private final Map<String, Integer> index = new HashMap<>();
    private final List<String> names = new ArrayList<>();
    private final int capacity;
    private final DisjointSet sets;
    private int next;

    EntityLinker(int capacity) {
        this.capacity = capacity;
        this.sets = new DisjointSet(capacity);
    }

    private int id(String name) {
        Integer existing = index.get(name);
        if (existing != null) return existing;
        if (next >= capacity) throw new IllegalStateException("linker capacity exceeded: " + capacity);
        index.put(name, next);
        names.add(name);
        return next++;
    }

    /** Record that two identifiers were seen together. Returns true if this observation merged two rings. */
    boolean link(String a, String b) { return sets.union(id(a), id(b)); }

    boolean sameRing(String a, String b) {
        Integer ia = index.get(a), ib = index.get(b);
        return ia != null && ib != null && sets.connected(ia, ib);
    }

    int ringSize(String name) {
        Integer i = index.get(name);
        return i == null ? 0 : sets.componentSize(i);
    }

    /** All identifiers in the same ring as name, sorted for deterministic output. */
    List<String> ring(String name) {
        List<String> out = new ArrayList<>();
        Integer i = index.get(name);
        if (i == null) return out;
        int root = sets.find(i);
        for (int j = 0; j < next; j++) if (sets.find(j) == root) out.add(names.get(j));
        out.sort(Comparator.naturalOrder());
        return out;
    }

    /** Every ring with at least minSize members, largest first; ties broken by the smallest member name. */
    List<List<String>> ringsOfAtLeast(int minSize) {
        Map<Integer, List<String>> byRoot = new TreeMap<>();
        for (int j = 0; j < next; j++) byRoot.computeIfAbsent(sets.find(j), k -> new ArrayList<>()).add(names.get(j));
        List<List<String>> out = new ArrayList<>();
        for (List<String> members : byRoot.values()) {
            if (members.size() >= minSize) { members.sort(Comparator.naturalOrder()); out.add(members); }
        }
        out.sort(Comparator.<List<String>>comparingInt(List::size).reversed().thenComparing(l -> l.get(0)));
        return out;
    }

    int identifiers() { return next; }
    int rings() { return sets.componentCount() - (capacity - next); } // unused slots are singletons we have not handed out
}

public final class Main {

    static void demoBasics() {
        System.out.println("== Union, find, connected ==");
        DisjointSet ds = new DisjointSet(8);
        System.out.println("8 elements, " + ds.componentCount() + " components");
        ds.union(0, 1); ds.union(2, 3); ds.union(1, 3); ds.union(5, 6);
        System.out.println("after union(0,1) union(2,3) union(1,3) union(5,6): " + ds.componentCount() + " components");
        System.out.println("connected(0,2)=" + ds.connected(0, 2) + "  connected(0,5)=" + ds.connected(0, 5)
                + "  size of 0's set=" + ds.componentSize(0) + "  size of 7's set=" + ds.componentSize(7));
        System.out.println("union(0,3) again -> " + ds.union(0, 3) + " (already joined; a cycle in the input, not in the forest)");
        System.out.println("invariant holds: " + ds.checkInvariant());
        System.out.println();
    }

    static void demoHeuristics() {
        System.out.println("== What union by rank and path compression buy ==");
        int n = 20_000;
        String[] labels = {"naive (neither)", "union by rank only", "path compression only", "both"};
        boolean[][] flags = {{false, false}, {true, false}, {false, true}, {true, true}};
        for (int v = 0; v < 4; v++) {
            DisjointSet ds = new DisjointSet(n, flags[v][0], flags[v][1]);
            // adversarial order for the naive forest: always union the growing set's element under a fresh singleton
            for (int i = 1; i < n; i++) ds.union(i, i - 1);
            int depthBefore = ds.maxDepth();
            ds.resetHops();
            for (int i = 0; i < n; i++) ds.find(i);
            long hopsPerFind = ds.hops() / n;
            System.out.printf("%-24s max depth before finds %6d, pointers followed per find %5d, max depth after %3d%n",
                    labels[v], depthBefore, hopsPerFind, ds.maxDepth());
        }
        System.out.println("chain workload: rank refuses to build the chain; compression repairs it after one find");
        System.out.println();
        System.out.println("random workload: 20,000 elements, 30,000 random unions, then one find per element");
        for (int v = 0; v < 4; v++) {
            DisjointSet ds = new DisjointSet(n, flags[v][0], flags[v][1]);
            Random rnd = new Random(3);
            for (int i = 0; i < 30_000; i++) ds.union(rnd.nextInt(n), rnd.nextInt(n));
            int depthBefore = ds.maxDepth();
            ds.resetHops();
            for (int i = 0; i < n; i++) ds.find(i);
            System.out.printf("%-24s max depth before finds %6d, pointers followed per find %5.2f, max depth after %3d%n",
                    labels[v], depthBefore, ds.hops() / (double) n, ds.maxDepth());
        }
        System.out.println("rank alone caps depth at log2(n) = 14; both together make almost every find a single hop");
        System.out.println();
    }

    static void demoFraudRing() {
        System.out.println("== Entity linking: accounts that share devices, cards, or emails ==");
        EntityLinker linker = new EntityLinker(64);
        String[][] observations = {
            {"acct:1001", "device:A"}, {"acct:1002", "device:A"},                        // two accounts, one phone
            {"acct:1002", "card:4111-xx-0001"}, {"acct:1003", "card:4111-xx-0001"},     // card fingerprint reused
            {"acct:1003", "email:x@example.com"}, {"acct:1004", "email:x@example.com"},  // same email
            {"acct:2001", "device:B"}, {"acct:2002", "device:B"},                        // a separate, smaller ring
            {"acct:3001", "device:C"},                                                   // an honest singleton pair
        };
        for (String[] o : observations) {
            boolean merged = linker.link(o[0], o[1]);
            System.out.printf("  link %-10s <-> %-20s %s%n", o[0], o[1], merged ? "merged" : "already linked");
        }
        System.out.println("identifiers seen: " + linker.identifiers() + ", rings: " + linker.rings());
        System.out.println("acct:1001 and acct:1004 share a ring: " + linker.sameRing("acct:1001", "acct:1004")
                + " (no direct link between them; three hops through device, card, email)");
        System.out.println("acct:1001 and acct:2001 share a ring: " + linker.sameRing("acct:1001", "acct:2001"));
        for (List<String> ring : linker.ringsOfAtLeast(4)) {
            System.out.println("ring of " + ring.size() + " flagged for review: " + ring);
        }
        System.out.println();
    }

    static void demoRandomMerges() {
        System.out.println("== 100,000 random unions over 50,000 elements ==");
        int n = 50_000;
        DisjointSet ds = new DisjointSet(n);
        Random rnd = new Random(11);
        int merges = 0;
        for (int i = 0; i < 100_000; i++) if (ds.union(rnd.nextInt(n), rnd.nextInt(n))) merges++;
        ds.resetHops();
        for (int i = 0; i < n; i++) ds.find(i);
        System.out.printf("effective merges %d, components left %d, largest set %d, max depth %d, avg pointers per find %.2f%n",
                merges, ds.componentCount(), largest(ds), ds.maxDepth(), ds.hops() / (double) n);
    }

    private static int largest(DisjointSet ds) {
        int best = 0;
        for (int i = 0; i < ds.elements(); i++) best = Math.max(best, ds.componentSize(i));
        return best;
    }

    public static void main(String[] args) {
        demoBasics();
        demoHeuristics();
        demoFraudRing();
        demoRandomMerges();
    }
}
