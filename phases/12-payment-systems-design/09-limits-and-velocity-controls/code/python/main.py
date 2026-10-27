"""Lesson: phases/12-payment-systems-design/09-limits-and-velocity-controls/docs/en.md
Limits, Velocity Controls, and Exposure
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: PCI DSS v4.0 (card testing guidance); Visa Core Rules (authorization velocity and
         excessive-decline programs); RFC 9110 (429 and Retry-After).
Python 3.11+, standard library only. Same design as the Java version.
"""

from __future__ import annotations

import threading
from collections import deque
from dataclasses import dataclass
from enum import Enum
from typing import Optional, Protocol


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
    def zero(currency: Currency) -> "Money":
        return Money(0, currency)

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

    def minus(self, other: "Money") -> "Money":
        self._require_same_currency(other)
        return Money(self.minor_units - other.minor_units, self.currency)

    def is_positive(self) -> bool:
        return self.minor_units > 0

    def is_greater_than(self, other: "Money") -> bool:
        return self.compare_to(other) > 0

    def compare_to(self, other: "Money") -> int:
        self._require_same_currency(other)
        return (self.minor_units > other.minor_units) - (self.minor_units < other.minor_units)

    def __str__(self) -> str:
        magnitude = abs(self.minor_units)
        whole, frac = divmod(magnitude, self.currency.minor_units_per_major())
        text = str(whole)
        if self.currency.exponent > 0:
            text += "." + str(frac).rjust(self.currency.exponent, "0")
        return f"{'-' if self.minor_units < 0 else ''}{text} {self.currency.code}"

    __repr__ = __str__


# ---------------------------------------------------------------------------------------------
# Time is injected. Every window boundary in this lesson is computed from clock.now_millis().
# ---------------------------------------------------------------------------------------------


class Clock(Protocol):
    def now_millis(self) -> int: ...


class FakeClock:
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
# The attempt being judged, and the dimensions a limit can key on.
# ---------------------------------------------------------------------------------------------


@dataclass(frozen=True)
class Attempt:
    """One authorization attempt as the limit engine sees it. The card is identified by a fingerprint, never a PAN."""

    attempt_id: str
    merchant_id: str
    card_fingerprint: str
    amount: Money

    def __post_init__(self) -> None:
        if not self.amount.is_positive():
            raise ValueError(f"amount must be positive: {self.amount}")


class LimitScope(Enum):
    """What a limit counts against. The scope key is the string the window is bucketed by."""

    CARD = "card"
    MERCHANT = "merchant"
    CARD_AT_MERCHANT = "card_at_merchant"
    GLOBAL = "global"

    def key_for(self, a: Attempt) -> str:
        if self is LimitScope.CARD:
            return f"card:{a.card_fingerprint}"
        if self is LimitScope.MERCHANT:
            return f"merchant:{a.merchant_id}"
        if self is LimitScope.CARD_AT_MERCHANT:
            return f"merchant:{a.merchant_id}|card:{a.card_fingerprint}"
        return "global"


class LimitKind(Enum):
    """What a limit measures over its window. EXPOSURE has no window: it is the sum of currently open authorizations."""

    COUNT = "count"
    AMOUNT = "amount"
    EXPOSURE = "exposure"

    def __str__(self) -> str:
        return self.name


class Severity(Enum):
    """HARD breaches deny. SOFT breaches allow but flag for review (step-up, manual review, delayed capture)."""

    HARD = "hard"
    SOFT = "soft"

    def __str__(self) -> str:
        return self.name


