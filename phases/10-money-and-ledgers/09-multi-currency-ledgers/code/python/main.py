"""Lesson: phases/10-money-and-ledgers/09-multi-currency-ledgers/docs/en.md
Multi-Currency Ledgers and Revaluation
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: ISO 4217 (codes, exponents); IAS 21 / ASC 830 (foreign-currency monetary items, exchange differences);
Money from Lesson 01, FX rate shape from Lesson 02. Python 3.11+, standard library only.
"""

from __future__ import annotations

from dataclasses import dataclass
from enum import Enum
from functools import total_ordering
from typing import Dict, Iterable, List, Optional, Tuple

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
    """Integer division with an explicit rounding rule, on magnitudes so negatives truncate like Java.
    Positions are often negative (a short EUR position), which is exactly where // would differ."""
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
# Per-currency sub-ledgers. An entry has exactly one currency. Cross-currency business events are
# transactions made of one entry per currency, joined through FX clearing accounts.
# ---------------------------------------------------------------------------------------------


class Side(Enum):
    DEBIT = "debit"
    CREDIT = "credit"


class AccountType(Enum):
    ASSET = Side.DEBIT
    EXPENSE = Side.DEBIT
    LIABILITY = Side.CREDIT
    EQUITY = Side.CREDIT
    REVENUE = Side.CREDIT

    def normal_balance(self) -> Side:
        return self.value


class LedgerError(ValueError):
    pass


class MixedCurrencyError(LedgerError):
    def __init__(self, id: str, entry_currency: Currency, found: Currency) -> None:
        super().__init__(f"entry {id} is {entry_currency.code} but has a {found.code} posting; use one entry per currency")


@dataclass(frozen=True)
class Account:
    """An account holds exactly one currency for its whole life. clearing marks FX clearing accounts."""

    id: str
    name: str
    type: AccountType
    currency: Currency
    clearing: bool


@dataclass(frozen=True)
class Posting:
    account_id: str
    side: Side
    amount: Money

    def __post_init__(self) -> None:
        if not self.amount.is_positive():
            raise LedgerError(f"posting amount must be positive, got {self.amount}")

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
    """A single-currency balanced entry. Mixing currencies inside one entry is rejected, not netted."""

    id: str
    currency: Currency
    postings: Tuple[Posting, ...]

    def __init__(self, id: str, currency: Currency, postings: Iterable[Posting]) -> None:
        postings = tuple(postings)
        if len(postings) < 2:
            raise LedgerError(f"entry {id} needs at least two postings")
        net = 0
        for p in postings:
            if p.amount.currency != currency:
                raise MixedCurrencyError(id, currency, p.amount.currency)
            net += p.signed_minor()
        if net != 0:
            raise LedgerError(f"entry {id} unbalanced by {Money.of_minor(net, currency)}")
        object.__setattr__(self, "id", id)
        object.__setattr__(self, "currency", currency)
        object.__setattr__(self, "postings", postings)

    @staticmethod
    def of(id: str, *postings: Posting) -> "JournalEntry":
        return JournalEntry(id, postings[0].amount.currency, postings)


@dataclass(frozen=True)
class FxRate:
    """An exchange rate as an exact rational: 1 unit of base = numerator/denominator units of quote.
    Source and timestamp travel with it so every conversion can be audited (Lesson 02)."""

    base: Currency
    quote: Currency
    numerator: int
    denominator: int
    source: str
    as_of: int

    def __post_init__(self) -> None:
        if self.base == self.quote:
            raise ValueError("rate needs two different currencies")
        if self.numerator <= 0 or self.denominator <= 0:
            raise ValueError("rate must be positive")

    def convert(self, amount: Money, rounding: Rounding) -> Money:
        """Convert an amount of base or quote into the other currency, in minor units, rounding once."""
        from_ = amount.currency
        if from_ == self.base:
            to, num, den = self.quote, self.numerator, self.denominator
        elif from_ == self.quote:
            to, num, den = self.base, self.denominator, self.numerator
        else:
            raise CurrencyMismatchError(amount.currency, self.base)
        # minor(to) = minor(from) * rate * 10^(exp(to) - exp(from))
        shift = to.exponent - from_.exponent
        if shift > 0:
            num *= 10 ** shift
        else:
            den *= 10 ** (-shift)
        return Money.of_minor(divide_rounded(amount.minor_units * num, den, rounding), to)

    def __str__(self) -> str:
        return f"1 {self.base.code} = {self.numerator}/{self.denominator} {self.quote.code} ({self.source})"


