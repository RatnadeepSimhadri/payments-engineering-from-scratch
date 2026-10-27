"""Lesson: phases/12-payment-systems-design/11-recurring-and-subscriptions/docs/en.md
Recurring Payments, Subscriptions, and Dunning
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: Visa/Mastercard stored-credential (credential-on-file) framework; Visa Account Updater;
         Stripe Billing docs (subscription statuses, proration).
Python 3.11+, standard library only. Same design as the Java version.
"""

from __future__ import annotations

import calendar
from collections import deque
from dataclasses import dataclass
from datetime import date, timedelta
from enum import Enum
from typing import Optional, Protocol


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
JPY = Currency("JPY", 0)


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

    @staticmethod
    def parse(text: str, currency: Currency) -> "Money":
        s = text.strip()
        negative = s.startswith("-")
        if negative or s.startswith("+"):
            s = s[1:]
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

    def is_positive(self) -> bool:
        return self.minor_units > 0

    def is_negative(self) -> bool:
        return self.minor_units < 0

    def is_zero(self) -> bool:
        return self.minor_units == 0

    def allocate(self, *ratios: int) -> list["Money"]:
        """Split by ratios with the largest-remainder method: shares sum to exactly this amount.
        Ties in remainder go to the lower index (stable sort), so results are deterministic."""
        if not ratios:
            raise ValueError("need at least one ratio")
        if any(r < 0 for r in ratios):
            raise ValueError("ratios must be non-negative")
        total = sum(ratios)
        if total == 0:
            raise ValueError("ratios must not all be zero")
        magnitude = abs(self.minor_units)
        shares = [(magnitude * r) // total for r in ratios]
        remainders = [(magnitude * r) % total for r in ratios]
        leftover = magnitude - sum(shares)
        order = sorted(range(len(ratios)), key=lambda i: -remainders[i])  # stable: ties keep index order
        for k in range(leftover):
            shares[order[k]] += 1
        sign = -1 if self.minor_units < 0 else 1
        return [Money(sign * s, self.currency) for s in shares]

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
# Time is injected. Billing runs at day granularity on the merchant's calendar.
# ---------------------------------------------------------------------------------------------


class Clock(Protocol):
    def today(self) -> date: ...


class FakeClock:
    def __init__(self, start: date) -> None:
        self._today = start

    def today(self) -> date:
        return self._today

    def advance_days(self, days: int) -> None:
        self._today = self._today + timedelta(days=days)

    def set(self, d: date) -> None:
        self._today = d


def add_months(d: date, months: int) -> date:
    """java.time's plusMonths: the day of month clamps to the target month's last day (Jan 31 + 1 = Feb 28)."""
    month_index = d.year * 12 + (d.month - 1) + months
    year, month = divmod(month_index, 12)
    month += 1
    last = calendar.monthrange(year, month)[1]
    return date(year, month, min(d.day, last))


# ---------------------------------------------------------------------------------------------
# Billing cycles: every period boundary is computed from the anchor, never from the previous boundary.
# ---------------------------------------------------------------------------------------------


class Interval(Enum):
    MONTHLY = 1
    QUARTERLY = 3
    YEARLY = 12

    @property
    def months(self) -> int:
        return self.value


@dataclass(frozen=True)
class Plan:
    id: str
    price: Money
    interval: Interval

    def __post_init__(self) -> None:
        if self.price.is_negative():
            raise ValueError("price must be non-negative")


class BillingCycle:
    @staticmethod
    def period_start(anchor: date, interval: Interval, n: int) -> date:
        """Start of period n (n = 0 is the anchor). add_months clamps to month end, e.g. Jan 31 + 1 month = Feb 28."""
        if n < 0:
            raise ValueError("period index must be >= 0")
        return add_months(anchor, n * interval.months)

    @staticmethod
    def period_end(anchor: date, interval: Interval, n: int) -> date:
        return BillingCycle.period_start(anchor, interval, n + 1)

    @staticmethod
    def chained_starts(anchor: date, interval: Interval, count: int) -> list[date]:
        """The bug this lesson warns about: each boundary computed from the previous one drifts after a short month."""
        out: list[date] = []
        d = anchor
        for _ in range(count):
            out.append(d)
            d = add_months(d, interval.months)
        return out

    @staticmethod
    def anchored_starts(anchor: date, interval: Interval, count: int) -> list[date]:
        return [BillingCycle.period_start(anchor, interval, i) for i in range(count)]


# ---------------------------------------------------------------------------------------------
# Proration: unused time on the old plan is credited, remaining time on the new plan is charged.
# ---------------------------------------------------------------------------------------------


@dataclass(frozen=True)
class ProrationResult:
    used_days: int
    remaining_days: int
    period_days: int
    credit: Money
    charge: Money

    def net(self) -> Money:
        return self.charge.minus(self.credit)


class Proration:
    @staticmethod
    def compute(old_plan: Plan, new_plan: Plan, period_start: date, period_end: date, change_date: date) -> ProrationResult:
        """Day-based proration with exact allocation. The old price is split used:remaining with
        largest-remainder allocation, so used_portion + credit == old price to the minor unit."""
        if change_date < period_start or not change_date < period_end:
            raise ValueError(f"change date {change_date} outside period [{period_start}, {period_end})")
        if old_plan.price.currency != new_plan.price.currency:
            raise CurrencyMismatchError(old_plan.price.currency, new_plan.price.currency)
        period = (period_end - period_start).days
        used = (change_date - period_start).days
        remaining = period - used
        credit = old_plan.price.allocate(used, remaining)[1]
        charge = new_plan.price.allocate(used, remaining)[1]
        return ProrationResult(used, remaining, period, credit, charge)


# ---------------------------------------------------------------------------------------------
# The subscription state machine.
# ---------------------------------------------------------------------------------------------


class SubscriptionStatus(Enum):
    TRIALING = "trialing"
    ACTIVE = "active"
    PAST_DUE = "past_due"
    UNPAID = "unpaid"
    CANCELED = "canceled"

    def __str__(self) -> str:
        return self.name


class IllegalTransitionError(RuntimeError):
    def __init__(self, from_state: SubscriptionStatus, to_state: SubscriptionStatus) -> None:
        super().__init__(f"illegal transition {from_state.name} -> {to_state.name}")


class SubscriptionTransitions:
    ALLOWED: dict[SubscriptionStatus, frozenset[SubscriptionStatus]] = {
        SubscriptionStatus.TRIALING: frozenset({SubscriptionStatus.ACTIVE, SubscriptionStatus.PAST_DUE, SubscriptionStatus.CANCELED}),
        SubscriptionStatus.ACTIVE: frozenset({SubscriptionStatus.PAST_DUE, SubscriptionStatus.CANCELED}),
        SubscriptionStatus.PAST_DUE: frozenset({SubscriptionStatus.ACTIVE, SubscriptionStatus.UNPAID, SubscriptionStatus.CANCELED}),
        SubscriptionStatus.UNPAID: frozenset({SubscriptionStatus.ACTIVE, SubscriptionStatus.CANCELED}),
        SubscriptionStatus.CANCELED: frozenset(),
    }

    @staticmethod
    def allowed(from_state: SubscriptionStatus, to_state: SubscriptionStatus) -> bool:
        return to_state in SubscriptionTransitions.ALLOWED[from_state]


class Subscription:
    def __init__(self, id: str, customer_id: str, plan: Plan, card_token: str, today: date, anchor: date) -> None:
        self.id = id
        self.customer_id = customer_id
        self.plan = plan
        self.card_token = card_token
        self.anchor = anchor
        self.status = SubscriptionStatus.TRIALING if anchor > today else SubscriptionStatus.ACTIVE
        self.period_index = -1  # -1 while trialing: period 0 starts at the anchor
        self.current_period_start = today
        self.current_period_end = anchor  # the next billing date
        self.cancel_at_period_end = False
        self.credit_balance = Money.zero(plan.price.currency)

    def transition(self, to: SubscriptionStatus) -> None:
        if self.status is to:
            return
        if not SubscriptionTransitions.allowed(self.status, to):
            raise IllegalTransitionError(self.status, to)
        self.status = to

    def bills(self) -> bool:
        return self.status is not SubscriptionStatus.CANCELED and self.status is not SubscriptionStatus.UNPAID


# ---------------------------------------------------------------------------------------------
# Invoices, the gateway, and the card-updater hook.
# ---------------------------------------------------------------------------------------------


class InvoiceStatus(Enum):
    OPEN = "open"
    PAID = "paid"
    UNCOLLECTIBLE = "uncollectible"


class ChargeOutcome(Enum):
    APPROVED = "approved"
    INSUFFICIENT_FUNDS = "insufficient_funds"
    DO_NOT_HONOR = "do_not_honor"
    EXPIRED_CARD = "expired_card"
    DO_NOT_RETRY = "do_not_retry"

    def retryable(self) -> bool:
        return self in (ChargeOutcome.INSUFFICIENT_FUNDS, ChargeOutcome.DO_NOT_HONOR)

    def __str__(self) -> str:
        return self.name


class Invoice:
    def __init__(self, id: str, subscription_id: str, period_start: date, period_end: date, amount: Money,
                 description: str, due: date) -> None:
        self.id = id
        self.subscription_id = subscription_id
        self.period_start = period_start
        self.period_end = period_end
        self.amount = amount
        self.description = description
        self.status = InvoiceStatus.OPEN
        self.attempts = 0
        self.first_failure: Optional[date] = None
        self.next_attempt: Optional[date] = due
        self.last_outcome: Optional[ChargeOutcome] = None


class ChargeGateway(Protocol):
    """Merchant-initiated charge on a stored credential; the reference is the gateway's idempotency key."""

    def charge(self, reference: str, card_token: str, amount: Money) -> ChargeOutcome: ...


class CardUpdater(Protocol):
    """Visa Account Updater / Mastercard ABU style: given an old credential, maybe a replacement."""

    def refresh(self, customer_id: str, old_card_token: str) -> Optional[str]: ...


@dataclass(frozen=True)
class DunningPolicy:
    retry_offsets_days: tuple[int, ...]
    when_exhausted: SubscriptionStatus

    def __post_init__(self) -> None:
        object.__setattr__(self, "retry_offsets_days", tuple(self.retry_offsets_days))
        offsets = self.retry_offsets_days
        for i, off in enumerate(offsets):
            if off <= 0 or (i > 0 and off <= offsets[i - 1]):
                raise ValueError(f"retry offsets must be positive and strictly increasing: {list(offsets)}")
        if self.when_exhausted not in (SubscriptionStatus.CANCELED, SubscriptionStatus.UNPAID):
            raise ValueError("exhausted dunning ends CANCELED or UNPAID")

    @staticmethod
    def standard() -> "DunningPolicy":
        return DunningPolicy((1, 3, 7, 14), SubscriptionStatus.CANCELED)

    def max_attempts(self) -> int:
        return 1 + len(self.retry_offsets_days)


# ---------------------------------------------------------------------------------------------
# The engine: one run_day() per calendar day, safe to call more than once on the same day.
# ---------------------------------------------------------------------------------------------


class BillingEngine:
    def __init__(self, clock: Clock, gateway: ChargeGateway, updater: CardUpdater, policy: DunningPolicy) -> None:
        self._clock = clock
        self._gateway = gateway
        self._updater = updater
        self._policy = policy
        self._subscriptions: dict[str, Subscription] = {}
        self._invoices: dict[str, Invoice] = {}
        self._log: list[str] = []

    def log(self) -> list[str]:
        return self._log

    def subscription(self, id: str) -> Subscription:
        return self._subscriptions[id]

    def invoice(self, id: str) -> Optional[Invoice]:
        return self._invoices.get(id)

    def invoices(self) -> list[Invoice]:
        return list(self._invoices.values())

    def subscribe(self, id: str, customer_id: str, plan: Plan, card_token: str, trial_days: int) -> Subscription:
        if trial_days < 0:
            raise ValueError("trial_days must be >= 0")
        if id in self._subscriptions:
            raise RuntimeError(f"duplicate subscription {id}")
        today = self._clock.today()
        s = Subscription(id, customer_id, plan, card_token, today, today + timedelta(days=trial_days))
        self._subscriptions[id] = s
        self._note(f"{s.id} created {s.status.name}, anchor {s.anchor}")
        return s

    def cancel(self, subscription_id: str, at_period_end: bool) -> None:
        s = self._subscriptions[subscription_id]
        if at_period_end:
            s.cancel_at_period_end = True
            self._note(f"{s.id} will cancel at {s.current_period_end}")
        else:
            s.transition(SubscriptionStatus.CANCELED)
            self._note(f"{s.id} canceled now")

    def change_plan(self, subscription_id: str, new_plan: Plan) -> ProrationResult:
        """Immediate plan change with proration. A positive net is invoiced now; a negative net becomes credit."""
        s = self._subscriptions[subscription_id]
        if s.status is not SubscriptionStatus.ACTIVE:
            raise RuntimeError(f"plan changes require ACTIVE, was {s.status.name}")
        if new_plan.interval is not s.plan.interval:
            raise ValueError("interval changes reset the anchor; not modeled")
        today = self._clock.today()
        p = Proration.compute(s.plan, new_plan, s.current_period_start, s.current_period_end, today)
        old = s.plan
        s.plan = new_plan
        self._note(f"{s.id} {old.id} -> {new_plan.id}: credit {p.credit}, charge {p.charge}, net {p.net()}")
        if p.net().is_positive():
            inv = self._open_invoice(f"{s.id}:proration:{today}", s, s.current_period_start, s.current_period_end, p.net(), "proration", today)
            self._attempt(inv, s, today)
        elif p.net().is_negative():
            s.credit_balance = s.credit_balance.minus(p.net())
        return p

    def run_day(self) -> None:
        """Run everything due today. Idempotent: a second call on the same day changes nothing."""
        today = self._clock.today()
        for s in self._subscriptions.values():
            while s.bills() and not today < s.current_period_end:
                if s.cancel_at_period_end:
                    s.transition(SubscriptionStatus.CANCELED)
                    self._note(f"{s.id} canceled at period end")
                    break
                self._roll_period(s)
        for inv in list(self._invoices.values()):
            if inv.status is InvoiceStatus.OPEN and inv.next_attempt is not None and not today < inv.next_attempt:
                s = self._subscriptions[inv.subscription_id]
                if s.status is SubscriptionStatus.CANCELED:
                    continue
                self._attempt(inv, s, today)

    def _roll_period(self, s: Subscription) -> None:
        s.period_index += 1
        s.current_period_start = BillingCycle.period_start(s.anchor, s.plan.interval, s.period_index)
        s.current_period_end = BillingCycle.period_end(s.anchor, s.plan.interval, s.period_index)
        due = s.plan.price
        applied = due if due.compare_to(s.credit_balance) < 0 else s.credit_balance
        s.credit_balance = s.credit_balance.minus(applied)
        amount = due.minus(applied)
        inv_id = f"{s.id}:{s.current_period_start}"
        inv = self._open_invoice(inv_id, s, s.current_period_start, s.current_period_end, amount, s.plan.id, s.current_period_start)
        if amount.is_zero():
            inv.status = InvoiceStatus.PAID
            inv.next_attempt = None
            self._note(f"{inv_id} covered by credit")

    def _open_invoice(self, id: str, s: Subscription, start: date, end: date, amount: Money, description: str, due: date) -> Invoice:
        existing = self._invoices.get(id)
        if existing is not None:
            return existing
        inv = Invoice(id, s.id, start, end, amount, description, due)
        self._invoices[id] = inv
        self._note(f"invoice {id} {amount} for [{start}, {end})")
        return inv

    def _attempt(self, inv: Invoice, s: Subscription, today: date) -> None:
        inv.attempts += 1
        reference = f"{inv.id}#{inv.attempts}"
        outcome = self._gateway.charge(reference, s.card_token, inv.amount)
        if outcome is ChargeOutcome.EXPIRED_CARD:
            fresh = self._updater.refresh(s.customer_id, s.card_token)
            if fresh is not None and fresh != s.card_token:
                self._note(f"{reference} EXPIRED_CARD; card updater {s.card_token} -> {fresh}")
                s.card_token = fresh
                inv.attempts += 1
                reference = f"{inv.id}#{inv.attempts}"
                outcome = self._gateway.charge(reference, s.card_token, inv.amount)
        inv.last_outcome = outcome
        if outcome is ChargeOutcome.APPROVED:
            inv.status = InvoiceStatus.PAID
            inv.next_attempt = None
            if s.status is not SubscriptionStatus.ACTIVE:
                s.transition(SubscriptionStatus.ACTIVE)
            self._note(f"{reference} APPROVED {inv.amount} -> {s.id} {s.status.name}")
            return
        if inv.first_failure is None:
            inv.first_failure = today
        if s.status in (SubscriptionStatus.ACTIVE, SubscriptionStatus.TRIALING):
            s.transition(SubscriptionStatus.PAST_DUE)
        retries_used = self._retries_used(inv)
        exhausted = not outcome.retryable() or retries_used >= len(self._policy.retry_offsets_days)
        if exhausted:
            inv.status = InvoiceStatus.UNCOLLECTIBLE
            inv.next_attempt = None
            s.transition(self._policy.when_exhausted)
            self._note(f"{reference} {outcome.name}; dunning exhausted -> invoice UNCOLLECTIBLE, {s.id} {s.status.name}")
            return
        inv.next_attempt = inv.first_failure + timedelta(days=self._policy.retry_offsets_days[retries_used])
        self._note(f"{reference} {outcome.name} -> {s.id} {s.status.name}, next attempt {inv.next_attempt}")

    def _retries_used(self, inv: Invoice) -> int:
        """Scheduled retries consumed so far: every dunning-day attempt after the first failure day."""
        assert inv.first_failure is not None
        today = self._clock.today()
        return sum(1 for offset in self._policy.retry_offsets_days if not today < inv.first_failure + timedelta(days=offset))

    def _note(self, line: str) -> None:
        self._log.append(f"{self._clock.today()}  {line}")


# ---------------------------------------------------------------------------------------------
# Fakes: a scripted gateway that dedupes by reference, and a card updater backed by a map.
# ---------------------------------------------------------------------------------------------


class ScriptedGateway:
    def __init__(self) -> None:
        self._scripts: dict[str, deque[ChargeOutcome]] = {}
        self._by_reference: dict[str, ChargeOutcome] = {}
        self._charges: list[str] = []

    def script(self, card_token: str, *outcomes: ChargeOutcome) -> None:
        self._scripts[card_token] = deque(outcomes)

    def charge(self, reference: str, card_token: str, amount: Money) -> ChargeOutcome:
        prior = self._by_reference.get(reference)
        if prior is not None:
            return prior
        q = self._scripts.get(card_token)
        outcome = q.popleft() if q else ChargeOutcome.APPROVED
        self._by_reference[reference] = outcome
        self._charges.append(f"{reference} {card_token} {amount} {outcome.name}")
        return outcome

    def charges(self) -> list[str]:
        return self._charges

    def approved_count(self) -> int:
        return sum(1 for c in self._charges if c.endswith("APPROVED"))


class MapCardUpdater:
    def __init__(self) -> None:
        self._replacements: dict[str, str] = {}
        self._calls = 0

    def replace(self, old_token: str, new_token: str) -> None:
        self._replacements[old_token] = new_token

    def calls(self) -> int:
        return self._calls

    def refresh(self, customer_id: str, old_card_token: str) -> Optional[str]:
        self._calls += 1
        return self._replacements.get(old_card_token)


# ---------------------------------------------------------------------------------------------
# Demo
# ---------------------------------------------------------------------------------------------

BASIC = Plan("basic", Money.parse("30.00", USD), Interval.MONTHLY)
PRO = Plan("pro", Money.parse("50.00", USD), Interval.MONTHLY)


def _dates(ds: list[date]) -> str:
    return "[" + ", ".join(str(d) for d in ds) + "]"


def main() -> None:
    print("== Anchored billing dates vs chained dates (anchor 2026-01-31) ==")
    jan31 = date(2026, 1, 31)
    print(f"anchored: {_dates(BillingCycle.anchored_starts(jan31, Interval.MONTHLY, 6))}")
    print(f"chained:  {_dates(BillingCycle.chained_starts(jan31, Interval.MONTHLY, 6))}  <- drifted to the 28th forever")

    print("\n== Proration: basic 30.00 -> pro 50.00 on 2026-03-11, period [2026-03-01, 2026-04-01) ==")
    p = Proration.compute(BASIC, PRO, date(2026, 3, 1), date(2026, 4, 1), date(2026, 3, 11))
    print(f"used {p.used_days} / remaining {p.remaining_days} / period {p.period_days} days")
    basic_split = BASIC.price.allocate(p.used_days, p.remaining_days)
    print(f"basic split used:unused = {basic_split[0]} + {basic_split[1]} = {basic_split[0].plus(basic_split[1])}")
    print(f"credit {p.credit}, charge {p.charge}, net due now {p.net()}")
    odd = Plan("odd", Money.parse("9.99", USD), Interval.MONTHLY)
    halves = odd.price.allocate(15, 15)
    print(f"9.99 split 15:15 days = {halves[0]} + {halves[1]} (independent half-up rounding would give 5.00 + 5.00 = 10.00)")

    print("\n== Billing engine: trial, renewals, dunning, card updater ==")
    clock = FakeClock(date(2026, 1, 31))
    gateway = ScriptedGateway()
    updater = MapCardUpdater()
    engine = BillingEngine(clock, gateway, updater, DunningPolicy.standard())

    gateway.script("tok_flaky", ChargeOutcome.APPROVED, ChargeOutcome.INSUFFICIENT_FUNDS, ChargeOutcome.INSUFFICIENT_FUNDS, ChargeOutcome.APPROVED)
    gateway.script("tok_old", ChargeOutcome.APPROVED, ChargeOutcome.EXPIRED_CARD)
    updater.replace("tok_old", "tok_reissued")
    gateway.script("tok_broke", ChargeOutcome.INSUFFICIENT_FUNDS, ChargeOutcome.INSUFFICIENT_FUNDS, ChargeOutcome.INSUFFICIENT_FUNDS,
                   ChargeOutcome.INSUFFICIENT_FUNDS, ChargeOutcome.INSUFFICIENT_FUNDS)

    engine.subscribe("sub_flaky", "cus_a", BASIC, "tok_flaky", 0)
    engine.subscribe("sub_updater", "cus_b", BASIC, "tok_old", 0)
    engine.subscribe("sub_trial", "cus_c", PRO, "tok_broke", 14)
    for _ in range(75):
        engine.run_day()
        engine.run_day()  # a second run on the same day must be a no-op
        clock.advance_days(1)
    for line in engine.log():
        print(line)

    print("\n== Final state ==")
    for sid in ("sub_flaky", "sub_updater", "sub_trial"):
        s = engine.subscription(sid)
        print(f"{sid:<12} {s.status.name:<9} card={s.card_token:<13} next bill {s.current_period_end}")
    print(f"gateway calls = {len(gateway.charges())}, approved = {gateway.approved_count()}")


if __name__ == "__main__":
    main()
