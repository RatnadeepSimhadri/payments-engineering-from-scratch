"""Lesson: phases/07-storage-and-transactions/04-isolation-levels-and-anomalies/docs/en.md
Isolation Levels and Their Anomalies
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: ANSI/ISO SQL-92 (ISO/IEC 9075:1992) section 4.28; Berenson, Bernstein, Gray, Melton, O'Neil, O'Neil,
"A Critique of ANSI SQL Isolation Levels" (SIGMOD 1995). Python 3.11+, standard library only. Same design as Java.
"""

from __future__ import annotations

from dataclasses import dataclass
from enum import Enum
from typing import Callable, TypeVar

T = TypeVar("T")


class Isolation(Enum):
    """The five levels this store can run at. The first three are the lock-based ANSI levels; SNAPSHOT is Berenson's SI."""

    READ_UNCOMMITTED = "READ_UNCOMMITTED"
    READ_COMMITTED = "READ_COMMITTED"
    REPEATABLE_READ = "REPEATABLE_READ"
    SNAPSHOT = "SNAPSHOT"
    SERIALIZABLE = "SERIALIZABLE"


class ConflictException(Exception):
    """Raised when an operation would conflict with another live transaction. The receiver is aborted.

    Deliberately not a RuntimeError, so it can never be mistaken for the "transaction is not active" error.
    """


def _j(v: object) -> str:
    """Render a value the way Java's string concatenation does (None -> null) so both demos print the same trace."""
    return "null" if v is None else str(v)


def _jmap(m: dict[str, int]) -> str:
    """Render a sorted map like Java's TreeMap.toString: {k=v, k=v}."""
    return "{" + ", ".join(f"{k}={v}" for k, v in m.items()) + "}"


def _with_prefix(m: dict[str, int], prefix: str) -> dict[str, int]:
    """All entries whose key starts with prefix, in key order: TreeMap.tailMap(prefix) up to the first non-match."""
    return {k: m[k] for k in sorted(m) if k.startswith(prefix)}


class LockEntry:
    """Per-key lock state. One exclusive holder, or any number of shared holders."""

    def __init__(self) -> None:
        self.shared: set[int] = set()
        self.exclusive: int | None = None

    def held_by_others(self, me: int) -> bool:
        if self.exclusive is not None and self.exclusive != me:
            return True
        return any(s != me for s in self.shared)


class Store:
    """A single-version key-value store (balances in minor units) with transactions.

    No MVCC: exactly one committed value per key. Isolation comes from locks (no-wait: a conflict aborts the
    requester instead of blocking) or, at SNAPSHOT, from a private copy taken at begin plus first-committer-wins.
    """

    def __init__(self) -> None:
        self.committed: dict[str, int] = {}
        self.version_of: dict[str, int] = {}  # key -> commit number that last wrote it
        self.locks: dict[str, LockEntry] = {}
        self.predicate_locks: dict[str, set[int]] = {}  # prefix -> txns that scanned it (SERIALIZABLE)
        self.commit_clock = 0
        self._next_tx_id = 1
        self.trace: list[str] = []

    def load(self, key: str, value: int) -> None:
        self.committed[key] = value
        self.version_of[key] = 0

    def begin(self, level: Isolation) -> "Txn":
        t = Txn(self, self._next_tx_id, level)
        self._next_tx_id += 1
        self.trace.append(f"T{t.id} begin {level.name}")
        return t

    # ---- lock table (no-wait) ----

    def lock_shared(self, tx: int, key: str) -> None:
        e = self.locks.setdefault(key, LockEntry())
        if e.exclusive is not None and e.exclusive != tx:
            raise ConflictException(f"T{tx} S-lock {key} blocked by X of T{e.exclusive}")
        e.shared.add(tx)

    def lock_exclusive(self, tx: int, key: str) -> None:
        e = self.locks.setdefault(key, LockEntry())
        if e.held_by_others(tx):
            raise ConflictException(f"T{tx} X-lock {key} blocked by {Store._describe_holders(e, tx)}")
        e.shared.discard(tx)
        e.exclusive = tx

    def lock_predicate(self, tx: int, prefix: str) -> None:
        # conflict if another transaction holds X on any existing key under this prefix
        for key, e in self.locks.items():
            if key.startswith(prefix) and e.exclusive is not None and e.exclusive != tx:
                raise ConflictException(f"T{tx} predicate {prefix}* blocked by X of T{e.exclusive} on {key}")
        self.predicate_locks.setdefault(prefix, set()).add(tx)

    def check_predicate_conflict(self, tx: int, key: str) -> None:
        for prefix, holders in self.predicate_locks.items():
            if key.startswith(prefix):
                for holder in sorted(holders):
                    if holder != tx:
                        raise ConflictException(f"T{tx} write {key} blocked by predicate lock {prefix}* of T{holder}")

    def release_all(self, tx: int) -> None:
        for e in self.locks.values():
            e.shared.discard(tx)
            if e.exclusive == tx:
                e.exclusive = None
        self.locks = {k: e for k, e in self.locks.items() if e.exclusive is not None or e.shared}
        for holders in self.predicate_locks.values():
            holders.discard(tx)
        self.predicate_locks = {p: h for p, h in self.predicate_locks.items() if h}

    @staticmethod
    def _describe_holders(e: LockEntry, me: int) -> str:
        if e.exclusive is not None and e.exclusive != me:
            return f"X of T{e.exclusive}"
        others = sorted(s for s in e.shared if s != me)
        return f"S of T{others}"

    def committed_with_prefix(self, prefix: str) -> dict[str, int]:
        return _with_prefix(self.committed, prefix)


