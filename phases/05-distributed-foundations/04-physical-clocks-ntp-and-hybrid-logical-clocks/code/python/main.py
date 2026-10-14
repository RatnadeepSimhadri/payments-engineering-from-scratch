"""Lesson: phases/05-distributed-foundations/04-physical-clocks-ntp-and-hybrid-logical-clocks/docs/en.md
Physical Clocks, NTP, and Hybrid Logical Clocks
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: RFC 5905 (NTPv4) section 8 for the offset/delay equations; Kulkarni, Demirbas, Madeppa, Avva, Leone,
"Logical Physical Clocks" (OPODIS 2014) for the HLC algorithm; Lamport 1978 for logical clocks. Stdlib only.
"""

from __future__ import annotations

import math
from dataclasses import dataclass, field
from functools import total_ordering
from typing import Optional

MASK64 = (1 << 64) - 1


def to_int64(x: int) -> int:
    """Wrap to a signed 64-bit long, as Java arithmetic does."""
    x &= MASK64
    return x - (1 << 64) if x >= (1 << 63) else x


def java_round(x: float) -> int:
    """Math.round(double): floor(x + 0.5). Python's round() is banker's rounding and would disagree on ties."""
    return math.floor(x + 0.5)


def tdiv(a: int, b: int) -> int:
    """Java's long division truncates toward zero; Python's // floors. -301 / 2 is -150 in Java, -151 with //."""
    q = abs(a) // abs(b)
    return q if (a >= 0) == (b > 0) else -q


class JavaRandom:
    """java.util.Random (48-bit LCG), plus the JDK 17+ RandomGenerator.nextLong(bound) it inherits, so the same seed
    draws the same latencies in Java and Python."""

    _MULTIPLIER, _ADDEND, _MASK = 0x5DEECE66D, 0xB, (1 << 48) - 1

    def __init__(self, seed: int) -> None:
        self._seed = (seed ^ self._MULTIPLIER) & self._MASK

    def next(self, bits: int) -> int:
        self._seed = (self._seed * self._MULTIPLIER + self._ADDEND) & self._MASK
        result = self._seed >> (48 - bits)
        return result - (1 << 32) if result & (1 << 31) else result  # (int) cast: signed 32-bit

    def next_long(self, bound: Optional[int] = None) -> int:
        if bound is None:
            return to_int64((self.next(32) << 32) + self.next(32))
        if bound <= 0:
            raise ValueError("bound must be positive")
        m = bound - 1
        r = self.next_long()
        if bound & m == 0:
            return r & m
        u = (r & MASK64) >> 1                        # r >>> 1
        while True:
            r = u % bound
            if to_int64(u + m - r) >= 0:            # reject over-represented candidates (long overflow test)
                return r
            u = (self.next_long() & MASK64) >> 1


class TrueTime:
    """The one true time of the simulation, in milliseconds. Every clock derives its reading from this value."""

    def __init__(self, start_ms: int) -> None:
        self._now_ms = start_ms

    def now(self) -> int:
        return self._now_ms

    def advance(self, ms: int) -> None:
        if ms < 0:
            raise ValueError("time does not go backwards")
        self._now_ms += ms


class DriftingClock:
    """A physical clock: true time distorted by a constant rate error (drift, ppm) and an additive offset.
    step() models NTP correcting the offset; the drift stays, which is why correction is never finished."""

    def __init__(self, true_time: TrueTime, drift_ppm: float, initial_offset_ms: int) -> None:
        self._true_time = true_time
        self._drift_ppm = float(drift_ppm)
        self._offset_ms = initial_offset_ms
        self._epoch_true_ms = true_time.now()

    def now(self) -> int:
        """What this machine believes the time is. Rounded to whole milliseconds like a real clock read."""
        elapsed = self._true_time.now() - self._epoch_true_ms
        drifted = java_round(elapsed * (self._drift_ppm / 1_000_000.0))
        return self._epoch_true_ms + elapsed + drifted + self._offset_ms

    def step(self, delta_ms: int) -> None:
        """Apply a correction, as NTP does when the offset is large: the clock jumps."""
        self._offset_ms += delta_ms

    def error(self) -> int:
        return self.now() - self._true_time.now()


