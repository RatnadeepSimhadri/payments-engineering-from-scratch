"""Lesson: phases/03-systems-data-structures/13-unique-id-generation/docs/en.md
Unique ID Generation: UUIDs, ULIDs, and Snowflake
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: RFC 9562 (UUIDs, versions 4 and 7, monotonic counters); ULID spec (Crockford base32);
Twitter Snowflake announcement (2010) for the 41/10/12 layout. Python 3.11+, standard library only.
"""

from __future__ import annotations

import math
import string
from dataclasses import dataclass
from decimal import ROUND_HALF_UP, Decimal, localcontext
from typing import Optional, Protocol

MASK64 = 0xFFFFFFFFFFFFFFFF


def to_int64(x: int) -> int:
    """Reinterpret the low 64 bits as a signed Java long."""
    x &= MASK64
    return x - (1 << 64) if x >= (1 << 63) else x


def fmt_fixed(x: float, digits: int) -> str:
    """Java's %.Nf: round the shortest decimal repr HALF_UP."""
    q = Decimal(repr(x)).quantize(Decimal(1).scaleb(-digits), rounding=ROUND_HALF_UP)
    return f"{q:.{digits}f}"


def fmt_sci(x: float, digits: int) -> str:
    """Java's %.Ne: HALF_UP on the shortest decimal repr, and an exponent of at least two digits (e-07)."""
    with localcontext() as ctx:
        ctx.rounding = ROUND_HALF_UP
        s = format(Decimal(repr(x)), f".{digits}e")
    mantissa, exp = s.split("e")
    sign = "-" if exp.startswith("-") else "+"
    return f"{mantissa}e{sign}{abs(int(exp)):02d}"


class JavaRandom:
    """java.util.Random, bit for bit: 48-bit LCG, so the same seed gives the same stream as Java."""

    _MULT = 0x5DEECE66D
    _MASK = (1 << 48) - 1

    def __init__(self, seed: int) -> None:
        self._seed = (seed ^ self._MULT) & self._MASK

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
        """Signed, like Java's long."""
        return to_int64((self._next(32) << 32) + self._next(32))


class IllegalStateError(Exception):
    """Java's IllegalStateException."""


class ClockMovedBackwardsException(RuntimeError):
    """Same name as the Java class: the Snowflake generator refuses to issue ids that could collide."""

    def __init__(self, by_millis: int) -> None:
        super().__init__(f"clock moved backwards by {by_millis} ms; refusing to issue ids that could collide")
        self.by_millis = by_millis


class Clock(Protocol):
    """Injected time source in milliseconds since the Unix epoch."""

    def now_millis(self) -> int: ...


class ManualClock:
    def __init__(self, start_millis: int) -> None:
        self._now = start_millis

    def now_millis(self) -> int:
        return self._now

    def advance(self, millis: int) -> None:
        self._now += millis

    def set(self, millis: int) -> None:
        """Clocks do go backwards in real systems (NTP step, VM migration). Tests need to simulate it."""
        self._now = millis


