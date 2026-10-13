"""Lesson: phases/02-algorithms/08-graph-traversal-bfs-dfs/docs/en.md
Graph Traversal: BFS, DFS, and Connected Components
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: CLRS ch. 22 (BFS, DFS, white/gray/black colouring); Tarjan, "Depth-First Search and Linear Graph
Algorithms" (1972). Money design copied from phases/10-money-and-ledgers/01-representing-money.
Python 3.11+, standard library only. Same design as the Java and Go versions.
"""

from __future__ import annotations

from collections import deque
from dataclasses import dataclass
from enum import Enum
from typing import Iterable, Optional, Sequence


@dataclass(frozen=True)
class Currency:
    """ISO 4217 currency: code and minor-unit exponent. Trimmed copy of Phase 10 Lesson 01."""

    code: str
    exponent: int

    def __post_init__(self) -> None:
        if len(self.code) != 3:
            raise ValueError("currency code must be three letters")
        if not 0 <= self.exponent <= 4:
            raise ValueError("exponent must be 0..4")

    def minor_units_per_major(self) -> int:
        return 10 ** self.exponent


USD = Currency("USD", 2)
EUR = Currency("EUR", 2)


class CurrencyMismatchError(ValueError):
    def __init__(self, a: Currency, b: Currency) -> None:
        super().__init__(f"cannot combine {a.code} with {b.code}")


@dataclass(frozen=True)
class Money:
    """Exact amount of one currency as a signed count of minor units. Never a float."""

    minor_units: int
    currency: Currency

    def __post_init__(self) -> None:
        if not isinstance(self.minor_units, int) or isinstance(self.minor_units, bool):
            raise TypeError("minor_units must be an int; never construct Money from a float")

    @staticmethod
    def of_minor(minor_units: int, currency: Currency) -> "Money":
        return Money(minor_units, currency)

    def plus(self, other: "Money") -> "Money":
        if self.currency != other.currency:
            raise CurrencyMismatchError(self.currency, other.currency)
        return Money(self.minor_units + other.minor_units, self.currency)

    def __str__(self) -> str:
        magnitude = abs(self.minor_units)
        whole, frac = divmod(magnitude, self.currency.minor_units_per_major())
        text = str(whole)
        if self.currency.exponent > 0:
            text += "." + str(frac).rjust(self.currency.exponent, "0")
        return f"{'-' if self.minor_units < 0 else ''}{text} {self.currency.code}"

    __repr__ = __str__


class Graph:
    """Adjacency-list graph over string node ids. Dicts and lists preserve insertion order, which is what
    makes every traversal below deterministic."""

    def __init__(self, directed: bool) -> None:
        self._directed = directed
        self._adjacency: dict[str, list[str]] = {}
        self._edge_count = 0

    def is_directed(self) -> bool:
        return self._directed

    def add_node(self, node_id: str) -> None:
        self._adjacency.setdefault(node_id, [])

    def add_edge(self, frm: str, to: str) -> None:
        """Adds the endpoints if absent. For an undirected graph the edge is stored in both lists."""
        self.add_node(frm)
        self.add_node(to)
        self._adjacency[frm].append(to)
        if not self._directed and frm != to:
            self._adjacency[to].append(frm)
        self._edge_count += 1

    def has_node(self, node_id: str) -> bool:
        return node_id in self._adjacency

    def neighbors(self, node_id: str) -> Sequence[str]:
        if node_id not in self._adjacency:
            raise KeyError(f"unknown node: {node_id}")
        return tuple(self._adjacency[node_id])

    def nodes(self) -> list[str]:
        return list(self._adjacency)

    def node_count(self) -> int:
        return len(self._adjacency)

    def edge_count(self) -> int:
        return self._edge_count

    def undirected_view(self) -> "Graph":
        """The same nodes with every edge made two-way. Components are defined on this view."""
        if not self._directed:
            return self
        g = Graph(directed=False)
        for n in self._adjacency:
            g.add_node(n)
        for frm, tos in self._adjacency.items():
            for to in tos:
                g.add_edge(frm, to)
        return g


