"""Lesson: phases/12-payment-systems-design/08-payouts-and-balance-management/docs/en.md
Payouts, Balances, and Negative Balance Handling
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: Nacha Operating Rules (batch/entry/control records, return reason codes R01-R03);
         Visa and Mastercard settlement (clearing vs settlement).
Python 3.11+, standard library only. Same design as the Java version.
"""

from __future__ import annotations

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

    def negate(self) -> "Money":
        return Money(-self.minor_units, self.currency)

    def min(self, other: "Money") -> "Money":
        return self if self.compare_to(other) <= 0 else other

    def is_positive(self) -> bool:
        return self.minor_units > 0

    def is_negative(self) -> bool:
        return self.minor_units < 0

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
# Calendar clock: settlement, reserves and payout schedules are all counted in days.
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


# Days of the week as date.weekday() returns them: Monday is 0, Sunday is 6 (java.time.DayOfWeek names).
class DayOfWeek(Enum):
    MONDAY = 0
    TUESDAY = 1
    WEDNESDAY = 2
    THURSDAY = 3
    FRIDAY = 4
    SATURDAY = 5
    SUNDAY = 6

    @staticmethod
    def of(d: date) -> "DayOfWeek":
        return DayOfWeek(d.weekday())


# ---------------------------------------------------------------------------------------------
# Minimal double-entry ledger (Phase 10, Lesson 03). Each merchant has four sub-accounts.
# ---------------------------------------------------------------------------------------------


class Side(Enum):
    DEBIT = "debit"
    CREDIT = "credit"


class AccountType(Enum):
    ASSET = ("asset", Side.DEBIT)
    LIABILITY = ("liability", Side.CREDIT)
    REVENUE = ("revenue", Side.CREDIT)

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


@dataclass(frozen=True)
class JournalEntry:
    id: str
    date: date
    description: str
    postings: tuple[Posting, ...]

    def __post_init__(self) -> None:
        object.__setattr__(self, "postings", tuple(self.postings))
        net = sum(p.amount.minor_units if p.side is Side.DEBIT else -p.amount.minor_units for p in self.postings)
        if len(self.postings) < 2 or net != 0:
            raise ValueError(f"entry {self.id} is unbalanced or too short")


class Ledger:
    def __init__(self, currency: Currency) -> None:
        self._accounts: dict[str, AccountType] = {}
        self._balances: dict[str, int] = {}
        self._journal: list[JournalEntry] = []
        self._currency = currency
        self._seq = 0

    def open(self, account: str, type_: AccountType) -> None:
        if account in self._accounts:
            raise ValueError(f"account exists: {account}")
        self._accounts[account] = type_
        self._balances[account] = 0

    def post(self, when: date, description: str, postings: list[Posting] | tuple[Posting, ...]) -> None:
        """Post one balanced entry; every posting amount is positive (a zero line is refused by Posting itself)."""
        for p in postings:
            if p.account not in self._accounts:
                raise ValueError(f"unknown account {p.account}")
        self._seq += 1
        e = JournalEntry(f"je_{self._seq:04d}", when, description, tuple(postings))
        self._journal.append(e)
        for p in e.postings:
            signed = p.amount.minor_units if p.side is self._accounts[p.account].normal else -p.amount.minor_units
            self._balances[p.account] += signed

    def balance(self, account: str) -> Money:
        b = self._balances.get(account)
        if b is None:
            raise ValueError(f"unknown account {account}")
        return Money.of_minor(b, self._currency)

    def balances(self) -> bool:
        debit_side = sum(self._balances[a] for a, t in self._accounts.items() if t.normal is Side.DEBIT)
        credit_side = sum(self._balances[a] for a, t in self._accounts.items() if t.normal is Side.CREDIT)
        return debit_side == credit_side

    def journal(self) -> tuple[JournalEntry, ...]:
        return tuple(self._journal)