@dataclass(frozen=True, order=True)
class Uuid:
    """128 bits as two unsigned 64-bit halves, with the RFC 9562 text form. Ordering is unsigned, field by field."""

    msb: int
    lsb: int

    def __post_init__(self) -> None:
        # Normalise Java-style signed longs to unsigned 64-bit, so -1 and 0xFFFF...FF are the same half.
        object.__setattr__(self, "msb", self.msb & MASK64)
        object.__setattr__(self, "lsb", self.lsb & MASK64)

    def version(self) -> int:
        return (self.msb >> 12) & 0xF

    def variant(self) -> int:
        """2 for the RFC 9562 variant (bit pattern 10xx in the top of the low half)."""
        return (self.lsb >> 62) & 0x3

    def timestamp_millis(self) -> int:
        """For version 7: the 48-bit Unix millisecond timestamp in the top of the high half."""
        return self.msb >> 16

    def compare_to(self, o: "Uuid") -> int:
        return (self > o) - (self < o)

    def __str__(self) -> str:
        h = f"{self.msb:016x}{self.lsb:016x}"
        return f"{h[0:8]}-{h[8:12]}-{h[12:16]}-{h[16:20]}-{h[20:32]}"

    @staticmethod
    def parse(text: str) -> "Uuid":
        if len(text) != 36 or text[8] != "-" or text[13] != "-" or text[18] != "-" or text[23] != "-":
            raise ValueError(f"not a UUID: {text}")
        hex_digits = text.replace("-", "")
        # int(x, 16) also accepts '_', '+' and whitespace; Long.parseUnsignedLong does not.
        if len(hex_digits) != 32 or any(c not in string.hexdigits for c in hex_digits):
            raise ValueError(f"not a UUID: {text}")
        return Uuid(int(hex_digits[:16], 16), int(hex_digits[16:], 16))


class UuidV4Generator:
    """RFC 9562 version 4: 122 random bits. No order, no time, no coordination."""

    def __init__(self, rng: JavaRandom) -> None:
        self._rng = rng

    def next(self) -> Uuid:
        msb, lsb = self._rng.next_long() & MASK64, self._rng.next_long() & MASK64
        msb = (msb & ~0xF000 & MASK64) | 0x4000                              # version nibble = 4
        lsb = (lsb & 0x3FFFFFFFFFFFFFFF) | 0x8000000000000000                # variant bits = 10
        return Uuid(msb, lsb)


class UuidV7Generator:
    """RFC 9562 version 7: 48-bit ms, 12-bit counter (method 1), 62 random bits. Monotonic per generator."""

    def __init__(self, clock: Clock, rng: JavaRandom) -> None:
        self._clock = clock
        self._rng = rng
        self._last_millis = -1
        self._counter = 0

    def next(self) -> Uuid:
        now = self._clock.now_millis()
        if now <= self._last_millis:  # same ms, or clock went backwards
            self._counter += 1
            if self._counter > 0xFFF:  # borrow the next ms
                self._last_millis += 1
                self._counter = self._rng.next_int(1 << 11)
        else:
            self._last_millis = now
            self._counter = self._rng.next_int(1 << 11)  # leave the top counter bit clear: room to count
        msb = (self._last_millis << 16) | 0x7000 | self._counter
        lsb = (self._rng.next_long() & 0x3FFFFFFFFFFFFFFF) | 0x8000000000000000
        return Uuid(msb, lsb)


ALPHABET = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"  # Crockford base32: no I, L, O, U
_DECODE: dict[str, int] = {}
for _i, _c in enumerate(ALPHABET):
    _DECODE[_c] = _i
    _DECODE[_c.lower()] = _i
# Crockford's alphabet excludes I, L, O, U to avoid misreads; decoding accepts the first three leniently.
_DECODE.update({"I": 1, "i": 1, "L": 1, "l": 1, "O": 0, "o": 0})


