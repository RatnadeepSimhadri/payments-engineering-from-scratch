"""Lesson: phases/04-concurrency/07-deadlocks-and-lock-ordering/docs/en.md
Deadlocks, Lock Ordering, and Detection
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: Coffman, Elphick & Shoshani, "System Deadlocks" (ACM Computing Surveys, 1971); threading.Lock.acquire(blocking=False);
PostgreSQL deadlock_timeout and wait-for-graph detector documentation.
Python 3.11+, standard library only. Same design as the Java and Go versions.
"""

from __future__ import annotations

import random
import threading
import time
from dataclasses import dataclass
from typing import Callable


# ---------------------------------------------------------------------------
# Money (copied from Phase 10, Lesson 01)
# ---------------------------------------------------------------------------


@dataclass(frozen=True)
class Currency:
    code: str
    exponent: int

    def __post_init__(self) -> None:
        if len(self.code) != 3:
            raise ValueError(f"currency code must be three letters: {self.code!r}")
        if not 0 <= self.exponent <= 4:
            raise ValueError(f"exponent must be 0..4: {self.exponent}")

    def minor_units_per_major(self) -> int:
        return 10 ** self.exponent


USD = Currency("USD", 2)


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

    def is_negative(self) -> bool:
        return self.minor_units < 0

    def __str__(self) -> str:
        magnitude = abs(self.minor_units)
        whole, frac = divmod(magnitude, self.currency.minor_units_per_major())
        text = str(whole)
        if self.currency.exponent > 0:
            text += "." + str(frac).rjust(self.currency.exponent, "0")
        return f"{'-' if self.minor_units < 0 else ''}{text} {self.currency.code}"

    __repr__ = __str__


class InsufficientFundsError(RuntimeError):
    def __init__(self, account_id: str, balance: int, requested: int) -> None:
        super().__init__(f"account {account_id} has {balance} minor units, debit of {requested} refused")


class DeadlockDetectedError(RuntimeError):
    """Raised by LockTracker when granting a lock request would close a cycle in the wait-for graph."""

    def __init__(self, cycle: list[str]) -> None:
        super().__init__("deadlock: " + " -> ".join(cycle) + " -> " + cycle[0])
        self.cycle = list(cycle)


# ---------------------------------------------------------------------------
# Wait-for graph and the tracker that builds it
# ---------------------------------------------------------------------------


class WaitForGraph:
    """A directed graph of "owner X waits for a lock held by owner Y". A cycle is a deadlock.
    Nodes are kept sorted so the cycle reported is the same on every run."""

    def __init__(self) -> None:
        self._edges: dict[str, set[str]] = {}

    def add_edge(self, waiter: str, holder: str) -> None:
        self._edges.setdefault(waiter, set()).add(holder)
        self._edges.setdefault(holder, set())

    def nodes(self) -> list[str]:
        return sorted(self._edges)

    def successors(self, node: str) -> list[str]:
        return sorted(self._edges.get(node, ()))

    def find_cycle(self) -> list[str]:
        """The owners on the first cycle found, in edge order starting from the smallest id, or [].
        Iterative DFS with three colours: white (unvisited), grey (on the path), black (finished)."""
        colour: dict[str, int] = {}          # absent = white, 1 = grey, 2 = black
        parent: dict[str, str] = {}
        for start in self.nodes():
            if start in colour:
                continue
            stack = [start]
            while stack:
                node = stack[-1]
                if node not in colour:
                    colour[node] = 1
                    for nxt in self.successors(node):
                        c = colour.get(nxt)
                        if c is None:
                            parent[nxt] = node
                            stack.append(nxt)
                        elif c == 1:
                            return self._path_of_cycle(parent, node, nxt)   # back edge: nxt is on the current path
                else:
                    stack.pop()
                    colour[node] = 2
        return []

    @staticmethod
    def _path_of_cycle(parent: dict[str, str], frm: str, to: str) -> list[str]:
        cycle = []
        cursor = frm
        while cursor != to:
            cycle.append(cursor)
            cursor = parent[cursor]
        cycle.append(to)
        cycle.reverse()                                  # edge order: to -> ... -> frm
        smallest = cycle.index(min(cycle))
        return cycle[smallest:] + cycle[:smallest]


