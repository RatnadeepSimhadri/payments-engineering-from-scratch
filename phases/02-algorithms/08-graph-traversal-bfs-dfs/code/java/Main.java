// Lesson: phases/02-algorithms/08-graph-traversal-bfs-dfs/docs/en.md
// Graph Traversal: BFS, DFS, and Connected Components
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: CLRS ch. 22 (BFS, DFS, white/gray/black colouring); Tarjan, "Depth-First Search and Linear Graph
// Algorithms" (1972). Money design copied from phases/10-money-and-ledgers/01-representing-money.
// Java 21, standard library only.

import java.util.ArrayDeque;
import java.util.ArrayList;
import java.util.Collections;
import java.util.Deque;
import java.util.HashMap;
import java.util.LinkedHashMap;
import java.util.LinkedHashSet;
import java.util.List;
import java.util.Map;
import java.util.Objects;
import java.util.Optional;
import java.util.Set;

/** ISO 4217 currency: code and minor-unit exponent. Trimmed copy of Phase 10 Lesson 01. */
record Currency(String code, int exponent) {
    static final Currency USD = new Currency("USD", 2);
    static final Currency EUR = new Currency("EUR", 2);

    Currency {
        if (code == null || code.length() != 3) throw new IllegalArgumentException("currency code must be three letters");
        if (exponent < 0 || exponent > 4) throw new IllegalArgumentException("exponent must be 0..4");
    }

    long minorUnitsPerMajor() {
        long scale = 1;
        for (int i = 0; i < exponent; i++) scale *= 10;
        return scale;
    }
}

final class CurrencyMismatchException extends IllegalArgumentException {
    CurrencyMismatchException(Currency a, Currency b) {
        super("cannot combine " + a.code() + " with " + b.code());
    }
}

/** Exact amount of one currency as a signed count of minor units. Never a float. */
record Money(long minorUnits, Currency currency) {
    Money { Objects.requireNonNull(currency, "currency"); }

    static Money ofMinor(long minorUnits, Currency currency) { return new Money(minorUnits, currency); }

    Money plus(Money other) {
        if (!currency.equals(other.currency)) throw new CurrencyMismatchException(currency, other.currency);
        return new Money(Math.addExact(minorUnits, other.minorUnits), currency);
    }

    @Override
    public String toString() {
        long magnitude = Math.abs(minorUnits);
        long scale = currency.minorUnitsPerMajor();
        String text = Long.toString(magnitude / scale);
        if (currency.exponent() > 0) {
            String frac = Long.toString(magnitude % scale);
            text = text + "." + "0".repeat(currency.exponent() - frac.length()) + frac;
        }
        return (minorUnits < 0 ? "-" : "") + text + " " + currency.code();
    }
}

/**
 * Adjacency-list graph over string node ids. Insertion order of nodes and of each node's neighbours is
 * preserved (LinkedHashMap, ArrayList), which is what makes every traversal below deterministic.
 */
final class Graph {
    private final boolean directed;
    private final Map<String, List<String>> adjacency = new LinkedHashMap<>();
    private int edgeCount = 0;

    Graph(boolean directed) { this.directed = directed; }

    boolean isDirected() { return directed; }

    void addNode(String id) {
        Objects.requireNonNull(id, "node id");
        adjacency.computeIfAbsent(id, k -> new ArrayList<>());
    }

    /** Adds the endpoints if absent. For an undirected graph the edge is stored in both lists. */
    void addEdge(String from, String to) {
        addNode(from);
        addNode(to);
        adjacency.get(from).add(to);
        if (!directed && !from.equals(to)) adjacency.get(to).add(from);
        edgeCount++;
    }

    boolean hasNode(String id) { return adjacency.containsKey(id); }

    List<String> neighbors(String id) {
        List<String> out = adjacency.get(id);
        if (out == null) throw new IllegalArgumentException("unknown node: " + id);
        return Collections.unmodifiableList(out);
    }

    Set<String> nodes() { return Collections.unmodifiableSet(adjacency.keySet()); }
    int nodeCount() { return adjacency.size(); }
    int edgeCount() { return edgeCount; }

    /** The same nodes with every edge made two-way. Components are defined on this view. */
    Graph undirectedView() {
        if (!directed) return this;
        Graph g = new Graph(false);
        for (String n : adjacency.keySet()) g.addNode(n);
        for (Map.Entry<String, List<String>> e : adjacency.entrySet()) {
            for (String to : e.getValue()) g.addEdge(e.getKey(), to);
        }
        return g;
    }
}

/** What BFS leaves behind: hop distance and parent for every reached node, and the visit order. */
record BfsResult(Map<String, Integer> distance, Map<String, String> parent, List<String> order) {
    boolean reached(String id) { return distance.containsKey(id); }
}

