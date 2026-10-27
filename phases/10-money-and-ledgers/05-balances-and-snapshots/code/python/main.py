"""Lesson: phases/10-money-and-ledgers/05-balances-and-snapshots/docs/en.md
Balances, Running Totals, and Snapshots
Run:  python3 main.py          (add --timing to print wall-clock measurements)
Test: python3 -m unittest -v
Sources: Fowler, "Event Sourcing" and "Snapshot" patterns; TigerBeetle account balances; Money from Lesson 01.
Python 3.11+, standard library only. Same design as the Java version, including java.util.Random's LCG.
"""

from __future__ import annotations

import sys
import time
from array import array
from dataclasses import dataclass
from enum import Enum
from functools import total_ordering
from typing import Dict, List, Mapping

# ---------------------------------------------------------------------------------------------
# Money (copied from Phase 10, Lesson 01). Integer minor units + ISO 4217 currency. Never a float.
# ---------------------------------------------------------------------------------------------


@dataclass(frozen=True, slots=True)
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

    def __hash__(self) -> int:  # the code identifies the currency; a million dict lookups should not hash two fields
        return hash(self.code)


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
@dataclass(frozen=True, slots=True)
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
# The ledger (shape from Lesson 03, trimmed to what balances need) with three ways to read a balance.
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


@dataclass(frozen=True, slots=True)
class Account:
    id: str
    name: str
    type: AccountType
    currency: Currency


@dataclass(frozen=True, slots=True)
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
        """Debits count positive, credits negative: the "net debit" convention used by every fold below."""
        return self.amount.minor_units if self.side is Side.DEBIT else -self.amount.minor_units


@dataclass(frozen=True, slots=True)
class JournalEntry:
    """A balanced entry with an effective timestamp (epoch seconds, injected by the caller, never read from a clock)."""

    id: str
    effective_at: int
    postings: tuple

    def __init__(self, id: str, effective_at: int, postings) -> None:
        postings = tuple(postings)
        if len(postings) < 2:
            raise LedgerError(f"entry {id} needs at least two postings")
        net: Dict[Currency, int] = {}
        for p in postings:
            net[p.amount.currency] = net.get(p.amount.currency, 0) + p.signed_minor()
        for currency, value in net.items():
            if value != 0:
                raise LedgerError(f"entry {id} unbalanced in {currency.code}")
        object.__setattr__(self, "id", id)
        object.__setattr__(self, "effective_at", effective_at)
        object.__setattr__(self, "postings", postings)


@dataclass(frozen=True, slots=True)
class Snapshot:
    """A point-in-time copy of every account's net debit, valid after the entry at journal position seq."""

    seq: int
    effective_at: int
    net_debits: Mapping[str, int]

    def __post_init__(self) -> None:
        object.__setattr__(self, "net_debits", dict(self.net_debits))  # the copy IS the snapshot

    def net_debit(self, account_id: str) -> int:
        return self.net_debits.get(account_id, 0)


@dataclass(frozen=True, slots=True)
class Reading:
    """A balance reading plus how many postings had to be examined to produce it: the cost, made visible."""

    balance: Money
    postings_scanned: int


class LongList:
    """Growable array of 64-bit integers (array('q')): a million boxed ints would cost 28 bytes each for nothing."""

    def __init__(self) -> None:
        self._data = array("q")

    def add(self, value: int) -> None:
        self._data.append(value)

    def get(self, index: int) -> int:
        return self._data[index]

    def size(self) -> int:
        return len(self._data)


LONG_MIN = -(1 << 63)


