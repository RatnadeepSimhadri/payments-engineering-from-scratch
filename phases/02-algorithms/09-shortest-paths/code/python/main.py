"""Lesson: phases/02-algorithms/09-shortest-paths/docs/en.md
Shortest Paths: Dijkstra, Bellman-Ford, and Negative Cycles
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: Dijkstra (1959), "A note on two problems in connexion with graphs"; Bellman (1958), "On a routing problem";
CLRS ch. 24. IndexedMinHeap copied from phases/01-complexity-and-core-structures/09-heaps-and-priority-queues;
Money copied from phases/10-money-and-ledgers/01-representing-money. Python 3.11+, standard library only.
"""

from __future__ import annotations

import math
from dataclasses import dataclass
from enum import Enum
from typing import Generic, Optional, Sequence, TypeVar

K = TypeVar("K")

# -----------------------------------------------------------------------------------------------------------
# Money (trimmed copy of Phase 10, Lesson 01): integer minor units, exact rational multiplication.
# -----------------------------------------------------------------------------------------------------------

RATE_SCALE = 100_000_000      # rates carry 8 decimals: 0.92 is 92_000_000
WEIGHT_SCALE = 1_000_000      # weights are -ln(rate) in micro-nats, rounded once
SOURCE = "__source__"


@dataclass(frozen=True)
class Currency:
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
GBP = Currency("GBP", 2)
JPY = Currency("JPY", 0)


class Rounding(Enum):
    HALF_EVEN = "half_even"
    HALF_UP = "half_up"
    DOWN = "down"
    UP = "up"


def divide_rounded(dividend: int, divisor: int, rounding: Rounding) -> int:
    """Integer division with an explicit rounding rule, on magnitudes so negatives truncate like Java/Go."""
    sign = -1 if (dividend < 0) != (divisor < 0) else 1
    quotient, remainder = divmod(abs(dividend), abs(divisor))
    if remainder == 0:
        return sign * quotient
    twice = remainder * 2
    if rounding is Rounding.UP:
        quotient += 1
    elif rounding is Rounding.HALF_UP and twice >= abs(divisor):
        quotient += 1
    elif rounding is Rounding.HALF_EVEN and (twice > abs(divisor) or (twice == abs(divisor) and quotient % 2 == 1)):
        quotient += 1
    return sign * quotient


@dataclass(frozen=True)
class Money:
    minor_units: int
    currency: Currency

    def __post_init__(self) -> None:
        if not isinstance(self.minor_units, int) or isinstance(self.minor_units, bool):
            raise TypeError("minor_units must be an int; never construct Money from a float")

    @staticmethod
    def of_minor(minor_units: int, currency: Currency) -> "Money":
        return Money(minor_units, currency)

    def convert(self, scaled_rate: int, to: Currency, rounding: Rounding) -> "Money":
        """Convert at an exact scaled rate (rate = scaled_rate / RATE_SCALE) with one rounding at the end.
        Minor-unit exponents may differ (USD cents to JPY yen), so both scales are applied."""
        numerator = self.minor_units * scaled_rate * to.minor_units_per_major()
        denominator = RATE_SCALE * self.currency.minor_units_per_major()
        return Money(divide_rounded(numerator, denominator, rounding), to)

    def __str__(self) -> str:
        magnitude = abs(self.minor_units)
        whole, frac = divmod(magnitude, self.currency.minor_units_per_major())
        text = str(whole)
        if self.currency.exponent > 0:
            text += "." + str(frac).rjust(self.currency.exponent, "0")
        return f"{'-' if self.minor_units < 0 else ''}{text} {self.currency.code}"

    __repr__ = __str__


# -----------------------------------------------------------------------------------------------------------
# IndexedMinHeap (copied from Phase 1, Lesson 09): the priority queue Dijkstra needs, with decrease_key.
# -----------------------------------------------------------------------------------------------------------


