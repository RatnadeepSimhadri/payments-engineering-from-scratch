"""Lesson: phases/09-reliability-and-scale/02-load-balancing-algorithms/docs/en.md
Load Balancing: Round Robin, Least Connections, Power of Two Choices
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: Mitzenmacher, "The Power of Two Choices in Randomized Load Balancing" (2001);
         Karger et al., "Consistent Hashing and Random Trees" (1997); nginx smooth weighted round robin.
Python 3.11+, standard library only. Same design as the Java and Go versions.
"""

from __future__ import annotations

import bisect
import math
from collections import deque
from dataclasses import dataclass, field

MASK64 = (1 << 64) - 1


class Rng:
    """SplitMix64: a tiny seeded generator that produces identical streams in Java, Python, and Go."""

    def __init__(self, seed: int) -> None:
        self.state = seed & MASK64

    def next_long(self) -> int:
        """Returns the next 64-bit value as a signed integer, matching Java's long."""
        self.state = (self.state + 0x9E3779B97F4A7C15) & MASK64
        z = self.state
        z = ((z ^ (z >> 30)) * 0xBF58476D1CE4E5B9) & MASK64
        z = ((z ^ (z >> 27)) * 0x94D049BB133111EB) & MASK64
        z ^= z >> 31
        return z - (1 << 64) if z >= 1 << 63 else z

    def next_double(self) -> float:
        """Uniform in [0, 1) with 53 bits of precision."""
        return ((self.next_long() & MASK64) >> 11) * (2.0 ** -53)

    def next_int(self, bound: int) -> int:
        """Uniform integer in [0, bound)."""
        return int(self.next_double() * bound)

    def next_exponential(self, mean: float) -> float:
        """Exponentially distributed with the given mean: the classic model of request work and arrival gaps."""
        return -mean * math.log(1.0 - self.next_double())


@dataclass(eq=False)
class Backend:
    """A server behind the balancer. Mutable because its load changes with every routed request."""

    id: str
    weight: int = 1          # relative capacity for weighted strategies
    speed: float = 1.0       # service-time divisor: 2.0 finishes work twice as fast as 1.0
    active_connections: int = 0
    total_assigned: int = 0
    healthy: bool = True     # flipped by the HealthChecker, never by a strategy

    def __post_init__(self) -> None:
        if self.weight <= 0:
            raise ValueError(f"weight must be positive: {self.id}")
        if self.speed <= 0:
            raise ValueError(f"speed must be positive: {self.id}")

    def __repr__(self) -> str:
        return self.id


@dataclass(frozen=True)
class Request:
    """One unit of work: who it is for (the affinity key) and how much work it carries, in ms on a speed-1.0 backend."""

    id: int
    key: str
    work_ms: float


class NoHealthyBackends(RuntimeError):
    pass


class Balancer:
    """A balancing strategy chooses among the currently healthy backends. It never mutates them."""

    name = "balancer"

    def pick(self, healthy: list[Backend], request: Request) -> Backend:
        raise NotImplementedError


class RoundRobin(Balancer):
    """Plain round robin: the i-th request goes to backend i mod n. Ignores weight, speed, and load."""

    name = "round-robin"

    def __init__(self) -> None:
        self.counter = 0

    def pick(self, healthy: list[Backend], request: Request) -> Backend:
        if not healthy:
            raise NoHealthyBackends("no healthy backends")
        b = healthy[self.counter % len(healthy)]
        self.counter += 1
        return b


class WeightedRoundRobin(Balancer):
    """Smooth weighted round robin (the nginx algorithm): weights 5:1:1 yield a a b a c a a."""

    name = "weighted-round-robin"

    def __init__(self) -> None:
        self.current: dict[str, int] = {}

    def pick(self, healthy: list[Backend], request: Request) -> Backend:
        if not healthy:
            raise NoHealthyBackends("no healthy backends")
        total = 0
        best: Backend | None = None
        best_score = -(1 << 62)
        for b in healthy:
            score = self.current.get(b.id, 0) + b.weight
            self.current[b.id] = score
            total += b.weight
            if score > best_score:
                best_score, best = score, b
        assert best is not None
        self.current[best.id] = best_score - total
        return best


