"""Lesson: phases/12-payment-systems-design/10-payment-method-abstraction/docs/en.md
Payment Method Abstraction and Processor Adapters
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: ISO 8583 field 39 response codes; NACHA Operating Rules (return reason codes R01-R85);
         ISO 4217 numeric currency codes; EMVCo 3-D Secure 2 (soft decline).
Python 3.11+, standard library only. Same design as the Java version.
"""

from __future__ import annotations

from dataclasses import dataclass
from enum import Enum
from typing import Callable, Optional, Protocol, Union


# ---------------------------------------------------------------------------------------------
# Money: integer minor units + ISO 4217 currency (Phase 10, Lesson 01). Copied, not imported.
# ---------------------------------------------------------------------------------------------


@dataclass(frozen=True)
class Currency:
    code: str
    exponent: int
    numeric: str

    def __post_init__(self) -> None:
        if len(self.code) != 3 or not self.code.isalpha() or not self.code.isupper():
            raise ValueError(f"currency code must be three uppercase letters: {self.code!r}")
        if not 0 <= self.exponent <= 4:
            raise ValueError(f"exponent must be 0..4: {self.exponent}")

    def minor_units_per_major(self) -> int:
        return 10 ** self.exponent


USD = Currency("USD", 2, "840")
EUR = Currency("EUR", 2, "978")
GBP = Currency("GBP", 2, "826")
JPY = Currency("JPY", 0, "392")
CHF = Currency("CHF", 2, "756")


class CurrencyMismatchError(ValueError):
    def __init__(self, a: Currency, b: Currency) -> None:
        super().__init__(f"cannot combine {a.code} with {b.code}")


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
        whole, _, frac = s.partition(".")
        whole = whole or "0"
        if not whole.isdigit() or (frac and not frac.isdigit()):
            raise ValueError(f"not a decimal amount: {text!r}")
        if len(frac) > currency.exponent:
            raise ArithmeticError(f"{text} has more decimals than {currency.code} allows")
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

    def is_positive(self) -> bool:
        return self.minor_units > 0

    def compare_to(self, other: "Money") -> int:
        self._require_same_currency(other)
        return (self.minor_units > other.minor_units) - (self.minor_units < other.minor_units)

    def to_plain_string(self) -> str:
        """Plain decimal without the currency code, e.g. "45.00" or "1200"."""
        magnitude = abs(self.minor_units)
        whole, frac = divmod(magnitude, self.currency.minor_units_per_major())
        text = str(whole)
        if self.currency.exponent > 0:
            text += "." + str(frac).rjust(self.currency.exponent, "0")
        return ("-" if self.minor_units < 0 else "") + text

    def __str__(self) -> str:
        return f"{self.to_plain_string()} {self.currency.code}"

    __repr__ = __str__


# ---------------------------------------------------------------------------------------------
# The normalized vocabulary. Product code sees only these types, never a vendor's raw codes.
# ---------------------------------------------------------------------------------------------


class MethodType(Enum):
    CARD = "card"
    BANK_DEBIT = "bank_debit"
    WALLET = "wallet"

    def __str__(self) -> str:
        return self.name


@dataclass(frozen=True)
class CardMethod:
    token: str
    network: str

    def type(self) -> MethodType:
        return MethodType.CARD


@dataclass(frozen=True)
class BankDebitMethod:
    token: str
    mandate_id: str

    def type(self) -> MethodType:
        return MethodType.BANK_DEBIT


@dataclass(frozen=True)
class WalletMethod:
    token: str
    provider: str

    def type(self) -> MethodType:
        return MethodType.WALLET


# The sealed interface: exactly these three, matched with isinstance / match-case.
PaymentMethod = Union[CardMethod, BankDebitMethod, WalletMethod]


class Capability(Enum):
    SEPARATE_CAPTURE = 0
    PARTIAL_CAPTURE = 1
    REFUND = 2
    PARTIAL_REFUND = 3
    ASYNC_CONFIRMATION = 4
    CUSTOMER_ACTION = 5

    def __str__(self) -> str:
        return self.name


def format_flags(flags: frozenset[Capability]) -> str:
    """EnumSet prints in declaration order; a frozenset does not, so sort by position."""
    return "[" + ", ".join(c.name for c in sorted(flags, key=lambda c: c.value)) + "]"


class PaymentStatus(Enum):
    AUTHORIZED = "authorized"
    SUCCEEDED = "succeeded"
    PENDING = "pending"
    REQUIRES_ACTION = "requires_action"
    DECLINED = "declined"
    FAILED = "failed"

    def is_final(self) -> bool:
        return self in (PaymentStatus.SUCCEEDED, PaymentStatus.DECLINED, PaymentStatus.FAILED)

    def __str__(self) -> str:
        return self.name


class RetryAdvice(Enum):
    NOT_APPLICABLE = "not_applicable"
    RETRY_NOW = "retry_now"
    RETRY_LATER = "retry_later"
    RETRY_WITH_CUSTOMER = "retry_with_customer"
    DO_NOT_RETRY = "do_not_retry"

    def __str__(self) -> str:
        return self.name


