"""Lesson: phases/07-storage-and-transactions/01-partitioning-and-rebalancing/docs/en.md
Partitioning Strategies and Rebalancing
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: Karger et al., "Consistent Hashing and Random Trees" (STOC 1997); DeCandia et al., "Dynamo" (SOSP 2007);
FNV-1a hash (Fowler, Noll, Vo). Python 3.11+, standard library only. Same design as the Java version.
"""

from __future__ import annotations

import bisect
from dataclasses import dataclass
from enum import Enum
from typing import Callable

MASK64 = 0xFFFFFFFFFFFFFFFF


class Partitioner:
    """Maps a key to a logical partition number in [0, partition_count())."""

    def partition_of(self, key: str) -> int:
        raise NotImplementedError

    def partition_count(self) -> int:
        raise NotImplementedError

    @staticmethod
    def fnv1a64(key: str) -> int:
        """FNV-1a, 64-bit, over the UTF-16 code units Java's charAt sees (identical to the bytes for ASCII).

        Returned as an unsigned 64-bit int; Java's signed long has the same bits.
        Python's hash() is randomised per process, so it can never be used for routing.
        """
        h = 0xCBF29CE484222325
        data = key.encode("utf-16-be")
        for i in range(0, len(data), 2):
            h ^= (data[i] << 8) | data[i + 1]
            h = (h * 0x100000001B3) & MASK64
        return h

    @staticmethod
    def hash64(key: str) -> int:
        """FNV-1a followed by the SplitMix64 finaliser so the high bits are well mixed too."""
        z = Partitioner.fnv1a64(key)
        z = ((z ^ (z >> 30)) * 0xBF58476D1CE4E5B9) & MASK64
        z = ((z ^ (z >> 27)) * 0x94D049BB133111EB) & MASK64
        return z ^ (z >> 31)


class RangePartitioner(Partitioner):
    """Key ranges: partition i holds keys in [split_points[i-1], split_points[i]). Preserves sort order, invites hot ranges."""

    def __init__(self, split_points: list[str]) -> None:
        for i in range(1, len(split_points)):
            if split_points[i - 1] >= split_points[i]:
                raise ValueError("split points must be strictly increasing")
        self._split_points = tuple(split_points)

    def partition_of(self, key: str) -> int:
        lo, hi = 0, len(self._split_points)  # find the number of split points <= key
        while lo < hi:
            mid = (lo + hi) >> 1
            if self._split_points[mid] <= key:
                lo = mid + 1
            else:
                hi = mid
        return lo

    def partition_count(self) -> int:
        return len(self._split_points) + 1

    def split_points(self) -> tuple[str, ...]:
        return self._split_points


class HashPartitioner(Partitioner):
    """Hash of the key modulo a FIXED partition count. Spreads keys uniformly; destroys range scans."""

    def __init__(self, partitions: int) -> None:
        if partitions <= 0:
            raise ValueError("partition count must be positive")
        self._partitions = partitions

    def partition_of(self, key: str) -> int:
        return Partitioner.hash64(key) % self._partitions  # unsigned already: Java needs Long.remainderUnsigned

    def partition_count(self) -> int:
        return self._partitions


class ConsistentHashRing:
    """Consistent hashing: nodes own arcs of a 64-bit ring; adding a node moves only the keys on the arcs it takes."""

    def __init__(self, virtual_nodes: int) -> None:
        if virtual_nodes <= 0:
            raise ValueError("virtual nodes must be positive")
        self._virtual_nodes = virtual_nodes
        self._hashes: list[int] = []  # sorted; plays the role of TreeMap's key set
        self._owner: dict[int, str] = {}

    def add_node(self, node: str) -> None:
        for v in range(self._virtual_nodes):
            h = Partitioner.hash64(f"{node}#{v}")
            if h not in self._owner:
                bisect.insort(self._hashes, h)
            self._owner[h] = node

    def remove_node(self, node: str) -> None:
        for v in range(self._virtual_nodes):
            h = Partitioner.hash64(f"{node}#{v}")
            if h in self._owner:
                del self._owner[h]
                self._hashes.pop(bisect.bisect_left(self._hashes, h))

    def node_for(self, key: str) -> str:
        if not self._hashes:
            raise RuntimeError("ring has no nodes")
        i = bisect.bisect_left(self._hashes, Partitioner.hash64(key))  # ceilingEntry
        return self._owner[self._hashes[i if i < len(self._hashes) else 0]]

    def size(self) -> int:
        return len(self._hashes)


