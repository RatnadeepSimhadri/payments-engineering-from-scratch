"""Lesson: phases/09-reliability-and-scale/04-cache-invalidation-and-stampedes/docs/en.md
Cache Invalidation, Stampedes, and Request Coalescing
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: Vattani, Chierichetti, Lowenstein, "Optimal Probabilistic Cache Stampede Prevention" (VLDB 2015);
         RFC 5861 (stale-while-revalidate); Nishtala et al., "Scaling Memcache at Facebook" (NSDI 2013).
Python 3.11+, standard library only. Same design as the Java version.
"""

from __future__ import annotations

import heapq
import math
from abc import ABC, abstractmethod
from dataclasses import dataclass
from typing import Callable, Optional


# ---------------------------------------------------------------------------------------------------
# Money (Phase 10, Lesson 01)
# ---------------------------------------------------------------------------------------------------


@dataclass(frozen=True)
class Currency:
    """ISO 4217 currency (Phase 10, Lesson 01)."""

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

    def minus(self, other: "Money") -> "Money":
        if self.currency != other.currency:
            raise ValueError(f"cannot combine {self.currency.code} with {other.currency.code}")
        return Money(self.minor_units - other.minor_units, self.currency)

    def __str__(self) -> str:
        scale = self.currency.minor_units_per_major()
        whole, frac = divmod(abs(self.minor_units), scale)
        sign = "-" if self.minor_units < 0 else ""
        if self.currency.exponent == 0:
            return f"{sign}{whole} {self.currency.code}"
        return f"{sign}{whole}.{frac:0{self.currency.exponent}d} {self.currency.code}"


# ---------------------------------------------------------------------------------------------------
# Simulator and store
# ---------------------------------------------------------------------------------------------------


Action = Callable[[], None]
Waiter = Callable[[Money], None]
"""A request's completion: the value the client was handed."""


class Simulator:
    """A discrete-event simulator. Time only moves when the next event is popped, so a run that represents ten
    seconds of traffic completes in milliseconds and is identical on every run. Events at the same instant run
    in scheduling order."""

    def __init__(self) -> None:
        self._queue: list[tuple[int, int, Action]] = []
        self._now_ms = 0
        self._seq = 0

    def now(self) -> int:
        return self._now_ms

    def schedule(self, at_ms: int, action: Action) -> None:
        if at_ms < self._now_ms:
            raise ValueError(f"cannot schedule in the past: {at_ms} < {self._now_ms}")
        heapq.heappush(self._queue, (at_ms, self._seq, action))
        self._seq += 1

    def after(self, delay_ms: int, action: Action) -> None:
        self.schedule(self._now_ms + delay_ms, action)

    def run(self) -> None:
        """Runs every event in time order until the queue is empty."""
        while self._queue:
            at_ms, _, action = heapq.heappop(self._queue)
            self._now_ms = at_ms
            action()


class BackingStore:
    """The database behind the cache. A load takes ``latency_ms`` of simulated time; the store counts how many
    are in flight."""

    def __init__(self, sim: Simulator, latency_ms: int) -> None:
        self._sim = sim
        self._latency_ms = latency_ms
        self._rows: dict[str, Money] = {}
        self.loads = 0
        self.in_flight = 0
        self.peak_in_flight = 0

    def write(self, key: str, value: Money) -> None:
        self._rows[key] = value

    def peek(self, key: str) -> Optional[Money]:
        return self._rows.get(key)

    def latency_ms(self) -> int:
        return self._latency_ms

    def load(self, key: str, cb: Waiter) -> None:
        """Starts a load. The value is the row as it stands when the load completes, delivered via the callback."""
        self.loads += 1
        self.in_flight += 1
        self.peak_in_flight = max(self.peak_in_flight, self.in_flight)

        def complete() -> None:
            self.in_flight -= 1
            cb(self._rows.get(key))

        self._sim.after(self._latency_ms, complete)


@dataclass(frozen=True)
class Entry:
    """A cached value with two horizons: fresh until, and (for stale-while-revalidate) usable until."""

    value: Money
    fresh_until_ms: int
    stale_until_ms: int
    load_took_ms: int

    def is_fresh(self, now: int) -> bool:
        return now < self.fresh_until_ms

    def is_usable(self, now: int) -> bool:
        return now < self.stale_until_ms


