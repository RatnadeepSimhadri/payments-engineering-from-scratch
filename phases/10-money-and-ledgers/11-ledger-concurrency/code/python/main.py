"""Lesson: phases/10-money-and-ledgers/11-ledger-concurrency/docs/en.md
Ledger Concurrency: Hot Accounts and Balance Checks
Run:  python3 main.py          (add --stats for contention counters and timings)
Test: python3 -m unittest -v
Sources: Kung & Robinson, "On Optimistic Methods for Concurrency Control" (1981); the CPython GIL and threading
module in place of the JLS memory model and java.util.concurrent; Money from Lesson 01. Python 3.11+, stdlib only.
"""

from __future__ import annotations

import itertools
import sys
import threading
import time
from dataclasses import dataclass
from enum import Enum
from functools import total_ordering
from typing import Dict, List, Optional, Sequence, Tuple

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
        order = sorted(range(len(ratios)), key=lambda i: -remainders[i])
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
# Part 1: a deterministic model checker. Each strategy is a tiny step machine; we explore EVERY
# interleaving of a few concurrent operations on one account and check the invariants at the end.
# This is the reproducible race: the GIL makes the real-thread race below hard to observe, the checker makes it certain.
# ---------------------------------------------------------------------------------------------


class Strategy(Enum):
    NAIVE = "naive"
    VERSIONED = "versioned"
    LOCKED = "locked"


@dataclass(frozen=True)
class Op:
    """A deposit (amount > 0, never rejected) or a payout (amount < 0, rejected if it would overdraw)."""

    name: str
    amount: int


@dataclass(frozen=True)
class Exploration:
    """Result of exhaustive exploration: how many complete schedules, and how many broke an invariant."""

    strategy: Strategy
    schedules: int
    violations: int
    first_violation: Optional[str]


