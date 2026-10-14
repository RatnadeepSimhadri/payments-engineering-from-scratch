"""Lesson: phases/02-algorithms/14-randomized-algorithms/docs/en.md
Randomized Algorithms and Reservoir Sampling
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: Vitter, "Random sampling with a reservoir" (ACM TOMS 1985), Algorithm R; Knuth, TAOCP vol. 2, 3.4.2; Hoare,
"Algorithm 65: Find" (CACM 1961); Steele, Lea, Flood (OOPSLA 2014) for SplitMix64; Rabin (J. Number Theory 1980).
"""

from __future__ import annotations

import math
import sys
from dataclasses import dataclass
from typing import Callable, Generic, TypeVar

T = TypeVar("T")

MASK64 = 0xFFFFFFFFFFFFFFFF
INT_MAX = 2**31 - 1
DOUBLE_MIN_NORMAL = sys.float_info.min  # Double.MIN_NORMAL, 2^-1022


def to_int64(x: int) -> int:
    """Reinterpret the low 64 bits as a signed Java long."""
    x &= MASK64
    return x - (1 << 64) if x >= (1 << 63) else x


def trunc_div(a: int, b: int) -> int:
    """Java's `/` on integers truncates toward zero; Python's `//` floors."""
    q = abs(a) // abs(b)
    return q if (a < 0) == (b < 0) else -q


def java_rem(a: int, b: int) -> int:
    """Java's `%`: the remainder takes the sign of the dividend (Python's takes the sign of the divisor)."""
    return a - trunc_div(a, b) * b


@dataclass(frozen=True)
class Currency:
    """ISO 4217 currency, copied from Phase 10, Lesson 01."""

    code: str
    exponent: int

    def __post_init__(self) -> None:
        if self.code is None or len(self.code) != 3:
            raise ValueError(f"currency code must be three letters: {self.code}")
        if not 0 <= self.exponent <= 4:
            raise ValueError(f"exponent must be 0..4: {self.exponent}")


USD = Currency("USD", 2)


@dataclass(frozen=True)
class Money:
    """Exact amount in minor units. Subset of Phase 10, Lesson 01."""

    minor_units: int
    currency: Currency

    def __post_init__(self) -> None:
        if self.currency is None:
            raise TypeError("currency")

    @staticmethod
    def of_minor(minor_units: int, currency: Currency) -> Money:
        return Money(minor_units, currency)

    def __str__(self) -> str:
        scale = 10 ** self.currency.exponent
        whole = trunc_div(self.minor_units, scale)  # Java truncating / and % keep the output identical
        frac = abs(java_rem(self.minor_units, scale))
        return f"{whole}.{frac:0{self.currency.exponent}d} {self.currency.code}"


class Rng:
    """
    SplitMix64: a small, fast generator with a 64-bit state, written out so the sequence is identical in every
    language the course ships. State is kept as an unsigned 64-bit value; every add and multiply is masked because
    Python integers do not wrap. Not for cryptography: use `secrets` for keys and tokens.
    """

    def __init__(self, seed: int) -> None:
        self._state = seed & MASK64

    def _next_u64(self) -> int:
        self._state = (self._state + 0x9E3779B97F4A7C15) & MASK64
        z = self._state
        z = ((z ^ (z >> 30)) * 0xBF58476D1CE4E5B9) & MASK64  # >> on a masked value is Java's >>>
        z = ((z ^ (z >> 27)) * 0x94D049BB133111EB) & MASK64
        return z ^ (z >> 31)

    def next_long(self) -> int:
        """The next value as a signed 64-bit integer, exactly what Java's nextLong() returns."""
        return to_int64(self._next_u64())

    def next_int(self, bound: int) -> int:
        """Uniform integer in [0, bound) by rejection sampling; `% bound` would bias small residues."""
        if bound <= 0:
            raise ValueError(f"bound must be positive: {bound}")
        mask = (1 << bound.bit_length()) - 1  # Long.highestOneBit(bound) * 2 - 1
        while True:
            candidate = self._next_u64() & mask
            if candidate < bound:
                return candidate

    def next_double(self) -> float:
        """Uniform double in [0, 1) with 53 bits of randomness."""
        return (self._next_u64() >> 11) * (1.0 / (1 << 53))


