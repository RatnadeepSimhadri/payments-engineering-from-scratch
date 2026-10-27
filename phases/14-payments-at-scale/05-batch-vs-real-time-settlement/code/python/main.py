"""Lesson: phases/14-payments-at-scale/05-batch-vs-real-time-settlement/docs/en.md
Batch vs Real-Time Settlement Pipelines
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: Nacha Operating Rules (batch/file control records, entry hash); ISO 20022 pain.001 (payment initiation).
Python 3.11+, standard library only. Money is integer minor units + currency (Phase 10, Lesson 01).
"""

from __future__ import annotations

from dataclasses import dataclass
from datetime import date, datetime, timezone
from enum import Enum
from functools import total_ordering
from typing import Optional


# ---------- Money (copied from Phase 10, Lesson 01) ----------


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


class Rounding(Enum):
    HALF_EVEN = "half_even"
    HALF_UP = "half_up"
    DOWN = "down"
    UP = "up"


class CurrencyMismatchError(ValueError):
    def __init__(self, a: Currency, b: Currency) -> None:
        super().__init__(f"cannot combine {a.code} with {b.code}")


def divide_rounded(dividend: int, divisor: int, rounding: Rounding) -> int:
    """Integer division with an explicit rounding rule. Python's // floors toward -inf, so the work is done on
    magnitudes and the sign reapplied, which is what Java's truncating / and % give."""
    sign = -1 if (dividend < 0) != (divisor < 0) else 1
    quotient, remainder = divmod(abs(dividend), abs(divisor))
    abs_divisor = abs(divisor)
    if remainder == 0:
        return sign * quotient
    twice = remainder * 2
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
    minor_units: int
    currency: Currency

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
        if negative:
            s = s[1:]
        whole, _, frac = s.partition(".")
        if len(frac) > currency.exponent:
            raise ArithmeticError(f"too many decimals: {text}")
        frac = frac.ljust(currency.exponent, "0")
        minor = int(whole or "0") * currency.minor_units_per_major() + (int(frac) if frac else 0)
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

    def is_positive(self) -> bool:
        return self.minor_units > 0

    def is_negative(self) -> bool:
        return self.minor_units < 0

    def times(self, numerator: int, denominator: int, rounding: Rounding) -> "Money":
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


# ---------------------------------------------------------------------------------------------
# Domain: merchants, settlement events, fees
# ---------------------------------------------------------------------------------------------


@dataclass(frozen=True)
class Merchant:
    """A merchant and the bank account its payouts go to. routing is a 9-digit ABA routing number."""

    id: str
    routing: str
    account: str
    on_hold: bool

    def __post_init__(self) -> None:
        if len(self.routing) != 9 or not self.routing.isdigit():
            raise ValueError(f"routing number must be 9 digits: {self.routing}")


class EventType(Enum):
    CAPTURE = "CAPTURE"
    REFUND = "REFUND"
    CHARGEBACK = "CHARGEBACK"

    def __str__(self) -> str:
        return self.name


@dataclass(frozen=True)
class SettlementEvent:
    """One money-moving event that the acquirer will settle with the platform: amount is always positive."""

    id: str
    merchant_id: str
    type: EventType
    amount: Money
    at: datetime

    def __post_init__(self) -> None:
        if not self.amount.is_positive():
            raise ValueError(f"event amounts are positive; the type gives the sign: {self.id}")