# ---------------------------------------------------------------------------------------------
# Merchants, captures, settlement reports, reserves, payouts.
# ---------------------------------------------------------------------------------------------


class PayoutFrequency(Enum):
    DAILY = "daily"
    WEEKLY = "weekly"


@dataclass(frozen=True)
class PayoutSchedule:
    """When a merchant is paid: every business day, or one weekday; never less than the minimum."""

    frequency: PayoutFrequency
    weekly_day: Optional[DayOfWeek]
    minimum_payout: Money

    def due_on(self, d: date) -> bool:
        dow = DayOfWeek.of(d)
        business_day = dow is not DayOfWeek.SATURDAY and dow is not DayOfWeek.SUNDAY
        return business_day and (self.frequency is PayoutFrequency.DAILY or dow is self.weekly_day)


@dataclass(frozen=True)
class ReservePolicy:
    """Rolling reserve: a share of each settlement is held back for a number of days before it becomes available."""

    basis_points: int
    hold_days: int

    def __post_init__(self) -> None:
        if self.basis_points < 0 or self.basis_points > 10_000:
            raise ValueError("reserve bps must be 0..10000")
        if self.hold_days < 0:
            raise ValueError("hold_days must be >= 0")


@dataclass(frozen=True)
class Merchant:
    id: str
    bank_account_token: str
    schedule: PayoutSchedule
    reserve: ReservePolicy


@dataclass(frozen=True)
class CaptureRecord:
    """A capture's net is owed to the merchant, but only becomes payable once the scheme has actually paid us for it."""

    id: str
    merchant_id: str
    gross: Money
    fee: Money
    captured_on: date

    def net(self) -> Money:
        return self.gross.minus(self.fee)


@dataclass(frozen=True)
class SettlementReport:
    """The scheme's statement that it paid us for these captures. The authority on "settled"."""

    id: str
    value_date: date
    capture_ids: tuple[str, ...]
    total_gross: Money

    def __post_init__(self) -> None:
        object.__setattr__(self, "capture_ids", tuple(self.capture_ids))


@dataclass(frozen=True)
class ReserveTranche:
    merchant_id: str
    amount: Money
    release_on: date


class PayoutDirection(Enum):
    CREDIT = "credit"
    DEBIT = "debit"

    def __str__(self) -> str:
        return self.name


class PayoutState(Enum):
    IN_TRANSIT = "in_transit"
    PAID = "paid"
    RETURNED = "returned"

    def __str__(self) -> str:
        return self.name


class Payout:
    def __init__(self, id: str, merchant_id: str, amount: Money, direction: PayoutDirection, initiated_on: date) -> None:
        self.id = id
        self.merchant_id = merchant_id
        self.amount = amount
        self.direction = direction
        self.initiated_on = initiated_on
        self.state = PayoutState.IN_TRANSIT
        self.return_code: Optional[str] = None

    def __str__(self) -> str:
        tail = "" if self.return_code is None else f" {self.return_code}"
        return f"{self.id} {self.direction.name} {self.merchant_id} {self.amount} {self.state.name}{tail}"

    __repr__ = __str__


class PayoutFileSink(Protocol):
    """The hook to the bank: a payout file per run. The real one writes NACHA or ISO 20022 pain.001."""

    def write(self, file_name: str, lines: list[str]) -> None: ...


class InMemoryPayoutFileSink:
    def __init__(self) -> None:
        self.files: dict[str, list[str]] = {}

    def write(self, file_name: str, lines: list[str]) -> None:
        self.files[file_name] = list(lines)


@dataclass(frozen=True)
class Balance:
    pending: Money
    available: Money
    reserve: Money
    in_transit: Money

    def total(self) -> Money:
        """Everything we owe the merchant, in any stage."""
        return self.pending.plus(self.available).plus(self.reserve).plus(self.in_transit)


