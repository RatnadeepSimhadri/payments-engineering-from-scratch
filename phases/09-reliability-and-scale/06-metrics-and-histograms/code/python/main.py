"""Lesson: phases/09-reliability-and-scale/06-metrics-and-histograms/docs/en.md
Metrics: Counters, Gauges, and Histograms Without Averages
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: Gil Tene, HdrHistogram (log-linear bucketing, "How NOT to Measure Latency"); Prometheus exposition format
         and metric types documentation; Dean & Barroso, "The Tail at Scale" (CACM 2013).
Python 3.11+, standard library only. Same design as the Java version.
"""

from __future__ import annotations

import math
from dataclasses import dataclass

MASK64 = (1 << 64) - 1
LONG_MAX = (1 << 63) - 1
LONG_MIN = -(1 << 63)


def to_int64(x: int) -> int:
    """Reinterpret the low 64 bits of x as a signed Java long."""
    x &= MASK64
    return x - (1 << 64) if x >= 1 << 63 else x


def java_round(x: float) -> int:
    """Math.round(double): round half up, computed exactly (x - floor(x) is exact for doubles below 2**52)."""
    f = math.floor(x)
    return f + 1 if x - f >= 0.5 else f


class Rng:
    """SplitMix64: a tiny seeded generator so every number in the demo and tests is reproducible."""

    def __init__(self, seed: int) -> None:
        self._state = seed & MASK64

    def next_long(self) -> int:
        self._state = (self._state + 0x9E3779B97F4A7C15) & MASK64
        z = self._state
        z = ((z ^ (z >> 30)) * 0xBF58476D1CE4E5B9) & MASK64
        z = ((z ^ (z >> 27)) * 0x94D049BB133111EB) & MASK64
        return to_int64(z ^ (z >> 31))

    def next_double(self) -> float:
        return ((self.next_long() & MASK64) >> 11) * (1.0 / (1 << 53))

    def next_gaussian(self) -> float:
        """Standard normal via Box-Muller; one draw per call, the second is discarded for simplicity."""
        u1 = 1.0 - self.next_double()
        u2 = self.next_double()
        return math.sqrt(-2.0 * math.log(u1)) * math.cos(2 * math.pi * u2)

    def next_log_normal_micros(self, median_micros: float, sigma: float) -> int:
        """Log-normal: the usual shape of service latency, a hump with a long right tail."""
        return max(1, java_round(median_micros * math.exp(sigma * self.next_gaussian())))


class Counter:
    """A monotonically non-decreasing count. Rates are derived by the scraper, never stored here."""

    def __init__(self) -> None:
        self._value = 0

    def inc(self, n: int = 1) -> None:
        if n < 0:
            raise ValueError(f"counter can only go up: {n}")
        self._value += n

    def value(self) -> int:
        return self._value

    @staticmethod
    def rate(previous: int, current: int, seconds: float) -> float:
        """The scraper's rate calculation over two samples. A counter that went down was reset by a process
        restart, and the only honest assumption is that it restarted from zero."""
        if seconds <= 0:
            raise ValueError("interval must be positive")
        delta = current - previous if current >= previous else current
        return delta / seconds


class Gauge:
    """A value that can go up or down: queue depth, in-flight requests, pending outbox rows."""

    def __init__(self) -> None:
        self._value = 0

    def set(self, v: int) -> None:
        self._value = v

    def inc(self) -> None:
        self._value += 1

    def dec(self) -> None:
        self._value -= 1

    def value(self) -> int:
        return self._value


@dataclass(frozen=True)
class Estimate:
    """A percentile estimate with the bucket bounds that contain the true value."""

    percentile: float
    lower: int
    upper: int

    def relative_error(self) -> float:
        """The worst-case relative error of reporting ``upper`` as the answer."""
        return 0.0 if self.lower == 0 else (self.upper - self.lower) / self.lower

    def __str__(self) -> str:
        return f"p{Estimate.trim(self.percentile)} in [{self.lower}, {self.upper}]"

    @staticmethod
    def trim(p: float) -> str:
        return str(int(p)) if p == math.floor(p) else repr(float(p))


