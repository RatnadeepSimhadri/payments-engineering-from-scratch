"""Lesson: phases/10-money-and-ledgers/03-double-entry-ledger/docs/en.md
The Double-Entry Ledger from Scratch
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: Pacioli, Summa de arithmetica (1494) for the debit=credit rule; ISO 4217 for currency exponents.
Money type copied from Phase 10, Lesson 01. Python 3.11+, standard library only. Same design as Java and Go.
"""

from __future__ import annotations

import random
from dataclasses import dataclass, field
from enum import Enum
from functools import total_ordering

# ---------------------------------------------------------------------------------------------
# Money (copied from Phase 10, Lesson 01). Integer minor units + ISO 4217 currency. Never a float.
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


class Rounding(Enum):
    HALF_EVEN = "half_even"
    HALF_UP = "half_up"
    DOWN = "down"
    UP = "up"


class CurrencyMismatchError(ValueError):
    def __init__(self, a: Currency, b: Currency) -> None:
        super().__init__(f"cannot combine {a.code} with {b.code}")


def divide_rounded(dividend: int, divisor: int, rounding: Rounding) -> int:
    """Truncation semantics identical to Java/Go: work on magnitudes, reapply the sign."""
    if divisor == 0:
        raise ZeroDivisionError("divisor must not be zero")
    sign = -1 if (dividend < 0) != (divisor < 0) else 1
    quotient, remainder = divmod(abs(dividend), abs(divisor))
    if remainder == 0:
        return sign * quotient
    twice = remainder * 2
    if rounding is Rounding.UP:
        quotient += 1
    elif rounding is Rounding.HALF_UP and twice >= abs(divisor):
        quotient += 1
    elif rounding is Rounding.HALF_EVEN and (twice > abs(divisor) or (twice == abs(divisor) and quotient % 2 == 1)):
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
            raise ValueError(f"not a decimal amount: {text!r}")
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
        whole, frac = divmod(magnitude, self.currency.minor_units_per_major())
        text = str(whole)
        if self.currency.exponent > 0:
            text += "." + str(frac).rjust(self.currency.exponent, "0")
        return f"{'-' if self.minor_units < 0 else ''}{text} {self.currency.code}"

    __repr__ = __str__


# ---------------------------------------------------------------------------------------------
# The ledger.
# ---------------------------------------------------------------------------------------------


class Side(Enum):
    """Which side of an entry a posting sits on. A posting is never signed: positive amount, one side."""

    DEBIT = "debit"
    CREDIT = "credit"

    def opposite(self) -> "Side":
        return Side.CREDIT if self is Side.DEBIT else Side.DEBIT


class AccountType(Enum):
    """The five account types; the value is the side that increases the account (its normal balance)."""

    ASSET = Side.DEBIT
    EXPENSE = Side.DEBIT
    LIABILITY = Side.CREDIT
    EQUITY = Side.CREDIT
    REVENUE = Side.CREDIT

    @property
    def normal_balance(self) -> Side:
        return self.value


class LedgerError(ValueError):
    """Base class for every rejection the ledger can produce."""


class UnbalancedEntryError(LedgerError):
    def __init__(self, entry_id: str, detail: str) -> None:
        super().__init__(f"entry {entry_id} rejected: {detail}")


class InvalidPostingError(LedgerError):
    pass


class UnknownAccountError(LedgerError):
    def __init__(self, account_id: str) -> None:
        super().__init__(f"unknown account: {account_id}")


class UnknownEntryError(LedgerError):
    def __init__(self, entry_id: str) -> None:
        super().__init__(f"unknown entry: {entry_id}")


class IdempotencyConflictError(LedgerError):
    def __init__(self, entry_id: str) -> None:
        super().__init__(f"entry id {entry_id} already posted with different content")


class AlreadyReversedError(LedgerError):
    def __init__(self, entry_id: str) -> None:
        super().__init__(f"entry {entry_id} has already been reversed")


@dataclass(frozen=True)
class Account:
    id: str
    name: str
    type: AccountType
    currency: Currency

    def __post_init__(self) -> None:
        if not self.id or not self.id.strip():
            raise ValueError("account id required")


