"""Lesson: phases/00-setup-and-tooling/05-benchmarking-and-profiling/docs/en.md
Benchmarking and Profiling Without Lying to Yourself: warm-up, forks, percentiles, a blackhole,
coordinated-omission correction, and a sampling-profile aggregator. Run: python3 main.py
Test: python3 -m unittest -v
Sources: Gil Tene, "How NOT to Measure Latency" (Strange Loop 2015); OpenJDK JMH docs; JEP 328 (JFR).
"""

from __future__ import annotations

import bisect
import math
import time
from collections import deque
from dataclasses import dataclass
from typing import Callable

_MASK64 = (1 << 64) - 1


def to_int64(value: int) -> int:
    """Wrap an int to Java's 64-bit two's-complement range, so the blackhole folds exactly like a Java long."""
    value &= _MASK64
    return value - (1 << 64) if value >= (1 << 63) else value


class Ticker:
    """A monotonic nanosecond source. Production uses SystemTicker; tests hand the harness a FakeTicker."""

    def nano_time(self) -> int:
        raise NotImplementedError


class SystemTicker(Ticker):
    """time.perf_counter_ns: monotonic, not wall-clock, the only correct source for elapsed time."""

    def nano_time(self) -> int:
        return time.perf_counter_ns()


class FakeTicker(Ticker):
    """Returns scripted readings in order. The harness reads the ticker exactly twice per iteration."""

    def __init__(self, *readings: int) -> None:
        self._readings = tuple(readings)
        self._next = 0

    def nano_time(self) -> int:
        if self._next >= len(self._readings):
            raise RuntimeError(f"FakeTicker exhausted after {len(self._readings)} readings")
        value = self._readings[self._next]
        self._next += 1
        return value

    def readings_consumed(self) -> int:
        return self._next


class Blackhole:
    """A sink every benchmark body hands its result to. CPython does no dead-code elimination, so here the
    blackhole only keeps the design identical to Java and Go; the XOR fold is still checked by the tests."""

    def __init__(self) -> None:
        self._sink = 0

    def consume(self, value: object) -> None:
        if value is None:
            return
        if isinstance(value, int) and not isinstance(value, bool):
            self._sink = to_int64(self._sink ^ value)
        else:
            self._sink = to_int64(self._sink ^ hash(value))

    def value(self) -> int:
        return self._sink


BenchmarkBody = Callable[[Blackhole], None]


@dataclass(frozen=True)
class Benchmark:
    """A unit of work to time. The body must hand its result to the blackhole."""

    name: str
    body: BenchmarkBody

    def __post_init__(self) -> None:
        if self.name is None or not self.name.strip():
            raise ValueError("benchmark needs a name")
        if self.body is None:
            raise ValueError("benchmark needs a body")


class Stats:
    """Order statistics over a sample. Percentiles use nearest-rank on a sorted copy; never interpolated."""

    @staticmethod
    def percentile(samples: list[int], p: float) -> int:
        """Nearest-rank percentile: the smallest value such that at least p% of samples are <= it."""
        if len(samples) == 0:
            raise ValueError("no samples")
        if p < 0 or p > 100:
            raise ValueError(f"percentile must be in 0..100: {p}")
        ordered = sorted(samples)
        if p == 0:
            return ordered[0]
        rank = math.ceil(p / 100.0 * len(ordered))  # 1-based rank
        return ordered[rank - 1]

    @staticmethod
    def min(samples: list[int]) -> int:
        return Stats.percentile(samples, 0)

    @staticmethod
    def max(samples: list[int]) -> int:
        return Stats.percentile(samples, 100)

    @staticmethod
    def mean(samples: list[int]) -> float:
        if len(samples) == 0:
            raise ValueError("no samples")
        total = 0.0
        for s in samples:
            total += s
        return total / len(samples)

    @staticmethod
    def stddev(samples: list[int]) -> float:
        """Population standard deviation."""
        mean = Stats.mean(samples)
        acc = 0.0
        for s in samples:
            acc += (s - mean) * (s - mean)
        return math.sqrt(acc / len(samples))

    @staticmethod
    def coefficient_of_variation(samples: list[int]) -> float:
        """stddev / mean. Dimensionless, so comparable across benchmarks."""
        mean = Stats.mean(samples)
        return 0.0 if mean == 0 else Stats.stddev(samples) / mean


