"""Lesson: phases/04-concurrency/12-testing-concurrent-code/docs/en.md
Testing Concurrent Code: Stress, Model Checking, and Determinism
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: Godefroid, "Model Checking for Programming Languages using VeriSoft" (POPL 1997); Musuvathi et al.,
"Finding and Reproducing Heisenbugs in Concurrent Programs" (CHESS, OSDI 2008). Python 3.11+, standard library only.
"""

from __future__ import annotations

import sys
import threading
from dataclasses import dataclass
from typing import Callable, Optional, Union

INT64_MIN, INT64_MAX = -(1 << 63), (1 << 63) - 1


def add_exact(a: int, b: int) -> int:
    """Math.addExact on a long: Python ints never overflow, so the Java bound is enforced by hand."""
    r = a + b
    if not INT64_MIN <= r <= INT64_MAX:
        raise OverflowError("long overflow")
    return r


class JavaRandom:
    """java.util.Random's 48-bit LCG, ported exactly so seed 42 samples the same schedules in Java and Python."""

    _MULTIPLIER, _ADDEND, _MASK = 0x5DEECE66D, 0xB, (1 << 48) - 1

    def __init__(self, seed: int) -> None:
        self._seed = (seed ^ self._MULTIPLIER) & self._MASK

    def next(self, bits: int) -> int:
        self._seed = (self._seed * self._MULTIPLIER + self._ADDEND) & self._MASK
        result = self._seed >> (48 - bits)
        return result - (1 << 32) if result & (1 << 31) else result  # (int) cast: signed 32-bit

    def next_int(self, bound: int) -> int:
        if bound <= 0:
            raise ValueError("bound must be positive")
        r = self.next(31)
        m = bound - 1
        if bound & m == 0:
            return (bound * r) >> 31
        u = r
        while True:
            r = u % bound
            if ((u - r + m) & 0xFFFFFFFF) < 0x80000000:  # the JDK's int-overflow rejection test
                return r
            u = self.next(31)


# ---------------------------------------------------------------------------------------------
# Part 1: a tiny machine whose scheduler is an input.
# ---------------------------------------------------------------------------------------------


@dataclass(frozen=True)
class Load:
    """reg = memory[var]"""

    var: str
    reg: str

    def __str__(self) -> str:
        return f"{self.reg} = load {self.var}"


@dataclass(frozen=True)
class Store:
    """memory[var] = reg"""

    var: str
    reg: str

    def __str__(self) -> str:
        return f"store {self.var} = {self.reg}"


@dataclass(frozen=True)
class Add:
    """reg = reg + k"""

    reg: str
    k: int

    def __str__(self) -> str:
        return f"{self.reg} += {self.k}"


@dataclass(frozen=True)
class AtomicAdd:
    """memory[var] += k in one indivisible step: AtomicLong.addAndGet, Redis INCRBY, UPDATE ... SET x = x + k."""

    var: str
    k: int

    def __str__(self) -> str:
        return f"atomic {self.var} += {self.k}"


@dataclass(frozen=True)
class Lock:
    """Blocks while another thread holds the lock."""

    lock: str

    def __str__(self) -> str:
        return f"lock {self.lock}"


@dataclass(frozen=True)
class Unlock:
    lock: str

    def __str__(self) -> str:
        return f"unlock {self.lock}"


@dataclass(frozen=True)
class BranchIfLess:
    """if (reg < k) jump to instruction index `target` in this thread, else fall through."""

    reg: str
    k: int
    target: int

    def __str__(self) -> str:
        return f"if {self.reg} < {self.k} goto {self.target}"


# One atomic step of a modelled thread (Java's sealed interface Instruction).
Instruction = Union[Load, Store, Add, AtomicAdd, Lock, Unlock, BranchIfLess]


@dataclass(frozen=True)
class ThreadSpec:
    """A modelled thread: a name and a straight-line program with optional forward branches."""

    name: str
    code: tuple

    def __init__(self, name: str, code: list) -> None:
        if name is None:
            raise TypeError("name")
        object.__setattr__(self, "name", name)
        object.__setattr__(self, "code", tuple(code))