class InterleavingChecker:
    DONE = 99

    class World:
        """Mutable world state, copied at every branch."""

        __slots__ = ("balance", "version", "lock_holder", "pc", "local_balance", "local_version", "applied", "trace")

        def __init__(self, n: int, balance: int) -> None:
            self.balance = balance
            self.version = 0
            self.lock_holder = -1
            self.pc = [0] * n
            self.local_balance = [0] * n
            self.local_version = [0] * n
            self.applied = [False] * n
            self.trace: List[str] = []

        def copy(self) -> "InterleavingChecker.World":
            w = InterleavingChecker.World.__new__(InterleavingChecker.World)
            w.balance, w.version, w.lock_holder = self.balance, self.version, self.lock_holder
            w.pc, w.local_balance, w.local_version = list(self.pc), list(self.local_balance), list(self.local_version)
            w.applied = list(self.applied)
            w.trace = list(self.trace)
            return w

    @staticmethod
    def explore(strategy: Strategy, initial_balance: int, ops: Sequence[Op]) -> Exploration:
        w = InterleavingChecker.World(len(ops), initial_balance)
        counts = [0, 0]
        first: List[Optional[str]] = [None]
        InterleavingChecker._dfs(strategy, initial_balance, ops, w, counts, first, 0)
        return Exploration(strategy, counts[0], counts[1], first[0])

    @staticmethod
    def _dfs(s: Strategy, initial: int, ops: Sequence[Op], w: "InterleavingChecker.World", counts: List[int],
             first: List[Optional[str]], depth: int) -> None:
        if depth > 200:
            raise RuntimeError("exploration did not terminate")
        any_enabled = False
        for t in range(len(ops)):
            if w.pc[t] == InterleavingChecker.DONE or not InterleavingChecker._enabled(s, w, t):
                continue
            any_enabled = True
            nxt = w.copy()
            InterleavingChecker._step(s, nxt, t, ops[t])
            InterleavingChecker._dfs(s, initial, ops, nxt, counts, first, depth + 1)
        if not any_enabled:
            counts[0] += 1
            violation = InterleavingChecker._check(initial, ops, w)
            if violation is not None:
                counts[1] += 1
                if first[0] is None:
                    first[0] = violation + " via " + " ".join(w.trace)

    @staticmethod
    def _enabled(s: Strategy, w: "InterleavingChecker.World", t: int) -> bool:
        return not (s is Strategy.LOCKED and w.pc[t] == 0 and w.lock_holder != -1)  # blocked waiting for the lock

    @staticmethod
    def _step(s: Strategy, w: "InterleavingChecker.World", t: int, op: Op) -> None:
        """One atomic step of thread t. Each case is exactly one shared-memory access (or lock action)."""
        who = op.name
        DONE = InterleavingChecker.DONE
        if s is Strategy.NAIVE:
            if w.pc[t] == 0:
                w.local_balance[t] = w.balance
                w.pc[t] = 1
                w.trace.append(f"{who}:read")
            else:
                nxt = w.local_balance[t] + op.amount
                if nxt >= 0:
                    w.balance = nxt
                    w.applied[t] = True
                    w.trace.append(f"{who}:write")
                else:
                    w.trace.append(f"{who}:reject")
                w.pc[t] = DONE
        elif s is Strategy.VERSIONED:
            if w.pc[t] == 0:
                w.local_balance[t] = w.balance
                w.local_version[t] = w.version
                w.pc[t] = 1
                w.trace.append(f"{who}:read")
            elif w.version != w.local_version[t]:
                w.pc[t] = 0  # stale read: retry from the top
                w.trace.append(f"{who}:conflict")
            else:  # validate + write, one atomic step
                nxt = w.local_balance[t] + op.amount
                if nxt >= 0:
                    w.balance = nxt
                    w.version += 1
                    w.applied[t] = True
                    w.trace.append(f"{who}:commit")
                else:
                    w.trace.append(f"{who}:reject")
                w.pc[t] = DONE
        else:  # LOCKED
            pc = w.pc[t]
            if pc == 0:
                w.lock_holder = t
                w.pc[t] = 1
                w.trace.append(f"{who}:lock")
            elif pc == 1:
                w.local_balance[t] = w.balance
                w.pc[t] = 2
                w.trace.append(f"{who}:read")
            elif pc == 2:
                nxt = w.local_balance[t] + op.amount
                if nxt >= 0:
                    w.balance = nxt
                    w.applied[t] = True
                    w.trace.append(f"{who}:write")
                else:
                    w.trace.append(f"{who}:reject")
                w.pc[t] = 3
            else:
                w.lock_holder = -1
                w.pc[t] = DONE
                w.trace.append(f"{who}:unlock")

    @staticmethod
    def _check(initial: int, ops: Sequence[Op], w: "InterleavingChecker.World") -> Optional[str]:
        """Invariants at the end of a schedule: conservation (balance = initial + applied ops) and no overdraft."""
        expected = initial + sum(ops[t].amount for t in range(len(ops)) if w.applied[t])
        if w.balance != expected:
            return f"lost update: balance {w.balance} but applied ops imply {expected}"
        if w.balance < 0:
            return f"overdraft: balance {w.balance}"
        return None


# ---------------------------------------------------------------------------------------------
# Part 2: real threads. Three ledgers behind one interface, each a different answer to the hot account.
# ---------------------------------------------------------------------------------------------


class InsufficientFundsError(RuntimeError):
    def __init__(self, account: str, balance: int, amount: int) -> None:
        super().__init__(f"account {account} has {balance} minor units, needs {amount}")


class AtomicLong:
    """A counter that several threads bump. `+=` on an attribute is not atomic across threads in CPython, so lock it."""

    def __init__(self) -> None:
        self._value = 0
        self._lock = threading.Lock()

    def increment_and_get(self) -> int:
        with self._lock:
            self._value += 1
            return self._value

    def get_and_increment(self) -> int:
        with self._lock:
            v = self._value
            self._value += 1
            return v

    def get(self) -> int:
        return self._value


class ConcurrentLedger:
    """The interface all three ledgers implement."""

    def name(self) -> str:
        raise NotImplementedError

    def open_account(self, id: str, currency: Currency, initial_minor: int, overdraft_allowed: bool) -> None:
        """Open an account. overdraft_allowed=False means a transfer out of it must never drive it below zero."""
        raise NotImplementedError

    def transfer(self, from_: str, to: str, amount: Money) -> None:
        """Move amount from one account to another atomically, or raise without changing anything."""
        raise NotImplementedError

    def balance(self, id: str) -> Money:
        """A consistent balance. For sharded accounts this sums the shards."""
        raise NotImplementedError

    def total_minor(self) -> int:
        """Sum of every account's balance: constant under transfers, the conservation check."""
        raise NotImplementedError

    def contention(self) -> int:
        """Retries (optimistic) or lock waits (pessimistic): how much the hot account cost. Not deterministic."""
        raise NotImplementedError


