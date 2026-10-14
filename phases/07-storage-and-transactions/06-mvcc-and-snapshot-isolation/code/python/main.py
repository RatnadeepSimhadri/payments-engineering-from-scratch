"""Lesson: phases/07-storage-and-transactions/06-mvcc-and-snapshot-isolation/docs/en.md
MVCC and Snapshot Isolation: a multi-version store with snapshot reads, first-committer-wins, and vacuum
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: Berenson et al., "A Critique of ANSI SQL Isolation Levels" (SIGMOD 1995); PostgreSQL docs ch. 13
(Concurrency Control) and ch. 25 (Routine Vacuuming). Python 3.11+, standard library only.
"""

from __future__ import annotations

from dataclasses import dataclass
from enum import Enum
from typing import Generic, TypeVar

V = TypeVar("V")

INT64_MIN, INT64_MAX = -(1 << 63), (1 << 63) - 1


def _check_int64(r: int) -> int:
    """Math.addExact / subtractExact: Python ints never overflow, so the signed 64-bit range is checked by hand."""
    if r < INT64_MIN or r > INT64_MAX:
        raise OverflowError("long overflow")
    return r


@dataclass(frozen=True)
class Currency:
    """ISO 4217 currency: code plus the number of decimal places in its minor unit (Phase 10, Lesson 01)."""

    code: str
    exponent: int

    def __post_init__(self) -> None:
        if self.code is None or len(self.code) != 3:
            raise ValueError("currency code must be three letters")
        if self.exponent < 0 or self.exponent > 4:
            raise ValueError("exponent must be 0..4")


Currency.USD = Currency("USD", 2)


@dataclass(frozen=True)
class Money:
    """An exact count of minor units in one currency. Never a float."""

    minor_units: int
    currency: Currency

    def __post_init__(self) -> None:
        if not isinstance(self.minor_units, int) or isinstance(self.minor_units, bool):
            raise TypeError("minor_units must be an int; never construct Money from a float")
        if self.currency is None:
            raise TypeError("currency")

    @staticmethod
    def of_minor(minor_units: int, currency: Currency) -> "Money":
        return Money(minor_units, currency)

    def plus(self, other: "Money") -> "Money":
        self._require_same_currency(other)
        return Money(_check_int64(self.minor_units + other.minor_units), self.currency)

    def minus(self, other: "Money") -> "Money":
        self._require_same_currency(other)
        return Money(_check_int64(self.minor_units - other.minor_units), self.currency)

    def is_negative(self) -> bool:
        return self.minor_units < 0

    def _require_same_currency(self, other: "Money") -> None:
        if self.currency != other.currency:
            raise ValueError(f"currency mismatch: {self.currency.code} vs {other.currency.code}")

    def __str__(self) -> str:
        scale = 10 ** self.currency.exponent
        a = abs(self.minor_units)
        frac = "" if self.currency.exponent == 0 else f".{a % scale:0{self.currency.exponent}d}"
        return f"{'-' if self.minor_units < 0 else ''}{a // scale}{frac} {self.currency.code}"


class TimestampOracle:
    """Hands out strictly increasing logical timestamps. One per commit; snapshots read the current value."""

    def __init__(self) -> None:
        self._last = 0

    def current(self) -> int:
        """The most recent timestamp handed out. A transaction beginning now sees everything committed at or before it."""
        return self._last

    def next(self) -> int:
        """A fresh timestamp, larger than every one handed out so far. Used as a commit timestamp."""
        self._last += 1
        return self._last


class TxnStatus(Enum):
    ACTIVE = "ACTIVE"
    COMMITTED = "COMMITTED"
    ABORTED = "ABORTED"


class WriteConflictException(RuntimeError):
    """Raised at commit when another transaction committed a newer version of a key this one wrote."""


@dataclass(frozen=True)
class Version(Generic[V]):
    """One immutable version of a key. value None is a tombstone (the key was deleted at commit_ts)."""

    commit_ts: int
    txn_id: int
    value: V | None

    def is_tombstone(self) -> bool:
        return self.value is None


class Transaction(Generic[V]):
    """A transaction: its snapshot, its private write set, and its outcome."""

    def __init__(self, id: int, start_ts: int) -> None:
        self.id = id
        self.start_ts = start_ts
        self.writes: dict[str, V | None] = {}  # None value = delete
        self.status = TxnStatus.ACTIVE
        self.commit_ts = -1

    def is_active(self) -> bool:
        return self.status is TxnStatus.ACTIVE

    def __str__(self) -> str:
        return f"T{self.id}@{self.start_ts}"


