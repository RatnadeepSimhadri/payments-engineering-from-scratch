"""Lesson: phases/09-reliability-and-scale/07-distributed-tracing/docs/en.md
Distributed Tracing and Context Propagation: trace/span ids, W3C traceparent, parent/child spans, exclusive time
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: W3C Trace Context Level 1 (https://www.w3.org/TR/trace-context/); OpenTelemetry Tracing API spec;
         Sigelman et al., "Dapper, a Large-Scale Distributed Systems Tracing Infrastructure" (Google, 2010).
Python 3.11+, standard library only. Same design as the Java version.
"""

from __future__ import annotations

from abc import ABC, abstractmethod
from dataclasses import dataclass
from typing import Optional


class Clock(ABC):
    """Injected time source. Spans record start and end from it; tests advance it by exact amounts."""

    @abstractmethod
    def now_millis(self) -> int: ...


class FakeClock(Clock):
    def __init__(self, start: int) -> None:
        self._now = start

    def now_millis(self) -> int:
        return self._now

    def advance(self, millis: int) -> None:
        if millis < 0:
            raise ValueError("time does not go backwards")
        self._now += millis


class JavaRandom:
    """java.util.Random, bit for bit (48-bit LCG), so seeded ids match the Java demo exactly."""

    _MULTIPLIER = 0x5DEECE66D
    _ADDEND = 0xB
    _MASK = (1 << 48) - 1

    def __init__(self, seed: int) -> None:
        self._seed = (seed ^ self._MULTIPLIER) & self._MASK

    def next(self, bits: int) -> int:
        self._seed = (self._seed * self._MULTIPLIER + self._ADDEND) & self._MASK
        r = self._seed >> (48 - bits)
        return r - (1 << 32) if r >= (1 << 31) else r

    def next_bytes(self, n: int) -> bytes:
        """Random.nextBytes: each 32-bit draw is consumed least-significant byte first."""
        out = bytearray()
        while len(out) < n:
            rnd = self.next(32) & 0xFFFFFFFF
            for _ in range(min(n - len(out), 4)):
                out.append(rnd & 0xFF)
                rnd >>= 8
        return bytes(out)


class IdGenerator:
    """Seeded id generator: 16-byte trace ids and 8-byte span ids as lowercase hex, never all zeros."""

    def __init__(self, seed: int) -> None:
        self._random = JavaRandom(seed)

    def trace_id(self) -> str:
        return self._hex(16)

    def span_id(self) -> str:
        return self._hex(8)

    def _hex(self, n: int) -> str:
        while True:
            buf = self._random.next_bytes(n)
            if any(buf):
                return buf.hex()


def is_hex(s: Optional[str], length: int) -> bool:
    if s is None or len(s) != length:
        return False
    return all(c in "0123456789abcdef" for c in s)


@dataclass(frozen=True)
class SpanContext:
    """The part of a span that crosses process boundaries. Serialized as the W3C traceparent header:
    version-traceid-parentid-flags, e.g. 00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"""

    trace_id: str
    span_id: str
    sampled: bool

    HEADER = "traceparent"
    STATE_HEADER = "tracestate"

    def __post_init__(self) -> None:
        if not is_hex(self.trace_id, 32) or self.trace_id == "0" * 32:
            raise ValueError(f"bad trace id: {self.trace_id}")
        if not is_hex(self.span_id, 16) or self.span_id == "0" * 16:
            raise ValueError(f"bad span id: {self.span_id}")

    @staticmethod
    def is_hex(s: Optional[str], length: int) -> bool:
        return is_hex(s, length)

    def to_traceparent(self) -> str:
        return f"00-{self.trace_id}-{self.span_id}-{'01' if self.sampled else '00'}"

    @staticmethod
    def parse_traceparent(header: Optional[str]) -> Optional["SpanContext"]:
        """Strict parse per W3C Trace Context Level 1; returns None on any violation so the receiver starts a new trace."""
        if header is None:
            return None
        parts = header.strip().split("-")
        if len(parts) < 4:
            return None
        version = parts[0]
        if not is_hex(version, 2) or version == "ff":
            return None
        if version == "00" and len(parts) != 4:  # version 00 has exactly four fields
            return None
        if not is_hex(parts[1], 32) or parts[1] == "0" * 32:
            return None
        if not is_hex(parts[2], 16) or parts[2] == "0" * 16:
            return None
        if not is_hex(parts[3], 2):
            return None
        sampled = (int(parts[3], 16) & 0x01) == 1
        return SpanContext(parts[1], parts[2], sampled)

    def __str__(self) -> str:
        return f"SpanContext[traceId={self.trace_id}, spanId={self.span_id}, sampled={str(self.sampled).lower()}]"


