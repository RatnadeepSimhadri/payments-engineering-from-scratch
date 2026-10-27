"""Lesson: phases/09-reliability-and-scale/03-caching-strategies/docs/en.md
Caching Strategies: Cache-Aside, Write-Through, Write-Behind
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: Memcached protocol docs (cache-aside idiom); Redis documentation on EXPIRE semantics;
         Phase 10 Lesson 01 for the Money type. Python 3.11+, standard library only. Same design as the Java version.
"""

from __future__ import annotations

import math
from abc import ABC, abstractmethod
from dataclasses import dataclass
from functools import total_ordering
from typing import Callable, Optional


# ---------------------------------------------------------------------------------------------------
# Money (Phase 10, Lesson 01)
# ---------------------------------------------------------------------------------------------------


@dataclass(frozen=True)
class Currency:
    """ISO 4217 currency: a three-letter code and the number of decimal places in its minor unit."""

    code: str
    exponent: int

    def __post_init__(self) -> None:
        if len(self.code) != 3 or not self.code.isalpha() or not self.code.isupper():
            raise ValueError(f"currency code must be three uppercase letters: {self.code!r}")
        if not 0 <= self.exponent <= 4:
            raise ValueError(f"exponent must be 0..4: {self.exponent}")

    def minor_units_per_major(self) -> int:
        return 10 ** self.exponent


USD = Currency("USD", 2)


class CurrencyMismatchError(ValueError):
    def __init__(self, a: Currency, b: Currency) -> None:
        super().__init__(f"cannot combine {a.code} with {b.code}")


@total_ordering
@dataclass(frozen=True)
class Money:
    """An exact amount of one currency as a signed count of minor units (Phase 10, Lesson 01)."""

    minor_units: int
    currency: Currency

    def __post_init__(self) -> None:
        if not isinstance(self.minor_units, int) or isinstance(self.minor_units, bool):
            raise TypeError("minor_units must be an int; never construct Money from a float")

    @staticmethod
    def of_minor(minor_units: int, currency: Currency) -> "Money":
        return Money(minor_units, currency)

    def _require_same_currency(self, other: "Money") -> None:
        if self.currency != other.currency:
            raise CurrencyMismatchError(self.currency, other.currency)

    def plus(self, other: "Money") -> "Money":
        self._require_same_currency(other)
        return Money(self.minor_units + other.minor_units, self.currency)

    def minus(self, other: "Money") -> "Money":
        self._require_same_currency(other)
        return Money(self.minor_units - other.minor_units, self.currency)

    def is_negative(self) -> bool:
        return self.minor_units < 0

    def compare_to(self, other: "Money") -> int:
        self._require_same_currency(other)
        return (self.minor_units > other.minor_units) - (self.minor_units < other.minor_units)

    def __lt__(self, other: "Money") -> bool:
        return self.compare_to(other) < 0

    def __str__(self) -> str:
        scale = self.currency.minor_units_per_major()
        whole, frac = divmod(abs(self.minor_units), scale)
        sign = "-" if self.minor_units < 0 else ""
        if self.currency.exponent == 0:
            return f"{sign}{whole} {self.currency.code}"
        return f"{sign}{whole}.{frac:0{self.currency.exponent}d} {self.currency.code}"


# ---------------------------------------------------------------------------------------------------
# Clock, store, stats, TTL cache
# ---------------------------------------------------------------------------------------------------


class ManualClock:
    """A clock the demo and the tests move by hand. Nothing in this lesson sleeps."""

    def __init__(self, start_ms: int) -> None:
        self._now_ms = start_ms

    def now(self) -> int:
        return self._now_ms

    def advance(self, ms: int) -> None:
        if ms < 0:
            raise ValueError("clock only moves forward")
        self._now_ms += ms


