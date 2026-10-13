"""Lesson: phases/10-money-and-ledgers/07-holds-and-available-balance/docs/en.md
Holds, Pending Transactions, and Available Balance
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: Visa Core Rules (authorization hold / incremental authorization / reversal); Regulation E 12 CFR 1005.
Money type copied from Phase 10, Lesson 01. Python 3.11+, standard library only. Same design as Java and Go.
"""

from __future__ import annotations

import random
import threading
from dataclasses import dataclass
from enum import Enum
from functools import total_ordering

# ---------------------------------------------------------------------------------------------
# Money (copied from Phase 10, Lesson 01). Integer minor units + ISO 4217 currency. Never a float.
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
JPY = Currency("JPY", 0)


class CurrencyMismatchError(ValueError):
    def __init__(self, a: Currency, b: Currency) -> None:
        super().__init__(f"cannot combine {a.code} with {b.code}")


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
    def zero(currency: Currency) -> "Money":
        return Money(0, currency)

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
            raise ArithmeticError(f"{text} has more decimals than {currency.code} allows ({currency.exponent})")
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

    def negate(self) -> "Money":
        return Money(-self.minor_units, self.currency)

    def is_zero(self) -> bool:
        return self.minor_units == 0

    def is_negative(self) -> bool:
        return self.minor_units < 0

    def is_positive(self) -> bool:
        return self.minor_units > 0

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


# ---------------------------------------------------------------------------------------------
# Time is injected. Nothing in this lesson reads the wall clock.
# ---------------------------------------------------------------------------------------------


class ManualClock:
    """A clock the test moves by hand, in whole seconds. Expiry is a function of this value only."""

    def __init__(self, start: int) -> None:
        self._now = start
        self._lock = threading.Lock()

    def now(self) -> int:
        with self._lock:
            return self._now

    def advance(self, seconds: int) -> None:
        if seconds < 0:
            raise ValueError("clocks do not run backwards in this lesson")
        with self._lock:
            self._now += seconds


# ---------------------------------------------------------------------------------------------
# Holds.
# ---------------------------------------------------------------------------------------------


class HoldStatus(Enum):
    ACTIVE = "ACTIVE"
    CAPTURED = "CAPTURED"
    VOIDED = "VOIDED"
    EXPIRED = "EXPIRED"


class Hold:
    """An authorization hold: `amount` is reserved until `expires_at`. Not a ledger posting."""

    def __init__(self, hold_id: str, amount: Money, created_at: int, expires_at: int) -> None:
        if not hold_id or not hold_id.strip():
            raise ValueError("hold id required")
        if not amount.is_positive():
            raise ValueError(f"hold amount must be positive, got {amount}")
        if expires_at <= created_at:
            raise ValueError("hold must expire after it is created")
        self.id = hold_id
        self.amount = amount
        self.created_at = created_at
        self.expires_at = expires_at
        self.status = HoldStatus.ACTIVE
        self.captured = Money.zero(amount.currency)

    def is_active(self) -> bool:
        return self.status is HoldStatus.ACTIVE

    def is_stale(self, now: int) -> bool:
        """True when the clock has passed the expiry and the hold is still marked active."""
        return self.status is HoldStatus.ACTIVE and now >= self.expires_at

    # Transitions. Only Account calls these, after it has checked the invariant.
    def _increment(self, extra: Money) -> None:
        self.amount = self.amount.plus(extra)

    def _mark_captured(self, captured: Money) -> None:
        self.captured = captured
        self.status = HoldStatus.CAPTURED

    def _mark_voided(self) -> None:
        self.status = HoldStatus.VOIDED

    def _mark_expired(self) -> None:
        self.status = HoldStatus.EXPIRED

    def __str__(self) -> str:
        tail = f" captured={self.captured}" if self.status is HoldStatus.CAPTURED else ""
        return f"{self.id} {self.amount} {self.status.value}{tail}"


class HoldError(Exception):
    pass


class InsufficientAvailableError(HoldError):
    def __init__(self, requested: Money, available: Money) -> None:
        super().__init__(f"requested {requested} but only {available} is available")


class OverCaptureError(HoldError):
    def __init__(self, hold_id: str, requested: Money, held: Money) -> None:
        super().__init__(f"hold {hold_id}: cannot capture {requested} against a hold of {held}")


class HoldNotActiveError(HoldError):
    def __init__(self, hold_id: str, status: HoldStatus) -> None:
        super().__init__(f"hold {hold_id} is {status.value}, not ACTIVE")


class UnknownHoldError(HoldError):
    def __init__(self, hold_id: str) -> None:
        super().__init__(f"unknown hold: {hold_id}")


class DuplicateHoldError(HoldError):
    def __init__(self, hold_id: str) -> None:
        super().__init__(f"hold id already used: {hold_id}")


