// Lesson: phases/02-algorithms/10-topological-sort-and-dags/docs/en.md
// Topological Sort and DAG Scheduling
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: Kahn, "Topological sorting of large networks" (CACM 1962); Tarjan, "Depth-first search and
// linear graph algorithms" (SIAM J. Comput. 1972); Kelley & Walker, "Critical-path planning and scheduling" (1959).
// Java 21, standard library only.

import java.util.ArrayDeque;
import java.util.ArrayList;
import java.util.Collections;
import java.util.Deque;
import java.util.HashMap;
import java.util.HashSet;
import java.util.LinkedHashMap;
import java.util.LinkedHashSet;
import java.util.List;
import java.util.Map;
import java.util.Objects;
import java.util.Set;

/**
 * A directed graph over string-named nodes. Insertion order of nodes and edges is preserved so that
 * every algorithm below produces the same answer on every run: a scheduler whose output changes
 * between runs cannot be audited.
 */
final class Digraph {
    private final Map<String, List<String>> adjacency = new LinkedHashMap<>();

    Digraph addNode(String node) {
        Objects.requireNonNull(node, "node");
        adjacency.computeIfAbsent(node, k -> new ArrayList<>());
        return this;
    }

    /** Adds the edge from -> to, meaning "from must finish before to can start". */
    Digraph addEdge(String from, String to) {
        addNode(from);
        addNode(to);
        List<String> out = adjacency.get(from);
        if (!out.contains(to)) out.add(to);
        return this;
    }

    Set<String> nodes() { return Collections.unmodifiableSet(adjacency.keySet()); }

    List<String> successors(String node) {
        List<String> out = adjacency.get(node);
        if (out == null) throw new IllegalArgumentException("unknown node: " + node);
        return Collections.unmodifiableList(out);
    }

    int nodeCount() { return adjacency.size(); }

    int edgeCount() {
        int n = 0;
        for (List<String> out : adjacency.values()) n += out.size();
        return n;
    }

    Map<String, Integer> inDegrees() {
        Map<String, Integer> in = new LinkedHashMap<>();
        for (String node : adjacency.keySet()) in.put(node, 0);
        for (List<String> out : adjacency.values()) {
            for (String to : out) in.merge(to, 1, Integer::sum);
        }
        return in;
    }
}

/** Thrown when a graph that must be a DAG contains a cycle. Carries the cycle so the caller can report it. */
final class CycleException extends IllegalStateException {
    private final List<String> cycle;

    CycleException(List<String> cycle) {
        super("cycle detected: " + String.join(" -> ", cycle) + " -> " + cycle.get(0));
        this.cycle = List.copyOf(cycle);
    }

    /** The nodes of one cycle, in edge order, without repeating the first node at the end. */
    List<String> cycle() { return cycle; }
}

/** Topological orderings of a DAG, with cycle detection that reports the offending cycle. */
final class TopoSort {
    private TopoSort() {}

    /**
     * Kahn's algorithm: repeatedly emit a node with in-degree zero and decrement its successors.
     * Ties are broken by node insertion order (the ready queue is FIFO), so the output is deterministic.
     * O(V + E) time, O(V) extra space. Throws CycleException if some node is never emitted.
     */
    static List<String> kahn(Digraph g) {
        Map<String, Integer> inDegree = g.inDegrees();
        Deque<String> ready = new ArrayDeque<>();
        for (Map.Entry<String, Integer> e : inDegree.entrySet()) {
            if (e.getValue() == 0) ready.add(e.getKey());
        }
        List<String> order = new ArrayList<>(g.nodeCount());
        while (!ready.isEmpty()) {
            String node = ready.poll();
            order.add(node);
            for (String next : g.successors(node)) {
                int remaining = inDegree.merge(next, -1, Integer::sum);
                if (remaining == 0) ready.add(next);
            }
        }
        if (order.size() != g.nodeCount()) {
            // Every node still holding in-degree > 0 lies on or downstream of a cycle; find one to report.
            throw new CycleException(findCycle(g));
        }
        return order;
    }

    /**
     * Depth-first topological sort: a node is finished only after all of its successors are finished,
     * so the reverse of finishing order is a topological order. Iterative, so a 100k-node chain does not
     * overflow the call stack. Throws CycleException with the back-edge cycle if one is found.
     */
    static List<String> dfs(Digraph g) {
        Map<String, Color> color = new HashMap<>();
        for (String n : g.nodes()) color.put(n, Color.WHITE);
        Map<String, String> parent = new HashMap<>();
        List<String> finished = new ArrayList<>(g.nodeCount());

        for (String root : g.nodes()) {
            if (color.get(root) != Color.WHITE) continue;
            Deque<Frame> stack = new ArrayDeque<>();
            stack.push(new Frame(root));
            color.put(root, Color.GRAY);
            while (!stack.isEmpty()) {
                Frame top = stack.peek();
                List<String> succ = g.successors(top.node);
                if (top.next < succ.size()) {
                    String child = succ.get(top.next++);
                    Color c = color.get(child);
                    if (c == Color.WHITE) {
                        color.put(child, Color.GRAY);
                        parent.put(child, top.node);
                        stack.push(new Frame(child));
                    } else if (c == Color.GRAY) {
                        throw new CycleException(walkBack(top.node, child, parent));
                    }
                    // BLACK: already finished, a cross or forward edge, safe to ignore
                } else {
                    color.put(top.node, Color.BLACK);
                    finished.add(top.node);
                    stack.pop();
                }
            }
        }
        Collections.reverse(finished);
        return finished;
    }