class Cell:
    """Mutable account cell for the lock-based ledgers. Guarded by its lock."""

    __slots__ = ("id", "currency", "overdraft_allowed", "lock", "balance")

    def __init__(self, id: str, currency: Currency, balance: int, overdraft_allowed: bool) -> None:
        self.id = id
        self.currency = currency
        self.balance = balance
        self.overdraft_allowed = overdraft_allowed
        self.lock = threading.Lock()


class PessimisticLedger(ConcurrentLedger):
    """Pessimistic: lock both accounts (in a global order), check, apply, unlock."""

    def __init__(self) -> None:
        self._cells: Dict[str, Cell] = {}
        self._registry_lock = threading.Lock()
        self._waits = AtomicLong()

    def name(self) -> str:
        return "pessimistic"

    def open_account(self, id: str, currency: Currency, initial_minor: int, overdraft_allowed: bool) -> None:
        with self._registry_lock:
            if id in self._cells:
                raise ValueError(f"account exists: {id}")
            self._cells[id] = Cell(id, currency, initial_minor, overdraft_allowed)

    def _cell(self, id: str) -> Cell:
        c = self._cells.get(id)
        if c is None:
            raise ValueError(f"unknown account: {id}")
        return c

    def transfer(self, from_: str, to: str, amount: Money) -> None:
        a, b = self._cell(from_), self._cell(to)
        self._validate(a, b, amount)
        first, second = (a, b) if a.id < b.id else (b, a)  # global order: no deadlock
        self._lock(first)
        try:
            self._lock(second)
            try:
                m = amount.minor_units
                if not a.overdraft_allowed and a.balance < m:
                    raise InsufficientFundsError(a.id, a.balance, m)
                a.balance -= m
                b.balance += m
            finally:
                second.lock.release()
        finally:
            first.lock.release()

    def _lock(self, c: Cell) -> None:
        if not c.lock.acquire(blocking=False):
            self._waits.increment_and_get()
            c.lock.acquire()

    def _move_up_to(self, src: Cell, dst: Cell, maximum: int) -> int:
        """Move min(src.balance, maximum) from src to dst under both locks (global order). Returns what moved."""
        first, second = (src, dst) if src.id < dst.id else (dst, src)
        self._lock(first)
        try:
            self._lock(second)
            try:
                take = min(max(src.balance, 0), maximum)
                src.balance -= take
                dst.balance += take
                return take
            finally:
                second.lock.release()
        finally:
            first.lock.release()

    @staticmethod
    def _validate(a: Cell, b: Cell, amount: Money) -> None:
        if a is b:
            raise ValueError("transfer to the same account")
        if not amount.is_positive():
            raise ValueError("amount must be positive")
        if a.currency != amount.currency:
            raise CurrencyMismatchError(a.currency, amount.currency)
        if b.currency != amount.currency:
            raise CurrencyMismatchError(b.currency, amount.currency)

    def balance(self, id: str) -> Money:
        c = self._cell(id)
        with c.lock:
            return Money.of_minor(c.balance, c.currency)

    def total_minor(self) -> int:
        """Locks every account in order, so the total is a consistent cut, not a smear across in-flight transfers."""
        every = sorted(self._cells.values(), key=lambda c: c.id)
        for c in every:
            c.lock.acquire()
        try:
            return sum(c.balance for c in every)
        finally:
            for c in every:
                c.lock.release()

    def contention(self) -> int:
        return self._waits.get()


@dataclass(frozen=True)
class AccountState:
    """Immutable versioned state: the row an optimistic UPDATE ... WHERE version = ? would read."""

    version: int
    balance: int


class _Row:
    __slots__ = ("id", "currency", "overdraft_allowed", "state", "commit")

    def __init__(self, id: str, currency: Currency, overdraft_allowed: bool, state: AccountState) -> None:
        self.id = id
        self.currency = currency
        self.overdraft_allowed = overdraft_allowed
        self.state = state  # replaced wholesale, never mutated: a plain attribute is CPython's AtomicReference
        self.commit = threading.Lock()