@dataclass(frozen=True)
class BfsResult:
    """What BFS leaves behind: hop distance and parent for every reached node, and the visit order."""

    distance: dict[str, int]
    parent: dict[str, str]
    order: list[str]

    def reached(self, node_id: str) -> bool:
        return node_id in self.distance


class Color(Enum):
    WHITE = "white"  # unvisited
    GRAY = "gray"    # on the current DFS path
    BLACK = "black"  # finished


def bfs(g: Graph, source: str) -> BfsResult:
    """Breadth-first from source. Each node is enqueued once, when first discovered; distance = hops."""
    if not g.has_node(source):
        raise KeyError(f"unknown node: {source}")
    distance: dict[str, int] = {source: 0}
    parent: dict[str, str] = {}
    order: list[str] = []
    queue: deque[str] = deque([source])
    while queue:
        u = queue.popleft()
        order.append(u)
        for v in g.neighbors(u):
            if v not in distance:  # discovered: mark BEFORE enqueueing, never after dequeueing
                distance[v] = distance[u] + 1
                parent[v] = u
                queue.append(v)
    return BfsResult(distance, parent, order)


def shortest_hop_path(g: Graph, source: str, target: str) -> list[str]:
    """Fewest-hops path from source to target by following BFS parents back; empty if unreachable."""
    r = bfs(g, source)
    if not r.reached(target):
        return []
    path: list[str] = []
    at: Optional[str] = target
    while at is not None:
        path.append(at)
        at = r.parent.get(at)
    return path[::-1]


def dfs_iterative(g: Graph, source: str) -> list[str]:
    """Depth-first preorder with an explicit stack. Neighbours are pushed in reverse so the visit order
    matches the recursive version, without the recursive version's stack-depth limit."""
    if not g.has_node(source):
        raise KeyError(f"unknown node: {source}")
    order: list[str] = []
    visited: set[str] = set()
    stack: list[str] = [source]
    while stack:
        u = stack.pop()
        if u in visited:  # a node may be pushed many times; it is visited once
            continue
        visited.add(u)
        order.append(u)
        for v in reversed(g.neighbors(u)):
            if v not in visited:
                stack.append(v)
    return order


def connected_components(g: Graph) -> list[list[str]]:
    """Connected components of the undirected view, each in discovery order, components in node order."""
    u = g.undirected_view()
    seen: set[str] = set()
    components: list[list[str]] = []
    for start in u.nodes():
        if start in seen:
            continue
        component = bfs(u, start).order
        seen.update(component)
        components.append(component)
    return components


def find_cycle(g: Graph) -> Optional[list[str]]:
    """Directed cycle detection by three-colour DFS, iterative. A GRAY neighbour is an ancestor on the current
    path, so the edge closes a cycle; a BLACK neighbour is a finished node reached by a second route.
    Returns the cycle as a closed walk (first node repeated at the end), or None if the graph is a DAG."""
    if not g.is_directed():
        raise ValueError("find_cycle is defined for directed graphs")
    color = {n: Color.WHITE for n in g.nodes()}
    for root in g.nodes():
        if color[root] is not Color.WHITE:
            continue
        path: list[str] = [root]       # the GRAY nodes, root first
        next_index: list[int] = [0]    # per frame: index of the next neighbour to examine
        color[root] = Color.GRAY
        while path:
            u = path[-1]
            i = next_index[-1]
            ns = g.neighbors(u)
            if i < len(ns):
                next_index[-1] = i + 1
                v = ns[i]
                if color[v] is Color.GRAY:
                    cycle = path[path.index(v):]
                    return cycle + [v]
                if color[v] is Color.WHITE:
                    color[v] = Color.GRAY
                    path.append(v)
                    next_index.append(0)
            else:
                color[u] = Color.BLACK
                path.pop()
                next_index.pop()
    return None


