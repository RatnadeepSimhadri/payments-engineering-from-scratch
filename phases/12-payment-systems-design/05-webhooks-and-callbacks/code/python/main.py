"""Lesson: phases/12-payment-systems-design/05-webhooks-and-callbacks/docs/en.md
Webhooks: Signing, Delivery, Ordering, and Replay
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: RFC 2104 (HMAC); FIPS 180-4 (SHA-256); Stripe "Check the webhook signatures" docs for the
         t=...,v1=... header shape.
Python 3.11+, standard library only (hmac/hashlib for HMAC). Same design as the Java version.
"""

from __future__ import annotations

import hashlib
import hmac
import re
from collections import deque
from dataclasses import dataclass, field
from enum import Enum
from typing import Optional, Protocol


# ---------------------------------------------------------------------------------------------
# Time is injected. The demo and the tests drive a FakeClock in whole seconds; nothing sleeps.
# ---------------------------------------------------------------------------------------------


class Clock(Protocol):
    def now_seconds(self) -> int: ...


class FakeClock:
    def __init__(self, start_seconds: int) -> None:
        self._now = start_seconds

    def now_seconds(self) -> int:
        return self._now

    def advance(self, seconds: int) -> None:
        if seconds < 0:
            raise ValueError("clocks do not run backwards in this lesson")
        self._now += seconds


# ---------------------------------------------------------------------------------------------
# Step 1: the event. The id is the dedupe key; the sequence is the per-object ordering key.
# ---------------------------------------------------------------------------------------------


@dataclass(frozen=True)
class Event:
    id: str
    type: str
    object_id: str
    sequence: int
    created_at: int
    status: str

    def to_json(self) -> str:
        """A fixed-shape JSON document. Keys are always emitted in this order so the bytes are deterministic."""
        return (f'{{"id":"{self.id}","type":"{self.type}","object":"{self.object_id}",'
                f'"sequence":{self.sequence},"created":{self.created_at},"status":"{self.status}"}}')

    @staticmethod
    def from_json(json: str) -> "Event":
        """Parses exactly the shape to_json emits. Not a general JSON parser; the lesson is not about parsing."""
        return Event(_field(json, "id"), _field(json, "type"), _field(json, "object"),
                     int(_field(json, "sequence")), int(_field(json, "created")), _field(json, "status"))


def _field(json: str, key: str) -> str:
    needle = f'"{key}":'
    at = json.find(needle)
    if at < 0:
        raise ValueError(f"missing field {key}")
    start = at + len(needle)
    if json[start] == '"':
        end = json.index('"', start + 1)
        return json[start + 1:end]
    end = start
    while end < len(json) and (json[end].isdigit() or json[end] == "-"):
        end += 1
    return json[start:end]


# ---------------------------------------------------------------------------------------------
# Step 2: signing. HMAC-SHA256 over "<timestamp>.<payload>" with a shared secret (RFC 2104).
# The header shape follows the widely copied Stripe scheme: t=<unix seconds>,v1=<hex mac>.
# ---------------------------------------------------------------------------------------------

HEADER_NAME = "Webhook-Signature"


def signed_payload(timestamp: int, payload: str) -> str:
    """The bytes that are authenticated. Binding the timestamp into the MAC is what makes replay detectable."""
    return f"{timestamp}.{payload}"


def hmac_sha256(key: bytes, message: bytes) -> bytes:
    return hmac.new(key, message, hashlib.sha256).digest()


def hex_of(data: bytes) -> str:
    return data.hex()


def unhex(text: str) -> bytes:
    return bytes.fromhex(text)


class WebhookSigner:
    HEADER_NAME = HEADER_NAME

    def __init__(self, secret: str, clock: Clock) -> None:
        if secret is None or len(secret) < 16:
            raise ValueError("secret must be at least 16 characters")
        self._secret = secret.encode("utf-8")
        self._clock = clock

    signed_payload = staticmethod(signed_payload)
    hmac_sha256 = staticmethod(hmac_sha256)
    hex = staticmethod(hex_of)

    def signature(self, timestamp: int, payload: str) -> str:
        return hex_of(hmac_sha256(self._secret, signed_payload(timestamp, payload).encode("utf-8")))

    def header(self, payload: str) -> str:
        """Header value for a payload signed now."""
        ts = self._clock.now_seconds()
        return f"t={ts},v1={self.signature(ts, payload)}"


