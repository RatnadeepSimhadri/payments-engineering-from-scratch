"""Lesson: phases/12-payment-systems-design/01-idempotent-payment-api/docs/en.md
Designing an Idempotent Payment API
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: IETF draft-ietf-httpapi-idempotency-key-header; Stripe API reference (Idempotent requests);
         RFC 9110 (HTTP semantics: 409, 422, Retry-After).
Python 3.11+, standard library only. Same design as the Java and Go versions.
"""

from __future__ import annotations

import hashlib
import threading
from dataclasses import dataclass, field
from enum import Enum
from typing import Callable, Optional

# ---------------------------------------------------------------------------------------------
# Money: integer minor units + ISO 4217 currency (Phase 10, Lesson 01). Copied, not imported.
# ---------------------------------------------------------------------------------------------


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


USD = Currency("USD", 2)
EUR = Currency("EUR", 2)
JPY = Currency("JPY", 0)


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
        if not s:
            raise ValueError("empty amount")
        whole, _, frac = s.partition(".")
        whole = whole or "0"
        if not whole.isdigit() or (frac and not frac.isdigit()):
            raise ValueError(f"not a decimal amount: {text!r}")
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

    def is_positive(self) -> bool:
        return self.minor_units > 0

    def __str__(self) -> str:
        magnitude = abs(self.minor_units)
        whole, frac = divmod(magnitude, self.currency.minor_units_per_major())
        text = str(whole)
        if self.currency.exponent > 0:
            text += "." + str(frac).rjust(self.currency.exponent, "0")
        return f"{'-' if self.minor_units < 0 else ''}{text} {self.currency.code}"

    __repr__ = __str__


# ---------------------------------------------------------------------------------------------
# Time is injected.
# ---------------------------------------------------------------------------------------------


class Clock:
    def now_millis(self) -> int:  # pragma: no cover - interface
        raise NotImplementedError


class FakeClock(Clock):
    def __init__(self, start_millis: int) -> None:
        self._now = start_millis
        self._lock = threading.Lock()

    def now_millis(self) -> int:
        with self._lock:
            return self._now

    def advance(self, millis: int) -> None:
        with self._lock:
            self._now += millis


# ---------------------------------------------------------------------------------------------
# The request and its fingerprint.
# ---------------------------------------------------------------------------------------------


def sha256_hex(text: str) -> str:
    return hashlib.sha256(text.encode("utf-8")).hexdigest()


@dataclass(frozen=True)
class PaymentRequest:
    amount: Money
    source: str
    description: str

    def __post_init__(self) -> None:
        if not self.amount.is_positive():
            raise ValueError(f"amount must be positive: {self.amount}")

    def canonical(self) -> str:
        """Fields sorted by name, one `name=value` per line, amount in minor units."""
        fields = {
            "amount": str(self.amount.minor_units),
            "currency": self.amount.currency.code,
            "description": self.description,
            "source": self.source,
        }
        return "".join(f"{k}={fields[k]}\n" for k in sorted(fields))

    def fingerprint(self) -> str:
        return sha256_hex(self.canonical())


REPLAYED = "Idempotent-Replayed"
RETRY_AFTER = "Retry-After"


@dataclass(frozen=True)
class PaymentResponse:
    status: int
    body: str
    headers: dict[str, str] = field(default_factory=dict)

    def with_header(self, name: str, value: str) -> "PaymentResponse":
        return PaymentResponse(self.status, self.body, {**self.headers, name: value})

    def replayed(self) -> bool:
        return self.headers.get(REPLAYED) == "true"

    def retry_after_seconds(self) -> int:
        return int(self.headers.get(RETRY_AFTER, "0"))


# ---------------------------------------------------------------------------------------------
# The processor behind the API.
# ---------------------------------------------------------------------------------------------


@dataclass(frozen=True)
class ProcessorResult:
    reference_id: str
    approved: bool
    code: str
    amount: Money


