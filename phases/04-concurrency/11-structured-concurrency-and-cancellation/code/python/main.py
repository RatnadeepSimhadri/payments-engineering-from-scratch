"""Lesson: phases/04-concurrency/11-structured-concurrency-and-cancellation/docs/en.md
Structured Concurrency and Cancellation
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: JEP 453 (Structured Concurrency) for the scope/fork/join shape; Go `context` package docs for deadline
and cancellation propagation. Python 3.11+, standard library only, threads (not asyncio) to mirror the Java design.
"""

from __future__ import annotations

import threading
from dataclasses import dataclass
from enum import Enum
from typing import Any, Callable, Generic, Optional, TypeVar

T = TypeVar("T")

INT64_MAX = (1 << 63) - 1
NANOS_PER_MILLI = 1_000_000


def millis(n: int) -> int:
    """Duration.ofMillis(n) as integer nanoseconds: durations are plain ints so a clock never rounds."""
    return n * NANOS_PER_MILLI


def add_exact(a: int, b: int) -> int:
    """Math.addExact on a long: Python ints never overflow, so the Java bound is enforced by hand."""
    r = a + b
    if r > INT64_MAX or r < -(1 << 63):
        raise OverflowError("long overflow")
    return r


@dataclass(frozen=True)
class Currency:
    """ISO 4217 currency, copied from Phase 10 Lesson 01: minor units + currency, never a float."""

    code: str
    exponent: int

    def __post_init__(self) -> None:
        if self.code is None or len(self.code) != 3:
            raise ValueError(f"currency code: {self.code}")
        if not 0 <= self.exponent <= 4:
            raise ValueError(f"exponent must be 0..4: {self.exponent}")


USD = Currency("USD", 2)


@dataclass(frozen=True)
class Money:
    minor_units: int
    currency: Currency

    @staticmethod
    def of_minor(minor_units: int, currency: Currency) -> "Money":
        return Money(minor_units, currency)

    def covers(self, other: "Money") -> bool:
        if self.currency != other.currency:
            raise ValueError("currency mismatch")
        return self.minor_units >= other.minor_units

    def __str__(self) -> str:
        scale = 10 ** self.currency.exponent
        whole, frac = abs(self.minor_units) // scale, abs(self.minor_units) % scale
        f = "" if self.currency.exponent == 0 else "." + str(frac).zfill(self.currency.exponent)
        return ("-" if self.minor_units < 0 else "") + str(whole) + f + " " + self.currency.code


class Clock:
    """A monotonic clock in nanoseconds. Injected so deadlines can be tested without sleeping."""

    def nanos(self) -> int:
        raise NotImplementedError


class SystemClock(Clock):
    def nanos(self) -> int:
        import time
        return time.monotonic_ns()


class ManualClock(Clock):
    """A clock that only moves when the test or the demo moves it."""

    def __init__(self) -> None:
        self._now = 0
        self._lock = threading.Lock()

    def nanos(self) -> int:
        with self._lock:
            return self._now

    def advance(self, d_nanos: int) -> None:
        with self._lock:
            self._now += d_nanos


@dataclass(frozen=True)
class Deadline:
    """An absolute instant on the injected clock by which work must be done. NONE never expires.
    Deadlines compose by taking the earliest, which is how a child inherits its parent's budget."""

    at_nanos: int

    @staticmethod
    def after(clock: Clock, d_nanos: int) -> "Deadline":
        return Deadline(add_exact(clock.nanos(), d_nanos))

    def earliest(self, other: "Deadline") -> "Deadline":
        return self if self.at_nanos <= other.at_nanos else other

    def is_expired(self, clock: Clock) -> bool:
        return self.at_nanos != INT64_MAX and clock.nanos() >= self.at_nanos

    def remaining(self, clock: Clock) -> int:
        """Nanoseconds left (Java returns a Duration); never negative."""
        if self.at_nanos == INT64_MAX:
            return INT64_MAX
        return max(0, self.at_nanos - clock.nanos())

    def is_none(self) -> bool:
        return self.at_nanos == INT64_MAX


Deadline.NONE = Deadline(INT64_MAX)  # type: ignore[attr-defined]


class CancelledException(RuntimeError):
    """Raised by cooperative code when it notices its token has been cancelled."""


class ExecutionException(Exception):
    """java.util.concurrent.ExecutionException: wraps the first failure; the original is `__cause__`."""