class OptimisticLedger(ConcurrentLedger):
    """Optimistic: read both versions without locking, run the balance check, then commit only if neither version
    changed (a short validate-and-write critical section, as in Kung and Robinson's serial validation). On conflict, retry."""

    def __init__(self) -> None:
        self._rows: Dict[str, _Row] = {}
        self._registry_lock = threading.Lock()
        self._retries = AtomicLong()

    def name(self) -> str:
        return "optimistic"

    def open_account(self, id: str, currency: Currency, initial_minor: int, overdraft_allowed: bool) -> None:
        with self._registry_lock:
            if id in self._rows:
                raise ValueError(f"account exists: {id}")
            self._rows[id] = _Row(id, currency, overdraft_allowed, AccountState(0, initial_minor))

    def _row(self, id: str) -> _Row:
        r = self._rows.get(id)
        if r is None:
            raise ValueError(f"unknown account: {id}")
        return r

    def transfer(self, from_: str, to: str, amount: Money) -> None:
        a, b = self._row(from_), self._row(to)
        if a is b:
            raise ValueError("transfer to the same account")
        if not amount.is_positive():
            raise ValueError("amount must be positive")
        if a.currency != amount.currency:
            raise CurrencyMismatchError(a.currency, amount.currency)
        if b.currency != amount.currency:
            raise CurrencyMismatchError(b.currency, amount.currency)
        m = amount.minor_units
        first, second = (a, b) if a.id < b.id else (b, a)
        while True:
            sa, sb = a.state, b.state                                   # read phase: no locks
            insufficient = not a.overdraft_allowed and sa.balance < m   # business check on the read
            first.commit.acquire()
            second.commit.acquire()
            try:
                if a.state is sa and b.state is sb:                      # validate: nothing changed since the read
                    if insufficient:
                        raise InsufficientFundsError(a.id, sa.balance, m)
                    a.state = AccountState(sa.version + 1, sa.balance - m)
                    b.state = AccountState(sb.version + 1, sb.balance + m)
                    return
            finally:
                second.commit.release()
                first.commit.release()
            self._retries.increment_and_get()                           # stale read: go again

    def balance(self, id: str) -> Money:
        r = self._row(id)
        return Money.of_minor(r.state.balance, r.currency)

    def version(self, id: str) -> int:
        return self._row(id).state.version

    def total_minor(self) -> int:
        every = sorted(self._rows.values(), key=lambda r: r.id)
        for r in every:
            r.commit.acquire()
        try:
            return sum(r.state.balance for r in every)
        finally:
            for r in every:
                r.commit.release()

    def contention(self) -> int:
        return self._retries.get()


