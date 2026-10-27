"""Lesson: phases/08-messaging-and-streams/07-stream-processing-windows/docs/en.md
Stream Processing: Tumbling, Sliding, and Session Windows
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: Akidau et al., "The Dataflow Model" (VLDB 2015), sections 2.2-2.3 (windowing, triggers);
         Apache Flink documentation, "Windows" (assigners, triggers, state); Kafka Streams "Windowing" docs.
Python 3.11+, standard library only. Same design as the Java version. Time is the record's processing timestamp
plus an explicit timer; nothing sleeps.
"""

from __future__ import annotations

from dataclasses import dataclass
from functools import total_ordering
from typing import Dict, List, Optional


@dataclass(frozen=True)
class Currency:
    """ISO 4217 currency: code and minor-unit exponent. Copied from Phase 10, Lesson 01; lessons are self-contained."""

    code: str
    exponent: int

    def __post_init__(self) -> None:
        if self.code is None or len(self.code) != 3:
            raise ValueError(f"currency code must be three letters: {self.code}")
        if self.exponent < 0 or self.exponent > 4:
            raise ValueError(f"exponent must be 0..4: {self.exponent}")


USD = Currency("USD", 2)


class CurrencyMismatchError(Exception):
    def __init__(self, a: Currency, b: Currency) -> None:
        super().__init__(f"cannot combine {a.code} with {b.code}")


@total_ordering
@dataclass(frozen=True)
class Money:
    """Integer minor units plus a currency. Mixing currencies is an error."""

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

    def _require_same_currency(self, other: "Money") -> None:
        if self.currency != other.currency:
            raise CurrencyMismatchError(self.currency, other.currency)

    def plus(self, other: "Money") -> "Money":
        self._require_same_currency(other)
        return Money(self.minor_units + other.minor_units, self.currency)

    def max(self, other: "Money") -> "Money":
        return self if self.compare_to(other) >= 0 else other

    def compare_to(self, other: "Money") -> int:
        self._require_same_currency(other)
        return (self.minor_units > other.minor_units) - (self.minor_units < other.minor_units)

    def __lt__(self, other: "Money") -> bool:
        return self.compare_to(other) < 0

    def __str__(self) -> str:
        scale = 10 ** self.currency.exponent
        abs_units = abs(self.minor_units)
        frac = str(abs_units % scale).rjust(self.currency.exponent, "0")
        return f"{'-' if self.minor_units < 0 else ''}{abs_units // scale}.{frac} {self.currency.code}"


# ---------------------------------------------------------------------------------------------------------------------
# Records, windows, aggregates.
# ---------------------------------------------------------------------------------------------------------------------


@dataclass(frozen=True)
class Txn:
    """One card transaction as the stream processor sees it. time is the processing time: the clock when the record
    arrived. This lesson uses processing time only; Lesson 08 separates event time from it."""

    card_id: str
    amount: Money
    time: int

    def __post_init__(self) -> None:
        if self.card_id is None:
            raise TypeError("cardId")
        if self.time < 0:
            raise ValueError("time must be non-negative")


@dataclass(frozen=True, order=True)
class Window:
    """A half-open time interval [start, end). Ordered by start, then end, which is what `order=True` gives."""

    start: int
    end: int

    def __post_init__(self) -> None:
        if self.end <= self.start:
            raise ValueError(f"window must have positive length: [{self.start}, {self.end})")

    def contains(self, t: int) -> bool:
        return self.start <= t < self.end

    def length(self) -> int:
        return self.end - self.start

    def __str__(self) -> str:
        return f"[{self.start}, {self.end})"


@dataclass(frozen=True)
class Aggregate:
    """What is kept per (key, window): count, sum, max. Updated in O(1) per record; the records themselves are not kept."""

    count: int
    sum: Money
    max: Money

    @staticmethod
    def empty(currency: Currency) -> "Aggregate":
        return Aggregate(0, Money.zero(currency), Money.zero(currency))

    def add(self, amount: Money) -> "Aggregate":
        return Aggregate(self.count + 1, self.sum.plus(amount), self.max.max(amount))

    def merge(self, other: "Aggregate") -> "Aggregate":
        return Aggregate(self.count + other.count, self.sum.plus(other.sum), self.max.max(other.max))

    def __str__(self) -> str:
        return f"Aggregate[count={self.count}, sum={self.sum}, max={self.max}]"


@dataclass(frozen=True)
class WindowResult:
    """An emitted result: one key, one closed window, its aggregate, and the processing time at which it was emitted."""

    key: str
    window: Window
    aggregate: Aggregate
    emitted_at: int


