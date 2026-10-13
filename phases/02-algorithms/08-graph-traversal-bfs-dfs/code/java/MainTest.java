// Tests for phases/02-algorithms/08-graph-traversal-bfs-dfs. Run: javac -d .out *.java && java -cp .out MainTest
import java.util.ArrayList;
import java.util.List;
import java.util.Map;
import java.util.Optional;

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

    /** The demo's money-flow graph: ring A->B->C->A, tail C->D->E, shortcut A->D, separate payroll cluster, one isolated node. */
    static Graph flowGraph() {
        Graph g = new Graph(true);
        g.addEdge("A", "B");
        g.addEdge("B", "C");
        g.addEdge("C", "A");
        g.addEdge("C", "D");
        g.addEdge("D", "E");
        g.addEdge("A", "D");
        g.addEdge("payroll", "X");
        g.addEdge("X", "landlord");
        g.addEdge("X", "grocer");
        g.addNode("dormant");
        return g;
    }

    public static void main(String[] args) {
        Graph g = flowGraph();
        Currency USD = Currency.USD;

        // graph basics
        check("node count includes isolated node", g.nodeCount() == 10);
        check("edge count", g.edgeCount() == 9);
        check("neighbors keep insertion order", g.neighbors("A").equals(List.of("B", "D")));
        checkThrows("neighbors of unknown node throws", IllegalArgumentException.class, () -> g.neighbors("nope"));
        Graph und = new Graph(false);
        und.addEdge("p", "q");
        check("undirected edge is visible from both ends", und.neighbors("q").equals(List.of("p")) && und.edgeCount() == 1);
        check("undirected view of directed graph has reverse edges", g.undirectedView().neighbors("B").equals(List.of("A", "C")));

        // BFS: distances, parents, order, paths
        BfsResult r = Traversal.bfs(g, "A");
        check("bfs distance to self is 0", r.distance().get("A") == 0);
        check("bfs distances are hop counts", r.distance().get("B") == 1 && r.distance().get("D") == 1 && r.distance().get("C") == 2 && r.distance().get("E") == 2);
        check("bfs visit order is level by level", r.order().equals(List.of("A", "B", "D", "C", "E")));
        check("bfs does not reach the other cluster", !r.reached("X") && !r.reached("dormant"));
        check("bfs parent of E is D via the shortcut", r.parent().get("E").equals("D") && r.parent().get("D").equals("A"));
        check("shortest hop path A -> E uses the shortcut", Traversal.shortestHopPath(g, "A", "E").equals(List.of("A", "D", "E")));
        check("shortest hop path to self", Traversal.shortestHopPath(g, "A", "A").equals(List.of("A")));
        check("unreachable path is empty", Traversal.shortestHopPath(g, "A", "X").isEmpty());
        check("path length equals distance + 1", Traversal.shortestHopPath(g, "A", "C").size() == r.distance().get("C") + 1);
        checkThrows("bfs from unknown node throws", IllegalArgumentException.class, () -> Traversal.bfs(g, "nope"));

        // DFS
        check("dfs preorder follows first neighbour deep", Traversal.dfsIterative(g, "A").equals(List.of("A", "B", "C", "D", "E")));
        check("dfs visits each node once on a cycle", Traversal.dfsIterative(g, "A").size() == 5);
        check("dfs on isolated node", Traversal.dfsIterative(g, "dormant").equals(List.of("dormant")));
        Graph chain = new Graph(true);
        for (int i = 0; i < 100_000; i++) chain.addEdge("n" + i, "n" + (i + 1));
        check("iterative dfs survives a 100,000-hop chain", Traversal.dfsIterative(chain, "n0").size() == 100_001);
        check("bfs distance along the chain", Traversal.bfs(chain, "n0").distance().get("n100000") == 100_000);

        // connected components
        List<List<String>> comps = Traversal.connectedComponents(g);
        check("three components", comps.size() == 3);
        check("first component is the ring plus its tail", comps.get(0).equals(List.of("A", "B", "D", "C", "E")));
        check("second component is the payroll cluster", comps.get(1).equals(List.of("payroll", "X", "landlord", "grocer")));
        check("isolated node is its own component", comps.get(2).equals(List.of("dormant")));
        check("components ignore edge direction", comps.get(1).contains("landlord"));
        check("rings with minSize 3 excludes the singleton", FraudRings.rings(g, 3).size() == 2);
        check("rings with minSize 5 keeps only the big one", FraudRings.rings(g, 5).size() == 1);

        // cycle detection
        Optional<List<String>> cycle = Traversal.findCycle(g);
        check("cycle found in the flow graph", cycle.isPresent());
        check("cycle is the closed walk A->B->C->A", cycle.get().equals(List.of("A", "B", "C", "A")));
        Graph dag = new Graph(true);
        dag.addEdge("payroll", "X");
        dag.addEdge("X", "landlord");
        dag.addEdge("payroll", "landlord");
        check("diamond (two routes to one node) is not a cycle", Traversal.findCycle(dag).isEmpty());
        Graph self = new Graph(true);
        self.addEdge("s", "s");
        check("self-loop is a cycle of length 1", Traversal.findCycle(self).get().equals(List.of("s", "s")));
        Graph later = new Graph(true);
        later.addEdge("a", "b");
        later.addEdge("c", "d");
        later.addEdge("d", "c");
        check("cycle in a later component is found", Traversal.findCycle(later).get().equals(List.of("c", "d", "c")));
        Graph two = new Graph(true);
        two.addEdge("p", "q");
        two.addEdge("q", "r");
        two.addEdge("r", "q");
        check("cycle not through the root is reported from its own first node", Traversal.findCycle(two).get().equals(List.of("q", "r", "q")));
        checkThrows("findCycle rejects undirected graphs", IllegalArgumentException.class, () -> Traversal.findCycle(und));

        // transfers, volume, money
        List<Transfer> transfers = List.of(
                new Transfer("A", "B", Money.ofMinor(50000, USD)),
                new Transfer("A", "B", Money.ofMinor(12000, USD)),
                new Transfer("B", "A", Money.ofMinor(100, USD)));
        Graph fromT = FraudRings.fromTransfers(transfers);
        check("fromTransfers dedups parallel edges", fromT.edgeCount() == 2 && fromT.neighbors("A").equals(List.of("B")));
        Map<String, Money> vol = FraudRings.volume(transfers);
        check("volume sums parallel transfers exactly", vol.get("A->B").equals(Money.ofMinor(62000, USD)));
        check("volume keeps the reverse edge separate", vol.get("B->A").equals(Money.ofMinor(100, USD)));
        checkThrows("volume rejects mixed currencies", CurrencyMismatchException.class, () -> FraudRings.volume(List.of(
                new Transfer("A", "B", Money.ofMinor(1, USD)), new Transfer("A", "B", Money.ofMinor(1, Currency.EUR)))));
        check("circular flow on the transfer graph", FraudRings.circularFlow(fromT).get().equals(List.of("A", "B", "A")));
        check("money toString", Money.ofMinor(62000, USD).toString().equals("620.00 USD"));

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }
}