class ReservoirSampler(Generic[T]):
    """
    Vitter's Algorithm R: a uniform sample of k items from a stream of unknown length in O(k) memory and one pass.
    Invariant after n items: every item seen so far is in the reservoir with probability exactly min(1, k / n).
    """

    def __init__(self, k: int, rng: Rng) -> None:
        if k <= 0:
            raise ValueError(f"k must be positive: {k}")
        if rng is None:
            raise TypeError("rng")
        self._k = k
        self._rng = rng
        self._reservoir: list[T] = []
        self._seen = 0

    def offer(self, item: T) -> None:
        self._seen += 1
        if len(self._reservoir) < self._k:
            self._reservoir.append(item)
            return
        # item number `seen` is kept with probability k / seen: pick a slot in [0, seen); keep if it is < k
        slot = self._next_long(self._seen)
        if slot < self._k:
            self._reservoir[slot] = item

    def _next_long(self, bound: int) -> int:
        if bound <= INT_MAX:
            return self._rng.next_int(bound)
        mask = (1 << bound.bit_length()) - 1
        while True:
            c = self._rng.next_long() & mask
            if 0 <= c < bound:
                return c

    def sample(self) -> tuple[T, ...]:
        return tuple(self._reservoir)

    def seen(self) -> int:
        return self._seen

    def capacity(self) -> int:
        return self._k


class JavaPriorityQueue(Generic[T]):
    """
    The binary heap of java.util.PriorityQueue (siftUp on add, siftDown on poll), so that iterating the backing
    array gives the same order as Java. heapq.heappop sifts the hole to a leaf first and then back up, which can
    leave equal keys in a different array order; sample() iterates the array, so the order is part of the output.
    """

    def __init__(self, cmp: Callable[[T, T], int]) -> None:
        self._cmp = cmp
        self._queue: list[T] = []

    def __len__(self) -> int:
        return len(self._queue)

    def __iter__(self):
        return iter(list(self._queue))

    def peek(self) -> T:
        return self._queue[0]

    def add(self, x: T) -> None:
        q, cmp = self._queue, self._cmp
        q.append(x)
        k = len(q) - 1
        while k > 0:
            parent = (k - 1) >> 1
            e = q[parent]
            if cmp(x, e) >= 0:
                break
            q[k] = e
            k = parent
        q[k] = x

    def poll(self) -> T:
        q, cmp = self._queue, self._cmp
        result = q[0]
        x = q.pop()
        n = len(q)
        if n > 0:
            k, half = 0, n >> 1
            while k < half:
                child = 2 * k + 1
                c = q[child]
                right = child + 1
                if right < n and cmp(c, q[right]) > 0:
                    child = right
                    c = q[child]
                if cmp(x, c) <= 0:
                    break
                q[k] = c
                k = child
            q[k] = x
        return result


@dataclass(frozen=True)
class _Entry(Generic[T]):
    key: float
    item: T


class WeightedReservoirSampler(Generic[T]):
    """
    Efraimidis-Spirakis A-Res: each item gets key u^(1/w) for u uniform in (0,1); keep the k largest keys.
    Selection probability is proportional to weight, so large payments are rarely missed by an audit sample.
    """

    def __init__(self, k: int, rng: Rng) -> None:
        if k <= 0:
            raise ValueError(f"k must be positive: {k}")
        if rng is None:
            raise TypeError("rng")
        self._k = k
        self._rng = rng
        self._heap: JavaPriorityQueue[_Entry[T]] = JavaPriorityQueue(lambda a, b: (a.key > b.key) - (a.key < b.key))

    def offer(self, item: T, weight: int) -> None:
        if weight <= 0:
            raise ValueError(f"weight must be positive: {weight}")
        u = self._rng.next_double()
        if u == 0.0:
            u = DOUBLE_MIN_NORMAL
        key = math.pow(u, 1.0 / weight)
        if len(self._heap) < self._k:
            self._heap.add(_Entry(key, item))
        elif key > self._heap.peek().key:
            self._heap.poll()
            self._heap.add(_Entry(key, item))

    def sample(self) -> list[T]:
        return [e.item for e in self._heap]


