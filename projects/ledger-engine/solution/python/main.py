"""Project: projects/ledger-engine — reference solution (Python), all four stages in one module.
Stage 1: Money, Side, AccountType, Account, AccountRegistry.
Stage 2: Posting, JournalEntry, PostedEntry, Ledger.post.
Stage 3: balance, balance_at, snapshots, trial_balance, reverse.
Stage 4: lines, reconcile, ReconciliationReport.
Docs: projects/ledger-engine/stages/*/docs/en.md
Run:  python3 main.py
Test: python3 scripts/project_test.py ledger-engine --lang python --all --solution
Python 3.11+, standard library only. Same design and names as the Java solution (snake_case methods).
Deterministic: no clock, no randomness, no I/O beyond stdout.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from datetime import date as _date
from enum import Enum
from functools import total_ordering
from types import MappingProxyType
from typing import Mapping, Sequence

# ---------------------------------------------------------------- stage 1: Money (same design as phases/10/01)

LONG_MIN = -(2**63)
LONG_MAX = 2**63 - 1


def _check_long(value: int) -> int:
    """Python ints never overflow; the Java/Go versions use 64-bit longs with Math.addExact.
    Keeping the same bound makes overflow a loud error here too (OverflowError is an ArithmeticError)."""
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
    """How to resolve a fractional minor unit. Named after the behaviour, not after a language's enum."""

    HALF_EVEN = "half_even"  # banker's rounding: ties go to the even neighbour; statistically unbiased
    HALF_UP = "half_up"  # ties go away from zero; what most people mean by "round"
    DOWN = "down"  # truncate toward zero; what you owe never rounds up against the payer
    UP = "up"  # away from zero; fees that must cover a cost


class CurrencyMismatchError(ValueError):
    def __init__(self, a: Currency, b: Currency) -> None:
        super().__init__(f"cannot combine {a.code} with {b.code}")


