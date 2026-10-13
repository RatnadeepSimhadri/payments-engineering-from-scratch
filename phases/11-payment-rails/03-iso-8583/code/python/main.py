"""Lesson: phases/11-payment-rails/03-iso-8583/docs/en.md
ISO 8583: Parsing the Card Network Message
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: ISO 8583-1:1987 (MTI, bitmaps, data element dictionary); ISO 4217 numeric currency codes.
Python 3.11+, standard library only. Same design as the Java and Go versions. ASCII variant; test PANs only.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from enum import Enum


class Iso8583Error(ValueError):
    """Any malformed message, field, or bitmap. Nothing is ever guessed or coerced."""


@dataclass(frozen=True)
class Currency:
    """ISO 4217 currency with its numeric code, the form DE49 carries (840 = USD)."""

    code: str
    numeric_code: int
    exponent: int

    def minor_units_per_major(self) -> int:
        return 10 ** self.exponent


USD = Currency("USD", 840, 2)
EUR = Currency("EUR", 978, 2)
GBP = Currency("GBP", 826, 2)
INR = Currency("INR", 356, 2)
JPY = Currency("JPY", 392, 0)
KWD = Currency("KWD", 414, 3)
_CURRENCIES = (USD, EUR, GBP, INR, JPY, KWD)


def currency_from_numeric(three_digits: str) -> Currency:
    for c in _CURRENCIES:
        if f"{c.numeric_code:03d}" == three_digits:
            return c
    raise Iso8583Error(f"unknown numeric currency code {three_digits}")


@dataclass(frozen=True)
class Money:
    """Integer minor units plus currency, copied from Phase 10, Lesson 01. DE4 is always minor units."""

    minor_units: int
    currency: Currency

    def __post_init__(self) -> None:
        if not isinstance(self.minor_units, int) or isinstance(self.minor_units, bool):
            raise TypeError("minor_units must be an int; never construct Money from a float")

    @staticmethod
    def of_minor(minor_units: int, currency: Currency) -> "Money":
        return Money(minor_units, currency)

    def __str__(self) -> str:
        magnitude = abs(self.minor_units)
        whole, frac = divmod(magnitude, self.currency.minor_units_per_major())
        text = str(whole)
        if self.currency.exponent > 0:
            text += "." + str(frac).rjust(self.currency.exponent, "0")
        return f"{'-' if self.minor_units < 0 else ''}{text} {self.currency.code}"


class FieldType(Enum):
    """How a data element's length is known: fixed by the spec, or announced by a 2- or 3-digit prefix."""

    FIXED = 0
    LLVAR = 2
    LLLVAR = 3

    @property
    def prefix_digits(self) -> int:
        return self.value


@dataclass(frozen=True)
class FieldSpec:
    """One row of the data element dictionary: number, length rule, length (exact or max), character class."""

    number: int
    type: FieldType
    length: int
    numeric: bool
    name: str

    def __post_init__(self) -> None:
        if not 2 <= self.number <= 128:
            raise ValueError(f"data element number must be 2..128: {self.number}")
        if self.length <= 0:
            raise ValueError("length must be positive")
        if self.type is FieldType.LLVAR and self.length > 99:
            raise ValueError("LLVAR max length is 99")
        if self.type is FieldType.LLLVAR and self.length > 999:
            raise ValueError("LLLVAR max length is 999")

    def validate(self, value: str) -> None:
        """Reject a value that does not fit this element. Called on set and again on encode."""
        label = f"DE{self.number} ({self.name})"
        if not value:
            raise Iso8583Error(f"{label} must not be empty")
        if self.type is FieldType.FIXED and len(value) != self.length:
            raise Iso8583Error(f"{label} must be exactly {self.length} chars, got {len(value)}")
        if self.type is not FieldType.FIXED and len(value) > self.length:
            raise Iso8583Error(f"{label} exceeds max length {self.length}: {len(value)}")
        if self.numeric and not value.isdigit():
            raise Iso8583Error(f"{label} must be numeric")
        if any(not 0x20 <= ord(ch) <= 0x7E for ch in value):
            raise Iso8583Error(f"DE{self.number} contains a non-ASCII character")


