"""Lesson: phases/07-storage-and-transactions/07-serializable-snapshot-isolation/docs/en.md
Serializable Snapshot Isolation: the SI store of Lesson 06 plus SIREAD locks, rw-antidependency tracking,
and the dangerous-structure abort rule.
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: Cahill, Roehm, Fekete, "Serializable Isolation for Snapshot Databases" (SIGMOD 2008); Ports & Grittner,
"Serializable Snapshot Isolation in PostgreSQL" (VLDB 2012). Python 3.11+, standard library only.
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
    """ISO 4217 currency (Phase 10, Lesson 01)."""

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
    """Exact minor units in one currency. Never a float."""

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
            raise ValueError("currency mismatch")

    def __str__(self) -> str:
        scale = 10 ** self.currency.exponent
        a = abs(self.minor_units)
        frac = "" if self.currency.exponent == 0 else f".{a % scale:0{self.currency.exponent}d}"
        return f"{'-' if self.minor_units < 0 else ''}{a // scale}{frac} {self.currency.code}"


class TimestampOracle:
    def __init__(self) -> None:
        self._last = 0

    def current(self) -> int:
        return self._last

    def next(self) -> int:
        self._last += 1
        return self._last


class TxnStatus(Enum):
    ACTIVE = "ACTIVE"
    COMMITTED = "COMMITTED"
    ABORTED = "ABORTED"


class WriteConflictException(RuntimeError):
    """Lesson 06's rule: a newer version of a written key exists, first committer wins."""


class SerializationFailureException(RuntimeError):
    """This lesson's rule: the transaction is, or completes, a dangerous structure T_in -rw-> T -rw-> T_out."""


@dataclass(frozen=True)
class Version(Generic[V]):
    commit_ts: int
    txn_id: int
    value: V | None

    def is_tombstone(self) -> bool:
        return self.value is None


def _first(ordered_set: dict[int, None]) -> int:
    """First element of an insertion-ordered set: LinkedHashSet.iterator().next()."""
    return next(iter(ordered_set))


class Transaction(Generic[V]):
    """An SI transaction plus the two conflict sets SSI needs.

    The conflict sets are dicts with None values: Python's insertion-ordered set, standing in for LinkedHashSet.
    """

    def __init__(self, id: int, start_ts: int, begin_seq: int) -> None:
        self.id = id
        self.start_ts = start_ts
        self.writes: dict[str, V | None] = {}
        self.in_conflicts: dict[int, None] = {}  # T_in -rw-> this: they read a version this txn overwrote
        self.out_conflicts: dict[int, None] = {}  # this -rw-> T_out: they wrote a newer version of what this txn read
        self.status = TxnStatus.ACTIVE
        self.commit_ts = -1
        # Event order: two transactions are concurrent when neither finished before the other began. Commit
        # timestamps cannot tell this, because a read-only commit consumes no timestamp.
        self.begin_seq = begin_seq
        self.end_seq = INT64_MAX
        self.abort_reason: str | None = None

    def concurrent_with(self, other: "Transaction") -> bool:
        return self.begin_seq < other.end_seq and other.begin_seq < self.end_seq

    def is_active(self) -> bool:
        return self.status is TxnStatus.ACTIVE

    def is_committed(self) -> bool:
        return self.status is TxnStatus.COMMITTED

    def is_pivot(self) -> bool:
        return bool(self.in_conflicts) and bool(self.out_conflicts)

    def __str__(self) -> str:
        return f"T{self.id}"