@dataclass(frozen=True)
class NtpSample:
    """One NTP exchange, RFC 5905 section 8: four timestamps, two derived quantities."""

    t1: int
    t2: int
    t3: int
    t4: int

    def offset(self) -> int:
        """How far the client clock is behind the server (positive means client is behind)."""
        return tdiv((self.t2 - self.t1) + (self.t3 - self.t4), 2)

    def delay(self) -> int:
        """Round-trip delay excluding the server's processing time. The offset error is at most delay/2."""
        return (self.t4 - self.t1) - (self.t3 - self.t2)


class NtpClient:
    """Polls a server over a network with seeded, possibly asymmetric latency and keeps the sample with the smallest
    delay: NTP's clock-filter rule, because the fastest round trip is the least contaminated by queueing."""

    def __init__(self, true_time: TrueTime, client: DriftingClock, server: DriftingClock, seed: int,
                 min_latency_ms: int, jitter_ms: int) -> None:
        self._true_time = true_time
        self._client = client
        self._server = server
        self._random = JavaRandom(seed)
        self._min_latency_ms = min_latency_ms
        self._jitter_ms = jitter_ms

    def exchange(self) -> NtpSample:
        """One request/response with independently drawn outbound and return latencies."""
        outbound = self._min_latency_ms + (0 if self._jitter_ms == 0 else self._random.next_long(self._jitter_ms + 1))
        inbound = self._min_latency_ms + (0 if self._jitter_ms == 0 else self._random.next_long(self._jitter_ms + 1))
        t1 = self._client.now()
        self._true_time.advance(outbound)
        t2 = self._server.now()
        t3 = self._server.now()              # zero processing time keeps the arithmetic legible
        self._true_time.advance(inbound)
        t4 = self._client.now()
        return NtpSample(t1, t2, t3, t4)

    def best_of(self, n: int) -> NtpSample:
        """Poll n times and return the sample with the smallest delay (ties: earliest)."""
        best: Optional[NtpSample] = None
        for _ in range(n):
            s = self.exchange()
            if best is None or s.delay() < best.delay():
                best = s
        return best

    def synchronize(self, polls: int) -> int:
        """Measure and correct the client clock by the best offset estimate. Returns the applied correction."""
        best = self.best_of(polls)
        self._client.step(best.offset())
        return best.offset()


class LamportClock:
    """A Lamport timestamp: ordering with no relation to physical time. Here only for the comparison trace."""

    def __init__(self) -> None:
        self._time = 0

    def tick(self) -> int:
        self._time += 1
        return self._time

    def receive(self, remote: int) -> int:
        self._time = max(self._time, remote) + 1
        return self._time

    def peek(self) -> int:
        return self._time


@total_ordering
@dataclass(frozen=True)
class HlcTimestamp:
    """HLC timestamp: l is logical wall time, c breaks ties among events with the same l. Lexicographic order.
    Packs into 64 bits: 48 for l and 16 for c. Python ints are unbounded, so both bounds are checked here."""

    l: int
    c: int

    COUNTER_BITS = 16
    COUNTER_MAX = (1 << 16) - 1

    def __post_init__(self) -> None:
        if self.c < 0 or self.c > HlcTimestamp.COUNTER_MAX:
            raise ValueError(f"counter out of range: {self.c}")
        if self.l < 0 or self.l >= (1 << 48):
            raise ValueError(f"logical time out of range: {self.l}")

    def pack(self) -> int:
        return (self.l << HlcTimestamp.COUNTER_BITS) | self.c

    @staticmethod
    def unpack(packed: int) -> "HlcTimestamp":
        return HlcTimestamp((packed & MASK64) >> HlcTimestamp.COUNTER_BITS, packed & HlcTimestamp.COUNTER_MAX)

    def compare_to(self, o: "HlcTimestamp") -> int:
        if self.l != o.l:
            return -1 if self.l < o.l else 1
        return (self.c > o.c) - (self.c < o.c)

    def __lt__(self, o: "HlcTimestamp") -> bool:
        return self.compare_to(o) < 0

    def __str__(self) -> str:
        return f"{self.l}.{self.c}"


class ClockDriftException(RuntimeError):
    """Raised when l would run more than max_drift_ms ahead of the physical clock."""

    def __init__(self, l: int, pt: int, max_drift: int) -> None:
        super().__init__(f"logical time {l} is {l - pt} ms ahead of physical time {pt}; bound is {max_drift}")


