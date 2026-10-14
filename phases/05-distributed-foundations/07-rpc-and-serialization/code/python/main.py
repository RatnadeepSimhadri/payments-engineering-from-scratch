"""Lesson: phases/05-distributed-foundations/07-rpc-and-serialization/docs/en.md
RPC, Framing, and Serialization Over Raw Sockets
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: RFC 9293 (TCP: byte stream, no message boundaries, half-open connections, section 3.6.2); RFC 8949 (CBOR)
and Protocol Buffers encoding docs. Sockets are simulated by an in-memory byte-stream pair, so every run is deterministic.
"""

from __future__ import annotations

from collections import deque
from dataclasses import dataclass
from enum import Enum
from typing import Optional

MASK64 = (1 << 64) - 1
INT64_MIN, INT64_MAX = -(1 << 63), (1 << 63) - 1


def to_int64(x: int) -> int:
    """Wrap to a signed 64-bit long, as Java arithmetic does."""
    x &= MASK64
    return x - (1 << 64) if x >= (1 << 63) else x


# ---------------------------------------------------------------------------- money (Phase 10, Lesson 01)


@dataclass(frozen=True)
class Currency:
    """ISO 4217 currency: a three-letter code and the number of decimal places in its minor unit."""

    code: str
    exponent: int

    def __post_init__(self) -> None:
        if self.code is None or len(self.code) != 3 or not all(ch.isupper() for ch in self.code):
            raise ValueError(f"currency code must be three uppercase letters: {self.code}")
        if not 0 <= self.exponent <= 4:
            raise ValueError(f"exponent must be 0..4: {self.exponent}")

    def minor_units_per_major(self) -> int:
        return 10 ** self.exponent


USD = Currency("USD", 2)
EUR = Currency("EUR", 2)


@dataclass(frozen=True)
class Money:
    """An exact integer count of minor units in one currency. Never a float."""

    minor_units: int
    currency: Currency

    @staticmethod
    def of_minor(minor_units: int, currency: Currency) -> "Money":
        return Money(minor_units, currency)

    def plus(self, other: "Money") -> "Money":
        if self.currency != other.currency:
            raise ValueError(f"cannot combine {self.currency.code} with {other.currency.code}")
        total = self.minor_units + other.minor_units
        if not INT64_MIN <= total <= INT64_MAX:                 # Math.addExact
            raise OverflowError("long overflow")
        return Money(total, self.currency)

    def __str__(self) -> str:
        scale = self.currency.minor_units_per_major()
        whole, frac = abs(self.minor_units) // scale, abs(self.minor_units) % scale
        sign = "-" if self.minor_units < 0 else ""
        if self.currency.exponent == 0:
            return f"{sign}{whole} {self.currency.code}"
        return f"{sign}{whole}.{str(frac).zfill(self.currency.exponent)} {self.currency.code}"


# ---------------------------------------------------------------------------- deterministic infrastructure


class Rng:
    """SplitMix64: the same stream in every language, so a seed replays everywhere."""

    def __init__(self, seed: int) -> None:
        self._state = seed & MASK64

    def next_long(self) -> int:
        self._state = (self._state + 0x9E3779B97F4A7C15) & MASK64
        z = self._state
        z = ((z ^ (z >> 30)) * 0xBF58476D1CE4E5B9) & MASK64
        z = ((z ^ (z >> 27)) * 0x94D049BB133111EB) & MASK64
        return to_int64(z ^ (z >> 31))

    def next_int(self, bound: int) -> int:
        return ((self.next_long() & MASK64) >> 33) % bound

    def next_double(self) -> float:
        return ((self.next_long() & MASK64) >> 11) * (1.0 / (1 << 53))


class SimClock:
    """A clock the simulation advances by hand. Milliseconds. Nothing in this lesson sleeps."""

    def __init__(self, start: int) -> None:
        self._now = start

    def now(self) -> int:
        return self._now

    def advance(self, millis: int) -> None:
        self._now += millis


# ---------------------------------------------------------------------------- codec


class CodecException(RuntimeError):
    """The bytes do not decode: truncated input, bad tag, negative length. Never leaks as an IndexError."""


