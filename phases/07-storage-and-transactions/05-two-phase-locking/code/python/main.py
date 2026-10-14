"""Lesson: phases/07-storage-and-transactions/05-two-phase-locking/docs/en.md
Two-Phase Locking and Deadlock Detection
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: Eswaran, Gray, Lorie, Traiger, "The Notions of Consistency and Predicate Locks in a Database System"
(CACM 1976); Gray & Reuter, Transaction Processing (1993) ch. 7-8. Python 3.11+, standard library only.
"""

from __future__ import annotations

from collections import deque
from dataclasses import dataclass
from enum import Enum

# ---------------------------------------------------------------------------------------------
# Money (Phase 10, Lesson 01 design): integer minor units plus a currency. Never a float.
# ---------------------------------------------------------------------------------------------

INT64_MIN, INT64_MAX = -(1 << 63), (1 << 63) - 1


def add_exact(a: int, b: int) -> int:
    """Java's Math.addExact on long: Python ints never overflow, so the 64-bit range is checked by hand."""
    r = a + b
    if r < INT64_MIN or r > INT64_MAX:
        raise OverflowError("long overflow")
    return r


@dataclass(frozen=True)
class Currency:
    code: str
    exponent: int

    def __post_init__(self) -> None:
        if self.code is None or len(self.code) != 3:
            raise ValueError(f"currency code: {self.code}")
        if self.exponent < 0 or self.exponent > 4:
            raise ValueError(f"exponent: {self.exponent}")


Currency.USD = Currency("USD", 2)


@dataclass(frozen=True)
class Money:
    minor_units: int
    currency: Currency

    def __post_init__(self) -> None:
        if not isinstance(self.minor_units, int) or isinstance(self.minor_units, bool):
            raise TypeError("minor_units must be an int; never construct Money from a float")
        if self.currency is None:
            raise TypeError("currency must not be None")

    @staticmethod
    def of_minor(minor_units: int, currency: Currency) -> "Money":
        return Money(minor_units, currency)

    def plus(self, other: "Money") -> "Money":
        if self.currency != other.currency:
            raise ValueError("currency mismatch")
        return Money(add_exact(self.minor_units, other.minor_units), self.currency)

    def __str__(self) -> str:
        scale = 10 ** self.currency.exponent
        whole, frac = divmod(abs(self.minor_units), scale)
        sign = "-" if self.minor_units < 0 else ""
        if self.currency.exponent == 0:
            return f"{sign}{whole} {self.currency.code}"
        return f"{sign}{whole}.{frac:0{self.currency.exponent}d} {self.currency.code}"


# ---------------------------------------------------------------------------------------------
# Lock manager: shared/exclusive locks per key, FIFO wait queues, and the wait-for graph.
# ---------------------------------------------------------------------------------------------


class LockMode(Enum):
    S = "S"
    X = "X"

    def compatible_with(self, other: "LockMode") -> bool:
        """Compatibility matrix: S/S is the only pair that can coexist."""
        return self is LockMode.S and other is LockMode.S


class TwoPhaseViolationException(RuntimeError):
    """Raised when a transaction requests a lock after it has released one: the two-phase rule."""


@dataclass(frozen=True)
class LockRequest:
    tx: int
    mode: LockMode


class LockEntry:
    """Per-key state: who holds the lock in which mode, and who is queued behind them in arrival order."""

    def __init__(self) -> None:
        self.holders: dict[int, LockMode] = {}  # insertion-ordered, like LinkedHashMap
        self.queue: deque[LockRequest] = deque()

    def compatible(self, tx: int, mode: LockMode) -> bool:
        """Could tx hold mode alongside every other current holder? Self is ignored (upgrades)."""
        return all(h == tx or mode.compatible_with(m) for h, m in self.holders.items())

    def is_empty(self) -> bool:
        return not self.holders and not self.queue


