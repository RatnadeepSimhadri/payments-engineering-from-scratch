# Topological Sort and DAG Scheduling

> Settlement dependencies and build graphs.

**Type:** Build
**Languages:** Java, Python
**Prerequisites:** Phase 2, Lesson 08 (Graph Traversal: BFS, DFS, and Connected Components), Phase 1, Lesson 05 (Stacks, Queues, and Deques)
**Time:** ~60 minutes

## Learning Objectives

- Define a topological order, state the one invariant every such order satisfies, and explain why it exists if and only if the graph has no directed cycle
- Build Kahn's algorithm (in-degree peeling) and the DFS finishing-order algorithm, and show on one graph why they produce different valid orders
- Detect a cycle during either algorithm and report the actual nodes of the cycle, not just "a cycle exists"
- Group a dependency graph into parallel levels and compute the minimum number of sequential rounds
- Compute earliest start, latest start, slack, and the critical path of a DAG of timed tasks with a forward and a backward pass

## The Problem

An acquirer runs an end-of-day settlement batch. It is nine jobs, each written by a different team over six years: ingest the day's captures, fetch the FX fixing rates, compute merchant fees, apply the FX fixing, net merchant positions, generate the payout file, post the ledger entries, reconcile the ledger against the netted positions, and submit the payout file to the bank. The orchestration is a cron table. `compute-fees` runs at 23:10 because `ingest-captures` "always finishes by 23:05". `net-positions` runs at 23:40 because fees "take about fifteen minutes". `submit-to-bank` runs at 01:00 because the bank's cutoff is 01:30 and nothing has ever been late.

On the last business day of the quarter, capture volume is 2.4 times normal. `ingest-captures` finishes at 23:31. `compute-fees` started at 23:10 on a partial table and computed fees for 40% of the day. `net-positions` ran on those fees. `generate-payout-file` produced a file paying 1,900 merchants 40% of what they were owed, and `submit-to-bank` sent it to the bank at 01:00. `reconcile` ran at 00:50, found the ledger did not match the netted positions, paged someone, and the page was acknowledged at 01:20. The money had already left. The next three days were spent issuing supplementary payouts, explaining the shortfall to merchants, and reconstructing which fee rows were computed against complete data.

Nothing in that batch was wrong in isolation. Each job did what it was told. The failure was that the dependencies between the jobs lived in people's heads and in clock times, not in the system. `compute-fees` depends on `ingest-captures`; `submit-to-bank` depends on `reconcile`. Those are edges in a directed graph, and the question "in what order may these run so that every job starts only after everything it depends on has finished" has a precise name: a topological order of that graph. A scheduler that computes the order from the edges, instead of guessing it from clock times, cannot run `compute-fees` on partial data no matter how big the quarter-end is. The same scheduler can tell you which jobs may run in parallel, how long the batch must take at minimum, and which job to speed up if you want the batch to finish sooner. And if someone adds an edge that makes `reconcile` wait for `submit-to-bank` while `submit-to-bank` waits for `reconcile`, the scheduler must refuse to run and say exactly which edges form the loop, because a batch that deadlocks silently at 01:00 is worse than one that fails loudly at deploy time.

## The Concept

### Definition and invariant

A directed graph `G = (V, E)` has a **topological order** if its nodes can be listed `v1, v2, ..., vn` so that for every edge `(u -> w)`, `u` appears before `w`. That is the entire invariant: **every edge points forward in the list.** The order is a linear extension of the partial order the edges define; where the edges say nothing about two nodes, the order may put them either way.

Two facts follow immediately:

- **A topological order exists if and only if the graph has no directed cycle.** If there were a cycle `a -> b -> c -> a`, then `a` must precede `b`, `b` must precede `c`, and `c` must precede `a`, which is impossible. Conversely, every finite DAG has at least one node with no incoming edges (follow edges backward; you cannot do so forever without repeating, and repeating means a cycle), and removing it leaves a smaller DAG, so you can peel the whole graph. That peeling argument is Kahn's algorithm.
- **The order is usually not unique.** In the settlement graph, `ingest-captures` and `fetch-fx-rates` have no edge between them, so either may go first. The number of valid orders can be exponential in the number of nodes (for a graph with no edges at all it is `n!`). An algorithm chooses one; a scheduler that wants reproducible runs must choose the same one every time, which means the tie-break rule is part of the design, not an accident.