@dataclass(frozen=True)
class HarnessConfig:
    """How a benchmark is run. Forks are repeated runs that each start on the harness's warm-up schedule."""

    warmup_iterations: int
    measured_iterations: int
    forks: int
    ops_per_iteration: int

    def __post_init__(self) -> None:
        if self.warmup_iterations < 0:
            raise ValueError("warmup_iterations must be >= 0")
        if self.measured_iterations < 1:
            raise ValueError("measured_iterations must be >= 1")
        if self.forks < 1:
            raise ValueError("forks must be >= 1")
        if self.ops_per_iteration < 1:
            raise ValueError("ops_per_iteration must be >= 1")


# Cheap bodies: many ops per iteration so a sample is not dominated by the two ticker reads.
HarnessConfig.QUICK = HarnessConfig(10, 7, 3, 5_000)
# Bodies that already walk thousands of elements: fewer ops per iteration keeps the demo short.
HarnessConfig.HEAVY = HarnessConfig(5, 7, 3, 200)


@dataclass(frozen=True)
class ForkResult:
    """One fork's measured samples in ns/op, plus the discarded warm-up samples for inspection."""

    fork: int
    warmup_ns_per_op: list[int]
    measured_ns_per_op: list[int]

    def median(self) -> int:
        return Stats.percentile(self.measured_ns_per_op, 50)


def _fmt_list(values: list[int]) -> str:
    return "[" + ", ".join(str(v) for v in values) + "]"


@dataclass(frozen=True)
class BenchmarkResult:
    """Every fork's samples pooled, plus the cross-fork spread that tells you whether to believe the number."""

    name: str
    forks: list[ForkResult]
    pooled_ns_per_op: list[int]

    UNSTABLE_CV = 0.10

    def p50(self) -> int:
        return Stats.percentile(self.pooled_ns_per_op, 50)

    def p90(self) -> int:
        return Stats.percentile(self.pooled_ns_per_op, 90)

    def p99(self) -> int:
        return Stats.percentile(self.pooled_ns_per_op, 99)

    def min(self) -> int:
        return Stats.min(self.pooled_ns_per_op)

    def max(self) -> int:
        return Stats.max(self.pooled_ns_per_op)

    def fork_medians(self) -> list[int]:
        return [f.median() for f in self.forks]

    def fork_spread(self) -> float:
        """Spread of the per-fork medians. Above UNSTABLE_CV the forks disagree and the result is noise."""
        return 0.0 if len(self.forks) < 2 else Stats.coefficient_of_variation(self.fork_medians())

    def stable(self) -> bool:
        return self.fork_spread() <= self.UNSTABLE_CV

    def row(self) -> str:
        return "%-32s p50 %8d  p90 %8d  p99 %8d  max %9d  forks %s  spread %5.1f%% %s" % (
            self.name, self.p50(), self.p90(), self.p99(), self.max(), _fmt_list(self.fork_medians()),
            self.fork_spread() * 100, "" if self.stable() else "UNSTABLE")