/** DFS colouring for directed-cycle detection: unvisited, on the current path, finished. */
enum Color { WHITE, GRAY, BLACK }

final class Traversal {
    /** Breadth-first from source. Each node is enqueued once, when first discovered; distance = hops. */
    static BfsResult bfs(Graph g, String source) {
        if (!g.hasNode(source)) throw new IllegalArgumentException("unknown node: " + source);
        Map<String, Integer> distance = new LinkedHashMap<>();
        Map<String, String> parent = new HashMap<>();
        List<String> order = new ArrayList<>();
        Deque<String> queue = new ArrayDeque<>();
        distance.put(source, 0);
        queue.add(source);
        while (!queue.isEmpty()) {
            String u = queue.poll();
            order.add(u);
            for (String v : g.neighbors(u)) {
                if (!distance.containsKey(v)) {          // discovered: mark BEFORE enqueueing, never after dequeueing
                    distance.put(v, distance.get(u) + 1);
                    parent.put(v, u);
                    queue.add(v);
                }
            }
        }
        return new BfsResult(distance, parent, order);
    }

    /** Fewest-hops path from source to target by following BFS parents back; empty if unreachable. */
    static List<String> shortestHopPath(Graph g, String source, String target) {
        BfsResult r = bfs(g, source);
        if (!r.reached(target)) return List.of();
        List<String> path = new ArrayList<>();
        for (String at = target; at != null; at = r.parent().get(at)) path.add(0, at);
        return path;
    }

    /**
     * Depth-first preorder with an explicit stack. Neighbours are pushed in reverse so the visit order is
     * the same as the recursive version would produce, without the recursive version's stack-depth limit.
     */
    static List<String> dfsIterative(Graph g, String source) {
        if (!g.hasNode(source)) throw new IllegalArgumentException("unknown node: " + source);
        List<String> order = new ArrayList<>();
        Set<String> visited = new LinkedHashSet<>();
        Deque<String> stack = new ArrayDeque<>();
        stack.push(source);
        while (!stack.isEmpty()) {
            String u = stack.pop();
            if (!visited.add(u)) continue;               // a node may be pushed many times; it is visited once
            order.add(u);
            List<String> ns = g.neighbors(u);
            for (int i = ns.size() - 1; i >= 0; i--) {
                if (!visited.contains(ns.get(i))) stack.push(ns.get(i));
            }
        }
        return order;
    }

    /** Connected components of the undirected view, each in discovery order, components in node order. */
    static List<List<String>> connectedComponents(Graph g) {
        Graph u = g.undirectedView();
        Set<String> seen = new LinkedHashSet<>();
        List<List<String>> components = new ArrayList<>();
        for (String start : u.nodes()) {
            if (seen.contains(start)) continue;
            List<String> component = bfs(u, start).order();
            seen.addAll(component);
            components.add(component);
        }
        return components;
    }

    /**
     * Directed cycle detection by three-colour DFS, iterative. A GRAY neighbour is an ancestor on the current
     * path, so the edge closes a cycle; a BLACK neighbour is merely a finished node reached by a second route.
     * Returns the cycle as a closed walk (first node repeated at the end), or empty if the graph is a DAG.
     */
    static Optional<List<String>> findCycle(Graph g) {
        if (!g.isDirected()) throw new IllegalArgumentException("findCycle is defined for directed graphs");
        Map<String, Color> color = new HashMap<>();
        for (String n : g.nodes()) color.put(n, Color.WHITE);
        for (String root : g.nodes()) {
            if (color.get(root) != Color.WHITE) continue;
            Deque<String> path = new ArrayDeque<>();       // the GRAY nodes, root at the bottom
            Deque<Integer> next = new ArrayDeque<>();      // per frame: index of the next neighbour to examine
            color.put(root, Color.GRAY);
            path.push(root);
            next.push(0);
            while (!path.isEmpty()) {
                String u = path.peek();
                int i = next.pop();
                List<String> ns = g.neighbors(u);
                if (i < ns.size()) {
                    next.push(i + 1);
                    String v = ns.get(i);
                    Color c = color.get(v);
                    if (c == Color.GRAY) return Optional.of(closeCycle(path, v));
                    if (c == Color.WHITE) {
                        color.put(v, Color.GRAY);
                        path.push(v);
                        next.push(0);
                    }
                } else {
                    color.put(u, Color.BLACK);
                    path.pop();
                }
            }
        }
        return Optional.empty();
    }

    private static List<String> closeCycle(Deque<String> path, String v) {
        List<String> cycle = new ArrayList<>();
        for (String node : path) {                        // iterates top (most recent) to bottom
            cycle.add(0, node);
            if (node.equals(v)) break;
        }
        cycle.add(v);
        return cycle;
    }
}