def divide_rounded(dividend: int, divisor: int, rounding: Rounding) -> int:
    """Integer division with an explicit rounding rule. Python's // floors toward -inf, so we work on
    magnitudes and reapply the sign to get the truncation semantics of Java's / and %."""
    if divisor == 0:
        raise ZeroDivisionError("divisor must not be zero")
    sign = -1 if (dividend < 0) != (divisor < 0) else 1
    quotient, remainder = divmod(abs(dividend), abs(divisor))
    if remainder == 0:
        return sign * quotient
    twice = remainder * 2
    abs_divisor = abs(divisor)
    if rounding is Rounding.DOWN:
        pass
    elif rounding is Rounding.UP:
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
    """An exact amount of one currency, stored as a signed count of minor units. Immutable.
    Arithmetic never produces a fraction: every operation that could is given a Rounding."""

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
        """Parse "12.34", "-0.5", ".99", "1200" exactly; reject more decimals than the currency allows.
        Excess precision -> ArithmeticError; anything that is not a decimal -> ValueError."""
        s = text.strip()
        negative = s.startswith("-")
        if negative or s.startswith("+"):
            s = s[1:]
        if not s:
            raise ValueError("empty amount")
        whole, _, frac = s.partition(".")
        whole = whole or "0"
        if not whole.isdigit() or (frac and not frac.isdigit()) or not whole.isascii() or not frac.isascii():
            raise ValueError(f"not a decimal amount: {text!r}")
        if len(frac) > currency.exponent:
            raise ArithmeticError(f"{text} has more decimals than {currency.code} allows ({currency.exponent})")
        frac = frac.ljust(currency.exponent, "0")
        minor = _check_long(int(whole) * currency.minor_units_per_major()) + (int(frac) if frac else 0)
        return Money(-minor if negative else minor, currency)

    def _require_same_currency(self, other: Money) -> None:
        if self.currency != other.currency:
            raise CurrencyMismatchError(self.currency, other.currency)

    def plus(self, other: Money) -> Money:
        self._require_same_currency(other)
        return Money(self.minor_units + other.minor_units, self.currency)

    def minus(self, other: Money) -> Money:
        self._require_same_currency(other)
        return Money(self.minor_units - other.minor_units, self.currency)

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
        """Multiply by an exact rational (2.9% is 29/1000; an FX rate of 0.9217 is 9217/10000) and
        resolve the remainder with the given rounding. The only rounding is the one you asked for."""
        if denominator <= 0:
            raise ArithmeticError("denominator must be positive")
        product = _check_long(self.minor_units * numerator)
        return Money(divide_rounded(product, denominator, rounding), self.currency)

    @staticmethod
    def divide_rounded(dividend: int, divisor: int, rounding: Rounding) -> int:
        """Integer division with an explicit rounding rule. Public so the ledger can reuse it."""
        return divide_rounded(dividend, divisor, rounding)

    def allocate(self, *ratios: int) -> list[Money]:
        """Split this amount in the given ratios so the parts sum exactly to the whole.
        Largest-remainder method: floor of each share, then the leftover minor units go one at a time to
        the shares with the largest remainder; ties keep index order (sorted is stable)."""
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
        leftover = magnitude - sum(shares)  # always 0 <= leftover < len(ratios)
        order = sorted(range(len(ratios)), key=lambda i: -remainders[i])
        for i in order[:leftover]:
            shares[i] += 1
        sign = -1 if self.minor_units < 0 else 1
        return [Money(sign * s, self.currency) for s in shares]

    @staticmethod
    def sum(currency: Currency, *amounts: Money) -> Money:
        """Sum any number of same-currency amounts; an empty sum of a currency is that currency's zero."""
        acc = Money(0, currency)
        for m in amounts:
            acc = acc.plus(m)
        return acc

    def __lt__(self, other: Money) -> bool:
        self._require_same_currency(other)
        return self.minor_units < other.minor_units

    def __str__(self) -> str:
        """"12.34 USD", "-0.50 EUR", "1200 JPY", "1.250 KWD": the decimal point is placed by the exponent."""
        magnitude = abs(self.minor_units)
        whole, frac = divmod(magnitude, self.currency.minor_units_per_major())
        text = str(whole)
        if self.currency.exponent > 0:
            text += "." + str(frac).rjust(self.currency.exponent, "0")
        return f"{'-' if self.minor_units < 0 else ''}{text} {self.currency.code}"

    __repr__ = __str__


# ---------------------------------------------------------------- stage 1: accounts


class Side(Enum):
    """Which column a posting lands in. Debit and credit are directions, not "minus" and "plus"."""

    DEBIT = "debit"
    CREDIT = "credit"

    def opposite(self) -> Side:
        return Side.CREDIT if self is Side.DEBIT else Side.DEBIT


class AccountType(Enum):
    """The five account classes of double-entry bookkeeping. The class decides which side increases the account."""

    ASSET = "asset"
    LIABILITY = "liability"
    EQUITY = "equity"
    REVENUE = "revenue"
    EXPENSE = "expense"

    def normal_side(self) -> Side:
        """Assets and expenses grow on the debit side; liabilities, equity, and revenue grow on the credit side."""
        return Side.DEBIT if self in (AccountType.ASSET, AccountType.EXPENSE) else Side.CREDIT


class LedgerError(ValueError):
    """Base type for every rule the ledger enforces, so callers can catch "the ledger said no" in one place."""


class DuplicateAccountError(LedgerError):
    def __init__(self, account_id: str) -> None:
        super().__init__(f"account already exists: {account_id}")


class UnknownAccountError(LedgerError):
    def __init__(self, account_id: str) -> None:
        super().__init__(f"unknown account: {account_id}")


def _require_text(value: object, what: str) -> str:
    if not isinstance(value, str) or not value.strip():
        raise ValueError(f"{what} must not be blank")
    return value


