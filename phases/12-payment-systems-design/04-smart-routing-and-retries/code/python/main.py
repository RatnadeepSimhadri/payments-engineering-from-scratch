"""Lesson: phases/12-payment-systems-design/04-smart-routing-and-retries/docs/en.md
Smart Routing, Cascading, and Retry Policies
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: ISO 8583 response codes (field 39); Mastercard Merchant Advice Codes; Visa excessive reattempt rules;
         Google SRE book ch. 22 (retry budgets); Nygard, "Release It!" (circuit breaker).
Python 3.11+, standard library only. Same design as the Java version; JavaRandom reproduces java.util.Random.
"""

from __future__ import annotations

from collections import deque
from dataclasses import dataclass
from enum import Enum
from typing import Callable, Optional, Protocol


# ---------------------------------------------------------------------------------------------
# Money: integer minor units + ISO 4217 currency (Phase 10, Lesson 01). Copied, not imported.
# ---------------------------------------------------------------------------------------------


@dataclass(frozen=True)
class Currency:
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
EUR = Currency("EUR", 2)


class CurrencyMismatchError(ValueError):
    def __init__(self, a: Currency, b: Currency) -> None:
        super().__init__(f"cannot combine {a.code} with {b.code}")


@dataclass(frozen=True)
class Money:
    minor_units: int
    currency: Currency

    def __post_init__(self) -> None:
        if not isinstance(self.minor_units, int) or isinstance(self.minor_units, bool):
            raise TypeError("minor_units must be an int; never construct Money from a float")

    @staticmethod
    def of_minor(minor_units: int, currency: Currency) -> "Money":
        return Money(minor_units, currency)

    @staticmethod
    def zero(currency: Currency) -> "Money":
        return Money(0, currency)

    def _require_same_currency(self, other: "Money") -> None:
        if self.currency != other.currency:
            raise CurrencyMismatchError(self.currency, other.currency)

    def plus(self, other: "Money") -> "Money":
        self._require_same_currency(other)
        return Money(self.minor_units + other.minor_units, self.currency)

    def minus(self, other: "Money") -> "Money":
        self._require_same_currency(other)
        return Money(self.minor_units - other.minor_units, self.currency)

    def is_positive(self) -> bool:
        return self.minor_units > 0

    def compare_to(self, other: "Money") -> int:
        self._require_same_currency(other)
        return (self.minor_units > other.minor_units) - (self.minor_units < other.minor_units)

    def __str__(self) -> str:
        magnitude = abs(self.minor_units)
        whole, frac = divmod(magnitude, self.currency.minor_units_per_major())
        text = str(whole)
        if self.currency.exponent > 0:
            text += "." + str(frac).rjust(self.currency.exponent, "0")
        return f"{'-' if self.minor_units < 0 else ''}{text} {self.currency.code}"

    __repr__ = __str__


# ---------------------------------------------------------------------------------------------
# Time is injected so the circuit breaker's cool-down is driven by the test, never by sleep().
# ---------------------------------------------------------------------------------------------


class Clock(Protocol):
    def now_millis(self) -> int: ...


class FakeClock:
    def __init__(self, start_millis: int) -> None:
        self._now = start_millis

    def now_millis(self) -> int:
        return self._now

    def advance(self, millis: int) -> None:
        self._now += millis


# ---------------------------------------------------------------------------------------------
# java.util.Random, bit for bit, so the seeded demo produces the same numbers as the Java lesson.
# ---------------------------------------------------------------------------------------------

_MASK48 = (1 << 48) - 1
_MULTIPLIER = 0x5DEECE66D


def _to_int32(x: int) -> int:
    x &= 0xFFFFFFFF
    return x - (1 << 32) if x >= (1 << 31) else x


