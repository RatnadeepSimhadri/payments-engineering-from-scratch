"""Lesson: phases/11-payment-rails/02-card-authorization-lifecycle/docs/en.md
Authorization, Capture, Void, Refund: The Card Lifecycle State Machine
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: ISO 8583:1987 message classes (01xx authorization, 02xx financial, 04xx reversal); Visa Core Rules and
Mastercard Transaction Processing Rules on authorization validity, incremental and estimated authorizations.
Python 3.11+, standard library only. Same design as the Java and Go versions.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from datetime import datetime, timedelta, timezone
from enum import Enum
from functools import total_ordering
from typing import Optional


# ---------------------------------------------------------------------------------------------------------------
# Money, copied from Phase 10 Lesson 01 and trimmed to what this lesson needs.
# ---------------------------------------------------------------------------------------------------------------


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
JPY = Currency("JPY", 0)


class Rounding(Enum):
    HALF_EVEN = "half_even"
    HALF_UP = "half_up"
    DOWN = "down"
    UP = "up"


class CurrencyMismatchError(ValueError):
    def __init__(self, a: Currency, b: Currency) -> None:
        super().__init__(f"cannot combine {a.code} with {b.code}")


def divide_rounded(dividend: int, divisor: int, rounding: Rounding) -> int:
    """Truncation semantics identical to Java/Go: work on magnitudes, reapply the sign."""
    if divisor == 0:
        raise ZeroDivisionError("divisor must not be zero")
    sign = -1 if (dividend < 0) != (divisor < 0) else 1
    quotient, remainder = divmod(abs(dividend), abs(divisor))
    if remainder == 0:
        return sign * quotient
    twice = remainder * 2
    if rounding is Rounding.UP:
        quotient += 1
    elif rounding is Rounding.HALF_UP and twice >= abs(divisor):
        quotient += 1
    elif rounding is Rounding.HALF_EVEN and (twice > abs(divisor) or (twice == abs(divisor) and quotient % 2 == 1)):
        quotient += 1
    return sign * quotient


@total_ordering
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
    def parse(text: str, currency: Currency) -> "Money":
        s = text.strip()
        negative = s.startswith("-")
        if negative or s.startswith("+"):
            s = s[1:]
        if not s:
            raise ValueError("empty amount")
        whole, _, frac = s.partition(".")
        whole = whole or "0"
        if not whole.isdigit() or (frac and not frac.isdigit()):
            raise ValueError(f"not a decimal amount: {text!r}")
        if len(frac) > currency.exponent:
            raise ArithmeticError(f"{text} has more decimals than {currency.code} allows")
        frac = frac.ljust(currency.exponent, "0")
        minor = int(whole) * currency.minor_units_per_major() + (int(frac) if frac else 0)
        return Money(-minor if negative else minor, currency)

    def _require_same_currency(self, other: "Money") -> None:
        if self.currency != other.currency:
            raise CurrencyMismatchError(self.currency, other.currency)

    def plus(self, other: "Money") -> "Money":
        self._require_same_currency(other)
        return Money(self.minor_units + other.minor_units, self.currency)

    def minus(self, other: "Money") -> "Money":
        self._require_same_currency(other)
        return Money(self.minor_units - other.minor_units, self.currency)

    def is_zero(self) -> bool:
        return self.minor_units == 0

    def is_positive(self) -> bool:
        return self.minor_units > 0

    def times(self, numerator: int, denominator: int, rounding: Rounding) -> "Money":
        if denominator <= 0:
            raise ArithmeticError("denominator must be positive")
        return Money(divide_rounded(self.minor_units * numerator, denominator, rounding), self.currency)

    def __lt__(self, other: "Money") -> bool:
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


# ---------------------------------------------------------------------------------------------------------------
# Clock
# ---------------------------------------------------------------------------------------------------------------


class Clock:
    """Injected time source. Production wraps datetime.now(timezone.utc); tests and the demo use FakeClock."""

    def now(self) -> datetime:
        raise NotImplementedError


class FakeClock(Clock):
    def __init__(self, start: datetime) -> None:
        self._current = start

    def now(self) -> datetime:
        return self._current

    def advance(self, delta: timedelta) -> None:
        self._current = self._current + delta


# ---------------------------------------------------------------------------------------------------------------
# The state machine
# ---------------------------------------------------------------------------------------------------------------


class TransactionState(Enum):
    AUTHORIZED = "AUTHORIZED"
    PARTIALLY_CAPTURED = "PARTIALLY_CAPTURED"
    CAPTURED = "CAPTURED"
    VOIDED = "VOIDED"
    EXPIRED = "EXPIRED"
    PARTIALLY_REFUNDED = "PARTIALLY_REFUNDED"
    REFUNDED = "REFUNDED"

    def is_terminal(self) -> bool:
        return self in (TransactionState.VOIDED, TransactionState.EXPIRED, TransactionState.REFUNDED)

    def __str__(self) -> str:
        return self.value


class Action(Enum):
    """Everything that can happen to a transaction. AUTHORIZE creates it; the other six are transitions on it."""

    AUTHORIZE = "AUTHORIZE"
    INCREMENT = "INCREMENT"
    CAPTURE = "CAPTURE"
    CLOSE_CAPTURE = "CLOSE_CAPTURE"
    VOID = "VOID"
    EXPIRE = "EXPIRE"
    REFUND = "REFUND"

    def __str__(self) -> str:
        return self.value


class IllegalTransitionError(RuntimeError):
    """A transition was attempted from a state that does not allow it."""

    def __init__(self, from_state: TransactionState, action: Action) -> None:
        super().__init__(f"{action} is not allowed in state {from_state}")
        self.from_state = from_state
        self.action = action


class LimitExceededError(ValueError):
    """An amount rule was broken: capture above the ceiling, refund above captured, non-positive amount."""


@dataclass(frozen=True)
class LifecyclePolicy:
    """How long an authorization stays valid and how far (in basis points) a capture may exceed it."""

    auth_validity: timedelta
    over_capture_tolerance_bps: int = 0

    def __post_init__(self) -> None:
        if self.auth_validity <= timedelta(0):
            raise ValueError("validity must be positive")
        if not 0 <= self.over_capture_tolerance_bps <= 10_000:
            raise ValueError("tolerance must be 0..10000 bps")

    @staticmethod
    def for_mcc(mcc: str) -> "LifecyclePolicy":
        """Illustrative mapping: gratuity-heavy categories get a 15% tolerance; everyone else gets none."""
        if mcc in ("5812", "5813", "5814", "4121", "7230"):
            return LifecyclePolicy(timedelta(days=7), 1500)
        return STANDARD

    def capture_ceiling(self, authorized: Money) -> Money:
        """The most that may be captured in total, rounded toward the merchant's disadvantage."""
        return authorized.times(10_000 + self.over_capture_tolerance_bps, 10_000, Rounding.DOWN)