    /** Returns one cycle of g in edge order, or an empty list if g is acyclic. */
    static List<String> findCycle(Digraph g) {
        try {
            dfs(g);
            return List.of();
        } catch (CycleException e) {
            return e.cycle();
        }
    }

    /** From the GRAY node `from` back up the DFS tree to `to`, then reverse into edge order. */
    private static List<String> walkBack(String from, String to, Map<String, String> parent) {
        List<String> path = new ArrayList<>();
        String cur = from;
        while (!cur.equals(to)) {
            path.add(cur);
            cur = parent.get(cur);
        }
        path.add(to);
        Collections.reverse(path);
        return path;
    }

    /** True if `order` lists every node of g exactly once and every edge points forward. */
    static boolean isTopologicalOrder(Digraph g, List<String> order) {
        if (order.size() != g.nodeCount() || new HashSet<>(order).size() != order.size()) return false;
        Map<String, Integer> pos = new HashMap<>();
        for (int i = 0; i < order.size(); i++) pos.put(order.get(i), i);
        for (String from : g.nodes()) {
            Integer pf = pos.get(from);
            if (pf == null) return false;
            for (String to : g.successors(from)) {
                if (pos.get(to) <= pf) return false;
            }
        }
        return true;
    }

    private enum Color { WHITE, GRAY, BLACK }

    private static final class Frame {
        final String node;
        int next = 0;
        Frame(String node) { this.node = node; }
    }
}

/** A task with a duration in whole time units (minutes, here). Durations are integers, like money. */
record Task(String name, long minutes) {
    Task {
        Objects.requireNonNull(name, "name");
        if (minutes < 0) throw new IllegalArgumentException("duration must be non-negative: " + minutes);
    }
}

/** One task's schedule: earliest start, earliest finish, latest start without delaying the end, and slack. */
record Slot(String task, long earliestStart, long earliestFinish, long latestStart, long slack) {
    boolean isCritical() { return slack == 0; }
}

/** The result of scheduling a DAG of tasks: per-task slots, the makespan, and the critical path. */
record Schedule(Map<String, Slot> slots, long makespan, List<String> criticalPath) {}

/**
 * DAG scheduling: parallel levels (which jobs can run together) and the critical path (the longest
 * chain of durations, which is the shortest possible total time no matter how many workers you add).
 */
final class DagScheduler {
    private final Digraph graph;
    private final Map<String, Task> tasks = new LinkedHashMap<>();

    DagScheduler(Digraph graph, List<Task> taskList) {
        this.graph = graph;
        for (Task t : taskList) tasks.put(t.name(), t);
        for (String n : graph.nodes()) {
            if (!tasks.containsKey(n)) throw new IllegalArgumentException("no duration for task " + n);
        }
    }

    /**
     * Groups tasks into levels: level 0 has no prerequisites; level k's prerequisites are all at levels < k.
     * Every task in one level may run concurrently. The number of levels is the length of the longest
     * chain of edges, which is the minimum number of sequential rounds.
     */
    List<List<String>> levels() {
        Map<String, Integer> depth = new HashMap<>();
        for (String n : TopoSort.kahn(graph)) {
            int d = 0;
            for (String from : graph.nodes()) {
                if (graph.successors(from).contains(n)) d = Math.max(d, depth.get(from) + 1);
            }
            depth.put(n, d);
        }
        int maxDepth = depth.values().stream().mapToInt(Integer::intValue).max().orElse(-1);
        List<List<String>> out = new ArrayList<>();
        for (int i = 0; i <= maxDepth; i++) out.add(new ArrayList<>());
        for (String n : graph.nodes()) out.get(depth.get(n)).add(n);
        return out;
    }