class Span:
    """One unit of work. Mutable while open; immutable in effect once ended."""

    def __init__(self, trace_id: str, span_id: str, parent_span_id: Optional[str], service: str, name: str,
                 start_millis: int) -> None:
        self.trace_id = trace_id
        self.span_id = span_id
        self.parent_span_id = parent_span_id  # None for a root span
        self.service = service
        self.name = name
        self.start_millis = start_millis
        self.end_millis = -1
        self.attributes: dict[str, str] = {}
        self.status = "OK"

    def is_root(self) -> bool:
        return self.parent_span_id is None

    def ended(self) -> bool:
        return self.end_millis >= 0

    def duration_millis(self) -> int:
        if not self.ended():
            raise RuntimeError(f"span still open: {self.name}")
        return self.end_millis - self.start_millis

    def context(self, sampled: bool) -> SpanContext:
        return SpanContext(self.trace_id, self.span_id, sampled)

    def set_attribute(self, key: str, value: str) -> None:
        self.attributes[key] = value

    def set_error(self, message: str) -> None:
        self.status = "ERROR"
        self.attributes["error.message"] = message


class Collector:
    """Receives finished spans. In production this is the exporter to Jaeger, Tempo, or a vendor; here it is a list."""

    def __init__(self) -> None:
        self._spans: list[Span] = []

    def export(self, span: Span) -> None:
        if not span.ended():
            raise RuntimeError("cannot export an open span")
        self._spans.append(span)

    def spans_for(self, trace_id: str) -> list[Span]:
        return [s for s in self._spans if s.trace_id == trace_id]

    def all(self) -> tuple[Span, ...]:
        return tuple(self._spans)

    def trace_ids(self) -> list[str]:
        """Distinct trace ids in first-seen order (the Java version's LinkedHashSet)."""
        return list(dict.fromkeys(s.trace_id for s in self._spans))

    def size(self) -> int:
        return len(self._spans)


class Tracer:
    """Creates spans in one service. Every service in the demo has its own Tracer sharing one Collector."""

    def __init__(self, service: str, clock: Clock, ids: IdGenerator, collector: Collector) -> None:
        self._service = service
        self._clock = clock
        self._ids = ids
        self._collector = collector

    def start_root(self, name: str) -> Span:
        """Start a root span: a new trace id, no parent."""
        return Span(self._ids.trace_id(), self._ids.span_id(), None, self._service, name, self._clock.now_millis())

    def start_child(self, name: str, parent: SpanContext) -> Span:
        """Start a child of an incoming context: same trace id, parent = the caller's span id."""
        return Span(parent.trace_id, self._ids.span_id(), parent.span_id, self._service, name, self._clock.now_millis())

    def start_from_carrier(self, name: str, carrier: dict[str, str]) -> Span:
        """Start a span from a carrier; if the header is missing or malformed, a new trace begins (and the break is visible)."""
        parent = SpanContext.parse_traceparent(carrier.get(SpanContext.HEADER))
        return self.start_child(name, parent) if parent is not None else self.start_root(name)

    def end(self, span: Span) -> None:
        span.end_millis = self._clock.now_millis()
        self._collector.export(span)


class Propagator:
    """Injects and extracts the W3C headers into a carrier (HTTP headers, Kafka record headers, a queue message's properties)."""

    @staticmethod
    def inject(ctx: SpanContext, tracestate: Optional[str]) -> dict[str, str]:
        carrier = {SpanContext.HEADER: ctx.to_traceparent()}
        if tracestate:
            carrier[SpanContext.STATE_HEADER] = tracestate
        return carrier