class JavaRandom:
    def __init__(self, seed: int) -> None:
        self._seed = (seed ^ _MULTIPLIER) & _MASK48

    def next(self, bits: int) -> int:
        self._seed = (self._seed * _MULTIPLIER + 0xB) & _MASK48
        return _to_int32(self._seed >> (48 - bits))

    def next_int(self, bound: int) -> int:
        if bound <= 0:
            raise ValueError("bound must be positive")
        r = self.next(31)
        m = bound - 1
        if (bound & m) == 0:
            return _to_int32((bound * r) >> 31)
        u = r
        r = u % bound
        while _to_int32(u - r + m) < 0:
            u = self.next(31)
            r = u % bound
        return r

    def next_double(self) -> float:
        return ((self.next(26) << 27) + self.next(27)) * (1.0 / (1 << 53))


# ---------------------------------------------------------------------------------------------
# The request and the processors we can send it to.
# ---------------------------------------------------------------------------------------------


class CardNetwork(Enum):
    VISA = "visa"
    MASTERCARD = "mastercard"
    AMEX = "amex"

    def __str__(self) -> str:
        return self.name


@dataclass(frozen=True)
class PaymentRequest:
    id: str
    amount: Money
    network: CardNetwork
    card_fingerprint: str

    def __post_init__(self) -> None:
        if not self.amount.is_positive():
            raise ValueError(f"amount must be positive: {self.amount}")


@dataclass(frozen=True)
class ProcessorConfig:
    """A processor's static terms: what it accepts and what it charges (basis points plus a fixed fee)."""

    id: str
    networks: frozenset[CardNetwork]
    currencies: frozenset[Currency]
    fee_bps: int
    fixed_fee_minor: int

    def __post_init__(self) -> None:
        object.__setattr__(self, "networks", frozenset(self.networks))
        object.__setattr__(self, "currencies", frozenset(self.currencies))
        if self.fee_bps < 0 or self.fixed_fee_minor < 0:
            raise ValueError("fees cannot be negative")

    def accepts(self, r: PaymentRequest) -> bool:
        return r.network in self.networks and r.amount.currency in self.currencies

    def fee(self, amount: Money) -> Money:
        """Fee in minor units: amount * bps / 10000 rounded half-up, plus the fixed component. Integer arithmetic only."""
        variable = (amount.minor_units * self.fee_bps + 5_000) // 10_000
        return Money.of_minor(variable + self.fixed_fee_minor, amount.currency)


# ---------------------------------------------------------------------------------------------
# Decline classification. The response code decides whether another processor could change the
# answer. Only SOFT_CASCADE may be cascaded; everything else stops the attempt chain.
# ---------------------------------------------------------------------------------------------


class DeclineClass(Enum):
    APPROVED = "approved"                  # 00
    SOFT_CASCADE = "soft_cascade"          # the path failed, not the card: another acquirer may succeed now
    SOFT_RETRY_LATER = "soft_retry_later"  # the issuer said no for now (funds, limits): same answer anywhere today
    AUTH_REQUIRED = "auth_required"        # the issuer wants strong customer authentication: step up, do not cascade
    HARD = "hard"                          # the card or the cardholder is the problem: never retry
    AMBIGUOUS = "ambiguous"                # we do not know whether it was authorized: reconcile before anything else

    def __str__(self) -> str:
        return self.name


@dataclass(frozen=True)
class DeclineRule:
    code: str
    meaning: str
    decline: DeclineClass


CONNECT_ERROR = "CONNECT_ERROR"
TIMEOUT = "TIMEOUT"


@dataclass(frozen=True)
class ProcessorResponse:
    code: str
    merchant_advice_code: Optional[str] = None

    CONNECT_ERROR = CONNECT_ERROR
    TIMEOUT = TIMEOUT

    @staticmethod
    def approved() -> "ProcessorResponse":
        return ProcessorResponse("00", None)

    @staticmethod
    def of(code: str, mac: Optional[str] = None) -> "ProcessorResponse":
        return ProcessorResponse(code, mac)

    @staticmethod
    def connect_error() -> "ProcessorResponse":
        return ProcessorResponse(CONNECT_ERROR, None)

    @staticmethod
    def timeout() -> "ProcessorResponse":
        return ProcessorResponse(TIMEOUT, None)

    def technical(self) -> bool:
        """Technical outcomes say something about the processor's health, not about the issuer's decision."""
        return self.code in (CONNECT_ERROR, TIMEOUT)


