"""Lesson: phases/04-concurrency/02-locks-and-atomic-counters/docs/en.md
Locks, Atomics, and a Balance That Never Lies
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: Python threading module docs; CPython's GIL and switch interval (sys.setswitchinterval);
Herlihy & Shavit, The Art of Multiprocessor Programming, ch. 5.
Python 3.11+, standard library only. Same design as the Java and Go versions.
"""

from __future__ import annotations

import sys
import threading
import time
from dataclasses import dataclass
from typing import Callable, Protocol


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
EUR = Currency("EUR", 2)


class CurrencyMismatchError(ValueError):
    def __init__(self, a: Currency, b: Currency) -> None:
        super().__init__(f"cannot combine {a.code} with {b.code}")


class InsufficientFundsError(RuntimeError):
    def __init__(self, account_id: str, balance: int, requested: int) -> None:
        super().__init__(f"account {account_id} has {balance} minor units, debit of {requested} refused")


@dataclass(frozen=True)
class Money:
    """Exact amount in minor units of one currency; immutable, so safe to share between threads."""

    minor_units: int
    currency: Currency

    def __post_init__(self) -> None:
        if not isinstance(self.minor_units, int) or isinstance(self.minor_units, bool):
            raise TypeError("minor_units must be an int; never construct Money from a float")

    @staticmethod
    def of_minor(minor_units: int, currency: Currency) -> "Money":
        return Money(minor_units, currency)

    def _require_same_currency(self, other: "Money") -> None:
        if self.currency != other.currency:
            raise CurrencyMismatchError(self.currency, other.currency)

    def plus(self, other: "Money") -> "Money":
        self._require_same_currency(other)
        return Money(self.minor_units + other.minor_units, self.currency)

    def minus(self, other: "Money") -> "Money":
        self._require_same_currency(other)
        return Money(self.minor_units - other.minor_units, self.currency)

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


# ---------------------------------------------------------------------------
# Counters: one contract, three implementations plus the bug
# ---------------------------------------------------------------------------


class BalanceCounter(Protocol):
    def add(self, delta: int) -> None: ...
    def get(self) -> int: ...
    def name(self) -> str: ...


class RacyCounter:
    """The bug. `self.balance += delta` is LOAD_ATTR, BINARY_OP, STORE_ATTR: three bytecodes, and
    the GIL may switch threads between any two of them. The hook runs between the load and the
    store so a test can force the interleaving instead of hoping the interpreter produces it."""

    def __init__(self, between_load_and_store: Callable[[], None] | None = None) -> None:
        self.balance = 0
        self._between = between_load_and_store or (lambda: None)

    def add(self, delta: int) -> None:
        loaded = self.balance          # 1. load
        self._between()                #    (another thread may run here)
        self.balance = loaded + delta  # 2. store a value computed from a possibly stale load

    def get(self) -> int:
        return self.balance

    def name(self) -> str:
        return "racy"


class LockedCounter:
    """Fix 1: mutual exclusion. threading.Lock makes load-add-store indivisible; releasing and
    re-acquiring the lock is what publishes the write to the next thread."""

    def __init__(self) -> None:
        self._lock = threading.Lock()
        self._balance = 0

    def add(self, delta: int) -> None:
        with self._lock:
            self._balance += delta

    def get(self) -> int:
        with self._lock:
            return self._balance

    def name(self) -> str:
        return "locked"


class AtomicLong:
    """CPython has no user-level compare-and-swap, so this emulates one with a lock. The point of
    keeping the CAS shape is that the caller's retry loop is identical to Java and Go."""

    def __init__(self, value: int = 0) -> None:
        self._lock = threading.Lock()
        self._value = value

    def get(self) -> int:
        with self._lock:
            return self._value

    def compare_and_set(self, expected: int, new: int) -> bool:
        with self._lock:
            if self._value != expected:
                return False
            self._value = new
            return True

    def increment_and_get(self) -> int:
        with self._lock:
            self._value += 1
            return self._value


class AtomicCounter:
    """Fix 2: compare-and-swap. Load, compute, publish only if nobody published since the load;
    otherwise retry. The retry count is the contention you paid for."""

    def __init__(self) -> None:
        self._value = AtomicLong()
        self._retries = AtomicLong()

    def add(self, delta: int) -> None:
        while True:
            current = self._value.get()
            if self._value.compare_and_set(current, current + delta):
                return
            self._retries.increment_and_get()

    def get(self) -> int:
        return self._value.get()

    def name(self) -> str:
        return "atomic"

    def cas_retries(self) -> int:
        return self._retries.get()


def stable_hash(s: str) -> int:
    """The classic 31-multiplier string hash on 32-bit wrap-around; identical in Java, Python, Go.
    Python's built-in hash() is salted per process, so it cannot be used for a stripe index."""
    h = 0
    for ch in s:
        h = (31 * h + ord(ch)) & 0xFFFFFFFF
    return h - 0x100000000 if h >= 0x80000000 else h