class LeastConnections(Balancer):
    """The backend with the fewest in-flight requests; ties go to the earliest in the list."""

    name = "least-connections"

    def pick(self, healthy: list[Backend], request: Request) -> Backend:
        if not healthy:
            raise NoHealthyBackends("no healthy backends")
        best = healthy[0]
        for b in healthy:
            if b.active_connections < best.active_connections:
                best = b
        return best


class PowerOfTwoChoices(Balancer):
    """Sample two distinct backends uniformly at random and take the less loaded one."""

    name = "power-of-two-choices"

    def __init__(self, rng: Rng) -> None:
        self.rng = rng

    def pick(self, healthy: list[Backend], request: Request) -> Backend:
        if not healthy:
            raise NoHealthyBackends("no healthy backends")
        if len(healthy) == 1:
            return healthy[0]
        i = self.rng.next_int(len(healthy))
        j = self.rng.next_int(len(healthy) - 1)
        if j >= i:
            j += 1  # distinct second sample
        a, b = healthy[i], healthy[j]
        return b if b.active_connections < a.active_connections else a


def fnv1a64(s: str) -> int:
    """FNV-1a, 64-bit: five lines and identical in every language. Its high bits barely depend on the last byte."""
    h = 0xCBF29CE484222325
    for byte in s.encode("utf-8"):
        h ^= byte
        h = (h * 0x100000001B3) & MASK64
    return h


def fmix64(z: int) -> int:
    """MurmurHash3's 64-bit finaliser: spreads every input bit across the whole word."""
    z &= MASK64
    z ^= z >> 33
    z = (z * 0xFF51AFD7ED558CCD) & MASK64
    z ^= z >> 33
    z = (z * 0xC4CEB9FE1A85EC53) & MASK64
    z ^= z >> 33
    return z


def hash64(s: str) -> int:
    """The ring hash as an unsigned 64-bit integer: FNV-1a for bytes-to-word, fmix64 to spread neighbours apart."""
    return fmix64(fnv1a64(s))


class ConsistentHash(Balancer):
    """Consistent hashing with virtual nodes: same key, same backend; removing a backend moves only its keys."""

    name = "consistent-hash"

    def __init__(self, virtual_nodes: int) -> None:
        if virtual_nodes <= 0:
            raise ValueError("virtual_nodes must be positive")
        self.virtual_nodes = virtual_nodes
        self._ring_hashes: list[int] = []
        self._ring_owners: list[Backend] = []
        self._built_for: list[str] = []

    def _rebuild(self, healthy: list[Backend]) -> None:
        points = []
        for b in healthy:
            for v in range(self.virtual_nodes):
                points.append((hash64(f"{b.id}#{v}"), b))
        points.sort(key=lambda p: p[0])  # stable: equal hashes keep insertion order
        self._ring_hashes = [p[0] for p in points]
        self._ring_owners = [p[1] for p in points]
        self._built_for = [b.id for b in healthy]

    def pick(self, healthy: list[Backend], request: Request) -> Backend:
        if not healthy:
            raise NoHealthyBackends("no healthy backends")
        if [b.id for b in healthy] != self._built_for:
            self._rebuild(healthy)
        h = hash64(request.key)
        i = bisect.bisect_left(self._ring_hashes, h)  # first ring position >= h, wrapping to 0
        if i == len(self._ring_hashes):
            i = 0
        return self._ring_owners[i]


class HealthChecker:
    """N consecutive failures evict a backend, M consecutive successes readmit it. Consecutive, not cumulative."""

    def __init__(self, fail_threshold: int, success_threshold: int) -> None:
        if fail_threshold <= 0 or success_threshold <= 0:
            raise ValueError("thresholds must be positive")
        self.fail_threshold = fail_threshold
        self.success_threshold = success_threshold
        self._failures: dict[str, int] = {}
        self._successes: dict[str, int] = {}

    def record(self, b: Backend, success: bool) -> bool:
        """Returns True if the backend's health flag changed as a result of this observation."""
        if success:
            self._failures[b.id] = 0
            s = self._successes.get(b.id, 0) + 1
            self._successes[b.id] = s
            if not b.healthy and s >= self.success_threshold:
                b.healthy = True
                return True
        else:
            self._successes[b.id] = 0
            f = self._failures.get(b.id, 0) + 1
            self._failures[b.id] = f
            if b.healthy and f >= self.fail_threshold:
                b.healthy = False
                return True
        return False