class BackingStore:
    """The slow system of record: a database holding account balances. Every read and write costs a fixed
    simulated latency that is accumulated, not slept. ``peek`` is the oracle the staleness meter uses;
    it is not a real operation and is not counted."""

    def __init__(self, read_latency_ms: int, write_latency_ms: int) -> None:
        self._rows: dict[str, Money] = {}
        self._read_latency_ms = read_latency_ms
        self._write_latency_ms = write_latency_ms
        self.reads = 0
        self.writes = 0
        self.simulated_latency_ms = 0

    def read(self, account: str) -> Optional[Money]:
        self.reads += 1
        self.simulated_latency_ms += self._read_latency_ms
        return self._rows.get(account)

    def write(self, account: str, balance: Money) -> None:
        self.writes += 1
        self.simulated_latency_ms += self._write_latency_ms
        self._rows[account] = balance

    def peek(self, account: str) -> Optional[Money]:
        """Truth without cost: what the balance really is right now. Used only to measure staleness."""
        return self._rows.get(account)


class CacheStats:
    """Hit, miss, and staleness counters for one strategy under one workload."""

    def __init__(self, cache_latency_per_op_ms: int) -> None:
        self.hits = 0
        self.misses = 0
        self.stale_reads = 0
        self.cache_latency_ms = 0
        self.cache_latency_per_op_ms = cache_latency_per_op_ms

    def hit(self) -> None:
        self.hits += 1
        self.cache_latency_ms += self.cache_latency_per_op_ms

    def miss(self) -> None:
        self.misses += 1
        self.cache_latency_ms += self.cache_latency_per_op_ms

    def reads(self) -> int:
        return self.hits + self.misses

    def hit_rate(self) -> float:
        return 0.0 if self.reads() == 0 else self.hits / self.reads()


@dataclass(frozen=True)
class Entry:
    value: Money
    expires_at_ms: int


class TtlCache:
    """A map of key to (value, expiry). Expiry is checked lazily on read against the injected clock."""

    NO_EXPIRY = (1 << 63) - 1  # Long.MAX_VALUE in the Java version

    def __init__(self, clock: ManualClock) -> None:
        self._entries: dict[str, Entry] = {}
        self._clock = clock

    def get(self, key: str) -> Optional[Money]:
        """Returns the cached value, or None when absent or expired. An expired entry is removed on the way out."""
        e = self._entries.get(key)
        if e is None:
            return None
        if e.expires_at_ms <= self._clock.now():
            del self._entries[key]
            return None
        return e.value

    def put(self, key: str, value: Money, ttl_ms: int) -> None:
        expires = self.NO_EXPIRY if ttl_ms == self.NO_EXPIRY else self._clock.now() + ttl_ms
        self._entries[key] = Entry(value, expires)

    def invalidate(self, key: str) -> bool:
        return self._entries.pop(key, None) is not None

    def contains(self, key: str) -> bool:
        return self.get(key) is not None

    def size(self) -> int:
        return len(self._entries)

    def clear(self) -> None:
        self._entries.clear()


# ---------------------------------------------------------------------------------------------------
# Strategies
# ---------------------------------------------------------------------------------------------------


class BalanceCache(ABC):
    """The one interface every strategy implements, so the workload can drive them interchangeably."""

    @abstractmethod
    def name(self) -> str: ...

    @abstractmethod
    def read(self, account: str) -> Optional[Money]:
        """The balance the application sees. May be stale depending on the strategy."""

    @abstractmethod
    def write(self, account: str, balance: Money) -> None:
        """Record a new balance. Where it goes first is the strategy."""

    @abstractmethod
    def stats(self) -> CacheStats: ...

    @abstractmethod
    def store(self) -> BackingStore: ...

    @abstractmethod
    def cache(self) -> TtlCache: ...

    def read_measured(self, account: str) -> Optional[Money]:
        """Counts a read as stale when the value handed to the caller differs from the store's current truth."""
        seen = self.read(account)
        truth = self.store().peek(account)
        if seen != truth:
            self.stats().stale_reads += 1
        return seen


class _CachedStrategy(BalanceCache):
    """Shared plumbing: the three accessors and the common miss-then-load read path."""

    def __init__(self, store: BackingStore, clock: ManualClock, cache_latency_ms: int) -> None:
        self._store = store
        self._cache = TtlCache(clock)
        self._stats = CacheStats(cache_latency_ms)

    def _read_with_ttl(self, account: str, loader: Callable[[str], Optional[Money]], ttl_ms: int) -> Optional[Money]:
        cached = self._cache.get(account)
        if cached is not None:
            self._stats.hit()
            return cached
        self._stats.miss()
        loaded = loader(account)
        if loaded is not None:
            self._cache.put(account, loaded, ttl_ms)
        return loaded

    def stats(self) -> CacheStats:
        return self._stats

    def store(self) -> BackingStore:
        return self._store

    def cache(self) -> TtlCache:
        return self._cache


