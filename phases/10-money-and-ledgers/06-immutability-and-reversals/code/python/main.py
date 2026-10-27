"""Lesson: phases/10-money-and-ledgers/06-immutability-and-reversals/docs/en.md
Immutability, Reversals, and Corrections
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: Pacioli (1494) on reversing entries; FIPS 180-4 (SHA-256) for the hash chain; Money from Lesson 01.
Python 3.11+, standard library only. Same design as the Java version; hashes are byte-for-byte identical.
"""

from __future__ import annotations

import hashlib
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
# Entries that can never change, and the three ways to undo one: full reversal, partial reversal,
# and correction (reverse + replace). Every undo is a new entry linked to the one it undoes.
# ---------------------------------------------------------------------------------------------


class Side(Enum):
    DEBIT = "debit"
    CREDIT = "credit"

    def opposite(self) -> "Side":
        return Side.CREDIT if self is Side.DEBIT else Side.DEBIT


class AccountType(Enum):
    ASSET = Side.DEBIT
    EXPENSE = Side.DEBIT
    LIABILITY = Side.CREDIT
    EQUITY = Side.CREDIT
    REVENUE = Side.CREDIT

    def normal_balance(self) -> Side:
        return self.value


class EntryKind(Enum):
    """ORIGINAL records a business event; REVERSAL undoes (part of) one; CORRECTION replaces one that was reversed."""

    ORIGINAL = "original"
    REVERSAL = "reversal"
    CORRECTION = "correction"


class LedgerError(ValueError):
    pass


class IdempotencyConflictError(LedgerError):
    def __init__(self, id: str) -> None:
        super().__init__(f"entry id {id} already posted with different content")


class AlreadyReversedError(LedgerError):
    def __init__(self, id: str) -> None:
        super().__init__(f"entry {id} is already fully reversed")


class ExcessiveReversalError(LedgerError):
    def __init__(self, id: str, requested: Money, remaining: Money) -> None:
        super().__init__(f"cannot reverse {requested} of {id}: only {remaining} remains")


@dataclass(frozen=True)
class Account:
    id: str
    name: str
    type: AccountType
    currency: Currency


@dataclass(frozen=True)
class Posting:
    account_id: str
    side: Side
    amount: Money

    def __post_init__(self) -> None:
        if not isinstance(self.side, Side):
            raise TypeError("side")
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

    def canonical(self) -> str:
        return f"{self.account_id}:{self.side.name}:{self.amount.minor_units}:{self.amount.currency.code}"


def _java_str(value: Optional[str]) -> str:
    """Java string concatenation renders null as "null"; the hash chain depends on the same bytes."""
    return "null" if value is None else value


@dataclass(frozen=True)
class JournalEntry:
    """Immutable and balanced by construction. A REVERSAL or CORRECTION must name the entry it relates to and a reason:
    an undo without a link or a why is indistinguishable from fraud in an audit."""

    id: str
    description: str
    postings: Tuple[Posting, ...]
    kind: EntryKind
    relates_to: Optional[str]
    reason: Optional[str]

    def __init__(self, id: str, description: str, postings: Iterable[Posting], kind: EntryKind,
                 relates_to: Optional[str], reason: Optional[str]) -> None:
        if id is None or not id.strip():
            raise LedgerError("entry id required")
        if description is None or not isinstance(kind, EntryKind):
            raise TypeError("description and kind are required")
        postings = tuple(postings)
        if len(postings) < 2:
            raise LedgerError(f"entry {id} needs at least two postings")
        if kind is EntryKind.ORIGINAL and relates_to is not None:
            raise LedgerError("an original entry relates to nothing")
        if kind is not EntryKind.ORIGINAL and (relates_to is None or reason is None or not reason.strip()):
            raise LedgerError(f"{kind.name} {id} must name the entry it relates to and a reason")
        net: Dict[Currency, int] = {}
        for p in postings:
            net[p.amount.currency] = net.get(p.amount.currency, 0) + p.signed_minor()
        for currency, value in net.items():
            if value != 0:
                raise LedgerError(f"entry {id} unbalanced: debits minus credits in {currency.code} = "
                                  f"{Money.of_minor(value, currency)}")
        object.__setattr__(self, "id", id)
        object.__setattr__(self, "description", description)
        object.__setattr__(self, "postings", postings)
        object.__setattr__(self, "kind", kind)
        object.__setattr__(self, "relates_to", relates_to)
        object.__setattr__(self, "reason", reason)

    @staticmethod
    def original(id: str, description: str, postings: Iterable[Posting]) -> "JournalEntry":
        return JournalEntry(id, description, postings, EntryKind.ORIGINAL, None, None)

    def currency(self) -> Currency:
        return self.postings[0].amount.currency

    def debit_total(self) -> int:
        """Sum of the debit legs: the "size" of the entry, used to bound partial reversals."""
        return sum(p.amount.minor_units for p in self.postings if p.side is Side.DEBIT)

    def canonical(self) -> str:
        head = f"{self.id}|{self.kind.name}|{_java_str(self.relates_to)}|{_java_str(self.reason)}|{self.description}"
        return head + "".join("|" + p.canonical() for p in self.postings)