class LockTracker:
    """Records every lock request, acquisition, and release so a wait-for graph can be built at any
    moment. requesting() is the detector: it refuses a request that would complete a cycle."""

    def __init__(self) -> None:
        self._mutex = threading.Lock()
        self._holders: dict[str, str] = {}   # lock_id -> owner
        self._waiting: dict[str, str] = {}   # owner -> lock_id
        self._events: list[str] = []

    def requesting(self, owner: str, lock_id: str) -> None:
        with self._mutex:
            self._waiting[owner] = lock_id
            self._events.append(f"{owner} requests {lock_id}")
            cycle = self._graph_locked().find_cycle()
            if owner in cycle:
                del self._waiting[owner]
                self._events.append(f"{owner} aborted: deadlock {cycle}")
                raise DeadlockDetectedError(cycle)

    def acquired(self, owner: str, lock_id: str) -> None:
        with self._mutex:
            self._waiting.pop(owner, None)
            self._holders[lock_id] = owner
            self._events.append(f"{owner} acquired {lock_id}")

    def released(self, owner: str, lock_id: str) -> None:
        with self._mutex:
            if self._holders.get(lock_id) == owner:
                del self._holders[lock_id]
            self._events.append(f"{owner} released {lock_id}")

    def graph(self) -> WaitForGraph:
        with self._mutex:
            return self._graph_locked()

    def _graph_locked(self) -> WaitForGraph:
        """One edge per waiting owner: waiter -> whoever holds the lock it wants (if anyone does)."""
        g = WaitForGraph()
        for waiter, lock_id in self._waiting.items():
            holder = self._holders.get(lock_id)
            if holder is not None and holder != waiter:
                g.add_edge(waiter, holder)
        return g

    def detect(self) -> list[str]:
        return self.graph().find_cycle()

    def events(self) -> list[str]:
        with self._mutex:
            return list(self._events)


class TrackedLock:
    """A lock that reports to a tracker. lock() may raise DeadlockDetectedError before blocking."""

    def __init__(self, lock_id: str, tracker: LockTracker | None) -> None:
        self.id = lock_id
        self._lock = threading.Lock()
        self._tracker = tracker                          # None = untracked, plain lock

    def lock(self, owner: str) -> None:
        if self._tracker is not None:
            self._tracker.requesting(owner, self.id)   # raises instead of blocking into a cycle
        self._lock.acquire()
        if self._tracker is not None:
            self._tracker.acquired(owner, self.id)

    def try_lock(self, owner: str) -> bool:
        if not self._lock.acquire(blocking=False):
            return False
        if self._tracker is not None:
            self._tracker.acquired(owner, self.id)
        return True

    def unlock(self, owner: str) -> None:
        if self._tracker is not None:
            self._tracker.released(owner, self.id)
        self._lock.release()

    def is_locked(self) -> bool:
        return self._lock.locked()


# ---------------------------------------------------------------------------
# Accounts and transfers
# ---------------------------------------------------------------------------


class Account:
    """An account whose balance is guarded by its own lock."""

    def __init__(self, account_id: str, currency: Currency, opening_minor_units: int, tracker: LockTracker | None) -> None:
        self.id = account_id
        self.currency = currency
        self.lock = TrackedLock(f"lock:{account_id}", tracker)
        self._balance = opening_minor_units              # guarded by lock

    def balance_minor(self) -> int:
        """Callers must hold lock (or know no one else is running)."""
        return self._balance

    def adjust(self, delta: int) -> None:
        self._balance += delta

    def balance(self) -> Money:
        self.lock.lock("reader")
        try:
            return Money.of_minor(self._balance, self.currency)
        finally:
            self.lock.unlock("reader")


class Backoff:
    """Pause between try-lock attempts: a seeded random spin then a yield. Counts pauses; never sleeps for a duration."""

    def __init__(self, seed: int) -> None:
        self._random = random.Random(seed)
        self._pauses = 0
        self._mutex = threading.Lock()

    def pause(self, attempt: int) -> None:
        with self._mutex:
            self._pauses += 1
            spins = self._random.randrange(1 << min(attempt, 10))   # exponential cap at 1024
        for _ in range(spins):
            pass
        time.sleep(0)                                    # yield the GIL; a yield, not a delay

    def pauses(self) -> int:
        with self._mutex:
            return self._pauses