```mermaid
flowchart LR
  IC[ingest-captures 20m] --> CF[compute-fees 15m]
  IC --> FX[fx-fixing 10m]
  FR[fetch-fx-rates 5m] --> FX
  CF --> NP[net-positions 25m]
  FX --> NP
  NP --> GP[generate-payout-file 10m]
  NP --> PL[post-ledger 30m]
  PL --> RC[reconcile 20m]
  GP --> SB[submit-to-bank 5m]
  RC --> SB
```

Nine jobs, ten edges, two sources (`ingest-captures`, `fetch-fx-rates`), one sink (`submit-to-bank`). The durations in minutes are used by the scheduling half of the lesson.

### Kahn's algorithm: peel the sources

Kahn (1962) is the peeling argument made into a procedure:

1. Compute the in-degree of every node (how many edges point at it).
2. Put every node with in-degree zero into a ready queue.
3. Repeatedly take a node from the queue, emit it, and for each of its successors subtract one from the in-degree. Any successor that reaches zero goes into the queue.
4. If every node was emitted, the emitted sequence is a topological order. If some were not, those nodes all still have in-degree greater than zero, which means each of them is waiting on another unfinished node, which means there is a cycle among them.

Trace on the settlement graph. Nodes are listed in the order they were added (`ingest-captures`, `compute-fees`, `fx-fixing`, `fetch-fx-rates`, `net-positions`, `generate-payout-file`, `post-ledger`, `reconcile`, `submit-to-bank`), and the ready queue is first-in, first-out:

```text
in-degree: ingest-captures 0, compute-fees 1, fx-fixing 2, fetch-fx-rates 0, net-positions 2,
           generate-payout-file 1, post-ledger 1, reconcile 1, submit-to-bank 2
ready = [ingest-captures, fetch-fx-rates]

pop ingest-captures   -> compute-fees 1->0 (ready), fx-fixing 2->1
pop fetch-fx-rates    -> fx-fixing 1->0 (ready)
pop compute-fees      -> net-positions 2->1
pop fx-fixing         -> net-positions 1->0 (ready)
pop net-positions     -> generate-payout-file 1->0 (ready), post-ledger 1->0 (ready)
pop generate-payout-file -> submit-to-bank 2->1
pop post-ledger       -> reconcile 1->0 (ready)
pop reconcile         -> submit-to-bank 1->0 (ready)
pop submit-to-bank    -> nothing

order = [ingest-captures, fetch-fx-rates, compute-fees, fx-fixing, net-positions,
         generate-payout-file, post-ledger, reconcile, submit-to-bank]      9 of 9 emitted
```

Every node is enqueued once and dequeued once, and every edge is examined once when its source is dequeued, so the running time is `O(V + E)` and the extra space is the in-degree map plus the queue, `O(V)`.

The tie-break is the FIFO queue over nodes in insertion order. Swap the queue for a priority queue keyed on node name and you get the lexicographically smallest topological order; key it on a priority field and you get "most urgent ready job first". The algorithm does not care what the queue is; the determinism of your scheduler depends entirely on it.

### DFS finishing order: finish your dependents first

The second algorithm comes from Tarjan (1972). Run a depth-first search from every unvisited node. A node is *finished* when the search has returned from all of its successors. Record nodes in finishing order; the reverse of that list is a topological order.

Why it works: when `u` finishes, every node reachable from `u` has already finished (the DFS went there first and came back). So every successor of `u` is earlier in finishing order than `u`, which means later than `u` in the reversed list. Every edge points forward.

Cycle detection falls out of the three-colour scheme. A node is WHITE before it is visited, GRAY while it is on the recursion stack (visited, not finished), and BLACK when finished. An edge from the current node to a GRAY node is a **back edge**: it points at an ancestor that is still open, so following it would return to where you already are. That is a cycle, and the cycle is exactly the stack from the GRAY node down to the current node. An edge to a BLACK node is a cross or forward edge and is harmless.

On the settlement graph, DFS from `ingest-captures` (the first node) with successors visited in insertion order produces finishing order `submit-to-bank, generate-payout-file, reconcile, post-ledger, net-positions, compute-fees, fx-fixing, ingest-captures, fetch-fx-rates`; reversed: `fetch-fx-rates, ingest-captures, fx-fixing, compute-fees, net-positions, post-ledger, reconcile, generate-payout-file, submit-to-bank`. Different from Kahn's order, equally valid. The implementation in this lesson is iterative with an explicit stack of `(node, next successor index)` frames, because a 100,000-job chain is a realistic dependency graph in a build system and a recursive DFS on it overflows the Java call stack at a few thousand frames.