GENESIS = "0" * 64


@dataclass(frozen=True)
class JournalRecord:
    """A journal slot: the entry, its position, and a SHA-256 link to the previous slot. Tampering breaks the chain."""

    seq: int
    entry: JournalEntry
    prev_hash: str
    hash: str

    GENESIS = GENESIS

    @staticmethod
    def hash_of(seq: int, entry: JournalEntry, prev_hash: str) -> str:
        return hashlib.sha256(f"{prev_hash}|{seq}|{entry.canonical()}".encode("utf-8")).hexdigest()


class Ledger:
    """Append-only journal. The public write methods are post (originals), reverse, reverse_partially, and correct;
    none of them modifies or removes an existing entry. Every write is idempotent on its entry id."""

    def __init__(self) -> None:
        self._accounts: Dict[str, Account] = {}
        self._journal: List[JournalRecord] = []
        self._index_by_id: Dict[str, int] = {}
        self._net_debits: Dict[str, int] = {}
        # For each reversible entry: how much of each posting (by index) has been reversed so far, in minor units.
        self._reversed_so_far: Dict[str, List[int]] = {}
        self._children: Dict[str, List[str]] = {}

    def open_account(self, id: str, name: str, type: AccountType, currency: Currency) -> Account:
        if id in self._accounts:
            raise LedgerError(f"account exists: {id}")
        account = Account(id, name, type, currency)
        self._accounts[id] = account
        self._net_debits[id] = 0
        return account

    def account(self, id: str) -> Account:
        a = self._accounts.get(id)
        if a is None:
            raise LedgerError(f"unknown account: {id}")
        return a

    def balance(self, account_id: str) -> Money:
        a = self.account(account_id)
        net = self._net_debits[account_id]
        return Money.of_minor(net if a.type.normal_balance() is Side.DEBIT else -net, a.currency)

    def entry(self, id: str) -> JournalEntry:
        i = self._index_by_id.get(id)
        if i is None:
            raise LedgerError(f"unknown entry: {id}")
        return self._journal[i].entry

    def records(self) -> Tuple[JournalRecord, ...]:
        """An immutable view: a tuple cannot be appended to, cleared, or have a record removed."""
        return tuple(self._journal)

    def size(self) -> int:
        return len(self._journal)

    def post(self, entry: JournalEntry) -> bool:
        """Post a new business event. Same id + same content is a no-op (returns False); same id + different content is a conflict."""
        if entry.kind is not EntryKind.ORIGINAL:
            raise LedgerError(f"use reverse, reverse_partially, or correct for {entry.kind.name}")
        if self._replayed(entry):
            return False
        self._validate_accounts(entry)
        self._append(entry)
        return True

    def reverse(self, original_id: str, reversal_id: str, reason: str) -> JournalEntry:
        """Reverse whatever remains of an entry. Idempotent on reversal_id."""
        existing = self._existing_child(reversal_id, original_id, EntryKind.REVERSAL)
        if existing is not None:
            return existing
        original = self._reversible(original_id)
        done = self._reversed_so_far[original_id]
        flipped: List[Posting] = []
        for i, p in enumerate(original.postings):
            remaining = p.amount.minor_units - done[i]
            if remaining > 0:
                flipped.append(Posting(p.account_id, p.side.opposite(), Money.of_minor(remaining, p.amount.currency)))
        if not flipped:
            raise AlreadyReversedError(original_id)
        reversal = JournalEntry(reversal_id, f"reversal of {original_id}", flipped, EntryKind.REVERSAL, original_id, reason)
        self._append(reversal)
        return reversal

    def reverse_partially(self, original_id: str, reversal_id: str, amount: Money, reason: str) -> JournalEntry:
        """Reverse amount of an entry (a partial refund). Each side's legs are allocated in proportion to what
        remains unreversed on each leg (largest remainder, Lesson 01), so no leg is ever over-reversed and the
        reversal is balanced. Idempotent on reversal_id."""
        existing = self._existing_child(reversal_id, original_id, EntryKind.REVERSAL)
        if existing is not None:
            if existing.debit_total() != amount.minor_units:
                raise IdempotencyConflictError(reversal_id)
            return existing
        original = self._reversible(original_id)
        if amount.currency != original.currency():
            raise CurrencyMismatchError(amount.currency, original.currency())
        if not amount.is_positive():
            raise LedgerError("partial reversal amount must be positive")
        done = self._reversed_so_far[original_id]
        remaining_debits = sum(p.amount.minor_units - done[i] for i, p in enumerate(original.postings) if p.side is Side.DEBIT)
        if remaining_debits == 0:
            raise AlreadyReversedError(original_id)
        if amount.minor_units > remaining_debits:
            raise ExcessiveReversalError(original_id, amount, Money.of_minor(remaining_debits, amount.currency))
        flipped: List[Posting] = []
        for side in Side:
            legs = [i for i, p in enumerate(original.postings) if p.side is side]
            ratios = [original.postings[i].amount.minor_units - done[i] for i in legs]
            parts = amount.allocate(*ratios)
            for k, i in enumerate(legs):
                if parts[k].is_zero():
                    continue
                p = original.postings[i]
                flipped.append(Posting(p.account_id, side.opposite(), parts[k]))
        reversal = JournalEntry(reversal_id, f"partial reversal of {original_id}", flipped, EntryKind.REVERSAL, original_id, reason)
        self._append(reversal)
        return reversal

    def correct(self, original_id: str, reversal_id: str, replacement_id: str,
                replacement_postings: Iterable[Posting], reason: str) -> JournalEntry:
        """Correct an entry that was wrong when posted: fully reverse it and post the replacement, linked to the original,
        as one atomic step. Both are validated before either is appended. Idempotent on (reversal_id, replacement_id)."""
        replacement = JournalEntry(replacement_id, f"correction of {original_id}", replacement_postings,
                                   EntryKind.CORRECTION, original_id, reason)
        prior_reversal = self._existing_child(reversal_id, original_id, EntryKind.REVERSAL)
        if prior_reversal is not None and replacement_id in self._index_by_id:
            if self.entry(replacement_id) != replacement:
                raise IdempotencyConflictError(replacement_id)
            return replacement
        if prior_reversal is not None or replacement_id in self._index_by_id:
            raise LedgerError(f"correction of {original_id} was half-applied; ids already in use")
        original = self._reversible(original_id)
        done = self._reversed_so_far[original_id]
        if any(v != 0 for v in done):
            raise LedgerError(f"entry {original_id} already has reversals; correct the remaining amount explicitly")
        self._validate_accounts(replacement)
        flipped = [Posting(p.account_id, p.side.opposite(), p.amount) for p in original.postings]
        reversal = JournalEntry(reversal_id, f"reversal of {original_id} for correction", flipped,
                                EntryKind.REVERSAL, original_id, reason)
        self._append(reversal)
        self._append(replacement)
        return replacement

    def is_fully_reversed(self, id: str) -> bool:
        e = self.entry(id)
        done = self._reversed_so_far.get(id)
        if done is None:
            return False
        return all(done[i] == p.amount.minor_units for i, p in enumerate(e.postings))

    def remaining(self, id: str) -> Money:
        """Amount of the entry not yet reversed (debit side), e.g. what can still be refunded."""
        e = self.entry(id)
        done = self._reversed_so_far.get(id)
        rem = sum(p.amount.minor_units - (0 if done is None else done[i])
                  for i, p in enumerate(e.postings) if p.side is Side.DEBIT)
        return Money.of_minor(rem, e.currency())

    def history(self, id: str) -> List[JournalRecord]:
        """The entry and everything that relates to it, transitively, in journal order: the audit story of one event."""
        ids: List[str] = []
        self._collect(id, ids)
        out = [self._journal[self._index_by_id[x]] for x in ids]
        out.sort(key=lambda r: r.seq)
        return out

    def audit_view(self) -> List[str]:
        """One line per record: seq, kind, id, link, reason, net effect. What an auditor reads."""
        lines = []
        for r in self._journal:
            e = r.entry
            link = "" if e.relates_to is None else f"-> {e.relates_to}"
            reason = "" if e.reason is None else e.reason
            debits = str(Money.of_minor(e.debit_total(), e.currency()))
            lines.append(f"#{r.seq} {e.kind.name:<10} {e.id:<9} {link:<13} {reason:<31} debits {debits:<11} hash {r.hash[:12]}")
        return lines

    @staticmethod
    def verify_chain(records: Iterable[JournalRecord]) -> int:
        """Recompute every hash from the genesis value. Returns the seq of the first broken record, or -1 if intact."""
        prev = GENESIS
        for i, r in enumerate(records):
            if r.seq != i + 1 or r.prev_hash != prev or r.hash != JournalRecord.hash_of(r.seq, r.entry, prev):
                return i + 1
            prev = r.hash
        return -1

    # ---- internals ----

    def _replayed(self, entry: JournalEntry) -> bool:
        i = self._index_by_id.get(entry.id)
        if i is None:
            return False
        if self._journal[i].entry == entry:
            return True
        raise IdempotencyConflictError(entry.id)

    def _existing_child(self, child_id: str, original_id: str, kind: EntryKind) -> Optional[JournalEntry]:
        i = self._index_by_id.get(child_id)
        if i is None:
            return None
        e = self._journal[i].entry
        if e.kind is not kind or e.relates_to != original_id:
            raise IdempotencyConflictError(child_id)
        return e

    def _reversible(self, id: str) -> JournalEntry:
        e = self.entry(id)
        if e.kind is EntryKind.REVERSAL:
            raise LedgerError(f"entry {id} is a reversal; post a new original instead of reversing a reversal")
        return e

    def _validate_accounts(self, entry: JournalEntry) -> None:
        for p in entry.postings:
            if self.account(p.account_id).currency != p.amount.currency:
                raise LedgerError(f"entry {entry.id}: account {p.account_id} is not {p.amount.currency.code}")

    def _append(self, entry: JournalEntry) -> None:
        seq = len(self._journal) + 1
        prev = GENESIS if not self._journal else self._journal[-1].hash
        self._journal.append(JournalRecord(seq, entry, prev, JournalRecord.hash_of(seq, entry, prev)))
        self._index_by_id[entry.id] = seq - 1
        for p in entry.postings:
            self._net_debits[p.account_id] = self._net_debits.get(p.account_id, 0) + p.signed_minor()
        if entry.kind is not EntryKind.REVERSAL:
            self._reversed_so_far[entry.id] = [0] * len(entry.postings)
        if entry.relates_to is not None:
            self._children.setdefault(entry.relates_to, []).append(entry.id)
            if entry.kind is EntryKind.REVERSAL:
                self._apply_reversal(entry)

    def _apply_reversal(self, reversal: JournalEntry) -> None:
        """Attribute each reversal leg to the original's legs (same account, opposite side), never exceeding a leg."""
        original = self.entry(reversal.relates_to)
        done = self._reversed_so_far[original.id]
        for r in reversal.postings:
            left = r.amount.minor_units
            for i, o in enumerate(original.postings):
                if left <= 0:
                    break
                if o.account_id != r.account_id or o.side is not r.side.opposite():
                    continue
                take = min(left, o.amount.minor_units - done[i])
                done[i] += take
                left -= take
            if left != 0:
                raise RuntimeError(f"reversal {reversal.id} exceeds original {original.id}")

    def _collect(self, id: str, out: List[str]) -> None:
        out.append(id)
        for child in self._children.get(id, []):
            self._collect(child, out)