# ---------------------------------------------------------------------------------------------
# Step 3: verification. Parse the header, check freshness, recompute, compare in constant time.
# ---------------------------------------------------------------------------------------------


class Verification(Enum):
    OK = "ok"
    MALFORMED_HEADER = "malformed_header"
    STALE_TIMESTAMP = "stale_timestamp"
    BAD_SIGNATURE = "bad_signature"

    def __str__(self) -> str:
        return self.name


@dataclass(frozen=True)
class ParsedHeader:
    timestamp: int
    signatures: tuple[str, ...]


_HEX64 = re.compile(r"[0-9a-f]{64}")
_INT = re.compile(r"[+-]?\d+")


def constant_time_equals(a: bytes, b: bytes) -> bool:
    """Comparison whose running time does not depend on where the first difference is (hmac.compare_digest)."""
    return hmac.compare_digest(a, b)


class WebhookVerifier:
    DEFAULT_TOLERANCE_SECONDS = 300

    def __init__(self, secret: str, clock: Clock, tolerance_seconds: int = DEFAULT_TOLERANCE_SECONDS) -> None:
        self._secret = secret.encode("utf-8")
        self._clock = clock
        self._tolerance_seconds = tolerance_seconds

    @staticmethod
    def parse(header: Optional[str]) -> Optional[ParsedHeader]:
        """Parses "t=123,v1=abc,v1=def" into timestamp and candidate signatures. Returns None when malformed."""
        if header is None or header == "":
            return None
        ts: Optional[int] = None
        sigs: list[str] = []
        parts = header.split(",")
        while parts and parts[-1] == "":  # Java's String.split drops trailing empty strings
            parts.pop()
        for part in parts:
            eq = part.find("=")
            if eq <= 0:
                return None
            k, v = part[:eq].strip(), part[eq + 1:].strip()
            if k == "t":
                if not _INT.fullmatch(v):
                    return None
                ts = int(v)
            elif k == "v1":
                if not _HEX64.fullmatch(v):
                    return None
                sigs.append(v)
            # unknown schemes (v0, v2) are ignored so the producer can rotate schemes without breaking consumers
        if ts is None or not sigs:
            return None
        return ParsedHeader(ts, tuple(sigs))

    constant_time_equals = staticmethod(constant_time_equals)
    unhex = staticmethod(unhex)

    def verify(self, payload: str, header: Optional[str]) -> Verification:
        parsed = WebhookVerifier.parse(header)
        if parsed is None:
            return Verification.MALFORMED_HEADER
        age = self._clock.now_seconds() - parsed.timestamp
        if abs(age) > self._tolerance_seconds:
            return Verification.STALE_TIMESTAMP
        expected = hmac_sha256(self._secret, signed_payload(parsed.timestamp, payload).encode("utf-8"))
        for candidate in parsed.signatures:
            if constant_time_equals(expected, unhex(candidate)):
                return Verification.OK
        return Verification.BAD_SIGNATURE


# ---------------------------------------------------------------------------------------------
# Step 4: delivery. At-least-once with a backoff schedule and a dead letter after the last attempt.
# ---------------------------------------------------------------------------------------------


class Endpoint(Protocol):
    def receive(self, payload: str, headers: dict[str, str]) -> int:
        """Returns an HTTP-style status code. Anything outside 200-299 is a failure to be retried."""
        ...


class FakeEndpoint:
    """An endpoint whose failures are scripted per attempt, so a retry schedule can be tested exactly."""

    def __init__(self) -> None:
        self.received: list[str] = []
        self.received_headers: list[dict[str, str]] = []
        self._scripted_statuses: deque[int] = deque()
        self.calls = 0

    def fail_with(self, *statuses: int) -> "FakeEndpoint":
        """The next calls return these statuses in order; after the script is exhausted every call returns 200."""
        self._scripted_statuses.extend(statuses)
        return self

    def receive(self, payload: str, headers: dict[str, str]) -> int:
        self.calls += 1
        status = self._scripted_statuses.popleft() if self._scripted_statuses else 200
        if 200 <= status < 300:
            self.received.append(payload)
            self.received_headers.append(headers)
        return status


class DeliveryState(Enum):
    PENDING = "pending"
    DELIVERED = "delivered"
    DEAD_LETTERED = "dead_lettered"

    def __str__(self) -> str:
        return self.name