class Pool:
    """The balancer plus its backends plus the health checker: the thing a request actually hits."""

    def __init__(self, backends: list[Backend], balancer: Balancer, health: HealthChecker) -> None:
        if not backends:
            raise ValueError("pool needs at least one backend")
        self.backends = list(backends)
        self.balancer = balancer
        self.health = health

    def healthy(self) -> list[Backend]:
        return [b for b in self.backends if b.healthy]

    def route(self, request: Request) -> Backend | None:
        """Routes the request, or returns None when every backend is evicted (the caller sheds load)."""
        h = self.healthy()
        if not h:
            return None
        b = self.balancer.pick(h, request)
        b.active_connections += 1
        b.total_assigned += 1
        return b

    def complete(self, b: Backend, success: bool) -> None:
        b.active_connections -= 1
        self.health.record(b, success)


@dataclass(frozen=True)
class SimResult:
    """What one simulation run measured. Latencies are in milliseconds."""

    strategy: str
    requests: int
    failed: int
    shed: int
    mean_ms: float
    p50_ms: float
    p99_ms: float
    max_queue: int
    max_assigned: int
    min_assigned: int

    def line(self) -> str:
        return (f"{self.strategy:<22} mean {self.mean_ms:8.2f}  p50 {self.p50_ms:7.2f}  p99 {self.p99_ms:9.2f}"
                f"  max-queue {self.max_queue:4d}  assigned {self.min_assigned:4d}..{self.max_assigned:<5d}"
                f" failed {self.failed}  shed {self.shed}")


def percentile(sorted_values: list[float], p: float) -> float:
    """Nearest-rank percentile on a sorted list."""
    if not sorted_values:
        return 0.0
    rank = math.ceil(p / 100.0 * len(sorted_values))
    return sorted_values[max(0, min(len(sorted_values) - 1, rank - 1))]


class Simulation:
    """Discrete-event simulation: each backend is a single FIFO server, so latency = queueing + service time.

    A backend's queue length is exactly its active_connections, which is what least-connections and P2C read.
    """

    probe_interval_ms = 500.0

    def __init__(self, backends: list[Backend], balancer: Balancer, health: HealthChecker,
                 mean_arrival_gap_ms: float, mean_work_ms: float, rng: Rng) -> None:
        self.backends = backends
        self.pool = Pool(backends, balancer, health)
        self.mean_arrival_gap_ms = mean_arrival_gap_ms
        self.mean_work_ms = mean_work_ms
        self.rng = rng
        self.outages: dict[str, tuple[float, float]] = {}
        self._in_flight: dict[str, deque[tuple[float, bool]]] = {b.id: deque() for b in backends}
        self._free_at: dict[str, float] = {b.id: 0.0 for b in backends}
        self._next_probe: dict[str, float] = {}

    def outage(self, backend_id: str, from_ms: float, to_ms: float) -> None:
        """Declares that a backend fails every request between from_ms and to_ms."""
        self.outages[backend_id] = (from_ms, to_ms)

    def _is_down(self, b: Backend, now: float) -> bool:
        w = self.outages.get(b.id)
        return w is not None and w[0] <= now < w[1]

    def _retire_completed(self, now: float) -> None:
        for b in self.backends:
            q = self._in_flight[b.id]
            while q and q[0][0] <= now:
                _, ok = q.popleft()
                self.pool.complete(b, ok)

    def _probe_evicted(self, now: float) -> None:
        """An evicted backend gets no traffic, so only an active probe can ever readmit it."""
        for b in self.backends:
            if b.healthy:
                continue
            if now >= self._next_probe.get(b.id, 0.0):
                self.pool.health.record(b, not self._is_down(b, now))
                self._next_probe[b.id] = now + self.probe_interval_ms

    def run(self, requests: int, key_space: int) -> SimResult:
        now = 0.0
        latencies: list[float] = []
        failed = shed = max_queue = 0
        for i in range(requests):
            now += self.rng.next_exponential(self.mean_arrival_gap_ms)
            work = self.rng.next_exponential(self.mean_work_ms)
            key = f"merchant-{self.rng.next_int(key_space)}"
            self._retire_completed(now)
            self._probe_evicted(now)
            b = self.pool.route(Request(i, key, work))
            if b is None:
                shed += 1
                continue
            max_queue = max(max_queue, b.active_connections)
            down = self._is_down(b, now)
            service = 1.0 if down else work / b.speed  # a dead backend fails fast
            start = max(now, self._free_at[b.id])
            done = start + service
            self._free_at[b.id] = done
            self._in_flight[b.id].append((done, not down))
            if down:
                failed += 1
            else:
                latencies.append(done - now)
        latencies.sort()
        mean = sum(latencies) / len(latencies) if latencies else 0.0
        assigned = [b.total_assigned for b in self.backends]
        return SimResult(self.pool.balancer.name, len(latencies), failed, shed, mean,
                         percentile(latencies, 50), percentile(latencies, 99), max_queue, max(assigned), min(assigned))


