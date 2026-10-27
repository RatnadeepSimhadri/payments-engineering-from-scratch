"""Lesson: phases/12-payment-systems-design/12-currency-conversion-at-checkout/docs/en.md
Currency Conversion at Checkout and DCC
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: ISO 4217 (currency exponents); Regulation (EU) 2019/518 (currency conversion disclosure as a
         markup over the ECB reference rate); ECB euro foreign exchange reference rates.
Python 3.11+, standard library only. Same design as the Java version; Rational is exact integer arithmetic.
"""

from __future__ import annotations

import math
from dataclasses import dataclass
from enum import Enum
from fractions import Fraction
from typing import Protocol


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
GBP = Currency("GBP", 2)
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

    def allocate(self, *ratios: int) -> list["Money"]:
        """Largest-remainder split (Phase 10, Lesson 01): shares sum to exactly this amount."""
        if any(r < 0 for r in ratios):
            raise ValueError("ratios must be non-negative")
        total = sum(ratios)
        if not ratios or total == 0:
            raise ValueError("need a positive ratio")
        magnitude = abs(self.minor_units)
        shares = [(magnitude * r) // total for r in ratios]
        remainders = [(magnitude * r) % total for r in ratios]
        order = sorted(range(len(ratios)), key=lambda i: -remainders[i])
        for k in range(magnitude - sum(shares)):
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
# Exact rationals. A rate is a ratio; rounding happens once, on the final amount, never on the rate.
# ---------------------------------------------------------------------------------------------


class RoundingMode(Enum):
    """The java.math.RoundingMode subset this lesson uses; every rounding names its mode."""

    CEILING = "ceiling"
    FLOOR = "floor"
    UP = "up"
    DOWN = "down"
    HALF_UP = "half_up"
    HALF_DOWN = "half_down"
    HALF_EVEN = "half_even"


@dataclass(frozen=True)
class Rational:
    num: int
    den: int

    def __post_init__(self) -> None:
        num, den = self.num, self.den
        if den == 0:
            raise ZeroDivisionError("zero denominator")
        if den < 0:
            num, den = -num, -den
        g = math.gcd(num, den)
        if g != 1:
            num, den = num // g, den // g
        object.__setattr__(self, "num", num)
        object.__setattr__(self, "den", den)

    @staticmethod
    def of(num: int, den: int) -> "Rational":
        return Rational(num, den)

    @staticmethod
    def parse(decimal: str) -> "Rational":
        """Exact parse of a decimal string: "1.0850" is 10850/10000 = 217/200."""
        f = Fraction(decimal.strip())
        return Rational(f.numerator, f.denominator)

    @staticmethod
    def bps(basis_points: int) -> "Rational":
        """basis points as a fraction: 350 bps = 350/10000."""
        return Rational.of(basis_points, 10_000)

    @staticmethod
    def pow10(exponent: int) -> "Rational":
        return Rational(10 ** exponent, 1) if exponent >= 0 else Rational(1, 10 ** -exponent)

    def times(self, o: "Rational") -> "Rational":
        return Rational(self.num * o.num, self.den * o.den)

    def divided_by(self, o: "Rational") -> "Rational":
        return Rational(self.num * o.den, self.den * o.num)

    def plus(self, o: "Rational") -> "Rational":
        return Rational(self.num * o.den + o.num * self.den, self.den * o.den)

    def minus(self, o: "Rational") -> "Rational":
        return self.plus(Rational(-o.num, o.den))

    def inverse(self) -> "Rational":
        return Rational(self.den, self.num)

    def signum(self) -> int:
        return (self.num > 0) - (self.num < 0)

    def round_to_long(self, mode: RoundingMode) -> int:
        """Round to an integer with an explicit mode, by integer arithmetic only (den is always positive)."""
        q, r = divmod(self.num, self.den)  # floor division: 0 <= r < den
        if r == 0:
            return q
        if mode is RoundingMode.FLOOR:
            return q
        if mode is RoundingMode.CEILING:
            return q + 1
        negative = self.num < 0
        if mode is RoundingMode.DOWN:
            return q + 1 if negative else q
        if mode is RoundingMode.UP:
            return q if negative else q + 1
        twice = 2 * r
        if twice > self.den:
            return q + 1
        if twice < self.den:
            return q
        # exactly half way
        if mode is RoundingMode.HALF_UP:
            return q if negative else q + 1
        if mode is RoundingMode.HALF_DOWN:
            return q + 1 if negative else q
        return q if q % 2 == 0 else q + 1  # HALF_EVEN

    def to_decimal(self, scale: int) -> str:
        """Plain decimal string with `scale` places, rounded HALF_EVEN (BigDecimal.divide + toPlainString)."""
        n = self.times(Rational.pow10(scale)).round_to_long(RoundingMode.HALF_EVEN)
        digits = str(abs(n)).rjust(scale + 1, "0")
        text = digits if scale == 0 else f"{digits[:-scale]}.{digits[-scale:]}"
        return ("-" if n < 0 else "") + text

    def compare_to(self, o: "Rational") -> int:
        left, right = self.num * o.den, o.num * self.den
        return (left > right) - (left < right)

    def __str__(self) -> str:
        return f"{self.num}/{self.den}"

    __repr__ = __str__


ONE = Rational(1, 1)
Rational.ONE = ONE  # type: ignore[attr-defined]


@dataclass(frozen=True)
class FxRate:
    """1 unit of `from_currency` = `rate` units of `to_currency`, in major units."""

    from_currency: Currency
    to_currency: Currency
    rate: Rational

    def __post_init__(self) -> None:
        if self.rate.signum() <= 0:
            raise ValueError("rate must be positive")
        if self.from_currency == self.to_currency and self.rate.compare_to(ONE) != 0:
            raise ValueError("same-currency rate must be 1")

    def inverse(self) -> "FxRate":
        return FxRate(self.to_currency, self.from_currency, self.rate.inverse())

    def adjusted_by(self, basis_points: int) -> "FxRate":
        """Multiply the rate by (1 + bps/10000); a negative bps scales it down."""
        return FxRate(self.from_currency, self.to_currency, self.rate.times(ONE.plus(Rational.bps(basis_points))))


class FxConverter:
    @staticmethod
    def convert(amount: Money, rate: FxRate, mode: RoundingMode) -> Money:
        """Exact conversion in minor units, one rounding at the end with an explicit mode."""
        if amount.currency != rate.from_currency:
            raise CurrencyMismatchError(amount.currency, rate.from_currency)
        minor = (Rational.of(amount.minor_units, 1)
                 .times(rate.rate)
                 .times(Rational.pow10(rate.to_currency.exponent - rate.from_currency.exponent)))
        return Money.of_minor(minor.round_to_long(mode), rate.to_currency)

    @staticmethod
    def convert_with_rounded_rate(amount: Money, rate: FxRate, rate_decimals: int, mode: RoundingMode) -> Money:
        """The anti-pattern: round the rate to a few decimals first, then convert."""
        rounded = Rational.parse(rate.rate.to_decimal(rate_decimals))
        return FxConverter.convert(amount, FxRate(rate.from_currency, rate.to_currency, rounded), mode)


# ---------------------------------------------------------------------------------------------
# Time and market rates are injected.
# ---------------------------------------------------------------------------------------------


class Clock(Protocol):
    def now_millis(self) -> int: ...


class FakeClock:
    def __init__(self, start: int) -> None:
        self._now = start

    def now_millis(self) -> int:
        return self._now

    def advance_millis(self, ms: int) -> None:
        self._now += ms


class RateSource(Protocol):
    def mid(self, from_currency: Currency, to_currency: Currency) -> Rational: ...


class FakeRateSource:
    """Mid-market rates set by hand; the inverse pair is derived exactly."""

    def __init__(self) -> None:
        self._rates: dict[str, Rational] = {}

    def set(self, from_currency: Currency, to_currency: Currency, decimal: str) -> None:
        r = Rational.parse(decimal)
        self._rates[from_currency.code + to_currency.code] = r
        self._rates[to_currency.code + from_currency.code] = r.inverse()

    def mid(self, from_currency: Currency, to_currency: Currency) -> Rational:
        if from_currency == to_currency:
            return ONE
        r = self._rates.get(from_currency.code + to_currency.code)
        if r is None:
            raise ValueError(f"no rate {from_currency.code}/{to_currency.code}")
        return r


# ---------------------------------------------------------------------------------------------
# Presentment pricing with a rate lock: the merchant is promised a settlement amount; the shopper
# pays a presentment amount; whoever locked the rate owns the movement until settlement.
# ---------------------------------------------------------------------------------------------


class QuoteExpiredError(RuntimeError):
    def __init__(self, id: str, now: int, expires_at: int) -> None:
        super().__init__(f"quote {id} expired at {expires_at}, now {now}")


@dataclass(frozen=True)
class RateQuote:
    """A locked customer rate settlement -> presentment, valid on [issued_at, expires_at)."""

    id: str
    customer_rate: FxRate
    mid: Rational
    spread_bps: int
    issued_at_millis: int
    expires_at_millis: int

    def valid_at(self, now: int) -> bool:
        return self.issued_at_millis <= now < self.expires_at_millis

    def settlement(self) -> Currency:
        return self.customer_rate.from_currency

    def presentment(self) -> Currency:
        return self.customer_rate.to_currency


@dataclass(frozen=True)
class LockedCharge:
    quote: RateQuote
    settlement_amount: Money
    presentment_amount: Money


@dataclass(frozen=True)
class SettlementResult:
    merchant_receives: Money
    psp_proceeds: Money
    psp_fx_pnl: Money


class FxDesk:
    def __init__(self, clock: Clock, rates: RateSource, spread_bps: int, lock_millis: int) -> None:
        if spread_bps < 0 or lock_millis <= 0:
            raise ValueError("spread >= 0 and lock > 0 required")
        self._clock = clock
        self._rates = rates
        self._spread_bps = spread_bps
        self._lock_millis = lock_millis
        self._quotes: dict[str, RateQuote] = {}
        self._seq = 0

    def quote(self, settlement: Currency, presentment: Currency) -> RateQuote:
        """Lock a rate now. The shopper's rate is mid plus the spread, so the shopper pays more presentment currency."""
        now = self._clock.now_millis()
        mid = self._rates.mid(settlement, presentment)
        customer = FxRate(settlement, presentment, mid).adjusted_by(self._spread_bps)
        self._seq += 1
        q = RateQuote(f"fxq_{self._seq:04d}", customer, mid, self._spread_bps, now, now + self._lock_millis)
        self._quotes[q.id] = q
        return q

    @staticmethod
    def presentment_price(settlement_price: Money, q: RateQuote) -> Money:
        """The price to display. Rounded up so the shopper never pays less than the locked rate implies."""
        return FxConverter.convert(settlement_price, q.customer_rate, RoundingMode.CEILING)

    def confirm(self, quote_id: str, settlement_price: Money) -> LockedCharge:
        """Checkout confirmation: only inside the lock window. After it, the shopper must see a new price."""
        q = self._quotes.get(quote_id)
        if q is None:
            raise ValueError(f"unknown quote {quote_id}")
        if settlement_price.currency != q.settlement():
            raise CurrencyMismatchError(settlement_price.currency, q.settlement())
        now = self._clock.now_millis()
        if not q.valid_at(now):
            raise QuoteExpiredError(q.id, now, q.expires_at_millis)
        return LockedCharge(q, settlement_price, FxDesk.presentment_price(settlement_price, q))

    @staticmethod
    def settle(charge: LockedCharge, market_mid_presentment_to_settlement: Rational) -> SettlementResult:
        """Days later the PSP sells the presentment currency at the then-market mid. The merchant gets the
        promised amount regardless; the difference is the PSP's FX P&L (spread minus market movement)."""
        market = FxRate(charge.quote.presentment(), charge.quote.settlement(), market_mid_presentment_to_settlement)
        proceeds = FxConverter.convert(charge.presentment_amount, market, RoundingMode.HALF_EVEN)
        return SettlementResult(charge.settlement_amount, proceeds, proceeds.minus(charge.settlement_amount))

    @staticmethod
    def break_even_rate(charge: LockedCharge) -> Rational:
        """The settlement-time rate at which the PSP's P&L is exactly zero."""
        return (Rational.of(charge.settlement_amount.minor_units, 1)
                .divided_by(Rational.of(charge.presentment_amount.minor_units, 1))
                .times(Rational.pow10(charge.quote.presentment().exponent - charge.quote.settlement().exponent)))


# ---------------------------------------------------------------------------------------------
# Dynamic currency conversion: the cardholder is offered their card's currency at a disclosed markup.
# ---------------------------------------------------------------------------------------------


@dataclass(frozen=True)
class DccOffer:
    merchant_amount: Money
    card_amount: Money
    dcc_rate: FxRate
    reference_rate: Rational
    markup_bps: int

    def markup_percent(self) -> Rational:
        """Markup over the reference rate, as an exact percentage: (dcc / reference - 1) * 100."""
        return self.dcc_rate.rate.divided_by(self.reference_rate).minus(ONE).times(Rational.of(100, 1))

    def _at_reference(self) -> Money:
        return FxConverter.convert(self.merchant_amount,
                                   FxRate(self.merchant_amount.currency, self.card_amount.currency, self.reference_rate),
                                   RoundingMode.HALF_EVEN)

    def effective_markup_percent(self) -> Rational:
        """What the cardholder actually pays over the reference conversion, after rounding the amount."""
        return Rational.of(self.card_amount.minor_units, self._at_reference().minor_units).minus(ONE).times(Rational.of(100, 1))

    def gross_margin(self) -> Money:
        """DCC gross margin in card currency: cardholder amount minus the reference conversion."""
        return self.card_amount.minus(self._at_reference())

    def disclosure(self) -> str:
        return (f"Pay {self.card_amount} (rate 1 {self.merchant_amount.currency.code} = {self.dcc_rate.rate.to_decimal(6)} "
                f"{self.card_amount.currency.code}, including a {self.markup_percent().to_decimal(2)}% markup over the ECB "
                f"reference rate {self.reference_rate.to_decimal(4)}) or pay {self.merchant_amount} and let your card issuer convert.")


class Dcc:
    @staticmethod
    def offer(merchant_amount: Money, card_currency: Currency, reference_rate: Rational, markup_bps: int) -> DccOffer:
        if merchant_amount.currency == card_currency:
            raise ValueError("DCC needs a different card currency")
        if markup_bps < 0:
            raise ValueError("markup must be >= 0")
        rate = FxRate(merchant_amount.currency, card_currency, reference_rate).adjusted_by(markup_bps)
        card_amount = FxConverter.convert(merchant_amount, rate, RoundingMode.HALF_EVEN)
        return DccOffer(merchant_amount, card_amount, rate, reference_rate, markup_bps)

    @staticmethod
    def issuer_conversion(merchant_amount: Money, card_currency: Currency, network_rate: Rational, issuer_fee_bps: int) -> Money:
        """What declining DCC costs: the network converts at its rate and the issuer may add a foreign transaction fee."""
        converted = FxConverter.convert(merchant_amount, FxRate(merchant_amount.currency, card_currency, network_rate), RoundingMode.HALF_EVEN)
        fee = Money.of_minor(Rational.of(converted.minor_units, 1).times(Rational.bps(issuer_fee_bps)).round_to_long(RoundingMode.HALF_EVEN),
                             card_currency)
        return converted.plus(fee)


# ---------------------------------------------------------------------------------------------
# Demo
# ---------------------------------------------------------------------------------------------

T0 = 1_760_000_000_000
FIFTEEN_MINUTES = 15 * 60 * 1000


def main() -> None:
    clock = FakeClock(T0)
    market = FakeRateSource()
    market.set(EUR, USD, "1.0850")
    desk = FxDesk(clock, market, 200, FIFTEEN_MINUTES)
    price = Money.parse("120.00", USD)

    print("== Presentment vs settlement: a US merchant shows EUR to a German shopper ==")
    q = desk.quote(USD, EUR)
    print(f"mid USD->EUR      = {q.mid} = {q.mid.to_decimal(10)}")
    print(f"customer USD->EUR = {q.customer_rate.rate} = {q.customer_rate.rate.to_decimal(10)} (mid + 2.00%)")
    print("lock window       = [t0, t0 + 15 min)")
    clock.advance_millis(9 * 60 * 1000)
    charge = desk.confirm(q.id, price)
    print(f"confirmed at +9m: shopper pays {charge.presentment_amount}, merchant is promised {charge.settlement_amount}")

    print("\n== Who owns the FX risk: the PSP, from lock to settlement ==")
    for eur_usd in ("1.0850", "1.0700", "1.0600", "1.1000"):
        r = FxDesk.settle(charge, Rational.parse(eur_usd))
        print(f"EUR/USD at settlement {eur_usd}: PSP sells {charge.presentment_amount} for {r.psp_proceeds}, "
              f"merchant gets {r.merchant_receives}, PSP FX P&L {r.psp_fx_pnl}")
    print(f"break-even EUR/USD = {FxDesk.break_even_rate(charge).to_decimal(6)}")

    print("\n== The lock expires: a late confirmation must re-quote ==")
    clock.advance_millis(6 * 60 * 1000)
    try:
        desk.confirm(q.id, price)
    except QuoteExpiredError as e:
        print(f"at +15m: {e}")
    market.set(EUR, USD, "1.0600")
    fresh = desk.quote(USD, EUR)
    print(f"re-quoted at EUR/USD 1.0600: shopper now pays {FxDesk.presentment_price(price, fresh)}")

    print("\n== Round the amount once, never the rate ==")
    big = Money.parse("50000.00", USD)
    print(f"exact rate:          {FxConverter.convert(big, q.customer_rate, RoundingMode.HALF_EVEN)}")
    print(f"rate rounded to 4dp: {FxConverter.convert_with_rounded_rate(big, q.customer_rate, 4, RoundingMode.HALF_EVEN)}"
          f"  (rate {q.customer_rate.rate.to_decimal(4)})")
    print(f"USD 120.00 -> JPY at 152.37: {FxConverter.convert(price, FxRate(USD, JPY, Rational.parse('152.37')), RoundingMode.HALF_EVEN)}")

    print("\n== DCC: a UK card at a German merchant, EUR 100.00 ==")
    eur100 = Money.parse("100.00", EUR)
    offer = Dcc.offer(eur100, GBP, Rational.parse("0.8550"), 350)
    print(offer.disclosure())
    print(f"disclosed markup  = {offer.markup_percent().to_decimal(4)}% (exact: {offer.markup_percent()})")
    print(f"effective markup  = {offer.effective_markup_percent().to_decimal(4)}% after rounding the amount")
    split = offer.gross_margin().allocate(3000, 7000)
    print(f"DCC gross margin  = {offer.gross_margin()}: merchant rebate {split[0]}, DCC provider {split[1]}")
    print(f"decline DCC, network 0.8560, no issuer fee:   {Dcc.issuer_conversion(eur100, GBP, Rational.parse('0.8560'), 0)}")
    print(f"decline DCC, network 0.8560, 2.99% issuer fee: {Dcc.issuer_conversion(eur100, GBP, Rational.parse('0.8560'), 299)}")


if __name__ == "__main__":
    main()