class ByteWriter:
    """Appends big-endian fixed-width integers and length-prefixed UTF-8 strings to a growable buffer."""

    def __init__(self) -> None:
        self._buf = bytearray()

    def u8(self, v: int) -> "ByteWriter":
        if not 0 <= v <= 0xFF:
            raise CodecException(f"u8 out of range: {v}")
        self._buf.append(v)
        return self

    def u16(self, v: int) -> "ByteWriter":
        if not 0 <= v <= 0xFFFF:
            raise CodecException(f"u16 out of range: {v}")
        self._buf += v.to_bytes(2, "big")
        return self

    def u32(self, v: int) -> "ByteWriter":
        if not 0 <= v <= 0xFFFFFFFF:
            raise CodecException(f"u32 out of range: {v}")
        self._buf += v.to_bytes(4, "big")
        return self

    def i64(self, v: int) -> "ByteWriter":
        """Two's complement, like (byte) (v >>> shift) in Java; a value outside the long range is a bug, not a wrap."""
        if not INT64_MIN <= v <= INT64_MAX:
            raise CodecException(f"i64 out of range: {v}")
        self._buf += (v & MASK64).to_bytes(8, "big")
        return self

    def string(self, s: str) -> "ByteWriter":
        """u16 byte length followed by UTF-8 bytes. 65535 bytes is the cap."""
        utf8 = s.encode("utf-8")
        self.u16(len(utf8))
        self._buf += utf8
        return self

    def bytes(self, b: bytes) -> "ByteWriter":
        self._buf += b
        return self

    def size(self) -> int:
        return len(self._buf)

    def to_bytes(self) -> bytes:
        return bytes(self._buf)


class ByteReader:
    """Reads what ByteWriter wrote. Every read checks bounds first and fails with CodecException."""

    def __init__(self, buf: bytes) -> None:
        self._buf = buf
        self._pos = 0

    def _need(self, n: int) -> None:
        if self._pos + n > len(self._buf):
            raise CodecException(f"truncated: need {n} bytes at offset {self._pos}, have {len(self._buf) - self._pos}")

    def u8(self) -> int:
        self._need(1)
        v = self._buf[self._pos]
        self._pos += 1
        return v

    def u16(self) -> int:
        self._need(2)
        v = int.from_bytes(self._buf[self._pos:self._pos + 2], "big")
        self._pos += 2
        return v

    def u32(self) -> int:
        self._need(4)
        v = int.from_bytes(self._buf[self._pos:self._pos + 4], "big")
        self._pos += 4
        return v

    def i64(self) -> int:
        self._need(8)
        v = int.from_bytes(self._buf[self._pos:self._pos + 8], "big", signed=True)
        self._pos += 8
        return v

    def string(self) -> str:
        n = self.u16()
        self._need(n)
        s = self._buf[self._pos:self._pos + n].decode("utf-8", errors="replace")   # Java substitutes U+FFFD too
        self._pos += n
        return s

    def remaining(self) -> int:
        return len(self._buf) - self._pos

    def expect_end(self) -> None:
        if self._pos != len(self._buf):
            raise CodecException(f"trailing bytes: {len(self._buf) - self._pos}")


# ---------------------------------------------------------------------------- framing


class Framer:
    """Length-prefixed framing: a 4-byte big-endian payload length, then the payload. The reader is incremental: feed
    it whatever bytes the stream produced and it yields complete frames only."""

    HEADER = 4

    def __init__(self, max_frame: int) -> None:
        self.max_frame = max_frame
        self._pending = b""
        self.frames_out = 0

    @staticmethod
    def frame_with_max(payload: bytes, max_frame: int) -> bytes:
        """Java's static Framer.frame(payload, maxFrame); Python cannot overload a name as static and instance."""
        if len(payload) > max_frame:
            raise CodecException(f"payload {len(payload)} exceeds max frame {max_frame}")
        return ByteWriter().u32(len(payload)).bytes(payload).to_bytes()

    def frame(self, payload: bytes) -> bytes:
        return Framer.frame_with_max(payload, self.max_frame)

    def feed(self, chunk: bytes) -> list[bytes]:
        """Append stream bytes and return every frame now complete. A hostile length raises before allocating."""
        joined = self._pending + chunk
        out: list[bytes] = []
        pos = 0
        while len(joined) - pos >= Framer.HEADER:
            length = ByteReader(joined[pos:pos + Framer.HEADER]).u32()
            if length > self.max_frame:
                raise CodecException(f"frame length {length} exceeds max {self.max_frame}; closing connection")
            if len(joined) - pos - Framer.HEADER < length:
                break                                          # header complete, body not yet
            out.append(joined[pos + Framer.HEADER:pos + Framer.HEADER + length])
            pos += Framer.HEADER + length
        self._pending = joined[pos:]
        self.frames_out += len(out)
        return out

    def buffered(self) -> int:
        return len(self._pending)