class Account:
    """One account with a settled (ledger) balance and a set of holds.

    Invariant proved by the tests:
        available == ledger - sum(amount of ACTIVE holds)   and   available >= 0

    Every public method expires stale holds against the injected clock before it reads or writes.

    Every public method takes the account's lock. "amount <= available" and the insertion of the hold
    must be one atomic step: two threads that both read available = 100.00 and both insert a 100.00
    hold would leave available at -100.00. The lock is an RLock because authorize() calls
    available_balance() calls held_total() calls expire_stale(), all of which take it again.
    The GIL does not make this safe: the check and the insert are separate bytecodes.
    """

    def __init__(self, account_id: str, currency: Currency, clock: ManualClock) -> None:
        self.id = account_id
        self.currency = currency
        self._clock = clock
        self._ledger = Money.zero(currency)
        self._holds: dict[str, Hold] = {}
        self._events: list[str] = []
        self._lock = threading.RLock()

    def expire_stale(self) -> int:
        """Sweep: any ACTIVE hold whose expiry has passed becomes EXPIRED and stops reducing available."""
        with self._lock:
            now = self._clock.now()
            released = 0
            for h in self._holds.values():
                if h.is_stale(now):
                    h._mark_expired()
                    released += 1
                    self._events.append(f"t={now} EXPIRED {h.id} released {h.amount}")
            return released

    def ledger_balance(self) -> Money:
        """The settled balance. Holds never change this number."""
        with self._lock:
            return self._ledger

    def held_total(self) -> Money:
        with self._lock:
            self.expire_stale()
            total = Money.zero(self.currency)
            for h in self._holds.values():
                if h.is_active():
                    total = total.plus(h.amount)
            return total

    def available_balance(self) -> Money:
        """What may still be promised to someone else: ledger minus everything already promised."""
        with self._lock:
            return self._ledger.minus(self.held_total())

    def active_holds(self) -> list[Hold]:
        with self._lock:
            self.expire_stale()
            return [h for h in self._holds.values() if h.is_active()]

    def hold(self, hold_id: str) -> Hold:
        with self._lock:
            h = self._holds.get(hold_id)
            if h is None:
                raise UnknownHoldError(hold_id)
            return h

    def events(self) -> list[str]:
        with self._lock:
            return list(self._events)

    def credit(self, amount: Money) -> None:
        """A settled credit (deposit, incoming transfer, refund). Raises both ledger and available."""
        with self._lock:
            self._require_positive(amount)
            self._ledger = self._ledger.plus(amount)
            self._events.append(f"t={self._clock.now()} CREDIT {amount}")

    def debit(self, amount: Money) -> None:
        """A settled debit that was never authorized (a fee, a direct debit). Must fit inside available."""
        with self._lock:
            self._require_positive(amount)
            available = self.available_balance()
            if amount > available:
                raise InsufficientAvailableError(amount, available)
            self._ledger = self._ledger.minus(amount)
            self._events.append(f"t={self._clock.now()} DEBIT {amount}")

    def authorize(self, hold_id: str, amount: Money, ttl_seconds: int) -> Hold:
        """Place a hold. Rejected if it would push available below zero. The ledger does not move."""
        with self._lock:
            self._require_positive(amount)
            if hold_id in self._holds:
                raise DuplicateHoldError(hold_id)
            available = self.available_balance()
            if amount > available:
                raise InsufficientAvailableError(amount, available)
            now = self._clock.now()
            h = Hold(hold_id, amount, now, now + ttl_seconds)
            self._holds[hold_id] = h
            self._events.append(f"t={now} AUTHORIZE {hold_id} {amount} until t={h.expires_at}")
            return h

    def increment(self, hold_id: str, extra: Money) -> Hold:
        """Hold stacking by incremental authorization: the same hold grows, subject to the same check."""
        with self._lock:
            self._require_positive(extra)
            h = self._require_active(hold_id)
            available = self.available_balance()
            if extra > available:
                raise InsufficientAvailableError(extra, available)
            h._increment(extra)
            self._events.append(f"t={self._clock.now()} INCREMENT {hold_id} +{extra} -> {h.amount}")
            return h

    def capture(self, hold_id: str, amount: Money) -> Money:
        """Capture the final amount. Ledger moves by `amount`; the remainder of the hold is released."""
        with self._lock:
            self._require_positive(amount)
            h = self._require_active(hold_id)
            if amount > h.amount:
                raise OverCaptureError(hold_id, amount, h.amount)
            self._ledger = self._ledger.minus(amount)
            h._mark_captured(amount)
            released = h.amount.minus(amount)
            self._events.append(f"t={self._clock.now()} CAPTURE {hold_id} {amount} released {released}")
            return released

    def void_hold(self, hold_id: str) -> Money:
        """Void (authorization reversal): the hold is cancelled and its whole amount returns to available."""
        with self._lock:
            h = self._require_active(hold_id)
            h._mark_voided()
            self._events.append(f"t={self._clock.now()} VOID {hold_id} released {h.amount}")
            return h.amount

    def check_invariant(self) -> bool:
        """Recompute the invariant from scratch. Tests call this after every operation."""
        with self._lock:
            self.expire_stale()
            total = Money.zero(self.currency)
            for h in self._holds.values():
                if h.status is HoldStatus.ACTIVE:
                    total = total.plus(h.amount)
            available = self.available_balance()
            return available == self._ledger.minus(total) and not available.is_negative()

    def _require_active(self, hold_id: str) -> Hold:
        self.expire_stale()
        h = self.hold(hold_id)
        if not h.is_active():
            raise HoldNotActiveError(hold_id, h.status)
        return h

    def _require_positive(self, amount: Money) -> None:
        if amount.currency != self.currency:
            raise CurrencyMismatchError(self.currency, amount.currency)
        if not amount.is_positive():
            raise ValueError(f"amount must be positive, got {amount}")


