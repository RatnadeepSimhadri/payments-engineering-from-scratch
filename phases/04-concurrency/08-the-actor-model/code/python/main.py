"""Lesson: phases/04-concurrency/08-the-actor-model/docs/en.md
The Actor Model
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: Hewitt, Bishop & Steiger, "A Universal Modular ACTOR Formalism for Artificial Intelligence" (IJCAI 1973);
Armstrong, "Making reliable distributed systems in the presence of software errors" (PhD thesis, 2003); Akka docs.
"""

from __future__ import annotations

import threading
from collections import deque
from concurrent.futures import Executor, ThreadPoolExecutor
from dataclasses import dataclass
from enum import Enum
from typing import Callable, Optional

INT64_MIN, INT64_MAX = -(1 << 63), (1 << 63) - 1


def checked_int64(value: int) -> int:
    """Math.addExact / subtractExact: Python ints never overflow, so the Java bound is enforced by hand."""
    if not INT64_MIN <= value <= INT64_MAX:
        raise OverflowError("long overflow")
    return value


# ---------------------------------------------------------------------------
# Money (copied from Phase 10, Lesson 01)
# ---------------------------------------------------------------------------


@dataclass(frozen=True)
class Currency:
    """ISO 4217 currency: code and minor-unit exponent."""

    code: str
    exponent: int

    def __post_init__(self) -> None:
        if self.code is None or len(self.code) != 3:
            raise ValueError(f"currency code must be three letters: {self.code}")
        if not 0 <= self.exponent <= 4:
            raise ValueError(f"exponent must be 0..4: {self.exponent}")


USD = Currency("USD", 2)
EUR = Currency("EUR", 2)


class CurrencyMismatchError(ValueError):
    def __init__(self, a: Currency, b: Currency) -> None:
        super().__init__(f"cannot combine {a.code} with {b.code}")


@dataclass(frozen=True)
class Money:
    """Exact amount in minor units of one currency. Immutable, which is what makes it safe to put in a message."""

    minor_units: int
    currency: Currency

    @staticmethod
    def of_minor(minor_units: int, currency: Currency) -> "Money":
        return Money(minor_units, currency)

    def _require_same_currency(self, other: "Money") -> None:
        if self.currency != other.currency:
            raise CurrencyMismatchError(self.currency, other.currency)

    def plus(self, other: "Money") -> "Money":
        self._require_same_currency(other)
        return Money(checked_int64(self.minor_units + other.minor_units), self.currency)

    def minus(self, other: "Money") -> "Money":
        self._require_same_currency(other)
        return Money(checked_int64(self.minor_units - other.minor_units), self.currency)

    def is_negative(self) -> bool:
        return self.minor_units < 0

    def compare_to(self, other: "Money") -> int:
        self._require_same_currency(other)
        return (self.minor_units > other.minor_units) - (self.minor_units < other.minor_units)

    def __str__(self) -> str:
        scale = 10 ** self.currency.exponent
        whole, frac = abs(self.minor_units) // scale, abs(self.minor_units) % scale
        frac_text = "" if self.currency.exponent == 0 else "." + str(frac).zfill(self.currency.exponent)
        return ("-" if self.minor_units < 0 else "") + str(whole) + frac_text + " " + self.currency.code


# ---------------------------------------------------------------------------
# Small stand-ins for java.util.Random and java.util.concurrent.atomic / CountDownLatch
# ---------------------------------------------------------------------------


class JavaRandom:
    """java.util.Random's 48-bit LCG, ported exactly so a seed picks the same interleaving in Java and Python."""

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
            if ((u - r + m) & 0xFFFFFFFF) < 0x80000000:  # int overflow check from the JDK source
                return r
            u = self.next(31)


class AtomicInteger:
    """A lock-guarded counter: the GIL makes `x += 1` look atomic but it is three bytecodes, not one."""

    def __init__(self, value: int = 0) -> None:
        self._value = value
        self._lock = threading.Lock()

    def increment_and_get(self) -> int:
        with self._lock:
            self._value += 1
            return self._value

    def decrement_and_get(self) -> int:
        with self._lock:
            self._value -= 1
            return self._value

    def accumulate_max(self, x: int) -> int:
        with self._lock:
            self._value = max(self._value, x)
            return self._value

    def get(self) -> int:
        with self._lock:
            return self._value


