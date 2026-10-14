"""Lesson: phases/04-concurrency/04-concurrent-hash-maps/docs/en.md
Concurrent Hash Maps: Striping and Lock-Free Reads.
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: JLS 17.4 (Java Memory Model); java.util.concurrent.ConcurrentHashMap Javadoc and source comments (Doug Lea);
Herlihy & Shavit, The Art of Multiprocessor Programming, ch. 13. Python 3.11+, standard library only.
"""

from __future__ import annotations

import threading
import time
from dataclasses import dataclass
from typing import Callable, Generic, Optional, TypeVar

K = TypeVar("K")
V = TypeVar("V")

MASK32 = (1 << 32) - 1
MASK64 = (1 << 64) - 1


def to_int32(v: int) -> int:
    """Wrap to a signed 32-bit value: Java int arithmetic."""
    v &= MASK32
    return v - (1 << 32) if v >= (1 << 31) else v


def _require(x, what: str):
    if x is None:
        raise TypeError(what)  # Java: NullPointerException from Objects.requireNonNull
    return x


class AtomicInteger:
    """A counter whose increment cannot be lost: += is three bytecodes, and a thread switch can split them."""

    def __init__(self, value: int = 0) -> None:
        self._value = value
        self._lock = threading.Lock()

    def get(self) -> int:
        return self._value

    def increment_and_get(self) -> int:
        with self._lock:
            self._value += 1
            return self._value

    def decrement_and_get(self) -> int:
        with self._lock:
            self._value -= 1
            return self._value


class Hashing:
    """Hashing shared by every implementation so the same key lands in the same bucket in every language port."""

    @staticmethod
    def stable_hash(key) -> int:
        """Java's String.hashCode written out; stable across processes and languages (Python's hash() is salted)."""
        if isinstance(key, str):
            h = 0
            data = key.encode("utf-16-be")  # charAt() returns UTF-16 code units
            for i in range(0, len(data), 2):
                h = (31 * h + ((data[i] << 8) | data[i + 1])) & MASK32
            return to_int32(h)
        return to_int32(hash(key))  # non-string keys: only stable within one process

    @staticmethod
    def spread(h: int) -> int:
        """The MurmurHash3 finaliser in 32-bit arithmetic: every input bit reaches every output bit."""
        u = h & MASK32
        u ^= u >> 16
        u = (u * 0x85EBCA6B) & MASK32
        u ^= u >> 13
        u = (u * 0xC2B2AE35) & MASK32
        u ^= u >> 16
        return to_int32(u)

    @staticmethod
    def bucket_index(key, table_length: int) -> int:
        return Hashing.spread(Hashing.stable_hash(key)) & (table_length - 1)


class ConcurrentStore(Generic[K, V]):
    """The operations every map in this lesson supports; the same names as java.util.concurrent.ConcurrentMap."""

    def get(self, key: K) -> Optional[V]:
        raise NotImplementedError

    def put(self, key: K, value: V) -> Optional[V]:
        """Returns the previous value or None."""
        raise NotImplementedError

    def put_if_absent(self, key: K, value: V) -> Optional[V]:
        """Atomic check-then-act: stores only if absent; returns the existing value, or None if this call stored."""
        raise NotImplementedError

    def remove(self, key: K) -> Optional[V]:
        raise NotImplementedError

    def compute(self, key: K, fn: Callable[[K, Optional[V]], Optional[V]]) -> Optional[V]:
        """Atomic read-modify-write for one key; fn gets the current value (or None) and returns the new one."""
        raise NotImplementedError

    def size(self) -> int:
        raise NotImplementedError


class GlobalLockMap(ConcurrentStore[K, V]):
    """Baseline: one dict behind one lock. Correct, simple, and the throughput ceiling the other two beat."""

    def __init__(self) -> None:
        self._map: dict = {}
        self._lock = threading.Lock()

    def get(self, key):
        _require(key, "key")
        with self._lock:
            return self._map.get(key)

    def put(self, key, value):
        _require(key, "key")
        _require(value, "value")
        with self._lock:
            old = self._map.get(key)
            self._map[key] = value
            return old

    def put_if_absent(self, key, value):
        _require(key, "key")
        _require(value, "value")
        with self._lock:
            existing = self._map.get(key)
            if existing is None:
                self._map[key] = value
            return existing

    def remove(self, key):
        _require(key, "key")
        with self._lock:
            return self._map.pop(key, None)

    def compute(self, key, fn):
        _require(key, "key")
        with self._lock:
            nxt = fn(key, self._map.get(key))
            if nxt is None:
                self._map.pop(key, None)
            else:
                self._map[key] = nxt
            return nxt

    def size(self):
        with self._lock:
            return len(self._map)


