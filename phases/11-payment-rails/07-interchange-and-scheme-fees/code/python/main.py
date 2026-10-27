"""Lesson: phases/11-payment-rails/07-interchange-and-scheme-fees/docs/en.md
Interchange, Scheme Fees, and Merchant Pricing: a fee engine that splits one card payment to the cent.
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: Regulation II (12 CFR 235.3-235.4, US debit cap); EU Regulation 2015/751 (IFR caps); scheme fee schedules.
Python 3.11+, standard library only. Rates in the demo table are illustrative, shaped like published schedules.
Money, Currency, Rounding are copied unchanged from phases/10-money-and-ledgers/01-representing-money/code/python/main.py.
"""

from __future__ import annotations

from dataclasses import dataclass
from enum import Enum
from functools import total_ordering
from typing import Dict, List, Sequence

# ---------------------------------------------------------------------------------------------
# Money (Phase 10, Lesson 01). Integer minor units, never floats.
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
INR = Currency("INR", 2)
JPY = Currency("JPY", 0)
KWD = Currency("KWD", 3)


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
    def sum(currency: Currency, *amounts: "Money") -> "Money":
        acc = Money(0, currency)
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
# The fee engine.
# ---------------------------------------------------------------------------------------------


class CardType(Enum):
    """The interchange category of the card, as the issuer's BIN declares it (Lesson 04 routes on the same BIN)."""

    CONSUMER_DEBIT_REGULATED = "consumer_debit_regulated"  # US: issuer >= $10bn assets, Reg II cap. EEA: all consumer debit (IFR)
    CONSUMER_DEBIT_EXEMPT = "consumer_debit_exempt"        # US debit from an issuer under $10bn: exempt from the cap
    CONSUMER_CREDIT = "consumer_credit"
    PREMIUM_CREDIT = "premium_credit"                      # rewards tiers: the issuer funds points from interchange
    COMMERCIAL_CREDIT = "commercial_credit"                # corporate and purchasing cards: capped neither in the US nor the EEA

    def is_consumer(self) -> bool:
        return self is not CardType.COMMERCIAL_CREDIT

    def is_debit(self) -> bool:
        return self in (CardType.CONSUMER_DEBIT_REGULATED, CardType.CONSUMER_DEBIT_EXEMPT)


class Region(Enum):
    """Where the issuer is relative to the merchant's acquirer."""

    US_DOMESTIC = "us_domestic"        # US card, US acquirer
    EEA_DOMESTIC = "eea_domestic"      # EEA card, EEA acquirer: IFR caps apply to consumer cards
    INTER_REGIONAL = "inter_regional"  # issuer and acquirer in different regions: cross-border scheme fee applies


class Channel(Enum):
    CARD_PRESENT = "card_present"
    CARD_NOT_PRESENT = "card_not_present"


@dataclass(frozen=True)
class Rate:
    """A price with a proportional part and a fixed part, such as 1.80% + 0.10.
    The proportional part is in parts per million (1.80% = 18_000 ppm, 0.05% = 500 ppm) so every rate a
    scheme publishes is an exact integer. The fixed part is in minor units of the transaction's currency."""

    ppm: int
    fixed_minor: int

    PPM = 1_000_000

    def __post_init__(self) -> None:
        if self.ppm < 0 or self.fixed_minor < 0:
            raise ValueError("rates are non-negative")

    @staticmethod
    def of(percent: str, fixed_minor: int) -> "Rate":
        """Rate.of("1.80", 10) is 1.80% + 10 minor units. Parses the percent exactly; never via a float."""
        whole, _, frac = percent.partition(".")
        if len(frac) > 4:
            raise ValueError(f"at most 4 decimals of a percent: {percent}")
        if not whole.isdigit() or (frac and not frac.isdigit()):
            raise ValueError(f"not a percent: {percent}")
        return Rate(int(whole) * 10_000 + int(frac.ljust(4, "0")), fixed_minor)

    def apply_to(self, amount: Money) -> Money:
        """amount x ppm / 1e6 rounded HALF_EVEN, plus the fixed part. Exactly one rounding per fee component."""
        proportional = amount.times(self.ppm, Rate.PPM, Rounding.HALF_EVEN)
        return proportional.plus(Money.of_minor(self.fixed_minor, amount.currency))

    def __str__(self) -> str:
        frac = f"{self.ppm % 10_000:04d}"
        while len(frac) > 2 and frac.endswith("0"):
            frac = frac[:-1]
        return f"{self.ppm // 10_000}.{frac}% + {self.fixed_minor}"