@dataclass(frozen=True, order=True)
class Ulid:
    """48-bit ms timestamp + 80 random bits (16 high + 64 low), Crockford base32, 26 characters, sortable."""

    timestamp_millis: int
    random_hi: int
    random_lo: int

    ALPHABET = ALPHABET

    def __post_init__(self) -> None:
        if not 0 <= self.timestamp_millis < (1 << 48):
            raise ValueError("timestamp must fit in 48 bits")
        if not 0 <= self.random_hi < (1 << 16):
            raise ValueError("randomHi must fit in 16 bits")
        object.__setattr__(self, "random_lo", self.random_lo & MASK64)  # Java's -1L is all ones

    def compare_to(self, o: "Ulid") -> int:
        return (self > o) - (self < o)

    def __str__(self) -> str:
        """10 chars of timestamp (50 bits, top two zero) + 16 chars of randomness (80 bits)."""
        out = [""] * 26
        t = self.timestamp_millis
        for i in range(9, -1, -1):
            out[i] = ALPHABET[t & 0x1F]
            t >>= 5
        lo, hi = self.random_lo, self.random_hi
        for i in range(25, 9, -1):  # emit from the low end, 5 bits at a time
            out[i] = ALPHABET[lo & 0x1F]
            lo = (lo >> 5) | ((hi & 0x1F) << 59)
            hi >>= 5
        return "".join(out)

    @staticmethod
    def parse(text: str) -> "Ulid":
        if len(text) != 26:
            raise ValueError(f"ULID must be 26 characters: {text}")
        t = 0
        for ch in text[:10]:
            t = (t << 5) | Ulid._digit(ch)
        if t >= (1 << 48):
            raise ValueError(f"ULID timestamp overflows 48 bits: {text}")
        hi = lo = 0
        for ch in text[10:]:
            d = Ulid._digit(ch)
            hi = ((hi << 5) | (lo >> 59)) & MASK64
            lo = ((lo << 5) | d) & MASK64
        return Ulid(t, hi & 0xFFFF, lo)

    @staticmethod
    def _digit(c: str) -> int:
        d = _DECODE.get(c, -1)
        if d < 0:
            raise ValueError(f"invalid Crockford base32 character: {c}")
        return d


class UlidGenerator:
    """Monotonic ULID generator: same millisecond increments the 80-bit random part by one."""

    def __init__(self, clock: Clock, rng: JavaRandom) -> None:
        self._clock = clock
        self._rng = rng
        self._last: Optional[Ulid] = None

    def resume(self, last_issued: Ulid) -> None:
        """Continue after a restart from the last id this process issued."""
        self._last = last_issued

    def next(self) -> Ulid:
        now = self._clock.now_millis()
        last = self._last
        if last is not None and now <= last.timestamp_millis:
            lo = (last.random_lo + 1) & MASK64          # wraps like a Java long
            hi = last.random_hi + (1 if lo == 0 else 0)  # carry into the high 16 bits
            if hi > 0xFFFF:
                raise IllegalStateError("ULID randomness overflowed within one millisecond")
            self._last = Ulid(last.timestamp_millis, hi, lo)
        else:
            self._last = Ulid(now, self._rng.next_int(1 << 16), self._rng.next_long())
        return self._last


class SnowflakeGenerator:
    """63 bits: 41-bit ms since a custom epoch | 10-bit worker | 12-bit sequence."""

    EPOCH_MILLIS = 1_704_067_200_000  # 2024-01-01T00:00:00Z
    WORKER_BITS, SEQUENCE_BITS = 10, 12
    MAX_WORKER = (1 << WORKER_BITS) - 1
    MAX_SEQUENCE = (1 << SEQUENCE_BITS) - 1
    MAX_TIMESTAMP = (1 << 41) - 1

    def __init__(self, clock: Clock, worker_id: int, max_backwards_millis: int) -> None:
        if not 0 <= worker_id <= self.MAX_WORKER:
            raise ValueError(f"worker id must be 0..{self.MAX_WORKER}")
        self._clock = clock
        self._worker_id = worker_id
        self._max_backwards_millis = max_backwards_millis
        self._last_millis = -1
        self._sequence = 0
        self.borrowed_millis = 0  # how many ms logical time ran ahead of the clock

    def next(self) -> int:
        now = self._clock.now_millis() - self.EPOCH_MILLIS
        if now < self._last_millis:
            if self._last_millis - now > self._max_backwards_millis:
                raise ClockMovedBackwardsException(self._last_millis - now)
            now = self._last_millis  # small drift: keep issuing on the last timestamp
        if now == self._last_millis:
            self._sequence = (self._sequence + 1) & self.MAX_SEQUENCE
            if self._sequence == 0:  # 4096 in one ms: borrow the next
                self._last_millis += 1
                self.borrowed_millis += 1
                now = self._last_millis
        else:
            self._last_millis = now
            self._sequence = 0
        if self._last_millis > self.MAX_TIMESTAMP:
            raise IllegalStateError("snowflake epoch exhausted")
        return ((self._last_millis << (self.WORKER_BITS + self.SEQUENCE_BITS))
                | (self._worker_id << self.SEQUENCE_BITS) | self._sequence)

    @staticmethod
    def timestamp_millis_of(id_: int) -> int:
        return (id_ >> (SnowflakeGenerator.WORKER_BITS + SnowflakeGenerator.SEQUENCE_BITS)) + SnowflakeGenerator.EPOCH_MILLIS

    @staticmethod
    def worker_id_of(id_: int) -> int:
        return (id_ >> SnowflakeGenerator.SEQUENCE_BITS) & SnowflakeGenerator.MAX_WORKER

    @staticmethod
    def sequence_of(id_: int) -> int:
        return id_ & SnowflakeGenerator.MAX_SEQUENCE