class ShardedLedger(PessimisticLedger):
    """Sharded: a hot account is K sub-accounts "id#0".."id#K-1". Credits pick one shard (by a caller-supplied
    routing key), so K writers proceed in parallel. Debits drain shards greedily, one shard lock at a time, and
    give everything back if the total is short, through an in-flight account. roll_up() sweeps every shard into shard 0."""

    def __init__(self) -> None:
        super().__init__()
        self._shard_count: Dict[str, int] = {}
        self._routing = itertools.count()  # next() on a count is atomic under the GIL

    def name(self) -> str:
        return "sharded"

    def open_hot_account(self, id: str, currency: Currency, initial_minor: int, shards: int) -> None:
        if shards < 1:
            raise ValueError("need at least one shard")
        parts = Money.of_minor(initial_minor, currency).allocate(*([1] * shards))
        for k in range(shards):
            super().open_account(self.shard_id(id, k), currency, parts[k].minor_units, False)
        super().open_account(f"{id}#inflight", currency, 0, False)
        self._shard_count[id] = shards

    @staticmethod
    def shard_id(id: str, k: int) -> str:
        return f"{id}#{k}"

    def is_hot(self, id: str) -> bool:
        return id in self._shard_count

    def shards(self, id: str) -> int:
        return self._shard_count.get(id, 1)

    def transfer(self, from_: str, to: str, amount: Money) -> None:
        if self.is_hot(from_) and self.is_hot(to):
            raise ValueError("hot-to-hot transfers are not supported")
        if self.is_hot(to):
            k = next(self._routing) % self.shards(to)  # spread credits across shards
            super().transfer(from_, self.shard_id(to, k), amount)
        elif self.is_hot(from_):
            self._debit_hot(from_, to, amount)
        else:
            super().transfer(from_, to, amount)

    def _debit_hot(self, hot: str, to: str, amount: Money) -> None:
        """Greedy drain across shards into an in-flight account, then one move to the destination. Every step is a
        two-account locked move, so a consistent cut (total_minor) sees the money in exactly one place at all times.
        If the shards are short, every unit is moved back and the payout is rejected."""
        inflight, dest = self._cell(f"{hot}#inflight"), self._cell(to)
        self._validate(inflight, dest, amount)
        remaining = amount.minor_units
        n = self.shards(hot)
        taken = [0] * n
        start = next(self._routing) % n
        for i in range(n):
            if remaining <= 0:
                break
            k = (start + i) % n
            got = self._move_up_to(self._cell(self.shard_id(hot, k)), inflight, remaining)
            taken[k] += got
            remaining -= got
        if remaining > 0:
            for k in range(n):
                if taken[k] > 0:
                    self._move_up_to(inflight, self._cell(self.shard_id(hot, k)), taken[k])
            raise InsufficientFundsError(hot, amount.minor_units - remaining, amount.minor_units)
        self._move_up_to(inflight, dest, amount.minor_units)

    def balance(self, id: str) -> Money:
        """Sum of the shards, read under all shard locks: a consistent balance for one hot account."""
        if not self.is_hot(id):
            return super().balance(id)
        cs = [self._cell(self.shard_id(id, k)) for k in range(self.shards(id))]
        for c in cs:
            c.lock.acquire()
        try:
            return Money.of_minor(sum(c.balance for c in cs), cs[0].currency)
        finally:
            for c in cs:
                c.lock.release()

    def roll_up(self, id: str) -> int:
        """Sweep shards 1..K-1 into shard 0 (in a real ledger, one sweep entry per shard). Total is unchanged."""
        moved = 0
        for k in range(1, self.shards(id)):
            shard = self._cell(self.shard_id(id, k))
            self._lock(shard)
            try:
                amount = shard.balance
            finally:
                shard.lock.release()
            if amount > 0:
                super().transfer(self.shard_id(id, k), self.shard_id(id, 0), Money.of_minor(amount, shard.currency))
                moved += amount
        return moved

    def shard_balance(self, id: str, k: int) -> int:
        return super().balance(self.shard_id(id, k)).minor_units


class JavaRandom:
    """java.util.Random's 48-bit LCG, so Workload scripts (and therefore the demo's balances) match the Java version."""

    def __init__(self, seed: int) -> None:
        self._seed = (seed ^ 0x5DEECE66D) & ((1 << 48) - 1)

    def next(self, bits: int) -> int:
        self._seed = (self._seed * 0x5DEECE66D + 0xB) & ((1 << 48) - 1)
        r = self._seed >> (48 - bits)
        return r - (1 << 32) if r >= (1 << 31) else r

    def next_int(self, bound: int) -> int:
        if bound <= 0:
            raise ValueError("bound must be positive")
        r = self.next(31)
        m = bound - 1
        if (bound & m) == 0:
            return (bound * r) >> 31
        u = r
        r = u % bound
        while u - r + m >= (1 << 31):
            u = self.next(31)
            r = u % bound
        return r