@dataclass(frozen=True)
class Entry(Generic[K]):
    key: K
    priority: int
    seq: int

    @staticmethod
    def compare(a: "Entry[K]", b: "Entry[K]") -> int:
        if a.priority != b.priority:
            return -1 if a.priority < b.priority else 1
        if a.seq != b.seq:
            return -1 if a.seq < b.seq else 1
        return 0


class IndexedMinHeap(Generic[K]):
    """Min-heap of unique keys with a position index so decrease_key is O(log n). Ties break by insertion order."""

    def __init__(self) -> None:
        self._entries: list[Entry[K]] = []
        self._index: dict[K, int] = {}
        self._next_seq = 0

    def push(self, key: K, priority: int) -> None:
        if key in self._index:
            raise ValueError(f"key already present: {key!r}")
        self._entries.append(Entry(key, priority, self._next_seq))
        self._next_seq += 1
        self._index[key] = len(self._entries) - 1
        self._sift_up(len(self._entries) - 1)

    def decrease_key(self, key: K, new_priority: int) -> None:
        i = self._index.get(key)
        if i is None:
            raise KeyError(f"key not present: {key!r}")
        current = self._entries[i]
        if new_priority > current.priority:
            raise ValueError(f"decrease_key cannot raise {key!r} from {current.priority} to {new_priority}")
        self._entries[i] = Entry(key, new_priority, current.seq)
        self._sift_up(i)

    def pop(self) -> Entry[K]:
        if not self._entries:
            raise IndexError("heap is empty")
        top = self._entries[0]
        last = self._entries.pop()
        del self._index[top.key]
        if self._entries:
            self._entries[0] = last
            self._index[last.key] = 0
            self._sift_down(0)
        return top

    def contains(self, key: K) -> bool:
        return key in self._index

    def size(self) -> int:
        return len(self._entries)

    def is_empty(self) -> bool:
        return not self._entries

    def _sift_up(self, i: int) -> None:
        e = self._entries
        while i > 0:
            parent = (i - 1) // 2
            if Entry.compare(e[parent], e[i]) <= 0:
                return
            self._swap(parent, i)
            i = parent

    def _sift_down(self, i: int) -> None:
        e = self._entries
        n = len(e)
        while True:
            left, right, smallest = 2 * i + 1, 2 * i + 2, i
            if left < n and Entry.compare(e[left], e[smallest]) < 0:
                smallest = left
            if right < n and Entry.compare(e[right], e[smallest]) < 0:
                smallest = right
            if smallest == i:
                return
            self._swap(i, smallest)
            i = smallest

    def _swap(self, a: int, b: int) -> None:
        e = self._entries
        e[a], e[b] = e[b], e[a]
        self._index[e[a].key] = a
        self._index[e[b].key] = b


# -----------------------------------------------------------------------------------------------------------
# Weighted graph and the two shortest-path algorithms.
# -----------------------------------------------------------------------------------------------------------


@dataclass(frozen=True)
class Edge:
    """A directed edge with an integer weight. Integers on purpose: sums are exact and associative."""

    frm: str
    to: str
    weight: int


class WeightedGraph:
    def __init__(self) -> None:
        self._adjacency: dict[str, list[Edge]] = {}
        self._edges: list[Edge] = []

    def add_node(self, node_id: str) -> None:
        self._adjacency.setdefault(node_id, [])

    def add_edge(self, frm: str, to: str, weight: int) -> None:
        self.add_node(frm)
        self.add_node(to)
        e = Edge(frm, to, weight)
        self._adjacency[frm].append(e)
        self._edges.append(e)

    def has_node(self, node_id: str) -> bool:
        return node_id in self._adjacency

    def nodes(self) -> list[str]:
        return list(self._adjacency)

    def out_edges(self, node_id: str) -> Sequence[Edge]:
        if node_id not in self._adjacency:
            raise KeyError(f"unknown node: {node_id}")
        return tuple(self._adjacency[node_id])

    def edges(self) -> Sequence[Edge]:
        return tuple(self._edges)

    def node_count(self) -> int:
        return len(self._adjacency)