class LogLinearHistogram:
    """A log-linear histogram in the style of HdrHistogram. Values below 2^precision_bits are counted exactly, one
    bucket per integer. Above that, each power-of-two range [2^e, 2^(e+1)) is split into 2^precision_bits equal
    sub-buckets, so every bucket's width is at most 2^-precision_bits of its lower bound: the relative error of any
    percentile is bounded by that fraction regardless of how large the value is. Memory is fixed at construction."""

    def __init__(self, precision_bits: int, highest_trackable: int) -> None:
        if precision_bits < 1 or precision_bits > 16:
            raise ValueError("precision_bits must be 1..16")
        if highest_trackable < (1 << precision_bits):
            raise ValueError("highest_trackable too small")
        self.precision_bits = precision_bits
        self.highest_trackable = highest_trackable
        self._linear = 1 << precision_bits  # 2^precision_bits sub-buckets per power of two
        self._counts = [0] * (self.index_of(highest_trackable) + 1)
        self._total = 0
        self._sum = 0  # exact, for the mean: kept so the demo can show what the mean hides
        self._min = LONG_MAX
        self._max = LONG_MIN

    def index_of(self, v: int) -> int:
        """Which bucket holds ``v``. Exact below 2^precision_bits; log-linear above."""
        if v < self._linear:
            return v
        e = v.bit_length() - 1  # floor(log2 v), e >= precision_bits
        sub = (v - (1 << e)) >> (e - self.precision_bits)  # which of the 2^precision_bits slices of [2^e, 2^(e+1))
        return (e - self.precision_bits + 1) * self._linear + sub

    def lower_bound(self, index: int) -> int:
        """The smallest value that lands in bucket ``index``. Inverse of index_of."""
        if index < self._linear:
            return index
        k = index // self._linear
        e = k + self.precision_bits - 1
        sub = index % self._linear
        return (1 << e) + (sub << (e - self.precision_bits))

    def upper_bound(self, index: int) -> int:
        """The largest value that lands in bucket ``index``."""
        return self.lower_bound(index + 1) - 1

    def record(self, v: int) -> None:
        self.record_many(v, 1)

    def record_many(self, v: int, n: int) -> None:
        if v < 0 or v > self.highest_trackable:
            raise ValueError(f"value out of range: {v}")
        if n <= 0:
            raise ValueError("count must be positive")
        self._counts[self.index_of(v)] += n
        self._total += n
        self._sum += v * n
        if v < self._min:
            self._min = v
        if v > self._max:
            self._max = v

    def count(self) -> int:
        return self._total

    def min(self) -> int:
        return 0 if self._total == 0 else self._min

    def max(self) -> int:
        return 0 if self._total == 0 else self._max

    def mean(self) -> float:
        return 0.0 if self._total == 0 else self._sum / self._total

    def bucket_count(self) -> int:
        return len(self._counts)

    def count_at(self, index: int) -> int:
        return self._counts[index]

    def percentile(self, p: float) -> Estimate:
        """Nearest-rank percentile: the bucket containing the value at rank ceil(p/100 * count). The estimate carries
        the bucket's bounds; the true value lies inside them. Reports ``upper`` as the point value, never below the truth."""
        if p < 0 or p > 100:
            raise ValueError("percentile must be 0..100")
        if self._total == 0:
            return Estimate(p, 0, 0)
        rank = max(1, math.ceil(p / 100.0 * self._total))
        seen = 0
        for i, c in enumerate(self._counts):
            seen += c
            if seen >= rank:
                return Estimate(p, max(self.lower_bound(i), self._min), min(self.upper_bound(i), self._max))
        return Estimate(p, self._max, self._max)

    def value(self, p: float) -> int:
        return self.percentile(p).upper

    def fraction_at_or_below(self, threshold: int) -> float:
        """Fraction of recorded values at or below ``threshold``: an SLO attainment query answered from the buckets."""
        if self._total == 0:
            return 1.0
        idx = self.index_of(min(threshold, self.highest_trackable))
        return sum(self._counts[: idx + 1]) / self._total

    def merge(self, other: "LogLinearHistogram") -> None:
        """Merge another histogram with identical layout into this one. Percentiles of the merge are exact to the same bound."""
        if other.precision_bits != self.precision_bits or other.highest_trackable != self.highest_trackable:
            raise ValueError("cannot merge histograms with different layouts")
        for i in range(len(self._counts)):
            self._counts[i] += other._counts[i]
        self._total += other._total
        self._sum += other._sum
        if other._total > 0:
            self._min = min(self._min, other._min)
            self._max = max(self._max, other._max)

    def copy(self) -> "LogLinearHistogram":
        h = LogLinearHistogram(self.precision_bits, self.highest_trackable)
        h.merge(self)
        return h

    def cumulative(self, boundaries: list[int]) -> list[int]:
        """Cumulative counts at the given upper bounds: the shape Prometheus classic histograms export (``le``)."""
        out = []
        for b in boundaries:
            idx = self.index_of(min(b, self.highest_trackable))
            out.append(sum(self._counts[: idx + 1]))
        return out


