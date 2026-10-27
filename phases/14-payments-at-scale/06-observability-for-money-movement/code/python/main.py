"""Lesson: phases/14-payments-at-scale/06-observability-for-money-movement/docs/en.md
Observability for Money Movement
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: Google SRE Workbook ch. 5 "Alerting on SLOs" (multiwindow, multi-burn-rate alerts);
Phase 9 Lessons 01 and 06 (SLOs, metrics). Python 3.11+, standard library only. Money is integer minor units.
"""

from __future__ import annotations

from dataclasses import dataclass
from datetime import datetime, timedelta, timezone
from enum import Enum
from functools import total_ordering
from typing import Callable, Optional


# ---------- Money (copied from Phase 10, Lesson 01) ----------


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


class CurrencyMismatchError(ValueError):
    def __init__(self, a: Currency, b: Currency) -> None:
        super().__init__(f"cannot combine {a.code} with {b.code}")


@total_ordering
@dataclass(frozen=True)
class Money:
    minor_units: int
    currency: Currency

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

    def minus(self, other: "Money") -> "Money":
        self._require_same_currency(other)
        return Money(self.minor_units - other.minor_units, self.currency)

    def __lt__(self, other: "Money") -> bool:
        self._require_same_currency(other)
        return self.minor_units < other.minor_units

    def __str__(self) -> str:
        magnitude = abs(self.minor_units)
        whole, frac = divmod(magnitude, self.currency.minor_units_per_major())
        text = str(whole)
        if self.currency.exponent > 0:
            text += "." + str(frac).rjust(self.currency.exponent, "0")
        return f"{'-' if self.minor_units < 0 else ''}{text} {self.currency.code}"

    __repr__ = __str__


# ---------------------------------------------------------------------------------------------
# Time is injected. Nothing in this file reads the wall clock.
# ---------------------------------------------------------------------------------------------


def instant(text: str) -> datetime:
    """Instant.parse for the 'YYYY-MM-DDTHH:MM:SSZ' form: an aware UTC datetime."""
    return datetime.fromisoformat(text.replace("Z", "+00:00"))


def iso_z(t: datetime) -> str:
    """Instant.toString: 2026-03-31T11:59:00Z."""
    return t.strftime("%Y-%m-%dT%H:%M:%SZ")


def minutes_of(d: timedelta) -> int:
    """Duration.toMinutes: whole minutes, truncated toward zero."""
    seconds = int(d.total_seconds())
    return seconds // 60 if seconds >= 0 else -((-seconds) // 60)


class Clock:
    def now(self) -> datetime:
        raise NotImplementedError


class ManualClock(Clock):
    def __init__(self, start: datetime) -> None:
        self._now = start

    def now(self) -> datetime:
        return self._now

    def advance(self, d: timedelta) -> None:
        if d < timedelta(0):
            raise ValueError("time does not go backwards")
        self._now = self._now + d


class PaymentState(Enum):
    CREATED = 0
    AUTHORIZED = 1
    CAPTURE_PENDING = 2
    CAPTURED = 3
    SETTLED = 4
    FAILED = 5

    def terminal(self) -> bool:
        return self is PaymentState.SETTLED or self is PaymentState.FAILED

    def __str__(self) -> str:
        return self.name


class AuthOutcome(Enum):
    """The three outcomes an availability SLI must tell apart: only PROCESSOR_ERROR is our failure."""

    APPROVED = 0
    ISSUER_DECLINE = 1
    PROCESSOR_ERROR = 2


class Severity(Enum):
    PAGE = "PAGE"
    TICKET = "TICKET"

    def __str__(self) -> str:
        return self.name


@dataclass(frozen=True)
class Alert:
    severity: Severity
    name: str
    detail: str


@dataclass(frozen=True)
class StuckPayment:
    payment_id: str
    processor: str
    state: PaymentState
    age: timedelta
    amount: Money