class LockManager:
    def __init__(self, log: list[str]) -> None:
        self.table: dict[str, LockEntry] = {}
        self.log = log

    def _keys(self) -> list[str]:
        return sorted(self.table)  # TreeMap iteration order

    def acquire(self, tx: int, key: str, mode: LockMode) -> bool:
        """Request a lock. True if granted now, False if queued and the caller must wait.

        FIFO: a request behind a queued request waits even if it would be compatible with the current holders,
        so a stream of readers cannot starve a writer. An upgrade (S held, X wanted) is queued at the front.
        """
        e = self.table.setdefault(key, LockEntry())
        held = e.holders.get(tx)
        if held is LockMode.X or (held is LockMode.S and mode is LockMode.S):
            return True  # already covered
        upgrade = held is LockMode.S and mode is LockMode.X
        if (not e.queue or upgrade) and e.compatible(tx, mode):
            e.holders[tx] = mode
            self.log.append(f"  T{tx} {mode.name}({key}) granted")
            return True
        if upgrade:
            e.queue.appendleft(LockRequest(tx, mode))
        else:
            e.queue.append(LockRequest(tx, mode))
        who = ",".join(f"T{b}" for b in self.blockers_of(tx, key, mode))
        self.log.append(f"  T{tx} {mode.name}({key}) WAITS for {who}")
        return False

    def blockers_of(self, tx: int, key: str, mode: LockMode) -> list[int]:
        """The transactions tx would wait for if it requested mode on key now, ascending."""
        out: set[int] = set()
        e = self.table.get(key)
        if e is None:
            return []
        for h, m in e.holders.items():
            if h != tx and not mode.compatible_with(m):
                out.add(h)
        for ahead in e.queue:
            if ahead.tx == tx:
                break  # only requests ahead of ours
            if not mode.compatible_with(ahead.mode):
                out.add(ahead.tx)
        return sorted(out)

    def release(self, tx: int, key: str) -> list[int]:
        """Release one lock early (basic 2PL). Returns the transactions whose queued requests were granted."""
        e = self.table.get(key)
        if e is None or e.holders.pop(tx, None) is None:
            return []
        self.log.append(f"  T{tx} releases {key}")
        granted = self._grant_waiters(key, e)
        if e.is_empty():
            del self.table[key]
        return granted

    def release_all(self, tx: int) -> list[int]:
        """Release every lock tx holds or waits for (commit or abort). Returns newly granted transactions."""
        granted: list[int] = []
        for key in self._keys():
            e = self.table[key]
            held = e.holders.pop(tx, None) is not None
            e.queue = deque(r for r in e.queue if r.tx != tx)
            if held:
                granted.extend(self._grant_waiters(key, e))
            if e.is_empty():
                del self.table[key]
        return granted

    def _grant_waiters(self, key: str, e: LockEntry) -> list[int]:
        granted: list[int] = []
        while e.queue and e.compatible(e.queue[0].tx, e.queue[0].mode):
            r = e.queue.popleft()
            e.holders[r.tx] = r.mode
            self.log.append(f"  T{r.tx} {r.mode.name}({key}) granted (was waiting)")
            granted.append(r.tx)
        return granted

    def keys_held_by(self, tx: int) -> list[str]:
        return [k for k in self._keys() if tx in self.table[k].holders]

    def wait_for_graph(self) -> "WaitForGraph":
        """Edges tx -> every transaction it is waiting for, derived from the queues."""
        g = WaitForGraph()
        for key in self._keys():
            for r in self.table[key].queue:
                for blocker in self.blockers_of(r.tx, key, r.mode):
                    g.add_edge(r.tx, blocker)
        return g


class WaitForGraph:
    """Directed graph "waits for". A cycle is a deadlock: nobody on it will ever be granted."""

    def __init__(self) -> None:
        self.edges: dict[int, set[int]] = {}

    def add_edge(self, frm: int, to: int) -> None:
        self.edges.setdefault(frm, set()).add(to)

    def find_cycle(self) -> list[int] | None:
        """Depth-first search with three colours; returns the first cycle found, in wait order, or None."""
        colour: dict[int, int] = {}  # 0 white, 1 grey (on stack), 2 black
        path: list[int] = []
        for start in sorted(self.edges):
            c = self._dfs(start, colour, path)
            if c is not None:
                return c
        return None

    def _dfs(self, node: int, colour: dict[int, int], path: list[int]) -> list[int] | None:
        c = colour.get(node, 0)
        if c == 2:
            return None
        if c == 1:  # back edge: the cycle is the path from node to here
            return path[path.index(node):]
        colour[node] = 1
        path.append(node)
        for nxt in sorted(self.edges.get(node, ())):
            found = self._dfs(nxt, colour, path)
            if found is not None:
                return found
        path.pop()
        colour[node] = 2
        return None

    def __str__(self) -> str:
        parts = [f"T{f} -> T{t}" for f in sorted(self.edges) for t in sorted(self.edges[f])]
        return ", ".join(parts) if parts else "(no waits)"


