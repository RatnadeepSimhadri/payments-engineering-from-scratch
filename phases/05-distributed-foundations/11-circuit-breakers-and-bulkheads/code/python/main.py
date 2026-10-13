"""Lesson: phases/05-distributed-foundations/11-circuit-breakers-and-bulkheads/docs/en.md
Circuit Breakers and Bulkheads
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: Nygard, "Release It!" (2007) ch. 5, Stability Patterns; Resilience4j CircuitBreaker and Bulkhead docs;
Google SRE Book ch. 22 "Addressing Cascading Failures". Python 3.11+, standard library only.
Same design as the Java and Go versions: same states, same window arithmetic, same simulation.
"""

from __future__ import annotations

import threading
from dataclasses import dataclass
from enum import Enum
from typing import Callable, Protocol


class Clock(Protocol):
    def now_ms(self) -> int: ...


class FakeClock:
    """A clock the breaker reads. No wall time passes, so every transition is replayable."""

    def __init__(self, start_ms: int = 0) -> None:
        self._now = start_ms

    def now_ms(self) -> int:
        return self._now

    def advance(self, ms: int) -> None:
        if ms < 0:
            raise ValueError("cannot advance a negative duration")
        self._now += ms


class State(Enum):
    CLOSED = "CLOSED"
    OPEN = "OPEN"
    HALF_OPEN = "HALF_OPEN"


class Admission(Enum):
    """Why a call was or was not let through. REJECTED_* outcomes never touched the dependency."""

    ADMITTED = "ADMITTED"
    REJECTED_OPEN = "REJECTED_OPEN"
    REJECTED_BULKHEAD = "REJECTED_BULKHEAD"


class CallOutcome(Enum):
    SUCCESS = "SUCCESS"
    FAILURE = "FAILURE"
    REJECTED_OPEN = "REJECTED_OPEN"
    REJECTED_BULKHEAD = "REJECTED_BULKHEAD"


@dataclass(frozen=True)
class Transition:
    """One recorded state change, for tests and for the operator's timeline."""

    from_state: State
    to_state: State
    at_ms: int


@dataclass(frozen=True)
class BreakerConfig:
    """Tuning in the operator's vocabulary: open when at least minimum_calls calls in the last window_ms
    failed at failure_rate_threshold_percent or worse; stay open for open_duration_ms; then let
    half_open_permits probes through."""

    failure_rate_threshold_percent: int
    minimum_calls: int
    window_ms: int
    bucket_ms: int
    open_duration_ms: int
    half_open_permits: int

    def __post_init__(self) -> None:
        if not 1 <= self.failure_rate_threshold_percent <= 100:
            raise ValueError("threshold must be 1..100")
        if self.minimum_calls < 1:
            raise ValueError("minimum_calls must be >= 1")
        if self.bucket_ms <= 0 or self.window_ms < self.bucket_ms or self.window_ms % self.bucket_ms != 0:
            raise ValueError("window_ms must be a positive multiple of bucket_ms")
        if self.open_duration_ms <= 0:
            raise ValueError("open_duration_ms must be positive")
        if self.half_open_permits < 1:
            raise ValueError("half_open_permits must be >= 1")