class DeclineReason(Enum):
    NONE = ("none", RetryAdvice.NOT_APPLICABLE)
    INSUFFICIENT_FUNDS = ("insufficient_funds", RetryAdvice.RETRY_LATER)
    DO_NOT_HONOR = ("do_not_honor", RetryAdvice.RETRY_LATER)
    ISSUER_UNAVAILABLE = ("issuer_unavailable", RetryAdvice.RETRY_NOW)
    AUTHENTICATION_REQUIRED = ("authentication_required", RetryAdvice.RETRY_WITH_CUSTOMER)
    EXPIRED_INSTRUMENT = ("expired_instrument", RetryAdvice.DO_NOT_RETRY)
    INVALID_ACCOUNT = ("invalid_account", RetryAdvice.DO_NOT_RETRY)
    SUSPECTED_FRAUD = ("suspected_fraud", RetryAdvice.DO_NOT_RETRY)
    UNAUTHORIZED_DEBIT = ("unauthorized_debit", RetryAdvice.DO_NOT_RETRY)
    INVALID_AMOUNT = ("invalid_amount", RetryAdvice.DO_NOT_RETRY)
    UNKNOWN = ("unknown", RetryAdvice.DO_NOT_RETRY)

    def __init__(self, _label: str, advice: RetryAdvice) -> None:
        self._advice = advice

    def advice(self) -> RetryAdvice:
        return self._advice

    def __str__(self) -> str:
        return self.name


@dataclass(frozen=True)
class ChargeRequest:
    reference: str
    amount: Money
    method: PaymentMethod
    capture: bool

    def __post_init__(self) -> None:
        if not self.amount.is_positive():
            raise ValueError(f"amount must be positive: {self.amount}")


@dataclass(frozen=True)
class PaymentResult:
    """What every adapter returns. raw_code is for logs and support, never for branching."""

    reference: Optional[str]
    processor_ref: str
    status: PaymentStatus
    amount: Money
    reason: DeclineReason
    raw_code: str

    def advice(self) -> RetryAdvice:
        return self.reason.advice()


@dataclass(frozen=True)
class PaymentEvent:
    """An asynchronous outcome (bank return, wallet approval) translated into the same vocabulary."""

    processor_ref: str
    status: PaymentStatus
    reason: DeclineReason
    raw_code: str


class UnsupportedCapabilityError(RuntimeError):
    pass


@dataclass(frozen=True)
class Capabilities:
    flags: frozenset[Capability]
    currencies: frozenset[Currency]

    def __post_init__(self) -> None:
        object.__setattr__(self, "flags", frozenset(self.flags))
        object.__setattr__(self, "currencies", frozenset(self.currencies))

    def supports(self, c: Capability) -> bool:
        return c in self.flags

    def require(self, adapter: str, c: Capability) -> None:
        if not self.supports(c):
            raise UnsupportedCapabilityError(f"{adapter} does not support {c.name}")

    def require_currency(self, adapter: str, c: Currency) -> None:
        if c not in self.currencies:
            raise UnsupportedCapabilityError(f"{adapter} does not support currency {c.code}")


class PaymentProcessor(Protocol):
    """The port. One interface for every rail; adapters implement it, product code depends on it."""

    def name(self) -> str: ...
    def method_type(self) -> MethodType: ...
    def capabilities(self) -> Capabilities: ...
    def charge(self, request: ChargeRequest) -> PaymentResult: ...
    def capture(self, processor_ref: str, amount: Money) -> PaymentResult: ...
    def refund(self, processor_ref: str, amount: Money) -> PaymentResult: ...
    def translate_event(self, raw_event: str) -> Optional[PaymentEvent]: ...


# ---------------------------------------------------------------------------------------------
# Fake vendors. Each has the quirks of its rail; none of them leak past its adapter.
# ---------------------------------------------------------------------------------------------


@dataclass(frozen=True)
class AuthResponse:
    response_code: str
    auth_id: str
    amount_minor: int
    currency_numeric: str


class FakeCardNetworkApi:
    """Card acquirer: ISO 8583-style response codes, ISO 4217 numeric currency, native idempotency keys."""

    _CODES = {
        "tok_insufficient": "51", "tok_do_not_honor": "05", "tok_expired": "54", "tok_fraud": "59",
        "tok_bad_number": "14", "tok_issuer_down": "91", "tok_sca_required": "1A", "tok_weird": "N7",
    }

    def __init__(self) -> None:
        self._by_idempotency_key: dict[str, AuthResponse] = {}
        self._ledger: dict[str, list[int]] = {}  # auth_id -> [authorized, captured, refunded]
        self._vendor_calls = 0
        self._charges_created = 0
        self._last_wire = ""

    def vendor_calls(self) -> int:
        return self._vendor_calls

    def charges_created(self) -> int:
        return self._charges_created

    def last_wire(self) -> str:
        """What went over the wire on the last authorize, for tests that pin the quirk translation."""
        return self._last_wire

    def authorize(self, idempotency_key: str, pan_token: str, amount_minor: int, currency_numeric: str, capture: bool) -> AuthResponse:
        self._vendor_calls += 1
        self._last_wire = f"amount={amount_minor} currency={currency_numeric}"
        prior = self._by_idempotency_key.get(idempotency_key)
        if prior is not None:
            return prior
        self._charges_created += 1
        code = FakeCardNetworkApi._CODES.get(pan_token, "00")
        auth_id = f"auth_{self._charges_created:06d}"
        if code == "00":
            self._ledger[auth_id] = [amount_minor, amount_minor if capture else 0, 0]
        response = AuthResponse(code, auth_id, amount_minor, currency_numeric)
        self._by_idempotency_key[idempotency_key] = response
        return response

    def capture(self, auth_id: str, amount_minor: int) -> str:
        self._vendor_calls += 1
        row = self._ledger.get(auth_id)
        if row is None:
            return "25"  # unable to locate record
        if row[1] + amount_minor > row[0]:
            return "13"  # invalid amount
        row[1] += amount_minor
        return "00"

    def refund(self, auth_id: str, amount_minor: int) -> str:
        self._vendor_calls += 1
        row = self._ledger.get(auth_id)
        if row is None:
            return "25"
        if row[2] + amount_minor > row[1]:
            return "13"
        row[2] += amount_minor
        return "00"