@dataclass(frozen=True)
class Limit:
    """A limit: at most `threshold` units (attempts, or minor units of money) per scope key within
    `window_millis`. `max_amount` is not None for AMOUNT and EXPOSURE limits, and the currency it
    carries is the only currency the limit can judge: a limit in USD does not see EUR attempts."""

    id: str
    scope: LimitScope
    kind: LimitKind
    window_millis: int
    max_count: int
    max_amount: Optional[Money]
    severity: Severity

    def __post_init__(self) -> None:
        if self.kind is LimitKind.COUNT and self.max_count <= 0:
            raise ValueError("COUNT limit needs max_count > 0")
        if self.kind is not LimitKind.COUNT and (self.max_amount is None or not self.max_amount.is_positive()):
            raise ValueError(f"{self.kind.name} limit needs a positive max_amount")
        if self.kind is not LimitKind.EXPOSURE and self.window_millis <= 0:
            raise ValueError(f"window must be positive for {self.kind.name}")

    @staticmethod
    def count(id: str, scope: LimitScope, window_millis: int, max_count: int, severity: Severity) -> "Limit":
        return Limit(id, scope, LimitKind.COUNT, window_millis, max_count, None, severity)

    @staticmethod
    def amount(id: str, scope: LimitScope, window_millis: int, max_amount: Money, severity: Severity) -> "Limit":
        return Limit(id, scope, LimitKind.AMOUNT, window_millis, 0, max_amount, severity)

    @staticmethod
    def exposure(id: str, scope: LimitScope, max_amount: Money, severity: Severity) -> "Limit":
        return Limit(id, scope, LimitKind.EXPOSURE, 0, 0, max_amount, severity)

    def applies(self, a: Attempt) -> bool:
        """Does this limit apply to the attempt at all? Money limits only judge their own currency."""
        return self.kind is LimitKind.COUNT or (self.max_amount is not None and self.max_amount.currency == a.amount.currency)


# ---------------------------------------------------------------------------------------------
# The sliding window: a deque of (timestamp, amount) entries, evicted lazily. The window is the
# half-open interval (now - W, now]: an entry exactly W old has left the window.
# ---------------------------------------------------------------------------------------------


@dataclass(frozen=True)
class Entry:
    at_millis: int
    amount_minor: int


class SlidingWindow:
    def __init__(self, window_millis: int) -> None:
        if window_millis <= 0:
            raise ValueError("window must be positive")
        self._window_millis = window_millis
        self._entries: deque[Entry] = deque()
        self._sum_minor = 0  # maintained incrementally so sum() is O(evicted), not O(n)

    def evict(self, now: int) -> None:
        """Drop every entry that has fallen out of (now - W, now]. Amortized O(1) per entry ever added."""
        cutoff = now - self._window_millis
        while self._entries and self._entries[0].at_millis <= cutoff:
            self._sum_minor -= self._entries.popleft().amount_minor

    def add(self, now: int, amount_minor: int) -> None:
        if self._entries and now < self._entries[-1].at_millis:
            raise ValueError("entries must be appended in time order; clock went backwards")
        self.evict(now)
        self._entries.append(Entry(now, amount_minor))
        self._sum_minor += amount_minor

    def count(self, now: int) -> int:
        self.evict(now)
        return len(self._entries)

    def sum_minor(self, now: int) -> int:
        self.evict(now)
        return self._sum_minor

    def next_expiry_millis(self, now: int) -> Optional[int]:
        """When the oldest entry leaves the window, i.e. the earliest instant a COUNT breach can clear. None if empty."""
        self.evict(now)
        return None if not self._entries else self._entries[0].at_millis + self._window_millis


# ---------------------------------------------------------------------------------------------
# Exposure: money authorized and not yet captured, voided, or expired. No window: it is a balance.
# ---------------------------------------------------------------------------------------------


class ExposureTracker:
    def __init__(self) -> None:
        self._open_by_scope_key: dict[str, dict[str, Money]] = {}  # scope_key -> attempt_id -> amount

    def open(self, scope_key: str, currency: Currency) -> Money:
        total = Money.zero(currency)
        for m in self._open_by_scope_key.get(scope_key, {}).values():
            if m.currency == currency:
                total = total.plus(m)
        return total

    def add(self, scope_key: str, attempt_id: str, amount: Money) -> None:
        self._open_by_scope_key.setdefault(scope_key, {})[attempt_id] = amount

    def release(self, scope_key: str, attempt_id: str) -> None:
        """Capture, void, or expiry releases the exposure. Unknown ids are ignored: release is idempotent."""
        open_ = self._open_by_scope_key.get(scope_key)
        if open_ is not None:
            open_.pop(attempt_id, None)


