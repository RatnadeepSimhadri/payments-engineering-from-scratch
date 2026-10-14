"""Lesson: phases/05-distributed-foundations/08-timeouts-and-deadlines/docs/en.md
Timeouts and Deadline Propagation
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: gRPC "grpc-timeout" header (PROTOCOL-HTTP2.md); Dean & Barroso, "The Tail at Scale" (CACM 2013); Go context
package docs. Python 3.11+, standard library only. Time is injected through Clock; nothing sleeps.
"""

from __future__ import annotations

import math
import re
from dataclasses import dataclass, field
from enum import Enum
from typing import Optional

MASK64 = (1 << 64) - 1
INT64_MAX = (1 << 63) - 1


def to_int64(x: int) -> int:
    """Wrap to a signed 64-bit long, as Java arithmetic does."""
    x &= MASK64
    return x - (1 << 64) if x >= (1 << 63) else x


def tdiv(a: int, b: int) -> int:
    """Java's long division truncates toward zero; Python's // floors."""
    q = abs(a) // abs(b)
    return q if (a >= 0) == (b > 0) else -q


class JavaRandom:
    """java.util.Random's 48-bit LCG and nextInt(bound), ported exactly so seed 42 yields the same latency study."""

    _MULTIPLIER, _ADDEND, _MASK = 0x5DEECE66D, 0xB, (1 << 48) - 1

    def __init__(self, seed: int) -> None:
        self._seed = (seed ^ self._MULTIPLIER) & self._MASK

    def next(self, bits: int) -> int:
        self._seed = (self._seed * self._MULTIPLIER + self._ADDEND) & self._MASK
        result = self._seed >> (48 - bits)
        return result - (1 << 32) if result & (1 << 31) else result  # (int) cast: signed 32-bit

    def next_int(self, bound: int) -> int:
        if bound <= 0:
            raise ValueError("bound must be positive")
        r = self.next(31)
        m = bound - 1
        if bound & m == 0:
            return (bound * r) >> 31
        u = r
        while True:
            r = u % bound
            if ((u - r + m) & 0xFFFFFFFF) < 0x80000000:  # the JDK's int-overflow rejection test
                return r
            u = self.next(31)


class Clock:
    """Source of time. Production uses time.time_ns() // 1_000_000; demos and tests use FakeClock."""

    def now_ms(self) -> int:
        raise NotImplementedError


class FakeClock(Clock):
    """A clock that only moves when the simulation says so."""

    def __init__(self, start_ms: int) -> None:
        self._now = start_ms

    def now_ms(self) -> int:
        return self._now

    def advance(self, ms: int) -> None:
        if ms < 0:
            raise ValueError(f"cannot move time backwards: {ms}")
        self._now += ms


class DeadlineExceededException(RuntimeError):
    """Raised when a call is refused or abandoned because its deadline has passed or cannot be met."""

    def __init__(self, message: str, remaining_ms: int) -> None:
        super().__init__(message)
        self.remaining_ms = remaining_ms


class UnavailableException(RuntimeError):
    """Raised by a simulated dependency that is up but failing. Retryable."""


@dataclass(frozen=True)
class Deadline:
    """An absolute point in time by which work must be finished. A timeout is relative ("500 ms from now");
    a deadline is absolute ("by t = 1500"). Deadlines compose across a call chain, timeouts do not."""

    at_ms: int

    @staticmethod
    def after(clock: Clock, timeout_ms: int) -> "Deadline":
        if timeout_ms < 0:
            raise ValueError(f"timeout must be >= 0: {timeout_ms}")
        return Deadline(Deadline.saturating_add(clock.now_ms(), timeout_ms))

    @staticmethod
    def saturating_add(a: int, b: int) -> int:
        """Java long addition that saturates instead of wrapping: overflow iff both operands disagree in sign with
        the result. Python ints never wrap, so the wrap is reproduced first and then detected the same way."""
        r = to_int64(a + b)
        return INT64_MAX if ((a ^ r) & (b ^ r)) < 0 else r

    def is_none(self) -> bool:
        return self.at_ms == INT64_MAX

    def remaining_ms(self, clock: Clock) -> int:
        """Milliseconds left, clamped at zero. Never negative, so callers can subtract margins safely."""
        if self.is_none():
            return INT64_MAX
        return max(0, self.at_ms - clock.now_ms())

    def is_expired(self, clock: Clock) -> bool:
        return not self.is_none() and clock.now_ms() >= self.at_ms

    def earliest(self, other: "Deadline") -> "Deadline":
        """The tighter of two deadlines. A child may shorten its parent's deadline, never extend it."""
        return self if self.at_ms <= other.at_ms else other