class RoutingTable:
    """Versioned partition -> node map. Every change bumps the version so a stale client can be told to refresh."""

    def __init__(self, partitioner: Partitioner, nodes: list[str]) -> None:
        if not nodes:
            raise ValueError("need at least one node")
        self._partitioner = partitioner
        self._owner = [nodes[p % len(nodes)] for p in range(partitioner.partition_count())]  # round-robin layout
        self._version = 1

    def version(self) -> int:
        return self._version

    def partitioner(self) -> Partitioner:
        return self._partitioner

    def partition_count(self) -> int:
        return len(self._owner)

    def owner_of(self, partition: int) -> str:
        return self._owner[partition]

    def node_for(self, key: str) -> str:
        return self._owner[self._partitioner.partition_of(key)]

    def assign(self, partition: int, node: str) -> None:
        if node is None:
            raise TypeError("node must not be None")
        if node != self._owner[partition]:
            self._owner[partition] = node
            self._version += 1

    def nodes(self) -> list[str]:
        return sorted(set(self._owner))

    def partitions_per_node(self) -> dict[str, int]:
        counts: dict[str, int] = {}
        for n in self._owner:
            counts[n] = counts.get(n, 0) + 1
        return dict(sorted(counts.items()))

    def partitions_of(self, node: str) -> list[int]:
        """Partitions currently owned by node, in ascending order."""
        return [p for p, n in enumerate(self._owner) if n == node]


class Rebalancer:
    """Plans the minimum set of partition moves that makes a routing table balanced after a node joins or leaves."""

    @dataclass(frozen=True)
    class Move:
        partition: int
        from_: str
        to: str

    @staticmethod
    def plan_add_node(table: RoutingTable, new_node: str) -> list["Rebalancer.Move"]:
        """Add node; repeatedly take one partition from the most loaded node until the new node has floor(P / N)."""
        if new_node in table.nodes():
            raise ValueError(f"node already present: {new_node}")
        moves: list[Rebalancer.Move] = []
        owned: dict[str, list[int]] = {n: list(table.partitions_of(n)) for n in table.nodes()}
        owned[new_node] = []
        target = table.partition_count() // len(owned)  # the new node gets floor(P / N)
        while len(owned[new_node]) < target:
            donor = Rebalancer._most_loaded(owned)
            partition = owned[donor].pop()  # take the highest-numbered partition: deterministic
            owned[new_node].append(partition)
            moves.append(Rebalancer.Move(partition, donor, new_node))
        return moves

    @staticmethod
    def plan_remove_node(table: RoutingTable, leaving: str) -> list["Rebalancer.Move"]:
        """Remove node; hand each of its partitions to the currently least loaded survivor."""
        if leaving not in table.nodes():
            raise ValueError(f"node not present: {leaving}")
        if len(table.nodes()) == 1:
            raise RuntimeError("cannot remove the last node")
        moves: list[Rebalancer.Move] = []
        owned = {n: list(table.partitions_of(n)) for n in table.nodes() if n != leaving}
        for partition in table.partitions_of(leaving):
            receiver = Rebalancer._least_loaded(owned)
            owned[receiver].append(partition)
            moves.append(Rebalancer.Move(partition, leaving, receiver))
        return moves

    @staticmethod
    def apply(table: RoutingTable, moves: list["Rebalancer.Move"]) -> None:
        for m in moves:
            table.assign(m.partition, m.to)

    @staticmethod
    def _most_loaded(owned: dict[str, list[int]]) -> str:
        best = None
        for name, parts in owned.items():  # dict order is insertion order, like LinkedHashMap
            if best is None or len(parts) > len(owned[best]):
                best = name
        return best

    @staticmethod
    def _least_loaded(owned: dict[str, list[int]]) -> str:
        best = None
        for name, parts in owned.items():
            if best is None or len(parts) < len(owned[best]):
                best = name
        return best


class HotKeyDetector:
    """Counts requests per partition and reports skew: how much hotter the hottest partition is than the average."""

    def __init__(self, partitions: int) -> None:
        self._hits = [0] * partitions
        self._total = 0

    def record(self, partition: int) -> None:
        self._hits[partition] += 1
        self._total += 1

    def hottest_partition(self) -> int:
        best = 0
        for p in range(1, len(self._hits)):
            if self._hits[p] > self._hits[best]:
                best = p
        return best

    def hits(self, partition: int) -> int:
        return self._hits[partition]

    def skew(self) -> float:
        """max / mean, so 1.0 is perfectly balanced and N means one partition takes everything."""
        if self._total == 0:
            return 1.0
        mean = self._total / len(self._hits)
        return self._hits[self.hottest_partition()] / mean

    @staticmethod
    def salt(key: str, secondary: str, fanout: int) -> str:
        """Append a small salt derived from a secondary field so one logical key spreads over `fanout` partitions."""
        s = Partitioner.hash64(secondary) % fanout
        return f"{key}#{s}"


