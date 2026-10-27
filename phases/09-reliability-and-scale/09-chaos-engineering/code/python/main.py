"""Lesson: phases/09-reliability-and-scale/09-chaos-engineering/docs/en.md
Chaos Engineering and Fault Injection
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: Basiri et al., "Chaos Engineering", IEEE Software 2016; principlesofchaos.org; ISO 4217 for Money.
Python 3.11+, standard library only. Deterministic: simulated clock, seeded java.util.Random port, in-memory network.
"""

from __future__ import annotations

import math
from dataclasses import dataclass
from enum import Enum
from typing import Optional


@dataclass(frozen=True)
class Currency:
    """ISO 4217 currency, copied from Phase 10 Lesson 01."""

    code: str
    exponent: int

    def __post_init__(self) -> None:
        if len(self.code) != 3:
            raise ValueError(f"currency code: {self.code}")
        if not 0 <= self.exponent <= 4:
            raise ValueError("exponent must be 0..4")


USD = Currency("USD", 2)


@dataclass(frozen=True)
class Money:
    """Integer minor units + currency. Never a float."""

    minor_units: int
    currency: Currency

    def __post_init__(self) -> None:
        if not isinstance(self.minor_units, int) or isinstance(self.minor_units, bool):
            raise TypeError("minor_units must be an int; never construct Money from a float")

    @staticmethod
    def of_minor(minor_units: int, currency: Currency) -> "Money":
        return Money(minor_units, currency)

    def __str__(self) -> str:
        if self.currency.exponent == 0:
            return f"{self.minor_units} {self.currency.code}"
        scale = 10 ** self.currency.exponent
        whole, frac = divmod(abs(self.minor_units), scale)
        sign = "-" if self.minor_units < 0 else ""
        return f"{sign}{whole}.{frac:0{self.currency.exponent}d} {self.currency.code}"


class SimClock:
    """Manual clock. Every "wait" in the simulation is an explicit advance, so demos and tests are deterministic."""

    def __init__(self, start_millis: int) -> None:
        self._now_millis = start_millis

    def now_millis(self) -> int:
        return self._now_millis

    def advance(self, millis: int) -> None:
        if millis < 0:
            raise ValueError("clock never moves backwards")
        self._now_millis += millis


class JavaRandom:
    """java.util.Random, bit for bit (48-bit LCG), so every seeded draw matches the Java demo exactly."""

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


# ---------------------------------------------------------------------------------------------------
# Faults and blast radius: a sealed family of values
# ---------------------------------------------------------------------------------------------------


class Fault:
    """The four faults this lesson injects. Each is a value: it can be logged, diffed, and replayed.
    The concrete faults are attached below as ``Fault.Latency`` etc., the Python spelling of a sealed interface."""


@dataclass(frozen=True)
class Latency(Fault):
    """Added one-way delay on a network link."""

    added_millis: int

    def __post_init__(self) -> None:
        if self.added_millis < 0:
            raise ValueError("latency must be >= 0")

    def __str__(self) -> str:
        return f"Latency[addedMillis={self.added_millis}]"


@dataclass(frozen=True)
class Error(Fault):
    """The callee fails the request with a 5xx-style error."""

    code: str

    def __str__(self) -> str:
        return f"Error[code={self.code}]"


@dataclass(frozen=True)
class Partition(Fault):
    """The link is cut: requests never arrive and never return."""

    def __str__(self) -> str:
        return "Partition[]"


@dataclass(frozen=True)
class ClockSkew(Fault):
    """The callee's clock runs ahead of (or behind) true time."""

    skew_millis: int

    def __str__(self) -> str:
        return f"ClockSkew[skewMillis={self.skew_millis}]"


Fault.Latency, Fault.Error, Fault.Partition, Fault.ClockSkew = Latency, Error, Partition, ClockSkew


@dataclass(frozen=True)
class AuthRequest:
    payment_id: str
    merchant_id: str
    amount: Money
    idempotency_key: str
    sent_at_millis: int


class BlastRadius:
    """Who is affected. The whole point of chaos in production is that this is small and explicit."""

    def includes(self, request: AuthRequest, rng: Optional[JavaRandom]) -> bool:
        raise NotImplementedError


