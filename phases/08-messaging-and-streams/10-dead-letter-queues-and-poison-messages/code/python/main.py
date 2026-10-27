"""Lesson: phases/08-messaging-and-streams/10-dead-letter-queues-and-poison-messages/docs/en.md
Dead-Letter Queues and Poison Messages
Run:  python3 main.py
Test: python3 -m unittest -v
Python 3.11+, standard library only. Same design as the Java version. Retry classification follows the transient/permanent
split used by AWS SQS redrive policies (maxReceiveCount) and Kafka Connect's errors.tolerance / errors.deadletterqueue.
JavaRandom reproduces java.util.Random so the seeded fault injector fails the same ledger calls as the Java demo.
"""

from __future__ import annotations

from dataclasses import dataclass
from enum import Enum
from typing import Dict, List, Optional


@dataclass(frozen=True)
class Currency:
    """ISO 4217 currency: three-letter code plus minor-unit exponent. Copied from Phase 10, Lesson 01."""

    code: str
    exponent: int

    def __post_init__(self) -> None:
        if self.code is None or len(self.code) != 3 or not self.code.isalpha() or not self.code.isupper():
            raise ValueError(f"currency code must be three uppercase letters: {self.code}")
        if self.exponent < 0 or self.exponent > 4:
            raise ValueError("exponent must be 0..4")

    @staticmethod
    def of(code: str) -> "Currency":
        if code == "USD":
            return USD
        if code == "JPY":
            return JPY
        raise ValueError(f"unknown currency: {code}")

    def minor_units_per_major(self) -> int:
        return 10 ** self.exponent


USD = Currency("USD", 2)
JPY = Currency("JPY", 0)


@dataclass(frozen=True)
class Money:
    """Integer minor units plus a currency. Never a float. parse() rejects excess precision instead of rounding."""

    minor_units: int
    currency: Currency

    def __post_init__(self) -> None:
        if not isinstance(self.minor_units, int) or isinstance(self.minor_units, bool):
            raise TypeError("minor_units must be an int; never construct Money from a float")
        if self.currency is None:
            raise TypeError("currency")

    @staticmethod
    def zero(currency: Currency) -> "Money":
        return Money(0, currency)

    @staticmethod
    def parse(text: str, currency: Currency) -> "Money":
        s = text.strip()
        negative = s.startswith("-")
        if negative or s.startswith("+"):
            s = s[1:]
        if s == "":
            raise ValueError("empty amount")
        whole, frac = s, ""
        dot = s.find(".")
        if dot >= 0:
            whole, frac = s[:dot], s[dot + 1:]
        if whole == "":
            whole = "0"
        if not whole.isdigit() or (frac != "" and not frac.isdigit()):
            raise ValueError(f"not a decimal amount: {text}")
        if len(frac) > currency.exponent:
            raise ArithmeticError(f"{text} has more decimals than {currency.code} allows ({currency.exponent})")
        padded = frac + "0" * (currency.exponent - len(frac))
        minor = int(whole) * currency.minor_units_per_major() + (0 if padded == "" else int(padded))
        return Money(-minor if negative else minor, currency)

    def plus(self, other: "Money") -> "Money":
        if self.currency != other.currency:
            raise ValueError(f"cannot combine {self.currency.code} with {other.currency.code}")
        return Money(self.minor_units + other.minor_units, self.currency)

    def __str__(self) -> str:
        scale = self.currency.minor_units_per_major()
        abs_units = abs(self.minor_units)
        frac = "" if self.currency.exponent == 0 else "." + str(abs_units % scale).rjust(self.currency.exponent, "0")
        return f"{'-' if self.minor_units < 0 else ''}{abs_units // scale}{frac} {self.currency.code}"


@dataclass(frozen=True)
class Event:
    """A posting event as it sits on the partition: offset, the account it belongs to (the ordering key),
    and the raw payload fields exactly as the producer serialized them. Parsing happens in the handler,
    because a poison message is by definition one that cannot be parsed."""

    offset: int
    account_key: str
    amount_text: str
    currency_code: str


class FailureKind(Enum):
    """Whether a failure might succeed on retry (TRANSIENT) or never will without a code or data change (PERMANENT)."""

    TRANSIENT = "TRANSIENT"
    PERMANENT = "PERMANENT"


class HandlerError(Exception):
    """The handler's one way to say no. Carries the classification the consumer's retry policy depends on."""

    def __init__(self, kind: FailureKind, message: str) -> None:
        super().__init__(message)
        self.kind = kind