# ---------------------------------------------------------------------------------------------
# Decisions carry their reasons. A denial you cannot explain is a support ticket you cannot close.
# ---------------------------------------------------------------------------------------------


class Outcome(Enum):
    ALLOW = "allow"
    REVIEW = "review"
    DENY = "deny"

    def __str__(self) -> str:
        return self.name


@dataclass(frozen=True)
class Reason:
    """One limit's verdict on one attempt: what was observed, what the attempt would make it, and the threshold."""

    limit_id: str
    scope: LimitScope
    scope_key: str
    kind: LimitKind
    severity: Severity
    observed: str
    would_be: str
    threshold: str
    window_millis: int
    retry_after_millis: Optional[int]

    def breached(self) -> bool:
        return self.severity is not None

    def explain(self) -> str:
        window = "open" if self.kind is LimitKind.EXPOSURE else f"in {self.window_millis // 1000}s"
        retry = "" if self.retry_after_millis is None else f", clears in {self.retry_after_millis // 1000}s"
        return (f"{self.limit_id} [{self.severity.name}] {self.scope_key}: {self.observed} -> {self.would_be} "
                f"exceeds {self.threshold} {window}{retry}")


@dataclass(frozen=True)
class Decision:
    outcome: Outcome
    reasons: tuple[Reason, ...]

    def allowed(self) -> bool:
        return self.outcome is not Outcome.DENY

    def explanations(self) -> list[str]:
        """The explanation a support engineer, a merchant dashboard, or an audit log needs. One line per breached limit."""
        return [r.explain() for r in self.reasons]

    def retry_after_seconds(self) -> int:
        """Seconds until the earliest HARD breach clears, for a Retry-After header. 0 if nothing is time-bound."""
        candidates = [r.retry_after_millis for r in self.reasons
                      if r.severity is Severity.HARD and r.retry_after_millis is not None]
        if not candidates:
            return 0
        return (min(candidates) + 999) // 1000


# ---------------------------------------------------------------------------------------------
# The limit hierarchy: platform defaults, overridden per merchant by limit id.
# ---------------------------------------------------------------------------------------------


class LimitPolicy:
    def __init__(self) -> None:
        self._defaults: dict[str, Limit] = {}
        self._merchant_overrides: dict[str, dict[str, Limit]] = {}

    def add_default(self, limit: Limit) -> "LimitPolicy":
        if limit.id in self._defaults:
            raise ValueError(f"duplicate limit id {limit.id}")
        self._defaults[limit.id] = limit
        return self

    def override(self, merchant_id: str, limit: Limit) -> "LimitPolicy":
        """A merchant override replaces the default with the same id; it never relaxes a limit the default does not have."""
        if limit.id not in self._defaults:
            raise ValueError(f"no default limit to override: {limit.id}")
        self._merchant_overrides.setdefault(merchant_id, {})[limit.id] = limit
        return self

    def limits_for(self, merchant_id: str) -> tuple[Limit, ...]:
        """The effective limits for a merchant, in the order the defaults were declared."""
        overrides = self._merchant_overrides.get(merchant_id, {})
        return tuple(overrides.get(d.id, d) for d in self._defaults.values())


# ---------------------------------------------------------------------------------------------
# The engine: evaluate every applicable limit, then record the attempt, under one lock.
# ---------------------------------------------------------------------------------------------