@dataclass(frozen=True)
class RateKey:
    card_type: CardType
    region: Region
    channel: Channel

    def __str__(self) -> str:  # the same rendering as Java's record toString, so messages match
        return f"RateKey[cardType={self.card_type.name}, region={self.region.name}, channel={self.channel.name}]"


class NoRateError(RuntimeError):
    def __init__(self, key: RateKey) -> None:
        super().__init__(f"no interchange rate for {key}")


class InterchangeTable:
    """The interchange table: what the acquirer pays the issuer, published by the scheme, looked up per transaction.
    Regulatory caps are checked when a rate is loaded, so a bad table fails at startup rather than at month end."""

    # EU Regulation 2015/751, Articles 3 and 4: consumer debit 0.2%, consumer credit 0.3%, no fixed part.
    IFR_DEBIT_CAP_PPM = 2_000
    IFR_CREDIT_CAP_PPM = 3_000
    # Regulation II, 12 CFR 235.3 and 235.4: 0.05% + 21 cents, plus 1 cent fraud-prevention adjustment.
    REG_II_CAP = Rate(500, 22)

    def __init__(self) -> None:
        self._rates: Dict[RateKey, Rate] = {}

    def put(self, type: CardType, region: Region, channel: Channel, rate: Rate) -> "InterchangeTable":
        key = RateKey(type, region, channel)
        InterchangeTable.check_caps(key, rate)
        self._rates[key] = rate
        return self

    def put_both(self, type: CardType, region: Region, rate: Rate) -> "InterchangeTable":
        """Put the same rate for both channels."""
        for ch in Channel:
            self.put(type, region, ch, rate)
        return self

    def lookup(self, type: CardType, region: Region, channel: Channel) -> Rate:
        key = RateKey(type, region, channel)
        rate = self._rates.get(key)
        if rate is None:
            raise NoRateError(key)
        return rate

    def size(self) -> int:
        return len(self._rates)

    @staticmethod
    def check_caps(key: RateKey, rate: Rate) -> None:
        if key.region is Region.EEA_DOMESTIC and key.card_type.is_consumer():
            cap = InterchangeTable.IFR_DEBIT_CAP_PPM if key.card_type.is_debit() else InterchangeTable.IFR_CREDIT_CAP_PPM
            if rate.ppm > cap or rate.fixed_minor > 0:
                raise ValueError(f"IFR cap exceeded for {key}: {rate}")
        if key.region is Region.US_DOMESTIC and key.card_type is CardType.CONSUMER_DEBIT_REGULATED:
            cap = InterchangeTable.REG_II_CAP
            if rate.ppm > cap.ppm or rate.fixed_minor > cap.fixed_minor:
                raise ValueError(f"Regulation II cap exceeded for {key}: {rate}")

    @staticmethod
    def demo() -> "InterchangeTable":
        """Illustrative table, shaped like published US and EEA schedules. There is no EEA "exempt debit" row."""
        t = InterchangeTable()
        us, eea, xb = Region.US_DOMESTIC, Region.EEA_DOMESTIC, Region.INTER_REGIONAL
        cp, cnp = Channel.CARD_PRESENT, Channel.CARD_NOT_PRESENT

        t.put_both(CardType.CONSUMER_DEBIT_REGULATED, us, Rate.of("0.05", 22))
        t.put(CardType.CONSUMER_DEBIT_EXEMPT, us, cp, Rate.of("0.80", 15))
        t.put(CardType.CONSUMER_DEBIT_EXEMPT, us, cnp, Rate.of("1.65", 15))
        t.put(CardType.CONSUMER_CREDIT, us, cp, Rate.of("1.51", 10))
        t.put(CardType.CONSUMER_CREDIT, us, cnp, Rate.of("1.80", 10))
        t.put(CardType.PREMIUM_CREDIT, us, cp, Rate.of("2.10", 10))
        t.put(CardType.PREMIUM_CREDIT, us, cnp, Rate.of("2.40", 10))
        t.put(CardType.COMMERCIAL_CREDIT, us, cp, Rate.of("2.50", 10))
        t.put(CardType.COMMERCIAL_CREDIT, us, cnp, Rate.of("2.70", 10))

        t.put_both(CardType.CONSUMER_DEBIT_REGULATED, eea, Rate.of("0.20", 0))
        t.put_both(CardType.CONSUMER_CREDIT, eea, Rate.of("0.30", 0))
        t.put_both(CardType.PREMIUM_CREDIT, eea, Rate.of("0.30", 0))
        t.put_both(CardType.COMMERCIAL_CREDIT, eea, Rate.of("1.50", 5))

        for debit in (CardType.CONSUMER_DEBIT_REGULATED, CardType.CONSUMER_DEBIT_EXEMPT):
            t.put(debit, xb, cp, Rate.of("0.20", 0))
            t.put(debit, xb, cnp, Rate.of("1.15", 0))
        for credit in (CardType.CONSUMER_CREDIT, CardType.PREMIUM_CREDIT):
            t.put(credit, xb, cp, Rate.of("0.30", 0))
            t.put(credit, xb, cnp, Rate.of("1.50", 0))
        t.put_both(CardType.COMMERCIAL_CREDIT, xb, Rate.of("2.00", 0))
        return t