STANDARD = LifecyclePolicy(timedelta(days=7), 0)


@dataclass(frozen=True)
class TransactionEvent:
    """One persisted transition. The log of these is the transaction; the state is derived from it."""

    sequence: int
    action: Action
    amount: Money
    from_state: Optional[TransactionState]
    to_state: TransactionState
    at: datetime
    reason: str

    def __post_init__(self) -> None:
        if not self.reason or not self.reason.strip():
            raise ValueError("every transition needs a reason")

    def __str__(self) -> str:
        origin = "(new)" if self.from_state is None else str(self.from_state)
        return (f"#{self.sequence} {str(self.action):<13} {str(self.amount):>12}  {origin} -> {self.to_state}"
                f"  @{self.at.isoformat()}  \"{self.reason}\"")


# The legal-move table: which actions each state accepts. Everything not listed raises.
LEGAL: dict[TransactionState, frozenset[Action]] = {
    TransactionState.AUTHORIZED: frozenset({Action.INCREMENT, Action.CAPTURE, Action.VOID, Action.EXPIRE}),
    TransactionState.PARTIALLY_CAPTURED: frozenset({Action.CAPTURE, Action.CLOSE_CAPTURE, Action.EXPIRE}),
    TransactionState.CAPTURED: frozenset({Action.REFUND}),
    TransactionState.PARTIALLY_REFUNDED: frozenset({Action.REFUND}),
    TransactionState.VOIDED: frozenset(),
    TransactionState.EXPIRED: frozenset(),
    TransactionState.REFUNDED: frozenset(),
}


