"""Lesson: phases/01-complexity-and-core-structures/11-bit-manipulation/docs/en.md
Bit Manipulation and Compact Encodings: masks, popcount, flags, bitmaps, ISO 8583, Snowflake ids.
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: ISO 8583-1:2003 (bitmap and data-element layout); Twitter Snowflake; Warren, "Hacker's Delight",
2nd ed., ch. 2 and 5; JLS 15.19 (shift operators, emulated here because Python ints are unbounded).
"""

from __future__ import annotations

from dataclasses import dataclass
from typing import Callable, Iterable

# Python ints are unbounded two's-complement integers with no sign bit to overflow into and no >>>.
# A Java long is represented here as a Python int in [LONG_MIN, LONG_MAX]; these helpers reproduce the
# fixed-width behaviour the lesson is about, so every result matches the Java demo bit for bit.
MASK64 = (1 << 64) - 1
MASK32 = (1 << 32) - 1
LONG_MIN = -(1 << 63)
LONG_MAX = (1 << 63) - 1


def to_int64(v: int) -> int:
    """Wrap to a signed 64-bit value, exactly what a Java long does on overflow."""
    v &= MASK64
    return v - (1 << 64) if v >= (1 << 63) else v


def to_int32(v: int) -> int:
    """Wrap to a signed 32-bit value: Java int arithmetic."""
    v &= MASK32
    return v - (1 << 32) if v >= (1 << 31) else v


def to_int8(v: int) -> int:
    """Java's (byte) cast: keep the low 8 bits and reinterpret them as signed."""
    v &= 0xFF
    return v - 256 if v >= 128 else v


def urshift64(v: int, n: int) -> int:
    """Java's >>> on a long: view the value as unsigned 64 bits, then shift in zeros."""
    return to_int64((v & MASK64) >> n)


def urshift32(v: int, n: int) -> int:
    """Java's >>> on an int."""
    return to_int32((v & MASK32) >> n)


def java_div(a: int, b: int) -> int:
    """Java integer division truncates toward zero; Python's // floors."""
    q = abs(a) // abs(b)
    return q if (a < 0) == (b < 0) else -q


def java_rem(a: int, b: int) -> int:
    """Java's % takes the sign of the dividend; Python's % takes the sign of the divisor."""
    return a - b * java_div(a, b)


