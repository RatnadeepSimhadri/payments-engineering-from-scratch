"""Lesson: phases/12-payment-systems-design/06-refunds-and-partial-refunds/docs/en.md
Refunds, Partial Refunds, and Refund Windows
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: Visa Core Rules (credit transaction processing); Stripe API reference (Refund object, amount_refunded);
         Pacioli, "Summa de arithmetica" (1494) for the double-entry rule.
Python 3.11+, standard library only. Same design as the Java version.
"""

from __future__ import annotations

import threading
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

    def is_zero(self) -> bool:
        return self.minor_units == 0

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
# Time is injected as a calendar date: refund windows are counted in days, and tests move the
# date rather than waiting for it.
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


# ---------------------------------------------------------------------------------------------
# A minimal double-entry ledger (Phase 10, Lesson 03): balanced entries, append-only, reversals
# instead of edits. Every refund movement below is one of these entries.
# ---------------------------------------------------------------------------------------------


class Side(Enum):
    DEBIT = "debit"
    CREDIT = "credit"

    def opposite(self) -> "Side":
        return Side.CREDIT if self is Side.DEBIT else Side.DEBIT


class AccountType(Enum):
    # (name, normal side): the name keeps members with the same normal side from collapsing into aliases.
    ASSET = ("asset", Side.DEBIT)
    LIABILITY = ("liability", Side.CREDIT)
    REVENUE = ("revenue", Side.CREDIT)
    EXPENSE = ("expense", Side.DEBIT)

    def __init__(self, _label: str, normal: Side) -> None:
        self.normal = normal


@dataclass(frozen=True)
class Posting:
    account: str
    side: Side
    amount: Money

    def __post_init__(self) -> None:
        if not self.amount.is_positive():
            raise ValueError(f"posting amount must be positive: {self.amount}")

    @staticmethod
    def debit(account: str, amount: Money) -> "Posting":
        return Posting(account, Side.DEBIT, amount)

    @staticmethod
    def credit(account: str, amount: Money) -> "Posting":
        return Posting(account, Side.CREDIT, amount)

    def __str__(self) -> str:
        return ("  Dr " if self.side is Side.DEBIT else "      Cr ") + f"{self.account}  {self.amount}"


@dataclass(frozen=True)
class JournalEntry:
    id: str
    description: str
    postings: tuple[Posting, ...]

    def __post_init__(self) -> None:
        object.__setattr__(self, "postings", tuple(self.postings))
        if len(self.postings) < 2:
            raise ValueError(f"entry {self.id} needs at least two postings")
        net = sum(p.amount.minor_units if p.side is Side.DEBIT else -p.amount.minor_units for p in self.postings)
        if net != 0:
            raise ValueError(f"entry {self.id} is unbalanced by {net} minor units")

    def reversal(self, new_id: str, why: str) -> "JournalEntry":
        flipped = tuple(Posting(p.account, p.side.opposite(), p.amount) for p in self.postings)
        return JournalEntry(new_id, f"{why} (reverses {self.id})", flipped)


class Ledger:
    def __init__(self, currency: Currency) -> None:
        self._accounts: dict[str, AccountType] = {}
        self._journal: list[JournalEntry] = []
        self._balances: dict[str, int] = {}
        self._currency = currency

    def open(self, account: str, type_: AccountType) -> None:
        self._accounts[account] = type_
        self._balances[account] = 0

    def post(self, e: JournalEntry) -> None:
        for p in e.postings:
            if p.account not in self._accounts:
                raise ValueError(f"unknown account {p.account}")
            if p.amount.currency != self._currency:
                raise CurrencyMismatchError(self._currency, p.amount.currency)
        self._journal.append(e)
        for p in e.postings:
            signed = p.amount.minor_units if p.side is self._accounts[p.account].normal else -p.amount.minor_units
            self._balances[p.account] += signed

    def balance(self, account: str) -> Money:
        """Balance on the account's normal side: a positive liability is money we owe."""
        return Money.of_minor(self._balances[account], self._currency)

    def balances(self) -> bool:
        """Sum of debit-normal balances equals sum of credit-normal balances, or the ledger is corrupt."""
        debit_side = sum(self._balances[a] for a, t in self._accounts.items() if t.normal is Side.DEBIT)
        credit_side = sum(self._balances[a] for a, t in self._accounts.items() if t.normal is Side.CREDIT)
        return debit_side == credit_side

    def journal(self) -> tuple[JournalEntry, ...]:
        return tuple(self._journal)

    def accounts(self) -> dict[str, AccountType]:
        return dict(self._accounts)


