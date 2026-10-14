"""Lesson: phases/06-replication-and-consensus/09-conflict-resolution/docs/en.md
Conflict Resolution: LWW, Version Vectors, Merge Functions
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: DeCandia et al., "Dynamo: Amazon's Highly Available Key-value Store" (SOSP 2007), section 4.4;
Parker et al., "Detection of Mutual Inconsistency in Distributed Systems" (IEEE TSE 1983). Python 3.11+, stdlib only.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from enum import Enum
from typing import Callable, Generic, Optional, TypeVar

V = TypeVar("V")


def fmt_map(m: dict) -> str:
    """Java's TreeMap.toString(): "{A=1, B=1}"."""
    return "{" + ", ".join(f"{k}={v}" for k, v in sorted(m.items())) + "}"


def fmt_set(xs) -> str:
    """Java's TreeSet.toString(): "[a, b]"."""
    return "[" + ", ".join(str(x) for x in sorted(xs)) + "]"


class Ordering(Enum):
    """How two versions relate under the happens-before order."""

    BEFORE = "BEFORE"
    AFTER = "AFTER"
    EQUAL = "EQUAL"
    CONCURRENT = "CONCURRENT"

    def __str__(self) -> str:
        return self.value


class VersionVector:
    """Per-replica counters. v1 dominates v2 when every entry of v1 >= v2's and at least one is greater. Immutable:
    increment and merge return new vectors."""

    def __init__(self, initial: Optional[dict[str, int]] = None) -> None:
        self._clocks: dict[str, int] = dict(initial or {})

    def copy(self) -> "VersionVector":
        return VersionVector(self._clocks)

    def get(self, replica: str) -> int:
        return self._clocks.get(replica, 0)

    def increment(self, replica: str) -> "VersionVector":
        """Returns a new vector with this replica's entry advanced by one."""
        out = self.copy()
        out._clocks[replica] = out._clocks.get(replica, 0) + 1
        return out

    def merge(self, other: "VersionVector") -> "VersionVector":
        """Pointwise maximum: the smallest vector that dominates both."""
        out = self.copy()
        for k, v in other._clocks.items():
            out._clocks[k] = max(out._clocks.get(k, v), v)
        return out

    def compare(self, other: "VersionVector") -> Ordering:
        less = greater = False
        for k in sorted(set(self._clocks) | set(other._clocks)):
            a, b = self.get(k), other.get(k)
            if a < b:
                less = True
            if a > b:
                greater = True
        if less and greater:
            return Ordering.CONCURRENT
        if less:
            return Ordering.BEFORE
        if greater:
            return Ordering.AFTER
        return Ordering.EQUAL

    def dominates(self, other: "VersionVector") -> bool:
        return self.compare(other) == Ordering.AFTER

    def concurrent_with(self, other: "VersionVector") -> bool:
        return self.compare(other) == Ordering.CONCURRENT

    def __eq__(self, o: object) -> bool:
        return isinstance(o, VersionVector) and self._clocks == o._clocks

    def __hash__(self) -> int:
        return hash(tuple(sorted(self._clocks.items())))

    def __str__(self) -> str:
        return fmt_map(self._clocks)

    __repr__ = __str__


@dataclass(frozen=True, order=True)
class Dot:
    """A write's identity: the replica that coordinated it and that replica's monotonically increasing counter."""

    replica: str
    seq: int

    def __str__(self) -> str:
        return f"{self.replica}:{self.seq}"


@dataclass(frozen=True)
class Versioned(Generic[V]):
    """A value, the dot that identifies the write, and the context (version vector) the writer had observed.
    A write is obsolete exactly when some other write's context covers its dot: that writer had seen it."""

    value: V
    dot: Dot
    context: VersionVector

    def version(self) -> VersionVector:
        """The full version vector of this write: everything it saw plus itself."""
        v = self.context.copy()
        return v if v.get(self.dot.replica) >= self.dot.seq else v.merge(VersionVector({self.dot.replica: self.dot.seq}))

    def obsoleted_by(self, other: "Versioned[V]") -> bool:
        return other.context.get(self.dot.replica) >= self.dot.seq

    def __str__(self) -> str:
        return f"{self.value}@{self.version()}"


@dataclass(frozen=True)
class Entry(Generic[V]):
    value: V
    timestamp: int
    writer: str

    def beats(self, other: Optional["Entry[V]"]) -> bool:
        if other is None:
            return True
        if self.timestamp != other.timestamp:
            return self.timestamp > other.timestamp
        return self.writer > other.writer