class Bits:
    """Masks, fields, and counting on 64-bit values. Every method is a few instructions; the point is to name them."""

    @staticmethod
    def mask(width: int) -> int:
        """A mask of `width` low bits set: mask(3) = 0b111. mask(64) is all ones (-1); mask(0) is 0."""
        if width < 0 or width > 64:
            raise ValueError(f"width must be 0..64: {width}")
        return -1 if width == 64 else (1 << width) - 1

    @staticmethod
    def is_set(v: int, bit: int) -> bool:
        return (((v & MASK64) >> bit) & 1) != 0

    @staticmethod
    def set(v: int, bit: int) -> int:
        return to_int64(v | (1 << bit))

    @staticmethod
    def clear(v: int, bit: int) -> int:
        return to_int64(v & ~(1 << bit))

    @staticmethod
    def toggle(v: int, bit: int) -> int:
        return to_int64(v ^ (1 << bit))

    @staticmethod
    def extract(v: int, shift: int, width: int) -> int:
        """Read `width` bits starting at `shift` as an unsigned value."""
        Bits._check_field(shift, width)
        return to_int64(((v & MASK64) >> shift) & Bits.mask(width))

    @staticmethod
    def insert(v: int, shift: int, width: int, field: int) -> int:
        """Write `field` into the `width` bits at `shift`, rejecting a field that does not fit."""
        Bits._check_field(shift, width)
        if (field & ~Bits.mask(width)) != 0:
            raise ValueError(f"{field} does not fit in {width} bits")
        hole = ~(Bits.mask(width) << shift)
        return to_int64((v & hole) | (field << shift))

    @staticmethod
    def _check_field(shift: int, width: int) -> None:
        if shift < 0 or width < 1 or shift + width > 64:
            raise ValueError(f"field {shift}+{width} outside 64 bits")

    @staticmethod
    def popcount_kernighan(v: int) -> int:
        """Number of one bits, by clearing the lowest set bit until nothing is left. O(popcount).
        The value is made unsigned first: a negative Python int has infinitely many one bits."""
        v &= MASK64
        n = 0
        while v != 0:
            v &= v - 1
            n += 1
        return n

    @staticmethod
    def popcount_parallel(v: int) -> int:
        """Number of one bits in constant time: add neighbours in 2-, 4-, 8-bit groups, then fold."""
        v &= MASK64
        v = v - ((v >> 1) & 0x5555555555555555)
        v = (v & 0x3333333333333333) + ((v >> 2) & 0x3333333333333333)
        v = (v + (v >> 4)) & 0x0F0F0F0F0F0F0F0F
        return ((v * 0x0101010101010101) & MASK64) >> 56  # the multiply wraps at 64 bits in Java

    @staticmethod
    def lowest_set_bit(v: int) -> int:
        """The lowest set bit on its own: 0b101100 -> 0b100. Zero for zero."""
        return to_int64(v & -v)

    @staticmethod
    def is_power_of_two(v: int) -> bool:
        return v > 0 and (v & (v - 1)) == 0

    @staticmethod
    def round_up_to_power_of_two(v: int) -> int:
        """Smallest power of two >= v, for v in 1..2^62. Used by hash-map and ring-buffer capacities."""
        if v < 1 or v > (1 << 62):
            raise ValueError(f"out of range: {v}")
        return 1 if v == 1 else 1 << (v - 1).bit_length()  # Long.highestOneBit(v - 1) << 1

    @staticmethod
    def to_binary(v: int, width: int) -> str:
        """Fixed-width binary rendering, most significant bit first."""
        return "".join("1" if Bits.is_set(v, i) else "0" for i in range(width - 1, -1, -1))

    @staticmethod
    def to_hex(v: int, digits: int) -> str:
        """Fixed-width upper-case hex of the unsigned 64-bit value (Long.toHexString)."""
        s = format(v & MASK64, "X")
        return "0" * max(0, digits - len(s)) + s


class AccountFlags:
    """Account state as a set of flags in one int. Each flag owns one bit; combinations are OR, tests are AND."""

    FROZEN = 1 << 0
    KYC_VERIFIED = 1 << 1
    PAYOUTS_ENABLED = 1 << 2
    HIGH_RISK = 1 << 3
    CHARGEBACK_HOLD = 1 << 4
    _NAMES = ["FROZEN", "KYC_VERIFIED", "PAYOUTS_ENABLED", "HIGH_RISK", "CHARGEBACK_HOLD"]

    def __init__(self, bits: int) -> None:
        if (bits & ~Bits.mask(len(self._NAMES))) != 0:
            raise ValueError(f"unknown flag bits in {bits:b}")
        self._bits = bits

    @staticmethod
    def none() -> AccountFlags:
        return AccountFlags(0)

    def has(self, flag: int) -> bool:
        return (self._bits & flag) == flag

    def has_any(self, flags: int) -> bool:
        return (self._bits & flags) != 0

    def with_(self, flag: int) -> AccountFlags:
        return AccountFlags(self._bits | flag)

    def without(self, flag: int) -> AccountFlags:
        return AccountFlags(self._bits & ~flag)

    def bits(self) -> int:
        return self._bits

    def can_pay_out(self) -> bool:
        """The business rule in one expression: may this account receive a payout right now?"""
        return (self.has(AccountFlags.KYC_VERIFIED | AccountFlags.PAYOUTS_ENABLED)
                and not self.has_any(AccountFlags.FROZEN | AccountFlags.CHARGEBACK_HOLD))

    def __str__(self) -> str:
        on = [name for i, name in enumerate(self._NAMES) if Bits.is_set(self._bits, i)]
        return "{" + ", ".join(on) + "}"