class TransferService:
    """Three ways to move money between two accounts. One is a deadlock waiting to happen."""

    def __init__(self, between_locks: Callable[[], None], backoff: Backoff, max_attempts: int = 1_000_000) -> None:
        self._between_locks = between_locks              # test seam: runs after the first lock, before the second
        self._backoff = backoff
        self._max_attempts = max_attempts

    def naive_transfer(self, frm: Account, to: Account, amount: Money, owner: str) -> None:
        """Lock frm, then to. Two transfers in opposite directions can each take their first lock and wait forever."""
        self._require_distinct(frm, to)
        frm.lock.lock(owner)
        try:
            self._between_locks()
            to.lock.lock(owner)
            try:
                self._move(frm, to, amount)
            finally:
                to.lock.unlock(owner)
        finally:
            frm.lock.unlock(owner)

    def ordered_transfer(self, frm: Account, to: Account, amount: Money, owner: str) -> None:
        """Fix 1: a global order. Everyone locks the smaller account id first, so no cycle can form."""
        self._require_distinct(frm, to)
        first, second = (frm, to) if frm.id < to.id else (to, frm)
        first.lock.lock(owner)
        try:
            self._between_locks()
            second.lock.lock(owner)
            try:
                self._move(frm, to, amount)
            finally:
                second.lock.unlock(owner)
        finally:
            first.lock.unlock(owner)

    def try_lock_transfer(self, frm: Account, to: Account, amount: Money, owner: str) -> None:
        """Fix 2: never wait while holding. Try both; if the second is busy, release the first, back off, retry."""
        self._require_distinct(frm, to)
        for attempt in range(1, self._max_attempts + 1):
            if frm.lock.try_lock(owner):
                try:
                    self._between_locks()
                    if to.lock.try_lock(owner):
                        try:
                            self._move(frm, to, amount)
                            return
                        finally:
                            to.lock.unlock(owner)
                finally:
                    frm.lock.unlock(owner)           # release before backing off: hold nothing while waiting
            self._backoff.pause(attempt)
        raise RuntimeError(f"transfer {frm.id} -> {to.id} gave up after {self._max_attempts} attempts")

    @staticmethod
    def _move(frm: Account, to: Account, amount: Money) -> None:
        """Both locks held. The check-then-act and the two writes are one atomic step from any observer's view."""
        if amount.is_negative():
            raise ValueError(f"amount must not be negative: {amount}")
        if frm.balance_minor() < amount.minor_units:
            raise InsufficientFundsError(frm.id, frm.balance_minor(), amount.minor_units)
        frm.adjust(-amount.minor_units)
        to.adjust(amount.minor_units)

    @staticmethod
    def _require_distinct(frm: Account, to: Account) -> None:
        if frm.id == to.id:
            raise ValueError(f"cannot transfer to the same account: {frm.id}")


def run_all(threads: int, body: Callable[[int], None]) -> None:
    """Runs body on `threads` threads and joins them all; fails loudly rather than hanging on a real deadlock."""
    failures: list[str] = []
    failures_lock = threading.Lock()

    def wrapped(index: int) -> None:
        try:
            body(index)
        except Exception as exc:  # noqa: BLE001 - surfaced to the caller below
            with failures_lock:
                failures.append(f"{type(exc).__name__}: {exc}")

    pool = [threading.Thread(target=wrapped, args=(t,), name=f"worker-{t}", daemon=True) for t in range(threads)]
    for thread in pool:
        thread.start()
    for thread in pool:
        thread.join(10)
        if thread.is_alive():
            raise RuntimeError(f"thread {thread.name} did not finish: deadlock?")
    if failures:
        raise RuntimeError(f"worker failures: {failures}")