class DeclineCodes:
    TABLE: dict[str, DeclineRule] = {}

    @staticmethod
    def _rule(code: str, meaning: str, c: DeclineClass) -> None:
        DeclineCodes.TABLE[code] = DeclineRule(code, meaning, c)

    @staticmethod
    def classify(r: ProcessorResponse) -> DeclineClass:
        """Classify a response. Two overrides come before the table: a Mastercard Merchant Advice Code of
        03 ("do not try again") or 21 ("payment cancellation") makes any decline HARD, and an unknown
        code is HARD because retrying what you do not understand is how a merchant gets fined."""
        rule = DeclineCodes.TABLE.get(r.code)
        if rule is not None and rule.decline is DeclineClass.APPROVED:
            return DeclineClass.APPROVED
        if r.merchant_advice_code in ("03", "21"):
            return DeclineClass.HARD
        return DeclineClass.HARD if rule is None else rule.decline

    @staticmethod
    def meaning(code: str) -> str:
        rule = DeclineCodes.TABLE.get(code)
        return "Unknown code" if rule is None else rule.meaning


for _code, _meaning, _class in (
    ("00", "Approved", DeclineClass.APPROVED),
    ("05", "Do not honor (generic)", DeclineClass.SOFT_CASCADE),
    ("06", "Error", DeclineClass.SOFT_CASCADE),
    ("91", "Issuer or switch inoperative", DeclineClass.SOFT_CASCADE),
    ("96", "System malfunction", DeclineClass.SOFT_CASCADE),
    (CONNECT_ERROR, "Request never reached the processor", DeclineClass.SOFT_CASCADE),
    ("51", "Insufficient funds", DeclineClass.SOFT_RETRY_LATER),
    ("61", "Exceeds withdrawal amount limit", DeclineClass.SOFT_RETRY_LATER),
    ("1A", "Additional customer authentication required", DeclineClass.AUTH_REQUIRED),
    ("65", "Exceeds frequency limit / SCA required (EEA)", DeclineClass.AUTH_REQUIRED),
    ("14", "Invalid card number", DeclineClass.HARD),
    ("41", "Lost card, pick up", DeclineClass.HARD),
    ("43", "Stolen card, pick up", DeclineClass.HARD),
    ("54", "Expired card", DeclineClass.HARD),
    ("57", "Transaction not permitted to cardholder", DeclineClass.HARD),
    ("59", "Suspected fraud", DeclineClass.HARD),
    ("62", "Restricted card", DeclineClass.HARD),
    ("N7", "CVV2 mismatch", DeclineClass.HARD),
    (TIMEOUT, "No response; authorization may exist", DeclineClass.AMBIGUOUS),
):
    DeclineCodes._rule(_code, _meaning, _class)


# ---------------------------------------------------------------------------------------------
# Authorization-rate estimates: a Beta prior plus exponentially decayed counts per (processor,
# network). Recent outcomes weigh more, so a processor that degrades is noticed within hours.
# The estimate is a probability used to rank; it is never an amount of money.
# ---------------------------------------------------------------------------------------------


@dataclass(frozen=True)
class _Counts:
    approvals: float
    attempts: float