class MinuteCounter:
    """Counts per minute bucket, keyed by epoch minute. A window query sums the buckets whose minute is in
    (now - window, now]. Old buckets are dropped once they fall outside the longest window we query."""

    def __init__(self, width: int) -> None:
        self.buckets: dict[int, list[int]] = {}
        self.width = width

    @staticmethod
    def minute(at: datetime) -> int:
        return int(at.timestamp()) // 60  # Math.floorDiv(epochSecond, 60)

    def add(self, at: datetime, slot: int, n: int) -> None:
        counts = self.buckets.get(self.minute(at))
        if counts is None:
            counts = self.buckets[self.minute(at)] = [0] * self.width
        counts[slot] += n

    def sum(self, now: datetime, window: timedelta) -> list[int]:
        now_minute = self.minute(now)
        start = now_minute - minutes_of(window) + 1
        total = [0] * self.width
        for minute, counts in self.buckets.items():
            if start <= minute <= now_minute:
                for i in range(self.width):
                    total[i] += counts[i]
        return total

    def covers(self, now: datetime, window: timedelta) -> bool:
        """True when the counter has data reaching back at least the whole window."""
        if not self.buckets:
            return False
        return min(self.buckets) <= self.minute(now) - minutes_of(window) + 1

    def prune(self, now: datetime, keep: timedelta) -> None:
        cutoff = self.minute(now) - minutes_of(keep)
        for minute in [m for m in self.buckets if m < cutoff]:
            del self.buckets[minute]


class Ratio:
    """Integer basis-point ratio: 10,000 * num / den, rounded down. -1 when there is no data."""

    @staticmethod
    def bps(numerator: int, denominator: int) -> int:
        return -1 if denominator == 0 else numerator * 10_000 // denominator

    @staticmethod
    def pct(bps: int) -> str:
        if bps < 0:
            return "n/a"
        return f"{bps // 100}.{bps % 100:02d}%"


@dataclass(frozen=True)
class Slo:
    """Burn rate for an SLO with error budget given in parts per million (99.9% SLO = 1,000 ppm budget).
    Returned x100 so that 14.4 is 1440 and every comparison is an exact integer comparison."""

    name: str
    budget_ppm: int

    def burn_rate_x100(self, bad: int, total: int) -> int:
        if total == 0:
            return 0
        # (bad / total) / (budgetPpm / 1e6) * 100 = bad * 1e8 / (total * budgetPpm)
        return bad * 100_000_000 // (total * self.budget_ppm)


@dataclass(frozen=True)
class BurnRule:
    """One burn-rate rule from the SRE Workbook table: both windows must exceed the threshold."""

    severity: Severity
    long_window: timedelta
    short_window: timedelta
    threshold_x100: int


@dataclass(frozen=True)
class Tracked:
    processor: str
    amount: Money
    state: PaymentState
    entered_at: datetime


def x100(v: int) -> str:
    return f"{v // 100}.{v % 100:02d}"