class _StripedNode:
    __slots__ = ("key", "value", "next")

    def __init__(self, key, value, nxt) -> None:
        self.key = key
        self.value = value
        self.next = nxt


class Segment:
    """One stripe: a tiny single-threaded hash map guarded by its own lock."""

    def __init__(self, initial_buckets: int) -> None:
        self.lock = threading.Lock()
        self.table: list[Optional[_StripedNode]] = [None] * initial_buckets
        self.count = 0
        self.resizes = 0

    def find(self, key) -> Optional[_StripedNode]:
        n = self.table[Hashing.bucket_index(key, len(self.table))]
        while n is not None:
            if n.key == key:
                return n
            n = n.next
        return None

    def insert(self, key, value) -> None:
        if self.count + 1 > len(self.table) * 3 // 4:
            self.grow()
        i = Hashing.bucket_index(key, len(self.table))
        self.table[i] = _StripedNode(key, value, self.table[i])
        self.count += 1

    def unlink(self, key):
        i = Hashing.bucket_index(key, len(self.table))
        prev, n = None, self.table[i]
        while n is not None:
            if n.key == key:
                if prev is None:
                    self.table[i] = n.next
                else:
                    prev.next = n.next
                self.count -= 1
                return n.value
            prev, n = n, n.next
        return None

    def grow(self) -> None:
        old = self.table
        fresh: list[Optional[_StripedNode]] = [None] * (len(old) * 2)
        for head in old:
            n = head
            while n is not None:
                i = Hashing.bucket_index(n.key, len(fresh))
                fresh[i] = _StripedNode(n.key, n.value, fresh[i])
                n = n.next
        self.table = fresh
        self.resizes += 1


class StripedHashMap(ConcurrentStore[K, V]):
    """Lock striping: the key space is split into N independent segments, each with its own lock, bucket table,
    count, and resize. Two operations contend only when their keys hash to the same segment. Reads take the
    segment lock too, which is the limitation LockFreeReadMap removes."""

    def __init__(self, stripes: int = 16) -> None:
        if stripes <= 0 or bin(stripes).count("1") != 1:
            raise ValueError(f"stripes must be a power of two: {stripes}")
        self._segments = [Segment(4) for _ in range(stripes)]
        self._segment_shift = 32 - ((stripes & -stripes).bit_length() - 1)  # 32 - numberOfTrailingZeros

    def segment_index(self, key) -> int:
        """Segment by the HIGH bits of the spread hash (Java's >>>); the bucket inside it by the low bits."""
        if len(self._segments) == 1:
            return 0
        return (Hashing.spread(Hashing.stable_hash(key)) & MASK32) >> self._segment_shift

    def _segment_for(self, key) -> Segment:
        return self._segments[self.segment_index(_require(key, "key"))]

    def get(self, key):
        s = self._segment_for(key)
        with s.lock:
            n = s.find(key)
            return None if n is None else n.value

    def put(self, key, value):
        _require(value, "value")
        s = self._segment_for(key)
        with s.lock:
            n = s.find(key)
            if n is not None:
                old = n.value
                n.value = value
                return old
            s.insert(key, value)
            return None

    def put_if_absent(self, key, value):
        _require(value, "value")
        s = self._segment_for(key)
        with s.lock:
            n = s.find(key)  # check ...
            if n is not None:
                return n.value
            s.insert(key, value)  # ... and act, under the same lock hold
            return None

    def remove(self, key):
        s = self._segment_for(key)
        with s.lock:
            return s.unlink(key)

    def compute(self, key, fn):
        s = self._segment_for(key)
        with s.lock:
            n = s.find(key)
            nxt = fn(key, None if n is None else n.value)
            if nxt is None:
                if n is not None:
                    s.unlink(key)
                return None
            if n is not None:
                n.value = nxt
            else:
                s.insert(key, nxt)
            return nxt

    def size(self) -> int:
        """Sums per-segment counts under all locks, taken in index order so two size() calls cannot deadlock."""
        for s in self._segments:
            s.lock.acquire()
        try:
            return sum(s.count for s in self._segments)
        finally:
            for s in reversed(self._segments):
                s.lock.release()

    def stripes(self) -> int:
        return len(self._segments)

    def total_resizes(self) -> int:
        return sum(s.resizes for s in self._segments)


