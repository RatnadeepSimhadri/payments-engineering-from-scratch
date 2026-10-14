"""Lesson: phases/01-complexity-and-core-structures/10-disjoint-sets/docs/en.md
Disjoint Sets (Union-Find) with Path Compression.
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: Tarjan, "Efficiency of a Good But Not Linear Set Union Algorithm", JACM 22(2), 1975;
Cormen et al., "Introduction to Algorithms", ch. 19 (data structures for disjoint sets). Stdlib only.
"""

from __future__ import annotations


class DisjointSet:
    """Disjoint-set forest over the integers 0..n-1.

    Invariant: parent encodes a forest; every element reaches exactly one root by following parent pointers,
    and two elements are in the same set iff they reach the same root. rank[r] bounds the height of the tree
    rooted at r (meaningful only at roots); size[r] is the exact number of elements in r's set.
    The two heuristics can be switched off independently so the lesson can measure what each buys."""

    def __init__(self, n: int, union_by_rank: bool = True, path_compression: bool = True) -> None:
        if n < 0:
            raise ValueError("n must be >= 0")
        self._parent = list(range(n))
        self._rank = [0] * n
        self._size = [1] * n
        self._components = n
        self._union_by_rank = union_by_rank
        self._path_compression = path_compression
        self._hops = 0  # instrumentation: parent pointers followed inside find

    def elements(self) -> int:
        return len(self._parent)

    def component_count(self) -> int:
        return self._components

    def hops(self) -> int:
        return self._hops

    def reset_hops(self) -> None:
        self._hops = 0

    def _check_index(self, x: int) -> None:
        if x < 0 or x >= len(self._parent):
            raise IndexError(f"element {x} not in 0..{len(self._parent) - 1}")

    def find(self, x: int) -> int:
        """Representative of x's set. Two passes: walk to the root, then point every node on the path straight
        at it. Iterative, so a long uncompressed chain cannot hit Python's 1,000-frame recursion limit."""
        self._check_index(x)
        parent = self._parent
        root = x
        hops = 0
        while parent[root] != root:
            root = parent[root]
            hops += 1
        self._hops += hops
        if self._path_compression:
            while parent[x] != root:
                nxt = parent[x]
                parent[x] = root
                x = nxt
        return root

    def union(self, a: int, b: int) -> bool:
        """Merge the sets containing a and b. Returns False if they were already the same set."""
        ra, rb = self.find(a), self.find(b)
        if ra == rb:
            return False
        if self._union_by_rank:
            if self._rank[ra] < self._rank[rb]:
                ra, rb = rb, ra  # ra is now the taller (or equal) root
            self._parent[rb] = ra
            if self._rank[ra] == self._rank[rb]:
                self._rank[ra] += 1
        else:
            self._parent[rb] = ra  # naive: always hang b's root under a's root
        self._size[ra] += self._size[rb]
        self._components -= 1
        return True

    def connected(self, a: int, b: int) -> bool:
        return self.find(a) == self.find(b)

    def component_size(self, x: int) -> int:
        """Number of elements in x's set."""
        return self._size[self.find(x)]

    def rank_of(self, x: int) -> int:
        """Rank of the root of x's set: an upper bound on the height of its tree."""
        return self._rank[self.find(x)]

    def depth(self, x: int) -> int:
        """How many parent pointers separate x from its root. Does not compress."""
        self._check_index(x)
        parent = self._parent
        d = 0
        while parent[x] != x:
            x = parent[x]
            d += 1
        return d

    def max_depth(self) -> int:
        """Deepest element in the forest. O(n * depth); diagnostic only."""
        best = 0
        for i in range(len(self._parent)):
            best = max(best, self.depth(i))
        return best

    def check_invariant(self) -> bool:
        """Parents in range, every walk terminates, sizes and component count agree."""
        parent, n = self._parent, len(self._parent)
        roots = 0
        total = 0
        counted = [0] * n
        for i in range(n):
            if parent[i] < 0 or parent[i] >= n:
                return False
            x, steps = i, 0
            while parent[x] != x:
                x = parent[x]
                steps += 1
                if steps > n:
                    return False  # cycle guard
            counted[x] += 1
            if parent[i] == i:
                roots += 1
                total += self._size[i]
        if roots != self._components or total != n:
            return False
        for i in range(n):
            if parent[i] == i and counted[i] != self._size[i]:
                return False
        return True


