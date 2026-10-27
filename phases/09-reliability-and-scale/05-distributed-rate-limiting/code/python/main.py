"""Lesson: phases/09-reliability-and-scale/05-distributed-rate-limiting/docs/en.md
Distributed Rate Limiting: token buckets across N nodes, three coordination strategies measured under seeded load
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: RFC 6585 (429 Too Many Requests); IETF draft-ietf-httpapi-ratelimit-headers; Redis INCR/EXPIRE docs;
         Stripe "Scaling your API with rate limiters" (2017). Python 3.11+, standard library only. Same design as the Java version.
"""

from __future__ import annotations

from abc import ABC, abstractmethod
from collections import deque
from dataclasses import dataclass


class Clock(ABC):
    """Injected time source so every simulation is deterministic and no test ever sleeps."""

    @abstractmethod
    def now_millis(self) -> int: ...


class FakeClock(Clock):
    def __init__(self, start_millis: int) -> None:
        self._now = start_millis

    def now_millis(self) -> int:
        return self._now

    def advance(self, millis: int) -> None:
        if millis < 0:
            raise ValueError("time does not go backwards")
        self._now += millis


class TokenBucket:
    """Token bucket with integer arithmetic. Tokens are stored in milli-tokens so a refill rate of
    7 tokens/second accrues exactly 7 milli-tokens per millisecond with no floating point drift."""

    def __init__(self, capacity: int, rate_per_second: int, now_millis: int) -> None:
        if capacity <= 0:
            raise ValueError("capacity must be positive")
        if rate_per_second < 0:
            raise ValueError("rate must be non-negative")
        self._capacity_milli = capacity * 1000
        self._rate_per_second = rate_per_second  # tokens per second == milli-tokens per millisecond
        self._tokens_milli = self._capacity_milli  # a new bucket starts full: the burst allowance is immediate
        self._last_refill_millis = now_millis

    def refill(self, now_millis: int) -> None:
        """Lazy refill: add rate * elapsed, clamp to capacity. Called before every decision."""
        if now_millis <= self._last_refill_millis:
            return
        elapsed = now_millis - self._last_refill_millis
        added = elapsed * self._rate_per_second
        self._tokens_milli = min(self._capacity_milli, self._tokens_milli + added)
        self._last_refill_millis = now_millis

    def try_acquire(self, tokens: int, now_millis: int) -> bool:
        self.refill(now_millis)
        needed = tokens * 1000
        if self._tokens_milli >= needed:
            self._tokens_milli -= needed
            return True
        return False

    def available_tokens(self, now_millis: int) -> int:
        """Whole tokens available at ``now_millis`` after lazy refill (floor). Exposed for tests and the demo."""
        self.refill(now_millis)
        return self._tokens_milli // 1000

    def rate_per_second(self) -> int:
        return self._rate_per_second

    def set_rate_per_second(self, rate_per_second: int) -> None:
        """Used by the periodic-sync strategy to hand a node a new share of the global rate."""
        if rate_per_second < 0:
            raise ValueError("rate must be non-negative")
        self._rate_per_second = rate_per_second


@dataclass(frozen=True)
class Decision:
    """Per-request outcome plus the simulated cost of reaching the decision."""

    allowed: bool
    latency_millis: int
    store_calls: int


class SharedStore:
    """Simulated shared store (Redis-like). Every call costs one round trip and is counted so the
    strategies can be compared on latency and coordination traffic, not just accuracy."""

    def __init__(self, rtt_millis: int) -> None:
        self._rtt_millis = rtt_millis
        self._buckets: dict[str, TokenBucket] = {}
        self._logs: dict[str, deque[int]] = {}
        self._usage_reports: dict[str, list[int]] = {}  # key -> per-node counts
        self._calls = 0

    def rtt_millis(self) -> int:
        return self._rtt_millis

    def calls(self) -> int:
        return self._calls

    def bucket(self, key: str, capacity: int, rate: int, now_millis: int) -> TokenBucket:
        self._calls += 1
        if key not in self._buckets:
            self._buckets[key] = TokenBucket(capacity, rate, now_millis)
        return self._buckets[key]

    def log(self, key: str) -> deque[int]:
        self._calls += 1
        return self._logs.setdefault(key, deque())

    def usage(self, key: str, nodes: int) -> list[int]:
        self._calls += 1
        if key not in self._usage_reports:
            self._usage_reports[key] = [0] * nodes
        return self._usage_reports[key]


