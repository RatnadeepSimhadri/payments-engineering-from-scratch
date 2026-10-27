"""Lesson: phases/10-money-and-ledgers/10-fees-and-splits/docs/en.md
Fees, Splits, and Platform Payments
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: largest-remainder apportionment (Hamilton); EU VAT Directive 2006/112/EC (tax-inclusive prices);
Money and allocate from Lesson 01, ledger shape from Lesson 03. Python 3.11+, standard library only.
"""

from __future__ import annotations

from dataclasses import dataclass
from enum import Enum
from functools import total_ordering
from typing import Callable, Dict, Iterable, List, Mapping, Optional, Tuple

# ---------------------------------------------------------------------------------------------
# Money (copied from Phase 10, Lesson 01). Integer minor units + ISO 4217 currency. Never a float.
# ---------------------------------------------------------------------------------------------


@dataclass(frozen=True)
class Currency:
    code: str
    exponent: int

    def __post_init__(self) -> None:
        if len(self.code) != 3 or not self.code.isalpha() or not self.code.isupper():
            raise ValueError(f"currency code must be three uppercase letters: {self.code}")
        if not 0 <= self.exponent <= 4:
            raise ValueError(f"exponent must be 0..4: {self.exponent}")

    def minor_units_per_major(self) -> int:
        return 10 ** self.exponent


USD = Currency("USD", 2)
EUR = Currency("EUR", 2)
GBP = Currency("GBP", 2)
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
    """Integer division with an explicit rounding rule, on magnitudes so negatives truncate like Java."""
    if divisor == 0:
        raise ZeroDivisionError("divisor must not be zero")
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
            raise ValueError(f"not a decimal amount: {text}")
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

    def abs(self) -> "Money":
        return self.negate() if self.minor_units < 0 else self

    def is_zero(self) -> bool:
        return self.minor_units == 0

    def is_negative(self) -> bool:
        return self.minor_units < 0

    def is_positive(self) -> bool:
        return self.minor_units > 0

    def times(self, numerator: int, denominator: int, rounding: Rounding) -> "Money":
        if denominator <= 0:
            raise ArithmeticError("denominator must be positive")
        return Money(divide_rounded(self.minor_units * numerator, denominator, rounding), self.currency)

    def allocate(self, *ratios: int) -> List["Money"]:
        """Largest-remainder split (Lesson 01): the parts always sum exactly to the whole; ties go to the lower index."""
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
        order = sorted(range(len(ratios)), key=lambda i: -remainders[i])  # stable: ties keep index order
        for i in order[:leftover]:
            shares[i] += 1
        sign = -1 if self.minor_units < 0 else 1
        return [Money(sign * s, self.currency) for s in shares]

    @staticmethod
    def sum(currency: Currency, *amounts: "Money") -> "Money":
        acc = Money.zero(currency)
        for m in amounts:
            acc = acc.plus(m)
        return acc

    def __lt__(self, other: "Money") -> bool:
        self._require_same_currency(other)
        return self.minor_units < other.minor_units

    def __str__(self) -> str:
        magnitude = abs(self.minor_units)
        scale = self.currency.minor_units_per_major()
        whole, frac = divmod(magnitude, scale)
        text = str(whole)
        if self.currency.exponent > 0:
            text += "." + str(frac).rjust(self.currency.exponent, "0")
        return f"{'-' if self.minor_units < 0 else ''}{text} {self.currency.code}"

    __repr__ = __str__


# ---------------------------------------------------------------------------------------------
# Fee schedules, the split of every line into parts that sum to it, and payouts.
# ---------------------------------------------------------------------------------------------