class AuthRateEstimator:
    def __init__(self, prior_rate: float, prior_weight: float, decay: float) -> None:
        if prior_rate <= 0 or prior_rate >= 1:
            raise ValueError("prior_rate must be in (0,1)")
        if decay <= 0 or decay > 1:
            raise ValueError("decay must be in (0,1]")
        self._prior_rate = prior_rate
        self._prior_weight = prior_weight
        self._decay = decay
        self._counts: dict[str, _Counts] = {}

    @staticmethod
    def _key(processor_id: str, network: CardNetwork) -> str:
        return f"{processor_id}/{network.name}"

    def estimate(self, processor_id: str, network: CardNetwork) -> float:
        """(decayed approvals + prior pseudo-approvals) / (decayed attempts + prior weight)."""
        c = self._counts.get(self._key(processor_id, network), _Counts(0.0, 0.0))
        return (c.approvals + self._prior_rate * self._prior_weight) / (c.attempts + self._prior_weight)

    def record(self, processor_id: str, network: CardNetwork, approved: bool) -> None:
        """Record one issuer decision. Technical failures are health signals and must not be recorded here."""
        k = self._key(processor_id, network)
        c = self._counts.get(k, _Counts(0.0, 0.0))
        self._counts[k] = _Counts(c.approvals * self._decay + (1 if approved else 0), c.attempts * self._decay + 1)

    def observations(self, processor_id: str, network: CardNetwork) -> float:
        return self._counts.get(self._key(processor_id, network), _Counts(0.0, 0.0)).attempts


# ---------------------------------------------------------------------------------------------
# Processor health: a circuit breaker over the last N technical outcomes. OPEN removes the
# processor from routing until the cool-down passes; HALF_OPEN lets one request probe it.
# ---------------------------------------------------------------------------------------------


class CircuitState(Enum):
    CLOSED = "closed"
    OPEN = "open"
    HALF_OPEN = "half_open"

    def __str__(self) -> str:
        return self.name


class ProcessorHealth:
    def __init__(self, clock: Clock, window_size: int, failure_threshold: int, cool_down_millis: int) -> None:
        self._clock = clock
        self._window_size = window_size
        self._failure_threshold = failure_threshold
        self._cool_down_millis = cool_down_millis
        self._window: deque[bool] = deque()
        self._state = CircuitState.CLOSED
        self._opened_at_millis = 0

    def state(self) -> CircuitState:
        if self._state is CircuitState.OPEN and self._clock.now_millis() - self._opened_at_millis >= self._cool_down_millis:
            self._state = CircuitState.HALF_OPEN
        return self._state

    def allows(self) -> bool:
        return self.state() is not CircuitState.OPEN

    def record_success(self) -> None:
        if self.state() is CircuitState.HALF_OPEN:
            self._state = CircuitState.CLOSED
            self._window.clear()
        self._push(False)

    def record_failure(self) -> None:
        if self.state() is CircuitState.HALF_OPEN:
            self._trip()
            return
        self._push(True)
        if sum(1 for b in self._window if b) >= self._failure_threshold:
            self._trip()

    def _trip(self) -> None:
        self._state = CircuitState.OPEN
        self._opened_at_millis = self._clock.now_millis()
        self._window.clear()

    def _push(self, failure: bool) -> None:
        self._window.append(failure)
        while len(self._window) > self._window_size:
            self._window.popleft()


# ---------------------------------------------------------------------------------------------
# Retry budget: cascades are paid for by first attempts. Each first attempt deposits a fraction
# of a token; each cascade spends a whole one. When an outage turns every attempt into a soft
# decline, the budget runs dry and the router stops multiplying load on a struggling network.
# ---------------------------------------------------------------------------------------------


class RetryBudget:
    TOKEN = 1_000  # milli-tokens per retry

    def __init__(self, deposit_per_attempt: int, capacity: int, initial: int) -> None:
        self._deposit_per_attempt = deposit_per_attempt  # milli-tokens earned per first attempt (100 = 10% retry ratio)
        self._capacity = capacity
        self._balance = min(initial, capacity)

    def record_first_attempt(self) -> None:
        self._balance = min(self._capacity, self._balance + self._deposit_per_attempt)

    def try_acquire_retry(self) -> bool:
        if self._balance < RetryBudget.TOKEN:
            return False
        self._balance -= RetryBudget.TOKEN
        return True

    def balance_milli(self) -> int:
        return self._balance