class FakeBankDebitApi:
    """ACH-style bank debit: decimal-string amounts, USD only, no idempotency, outcome arrives days later."""

    _RETURNS = {"acct_nsf": "R01", "acct_closed": "R02", "acct_no_account": "R03", "acct_unauthorized": "R10", "acct_weird": "R99"}

    def __init__(self) -> None:
        self._outbox: list[str] = []
        self._vendor_calls = 0
        self._charges_created = 0
        self._trace_seq = 0
        self._last_wire = ""

    def vendor_calls(self) -> int:
        return self._vendor_calls

    def charges_created(self) -> int:
        return self._charges_created

    def last_wire(self) -> str:
        return self._last_wire

    def submit_debit(self, account_token: str, amount_decimal: str) -> str:
        """Returns a trace number. Submitting the same debit twice creates two debits: the rail has no dedupe."""
        self._vendor_calls += 1
        self._last_wire = f"amount={amount_decimal}"
        self._charges_created += 1
        self._trace_seq += 1
        trace = f"ach_{self._trace_seq:06d}"
        return_code = FakeBankDebitApi._RETURNS.get(account_token)
        self._outbox.append(f"ach|settled|{trace}" if return_code is None else f"ach|returned|{trace}|{return_code}")
        return trace

    def submit_credit(self, original_trace: str, amount_decimal: str) -> str:
        self._vendor_calls += 1
        self._trace_seq += 1
        return f"ach_{self._trace_seq:06d}"

    def drain_events(self) -> list[str]:
        """What the bank's file or webhook delivers later."""
        out = list(self._outbox)
        self._outbox.clear()
        return out


@dataclass(frozen=True)
class WalletOrder:
    id: str
    status: str
    issue: str


class FakeWalletApi:
    """Wallet: lowercase currency, status words instead of codes, payer approval out of band."""

    def __init__(self) -> None:
        self._by_request_id: dict[str, WalletOrder] = {}
        self._ledger: dict[str, list[int]] = {}  # order_id -> [authorized, captured, refunded]
        self._outbox: list[str] = []
        self._vendor_calls = 0
        self._charges_created = 0
        self._last_wire = ""

    def vendor_calls(self) -> int:
        return self._vendor_calls

    def charges_created(self) -> int:
        return self._charges_created

    def last_wire(self) -> str:
        return self._last_wire

    def create_order(self, request_id: str, wallet_token: str, amount_minor: int, currency_lower: str, intent: str) -> WalletOrder:
        self._vendor_calls += 1
        self._last_wire = f"amount={amount_minor} currency={currency_lower} intent={intent}"
        prior = self._by_request_id.get(request_id)
        if prior is not None:
            return prior
        self._charges_created += 1
        oid = f"wal_{self._charges_created:06d}"
        if wallet_token == "wal_nsf":
            order = WalletOrder(oid, "DECLINED", "INSUFFICIENT_FUNDS")
        elif wallet_token == "wal_declined":
            order = WalletOrder(oid, "DECLINED", "INSTRUMENT_DECLINED")
        elif wallet_token == "wal_action":
            order = WalletOrder(oid, "PAYER_ACTION_REQUIRED", "")
        else:
            order = WalletOrder(oid, "COMPLETED" if intent == "CAPTURE" else "APPROVED", "")
        if order.status != "DECLINED":
            captured = amount_minor if intent == "CAPTURE" and order.status == "COMPLETED" else 0
            self._ledger[oid] = [amount_minor, captured, 0]
        self._by_request_id[request_id] = order
        return order

    def capture_order(self, order_id: str, amount_minor: int) -> str:
        """The wallet captures the full authorized amount or nothing."""
        self._vendor_calls += 1
        row = self._ledger.get(order_id)
        if row is None or row[1] != 0 or amount_minor != row[0]:
            return "UNPROCESSABLE_ENTITY"
        row[1] = amount_minor
        return "COMPLETED"

    def refund_capture(self, order_id: str, amount_minor: int) -> str:
        self._vendor_calls += 1
        row = self._ledger.get(order_id)
        if row is None or row[2] + amount_minor > row[1]:
            return "UNPROCESSABLE_ENTITY"
        row[2] += amount_minor
        return "COMPLETED"

    def payer_approves(self, order_id: str) -> None:
        """The payer finishes the out-of-band step in the wallet's own UI."""
        row = self._ledger[order_id]
        row[1] = row[0]
        self._outbox.append(f"wallet:PAYMENT.CAPTURE.COMPLETED:{order_id}")

    def payer_abandons(self, order_id: str) -> None:
        self._outbox.append(f"wallet:PAYMENT.CAPTURE.DENIED:{order_id}")

    def drain_events(self) -> list[str]:
        out = list(self._outbox)
        self._outbox.clear()
        return out