class TraceTree:
    """A trace reassembled from its spans: tree structure, exclusive time, orphans, and a text timeline."""

    def __init__(self, trace_id: str, spans: list[Span]) -> None:
        self.trace_id = trace_id
        self.spans = sorted(spans, key=lambda s: (s.start_millis, s.span_id))
        self.by_id: dict[str, Span] = {s.span_id: s for s in self.spans}
        self.children: dict[str, list[Span]] = {}
        self.roots: list[Span] = []
        self.orphans: list[Span] = []
        for s in self.spans:
            if s.is_root():
                self.roots.append(s)
            elif s.parent_span_id in self.by_id:
                self.children.setdefault(s.parent_span_id, []).append(s)
            else:
                self.orphans.append(s)  # parent never arrived: a service dropped the header or its span was lost

    def children_of(self, s: Span) -> list[Span]:
        return self.children.get(s.span_id, [])

    def exclusive_millis(self, s: Span) -> int:
        """Time spent in this span itself, not in any child: duration minus the union of child intervals."""
        kids = sorted(self.children_of(s), key=lambda k: k.start_millis)
        covered = 0
        cursor = s.start_millis
        for k in kids:
            start, end = max(cursor, k.start_millis), min(s.end_millis, k.end_millis)
            if end > start:
                covered += end - start
                cursor = end
        return s.duration_millis() - covered

    def depth(self, s: Span) -> int:
        d = 0
        cur = s
        while cur.parent_span_id is not None and cur.parent_span_id in self.by_id:
            cur = self.by_id[cur.parent_span_id]
            d += 1
        return d

    def hottest(self) -> Optional[Span]:
        """The span with the largest exclusive time: where the trace actually spent its wall clock."""
        best: Optional[Span] = None
        for s in self.spans:
            if best is None or self.exclusive_millis(s) > self.exclusive_millis(best):
                best = s
        return best

    def render_timeline(self, width: int) -> str:
        """Text timeline: one row per span, depth-indented, with a bar scaled to the root's duration."""
        if not self.roots:
            return f"(no root span for trace {self.trace_id})"
        root = self.roots[0]
        origin, total = root.start_millis, max(1, root.duration_millis())
        out = [f"trace {self.trace_id}  total {total} ms\n",
               f"{'service / span':<46} {'start':>7} {'dur':>6}  timeline\n"]
        for s in self.spans:
            if not s.is_root() and s.parent_span_id not in self.by_id:
                continue  # orphans listed separately
            label = "  " * self.depth(s) + s.service + " / " + s.name
            start = (s.start_millis - origin) * width // total
            length = max(1, s.duration_millis() * width // total)
            bar = " " * min(start, width) + "#" * min(length, width - min(start, width))
            flag = "  ERROR" if s.status == "ERROR" else ""
            out.append(f"{label:<46} {s.start_millis - origin:5d} ms {s.duration_millis():4d} ms  {bar}{flag}\n")
        for o in self.orphans:
            out.append(f"ORPHAN {o.service} / {o.name} (parent {o.parent_span_id} never arrived)\n")
        return "".join(out)


class PaymentSystem:
    """The six simulated services of a card payment. Each one extracts the context from the incoming carrier,
    starts a child span, does some clock-advancing "work", and injects a fresh carrier for its downstream calls."""

    SERVICES = ("api-gateway", "payments", "risk", "ledger", "processor-adapter", "notifications")

    def __init__(self, seed: int) -> None:
        self.clock = FakeClock(1_700_000_000_000)
        self.collector = Collector()
        self.ids = IdGenerator(seed)
        self.tracers = {svc: Tracer(svc, self.clock, self.ids, self.collector) for svc in self.SERVICES}
        # fault switch for the demo: the notifications consumer "forgets" to copy the header
        self.notifications_drops_header = False

    def tracer(self, svc: str) -> Tracer:
        return self.tracers[svc]

    def charge(self, incoming: dict[str, str], payment_id: str, amount_minor: int, currency: str) -> str:
        """Entry point: an HTTP request with (possibly) a traceparent from the merchant's client. Returns the trace id."""
        gw = self.tracer("api-gateway")
        root = gw.start_from_carrier("POST /charges", incoming)
        root.set_attribute("payment.id", payment_id)
        root.set_attribute("http.method", "POST")
        self.clock.advance(2)  # auth, parsing
        self.payments(Propagator.inject(root.context(True), incoming.get(SpanContext.STATE_HEADER)),
                      payment_id, amount_minor, currency)
        self.clock.advance(1)  # serialize response
        gw.end(root)
        return root.trace_id

    def payments(self, carrier: dict[str, str], payment_id: str, amount_minor: int, currency: str) -> None:
        t = self.tracer("payments")
        s = t.start_from_carrier("authorize", carrier)
        s.set_attribute("payment.id", payment_id)
        s.set_attribute("amount.minor", str(amount_minor))
        s.set_attribute("currency", currency)
        self.clock.advance(3)  # load merchant config, idempotency check
        decision = self.risk(Propagator.inject(s.context(True), carrier.get(SpanContext.STATE_HEADER)), payment_id)
        s.set_attribute("risk.decision", decision)
        self.clock.advance(1)
        self.ledger(Propagator.inject(s.context(True), None), payment_id, amount_minor)
        self.clock.advance(1)
        self.processor(Propagator.inject(s.context(True), None), payment_id)
        self.clock.advance(2)
        self.notifications(Propagator.inject(s.context(True), None), payment_id)
        self.clock.advance(1)
        t.end(s)

    def risk(self, carrier: dict[str, str], payment_id: str) -> str:
        t = self.tracer("risk")
        s = t.start_from_carrier("score", carrier)
        self.clock.advance(4)  # feature lookup
        model = t.start_child("model.infer", s.context(True))
        self.clock.advance(18)  # the slow part
        t.end(model)
        self.clock.advance(1)
        t.end(s)
        return "APPROVE"

    def ledger(self, carrier: dict[str, str], payment_id: str, amount_minor: int) -> None:
        t = self.tracer("ledger")
        s = t.start_from_carrier("post-hold", carrier)
        s.set_attribute("amount.minor", str(amount_minor))
        self.clock.advance(6)  # write journal entries
        t.end(s)

    def processor(self, carrier: dict[str, str], payment_id: str) -> None:
        t = self.tracer("processor-adapter")
        s = t.start_from_carrier("iso8583.auth", carrier)
        self.clock.advance(2)
        net = t.start_child("network.roundtrip", s.context(True))
        self.clock.advance(45)  # the card network
        t.end(net)
        self.clock.advance(2)
        t.end(s)

    def notifications(self, carrier: dict[str, str], payment_id: str) -> None:
        t = self.tracer("notifications")
        # Header-drop fault: when enabled, the carrier is replaced with an empty one, as if a queue consumer never copied it.
        s = t.start_from_carrier("enqueue-webhook", {} if self.notifications_drops_header else carrier)
        self.clock.advance(3)
        t.end(s)


def main() -> None:
    print("== W3C traceparent round trip ==")
    ctx = SpanContext("4bf92f3577b34da6a3ce929d0e0e4736", "00f067aa0ba902b7", True)
    header = ctx.to_traceparent()
    print(f"header:  {header}")
    print(f"parsed:  {SpanContext.parse_traceparent(header)}")
    bad = SpanContext.parse_traceparent("00-" + "0" * 32 + "-00f067aa0ba902b7-01") is not None
    print(f"bad:     {str(bad).lower()}  (all-zero trace id rejected)")
    print()

    print("== One payment through six services (seed 42) ==")
    sys_ = PaymentSystem(42)
    incoming = {SpanContext.STATE_HEADER: "merchant=acme"}
    trace_id = sys_.charge(incoming, "pay_8f3k2", 1999, "USD")
    tree = TraceTree(trace_id, sys_.collector.spans_for(trace_id))
    print(tree.render_timeline(48), end="")
    print()

    root = tree.roots[0]
    print(f"spans exported: {len(tree.spans)}, root duration: {root.duration_millis()} ms")
    sum_exclusive = sum(tree.exclusive_millis(s) for s in tree.spans)
    print(f"sum of exclusive times: {sum_exclusive} ms  (equals root duration when children are sequential)")
    hot = tree.hottest()
    print(f"hottest span: {hot.service} / {hot.name} with {tree.exclusive_millis(hot)} ms exclusive")
    print()

    print("== The same payment when notifications drops the header (seed 42) ==")
    broken = PaymentSystem(42)
    broken.notifications_drops_header = True
    t2 = broken.charge({}, "pay_8f3k2", 1999, "USD")
    broken_tree = TraceTree(t2, broken.collector.spans_for(t2))
    print(f"spans in trace {t2[:8]}...: {len(broken_tree.spans)} (one fewer)")
    print(f"total traces in collector: {len(broken.collector.trace_ids())} (the notification span became its own root)")
    print()

    print("== Context extracted from a merchant-supplied traceparent ==")
    joined = PaymentSystem(7)
    from_merchant = Propagator.inject(SpanContext("a" * 32, "b" * 16, True), "vendor=xyz")
    t3 = joined.charge(from_merchant, "pay_join1", 500, "EUR")
    gateway = TraceTree(t3, joined.collector.spans_for(t3)).spans[0]
    print(f"gateway span joined trace {t3} with parent {gateway.parent_span_id}")


if __name__ == "__main__":
    main()
