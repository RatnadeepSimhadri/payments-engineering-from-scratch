"""Lesson: phases/03-systems-data-structures/02-count-min-sketch-and-hyperloglog/docs/en.md
Count-Min Sketch and HyperLogLog: frequency and cardinality in constant memory
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: Cormode & Muthukrishnan (2005), Count-Min Sketch; Flajolet et al. (2007), HyperLogLog; Heule et al. (2013), HLL in Practice.
"""

from __future__ import annotations

import math
from decimal import ROUND_HALF_UP, Decimal

MASK64 = 0xFFFFFFFFFFFFFFFF
LONG_MAX = (1 << 63) - 1


def to_int64(x: int) -> int:
    """Reinterpret the low 64 bits as a signed Java long."""
    x &= MASK64
    return x - (1 << 64) if x >= (1 << 63) else x


def java_abs_long(x: int) -> int:
    """Math.abs(long): Long.MIN_VALUE stays negative because its negation overflows."""
    return to_int64(-x) if x < 0 else x


def java_rem(a: int, b: int) -> int:
    """Java's % truncates toward zero, so the remainder takes the sign of the dividend."""
    r = abs(a) % abs(b)
    return -r if a < 0 else r


def fmt_fixed(x: float, digits: int) -> str:
    """Java's %.Nf: round the shortest decimal repr HALF_UP (Python's f-format rounds the binary value)."""
    q = Decimal(repr(x)).quantize(Decimal(1).scaleb(-digits), rounding=ROUND_HALF_UP)
    return f"{q:.{digits}f}"


class JavaRandom:
    """java.util.Random, bit for bit: 48-bit LCG, so the same seed gives the same stream as Java."""

    _MULT = 0x5DEECE66D
    _MASK = (1 << 48) - 1

    def __init__(self, seed: int) -> None:
        self._seed = (seed ^ self._MULT) & self._MASK
        self._next_gaussian: float | None = None

    def _next(self, bits: int) -> int:
        self._seed = (self._seed * self._MULT + 0xB) & self._MASK
        r = self._seed >> (48 - bits)
        return r - (1 << 32) if r >= (1 << 31) else r  # (int) cast

    def next_int(self, bound: int) -> int:
        if bound <= 0:
            raise ValueError("bound must be positive")
        r = self._next(31)
        m = bound - 1
        if bound & m == 0:
            return (bound * r) >> 31
        u = r
        while True:
            r = u % bound
            if (u - r + m) & 0xFFFFFFFF < 0x80000000:  # Java: u - r + m >= 0 in 32-bit int
                return r
            u = self._next(31)

    def next_long(self) -> int:
        return to_int64((self._next(32) << 32) + self._next(32))

    def next_double(self) -> float:
        return ((self._next(26) << 27) + self._next(27)) * (1.0 / (1 << 53))

    def next_gaussian(self) -> float:
        if self._next_gaussian is not None:
            g, self._next_gaussian = self._next_gaussian, None
            return g
        while True:
            v1 = 2 * self.next_double() - 1
            v2 = 2 * self.next_double() - 1
            s = v1 * v1 + v2 * v2
            if 0 < s < 1:
                break
        multiplier = math.sqrt(-2 * math.log(s) / s)
        self._next_gaussian = v2 * multiplier
        return v1 * multiplier


class Hashing:
    """The two 64-bit hash functions both sketches are built on. Unsigned 64-bit results."""

    FNV_OFFSET = 0xCBF29CE484222325
    FNV_PRIME = 0x100000001B3

    @staticmethod
    def fnv1a64(key: str) -> int:
        """FNV-1a over the UTF-8 bytes; masked after every multiply so it wraps like a Java long."""
        h = Hashing.FNV_OFFSET
        for b in key.encode("utf-8"):
            h = ((h ^ b) * Hashing.FNV_PRIME) & MASK64
        return h

    @staticmethod
    def mix64(h: int) -> int:
        """MurmurHash3's fmix64 finalizer: a bijection that spreads every input bit to every output bit."""
        h &= MASK64
        h ^= h >> 33
        h = (h * 0xFF51AFD7ED558CCD) & MASK64
        h ^= h >> 33
        h = (h * 0xC4CEB9FE1A85EC53) & MASK64
        h ^= h >> 33
        return h

    @staticmethod
    def hash64(key: str) -> int:
        return Hashing.mix64(Hashing.fnv1a64(key))