class Ledger:
    """Append-only journal with:
      1. a running balance per account (O(1) current balance),
      2. a running-balance column per account (balance after each of its postings; O(log n) as-of reads),
      3. periodic snapshots of every account every snapshot_interval entries (bounded replay).
    Every read path must agree with fold_net_debit, the slow, obviously-correct full scan."""

    def __init__(self, snapshot_interval: int) -> None:
        if snapshot_interval < 1:
            raise ValueError("snapshot_interval must be >= 1")
        self._snapshot_interval = snapshot_interval
        self._accounts: Dict[str, Account] = {}
        self._journal: List[JournalEntry] = []          # journal[i] has seq i + 1
        self._running: Dict[str, int] = {}              # current net debit per account
        self._column_seq: Dict[str, LongList] = {}      # per account: seq of each posting
        self._column_after: Dict[str, LongList] = {}    # per account: net debit after it
        self._snapshots: List[Snapshot] = [Snapshot(0, LONG_MIN, {})]  # ascending by seq; seq 0 is empty

    def open_account(self, id: str, name: str, type: AccountType, currency: Currency) -> Account:
        if id in self._accounts:
            raise LedgerError(f"account exists: {id}")
        account = Account(id, name, type, currency)
        self._accounts[id] = account
        self._running[id] = 0
        self._column_seq[id] = LongList()
        self._column_after[id] = LongList()
        return account

    def account(self, id: str) -> Account:
        a = self._accounts.get(id)
        if a is None:
            raise LedgerError(f"unknown account: {id}")
        return a

    def head(self) -> int:
        return len(self._journal)

    def snapshots(self) -> List[Snapshot]:
        return list(self._snapshots)

    def post(self, entry: JournalEntry) -> int:
        """Validate everything, then mutate everything. Returns the entry's journal position (seq)."""
        for p in entry.postings:
            if self.account(p.account_id).currency != p.amount.currency:
                raise LedgerError(f"entry {entry.id}: currency does not match account {p.account_id}")
        if self._journal and entry.effective_at < self._journal[-1].effective_at:
            raise LedgerError(f"entry {entry.id} is backdated; post a correction at the current time instead")
        self._journal.append(entry)
        seq = len(self._journal)
        running = self._running
        for p in entry.postings:
            after = running[p.account_id] + p.signed_minor()
            running[p.account_id] = after
            self._column_seq[p.account_id].add(seq)
            self._column_after[p.account_id].add(after)
        if seq % self._snapshot_interval == 0:
            self.take_snapshot()
        return seq

    def take_snapshot(self) -> Snapshot:
        """Copy every running total. O(accounts). Called automatically every snapshot_interval entries."""
        seq = self.head()
        last = self._snapshots[-1]
        if last.seq == seq:
            return last
        s = Snapshot(seq, self._journal[seq - 1].effective_at, self._running)
        self._snapshots.append(s)
        return s

    # ---- read path 0: the definition. Slow, obviously correct, used to check every other path. ----

    def fold_net_debit(self, account_id: str, seq: int) -> Reading:
        """Net debit of an account after the entry at seq, by summing every posting from the start. O(postings)."""
        a = self.account(account_id)
        acc = 0
        scanned = 0
        for i in range(seq):
            for p in self._journal[i].postings:
                scanned += 1
                if p.account_id == account_id:
                    acc += p.signed_minor()
        return Reading(Money.of_minor(acc, a.currency), scanned)

    # ---- read path 1: the running total. O(1), but only for "now". ----

    def current_net_debit(self, account_id: str) -> Money:
        return Money.of_minor(self._running[account_id], self.account(account_id).currency)

    def balance(self, account_id: str) -> Money:
        """Balance in the account's normal direction (positive = what you expect for that account type)."""
        return self._to_normal(self.account(account_id), self._running[account_id])

    # ---- read path 2: nearest snapshot at or before seq, then replay the delta. O(snapshot_interval). ----

    def snapshot_net_debit(self, account_id: str, seq: int) -> Reading:
        a = self.account(account_id)
        self._require_seq(seq)
        base = self.nearest_snapshot(seq)
        acc = base.net_debit(account_id)
        scanned = 0
        for s in range(base.seq, seq):
            for p in self._journal[s].postings:
                scanned += 1
                if p.account_id == account_id:
                    acc += p.signed_minor()
        return Reading(Money.of_minor(acc, a.currency), scanned)

    def nearest_snapshot(self, seq: int) -> Snapshot:
        """Binary search: the last snapshot whose seq <= target. Snapshot 0 (all zero) always qualifies."""
        lo, hi = 0, len(self._snapshots) - 1
        while lo < hi:
            mid = (lo + hi + 1) >> 1
            if self._snapshots[mid].seq <= seq:
                lo = mid
            else:
                hi = mid - 1
        return self._snapshots[lo]

    # ---- read path 3: the per-account running-balance column. O(log postings of that account). ----

    def column_net_debit(self, account_id: str, seq: int) -> Reading:
        a = self.account(account_id)
        self._require_seq(seq)
        seqs, after = self._column_seq[account_id], self._column_after[account_id]
        lo, hi, found, probes = 0, seqs.size() - 1, -1, 0
        while lo <= hi:  # last posting of this account with seq <= target (what bisect_right - 1 would return)
            mid = (lo + hi) >> 1
            probes += 1
            if seqs.get(mid) <= seq:
                found = mid
                lo = mid + 1
            else:
                hi = mid - 1
        return Reading(Money.of_minor(0 if found < 0 else after.get(found), a.currency), probes)

    # ---- time-based queries: translate a timestamp into a journal position, then read. ----

    def seq_as_of(self, timestamp: int) -> int:
        """The journal position of the last entry with effective_at <= t; 0 if none. Binary search: timestamps never decrease."""
        lo, hi = 0, len(self._journal)  # answer in [0, size]
        while lo < hi:
            mid = (lo + hi + 1) >> 1
            if self._journal[mid - 1].effective_at <= timestamp:
                lo = mid
            else:
                hi = mid - 1
        return lo

    def balance_as_of(self, account_id: str, timestamp: int) -> Money:
        a = self.account(account_id)
        return self._to_normal(a, self.snapshot_net_debit(account_id, self.seq_as_of(timestamp)).balance.minor_units)

    def verify(self, s: Snapshot) -> bool:
        """Check a snapshot against the definition: every account's value must equal the full fold at its seq."""
        for id in self._accounts:
            if s.net_debit(id) != self.fold_net_debit(id, s.seq).balance.minor_units:
                return False
        for id in s.net_debits:
            if id not in self._accounts:
                return False
        return True

    def _require_seq(self, seq: int) -> None:
        if seq < 0 or seq > self.head():
            raise LedgerError(f"seq {seq} outside journal 0..{self.head()}")

    @staticmethod
    def _to_normal(a: Account, net_debit: int) -> Money:
        signed = net_debit if a.type.normal_balance() is Side.DEBIT else -net_debit
        return Money.of_minor(signed, a.currency)