class ClientStats:
    """What every client records: when it asked, when it got an answer, and whether the answer matched the store."""

    def __init__(self) -> None:
        self.latencies: list[int] = []
        self.hits = 0
        self.misses = 0
        self.stale_serves = 0
        self.lock_retries = 0
        self.early_refreshes = 0
        self.early_lead_total_ms = 0

    def average_lead_ms(self) -> int:
        """Average time before expiry at which an early refresh was triggered."""
        return 0 if self.early_refreshes == 0 else self.early_lead_total_ms // self.early_refreshes

    def record(self, latency_ms: int, stale: bool) -> None:
        self.latencies.append(latency_ms)
        if stale:
            self.stale_serves += 1

    def percentile(self, p: float) -> int:
        if not self.latencies:
            return 0
        ordered = sorted(self.latencies)
        rank = math.ceil(p * len(ordered)) - 1  # nearest-rank
        return ordered[max(0, min(len(ordered) - 1, rank))]

    def max(self) -> int:
        return max(self.latencies) if self.latencies else 0


# ---------------------------------------------------------------------------------------------------
# Strategies
# ---------------------------------------------------------------------------------------------------


class Strategy(ABC):
    """The common shape of every strategy: a per-key map of entries, a store, the simulator clock, a stats block,
    and the two operations the workload drives. Subclasses decide what happens on a miss and on an aging hit."""

    def __init__(self, sim: Simulator, store: BackingStore, ttl_ms: int) -> None:
        self.sim = sim
        self.store = store
        self.stats = ClientStats()
        self.entries: dict[str, Entry] = {}
        self.ttl_ms = ttl_ms

    @abstractmethod
    def name(self) -> str: ...

    @abstractmethod
    def get(self, key: str, waiter: Waiter) -> None:
        """A client asks for key at the current simulated time. The waiter is called when a value is available."""

    def invalidate(self, key: str) -> None:
        """Explicit invalidation: forget the key now. For a hot key this is an expiry you chose the moment of."""
        self.entries.pop(key, None)

    def fresh_entry(self, value: Money, load_took_ms: int, grace_ms: int) -> Entry:
        now = self.sim.now()
        return Entry(value, now + self.ttl_ms, now + self.ttl_ms + grace_ms, load_took_ms)

    def measured(self, key: str, arrived_ms: int, inner: Waiter) -> Waiter:
        """Wraps a waiter so that latency and staleness are measured against the store at completion time."""

        def complete(value: Money) -> None:
            stale = value != self.store.peek(key)
            self.stats.record(self.sim.now() - arrived_ms, stale)
            inner(value)

        return complete


class NaiveStrategy(Strategy):
    """Every miss loads. The hot key expires and every request that arrives during one store latency starts its
    own load."""

    def name(self) -> str:
        return "naive (every miss loads)"

    def get(self, key: str, waiter: Waiter) -> None:
        w = self.measured(key, self.sim.now(), waiter)
        e = self.entries.get(key)
        if e is not None and e.is_fresh(self.sim.now()):
            self.stats.hits += 1
            w(e.value)
            return
        self.stats.misses += 1
        started = self.sim.now()

        def loaded(value: Money) -> None:
            self.entries[key] = self.fresh_entry(value, self.sim.now() - started, 0)
            w(value)

        self.store.load(key, loaded)


class SingleFlightStrategy(Strategy):
    """Request coalescing (single-flight): the first miss starts a load and registers itself; every later miss for
    the same key, while that load is in flight, joins the waiting list instead of loading. One load, N answers."""

    def __init__(self, sim: Simulator, store: BackingStore, ttl_ms: int) -> None:
        super().__init__(sim, store, ttl_ms)
        self._in_flight: dict[str, list[Waiter]] = {}

    def name(self) -> str:
        return "single-flight (coalesce)"

    def waiting_for(self, key: str) -> int:
        return len(self._in_flight.get(key, []))

    def get(self, key: str, waiter: Waiter) -> None:
        w = self.measured(key, self.sim.now(), waiter)
        e = self.entries.get(key)
        if e is not None and e.is_fresh(self.sim.now()):
            self.stats.hits += 1
            w(e.value)
            return
        self.stats.misses += 1
        waiters = self._in_flight.get(key)
        if waiters is not None:  # join the flight already in the air
            waiters.append(w)
            return
        self._in_flight[key] = [w]
        started = self.sim.now()

        def loaded(value: Money) -> None:
            self.entries[key] = self.fresh_entry(value, self.sim.now() - started, 0)
            for each in self._in_flight.pop(key):
                each(value)

        self.store.load(key, loaded)


