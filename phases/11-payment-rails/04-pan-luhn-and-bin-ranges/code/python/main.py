"""Lesson: phases/11-payment-rails/04-pan-luhn-and-bin-ranges/docs/en.md
PANs, Luhn, and BIN Range Routing
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: ISO/IEC 7812-1 (PAN structure, Luhn check digit, Annex B); PCI DSS v4.0 Req. 3.4.1 (masking);
         network public BIN prefix rules (Visa 4; Mastercard 51-55, 2221-2720; Amex 34, 37; Discover 6011, 65, 644-649).
Python 3.11+, standard library only. Only published test PANs appear anywhere in this lesson.
"""

from __future__ import annotations

from dataclasses import dataclass
from enum import Enum
from typing import List, Optional, Sequence, Union


class Luhn:
    """The Luhn (mod 10) check-digit algorithm from ISO/IEC 7812-1 Annex B. Works on any digit string."""

    @staticmethod
    def _require_digits(s: str) -> None:
        if not s:
            raise ValueError("digit string must not be empty")
        for i, c in enumerate(s):
            if c < "0" or c > "9":
                raise ValueError(f"not a digit at position {i}")

    @staticmethod
    def sum(digits: str, rightmost_is_check_digit: bool) -> int:
        """Luhn sum of the digits when the rightmost digit is at position 0.
        If rightmost_is_check_digit is True, doubling starts at position 1; otherwise at position 0."""
        total = 0
        dbl = not rightmost_is_check_digit
        for c in reversed(digits):
            d = ord(c) - 48
            if dbl:
                d *= 2
                if d > 9:
                    d -= 9
            total += d
            dbl = not dbl
        return total

    @staticmethod
    def is_valid(digits: str) -> bool:
        """True when the full number (including its check digit) satisfies sum mod 10 == 0."""
        Luhn._require_digits(digits)
        if len(digits) < 2:
            return False
        return Luhn.sum(digits, True) % 10 == 0

    @staticmethod
    def check_digit(partial: str) -> int:
        """The check digit that makes partial + digit Luhn-valid."""
        Luhn._require_digits(partial)
        return (10 - Luhn.sum(partial, False) % 10) % 10

    @staticmethod
    def complete(partial: str) -> str:
        """partial with its check digit appended."""
        return partial + str(Luhn.check_digit(partial))


class Network(Enum):
    """The networks this lesson routes to. Real routing tables carry many more (JCB, UnionPay, RuPay, domestic debit schemes)."""

    VISA = "visa"
    MASTERCARD = "mastercard"
    AMEX = "amex"
    DISCOVER = "discover"
    UNKNOWN = "unknown"


@dataclass(frozen=True)
class Pan:
    """A primary account number: 12 to 19 digits (ISO/IEC 7812-1) that pass the Luhn check. Never logged unmasked."""

    digits: str

    MIN_LENGTH = 12
    MAX_LENGTH = 19

    def __post_init__(self) -> None:
        if not isinstance(self.digits, str):
            raise TypeError("digits")
        if len(self.digits) < Pan.MIN_LENGTH or len(self.digits) > Pan.MAX_LENGTH:
            raise ValueError(f"PAN must be {Pan.MIN_LENGTH}-{Pan.MAX_LENGTH} digits, got {len(self.digits)}")
        if not Luhn.is_valid(self.digits):
            raise ValueError("PAN fails the Luhn check")

    @staticmethod
    def parse(text: str) -> "Pan":
        """Parses a PAN that may contain spaces or hyphens (as typed on a form). Rejects anything else."""
        out = []
        for c in text:
            if "0" <= c <= "9":
                out.append(c)
            elif c not in (" ", "-"):
                raise ValueError("PAN contains a non-digit character")
        return Pan("".join(out))

    def length(self) -> int:
        return len(self.digits)

    def major_industry_identifier(self) -> int:
        """The Major Industry Identifier: first digit. 3 = travel/entertainment, 4 and 5 = banking, 6 = merchandising/banking."""
        return ord(self.digits[0]) - 48

    def bin(self, length: int) -> str:
        """Issuer identification number of the given length (6 or 8)."""
        if length not in (6, 8):
            raise ValueError("BIN length must be 6 or 8")
        return self.digits[:length]

    def last_four(self) -> str:
        return self.digits[-4:]

    def masked(self, prefix: int = 6) -> str:
        """PCI DSS masking: the first prefix digits and the last four, everything else '*'. prefix is at most 8."""
        if prefix < 0 or prefix > 8:
            raise ValueError("may show at most 8 leading digits")
        if prefix + 4 >= len(self.digits):
            raise ValueError("mask would reveal the whole PAN")
        return self.digits[:prefix] + "*" * (len(self.digits) - 4 - prefix) + self.last_four()

    def __str__(self) -> str:
        """str() is the mask on purpose: a Pan that ends up in a log line is masked."""
        return self.masked()

    # f-strings, debuggers, and containers call repr(), which a dataclass would otherwise generate with the digits.
    __repr__ = __str__


