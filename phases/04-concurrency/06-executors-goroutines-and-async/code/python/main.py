"""Lesson: phases/04-concurrency/06-executors-goroutines-and-async/docs/en.md
Executors, Goroutines, and asyncio: one fan-out/fan-in problem on a thread pool, a thread per task, and futures.
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: PEP 3148 (concurrent.futures); Python asyncio documentation (gather, wait_for); JEP 444 (Virtual Threads);
java.util.concurrent.CompletableFuture Javadoc. Time is injected through Timer; nothing sleeps for real.
"""

from __future__ import annotations

import asyncio
import threading
import time
from concurrent.futures import CancelledError, Executor, Future, InvalidStateError, ThreadPoolExecutor
from dataclasses import dataclass
from enum import Enum
from typing import Callable, Optional

LONG_MIN = -(1 << 63)
LONG_MAX = (1 << 63) - 1


@dataclass(frozen=True)
class Currency:
    """ISO 4217 currency: code and minor-unit exponent. Copied from Phase 10, Lesson 01."""

    code: str
    exponent: int

    def __post_init__(self) -> None:
        if self.code is None or len(self.code) != 3:
            raise ValueError(f"currency code must be three letters: {self.code}")
        if self.exponent < 0 or self.exponent > 4:
            raise ValueError(f"exponent must be 0..4: {self.exponent}")


USD = Currency("USD", 2)
EUR = Currency("EUR", 2)


@dataclass(frozen=True)
class Money:
    """Exact amount in minor units of one currency. Immutable, so a quote can be shared across threads freely."""

    minor_units: int
    currency: Currency

    def __post_init__(self) -> None:
        if self.currency is None:
            raise TypeError("currency")
        if not isinstance(self.minor_units, int) or isinstance(self.minor_units, bool):
            raise TypeError("minor_units must be an int; never a float")

    @staticmethod
    def of_minor(minor_units: int, currency: Currency) -> Money:
        return Money(minor_units, currency)

    def _require_same_currency(self, other: Money) -> None:
        if self.currency != other.currency:
            raise ValueError(f"cannot combine {self.currency.code} with {other.currency.code}")

    def plus(self, other: Money) -> Money:
        self._require_same_currency(other)
        total = self.minor_units + other.minor_units
        if total < LONG_MIN or total > LONG_MAX:  # Math.addExact: Python ints never overflow on their own
            raise OverflowError("long overflow")
        return Money(total, self.currency)

    def compare_to(self, other: Money) -> int:
        self._require_same_currency(other)
        return (self.minor_units > other.minor_units) - (self.minor_units < other.minor_units)

    def __lt__(self, other: Money) -> bool:
        return self.compare_to(other) < 0

    def __str__(self) -> str:
        scale = 10 ** self.currency.exponent
        whole, frac = divmod(abs(self.minor_units), scale)  # on abs(): no floor-vs-truncate difference
        frac_text = "" if self.currency.exponent == 0 else f".{frac:0{self.currency.exponent}d}"
        return f"{'-' if self.minor_units < 0 else ''}{whole}{frac_text} {self.currency.code}"


@dataclass(frozen=True)
class PaymentRequest:
    payment_id: str
    amount: Money


class Outcome(Enum):
    OK = "OK"
    TIMED_OUT = "TIMED_OUT"
    FAILED = "FAILED"


@dataclass(frozen=True)
class Quote:
    """One processor's answer: a fee quote, or why there is none."""

    processor: str
    fee: Optional[Money]
    outcome: Outcome
    detail: str

    @staticmethod
    def ok(processor: str, fee: Money) -> Quote:
        return Quote(processor, fee, Outcome.OK, "")

    @staticmethod
    def timed_out(processor: str, after_millis: int) -> Quote:
        return Quote(processor, None, Outcome.TIMED_OUT, f"no answer within {after_millis} ms")

    @staticmethod
    def failed(processor: str, cause: BaseException) -> Quote:
        # Java unwraps CompletionException here; concurrent.futures never wraps, so the cause is already the cause.
        return Quote(processor, None, Outcome.FAILED, f"{type(cause).__name__}: {cause}")


# ---------------------------------------------------------------------------------------------------------------
# CompletableFuture, the parts the lesson uses, over concurrent.futures.Future. Like CompletableFuture, a Future runs
# its done-callbacks synchronously in the thread that completes it, so composition is deterministic. complete() is
# Java's "complete if not already done": the first completion wins and later ones are no-ops.
# ---------------------------------------------------------------------------------------------------------------

