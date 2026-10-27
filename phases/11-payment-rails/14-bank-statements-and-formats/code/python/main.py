"""Lesson: phases/11-payment-rails/14-bank-statements-and-formats/docs/en.md
Bank Statement Formats: MT940, CAMT.053, BAI2, OFX parsed into one normalized model with balance continuity checks.
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: SWIFT MT940 (Category 9 user handbook); ISO 20022 camt.053.001.08; BAI2 Cash Management Balance Reporting
Specifications (X9.121 lineage); OFX Specification 1.0.2 / 2.x. Python 3.11+, standard library only. Samples are fictional.
"""

from __future__ import annotations

import re
import xml.etree.ElementTree as ET
from dataclasses import dataclass
from datetime import date
from enum import Enum
from functools import total_ordering
from textwrap import dedent
from typing import Optional


# ---------------------------------------------------------------------------------------------------------------
# Money, Currency, Rounding: copied from phases/10-money-and-ledgers/01-representing-money/code/python
# (lessons are self-contained; there is no shared library). Integer minor units, never floats.
# One addition for this lesson: Currency.of(code), because statements name their currency as a string.
# ---------------------------------------------------------------------------------------------------------------


@dataclass(frozen=True)
class Currency:
    code: str
    exponent: int

    def __post_init__(self) -> None:
        if len(self.code) != 3 or not self.code.isalpha() or not self.code.isupper():
            raise ValueError(f"currency code must be three uppercase letters: {self.code!r}")
        if not 0 <= self.exponent <= 4:
            raise ValueError(f"exponent must be 0..4: {self.exponent}")

    def minor_units_per_major(self) -> int:
        return 10 ** self.exponent

    @staticmethod
    def of(code: str) -> "Currency":
        """Look up a known ISO 4217 code; statements carry the code as text. Unknown codes are an error, not a guess."""
        known = {"USD": USD, "EUR": EUR, "GBP": GBP, "INR": INR, "JPY": JPY, "KWD": KWD}
        if code not in known:
            raise ValueError(f"unknown currency {code}")
        return known[code]


USD = Currency("USD", 2)
EUR = Currency("EUR", 2)
GBP = Currency("GBP", 2)
INR = Currency("INR", 2)
JPY = Currency("JPY", 0)
KWD = Currency("KWD", 3)


class Rounding(Enum):
    HALF_EVEN = "half_even"
    HALF_UP = "half_up"
    DOWN = "down"
    UP = "up"


class CurrencyMismatchError(ValueError):
    def __init__(self, a: Currency, b: Currency) -> None:
        super().__init__(f"cannot combine {a.code} with {b.code}")


def divide_rounded(dividend: int, divisor: int, rounding: Rounding) -> int:
    if divisor == 0:
        raise ZeroDivisionError("divisor must not be zero")
    sign = -1 if (dividend < 0) != (divisor < 0) else 1
    quotient, remainder = divmod(abs(dividend), abs(divisor))
    abs_divisor = abs(divisor)
    if remainder == 0:
        return sign * quotient
    twice = remainder * 2
    if rounding is Rounding.UP:
        quotient += 1
    elif rounding is Rounding.HALF_UP:
        if twice >= abs_divisor:
            quotient += 1
    elif rounding is Rounding.HALF_EVEN:
        if twice > abs_divisor or (twice == abs_divisor and quotient % 2 == 1):
            quotient += 1
    return sign * quotient


@total_ordering
@dataclass(frozen=True)
class Money:
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
        whole, _, frac = s.partition(".")
        whole = whole or "0"
        if not whole.isdigit() or (frac and not frac.isdigit()):
            raise ValueError(f"not a decimal amount: {text}")
        if len(frac) > currency.exponent:
            raise ArithmeticError(f"{text} has more decimals than {currency.code} allows ({currency.exponent})")
        frac = frac.ljust(currency.exponent, "0")
        minor = int(whole) * currency.minor_units_per_major() + (int(frac) if frac else 0)
        return Money(-minor if negative else minor, currency)

    def _require_same_currency(self, other: "Money") -> None:
        if self.currency != other.currency:
            raise CurrencyMismatchError(self.currency, other.currency)

    def plus(self, other: "Money") -> "Money":
        self._require_same_currency(other)
        return Money(self.minor_units + other.minor_units, self.currency)

    def minus(self, other: "Money") -> "Money":
        self._require_same_currency(other)
        return Money(self.minor_units - other.minor_units, self.currency)

    def negate(self) -> "Money":
        return Money(-self.minor_units, self.currency)

    def abs(self) -> "Money":
        return self.negate() if self.minor_units < 0 else self

    def is_zero(self) -> bool:
        return self.minor_units == 0

    def is_negative(self) -> bool:
        return self.minor_units < 0

    def is_positive(self) -> bool:
        return self.minor_units > 0

    def times(self, numerator: int, denominator: int, rounding: Rounding) -> "Money":
        if denominator <= 0:
            raise ArithmeticError("denominator must be positive")
        return Money(divide_rounded(self.minor_units * numerator, denominator, rounding), self.currency)

    @staticmethod
    def sum(currency: Currency, *amounts: "Money") -> "Money":
        acc = Money(0, currency)
        for m in amounts:
            acc = acc.plus(m)
        return acc

    def __lt__(self, other: "Money") -> bool:
        self._require_same_currency(other)
        return self.minor_units < other.minor_units

    def __str__(self) -> str:
        magnitude = abs(self.minor_units)
        whole, frac = divmod(magnitude, self.currency.minor_units_per_major())
        text = str(whole)
        if self.currency.exponent > 0:
            text += "." + str(frac).rjust(self.currency.exponent, "0")
        return f"{'-' if self.minor_units < 0 else ''}{text} {self.currency.code}"

    __repr__ = __str__