class AtomicBoolean:
    def __init__(self, value: bool = False) -> None:
        self._value = value
        self._lock = threading.Lock()

    def compare_and_set(self, expect: bool, update: bool) -> bool:
        with self._lock:
            if self._value != expect:
                return False
            self._value = update
            return True

    def set(self, value: bool) -> None:
        with self._lock:
            self._value = value

    def get(self) -> bool:
        with self._lock:
            return self._value


class CountDownLatch:
    def __init__(self, count: int) -> None:
        self._count = count
        self._cond = threading.Condition()

    def count_down(self) -> None:
        with self._cond:
            if self._count > 0:
                self._count -= 1
                if self._count == 0:
                    self._cond.notify_all()

    def await_(self, timeout: Optional[float] = None) -> bool:
        with self._cond:
            return self._cond.wait_for(lambda: self._count == 0, timeout)


# ---------------------------------------------------------------------------
# The actor runtime: messages, references, mailboxes, cells, supervision, schedulers.
# ---------------------------------------------------------------------------


class Message:
    """Every message is an immutable frozen dataclass. Actors share nothing else."""


@dataclass(frozen=True)
class Deposit(Message):
    correlation_id: str
    amount: Money
    reply_to: Optional["ActorRef"]


@dataclass(frozen=True)
class Withdraw(Message):
    correlation_id: str
    amount: Money
    reply_to: Optional["ActorRef"]


@dataclass(frozen=True)
class GetBalance(Message):
    reply_to: "ActorRef"


@dataclass(frozen=True)
class Deposited(Message):
    correlation_id: str
    account: str
    balance: Money


@dataclass(frozen=True)
class Withdrawn(Message):
    correlation_id: str
    account: str
    amount: Money
    balance: Money


@dataclass(frozen=True)
class InsufficientFunds(Message):
    correlation_id: str
    account: str
    requested: Money
    balance: Money


@dataclass(frozen=True)
class BalanceIs(Message):
    account: str
    balance: Money


@dataclass(frozen=True)
class Transfer(Message):
    transfer_id: str
    from_: str
    to: str
    amount: Money
    reply_to: Optional["ActorRef"]


@dataclass(frozen=True)
class TransferResult(Message):
    transfer_id: str
    ok: bool
    reason: str


@dataclass(frozen=True)
class Increment(Message):
    pass


@dataclass(frozen=True)
class CountIs(Message):
    count: int


@dataclass(frozen=True)
class Crash(Message):
    """Makes any actor throw: the supervision demo's fault injector."""

    reason: str


class Actor:
    """What an actor does. One instance, one mailbox, one message at a time."""

    def __init__(self) -> None:
        self._context: Optional[ActorContext] = None

    def bind(self, context: "ActorContext") -> None:
        self._context = context

    def context(self) -> "ActorContext":
        return self._context

    def self_ref(self) -> "ActorRef":
        return self._context.self_ref()

    def receive(self, message: Message) -> None:
        raise NotImplementedError

    def pre_start(self) -> None:
        """Called after a RESTART before the first message of the new incarnation. Default: nothing."""


class ActorContext:
    """What the runtime gives an actor: who it is, and how to reach others. Never another actor's object."""

    def __init__(self, self_ref: "ActorRef", system: "ActorSystem") -> None:
        self._self = self_ref
        self._system = system

    def self_ref(self) -> "ActorRef":
        return self._self

    def lookup(self, name: str) -> "ActorRef":
        return self._system.ref(name)


class SupervisorStrategy(Enum):
    RESUME = "RESUME"    # drop the failing message, keep the actor's state
    RESTART = "RESTART"  # drop the failing message, replace the actor from its factory; mailbox survives
    STOP = "STOP"        # drop the actor; later messages are dead letters


class ActorRef:
    """The only handle other code ever holds. tell() is the only operation."""

    def __init__(self, name: str, cell: "ActorCell") -> None:
        self._name = name
        self._cell = cell

    def name(self) -> str:
        return self._name

    def tell(self, message: Message) -> None:
        if message is None:
            raise TypeError("message")
        self._cell.enqueue(message)

    def __repr__(self) -> str:
        return self._name