def complete(f: Future, value) -> bool:
    try:
        f.set_result(value)
        return True
    except InvalidStateError:
        return False


def complete_exceptionally(f: Future, exc: BaseException) -> bool:
    try:
        f.set_exception(exc)
        return True
    except InvalidStateError:
        return False


def completed_future(value) -> Future:
    f: Future = Future()
    f.set_result(value)
    return f


def _propagate(src: Future, dst: Future, fn: Callable) -> None:
    """Copy src's outcome into dst, mapping a normal result through fn."""
    if src.cancelled():
        complete_exceptionally(dst, CancelledError())
        return
    exc = src.exception()
    if exc is not None:
        complete_exceptionally(dst, exc)
        return
    try:
        complete(dst, fn(src.result()))
    except Exception as e:  # a throwing fn completes the dependent exceptionally, like thenApply
        complete_exceptionally(dst, e)


def then_apply(src: Future, fn: Callable) -> Future:
    dst: Future = Future()
    src.add_done_callback(lambda s: _propagate(s, dst, fn))
    return dst


def when_complete(src: Future, action: Callable[[], None]) -> None:
    src.add_done_callback(lambda s: action())


def apply_to_either(a: Future, b: Future) -> Future:
    """Completes with whichever of a or b completes first (a is checked first when both are already done)."""
    dst: Future = Future()
    a.add_done_callback(lambda s: _propagate(s, dst, lambda v: v))
    b.add_done_callback(lambda s: _propagate(s, dst, lambda v: v))
    return dst


def exceptionally(src: Future, fn: Callable[[BaseException], object]) -> Future:
    dst: Future = Future()

    def on_done(s: Future) -> None:
        if s.cancelled():
            complete(dst, fn(CancelledError()))
        elif s.exception() is not None:
            complete(dst, fn(s.exception()))
        else:
            complete(dst, s.result())

    src.add_done_callback(on_done)
    return dst


def all_of(futures: list[Future]) -> Future:
    """CompletableFuture.allOf(...).thenApply(join each): a future of every result, in input order."""
    dst: Future = Future()
    if not futures:
        dst.set_result([])
        return dst
    remaining = [len(futures)]
    lock = threading.Lock()

    def on_done(_: Future) -> None:
        with lock:
            remaining[0] -= 1
            last = remaining[0] == 0
        if last:
            complete(dst, [f.result() for f in futures])  # result() cannot block: every future is done

    for f in futures:
        f.add_done_callback(on_done)
    return dst


class AtomicInteger:
    def __init__(self, value: int = 0) -> None:
        self._value = value
        self._lock = threading.Lock()

    def get(self) -> int:
        return self._value

    def increment_and_get(self) -> int:
        with self._lock:
            self._value += 1
            return self._value

    def decrement_and_get(self) -> int:
        with self._lock:
            self._value -= 1
            return self._value


class CountDownLatch:
    """java.util.concurrent.CountDownLatch: the stdlib has Event and Barrier but no count-down gate."""

    def __init__(self, count: int) -> None:
        self._count = count
        self._cond = threading.Condition()

    def count_down(self) -> None:
        with self._cond:
            if self._count > 0:
                self._count -= 1
                if self._count == 0:
                    self._cond.notify_all()

    def await_(self) -> None:
        with self._cond:
            self._cond.wait_for(lambda: self._count == 0)


class ProcessorAdapter:
    """A card processor's quote endpoint. The real one is an HTTP call; here it is whatever the adapter decides."""

    def name(self) -> str:
        raise NotImplementedError

    def quote(self, request: PaymentRequest) -> Future:
        raise NotImplementedError


