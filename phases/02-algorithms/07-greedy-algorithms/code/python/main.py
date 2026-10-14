"""Lesson: phases/02-algorithms/07-greedy-algorithms/docs/en.md
Greedy Algorithms and Exchange Arguments
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: Huffman, "A Method for the Construction of Minimum-Redundancy Codes" (Proc. IRE, 1952); Kleinberg & Tardos,
Algorithm Design, ch. 4; Pearson, "A polynomial-time algorithm for the change-making problem" (2005). Stdlib only.
"""

from __future__ import annotations

import heapq
from dataclasses import dataclass
from typing import Any

LONG_MAX = 2**63 - 1


def compare(a: int, b: int) -> int:
    return (a > b) - (a < b)


@dataclass(frozen=True)
class Currency:
    """ISO 4217 currency: a three-letter code and the number of decimal places in its minor unit."""

    code: str
    exponent: int

    def __post_init__(self) -> None:
        if self.code is None or len(self.code) != 3:
            raise ValueError(f"currency code: {self.code}")
        if not 0 <= self.exponent <= 4:
            raise ValueError(f"exponent must be 0..4: {self.exponent}")


USD = Currency("USD", 2)


@dataclass(frozen=True)
class Money:
    """An exact amount of one currency as a signed count of minor units (Phase 10, Lesson 01)."""

    minor_units: int
    currency: Currency

    @staticmethod
    def of_minor(minor_units: int, currency: Currency) -> Money:
        return Money(minor_units, currency)

    def compare_to(self, other: Money) -> int:
        if self.currency != other.currency:
            raise ValueError(f"cannot compare {self.currency.code} with {other.currency.code}")
        return compare(self.minor_units, other.minor_units)

    def __str__(self) -> str:
        scale = 10 ** self.currency.exponent
        whole, frac = divmod(abs(self.minor_units), scale)
        sign = "-" if self.minor_units < 0 else ""
        if self.currency.exponent == 0:
            return f"{sign}{whole} {self.currency.code}"
        return f"{sign}{whole}.{frac:0{self.currency.exponent}d} {self.currency.code}"


@dataclass(frozen=True)
class Interval:
    """A half-open time interval [start, end): a settlement window, a maintenance slot, a batch job's run."""

    name: str
    start: int
    end: int

    def __post_init__(self) -> None:
        if self.end <= self.start:
            raise ValueError(f"interval must have end > start: {self.name}")


class IntervalScheduling:
    """Greedy interval problems. Both are provably optimal by exchange arguments (see docs)."""

    @staticmethod
    def max_non_overlapping(intervals: list[Interval]) -> list[Interval]:
        """Maximum number of pairwise non-overlapping intervals: always take the one that ends earliest. O(n log n)."""
        sorted_ = sorted(intervals, key=lambda iv: (iv.end, iv.start))
        chosen: list[Interval] = []
        last_end = -(2**63)
        for iv in sorted_:
            if iv.start >= last_end:  # compatible: take it
                chosen.append(iv)
                last_end = iv.end
        return chosen

    @staticmethod
    def shortest_first(intervals: list[Interval]) -> list[Interval]:
        """The tempting wrong greedy: take the shortest interval first. Kept to show it fail."""
        sorted_ = sorted(intervals, key=lambda iv: (iv.end - iv.start, iv.start))
        chosen: list[Interval] = []
        for iv in sorted_:
            compatible = all(not (iv.start < c.end and c.start < iv.end) for c in chosen)
            if compatible:
                chosen.append(iv)
        return chosen

    @staticmethod
    def min_resources(intervals: list[Interval]) -> int:
        """
        Minimum number of lanes so that no two overlapping intervals share one. Sort by start; a min-heap of end
        times tells whether the earliest-finishing lane is free. Equals the maximum overlap depth, so it is optimal.
        """
        sorted_ = sorted(intervals, key=lambda iv: (iv.start, iv.end))
        ends: list[int] = []
        for iv in sorted_:
            if ends and ends[0] <= iv.start:
                heapq.heappop(ends)  # reuse the lane that frees up first
            heapq.heappush(ends, iv.end)
        return len(ends)


@dataclass(frozen=True, eq=False)
class Node:
    """A node of the code tree. Leaves carry a symbol; internal nodes carry the sum of their children's weights."""

    weight: int
    symbol: int
    left: Node | None
    right: Node | None
    order: int

    def is_leaf(self) -> bool:
        return self.left is None