class Shuffle:
    """Shuffles: the correct one and the common wrong one, kept side by side so a test can tell them apart."""

    @staticmethod
    def fisher_yates(a: list, rng: Rng) -> None:
        """For i from n-1 down to 1, swap a[i] with a[j], j uniform in [0, i]. Every permutation equally likely."""
        for i in range(len(a) - 1, 0, -1):
            j = rng.next_int(i + 1)
            a[i], a[j] = a[j], a[i]

    @staticmethod
    def naive_shuffle(a: list, rng: Rng) -> None:
        """Swap every position with a random position in the WHOLE list: n^n outcomes cannot split evenly over n!."""
        for i in range(len(a)):
            j = rng.next_int(len(a))
            a[i], a[j] = a[j], a[i]

    @staticmethod
    def permutation_counts(trials: int, rng: Rng, fisher: bool) -> list[int]:
        """Shuffle [a, b, c] `trials` times and count each of the 6 permutations (lexicographic order)."""
        perms = ["abc", "acb", "bac", "bca", "cab", "cba"]
        counts = [0] * 6
        for _ in range(trials):
            a = ["a", "b", "c"]
            if fisher:
                Shuffle.fisher_yates(a, rng)
            else:
                Shuffle.naive_shuffle(a, rng)
            counts[perms.index("".join(a))] += 1
        return counts


