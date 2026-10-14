"""Lesson: phases/07-storage-and-transactions/11-change-data-capture/docs/en.md
Change Data Capture: turn a database commit log into an ordered change stream with LSNs, consumer offsets,
snapshot-then-stream bootstrap, and an idempotent sink.
Run:  python3 main.py
Test: python3 -m unittest -v
Python 3.11+, standard library only. Event envelope follows the Debezium change-event shape (op c/u/d/r, before, after).
"""

from __future__ import annotations

from dataclasses import dataclass, field
from enum import Enum
from types import MappingProxyType
from typing import Mapping, Protocol

INT64_MIN, INT64_MAX = -(1 << 63), (1 << 63) - 1


@dataclass(frozen=True)
class Currency:
    """ISO 4217 currency: three-letter code and minor-unit exponent. Copied from Phase 10, Lesson 01."""

    code: str
    exponent: int

    def __post_init__(self) -> None:
        if self.code is None or len(self.code) != 3 or not all(c.isupper() for c in self.code):
            raise ValueError(f"currency code must be three uppercase letters: {self.code}")
        if self.exponent < 0 or self.exponent > 4:
            raise ValueError(f"exponent must be 0..4: {self.exponent}")

    def minor_units_per_major(self) -> int:
        return 10 ** self.exponent


Currency.USD = Currency("USD", 2)


def _exact(r: int) -> int:
    """Math.addExact / negateExact: Python ints never overflow, so the signed 64-bit range is checked by hand."""
    if r < INT64_MIN or r > INT64_MAX:
        raise OverflowError("long overflow")
    return r


@dataclass(frozen=True)
class Money:
    """Integer minor units plus a currency. Never a float."""

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
        if self.currency != other.currency:
            raise ValueError(f"cannot combine {self.currency.code} with {other.currency.code}")
        return Money(_exact(self.minor_units + other.minor_units), self.currency)

    def minus(self, other: "Money") -> "Money":
        return self.plus(Money(_exact(-other.minor_units), self.currency))

    def __str__(self) -> str:
        scale = self.currency.minor_units_per_major()
        a = abs(self.minor_units)
        sign = "-" if self.minor_units < 0 else ""
        if self.currency.exponent == 0:
            return f"{sign}{a // scale} {self.currency.code}"
        return f"{sign}{a // scale}.{a % scale:0{self.currency.exponent}d} {self.currency.code}"


class Clock(Protocol):
    """Injected time source so demos and tests are deterministic."""

    def now_millis(self) -> int: ...


class FakeClock:
    def __init__(self, start: int) -> None:
        self._now = start

    def now_millis(self) -> int:
        return self._now

    def advance(self, millis: int) -> None:
        self._now += millis


class Op(Enum):
    """The kind of row change a log record describes."""

    INSERT = "INSERT"
    UPDATE = "UPDATE"
    DELETE = "DELETE"


def _frozen(m: Mapping[str, str] | None) -> Mapping[str, str] | None:
    """Copy then wrap read-only: Collections.unmodifiableMap(new LinkedHashMap<>(m))."""
    return None if m is None else MappingProxyType(dict(m))


@dataclass(frozen=True)
class LogRecord:
    """One commit-log entry. The LSN is assigned at commit, strictly increasing, and the only ordering CDC uses.

    before is None for INSERT, after is None for DELETE. Amount columns carry integer minor units as strings.
    """

    lsn: int
    tx_id: int
    op: Op
    table: str
    key: str
    before: Mapping[str, str] | None
    after: Mapping[str, str] | None

    def __post_init__(self) -> None:
        if self.table is None or self.key is None:
            raise TypeError("table and key are required")
        object.__setattr__(self, "before", _frozen(self.before))
        object.__setattr__(self, "after", _frozen(self.after))


class LogTruncatedException(RuntimeError):
    """Raised when a reader asks for an LSN the log has already truncated: the reader must re-snapshot."""

    def __init__(self, requested: int, oldest_available: int) -> None:
        super().__init__(f"LSN {requested} has been truncated; oldest available is {oldest_available}")
        self.requested = requested
        self.oldest_available = oldest_available