@dataclass(frozen=True)
class PathResult:
    """Distances and parents from one source; a path is read back through the parents."""

    distance: dict[str, int]
    parent: dict[str, str]

    def reached(self, node_id: str) -> bool:
        return node_id in self.distance

    def path_to(self, target: str) -> list[str]:
        if not self.reached(target):
            return []
        path: list[str] = []
        at: Optional[str] = target
        while at is not None:
            path.append(at)
            at = self.parent.get(at)
        return path[::-1]


def dijkstra(g: WeightedGraph, source: str) -> PathResult:
    """Single-source shortest paths for NON-NEGATIVE weights. Invariant: when a node is popped from the heap its
    distance is final, because every other reachable node has distance >= the popped one and edges only add.
    O((V + E) log V) with the indexed heap: each node pushed once, each edge relaxes at most once."""
    if not g.has_node(source):
        raise KeyError(f"unknown node: {source}")
    for e in g.edges():
        if e.weight < 0:
            raise ValueError(f"Dijkstra requires non-negative weights; {e.frm}->{e.to} is {e.weight}")
    distance: dict[str, int] = {}
    parent: dict[str, str] = {}
    tentative: dict[str, int] = {source: 0}
    heap: IndexedMinHeap[str] = IndexedMinHeap()
    heap.push(source, 0)
    while not heap.is_empty():
        top = heap.pop()
        u = top.key
        distance[u] = top.priority  # settled: never changes again
        for e in g.out_edges(u):
            if e.to in distance:  # already settled; a shorter route cannot exist
                continue
            candidate = top.priority + e.weight
            known = tentative.get(e.to)
            if known is None:
                tentative[e.to] = candidate
                parent[e.to] = u
                heap.push(e.to, candidate)
            elif candidate < known:
                tentative[e.to] = candidate
                parent[e.to] = u
                heap.decrease_key(e.to, candidate)
    return PathResult(distance, parent)


@dataclass(frozen=True)
class BellmanFordResult:
    paths: PathResult
    negative_cycle: Optional[list[str]]


def bellman_ford(g: WeightedGraph, source: str) -> BellmanFordResult:
    """Single-source shortest paths for ANY weights, O(V * E). Relax every edge V-1 times; a shortest path has at
    most V-1 edges, so after round k every node whose shortest path has k edges is correct. If a V-th round
    still relaxes an edge, a negative cycle is reachable, and the parent pointers lead into it."""
    if not g.has_node(source):
        raise KeyError(f"unknown node: {source}")
    distance: dict[str, int] = {source: 0}
    parent: dict[str, str] = {}
    n = g.node_count()
    relaxed_in_last_round: Optional[str] = None
    for _ in range(n):
        relaxed_in_last_round = None
        for e in g.edges():
            du = distance.get(e.frm)
            if du is None:  # unreachable so far
                continue
            candidate = du + e.weight
            dv = distance.get(e.to)
            if dv is None or candidate < dv:
                distance[e.to] = candidate
                parent[e.to] = e.frm
                relaxed_in_last_round = e.to
        if relaxed_in_last_round is None:  # early exit: a full round changed nothing
            break
    if relaxed_in_last_round is None:
        return BellmanFordResult(PathResult(distance, parent), None)
    # Round n still relaxed: walk parents n times to be sure we are inside the cycle, then collect it.
    x = relaxed_in_last_round
    for _ in range(n):
        x = parent[x]
    cycle: list[str] = [x]
    at = parent[x]
    while at != x:
        cycle.append(at)
        at = parent[at]
    cycle.append(x)
    return BellmanFordResult(PathResult(distance, parent), cycle[::-1])  # parents point backwards; reverse for forward order


# -----------------------------------------------------------------------------------------------------------
# FX: rates as scaled integers, weights as integer-scaled negative logs, arbitrage as a negative cycle.
# -----------------------------------------------------------------------------------------------------------


