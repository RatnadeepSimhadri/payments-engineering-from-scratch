"""Lesson: phases/01-complexity-and-core-structures/06-hash-maps-from-scratch/docs/en.md
Hash Maps from Scratch: Chaining, Open Addressing, and Resizing
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: Knuth TAOCP Vol. 3 Section 6.4 (hashing, linear probing analysis); FNV-1a (Fowler, Noll, Vo);
         Stripe API "Idempotent requests" for the store framing.
Python 3.11+, standard library only. Same design as the Java and Go versions.
"""

from __future__ import annotations

from dataclasses import dataclass
from typing import Callable, Generic, Hashable, Optional, TypeVar

K = TypeVar("K", bound=Hashable)
V = TypeVar("V")

MASK32 = 0xFFFFFFFF


def fnv1a(s: str) -> int:
    """FNV-1a, 32-bit, over the UTF-8 bytes. Same result in Java, Python, and Go.
    Python's built-in hash(str) is salted per process (PYTHONHASHSEED), so it is useless for a
    deterministic demo; this one is not."""
    h = 0x811C9DC5
    for b in s.encode("utf-8"):
        h ^= b
        h = (h * 0x01000193) & MASK32
    return h


def constant_hasher(_key: object) -> int:
    """A pathological hasher: every key lands in bucket 0. Used to measure worst-case behaviour."""
    return 0


def index_for(hash32: int, capacity: int) -> int:
    """Map a 32-bit hash to a slot in a power-of-two table, mixing the high bits into the low ones first."""
    spread = (hash32 ^ (hash32 >> 16)) & MASK32
    return spread & (capacity - 1)


def _require_power_of_two(capacity: int) -> None:
    if capacity <= 0 or capacity & (capacity - 1):
        raise ValueError(f"capacity must be a power of two: {capacity}")


class _Node(Generic[K, V]):
    __slots__ = ("key", "value", "next")

    def __init__(self, key: K, value: V, nxt: Optional["_Node[K, V]"]) -> None:
        self.key = key
        self.value = value
        self.next = nxt


class ChainedHashMap(Generic[K, V]):
    """Separate chaining: each bucket is the head of a singly linked list of entries."""

    DEFAULT_CAPACITY = 8
    MAX_LOAD = 0.75

    def __init__(self, hasher: Callable[[K], int], capacity: int = DEFAULT_CAPACITY) -> None:
        _require_power_of_two(capacity)
        self._hasher = hasher
        self._table: list[Optional[_Node[K, V]]] = [None] * capacity
        self._size = 0
        self._collisions = 0
        self._resizes = 0

    def _bucket_of(self, key: K) -> int:
        return index_for(self._hasher(key), len(self._table))

    def put(self, key: K, value: V) -> Optional[V]:
        if key is None:
            raise TypeError("key must not be None")
        b = self._bucket_of(key)
        n = self._table[b]
        while n is not None:
            if n.key == key:
                old = n.value
                n.value = value
                return old
            n = n.next
        if self._table[b] is not None:
            self._collisions += 1
        self._table[b] = _Node(key, value, self._table[b])
        self._size += 1
        if self._size > len(self._table) * self.MAX_LOAD:
            self._resize(len(self._table) * 2)
        return None

    def get(self, key: K) -> Optional[V]:
        n = self._table[self._bucket_of(key)]
        while n is not None:
            if n.key == key:
                return n.value
            n = n.next
        return None

    def contains_key(self, key: K) -> bool:
        n = self._table[self._bucket_of(key)]
        while n is not None:
            if n.key == key:
                return True
            n = n.next
        return False

    def remove(self, key: K) -> Optional[V]:
        b = self._bucket_of(key)
        prev: Optional[_Node[K, V]] = None
        n = self._table[b]
        while n is not None:
            if n.key == key:
                if prev is None:
                    self._table[b] = n.next
                else:
                    prev.next = n.next
                self._size -= 1
                return n.value
            prev, n = n, n.next
        return None

    def _resize(self, new_capacity: int) -> None:
        old = self._table
        self._table = [None] * new_capacity
        for head in old:
            n = head
            while n is not None:
                nxt = n.next
                b = index_for(self._hasher(n.key), new_capacity)
                n.next = self._table[b]
                self._table[b] = n
                n = nxt
        self._resizes += 1

    def max_chain_length(self) -> int:
        """Longest chain in the table: the worst-case lookup cost right now."""
        best = 0
        for head in self._table:
            length = 0
            n = head
            while n is not None:
                length += 1
                n = n.next
            best = max(best, length)
        return best

    def size(self) -> int:
        return self._size

    def capacity(self) -> int:
        return len(self._table)

    def load_factor(self) -> float:
        return self._size / len(self._table)

    def collisions(self) -> int:
        return self._collisions

    def resizes(self) -> int:
        return self._resizes