class LWWStore(Generic[V]):
    """Last-writer-wins by wall-clock timestamp (replica id breaks ties). Keeps one value per key.
    Any write that loses the comparison is discarded with no record that it ever happened."""

    Entry = Entry

    def __init__(self) -> None:
        self._entries: dict[str, Entry[V]] = {}
        self._discarded = 0

    def put(self, key: str, value: V, timestamp: int, writer: str) -> None:
        incoming = Entry(value, timestamp, writer)
        current = self._entries.get(key)
        if incoming.beats(current):
            if current is not None:
                self._discarded += 1
            self._entries[key] = incoming
        else:
            self._discarded += 1

    def get(self, key: str) -> Optional[V]:
        e = self._entries.get(key)
        return None if e is None else e.value

    def discarded(self) -> int:
        """Count of writes that were overwritten or rejected: the data LWW lost."""
        return self._discarded

    def merge(self, other: "LWWStore[V]") -> "LWWStore[V]":
        out: LWWStore[V] = LWWStore()
        out._entries.update(self._entries)
        for k, e in other._entries.items():
            if e.beats(out._entries.get(k)):
                out._entries[k] = e
        return out


class MergeFunction(Generic[V]):
    """Application-level merge: given every concurrent sibling, produce the one value the application wants."""

    def __init__(self, fn: Callable[[list], V]) -> None:
        self._fn = fn

    def merge(self, siblings: list) -> V:
        return self._fn(siblings)


@dataclass(frozen=True)
class ReadResult(Generic[V]):
    """Read: every sibling plus the context (merged vector) a client must send back with its next write."""

    siblings: tuple
    context: VersionVector

    def values(self) -> list:
        return [s.value for s in self.siblings]

    def has_conflict(self) -> bool:
        return len(self.siblings) > 1


class SiblingStore(Generic[V]):
    """Dynamo-style store with dotted version vectors: each key holds a set of concurrent siblings. A write carries the
    context the client read; the store drops every sibling that context covers and keeps the rest."""

    ReadResult = ReadResult

    def __init__(self, replica: str) -> None:
        self._replica = replica
        self._counter = 0
        self._data: dict[str, list[Versioned[V]]] = {}

    def replica(self) -> str:
        return self._replica

    def get(self, key: str) -> ReadResult[V]:
        sibs = self._data.get(key, [])
        ctx = VersionVector()
        for s in sibs:
            ctx = ctx.merge(s.version())
        return ReadResult(tuple(sibs), ctx)

    def put(self, key: str, value: V, context: VersionVector) -> VersionVector:
        """Write with the context the client read. The new write gets a fresh dot from this replica's counter; it
        obsoletes every sibling its context covers and is concurrent with every sibling it does not."""
        self._counter += 1
        write = Versioned(value, Dot(self._replica, self._counter), context.copy())
        kept = [s for s in self._data.get(key, []) if not s.obsoleted_by(write)]
        kept.append(write)
        self._data[key] = kept
        return write.version()

    def put_blind(self, key: str, value: V) -> VersionVector:
        """Write with no context at all: the write is concurrent with everything already stored."""
        return self.put(key, value, VersionVector())

    def merge_from(self, other: "SiblingStore[V]") -> None:
        """Anti-entropy: take every sibling the other replica holds, keep only the ones nobody has obsoleted."""
        for key in sorted(other._data):
            all_ = list(self._data.get(key, []))
            for s in other._data[key]:
                if s not in all_:
                    all_.append(s)
            self._data[key] = SiblingStore.maximal(all_)

    def resolve(self, key: str, fn: MergeFunction[V]) -> Optional[V]:
        """Read every sibling, resolve with the application merge, write back with the merged context."""
        read = self.get(key)
        if not read.siblings:
            return None
        merged = read.siblings[0].value if len(read.siblings) == 1 else fn.merge(read.values())
        if len(read.siblings) > 1:
            self.put(key, merged, read.context)
        return merged

    def sibling_count(self, key: str) -> int:
        return len(self._data.get(key, []))

    @staticmethod
    def maximal(all_: list) -> list:
        """Siblings that no other sibling's context has observed. `is not` mirrors Java's reference comparison."""
        out: list = []
        for candidate in all_:
            obsolete = any(other is not candidate and candidate.obsoleted_by(other) for other in all_)
            if not obsolete and candidate not in out:
                out.append(candidate)
        return out


@dataclass(frozen=True)
class Cart:
    """Shopping cart: item -> quantity. Immutable value; operations return new carts."""

    items: dict = field(default_factory=dict)

    @staticmethod
    def empty() -> "Cart":
        return Cart({})

    def add(self, item: str, qty: int) -> "Cart":
        m = dict(self.items)
        m[item] = m.get(item, 0) + qty
        return Cart(m)

    def remove(self, item: str) -> "Cart":
        m = dict(self.items)
        m.pop(item, None)
        return Cart(m)

    def __str__(self) -> str:
        return fmt_map(self.items)


def _union_max(siblings: list) -> Cart:
    """Dynamo's cart merge: union of items, max quantity. Removed items can come back."""
    m: dict[str, int] = {}
    for c in siblings:
        for k, v in c.items.items():
            m[k] = max(m[k], v) if k in m else v
    return Cart(m)


Cart.UNION_MAX = MergeFunction(_union_max)

SEVERITY = ["OPEN", "UNDER_REVIEW", "FUNDS_HELD", "CLOSED_REFUNDED"]


