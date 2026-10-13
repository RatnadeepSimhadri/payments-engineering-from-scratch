"""Lesson: phases/11-payment-rails/08-ach-and-nacha/docs/en.md
ACH and the NACHA File Format
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: Nacha Operating Rules & Guidelines, Appendix Three (ACH record formats); ABA Routing Number
Policy (check-digit weights 3-7-1); Nacha Appendix Four (return reason codes).
Python 3.11+, standard library only. Same design as the Java and Go versions.
"""

from __future__ import annotations

from dataclasses import dataclass, replace
from datetime import date, datetime, timedelta, timezone
from enum import Enum
from typing import Callable, Optional


# ---------------------------------------------------------------------------
# Money (the subset of Phase 10, Lesson 01 that ACH needs)
# ---------------------------------------------------------------------------


@dataclass(frozen=True)
class Currency:
    """ISO 4217 currency. ACH moves USD only, but the type stays honest."""

    code: str
    exponent: int

    def __post_init__(self) -> None:
        if len(self.code) != 3 or not self.code.isalpha() or not self.code.isupper():
            raise ValueError(f"currency code must be three uppercase letters: {self.code!r}")
        if not 0 <= self.exponent <= 4:
            raise ValueError(f"exponent must be 0..4: {self.exponent}")

    def minor_units_per_major(self) -> int:
        return 10 ** self.exponent


USD = Currency("USD", 2)