@dataclass(frozen=True)
class FeeSchedule:
    """Percentage fee (numerator/denominator) plus a fixed fee per capture, and a flat chargeback fee."""

    percent_numerator: int
    percent_denominator: int
    fixed: Money
    chargeback_fee: Money

    @staticmethod
    def standard(c: Currency) -> "FeeSchedule":
        return FeeSchedule(29, 1000, Money.of_minor(30, c), Money.of_minor(1500, c))  # 2.9% + 0.30, 15.00

    def fee_for(self, e: SettlementEvent) -> Money:
        """Fee the platform keeps on this event. Refunds return no fee (the common card-acquiring policy)."""
        if e.type is EventType.CAPTURE:
            return e.amount.times(self.percent_numerator, self.percent_denominator, Rounding.HALF_EVEN).plus(self.fixed)
        if e.type is EventType.REFUND:
            return Money.zero(e.amount.currency)
        return self.chargeback_fee

    def merchant_effect(self, e: SettlementEvent) -> Money:
        """Signed effect of the event on what the platform owes the merchant."""
        if e.type is EventType.CAPTURE:
            return e.amount.minus(self.fee_for(e))
        if e.type is EventType.REFUND:
            return e.amount.negate()
        return e.amount.plus(self.chargeback_fee).negate()

    @staticmethod
    def processor_effect(e: SettlementEvent) -> Money:
        """Signed effect of the event on what the acquirer owes the platform."""
        return e.amount if e.type is EventType.CAPTURE else e.amount.negate()


# ---------------------------------------------------------------------------------------------
# A minimal double-entry ledger: debit positive, credit negative, every journal sums to zero.
# ---------------------------------------------------------------------------------------------


@dataclass(frozen=True)
class Posting:
    account: str
    minor: int


class UnbalancedJournalException(RuntimeError):
    def __init__(self, journal_id: str, total: int) -> None:
        super().__init__(f"journal {journal_id} does not balance: sum {total}")


class Ledger:
    PROCESSOR_RECEIVABLE = "processor_receivable"
    FEE_REVENUE = "fee_revenue"
    PAYOUTS_IN_TRANSIT = "payouts_in_transit"
    CASH = "cash:settlement_bank"

    @staticmethod
    def merchant_payable(merchant_id: str) -> str:
        return f"merchant_payable:{merchant_id}"

    def __init__(self, currency: Currency) -> None:
        self.currency = currency
        self._balances: dict[str, int] = {}
        self._journals: dict[str, tuple] = {}
        self._min_cash = 0

    def post(self, journal_id: str, postings: list) -> bool:
        """Post a balanced journal once. Returns False (and changes nothing) if the id was already posted."""
        if journal_id in self._journals:
            return False
        total = sum(p.minor for p in postings)
        if total != 0:
            raise UnbalancedJournalException(journal_id, total)
        for p in postings:
            self._balances[p.account] = self._balances.get(p.account, 0) + p.minor
        self._journals[journal_id] = tuple(postings)
        self._min_cash = min(self._min_cash, self._balances.get(self.CASH, 0))
        return True

    def balance(self, account: str) -> Money:
        return Money.of_minor(self._balances.get(account, 0), self.currency)

    def owed_to(self, merchant_id: str) -> Money:
        """What the platform owes the merchant: the credit balance of its payable account, as a positive number."""
        return self.balance(self.merchant_payable(merchant_id)).negate()

    def trial_balance(self) -> int:
        return sum(self._balances.values())

    def journal_count(self) -> int:
        return len(self._journals)

    def peak_prefunding(self) -> Money:
        """The most negative the settlement bank account has been: how much the platform had to prefund."""
        return Money.of_minor(-self._min_cash, self.currency)

    def balances_view(self) -> dict:
        """Account -> minor units, in account-name order (the Java keeps a TreeMap)."""
        return {k: self._balances[k] for k in sorted(self._balances)}