class CancellationToken:
    """A cooperative cancellation signal arranged in a tree: cancelling a parent cancels every descendant;
    cancelling a child never touches the parent. Expiry of the effective deadline becomes a cancellation lazily
    the next time anyone asks. This is Go's `context.Context` in forty lines."""

    def __init__(self, parent: Optional["CancellationToken"], clock: Clock, deadline: Deadline) -> None:
        if clock is None or deadline is None:
            raise TypeError("clock and deadline are required")
        self._parent = parent
        self._clock = clock
        self._deadline = deadline
        self._reason: Optional[str] = None
        self._lock = threading.Lock()          # stands in for AtomicReference.compareAndSet
        self._callbacks: list[Callable[[], Any]] = []
        self._children: list[CancellationToken] = []

    @staticmethod
    def root(clock: Clock) -> "CancellationToken":
        return CancellationToken(None, clock, Deadline.NONE)

    def child(self, own: Deadline = Deadline.NONE) -> "CancellationToken":
        """A child whose deadline is the earlier of its own and its parent's."""
        c = CancellationToken(self, self._clock, self._deadline.earliest(own))
        self._children.append(c)
        if self.is_cancelled():
            c.cancel(self._reason)
        return c

    def deadline(self) -> Deadline:
        return self._deadline

    def cancel(self, why: str) -> bool:
        """Idempotent. The first reason wins; callbacks run exactly once, then children are cancelled."""
        if why is None:
            raise TypeError("why")
        with self._lock:                       # compareAndSet(null, why)
            if self._reason is not None:
                return False
            self._reason = why
        for r in list(self._callbacks):        # snapshot, like iterating a CopyOnWriteArrayList
            r()
        for c in list(self._children):
            c.cancel(why)
        return True

    def is_cancelled(self) -> bool:
        """True once cancelled explicitly or once the deadline has passed on the clock."""
        if self._reason is None and self._deadline.is_expired(self._clock):
            self.cancel("deadline exceeded")
        return self._reason is not None

    def reason(self) -> Optional[str]:
        self.is_cancelled()
        return self._reason

    def on_cancel(self, callback: Callable[[], Any]) -> None:
        """Registers cleanup. If already cancelled the callback runs now, so registration can never miss it."""
        self._callbacks.append(callback)
        if self.is_cancelled():
            callback()

    def throw_if_cancelled(self) -> None:
        if self.is_cancelled():
            raise CancelledException(self._reason)

    def parent(self) -> Optional["CancellationToken"]:
        return self._parent


class Subtask(Generic[T]):
    """The handle a scope returns from fork. Values are available only after join."""

    class State(Enum):
        UNAVAILABLE = "UNAVAILABLE"
        SUCCESS = "SUCCESS"
        FAILED = "FAILED"
        CANCELLED = "CANCELLED"

    def __init__(self, name: str) -> None:
        self._name = name
        self._state = Subtask.State.UNAVAILABLE
        self._value: Optional[T] = None
        self._failure: Optional[BaseException] = None
        self._runner: Optional[threading.Thread] = None   # guarded by _lock; non-None only while the body runs
        self._interrupted = threading.Event()
        self._lock = threading.Lock()

    def started(self, t: threading.Thread) -> None:
        with self._lock:
            self._runner = t

    def finished(self) -> None:
        with self._lock:
            self._runner = None

    def interrupt(self) -> None:
        """Python threads cannot be interrupted; this sets a flag a body may poll, only while it is running."""
        with self._lock:
            if self._runner is not None:
                self._interrupted.set()

    def interrupted(self) -> bool:
        return self._interrupted.is_set()

    def name(self) -> str:
        return self._name

    def state(self) -> "Subtask.State":
        return self._state

    def get(self) -> T:
        if self._state is not Subtask.State.SUCCESS:
            raise RuntimeError(f"{self._name} is {self._state.value}, not SUCCESS")
        return self._value  # type: ignore[return-value]

    def exception(self) -> Optional[BaseException]:
        return self._failure

    def succeed(self, v: T) -> None:
        self._value = v
        self._state = Subtask.State.SUCCESS

    def fail(self, t: BaseException) -> None:
        self._failure = t
        self._state = Subtask.State.FAILED

    def cancelled(self) -> None:
        self._state = Subtask.State.CANCELLED


class ThreadPerTaskExecutor:
    """Executors.newVirtualThreadPerTaskExecutor(): one new (daemon) thread per task, never a shared queue."""

    def __init__(self) -> None:
        self._threads: list[threading.Thread] = []
        self._lock = threading.Lock()

    def execute(self, fn: Callable[[], None]) -> None:
        t = threading.Thread(target=fn, daemon=True)
        with self._lock:
            self._threads.append(t)
        t.start()

    def shutdown(self) -> None:
        pass

    def await_termination(self, timeout_seconds: float) -> bool:
        with self._lock:
            threads = list(self._threads)
        for t in threads:
            t.join(timeout_seconds)
        return all(not t.is_alive() for t in threads)