| | Kahn (in-degree) | DFS (finishing order) |
|---|---|---|
| Time, space | `O(V + E)`, `O(V)` | `O(V + E)`, `O(V)` |
| Natural tie-break | the ready queue: FIFO, priority, lexicographic | successor order within each node |
| Cycle detection | leftover nodes after the queue drains | back edge to a GRAY node |
| Cycle report | needs a second pass over the leftover nodes | free: the stack between the GRAY node and the current node |
| Streaming use | yes: emit a node as soon as it is ready; this is how a parallel scheduler runs | no: nothing is emitted until the deepest node finishes |
| Also gives you | parallel levels (every node dequeued before the queue refills forms a round) | reverse postorder, strongly connected components (Tarjan), the same traversal you used in Lesson 08 |

Use Kahn when you are scheduling (you want to dispatch jobs as they become ready); use DFS when you need the cycle reported or when you already have a DFS for another reason.

### Reporting the cycle, not just detecting it

"Cycle detected" is a useless error at 23:00 with nine jobs and ten edges, and a hopeless one with 4,000 jobs. The cycle itself is cheap to produce. In the DFS, when the current node `u` has a successor `w` that is GRAY, walk the `parent` map from `u` back to `w`, and reverse: that is the cycle `w -> ... -> u -> w`. Kahn's algorithm does not naturally know the cycle (it only knows which nodes were never ready), so this implementation runs the DFS on the graph to find one to report. The leftover nodes after Kahn are every node on a cycle *plus everything downstream of a cycle*, which is more than the cycle; reporting all of them is how you get an error message that lists 3,800 of your 4,000 jobs.

Add one edge to the settlement graph, `submit-to-bank -> reconcile` (someone decided reconciliation should confirm the submission). Kahn now emits seven nodes and stops with `reconcile` and `submit-to-bank` both at in-degree 1. The DFS reports `cycle detected: submit-to-bank -> reconcile -> submit-to-bank`. That message tells the author of the change exactly which two edges to argue about.

### Scheduling on a DAG: levels and the critical path

A topological order is a serial schedule. A batch system has more than one worker, so the interesting questions are: what can run at the same time, how long must the batch take at minimum, and where is the slack?

**Levels.** Define the level of a node as 0 if it has no predecessors and otherwise one more than the maximum level of its predecessors. Every node in a level has all of its prerequisites in strictly lower levels, so a level can run as one parallel round. The number of levels is the length (in edges) of the longest path plus one, and it is the minimum number of sequential rounds if every job took unit time. The settlement graph has six levels: `{ingest-captures, fetch-fx-rates}`, `{compute-fees, fx-fixing}`, `{net-positions}`, `{generate-payout-file, post-ledger}`, `{reconcile}`, `{submit-to-bank}`.

**Critical path.** Jobs do not take unit time. Give each one a duration and ask for the earliest time each can start. This is the longest-path problem on a DAG, and unlike longest path on a general graph (NP-hard), on a DAG it is one pass in topological order. The method is Kelley and Walker's critical path method (1959), the thing project managers draw as Gantt charts:

- **Forward pass**, in topological order: `earliestStart(v) = max over predecessors u of earliestFinish(u)`, or 0 for a source; `earliestFinish(v) = earliestStart(v) + duration(v)`. The **makespan** is the maximum `earliestFinish`.
- **Backward pass**, in reverse topological order: `latestFinish(v) = min over successors w of latestStart(w)`, or the makespan for a sink; `latestStart(v) = latestFinish(v) - duration(v)`.
- **Slack** `= latestStart - earliestStart`. A job with zero slack cannot be delayed at all without delaying the whole batch. The zero-slack jobs form the **critical path**, and the sum of their durations equals the makespan.

Worked by hand on the settlement graph, durations in minutes:

```text
forward pass (earliest start - earliest finish)
  ingest-captures       0 - 20
  fetch-fx-rates        0 - 5
  compute-fees         20 - 35     waits for ingest-captures
  fx-fixing            20 - 30     max(ingest 20, fetch 5) = 20
  net-positions        35 - 60     max(fees 35, fx 30) = 35
  generate-payout-file 60 - 70
  post-ledger          60 - 90
  reconcile            90 - 110
  submit-to-bank      110 - 115    max(payout 70, reconcile 110) = 110
makespan = 115

backward pass (latest start)
  submit-to-bank      115 - 5 = 110
  reconcile           110 - 20 = 90
  generate-payout-file 110 - 10 = 100        slack 100 - 60 = 40
  post-ledger          90 - 30 = 60
  net-positions        min(100, 60) - 25 = 35
  fx-fixing            35 - 10 = 25           slack 25 - 20 = 5
  compute-fees         35 - 15 = 20
  fetch-fx-rates       25 - 5 = 20            slack 20 - 0 = 20
  ingest-captures      min(20, 25) - 20 = 0

critical path: ingest-captures -> compute-fees -> net-positions -> post-ledger -> reconcile -> submit-to-bank
20 + 15 + 25 + 30 + 20 + 5 = 115
```

Read the slack column as an engineering budget. Making `fetch-fx-rates` ten times faster saves nothing. Making `generate-payout-file` slower by half an hour costs nothing. Shaving ten minutes off `post-ledger` finishes the batch at 01:05 instead of 01:15. The cron table version of this batch encoded none of that; the DAG version computes it in `O(V + E)`.

### What the structure does not do

A topological order is a statement about dependencies, nothing more. It does not know how many workers you have, so the levels are an upper bound on parallelism, not a schedule for a two-worker pool (that problem, list scheduling with resource constraints, is NP-hard in general and solved with heuristics). It does not know that a job can fail: retrying `compute-fees` after a failure and re-running its downstream jobs is the orchestrator's job, and the topological order tells it what "downstream" means. It does not know about data: a DAG can tell you `compute-fees` runs after `ingest-captures`; it cannot tell you `ingest-captures` finished with a complete table rather than a partial one. That guarantee needs the job to publish an explicit completion marker, which is a Phase 8 concern. And the critical path assumes durations are known; in practice they are estimates from history, and the critical path you compute is only as good as those estimates.

## Build It

The implementation is in `code/java/Main.java` and lives in four types: `Digraph`, `TopoSort`, `CycleException`, and `DagScheduler` with its `Task`, `Slot`, and `Schedule` records.

### Step 1: A directed graph that remembers insertion order

```java
final class Digraph {
    private final Map<String, List<String>> adjacency = new LinkedHashMap<>();

    Digraph addNode(String node) {
        Objects.requireNonNull(node, "node");
        adjacency.computeIfAbsent(node, k -> new ArrayList<>());
        return this;
    }

    Digraph addEdge(String from, String to) {
        addNode(from);
        addNode(to);
        List<String> out = adjacency.get(from);
        if (!out.contains(to)) out.add(to);
        return this;
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
```

`LinkedHashMap` rather than `HashMap` is the first design decision and the most important one. Both algorithms break ties by the order they encounter nodes, and a `HashMap` iterates in an order that depends on the hash codes and the table size, so two runs of the same batch with one extra job could schedule everything differently. A scheduler whose output you cannot predict from its input cannot be tested or audited. Insertion order is a choice a reviewer can see.

`addEdge` ignores duplicates because a repeated edge would make the in-degree count 2 for a single dependency and Kahn would never release the node. The edge list is a `List` with a linear `contains` check, which is fine for the fan-out of a dependency graph; a graph with nodes of degree 10,000 would use a `LinkedHashSet`.

**Predict:** what does `inDegrees()` return for a node that was added with `addNode` and never appears in any edge, and what does Kahn do with it?

### Step 2: Kahn's algorithm

```java
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
        throw new CycleException(findCycle(g));
    }
    return order;
}
```

This is the trace from The Concept, line for line. `inDegree.merge(next, -1, Integer::sum)` decrements and returns the new value in one call, so the "did it just reach zero" test is on the returned value and a node is enqueued exactly once. The final size check is the cycle test: Kahn cannot emit a node that is waiting on an unemitted node, so a shortfall means a cycle exists somewhere among the leftovers. The leftovers themselves are not reported, because they include everything downstream of the cycle; `findCycle` runs the DFS to name the actual loop.

**Predict:** if `fetch-fx-rates` were added to the graph before `ingest-captures`, which node would Kahn emit first, and would the order still be valid?

### Step 3: Iterative DFS with three colours

```java
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
```

