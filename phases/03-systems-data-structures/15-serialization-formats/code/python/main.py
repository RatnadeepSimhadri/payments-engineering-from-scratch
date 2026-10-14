"""Lesson: phases/03-systems-data-structures/15-serialization-formats/docs/en.md
Serialization: JSON, Protobuf-style Binary, and Schema Evolution
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: Protocol Buffers encoding spec (protobuf.dev/programming-guides/encoding): varints, ZigZag, tags, wire types,
unknown-field handling; RFC 8259 (JSON); IEEE 754 for the 2^53 integer limit. Python 3.11+, standard library only.
"""

from __future__ import annotations

from dataclasses import dataclass
from decimal import ROUND_HALF_UP, Decimal
from enum import Enum
from typing import Optional, Union

MASK64 = 0xFFFFFFFFFFFFFFFF
LONG_MIN, LONG_MAX = -(1 << 63), (1 << 63) - 1


def to_int64(x: int) -> int:
    """Reinterpret the low 64 bits as a signed Java long. Every 'long' in this file goes through here."""
    x &= MASK64
    return x - (1 << 64) if x >= (1 << 63) else x


def to_int32(x: int) -> int:
    """Java's (int) cast: keep the low 32 bits, signed."""
    x &= 0xFFFFFFFF
    return x - (1 << 32) if x >= (1 << 31) else x


def fmt_fixed(x: float, digits: int) -> str:
    """Java's %.Nf: round the shortest decimal repr HALF_UP."""
    q = Decimal(repr(x)).quantize(Decimal(1).scaleb(-digits), rounding=ROUND_HALF_UP)
    return f"{q:.{digits}f}"


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
        return to_int64((self._next(32) << 32) + self._next(32))

    def next_boolean(self) -> bool:
        return self._next(1) != 0


@dataclass(frozen=True)
class Currency:
    """ISO 4217 currency: code and minor-unit exponent. Copied from Phase 10, Lesson 01."""

    code: str
    exponent: int

    def __post_init__(self) -> None:
        if self.code is None or len(self.code) != 3:
            raise ValueError("currency code must be three letters")
        if not 0 <= self.exponent <= 4:
            raise ValueError("exponent must be 0..4")

    @staticmethod
    def of(code: str) -> "Currency":
        return {"USD": USD, "JPY": JPY}.get(code) or Currency(code, 2)


USD = Currency("USD", 2)
JPY = Currency("JPY", 0)


@dataclass(frozen=True)
class Money:
    """Integer minor units plus a currency. On the wire: sint64 minor units + a 3-byte currency string."""

    minor_units: int
    currency: Currency

    def __post_init__(self) -> None:
        if self.currency is None:
            raise TypeError("currency")
        if not isinstance(self.minor_units, int) or isinstance(self.minor_units, bool):
            raise TypeError("minor_units must be an int; never construct Money from a float")
        if not LONG_MIN <= self.minor_units <= LONG_MAX:
            raise OverflowError("minor_units must fit in a signed 64-bit long")

    @staticmethod
    def of_minor(minor_units: int, currency: Currency) -> "Money":
        return Money(minor_units, currency)

    def __str__(self) -> str:
        scale = 10 ** self.currency.exponent
        sign = "-" if self.minor_units < 0 else ""
        whole, frac = divmod(abs(self.minor_units), scale)
        if self.currency.exponent == 0:
            return f"{sign}{whole} {self.currency.code}"
        return f"{sign}{whole}.{frac:0{self.currency.exponent}d} {self.currency.code}"


class MalformedMessageException(Exception):
    """Same name as the Java class: truncated, overflowing, or mistyped input is an error, never a guess."""


class ByteSink:
    """Growable byte buffer for encoding."""

    def __init__(self) -> None:
        self._buf = bytearray()

    def write(self, b: Union[int, bytes, bytearray]) -> None:
        """Java overloads write(int) and write(byte[]); one method takes either here."""
        if isinstance(b, int):
            self._buf.append(b & 0xFF)
        else:
            self._buf += b

    def size(self) -> int:
        return len(self._buf)

    def to_byte_array(self) -> bytes:
        return bytes(self._buf)