class StripedAccountMap:
    """Fix 3: lock striping. Many accounts, a fixed number of locks; an account's stripe is a hash
    of its id. A debit is a check-then-act (enough balance? then subtract), which is one line
    under the stripe lock and impossible with a bare atomic."""

    def __init__(self, currency: Currency, stripes: int) -> None:
        if stripes <= 0:
            raise ValueError(f"stripes must be positive: {stripes}")
        self.currency = currency
        self._locks = [threading.Lock() for _ in range(stripes)]
        self._buckets: list[dict[str, int]] = [{} for _ in range(stripes)]

    def stripes(self) -> int:
        return len(self._locks)

    def stripe_index(self, account_id: str) -> int:
        return stable_hash(account_id) % len(self._locks)  # Python % is already non-negative here

    def open(self, account_id: str) -> None:
        s = self.stripe_index(account_id)
        with self._locks[s]:
            self._buckets[s].setdefault(account_id, 0)

    def credit(self, account_id: str, amount: Money) -> None:
        self._require_valid(amount)
        s = self.stripe_index(account_id)
        with self._locks[s]:
            current = self._require_open(s, account_id)
            self._buckets[s][account_id] = current + amount.minor_units

    def debit(self, account_id: str, amount: Money) -> None:
        self._require_valid(amount)
        s = self.stripe_index(account_id)
        with self._locks[s]:
            current = self._require_open(s, account_id)
            if current < amount.minor_units:
                raise InsufficientFundsError(account_id, current, amount.minor_units)
            self._buckets[s][account_id] = current - amount.minor_units

    def balance(self, account_id: str) -> Money:
        s = self.stripe_index(account_id)
        with self._locks[s]:
            return Money.of_minor(self._require_open(s, account_id), self.currency)

    def total(self) -> Money:
        """Takes every stripe lock in index order so the sum is a consistent snapshot."""
        for lock in self._locks:
            lock.acquire()
        try:
            return Money.of_minor(sum(sum(b.values()) for b in self._buckets), self.currency)
        finally:
            for lock in reversed(self._locks):
                lock.release()

    def total_minor(self) -> int:
        return self.total().minor_units

    def _require_open(self, stripe: int, account_id: str) -> int:
        current = self._buckets[stripe].get(account_id)
        if current is None:
            raise ValueError(f"no such account: {account_id}")
        return current

    def _require_valid(self, amount: Money) -> None:
        if amount.currency != self.currency:
            raise CurrencyMismatchError(self.currency, amount.currency)
        if amount.is_negative():
            raise ValueError(f"amount must not be negative: {amount}")


class Account:
    """An account whose balance lives in a counter. Money in, Money out; minor units inside."""

    def __init__(self, account_id: str, currency: Currency, counter: BalanceCounter) -> None:
        self.id = account_id
        self.currency = currency
        self._counter = counter

    def credit(self, amount: Money) -> None:
        self._require_valid(amount)
        self._counter.add(amount.minor_units)

    def debit(self, amount: Money) -> None:
        self._require_valid(amount)
        self._counter.add(-amount.minor_units)

    def balance(self) -> Money:
        return Money.of_minor(self._counter.get(), self.currency)

    def _require_valid(self, amount: Money) -> None:
        if amount.currency != self.currency:
            raise CurrencyMismatchError(self.currency, amount.currency)
        if amount.is_negative():
            raise ValueError(f"amount must not be negative: {amount}")


# ---------------------------------------------------------------------------
# Benchmark harness with an injected stopwatch
# ---------------------------------------------------------------------------


class Stopwatch(Protocol):
    def nanos(self) -> int: ...


class SystemStopwatch:
    def nanos(self) -> int:
        return time.perf_counter_ns()


class FakeStopwatch:
    """Returns scripted readings in order; repeats the last one."""

    def __init__(self, *readings: int) -> None:
        if not readings:
            raise ValueError("need at least one reading")
        self._readings = readings
        self._next = 0

    def nanos(self) -> int:
        value = self._readings[min(self._next, len(self._readings) - 1)]
        self._next += 1
        return value


@dataclass(frozen=True)
class BenchmarkResult:
    name: str
    threads: int
    ops_per_thread: int
    delta: int
    observed: int
    elapsed_nanos: int

    def expected(self) -> int:
        return self.threads * self.ops_per_thread * self.delta

    def lost(self) -> int:
        return self.expected() - self.observed

    def exact(self) -> bool:
        return self.lost() == 0

    def ops_per_second(self) -> float:
        ops = self.threads * self.ops_per_thread
        return float("inf") if self.elapsed_nanos <= 0 else ops * 1_000_000_000 / self.elapsed_nanos


Worker = Callable[[int, int], None]  # (thread_index, iteration)