class SlidingWindow:
    """A time-based sliding window of call results: a ring of fixed-width buckets.

    A bucket is reused when the clock reaches its slot again, and any bucket older than window_ms is
    ignored when counting. Eviction is implicit in the arithmetic: nothing runs in the background."""

    _EMPTY = None

    def __init__(self, window_ms: int, bucket_ms: int) -> None:
        if bucket_ms <= 0 or window_ms < bucket_ms or window_ms % bucket_ms != 0:
            raise ValueError("window_ms must be a positive multiple of bucket_ms")
        self.window_ms = window_ms
        self.bucket_ms = bucket_ms
        n = window_ms // bucket_ms
        self._bucket_start: list[int | None] = [None] * n
        self._successes = [0] * n
        self._failures = [0] * n

    def record(self, now_ms: int, success: bool) -> None:
        start = now_ms - (now_ms % self.bucket_ms)          # Python % already floors like Math.floorMod
        i = (start // self.bucket_ms) % len(self._bucket_start)
        if self._bucket_start[i] != start:                 # the slot belongs to an older period: reuse it
            self._bucket_start[i] = start
            self._successes[i] = 0
            self._failures[i] = 0
        if success:
            self._successes[i] += 1
        else:
            self._failures[i] += 1

    def _live(self, i: int, now_ms: int) -> bool:
        start = self._bucket_start[i]
        return start is not None and now_ms - start < self.window_ms

    def total(self, now_ms: int) -> int:
        return sum(self._successes[i] + self._failures[i]
                   for i in range(len(self._bucket_start)) if self._live(i, now_ms))

    def failures(self, now_ms: int) -> int:
        return sum(self._failures[i] for i in range(len(self._bucket_start)) if self._live(i, now_ms))

    def failure_rate_percent(self, now_ms: int) -> int:
        """Integer percent, 0 when the window is empty. Integer arithmetic so every language agrees."""
        t = self.total(now_ms)
        return 0 if t == 0 else (100 * self.failures(now_ms)) // t

    def reset(self) -> None:
        n = len(self._bucket_start)
        self._bucket_start = [None] * n
        self._successes = [0] * n
        self._failures = [0] * n


class CircuitBreaker:
    """CLOSED -> OPEN when the window's failure rate crosses the threshold with enough calls;
    OPEN -> HALF_OPEN when open_duration_ms has elapsed and someone asks for a permit;
    HALF_OPEN -> OPEN on any probe failure; HALF_OPEN -> CLOSED when half_open_permits probes all succeeded.
    One lock guards every method; each is short and touches only counters."""

    def __init__(self, config: BreakerConfig, clock: Clock) -> None:
        self.config = config
        self.clock = clock
        self._window = SlidingWindow(config.window_ms, config.bucket_ms)
        self._transitions: list[Transition] = []
        self._state = State.CLOSED
        self._opened_at_ms = 0
        self._probes_issued = 0
        self._probe_successes = 0
        self._rejected = 0
        self._lock = threading.Lock()

    def state(self) -> State:
        with self._lock:
            return self._state

    def rejected(self) -> int:
        with self._lock:
            return self._rejected

    def transitions(self) -> list[Transition]:
        with self._lock:
            return list(self._transitions)

    def failure_rate_percent(self) -> int:
        with self._lock:
            return self._window.failure_rate_percent(self.clock.now_ms())

    def _transition_to(self, nxt: State) -> None:
        self._transitions.append(Transition(self._state, nxt, self.clock.now_ms()))
        self._state = nxt

    def try_acquire(self) -> bool:
        """Ask for permission to call the dependency. The only place OPEN becomes HALF_OPEN: there is no
        timer thread, so the first caller after the open duration performs the transition."""
        with self._lock:
            return self._try_acquire_locked()

    def _try_acquire_locked(self) -> bool:
        now = self.clock.now_ms()
        if self._state is State.CLOSED:
            return True
        if self._state is State.OPEN:
            if now - self._opened_at_ms < self.config.open_duration_ms:
                self._rejected += 1
                return False
            self._transition_to(State.HALF_OPEN)
            self._probes_issued = 0
            self._probe_successes = 0
            return self._try_acquire_locked()
        # HALF_OPEN
        if self._probes_issued >= self.config.half_open_permits:
            self._rejected += 1
            return False
        self._probes_issued += 1
        return True

    def on_success(self) -> None:
        with self._lock:
            now = self.clock.now_ms()
            if self._state is State.CLOSED:
                self._window.record(now, True)
            elif self._state is State.HALF_OPEN:
                self._probe_successes += 1
                if self._probe_successes >= self.config.half_open_permits:
                    self._window.reset()
                    self._transition_to(State.CLOSED)
            # OPEN: a late result from before the trip; it says nothing about the dependency now

    def on_failure(self) -> None:
        with self._lock:
            now = self.clock.now_ms()
            if self._state is State.CLOSED:
                self._window.record(now, False)
                if (self._window.total(now) >= self.config.minimum_calls
                        and self._window.failure_rate_percent(now) >= self.config.failure_rate_threshold_percent):
                    self._opened_at_ms = now
                    self._transition_to(State.OPEN)
            elif self._state is State.HALF_OPEN:
                self._opened_at_ms = now
                self._transition_to(State.OPEN)
            # OPEN: late result; already open


class Bulkhead:
    """At most max_concurrent calls to one dependency in flight at once. A hand-rolled counting semaphore so
    the three languages share one design; threading.BoundedSemaphore is the production equivalent.
    Acquisition never blocks: a full bulkhead rejects, it does not queue."""

    def __init__(self, max_concurrent: int) -> None:
        if max_concurrent < 1:
            raise ValueError("max_concurrent must be >= 1")
        self.max_concurrent = max_concurrent
        self._in_flight = 0
        self._rejected = 0
        self._lock = threading.Lock()

    def try_acquire(self) -> bool:
        with self._lock:
            if self._in_flight >= self.max_concurrent:
                self._rejected += 1
                return False
            self._in_flight += 1
            return True

    def release(self) -> None:
        with self._lock:
            if self._in_flight == 0:
                raise RuntimeError("release without acquire")
            self._in_flight -= 1

    def in_flight(self) -> int:
        with self._lock:
            return self._in_flight

    def available(self) -> int:
        with self._lock:
            return self.max_concurrent - self._in_flight

    def rejected(self) -> int:
        with self._lock:
            return self._rejected


class GuardedCall:
    """Breaker and bulkhead composed around one dependency. The bulkhead is checked first: a full bulkhead is
    a statement about us (we are saturated), not about the dependency, so it must not count toward the
    failure rate and must not consume a half-open probe."""

    def __init__(self, breaker: CircuitBreaker, bulkhead: Bulkhead) -> None:
        self.breaker = breaker
        self.bulkhead = bulkhead

    def begin(self) -> Admission:
        if not self.bulkhead.try_acquire():
            return Admission.REJECTED_BULKHEAD
        if not self.breaker.try_acquire():
            self.bulkhead.release()
            return Admission.REJECTED_OPEN
        return Admission.ADMITTED

    def complete(self, success: bool) -> None:
        self.bulkhead.release()
        if success:
            self.breaker.on_success()
        else:
            self.breaker.on_failure()

    def call(self, op: Callable[[], bool]) -> CallOutcome:
        a = self.begin()
        if a is Admission.REJECTED_BULKHEAD:
            return CallOutcome.REJECTED_BULKHEAD
        if a is Admission.REJECTED_OPEN:
            return CallOutcome.REJECTED_OPEN
        try:
            ok = bool(op())
        except Exception:      # the dependency's exception is a failure, never a crash of the caller
            ok = False
        self.complete(ok)
        return CallOutcome.SUCCESS if ok else CallOutcome.FAILURE


@dataclass(frozen=True)
class TimelineRow:
    """Per-second counts of what the checkout saw, plus the breaker state at the end of the second."""

    second: int
    success: int
    failure: int
    rejected_open: int
    rejected_bulkhead: int
    state_at_end: State


@dataclass(frozen=True)
class OutageReport:
    rows: list[TimelineRow]
    transitions: list[Transition]
    total_success: int
    total_failure: int
    total_rejected_open: int
    total_rejected_bulkhead: int

    def count_in(self, from_ms: int, to_ms: int, kind: CallOutcome) -> int:
        n = 0
        for r in self.rows:
            start = r.second * 1000
            if start < from_ms or start >= to_ms:
                continue
            n += {CallOutcome.SUCCESS: r.success, CallOutcome.FAILURE: r.failure,
                  CallOutcome.REJECTED_OPEN: r.rejected_open, CallOutcome.REJECTED_BULKHEAD: r.rejected_bulkhead}[kind]
        return n

    def table(self) -> str:
        lines = [f"{'sec':>4} | {'ok':>4} | {'fail':>4} | {'rej_open':>8} | {'rej_bulk':>8} | state"]
        for r in self.rows:
            lines.append(f"{r.second:>4} | {r.success:>4} | {r.failure:>4} | {r.rejected_open:>8} | "
                         f"{r.rejected_bulkhead:>8} | {r.state_at_end.value}")
        return "\n".join(lines) + "\n"


class OutageSimulation:
    """A card-processor outage in virtual time. The checkout sends one authorization every call_interval_ms.
    While the processor is healthy a call completes at once with success. During [outage_start_ms,
    outage_end_ms) a call hangs for timeout_ms and then fails. Completions are processed before the new call
    at the same tick, so the run is a pure function of its parameters and identical in all three languages."""

    @staticmethod
    def run(config: BreakerConfig, bulkhead_size: int, call_interval_ms: int, horizon_ms: int,
            outage_start_ms: int, outage_end_ms: int, timeout_ms: int) -> OutageReport:
        clock = FakeClock(0)
        breaker = CircuitBreaker(config, clock)
        bulkhead = Bulkhead(bulkhead_size)
        guarded = GuardedCall(breaker, bulkhead)
        pending: list[int] = []     # due times of hung calls; dues are monotone so insertion order is sorted

        seconds = horizon_ms // 1000
        ok = [0] * seconds
        fail = [0] * seconds
        rej_open = [0] * seconds
        rej_bulk = [0] * seconds
        state_at_end = [State.CLOSED] * seconds

        for t in range(0, horizon_ms, call_interval_ms):
            clock.advance(t - clock.now_ms())
            sec = t // 1000
            while pending and pending[0] <= t:          # hung calls time out: each is a failure
                pending.pop(0)
                guarded.complete(False)
                fail[sec] += 1
            a = guarded.begin()
            if a is Admission.REJECTED_OPEN:
                rej_open[sec] += 1
            elif a is Admission.REJECTED_BULKHEAD:
                rej_bulk[sec] += 1
            else:
                healthy = t < outage_start_ms or t >= outage_end_ms
                if healthy:
                    guarded.complete(True)
                    ok[sec] += 1
                else:
                    pending.append(t + timeout_ms)
            state_at_end[sec] = breaker.state()

        rows = [TimelineRow(s, ok[s], fail[s], rej_open[s], rej_bulk[s], state_at_end[s]) for s in range(seconds)]
        return OutageReport(rows, breaker.transitions(), sum(ok), sum(fail), sum(rej_open), sum(rej_bulk))


DEMO = BreakerConfig(50, 10, 5_000, 1_000, 3_000, 3)


def main() -> None:
    print("== Sliding window: 5 s of 1 s buckets, failures age out ==")
    w = SlidingWindow(5_000, 1_000)
    for _ in range(8):
        w.record(0, True)
    for _ in range(12):
        w.record(1_500, False)
    print(f"t=1500: total={w.total(1_500)} failures={w.failures(1_500)} rate={w.failure_rate_percent(1_500)}%")
    print(f"t=5000: total={w.total(5_000)} failures={w.failures(5_000)} rate={w.failure_rate_percent(5_000)}%"
          "  (the 8 successes from t=0 have aged out)")
    print(f"t=6500: total={w.total(6_500)} failures={w.failures(6_500)} rate={w.failure_rate_percent(6_500)}%"
          "  (now the failures are gone too)")

    print("\n== Breaker by hand: 50% of at least 10 calls, open 3 s, 3 probes ==")
    clock = FakeClock(0)
    cb = CircuitBreaker(DEMO, clock)
    for _ in range(5):
        cb.try_acquire(); cb.on_success()
    for _ in range(4):
        cb.try_acquire(); cb.on_failure()
    print(f"5 ok + 4 fail: rate={cb.failure_rate_percent()}% state={cb.state().value} (9 calls, below minimum_calls=10)")
    cb.try_acquire(); cb.on_failure()
    print(f"5 ok + 5 fail: rate={cb.failure_rate_percent()}% state={cb.state().value}")
    print(f"t={clock.now_ms()} try_acquire={cb.try_acquire()} (fail fast, no call made)")
    clock.advance(2_999)
    print(f"t={clock.now_ms()} try_acquire={cb.try_acquire()} state={cb.state().value}")
    clock.advance(1)
    print(f"t={clock.now_ms()} try_acquire={cb.try_acquire()} state={cb.state().value}"
          " (first caller after the open duration performs the transition)")
    cb.on_failure()
    print(f"probe failed: state={cb.state().value}, open timer restarted at t={clock.now_ms()}")
    clock.advance(3_000)
    for _ in range(3):
        cb.try_acquire(); cb.on_success()
    print(f"t={clock.now_ms()} three probes succeeded: state={cb.state().value}")
    print("transitions: " + ", ".join(f"{t.from_state.value}->{t.to_state.value}@{t.at_ms}" for t in cb.transitions()))

    print("\n== Bulkhead: 3 permits ==")
    bh = Bulkhead(3)
    results = [bh.try_acquire() for _ in range(4)]
    print(f"acquire x4: {' '.join(str(r).lower() for r in results)}  in_flight={bh.in_flight()} rejected={bh.rejected()}")
    bh.release()
    print(f"after one release: acquire={str(bh.try_acquire()).lower()} in_flight={bh.in_flight()}")

    print("\n== Card processor outage: 20 auth/s, bulkhead 10, 2 s timeout, processor dead from t=5 s to t=15 s ==")
    report = OutageSimulation.run(DEMO, 10, 50, 25_000, 5_000, 15_000, 2_000)
    print(report.table(), end="")
    print("transitions:")
    for tr in report.transitions:
        print(f"  t={tr.at_ms:>6} ms  {tr.from_state.value} -> {tr.to_state.value}")
    print(f"totals: ok={report.total_success} fail={report.total_failure} "
          f"rejected_open={report.total_rejected_open} rejected_bulkhead={report.total_rejected_bulkhead}")
    during = (report.count_in(5_000, 15_000, CallOutcome.SUCCESS) + report.total_failure
              + report.count_in(5_000, 15_000, CallOutcome.REJECTED_OPEN)
              + report.count_in(5_000, 15_000, CallOutcome.REJECTED_BULKHEAD))
    print(f"calls that waited 2 s for a dead processor: {report.total_failure} of {during} calls during the outage")


if __name__ == "__main__":
    main()