class ByteSource:
    """Cursor over an encoded buffer. Every read checks bounds."""

    def __init__(self, buf: bytes, start: int = 0, limit: Optional[int] = None) -> None:
        self._buf = buf
        self._pos = start
        self._limit = len(buf) if limit is None else limit

    def has_more(self) -> bool:
        return self._pos < self._limit

    def position(self) -> int:
        return self._pos

    def read_byte(self) -> int:
        if self._pos >= self._limit:
            raise MalformedMessageException(f"truncated: wanted a byte at offset {self._pos}")
        b = self._buf[self._pos]
        self._pos += 1
        return b

    def read_bytes(self, n: int) -> bytes:
        if n < 0 or self._pos + n > self._limit:
            raise MalformedMessageException(f"truncated: wanted {n} bytes at offset {self._pos}")
        out = bytes(self._buf[self._pos:self._pos + n])
        self._pos += n
        return out

    def slice(self, n: int) -> "ByteSource":
        """A view over the next n bytes, for nested messages, without copying."""
        if n < 0 or self._pos + n > self._limit:
            raise MalformedMessageException(f"truncated: nested message of {n} bytes at offset {self._pos}")
        s = ByteSource(self._buf, self._pos, self._pos + n)
        self._pos += n
        return s


class Varint:
    """Varint and ZigZag. Values are Java longs (signed 64-bit); the wire form is their unsigned bit pattern."""

    MAX_BYTES = 10

    @staticmethod
    def write_unsigned(out: ByteSink, value: int) -> None:
        """Little-endian base-128: 7 payload bits per byte, high bit set on every byte but the last."""
        value &= MASK64  # the unsigned view; Java's >>> does this implicitly
        while value & ~0x7F:
            out.write((value & 0x7F) | 0x80)
            value >>= 7
        out.write(value)

    @staticmethod
    def read_unsigned(src: ByteSource) -> int:
        result = 0
        for shift in range(0, 64, 7):
            b = src.read_byte()
            result |= (b & 0x7F) << shift
            if b & 0x80 == 0:
                if shift == 63 and (b & 0x7E) != 0:
                    raise MalformedMessageException("varint overflows 64 bits")
                return to_int64(result)
        raise MalformedMessageException("varint longer than 10 bytes")

    @staticmethod
    def zigzag_encode(n: int) -> int:
        """0,-1,1,-2,2 -> 0,1,2,3,4. Python's >> on a negative int is arithmetic, like Java's >>."""
        return to_int64((n << 1) ^ (n >> 63))

    @staticmethod
    def zigzag_decode(n: int) -> int:
        return to_int64(((n & MASK64) >> 1) ^ -(n & 1))

    @staticmethod
    def write_signed(out: ByteSink, value: int) -> None:
        Varint.write_unsigned(out, Varint.zigzag_encode(value))

    @staticmethod
    def read_signed(src: ByteSource) -> int:
        return Varint.zigzag_decode(Varint.read_unsigned(src))

    @staticmethod
    def encoded_size(unsigned_value: int) -> int:
        unsigned_value &= MASK64
        n = 1
        while unsigned_value & ~0x7F:
            unsigned_value >>= 7
            n += 1
        return n


class WireType(Enum):
    """Protobuf wire types: the decoder needs only this to skip a field it does not know."""

    VARINT = 0
    FIXED64 = 1
    LENGTH_DELIMITED = 2
    FIXED32 = 5

    @property
    def code(self) -> int:
        return self.value

    @staticmethod
    def of(code: int) -> "WireType":
        for w in WireType:
            if w.code == code:
                return w
        raise MalformedMessageException(f"unknown wire type {code} (groups are not supported)")