class Bitmap:
    """Dense bit set over 64-bit words: one bit per index, 64 per word. Words are stored as unsigned Python
    ints (0..2^64-1), which is the honest type for a bitmap word; Java has to use a signed long."""

    def __init__(self, capacity: int) -> None:
        if capacity < 0:
            raise ValueError("capacity must be >= 0")
        self._capacity = capacity
        self._words = [0] * ((capacity + 63) >> 6)

    def capacity(self) -> int:
        return self._capacity

    def words_allocated(self) -> int:
        return len(self._words)

    def _check_index(self, i: int) -> None:
        if i < 0 or i >= self._capacity:
            raise IndexError(f"bit {i} not in 0..{self._capacity - 1}")

    def set(self, i: int) -> None:
        self._check_index(i)
        self._words[i >> 6] |= 1 << (i & 63)

    def clear(self, i: int) -> None:
        self._check_index(i)
        self._words[i >> 6] &= ~(1 << (i & 63)) & MASK64

    def get(self, i: int) -> bool:
        self._check_index(i)
        return (self._words[i >> 6] & (1 << (i & 63))) != 0

    def cardinality(self) -> int:
        return sum(Bits.popcount_parallel(w) for w in self._words)

    def next_set_bit(self, frm: int) -> int:
        """Index of the first set bit at or after `frm`, or -1. Skips whole zero words."""
        if frm < 0:
            raise IndexError("from must be >= 0")
        if frm >= self._capacity:
            return -1
        w = frm >> 6
        word = self._words[w] & ((MASK64 << (frm & 63)) & MASK64)  # drop bits below `frm`
        while True:
            if word != 0:
                idx = (w << 6) + ((word & -word).bit_length() - 1)  # Long.numberOfTrailingZeros
                return idx if idx < self._capacity else -1
            w += 1
            if w == len(self._words):
                return -1
            word = self._words[w]

    def and_(self, other: Bitmap) -> Bitmap:
        return self._combine(other, True)

    def or_(self, other: Bitmap) -> Bitmap:
        return self._combine(other, False)

    def _combine(self, other: Bitmap, use_and: bool) -> Bitmap:
        if other._capacity != self._capacity:
            raise ValueError("bitmaps must have equal capacity")
        out = Bitmap(self._capacity)
        for i in range(len(self._words)):
            out._words[i] = (self._words[i] & other._words[i]) if use_and else (self._words[i] | other._words[i])
        return out


class Iso8583Bitmap:
    """ISO 8583 bitmap: 64 bits announcing which data elements follow. Field f (1..64) is bit (64 - f) of the
    primary bitmap, so field 1 is the most significant bit; field 1 means "a secondary bitmap follows"."""

    @staticmethod
    def encode(fields: Iterable[int]) -> str:
        """16 hex characters, or 32 when any field > 64 is present."""
        primary, secondary = 0, 0
        for f in fields:
            if f < 2 or f > 128:
                raise ValueError(f"field must be 2..128 (field 1 is derived): {f}")
            if f <= 64:
                primary |= 1 << (64 - f)
            else:
                secondary |= 1 << (128 - f)
        if secondary != 0:
            primary |= 1 << 63  # field 1: secondary bitmap present
            return Bits.to_hex(primary, 16) + Bits.to_hex(secondary, 16)
        return Bits.to_hex(primary, 16)

    @staticmethod
    def decode(hex_str: str) -> list[int]:
        """Decode 16 or 32 hex characters into the ascending list of present fields (field 1 excluded)."""
        if len(hex_str) not in (16, 32):
            raise ValueError(f"bitmap must be 16 or 32 hex chars, got {len(hex_str)}")
        primary = int(hex_str[:16], 16)  # Long.parseUnsignedLong; raises ValueError on a non-hex character
        has_secondary = Bits.is_set(primary, 63)
        if has_secondary != (len(hex_str) == 32):
            raise ValueError(f"field 1 says secondary bitmap {'present' if has_secondary else 'absent'}"
                             " but length says otherwise")
        fields = [f for f in range(2, 65) if Bits.is_set(primary, 64 - f)]
        if has_secondary:
            secondary = int(hex_str[16:], 16)
            fields += [f for f in range(65, 129) if Bits.is_set(secondary, 128 - f)]
        return fields