@dataclass(frozen=True)
class SchemeFees:
    """What the network charges the acquirer per transaction: an assessment on volume, a fixed per-transaction
    processing fee, and a cross-border assessment when issuer and acquirer are in different regions."""

    assessment: Rate
    cross_border_ppm: int

    def fee_for(self, amount: Money, region: Region) -> Money:
        fee = self.assessment.apply_to(amount)
        if region is Region.INTER_REGIONAL:
            fee = fee.plus(amount.times(self.cross_border_ppm, Rate.PPM, Rounding.HALF_EVEN))
        return fee

    @staticmethod
    def demo() -> "SchemeFees":
        return SchemeFees(Rate.of("0.14", 2), 6_000)


class Pricing:
    """How the acquirer prices the merchant. Exactly two variants; FeeEngine.quote dispatches with match."""


@dataclass(frozen=True)
class IcPlusPlus(Pricing):
    """Interchange ++: pass interchange and scheme fees through at cost, add a disclosed markup."""

    markup: Rate


@dataclass(frozen=True)
class Blended(Pricing):
    """Blended: one rate for every card; the acquirer keeps whatever is left after interchange and scheme."""

    merchant_rate: Rate


Pricing.IcPlusPlus = IcPlusPlus  # type: ignore[attr-defined]
Pricing.Blended = Blended  # type: ignore[attr-defined]


@dataclass(frozen=True)
class Transaction:
    id: str
    amount: Money
    card_type: CardType
    region: Region
    channel: Channel

    def __post_init__(self) -> None:
        if not isinstance(self.amount, Money):
            raise TypeError("amount")
        if not self.amount.is_positive():
            raise ValueError(f"a purchase amount is positive: {self.amount}")