class LockingStrategy(Strategy):
    """A per-key lock, the shape you get with Redis SET NX or a database advisory lock. The lock holder loads;
    everyone else backs off and retries after retry_ms. One load, but every retry is latency and a cache round trip."""

    def __init__(self, sim: Simulator, store: BackingStore, ttl_ms: int, retry_ms: int) -> None:
        super().__init__(sim, store, ttl_ms)
        self._locks: set[str] = set()
        self._retry_ms = retry_ms

    def name(self) -> str:
        return f"lock + retry ({self._retry_ms} ms)"

    def get(self, key: str, waiter: Waiter) -> None:
        self._attempt(key, self.sim.now(), waiter, True)

    def _attempt(self, key: str, arrived_ms: int, waiter: Waiter, first: bool) -> None:
        e = self.entries.get(key)
        if e is not None and e.is_fresh(self.sim.now()):
            if first:
                self.stats.hits += 1
            self.measured(key, arrived_ms, waiter)(e.value)
            return
        if first:
            self.stats.misses += 1
        if key not in self._locks:  # SET key NX: we own the reload
            self._locks.add(key)
            started = self.sim.now()

            def loaded(value: Money) -> None:
                self.entries[key] = self.fresh_entry(value, self.sim.now() - started, 0)
                self._locks.discard(key)
                self.measured(key, arrived_ms, waiter)(value)

            self.store.load(key, loaded)
        else:
            self.stats.lock_retries += 1
            self.sim.after(self._retry_ms, lambda: self._attempt(key, arrived_ms, waiter, False))


class JavaRandom:
    """java.util.Random, bit for bit (48-bit LCG), so jitter and XFetch draws match the Java demo exactly."""

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


DOUBLE_MIN_VALUE = 5e-324  # Java's Double.MIN_VALUE: the smallest positive subnormal


class EarlyExpirationStrategy(Strategy):
    """Probabilistic early expiration (XFetch, Vattani et al. 2015). On a hit, a client recomputes early with a
    probability that rises as expiry approaches, scaled by how long the last load took:
    recompute if now - delta * beta * ln(rand) >= fresh_until. The hot key is refreshed before it expires, by one
    or a few clients rather than by everyone who arrives after the cliff. No coordination, no lock."""

    def __init__(self, sim: Simulator, store: BackingStore, ttl_ms: int, beta: float, seed: int) -> None:
        super().__init__(sim, store, ttl_ms)
        self._beta = beta
        self._rng = JavaRandom(seed)

    def name(self) -> str:
        return f"probabilistic early expiry (beta={self._beta})"

    def should_recompute(self, e: Entry, now: int) -> bool:
        u = self._rng.next_double()
        if u == 0.0:
            u = DOUBLE_MIN_VALUE
        gap = e.load_took_ms * self._beta * -math.log(u)  # >= 0, heavy-tailed
        return now + gap >= e.fresh_until_ms

    def get(self, key: str, waiter: Waiter) -> None:
        w = self.measured(key, self.sim.now(), waiter)
        e = self.entries.get(key)
        if e is not None and e.is_fresh(self.sim.now()):
            if not self.should_recompute(e, self.sim.now()):
                self.stats.hits += 1
                w(e.value)
                return
            self.stats.early_refreshes += 1  # this client volunteers to pay for the reload
            self.stats.early_lead_total_ms += e.fresh_until_ms - self.sim.now()
        else:
            self.stats.misses += 1
        started = self.sim.now()

        def loaded(value: Money) -> None:
            self.entries[key] = self.fresh_entry(value, self.sim.now() - started, 0)
            w(value)

        self.store.load(key, loaded)