@dataclass(frozen=True)
class CaseFile:
    """A dispute case file on a payment: free-text notes from agents plus a status."""

    status: str
    notes: frozenset

    SEVERITY = SEVERITY

    @staticmethod
    def open() -> "CaseFile":
        return CaseFile("OPEN", frozenset())

    def with_note(self, note: str) -> "CaseFile":
        return CaseFile(self.status, self.notes | {note})

    def with_status(self, s: str) -> "CaseFile":
        if s not in SEVERITY:
            raise ValueError(f"unknown status {s}")
        return CaseFile(s, frozenset(self.notes))

    def __str__(self) -> str:
        return f"{self.status} notes={fmt_set(self.notes)}"


def _union_notes_strictest_status(siblings: list) -> CaseFile:
    """Keep every note (union), and for status pick the most restrictive one so a partition can never silently release
    held funds. The rule is a business decision made explicit."""
    notes: set[str] = set()
    status = "OPEN"
    for c in siblings:
        notes |= c.notes
        if SEVERITY.index(c.status) > SEVERITY.index(status):
            status = c.status
    return CaseFile(status, frozenset(notes))


CaseFile.UNION_NOTES_STRICTEST_STATUS = MergeFunction(_union_notes_strictest_status)


def main() -> None:
    print("== Version vectors: what 'concurrent' means ==")
    base = VersionVector()
    a1 = base.increment("A")      # {A=1}
    a1b1 = a1.increment("B")      # {A=1, B=1}: B wrote after seeing A's write
    a2 = a1.increment("A")        # {A=2}: A wrote again without seeing B
    print(f"{a1} vs {a1b1} -> {a1.compare(a1b1)}")
    print(f"{a1b1} vs {a2} -> {a1b1.compare(a2)}  (neither saw the other: a real conflict)")
    print(f"merge = {a1b1.merge(a2)}  (the context a resolver writes back with)")

    print()
    print("== LWW: deterministic, and it throws one write away ==")
    east: LWWStore[str] = LWWStore()
    west: LWWStore[str] = LWWStore()
    east.put("case-9021/note", "agent-1: customer says card was stolen on 3 Oct", 1_000, "east")
    west.put("case-9021/note", "agent-2: merchant provided signed delivery receipt", 1_002, "west")
    lww = east.merge(west)
    print(f"east wrote at t=1000, west wrote at t=1002 -> merged value: \"{lww.get('case-9021/note')}\"")
    print("agent-1's note is gone; nothing in the store records that it existed")
    skewed: LWWStore[str] = LWWStore()
    skewed.put("balance-note", "newer write, but this node's clock is 5s behind", 995, "slow-clock")
    skewed.put("balance-note", "older write from a node with a fast clock", 1_000, "fast-clock")
    print(f"clock skew: \"{skewed.get('balance-note')}\" wins although it happened first")

    print()
    print("== Siblings: keep both, make the application decide ==")
    s_east: SiblingStore[CaseFile] = SiblingStore("east")
    s_west: SiblingStore[CaseFile] = SiblingStore("west")
    s_east.put("case-9021", CaseFile.open(), VersionVector())
    s_west.merge_from(s_east)   # both replicas hold version {east=1}
    # partition: each agent reads {east=1}, edits, writes back with that context
    east_read = s_east.get("case-9021")
    s_east.put("case-9021", east_read.siblings[0].value.with_note("agent-1: card reported stolen 3 Oct").with_status("FUNDS_HELD"),
               east_read.context)
    west_read = s_west.get("case-9021")
    s_west.put("case-9021", west_read.siblings[0].value.with_note("agent-2: merchant has signed receipt").with_status("UNDER_REVIEW"),
               west_read.context)
    s_east.merge_from(s_west)
    conflicted = s_east.get("case-9021")
    print(f"after anti-entropy east holds {len(conflicted.siblings)} siblings:")
    for s in conflicted.siblings:
        print(f"  {s}")
    resolved = s_east.resolve("case-9021", CaseFile.UNION_NOTES_STRICTEST_STATUS)
    print(f"application merge (union notes, strictest status): {resolved}")
    print(f"siblings after resolve: {s_east.sibling_count('case-9021')}, version {s_east.get('case-9021').context}")

    print()
    print("== The shopping-cart anomaly from the Dynamo paper ==")
    c_a: SiblingStore[Cart] = SiblingStore("A")
    c_b: SiblingStore[Cart] = SiblingStore("B")
    c_a.put("cart-7", Cart.empty().add("book", 1).add("pen", 2), VersionVector())
    c_b.merge_from(c_a)
    ra = c_a.get("cart-7")
    c_a.put("cart-7", ra.siblings[0].value.remove("pen"), ra.context)        # A deletes pen
    rb = c_b.get("cart-7")
    c_b.put("cart-7", rb.siblings[0].value.add("book", 1), rb.context)       # B adds another book
    c_a.merge_from(c_b)
    merged = c_a.resolve("cart-7", Cart.UNION_MAX)
    print(f"A removed pen, B concurrently added a book -> union/max merge: {merged}")
    print("the deleted pen is back: union of states cannot see a deletion (an OR-Set, Lesson 08, can)")


if __name__ == "__main__":
    main()