class MetricsRegistry:
    """A name-to-metric registry with a text exposition in the Prometheus format."""

    def __init__(self, precision_bits: int, highest_trackable: int) -> None:
        self._counters: dict[str, Counter] = {}
        self._gauges: dict[str, Gauge] = {}
        self._histograms: dict[str, LogLinearHistogram] = {}
        self.precision_bits = precision_bits
        self.highest_trackable = highest_trackable

    def counter(self, name: str) -> Counter:
        return self._counters.setdefault(name, Counter())

    def gauge(self, name: str) -> Gauge:
        return self._gauges.setdefault(name, Gauge())

    def histogram(self, name: str) -> LogLinearHistogram:
        if name not in self._histograms:
            self._histograms[name] = LogLinearHistogram(self.precision_bits, self.highest_trackable)
        return self._histograms[name]

    def render(self, histogram_boundaries: list[int]) -> str:
        """Prometheus text format. Histograms are exported at the given ``le`` boundaries plus +Inf, _sum and _count."""
        out: list[str] = []
        for name, c in self._counters.items():
            out.append(f"# TYPE {name}_total counter\n")
            out.append(f"{name}_total {c.value()}\n")
        for name, g in self._gauges.items():
            out.append(f"# TYPE {name} gauge\n")
            out.append(f"{name} {g.value()}\n")
        for name, h in self._histograms.items():
            out.append(f"# TYPE {name} histogram\n")
            cum = h.cumulative(histogram_boundaries)
            for b, n in zip(histogram_boundaries, cum):
                out.append(f'{name}_bucket{{le="{b}"}} {n}\n')
            out.append(f'{name}_bucket{{le="+Inf"}} {h.count()}\n')
            out.append(f"{name}_sum {java_round(h.mean() * h.count())}\n")
            out.append(f"{name}_count {h.count()}\n")
        return "".join(out)


MAX_MICROS = 60 * 1_000_000  # one minute


def exact_percentile(sorted_samples: list[int], p: float) -> int:
    """Exact nearest-rank percentile on raw samples, the ground truth the histogram is compared against."""
    rank = max(1, math.ceil(p / 100.0 * len(sorted_samples)))
    return sorted_samples[rank - 1]


def ms(micros: int) -> str:
    return f"{micros / 1000.0:.1f} ms"


def authorization_latencies(n: int, slow_fraction: float, rng: Rng) -> list[int]:
    """A payments authorization endpoint: most calls are fast, a slice hit a slow issuer path."""
    out = []
    for _ in range(n):
        slow = rng.next_double() < slow_fraction
        out.append(rng.next_log_normal_micros(2_000_000, 0.3) if slow else rng.next_log_normal_micros(12_000, 0.5))
    return out


def indent(text: str, n: int) -> str:
    """String.indent(n): prefix every line with n spaces and end with a newline."""
    return "".join(" " * n + line + "\n" for line in text.splitlines())