class StructuredScope:
    """A structured scope: every task forked inside it is joined before the scope exits, no exceptions.
    Policy is shutdown-on-failure: the first failure cancels the siblings. The deadline is inherited from the
    parent token and tightened by the scope's own budget. close() guarantees no forked task is still running."""

    def __init__(self, name: str, executor: ThreadPerTaskExecutor, parent: CancellationToken,
                 deadline: Deadline = Deadline.NONE, clock: Optional[Clock] = None) -> None:
        if name is None or executor is None or clock is None:
            raise TypeError("name, executor and clock are required")
        self._name = name
        self._executor = executor
        self._clock = clock
        self._token = parent.child(deadline)
        self._subtasks: list[Subtask[Any]] = []
        self._running = 0
        self._completion = threading.Condition()      # guards _running and wakes join()
        self._joined = False
        self._closed = False
        self._first_failure: Optional[Subtask[Any]] = None
        self._failure_lock = threading.Lock()

    def __enter__(self) -> "StructuredScope":
        return self

    def __exit__(self, *exc: object) -> None:
        self.close()

    def token(self) -> CancellationToken:
        return self._token

    def deadline(self) -> Deadline:
        return self._token.deadline()

    def name(self) -> str:
        return self._name

    def fork(self, task_name: str, body: Callable[[CancellationToken], T]) -> Subtask[T]:
        if self._joined or self._closed:
            raise RuntimeError(f"{self._name}: cannot fork after join or close")
        subtask: Subtask[T] = Subtask(task_name)
        self._subtasks.append(subtask)
        with self._completion:
            self._running += 1

        def run() -> None:
            subtask.started(threading.current_thread())
            try:
                if self._token.is_cancelled():
                    subtask.cancelled()
                else:
                    subtask.succeed(body(self._token))
            except (CancelledException, InterruptedError):
                subtask.cancelled()
            except BaseException as t:  # Throwable
                if self._token.is_cancelled():
                    subtask.cancelled()
                else:
                    subtask.fail(t)
                    self._on_failure(subtask)
            finally:
                subtask.finished()
                with self._completion:
                    self._running -= 1
                    self._completion.notify_all()

        self._executor.execute(run)
        return subtask

    def _on_failure(self, failed: Subtask[Any]) -> None:
        with self._failure_lock:
            if self._first_failure is None:
                self._first_failure = failed
        self._token.cancel(f"{self._name}: {failed.name()} failed: {failed.exception()!r}")
        self._interrupt_running()

    def _interrupt_running(self) -> None:
        for s in self._subtasks:
            s.interrupt()

    def join(self) -> None:
        """Waits until every subtask has finished, or the deadline passes (then cancels and still waits for every
        subtask to acknowledge). Polls in short real slices so a manual clock advanced by another thread is noticed;
        never sleeps for simulated time."""
        self._joined = True
        while self.active() > 0:
            if self._token.is_cancelled():
                self._interrupt_running()
            with self._completion:
                if self._running > 0:
                    self._completion.wait(0.005)
        for s in self._subtasks:
            if s.state() is Subtask.State.UNAVAILABLE:
                s.cancelled()

    def throw_if_failed(self) -> None:
        """After join: re-raises the first failure. A cancelled-by-deadline scope raises TimeoutError."""
        if not self._joined:
            raise RuntimeError(f"{self._name}: throw_if_failed before join")
        f = self._first_failure
        if f is not None:
            raise ExecutionException(f"{f.name()} failed") from f.exception()
        if self._token.is_cancelled() and self._token.reason().startswith("deadline"):
            raise TimeoutError(f"{self._name}: {self._token.reason()}")

    def is_cancelled(self) -> bool:
        return self._token.is_cancelled()

    def cancellation_reason(self) -> Optional[str]:
        return self._token.reason()

    def active(self) -> int:
        """Number of forked tasks that have not yet finished; zero after join or close."""
        with self._completion:
            return self._running

    def subtasks(self) -> tuple[Subtask[Any], ...]:
        return tuple(self._subtasks)

    def close(self) -> None:
        """Leaving the scope without joining is a bug; close cancels what is left and waits for it anyway."""
        if self._closed:
            return
        self._closed = True
        if not self._joined:
            self._token.cancel(f"{self._name}: scope closed without join")
            self.join()


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


class AtomicInteger:
    def __init__(self) -> None:
        self._v = 0
        self._lock = threading.Lock()

    def increment_and_get(self) -> int:
        with self._lock:
            self._v += 1
            return self._v

    def get(self) -> int:
        with self._lock:
            return self._v