Each `Frame` holds a node and the index of the next successor to examine, which is exactly what a recursive call's local state would hold. When the index runs off the end of the successor list the node is finished: it turns BLACK, goes on the `finished` list, and the frame is popped. An edge to a GRAY node is a back edge and triggers the cycle report. An edge to a BLACK node is skipped: that subtree was already fully explored, which is what makes the whole algorithm `O(V + E)` rather than exponential.

The `color` and `parent` maps here may be `HashMap`s because they are only looked up by key, never iterated; the iteration order that determines the output comes from `g.nodes()` and `g.successors`, which are insertion-ordered.

**Predict:** on the graph `a -> b, a -> c, b -> c`, which colour is `c` when the search reaches it the second time (via `a`), and why is that edge not a cycle?

### Step 4: Walking back the cycle

```java
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
```

`from` is the node whose edge pointed at a GRAY ancestor `to`. Every node between them on the DFS stack has a `parent` entry pointing one step up, so following `parent` from `from` until you reach `to` collects the stack segment, and reversing it puts the cycle in edge order starting at `to`. A self-loop `a -> a` gives `from == to`, the loop body never runs, and the cycle is `[a]`. The `CycleException` constructor formats it as `a -> b -> c -> a` so the message reads as a path.

### Step 5: Levels, for parallel rounds

```java
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
```

Processing nodes in topological order is what makes `depth.get(from)` safe: every predecessor has been assigned a depth before any node that depends on it. The predecessor scan is `O(V * E)` as written because `Digraph` stores only out-edges; a production scheduler stores in-edges too and this becomes `O(V + E)`. The lesson keeps the graph type small and accepts the cost at nine nodes.

**Predict:** if a graph has no edges at all, how many levels does it have?

### Step 6: Forward pass, backward pass, slack

```java
for (String n : order) {
    long start = -1;
    for (String from : predecessors(n)) {
        long f = earliestFinish.get(from);
        if (f > start) {
            start = f;
            criticalPredecessor.put(n, from);
        }
    }
    if (start < 0) start = 0;
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
```

The forward pass remembers, for each node, which predecessor forced its start time (`criticalPredecessor`). After the pass, start at the task that finishes at the makespan and follow `criticalPredecessor` backwards: that is the critical path, and its durations sum to the makespan. The strict `>` means the first predecessor in insertion order wins a tie, so two equally long paths produce the same reported critical path on every run.

The backward pass runs the same loop in reverse topological order so that every successor's `latestFinish` is known before the node that feeds it. Slack is then `latestStart - earliestStart`, assembled into a `Slot` record per task, and `isCritical()` is `slack == 0`.

Durations are `long` minutes and the addition is `Math.addExact`, for the same reason money is `long` minor units: a scheduling horizon that silently wraps negative is a bug that looks like a job scheduled in 1970.

**Predict:** `generate-payout-file` has slack 40. If its duration grows from 10 to 50 minutes, what happens to the makespan and to the critical path?

### Step 7: Run it

```bash
cd phases/02-algorithms/10-topological-sort-and-dags/code/java
javac -d .out *.java && java -cp .out Main
java -cp .out MainTest
```

Expected output, abbreviated:

```console
== Settlement batch: 9 jobs, 10 dependencies ==
Kahn order: [ingest-captures, fetch-fx-rates, compute-fees, fx-fixing, net-positions, generate-payout-file, post-ledger, reconcile, submit-to-bank]
DFS order:  [fetch-fx-rates, ingest-captures, fx-fixing, compute-fees, net-positions, post-ledger, reconcile, generate-payout-file, submit-to-bank]
both valid: true true

== Parallel levels (jobs in one level can run together) ==
level 0: [ingest-captures, fetch-fx-rates]
level 1: [compute-fees, fx-fixing]
level 2: [net-positions]
level 3: [generate-payout-file, post-ledger]
level 4: [reconcile]
level 5: [submit-to-bank]

== Critical path ==
task                      ES    EF    LS slack
ingest-captures            0    20     0     0  <- critical
compute-fees              20    35    20     0  <- critical
fx-fixing                 20    30    25     5
...
makespan = 115 minutes; critical path = [ingest-captures, compute-fees, net-positions, post-ledger, reconcile, submit-to-bank]

== A cycle: reconcile now waits for submit-to-bank ==
rejected: cycle detected: submit-to-bank -> reconcile -> submit-to-bank
```