class SimClock:
    """Deterministic simulated clock; the consumer advances it by the backoff instead of sleeping."""

    def __init__(self) -> None:
        self._now = 0

    def now(self) -> int:
        return self._now

    def advance(self, millis: int) -> None:
        if millis < 0:
            raise ValueError("cannot advance backwards")
        self._now += millis


class JavaRandom:
    """java.util.Random's 48-bit LCG, so the seeded fault injector times out on the same calls as the Java demo."""

    _MASK = (1 << 48) - 1

    def __init__(self, seed: int) -> None:
        self._seed = (seed ^ 0x5DEECE66D) & self._MASK

    def next(self, bits: int) -> int:
        self._seed = (self._seed * 0x5DEECE66D + 0xB) & self._MASK
        value = (self._seed >> (48 - bits)) & 0xFFFFFFFF
        return value - (1 << 32) if value >= 1 << 31 else value

    def next_double(self) -> float:
        return ((self.next(26) << 27) + self.next(27)) * (1.0 / (1 << 53))


class FaultInjector:
    """Seeded transient fault injector: the ledger database times out on a fixed fraction of calls."""

    def __init__(self, seed: int, failure_rate: float) -> None:
        self._random = JavaRandom(seed)
        self._failure_rate = failure_rate
        self._enabled = True

    def should_fail(self) -> bool:
        return self._enabled and self._random.next_double() < self._failure_rate

    def disable(self) -> None:
        self._enabled = False


class Handler:
    def handle(self, event: Event) -> None:
        """Process one event or raise HandlerError. Must be idempotent per offset, because retries re-invoke it."""
        raise NotImplementedError


class LedgerPostingHandler(Handler):
    """Posts an amount to an in-memory per-account ledger. Parse failures are PERMANENT (the bytes will never
    parse differently). Injected database timeouts are TRANSIENT. The handler is idempotent: an offset that
    has already been applied is a no-op, so a retry after a late success cannot double-post."""

    def __init__(self, faults: FaultInjector) -> None:
        self._balances: Dict[str, Money] = {}
        self._applied: Dict[int, bool] = {}
        self._faults = faults

    def handle(self, e: Event) -> None:
        try:
            currency = Currency.of(e.currency_code)
            amount = Money.parse(e.amount_text, currency)
        except (ValueError, ArithmeticError) as ex:  # ValueError plays NumberFormatException / IllegalArgumentException
            raise HandlerError(FailureKind.PERMANENT, str(ex)) from None
        if self._faults.should_fail():
            raise HandlerError(FailureKind.TRANSIENT, "ledger db timeout")
        if e.offset in self._applied:  # idempotent replay
            return
        current = self._balances.get(e.account_key)
        self._balances[e.account_key] = amount if current is None else current.plus(amount)
        self._applied[e.offset] = True

    def balance(self, account_key: str) -> Optional[Money]:
        return self._balances.get(account_key)

    def balances(self) -> Dict[str, Money]:
        return dict(sorted(self._balances.items()))  # sorted: deterministic output


class ParkReason(Enum):
    """Why an event was parked."""

    PERMANENT_FAILURE = "PERMANENT_FAILURE"
    MAX_ATTEMPTS_EXCEEDED = "MAX_ATTEMPTS_EXCEEDED"
    KEY_QUARANTINED = "KEY_QUARANTINED"

    def __str__(self) -> str:
        return self.name


@dataclass(frozen=True)
class DeadLetter:
    """A parked event with everything an operator needs to decide what to do with it."""

    event: Event
    reason: ParkReason
    attempts: int
    last_error: str
    parked_at: int


class DeadLetterQueue:
    """Append-only parking lot with a per-key view, so replay can be done one account at a time, in offset order."""

    def __init__(self) -> None:
        self._entries: List[DeadLetter] = []

    def park(self, letter: DeadLetter) -> None:
        self._entries.append(letter)

    def all(self) -> List[DeadLetter]:
        return list(self._entries)

    def for_key(self, key: str) -> List[DeadLetter]:
        return [d for d in self._entries if d.event.account_key == key]

    def clear(self, key: str) -> None:
        """Remove every entry for the key; called when a replay for that key begins."""
        self._entries = [d for d in self._entries if d.event.account_key != key]

    def size(self) -> int:
        return len(self._entries)