# ---------------------------------------------------------------------------------------------------------------------
# Assigners: a pure function from a timestamp to the windows it belongs to.
# ---------------------------------------------------------------------------------------------------------------------


class WindowAssigner:
    def assign(self, time: int) -> List[Window]:
        raise NotImplementedError

    def describe(self) -> str:
        raise NotImplementedError


class TumblingWindows(WindowAssigner):
    """Fixed-size, non-overlapping windows aligned to the epoch: every timestamp is in exactly one."""

    def __init__(self, size: int) -> None:
        if size <= 0:
            raise ValueError("size must be positive")
        self.size = size

    def assign(self, time: int) -> List[Window]:
        start = time - time % self.size  # Python's % already floors, like Math.floorMod
        return [Window(start, start + self.size)]

    def describe(self) -> str:
        return f"tumbling(size={self.size})"


class SlidingWindows(WindowAssigner):
    """Fixed-size windows that start every slide: every timestamp is in size/slide windows."""

    def __init__(self, size: int, slide: int) -> None:
        if size <= 0 or slide <= 0:
            raise ValueError("size and slide must be positive")
        if slide > size:
            raise ValueError("slide larger than size leaves gaps; use tumbling windows")
        if size % slide != 0:
            raise ValueError("size must be a multiple of slide")
        self.size = size
        self.slide = slide

    def assign(self, time: int) -> List[Window]:
        out: List[Window] = []
        last_start = time - time % self.slide
        start = last_start
        while start > time - self.size:
            out.append(Window(start, start + self.size))
            start -= self.slide
        out.reverse()
        return out

    def describe(self) -> str:
        return f"sliding(size={self.size}, slide={self.slide})"


# ---------------------------------------------------------------------------------------------------------------------
# Operators. Each keeps per-key, per-window state, updates it on process(), and emits on on_timer(now) when a window's
# trigger condition is met in processing time.
# ---------------------------------------------------------------------------------------------------------------------


class WindowOperator:
    def process(self, txn: Txn) -> None:
        raise NotImplementedError

    def on_timer(self, now: int) -> List[WindowResult]:
        raise NotImplementedError

    def state_size(self) -> int:
        raise NotImplementedError

    def describe(self) -> str:
        raise NotImplementedError


def _sort_results(out: List[WindowResult]) -> List[WindowResult]:
    out.sort(key=lambda r: (r.window, r.key))
    return out


class WindowedAggregator(WindowOperator):
    """Tumbling or sliding windows: assigner decides membership, the trigger is "processing time has passed the window end"."""

    def __init__(self, assigner: WindowAssigner, currency: Currency) -> None:
        self._assigner = assigner
        self._currency = currency
        self._state: Dict[str, Dict[Window, Aggregate]] = {}
        self._max_time_seen = -1

    def process(self, txn: Txn) -> None:
        if txn.time < self._max_time_seen:
            raise ValueError(f"processing time went backwards: {txn.time} < {self._max_time_seen}")
        self._max_time_seen = txn.time
        per_key = self._state.setdefault(txn.card_id, {})
        for w in self._assigner.assign(txn.time):
            fresh = Aggregate.empty(self._currency).add(txn.amount)
            per_key[w] = fresh if w not in per_key else per_key[w].merge(fresh)

    def on_timer(self, now: int) -> List[WindowResult]:
        """Emit and drop every window whose end <= now. Windows are emitted once; later records cannot reach them."""
        out: List[WindowResult] = []
        for key in list(self._state.keys()):
            windows = self._state[key]
            for w in sorted(windows):
                if w.end > now:
                    break
                out.append(WindowResult(key, w, windows.pop(w), now))
            if not windows:
                del self._state[key]
        return _sort_results(out)

    def peek(self, key: str, window: Window) -> Optional[Aggregate]:
        """Current (key, window) aggregate, or None. Used by tests and by the demo to show state before a trigger."""
        per_key = self._state.get(key)
        return None if per_key is None else per_key.get(window)

    def state_size(self) -> int:
        return sum(len(m) for m in self._state.values())

    def describe(self) -> str:
        return self._assigner.describe()