Deadline.NONE = Deadline(INT64_MAX)  # type: ignore[attr-defined]  # no deadline: never expires

_JAVA_LONG = re.compile(r"[+-]?[0-9]+")


@dataclass(frozen=True)
class Context:
    """What travels with a request down the call chain: an identifier for logs and the deadline.
    Modelled on Go's context.Context and gRPC's per-call deadline."""

    request_id: str
    deadline: Deadline

    @staticmethod
    def root(request_id: str) -> "Context":
        return Context(request_id, Deadline.NONE)

    def with_timeout(self, clock: Clock, timeout_ms: int) -> "Context":
        """Derive a child context whose deadline is min(parent, now + timeout). Tightening only."""
        return Context(self.request_id, self.deadline.earliest(Deadline.after(clock, timeout_ms)))

    def without_deadline(self) -> "Context":
        """What a service that drops the deadline effectively passes downstream. The bug this lesson is about."""
        return Context(self.request_id, Deadline.NONE)

    def remaining_ms(self, clock: Clock) -> int:
        return self.deadline.remaining_ms(clock)

    def checkpoint(self, clock: Clock, where: str) -> None:
        """Call before any expensive or irreversible step."""
        if self.deadline.is_expired(clock):
            raise DeadlineExceededException(f"{where}: deadline already passed", 0)

    def require_budget(self, clock: Clock, needed_ms: int, where: str) -> None:
        """Refuse work that cannot finish in the budget: the step that prevents a side effect nobody will read."""
        remaining = self.remaining_ms(clock)
        if remaining < needed_ms:
            raise DeadlineExceededException(f"{where} needs {needed_ms} ms but only {remaining} ms remain", remaining)

    def to_wire_timeout(self, clock: Clock) -> Optional[str]:
        """The remaining budget as a relative duration in the shape of gRPC's grpc-timeout header (digits + unit).
        Relative, not absolute, because the receiver's clock is not ours. None when there is no deadline."""
        if self.deadline.is_none():
            return None
        return f"{self.remaining_ms(clock)}m"

    @staticmethod
    def from_wire(request_id: str, header: Optional[str], clock: Clock) -> "Context":
        """Rebuild a context on the receiving side from the wire header against the receiver's own clock."""
        if header is None:
            return Context.root(request_id)
        if len(header) < 2 or len(header) > 9:
            raise ValueError(f"bad timeout header: {header}")
        unit = header[-1]
        digits = header[:-1]
        if not _JAVA_LONG.fullmatch(digits):          # Long.parseLong: no spaces, no underscores
            raise ValueError(f"For input string: \"{digits}\"")
        value = int(digits)
        if unit == "H":
            ms = value * 3_600_000
        elif unit == "M":
            ms = value * 60_000
        elif unit == "S":
            ms = value * 1_000
        elif unit == "m":
            ms = value
        elif unit == "u":
            ms = tdiv(value, 1_000)
        elif unit == "n":
            ms = tdiv(value, 1_000_000)
        else:
            raise ValueError(f"bad timeout unit: {unit}")
        return Context.root(request_id).with_timeout(clock, ms)


@dataclass(frozen=True)
class Effect:
    at_ms: int
    what: str

    def __str__(self) -> str:
        return f"t={self.at_ms}ms {self.what}"


class EffectLog:
    """Append-only record of irreversible things the simulated services did, with the time they did them."""

    def __init__(self) -> None:
        self._effects: list[Effect] = []

    def record(self, at_ms: int, what: str) -> None:
        self._effects.append(Effect(at_ms, what))

    def all(self) -> list[Effect]:
        return list(self._effects)

    def contains(self, fragment: str) -> bool:
        return any(fragment in e.what for e in self._effects)


