"""Project: projects/ledger-engine — stage 1 starter (Python): the Money value type and the chart of accounts.
Docs: projects/ledger-engine/stages/01-money-and-accounts/docs/en.md
Run:  python3 main.py
Grade: python3 scripts/project_test.py ledger-engine --lang python --stage 1 --path <workspace>
Same design as phases/10-money-and-ledgers/01-representing-money. Currency, Rounding, CurrencyMismatchError,
and the Money dataclass are given; parse, plus, minus, times, divide_rounded, and allocate are yours to implement.
Every function marked TODO raises NotImplementedError until you replace it. Keep the names: the graders of every
stage use them. Later stages add Posting, JournalEntry, Ledger, ... to this same file.
Python 3.11+, standard library only.
"""

from __future__ import annotations

from dataclasses import dataclass
from enum import Enum
from functools import total_ordering

# ---------------------------------------------------------------- Money (given parts)

LONG_MIN = -(2**63)
LONG_MAX = 2**63 - 1


def _check_long(value: int) -> int:
    """Python ints never overflow; the Java/Go versions use 64-bit longs with Math.addExact. Keep the same
    bound so a huge amount is a loud error here too (OverflowError is an ArithmeticError)."""
    if not LONG_MIN <= value <= LONG_MAX:
        raise OverflowError(f"amount overflows 64-bit minor units: {value}")
    return value


@dataclass(frozen=True)
class Currency:
    """ISO 4217 currency: a three-letter code and the number of decimals in its minor unit."""

    code: str
    exponent: int

    def __post_init__(self) -> None:
        if not isinstance(self.code, str) or len(self.code) != 3 or not self.code.isalpha() or not self.code.isupper():
            raise ValueError(f"currency code must be three uppercase letters: {self.code!r}")
        if not 0 <= self.exponent <= 4:
            raise ValueError(f"exponent must be 0..4: {self.exponent}")

    def minor_units_per_major(self) -> int:
        """10**exponent: 100 for USD, 1 for JPY, 1000 for KWD."""
        return 10**self.exponent


USD = Currency("USD", 2)
EUR = Currency("EUR", 2)
GBP = Currency("GBP", 2)
INR = Currency("INR", 2)
JPY = Currency("JPY", 0)
KWD = Currency("KWD", 3)


class Rounding(Enum):
    """How to resolve a fractional minor unit."""

    HALF_EVEN = "half_even"  # banker's rounding: ties go to the even neighbour; unbiased
    HALF_UP = "half_up"  # ties go away from zero; what most people mean by "round"
    DOWN = "down"  # truncate toward zero
    UP = "up"  # away from zero


class CurrencyMismatchError(ValueError):
    def __init__(self, a: Currency, b: Currency) -> None:
        super().__init__(f"cannot combine {a.code} with {b.code}")


def divide_rounded(dividend: int, divisor: int, rounding: Rounding) -> int:
    """Integer division with an explicit rounding rule. Python's // floors toward -inf; Java's / truncates
    toward zero. Work on magnitudes and reapply the sign. HALF_EVEN on an exact tie goes to the even neighbour:
    7/2 -> 4, 5/2 -> 2, -7/2 HALF_UP -> -4."""
    raise NotImplementedError("TODO stage 01: divide_rounded")


@total_ordering
@dataclass(frozen=True)
class Money:
    """An exact amount of one currency, stored as a signed count of minor units. Immutable."""

    minor_units: int
    currency: Currency

    def __post_init__(self) -> None:
        if not isinstance(self.minor_units, int) or isinstance(self.minor_units, bool):
            raise TypeError("minor_units must be an int; never construct Money from a float")
        if not isinstance(self.currency, Currency):
            raise TypeError("currency must be a Currency")
        _check_long(self.minor_units)

    @staticmethod
    def of_minor(minor_units: int, currency: Currency) -> Money:
        return Money(minor_units, currency)

    @staticmethod
    def parse(text: str, currency: Currency) -> Money:
        """Parse "12.34", "-0.5", ".99", "1200" exactly. More decimals than the currency allows -> ArithmeticError.
        Not a decimal ("12,34", "abc", "") -> ValueError."""
        raise NotImplementedError("TODO stage 01: Money.parse")

    def _require_same_currency(self, other: Money) -> None:
        if self.currency != other.currency:
            raise CurrencyMismatchError(self.currency, other.currency)

    def plus(self, other: Money) -> Money:
        """Same-currency addition. Money's constructor keeps the 64-bit bound (OverflowError)."""
        raise NotImplementedError("TODO stage 01: Money.plus")

    def minus(self, other: Money) -> Money:
        """Same-currency subtraction."""
        raise NotImplementedError("TODO stage 01: Money.minus")

    def negate(self) -> Money:
        return Money(-self.minor_units, self.currency)

    def abs(self) -> Money:
        return self.negate() if self.minor_units < 0 else self

    def is_zero(self) -> bool:
        return self.minor_units == 0

    def is_negative(self) -> bool:
        return self.minor_units < 0

    def is_positive(self) -> bool:
        return self.minor_units > 0

    def times(self, numerator: int, denominator: int, rounding: Rounding) -> Money:
        """Multiply by an exact rational numerator/denominator (a fee of 2.9% is 29/1000) and resolve the
        remainder with the given rounding. A denominator <= 0 is an ArithmeticError."""
        raise NotImplementedError("TODO stage 01: Money.times")

    @staticmethod
    def divide_rounded(dividend: int, divisor: int, rounding: Rounding) -> int:
        """Integer division with an explicit rounding rule. Public so the ledger can reuse it."""
        raise NotImplementedError("TODO stage 01: Money.divide_rounded")

    def allocate(self, *ratios: int) -> list[Money]:
        """Split this amount in the given ratios so the parts sum exactly to the whole (largest-remainder
        method, ties broken by index order). No ratios, a negative ratio, or all-zero ratios -> ValueError."""
        raise NotImplementedError("TODO stage 01: Money.allocate")

    @staticmethod
    def sum(currency: Currency, *amounts: Money) -> Money:
        acc = Money(0, currency)
        for m in amounts:
            acc = acc.plus(m)
        return acc

    def __lt__(self, other: Money) -> bool:
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