@dataclass(frozen=True)
class Parts:
    timestamp_millis: int
    worker: int
    sequence: int

    def __str__(self) -> str:
        return f"Parts[timestamp_millis={self.timestamp_millis}, worker={self.worker}, sequence={self.sequence}]"


class SnowflakeId:
    """Snowflake-style 64-bit id: 0 | 41-bit ms since a custom epoch | 10-bit worker | 12-bit sequence.
    Ids are unique per worker and increase with time, so they sort by creation order as plain integers."""

    EPOCH_MILLIS = 1_700_000_000_000  # 2023-11-14T22:13:20Z; ids stay positive until 2093
    SEQUENCE_BITS = 12
    WORKER_BITS = 10
    TIMESTAMP_BITS = 41
    WORKER_SHIFT = SEQUENCE_BITS
    TIMESTAMP_SHIFT = SEQUENCE_BITS + WORKER_BITS
    MAX_SEQUENCE = (1 << SEQUENCE_BITS) - 1
    MAX_WORKER = (1 << WORKER_BITS) - 1
    MAX_TIMESTAMP = (1 << TIMESTAMP_BITS) - 1

    @staticmethod
    def compose(timestamp_millis: int, worker: int, sequence: int) -> int:
        delta = timestamp_millis - SnowflakeId.EPOCH_MILLIS
        if delta < 0 or delta > SnowflakeId.MAX_TIMESTAMP:
            raise ValueError("timestamp outside the 41-bit window")
        i = 0
        i = Bits.insert(i, SnowflakeId.TIMESTAMP_SHIFT, SnowflakeId.TIMESTAMP_BITS, delta)
        i = Bits.insert(i, SnowflakeId.WORKER_SHIFT, SnowflakeId.WORKER_BITS, worker)
        i = Bits.insert(i, 0, SnowflakeId.SEQUENCE_BITS, sequence)
        return i

    @staticmethod
    def decompose(id_: int) -> Parts:
        if id_ < 0:
            raise ValueError("negative id: sign bit must be zero")
        return Parts(
            Bits.extract(id_, SnowflakeId.TIMESTAMP_SHIFT, SnowflakeId.TIMESTAMP_BITS) + SnowflakeId.EPOCH_MILLIS,
            Bits.extract(id_, SnowflakeId.WORKER_SHIFT, SnowflakeId.WORKER_BITS),
            Bits.extract(id_, 0, SnowflakeId.SEQUENCE_BITS))

    class Generator:
        """Per-worker generator. The clock is injected so tests and the demo are deterministic."""

        def __init__(self, worker: int, clock: Callable[[], int]) -> None:
            if worker < 0 or worker > SnowflakeId.MAX_WORKER:
                raise ValueError(f"worker must be 0..{SnowflakeId.MAX_WORKER}")
            self._worker = worker
            self._clock = clock
            self._last_millis = -1
            self._sequence = 0
            self._waits = 0  # instrumentation: how often the sequence overflowed within one millisecond

        def next(self) -> int:
            now = self._clock()
            if now < self._last_millis:
                # Java: IllegalStateException
                raise RuntimeError(f"clock moved backwards by {self._last_millis - now} ms; refusing to issue ids")
            if now == self._last_millis:
                self._sequence = (self._sequence + 1) & SnowflakeId.MAX_SEQUENCE
                if self._sequence == 0:  # 4096 ids in one millisecond: wait for the clock to advance
                    self._waits += 1
                    while now <= self._last_millis:
                        now = self._clock()
            else:
                self._sequence = 0
            self._last_millis = now
            return SnowflakeId.compose(now, self._worker, self._sequence)

        def waits(self) -> int:
            return self._waits