class StaleWhileRevalidateStrategy(Strategy):
    """Stale-while-revalidate (RFC 5861). An entry has a fresh horizon and a grace horizon. Between the two, the
    stale value is served immediately and one background refresh is started (coalesced). Clients never wait for
    the store unless the entry is past its grace too; the price is bounded, explicit staleness."""

    def __init__(self, sim: Simulator, store: BackingStore, ttl_ms: int, grace_ms: int) -> None:
        super().__init__(sim, store, ttl_ms)
        self._grace_ms = grace_ms
        self._refreshing: set[str] = set()
        self._cold_waiters: dict[str, list[Waiter]] = {}

    def name(self) -> str:
        return f"stale-while-revalidate (grace {self._grace_ms} ms)"

    def is_refreshing(self, key: str) -> bool:
        return key in self._refreshing

    def get(self, key: str, waiter: Waiter) -> None:
        w = self.measured(key, self.sim.now(), waiter)
        e = self.entries.get(key)
        now = self.sim.now()
        if e is not None and e.is_fresh(now):
            self.stats.hits += 1
            w(e.value)
            return
        if e is not None and e.is_usable(now):  # stale but within grace: serve it, refresh once
            self.stats.hits += 1
            w(e.value)
            self._start_refresh(key)
            return
        self.stats.misses += 1  # cold or past grace: must wait, but coalesce
        self._cold_waiters.setdefault(key, []).append(w)
        self._start_refresh(key)

    def _start_refresh(self, key: str) -> None:
        if key in self._refreshing:
            return
        self._refreshing.add(key)
        started = self.sim.now()

        def loaded(value: Money) -> None:
            self.entries[key] = self.fresh_entry(value, self.sim.now() - started, self._grace_ms)
            self._refreshing.discard(key)
            for each in self._cold_waiters.pop(key, []):
                each(value)

        self.store.load(key, loaded)


# ---------------------------------------------------------------------------------------------------
# Scenario
# ---------------------------------------------------------------------------------------------------


Factory = Callable[[Simulator, BackingStore], Strategy]


def _ignore(_: Money) -> None:
    pass