class ActorCell:
    """Runtime bookkeeping for one actor: its mailbox, its current instance, its factory, and its counters."""

    def __init__(self, name: str, factory: Callable[[], Actor], strategy: SupervisorStrategy, system: "ActorSystem") -> None:
        self.name = name
        self.factory = factory
        self.strategy = strategy
        self.mailbox: deque[Message] = deque()            # append/popleft are thread-safe, like ConcurrentLinkedQueue
        self.scheduled = AtomicBoolean(False)             # ThreadedScheduler: at most one run queued or running
        self.concurrent_receives = AtomicInteger()        # invariant check: never exceeds 1
        self.max_concurrent_receives = AtomicInteger()
        self.processed = AtomicInteger()
        self.restarts = AtomicInteger()
        self.failures = AtomicInteger()
        self.dead_letters = AtomicInteger()
        self.stopped = False
        self._actor: Optional[Actor] = None
        self._system = system
        self.self_ref: Optional[ActorRef] = None

    def start(self) -> None:
        self.self_ref = ActorRef(self.name, self)
        self._actor = self.factory()
        self._actor.bind(ActorContext(self.self_ref, self._system))
        self._actor.pre_start()

    def enqueue(self, message: Message) -> None:
        if self.stopped:
            self.dead_letters.increment_and_get()
            self._system.dead_letter(self.name, message)
            return
        self.mailbox.append(message)
        self._system.scheduler().schedule(self)

    def process_one(self) -> bool:
        """Process one message. Returns False if the mailbox was empty. The scheduler guarantees no overlap."""
        try:
            message = self.mailbox.popleft()
        except IndexError:
            return False
        if self.stopped:
            self.dead_letters.increment_and_get()
            self._system.dead_letter(self.name, message)
            return True
        now = self.concurrent_receives.increment_and_get()
        self.max_concurrent_receives.accumulate_max(now)
        try:
            self._system.trace(self.name, message)
            self._actor.receive(message)
            self.processed.increment_and_get()
        except Exception as e:  # RuntimeException: supervision decides
            self.failures.increment_and_get()
            self._supervise(message, e)
        finally:
            self.concurrent_receives.decrement_and_get()
        return True

    def _supervise(self, message: Message, e: Exception) -> None:
        self._system.failure(self.name, message, e, self.strategy)
        if self.strategy is SupervisorStrategy.RESUME:
            pass  # state kept, message dropped
        elif self.strategy is SupervisorStrategy.RESTART:
            self.restarts.increment_and_get()
            self._actor = self.factory()
            self._actor.bind(ActorContext(self.self_ref, self._system))
            self._actor.pre_start()
        elif self.strategy is SupervisorStrategy.STOP:
            self.stopped = True
            while True:
                try:
                    m = self.mailbox.popleft()
                except IndexError:
                    break
                self.dead_letters.increment_and_get()
                self._system.dead_letter(self.name, m)


class Scheduler:
    """How mailboxes get processor time. One deterministic for tests, one threaded for production."""

    def schedule(self, cell: ActorCell) -> None:
        raise NotImplementedError


class DeterministicScheduler(Scheduler):
    """Single-threaded, replayable. Each step picks one runnable actor (seeded JavaRandom, or round-robin with
    seed 0) and delivers exactly one message. The interleaving is a function of the seed."""

    def __init__(self, seed: int) -> None:
        self._runnable: list[ActorCell] = []
        self._random = JavaRandom(seed)
        self._round_robin = seed == 0
        self._steps = 0

    def schedule(self, cell: ActorCell) -> None:
        if cell not in self._runnable:
            self._runnable.append(cell)

    def step(self) -> bool:
        """Deliver one message to one actor. Returns False when nothing is runnable."""
        if not self._runnable:
            return False
        if self._round_robin:
            cell = self._runnable.pop(0)
        else:
            index = self._random.next_int(len(self._runnable))
            cell = self._runnable.pop(index)
        cell.process_one()
        self._steps += 1
        if cell.mailbox and cell not in self._runnable:
            self._runnable.append(cell)
        return True

    def run_until_idle(self) -> int:
        delivered = 0
        while self.step():
            delivered += 1
        return delivered

    def steps(self) -> int:
        return self._steps


class ThreadedScheduler(Scheduler):
    """Multi-threaded. The CAS on `scheduled` is the whole guarantee that receive() never runs on two threads."""

    def __init__(self, executor: Executor, throughput: int) -> None:
        self._executor = executor
        self._throughput = throughput

    def schedule(self, cell: ActorCell) -> None:
        if cell.scheduled.compare_and_set(False, True):
            self._executor.submit(self._run, cell)

    def _run(self, cell: ActorCell) -> None:
        try:
            for _ in range(self._throughput):  # drain a batch
                if not cell.process_one():
                    break
        finally:
            cell.scheduled.set(False)          # release first ...
            if cell.mailbox:                   # ... then re-check: a message may have landed in between
                self.schedule(cell)


