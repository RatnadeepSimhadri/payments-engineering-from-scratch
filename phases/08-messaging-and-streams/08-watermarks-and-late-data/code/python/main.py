"""Lesson: phases/08-messaging-and-streams/08-watermarks-and-late-data/docs/en.md
Watermarks, Event Time, and Late Data
Run:  python3 main.py
Test: python3 -m unittest -v
Python 3.11+, standard library only. Same design as the Java version. Semantics follow the Dataflow model
(Akidau et al., VLDB 2015) and Apache Flink's bounded-out-of-orderness watermark strategy.
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
        if self.code is None or len(self.code) != 3:
            raise ValueError("currency code must be three letters")
        if self.exponent < 0 or self.exponent > 4:
            raise ValueError("exponent must be 0..4")


USD = Currency("USD", 2)


@dataclass(frozen=True)
class Money:
    """Integer minor units plus a currency. Never a float."""

    minor_units: int
    currency: Currency

    def __post_init__(self) -> None:
        if not isinstance(self.minor_units, int) or isinstance(self.minor_units, bool):
            raise TypeError("minor_units must be an int; never construct Money from a float")
        if self.currency is None:
            raise TypeError("currency")

    @staticmethod
    def of_minor(minor_units: int, currency: Currency) -> "Money":
        return Money(minor_units, currency)

    @staticmethod
    def zero(currency: Currency) -> "Money":
        return Money(0, currency)

    def plus(self, other: "Money") -> "Money":
        if self.currency != other.currency:
            raise ValueError(f"cannot combine {self.currency.code} with {other.currency.code}")
        return Money(self.minor_units + other.minor_units, self.currency)

    def __str__(self) -> str:
        scale = 10 ** self.currency.exponent
        abs_units = abs(self.minor_units)
        frac = "" if self.currency.exponent == 0 else "." + str(abs_units % scale).rjust(self.currency.exponent, "0")
        return f"{'-' if self.minor_units < 0 else ''}{abs_units // scale}{frac} {self.currency.code}"


@dataclass(frozen=True)
class Event:
    """A payment event. event_time is when the payment happened (stamped by the producer);
    arrival_time is when the stream processor saw it (stamped by the processor's clock).
    Both are epoch-style milliseconds; the demo uses minutes-since-midnight for readability."""

    key: str
    event_time: int
    arrival_time: int
    amount: Money


class LateReason(Enum):
    """Why an event landed in the side output."""

    WINDOW_CLOSED = "WINDOW_CLOSED"

    def __str__(self) -> str:
        return self.name


@dataclass(frozen=True)
class LateEvent:
    """A late event parked in the side output, with the window it would have belonged to."""

    event: Event
    window_start: int
    window_end: int
    watermark_at_arrival: int
    reason: LateReason


class FiringKind(Enum):
    """Whether a window result is its first (on-time) firing or a corrected re-firing caused by an allowed-late event."""

    ON_TIME = "ON_TIME"
    LATE_UPDATE = "LATE_UPDATE"

    def __str__(self) -> str:
        return self.name


@dataclass(frozen=True)
class WindowResult:
    """A window result: [window_start, window_end) is half-open."""

    window_start: int
    window_end: int
    count: int
    sum: Money
    kind: FiringKind


@dataclass(frozen=True, order=True)
class Window:
    """Half-open interval [start, end). Tumbling windows of a fixed size tile the time axis; ordered by start."""

    start: int
    end: int


class TumblingWindowAssigner:
    """Assigns each event time to exactly one tumbling window: start = floor(t / size) * size."""

    def __init__(self, size_millis: int) -> None:
        if size_millis <= 0:
            raise ValueError("window size must be positive")
        self._size_millis = size_millis

    def assign(self, timestamp: int) -> Window:
        start = (timestamp // self._size_millis) * self._size_millis  # // floors, like Math.floorDiv
        return Window(start, start + self._size_millis)


class BoundedOutOfOrdernessWatermark:
    """Bounded out-of-orderness watermark. After seeing a maximum event time T, the generator asserts
    that no further event with event_time < T - max_out_of_orderness will arrive. The watermark never
    moves backwards: a late event does not pull it down."""

    def __init__(self, max_out_of_orderness_millis: int) -> None:
        if max_out_of_orderness_millis < 0:
            raise ValueError("out-of-orderness must be >= 0")
        self._max_out_of_orderness_millis = max_out_of_orderness_millis
        self._max_event_time: Optional[int] = None

    def observe(self, event_time: int) -> Optional[int]:
        """Observe an event time; returns the (possibly unchanged) current watermark."""
        if self._max_event_time is None or event_time > self._max_event_time:
            self._max_event_time = event_time
        return self.current()

    def current(self) -> Optional[int]:
        """None before any event has been seen: nothing is known to be complete (Java's Long.MIN_VALUE)."""
        if self._max_event_time is None:
            return None
        return self._max_event_time - self._max_out_of_orderness_millis


class WindowState:
    """Mutable per-window accumulator. Kept until the window is garbage-collected after allowed lateness."""

    def __init__(self, currency: Currency) -> None:
        self.count = 0
        self.sum = Money.zero(currency)
        self.fired = False

    def add(self, e: Event) -> None:
        self.count += 1
        self.sum = self.sum.plus(e.amount)


class EventTimeWindowOperator:
    """Event-time tumbling-window operator with a watermark, allowed lateness, and a late-data side output.

    Invariants:
     1. A window [s, e) fires ON_TIME exactly once, when the watermark first reaches e.
     2. An event with event_time < watermark whose window has already fired is either folded in as a
        LATE_UPDATE (if watermark < e + allowed_lateness) or sent to the side output, never dropped silently.
     3. Every event is accounted for exactly once: sum over all final window counts + side output size == events seen.
    """

    def __init__(self, window_size_millis: int, max_out_of_orderness_millis: int, allowed_lateness_millis: int, currency: Currency) -> None:
        if allowed_lateness_millis < 0:
            raise ValueError("allowed lateness must be >= 0")
        self._assigner = TumblingWindowAssigner(window_size_millis)
        self._watermark = BoundedOutOfOrdernessWatermark(max_out_of_orderness_millis)
        self._allowed_lateness_millis = allowed_lateness_millis
        self._currency = currency
        self._open: Dict[Window, WindowState] = {}   # TreeMap in Java; sorted on each advance
        self._side_output: List[LateEvent] = []
        self._emitted: List[WindowResult] = []
        self._events_seen = 0
        self._events_in_closed_windows = 0

    def process(self, e: Event) -> List[WindowResult]:
        """Process one event; returns the results emitted as a consequence (zero, one, or several)."""
        self._events_seen += 1
        out: List[WindowResult] = []
        w = self._assigner.assign(e.event_time)
        wm_before = self._watermark.current()

        if wm_before is not None and wm_before >= w.end + self._allowed_lateness_millis:
            # The window was fired and then garbage-collected. Side output, never silent drop.
            self._side_output.append(LateEvent(e, w.start, w.end, wm_before, LateReason.WINDOW_CLOSED))
            self._watermark.observe(e.event_time)
            return out

        state = self._open.get(w)
        if state is None:
            state = self._open[w] = WindowState(self._currency)
        state.add(e)
        if state.fired:
            # Window already fired on time; this is an allowed-late event. Re-emit a corrected result.
            out.append(WindowResult(w.start, w.end, state.count, state.sum, FiringKind.LATE_UPDATE))

        wm_after = self._watermark.observe(e.event_time)
        out.extend(self._advance(wm_after))
        return out

    def _advance(self, wm: Optional[int]) -> List[WindowResult]:
        """Fire every unfired window whose end <= watermark, then GC windows past end + allowed lateness."""
        out: List[WindowResult] = []
        if wm is None:
            return out
        for w in sorted(self._open):
            if w.end > wm:
                break  # ordered by start; later windows are not ready
            s = self._open[w]
            if not s.fired:
                s.fired = True
                out.append(WindowResult(w.start, w.end, s.count, s.sum, FiringKind.ON_TIME))
        for w in sorted(self._open):
            if wm >= w.end + self._allowed_lateness_millis:
                self._events_in_closed_windows += self._open.pop(w).count
            else:
                break
        self._emitted.extend(out)
        return out

    def current_watermark(self) -> Optional[int]:
        return self._watermark.current()

    def side_output(self) -> List[LateEvent]:
        return list(self._side_output)

    def all_emitted(self) -> List[WindowResult]:
        return list(self._emitted)

    def open_window_count(self) -> int:
        return len(self._open)

    def accounts_for_every_event(self) -> bool:
        """Invariant 3: nothing lost, nothing double-counted."""
        in_open = sum(s.count for s in self._open.values())
        return self._events_seen == in_open + self._events_in_closed_windows + len(self._side_output)


class ProcessingTimeWindowOperator:
    """The naive alternative: windows keyed by arrival (processing) time. Deterministic here because the
    processor's clock is injected via Event.arrival_time. It never has late data, because it never asks
    when anything actually happened."""

    def __init__(self, window_size_millis: int, currency: Currency) -> None:
        self._assigner = TumblingWindowAssigner(window_size_millis)
        self._windows: Dict[Window, WindowState] = {}
        self._currency = currency

    def process(self, e: Event) -> None:
        w = self._assigner.assign(e.arrival_time)
        state = self._windows.get(w)
        if state is None:
            state = self._windows[w] = WindowState(self._currency)
        state.add(e)

    def results(self) -> List[WindowResult]:
        return [WindowResult(w.start, w.end, s.count, s.sum, FiringKind.ON_TIME) for w, s in sorted(self._windows.items())]


MINUTE = 60_000


def t(hhmm_text: str) -> int:
    """"11:59" -> milliseconds since midnight. Keeps the demo readable."""
    h, m = hhmm_text.split(":")
    return (int(h) * 60 + int(m)) * MINUTE


def hhmm(millis: Optional[int]) -> str:
    if millis is None:
        return "-inf"
    minutes = millis // MINUTE
    return f"{minutes // 60:02d}:{minutes % 60:02d}"


def show(r: WindowResult) -> str:
    return f"[{hhmm(r.window_start)}, {hhmm(r.window_end)}) count={r.count} sum={r.sum} {r.kind}"


def ev(key: str, event_time: str, arrival_time: str, cents: int) -> Event:
    return Event(key, t(event_time), t(arrival_time), Money.of_minor(cents, USD))


def main() -> None:
    # Hourly settlement totals per merchant. The 11:59 authorization from a store with a flaky
    # uplink arrives at 12:03, after the 12:00-12:05 traffic has already been seen.
    events = [
        ev("m-1", "11:40", "11:40", 1999),
        ev("m-1", "11:52", "11:52", 4500),
        ev("m-2", "11:58", "11:58", 1250),
        ev("m-1", "12:01", "12:01", 800),
        ev("m-2", "12:02", "12:02", 3000),
        ev("m-1", "11:59", "12:03", 7700),   # happened in the 11:00 hour, arrived in the 12:00 hour
        ev("m-2", "12:04", "12:04", 1500),
        ev("m-1", "12:09", "12:09", 2000),   # watermark reaches 12:04: the 11:00 window fires
        ev("m-2", "11:57", "12:10", 999),    # late but within allowed lateness: corrected re-firing
        ev("m-1", "12:16", "12:16", 600),    # watermark 12:11 passes 12:00 + 10 min: 11:00 window is GC'd
        ev("m-2", "11:50", "12:20", 2500),   # too late: side output
        ev("m-1", "13:06", "13:06", 100),    # watermark 13:01 fires the 12:00 window
    ]

    print("== Processing-time windows: the 11:59 payment lands in the 12:00 hour ==")
    pt = ProcessingTimeWindowOperator(60 * MINUTE, USD)
    for e in events:
        pt.process(e)
    for r in pt.results():
        print(f"  {show(r)}")

    print()
    print("== Event-time windows, watermark = max event time - 5 min, allowed lateness 10 min ==")
    op = EventTimeWindowOperator(60 * MINUTE, 5 * MINUTE, 10 * MINUTE, USD)
    for e in events:
        fired = op.process(e)
        print(f"  event {e.key} @{hhmm(e.event_time)} (arrived {hhmm(e.arrival_time)}) {str(e.amount):<12} watermark={hhmm(op.current_watermark())}")
        for r in fired:
            print(f"    -> fired {show(r)}")

    print()
    print("== Side output (late data that missed the window entirely) ==")
    for le in op.side_output():
        print(f"  {le.event.key} @{hhmm(le.event.event_time)} arrived {hhmm(le.event.arrival_time)} amount={le.event.amount} "
              f"target=[{hhmm(le.window_start)}, {hhmm(le.window_end)}) watermark-at-arrival={hhmm(le.watermark_at_arrival)} reason={le.reason}")
    print()
    print(f"every event accounted for exactly once: {str(op.accounts_for_every_event()).lower()}")
    print(f"open windows still holding state: {op.open_window_count()}")


if __name__ == "__main__":
    main()