def _b(value: bool) -> str:
    return "true" if value else "false"


def _list(values: Iterable[int]) -> str:
    return "[" + ", ".join(str(v) for v in values) + "]"


def demo_masks_and_fields() -> None:
    print("== Masks, fields, popcount ==")
    v = 0b1011_0100
    print(f"v            = {Bits.to_binary(v, 8)}")
    print(f"set bit 0    = {Bits.to_binary(Bits.set(v, 0), 8)}")
    print(f"clear bit 7  = {Bits.to_binary(Bits.clear(v, 7), 8)}")
    print(f"toggle bit 2 = {Bits.to_binary(Bits.toggle(v, 2), 8)}")
    print(f"extract bits 4..6 = {Bits.extract(v, 4, 3)}  (0b011)")
    print(f"insert 0b110 at 4 = {Bits.to_binary(Bits.insert(v, 4, 3, 0b110), 8)}")
    print(f"popcount(v) kernighan={Bits.popcount_kernighan(v)} parallel={Bits.popcount_parallel(v)}"
          f" int.bit_count={v.bit_count()}")
    print(f"lowest set bit of 0b1011_0100 = {Bits.to_binary(Bits.lowest_set_bit(v), 8)}")
    print(f"roundUpToPowerOfTwo(1000) = {Bits.round_up_to_power_of_two(1000)}, "
          f"isPowerOfTwo(1024) = {_b(Bits.is_power_of_two(1024))}")
    print()


def demo_flags() -> None:
    print("== Account flags: five booleans in one int ==")
    f = AccountFlags.none().with_(AccountFlags.KYC_VERIFIED).with_(AccountFlags.PAYOUTS_ENABLED)
    print(f"{f} bits={Bits.to_binary(f.bits(), 5)} canPayOut={_b(f.can_pay_out())}")
    f = f.with_(AccountFlags.CHARGEBACK_HOLD)
    print(f"{f} bits={Bits.to_binary(f.bits(), 5)} canPayOut={_b(f.can_pay_out())}")
    f = f.without(AccountFlags.CHARGEBACK_HOLD).with_(AccountFlags.HIGH_RISK)
    print(f"{f} bits={Bits.to_binary(f.bits(), 5)} canPayOut={_b(f.can_pay_out())} (high risk alone does not block)")
    print()


def first_n(b: Bitmap, n: int) -> list[int]:
    out: list[int] = []
    i = b.next_set_bit(0)
    while i >= 0 and len(out) < n:
        out.append(i)
        i = b.next_set_bit(i + 1)
    return out


