// Lesson: phases/02-algorithms/11-minimum-spanning-trees/docs/en.md
// Minimum Spanning Trees: Kruskal and Prim
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: Kruskal, "On the shortest spanning subtree of a graph" (Proc. AMS 1956); Prim, "Shortest connection
// networks and some generalizations" (Bell System Tech. J. 1957); Tarjan, "Efficiency of a good but not linear
// set union algorithm" (JACM 1975) for union-find complexity. Java 21, standard library only.

import java.util.ArrayList;
import java.util.Collections;
import java.util.Comparator;
import java.util.HashMap;
import java.util.HashSet;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.Objects;
import java.util.PriorityQueue;
import java.util.Set;

/** An undirected edge with an integer weight (cost in whole currency minor units, metres, milliseconds). */
record WeightedEdge(String u, String v, long weight) {
    WeightedEdge {
        Objects.requireNonNull(u, "u");
        Objects.requireNonNull(v, "v");
        if (u.equals(v)) throw new IllegalArgumentException("self-loop " + u + " can never be in a spanning tree");
        if (weight < 0) throw new IllegalArgumentException("weight must be non-negative: " + weight);
    }

    /** The endpoint that is not `node`. */
    String other(String node) {
        if (node.equals(u)) return v;
        if (node.equals(v)) return u;
        throw new IllegalArgumentException(node + " is not an endpoint of " + this);
    }

    /** Two edges are the same connection regardless of which endpoint was listed first. */
    boolean sameEndpoints(WeightedEdge o) {
        return (u.equals(o.u) && v.equals(o.v)) || (u.equals(o.v) && v.equals(o.u));
    }
}

/** Undirected weighted graph. Insertion order of nodes and edges is preserved so tie-breaks are reproducible. */
final class WeightedGraph {
    private final Map<String, List<WeightedEdge>> adjacency = new LinkedHashMap<>();
    private final List<WeightedEdge> edges = new ArrayList<>();

    WeightedGraph addNode(String node) {
        adjacency.computeIfAbsent(Objects.requireNonNull(node), k -> new ArrayList<>());
        return this;
    }

    WeightedGraph addEdge(String u, String v, long weight) {
        WeightedEdge e = new WeightedEdge(u, v, weight);
        addNode(u);
        addNode(v);
        edges.add(e);
        adjacency.get(u).add(e);
        adjacency.get(v).add(e);
        return this;
    }

    Set<String> nodes() { return Collections.unmodifiableSet(adjacency.keySet()); }
    List<WeightedEdge> edges() { return Collections.unmodifiableList(edges); }
    List<WeightedEdge> incident(String node) {
        List<WeightedEdge> out = adjacency.get(node);
        if (out == null) throw new IllegalArgumentException("unknown node: " + node);
        return Collections.unmodifiableList(out);
    }
    int nodeCount() { return adjacency.size(); }
    int edgeCount() { return edges.size(); }
}

/**
 * Disjoint-set forest with union by rank and path compression (Phase 1, Lesson 10). Amortized
 * O(alpha(n)) per operation, effectively constant. Element names are strings; the parent map is the forest.
 */
final class UnionFind {
    private final Map<String, String> parent = new HashMap<>();
    private final Map<String, Integer> rank = new HashMap<>();
    private int components = 0;

    UnionFind(Iterable<String> elements) {
        for (String e : elements) {
            parent.put(e, e);
            rank.put(e, 0);
            components++;
        }
    }

    /** Representative of e's set. Path compression: every node on the way up is re-pointed at the root. */
    String find(String e) {
        String root = e;
        while (!parent.get(root).equals(root)) root = parent.get(root);
        while (!parent.get(e).equals(root)) {
            String next = parent.get(e);
            parent.put(e, root);
            e = next;
        }
        return root;
    }

    /** Merges the sets of a and b. Returns false if they were already the same set (an edge between them would close a cycle). */
    boolean union(String a, String b) {
        String ra = find(a), rb = find(b);
        if (ra.equals(rb)) return false;
        int rka = rank.get(ra), rkb = rank.get(rb);
        if (rka < rkb) { parent.put(ra, rb); }
        else if (rka > rkb) { parent.put(rb, ra); }
        else { parent.put(rb, ra); rank.put(ra, rka + 1); }
        components--;
        return true;
    }

    boolean connected(String a, String b) { return find(a).equals(find(b)); }
    int components() { return components; }
}

/** The output of either algorithm: the chosen edges, their total weight, and how many trees the forest has. */
record SpanningForest(List<WeightedEdge> edges, long totalWeight, int componentCount) {
    /** True when the forest is a single tree, i.e. the input graph was connected. */
    boolean isTree() { return componentCount == 1; }
}