def main() -> None:
    print("== A wait-for graph, by hand ==")
    g = WaitForGraph()
    g.add_edge("T1", "T2")                               # T1 waits for a lock T2 holds
    g.add_edge("T2", "T3")
    g.add_edge("T3", "T1")
    g.add_edge("T4", "T1")                               # T4 is stuck behind the cycle but not on it
    print(f"edges T1->T2, T2->T3, T3->T1, T4->T1: cycle = {g.find_cycle()}")
    chain = WaitForGraph()
    chain.add_edge("T1", "T2")
    chain.add_edge("T2", "T3")
    print(f"edges T1->T2, T2->T3: cycle = {chain.find_cycle()} (a chain waits, it does not deadlock)")

    print("\n== The classic: A->B while B->A, replayed from recorded lock requests ==")
    scripted = LockTracker()
    scripted.acquired("T1", "lock:A")
    scripted.acquired("T2", "lock:B")
    scripted.requesting("T1", "lock:B")                  # T1 now waits for T2: no cycle yet
    print(f"after T1 requests lock:B: cycle = {scripted.detect()}")
    try:
        scripted.requesting("T2", "lock:A")              # T2 would wait for T1: cycle
    except DeadlockDetectedError as exc:
        print(f"T2 requests lock:A -> {exc}")

    print("\n== Live: two naive transfers, forced to interleave, caught by the detector ==")
    tracker = LockTracker()
    a = Account("A", USD, 10_000, tracker)
    b = Account("B", USD, 10_000, tracker)
    naive = TransferService(threading.Barrier(2).wait, Backoff(42))
    outcomes: list[str] = []
    outcomes_lock = threading.Lock()

    def attempt(frm: Account, to: Account, amount: Money, owner: str, label: str) -> None:
        try:
            naive.naive_transfer(frm, to, amount, owner)
            result = f"{label} committed"
        except DeadlockDetectedError as exc:
            result = f"{label.rsplit(' ', 1)[0]} aborted: {exc}"
        with outcomes_lock:
            outcomes.append(result)

    t1 = threading.Thread(target=attempt, args=(a, b, Money.of_minor(2_500, USD), "T1", "T1 A->B 25.00"))
    t2 = threading.Thread(target=attempt, args=(b, a, Money.of_minor(1_000, USD), "T2", "T2 B->A 10.00"))
    t1.start(); t2.start(); t1.join(10); t2.join(10)
    for outcome in sorted(outcomes):
        print(f"  {outcome}")
    print("  recorded events:")
    for event in tracker.events():
        print(f"    {event}")
    print(f"  balances A={a.balance()} B={b.balance()} (sum {Money.of_minor(a.balance_minor() + b.balance_minor(), USD)})")

    print("\n== Fix 1: lock ordering, 8 threads x 500 transfers each way ==")
    c = Account("C", USD, 100_000, None)
    d = Account("D", USD, 100_000, None)
    ordered = TransferService(lambda: time.sleep(0), Backoff(7))

    def ordered_work(t: int) -> None:
        for _ in range(500):
            if t % 2 == 0:
                ordered.ordered_transfer(c, d, Money.of_minor(1, USD), f"worker-{t}")
            else:
                ordered.ordered_transfer(d, c, Money.of_minor(1, USD), f"worker-{t}")

    run_all(8, ordered_work)
    print(f"  completed; C={c.balance()} D={d.balance()} sum {Money.of_minor(c.balance_minor() + d.balance_minor(), USD)}")

    print("\n== Fix 2: try-lock with backoff, 8 threads x 200 transfers each way ==")
    e = Account("E", USD, 100_000, None)
    f = Account("F", USD, 100_000, None)
    backoff = Backoff(7)
    trying = TransferService(lambda: time.sleep(0), backoff)

    def trying_work(t: int) -> None:
        for _ in range(200):                             # fewer than Java/Go: each yield is a syscall under the GIL
            if t % 2 == 0:
                trying.try_lock_transfer(e, f, Money.of_minor(1, USD), f"worker-{t}")
            else:
                trying.try_lock_transfer(f, e, Money.of_minor(1, USD), f"worker-{t}")

    run_all(8, trying_work)
    print(f"  completed; E={e.balance()} F={f.balance()} sum {Money.of_minor(e.balance_minor() + f.balance_minor(), USD)}; "
          f"backoff pauses: {backoff.pauses()} (varies)")


if __name__ == "__main__":
    main()