class Iso8583Spec:
    """The practical subset of the ISO 8583-1:1987 dictionary this lesson implements."""

    def __init__(self) -> None:
        self._fields: dict[int, FieldSpec] = {}

    @staticmethod
    def standard_1987() -> "Iso8583Spec":
        s = Iso8583Spec()
        for row in (
            (2, FieldType.LLVAR, 19, True, "primary account number"),
            (3, FieldType.FIXED, 6, True, "processing code"),
            (4, FieldType.FIXED, 12, True, "amount, transaction"),
            (7, FieldType.FIXED, 10, True, "transmission date and time MMDDhhmmss"),
            (11, FieldType.FIXED, 6, True, "system trace audit number"),
            (12, FieldType.FIXED, 6, True, "time, local transaction hhmmss"),
            (13, FieldType.FIXED, 4, True, "date, local transaction MMDD"),
            (22, FieldType.FIXED, 3, True, "point of service entry mode"),
            (37, FieldType.FIXED, 12, False, "retrieval reference number"),
            (39, FieldType.FIXED, 2, False, "response code"),
            (41, FieldType.FIXED, 8, False, "card acceptor terminal id"),
            (42, FieldType.FIXED, 15, False, "card acceptor id"),
            (49, FieldType.FIXED, 3, True, "currency code, transaction"),
            (102, FieldType.LLVAR, 28, False, "account identification 1"),
            (123, FieldType.LLLVAR, 999, False, "reserved for private use"),
        ):
            s.add(FieldSpec(*row))
        return s

    def add(self, spec: FieldSpec) -> "Iso8583Spec":
        self._fields[spec.number] = spec
        return self

    def require(self, number: int) -> FieldSpec:
        spec = self._fields.get(number)
        if spec is None:
            raise Iso8583Error(f"DE{number} is not defined in this specification")
        return spec

    def defines(self, number: int) -> bool:
        return number in self._fields


class Bitmap:
    """The 8-byte presence map. Bit 1 is the most significant bit of the first byte and announces a second bitmap."""

    def __init__(self) -> None:
        self._bits = [False] * 65  # index 1..64

    def set(self, bit: int) -> None:
        self._bits[bit] = True

    def is_set(self, bit: int) -> bool:
        return self._bits[bit]

    def to_hex(self) -> str:
        """16 uppercase hex characters, the ASCII variant of the 8 binary bytes."""
        out = []
        for b in range(8):
            value = 0
            for i in range(8):
                if self._bits[b * 8 + i + 1]:
                    value |= 1 << (7 - i)
            out.append(f"{value:02X}")
        return "".join(out)

    @staticmethod
    def from_hex(hex_text: str) -> "Bitmap":
        if len(hex_text) != 16:
            raise Iso8583Error(f"bitmap must be 16 hex characters, got {len(hex_text)}")
        bm = Bitmap()
        for b in range(8):
            try:
                value = int(hex_text[b * 2 : b * 2 + 2], 16)
            except ValueError:
                raise Iso8583Error(f"bitmap is not hexadecimal: {hex_text}") from None
            for i in range(8):
                if value & (1 << (7 - i)):
                    bm._bits[b * 8 + i + 1] = True
        return bm