@dataclass(frozen=True)
class Program:
    """The whole program: initial shared memory and the threads that run against it."""

    initial_memory: dict
    threads: tuple

    def __init__(self, initial_memory: dict, threads: list) -> None:
        object.__setattr__(self, "initial_memory", dict(initial_memory))
        object.__setattr__(self, "threads", tuple(threads))

    def interleavings_upper_bound(self) -> int:
        """Number of distinct interleavings if no instruction ever blocks: the multinomial coefficient."""
        total, result = 0, 1
        for t in self.threads:
            for i in range(1, len(t.code) + 1):
                total += 1
                product = result * total
                if product > INT64_MAX:                      # Math.multiplyExact
                    raise OverflowError("long overflow")
                result = product // i                        # exact: C(total, i) is an integer
        return result


@dataclass(frozen=True)
class TraceStep:
    """One scheduling decision: which thread ran, and what it executed."""

    thread: str
    instruction: Instruction

    def __str__(self) -> str:
        return f"{self.thread}: {self.instruction}"


@dataclass(frozen=True)
class Invariant:
    """A named end-state predicate."""

    name: str
    holds: Callable[[dict], bool]


class IllegalStateError(RuntimeError):
    """Java's IllegalStateException: the machine was asked to do something its state forbids."""


class Machine:
    """Shared memory, one program counter and register file per thread, lock ownership.
    It never spawns a thread: step(i) runs exactly one instruction of thread i on the caller's thread."""

    def __init__(self, program: Program) -> None:
        self._program = program
        self.memory: dict[str, int] = dict(program.initial_memory)
        self._pc = [0] * len(program.threads)
        self._registers: list[dict[str, int]] = [{} for _ in program.threads]
        self._lock_owner: dict[str, int] = {}
        self._trace: list[TraceStep] = []

    def finished(self, t: int) -> bool:
        return self._pc[t] >= len(self._program.threads[t].code)

    def all_finished(self) -> bool:
        return all(self.finished(t) for t in range(len(self._pc)))

    def enabled(self, t: int) -> bool:
        """A thread is enabled when it has an instruction left and that instruction can run now."""
        if self.finished(t):
            return False
        nxt = self._program.threads[t].code[self._pc[t]]
        if isinstance(nxt, Lock):
            owner = self._lock_owner.get(nxt.lock)
            return owner is None or owner == t
        return True

    def enabled_threads(self) -> list[int]:
        return [t for t in range(len(self._pc)) if self.enabled(t)]

    def deadlocked(self) -> bool:
        """Deadlock: nobody finished everything, yet nobody can move."""
        return not self.all_finished() and not self.enabled_threads()

    def step(self, t: int) -> None:
        if not self.enabled(t):
            raise IllegalStateError(f"thread {t} is not enabled")
        spec = self._program.threads[t]
        ins = spec.code[self._pc[t]]
        regs = self._registers[t]
        nxt = self._pc[t] + 1
        match ins:
            case Load(var=var, reg=reg):
                regs[reg] = self.memory.get(var, 0)
            case Store(var=var, reg=reg):
                self.memory[var] = regs.get(reg, 0)
            case Add(reg=reg, k=k):
                regs[reg] = add_exact(regs.get(reg, 0), k)
            case AtomicAdd(var=var, k=k):
                self.memory[var] = add_exact(self.memory.get(var, 0), k)
            case Lock(lock=lock):
                self._lock_owner[lock] = t
            case Unlock(lock=lock):
                if self._lock_owner.get(lock) != t:
                    raise IllegalStateError(f"{spec.name} unlocks {lock} it does not hold")
                del self._lock_owner[lock]
            case BranchIfLess(reg=reg, k=k, target=target):
                if regs.get(reg, 0) < k:
                    nxt = target
        self._pc[t] = nxt
        self._trace.append(TraceStep(spec.name, ins))

    def trace(self) -> tuple[TraceStep, ...]:
        return tuple(self._trace)

    def memory_snapshot(self) -> dict[str, int]:
        return dict(self.memory)