class Txn:
    class Status(Enum):
        ACTIVE = "ACTIVE"
        COMMITTED = "COMMITTED"
        ABORTED = "ABORTED"

    def __init__(self, store: Store, id: int, level: Isolation) -> None:
        self.store = store
        self.id = id
        self.level = level
        self.status = Txn.Status.ACTIVE
        self.writes: dict[str, int] = {}  # buffered writes (all levels except READ_UNCOMMITTED)
        self.undo: dict[str, int | None] = {}  # READ_UNCOMMITTED: previous committed value for in-place writes
        # SNAPSHOT: private copy taken at begin
        self.snapshot: dict[str, int] | None = dict(store.committed) if level is Isolation.SNAPSHOT else None
        self.snapshot_version = store.commit_clock
        self.read_set: set[str] = set()

    def _require_active(self) -> None:
        if self.status is not Txn.Status.ACTIVE:
            raise RuntimeError(f"T{self.id} is {self.status.name}")

    def _guarded(self, op: Callable[[], T]) -> T:
        """Wraps an operation so a conflict aborts this transaction and releases its locks before propagating."""
        self._require_active()
        try:
            return op()
        except ConflictException as c:
            self.store.trace.append(f"  {c} -> T{self.id} ABORTED")
            self.abort()
            raise

    def read(self, key: str) -> int | None:
        def op() -> int | None:
            match self.level:
                case Isolation.READ_UNCOMMITTED:
                    v = self.store.committed.get(key)  # sees in-place writes of others
                case Isolation.READ_COMMITTED:
                    v = self.writes[key] if key in self.writes else self.store.committed.get(key)
                case Isolation.REPEATABLE_READ | Isolation.SERIALIZABLE:
                    if key not in self.writes:
                        self.store.lock_shared(self.id, key)
                    v = self.writes[key] if key in self.writes else self.store.committed.get(key)
                case Isolation.SNAPSHOT:
                    v = self.writes[key] if key in self.writes else self.snapshot.get(key)
            self.read_set.add(key)
            self.store.trace.append(f"  T{self.id} read {key} = {_j(v)}")
            return v

        return self._guarded(op)

    def scan(self, prefix: str) -> dict[str, int]:
        """All keys with the given prefix: a predicate read, the kind that phantoms attack."""

        def op() -> dict[str, int]:
            match self.level:
                case Isolation.SNAPSHOT:
                    base = _with_prefix(self.snapshot, prefix)
                case Isolation.SERIALIZABLE:
                    self.store.lock_predicate(self.id, prefix)
                    base = self.store.committed_with_prefix(prefix)
                    for k in base:
                        self.store.lock_shared(self.id, k)
                case Isolation.REPEATABLE_READ:
                    base = self.store.committed_with_prefix(prefix)
                    for k in base:
                        self.store.lock_shared(self.id, k)  # item locks only: new keys are unprotected
                case _:
                    base = self.store.committed_with_prefix(prefix)
            result = dict(base)
            if self.level is not Isolation.READ_UNCOMMITTED:
                for k, v in self.writes.items():
                    if k.startswith(prefix):
                        result[k] = v
            result = dict(sorted(result.items()))
            self.store.trace.append(f"  T{self.id} scan {prefix}* = {_jmap(result)}")
            return result

        return self._guarded(op)

    def write(self, key: str, value: int) -> None:
        def op() -> None:
            match self.level:
                case Isolation.READ_UNCOMMITTED:
                    self.store.lock_exclusive(self.id, key)  # still no dirty WRITES: X lock held to commit
                    self.undo.setdefault(key, self.store.committed.get(key))
                    self.store.committed[key] = value  # applied in place, visible to everyone now
                case Isolation.READ_COMMITTED | Isolation.REPEATABLE_READ:
                    self.store.lock_exclusive(self.id, key)
                    self.writes[key] = value
                case Isolation.SERIALIZABLE:
                    self.store.check_predicate_conflict(self.id, key)
                    self.store.lock_exclusive(self.id, key)
                    self.writes[key] = value
                case Isolation.SNAPSHOT:
                    self.writes[key] = value  # no locks; validated at commit
            self.store.trace.append(f"  T{self.id} write {key} = {value}")

        self._guarded(op)

    def commit(self) -> None:
        def op() -> None:
            s = self.store
            if self.level is Isolation.SNAPSHOT:
                for k in self.writes:
                    v = s.version_of.get(k, 0)
                    if v > self.snapshot_version:
                        raise ConflictException(
                            f"T{self.id} first-committer-wins: {k} committed at v{v} after snapshot v{self.snapshot_version}")
            s.commit_clock += 1
            if self.level is Isolation.READ_UNCOMMITTED:
                for k in self.undo:
                    s.version_of[k] = s.commit_clock
            else:
                for k, v in self.writes.items():
                    s.committed[k] = v
                    s.version_of[k] = s.commit_clock
            self.status = Txn.Status.COMMITTED
            s.release_all(self.id)
            s.trace.append(f"  T{self.id} COMMIT (v{s.commit_clock})")

        self._guarded(op)

    def abort(self) -> None:
        if self.status is not Txn.Status.ACTIVE:
            return
        if self.level is Isolation.READ_UNCOMMITTED:
            for k, old in self.undo.items():
                if old is None:
                    self.store.committed.pop(k, None)
                else:
                    self.store.committed[k] = old
        self.status = Txn.Status.ABORTED
        self.store.release_all(self.id)
        self.store.trace.append(f"  T{self.id} ABORT")