class Harness:
    """For each fork: warmup_iterations (timed, discarded), then measured_iterations (kept). One iteration is
    ops_per_iteration calls of the body between two ticker readings; the sample is ns/op.
    Invariant: exactly 2 * (warmup + measured) * forks ticker readings per benchmark."""

    def __init__(self, ticker: Ticker, config: HarnessConfig) -> None:
        self._ticker = ticker
        self._config = config
        self._blackhole = Blackhole()

    def run(self, benchmark: Benchmark) -> BenchmarkResult:
        fork_results: list[ForkResult] = []
        pooled: list[int] = []
        for fork in range(1, self._config.forks + 1):
            warm = [self._time_iteration(benchmark.body) for _ in range(self._config.warmup_iterations)]
            measured: list[int] = []
            for _ in range(self._config.measured_iterations):
                sample = self._time_iteration(benchmark.body)
                measured.append(sample)
                pooled.append(sample)
            fork_results.append(ForkResult(fork, warm, measured))
        return BenchmarkResult(benchmark.name, fork_results, pooled)

    def _time_iteration(self, body: BenchmarkBody) -> int:
        bh = self._blackhole
        start = self._ticker.nano_time()
        for _ in range(self._config.ops_per_iteration):
            body(bh)
        end = self._ticker.nano_time()
        elapsed = end - start
        if elapsed < 0:
            raise RuntimeError(f"ticker went backwards: {start} -> {end}")
        return elapsed // self._config.ops_per_iteration  # elapsed >= 0, so floor == Java truncation

    def blackhole(self) -> Blackhole:
        return self._blackhole

    @staticmethod
    def header() -> str:
        return "%-32s %-13s %-13s %-13s %-14s %s" % ("benchmark (ns/op)", "p50", "p90", "p99", "max", "per-fork medians")


class LatencyRecorder:
    """Records latencies from a load generator that sends one request per expected interval, and back-fills
    the requests a stall prevented from being sent (HdrHistogram's recordValueWithExpectedInterval)."""

    def __init__(self) -> None:
        self._raw: list[int] = []
        self._corrected: list[int] = []

    def record(self, latency_nanos: int, expected_interval_nanos: int) -> None:
        if latency_nanos < 0:
            raise ValueError("latency must be >= 0")
        if expected_interval_nanos <= 0:
            raise ValueError("expected interval must be > 0")
        self._raw.append(latency_nanos)
        self._corrected.append(latency_nanos)
        missing = latency_nanos - expected_interval_nanos
        while missing >= expected_interval_nanos:
            self._corrected.append(missing)
            missing -= expected_interval_nanos

    def raw_samples(self) -> list[int]:
        return list(self._raw)

    def corrected_samples(self) -> list[int]:
        return list(self._corrected)

    def raw_count(self) -> int:
        return len(self._raw)

    def corrected_count(self) -> int:
        return len(self._corrected)


class SampleProfile:
    """What a sampling profiler computes: per stack snapshot, the leaf gets one unit of self time and every
    distinct frame on the stack gets one unit of inclusive time. Stacks are given root-first."""

    def __init__(self) -> None:
        self._self: dict[str, int] = {}
        self._inclusive: dict[str, int] = {}
        self._samples = 0

    def record(self, *stack_root_first: str) -> None:
        if len(stack_root_first) == 0:
            raise ValueError("empty stack")
        self._samples += 1
        leaf = stack_root_first[-1]
        self._self[leaf] = self._self.get(leaf, 0) + 1
        for frame in dict.fromkeys(stack_root_first):  # ordered de-dup, like LinkedHashSet
            self._inclusive[frame] = self._inclusive.get(frame, 0) + 1

    def samples(self) -> int:
        return self._samples

    def self_count(self, frame: str) -> int:
        return self._self.get(frame, 0)

    def inclusive_count(self, frame: str) -> int:
        return self._inclusive.get(frame, 0)

    def top_self(self, n: int) -> list[tuple[str, int]]:
        """Frames by self time, descending; ties broken by name so the output is deterministic."""
        return self._top(self._self, n)

    def top_inclusive(self, n: int) -> list[tuple[str, int]]:
        return self._top(self._inclusive, n)

    @staticmethod
    def _top(counts: dict[str, int], n: int) -> list[tuple[str, int]]:
        return sorted(counts.items(), key=lambda e: (-e[1], e[0]))[:n]

    def report(self, n: int) -> str:
        lines = [f"{self._samples} samples", "%-28s %6s %6s" % ("frame", "self", "incl")]
        for frame, count in self.top_self(n):
            lines.append("%-28s %5.1f%% %5.1f%%" % (
                frame, 100.0 * count / self._samples, 100.0 * self.inclusive_count(frame) / self._samples))
        return "\n".join(lines) + "\n"


