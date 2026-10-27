"""Project: projects/idempotent-payment-gateway — Stage 01 starter (Python): idempotency store.
Run:  python3 main.py
Grade: python3 scripts/project_test.py idempotent-payment-gateway --lang python --stage 1 --path <workspace>
Money, Clock/FakeClock and Rng are given and must not change. The data types of stage 01 are given too; every
function marked TODO raises NotImplementedError until you implement it, so the stage-01 tests fail visibly until
then. Keep the names: the tests import them. Later stages add PaymentState, Payment, BackoffPolicy, Gateway,
WebhookDispatcher, ... to this same file (see each stage's starter/STAGE.md).
Python 3.11+, standard library only.
"""

from __future__ import annotations

import hashlib
import threading
import time
from dataclasses import dataclass, field
from enum import Enum
from functools import total_ordering
from typing import Protocol, Sequence

# ---------------------------------------------------------------- Money (given; copied from phases/10/01)

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
        """TODO: hashlib.sha256 over the UTF-8 bytes, hex-encoded in lowercase."""
        raise NotImplementedError("TODO stage 01: Fingerprint.sha256_hex")


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
        """TODO: "<minor_units>\\n<currency code>\\n<source>\\n<description>". The key is NOT part of it."""
        raise NotImplementedError("TODO stage 01: ChargeRequest.canonical")

    def fingerprint(self) -> str:
        """TODO: Fingerprint.sha256_hex(self.canonical())."""
        raise NotImplementedError("TODO stage 01: ChargeRequest.fingerprint")


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
        """TODO: Claim the key for this request or report why the caller must not proceed.
        No live record -> insert IN_PROGRESS (expired records count as absent) and return NEW.
        Different fingerprint -> CONFLICT. Same fingerprint and IN_PROGRESS -> IN_PROGRESS.
        Same fingerprint and COMPLETED -> REPLAY with the stored response. Hold self._lock while you look and write."""
        raise NotImplementedError("TODO stage 01: IdempotencyStore.begin")

    def complete(self, merchant_id: str, key: str, response: ApiResponse) -> None:
        """TODO: Store the response (replayed=False) for an IN_PROGRESS key. Unknown, expired or already completed -> RuntimeError."""
        raise NotImplementedError("TODO stage 01: IdempotencyStore.complete")

    def release(self, merchant_id: str, key: str) -> bool:
        """TODO: Drop an IN_PROGRESS record so the key can be used again; False if absent; RuntimeError if COMPLETED."""
        raise NotImplementedError("TODO stage 01: IdempotencyStore.release")

    def get(self, merchant_id: str, key: str) -> IdempotencyRecord | None:
        """TODO: The live record, if any; expired records read as absent (None)."""
        raise NotImplementedError("TODO stage 01: IdempotencyStore.get")

    def purge_expired(self) -> int:
        """TODO: Drop every expired record; return how many were removed."""
        raise NotImplementedError("TODO stage 01: IdempotencyStore.purge_expired")

    def size(self) -> int:
        """Records held, expired or not; call purge_expired() first for the live count."""
        with self._lock:
            return len(self._records)


# ---------------------------------------------------------------- workspace entry point (the grader never runs it)


def main() -> None:
    clock = FakeClock(1_700_000_000_000)
    store = IdempotencyStore(clock, 24 * 60 * 60 * 1000)
    req = ChargeRequest("m_1", "key-1", Money.parse("19.99", USD), "tok_visa", "order-42")
    print(f"idempotent-payment-gateway workspace: store ttl {store.ttl_millis()} ms, request {req.amount}")
    try:
        print(f"fingerprint = {req.fingerprint()}")
        print(f"first begin -> {store.begin('m_1', 'key-1', req.fingerprint()).decision.name}")
    except NotImplementedError as todo:
        print(f"not implemented yet: {todo}")


if __name__ == "__main__":
    main()