class MvccStore(Generic[V]):
    """A multi-version key-value store under snapshot isolation.

    Invariants:
     - A transaction with start_ts S sees, per key, the newest version with commit_ts <= S (plus its own writes).
     - Writes are buffered privately and become versions only at commit, stamped with a fresh commit timestamp.
     - First committer wins: commit fails if any written key has a version with commit_ts > S.
     - Readers never block writers and writers never block readers; nothing in this class waits.
    """

    def __init__(self, oracle: TimestampOracle) -> None:
        self._oracle = oracle
        self._versions: dict[str, list[Version[V]]] = {}  # per key, ascending commit_ts
        self._active: dict[int, Transaction[V]] = {}
        self._next_txn_id = 1
        self._commits = 0
        self._aborts = 0

    def begin(self) -> Transaction[V]:
        """Begin a transaction whose snapshot is "everything committed so far"."""
        txn: Transaction[V] = Transaction(self._next_txn_id, self._oracle.current())
        self._next_txn_id += 1
        self._active[txn.id] = txn
        return txn

    def read(self, txn: Transaction[V], key: str) -> V | None:
        """Snapshot read: own uncommitted write first, otherwise the newest version committed at or before start_ts."""
        self._require_active(txn)
        if key in txn.writes:
            return txn.writes[key]
        v = self._visible_version(key, txn.start_ts)
        return None if v is None or v.is_tombstone() else v.value

    def write(self, txn: Transaction[V], key: str, value: V) -> None:
        """Buffer a write. Nothing is visible to anyone else until commit."""
        self._require_active(txn)
        if value is None:
            raise TypeError("use delete() for tombstones")
        txn.writes[key] = value

    def delete(self, txn: Transaction[V], key: str) -> None:
        self._require_active(txn)
        txn.writes[key] = None

    def commit(self, txn: Transaction[V]) -> int:
        """Validate then publish: if any written key has a version newer than our snapshot, we lose."""
        self._require_active(txn)
        for key in txn.writes:
            newest = self._newest_version(key)
            if newest is not None and newest.commit_ts > txn.start_ts:
                self.abort(txn)
                raise WriteConflictException(
                    f"{txn} lost {key} to T{newest.txn_id} (committed at {newest.commit_ts} > snapshot {txn.start_ts})")
        ts = txn.start_ts if not txn.writes else self._oracle.next()  # read-only transactions consume no timestamp
        for key, value in txn.writes.items():
            self._versions.setdefault(key, []).append(Version(ts, txn.id, value))
        txn.status = TxnStatus.COMMITTED
        txn.commit_ts = ts
        del self._active[txn.id]
        self._commits += 1
        return ts

    def abort(self, txn: Transaction[V]) -> None:
        if not txn.is_active():
            return
        txn.status = TxnStatus.ABORTED
        txn.writes.clear()
        del self._active[txn.id]
        self._aborts += 1

    def vacuum(self) -> int:
        """Remove versions no snapshot can see any more; the horizon is the oldest active snapshot (or now).

        A version is dead when a newer version of the same key has commit_ts <= horizon. A tombstone that is itself
        the newest version at or below the horizon takes the whole key with it. Returns the number removed.
        """
        horizon = self.oldest_active_snapshot()
        removed = 0
        empty_keys: list[str] = []
        for key, chain in self._versions.items():
            keep_from = 0  # index of the newest version with commit_ts <= horizon; everything before it is dead
            for i, v in enumerate(chain):
                if v.commit_ts <= horizon:
                    keep_from = i
            if keep_from > 0:
                removed += keep_from
                del chain[:keep_from]
            oldest_kept = chain[0]
            if oldest_kept.is_tombstone() and oldest_kept.commit_ts <= horizon:
                chain.pop(0)
                removed += 1
            if not chain:
                empty_keys.append(key)
        for key in empty_keys:
            del self._versions[key]
        return removed

    def oldest_active_snapshot(self) -> int:
        horizon = self._oracle.current()
        for t in self._active.values():
            horizon = min(horizon, t.start_ts)
        return horizon

    def version_count(self, key: str) -> int:
        return len(self._versions.get(key, ()))

    def total_versions(self) -> int:
        return sum(len(c) for c in self._versions.values())

    def active_count(self) -> int:
        return len(self._active)

    def commits(self) -> int:
        return self._commits

    def aborts(self) -> int:
        return self._aborts

    def history(self, key: str) -> tuple[Version[V], ...]:
        return tuple(self._versions.get(key, ()))  # read-only view, like Collections.unmodifiableList

    def _visible_version(self, key: str, snapshot_ts: int) -> Version[V] | None:
        for v in reversed(self._versions.get(key, ())):
            if v.commit_ts <= snapshot_ts:
                return v
        return None

    def _newest_version(self, key: str) -> Version[V] | None:
        chain = self._versions.get(key)
        return chain[-1] if chain else None

    def _require_active(self, txn: Transaction[V]) -> None:
        if not txn.is_active():
            raise RuntimeError(f"{txn} is {txn.status.name}")