# ---------------------------------------------------------------------------------------------
# Adapters: the only place a vendor's vocabulary is spoken.
# ---------------------------------------------------------------------------------------------


class CardAdapter:
    RESPONSE_CODES: dict[str, DeclineReason] = {
        "51": DeclineReason.INSUFFICIENT_FUNDS,
        "05": DeclineReason.DO_NOT_HONOR,
        "54": DeclineReason.EXPIRED_INSTRUMENT,
        "59": DeclineReason.SUSPECTED_FRAUD,
        "14": DeclineReason.INVALID_ACCOUNT,
        "91": DeclineReason.ISSUER_UNAVAILABLE,
        "1A": DeclineReason.AUTHENTICATION_REQUIRED,
        "13": DeclineReason.INVALID_AMOUNT,
    }

    CAPS = Capabilities(
        frozenset({Capability.SEPARATE_CAPTURE, Capability.PARTIAL_CAPTURE, Capability.REFUND, Capability.PARTIAL_REFUND}),
        frozenset({USD, EUR, GBP, JPY}))

    def __init__(self, api: FakeCardNetworkApi) -> None:
        self._api = api

    def name(self) -> str:
        return "card"

    def method_type(self) -> MethodType:
        return MethodType.CARD

    def capabilities(self) -> Capabilities:
        return CardAdapter.CAPS

    @staticmethod
    def map_code(code: str) -> DeclineReason:
        return CardAdapter.RESPONSE_CODES.get(code, DeclineReason.UNKNOWN)

    def charge(self, request: ChargeRequest) -> PaymentResult:
        match request.method:
            case CardMethod() as card:
                pass
            case _:
                raise ValueError(f"card adapter given {request.method.type().name}")
        CardAdapter.CAPS.require_currency(self.name(), request.amount.currency)
        if not request.capture:
            CardAdapter.CAPS.require(self.name(), Capability.SEPARATE_CAPTURE)
        r = self._api.authorize(request.reference, card.token, request.amount.minor_units,
                                request.amount.currency.numeric, request.capture)
        if r.response_code == "00":
            status = PaymentStatus.SUCCEEDED if request.capture else PaymentStatus.AUTHORIZED
            return PaymentResult(request.reference, r.auth_id, status, request.amount, DeclineReason.NONE, "00")
        reason = CardAdapter.map_code(r.response_code)
        status = PaymentStatus.REQUIRES_ACTION if reason is DeclineReason.AUTHENTICATION_REQUIRED else PaymentStatus.DECLINED
        return PaymentResult(request.reference, r.auth_id, status, request.amount, reason, r.response_code)

    def capture(self, processor_ref: str, amount: Money) -> PaymentResult:
        return self._settle(processor_ref, amount, self._api.capture(processor_ref, amount.minor_units))

    def refund(self, processor_ref: str, amount: Money) -> PaymentResult:
        return self._settle(processor_ref, amount, self._api.refund(processor_ref, amount.minor_units))

    def _settle(self, processor_ref: str, amount: Money, code: str) -> PaymentResult:
        if code == "00":
            return PaymentResult(None, processor_ref, PaymentStatus.SUCCEEDED, amount, DeclineReason.NONE, code)
        return PaymentResult(None, processor_ref, PaymentStatus.DECLINED, amount, CardAdapter.map_code(code), code)

    def translate_event(self, raw_event: str) -> Optional[PaymentEvent]:
        """Card authorizations are synchronous; this rail has no confirmation events to translate."""
        return None