@dataclass(frozen=True)
class ExplorationResult:
    """What an exploration found. `counterexample` is the first schedule that broke the invariant or deadlocked."""

    schedules_explored: int
    violations: int
    deadlocks: int
    counterexample: Optional[tuple]          # Optional<List<TraceStep>>: None when nothing went wrong
    final_memory: Optional[dict]

    def clean(self) -> bool:
        return self.violations == 0 and self.deadlocks == 0


class Explorer:
    """`exhaustive` enumerates every interleaving by depth-first search over scheduling choices and replays each
    from the initial state (stateless model checking). `random` samples schedules with a seeded generator for
    programs too large to enumerate. Both are single-threaded and deterministic."""

    def __init__(self) -> None:
        self._explored = self._violations = self._deadlocks = 0
        self._counterexample: Optional[tuple] = None
        self._final_memory: Optional[dict] = None

    @staticmethod
    def exhaustive(program: Program, invariant: Invariant) -> ExplorationResult:
        e = Explorer()
        e._dfs(program, invariant, [])
        return e._result()

    @staticmethod
    def random(program: Program, invariant: Invariant, seed: int, schedules: int) -> ExplorationResult:
        e = Explorer()
        rng = JavaRandom(seed)
        for _ in range(schedules):
            m = Machine(program)
            while not m.all_finished() and not m.deadlocked():
                choices = m.enabled_threads()
                m.step(choices[rng.next_int(len(choices))])
            e._record(m, invariant)
        return e._result()

    def _dfs(self, program: Program, invariant: Invariant, prefix: list[int]) -> None:
        m = Explorer._replay(program, prefix)
        if m.all_finished() or m.deadlocked():
            self._record(m, invariant)
            return
        for t in m.enabled_threads():
            prefix.append(t)
            self._dfs(program, invariant, prefix)
            prefix.pop()

    @staticmethod
    def _replay(program: Program, schedule: list[int]) -> Machine:
        m = Machine(program)
        for t in schedule:
            m.step(t)
        return m

    def _record(self, m: Machine, invariant: Invariant) -> None:
        self._explored += 1
        bad = False
        if m.deadlocked():
            self._deadlocks += 1
            bad = True
        elif not invariant.holds(m.memory):
            self._violations += 1
            bad = True
        if bad and self._counterexample is None:
            self._counterexample = m.trace()
            self._final_memory = m.memory_snapshot()

    def _result(self) -> ExplorationResult:
        return ExplorationResult(self._explored, self._violations, self._deadlocks,
                                 self._counterexample, self._final_memory)