@dataclass(frozen=True)
class Posting:
    """One line of a journal entry: a positive amount of one currency on one side of one account."""

    account_id: str
    side: Side
    amount: Money

    def __post_init__(self) -> None:
        if not self.amount.is_positive():
            raise InvalidPostingError(f"posting amount must be positive, got {self.amount}")

    @staticmethod
    def debit(account_id: str, amount: Money) -> "Posting":
        return Posting(account_id, Side.DEBIT, amount)

    @staticmethod
    def credit(account_id: str, amount: Money) -> "Posting":
        return Posting(account_id, Side.CREDIT, amount)

    def flipped(self) -> "Posting":
        return Posting(self.account_id, self.side.opposite(), self.amount)

    def signed_minor_units(self) -> int:
        return self.amount.minor_units if self.side is Side.DEBIT else -self.amount.minor_units

    def __str__(self) -> str:
        prefix = "  Dr " if self.side is Side.DEBIT else "      Cr "
        return f"{prefix}{self.account_id}  {self.amount}"


@dataclass(frozen=True)
class JournalEntry:
    """An immutable, balanced set of postings. If a JournalEntry exists, debits equal credits per currency."""

    id: str
    description: str
    postings: tuple[Posting, ...]
    reversal_of: str | None = None

    def __init__(self, id: str, description: str, postings, reversal_of: str | None = None) -> None:
        if not id or not id.strip():
            raise ValueError("entry id required")
        postings = tuple(postings)
        if len(postings) < 2:
            raise UnbalancedEntryError(id, "an entry needs at least two postings")
        net: dict[Currency, int] = {}
        for p in postings:
            net[p.amount.currency] = net.get(p.amount.currency, 0) + p.signed_minor_units()
        problems = [f"debits minus credits in {c.code} = {Money(v, c)}" for c, v in net.items() if v != 0]
        if problems:
            raise UnbalancedEntryError(id, "; ".join(problems) + " (must be zero per currency)")
        object.__setattr__(self, "id", id)
        object.__setattr__(self, "description", description)
        object.__setattr__(self, "postings", postings)
        object.__setattr__(self, "reversal_of", reversal_of)

    def reversal(self, new_id: str, description: str) -> "JournalEntry":
        """The entry that undoes this one: same postings, every side flipped. Nothing is ever edited."""
        return JournalEntry(new_id, description, [p.flipped() for p in self.postings], reversal_of=self.id)

    def total_debits(self, currency: Currency) -> Money:
        acc = Money.zero(currency)
        for p in self.postings:
            if p.side is Side.DEBIT and p.amount.currency == currency:
                acc = acc.plus(p.amount)
        return acc


@dataclass(frozen=True)
class TrialBalanceRow:
    account: Account
    debit: Money
    credit: Money


@dataclass(frozen=True)
class TrialBalance:
    """The trial balance for one currency. Its two column totals are equal or the ledger is corrupt."""

    currency: Currency
    rows: tuple[TrialBalanceRow, ...]
    total_debits: Money
    total_credits: Money

    def balances(self) -> bool:
        return self.total_debits == self.total_credits


