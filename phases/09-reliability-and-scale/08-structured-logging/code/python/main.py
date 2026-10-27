"""Lesson: phases/09-reliability-and-scale/08-structured-logging/docs/en.md
Structured Logging and Log Correlation: JSON records, bound context, trace correlation, PAN/secret redaction, sampling, query
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: PCI DSS v4.0 Requirement 3.3/3.4 (PAN rendering and storage); RFC 8259 (JSON); RFC 5424 (syslog severity levels);
         W3C Trace Context; OpenTelemetry Logs Data Model. Python 3.11+, standard library only. Same design as the Java version.
"""

from __future__ import annotations

import re
from abc import ABC, abstractmethod
from enum import Enum
from typing import Optional


class Clock(ABC):
    """Injected time source."""

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


class Level(Enum):
    """Severity in increasing order. Numeric codes follow RFC 5424 so a reader can compare across systems."""

    DEBUG = (0, 7)
    INFO = (1, 6)
    WARN = (2, 4)
    ERROR = (3, 3)

    def __init__(self, ordinal: int, syslog: int) -> None:
        self.ordinal = ordinal
        self.syslog = syslog

    def at_least(self, other: "Level") -> bool:
        return self.ordinal >= other.ordinal

    def __str__(self) -> str:
        return self.name


class Json:
    @staticmethod
    def pair(out: list[str], key: str, value: Optional[str], quote_value: bool) -> list[str]:
        out.append(f'"{Json.escape(key)}":')
        if value is None:
            out.append("null")
        elif quote_value:
            out.append(f'"{Json.escape(value)}"')
        else:
            out.append(value)
        return out

    @staticmethod
    def escape(s: str) -> str:
        out = []
        for c in s:
            if c == '"':
                out.append('\\"')
            elif c == "\\":
                out.append("\\\\")
            elif c == "\n":
                out.append("\\n")
            elif c == "\r":
                out.append("\\r")
            elif c == "\t":
                out.append("\\t")
            elif ord(c) < 0x20:
                out.append(f"\\u{ord(c):04x}")
            else:
                out.append(c)
        return "".join(out)


class LogRecord:
    """One log event. Fields keep insertion order so the serialized form is stable and diffable."""

    def __init__(self, timestamp_millis: int, level: Level, service: str, message: str, fields: dict[str, str]) -> None:
        self.timestamp_millis = timestamp_millis
        self.level = level
        self.service = service
        self.message = message
        self.fields: dict[str, str] = dict(fields)

    def field(self, key: str) -> Optional[str]:
        return self.fields.get(key)

    def to_json(self) -> str:
        """Hand-written JSON (RFC 8259): one line, fixed key order, every string escaped."""
        out: list[str] = ["{"]
        Json.pair(out, "ts", str(self.timestamp_millis), False)
        out.append(",")
        Json.pair(out, "level", self.level.name, True)
        out.append(",")
        Json.pair(out, "service", self.service, True)
        out.append(",")
        Json.pair(out, "msg", self.message, True)
        for k, v in self.fields.items():
            out.append(",")
            Json.pair(out, k, v, True)
        out.append("}")
        return "".join(out)


class Redactor:
    """Removes card numbers and secrets before a record is written. Two rules:
     1. Any field whose key names a secret is replaced wholesale.
     2. Any run of 13-19 digits (allowing spaces or dashes) that passes the Luhn check is masked to first six + last four,
        the maximum PCI DSS permits to be displayed."""

    SECRET_KEYS = frozenset({"password", "secret", "token", "cvv", "cvc", "api_key", "authorization", "pan"})
    _CANDIDATE = re.compile(r"(?<!\d)\d(?:[ -]?\d){12,18}(?!\d)", re.ASCII)

    def __init__(self) -> None:
        self.redactions = 0

    def redact_value(self, key: str, value: Optional[str]) -> Optional[str]:
        if value is None:
            return None
        if key.lower() in self.SECRET_KEYS:
            self.redactions += 1
            return "[REDACTED]"
        return self.mask_pans(value)

    def mask_pans(self, text: str) -> str:
        out = []
        last = 0
        for m in self._CANDIDATE.finditer(text):
            digits = re.sub(r"[ -]", "", m.group())
            if Redactor.luhn_valid(digits):
                out.append(text[last:m.start()])
                out.append(Redactor.mask(digits))
                last = m.end()
                self.redactions += 1
        out.append(text[last:])
        return "".join(out)

    @staticmethod
    def mask(digits: str) -> str:
        return digits[:6] + "*" * (len(digits) - 10) + digits[-4:]

    @staticmethod
    def luhn_valid(digits: str) -> bool:
        """Luhn mod-10 (ISO/IEC 7812-1). Rejects most random digit runs so order ids and timestamps are left alone."""
        total = 0
        dbl = False
        for ch in reversed(digits):
            d = ord(ch) - ord("0")
            if dbl:
                d *= 2
                if d > 9:
                    d -= 9
            total += d
            dbl = not dbl
        return total % 10 == 0


