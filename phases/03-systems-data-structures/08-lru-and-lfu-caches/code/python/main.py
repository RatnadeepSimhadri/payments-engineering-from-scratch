"""Lesson: phases/03-systems-data-structures/08-lru-and-lfu-caches/docs/en.md
LRU and LFU Caches from Scratch
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: Shah, Mitra, Matani (2010), "An O(1) algorithm for implementing the LFU cache eviction scheme";
Denning (1968), "The Working Set Model for Program Behavior"; RFC 9111 (HTTP Caching) for freshness lifetime.
Python 3.11+, standard library only. Same design as the Java and Go versions.
"""

from __future__ import annotations

from dataclasses import dataclass
from typing import Generic, Hashable, Optional, TypeVar

K = TypeVar("K", bound=Hashable)
V = TypeVar("V")

MASK64 = (1 << 64) - 1
NO_EXPIRY = (1 << 63) - 1


# ----- clock ---------------------------------------------------------------------------------------


class Clock:
    """Something that tells the time in milliseconds. Injected so tests never sleep."""

    def now_millis(self) -> int:
        raise NotImplementedError


class ManualClock(Clock):
    def __init__(self, start_millis: int = 0) -> None:
        self._now = start_millis

    def now_millis(self) -> int:
        return self._now

    def advance(self, millis: int) -> None:
        if millis < 0:
            raise ValueError("clock cannot go backwards")
        self._now += millis


# ----- money (minor units + currency, from Phase 10 Lesson 01) -------------------------------------


@dataclass(frozen=True)
class Money:
    minor_units: int
    currency: str

    def plus(self, other: "Money") -> "Money":
        self._same(other)
        return Money(self.minor_units + other.minor_units, self.currency)

    def minus(self, other: "Money") -> "Money":
        self._same(other)
        return Money(self.minor_units - other.minor_units, self.currency)

    def _same(self, other: "Money") -> None:
        if self.currency != other.currency:
            raise ValueError(f"cannot combine {self.currency} with {other.currency}")

    def __str__(self) -> str:
        sign = "-" if self.minor_units < 0 else ""
        mag = abs(self.minor_units)
        return f"{sign}{mag // 100}.{mag % 100:02d} {self.currency}"


# ----- LRU: hash map + doubly linked list ----------------------------------------------------------


class _Node(Generic[K, V]):
    __slots__ = ("key", "value", "expires_at", "prev", "next")

    def __init__(self, key: K, value: V, expires_at: int) -> None:
        self.key = key
        self.value = value
        self.expires_at = expires_at
        self.prev: Optional[_Node[K, V]] = None
        self.next: Optional[_Node[K, V]] = None


class LruCache(Generic[K, V]):
    """Least-recently-used cache with O(1) get, put, and remove. Entries may carry a TTL; an expired
    entry is a miss and is removed on access. The list runs most-recent (head) to least-recent (tail)."""

    def __init__(self, capacity: int, clock: Optional[Clock] = None) -> None:
        if capacity <= 0:
            raise ValueError(f"capacity must be positive: {capacity}")
        self.capacity = capacity
        self.clock = clock or ManualClock()
        self._map: dict[K, _Node[K, V]] = {}
        self._head: _Node = _Node(None, None, 0)  # sentinel: most recent follows head
        self._tail: _Node = _Node(None, None, 0)  # sentinel: least recent precedes tail
        self._head.next = self._tail
        self._tail.prev = self._head
        self.hits = 0
        self.misses = 0
        self.evictions = 0
        self.expirations = 0

    # list surgery: every operation is a constant number of pointer writes
    def _unlink(self, node: _Node[K, V]) -> None:
        node.prev.next = node.next
        node.next.prev = node.prev

    def _push_front(self, node: _Node[K, V]) -> None:
        node.next = self._head.next
        node.prev = self._head
        self._head.next.prev = node
        self._head.next = node

    def _expired(self, node: _Node[K, V]) -> bool:
        return node.expires_at <= self.clock.now_millis()

    def get(self, key: K) -> Optional[V]:
        node = self._map.get(key)
        if node is None:
            self.misses += 1
            return None
        if self._expired(node):
            self._unlink(node)
            del self._map[key]
            self.expirations += 1
            self.misses += 1
            return None
        self._unlink(node)
        self._push_front(node)
        self.hits += 1
        return node.value

    def put(self, key: K, value: V, ttl_millis: Optional[int] = None) -> None:
        if ttl_millis is not None and ttl_millis <= 0:
            raise ValueError("ttl_millis must be positive")
        expires_at = NO_EXPIRY if ttl_millis is None else self.clock.now_millis() + ttl_millis
        node = self._map.get(key)
        if node is not None:
            node.value = value
            node.expires_at = expires_at
            self._unlink(node)
            self._push_front(node)
            return
        if len(self._map) >= self.capacity:
            victim = self._tail.prev
            self._unlink(victim)
            del self._map[victim.key]
            self.evictions += 1
        node = _Node(key, value, expires_at)
        self._map[key] = node
        self._push_front(node)

    def remove(self, key: K) -> bool:
        node = self._map.pop(key, None)
        if node is None:
            return False
        self._unlink(node)
        return True

    def __len__(self) -> int:
        return len(self._map)

    def keys(self) -> list[K]:
        """Most recent first. O(n); for tests and debugging."""
        out = []
        node = self._head.next
        while node is not self._tail:
            out.append(node.key)
            node = node.next
        return out

    def hit_rate(self) -> float:
        total = self.hits + self.misses
        return self.hits / total if total else 0.0