/** Kruskal's and Prim's algorithms for minimum spanning trees (forests, when the graph is disconnected). */
final class Mst {
    private Mst() {}

    /**
     * Kruskal: sort edges by weight (stable, so equal weights keep insertion order), take each edge whose
     * endpoints are in different components, skip the rest. O(E log E) for the sort; the union-find work
     * is O(E alpha(V)). Correct by the cut property: the lightest edge crossing any cut is in some MST.
     */
    static SpanningForest kruskal(WeightedGraph g) {
        List<WeightedEdge> sorted = new ArrayList<>(g.edges());
        sorted.sort(Comparator.comparingLong(WeightedEdge::weight));   // List.sort is a stable merge sort
        UnionFind uf = new UnionFind(g.nodes());
        List<WeightedEdge> chosen = new ArrayList<>();
        long total = 0;
        for (WeightedEdge e : sorted) {
            if (chosen.size() == g.nodeCount() - 1) break;             // a tree on V nodes has V-1 edges
            if (uf.union(e.u(), e.v())) {
                chosen.add(e);
                total = Math.addExact(total, e.weight());
            }
        }
        return new SpanningForest(chosen, total, uf.components());
    }

    /**
     * Prim: grow one tree from a start node, always adding the lightest edge that leaves the tree. The
     * frontier is a binary heap of candidate edges with lazy deletion: stale entries (both endpoints
     * already in the tree) are discarded when popped. O(E log E). For a disconnected graph, restarts
     * from the first unvisited node so the result is a forest comparable with Kruskal's.
     */
    static SpanningForest prim(WeightedGraph g, String start) {
        if (!g.nodes().contains(start)) throw new IllegalArgumentException("unknown start node: " + start);
        Set<String> inTree = new HashSet<>();
        List<WeightedEdge> chosen = new ArrayList<>();
        long total = 0;
        int components = 0;

        List<String> roots = new ArrayList<>();
        roots.add(start);
        for (String n : g.nodes()) if (!n.equals(start)) roots.add(n);

        for (String root : roots) {
            if (inTree.contains(root)) continue;
            components++;
            inTree.add(root);
            // (weight, insertion index) so equal weights pop in a reproducible order
            PriorityQueue<long[]> heap = new PriorityQueue<>(
                    Comparator.<long[]>comparingLong(a -> a[0]).thenComparingLong(a -> a[1]));
            List<WeightedEdge> byIndex = new ArrayList<>();
            pushFrontier(g, root, inTree, heap, byIndex);
            while (!heap.isEmpty()) {
                long[] top = heap.poll();
                WeightedEdge e = byIndex.get((int) top[1]);
                String next = inTree.contains(e.u()) ? (inTree.contains(e.v()) ? null : e.v()) : e.u();
                if (next == null) continue;                              // stale: both ends already in the tree
                inTree.add(next);
                chosen.add(e);
                total = Math.addExact(total, e.weight());
                pushFrontier(g, next, inTree, heap, byIndex);
            }
        }
        return new SpanningForest(chosen, total, components);
    }

    private static void pushFrontier(WeightedGraph g, String node, Set<String> inTree,
                                     PriorityQueue<long[]> heap, List<WeightedEdge> byIndex) {
        for (WeightedEdge e : g.incident(node)) {
            if (inTree.contains(e.other(node))) continue;
            byIndex.add(e);
            heap.add(new long[] {e.weight(), byIndex.size() - 1});
        }
    }

    /**
     * Independent check of a candidate forest: it uses only edges of g, has exactly V - components edges,
     * connects everything that g connects, and every edge is a lightest edge across the cut it closes.
     * Brute force O(E * V) per edge; for tests and reviews, not for production.
     */
    static boolean isMinimumSpanningForest(WeightedGraph g, SpanningForest f) {
        if (f.edges().size() != g.nodeCount() - f.componentCount()) return false;
        UnionFind all = new UnionFind(g.nodes());
        for (WeightedEdge e : g.edges()) all.union(e.u(), e.v());
        if (all.components() != f.componentCount()) return false;

        UnionFind forest = new UnionFind(g.nodes());
        for (WeightedEdge e : f.edges()) {
            if (!g.edges().contains(e)) return false;
            if (!forest.union(e.u(), e.v())) return false;           // a cycle: not a forest
        }
        // Cut property: removing edge e splits its tree into two sides; no edge of g across that split is lighter.
        for (WeightedEdge removed : f.edges()) {
            UnionFind without = new UnionFind(g.nodes());
            for (WeightedEdge e : f.edges()) if (e != removed) without.union(e.u(), e.v());
            for (WeightedEdge e : g.edges()) {
                boolean crosses = without.find(e.u()).equals(without.find(removed.u()))
                        != without.find(e.v()).equals(without.find(removed.u()));
                if (crosses && e.weight() < removed.weight()) return false;
            }
        }
        return true;
    }