class RateLimiter(ABC):
    """The contract every strategy implements: one node asks whether one request for one key may proceed."""

    @abstractmethod
    def try_acquire(self, node: int, key: str, now_millis: int) -> Decision: ...

    @abstractmethod
    def name(self) -> str: ...


class CentralizedLimiter(RateLimiter):
    """Strategy 1: one bucket per key in the shared store. Every decision is a round trip; the answer is exact."""

    def __init__(self, store: SharedStore, capacity: int, rate_per_second: int) -> None:
        self._store = store
        self._capacity = capacity
        self._rate_per_second = rate_per_second

    def try_acquire(self, node: int, key: str, now_millis: int) -> Decision:
        bucket = self._store.bucket(key, self._capacity, self._rate_per_second, now_millis)
        ok = bucket.try_acquire(1, now_millis)
        return Decision(ok, self._store.rtt_millis(), 1)

    def name(self) -> str:
        return "centralized"


class LocalSyncLimiter(RateLimiter):
    """Strategy 2: each node owns a local bucket holding a share of the global rate and decides in-process
    (zero latency). Every sync_interval the nodes report their demand to the store and the shares are
    redistributed in proportion to it. Between syncs the global limit is only approximately enforced."""

    def __init__(self, store: SharedStore, nodes: int, capacity: int, rate_per_second: int,
                 sync_interval_millis: int) -> None:
        if nodes <= 0:
            raise ValueError("need at least one node")
        self._store = store
        self._nodes = nodes
        self._capacity = capacity
        self._rate_per_second = rate_per_second
        self._sync_interval_millis = sync_interval_millis
        self._local: dict[str, list[TokenBucket]] = {}
        self._demand_since_sync: dict[str, list[int]] = {}
        self._last_sync: dict[str, int] = {}
        self._syncs = 0

    def _buckets_for(self, key: str, now_millis: int) -> list[TokenBucket]:
        buckets = self._local.get(key)
        if buckets is None:
            buckets = [TokenBucket(max(1, self._capacity // self._nodes), self._rate_per_second // self._nodes, now_millis)
                       for _ in range(self._nodes)]
            self._local[key] = buckets
            self._last_sync[key] = now_millis
            self._demand_since_sync[key] = [0] * self._nodes
        return buckets

    def try_acquire(self, node: int, key: str, now_millis: int) -> Decision:
        buckets = self._buckets_for(key, now_millis)
        demand = self._demand_since_sync[key]
        demand[node] += 1
        store_calls = 0
        latency = 0
        if now_millis - self._last_sync[key] >= self._sync_interval_millis:
            store_calls = self.sync(key, buckets, demand, now_millis)
            latency = self._store.rtt_millis()  # the sync is off the request path in production; charged here for honesty
        ok = buckets[node].try_acquire(1, now_millis)
        return Decision(ok, latency, store_calls)

    def sync(self, key: str, buckets: list[TokenBucket], demand: list[int], now_millis: int) -> int:
        """Redistribute the global rate in proportion to each node's observed demand; floor at 1/(4N) so a cold
        node can still start."""
        shared = self._store.usage(key, self._nodes)
        total = 0
        for i in range(self._nodes):
            shared[i] = demand[i]
            total += demand[i]
        floor = max(1, self._rate_per_second // (4 * self._nodes))
        remaining = self._rate_per_second - floor * self._nodes
        for i in range(self._nodes):
            share = floor + (remaining // self._nodes if total == 0 else remaining * shared[i] // total)
            buckets[i].refill(now_millis)
            buckets[i].set_rate_per_second(share)
            demand[i] = 0
        self._last_sync[key] = now_millis
        self._syncs += 1
        return 1

    def syncs(self) -> int:
        return self._syncs

    def rate_of(self, key: str, node: int) -> int:
        return self._local[key][node].rate_per_second()

    def name(self) -> str:
        return f"local+sync({self._sync_interval_millis}ms)"


class SlidingWindowLogLimiter(RateLimiter):
    """Strategy 3: a shared sorted log of admitted timestamps per key. Exact over any sliding window,
    one round trip per decision, and memory proportional to the limit itself."""

    def __init__(self, store: SharedStore, limit: int, window_millis: int) -> None:
        self._store = store
        self._limit = limit
        self._window_millis = window_millis

    def try_acquire(self, node: int, key: str, now_millis: int) -> Decision:
        log = self._store.log(key)
        cutoff = now_millis - self._window_millis
        while log and log[0] <= cutoff:
            log.popleft()
        if len(log) < self._limit:
            log.append(now_millis)
            return Decision(True, self._store.rtt_millis(), 1)
        return Decision(False, self._store.rtt_millis(), 1)

    def log_size(self, key: str) -> int:
        return len(self._store.log(key))

    def name(self) -> str:
        return "sliding-log"


class LoadReport:
    """What the load generator records: enough to judge accuracy, latency, and coordination cost."""

    def __init__(self, strategy: str, seconds: int) -> None:
        self.strategy = strategy
        self.offered = 0
        self.admitted = 0
        self.rejected = 0
        self.latency_total = 0
        self.store_calls = 0
        self.admitted_per_second = [0] * seconds

    def peak_per_second(self) -> int:
        return max(self.admitted_per_second, default=0)

    def mean_latency_millis(self) -> float:
        return 0.0 if self.offered == 0 else self.latency_total / self.offered

    def versus_ideal(self, rate_per_second: int, capacity: int) -> int:
        """Admitted minus the most a correct token bucket could admit (rate * seconds + one full burst), capped at
        what was offered. Positive = leaked over the limit; negative = under-admitted (tokens left unused)."""
        ideal = min(self.offered, rate_per_second * len(self.admitted_per_second) + capacity)
        return self.admitted - ideal


class JavaRandom:
    """java.util.Random, bit for bit (48-bit LCG), so the seeded load matches the Java demo exactly."""

    _MULTIPLIER = 0x5DEECE66D
    _ADDEND = 0xB
    _MASK = (1 << 48) - 1

    def __init__(self, seed: int) -> None:
        self._seed = (seed ^ self._MULTIPLIER) & self._MASK

    def next(self, bits: int) -> int:
        self._seed = (self._seed * self._MULTIPLIER + self._ADDEND) & self._MASK
        r = self._seed >> (48 - bits)
        return r - (1 << 32) if r >= (1 << 31) else r

    def next_double(self) -> float:
        return ((self.next(26) << 27) + self.next(27)) * (1.0 / (1 << 53))


class LoadGenerator:
    """Seeded load generator. Each millisecond each node gets a Bernoulli request draw whose probability
    depends on node skew: node 0 receives ``hot_share`` of the traffic, the rest is spread evenly."""

    def __init__(self, nodes: int, requests_per_second: float, hot_share: float, seed: int) -> None:
        self._nodes = nodes
        self._requests_per_second = requests_per_second
        self._hot_share = hot_share
        self._seed = seed

    def run(self, limiter: RateLimiter, clock: FakeClock, key: str, seconds: int, rate_per_second: int,
            capacity: int) -> LoadReport:
        random = JavaRandom(self._seed)
        report = LoadReport(limiter.name(), seconds)
        start = clock.now_millis()
        for _ in range(seconds * 1000):
            for node in range(self._nodes):
                if self._nodes == 1:
                    share = 1.0
                else:
                    share = self._hot_share if node == 0 else (1.0 - self._hot_share) / (self._nodes - 1)
                p = self._requests_per_second * share / 1000.0
                if random.next_double() < p:
                    d = limiter.try_acquire(node, key, clock.now_millis())
                    report.offered += 1
                    report.latency_total += d.latency_millis
                    report.store_calls += d.store_calls
                    if d.allowed:
                        report.admitted += 1
                        report.admitted_per_second[(clock.now_millis() - start) // 1000] += 1
                    else:
                        report.rejected += 1
            clock.advance(1)
        return report


RATE = 100  # merchant API quota: 100 requests/second
CAPACITY = 100  # one second of burst
NODES = 4
SECONDS = 10
RTT = 2  # milliseconds to the shared store


def compare(offered_rps: float, hot_share: float, seed: int) -> list[LoadReport]:
    load = LoadGenerator(NODES, offered_rps, hot_share, seed)
    reports = []
    c1 = FakeClock(0)
    reports.append(load.run(CentralizedLimiter(SharedStore(RTT), CAPACITY, RATE), c1, "merchant:acme", SECONDS, RATE, CAPACITY))
    c2 = FakeClock(0)
    reports.append(load.run(LocalSyncLimiter(SharedStore(RTT), NODES, CAPACITY, RATE, 1000), c2, "merchant:acme", SECONDS, RATE, CAPACITY))
    c3 = FakeClock(0)
    reports.append(load.run(LocalSyncLimiter(SharedStore(RTT), NODES, CAPACITY, RATE, 100), c3, "merchant:acme", SECONDS, RATE, CAPACITY))
    c4 = FakeClock(0)
    reports.append(load.run(SlidingWindowLogLimiter(SharedStore(RTT), RATE, 1000), c4, "merchant:acme", SECONDS, RATE, CAPACITY))
    return reports


def print_table(title: str, reports: list[LoadReport]) -> None:
    print(f"== {title} ==")
    print(f"{'strategy':<20} {'offered':>8} {'admitted':>9} {'rejected':>9} {'peak/s':>10} {'vs ideal':>10} {'mean ms':>10}")
    for r in reports:
        print(f"{r.strategy:<20} {r.offered:>8d} {r.admitted:>9d} {r.rejected:>9d} {r.peak_per_second():>10d} "
              f"{r.versus_ideal(RATE, CAPACITY):>+10d} {r.mean_latency_millis():>10.2f}")
    print()


def main() -> None:
    print("== A single token bucket: 100/s, capacity 100 ==")
    clock = FakeClock(0)
    bucket = TokenBucket(CAPACITY, RATE, clock.now_millis())
    burst = 0
    while bucket.try_acquire(1, clock.now_millis()):
        burst += 1
    print(f"burst admitted from a full bucket: {burst}")
    clock.advance(10)
    print(f"after 10 ms: {bucket.available_tokens(clock.now_millis())} token(s) refilled (100/s * 0.010 s = 1)")
    clock.advance(5000)
    print(f"after 5 s idle: {bucket.available_tokens(clock.now_millis())} tokens (clamped at capacity, not 500)")
    print()

    print_table("Even load, 4 nodes, 400 offered/s against a 100/s limit (seed 7)", compare(400, 0.25, 7))
    print_table("Skewed load, node 0 takes 85% of 400 offered/s (seed 7)", compare(400, 0.85, 7))
    print_table("Light load, 60 offered/s, nobody should be rejected (seed 7)", compare(60, 0.25, 7))

    print("Reading the table:")
    print("  centralized and sliding-log never exceed 100 admitted in any second: exact, but every call pays the 2 ms round trip.")
    print("  local+sync pays no latency, but with skew the hot node runs dry while cold nodes hold unused tokens, so it under-admits;")
    print("  syncing every 100 ms instead of every 1 s closes much of that gap at 10x the coordination traffic.")
    print("  sliding-log has no burst allowance, so it admits exactly 100 per window and shows -100 against the bucket ideal.")


if __name__ == "__main__":
    main()