@dataclass(frozen=True)
class Transaction:
    """A business event: one entry per currency, posted all-or-nothing. rate is None for single-currency events."""

    id: str
    description: str
    entries: Tuple[JournalEntry, ...]
    rate: Optional[FxRate]

    def __post_init__(self) -> None:
        object.__setattr__(self, "entries", tuple(self.entries))


class SubLedger:
    """One currency's journal and balances. Knows nothing about other currencies."""

    def __init__(self, currency: Currency) -> None:
        self.currency = currency
        self._journal: List[JournalEntry] = []
        self._net_debits: Dict[str, int] = {}

    def open(self, account_id: str) -> None:
        self._net_debits[account_id] = 0

    def append(self, e: JournalEntry) -> None:
        self._journal.append(e)
        for p in e.postings:
            self._net_debits[p.account_id] = self._net_debits.get(p.account_id, 0) + p.signed_minor()

    def net_debit(self, account_id: str) -> int:
        return self._net_debits.get(account_id, 0)

    def trial_balance_net(self) -> int:
        """Sum of net debits over every account in this currency. Zero, always, or the sub-ledger is corrupt."""
        return sum(self._net_debits.values())

    def size(self) -> int:
        return len(self._journal)

    def net_debits(self) -> Dict[str, int]:
        return dict(self._net_debits)


@dataclass(frozen=True)
class Position:
    """Per foreign currency: the net position outside clearing, what it cost in functional currency, and revaluations booked."""

    currency: Currency
    position: Money
    basis: Money
    revalued: Money

    def carrying_value(self) -> Money:
        return self.basis.plus(self.revalued)