HOUR = 3600
DAY = 24 * HOUR


def race_authorizations(account: Account, threads: int, amount: Money) -> tuple[int, int]:
    """Start `threads` threads that each try to place one hold of `amount` at the same instant.
    Returns (accepted, rejected). With 500.00 available and 50 x 20.00 requested, exactly 25 succeed;
    the lock in Account is what makes that "exactly", not "about"."""
    start = threading.Barrier(threads)
    accepted: list[int] = []
    rejected: list[int] = []
    tally = threading.Lock()

    def worker(i: int) -> None:
        start.wait()
        try:
            account.authorize(f"race-{i}", amount, 3600)
            with tally:
                accepted.append(i)
        except InsufficientAvailableError:
            with tally:
                rejected.append(i)

    workers = [threading.Thread(target=worker, args=(i,)) for i in range(threads)]
    for t in workers:
        t.start()
    for t in workers:
        t.join()
    return len(accepted), len(rejected)


def show(label: str, a: Account) -> None:
    print(f"{label:<44} ledger={str(a.ledger_balance()):<11} held={str(a.held_total()):<11} "
          f"available={str(a.available_balance()):<11} invariant={str(a.check_invariant()).lower()}")


def main() -> None:
    clock = ManualClock(1_700_000_000)  # fixed epoch; never the wall clock
    card = Account("debit-4242", USD, clock)

    print("== A debit card account: payroll lands, then two authorizations ==")
    card.credit(Money.parse("500.00", USD))
    show("payroll credit 500.00", card)
    card.authorize("auth-hotel", Money.parse("300.00", USD), 7 * DAY)
    show("hotel authorizes 300.00 (7-day hold)", card)
    card.authorize("auth-fuel", Money.parse("125.00", USD), 2 * HOUR)
    show("fuel pump pre-authorizes 125.00 (2-hour hold)", card)

    print("\n== Available, not ledger, is what the next request sees ==")
    try:
        card.debit(Money.parse("80.00", USD))
    except InsufficientAvailableError as e:
        print(f"rejected: {e}  (ledger still says 500.00)")

    print("\n== Partial capture releases the remainder in the same step ==")
    released = card.capture("auth-fuel", Money.parse("42.17", USD))
    print(f"fuel captures 42.17, released {released}")
    show("after fuel capture", card)

    print("\n== Expiry: the clock moves 8 days; nobody captured the hotel hold ==")
    clock.advance(8 * DAY)
    show("8 days later (hotel hold expired on read)", card)
    try:
        card.capture("auth-hotel", Money.parse("300.00", USD))
    except HoldNotActiveError as e:
        print(f"rejected: {e}")

    print("\n== Hold stacking: a new hotel authorization, then an incremental ==")
    card.authorize("auth-hotel-2", Money.parse("310.00", USD), 7 * DAY)
    card.increment("auth-hotel-2", Money.parse("45.00", USD))
    show("310.00 + 45.00 incremental", card)
    try:
        card.capture("auth-hotel-2", Money.parse("400.00", USD))
    except OverCaptureError as e:
        print(f"rejected: {e}")
    card.capture("auth-hotel-2", Money.parse("355.00", USD))
    show("hotel captures the full 355.00", card)

    print("\n== Void: an authorization reversal returns the whole amount ==")
    card.authorize("auth-coffee", Money.parse("4.50", USD), DAY)
    show("coffee authorizes 4.50", card)
    card.void_hold("auth-coffee")
    show("coffee voided", card)

    print("\n== Concurrency: 50 threads race to authorize 20.00 against 500.00 ==")
    shared = Account("shared", USD, clock)
    shared.credit(Money.parse("500.00", USD))
    accepted, rejected = race_authorizations(shared, 50, Money.parse("20.00", USD))
    print(f"accepted={accepted} rejected={rejected} available={shared.available_balance()} "
          f"invariant={str(shared.check_invariant()).lower()}")

    print("\n== Event log ==")
    for e in card.events():
        print(e)
    print(f"\nfinal: {card.ledger_balance()} ledger, {card.available_balance()} available, "
          f"invariant={str(card.check_invariant()).lower()}")


if __name__ == "__main__":
    main()