class Encoder:
    """Writes tag-prefixed fields. A tag is varint((field_number << 3) | wire_type)."""

    def __init__(self) -> None:
        self.out = ByteSink()

    def _tag(self, field: int, type_: WireType) -> None:
        if field < 1 or field > 536_870_911:
            raise ValueError(f"field number out of range: {field}")
        Varint.write_unsigned(self.out, (field << 3) | type_.code)

    def uint64(self, field: int, value: int) -> "Encoder":
        self._tag(field, WireType.VARINT)
        Varint.write_unsigned(self.out, value)
        return self

    def sint64(self, field: int, value: int) -> "Encoder":
        self._tag(field, WireType.VARINT)
        Varint.write_signed(self.out, value)
        return self

    def bool(self, field: int, value: bool) -> "Encoder":
        return self.uint64(field, 1 if value else 0)

    def fixed64(self, field: int, value: int) -> "Encoder":
        self._tag(field, WireType.FIXED64)
        self.out.write((value & MASK64).to_bytes(8, "little"))  # little-endian
        return self

    def bytes(self, field: int, value: bytes) -> "Encoder":
        self._tag(field, WireType.LENGTH_DELIMITED)
        Varint.write_unsigned(self.out, len(value))
        self.out.write(value)
        return self

    def string(self, field: int, value: str) -> "Encoder":
        return self.bytes(field, value.encode("utf-8"))

    def message(self, field: int, nested: "Encoder") -> "Encoder":
        """A nested message is just bytes: encode it separately, then write it length-delimited."""
        return self.bytes(field, nested.to_byte_array())

    def to_byte_array(self) -> bytes:
        return self.out.to_byte_array()


class Decoder:
    """Reads tag-prefixed fields in order. Unknown fields are skipped by wire type."""

    def __init__(self, source: Union[bytes, bytearray, ByteSource]) -> None:
        self._in = source if isinstance(source, ByteSource) else ByteSource(bytes(source))
        self._field = 0
        self._type: Optional[WireType] = None
        self.skipped = 0

    def next(self) -> bool:
        """Advance to the next field. False at end of message."""
        if not self._in.has_more():
            return False
        tag = Varint.read_unsigned(self._in)
        self._field = to_int32((tag & MASK64) >> 3)
        if self._field == 0:
            raise MalformedMessageException("field number 0 is reserved")
        self._type = WireType.of(tag & 0x7)
        return True

    def field(self) -> int:
        return self._field

    def type(self) -> Optional[WireType]:
        return self._type

    def _expect(self, expected: WireType) -> None:
        if self._type is not expected:
            raise MalformedMessageException(
                f"field {self._field}: expected {expected.name} but found {self._type.name}")

    def uint64(self) -> int:
        self._expect(WireType.VARINT)
        return Varint.read_unsigned(self._in)

    def sint64(self) -> int:
        self._expect(WireType.VARINT)
        return Varint.read_signed(self._in)

    def bool(self) -> bool:
        return self.uint64() != 0

    def fixed64(self) -> int:
        self._expect(WireType.FIXED64)
        v = 0
        for i in range(8):
            v |= self._in.read_byte() << (8 * i)
        return to_int64(v)

    def bytes(self) -> bytes:
        self._expect(WireType.LENGTH_DELIMITED)
        n = to_int32(Varint.read_unsigned(self._in))
        return self._in.read_bytes(n)

    def string(self) -> str:
        return self.bytes().decode("utf-8", errors="replace")  # Java's new String(..., UTF_8) also replaces

    def message(self) -> "Decoder":
        self._expect(WireType.LENGTH_DELIMITED)
        n = to_int32(Varint.read_unsigned(self._in))
        return Decoder(self._in.slice(n))

    def skip(self) -> None:
        """Skip the current field's value using only its wire type."""
        self.skipped += 1
        if self._type is WireType.VARINT:
            Varint.read_unsigned(self._in)
        elif self._type is WireType.FIXED64:
            self._in.read_bytes(8)
        elif self._type is WireType.FIXED32:
            self._in.read_bytes(4)
        elif self._type is WireType.LENGTH_DELIMITED:
            self._in.read_bytes(to_int32(Varint.read_unsigned(self._in)))