# ---------------------------------------------------------------------------------------------
# The gateway is the network boundary. Scripted for tests, simulated with a seeded Random for the
# learning demo. Nothing here sleeps or opens a socket.
# ---------------------------------------------------------------------------------------------


class ProcessorGateway(Protocol):
    def authorize(self, processor_id: str, request: PaymentRequest) -> ProcessorResponse: ...


class ScriptedGateway:
    def __init__(self) -> None:
        self._scripts: dict[str, deque[ProcessorResponse]] = {}
        self._calls: list[str] = []

    def script(self, processor_id: str, *responses: ProcessorResponse) -> "ScriptedGateway":
        self._scripts.setdefault(processor_id, deque()).extend(responses)
        return self

    def calls(self) -> tuple[str, ...]:
        return tuple(self._calls)

    def authorize(self, processor_id: str, request: PaymentRequest) -> ProcessorResponse:
        self._calls.append(f"{processor_id}:{request.id}")
        q = self._scripts.get(processor_id)
        return q.popleft() if q else ProcessorResponse.approved()


class SimulatedGateway:
    """True approval probability per (processor, network), decided by a seeded Random."""

    def __init__(self, seed: int) -> None:
        self._random = JavaRandom(seed)
        self._true_rates: dict[str, float] = {}

    def rate(self, processor_id: str, network: CardNetwork, approval_probability: float) -> "SimulatedGateway":
        self._true_rates[f"{processor_id}/{network.name}"] = approval_probability
        return self

    def authorize(self, processor_id: str, request: PaymentRequest) -> ProcessorResponse:
        p = self._true_rates.get(f"{processor_id}/{request.network.name}", 0.9)
        roll = self._random.next_double()
        if roll < p:
            return ProcessorResponse.approved()
        # Of the declines, a third are path problems (05/91) another acquirer might clear; the rest are issuer decisions.
        decline_roll = self._random.next_double()
        if decline_roll < 0.33:
            return ProcessorResponse.of("05" if decline_roll < 0.2 else "91")
        return ProcessorResponse.of("51" if decline_roll < 0.8 else "54")


# ---------------------------------------------------------------------------------------------
# The router.
# ---------------------------------------------------------------------------------------------


@dataclass(frozen=True)
class Candidate:
    """One ranked option: the inputs to the score are kept so the decision can be logged and explained."""

    processor: ProcessorConfig
    auth_rate: float
    fee: Money
    expected_margin_minor: float

    def __str__(self) -> str:
        return (f"{self.processor.id:<6} auth {self.auth_rate:.3f}  fee {str(self.fee):<10} "
                f"expected margin {self.expected_margin_minor:8.2f} minor")


@dataclass(frozen=True)
class Attempt:
    number: int
    processor_id: str
    code: str
    decline: DeclineClass
    at_millis: int

    def __str__(self) -> str:
        return f"#{self.number} {self.processor_id:<6} {self.code:<13} {self.decline.name:<16} {DeclineCodes.meaning(self.code)}"


class RouteOutcome(Enum):
    APPROVED = "approved"
    DECLINED = "declined"
    PENDING_UNKNOWN = "pending_unknown"
    NO_ROUTE = "no_route"

    def __str__(self) -> str:
        return self.name


@dataclass(frozen=True)
class RouteResult:
    payment_id: str
    outcome: RouteOutcome
    attempts: tuple[Attempt, ...]
    stop_reason: str

    def approved_by(self) -> Optional[str]:
        return self.attempts[-1].processor_id if self.outcome is RouteOutcome.APPROVED else None


@dataclass(frozen=True)
class RoutingPolicy:
    max_attempts: int
    exploration_rate: float

    def __post_init__(self) -> None:
        if self.max_attempts < 1:
            raise ValueError("max_attempts must be >= 1")
        if self.exploration_rate < 0 or self.exploration_rate >= 1:
            raise ValueError("exploration_rate must be in [0,1)")