class Platform:
    """Books events into the ledger as they happen. Shared by both settlement styles."""

    def __init__(self, currency: Currency, fees: FeeSchedule) -> None:
        self.ledger = Ledger(currency)
        self.fees = fees
        self.merchants: dict[str, Merchant] = {}
        self.events: list[SettlementEvent] = []

    def add_merchant(self, m: Merchant) -> None:
        self.merchants[m.id] = m

    def book(self, e: SettlementEvent) -> bool:
        """Post the event's journal at the moment it happens. Idempotent on event id."""
        if e.merchant_id not in self.merchants:
            raise ValueError(f"unknown merchant {e.merchant_id}")
        if e.amount.currency != self.ledger.currency:
            raise CurrencyMismatchError(self.ledger.currency, e.amount.currency)
        payable = Ledger.merchant_payable(e.merchant_id)
        amount = e.amount.minor_units
        fee = self.fees.fee_for(e).minor_units
        if e.type is EventType.CAPTURE:
            postings = [Posting(Ledger.PROCESSOR_RECEIVABLE, amount),
                        Posting(payable, -(amount - fee)),
                        Posting(Ledger.FEE_REVENUE, -fee)]
        elif e.type is EventType.REFUND:
            postings = [Posting(payable, amount),
                        Posting(Ledger.PROCESSOR_RECEIVABLE, -amount)]
        else:
            postings = [Posting(payable, amount + fee),
                        Posting(Ledger.PROCESSOR_RECEIVABLE, -amount),
                        Posting(Ledger.FEE_REVENUE, -fee)]
        fresh = self.ledger.post(f"evt:{e.id}", postings)
        if fresh:
            self.events.append(e)
        return fresh

    def receive_funding(self, funding_id: str, amount: Money) -> None:
        """The acquirer pays what it owes for a set of events: cash in, receivable down."""
        self.ledger.post(f"fund:{funding_id}", [
            Posting(Ledger.CASH, amount.minor_units),
            Posting(Ledger.PROCESSOR_RECEIVABLE, -amount.minor_units)])


# ---------------------------------------------------------------------------------------------
# Batch settlement: cutoff, per-merchant netting, payout instructions, settlement file.
# ---------------------------------------------------------------------------------------------


@dataclass(frozen=True)
class PayoutInstruction:
    sequence: int
    merchant_id: str
    routing: str
    account: str
    amount: Money


@dataclass(frozen=True)
class SettlementBatch:
    batch_id: str
    business_date: date
    cutoff: datetime
    event_ids: tuple
    instructions: tuple
    carried_forward: dict
    processor_funding: Money

    def total(self) -> Money:
        total = Money.zero(self.processor_funding.currency)
        for p in self.instructions:
            total = total.plus(p.amount)
        return total