@dataclass(frozen=True)
class PercentOfRequests(BlastRadius):
    percent: int

    def __post_init__(self) -> None:
        if self.percent < 0 or self.percent > 100:
            raise ValueError("percent 0..100")

    def includes(self, request: AuthRequest, rng: Optional[JavaRandom]) -> bool:
        return rng.next_int(100) < self.percent

    def __str__(self) -> str:
        return f"PercentOfRequests[percent={self.percent}]"


@dataclass(frozen=True)
class Merchants(BlastRadius):
    merchant_ids: frozenset[str]

    def includes(self, request: AuthRequest, rng: Optional[JavaRandom]) -> bool:
        return request.merchant_id in self.merchant_ids

    def __str__(self) -> str:
        return f"Merchants[merchantIds=[{', '.join(sorted(self.merchant_ids))}]]"


@dataclass(frozen=True)
class Everyone(BlastRadius):
    def includes(self, request: AuthRequest, rng: Optional[JavaRandom]) -> bool:
        return True

    def __str__(self) -> str:
        return "Everyone[]"


BlastRadius.PercentOfRequests, BlastRadius.Merchants, BlastRadius.Everyone = PercentOfRequests, Merchants, Everyone


@dataclass(frozen=True)
class Injection:
    """A fault bound to a target and a blast radius."""

    target: str
    fault: Fault
    blast_radius: BlastRadius


class FaultInjector:
    """The fault-injection layer. Lives between caller and callee, decides per request whether an injection
    applies, and is the only component with a "clear everything" switch (the abort path depends on it)."""

    def __init__(self, seed: int) -> None:
        self._injections: list[Injection] = []
        self._rng = JavaRandom(seed)
        self._applied = 0

    def inject(self, target: str, fault: Fault, radius: BlastRadius) -> None:
        self._injections.append(Injection(target, fault, radius))

    def clear(self) -> None:
        self._injections.clear()

    def is_armed(self) -> bool:
        return bool(self._injections)

    def applied_count(self) -> int:
        return self._applied

    def faults_for(self, target: str, request: AuthRequest) -> list[Fault]:
        """Faults whose target and blast radius match this request, in injection order."""
        out: list[Fault] = []
        for inj in self._injections:
            if inj.target == target and inj.blast_radius.includes(request, self._rng):
                out.append(inj.fault)
                self._applied += 1
        return out


class Outcome(Enum):
    AUTHORIZED = "AUTHORIZED"  # good
    DECLINED = "DECLINED"  # good: a legitimate business answer
    TIMEOUT = "TIMEOUT"  # bad: caller gave up
    PROCESSOR_ERROR = "PROCESSOR_ERROR"  # bad: callee failed
    STALE_REQUEST = "STALE_REQUEST"  # bad: callee rejected the request timestamp

    def is_good(self) -> bool:
        return self in (Outcome.AUTHORIZED, Outcome.DECLINED)


@dataclass(frozen=True)
class AuthResult:
    outcome: Outcome
    latency_millis: int
    attempts: int


class ProcessorErrorException(RuntimeError):
    pass


class StaleRequestException(RuntimeError):
    pass


class Processor:
    """The downstream card processor. Verifies the request timestamp against ITS clock (a signing window, as
    real processors do), deduplicates by idempotency key, and declines a seeded 2% of traffic."""

    TIMESTAMP_TOLERANCE_MILLIS = 5 * 60 * 1000

    def __init__(self, clock: SimClock, seed: int) -> None:
        self._clock = clock
        self._rng = JavaRandom(seed)
        self._clock_skew_millis = 0
        self._by_idempotency_key: dict[str, Outcome] = {}
        self._authorizations_by_payment: dict[str, int] = {}

    def set_clock_skew(self, millis: int) -> None:
        self._clock_skew_millis = millis

    def local_now_millis(self) -> int:
        return self._clock.now_millis() + self._clock_skew_millis

    def handle(self, request: AuthRequest) -> Outcome:
        age = abs(self.local_now_millis() - request.sent_at_millis)
        if age > self.TIMESTAMP_TOLERANCE_MILLIS:
            raise StaleRequestException(f"request timestamp outside signing window by {age} ms")
        seen = self._by_idempotency_key.get(request.idempotency_key)
        if seen is not None:
            return seen  # replay: same answer, no second authorization
        outcome = Outcome.DECLINED if self._rng.next_int(100) < 2 else Outcome.AUTHORIZED
        self._by_idempotency_key[request.idempotency_key] = outcome
        if outcome == Outcome.AUTHORIZED:
            self._authorizations_by_payment[request.payment_id] = self._authorizations_by_payment.get(request.payment_id, 0) + 1
        return outcome

    def authorizations_for(self, payment_id: str) -> int:
        return self._authorizations_by_payment.get(payment_id, 0)

    def no_double_authorizations(self) -> bool:
        return all(n <= 1 for n in self._authorizations_by_payment.values())


