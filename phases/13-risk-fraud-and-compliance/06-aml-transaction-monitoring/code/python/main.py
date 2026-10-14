"""Lesson: phases/13-risk-fraud-and-compliance/06-aml-transaction-monitoring/docs/en.md
AML Transaction Monitoring and Structuring Detection
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: 31 U.S.C. 5324 and 31 CFR 1010.311 (currency transaction reports, structuring); FATF Recommendations 10 and 20;
         FinCEN SAR filing instructions. Python 3.11+, standard library only. Every account and name is synthetic.
"""

from __future__ import annotations

from dataclasses import dataclass
from enum import Enum
from typing import Optional

LONG_MIN = -(1 << 63)
LONG_MAX = (1 << 63) - 1


def _check_long(r: int) -> int:
    """Math.addExact / multiplyExact: Python ints never overflow, so the 64-bit contract is enforced by hand."""
    if r < LONG_MIN or r > LONG_MAX:
        raise OverflowError("long overflow")
    return r


def jdiv(a: int, b: int) -> int:
    """Java long division: truncates toward zero (Python's // floors toward negative infinity)."""
    q = abs(a) // abs(b)
    return q if (a < 0) == (b < 0) else -q


def jmod(a: int, b: int) -> int:
    """Java's %: the remainder takes the sign of the dividend."""
    return a - jdiv(a, b) * b


def to_int32(x: int) -> int:
    x &= 0xFFFFFFFF
    return x - (1 << 32) if x >= (1 << 31) else x


class JavaRandom:
    """java.util.Random's 48-bit LCG and nextInt(bound), bit for bit, so seed 7 builds the same population as the Java."""

    MULTIPLIER = 0x5DEECE66D
    MASK = (1 << 48) - 1

    def __init__(self, seed: int) -> None:
        self.seed = (seed ^ JavaRandom.MULTIPLIER) & JavaRandom.MASK

    def next(self, bits: int) -> int:
        self.seed = (self.seed * JavaRandom.MULTIPLIER + 0xB) & JavaRandom.MASK
        return to_int32(self.seed >> (48 - bits))

    def next_int(self, bound: int) -> int:
        if bound <= 0:
            raise ValueError("bound must be positive")
        r = self.next(31)
        m = bound - 1
        if bound & m == 0:
            return (bound * r) >> 31
        u = r
        while True:
            r = u % bound
            if to_int32(u - r + m) >= 0:
                return r
            u = self.next(31)


# ---------------------------------------------------------------------------------------------
# Money, copied from Phase 10 Lesson 01. Integer minor units; never a float.
# ---------------------------------------------------------------------------------------------
@dataclass(frozen=True)
class Currency:
    code: str
    exponent: int