# ---------------------------------------------------------------------------------------------
# Captures and refunds.
# ---------------------------------------------------------------------------------------------


@dataclass(frozen=True)
class Capture:
    """Settled money a refund can be issued against. The fee is what the processor kept on capture."""

    id: str
    payment_id: str
    amount: Money
    fee: Money
    captured_on: date


class RefundState(Enum):
    PENDING = "pending"
    SUCCEEDED = "succeeded"
    FAILED = "failed"

    def __str__(self) -> str:
        return self.name


class FeeRefundPolicy(Enum):
    """Whether the processor gives back its fee when a payment is refunded. Contracts differ; the ledger must not care."""

    NONE = "none"
    PROPORTIONAL = "proportional"


@dataclass(frozen=True)
class RefundPolicy:
    window_days: int
    fee_policy: FeeRefundPolicy

    def __post_init__(self) -> None:
        if self.window_days < 0:
            raise ValueError("window_days must be >= 0")


class Refund:
    def __init__(self, id: str, capture_id: str, amount: Money, reason: str, requested_on: date) -> None:
        self.id = id
        self.capture_id = capture_id
        self.amount = amount
        self.reason = reason
        self.requested_on = requested_on
        self.state = RefundState.PENDING
        self.fee_returned = Money.zero(amount.currency)
        self.failure_reason: Optional[str] = None

    def __str__(self) -> str:
        return f"{self.id:<8} {self.state.name:<10} {str(self.amount):<12} fee back {str(self.fee_returned):<10} {self.reason}"


class RefundError(RuntimeError):
    pass


class OverRefundError(RefundError):
    pass


class RefundWindowClosedError(RefundError):
    pass


class RefundIdempotencyConflictError(RefundError):
    pass


@dataclass(frozen=True)
class RefundResult:
    refund: Refund
    replayed: bool