# ---------------------------------------------------------------------------------------------------------------
# The lesson
# ---------------------------------------------------------------------------------------------------------------


class StatementParseException(RuntimeError):
    pass


class StatementFormat(Enum):
    MT940 = "MT940"
    CAMT053 = "CAMT053"
    BAI2 = "BAI2"
    OFX = "OFX"

    def __str__(self) -> str:
        return self.name

    @staticmethod
    def detect(text: str) -> "StatementFormat":
        """Sniff the format from the first bytes. Every format announces itself; none needs a file extension."""
        head = text.lstrip()
        if head.startswith("{1:") or head.startswith(":20:"):
            return StatementFormat.MT940
        if head.startswith("01,"):
            return StatementFormat.BAI2
        if head.startswith("OFXHEADER") or head.startswith("<OFX>") or "<?OFX" in head:
            return StatementFormat.OFX
        if head.startswith("<") and "camt.053" in head:
            return StatementFormat.CAMT053
        raise StatementParseException(f"unrecognised statement format: {head[:20]}")


@dataclass(frozen=True)
class StatementLine:
    """One booked movement. Amount is signed: positive is a credit to the account, negative a debit."""

    booking_date: date
    value_date: date
    amount: Money
    reference: str
    bank_reference: str
    description: str


@dataclass(frozen=True)
class Statement:
    """The normalized statement every format is parsed into. opening_reported is False when the format gives only a
    closing balance (OFX) and the opening was derived; such a statement cannot prove its own continuity."""

    source: StatementFormat
    account_id: str
    currency: Currency
    statement_id: str
    opening_date: date
    opening: Money
    closing_date: date
    closing: Money
    lines: tuple
    opening_reported: bool

    def __post_init__(self) -> None:
        object.__setattr__(self, "lines", tuple(self.lines))

    def movement(self) -> Money:
        total = Money.of_minor(0, self.currency)
        for line in self.lines:
            total = total.plus(line.amount)
        return total

    def continuity_errors(self) -> list[str]:
        """The continuity invariant: opening + sum(lines) == closing. Returns problems rather than raising."""
        errors: list[str] = []
        expected = self.opening.plus(self.movement())
        if expected != self.closing:
            errors.append(f"{self.statement_id}: opening {self.opening} + movements {self.movement()} = {expected}"
                          f", but closing is {self.closing} (difference {self.closing.minus(expected)})")
        for line in self.lines:
            if line.booking_date > self.closing_date:
                errors.append(f"{self.statement_id}: line {line.bank_reference} booked after closing date")
        return errors


class StatementChain:
    @staticmethod
    def gaps(statements: list[Statement]) -> list[str]:
        """Across consecutive statements of one account: each closing balance must be the next opening balance."""
        errors: list[str] = []
        for i in range(1, len(statements)):
            prev, nxt = statements[i - 1], statements[i]
            if prev.account_id != nxt.account_id:
                errors.append(f"account changed between {prev.statement_id} and {nxt.statement_id}")
                continue
            if not nxt.closing_date > prev.closing_date:
                errors.append(f"{nxt.statement_id} closes on {nxt.closing_date}, not after {prev.statement_id}")
            if prev.closing != nxt.opening:
                errors.append(f"gap between {prev.statement_id} closing {prev.closing} and "
                              f"{nxt.statement_id} opening {nxt.opening} (missing {nxt.opening.minus(prev.closing)})")
        return errors


class Parse:
    """Shared helpers for the four parsers."""

    @staticmethod
    def decimal(text: str, c: Currency) -> Money:
        """"1500,00" (MT940), "1500.00" (camt, OFX), "-1500" -> exact Money. Never via a float."""
        try:
            return Money.parse(text.strip().replace(",", "."), c)
        except (ValueError, ArithmeticError) as e:
            raise StatementParseException(f"bad amount '{text}': {e}") from e

    @staticmethod
    def yymmdd(s: str) -> date:
        """YYMMDD, pivoting at 2000 (all formats here are 21st-century)."""
        return date(2000 + int(s[0:2]), int(s[2:4]), int(s[4:6]))

    @staticmethod
    def normalize_newlines(text: str) -> str:
        return text.replace("\r\n", "\n").replace("\r", "\n")


@dataclass(frozen=True)
class Field:
    tag: str
    value: str