HealthFactory = Callable[[str, Clock], ProcessorHealth]


class Router:
    def __init__(self, configs: list[ProcessorConfig], health_factory: HealthFactory, estimator: AuthRateEstimator,
                 budget: RetryBudget, gateway: ProcessorGateway, clock: Clock, policy: RoutingPolicy,
                 random: JavaRandom) -> None:
        self._processors: dict[str, ProcessorConfig] = {}
        self._health: dict[str, ProcessorHealth] = {}
        for c in configs:
            self._processors[c.id] = c
            self._health[c.id] = health_factory(c.id, clock)
        self._estimator = estimator
        self._budget = budget
        self._gateway = gateway
        self._clock = clock
        self._policy = policy
        self._random = random

    def health(self, processor_id: str) -> ProcessorHealth:
        return self._health[processor_id]

    def estimator(self) -> AuthRateEstimator:
        return self._estimator

    def budget(self) -> RetryBudget:
        return self._budget

    def rank(self, request: PaymentRequest) -> list[Candidate]:
        """Eligible processors, best first. Eligible: accepts the network and currency, and its circuit is
        not OPEN. Score: P(approve) * (amount - fee), the expected margin of sending the attempt there.
        Ties break on processor id so the order is deterministic."""
        out: list[Candidate] = []
        for p in self._processors.values():
            if not p.accepts(request) or not self._health[p.id].allows():
                continue
            rate = self._estimator.estimate(p.id, request.network)
            fee = p.fee(request.amount)
            margin = rate * request.amount.minus(fee).minor_units
            out.append(Candidate(p, rate, fee, margin))
        out.sort(key=lambda c: (-c.expected_margin_minor, c.processor.id))
        return out

    def route(self, request: PaymentRequest) -> RouteResult:
        """Try the ranked processors in order. Stop on approval, on any decline that is not SOFT_CASCADE,
        on the per-payment attempt cap, or when the shared retry budget cannot pay for a cascade."""
        ranked = list(self.rank(request))
        if not ranked:
            return RouteResult(request.id, RouteOutcome.NO_ROUTE, (), "no eligible processor")
        if len(ranked) > 1 and self._policy.exploration_rate > 0 and self._random.next_double() < self._policy.exploration_rate:
            j = 1 + self._random.next_int(len(ranked) - 1)  # explore: lead with a non-best candidate
            ranked[0], ranked[j] = ranked[j], ranked[0]
        attempts: list[Attempt] = []
        self._budget.record_first_attempt()
        for c in ranked:
            if len(attempts) >= self._policy.max_attempts:
                return RouteResult(request.id, RouteOutcome.DECLINED, tuple(attempts),
                                   f"attempt cap {self._policy.max_attempts} reached")
            if attempts and not self._budget.try_acquire_retry():
                return RouteResult(request.id, RouteOutcome.DECLINED, tuple(attempts), "retry budget exhausted")
            pid = c.processor.id
            if not self._health[pid].allows():
                continue  # tripped by an earlier attempt of this same payment
            response = self._gateway.authorize(pid, request)
            decline = DeclineCodes.classify(response)
            attempts.append(Attempt(len(attempts) + 1, pid, response.code, decline, self._clock.now_millis()))
            self._learn(pid, request, response, decline)
            if decline is DeclineClass.APPROVED:
                return RouteResult(request.id, RouteOutcome.APPROVED, tuple(attempts), "approved")
            if decline is DeclineClass.SOFT_CASCADE:
                continue  # the only class that may try the next processor
            if decline is DeclineClass.SOFT_RETRY_LATER:
                return RouteResult(request.id, RouteOutcome.DECLINED, tuple(attempts), "issuer decline: retry later, not elsewhere")
            if decline is DeclineClass.AUTH_REQUIRED:
                return RouteResult(request.id, RouteOutcome.DECLINED, tuple(attempts), "step up with 3-D Secure, then retry")
            if decline is DeclineClass.HARD:
                return RouteResult(request.id, RouteOutcome.DECLINED, tuple(attempts), "hard decline: do not retry this card")
            return RouteResult(request.id, RouteOutcome.PENDING_UNKNOWN, tuple(attempts),
                               f"timeout: query or reverse at {pid} before any cascade")
        return RouteResult(request.id, RouteOutcome.DECLINED, tuple(attempts), "all eligible processors tried")

    def _learn(self, pid: str, request: PaymentRequest, response: ProcessorResponse, decline: DeclineClass) -> None:
        """Health learns from technical outcomes; the auth-rate estimate learns only from issuer decisions."""
        h = self._health[pid]
        if response.technical():
            h.record_failure()
            return
        h.record_success()
        self._estimator.record(pid, request.network, decline is DeclineClass.APPROVED)