class BatchSettler:
    def __init__(self, platform: Platform, minimum_payout: Money) -> None:
        self.platform = platform
        self.minimum_payout = minimum_payout
        self._settled_by: dict[str, str] = {}      # event id -> batch id
        self._carry: dict[str, Money] = {}         # merchant -> carried net
        self._batches: dict[str, SettlementBatch] = {}

    def carried(self, merchant_id: str) -> Money:
        return self._carry.get(merchant_id, Money.zero(self.platform.ledger.currency))

    def is_settled(self, event_id: str) -> bool:
        return event_id in self._settled_by

    def run(self, business_date: date, cutoff: datetime) -> SettlementBatch:
        """Close the books for one business date. Every booked, unsettled event before the cutoff for a merchant not
        on hold is netted into one amount per merchant. Positive nets at or above the minimum become payout
        instructions; the rest are carried to the next batch. Running the same business date twice returns the
        first batch unchanged: the batch id is the idempotency key."""
        batch_id = f"BATCH-{business_date}"
        existing = self._batches.get(batch_id)
        if existing is not None:
            return existing

        c = self.platform.ledger.currency
        net: dict[str, Money] = {}
        included: dict[str, list[str]] = {}
        funding = Money.zero(c)
        for e in self.platform.events:
            if e.id in self._settled_by or not e.at < cutoff:
                continue
            m = self.platform.merchants[e.merchant_id]
            if m.on_hold:
                continue
            net[e.merchant_id] = net.get(e.merchant_id, Money.zero(c)).plus(self.platform.fees.merchant_effect(e))
            included.setdefault(e.merchant_id, []).append(e.id)
            funding = funding.plus(FeeSchedule.processor_effect(e))
        for merchant_id, carried in self._carry.items():
            if not self.platform.merchants[merchant_id].on_hold:
                net[merchant_id] = net.get(merchant_id, Money.zero(c)).plus(carried)

        instructions: list[PayoutInstruction] = []
        carried_out: dict[str, Money] = {}
        for merchant_id in sorted(net):  # TreeMap order: one deterministic sequence per merchant id
            amount = net[merchant_id]
            m = self.platform.merchants[merchant_id]
            if amount.is_positive() and amount >= self.minimum_payout:
                p = PayoutInstruction(len(instructions) + 1, merchant_id, m.routing, m.account, amount)
                instructions.append(p)
                self.platform.ledger.post(f"payout:{batch_id}:{merchant_id}", [
                    Posting(Ledger.merchant_payable(merchant_id), amount.minor_units),
                    Posting(Ledger.PAYOUTS_IN_TRANSIT, -amount.minor_units)])
                self._carry.pop(merchant_id, None)
            elif amount.minor_units != 0:
                self._carry[merchant_id] = amount
                carried_out[merchant_id] = amount
            else:
                self._carry.pop(merchant_id, None)
        event_ids: list[str] = []
        for merchant_id in sorted(included):
            for event_id in included[merchant_id]:
                self._settled_by[event_id] = batch_id
                event_ids.append(event_id)
        batch = SettlementBatch(batch_id, business_date, cutoff, tuple(event_ids), tuple(instructions),
                                {k: carried_out[k] for k in sorted(carried_out)}, funding)
        self._batches[batch_id] = batch
        return batch

    def confirm_payouts(self, batch: SettlementBatch) -> None:
        """The bank confirms the payout file was executed: in-transit clears against cash."""
        total = batch.total().minor_units
        if total == 0:
            return
        self.platform.ledger.post(f"confirm:{batch.batch_id}", [
            Posting(Ledger.PAYOUTS_IN_TRANSIT, total),
            Posting(Ledger.CASH, -total)])

    def ties_out(self) -> bool:
        """The tie-out: for every merchant, the ledger's payable balance must equal what is carried plus the effect
        of events not yet settled. If this is False, a payout was lost or paid twice."""
        expected: dict[str, Money] = {mid: self.carried(mid) for mid in self.platform.merchants}
        for e in self.platform.events:
            if e.id not in self._settled_by:
                expected[e.merchant_id] = expected[e.merchant_id].plus(self.platform.fees.merchant_effect(e))
        for merchant_id, amount in expected.items():
            if self.platform.ledger.owed_to(merchant_id) != amount:
                return False
        return self.platform.ledger.trial_balance() == 0


class SettlementFile:
    """Renders a batch as a fixed-layout text file in the spirit of a Nacha file: a header, one detail line per
    payout, and a control record with entry count, total, and entry hash (the sum of the first eight digits of
    every routing number, modulo 10^10). The receiving bank recomputes all three."""

    @staticmethod
    def render(batch: SettlementBatch) -> str:
        currency = batch.processor_funding.currency.code
        out = [f"H|{batch.batch_id}|{batch.business_date}|{currency}\n"]
        total = 0
        for p in batch.instructions:
            out.append(f"D|{p.sequence:06d}|{p.merchant_id}|{p.routing}|{p.account}|{p.amount.minor_units}\n")
            total += p.amount.minor_units
        out.append(f"T|{len(batch.instructions)}|{total}|{SettlementFile.entry_hash(batch.instructions):010d}\n")
        return "".join(out)

    @staticmethod
    def entry_hash(instructions) -> int:
        return sum(int(p.routing[:8]) for p in instructions) % 10_000_000_000

    @staticmethod
    def validate(file: str) -> bool:
        """Receiver-side validation: recompute count, total and hash from the detail lines."""
        count = total = hash_ = 0
        trailer: Optional[list[str]] = None
        for line in file.split("\n"):
            f = line.split("|")
            if f[0] == "D":
                count += 1
                total += int(f[5])
                hash_ += int(f[3][:8])
            elif f[0] == "T":
                trailer = f
        return (trailer is not None and int(trailer[1]) == count and int(trailer[2]) == total
                and int(trailer[3]) == hash_ % 10_000_000_000)