@dataclass(frozen=True)
class Account:
    """An account's identity never changes: id, class, and currency are fixed when it is opened."""

    id: str
    type: AccountType
    currency: Currency

    def __post_init__(self) -> None:
        _require_text(self.id, "account id")
        if not isinstance(self.type, AccountType):
            raise TypeError("type must be an AccountType")
        if not isinstance(self.currency, Currency):
            raise TypeError("currency must be a Currency")

    def normal_side(self) -> Side:
        """The side on which this account's balance grows."""
        return self.type.normal_side()


class AccountRegistry:
    """The chart of accounts: every account the ledger knows, in opening order. Ids are unique. Not thread-safe."""

    def __init__(self) -> None:
        self._accounts: dict[str, Account] = {}

    def open(self, account_id: str, type: AccountType, currency: Currency) -> Account:
        """Open an account. The same id can never be opened twice, even with identical attributes."""
        account = Account(account_id, type, currency)
        if account.id in self._accounts:
            raise DuplicateAccountError(account.id)
        self._accounts[account.id] = account
        return account

    def get(self, account_id: str) -> Account:
        """Look an account up; an unknown id is an error, never None."""
        account = self._accounts.get(account_id)
        if account is None:
            raise UnknownAccountError(account_id)
        return account

    def contains(self, account_id: str) -> bool:
        return account_id in self._accounts

    def size(self) -> int:
        return len(self._accounts)

    def all(self) -> tuple[Account, ...]:
        """Accounts in opening order; an immutable copy, so the chart cannot be edited from outside."""
        return tuple(self._accounts.values())


# ---------------------------------------------------------------- stage 2: postings, entries, exceptions


@dataclass(frozen=True)
class Posting:
    """One leg of an entry: a positive amount on one side of one account."""

    account_id: str
    side: Side
    amount: Money

    def __post_init__(self) -> None:
        _require_text(self.account_id, "posting account id")
        if not isinstance(self.side, Side):
            raise TypeError("side must be a Side")
        if not isinstance(self.amount, Money):
            raise TypeError("amount must be a Money")
        if not self.amount.is_positive():
            raise ValueError(f"posting amount must be positive: {self.amount}")

    def mirrored(self) -> Posting:
        """The posting with its side flipped: the building block of a reversal (stage 3)."""
        return Posting(self.account_id, self.side.opposite(), self.amount)


@dataclass(frozen=True)
class JournalEntry:
    """An atomic group of postings identified by a caller-chosen id (the idempotency key) and an ISO-8601
    booking date ("2026-10-01"). Immutable: the postings are copied into a tuple. Construction does not
    check balance; Ledger.post does, so a bad entry can be built, offered, and refused without ever
    touching the journal."""

    id: str
    date: str
    postings: tuple[Posting, ...]

    def __post_init__(self) -> None:
        _require_text(self.id, "entry id")
        _require_text(self.date, "entry date")
        postings = tuple(self.postings)
        if any(not isinstance(p, Posting) for p in postings):
            raise TypeError("postings must be Posting values")
        object.__setattr__(self, "postings", postings)

    @staticmethod
    def of(id: str, date: str, *postings: Posting) -> JournalEntry:
        """Varargs convenience, the Java JournalEntry(String, String, Posting...) constructor."""
        return JournalEntry(id, date, postings)

    def currency(self) -> Currency:
        """The currency of the first posting; Ledger.post guarantees every posting shares it."""
        if not self.postings:
            raise RuntimeError("entry has no postings")
        return self.postings[0].amount.currency


@dataclass(frozen=True)
class PostedEntry:
    """What the journal actually stores: the entry plus the sequence number it was given on arrival (1, 2, 3, ...)."""

    sequence: int
    entry: JournalEntry

    @property
    def id(self) -> str:
        return self.entry.id


class InvalidEntryError(LedgerError):
    def __init__(self, entry_id: str, why: str) -> None:
        super().__init__(f"entry {entry_id} rejected: {why}")


class UnbalancedEntryError(LedgerError):
    def __init__(self, entry_id: str, debits: Money, credits: Money) -> None:
        super().__init__(f"entry {entry_id} is unbalanced: debits {debits} != credits {credits}")