class CommitLog:
    """Append-only commit log with retention. read_from returns records with LSN strictly greater than the offset."""

    def __init__(self) -> None:
        self._records: list[LogRecord] = []
        self._truncated_below = 1  # smallest LSN still present
        self._next_lsn = 1

    def append(self, tx_id: int, op: Op, table: str, key: str, before, after) -> int:
        r = LogRecord(self._next_lsn, tx_id, op, table, key, before, after)
        self._next_lsn += 1
        self._records.append(r)
        return r.lsn

    def head_lsn(self) -> int:
        """LSN of the newest record, or 0 if the log is empty."""
        return self._next_lsn - 1

    def oldest_lsn(self) -> int:
        return self._truncated_below

    def size(self) -> int:
        return len(self._records)

    def read_from(self, after_lsn: int, max: int) -> list[LogRecord]:
        """Records with lsn > after_lsn, oldest first, at most max."""
        if after_lsn + 1 < self._truncated_below:
            raise LogTruncatedException(after_lsn + 1, self._truncated_below)
        out: list[LogRecord] = []
        for r in self._records:
            if r.lsn > after_lsn:
                out.append(r)
                if len(out) >= max:
                    break
        return out

    def truncate_before(self, keep_from_lsn: int) -> int:
        """Drop every record with lsn < keep_from_lsn. Models WAL recycling / binlog purge."""
        before = len(self._records)
        self._records = [r for r in self._records if r.lsn >= keep_from_lsn]
        self._truncated_below = max(self._truncated_below, keep_from_lsn)
        return before - len(self._records)


class Database:
    """Tables of string-column rows plus a commit log; a transaction's writes are applied and logged at commit."""

    def __init__(self) -> None:
        self._tables: dict[str, dict[str, dict[str, str]]] = {}
        self._log = CommitLog()
        self._next_tx_id = 1

    def log(self) -> CommitLog:
        return self._log

    def read(self, table: str, key: str) -> dict[str, str] | None:
        t = self._tables.get(table)
        return None if t is None else t.get(key)

    def scan(self, table: str) -> dict[str, dict[str, str]]:
        """Snapshot of one table: a copy of every row, in key order."""
        t = self._tables.get(table, {})
        return {k: dict(t[k]) for k in sorted(t)}

    def begin(self) -> "Transaction":
        tx = Transaction(self, self._next_tx_id)
        self._next_tx_id += 1
        return tx

    def commit(self, tx: "Transaction") -> int:
        """Called by Transaction.commit: applies the buffered writes to the tables and appends them to the log."""
        last_lsn = 0
        for w in tx.writes:
            t = self._tables.setdefault(w.table, {})
            before = t.get(w.key)
            if w.after is None:
                if before is None:
                    continue  # deleting a missing row logs nothing
                del t[w.key]
                op = Op.DELETE
            else:
                t[w.key] = dict(w.after)
                op = Op.INSERT if before is None else Op.UPDATE
            last_lsn = self._log.append(tx.tx_id, op, w.table, w.key, before, w.after)
        return last_lsn


class Transaction:
    """Buffers writes until commit. Reads inside the transaction see the database state, not the buffer."""

    @dataclass(frozen=True)
    class Write:
        table: str
        key: str
        after: dict[str, str] | None

    def __init__(self, db: Database, tx_id: int) -> None:
        self._db = db
        self.tx_id = tx_id
        self.writes: list[Transaction.Write] = []
        self._done = False

    def put(self, table: str, key: str, columns: Mapping[str, str]) -> "Transaction":
        self._require_open()
        self.writes.append(Transaction.Write(table, key, dict(columns)))
        return self

    def delete(self, table: str, key: str) -> "Transaction":
        self._require_open()
        self.writes.append(Transaction.Write(table, key, None))
        return self

    def commit(self) -> int:
        """Returns the LSN of the last record this transaction wrote, or 0 if it wrote nothing."""
        self._require_open()
        self._done = True
        return self._db.commit(self)

    def rollback(self) -> None:
        self._require_open()
        self._done = True
        self.writes.clear()

    def _require_open(self) -> None:
        if self._done:
            raise RuntimeError(f"transaction {self.tx_id} is finished")


