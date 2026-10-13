// Tests for phases/02-algorithms/10-topological-sort-and-dags. Run: javac -d .out *.java && java -cp .out MainTest
import java.util.ArrayList;
import java.util.List;

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
        Digraph g = Main.settlementGraph();

        // graph basics
        check("node count", g.nodeCount() == 9);
        check("edge count", g.edgeCount() == 10);
        check("duplicate edge ignored", new Digraph().addEdge("a", "b").addEdge("a", "b").edgeCount() == 1);
        check("in-degree of net-positions is 2", g.inDegrees().get("net-positions") == 2);
        checkThrows("unknown node rejected", IllegalArgumentException.class, () -> g.successors("nope"));

        // Kahn: valid, deterministic, and exactly the order the lesson text traces
        List<String> kahn = TopoSort.kahn(g);
        check("kahn is a topological order", TopoSort.isTopologicalOrder(g, kahn));
        check("kahn order matches the hand trace", kahn.equals(List.of(
                "ingest-captures", "fetch-fx-rates", "compute-fees", "fx-fixing", "net-positions",
                "generate-payout-file", "post-ledger", "reconcile", "submit-to-bank")));
        check("kahn is deterministic", kahn.equals(TopoSort.kahn(Main.settlementGraph())));

        // DFS: also valid, usually a different order
        List<String> dfs = TopoSort.dfs(g);
        check("dfs is a topological order", TopoSort.isTopologicalOrder(g, dfs));
        check("dfs starts with a source", g.inDegrees().get(dfs.get(0)) == 0);
        check("dfs ends with a sink", g.successors(dfs.get(dfs.size() - 1)).isEmpty());

        // the validator itself
        check("validator rejects a reversed edge", !TopoSort.isTopologicalOrder(g, reversed(kahn)));
        check("validator rejects a missing node", !TopoSort.isTopologicalOrder(g, kahn.subList(0, 8)));
        check("validator rejects a duplicate node", !TopoSort.isTopologicalOrder(
                new Digraph().addEdge("a", "b"), List.of("a", "a")));

        // boundaries
        check("empty graph sorts to empty", TopoSort.kahn(new Digraph()).isEmpty() && TopoSort.dfs(new Digraph()).isEmpty());
        check("single node", TopoSort.kahn(new Digraph().addNode("x")).equals(List.of("x")));
        Digraph disconnected = new Digraph().addEdge("a", "b").addEdge("c", "d");
        check("disconnected components both ordered", TopoSort.isTopologicalOrder(disconnected, TopoSort.kahn(disconnected))
                && TopoSort.isTopologicalOrder(disconnected, TopoSort.dfs(disconnected)));

        // deep chain must not overflow the stack
        Digraph chain = new Digraph();
        for (int i = 0; i < 100_000; i++) chain.addEdge("n" + i, "n" + (i + 1));
        check("100k-node chain via dfs", TopoSort.dfs(chain).size() == 100_001);
        check("100k-node chain via kahn", TopoSort.kahn(chain).get(0).equals("n0"));

        // cycles are detected and reported, by both algorithms
        Digraph broken = Main.settlementGraph().addEdge("submit-to-bank", "reconcile");
        checkThrows("kahn throws on cycle", CycleException.class, () -> TopoSort.kahn(broken));
        checkThrows("dfs throws on cycle", CycleException.class, () -> TopoSort.dfs(broken));
        check("reported cycle is submit-to-bank -> reconcile", TopoSort.findCycle(broken).equals(List.of("submit-to-bank", "reconcile")));
        check("acyclic graph reports no cycle", TopoSort.findCycle(g).isEmpty());
        Digraph self = new Digraph().addEdge("a", "a");
        check("self-loop is a cycle of length one", TopoSort.findCycle(self).equals(List.of("a")));
        Digraph triangle = new Digraph().addEdge("x", "y").addEdge("y", "z").addEdge("z", "x").addEdge("w", "x");
        List<String> cyc = TopoSort.findCycle(triangle);
        check("triangle cycle has three nodes in edge order", cyc.size() == 3 && isCycle(triangle, cyc));
        String message = "";
        try { TopoSort.kahn(broken); } catch (CycleException e) { message = e.getMessage(); }
        check("cycle message names the loop", message.contains("submit-to-bank -> reconcile -> submit-to-bank"));

        // scheduling: levels
        DagScheduler scheduler = new DagScheduler(g, Main.settlementTasks());
        List<List<String>> levels = scheduler.levels();
        check("six levels", levels.size() == 6);
        check("level 0 = sources", levels.get(0).equals(List.of("ingest-captures", "fetch-fx-rates")));
        check("level 3 = payout file and ledger posting", levels.get(3).equals(List.of("generate-payout-file", "post-ledger")));
        check("last level = submit", levels.get(5).equals(List.of("submit-to-bank")));

        // scheduling: critical path
        Schedule s = scheduler.schedule();
        check("makespan 115", s.makespan() == 115);
        check("critical path", s.criticalPath().equals(List.of(
                "ingest-captures", "compute-fees", "net-positions", "post-ledger", "reconcile", "submit-to-bank")));
        check("critical tasks have zero slack", s.criticalPath().stream().allMatch(t -> s.slots().get(t).slack() == 0));
        check("fetch-fx-rates slack 20", s.slots().get("fetch-fx-rates").slack() == 20);
        check("fx-fixing slack 5", s.slots().get("fx-fixing").slack() == 5);
        check("generate-payout-file slack 40", s.slots().get("generate-payout-file").slack() == 40);
        check("net-positions ES 35 EF 60", s.slots().get("net-positions").earliestStart() == 35 && s.slots().get("net-positions").earliestFinish() == 60);
        check("critical path length equals makespan", s.criticalPath().stream()
                .mapToLong(t -> Main.settlementTasks().stream().filter(x -> x.name().equals(t)).findFirst().get().minutes()).sum() == 115);
        checkThrows("scheduler needs every duration", IllegalArgumentException.class,
                () -> new DagScheduler(g, List.of(new Task("ingest-captures", 1))));
        checkThrows("negative duration rejected", IllegalArgumentException.class, () -> new Task("t", -1));
        checkThrows("scheduling a cyclic graph fails", CycleException.class,
                () -> new DagScheduler(broken, Main.settlementTasks()).schedule());

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }

    static List<String> reversed(List<String> in) {
        List<String> out = new ArrayList<>(in);
        java.util.Collections.reverse(out);
        return out;
    }

    static boolean isCycle(Digraph g, List<String> cyc) {
        for (int i = 0; i < cyc.size(); i++) {
            if (!g.successors(cyc.get(i)).contains(cyc.get((i + 1) % cyc.size()))) return false;
        }
        return true;
    }
}