def demo_bitmap() -> None:
    print("== Bitmap: which of 1,000,000 merchants settled today ==")
    merchants = 1_000_000
    settled_today, active_this_month = Bitmap(merchants), Bitmap(merchants)
    for m in range(0, merchants, 7):
        settled_today.set(m)
    for m in range(0, merchants, 5):
        active_this_month.set(m)
    both = active_this_month.and_(settled_today)
    print("memory: %d longs = %d KB for %d merchants (a boolean[] would be %d KB, a HashSet<Integer> about %d KB)" % (
        settled_today.words_allocated(), settled_today.words_allocated() * 8 // 1024, merchants,
        merchants // 1024, merchants // 7 * 48 // 1024))
    print(f"settled today: {settled_today.cardinality()}, active this month: {active_this_month.cardinality()}"
          f", both: {both.cardinality()}")
    print(f"first five settled merchants: {_list(first_n(settled_today, 5))}; "
          f"first set bit at or after 100: {settled_today.next_set_bit(100)}")
    print()


def demo_iso8583() -> None:
    print("== ISO 8583 bitmap ==")
    auth = sorted({2, 3, 4, 7, 11, 12, 13, 22, 24, 25, 35, 41, 42, 49})
    primary = Iso8583Bitmap.encode(auth)
    print(f"0100 authorisation request fields {_list(auth)}")
    print(f"primary bitmap hex   = {primary}")
    print(f"primary bitmap bits  = {Bits.to_binary(int(primary, 16), 64)}")
    print(f"decoded              = {_list(Iso8583Bitmap.decode(primary))}")
    with_secondary = sorted(set(auth) | {102})
    both = Iso8583Bitmap.encode(with_secondary)
    print("adding field 102 (account id 1) sets field 1 and appends a secondary bitmap:")
    print(f"bitmaps hex          = {both}  ({len(both)} hex chars)")
    print(f"decoded              = {_list(Iso8583Bitmap.decode(both))}")
    print()


def demo_snowflake() -> None:
    print("== Snowflake-style ids: 41 + 10 + 12 bits ==")
    fake_now = [SnowflakeId.EPOCH_MILLIS + 86_400_000]  # one day after the epoch
    calls = [0]

    def clock() -> int:  # advances 1 ms every 5,000 reads
        calls[0] += 1
        if calls[0] % 5_000 == 0:
            fake_now[0] += 1
        return fake_now[0]

    gen = SnowflakeId.Generator(7, clock)
    prev = -1
    monotonic = True
    first = 0
    for i in range(10_000):
        id_ = gen.next()
        if i == 0:
            first = id_
        if id_ <= prev:
            monotonic = False
        prev = id_
    p = SnowflakeId.decompose(first)
    print(f"first id = {first} = {Bits.to_binary(first, 64)}")
    print(f"  timestamp {p.timestamp_millis} ms, worker {p.worker}, sequence {p.sequence}")
    print(f"10,000 ids from one worker: strictly increasing = {_b(monotonic)}, "
          f"sequence overflows waited out = {gen.waits()}")
    print(f"last id decomposed: {SnowflakeId.decompose(prev)}")
    print()


def demo_twos_complement_traps() -> None:
    print("== Two's complement traps ==")
    print(f"Math.abs(Long.MIN_VALUE)      = {to_int64(abs(LONG_MIN))}  (still negative: there is no +2^63)")
    print(f"Long.MAX_VALUE + 1            = {to_int64(LONG_MAX + 1)}  (wraps silently; Math.addExact throws)")
    print(f"(byte) 0x80                   = {to_int8(0x80)}  and (byte)0x80 & 0xFF = {to_int8(0x80) & 0xFF}"
          "  (mask before you widen)")
    print(f"-8 >> 1  (arithmetic)         = {-8 >> 1}   -8 >>> 1 (logical) = {urshift32(-8, 1)}")
    print(f"1 << 31  (int)                = {to_int32(1 << 31)}  1L << 31 (long) = {1 << 31}")
    print(f"1 << 32  (int, shift mod 32)  = {to_int32(1 << (32 & 31))}"
          "  (JLS 15.19: only the low 5 bits of the shift count are used)")
    lo, hi = 2_000_000_000, 2_100_000_000
    print(f"(lo + hi) / 2 with int        = {java_div(to_int32(lo + hi), 2)}  vs  lo + ((hi - lo) >>> 1) = "
          f"{to_int32(lo + urshift32(hi - lo, 1))}  (the binary-search midpoint bug)")
    print(f"-7 / 2 = {java_div(-7, 2)}  -7 >> 1 = {-7 >> 1}  (division truncates toward zero, shift floors)")
    print(f"-7 % 2 = {java_rem(-7, 2)}   -7 & 1 = {-7 & 1}  (use & for \"is odd\" on negatives)")


def main() -> None:
    demo_masks_and_fields()
    demo_flags()
    demo_bitmap()
    demo_iso8583()
    demo_snowflake()
    demo_twos_complement_traps()


if __name__ == "__main__":
    main()