class LimitEngine:
    def __init__(self, policy: LimitPolicy, clock: Clock) -> None:
        self._policy = policy
        self._clock = clock
        self._windows: dict[str, SlidingWindow] = {}  # limit_id + "@" + scope_key -> window
        self._exposure = ExposureTracker()
        self._open_attempts: dict[str, Attempt] = {}  # attempt_id -> attempt, for release()
        self._lock = threading.Lock()

    def _window_for(self, limit: Limit, scope_key: str) -> SlidingWindow:
        key = f"{limit.id}@{scope_key}"
        w = self._windows.get(key)
        if w is None:
            w = SlidingWindow(limit.window_millis)
            self._windows[key] = w
        return w

    def check(self, attempt: Attempt) -> Decision:
        """Judge one attempt against every effective limit and, if not denied, record it in every window
        and in exposure. Check-and-record is one critical section so two concurrent attempts cannot
        both see "4 of 5" and both pass. All breaches are collected, not just the first: a denial
        must name every limit it tripped so the explanation is complete."""
        with self._lock:
            now = self._clock.now_millis()
            reasons: list[Reason] = []
            outcome = Outcome.ALLOW
            for limit in self._policy.limits_for(attempt.merchant_id):
                if not limit.applies(attempt):
                    continue
                r = self._evaluate(limit, attempt, now)
                if r is None:
                    continue
                reasons.append(r)
                if limit.severity is Severity.HARD:
                    outcome = Outcome.DENY
                elif outcome is Outcome.ALLOW:
                    outcome = Outcome.REVIEW
            if outcome is not Outcome.DENY:
                self._record(attempt, now)
            return Decision(outcome, tuple(reasons))

    def _evaluate(self, limit: Limit, attempt: Attempt, now: int) -> Optional[Reason]:
        scope_key = limit.scope.key_for(attempt)
        if limit.kind is LimitKind.COUNT:
            w = self._window_for(limit, scope_key)
            observed = w.count(now)
            if observed + 1 <= limit.max_count:
                return None
            expiry = w.next_expiry_millis(now)
            return Reason(limit.id, limit.scope, scope_key, limit.kind, limit.severity,
                          f"{observed} attempts", f"{observed + 1} attempts", f"{limit.max_count} attempts",
                          limit.window_millis, None if expiry is None else expiry - now)
        assert limit.max_amount is not None
        if limit.kind is LimitKind.AMOUNT:
            w = self._window_for(limit, scope_key)
            observed_m = Money.of_minor(w.sum_minor(now), attempt.amount.currency)
            would_be = observed_m.plus(attempt.amount)
            if not would_be.is_greater_than(limit.max_amount):
                return None
            expiry = w.next_expiry_millis(now)
            return Reason(limit.id, limit.scope, scope_key, limit.kind, limit.severity,
                          str(observed_m), str(would_be), str(limit.max_amount),
                          limit.window_millis, None if expiry is None else expiry - now)
        # EXPOSURE
        observed_m = self._exposure.open(scope_key, attempt.amount.currency)
        would_be = observed_m.plus(attempt.amount)
        if not would_be.is_greater_than(limit.max_amount):
            return None
        return Reason(limit.id, limit.scope, scope_key, limit.kind, limit.severity,
                      str(observed_m), str(would_be), str(limit.max_amount), 0, None)

    def _record(self, attempt: Attempt, now: int) -> None:
        for limit in self._policy.limits_for(attempt.merchant_id):
            if not limit.applies(attempt):
                continue
            scope_key = limit.scope.key_for(attempt)
            if limit.kind is LimitKind.EXPOSURE:
                self._exposure.add(scope_key, attempt.attempt_id, attempt.amount)
            else:
                self._window_for(limit, scope_key).add(now, attempt.amount.minor_units)
        self._open_attempts[attempt.attempt_id] = attempt

    def release(self, attempt_id: str) -> None:
        """Capture, void, or authorization expiry: the attempt no longer counts toward exposure. Velocity history is untouched."""
        with self._lock:
            attempt = self._open_attempts.pop(attempt_id, None)
            if attempt is None:
                return
            for limit in self._policy.limits_for(attempt.merchant_id):
                if limit.kind is LimitKind.EXPOSURE:
                    self._exposure.release(limit.scope.key_for(attempt), attempt_id)

    def open_exposure(self, limit: Limit, sample: Attempt) -> Money:
        with self._lock:
            return self._exposure.open(limit.scope.key_for(sample), sample.amount.currency)

    def window_count(self, limit: Limit, sample: Attempt) -> int:
        with self._lock:
            return self._window_for(limit, limit.scope.key_for(sample)).count(self._clock.now_millis())


# ---------------------------------------------------------------------------------------------
# Demo
# ---------------------------------------------------------------------------------------------