@dataclass(frozen=True)
class PostingV1:
    """Schema v1: 1 id (uint64), 2 amount_minor (sint64), 3 currency, 4 debit_account, 5 credit_account."""

    id: int
    amount: Money
    debit_account: str
    credit_account: str

    F_ID, F_AMOUNT, F_CURRENCY, F_DEBIT, F_CREDIT = 1, 2, 3, 4, 5

    def encode(self) -> bytes:
        return (Encoder()
                .uint64(self.F_ID, self.id)
                .sint64(self.F_AMOUNT, self.amount.minor_units)
                .string(self.F_CURRENCY, self.amount.currency.code)
                .string(self.F_DEBIT, self.debit_account)
                .string(self.F_CREDIT, self.credit_account)
                .to_byte_array())

    @staticmethod
    def decode(source: Union[bytes, Decoder]) -> "PostingV1":
        d = source if isinstance(source, Decoder) else Decoder(source)
        id_ = minor = 0
        currency = debit = credit = ""
        while d.next():
            f = d.field()
            if f == PostingV1.F_ID:
                id_ = d.uint64()
            elif f == PostingV1.F_AMOUNT:
                minor = d.sint64()
            elif f == PostingV1.F_CURRENCY:
                currency = d.string()
            elif f == PostingV1.F_DEBIT:
                debit = d.string()
            elif f == PostingV1.F_CREDIT:
                credit = d.string()
            else:
                d.skip()  # a field from the future: preserve nothing, break nothing
        if not currency:
            raise MalformedMessageException("posting without a currency")
        return PostingV1(id_, Money.of_minor(minor, Currency.of(currency)), debit, credit)

    def __str__(self) -> str:
        """Java record toString, so the demo output matches."""
        return (f"PostingV1[id={self.id}, amount={self.amount}, debitAccount={self.debit_account}, "
                f"creditAccount={self.credit_account}]")


@dataclass(frozen=True)
class Audit:
    """Added in v2 as a nested message: 1 actor (string), 2 recorded_at_millis (fixed64)."""

    actor: str
    recorded_at_millis: int

    def encode(self) -> bytes:
        return Encoder().string(1, self.actor).fixed64(2, self.recorded_at_millis).to_byte_array()

    @staticmethod
    def decode(d: Decoder) -> "Audit":
        actor, at = "", 0
        while d.next():
            if d.field() == 1:
                actor = d.string()
            elif d.field() == 2:
                at = d.fixed64()
            else:
                d.skip()
        return Audit(actor, at)

    def __str__(self) -> str:
        return f"Audit[actor={self.actor}, recordedAtMillis={self.recorded_at_millis}]"


@dataclass(frozen=True)
class PostingV2:
    """Schema v2: fields 1-5 unchanged; 6 memo, 7 audit (nested), 8 reversal (bool). Nothing is renumbered."""

    id: int
    amount: Money
    debit_account: str
    credit_account: str
    memo: str
    audit: Optional[Audit]
    reversal: bool

    F_MEMO, F_AUDIT, F_REVERSAL = 6, 7, 8

    def encode(self) -> bytes:
        e = (Encoder()
             .uint64(PostingV1.F_ID, self.id)
             .sint64(PostingV1.F_AMOUNT, self.amount.minor_units)
             .string(PostingV1.F_CURRENCY, self.amount.currency.code)
             .string(PostingV1.F_DEBIT, self.debit_account)
             .string(PostingV1.F_CREDIT, self.credit_account))
        if self.memo:  # default values are omitted, as protobuf does
            e.string(self.F_MEMO, self.memo)
        if self.audit is not None:
            e.message(self.F_AUDIT, Encoder().string(1, self.audit.actor).fixed64(2, self.audit.recorded_at_millis))
        if self.reversal:
            e.bool(self.F_REVERSAL, True)
        return e.to_byte_array()

    @staticmethod
    def decode(data: bytes) -> "PostingV2":
        d = Decoder(data)
        id_ = minor = 0
        currency = debit = credit = memo = ""
        audit: Optional[Audit] = None
        reversal = False
        while d.next():
            f = d.field()
            if f == PostingV1.F_ID:
                id_ = d.uint64()
            elif f == PostingV1.F_AMOUNT:
                minor = d.sint64()
            elif f == PostingV1.F_CURRENCY:
                currency = d.string()
            elif f == PostingV1.F_DEBIT:
                debit = d.string()
            elif f == PostingV1.F_CREDIT:
                credit = d.string()
            elif f == PostingV2.F_MEMO:
                memo = d.string()
            elif f == PostingV2.F_AUDIT:
                audit = Audit.decode(d.message())
            elif f == PostingV2.F_REVERSAL:
                reversal = d.bool()
            else:
                d.skip()
        if not currency:
            raise MalformedMessageException("posting without a currency")
        return PostingV2(id_, Money.of_minor(minor, Currency.of(currency)), debit, credit, memo, audit, reversal)

    def as_v1(self) -> PostingV1:
        return PostingV1(self.id, self.amount, self.debit_account, self.credit_account)