class JavaRandom:
    """java.util.Random, bit for bit (48-bit LCG), so seeded_ints matches the Java demo exactly."""

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


def seeded_ints(bound: int, seed: int) -> list[int]:
    """Deterministic pseudo-random ints in [0, bound); the same sequence as Java's Random(seed)."""
    random = JavaRandom(seed)
    return [random.next_int(bound) for _ in range(bound)]


class RandomGet:
    """Reads seq[indices[cursor]] and advances; a class so the body does not capture a mutable local."""

    def __init__(self, seq, indices: list[int]) -> None:
        self._seq = seq
        self._indices = indices
        self._cursor = 0

    def run(self, bh: Blackhole) -> None:
        bh.consume(self._seq[self._indices[self._cursor]])
        self._cursor = (self._cursor + 1) % len(self._indices)

    __call__ = run


class SortedMap:
    """The stdlib has no TreeMap; a sorted key list searched with bisect is the O(log n) ordered stand-in."""

    def __init__(self) -> None:
        self._keys: list[int] = []
        self._values: list[int] = []

    def put(self, key: int, value: int) -> None:
        i = bisect.bisect_left(self._keys, key)
        if i < len(self._keys) and self._keys[i] == key:
            self._values[i] = value
        else:
            self._keys.insert(i, key)
            self._values.insert(i, value)

    def get(self, key: int):
        i = bisect.bisect_left(self._keys, key)
        if i < len(self._keys) and self._keys[i] == key:
            return self._values[i]
        return None

    def keys(self) -> list[int]:
        return self._keys


class MapGet:
    def __init__(self, mapping, keys: list[int]) -> None:
        self._map = mapping
        self._keys = keys
        self._cursor = 0

    def run(self, bh: Blackhole) -> None:
        bh.consume(self._map.get(self._keys[self._cursor]))
        self._cursor = (self._cursor + 1) % len(self._keys)

    __call__ = run