class ActorSystem:
    """Owns the cells, the scheduler, and the trace."""

    def __init__(self, scheduler: Scheduler, tracing: bool) -> None:
        self._scheduler = scheduler
        self._cells: dict[str, ActorCell] = {}
        self._trace: list[str] = []
        self._failures: list[str] = []
        self._dead_letters = AtomicInteger()
        self._tracing = tracing
        self._lock = threading.Lock()

    def scheduler(self) -> Scheduler:
        return self._scheduler

    def spawn(self, name: str, factory: Callable[[], Actor], strategy: SupervisorStrategy) -> ActorRef:
        with self._lock:
            if name in self._cells:
                raise ValueError(f"actor already exists: {name}")
            cell = ActorCell(name, factory, strategy, self)
            cell.start()
            self._cells[name] = cell
            return cell.self_ref

    def ref(self, name: str) -> ActorRef:
        cell = self._cells.get(name)
        if cell is None:
            raise ValueError(f"no such actor: {name}")
        return cell.self_ref

    def cell(self, name: str) -> Optional[ActorCell]:
        return self._cells.get(name)

    def trace(self, actor: Optional[str] = None, message: Optional[Message] = None) -> list[str]:
        """With arguments, records a delivery; without, returns the trace (Java's trace(..) and trace())."""
        if actor is not None and self._tracing:
            self._trace.append(f"{actor} <- {describe(message)}")
        return self._trace

    def failure(self, actor: str, message: Message, e: Exception, strategy: SupervisorStrategy) -> None:
        self._failures.append(f"{actor} failed on {describe(message)}: {e} -> {strategy.value}")

    def dead_letter(self, actor: str, message: Message) -> None:
        self._dead_letters.increment_and_get()

    def failures(self) -> list[str]:
        return self._failures

    def dead_letters(self) -> int:
        return self._dead_letters.get()


def describe(m: Message) -> str:
    match m:
        case Deposit():
            return f"Deposit({m.correlation_id}, {m.amount})"
        case Withdraw():
            return f"Withdraw({m.correlation_id}, {m.amount})"
        case Deposited():
            return f"Deposited({m.correlation_id}, {m.account}, {m.balance})"
        case Withdrawn():
            return f"Withdrawn({m.correlation_id}, {m.account}, {m.amount})"
        case InsufficientFunds():
            return f"InsufficientFunds({m.correlation_id}, {m.account}, wanted {m.requested}, has {m.balance})"
        case Transfer():
            return f"Transfer({m.transfer_id}, {m.from_} -> {m.to}, {m.amount})"
        case TransferResult():
            return f"TransferResult({m.transfer_id}, {'ok' if m.ok else 'failed: ' + m.reason})"
        case GetBalance():
            return "GetBalance"
        case BalanceIs():
            return f"BalanceIs({m.account}, {m.balance})"
        case Increment():
            return "Increment"
        case CountIs():
            return f"CountIs({m.count})"
        case Crash():
            return f"Crash({m.reason})"
    raise TypeError(f"not a message: {m!r}")


# ---------------------------------------------------------------------------
# The payments actors: one account = one actor, a coordinator for transfers, a probe for tests.
# ---------------------------------------------------------------------------


class AccountActor(Actor):
    """Owns one balance. No lock anywhere: the mailbox serialises every access."""

    def __init__(self, account_id: str, opening: Money) -> None:
        super().__init__()
        if opening.is_negative():
            raise ValueError("opening balance must not be negative")
        self._account_id = account_id
        self._balance = opening

    def receive(self, message: Message) -> None:
        match message:
            case Deposit(correlation_id=cid, amount=amount, reply_to=reply_to):
                self._balance = self._balance.plus(amount)  # raises CurrencyMismatchError for a wrong-currency deposit
                if reply_to is not None:
                    reply_to.tell(Deposited(cid, self._account_id, self._balance))
            case Withdraw(correlation_id=cid, amount=amount, reply_to=reply_to):
                if self._balance.compare_to(amount) < 0:  # check ...
                    if reply_to is not None:
                        reply_to.tell(InsufficientFunds(cid, self._account_id, amount, self._balance))
                else:
                    self._balance = self._balance.minus(amount)  # ... and act: nothing can interleave between them
                    if reply_to is not None:
                        reply_to.tell(Withdrawn(cid, self._account_id, amount, self._balance))
            case GetBalance(reply_to=reply_to):
                reply_to.tell(BalanceIs(self._account_id, self._balance))
            case Crash(reason=reason):
                raise RuntimeError(reason)
            case _:
                raise ValueError(f"account does not understand {describe(message)}")

    def balance(self) -> Money:
        return self._balance