class ScriptedAdapter(ProcessorAdapter):
    """Deterministic stand-in for a remote processor. Immediate adapters answer at once; scripted ones answer when
    the test or demo calls respond or fail; a hung adapter is a scripted one nobody ever answers."""

    def __init__(self, name: str, immediate_fee: Optional[Money]) -> None:
        self._name = name
        self._immediate_fee = immediate_fee  # None -> scripted
        self._pending: list[Future] = []
        self._calls = AtomicInteger()
        self._lock = threading.Lock()  # Java: synchronized

    @staticmethod
    def immediate(name: str, fee: Money) -> ScriptedAdapter:
        return ScriptedAdapter(name, fee)

    @staticmethod
    def scripted(name: str) -> ScriptedAdapter:
        return ScriptedAdapter(name, None)

    def name(self) -> str:
        return self._name

    def quote(self, request: PaymentRequest) -> Future:
        with self._lock:
            self._calls.increment_and_get()
            if self._immediate_fee is not None:
                return completed_future(Quote.ok(self._name, self._immediate_fee))
            f: Future = Future()
            self._pending.append(f)
            return f

    def respond(self, fee: Money) -> None:
        """Answer every outstanding call with this fee."""
        with self._lock:
            pending, self._pending = self._pending, []
        for f in pending:
            complete(f, Quote.ok(self._name, fee))

    def fail(self, message: str) -> None:
        with self._lock:
            pending, self._pending = self._pending, []
        for f in pending:
            complete_exceptionally(f, RuntimeError(message))  # Java: IllegalStateException

    def calls(self) -> int:
        return self._calls.get()

    def outstanding(self) -> int:
        with self._lock:
            return len(self._pending)


class Timer:
    """The one place time enters the design. Production uses RealTimer; tests and the demo advance a ManualTimer."""

    def after(self, millis: int) -> Future:
        raise NotImplementedError

    def now_millis(self) -> int:
        raise NotImplementedError


class RealTimer(Timer):
    """Wall-clock alarms on threading.Timer, the counterpart of CompletableFuture.delayedExecutor. Not used by tests."""

    def __init__(self) -> None:
        self._start = time.monotonic_ns()

    def after(self, millis: int) -> Future:
        f: Future = Future()
        t = threading.Timer(millis / 1000.0, lambda: complete(f, None))
        t.daemon = True
        t.start()
        return f

    def now_millis(self) -> int:
        return (time.monotonic_ns() - self._start) // 1_000_000


class ManualTimer(Timer):
    """Simulated clock: advance fires every timer whose deadline has passed, in deadline order (ties by creation)."""

    def __init__(self) -> None:
        self._pending: list[tuple[int, int, Future]] = []
        self._now = 0
        self._seq = 0
        self._lock = threading.Lock()

    def after(self, millis: int) -> Future:
        if millis < 0:
            raise ValueError("negative delay")
        with self._lock:
            f: Future = Future()
            self._pending.append((self._now + millis, self._seq, f))
            self._seq += 1
            return f

    def now_millis(self) -> int:
        with self._lock:
            return self._now

    def advance(self, millis: int) -> None:
        due = []
        with self._lock:
            self._now += millis
            self._pending.sort(key=lambda p: (p[0], p[1]))
            keep = []
            for p in self._pending:
                if p[2].cancelled():
                    continue  # the caller no longer needs this alarm
                if p[0] <= self._now:
                    due.append(p)
                else:
                    keep.append(p)
            self._pending = keep
        for p in due:
            complete(p[2], None)  # outside the lock: completion runs callbacks

    def outstanding(self) -> int:
        """Alarms that are still armed (not fired, not cancelled)."""
        with self._lock:
            return sum(1 for p in self._pending if not p[2].cancelled())


class FanOut:
    """Fan-out to every processor, fan-in to one list. Each adapter call races its own timeout; the combined future
    completes when every adapter has answered, failed, or been timed out. Nothing in here blocks a thread."""

    @staticmethod
    def with_timeout(adapter: ProcessorAdapter, request: PaymentRequest, timeout_millis: int, timer: Timer) -> Future:
        try:
            call = adapter.quote(request)
        except Exception as e:  # a synchronous throw is still a failed quote, not a crashed fan-out
            return completed_future(Quote.failed(adapter.name(), e))
        alarm = timer.after(timeout_millis)
        deadline = then_apply(alarm, lambda _: Quote.timed_out(adapter.name(), timeout_millis))
        # Register the race before the alarm cancellation: Future runs callbacks in registration order, so the call's
        # answer must reach apply_to_either before cancelling the alarm completes `deadline` with CancelledError.
        # (CompletableFuture keeps dependents on a stack and typically runs the newest first, which is why the Java
        # version can register in the opposite order.)
        either = apply_to_either(call, deadline)
        when_complete(call, lambda: alarm.cancel())  # the answer arrived: the alarm is no longer needed
        return exceptionally(either, lambda t: Quote.failed(adapter.name(), t))

    @staticmethod
    def quote_all(adapters: list[ProcessorAdapter], request: PaymentRequest, timeout_millis: int,
                  timer: Timer) -> Future:
        return all_of([FanOut.with_timeout(a, request, timeout_millis, timer) for a in adapters])

    @staticmethod
    def first_ok(adapters: list[ProcessorAdapter], request: PaymentRequest, timeout_millis: int,
                 timer: Timer) -> Future:
        """First adapter to answer OK wins; timeouts and failures are ignored unless everyone produced one.
        Completes with the winning Quote, or None (Java: Optional.empty())."""
        futures = [FanOut.with_timeout(a, request, timeout_millis, timer) for a in adapters]
        winner: Future = Future()
        remaining = AtomicInteger(len(futures))

        def on_quote(f: Future) -> None:
            q = f.result()
            if q.outcome is Outcome.OK:
                complete(winner, q)  # first OK completes; later ones are no-ops
            if remaining.decrement_and_get() == 0:
                complete(winner, None)  # everyone answered, none OK

        for f in futures:
            f.add_done_callback(on_quote)
        return winner

    @staticmethod
    def cheapest(quotes: list[Quote]) -> Optional[Quote]:
        ok = [q for q in quotes if q.outcome is Outcome.OK]
        return min(ok, key=lambda q: q.fee) if ok else None