@dataclass(frozen=True)
class ConsumerStats:
    """Counters that make the behaviour measurable."""

    processed: int
    parked: int
    quarantined: int
    retries: int
    committed_offset: int

    def __str__(self) -> str:
        return (f"ConsumerStats[processed={self.processed}, parked={self.parked}, quarantined={self.quarantined}, "
                f"retries={self.retries}, committedOffset={self.committed_offset}]")


class ResilientConsumer:
    """A consumer that never stalls the partition. For each event in offset order:
      1. If the event's key is quarantined, park it as KEY_QUARANTINED without calling the handler, so per-key
         ordering is preserved (a later posting must not land before an earlier one that is still parked).
      2. Otherwise call the handler. PERMANENT failure: park at once. TRANSIENT failure: back off (simulated
         clock) and retry up to max_attempts, then park as MAX_ATTEMPTS_EXCEEDED.
      3. Parking quarantines the key. Either way the offset is committed and the next event is processed.
    Invariant: processed + parked == events consumed, and committed_offset == last offset + 1."""

    def __init__(self, handler: Handler, dlq: DeadLetterQueue, clock: SimClock, max_attempts: int, base_backoff_millis: int) -> None:
        if max_attempts < 1:
            raise ValueError("maxAttempts must be >= 1")
        self._handler = handler
        self._dlq = dlq
        self._clock = clock
        self._max_attempts = max_attempts
        self._base_backoff_millis = base_backoff_millis
        self._quarantined_keys: Dict[str, int] = {}  # key -> offset that caused it
        self._processed = self._parked = self._quarantined = self._retries = self._committed_offset = 0

    def consume(self, partition: List[Event]) -> None:
        for e in partition:
            if e.account_key in self._quarantined_keys:
                self._dlq.park(DeadLetter(e, ParkReason.KEY_QUARANTINED, 0,
                                          f"earlier offset {self._quarantined_keys[e.account_key]} for this key is parked",
                                          self._clock.now()))
                self._parked += 1
                self._quarantined += 1
            else:
                self._attempt(e)
            self._committed_offset = e.offset + 1

    def _attempt(self, e: Event) -> bool:
        """Try the handler with retries; returns True on success, False if the event was parked."""
        attempts = 0
        while True:
            attempts += 1
            try:
                self._handler.handle(e)
                self._processed += 1
                return True
            except HandlerError as ex:
                if ex.kind is FailureKind.PERMANENT:
                    self._park(e, ParkReason.PERMANENT_FAILURE, attempts, str(ex))
                    return False
                if attempts >= self._max_attempts:
                    self._park(e, ParkReason.MAX_ATTEMPTS_EXCEEDED, attempts, str(ex))
                    return False
                self._retries += 1
                self._clock.advance(self._base_backoff_millis << (attempts - 1))  # exponential backoff: base, 2x, 4x, ...

    def _park(self, e: Event, reason: ParkReason, attempts: int, error: str) -> None:
        self._dlq.park(DeadLetter(e, reason, attempts, error, self._clock.now()))
        self._parked += 1
        self._quarantined_keys[e.account_key] = e.offset

    def replay(self, key: str, corrections: Dict[int, Event]) -> int:
        """Replay every parked event for one key, in offset order, after the operator has fixed the cause.
        `corrections` maps an offset to a repaired event (for poison payloads); other events replay as-is.
        Stops at the first event that fails again, leaving it and its followers parked. On full success the
        key leaves quarantine."""
        letters = sorted(self._dlq.for_key(key), key=lambda d: d.event.offset)
        self._dlq.clear(key)
        self._quarantined_keys.pop(key, None)
        replayed = 0
        for i, letter in enumerate(letters):
            e = corrections.get(letter.event.offset, letter.event)
            self._parked -= 1  # leaving the DLQ, one way or the other
            if self._attempt(e):
                replayed += 1
            else:
                # Re-park the rest in order, preserving per-key ordering behind the failure.
                for rest_letter in letters[i + 1:]:
                    self._dlq.park(DeadLetter(rest_letter.event, ParkReason.KEY_QUARANTINED, 0,
                                              f"replay of offset {e.offset} failed again", self._clock.now()))
                    self._quarantined += 1
                break
        return replayed

    def is_quarantined(self, key: str) -> bool:
        return key in self._quarantined_keys

    def stats(self) -> ConsumerStats:
        return ConsumerStats(self._processed, self._parked, self._quarantined, self._retries, self._committed_offset)


