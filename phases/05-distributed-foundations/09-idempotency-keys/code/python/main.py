"""Lesson: phases/05-distributed-foundations/09-idempotency-keys/docs/en.md
Idempotency Keys: Exactly-Once Effects on At-Least-Once Networks
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: Stripe API reference, "Idempotent requests"; IETF draft-ietf-httpapi-idempotency-key-header;
RFC 9110 section 9.2.2 (idempotent methods). Python 3.11+, standard library only.
Same design as the Java and Go versions: same fingerprint, same outcomes, same status codes.
"""

from __future__ import annotations

import hashlib
import threading
from dataclasses import dataclass
from enum import Enum
from typing import Protocol

DAY_MS = 24 * 60 * 60 * 1000


class Clock(Protocol):
    def now_ms(self) -> int: ...


class FakeClock:
    """A clock the store reads for TTL and lease decisions. No wall time passes."""

    def __init__(self, start_ms: int = 0) -> None:
        self._now = start_ms

    def now_ms(self) -> int:
        return self._now

    def advance(self, ms: int) -> None:
        if ms < 0:
            raise ValueError("cannot advance a negative duration")
        self._now += ms


@dataclass(frozen=True)
class Currency:
    """ISO 4217 currency, copied from Phase 10 Lesson 01."""

    code: str
    exponent: int

    def __post_init__(self) -> None:
        if len(self.code) != 3 or not self.code.isalpha() or not self.code.isupper():
            raise ValueError(f"currency code must be three uppercase letters: {self.code!r}")
        if not 0 <= self.exponent <= 4:
            raise ValueError(f"exponent must be 0..4: {self.exponent}")


USD = Currency("USD", 2)
EUR = Currency("EUR", 2)


@dataclass(frozen=True)
class Money:
    """Integer minor units plus a currency. Never a float."""

    minor_units: int
    currency: Currency

    def __post_init__(self) -> None:
        if not isinstance(self.minor_units, int) or isinstance(self.minor_units, bool):
            raise TypeError("minor_units must be an int; never construct Money from a float")

    @staticmethod
    def of_minor(minor_units: int, currency: Currency) -> "Money":
        return Money(minor_units, currency)

    def __str__(self) -> str:
        if self.currency.exponent == 0:
            return f"{self.minor_units} {self.currency.code}"
        scale = 10 ** self.currency.exponent
        sign = "-" if self.minor_units < 0 else ""
        whole, frac = divmod(abs(self.minor_units), scale)
        return f"{sign}{whole}.{frac:0{self.currency.exponent}d} {self.currency.code}"


def sha256_hex(text: str) -> str:
    return hashlib.sha256(text.encode("utf-8")).hexdigest()


@dataclass(frozen=True)
class ChargeRequest:
    """One POST /v1/charges as the server sees it. The key is chosen by the client; the fingerprint is
    computed by the server from the fields that have an effect."""

    account_id: str
    idempotency_key: str | None
    amount: Money
    card_token: str
    description: str | None = None

    def __post_init__(self) -> None:
        if self.idempotency_key is not None and (not self.idempotency_key.strip() or len(self.idempotency_key) > 255):
            raise ValueError("Idempotency-Key must be 1..255 characters")
        if self.amount.minor_units <= 0:
            raise ValueError("amount must be positive")

    def canonical(self) -> str:
        """Fixed field order, one field per line, so a reordered JSON body hashes the same."""
        return (f"POST /v1/charges\n{self.amount.minor_units}\n{self.amount.currency.code}\n"
                f"{self.card_token}\n{self.description or ''}")

    def fingerprint(self) -> str:
        return sha256_hex(self.canonical())


@dataclass(frozen=True)
class Response:
    """What the client receives. `replayed` is the Idempotent-Replayed: true header a replay carries."""

    status: int
    body: str
    replayed: bool = False

    def as_replay(self) -> "Response":
        return Response(self.status, self.body, True)


class Outcome(Enum):
    NEW = "NEW"                  # no usable entry: execute, then complete() or abandon()
    REPLAY = "REPLAY"            # completed entry, same fingerprint: return its response
    IN_PROGRESS = "IN_PROGRESS"  # another request with this key is executing: 409
    CONFLICT = "CONFLICT"        # same key, different fingerprint: 422


@dataclass(frozen=True)
class Begin:
    outcome: Outcome
    response: Response | None = None


