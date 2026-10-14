"""Lesson: phases/02-algorithms/10-topological-sort-and-dags/docs/en.md
Topological Sort and DAG Scheduling
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: Kahn, "Topological sorting of large networks" (CACM 1962); Tarjan, "Depth-first search and linear graph
algorithms" (SIAM J. Comput. 1972); Kelley & Walker, "Critical-path planning and scheduling" (1959). Stdlib only.
"""

from __future__ import annotations

from collections import deque
from dataclasses import dataclass
from enum import Enum
from typing import Any, KeysView

LONG_MIN = -(2**63)
LONG_MAX = 2**63 - 1


def add_exact(a: int, b: int) -> int:
    """Math.addExact for long: Python ints never overflow, so check the signed 64-bit range explicitly."""
    r = a + b
    if not LONG_MIN <= r <= LONG_MAX:
        raise ArithmeticError("long overflow")
    return r


class Digraph:
    """
    A directed graph over string-named nodes. Insertion order of nodes and edges is preserved (a dict does
    this natively) so every algorithm below produces the same answer on every run.
    """

    def __init__(self) -> None:
        self._adjacency: dict[str, list[str]] = {}

    def add_node(self, node: str) -> Digraph:
        if node is None:
            raise TypeError("node")
        self._adjacency.setdefault(node, [])
        return self

    def add_edge(self, from_: str, to: str) -> Digraph:
        """Adds the edge from_ -> to, meaning "from_ must finish before to can start"."""
        self.add_node(from_)
        self.add_node(to)
        out = self._adjacency[from_]
        if to not in out:
            out.append(to)
        return self

    def nodes(self) -> KeysView[str]:
        return self._adjacency.keys()  # a read-only, insertion-ordered view

    def successors(self, node: str) -> tuple[str, ...]:
        out = self._adjacency.get(node)
        if out is None:
            raise ValueError(f"unknown node: {node}")
        return tuple(out)  # read-only, like Collections.unmodifiableList

    def node_count(self) -> int:
        return len(self._adjacency)

    def edge_count(self) -> int:
        return sum(len(out) for out in self._adjacency.values())

    def in_degrees(self) -> dict[str, int]:
        in_ = {node: 0 for node in self._adjacency}
        for out in self._adjacency.values():
            for to in out:
                in_[to] += 1
        return in_


class CycleException(RuntimeError):
    """Raised when a graph that must be a DAG contains a cycle. Carries the cycle so the caller can report it."""

    def __init__(self, cycle: list[str]) -> None:
        super().__init__("cycle detected: " + " -> ".join(cycle) + " -> " + cycle[0])
        self._cycle = tuple(cycle)

    def cycle(self) -> list[str]:
        """The nodes of one cycle, in edge order, without repeating the first node at the end."""
        return list(self._cycle)


class _Color(Enum):
    WHITE = 0
    GRAY = 1
    BLACK = 2


class _Frame:
    """One DFS frame: the node and the index of the next successor to examine."""

    __slots__ = ("node", "next")

    def __init__(self, node: str) -> None:
        self.node = node
        self.next = 0