class BalanceService:
    SCHEME = "scheme_receivable"             # asset: captured, not yet paid to us by the scheme
    BANK = "bank_cash"                       # asset: our operating bank account
    COLLECTIONS = "collections_receivable"   # asset: debits we have asked merchants' banks for
    FEES = "fee_revenue"

    def __init__(self, ledger: Ledger, clock: Clock, sink: PayoutFileSink, collect_after_negative_days: int, currency: Currency) -> None:
        self._ledger = ledger
        self._clock = clock
        self._sink = sink
        self._collect_after_negative_days = collect_after_negative_days
        self._currency = currency
        self._merchants: dict[str, Merchant] = {}
        self._captures: dict[str, CaptureRecord] = {}
        self._settled_by: dict[str, str] = {}
        self._tranches: list[ReserveTranche] = []
        self._payouts: dict[str, Payout] = {}
        self._negative_since: dict[str, date] = {}
        self._paused: dict[str, str] = {}
        self._payout_seq = 0
        self._file_seq = 0
        ledger.open(BalanceService.SCHEME, AccountType.ASSET)
        ledger.open(BalanceService.BANK, AccountType.ASSET)
        ledger.open(BalanceService.COLLECTIONS, AccountType.ASSET)
        ledger.open(BalanceService.FEES, AccountType.REVENUE)

    @staticmethod
    def pending_acct(m: str) -> str:
        return m + ":pending"

    @staticmethod
    def available_acct(m: str) -> str:
        return m + ":available"

    @staticmethod
    def reserve_acct(m: str) -> str:
        return m + ":reserve"

    @staticmethod
    def in_transit_acct(m: str) -> str:
        return m + ":in_transit"

    def ledger(self) -> Ledger:
        return self._ledger

    def onboard(self, m: Merchant) -> None:
        self._merchants[m.id] = m
        self._ledger.open(BalanceService.pending_acct(m.id), AccountType.LIABILITY)
        self._ledger.open(BalanceService.available_acct(m.id), AccountType.LIABILITY)
        self._ledger.open(BalanceService.reserve_acct(m.id), AccountType.LIABILITY)
        self._ledger.open(BalanceService.in_transit_acct(m.id), AccountType.LIABILITY)

    def merchant(self, id: str) -> Merchant:
        m = self._merchants.get(id)
        if m is None:
            raise ValueError(f"unknown merchant {id}")
        return m

    def balance(self, merchant_id: str) -> Balance:
        self.merchant(merchant_id)
        return Balance(self._ledger.balance(BalanceService.pending_acct(merchant_id)),
                       self._ledger.balance(BalanceService.available_acct(merchant_id)),
                       self._ledger.balance(BalanceService.reserve_acct(merchant_id)),
                       self._ledger.balance(BalanceService.in_transit_acct(merchant_id)))

    def capture(self, capture_id: str, merchant_id: str, gross: Money, fee_bps: int, fixed_fee_minor: int) -> CaptureRecord:
        """A capture: the scheme owes us the gross; the merchant is owed the net, but it is PENDING until settlement."""
        self.merchant(merchant_id)
        if capture_id in self._captures:
            raise RuntimeError(f"capture exists: {capture_id}")
        variable = (gross.minor_units * fee_bps + 5_000) // 10_000
        c = CaptureRecord(capture_id, merchant_id, gross, Money.of_minor(variable + fixed_fee_minor, self._currency), self._clock.today())
        postings = [Posting.debit(BalanceService.SCHEME, gross), Posting.credit(BalanceService.pending_acct(merchant_id), c.net())]
        if c.fee.is_positive():
            postings.append(Posting.credit(BalanceService.FEES, c.fee))
        self._ledger.post(self._clock.today(), f"capture {capture_id}", postings)
        self._captures[capture_id] = c
        return c

    def is_settled(self, capture_id: str) -> bool:
        return capture_id in self._settled_by

    def settle(self, report: SettlementReport) -> None:
        """Apply a settlement report. It must reconcile first: every capture known and unsettled, and the report's
        total equal to the sum of their gross amounts. Only then does cash arrive and pending become available,
        minus the merchant's rolling reserve, which is held as a tranche with its own release date."""
        total = Money.zero(self._currency)
        for cid in report.capture_ids:
            c = self._captures.get(cid)
            if c is None:
                raise RuntimeError(f"report {report.id} names unknown capture {cid}")
            if self.is_settled(cid):
                raise RuntimeError(f"report {report.id}: {cid} already settled by {self._settled_by[cid]}")
            total = total.plus(c.gross)
        if total != report.total_gross:
            raise RuntimeError(f"report {report.id} does not reconcile: total {report.total_gross} but captures sum to {total}")
        postings = [Posting.debit(BalanceService.BANK, total), Posting.credit(BalanceService.SCHEME, total)]
        net_by_merchant: dict[str, Money] = {}
        for cid in report.capture_ids:
            c = self._captures[cid]
            net_by_merchant[c.merchant_id] = net_by_merchant.get(c.merchant_id, Money.zero(self._currency)).plus(c.net())
            self._settled_by[cid] = report.id
        for merchant_id, net in net_by_merchant.items():
            m = self.merchant(merchant_id)
            reserve = Money.of_minor((net.minor_units * m.reserve.basis_points) // 10_000, self._currency)
            postings.append(Posting.debit(BalanceService.pending_acct(m.id), net))
            postings.append(Posting.credit(BalanceService.available_acct(m.id), net.minus(reserve)))
            if reserve.is_positive():
                postings.append(Posting.credit(BalanceService.reserve_acct(m.id), reserve))
                self._tranches.append(ReserveTranche(m.id, reserve, report.value_date + timedelta(days=m.reserve.hold_days)))
        self._ledger.post(self._clock.today(), f"settlement {report.id}", postings)

    def debit_merchant(self, merchant_id: str, amount: Money, reason: str) -> None:
        """A refund, chargeback, or fee: it comes out of available, which may go negative. The scheme nets it from what it owes us."""
        self.merchant(merchant_id)
        if not amount.is_positive():
            raise ValueError("debit must be positive")
        self._ledger.post(self._clock.today(), f"{reason} {merchant_id}", [
            Posting.debit(BalanceService.available_acct(merchant_id), amount), Posting.credit(BalanceService.SCHEME, amount)])
        self._track_negative(merchant_id)

    def release_reserves(self) -> Money:
        """Release every tranche due today or earlier into available."""
        released = Money.zero(self._currency)
        today = self._clock.today()
        due = [t for t in self._tranches if not t.release_on > today]
        for t in due:
            self._ledger.post(today, f"reserve release {t.merchant_id} due {t.release_on}", [
                Posting.debit(BalanceService.reserve_acct(t.merchant_id), t.amount),
                Posting.credit(BalanceService.available_acct(t.merchant_id), t.amount)])
            released = released.plus(t.amount)
            self._track_negative(t.merchant_id)
        self._tranches = [t for t in self._tranches if t not in due]
        return released

    def cover_from_reserve(self, merchant_id: str) -> Money:
        """Negative balance, step 1: use the merchant's own reserve, earliest-releasing tranches first, before
        asking anyone for money. That is what the reserve is for."""
        deficit = self._ledger.balance(BalanceService.available_acct(merchant_id)).negate()
        used = Money.zero(self._currency)
        if not deficit.is_positive():
            return used
        mine = sorted((t for t in self._tranches if t.merchant_id == merchant_id), key=lambda t: t.release_on)
        for t in mine:
            if not deficit.is_positive():
                break
            take = t.amount.min(deficit)
            self._tranches.remove(t)
            if take.compare_to(t.amount) < 0:
                self._tranches.append(ReserveTranche(merchant_id, t.amount.minus(take), t.release_on))
            used = used.plus(take)
            deficit = deficit.minus(take)
        if used.is_positive():
            self._ledger.post(self._clock.today(), f"reserve applied to negative balance {merchant_id}", [
                Posting.debit(BalanceService.reserve_acct(merchant_id), used),
                Posting.credit(BalanceService.available_acct(merchant_id), used)])
        self._track_negative(merchant_id)
        return used

    def _track_negative(self, merchant_id: str) -> None:
        if self._ledger.balance(BalanceService.available_acct(merchant_id)).is_negative():
            self._negative_since.setdefault(merchant_id, self._clock.today())
        else:
            self._negative_since.pop(merchant_id, None)

    def negative_since(self, merchant_id: str) -> Optional[date]:
        return self._negative_since.get(merchant_id)

    def scheduled_reserve(self, merchant_id: str) -> Money:
        """Sum of the merchant's unreleased tranches. Must always equal the reserve account's balance."""
        total = Money.zero(self._currency)
        for t in self._tranches:
            if t.merchant_id == merchant_id:
                total = total.plus(t.amount)
        return total

    def is_paused(self, merchant_id: str) -> bool:
        return merchant_id in self._paused

    def resume(self, merchant_id: str, why: str) -> None:
        self._paused.pop(merchant_id, None)

    def payout(self, id: str) -> Payout:
        p = self._payouts.get(id)
        if p is None:
            raise ValueError(f"unknown payout {id}")
        return p

    def run_payouts(self) -> list[Payout]:
        """The daily payout run. For each merchant: cover a negative balance from reserve; if it has been negative
        for collect_after_negative_days, ask its bank for the deficit (a DEBIT line); otherwise, if its schedule is
        due, it is not paused, and available meets the minimum, pay out all of available (a CREDIT line). One
        file per run, with a trailer carrying the counts and totals the bank checks."""
        today = self._clock.today()
        batch: list[Payout] = []
        for m in list(self._merchants.values()):
            self.cover_from_reserve(m.id)
            available = self._ledger.balance(BalanceService.available_acct(m.id))
            since = self._negative_since.get(m.id)
            if available.is_negative() and since is not None and not today < since + timedelta(days=self._collect_after_negative_days):
                self._payout_seq += 1
                debit = Payout(f"co_{self._payout_seq:04d}", m.id, available.negate(), PayoutDirection.DEBIT, today)
                self._ledger.post(today, f"collection {debit.id}", [
                    Posting.debit(BalanceService.COLLECTIONS, debit.amount),
                    Posting.credit(BalanceService.available_acct(m.id), debit.amount)])
                self._track_negative(m.id)
                self._payouts[debit.id] = debit
                batch.append(debit)
                continue
            if not m.schedule.due_on(today) or self.is_paused(m.id):
                continue
            if available.compare_to(m.schedule.minimum_payout) < 0 or not available.is_positive():
                continue
            self._payout_seq += 1
            credit = Payout(f"po_{self._payout_seq:04d}", m.id, available, PayoutDirection.CREDIT, today)
            self._ledger.post(today, f"payout {credit.id}", [
                Posting.debit(BalanceService.available_acct(m.id), available),
                Posting.credit(BalanceService.in_transit_acct(m.id), available)])
            self._payouts[credit.id] = credit
            batch.append(credit)
        if batch:
            self._write_file(today, batch)
        return batch

    def _write_file(self, today: date, batch: list[Payout]) -> None:
        self._file_seq += 1
        name = f"payouts_{today}_{self._file_seq:03d}.csv"
        lines = [f"H,{today},{name},{self._currency.code}"]
        credits = 0
        debits = 0
        for p in batch:
            lines.append(f"D,{p.id},{p.merchant_id},{self.merchant(p.merchant_id).bank_account_token},{p.direction.name},{p.amount.minor_units}")
            if p.direction is PayoutDirection.CREDIT:
                credits += p.amount.minor_units
            else:
                debits += p.amount.minor_units
        lines.append(f"T,{len(batch)},{credits},{debits}")
        self._sink.write(name, lines)

    def confirm(self, payout_id: str) -> None:
        """The bank confirms a line. A credit leaves our cash; a debit (collection) arrives in it."""
        p = self.payout(payout_id)
        if p.state is PayoutState.PAID:
            return
        if p.state is not PayoutState.IN_TRANSIT:
            raise RuntimeError(f"{payout_id} is {p.state.name}")
        if p.direction is PayoutDirection.CREDIT:
            self._ledger.post(self._clock.today(), f"payout paid {p.id}", [
                Posting.debit(BalanceService.in_transit_acct(p.merchant_id), p.amount), Posting.credit(BalanceService.BANK, p.amount)])
        else:
            self._ledger.post(self._clock.today(), f"collection received {p.id}", [
                Posting.debit(BalanceService.BANK, p.amount), Posting.credit(BalanceService.COLLECTIONS, p.amount)])
        p.state = PayoutState.PAID

    def returned(self, payout_id: str, return_code: str) -> None:
        """The bank returns a line (NACHA return codes: R01 insufficient funds, R02 account closed, R03 no account).
        A returned credit goes back to available and payouts pause until the bank details are fixed; a returned
        debit puts the deficit back on the merchant's balance."""
        p = self.payout(payout_id)
        if p.state is not PayoutState.IN_TRANSIT:
            raise RuntimeError(f"{payout_id} is {p.state.name}; cannot return")
        if p.direction is PayoutDirection.CREDIT:
            self._ledger.post(self._clock.today(), f"payout returned {p.id} {return_code}", [
                Posting.debit(BalanceService.in_transit_acct(p.merchant_id), p.amount),
                Posting.credit(BalanceService.available_acct(p.merchant_id), p.amount)])
            self._paused[p.merchant_id] = f"payout {p.id} returned {return_code}"
        else:
            self._ledger.post(self._clock.today(), f"collection returned {p.id} {return_code}", [
                Posting.debit(BalanceService.available_acct(p.merchant_id), p.amount),
                Posting.credit(BalanceService.COLLECTIONS, p.amount)])
            self._negative_since.pop(p.merchant_id, None)
            self._track_negative(p.merchant_id)
        p.state = PayoutState.RETURNED
        p.return_code = return_code


# ---------------------------------------------------------------------------------------------
# Demo
# ---------------------------------------------------------------------------------------------


def usd(minor: int) -> Money:
    return Money.of_minor(minor, USD)


def _fmt(batch: list[Payout]) -> str:
    return "[" + ", ".join(str(p) for p in batch) + "]"


def show(svc: BalanceService, clock: FakeClock, m: str, label: str) -> None:
    b = svc.balance(m)
    today = clock.today()
    print(f"{today} {DayOfWeek.of(today).name[:3]:<3} {label:<38} pending {str(b.pending):<11} available {str(b.available):<12} "
          f"reserve {str(b.reserve):<10} in transit {b.in_transit}")


def main() -> None:
    clock = FakeClock(date(2026, 3, 2))  # a Monday
    files = InMemoryPayoutFileSink()
    svc = BalanceService(Ledger(USD), clock, files, 7, USD)
    svc.onboard(Merchant("m_shoe", "ba_tok_7Q2", PayoutSchedule(PayoutFrequency.DAILY, None, usd(1_000)), ReservePolicy(1_000, 90)))
    svc.onboard(Merchant("m_art", "ba_tok_X91", PayoutSchedule(PayoutFrequency.WEEKLY, DayOfWeek.FRIDAY, usd(2_500)), ReservePolicy(0, 0)))

    print("== Captures are owed to the merchant but not payable: they are PENDING until the scheme pays us ==")
    svc.capture("cap_1", "m_shoe", usd(10_000), 290, 30)
    svc.capture("cap_2", "m_shoe", usd(25_000), 290, 30)
    svc.capture("cap_3", "m_shoe", usd(5_000), 290, 30)
    svc.capture("cap_4", "m_art", usd(18_000), 290, 30)
    show(svc, clock, "m_shoe", "after 3 captures (400.00 gross)")
    print(f"payout run Monday -> {_fmt(svc.run_payouts())}  (nothing settled, nothing to pay)")
    clock.advance_days(1)
    print(f"payout run Tuesday -> {_fmt(svc.run_payouts())}  (the expected T+1 report did not arrive)")

    print("\n== Wednesday: the settlement report arrives and reconciles; 10% of m_shoe's net goes to a 90-day reserve ==")
    clock.advance_days(1)
    try:
        svc.settle(SettlementReport("st_bad", clock.today(), ("cap_1", "cap_2", "cap_3"), usd(39_999)))
    except RuntimeError as e:
        print(f"refused: {e}")
    svc.settle(SettlementReport("st_0302", clock.today(), ("cap_1", "cap_2", "cap_3", "cap_4"), usd(58_000)))
    show(svc, clock, "m_shoe", "after settlement st_0302")
    wed = svc.run_payouts()
    print(f"payout run Wednesday -> {_fmt(wed)}")
    show(svc, clock, "m_shoe", "after the payout run")
    for name, lines in files.files.items():
        print(f"file {name}")
        for line in lines:
            print(f"   {line}")
    clock.advance_days(1)
    svc.confirm(wed[0].id)
    show(svc, clock, "m_shoe", "bank confirmed the payout")

    print("\n== Friday: m_art's weekly payout is returned R03 (no account); payouts pause ==")
    clock.advance_days(1)
    fri = svc.run_payouts()
    print(f"payout run Friday -> {_fmt(fri)}")
    svc.returned(fri[0].id, "R03")
    show(svc, clock, "m_art", "after the return")
    clock.advance_days(7)
    print(f"next Friday run -> {_fmt(svc.run_payouts())}  (m_art paused: {str(svc.is_paused('m_art')).lower()})")
    svc.resume("m_art", "bank details re-verified")
    print(f"after resume, Friday run would pay {svc.balance('m_art').available}")

    print("\n== A 600.00 chargeback drives m_shoe negative; the reserve covers what it can ==")
    clock.advance_days(3)  # Monday 2026-03-16
    svc.debit_merchant("m_shoe", usd(60_000), "chargeback dp_9")
    show(svc, clock, "m_shoe", "after the chargeback")
    svc.run_payouts()
    show(svc, clock, "m_shoe", "reserve applied, still negative")
    print(f"negative since {svc.negative_since('m_shoe')}")

    print("\n== New settlements net against the deficit; after 7 negative days the rest is collected ==")
    svc.capture("cap_5", "m_shoe", usd(8_000), 290, 30)
    clock.advance_days(2)
    svc.settle(SettlementReport("st_0316", clock.today(), ("cap_5",), usd(8_000)))
    svc.run_payouts()
    show(svc, clock, "m_shoe", "netted by st_0316, reserve applied")
    clock.advance_days(5)  # Monday 2026-03-23, 7 days negative
    collect = svc.run_payouts()
    print(f"payout run -> {_fmt(collect)}")
    show(svc, clock, "m_shoe", "collection initiated")
    clock.advance_days(2)
    svc.confirm(collect[0].id)
    show(svc, clock, "m_shoe", "collection received")

    ledger = svc.ledger()
    print("\n== Ledger ==")
    for a in (BalanceService.SCHEME, BalanceService.BANK, BalanceService.COLLECTIONS, BalanceService.FEES):
        print(f"   {a:<24} {str(ledger.balance(a)):>12}")
    for m in ("m_shoe", "m_art"):
        print(f"   {m:<24} {str(svc.balance(m).total()):>12} owed in total")
    print(f"   ledger balances: {str(ledger.balances()).lower()}, journal entries: {len(ledger.journal())}")


if __name__ == "__main__":
    main()