class Mt940Parser:
    """SWIFT MT940 Customer Statement. Fields are ":tag:value" lines; a line that does not start a tag continues the
    previous field. :60F/:60M opening, :61 statement line, :86 information to account owner, :62F/:62M closing."""

    TAG = re.compile(r"^:(\d{2}[A-Z]?):(.*)$")
    BALANCE = re.compile(r"([CD])(\d{6})([A-Z]{3})(\d+,\d{0,2})")
    # :61: value date, [entry date], mark (C, D, RC, RD), [funds code], amount, type, customer ref, [//bank ref], [\n supplementary]
    LINE = re.compile(r"(\d{6})(\d{4})?(RC|RD|C|D)([A-Z])?(\d+,\d{0,2})([NFS][A-Z0-9]{3})([^\n]*?)(?://([^\n]{1,16}))?(?:\n(.*))?",
                      re.DOTALL)
    ENVELOPE = re.compile(r"\{1:[^}]*\}(\{2:[^}]*\})?(\{3:(\{[^}]*\})*\})?\{4:")
    TRAILER = re.compile(r"^-\}.*$", re.MULTILINE)

    @staticmethod
    def fields(block: str) -> list[Field]:
        out: list[Field] = []
        tag: Optional[str] = None
        value: list[str] = []
        for line in block.split("\n"):
            m = Mt940Parser.TAG.match(line)
            if m:
                if tag is not None:
                    out.append(Field(tag, "".join(value)))
                tag = m.group(1)
                value = [m.group(2)]
            elif tag is not None and line != "":
                value.append("\n" + line)
        if tag is not None:
            out.append(Field(tag, "".join(value)))
        return out

    @staticmethod
    def parse_all(text: str) -> list[Statement]:
        # Strip SWIFT envelopes ({1:..}{2:..}[{3:..}]{4: ... -}); a file may concatenate several messages.
        body = Mt940Parser.TRAILER.sub("", Mt940Parser.ENVELOPE.sub("", Parse.normalize_newlines(text)))
        statements: list[Statement] = []
        current: list[Field] = []
        for f in Mt940Parser.fields(body):
            if f.tag == "20" and current:
                statements.append(Mt940Parser._build(current))
                current = []
            current.append(f)
        if current:
            statements.append(Mt940Parser._build(current))
        if not statements:
            raise StatementParseException("MT940: no :20: field found")
        return statements

    @staticmethod
    def parse(text: str) -> Statement:
        statements = Mt940Parser.parse_all(text)
        if len(statements) != 1:
            raise StatementParseException(f"MT940: expected one statement, found {len(statements)}")
        return statements[0]

    @staticmethod
    def _build(fields: list[Field]) -> Statement:
        ref = account = None
        seq = ""
        opening = closing = None
        lines: list[StatementLine] = []
        currency: Optional[Currency] = None
        pending_line: Optional[re.Match] = None
        pending_info: Optional[str] = None
        for f in fields:
            if f.tag == "20":
                ref = f.value.strip()
            elif f.tag == "25":
                account = f.value.strip()
            elif f.tag == "28C":
                seq = f.value.strip()
            elif f.tag in ("60F", "60M"):
                opening = Mt940Parser._balance(f)
                currency = Currency.of(opening.group(3))
            elif f.tag == "61":
                if pending_line is not None:
                    lines.append(Mt940Parser._line(pending_line, pending_info, currency))
                pending_line = Mt940Parser.LINE.fullmatch(f.value)
                if pending_line is None:
                    raise StatementParseException(f"MT940: bad :61: {f.value}")
                pending_info = None
            elif f.tag == "86":
                if pending_line is not None:
                    pending_info = f.value.replace("\n", " ").strip()
            elif f.tag in ("62F", "62M"):
                closing = Mt940Parser._balance(f)
            # :21:, :64:, :65: and others are not needed for the normalized statement
        if pending_line is not None:
            lines.append(Mt940Parser._line(pending_line, pending_info, currency))
        if ref is None or account is None or opening is None or closing is None:
            raise StatementParseException("MT940: statement is missing :20:, :25:, :60a: or :62a:")
        if closing.group(3) != opening.group(3):
            raise StatementParseException("MT940: opening and closing currencies differ")
        return Statement(StatementFormat.MT940, account, currency, ref + ("" if not seq else "/" + seq),
                         Parse.yymmdd(opening.group(2)), Mt940Parser._signed(opening.group(1), Parse.decimal(opening.group(4), currency)),
                         Parse.yymmdd(closing.group(2)), Mt940Parser._signed(closing.group(1), Parse.decimal(closing.group(4), currency)),
                         tuple(lines), True)

    @staticmethod
    def _balance(f: Field) -> re.Match:
        m = Mt940Parser.BALANCE.fullmatch(f.value.strip())
        if m is None:
            raise StatementParseException(f"MT940: bad balance :{f.tag}: {f.value}")
        return m

    @staticmethod
    def _signed(mark: str, m: Money) -> Money:
        return m.negate() if mark == "D" else m

    @staticmethod
    def _line(m: re.Match, info: Optional[str], currency: Currency) -> StatementLine:
        value = Parse.yymmdd(m.group(1))
        booking = value
        if m.group(2) is not None:
            month, day = int(m.group(2)[0:2]), int(m.group(2)[2:4])
            year = value.year
            if month - value.month > 6:
                year -= 1  # value 2027-01-02, entry 1231 -> 2026-12-31
            if value.month - month > 6:
                year += 1  # value 2026-12-31, entry 0102 -> 2027-01-02
            booking = date(year, month, day)
        amount = Parse.decimal(m.group(5), currency)
        # C credit, D debit, RC reversal of a credit (a debit), RD reversal of a debit (a credit)
        credit = m.group(3) in ("C", "RD")
        customer_ref = m.group(7).strip()
        supplementary = "" if m.group(9) is None else m.group(9).strip()
        description = info if info is not None else supplementary
        return StatementLine(booking, value, amount if credit else amount.negate(),
                             "" if customer_ref == "NONREF" else customer_ref, m.group(8) or "", description)