class SsiStore(Generic[V]):
    """Serializable snapshot isolation (Cahill et al. 2008) on top of the Lesson 06 store.

    Additions over SI:
     - Every read takes a SIREAD "lock" on the key: a record, never a block, kept after commit while any concurrent
       transaction is still active.
     - Every read of a key that has a newer version (committed, or buffered in an active writer) records T -rw-> W.
     - Every write of a key that a concurrent transaction read records R -rw-> T.
     - A transaction with both an incoming and an outgoing rw-antidependency is a pivot and is aborted at commit;
       a transaction that would attach a second edge to an already-committed pivot is aborted on the spot.
    """

    def __init__(self, oracle: TimestampOracle) -> None:
        self._oracle = oracle
        self._versions: dict[str, list[Version[V]]] = {}
        self._txns: dict[int, Transaction[V]] = {}  # active and recently committed
        self._sireads: dict[str, dict[int, None]] = {}  # key -> transactions that read it
        self._next_txn_id = 1
        self._seq = 0  # event order of begin/commit/abort
        self._commits = 0
        self._aborts = 0

    def begin(self) -> Transaction[V]:
        self._seq += 1
        txn: Transaction[V] = Transaction(self._next_txn_id, self._oracle.current(), self._seq)
        self._next_txn_id += 1
        self._txns[txn.id] = txn
        return txn

    def read(self, txn: Transaction[V], key: str) -> V | None:
        self._require_active(txn)
        self._sireads.setdefault(key, {})[txn.id] = None
        # rw-antidependency: we read an older version than someone has written or is writing
        for v in self._versions.get(key, ()):
            if v.commit_ts > txn.start_ts:
                self._record_rw(txn, self._txns.get(v.txn_id))
        for other in list(self._txns.values()):  # copy: _record_rw may abort and remove txn
            if other is not txn and other.is_active() and key in other.writes:
                self._record_rw(txn, other)
        if not txn.is_active():
            raise SerializationFailureException(txn.abort_reason)
        if key in txn.writes:
            return txn.writes[key]
        v = self._visible_version(key, txn.start_ts)
        return None if v is None or v.is_tombstone() else v.value

    def write(self, txn: Transaction[V], key: str, value: V) -> None:
        self._require_active(txn)
        if value is None:
            raise TypeError("use delete() for tombstones")
        txn.writes[key] = value
        self._check_readers_of(txn, key)

    def delete(self, txn: Transaction[V], key: str) -> None:
        self._require_active(txn)
        txn.writes[key] = None
        self._check_readers_of(txn, key)

    def _check_readers_of(self, txn: Transaction[V], key: str) -> None:
        """Readers of this key that are concurrent with the writer form R -rw-> txn."""
        for reader_id in list(self._sireads.get(key, {})):
            reader = self._txns.get(reader_id)
            if reader is None or reader is txn:
                continue
            if reader.concurrent_with(txn):
                self._record_rw(reader, txn)
        if not txn.is_active():
            raise SerializationFailureException(txn.abort_reason)

    def _record_rw(self, reader: Transaction[V] | None, writer: Transaction[V] | None) -> None:
        """Record reader -rw-> writer. If the other side is a committed pivot, abort the one still executing."""
        if reader is None or writer is None or reader is writer:
            return
        if not reader.is_active() and not reader.is_committed():
            return
        if not writer.is_active() and not writer.is_committed():
            return
        reader.out_conflicts[writer.id] = None
        writer.in_conflicts[reader.id] = None
        if reader.is_active() and writer.is_committed() and writer.out_conflicts:
            self.abort(reader, f"{reader} -rw-> {writer} -rw-> T{_first(writer.out_conflicts)}: "
                               f"{writer} is a committed pivot")
        elif writer.is_active() and reader.is_committed() and reader.in_conflicts:
            self.abort(writer, f"T{_first(reader.in_conflicts)} -rw-> {reader} -rw-> {writer}: "
                               f"{reader} is a committed pivot")

    def commit(self, txn: Transaction[V]) -> int:
        self._require_active(txn)
        for key in txn.writes:
            newest = self._newest_version(key)
            if newest is not None and newest.commit_ts > txn.start_ts:
                self.abort(txn, f"first committer wins on {key}")
                raise WriteConflictException(txn.abort_reason)
        if txn.is_pivot():
            self.abort(txn, f"T{_first(txn.in_conflicts)} -rw-> {txn} -rw-> T{_first(txn.out_conflicts)}: "
                            f"{txn} is the pivot")
            raise SerializationFailureException(txn.abort_reason)
        ts = txn.start_ts if not txn.writes else self._oracle.next()
        for key, value in txn.writes.items():
            self._versions.setdefault(key, []).append(Version(ts, txn.id, value))
        txn.status = TxnStatus.COMMITTED
        txn.commit_ts = ts
        self._seq += 1
        txn.end_seq = self._seq
        self._commits += 1
        return ts

    def abort(self, txn: Transaction[V], reason: str) -> None:
        """Abort: drop the write set, drop our SIREADs, and remove our edges from everyone else's conflict sets."""
        if not txn.is_active():
            return
        txn.status = TxnStatus.ABORTED
        txn.abort_reason = reason
        self._seq += 1
        txn.end_seq = self._seq
        txn.writes.clear()
        for readers in self._sireads.values():
            readers.pop(txn.id, None)
        for other in self._txns.values():
            other.in_conflicts.pop(txn.id, None)
            other.out_conflicts.pop(txn.id, None)
        self._txns.pop(txn.id, None)
        self._aborts += 1

    def release_finished(self) -> int:
        """Forget committed transactions that finished before every active one began. Returns how many."""
        oldest_active_begin = self._seq + 1
        for t in self._txns.values():
            if t.is_active():
                oldest_active_begin = min(oldest_active_begin, t.begin_seq)
        done = [t.id for t in self._txns.values() if t.is_committed() and t.end_seq < oldest_active_begin]
        for id in done:
            del self._txns[id]
            for readers in self._sireads.values():
                readers.pop(id, None)
        self._sireads = {k: r for k, r in self._sireads.items() if r}
        return len(done)

    def oldest_active_snapshot(self) -> int:
        horizon = self._oracle.current()
        for t in self._txns.values():
            if t.is_active():
                horizon = min(horizon, t.start_ts)
        return horizon

    def retained_read_locks(self) -> int:
        return sum(len(r) for r in self._sireads.values())

    def readers_of(self, key: str) -> set[int]:
        return set(self._sireads.get(key, {}))

    def commits(self) -> int:
        return self._commits

    def aborts(self) -> int:
        return self._aborts

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
            reason = "" if txn.abort_reason is None else f" ({txn.abort_reason})"
            raise RuntimeError(f"{txn} is {txn.status.name}{reason}")