class BankDebitAdapter:
    RETURN_CODES: dict[str, DeclineReason] = {
        "R01": DeclineReason.INSUFFICIENT_FUNDS,
        "R09": DeclineReason.INSUFFICIENT_FUNDS,
        "R02": DeclineReason.INVALID_ACCOUNT,
        "R03": DeclineReason.INVALID_ACCOUNT,
        "R04": DeclineReason.INVALID_ACCOUNT,
        "R16": DeclineReason.DO_NOT_HONOR,
        "R10": DeclineReason.UNAUTHORIZED_DEBIT,
        "R29": DeclineReason.UNAUTHORIZED_DEBIT,
    }

    CAPS = Capabilities(frozenset({Capability.REFUND, Capability.PARTIAL_REFUND, Capability.ASYNC_CONFIRMATION}), frozenset({USD}))

    def __init__(self, api: FakeBankDebitApi) -> None:
        self._api = api
        self._by_reference: dict[str, PaymentResult] = {}  # the rail has no dedupe, so we do
        self._debited: dict[str, Money] = {}
        self._refunded: dict[str, Money] = {}

    def name(self) -> str:
        return "bank_debit"

    def method_type(self) -> MethodType:
        return MethodType.BANK_DEBIT

    def capabilities(self) -> Capabilities:
        return BankDebitAdapter.CAPS

    @staticmethod
    def map_code(code: str) -> DeclineReason:
        return BankDebitAdapter.RETURN_CODES.get(code, DeclineReason.UNKNOWN)

    def charge(self, request: ChargeRequest) -> PaymentResult:
        match request.method:
            case BankDebitMethod() as account:
                pass
            case _:
                raise ValueError(f"bank adapter given {request.method.type().name}")
        BankDebitAdapter.CAPS.require_currency(self.name(), request.amount.currency)
        if not request.capture:
            BankDebitAdapter.CAPS.require(self.name(), Capability.SEPARATE_CAPTURE)
        prior = self._by_reference.get(request.reference)
        if prior is not None:
            return prior
        trace = self._api.submit_debit(account.token, request.amount.to_plain_string())
        result = PaymentResult(request.reference, trace, PaymentStatus.PENDING, request.amount, DeclineReason.NONE, "")
        self._by_reference[request.reference] = result
        self._debited[trace] = request.amount
        return result

    def capture(self, processor_ref: str, amount: Money) -> PaymentResult:
        BankDebitAdapter.CAPS.require(self.name(), Capability.SEPARATE_CAPTURE)
        raise AssertionError("unreachable")

    def refund(self, processor_ref: str, amount: Money) -> PaymentResult:
        BankDebitAdapter.CAPS.require(self.name(), Capability.REFUND)
        original = self._debited.get(processor_ref)
        if original is None:
            raise ValueError(f"unknown debit {processor_ref}")
        already = self._refunded.get(processor_ref, Money.of_minor(0, amount.currency))
        if already.plus(amount).compare_to(original) > 0:
            return PaymentResult(None, processor_ref, PaymentStatus.DECLINED, amount, DeclineReason.INVALID_AMOUNT, "over_refund")
        credit_trace = self._api.submit_credit(processor_ref, amount.to_plain_string())
        self._refunded[processor_ref] = already.plus(amount)
        return PaymentResult(None, credit_trace, PaymentStatus.PENDING, amount, DeclineReason.NONE, "")

    def translate_event(self, raw_event: str) -> Optional[PaymentEvent]:
        parts = raw_event.split("|")
        if len(parts) < 3 or parts[0] != "ach":
            return None
        if parts[1] == "settled":
            return PaymentEvent(parts[2], PaymentStatus.SUCCEEDED, DeclineReason.NONE, "")
        if parts[1] == "returned":
            if len(parts) < 4:
                return None
            return PaymentEvent(parts[2], PaymentStatus.FAILED, BankDebitAdapter.map_code(parts[3]), parts[3])
        return None


class WalletAdapter:
    ISSUES: dict[str, DeclineReason] = {
        "INSUFFICIENT_FUNDS": DeclineReason.INSUFFICIENT_FUNDS,
        "INSTRUMENT_DECLINED": DeclineReason.DO_NOT_HONOR,
        "PAYER_CANNOT_PAY": DeclineReason.DO_NOT_HONOR,
    }

    CAPS = Capabilities(
        frozenset({Capability.SEPARATE_CAPTURE, Capability.REFUND, Capability.PARTIAL_REFUND, Capability.CUSTOMER_ACTION}),
        frozenset({USD, EUR, GBP}))

    _STATUSES = {"COMPLETED": PaymentStatus.SUCCEEDED, "APPROVED": PaymentStatus.AUTHORIZED,
                 "PAYER_ACTION_REQUIRED": PaymentStatus.REQUIRES_ACTION}

    def __init__(self, api: FakeWalletApi) -> None:
        self._api = api
        self._authorized: dict[str, Money] = {}

    def name(self) -> str:
        return "wallet"

    def method_type(self) -> MethodType:
        return MethodType.WALLET

    def capabilities(self) -> Capabilities:
        return WalletAdapter.CAPS

    @staticmethod
    def map_issue(issue: str) -> DeclineReason:
        return WalletAdapter.ISSUES.get(issue, DeclineReason.UNKNOWN)

    def charge(self, request: ChargeRequest) -> PaymentResult:
        match request.method:
            case WalletMethod() as wallet:
                pass
            case _:
                raise ValueError(f"wallet adapter given {request.method.type().name}")
        WalletAdapter.CAPS.require_currency(self.name(), request.amount.currency)
        if not request.capture:
            WalletAdapter.CAPS.require(self.name(), Capability.SEPARATE_CAPTURE)
        o = self._api.create_order(request.reference, wallet.token, request.amount.minor_units,
                                   request.amount.currency.code.lower(), "CAPTURE" if request.capture else "AUTHORIZE")
        status = WalletAdapter._STATUSES.get(o.status, PaymentStatus.DECLINED)
        if status is PaymentStatus.AUTHORIZED:
            self._authorized[o.id] = request.amount
        reason = WalletAdapter.map_issue(o.issue) if status is PaymentStatus.DECLINED else DeclineReason.NONE
        return PaymentResult(request.reference, o.id, status, request.amount, reason, o.status + ("" if not o.issue else "/" + o.issue))

    def capture(self, processor_ref: str, amount: Money) -> PaymentResult:
        WalletAdapter.CAPS.require(self.name(), Capability.SEPARATE_CAPTURE)
        auth = self._authorized.get(processor_ref)
        if auth is not None and amount.compare_to(auth) != 0:
            WalletAdapter.CAPS.require(self.name(), Capability.PARTIAL_CAPTURE)
        status = self._api.capture_order(processor_ref, amount.minor_units)
        if status == "COMPLETED":
            return PaymentResult(None, processor_ref, PaymentStatus.SUCCEEDED, amount, DeclineReason.NONE, status)
        return PaymentResult(None, processor_ref, PaymentStatus.DECLINED, amount, DeclineReason.INVALID_AMOUNT, status)

    def refund(self, processor_ref: str, amount: Money) -> PaymentResult:
        WalletAdapter.CAPS.require(self.name(), Capability.REFUND)
        status = self._api.refund_capture(processor_ref, amount.minor_units)
        if status == "COMPLETED":
            return PaymentResult(None, processor_ref, PaymentStatus.SUCCEEDED, amount, DeclineReason.NONE, status)
        return PaymentResult(None, processor_ref, PaymentStatus.DECLINED, amount, DeclineReason.INVALID_AMOUNT, status)

    def translate_event(self, raw_event: str) -> Optional[PaymentEvent]:
        parts = raw_event.split(":")
        if len(parts) != 3 or parts[0] != "wallet":
            return None
        if parts[1] == "PAYMENT.CAPTURE.COMPLETED":
            return PaymentEvent(parts[2], PaymentStatus.SUCCEEDED, DeclineReason.NONE, parts[1])
        if parts[1] == "PAYMENT.CAPTURE.DENIED":
            return PaymentEvent(parts[2], PaymentStatus.FAILED, DeclineReason.DO_NOT_HONOR, parts[1])
        return None