class RefundService:
    """The refund service. Every public method holds one lock: the check "does the remaining balance cover
    this refund" and the reservation that consumes it must be one atomic step, or two concurrent partial
    refunds can both pass the check. In a database this is one conditional UPDATE (see the docs)."""

    SCHEME = "scheme_settlement"    # asset: what the card scheme owes / has paid us
    MERCHANT = "merchant_payable"   # liability: what we owe the merchant
    REFUNDS = "refunds_payable"     # liability: refunds promised, not yet sent
    FEES = "fee_revenue"            # revenue: processing fees we kept

    def __init__(self, ledger: Ledger, clock: Clock, policy: RefundPolicy) -> None:
        self._ledger = ledger
        self._clock = clock
        self._policy = policy
        self._captures: dict[str, Capture] = {}
        self._refunds: dict[str, Refund] = {}
        self._refunds_by_capture: dict[str, list[Refund]] = {}
        self._entry_seq = 0
        self._lock = threading.RLock()
        ledger.open(RefundService.SCHEME, AccountType.ASSET)
        ledger.open(RefundService.MERCHANT, AccountType.LIABILITY)
        ledger.open(RefundService.REFUNDS, AccountType.LIABILITY)
        ledger.open(RefundService.FEES, AccountType.REVENUE)

    def _next_entry_id(self) -> str:
        self._entry_seq += 1
        return f"je_{self._entry_seq:03d}"

    def capture(self, capture_id: str, payment_id: str, amount: Money, fee_bps: int, fixed_fee_minor: int) -> Capture:
        """Record a capture: the scheme owes us the gross, we owe the merchant the net, and we keep the fee."""
        with self._lock:
            if capture_id in self._captures:
                raise RuntimeError(f"capture exists: {capture_id}")
            if not amount.is_positive():
                raise ValueError("capture amount must be positive")
            variable = (amount.minor_units * fee_bps + 5_000) // 10_000
            fee = Money.of_minor(variable + fixed_fee_minor, amount.currency)
            c = Capture(capture_id, payment_id, amount, fee, self._clock.today())
            self._ledger.post(JournalEntry(self._next_entry_id(), f"capture {capture_id}", (
                Posting.debit(RefundService.SCHEME, amount),
                Posting.credit(RefundService.MERCHANT, amount.minus(fee)),
                Posting.credit(RefundService.FEES, fee))))
            self._captures[capture_id] = c
            self._refunds_by_capture[capture_id] = []
            return c

    def refunded(self, capture_id: str) -> Money:
        with self._lock:
            return self._sum(capture_id, RefundState.SUCCEEDED)

    def pending(self, capture_id: str) -> Money:
        with self._lock:
            return self._sum(capture_id, RefundState.PENDING)

    def remaining_refundable(self, capture_id: str) -> Money:
        """captured - succeeded - pending. A pending refund already consumes balance: that is what stops the race."""
        with self._lock:
            c = self._capture(capture_id)
            return c.amount.minus(self.refunded(capture_id)).minus(self.pending(capture_id))

    def fee_returned(self, capture_id: str) -> Money:
        with self._lock:
            total = Money.zero(self._capture(capture_id).amount.currency)
            for r in self._refunds_by_capture[capture_id]:
                total = total.plus(r.fee_returned)
            return total

    def refund(self, refund_id: str) -> Refund:
        with self._lock:
            r = self._refunds.get(refund_id)
            if r is None:
                raise ValueError(f"unknown refund {refund_id}")
            return r

    def refunds_for(self, capture_id: str) -> tuple[Refund, ...]:
        with self._lock:
            return tuple(self._refunds_by_capture[self._capture(capture_id).id])

    def _capture(self, capture_id: str) -> Capture:
        c = self._captures.get(capture_id)
        if c is None:
            raise ValueError(f"unknown capture {capture_id}")
        return c

    def _sum(self, capture_id: str, state: RefundState) -> Money:
        total = Money.zero(self._capture(capture_id).amount.currency)
        for r in self._refunds_by_capture[capture_id]:
            if r.state is state:
                total = total.plus(r.amount)
        return total

    def request_refund(self, refund_id: str, capture_id: str, amount: Money, reason: str) -> RefundResult:
        """Request a refund. Checks, in order: idempotency (same id), amount positive and same currency, the
        refund window, and the remaining refundable balance. Only after every check passes does anything
        change: the refund is recorded PENDING and the merchant's balance is moved into refunds_payable."""
        with self._lock:
            existing = self._refunds.get(refund_id)
            if existing is not None:
                if existing.capture_id == capture_id and existing.amount == amount:
                    return RefundResult(existing, True)
                raise RefundIdempotencyConflictError(
                    f"refund id {refund_id} was already used for {existing.amount} on {existing.capture_id}")
            c = self._capture(capture_id)
            if not amount.is_positive():
                raise ValueError(f"refund amount must be positive: {amount}")
            if amount.currency != c.amount.currency:
                raise CurrencyMismatchError(c.amount.currency, amount.currency)
            deadline = c.captured_on + timedelta(days=self._policy.window_days)
            today = self._clock.today()
            if today > deadline:
                raise RefundWindowClosedError(
                    f"capture {capture_id} on {c.captured_on}: refund window of {self._policy.window_days} days closed on "
                    f"{deadline} (today {today}, {(today - c.captured_on).days} days)")
            remaining = self.remaining_refundable(capture_id)
            if amount.compare_to(remaining) > 0:
                raise OverRefundError(
                    f"refund {amount} exceeds remaining refundable {remaining} on {capture_id} (captured {c.amount}, "
                    f"refunded {self.refunded(capture_id)}, pending {self.pending(capture_id)})")
            r = Refund(refund_id, capture_id, amount, reason, today)
            self._ledger.post(JournalEntry(RefundService._reserve_entry_id(refund_id),
                                           f"refund {refund_id} requested on {capture_id}", (
                Posting.debit(RefundService.MERCHANT, amount),
                Posting.credit(RefundService.REFUNDS, amount))))
            self._refunds[refund_id] = r
            self._refunds_by_capture[capture_id].append(r)
            return RefundResult(r, False)

    @staticmethod
    def _reserve_entry_id(refund_id: str) -> str:
        return f"je_req_{refund_id}"

    def succeed(self, refund_id: str) -> bool:
        """The processor confirmed the refund. Money leaves: refunds_payable is cleared against the scheme
        account, and under PROPORTIONAL the fee share is returned from fee_revenue to the merchant.
        Returns False for a redelivered confirmation (already SUCCEEDED)."""
        with self._lock:
            r = self.refund(refund_id)
            if r.state is RefundState.SUCCEEDED:
                return False
            if r.state is not RefundState.PENDING:
                raise RefundError(f"refund {refund_id} is {r.state.name}; cannot succeed")
            c = self._capture(r.capture_id)
            fee_back = (self._fee_share(c, r.amount) if self._policy.fee_policy is FeeRefundPolicy.PROPORTIONAL
                        else Money.zero(r.amount.currency))
            postings = [Posting.debit(RefundService.REFUNDS, r.amount), Posting.credit(RefundService.SCHEME, r.amount)]
            if fee_back.is_positive():
                postings.append(Posting.debit(RefundService.FEES, fee_back))
                postings.append(Posting.credit(RefundService.MERCHANT, fee_back))
            self._ledger.post(JournalEntry(self._next_entry_id(), f"refund {refund_id} settled", tuple(postings)))
            r.state = RefundState.SUCCEEDED
            r.fee_returned = fee_back
            return True

    def _fee_share(self, c: Capture, refund_amount: Money) -> Money:
        """Fee to return for a refund that is about to succeed, computed cumulatively so the rounding never
        drifts: target(after) - returned(so far), where target(x) = floor(fee * x / captured). When the
        capture is fully refunded, target is exactly the fee, so the sum of the shares is exactly the fee."""
        after = self.refunded(c.id).plus(refund_amount).minor_units
        target = (c.fee.minor_units * after) // c.amount.minor_units
        return Money.of_minor(target, c.fee.currency).minus(self.fee_returned(c.id))

    def fail(self, refund_id: str, why: str) -> None:
        """The processor refused the refund: the reservation is reversed and the balance becomes refundable again."""
        with self._lock:
            r = self.refund(refund_id)
            if r.state is not RefundState.PENDING:
                raise RefundError(f"refund {refund_id} is {r.state.name}; cannot fail")
            reserve = next(e for e in self._ledger.journal() if e.id == RefundService._reserve_entry_id(refund_id))
            self._ledger.post(reserve.reversal(self._next_entry_id(), f"refund {refund_id} failed: {why}"))
            r.state = RefundState.FAILED
            r.failure_reason = why

    def check_invariants(self) -> list[str]:
        """The invariants the tests assert after every operation. Returns an empty list when all hold."""
        with self._lock:
            broken: list[str] = []
            if not self._ledger.balances():
                broken.append("ledger does not balance")
            pending_total = Money.zero(self._ledger.balance(RefundService.REFUNDS).currency)
            for c in self._captures.values():
                refunded_and_pending = self.refunded(c.id).plus(self.pending(c.id))
                if refunded_and_pending.compare_to(c.amount) > 0:
                    broken.append(f"{c.id}: refunded + pending exceeds captured")
                if self.fee_returned(c.id).compare_to(c.fee) > 0:
                    broken.append(f"{c.id}: fee returned exceeds fee charged")
                if (self.refunded(c.id) == c.amount and self._policy.fee_policy is FeeRefundPolicy.PROPORTIONAL
                        and self.fee_returned(c.id) != c.fee):
                    broken.append(f"{c.id}: fully refunded but fee not fully returned")
                pending_total = pending_total.plus(self.pending(c.id))
            if self._ledger.balance(RefundService.REFUNDS) != pending_total:
                broken.append(f"refunds_payable {self._ledger.balance(RefundService.REFUNDS)} != pending {pending_total}")
            return broken


