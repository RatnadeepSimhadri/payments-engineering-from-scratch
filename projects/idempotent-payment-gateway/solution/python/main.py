"""Project: projects/idempotent-payment-gateway — reference solution (Python), all four stages in one module.
Stage 01: Fingerprint, ChargeRequest, ApiResponse, IdempotencyStore (NEW / REPLAY / IN_PROGRESS / CONFLICT, TTL).
Stage 02: PaymentState, PaymentStateMachine, Payment (versioned history, partial refunds).
Stage 03: ScriptedProcessor, BackoffPolicy (full jitter from the seeded Rng), RetryBudget, CircuitBreaker,
          RetryingClient, Gateway (201 / 402 / 409 / 422 / 503).
Stage 04: WebhookEvent, WebhookSigner, WebhookVerifier, WebhookConsumer, WebhookDispatcher (at-least-once, dead letters).
Run:  python3 main.py
Grade: python3 scripts/project_test.py idempotent-payment-gateway --lang python --all --solution
Sources: FIPS 180-4 (SHA-256), RFC 2104 (HMAC), Vigna 2016 (xorshift64*), "Exponential Backoff and Jitter" (AWS, 2015).
Python 3.11+, standard library only (hashlib, hmac, threading). Same design and names as the Java solution.
"""

from __future__ import annotations

import hashlib
import hmac
import threading
import time
from dataclasses import dataclass, field
from enum import Enum
from functools import total_ordering
from typing import Protocol, Sequence

# ---------------------------------------------------------------- Money (copied from phases/10/01, same as the Java file)

LONG_MIN = -(2**63)
LONG_MAX = 2**63 - 1


def _check_long(value: int) -> int:
    """Python ints never overflow; keep the Java/Go 64-bit bound so a huge amount is a loud error here too."""
    if not LONG_MIN <= value <= LONG_MAX:
        raise OverflowError(f"amount overflows 64-bit minor units: {value}")
    return value


@dataclass(frozen=True)
class Currency:
    """ISO 4217 currency: a three-letter code and the number of decimals in its minor unit."""

    code: str
    exponent: int

    def __post_init__(self) -> None:
        if not isinstance(self.code, str) or len(self.code) != 3 or not self.code.isalpha() or not self.code.isupper():
            raise ValueError(f"currency code must be three uppercase letters: {self.code!r}")
        if not 0 <= self.exponent <= 4:
            raise ValueError(f"exponent must be 0..4: {self.exponent}")

    def minor_units_per_major(self) -> int:
        return 10**self.exponent


USD = Currency("USD", 2)
EUR = Currency("EUR", 2)
GBP = Currency("GBP", 2)
INR = Currency("INR", 2)
JPY = Currency("JPY", 0)
KWD = Currency("KWD", 3)


class Rounding(Enum):
    """How to resolve a fractional minor unit."""

    HALF_EVEN = "half_even"
    HALF_UP = "half_up"
    DOWN = "down"
    UP = "up"


class CurrencyMismatchError(ValueError):
    def __init__(self, a: Currency, b: Currency) -> None:
        super().__init__(f"cannot combine {a.code} with {b.code}")


def divide_rounded(dividend: int, divisor: int, rounding: Rounding) -> int:
    """Integer division with an explicit rounding rule (truncation semantics of Java's / and %)."""
    if divisor == 0:
        raise ZeroDivisionError("divisor must not be zero")
    sign = -1 if (dividend < 0) != (divisor < 0) else 1
    quotient, remainder = divmod(abs(dividend), abs(divisor))
    if remainder == 0:
        return sign * quotient
    twice = remainder * 2
    abs_divisor = abs(divisor)
    if rounding is Rounding.UP:
        quotient += 1
    elif rounding is Rounding.HALF_UP:
        if twice >= abs_divisor:
            quotient += 1
    elif rounding is Rounding.HALF_EVEN:
        if twice > abs_divisor or (twice == abs_divisor and quotient % 2 == 1):
            quotient += 1
    return sign * quotient