class Camt053Parser:
    """ISO 20022 camt.053.001.08 Bank to Customer Statement. Only booked (BOOK) entries move the balance."""

    NS = "urn:iso:std:iso:20022:tech:xsd:camt.053.001.08"
    DOCTYPE = re.compile(r"<!DOCTYPE", re.IGNORECASE)

    @staticmethod
    def parse(xml: str) -> Statement:
        doc = Camt053Parser._parse_root(xml)
        stmt = Camt053Parser.child(Camt053Parser.child(doc, "BkToCstmrStmt"), "Stmt")
        acct = Camt053Parser.child(stmt, "Acct")
        account = Camt053Parser.text(Camt053Parser.child(Camt053Parser.child(acct, "Id"), "IBAN"))
        currency = Currency.of(Camt053Parser.text(Camt053Parser.child(acct, "Ccy")))
        opening = closing = None
        opening_date = closing_date = None
        for bal in Camt053Parser.children(stmt, "Bal"):
            code = Camt053Parser.text(Camt053Parser.child(Camt053Parser.child(Camt053Parser.child(bal, "Tp"), "CdOrPrtry"), "Cd"))
            amount = Camt053Parser._amount(bal, currency)
            dt = date.fromisoformat(Camt053Parser.text(Camt053Parser.child(Camt053Parser.child(bal, "Dt"), "Dt")))
            if code in ("OPBD", "PRCD"):
                opening, opening_date = amount, dt
            if code == "CLBD":
                closing, closing_date = amount, dt
        if opening is None or closing is None:
            raise StatementParseException("camt.053: missing OPBD/PRCD or CLBD balance")
        lines: list[StatementLine] = []
        for ntry in Camt053Parser.children(stmt, "Ntry"):
            status = Camt053Parser.text(Camt053Parser.child(Camt053Parser.child(ntry, "Sts"), "Cd"))
            if status != "BOOK":
                continue  # PDNG and INFO entries are information, not balance movements
            amount = Camt053Parser._amount(ntry, currency)
            rvsl = Camt053Parser.opt_child(ntry, "RvslInd")
            if rvsl is not None and Camt053Parser.text(rvsl) == "true":
                amount = amount.negate()
            tx_dtls = Camt053Parser.opt_child(Camt053Parser.opt_child(ntry, "NtryDtls"), "TxDtls")
            refs = Camt053Parser.opt_child(tx_dtls, "Refs")
            e2e = "" if refs is None or Camt053Parser.opt_child(refs, "EndToEndId") is None else Camt053Parser.text(Camt053Parser.child(refs, "EndToEndId"))
            addtl = Camt053Parser.opt_child(ntry, "AddtlNtryInf")
            rmt = Camt053Parser.opt_child(Camt053Parser.opt_child(tx_dtls, "RmtInf"), "Ustrd")
            description = Camt053Parser.text(addtl) if addtl is not None else Camt053Parser.text(rmt) if rmt is not None else ""
            lines.append(StatementLine(
                date.fromisoformat(Camt053Parser.text(Camt053Parser.child(Camt053Parser.child(ntry, "BookgDt"), "Dt"))),
                date.fromisoformat(Camt053Parser.text(Camt053Parser.child(Camt053Parser.child(ntry, "ValDt"), "Dt"))), amount,
                "" if e2e == "NOTPROVIDED" else e2e, Camt053Parser.text(Camt053Parser.child(ntry, "AcctSvcrRef")), description))
        return Statement(StatementFormat.CAMT053, account, currency, Camt053Parser.text(Camt053Parser.child(stmt, "Id")),
                         opening_date, opening, closing_date, closing, tuple(lines), True)

    @staticmethod
    def _amount(parent: ET.Element, currency: Currency) -> Money:
        """Amt with Ccy attribute plus CdtDbtInd CRDT/DBIT; the currency must match the account."""
        amt = Camt053Parser.child(parent, "Amt")
        if amt.get("Ccy", "") != currency.code:
            raise StatementParseException(f"camt.053: amount in {amt.get('Ccy', '')} on a {currency.code} account")
        m = Parse.decimal(Camt053Parser.text(amt), currency)
        ind = Camt053Parser.text(Camt053Parser.child(parent, "CdtDbtInd"))
        if ind not in ("CRDT", "DBIT"):
            raise StatementParseException(f"camt.053: bad CdtDbtInd {ind}")
        return m.negate() if ind == "DBIT" else m

    @staticmethod
    def _parse_root(xml: str) -> ET.Element:
        """XXE-safe: ElementTree never fetches external entities, but it would expand internal ones, so any DOCTYPE
        is rejected before the parser sees it."""
        if Camt053Parser.DOCTYPE.search(xml):
            raise StatementParseException("camt.053: malformed XML: DOCTYPE is disallowed")
        try:
            root = ET.fromstring(xml)
        except ET.ParseError as e:
            raise StatementParseException(f"camt.053: malformed XML: {e}") from e
        if Camt053Parser._namespace(root) != Camt053Parser.NS:
            raise StatementParseException(f"camt.053: unexpected namespace {Camt053Parser._namespace(root)}")
        return root

    @staticmethod
    def _local_name(e: ET.Element) -> str:
        return e.tag[e.tag.index("}") + 1:] if e.tag.startswith("{") else e.tag

    @staticmethod
    def _namespace(e: ET.Element) -> Optional[str]:
        return e.tag[1:e.tag.index("}")] if e.tag.startswith("{") else None

    @staticmethod
    def opt_child(parent: Optional[ET.Element], name: str) -> Optional[ET.Element]:
        if parent is None:
            return None
        for n in parent:
            if Camt053Parser._local_name(n) == name:
                return n
        return None

    @staticmethod
    def child(parent: Optional[ET.Element], name: str) -> ET.Element:
        e = Camt053Parser.opt_child(parent, name)
        if e is None:
            raise StatementParseException(f"camt.053: missing {name} under {'?' if parent is None else Camt053Parser._local_name(parent)}")
        return e

    @staticmethod
    def children(parent: ET.Element, name: str) -> list[ET.Element]:
        return [n for n in parent if Camt053Parser._local_name(n) == name]

    @staticmethod
    def text(e: ET.Element) -> str:
        return "".join(e.itertext()).strip()