@dataclass(frozen=True)
class Money:
    """An exact amount in minor units of one currency. Never constructed from a float."""

    minor_units: int
    currency: Currency

    def __post_init__(self) -> None:
        if not isinstance(self.minor_units, int) or isinstance(self.minor_units, bool):
            raise TypeError("minor_units must be an int; never construct Money from a float")

    @staticmethod
    def of_minor(minor_units: int, currency: Currency) -> "Money":
        return Money(minor_units, currency)

    @staticmethod
    def parse(text: str, currency: Currency) -> "Money":
        s = text.strip()
        negative = s.startswith("-")
        if negative or s.startswith("+"):
            s = s[1:]
        if not s:
            raise ValueError("empty amount")
        whole, frac = s, ""
        if "." in s:
            whole, frac = s.split(".", 1)
        if whole == "":
            whole = "0"
        if not whole.isdigit() or (frac and not frac.isdigit()):
            raise ValueError(f"not a decimal amount: {text!r}")
        if len(frac) > currency.exponent:
            raise ArithmeticError(f"{text} has more decimals than {currency.code} allows")
        frac = frac.ljust(currency.exponent, "0")
        minor = int(whole) * currency.minor_units_per_major() + (int(frac) if frac else 0)
        return Money(-minor if negative else minor, currency)

    def plus(self, other: "Money") -> "Money":
        if self.currency != other.currency:
            raise ValueError(f"cannot combine {self.currency.code} with {other.currency.code}")
        return Money(self.minor_units + other.minor_units, self.currency)

    def is_negative(self) -> bool:
        return self.minor_units < 0

    def is_zero(self) -> bool:
        return self.minor_units == 0

    def __str__(self) -> str:
        magnitude = abs(self.minor_units)
        scale = self.currency.minor_units_per_major()
        result = str(magnitude // scale)
        if self.currency.exponent > 0:
            result += "." + str(magnitude % scale).rjust(self.currency.exponent, "0")
        return ("-" if self.minor_units < 0 else "") + result + " " + self.currency.code


class NachaFormatError(ValueError):
    """A line, a field, or a control total does not satisfy the Nacha file format."""


# ---------------------------------------------------------------------------
# Routing numbers, SEC codes, transaction codes
# ---------------------------------------------------------------------------


class RoutingNumber:
    """ABA routing transit number: 8 digits of identity plus a mod-10 check digit weighted 3-7-1."""

    WEIGHTS = (3, 7, 1, 3, 7, 1, 3, 7)

    @staticmethod
    def check_digit(first8: str) -> int:
        """The ninth digit that makes the weighted sum of all nine digits a multiple of ten."""
        if len(first8) != 8 or not first8.isdigit():
            raise NachaFormatError(f"routing prefix must be 8 digits: {first8!r}")
        total = sum(w * int(d) for w, d in zip(RoutingNumber.WEIGHTS, first8))
        return (10 - total % 10) % 10

    @staticmethod
    def is_valid(routing: str) -> bool:
        if len(routing) != 9 or not routing.isdigit():
            return False
        return RoutingNumber.check_digit(routing[:8]) == int(routing[8])

    @staticmethod
    def require(routing: str) -> str:
        if not RoutingNumber.is_valid(routing):
            raise NachaFormatError(f"invalid routing number (check digit): {routing}")
        return routing


class SecCode(Enum):
    """Standard Entry Class: which rulebook chapter governs the batch and what the receiver authorized."""

    PPD = "PPD"  # Prearranged Payment and Deposit: consumer, written/standing authorization
    CCD = "CCD"  # Corporate Credit or Debit: business-to-business
    WEB = "WEB"  # Internet-Initiated: consumer debit authorized online


class TransactionCode(Enum):
    """Two-digit code: account type (2x checking, 3x savings) and direction (credit, debit, prenote, return)."""

    CHECKING_RETURN_CREDIT = 21
    CHECKING_CREDIT = 22
    CHECKING_PRENOTE_CREDIT = 23
    CHECKING_RETURN_DEBIT = 26
    CHECKING_DEBIT = 27
    CHECKING_PRENOTE_DEBIT = 28
    SAVINGS_RETURN_CREDIT = 31
    SAVINGS_CREDIT = 32
    SAVINGS_PRENOTE_CREDIT = 33
    SAVINGS_RETURN_DEBIT = 36
    SAVINGS_DEBIT = 37
    SAVINGS_PRENOTE_DEBIT = 38

    @property
    def code(self) -> int:
        return self.value

    def is_credit(self) -> bool:
        return 1 <= self.value % 10 <= 4

    def is_debit(self) -> bool:
        return 6 <= self.value % 10 <= 9

    def is_prenote(self) -> bool:
        return self.value % 10 in (3, 8)

    def is_return(self) -> bool:
        return self.value % 10 in (1, 6)

    @staticmethod
    def from_code(code: int) -> "TransactionCode":
        for t in TransactionCode:
            if t.value == code:
                return t
        raise NachaFormatError(f"unknown transaction code: {code}")


# ---------------------------------------------------------------------------
# Fixed-width helpers. Nacha positions are 1-based and inclusive; so are these.
# ---------------------------------------------------------------------------


class Fixed:
    @staticmethod
    def alpha(value: Optional[str], width: int) -> str:
        """Alphameric: left-justify, space-fill, truncate, uppercase."""
        s = (value or "").upper()[:width]
        return s.ljust(width)

    @staticmethod
    def numeric(value: int, width: int) -> str:
        """Numeric: right-justify, zero-fill; a value that does not fit is an error, never truncated."""
        if value < 0:
            raise NachaFormatError(f"numeric field cannot be negative: {value}")
        s = str(value)
        if len(s) > width:
            raise NachaFormatError(f"value {value} does not fit in {width} digits")
        return s.rjust(width, "0")

    @staticmethod
    def digits(value: str, width: int) -> str:
        """Numeric given as a digit string (account numbers keep leading zeros)."""
        if not value or not value.isdigit():
            raise NachaFormatError(f"expected digits: {value!r}")
        if len(value) > width:
            raise NachaFormatError(f"{value!r} does not fit in {width} digits")
        return value.rjust(width, "0")

    @staticmethod
    def field(line: str, start: int, end: int) -> str:
        """Characters start..end inclusive, 1-based, exactly as the Nacha layout tables number them."""
        return line[start - 1 : end]

    @staticmethod
    def number(line: str, start: int, end: int) -> int:
        s = Fixed.field(line, start, end)
        if not s.isdigit():
            raise NachaFormatError(f"expected digits at {start}-{end}: {s!r}")
        return int(s)

    @staticmethod
    def require_match(what: str, declared: object, computed: object) -> None:
        if declared != computed:
            raise NachaFormatError(f"{what} mismatch: declared {declared}, computed {computed}")

    @staticmethod
    def require94(line: str) -> str:
        if len(line) != 94:
            raise NachaFormatError(f"record must be 94 characters, got {len(line)}: {line!r}")
        return line


# ---------------------------------------------------------------------------
# Records
# ---------------------------------------------------------------------------


@dataclass(frozen=True)
class FileHeader:
    """Record type 1. One per file. Who is sending, who is receiving, when it was created."""

    immediate_destination: str
    immediate_origin: str
    created: datetime
    file_id_modifier: str
    destination_name: str
    origin_name: str
    reference_code: str

    def __post_init__(self) -> None:
        RoutingNumber.require(self.immediate_destination)
        if len(self.immediate_origin) not in (9, 10):
            raise NachaFormatError("immediate origin must be 9 or 10 characters")
        m = self.file_id_modifier
        if len(m) != 1 or not m.isalnum() or m.islower():
            raise NachaFormatError("file ID modifier must be A-Z or 0-9")

    @staticmethod
    def of(destination: str, origin: str, clock: Callable[[], datetime], destination_name: str, origin_name: str) -> "FileHeader":
        return FileHeader(destination, origin, clock(), "A", destination_name, origin_name, "")

    def to_line(self) -> str:
        dest = self.immediate_destination
        orig = self.immediate_origin
        line = (
            "1" + "01"
            + Fixed.alpha(" " + dest if len(dest) == 9 else dest, 10)
            + Fixed.alpha(" " + orig if len(orig) == 9 else orig, 10)
            + self.created.strftime("%y%m%d") + self.created.strftime("%H%M")
            + self.file_id_modifier + "094" + "10" + "1"
            + Fixed.alpha(self.destination_name, 23) + Fixed.alpha(self.origin_name, 23) + Fixed.alpha(self.reference_code, 8)
        )
        return Fixed.require94(line)

    @staticmethod
    def parse(line: str) -> "FileHeader":
        Fixed.require94(line)
        if line[0] != "1":
            raise NachaFormatError(f"not a file header: {line!r}")
        if Fixed.field(line, 35, 37) != "094" or Fixed.field(line, 38, 39) != "10" or line[39] != "1":
            raise NachaFormatError("file header must declare record size 094, blocking factor 10, format code 1")
        created = datetime.strptime(Fixed.field(line, 24, 29) + Fixed.field(line, 30, 33), "%y%m%d%H%M")
        return FileHeader(
            Fixed.field(line, 4, 13).strip(), Fixed.field(line, 14, 23).strip(), created, line[33],
            Fixed.field(line, 41, 63).strip(), Fixed.field(line, 64, 86).strip(), Fixed.field(line, 87, 94).strip(),
        )


MIXED, CREDITS_ONLY, DEBITS_ONLY = 200, 220, 225


@dataclass(frozen=True)
class BatchHeader:
    """Record type 5. One per batch. Who the company is, what SEC code governs, when entries should settle."""

    service_class_code: int
    company_name: str
    company_discretionary_data: str
    company_id: str
    sec_code: SecCode
    entry_description: str
    descriptive_date: str
    effective_entry_date: str
    odfi_id: str
    batch_number: int

    def __post_init__(self) -> None:
        if self.service_class_code not in (MIXED, CREDITS_ONLY, DEBITS_ONLY):
            raise NachaFormatError(f"service class code must be 200, 220, or 225: {self.service_class_code}")
        if len(self.odfi_id) != 8 or not self.odfi_id.isdigit():
            raise NachaFormatError(f"ODFI id must be 8 digits: {self.odfi_id!r}")
        if len(self.effective_entry_date) != 6:
            raise NachaFormatError("effective entry date must be yyMMdd")
        if self.batch_number < 1:
            raise NachaFormatError("batch number starts at 1")

    def to_line(self) -> str:
        line = (
            "5" + Fixed.numeric(self.service_class_code, 3) + Fixed.alpha(self.company_name, 16)
            + Fixed.alpha(self.company_discretionary_data, 20) + Fixed.alpha(self.company_id, 10) + self.sec_code.value
            + Fixed.alpha(self.entry_description, 10) + Fixed.alpha(self.descriptive_date, 6) + self.effective_entry_date
            + "   "  # settlement date: filled by the ACH Operator
            + "1"    # originator status code
            + self.odfi_id + Fixed.numeric(self.batch_number, 7)
        )
        return Fixed.require94(line)

    @staticmethod
    def parse(line: str) -> "BatchHeader":
        Fixed.require94(line)
        if line[0] != "5":
            raise NachaFormatError(f"not a batch header: {line!r}")
        try:
            sec = SecCode(Fixed.field(line, 51, 53))
        except ValueError as exc:
            raise NachaFormatError(f"unknown SEC code: {Fixed.field(line, 51, 53)!r}") from exc
        return BatchHeader(
            Fixed.number(line, 2, 4), Fixed.field(line, 5, 20).strip(), Fixed.field(line, 21, 40).strip(),
            Fixed.field(line, 41, 50).strip(), sec, Fixed.field(line, 54, 63).strip(), Fixed.field(line, 64, 69).strip(),
            Fixed.field(line, 70, 75), Fixed.field(line, 80, 87), Fixed.number(line, 88, 94),
        )


@dataclass(frozen=True)
class ReturnReason:
    """Nacha Appendix Four return reason, the subset a payments team sees most.

    extended_window: sixty calendar days (unauthorized consumer debit) instead of two banking days.
    representable: the Originator may re-present the entry, at most twice (R01 and R09 only).
    """

    code: str
    description: str
    extended_window: bool
    representable: bool

    def window_days(self) -> int:
        return 60 if self.extended_window else 2

    def deadline(self, settlement_date: date) -> date:
        """Last date the RDFI may transmit the return. Banking days skip Sat/Sun; Fed holidays are out of scope."""
        if self.extended_window:
            return settlement_date + timedelta(days=60)
        d, remaining = settlement_date, 2
        while remaining > 0:
            d += timedelta(days=1)
            if d.weekday() < 5:
                remaining -= 1
        return d

    def is_timely(self, settlement_date: date, return_date: date) -> bool:
        return return_date <= self.deadline(settlement_date)

    @staticmethod
    def lookup(code: str) -> "ReturnReason":
        for r in RETURN_REASONS:
            if r.code == code:
                return r
        raise NachaFormatError(f"unknown return reason code: {code!r}")

    @staticmethod
    def is_known(code: str) -> bool:
        return any(r.code == code for r in RETURN_REASONS)


RETURN_REASONS = (
    ReturnReason("R01", "Insufficient funds", False, True),
    ReturnReason("R02", "Account closed", False, False),
    ReturnReason("R03", "No account / unable to locate account", False, False),
    ReturnReason("R04", "Invalid account number structure", False, False),
    ReturnReason("R05", "Unauthorized debit to consumer account using corporate SEC code", True, False),
    ReturnReason("R07", "Authorization revoked by customer", True, False),
    ReturnReason("R08", "Payment stopped", False, False),
    ReturnReason("R09", "Uncollected funds", False, True),
    ReturnReason("R10", "Customer advises originator is not known to receiver and/or not authorized", True, False),
    ReturnReason("R11", "Customer advises entry not in accordance with the terms of the authorization", True, False),
    ReturnReason("R16", "Account frozen", False, False),
    ReturnReason("R20", "Non-transaction account", False, False),
    ReturnReason("R29", "Corporate customer advises not authorized", False, False),
)


@dataclass(frozen=True)
class Addenda:
    """Record type 7: payment-related addenda (type 05, free text) or return addenda (type 99, reason + original trace)."""

    type_code: str
    reason_code: str
    original_trace: str
    original_dfi: str
    info: str

    def __post_init__(self) -> None:
        if self.type_code not in ("05", "99"):
            raise NachaFormatError(f"addenda type must be 05 or 99: {self.type_code!r}")
        if self.type_code == "99":
            if len(self.reason_code) != 3:
                raise NachaFormatError(f"return reason code must be 3 characters: {self.reason_code!r}")
            if len(self.original_trace) != 15:
                raise NachaFormatError("original trace must be 15 digits")
            if len(self.original_dfi) != 8:
                raise NachaFormatError("original DFI must be 8 digits")

    @staticmethod
    def payment(info: str) -> "Addenda":
        return Addenda("05", "", "", "", info)

    @staticmethod
    def returned(reason_code: str, original_trace: str, original_dfi: str, info: str) -> "Addenda":
        return Addenda("99", reason_code, original_trace, original_dfi, info)

    def is_return(self) -> bool:
        return self.type_code == "99"

    def reason(self) -> ReturnReason:
        """The Appendix Four entry for this return; only meaningful on a type-99 addenda."""
        if not self.is_return():
            raise NachaFormatError("not a return addenda")
        return ReturnReason.lookup(self.reason_code)

    def to_line(self, entry_sequence: int, trace_number: str) -> str:
        if self.type_code == "05":
            line = "7" + "05" + Fixed.alpha(self.info, 80) + Fixed.numeric(1, 4) + Fixed.numeric(entry_sequence, 7)
        else:
            line = (
                "7" + "99" + Fixed.alpha(self.reason_code, 3) + self.original_trace
                + Fixed.alpha("", 6)  # date of death
                + self.original_dfi + Fixed.alpha(self.info, 44) + trace_number
            )
        return Fixed.require94(line)

    @staticmethod
    def parse(line: str) -> "Addenda":
        Fixed.require94(line)
        if line[0] != "7":
            raise NachaFormatError(f"not an addenda record: {line!r}")
        if Fixed.field(line, 2, 3) == "05":
            return Addenda.payment(Fixed.field(line, 4, 83).strip())
        return Addenda.returned(Fixed.field(line, 4, 6), Fixed.field(line, 7, 21), Fixed.field(line, 28, 35), Fixed.field(line, 36, 79).strip())


MAX_AMOUNT_CENTS = 9_999_999_999


@dataclass(frozen=True)
class EntryDetail:
    """Record type 6. One per payment. The receiver's bank, account, amount, and a trace number the ODFI assigns."""

    transaction_code: TransactionCode
    receiving_dfi: str
    account_number: str
    amount: Money
    individual_id: str
    individual_name: str
    discretionary_data: str = ""
    addenda: Optional[Addenda] = None
    trace_number: str = ""

    def __post_init__(self) -> None:
        RoutingNumber.require(self.receiving_dfi)
        if not self.account_number.strip() or len(self.account_number) > 17:
            raise NachaFormatError("account number must be 1-17 characters")
        if self.amount.currency != USD:
            raise NachaFormatError("ACH carries USD only")
        if self.amount.is_negative():
            raise NachaFormatError("amount must not be negative; direction is the transaction code")
        if self.amount.minor_units > MAX_AMOUNT_CENTS:
            raise NachaFormatError("amount exceeds 10 digits of cents")
        if self.transaction_code.is_prenote() and not self.amount.is_zero():
            raise NachaFormatError("prenote entries must carry a zero amount")
        if self.trace_number and len(self.trace_number) != 15:
            raise NachaFormatError("trace number must be 15 digits")

    @staticmethod
    def of(code: TransactionCode, receiving_dfi: str, account: str, amount: Money, individual_id: str, name: str) -> "EntryDetail":
        return EntryDetail(code, receiving_dfi, account, amount, individual_id, name)

    def with_addenda(self, addenda: Addenda) -> "EntryDetail":
        return replace(self, addenda=addenda)

    def with_trace(self, odfi_id: str, sequence: int) -> "EntryDetail":
        return replace(self, trace_number=odfi_id + Fixed.numeric(sequence, 7))

    def receiving_dfi_id(self) -> str:
        """The first eight digits: what the entry hash sums. The check digit is excluded on purpose."""
        return self.receiving_dfi[:8]

    def record_count(self) -> int:
        return 1 if self.addenda is None else 2

    def to_line(self) -> str:
        line = (
            "6" + Fixed.numeric(self.transaction_code.code, 2) + self.receiving_dfi + Fixed.alpha(self.account_number, 17)
            + Fixed.numeric(self.amount.minor_units, 10) + Fixed.alpha(self.individual_id, 15) + Fixed.alpha(self.individual_name, 22)
            + Fixed.alpha(self.discretionary_data, 2) + ("0" if self.addenda is None else "1") + self.trace_number
        )
        return Fixed.require94(line)

    @staticmethod
    def parse(line: str) -> "EntryDetail":
        Fixed.require94(line)
        if line[0] != "6":
            raise NachaFormatError(f"not an entry detail: {line!r}")
        has_addenda = line[78] == "1"
        entry = EntryDetail(
            TransactionCode.from_code(Fixed.number(line, 2, 3)), Fixed.field(line, 4, 12), Fixed.field(line, 13, 29).strip(),
            Money.of_minor(Fixed.number(line, 30, 39), USD), Fixed.field(line, 40, 54).strip(), Fixed.field(line, 55, 76).strip(),
            Fixed.field(line, 77, 78).strip(), None, Fixed.field(line, 80, 94),
        )
        return entry.with_addenda(Addenda.payment("")) if has_addenda else entry  # placeholder until the 7 record arrives


@dataclass(frozen=True)
class BatchControl:
    """Record type 8: the totals the receiver recomputes. Equality of parsed vs computed is the integrity check."""

    service_class_code: int
    entry_addenda_count: int
    entry_hash: int
    total_debit: Money
    total_credit: Money
    company_id: str
    odfi_id: str
    batch_number: int

    def verify_against(self, computed: "BatchControl") -> None:
        b = f"batch {self.batch_number} "
        Fixed.require_match(b + "entry/addenda count", self.entry_addenda_count, computed.entry_addenda_count)
        Fixed.require_match(b + "entry hash", self.entry_hash, computed.entry_hash)
        Fixed.require_match(b + "total debit", self.total_debit, computed.total_debit)
        Fixed.require_match(b + "total credit", self.total_credit, computed.total_credit)
        Fixed.require_match(b + "control record", self, computed)

    def to_line(self) -> str:
        line = (
            "8" + Fixed.numeric(self.service_class_code, 3) + Fixed.numeric(self.entry_addenda_count, 6) + Fixed.numeric(self.entry_hash, 10)
            + Fixed.numeric(self.total_debit.minor_units, 12) + Fixed.numeric(self.total_credit.minor_units, 12) + Fixed.alpha(self.company_id, 10)
            + Fixed.alpha("", 19)  # message authentication code
            + Fixed.alpha("", 6) + self.odfi_id + Fixed.numeric(self.batch_number, 7)
        )
        return Fixed.require94(line)

    @staticmethod
    def parse(line: str) -> "BatchControl":
        Fixed.require94(line)
        if line[0] != "8":
            raise NachaFormatError(f"not a batch control: {line!r}")
        return BatchControl(
            Fixed.number(line, 2, 4), Fixed.number(line, 5, 10), Fixed.number(line, 11, 20),
            Money.of_minor(Fixed.number(line, 21, 32), USD), Money.of_minor(Fixed.number(line, 33, 44), USD),
            Fixed.field(line, 45, 54).strip(), Fixed.field(line, 80, 87), Fixed.number(line, 88, 94),
        )


@dataclass(frozen=True)
class FileControl:
    """Record type 9: file-level totals plus the block count that explains the 9-filler lines."""

    batch_count: int
    block_count: int
    entry_addenda_count: int
    entry_hash: int
    total_debit: Money
    total_credit: Money

    def verify_against(self, computed: "FileControl") -> None:
        Fixed.require_match("file batch count", self.batch_count, computed.batch_count)
        Fixed.require_match("file block count", self.block_count, computed.block_count)
        Fixed.require_match("file entry/addenda count", self.entry_addenda_count, computed.entry_addenda_count)
        Fixed.require_match("file entry hash", self.entry_hash, computed.entry_hash)
        Fixed.require_match("file total debit", self.total_debit, computed.total_debit)
        Fixed.require_match("file total credit", self.total_credit, computed.total_credit)

    def to_line(self) -> str:
        line = (
            "9" + Fixed.numeric(self.batch_count, 6) + Fixed.numeric(self.block_count, 6) + Fixed.numeric(self.entry_addenda_count, 8)
            + Fixed.numeric(self.entry_hash, 10) + Fixed.numeric(self.total_debit.minor_units, 12)
            + Fixed.numeric(self.total_credit.minor_units, 12) + Fixed.alpha("", 39)
        )
        return Fixed.require94(line)

    @staticmethod
    def parse(line: str) -> "FileControl":
        Fixed.require94(line)
        if line[0] != "9":
            raise NachaFormatError(f"not a file control: {line!r}")
        return FileControl(
            Fixed.number(line, 2, 7), Fixed.number(line, 8, 13), Fixed.number(line, 14, 21), Fixed.number(line, 22, 31),
            Money.of_minor(Fixed.number(line, 32, 43), USD), Money.of_minor(Fixed.number(line, 44, 55), USD),
        )


HASH_MODULUS = 10_000_000_000


def entry_hash(receiving_dfi_ids: list[str]) -> int:
    """Sum of the receiving DFI identifiers (8 digits each), keeping only the rightmost 10 digits."""
    return sum(int(i) for i in receiving_dfi_ids) % HASH_MODULUS


@dataclass(frozen=True)
class Batch:
    """A batch header, its entries (trace numbers assigned here), and the control record computed from them."""

    header: BatchHeader
    entries: tuple[EntryDetail, ...]

    def __post_init__(self) -> None:
        if not self.entries:
            raise NachaFormatError("a batch needs at least one entry")
        traced = tuple(
            e if e.trace_number else e.with_trace(self.header.odfi_id, i + 1) for i, e in enumerate(self.entries)
        )
        for e in traced:
            if self.header.service_class_code == CREDITS_ONLY and e.transaction_code.is_debit():
                raise NachaFormatError("debit entry in a 220 credits-only batch")
            if self.header.service_class_code == DEBITS_ONLY and e.transaction_code.is_credit():
                raise NachaFormatError("credit entry in a 225 debits-only batch")
        object.__setattr__(self, "entries", traced)

    def entry_hash(self) -> int:
        return entry_hash([e.receiving_dfi_id() for e in self.entries])

    def entry_addenda_count(self) -> int:
        return sum(e.record_count() for e in self.entries)

    def total_debit(self) -> Money:
        total = Money.of_minor(0, USD)
        for e in self.entries:
            if e.transaction_code.is_debit():
                total = total.plus(e.amount)
        return total

    def total_credit(self) -> Money:
        total = Money.of_minor(0, USD)
        for e in self.entries:
            if e.transaction_code.is_credit():
                total = total.plus(e.amount)
        return total

    def control(self) -> BatchControl:
        return BatchControl(
            self.header.service_class_code, self.entry_addenda_count(), self.entry_hash(), self.total_debit(), self.total_credit(),
            self.header.company_id, self.header.odfi_id, self.header.batch_number,
        )

    def to_lines(self) -> list[str]:
        lines = [self.header.to_line()]
        for i, e in enumerate(self.entries):
            lines.append(e.to_line())
            if e.addenda is not None:
                lines.append(e.addenda.to_line(i + 1, e.trace_number))
        lines.append(self.control().to_line())
        return lines


BLOCKING_FACTOR = 10
FILLER = "9" * 94


@dataclass(frozen=True)
class AchFile:
    """The whole file: header, batches, control, and 9-filler to a multiple of ten records."""

    header: FileHeader
    batches: tuple[Batch, ...]

    def __post_init__(self) -> None:
        if not self.batches:
            raise NachaFormatError("a file needs at least one batch")
        object.__setattr__(self, "batches", tuple(self.batches))

    def record_count(self) -> int:
        return 2 + sum(2 + b.entry_addenda_count() for b in self.batches)

    def block_count(self) -> int:
        return (self.record_count() + BLOCKING_FACTOR - 1) // BLOCKING_FACTOR

    def control(self) -> FileControl:
        debit, credit = Money.of_minor(0, USD), Money.of_minor(0, USD)
        hash_total, count = 0, 0
        for b in self.batches:
            debit = debit.plus(b.total_debit())
            credit = credit.plus(b.total_credit())
            hash_total = (hash_total + b.entry_hash()) % HASH_MODULUS
            count += b.entry_addenda_count()
        return FileControl(len(self.batches), self.block_count(), count, hash_total, debit, credit)

    def to_lines(self) -> list[str]:
        lines = [self.header.to_line()]
        for b in self.batches:
            lines.extend(b.to_lines())
        lines.append(self.control().to_line())
        while len(lines) % BLOCKING_FACTOR != 0:
            lines.append(FILLER)
        return lines

    def write(self) -> str:
        return "\n".join(self.to_lines()) + "\n"

    @staticmethod
    def parse(text: str) -> "AchFile":
        """Parse and verify: every line 94 characters, every routing number valid, every control total recomputed and matched."""
        file_header: Optional[FileHeader] = None
        batches: list[Batch] = []
        batch_header: Optional[BatchHeader] = None
        entries: Optional[list[EntryDetail]] = None
        file_control: Optional[FileControl] = None
        for raw in text.replace("\r\n", "\n").split("\n"):
            if raw == "":
                continue
            line = Fixed.require94(raw)
            if line == FILLER:
                continue
            kind = line[0]
            if kind == "1":
                if file_header is not None:
                    raise NachaFormatError("duplicate file header")
                file_header = FileHeader.parse(line)
            elif kind == "5":
                if file_header is None:
                    raise NachaFormatError("batch header before file header")
                if batch_header is not None:
                    raise NachaFormatError("batch header before previous batch control")
                batch_header = BatchHeader.parse(line)
                entries = []
            elif kind == "6":
                if batch_header is None or entries is None:
                    raise NachaFormatError("entry outside a batch")
                entries.append(EntryDetail.parse(line))
            elif kind == "7":
                if not entries:
                    raise NachaFormatError("addenda without an entry")
                last = entries[-1]
                if last.addenda is None:
                    raise NachaFormatError("addenda follows an entry whose addenda indicator is 0")
                entries[-1] = last.with_addenda(Addenda.parse(line))
            elif kind == "8":
                if batch_header is None or entries is None:
                    raise NachaFormatError("batch control without a batch header")
                batch = Batch(batch_header, tuple(entries))
                BatchControl.parse(line).verify_against(batch.control())
                batches.append(batch)
                batch_header, entries = None, None
            elif kind == "9":
                if file_control is not None:
                    raise NachaFormatError("duplicate file control")
                file_control = FileControl.parse(line)
            else:
                raise NachaFormatError(f"unknown record type {kind!r}")
        if file_header is None or file_control is None:
            raise NachaFormatError("file header and file control are both required")
        if batch_header is not None:
            raise NachaFormatError("file ended inside a batch")
        ach = AchFile(file_header, tuple(batches))
        file_control.verify_against(ach.control())
        return ach


# ---------------------------------------------------------------------------
# Worked example
# ---------------------------------------------------------------------------

ODFI = "021999996"  # the originating bank (fictitious, passes the ABA checksum); its first 8 digits prefix every trace number


def fixed_clock() -> datetime:
    return datetime(2026, 10, 11, 9, 30, tzinfo=timezone.utc).replace(tzinfo=None)


def payroll_file() -> AchFile:
    header = FileHeader.of(ODFI, "1234567890", fixed_clock, "FIRST FICTIONAL BANK", "ACME PAYROLL INC")
    batch_header = BatchHeader(CREDITS_ONLY, "ACME PAYROLL", "", "1234567890", SecCode.PPD, "PAYROLL", "OCT 15", "261013", ODFI[:8], 1)
    entries = (
        EntryDetail.of(TransactionCode.CHECKING_CREDIT, "011999993", "000123456789", Money.parse("2512.37", USD), "EMP0001", "ADA LOVELACE"),
        EntryDetail.of(TransactionCode.SAVINGS_CREDIT, "061999998", "9876543210", Money.parse("1800.00", USD), "EMP0002", "GRACE HOPPER"),
        EntryDetail.of(TransactionCode.CHECKING_CREDIT, "121999993", "55501234", Money.parse("3075.50", USD), "EMP0003", "EDSGER DIJKSTRA")
        .with_addenda(Addenda.payment("BONUS INCLUDED Q3")),
    )
    return AchFile(header, (Batch(batch_header, entries),))


def main() -> None:
    print("== Routing number check digit (ABA mod-10, weights 3-7-1) ==")
    print(f"02199999 -> check digit {RoutingNumber.check_digit('02199999')}  (021999996 valid: {RoutingNumber.is_valid('021999996')})")
    print(f"021999969 valid? {RoutingNumber.is_valid('021999969')}  (two digits transposed)")

    print("\n== A payroll file: 1 batch, 3 PPD credits, 1 addenda ==")
    ach = payroll_file()
    text = ach.write()
    print(text, end="")
    batch = ach.batches[0]
    print(f"entry hash   = {batch.entry_hash()}  (01199999 + 06199999 + 12199999)")
    print(f"total credit = {batch.total_credit()}, total debit = {batch.total_debit()}")
    print(f"records = {ach.record_count()}, blocks = {ach.block_count()}, lines written = {len(ach.to_lines())}")

    print("\n== Round trip ==")
    parsed = AchFile.parse(text)
    print(f"parse(write(file)) == file ? {parsed == ach}")

    print("\n== A return: R01 Insufficient Funds on a WEB debit ==")
    original = EntryDetail.of(TransactionCode.CHECKING_DEBIT, "011999993", "000123456789", Money.parse("49.99", USD), "ORD-7781", "ADA LOVELACE").with_trace(ODFI[:8], 1)
    returned = EntryDetail.of(TransactionCode.CHECKING_RETURN_DEBIT, "011999993", "000123456789", original.amount, "ORD-7781", "ADA LOVELACE").with_addenda(
        Addenda.returned("R01", original.trace_number, ODFI[:8], "")
    )
    return_header = BatchHeader(DEBITS_ONLY, "ACME STORE", "", "1234567890", SecCode.WEB, "RETURN", "", "261015", "01199999", 1)
    for line in Batch(return_header, (returned,)).to_lines():
        print(line)
    assert returned.addenda is not None
    reason = returned.addenda.reason()
    settled = date(2026, 10, 15)  # a Thursday
    kind = "calendar" if reason.extended_window else "banking"
    print(f"{reason.code} {reason.description}: window {reason.window_days()} {kind} days, deadline {reason.deadline(settled)}, re-presentable {reason.representable}")
    r10 = ReturnReason.lookup("R10")
    print(f"{r10.code} {r10.description}: deadline {r10.deadline(settled)}, re-presentable {r10.representable}")

    print("\n== Rejections ==")
    try:
        EntryDetail.of(TransactionCode.CHECKING_CREDIT, "021999969", "1", Money.parse("1.00", USD), "", "X")
    except NachaFormatError as exc:
        print(f"rejected: {exc}")
    try:
        ctl = batch.control().to_line()  # bump the last digit of the credit total (position 44)
        bumped = "0" if ctl[43] == "9" else chr(ord(ctl[43]) + 1)
        AchFile.parse(text.replace(ctl, ctl[:43] + bumped + ctl[44:]))
    except NachaFormatError as exc:
        print(f"rejected: {exc}")


if __name__ == "__main__":
    main()