@total_ordering
@dataclass(frozen=True)
class Money:
    """An exact amount of one currency, stored as a signed count of minor units. Immutable."""

    minor_units: int
    currency: Currency

    def __post_init__(self) -> None:
        if not isinstance(self.minor_units, int) or isinstance(self.minor_units, bool):
            raise TypeError("minor_units must be an int; never construct Money from a float")
        if not isinstance(self.currency, Currency):
            raise TypeError("currency must be a Currency")
        _check_long(self.minor_units)

    @staticmethod
    def of_minor(minor_units: int, currency: Currency) -> Money:
        return Money(minor_units, currency)

    @staticmethod
    def parse(text: str, currency: Currency) -> Money:
        s = text.strip()
        negative = s.startswith("-")
        if negative or s.startswith("+"):
            s = s[1:]
        if not s:
            raise ValueError("empty amount")
        whole, _, frac = s.partition(".")
        whole = whole or "0"
        if not whole.isdigit() or (frac and not frac.isdigit()) or not whole.isascii() or not frac.isascii():
            raise ValueError(f"not a decimal amount: {text!r}")
        if len(frac) > currency.exponent:
            raise ArithmeticError(f"{text} has more decimals than {currency.code} allows ({currency.exponent})")
        frac = frac.ljust(currency.exponent, "0")
        minor = _check_long(int(whole) * currency.minor_units_per_major()) + (int(frac) if frac else 0)
        return Money(-minor if negative else minor, currency)

    def _require_same_currency(self, other: Money) -> None:
        if self.currency != other.currency:
            raise CurrencyMismatchError(self.currency, other.currency)

    def plus(self, other: Money) -> Money:
        self._require_same_currency(other)
        return Money(self.minor_units + other.minor_units, self.currency)

    def minus(self, other: Money) -> Money:
        self._require_same_currency(other)
        return Money(self.minor_units - other.minor_units, self.currency)

    def negate(self) -> Money:
        return Money(-self.minor_units, self.currency)

    def abs(self) -> Money:
        return self.negate() if self.minor_units < 0 else self

    def is_zero(self) -> bool:
        return self.minor_units == 0

    def is_negative(self) -> bool:
        return self.minor_units < 0

    def is_positive(self) -> bool:
        return self.minor_units > 0

    def times(self, numerator: int, denominator: int, rounding: Rounding) -> Money:
        if denominator <= 0:
            raise ArithmeticError("denominator must be positive")
        return Money(divide_rounded(_check_long(self.minor_units * numerator), denominator, rounding), self.currency)

    @staticmethod
    def divide_rounded(dividend: int, divisor: int, rounding: Rounding) -> int:
        return divide_rounded(dividend, divisor, rounding)

    def allocate(self, *ratios: int) -> list[Money]:
        if not ratios:
            raise ValueError("need at least one ratio")
        if any(r < 0 for r in ratios):
            raise ValueError("ratios must be non-negative")
        total = sum(ratios)
        if total == 0:
            raise ValueError("ratios must not all be zero")
        magnitude = abs(self.minor_units)
        shares = [magnitude * r // total for r in ratios]
        remainders = [magnitude * r % total for r in ratios]
        leftover = magnitude - sum(shares)
        order = sorted(range(len(ratios)), key=lambda i: -remainders[i])
        for i in order[:leftover]:
            shares[i] += 1
        sign = -1 if self.minor_units < 0 else 1
        return [Money(sign * s, self.currency) for s in shares]

    @staticmethod
    def sum(currency: Currency, *amounts: Money) -> Money:
        acc = Money(0, currency)
        for m in amounts:
            acc = acc.plus(m)
        return acc

    def __lt__(self, other: Money) -> bool:
        self._require_same_currency(other)
        return self.minor_units < other.minor_units

    def __str__(self) -> str:
        magnitude = abs(self.minor_units)
        whole, frac = divmod(magnitude, self.currency.minor_units_per_major())
        text = str(whole)
        if self.currency.exponent > 0:
            text += "." + str(frac).rjust(self.currency.exponent, "0")
        return f"{'-' if self.minor_units < 0 else ''}{text} {self.currency.code}"

    __repr__ = __str__


# ---------------------------------------------------------------- injected time


class Clock(Protocol):
    """A time source the gateway reads and waits against."""

    def now_millis(self) -> int: ...

    def sleep(self, millis: int) -> None: ...


class FakeClock:
    """Deterministic clock for tests and demos: time moves only when you move it."""

    def __init__(self, start_millis: int) -> None:
        self._now = start_millis

    def now_millis(self) -> int:
        return self._now

    def sleep(self, millis: int) -> None:
        self.advance(millis)

    def advance(self, millis: int) -> None:
        if millis < 0:
            raise ValueError(f"cannot advance a negative duration: {millis}")
        self._now += millis

    def set(self, millis: int) -> None:
        self._now = millis


class SystemClock:
    """Wall-clock implementation for a real deployment. Never used by the grader."""

    def now_millis(self) -> int:
        return int(time.time() * 1000)

    def sleep(self, millis: int) -> None:
        time.sleep(millis / 1000)


# ---------------------------------------------------------------- seeded randomness

_MASK64 = (1 << 64) - 1


class Rng:
    """xorshift64* (Vigna, 2016). The same algorithm and seed produce the same sequence in Java, Python, and Go,
    so a retry schedule computed in one language is the schedule in the others. Not for anything security-sensitive."""

    def __init__(self, seed: int) -> None:
        seed &= _MASK64
        self._state = 0x9E3779B97F4A7C15 if seed == 0 else seed

    def next_long(self) -> int:
        """Next 64-bit output as an unsigned int in [0, 2**64); Java returns the same bits as a signed long."""
        x = self._state
        x ^= x >> 12
        x ^= (x << 25) & _MASK64
        x ^= x >> 27
        self._state = x
        return (x * 0x2545F4914F6CDD1D) & _MASK64

    def next_below(self, bound: int) -> int:
        """Uniform integer in [0, bound), using the unsigned interpretation of next_long()."""
        if bound <= 0:
            raise ValueError(f"bound must be positive: {bound}")
        return self.next_long() % bound


# ---------------------------------------------------------------- stage 01: idempotency store


class Fingerprint:
    """SHA-256 over a canonical string, rendered as 64 lowercase hex characters."""

    @staticmethod
    def sha256_hex(canonical: str) -> str:
        return hashlib.sha256(canonical.encode("utf-8")).hexdigest()


def _require_text(value: object, field_name: str) -> str:
    if not isinstance(value, str) or not value.strip():
        raise ValueError(f"{field_name} is required")
    return value


@dataclass(frozen=True)
class ChargeRequest:
    """A merchant's request to charge a customer. The idempotency key identifies the attempt; the canonical
    form (amount, currency, source, description joined by newlines) identifies the intent."""

    merchant_id: str
    idempotency_key: str
    amount: Money
    source: str
    description: str

    def __post_init__(self) -> None:
        _require_text(self.merchant_id, "merchant_id")
        _require_text(self.idempotency_key, "idempotency_key")
        _require_text(self.source, "source")
        if not isinstance(self.amount, Money):
            raise TypeError("amount must be a Money")
        if not isinstance(self.description, str):
            raise TypeError("description must be a str")
        if "\n" in self.source or "\n" in self.description:
            raise ValueError("source and description must not contain newlines")
        if not self.amount.is_positive():
            raise ValueError(f"amount must be positive: {self.amount}")

    def canonical(self) -> str:
        """"1999\\nUSD\\ntok_visa\\norder-42": the fields that define the intent, in a fixed order. The key is not part of it."""
        return f"{self.amount.minor_units}\n{self.amount.currency.code}\n{self.source}\n{self.description}"

    def fingerprint(self) -> str:
        return Fingerprint.sha256_hex(self.canonical())


@dataclass(frozen=True)
class ApiResponse:
    """What the gateway returned to the merchant; the store keeps it so a replay returns the same bytes."""

    status: int
    body: str
    replayed: bool = False

    def as_replay(self) -> ApiResponse:
        return ApiResponse(self.status, self.body, True)


class KeyState(Enum):
    IN_PROGRESS = "in_progress"
    COMPLETED = "completed"


class Decision(Enum):
    NEW = "new"
    REPLAY = "replay"
    IN_PROGRESS = "in_progress"
    CONFLICT = "conflict"


@dataclass(frozen=True)
class BeginResult:
    """Outcome of begin(): the response is present only for REPLAY."""

    decision: Decision
    response: ApiResponse | None = None

    @staticmethod
    def of(decision: Decision) -> BeginResult:
        return BeginResult(decision, None)


@dataclass(frozen=True)
class IdempotencyRecord:
    merchant_id: str
    key: str
    fingerprint: str
    state: KeyState
    response: ApiResponse | None
    created_at: int
    expires_at: int

    def is_expired(self, now: int) -> bool:
        return now >= self.expires_at


class IdempotencyStore:
    """In-memory idempotency store keyed by (merchant_id, key). Every public method holds one lock so that
    concurrent begin() calls with the same key admit exactly one caller; the rest see IN_PROGRESS."""

    def __init__(self, clock: Clock, ttl_millis: int) -> None:
        if clock is None:
            raise TypeError("clock is required")
        if ttl_millis <= 0:
            raise ValueError(f"ttl_millis must be positive: {ttl_millis}")
        self._clock = clock
        self._ttl_millis = ttl_millis
        self._records: dict[tuple[str, str], IdempotencyRecord] = {}
        self._lock = threading.Lock()

    def ttl_millis(self) -> int:
        return self._ttl_millis

    def begin(self, merchant_id: str, key: str, fingerprint: str) -> BeginResult:
        """Claim the key for this request or report why the caller must not proceed."""
        if fingerprint is None:
            raise TypeError("fingerprint is required")
        store_key = (merchant_id, key)
        with self._lock:
            now = self._clock.now_millis()
            existing = self._records.get(store_key)
            if existing is not None and existing.is_expired(now):
                del self._records[store_key]
                existing = None
            if existing is None:
                self._records[store_key] = IdempotencyRecord(merchant_id, key, fingerprint, KeyState.IN_PROGRESS, None, now, now + self._ttl_millis)
                return BeginResult.of(Decision.NEW)
            if existing.fingerprint != fingerprint:
                return BeginResult.of(Decision.CONFLICT)
            if existing.state is KeyState.IN_PROGRESS:
                return BeginResult.of(Decision.IN_PROGRESS)
            return BeginResult(Decision.REPLAY, existing.response)

    def complete(self, merchant_id: str, key: str, response: ApiResponse) -> None:
        """Store the response for an IN_PROGRESS key. Completing twice, or a key never begun, is a bug."""
        if response is None:
            raise TypeError("response is required")
        store_key = (merchant_id, key)
        with self._lock:
            existing = self._records.get(store_key)
            if existing is None or existing.is_expired(self._clock.now_millis()):
                raise RuntimeError(f"no in-progress record for {merchant_id}/{key}")
            if existing.state is KeyState.COMPLETED:
                raise RuntimeError(f"record already completed for {merchant_id}/{key}")
            self._records[store_key] = IdempotencyRecord(merchant_id, key, existing.fingerprint, KeyState.COMPLETED,
                                                         ApiResponse(response.status, response.body), existing.created_at, existing.expires_at)

    def release(self, merchant_id: str, key: str) -> bool:
        """Give the key back after a failure that must not be replayed (timeout, circuit open). Completed keys are kept."""
        store_key = (merchant_id, key)
        with self._lock:
            existing = self._records.get(store_key)
            if existing is None:
                return False
            if existing.state is KeyState.COMPLETED:
                raise RuntimeError(f"cannot release a completed record for {merchant_id}/{key}")
            del self._records[store_key]
            return True

    def get(self, merchant_id: str, key: str) -> IdempotencyRecord | None:
        """The live record, if any; expired records read as absent (None)."""
        with self._lock:
            existing = self._records.get((merchant_id, key))
            if existing is None or existing.is_expired(self._clock.now_millis()):
                return None
            return existing

    def purge_expired(self) -> int:
        """Drop every expired record; returns how many were removed."""
        with self._lock:
            now = self._clock.now_millis()
            stale = [k for k, r in self._records.items() if r.is_expired(now)]
            for k in stale:
                del self._records[k]
            return len(stale)

    def size(self) -> int:
        """Records held, expired or not; call purge_expired() first for the live count."""
        with self._lock:
            return len(self._records)


# ---------------------------------------------------------------- stage 02: payment state machine


class PaymentState(Enum):
    CREATED = "created"
    PROCESSING = "processing"
    SUCCEEDED = "succeeded"
    FAILED = "failed"
    CANCELED = "canceled"
    REFUNDED = "refunded"

    def is_terminal(self) -> bool:
        """Terminal states accept no further transition (a fully refunded payment is done)."""
        return self in (PaymentState.FAILED, PaymentState.CANCELED, PaymentState.REFUNDED)

    def wire(self) -> str:
        """Lower-case name used in API bodies and webhook payloads: "succeeded"."""
        return self.name.lower()


class IllegalTransitionError(RuntimeError):
    def __init__(self, payment_id: str, from_state: PaymentState, to_state: PaymentState) -> None:
        super().__init__(f"{payment_id}: illegal transition {from_state.name} -> {to_state.name}")


@dataclass(frozen=True)
class Transition:
    """One entry of the transition log. Partial refunds are logged as SUCCEEDED -> SUCCEEDED with the amount in the reason."""

    from_state: PaymentState
    to_state: PaymentState
    reason: str
    at_millis: int


class PaymentStateMachine:
    """The single source of truth for which moves are legal."""

    TABLE: dict[PaymentState, frozenset[PaymentState]] = {
        PaymentState.CREATED: frozenset({PaymentState.PROCESSING, PaymentState.CANCELED}),
        PaymentState.PROCESSING: frozenset({PaymentState.SUCCEEDED, PaymentState.FAILED}),
        PaymentState.SUCCEEDED: frozenset({PaymentState.REFUNDED}),
        PaymentState.FAILED: frozenset(),
        PaymentState.CANCELED: frozenset(),
        PaymentState.REFUNDED: frozenset(),
    }

    @staticmethod
    def can_transition(from_state: PaymentState, to_state: PaymentState) -> bool:
        return to_state in PaymentStateMachine.TABLE[from_state]

    @staticmethod
    def next_states(from_state: PaymentState) -> frozenset[PaymentState]:
        return PaymentStateMachine.TABLE[from_state]


class Payment:
    """A single payment attempt and its money: what was captured and how much of it has been given back."""

    def __init__(self, id: str, amount: Money, clock: Clock) -> None:
        _require_text(id, "id")
        if not isinstance(amount, Money):
            raise TypeError("amount must be a Money")
        if clock is None:
            raise TypeError("clock is required")
        if not amount.is_positive():
            raise ValueError(f"amount must be positive: {amount}")
        self._id = id
        self._amount = amount
        self._clock = clock
        self._state = PaymentState.CREATED
        self._captured = Money.of_minor(0, amount.currency)
        self._refunded = Money.of_minor(0, amount.currency)
        self._processor_reference = ""
        self._version = 1  # bumps on every recorded transition: the optimistic-locking token a database row would carry
        self._history: list[Transition] = []

    def id(self) -> str:
        return self._id

    def amount(self) -> Money:
        return self._amount

    def state(self) -> PaymentState:
        return self._state

    def captured(self) -> Money:
        return self._captured

    def refunded(self) -> Money:
        return self._refunded

    def processor_reference(self) -> str:
        return self._processor_reference

    def version(self) -> int:
        return self._version

    def history(self) -> tuple[Transition, ...]:
        return tuple(self._history)

    def refundable(self) -> Money:
        """What can still be refunded: captured minus refunded (zero unless SUCCEEDED)."""
        return self._captured.minus(self._refunded)

    def _apply(self, to_state: PaymentState, reason: str) -> None:
        if not PaymentStateMachine.can_transition(self._state, to_state):
            raise IllegalTransitionError(self._id, self._state, to_state)
        self._record(Transition(self._state, to_state, reason, self._clock.now_millis()))
        self._state = to_state

    def _record(self, transition: Transition) -> None:
        self._history.append(transition)
        self._version += 1

    def start_processing(self) -> None:
        self._apply(PaymentState.PROCESSING, "sent to processor")

    def succeed(self, reference: str) -> None:
        """The processor approved: the full amount is captured."""
        if reference is None:
            raise TypeError("reference is required")
        self._apply(PaymentState.SUCCEEDED, "approved " + reference)
        self._processor_reference = reference
        self._captured = self._amount

    def fail(self, reason: str) -> None:
        if reason is None:
            raise TypeError("reason is required")
        self._apply(PaymentState.FAILED, reason)

    def cancel(self, reason: str) -> None:
        if reason is None:
            raise TypeError("reason is required")
        self._apply(PaymentState.CANCELED, reason)

    def refund(self, refund_amount: Money, reason: str) -> Money:
        """Refund part or all of the captured amount. Only a SUCCEEDED payment can be refunded; the amount must be
        positive, in the payment's currency, and no more than what is still refundable. Returns what remains."""
        if not isinstance(refund_amount, Money):
            raise TypeError("refund_amount must be a Money")
        if reason is None:
            raise TypeError("reason is required")
        if self._state is not PaymentState.SUCCEEDED:
            raise IllegalTransitionError(self._id, self._state, PaymentState.REFUNDED)
        if not refund_amount.is_positive():
            raise ValueError(f"refund must be positive: {refund_amount}")
        remaining = self.refundable()
        if refund_amount > remaining:  # CurrencyMismatchError if the currencies differ
            raise ValueError(f"refund {refund_amount} exceeds refundable {remaining}")
        self._refunded = self._refunded.plus(refund_amount)
        if self._refunded == self._captured:
            self._apply(PaymentState.REFUNDED, f"{reason} ({refund_amount})")
        else:
            self._record(Transition(PaymentState.SUCCEEDED, PaymentState.SUCCEEDED, f"{reason} ({refund_amount})", self._clock.now_millis()))
        return self.refundable()

    def __str__(self) -> str:
        return f"{self._id}[{self._state.wire()} v{self._version} {self._amount} captured={self._captured} refunded={self._refunded}]"

    __repr__ = __str__


# ---------------------------------------------------------------- stage 03: retries, budget, breaker, gateway


class ProcessorOutcome(Enum):
    APPROVED = "approved"  # funds captured; the processor returns a reference
    DECLINED = "declined"  # a definitive answer: never retry a decline
    UNAVAILABLE = "unavailable"  # timeout, 5xx, connection reset: the only outcome worth retrying


@dataclass(frozen=True)
class ProcessorResult:
    outcome: ProcessorOutcome
    reference: str


class Processor(Protocol):
    def charge(self, request: ChargeRequest) -> ProcessorResult: ...


class ScriptedProcessor:
    """Fake processor driven by a script: 'A' approve, 'D' decline, 'U' unavailable. "UUA" fails twice then approves.
    When the script runs out the last character repeats, so "A" is an always-healthy processor and "U" is a dead one."""

    def __init__(self, script: str) -> None:
        if not isinstance(script, str) or not script or any(c not in "ADU" for c in script):
            raise ValueError(f"script must be a non-empty string of A, D, U: {script!r}")
        self._script = script
        self._calls = 0

    def charge(self, request: ChargeRequest) -> ProcessorResult:
        step = self._script[min(self._calls, len(self._script) - 1)]
        self._calls += 1
        if step == "A":
            return ProcessorResult(ProcessorOutcome.APPROVED, f"auth-{self._calls}")
        if step == "D":
            return ProcessorResult(ProcessorOutcome.DECLINED, f"decl-{self._calls}")
        return ProcessorResult(ProcessorOutcome.UNAVAILABLE, "")

    def calls(self) -> int:
        return self._calls


class BackoffPolicy:
    """Exponential backoff with full jitter and a hard cap, plus the maximum number of attempts (first try included)."""

    def __init__(self, base_millis: int, cap_millis: int, max_attempts: int) -> None:
        if base_millis <= 0 or cap_millis < base_millis:
            raise ValueError("need 0 < base <= cap")
        if max_attempts < 1:
            raise ValueError("max_attempts must be at least 1")
        self._base_millis = base_millis
        self._cap_millis = cap_millis
        self._max_attempts = max_attempts

    def max_attempts(self) -> int:
        return self._max_attempts

    def ceiling_for(self, retry: int) -> int:
        """min(cap, base * 2^(retry-1)) for retry >= 1: the largest delay full jitter may draw."""
        if retry < 1:
            raise ValueError("retry numbers start at 1")
        ceiling = self._base_millis
        i = 1
        while i < retry and ceiling < self._cap_millis:
            ceiling = min(self._cap_millis, ceiling * 2)
            i += 1
        return min(self._cap_millis, ceiling)

    def delay_before(self, retry: int, rng: Rng) -> int:
        """uniform(0, ceiling) inclusive, from the injected Rng."""
        return rng.next_below(self.ceiling_for(retry) + 1)


class RetryBudget:
    """A token budget for retries. Every retry withdraws one token; every definitive answer from the processor
    deposits one (up to the cap). When the processor is down, the budget drains and retries stop."""

    def __init__(self, initial_tokens: int, max_tokens: int) -> None:
        if max_tokens < 1 or initial_tokens < 0 or initial_tokens > max_tokens:
            raise ValueError("bad budget")
        self._tokens = initial_tokens
        self._max_tokens = max_tokens

    def tokens(self) -> int:
        return self._tokens

    def try_withdraw(self) -> bool:
        if self._tokens == 0:
            return False
        self._tokens -= 1
        return True

    def deposit(self, n: int) -> None:
        if n < 0:
            raise ValueError("deposit must be non-negative")
        self._tokens = min(self._max_tokens, self._tokens + n)


class CircuitState(Enum):
    CLOSED = "closed"
    OPEN = "open"
    HALF_OPEN = "half_open"


class CircuitBreaker:
    """CLOSED: everything flows; failure_threshold consecutive failures open the circuit.
    OPEN: every request is refused until open_millis have passed on the injected clock, then HALF_OPEN.
    HALF_OPEN: requests flow as probes; one failure reopens, successes_to_close consecutive successes close."""

    def __init__(self, clock: Clock, failure_threshold: int, open_millis: int, successes_to_close: int) -> None:
        if failure_threshold < 1 or open_millis <= 0 or successes_to_close < 1:
            raise ValueError("bad breaker settings")
        if clock is None:
            raise TypeError("clock is required")
        self._clock = clock
        self._failure_threshold = failure_threshold
        self._open_millis = open_millis
        self._successes_to_close = successes_to_close
        self._state = CircuitState.CLOSED
        self._consecutive_failures = 0
        self._half_open_successes = 0
        self._opened_at = 0

    def state(self) -> CircuitState:
        """Current state after applying the OPEN -> HALF_OPEN timer."""
        if self._state is CircuitState.OPEN and self._clock.now_millis() - self._opened_at >= self._open_millis:
            self._state = CircuitState.HALF_OPEN
            self._half_open_successes = 0
        return self._state

    def allow_request(self) -> bool:
        return self.state() is not CircuitState.OPEN

    def record_success(self) -> None:
        current = self.state()
        self._consecutive_failures = 0
        if current is CircuitState.HALF_OPEN:
            self._half_open_successes += 1
            if self._half_open_successes >= self._successes_to_close:
                self._state = CircuitState.CLOSED
                self._half_open_successes = 0

    def record_failure(self) -> None:
        current = self.state()
        self._consecutive_failures += 1
        if current is CircuitState.HALF_OPEN or self._consecutive_failures >= self._failure_threshold:
            self._trip()

    def _trip(self) -> None:
        self._state = CircuitState.OPEN
        self._opened_at = self._clock.now_millis()
        self._consecutive_failures = 0
        self._half_open_successes = 0

    def retry_after_millis(self) -> int:
        """Milliseconds until an OPEN circuit becomes HALF_OPEN; zero when requests are allowed."""
        return self._opened_at + self._open_millis - self._clock.now_millis() if self.state() is CircuitState.OPEN else 0


@dataclass(frozen=True)
class ChargeOutcome:
    """The result of a charge through the retrying client: the final outcome plus how it was reached."""

    outcome: ProcessorOutcome
    reference: str
    attempts: int
    waited_millis: int
    reason: str


class RetryingClient:
    """Wraps a Processor with backoff, a retry budget, and a circuit breaker. Only UNAVAILABLE is retried."""

    def __init__(self, processor: Processor, policy: BackoffPolicy, budget: RetryBudget, breaker: CircuitBreaker, rng: Rng, clock: Clock) -> None:
        for name, value in (("processor", processor), ("policy", policy), ("budget", budget), ("breaker", breaker), ("rng", rng), ("clock", clock)):
            if value is None:
                raise TypeError(f"{name} is required")
        self._processor = processor
        self._policy = policy
        self._budget = budget
        self._breaker = breaker
        self._rng = rng
        self._clock = clock

    def charge(self, request: ChargeRequest) -> ChargeOutcome:
        if not self._breaker.allow_request():
            return ChargeOutcome(ProcessorOutcome.UNAVAILABLE, "", 0, 0, "circuit open")
        attempts = 0
        waited = 0
        while True:
            attempts += 1
            result = self._processor.charge(request)
            if result.outcome is not ProcessorOutcome.UNAVAILABLE:
                self._breaker.record_success()
                self._budget.deposit(1)
                reason = "approved" if result.outcome is ProcessorOutcome.APPROVED else "declined"
                return ChargeOutcome(result.outcome, result.reference, attempts, waited, reason)
            self._breaker.record_failure()
            if attempts >= self._policy.max_attempts():
                return ChargeOutcome(ProcessorOutcome.UNAVAILABLE, "", attempts, waited, "retries exhausted")
            if not self._breaker.allow_request():
                return ChargeOutcome(ProcessorOutcome.UNAVAILABLE, "", attempts, waited, "circuit open")
            if not self._budget.try_withdraw():
                return ChargeOutcome(ProcessorOutcome.UNAVAILABLE, "", attempts, waited, "retry budget exhausted")
            delay = self._policy.delay_before(attempts, self._rng)
            self._clock.sleep(delay)
            waited += delay


class Gateway:
    """The payment gateway a merchant talks to. One charge() call: claim the idempotency key, create the payment,
    drive it through the state machine with the retrying client, store the response under the key.
    Responses that must not be replayed (processor unavailable, circuit open) release the key instead."""

    def __init__(self, store: IdempotencyStore, client: RetryingClient, clock: Clock) -> None:
        for name, value in (("store", store), ("client", client), ("clock", clock)):
            if value is None:
                raise TypeError(f"{name} is required")
        self._store = store
        self._client = client
        self._clock = clock
        self._payments: dict[str, Payment] = {}
        self._dispatcher: WebhookDispatcher | None = None  # Stage 04; None until attached
        self._payment_seq = 0
        self._event_seq = 0

    def charge(self, request: ChargeRequest) -> ApiResponse:
        begin = self._store.begin(request.merchant_id, request.idempotency_key, request.fingerprint())
        if begin.decision is Decision.REPLAY:
            assert begin.response is not None
            return begin.response.as_replay()
        if begin.decision is Decision.IN_PROGRESS:
            return ApiResponse(409, '{"error":"request in progress"}')
        if begin.decision is Decision.CONFLICT:
            return ApiResponse(422, '{"error":"idempotency key reused with a different request"}')
        self._payment_seq += 1
        payment = Payment(f"pay_{self._payment_seq}", request.amount, self._clock)
        self._payments[payment.id()] = payment
        payment.start_processing()
        outcome = self._client.charge(request)
        if outcome.outcome is ProcessorOutcome.APPROVED:
            payment.succeed(outcome.reference)
            response = ApiResponse(201, self._body(payment))
            event_type = "payment.succeeded"
        elif outcome.outcome is ProcessorOutcome.DECLINED:
            payment.fail("declined " + outcome.reference)
            response = ApiResponse(402, self._body(payment))
            event_type = "payment.failed"
        else:
            payment.fail(outcome.reason)
            self._store.release(request.merchant_id, request.idempotency_key)
            return ApiResponse(503, f'{{"error":"{outcome.reason}","payment_id":"{payment.id()}"}}')
        # Complete the key first, then emit: a crash in between loses an event (detectable, repairable) rather
        # than leaving the key open so a retry charges again. The transactional outbox closes even that gap.
        self._store.complete(request.merchant_id, request.idempotency_key, response)
        self._emit(event_type, payment)
        return response

    @staticmethod
    def _body(p: Payment) -> str:
        return f'{{"payment_id":"{p.id()}","state":"{p.state().wire()}","amount":"{p.amount()}"}}'

    def attach_webhooks(self, dispatcher: WebhookDispatcher) -> None:
        """Stage 04: route lifecycle events to a dispatcher."""
        if dispatcher is None:
            raise TypeError("dispatcher is required")
        self._dispatcher = dispatcher

    def _emit(self, type: str, payment: Payment) -> None:
        if self._dispatcher is None:
            return
        self._event_seq += 1
        event = WebhookEvent(f"evt_{self._event_seq}", type, payment.id(), f"amount={payment.amount()}", self._clock.now_millis())
        self._dispatcher.enqueue(event)

    def payment(self, id: str) -> Payment | None:
        return self._payments.get(id)

    def payments(self) -> tuple[Payment, ...]:
        return tuple(self._payments.values())


# ---------------------------------------------------------------- stage 04: webhooks


@dataclass(frozen=True)
class WebhookEvent:
    """One lifecycle event. The body is a fixed-order JSON object so that the same event always produces the same bytes."""

    id: str
    type: str
    payment_id: str
    data: str
    created_at: int

    def __post_init__(self) -> None:
        for s in (self.id, self.type, self.payment_id, self.data):
            if not isinstance(s, str):
                raise TypeError("event fields must be strings")
            if '"' in s or "\\" in s:
                raise ValueError("fields must not contain quotes or backslashes")
        if not self.id.strip() or not self.type.strip():
            raise ValueError("id and type are required")

    def to_body(self) -> str:
        return (f'{{"id":"{self.id}","type":"{self.type}","payment_id":"{self.payment_id}",'
                f'"data":"{self.data}","created_at":{self.created_at}}}')

    @staticmethod
    def parse(body: str) -> WebhookEvent:
        """Inverse of to_body() for the fixed format above; anything else is rejected (ValueError)."""
        try:
            id = WebhookEvent._field(body, "id")
            type = WebhookEvent._field(body, "type")
            payment_id = WebhookEvent._field(body, "payment_id")
            data = WebhookEvent._field(body, "data")
            marker = '"created_at":'
            at = body.find(marker)
            if at < 0:
                raise ValueError("missing created_at")
            digits = body[at + len(marker):body.rfind("}")]
            return WebhookEvent(id, type, payment_id, data, int(digits))
        except (ValueError, TypeError, IndexError) as exc:
            raise ValueError(f"malformed webhook body: {exc}") from exc

    @staticmethod
    def _field(body: str, key: str) -> str:
        marker = f'"{key}":"'
        start = body.find(marker)
        if start < 0:
            raise ValueError(f"missing {key}")
        start += len(marker)
        end = body.find('"', start)
        if end < 0:
            raise ValueError(f"unterminated {key}")
        return body[start:end]


class WebhookSigner:
    """Produces the signature header the producer sends: "t=<millis>,v1=<hmac-sha256 hex>"."""

    def __init__(self, secret: str) -> None:
        if not secret:
            raise ValueError("secret is required")
        self._secret = secret.encode("utf-8")

    @staticmethod
    def hmac_sha256_hex(secret: bytes, message: str) -> str:
        return hmac.new(secret, message.encode("utf-8"), hashlib.sha256).hexdigest()

    def signature(self, timestamp_millis: int, body: str) -> str:
        """The signed payload is "<timestamp>.<body>": binding the timestamp defeats replay of an old signature."""
        return self.hmac_sha256_hex(self._secret, f"{timestamp_millis}.{body}")

    def header(self, timestamp_millis: int, body: str) -> str:
        return f"t={timestamp_millis},v1={self.signature(timestamp_millis, body)}"


class VerifyResult(Enum):
    OK = "ok"
    MALFORMED_HEADER = "malformed_header"
    STALE_TIMESTAMP = "stale_timestamp"
    BAD_SIGNATURE = "bad_signature"


class WebhookVerifier:
    """The consumer side of the signature: same secret, same string, constant-time compare, bounded clock skew."""

    def __init__(self, secret: str, tolerance_millis: int, clock: Clock) -> None:
        if not secret:
            raise ValueError("secret is required")
        if tolerance_millis < 0:
            raise ValueError("tolerance must be non-negative")
        if clock is None:
            raise TypeError("clock is required")
        self._secret = secret.encode("utf-8")
        self._tolerance_millis = tolerance_millis
        self._clock = clock

    def verify(self, body: str | None, header: str | None) -> VerifyResult:
        if body is None or header is None:
            return VerifyResult.MALFORMED_HEADER
        parts = header.split(",")
        if len(parts) != 2 or not parts[0].startswith("t=") or not parts[1].startswith("v1="):
            return VerifyResult.MALFORMED_HEADER
        try:
            timestamp = int(parts[0][2:])
        except ValueError:
            return VerifyResult.MALFORMED_HEADER
        provided = parts[1][3:]
        if abs(self._clock.now_millis() - timestamp) > self._tolerance_millis:
            return VerifyResult.STALE_TIMESTAMP
        expected = WebhookSigner.hmac_sha256_hex(self._secret, f"{timestamp}.{body}")
        return VerifyResult.OK if hmac.compare_digest(expected.encode("utf-8"), provided.encode("utf-8")) else VerifyResult.BAD_SIGNATURE


class ReceiveStatus(Enum):
    ACCEPTED = "accepted"
    DUPLICATE = "duplicate"
    REJECTED = "rejected"


@dataclass(frozen=True)
class ReceiveResult:
    status: ReceiveStatus
    verification: VerifyResult
    event: WebhookEvent | None


class WebhookConsumer:
    """A merchant's webhook handler: verify, dedupe by event id, then process exactly once."""

    def __init__(self, verifier: WebhookVerifier) -> None:
        if verifier is None:
            raise TypeError("verifier is required")
        self._verifier = verifier
        self._seen_ids: set[str] = set()
        self._processed: list[WebhookEvent] = []

    def receive(self, body: str, header: str) -> ReceiveResult:
        verification = self._verifier.verify(body, header)
        if verification is not VerifyResult.OK:
            return ReceiveResult(ReceiveStatus.REJECTED, verification, None)
        try:
            event = WebhookEvent.parse(body)
        except ValueError:
            return ReceiveResult(ReceiveStatus.REJECTED, VerifyResult.OK, None)
        if event.id in self._seen_ids:
            return ReceiveResult(ReceiveStatus.DUPLICATE, verification, event)
        self._seen_ids.add(event.id)
        self._processed.append(event)
        return ReceiveResult(ReceiveStatus.ACCEPTED, verification, event)

    def processed(self) -> tuple[WebhookEvent, ...]:
        return tuple(self._processed)


class WebhookEndpoint(Protocol):
    """Where the dispatcher posts to. Returns True for a 2xx."""

    def deliver(self, body: str, header: str) -> bool: ...


@dataclass(frozen=True)
class Delivery:
    body: str
    header: str


class ScriptedEndpoint:
    """Scripted endpoint: 'S' succeed, 'F' fail; the last character repeats. Records every delivery it saw."""

    Delivery = Delivery

    def __init__(self, script: str) -> None:
        if not isinstance(script, str) or not script or any(c not in "SF" for c in script):
            raise ValueError(f"script must be a non-empty string of S and F: {script!r}")
        self._script = script
        self._deliveries: list[Delivery] = []

    def deliver(self, body: str, header: str) -> bool:
        step = self._script[min(len(self._deliveries), len(self._script) - 1)]
        self._deliveries.append(Delivery(body, header))
        return step == "S"

    def deliveries(self) -> tuple[Delivery, ...]:
        return tuple(self._deliveries)


class ConsumerEndpoint:
    """Adapts a WebhookConsumer as an endpoint: ACCEPTED and DUPLICATE are both 2xx, REJECTED is not."""

    def __init__(self, consumer: WebhookConsumer) -> None:
        if consumer is None:
            raise TypeError("consumer is required")
        self._consumer = consumer

    def deliver(self, body: str, header: str) -> bool:
        return self._consumer.receive(body, header).status is not ReceiveStatus.REJECTED


@dataclass(frozen=True)
class DeliveryJob:
    """A queued delivery: the event, how many attempts have been made, and when the next one is due."""

    event: WebhookEvent
    attempts: int
    next_attempt_at: int


class WebhookDispatcher:
    """At-least-once delivery. enqueue() schedules the first attempt now; run_due() performs every attempt whose
    time has come. A failure schedules the next attempt retry_delays[attempts-1] later; when the schedule is
    used up the job goes to the dead-letter list for a human."""

    def __init__(self, signer: WebhookSigner, endpoint: WebhookEndpoint, clock: Clock, retry_delays_millis: Sequence[int]) -> None:
        for name, value in (("signer", signer), ("endpoint", endpoint), ("clock", clock), ("retry_delays_millis", retry_delays_millis)):
            if value is None:
                raise TypeError(f"{name} is required")
        self._retry_delays = tuple(retry_delays_millis)
        if any(d < 0 for d in self._retry_delays):
            raise ValueError("retry delays must be non-negative")
        self._signer = signer
        self._endpoint = endpoint
        self._clock = clock
        self._pending: list[DeliveryJob] = []
        self._delivered: list[WebhookEvent] = []
        self._dead_letters: list[DeliveryJob] = []

    def max_attempts(self) -> int:
        """Total attempts a job may receive: the first one plus one per retry delay."""
        return len(self._retry_delays) + 1

    def enqueue(self, event: WebhookEvent) -> None:
        if event is None:
            raise TypeError("event is required")
        self._pending.append(DeliveryJob(event, 0, self._clock.now_millis()))

    def run_due(self) -> int:
        """Attempt every due job once. Returns the number of attempts made."""
        now = self._clock.now_millis()
        attempts_made = 0
        for job in list(self._pending):
            if job.next_attempt_at > now:
                continue
            self._pending.remove(job)
            attempts_made += 1
            body = job.event.to_body()
            ok = self._endpoint.deliver(body, self._signer.header(now, body))
            attempts = job.attempts + 1
            if ok:
                self._delivered.append(job.event)
            elif attempts >= self.max_attempts():
                self._dead_letters.append(DeliveryJob(job.event, attempts, now))
            else:
                self._pending.append(DeliveryJob(job.event, attempts, now + self._retry_delays[attempts - 1]))
        return attempts_made

    def pending(self) -> tuple[DeliveryJob, ...]:
        return tuple(self._pending)

    def delivered(self) -> tuple[WebhookEvent, ...]:
        return tuple(self._delivered)

    def dead_letters(self) -> tuple[DeliveryJob, ...]:
        return tuple(self._dead_letters)


# ---------------------------------------------------------------- demo


def main() -> None:
    clock = FakeClock(1_700_000_000_000)
    store = IdempotencyStore(clock, 24 * 60 * 60 * 1000)

    print("== Stage 01: idempotency store ==")
    req = ChargeRequest("m_1", "key-1", Money.parse("19.99", USD), "tok_visa", "order-42")
    print("canonical  = " + req.canonical().replace("\n", "\\n"))
    print("fingerprint= " + req.fingerprint())
    print(f"first begin -> {store.begin('m_1', 'key-1', req.fingerprint()).decision.name}")
    print(f"while open  -> {store.begin('m_1', 'key-1', req.fingerprint()).decision.name}")
    store.complete("m_1", "key-1", ApiResponse(201, '{"payment_id":"pay_1"}'))
    print(f"after done  -> {store.begin('m_1', 'key-1', req.fingerprint())}")
    other = ChargeRequest("m_1", "key-1", Money.parse("29.99", USD), "tok_visa", "order-42")
    print(f"other body  -> {store.begin('m_1', 'key-1', other.fingerprint()).decision.name}")

    print("\n== Stage 02: payment state machine ==")
    p = Payment("pay_demo", Money.parse("50.00", USD), clock)
    p.start_processing()
    p.succeed("auth-77")
    print(f"refund 20 -> remaining {p.refund(Money.parse('20.00', USD), 'customer request')}")
    print(f"refund 30 -> remaining {p.refund(Money.parse('30.00', USD), 'customer request')} state {p.state().name}")
    try:
        p.refund(Money.parse("1.00", USD), "again")
    except IllegalTransitionError as exc:
        print(f"rejected: {exc}")
    for t in p.history():
        print(f"  {t.from_state.name} -> {t.to_state.name} : {t.reason}")

    print("\n== Stage 03: retries with full jitter, budget, breaker ==")
    policy = BackoffPolicy(100, 2_000, 4)
    rng = Rng(42)
    for retry in range(1, 5):
        print(f"retry {retry}: ceiling {policy.ceiling_for(retry)} ms, drawn {policy.delay_before(retry, Rng(42 + retry))} ms")
    breaker = CircuitBreaker(clock, 3, 30_000, 1)
    client = RetryingClient(ScriptedProcessor("UUA"), policy, RetryBudget(10, 10), breaker, rng, clock)
    gateway = Gateway(store, client, clock)
    first = gateway.charge(ChargeRequest("m_1", "key-2", Money.parse("19.99", USD), "tok_visa", "order-43"))
    print(f"charge -> {first.status} {first.body}")
    replay = gateway.charge(ChargeRequest("m_1", "key-2", Money.parse("19.99", USD), "tok_visa", "order-43"))
    print(f"replay -> {replay.status} replayed={str(replay.replayed).lower()}")

    print("\n== Stage 04: signed webhooks, at-least-once, dedupe ==")
    signer = WebhookSigner("whsec_demo")
    consumer = WebhookConsumer(WebhookVerifier("whsec_demo", 5 * 60_000, clock))
    dispatcher = WebhookDispatcher(signer, ConsumerEndpoint(consumer), clock, [60_000, 300_000])
    gateway.attach_webhooks(dispatcher)
    gateway.charge(ChargeRequest("m_1", "key-3", Money.parse("5.00", USD), "tok_visa", "order-44"))
    print(f"pending before run: {len(dispatcher.pending())}")
    dispatcher.run_due()
    evt = consumer.processed()[0]
    body = evt.to_body()
    header = signer.header(clock.now_millis(), body)
    print(f"delivered {evt.id} {evt.type} header {header}")
    print(f"same event again -> {consumer.receive(body, header).status.name}")
    print(f"tampered body    -> {consumer.receive(body.replace('5.00', '500.00'), header).status.name}")


if __name__ == "__main__":
    main()