@dataclass(frozen=True)
class LogicalRecord:
    """One logical record (88 continuations joined) and how many physical lines it used."""

    type: str
    fields: tuple
    text: Optional[str]
    physical_lines: int


class Bai2Parser:
    """BAI2 (Bank Administration Institute, version 2). Comma-delimited records ending in '/', 88 continues the
    previous record. 01 file header, 02 group header, 03 account identifier and summaries, 16 transaction detail,
    49 account trailer, 98 group trailer, 99 file trailer. Amounts are integers in minor units, no decimal point."""

    @staticmethod
    def records(text: str) -> list[LogicalRecord]:
        out: list[LogicalRecord] = []
        for raw in Parse.normalize_newlines(text).split("\n"):
            if not raw.strip():
                continue
            line = raw.strip()
            rtype = line[0:2]
            if rtype == "88":
                if not out:
                    raise StatementParseException("BAI2: 88 continuation with no previous record")
                prev = out.pop()
                rest = line[3:]
                if prev.type == "16":
                    out.append(LogicalRecord("16", prev.fields, (prev.text + " " + rest).strip(), prev.physical_lines + 1))
                else:
                    out.append(LogicalRecord(prev.type, prev.fields + tuple(Bai2Parser._split(rest)), None, prev.physical_lines + 1))
                continue
            if rtype == "16":
                # 16: type code, amount, funds type, [funds fields], bank ref, customer ref, text-to-end-of-record
                parts = line.split(",")
                funds = parts[3]
                if funds in ("0", "1", "2", "Z", ""):
                    funds_extra = 0
                elif funds == "V":
                    funds_extra = 2
                elif funds == "S":
                    funds_extra = 3
                else:
                    raise StatementParseException(f"BAI2: unsupported funds type {funds}")
                fixed = 4 + funds_extra + 2  # record code, type, amount, funds, extras, bank ref, customer ref
                head = line.split(",", fixed)
                fields = tuple(head[1:fixed])
                txt = head[fixed] if len(head) > fixed else ""
                if txt.endswith("/"):
                    txt = txt[:-1]
                out.append(LogicalRecord("16", fields, txt.strip(), 1))
            else:
                out.append(LogicalRecord(rtype, tuple(Bai2Parser._split(line[3:])), None, 1))
        return out

    @staticmethod
    def _split(body: str) -> list[str]:
        b = body[:-1] if body.endswith("/") else body
        return b.split(",")

    @staticmethod
    def amount(s: str) -> int:
        if s == "":
            return 0
        try:
            return int(s[1:] if s.startswith("+") else s)
        except ValueError as e:
            raise StatementParseException(f"BAI2: bad amount {s}") from e

    @staticmethod
    def parse_all(text: str) -> list[Statement]:
        recs = Bai2Parser.records(text)
        if not recs or recs[0].type != "01":
            raise StatementParseException("BAI2: file must start with 01")
        out: list[Statement] = []
        as_of: Optional[date] = None
        group_currency: Optional[Currency] = None
        account: Optional[str] = None
        currency: Optional[Currency] = None
        opening = closing = None
        lines: Optional[list[StatementLine]] = None
        account_total = group_total = file_total = 0
        account_records = group_records = file_records = groups = accounts = 0
        for r in recs:
            file_records += r.physical_lines
            group_records += r.physical_lines
            account_records += r.physical_lines
            f = r.fields
            if r.type == "01":
                pass
            elif r.type == "02":
                as_of = Parse.yymmdd(f[3])
                group_currency = USD if f[5] == "" else Currency.of(f[5])
                group_records = r.physical_lines
                group_total = 0
                accounts = 0
                groups += 1
            elif r.type == "03":
                account = f[0]
                currency = group_currency if f[1] == "" else Currency.of(f[1])
                account_records = r.physical_lines
                account_total = 0
                lines = []
                opening = None
                closing = None
                # summary triplets: type code, amount, item count, funds type (only "" or 0 supported here)
                i = 2
                while i + 3 < len(f) + 1 and f[i] != "":
                    amt = Bai2Parser.amount(f[i + 1])
                    account_total += amt
                    if f[i] == "010":
                        opening = Money.of_minor(amt, currency)
                    if f[i] == "015":
                        closing = Money.of_minor(amt, currency)
                    i += 4
            elif r.type == "16":
                if lines is None:
                    raise StatementParseException("BAI2: 16 record outside an account")
                type_code = int(f[0])
                amt = Bai2Parser.amount(f[1])
                account_total += amt
                credit = 100 <= type_code <= 399
                debit = 400 <= type_code <= 699
                if not credit and not debit:
                    raise StatementParseException(f"BAI2: type code {type_code} is not a credit or debit detail")
                n = len(f)
                lines.append(StatementLine(as_of, as_of, Money.of_minor(amt if credit else -amt, currency), f[n - 1], f[n - 2], r.text))
            elif r.type == "49":
                declared = Bai2Parser.amount(f[0])
                declared_records = int(f[1])
                if declared != account_total:
                    raise StatementParseException(f"BAI2: account {account} control total {declared} != computed {account_total}")
                if declared_records != account_records:
                    raise StatementParseException(f"BAI2: account {account} record count {declared_records} != {account_records}")
                if opening is None or closing is None:
                    raise StatementParseException(f"BAI2: account {account} lacks 010 opening or 015 closing ledger")
                out.append(Statement(StatementFormat.BAI2, account, currency, f"BAI2-{account}-{as_of}", as_of, opening, as_of, closing,
                                     tuple(lines), True))
                group_total += account_total
                accounts += 1
                lines = None
            elif r.type == "98":
                if Bai2Parser.amount(f[0]) != group_total:
                    raise StatementParseException(f"BAI2: group control total {f[0]} != computed {group_total}")
                if int(f[1]) != accounts:
                    raise StatementParseException("BAI2: group account count mismatch")
                if int(f[2]) != group_records:
                    raise StatementParseException(f"BAI2: group record count {f[2]} != {group_records}")
                file_total += group_total
            elif r.type == "99":
                if Bai2Parser.amount(f[0]) != file_total:
                    raise StatementParseException(f"BAI2: file control total {f[0]} != computed {file_total}")
                if int(f[1]) != groups:
                    raise StatementParseException("BAI2: file group count mismatch")
                if int(f[2]) != file_records:
                    raise StatementParseException(f"BAI2: file record count {f[2]} != {file_records}")
            else:
                raise StatementParseException(f"BAI2: unknown record type {r.type}")
        if recs[-1].type != "99":
            raise StatementParseException("BAI2: file must end with 99")
        return out

    @staticmethod
    def parse(text: str) -> Statement:
        statements = Bai2Parser.parse_all(text)
        if len(statements) != 1:
            raise StatementParseException(f"BAI2: expected one account, found {len(statements)}")
        return statements[0]