# ---------------------------------------------------------------------------------------------
# Transactions, operations, and the scheduler that interleaves them deterministically.
# ---------------------------------------------------------------------------------------------


class VictimPolicy(Enum):
    YOUNGEST = "YOUNGEST"  # abort the transaction that started last: it has presumably done the least work
    FEWEST_LOCKS = "FEWEST_LOCKS"  # abort the one holding the fewest locks: the cheapest to undo


class Op:
    """The steps a scripted transaction can take (Java: sealed interface Op). Add is a read-modify-write under X."""


@dataclass(frozen=True)
class _Read(Op):
    key: str


@dataclass(frozen=True)
class _Add(Op):
    key: str
    delta: Money


@dataclass(frozen=True)
class _Unlock(Op):
    key: str


@dataclass(frozen=True)
class _Commit(Op):
    pass


Op.Read, Op.Add, Op.Unlock, Op.Commit = _Read, _Add, _Unlock, _Commit


class Txn:
    class Status(Enum):
        ACTIVE = "ACTIVE"
        BLOCKED = "BLOCKED"
        COMMITTED = "COMMITTED"
        ABORTED = "ABORTED"

    class Phase(Enum):
        GROWING = "GROWING"
        SHRINKING = "SHRINKING"

    def __init__(self, id: int, script: list[Op]) -> None:
        self.id = id
        self.script = tuple(script)
        self.pc = 0
        self.status = Txn.Status.ACTIVE
        self.phase = Txn.Phase.GROWING
        self.restarts = 0
        self.undo: dict[str, Money] = {}  # key -> value before our first in-place write
        self.reads: dict[str, Money] = {}  # what this transaction observed (for tests)

    def finished(self) -> bool:
        return self.status in (Txn.Status.COMMITTED, Txn.Status.ABORTED)


