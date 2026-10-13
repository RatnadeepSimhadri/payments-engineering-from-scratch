"""Lesson: phases/05-distributed-foundations/10-retries-backoff-and-jitter/docs/en.md
Retries, Exponential Backoff, and Jitter
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: AWS Architecture Blog "Exponential Backoff and Jitter" (Brooker, 2015); Finagle RetryBudget docs;
Google SRE Book ch. 22 "Addressing Cascading Failures". Python 3.11+, standard library only.
Same design as the Java and Go versions: same xorshift64* generator, same seeds, same schedules.
"""

from __future__ import annotations

import heapq
from dataclasses import dataclass, field
from enum import Enum
from typing import Callable, Protocol

MASK64 = (1 << 64) - 1


class Rng:
    """Deterministic xorshift64* generator, bit-identical to the Java and Go versions.

    Python ints are unbounded, so every step is masked to 64 bits by hand. Never use this for
    anything security-sensitive; it exists so demos and tests are replayable."""

    def __init__(self, seed: int) -> None:
        self.state = (seed & MASK64) or 0x9E3779B97F4A7C15

    def next_long(self) -> int:
        """Returns the next value as a signed 64-bit integer, matching Java's long."""
        x = self.state
        x ^= x >> 12
        x ^= (x << 25) & MASK64
        x ^= x >> 27
        self.state = x
        out = (x * 0x2545F4914F6CDD1D) & MASK64
        return out - (1 << 64) if out >= (1 << 63) else out

    def next_below(self, bound: int) -> int:
        """Uniform integer in [0, bound), using the unsigned value like Long.remainderUnsigned."""
        if bound <= 0:
            raise ValueError(f"bound must be positive: {bound}")
        return (self.next_long() & MASK64) % bound


class Clock(Protocol):
    def now_ms(self) -> int: ...
    def sleep(self, ms: int) -> None: ...


class FakeClock:
    """A clock that only advances when someone sleeps against it. No wall time passes."""

    def __init__(self, start_ms: int = 0) -> None:
        self._now = start_ms

    def now_ms(self) -> int:
        return self._now

    def sleep(self, ms: int) -> None:
        if ms < 0:
            raise ValueError("cannot sleep a negative duration")
        self._now += ms


class Jitter(Enum):
    """How the random component of a delay is drawn. Names follow Brooker's 2015 post."""

    NONE = "none"                  # delay = exp(attempt): everyone who failed together retries together
    FULL = "full"                  # delay = uniform[0, exp]: widest spread, lowest peak
    EQUAL = "equal"                # delay = exp/2 + uniform[0, exp/2]: never sooner than half the backoff
    DECORRELATED = "decorrelated"  # delay = min(cap, uniform[base, 3 * previous]): spread grows with history


class Outcome(Enum):
    """What a call to the payment processor came back with. Retryability is decided once, here."""

    # (code, retryable). Enum members with equal values become aliases, so each needs a distinct code.
    OK = ("ok", False)
    TIMEOUT = ("timeout", True)                    # no response: the processor may or may not have done the work
    CONNECTION_RESET = ("connection_reset", True)  # transport failed before or after the request was read
    RATE_LIMITED = ("rate_limited", True)          # HTTP 429: slow down, so retry only with backoff
    SERVER_ERROR = ("server_error", True)          # HTTP 503/502: transient on the processor side
    BAD_REQUEST = ("bad_request", False)           # HTTP 400: our payload is wrong; the same bytes fail the same way
    UNAUTHORIZED = ("unauthorized", False)         # HTTP 401: our credentials are wrong; retrying is brute force
    CARD_DECLINED = ("card_declined", False)       # a valid business answer, not an error

    def retryable(self) -> bool:
        return self.value[1]


@dataclass(frozen=True)
class RetryPolicy:
    """exp(attempt) = min(cap, base * 2^attempt); jitter decides how much of that interval is random."""

    base_ms: int
    cap_ms: int
    max_attempts: int
    jitter: Jitter

    def __post_init__(self) -> None:
        if self.base_ms <= 0:
            raise ValueError("base_ms must be positive")
        if self.cap_ms < self.base_ms:
            raise ValueError("cap_ms must be >= base_ms")
        if self.max_attempts < 1:
            raise ValueError("max_attempts must be >= 1")

    def exponential(self, attempt: int) -> int:
        """Pure exponential backoff with the cap applied; doubling stops once the cap is reached."""
        delay = self.base_ms
        i = 0
        while i < attempt and delay < self.cap_ms:
            delay = min(self.cap_ms, delay * 2)
            i += 1
        return min(self.cap_ms, delay)

    def delay(self, attempt: int, previous_ms: int, rng: Rng) -> int:
        """Delay before retry number `attempt` (0 = first retry). Only DECORRELATED reads previous_ms."""
        exp = self.exponential(attempt)
        if self.jitter is Jitter.NONE:
            return exp
        if self.jitter is Jitter.FULL:
            return rng.next_below(exp + 1)
        if self.jitter is Jitter.EQUAL:
            return exp // 2 + rng.next_below(exp // 2 + 1)
        upper = min(self.cap_ms, max(self.base_ms, previous_ms) * 3)
        return self.base_ms + rng.next_below(upper - self.base_ms + 1)

    def schedule(self, rng: Rng) -> list[int]:
        """Every delay this policy would produce if every attempt failed: max_attempts - 1 entries."""
        delays: list[int] = []
        previous = self.base_ms
        for attempt in range(self.max_attempts - 1):
            d = self.delay(attempt, previous, rng)
            delays.append(d)
            previous = d
        return delays