# ---------------------------------------------------------------------------------------------
# Demo
# ---------------------------------------------------------------------------------------------

USD_EUR = frozenset({USD, EUR})


def processors() -> list[ProcessorConfig]:
    return [
        ProcessorConfig("acq_a", frozenset({CardNetwork.VISA, CardNetwork.MASTERCARD}), USD_EUR, 220, 20),
        ProcessorConfig("acq_b", frozenset({CardNetwork.VISA, CardNetwork.MASTERCARD}), USD_EUR, 290, 30),
        ProcessorConfig("acq_c", frozenset({CardNetwork.AMEX}), frozenset({USD}), 350, 0),
    ]


def router(gateway: ProcessorGateway, clock: FakeClock, est: AuthRateEstimator, budget: RetryBudget, explore: float) -> Router:
    return Router(processors(), lambda _id, c: ProcessorHealth(c, 10, 5, 30_000), est, budget, gateway, clock,
                  RoutingPolicy(3, explore), JavaRandom(7))


def print_result(r: RouteResult) -> None:
    print(f"{r.payment_id} -> {r.outcome.name} ({r.stop_reason})")
    for a in r.attempts:
        print(f"   {a}")


def main() -> None:
    clock = FakeClock(1_700_000_000_000)

    print("== Decline classification table ==")
    for r in DeclineCodes.TABLE.values():
        print(f"{r.code:<13} {r.decline.name:<17} {r.meaning}")

    print("\n== Ranking a 100.00 USD Visa payment (expected margin = P(approve) x (amount - fee)) ==")
    est = AuthRateEstimator(0.85, 20, 0.995)
    for i in range(100):
        est.record("acq_a", CardNetwork.VISA, i % 100 < 86)
    for i in range(100):
        est.record("acq_b", CardNetwork.VISA, i % 100 < 93)
    gw = ScriptedGateway()
    r = router(gw, clock, est, RetryBudget(100, 10_000, 3_000), 0)
    p100 = PaymentRequest("pay_100", Money.of_minor(10_000, USD), CardNetwork.VISA, "fp_1")
    for c in r.rank(p100):
        print(c)

    print("\n== Cascading: only SOFT_CASCADE moves to the next processor ==")
    gw.script("acq_b", ProcessorResponse.of("91")).script("acq_a", ProcessorResponse.approved())
    print_result(r.route(p100))
    gw.script("acq_b", ProcessorResponse.of("43"))
    print_result(r.route(PaymentRequest("pay_stolen", Money.of_minor(10_000, USD), CardNetwork.VISA, "fp_2")))
    gw.script("acq_b", ProcessorResponse.of("51"))
    print_result(r.route(PaymentRequest("pay_nsf", Money.of_minor(10_000, USD), CardNetwork.VISA, "fp_3")))
    gw.script("acq_b", ProcessorResponse.of("05", "03"))
    print_result(r.route(PaymentRequest("pay_mac03", Money.of_minor(10_000, USD), CardNetwork.VISA, "fp_4")))
    gw.script("acq_b", ProcessorResponse.timeout())
    print_result(r.route(PaymentRequest("pay_timeout", Money.of_minor(10_000, USD), CardNetwork.VISA, "fp_5")))
    gw.script("acq_b", ProcessorResponse.of("96")).script("acq_a", ProcessorResponse.of("05"))
    print_result(r.route(PaymentRequest("pay_exhaust", Money.of_minor(10_000, USD), CardNetwork.VISA, "fp_6")))
    print_result(r.route(PaymentRequest("pay_amex", Money.of_minor(10_000, EUR), CardNetwork.AMEX, "fp_7")))

    print("\n== Circuit breaker: 5 technical failures in the last 10 calls take acq_b out of the ranking ==")
    flaky = ScriptedGateway()
    cb = router(flaky, clock, est, RetryBudget(100, 10_000, 10_000), 0)
    sent = 0
    while cb.health("acq_b").allows():
        flaky.script("acq_b", ProcessorResponse.connect_error())
        res = cb.route(PaymentRequest(f"pay_ce{sent}", Money.of_minor(5_000, USD), CardNetwork.VISA, "fp_ce"))
        print(f"pay_ce{sent} -> {res.outcome.name} via {res.approved_by()} after {len(res.attempts)} attempts")
        sent += 1
    ranking = "[" + ", ".join(c.processor.id for c in cb.rank(p100)) + "]"
    print(f"acq_b circuit: {cb.health('acq_b').state().name}; ranking now {ranking}")
    clock.advance(30_000)
    print(f"after 30s cool-down: {cb.health('acq_b').state().name}; the next payment probes it")
    probe = cb.route(PaymentRequest("pay_probe", Money.of_minor(5_000, USD), CardNetwork.VISA, "fp_p"))
    print(f"probe approved by {probe.approved_by()}; acq_b circuit: {cb.health('acq_b').state().name}")

    print("\n== Retry budget: an issuer outage makes every first attempt a 91 ==")
    outage = ScriptedGateway()
    storm = router(outage, clock, AuthRateEstimator(0.85, 20, 0.995), RetryBudget(100, 10_000, 3_000), 0)
    cascades = 0
    attempts = 0
    for i in range(20):
        outage.script("acq_a", ProcessorResponse.of("91")).script("acq_b", ProcessorResponse.of("91"))
        res = storm.route(PaymentRequest(f"pay_s{i}", Money.of_minor(2_000, USD), CardNetwork.VISA, f"fp_s{i}"))
        attempts += len(res.attempts)
        cascades += len(res.attempts) - 1
    print(f"20 payments -> {attempts} processor calls, {cascades} cascades; budget left "
          f"{storm.budget().balance_milli()} milli-tokens (without a budget: 40 calls)")

    print("\n== Learning: acq_a's true Visa approval rate is 0.80, acq_b's is 0.93; both start at the 0.85 prior ==")
    learn = AuthRateEstimator(0.85, 20, 0.995)
    sim = SimulatedGateway(42).rate("acq_a", CardNetwork.VISA, 0.80).rate("acq_b", CardNetwork.VISA, 0.93)
    lr = router(sim, clock, learn, RetryBudget(100, 10_000, 3_000), 0.05)
    first_choice: dict[str, int] = {}
    approved = 0
    for i in range(2_000):
        res = lr.route(PaymentRequest(f"pay_l{i}", Money.of_minor(10_000, USD), CardNetwork.VISA, f"fp_l{i}"))
        first_choice[res.attempts[0].processor_id] = first_choice.get(res.attempts[0].processor_id, 0) + 1
        if res.outcome is RouteOutcome.APPROVED:
            approved += 1
    print(f"estimates after 2000 payments: acq_a {learn.estimate('acq_a', CardNetwork.VISA):.3f}, "
          f"acq_b {learn.estimate('acq_b', CardNetwork.VISA):.3f}")
    choices = "{" + ", ".join(f"{k}={v}" for k, v in first_choice.items()) + "}"
    print(f"first attempts: {choices}; approved {approved}/2000")


if __name__ == "__main__":
    main()