class Ledger:
    """An append-only journal plus a chart of accounts. Balances are derived from postings and cached
    as two running totals per account, which the tests prove equal a fresh recomputation."""

    def __init__(self) -> None:
        self._accounts: dict[str, Account] = {}
        self._journal: list[JournalEntry] = []
        self._index_by_id: dict[str, int] = {}
        self._reversed_by: dict[str, str] = {}
        self._debit_totals: dict[str, int] = {}
        self._credit_totals: dict[str, int] = {}

    def open_account(self, id: str, name: str, type: AccountType, currency: Currency) -> Account:
        if id in self._accounts:
            raise ValueError(f"account exists: {id}")
        account = Account(id, name, type, currency)
        self._accounts[id] = account
        self._debit_totals[id] = 0
        self._credit_totals[id] = 0
        return account

    def account(self, id: str) -> Account:
        try:
            return self._accounts[id]
        except KeyError:
            raise UnknownAccountError(id) from None

    def post(self, entry: JournalEntry) -> bool:
        """Append an entry. True if appended; False if the identical entry is already in the journal
        (idempotent replay). A different entry under the same id is a conflict. All validation runs
        before the first mutation, so a rejected entry leaves no trace."""
        existing = self._index_by_id.get(entry.id)
        if existing is not None:
            if self._journal[existing] == entry:
                return False
            raise IdempotencyConflictError(entry.id)
        for p in entry.postings:
            account = self.account(p.account_id)
            if account.currency != p.amount.currency:
                raise InvalidPostingError(
                    f"entry {entry.id} rejected: account {account.id} is {account.currency.code} "
                    f"but posting is {p.amount.currency.code}")
        if entry.reversal_of is not None:
            if entry.reversal_of not in self._index_by_id:
                raise UnknownEntryError(entry.reversal_of)
            if entry.reversal_of in self._reversed_by:
                raise AlreadyReversedError(entry.reversal_of)
        # Validation complete: now mutate, and mutate everything.
        self._journal.append(entry)
        self._index_by_id[entry.id] = len(self._journal) - 1
        if entry.reversal_of is not None:
            self._reversed_by[entry.reversal_of] = entry.id
        for p in entry.postings:
            totals = self._debit_totals if p.side is Side.DEBIT else self._credit_totals
            totals[p.account_id] += p.amount.minor_units
        return True

    def reverse(self, entry_id: str, new_entry_id: str, description: str) -> JournalEntry:
        """Post the reversal of an existing entry. The original stays in the journal forever."""
        reversal = self.entry(entry_id).reversal(new_entry_id, description)
        self.post(reversal)
        return reversal

    def entry(self, id: str) -> JournalEntry:
        try:
            return self._journal[self._index_by_id[id]]
        except KeyError:
            raise UnknownEntryError(id) from None

    def entries(self) -> tuple[JournalEntry, ...]:
        return tuple(self._journal)

    def size(self) -> int:
        return len(self._journal)

    def balance(self, account_id: str) -> Money:
        """Balance in the account's normal direction: positive means what you expect for that type."""
        account = self.account(account_id)
        debits, credits = self._debit_totals[account_id], self._credit_totals[account_id]
        signed = debits - credits if account.type.normal_balance is Side.DEBIT else credits - debits
        return Money(signed, account.currency)

    def net_debit(self, account_id: str) -> Money:
        """Debits minus credits regardless of type. Sums to zero across all accounts of a currency."""
        account = self.account(account_id)
        return Money(self._debit_totals[account_id] - self._credit_totals[account_id], account.currency)

    def net_debit_from_postings(self, account_id: str) -> Money:
        """Recompute by scanning every posting. The cache must always agree with this."""
        account = self.account(account_id)
        total = sum(p.signed_minor_units() for e in self._journal for p in e.postings if p.account_id == account_id)
        return Money(total, account.currency)

    def trial_balance(self, currency: Currency) -> TrialBalance:
        rows = []
        total_debits = total_credits = Money.zero(currency)
        for account in self._accounts.values():
            if account.currency != currency:
                continue
            net = self.net_debit(account.id)
            debit = Money.zero(currency) if net.is_negative() else net
            credit = net.negate() if net.is_negative() else Money.zero(currency)
            rows.append(TrialBalanceRow(account, debit, credit))
            total_debits = total_debits.plus(debit)
            total_credits = total_credits.plus(credit)
        return TrialBalance(currency, tuple(rows), total_debits, total_credits)


class RandomEntries:
    """Deterministic generator of balanced entries for stress tests: seeded, never the wall clock."""

    def __init__(self, seed: int, account_ids: list[str], currency: Currency) -> None:
        self._random = random.Random(seed)
        self._account_ids = list(account_ids)
        self._currency = currency
        self._counter = 0

    def next(self) -> JournalEntry:
        legs = 2 + self._random.randrange(3)  # 2..4 postings
        postings = []
        debit_sum = 0
        for _ in range(legs - 1):
            amount = 1 + self._random.randrange(100_000)  # 0.01 .. 1000.00
            postings.append(Posting.debit(self._random.choice(self._account_ids), Money(amount, self._currency)))
            debit_sum += amount
        postings.append(Posting.credit(self._random.choice(self._account_ids), Money(debit_sum, self._currency)))
        entry = JournalEntry(f"rnd-{self._counter}", "random balanced entry", postings)
        self._counter += 1
        return entry