class SimulatedCrash(RuntimeError):
    def __init__(self) -> None:
        super().__init__("process crashed after the processor committed the charge")


class FakeProcessor:
    def __init__(self) -> None:
        self._charges: dict[str, ProcessorResult] = {}
        self._lock = threading.Lock()
        self.charge_calls = 0
        self._crash_after_next_charge = False
        self.before_commit: Callable[[], None] = lambda: None

    def charge(self, reference_id: str, amount: Money, source: str) -> ProcessorResult:
        with self._lock:
            self.charge_calls += 1
        self.before_commit()
        with self._lock:
            if reference_id in self._charges:
                return self._charges[reference_id]
            approved = not source.startswith("tok_decline")
            result = ProcessorResult(reference_id, approved, "approved" if approved else "card_declined", amount)
            self._charges[reference_id] = result
            if self._crash_after_next_charge:
                self._crash_after_next_charge = False
                raise SimulatedCrash()
            return result

    def lookup(self, reference_id: str) -> Optional[ProcessorResult]:
        with self._lock:
            return self._charges.get(reference_id)

    def charges_stored(self) -> int:
        with self._lock:
            return len(self._charges)

    def crash_after_next_charge(self) -> None:
        self._crash_after_next_charge = True


# ---------------------------------------------------------------------------------------------
# The idempotency store.
# ---------------------------------------------------------------------------------------------


class KeyState(Enum):
    IN_PROGRESS = "in_progress"
    COMPLETED = "completed"


@dataclass(frozen=True)
class IdempotencyRecord:
    key: str
    fingerprint: str
    reference_id: str
    state: KeyState
    response: Optional[PaymentResponse]
    created_at_millis: int
    updated_at_millis: int

    def complete(self, response: PaymentResponse, now: int) -> "IdempotencyRecord":
        return IdempotencyRecord(self.key, self.fingerprint, self.reference_id, KeyState.COMPLETED, response,
                                 self.created_at_millis, now)

    def renew_lease(self, now: int) -> "IdempotencyRecord":
        return IdempotencyRecord(self.key, self.fingerprint, self.reference_id, KeyState.IN_PROGRESS, None,
                                 self.created_at_millis, now)


class Reservation(Enum):
    RESERVED = "reserved"
    RECOVER = "recover"
    IN_PROGRESS = "in_progress"
    COMPLETED = "completed"
    FINGERPRINT_MISMATCH = "fingerprint_mismatch"


@dataclass(frozen=True)
class ReserveOutcome:
    decision: Reservation
    record: IdempotencyRecord


class IdempotencyStore:
    def __init__(self, clock: Clock, ttl_millis: int, lease_millis: int) -> None:
        self._records: dict[str, IdempotencyRecord] = {}
        self._lock = threading.Lock()
        self._clock = clock
        self._ttl_millis = ttl_millis
        self._lease_millis = lease_millis

    def reserve(self, key: str, fingerprint: str, reference_id: str) -> ReserveOutcome:
        """Atomic check-and-reserve: the whole decision happens under one lock."""
        with self._lock:
            now = self._clock.now_millis()
            existing = self._records.get(key)
            if existing is not None and now - existing.created_at_millis >= self._ttl_millis:
                del self._records[key]
                existing = None
            if existing is None:
                fresh = IdempotencyRecord(key, fingerprint, reference_id, KeyState.IN_PROGRESS, None, now, now)
                self._records[key] = fresh
                return ReserveOutcome(Reservation.RESERVED, fresh)
            if existing.fingerprint != fingerprint:
                return ReserveOutcome(Reservation.FINGERPRINT_MISMATCH, existing)
            if existing.state is KeyState.COMPLETED:
                return ReserveOutcome(Reservation.COMPLETED, existing)
            if now - existing.updated_at_millis < self._lease_millis:
                return ReserveOutcome(Reservation.IN_PROGRESS, existing)
            renewed = existing.renew_lease(now)
            self._records[key] = renewed
            return ReserveOutcome(Reservation.RECOVER, renewed)

    def complete(self, key: str, response: PaymentResponse) -> None:
        with self._lock:
            existing = self._records.get(key)
            if existing is None:
                raise RuntimeError(f"complete() without reserve(): {key}")
            self._records[key] = existing.complete(response, self._clock.now_millis())

    def get(self, key: str) -> Optional[IdempotencyRecord]:
        with self._lock:
            return self._records.get(key)

    def size(self) -> int:
        with self._lock:
            return len(self._records)