class Delivery:
    def __init__(self, event: Event, first_attempt_at: int) -> None:
        self.event = event
        self.payload = event.to_json()
        self.state = DeliveryState.PENDING
        self.attempts = 0
        self.next_attempt_at = first_attempt_at
        self.statuses: list[int] = []
        self.attempt_times: list[int] = []


class WebhookDispatcher:
    # Seconds to wait after attempt n fails before attempt n+1. Length + 1 = max attempts.
    DEFAULT_BACKOFF_SECONDS: tuple[int, ...] = (30, 120, 600, 3600, 21600)

    def __init__(self, clock: Clock, signer: WebhookSigner, endpoint: Endpoint,
                 backoff_seconds: Optional[tuple[int, ...]] = None) -> None:
        self._clock = clock
        self._signer = signer
        self._endpoint = endpoint
        self._backoff = tuple(backoff_seconds if backoff_seconds is not None else WebhookDispatcher.DEFAULT_BACKOFF_SECONDS)
        self.deliveries: list[Delivery] = []
        self.dead_letters: list[Delivery] = []

    def max_attempts(self) -> int:
        return len(self._backoff) + 1

    def enqueue(self, event: Event) -> Delivery:
        d = Delivery(event, self._clock.now_seconds())
        self.deliveries.append(d)
        return d

    def tick(self) -> int:
        """Attempts every delivery that is due. Signs at send time so the timestamp reflects the retry, not the enqueue."""
        attempted = 0
        for d in self.deliveries:
            if d.state is not DeliveryState.PENDING or d.next_attempt_at > self._clock.now_seconds():
                continue
            attempted += 1
            headers = {
                HEADER_NAME: self._signer.header(d.payload),
                "Webhook-Id": d.event.id,
                "Webhook-Attempt": str(d.attempts + 1),
            }
            status = self._endpoint.receive(d.payload, headers)
            d.attempts += 1
            d.statuses.append(status)
            d.attempt_times.append(self._clock.now_seconds())
            if 200 <= status < 300:
                d.state = DeliveryState.DELIVERED
            elif d.attempts >= self.max_attempts():
                d.state = DeliveryState.DEAD_LETTERED
                self.dead_letters.append(d)
            else:
                d.next_attempt_at = self._clock.now_seconds() + self._backoff[d.attempts - 1]
        return attempted

    def drain(self, fake: FakeClock) -> int:
        """Drives the clock forward to each due time until nothing is pending. Returns the number of ticks that did work."""
        ticks = 0
        while True:
            pending = [d.next_attempt_at for d in self.deliveries if d.state is DeliveryState.PENDING]
            if not pending:
                return ticks
            nxt = min(pending)
            if nxt > fake.now_seconds():
                fake.advance(nxt - fake.now_seconds())
            self.tick()
            ticks += 1


# ---------------------------------------------------------------------------------------------
# Step 5: the consumer. Verify, dedupe by event id, and never let an older event overwrite a newer state.
# ---------------------------------------------------------------------------------------------


class Outcome(Enum):
    ACCEPTED = "accepted"
    DUPLICATE = "duplicate"
    OUT_OF_ORDER = "out_of_order"
    REJECTED = "rejected"

    def __str__(self) -> str:
        return self.name


@dataclass(frozen=True)
class Handled:
    """The HTTP status the endpoint should answer with, alongside what happened."""

    http_status: int
    outcome: Outcome
    verification: Verification


class WebhookConsumer:
    Handled = Handled

    def __init__(self, verifier: WebhookVerifier) -> None:
        self._verifier = verifier
        self._seen_event_ids: set[str] = set()
        self.latest_sequence: dict[str, int] = {}
        self.object_status: dict[str, str] = {}
        self.log: list[str] = []

    def handle(self, payload: str, headers: dict[str, str]) -> Handled:
        v = self._verifier.verify(payload, headers.get(HEADER_NAME))
        if v is not Verification.OK:
            self.log.append(f"rejected {v.name}")
            return Handled(400, Outcome.REJECTED, v)  # 4xx: the producer should not retry a forgery
        e = Event.from_json(payload)
        if e.id in self._seen_event_ids:
            self.log.append(f"duplicate {e.id}")
            return Handled(200, Outcome.DUPLICATE, v)  # 200: tell the producer to stop retrying
        self._seen_event_ids.add(e.id)
        latest = self.latest_sequence.get(e.object_id, -1)
        if e.sequence <= latest:
            self.log.append(f"out-of-order {e.id} seq {e.sequence} <= {latest}")
            return Handled(200, Outcome.OUT_OF_ORDER, v)
        self.latest_sequence[e.object_id] = e.sequence
        self.object_status[e.object_id] = e.status
        self.log.append(f"applied {e.id} -> {e.object_id}={e.status}")
        return Handled(200, Outcome.ACCEPTED, v)