class CardTransaction:
    """The lifecycle of one card authorization. Every mutation goes through _transition, which runs after the
    legal-move table and the amount rule have both been checked, so a failed call leaves nothing behind."""

    def __init__(self, id: str, amount: Money, clock: Clock, policy: LifecyclePolicy, reason: str) -> None:
        if not amount.is_positive():
            raise LimitExceededError(f"authorization amount must be positive: {amount}")
        self._id = id
        self._clock = clock
        self._policy = policy
        self._authorized = amount
        self._captured = Money.of_minor(0, amount.currency)
        self._refunded = Money.of_minor(0, amount.currency)
        self._state = TransactionState.AUTHORIZED
        now = clock.now()
        self._expires_at = now + policy.auth_validity
        self._events: list[TransactionEvent] = [
            TransactionEvent(1, Action.AUTHORIZE, amount, None, TransactionState.AUTHORIZED, now, reason)
        ]

    @staticmethod
    def authorize(id: str, amount: Money, clock: Clock, policy: LifecyclePolicy, reason: str) -> "CardTransaction":
        return CardTransaction(id, amount, clock, policy, reason)

    # ---- read side ----

    @property
    def id(self) -> str:
        return self._id

    @property
    def state(self) -> TransactionState:
        return self._state

    @property
    def authorized_amount(self) -> Money:
        return self._authorized

    @property
    def captured_amount(self) -> Money:
        return self._captured

    @property
    def refunded_amount(self) -> Money:
        return self._refunded

    @property
    def expires_at(self) -> datetime:
        return self._expires_at

    @property
    def events(self) -> tuple[TransactionEvent, ...]:
        return tuple(self._events)

    def remaining_authorization(self) -> Money:
        """Authorized minus captured while the hold is open; zero once it is closed, voided, or expired."""
        zero = Money.of_minor(0, self._authorized.currency)
        if self._state not in (TransactionState.AUTHORIZED, TransactionState.PARTIALLY_CAPTURED):
            return zero
        rest = self._authorized.minus(self._captured)
        return rest if rest.is_positive() else zero

    def net_settled(self) -> Money:
        """What the merchant will actually be paid: captured minus refunded."""
        return self._captured.minus(self._refunded)

    # ---- write side ----

    def increment_authorization(self, additional: Money, reason: str) -> None:
        """Raise the authorized amount while the authorization is still open; resets the expiry window."""
        self.expire_if_due(reason)
        self._require(Action.INCREMENT)
        self._require_positive(additional)
        self._authorized = self._authorized.plus(additional)
        self._expires_at = self._clock.now() + self._policy.auth_validity
        self._transition(Action.INCREMENT, additional, TransactionState.AUTHORIZED, reason)

    def capture(self, amount: Money, reason: str) -> None:
        """Capture part or all of the authorization. Cumulative captures may not exceed the policy's ceiling."""
        self.expire_if_due(reason)
        self._require(Action.CAPTURE)
        self._require_positive(amount)
        ceiling = self._policy.capture_ceiling(self._authorized)
        total = self._captured.plus(amount)
        if total > ceiling:
            raise LimitExceededError(
                f"capture of {amount} would bring total to {total}, above ceiling {ceiling} "
                f"(authorized {self._authorized}, tolerance {self._policy.over_capture_tolerance_bps} bps)")
        self._captured = total
        nxt = TransactionState.CAPTURED if self._captured >= self._authorized else TransactionState.PARTIALLY_CAPTURED
        self._transition(Action.CAPTURE, amount, nxt, reason)

    def close_capture(self, reason: str) -> None:
        """Declare a partially captured transaction complete: the uncaptured remainder is released."""
        self.expire_if_due(reason)
        self._require(Action.CLOSE_CAPTURE)
        released = self.remaining_authorization()
        self._transition(Action.CLOSE_CAPTURE, released, TransactionState.CAPTURED, reason)

    def void_authorization(self, reason: str) -> None:
        """Release the whole hold before anything is captured. Not available once money has moved."""
        self.expire_if_due(reason)
        self._require(Action.VOID)
        self._transition(Action.VOID, self._authorized, TransactionState.VOIDED, reason)

    def refund(self, amount: Money, reason: str) -> None:
        """Return captured money to the cardholder. Cumulative refunds may not exceed what was captured."""
        self._require(Action.REFUND)
        self._require_positive(amount)
        total = self._refunded.plus(amount)
        if total > self._captured:
            raise LimitExceededError(
                f"refund of {amount} would bring total refunded to {total}, above captured {self._captured}")
        self._refunded = total
        nxt = TransactionState.REFUNDED if self._refunded == self._captured else TransactionState.PARTIALLY_REFUNDED
        self._transition(Action.REFUND, amount, nxt, reason)

    def expire_if_due(self, attempted_reason: str) -> bool:
        """The clock-driven transition. Records EXPIRE and returns True if the window has passed and the state
        still allows it. Called before every merchant-initiated action."""
        if Action.EXPIRE not in LEGAL[self._state]:
            return False
        if self._clock.now() < self._expires_at:
            return False
        days = self._policy.auth_validity.days
        # A never-captured authorization becomes EXPIRED (terminal). A partially captured one keeps its captured
        # funds: the remainder lapses and it becomes CAPTURED, as close_capture would leave it, so refunds still work.
        nxt = TransactionState.CAPTURED if self._state is TransactionState.PARTIALLY_CAPTURED else TransactionState.EXPIRED
        self._transition(Action.EXPIRE, self.remaining_authorization(), nxt,
                         f"authorization validity of {days} days elapsed (while attempting: {attempted_reason})")
        return True

    # ---- the single choke point ----

    def _require(self, action: Action) -> None:
        if action not in LEGAL[self._state]:
            raise IllegalTransitionError(self._state, action)

    def _require_positive(self, amount: Money) -> None:
        if amount.currency != self._authorized.currency:
            raise CurrencyMismatchError(self._authorized.currency, amount.currency)
        if not amount.is_positive():
            raise LimitExceededError(f"amount must be positive: {amount}")

    def _transition(self, action: Action, amount: Money, to_state: TransactionState, reason: str) -> None:
        self._events.append(TransactionEvent(len(self._events) + 1, action, amount, self._state, to_state,
                                             self._clock.now(), reason))
        self._state = to_state

    def print_events(self) -> None:
        print(f"transaction {self._id} [{self._state}] authorized={self._authorized} captured={self._captured} "
              f"refunded={self._refunded} net={self.net_settled()}")
        for e in self._events:
            print(f"  {e}")