class _FrozenNode:
    """An immutable chain node: the Python stand-in for Java's all-final-fields Node."""

    __slots__ = ("key", "value", "next")

    def __init__(self, key, value, nxt) -> None:
        object.__setattr__(self, "key", key)
        object.__setattr__(self, "value", value)
        object.__setattr__(self, "next", nxt)

    def __setattr__(self, name, value):
        raise AttributeError("chain nodes are immutable; build a new chain instead")


class LockFreeReadMap(ConcurrentStore[K, V]):
    """Lock striping for writers, no locks for readers. Each bucket holds an immutable chain. A writer, holding the
    stripe lock, builds a new chain and publishes it with one list-slot store; a reader loads the table, loads the
    bucket, and walks nodes that never change. The reader sees the chain before the write or after, never half."""

    def __init__(self, stripes: int = 16, initial_buckets: int = 64) -> None:
        def pow2(x: int) -> bool:
            return x > 0 and (x & (x - 1)) == 0
        if not pow2(stripes) or not pow2(initial_buckets) or initial_buckets < stripes:
            raise ValueError("stripes and buckets must be powers of two with buckets >= stripes")
        self._table: list[Optional[_FrozenNode]] = [None] * initial_buckets
        self._stripe_locks = [threading.Lock() for _ in range(stripes)]
        self._count = AtomicInteger()  # like ConcurrentHashMap, size() is a weakly consistent snapshot
        self._resizes = 0

    def _stripe_for(self, key) -> int:
        """bucket index mod stripes: both powers of two, so a bucket always maps to the same stripe across resizes."""
        return Hashing.spread(Hashing.stable_hash(key)) & (len(self._stripe_locks) - 1)

    def get(self, key):
        _require(key, "key")
        t = self._table                                   # one atomic load of the table reference
        n = t[Hashing.bucket_index(key, len(t))]           # one atomic load of the bucket
        while n is not None:                               # immutable nodes: no further synchronization
            if n.key == key:
                return n.value
            n = n.next
        return None

    def _without(self, head: Optional[_FrozenNode], key, removed: list):
        """Rebuild the chain without `key`; the removed node's value goes to removed[0]."""
        if head is None:
            return None
        if head.key == key:
            removed[0] = head.value
            return head.next
        rest = self._without(head.next, key, removed)
        return head if removed[0] is None and rest is head.next else _FrozenNode(head.key, head.value, rest)

    def put(self, key, value):
        _require(key, "key")
        _require(value, "value")
        s = self._stripe_for(key)
        with self._stripe_locks[s]:
            t = self._table
            i = Hashing.bucket_index(key, len(t))
            removed = [None]
            rest = self._without(t[i], key, removed)
            old = removed[0]
            t[i] = _FrozenNode(key, value, rest)  # a single slot store publishes the new chain
            inserted = old is None
            if inserted:
                self._count.increment_and_get()
        if inserted:
            self._grow_if_crowded()
        return old

    def put_if_absent(self, key, value):
        _require(key, "key")
        _require(value, "value")
        s = self._stripe_for(key)
        with self._stripe_locks[s]:
            t = self._table
            i = Hashing.bucket_index(key, len(t))
            head = t[i]
            n = head
            while n is not None:  # check ...
                if n.key == key:
                    return n.value
                n = n.next
            t[i] = _FrozenNode(key, value, head)  # ... and act, same lock hold
            self._count.increment_and_get()
        self._grow_if_crowded()
        return None

    def remove(self, key):
        _require(key, "key")
        s = self._stripe_for(key)
        with self._stripe_locks[s]:
            t = self._table
            i = Hashing.bucket_index(key, len(t))
            removed = [None]
            rebuilt = self._without(t[i], key, removed)
            if removed[0] is None:
                return None
            t[i] = rebuilt
            self._count.decrement_and_get()
            return removed[0]

    def compute(self, key, fn):
        _require(key, "key")
        s = self._stripe_for(key)
        inserted = False
        with self._stripe_locks[s]:
            t = self._table
            i = Hashing.bucket_index(key, len(t))
            removed = [None]
            rest = self._without(t[i], key, removed)
            old = removed[0]
            nxt = fn(key, old)
            if nxt is None:
                if old is not None:
                    t[i] = rest
                    self._count.decrement_and_get()
                result = None
            else:
                t[i] = _FrozenNode(key, nxt, rest)  # updated node in front of the untouched rest
                if old is None:
                    self._count.increment_and_get()
                    inserted = True
                result = nxt
        if inserted:
            self._grow_if_crowded()
        return result

    def _grow_if_crowded(self) -> None:
        """Called with NO lock held, so a writer never holds one stripe and then takes all of them."""
        t = self._table
        if self._count.get() > len(t) * 3 // 4:
            self.resize_to(len(t) * 2)

    def size(self) -> int:
        """Lock-free and weakly consistent: an insert that has published its node may not yet have counted itself."""
        return self._count.get()

    def buckets(self) -> int:
        return len(self._table)

    def resizes(self) -> int:
        return self._resizes

    def resize_to(self, new_buckets: int) -> None:
        """Takes every stripe lock, builds a fresh table, and publishes it with one attribute store. Readers that
        loaded the old table keep walking it; it is frozen because every write goes through a lock held here."""
        if new_buckets <= 0 or (new_buckets & (new_buckets - 1)) != 0 or new_buckets < len(self._stripe_locks):
            raise ValueError("bad bucket count")
        for lock in self._stripe_locks:
            lock.acquire()
        try:
            old = self._table
            if new_buckets <= len(old):
                return  # another thread grew first
            fresh: list[Optional[_FrozenNode]] = [None] * new_buckets
            for head in old:
                n = head
                while n is not None:
                    j = Hashing.bucket_index(n.key, new_buckets)
                    fresh[j] = _FrozenNode(n.key, n.value, fresh[j])
                    n = n.next
            self._table = fresh
            self._resizes += 1
        finally:
            for lock in reversed(self._stripe_locks):
                lock.release()