# ---------------------------------------------------------------------------------------------
# The facade product code calls: route by method type, keep the latest normalized state.
# ---------------------------------------------------------------------------------------------


class PaymentService:
    def __init__(self) -> None:
        self._processors: dict[MethodType, PaymentProcessor] = {}
        self._by_processor_ref: dict[str, PaymentResult] = {}

    def register(self, processor: PaymentProcessor) -> None:
        if processor.method_type() in self._processors:
            raise RuntimeError(f"already registered: {processor.method_type().name}")
        self._processors[processor.method_type()] = processor

    def processor_for(self, type_: MethodType) -> PaymentProcessor:
        p = self._processors.get(type_)
        if p is None:
            raise UnsupportedCapabilityError(f"no processor for {type_.name}")
        return p

    def pay(self, request: ChargeRequest) -> PaymentResult:
        result = self.processor_for(request.method.type()).charge(request)
        self._by_processor_ref[result.processor_ref] = result
        return result

    def on_raw_event(self, type_: MethodType, raw_event: str) -> Optional[PaymentResult]:
        """Feed a raw notification to the adapter for its rail; apply it if the adapter recognizes it."""
        e = self.processor_for(type_).translate_event(raw_event)
        if e is None:
            return None
        current = self._by_processor_ref.get(e.processor_ref)
        if current is None or current.status.is_final():
            return None  # unknown, or already decided
        updated = PaymentResult(current.reference, current.processor_ref, e.status, current.amount, e.reason, e.raw_code)
        self._by_processor_ref[e.processor_ref] = updated
        return updated

    def lookup(self, processor_ref: str) -> Optional[PaymentResult]:
        return self._by_processor_ref.get(processor_ref)


# ---------------------------------------------------------------------------------------------
# The conformance suite: one set of contract checks every adapter must pass.
# ---------------------------------------------------------------------------------------------


@dataclass(frozen=True)
class ConformanceTarget:
    """Everything the suite needs to drive one adapter: its vendor's test tokens and counters."""

    processor: PaymentProcessor
    currency: Currency
    approve: PaymentMethod
    insufficient_funds: PaymentMethod
    drain_events: Callable[[], list[str]]
    vendor_calls: Callable[[], int]
    charges_created: Callable[[], int]


@dataclass(frozen=True)
class ConformanceReport:
    adapter: str
    checks: int
    failures: tuple[str, ...]

    def passed(self) -> bool:
        return not self.failures