def to_int32(x: int) -> int:
    x &= 0xFFFFFFFF
    return x - (1 << 32) if x >= 1 << 31 else x


class Sampler:
    """Keeps or drops a record. ERROR and WARN are always kept. DEBUG and INFO are kept for 1 in ``rate`` traces, decided by
    hashing the trace id, so a trace is logged completely or not at all. Records without a trace id use the message hash."""

    def __init__(self, debug_rate: int, info_rate: int) -> None:
        if debug_rate < 1 or info_rate < 1:
            raise ValueError("rates are 1-in-N, N >= 1")
        self._debug_rate = debug_rate
        self._info_rate = info_rate
        self.dropped = 0

    def keep(self, level: Level, trace_id: Optional[str], message: str) -> bool:
        if level.at_least(Level.WARN):
            return True
        rate = self._debug_rate if level == Level.DEBUG else self._info_rate
        if rate == 1:
            return True
        key = trace_id if trace_id is not None else message
        keep = Sampler.fnv1a(key) % rate == 0  # Python % floors, like Math.floorMod
        if not keep:
            self.dropped += 1
        return keep

    @staticmethod
    def fnv1a(s: str) -> int:
        """FNV-1a over UTF-16 code units, wrapped to a signed 32-bit int exactly as Java's int arithmetic does."""
        h = 0x811C9DC5
        for c in s:
            h ^= ord(c)
            h = (h * 0x01000193) & 0xFFFFFFFF
        return to_int32(h)


class LogStore:
    """In-memory destination with an index by field value: the part of a log platform the lesson needs."""

    def __init__(self, indexed_fields: frozenset[str] | set[str]) -> None:
        self._records: list[LogRecord] = []
        self._index: dict[str, dict[str, list[LogRecord]]] = {}  # field -> value -> records
        self._indexed = indexed_fields

    def write(self, r: LogRecord) -> None:
        self._records.append(r)
        for f in self._indexed:
            v = r.field(f)
            if v is not None:
                self._index.setdefault(f, {}).setdefault(v, []).append(r)

    def query(self, field: str, value: str) -> list[LogRecord]:
        return self._index.get(field, {}).get(value, [])

    def all(self) -> tuple[LogRecord, ...]:
        return tuple(self._records)

    def size(self) -> int:
        return len(self._records)


class Logger:
    """The logger a service holds. ``with_`` returns a child logger carrying bound fields (payment_id, trace_id, span_id)
    so every record emitted inside a request scope carries them without each call site repeating them."""

    def __init__(self, service: str, clock: Clock, minimum: Level, redactor: Redactor, sampler: Sampler,
                 store: LogStore, bound: Optional[dict[str, str]] = None) -> None:
        self._service = service
        self._clock = clock
        self._minimum = minimum
        self._redactor = redactor
        self._sampler = sampler
        self._store = store
        self._bound: dict[str, str] = {} if bound is None else bound

    def with_(self, key: str, value: str) -> "Logger":
        nxt = dict(self._bound)
        nxt[key] = value
        return Logger(self._service, self._clock, self._minimum, self._redactor, self._sampler, self._store, nxt)

    def with_trace(self, trace_id: str, span_id: str) -> "Logger":
        return self.with_("trace_id", trace_id).with_("span_id", span_id)

    def debug(self, msg: str, *kv: str) -> None:
        self.log(Level.DEBUG, msg, *kv)

    def info(self, msg: str, *kv: str) -> None:
        self.log(Level.INFO, msg, *kv)

    def warn(self, msg: str, *kv: str) -> None:
        self.log(Level.WARN, msg, *kv)

    def error(self, msg: str, *kv: str) -> None:
        self.log(Level.ERROR, msg, *kv)

    def log(self, level: Level, message: str, *kv: str) -> None:
        if len(kv) % 2 != 0:
            raise ValueError("fields must be key/value pairs")
        if not level.at_least(self._minimum):
            return
        if not self._sampler.keep(level, self._bound.get("trace_id"), message):
            return
        fields: dict[str, str] = {}
        for k, v in self._bound.items():
            fields[k] = self._redactor.redact_value(k, v)
        for i in range(0, len(kv), 2):
            fields[kv[i]] = self._redactor.redact_value(kv[i], kv[i + 1])
        self._store.write(LogRecord(self._clock.now_millis(), level, self._service, self._redactor.mask_pans(message), fields))