@dataclass(frozen=True)
class IdempotencyRecord:
    """What the map stores in the demo: the record an idempotent payment endpoint keeps per key."""

    payment_id: str
    amount_minor: int
    status: str

    def __str__(self) -> str:
        return f"IdempotencyRecord[payment_id={self.payment_id}, amount_minor={self.amount_minor}, status={self.status}]"


def benchmark(store: ConcurrentStore, threads: int, ops: int, key_space: int) -> int:
    """Same workload on any store: `threads` workers, each `ops` operations, 95% reads. Returns ops/ms."""
    key_table = [f"key-{k}" for k in range(key_space)]
    for k in key_table:
        store.put(k, 0)
    start = threading.Event()

    def work(seed: int) -> None:
        start.wait()
        x = 0x9E3779B97F4A7C15 ^ seed  # per-thread deterministic xorshift64, no shared state
        for _ in range(ops):
            x = (x ^ (x << 13)) & MASK64
            x ^= x >> 7
            x = (x ^ (x << 17)) & MASK64
            key = key_table[x % key_space]  # x is unsigned here, so % is Long.remainderUnsigned
            if (x >> 32) % 100 < 95:
                store.get(key)
            else:
                store.compute(key, lambda k, v: 1 if v is None else v + 1)

    workers = [threading.Thread(target=work, args=(t,)) for t in range(threads)]
    for w in workers:
        w.start()
    t0 = time.perf_counter_ns()
    start.set()
    for w in workers:
        w.join()
    ms = max(1, (time.perf_counter_ns() - t0) // 1_000_000)
    return threads * ops // ms


def race_get_then_put(store: ConcurrentStore, threads: int) -> int:
    """Forces the check-then-act race with a barrier in the gap: every thread reads "absent", all wait for each
    other, then all write. Returns how many threads believed they created the record."""
    in_the_gap = threading.Barrier(threads)
    winners = [0] * threads

    def work(id_: int) -> None:
        existing = store.get("idem-7")  # check
        in_the_gap.wait()                # everyone has checked, nobody has acted
        if existing is None:
            store.put("idem-7", IdempotencyRecord(f"pay_{id_}", 4_999, "CAPTURED"))  # act
            winners[id_] = 1

    workers = [threading.Thread(target=work, args=(t,)) for t in range(threads)]
    for w in workers:
        w.start()
    for w in workers:
        w.join()
    return sum(winners)


def race_put_if_absent(store: ConcurrentStore, threads: int) -> int:
    """Same shape, but the check and the act are one put_if_absent."""
    start = threading.Barrier(threads)
    winners = [0] * threads

    def work(id_: int) -> None:
        start.wait()
        if store.put_if_absent("idem-7", IdempotencyRecord(f"pay_{id_}", 4_999, "CAPTURED")) is None:
            winners[id_] = 1

    workers = [threading.Thread(target=work, args=(t,)) for t in range(threads)]
    for w in workers:
        w.start()
    for w in workers:
        w.join()
    return sum(winners)


def demo_stripes() -> None:
    print("== Where keys land: 16 stripes, top 4 bits of the mixed hash ==")
    m: StripedHashMap[str, int] = StripedHashMap(16)
    print("  ".join(f"acct-{i}->s{m.segment_index(f'acct-{i}')}" for i in range(1, 9)))
    h1, h2 = Hashing.stable_hash("acct-1"), Hashing.stable_hash("acct-2")
    print(f'stableHash("acct-1") = {h1}, stableHash("acct-2") = {h2}'
          f" (differ by 1: all low bits); after spread: {Hashing.spread(h1)} and {Hashing.spread(h2)}")
    print()


def demo_lock_free_read() -> None:
    print("== Lock-free reads: a writer replaces the chain, a reader sees old or new, never half ==")
    m: LockFreeReadMap[str, IdempotencyRecord] = LockFreeReadMap(4, 4)  # tiny on purpose: growth is visible
    m.put("idem-1", IdempotencyRecord("pay_001", 1_999, "CAPTURED"))
    m.put("idem-2", IdempotencyRecord("pay_002", 2_500, "CAPTURED"))
    m.put("idem-3", IdempotencyRecord("pay_003", 4_999, "PENDING"))
    print(f"3 puts into a 4-bucket table -> buckets = {m.buckets()}, size = {m.size()} (load 3/4: not yet)")
    m.put("idem-4", IdempotencyRecord("pay_004", 100, "PENDING"))
    print(f"4th put -> buckets = {m.buckets()}, resizes = {m.resizes()} (load crossed 75%)")
    print(f"get idem-3 -> {m.get('idem-3')}")
    m.compute("idem-3", lambda k, r: IdempotencyRecord(r.payment_id, r.amount_minor, "CAPTURED"))
    print(f"after compute: get idem-3 -> {m.get('idem-3')}")
    removed = m.remove("idem-2")
    print(f"remove idem-2 -> {removed}; get idem-2 -> {m.get('idem-2')}")
    m.resize_to(32)
    print(f"explicit resize to {m.buckets()} buckets; get idem-1 -> {m.get('idem-1')}; size = {m.size()}")
    print()


def main() -> None:
    demo_stripes()
    demo_lock_free_read()

    print("== Check-then-act, forced with a barrier in the gap (8 threads, one idempotency key) ==")
    print(f"get-then-put on StripedHashMap:   {race_get_then_put(StripedHashMap(), 8)} threads believe they created the record")
    print(f"putIfAbsent on StripedHashMap:    {race_put_if_absent(StripedHashMap(), 8)} thread created it")
    print(f"get-then-put on LockFreeReadMap:  {race_get_then_put(LockFreeReadMap(), 8)} threads believe they created the record")
    print(f"putIfAbsent on LockFreeReadMap:   {race_put_if_absent(LockFreeReadMap(), 8)} thread created it")
    print("A thread-safe map does not make your check-then-act atomic. Only a per-key atomic operation does.")
    print()

    # Java runs 100,000 ops per thread; the interpreted loop costs microseconds per op, so Python runs 20,000.
    threads, ops, keys = 4, 20_000, 1024
    print(f"== Throughput, {threads} threads x {ops:,} ops, 95% get / 5% compute, {keys:,} keys"
          " (ops per ms; varies by machine) ==")
    global_lock = benchmark(GlobalLockMap(), threads, ops, keys)
    striped = benchmark(StripedHashMap(16), threads, ops, keys)
    lock_free = benchmark(LockFreeReadMap(16, 2048), threads, ops, keys)
    print("%-18s %8d" % ("GlobalLockMap", global_lock))
    print("%-18s %8d" % ("StripedHashMap", striped))
    print("%-18s %8d" % ("LockFreeReadMap", lock_free))
    print("(Under the GIL only one thread runs Python bytecode at a time, so striping cannot add parallelism here;"
          " the numbers measure lock and hashing overhead. The free-threaded build (Lesson 10) changes that.)")


if __name__ == "__main__":
    main()