class MultiCurrencyLedger:
    """A ledger with one SubLedger per currency, a functional (reporting) currency, and per-currency FX clearing accounts.
    exchange() posts a two-entry transaction through clearing; revalue() books unrealized gain/loss in functional
    currency only; realize() books the realized result when a foreign position is flat."""

    UNREALIZED = "4900"
    REALIZED = "4910"

    def __init__(self, functional: Currency) -> None:
        self.functional = functional
        self._sub_ledgers: Dict[Currency, SubLedger] = {}
        self._accounts: Dict[str, Account] = {}
        self._transactions: Dict[str, Transaction] = {}
        self._basis: Dict[Currency, int] = {}      # functional minor units, signed like the position
        self._revalued: Dict[Currency, int] = {}   # cumulative unrealized gain (+) / loss (-)
        self._adjustment_counter = 0
        self.add_currency(functional)
        self.open_account(self.UNREALIZED, "Unrealized FX Gain/Loss", AccountType.REVENUE, functional)
        self.open_account(self.REALIZED, "Realized FX Gain/Loss", AccountType.REVENUE, functional)

    @staticmethod
    def clearing_id(c: Currency) -> str:
        return f"1900-{c.code}"

    @staticmethod
    def revaluation_id(c: Currency) -> str:
        return f"1950-{c.code}"

    def add_currency(self, c: Currency) -> None:
        """Register a currency: its sub-ledger, its clearing account, and (if foreign) its revaluation adjustment account."""
        if c in self._sub_ledgers:
            return
        self._sub_ledgers[c] = SubLedger(c)
        self._open_account_internal(Account(self.clearing_id(c), f"FX Clearing {c.code}", AccountType.ASSET, c, True))
        if c != self.functional:
            self._open_account_internal(Account(self.revaluation_id(c), f"FX Revaluation Adjustment {c.code}",
                                                AccountType.ASSET, self.functional, False))
            self._basis[c] = 0
            self._revalued[c] = 0

    def open_account(self, id: str, name: str, type: AccountType, currency: Currency) -> Account:
        self.add_currency(currency)
        return self._open_account_internal(Account(id, name, type, currency, False))

    def _open_account_internal(self, a: Account) -> Account:
        if a.id in self._accounts:
            raise LedgerError(f"account exists: {a.id}")
        self._accounts[a.id] = a
        self._sub_ledgers[a.currency].open(a.id)
        return a

    def account(self, id: str) -> Account:
        a = self._accounts.get(id)
        if a is None:
            raise LedgerError(f"unknown account: {id}")
        return a

    def sub_ledger(self, c: Currency) -> SubLedger:
        s = self._sub_ledgers.get(c)
        if s is None:
            raise LedgerError(f"no sub-ledger for {c.code}")
        return s

    def balance(self, account_id: str) -> Money:
        a = self.account(account_id)
        net = self.sub_ledger(a.currency).net_debit(account_id)
        return Money.of_minor(net if a.type.normal_balance() is Side.DEBIT else -net, a.currency)

    def net_debit(self, account_id: str) -> Money:
        a = self.account(account_id)
        return Money.of_minor(self.sub_ledger(a.currency).net_debit(account_id), a.currency)

    def post(self, tx: Transaction) -> bool:
        """Post a transaction: validate every entry against its sub-ledger, then append all. Idempotent on transaction id."""
        existing = self._transactions.get(tx.id)
        if existing is not None:
            if existing == tx:
                return False
            raise LedgerError(f"transaction {tx.id} already posted with different content")
        if not tx.entries:
            raise LedgerError(f"transaction {tx.id} has no entries")
        for e in tx.entries:
            for p in e.postings:
                if self.account(p.account_id).currency != e.currency:
                    raise LedgerError(f"entry {e.id}: account {p.account_id} is not a {e.currency.code} account")
        for e in tx.entries:
            self.sub_ledger(e.currency).append(e)
        self._transactions[tx.id] = tx
        return True

    def exchange(self, tx_id: str, description: str, debit_account: str, amount: Money, credit_account: str,
                 rate: FxRate) -> Transaction:
        """Cross-currency movement: debit debit_account by amount (its currency A) and credit credit_account with the
        converted amount (currency B), as two single-currency entries joined by the clearing accounts 1900-A and
        1900-B. One of A, B must be the functional currency so the position's functional cost (basis) is known exactly."""
        a, b = self.account(debit_account).currency, self.account(credit_account).currency
        if amount.currency != a:
            raise CurrencyMismatchError(amount.currency, a)
        if a == b:
            raise LedgerError("exchange needs two currencies; post a single-currency entry instead")
        if a != self.functional and b != self.functional:
            raise LedgerError(f"exchange {a.code}->{b.code} must route through {self.functional.code}")
        converted = rate.convert(amount, Rounding.HALF_EVEN)
        if converted.currency != b:
            raise CurrencyMismatchError(converted.currency, b)
        if not converted.is_positive():
            raise LedgerError("converted amount rounds to zero")
        tx = Transaction(tx_id, description, (
            JournalEntry.of(f"{tx_id}/{a.code}", Posting.debit(debit_account, amount), Posting.credit(self.clearing_id(a), amount)),
            JournalEntry.of(f"{tx_id}/{b.code}", Posting.debit(self.clearing_id(b), converted), Posting.credit(credit_account, converted)),
        ), rate)
        if self.post(tx):
            # Position outside clearing: A gains +amount, B loses -converted. Basis follows at the functional value.
            if b == self.functional:
                self._basis[a] += converted.minor_units
            else:
                self._basis[b] -= amount.minor_units
        return self._transactions[tx_id]

    def position(self, c: Currency) -> Money:
        """Net debit of every non-clearing account in a foreign currency: what we actually hold (+) or owe (-)."""
        sub = self.sub_ledger(c)
        acc = sum(sub.net_debit(a.id) for a in self._accounts.values() if a.currency == c and not a.clearing)
        return Money.of_minor(acc, c)

    def position_report(self, c: Currency) -> Position:
        self._require_foreign(c)
        return Position(c, self.position(c), Money.of_minor(self._basis[c], self.functional),
                        Money.of_minor(self._revalued[c], self.functional))

    def revalue(self, c: Currency, closing_rate: FxRate) -> Money:
        """Mark the foreign position to the closing rate. The difference between its value at that rate and its
        carrying value (basis + earlier revaluations) is booked as unrealized gain/loss, in functional currency only:
        gain -> Dr 1950-c / Cr 4900; loss -> Dr 4900 / Cr 1950-c. Returns the gain (negative for a loss)."""
        self._require_foreign(c)
        fair = closing_rate.convert(self.position(c), Rounding.HALF_EVEN)
        gain = fair.minor_units - (self._basis[c] + self._revalued[c])
        if gain != 0:
            g = Money.of_minor(abs(gain), self.functional)
            self._adjustment_counter += 1
            id = f"reval-{self._adjustment_counter}-{c.code}"
            if gain > 0:
                e = JournalEntry.of(id, Posting.debit(self.revaluation_id(c), g), Posting.credit(self.UNREALIZED, g))
            else:
                e = JournalEntry.of(id, Posting.debit(self.UNREALIZED, g), Posting.credit(self.revaluation_id(c), g))
            self.post(Transaction(id, f"revalue {c.code} at {closing_rate}", (e,), closing_rate))
            self._revalued[c] += gain
        return Money.of_minor(gain, self.functional)

    def realize(self, c: Currency) -> Money:
        """When a foreign position is flat (zero outside clearing), its remaining basis is the realized gain/loss.
        Book it from functional clearing to 4910 and reverse the unrealized revaluations, which are no longer unrealized.
        Returns the realized gain (negative for a loss)."""
        self._require_foreign(c)
        if not self.position(c).is_zero():
            raise LedgerError(f"position in {c.code} is {self.position(c)}; realize only a flat position")
        realized_gain = -self._basis[c]
        entries: List[JournalEntry] = []
        self._adjustment_counter += 1
        id = f"realize-{self._adjustment_counter}-{c.code}"
        if realized_gain != 0:
            g = Money.of_minor(abs(realized_gain), self.functional)
            if realized_gain > 0:
                entries.append(JournalEntry.of(f"{id}/pnl", Posting.debit(self.clearing_id(self.functional), g), Posting.credit(self.REALIZED, g)))
            else:
                entries.append(JournalEntry.of(f"{id}/pnl", Posting.debit(self.REALIZED, g), Posting.credit(self.clearing_id(self.functional), g)))
        rev = self._revalued[c]
        if rev != 0:
            r = Money.of_minor(abs(rev), self.functional)
            if rev > 0:
                entries.append(JournalEntry.of(f"{id}/unwind", Posting.debit(self.UNREALIZED, r), Posting.credit(self.revaluation_id(c), r)))
            else:
                entries.append(JournalEntry.of(f"{id}/unwind", Posting.debit(self.revaluation_id(c), r), Posting.credit(self.UNREALIZED, r)))
        if entries:
            self.post(Transaction(id, f"realize {c.code} position", tuple(entries), None))
        self._basis[c] = 0
        self._revalued[c] = 0
        return Money.of_minor(realized_gain, self.functional)

    def clearing_matches_basis(self) -> bool:
        """Invariant: functional clearing net debit == sum of open foreign bases (each exchange moves both by the same amount)."""
        return self.sub_ledger(self.functional).net_debit(self.clearing_id(self.functional)) == sum(self._basis.values())

    def all_sub_ledgers_balance(self) -> bool:
        """Every sub-ledger nets to zero in its own currency."""
        return all(s.trial_balance_net() == 0 for s in self._sub_ledgers.values())

    def currencies(self) -> List[Currency]:
        return list(self._sub_ledgers)

    def accounts(self) -> List[Account]:
        return list(self._accounts.values())

    def _require_foreign(self, c: Currency) -> None:
        if c == self.functional or c not in self._basis:
            raise LedgerError(f"{c.code} is not a foreign currency of this ledger")