@dataclass
class Iso8583Message:
    """One ISO 8583 message: a four-digit MTI and a sorted map of present data elements.
    Values are stored as strings in their on-the-wire form; interpretation (amount, currency)
    happens in typed accessors so the wire format and the domain type never get confused."""

    mti: str
    spec: Iso8583Spec
    _fields: dict[int, str] = field(default_factory=dict, init=False, repr=False)

    def __post_init__(self) -> None:
        if len(self.mti) != 4 or not self.mti.isdigit():
            raise Iso8583Error(f"MTI must be four digits: {self.mti!r}")

    def mti_version(self) -> int:
        """0 = 1987, 1 = 1993, 2 = 2003. This lesson only encodes version 0."""
        return int(self.mti[0])

    def mti_class(self) -> int:
        """1 = authorization, 2 = financial, 4 = reversal, 8 = network management."""
        return int(self.mti[1])

    def mti_function(self) -> int:
        """0 = request, 1 = request response, 2 = advice, 3 = advice response, 4 = notification."""
        return int(self.mti[2])

    def mti_origin(self) -> int:
        """0 = acquirer, 1 = acquirer repeat, 2 = issuer, 3 = issuer repeat."""
        return int(self.mti[3])

    def response_mti(self) -> str:
        """0100 -> 0110, 0200 -> 0210, 0420 -> 0430. Only requests and advices have a response."""
        function = self.mti_function()
        if function not in (0, 2):
            raise Iso8583Error(f"MTI {self.mti} is not a request or advice")
        return self.mti[:2] + str(function + 1) + self.mti[3]

    def set(self, number: int, value: str) -> "Iso8583Message":
        self.spec.require(number).validate(value)
        self._fields[number] = value
        return self

    def get(self, number: int) -> str:
        if number not in self._fields:
            raise Iso8583Error(f"DE{number} is not present in this message")
        return self._fields[number]

    def has(self, number: int) -> bool:
        return number in self._fields

    def fields(self) -> dict[int, str]:
        return dict(sorted(self._fields.items()))

    def amount(self) -> Money:
        """DE4 (12 digits, minor units) interpreted in the currency named by DE49. The only place money is typed."""
        return Money.of_minor(int(self.get(4)), currency_from_numeric(self.get(49)))

    def set_amount(self, money: Money) -> "Iso8583Message":
        if money.minor_units < 0:
            raise Iso8583Error("DE4 cannot carry a negative amount; use the MTI class instead")
        self.set(4, f"{money.minor_units:012d}")
        self.set(49, f"{money.currency.numeric_code:03d}")
        return self

    def masked_pan(self) -> str:
        """First six and last four digits of DE2; everything else replaced. Never log the raw PAN."""
        pan = self.get(2)
        if len(pan) <= 10:
            return "*" * len(pan)
        return pan[:6] + "*" * (len(pan) - 10) + pan[-4:]

    def encode(self) -> bytes:
        """The MTI, the bitmaps, then every present element in ascending order. ASCII variant."""
        primary, secondary = Bitmap(), Bitmap()
        need_secondary = False
        for n in self._fields:
            if n <= 64:
                primary.set(n)
            else:
                secondary.set(n - 64)
                need_secondary = True
        if need_secondary:
            primary.set(1)
        out = [self.mti, primary.to_hex()]
        if need_secondary:
            out.append(secondary.to_hex())
        for n, value in sorted(self._fields.items()):
            fs = self.spec.require(n)
            fs.validate(value)
            if fs.type is not FieldType.FIXED:
                out.append(str(len(value)).zfill(fs.type.prefix_digits))
            out.append(value)
        return "".join(out).encode("ascii")

    @staticmethod
    def decode(raw: bytes, spec: Iso8583Spec) -> "Iso8583Message":
        """Inverse of encode. Every malformed input is a specific error; nothing is guessed."""
        s = raw.decode("ascii", errors="strict")
        if len(s) < 4 + 16:
            raise Iso8583Error(f"message too short for MTI and primary bitmap: {len(s)} bytes")
        msg = Iso8583Message(s[:4], spec)
        pos = 4
        primary = Bitmap.from_hex(s[pos : pos + 16])
        pos += 16
        present = [False] * 129
        for bit in range(2, 65):
            present[bit] = primary.is_set(bit)
        if primary.is_set(1):
            if len(s) < pos + 16:
                raise Iso8583Error("bit 1 set but no secondary bitmap follows")
            secondary = Bitmap.from_hex(s[pos : pos + 16])
            pos += 16
            if secondary.is_set(1):
                raise Iso8583Error("tertiary bitmap (bit 65) is not supported")
            for bit in range(2, 65):
                present[64 + bit] = secondary.is_set(bit)
        for n in range(2, 129):
            if not present[n]:
                continue
            fs = spec.require(n)
            length = fs.length
            if fs.type is not FieldType.FIXED:
                digits = fs.type.prefix_digits
                if pos + digits > len(s):
                    raise Iso8583Error(f"truncated length prefix for DE{n}")
                prefix = s[pos : pos + digits]
                if not prefix.isdigit():
                    raise Iso8583Error(f"length prefix for DE{n} is not numeric: {prefix}")
                length = int(prefix)
                if length > fs.length:
                    raise Iso8583Error(f"DE{n} announces {length} chars, max is {fs.length}")
                pos += digits
            if pos + length > len(s):
                raise Iso8583Error(f"truncated: DE{n} needs {length} chars, {len(s) - pos} remain")
            msg.set(n, s[pos : pos + length])
            pos += length
        if pos != len(s):
            raise Iso8583Error(f"{len(s) - pos} trailing bytes after the last data element")
        return msg

    def dump(self) -> str:
        """Human-readable, PAN masked, one element per line. For logs and the demo."""
        lines = [f"MTI {self.mti}"]
        for n, value in sorted(self._fields.items()):
            shown = self.masked_pan() if n == 2 else value
            lines.append(f"  DE{n:<3d} {self.spec.require(n).name:<40} {shown}")
        return "\n".join(lines) + "\n"