@dataclass(frozen=True)
class ChangeEvent:
    """Debezium-shaped change event: op "c", "u", "d" or "r" (snapshot read). lsn is the position in the source log."""

    op: str
    table: str
    key: str
    before: Mapping[str, str] | None
    after: Mapping[str, str] | None
    lsn: int
    tx_id: int
    ts_millis: int
    snapshot: bool

    @staticmethod
    def from_log(r: LogRecord, ts_millis: int) -> "ChangeEvent":
        op = {Op.INSERT: "c", Op.UPDATE: "u", Op.DELETE: "d"}[r.op]
        return ChangeEvent(op, r.table, r.key, r.before, r.after, r.lsn, r.tx_id, ts_millis, False)

    @staticmethod
    def snapshot_row(table: str, key: str, row: Mapping[str, str], snapshot_lsn: int, ts_millis: int) -> "ChangeEvent":
        return ChangeEvent("r", table, key, None, row, snapshot_lsn, 0, ts_millis, True)


class OffsetStore:
    """Durable store of consumer offsets, one per consumer name."""

    def __init__(self) -> None:
        self._offsets: dict[str, int] = {}
        self.commits = 0

    def load(self, consumer: str) -> int:
        return self._offsets.get(consumer, 0)

    def save(self, consumer: str, lsn: int) -> None:
        self._offsets[consumer] = lsn
        self.commits += 1


class Sink(Protocol):
    """Something that receives change events. Must tolerate seeing the same event more than once."""

    def apply(self, event: ChangeEvent) -> None: ...


class CdcConnector:
    """Reads the log from a stored offset, hands events to the sink, and commits the offset only after apply.

    A crash between apply and commit means the batch is delivered again on restart: at-least-once.
    """

    def __init__(self, name: str, db: Database, offsets: OffsetStore, sink: Sink, clock: Clock) -> None:
        self.name = name
        self._db = db
        self._offsets = offsets
        self._sink = sink
        self._clock = clock
        self.events_delivered = 0

    def offset(self) -> int:
        return self._offsets.load(self.name)

    def snapshot(self, table: str) -> int:
        """Record the log head as the snapshot LSN, emit every row as an "r" event, then store that LSN as offset."""
        snapshot_lsn = self._db.log().head_lsn()
        for key, row in self._db.scan(table).items():
            self._deliver(ChangeEvent.snapshot_row(table, key, row, snapshot_lsn, self._clock.now_millis()))
        self._offsets.save(self.name, snapshot_lsn)
        return snapshot_lsn

    def poll(self, max_events: int, crash_before_commit: bool = False) -> int:
        """Stream one batch; commit the offset after the whole batch is applied unless crash_before_commit."""
        frm = self._offsets.load(self.name)
        batch = self._db.log().read_from(frm, max_events)
        if not batch:
            return 0
        last = frm
        for r in batch:
            self._deliver(ChangeEvent.from_log(r, self._clock.now_millis()))
            last = r.lsn
        if not crash_before_commit:
            self._offsets.save(self.name, last)
        return len(batch)

    def _deliver(self, e: ChangeEvent) -> None:
        self._sink.apply(e)
        self.events_delivered += 1