# ---------------------------------------------------------------------------------------------
# Demo
# ---------------------------------------------------------------------------------------------


def balances(ledger: Ledger) -> None:
    for a in ledger.accounts():
        print(f"   {a:<18} {str(ledger.balance(a)):>12}")
    print(f"   ledger balances: {str(ledger.balances()).lower()}")


def main() -> None:
    clock = FakeClock(date(2026, 1, 10))
    ledger = Ledger(USD)
    svc = RefundService(ledger, clock, RefundPolicy(180, FeeRefundPolicy.PROPORTIONAL))

    print("== Capture 100.00 USD at 2.90% + 0.30 ==")
    cap = svc.capture("cap_1", "pay_1", Money.of_minor(10_000, USD), 290, 30)
    print(f"fee {cap.fee}, merchant owed {cap.amount.minus(cap.fee)}")
    balances(ledger)

    print("\n== Partial refund 30.00, settled ==")
    clock.advance_days(5)
    svc.request_refund("re_1", "cap_1", Money.of_minor(3_000, USD), "one item returned")
    print(f"pending: remaining refundable {svc.remaining_refundable('cap_1')}")
    svc.succeed("re_1")
    print(f"settled: {svc.refund('re_1')}; remaining {svc.remaining_refundable('cap_1')}")

    print("\n== A second partial is pending; an over-refund is refused; a retry replays ==")
    svc.request_refund("re_2", "cap_1", Money.of_minor(3_000, USD), "second item returned")
    try:
        svc.request_refund("re_3", "cap_1", Money.of_minor(5_000, USD), "customer asked for the rest")
    except OverRefundError as e:
        print(f"refused: {e}")
    again = svc.request_refund("re_2", "cap_1", Money.of_minor(3_000, USD), "client retry")
    print(f"re_2 again -> replayed={str(again.replayed).lower()}, state {again.refund.state}")
    try:
        svc.request_refund("re_2", "cap_1", Money.of_minor(3_500, USD), "client bug")
    except RefundIdempotencyConflictError as e:
        print(f"refused: {e}")

    print("\n== The processor fails re_2: the reservation is reversed ==")
    svc.fail("re_2", "card account closed")
    print(f"re_2 {svc.refund('re_2').state}; remaining refundable {svc.remaining_refundable('cap_1')}")

    print("\n== Refund the remaining 70.00: the fee shares sum to exactly the fee ==")
    svc.request_refund("re_4", "cap_1", Money.of_minor(7_000, USD), "order canceled")
    svc.succeed("re_4")
    print(f"redelivered confirmation applied again? {str(svc.succeed('re_4')).lower()}")
    for r in svc.refunds_for("cap_1"):
        print(f"   {r}")
    print(f"refunded {svc.refunded('cap_1')}, fee returned {svc.fee_returned('cap_1')} of {cap.fee}"
          f", remaining {svc.remaining_refundable('cap_1')}")
    balances(ledger)
    print("journal for cap_1:")
    for e in ledger.journal():
        print(f"   {e.id}  {e.description}")
        for p in e.postings:
            print(f"   {p}")

    print("\n== The same full refund under FeeRefundPolicy.NONE: the merchant ends up owing the fee ==")
    none_ledger = Ledger(USD)
    none = RefundService(none_ledger, clock, RefundPolicy(180, FeeRefundPolicy.NONE))
    none.capture("cap_n", "pay_n", Money.of_minor(10_000, USD), 290, 30)
    none.request_refund("re_n", "cap_n", Money.of_minor(10_000, USD), "order canceled")
    none.succeed("re_n")
    print(f"merchant_payable {none_ledger.balance(RefundService.MERCHANT)}, fee_revenue "
          f"{none_ledger.balance(RefundService.FEES)}, ledger balances: {str(none_ledger.balances()).lower()}")

    print("\n== Rounding: 10.00 USD capture, fee 0.59, refunded as 3.33 + 3.33 + 3.34 ==")
    small = svc.capture("cap_2", "pay_2", Money.of_minor(1_000, USD), 290, 30)
    for amt in (333, 333, 334):
        rid = f"re_s{amt}_{len(svc.refunds_for('cap_2'))}"
        svc.request_refund(rid, "cap_2", Money.of_minor(amt, USD), "split")
        svc.succeed(rid)
    shares = "[" + ", ".join(str(r.fee_returned) for r in svc.refunds_for("cap_2")) + "]"
    print(f"shares: {shares} sum {svc.fee_returned('cap_2')} (fee {small.fee}; per-refund floor would give 0.57)")

    print("\n== The refund window: 180 days ==")
    svc.capture("cap_3", "pay_3", Money.of_minor(2_500, USD), 290, 30)
    clock.advance_days(180)
    svc.request_refund("re_w1", "cap_3", Money.of_minor(500, USD), "day 180")
    print(f"day 180: accepted, remaining {svc.remaining_refundable('cap_3')}")
    clock.advance_days(1)
    try:
        svc.request_refund("re_w2", "cap_3", Money.of_minor(500, USD), "day 181")
    except RefundWindowClosedError as e:
        print(f"refused: {e}")

    print(f"\ninvariants broken: {svc.check_invariants()}")


if __name__ == "__main__":
    main()