class PaymentFlow:
    """Six services logging one payment, with the trace ids from Lesson 07 bound into every record."""

    def __init__(self, sampler: Sampler, minimum: Level) -> None:
        self.clock = FakeClock(1_700_000_000_000)
        self.redactor = Redactor()
        self.store = LogStore(frozenset({"payment_id", "trace_id", "merchant_id"}))
        self.sampler = sampler
        self.minimum = minimum

    def logger(self, service: str) -> Logger:
        return Logger(service, self.clock, self.minimum, self.redactor, self.sampler, self.store)

    def charge(self, trace_id: str, payment_id: str, merchant_id: str, amount_minor: int, currency: str, raw_pan: str,
               processor_fails: bool) -> None:
        gw = self.logger("api-gateway").with_trace(trace_id, "a1a1a1a1a1a1a1a1").with_("payment_id", payment_id).with_("merchant_id", merchant_id)
        gw.info("request received", "method", "POST", "path", "/charges")
        self.clock.advance(2)

        pay = self.logger("payments").with_trace(trace_id, "b2b2b2b2b2b2b2b2").with_("payment_id", payment_id).with_("merchant_id", merchant_id)
        pay.debug("idempotency key checked", "idempotency_key", "idem_" + payment_id)
        pay.info("authorize started", "amount_minor", str(amount_minor), "currency", currency)
        # A careless developer logs the raw request body. The redactor must catch the PAN.
        pay.debug("raw request body", "body", '{"card":"' + raw_pan + '","amount":' + str(amount_minor) + "}")
        self.clock.advance(3)

        risk = self.logger("risk").with_trace(trace_id, "c3c3c3c3c3c3c3c3").with_("payment_id", payment_id)
        risk.info("score computed", "score", "12", "decision", "APPROVE")
        self.clock.advance(23)

        ledger = self.logger("ledger").with_trace(trace_id, "d4d4d4d4d4d4d4d4").with_("payment_id", payment_id)
        ledger.info("hold posted", "amount_minor", str(amount_minor), "currency", currency, "journal_id", "jrn_" + payment_id)
        self.clock.advance(6)

        proc = self.logger("processor-adapter").with_trace(trace_id, "e5e5e5e5e5e5e5e5").with_("payment_id", payment_id)
        proc.debug("iso8583 message built", "mti", "0100", "pan", raw_pan)
        self.clock.advance(45)
        if processor_fails:
            proc.error("processor timeout", "processor", "acq-01", "timeout_ms", "45", "retryable", "true")
            again = self.logger("ledger").with_trace(trace_id, "d4d4d4d4d4d4d4d4").with_("payment_id", payment_id)
            again.warn("hold reversed", "reason", "processor_timeout", "journal_id", "jrn_" + payment_id)
        else:
            proc.info("authorization approved", "processor", "acq-01", "auth_code", "ZX91Q2", "pan", raw_pan)
        self.clock.advance(4)

        notif = self.logger("notifications").with_trace(trace_id, "f6f6f6f6f6f6f6f6").with_("payment_id", payment_id)
        notif.info("webhook enqueued", "event", "payment.failed" if processor_fails else "payment.authorized",
                   "secret", "whsec_live_0123456789")
        self.clock.advance(3)


def main() -> None:
    print("== Unstructured vs structured ==")
    print("unstructured: 2026-10-13 14:02:11 INFO Charged 19.99 for pay_8f3k2 (merchant acme) ok")
    flow = PaymentFlow(Sampler(1, 1), Level.DEBUG)
    flow.charge("4bf92f3577b34da6a3ce929d0e0e4736", "pay_8f3k2", "acme", 1999, "USD", "4111 1111 1111 1111", False)
    print("structured:   " + flow.store.all()[1].to_json())
    print()

    print("== Every record for payment pay_8f3k2, across six services, in order ==")
    for r in flow.store.query("payment_id", "pay_8f3k2"):
        print(r.to_json())
    print()

    print("== Redaction ==")
    print(f"records written: {flow.store.size()}, redactions applied: {flow.redactor.redactions}")
    r = Redactor()
    print("PAN in free text:    " + r.mask_pans("declined card 4111111111111111 for order 1234567890123"))
    print("order id left alone: " + r.mask_pans("order 1234567890123 shipped") + "  (fails Luhn)")
    print("secret by key:       " + r.redact_value("api_key", "sk_live_abcdef"))
    print()

    print("== A failed payment: ERROR and WARN survive sampling, DEBUG/INFO are sampled 1-in-4 traces ==")
    sampled = PaymentFlow(Sampler(4, 4), Level.DEBUG)
    traces = ["0af7651916cd43dd8448eb211c80319c", "1af7651916cd43dd8448eb211c80319c",
              "2af7651916cd43dd8448eb211c80319c", "3af7651916cd43dd8448eb211c80319c"]
    for i, t in enumerate(traces):
        sampled.charge(t, f"pay_s{i}", "acme", 500 + i, "EUR", "5555555555554444", i == 2)
    print(f"records kept: {sampled.store.size()} of {sampled.store.size() + sampled.sampler.dropped} emitted")
    for t in traces:
        recs = sampled.store.query("trace_id", t)
        errors = sum(1 for x in recs if x.level.at_least(Level.WARN))
        print(f"trace {t[:8]}...: {len(recs):2d} records kept, {errors} WARN/ERROR")
    print()
    print("trace 1af76519... was selected by the hash, so all nine of its records survive; the other traces kept only WARN/ERROR")
    print()
    print("== Query: what happened to pay_s2? (its INFO/DEBUG were sampled out; the failure was not) ==")
    for rec in sampled.store.query("payment_id", "pay_s2"):
        print(f"{rec.service:<18} {rec.level.name:<5} {rec.message}")


if __name__ == "__main__":
    main()