def main() -> None:
    print("== 1. Bucket layout: precisionBits=7 means at most 1/128 = 0.78% relative error ==")
    layout = LogLinearHistogram(7, MAX_MICROS)
    for v in (5, 127, 128, 129, 255, 256, 1_000, 12_345, 1_000_000, 2_000_000):
        i = layout.index_of(v)
        print(f"  value {v:>10,d} -> bucket {i:5d} covering [{layout.lower_bound(i):,d}, {layout.upper_bound(i):,d}]"
              f"  width {layout.upper_bound(i) - layout.lower_bound(i) + 1:,d}")
    print(f"  buckets allocated for values up to one minute in microseconds: {layout.bucket_count()}")

    print("\n== 2. Averages lie: 100,000 authorizations, 1% hit a slow issuer path ==")
    rng = Rng(2024)
    samples = authorization_latencies(100_000, 0.01, rng)
    h = LogLinearHistogram(7, MAX_MICROS)
    for s in samples:
        h.record(s)
    ordered = sorted(samples)
    print(f"  mean   = {ms(java_round(h.mean()))}   <- under a 100 ms SLO, this looks healthy")
    for p in (50, 90, 99, 99.9):
        e = h.percentile(p)
        print(f"  p{Estimate.trim(p):<5} = {ms(e.upper):<12} histogram says [{ms(e.lower)}, {ms(e.upper)}], "
              f"exact = {ms(exact_percentile(ordered, p))}, bound {e.relative_error() * 100:.2f}%")
    print(f"  fraction of requests at or below 100 ms = {100 * h.fraction_at_or_below(100_000):.3f}%  (the SLI, answered from the buckets)")
    print("  one in a hundred customers waited more than a second; the average never showed it")

    print("\n== 3. Averaging percentiles is wrong; merging histograms is right ==")
    r1, r2 = Rng(7), Rng(8)
    host_a, host_b = LogLinearHistogram(7, MAX_MICROS), LogLinearHistogram(7, MAX_MICROS)
    for s in authorization_latencies(90_000, 0.0, r1):  # healthy host, lots of traffic
        host_a.record(s)
    for s in authorization_latencies(10_000, 0.10, r2):  # sick host, little traffic
        host_b.record(s)
    fleet = host_a.copy()
    fleet.merge(host_b)
    print(f"  host A (90,000 requests): p99 = {ms(host_a.value(99))}   host B (10,000 requests, 10% slow): p99 = {ms(host_b.value(99))}")
    print(f"  average of the two p99s      = {ms((host_a.value(99) + host_b.value(99)) // 2)}   <- meaningless")
    print(f"  max of the two p99s          = {ms(max(host_a.value(99), host_b.value(99)))}   <- pessimistic")
    print(f"  p99 of the merged histogram  = {ms(fleet.value(99))}   <- the fleet p99, computed correctly")
    print(f"  average of means (A, B)      = {ms(java_round((host_a.mean() + host_b.mean()) / 2))}   vs true fleet mean "
          f"{ms(java_round(fleet.mean()))}  (unweighted means are wrong too)")

    print("\n== 4. Counters only go up; the scraper computes the rate and must handle a restart ==")
    auths = Counter()
    auths.inc(1_000)
    t0 = auths.value()
    auths.inc(250)
    print(f"  authorizations_total {t0} -> {auths.value()} over 10 s: rate = {Counter.rate(t0, auths.value(), 10):.1f}/s")
    after_restart = Counter()
    after_restart.inc(40)
    print(f"  after a process restart the counter reads {after_restart.value()}: naive rate = "
          f"{(after_restart.value() - auths.value()) / 10.0:.1f}/s, reset-aware rate = "
          f"{Counter.rate(auths.value(), after_restart.value(), 10):.1f}/s")

    print("\n== 5. Exposition: what a scraper sees ==")
    reg = MetricsRegistry(7, MAX_MICROS)
    reg.counter("auth_requests").inc(100_000)
    reg.counter("auth_failures").inc(312)
    reg.gauge("auth_inflight").set(17)
    reg.histogram("auth_latency_micros").merge(h)
    print(indent(reg.render([10_000, 25_000, 50_000, 100_000, 250_000, 1_000_000, 5_000_000]), 2), end="")


if __name__ == "__main__":
    main()