class TransferCoordinator(Actor):
    """Moves money with two messages: Withdraw from the source, then Deposit to the destination."""

    def __init__(self) -> None:
        super().__init__()
        self._in_flight: dict[str, Transfer] = {}
        self._completed = 0
        self._rejected = 0

    def receive(self, message: Message) -> None:
        match message:
            case Transfer():
                self._in_flight[message.transfer_id] = message
                self.context().lookup(message.from_).tell(Withdraw(message.transfer_id, message.amount, self.self_ref()))
            case Withdrawn():
                t = self._in_flight[message.correlation_id]
                self.context().lookup(t.to).tell(Deposit(t.transfer_id, t.amount, self.self_ref()))
            case Deposited():
                t = self._in_flight.pop(message.correlation_id)
                self._completed += 1
                if t.reply_to is not None:
                    t.reply_to.tell(TransferResult(t.transfer_id, True, ""))
            case InsufficientFunds():
                t = self._in_flight.pop(message.correlation_id)
                self._rejected += 1
                if t.reply_to is not None:
                    t.reply_to.tell(TransferResult(t.transfer_id, False,
                                                   f"{message.account} has {message.balance}, needs {message.requested}"))
            case _:
                raise ValueError(f"coordinator does not understand {describe(message)}")

    def completed(self) -> int:
        return self._completed

    def rejected(self) -> int:
        return self._rejected

    def in_flight(self) -> int:
        return len(self._in_flight)


class CounterActor(Actor):
    """Counts increments; used to show what RESTART does to state and RESUME does not."""

    def __init__(self) -> None:
        super().__init__()
        self._count = 0

    def receive(self, message: Message) -> None:
        match message:
            case Increment():
                self._count += 1
            case GetBalance(reply_to=reply_to):
                reply_to.tell(CountIs(self._count))
            case Crash(reason=reason):
                raise RuntimeError(reason)
            case _:
                raise ValueError(f"counter does not understand {describe(message)}")


class ProbeActor(Actor):
    """A test inbox: records everything it receives."""

    def __init__(self, expected: int = 0) -> None:
        super().__init__()
        self.received: list[Message] = []
        self.latch = CountDownLatch(expected)

    def receive(self, message: Message) -> None:
        self.received.append(message)
        self.latch.count_down()

    def transfer_results(self) -> list[TransferResult]:
        return [m for m in self.received if isinstance(m, TransferResult)]

    def balances(self) -> dict[str, Money]:
        return {m.account: m.balance for m in self.received if isinstance(m, BalanceIs)}


def usd(minor: int) -> Money:
    return Money.of_minor(minor, USD)


def bank_with(scheduler: Scheduler, tracing: bool) -> ActorSystem:
    """Builds three accounts and a coordinator on the given scheduler. Total money in the system: 300.00 USD."""
    system = ActorSystem(scheduler, tracing)
    system.spawn("acct-A", lambda: AccountActor("acct-A", usd(10_000)), SupervisorStrategy.RESUME)
    system.spawn("acct-B", lambda: AccountActor("acct-B", usd(10_000)), SupervisorStrategy.RESUME)
    system.spawn("acct-C", lambda: AccountActor("acct-C", usd(10_000)), SupervisorStrategy.RESUME)
    system.spawn("transfers", TransferCoordinator, SupervisorStrategy.RESUME)
    return system


def actor_of(system: ActorSystem, name: str) -> Actor:
    """Test-only peek at the actor instance; production code never does this, which is the point of the model."""
    return system.cell(name)._actor


def total(system: ActorSystem, *accounts: str) -> Money:
    s = usd(0)
    for a in accounts:
        s = s.plus(actor_of(system, a).balance())
    return s


def scripted_transfers(reply_to: ActorRef) -> list[Transfer]:
    return [
        Transfer("t1", "acct-A", "acct-B", usd(7_500), reply_to),
        Transfer("t2", "acct-B", "acct-C", usd(12_000), reply_to),
        Transfer("t3", "acct-C", "acct-A", usd(5_000), reply_to),
        Transfer("t4", "acct-A", "acct-C", usd(9_000), reply_to),
        Transfer("t5", "acct-B", "acct-A", usd(2_500), reply_to),
        Transfer("t6", "acct-C", "acct-B", usd(1_000), reply_to),
    ]