@dataclass(frozen=True)
class FeeSchedule:
    """percent + fixed, clamped to [minimum, maximum], and never more than the amount it is charged on.
    Percent is in basis points (1 bp = 0.01%), an exact rational bps/10000."""

    name: str
    bps: int
    fixed: Money
    minimum: Optional[Money]
    maximum: Optional[Money]
    rounding: Rounding

    def __post_init__(self) -> None:
        if self.bps < 0 or self.bps > 10_000:
            raise ValueError(f"bps must be 0..10000: {self.bps}")
        if self.fixed.is_negative():
            raise ValueError("fixed fee must not be negative")
        if self.minimum is not None and self.maximum is not None and self.minimum > self.maximum:
            raise ValueError(f"minimum {self.minimum} exceeds maximum {self.maximum}")

    @staticmethod
    def percent_plus_fixed(name: str, bps: int, fixed: Money) -> "FeeSchedule":
        return FeeSchedule(name, bps, fixed, None, None, Rounding.HALF_EVEN)

    def with_bounds(self, minimum: Money, maximum: Money) -> "FeeSchedule":
        return FeeSchedule(self.name, self.bps, self.fixed, minimum, maximum, self.rounding)

    def fee_for(self, amount: Money) -> Money:
        if amount.is_negative():
            raise ValueError(f"fee base must not be negative: {amount}")
        fee = amount.times(self.bps, 10_000, self.rounding).plus(self.fixed)
        if self.minimum is not None and fee < self.minimum:
            fee = self.minimum
        if self.maximum is not None and fee > self.maximum:
            fee = self.maximum
        if fee > amount:  # a fee never exceeds what it is charged on
            fee = amount
        return fee


@dataclass(frozen=True)
class LineItem:
    sku: str
    seller_id: str
    price: Money


@dataclass(frozen=True)
class Order:
    id: str
    lines: Tuple[LineItem, ...]
    discount: Money

    def __post_init__(self) -> None:
        object.__setattr__(self, "lines", tuple(self.lines))
        if not self.lines:
            raise ValueError("order needs at least one line")
        if self.discount.is_negative():
            raise ValueError("discount must not be negative")

    def subtotal(self) -> Money:
        acc = Money.zero(self.discount.currency)
        for l in self.lines:
            acc = acc.plus(l.price)
        return acc

    def charged(self) -> Money:
        return self.subtotal().minus(self.discount)


@dataclass(frozen=True)
class SplitPolicy:
    """Prices are tax-inclusive (VAT-style). The platform commission is charged on the amount net of tax.
    A referral partner, if a seller has one, receives referral_share_bps of the commission."""

    commission: FeeSchedule
    tax_bps: int
    referral_share_bps: int
    referrer_by_seller: Mapping[str, str]
    processor_fee: FeeSchedule

    def __post_init__(self) -> None:
        object.__setattr__(self, "referrer_by_seller", dict(self.referrer_by_seller))
        if self.tax_bps < 0 or self.referral_share_bps < 0 or self.referral_share_bps > 10_000:
            raise ValueError("bad bps")


@dataclass(frozen=True)
class LineSplit:
    """One line's money, in parts. Invariant: tax + platform_fee + referral_fee + seller_net == charged."""

    line: LineItem
    discount_share: Money
    charged: Money
    tax: Money
    platform_fee: Money
    referral_fee: Money
    referrer: Optional[str]
    seller_net: Money

    def sum_of_parts(self) -> Money:
        return self.tax.plus(self.platform_fee).plus(self.referral_fee).plus(self.seller_net)


@dataclass(frozen=True)
class OrderSplit:
    order: Order
    lines: Tuple[LineSplit, ...]
    processor_fee: Money

    def total(self, part: Callable[[LineSplit], Money]) -> Money:
        acc = Money.zero(self.order.discount.currency)
        for l in self.lines:
            acc = acc.plus(part(l))
        return acc

    def seller_nets(self) -> Dict[str, Money]:
        out: Dict[str, Money] = {}
        for l in self.lines:
            out[l.line.seller_id] = out[l.line.seller_id].plus(l.seller_net) if l.line.seller_id in out else l.seller_net
        return out

    def referral_fees(self) -> Dict[str, Money]:
        out: Dict[str, Money] = {}
        for l in self.lines:
            if l.referrer is not None and not l.referral_fee.is_zero():
                out[l.referrer] = out[l.referrer].plus(l.referral_fee) if l.referrer in out else l.referral_fee
        return out