@dataclass(frozen=True)
class BinRange:
    """One row of a BIN table: an inclusive range of account-number prefixes, normalized to WIDTH digits,
    and what it maps to. A 6-digit BIN 411111 is stored as [41111100, 41111199]; an 8-digit BIN 41111120 as a single point."""

    low: int
    high: int
    network: Network
    issuer: str
    product: str

    WIDTH = 8
    MIN_KEY = 0
    MAX_KEY = 99_999_999

    def __post_init__(self) -> None:
        if self.low < BinRange.MIN_KEY or self.high > BinRange.MAX_KEY or self.low > self.high:
            raise ValueError(f"bad range {self.low}-{self.high}")
        if not isinstance(self.network, Network) or self.issuer is None or self.product is None:
            raise TypeError("network, issuer and product are required")

    @staticmethod
    def of_prefix(prefix: str, network: Network, issuer: str, product: str) -> "BinRange":
        """Builds a range covering every account number that starts with prefix."""
        return BinRange.of_prefix_range(prefix, prefix, network, issuer, product)

    @staticmethod
    def of_prefix_range(from_prefix: str, to_prefix: str, network: Network, issuer: str, product: str) -> "BinRange":
        """Builds a range from from_prefix through to_prefix inclusive, both of the same length (e.g. "2221" to "2720")."""
        if len(from_prefix) != len(to_prefix):
            raise ValueError("prefix range bounds must have the same length")
        return BinRange(BinRange.normalize_low(from_prefix), BinRange.normalize_high(to_prefix), network, issuer, product)

    @staticmethod
    def normalize_low(prefix: str) -> int:
        return BinRange._pad(prefix, "0")

    @staticmethod
    def normalize_high(prefix: str) -> int:
        return BinRange._pad(prefix, "9")

    @staticmethod
    def _pad(prefix: str, fill: str) -> int:
        if not prefix or len(prefix) > BinRange.WIDTH:
            raise ValueError(f"prefix must be 1-{BinRange.WIDTH} digits")
        if not prefix.isdigit():
            raise ValueError(f"prefix must be digits: {prefix}")
        return int(prefix.ljust(BinRange.WIDTH, fill))

    @staticmethod
    def key_of(pan: Pan) -> int:
        """The lookup key of a PAN: its first WIDTH digits as a number."""
        return int(pan.digits[:BinRange.WIDTH])

    def contains(self, key: int) -> bool:
        return self.low <= key <= self.high

    def overlaps(self, other: "BinRange") -> bool:
        return self.low <= other.high and other.low <= self.high


class BinTable:
    """A sorted, non-overlapping set of BinRanges with O(log n) lookup by binary search."""

    def __init__(self, ranges: Sequence[BinRange]) -> None:
        ordered = sorted(ranges, key=lambda r: (r.low, r.high))
        for prev, cur in zip(ordered, ordered[1:]):
            if prev.overlaps(cur):
                raise ValueError(f"overlapping BIN ranges: {prev} and {cur}")
        self._ranges: tuple = tuple(ordered)

    def size(self) -> int:
        return len(self._ranges)

    def lookup(self, key_or_pan: Union[int, Pan]) -> Optional[BinRange]:
        """Binary search for the last range whose low <= key, then check key <= high. Accepts a key or a Pan."""
        key = BinRange.key_of(key_or_pan) if isinstance(key_or_pan, Pan) else key_or_pan
        ranges = self._ranges
        lo, hi, candidate = 0, len(ranges) - 1, -1
        while lo <= hi:
            mid = (lo + hi) >> 1
            if ranges[mid].low <= key:
                candidate = mid
                lo = mid + 1
            else:
                hi = mid - 1
        if candidate >= 0 and ranges[candidate].contains(key):
            return ranges[candidate]
        return None

    @staticmethod
    def network_ranges() -> "BinTable":
        """The public network prefix rules, as a BinTable. Real tables are licensed from the networks and updated monthly."""
        return BinTable([
            BinRange.of_prefix("4", Network.VISA, "network", "Visa"),
            BinRange.of_prefix_range("51", "55", Network.MASTERCARD, "network", "Mastercard 5-series"),
            BinRange.of_prefix_range("2221", "2720", Network.MASTERCARD, "network", "Mastercard 2-series"),
            BinRange.of_prefix("34", Network.AMEX, "network", "American Express"),
            BinRange.of_prefix("37", Network.AMEX, "network", "American Express"),
            BinRange.of_prefix("6011", Network.DISCOVER, "network", "Discover"),
            BinRange.of_prefix_range("644", "649", Network.DISCOVER, "network", "Discover"),
            BinRange.of_prefix("65", Network.DISCOVER, "network", "Discover"),
        ])

    @staticmethod
    def detect_network(pan: Pan) -> Network:
        """Network detection is a lookup in the network table."""
        hit = BinTable.network_ranges().lookup(pan)
        return hit.network if hit is not None else Network.UNKNOWN

    @staticmethod
    def length_allowed(network: Network, length: int) -> bool:
        """Lengths each network issues. ISO 7812 allows 12-19; the networks are narrower."""
        if network is Network.VISA:
            return length in (13, 16, 19)
        if network is Network.MASTERCARD:
            return length == 16
        if network is Network.AMEX:
            return length == 15
        if network is Network.DISCOVER:
            return length in (16, 19)
        return Pan.MIN_LENGTH <= length <= Pan.MAX_LENGTH