class UnknownEntryError(LedgerError):
    def __init__(self, entry_id: str) -> None:
        super().__init__(f"unknown entry: {entry_id}")


# ---------------------------------------------------------------- stage 3: snapshots and trial balance values


@dataclass(frozen=True)
class BalanceSnapshot:
    """Signed balances of every account after the entry with this sequence number was applied."""

    sequence: int
    balances: Mapping[str, Money]

    def __post_init__(self) -> None:
        object.__setattr__(self, "balances", MappingProxyType(dict(self.balances)))


@dataclass(frozen=True)
class TrialBalanceLine:
    """One row of a trial balance: the account's net balance placed in the debit or the credit column."""

    account: Account
    debit: Money
    credit: Money


@dataclass(frozen=True)
class TrialBalance:
    """All accounts of one currency with their column totals. The two totals agree, or an entry got in unbalanced."""

    currency: Currency
    lines: tuple[TrialBalanceLine, ...]
    total_debits: Money
    total_credits: Money

    def __post_init__(self) -> None:
        object.__setattr__(self, "lines", tuple(self.lines))

    def difference(self) -> Money:
        return self.total_debits.minus(self.total_credits)

    def is_balanced(self) -> bool:
        return self.difference().is_zero()


# ---------------------------------------------------------------- stage 4: statement lines, breaks, report


@dataclass(frozen=True)
class StatementLine:
    """One line of an external statement (a processor settlement file, a bank statement). The reference is
    what we match on first; the amount is signed in the account's normal direction (money in is positive
    for a bank asset account); the date is ISO-8601 ("2026-10-01")."""

    ref: str
    amount: Money
    date: str

    def __post_init__(self) -> None:
        _require_text(self.ref, "statement line reference")
        if not isinstance(self.amount, Money):
            raise TypeError("amount must be a Money")
        _require_text(self.date, "statement line date")


@dataclass(frozen=True)
class LedgerLine:
    """Our side of the same shape: one journal entry's net effect on one account, keyed by the entry id."""

    ref: str
    amount: Money
    date: str


class BreakType(Enum):
    """The four ways a statement line and a journal entry can fail to agree."""

    MISSING_IN_LEDGER = "missing_in_ledger"  # the statement has it, our journal does not
    MISSING_IN_STATEMENT = "missing_in_statement"  # our journal has it, the statement does not
    AMOUNT_MISMATCH = "amount_mismatch"  # same reference on both sides, different amounts
    DUPLICATE = "duplicate"  # the statement listed the same reference twice


class MatchedBy(Enum):
    """How a match was found: by shared reference, or by equal amount within the date window."""

    REF = "ref"
    AMOUNT_AND_DATE = "amount_and_date"


@dataclass(frozen=True)
class ReconciliationMatch:
    ledger_ref: str
    statement_ref: str
    amount: Money
    by: MatchedBy


@dataclass(frozen=True)
class ReconciliationBreak:
    """A break carries both sides; the absent side is the currency's zero, and the type says which side is absent."""

    type: BreakType
    ref: str
    ledger_amount: Money
    statement_amount: Money


@dataclass(frozen=True)
class ReconciliationReport:
    """Everything the reconciliation found, with totals that prove nothing was dropped:
    ledger_total == matched_total + ledger_break_total and statement_total == matched_total + statement_break_total."""

    account_id: str
    currency: Currency
    matches: tuple[ReconciliationMatch, ...]
    breaks: tuple[ReconciliationBreak, ...]
    ledger_total: Money
    statement_total: Money

    def __post_init__(self) -> None:
        object.__setattr__(self, "matches", tuple(self.matches))
        object.__setattr__(self, "breaks", tuple(self.breaks))

    def matched_total(self) -> Money:
        return Money.sum(self.currency, *(m.amount for m in self.matches))

    def ledger_break_total(self) -> Money:
        """The ledger-side amounts of every break (zero for breaks the ledger never saw)."""
        return Money.sum(self.currency, *(b.ledger_amount for b in self.breaks))

    def statement_break_total(self) -> Money:
        """The statement-side amounts of every break (zero for breaks the statement never saw)."""
        return Money.sum(self.currency, *(b.statement_amount for b in self.breaks))

    def breaks_of(self, type: BreakType) -> list[ReconciliationBreak]:
        return [b for b in self.breaks if b.type is type]

    def ties_out(self) -> bool:
        """True when every minor unit on both sides is accounted for by exactly one match or one break."""
        matched = self.matched_total()
        return (self.ledger_total == matched.plus(self.ledger_break_total())
                and self.statement_total == matched.plus(self.statement_break_total()))

    def is_clean(self) -> bool:
        return not self.breaks