@dataclass(frozen=True)
class Rate:
    """One quote: 1 unit of `frm` buys scaled_rate / RATE_SCALE units of `to`."""

    frm: str
    to: str
    scaled_rate: int

    def __post_init__(self) -> None:
        if self.scaled_rate <= 0:
            raise ValueError(f"rate must be positive: {self.frm}->{self.to}")


def weight_of(scaled_rate: int) -> int:
    """The only floating-point operation in the lesson. ln turns a product of rates into a sum of weights; the
    minus sign turns "best rate" into "shortest path"; the scale turns the float into an int so every sum is
    exact, associative, and identical across Java, Python, and Go. Rounding is +-0.5 micro-nat per edge, so
    every detected cycle is re-checked exactly with cycle_product before anyone calls it arbitrage.
    round() rounds ties to even; Java's Math.round rounds ties up; Go's math.Round rounds ties away from zero.
    No rate in this lesson lands on an exact tie, so the three agree."""
    rate = scaled_rate / RATE_SCALE
    return round(-math.log(rate) * WEIGHT_SCALE)


def from_rates(rates: Sequence[Rate]) -> WeightedGraph:
    g = WeightedGraph()
    for r in rates:
        g.add_edge(r.frm, r.to, weight_of(r.scaled_rate))
    return g


def best_conversion_path(rates: Sequence[Rate], frm: str, to: str) -> list[str]:
    """Best conversion (max product of rates = min sum of -log weights). Bellman-Ford, because weights can be negative."""
    r = bellman_ford(from_rates(rates), frm)
    if r.negative_cycle is not None:
        raise RuntimeError(f"rates contain an arbitrage cycle: {r.negative_cycle}")
    return r.paths.path_to(to)