_EMPTY, _OCCUPIED, _TOMBSTONE = 0, 1, 2


class OpenAddressingHashMap(Generic[K, V]):
    """Open addressing with linear probing and tombstones: keys, values, and states live in flat lists."""

    DEFAULT_CAPACITY = 8
    MAX_LOAD = 0.5

    def __init__(self, hasher: Callable[[K], int], capacity: int = DEFAULT_CAPACITY) -> None:
        _require_power_of_two(capacity)
        self._hasher = hasher
        self._allocate(capacity)
        self._size = 0
        self._collisions = 0  # total displacement: sum over new-key inserts of (slot used - home slot)
        self._resizes = 0

    def _allocate(self, capacity: int) -> None:
        self._keys: list[Optional[K]] = [None] * capacity
        self._values: list[Optional[V]] = [None] * capacity
        self._states = bytearray(capacity)
        self._tombstones = 0

    def _home_of(self, key: K) -> int:
        return index_for(self._hasher(key), len(self._keys))

    def slot_of(self, key: K) -> int:
        """Slot holding the key, or -1. Probing stops at EMPTY and walks through TOMBSTONEs."""
        cap = len(self._keys)
        i = self._home_of(key)
        for _ in range(cap):
            state = self._states[i]
            if state == _EMPTY:
                return -1
            if state == _OCCUPIED and self._keys[i] == key:
                return i
            i = (i + 1) & (cap - 1)
        return -1

    def put(self, key: K, value: V) -> Optional[V]:
        if key is None:
            raise TypeError("key must not be None")
        if self._size + self._tombstones + 1 > len(self._keys) * self.MAX_LOAD:
            self._resize(len(self._keys) * 2)
        cap = len(self._keys)
        home = self._home_of(key)
        i = home
        first_tombstone = -1
        displacement = 0
        while True:
            state = self._states[i]
            if state == _EMPTY:
                break
            if state == _OCCUPIED and self._keys[i] == key:
                old = self._values[i]
                self._values[i] = value
                return old
            if state == _TOMBSTONE and first_tombstone < 0:
                first_tombstone = i
            i = (i + 1) & (cap - 1)
            displacement += 1
        if first_tombstone >= 0:  # reuse the grave rather than extend the cluster
            i = first_tombstone
            self._tombstones -= 1
            displacement = (i - home) & (cap - 1)
        self._collisions += displacement
        self._keys[i] = key
        self._values[i] = value
        self._states[i] = _OCCUPIED
        self._size += 1
        return None

    def get(self, key: K) -> Optional[V]:
        i = self.slot_of(key)
        return None if i < 0 else self._values[i]

    def contains_key(self, key: K) -> bool:
        return self.slot_of(key) >= 0

    def remove(self, key: K) -> Optional[V]:
        i = self.slot_of(key)
        if i < 0:
            return None
        old = self._values[i]
        self._keys[i] = None
        self._values[i] = None
        self._states[i] = _TOMBSTONE  # not EMPTY: a probe chain may run through this slot
        self._tombstones += 1
        self._size -= 1
        return old

    def _resize(self, new_capacity: int) -> None:
        old_keys, old_values, old_states = self._keys, self._values, self._states
        self._allocate(new_capacity)
        for j, state in enumerate(old_states):
            if state != _OCCUPIED:  # tombstones are dropped here
                continue
            k = old_keys[j]
            i = index_for(self._hasher(k), new_capacity)
            while self._states[i] != _EMPTY:
                i = (i + 1) & (new_capacity - 1)
            self._keys[i] = k
            self._values[i] = old_values[j]
            self._states[i] = _OCCUPIED
        self._resizes += 1

    def max_probe_run(self) -> int:
        """Longest run of non-EMPTY slots: the worst-case probe length for a miss right now."""
        best = run = 0
        cap = len(self._states)
        for _ in range(2):  # two passes so a run wrapping the end is seen whole
            for state in self._states:
                if state == _EMPTY:
                    best = max(best, run)
                    run = 0
                else:
                    run = min(run + 1, cap)
        return max(best, run)

    def size(self) -> int:
        return self._size

    def capacity(self) -> int:
        return len(self._keys)

    def load_factor(self) -> float:
        return self._size / len(self._keys)

    def collisions(self) -> int:
        return self._collisions

    def tombstones(self) -> int:
        return self._tombstones

    def resizes(self) -> int:
        return self._resizes