# ---------------------------------------------------------------- the ledger


class Ledger:
    """An in-memory double-entry ledger: a chart of accounts and an append-only journal. Not thread-safe."""

    DEFAULT_SNAPSHOT_EVERY = 100

    def __init__(self, accounts: AccountRegistry, snapshot_every: int = DEFAULT_SNAPSHOT_EVERY) -> None:
        """snapshot_every is K: a running-balance snapshot is taken after every K-th posted entry."""
        if not isinstance(accounts, AccountRegistry):
            raise ValueError("ledger needs an account registry")
        if not isinstance(snapshot_every, int) or snapshot_every < 1:
            raise ValueError("snapshot_every must be >= 1")
        self._accounts = accounts
        self._snapshot_every = snapshot_every
        self._journal: list[PostedEntry] = []
        self._by_id: dict[str, PostedEntry] = {}
        self._snapshots: list[BalanceSnapshot] = []

    def accounts(self) -> AccountRegistry:
        return self._accounts

    def snapshot_every(self) -> int:
        return self._snapshot_every

    # ---------------------------------------------------------------- stage 2: posting

    def post(self, entry: JournalEntry) -> PostedEntry:
        """Append an entry to the journal and give it the next sequence number. Posting an id that is already
        in the journal is a no-op that returns the record already there. Every rule is checked before
        anything is written, so a rejected entry leaves the journal exactly as it was."""
        existing = self._by_id.get(entry.id)
        if existing is not None:
            return existing
        self._validate(entry)
        posted = PostedEntry(len(self._journal) + 1, entry)
        self._journal.append(posted)
        self._by_id[entry.id] = posted
        if posted.sequence % self._snapshot_every == 0:
            self._snapshots.append(BalanceSnapshot(posted.sequence, self._balances_at(posted.sequence)))
        return posted

    def _validate(self, entry: JournalEntry) -> None:
        """Order of checks: posting count, unknown account, mixed currencies, account currency, balance."""
        if len(entry.postings) < 2:
            raise InvalidEntryError(entry.id, f"needs at least two postings, got {len(entry.postings)}")
        currency = entry.currency()
        debits = Money.of_minor(0, currency)
        credits = Money.of_minor(0, currency)
        for posting in entry.postings:
            account = self._accounts.get(posting.account_id)
            if posting.amount.currency != currency:
                raise CurrencyMismatchError(currency, posting.amount.currency)
            if account.currency != currency:
                raise CurrencyMismatchError(account.currency, currency)
            if posting.side is Side.DEBIT:
                debits = debits.plus(posting.amount)
            else:
                credits = credits.plus(posting.amount)
        if debits != credits:
            raise UnbalancedEntryError(entry.id, debits, credits)

    def journal(self) -> tuple[PostedEntry, ...]:
        """The journal in sequence order. A copy: entries are never edited or removed."""
        return tuple(self._journal)

    def entry(self, entry_id: str) -> PostedEntry:
        posted = self._by_id.get(entry_id)
        if posted is None:
            raise UnknownEntryError(entry_id)
        return posted

    def has_entry(self, entry_id: str) -> bool:
        return entry_id in self._by_id

    def size(self) -> int:
        return len(self._journal)

    # ---------------------------------------------------------------- stage 3: balances, trial balance, reversals

    @staticmethod
    def _signed(posting: Posting, account: Account) -> Money:
        """Signed effect of one posting on its account: positive on the account's normal side."""
        return posting.amount if posting.side is account.normal_side() else posting.amount.negate()

    def balance(self, account_id: str) -> Money:
        """Derived, never stored directly: the latest snapshot plus a replay of everything after it."""
        account = self._accounts.get(account_id)
        zero = Money.of_minor(0, account.currency)
        base = self._snapshots[-1] if self._snapshots else None
        balance = zero if base is None else base.balances.get(account_id, zero)
        start = 0 if base is None else base.sequence
        for posted in self._journal[start:]:
            for posting in posted.entry.postings:
                if posting.account_id == account_id:
                    balance = balance.plus(self._signed(posting, account))
        return balance

    def balance_at(self, account_id: str, sequence: int) -> Money:
        """Full recomputation from the start of the journal through `sequence` inclusive. Ignores snapshots."""
        account = self._accounts.get(account_id)
        balance = Money.of_minor(0, account.currency)
        for posted in self._journal:
            if posted.sequence > sequence:
                break
            for posting in posted.entry.postings:
                if posting.account_id == account_id:
                    balance = balance.plus(self._signed(posting, account))
        return balance

    def _balances_at(self, sequence: int) -> dict[str, Money]:
        """Signed balance of every known account after `sequence`, by full replay."""
        return {account.id: self.balance_at(account.id, sequence) for account in self._accounts.all()}

    def snapshots(self) -> tuple[BalanceSnapshot, ...]:
        return tuple(self._snapshots)

    def trial_balance(self) -> list[TrialBalance]:
        """Per currency: each account's net (debits minus credits) goes in the debit column if positive, the
        credit column if negative. Column totals must agree because every posted entry was balanced."""
        net: dict[str, Money] = {a.id: Money.of_minor(0, a.currency) for a in self._accounts.all()}
        for posted in self._journal:
            for posting in posted.entry.postings:
                current = net[posting.account_id]
                net[posting.account_id] = (current.plus(posting.amount) if posting.side is Side.DEBIT
                                           else current.minus(posting.amount))
        groups: dict[str, list[TrialBalanceLine]] = {}
        currencies: dict[str, Currency] = {}
        for account in self._accounts.all():
            balance = net[account.id]
            zero = Money.of_minor(0, account.currency)
            line = (TrialBalanceLine(account, zero, balance.negate()) if balance.is_negative()
                    else TrialBalanceLine(account, balance, zero))
            groups.setdefault(account.currency.code, []).append(line)
            currencies[account.currency.code] = account.currency
        result: list[TrialBalance] = []
        for code in sorted(groups):
            currency = currencies[code]
            debits = Money.sum(currency, *(line.debit for line in groups[code]))
            credits = Money.sum(currency, *(line.credit for line in groups[code]))
            result.append(TrialBalance(currency, tuple(groups[code]), debits, credits))
        return result

    @staticmethod
    def reversal_id(entry_id: str) -> str:
        """The id a reversal of `entry_id` always gets, so reversing twice finds the first reversal."""
        return "rev:" + entry_id

    def reverse(self, entry_id: str) -> PostedEntry:
        """Post the mirror of an entry: same postings, every side flipped, same date as the original, id
        rev:<entry_id>. The original stays in the journal untouched. Reversing an already-reversed entry
        returns the existing reversal (post() de-duplicates on id), so the operation is idempotent."""
        original = self.entry(entry_id)
        mirrored = tuple(p.mirrored() for p in original.entry.postings)
        return self.post(JournalEntry(self.reversal_id(entry_id), original.entry.date, mirrored))

    def is_reversed(self, entry_id: str) -> bool:
        return self.reversal_id(entry_id) in self._by_id

    # ---------------------------------------------------------------- stage 4: reconciliation

    def lines(self, account_id: str) -> list[LedgerLine]:
        """Our side of a statement: one line per journal entry that touches the account, in journal order,
        with the entry's net effect on the account in the account's normal direction."""
        account = self._accounts.get(account_id)
        out: list[LedgerLine] = []
        for posted in self._journal:
            net = Money.of_minor(0, account.currency)
            touches = False
            for posting in posted.entry.postings:
                if posting.account_id != account_id:
                    continue
                touches = True
                net = net.plus(self._signed(posting, account))
            if touches:
                out.append(LedgerLine(posted.id, net, posted.entry.date))
        return out

    @staticmethod
    def parse_date(text: str) -> _date:
        try:
            return _date.fromisoformat(text)
        except (TypeError, ValueError):
            raise LedgerError(f"not an ISO-8601 date: {text}") from None

    @staticmethod
    def within_window(a: str, b: str, window_days: int) -> bool:
        return abs((Ledger.parse_date(b) - Ledger.parse_date(a)).days) <= window_days

    def reconcile(self, account_id: str, statement: Sequence[StatementLine], window_days: int) -> ReconciliationReport:
        """Match the account's journal lines against an external statement. Pass 1 matches by reference
        (equal amounts match; unequal amounts are AMOUNT_MISMATCH; a repeated statement reference is a
        DUPLICATE). Pass 2 matches what is left by equal amount within `window_days` of each other,
        first unmatched ledger line in journal order wins. Whatever remains is missing on one side.
        The report's totals tie out by construction: every line lands in exactly one match or one break."""
        account = self._accounts.get(account_id)
        if window_days < 0:
            raise ValueError("window_days must be >= 0")
        currency = account.currency
        zero = Money.of_minor(0, currency)

        ledger_lines = self.lines(account_id)
        ledger_by_ref = {line.ref: line for line in ledger_lines}
        ledger_total = Money.sum(currency, *(line.amount for line in ledger_lines))
        statement_total = zero
        for line in statement:
            if line.amount.currency != currency:
                raise CurrencyMismatchError(currency, line.amount.currency)
            self.parse_date(line.date)
            statement_total = statement_total.plus(line.amount)

        matches: list[ReconciliationMatch] = []
        breaks: list[ReconciliationBreak] = []
        consumed_ledger: set[str] = set()
        seen_statement_refs: set[str] = set()
        unmatched_statement: list[StatementLine] = []

        # pass 1: by reference
        for line in statement:
            if line.ref in seen_statement_refs:
                breaks.append(ReconciliationBreak(BreakType.DUPLICATE, line.ref, zero, line.amount))
                continue
            seen_statement_refs.add(line.ref)
            ours = ledger_by_ref.get(line.ref)
            if ours is None:
                unmatched_statement.append(line)
            elif ours.amount == line.amount:
                matches.append(ReconciliationMatch(ours.ref, line.ref, line.amount, MatchedBy.REF))
                consumed_ledger.add(ours.ref)
            else:
                breaks.append(ReconciliationBreak(BreakType.AMOUNT_MISMATCH, line.ref, ours.amount, line.amount))
                consumed_ledger.add(ours.ref)

        # pass 2: by amount within the date window
        for line in unmatched_statement:
            found = None
            for ours in ledger_lines:
                if ours.ref in consumed_ledger:
                    continue
                if ours.amount == line.amount and self.within_window(ours.date, line.date, window_days):
                    found = ours
                    break
            if found is None:
                breaks.append(ReconciliationBreak(BreakType.MISSING_IN_LEDGER, line.ref, zero, line.amount))
            else:
                matches.append(ReconciliationMatch(found.ref, line.ref, line.amount, MatchedBy.AMOUNT_AND_DATE))
                consumed_ledger.add(found.ref)

        # whatever the ledger still holds, the statement never showed
        for ours in ledger_lines:
            if ours.ref not in consumed_ledger:
                breaks.append(ReconciliationBreak(BreakType.MISSING_IN_STATEMENT, ours.ref, ours.amount, zero))
        return ReconciliationReport(account_id, currency, tuple(matches), tuple(breaks), ledger_total, statement_total)


