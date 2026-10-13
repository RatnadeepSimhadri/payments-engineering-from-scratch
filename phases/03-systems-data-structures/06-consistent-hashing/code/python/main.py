"""Lesson: phases/03-systems-data-structures/06-consistent-hashing/docs/en.md
Consistent Hashing and Virtual Nodes
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: Karger et al. (1997), "Consistent Hashing and Random Trees"; DeCandia et al. (2007), "Dynamo";
FNV-1a (IETF draft-eastlake-fnv) and MurmurHash3 fmix64 for the point hash.
Python 3.11+, standard library only. Same design as the Java and Go versions.
"""

from __future__ import annotations

import bisect
import math
from typing import Iterable

MASK64 = (1 << 64) - 1
FNV_OFFSET = 0xCBF29CE484222325
FNV_PRIME = 0x100000001B3


def fnv1a64(data: bytes) -> int:
    h = FNV_OFFSET
    for b in data:
        h ^= b
        h = (h * FNV_PRIME) & MASK64
    return h


def mix64(h: int) -> int:
    h &= MASK64
    h ^= h >> 33
    h = (h * 0xFF51AFD7ED558CCD) & MASK64
    h ^= h >> 33
    h = (h * 0xC4CEB9FE1A85EC53) & MASK64
    h ^= h >> 33
    return h


def hash_key(key: str) -> int:
    """Position of a key or a virtual node on the ring: a 64-bit unsigned integer.
    FNV-1a for the bytes, fmix64 to spread the low bits that short, similar keys leave correlated."""
    return mix64(fnv1a64(key.encode("utf-8")))


class HashRing:
    """A sorted array of (hash, node) points. Each physical node owns `virtual_nodes` points.
    A key belongs to the first point clockwise from its own hash; the ring wraps."""

    def __init__(self, virtual_nodes: int = 100) -> None:
        if virtual_nodes <= 0:
            raise ValueError(f"virtual_nodes must be positive: {virtual_nodes}")
        self.virtual_nodes = virtual_nodes
        self._hashes: list[int] = []   # sorted ascending; parallel to _owners
        self._owners: list[str] = []
        self._nodes: set[str] = set()

    # ----- membership of the ring ------------------------------------------------------------

    @staticmethod
    def point_hash(node: str, replica: int) -> int:
        """The i-th virtual node of `node` sits at hash(node + '#' + i)."""
        return hash_key(f"{node}#{replica}")

    def add_node(self, node: str) -> None:
        if node in self._nodes:
            raise ValueError(f"node already on ring: {node}")
        for i in range(self.virtual_nodes):
            h = self.point_hash(node, i)
            pos = bisect.bisect_left(self._hashes, h)
            # Ties on a 64-bit hash are practically impossible; order them by owner name so every
            # language builds the identical ring.
            while pos < len(self._hashes) and self._hashes[pos] == h and self._owners[pos] < node:
                pos += 1
            self._hashes.insert(pos, h)
            self._owners.insert(pos, node)
        self._nodes.add(node)

    def remove_node(self, node: str) -> None:
        if node not in self._nodes:
            raise ValueError(f"node not on ring: {node}")
        keep = [(h, o) for h, o in zip(self._hashes, self._owners) if o != node]
        self._hashes = [h for h, _ in keep]
        self._owners = [o for _, o in keep]
        self._nodes.remove(node)

    def nodes(self) -> list[str]:
        return sorted(self._nodes)

    def point_count(self) -> int:
        return len(self._hashes)

    # ----- lookups ---------------------------------------------------------------------------

    def _first_index_at_or_after(self, h: int) -> int:
        """Binary search for the first point with hash >= h, wrapping to 0 past the end."""
        pos = bisect.bisect_left(self._hashes, h)
        return 0 if pos == len(self._hashes) else pos

    def lookup(self, key: str) -> str:
        """The node that owns `key`: O(log P) for P points."""
        if not self._hashes:
            raise LookupError("ring is empty")
        return self._owners[self._first_index_at_or_after(hash_key(key))]

    def lookup_n(self, key: str, n: int) -> list[str]:
        """The owner plus the next n-1 distinct nodes clockwise: a replication preference list.
        Returns fewer than n if the ring has fewer than n nodes."""
        if n <= 0:
            raise ValueError("n must be positive")
        if not self._hashes:
            raise LookupError("ring is empty")
        start = self._first_index_at_or_after(hash_key(key))
        result: list[str] = []
        total = len(self._hashes)
        for step in range(total):
            owner = self._owners[(start + step) % total]
            if owner not in result:
                result.append(owner)
                if len(result) == n or len(result) == len(self._nodes):
                    break
        return result

    def assign(self, keys: Iterable[str]) -> dict[str, str]:
        return {key: self.lookup(key) for key in keys}