# ---------------------------------------------------------------------------- RPC messages


class MessageKind(Enum):
    REQUEST = 1
    RESPONSE = 2
    ERROR = 3

    @property
    def tag(self) -> int:
        return self.value

    @staticmethod
    def from_tag(tag: int) -> "MessageKind":
        for k in MessageKind:
            if k.tag == tag:
                return k
        raise CodecException(f"unknown message kind tag {tag}")


@dataclass(frozen=True)
class RpcMessage:
    """One RPC message. Wire layout: u8 kind, i64 requestId, string method, u32 payloadLength, payload bytes."""

    kind: MessageKind
    request_id: int
    method: str
    payload: bytes

    def encode(self) -> bytes:
        return (ByteWriter().u8(self.kind.tag).i64(self.request_id).string(self.method)
                .u32(len(self.payload)).bytes(self.payload).to_bytes())

    @staticmethod
    def decode(data: bytes) -> "RpcMessage":
        r = ByteReader(data)
        kind = MessageKind.from_tag(r.u8())
        rid = r.i64()
        method = r.string()
        n = r.u32()
        if n > r.remaining():
            raise CodecException(f"payload length {n} exceeds remaining {r.remaining()}")
        payload = bytes(r.u8() for _ in range(n))
        r.expect_end()
        return RpcMessage(kind, rid, method, payload)

    @staticmethod
    def error(request_id: int, method: str, reason: str) -> "RpcMessage":
        return RpcMessage(MessageKind.ERROR, request_id, method, ByteWriter().string(reason).to_bytes())

    def error_reason(self) -> str:
        return ByteReader(self.payload).string()


# ---------------------------------------------------------------------------- the simulated socket pair


@dataclass(frozen=True)
class Segment:
    arrive_at: int
    data: bytes


class Channel:
    """One direction of a connection. Writes are segmented into random-sized chunks that arrive after a random delay,
    which is what TCP does to your messages. cut() makes it half-open: writes are accepted and vanish, reads see nothing."""

    def __init__(self, rng: Rng, max_delay: int, max_segment: int) -> None:
        self._rng = rng
        self._max_delay = max(1, max_delay)
        self._max_segment = max(1, max_segment)
        self._in_flight: deque[Segment] = deque()
        self._cut = False
        self.bytes_written = 0
        self.bytes_delivered = 0
        self.bytes_lost = 0

    def write(self, data: bytes, now: int) -> None:
        self.bytes_written += len(data)
        if self._cut:
            self.bytes_lost += len(data)                       # half-open: the peer is gone and nobody told us
            return
        pos = 0
        arrive = now
        while pos < len(data):
            n = min(len(data) - pos, 1 + self._rng.next_int(self._max_segment))
            arrive += 1 + self._rng.next_int(self._max_delay)  # segments stay ordered
            self._in_flight.append(Segment(arrive, data[pos:pos + n]))
            pos += n

    def read(self, now: int) -> bytes:
        """Everything that has arrived by `now`, concatenated. Empty when nothing has: a non-blocking read."""
        if self._cut:
            return b""
        w = ByteWriter()
        while self._in_flight and self._in_flight[0].arrive_at <= now:
            w.bytes(self._in_flight.popleft().data)
        self.bytes_delivered += w.size()
        return w.to_bytes()

    def cut(self) -> None:
        self._cut = True
        self.bytes_lost += sum(len(s.data) for s in self._in_flight)
        self._in_flight.clear()

    def is_cut(self) -> bool:
        return self._cut

    def pending_segments(self) -> int:
        return len(self._in_flight)


class Link:
    """A connection: two channels. Connection ids show that a reconnect is a new connection, not a repair."""

    _next_id = 1

    def __init__(self, rng: Rng, max_delay: int, max_segment: int) -> None:
        self.connection_id = Link._next_id
        Link._next_id += 1
        self.client_to_server = Channel(rng, max_delay, max_segment)
        self.server_to_client = Channel(rng, max_delay, max_segment)

    def half_open(self) -> None:
        """Simulate the peer vanishing without a FIN or RST: both directions go dark, neither side gets an error."""
        self.client_to_server.cut()
        self.server_to_client.cut()