def main() -> None:
    print("== 1. Why warm-up matters: the first iterations pay for caches and allocation, not your code ==")
    # 2_000 ops/iteration instead of Java's 20_000: CPython interprets the loop, and the demo must stay under 10 s.
    warm_demo = Harness(SystemTicker(), HarnessConfig(5, 5, 1, 2_000))
    data = seeded_ints(1_000, 7)

    def sum_body(bh: Blackhole) -> None:
        acc = 0
        for v in data:
            acc += v
        bh.consume(acc)

    total = warm_demo.run(Benchmark("sum of 1000 ints", sum_body))
    only = total.forks[0]
    print("warm-up samples (discarded) ns/op: " + _fmt_list(only.warmup_ns_per_op))
    print("measured samples           ns/op: " + _fmt_list(only.measured_ns_per_op))
    print("CPython has no JIT, so the warm-up drop is small; in Java the first sample is often several times the last.")

    print()
    print("== 2. list vs deque, honestly ==")
    harness = Harness(SystemTicker(), HarnessConfig.QUICK)
    heavy = Harness(SystemTicker(), HarnessConfig.HEAVY)
    n = 2_000
    array_list: list[int] = []
    linked_list: deque[int] = deque()
    for i in range(n):
        array_list.append(i)
        linked_list.append(i)
    indices = seeded_ints(n, 42)

    def list_shift(bh: Blackhole) -> None:
        array_list.insert(0, -1)
        bh.consume(array_list.pop(0))

    def deque_shift(bh: Blackhole) -> None:
        linked_list.appendleft(-1)
        bh.consume(linked_list.popleft())

    def list_iterate(bh: Blackhole) -> None:
        acc = 0
        for v in array_list:
            acc += v
        bh.consume(acc)

    def deque_iterate(bh: Blackhole) -> None:
        acc = 0
        for v in linked_list:
            acc += v
        bh.consume(acc)

    print(Harness.header())
    for r in (
        harness.run(Benchmark("list[i] (random)", RandomGet(array_list, indices))),
        harness.run(Benchmark("deque[i] (random)", RandomGet(linked_list, indices))),
        harness.run(Benchmark("list insert(0)+pop(0)", list_shift)),
        harness.run(Benchmark("deque appendleft+popleft", deque_shift)),
        heavy.run(Benchmark("list iterate+sum", list_iterate)),
        heavy.run(Benchmark("deque iterate+sum", deque_iterate)),
    ):
        print(r.row())
    print("[i]: O(1) vs O(n) block walk (64 items per block, so small at n=2000). Front insert: O(n) memmove vs O(1) relink.")
    print("A benchmark that shows only one row is an argument, not a measurement.")

    print()
    print("== 3. dict vs sorted map (bisect) ==")
    hash_map: dict[int, int] = {}
    tree_map = SortedMap()
    for i in range(n):
        hash_map[i * 7919 % 100_003] = i
        tree_map.put(i * 7919 % 100_003, i)
    keys = [(indices[i] % n) * 7919 % 100_003 for i in range(n)]

    def dict_sorted(bh: Blackhole) -> None:
        ks = sorted(hash_map.keys())
        bh.consume(ks[0])

    def tree_iterate(bh: Blackhole) -> None:
        first = None
        for k in tree_map.keys():
            if first is None:
                first = k
        bh.consume(first)

    print(Harness.header())
    for r in (
        harness.run(Benchmark("dict.get", MapGet(hash_map, keys))),
        harness.run(Benchmark("SortedMap.get (bisect)", MapGet(tree_map, keys))),
        heavy.run(Benchmark("dict keys in order (sort)", dict_sorted)),
        heavy.run(Benchmark("SortedMap keys in order (walk)", tree_iterate)),
    ):
        print(r.row())
    print("get: O(1) expected vs O(log n). Ordered traversal: O(n log n) sort vs O(n) walk. Pick the map for the access pattern.")

    print()
    print("== 4. Coordinated omission: one stall, two very different p99s (scripted, deterministic) ==")
    recorder = LatencyRecorder()
    interval = 1_000_000  # the load generator sends one request per 1 ms
    for _ in range(999):
        recorder.record(200_000, interval)  # 999 requests at 0.2 ms
    recorder.record(2_000_000_000, interval)  # one 2 s stall (a GC pause, a lock)
    raw = recorder.raw_samples()
    corrected = recorder.corrected_samples()
    print("raw:       %d samples, p50 %.1f ms, p99 %.1f ms, max %.0f ms" % (
        len(raw), Stats.percentile(raw, 50) / 1e6, Stats.percentile(raw, 99) / 1e6, Stats.max(raw) / 1e6))
    print("corrected: %d samples, p50 %.1f ms, p99 %.1f ms, max %.0f ms" % (
        len(corrected), Stats.percentile(corrected, 50) / 1e6, Stats.percentile(corrected, 99) / 1e6,
        Stats.max(corrected) / 1e6))
    print("During the 2 s stall the generator should have sent 1,999 more requests, each of which would have waited.")
    print("Raw p99 says the service is fine. Corrected p99 says a user hitting the stall waited over a second.")

    print()
    print("== 5. What a sampling profiler computes (scripted stacks) ==")
    profile = SampleProfile()
    for _ in range(60):
        profile.record("main", "settleBatch", "computeFees", "Money.times")
    for _ in range(25):
        profile.record("main", "settleBatch", "writeFile", "FileChannel.write")
    for _ in range(10):
        profile.record("main", "settleBatch", "computeFees", "HashMap.get")
    for _ in range(5):
        profile.record("main", "settleBatch", "parseInput")
    print(profile.report(5), end="")
    print("Self time names the hot leaf; inclusive time names the caller worth restructuring. computeFees is 70% inclusive, 0% self.")


if __name__ == "__main__":
    main()