class Splitter:
    def __init__(self, policy: SplitPolicy) -> None:
        self._policy = policy

    def split(self, order: Order) -> OrderSplit:
        """1. Allocate the order discount across lines in proportion to price.
        2. Per line: allocate charged into [net of tax, tax] by ratio 10000 : tax_bps.
        3. Commission = schedule(net of tax). Allocate it into [platform, referral] if the seller has a referrer.
        4. Seller net = net of tax - commission: the remainder, so the parts sum to the line exactly."""
        policy = self._policy
        cur = order.discount.currency
        if order.discount > order.subtotal():
            raise ValueError("discount exceeds subtotal")
        weights = []
        for line in order.lines:
            if not line.price.is_positive():
                raise ValueError(f"line price must be positive: {line.price}")
            weights.append(line.price.minor_units)
        discount_shares = _zeros(cur, len(weights)) if order.discount.is_zero() else order.discount.allocate(*weights)
        out: List[LineSplit] = []
        for i, line in enumerate(order.lines):
            charged = line.price.minus(discount_shares[i])
            tax_parts = _zeros(cur, 2) if charged.is_zero() else charged.allocate(10_000, policy.tax_bps)
            net_of_tax, tax = tax_parts[0], tax_parts[1]
            commission = policy.commission.fee_for(net_of_tax)
            referrer = policy.referrer_by_seller.get(line.seller_id)
            platform, referral = commission, Money.zero(cur)
            if referrer is not None and not commission.is_zero():
                parts = commission.allocate(10_000 - policy.referral_share_bps, policy.referral_share_bps)
                platform, referral = parts[0], parts[1]
            seller_net = net_of_tax.minus(commission)
            out.append(LineSplit(line, discount_shares[i], charged, tax, platform, referral, referrer, seller_net))
        processor_fee = policy.processor_fee.fee_for(order.charged())
        return OrderSplit(order, tuple(out), processor_fee)


def _zeros(c: Currency, n: int) -> List[Money]:
    return [Money.zero(c)] * n


@dataclass(frozen=True)
class PayoutPolicy:
    """Payout policy: hold a rolling reserve, charge a payout fee, and skip payouts below a minimum (carry them over)."""

    reserve_bps: int
    payout_fee: FeeSchedule
    minimum_payout: Money


@dataclass(frozen=True)
class Payout:
    """available == reserve + fee + net + carried_over, always."""

    seller_id: str
    available: Money
    reserve: Money
    fee: Money
    net: Money
    carried_over: Money

    def paid(self) -> bool:
        return self.net.is_positive()


class PayoutCalculator:
    @staticmethod
    def compute(seller_id: str, available: Money, policy: PayoutPolicy) -> Payout:
        zero = Money.zero(available.currency)
        if not available.is_positive():
            return Payout(seller_id, available, zero, zero, zero, available)
        reserve = available.times(policy.reserve_bps, 10_000, Rounding.UP)  # round the hold in the platform's favour
        after_reserve = available.minus(reserve)
        fee = policy.payout_fee.fee_for(after_reserve)
        net = after_reserve.minus(fee)
        if net < policy.minimum_payout:
            return Payout(seller_id, available, zero, zero, zero, available)  # nothing moves; try again next cycle
        return Payout(seller_id, available, reserve, fee, net, zero)


# ---------------------------------------------------------------------------------------------
# A minimal double-entry ledger (Lesson 03) to show that the split posts as one balanced entry.
# ---------------------------------------------------------------------------------------------


class Side(Enum):
    DEBIT = "debit"
    CREDIT = "credit"


