"""Lesson: phases/02-algorithms/11-minimum-spanning-trees/docs/en.md
Minimum Spanning Trees: Kruskal and Prim
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: Kruskal (Proc. AMS 1956); Prim (Bell System Tech. J. 1957); Tarjan, "Efficiency of a good but not linear
set union algorithm" (JACM 1975). Python 3.11+, standard library only. Same design as the Java version.
"""

from __future__ import annotations

import heapq
from dataclasses import dataclass
from typing import Any, Iterable, KeysView

LONG_MIN = -(2**63)
LONG_MAX = 2**63 - 1


def add_exact(a: int, b: int) -> int:
    """Math.addExact for long: Python ints never overflow, so check the signed 64-bit range explicitly."""
    r = a + b
    if not LONG_MIN <= r <= LONG_MAX:
        raise ArithmeticError("long overflow")
    return r


@dataclass(frozen=True)
class WeightedEdge:
    """An undirected edge with an integer weight (cost in whole currency units, metres, milliseconds)."""

    u: str
    v: str
    weight: int

    def __post_init__(self) -> None:
        if self.u is None:
            raise TypeError("u")
        if self.v is None:
            raise TypeError("v")
        if self.u == self.v:
            raise ValueError(f"self-loop {self.u} can never be in a spanning tree")
        if self.weight < 0:
            raise ValueError(f"weight must be non-negative: {self.weight}")

    def other(self, node: str) -> str:
        """The endpoint that is not `node`."""
        if node == self.u:
            return self.v
        if node == self.v:
            return self.u
        raise ValueError(f"{node} is not an endpoint of {self}")

    def same_endpoints(self, o: WeightedEdge) -> bool:
        """Two edges are the same connection regardless of which endpoint was listed first."""
        return (self.u == o.u and self.v == o.v) or (self.u == o.v and self.v == o.u)


class WeightedGraph:
    """Undirected weighted graph. Insertion order of nodes and edges is preserved so tie-breaks are reproducible."""

    def __init__(self) -> None:
        self._adjacency: dict[str, list[WeightedEdge]] = {}
        self._edges: list[WeightedEdge] = []

    def add_node(self, node: str) -> WeightedGraph:
        if node is None:
            raise TypeError("node")
        self._adjacency.setdefault(node, [])
        return self

    def add_edge(self, u: str, v: str, weight: int) -> WeightedGraph:
        e = WeightedEdge(u, v, weight)
        self.add_node(u)
        self.add_node(v)
        self._edges.append(e)
        self._adjacency[u].append(e)
        self._adjacency[v].append(e)
        return self

    def nodes(self) -> KeysView[str]:
        return self._adjacency.keys()

    def edges(self) -> tuple[WeightedEdge, ...]:
        return tuple(self._edges)

    def incident(self, node: str) -> tuple[WeightedEdge, ...]:
        out = self._adjacency.get(node)
        if out is None:
            raise ValueError(f"unknown node: {node}")
        return tuple(out)

    def node_count(self) -> int:
        return len(self._adjacency)

    def edge_count(self) -> int:
        return len(self._edges)


class UnionFind:
    """Disjoint-set forest with union by rank and path compression (Phase 1, Lesson 10). Amortized O(alpha(n))."""

    def __init__(self, elements: Iterable[str]) -> None:
        self._parent: dict[str, str] = {}
        self._rank: dict[str, int] = {}
        self._components = 0
        for e in elements:
            self._parent[e] = e
            self._rank[e] = 0
            self._components += 1

    def find(self, e: str) -> str:
        """Representative of e's set. Path compression: every node on the way up is re-pointed at the root."""
        root = e
        while self._parent[root] != root:
            root = self._parent[root]
        while self._parent[e] != root:
            nxt = self._parent[e]
            self._parent[e] = root
            e = nxt
        return root

    def union(self, a: str, b: str) -> bool:
        """Merges the sets of a and b. False if they were already one set (an edge would close a cycle)."""
        ra, rb = self.find(a), self.find(b)
        if ra == rb:
            return False
        rka, rkb = self._rank[ra], self._rank[rb]
        if rka < rkb:
            self._parent[ra] = rb
        elif rka > rkb:
            self._parent[rb] = ra
        else:
            self._parent[rb] = ra
            self._rank[ra] = rka + 1
        self._components -= 1
        return True

    def connected(self, a: str, b: str) -> bool:
        return self.find(a) == self.find(b)

    def components(self) -> int:
        return self._components