class ProcessorConformance:
    def __init__(self, target: ConformanceTarget) -> None:
        self._t = target
        self._events: dict[str, PaymentEvent] = {}
        self._failures: list[str] = []
        self._checks = 0
        self._seq = 0

    @staticmethod
    def run(target: ConformanceTarget) -> ConformanceReport:
        """Runs every contract check once against a fresh target. Empty failures means conformant."""
        c = ProcessorConformance(target)
        c._run_all()
        return ConformanceReport(target.processor.name(), c._checks, tuple(c._failures))

    def _expect(self, name: str, ok: bool) -> None:
        self._checks += 1
        if not ok:
            self._failures.append(f"{self._t.processor.name()}: {name}")

    def _request(self, method: PaymentMethod, amount: str, capture: bool) -> ChargeRequest:
        self._seq += 1
        return ChargeRequest(f"conf-{self._t.processor.name()}-{self._seq}", Money.parse(amount, self._t.currency), method, capture)

    def _final_status(self, r: PaymentResult) -> PaymentStatus:
        """Synchronous result, or the translated event if the adapter said PENDING."""
        if r.status is not PaymentStatus.PENDING:
            return r.status
        for raw in self._t.drain_events():
            e = self._t.processor.translate_event(raw)
            if e is not None:
                self._events[e.processor_ref] = e
        e = self._events.get(r.processor_ref)
        return PaymentStatus.PENDING if e is None else e.status

    def _final_reason(self, r: PaymentResult) -> DeclineReason:
        if r.status is not PaymentStatus.PENDING:
            return r.reason
        self._final_status(r)
        e = self._events.get(r.processor_ref)
        return DeclineReason.NONE if e is None else e.reason

    def _throws_unsupported(self, action: Callable[[], object]) -> tuple[bool, int]:
        """(did it raise UnsupportedCapabilityError, vendor calls made before raising; -1 if it did not raise)."""
        before = self._t.vendor_calls()
        try:
            action()
            return False, -1
        except UnsupportedCapabilityError:
            return True, self._t.vendor_calls() - before

    def _run_all(self) -> None:
        t = self._t
        p = t.processor
        caps = p.capabilities()
        is_async = caps.supports(Capability.ASYNC_CONFIRMATION)

        # 1. Approval: synchronous success, or PENDING only if the adapter declares async confirmation.
        ok = self._request(t.approve, "45.00", True)
        approved = p.charge(ok)
        self._expect("approve returns a processor reference", bool(approved.processor_ref))
        self._expect("approve echoes the exact amount", approved.amount == ok.amount)
        self._expect("approve status is SUCCEEDED, or PENDING iff ASYNC_CONFIRMATION",
                     approved.status is PaymentStatus.PENDING if is_async else approved.status is PaymentStatus.SUCCEEDED)
        self._expect("approve eventually SUCCEEDED", self._final_status(approved) is PaymentStatus.SUCCEEDED)

        # 2. Insufficient funds normalizes to one reason and one retry advice, whatever the rail calls it.
        nsf = p.charge(self._request(t.insufficient_funds, "45.00", True))
        nsf_final = self._final_status(nsf)
        self._expect("insufficient funds ends DECLINED or FAILED", nsf_final in (PaymentStatus.DECLINED, PaymentStatus.FAILED))
        self._expect("insufficient funds maps to INSUFFICIENT_FUNDS", self._final_reason(nsf) is DeclineReason.INSUFFICIENT_FUNDS)
        self._expect("insufficient funds advises RETRY_LATER", self._final_reason(nsf).advice() is RetryAdvice.RETRY_LATER)

        # 3. Idempotency: the same reference twice is one charge at the vendor, one processor reference.
        dup = self._request(t.approve, "12.34", True)
        created_before = t.charges_created()
        first = p.charge(dup)
        second = p.charge(dup)
        self._expect("same reference returns the same processor reference", first.processor_ref == second.processor_ref)
        self._expect("same reference creates exactly one vendor charge", t.charges_created() - created_before == 1)

        # 4. Currency honesty: an undeclared currency is refused before the vendor is called.
        chf = ChargeRequest(f"conf-chf-{p.name()}", Money.parse("10.00", CHF), t.approve, True)
        raised, delta = self._throws_unsupported(lambda: p.charge(chf))
        self._expect("undeclared currency throws UnsupportedCapabilityError", raised)
        self._expect("undeclared currency never reaches the vendor", delta == 0)

        # 5. Separate capture: works if declared, refused before the vendor if not.
        if caps.supports(Capability.SEPARATE_CAPTURE):
            auth = p.charge(self._request(t.approve, "100.00", False))
            self._expect("authorize-only returns AUTHORIZED", auth.status is PaymentStatus.AUTHORIZED)
            partial = Money.parse("60.00", t.currency)
            if caps.supports(Capability.PARTIAL_CAPTURE):
                cap = p.capture(auth.processor_ref, partial)
                self._expect("partial capture SUCCEEDED for the partial amount", cap.status is PaymentStatus.SUCCEEDED and cap.amount == partial)
            else:
                raised, delta = self._throws_unsupported(lambda: p.capture(auth.processor_ref, partial))
                self._expect("undeclared partial capture is refused", raised)
                self._expect("undeclared partial capture never reaches the vendor", delta == 0)
                cap = p.capture(auth.processor_ref, auth.amount)
                self._expect("full capture SUCCEEDED", cap.status is PaymentStatus.SUCCEEDED)
        else:
            raised, delta = self._throws_unsupported(lambda: p.charge(self._request(t.approve, "100.00", False)))
            self._expect("undeclared separate capture is refused", raised)
            self._expect("undeclared separate capture never reaches the vendor", delta == 0)

        # 6. Refunds: a declared partial refund works for exactly the requested amount; over-refund is declined.
        if caps.supports(Capability.PARTIAL_REFUND):
            half = Money.parse("22.50", t.currency)
            refund = p.refund(approved.processor_ref, half)
            self._expect("partial refund is accepted", refund.status in (PaymentStatus.SUCCEEDED, PaymentStatus.PENDING))
            self._expect("partial refund is for the requested amount", refund.amount == half)
            over = p.refund(approved.processor_ref, Money.parse("30.00", t.currency))
            self._expect("refund beyond the captured amount is DECLINED", over.status is PaymentStatus.DECLINED)

        # 7. Events: unknown or foreign notifications are ignored, never thrown on.
        self._expect("unknown event is ignored", p.translate_event("totally|unrelated|payload") is None)
        self._expect("empty event is ignored", p.translate_event("") is None)

        # 8. Declines always carry a reason and the raw code, so support can trace them without product code branching on it.
        self._expect("decline carries a non-NONE reason", nsf.status is PaymentStatus.PENDING or nsf.reason is not DeclineReason.NONE)


# ---------------------------------------------------------------------------------------------
# Demo
# ---------------------------------------------------------------------------------------------