def print_entry(e: JournalEntry) -> None:
    link = "" if e.relates_to is None else f" -> {e.relates_to} ({e.reason})"
    print(f"{e.kind.name} {e.id}{link}")
    for p in e.postings:
        print(f"{'  Dr ' if p.side is Side.DEBIT else '      Cr '}{p.account_id}  {p.amount}")


def main() -> None:
    ledger = Ledger()
    ledger.open_account("1200", "Processor Receivable", AccountType.ASSET, USD)
    ledger.open_account("2100", "Merchant Payable", AccountType.LIABILITY, USD)
    ledger.open_account("4100", "Fee Revenue", AccountType.REVENUE, USD)

    print("== A 120.00 USD sale with a 2.9% fee ==")
    gross = Money.parse("120.00", USD)
    fee = gross.times(29, 1000, Rounding.HALF_EVEN)
    sale = JournalEntry.original("pay-100", "card payment captured", [
        Posting.debit("1200", gross), Posting.credit("2100", gross.minus(fee)), Posting.credit("4100", fee)])
    ledger.post(sale)
    print_entry(sale)

    print("\n== Partial refund of 50.00: fee and payable reversed in proportion ==")
    r1 = ledger.reverse_partially("pay-100", "ref-100-1", Money.parse("50.00", USD), "customer returned 1 of 2 items")
    print_entry(r1)
    again = ledger.reverse_partially("pay-100", "ref-100-1", Money.parse("50.00", USD), "retry of the same refund")
    print(f"retry of ref-100-1 returns the same entry: {str(again is r1).lower()}, journal size {ledger.size()}")
    print(f"remaining refundable: {ledger.remaining('pay-100')}")
    try:
        ledger.reverse_partially("pay-100", "ref-100-2", Money.parse("80.00", USD), "refund too large")
    except ExcessiveReversalError as e:
        print(f"rejected: {e}")

    print("\n== Full reversal of what remains (chargeback) ==")
    r2 = ledger.reverse("pay-100", "cb-100", "chargeback 10.4 fraud")
    print_entry(r2)
    print(f"fully reversed: {str(ledger.is_fully_reversed('pay-100')).lower()}; balances 1200 {ledger.balance('1200')}"
          f", 2100 {ledger.balance('2100')}, 4100 {ledger.balance('4100')}")
    try:
        ledger.reverse("pay-100", "cb-100b", "duplicate chargeback notice")
    except AlreadyReversedError as e:
        print(f"rejected: {e}")

    print("\n== Correction: a fee booked at 3.9% instead of 2.9% ==")
    gross2 = Money.parse("80.00", USD)
    wrong_fee = gross2.times(39, 1000, Rounding.HALF_EVEN)
    ledger.post(JournalEntry.original("pay-200", "card payment captured", [
        Posting.debit("1200", gross2), Posting.credit("2100", gross2.minus(wrong_fee)), Posting.credit("4100", wrong_fee)]))
    right_fee = gross2.times(29, 1000, Rounding.HALF_EVEN)
    fixed = ledger.correct("pay-200", "rev-200", "pay-200c", [
        Posting.debit("1200", gross2), Posting.credit("2100", gross2.minus(right_fee)), Posting.credit("4100", right_fee)],
        "fee rate config error INC-4471")
    print_entry(ledger.entry("rev-200"))
    print_entry(fixed)
    print(f"fee revenue now {ledger.balance('4100')} (2.32 on pay-200, 0.00 net on pay-100)")

    print("\n== Audit view: nothing was edited, everything is linked ==")
    for line in ledger.audit_view():
        print(line)
    print(f"history of pay-100: [{', '.join(r.entry.id for r in ledger.history('pay-100'))}]")
    print(f"hash chain intact: {str(Ledger.verify_chain(ledger.records()) == -1).lower()}")

    tampered = list(ledger.records())
    victim = tampered[2]
    edited = JournalEntry.original("cb-100", "quietly edited", victim.entry.postings)
    tampered[2] = JournalRecord(victim.seq, edited, victim.prev_hash, victim.hash)
    print(f"after editing record #3 in a copy, first broken seq = {Ledger.verify_chain(tampered)}")


if __name__ == "__main__":
    main()