class EntityLinker:
    """Entity linking for fraud: every identifier is an element, every shared identifier is a union, and a ring
    is a connected component. Names map to indices lazily so callers never see integers."""

    def __init__(self, capacity: int) -> None:
        self._index: dict[str, int] = {}
        self._names: list[str] = []
        self._capacity = capacity
        self._sets = DisjointSet(capacity)
        self._next = 0

    def _id(self, name: str) -> int:
        existing = self._index.get(name)
        if existing is not None:
            return existing
        if self._next >= self._capacity:
            raise RuntimeError(f"linker capacity exceeded: {self._capacity}")  # Java: IllegalStateException
        self._index[name] = self._next
        self._names.append(name)
        self._next += 1
        return self._next - 1

    def link(self, a: str, b: str) -> bool:
        """Record that two identifiers were seen together. Returns True if this observation merged two rings."""
        return self._sets.union(self._id(a), self._id(b))

    def same_ring(self, a: str, b: str) -> bool:
        ia, ib = self._index.get(a), self._index.get(b)
        return ia is not None and ib is not None and self._sets.connected(ia, ib)

    def ring_size(self, name: str) -> int:
        i = self._index.get(name)
        return 0 if i is None else self._sets.component_size(i)

    def ring(self, name: str) -> list[str]:
        """All identifiers in the same ring as name, sorted for deterministic output."""
        i = self._index.get(name)
        if i is None:
            return []
        root = self._sets.find(i)
        return sorted(self._names[j] for j in range(self._next) if self._sets.find(j) == root)

    def rings_of_at_least(self, min_size: int) -> list[list[str]]:
        """Every ring with at least min_size members, largest first; ties broken by the smallest member name."""
        by_root: dict[int, list[str]] = {}
        for j in range(self._next):
            by_root.setdefault(self._sets.find(j), []).append(self._names[j])
        out = [sorted(by_root[root]) for root in sorted(by_root) if len(by_root[root]) >= min_size]
        out.sort(key=lambda members: (-len(members), members[0]))
        return out

    def identifiers(self) -> int:
        return self._next

    def rings(self) -> int:
        return self._sets.component_count() - (self._capacity - self._next)  # unused slots are singletons


class JavaRandom:
    """java.util.Random, bit for bit (48-bit LCG), so the random workloads match the Java demo exactly."""

    _MULTIPLIER = 0x5DEECE66D
    _ADDEND = 0xB
    _MASK = (1 << 48) - 1

    def __init__(self, seed: int) -> None:
        self._seed = (seed ^ self._MULTIPLIER) & self._MASK

    def next(self, bits: int) -> int:
        self._seed = (self._seed * self._MULTIPLIER + self._ADDEND) & self._MASK
        r = self._seed >> (48 - bits)
        return r - (1 << 32) if r >= (1 << 31) else r

    def next_int(self, bound: int) -> int:
        if bound <= 0:
            raise ValueError("bound must be positive")
        r = self.next(31)
        m = bound - 1
        if bound & m == 0:
            return (bound * r) >> 31
        u = r
        r = u % bound
        while u - r + m >= (1 << 31):  # Java's int overflow test: u - r + m < 0
            u = self.next(31)
            r = u % bound
        return r


def _b(value: bool) -> str:
    return "true" if value else "false"


def demo_basics() -> None:
    print("== Union, find, connected ==")
    ds = DisjointSet(8)
    print(f"8 elements, {ds.component_count()} components")
    ds.union(0, 1)
    ds.union(2, 3)
    ds.union(1, 3)
    ds.union(5, 6)
    print(f"after union(0,1) union(2,3) union(1,3) union(5,6): {ds.component_count()} components")
    print(f"connected(0,2)={_b(ds.connected(0, 2))}  connected(0,5)={_b(ds.connected(0, 5))}"
          f"  size of 0's set={ds.component_size(0)}  size of 7's set={ds.component_size(7)}")
    print(f"union(0,3) again -> {_b(ds.union(0, 3))} (already joined; a cycle in the input, not in the forest)")
    print(f"invariant holds: {_b(ds.check_invariant())}")
    print()


LABELS = ["naive (neither)", "union by rank only", "path compression only", "both"]
FLAGS = [(False, False), (True, False), (False, True), (True, True)]