@dataclass(frozen=True)
class Rig:
    service: PaymentService
    card: FakeCardNetworkApi
    bank: FakeBankDebitApi
    wallet: FakeWalletApi

    @staticmethod
    def create() -> "Rig":
        card = FakeCardNetworkApi()
        bank = FakeBankDebitApi()
        wallet = FakeWalletApi()
        service = PaymentService()
        service.register(CardAdapter(card))
        service.register(BankDebitAdapter(bank))
        service.register(WalletAdapter(wallet))
        return Rig(service, card, bank, wallet)


def targets() -> list[ConformanceTarget]:
    card = FakeCardNetworkApi()
    bank = FakeBankDebitApi()
    wallet = FakeWalletApi()
    return [
        ConformanceTarget(CardAdapter(card), USD, CardMethod("tok_visa_4242", "visa"), CardMethod("tok_insufficient", "visa"),
                          lambda: [], card.vendor_calls, card.charges_created),
        ConformanceTarget(BankDebitAdapter(bank), USD, BankDebitMethod("acct_ok", "mandate_1"), BankDebitMethod("acct_nsf", "mandate_2"),
                          bank.drain_events, bank.vendor_calls, bank.charges_created),
        ConformanceTarget(WalletAdapter(wallet), USD, WalletMethod("wal_ok", "wallet"), WalletMethod("wal_nsf", "wallet"),
                          wallet.drain_events, wallet.vendor_calls, wallet.charges_created),
    ]


def show(label: str, r: PaymentResult) -> None:
    print(f"{label:<34} -> {r.status.name:<15} reason={r.reason.name:<24} advice={r.advice().name:<19} "
          f"raw={r.raw_code if r.raw_code else '-'}")


def _require(r: Optional[PaymentResult]) -> PaymentResult:
    if r is None:
        raise RuntimeError("no such payment")
    return r


def main() -> None:
    rig = Rig.create()
    svc = rig.service

    print("== One interface, three rails: the same 45.00 USD charge ==")
    show("card  tok_visa_4242", svc.pay(ChargeRequest("order-1", Money.parse("45.00", USD), CardMethod("tok_visa_4242", "visa"), True)))
    ach = svc.pay(ChargeRequest("order-2", Money.parse("45.00", USD), BankDebitMethod("acct_ok", "mandate_7"), True))
    show("bank  acct_ok", ach)
    show("wallet wal_ok", svc.pay(ChargeRequest("order-3", Money.parse("45.00", USD), WalletMethod("wal_ok", "wallet"), True)))

    print("\n== Three vendors say 'no money' three ways; product code sees one reason ==")
    show("card  ISO 8583 code 51", svc.pay(ChargeRequest("order-4", Money.parse("45.00", USD), CardMethod("tok_insufficient", "visa"), True)))
    ach_nsf = svc.pay(ChargeRequest("order-5", Money.parse("45.00", USD), BankDebitMethod("acct_nsf", "mandate_8"), True))
    show("bank  submitted (outcome later)", ach_nsf)
    for raw in rig.bank.drain_events():
        print(f"  raw bank event: {raw}")
        svc.on_raw_event(MethodType.BANK_DEBIT, raw)
    show("bank  after return file", _require(svc.lookup(ach_nsf.processor_ref)))
    show("bank  order-2 after settlement", _require(svc.lookup(ach.processor_ref)))
    show("wallet INSUFFICIENT_FUNDS", svc.pay(ChargeRequest("order-6", Money.parse("45.00", USD), WalletMethod("wal_nsf", "wallet"), True)))

    print("\n== Customer action: SCA soft decline and wallet approval, one status ==")
    show("card  ISO 8583 code 1A", svc.pay(ChargeRequest("order-7", Money.parse("45.00", USD), CardMethod("tok_sca_required", "visa"), True)))
    action = svc.pay(ChargeRequest("order-8", Money.parse("45.00", USD), WalletMethod("wal_action", "wallet"), True))
    show("wallet PAYER_ACTION_REQUIRED", action)
    rig.wallet.payer_approves(action.processor_ref)
    for raw in rig.wallet.drain_events():
        svc.on_raw_event(MethodType.WALLET, raw)
    show("wallet after payer approves", _require(svc.lookup(action.processor_ref)))

    print("\n== Capability flags refuse before the vendor is called ==")
    before = rig.bank.vendor_calls()
    try:
        svc.pay(ChargeRequest("order-9", Money.parse("45.00", USD), BankDebitMethod("acct_ok", "mandate_9"), False))
    except UnsupportedCapabilityError as e:
        print(f"authorize-only on bank debit: {e} (vendor calls +{rig.bank.vendor_calls() - before})")
    try:
        svc.pay(ChargeRequest("order-10", Money.parse("4500", JPY), WalletMethod("wal_ok", "wallet"), True))
    except UnsupportedCapabilityError as e:
        print(f"JPY on wallet: {e}")
    jpy = svc.pay(ChargeRequest("order-11", Money.parse("4500", JPY), CardMethod("tok_visa_4242", "visa"), True))
    print(f"JPY on card: {jpy.status.name} {jpy.amount} (sent as minor=4500, currency=392)")

    print("\n== Conformance suite, run against every adapter ==")
    for target in targets():
        report = ProcessorConformance.run(target)
        verdict = "PASS" if report.passed() else "FAIL " + "[" + ", ".join(report.failures) + "]"
        print(f"{report.adapter:<10} {report.checks:2d} checks  {format_flags(target.processor.capabilities().flags):<62} {verdict}")


if __name__ == "__main__":
    main()