# ---------------------------------------------------------------------------- the service


class BalanceService:
    """Account balances. Deliberately not idempotent so the retry hazard is visible; Lesson 09 fixes that."""

    def __init__(self) -> None:
        self.balances: dict[str, Money] = {"alice": Money.of_minor(10_000, USD), "bob": Money.of_minor(2_500, USD)}
        self.credits_applied = 0

    def get_balance(self, account: str) -> Money:
        m = self.balances.get(account)
        if m is None:
            raise ValueError(f"no such account: {account}")
        return m

    def credit(self, account: str, amount: Money) -> Money:
        updated = self.get_balance(account).plus(amount)
        self.balances[account] = updated
        self.credits_applied += 1
        return updated

    # -- payload codecs: the service owns its own wire format; the RPC layer sees opaque bytes.

    @staticmethod
    def encode_money(m: Money) -> bytes:
        return ByteWriter().i64(m.minor_units).string(m.currency.code).u8(m.currency.exponent).to_bytes()

    @staticmethod
    def decode_money(r: ByteReader) -> Money:
        minor = r.i64()
        code = r.string()
        exponent = r.u8()
        return Money.of_minor(minor, Currency(code, exponent))

    @staticmethod
    def encode_get_balance(account: str) -> bytes:
        return ByteWriter().string(account).to_bytes()

    @staticmethod
    def encode_credit(account: str, amount: Money) -> bytes:
        return ByteWriter().string(account).bytes(BalanceService.encode_money(amount)).to_bytes()

    def dispatch(self, request: RpcMessage) -> RpcMessage:
        """Dispatch by method name. Unknown methods and bad payloads become ERROR responses, never a dropped connection."""
        try:
            r = ByteReader(request.payload)
            if request.method == "GetBalance":
                account = r.string()
                r.expect_end()
                return RpcMessage(MessageKind.RESPONSE, request.request_id, request.method,
                                  BalanceService.encode_money(self.get_balance(account)))
            if request.method == "Credit":
                account = r.string()
                amount = BalanceService.decode_money(r)
                r.expect_end()
                return RpcMessage(MessageKind.RESPONSE, request.request_id, request.method,
                                  BalanceService.encode_money(self.credit(account, amount)))
            return RpcMessage.error(request.request_id, request.method, f"unknown method {request.method}")
        except (CodecException, ValueError) as e:
            return RpcMessage.error(request.request_id, request.method, str(e))


# ---------------------------------------------------------------------------- server and client


class RpcServer:
    """Reads frames off the connection, dispatches, writes responses. One Framer per connection: the stream has state."""

    def __init__(self, service: BalanceService, max_frame: int) -> None:
        self.service = service
        self._max_frame = max_frame
        self._framers: dict[Link, Framer] = {}
        self.requests_served = 0
        self.malformed_frames = 0
        self.processing_millis = 0          # extra processing time per request; makes the server slow
        self._delayed: deque[tuple[int, Link, RpcMessage]] = deque()

    def accept(self, link: Link) -> None:
        self._framers[link] = Framer(self._max_frame)

    def is_closed(self, link: Link) -> bool:
        return link not in self._framers

    def poll(self, now: int) -> None:
        """One scheduling turn: drain every connection's readable bytes, dispatch complete frames, flush due responses."""
        for link, framer in list(self._framers.items()):
            chunk = link.client_to_server.read(now)
            if not chunk:
                continue
            try:
                frames = framer.feed(chunk)
            except CodecException:
                self.malformed_frames += 1
                del self._framers[link]                        # a corrupt stream cannot be resynchronised: close it
                continue
            for f in frames:
                try:
                    request = RpcMessage.decode(f)
                except CodecException:
                    self.malformed_frames += 1
                    continue
                response = self.service.dispatch(request)
                self.requests_served += 1
                self._delayed.append((now + self.processing_millis, link, response))
        while self._delayed and self._delayed[0][0] <= now:
            _, link, response = self._delayed.popleft()
            if link in self._framers:
                link.server_to_client.write(Framer.frame_with_max(response.encode(), self._max_frame), now)


class RpcTimeoutException(RuntimeError):
    pass


class RpcErrorException(RuntimeError):
    pass