@dataclass(frozen=True)
class Transfer:
    """One transfer between two accounts. The edge of a money-flow graph."""

    frm: str
    to: str
    amount: Money


def from_transfers(transfers: Iterable[Transfer]) -> Graph:
    g = Graph(directed=True)
    edges: set[tuple[str, str]] = set()
    for t in transfers:
        if (t.frm, t.to) not in edges:  # one edge per ordered pair
            edges.add((t.frm, t.to))
            g.add_edge(t.frm, t.to)
    return g


def volume(transfers: Iterable[Transfer]) -> dict[str, Money]:
    """Total moved along each ordered pair, in insertion order. Mixed currencies are an error, not a sum."""
    out: dict[str, Money] = {}
    for t in transfers:
        key = f"{t.frm}->{t.to}"
        out[key] = out[key].plus(t.amount) if key in out else t.amount
    return out


def rings(g: Graph, min_size: int) -> list[list[str]]:
    """Components with at least min_size accounts: candidate rings or mule networks."""
    return [c for c in connected_components(g) if len(c) >= min_size]


def circular_flow(g: Graph) -> Optional[list[str]]:
    """A closed loop of transfers, if any: money that returns to where it started."""
    return find_cycle(g)


def main() -> None:
    transfers = [
        Transfer("acct-A", "acct-B", Money.of_minor(50000, USD)),
        Transfer("acct-B", "acct-C", Money.of_minor(48000, USD)),
        Transfer("acct-C", "acct-A", Money.of_minor(47000, USD)),  # closes the ring A -> B -> C -> A
        Transfer("acct-A", "acct-B", Money.of_minor(12000, USD)),  # second transfer on an existing edge
        Transfer("acct-C", "acct-D", Money.of_minor(1000, USD)),
        Transfer("acct-D", "acct-E", Money.of_minor(900, USD)),
        Transfer("acct-A", "acct-D", Money.of_minor(500, USD)),    # shortcut: A reaches D in one hop
        Transfer("payroll", "acct-X", Money.of_minor(250000, USD)),
        Transfer("acct-X", "landlord", Money.of_minor(180000, USD)),
        Transfer("acct-X", "grocer", Money.of_minor(9000, USD)),
    ]
    g = from_transfers(transfers)
    g.add_node("dormant")
    print("== Money-flow graph ==")
    print(f"{g.node_count()} accounts, {g.edge_count()} distinct edges")
    for edge, total in volume(transfers).items():
        print(f"  {edge} : {total}")

    print("\n== BFS from acct-A: hop distance to every reachable account ==")
    r = bfs(g, "acct-A")
    print(f"visit order : {r.order}")
    print(f"distances   : {r.distance}")
    print(f"path A -> E : {shortest_hop_path(g, 'acct-A', 'acct-E')}")
    print(f"path A -> X : {shortest_hop_path(g, 'acct-A', 'acct-X')}  (unreachable: empty)")

    print("\n== Iterative DFS preorder from acct-A ==")
    print(dfs_iterative(g, "acct-A"))

    print("\n== Connected components (direction ignored) ==")
    for c in connected_components(g):
        print(f"  size {len(c)}: {c}")
    print(f"rings of 3+ accounts: {rings(g, 3)}")

    print("\n== Directed cycle (three-colour DFS) ==")
    print(f"circular flow: {circular_flow(g) or []}")
    dag = Graph(directed=True)
    dag.add_edge("payroll", "acct-X")
    dag.add_edge("acct-X", "landlord")
    dag.add_edge("payroll", "landlord")  # diamond: two routes to landlord, no cycle
    print(f"payroll graph has cycle: {find_cycle(dag) is not None}")

    print("\n== 100,000-hop chain: iterative DFS does not overflow the stack ==")
    chain = Graph(directed=True)
    for i in range(100_000):
        chain.add_edge(f"n{i}", f"n{i + 1}")
    print(f"visited {len(dfs_iterative(chain, 'n0'))} nodes")


if __name__ == "__main__":
    main()