class Programs:
    """The programs this lesson checks. Every one mirrors a real class from Lesson 02 or Lesson 07."""

    @staticmethod
    def racy_increment(initial: int, delta: int) -> Program:
        """Two threads each do `balance += delta` as load/add/store. The Lesson 02 lost update."""
        body = [Load("balance", "r"), Add("r", delta), Store("balance", "r")]
        return Program({"balance": initial}, [ThreadSpec("T1", body), ThreadSpec("T2", body)])

    @staticmethod
    def locked_increment(initial: int, delta: int) -> Program:
        """The same increment under one lock: Lesson 02's LockedCounter."""
        body = [Lock("L"), Load("balance", "r"), Add("r", delta), Store("balance", "r"), Unlock("L")]
        return Program({"balance": initial}, [ThreadSpec("T1", body), ThreadSpec("T2", body)])

    @staticmethod
    def check_then_act_debit(initial: int, debit: int) -> Program:
        """Two debits that each check the balance, then subtract atomically. A bare atomic cannot do a debit."""
        body = [
            Load("balance", "r"),             # 0: check...
            BranchIfLess("r", debit, 3),      # 1: insufficient -> skip to end
            AtomicAdd("balance", -debit),     # 2: ...then act, atomically but too late
        ]
        return Program({"balance": initial}, [ThreadSpec("D1", body), ThreadSpec("D2", body)])

    @staticmethod
    def transfer_unordered() -> Program:
        """Transfer A->B while B->A, each locking source then destination. Lesson 07's deadlock."""
        ab = [Lock("A"), Lock("B"), Load("a", "x"), Add("x", -10), Store("a", "x"),
              Load("b", "y"), Add("y", 10), Store("b", "y"), Unlock("B"), Unlock("A")]
        ba = [Lock("B"), Lock("A"), Load("b", "x"), Add("x", -10), Store("b", "x"),
              Load("a", "y"), Add("y", 10), Store("a", "y"), Unlock("A"), Unlock("B")]
        return Program({"a": 100, "b": 100}, [ThreadSpec("AtoB", ab), ThreadSpec("BtoA", ba)])

    @staticmethod
    def transfer_ordered() -> Program:
        """The same two transfers with locks always taken in alphabetical order. Lesson 07's fix."""
        ab = [Lock("A"), Lock("B"), Load("a", "x"), Add("x", -10), Store("a", "x"),
              Load("b", "y"), Add("y", 10), Store("b", "y"), Unlock("B"), Unlock("A")]
        ba = [Lock("A"), Lock("B"), Load("b", "x"), Add("x", -10), Store("b", "x"),
              Load("a", "y"), Add("y", 10), Store("a", "y"), Unlock("B"), Unlock("A")]
        return Program({"a": 100, "b": 100}, [ThreadSpec("AtoB", ab), ThreadSpec("BtoA", ba)])

    @staticmethod
    def balance_equals(expected: int) -> Invariant:
        return Invariant(f"balance == {expected}", lambda m: m.get("balance", 0) == expected)

    @staticmethod
    def balance_non_negative() -> Invariant:
        return Invariant("balance >= 0", lambda m: m.get("balance", 0) >= 0)

    @staticmethod
    def conserved(total: int) -> Invariant:
        return Invariant(f"a + b == {total}", lambda m: m.get("a", 0) + m.get("b", 0) == total)


# ---------------------------------------------------------------------------------------------
# Part 2: a stress harness with real threads, for the code the model abstracts.
# ---------------------------------------------------------------------------------------------


def _preemption_point() -> None:
    """A no-op call. CPython only switches threads at "eval breaker" checks (calls, loop back-edges), so without
    a call between the load and the store the GIL makes Lesson 02's window effectively atomic and the stress test
    would never see the race the model checker proves. This call is where a real JIT or a real CPU would interleave."""


class BalanceCounter:
    def add(self, delta: int) -> None:
        raise NotImplementedError

    def get(self) -> int:
        raise NotImplementedError


class RacyCounter(BalanceCounter):
    """Lesson 02's bug, plus the one preemption point CPython needs to expose it."""

    def __init__(self) -> None:
        self._balance = 0

    def add(self, delta: int) -> None:
        loaded = self._balance
        _preemption_point()
        self._balance = loaded + delta

    def get(self) -> int:
        return self._balance


class LockedCounter(BalanceCounter):
    """Lesson 02's first fix, unchanged."""

    def __init__(self) -> None:
        self._lock = threading.Lock()
        self._balance = 0

    def add(self, delta: int) -> None:
        with self._lock:
            loaded = self._balance
            _preemption_point()
            self._balance = loaded + delta

    def get(self) -> int:
        with self._lock:
            return self._balance


@dataclass(frozen=True)
class StressResult:
    rounds: int
    rounds_with_lost_updates: int
    total_lost: int
    expected_per_round: int