class SessionAggregator(WindowOperator):
    """Session windows: per key, a record extends the session it falls within gap of, or starts a new one. Two
    sessions that a record bridges are merged. A session closes when processing time passes its last record + gap."""

    def __init__(self, gap: int, currency: Currency) -> None:
        if gap <= 0:
            raise ValueError("gap must be positive")
        self.gap = gap
        self._currency = currency
        self._state: Dict[str, Dict[Window, Aggregate]] = {}
        self._max_time_seen = -1

    def process(self, txn: Txn) -> None:
        if txn.time < self._max_time_seen:
            raise ValueError(f"processing time went backwards: {txn.time} < {self._max_time_seen}")
        self._max_time_seen = txn.time
        sessions = self._state.setdefault(txn.card_id, {})
        # A record's own session is [t, t + gap). Merge with every existing session it overlaps.
        mine = Window(txn.time, txn.time + self.gap)
        agg = Aggregate.empty(self._currency).add(txn.amount)
        for w in sorted(sessions):
            overlaps = w.start < mine.end and mine.start < w.end
            if overlaps:
                mine = Window(min(w.start, mine.start), max(w.end, mine.end))
                agg = agg.merge(sessions.pop(w))
        sessions[mine] = agg

    def on_timer(self, now: int) -> List[WindowResult]:
        """A session [start, end) with end = last_record + gap closes when now >= end: the gap has elapsed with nothing new."""
        out: List[WindowResult] = []
        for key in list(self._state.keys()):
            sessions = self._state[key]
            for w in sorted(sessions):
                if w.end <= now:
                    # Report the session as [first record, last record + gap); the gap is part of the window by definition.
                    out.append(WindowResult(key, w, sessions.pop(w), now))
            if not sessions:
                del self._state[key]
        return _sort_results(out)

    def open_sessions(self, key: str) -> List[Window]:
        return sorted(self._state.get(key, {}).keys())

    def state_size(self) -> int:
        return sum(len(m) for m in self._state.values())

    def describe(self) -> str:
        return f"session(gap={self.gap})"


# ---------------------------------------------------------------------------------------------------------------------
# Driver and the velocity rule.
# ---------------------------------------------------------------------------------------------------------------------


class StreamRunner:
    """Feeds records to an operator in time order and fires the timer every timer_interval of processing time."""

    def __init__(self, operator: WindowOperator, timer_interval: int) -> None:
        if timer_interval <= 0:
            raise ValueError("timerInterval must be positive")
        self._operator = operator
        self._timer_interval = timer_interval
        self._next_timer = timer_interval
        self._max_state_size = 0
        self._results: List[WindowResult] = []

    def run(self, txns: List[Txn], end_of_stream: int) -> List[WindowResult]:
        """Process all records (must be in non-decreasing time), then fire the timer at end_of_stream."""
        for t in txns:
            while self._next_timer <= t.time:
                self._results.extend(self._operator.on_timer(self._next_timer))
                self._next_timer += self._timer_interval
            self._operator.process(t)
            self._max_state_size = max(self._max_state_size, self._operator.state_size())
        while self._next_timer <= end_of_stream:
            self._results.extend(self._operator.on_timer(self._next_timer))
            self._next_timer += self._timer_interval
        return list(self._results)

    def max_state_size(self) -> int:
        return self._max_state_size


class VelocityRule:
    """The fraud rule: more than max_per_window transactions for one card in one window is a velocity breach."""

    def __init__(self, max_per_window: int) -> None:
        self.max_per_window = max_per_window

    def breaches(self, results: List[WindowResult]) -> List[WindowResult]:
        return [r for r in results if r.aggregate.count > self.max_per_window]


SEC = 1_000
MIN = 60 * SEC


def txn(card: str, cents: int, time: int) -> Txn:
    return Txn(card, Money.of_minor(cents, USD), time)


def demo_stream() -> List[Txn]:
    """The demo stream: three cards over about four minutes of processing time."""
    s: List[Txn] = []
    # card-A: four quick transactions straddling the 1:00 boundary, then quiet
    s.append(txn("card-A", 1_299, 0 * MIN + 50 * SEC))
    s.append(txn("card-A", 2_450, 0 * MIN + 56 * SEC))
    s.append(txn("card-A", 899, 1 * MIN + 3 * SEC))
    s.append(txn("card-A", 15_000, 1 * MIN + 9 * SEC))
    # card-B: one transaction a minute, steady
    for m in range(4):
        s.append(txn("card-B", 4_000 + m, m * MIN + 30 * SEC))
    # card-C: a burst at 2:00-2:10, a pause, another burst at 3:00
    s.append(txn("card-C", 500, 2 * MIN + 0 * SEC))
    s.append(txn("card-C", 500, 2 * MIN + 4 * SEC))
    s.append(txn("card-C", 500, 2 * MIN + 10 * SEC))
    s.append(txn("card-C", 9_900, 3 * MIN + 0 * SEC))
    s.append(txn("card-C", 9_900, 3 * MIN + 5 * SEC))
    s.sort(key=lambda t: t.time)  # stable, like List.sort
    return s