class BalanceViewSink:
    """Idempotent materialized view of the accounts table; ignores any event at or below the LSN applied per key."""

    def __init__(self) -> None:
        self._balances: dict[str, Money] = {}
        self._applied_lsn: dict[str, int] = {}
        self.applied = 0
        self.skipped = 0

    def apply(self, e: ChangeEvent) -> None:
        if e.table != "accounts":
            return
        seen = self._applied_lsn.get(e.key, 0)
        if e.lsn <= seen:
            self.skipped += 1
            return
        if e.op == "d":
            self._balances.pop(e.key, None)
        else:
            self._balances[e.key] = Money.of_minor(int(e.after["balance_minor"]), Currency.USD)
        self._applied_lsn[e.key] = e.lsn
        self.applied += 1

    def balance(self, account_id: str) -> Money | None:
        return self._balances.get(account_id)

    def size(self) -> int:
        return len(self._balances)

    def all(self) -> Mapping[str, Money]:
        """Read-only, key-ordered view (Java: unmodifiable TreeMap)."""
        return MappingProxyType(dict(sorted(self._balances.items())))


class NaiveTotalsSink:
    """Counts and sums payments as they arrive. Deliberately NOT idempotent: redelivery double-counts."""

    def __init__(self) -> None:
        self.payments_seen = 0
        self.total = Money.of_minor(0, Currency.USD)

    def apply(self, e: ChangeEvent) -> None:
        if e.table != "payments" or e.op != "c":
            return
        self.payments_seen += 1
        self.total = self.total.plus(Money.of_minor(int(e.after["amount_minor"]), Currency.USD))


class IdempotentTotalsSink:
    """Same totals, made idempotent by remembering which payment keys have been counted."""

    def __init__(self) -> None:
        self._counted: set[str] = set()
        self.total = Money.of_minor(0, Currency.USD)

    def apply(self, e: ChangeEvent) -> None:
        if e.table != "payments" or e.op not in ("c", "r"):
            return
        if e.key in self._counted:
            return
        self._counted.add(e.key)
        self.total = self.total.plus(Money.of_minor(int(e.after["amount_minor"]), Currency.USD))

    def payments_seen(self) -> int:
        return len(self._counted)


@dataclass
class RecordingSink:
    """Records every event it sees, for assertions on order and envelope content."""

    events: list[ChangeEvent] = field(default_factory=list)

    def apply(self, e: ChangeEvent) -> None:
        self.events.append(e)


def account(balance_minor: int, version: int) -> dict[str, str]:
    return {"balance_minor": str(balance_minor), "currency": "USD", "version": str(version)}


def payment(frm: str, to: str, amount_minor: int) -> dict[str, str]:
    return {"from": frm, "to": to, "amount_minor": str(amount_minor), "currency": "USD"}


def transfer(db: Database, payment_id: str, frm: str, to: str, amount_minor: int) -> int:
    """Move money between two accounts in one transaction: one payments row, two account updates, one commit."""
    from_bal = int(db.read("accounts", frm)["balance_minor"])
    to_bal = int(db.read("accounts", to)["balance_minor"])
    from_ver = int(db.read("accounts", frm)["version"])
    to_ver = int(db.read("accounts", to)["version"])
    return (db.begin()
            .put("payments", payment_id, payment(frm, to, amount_minor))
            .put("accounts", frm, account(from_bal - amount_minor, from_ver + 1))
            .put("accounts", to, account(to_bal + amount_minor, to_ver + 1))
            .commit())


def _jmap(m: Mapping) -> str:
    """Render a map like Java's AbstractMap.toString: {k=v, k=v}."""
    return "{" + ", ".join(f"{k}={v}" for k, v in m.items()) + "}"


def _image(m: Mapping[str, str] | None) -> str:
    if m is None:
        return "null"
    return m["balance_minor"] if m.get("balance_minor") is not None else _jmap(m)


def describe(e: ChangeEvent) -> str:
    return f"lsn={e.lsn} tx={e.tx_id} op={e.op} {e.table}/{e.key} before={_image(e.before)} after={_image(e.after)}"