    /**
     * Forward pass in topological order computes earliest start/finish; backward pass in reverse order
     * computes latest start; slack = latestStart - earliestStart. Tasks with zero slack form the
     * critical path. O(V + E).
     */
    Schedule schedule() {
        List<String> order = TopoSort.kahn(graph);
        Map<String, Long> earliestStart = new HashMap<>();
        Map<String, Long> earliestFinish = new HashMap<>();
        Map<String, String> criticalPredecessor = new HashMap<>();
        for (String n : order) {
            long start = -1;
            for (String from : predecessors(n)) {
                long f = earliestFinish.get(from);
                if (f > start) {                     // strict: first predecessor wins ties, deterministically
                    start = f;
                    criticalPredecessor.put(n, from);
                }
            }
            if (start < 0) start = 0;                // no predecessors: can start at time zero
            earliestStart.put(n, start);
            earliestFinish.put(n, Math.addExact(start, tasks.get(n).minutes()));
        }
        long makespan = earliestFinish.values().stream().mapToLong(Long::longValue).max().orElse(0);

        Map<String, Long> latestFinish = new HashMap<>();
        for (int i = order.size() - 1; i >= 0; i--) {
            String n = order.get(i);
            long lf = makespan;
            for (String to : graph.successors(n)) {
                lf = Math.min(lf, latestFinish.get(to) - tasks.get(to).minutes());
            }
            latestFinish.put(n, lf);
        }

        Map<String, Slot> slots = new LinkedHashMap<>();
        for (String n : graph.nodes()) {
            long es = earliestStart.get(n);
            long ls = latestFinish.get(n) - tasks.get(n).minutes();
            slots.put(n, new Slot(n, es, earliestFinish.get(n), ls, ls - es));
        }

        // Walk back from the latest-finishing task along critical predecessors.
        String end = null;
        for (String n : order) {
            if (earliestFinish.get(n) == makespan && (end == null)) end = n;
        }
        LinkedHashSet<String> path = new LinkedHashSet<>();
        for (String cur = end; cur != null; cur = criticalPredecessor.get(cur)) path.add(cur);
        List<String> critical = new ArrayList<>(path);
        Collections.reverse(critical);
        return new Schedule(slots, makespan, critical);
    }

    private List<String> predecessors(String node) {
        List<String> preds = new ArrayList<>();
        for (String from : graph.nodes()) {
            if (graph.successors(from).contains(node)) preds.add(from);
        }
        return preds;
    }
}

public final class Main {
    /** The end-of-day settlement batch used throughout the lesson text. */
    static Digraph settlementGraph() {
        return new Digraph()
                .addEdge("ingest-captures", "compute-fees")
                .addEdge("ingest-captures", "fx-fixing")
                .addEdge("fetch-fx-rates", "fx-fixing")
                .addEdge("compute-fees", "net-positions")
                .addEdge("fx-fixing", "net-positions")
                .addEdge("net-positions", "generate-payout-file")
                .addEdge("net-positions", "post-ledger")
                .addEdge("post-ledger", "reconcile")
                .addEdge("generate-payout-file", "submit-to-bank")
                .addEdge("reconcile", "submit-to-bank");
    }

    static List<Task> settlementTasks() {
        return List.of(
                new Task("ingest-captures", 20),
                new Task("fetch-fx-rates", 5),
                new Task("compute-fees", 15),
                new Task("fx-fixing", 10),
                new Task("net-positions", 25),
                new Task("generate-payout-file", 10),
                new Task("post-ledger", 30),
                new Task("reconcile", 20),
                new Task("submit-to-bank", 5));
    }

    public static void main(String[] args) {
        Digraph g = settlementGraph();
        System.out.println("== Settlement batch: " + g.nodeCount() + " jobs, " + g.edgeCount() + " dependencies ==");
        System.out.println("Kahn order: " + TopoSort.kahn(g));
        System.out.println("DFS order:  " + TopoSort.dfs(g));
        System.out.println("both valid: " + TopoSort.isTopologicalOrder(g, TopoSort.kahn(g))
                + " " + TopoSort.isTopologicalOrder(g, TopoSort.dfs(g)));

        System.out.println();
        System.out.println("== Parallel levels (jobs in one level can run together) ==");
        DagScheduler scheduler = new DagScheduler(g, settlementTasks());
        List<List<String>> levels = scheduler.levels();
        for (int i = 0; i < levels.size(); i++) System.out.println("level " + i + ": " + levels.get(i));

        System.out.println();
        System.out.println("== Critical path ==");
        Schedule s = scheduler.schedule();
        System.out.printf("%-22s %5s %5s %5s %5s%n", "task", "ES", "EF", "LS", "slack");
        for (Slot slot : s.slots().values()) {
            System.out.printf("%-22s %5d %5d %5d %5d%s%n", slot.task(), slot.earliestStart(), slot.earliestFinish(),
                    slot.latestStart(), slot.slack(), slot.isCritical() ? "  <- critical" : "");
        }
        System.out.println("makespan = " + s.makespan() + " minutes; critical path = " + s.criticalPath());

        System.out.println();
        System.out.println("== A cycle: reconcile now waits for submit-to-bank ==");
        Digraph broken = settlementGraph().addEdge("submit-to-bank", "reconcile");
        try {
            TopoSort.kahn(broken);
        } catch (CycleException e) {
            System.out.println("rejected: " + e.getMessage());
        }
    }
}