def authorize(pool: ThreadPerTaskExecutor, parent: CancellationToken, clock: Clock, budget_nanos: int,
              balance: Money, amount: Money, fraud_fails: bool, slow_bank: Optional[ManualClock],
              log: list[str]) -> str:
    """A fan-out authorization: three checks, one answer. All three share one token and one deadline."""
    siblings_started = CountDownLatch(2)   # makes the demo's ordering deterministic
    with StructuredScope("authorize", pool, parent, Deadline.after(clock, budget_nanos), clock) as scope:
        def fraud_check(token: CancellationToken) -> bool:
            siblings_started.await_()
            if fraud_fails:
                raise RuntimeError("velocity rule tripped")
            return True

        def balance_check(token: CancellationToken) -> bool:
            token.on_cancel(lambda: log.append("balance-check: released hold"))
            siblings_started.count_down()
            wait_for_signal_or_cancel(token)
            return balance.covers(amount)

        def fx_quote(token: CancellationToken) -> int:
            token.on_cancel(lambda: log.append("fx-quote: closed quote stream"))
            siblings_started.count_down()
            if slow_bank is not None:
                slow_bank.advance(budget_nanos + millis(1))   # the bank is slower than our budget
            wait_for_signal_or_cancel(token)
            return 10_000

        fraud = scope.fork("fraud-check", fraud_check)
        funds = scope.fork("balance-check", balance_check)
        fx = scope.fork("fx-quote", fx_quote)
        scope.join()
        try:
            scope.throw_if_failed()
        except ExecutionException as e:
            return f"DECLINED ({e.__cause__})"
        except TimeoutError as e:
            return f"TIMEOUT ({e})"
        return f"APPROVED at rate {fx.get()}" if fraud.get() and funds.get() else "DECLINED (insufficient funds)"


def wait_for_signal_or_cancel(token: CancellationToken) -> None:
    """Simulates a remote call that completes only when cancelled or when a signal arrives; here nobody signals."""
    monitor = threading.Condition()

    def wake() -> None:
        with monitor:
            monitor.notify_all()

    token.on_cancel(wake)
    with monitor:
        while not token.is_cancelled():
            monitor.wait(0.005)
    token.throw_if_cancelled()


def main() -> None:
    pool = ThreadPerTaskExecutor()
    clock = SystemClock()
    balance = Money.of_minor(10_000, USD)
    amount = Money.of_minor(2_500, USD)

    print("== Deadlines compose by taking the earliest ==")
    mc = ManualClock()
    root = CancellationToken.root(mc)
    request = root.child(Deadline.after(mc, millis(200)))
    bank_call = request.child(Deadline.after(mc, millis(800)))
    print(f"request budget  = {request.deadline().remaining(mc) // NANOS_PER_MILLI} ms")
    print(f"bank-call asked = 800 ms, got {bank_call.deadline().remaining(mc) // NANOS_PER_MILLI} ms (inherited)")
    mc.advance(millis(250))
    print(f"after 250 ms: request cancelled? {str(request.is_cancelled()).lower()} ({request.reason()})")
    print(f"              bank-call cancelled? {str(bank_call.is_cancelled()).lower()}, "
          f"root cancelled? {str(root.is_cancelled()).lower()}")

    print()
    print("== One sibling fails: the others are cancelled and clean up ==")
    log: list[str] = []
    r1 = authorize(pool, CancellationToken.root(clock), clock, millis(5_000), balance, amount, True, None, log)
    print(r1)
    for line in sorted(log):
        print("  cleanup: " + line)

    print()
    print("== The bank is slower than the budget: deadline cancels everything ==")
    slow = ManualClock()
    log2: list[str] = []
    r2 = authorize(pool, CancellationToken.root(slow), slow, millis(300), balance, amount, False, slow, log2)
    print(r2)
    for line in sorted(log2):
        print("  cleanup: " + line)

    print()
    print("== Leaving a scope without join still waits for every child ==")
    finished = AtomicInteger()

    def worker(token: CancellationToken) -> None:
        wait_for_signal_or_cancel(token)
        finished.increment_and_get()

    with StructuredScope("leaky", pool, CancellationToken.root(clock), clock=clock) as scope:
        leaked = scope
        for i in range(3):
            scope.fork(f"worker-{i}", worker)
        # no join: the with block exits early, say because of an early return
    print(f"scope cancelled on close: {leaked.cancellation_reason()}")
    cancelled = sum(1 for s in leaked.subtasks() if s.state() is Subtask.State.CANCELLED)
    print(f"subtasks cancelled = {cancelled} of 3; completed normally = {finished.get()}; "
          f"still running after close = {leaked.active()}")

    pool.shutdown()
    pool.await_termination(5)


if __name__ == "__main__":
    main()