class TopoSort:
    """Topological orderings of a DAG, with cycle detection that reports the offending cycle."""

    @staticmethod
    def kahn(g: Digraph) -> list[str]:
        """
        Kahn's algorithm: repeatedly emit a node with in-degree zero and decrement its successors.
        Ties are broken by node insertion order (the ready queue is FIFO). O(V + E). Raises CycleException.
        """
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
        if len(order) != g.node_count():
            # every node still holding in-degree > 0 lies on or downstream of a cycle; find one to report
            raise CycleException(TopoSort.find_cycle(g))
        return order

    @staticmethod
    def dfs(g: Digraph) -> list[str]:
        """
        Depth-first topological sort: reverse finishing order. Iterative, so a 100k-node chain does not hit
        the recursion limit. Raises CycleException with the back-edge cycle if one is found.
        """
        color = {n: _Color.WHITE for n in g.nodes()}
        parent: dict[str, str] = {}
        finished: list[str] = []

        for root in g.nodes():
            if color[root] is not _Color.WHITE:
                continue
            stack = [_Frame(root)]
            color[root] = _Color.GRAY
            while stack:
                top = stack[-1]
                succ = g.successors(top.node)
                if top.next < len(succ):
                    child = succ[top.next]
                    top.next += 1
                    c = color[child]
                    if c is _Color.WHITE:
                        color[child] = _Color.GRAY
                        parent[child] = top.node
                        stack.append(_Frame(child))
                    elif c is _Color.GRAY:
                        raise CycleException(TopoSort._walk_back(top.node, child, parent))
                    # BLACK: already finished, a cross or forward edge, safe to ignore
                else:
                    color[top.node] = _Color.BLACK
                    finished.append(top.node)
                    stack.pop()
        finished.reverse()
        return finished

    @staticmethod
    def find_cycle(g: Digraph) -> list[str]:
        """Returns one cycle of g in edge order, or an empty list if g is acyclic."""
        try:
            TopoSort.dfs(g)
            return []
        except CycleException as e:
            return e.cycle()

    @staticmethod
    def _walk_back(from_: str, to: str, parent: dict[str, str]) -> list[str]:
        """From the GRAY node `from_` back up the DFS tree to `to`, then reverse into edge order."""
        path: list[str] = []
        cur = from_
        while cur != to:
            path.append(cur)
            cur = parent[cur]
        path.append(to)
        path.reverse()
        return path

    @staticmethod
    def is_topological_order(g: Digraph, order: list[str]) -> bool:
        """True if `order` lists every node of g exactly once and every edge points forward."""
        if len(order) != g.node_count() or len(set(order)) != len(order):
            return False
        pos = {node: i for i, node in enumerate(order)}
        for from_ in g.nodes():
            pf = pos.get(from_)
            if pf is None:
                return False
            for to in g.successors(from_):
                if pos[to] <= pf:
                    return False
        return True


@dataclass(frozen=True)
class Task:
    """A task with a duration in whole time units (minutes, here). Durations are integers, like money."""

    name: str
    minutes: int

    def __post_init__(self) -> None:
        if self.name is None:
            raise TypeError("name")
        if self.minutes < 0:
            raise ValueError(f"duration must be non-negative: {self.minutes}")


@dataclass(frozen=True)
class Slot:
    """One task's schedule: earliest start, earliest finish, latest start without delaying the end, and slack."""

    task: str
    earliest_start: int
    earliest_finish: int
    latest_start: int
    slack: int

    def is_critical(self) -> bool:
        return self.slack == 0


@dataclass(frozen=True)
class Schedule:
    """The result of scheduling a DAG of tasks: per-task slots, the makespan, and the critical path."""

    slots: dict[str, Slot]
    makespan: int
    critical_path: list[str]


class DagScheduler:
    """DAG scheduling: parallel levels and the critical path (the longest chain of durations)."""

    def __init__(self, graph: Digraph, task_list: list[Task]) -> None:
        self._graph = graph
        self._tasks: dict[str, Task] = {t.name: t for t in task_list}
        for n in graph.nodes():
            if n not in self._tasks:
                raise ValueError(f"no duration for task {n}")

    def levels(self) -> list[list[str]]:
        """Level 0 has no prerequisites; level k's prerequisites are all at levels < k. One level runs together."""
        depth: dict[str, int] = {}
        for n in TopoSort.kahn(self._graph):
            d = 0
            for from_ in self._graph.nodes():
                if n in self._graph.successors(from_):
                    d = max(d, depth[from_] + 1)
            depth[n] = d
        max_depth = max(depth.values(), default=-1)
        out: list[list[str]] = [[] for _ in range(max_depth + 1)]
        for n in self._graph.nodes():
            out[depth[n]].append(n)
        return out

    def schedule(self) -> Schedule:
        """Forward pass for earliest times, backward pass for latest; slack = LS - ES; zero slack is critical."""
        order = TopoSort.kahn(self._graph)
        earliest_start: dict[str, int] = {}
        earliest_finish: dict[str, int] = {}
        critical_predecessor: dict[str, str] = {}
        for n in order:
            start = -1
            for from_ in self._predecessors(n):
                f = earliest_finish[from_]
                if f > start:  # strict: first predecessor wins ties, deterministically
                    start = f
                    critical_predecessor[n] = from_
            if start < 0:
                start = 0  # no predecessors: can start at time zero
            earliest_start[n] = start
            earliest_finish[n] = add_exact(start, self._tasks[n].minutes)
        makespan = max(earliest_finish.values(), default=0)

        latest_finish: dict[str, int] = {}
        for n in reversed(order):
            lf = makespan
            for to in self._graph.successors(n):
                lf = min(lf, latest_finish[to] - self._tasks[to].minutes)
            latest_finish[n] = lf

        slots: dict[str, Slot] = {}
        for n in self._graph.nodes():
            es = earliest_start[n]
            ls = latest_finish[n] - self._tasks[n].minutes
            slots[n] = Slot(n, es, earliest_finish[n], ls, ls - es)

        # walk back from the first latest-finishing task along critical predecessors
        end = next((n for n in order if earliest_finish[n] == makespan), None)
        path: dict[str, None] = {}  # an insertion-ordered set, like LinkedHashSet
        cur = end
        while cur is not None:
            path[cur] = None
            cur = critical_predecessor.get(cur)
        critical = list(path)
        critical.reverse()
        return Schedule(slots, makespan, critical)

    def _predecessors(self, node: str) -> list[str]:
        return [from_ for from_ in self._graph.nodes() if node in self._graph.successors(from_)]