class MoneyMovementMonitor:
    DEFAULT_BURN_RULES = (
        BurnRule(Severity.PAGE, timedelta(hours=1), timedelta(minutes=5), 1440),    # 2% of 30-day budget in 1h
        BurnRule(Severity.PAGE, timedelta(hours=6), timedelta(minutes=30), 600),    # 5% in 6h
        BurnRule(Severity.TICKET, timedelta(hours=24), timedelta(hours=2), 300),    # 10% in 1d
        BurnRule(Severity.TICKET, timedelta(hours=72), timedelta(hours=6), 100),    # 10% in 3d
    )

    AGE_BANDS_MINUTES = (1, 5, 15, 60, 24 * 60)

    @staticmethod
    def default_stuck_after() -> dict:
        return {
            PaymentState.CREATED: timedelta(minutes=30),          # abandoned checkout or lost auth response
            PaymentState.AUTHORIZED: timedelta(days=6),           # capture before the auth expires (~7 days)
            PaymentState.CAPTURE_PENDING: timedelta(minutes=15),  # capture sent, no answer
            PaymentState.CAPTURED: timedelta(days=3),             # should be in a settlement file by now
        }

    def __init__(self, clock: Clock, currency: Currency, stuck_after: Optional[dict] = None, slo: Optional[Slo] = None,
                 burn_rules=None, stuck_page_threshold: Optional[Money] = None, auth_drop_bps: int = 500,
                 min_attempts_for_rate: int = 100, recon_break_ticket_bps: int = 50) -> None:
        self.clock = clock
        self.currency = currency
        self.stuck_after = self.default_stuck_after() if stuck_after is None else stuck_after
        self.slo = Slo("auth availability 99.9%", 1_000) if slo is None else slo
        self.burn_rules = self.DEFAULT_BURN_RULES if burn_rules is None else burn_rules
        self.stuck_page_threshold = Money.of_minor(100_000, currency) if stuck_page_threshold is None else stuck_page_threshold
        self.auth_drop_bps = auth_drop_bps
        self.min_attempts_for_rate = min_attempts_for_rate
        self.recon_break_ticket_bps = recon_break_ticket_bps

        self.payments: dict[str, Tracked] = {}
        self.auth_by_processor: dict[str, MinuteCounter] = {}
        self.auth_all = MinuteCounter(len(AuthOutcome))
        self.recon_runs: list[tuple[int, int]] = []  # (matched, breaks)

    # ---- inputs -------------------------------------------------------------------------------

    def transition(self, payment_id: str, processor: str, amount: Money, to: PaymentState) -> None:
        """Record that a payment entered a state now. Terminal states stop tracking the payment."""
        if amount.currency != self.currency:
            raise CurrencyMismatchError(self.currency, amount.currency)
        if to.terminal():
            self.payments.pop(payment_id, None)
        else:
            self.payments[payment_id] = Tracked(processor, amount, to, self.clock.now())

    def auth_result(self, processor: str, outcome: AuthOutcome) -> None:
        counter = self.auth_by_processor.get(processor)
        if counter is None:
            counter = self.auth_by_processor[processor] = MinuteCounter(len(AuthOutcome))
        counter.add(self.clock.now(), outcome.value, 1)
        self.auth_all.add(self.clock.now(), outcome.value, 1)

    def recon_run(self, matched: int, breaks: int) -> None:
        self.recon_runs.append((matched, breaks))

    # ---- metrics ------------------------------------------------------------------------------

    def age_of(self, payment_id: str) -> Optional[timedelta]:
        t = self.payments.get(payment_id)
        return None if t is None else self.clock.now() - t.entered_at

    def stuck(self) -> list[StuckPayment]:
        """Payments whose time in their current state exceeds that state's limit, oldest first."""
        out = []
        for payment_id, t in self.payments.items():
            age = self.clock.now() - t.entered_at
            limit = self.stuck_after.get(t.state)
            if limit is not None and age > limit:
                out.append(StuckPayment(payment_id, t.processor, t.state, age, t.amount))
        out.sort(key=lambda s: (-s.age.total_seconds(), s.payment_id))
        return out

    def stuck_exposure(self) -> Money:
        total = Money.zero(self.currency)
        for s in self.stuck():
            total = total.plus(s.amount)
        return total

    def in_flight_exposure(self) -> dict:
        """Money in every non-terminal state: what is committed but not yet final."""
        out = {s: Money.zero(self.currency) for s in PaymentState if not s.terminal()}
        for t in self.payments.values():
            out[t.state] = out[t.state].plus(t.amount)
        return out

    def total_in_flight(self) -> Money:
        total = Money.zero(self.currency)
        for m in self.in_flight_exposure().values():
            total = total.plus(m)
        return total

    def state_age_histogram(self) -> dict:
        """Histogram of payments by state and age band: the shape of the queue, not just its length."""
        bands = self.AGE_BANDS_MINUTES
        out = {s: [0] * (len(bands) + 1) for s in PaymentState if not s.terminal()}
        for t in self.payments.values():
            minutes = minutes_of(self.clock.now() - t.entered_at)
            band = 0
            while band < len(bands) and minutes >= bands[band]:
                band += 1
            out[t.state][band] += 1
        return out

    def auth_rate_bps(self, processor: Optional[str], window: timedelta) -> int:
        """Approvals over all attempts, in basis points; -1 if fewer than min_attempts_for_rate attempts."""
        c = self._counts(processor, window)
        total = c[0] + c[1] + c[2]
        return -1 if total < self.min_attempts_for_rate else Ratio.bps(c[AuthOutcome.APPROVED.value], total)

    def attempts(self, processor: Optional[str], window: timedelta) -> int:
        c = self._counts(processor, window)
        return c[0] + c[1] + c[2]

    def _counts(self, processor: Optional[str], window: timedelta) -> list[int]:
        if processor is None:
            return self.auth_all.sum(self.clock.now(), window)
        mc = self.auth_by_processor.get(processor)
        return [0, 0, 0] if mc is None else mc.sum(self.clock.now(), window)

    def burn_rate_x100(self, window: timedelta) -> int:
        """Availability SLI burn rate: processor errors are bad, issuer declines are good events."""
        c = self.auth_all.sum(self.clock.now(), window)
        return self.slo.burn_rate_x100(c[AuthOutcome.PROCESSOR_ERROR.value], c[0] + c[1] + c[2])

    def recon_break_rate_bps(self, last_runs: int) -> int:
        """Breaks over records across the last n reconciliation runs, in basis points."""
        matched = breaks = 0
        for m, b in self.recon_runs[max(0, len(self.recon_runs) - last_runs):]:
            matched += m
            breaks += b
        return Ratio.bps(breaks, matched + breaks)

    def burn_rule_armed(self, r: BurnRule) -> bool:
        return self.auth_all.covers(self.clock.now(), r.long_window)

    def processors(self) -> list[str]:
        return sorted(self.auth_by_processor)  # TreeMap order

    # ---- alerts -------------------------------------------------------------------------------

    def evaluate_alerts(self) -> list[Alert]:
        out: list[Alert] = []
        for r in self.burn_rules:
            if not self.auth_all.covers(self.clock.now(), r.long_window):
                continue  # a half-empty long window is a short window
            lng, shrt = self.burn_rate_x100(r.long_window), self.burn_rate_x100(r.short_window)
            if lng >= r.threshold_x100 and shrt >= r.threshold_x100:
                out.append(Alert(r.severity, f"slo-burn-{minutes_of(r.long_window)}m",
                                 f"{self.slo.name} burning {x100(lng)} over {minutes_of(r.long_window)}m and {x100(shrt)} over "
                                 f"{minutes_of(r.short_window)}m (threshold {x100(r.threshold_x100)})"))
        stuck = self.stuck()
        if stuck:
            exposure = self.stuck_exposure()
            sev = Severity.PAGE if exposure >= self.stuck_page_threshold else Severity.TICKET
            oldest = stuck[0]
            out.append(Alert(sev, "stuck-payments", f"{len(stuck)} stuck, {exposure}; oldest {oldest.payment_id} in {oldest.state} "
                                                    f"for {minutes_of(oldest.age)}m"))
        fleet = self.auth_rate_bps(None, timedelta(minutes=15))
        for p in self.processors():
            rate = self.auth_rate_bps(p, timedelta(minutes=15))
            if rate >= 0 and fleet >= 0 and fleet - rate >= self.auth_drop_bps:
                out.append(Alert(Severity.PAGE, f"auth-rate-drop:{p}", f"{p} approves {Ratio.pct(rate)} vs fleet {Ratio.pct(fleet)} over 15m"))
        recon = self.recon_break_rate_bps(4)
        if recon >= self.recon_break_ticket_bps:
            out.append(Alert(Severity.TICKET, "recon-break-rate", f"break rate {Ratio.pct(recon)} over last 4 runs"))
        return out

    # ---- dashboard ----------------------------------------------------------------------------

    def dashboard(self) -> str:
        lines = [f"MONEY MOVEMENT  {iso_z(self.clock.now())}\n", f"in flight {self.total_in_flight()}\n"]
        hist = self.state_age_histogram()
        lines.append(f"  {'state':<16} {'amount':<14} {'<1m':>6} {'1-5m':>6} {'5-15m':>6} {'15-60m':>6} {'1-24h':>6} {'>24h':>6}\n")
        for state, amount in self.in_flight_exposure().items():
            h = hist[state]
            lines.append(f"  {state.name:<16} {str(amount):<14} {h[0]:>6} {h[1]:>6} {h[2]:>6} {h[3]:>6} {h[4]:>6} {h[5]:>6}\n")
        lines.append(f"auth rate        {'5m':<10} {'1h':<10} {'attempts 1h':<10}\n")
        for p in self.processors():
            lines.append(f"  {p:<14} {Ratio.pct(self.auth_rate_bps(p, timedelta(minutes=5))):<10} "
                         f"{Ratio.pct(self.auth_rate_bps(p, timedelta(hours=1))):<10} {self.attempts(p, timedelta(hours=1))}\n")
        lines.append(f"slo burn         5m {x100(self.burn_rate_x100(timedelta(minutes=5)))}  1h {x100(self.burn_rate_x100(timedelta(hours=1)))}"
                     f"  6h {x100(self.burn_rate_x100(timedelta(hours=6)))}\n")
        lines.append(f"recon breaks     {Ratio.pct(self.recon_break_rate_bps(4))} (last 4 runs)\n")
        alerts = self.evaluate_alerts()
        if not alerts:
            lines.append("alerts           none\n")
        for a in alerts:
            lines.append(f"ALERT {a.severity} {a.name}: {a.detail}\n")
        return "".join(lines)