@dataclass(frozen=True)
class RoutingDecision:
    """What a card-processing front end does with a PAN before it touches an authorization: validate, detect, route, mask."""

    pan: Pan
    network: Network
    issuer_range: Optional[BinRange]
    length_ok: bool

    def describe(self) -> str:
        issuer = (f"  issuer={self.issuer_range.issuer} product={self.issuer_range.product}"
                  if self.issuer_range is not None else "  issuer=unknown BIN")
        return (f"{self.pan.masked()}  len={self.pan.length()}  {self.network.name}"
                f"{'' if self.length_ok else ' (length not issued by this network)'}{issuer}")


class CardRouter:
    def __init__(self, issuer_table: BinTable) -> None:
        self._issuer_table = issuer_table

    def route(self, raw_pan: str) -> RoutingDecision:
        pan = Pan.parse(raw_pan)
        network = BinTable.detect_network(pan)
        return RoutingDecision(pan, network, self._issuer_table.lookup(pan), BinTable.length_allowed(network, pan.length()))


def sample_issuer_table() -> BinTable:
    """A small issuer table made only of well-known test BINs. Issuer names are illustrative."""
    return BinTable([
        BinRange.of_prefix("411111", Network.VISA, "Test Bank A", "Visa Classic credit"),
        BinRange.of_prefix("424242", Network.VISA, "Test Bank B", "Visa debit"),
        BinRange.of_prefix("55555555", Network.MASTERCARD, "Test Bank C", "Mastercard World (8-digit BIN)"),
        BinRange.of_prefix("222300", Network.MASTERCARD, "Test Bank D", "Mastercard 2-series credit"),
        BinRange.of_prefix("378282", Network.AMEX, "Amex (test)", "Amex Green"),
        BinRange.of_prefix("601111", Network.DISCOVER, "Discover (test)", "Discover it"),
    ])


def _bool(b: bool) -> str:
    return str(b).lower()


def main() -> None:
    print("== Luhn by hand: 4111 1111 1111 111? ==")
    partial = "411111111111111"
    print(f"sum of weighted digits (no check digit) = {Luhn.sum(partial, False)}")
    print(f"check digit = (10 - {Luhn.sum(partial, False) % 10}) mod 10 = {Luhn.check_digit(partial)}")
    print(f"complete PAN = {Luhn.complete(partial)}  valid={_bool(Luhn.is_valid(Luhn.complete(partial)))}")
    print(f"single-digit typo 4111111111111112 valid={_bool(Luhn.is_valid('4111111111111112'))}")
    print(f"adjacent transposition 4242424242424242 -> 4422424242424242 valid={_bool(Luhn.is_valid('4422424242424242'))}")
    print(f"09 <-> 90 transposition is the one Luhn misses: 091 valid={_bool(Luhn.is_valid('091'))}, 901 valid={_bool(Luhn.is_valid('901'))}")
    print()

    print(f"== BIN table normalized to {BinRange.WIDTH} digits ==")
    issuers = sample_issuer_table()
    print(f"411111 (6-digit)   -> [{BinRange.normalize_low('411111')}, {BinRange.normalize_high('411111')}]")
    print(f"55555555 (8-digit) -> [{BinRange.normalize_low('55555555')}, {BinRange.normalize_high('55555555')}]")
    print(f"2221-2720 (network) -> [{BinRange.normalize_low('2221')}, {BinRange.normalize_high('2720')}]")
    print(f"{issuers.size()} issuer ranges, {BinTable.network_ranges().size()} network ranges, binary search on both")
    print()

    print("== Routing test PANs ==")
    router = CardRouter(issuers)
    for raw in ("4111 1111 1111 1111", "4242424242424242", "5555-5555-5555-4444", "2223003122003222",
                "378282246310005", "6011111111111117", "4000056655665556", "6304000000000000"):
        print(router.route(raw).describe())
    print()
    print("== Masking ==")
    amex = Pan.parse("378282246310005")
    print(f"default (6+4): {amex.masked()}   8-digit BIN visible: {amex.masked(8)}   last four only: {amex.masked(0)}")
    print(f"toString() is the mask, so a stray log line prints {amex}")
    print()
    for bad in ("4111111111111112", "1234", "4111 1111 1111 111a"):
        try:
            Pan.parse(bad)
            print(f"accepted {bad} (should not happen)")
        except ValueError as e:
            print(f"rejected: {e}")


if __name__ == "__main__":
    main()