class SimNetwork:
    """In-memory network between gateway and processor. Computes the round-trip latency from a seeded base
    plus any injected latency, applies partitions, errors, and clock skew, and advances the shared clock."""

    LINK_GATEWAY_TO_PROCESSOR = "gateway->processor"
    TARGET_PROCESSOR = "processor"

    def __init__(self, clock: SimClock, injector: FaultInjector, seed: int) -> None:
        self._clock = clock
        self._injector = injector
        self._rng = JavaRandom(seed)

    def call(self, request: AuthRequest, processor: Processor, timeout_millis: int) -> Outcome:
        """Returns the outcome after advancing the clock by the observed latency, or TIMEOUT after timeout_millis."""
        latency = 80 + self._rng.next_int(41)  # 80..120 ms baseline
        partitioned = False
        for f in self._injector.faults_for(self.LINK_GATEWAY_TO_PROCESSOR, request):
            match f:
                case Fault.Latency(added_millis=added):
                    latency += added
                case Fault.Partition():
                    partitioned = True
        if partitioned or latency > timeout_millis:
            self._clock.advance(timeout_millis)
            return Outcome.TIMEOUT
        self._clock.advance(latency)
        skew = 0
        error_code: Optional[str] = None
        for f in self._injector.faults_for(self.TARGET_PROCESSOR, request):
            match f:
                case Fault.ClockSkew(skew_millis=s):
                    skew = s
                case Fault.Error(code=c):
                    error_code = c
        processor.set_clock_skew(skew)
        try:
            if error_code is not None:
                raise ProcessorErrorException(error_code)
            return processor.handle(request)
        except ProcessorErrorException:
            return Outcome.PROCESSOR_ERROR
        except StaleRequestException:
            return Outcome.STALE_REQUEST
        finally:
            processor.set_clock_skew(0)


class Gateway:
    """The system under test: times out, retries once with the same idempotency key, never retries a decline."""

    TIMEOUT_MILLIS = 2_000
    MAX_ATTEMPTS = 2

    def __init__(self, clock: SimClock, network: SimNetwork, processor: Processor) -> None:
        self._clock = clock
        self._network = network
        self._processor = processor

    def authorize(self, payment_id: str, merchant_id: str, amount: Money) -> AuthResult:
        started = self._clock.now_millis()
        idempotency_key = "idem-" + payment_id
        outcome = Outcome.TIMEOUT
        attempts = 0
        while attempts < self.MAX_ATTEMPTS:
            attempts += 1
            request = AuthRequest(payment_id, merchant_id, amount, idempotency_key, self._clock.now_millis())
            outcome = self._network.call(request, self._processor, self.TIMEOUT_MILLIS)
            if outcome not in (Outcome.TIMEOUT, Outcome.PROCESSOR_ERROR):
                break
        return AuthResult(outcome, self._clock.now_millis() - started, attempts)


class TrafficGenerator:
    """Seeded traffic: payment ids, ten merchants, amounts between 1.00 and 500.00 USD."""

    def __init__(self, seed: int, prefix: str) -> None:
        self._rng = JavaRandom(seed)
        self._sequence = 0
        self._prefix = prefix

    def next_payment_id(self) -> str:
        self._sequence += 1
        return f"{self._prefix}-{self._sequence}"

    def next_merchant_id(self) -> str:
        return f"m-{self._rng.next_int(10)}"

    def next_amount(self) -> Money:
        return Money.of_minor(100 + self._rng.next_int(49_901), USD)