@dataclass(frozen=True)
class SpanningForest:
    """The output of either algorithm: the chosen edges, their total weight, and how many trees the forest has."""

    edges: list[WeightedEdge]
    total_weight: int
    component_count: int

    def is_tree(self) -> bool:
        """True when the forest is a single tree, i.e. the input graph was connected."""
        return self.component_count == 1


class Mst:
    """Kruskal's and Prim's algorithms for minimum spanning trees (forests, when the graph is disconnected)."""

    @staticmethod
    def kruskal(g: WeightedGraph) -> SpanningForest:
        """
        Sort edges by weight (stable, so equal weights keep insertion order), take each edge whose endpoints are
        in different components, skip the rest. O(E log E). Correct by the cut property.
        """
        sorted_ = sorted(g.edges(), key=lambda e: e.weight)  # sorted() is a stable Timsort, like List.sort
        uf = UnionFind(g.nodes())
        chosen: list[WeightedEdge] = []
        total = 0
        for e in sorted_:
            if len(chosen) == g.node_count() - 1:
                break  # a tree on V nodes has V-1 edges
            if uf.union(e.u, e.v):
                chosen.append(e)
                total = add_exact(total, e.weight)
        return SpanningForest(chosen, total, uf.components())

    @staticmethod
    def prim(g: WeightedGraph, start: str) -> SpanningForest:
        """
        Grow one tree from a start node, always adding the lightest edge that leaves it. The frontier is a binary
        heap of (weight, insertion index) with lazy deletion. Restarts from the first unvisited node per component.
        """
        if start not in g.nodes():
            raise ValueError(f"unknown start node: {start}")
        in_tree: set[str] = set()
        chosen: list[WeightedEdge] = []
        total = 0
        components = 0

        roots = [start] + [n for n in g.nodes() if n != start]
        for root in roots:
            if root in in_tree:
                continue
            components += 1
            in_tree.add(root)
            heap: list[tuple[int, int]] = []  # (weight, insertion index) so equal weights pop reproducibly
            by_index: list[WeightedEdge] = []
            Mst._push_frontier(g, root, in_tree, heap, by_index)
            while heap:
                _, idx = heapq.heappop(heap)
                e = by_index[idx]
                nxt = (None if e.v in in_tree else e.v) if e.u in in_tree else e.u
                if nxt is None:
                    continue  # stale: both ends already in the tree
                in_tree.add(nxt)
                chosen.append(e)
                total = add_exact(total, e.weight)
                Mst._push_frontier(g, nxt, in_tree, heap, by_index)
        return SpanningForest(chosen, total, components)

    @staticmethod
    def _push_frontier(g: WeightedGraph, node: str, in_tree: set[str],
                       heap: list[tuple[int, int]], by_index: list[WeightedEdge]) -> None:
        for e in g.incident(node):
            if e.other(node) in in_tree:
                continue
            by_index.append(e)
            heapq.heappush(heap, (e.weight, len(by_index) - 1))

    @staticmethod
    def is_minimum_spanning_forest(g: WeightedGraph, f: SpanningForest) -> bool:
        """
        Independent check: only edges of g, exactly V - components edges, connects what g connects, and every edge
        is a lightest edge across the cut it closes. Brute force; for tests and reviews.
        """
        if len(f.edges) != g.node_count() - f.component_count:
            return False
        all_ = UnionFind(g.nodes())
        for e in g.edges():
            all_.union(e.u, e.v)
        if all_.components() != f.component_count:
            return False

        forest = UnionFind(g.nodes())
        g_edges = g.edges()
        for e in f.edges:
            if e not in g_edges:
                return False
            if not forest.union(e.u, e.v):
                return False  # a cycle: not a forest
        # cut property: removing edge e splits its tree in two; no edge of g across that split is lighter
        for removed in f.edges:
            without = UnionFind(g.nodes())
            for e in f.edges:
                if e is not removed:  # identity, like Java's != on references: parallel equal edges stay distinct
                    without.union(e.u, e.v)
            for e in g_edges:
                side = without.find(removed.u)
                crosses = (without.find(e.u) == side) != (without.find(e.v) == side)
                if crosses and e.weight < removed.weight:
                    return False
        return True

    @staticmethod
    def clusters(g: WeightedGraph, k: int) -> list[list[str]]:
        """Single-linkage clustering: the MST minus its k - 1 heaviest edges. Clusters in node insertion order."""
        if k < 1:
            raise ValueError("k must be >= 1")
        f = Mst.kruskal(g)
        keep = sorted(f.edges, key=lambda e: e.weight)
        to_remove = min(k - f.component_count, len(keep))
        if to_remove > 0:
            keep = keep[:len(keep) - to_remove]
        uf = UnionFind(g.nodes())
        for e in keep:
            uf.union(e.u, e.v)
        groups: dict[str, list[str]] = {}
        for n in g.nodes():
            groups.setdefault(uf.find(n), []).append(n)
        return list(groups.values())