class Scheduler:
    def __init__(self, policy: VictimPolicy, restart_victims: bool) -> None:
        self.db: dict[str, Money] = {}
        self.log: list[str] = []
        self.locks = LockManager(self.log)
        self.txns: dict[int, Txn] = {}  # ids are assigned in increasing order, so insertion order is TreeMap order
        self.policy = policy
        self.restart_victims = restart_victims
        self.next_id = 1
        self.deadlocks_detected = 0
        self.cycles: list[list[int]] = []
        self.victims: list[int] = []

    def load(self, key: str, value: Money) -> None:
        self.db[key] = value

    def begin(self, script: list[Op]) -> Txn:
        t = Txn(self.next_id, script)
        self.next_id += 1
        self.txns[t.id] = t
        self.log.append(f"T{t.id} begin")
        return t

    def run(self, *order: int) -> None:
        """Drive the transactions: order names who steps at each tick, then the rest runs round-robin."""
        for id in order:
            self.step(self.txns[id])
        guard = 0
        while any(not t.finished() for t in self.txns.values()):
            progressed = False
            for t in list(self.txns.values()):
                progressed = self.step(t) or progressed  # step every transaction, like Java's |=
            if not progressed:
                self.detect_and_resolve()
                guard += 1
                if guard > 10_000:
                    raise RuntimeError(f"scheduler stuck: {self.locks.wait_for_graph()}")
            else:
                guard = 0

    def step(self, t: Txn) -> bool:
        """Execute one step of t. Returns True if the transaction advanced (blocking counts as progress)."""
        if t.status is not Txn.Status.ACTIVE:
            return False
        op = t.script[t.pc]
        if self._execute(t, op):
            t.pc += 1
            return True
        t.status = Txn.Status.BLOCKED
        self.detect_and_resolve()
        return True

    def _execute(self, t: Txn, op: Op) -> bool:
        match op:
            case Op.Read(key=key):
                if not self._lock(t, key, LockMode.S):
                    return False
                v = self.db.get(key)
                t.reads[key] = v
                self.log.append(f"  T{t.id} read {key} = {v}")
            case Op.Add(key=key, delta=delta):
                if not self._lock(t, key, LockMode.X):
                    return False
                before = self.db.get(key)
                t.undo.setdefault(key, before)
                self.db[key] = before.plus(delta)  # in place: strict 2PL keeps it invisible until commit
                self.log.append(f"  T{t.id} {key} {before} -> {self.db[key]}")
            case Op.Unlock(key=key):
                t.phase = Txn.Phase.SHRINKING
                self._grant(self.locks.release(t.id, key))
            case Op.Commit():
                self.commit(t)
        return True

    def _lock(self, t: Txn, key: str, mode: LockMode) -> bool:
        if t.phase is Txn.Phase.SHRINKING and key not in self.locks.keys_held_by(t.id):
            raise TwoPhaseViolationException(f"T{t.id} requested {mode.name}({key}) after releasing a lock")
        return self.locks.acquire(t.id, key, mode)

    def commit(self, t: Txn) -> None:
        t.status = Txn.Status.COMMITTED
        self.log.append(f"  T{t.id} COMMIT")
        self._grant(self.locks.release_all(t.id))

    def abort(self, t: Txn) -> None:
        entries = list(t.undo.items())
        for key, value in reversed(entries):
            self.db[key] = value
        t.undo.clear()
        t.status = Txn.Status.ABORTED
        self.log.append(f"  T{t.id} ABORT (undo {len(entries)} write{'' if len(entries) == 1 else 's'})")
        self._grant(self.locks.release_all(t.id))
        if self.restart_victims:
            t.status = Txn.Status.ACTIVE
            t.phase = Txn.Phase.GROWING
            t.pc = 0
            t.reads.clear()
            t.restarts += 1
            self.log.append(f"  T{t.id} restarted (attempt {t.restarts + 1})")

    def _grant(self, granted: list[int]) -> None:
        """Transactions whose queued request was just granted continue with the step they were blocked on."""
        for id in granted:
            w = self.txns[id]
            if w.status is not Txn.Status.BLOCKED:
                continue
            w.status = Txn.Status.ACTIVE
            if self._execute(w, w.script[w.pc]):
                w.pc += 1
            else:
                w.status = Txn.Status.BLOCKED

    def detect_and_resolve(self) -> int | None:
        """Build the wait-for graph; if it has a cycle, pick a victim by policy and abort it. Returns the victim id."""
        cycle = self.locks.wait_for_graph().find_cycle()
        if cycle is None:
            return None
        self.deadlocks_detected += 1
        self.cycles.append(cycle)
        victim = self.choose_victim(cycle)
        self.victims.append(victim)
        self.log.append(f"  DEADLOCK {Scheduler.describe(cycle)} -> victim T{victim} ({self.policy.name})")
        self.abort(self.txns[victim])
        return victim

    def choose_victim(self, cycle: list[int]) -> int:
        match self.policy:
            case VictimPolicy.YOUNGEST:
                return max(cycle)
            case VictimPolicy.FEWEST_LOCKS:
                best = cycle[0]
                for id in cycle:
                    mine, theirs = len(self.locks.keys_held_by(id)), len(self.locks.keys_held_by(best))
                    if mine < theirs or (mine == theirs and id > best):  # tie: youngest
                        best = id
                return best

    @staticmethod
    def describe(cycle: list[int]) -> str:
        return "".join(f"T{id} -> " for id in cycle) + f"T{cycle[0]}"

    def total(self, currency: Currency) -> Money:
        s = Money.of_minor(0, currency)
        for m in self.db.values():
            s = s.plus(m)
        return s

    def lock_table_empty(self) -> bool:
        return not self.locks.table


# ---------------------------------------------------------------------------------------------
# Worked example
# ---------------------------------------------------------------------------------------------

USD = Currency.USD


def usd(minor: int) -> Money:
    return Money.of_minor(minor, USD)