def usd(major: str) -> Money:
    negative = major.startswith("-")
    parts = (major[1:] if negative else major).split(".")
    cents = int(parts[0]) * 100 + (int((parts[1] + "00")[:2]) if len(parts) > 1 else 0)
    return Money.of_minor(-cents if negative else cents, Currency.USD)


def balance(store: SsiStore[Money], t: Transaction[Money], key: str) -> Money:
    v = store.read(t, key)
    return v if v is not None else Money.of_minor(0, Currency.USD)


def withdraw_if_jointly_covered(store: SsiStore[Money], t: Transaction[Money], frm: str, other: str,
                                amount: Money) -> bool:
    total = balance(store, t, frm).plus(balance(store, t, other))
    if total.minus(amount).is_negative():
        return False
    store.write(t, frm, balance(store, t, frm).minus(amount))
    return True


def try_commit(store: SsiStore[Money], t: Transaction[Money]) -> str:
    try:
        ts = store.commit(t)
        return f"{t} committed at ts {ts}"
    except (SerializationFailureException, WriteConflictException) as e:
        return f"{t} ABORTED: {e}"


def _set(s) -> str:
    """Print a set like Java's Set.toString: [1, 2]."""
    return "[" + ", ".join(str(x) for x in s) + "]"


def _b(flag: bool) -> str:
    return "true" if flag else "false"