class Json:
    """A minimal JSON writer: RFC 8259 string escaping; 64-bit numbers optionally quoted."""

    @staticmethod
    def posting(p: PostingV2, numbers_as_strings: bool) -> str:
        def num(v: int) -> str:
            return f'"{v}"' if numbers_as_strings else str(v)

        parts = [f'{{"id":{num(p.id)}', f',"amountMinor":{num(p.amount.minor_units)}',
                 f',"currency":{Json.quote(p.amount.currency.code)}',
                 f',"debitAccount":{Json.quote(p.debit_account)}',
                 f',"creditAccount":{Json.quote(p.credit_account)}']
        if p.memo:
            parts.append(f',"memo":{Json.quote(p.memo)}')
        if p.audit is not None:
            parts.append(f',"audit":{{"actor":{Json.quote(p.audit.actor)},'
                         f'"recordedAtMillis":{p.audit.recorded_at_millis}}}')
        if p.reversal:
            parts.append(',"reversal":true')
        parts.append("}")
        return "".join(parts)

    @staticmethod
    def quote(s: str) -> str:
        out = ['"']
        for c in s:
            if c == '"':
                out.append('\\"')
            elif c == "\\":
                out.append("\\\\")
            elif c == "\n":
                out.append("\\n")
            elif c == "\r":
                out.append("\\r")
            elif c == "\t":
                out.append("\\t")
            elif ord(c) < 0x20:
                out.append(f"\\u{ord(c):04x}")
            else:
                out.append(c)
        out.append('"')
        return "".join(out)


def hex_bytes(b: bytes) -> str:
    return " ".join(f"{x:02x}" for x in b)


def java_bool(b: bool) -> str:
    return "true" if b else "false"


def java_str(x: object) -> str:
    return "null" if x is None else str(x)