# ---------------------------------------------------------------------------------------------
# The handler: POST /payments modelled in-process.
# ---------------------------------------------------------------------------------------------

KEY_TTL_MILLIS = 24 * 60 * 60 * 1000
LEASE_MILLIS = 30_000
RETRY_AFTER_SECONDS = 1


KEY_SEPARATOR = "\u001f"  # a control character the key grammar forbids, so scoped keys cannot collide


def scoped_key(merchant_id: str, idempotency_key: str) -> str:
    """Keys are scoped to the merchant (the authenticated caller): merchant + key is the store key."""
    return merchant_id + KEY_SEPARATOR + idempotency_key


def reference_id_for(scoped: str) -> str:
    """Our reference at the processor is a pure function of the scoped key, so a retry can ask about it."""
    return "ref_" + sha256_hex("ref:" + scoped)[:16]


class PaymentsHandler:
    def __init__(self, store: IdempotencyStore, processor: FakeProcessor) -> None:
        self._store = store
        self._processor = processor

    def post(self, merchant_id: Optional[str], idempotency_key: Optional[str], request: PaymentRequest) -> PaymentResponse:
        """POST /payments: the only idempotent operation."""
        if merchant_id is None or not merchant_id.strip():
            return PaymentResponse(401, '{"error":"unauthenticated"}')
        if idempotency_key is None or not idempotency_key.strip():
            return PaymentResponse(400, '{"error":"idempotency_key_required"}')
        if len(idempotency_key) > 255 or any(ord(c) < 32 or ord(c) == 127 for c in idempotency_key):
            return PaymentResponse(400, '{"error":"idempotency_key_invalid"}')
        key = scoped_key(merchant_id, idempotency_key)
        fingerprint = request.fingerprint()
        reference_id = reference_id_for(key)
        outcome = self._store.reserve(key, fingerprint, reference_id)
        if outcome.decision is Reservation.FINGERPRINT_MISMATCH:
            return PaymentResponse(422, '{"error":"idempotency_key_reused_with_different_request"}')
        if outcome.decision is Reservation.IN_PROGRESS:
            return PaymentResponse(409, '{"error":"request_in_progress"}').with_header(RETRY_AFTER, str(RETRY_AFTER_SECONDS))
        if outcome.decision is Reservation.COMPLETED:
            assert outcome.record.response is not None
            return outcome.record.response.with_header(REPLAYED, "true")
        if outcome.decision is Reservation.RECOVER:
            return self._recover(key, request, reference_id)
        return self._execute(key, request, reference_id)

    def get(self, payment_id: str) -> PaymentResponse:
        """GET /payments/{id}: a read. No key, no store record, always the *current* state.
        Replaying a stored response here would hide a capture, refund, or dispute that happened since."""
        current = self._processor.lookup(payment_id)
        if current is None:
            return PaymentResponse(404, '{"error":"not_found"}')
        return PaymentResponse(200, render(current).body)

    def _execute(self, key: str, request: PaymentRequest, reference_id: str) -> PaymentResponse:
        result = self._processor.charge(reference_id, request.amount, request.source)
        response = render(result)
        self._store.complete(key, response)
        return response

    def _recover(self, key: str, request: PaymentRequest, reference_id: str) -> PaymentResponse:
        """The previous owner died between charge() and complete(). Ask before charging again."""
        result = self._processor.lookup(reference_id)
        if result is None:
            result = self._processor.charge(reference_id, request.amount, request.source)
        response = render(result)
        self._store.complete(key, response)
        return response