class RpcClient:
    """Assigns request ids, frames and writes the request, then pumps the simulation until the matching response arrives
    or the deadline passes. Responses whose id is not pending are late answers to timed-out calls: counted, not
    delivered. The id counter survives reconnects so a late answer from an old connection can never match a new call."""

    def __init__(self, clock: SimClock, server: RpcServer, rng: Rng, max_delay: int, max_segment: int,
                 max_frame: int) -> None:
        self._clock = clock
        self._server = server
        self._rng = rng
        self._max_delay, self._max_segment, self._max_frame = max_delay, max_segment, max_frame
        self._link: Optional[Link] = None
        self._framer: Optional[Framer] = None
        self._next_request_id = 1
        self._received: dict[int, RpcMessage] = {}
        self.timeouts = 0
        self.late_responses = 0
        self.reconnects = 0
        self.connect()

    def connect(self) -> None:
        self._link = Link(self._rng, self._max_delay, self._max_segment)
        self._framer = Framer(self._max_frame)
        self._server.accept(self._link)

    def reconnect(self) -> None:
        self.reconnects += 1
        self.connect()

    def link(self) -> Link:
        return self._link

    def next_request_id(self) -> int:
        return self._next_request_id

    def send(self, method: str, payload: bytes) -> int:
        """Write a request without waiting. Returns its id so the caller can await it later."""
        rid = self._next_request_id
        self._next_request_id += 1
        self._link.client_to_server.write(
            self._framer.frame(RpcMessage(MessageKind.REQUEST, rid, method, payload).encode()), self._clock.now())
        return rid

    def await_(self, rid: int, timeout_millis: int) -> RpcMessage:
        """Java's await(): pump the world until the response for `rid` arrives or the deadline passes."""
        deadline = self._clock.now() + timeout_millis
        while True:
            self.pump()
            r = self._received.pop(rid, None)
            if r is not None:
                if r.kind is MessageKind.ERROR:
                    raise RpcErrorException(f"{r.method}: {r.error_reason()}")
                return r
            if self._clock.now() >= deadline:
                self.timeouts += 1
                raise RpcTimeoutException(f"request {rid} timed out after {timeout_millis} ms on connection "
                                          f"{self._link.connection_id} (outcome unknown)")
            self._clock.advance(1)

    def call(self, method: str, payload: bytes, timeout_millis: int) -> RpcMessage:
        return self.await_(self.send(method, payload), timeout_millis)

    def pump(self) -> None:
        """Advance the world one step: the server runs, then we read whatever the server-to-client channel delivered."""
        self._server.poll(self._clock.now())
        chunk = self._link.server_to_client.read(self._clock.now())
        if not chunk:
            return
        for f in self._framer.feed(chunk):
            m = RpcMessage.decode(f)
            if m.request_id >= self._next_request_id or m.request_id in self._received:
                raise RuntimeError(f"response for unknown id {m.request_id}")
            self._received[m.request_id] = m
        # Anything buffered for an id that has already timed out is a late response: count and drop it.
        for rid in list(self._received):
            if rid < self._current_call_id():
                self.late_responses += 1
                del self._received[rid]

    def _current_call_id(self) -> int:
        return self._next_request_id - 1

    # -- typed wrappers so callers never touch bytes

    def get_balance(self, account: str, timeout: int) -> Money:
        return BalanceService.decode_money(ByteReader(
            self.call("GetBalance", BalanceService.encode_get_balance(account), timeout).payload))

    def credit(self, account: str, amount: Money, timeout: int) -> Money:
        return BalanceService.decode_money(ByteReader(
            self.call("Credit", BalanceService.encode_credit(account, amount), timeout).payload))


# ---------------------------------------------------------------------------- demo


def hex_(b: bytes) -> str:
    return b.hex()