class StressTest:
    """Runs `threads` real threads that each add `delta` `ops_per_thread` times, `rounds` times over, releasing
    every round through a barrier so the threads collide as hard as the runtime allows (here: a 10-microsecond
    GIL switch interval). A stress test can show a race; it can never show the absence of one."""

    SWITCH_INTERVAL_SECONDS = 1e-5

    @staticmethod
    def run(factory: Callable[[], BalanceCounter], threads: int, ops_per_thread: int, delta: int,
            rounds: int) -> StressResult:
        expected = threads * ops_per_thread * delta
        bad_rounds, total_lost = 0, 0
        previous = sys.getswitchinterval()
        sys.setswitchinterval(StressTest.SWITCH_INTERVAL_SECONDS)
        try:
            for _ in range(rounds):
                counter = factory()
                barrier = threading.Barrier(threads)

                def work() -> None:
                    try:
                        barrier.wait()
                    except threading.BrokenBarrierError:
                        return
                    for _ in range(ops_per_thread):
                        counter.add(delta)

                ts = [threading.Thread(target=work) for _ in range(threads)]
                for t in ts:
                    t.start()
                for t in ts:
                    t.join()
                lost = expected - counter.get()
                if lost != 0:
                    bad_rounds += 1
                    total_lost += lost
        finally:
            sys.setswitchinterval(previous)
        return StressResult(rounds, bad_rounds, total_lost, expected)


def format_memory(memory: dict) -> str:
    """Java's Map.toString for these programs: `{balance=12500}`, keys in HashMap order (alphabetical here)."""
    return "{" + ", ".join(f"{k}={memory[k]}" for k in sorted(memory)) + "}"


def report(title: str, p: Program, r: ExplorationResult) -> None:
    print(f"-- {title} --")
    print(f"interleavings (upper bound) = {p.interleavings_upper_bound()}, explored = {r.schedules_explored}"
          f", violations = {r.violations}, deadlocks = {r.deadlocks}")
    if r.counterexample is not None:
        print(f"first counterexample, final memory {format_memory(r.final_memory)}:")
        for s in r.counterexample:
            print(f"    {s}")


def main() -> None:
    print("== Exhaustive: balance += 25.00 USD twice, from 100.00 USD (minor units) ==")
    racy = Programs.racy_increment(10_000, 2_500)
    report("racy load/add/store", racy, Explorer.exhaustive(racy, Programs.balance_equals(15_000)))
    locked = Programs.locked_increment(10_000, 2_500)
    report("same increment under a lock", locked, Explorer.exhaustive(locked, Programs.balance_equals(15_000)))

    print()
    print("== Exhaustive: two 1.00 USD debits from 1.50 USD, each checks first ==")
    debit = Programs.check_then_act_debit(150, 100)
    report("check-then-act debit", debit, Explorer.exhaustive(debit, Programs.balance_non_negative()))

    print()
    print("== Exhaustive: transfer A->B while B->A ==")
    unordered = Programs.transfer_unordered()
    report("locks taken source-then-destination", unordered, Explorer.exhaustive(unordered, Programs.conserved(200)))
    ordered = Programs.transfer_ordered()
    report("locks taken in a global order", ordered, Explorer.exhaustive(ordered, Programs.conserved(200)))

    print()
    print("== Random schedules (seed 42): same bug, sampled instead of enumerated ==")
    sampled = Explorer.random(racy, Programs.balance_equals(15_000), 42, 1_000)
    print(f"1000 random schedules of the racy program: violations = {sampled.violations}")
    again = Explorer.random(racy, Programs.balance_equals(15_000), 42, 1_000)
    print(f"same seed again: violations = {again.violations} (identical: {str(sampled == again).lower()})")

    print()
    print("== Stress with real threads: 4 threads x 20000 adds, 20 rounds, barrier start ==")
    racy_stress = StressTest.run(RacyCounter, 4, 20_000, 1, 20)
    locked_stress = StressTest.run(LockedCounter, 4, 20_000, 1, 20)
    print(f"racy  : rounds with lost updates = {racy_stress.rounds_with_lost_updates} of {racy_stress.rounds}"
          " (nondeterministic: this line may differ between runs)")
    print(f"locked: rounds with lost updates = {locked_stress.rounds_with_lost_updates} of {locked_stress.rounds}"
          " (the lock makes this exact)")
    print(f"The model checker proved the racy program wrong in "
          f"{Explorer.exhaustive(racy, Programs.balance_equals(15_000)).schedules_explored}"
          " schedules; the stress test only made it likely.")


if __name__ == "__main__":
    main()