class CountMinSketch:
    """depth rows of width counters; estimate is the min over the key's counters. Never underestimates."""

    def __init__(self, width: int, depth: int) -> None:
        if width <= 0:
            raise ValueError(f"width must be positive: {width}")
        if depth <= 0:
            raise ValueError(f"depth must be positive: {depth}")
        self.width = width
        self.depth = depth
        self._table = [[0] * width for _ in range(depth)]
        self._total = 0

    @staticmethod
    def for_error_bounds(epsilon: float, delta: float) -> "CountMinSketch":
        if not 0 < epsilon < 1:
            raise ValueError(f"epsilon must be in (0,1): {epsilon}")
        if not 0 < delta < 1:
            raise ValueError(f"delta must be in (0,1): {delta}")
        return CountMinSketch(math.ceil(math.e / epsilon), math.ceil(math.log(1.0 / delta)))

    def column(self, h1: int, h2: int, row: int) -> int:
        """Double hashing h1 + i*h2, wrapped to 64 bits, unsigned remainder (Long.remainderUnsigned)."""
        return ((h1 + row * h2) & MASK64) % self.width

    def add(self, key: str, count: int = 1) -> None:
        if count < 0:
            raise ValueError(f"count must be non-negative: {count}")
        h1 = Hashing.hash64(key)
        h2 = Hashing.mix64(h1)
        for row in range(self.depth):
            self._table[row][self.column(h1, h2, row)] += count
        self._total += count

    def estimate(self, key: str) -> int:
        h1 = Hashing.hash64(key)
        h2 = Hashing.mix64(h1)
        return min(self._table[row][self.column(h1, h2, row)] for row in range(self.depth))

    def total(self) -> int:
        return self._total

    def error_bound(self) -> float:
        return math.e / self.width * self._total

    def failure_probability(self) -> float:
        return math.exp(-self.depth)

    def merge(self, other: "CountMinSketch") -> "CountMinSketch":
        if other.width != self.width or other.depth != self.depth:
            raise ValueError("cannot merge sketches of different shape")
        out = CountMinSketch(self.width, self.depth)
        for r in range(self.depth):
            out._table[r] = [a + b for a, b in zip(self._table[r], other._table[r])]
        out._total = self._total + other._total
        return out

    def bytes(self) -> int:
        return self.width * self.depth * 8


class HyperLogLog:
    """m = 2^precision registers of max leading-zero runs; bias-corrected harmonic mean."""

    def __init__(self, precision: int) -> None:
        if precision < 4 or precision > 16:
            raise ValueError(f"precision must be 4..16: {precision}")
        self.precision = precision
        self.registers = 1 << precision
        self._register = bytearray(self.registers)

    @staticmethod
    def rho(hash_: int, precision: int) -> int:
        """Position of the first 1-bit after the index bits, from 1; all-zero gives 65 - p."""
        rest = (hash_ << precision) & MASK64
        if rest == 0:
            return 64 - precision + 1
        return 64 - rest.bit_length() + 1  # numberOfLeadingZeros + 1

    def add(self, key: str) -> None:
        h = Hashing.hash64(key)
        index = h >> (64 - self.precision)
        r = HyperLogLog.rho(h, self.precision)
        if r > self._register[index]:
            self._register[index] = r

    @staticmethod
    def alpha(m: int) -> float:
        return {16: 0.673, 32: 0.697, 64: 0.709}.get(m, 0.7213 / (1.0 + 1.079 / m))

    def raw_estimate(self) -> float:
        s = 0.0
        for r in self._register:
            s += 2.0 ** -r
        return HyperLogLog.alpha(self.registers) * self.registers * float(self.registers) / s

    def zero_registers(self) -> int:
        return self._register.count(0)

    def cardinality(self) -> int:
        estimate = self.raw_estimate()
        if estimate <= 2.5 * self.registers:
            zeros = self.zero_registers()
            if zeros > 0:
                estimate = self.registers * math.log(self.registers / zeros)
        return math.floor(estimate + 0.5)  # Math.round, not banker's rounding

    def standard_error(self) -> float:
        return 1.04 / math.sqrt(self.registers)

    def merge(self, other: "HyperLogLog") -> "HyperLogLog":
        if other.precision != self.precision:
            raise ValueError("cannot merge different precisions")
        out = HyperLogLog(self.precision)
        out._register = bytearray(max(a, b) for a, b in zip(self._register, other._register))
        return out

    def register_at(self, i: int) -> int:
        return self._register[i]

    def bytes(self) -> int:
        return self.registers


