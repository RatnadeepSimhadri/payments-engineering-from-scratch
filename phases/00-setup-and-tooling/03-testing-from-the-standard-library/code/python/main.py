"""Lesson: phases/00-setup-and-tooling/03-testing-from-the-standard-library/docs/en.md
Testing from the Standard Library: unittest, FakeClock, SeededRandom, and a jittered backoff to test with them
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: Steele, Lea, Flood, "Fast Splittable Pseudorandom Number Generators" (OOPSLA 2014) for SplitMix64;
         Brooker, "Exponential Backoff And Jitter" (AWS Architecture Blog, 2015) for full-jitter backoff.
Python 3.11+, standard library only. Same design as the Java and Go versions.
"""

from __future__ import annotations

import threading
import time
from dataclasses import dataclass
from typing import Protocol, TypeVar

T = TypeVar("T")

_MASK64 = (1 << 64) - 1
_GOLDEN_GAMMA = 0x9E3779B97F4A7C15


class Clock(Protocol):
    """A source of time in epoch milliseconds. Production code takes a Clock; tests hand it a FakeClock."""

    def now_millis(self) -> int: ...


class SystemClock:
    """The real clock. Never appears in a test."""

    def now_millis(self) -> int:
        return time.time_ns() // 1_000_000


class FakeClock:
    """A clock that moves only when told to. Thread-safe so code under test may read it from worker threads.
    Invariant: time never goes backwards; advance() rejects negatives and advance_to() rejects the past."""

    def __init__(self, start_millis: int) -> None:
        self._now = start_millis
        self._lock = threading.Lock()

    def now_millis(self) -> int:
        with self._lock:
            return self._now

    def advance(self, millis: int) -> None:
        if millis < 0:
            raise ValueError(f"cannot advance by a negative duration: {millis}")
        with self._lock:
            self._now += millis

    def advance_to(self, millis: int) -> None:
        with self._lock:
            if millis < self._now:
                raise ValueError(f"cannot move the clock backwards from {self._now} to {millis}")
            self._now = millis


def _to_signed64(u: int) -> int:
    return u - (1 << 64) if u >= 1 << 63 else u


class SeededRandom:
    """SplitMix64: a 64-bit generator with a 64-bit state. The same sequence for the same seed in Java,
    Python, and Go, which is why the course uses it instead of each language's own PRNG. Python ints are
    unbounded, so every multiply and add is masked back to 64 bits by hand.
    Not for cryptography or anything money-bearing; for simulations, shuffles, and jitter."""

    def __init__(self, seed: int) -> None:
        self._seed = seed
        self._state = seed & _MASK64

    @property
    def seed(self) -> int:
        return self._seed

    def _next_u64(self) -> int:
        self._state = (self._state + _GOLDEN_GAMMA) & _MASK64
        z = self._state
        z = ((z ^ (z >> 30)) * 0xBF58476D1CE4E5B9) & _MASK64
        z = ((z ^ (z >> 27)) * 0x94D049BB133111EB) & _MASK64
        return z ^ (z >> 31)

    def next_long(self) -> int:
        """The next 64 bits, as a signed value so it matches Java's long and Go's int64 exactly."""
        return _to_signed64(self._next_u64())

    def next_int(self, bound: int) -> int:
        """Uniform in [0, bound). Uses the top 31 bits; the modulo bias is below 2^-31 for any realistic bound."""
        if bound <= 0:
            raise ValueError(f"bound must be positive: {bound}")
        return (self._next_u64() >> 33) % bound

    def next_double(self) -> float:
        """Uniform in [0.0, 1.0): the top 53 bits scaled by 2^-53, so every float is exactly representable."""
        return (self._next_u64() >> 11) * 2.0 ** -53

    def shuffle(self, items: list[T]) -> None:
        """Fisher-Yates, in place: for i from n-1 down to 1, swap a[i] with a[next_int(i+1)]."""
        for i in range(len(items) - 1, 0, -1):
            j = self.next_int(i + 1)
            items[i], items[j] = items[j], items[i]


@dataclass(frozen=True)
class Deadline:
    """A point in time to compare the clock against. Shows why a fake clock exists: no sleep in the test."""

    clock: Clock
    at_millis: int

    def expired(self) -> bool:
        return self.clock.now_millis() >= self.at_millis

    def remaining_millis(self) -> int:
        return max(0, self.at_millis - self.clock.now_millis())


def backoff_millis(attempt: int, base_millis: int, cap_millis: int, rng: SeededRandom) -> int:
    """Full-jitter exponential backoff: a uniform delay in [0, min(cap, base * 2^attempt)).
    Deterministic given the rng, which is what makes the retry scheduler in Phase 2 testable."""
    if attempt < 0 or base_millis <= 0 or cap_millis < base_millis:
        raise ValueError("attempt >= 0, base > 0, cap >= base required")
    shift = min(attempt, 30)  # 2^30 * base already exceeds any sane cap
    window = min(cap_millis, base_millis << shift)
    return rng.next_int(window)


def main() -> None:
    print("== SeededRandom(42): the same five numbers in Java, Python, and Go ==")
    rng = SeededRandom(42)
    for _ in range(5):
        print(rng.next_long())

    print("\n== next_int(100) x 8 and a shuffle of 0..9 with seed 7 ==")
    rng = SeededRandom(42)
    print(" ".join(str(rng.next_int(100)) for _ in range(8)))
    deck = list(range(10))
    SeededRandom(7).shuffle(deck)
    print(deck)

    print("\n== FakeClock and a deadline: no time.sleep anywhere ==")
    clock = FakeClock(1_700_000_000_000)  # 2023-11-14T22:13:20Z
    deadline = Deadline(clock, clock.now_millis() + 30_000)
    print(f"t+0s     expired={str(deadline.expired()).lower()} remaining={deadline.remaining_millis()}ms")
    clock.advance(29_999)
    print(f"t+29.999 expired={str(deadline.expired()).lower()} remaining={deadline.remaining_millis()}ms")
    clock.advance(1)
    print(f"t+30s    expired={str(deadline.expired()).lower()} remaining={deadline.remaining_millis()}ms")

    print("\n== Full-jitter backoff for a PSP call, base 100ms cap 2000ms, seed 42 ==")
    rng = SeededRandom(42)
    for attempt in range(5):
        window = min(2000, 100 << attempt)
        print(f"attempt {attempt}: window [0, {window:4d}) -> wait {backoff_millis(attempt, 100, 2000, rng):4d} ms")
    try:
        clock.advance(-1)
    except ValueError as exc:
        print(f"\nrejected: {exc}")


if __name__ == "__main__":
    main()