class RetryBudget:
    """Finagle-style retry budget: every first attempt deposits percent_can_retry% of a token, every
    retry withdraws a whole token. Integer millitokens, so no float drift."""

    TOKEN = 1000

    def __init__(self, percent_can_retry: int, max_tokens: int) -> None:
        if not 0 <= percent_can_retry <= 100:
            raise ValueError("percent must be 0..100")
        if max_tokens < 1:
            raise ValueError("max_tokens must be >= 1")
        self._deposit = self.TOKEN * percent_can_retry // 100
        self._max_balance = self.TOKEN * max_tokens
        self._balance = 0

    def deposit(self) -> None:
        self._balance = min(self._max_balance, self._balance + self._deposit)

    def try_withdraw(self) -> bool:
        if self._balance < self.TOKEN:
            return False
        self._balance -= self.TOKEN
        return True

    def tokens(self) -> int:
        return self._balance // self.TOKEN


class StopReason(Enum):
    SUCCEEDED = "succeeded"
    NON_RETRYABLE = "non_retryable"
    ATTEMPTS_EXHAUSTED = "attempts_exhausted"
    BUDGET_EXHAUSTED = "budget_exhausted"


@dataclass(frozen=True)
class RetryResult:
    final_outcome: Outcome
    attempts: int
    total_delay_ms: int
    stop_reason: StopReason
    delays: list[int] = field(default_factory=list)

    def succeeded(self) -> bool:
        return self.stop_reason is StopReason.SUCCEEDED


Operation = Callable[[int], Outcome]


class Retrier:
    """Runs an operation under a policy, an optional budget, an injected clock, and a seeded Rng."""

    def __init__(self, policy: RetryPolicy, budget: RetryBudget | None, clock: Clock, rng: Rng) -> None:
        self.policy = policy
        self.budget = budget
        self.clock = clock
        self.rng = rng

    def execute(self, op: Operation) -> RetryResult:
        if self.budget is not None:
            self.budget.deposit()
        delays: list[int] = []
        total = 0
        previous = self.policy.base_ms
        outcome = Outcome.TIMEOUT
        for attempt in range(self.policy.max_attempts):
            outcome = op(attempt)
            if outcome is Outcome.OK:
                return RetryResult(outcome, attempt + 1, total, StopReason.SUCCEEDED, delays)
            if not outcome.retryable():
                return RetryResult(outcome, attempt + 1, total, StopReason.NON_RETRYABLE, delays)
            if attempt == self.policy.max_attempts - 1:
                break
            if self.budget is not None and not self.budget.try_withdraw():
                return RetryResult(outcome, attempt + 1, total, StopReason.BUDGET_EXHAUSTED, delays)
            d = self.policy.delay(attempt, previous, self.rng)
            self.clock.sleep(d)
            delays.append(d)
            total += d
            previous = d
        return RetryResult(outcome, self.policy.max_attempts, total, StopReason.ATTEMPTS_EXHAUSTED, delays)


@dataclass(frozen=True)
class StormReport:
    """What the processor saw during one storm: attempts per bucket, retries per bucket, outcomes."""

    load_per_bucket: list[int]
    retries_per_bucket: list[int]
    succeeded: int
    gave_up: int
    still_waiting: int
    total_attempts: int
    bucket_ms: int

    def peak_load(self) -> int:
        return max(self.load_per_bucket, default=0)

    def peak_load_from(self, from_ms: int) -> int:
        """Highest load in any bucket starting at or after from_ms: what the recovered processor sees."""
        return max((l for i, l in enumerate(self.load_per_bucket) if i * self.bucket_ms >= from_ms), default=0)

    def busy_buckets(self) -> int:
        return sum(1 for l in self.load_per_bucket if l > 0)

    def histogram(self, scale: int) -> str:
        lines = []
        for i, load in enumerate(self.load_per_bucket):
            if load == 0:
                continue
            bars = (load + scale - 1) // scale
            lines.append(f"{i * self.bucket_ms:5d} ms | {load:4d} | " + "#" * bars)
        return "\n".join(lines) + ("\n" if lines else "")