@dataclass(frozen=True)
class Outcome:
    anomaly: str
    level: Isolation
    observed: bool
    detail: str
    trace: list[str]


class AnomalyLab:
    """Each scenario is a fixed interleaving of two transactions that produces one anomaly when the level allows it."""

    ANOMALIES = ("dirty read", "non-repeatable read", "phantom", "lost update", "write skew")

    @staticmethod
    def run(anomaly: str, level: Isolation) -> Outcome:
        scripts = {
            "dirty read": AnomalyLab.dirty_read,
            "non-repeatable read": AnomalyLab.non_repeatable_read,
            "phantom": AnomalyLab.phantom,
            "lost update": AnomalyLab.lost_update,
            "write skew": AnomalyLab.write_skew,
        }
        if anomaly not in scripts:
            raise ValueError(anomaly)
        return scripts[anomaly](level)

    @staticmethod
    def _quietly(r: Callable[[], object]) -> None:
        """Scripts keep going after a conflict: the aborted transaction's later steps are skipped, as a retry loop would."""
        try:
            r()
        except ConflictException:
            pass  # already aborted and traced by _guarded()
        except RuntimeError:
            pass  # a later step of a transaction that was aborted earlier

    @staticmethod
    def dirty_read(level: Isolation) -> Outcome:
        """P1: T2 reads a value T1 wrote but then rolled back."""
        s = Store()
        s.load("acct:alice", 1000)
        t1, t2 = s.begin(level), s.begin(level)
        q = AnomalyLab._quietly
        q(lambda: t1.write("acct:alice", 900))  # alice pays 1.00, not yet committed
        seen: list[int | None] = [None]
        q(lambda: seen.__setitem__(0, t2.read("acct:alice")))
        t1.abort()  # the payment is declined and rolled back
        q(t2.commit)
        observed = seen[0] is not None and seen[0] == 900
        return Outcome("dirty read", level, observed,
                       f"T2 saw {_j(seen[0])}; committed value is {_j(s.committed.get('acct:alice'))}", s.trace)

    @staticmethod
    def non_repeatable_read(level: Isolation) -> Outcome:
        """P2: T1 reads the same key twice and gets different values because T2 committed in between."""
        s = Store()
        s.load("acct:alice", 1000)
        t1, t2 = s.begin(level), s.begin(level)
        q = AnomalyLab._quietly
        first: list[int | None] = [None]
        second: list[int | None] = [None]
        q(lambda: first.__setitem__(0, t1.read("acct:alice")))
        q(lambda: t2.write("acct:alice", 500))
        q(t2.commit)
        q(lambda: second.__setitem__(0, t1.read("acct:alice")))
        q(t1.commit)
        observed = first[0] is not None and second[0] is not None and first[0] != second[0]
        return Outcome("non-repeatable read", level, observed, f"T1 read {_j(first[0])} then {_j(second[0])}", s.trace)

    @staticmethod
    def phantom(level: Isolation) -> Outcome:
        """P3: T1 runs the same predicate query twice; T2 inserted a matching row in between."""
        s = Store()
        s.load("posting:m42:001", 500)
        s.load("posting:m42:002", 700)
        t1, t2 = s.begin(level), s.begin(level)
        q = AnomalyLab._quietly
        counts = [0, 0]
        q(lambda: counts.__setitem__(0, len(t1.scan("posting:m42:"))))
        q(lambda: t2.write("posting:m42:003", 300))  # a new posting for the same merchant
        q(t2.commit)
        q(lambda: counts.__setitem__(1, len(t1.scan("posting:m42:"))))
        q(t1.commit)
        observed = t1.status is not Txn.Status.ABORTED and counts[0] != counts[1]
        return Outcome("phantom", level, observed, f"T1 counted {counts[0]} then {counts[1]} postings", s.trace)

    @staticmethod
    def lost_update(level: Isolation) -> Outcome:
        """P4: two read-modify-write deposits; one increment vanishes."""
        s = Store()
        s.load("acct:alice", 1000)
        t1, t2 = s.begin(level), s.begin(level)
        q = AnomalyLab._quietly
        r1: list[int | None] = [None]
        r2: list[int | None] = [None]
        q(lambda: r1.__setitem__(0, t1.read("acct:alice")))
        q(lambda: r2.__setitem__(0, t2.read("acct:alice")))
        q(lambda: t1.write("acct:alice", r1[0] + 100))  # deposit 1.00
        q(t1.commit)
        q(lambda: t2.write("acct:alice", r2[0] + 200) if r2[0] is not None else None)  # deposit 2.00
        q(t2.commit)
        final_balance = s.committed["acct:alice"]
        both_committed = t1.status is Txn.Status.COMMITTED and t2.status is Txn.Status.COMMITTED
        observed = both_committed and final_balance != 1300
        return Outcome("lost update", level, observed,
                       f"final balance {final_balance} (expected 1300 if both deposits landed); "
                       f"T1 {t1.status.name}, T2 {t2.status.name}", s.trace)

    @staticmethod
    def write_skew(level: Isolation) -> Outcome:
        """A5B: each transaction checks an invariant over two keys and writes a different key; the invariant breaks."""
        s = Store()
        s.load("acct:alice:checking", 600)
        s.load("acct:alice:savings", 600)
        t1, t2 = s.begin(level), s.begin(level)
        q = AnomalyLab._quietly
        c = [0, 0]
        sv = [0, 0]

        def check(i: int, t: Txn) -> None:
            c[i] = t.read("acct:alice:checking")
            sv[i] = t.read("acct:alice:savings")

        q(lambda: check(0, t1))  # T1 checks: 1200 >= 1000
        q(lambda: check(1, t2))  # T2 checks: 1200 >= 1000
        q(lambda: t1.write("acct:alice:checking", c[0] - 1000) if c[0] + sv[0] - 1000 >= 0 else None)
        q(lambda: t2.write("acct:alice:savings", sv[1] - 1000) if c[1] + sv[1] - 1000 >= 0 else None)
        q(t1.commit)
        q(t2.commit)
        total = s.committed["acct:alice:checking"] + s.committed["acct:alice:savings"]
        observed = total < 0
        return Outcome("write skew", level, observed,
                       f"combined balance {total} (invariant: >= 0); T1 {t1.status.name}, T2 {t2.status.name}", s.trace)

    @staticmethod
    def matrix() -> dict[str, dict[Isolation, bool]]:
        """anomaly -> level -> observed?"""
        return {a: {lvl: AnomalyLab.run(a, lvl).observed for lvl in Isolation} for a in AnomalyLab.ANOMALIES}