def card_token(rng: JavaRandom) -> str:
    v = java_abs_long(rng.next_long())
    return "tok_" + f"{java_rem(v, 10_000_000_000_000_000):016d}"


def main() -> None:
    rng = JavaRandom(42)

    print("== Count-Min Sketch: authorizations per merchant, 11 KB instead of a hash map ==")
    cms = CountMinSketch.for_error_bounds(0.01, 0.01)
    print(f"width={cms.width} depth={cms.depth} bytes={cms.bytes()} (epsilon=0.01, delta=0.01)")

    exact: dict[str, int] = {}
    merchants = [f"merchant_{i}" for i in range(1_000)]
    for _ in range(200_000):
        m = int(min(len(merchants) - 1, math.floor(abs(rng.next_gaussian()) * 120)))
        cms.add(merchants[m])
        exact[merchants[m]] = exact.get(merchants[m], 0) + 1
    print(f"total events = {cms.total()}, error bound = {fmt_fixed(cms.error_bound(), 0)}")
    for m in ("merchant_0", "merchant_5", "merchant_50", "merchant_300", "merchant_999"):
        truth = exact.get(m, 0)
        est = cms.estimate(m)
        print(f"{m:<14} true={truth:6d}  estimate={est:6d}  over by {est - truth}")

    print()
    print("== HyperLogLog: distinct cards per merchant, 4 KB instead of a set ==")
    hll = HyperLogLog(12)
    distinct: set[str] = set()
    cards = [card_token(rng) for _ in range(50_000)]
    for _ in range(300_000):
        card = cards[rng.next_int(len(cards))]
        hll.add(card)
        distinct.add(card)
    print(f"precision={hll.precision} registers={hll.registers} bytes={hll.bytes()} "
          f"standard error={fmt_fixed(hll.standard_error() * 100, 2)}%")
    card = hll.cardinality()
    print(f"300000 events, true distinct = {len(distinct)}, HLL estimate = {card} "
          f"({fmt_fixed(100.0 * (card - len(distinct)) / len(distinct), 2)}% off)")

    print()
    print("== Small sets: linear counting takes over ==")
    small = HyperLogLog(12)
    for i in range(1, 101):
        small.add(f"card_{i}")
    print(f"100 distinct cards -> raw {fmt_fixed(small.raw_estimate(), 1)}, corrected {small.cardinality()}")

    print()
    print("== Merging two merchants' sketches is the union, not the sum ==")
    a, b = HyperLogLog(12), HyperLogLog(12)
    for i in range(20_000):
        a.add(cards[i])
    for i in range(10_000, 30_000):
        b.add(cards[i])
    print(f"a={a.cardinality()} b={b.cardinality()} merged={a.merge(b).cardinality()} "
          f"(true union 30000, sum would say {a.cardinality() + b.cardinality()})")

    print()
    print("== Memory: exact vs sketch for 1,000 merchants x 50,000 cards ==")
    exact_bytes = 1_000 * 50_000 * 40
    print(f"exact sets  ~ {exact_bytes:,} bytes")
    print(f"1000 HLLs    = {1_000 * hll.bytes():,} bytes")
    print(str([HyperLogLog.rho(0x0008000000000000, 12), HyperLogLog.rho(0x0000100000000000, 12), HyperLogLog.rho(0, 12)])
          + " <- rho when the first rest-bit is 1, when it is the 8th, and when all 52 are zero")


if __name__ == "__main__":
    main()