def usd(major: str) -> Money:
    """Parse "19.99" into 1999 cents without ever touching a float. Demo helper; the full parser is Phase 10, Lesson 01."""
    negative = major.startswith("-")
    parts = (major[1:] if negative else major).split(".")
    cents = int(parts[0]) * 100 + (int((parts[1] + "00")[:2]) if len(parts) > 1 else 0)
    return Money.of_minor(-cents if negative else cents, Currency.USD)


def balance(store: MvccStore[Money], t: Transaction[Money], key: str) -> Money:
    v = store.read(t, key)
    return v if v is not None else Money.of_minor(0, Currency.USD)


def withdraw_if_jointly_covered(store: MvccStore[Money], t: Transaction[Money], frm: str, other: str,
                                amount: Money) -> bool:
    """Try to withdraw from frm as long as the two joint accounts together stay at or above zero."""
    total = balance(store, t, frm).plus(balance(store, t, other))
    if total.minus(amount).is_negative():
        return False
    store.write(t, frm, balance(store, t, frm).minus(amount))
    return True


def main() -> None:
    oracle = TimestampOracle()
    store: MvccStore[Money] = MvccStore(oracle)

    setup = store.begin()
    store.write(setup, "acct:A", usd("100.00"))
    store.write(setup, "acct:B", usd("100.00"))
    store.commit(setup)

    print("== 1. Readers never block writers (and never see them) ==")
    reader = store.begin()
    print(f"{reader} reads A = {balance(store, reader, 'acct:A')}")
    writer = store.begin()
    store.write(writer, "acct:A", usd("40.00"))
    ts = store.commit(writer)
    print(f"{writer} wrote A = 40.00 and committed at ts {ts} without waiting for {reader}")
    print(f"{reader} reads A again = {balance(store, reader, 'acct:A')}  (same snapshot, same answer)")
    later = store.begin()
    print(f"{later} (new snapshot) reads A = {balance(store, later, 'acct:A')}")
    store.commit(reader)
    store.commit(later)

    print()
    print("== 2. Lost update: first committer wins, second aborts ==")
    t1 = store.begin()
    t2 = store.begin()
    store.write(t1, "acct:A", balance(store, t1, "acct:A").minus(usd("10.00")))
    store.write(t2, "acct:A", balance(store, t2, "acct:A").minus(usd("25.00")))
    store.commit(t1)
    peek = store.begin()
    print(f"{t1} committed A = {balance(store, peek, 'acct:A')}")
    store.commit(peek)
    try:
        store.commit(t2)
    except WriteConflictException as e:
        print(f"rejected: {e}")

    print()
    print("== 3. Write skew: the anomaly snapshot isolation lets through ==")
    reset = store.begin()
    store.write(reset, "acct:A", usd("100.00"))
    store.write(reset, "acct:B", usd("100.00"))
    store.commit(reset)
    w1 = store.begin()
    w2 = store.begin()
    ok1 = withdraw_if_jointly_covered(store, w1, "acct:A", "acct:B", usd("150.00"))
    ok2 = withdraw_if_jointly_covered(store, w2, "acct:B", "acct:A", usd("150.00"))
    print(f"{w1} checks A+B = 200.00 >= 150.00 -> {str(ok1).lower()}, writes A = -50.00")
    print(f"{w2} checks A+B = 200.00 >= 150.00 -> {str(ok2).lower()}, writes B = -50.00")
    store.commit(w1)
    store.commit(w2)
    audit = store.begin()
    a, b = balance(store, audit, "acct:A"), balance(store, audit, "acct:B")
    print(f"both committed (disjoint write sets). A = {a}, B = {b}, A+B = {a.plus(b)}  <- constraint violated")
    store.commit(audit)

    print()
    print("== 4. Garbage collection: a long-running snapshot pins old versions ==")
    long_runner = store.begin()
    for _ in range(5):
        t = store.begin()
        store.write(t, "acct:A", balance(store, t, "acct:A").plus(usd("1.00")))
        store.commit(t)
    print(f"versions of A before vacuum: {store.version_count('acct:A')}, total: {store.total_versions()}")
    removed = store.vacuum()
    print(f"vacuum with {long_runner} still open removed {removed} versions "
          f"(horizon = ts {store.oldest_active_snapshot()})")
    store.commit(long_runner)
    removed = store.vacuum()
    print(f"vacuum after it finished removed {removed}; versions of A now: {store.version_count('acct:A')}, "
          f"total: {store.total_versions()}")
    print(f"commits = {store.commits()}, aborts = {store.aborts()}")


if __name__ == "__main__":
    main()