@dataclass(frozen=True)
class FeeBreakdown:
    """Who gets what from one transaction. The invariant, checked on construction:
    interchange + scheme_fees + acquirer_margin + merchant_net == gross, to the minor unit."""

    gross: Money
    interchange_rate: Rate
    interchange: Money
    scheme_fees: Money
    acquirer_margin: Money
    merchant_net: Money

    def __post_init__(self) -> None:
        total = Money.sum(self.gross.currency, self.interchange, self.scheme_fees, self.acquirer_margin, self.merchant_net)
        if total != self.gross:
            raise RuntimeError(f"fee split does not sum to gross: {total} != {self.gross}")

    def merchant_discount(self) -> Money:
        """The merchant discount: everything the merchant does not receive."""
        return self.gross.minus(self.merchant_net)

    def effective_rate_bps(self) -> int:
        """Basis points of gross, rounded HALF_EVEN, for reporting only."""
        return divide_rounded(self.merchant_discount().minor_units * 10_000, self.gross.minor_units, Rounding.HALF_EVEN)


class FeeEngine:
    def __init__(self, table: InterchangeTable, scheme: SchemeFees) -> None:
        self._table = table
        self._scheme = scheme

    def quote(self, tx: Transaction, pricing: Pricing) -> FeeBreakdown:
        gross = tx.amount
        ic_rate = self._table.lookup(tx.card_type, tx.region, tx.channel)
        interchange = ic_rate.apply_to(gross)
        scheme_fees = self._scheme.fee_for(gross, tx.region)
        match pricing:
            case IcPlusPlus(markup=markup):
                merchant_discount = interchange.plus(scheme_fees).plus(markup.apply_to(gross))
            case Blended(merchant_rate=merchant_rate):
                merchant_discount = merchant_rate.apply_to(gross)
            case _:
                raise TypeError(f"unknown pricing model: {pricing!r}")
        acquirer_margin = merchant_discount.minus(interchange).minus(scheme_fees)
        merchant_net = gross.minus(merchant_discount)
        return FeeBreakdown(gross, ic_rate, interchange, scheme_fees, acquirer_margin, merchant_net)

    @staticmethod
    def attribute(aggregate_fee: Money, txs: Sequence[Transaction]) -> List[Money]:
        """Attribute a fee billed in aggregate (a monthly scheme fee, a statement fee) back to the transactions that
        caused it, in proportion to their amounts. Largest-remainder allocation: the parts sum exactly to the fee."""
        weights = []
        for tx in txs:
            if tx.amount.currency != aggregate_fee.currency:
                raise CurrencyMismatchError(tx.amount.currency, aggregate_fee.currency)
            weights.append(tx.amount.minor_units)
        return aggregate_fee.allocate(*weights)

    @staticmethod
    def total(quotes: Sequence[FeeBreakdown]) -> FeeBreakdown:
        """Sum one month of quotes into the totals an IC++ merchant statement shows."""
        c = quotes[0].gross.currency
        g = ic = sf = am = net = Money.of_minor(0, c)
        for q in quotes:
            g, ic, sf = g.plus(q.gross), ic.plus(q.interchange), sf.plus(q.scheme_fees)
            am, net = am.plus(q.acquirer_margin), net.plus(q.merchant_net)
        return FeeBreakdown(g, Rate(0, 0), ic, sf, am, net)


def _print(label: str, f: FeeBreakdown) -> None:
    print(f"{label:<34} gross {str(f.gross):<11} IC {str(f.interchange):<9} scheme {str(f.scheme_fees):<8} "
          f"acquirer {str(f.acquirer_margin):<9} merchant {str(f.merchant_net):<11} "
          f"({f.effective_rate_bps()} bps, IC rate {f.interchange_rate})")