class Scenario:
    """The hot-key scenario: steady traffic on one key for a while, a write to the store, and the TTL cliff."""

    WARM_UP_MS = 200  # traffic starts after the warm-up load has landed

    def __init__(self, requests_per_second: int, duration_ms: int, store_latency_ms: int, ttl_ms: int,
                 write_at_ms: int, seed: int) -> None:
        self.requests_per_second = requests_per_second
        self.duration_ms = duration_ms
        self.store_latency_ms = store_latency_ms
        self.ttl_ms = ttl_ms
        self.write_at_ms = write_at_ms
        self.seed = seed

    def run(self, factory: Factory) -> Strategy:
        """Runs the scenario against a freshly built strategy and returns it with its statistics filled in."""
        sim = Simulator()
        store = BackingStore(sim, self.store_latency_ms)
        key = "balance:acct-hot"
        store.write(key, Money.of_minor(100_000, USD))
        s = factory(sim, store)
        jitter = JavaRandom(self.seed)
        gap_micros = 1_000_000 // self.requests_per_second
        sim.schedule(0, lambda: s.get(key, _ignore))  # one warm-up request fills the cache at t=0
        for t in range(self.WARM_UP_MS * 1000, self.duration_ms * 1000, gap_micros):
            at = t // 1000 + jitter.next_int(2)  # one request every gap, with 0-1 ms of jitter
            sim.schedule(at, lambda: s.get(key, _ignore))
        if self.write_at_ms >= 0:
            sim.schedule(self.write_at_ms,
                         lambda: store.write(key, Money.of_minor(100_000, USD).minus(Money.of_minor(8_000, USD))))
        sim.run()
        return s

    def run_with_invalidation(self, factory: Factory, invalidate_at_ms: int) -> Strategy:
        """Same traffic, but the key is explicitly invalidated at invalidate_at_ms instead of waiting for the TTL."""
        sim = Simulator()
        store = BackingStore(sim, self.store_latency_ms)
        key = "balance:acct-hot"
        store.write(key, Money.of_minor(100_000, USD))
        s = factory(sim, store)
        gap_micros = 1_000_000 // self.requests_per_second
        sim.schedule(0, lambda: s.get(key, _ignore))
        for t in range(self.WARM_UP_MS * 1000, self.duration_ms * 1000, gap_micros):
            sim.schedule(t // 1000, lambda: s.get(key, _ignore))
        sim.schedule(invalidate_at_ms, lambda: s.invalidate(key))
        sim.run()
        return s


# ---------------------------------------------------------------------------------------------------
# Demo
# ---------------------------------------------------------------------------------------------------


def row(s: Strategy) -> str:
    c = s.stats
    return (f"{s.name():<42} loads {s.store.loads:4d}  peak in-flight {s.store.peak_in_flight:4d}  p50 {c.percentile(0.50):4d} ms"
            f"  p99 {c.percentile(0.99):4d} ms  max {c.max():4d} ms  stale {c.stale_serves:5d}  retries {c.lock_retries:5d}"
            f"  early {c.early_refreshes:3d}")


def main() -> None:
    print("== The stampede: one hot balance key, 1,000 requests/s, 10 s of traffic, TTL 5 s, store latency 100 ms ==")
    print("   The store is debited at t=4.0 s (so values served from 4.0 s to the next reload are stale by 80.00 USD).")
    sc = Scenario(1_000, 10_000, 100, 5_000, 4_000, 7)
    results = [
        sc.run(lambda sim, st: NaiveStrategy(sim, st, 5_000)),
        sc.run(lambda sim, st: SingleFlightStrategy(sim, st, 5_000)),
        sc.run(lambda sim, st: LockingStrategy(sim, st, 5_000, 10)),
        sc.run(lambda sim, st: EarlyExpirationStrategy(sim, st, 5_000, 1.0, 11)),
        sc.run(lambda sim, st: StaleWhileRevalidateStrategy(sim, st, 5_000, 2_000)),
    ]
    for s in results:
        print(row(s))

    print()
    print("Reading the table:")
    print("  naive: when the key expires at t=5000 every request in the next 100 ms misses and loads: ~100 concurrent loads for one row.")
    print("  single-flight: one load per cliff; the ~100 waiters share it and wait at most one store latency; the store sees 1 request.")
    print("  lock + retry: one load, but waiters poll; each retry is a cache round trip and 10 ms of added latency.")
    print("  early expiry: a few clients volunteer to reload before the cliff, so the cliff never arrives; no coordination at all.")
    print("  stale-while-revalidate: nobody waits after warm-up; the cost is the stale column, which is bounded by the grace period.")
    print("  stale: every strategy serves ~1,000 stale answers between the write at 4.0 s and the TTL at 5.0 s. That is the TTL hazard from Lesson 03;")
    print("         only invalidation at write time removes it, and invalidation is itself a cliff (below).")

    print()
    print("== Invalidation is a cliff you chose: same traffic, key invalidated at t=2.5 s ==")
    n = sc.run_with_invalidation(lambda sim, st: NaiveStrategy(sim, st, 5_000), 2_500)
    f = sc.run_with_invalidation(lambda sim, st: SingleFlightStrategy(sim, st, 5_000), 2_500)
    print(row(n))
    print(row(f))
    print("Invalidating a hot key on every write is correct (Lesson 03) and produces exactly one stampede per write unless misses are coalesced.")

    print()
    print("== Why beta matters: early-expiry behaviour over 30 s of traffic, no writes, at different beta ==")
    quiet = Scenario(1_000, 30_000, 100, 5_000, -1, 7)
    for beta in (0.5, 1.0, 2.0, 4.0):
        e = quiet.run(lambda sim, st, b=beta: EarlyExpirationStrategy(sim, st, 5_000, b, 11))
        print(f"  beta={beta:.1f}  loads {e.store.loads:3d}  peak in-flight {e.store.peak_in_flight:2d}  early refreshes "
              f"{e.stats.early_refreshes:3d}  avg lead before expiry {e.stats.average_lead_ms():4d} ms  p99 {e.stats.percentile(0.99):3d} ms")
    print("Low beta waits until the last moment, so several clients roll a refresh during the same 100 ms load and duplicate it.\n"
          "High beta refreshes well before expiry and so runs more refresh cycles. The cliff is never reached either way; beta=1 is the paper's default.")


if __name__ == "__main__":
    main()