def balls_in_bins(balls: int, bins: int, choices: int, rng: Rng) -> int:
    """Maximum load after dropping balls into bins with d random choices each."""
    load = [0] * bins
    max_load = 0
    for _ in range(balls):
        best = rng.next_int(bins)
        for _ in range(1, choices):
            cand = rng.next_int(bins)
            if load[cand] < load[best]:
                best = cand
        load[best] += 1
        max_load = max(max_load, load[best])
    return max_load


def fleet() -> list[Backend]:
    """Five fast servers and one at half speed: a fleet mid-rollout or with a noisy neighbour."""
    return [Backend("auth-1", 2, 1.0), Backend("auth-2", 2, 1.0), Backend("auth-3", 2, 1.0),
            Backend("auth-4", 2, 1.0), Backend("auth-5", 2, 1.0), Backend("auth-6", 1, 0.5)]


def simulate(balancer: Balancer, seed: int, with_outage: bool) -> SimResult:
    sim = Simulation(fleet(), balancer, HealthChecker(3, 2), 2.2, 10.0, Rng(seed))
    if with_outage:
        sim.outage("auth-3", 5_000, 8_000)
    return sim.run(20_000, 500)


def main() -> None:
    print("== Balls into bins: 10,000 balls, 10,000 bins ==")
    for d in (1, 2, 3):
        print(f"{d} choice(s): max load {balls_in_bins(10_000, 10_000, d, Rng(7))}")

    print()
    print("== Authorization fleet: 5 fast servers + 1 at half speed, 20,000 requests, ~83% utilisation ==")
    for s in (RoundRobin(), WeightedRoundRobin(), LeastConnections(), PowerOfTwoChoices(Rng(11)), ConsistentHash(100)):
        print(simulate(s, 42, False).line())

    print()
    print("== Same fleet, auth-3 dies from t=5s to t=8s; health checker evicts after 3 failures ==")
    for s in (RoundRobin(), LeastConnections(), PowerOfTwoChoices(Rng(11))):
        print(simulate(s, 42, True).line())

    print()
    print("== Smooth weighted round robin, weights a=5 b=1 c=1 ==")
    wrr = WeightedRoundRobin()
    abc = [Backend("a", 5, 1), Backend("b", 1, 1), Backend("c", 1, 1)]
    print(" ".join(wrr.pick(abc, Request(i, "k", 1)).id for i in range(7)))

    print()
    print("== Consistent hashing: remove one of 6 backends, how many of 1,000 keys move? ==")
    ch = ConsistentHash(100)
    six = fleet()
    five = six[:2] + six[3:]
    moved = sum(1 for k in range(1000)
                if ch.pick(six, Request(k, f"merchant-{k}", 1)) is not ch.pick(five, Request(k, f"merchant-{k}", 1)))
    print(f"moved {moved} of 1000 keys (ideal 1/6 = 167); modulo hashing would move ~833")


if __name__ == "__main__":
    main()