@dataclass(frozen=True)
class Workload:
    """The workload: many customers paying one merchant (the hot account), plus payouts out of it to the bank."""

    threads: int
    ops_per_thread: int
    customers: int
    customer_float: int
    merchant_float: int
    payout_every: int
    seed: int

    @staticmethod
    def customer(i: int) -> str:
        return f"c{i:03d}"

    def open(self, ledger: ConcurrentLedger, merchant_shards: int) -> None:
        for i in range(self.customers):
            ledger.open_account(self.customer(i), USD, self.customer_float, False)
        ledger.open_account("bank", USD, 0, True)
        if isinstance(ledger, ShardedLedger) and merchant_shards > 1:
            ledger.open_hot_account("merchant", USD, self.merchant_float, merchant_shards)
        else:
            ledger.open_account("merchant", USD, self.merchant_float, False)

    def script(self, thread: int) -> List[Tuple[int, int]]:
        """Deterministic per-thread script: what each thread will do, independent of how threads interleave."""
        r = JavaRandom(self.seed * 31 + thread)
        ops = []
        for i in range(self.ops_per_thread):
            payout = i % self.payout_every == self.payout_every - 1
            ops.append((-1 if payout else r.next_int(self.customers), 1 + r.next_int(10_000)))
        return ops

    def expected(self) -> Dict[str, int]:
        """Expected final balances: computable from the scripts alone because every op is guaranteed to succeed."""
        m = {self.customer(i): self.customer_float for i in range(self.customers)}
        m["merchant"] = self.merchant_float
        m["bank"] = 0
        for t in range(self.threads):
            for who, amount in self.script(t):
                if who < 0:
                    m["merchant"] -= amount
                    m["bank"] += amount
                else:
                    m[self.customer(who)] -= amount
                    m["merchant"] += amount
        return m

    def payout_total(self) -> int:
        return sum(amount for t in range(self.threads) for who, amount in self.script(t) if who < 0)

    def run(self, ledger: ConcurrentLedger) -> int:
        """Run every thread's script concurrently, released together by an event. Returns failures seen (should be 0)."""
        start = threading.Event()
        failures = AtomicLong()

        def worker(ops: List[Tuple[int, int]]) -> None:
            start.wait()
            for who, amount in ops:
                try:
                    m = Money.of_minor(amount, USD)
                    if who < 0:
                        ledger.transfer("merchant", "bank", m)
                    else:
                        ledger.transfer(self.customer(who), "merchant", m)
                except Exception:
                    failures.increment_and_get()

        ts = [threading.Thread(target=worker, args=(self.script(t),)) for t in range(self.threads)]
        for th in ts:
            th.start()
        start.set()
        for th in ts:
            th.join()
        return failures.get()


def _bool(b: bool) -> str:
    return str(b).lower()


def main() -> None:
    stats = len(sys.argv) > 1 and sys.argv[1] == "--stats"

    print("== Exhaustive interleavings: balance 100.00, payouts of 60.00 and 60.00, deposit of 30.00 ==")
    ops = [Op("P1", -6_000), Op("P2", -6_000), Op("D", 3_000)]
    for s in Strategy:
        e = InterleavingChecker.explore(s, 10_000, ops)
        print(f"{s.name:<9} schedules {e.schedules:4d}  violations {e.violations:4d}")
        if e.first_violation is not None:
            print(f"          e.g. {e.first_violation}")

    print("\n== 8 threads x 20,000 ops: customers pay one hot merchant account; every 20th op pays out ==")
    w = Workload(8, 20_000, 200, 50_000_000, 2_000_000_000, 20, 42)
    expected = w.expected()
    initial_total = 200 * 50_000_000 + 2_000_000_000
    print(f"payouts requested {Money.of_minor(w.payout_total(), USD)} against a merchant float of "
          f"{Money.of_minor(2_000_000_000, USD)} (every op can succeed in any order)")
    ledgers: List[ConcurrentLedger] = [PessimisticLedger(), OptimisticLedger(), ShardedLedger()]
    for ledger in ledgers:
        w.open(ledger, 8)
        t0 = time.perf_counter_ns()
        failures = w.run(ledger)
        ms = (time.perf_counter_ns() - t0) // 1_000_000
        if isinstance(ledger, ShardedLedger):
            ledger.roll_up("merchant")
        match = all(ledger.balance(k).minor_units == v for k, v in expected.items())
        print(f"{ledger.name():<12} failures {failures}  total conserved {_bool(ledger.total_minor() == initial_total):<5}  "
              f"every balance as expected {_bool(match):<5}  merchant {ledger.balance('merchant')}")
        if stats:
            print(f"             contention {ledger.contention()}  elapsed {ms} ms")

    print("\n== A payout that would overdraw is rejected atomically, in every strategy ==")
    for ledger in (PessimisticLedger(), OptimisticLedger(), ShardedLedger()):
        ledger.open_account("bank", USD, 0, True)
        if isinstance(ledger, ShardedLedger):
            ledger.open_hot_account("merchant", USD, 10_000, 4)
        else:
            ledger.open_account("merchant", USD, 10_000, False)
        try:
            ledger.transfer("merchant", "bank", Money.parse("100.01", USD))
        except InsufficientFundsError:
            print(f"{ledger.name()}: rejected; merchant still {ledger.balance('merchant')}, bank {ledger.balance('bank')}")


if __name__ == "__main__":
    main()