class Entry:
    """One row of the store. IN_PROGRESS until complete() stores a response."""

    __slots__ = ("fingerprint", "started_at_ms", "response", "expires_at_ms")

    def __init__(self, fingerprint: str, started_at_ms: int) -> None:
        self.fingerprint = fingerprint
        self.started_at_ms = started_at_ms
        self.response: Response | None = None
        self.expires_at_ms = 0

    def in_progress(self) -> bool:
        return self.response is None


class IdempotencyStore:
    """Keyed by (account, idempotency key); remembers the fingerprint and, once done, the response to replay.
    One lock: begin() is the atomic check-and-reserve that makes concurrent duplicates safe."""

    def __init__(self, clock: Clock, ttl_ms: int, in_progress_lease_ms: int) -> None:
        if ttl_ms <= 0 or in_progress_lease_ms <= 0:
            raise ValueError("ttl and lease must be positive")
        self.clock = clock
        self.ttl_ms = ttl_ms
        self.in_progress_lease_ms = in_progress_lease_ms
        self._entries: dict[tuple[str, str], Entry] = {}
        self._lock = threading.Lock()

    def begin(self, account_id: str, key: str, fingerprint: str) -> Begin:
        """Atomically: look up the key; decide REPLAY / IN_PROGRESS / CONFLICT for a live entry;
        otherwise reserve the key for this caller and answer NEW."""
        with self._lock:
            now = self.clock.now_ms()
            k = (account_id, key)
            e = self._entries.get(k)
            if e is not None:
                if not e.in_progress() and now >= e.expires_at_ms:
                    del self._entries[k]                                    # TTL passed: forget it
                    e = None
                elif e.in_progress() and now - e.started_at_ms >= self.in_progress_lease_ms:
                    del self._entries[k]                                    # first attempt died: let a retry take over
                    e = None
            if e is not None:
                if e.fingerprint != fingerprint:
                    return Begin(Outcome.CONFLICT)
                if e.in_progress():
                    return Begin(Outcome.IN_PROGRESS)
                assert e.response is not None
                return Begin(Outcome.REPLAY, e.response.as_replay())
            self._entries[k] = Entry(fingerprint, now)
            return Begin(Outcome.NEW)

    def complete(self, account_id: str, key: str, response: Response) -> None:
        """Store the handler's response, whatever its status, and start the TTL."""
        with self._lock:
            e = self._entries.get((account_id, key))
            if e is None or not e.in_progress():
                raise RuntimeError(f"complete without a live reservation: {key}")
            e.response = response
            e.expires_at_ms = self.clock.now_ms() + self.ttl_ms

    def abandon(self, account_id: str, key: str) -> None:
        """The handler crashed before producing a response: release the reservation so a retry can execute."""
        with self._lock:
            e = self._entries.get((account_id, key))
            if e is not None and e.in_progress():
                del self._entries[(account_id, key)]

    def purge_expired(self) -> int:
        """Remove every completed entry whose TTL has passed. Returns how many were removed."""
        with self._lock:
            now = self.clock.now_ms()
            before = len(self._entries)
            self._entries = {k: e for k, e in self._entries.items() if e.in_progress() or now < e.expires_at_ms}
            return before - len(self._entries)

    def size(self) -> int:
        with self._lock:
            return len(self._entries)


class Processor(Protocol):
    """The thing with side effects: the card network. One call here is one movement of money."""

    def charge(self, request: ChargeRequest) -> Response: ...


class FakeProcessor:
    """Deterministic stand-in: succeeds with ch_1, ch_2, ... and can be told to decline, fail, or crash."""

    def __init__(self) -> None:
        self._executions = 0
        self._decline_next = 0
        self._server_error_next = 0
        self._crash_next = 0
        self._lock = threading.Lock()

    def charge(self, request: ChargeRequest) -> Response:
        with self._lock:
            self._executions += 1
            if self._crash_next > 0:
                self._crash_next -= 1
                raise ConnectionError("processor connection reset")
            if self._server_error_next > 0:
                self._server_error_next -= 1
                return Response(502, '{"error":"processor_unavailable"}')
            if self._decline_next > 0:
                self._decline_next -= 1
                return Response(402, '{"error":"card_declined"}')
            return Response(200, f'{{"id":"ch_{self._executions}","amount":"{request.amount}","status":"succeeded"}}')

    def executions(self) -> int:
        with self._lock:
            return self._executions

    def decline_next(self) -> None:
        with self._lock:
            self._decline_next += 1

    def server_error_next(self) -> None:
        with self._lock:
            self._server_error_next += 1

    def crash_next(self) -> None:
        with self._lock:
            self._crash_next += 1