def settlement_graph() -> Digraph:
    """The end-of-day settlement batch used throughout the lesson text."""
    return (Digraph()
            .add_edge("ingest-captures", "compute-fees")
            .add_edge("ingest-captures", "fx-fixing")
            .add_edge("fetch-fx-rates", "fx-fixing")
            .add_edge("compute-fees", "net-positions")
            .add_edge("fx-fixing", "net-positions")
            .add_edge("net-positions", "generate-payout-file")
            .add_edge("net-positions", "post-ledger")
            .add_edge("post-ledger", "reconcile")
            .add_edge("generate-payout-file", "submit-to-bank")
            .add_edge("reconcile", "submit-to-bank"))


def settlement_tasks() -> list[Task]:
    return [
        Task("ingest-captures", 20),
        Task("fetch-fx-rates", 5),
        Task("compute-fees", 15),
        Task("fx-fixing", 10),
        Task("net-positions", 25),
        Task("generate-payout-file", 10),
        Task("post-ledger", 30),
        Task("reconcile", 20),
        Task("submit-to-bank", 5),
    ]


def jstr(x: Any) -> str:
    """List.toString: [a, b]."""
    if isinstance(x, list):
        return "[" + ", ".join(jstr(e) for e in x) + "]"
    return str(x)


def jbool(b: bool) -> str:
    return "true" if b else "false"


def main() -> None:
    g = settlement_graph()
    print(f"== Settlement batch: {g.node_count()} jobs, {g.edge_count()} dependencies ==")
    print(f"Kahn order: {jstr(TopoSort.kahn(g))}")
    print(f"DFS order:  {jstr(TopoSort.dfs(g))}")
    print(f"both valid: {jbool(TopoSort.is_topological_order(g, TopoSort.kahn(g)))} "
          f"{jbool(TopoSort.is_topological_order(g, TopoSort.dfs(g)))}")

    print()
    print("== Parallel levels (jobs in one level can run together) ==")
    scheduler = DagScheduler(g, settlement_tasks())
    for i, level in enumerate(scheduler.levels()):
        print(f"level {i}: {jstr(level)}")

    print()
    print("== Critical path ==")
    s = scheduler.schedule()
    print(f"{'task':<22} {'ES':>5} {'EF':>5} {'LS':>5} {'slack':>5}")
    for slot in s.slots.values():
        mark = "  <- critical" if slot.is_critical() else ""
        print(f"{slot.task:<22} {slot.earliest_start:5d} {slot.earliest_finish:5d} "
              f"{slot.latest_start:5d} {slot.slack:5d}{mark}")
    print(f"makespan = {s.makespan} minutes; critical path = {jstr(s.critical_path)}")

    print()
    print("== A cycle: reconcile now waits for submit-to-bank ==")
    broken = settlement_graph().add_edge("submit-to-bank", "reconcile")
    try:
        TopoSort.kahn(broken)
    except CycleException as e:
        print(f"rejected: {e}")


if __name__ == "__main__":
    main()