def mmss(millis: int) -> str:
    return f"{millis // MIN}:{(millis % MIN) // SEC:02d}"


def _print(results: List[WindowResult]) -> None:
    for r in results:
        print(f"  {r.key:<7} {mmss(r.window.start)}-{mmss(r.window.end)}  count={r.aggregate.count} sum={r.aggregate.sum} "
              f"max={r.aggregate.max}  (emitted at {mmss(r.emitted_at)})")


def _fmt(items) -> str:
    return "[" + ", ".join(str(i) for i in items) + "]"


def main() -> None:
    stream = demo_stream()
    end = 5 * MIN
    rule = VelocityRule(3)

    print("== 1. Window assignment is a pure function of the timestamp ==")
    t = 1 * MIN + 3 * SEC
    print(f"t={mmss(t)}  tumbling(60s): {_fmt(TumblingWindows(MIN).assign(t))}")
    print(f"t={mmss(t)}  sliding(60s, 10s): {_fmt(SlidingWindows(MIN, 10 * SEC).assign(t))}")

    print("\n== 2. Tumbling 1-minute windows: transactions per card per minute ==")
    tumbling = StreamRunner(WindowedAggregator(TumblingWindows(MIN), USD), 10 * SEC)
    tumbling_results = tumbling.run(stream, end)
    _print(tumbling_results)
    print(f"velocity breaches (> 3 per window): {len(rule.breaches(tumbling_results))}   max state entries: {tumbling.max_state_size()}")
    print("card-A made 4 transactions in 19 seconds (0:50 to 1:09) and no tumbling window saw more than 2.")

    print("\n== 3. Sliding 1-minute windows every 10 seconds: the boundary problem goes away ==")
    sliding = StreamRunner(WindowedAggregator(SlidingWindows(MIN, 10 * SEC), USD), 10 * SEC)
    sliding_results = sliding.run(stream, end)
    breaches = rule.breaches(sliding_results)
    print(f"{len(sliding_results)} window results emitted ({sum(1 for r in sliding_results if r.key == 'card-A')} for card-A); breaches:")
    _print(breaches)
    print(f"max state entries: {sliding.max_state_size()} (each record lives in size/slide = 6 windows)")

    print("\n== 4. Session windows with a 30-second gap: activity bursts, not clock minutes ==")
    sessions = SessionAggregator(30 * SEC, USD)
    session_runner = StreamRunner(sessions, 10 * SEC)
    session_results = session_runner.run(stream, end)
    _print(session_results)
    print("card-B never pauses less than 30 s between transactions, so each transaction is its own session;")
    print("card-C's two bursts are two sessions; card-A's four transactions are one.")

    print("\n== 5. Trigger timing: a window closes only when the timer passes its end ==")
    agg = WindowedAggregator(TumblingWindows(MIN), USD)
    agg.process(txn("card-A", 1_299, 50 * SEC))
    agg.process(txn("card-A", 2_450, 56 * SEC))
    first = Window(0, MIN)
    print(f"state for card-A {first} before the timer: {agg.peek('card-A', first)}")
    print(f"onTimer(0:59) emits {len(agg.on_timer(59 * SEC))} windows; onTimer(1:00) emits {len(agg.on_timer(MIN))}")
    try:
        agg.process(txn("card-A", 1, 30 * SEC))
    except ValueError as e:
        print(f"a record with an earlier processing time is rejected: {e}")

    print("\n== 6. Session extension in detail ==")
    s2 = SessionAggregator(30 * SEC, USD)
    s2.process(txn("card-C", 500, 0))

    def spans(key: str) -> str:
        return _fmt(f"{mmss(w.start)}-{mmss(w.end)}" for w in s2.open_sessions(key))

    print(f"record at 0:00, gap 30 s -> open session {spans('card-C')}")
    s2.process(txn("card-C", 500, 25 * SEC))
    s2.process(txn("card-C", 500, 50 * SEC))
    print(f"records at 0:25 and 0:50 each arrive before the gap elapses -> extended to {spans('card-C')}")
    s2.process(txn("card-C", 500, 2 * MIN))
    print(f"record at 2:00 arrives after the gap -> two sessions: {spans('card-C')}")
    closed = s2.on_timer(80 * SEC)
    print("onTimer(1:20) closes the first: " + _fmt(f"{mmss(r.window.start)}-{mmss(r.window.end)} count={r.aggregate.count}" for r in closed))
    print(f"state entries: tumbling={tumbling.max_state_size()} sliding={sliding.max_state_size()} "
          f"session={session_runner.max_state_size()} (peak during the run)")


if __name__ == "__main__":
    main()