# ---------------------------------------------------------------------------------------------
# Real-time settlement: pay each merchant's positive balance the moment it appears.
# ---------------------------------------------------------------------------------------------


class RealTimeSettler:
    def __init__(self, platform: Platform) -> None:
        self.platform = platform
        self._transfers = 0
        self._gross_paid = Money.zero(platform.ledger.currency)

    def on_event(self, e: SettlementEvent) -> None:
        """Book the event, then sweep the merchant's positive balance over an instant rail."""
        if not self.platform.book(e):
            return
        owed = self.platform.ledger.owed_to(e.merchant_id)
        if not owed.is_positive():
            return
        self._transfers += 1
        self._gross_paid = self._gross_paid.plus(owed)
        self.platform.ledger.post(f"rt:{e.id}", [
            Posting(Ledger.merchant_payable(e.merchant_id), owed.minor_units),
            Posting(Ledger.CASH, -owed.minor_units)])

    def transfers(self) -> int:
        return self._transfers

    def gross_paid(self) -> Money:
        return self._gross_paid

    def clawback_exposure(self) -> Money:
        """Sum of negative merchant balances: money already paid out that must be clawed back."""
        exposure = Money.zero(self.platform.ledger.currency)
        for merchant_id in self.platform.merchants:
            owed = self.platform.ledger.owed_to(merchant_id)
            if owed.is_negative():
                exposure = exposure.minus(owed)
        return exposure


@dataclass(frozen=True)
class LiquidityComparison:
    """Side-by-side numbers for the two pipelines over the same day of events."""

    batch_transfers: int
    batch_paid: Money
    batch_peak_prefunding: Money
    batch_cash_after: Money
    real_time_transfers: int
    real_time_paid: Money
    real_time_peak_prefunding: Money
    real_time_exposure: Money
    real_time_cash_after: Money

    @staticmethod
    def run(merchants: list, day: list, business_date: date, cutoff: datetime, c: Currency) -> "LiquidityComparison":
        batch_platform = Platform(c, FeeSchedule.standard(c))
        for m in merchants:
            batch_platform.add_merchant(m)
        settler = BatchSettler(batch_platform, Money.zero(c))
        for e in day:
            batch_platform.book(e)
        batch = settler.run(business_date, cutoff)
        batch_platform.receive_funding(batch.batch_id, batch.processor_funding)  # T+1 morning: acquirer pays first
        settler.confirm_payouts(batch)                                            # T+1 afternoon: payout file executes

        rt_platform = Platform(c, FeeSchedule.standard(c))
        for m in merchants:
            rt_platform.add_merchant(m)
        rt = RealTimeSettler(rt_platform)
        rt_funding = Money.zero(c)
        for e in day:
            if not e.at < cutoff:
                continue  # same scope as the batch, so the comparison is fair
            rt.on_event(e)
            rt_funding = rt_funding.plus(FeeSchedule.processor_effect(e))
        exposure = rt.clawback_exposure()
        rt_peak = rt_platform.ledger.peak_prefunding()
        rt_platform.receive_funding("T+1", rt_funding)

        return LiquidityComparison(len(batch.instructions), batch.total(), batch_platform.ledger.peak_prefunding(),
                                   batch_platform.ledger.balance(Ledger.CASH), rt.transfers(), rt.gross_paid(), rt_peak,
                                   exposure, rt_platform.ledger.balance(Ledger.CASH))


# ---------------------------------------------------------------------------------------------
# Demo
# ---------------------------------------------------------------------------------------------