def run_benchmark(name: str, threads: int, ops_per_thread: int, delta: int, worker: Worker,
                  observe: Callable[[], int], stopwatch: Stopwatch) -> BenchmarkResult:
    """Runs N threads, all released together by an Event, then reads the observed total."""
    if threads <= 0 or ops_per_thread < 0:
        raise ValueError("threads > 0, ops_per_thread >= 0")
    start = threading.Event()
    failures: list[BaseException] = []
    failures_lock = threading.Lock()

    def body(thread_index: int) -> None:
        try:
            start.wait()
            for i in range(ops_per_thread):
                worker(thread_index, i)
        except BaseException as exc:  # noqa: BLE001 - surfaced to the caller below
            with failures_lock:
                failures.append(exc)

    pool = [threading.Thread(target=body, args=(t,), name=f"bench-{t}") for t in range(threads)]
    for thread in pool:
        thread.start()
    before = stopwatch.nanos()
    start.set()                      # all threads begin together: maximum contention
    for thread in pool:
        thread.join()                # join() publishes everything the thread wrote
    after = stopwatch.nanos()
    if failures:
        raise RuntimeError(f"worker failed: {failures[0]!r}") from failures[0]
    return BenchmarkResult(name, threads, ops_per_thread, delta, observe(), after - before)


def measure(counter: BalanceCounter, threads: int, ops_per_thread: int, delta: int, stopwatch: Stopwatch) -> BenchmarkResult:
    return run_benchmark(counter.name(), threads, ops_per_thread, delta,
                         lambda t, i: counter.add(delta), counter.get, stopwatch)


def measure_map(account_map: StripedAccountMap, ids: list[str], threads: int, ops_per_thread: int, delta: int,
                stopwatch: Stopwatch) -> BenchmarkResult:
    amount = Money.of_minor(delta, account_map.currency)
    return run_benchmark(f"striped-map({account_map.stripes()})", threads, ops_per_thread, delta,
                         lambda t, i: account_map.credit(ids[t % len(ids)], amount), account_map.total_minor, stopwatch)


def line(r: BenchmarkResult, currency: Currency, note: str = "") -> str:
    return (f"{r.name:<16} observed {str(Money.of_minor(r.observed, currency)):>14}  "
            f"lost {str(Money.of_minor(r.lost(), currency)):>12}  {r.ops_per_second():>12,.0f} ops/s{note}")


def main() -> None:
    print("== A lost update, forced deterministically ==")
    holder: list[RacyCounter] = []
    fired = [False]

    def interleave() -> None:
        if not fired[0]:                # the first add pauses between load and store...
            fired[0] = True
            holder[0].add(1)            # ...while a second add runs to completion

    holder.append(RacyCounter(interleave))
    holder[0].add(1)
    print(f"two credits of 0.01 USD, balance = {Money.of_minor(holder[0].get(), USD)} (expected 0.02 USD): "
          "the second write overwrote the first")

    threads, ops = 4, 50_000
    print(f"\n== {threads} threads each credit 0.01 USD {ops:,} times: expected {Money.of_minor(threads * ops, USD)} ==")
    clock = SystemStopwatch()
    default_interval = sys.getswitchinterval()
    sys.setswitchinterval(1e-6)      # make the GIL hand over far more often than the 5 ms default
    racy = measure(RacyCounter(), threads, ops, 1, clock)
    sys.setswitchinterval(default_interval)
    print(line(racy, USD, "   <- varies run to run; with the default 5 ms switch interval it is rare, not absent"))
    print(line(measure(LockedCounter(), threads, ops, 1, clock), USD))
    atomic = AtomicCounter()
    print(line(measure(atomic, threads, ops, 1, clock), USD, f"   CAS retries: {atomic.cas_retries():,} (varies)"))
    ids = [f"acct-{n}" for n in range(1, 9)]
    account_map = StripedAccountMap(USD, 16)
    for account_id in ids:
        account_map.open(account_id)
    print(line(measure_map(account_map, ids, threads, ops, 1, clock), USD))
    print("(ops/s are wall-clock on this machine and vary; the lost column is the lesson)")

    print("\n== Check-then-act: a debit that cannot overdraw ==")
    account_map.credit("acct-1", Money.of_minor(500, USD))
    print(f"acct-1 balance {account_map.balance('acct-1')} on stripe {account_map.stripe_index('acct-1')}")
    try:
        account_map.debit("acct-1", Money.of_minor(1_000_000, USD))
    except InsufficientFundsError as exc:
        print(f"rejected: {exc}")
    print(f"map total = {account_map.total()}")

    print("\n== The same counter behind an Account ==")
    acct = Account("acct-7", USD, LockedCounter())
    acct.credit(Money.of_minor(10_000, USD))
    acct.credit(Money.of_minor(2_500, USD))
    acct.credit(Money.of_minor(2_500, USD))
    acct.debit(Money.of_minor(1_999, USD))
    print(f"{acct.id} balance after 100.00 + 25.00 + 25.00 - 19.99 = {acct.balance()}")
    try:
        acct.credit(Money.of_minor(100, EUR))
    except CurrencyMismatchError as exc:
        print(f"rejected: {exc}")

    print("\n== Deterministic replay with an injected stopwatch ==")
    replay = measure(LockedCounter(), 4, 1_000, 1, FakeStopwatch(1_000_000, 3_000_000))
    print(f"{replay.threads} threads x {replay.ops_per_thread:,} ops in {replay.elapsed_nanos / 1e6:.3f} ms = "
          f"{replay.ops_per_second():,.0f} ops/s, lost {replay.lost()}")


if __name__ == "__main__":
    main()