def main() -> None:
    clock = FakeClock(1_700_000_000_000)
    db = Database()

    print("== 1. Transactions become log records with strictly increasing LSNs ==")
    db.begin().put("accounts", "acc_alice", account(10_000, 1)).put("accounts", "acc_bob", account(5_000, 1)).commit()
    transfer(db, "pay_001", "acc_alice", "acc_bob", 2_500)
    for r in db.log().read_from(0, 100):
        print(f"  {describe(ChangeEvent.from_log(r, clock.now_millis()))}")

    print()
    print("== 2. Snapshot, then stream from the snapshot LSN ==")
    offsets = OffsetStore()
    view = BalanceViewSink()
    connector = CdcConnector("balance-view", db, offsets, view, clock)
    snap_lsn = connector.snapshot("accounts")
    print(f"  snapshot at lsn={snap_lsn} delivered {view.applied} rows; view: {_jmap(view.all())}")
    clock.advance(10)
    transfer(db, "pay_002", "acc_bob", "acc_alice", 1_000)  # committed after the snapshot
    n = connector.poll(100)
    print(f"  streamed {n} events after lsn={snap_lsn}; offset now {connector.offset()}; view: {_jmap(view.all())}")
    print(f"  database says alice={db.read('accounts', 'acc_alice')['balance_minor']} "
          f"bob={db.read('accounts', 'acc_bob')['balance_minor']}")

    print()
    print("== 3. Crash after apply, before offset commit: at-least-once redelivery ==")
    clock.advance(10)
    transfer(db, "pay_003", "acc_alice", "acc_bob", 300)
    transfer(db, "pay_004", "acc_alice", "acc_bob", 700)
    naive = NaiveTotalsSink()
    exact = IdempotentTotalsSink()
    offsets2 = OffsetStore()
    naive_conn = CdcConnector("naive-totals", db, offsets2, naive, clock)
    exact_conn = CdcConnector("exact-totals", db, offsets2, exact, clock)
    naive_conn.poll(6, True)  # applies lsn 1..6, dies before saving the offset
    exact_conn.poll(6, True)
    print(f"  after crash: naive total={naive.total} ({naive.payments_seen} payments), exact total={exact.total} "
          f"({exact.payments_seen()} payments), offsets still {naive_conn.offset()}/{exact_conn.offset()}")
    naive_conn.poll(100)  # restart: replays 1..6 then continues
    exact_conn.poll(100)
    print(f"  after restart: naive total={naive.total} ({naive.payments_seen} payments)  <- double counted")
    print(f"                 exact total={exact.total} ({exact.payments_seen()} payments)  <- idempotent by payment key")
    print(f"  true total of pay_001..pay_004 = {Money.of_minor(2_500 + 1_000 + 300 + 700, Currency.USD)}")

    print()
    print("== 4. The view catches up and the redelivered events are skipped by LSN ==")
    n = connector.poll(100)
    print(f"  streamed {n} events; view applied={view.applied} skipped={view.skipped}; view: {_jmap(view.all())}")
    transfer(db, "pay_005", "acc_bob", "acc_alice", 1_500)
    connector.poll(3, True)  # applies the three records of pay_005, dies before committing the offset
    redelivered = connector.poll(100)
    print(f"  crash/replay: {redelivered} events redelivered; view applied={view.applied} skipped={view.skipped}; "
          f"view: {_jmap(view.all())}")

    print()
    print("== 5. Log retention: an offset older than the log forces a re-snapshot ==")
    late_offsets = OffsetStore()
    late_view = BalanceViewSink()
    late = CdcConnector("late-consumer", db, late_offsets, late_view, clock)
    dropped = db.log().truncate_before(8)
    print(f"  truncated {dropped} records; oldest lsn now {db.log().oldest_lsn()}; late consumer offset is {late.offset()}")
    try:
        late.poll(100)
    except LogTruncatedException as e:
        print(f"  poll failed: {e}")
    resnap = late.snapshot("accounts")
    late.poll(100)
    print(f"  re-snapshot at lsn={resnap} then stream; late view: {_jmap(late_view.all())}")
    print(f"  both views agree: {'true' if late_view.all() == view.all() else 'false'}")


if __name__ == "__main__":
    main()