@dataclass(frozen=True)
class Posting:
    account_id: str
    side: Side
    amount: Money

    def __post_init__(self) -> None:
        if not self.amount.is_positive():
            raise ValueError(f"posting amount must be positive, got {self.amount}")

    @staticmethod
    def debit(account_id: str, amount: Money) -> "Posting":
        return Posting(account_id, Side.DEBIT, amount)

    @staticmethod
    def credit(account_id: str, amount: Money) -> "Posting":
        return Posting(account_id, Side.CREDIT, amount)

    def signed_minor(self) -> int:
        return self.amount.minor_units if self.side is Side.DEBIT else -self.amount.minor_units


@dataclass(frozen=True)
class JournalEntry:
    id: str
    postings: Tuple[Posting, ...]

    def __init__(self, id: str, postings: Iterable[Posting]) -> None:
        postings = tuple(postings)
        net = sum(p.signed_minor() for p in postings)
        if len(postings) < 2 or net != 0:
            raise ValueError(f"entry {id} unbalanced by {net}")
        object.__setattr__(self, "id", id)
        object.__setattr__(self, "postings", postings)


class Ledger:
    def __init__(self) -> None:
        self._net_debits: Dict[str, int] = {}
        self._journal: List[JournalEntry] = []

    def post(self, e: JournalEntry) -> None:
        self._journal.append(e)
        for p in e.postings:
            self._net_debits[p.account_id] = self._net_debits.get(p.account_id, 0) + p.signed_minor()

    def net_debit(self, account_id: str) -> int:
        return self._net_debits.get(account_id, 0)

    def trial_balance_net(self) -> int:
        return sum(self._net_debits.values())

    def net_debits(self) -> Dict[str, int]:
        return self._net_debits

    @staticmethod
    def capture_entry(s: OrderSplit) -> JournalEntry:
        """Build the capture entry for a split: one debit to the receivable, one credit per party, nothing dropped."""
        ps: List[Posting] = [Posting.debit("1200 Receivable", s.order.charged())]
        _add_credit(ps, "2300 VAT Payable", s.total(lambda l: l.tax))
        _add_credit(ps, "4100 Commission Revenue", s.total(lambda l: l.platform_fee))
        for partner, fee in s.referral_fees().items():
            _add_credit(ps, f"2400 Referral Payable {partner}", fee)
        for seller, net in s.seller_nets().items():
            _add_credit(ps, f"2100 Seller Payable {seller}", net)
        if s.processor_fee.is_positive():
            ps.append(Posting.debit("5100 Processing Expense", s.processor_fee))
            ps.append(Posting.credit("1200 Receivable", s.processor_fee))
        return JournalEntry(f"cap-{s.order.id}", ps)

    @staticmethod
    def payout_entry(p: Payout) -> JournalEntry:
        ps: List[Posting] = []
        payable = f"2100 Seller Payable {p.seller_id}"
        ps.append(Posting.debit(payable, p.reserve.plus(p.fee).plus(p.net)))
        _add_credit(ps, f"2150 Seller Reserve {p.seller_id}", p.reserve)
        _add_credit(ps, "4200 Payout Fee Revenue", p.fee)
        _add_credit(ps, "1010 Bank", p.net)
        return JournalEntry(f"out-{p.seller_id}", ps)


def _add_credit(ps: List[Posting], account: str, amount: Money) -> None:
    if amount.is_positive():
        ps.append(Posting.credit(account, amount))


def _amt(m: Money) -> str:
    return str(m)[:-4]


def _java_map(d: Mapping[str, Money]) -> str:
    """Render a dict the way Java prints a Map, so the demo output matches line for line."""
    return "{" + ", ".join(f"{k}={v}" for k, v in d.items()) + "}"