/** One transfer between two accounts. The edge of a money-flow graph. */
record Transfer(String from, String to, Money amount) {}

/** Money-flow graph helpers: build from transfers, aggregate volume per edge, find rings and circular flows. */
final class FraudRings {
    static Graph fromTransfers(List<Transfer> transfers) {
        Graph g = new Graph(true);
        Set<String> edges = new LinkedHashSet<>();
        for (Transfer t : transfers) {
            if (edges.add(t.from() + "->" + t.to())) g.addEdge(t.from(), t.to());   // one edge per ordered pair
        }
        return g;
    }

    /** Total moved along each ordered pair, in insertion order. Mixed currencies are an error, not a sum. */
    static Map<String, Money> volume(List<Transfer> transfers) {
        Map<String, Money> out = new LinkedHashMap<>();
        for (Transfer t : transfers) out.merge(t.from() + "->" + t.to(), t.amount(), Money::plus);
        return out;
    }

    /** Components with at least minSize accounts: candidate rings or mule networks. */
    static List<List<String>> rings(Graph g, int minSize) {
        List<List<String>> out = new ArrayList<>();
        for (List<String> c : Traversal.connectedComponents(g)) if (c.size() >= minSize) out.add(c);
        return out;
    }

    /** A closed loop of transfers, if any: money that returns to where it started. */
    static Optional<List<String>> circularFlow(Graph g) { return Traversal.findCycle(g); }
}

public final class Main {
    public static void main(String[] args) {
        Currency usd = Currency.USD;
        List<Transfer> transfers = List.of(
                new Transfer("acct-A", "acct-B", Money.ofMinor(50000, usd)),
                new Transfer("acct-B", "acct-C", Money.ofMinor(48000, usd)),
                new Transfer("acct-C", "acct-A", Money.ofMinor(47000, usd)),   // closes the ring A -> B -> C -> A
                new Transfer("acct-A", "acct-B", Money.ofMinor(12000, usd)),   // second transfer on an existing edge
                new Transfer("acct-C", "acct-D", Money.ofMinor(1000, usd)),
                new Transfer("acct-D", "acct-E", Money.ofMinor(900, usd)),
                new Transfer("acct-A", "acct-D", Money.ofMinor(500, usd)),     // shortcut: A reaches D in one hop
                new Transfer("payroll", "acct-X", Money.ofMinor(250000, usd)),
                new Transfer("acct-X", "landlord", Money.ofMinor(180000, usd)),
                new Transfer("acct-X", "grocer", Money.ofMinor(9000, usd)));
        Graph g = FraudRings.fromTransfers(transfers);
        g.addNode("dormant");
        System.out.println("== Money-flow graph ==");
        System.out.printf("%d accounts, %d distinct edges%n", g.nodeCount(), g.edgeCount());
        FraudRings.volume(transfers).forEach((edge, total) -> System.out.println("  " + edge + " : " + total));

        System.out.println("\n== BFS from acct-A: hop distance to every reachable account ==");
        BfsResult r = Traversal.bfs(g, "acct-A");
        System.out.println("visit order : " + r.order());
        System.out.println("distances   : " + r.distance());
        System.out.println("path A -> E : " + Traversal.shortestHopPath(g, "acct-A", "acct-E"));
        System.out.println("path A -> X : " + Traversal.shortestHopPath(g, "acct-A", "acct-X") + "  (unreachable: empty)");

        System.out.println("\n== Iterative DFS preorder from acct-A ==");
        System.out.println(Traversal.dfsIterative(g, "acct-A"));

        System.out.println("\n== Connected components (direction ignored) ==");
        List<List<String>> components = Traversal.connectedComponents(g);
        for (List<String> c : components) System.out.println("  size " + c.size() + ": " + c);
        System.out.println("rings of 3+ accounts: " + FraudRings.rings(g, 3));

        System.out.println("\n== Directed cycle (three-colour DFS) ==");
        System.out.println("circular flow: " + FraudRings.circularFlow(g).orElse(List.of()));
        Graph dag = new Graph(true);
        dag.addEdge("payroll", "acct-X");
        dag.addEdge("acct-X", "landlord");
        dag.addEdge("payroll", "landlord");                 // diamond: two routes to landlord, no cycle
        System.out.println("payroll graph has cycle: " + Traversal.findCycle(dag).isPresent());

        System.out.println("\n== 100,000-hop chain: iterative DFS does not overflow the stack ==");
        Graph chain = new Graph(true);
        for (int i = 0; i < 100_000; i++) chain.addEdge("n" + i, "n" + (i + 1));
        System.out.println("visited " + Traversal.dfsIterative(chain, "n0").size() + " nodes");
    }
}