class OfxNode:
    def __init__(self, name: str, value: Optional[str]) -> None:
        self.name = name
        self.value = value
        self.children: list[OfxNode] = []

    def find(self, n: str) -> Optional["OfxNode"]:
        if self.name == n:
            return self
        for c in self.children:
            hit = c.find(n)
            if hit is not None:
                return hit
        return None

    def req(self, n: str) -> str:
        hit = self.find(n)
        if hit is None or hit.value is None:
            raise StatementParseException(f"OFX: missing <{n}>")
        return hit.value

    def opt(self, n: str) -> str:
        hit = self.find(n)
        return "" if hit is None or hit.value is None else hit.value


class OfxParser:
    """OFX 1.x (SGML: leaf elements have no closing tag) and 2.x (XML). A tiny tokenizer handles both.
    OFX reports only a closing (ledger) balance, so the opening balance is derived and cannot be verified."""

    @staticmethod
    def tree(text: str) -> OfxNode:
        start = text.find("<OFX>")
        if start < 0:
            raise StatementParseException("OFX: no <OFX> root")
        root = OfxNode("#root", None)
        stack = [root]
        i = start
        while i < len(text):
            lt = text.find("<", i)
            if lt < 0:
                break
            gt = text.find(">", lt)
            if gt < 0:
                raise StatementParseException("OFX: unterminated tag")
            tag = text[lt + 1:gt].strip()
            nxt = text.find("<", gt)
            value = text[gt + 1:len(text) if nxt < 0 else nxt].strip()
            if tag.startswith("/"):
                name = tag[1:]
                # close an aggregate; a closing tag for a leaf (OFX 2.x XML) matches nothing on the stack and is ignored
                if any(n.name == name for n in stack):
                    while stack[-1].name != name:
                        stack.pop()
                    stack.pop()
            elif value:
                stack[-1].children.append(OfxNode(tag, value))  # leaf: <TRNAMT>-1500.00
            else:
                agg = OfxNode(tag, None)                        # aggregate: <STMTTRN>
                stack[-1].children.append(agg)
                stack.append(agg)
            i = len(text) if nxt < 0 else nxt
        return root

    @staticmethod
    def date(ofx_date: str) -> date:
        return date(int(ofx_date[0:4]), int(ofx_date[4:6]), int(ofx_date[6:8]))

    @staticmethod
    def parse(text: str) -> Statement:
        root = OfxParser.tree(Parse.normalize_newlines(text))
        stmt = root.find("STMTRS")
        if stmt is None:
            raise StatementParseException("OFX: no <STMTRS> (bank statement response)")
        currency = Currency.of(stmt.req("CURDEF"))
        account = stmt.req("ACCTID")
        lst = stmt.find("BANKTRANLIST")
        if lst is None:
            raise StatementParseException("OFX: no <BANKTRANLIST>")
        lines: list[StatementLine] = []
        for t in lst.children:
            if t.name != "STMTTRN":
                continue
            posted = OfxParser.date(t.req("DTPOSTED"))
            user = t.opt("DTUSER")
            name, memo = t.opt("NAME"), t.opt("MEMO")
            description = (name + " " + memo).strip()
            lines.append(StatementLine(posted, posted if user == "" else OfxParser.date(user), Parse.decimal(t.req("TRNAMT"), currency),
                                       t.opt("REFNUM"), t.req("FITID"), description))
        ledger = stmt.find("LEDGERBAL")
        if ledger is None:
            raise StatementParseException("OFX: no <LEDGERBAL>")
        closing = Parse.decimal(ledger.req("BALAMT"), currency)
        closing_date = OfxParser.date(ledger.req("DTASOF"))
        movement = Money.of_minor(0, currency)
        for line in lines:
            movement = movement.plus(line.amount)
        return Statement(StatementFormat.OFX, account, currency, f"OFX-{account}-{lst.req('DTEND')}",
                         OfxParser.date(lst.req("DTSTART")), closing.minus(movement), closing_date, closing, tuple(lines), False)