class ConsumerEndpoint:
    """Adapter so a WebhookConsumer can sit behind the dispatcher as an Endpoint."""

    def __init__(self, consumer: WebhookConsumer) -> None:
        self.consumer = consumer

    def receive(self, payload: str, headers: dict[str, str]) -> int:
        return self.consumer.handle(payload, headers).http_status


SECRET = "whsec_demo_secret_do_not_reuse_0001"


def main() -> None:
    clock = FakeClock(1_700_000_000)
    signer = WebhookSigner(SECRET, clock)
    verifier = WebhookVerifier(SECRET, clock)

    print("== Signing and verifying ==")
    authorized = Event("evt_001", "payment.authorized", "pay_42", 1, clock.now_seconds(), "authorized")
    payload = authorized.to_json()
    header = signer.header(payload)
    print(f"payload: {payload}")
    print(f"header:  {header}")
    print(f"verify untouched        -> {verifier.verify(payload, header)}")
    tampered = payload.replace('authorized"}', 'captured"}')
    print(f"verify tampered payload -> {verifier.verify(tampered, header)}")
    print(f"verify wrong secret     -> {WebhookVerifier('whsec_some_other_secret_9999', clock).verify(payload, header)}")
    clock.advance(301)
    print(f"verify 301s later       -> {verifier.verify(payload, header)} (tolerance 300s: replay window closed)")

    print("\n== At-least-once delivery with backoff ==")
    flaky = FakeEndpoint().fail_with(503, 500)
    dispatcher = WebhookDispatcher(clock, signer, flaky)
    t0 = clock.now_seconds()
    d = dispatcher.enqueue(authorized)
    dispatcher.drain(clock)
    print(f"attempts: {d.attempts}, statuses: {d.statuses}, state: {d.state}")
    offsets = [t - t0 for t in d.attempt_times]
    print(f"attempt offsets (s): {offsets}  <- 0, +30, +150")

    print(f"\n== Dead letter after {dispatcher.max_attempts()} attempts ==")
    dead = FakeEndpoint().fail_with(500, 500, 500, 500, 500, 500, 500)
    dispatcher2 = WebhookDispatcher(clock, signer, dead)
    t0 = clock.now_seconds()
    dl = dispatcher2.enqueue(Event("evt_002", "payment.captured", "pay_42", 2, clock.now_seconds(), "captured"))
    dispatcher2.drain(clock)
    offsets = [t - t0 for t in dl.attempt_times]
    print(f"attempt offsets (s): {offsets}")
    print(f"state: {dl.state}, dead letters: {len(dispatcher2.dead_letters)}, "
          f"total wall time: {(clock.now_seconds() - t0) / 3600.0:.2f}h")

    print("\n== Consumer: dedupe and ordering ==")
    consumer = WebhookConsumer(verifier)
    captured = Event("evt_003", "payment.captured", "pay_77", 2, clock.now_seconds(), "captured")
    authorized77 = Event("evt_004", "payment.authorized", "pay_77", 1, clock.now_seconds(), "authorized")
    for e in (captured, captured, authorized77):
        h = {HEADER_NAME: signer.header(e.to_json())}
        r = consumer.handle(e.to_json(), h)
        print(f"{e.id:<8} seq={e.sequence} -> {r.outcome.name:<12} http {r.http_status}")
    print(f"pay_77 status: {consumer.object_status['pay_77']}  (captured, not rolled back to authorized)")
    forged = consumer.handle(captured.to_json(), {HEADER_NAME: f"t={clock.now_seconds()},v1=" + "0" * 64})
    print(f"forged signature -> {forged.outcome} ({forged.verification}) http {forged.http_status}")


if __name__ == "__main__":
    main()