# ----- LFU: frequency buckets, O(1) --------------------------------------------------------------


class _LfuNode(Generic[K, V]):
    __slots__ = ("key", "value", "freq", "prev", "next")

    def __init__(self, key: K, value: V) -> None:
        self.key = key
        self.value = value
        self.freq = 1
        self.prev: Optional[_LfuNode[K, V]] = None
        self.next: Optional[_LfuNode[K, V]] = None


class _Bucket(Generic[K, V]):
    """A doubly linked list of nodes that share one frequency; most recent at the head."""

    def __init__(self) -> None:
        self.head: _LfuNode = _LfuNode(None, None)
        self.tail: _LfuNode = _LfuNode(None, None)
        self.head.next = self.tail
        self.tail.prev = self.head
        self.size = 0

    def push_front(self, node: _LfuNode[K, V]) -> None:
        node.next = self.head.next
        node.prev = self.head
        self.head.next.prev = node
        self.head.next = node
        self.size += 1

    def unlink(self, node: _LfuNode[K, V]) -> None:
        node.prev.next = node.next
        node.next.prev = node.prev
        self.size -= 1

    def least_recent(self) -> _LfuNode[K, V]:
        return self.tail.prev


class LfuCache(Generic[K, V]):
    """Least-frequently-used cache with O(1) get and put (Shah, Mitra, Matani 2010).
    Nodes live in a bucket per frequency; min_freq tracks the lowest non-empty bucket. Ties within
    a bucket are broken least-recently-used first."""

    def __init__(self, capacity: int) -> None:
        if capacity <= 0:
            raise ValueError(f"capacity must be positive: {capacity}")
        self.capacity = capacity
        self._map: dict[K, _LfuNode[K, V]] = {}
        self._buckets: dict[int, _Bucket[K, V]] = {}
        self._min_freq = 0
        self.hits = 0
        self.misses = 0
        self.evictions = 0

    def _touch(self, node: _LfuNode[K, V]) -> None:
        """Move node from bucket f to bucket f+1, maintaining min_freq. Constant time."""
        bucket = self._buckets[node.freq]
        bucket.unlink(node)
        if bucket.size == 0:
            del self._buckets[node.freq]
            if self._min_freq == node.freq:
                self._min_freq = node.freq + 1
        node.freq += 1
        self._buckets.setdefault(node.freq, _Bucket()).push_front(node)

    def get(self, key: K) -> Optional[V]:
        node = self._map.get(key)
        if node is None:
            self.misses += 1
            return None
        self._touch(node)
        self.hits += 1
        return node.value

    def put(self, key: K, value: V) -> None:
        node = self._map.get(key)
        if node is not None:
            node.value = value
            self._touch(node)
            return
        if len(self._map) >= self.capacity:
            bucket = self._buckets[self._min_freq]
            victim = bucket.least_recent()
            bucket.unlink(victim)
            if bucket.size == 0:
                del self._buckets[self._min_freq]
            del self._map[victim.key]
            self.evictions += 1
        node = _LfuNode(key, value)
        self._map[key] = node
        self._buckets.setdefault(1, _Bucket()).push_front(node)
        self._min_freq = 1

    def remove(self, key: K) -> bool:
        node = self._map.pop(key, None)
        if node is None:
            return False
        bucket = self._buckets[node.freq]
        bucket.unlink(node)
        if bucket.size == 0:
            del self._buckets[node.freq]
            if self._min_freq == node.freq:
                # the next non-empty bucket; O(distinct frequencies), only on explicit remove
                self._min_freq = min(self._buckets) if self._buckets else 0
        return True

    def frequency(self, key: K) -> int:
        node = self._map.get(key)
        return node.freq if node else 0

    def min_frequency(self) -> int:
        return self._min_freq

    def __len__(self) -> int:
        return len(self._map)

    def hit_rate(self) -> float:
        total = self.hits + self.misses
        return self.hits / total if total else 0.0


# ----- deterministic trace and replay -------------------------------------------------------------


class Rng:
    """splitmix64: identical sequences in Java, Python, and Go for a given seed."""

    def __init__(self, seed: int) -> None:
        self.state = seed & MASK64

    def next(self) -> int:
        self.state = (self.state + 0x9E3779B97F4A7C15) & MASK64
        z = self.state
        z = ((z ^ (z >> 30)) * 0xBF58476D1CE4E5B9) & MASK64
        z = ((z ^ (z >> 27)) * 0x94D049BB133111EB) & MASK64
        return z ^ (z >> 31)

    def next_int(self, bound: int) -> int:
        return (self.next() >> 33) % bound