class JavaRandom:
    """java.util.Random, bit for bit (the JDK's 48-bit LCG), so seed 7 generates the same day as the Java demo."""

    MULTIPLIER = 0x5DEECE66D
    ADDEND = 0xB
    MASK = (1 << 48) - 1

    def __init__(self, seed: int) -> None:
        self.seed = (seed ^ self.MULTIPLIER) & self.MASK

    def next(self, bits: int) -> int:
        self.seed = (self.seed * self.MULTIPLIER + self.ADDEND) & self.MASK
        value = self.seed >> (48 - bits)
        return value - (1 << 32) if value >= (1 << 31) else value  # Java's int is signed

    def next_int(self, bound: int) -> int:
        if bound <= 0:
            raise ValueError("bound must be positive")
        r = self.next(31)
        m = bound - 1
        if (bound & m) == 0:  # power of two
            return (bound * r) >> 31
        u = r
        r = u % bound
        while _to_int32(u - r + m) < 0:  # the overflow check Java performs in 32-bit arithmetic
            u = self.next(31)
            r = u % bound
        return r


def _to_int32(x: int) -> int:
    x &= 0xFFFFFFFF
    return x - (1 << 32) if x >= (1 << 31) else x


def instant(text: str) -> datetime:
    """Instant.parse for the 'YYYY-MM-DDTHH:MM:SSZ' form: an aware UTC datetime."""
    return datetime.fromisoformat(text.replace("Z", "+00:00"))


DAY = date(2026, 3, 31)
CUTOFF = instant("2026-03-31T23:00:00Z")


def at(hhmm: str) -> datetime:
    return instant(f"2026-03-31T{hhmm}:00Z")


def demo_merchants() -> list:
    return [
        Merchant("m_acme", "021000021", "100200300", False),
        Merchant("m_bolt", "026009593", "555000111", False),
        Merchant("m_cora", "011000138", "987654321", False),
    ]


def demo_day() -> list:
    return [
        SettlementEvent("c1", "m_acme", EventType.CAPTURE, Money.parse("120.00", USD), at("09:15")),
        SettlementEvent("c3", "m_bolt", EventType.CAPTURE, Money.parse("45.50", USD), at("10:05")),
        SettlementEvent("c2", "m_acme", EventType.CAPTURE, Money.parse("80.00", USD), at("11:40")),
        SettlementEvent("c4", "m_cora", EventType.CAPTURE, Money.parse("1000.00", USD), at("12:30")),
        SettlementEvent("c6", "m_cora", EventType.CAPTURE, Money.parse("250.00", USD), at("13:45")),
        SettlementEvent("r1", "m_acme", EventType.REFUND, Money.parse("20.00", USD), at("15:05")),
        SettlementEvent("cb1", "m_bolt", EventType.CHARGEBACK, Money.parse("60.00", USD), at("16:20")),
        SettlementEvent("c5", "m_cora", EventType.CAPTURE, Money.parse("10.00", USD), at("23:30")),
    ]