class ThreadPerTaskExecutor(Executor):
    """Executors.newVirtualThreadPerTaskExecutor in shape: every submitted task gets a fresh thread, so a blocked task
    never waits for a pool slot. Python has no virtual threads; these are OS threads, so the cost per blocked task is
    a stack and a kernel thread rather than a few hundred bytes."""

    def __init__(self) -> None:
        self._threads: list[threading.Thread] = []
        self._lock = threading.Lock()

    def submit(self, fn, /, *args, **kwargs) -> Future:
        f: Future = Future()

        def run() -> None:
            if not f.set_running_or_notify_cancel():
                return
            try:
                f.set_result(fn(*args, **kwargs))
            except BaseException as e:
                f.set_exception(e)

        t = threading.Thread(target=run, daemon=True)
        with self._lock:
            self._threads.append(t)
        t.start()
        return f

    def shutdown(self, wait: bool = True, *, cancel_futures: bool = False) -> None:
        if wait:
            with self._lock:
                threads = list(self._threads)
            for t in threads:
                t.join()


def invoke_all(executor: Executor, tasks: list[Callable[[], object]]) -> list[Future]:
    """ExecutorService.invokeAll: submit every task and return only when every one is done."""
    futures = [executor.submit(t) for t in tasks]
    for f in futures:
        exc = f.exception()  # waits; does not raise the task's exception
        del exc
    return futures


class BlockingFanOut:
    """The same fan-out in the blocking style: one task per adapter, each blocks until its answer arrives. How well
    this works depends entirely on who supplies the threads, which is the point of the executor comparison."""

    @staticmethod
    def quote_all(executor: Executor, adapters: list[ProcessorAdapter], request: PaymentRequest) -> list[Quote]:
        tasks = [(lambda a=a: a.quote(request).result()) for a in adapters]  # result() blocks this task's thread
        out = []
        for i, f in enumerate(invoke_all(executor, tasks)):
            exc = f.exception()
            out.append(Quote.failed(adapters[i].name(), exc) if exc is not None else f.result())
        return out


class GatedTask:
    """A blocking task that records when it started and then waits for a gate. Used to show pool starvation."""

    def __init__(self, id: int, gate: threading.Event, started: AtomicInteger, started_latch: CountDownLatch) -> None:
        self._id = id
        self._gate = gate
        self._started = started
        self._started_latch = started_latch

    def __call__(self) -> int:
        self._started.increment_and_get()
        self._started_latch.count_down()
        self._gate.wait()  # a blocked thread: a card network that has not answered yet
        return self._id


def count_started_before_release(executor: Executor, tasks: int, expected_started: int) -> int:
    """Submit `tasks` blocking tasks, wait until `expected_started` are running, and report how many started.
    With a fixed pool of N < tasks exactly N start; with a thread per task, all of them do."""
    gate = threading.Event()  # CountDownLatch(1)
    started_latch = CountDownLatch(expected_started)
    started = AtomicInteger()
    futures = [executor.submit(GatedTask(i, gate, started, started_latch)) for i in range(tasks)]
    started_latch.await_()
    observed = started.get()  # tasks beyond the pool size cannot have started: no thread exists for them
    gate.set()
    for f in futures:
        f.result()
    return observed