class JavaRandom:
    """java.util.Random's 48-bit LCG, so a seed produces the same stream here as in the Java version."""

    MULTIPLIER = 0x5DEECE66D
    ADDEND = 0xB
    MASK = (1 << 48) - 1

    def __init__(self, seed: int) -> None:
        self._seed = (seed ^ self.MULTIPLIER) & self.MASK

    def next(self, bits: int) -> int:
        self._seed = (self._seed * self.MULTIPLIER + self.ADDEND) & self.MASK
        r = self._seed >> (48 - bits)
        return r - (1 << 32) if r >= (1 << 31) else r  # Java's (int) cast

    def next_int(self, bound: int) -> int:
        if bound <= 0:
            raise ValueError("bound must be positive")
        r = self.next(31)
        m = bound - 1
        if (bound & m) == 0:  # power of two
            return (bound * r) >> 31
        u = r
        r = u % bound
        while u - r + m >= (1 << 31):  # Java: the int sum overflowed to negative -> reject and redraw
            u = self.next(31)
            r = u % bound
        return r


class PaymentStream:
    """Seeded generator of two-leg card payments into many merchant accounts: deterministic, no wall clock."""

    def __init__(self, seed: int, merchants: int, start_epoch_seconds: int) -> None:
        self._random = JavaRandom(seed)
        self._merchants = merchants
        self._clock = start_epoch_seconds
        self._counter = 0

    @staticmethod
    def merchant_id(i: int) -> str:
        return f"m{i:04d}"

    def next(self) -> JournalEntry:
        self._clock += self._random.next_int(3)                       # 0..2 seconds between payments
        amount = 100 + self._random.next_int(50_000)                   # 1.00 .. 500.99 USD
        merchant = PaymentStream.merchant_id(self._random.next_int(self._merchants))
        m = Money.of_minor(amount, USD)
        entry = JournalEntry(f"p{self._counter}", self._clock, [Posting.debit("1200", m), Posting.credit(merchant, m)])
        self._counter += 1
        return entry