class Metrics:
    """Counts outcomes and keeps latency samples; p99 is computed from the sorted samples, never from an average."""

    def __init__(self) -> None:
        self._counts: dict[Outcome, int] = {}
        self._latencies: list[int] = []

    def record(self, result: AuthResult) -> None:
        self._counts[result.outcome] = self._counts.get(result.outcome, 0) + 1
        self._latencies.append(result.latency_millis)

    def total(self) -> int:
        return len(self._latencies)

    def count(self, o: Outcome) -> int:
        return self._counts.get(o, 0)

    def good_rate_percent(self) -> float:
        """Good events / all events, in percent, 100.0 when empty."""
        if not self._latencies:
            return 100.0
        good = self.count(Outcome.AUTHORIZED) + self.count(Outcome.DECLINED)
        return 100.0 * good / len(self._latencies)

    def p99_millis(self) -> int:
        if not self._latencies:
            return 0
        ordered = sorted(self._latencies)
        index = math.ceil(0.99 * len(ordered)) - 1
        return ordered[max(0, index)]

    def __str__(self) -> str:
        return (f"n={self.total()} good={self.good_rate_percent():.1f}% p99={self.p99_millis()}ms "
                f"auth={self.count(Outcome.AUTHORIZED)} decl={self.count(Outcome.DECLINED)} "
                f"timeout={self.count(Outcome.TIMEOUT)} error={self.count(Outcome.PROCESSOR_ERROR)} "
                f"stale={self.count(Outcome.STALE_REQUEST)}")


@dataclass(frozen=True)
class SteadyState:
    """What "normal" looks like, as thresholds on measurable SLIs. If you cannot write this, you are not ready to inject."""

    min_good_rate_percent: float
    max_p99_millis: int

    def holds(self, m: Metrics) -> bool:
        return m.good_rate_percent() >= self.min_good_rate_percent and m.p99_millis() <= self.max_p99_millis


@dataclass(frozen=True)
class AbortCondition:
    """The tripwire: evaluated every check_every requests on the cumulative experimental metrics."""

    min_good_rate_percent: float
    check_every: int

    def triggered(self, m: Metrics) -> bool:
        return m.good_rate_percent() < self.min_good_rate_percent


@dataclass(frozen=True)
class Experiment:
    name: str
    hypothesis: str
    target: str
    fault: Fault
    blast_radius: BlastRadius
    steady_state: SteadyState
    abort: AbortCondition
    requests: int


@dataclass(frozen=True)
class ExperimentReport:
    """Everything the GameDay needs to decide what to fix."""

    experiment: Experiment
    baseline: Metrics
    experimental: Metrics
    recovery: Metrics
    baseline_held: bool
    aborted: bool
    requests_before_abort: int
    hypothesis_held: bool
    recovered: bool
    no_double_authorizations: bool

    def verdict(self) -> str:
        if not self.baseline_held:
            return "NOT RUN (steady state not established)"
        if self.aborted:
            return f"ABORTED after {self.requests_before_abort} requests"
        return "HYPOTHESIS HELD" if self.hypothesis_held else "HYPOTHESIS VIOLATED"

    def render(self) -> str:
        e = self.experiment
        return (f"Experiment: {e.name}\n"
                f"  Hypothesis: {e.hypothesis}\n"
                f"  Fault: {e.fault} on {e.target}, blast radius {e.blast_radius}\n"
                f"  Baseline:     {self.baseline}\n"
                f"  Experimental: {self.experimental}\n"
                f"  Recovery:     {self.recovery}\n"
                f"  Invariant (no payment authorized twice): {'HELD' if self.no_double_authorizations else 'VIOLATED'}\n"
                f"  Recovered to steady state after clearing faults: {'yes' if self.recovered else 'NO'}\n"
                f"  Verdict: {self.verdict()}\n")