class HybridLogicalClock:
    """Hybrid Logical Clock, Kulkarni et al. 2014, Figure 5. Each node holds (l, c).
     - send/local: l' = max(l, pt); c' = c + 1 if l' == l else 0
     - receive(m): l' = max(l, m.l, pt); c' depends on which of the three l's won the max
    Invariants: l >= pt; l - pt <= max_drift; e happens-before f implies ts(e) < ts(f)."""

    def __init__(self, physical: DriftingClock, max_drift_ms: int) -> None:
        self._physical = physical
        self._max_drift_ms = max_drift_ms
        self._l = physical.now()
        self._c = 0

    def now(self) -> HlcTimestamp:
        """Timestamp a local event or an outgoing message."""
        pt = self._physical.now()
        new_l = max(self._l, pt)
        new_c = self._bump_counter(self._c) if new_l == self._l else 0
        return self._commit(new_l, new_c, pt)

    def receive(self, m: HlcTimestamp) -> HlcTimestamp:
        """Merge an incoming message's timestamp, then timestamp the receive event."""
        pt = self._physical.now()
        new_l = max(self._l, m.l, pt)
        if new_l == self._l and new_l == m.l:
            new_c = self._bump_counter(max(self._c, m.c))
        elif new_l == self._l:
            new_c = self._bump_counter(self._c)
        elif new_l == m.l:
            new_c = self._bump_counter(m.c)
        else:
            new_c = 0
        return self._commit(new_l, new_c, pt)

    def peek(self) -> HlcTimestamp:
        return HlcTimestamp(self._l, self._c)

    @staticmethod
    def _bump_counter(c: int) -> int:
        if c >= HlcTimestamp.COUNTER_MAX:
            raise ArithmeticError("HLC counter overflow: more than 65535 events in one millisecond")
        return c + 1

    def _commit(self, new_l: int, new_c: int, pt: int) -> HlcTimestamp:
        """Validate before mutating, so a rejected timestamp leaves the clock exactly as it was."""
        if new_l - pt > self._max_drift_ms:
            raise ClockDriftException(new_l, pt, self._max_drift_ms)
        self._l = new_l
        self._c = new_c
        return HlcTimestamp(self._l, self._c)


@dataclass(frozen=True)
class TraceEvent:
    """One stamped event in the comparison trace."""

    node: str
    what: str
    true_ms: int
    wall_ms: int
    lamport: int
    hlc: HlcTimestamp


class Node:
    """A node with all three clocks, so the same event can be stamped three ways and the stamps compared."""

    def __init__(self, name: str, true_time: TrueTime, drift_ppm: float, offset_ms: int, max_drift_ms: int) -> None:
        self.name = name
        self._true_time = true_time
        self.wall = DriftingClock(true_time, drift_ppm, offset_ms)
        self.lamport = LamportClock()
        self.hlc = HybridLogicalClock(self.wall, max_drift_ms)

    def local(self, what: str) -> TraceEvent:
        return TraceEvent(self.name, what, self._true_time.now(), self.wall.now(), self.lamport.tick(), self.hlc.now())

    def send(self, what: str) -> TraceEvent:
        return self.local("send " + what)

    def receive(self, what: str, message: TraceEvent) -> TraceEvent:
        return TraceEvent(self.name, "recv " + what, self._true_time.now(), self.wall.now(),
                          self.lamport.receive(message.lamport), self.hlc.receive(message.hlc))


@dataclass(frozen=True)
class TraceReport:
    """Causal pairs (send, receive) and the three orderings' verdicts on them."""

    pairs: int
    wall_violations: int
    lamport_violations: int
    hlc_violations: int
    max_hlc_ahead_of_true_ms: int


@dataclass
class Trace:
    events: list = field(default_factory=list)
    causal_pairs: list = field(default_factory=list)

    def record(self, e: TraceEvent) -> TraceEvent:
        self.events.append(e)
        return e

    def deliver(self, to: Node, what: str, sent: TraceEvent) -> TraceEvent:
        received = self.record(to.receive(what, sent))
        self.causal_pairs.append((sent, received))
        return received

    def report(self) -> TraceReport:
        wall = lamport = hlc = 0
        for a, b in self.causal_pairs:
            if not a.wall_ms < b.wall_ms:
                wall += 1
            if not a.lamport < b.lamport:
                lamport += 1
            if not a.hlc.compare_to(b.hlc) < 0:
                hlc += 1
        max_ahead = 0
        for e in self.events:
            max_ahead = max(max_ahead, e.hlc.l - e.true_ms)
        return TraceReport(len(self.causal_pairs), wall, lamport, hlc, max_ahead)