async def wait_for_cancels_inner() -> tuple[bool, bool]:
    """The asyncio contrast: asyncio.wait_for cancels the awaited task on timeout, where Java's applyToEither and
    this module's FanOut only abandon the wait. Uses timeout=0, so nothing waits on a real clock.
    Returns (timed_out, inner_was_cancelled)."""
    never_answered = asyncio.Event()
    inner_cancelled = [False]

    async def quote() -> None:
        try:
            await never_answered.wait()
        except asyncio.CancelledError:
            inner_cancelled[0] = True
            raise

    task = asyncio.ensure_future(quote())
    await asyncio.sleep(0)  # let the inner task start and park on the event
    timed_out = False
    try:
        await asyncio.wait_for(task, timeout=0)
    except asyncio.TimeoutError:
        timed_out = True
    return timed_out, inner_cancelled[0] and task.cancelled()


def describe(quotes: list[Quote]) -> str:
    lines = []
    for q in quotes:
        line = "  %-8s%s" % (q.processor, q.outcome.value)
        line += f" fee {q.fee}" if q.outcome is Outcome.OK else f" ({q.detail})"
        lines.append(line + "\n")
    return "".join(lines)


def _b(value: bool) -> str:
    return "true" if value else "false"


def main() -> None:
    request = PaymentRequest("pay_001", Money.of_minor(12_999, USD))

    print("== Model 1: a fixed thread pool and blocking calls ==")
    with ThreadPoolExecutor(max_workers=2) as pool:
        started = count_started_before_release(pool, 3, 2)
        print(f"3 blocking adapter calls on a pool of 2 threads: {started} started;"
              " the third waits for a thread that is itself waiting")

    print()
    print("== Model 2: a thread per task, same blocking code (Python has no virtual threads) ==")
    with ThreadPerTaskExecutor() as per_task:
        started = count_started_before_release(per_task, 3, 3)
        print(f"3 blocking adapter calls on a thread per task: {started} started;"
              " no task waits for a pool slot, but each blocked one holds an OS thread")
        alpha = ScriptedAdapter.immediate("alpha", Money.of_minor(329, USD))
        beta = ScriptedAdapter.immediate("beta", Money.of_minor(301, USD))
        quotes = BlockingFanOut.quote_all(per_task, [alpha, beta], request)
        print(describe(quotes), end="")
        best = FanOut.cheapest(quotes)
        print(f"cheapest: {best.processor if best else 'none'}")

    print()
    print("== Model 3: Future fan-out with per-adapter timeouts (simulated clock) ==")
    timer = ManualTimer()
    alpha = ScriptedAdapter.immediate("alpha", Money.of_minor(329, USD))
    beta = ScriptedAdapter.scripted("beta")
    gamma = ScriptedAdapter.scripted("gamma")
    delta = ScriptedAdapter.scripted("delta")
    all_quotes = FanOut.quote_all([alpha, beta, gamma, delta], request, 800, timer)
    print(f"t={timer.now_millis()}ms  fan-out issued to 4 processors; alpha answered immediately;"
          f" combined future done: {_b(all_quotes.done())}")
    timer.advance(250)
    beta.respond(Money.of_minor(275, USD))
    print(f"t={timer.now_millis()}ms  beta answered; combined future done: {_b(all_quotes.done())}")
    timer.advance(300)
    delta.fail("HTTP 503 from processor")
    print(f"t={timer.now_millis()}ms  delta failed; gamma silent; combined future done: {_b(all_quotes.done())}")
    timer.advance(250)
    print(f"t={timer.now_millis()}ms  deadline passed; combined future done: {_b(all_quotes.done())}")
    quotes = all_quotes.result()
    print(describe(quotes), end="")
    best = FanOut.cheapest(quotes)
    print(f"cheapest OK quote: {f'{best.processor} at {best.fee}' if best else 'none'}")
    print(f"gamma still has {gamma.outstanding()} outstanding call: the timeout abandoned the wait,"
          " it did not cancel the request")

    print()
    print("== Fan-in variant: first OK wins ==")
    timer2 = ManualTimer()
    slow = ScriptedAdapter.scripted("slow")
    fast = ScriptedAdapter.scripted("fast")
    first = FanOut.first_ok([slow, fast], request, 800, timer2)
    timer2.advance(100)
    fast.respond(Money.of_minor(350, USD))
    winner = first.result()
    print(f"fast answered at t=100ms -> winner: {winner.processor if winner else 'none'}"
          " (slow never answered and nobody waited for it)")


if __name__ == "__main__":
    main()