def main() -> None:
    min_fee, max_fee = Money.parse("0.50", EUR), Money.parse("25.00", EUR)
    policy = SplitPolicy(
        FeeSchedule.percent_plus_fixed("commission 12% + 0.30", 1_200, Money.parse("0.30", EUR)).with_bounds(min_fee, max_fee),
        1_900,                                                  # German VAT 19%, included in prices
        2_000,                                                  # referral partner gets 20% of commission
        {"seller-B": "partner-P"},
        FeeSchedule.percent_plus_fixed("processor 1.4% + 0.25", 140, Money.parse("0.25", EUR)))

    print("== Fee schedule: 12% + 0.30, min 0.50, max 25.00 ==")
    for amount in ("1.00", "2.00", "50.00", "300.00", "0.40"):
        a = Money.parse(amount, EUR)
        print(f"  fee on {str(a):<11} = {policy.commission.fee_for(a)}")

    print("\n== Order o-1: three lines, two sellers, 10.00 coupon, prices include 19% VAT ==")
    order = Order("o-1", (
        LineItem("lamp", "seller-A", Money.parse("59.99", EUR)),
        LineItem("bulb", "seller-A", Money.parse("4.99", EUR)),
        LineItem("rug", "seller-B", Money.parse("119.00", EUR))), Money.parse("10.00", EUR))
    split = Splitter(policy).split(order)
    row = "  {:<5} {:<9} {:>8} {:>9} {:>9} {:>8} {:>9} {:>9} {:>10} {:>6}"
    print(row.format("sku", "seller", "price", "discount", "charged", "VAT", "platform", "referral", "seller net", "sums"))
    for l in split.lines:
        print(row.format(l.line.sku, l.line.seller_id, _amt(l.line.price), _amt(l.discount_share), _amt(l.charged), _amt(l.tax),
                         _amt(l.platform_fee), _amt(l.referral_fee), _amt(l.seller_net), str(l.sum_of_parts() == l.charged).lower()))
    print(f"  order charged {order.charged()}; VAT {split.total(lambda l: l.tax)}; platform {split.total(lambda l: l.platform_fee)}"
          f"; referral {_java_map(split.referral_fees())}; sellers {_java_map(split.seller_nets())}")
    print(f"  processor fee on the charge: {split.processor_fee}")

    print("\n== Posted as one balanced entry ==")
    ledger = Ledger()
    cap = Ledger.capture_entry(split)
    ledger.post(cap)
    for p in cap.postings:
        print(f"{'  Dr ' if p.side is Side.DEBIT else '      Cr '}{p.account_id}  {p.amount}")
    print(f"  trial balance nets to {ledger.trial_balance_net()}")

    print("\n== Payouts: 10% rolling reserve (rounded up), payout fee 0.25, minimum payout 20.00 ==")
    payout_policy = PayoutPolicy(1_000, FeeSchedule.percent_plus_fixed("payout", 0, Money.parse("0.25", EUR)), Money.parse("20.00", EUR))
    for seller in split.seller_nets():
        available = Money.of_minor(-ledger.net_debit(f"2100 Seller Payable {seller}"), EUR)
        p = PayoutCalculator.compute(seller, available, payout_policy)
        print(f"  {seller}: available {p.available} -> reserve {p.reserve}, fee {p.fee}, to bank {p.net}, carried {p.carried_over}")
        if p.paid():
            ledger.post(Ledger.payout_entry(p))
    small = PayoutCalculator.compute("seller-C", Money.parse("12.40", EUR), payout_policy)
    print(f"  seller-C: available {small.available} -> below minimum, carried {small.carried_over}")
    print(f"  trial balance after payouts nets to {ledger.trial_balance_net()}")

    print("\n== Why allocate, not round each part: 0.10 split three ways ==")
    dime = Money.parse("0.10", EUR)
    naive = dime.times(1, 3, Rounding.HALF_EVEN)
    print(f"  rounding each third: {naive} x 3 = {naive.times(3, 1, Rounding.HALF_EVEN)} (a cent lost)")
    print(f"  allocate(1,1,1): {dime.allocate(1, 1, 1)} sum {Money.sum(EUR, *dime.allocate(1, 1, 1))}")


if __name__ == "__main__":
    main()