class Huffman:
    """Huffman coding: a greedy that is optimal because merging the two rarest symbols is always safe."""

    Node = Node

    @staticmethod
    def build(frequencies: dict[int, int]) -> Node:
        """Build the tree. `order` breaks weight ties by creation sequence so the tree, and the code, are deterministic."""
        if not frequencies:
            raise ValueError("no symbols")
        pq: list[tuple[int, int, Node]] = []  # (weight, order, node): heapq compares tuples left to right
        order = 0
        for symbol in sorted(frequencies):  # TreeMap iteration: ascending symbol
            weight = frequencies[symbol]
            if weight <= 0:
                raise ValueError(f"frequency must be positive: {symbol}")
            heapq.heappush(pq, (weight, order, Node(weight, symbol, None, None, order)))
            order += 1
        if len(pq) == 1:  # a one-symbol alphabet still needs a one-bit code
            only = heapq.heappop(pq)[2]
            return Node(only.weight, -1, only, None, order)
        while len(pq) > 1:
            a = heapq.heappop(pq)[2]  # the two lightest: the greedy choice
            b = heapq.heappop(pq)[2]
            merged = Node(a.weight + b.weight, -1, a, b, order)
            heapq.heappush(pq, (merged.weight, merged.order, merged))
            order += 1
        return heapq.heappop(pq)[2]

    @staticmethod
    def codes(root: Node) -> dict[int, str]:
        """Symbol -> bit string. Left edges are 0, right edges are 1."""
        out: dict[int, str] = {}
        Huffman._assign(root, "", out)
        return out

    @staticmethod
    def _assign(n: Node, prefix: str, out: dict[int, str]) -> None:
        if n.is_leaf():
            out[n.symbol] = prefix if prefix else "0"
            return
        assert n.left is not None
        Huffman._assign(n.left, prefix + "0", out)
        if n.right is not None:
            Huffman._assign(n.right, prefix + "1", out)

    @staticmethod
    def encoded_bits(frequencies: dict[int, int], codes: dict[int, str]) -> int:
        """Total encoded length in bits = sum over symbols of frequency * code length."""
        return sum(f * len(codes[s]) for s, f in frequencies.items())

    @staticmethod
    def encode(symbols: list[int], codes: dict[int, str]) -> str:
        return "".join(codes[s] for s in symbols)

    @staticmethod
    def decode(bits: str, root: Node) -> list[int]:
        """Decode by walking the tree; a prefix code needs no separators."""
        out: list[int] = []
        cur: Node | None = root
        for ch in bits:
            assert cur is not None
            cur = cur.left if ch == "0" else cur.right
            if cur is None:
                raise ValueError("invalid bit string")
            if cur.is_leaf():
                out.append(cur.symbol)
                cur = root
        if cur is not root:
            raise ValueError("truncated bit string")
        return out

    @staticmethod
    def is_prefix_free(codes: dict[int, str]) -> bool:
        """True when no code is a prefix of another: the property that makes decoding unambiguous."""
        cs = list(codes.values())
        for i in range(len(cs)):
            for j in range(len(cs)):
                if i != j and cs[j].startswith(cs[i]):
                    return False
        return True


class ChangeMaking:
    """
    Change-making: pay an amount with the fewest coins or notes from a denomination set.
    Greedy (largest denomination first) is optimal for canonical systems (USD, EUR, INR) and wrong for others.
    """

    @staticmethod
    def greedy(denominations: list[int], amount: int) -> list[int] | None:
        """Largest first. Counts per denomination (same order as input), or None if the amount cannot be made."""
        count = [0] * len(denominations)
        remaining = amount
        for d in sorted(denominations, reverse=True):
            idx = denominations.index(d)
            count[idx] = remaining // d  # amounts are non-negative: // and Java's / agree
            remaining %= d
        return count if remaining == 0 else None

    @staticmethod
    def optimal_table(denominations: list[int], max_amount: int) -> list[int]:
        """Exact minimum coin count for every amount 0..max_amount by DP; LONG_MAX marks impossible. O(max * k)."""
        if max_amount < 0:
            raise ValueError("negative amount")
        best = [LONG_MAX] * (max_amount + 1)
        best[0] = 0
        for a in range(1, max_amount + 1):
            for d in denominations:
                if d <= a and best[a - d] != LONG_MAX:
                    best[a] = min(best[a], best[a - d] + 1)
        return best

    @staticmethod
    def optimal_count(denominations: list[int], amount: int) -> int:
        """Exact minimum coin count for one amount: the oracle greedy is tested against."""
        return ChangeMaking.optimal_table(denominations, amount)[amount]

    @staticmethod
    def total(counts: list[int]) -> int:
        return sum(counts)

    @staticmethod
    def is_canonical(denominations: list[int]) -> bool:
        """
        Kozen-Zaks test: a system is canonical (greedy optimal for every amount) iff greedy is optimal for every
        amount below the sum of the two largest denominations. Needs a 1-coin to make every amount.
        """
        sorted_ = sorted(denominations)
        if sorted_[0] != 1:
            return False
        limit = sorted_[-1] + sorted_[-2] if len(sorted_) >= 2 else sorted_[0]
        optimal = ChangeMaking.optimal_table(denominations, limit)
        for amount in range(1, limit):
            g = ChangeMaking.greedy(denominations, amount)
            if g is None or ChangeMaking.total(g) != optimal[amount]:
                return False
        return True