class Service:
    """Something that can be called with a context."""

    def call(self, ctx: Context) -> str:
        raise NotImplementedError


class RemoteService(Service):
    """A simulated remote service. Receiving the request costs one hop; the work costs work_ms; the downstream (if any)
    is called next; a side effect (if any) is recorded once the work is done. The response costs one more hop."""

    HOP_MS = 20

    def __init__(self, name: str, work_ms: int, side_effect: Optional[str], downstream: Optional[Service],
                 propagate: bool, clock: FakeClock, log: EffectLog) -> None:
        self.name = name
        self.work_ms = work_ms
        self.side_effect = side_effect
        self.downstream = downstream
        self.propagate = propagate          # False models a service that forgets to forward the deadline
        self._clock = clock
        self._log = log

    def call(self, incoming: Context) -> str:
        self._clock.advance(RemoteService.HOP_MS)                       # request on the wire
        # What arrives is a header, not an object: rebuild the context against this service's clock.
        ctx = Context.from_wire(incoming.request_id, incoming.to_wire_timeout(self._clock), self._clock)
        try:
            ctx.require_budget(self._clock, self.work_ms, self.name)    # admission: refuse what cannot finish
            self._clock.advance(self.work_ms)
            if self.downstream is not None:
                forwarded = ctx if self.propagate else ctx.without_deadline()
                self.downstream.call(forwarded)
            if self.side_effect is not None:
                self._log.record(self._clock.now_ms(), f"{self.name}: {self.side_effect}")
            return f"{self.name} ok"
        finally:
            self._clock.advance(RemoteService.HOP_MS)                   # response (or error) on the wire


class Status(Enum):
    OK = "OK"                    # response arrived before the deadline
    FAILED_FAST = "FAILED_FAST"  # a DeadlineExceededException came back before the deadline: no work was wasted
    TIMED_OUT = "TIMED_OUT"      # the caller stopped waiting; the response, if any, arrived to nobody
    UNAVAILABLE = "UNAVAILABLE"  # the dependency answered with a retryable error


@dataclass(frozen=True)
class CallOutcome:
    status: Status
    elapsed_ms: int
    detail: str


class Client:
    """The edge: the only place a timeout is chosen; everything below derives its budget from this one number."""

    def __init__(self, clock: FakeClock) -> None:
        self._clock = clock

    def invoke(self, service: Service, request_id: str, timeout_ms: int) -> CallOutcome:
        start = self._clock.now_ms()
        ctx = Context.root(request_id).with_timeout(self._clock, timeout_ms)
        try:
            result = service.call(ctx)
            elapsed = self._clock.now_ms() - start
            # The response may have arrived after we stopped listening. Then it is a timeout, whatever it says.
            if elapsed > timeout_ms:
                return CallOutcome(Status.TIMED_OUT, elapsed,
                                   f"response discarded: arrived {elapsed - timeout_ms} ms late")
            return CallOutcome(Status.OK, elapsed, result)
        except DeadlineExceededException as e:
            elapsed = self._clock.now_ms() - start
            return CallOutcome(Status.TIMED_OUT if elapsed > timeout_ms else Status.FAILED_FAST, elapsed, str(e))
        except UnavailableException as e:
            return CallOutcome(Status.UNAVAILABLE, self._clock.now_ms() - start, str(e))


class FlakyService(Service):
    """A dependency that fails its first N calls with UnavailableException, then succeeds. Each call costs latency_ms."""

    def __init__(self, clock: FakeClock, latency_ms: int, fail_before: int) -> None:
        self._clock = clock
        self._latency_ms = latency_ms
        self._failures_left = fail_before
        self.calls = 0

    def call(self, ctx: Context) -> str:
        self.calls += 1
        ctx.require_budget(self._clock, self._latency_ms, "flaky")
        self._clock.advance(self._latency_ms)
        if self._failures_left > 0:
            self._failures_left -= 1
            raise UnavailableException(f"503 from flaky (call {self.calls})")
        return f"flaky ok after {self.calls} call(s)"