class StatementParsers:
    @staticmethod
    def parse(text: str) -> Statement:
        fmt = StatementFormat.detect(text)
        if fmt is StatementFormat.MT940:
            return Mt940Parser.parse(text)
        if fmt is StatementFormat.CAMT053:
            return Camt053Parser.parse(text)
        if fmt is StatementFormat.BAI2:
            return Bai2Parser.parse(text)
        return OfxParser.parse(text)


class Samples:
    """One business day on one account (opening 12,500.00; -1,500.00; +3,250.75; -12.50; closing 14,238.25) in four formats."""

    MT940 = dedent("""\
        {1:F01COBADEFFAXXX0000000000}{2:O9402200261012COBADEFFAXXX00000000002610122200N}{4:
        :20:STMT261012
        :25:DE89370400440532013000
        :28C:00287/001
        :60F:C261009EUR12500,00
        :61:2610121012D1500,00NTRFINV-4411//COBA0001
        :86:Supplier payment INV-4411
        Fournitures Lyon SARL
        :61:2610121012C3250,75NTRFORDER-77//COBA0002
        :86:Customer receipt ORDER-77 Delft Sensors BV
        :61:2610121012D12,50NCHGNONREF//COBA0003
        :86:Account maintenance fee
        :62F:C261012EUR14238,25
        :64:C261012EUR14238,25
        -}
        """)

    MT940_DAY2 = dedent("""\
        {1:F01COBADEFFAXXX0000000000}{2:O9402200261013COBADEFFAXXX00000000002610132200N}{4:
        :20:STMT261013
        :25:DE89370400440532013000
        :28C:00288/001
        :60F:C261012EUR%s
        :61:2610131013C100,00NTRFORDER-78//COBA0004
        :86:Customer receipt ORDER-78
        :62F:C261013EUR%s
        -}
        """)

    CAMT053 = dedent("""\
        <?xml version="1.0" encoding="UTF-8"?>
        <Document xmlns="urn:iso:std:iso:20022:tech:xsd:camt.053.001.08">
          <BkToCstmrStmt>
            <GrpHdr><MsgId>COBA-STMT-261012</MsgId><CreDtTm>2026-10-12T22:00:00</CreDtTm></GrpHdr>
            <Stmt>
              <Id>STMT261012</Id>
              <ElctrncSeqNb>287</ElctrncSeqNb>
              <CreDtTm>2026-10-12T22:00:00</CreDtTm>
              <Acct><Id><IBAN>DE89370400440532013000</IBAN></Id><Ccy>EUR</Ccy></Acct>
              <Bal>
                <Tp><CdOrPrtry><Cd>OPBD</Cd></CdOrPrtry></Tp>
                <Amt Ccy="EUR">12500.00</Amt><CdtDbtInd>CRDT</CdtDbtInd><Dt><Dt>2026-10-12</Dt></Dt>
              </Bal>
              <Bal>
                <Tp><CdOrPrtry><Cd>CLBD</Cd></CdOrPrtry></Tp>
                <Amt Ccy="EUR">14238.25</Amt><CdtDbtInd>CRDT</CdtDbtInd><Dt><Dt>2026-10-12</Dt></Dt>
              </Bal>
              <Ntry>
                <Amt Ccy="EUR">1500.00</Amt><CdtDbtInd>DBIT</CdtDbtInd><Sts><Cd>BOOK</Cd></Sts>
                <BookgDt><Dt>2026-10-12</Dt></BookgDt><ValDt><Dt>2026-10-12</Dt></ValDt>
                <AcctSvcrRef>COBA0001</AcctSvcrRef>
                <NtryDtls><TxDtls><Refs><EndToEndId>INV-4411</EndToEndId></Refs>
                  <RmtInf><Ustrd>Invoice 4411</Ustrd></RmtInf></TxDtls></NtryDtls>
                <AddtlNtryInf>Supplier payment INV-4411 Fournitures Lyon SARL</AddtlNtryInf>
              </Ntry>
              <Ntry>
                <Amt Ccy="EUR">3250.75</Amt><CdtDbtInd>CRDT</CdtDbtInd><Sts><Cd>BOOK</Cd></Sts>
                <BookgDt><Dt>2026-10-12</Dt></BookgDt><ValDt><Dt>2026-10-12</Dt></ValDt>
                <AcctSvcrRef>COBA0002</AcctSvcrRef>
                <NtryDtls><TxDtls><Refs><EndToEndId>ORDER-77</EndToEndId></Refs></TxDtls></NtryDtls>
                <AddtlNtryInf>Customer receipt ORDER-77 Delft Sensors BV</AddtlNtryInf>
              </Ntry>
              <Ntry>
                <Amt Ccy="EUR">12.50</Amt><CdtDbtInd>DBIT</CdtDbtInd><Sts><Cd>BOOK</Cd></Sts>
                <BookgDt><Dt>2026-10-12</Dt></BookgDt><ValDt><Dt>2026-10-12</Dt></ValDt>
                <AcctSvcrRef>COBA0003</AcctSvcrRef>
                <AddtlNtryInf>Account maintenance fee</AddtlNtryInf>
              </Ntry>
              <Ntry>
                <Amt Ccy="EUR">999.00</Amt><CdtDbtInd>CRDT</CdtDbtInd><Sts><Cd>PDNG</Cd></Sts>
                <BookgDt><Dt>2026-10-13</Dt></BookgDt><ValDt><Dt>2026-10-13</Dt></ValDt>
                <AcctSvcrRef>COBA0099</AcctSvcrRef>
                <AddtlNtryInf>Pending incoming transfer</AddtlNtryInf>
              </Ntry>
            </Stmt>
          </BkToCstmrStmt>
        </Document>
        """)

    BAI2 = dedent("""\
        01,021000021,ACMEUS,261012,2200,1,80,1,2/
        02,ACMEUS,021000021,1,261012,2200,USD,2/
        03,0001234567,USD,010,1250000,,,015,1423825,,/
        16,495,150000,Z,COBA0001,INV-4411,Supplier payment INV-4411
        88,Fournitures Lyon SARL
        16,195,325075,Z,COBA0002,ORDER-77,Customer receipt ORDER-77 Delft Sensors BV
        16,698,1250,Z,COBA0003,,Account maintenance fee
        49,3150150,6/
        98,3150150,1,8/
        99,3150150,1,10/
        """)

    OFX = dedent("""\
        OFXHEADER:100
        DATA:OFXSGML
        VERSION:102
        SECURITY:NONE
        ENCODING:USASCII
        CHARSET:1252
        COMPRESSION:NONE
        OLDFILEUID:NONE
        NEWFILEUID:NONE

        <OFX>
        <SIGNONMSGSRSV1><SONRS><STATUS><CODE>0<SEVERITY>INFO</STATUS><DTSERVER>20261012220000<LANGUAGE>ENG</SONRS></SIGNONMSGSRSV1>
        <BANKMSGSRSV1><STMTTRNRS><TRNUID>1<STATUS><CODE>0<SEVERITY>INFO</STATUS>
        <STMTRS><CURDEF>USD
        <BANKACCTFROM><BANKID>021000021<ACCTID>0001234567<ACCTTYPE>CHECKING</BANKACCTFROM>
        <BANKTRANLIST><DTSTART>20261012<DTEND>20261012
        <STMTTRN><TRNTYPE>DEBIT<DTPOSTED>20261012<TRNAMT>-1500.00<FITID>COBA0001<REFNUM>INV-4411<NAME>Fournitures Lyon SARL<MEMO>Supplier payment INV-4411</STMTTRN>
        <STMTTRN><TRNTYPE>CREDIT<DTPOSTED>20261012<TRNAMT>3250.75<FITID>COBA0002<REFNUM>ORDER-77<NAME>Delft Sensors BV<MEMO>Customer receipt ORDER-77</STMTTRN>
        <STMTTRN><TRNTYPE>SRVCHG<DTPOSTED>20261012<TRNAMT>-12.50<FITID>COBA0003<NAME>Account maintenance fee</STMTTRN>
        </BANKTRANLIST>
        <LEDGERBAL><BALAMT>14238.25<DTASOF>20261012220000</LEDGERBAL>
        </STMTRS></STMTTRNRS></BANKMSGSRSV1></OFX>
        """)