def render(result: ProcessorResult) -> PaymentResponse:
    status = "succeeded" if result.approved else "failed"
    body = f'{{"id":"{result.reference_id}","status":"{status}","code":"{result.code}","amount":"{result.amount}"}}'
    return PaymentResponse(201 if result.approved else 402, body)


def main() -> None:
    clock = FakeClock(1_700_000_000_000)
    processor = FakeProcessor()
    store = IdempotencyStore(clock, KEY_TTL_MILLIS, LEASE_MILLIS)
    api = PaymentsHandler(store, processor)
    acme = "merchant_acme"

    order = PaymentRequest(Money.parse("19.99", USD), "tok_visa_4242", "Order 1001")
    print("== Fingerprint: SHA-256 of the canonical body ==")
    print(order.canonical(), end="")
    print(f"fingerprint = {order.fingerprint()}")

    print("\n== First call charges; the duplicate is replayed, not re-charged ==")
    first = api.post(acme, "key-1001", order)
    print(f"POST #1 -> {first.status} {first.body} {first.headers}")
    second = api.post(acme, "key-1001", order)
    print(f"POST #2 -> {second.status} {second.body} {second.headers}")
    print(f"processor charge calls = {processor.charge_calls}")

    print("\n== Same key, different body: refuse, do not guess ==")
    changed = PaymentRequest(Money.parse("29.99", USD), "tok_visa_4242", "Order 1001")
    mismatch = api.post(acme, "key-1001", changed)
    print(f"POST #3 -> {mismatch.status} {mismatch.body}")

    print("\n== Same key, different merchant: a different payment ==")
    other = api.post("merchant_globex", "key-1001", order)
    print(f"POST by globex -> {other.status} replayed={other.replayed()}"
          f" ref={reference_id_for(scoped_key('merchant_globex', 'key-1001'))}")
    print(f"processor charge calls = {processor.charge_calls}")

    print("\n== Crash after the processor committed, before we stored the response ==")
    order2 = PaymentRequest(Money.parse("45.00", USD), "tok_visa_4242", "Order 1002")
    processor.crash_after_next_charge()
    try:
        api.post(acme, "key-1002", order2)
    except SimulatedCrash as exc:
        print(f"POST #4 -> {exc}")
    too_soon = api.post(acme, "key-1002", order2)
    print(f"retry at +0s   -> {too_soon.status} Retry-After={too_soon.retry_after_seconds()} (owner lease still live)")
    clock.advance(LEASE_MILLIS)
    recovered = api.post(acme, "key-1002", order2)
    print(f"retry at +30s  -> {recovered.status} {recovered.body}")
    print(f"processor charge calls = {processor.charge_calls}, charges stored = {processor.charges_stored()}"
          f"  (recovery looked up {reference_id_for(scoped_key(acme, 'key-1002'))} instead of charging again)")

    print("\n== Keys expire after 24h ==")
    clock.advance(KEY_TTL_MILLIS)
    after_ttl = api.post(acme, "key-1001", order)
    print(f"POST key-1001 at +24h -> {after_ttl.status} replayed={after_ttl.replayed()}"
          f" (new charge: {processor.charge_calls} calls total)")

    print("\n== Declines are stored and replayed too ==")
    declined = PaymentRequest(Money.parse("10.00", USD), "tok_decline_0002", "Order 1003")
    d1 = api.post(acme, "key-1003", declined)
    d2 = api.post(acme, "key-1003", declined)
    print(f"POST #1 -> {d1.status}, POST #2 -> {d2.status} replayed={d2.replayed()}")

    print("\n== Reads are not idempotent operations: no key, no stored response ==")
    pid = reference_id_for(scoped_key(acme, "key-1002"))
    read = api.get(pid)
    print(f"GET /payments/{pid} -> {read.status} {read.body}")
    print(f"GET /payments/ref_missing -> {api.get('ref_missing').status}; store records = {store.size()} (unchanged by reads)")


if __name__ == "__main__":
    main()