The test file checks 41 assertions: both orders satisfy the validator, Kahn's order matches the hand trace exactly, the 100,000-node chain does not overflow the stack, the cycle is reported as the two-node loop and not as every downstream node, the levels and the slack column match the hand computation, and a cyclic graph cannot be scheduled.

### In Python

```python
in_degree = g.in_degrees()
ready: deque[str] = deque(node for node, d in in_degree.items() if d == 0)
order: list[str] = []
while ready:
    node = ready.popleft()
    order.append(node)
    for nxt in g.successors(node):
        in_degree[nxt] -= 1
        if in_degree[nxt] == 0:
            ready.append(nxt)
```

`Digraph`, `TopoSort`, `CycleException`, and `DagScheduler` keep their names and their algorithms; three things differ, all in `code/python/main.py`:

- **Insertion order is free.** A `dict` preserves insertion order (guaranteed since 3.7), so `Digraph` needs no `LinkedHashMap`, and `schedule` uses a `dict` with `None` values as the insertion-ordered set that `LinkedHashSet` provides. `successors` returns a tuple and `nodes` a keys view, the read-only counterparts of `Collections.unmodifiableList` and `unmodifiableSet`. The Kahn order, the DFS order, the levels, and every slack value match the Java run.
- **The iterative DFS is still required, for a different reason.** CPython stops at `sys.getrecursionlimit()` (1,000 frames by default) long before the JVM's stack runs out, so a recursive DFS would fail on the 100,000-node chain at depth 1,000 rather than around ten thousand. The explicit stack of `_Frame` objects (with `__slots__`, holding the node and the next successor index) is the same code as the Java.
- **Exceptions.** `CycleException` subclasses `RuntimeError`, the closest built-in to `IllegalStateException`, and carries the cycle the same way; argument errors (`unknown node`, a missing duration, a negative duration) raise `ValueError`. A test checks that a cycle is not reported as a `ValueError`, so callers can tell a bad graph from a bad argument. Durations are summed with `add_exact`, which raises `ArithmeticError` where `Math.addExact` would.

```bash
cd phases/02-algorithms/10-topological-sort-and-dags/code/python
python3 main.py
python3 -m unittest -v
```

The demo output is identical to the Java run; the suite covers the Java's 41 checks plus 2.

### Porting notes

The Go port uses a `[]string` for node order beside a `map[string][]string` for adjacency, because Go maps iterate in deliberately randomized order and a scheduler built on bare map iteration would produce a different valid order on every run. Go returns `(order, cycle, err)` rather than throwing; `errors.As` recovers the cycle. Durations stay `int64`, and Go's addition needs the hand-written overflow check from Phase 10, Lesson 01.

## Use It

**Build systems.** Make, Gradle, Bazel, and Cargo all compute a topological order of the target graph and dispatch independent targets in parallel; Bazel's `--jobs` flag is the worker count applied to the levels you computed in Step 5, and `bazel query --output=graph` prints the DAG. A "circular dependency" error from any of them is the `CycleException` from Step 4.

**Workflow orchestrators.** Apache Airflow calls its unit of scheduling a DAG by name, refuses to load a DAG file that contains a cycle, and schedules a task when all upstream tasks have succeeded, which is Kahn's algorithm with the ready queue fed by task completion events rather than by a loop. Temporal, Dagster, and Argo Workflows do the same with different vocabularies. Airflow's scheduler uses "critical path" informally; the computation it would need is Step 6.

**Databases.** PostgreSQL's `pg_dump` orders the objects in a dump topologically (a view after the tables it reads, a foreign key after both tables) and has a documented cycle-breaking step for the circular cases that SQL permits. Liquibase and Flyway order migrations by declared dependencies. Dependency resolution in package managers (`pip`, `npm`, `apt`) is a topological sort on top of a version-selection problem that is much harder.

**The JDK.** There is no `Collections.topologicalSort`. `java.util.concurrent.CompletableFuture` composes futures into a DAG implicitly (`thenCombine` is a node with two predecessors), and the JVM's own class initialization order is a topological order of the static-dependency graph, with a cycle resolved by Java's rule that a class in the middle of initialization is visible to the thread initializing it, which is why a static-initializer cycle produces nulls rather than an error. Knowing that the mechanism exists and is silent is the reason to recognize it.

