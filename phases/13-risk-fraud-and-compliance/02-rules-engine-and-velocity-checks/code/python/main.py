"""Lesson: phases/13-risk-fraud-and-compliance/02-rules-engine-and-velocity-checks/docs/en.md
A Rules Engine with Velocity Checks
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: ISO 4217 (currency exponents); ISO/IEC 7812 (issuer identification numbers / BINs);
         PCI DSS v4.0 Requirement 3 for why a card is identified by a token, never a PAN.
Python 3.11+, standard library only. Same design as the Java and Go versions.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from enum import Enum
from functools import total_ordering
from typing import Any

# ---------------------------------------------------------------------------------------------
# Money, copied from Phase 10 Lesson 01: integer minor units paired with an ISO 4217 currency.
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


class CurrencyMismatchError(ValueError):
    def __init__(self, a: Currency, b: Currency) -> None:
        super().__init__(f"cannot combine {a.code} with {b.code}")


@total_ordering
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
# Time is injected. Nothing in this file calls time.time().
# ---------------------------------------------------------------------------------------------


class ManualClock:
    """A clock the test or demo moves by hand. Monotone: advance() never goes backwards."""

    def __init__(self, start_millis: int) -> None:
        self._now = start_millis

    def now_millis(self) -> int:
        return self._now

    def advance(self, millis: int) -> None:
        if millis < 0:
            raise ValueError("clock never goes backwards")
        self._now += millis


# ---------------------------------------------------------------------------------------------
# The input: one authorization attempt. The card is a token (PCI DSS), never a PAN.
# ---------------------------------------------------------------------------------------------


@dataclass(frozen=True)
class Transaction:
    id: str
    account_id: str
    card_token: str
    bin: str
    country: str
    card_age_days: int
    amount: Money

    def facts(self) -> dict[str, Any]:
        """The facts a rule can see before velocity enrichment. Keys are the rule `field` names."""
        return {
            "amount": self.amount,
            "bin": self.bin,
            "country": self.country,
            "card_age_days": self.card_age_days,
            "account_id": self.account_id,
        }


# ---------------------------------------------------------------------------------------------
# Velocity: a sliding window approximated by a ring of time buckets.
# ---------------------------------------------------------------------------------------------


class _Ring:
    __slots__ = ("starts", "counts", "sums")

    def __init__(self, n: int) -> None:
        self.starts = [None] * n  # None marks a never-used slot
        self.counts = [0] * n
        self.sums = [0] * n


class SlidingWindowCounter:
    """Counts and sums events per key over a sliding window of W ms split into n buckets of
    width b = W / n. Recording lands in bucket floor(now / b); a slot holding an older lap is
    reset first. Queries add every slot whose start is newer than now - W. Memory per key is
    O(n); the effective window is between W - b and W."""

    def __init__(self, window_millis: int, buckets: int, currency: Currency) -> None:
        if buckets <= 0:
            raise ValueError("buckets must be positive")
        if window_millis <= 0 or window_millis % buckets != 0:
            raise ValueError("window must be a positive multiple of the bucket count")
        self.window_millis = window_millis
        self.bucket_millis = window_millis // buckets
        self.buckets = buckets
        self.currency = currency
        self._rings: dict[str, _Ring] = {}

    def record(self, key: str, amount: Money, now_millis: int) -> None:
        if amount.currency != self.currency:
            raise CurrencyMismatchError(self.currency, amount.currency)
        ring = self._rings.setdefault(key, _Ring(self.buckets))
        lap = now_millis // self.bucket_millis          # Python // floors; timestamps are non-negative here
        bucket_start = lap * self.bucket_millis
        idx = lap % self.buckets
        if ring.starts[idx] != bucket_start:            # slot holds a previous lap: reset it
            ring.starts[idx] = bucket_start
            ring.counts[idx] = 0
            ring.sums[idx] = 0
        ring.counts[idx] += 1
        ring.sums[idx] += amount.minor_units

    def _live(self, ring: _Ring, now_millis: int):
        cutoff = now_millis - self.window_millis
        return (i for i, start in enumerate(ring.starts) if start is not None and start > cutoff)

    def count(self, key: str, now_millis: int) -> int:
        ring = self._rings.get(key)
        if ring is None:
            return 0
        return sum(ring.counts[i] for i in self._live(ring, now_millis))

    def sum(self, key: str, now_millis: int) -> Money:
        ring = self._rings.get(key)
        total = 0 if ring is None else sum(ring.sums[i] for i in self._live(ring, now_millis))
        return Money.of_minor(total, self.currency)

    def evict_idle(self, now_millis: int) -> int:
        """Drop keys with no live bucket. For a housekeeping job, never the hot path."""
        before = len(self._rings)
        self._rings = {k: r for k, r in self._rings.items() if any(True for _ in self._live(r, now_millis))}
        return before - len(self._rings)

    def tracked_keys(self) -> int:
        return len(self._rings)


class Dimension(Enum):
    CARD = "card"
    ACCOUNT = "account"

    def key_for(self, tx: Transaction) -> str:
        return f"card:{tx.card_token}" if self is Dimension.CARD else f"account:{tx.account_id}"


class Metric(Enum):
    COUNT = "count"
    SUM = "sum"


@dataclass(frozen=True)
class Definition:
    fact_name: str
    dimension: Dimension
    metric: Metric
    counter: SlidingWindowCounter


class VelocityService:
    """Named counters, each keyed by one transaction dimension. `enrich` records the attempt and
    adds one fact per counter so the engine sees velocity as just another field."""

    def __init__(self) -> None:
        self._definitions: list[Definition] = []

    def define(self, fact_name: str, dimension: Dimension, metric: Metric, counter: SlidingWindowCounter) -> "VelocityService":
        if any(d.fact_name == fact_name for d in self._definitions):
            raise ValueError(f"duplicate fact: {fact_name}")
        self._definitions.append(Definition(fact_name, dimension, metric, counter))
        return self

    def enrich(self, tx: Transaction, now_millis: int) -> dict[str, Any]:
        """Record the attempt, then return the transaction facts plus one velocity fact per counter.
        The attempt counts itself; declined attempts are recorded too (a burst of declines is the
        card-testing signal)."""
        facts = tx.facts()
        for d in self._definitions:
            key = d.dimension.key_for(tx)
            d.counter.record(key, tx.amount, now_millis)
            facts[d.fact_name] = d.counter.count(key, now_millis) if d.metric is Metric.COUNT else d.counter.sum(key, now_millis)
        return facts


# ---------------------------------------------------------------------------------------------
# Rules as data: field, operator, value, action, priority, score, explanation.
# ---------------------------------------------------------------------------------------------


class Action(Enum):
    ALLOW = 0
    REVIEW = 1
    DECLINE = 2


def _type_name(o: Any) -> str:
    return "null" if o is None else type(o).__name__


def _compare(a: Any, b: Any) -> int:
    """Order two facts of the same kind. int vs int, Money vs Money; anything else is a config bug.
    bool is excluded because True is an int in Python."""
    ints = (isinstance(a, int) and not isinstance(a, bool)) and (isinstance(b, int) and not isinstance(b, bool))
    if ints or (isinstance(a, Money) and isinstance(b, Money)):
        return (a > b) - (a < b)  # Money.__lt__ raises on currency mismatch
    raise ValueError(f"cannot order {_type_name(a)} against {_type_name(b)}")


def _as_set(expected: Any) -> frozenset:
    if isinstance(expected, (set, frozenset)):
        return frozenset(expected)
    raise ValueError(f"IN / NOT_IN need a set, got {_type_name(expected)}")


class Operator(Enum):
    EQ = "EQ"
    NE = "NE"
    GT = "GT"
    GTE = "GTE"
    LT = "LT"
    LTE = "LTE"
    IN = "IN"
    NOT_IN = "NOT_IN"

    def apply(self, observed: Any, expected: Any) -> bool:
        if self is Operator.EQ:
            return observed == expected
        if self is Operator.NE:
            return observed != expected
        if self is Operator.IN:
            return observed in _as_set(expected)
        if self is Operator.NOT_IN:
            return observed not in _as_set(expected)
        cmp = _compare(observed, expected)
        return {Operator.GT: cmp > 0, Operator.GTE: cmp >= 0, Operator.LT: cmp < 0, Operator.LTE: cmp <= 0}[self]


def _fmt(value: Any) -> str:
    if isinstance(value, frozenset):
        return "[" + ", ".join(sorted(value)) + "]"
    return "<absent>" if value is None else str(value)


@dataclass(frozen=True)
class Rule:
    """One rule. Lower priority number is evaluated and, under FIRST_MATCH, decided first.
    `score` is only used under SCORE. `explanation` is what an analyst or customer reads."""

    name: str
    field: str
    operator: Operator
    value: Any
    action: Action
    priority: int
    score: int
    explanation: str
    shadow: bool = False  # evaluated and traced, but never decides and never scores

    def __post_init__(self) -> None:
        if not self.name.strip():
            raise ValueError("rule needs a name")
        if not self.field.strip():
            raise ValueError("rule needs a field")
        if self.value is None:
            raise ValueError("rule needs a value")
        if self.score < 0:
            raise ValueError("score must be non-negative")
        if not self.explanation.strip():
            raise ValueError("rule needs an explanation")
        if isinstance(self.value, set):
            object.__setattr__(self, "value", frozenset(self.value))

    def as_shadow(self) -> "Rule":
        """Same rule, evaluated and traced but never acting: it cannot decide and its score is not counted."""
        return Rule(self.name, self.field, self.operator, self.value, self.action, self.priority, self.score,
                    self.explanation, True)

    def describe(self) -> str:
        text = f"IF {self.field} {self.operator.value} {_fmt(self.value)} THEN {self.action.name} (p{self.priority}, +{self.score})"
        return text + (" [shadow]" if self.shadow else "")


@dataclass(frozen=True)
class RuleTrace:
    rule: str
    priority: int
    field: str
    observed: Any
    operator: Operator
    expected: Any
    action: Action
    score: int
    fired: bool
    shadow: bool
    explanation: str


class Strategy(Enum):
    FIRST_MATCH = "FIRST_MATCH"
    SCORE = "SCORE"


@dataclass(frozen=True)
class Decision:
    """The decision and its evidence. `fired()` is every rule whose condition held, in priority
    order; `traces` is every rule evaluated."""

    transaction_id: str
    action: Action
    strategy: Strategy
    score: int
    deciding_rule: str | None
    traces: list[RuleTrace] = field(default_factory=list)
    decided_at_millis: int = 0

    def fired(self) -> list[RuleTrace]:
        """Live rules whose condition held. Shadow rules are excluded: they did not act."""
        return [t for t in self.traces if t.fired and not t.shadow]

    def shadow_fired(self) -> list[RuleTrace]:
        """Shadow rules whose condition held: what a candidate rule would have done, measured without acting."""
        return [t for t in self.traces if t.fired and t.shadow]

    def fired_rule_names(self) -> list[str]:
        return [t.rule for t in self.fired()]

    def explain(self) -> str:
        head = f"decision for {self.transaction_id}: {self.action.name} [{self.strategy.value}, score={self.score}]"
        if self.strategy is Strategy.SCORE:
            head += " by accumulated score"
        elif self.deciding_rule is None:
            head += " default: no rule matched"
        else:
            head += f' by "{self.deciding_rule}"'
        lines = [head + f" at t={self.decided_at_millis}"]
        for t in self.traces:
            mark = "pass" if not t.fired else "shadow" if t.shadow else "fire"
            line = (f"  [{mark}] p{t.priority} {t.rule}: "
                    f"{t.field}={_fmt(t.observed)} {t.operator.value} {_fmt(t.expected)}")
            if t.fired:
                line += f" -> {t.action.name} (+{t.score}): {t.explanation}"
            lines.append(line)
        return "\n".join(lines) + "\n"


class RulesEngine:
    def __init__(self, rules: list[Rule], strategy: Strategy, review_at: int, decline_at: int) -> None:
        names = [r.name for r in rules]
        if len(set(names)) != len(names):
            raise ValueError("duplicate rule name")
        if review_at <= 0 or decline_at <= review_at:
            raise ValueError("need 0 < review_at < decline_at")
        self.rules = sorted(rules, key=lambda r: r.priority)  # stable: ties keep declaration order
        self.strategy = strategy
        self.review_at = review_at
        self.decline_at = decline_at

    def decide(self, transaction_id: str, facts: dict[str, Any], now_millis: int) -> Decision:
        """Evaluate every rule (the full trace is the audit record), then decide. An absent fact
        never fires a rule. A type mismatch between fact and value raises: a rule that can never
        evaluate is a configuration error, not a risk signal."""
        traces: list[RuleTrace] = []
        total = 0
        first: Rule | None = None
        for rule in self.rules:
            observed = facts.get(rule.field)
            fired = observed is not None and rule.operator.apply(observed, rule.value)
            traces.append(RuleTrace(rule.name, rule.priority, rule.field, observed, rule.operator, rule.value,
                                    rule.action, rule.score, fired, rule.shadow, rule.explanation))
            if fired and not rule.shadow:  # a shadow rule is observed, never obeyed
                total += rule.score
                first = first or rule
        if self.strategy is Strategy.FIRST_MATCH:
            action = first.action if first else Action.ALLOW
            return Decision(transaction_id, action, self.strategy, total, first.name if first else None, traces, now_millis)
        action = Action.DECLINE if total >= self.decline_at else Action.REVIEW if total >= self.review_at else Action.ALLOW
        return Decision(transaction_id, action, self.strategy, total, None, traces, now_millis)


class Screener:
    """Glue: record velocity, enrich facts, decide. One call per authorization attempt."""

    def __init__(self, engine: RulesEngine, velocity: VelocityService, clock: ManualClock) -> None:
        self.engine, self.velocity, self.clock = engine, velocity, clock

    def screen(self, tx: Transaction) -> Decision:
        now = self.clock.now_millis()
        return self.engine.decide(tx.id, self.velocity.enrich(tx, now), now)


# ---------------------------------------------------------------------------------------------
# Demo
# ---------------------------------------------------------------------------------------------

SECOND, MINUTE, HOUR, DAY = 1_000, 60_000, 3_600_000, 86_400_000


def usd(text: str) -> Money:
    return Money.parse(text, USD)


def tx(id: str, account: str, card: str, bin: str, country: str, card_age: int, amount: str) -> Transaction:
    return Transaction(id, account, card, bin, country, card_age, usd(amount))


def card_testing_rules() -> list[Rule]:
    """The rule set the demo and the tests share. Rules are data: this could be a JSON file."""
    return [
        Rule("bin-blocklist", "bin", Operator.IN, {"999999", "888888"}, Action.DECLINE, 5, 100,
             "issuer range is on the internal blocklist"),
        Rule("card-testing-burst", "attempts_per_card_10m", Operator.GT, 5, Action.DECLINE, 10, 60,
             "more than five authorizations on one card in ten minutes is the card-testing signature"),
        Rule("fresh-card-micro-auth", "card_age_days", Operator.LT, 1, Action.REVIEW, 20, 25,
             "card was added less than a day ago"),
        Rule("high-risk-country", "country", Operator.IN, {"NG", "RU", "VE"}, Action.REVIEW, 30, 30,
             "issuing country is on the elevated-risk list"),
        Rule("daily-spend", "amount_per_account_1d", Operator.GT, usd("2000.00"), Action.REVIEW, 40, 40,
             "account spent more than 2000.00 USD in 24 hours"),
        Rule("large-ticket", "amount", Operator.GTE, usd("5000.00"), Action.REVIEW, 50, 30,
             "single authorization at or above 5000.00 USD"),
    ]


def velocity() -> VelocityService:
    return (VelocityService()
            .define("attempts_per_card_10m", Dimension.CARD, Metric.COUNT, SlidingWindowCounter(10 * MINUTE, 10, USD))
            .define("amount_per_account_1d", Dimension.ACCOUNT, Metric.SUM, SlidingWindowCounter(DAY, 24, USD)))


def main() -> None:
    clock = ManualClock(1_700_000_040_000)
    engine = RulesEngine(card_testing_rules(), Strategy.FIRST_MATCH, 50, 80)
    screener = Screener(engine, velocity(), clock)

    print("== Rules in priority order ==")
    for r in engine.rules:
        print(f"  {r.name}: {r.describe()}")

    print("\n== A normal purchase ==")
    print(screener.screen(tx("tx-001", "acct_alice", "tok_alice", "411111", "US", 400, "42.50")).explain(), end="")

    print("\n== Card testing: six $1 attempts on a fresh card in two minutes ==")
    last = None
    for i in range(1, 7):
        clock.advance(20 * SECOND)
        last = screener.screen(tx(f"tx-10{i}", "acct_mallory", "tok_mallory", "411111", "US", 0, "1.00"))
        print(f"  attempt {i} -> {last.action.name} fired={last.fired_rule_names()}")
    print(last.explain(), end="")

    print("\n== Ten minutes later the window has slid past the burst ==")
    clock.advance(10 * MINUTE)
    print(screener.screen(tx("tx-107", "acct_mallory", "tok_mallory", "411111", "US", 0, "1.00")).explain(), end="")

    print("\n== Priority: a blocked BIN decides before anything else, but every rule is still traced ==")
    print(screener.screen(tx("tx-200", "acct_bob", "tok_bob", "999999", "NG", 0, "900.00")).explain(), end="")

    print("\n== Velocity on money, not count: three $700 purchases trip the daily sum ==")
    for i in range(1, 4):
        clock.advance(5 * MINUTE)
        last = screener.screen(tx(f"tx-30{i}", "acct_dave", "tok_dave", "411111", "US", 90, "700.00"))
    print(last.explain(), end="")

    print("\n== Same rules, SCORE strategy: four REVIEW signals add up to a DECLINE ==")
    scored = Screener(RulesEngine(card_testing_rules(), Strategy.SCORE, 50, 80), velocity(), clock)
    print(scored.screen(tx("tx-400", "acct_eve", "tok_eve", "411111", "RU", 0, "5500.00")).explain(), end="")
    first_match = Screener(engine, velocity(), clock).screen(tx("tx-401", "acct_eve", "tok_eve", "411111", "RU", 0, "5500.00"))
    print(f"FIRST_MATCH would have said: {first_match.action.name}")

    print("\n== Shadow mode: a candidate rule is traced but does not act ==")
    with_candidate = card_testing_rules() + [
        Rule("mid-ticket-candidate", "amount", Operator.GTE, usd("500.00"), Action.REVIEW, 45, 20,
             "candidate: single authorization at or above 500.00 USD").as_shadow()]
    shadow_screener = Screener(RulesEngine(with_candidate, Strategy.FIRST_MATCH, 50, 80), velocity(), clock)
    sd = shadow_screener.screen(tx("tx-500", "acct_frank", "tok_frank", "411111", "US", 200, "750.00"))
    print(sd.explain(), end="")
    print(f"acted on: {sd.fired_rule_names()}  would have fired: {[t.rule for t in sd.shadow_fired()]}")


if __name__ == "__main__":
    main()