class Quickselect:
    """
    Randomized quickselect: the k-th smallest element in expected O(n). Las Vegas: the answer is always right,
    only the running time is random. Reorders the list it is given; callers pass a copy if they care.
    """

    @staticmethod
    def select(a: list[int], k: int, rng: Rng) -> int:
        if not a:
            raise ValueError("empty array")
        if k < 0 or k >= len(a):
            raise ValueError(f"k out of range: {k}")
        lo, hi = 0, len(a) - 1
        while lo < hi:
            pivot_index = lo + rng.next_int(hi - lo + 1)
            p = Quickselect.partition(a, lo, hi, pivot_index)
            if k == p:
                return a[k]
            if k < p:
                hi = p - 1
            else:
                lo = p + 1
        return a[lo]

    @staticmethod
    def partition(a: list[int], lo: int, hi: int, pivot_index: int) -> int:
        """Lomuto partition around a[pivot_index]; returns the pivot's final position."""
        pivot = a[pivot_index]
        a[pivot_index], a[hi] = a[hi], a[pivot_index]
        store = lo
        for i in range(lo, hi):
            if a[i] < pivot:
                a[i], a[store] = a[store], a[i]
                store += 1
        a[store], a[hi] = a[hi], a[store]
        return store

    @staticmethod
    def median(a: list[int], rng: Rng) -> int:
        """Lower median: element at index (n-1)/2 of the sorted order."""
        return Quickselect.select(a, (len(a) - 1) // 2, rng)

    @staticmethod
    def percentile(a: list[int], q: float, rng: Rng) -> int:
        """Nearest-rank percentile: index ceil(q * n) - 1."""
        if q <= 0 or q > 1:
            raise ValueError(f"q must be in (0, 1]: {q}")
        idx = math.ceil(q * len(a)) - 1
        return Quickselect.select(a, max(0, idx), rng)


class MillerRabin:
    """
    Monte Carlo primality test. "Composite" is always right; "probably prime" is wrong with probability at most
    4^-rounds. The n < 2^31 guard is kept for parity with the Java, where it keeps a*b inside a long.
    """

    @staticmethod
    def is_probable_prime(n: int, rounds: int, rng: Rng) -> bool:
        if n >= (1 << 31):
            raise ValueError(f"n must be below 2^31: {n}")
        if n < 2:
            return False
        if n < 4:
            return True
        if n % 2 == 0:
            return False
        d, s = n - 1, 0
        while d % 2 == 0:  # n - 1 = 2^s * d with d odd
            d //= 2
            s += 1
        for _ in range(rounds):
            a = 2 + rng.next_int(n - 3)  # witness candidate in [2, n-2]
            if MillerRabin.is_witness(a, d, s, n):
                return False  # a proves n composite
        return True  # no witness found in `rounds` tries

    @staticmethod
    def is_witness(a: int, d: int, s: int, n: int) -> bool:
        """True if a is a witness to n's compositeness: a^d != 1 and a^(2^i d) != -1 for all i < s."""
        x = MillerRabin.pow_mod(a, d, n)
        if x == 1 or x == n - 1:
            return False
        for _ in range(1, s):
            x = (x * x) % n
            if x == n - 1:
                return False
        return True

    @staticmethod
    def pow_mod(base: int, exp: int, mod: int) -> int:
        """Square-and-multiply, written out; the built-in pow(base, exp, mod) computes the same thing."""
        result = 1
        base %= mod
        while exp > 0:
            if exp & 1:
                result = (result * base) % mod
            base = (base * base) % mod
            exp >>= 1
        return result

    @staticmethod
    def is_prime_by_trial_division(n: int) -> bool:
        """Deterministic reference for small n, used by the tests as the oracle."""
        if n < 2:
            return False
        f = 2
        while f * f <= n:
            if n % f == 0:
                return False
            f += 1
        return True


@dataclass(frozen=True)
class Transaction:
    id: str
    amount: Money


def day_of_transactions(rng: Rng, count: int) -> list[Transaction]:
    out: list[Transaction] = []
    for i in range(count):
        # most payments small, a few large: 90% in [1, 200], 10% in [200, 5000] dollars
        dollars = 200 + rng.next_int(4801) if rng.next_int(10) == 0 else 1 + rng.next_int(200)
        cents = rng.next_int(100)
        out.append(Transaction(f"txn-{i:05d}", Money.of_minor(dollars * 100 + cents, USD)))
    return out


def jbool(b: bool) -> str:
    return "true" if b else "false"


def main() -> None:
    rng = Rng(2026)
    day = day_of_transactions(rng, 10_000)

    print("== Reservoir sampling: 5 of 10,000 transactions for audit, one pass, O(k) memory ==")
    uniform: ReservoirSampler[Transaction] = ReservoirSampler(5, Rng(1))
    for t in day:
        uniform.offer(t)
    print(f"seen {uniform.seen()}, sample: [{', '.join(t.id for t in uniform.sample())}]")

    print()
    print("== Weighted reservoir: probability proportional to amount ==")
    weighted: WeightedReservoirSampler[Transaction] = WeightedReservoirSampler(5, Rng(1))
    for t in day:
        weighted.offer(t, t.amount.minor_units)
    for t in weighted.sample():
        print(f"  {t.id}  {t.amount}")

    print()
    print("== Fisher-Yates vs the naive shuffle: 60,000 shuffles of [a, b, c] ==")
    print(f"{'perm':<6} {'fisher':>10} {'naive':>10}")
    fy = Shuffle.permutation_counts(60_000, Rng(7), True)
    nv = Shuffle.permutation_counts(60_000, Rng(7), False)
    perms = ["abc", "acb", "bac", "bca", "cab", "cba"]
    for i in range(6):
        print(f"{perms[i]:<6} {fy[i]:10d} {nv[i]:10d}")
    print("expected 10,000 each; naive gives acb, bac, bca 5/27 each (11,111) and the others 4/27 (8,889): "
          "27 outcomes cannot split evenly over 6")

    print()
    print("== Quickselect: median and p99 amount without sorting 10,000 values ==")
    amounts = [t.amount.minor_units for t in day]
    median = Quickselect.median(list(amounts), Rng(3))
    p99 = Quickselect.percentile(list(amounts), 0.99, Rng(3))
    sorted_ = sorted(amounts)
    print(f"median {Money.of_minor(median, USD)} (sorted says {Money.of_minor(sorted_[(len(sorted_) - 1) // 2], USD)})")
    print(f"p99    {Money.of_minor(p99, USD)} (sorted says "
          f"{Money.of_minor(sorted_[math.ceil(0.99 * len(sorted_)) - 1], USD)})")

    print()
    print("== Miller-Rabin: Monte Carlo primality, 561 is a Carmichael number that fools Fermat ==")
    for n in (561, 1_000_003, 2_147_483_647, 1_000_001, 7919 * 7927):
        print(f"{n:12d}  probable prime after 10 rounds: {jbool(MillerRabin.is_probable_prime(n, 10, Rng(5))):<5}  "
              f"trial division: {jbool(MillerRabin.is_prime_by_trial_division(n))}")


if __name__ == "__main__":
    main()
