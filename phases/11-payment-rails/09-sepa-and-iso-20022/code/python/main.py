"""Lesson: phases/11-payment-rails/09-sepa-and-iso-20022/docs/en.md
SEPA, Wires, and ISO 20022: generate and parse pain.001 and pacs.008 with xml.etree, validate SEPA rules.
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: ISO 20022 message definitions pain.001.001.09 and pacs.008.001.08; EPC SEPA Credit Transfer Rulebook
and Implementation Guidelines (EPC125-05, EPC115-06); ISO 13616 (IBAN); ISO 9362 (BIC). Python 3.11+, stdlib only.
"""

from __future__ import annotations

import re
import xml.etree.ElementTree as ET
from dataclasses import dataclass
from datetime import date, datetime, timedelta
from enum import Enum
from functools import total_ordering
from typing import Callable, Optional


# ---------------------------------------------------------------------------------------------------------------
# Money, Currency, Rounding: copied unchanged from phases/10-money-and-ledgers/01-representing-money/code/python
# (lessons are self-contained; there is no shared library). Integer minor units, never floats.
# ---------------------------------------------------------------------------------------------------------------


@dataclass(frozen=True)
class Currency:
    """ISO 4217 currency: a three-letter code and the number of decimals in its minor unit."""

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
    """Integer division with an explicit rounding rule, on magnitudes so Python's floor division matches Java."""
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
        """Parse "12.34", "-0.5", "1200" exactly; reject more decimals than the currency allows."""
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

    def allocate(self, *ratios: int) -> list["Money"]:
        if not ratios:
            raise ValueError("need at least one ratio")
        if any(r < 0 for r in ratios):
            raise ValueError("ratios must be non-negative")
        total = sum(ratios)
        if total == 0:
            raise ValueError("ratios must not all be zero")
        magnitude = abs(self.minor_units)
        shares = [magnitude * r // total for r in ratios]
        remainders = [magnitude * r % total for r in ratios]
        leftover = magnitude - sum(shares)
        order = sorted(range(len(ratios)), key=lambda i: -remainders[i])
        for i in order[:leftover]:
            shares[i] += 1
        sign = -1 if self.minor_units < 0 else 1
        return [Money(sign * s, self.currency) for s in shares]

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


class Iso20022Exception(RuntimeError):
    """Anything wrong with an ISO 20022 message: malformed XML, wrong message type, bad totals, rule violations."""


class Iban:
    """ISO 13616 IBAN check: country length table (SEPA subset) and mod-97 == 1. Lesson 11 builds the full registry."""

    LENGTHS = {
        "AT": 20, "BE": 16, "DE": 22, "ES": 24, "FI": 18, "FR": 27,
        "GB": 22, "IE": 22, "IT": 27, "LU": 20, "NL": 18, "PT": 25,
    }

    @staticmethod
    def is_valid(iban: Optional[str]) -> bool:
        if iban is None or len(iban) < 15 or len(iban) > 34:
            return False
        if not all(("A" <= c <= "Z") or ("0" <= c <= "9") for c in iban):
            return False
        expected = Iban.LENGTHS.get(iban[:2])
        if expected is None or expected != len(iban):
            return False
        rearranged = iban[4:] + iban[:4]
        remainder = 0
        for c in rearranged:
            value = ord(c) - ord("0") if c.isdigit() else ord(c) - ord("A") + 10
            remainder = (remainder * 10 + value if value < 10 else remainder * 100 + value) % 97
        return remainder == 1

    @staticmethod
    def bank_code(iban: str) -> str:
        """The national bank code inside the BBAN, for the countries the demo directory knows."""
        country = iban[:2]
        if country == "DE":
            return iban[4:12]  # Bankleitzahl, 8 digits
        if country == "FR":
            return iban[4:9]   # code banque, 5 digits
        return iban[4:8]       # ES entidad, NL bank code, and most others: 4 characters


class Bic:
    """ISO 9362 BIC: 4 letters institution, 2 letters country, 2 alphanumeric location, optional 3 branch."""

    FORMAT = re.compile(r"[A-Z]{4}[A-Z]{2}[A-Z0-9]{2}([A-Z0-9]{3})?")

    @staticmethod
    def is_valid(bic: Optional[str]) -> bool:
        return bic is not None and Bic.FORMAT.fullmatch(bic) is not None


@dataclass(frozen=True)
class Party:
    """A debtor or creditor: name, account (IBAN), and the bank's BIC (optional for the customer since IBAN-only)."""

    name: str
    iban: str
    bic: Optional[str] = None

    def __post_init__(self) -> None:
        if self.name is None:
            raise TypeError("name")
        if self.iban is None:
            raise TypeError("iban")


@dataclass(frozen=True)
class CreditTransfer:
    """One payment inside a pain.001: CdtTrfTxInf."""

    end_to_end_id: str
    amount: Money
    creditor: Party
    remittance: Optional[str]


@dataclass(frozen=True)
class PaymentInitiation:
    """A customer-to-bank credit transfer initiation (pain.001) with one payment information block."""

    message_id: str
    created_at: datetime
    initiating_party: str
    payment_info_id: str
    requested_execution_date: date
    debtor: Party
    transfers: tuple

    def __post_init__(self) -> None:
        object.__setattr__(self, "transfers", tuple(self.transfers))

    def number_of_transactions(self) -> int:
        return len(self.transfers)

    def control_sum(self) -> Money:
        total = Money.of_minor(0, EUR)
        for t in self.transfers:
            total = total.plus(t.amount)
        return total


@dataclass(frozen=True)
class InterbankTransfer:
    """One payment inside a pacs.008: the interbank view, with the transaction id the debtor bank assigned."""

    end_to_end_id: str
    transaction_id: str
    amount: Money
    debtor: Party
    creditor: Party
    remittance: Optional[str]


@dataclass(frozen=True)
class InterbankMessage:
    """A bank-to-bank customer credit transfer (pacs.008)."""

    message_id: str
    created_at: datetime
    settlement_date: date
    transfers: tuple

    def __post_init__(self) -> None:
        object.__setattr__(self, "transfers", tuple(self.transfers))

    def total_settlement_amount(self) -> Money:
        total = Money.of_minor(0, EUR)
        for t in self.transfers:
            total = total.plus(t.amount)
        return total


class SepaRules:
    """SEPA Credit Transfer business rules from the EPC rulebook and implementation guidelines."""

    # The SEPA basic Latin character set (EPC217-08): letters, digits, and / - ? : ( ) . , ' + space.
    LATIN = re.compile(r"[A-Za-z0-9/\-?:().,'+ ]*")
    MAX_AMOUNT_MINOR = 99_999_999_999  # 999,999,999.99 EUR

    @staticmethod
    def violations(p: PaymentInitiation) -> list[str]:
        v: list[str] = []
        SepaRules._id(v, "MsgId", p.message_id)
        SepaRules._id(v, "PmtInfId", p.payment_info_id)
        SepaRules._text(v, "InitgPty/Nm", p.initiating_party, 70)
        SepaRules._party(v, "Dbtr", p.debtor)
        if p.requested_execution_date < p.created_at.date():
            v.append(f"ReqdExctnDt {p.requested_execution_date} is before CreDtTm {p.created_at.date()}")
        if not p.transfers:
            v.append("at least one CdtTrfTxInf is required")
        seen: set[str] = set()
        for t in p.transfers:
            where = f"CdtTrfTxInf[{t.end_to_end_id}]"
            SepaRules._id(v, where + "/EndToEndId", t.end_to_end_id)
            if t.end_to_end_id in seen:
                v.append(where + ": duplicate EndToEndId in message")
            seen.add(t.end_to_end_id)
            if t.amount.currency != EUR:
                v.append(f"{where}: SEPA amounts must be EUR, got {t.amount.currency.code}")
            if t.amount.minor_units < 1 or t.amount.minor_units > SepaRules.MAX_AMOUNT_MINOR:
                v.append(f"{where}: amount out of range 0.01..999999999.99: {t.amount}")
            SepaRules._party(v, where + "/Cdtr", t.creditor)
            if t.remittance is not None:
                SepaRules._text(v, where + "/RmtInf/Ustrd", t.remittance, 140)
        return v

    @staticmethod
    def _id(v: list[str], field: str, value: Optional[str]) -> None:
        if value is None or value == "" or len(value) > 35:
            v.append(field + " must be 1..35 characters")
        elif SepaRules.LATIN.fullmatch(value) is None:
            v.append(field + " has characters outside the SEPA set: " + SepaRules.first_outside(value))
        elif value.startswith("/") or value.endswith("/") or "//" in value:
            v.append(field + " must not start or end with '/' or contain '//'")

    @staticmethod
    def _text(v: list[str], field: str, value: Optional[str], max_len: int) -> None:
        if value is None or value == "" or len(value) > max_len:
            v.append(f"{field} must be 1..{max_len} characters")
        elif SepaRules.LATIN.fullmatch(value) is None:
            v.append(field + " has characters outside the SEPA set: " + SepaRules.first_outside(value))

    @staticmethod
    def first_outside(value: str) -> str:
        """"U+00FC at index 1": report the code point, so logs stay ASCII and the culprit is unambiguous."""
        for i, ch in enumerate(value):
            if SepaRules.LATIN.fullmatch(ch) is None:
                return f"U+{ord(ch):04X} at index {i}"
        return "none"

    @staticmethod
    def _party(v: list[str], field: str, party: Party) -> None:
        SepaRules._text(v, field + "/Nm", party.name, 70)
        if not Iban.is_valid(party.iban):
            v.append(f"{field}Acct/IBAN invalid: {party.iban}")
        if party.bic is not None and not Bic.is_valid(party.bic):
            v.append(f"{field}Agt/BICFI invalid: {party.bic}")


class Xml:
    """Secure ElementTree parsing and small helpers shared by both message types."""

    DOCTYPE = re.compile(r"<!DOCTYPE", re.IGNORECASE)

    @staticmethod
    def new_document(namespace: str) -> ET.Element:
        return ET.Element(f"{{{namespace}}}Document")

    @staticmethod
    def local_name(element: ET.Element) -> str:
        tag = element.tag
        return tag[tag.index("}") + 1:] if tag.startswith("{") else tag

    @staticmethod
    def namespace_uri(element: ET.Element) -> Optional[str]:
        tag = element.tag
        return tag[1:tag.index("}")] if tag.startswith("{") else None

    @staticmethod
    def parse_root(xml: str, expected_namespace: str, expected_child: str) -> ET.Element:
        """XXE-safe: ElementTree never fetches external entities, but it would still expand internal ones,
        so any DOCTYPE is rejected before the parser sees it. A payment file is never trusted input."""
        if Xml.DOCTYPE.search(xml):
            raise Iso20022Exception("malformed XML: DOCTYPE is disallowed when processing a payment file")
        try:
            root = ET.fromstring(xml)
        except ET.ParseError as e:
            raise Iso20022Exception(f"malformed XML: {e}") from e
        if Xml.local_name(root) != "Document" or Xml.namespace_uri(root) != expected_namespace:
            raise Iso20022Exception(f"expected Document in {expected_namespace}, got {Xml.namespace_uri(root)}")
        return Xml.child(root, expected_child)

    @staticmethod
    def el(parent: ET.Element, name: str, text: Optional[str] = None) -> ET.Element:
        e = ET.SubElement(parent, f"{{{Xml.namespace_uri(parent)}}}{name}")
        if text is not None:
            e.text = text
        return e

    @staticmethod
    def child(parent: ET.Element, *path: str) -> ET.Element:
        """Walk direct children by local name: child(e, "DbtrAcct", "Id", "IBAN"). Raises if any step is missing."""
        current = parent
        for name in path:
            nxt = None
            for n in current:
                if Xml.local_name(n) == name:
                    nxt = n
                    break
            if nxt is None:
                raise Iso20022Exception(f"missing element {'/'.join(path)} under {Xml.local_name(parent)}")
            current = nxt
        return current

    @staticmethod
    def opt_child(parent: ET.Element, *path: str) -> Optional[ET.Element]:
        try:
            return Xml.child(parent, *path)
        except Iso20022Exception:
            return None

    @staticmethod
    def children(parent: ET.Element, name: str) -> list[ET.Element]:
        return [n for n in parent if Xml.local_name(n) == name]

    @staticmethod
    def text(parent: ET.Element, *path: str) -> str:
        return (Xml.child(parent, *path).text or "").strip()

    @staticmethod
    def opt_text(parent: ET.Element, *path: str) -> Optional[str]:
        e = Xml.opt_child(parent, *path)
        return None if e is None else (e.text or "").strip()

    @staticmethod
    def amount(amount_element: ET.Element) -> Money:
        """"1234.56" with Ccy="EUR" -> Money; rejects a third decimal and a non-EUR currency attribute."""
        ccy = amount_element.get("Ccy", "")
        if ccy != "EUR":
            raise Iso20022Exception(f"unsupported currency {ccy} (SEPA is EUR only)")
        raw = amount_element.text or ""
        try:
            return Money.parse(raw, EUR)
        except (ArithmeticError, ValueError) as e:
            raise Iso20022Exception(f"bad amount {raw}: {e}") from e

    @staticmethod
    def decimal(m: Money) -> str:
        """Money -> "1234.56": the ISO 20022 decimal form, '.' separator, no grouping, no currency in the text."""
        s = str(m)
        return s[:s.index(" ")]

    @staticmethod
    def amount_el(parent: ET.Element, name: str, m: Money) -> None:
        Xml.el(parent, name, Xml.decimal(m)).set("Ccy", m.currency.code)

    @staticmethod
    def serialize(root: ET.Element) -> str:
        """Two-space indentation and a default namespace, the way the Java Transformer prints it.
        ElementTree refuses `default_namespace=` when an attribute such as Ccy is unqualified, so the message
        namespace is registered with the empty prefix instead; that is what makes it `xmlns="urn:..."`."""
        ET.indent(root, space="  ")
        ET.register_namespace("", Xml.namespace_uri(root))
        body = ET.tostring(root, encoding="unicode")
        return '<?xml version="1.0" encoding="UTF-8"?>\n' + body + "\n"

    DATE_TIME = "%Y-%m-%dT%H:%M:%S"  # ISODateTime with seconds, no zone: 2026-10-13T09:30:00

    @staticmethod
    def agent(parent: ET.Element, name: str, bic: str) -> None:
        Xml.el(Xml.el(Xml.el(parent, name), "FinInstnId"), "BICFI", bic)

    @staticmethod
    def account(parent: ET.Element, name: str, iban: str) -> None:
        Xml.el(Xml.el(Xml.el(parent, name), "Id"), "IBAN", iban)


class Pain001:
    """pain.001.001.09: Customer Credit Transfer Initiation, the file a company sends its bank."""

    NS = "urn:iso:std:iso:20022:tech:xsd:pain.001.001.09"

    @staticmethod
    def write(p: PaymentInitiation) -> str:
        violations = SepaRules.violations(p)
        if violations:
            raise Iso20022Exception(f"SEPA rule violations: {violations}")
        root = Xml.new_document(Pain001.NS)
        init = Xml.el(root, "CstmrCdtTrfInitn")

        grp = Xml.el(init, "GrpHdr")
        Xml.el(grp, "MsgId", p.message_id)
        Xml.el(grp, "CreDtTm", p.created_at.strftime(Xml.DATE_TIME))
        Xml.el(grp, "NbOfTxs", str(p.number_of_transactions()))
        Xml.el(grp, "CtrlSum", Xml.decimal(p.control_sum()))
        Xml.el(Xml.el(grp, "InitgPty"), "Nm", p.initiating_party)

        pmt = Xml.el(init, "PmtInf")
        Xml.el(pmt, "PmtInfId", p.payment_info_id)
        Xml.el(pmt, "PmtMtd", "TRF")
        Xml.el(pmt, "NbOfTxs", str(p.number_of_transactions()))
        Xml.el(pmt, "CtrlSum", Xml.decimal(p.control_sum()))
        Xml.el(Xml.el(Xml.el(pmt, "PmtTpInf"), "SvcLvl"), "Cd", "SEPA")
        Xml.el(Xml.el(pmt, "ReqdExctnDt"), "Dt", p.requested_execution_date.isoformat())
        Xml.el(Xml.el(pmt, "Dbtr"), "Nm", p.debtor.name)
        Xml.account(pmt, "DbtrAcct", p.debtor.iban)
        if p.debtor.bic is not None:
            Xml.agent(pmt, "DbtrAgt", p.debtor.bic)
        Xml.el(pmt, "ChrgBr", "SLEV")

        for t in p.transfers:
            tx = Xml.el(pmt, "CdtTrfTxInf")
            Xml.el(Xml.el(tx, "PmtId"), "EndToEndId", t.end_to_end_id)
            Xml.amount_el(Xml.el(tx, "Amt"), "InstdAmt", t.amount)
            if t.creditor.bic is not None:
                Xml.agent(tx, "CdtrAgt", t.creditor.bic)
            Xml.el(Xml.el(tx, "Cdtr"), "Nm", t.creditor.name)
            Xml.account(tx, "CdtrAcct", t.creditor.iban)
            if t.remittance is not None:
                Xml.el(Xml.el(tx, "RmtInf"), "Ustrd", t.remittance)
        return Xml.serialize(root)

    @staticmethod
    def parse(xml: str) -> PaymentInitiation:
        init = Xml.parse_root(xml, Pain001.NS, "CstmrCdtTrfInitn")
        grp = Xml.child(init, "GrpHdr")
        pmts = Xml.children(init, "PmtInf")
        if len(pmts) != 1:
            raise Iso20022Exception(f"this parser handles exactly one PmtInf, found {len(pmts)}")
        pmt = pmts[0]
        if Xml.text(pmt, "PmtMtd") != "TRF":
            raise Iso20022Exception("PmtMtd must be TRF")
        debtor = Party(Xml.text(pmt, "Dbtr", "Nm"), Xml.text(pmt, "DbtrAcct", "Id", "IBAN"),
                       Xml.opt_text(pmt, "DbtrAgt", "FinInstnId", "BICFI"))
        transfers = []
        for tx in Xml.children(pmt, "CdtTrfTxInf"):
            creditor = Party(Xml.text(tx, "Cdtr", "Nm"), Xml.text(tx, "CdtrAcct", "Id", "IBAN"),
                             Xml.opt_text(tx, "CdtrAgt", "FinInstnId", "BICFI"))
            transfers.append(CreditTransfer(Xml.text(tx, "PmtId", "EndToEndId"), Xml.amount(Xml.child(tx, "Amt", "InstdAmt")),
                                            creditor, Xml.opt_text(tx, "RmtInf", "Ustrd")))
        p = PaymentInitiation(Xml.text(grp, "MsgId"), datetime.fromisoformat(Xml.text(grp, "CreDtTm")),
                              Xml.text(grp, "InitgPty", "Nm"), Xml.text(pmt, "PmtInfId"),
                              date.fromisoformat(Xml.text(pmt, "ReqdExctnDt", "Dt")), debtor, tuple(transfers))
        Pain001._check_totals("GrpHdr", grp, p)
        Pain001._check_totals("PmtInf", pmt, p)
        return p

    @staticmethod
    def _check_totals(where: str, header: ET.Element, p: PaymentInitiation) -> None:
        """The declared NbOfTxs and CtrlSum must equal what the message actually contains."""
        declared_count = int(Xml.text(header, "NbOfTxs"))
        if declared_count != p.number_of_transactions():
            raise Iso20022Exception(f"{where} NbOfTxs {declared_count} != {p.number_of_transactions()} transactions")
        ctrl = Xml.opt_text(header, "CtrlSum")
        if ctrl is not None and Money.parse(ctrl, EUR) != p.control_sum():
            raise Iso20022Exception(f"{where} CtrlSum {ctrl} != computed {Xml.decimal(p.control_sum())}")


class Pacs008:
    """pacs.008.001.08: FI to FI Customer Credit Transfer, what the debtor bank sends to the clearing system."""

    NS = "urn:iso:std:iso:20022:tech:xsd:pacs.008.001.08"

    @staticmethod
    def from_pain001(p: PaymentInitiation, message_id: str, created_at: datetime,
                     bic_directory: Callable[[str], Optional[str]]) -> InterbankMessage:
        """Field mapping pain.001 -> pacs.008. EndToEndId passes through unchanged; TxId is new and owned by the
        debtor bank; ReqdExctnDt becomes IntrBkSttlmDt; InstdAmt becomes IntrBkSttlmAmt (same currency in SEPA).
        A missing creditor BIC (IBAN-only initiation) is derived from the IBAN through the bank's directory."""
        out = []
        seq = 1
        for t in p.transfers:
            creditor = t.creditor
            if creditor.bic is None:
                bic = bic_directory(creditor.iban)
                if bic is None:
                    raise Iso20022Exception(f"no BIC for IBAN {creditor.iban} in directory")
                creditor = Party(creditor.name, creditor.iban, bic)
            tx_id = f"{message_id}-{seq:04d}"
            seq += 1
            out.append(InterbankTransfer(t.end_to_end_id, tx_id, t.amount, p.debtor, creditor, t.remittance))
        return InterbankMessage(message_id, created_at, p.requested_execution_date, tuple(out))

    @staticmethod
    def write(m: InterbankMessage) -> str:
        root = Xml.new_document(Pacs008.NS)
        msg = Xml.el(root, "FIToFICstmrCdtTrf")
        grp = Xml.el(msg, "GrpHdr")
        Xml.el(grp, "MsgId", m.message_id)
        Xml.el(grp, "CreDtTm", m.created_at.strftime(Xml.DATE_TIME))
        Xml.el(grp, "NbOfTxs", str(len(m.transfers)))
        Xml.amount_el(grp, "TtlIntrBkSttlmAmt", m.total_settlement_amount())
        Xml.el(grp, "IntrBkSttlmDt", m.settlement_date.isoformat())
        Xml.el(Xml.el(grp, "SttlmInf"), "SttlmMtd", "CLRG")
        for t in m.transfers:
            tx = Xml.el(msg, "CdtTrfTxInf")
            pmt_id = Xml.el(tx, "PmtId")
            Xml.el(pmt_id, "EndToEndId", t.end_to_end_id)
            Xml.el(pmt_id, "TxId", t.transaction_id)
            Xml.el(Xml.el(Xml.el(tx, "PmtTpInf"), "SvcLvl"), "Cd", "SEPA")
            Xml.amount_el(tx, "IntrBkSttlmAmt", t.amount)
            Xml.el(tx, "ChrgBr", "SLEV")
            Xml.el(Xml.el(tx, "Dbtr"), "Nm", t.debtor.name)
            Xml.account(tx, "DbtrAcct", t.debtor.iban)
            Xml.agent(tx, "DbtrAgt", t.debtor.bic)
            Xml.agent(tx, "CdtrAgt", t.creditor.bic)
            Xml.el(Xml.el(tx, "Cdtr"), "Nm", t.creditor.name)
            Xml.account(tx, "CdtrAcct", t.creditor.iban)
            if t.remittance is not None:
                Xml.el(Xml.el(tx, "RmtInf"), "Ustrd", t.remittance)
        return Xml.serialize(root)

    @staticmethod
    def parse(xml: str) -> InterbankMessage:
        msg = Xml.parse_root(xml, Pacs008.NS, "FIToFICstmrCdtTrf")
        grp = Xml.child(msg, "GrpHdr")
        transfers = []
        for tx in Xml.children(msg, "CdtTrfTxInf"):
            debtor = Party(Xml.text(tx, "Dbtr", "Nm"), Xml.text(tx, "DbtrAcct", "Id", "IBAN"),
                           Xml.text(tx, "DbtrAgt", "FinInstnId", "BICFI"))
            creditor = Party(Xml.text(tx, "Cdtr", "Nm"), Xml.text(tx, "CdtrAcct", "Id", "IBAN"),
                             Xml.text(tx, "CdtrAgt", "FinInstnId", "BICFI"))
            transfers.append(InterbankTransfer(Xml.text(tx, "PmtId", "EndToEndId"), Xml.text(tx, "PmtId", "TxId"),
                                               Xml.amount(Xml.child(tx, "IntrBkSttlmAmt")), debtor, creditor,
                                               Xml.opt_text(tx, "RmtInf", "Ustrd")))
        m = InterbankMessage(Xml.text(grp, "MsgId"), datetime.fromisoformat(Xml.text(grp, "CreDtTm")),
                             date.fromisoformat(Xml.text(grp, "IntrBkSttlmDt")), tuple(transfers))
        declared = int(Xml.text(grp, "NbOfTxs"))
        if declared != len(transfers):
            raise Iso20022Exception(f"NbOfTxs {declared} != {len(transfers)} transactions")
        total = Xml.amount(Xml.child(grp, "TtlIntrBkSttlmAmt"))
        if total != m.total_settlement_amount():
            raise Iso20022Exception(f"TtlIntrBkSttlmAmt {total} != computed {m.total_settlement_amount()}")
        return m


NOW = datetime(2026, 10, 13, 9, 30, 0)


def demo_initiation() -> PaymentInitiation:
    debtor = Party("Acme Robotics GmbH", "DE89370400440532013000", "COBADEFFXXX")
    return PaymentInitiation("ACME-20261013-001", NOW, "Acme Robotics GmbH", "PAYRUN-42", date(2026, 10, 14), debtor, (
        CreditTransfer("INV-2026-0912", Money.parse("1250.00", EUR),
                       Party("Fournitures Lyon SARL", "FR1420041010050500013M02606", "PSSTFRPPLYO"), "Invoice 2026-0912"),
        CreditTransfer("INV-2026-0913", Money.parse("349.99", EUR),
                       Party("Delft Sensors BV", "NL91ABNA0417164300", "ABNANL2A"), "Invoice 2026-0913"),
        CreditTransfer("INV-2026-0914", Money.parse("12000.50", EUR),
                       Party("Componentes Bilbao SL", "ES9121000418450200051332", None), "Invoice 2026-0914 batch 7"),
    ))


def demo_directory() -> Callable[[str], Optional[str]]:
    by_bank_code = {"ES2100": "CAIXESBBXXX", "NLABNA": "ABNANL2A", "DE37040044": "COBADEFFXXX"}
    return lambda iban: by_bank_code.get(iban[:2] + Iban.bank_code(iban))


def main() -> None:
    p = demo_initiation()
    print("== SEPA validation ==")
    print(f"violations: {SepaRules.violations(p)}")
    print(f"NbOfTxs {p.number_of_transactions()}, CtrlSum {p.control_sum()}")

    print("\n== pain.001.001.09 (customer -> debtor bank) ==")
    pain = Pain001.write(p)
    print(pain, end="")
    back = Pain001.parse(pain)
    print(f"round trip equal: {str(back == p).lower()}")

    print("\n== pacs.008.001.08 (debtor bank -> clearing) ==")
    m = Pacs008.from_pain001(back, "COBA-20261013-7781", NOW + timedelta(minutes=5), demo_directory())
    pacs = Pacs008.write(m)
    first_tx = pacs.index("</CdtTrfTxInf>") + len("</CdtTrfTxInf>")
    print(pacs[:first_tx])
    print("    ... 2 more CdtTrfTxInf ...")
    parsed = Pacs008.parse(pacs)
    print(f"round trip equal: {str(parsed == m).lower()}")
    for t in parsed.transfers:
        print(f"{t.end_to_end_id} -> TxId {t.transaction_id}, {t.amount} to {t.creditor.bic}")

    print("\n== Rejections ==")
    bad = PaymentInitiation("ACME/BAD//1", NOW, "Acme", "PAYRUN-43", date(2026, 10, 12),
                            Party("Acme Robotics GmbH", "DE89370400440532013001", "COBADEFF"), (
                                CreditTransfer("INV-1", Money.parse("10.00", USD),
                                               Party("Zürich Präzision AG", "NL91ABNA0417164300", None), None),))
    for v in SepaRules.violations(bad):
        print(f"rule: {v}")
    tampered = pain.replace("<CtrlSum>13600.49</CtrlSum>", "<CtrlSum>13600.50</CtrlSum>")
    for action in (lambda: Pain001.parse(tampered),
                   lambda: Pain001.parse(pain.replace("349.99", "349.999")),
                   lambda: Pacs008.parse(pain)):
        try:
            action()
        except Iso20022Exception as e:
            print(f"rejected: {e}")
    xxe = ('<?xml version="1.0"?><!DOCTYPE d [<!ENTITY x SYSTEM "file:///etc/passwd">]><Document xmlns="'
           + Pain001.NS + '">&x;</Document>')
    try:
        Pain001.parse(xxe)
    except Iso20022Exception:
        print("rejected: DOCTYPE (XXE guard)")


if __name__ == "__main__":
    main()