class ExperimentRunner:
    """Runs one experiment: baseline, inject, observe with abort checks, clear, verify recovery."""

    def __init__(self, clock: SimClock, injector: FaultInjector, gateway: Gateway, processor: Processor,
                 traffic: TrafficGenerator) -> None:
        self._clock = clock
        self._injector = injector
        self._gateway = gateway
        self._processor = processor
        self._traffic = traffic

    def _drive(self, requests: int, abort: Optional[AbortCondition]) -> tuple[Metrics, int]:
        """Returns the metrics and the request index at which the abort fired, or -1."""
        m = Metrics()
        for i in range(1, requests + 1):
            m.record(self._gateway.authorize(self._traffic.next_payment_id(), self._traffic.next_merchant_id(),
                                             self._traffic.next_amount()))
            self._clock.advance(10)  # inter-arrival gap
            if abort is not None and i % abort.check_every == 0 and abort.triggered(m):
                return m, i
        return m, -1

    def run(self, e: Experiment) -> ExperimentReport:
        self._injector.clear()
        baseline, _ = self._drive(e.requests, None)
        baseline_held = e.steady_state.holds(baseline)
        if not baseline_held:
            empty = Metrics()
            return ExperimentReport(e, baseline, empty, empty, False, False, 0, False, False,
                                    self._processor.no_double_authorizations())
        self._injector.inject(e.target, e.fault, e.blast_radius)
        experimental, stop = self._drive(e.requests, e.abort)
        aborted = stop > 0
        requests_before_abort = stop if aborted else experimental.total()
        self._injector.clear()  # the abort path and the normal path both end here
        recovery, _ = self._drive(e.requests, None)
        hypothesis_held = not aborted and e.steady_state.holds(experimental)
        return ExperimentReport(e, baseline, experimental, recovery, True, aborted, requests_before_abort,
                                hypothesis_held, e.steady_state.holds(recovery), self._processor.no_double_authorizations())


class GameDay:
    """A GameDay is an ordered list of experiments with a summary at the end."""

    def __init__(self, runner: ExperimentRunner) -> None:
        self._runner = runner
        self._reports: list[ExperimentReport] = []

    def run(self, e: Experiment) -> ExperimentReport:
        r = self._runner.run(e)
        self._reports.append(r)
        return r

    def reports(self) -> list[ExperimentReport]:
        return self._reports

    def summary(self) -> str:
        out = ["GameDay summary\n"]
        for r in self._reports:
            out.append(f"  {r.experiment.name:<44} {r.verdict()}\n")
        return "".join(out)


STEADY = SteadyState(99.0, 1_500)
ABORT = AbortCondition(80.0, 50)


def experiment(name: str, hypothesis: str, target: str, fault: Fault, radius: BlastRadius) -> Experiment:
    return Experiment(name, hypothesis, target, fault, radius, STEADY, ABORT, 500)


def main() -> None:
    clock = SimClock(1_700_000_000_000)
    injector = FaultInjector(7)
    processor = Processor(clock, 11)
    network = SimNetwork(clock, injector, 13)
    gateway = Gateway(clock, network, processor)
    traffic = TrafficGenerator(17, "pay")
    day = GameDay(ExperimentRunner(clock, injector, gateway, processor, traffic))

    print(f"Steady state: good rate >= {STEADY.min_good_rate_percent}%, p99 <= {STEADY.max_p99_millis} ms")
    print(f"Abort if good rate < {ABORT.min_good_rate_percent}% at any {ABORT.check_every}-request checkpoint\n")

    plan = [
        experiment("latency +300ms, 10% of requests",
                   "A 300 ms slower processor stays inside the 2 s timeout; p99 rises but the SLO holds",
                   SimNetwork.LINK_GATEWAY_TO_PROCESSOR, Fault.Latency(300), BlastRadius.PercentOfRequests(10)),
        experiment("latency +2500ms, 10% of requests",
                   "Requests beyond the timeout are retried once with the same idempotency key; no double authorization",
                   SimNetwork.LINK_GATEWAY_TO_PROCESSOR, Fault.Latency(2_500), BlastRadius.PercentOfRequests(10)),
        experiment("processor 500s, 70% of requests",
                   "One retry absorbs 5xx bursts; the abort tripwire fires before the whole fleet is affected",
                   SimNetwork.TARGET_PROCESSOR, Fault.Error("HTTP_500"), BlastRadius.PercentOfRequests(70)),
        experiment("partition, canary merchant m-3",
                   "A cut link fails fast for the canary merchant and leaves every other merchant inside the SLO",
                   SimNetwork.LINK_GATEWAY_TO_PROCESSOR, Fault.Partition(), BlastRadius.Merchants(frozenset({"m-3"}))),
        experiment("processor clock +6 minutes, everyone",
                   "Six minutes of skew is inside what NTP drift could produce; the processor should still accept requests",
                   SimNetwork.TARGET_PROCESSOR, Fault.ClockSkew(6 * 60 * 1000), BlastRadius.Everyone()),
    ]

    for e in plan:
        print(day.run(e).render())
    print(day.summary(), end="")
    print(f"\nFaults applied in total: {injector.applied_count()}; injector armed now: {str(injector.is_armed()).lower()}")


if __name__ == "__main__":
    main()