def print_transaction(tx: Transaction) -> None:
    print(f"{tx.id}  {tx.description}{'' if tx.rate is None else f'  @ {tx.rate}'}")
    for e in tx.entries:
        print(f"  [{e.currency.code} sub-ledger] {e.id}")
        for p in e.postings:
            print(f"{'    Dr ' if p.side is Side.DEBIT else '        Cr '}{p.account_id}  {p.amount}")


def print_balances(l: MultiCurrencyLedger) -> None:
    for a in l.accounts():
        b = l.balance(a.id)
        if not b.is_zero():
            print(f"  {a.id:<9} {a.name:<34} {b}")
    for c in l.currencies():
        print(f"  {c.code} sub-ledger nets to {Money.of_minor(l.sub_ledger(c).trial_balance_net(), c)}")


def _bool(b: bool) -> str:
    return str(b).lower()


def main() -> None:
    day = 1_767_225_600
    ledger = MultiCurrencyLedger(USD)
    ledger.open_account("1200-USD", "Processor Receivable", AccountType.ASSET, USD)
    ledger.open_account("1010-USD", "Operating Bank", AccountType.ASSET, USD)
    ledger.open_account("1010-EUR", "Operating Bank", AccountType.ASSET, EUR)
    ledger.open_account("2100-EUR", "Merchant Payable", AccountType.LIABILITY, EUR)

    print("== A US customer pays 110.00 USD; the German merchant is owed EUR at 1 EUR = 1.1000 USD ==")
    booked = FxRate(EUR, USD, 11_000, 10_000, "rate-desk", day)
    print_transaction(ledger.exchange("fx-1", "cross-border card payment", "1200-USD", Money.parse("110.00", USD), "2100-EUR", booked))
    print_balances(ledger)
    open_ = ledger.position_report(EUR)
    print(f"  EUR position {open_.position} (we owe EUR), basis {open_.basis} (its USD value when booked)")

    print("\n== Mixing currencies in one entry is rejected ==")
    try:
        JournalEntry("bad", USD, [Posting.debit("1200-USD", Money.parse("110.00", USD)),
                                  Posting.credit("2100-EUR", Money.parse("100.00", EUR))])
    except MixedCurrencyError as e:
        print(f"rejected: {e}")

    print("\n== Month end: EUR strengthens to 1.1200 USD; revalue the open EUR position ==")
    close = FxRate(EUR, USD, 11_200, 10_000, "ECB reference", day + 30 * 86_400)
    print(f"unrealized gain/loss booked: {ledger.revalue(EUR, close)}")
    print(f"revalue again at the same rate: {ledger.revalue(EUR, close)} (idempotent: nothing left to book)")
    p = ledger.position_report(EUR)
    print(f"  EUR position {p.position}, basis {p.basis}, revalued {p.revalued}, carrying {p.carrying_value()}")
    print(f"  4900 Unrealized FX Gain/Loss {ledger.balance('4900')}")

    print("\n== Processor settles 110.00 USD; treasury buys 100.00 EUR at 1.1150; merchant is paid ==")
    ledger.post(Transaction("stl-1", "processor settlement", (JournalEntry.of(
        "stl-1/USD", Posting.debit("1010-USD", Money.parse("110.00", USD)), Posting.credit("1200-USD", Money.parse("110.00", USD))),), None))
    market = FxRate(EUR, USD, 11_150, 10_000, "bank quote", day + 31 * 86_400)
    print_transaction(ledger.exchange("fx-2", "buy EUR for payout", "1010-EUR", Money.parse("100.00", EUR), "1010-USD", market))
    ledger.post(Transaction("out-1", "pay merchant", (JournalEntry.of(
        "out-1/EUR", Posting.debit("2100-EUR", Money.parse("100.00", EUR)), Posting.credit("1010-EUR", Money.parse("100.00", EUR))),), None))
    print(f"EUR position now {ledger.position(EUR)}; functional clearing holds {ledger.net_debit('1900-USD')}")
    print(f"realized gain/loss: {ledger.realize(EUR)}")
    print(f"  4900 Unrealized {ledger.balance('4900')}, 4910 Realized {ledger.balance('4910')}"
          f", 1950-EUR {ledger.balance('1950-EUR')}, 1900-USD {ledger.net_debit('1900-USD')}")
    print(f"  operating bank 1010-USD {ledger.balance('1010-USD')}: the 1.50 USD the round trip cost")
    print(f"  every sub-ledger balances: {_bool(ledger.all_sub_ledgers_balance())}; "
          f"clearing matches basis: {_bool(ledger.clearing_matches_basis())}")

    print("\n== Exponents differ: 5,000 JPY to USD at 1 USD = 157.25 JPY ==")
    jpy = FxRate(USD, JPY, 15_725, 100, "rate-desk", day)
    print(f"5000 JPY -> {jpy.convert(Money.parse('5000', JPY), Rounding.HALF_EVEN)}"
          f"; 31.80 USD -> {jpy.convert(Money.parse('31.80', USD), Rounding.HALF_EVEN)}")


if __name__ == "__main__":
    main()