SECOND = 1000
MINUTE = 60 * SECOND
HOUR = 60 * MINUTE
DAY = 24 * HOUR


def standard_policy() -> LimitPolicy:
    return (LimitPolicy()
            .add_default(Limit.count("card-attempts-1h", LimitScope.CARD, HOUR, 5, Severity.HARD))
            .add_default(Limit.amount("card-amount-24h", LimitScope.CARD, DAY, Money.parse("500.00", USD), Severity.SOFT))
            .add_default(Limit.amount("merchant-amount-24h", LimitScope.MERCHANT, DAY, Money.parse("10000.00", USD), Severity.HARD))
            .add_default(Limit.exposure("merchant-exposure", LimitScope.MERCHANT, Money.parse("2000.00", USD), Severity.HARD))
            .override("merchant_bigbox", Limit.amount("merchant-amount-24h", LimitScope.MERCHANT, DAY, Money.parse("250000.00", USD), Severity.HARD))
            .override("merchant_bigbox", Limit.exposure("merchant-exposure", LimitScope.MERCHANT, Money.parse("50000.00", USD), Severity.HARD)))


def show(label: str, d: Decision) -> None:
    print(f"{label} -> {d.outcome.name}")
    for line in d.explanations():
        print(f"    {line}")


def main() -> None:
    clock = FakeClock(1_700_000_000_000)
    engine = LimitEngine(standard_policy(), clock)
    acme, card = "merchant_acme", "fp_4242"

    print("== Card testing: 5 attempts per card per hour, hard ==")
    for i in range(1, 7):
        d = engine.check(Attempt(f"att-{i}", acme, card, Money.parse("1.00", USD)))
        show(f"attempt {i} (1.00 USD)", d)
        if d.outcome is Outcome.DENY:
            print(f"    Retry-After: {d.retry_after_seconds()}s")
        clock.advance(10 * SECOND)

    print("\n== The window slides: 1h after the first attempt, one slot frees ==")
    clock.advance(HOUR - 60 * SECOND)  # now exactly 1h after attempt 1
    show("attempt 7 at t0+1h", engine.check(Attempt("att-7", acme, card, Money.parse("1.00", USD))))
    show("attempt 8 at t0+1h", engine.check(Attempt("att-8", acme, card, Money.parse("1.00", USD))))

    print("\n== Soft limit: a big ticket is allowed but flagged for review ==")
    clock.advance(2 * HOUR)
    card2 = "fp_1881"
    show("480.00 USD", engine.check(Attempt("att-9", acme, card2, Money.parse("480.00", USD))))
    show("30.00 USD (would be 510.00 of 500.00)", engine.check(Attempt("att-10", acme, card2, Money.parse("30.00", USD))))

    print("\n== Exposure: open authorizations count until captured or voided ==")
    card3 = "fp_7777"
    show("1,400.00 USD auth", engine.check(Attempt("att-11", acme, card3, Money.parse("1400.00", USD))))
    show("200.00 USD auth (open exposure 1,916.00)", engine.check(Attempt("att-12", acme, "fp_7778", Money.parse("200.00", USD))))
    engine.release("att-11")
    print("captured att-11: exposure back to " + str(engine.open_exposure(
        standard_policy().limits_for(acme)[3], Attempt("x", acme, card3, Money.parse("1.00", USD)))))
    show("200.00 USD auth after capture", engine.check(Attempt("att-13", acme, "fp_7779", Money.parse("200.00", USD))))

    print("\n== Hierarchy: bigbox overrides the merchant limits, keeps the card limits ==")
    show("bigbox 15,000.00 USD", engine.check(Attempt("att-14", "merchant_bigbox", "fp_9001", Money.parse("15000.00", USD))))
    show("acme   15,000.00 USD", engine.check(Attempt("att-15", acme, "fp_9002", Money.parse("15000.00", USD))))

    print("\n== A denial names every limit it tripped ==")
    multi = engine.check(Attempt("att-16", acme, card, Money.parse("1400.00", USD)))
    show("card fp_4242, 1,400.00 USD", multi)
    print(f"breached limits: {len(multi.reasons)}")


if __name__ == "__main__":
    main()