def demo_deterministic(seed: int) -> None:
    scheduler = DeterministicScheduler(seed)
    system = bank_with(scheduler, True)
    probe = system.spawn("probe", ProbeActor, SupervisorStrategy.RESUME)
    for t in scripted_transfers(probe):
        system.ref("transfers").tell(t)
    delivered = scheduler.run_until_idle()
    p = actor_of(system, "probe")
    print(f"seed {seed}: {delivered} messages delivered, {len(system.trace())} traced")
    for r in p.transfer_results():
        print("  " + describe(r))
    print(f"  balances: A={actor_of(system, 'acct-A').balance()} B={actor_of(system, 'acct-B').balance()}"
          f" C={actor_of(system, 'acct-C').balance()}  total={total(system, 'acct-A', 'acct-B', 'acct-C')}")


def main() -> None:
    print("== One account, one mailbox: six transfers on a deterministic scheduler ==")
    demo_deterministic(42)
    print("first 6 trace lines (seed 42):")
    s42 = DeterministicScheduler(42)
    sys42 = bank_with(s42, True)
    probe42 = sys42.spawn("probe", ProbeActor, SupervisorStrategy.RESUME)
    for t in scripted_transfers(probe42):
        sys42.ref("transfers").tell(t)
    s42.run_until_idle()
    for line in sys42.trace()[:6]:
        print("  " + line)
    print()
    print("Same script, different seed: a different interleaving, the same conservation of money.")
    demo_deterministic(7)
    print()

    print("== Supervision: RESUME keeps state, RESTART resets it, STOP turns the mailbox into dead letters ==")
    sched = DeterministicScheduler(0)
    system = ActorSystem(sched, False)
    probe = system.spawn("probe", ProbeActor, SupervisorStrategy.RESUME)
    account = system.spawn("acct-X", lambda: AccountActor("acct-X", usd(5_000)), SupervisorStrategy.RESUME)
    resumed = system.spawn("counter-resume", CounterActor, SupervisorStrategy.RESUME)
    restarted = system.spawn("counter-restart", CounterActor, SupervisorStrategy.RESTART)
    stopped = system.spawn("counter-stop", CounterActor, SupervisorStrategy.STOP)
    account.tell(Deposit("d1", Money.of_minor(100, EUR), probe))  # wrong currency: receive() raises
    account.tell(GetBalance(probe))
    for c in (resumed, restarted, stopped):
        c.tell(Increment())
        c.tell(Increment())
        c.tell(Crash("simulated bug"))
        c.tell(Increment())
        c.tell(GetBalance(probe))
    sched.run_until_idle()
    for f in system.failures():
        print("  " + f)
    p = actor_of(system, "probe")
    print(f"  acct-X balance after the bad deposit was dropped: {p.balances()['acct-X']}")
    counts = [m.count for m in p.received if isinstance(m, CountIs)]
    print(f"  counter-resume reports {counts[0]} (2 before the crash + 1 after); counter-restart reports {counts[1]}"
          f" (state reset); counter-stop reports nothing, dead letters = {system.dead_letters()}")
    print()

    print("== Threaded scheduler: 4 threads x 25,000 deposits into one account, no locks in AccountActor ==")
    with ThreadPoolExecutor(max_workers=4) as pool:
        threaded = ActorSystem(ThreadedScheduler(pool, 64), False)
        acct = threaded.spawn("acct-hot", lambda: AccountActor("acct-hot", usd(0)), SupervisorStrategy.RESUME)
        threads, per_thread = 4, 25_000
        done = CountDownLatch(threads)

        def produce() -> None:
            for _ in range(per_thread):
                acct.tell(Deposit("x", usd(1), None))
            done.count_down()

        for _ in range(threads):
            pool.submit(produce)
        done.await_()
        probe_ref = threaded.spawn("probe", lambda: ProbeActor(1), SupervisorStrategy.RESUME)
        acct.tell(GetBalance(probe_ref))
        pr = actor_of(threaded, "probe")
        pr.latch.await_(10)
        cell = threaded.cell("acct-hot")
        print(f"  balance = {pr.balances().get('acct-hot')} (expected 1000.00 USD); messages processed = {cell.processed.get()}"
              f"; max concurrent receive() = {cell.max_concurrent_receives.get()}")


if __name__ == "__main__":
    main()