def main() -> None:
    print("== The wire format: one GetBalance request, framed ==")
    req = RpcMessage(MessageKind.REQUEST, 1, "GetBalance", BalanceService.encode_get_balance("alice"))
    body = req.encode()
    framed = Framer.frame_with_max(body, 1 << 20)
    print(f"payload  ({len(body)} bytes): {hex_(body)}")
    print(f"framed   ({len(framed)} bytes): {hex_(framed)}")
    print(f"  = u32 length {len(body)} | u8 kind=1 | i64 requestId=1 | u16 len=10 'GetBalance' | u32 payloadLen=7"
          " | u16 len=5 'alice'")
    back = RpcMessage.decode(body)
    print(f"decodes back to {back.kind.name} id={back.request_id} method={back.method}")

    print()
    print("== TCP does not respect your message boundaries: 3 frames, segmented into random chunks ==")
    rng = Rng(42)
    ch = Channel(rng, 3, 7)
    reader = Framer(1 << 20)
    for i in range(1, 4):
        ch.write(Framer.frame_with_max(RpcMessage(MessageKind.REQUEST, i, "GetBalance",
                                                  BalanceService.encode_get_balance("bob")).encode(), 1 << 20), 0)
    chunks = frames = 0
    t = 1
    while t <= 200 and frames < 3:
        chunk = ch.read(t)
        if chunk:
            chunks += 1
            got = reader.feed(chunk)
            frames += len(got)
            print(f"t={t:<3} read {len(chunk):2} bytes -> {len(got)} complete frame(s), {reader.buffered():2} bytes buffered")
        t += 1
    print(f"{frames} frames reassembled from {chunks} reads; no frame was ever half-decoded")

    print()
    print("== A hostile length prefix is rejected before any allocation ==")
    try:
        Framer(1 << 20).feed(ByteWriter().u32(0xFFFFFFFF).to_bytes())
    except CodecException as e:
        print(f"rejected: {e}")
    try:
        RpcMessage.decode(body[:len(body) - 3])
    except CodecException as e:
        print(f"truncated message rejected: {e}")

    print()
    print("== Request/response over the simulated socket pair (seed 7, delay 1-5 ms per segment) ==")
    clock = SimClock(0)
    service = BalanceService()
    server = RpcServer(service, 1 << 20)
    client = RpcClient(clock, server, Rng(7), 5, 16, 1 << 20)
    t0 = clock.now()
    alice = client.get_balance("alice", 500)
    print(f"GetBalance(alice) = {alice} in {clock.now() - t0} ms, connection {client.link().connection_id}")
    t0 = clock.now()
    after = client.credit("alice", Money.of_minor(1_999, USD), 500)
    print(f"Credit(alice, 19.99 USD) = {after} in {clock.now() - t0} ms")
    try:
        client.call("Transfer", b"", 500)
    except RpcErrorException as e:
        print(f"server-side error travels back with the same request id: {e}")

    print()
    print("== Timeout: the server is slow, the client gives up, the answer arrives later and is discarded ==")
    server.processing_millis = 300
    t0 = clock.now()
    try:
        client.get_balance("bob", 100)
    except RpcTimeoutException as e:
        print(f"t={clock.now() - t0}: {e}")
    server.processing_millis = 0
    bob = client.get_balance("bob", 1000)
    print(f"next call still gets the right answer: GetBalance(bob) = {bob}; "
          f"late responses discarded so far = {client.late_responses}")

    print()
    print("== Half-open: the server vanishes mid-Credit; the client's socket shows no error, only silence ==")
    credits_before = service.credits_applied
    doomed = client.link()
    in_flight = client.send("Credit", BalanceService.encode_credit("bob", Money.of_minor(5_000, USD)))
    for _ in range(60):                                  # request arrives and is applied...
        clock.advance(1)
        server.poll(clock.now())
    doomed.half_open()                                   # ...and the reply never makes it back
    print(f"server applied the credit (creditsApplied {credits_before} -> {service.credits_applied}), "
          f"bob = {service.get_balance('bob')}")
    print(f"then the link went half-open: {doomed.server_to_client.bytes_lost} reply bytes lost, client sees nothing")
    probe = client.send("GetBalance", BalanceService.encode_get_balance("bob"))   # a write into the void: no error
    print(f"a further write of request {probe} 'succeeds': {doomed.client_to_server.bytes_lost} bytes accepted by the "
          "socket and lost; reads return 0 bytes forever")
    try:
        client.await_(in_flight, 200)
    except RpcTimeoutException as e:
        print(f"client: {e}; only the deadline detects a half-open connection")
    client.reconnect()
    print(f"reconnect -> new connection {client.link().connection_id} "
          f"(ids continue from {client.next_request_id()}, so no old reply can match)")
    retried = client.credit("bob", Money.of_minor(5_000, USD), 500)
    print(f"naive retry of Credit(bob, 50.00 USD) = {retried}  <- applied {service.credits_applied - credits_before}"
          " times for one intended credit. A timeout means UNKNOWN, not FAILED. Lesson 09 (idempotency keys) is the fix.")


if __name__ == "__main__":
    main()
