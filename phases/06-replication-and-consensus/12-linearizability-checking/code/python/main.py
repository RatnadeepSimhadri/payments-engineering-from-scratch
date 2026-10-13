"""Lesson: phases/06-replication-and-consensus/12-linearizability-checking/docs/en.md
Linearizability Checking with Recorded Histories
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: Herlihy & Wing, "Linearizability: A Correctness Condition for Concurrent Objects" (TOPLAS 1990);
         Wing & Gong, "Testing and Verifying Concurrent Objects" (JPDC 1993); Lowe, "Testing for Linearizability" (CCPE 2017).
Python 3.11+, standard library only. Same design as the Java and Go versions.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from enum import Enum

MASK64 = (1 << 64) - 1


class OpKind(Enum):
    READ = "READ"
    WRITE = "WRITE"


class Consistency(Enum):
    """The consistency model the checker searches for. Same search; only the "may go next" rule differs."""

    LINEARIZABLE = "LINEARIZABLE"
    SEQUENTIAL = "SEQUENTIAL"


@dataclass(frozen=True)
class Op:
    """One completed client operation on a key-value register.

    For a WRITE, value is what was written; for a READ, value is what the client got back. invoked_at and
    returned_at are the client's own timestamps. An op that returned before another was invoked precedes it in
    real time; otherwise they are concurrent.
    """

    id: int
    client: str
    kind: OpKind
    key: str
    value: int
    invoked_at: int
    returned_at: int

    def __post_init__(self) -> None:
        if self.returned_at < self.invoked_at:
            raise ValueError(f"op {self.id} returned before it was invoked")

    def precedes(self, other: "Op") -> bool:
        return self.returned_at < other.invoked_at

    def concurrent_with(self, other: "Op") -> bool:
        return not self.precedes(other) and not other.precedes(self)

    def __str__(self) -> str:
        body = f"write({self.key}={self.value})" if self.kind == OpKind.WRITE else f"read({self.key})->{self.value}"
        return f"#{self.id} {self.client} {body} [{self.invoked_at},{self.returned_at}]"


class History:
    """An ordered collection of completed ops. Ids are dense 0..n-1 so the checker can use a bitmask of linearized ops."""

    MAX_OPS = 64

    def __init__(self) -> None:
        self._ops: list[Op] = []

    def add(self, client: str, kind: OpKind, key: str, value: int, invoked_at: int, returned_at: int) -> "History":
        if len(self._ops) >= History.MAX_OPS:
            raise ValueError(f"this checker handles at most {History.MAX_OPS} ops per history")
        self._ops.append(Op(len(self._ops), client, kind, key, value, invoked_at, returned_at))
        return self

    def write(self, client: str, key: str, value: int, invoked_at: int, returned_at: int) -> "History":
        return self.add(client, OpKind.WRITE, key, value, invoked_at, returned_at)

    def read(self, client: str, key: str, value: int, invoked_at: int, returned_at: int) -> "History":
        return self.add(client, OpKind.READ, key, value, invoked_at, returned_at)

    @property
    def ops(self) -> list[Op]:
        return list(self._ops)

    def size(self) -> int:
        return len(self._ops)

    def op(self, op_id: int) -> Op:
        return self._ops[op_id]


class FakeClock:
    """Injected clock: the recorder never reads time.monotonic, so recorded histories are reproducible."""

    def __init__(self) -> None:
        self._now = 0

    def now(self) -> int:
        return self._now

    def tick(self) -> int:
        self._now += 1
        return self._now

    def advance(self, ticks: int) -> None:
        self._now += ticks


@dataclass(frozen=True)
class _Pending:
    client: str
    kind: OpKind
    key: str
    value: int
    invoked_at: int


class HistoryRecorder:
    """Records a history the way a test harness would: stamp before the request leaves, stamp after the response arrives."""

    def __init__(self, clock: FakeClock) -> None:
        self._clock = clock
        self._pending: dict[int, _Pending] = {}
        self._completed: list[Op] = []
        self._next_id = 0

    def invoke_write(self, client: str, key: str, value: int) -> int:
        return self._invoke(client, OpKind.WRITE, key, value)

    def invoke_read(self, client: str, key: str) -> int:
        return self._invoke(client, OpKind.READ, key, 0)

    def _invoke(self, client: str, kind: OpKind, key: str, value: int) -> int:
        op_id = self._next_id
        self._next_id += 1
        self._pending[op_id] = _Pending(client, kind, key, value, self._clock.now())
        return op_id

    def complete(self, op_id: int, value: int) -> None:
        """Complete a write (value ignored) or a read (value is what came back). The return timestamp is taken now."""
        p = self._pending.pop(op_id, None)
        if p is None:
            raise ValueError(f"no pending op {op_id}")
        v = p.value if p.kind == OpKind.WRITE else value
        self._completed.append(Op(op_id, p.client, p.kind, p.key, v, p.invoked_at, self._clock.now()))

    def pending_count(self) -> int:
        return len(self._pending)

    def history(self) -> History:
        """The history so far. Ops are renumbered densely in invocation order; pending ops are an error (Exercise 2)."""
        if self._pending:
            raise RuntimeError(f"{len(self._pending)} ops never completed; this checker needs a complete history")
        h = History()
        for o in sorted(self._completed, key=lambda o: o.id):
            h.add(o.client, o.kind, o.key, o.value, o.invoked_at, o.returned_at)
        return h


State = dict[str, int]


class RegisterModel:
    """The sequential specification: a map of registers, each initially 0.

    step returns the next state, or None if the op's observed result is impossible from this state.
    """

    def initial(self) -> State:
        return {}

    def step(self, state: State, op: Op) -> State | None:
        current = state.get(op.key, 0)
        if op.kind == OpKind.READ:
            return state if current == op.value else None
        nxt = dict(state)
        nxt[op.key] = op.value
        return nxt

    def key(self, state: State) -> str:
        """Canonical, hashable form for the memo table: "a=1,b=2"."""
        return ",".join(f"{k}={v}" for k, v in sorted(state.items()))


@dataclass(frozen=True)
class CheckResult:
    """The verdict plus what the search did: the witness order if ok, the longest prefix it ever linearized if not."""

    ok: bool
    model: Consistency
    order: tuple[int, ...]
    longest_prefix: tuple[int, ...]
    explored: int
    memo_hits: int

    def describe(self, h: History) -> str:
        lines = []
        if self.ok:
            lines.append(f"{self.model.value}: yes. Witness order:")
            lines.extend(f"  {h.op(i)}" for i in self.order)
        else:
            lines.append(f"{self.model.value}: NO. Longest {self.model.value.lower()} prefix ({len(self.longest_prefix)} of {h.size()} ops):")
            lines.extend(f"  {h.op(i)}" for i in self.longest_prefix)
            lines.append("  no remaining op can go next from here")
        lines.append(f"  ({self.explored} states explored, {self.memo_hits} memo hits)")
        return "\n".join(lines)


class Checker:
    """Wing-Gong backtracking search with Lowe's (linearized-set, state) memo.

    Finds a sequential order of all ops that (a) the model accepts and (b) respects real time (LINEARIZABLE) or
    per-client program order (SEQUENTIAL).
    """

    def __init__(self, model: RegisterModel, memoize: bool = True, state_budget: int = 5_000_000) -> None:
        self._model = model
        self._memoize = memoize
        self._state_budget = state_budget
        self._ops: list[Op] = []
        self._consistency = Consistency.LINEARIZABLE
        self._failed_configs: set[str] = set()
        self._order: list[int] = []
        self._longest_prefix: list[int] = []
        self._explored = 0
        self._memo_hits = 0

    def check(self, history: History, consistency: Consistency = Consistency.LINEARIZABLE) -> CheckResult:
        self._ops = history.ops
        self._consistency = consistency
        self._failed_configs = set()
        self._order = []
        self._longest_prefix = []
        self._explored = 0
        self._memo_hits = 0
        ok = self._search(0, self._model.initial())
        return CheckResult(ok, consistency, tuple(self._order), tuple(self._longest_prefix), self._explored, self._memo_hits)

    def _search(self, done: int, state: State) -> bool:
        self._explored += 1
        if self._explored > self._state_budget:
            raise RuntimeError(f"state budget of {self._state_budget} exceeded; history too concurrent for this checker")
        if len(self._order) > len(self._longest_prefix):
            self._longest_prefix = list(self._order)
        if done.bit_count() == len(self._ops):
            return True
        memo_key = f"{done}|{self._model.key(state)}" if self._memoize else ""
        if self._memoize and memo_key in self._failed_configs:
            self._memo_hits += 1
            return False
        for i, op in enumerate(self._ops):
            if done & (1 << i) or not self._may_go_next(i, done):
                continue
            nxt = self._model.step(state, op)
            if nxt is None:
                continue
            self._order.append(i)
            if self._search(done | (1 << i), nxt):
                return True
            self._order.pop()
        if self._memoize:
            self._failed_configs.add(memo_key)
        return False

    def _may_go_next(self, i: int, done: int) -> bool:
        """LINEARIZABLE: no not-yet-linearized op returned before i was invoked. SEQUENTIAL: no earlier op of the same client remains."""
        candidate = self._ops[i]
        for j, other in enumerate(self._ops):
            if j == i or done & (1 << j):
                continue
            if self._consistency == Consistency.LINEARIZABLE:
                if other.precedes(candidate):
                    return False
            elif other.client == candidate.client and other.invoked_at < candidate.invoked_at:
                return False
        return True


class Rng:
    """SplitMix64 (Steele, Lea, Flood 2014), masked to 64 bits so the stream matches Java and Go."""

    def __init__(self, seed: int) -> None:
        self.state = seed & MASK64

    def next_long(self) -> int:
        self.state = (self.state + 0x9E3779B97F4A7C15) & MASK64
        z = self.state
        z = ((z ^ (z >> 30)) * 0xBF58476D1CE4E5B9) & MASK64
        z = ((z ^ (z >> 27)) * 0x94D049BB133111EB) & MASK64
        return z ^ (z >> 31)

    def next_int(self, bound: int) -> int:
        return (self.next_long() >> 33) % bound


class HistoryGenerator:
    """Builds histories that are linearizable by construction: pick a sequential run, then widen each op into an interval."""

    @staticmethod
    def linearizable(seed: int, n: int, clients: int, window: int) -> History:
        rng = Rng(seed)
        h = History()
        value = 0
        for k in range(n):
            point = 10 * (k + 1)
            invoked = point - rng.next_int(window)
            returned = point + rng.next_int(window)
            client = f"c{rng.next_int(clients)}"
            if rng.next_int(2) == 0:
                value = 1 + rng.next_int(9)
                h.write(client, "x", value, invoked, returned)
            else:
                h.read(client, "x", value, invoked, returned)
        return h


def _show(title: str, h: History, r: CheckResult) -> None:
    print(f"== {title} ==")
    for o in h.ops:
        print(f"  {o}")
    print(r.describe(h))
    print()


def main() -> None:
    checker = Checker(RegisterModel())

    good = (History().write("ledger", "bal", 500, 0, 4).write("ledger", "bal", 300, 6, 9)
            .read("risk", "bal", 300, 10, 12).read("app", "bal", 300, 13, 15))
    _show("A correct replica: every read sees the latest completed write", good, checker.check(good))

    stale = (History().write("ledger", "bal", 500, 0, 4).write("ledger", "bal", 300, 6, 9)
             .read("risk", "bal", 500, 10, 12).read("app", "bal", 300, 13, 15))
    _show("A lagging replica: risk reads 500 after the write of 300 returned (stale read)", stale, checker.check(stale))

    seq_only = History().write("ledger", "bal", 300, 0, 4).read("risk", "bal", 0, 6, 8)
    print("== Sequentially consistent but not linearizable ==")
    for o in seq_only.ops:
        print(f"  {o}")
    print(checker.check(seq_only, Consistency.LINEARIZABLE).describe(seq_only))
    print(checker.check(seq_only, Consistency.SEQUENTIAL).describe(seq_only))
    print("  (sequential consistency may put risk's read before ledger's write; linearizability may not, because the write returned at 4 and the read was invoked at 6)")
    print()

    window = History().write("ledger", "bal", 300, 0, 10).read("risk", "bal", 0, 5, 15).read("app", "bal", 300, 6, 8)
    _show("Linearizable only because of the concurrency window: risk's read of 0 overlaps the write", window, checker.check(window))
    shrunk = History().write("ledger", "bal", 300, 0, 10).read("risk", "bal", 0, 11, 15).read("app", "bal", 300, 6, 8)
    _show("Same ops, but risk's read starts after the write returned", shrunk, checker.check(shrunk))

    print("== Recording a history with an injected clock: a read served by a replica 3 ticks behind ==")
    clock = FakeClock()
    rec = HistoryRecorder(clock)
    w1 = rec.invoke_write("ledger", "bal", 500); clock.advance(2); rec.complete(w1, 0)
    clock.advance(1)
    w2 = rec.invoke_write("ledger", "bal", 300); clock.advance(2); rec.complete(w2, 0)
    clock.advance(1)
    r1 = rec.invoke_read("risk", "bal"); clock.advance(1); rec.complete(r1, 500)  # replica had not applied w2 yet
    recorded = rec.history()
    for o in recorded.ops:
        print(f"  {o}")
    print(checker.check(recorded).describe(recorded))
    print()

    print("== Why the memo matters: 8 fully concurrent writes followed by one read ==")
    burst = History()
    for i in range(8):
        burst.write(f"c{i}", "x", i + 1, 0, 100)
    burst.read("reader", "x", 1, 101, 102)  # the FIRST write must be linearized last
    with_memo = Checker(RegisterModel(), True, 5_000_000).check(burst)
    without_memo = Checker(RegisterModel(), False, 5_000_000).check(burst)
    print(f"  with memo:    ok={str(with_memo.ok).lower()}, {with_memo.explored} states explored, {with_memo.memo_hits} memo hits")
    print(f"  without memo: ok={str(without_memo.ok).lower()}, {without_memo.explored} states explored (depth-first in id order keeps retrying permutations that put write(x=1) first)")
    print()

    print("== 50 generated histories (20 ops, 3 clients, window 15) that are linearizable by construction ==")
    passed = 0
    max_explored = 0
    for seed in range(1, 51):
        r = checker.check(HistoryGenerator.linearizable(seed, 20, 3, 15))
        if r.ok:
            passed += 1
        max_explored = max(max_explored, r.explored)
    print(f"  passed {passed}/50; most states explored by any one check: {max_explored}")


if __name__ == "__main__":
    main()