class CacheAside(_CachedStrategy):
    """Cache-aside (lazy loading): the application checks the cache, loads from the store on a miss, and
    populates the cache with a TTL. Writes go to the store. Whether the write also invalidates the cached
    entry is the whole difference between "eventually right" and "wrong for up to one TTL"."""

    def __init__(self, store: BackingStore, clock: ManualClock, ttl_ms: int, invalidate_on_write: bool,
                 cache_latency_ms: int) -> None:
        super().__init__(store, clock, cache_latency_ms)
        self._ttl_ms = ttl_ms
        self._invalidate_on_write = invalidate_on_write

    def name(self) -> str:
        return "cache-aside + invalidate" if self._invalidate_on_write else "cache-aside, TTL only"

    def read(self, account: str) -> Optional[Money]:
        return self._read_with_ttl(account, self._store.read, self._ttl_ms)

    def write(self, account: str, balance: Money) -> None:
        self._store.write(account, balance)
        if self._invalidate_on_write:
            self._cache.invalidate(account)


class ReadThrough(_CachedStrategy):
    """Read-through: the cache owns the loader, so callers can never forget to populate it or populate it
    with the wrong key. Same read path as cache-aside, moved behind the interface. Writes go to the store
    and delete the entry (write-around with invalidation); the next read reloads."""

    def __init__(self, store: BackingStore, clock: ManualClock, ttl_ms: int, cache_latency_ms: int) -> None:
        super().__init__(store, clock, cache_latency_ms)
        self._loader: Callable[[str], Optional[Money]] = store.read
        self._ttl_ms = ttl_ms

    def name(self) -> str:
        return "read-through"

    def read(self, account: str) -> Optional[Money]:
        return self._read_with_ttl(account, self._loader, self._ttl_ms)

    def write(self, account: str, balance: Money) -> None:
        self._store.write(account, balance)
        self._cache.invalidate(account)


class WriteThrough(_CachedStrategy):
    """Write-through: every write goes to the store and then into the cache, synchronously. The cache is
    never stale with respect to writes that went through it; the cost is store latency on every write and
    cache space spent on keys nobody will read."""

    def __init__(self, store: BackingStore, clock: ManualClock, ttl_ms: int, cache_latency_ms: int) -> None:
        super().__init__(store, clock, cache_latency_ms)
        self._ttl_ms = ttl_ms

    def name(self) -> str:
        return "write-through"

    def read(self, account: str) -> Optional[Money]:
        return self._read_with_ttl(account, self._store.read, self._ttl_ms)

    def write(self, account: str, balance: Money) -> None:
        self._store.write(account, balance)  # store first: if this fails, the cache never sees the value
        self._cache.put(account, balance, self._ttl_ms)


class WriteBehind(_CachedStrategy):
    """Write-behind (write-back): writes land in the cache and a dirty queue; the store is updated later by
    ``flush``, which coalesces repeated writes to one key into one store write. Fast and cheap, and the
    only strategy here where a cache crash loses committed-looking data."""

    def __init__(self, store: BackingStore, clock: ManualClock, cache_latency_ms: int) -> None:
        super().__init__(store, clock, cache_latency_ms)
        self._dirty: dict[str, Money] = {}  # insertion-ordered, like LinkedHashMap
        self._order: list[str] = []
        self.lost_writes = 0

    def name(self) -> str:
        return "write-behind"

    def read(self, account: str) -> Optional[Money]:
        # dirty data must never expire
        return self._read_with_ttl(account, self._store.read, TtlCache.NO_EXPIRY)

    def write(self, account: str, balance: Money) -> None:
        self._cache.put(account, balance, TtlCache.NO_EXPIRY)
        if account not in self._dirty:
            self._order.append(account)
        self._dirty[account] = balance

    def dirty_count(self) -> int:
        return len(self._dirty)

    def flush(self) -> int:
        """Writes every dirty key to the store once, in first-dirtied order. Returns the number of store writes."""
        flushed = 0
        for key in self._order:
            self._store.write(key, self._dirty.pop(key))
            flushed += 1
        self._order.clear()
        return flushed

    def crash(self) -> None:
        """The cache process dies before flushing. Everything dirty is gone; the store keeps the old balances."""
        self.lost_writes += len(self._dirty)
        self._dirty.clear()
        self._order.clear()
        self._cache.clear()