# ---------------------------------------------------------------- demo


def main() -> None:
    print("== Ledger Engine: a double-entry ledger that reconciles itself ==")

    # Stage 1: a chart of accounts
    chart = AccountRegistry()
    chart.open("bank", AccountType.ASSET, USD)
    chart.open("receivable", AccountType.ASSET, USD)
    chart.open("payable:sellers", AccountType.LIABILITY, USD)
    chart.open("revenue:fees", AccountType.REVENUE, USD)
    for account in chart.all():
        print(f"  {account.id:<16} {account.type.name:<9} {account.currency.code}  grows on {account.normal_side().name}")

    # Stage 2: post balanced entries; the ledger refuses anything else
    ledger = Ledger(chart, 2)
    sale = Money.parse("100.00", USD)
    fee = sale.times(29, 1000, Rounding.HALF_EVEN).plus(Money.of_minor(30, USD))
    ledger.post(JournalEntry.of("sale-1", "2026-10-01",
                                Posting("bank", Side.DEBIT, sale),
                                Posting("payable:sellers", Side.CREDIT, sale.minus(fee)),
                                Posting("revenue:fees", Side.CREDIT, fee)))
    ledger.post(JournalEntry.of("payout-1", "2026-10-03",
                                Posting("payable:sellers", Side.DEBIT, Money.parse("50.00", USD)),
                                Posting("bank", Side.CREDIT, Money.parse("50.00", USD))))
    ledger.post(JournalEntry.of("sale-2", "2026-10-04",
                                Posting("bank", Side.DEBIT, Money.parse("20.00", USD)),
                                Posting("revenue:fees", Side.CREDIT, Money.parse("20.00", USD))))
    try:
        ledger.post(JournalEntry.of("bad", "2026-10-04",
                                    Posting("bank", Side.DEBIT, Money.parse("1.00", USD)),
                                    Posting("revenue:fees", Side.CREDIT, Money.parse("0.99", USD))))
    except LedgerError as exc:
        print(f"\nrejected: {exc}")
    print(f"journal has {ledger.size()} entries; fee on 100.00 = {fee}")

    # Stage 3: balances are derived, the trial balance ties, reversals mirror
    print(f"\nbalances: bank {ledger.balance('bank')}, sellers {ledger.balance('payable:sellers')}, "
          f"fees {ledger.balance('revenue:fees')}")
    tb = ledger.trial_balance()[0]
    print(f"trial balance {tb.currency.code}: debits {tb.total_debits} credits {tb.total_credits} difference {tb.difference()}")
    ledger.reverse("sale-2")
    print(f"after reversing sale-2: bank {ledger.balance('bank')}, fees {ledger.balance('revenue:fees')}, "
          f"snapshots at {[s.sequence for s in ledger.snapshots()]}")

    # Stage 4: the processor's statement does not quite agree
    statement = [
        StatementLine("sale-1", Money.parse("100.00", USD), "2026-10-01"),
        StatementLine("PAYOUT-0001", Money.parse("-50.00", USD), "2026-10-04"),
        StatementLine("sale-2", Money.parse("20.00", USD), "2026-10-04"),
        StatementLine("sale-2", Money.parse("20.00", USD), "2026-10-04"),
        StatementLine("refund-9", Money.parse("-5.00", USD), "2026-10-05"),
    ]
    report = ledger.reconcile("bank", statement, 3)
    print(f"\nreconcile bank: {len(report.matches)} matched, {len(report.breaks)} breaks")
    for m in report.matches:
        print(f"  match  {m.ledger_ref} ~ {m.statement_ref} {m.amount} by {m.by.name}")
    for b in report.breaks:
        print(f"  break  {b.type.name} {b.ref} ledger {b.ledger_amount} statement {b.statement_amount}")
    print(f"ledger total {report.ledger_total} = matched {report.matched_total()} + breaks {report.ledger_break_total()}; "
          f"statement total {report.statement_total} = matched + breaks {report.statement_break_total()}; "
          f"ties out: {report.ties_out()}")


if __name__ == "__main__":
    main()