def collision_probability(n: float, bits: int) -> float:
    """Birthday bound: probability of at least one collision among n ids drawn from 2^bits values."""
    pairs = n * (n - 1) / 2.0
    return 1.0 - math.exp(-pairs / math.pow(2.0, bits))


def ids_for_collision_probability(p: float, bits: int) -> float:
    """n ~ sqrt(2 * 2^bits * ln(1/(1-p)))."""
    return math.sqrt(2.0 * math.pow(2.0, bits) * math.log(1.0 / (1.0 - p)))


def is_strictly_increasing(items: list) -> bool:
    return all(items[i - 1] < items[i] for i in range(1, len(items)))


def is_lexically_increasing(items: list[str]) -> bool:
    return all(items[i - 1] < items[i] for i in range(1, len(items)))


def java_bool(b: bool) -> str:
    return "true" if b else "false"


def main() -> None:
    rng = JavaRandom(7)
    clock = ManualClock(1_760_000_000_000)  # 2025-10-09T08:53:20Z

    print("== 1. Four ids for the same posting, four shapes ==")
    v4 = UuidV4Generator(rng)
    v7 = UuidV7Generator(clock, rng)
    ulid = UlidGenerator(clock, rng)
    flake = SnowflakeGenerator(clock, 17, 5)
    a4, a7, au, af = v4.next(), v7.next(), ulid.next(), flake.next()
    print(f"  UUIDv4    {a4}  version={a4.version()} variant={a4.variant()}  (122 random bits)")
    print(f"  UUIDv7    {a7}  version={a7.version()} ts={a7.timestamp_millis()} (48-bit ms, 12-bit counter, 62 random)")
    print(f"  ULID      {au}  ts={au.timestamp_millis} (48-bit ms, 80 random, base32)")
    print(f"  Snowflake {af}  ts={SnowflakeGenerator.timestamp_millis_of(af)} worker={SnowflakeGenerator.worker_id_of(af)} "
          f"seq={SnowflakeGenerator.sequence_of(af)} (63 bits)")

    print()
    print("== 2. Sortability: 1000 ids over 1000 ms, then sort the strings ==")
    flakes: list[int] = []
    v4_str: list[str] = []
    v7_str: list[str] = []
    ulid_str: list[str] = []
    for _ in range(1000):
        clock.advance(1)
        x4, x7, xu, xf = v4.next(), v7.next(), ulid.next(), flake.next()
        flakes.append(xf)
        v4_str.append(str(x4))
        v7_str.append(str(x7))
        ulid_str.append(str(xu))
    print(f"  generation order == string sort order?  v4={java_bool(is_lexically_increasing(v4_str))}  "
          f"v7={java_bool(is_lexically_increasing(v7_str))}  ulid={java_bool(is_lexically_increasing(ulid_str))}  "
          f"snowflake={java_bool(is_strictly_increasing(flakes))}")
    sorted4 = sorted(v4_str)
    displaced = sum(1 for i in range(1000) if sorted4[i] != v4_str[i])
    print(f"  v4: {displaced} of 1000 ids change position when sorted -> random B-tree page writes")

    print()
    print("== 3. Monotonic within one millisecond ==")
    frozen = ManualClock(1_760_000_000_000)
    fv7 = UuidV7Generator(frozen, JavaRandom(1))
    fu = UlidGenerator(frozen, JavaRandom(1))
    ff = SnowflakeGenerator(frozen, 3, 5)
    burst7, burst_u, burst_f = [], [], []
    for _ in range(5000):
        burst7.append(fv7.next())
        burst_u.append(fu.next())
        burst_f.append(ff.next())
    print(f"  5000 ids, clock frozen: v7 increasing={java_bool(is_strictly_increasing(burst7))} "
          f"ulid increasing={java_bool(is_strictly_increasing(burst_u))} "
          f"snowflake increasing={java_bool(is_strictly_increasing(burst_f))}")
    print(f"  v7 first two: {burst7[0]} -> {burst7[1]} (counter +1)")
    print(f"  ulid first two: {burst_u[0]} -> {burst_u[1]} (random part +1)")
    print(f"  snowflake borrowed {ff.borrowed_millis} ms of logical time to issue 5000 ids at 4096/ms")

    print()
    print("== 4. The clock moves backwards ==")
    drift = ManualClock(1_760_000_000_000)
    sf = SnowflakeGenerator(drift, 9, 5)
    before = sf.next()
    drift.set(1_760_000_000_000 - 3)
    after = sf.next()
    print(f"  3 ms backwards (within tolerance 5): issued {before} then {after}, still increasing={java_bool(after > before)}")
    drift.set(1_760_000_000_000 - 60_000)
    try:
        sf.next()
    except ClockMovedBackwardsException as e:
        print(f"  60 s backwards: {e}")
    dv7 = UuidV7Generator(drift, JavaRandom(2))
    drift.set(1_760_000_000_000)
    u1 = dv7.next()
    drift.set(1_760_000_000_000 - 60_000)
    u2 = dv7.next()
    print(f"  v7 after a 60 s step back: keeps the last timestamp, increasing={java_bool(u2.compare_to(u1) > 0)}")

    print()
    print("== 5. Collision math (birthday bound) ==")
    print(f"  UUIDv4, 122 random bits: 10^12 ids -> p = {fmt_sci(collision_probability(1e12, 122), 1)}; "
          f"50% at {fmt_sci(ids_for_collision_probability(0.5, 122), 2)} ids")
    print(f"  ULID, 80 random bits per ms: 10^6 ids in one ms -> p = {fmt_sci(collision_probability(1e6, 80), 1)}; "
          f"10^9 in one ms -> p = {fmt_sci(collision_probability(1e9, 80), 1)}")
    print(f"  UUIDv7, 62 random bits per ms across uncoordinated generators: 10^6 ids in one ms -> "
          f"p = {fmt_sci(collision_probability(1e6, 62), 1)}")
    print(f"  a 32-bit random id: 10^5 ids -> p = {fmt_fixed(collision_probability(1e5, 32), 3)} (50% at about 77,000 ids)")
    print("  Snowflake: zero collision probability by construction, 4096 ids/ms/worker, 1024 workers, 69.7 years of 41-bit ms")

    print()
    print("== 6. Round trips ==")
    print(f"  Uuid.parse(toString) == original: {java_bool(Uuid.parse(str(a7)) == a7)}")
    print(f"  Ulid.parse(toString) == original: {java_bool(Ulid.parse(str(au)) == au)}")
    print(f"  Ulid lenient decode: parse accepts lowercase and maps I/L->1, O->0: "
          f"{java_bool(Ulid.parse(str(au).lower()) == au)}")


if __name__ == "__main__":
    main()