def print_entry(e: JournalEntry) -> None:
    suffix = f"  (reverses {e.reversal_of})" if e.reversal_of else ""
    print(f"{e.id}  {e.description}{suffix}")
    for p in e.postings:
        print(p)


def print_trial_balance(ledger: Ledger, currency: Currency) -> None:
    tb = ledger.trial_balance(currency)
    print(f"{'account':<28} {'debit':>14} {'credit':>14}")
    for row in tb.rows:
        debit = "" if row.debit.is_zero() else str(row.debit)
        credit = "" if row.credit.is_zero() else str(row.credit)
        print(f"{row.account.id + ' ' + row.account.name:<28} {debit:>14} {credit:>14}")
    print(f"{'TOTAL':<28} {str(tb.total_debits):>14} {str(tb.total_credits):>14}   balances={tb.balances()}")


def main() -> None:
    ledger = Ledger()
    ledger.open_account("1010", "Settlement Bank", AccountType.ASSET, USD)
    ledger.open_account("1200", "Processor Receivable", AccountType.ASSET, USD)
    ledger.open_account("2100", "Merchant Payable", AccountType.LIABILITY, USD)
    ledger.open_account("4100", "Fee Revenue", AccountType.REVENUE, USD)

    print("== A customer pays a merchant 100.00 USD; we keep a 2.9% fee ==")
    gross = Money.parse("100.00", USD)
    fee = gross.times(29, 1000, Rounding.HALF_EVEN)
    net = gross.minus(fee)
    sale = JournalEntry("pay-001", "card payment captured", [
        Posting.debit("1200", gross), Posting.credit("2100", net), Posting.credit("4100", fee)])
    print(f"posted={ledger.post(sale)}")
    print_entry(sale)
    print(f"replay posted={ledger.post(sale)}  (same id, same content: no-op)")

    settle = JournalEntry("stl-001", "processor settles to our bank", [Posting.debit("1010", gross), Posting.credit("1200", gross)])
    ledger.post(settle)
    print_entry(settle)
    payout = JournalEntry("out-001", "payout to merchant", [Posting.debit("2100", net), Posting.credit("1010", net)])
    ledger.post(payout)
    print_entry(payout)

    print("\n== Balances (in each account's normal direction) ==")
    for account_id in ("1010", "1200", "2100", "4100"):
        a = ledger.account(account_id)
        print(f"{account_id} {a.name:<22} {a.type.name:<9} {ledger.balance(account_id)}")
    print("\n== Trial balance ==")
    print_trial_balance(ledger, USD)

    print("\n== Entries that cannot exist ==")
    attempts = (
        lambda: JournalEntry("bad-1", "unbalanced", [Posting.debit("1200", gross), Posting.credit("2100", net)]),
        lambda: JournalEntry("bad-2", "mixed currency", [Posting.debit("1200", gross), Posting.credit("2100", Money.parse("100.00", EUR))]),
        lambda: ledger.post(JournalEntry("pay-001", "same id, different content", [Posting.debit("1200", net), Posting.credit("2100", net)])),
    )
    for attempt in attempts:
        try:
            attempt()
        except LedgerError as exc:
            print(f"rejected: {exc}")

    print("\n== Reversal: the merchant refunds the customer; nothing is edited ==")
    before = ledger.balance("4100")
    reversal = ledger.reverse("pay-001", "rev-001", "refund: reverse pay-001")
    print_entry(reversal)
    print(f"fee revenue before refund {before}, after {ledger.balance('4100')}")
    print(f"journal now holds {ledger.size()} entries; pay-001 is still entry #1")

    print("\n== 10,000 seeded random balanced entries: trial balance still sums to zero ==")
    gen = RandomEntries(42, ["1010", "1200", "2100", "4100"], USD)
    for _ in range(10_000):
        ledger.post(gen.next())
    tb = ledger.trial_balance(USD)
    print(f"entries={ledger.size()} totalDebits={tb.total_debits} totalCredits={tb.total_credits} balances={tb.balances()}")


if __name__ == "__main__":
    main()