class StopTheWorldConsumer:
    """The alternative most systems ship by accident: on any failure, stop and retry the same offset forever.
    Records how far the partition got before it stalled."""

    def __init__(self, handler: Handler, max_attempts: int) -> None:
        self._handler = handler
        self._max_attempts = max_attempts
        self._processed = 0
        self._committed_offset = 0
        self._stuck_on: Optional[Event] = None

    def consume(self, partition: List[Event]) -> None:
        for e in partition:
            attempts = 0
            while True:
                attempts += 1
                try:
                    self._handler.handle(e)
                    self._processed += 1
                    self._committed_offset = e.offset + 1
                    break
                except HandlerError as ex:
                    if ex.kind is FailureKind.PERMANENT or attempts >= self._max_attempts:
                        self._stuck_on = e
                        return  # the partition is now stalled behind this offset

    def processed(self) -> int:
        return self._processed

    def committed_offset(self) -> int:
        return self._committed_offset

    def stuck_on(self) -> Optional[Event]:
        return self._stuck_on


def partition() -> List[Event]:
    return [
        Event(0, "acct-A", "100.00", "USD"),
        Event(1, "acct-B", "25.50", "USD"),
        Event(2, "acct-A", "12.345", "USD"),   # poison: three decimals in a two-decimal currency
        Event(3, "acct-C", "1200", "JPY"),
        Event(4, "acct-A", "-40.00", "USD"),   # fine on its own, but acct-A is quarantined behind offset 2
        Event(5, "acct-B", "10.00", "USD"),
        Event(6, "acct-D", "7.00", "US$"),     # poison: unknown currency code
        Event(7, "acct-C", "300", "JPY"),
        Event(8, "acct-B", "4.25", "USD"),
        Event(9, "acct-A", "5.00", "USD"),     # also quarantined behind offset 2
    ]


def main() -> None:
    events = partition()

    print("== Stop-the-world consumer: retry the same offset until it works ==")
    naive = StopTheWorldConsumer(LedgerPostingHandler(FaultInjector(7, 0.0)), 3)
    naive.consume(events)
    stuck = naive.stuck_on()
    print(f"processed {naive.processed()} of {len(events)}, committed offset {naive.committed_offset()}, "
          f"stuck on offset {stuck.offset} ({stuck.account_key} {stuck.amount_text})")
    print("accounts B, C, and D never see another posting until someone wakes up.")
    print()

    print("== Resilient consumer: park the poison, quarantine its key, keep every other account moving ==")
    clock = SimClock()
    faults = FaultInjector(11, 0.25)  # one ledger call in four times out
    handler = LedgerPostingHandler(faults)
    dlq = DeadLetterQueue()
    consumer = ResilientConsumer(handler, dlq, clock, 3, 100)
    consumer.consume(events)
    s = consumer.stats()
    print(f"processed={s.processed} parked={s.parked} (of which quarantined followers={s.quarantined}) "
          f"transient retries={s.retries} committed offset={s.committed_offset} simulated clock={clock.now()}ms")
    print("balances: {" + ", ".join(f"{k}={v}" for k, v in handler.balances().items()) + "}")
    print("dead letters:")
    for d in dlq.all():
        print(f"  offset {d.event.offset} {d.event.account_key:<6} {d.event.amount_text:<8} {d.event.currency_code:<4} "
              f"reason={str(d.reason):<22} attempts={d.attempts} error={d.last_error}")
    print()

    print("== Replay acct-A after correcting offset 2 (12.345 -> 12.34, approved by finance) ==")
    faults.disable()
    replayed_a = consumer.replay("acct-A", {2: Event(2, "acct-A", "12.34", "USD")})
    print(f"replayed {replayed_a} events in offset order; acct-A quarantined={str(consumer.is_quarantined('acct-A')).lower()}")
    print(f"acct-A balance = {handler.balance('acct-A')}  (100.00 + 12.34 - 40.00 + 5.00)")
    print()

    print("== Replay acct-B after the ledger database recovered: no correction needed ==")
    replayed_b = consumer.replay("acct-B", {})
    print(f"replayed {replayed_b} events; acct-B balance = {handler.balance('acct-B')}  (25.50 + 10.00 + 4.25)")
    print(f"dead letters remaining: {dlq.size()} (offset 6, acct-D, awaiting a currency-code fix)")
    print(f"stats: {consumer.stats()}")


if __name__ == "__main__":
    main()