class RetryStorm:
    """A retry storm in virtual time as a discrete-event simulation over a min-heap of (time, client).
    Events are processed in (time, client) order so the RNG is drawn in a fixed order and the whole run
    is replayable from its seed in all three languages."""

    @staticmethod
    def run(clients: int, capacity_per_bucket: int, bucket_ms: int, outage_ms: int,
            horizon_buckets: int, policy: RetryPolicy, seed: int) -> StormReport:
        rng = Rng(seed)
        queue: list[tuple[int, int]] = [(0, c) for c in range(clients)]
        heapq.heapify(queue)
        load = [0] * horizon_buckets
        retries = [0] * horizon_buckets
        served = [0] * horizon_buckets
        previous_delay = [policy.base_ms] * clients
        attempts_made = [0] * clients
        horizon_ms = horizon_buckets * bucket_ms
        succeeded = gave_up = total_attempts = 0

        while queue and queue[0][0] < horizon_ms:
            at, c = heapq.heappop(queue)
            bucket = at // bucket_ms
            load[bucket] += 1
            if attempts_made[c] > 0:
                retries[bucket] += 1
            attempts_made[c] += 1
            total_attempts += 1
            healthy = at >= outage_ms
            if healthy and served[bucket] < capacity_per_bucket:
                served[bucket] += 1
                succeeded += 1
                continue
            if attempts_made[c] >= policy.max_attempts:
                gave_up += 1
                continue
            d = policy.delay(attempts_made[c] - 1, previous_delay[c], rng)
            previous_delay[c] = d
            heapq.heappush(queue, (at + max(d, 1), c))  # a zero delay still lands strictly later
        return StormReport(load, retries, succeeded, gave_up, len(queue), total_attempts, bucket_ms)


def main() -> None:
    print("== The schedule: base 100 ms, cap 10 s, 6 attempts, seed 42 ==")
    for j in Jitter:
        print(f"{j.name:<13} {RetryPolicy(100, 10_000, 6, j).schedule(Rng(42))}")
    print(f"{'NONE, cap 1 s':<13} {RetryPolicy(100, 1_000, 8, Jitter.NONE).schedule(Rng(42))}  (cap reached at attempt 4)")

    print("\n== One payment, processor flapping: TIMEOUT, TIMEOUT, OK ==")
    clock = FakeClock(0)
    retrier = Retrier(RetryPolicy(100, 10_000, 5, Jitter.NONE), None, clock, Rng(7))
    script = [Outcome.TIMEOUT, Outcome.TIMEOUT, Outcome.OK]
    r = retrier.execute(lambda attempt: script[min(attempt, len(script) - 1)])
    print(f"result={r.stop_reason.name} attempts={r.attempts} waited={r.total_delay_ms} ms delays={r.delays} clock={clock.now_ms()} ms")

    print("\n== Non-retryable: the issuer declined; a retry would be a second authorization ==")
    declined = retrier.execute(lambda attempt: Outcome.CARD_DECLINED)
    print(f"result={declined.stop_reason.name} attempts={declined.attempts} waited={declined.total_delay_ms} ms")

    print("\n== Retry budget: 20% of organic traffic may be retries, max 10 tokens banked ==")
    budget = RetryBudget(20, 10)
    budgeted = Retrier(RetryPolicy(100, 1_000, 3, Jitter.NONE), budget, FakeClock(0), Rng(1))
    for _ in range(50):
        budgeted.execute(lambda attempt: Outcome.OK)  # healthy traffic banks tokens
    print(f"after 50 healthy requests: {budget.tokens()} tokens banked")
    for i in range(1, 9):
        rr = budgeted.execute(lambda attempt: Outcome.SERVER_ERROR)
        print(f"outage request {i}: attempts={rr.attempts} stop={rr.stop_reason.name:<18} tokens left={budget.tokens()}")

    print("\n== Retry storm: 1000 clients, processor down 1 s, then 200 calls per 100 ms bucket, 8 attempts ==")
    for j in Jitter:
        report = RetryStorm.run(1000, 200, 100, 1_000, 60, RetryPolicy(100, 5_000, 8, j), 2024)
        print(f"\n-- {j.name}: peak after recovery={report.peak_load_from(1_000)}/bucket busy buckets={report.busy_buckets()} "
              f"succeeded={report.succeeded} gave up={report.gave_up} waiting={report.still_waiting} attempts={report.total_attempts}")
        if j in (Jitter.NONE, Jitter.FULL):
            print(report.histogram(25), end="")


if __name__ == "__main__":
    main()