def payment_trace(t: TrueTime, gateway: Node, ledger: Node, notifier: Node) -> Trace:
    """Three services, one payment: gateway authorises, ledger posts, notifier emails. Clocks disagree by 80 ms."""
    trace = Trace()
    auth = trace.record(gateway.send("authorize pay_7f3"))
    t.advance(4)
    trace.deliver(ledger, "authorize pay_7f3", auth)
    t.advance(2)
    post_ack = trace.record(ledger.send("posted pay_7f3"))
    t.advance(3)
    trace.deliver(gateway, "posted pay_7f3", post_ack)
    t.advance(1)
    capture = trace.record(gateway.send("capture pay_7f3"))
    t.advance(5)
    trace.deliver(ledger, "capture pay_7f3", capture)
    t.advance(2)
    notify = trace.record(ledger.send("notify pay_7f3"))
    t.advance(6)
    trace.deliver(notifier, "notify pay_7f3", notify)
    t.advance(1)
    trace.record(notifier.local("email sent"))
    return trace


def main() -> None:
    print("== A quartz clock drifts ==")
    t = TrueTime(1_700_000_000_000)
    fast = DriftingClock(t, 50, 0)
    t.advance(86_400_000)
    print(f"50 ppm clock after one day: {fast.error()} ms fast")

    print("\n== NTP: estimating offset from four timestamps ==")
    t2 = TrueTime(1_700_000_000_000)
    server = DriftingClock(t2, 0, 0)
    client = DriftingClock(t2, 20, -150)
    ntp = NtpClient(t2, client, server, 42, 10, 0)
    symmetric = ntp.exchange()
    print(f"symmetric 10 ms each way: offset={symmetric.offset()} ms (true -150), delay={symmetric.delay()} ms")
    jittery = NtpClient(t2, client, server, 7, 10, 40)
    for i in range(3):
        s = jittery.exchange()
        print(f"jittery sample {i + 1}: offset={s.offset()} ms, delay={s.delay()} ms, "
              f"error={s.offset() - (-client.error())} ms (bound delay/2={tdiv(s.delay(), 2)})")
    applied = jittery.synchronize(8)
    print(f"best-of-8 correction applied: {applied} ms; residual client error after step: {client.error()} ms")

    print("\n== Three clocks on one payment trace ==")
    t3 = TrueTime(1_700_000_000_000)
    gateway = Node("gateway", t3, 0, 0, 1000)
    ledger = Node("ledger", t3, 0, -80, 1000)     # ledger's wall clock is 80 ms behind
    notifier = Node("notifier", t3, 0, 35, 1000)
    trace = payment_trace(t3, gateway, ledger, notifier)
    t0 = 1_700_000_000_000
    print(f"{'node':<9} {'event':<22} {'true':<8} {'wall':<8} {'lamport':<8} hlc (l shown relative to T0)")
    for e in trace.events:
        print(f"{e.node:<9} {e.what:<22} {e.true_ms - t0:<8} {e.wall_ms - t0:<8} {e.lamport:<8} "
              f"{e.hlc.l - t0}.{e.hlc.c}")
    r = trace.report()
    print(f"causal pairs: {r.pairs}; wall-clock order violated: {r.wall_violations}; "
          f"Lamport violated: {r.lamport_violations}; HLC violated: {r.hlc_violations}")
    print(f"HLC stayed within {r.max_hlc_ahead_of_true_ms} ms of true time; "
          f"Lamport's largest value was {trace.events[-1].lamport} (unrelated to time)")

    print("\n== Bounded drift: HLC refuses to run away from physical time ==")
    t4 = TrueTime(1_700_000_000_000)
    slow = DriftingClock(t4, 0, 0)
    hlc = HybridLogicalClock(slow, 500)
    try:
        hlc.receive(HlcTimestamp(slow.now() + 2_000, 0))
    except ClockDriftException as e:
        print(f"rejected: {e}")
    packed = HlcTimestamp(1_700_000_000_123, 7)
    print(f"pack({packed}) = {packed.pack()}, unpack round-trips: {HlcTimestamp.unpack(packed.pack())}")


if __name__ == "__main__":
    main()