**Payments.** Settlement batches, as in The Problem. Fee waterfalls (interchange, then scheme fee, then acquirer margin, each computed on the result of the previous) are DAGs with one path. Ledger posting rules that depend on other rules having fired are a DAG the rules engine must order, and a cycle in them is a rule that can never fire.

## Ship It

`outputs/checklist-dag-scheduler.md` is a review checklist for any batch, pipeline, or orchestration change: are dependencies declared as edges rather than as clock times, is the tie-break deterministic, does a cycle fail at load time with the cycle named, are completion markers explicit, and has anyone looked at the critical path before promising a cutoff to the bank.

## Exercises

1. **Reinforce.** Replace the `ArrayDeque` in `kahn` with a `PriorityQueue<String>` so the output is the lexicographically smallest topological order. Write a test asserting the new order on the settlement graph, and a second test showing the output is still a valid topological order under `isTopologicalOrder`.
2. **Apply.** Extend `Digraph` to also store predecessors (`Map<String, List<String>>` in-edges), then rewrite `levels()` and the forward pass to run in `O(V + E)` instead of `O(V * E)`. Time both versions on a 2,000-node random DAG using the harness from Phase 0, Lesson 05.
3. **Extend.** Count the number of distinct topological orders of a DAG using dynamic programming over subsets (Phase 2, Lesson 06): `count(S)` for a set `S` of already-placed nodes is the sum over every node `v` not in `S` whose predecessors are all in `S` of `count(S + v)`. Use a bitmask for `S`. Verify it gives 1 for a chain, `n!` for an edgeless graph, and work out by hand what it gives for the settlement graph before running it. Explain why this is exponential and why that does not matter for scheduling.

## Key Terms

| Term | What people say | What it actually means |
|------|-----------------|------------------------|
| Topological order | "dependency order" | A listing of a DAG's nodes in which every edge points from an earlier node to a later one; a linear extension of the partial order the edges define; exists iff the graph is acyclic |
| DAG | "a graph with no loops" | A directed graph with no directed cycle; undirected cycles (two paths to the same node, as `ingest -> fx-fixing <- fetch-fx`) are allowed and common |
| In-degree | "how many things it waits on" | The number of edges pointing into a node; Kahn's algorithm emits a node when its remaining in-degree reaches zero |
| Back edge | "a loop" | In DFS, an edge to a node that is currently on the recursion stack (GRAY); its existence is exactly equivalent to the graph having a cycle |
| Source / sink | "the start / the end" | A node with in-degree 0 / out-degree 0; every DAG has at least one of each, which is why peeling always makes progress |
| Level | "stage" or "wave" | The length of the longest path from any source to the node; all nodes in one level can run concurrently; the number of levels is the minimum number of sequential rounds |
| Critical path | "the long pole" | The chain of zero-slack tasks whose durations sum to the makespan; shortening any task not on it does not finish the batch sooner |
| Slack | "wiggle room" | `latestStart - earliestStart`; how long a task may be delayed without delaying the end; zero on the critical path |

## Further Reading

- [Kahn, "Topological sorting of large networks", Communications of the ACM 5(11), 1962](https://dl.acm.org/doi/10.1145/368996.369025) — the original in-degree peeling algorithm, written for PERT-style project networks, which is the same application as this lesson's settlement batch.
- [Tarjan, "Depth-first search and linear graph algorithms", SIAM Journal on Computing 1(2), 1972](https://doi.org/10.1137/0201010) — the DFS framework that gives finishing-order topological sort and strongly connected components in one traversal.
- [Kelley and Walker, "Critical-path planning and scheduling", Eastern Joint Computer Conference, 1959](https://dl.acm.org/doi/10.1145/1460299.1460318) — the forward/backward pass and slack computation of Step 6, in its original form.
- [Apache Airflow: DAGs](https://airflow.apache.org/docs/apache-airflow/stable/core-concepts/dags.html) — a production orchestrator's definition of a DAG, its cycle check at load time, and how task dependencies drive scheduling.
- [Bazel: Build concepts, the dependency graph](https://bazel.build/concepts/build-ref) — how a build system models targets as a DAG and why it insists the graph be acyclic.
- [PostgreSQL `pg_dump` source, `pg_dump_sort.c`](https://github.com/postgres/postgres/blob/master/src/bin/pg_dump/pg_dump_sort.c) — a real topological sort with explicit cycle-breaking rules for the dependency loops SQL allows between objects.