def generated_day(merchants: list, count: int, seed: int) -> list:
    """A seeded day: many merchants, many events, a realistic mix of refunds and chargebacks."""
    rnd = JavaRandom(seed)
    out = []
    day_start = int(instant("2026-03-31T00:00:00Z").timestamp())
    for i in range(count):
        m = merchants[rnd.next_int(len(merchants))]
        roll = rnd.next_int(1000)
        kind = EventType.CAPTURE if roll < 930 else EventType.REFUND if roll < 995 else EventType.CHARGEBACK
        minor = 500 + rnd.next_int(49_500)  # 5.00 .. 499.99
        when = datetime.fromtimestamp(day_start + i * 86_400 // count, tz=timezone.utc)
        out.append(SettlementEvent(f"g{i}", m.id, kind, Money.of_minor(minor, USD), when))
    return out


def generated_merchants(n: int) -> list:
    return [Merchant(f"m_{i:04d}", f"0{21000021 + i:08d}", f"ACCT{i}", False) for i in range(n)]


def print_comparison(cmp: LiquidityComparison) -> None:
    print(f"  {'':<26} {'batch (T+1)':<18} {'real time':<18}")
    print(f"  {'transfers sent':<26} {cmp.batch_transfers:<18} {cmp.real_time_transfers:<18}")
    print(f"  {'value paid to merchants':<26} {str(cmp.batch_paid):<18} {str(cmp.real_time_paid):<18}")
    print(f"  {'peak prefunding':<26} {str(cmp.batch_peak_prefunding):<18} {str(cmp.real_time_peak_prefunding):<18}")
    print(f"  {'clawback exposure':<26} {'0.00 USD':<18} {str(cmp.real_time_exposure):<18}")
    print(f"  {'cash after T+1 funding':<26} {str(cmp.batch_cash_after):<18} {str(cmp.real_time_cash_after):<18}")


def main() -> None:
    print("== One business day, three merchants, cutoff 23:00 ==")
    platform = Platform(USD, FeeSchedule.standard(USD))
    for m in demo_merchants():
        platform.add_merchant(m)
    for e in demo_day():
        platform.book(e)
        print(f"{e.at.strftime('%H:%M')} {e.type.name:<10} {e.merchant_id:<6} {str(e.amount):<12} fee {str(platform.fees.fee_for(e)):<9} "
              f"merchant {platform.fees.merchant_effect(e)}")
    settler = BatchSettler(platform, Money.zero(USD))
    batch = settler.run(DAY, CUTOFF)
    print(f"\nbatch {batch.batch_id}: {len(batch.event_ids)} events, {len(batch.instructions)} payouts totalling {batch.total()}")
    for p in batch.instructions:
        print(f"  pay {p.merchant_id} {p.amount}")
    for m, a in batch.carried_forward.items():
        print(f"  carry {m} {a} (merchant owes the platform)")
    print(f"  c5 at 23:30 settled? {str(settler.is_settled('c5')).lower()} (after cutoff: next batch)")
    print(f"acquirer owes for this batch: {batch.processor_funding}")

    print("\n== Settlement file ==")
    file = SettlementFile.render(batch)
    print(file, end="")
    print(f"receiver validation: {str(SettlementFile.validate(file)).lower()}")

    platform.receive_funding(batch.batch_id, batch.processor_funding)
    settler.confirm_payouts(batch)
    print("\n== Ledger after funding and payout ==")
    for acct, v in platform.ledger.balances_view().items():
        print(f"  {acct:<28} {Money.of_minor(v, USD)}")
    print(f"trial balance: {platform.ledger.trial_balance()}   ties out: {str(settler.ties_out()).lower()}")
    again = settler.run(DAY, CUTOFF)
    print(f"rerun of {again.batch_id} returns the same batch: {str(again is batch).lower()}"
          f", journals still {platform.ledger.journal_count()}")

    print("\n== Batch vs real time over the same events ==")
    print_comparison(LiquidityComparison.run(demo_merchants(), demo_day(), DAY, CUTOFF, USD))

    print("\n== 200 merchants, 50,000 events, seed 7 ==")
    many = generated_merchants(200)
    big = generated_day(many, 50_000, 7)
    big_platform = Platform(USD, FeeSchedule.standard(USD))
    for m in many:
        big_platform.add_merchant(m)
    for e in big:
        big_platform.book(e)
    big_settler = BatchSettler(big_platform, Money.parse("1.00", USD))
    big_batch = big_settler.run(DAY, instant("2026-04-01T00:00:00Z"))
    print(f"payouts {len(big_batch.instructions)}, total {big_batch.total()}, carried {len(big_batch.carried_forward)}"
          f", ties out {str(big_settler.ties_out()).lower()}"
          f", file valid {str(SettlementFile.validate(SettlementFile.render(big_batch))).lower()}")
    print_comparison(LiquidityComparison.run(many, big, DAY, instant("2026-04-01T00:00:00Z"), USD))


if __name__ == "__main__":
    main()