def main() -> None:
    print("== Which anomalies does each level allow? (Y = anomaly observed) ==")
    print(f"{'anomaly':<20} {'READ_UNCOMMITTED':<18} {'READ_COMMITTED':<16} {'REPEATABLE_READ':<16} "
          f"{'SNAPSHOT':<10} {'SERIALIZABLE':<12}")
    widths = [18, 16, 16, 10, 12]
    for anomaly, row in AnomalyLab.matrix().items():
        line = f"{anomaly:<20} "
        for w, b in zip(widths, row.values()):
            line += f"{'Y' if b else '-':<{w}} "
        print(line.rstrip())

    def show(title: str, outcome: Outcome) -> None:
        print()
        print(title)
        for line in outcome.trace:
            print(line)
        print(f"  => {outcome.detail}")

    show("== Write skew under SNAPSHOT: both pass the check, both commit, the invariant is gone ==",
         AnomalyLab.write_skew(Isolation.SNAPSHOT))
    show("== Same interleaving under SERIALIZABLE: the second writer is refused ==",
         AnomalyLab.write_skew(Isolation.SERIALIZABLE))
    show("== Phantom under REPEATABLE_READ (item locks cannot lock a row that does not exist yet) ==",
         AnomalyLab.phantom(Isolation.REPEATABLE_READ))
    show("== Lost update under SNAPSHOT: first committer wins, second is told ==",
         AnomalyLab.lost_update(Isolation.SNAPSHOT))


if __name__ == "__main__":
    main()