# ---------------------------------------------------------------------------------------------------
# Workload
# ---------------------------------------------------------------------------------------------------


class JavaRandom:
    """java.util.Random, bit for bit (48-bit LCG), so the seeded workload matches the Java demo exactly."""

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

    def next_double(self) -> float:
        return ((self.next(26) << 27) + self.next(27)) * (1.0 / (1 << 53))


def java_round(x: float) -> int:
    """Math.round(double): floor(x + 0.5), not Python's banker's rounding."""
    return math.floor(x + 0.5)


@dataclass(frozen=True)
class Op:
    is_write: bool
    account: str
    debit_minor: int


class Workload:
    """A seeded, skewed workload: most reads hit a few hot accounts (the 80/20 shape real balance traffic has),
    writes are debits of a random amount. Deterministic for a given seed so every strategy sees the same ops."""

    @staticmethod
    def generate(seed: int, ops: int, accounts: int, write_fraction: float, hot_fraction: float,
                 hot_share: float) -> list[Op]:
        rng = JavaRandom(seed)
        hot_count = max(1, java_round(accounts * hot_fraction))
        out: list[Op] = []
        for _ in range(ops):
            hot = rng.next_double() < hot_share
            idx = rng.next_int(hot_count) if hot else hot_count + rng.next_int(max(1, accounts - hot_count))
            account = f"acct-{idx % accounts:04d}"
            is_write = rng.next_double() < write_fraction
            out.append(Op(is_write, account, 1 + rng.next_int(2000) if is_write else 0))
        return out

    @staticmethod
    def seeded_store(accounts: int, read_latency_ms: int, write_latency_ms: int) -> BackingStore:
        store = BackingStore(read_latency_ms, write_latency_ms)
        for i in range(accounts):
            store.write(f"acct-{i:04d}", Money.of_minor(100_000, USD))
        store.reads = 0
        store.writes = 0
        store.simulated_latency_ms = 0  # seeding is not part of the measurement
        return store

    @staticmethod
    def run(target: BalanceCache, ops: list[Op], clock: ManualClock, ms_per_op: int) -> None:
        """Runs the ops. Each write reads the current balance through the cache, debits, and writes back."""
        for op in ops:
            if op.is_write:
                current = target.read_measured(op.account)
                target.write(op.account, current.minus(Money.of_minor(op.debit_minor, USD)))
            else:
                target.read_measured(op.account)
            clock.advance(ms_per_op)


# ---------------------------------------------------------------------------------------------------
# Demo
# ---------------------------------------------------------------------------------------------------


def row(c: BalanceCache) -> str:
    s = c.stats()
    st = c.store()
    return (f"{c.name():<26} hit rate {100 * s.hit_rate():5.1f}%  store reads {st.reads:5d}  store writes {st.writes:5d}"
            f"  divergent {s.stale_reads:5d}  simulated latency {st.simulated_latency_ms + s.cache_latency_ms:7d} ms")