class WrongOwnerException(RuntimeError):
    """Raised by a node that receives a request for a partition it no longer owns; carries the version to fetch."""

    def __init__(self, partition: int, current_version: int) -> None:
        super().__init__(f"partition {partition} not owned here; routing table is now at version {current_version}")
        self.partition = partition
        self.current_version = current_version


class StorageNode:
    """An in-memory storage node that only answers for partitions the routing table says it owns."""

    def __init__(self, name: str, table: RoutingTable) -> None:
        self.name = name
        self._data: dict[str, str] = {}
        self._table = table

    def put(self, key: str, value: str) -> None:
        self._require_owner(key)
        self._data[key] = value

    def get(self, key: str) -> str | None:
        self._require_owner(key)
        return self._data.get(key)

    def put_direct(self, key: str, value: str) -> None:
        """Used by the migration itself, which legitimately writes into a node that is not yet the owner."""
        self._data[key] = value

    def snapshot_of_partition(self, partition: int) -> dict[str, str]:
        part = self._table.partitioner()
        return {k: self._data[k] for k in sorted(self._data) if part.partition_of(k) == partition}

    def drop_partition(self, partition: int) -> int:
        part = self._table.partitioner()
        doomed = [k for k in self._data if part.partition_of(k) == partition]
        for k in doomed:
            del self._data[k]
        return len(doomed)

    def size(self) -> int:
        return len(self._data)

    def _require_owner(self, key: str) -> None:
        p = self._table.partitioner().partition_of(key)
        if self._table.owner_of(p) != self.name:
            raise WrongOwnerException(p, self._table.version())


class PartitionMigration:
    """Moves one partition from source to destination without refusing writes: copy, dual-write, cutover, drop."""

    class State(Enum):
        PLANNED = "PLANNED"
        COPYING = "COPYING"
        DUAL_WRITE = "DUAL_WRITE"
        CUT_OVER = "CUT_OVER"
        DONE = "DONE"

    def __init__(self, partition: int, source: StorageNode, destination: StorageNode, table: RoutingTable) -> None:
        if table.owner_of(partition) != source.name:
            raise ValueError(f"{source.name} does not own partition {partition}")
        self.partition = partition
        self.source = source
        self.destination = destination
        self._table = table
        self._state = PartitionMigration.State.PLANNED
        self.copied = 0
        self.dual_writes = 0

    def state(self) -> "PartitionMigration.State":
        return self._state

    def start_copy(self) -> None:
        """Phase 1: snapshot the partition and copy it. Writes keep going to source only."""
        self._require_state(PartitionMigration.State.PLANNED)
        self._state = PartitionMigration.State.COPYING
        for k, v in self.source.snapshot_of_partition(self.partition).items():
            self.destination.put_direct(k, v)
            self.copied += 1
        self._state = PartitionMigration.State.DUAL_WRITE

    def write(self, key: str, value: str) -> None:
        """Phase 2: every write to this partition goes to both nodes until cutover. Reads still go to source."""
        if self._table.partitioner().partition_of(key) != self.partition:
            raise ValueError("key not in partition")
        s = PartitionMigration.State
        if self._state is s.DUAL_WRITE:
            self.source.put(key, value)
            self.destination.put_direct(key, value)
            self.dual_writes += 1
        elif self._state in (s.PLANNED, s.COPYING):
            self.source.put(key, value)
        else:
            self.destination.put(key, value)

    def cut_over(self) -> None:
        """Phase 3: flip the routing table. From now on source rejects the partition with WrongOwnerException."""
        self._require_state(PartitionMigration.State.DUAL_WRITE)
        self._table.assign(self.partition, self.destination.name)
        self._state = PartitionMigration.State.CUT_OVER

    def drop_source(self) -> int:
        """Phase 4: once no client can still be mid-write to source, free the old copy."""
        self._require_state(PartitionMigration.State.CUT_OVER)
        dropped = self.source.drop_partition(self.partition)
        self._state = PartitionMigration.State.DONE
        return dropped

    def _require_state(self, expected: "PartitionMigration.State") -> None:
        if self._state is not expected:
            raise RuntimeError(f"expected {expected.value} but migration is {self._state.value}")


def moved_fraction(keys: list[str], before: Callable[[str], str], after: Callable[[str], str]) -> float:
    """Fraction of keys whose owner changes when the layout goes from `before` to `after`."""
    moved = sum(1 for k in keys if before(k) != after(k))
    return moved / len(keys)