TEST_PAN = "4111111111111111"  # published Visa test number, never a real card


def main() -> None:
    spec = Iso8583Spec.standard_1987()

    print("== Building a 0200 financial request ==")
    request = (
        Iso8583Message("0200", spec)
        .set(2, TEST_PAN)
        .set(3, "000000")
        .set_amount(Money.of_minor(1999, USD))
        .set(7, "1012143015")
        .set(11, "000123")
        .set(12, "143015")
        .set(13, "1012")
        .set(22, "051")
        .set(37, "123456789012")
        .set(41, "TERM0001")
        .set(42, "MERCHANT0000001")
    )
    print(request.dump(), end="")
    wire = request.encode()
    wire_text = wire.decode("ascii")
    print(f"encoded {len(wire)} bytes:")
    print(f"  {wire_text}")
    print(f"  MTI={wire_text[:4]} bitmap={wire_text[4:20]} DE2 prefix={wire_text[20:22]}")

    print("\n== Decoding it back ==")
    decoded = Iso8583Message.decode(wire, spec)
    print(f"amount = {decoded.amount()}, STAN = {decoded.get(11)}, RRN = {decoded.get(37)}")
    print(f"round trip equal: {wire == decoded.encode()}")

    print("\n== The issuer answers with a 0210 ==")
    response = Iso8583Message(request.response_mti(), spec)
    for n in (2, 3, 4, 7, 11, 12, 13, 37, 41, 42, 49):
        response.set(n, request.get(n))
    response.set(39, "00")
    response_text = response.encode().decode("ascii")
    print(f"MTI {response.mti} bitmap={response_text[4:20]} (bit 39 added, bit 22 dropped)")
    print(f"response code DE39 = {response.get(39)} -> approved")

    print("\n== A secondary bitmap appears when any DE above 64 is present ==")
    with_account = (
        Iso8583Message("0200", spec)
        .set(2, TEST_PAN).set(3, "000000").set_amount(Money.of_minor(500000, INR))
        .set(11, "000124").set(102, "12345678901")
    )
    secondary_text = with_account.encode().decode("ascii")
    print(f"primary   = {secondary_text[4:20]}  (first hex digit F: bit 1 set)")
    print(f"secondary = {secondary_text[20:36]}  (bit 38 = DE 102)")
    print(f"amount = {Iso8583Message.decode(with_account.encode(), spec).amount()}")

    print("\n== Malformed input is rejected, never guessed ==")
    bad = [
        wire_text[:60],                                            # truncated mid-field
        wire_text + "XYZ",                                         # trailing bytes
        "0200" + "ZZ38040008C08000" + wire_text[20:],              # bitmap not hex
        "0200" + "7238040008C08000" + "99" + wire_text[22:],       # LLVAR length over max
    ]
    for b in bad:
        try:
            Iso8583Message.decode(b.encode("ascii"), spec)
            print("accepted (unexpected)")
        except Iso8583Error as exc:
            print(f"rejected: {exc}")
    try:
        Iso8583Message("0200", spec).set(4, "19.99")
    except Iso8583Error as exc:
        print(f"rejected: {exc}")


if __name__ == "__main__":
    main()