def main() -> None:
    clock = FakeClock(datetime(2026, 3, 1, 9, 0, tzinfo=timezone.utc))

    print("== Hotel stay: estimated auth, incremental auth, partial capture, refund ==")
    hotel = CardTransaction.authorize("txn-hotel-001", Money.parse("120.00", USD), clock, STANDARD,
                                      "check-in: estimated one night plus incidentals")
    clock.advance(timedelta(days=1))
    hotel.increment_authorization(Money.parse("30.00", USD), "minibar and late checkout")
    clock.advance(timedelta(hours=6))
    hotel.capture(Money.parse("100.00", USD), "folio: room night 100.00")
    hotel.close_capture("checkout complete; release the unused 50.00 hold")
    clock.advance(timedelta(days=3))
    hotel.refund(Money.parse("20.00", USD), "goodwill: noisy room complaint")
    hotel.print_events()
    print(f"  remaining authorization: {hotel.remaining_authorization()}")

    print("\n== Restaurant (MCC 5812): tip pushes the capture 12% above the authorization ==")
    tips = LifecyclePolicy.for_mcc("5812")
    dinner = CardTransaction.authorize("txn-dinner-002", Money.parse("80.00", USD), clock, tips, "pre-tip bill presented")
    print(f"  capture ceiling with 15% tolerance: {tips.capture_ceiling(dinner.authorized_amount)}")
    dinner.capture(Money.parse("89.60", USD), "bill 80.00 plus 9.60 tip")
    dinner.print_events()
    try:
        dinner.refund(Money.parse("100.00", USD), "customer asked for more than they paid")
    except LimitExceededError as exc:
        print(f"  rejected: {exc}")

    print("\n== Same merchant without the tolerance flag ==")
    strict = CardTransaction.authorize("txn-strict-003", Money.parse("80.00", USD), clock, STANDARD, "pre-tip bill presented")
    try:
        strict.capture(Money.parse("89.60", USD), "bill 80.00 plus 9.60 tip")
    except LimitExceededError as exc:
        print(f"  rejected: {exc}")

    print("\n== Forgotten authorization: capture attempted on day 8 ==")
    stale = CardTransaction.authorize("txn-stale-004", Money.parse("45.00", USD), clock, STANDARD, "online order placed")
    clock.advance(timedelta(days=8))
    try:
        stale.capture(Money.parse("45.00", USD), "order shipped")
    except IllegalTransitionError as exc:
        print(f"  rejected: {exc}")
    stale.print_events()

    print("\n== Illegal moves are exceptions, not silent no-ops ==")
    for attempt in (lambda: hotel.void_authorization("oops"), lambda: hotel.capture(Money.parse("1.00", USD), "one more")):
        try:
            attempt()
        except IllegalTransitionError as exc:
            print(f"  rejected: {exc}")


if __name__ == "__main__":
    main()