def main() -> None:
    print("== 1. Varints: 7 bits per byte, little-endian, continuation bit ==")
    for v in (0, 1, 127, 128, 150, 300, 16_383, 16_384, 1_000_000_000, LONG_MAX):
        s = ByteSink()
        Varint.write_unsigned(s, v)
        print(f"  {v:20d} -> {hex_bytes(s.to_byte_array()):<30} ({s.size()} byte{'' if s.size() == 1 else 's'})")
    neg = ByteSink()
    Varint.write_unsigned(neg, -1)
    print(f"  -1 as a plain varint = {hex_bytes(neg.to_byte_array())} (10 bytes: the sign bit is 63 bits up)")

    print()
    print("== 2. ZigZag: small negatives stay small ==")
    for v in (0, -1, 1, -2, 2, -64, 63, -65, 64, LONG_MIN):
        s = ByteSink()
        Varint.write_signed(s, v)
        print(f"  {v:20d} -> zigzag {Varint.zigzag_encode(v):20d} -> {hex_bytes(s.to_byte_array()):<30} "
              f"({s.size()} byte{'' if s.size() == 1 else 's'})")
    refund = ByteSink()
    Varint.write_signed(refund, -1999)
    print(f"  a refund of -19.99 USD as sint64 = {hex_bytes(refund.to_byte_array())} (2 bytes, versus 10 as uint64)")

    print()
    print("== 3. Tags and wire types: the protobuf.dev test vectors ==")
    f1 = Encoder().uint64(1, 150).to_byte_array()
    f2 = Encoder().string(2, "testing").to_byte_array()
    print(f"  field 1 = 150        -> {hex_bytes(f1)}   (tag 0x08 = field 1 << 3 | VARINT)")
    print(f"  field 2 = \"testing\"  -> {hex_bytes(f2)}   (tag 0x12 = field 2 << 3 | LENGTH_DELIMITED, length 7)")

    print()
    print("== 4. A posting on the wire ==")
    p = PostingV2(1_048_577, Money.of_minor(-1999, USD), "acct:merchant:4471", "acct:customer:9", "refund order 5512",
                  Audit("svc-refunds", 1_760_000_000_000), True)
    bin_ = p.encode()
    print(f"  binary ({len(bin_)} bytes): {hex_bytes(bin_)}")
    json = Json.posting(p, False)
    print(f"  json   ({len(json.encode('utf-8'))} bytes): {json}")
    back = PostingV2.decode(bin_)
    print(f"  decode(encode(p)) equals p: {java_bool(back == p)}  amount = {back.amount}")

    print()
    print("== 5. Schema evolution ==")
    v1_view = Decoder(bin_)
    old_reader = PostingV1.decode(v1_view)
    print(f"  v1 reader on v2 bytes: {old_reader}  (skipped {v1_view.skipped} unknown fields by wire type)")
    v1 = PostingV1(7, Money.of_minor(1250, USD), "acct:a", "acct:b")
    new_reader = PostingV2.decode(v1.encode())
    print(f"  v2 reader on v1 bytes: memo=\"{new_reader.memo}\" audit={java_str(new_reader.audit)} "
          f"reversal={java_bool(new_reader.reversal)}  (defaults, not errors)")
    print(f"  v2 -> v1 -> compare core fields: {java_bool(back.as_v1() == old_reader)}")

    print()
    print("== 6. What JSON numbers cannot carry ==")
    big_id = 9_007_199_254_740_993  # 2^53 + 1
    as_double = float(big_id)
    print(f"  id {big_id} through a JSON parser that uses double -> {int(as_double)}  (lost: {big_id - int(as_double)})")
    print("  Twitter, Stripe, and this lesson send 64-bit ids as strings for this reason: "
          + Json.posting(PostingV2(big_id, p.amount, "a", "b", "", None, False), True))
    print(f"  0.1 + 0.2 in JSON-as-double = {0.1 + 0.2}; minor units as integers avoid the question entirely")

    print()
    print("== 7. Size over 10,000 random postings ==")
    rng = JavaRandom(11)
    bin_total = json_total = 0
    for _ in range(10_000):
        q = random_posting(rng)
        b = q.encode()
        if PostingV2.decode(b) != q:
            raise AssertionError("round trip failed")
        bin_total += len(b)
        json_total += len(Json.posting(q, True).encode("utf-8"))
    print(f"  binary {bin_total} bytes, json {json_total} bytes, ratio {fmt_fixed(json_total / bin_total, 2)}x; "
          "all 10000 round-tripped")

    print()
    print("== 8. Malformed input is an error, not a guess ==")
    truncated = bin_[:-3]
    try:
        PostingV2.decode(truncated)
    except MalformedMessageException as e:
        print(f"  last 3 bytes cut off: {e}")
    wrong_type = Encoder().string(PostingV1.F_AMOUNT, "19.99").to_byte_array()
    try:
        PostingV2.decode(wrong_type)
    except MalformedMessageException as e:
        print(f"  type changed on field 2: {e}")


def random_posting(rng: JavaRandom) -> PostingV2:
    """The seeded generator from the Java demo; draws happen in Java's left-to-right argument order."""
    id_ = (rng.next_long() & MASK64) >> 1  # nextLong() >>> 1
    minor = rng.next_int(2_000_001) - 1_000_000
    debit = f"acct:{rng.next_int(100_000)}"
    credit = f"acct:{rng.next_int(100_000)}"
    memo = f"memo {rng.next_int(1000)}" if rng.next_boolean() else ""
    audit = Audit(f"svc-{rng.next_int(10)}", 1_760_000_000_000 + rng.next_int(1_000_000)) if rng.next_boolean() else None
    reversal = rng.next_int(20) == 0
    return PostingV2(id_, Money.of_minor(minor, USD), debit, credit, memo, audit, reversal)


if __name__ == "__main__":
    main()