def transfer(frm: str, to: str, amount_minor: int) -> list[Op]:
    """Transfer amount from frm to to: two read-modify-writes under X locks, then commit."""
    return [Op.Add(frm, usd(-amount_minor)), Op.Add(to, usd(amount_minor)), Op.Commit()]


def main() -> None:
    print("== Two transfers in opposite directions: A->B and B->A, strict 2PL ==")
    s = Scheduler(VictimPolicy.YOUNGEST, True)
    s.load("acct:A", usd(100_000))
    s.load("acct:B", usd(50_000))
    s.begin(transfer("acct:A", "acct:B", 10_000))  # T1: 100.00 from A to B
    s.begin(transfer("acct:B", "acct:A", 5_000))  # T2:  50.00 from B to A
    s.run(1, 2, 1, 2)  # T1 locks A, T2 locks B, T1 wants B, T2 wants A
    for line in s.log:
        print(line)
    print(f"  => deadlocks detected: {s.deadlocks_detected}, victims: {s.victims}")
    print(f"  => final A = {s.db['acct:A']}, B = {s.db['acct:B']}, total = {s.total(USD)}")

    print()
    print("== Same shape, FEWEST_LOCKS policy: T2 also debits a fee account, so it holds two locks ==")
    f = Scheduler(VictimPolicy.FEWEST_LOCKS, True)
    f.load("acct:A", usd(100_000))
    f.load("acct:B", usd(50_000))
    f.load("fees", usd(0))
    f.begin(transfer("acct:A", "acct:B", 10_000))
    f.begin([Op.Add("acct:B", usd(-5_050)), Op.Add("fees", usd(50)), Op.Add("acct:A", usd(5_000)), Op.Commit()])
    f.run(1, 2, 2, 1, 2)  # T2 holds B and fees (2 locks) when the cycle forms; T1 holds A
    for line in f.log:
        if "DEADLOCK" in line or "ABORT" in line:
            print(line)
    print(f"  => victim T{f.victims[0]}: T1 holds 1 lock, T2 holds 2, so the cheaper rollback is T1")

    print()
    print("== Lock upgrade deadlock: two deposits each read the balance (S) then write it (X) ==")
    u = Scheduler(VictimPolicy.YOUNGEST, True)
    u.load("acct:A", usd(100_000))
    u.begin([Op.Read("acct:A"), Op.Add("acct:A", usd(1_000)), Op.Commit()])
    u.begin([Op.Read("acct:A"), Op.Add("acct:A", usd(2_000)), Op.Commit()])
    u.run(1, 2, 1, 2)
    for line in u.log:
        print(line)
    print(f"  => final A = {u.db['acct:A']} (both deposits landed; no lost update)")

    print()
    print("== Why strict: basic 2PL releases X(A) before commit, a reader sees a balance that never existed ==")
    b = Scheduler(VictimPolicy.YOUNGEST, False)
    b.load("acct:A", usd(100_000))
    writer = b.begin([Op.Add("acct:A", usd(-90_000)), Op.Unlock("acct:A"), Op.Read("acct:A")])
    reader = b.begin([Op.Read("acct:A"), Op.Commit()])
    b.step(writer)  # X(A), A := 10.00
    b.step(writer)  # releases A early: legal in basic 2PL, phase -> SHRINKING
    b.step(reader)  # reads 10.00, an uncommitted value
    b.step(reader)  # and commits on it
    b.abort(writer)  # writer rolls back: A is 100.00 again
    for line in b.log:
        print(line)
    print(f"  => T2 committed having read {reader.reads['acct:A']}; the committed balance is {b.db['acct:A']}")
    try:
        b2 = Scheduler(VictimPolicy.YOUNGEST, False)
        b2.load("k", usd(1))
        b2.load("j", usd(1))
        t = b2.begin([Op.Add("k", usd(1)), Op.Unlock("k"), Op.Read("j")])
        b2.step(t)
        b2.step(t)
        b2.step(t)
    except TwoPhaseViolationException as e:
        print(f"  => {e}: the two-phase rule forbids growing after shrinking")


if __name__ == "__main__":
    main()