# ---------------------------------------------------------------------------------------------
# Simulation
# ---------------------------------------------------------------------------------------------


class JavaRandom:
    """java.util.Random, bit for bit (the JDK's 48-bit LCG), so seed 42 produces the same eight hours as the Java demo."""

    MULTIPLIER = 0x5DEECE66D
    ADDEND = 0xB
    MASK = (1 << 48) - 1

    def __init__(self, seed: int) -> None:
        self.seed = (seed ^ self.MULTIPLIER) & self.MASK

    def next(self, bits: int) -> int:
        self.seed = (self.seed * self.MULTIPLIER + self.ADDEND) & self.MASK
        value = self.seed >> (48 - bits)
        return value - (1 << 32) if value >= (1 << 31) else value  # Java's int is signed

    def next_int(self, bound: int) -> int:
        if bound <= 0:
            raise ValueError("bound must be positive")
        r = self.next(31)
        m = bound - 1
        if (bound & m) == 0:  # power of two
            return (bound * r) >> 31
        u = r
        r = u % bound
        while _to_int32(u - r + m) < 0:  # the overflow check Java performs in 32-bit arithmetic
            u = self.next(31)
            r = u % bound
        return r

    def next_double(self) -> float:
        return ((self.next(26) << 27) + self.next(27)) * (1.0 / (1 << 53))