@dataclass(frozen=True)
class CycleProduct:
    """Exact rational product of the rates along a closed walk: numerator / RATE_SCALE**k."""

    numerator: int
    denominator: int

    def exceeds_one(self) -> bool:
        return self.numerator > self.denominator

    def gain_basis_points(self) -> int:
        """(num/den - 1) * 10000, truncated toward zero."""
        diff = (self.numerator - self.denominator) * 10_000
        return -((-diff) // self.denominator) if diff < 0 else diff // self.denominator


def rate_between(rates: Sequence[Rate], frm: str, to: str) -> int:
    for r in rates:
        if r.frm == frm and r.to == to:
            return r.scaled_rate
    raise KeyError(f"no rate {frm}->{to}")


def cycle_product(rates: Sequence[Rate], closed_walk: Sequence[str]) -> CycleProduct:
    num, den = 1, 1
    for a, b in zip(closed_walk, closed_walk[1:]):
        num *= rate_between(rates, a, b)
        den *= RATE_SCALE
    return CycleProduct(num, den)


def arbitrage(rates: Sequence[Rate]) -> Optional[list[str]]:
    """Any negative cycle in the whole graph: add a virtual source with zero-weight edges to every currency."""
    g = from_rates(rates)
    for node in g.nodes():
        g.add_edge(SOURCE, node, 0)
    cycle = bellman_ford(g, SOURCE).negative_cycle
    if cycle is not None and not cycle_product(rates, cycle).exceeds_one():
        return None  # rounding artefact, not arbitrage: the exact product is <= 1
    return cycle


def rotate_to(closed_walk: Sequence[str], start: str) -> list[str]:
    """Rotate a closed walk so it starts (and ends) at start: [EUR, GBP, USD, EUR] -> [USD, EUR, GBP, USD]."""
    if start not in closed_walk:
        raise KeyError(f"{start} is not on the cycle {list(closed_walk)}")
    k = closed_walk.index(start)
    length = len(closed_walk) - 1
    return [closed_walk[(k + i) % length] for i in range(length + 1)]


def convert_along(amount: Money, path: Sequence[str], rates: Sequence[Rate], currencies: dict[str, Currency]) -> Money:
    """Convert an amount along a path, rounding HALF_EVEN at each hop as a real multi-leg conversion would."""
    if not path or amount.currency.code != path[0]:
        raise ValueError(f"amount is {amount.currency.code} but path starts at {list(path)}")
    current = amount
    for a, b in zip(path, path[1:]):
        current = current.convert(rate_between(rates, a, b), currencies[b], Rounding.HALF_EVEN)
    return current


def market() -> list[Rate]:
    """Mid rates with a 0.2% spread applied, except USD->JPY which is quoted wide (1%)."""
    return [
        Rate("USD", "EUR", 91_816_000),      # 0.92 * 0.998
        Rate("EUR", "USD", 108_478_260),     # 1/0.92 * 0.998
        Rate("EUR", "GBP", 85_828_000),      # 0.86 * 0.998
        Rate("GBP", "EUR", 116_046_510),     # 1/0.86 * 0.998
        Rate("USD", "JPY", 14_850_000_000),  # 150 * 0.99 (wide)
        Rate("JPY", "USD", 665_333),         # 1/150 * 0.998
        Rate("EUR", "JPY", 16_271_400_000),  # 163.04 * 0.998
        Rate("JPY", "EUR", 612_060),         # 1/163.04 * 0.998
        Rate("GBP", "USD", 126_137_000),     # 1.2639 * 0.998
    ]


CURRENCIES = {"USD": USD, "EUR": EUR, "GBP": GBP, "JPY": JPY}


def main() -> None:
    print("== Rates to weights: -ln(rate) in micro-nats ==")
    for r in market()[:5]:
        print(f"{r.frm}->{r.to} rate={r.scaled_rate}/1e8 weight={weight_of(r.scaled_rate)}")

    print("\n== Dijkstra on non-negative spread costs (basis points) ==")
    spreads = WeightedGraph()
    spreads.add_edge("USD", "EUR", 20)
    spreads.add_edge("EUR", "JPY", 20)
    spreads.add_edge("USD", "JPY", 100)
    spreads.add_edge("EUR", "GBP", 20)
    spreads.add_edge("GBP", "JPY", 15)
    d = dijkstra(spreads, "USD")
    print(f"USD->JPY cheapest spread: {d.distance['JPY']} bps via {d.path_to('JPY')}")
    print(f"all distances: {d.distance}")

    print("\n== Bellman-Ford on -log weights: best conversion path ==")
    rates = market()
    best = best_conversion_path(rates, "USD", "JPY")
    ten_k = Money.of_minor(1_000_000, USD)
    print(f"best USD->JPY path: {best}")
    print(f"  10,000.00 USD along best path  = {convert_along(ten_k, best, rates, CURRENCIES)}")
    print(f"  10,000.00 USD on the direct quote = {convert_along(ten_k, ['USD', 'JPY'], rates, CURRENCIES)}")
    print(f"arbitrage in consistent market: {arbitrage(rates) or 'none'}")

    print("\n== A stale GBP->USD quote creates a negative cycle ==")
    stale = rates[:8] + [Rate("GBP", "USD", 128_000_000)]  # 1.28 instead of 1.2614
    cycle = arbitrage(stale)
    print(f"arbitrage cycle: {cycle or []}")
    if cycle is not None:
        p = cycle_product(stale, cycle)
        print(f"exact product of rates along the cycle = {p.numerator} / {p.denominator}")
        print(f"gain = {p.gain_basis_points()} basis points per loop")
        from_usd = rotate_to(cycle, "USD")
        print(f"10,000.00 USD around {from_usd} = {convert_along(ten_k, from_usd, stale, CURRENCIES)}")

    print("\n== Why Dijkstra cannot be used on these weights ==")
    try:
        dijkstra(from_rates(rates), "USD")
    except ValueError as exc:
        print(f"rejected: {exc}")


if __name__ == "__main__":
    main()