def site_network() -> WeightedGraph:
    """Eight processing sites and the monthly cost, in whole dollars, of each feasible leased line."""
    return (WeightedGraph()
            .add_edge("ashburn", "newyork", 4)
            .add_edge("ashburn", "chicago", 7)
            .add_edge("newyork", "chicago", 9)
            .add_edge("newyork", "london", 12)
            .add_edge("chicago", "dallas", 6)
            .add_edge("chicago", "london", 15)
            .add_edge("dallas", "phoenix", 5)
            .add_edge("dallas", "london", 20)
            .add_edge("phoenix", "sanjose", 3)
            .add_edge("sanjose", "chicago", 11)
            .add_edge("london", "frankfurt", 2)
            .add_edge("sanjose", "frankfurt", 25))


def same_edge_set(a: SpanningForest, b: SpanningForest) -> bool:
    if len(a.edges) != len(b.edges):
        return False
    return all(any(e.same_endpoints(f) and e.weight == f.weight for f in b.edges) for e in a.edges)


def jstr(x: Any) -> str:
    """List.toString: [a, b], nested lists included."""
    if isinstance(x, list):
        return "[" + ", ".join(jstr(e) for e in x) + "]"
    return str(x)


def jbool(b: bool) -> str:
    return "true" if b else "false"


def main() -> None:
    g = site_network()
    print(f"== Site network: {g.node_count()} sites, {g.edge_count()} feasible lines ==")
    print(f"cost of leasing every line: {sum(e.weight for e in g.edges())}")

    print()
    print("== Kruskal: lightest edges first, skip any that closes a cycle ==")
    k = Mst.kruskal(g)
    for e in k.edges:
        print(f"  take {e.u:<9} - {e.v:<9} {e.weight:3d}")
    print(f"total {k.total_weight}, tree: {jbool(k.is_tree())}, "
          f"verified minimum: {jbool(Mst.is_minimum_spanning_forest(g, k))}")

    print()
    print("== Prim from ashburn: grow one tree, always the lightest edge leaving it ==")
    p = Mst.prim(g, "ashburn")
    for e in p.edges:
        print(f"  take {e.u:<9} - {e.v:<9} {e.weight:3d}")
    print(f"total {p.total_weight}, same edge set as Kruskal: {jbool(same_edge_set(k, p))}")

    print()
    print("== Disconnected: drop the four transatlantic lines ==")
    split = WeightedGraph()
    transatlantic = [WeightedEdge("newyork", "london", 0), WeightedEdge("chicago", "london", 0),
                     WeightedEdge("dallas", "london", 0), WeightedEdge("sanjose", "frankfurt", 0)]
    for e in g.edges():
        if not any(e.same_endpoints(t) for t in transatlantic):
            split.add_edge(e.u, e.v, e.weight)
    forest = Mst.kruskal(split)
    print(f"components: {forest.component_count}, edges: {len(forest.edges)}, total {forest.total_weight}")

    print()
    print("== Single-linkage clustering: cut the 2 heaviest MST edges for 3 clusters ==")
    print(jstr(Mst.clusters(g, 3)))


if __name__ == "__main__":
    main()