def jstr(x: Any) -> str:
    """List.toString / Arrays.toString: [a, b]."""
    if isinstance(x, list):
        return "[" + ", ".join(jstr(e) for e in x) + "]"
    return str(x)


def jbool(b: bool) -> str:
    return "true" if b else "false"


def describe(denoms: list[int], counts: list[int]) -> str:
    parts = [f"{counts[i]}x{denoms[i]}" for i in range(len(denoms) - 1, -1, -1) if counts[i] > 0]
    return "{" + " ".join(parts) + "}"


def main() -> None:
    usd = USD

    print("== Interval scheduling: settlement windows that need an exclusive lane ==")
    windows = [
        Interval("ACH-batch", 0, 6), Interval("card-settle", 1, 4), Interval("wire-cutoff", 3, 5),
        Interval("fx-fixing", 3, 8), Interval("payout-run", 4, 7), Interval("recon", 5, 9),
        Interval("eod-close", 6, 10), Interval("reporting", 8, 11)]
    best = IntervalScheduling.max_non_overlapping(windows)
    print(f"earliest-end-first picks {len(best)}: {jstr([iv.name for iv in best])}")
    worse = IntervalScheduling.shortest_first(windows)
    print(f"shortest-first picks     {len(worse)}: {jstr([iv.name for iv in worse])}")
    print(f"lanes needed to run all {len(windows)} windows: {IntervalScheduling.min_resources(windows)}")

    print()
    print("== Huffman: a prefix code for ISO 8583 response codes by frequency ==")
    freq = dict(sorted({0: 850_000, 5: 60_000, 51: 45_000, 14: 20_000, 54: 15_000, 91: 7_000, 96: 3_000}.items()))
    tree = Huffman.build(freq)
    codes = Huffman.codes(tree)
    for k, v in freq.items():
        print(f"  code {k:2d}  freq {v:8,}  bits {codes[k]}")
    huff = Huffman.encoded_bits(freq, codes)
    total = sum(freq.values())
    fixed = total * 3  # 7 symbols need 3 bits each in a fixed-width code
    print(f"fixed 3-bit code: {fixed:,} bits; Huffman: {huff:,} bits ({100.0 * huff / fixed:.1f}% of fixed); "
          f"prefix-free: {jbool(Huffman.is_prefix_free(codes))}")
    msg = [0, 0, 0, 5, 0, 51, 0, 0, 14, 0]
    bits = Huffman.encode(msg, codes)
    print(f"encode {jstr(msg)} -> {bits} -> decode {jstr(Huffman.decode(bits, tree))}")

    print()
    print("== Change-making: greedy is optimal for some denomination sets and wrong for others ==")
    usd_coins = [1, 5, 10, 25, 100, 500, 1000, 2000, 5000, 10000]
    odd = [1, 3, 4]
    old_uk = [1, 3, 6, 12, 24, 30, 60, 240]  # pre-decimal pence
    print(f"USD-like {jstr(usd_coins)} canonical? {jbool(ChangeMaking.is_canonical(usd_coins))}")
    print(f"{{1,3,4}}  canonical? {jbool(ChangeMaking.is_canonical(odd))}: amount 6 -> greedy "
          f"{ChangeMaking.total(ChangeMaking.greedy(odd, 6))} coins, optimal {ChangeMaking.optimal_count(odd, 6)}")
    print(f"pre-1971 UK pence canonical? {jbool(ChangeMaking.is_canonical(old_uk))}: amount 48 -> greedy "
          f"{ChangeMaking.total(ChangeMaking.greedy(old_uk, 48))} coins (30+12+6), "
          f"optimal {ChangeMaking.optimal_count(old_uk, 48)} (24+24)")
    payout = ChangeMaking.greedy(usd_coins, 18_741)
    assert payout is not None
    print(f"pay {Money.of_minor(18_741, usd)} greedily: {ChangeMaking.total(payout)} pieces {describe(usd_coins, payout)}")


if __name__ == "__main__":
    main()