class PaymentsApi:
    """The endpoint. Without a key every call executes. With a key: reserve, execute, store; replay on retry;
    409 while another attempt is running; 422 when the key is reused with a different body."""

    def __init__(self, store: IdempotencyStore, processor: Processor) -> None:
        self.store = store
        self.processor = processor

    def charge(self, request: ChargeRequest) -> Response:
        if request.idempotency_key is None:
            return self.processor.charge(request)
        key = request.idempotency_key
        b = self.store.begin(request.account_id, key, request.fingerprint())
        if b.outcome is Outcome.REPLAY:
            assert b.response is not None
            return b.response
        if b.outcome is Outcome.IN_PROGRESS:
            return Response(409, '{"error":"idempotency_key_in_progress"}')
        if b.outcome is Outcome.CONFLICT:
            return Response(422, '{"error":"idempotency_key_reused_with_different_request"}')
        try:
            response = self.processor.charge(request)
        except Exception:
            self.store.abandon(request.account_id, key)
            raise
        self.store.complete(request.account_id, key, response)
        return response


def main() -> None:
    clock = FakeClock(1_700_000_000_000)
    processor = FakeProcessor()
    store = IdempotencyStore(clock, DAY_MS, 30_000)
    api = PaymentsApi(store, processor)
    req = ChargeRequest("acct_1", "order-8812-attempt", Money.of_minor(1999, USD), "tok_visa_4242", "Order 8812")

    print("== Without a key: a retry is a second charge ==")
    no_key = ChargeRequest("acct_1", None, Money.of_minor(1999, USD), "tok_visa_4242", "Order 8812")
    api.charge(no_key)
    api.charge(no_key)
    print(f"two sends of the same request executed {processor.executions()} charges")

    print("\n== With a key: the retry replays the stored response ==")
    print(f"fingerprint = {req.fingerprint()[:16]}...")
    first = api.charge(req)
    print(f"first:  {first.status} {first.body} replayed={str(first.replayed).lower()}")
    retry = api.charge(req)
    print(f"retry:  {retry.status} {retry.body} replayed={str(retry.replayed).lower()}")
    print(f"processor executions so far: {processor.executions()} (the keyed pair added exactly one)")

    print("\n== Same key, different amount: 422, nothing executes ==")
    tampered = ChargeRequest("acct_1", "order-8812-attempt", Money.of_minor(2999, USD), "tok_visa_4242", "Order 8812")
    conflict = api.charge(tampered)
    print(f"conflict: {conflict.status} {conflict.body}")

    print("\n== Concurrent duplicates: one wins, the other sees 409 until it is done ==")
    fp = req.fingerprint()
    a = store.begin("acct_1", "order-9001", fp)
    b = store.begin("acct_1", "order-9001", fp)
    print(f"request A begin -> {a.outcome.value} ; request B begin -> {b.outcome.value}")
    store.complete("acct_1", "order-9001", Response(200, '{"id":"ch_9001"}'))
    b_retry = store.begin("acct_1", "order-9001", fp)
    assert b_retry.response is not None
    print(f"A completes; B retries -> {b_retry.outcome.value} with body {b_retry.response.body}")

    print("\n== Declines are responses too: they replay ==")
    processor.decline_next()
    declined = ChargeRequest("acct_1", "order-7001", Money.of_minor(5000, USD), "tok_visa_0002")
    d1 = api.charge(declined)
    d2 = api.charge(declined)
    print(f"first {d1.status}, retry {d2.status} replayed={str(d2.replayed).lower()}, executions for this key: 1")

    print("\n== A crash mid-execution releases the reservation; the retry executes ==")
    processor.crash_next()
    crashy = ChargeRequest("acct_1", "order-7002", Money.of_minor(1200, USD), "tok_visa_4242")
    before = processor.executions()
    try:
        api.charge(crashy)
    except ConnectionError as e:
        print(f"first attempt: {e}")
    recovered = api.charge(crashy)
    print(f"retry: {recovered.status}, executions for this key: {processor.executions() - before} "
          "(the hazard: did the first attempt move money?)")

    print("\n== TTL: after 24 h the key is forgotten and a retry executes again ==")
    print(f"entries before purge: {store.size()}")
    clock.advance(DAY_MS)
    print(f"purged {store.purge_expired()} expired entries; entries now: {store.size()}")
    late = api.charge(req)
    print(f"same request after TTL: {late.status} replayed={str(late.replayed).lower()} -> a new charge {late.body}")


if __name__ == "__main__":
    main()