def payment_ids(n: int) -> list[str]:
    return [f"pay_{i:08d}" for i in range(n)]


def _java_map(m: dict[str, int]) -> str:
    return "{" + ", ".join(f"{k}={v}" for k, v in m.items()) + "}"


def main() -> None:
    keys = payment_ids(100_000)
    nodes = ["node-a", "node-b", "node-c", "node-d"]

    print("== Range vs hash: where do merchants land? ==")
    by_merchant_prefix = RangePartitioner(["f", "m", "s"])  # [..f) [f..m) [m..s) [s..)
    by_hash = HashPartitioner(4)
    for m in ["acme", "globex", "megamart", "zenith"]:
        print(f"  merchant {m:<9} range-partition={by_merchant_prefix.partition_of(m)} "
              f"hash-partition={by_hash.partition_of(m)}")

    print()
    print("== Hot key: one merchant is 60% of traffic ==")
    plain = HotKeyDetector(16)
    salted = HotKeyDetector(16)
    p16 = HashPartitioner(16)
    for i in range(10_000):
        merchant = "megamart" if i % 10 < 6 else f"merchant-{i % 97}"
        plain.record(p16.partition_of(merchant))
        salted.record(p16.partition_of(HotKeyDetector.salt(merchant, f"pay_{i}", 8)))
    print(f"  unsalted: hottest partition {plain.hottest_partition()} takes "
          f"{plain.hits(plain.hottest_partition())} of 10000 (skew {plain.skew():.1f}x)")
    print(f"  salted x8: hottest partition {salted.hottest_partition()} takes "
          f"{salted.hits(salted.hottest_partition())} of 10000 (skew {salted.skew():.1f}x); "
          "reads for megamart now fan out to 8 partitions")

    print()
    print("== Adding a 5th node: how many keys move? ==")
    mod4 = HashPartitioner(4)
    mod5 = HashPartitioner(5)
    naive = moved_fraction(keys, lambda k: f"n{mod4.partition_of(k)}", lambda k: f"n{mod5.partition_of(k)}")
    print(f"  hash mod N, N 4 -> 5:               {naive * 100:.1f}% of keys move (almost everything)")

    fixed = RoutingTable(HashPartitioner(64), nodes)
    owner_before = {k: fixed.node_for(k) for k in keys}
    moves = Rebalancer.plan_add_node(fixed, "node-e")
    Rebalancer.apply(fixed, moves)
    fixed_moved = moved_fraction(keys, owner_before.get, fixed.node_for)
    print(f"  64 fixed partitions, 4 -> 5 nodes:  {fixed_moved * 100:.1f}% of keys move "
          f"({len(moves)} partition moves, layout {_java_map(fixed.partitions_per_node())})")

    ring = ConsistentHashRing(150)
    for n in nodes:
        ring.add_node(n)
    ring_before = {k: ring.node_for(k) for k in keys}
    ring.add_node("node-e")
    ring_moved = moved_fraction(keys, ring_before.get, ring.node_for)
    print(f"  consistent hashing, 150 vnodes:     {ring_moved * 100:.1f}% of keys move (ideal 1/5 = 20.0%)")

    print()
    print("== Moving partition 6 from node-a to node-e without downtime ==")
    table = RoutingTable(HashPartitioner(8), ["node-a", "node-b"])
    a = StorageNode("node-a", table)
    e = StorageNode("node-e", table)
    loaded = 0
    for k in keys[:2_000]:
        if table.partitioner().partition_of(k) == 6:
            a.put(k, "v1")
            loaded += 1
    print(f"  partition 6 has {loaded} keys on node-a; routing table v{table.version()}")
    mig = PartitionMigration(6, a, e, table)
    mig.start_copy()
    print(f"  bulk copy done: {mig.copied} keys copied, state {mig.state().value}")
    live = next(k for k in keys[2_000:] if table.partitioner().partition_of(k) == 6)
    mig.write(live, "v2-written-during-migration")
    print(f"  live write to {live} during migration went to both nodes (dual writes: {mig.dual_writes})")
    mig.cut_over()
    print(f"  cut over: routing table v{table.version()}, partition 6 owner = {table.owner_of(6)}")
    try:
        a.get(live)
    except WrongOwnerException as ex:
        print(f"  stale client asked node-a: {ex}")
    print(f"  node-e serves {live} = {e.get(live)}")
    print(f"  dropped {mig.drop_source()} keys from node-a; migration {mig.state().value}")


if __name__ == "__main__":
    main()