def heuristics_table(n: int, random_unions: int) -> list[tuple[int, int, int, int, float, int]]:
    """For each variant: (chain depth before, chain hops per find, chain depth after,
    random depth before, random hops per find, random depth after). Deterministic for a given n."""
    rows = []
    for by_rank, compress in FLAGS:
        ds = DisjointSet(n, by_rank, compress)
        # adversarial order for the naive forest: always union the growing set's element under a fresh singleton
        for i in range(1, n):
            ds.union(i, i - 1)
        chain_before = ds.max_depth()
        ds.reset_hops()
        for i in range(n):
            ds.find(i)
        chain_hops = ds.hops() // n
        chain_after = ds.max_depth()

        ds = DisjointSet(n, by_rank, compress)
        rnd = JavaRandom(3)
        for _ in range(random_unions):
            ds.union(rnd.next_int(n), rnd.next_int(n))
        random_before = ds.max_depth()
        ds.reset_hops()
        for i in range(n):
            ds.find(i)
        rows.append((chain_before, chain_hops, chain_after, random_before, ds.hops() / n, ds.max_depth()))
    return rows


def demo_heuristics(n: int = 2_000, random_unions: int = 3_000) -> None:
    # Java runs n = 20,000 with 30,000 random unions. The naive variant is O(n^2) pointer hops by design
    # (200 million on the 20,000 chain), which CPython cannot finish inside the ten-second demo budget, so the
    # Python demo runs the same workloads at one tenth the size; the Java algorithm prints the same numbers at
    # n = 2,000 (pinned in test_main.py).
    print("== What union by rank and path compression buy ==")
    rows = heuristics_table(n, random_unions)
    for label, row in zip(LABELS, rows):
        print("%-24s max depth before finds %6d, pointers followed per find %5d, max depth after %3d"
              % (label, row[0], row[1], row[2]))
    print("chain workload: rank refuses to build the chain; compression repairs it after one find")
    print()
    print(f"random workload: {n:,} elements, {random_unions:,} random unions, then one find per element")
    for label, row in zip(LABELS, rows):
        print("%-24s max depth before finds %6d, pointers followed per find %5.2f, max depth after %3d"
              % (label, row[3], row[4], row[5]))
    print(f"rank alone caps depth at log2(n) = {n.bit_length() - 1}; "
          "both together make almost every find a single hop")
    print()


def demo_fraud_ring() -> None:
    print("== Entity linking: accounts that share devices, cards, or emails ==")
    linker = EntityLinker(64)
    observations = [
        ("acct:1001", "device:A"), ("acct:1002", "device:A"),                        # two accounts, one phone
        ("acct:1002", "card:4111-xx-0001"), ("acct:1003", "card:4111-xx-0001"),     # card fingerprint reused
        ("acct:1003", "email:x@example.com"), ("acct:1004", "email:x@example.com"),  # same email
        ("acct:2001", "device:B"), ("acct:2002", "device:B"),                        # a separate, smaller ring
        ("acct:3001", "device:C"),                                                   # an honest singleton pair
    ]
    for a, b in observations:
        merged = linker.link(a, b)
        print("  link %-10s <-> %-20s %s" % (a, b, "merged" if merged else "already linked"))
    print(f"identifiers seen: {linker.identifiers()}, rings: {linker.rings()}")
    print(f"acct:1001 and acct:1004 share a ring: {_b(linker.same_ring('acct:1001', 'acct:1004'))}"
          " (no direct link between them; three hops through device, card, email)")
    print(f"acct:1001 and acct:2001 share a ring: {_b(linker.same_ring('acct:1001', 'acct:2001'))}")
    for ring in linker.rings_of_at_least(4):
        print(f"ring of {len(ring)} flagged for review: [{', '.join(ring)}]")
    print()


def largest(ds: DisjointSet) -> int:
    return max((ds.component_size(i) for i in range(ds.elements())), default=0)


def demo_random_merges() -> None:
    print("== 100,000 random unions over 50,000 elements ==")
    n = 50_000
    ds = DisjointSet(n)
    rnd = JavaRandom(11)
    merges = 0
    for _ in range(100_000):
        if ds.union(rnd.next_int(n), rnd.next_int(n)):
            merges += 1
    ds.reset_hops()
    for i in range(n):
        ds.find(i)
    print("effective merges %d, components left %d, largest set %d, max depth %d, avg pointers per find %.2f"
          % (merges, ds.component_count(), largest(ds), ds.max_depth(), ds.hops() / n))


def main() -> None:
    demo_basics()
    demo_heuristics()
    demo_fraud_ring()
    demo_random_merges()


if __name__ == "__main__":
    main()