@dataclass(frozen=True)
class RetryOutcome:
    status: Status
    attempts: int
    elapsed_ms: int
    attempt_timeouts_ms: list = field(default_factory=list)


class DeadlineAwareRetrier:
    """Retries that live inside a deadline. Each attempt gets min(per_attempt_timeout, remaining), and no attempt starts
    when the remaining budget is below the floor a useful attempt needs."""

    def __init__(self, clock: FakeClock, per_attempt_timeout_ms: int, backoff_ms: int,
                 min_useful_attempt_ms: int) -> None:
        self._clock = clock
        self._per_attempt_timeout_ms = per_attempt_timeout_ms
        self._backoff_ms = backoff_ms
        self._min_useful_attempt_ms = min_useful_attempt_ms

    def call(self, service: Service, ctx: Context) -> RetryOutcome:
        start = self._clock.now_ms()
        budgets: list[int] = []
        attempts = 0
        while True:
            remaining = ctx.remaining_ms(self._clock)
            if remaining < self._min_useful_attempt_ms:
                return RetryOutcome(Status.FAILED_FAST, attempts, self._clock.now_ms() - start, budgets)
            attempt_budget = min(self._per_attempt_timeout_ms, remaining)
            budgets.append(attempt_budget)
            attempts += 1
            attempt = ctx.with_timeout(self._clock, attempt_budget)
            try:
                service.call(attempt)
                return RetryOutcome(Status.OK, attempts, self._clock.now_ms() - start, budgets)
            except UnavailableException:
                sleep = min(self._backoff_ms, ctx.remaining_ms(self._clock))   # never sleep past the deadline
                self._clock.advance(sleep)
            except DeadlineExceededException:
                return RetryOutcome(Status.FAILED_FAST, attempts, self._clock.now_ms() - start, budgets)


@dataclass(frozen=True)
class HedgeResult:
    """Java's Hedger.Result."""

    latency_ms: int
    requests_sent: int
    winner: str


@dataclass(frozen=True)
class Study:
    p50: int
    p99: int
    p99_hedged: int
    extra_load_percent: float


class Hedger:
    """Hedged request: send to the primary; if no answer within hedge_after_ms, also send to a backup and take whichever
    answers first. Only legal for idempotent reads (Phase 5, Lesson 09 covers why)."""

    Result = HedgeResult

    @staticmethod
    def call(primary_latency_ms: int, backup_latency_ms: int, hedge_after_ms: int) -> HedgeResult:
        if primary_latency_ms <= hedge_after_ms:
            return HedgeResult(primary_latency_ms, 1, "primary")
        hedged_arrival = hedge_after_ms + backup_latency_ms
        if primary_latency_ms <= hedged_arrival:
            return HedgeResult(primary_latency_ms, 2, "primary")
        return HedgeResult(hedged_arrival, 2, "backup")

    @staticmethod
    def sample_latency(rng: JavaRandom) -> int:
        """A latency with a long tail: most calls are fast, a few percent hit a stall. Seeded, so reproducible."""
        return 600 + rng.next_int(400) if rng.next_int(100) < 5 else 20 + rng.next_int(40)

    @staticmethod
    def percentile(sorted_values: list[int], p: float) -> int:
        idx = math.ceil(p * len(sorted_values)) - 1
        return sorted_values[max(0, min(len(sorted_values) - 1, idx))]

    @staticmethod
    def study(seed: int, samples: int, hedge_after_ms: int) -> Study:
        rng = JavaRandom(seed)
        plain: list[int] = []
        hedged: list[int] = []
        extra_requests = 0
        for _ in range(samples):
            primary = Hedger.sample_latency(rng)
            backup = Hedger.sample_latency(rng)
            plain.append(primary)
            r = Hedger.call(primary, backup, hedge_after_ms)
            hedged.append(r.latency_ms)
            extra_requests += r.requests_sent - 1
        plain.sort()
        hedged.sort()
        return Study(Hedger.percentile(plain, 0.50), Hedger.percentile(plain, 0.99),
                     Hedger.percentile(hedged, 0.99), 100.0 * extra_requests / samples)