# ---------------------------------------------------------------- accounts


class Side(Enum):
    """Which column a posting lands in. Debit and credit are directions, not "minus" and "plus"."""

    DEBIT = "debit"
    CREDIT = "credit"

    def opposite(self) -> Side:
        """DEBIT <-> CREDIT."""
        raise NotImplementedError("TODO stage 01: Side.opposite")


class AccountType(Enum):
    """The five account classes of double-entry bookkeeping. The class decides which side increases the account."""

    ASSET = "asset"
    LIABILITY = "liability"
    EQUITY = "equity"
    REVENUE = "revenue"
    EXPENSE = "expense"

    def normal_side(self) -> Side:
        """Assets and expenses grow on the debit side; liabilities, equity, and revenue grow on the credit side."""
        raise NotImplementedError("TODO stage 01: AccountType.normal_side")


class LedgerError(ValueError):
    """Base type for every rule the ledger enforces, so callers can catch "the ledger said no" in one place."""


class DuplicateAccountError(LedgerError):
    def __init__(self, account_id: str) -> None:
        super().__init__(f"account already exists: {account_id}")


class UnknownAccountError(LedgerError):
    def __init__(self, account_id: str) -> None:
        super().__init__(f"unknown account: {account_id}")


@dataclass(frozen=True)
class Account:
    """An account's identity never changes: id, class, and currency are fixed when it is opened."""

    id: str
    type: AccountType
    currency: Currency

    def __post_init__(self) -> None:
        # TODO stage 01: reject a None or blank id (ValueError), a type that is not an AccountType and a
        # currency that is not a Currency (TypeError).
        raise NotImplementedError("TODO stage 01: Account guards")

    def normal_side(self) -> Side:
        """The side on which this account's balance grows."""
        raise NotImplementedError("TODO stage 01: Account.normal_side")


class AccountRegistry:
    """The chart of accounts: every account the ledger knows, in opening order. Ids are unique. Not thread-safe."""

    def __init__(self) -> None:
        self._accounts: dict[str, Account] = {}  # dicts keep insertion order

    def open(self, account_id: str, type: AccountType, currency: Currency) -> Account:
        """Open an account. The same id can never be opened twice, even with identical attributes -> DuplicateAccountError."""
        raise NotImplementedError("TODO stage 01: AccountRegistry.open")

    def get(self, account_id: str) -> Account:
        """Look an account up; an unknown id is an UnknownAccountError, never None."""
        raise NotImplementedError("TODO stage 01: AccountRegistry.get")

    def contains(self, account_id: str) -> bool:
        raise NotImplementedError("TODO stage 01: AccountRegistry.contains")

    def size(self) -> int:
        raise NotImplementedError("TODO stage 01: AccountRegistry.size")

    def all(self) -> tuple[Account, ...]:
        """Accounts in opening order; an immutable copy (a tuple), so the chart cannot be edited from outside."""
        raise NotImplementedError("TODO stage 01: AccountRegistry.all")


# ---------------------------------------------------------------- workspace entry point (the grader never runs it)


def main() -> None:
    print("== Ledger Engine workspace ==")
    print(f"given: {Money.of_minor(1234, USD)}, {Money.of_minor(1250, KWD)}")
    try:
        fee = Money.parse("100.00", USD).times(29, 1000, Rounding.HALF_EVEN)
        chart = AccountRegistry()
        bank = chart.open("bank", AccountType.ASSET, USD)
        print(f"fee {fee}; {bank.id} grows on {bank.normal_side().name}")
    except NotImplementedError as todo:
        print(f"stage 1 not implemented yet: {todo}")


if __name__ == "__main__":
    main()