def _to_int32(x: int) -> int:
    x &= 0xFFFFFFFF
    return x - (1 << 32) if x >= (1 << 31) else x


class Simulation:
    """A seeded eight-hour day from 06:00: two processors, 40 attempts a minute, an hourly settlement run,
    three lost capture webhooks at 12:00, and a partial outage on proc_b from 13:00. Records the first
    minute each alert fires. Six quiet hours come first so the 6h burn window has full coverage."""

    START = instant("2026-03-31T06:00:00Z")
    MINUTES = 480
    LOST_WEBHOOK_MINUTE = 360
    OUTAGE_MINUTE = 420

    def __init__(self, seed: int) -> None:
        self.rnd = JavaRandom(seed)
        self.clock = ManualClock(self.START)
        self.monitor = MoneyMovementMonitor(self.clock, USD)
        self.first_fired: dict[str, int] = {}
        self.snapshots: dict[int, str] = {}
        self._scheduled: dict[int, list[Callable[[], None]]] = {}
        self._recon_matched = 0
        self._recon_breaks = 0
        self._seq = 0
        self._captured: dict[str, Money] = {}

    def _at(self, minute: int, action: Callable[[], None]) -> None:
        self._scheduled.setdefault(minute, []).append(action)

    def run(self, minutes: int, snapshot_minutes: set) -> None:
        for minute in range(minutes):
            for processor in ("proc_a", "proc_b"):
                for _ in range(20):
                    self._attempt(minute, processor)
            if minute == self.LOST_WEBHOOK_MINUTE:
                for amount in (40_000, 35_000, 30_000):
                    self.monitor.transition(f"pay_lost_{amount}", "proc_a", Money.of_minor(amount, USD), PaymentState.CAPTURE_PENDING)
            for action in self._scheduled.pop(minute, []):
                action()
            if minute % 60 == 59:
                # hourly settlement file in this simulation: everything captured so far settles
                for payment_id, amt in self._captured.items():
                    self.monitor.transition(payment_id, "settle", amt, PaymentState.SETTLED)
                self._captured.clear()
            if minute % 30 == 29:
                self.monitor.recon_run(self._recon_matched, self._recon_breaks)
                self._recon_matched = 0
                self._recon_breaks = 0
            for a in self.monitor.evaluate_alerts():
                self.first_fired.setdefault(a.name, minute)
            if minute in snapshot_minutes:
                self.snapshots[minute] = self.monitor.dashboard()
            self.clock.advance(timedelta(minutes=1))

    def _attempt(self, minute: int, processor: str) -> None:
        payment_id = f"pay_{self._seq:06d}"
        self._seq += 1
        amount = Money.of_minor(500 + self.rnd.next_int(49_500), USD)
        u = self.rnd.next_double()
        degraded = processor == "proc_b" and minute >= self.OUTAGE_MINUTE
        error_p = 0.25 if degraded else 0.0005
        outcome = (AuthOutcome.PROCESSOR_ERROR if u < error_p
                   else AuthOutcome.ISSUER_DECLINE if u < error_p + 0.08
                   else AuthOutcome.APPROVED)
        monitor = self.monitor
        monitor.transition(payment_id, processor, amount, PaymentState.CREATED)
        monitor.auth_result(processor, outcome)
        if outcome is AuthOutcome.APPROVED:
            monitor.transition(payment_id, processor, amount, PaymentState.AUTHORIZED)
            self._at(minute + 1, lambda: monitor.transition(payment_id, processor, amount, PaymentState.CAPTURE_PENDING))

            def capture() -> None:
                monitor.transition(payment_id, processor, amount, PaymentState.CAPTURED)
                self._captured[payment_id] = amount
                self._recon_matched += 1

            self._at(minute + 2, capture)
        elif outcome is AuthOutcome.ISSUER_DECLINE:
            monitor.transition(payment_id, processor, amount, PaymentState.FAILED)
        else:
            monitor.transition(payment_id, processor, amount, PaymentState.FAILED)
            # A timeout is not a decline: one in five was actually approved at the processor,
            # which reconciliation later reports as a capture we do not have.
            if self.rnd.next_int(5) == 0:
                self._recon_breaks += 1


def main() -> None:
    print("== Burn rate by hand: 99.9% SLO, budget 1,000 ppm ==")
    slo = Slo("auth availability 99.9%", 1_000)
    print(f"5m: 6 errors / 200 attempts   -> burn {x100(slo.burn_rate_x100(6, 200))}")
    print(f"1h: 40 errors / 2,400 attempts -> burn {x100(slo.burn_rate_x100(40, 2_400))}")
    print("page rule needs both >= 14.40: fires")

    print("\n== Eight hours of traffic, seed 42 ==")
    sim = Simulation(42)
    sim.run(Simulation.MINUTES, {359, 380, 430})
    print(f"lost capture webhooks injected at minute {Simulation.LOST_WEBHOOK_MINUTE}"
          f"; proc_b outage starts at minute {Simulation.OUTAGE_MINUTE}")
    for name, minute in sim.first_fired.items():
        when = (Simulation.START + timedelta(minutes=minute)).strftime("%H:%M")
        print(f"  first fired {name:<22} minute {minute:>3} ({when})")
    for minute in sorted(sim.snapshots):
        print(f"\n-- dashboard at minute {minute} --")
        print(sim.snapshots[minute], end="")


if __name__ == "__main__":
    main()