@dataclass(frozen=True)
class StoredResponse:
    """What an API server stores per idempotency key: the status and body it already sent."""

    status: int
    body: str


@dataclass(frozen=True)
class Outcome:
    """The result of asking the store to handle a request: the response, and whether it was replayed."""

    response: StoredResponse
    replayed: bool


class IdempotencyStore:
    """key = client-supplied idempotency key, value = the response first produced for it.
    The first call with a key runs the handler and records its response; every later call replays it
    without running the handler again, so a retried "charge 19.99 USD" charges once."""

    def __init__(self, backing: ChainedHashMap[str, StoredResponse] | OpenAddressingHashMap[str, StoredResponse]) -> None:
        self._responses = backing
        self._handler_runs = 0

    def handle(self, idempotency_key: str, handler: Callable[[], StoredResponse]) -> Outcome:
        existing = self._responses.get(idempotency_key)
        if existing is not None:
            return Outcome(existing, True)
        fresh = handler()
        self._handler_runs += 1
        self._responses.put(idempotency_key, fresh)
        return Outcome(fresh, False)

    def handler_runs(self) -> int:
        return self._handler_runs

    def size(self) -> int:
        return self._responses.size()


def keys(n: int) -> list[str]:
    return [f"idem-{i:06d}" for i in range(n)]


def main() -> None:
    print("== FNV-1a is the same in every language ==")
    print(f'fnv1a("idem-000001") = 0x{fnv1a("idem-000001"):08x}')
    print(f'fnv1a("") = 0x{fnv1a(""):08x}')

    print("\n== 10,000 idempotency keys, separate chaining ==")
    chained: ChainedHashMap[str, int] = ChainedHashMap(fnv1a)
    ks = keys(10_000)
    for i, k in enumerate(ks):
        chained.put(k, i)
    print(f"size={chained.size()} capacity={chained.capacity()} load={chained.load_factor():.3f} "
          f"resizes={chained.resizes()} collisions={chained.collisions()} maxChain={chained.max_chain_length()}")

    print("\n== Same keys, open addressing (linear probing) ==")
    open_map: OpenAddressingHashMap[str, int] = OpenAddressingHashMap(fnv1a)
    for i, k in enumerate(ks):
        open_map.put(k, i)
    print(f"size={open_map.size()} capacity={open_map.capacity()} load={open_map.load_factor():.3f} "
          f"resizes={open_map.resizes()} displacement={open_map.collisions()} maxProbeRun={open_map.max_probe_run()}")

    print("\n== Tombstones: remove half, re-insert, probe chains survive ==")
    for k in ks[::2]:
        open_map.remove(k)
    print(f"after removing 5,000: size={open_map.size()} tombstones={open_map.tombstones()} "
          f"get(idem-000001)={open_map.get('idem-000001')}")
    for i in range(0, len(ks), 2):
        open_map.put(ks[i], -i)
    print(f"after re-inserting: size={open_map.size()} tombstones={open_map.tombstones()} (graves were reused)")

    print("\n== Worst case: a hasher that returns 0 for every key ==")
    bad: ChainedHashMap[str, int] = ChainedHashMap(constant_hasher)
    bad_open: OpenAddressingHashMap[str, int] = OpenAddressingHashMap(constant_hasher)
    for i in range(64):
        bad.put(ks[i], i)
        bad_open.put(ks[i], i)
    print(f"chaining: 64 keys -> collisions={bad.collisions()} maxChain={bad.max_chain_length()} (every lookup is O(n))")
    print(f"probing:  64 keys -> displacement={bad_open.collisions()} maxProbeRun={bad_open.max_probe_run()} (sum 0..63 = 2016)")

    print("\n== Idempotency store: retry the same charge three times ==")
    store = IdempotencyStore(ChainedHashMap(fnv1a))
    for attempt in range(1, 4):
        o = store.handle("order-8841-charge",
                         lambda: StoredResponse(201, '{"charge":"ch_1","amount":1999,"currency":"USD"}'))
        print(f"attempt {attempt}: status={o.response.status} replayed={str(o.replayed).lower()} body={o.response.body}")
    print(f"handler ran {store.handler_runs()} time(s) for 3 attempts")


if __name__ == "__main__":
    main()
