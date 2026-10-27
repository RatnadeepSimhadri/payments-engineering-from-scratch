"""Lesson: phases/10-money-and-ledgers/02-currency-and-fx/docs/en.md
Currencies, Exchange Rates, and FX Conversion
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: ISO 4217:2015 (alphabetic code, numeric code, minor-unit exponent); ECB reference-rate conventions
(base currency quoting). Money type copied from Phase 10, Lesson 01. Python 3.11+, standard library only.
"""

from __future__ import annotations

from dataclasses import dataclass
from enum import Enum
from functools import total_ordering
from typing import Dict, List, Optional

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
    """Integer division with an explicit rounding rule. Python's // floors toward -inf, so we work on
    magnitudes and reapply the sign to get the truncation semantics Java's / has."""
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
# ISO 4217 registry.
# ---------------------------------------------------------------------------------------------


class UnknownCurrencyError(ValueError):
    def __init__(self, code: str) -> None:
        super().__init__(f"unknown currency code: {code}")


@dataclass(frozen=True)
class CurrencyInfo:
    """One row of ISO 4217: alphabetic code, numeric code, minor-unit exponent, and a display name."""

    code: str
    numeric_code: int
    exponent: int
    name: str

    def currency(self) -> Currency:
        return Currency(self.code, self.exponent)


class CurrencyRegistry:
    """The list of currencies this system is allowed to handle. The exponent lives here, in data, so a
    currency that redenominates (ISO 4217 amendments do this) is a data change, not a code change."""

    def __init__(self) -> None:
        self._by_code: Dict[str, CurrencyInfo] = {}
        self._by_numeric: Dict[int, CurrencyInfo] = {}

    @staticmethod
    def iso4217() -> "CurrencyRegistry":
        """A registry pre-loaded with a representative slice of ISO 4217, including the 0- and 3-decimal cases."""
        r = CurrencyRegistry()
        r.register("USD", 840, 2, "US Dollar")
        r.register("EUR", 978, 2, "Euro")
        r.register("GBP", 826, 2, "Pound Sterling")
        r.register("INR", 356, 2, "Indian Rupee")
        r.register("CHF", 756, 2, "Swiss Franc")
        r.register("CAD", 124, 2, "Canadian Dollar")
        r.register("AUD", 36, 2, "Australian Dollar")
        r.register("BRL", 986, 2, "Brazilian Real")
        r.register("JPY", 392, 0, "Yen")
        r.register("KRW", 410, 0, "Won")
        r.register("VND", 704, 0, "Dong")
        r.register("KWD", 414, 3, "Kuwaiti Dinar")
        r.register("BHD", 48, 3, "Bahraini Dinar")
        r.register("JOD", 400, 3, "Jordanian Dinar")
        r.register("CLF", 990, 4, "Unidad de Fomento")
        return r

    def register(self, code: str, numeric_code: int, exponent: int, name: str) -> CurrencyInfo:
        info = CurrencyInfo(code, numeric_code, exponent, name)
        info.currency()  # validates code and exponent
        if code in self._by_code:
            raise ValueError(f"already registered: {code}")
        if numeric_code in self._by_numeric:
            raise ValueError(f"numeric code already registered: {numeric_code}")
        self._by_code[code] = info
        self._by_numeric[numeric_code] = info
        return info

    def currency(self, code: str) -> Currency:
        return self.info(code).currency()

    def info(self, code: str) -> CurrencyInfo:
        info = self._by_code.get(code)
        if info is None:
            raise UnknownCurrencyError(code)
        return info

    def by_numeric(self, numeric_code: int) -> Currency:
        info = self._by_numeric.get(numeric_code)
        if info is None:
            raise UnknownCurrencyError(str(numeric_code))
        return info.currency()

    def contains(self, code: str) -> bool:
        return code in self._by_code

    def parse(self, amount: str, code: str) -> Money:
        """Parse a decimal string in a currency known to the registry. Unknown code: rejected before the amount is read."""
        return Money.parse(amount, self.currency(code))

    def size(self) -> int:
        return len(self._by_code)


# ---------------------------------------------------------------------------------------------
# Exchange rates as exact rationals.
# ---------------------------------------------------------------------------------------------


class StaleRateError(RuntimeError):
    pass


def gcd(a: int, b: int) -> int:
    a, b = abs(a), abs(b)
    while b != 0:
        a, b = b, a % b
    return 1 if a == 0 else a