def main() -> None:
    oracle = TimestampOracle()
    store: SsiStore[Money] = SsiStore(oracle)
    setup = store.begin()
    store.write(setup, "acct:A", usd("100.00"))
    store.write(setup, "acct:B", usd("100.00"))
    store.commit(setup)

    print("== 1. Write skew from Lesson 06, now under SSI ==")
    t1 = store.begin()
    t2 = store.begin()
    withdraw_if_jointly_covered(store, t1, "acct:A", "acct:B", usd("150.00"))
    print(f"{t1} read A,B (SIREAD on both), wrote A = -50.00; readers of A = {_set(sorted(store.readers_of('acct:A')))}")
    withdraw_if_jointly_covered(store, t2, "acct:B", "acct:A", usd("150.00"))
    print(f"{t2} read A,B, wrote B = -50.00")
    print(f"edges: {t2} -rw-> {t1} ({t2} read A, {t1} writes A); {t1} -rw-> {t2} ({t1} read B, {t2} writes B)")
    print(f"  {t1} in={_set(t1.in_conflicts)} out={_set(t1.out_conflicts)} pivot={_b(t1.is_pivot())}")
    print(f"  {t2} in={_set(t2.in_conflicts)} out={_set(t2.out_conflicts)} pivot={_b(t2.is_pivot())}")
    print(try_commit(store, t1))
    print(f"after the abort, {t2} in={_set(t2.in_conflicts)} out={_set(t2.out_conflicts)}")
    print(try_commit(store, t2))
    audit = store.begin()
    a, b = balance(store, audit, "acct:A"), balance(store, audit, "acct:B")
    print(f"A = {a}, B = {b}, A+B = {a.plus(b)}  <- constraint holds")
    store.commit(audit)

    print()
    print("== 2. One rw-antidependency alone is harmless ==")
    reader = store.begin()
    writer = store.begin()
    print(f"{reader} reads A = {balance(store, reader, 'acct:A')}")
    store.write(writer, "acct:A", usd("75.00"))
    print(f"{writer} writes A = 75.00; edge {reader} -rw-> {writer}")
    print(try_commit(store, writer))
    print(f"{try_commit(store, reader)}  (serializable order: {reader} before {writer})")

    print()
    print("== 3. SIREAD locks outlive their transaction ==")
    early = store.begin()
    late = store.begin()
    seen = balance(store, early, "acct:B")
    print(f"{early} reads B = {seen} and commits: {try_commit(store, early)}")
    print(f"readers of B still recorded: {_set(sorted(store.readers_of('acct:B')))} "
          f"(retained locks = {store.retained_read_locks()})")
    store.write(late, "acct:B", usd("1.00"))
    print(f"{late} (started before {early} committed) writes B: edge {early} -rw-> {late}; "
          f"{late} in={_set(late.in_conflicts)}")
    print(f"{try_commit(store, late)}  (one edge, no pivot)")
    released = store.release_finished()
    print(f"released {released} finished transactions; retained locks now {store.retained_read_locks()}")

    print()
    print("== 4. The pivot has already committed: abort the newcomer on the spot ==")
    p = store.begin()  # will be the pivot
    q = store.begin()  # concurrent with p
    balance(store, p, "acct:A")  # p reads A
    store.write(p, "acct:B", usd("5.00"))  # p writes B
    print(f"{p} reads A, writes B, commits: {try_commit(store, p)}")
    print(f"{q} reads B (older version than {p} wrote): edge {q} -rw-> {p}")
    balance(store, q, "acct:B")
    try:
        store.write(q, "acct:A", usd("6.00"))
        print(f"{q} wrote A")
    except SerializationFailureException as e:
        print(f"{q} tries to write A, which {p} read: ABORTED immediately: {e}")
    print(f"commits = {store.commits()}, aborts = {store.aborts()}")


if __name__ == "__main__":
    main()