def show(s: Statement) -> None:
    print(f"{s.source}  account {s.account_id}  {s.statement_id}  {s.opening_date} {s.opening} -> {s.closing_date} {s.closing}"
          f"{'' if s.opening_reported else '  (opening derived)'}")
    for line in s.lines:
        print(f"   {line.booking_date}  {str(line.amount):>12}  ref={line.reference:<9} bank={line.bank_reference:<9} {line.description}")
    errors = s.continuity_errors()
    print("   continuity: " + (f"OK (opening + {s.movement()} = closing)" if not errors else str(errors)))


def main() -> None:
    print("== Four formats, one normalized model ==")
    for sample in (Samples.MT940, Samples.CAMT053, Samples.BAI2, Samples.OFX):
        show(StatementParsers.parse(sample))

    print("\n== Same day, same minor-unit movements? ==")
    for sample in (Samples.MT940, Samples.CAMT053, Samples.BAI2, Samples.OFX):
        s = StatementParsers.parse(sample)
        amounts = [line.amount.minor_units for line in s.lines]
        print(f"{s.source} {amounts} closing {s.closing.minor_units}")

    print("\n== A statement that does not add up ==")
    broken = Mt940Parser.parse(Samples.MT940.replace(":62F:C261012EUR14238,25", ":62F:C261012EUR14283,25"))
    for e in broken.continuity_errors():
        print(f"   {e}")

    print("\n== Chains: day 2 must open where day 1 closed ==")
    two_days = Samples.MT940 + Samples.MT940_DAY2 % ("14238,25", "14338,25")
    chain = Mt940Parser.parse_all(two_days)
    print(f"   statements: {len(chain)}, gaps: {StatementChain.gaps(chain)}")
    gapped = Samples.MT940 + Samples.MT940_DAY2 % ("14250,75", "14350,75")
    bad = Mt940Parser.parse_all(gapped)
    print(f"   each internally continuous: {str(all(not s.continuity_errors() for s in bad)).lower()}")
    for g in StatementChain.gaps(bad):
        print(f"   {g}")

    print("\n== Rejections ==")
    for action in (lambda: Bai2Parser.parse(Samples.BAI2.replace("49,3150150,6/", "49,3150151,6/")),
                   lambda: Mt940Parser.parse(Samples.MT940.replace("D12,50NCHG", "D12,505NCHG")),
                   lambda: StatementParsers.parse("Date,Description,Amount\n2026-10-12,Fee,-12.50\n")):
        try:
            action()
        except StatementParseException as e:
            print(f"   {e}")


if __name__ == "__main__":
    main()