def build_chain(clock: FakeClock, log: EffectLog, propagate: bool) -> Service:
    """checkout -> payments -> processor, where the processor charges a card."""
    processor = RemoteService("processor", 400, "CHARGE 19.99 USD card 4242", None, propagate, clock, log)
    payments = RemoteService("payments", 50, None, processor, propagate, clock, log)
    return RemoteService("checkout", 50, None, payments, propagate, clock, log)


def main() -> None:
    print("== A deadline is absolute; a timeout is relative ==")
    clock = FakeClock(1_000)
    ctx = Context.root("req-1").with_timeout(clock, 500)
    print(f"now={clock.now_ms()} deadline={ctx.deadline.at_ms} remaining={ctx.remaining_ms(clock)}")
    clock.advance(180)
    print(f"now={clock.now_ms()} remaining={ctx.remaining_ms(clock)} wire header={ctx.to_wire_timeout(clock)}")
    child = ctx.with_timeout(clock, 1_000)
    print(f"child asked for 1000 ms, got {child.remaining_ms(clock)} ms (a child can only tighten)")
    clock.advance(400)
    print(f"now={clock.now_ms()} remaining={ctx.remaining_ms(clock)} "
          f"expired={str(ctx.deadline.is_expired(clock)).lower()}")

    print()
    print("== The downstream call that outlives its caller ==")
    c1 = FakeClock(0)
    log1 = EffectLog()
    o1 = Client(c1).invoke(build_chain(c1, log1, False), "pay-1", 500)
    print(f"no propagation : {o1.status.name} after {o1.elapsed_ms} ms; {o1.detail}")
    for e in log1.all():
        print(f"  side effect: {e}")
    print("  customer saw a timeout and will retry; the card was charged anyway.")

    c2 = FakeClock(0)
    log2 = EffectLog()
    o2 = Client(c2).invoke(build_chain(c2, log2, True), "pay-2", 500)
    print(f"propagation    : {o2.status.name} after {o2.elapsed_ms} ms; {o2.detail}")
    print(f"  side effects recorded: {len(log2.all())}")

    c3 = FakeClock(0)
    log3 = EffectLog()
    o3 = Client(c3).invoke(build_chain(c3, log3, True), "pay-3", 700)
    print(f"budget 700 ms  : {o3.status.name} after {o3.elapsed_ms} ms; {o3.detail}")
    for e in log3.all():
        print(f"  side effect: {e}")

    print()
    print("== Retries live inside the deadline, not beside it ==")
    c4 = FakeClock(0)
    r4 = DeadlineAwareRetrier(c4, 300, 50, 50).call(FlakyService(c4, 150, 2),
                                                    Context.root("rt-1").with_timeout(c4, 1_000))
    print(f"deadline 1000 ms: {r4.status.name} after {r4.attempts} attempts, {r4.elapsed_ms} ms, "
          f"per-attempt budgets {r4.attempt_timeouts_ms}")
    c5 = FakeClock(0)
    r5 = DeadlineAwareRetrier(c5, 300, 50, 50).call(FlakyService(c5, 150, 2),
                                                    Context.root("rt-2").with_timeout(c5, 400))
    print(f"deadline  400 ms: {r5.status.name} after {r5.attempts} attempts, {r5.elapsed_ms} ms, "
          f"per-attempt budgets {r5.attempt_timeouts_ms} (third attempt never started)")

    print()
    print("== Hedged requests cut the tail for a few percent more load ==")
    h1 = Hedger.call(800, 40, 60)
    print(f"primary 800 ms, backup 40 ms, hedge after 60 ms -> {h1.latency_ms} ms from {h1.winner}"
          f" using {h1.requests_sent} requests")
    h2 = Hedger.call(30, 40, 60)
    print(f"primary  30 ms                                  -> {h2.latency_ms} ms from {h2.winner}"
          f" using {h2.requests_sent} request")
    s = Hedger.study(42, 10_000, 60)
    print(f"10000 calls, seed 42: p50={s.p50} ms p99={s.p99} ms; hedged p99={s.p99_hedged} ms; "
          f"extra load={s.extra_load_percent:.1f}%")


if __name__ == "__main__":
    main()