def main() -> None:
    timing = len(sys.argv) > 1 and sys.argv[1] == "--timing"

    print("== A small ledger, snapshot every 3 entries ==")
    ledger = Ledger(3)
    ledger.open_account("1200", "Processor Receivable", AccountType.ASSET, USD)
    ledger.open_account("2100", "Merchant Payable", AccountType.LIABILITY, USD)
    ledger.open_account("4100", "Fee Revenue", AccountType.REVENUE, USD)
    day = 1_767_225_600  # 2026-01-01T00:00:00Z
    sales = [10_000, 2_500, 4_000, 1_999, 7_500]
    for i, sale in enumerate(sales):
        gross = Money.of_minor(sale, USD)
        fee = gross.times(29, 1000, Rounding.HALF_EVEN)
        seq = ledger.post(JournalEntry(f"pay-{i + 1}", day + i * 3600, [
            Posting.debit("1200", gross), Posting.credit("2100", gross.minus(fee)), Posting.credit("4100", fee)]))
        print(f"seq {seq}  t=+{i}h  gross {str(gross):<11} fee {str(fee):<9}  payable now {ledger.balance('2100')}")
    print(f"snapshots at seq {[s.seq for s in ledger.snapshots()]}")
    for seq in range(ledger.head() + 1):
        fold = ledger.fold_net_debit("2100", seq)
        snap = ledger.snapshot_net_debit("2100", seq)
        col = ledger.column_net_debit("2100", seq)
        print(f"2100 net debit after seq {seq}: fold {str(fold.balance):<12} ({fold.postings_scanned:2d} postings)  "
              f"snapshot {str(snap.balance):<12} ({snap.postings_scanned})  column {str(col.balance):<12} ({col.postings_scanned} probes)")
    print(f"payable as of t=+2h30m: {ledger.balance_as_of('2100', day + 9000)} (seq {ledger.seq_as_of(day + 9000)})")
    print(f"payable as of before the first payment: {ledger.balance_as_of('2100', day - 1)}")
    try:
        ledger.post(JournalEntry("late", day, [
            Posting.debit("1200", Money.of_minor(100, USD)), Posting.credit("2100", Money.of_minor(100, USD))]))
    except LedgerError as e:
        print(f"rejected: {e}")

    print("\n== One million postings, 1,000 merchants, snapshot every 10,000 entries ==")
    big = Ledger(10_000)
    big.open_account("1200", "Processor Receivable", AccountType.ASSET, USD)
    merchants = 1_000
    for i in range(merchants):
        big.open_account(PaymentStream.merchant_id(i), f"Merchant {i}", AccountType.LIABILITY, USD)
    stream = PaymentStream(7, merchants, day)
    t0 = time.perf_counter_ns()
    for _ in range(500_000):
        big.post(stream.next())  # 2 postings each
    t1 = time.perf_counter_ns()
    target = 437_123
    merchant = PaymentStream.merchant_id(42)
    t2 = time.perf_counter_ns()
    fold = big.fold_net_debit(merchant, target)
    t3 = time.perf_counter_ns()
    snap = big.snapshot_net_debit(merchant, target)
    t4 = time.perf_counter_ns()
    col = big.column_net_debit(merchant, target)
    t5 = time.perf_counter_ns()
    print(f"entries={big.head()} snapshots={len(big.snapshots())}")
    print(f"merchant {merchant} as of seq {target}:")
    print(f"  full fold      {fold.balance}  postings scanned {fold.postings_scanned}")
    print(f"  snapshot+delta {snap.balance}  postings scanned {snap.postings_scanned}"
          f"  (from snapshot at seq {big.nearest_snapshot(target).seq})")
    print(f"  running column {col.balance}  binary-search probes {col.postings_scanned}")
    agree = fold.balance == snap.balance and snap.balance == col.balance
    print(f"  all three agree: {str(agree).lower()}")
    total = Money.zero(USD)
    for i in range(merchants):
        total = total.plus(big.balance(PaymentStream.merchant_id(i)))
    print(f"receivable {big.balance('1200')} == sum of merchant payables {total}: {str(big.balance('1200') == total).lower()}")
    if timing:
        print(f"timing: post 500k entries {(t1 - t0) // 1_000_000} ms; fold {(t3 - t2) / 1e6:.3f} ms; "
              f"snapshot {(t4 - t3) / 1e6:.3f} ms; column {(t5 - t4) / 1e6:.4f} ms")


if __name__ == "__main__":
    main()