def bin_trace(seed: int, ops: int, hot_offset: int, hot_count: int = 20, hot_percent: int = 80, cold_count: int = 2000) -> list[str]:
    """hot_percent of lookups hit `hot_count` BINs starting at hot_offset; the rest spread over cold BINs."""
    rng = Rng(seed)
    keys = []
    for _ in range(ops):
        if rng.next_int(100) < hot_percent:
            keys.append(f"bin-{hot_offset + rng.next_int(hot_count)}")
        else:
            keys.append(f"bin-{2 * hot_count + rng.next_int(cold_count)}")
    return keys


def replay(cache, keys: list[str]) -> float:
    """Look each key up; on a miss, 'load' it (the value is derived from the key) and insert.
    Returns the hit rate over this batch only."""
    hits = 0
    for key in keys:
        if cache.get(key) is not None:
            hits += 1
        else:
            cache.put(key, f"record:{key}")
    return hits / len(keys) if keys else 0.0


def main() -> None:
    print("== LRU: merchant config, most recent at the head ==")
    clock = ManualClock(1_700_000_000_000)
    lru: LruCache[str, str] = LruCache(3, clock)
    for m in ("merchant-1", "merchant-2", "merchant-3"):
        lru.put(m, f"config({m})")
    print(f"after 3 puts: {lru.keys()}")
    lru.get("merchant-1")
    print(f"after get(merchant-1): {lru.keys()}")
    lru.put("merchant-4", "config(merchant-4)")
    print(f"after put(merchant-4): {lru.keys()}  evicted merchant-2, evictions={lru.evictions}")

    print("\n== TTL: a config entry that must be re-read after five minutes ==")
    lru.put("merchant-9", "fee_bps=290", ttl_millis=5 * 60_000)
    print(f"t+0:     get(merchant-9) = {lru.get('merchant-9')}")
    clock.advance(4 * 60_000 + 59_999)
    print(f"t+4:59.9 get(merchant-9) = {lru.get('merchant-9')}")
    clock.advance(1)
    print(f"t+5:00   get(merchant-9) = {lru.get('merchant-9')}  expirations={lru.expirations}")

    print("\n== LFU: BIN lookups, frequency buckets ==")
    lfu: LfuCache[str, str] = LfuCache(3)
    for b in ("bin-411111", "bin-411111", "bin-411111", "bin-555555", "bin-555555", "bin-601100"):
        lfu.put(b, f"brand({b})")
    print(f"frequencies: 411111={lfu.frequency('bin-411111')} 555555={lfu.frequency('bin-555555')} 601100={lfu.frequency('bin-601100')} min={lfu.min_frequency()}")
    lfu.put("bin-371449", "brand(bin-371449)")
    print(f"put(bin-371449) evicts the least frequent: 601100 present = {lfu.get('bin-601100') is not None}, 411111 present = {lfu.get('bin-411111') is not None}")

    print("\n== Same trace, both policies, capacity 32 (20 hot BINs carry 80% of lookups) ==")
    segments = [
        ("phase 1: hot set A", bin_trace(1, 6000, hot_offset=0)),
        ("scan: 500 one-off BINs", [f"scan-{i}" for i in range(500)]),
        ("phase 1 again", bin_trace(2, 2000, hot_offset=0)),
        ("phase 2: hot set B", bin_trace(3, 6000, hot_offset=20)),
    ]
    lru2: LruCache[str, str] = LruCache(32)
    lfu2: LfuCache[str, str] = LfuCache(32)
    print(f"{'segment':24} {'LRU hit rate':>12} {'LFU hit rate':>12}")
    for name, keys in segments:
        print(f"{name:24} {replay(lru2, keys):>12.3f} {replay(lfu2, keys):>12.3f}")
    # the scan flushed LRU's hot set; LFU kept it. Show the first 200 lookups after the scan.
    lru3: LruCache[str, str] = LruCache(32)
    lfu3: LfuCache[str, str] = LfuCache(32)
    for _, keys in segments[:2]:
        replay(lru3, keys)
        replay(lfu3, keys)
    print(f"first 200 lookups after the scan: LRU {replay(lru3, segments[2][1][:200]):.3f}  LFU {replay(lfu3, segments[2][1][:200]):.3f}")

    print("\n== Why you do not cache balances ==")
    ledger_balance = Money(10_000, "USD")
    balance_cache: LruCache[str, Money] = LruCache(16)
    balance_cache.put("acct-7", ledger_balance)
    ledger_balance = ledger_balance.minus(Money(8_000, "USD"))  # a posting lands: 100.00 -> 20.00
    cached = balance_cache.get("acct-7")
    authorization = Money(5_000, "USD")
    print(f"ledger says {ledger_balance}, cache says {cached}")
    print(f"authorize {authorization} against the cache: {'approved' if cached.minus(authorization).minor_units >= 0 else 'declined'}")
    print(f"actual balance after that approval would be {ledger_balance.minus(authorization)}: an overdraft the ledger never allowed")


if __name__ == "__main__":
    main()