    /**
     * Single-linkage clustering: the MST with its k - 1 heaviest edges removed is the k-clustering that
     * maximizes the minimum distance between clusters. Returns the clusters as lists of nodes in insertion order.
     */
    static List<List<String>> clusters(WeightedGraph g, int k) {
        if (k < 1) throw new IllegalArgumentException("k must be >= 1");
        SpanningForest f = kruskal(g);
        List<WeightedEdge> keep = new ArrayList<>(f.edges());
        keep.sort(Comparator.comparingLong(WeightedEdge::weight));
        int toRemove = Math.min(k - f.componentCount(), keep.size());
        if (toRemove > 0) keep = keep.subList(0, keep.size() - toRemove);
        UnionFind uf = new UnionFind(g.nodes());
        for (WeightedEdge e : keep) uf.union(e.u(), e.v());
        Map<String, List<String>> groups = new LinkedHashMap<>();
        for (String n : g.nodes()) groups.computeIfAbsent(uf.find(n), x -> new ArrayList<>()).add(n);
        return new ArrayList<>(groups.values());
    }
}

public final class Main {
    /** Eight processing sites and the monthly cost, in whole dollars, of a leased line between each pair that is feasible. */
    static WeightedGraph siteNetwork() {
        return new WeightedGraph()
                .addEdge("ashburn", "newyork", 4)
                .addEdge("ashburn", "chicago", 7)
                .addEdge("newyork", "chicago", 9)
                .addEdge("newyork", "london", 12)
                .addEdge("chicago", "dallas", 6)
                .addEdge("chicago", "london", 15)
                .addEdge("dallas", "phoenix", 5)
                .addEdge("dallas", "london", 20)
                .addEdge("phoenix", "sanjose", 3)
                .addEdge("sanjose", "chicago", 11)
                .addEdge("london", "frankfurt", 2)
                .addEdge("sanjose", "frankfurt", 25);
    }

    public static void main(String[] args) {
        WeightedGraph g = siteNetwork();
        System.out.println("== Site network: " + g.nodeCount() + " sites, " + g.edgeCount() + " feasible lines ==");
        long all = 0;
        for (WeightedEdge e : g.edges()) all += e.weight();
        System.out.println("cost of leasing every line: " + all);

        System.out.println();
        System.out.println("== Kruskal: lightest edges first, skip any that closes a cycle ==");
        SpanningForest k = Mst.kruskal(g);
        for (WeightedEdge e : k.edges()) System.out.printf("  take %-9s - %-9s %3d%n", e.u(), e.v(), e.weight());
        System.out.println("total " + k.totalWeight() + ", tree: " + k.isTree() + ", verified minimum: " + Mst.isMinimumSpanningForest(g, k));

        System.out.println();
        System.out.println("== Prim from ashburn: grow one tree, always the lightest edge leaving it ==");
        SpanningForest p = Mst.prim(g, "ashburn");
        for (WeightedEdge e : p.edges()) System.out.printf("  take %-9s - %-9s %3d%n", e.u(), e.v(), e.weight());
        System.out.println("total " + p.totalWeight() + ", same edge set as Kruskal: " + sameEdgeSet(k, p));

        System.out.println();
        System.out.println("== Disconnected: drop the four transatlantic lines ==");
        WeightedGraph split = new WeightedGraph();
        for (WeightedEdge e : g.edges()) {
            boolean transatlantic = e.sameEndpoints(new WeightedEdge("newyork", "london", 0))
                    || e.sameEndpoints(new WeightedEdge("chicago", "london", 0))
                    || e.sameEndpoints(new WeightedEdge("dallas", "london", 0))
                    || e.sameEndpoints(new WeightedEdge("sanjose", "frankfurt", 0));
            if (!transatlantic) split.addEdge(e.u(), e.v(), e.weight());
        }
        SpanningForest forest = Mst.kruskal(split);
        System.out.println("components: " + forest.componentCount() + ", edges: " + forest.edges().size() + ", total " + forest.totalWeight());

        System.out.println();
        System.out.println("== Single-linkage clustering: cut the 2 heaviest MST edges for 3 clusters ==");
        System.out.println(Mst.clusters(g, 3));
    }

    static boolean sameEdgeSet(SpanningForest a, SpanningForest b) {
        if (a.edges().size() != b.edges().size()) return false;
        for (WeightedEdge e : a.edges()) {
            boolean found = false;
            for (WeightedEdge f : b.edges()) if (e.sameEndpoints(f) && e.weight() == f.weight()) found = true;
            if (!found) return false;
        }
        return true;
    }
}