class ModNSharder:
    """The naive alternative: shard = hash(key) mod N over a sorted node list. Adding a node
    renumbers almost every key."""

    def __init__(self, nodes: Iterable[str]) -> None:
        self._nodes = sorted(nodes)
        if not self._nodes:
            raise ValueError("need at least one node")

    def lookup(self, key: str) -> str:
        return self._nodes[hash_key(key) % len(self._nodes)]

    def assign(self, keys: Iterable[str]) -> dict[str, str]:
        return {key: self.lookup(key) for key in keys}


# ----- measurement -------------------------------------------------------------------------------


def fraction_moved(before: dict[str, str], after: dict[str, str]) -> float:
    """Share of keys whose owner changed between two assignments of the same key set."""
    if not before:
        return 0.0
    moved = sum(1 for key, owner in before.items() if after[key] != owner)
    return moved / len(before)


def load_per_node(assignment: dict[str, str], nodes: Iterable[str]) -> dict[str, int]:
    counts = {node: 0 for node in nodes}
    for owner in assignment.values():
        counts[owner] += 1
    return counts


def imbalance(assignment: dict[str, str], nodes: Iterable[str]) -> tuple[float, float]:
    """(max load / mean load, coefficient of variation). 1.0 and 0.0 would be perfect balance."""
    counts = list(load_per_node(assignment, nodes).values())
    mean = sum(counts) / len(counts)
    variance = sum((c - mean) ** 2 for c in counts) / len(counts)
    return max(counts) / mean, math.sqrt(variance) / mean


def account_ids(count: int) -> list[str]:
    return [f"acct-{i:06d}" for i in range(count)]


def main() -> None:
    shards = [f"ledger-shard-{c}" for c in "abcdefghij"]
    keys = account_ids(20_000)

    print("== Where an account lands ==")
    ring = HashRing(virtual_nodes=100)
    for s in shards:
        ring.add_node(s)
    key = "acct-000123"
    print(f"hash_key({key!r}) = 0x{hash_key(key):016x}")
    print(f"{ring.point_count()} points on the ring for {len(ring.nodes())} shards")
    print(f"lookup({key!r}) = {ring.lookup(key)}")
    print(f"lookup_n({key!r}, 3) = {ring.lookup_n(key, 3)}  (owner + 2 replicas)")

    print("\n== Adding an eleventh shard: how many accounts move? ==")
    before_ring = ring.assign(keys)
    before_mod = ModNSharder(shards).assign(keys)
    ring.add_node("ledger-shard-k")
    after_ring = ring.assign(keys)
    after_mod = ModNSharder(shards + ["ledger-shard-k"]).assign(keys)
    print(f"consistent hash ring: {fraction_moved(before_ring, after_ring):.3f} of keys moved (ideal 1/11 = {1 / 11:.3f})")
    print(f"hash(key) mod N:      {fraction_moved(before_mod, after_mod):.3f} of keys moved (expected 10/11 = {10 / 11:.3f})")
    moved_to_new = sum(1 for k in keys if after_ring[k] != before_ring[k] and after_ring[k] == "ledger-shard-k")
    moved_elsewhere = sum(1 for k in keys if after_ring[k] != before_ring[k] and after_ring[k] != "ledger-shard-k")
    print(f"ring: {moved_to_new} keys moved to the new shard, {moved_elsewhere} moved anywhere else")

    print("\n== Removing a shard: only its keys move ==")
    ring.remove_node("ledger-shard-c")
    after_remove = ring.assign(keys)
    stayed = sum(1 for k in keys if after_ring[k] != "ledger-shard-c" and after_remove[k] == after_ring[k])
    print(f"{stayed} keys not on shard-c kept their owner (out of {sum(1 for k in keys if after_ring[k] != 'ledger-shard-c')})")
    print(f"{fraction_moved(after_ring, after_remove):.3f} of keys moved (shard-c held {sum(1 for k in keys if after_ring[k] == 'ledger-shard-c') / len(keys):.3f})")

    print("\n== Virtual nodes and balance (10 shards, 20,000 accounts) ==")
    print(f"{'vnodes':>6} {'max/mean':>9} {'coeff. of variation':>20}")
    for v in (1, 10, 100, 1000):
        r = HashRing(virtual_nodes=v)
        for s in shards:
            r.add_node(s)
        ratio, cv = imbalance(r.assign(keys), shards)
        print(f"{v:>6} {ratio:>9.3f} {cv:>20.3f}")

    print("\n== Replication: the preference list for an account ==")
    r = HashRing(virtual_nodes=100)
    for s in shards[:5]:
        r.add_node(s)
    for k in ("acct-000001", "acct-000002"):
        print(f"lookup_n({k!r}, 3) = {r.lookup_n(k, 3)}")
    print(f"lookup_n with n > nodes returns every node once: {r.lookup_n('acct-000001', 8)}")
    try:
        HashRing().lookup("acct-000001")
    except LookupError as exc:
        print(f"rejected: {exc}")


if __name__ == "__main__":
    main()