USD = Currency("USD", 2)


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
            raise ValueError(f"cannot combine {self.currency.code} with {other.currency.code}")

    def plus(self, other: "Money") -> "Money":
        self._require_same_currency(other)
        return Money(_check_long(self.minor_units + other.minor_units), self.currency)

    def minus(self, other: "Money") -> "Money":
        self._require_same_currency(other)
        return Money(_check_long(self.minor_units - other.minor_units), self.currency)

    def basis_points(self, bps: int) -> "Money":
        """this * bps / 10000, rounded HALF_EVEN. Used for "at least 80% of the threshold" style bands."""
        product = _check_long(self.minor_units * bps)
        q, r = jdiv(product, 10_000), jmod(product, 10_000)
        twice = abs(r) * 2
        if twice > 10_000 or (twice == 10_000 and q % 2 != 0):
            q += -1 if product < 0 else 1
        return Money(q, self.currency)

    def compare_to(self, other: "Money") -> int:
        self._require_same_currency(other)
        return (self.minor_units > other.minor_units) - (self.minor_units < other.minor_units)

    def __str__(self) -> str:
        scale = 10 ** self.currency.exponent
        a = abs(self.minor_units)
        frac = "" if self.currency.exponent == 0 else f".{a % scale:0{self.currency.exponent}d}"
        return ("-" if self.minor_units < 0 else "") + str(a // scale) + frac + " " + self.currency.code


# ---------------------------------------------------------------------------------------------
# Step 1: the transaction. Direction and channel are what the scenarios filter on.
# ---------------------------------------------------------------------------------------------
class Direction(Enum):
    IN = "IN"
    OUT = "OUT"


class Channel(Enum):
    CASH = "CASH"
    WIRE = "WIRE"
    ACH = "ACH"
    CARD = "CARD"


class Hours:
    HOUR = 3600
    DAY = 24 * HOUR

    @staticmethod
    def format(ts: int) -> str:
        """Day/hour offset from a fixed epoch, so output reads as "d2 13:00" rather than a Unix timestamp."""
        rel = ts - T0
        return f"d{jdiv(rel, Hours.DAY)} {jdiv(jmod(rel, Hours.DAY), Hours.HOUR):02d}:{jdiv(jmod(rel, Hours.HOUR), 60):02d}"


T0 = 1_700_000_000 - (1_700_000_000 % Hours.DAY)


@dataclass(frozen=True)
class Transaction:
    id: str
    account_id: str
    counterparty_id: str
    direction: Direction
    channel: Channel
    amount: Money
    timestamp: int

    def __post_init__(self) -> None:
        if self.amount.minor_units <= 0:
            raise ValueError(f"transaction amount must be positive: {self.amount}")

    def brief(self) -> str:
        who = f"from {self.counterparty_id}" if self.direction == Direction.IN else f"to {self.counterparty_id}"
        return f"{self.id} {self.direction.value} {self.channel.value} {self.amount} {who} @{Hours.format(self.timestamp)}"


def by_time(tx: Transaction) -> tuple:
    return (tx.timestamp, tx.id)


# ---------------------------------------------------------------------------------------------
# Step 2: scenarios as data. A scenario is a kind plus named parameters; the engine interprets them.
# ---------------------------------------------------------------------------------------------
class ScenarioKind(Enum):
    STRUCTURING = "STRUCTURING"
    RAPID_PASS_THROUGH = "RAPID_PASS_THROUGH"
    VELOCITY = "VELOCITY"


class Severity(Enum):
    LOW = 0
    MEDIUM = 1
    HIGH = 2


@dataclass(frozen=True)
class Scenario:
    id: str
    kind: ScenarioKind
    severity: Severity
    params: dict
    description: str

    def param(self, name: str) -> int:
        v = self.params.get(name)
        if v is None:
            raise ValueError(f"scenario {self.id} missing parameter {name}")
        return v

    @staticmethod
    def structuring(id: str, threshold_minor: int, floor_bps: int, window_seconds: int, min_count: int) -> "Scenario":
        """Cash deposits each under the reporting threshold but at least floor_bps of it, summing to >= threshold."""
        return Scenario(id, ScenarioKind.STRUCTURING, Severity.HIGH,
                        {"thresholdMinor": threshold_minor, "floorBps": floor_bps, "windowSeconds": window_seconds, "minCount": min_count},
                        f"cash deposits just under the CTR threshold that aggregate above it within {window_seconds // Hours.DAY} days")

    @staticmethod
    def rapid_pass_through(id: str, min_in_minor: int, out_bps: int, window_seconds: int) -> "Scenario":
        """Inbound funds followed by outbound transfers of at least out_bps of the inbound amount inside the window."""
        return Scenario(id, ScenarioKind.RAPID_PASS_THROUGH, Severity.HIGH,
                        {"minInMinor": min_in_minor, "outBps": out_bps, "windowSeconds": window_seconds},
                        f"funds received and at least {out_bps // 100}% sent onward within {window_seconds // Hours.HOUR} hours")

    @staticmethod
    def velocity(id: str, window_seconds: int, baseline_count: int, baseline_counterparties: int, score_threshold: int) -> "Scenario":
        """Too many transactions or too many distinct counterparties in a window; scored, alert at >= score_threshold."""
        return Scenario(id, ScenarioKind.VELOCITY, Severity.MEDIUM,
                        {"windowSeconds": window_seconds, "baselineCount": baseline_count,
                         "baselineCounterparties": baseline_counterparties, "scoreThreshold": score_threshold},
                        f"transaction count and counterparty spread far above baseline within {window_seconds // Hours.HOUR} hours")


# ---------------------------------------------------------------------------------------------
# Step 3: alerts carry their evidence. An alert without the transactions behind it is an opinion.
# ---------------------------------------------------------------------------------------------
@dataclass(frozen=True)
class Alert:
    scenario_id: str
    kind: ScenarioKind
    severity: Severity
    account_id: str
    score: int
    evidence: tuple
    narrative: str

    def window_start(self) -> int:
        return min((tx.timestamp for tx in self.evidence), default=0)

    def window_end(self) -> int:
        return max((tx.timestamp for tx in self.evidence), default=0)

    def total(self) -> Money:
        t = Money.zero(self.evidence[0].amount.currency)
        for tx in self.evidence:
            t = t.plus(tx.amount)
        return t

    def explain(self) -> str:
        lines = [f"[{self.severity.name} {self.scenario_id}] {self.kind.value} on {self.account_id} score={self.score} "
                 f"{Hours.format(self.window_start())}..{Hours.format(self.window_end())}: {self.narrative}"]
        lines += [f"    {tx.brief()}" for tx in self.evidence]
        return "\n".join(lines)


# ---------------------------------------------------------------------------------------------
# Step 4: the engine. Groups by account, sorts by time, runs each scenario's detector, merges overlaps.
# ---------------------------------------------------------------------------------------------
class MonitoringEngine:
    def __init__(self, scenarios: list[Scenario]) -> None:
        ids: set[str] = set()
        for s in scenarios:
            if s.id in ids:
                raise ValueError(f"duplicate scenario id {s.id}")
            ids.add(s.id)
        self._scenarios = list(scenarios)

    def evaluate(self, transactions: list[Transaction]) -> list[Alert]:
        by_account: dict[str, list[Transaction]] = {}
        for tx in transactions:
            by_account.setdefault(tx.account_id, []).append(tx)
        alerts: list[Alert] = []
        for account in sorted(by_account):   # TreeMap order
            srt = sorted(by_account[account], key=by_time)
            for s in self._scenarios:
                if s.kind == ScenarioKind.STRUCTURING:
                    raw = MonitoringEngine.structuring(s, account, srt)
                elif s.kind == ScenarioKind.RAPID_PASS_THROUGH:
                    raw = MonitoringEngine.rapid_pass_through(s, account, srt)
                else:
                    raw = MonitoringEngine.velocity(s, account, srt)
                alerts.extend(MonitoringEngine.merge_overlapping(raw))
        alerts.sort(key=lambda a: (-a.severity.value, -a.score, a.account_id, a.scenario_id))
        return alerts

    @staticmethod
    def structuring(s: Scenario, account: str, srt: list[Transaction]) -> list[Alert]:
        """Rolling window over qualifying cash deposits: each in [floor, threshold), window sum >= threshold, count >= min_count."""
        threshold = Money.of_minor(s.param("thresholdMinor"), USD)
        floor = threshold.basis_points(s.param("floorBps"))
        window = s.param("windowSeconds")
        min_count = s.param("minCount")
        qualifying = [tx for tx in srt if tx.direction == Direction.IN and tx.channel == Channel.CASH
                      and tx.amount.compare_to(threshold) < 0 and tx.amount.compare_to(floor) >= 0]
        out: list[Alert] = []
        left = 0
        total = Money.zero(USD)
        for right in range(len(qualifying)):
            total = total.plus(qualifying[right].amount)
            while qualifying[right].timestamp - qualifying[left].timestamp > window:
                total = total.minus(qualifying[left].amount)
                left += 1
            count = right - left + 1
            if count >= min_count and total.compare_to(threshold) >= 0:
                evidence = tuple(qualifying[left:right + 1])
                score = min(100, 50 + 10 * count + jdiv((total.minor_units - threshold.minor_units) * 10, threshold.minor_units))
                out.append(Alert(s.id, s.kind, s.severity, account, score, evidence,
                                 f"{count} cash deposits of {floor} to {threshold} totalling {total} within {window // Hours.DAY} days"))
        return out

    @staticmethod
    def rapid_pass_through(s: Scenario, account: str, srt: list[Transaction]) -> list[Alert]:
        """For each inbound of at least min_in, outbound transfers inside the window totalling >= out_bps of it."""
        min_in = Money.of_minor(s.param("minInMinor"), USD)
        out_bps, window = s.param("outBps"), s.param("windowSeconds")
        out: list[Alert] = []
        for i, inbound in enumerate(srt):
            if inbound.direction != Direction.IN or inbound.amount.compare_to(min_in) < 0:
                continue
            required = inbound.amount.basis_points(out_bps)
            sent_on = Money.zero(USD)
            evidence = [inbound]
            j = i + 1
            while j < len(srt) and srt[j].timestamp - inbound.timestamp <= window:
                tx = srt[j]
                j += 1
                if tx.direction != Direction.OUT or tx.channel == Channel.CARD:
                    continue
                sent_on = sent_on.plus(tx.amount)
                evidence.append(tx)
                if sent_on.compare_to(required) >= 0:
                    break
            if sent_on.compare_to(required) >= 0:
                elapsed_hours = jdiv(evidence[-1].timestamp - inbound.timestamp, Hours.HOUR)
                window_hours = jdiv(window, Hours.HOUR)
                score = min(100, 60 + jdiv((window_hours - elapsed_hours) * 40, max(1, window_hours)))
                out.append(Alert(s.id, s.kind, s.severity, account, score, tuple(evidence),
                                 f"{inbound.amount} in, {sent_on} out to {len(evidence) - 1} counterparties within {elapsed_hours}h"))
        return out

    @staticmethod
    def velocity(s: Scenario, account: str, srt: list[Transaction]) -> list[Alert]:
        """Score = 50 * count/baseline_count + 50 * counterparties/baseline_counterparties over the window."""
        window, base_count, base_cp = s.param("windowSeconds"), s.param("baselineCount"), s.param("baselineCounterparties")
        score_threshold = s.param("scoreThreshold")
        out: list[Alert] = []
        left = 0
        for right in range(len(srt)):
            while srt[right].timestamp - srt[left].timestamp > window:
                left += 1
            win = srt[left:right + 1]
            cps = {tx.counterparty_id for tx in win}
            score = jdiv(50 * len(win), base_count) + jdiv(50 * len(cps), base_cp)
            if score >= score_threshold:
                out.append(Alert(s.id, s.kind, s.severity, account, min(100, score), tuple(win),
                                 f"{len(win)} transactions with {len(cps)} counterparties in {window // Hours.HOUR}h "
                                 f"(baseline {base_count}/{base_cp})"))
        return out

    @staticmethod
    def merge_overlapping(raw: list[Alert]) -> list[Alert]:
        """Alerts from the same scenario whose evidence overlaps are one pattern, not several. Keep the highest score."""
        merged: list[Alert] = []
        for a in raw:
            if merged:
                last = merged[-1]
                last_ids = {tx.id for tx in last.evidence}
                if any(tx.id in last_ids for tx in a.evidence):
                    union: dict[str, Transaction] = {}
                    for tx in last.evidence:
                        union[tx.id] = tx
                    for tx in a.evidence:
                        union[tx.id] = tx
                    ev = tuple(sorted(union.values(), key=by_time))
                    better = a if a.score > last.score else last
                    cut = better.narrative.find("; merged")
                    peak = better.narrative[:cut] if cut >= 0 else better.narrative
                    merged_alert = Alert(a.scenario_id, a.kind, a.severity, a.account_id, better.score, ev, peak)
                    merged[-1] = Alert(a.scenario_id, a.kind, a.severity, a.account_id, better.score, ev,
                                       f"{peak}; merged window holds {len(ev)} transactions totalling {merged_alert.total()}")
                    continue
            merged.append(a)
        return merged


# ---------------------------------------------------------------------------------------------
# Step 5: a seeded synthetic population with planted patterns, so recall and false positives are measurable.
# ---------------------------------------------------------------------------------------------
class SyntheticGenerator:
    def __init__(self, seed: int) -> None:
        self._rng = JavaRandom(seed)
        self._seq = 0
        self.planted: dict[str, str] = {}   # account -> pattern it carries

    def _next_id(self) -> str:
        self._seq += 1
        return f"tx{self._seq:05d}"

    def _tx(self, account: str, cp: str, d: Direction, ch: Channel, minor: int, ts: int) -> Transaction:
        return Transaction(self._next_id(), account, cp, d, ch, Money.of_minor(minor, USD), ts)

    def normal_account(self, account: str, t0: int, days: int) -> list[Transaction]:
        """Ordinary retail behaviour: payroll in, card spend out, the odd cash deposit well under the threshold.
        Argument order matches Java's left-to-right evaluation, so every next_int is drawn in the same sequence."""
        rng = self._rng
        out: list[Transaction] = []
        for d in range(days):
            day = t0 + d * Hours.DAY
            if d % 14 == 0:
                out.append(self._tx(account, f"employer-{account}", Direction.IN, Channel.ACH, 180_000 + rng.next_int(60_000), day + 6 * Hours.HOUR))
            spends = 1 + rng.next_int(4)
            for _ in range(spends):
                cp = f"merchant-{rng.next_int(40)}"
                minor = 500 + rng.next_int(12_000)
                hour = 8 + rng.next_int(12)
                ts = day + hour * Hours.HOUR + rng.next_int(3600)
                out.append(self._tx(account, cp, Direction.OUT, Channel.CARD, minor, ts))
            if rng.next_int(10) == 0:
                out.append(self._tx(account, "branch", Direction.IN, Channel.CASH, 2_000 + rng.next_int(300_000), day + 10 * Hours.HOUR))
        return out

    def structurer(self, account: str, t0: int, deposits: int, threshold_minor: int) -> list[Transaction]:
        """The classic: N cash deposits each a little under the threshold, spread over a few days."""
        self.planted[account] = "STRUCTURING"
        out = list(self.normal_account(account, t0, 10))
        for i in range(deposits):
            amount = threshold_minor - 5_000 - self._rng.next_int(60_000)   # 9,400.00 to 9,950.00 on a 10,000.00 threshold
            cp = f"branch-{self._rng.next_int(3)}"
            out.append(self._tx(account, cp, Direction.IN, Channel.CASH, amount, t0 + 2 * Hours.DAY + i * 7 * Hours.HOUR))
        return out

    def pass_through(self, account: str, t0: int, in_minor: int, legs: int) -> list[Transaction]:
        """A mule: a large wire in, then almost all of it wired on to several counterparties within hours."""
        self.planted[account] = "RAPID_PASS_THROUGH"
        out = list(self.normal_account(account, t0, 10))
        inbound = t0 + 3 * Hours.DAY + 9 * Hours.HOUR
        out.append(self._tx(account, "shell-co", Direction.IN, Channel.WIRE, in_minor, inbound))
        remaining = jdiv(in_minor * 9_600, 10_000)   # 96% sent on, integer arithmetic
        for i in range(legs):
            leg = remaining if i == legs - 1 else jdiv(remaining, legs - i)
            remaining -= leg
            out.append(self._tx(account, f"beneficiary-{i}", Direction.OUT, Channel.WIRE, leg, inbound + (2 + i * 3) * Hours.HOUR))
        return out

    def burst(self, account: str, t0: int, count: int) -> list[Transaction]:
        """A burst: dozens of small transfers to many counterparties in one afternoon."""
        self.planted[account] = "VELOCITY"
        out = list(self.normal_account(account, t0, 10))
        start = t0 + 5 * Hours.DAY + 13 * Hours.HOUR
        for i in range(count):
            out.append(self._tx(account, f"peer-{i}", Direction.OUT, Channel.ACH, 2_000 + self._rng.next_int(20_000), start + i * 5 * 60))
        return out

    def cash_business(self, account: str, t0: int, days: int) -> list[Transaction]:
        """A legitimate cash-intensive business (a laundromat, a food truck): daily cash deposits that sit near the floor."""
        out: list[Transaction] = []
        for d in range(days):
            day = t0 + d * Hours.DAY
            out.append(self._tx(account, "branch-0", Direction.IN, Channel.CASH, 550_000 + self._rng.next_int(400_000), day + 18 * Hours.HOUR))
            if d % 7 == 6:
                cp = f"supplier-{self._rng.next_int(5)}"
                out.append(self._tx(account, cp, Direction.OUT, Channel.ACH, 1_500_000 + self._rng.next_int(1_000_000), day + 9 * Hours.HOUR))
        return out

    def population(self, normal_accounts: int, t0: int) -> list[Transaction]:
        all_tx: list[Transaction] = []
        for i in range(normal_accounts):
            all_tx.extend(self.normal_account(f"acct-{i:03d}", t0, 10))
        all_tx.extend(self.cash_business("acct-laundromat", t0, 10))
        all_tx.extend(self.cash_business("acct-foodtruck", t0, 10))
        all_tx.extend(self.structurer("acct-structurer", t0, 10, 1_000_000))
        all_tx.extend(self.pass_through("acct-mule", t0, 4_850_000, 3))
        all_tx.extend(self.burst("acct-burst", t0, 36))
        return all_tx


def standard_scenarios() -> list[Scenario]:
    return [
        Scenario.structuring("STR-01", 1_000_000, 8_000, 3 * Hours.DAY, 3),
        Scenario.rapid_pass_through("RPT-01", 1_000_000, 9_000, 24 * Hours.HOUR),
        Scenario.velocity("VEL-01", 6 * Hours.HOUR, 6, 4, 150),
    ]


def fmt_params(params: dict) -> str:
    """Java prints Map.of(...) in an order that changes from run to run; Python prints declaration order."""
    return "{" + ", ".join(f"{k}={v}" for k, v in params.items()) + "}"


def main() -> None:
    print("== Scenarios (data, not code) ==")
    for s in standard_scenarios():
        print(f"  {s.id:<7} {s.kind.value:<18} {s.severity.name:<6} {s.description}")
        print(f"    params {fmt_params(s.params)}")

    print("\n== Hand-built case: ten deposits of 9,900.00 over three days ==")
    ten_deposits = [Transaction(f"dep{i}", "acct-demo", "branch", Direction.IN, Channel.CASH, Money.of_minor(990_000, USD),
                                T0 + i * 7 * Hours.HOUR) for i in range(10)]
    engine = MonitoringEngine(standard_scenarios())
    for a in engine.evaluate(ten_deposits):
        print("  " + a.explain().replace("\n", "\n  "))

    print("\n== Synthetic population: 60 ordinary accounts, two cash businesses, three planted patterns (seed 7) ==")
    gen = SyntheticGenerator(7)
    population = gen.population(60, T0)
    alerts = engine.evaluate(population)
    print(f"  transactions: {len(population)}, alerts: {len(alerts)}")
    for a in alerts:
        planted = gen.planted.get(a.account_id, "none")
        print(f"  {a.severity.name:<5} {a.scenario_id:<7} {a.account_id:<16} score={a.score:3d} planted={planted:<18} {a.narrative}")
    true_positives = false_positives = 0
    found: dict[str, None] = {}
    for a in alerts:
        if a.kind.value == gen.planted.get(a.account_id):
            true_positives += 1
            found[a.account_id] = None
        else:
            false_positives += 1
    print(f"  planted patterns found: {len(found)} of {len(gen.planted)}; true-positive alerts {true_positives}, "
          f"false-positive alerts {false_positives}")

    print("\n== The first alert in full ==")
    if alerts:
        print("  " + alerts[0].explain().replace("\n", "\n  "))


if __name__ == "__main__":
    main()