def main() -> None:
    print("== The stale balance hazard: cache-aside with TTL, no invalidation ==")
    clock = ManualClock(0)
    store = BackingStore(20, 25)
    store.write("acct-0001", Money.of_minor(10_000, USD))
    ttl_only = CacheAside(store, clock, 30_000, False, 1)
    print(f"balance read through cache:        {ttl_only.read('acct-0001')}  (miss, now cached for 30s)")
    store.write("acct-0001", Money.of_minor(2_000, USD))  # a batch job debits 80.00 straight to the database
    print(f"batch job debits 80.00 in the DB:  store = {store.peek('acct-0001')}")
    seen = ttl_only.read("acct-0001")
    print(f"authorization reads the cache:     {seen}  (hit; STALE by {seen.minus(store.peek('acct-0001'))})")
    request = Money.of_minor(5_000, USD)
    approved = seen.compare_to(request) >= 0
    print(f"approve 50.00 withdrawal?          {str(approved).lower()}  -> store balance would become "
          f"{store.peek('acct-0001').minus(request)}")
    clock.advance(30_000)
    print(f"after the 30s TTL:                 {ttl_only.read('acct-0001')}  (miss, reloaded; correct again)")

    print()
    print("== Same scenario with invalidate-on-write ==")
    clock2 = ManualClock(0)
    store2 = BackingStore(20, 25)
    store2.write("acct-0001", Money.of_minor(10_000, USD))
    invalidating = CacheAside(store2, clock2, 30_000, True, 1)
    invalidating.read("acct-0001")
    invalidating.write("acct-0001", Money.of_minor(2_000, USD))
    print(f"read after write:                  {invalidating.read('acct-0001')}  (miss, cache entry was removed by the write)")

    print()
    print("== Write-behind: fast until the cache dies ==")
    clock3 = ManualClock(0)
    store3 = BackingStore(20, 25)
    store3.write("acct-0007", Money.of_minor(50_000, USD))
    wb = WriteBehind(store3, clock3, 1)
    wb.write("acct-0007", Money.of_minor(45_000, USD))
    wb.write("acct-0007", Money.of_minor(41_000, USD))
    wb.write("acct-0007", Money.of_minor(40_000, USD))
    print(f"three debits applied; cache says   {wb.read('acct-0007')}, store still says {store3.peek('acct-0007')}"
          f", dirty keys = {wb.dirty_count()}")
    before = store3.writes
    flushed = wb.flush()
    print(f"flush(): {flushed} store write for 3 debits (coalesced); store now {store3.peek('acct-0007')}"
          f"; store writes went {before} -> {store3.writes}")
    wb.write("acct-0007", Money.of_minor(30_000, USD))
    wb.crash()
    print(f"one more debit, then crash before flush: store = {store3.peek('acct-0007')}, lost writes = {wb.lost_writes}")

    print()
    print("== Measured: 20,000 ops, 500 accounts, 20% of accounts take 80% of traffic, 15% writes ==")
    print("   store read = 20 ms, store write = 25 ms, cache op = 1 ms, TTL = 5 s, one op per 10 ms")
    ops = Workload.generate(42, 20_000, 500, 0.15, 0.20, 0.80)
    c1, c2, c3, c4, c5 = (ManualClock(0) for _ in range(5))
    strategies: list[BalanceCache] = [
        CacheAside(Workload.seeded_store(500, 20, 25), c1, 5_000, False, 1),
        CacheAside(Workload.seeded_store(500, 20, 25), c2, 5_000, True, 1),
        ReadThrough(Workload.seeded_store(500, 20, 25), c3, 5_000, 1),
        WriteThrough(Workload.seeded_store(500, 20, 25), c4, 5_000, 1),
        WriteBehind(Workload.seeded_store(500, 20, 25), c5, 1),
    ]
    clocks = [c1, c2, c3, c4, c5]
    for strategy, clk in zip(strategies, clocks):
        Workload.run(strategy, ops, clk, 10)
        if isinstance(strategy, WriteBehind):
            strategy.flush()
        print(row(strategy))
    no_cache_latency = sum(45 if op.is_write else 20 for op in ops)  # read 20 ms; write = read + write, 45 ms
    writes = sum(1 for op in ops if op.is_write)
    print(f"{'no cache':<26} hit rate   0.0%  store reads {len(ops):5d}  store writes {writes:5d}  divergent     0"
          f"  simulated latency {no_cache_latency:7d} ms")
    print()
    print('"divergent" = reads where the value handed to the caller differed from the store at that moment.')
    print("For cache-aside that is a stale balance an authorization trusted. For write-behind the arrow is reversed:")
    print("the cache is right and the store is behind, which is durability exposure rather than staleness.")


if __name__ == "__main__":
    main()