def main() -> None:
    engine = FeeEngine(InterchangeTable.demo(), SchemeFees.demo())
    icpp: Pricing = IcPlusPlus(Rate.of("0.20", 5))
    blended: Pricing = Blended(Rate.of("2.90", 30))
    hundred = Money.parse("100.00", USD)

    print("== Who gets paid on 100.00 USD (US consumer credit, card-not-present, IC++) ==")
    f = engine.quote(Transaction("t1", hundred, CardType.CONSUMER_CREDIT, Region.US_DOMESTIC, Channel.CARD_NOT_PRESENT), icpp)
    print(f"issuer   (interchange 1.80% + 0.10)   {f.interchange}")
    print(f"network  (assessment 0.14% + 0.02)    {f.scheme_fees}")
    print(f"acquirer (markup 0.20% + 0.05)        {f.acquirer_margin}")
    print(f"merchant (net settlement)             {f.merchant_net}")
    print(f"sum                                   {Money.sum(USD, f.interchange, f.scheme_fees, f.acquirer_margin, f.merchant_net)}")

    print("\n== The same 100.00 USD across card types, CNP ==")
    for type_ in CardType:
        _print(f"{type_.name} IC++", engine.quote(Transaction("t", hundred, type_, Region.US_DOMESTIC, Channel.CARD_NOT_PRESENT), icpp))

    print("\n== Blended 2.90% + 0.30: the merchant pays the same, the acquirer's margin moves ==")
    for type_ in CardType:
        _print(f"{type_.name} blended", engine.quote(Transaction("t", hundred, type_, Region.US_DOMESTIC, Channel.CARD_NOT_PRESENT), blended))

    print("\n== Small tickets: 2.00 USD regulated debit ==")
    two = Money.parse("2.00", USD)
    _print("regulated debit IC++", engine.quote(Transaction("t", two, CardType.CONSUMER_DEBIT_REGULATED, Region.US_DOMESTIC, Channel.CARD_PRESENT), icpp))
    _print("regulated debit blended", engine.quote(Transaction("t", two, CardType.CONSUMER_DEBIT_REGULATED, Region.US_DOMESTIC, Channel.CARD_PRESENT), blended))

    print("\n== EEA: IFR caps and cross-border ==")
    eur = Money.parse("100.00", EUR)
    _print("EEA premium credit CP", engine.quote(Transaction("t", eur, CardType.PREMIUM_CREDIT, Region.EEA_DOMESTIC, Channel.CARD_PRESENT), icpp))
    _print("inter-regional credit CNP", engine.quote(Transaction("t", eur, CardType.CONSUMER_CREDIT, Region.INTER_REGIONAL, Channel.CARD_NOT_PRESENT), icpp))
    try:
        InterchangeTable().put(CardType.CONSUMER_CREDIT, Region.EEA_DOMESTIC, Channel.CARD_PRESENT, Rate.of("0.35", 0))
    except ValueError as e:
        print(f"rejected: {e}")
    try:
        engine.quote(Transaction("t", eur, CardType.CONSUMER_DEBIT_EXEMPT, Region.EEA_DOMESTIC, Channel.CARD_PRESENT), icpp)
    except NoRateError as e:
        print(f"rejected: {e}")

    print("\n== Rounding: 37.77 USD consumer credit CNP ==")
    odd = engine.quote(Transaction("t", Money.parse("37.77", USD), CardType.CONSUMER_CREDIT, Region.US_DOMESTIC, Channel.CARD_NOT_PRESENT), icpp)
    print(f"interchange = 37.77 x 1.80% = 0.67986 -> 0.68, + 0.10 = {odd.interchange}")
    _print("37.77", odd)

    print("\n== Attributing a 25.00 USD monthly scheme fee to three transactions ==")
    month = [
        Transaction("a", Money.parse("100.00", USD), CardType.CONSUMER_CREDIT, Region.US_DOMESTIC, Channel.CARD_PRESENT),
        Transaction("b", Money.parse("37.77", USD), CardType.PREMIUM_CREDIT, Region.US_DOMESTIC, Channel.CARD_NOT_PRESENT),
        Transaction("c", Money.parse("2.00", USD), CardType.CONSUMER_DEBIT_REGULATED, Region.US_DOMESTIC, Channel.CARD_PRESENT),
    ]
    parts = FeeEngine.attribute(Money.parse("25.00", USD), month)
    for tx, part in zip(month, parts):
        print(f"{tx.id} {tx.amount} -> {part}")
    print(f"sum = {Money.sum(USD, *parts)}")
    quotes = [engine.quote(tx, icpp) for tx in month]
    _print("month total IC++", FeeEngine.total(quotes))


if __name__ == "__main__":
    main()