@dataclass(frozen=True)
class FxRate:
    """One unit of base buys numerator/denominator units of quote: an exact rational, so "0.9217" is 9217/10000
    and never a float. Carries provenance (source, observation time) and the spread in basis points the
    provider charges around this mid rate. The pair is reduced by gcd on construction, as in Java."""

    base: Currency
    quote: Currency
    numerator: int
    denominator: int
    source: str
    observed_at_epoch_seconds: int
    spread_bps: int

    def __post_init__(self) -> None:
        if self.base == self.quote:
            raise ValueError(f"base and quote must differ: {self.base.code}")
        if self.numerator <= 0 or self.denominator <= 0:
            raise ValueError("rate must be positive")
        if self.spread_bps < 0 or self.spread_bps >= 10_000:
            raise ValueError("spread must be 0..9999 bps")
        g = gcd(self.numerator, self.denominator)
        object.__setattr__(self, "numerator", self.numerator // g)
        object.__setattr__(self, "denominator", self.denominator // g)

    @staticmethod
    def of(base: Currency, quote: Currency, decimal_rate: str, source: str,
           observed_at_epoch_seconds: int, spread_bps: int) -> "FxRate":
        """Build a mid rate from a decimal string such as "0.9217" or "149.37" without going through a float."""
        s = decimal_rate.strip()
        whole, _, frac = s.partition(".")
        whole = whole or "0"
        if not whole.isdigit() or (frac and not frac.isdigit()) or not (whole + frac):
            raise ValueError(f"not a decimal rate: {decimal_rate}")
        den = 10 ** len(frac)
        num = int(whole + frac)
        return FxRate(base, quote, num, den, source, observed_at_epoch_seconds, spread_bps)

    def invert(self) -> "FxRate":
        """The same rate quoted the other way round: quote per base becomes base per quote. Exact, no rounding."""
        return FxRate(self.quote, self.base, self.denominator, self.numerator, self.source,
                      self.observed_at_epoch_seconds, self.spread_bps)

    def bid(self) -> "FxRate":
        """The rate the customer gets when they sell base and buy quote: mid less half the spread."""
        return self._with_spread_applied(-self.spread_bps)

    def ask(self) -> "FxRate":
        """The rate the customer pays when they buy base with quote: mid plus half the spread."""
        return self._with_spread_applied(+self.spread_bps)

    def _with_spread_applied(self, signed_bps: int) -> "FxRate":
        # mid * (1 +- spread/2) = num/den * (20000 +- bps)/20000, all integer
        num = self.numerator * (20_000 + signed_bps)
        den = self.denominator * 20_000
        return FxRate(self.base, self.quote, num, den, self.source, self.observed_at_epoch_seconds, 0)

    def cross(self, other: "FxRate") -> "FxRate":
        """Cross rate through a shared base. If this is USD/EUR (1 USD = x EUR) and other is USD/GBP (1 USD = y GBP),
        the result is EUR/GBP: 1 EUR = y/x GBP. Exact rational division; no intermediate rounding."""
        if self.base != other.base:
            raise ValueError(f"cross rate needs a shared base: {self.base.code} vs {other.base.code}")
        if self.quote == other.quote:
            raise ValueError("cross rate between identical quotes")
        num = other.numerator * self.denominator
        den = other.denominator * self.numerator
        return FxRate(self.quote, other.quote, num, den, self.source + "+" + other.source,
                      min(self.observed_at_epoch_seconds, other.observed_at_epoch_seconds),
                      max(self.spread_bps, other.spread_bps))

    def require_fresh(self, now_epoch_seconds: int, max_age_seconds: int) -> "FxRate":
        """Reject a rate older than max_age_seconds at the given clock reading. Rates have a shelf life."""
        age = now_epoch_seconds - self.observed_at_epoch_seconds
        if age > max_age_seconds:
            raise StaleRateError(f"{self.pair()} rate from {self.source} is {age}s old; max {max_age_seconds}s")
        return self

    def pair(self) -> str:
        return f"{self.base.code}/{self.quote.code}"

    def to_decimal(self, places: int) -> str:
        """Decimal rendering with the requested number of places, HALF_EVEN. For display and logs only."""
        scale = 10 ** places
        scaled = divide_rounded(self.numerator * scale, self.denominator, Rounding.HALF_EVEN)
        whole, frac = divmod(scaled, scale)
        if places == 0:
            return str(whole)
        return f"{whole}.{str(frac).rjust(places, '0')}"

    def __str__(self) -> str:
        return f"{self.pair()} {self.to_decimal(6)} ({self.numerator}/{self.denominator}) from {self.source}"


# ---------------------------------------------------------------------------------------------
# Conversion: the one place a fraction is created, and the record of what was done with it.
# ---------------------------------------------------------------------------------------------


@dataclass(frozen=True)
class Conversion:
    """The result of one conversion: the amount that went in, the amount that came out, the rate and rounding used,
    and the exact fraction of a quote minor unit that the rounding discarded (remainder_numerator/remainder_denominator,
    in the open interval (-1, 1))."""

    from_: Money
    to: Money
    rate: FxRate
    rounding: Rounding
    remainder_numerator: int
    remainder_denominator: int

    def rounded(self) -> bool:
        """True when the rounding moved the result away from the exact value in the quote currency."""
        return self.remainder_numerator != 0


@dataclass(frozen=True)
class RoundTrip:
    out: Conversion
    back: Conversion
    residue: Money


class FxConverter:
    @staticmethod
    def convert(amount: Money, rate: FxRate, rounding: Rounding) -> Conversion:
        """amount (base minor units) * rate * 10^quote_exp / 10^base_exp, as one exact rational, rounded once.
        The remainder of that single division is reported, not discarded."""
        if amount.currency != rate.base:
            raise CurrencyMismatchError(amount.currency, rate.base)
        num = amount.minor_units * rate.numerator * rate.quote.minor_units_per_major()
        den = rate.denominator * rate.base.minor_units_per_major()
        rounded = divide_rounded(num, den, rounding)
        # exact - rounded, as a fraction of one quote minor unit: (num - rounded*den) / den
        remainder_num = num - rounded * den
        g = gcd(remainder_num, den)
        # g divides remainder_num exactly, so floor division equals Java's truncating division here
        return Conversion(amount, Money.of_minor(rounded, rate.quote), rate, rounding, remainder_num // g, den // g)

    @staticmethod
    def round_trip(amount: Money, rate: FxRate, rounding: Rounding) -> RoundTrip:
        """Convert out and back with the inverse rate. The residue is what the customer does not get back:
        positive means the round trip lost money, negative means it manufactured money. Never silently zero."""
        out = FxConverter.convert(amount, rate, rounding)
        back = FxConverter.convert(out.to, rate.invert(), rounding)
        residue = amount.minus(back.to)
        return RoundTrip(out, back, residue)


class RateTable:
    """A rate table keyed by pair, with a base currency through which any cross rate can be derived.
    Direct rates win; otherwise base/X and base/Y are combined. Every lookup is checked for staleness."""

    def __init__(self, base_currency: Currency, max_age_seconds: int) -> None:
        self._base_currency = base_currency
        self._rates: Dict[str, FxRate] = {}
        self._max_age_seconds = max_age_seconds

    def put(self, rate: FxRate) -> None:
        if rate.base != self._base_currency:
            raise ValueError(f"table quotes against {self._base_currency.code}; got {rate.pair()}")
        self._rates[rate.quote.code] = rate

    def rate(self, from_: Currency, to: Currency, now_epoch_seconds: int) -> FxRate:
        if from_ == to:
            raise ValueError(f"no rate needed for {from_.code}")
        if from_ == self._base_currency:
            return self._direct(to).require_fresh(now_epoch_seconds, self._max_age_seconds)
        if to == self._base_currency:
            return self._direct(from_).require_fresh(now_epoch_seconds, self._max_age_seconds).invert()
        a = self._direct(from_).require_fresh(now_epoch_seconds, self._max_age_seconds)
        b = self._direct(to).require_fresh(now_epoch_seconds, self._max_age_seconds)
        return a.cross(b)

    def _direct(self, quote: Currency) -> FxRate:
        r: Optional[FxRate] = self._rates.get(quote.code)
        if r is None:
            raise ValueError(f"no {self._base_currency.code}/{quote.code} rate loaded")
        return r

    def all(self) -> List[FxRate]:
        return list(self._rates.values())


def main() -> None:
    now = 1_700_000_000  # injected clock: 2023-11-14T22:13:20Z
    registry = CurrencyRegistry.iso4217()
    usd, eur, gbp = registry.currency("USD"), registry.currency("EUR"), registry.currency("GBP")
    jpy, kwd = registry.currency("JPY"), registry.currency("KWD")

    print("== ISO 4217 registry: the exponent is data ==")
    for code in ("USD", "JPY", "KWD", "CLF"):
        info = registry.info(code)
        print(f"{code} numeric={info.numeric_code:03d} exponent={info.exponent}  "
              f"1 major = {info.currency().minor_units_per_major()} minor  ({info.name})")
    print(f"numeric 978 -> {registry.by_numeric(978).code}")
    try:
        registry.currency("USX")
    except UnknownCurrencyError as e:
        print(f"rejected: {e}")

    print("\n== A rate is an exact rational with provenance ==")
    usd_eur = FxRate.of(usd, eur, "0.9217", "ecb-ref", now - 60, 50)
    usd_jpy = FxRate.of(usd, jpy, "149.37", "ecb-ref", now - 60, 50)
    usd_gbp = FxRate.of(usd, gbp, "0.7893", "ecb-ref", now - 60, 50)
    usd_kwd = FxRate.of(usd, kwd, "0.30825", "ecb-ref", now - 60, 50)
    print(usd_eur)
    print(f"inverted: {usd_eur.invert()}   (exact: 10000/9217, not 1.0849...)")
    print(f"bid {usd_eur.bid().to_decimal(6)}  mid {usd_eur.to_decimal(6)}  ask {usd_eur.ask().to_decimal(6)}  (50 bps spread)")

    print("\n== Conversion rounds exactly once, and reports what it dropped ==")
    c1 = FxConverter.convert(Money.parse("100.00", usd), usd_eur, Rounding.HALF_EVEN)
    print(f"{c1.from_} -> {c1.to}  dropped {c1.remainder_numerator}/{c1.remainder_denominator} of a cent")
    c2 = FxConverter.convert(Money.parse("19.99", usd), usd_eur, Rounding.HALF_EVEN)
    print(f"{c2.from_} -> {c2.to}  exact 18.424783, dropped {c2.remainder_numerator}/{c2.remainder_denominator} of a cent")
    c3 = FxConverter.convert(Money.parse("19.99", usd), usd_jpy, Rounding.HALF_EVEN)
    print(f"{c3.from_} -> {c3.to}  (JPY has no fraction: exact 2985.9063 yen)")
    c4 = FxConverter.convert(Money.parse("19.99", usd), usd_kwd, Rounding.HALF_EVEN)
    print(f"{c4.from_} -> {c4.to}  (KWD has three decimals)")
    c5 = FxConverter.convert(Money.parse("0.01", usd), usd_eur, Rounding.DOWN)
    print(f"{c5.from_} -> {c5.to}  with DOWN: one US cent is less than one euro cent")

    print("\n== Round trip: the residue is real money and needs a home ==")
    rt = FxConverter.round_trip(Money.parse("19.99", usd), usd_eur, Rounding.HALF_EVEN)
    print(f"{rt.out.from_} -> {rt.out.to} -> {rt.back.to}   residue {rt.residue}")
    rt2 = FxConverter.round_trip(Money.parse("0.06", usd), usd_eur, Rounding.HALF_EVEN)
    print(f"{rt2.out.from_} -> {rt2.out.to} -> {rt2.back.to}   residue {rt2.residue}")
    print("Positive residue: the platform gained a cent (FX rounding gain). Negative: it manufactured one (FX rounding loss).")
    print("Either way the residue is posted to an FX rounding account, never swallowed.")

    print("\n== Cross rates through a base currency ==")
    table = RateTable(usd, 300)
    table.put(usd_eur)
    table.put(usd_gbp)
    table.put(usd_jpy)
    eur_gbp = table.rate(eur, gbp, now)
    print(f"EUR/GBP via USD = {eur_gbp}")
    c6 = FxConverter.convert(Money.parse("50.00", eur), eur_gbp, Rounding.HALF_EVEN)
    print(f"{c6.from_} -> {c6.to}  (50 * 7893/9217 = 42.8176...)")
    eur_usd = table.rate(eur, usd, now)
    print(f"EUR/USD (inverted) = {eur_usd.to_decimal(6)}")

    print("\n== Rates go stale ==")
    try:
        table.rate(usd, eur, now + 3600)
    except StaleRateError as e:
        print(f"rejected: {e}")
    try:
        FxConverter.convert(Money.parse("1.00", eur), usd_eur, Rounding.HALF_EVEN)
    except CurrencyMismatchError as e:
        print(f"rejected: {e}")


if __name__ == "__main__":
    main()
