"""Lesson: phases/13-risk-fraud-and-compliance/03-risk-scoring-and-thresholds/docs/en.md
Risk Scoring, Thresholds, and Precision-Recall Trade-offs
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: logistic regression by gradient descent (Hastie, Tibshirani, Friedman, "Elements of Statistical Learning", ch. 4);
reliability diagrams and expected calibration error (Naeini et al., AAAI 2015). Python 3.11+, standard library only.
"""

from __future__ import annotations

import math
import operator
from dataclasses import dataclass
from decimal import ROUND_HALF_UP, Decimal
from enum import Enum
from functools import reduce
from itertools import repeat
from typing import Optional

LONG_MIN = -(1 << 63)
LONG_MAX = (1 << 63) - 1


def _check_long(r: int) -> int:
    """Math.addExact / multiplyExact: Python ints never overflow, so the 64-bit contract is enforced by hand."""
    if r < LONG_MIN or r > LONG_MAX:
        raise OverflowError("long overflow")
    return r


def fsum_seq(values) -> float:
    """Left-to-right float addition, exactly like a Java `+=` loop. Python 3.12+ `sum()` uses compensated summation,
    which is more accurate and therefore NOT bit-identical to the Java version."""
    return reduce(operator.add, values, 0.0)


def java_round(x: float) -> int:
    """Java's Math.round(double): round half up (toward positive infinity)."""
    f = math.floor(x)
    return int(f) + (1 if x - f >= 0.5 else 0)


def jf(x: float, digits: int, plus: bool = False) -> str:
    """Java's %.Nf / %+.Nf: HALF_UP on the shortest decimal representation (Python's f-format is half-even on the exact
    binary value, so 0.125 would print as 0.12 instead of Java's 0.13)."""
    d = Decimal(repr(float(x))).quantize(Decimal(1).scaleb(-digits), rounding=ROUND_HALF_UP)
    s = f"{d:f}"
    return "+" + s if plus and not s.startswith("-") else s


def to_int32(x: int) -> int:
    x &= 0xFFFFFFFF
    return x - (1 << 32) if x >= (1 << 31) else x


class JavaRandom:
    """java.util.Random, bit for bit: 48-bit LCG, next(bits), nextInt(bound), nextDouble, nextGaussian (polar method with
    the cached second value). Same seed, same synthetic dataset as the Java version."""

    MULTIPLIER = 0x5DEECE66D
    MASK = (1 << 48) - 1

    def __init__(self, seed: int) -> None:
        self.seed = (seed ^ JavaRandom.MULTIPLIER) & JavaRandom.MASK
        self._next_next_gaussian = 0.0
        self._have_next_next_gaussian = False

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

    def next_double(self) -> float:
        return ((self.next(26) << 27) + self.next(27)) * 2.0 ** -53

    def next_gaussian(self) -> float:
        if self._have_next_next_gaussian:
            self._have_next_next_gaussian = False
            return self._next_next_gaussian
        while True:
            v1 = 2 * self.next_double() - 1
            v2 = 2 * self.next_double() - 1
            s = v1 * v1 + v2 * v2
            if 0 < s < 1:
                break
        multiplier = math.sqrt(-2 * math.log(s) / s)
        self._next_next_gaussian = v2 * multiplier
        self._have_next_next_gaussian = True
        return v1 * multiplier


def java_shuffle(items: list, rng: JavaRandom) -> None:
    """Collections.shuffle(list, rnd): Fisher-Yates from the end, swapping i-1 with nextInt(i)."""
    for i in range(len(items), 1, -1):
        j = rng.next_int(i)
        items[i - 1], items[j] = items[j], items[i - 1]


# ---------------------------------------------------------------------------------------------- money (Phase 10, Lesson 01)


@dataclass(frozen=True)
class Currency:
    """ISO 4217 currency: code and minor-unit exponent. Copied from Phase 10, Lesson 01."""

    code: str
    exponent: int

    def __post_init__(self) -> None:
        if self.code is None or len(self.code) != 3:
            raise ValueError(f"currency code: {self.code}")
        if not 0 <= self.exponent <= 4:
            raise ValueError(f"exponent must be 0..4: {self.exponent}")

    def minor_units_per_major(self) -> int:
        return 10 ** self.exponent


USD = Currency("USD", 2)


class Rounding(Enum):
    HALF_EVEN = "HALF_EVEN"
    DOWN = "DOWN"


@dataclass(frozen=True)
class Money:
    """Integer minor units plus a currency. Never a float. Copied from Phase 10, Lesson 01 (subset)."""

    minor_units: int
    currency: Currency

    def __post_init__(self) -> None:
        if self.currency is None:
            raise TypeError("currency")
        if not isinstance(self.minor_units, int) or isinstance(self.minor_units, bool):
            raise TypeError("minor_units must be an int; never construct Money from a float")

    @staticmethod
    def of_minor(minor_units: int, currency: Currency) -> "Money":
        return Money(minor_units, currency)

    @staticmethod
    def parse(text: str, currency: Currency) -> "Money":
        s = text.strip()
        negative = s.startswith("-")
        if negative or s.startswith("+"):
            s = s[1:]
        whole, frac = s, ""
        dot = s.find(".")
        if dot >= 0:
            whole, frac = s[:dot], s[dot + 1:]
        if not whole:
            whole = "0"
        if len(frac) > currency.exponent:
            raise ArithmeticError(f"{text} has too many decimals for {currency.code}")
        if not whole.isdigit() or (frac and not frac.isdigit()):
            raise ValueError(f"not a decimal amount: {text}")
        padded = frac.ljust(currency.exponent, "0")
        minor = _check_long(_check_long(int(whole) * currency.minor_units_per_major()) + (int(padded) if padded else 0))
        return Money(-minor if negative else minor, currency)

    def _require_same_currency(self, other: "Money") -> None:
        if self.currency != other.currency:
            raise ValueError(f"cannot combine {self.currency.code} with {other.currency.code}")

    def plus(self, other: "Money") -> "Money":
        self._require_same_currency(other)
        return Money(_check_long(self.minor_units + other.minor_units), self.currency)

    def minus(self, other: "Money") -> "Money":
        self._require_same_currency(other)
        return Money(_check_long(self.minor_units - other.minor_units), self.currency)

    @staticmethod
    def divide_rounded(dividend: int, divisor: int, rounding: Rounding) -> int:
        """Java long division truncates toward zero; Python's // floors. Work on absolute values and reapply the sign."""
        sign = -1 if (dividend < 0) != (divisor < 0) else 1
        q_abs, r_abs = divmod(abs(dividend), abs(divisor))
        quotient = sign * q_abs
        if r_abs == 0:
            return quotient
        if rounding == Rounding.DOWN:
            return quotient
        twice = r_abs * 2
        if twice > abs(divisor):
            return quotient + sign
        if twice < abs(divisor):
            return quotient
        return quotient if quotient % 2 == 0 else quotient + sign

    def times(self, numerator: int, denominator: int, rounding: Rounding) -> "Money":
        if denominator <= 0:
            raise ArithmeticError("denominator must be positive")
        return Money(Money.divide_rounded(_check_long(self.minor_units * numerator), denominator, rounding), self.currency)

    def compare_to(self, other: "Money") -> int:
        self._require_same_currency(other)
        return (self.minor_units > other.minor_units) - (self.minor_units < other.minor_units)

    def __str__(self) -> str:
        whole, frac = divmod(abs(self.minor_units), self.currency.minor_units_per_major())
        sign = "-" if self.minor_units < 0 else ""
        if self.currency.exponent == 0:
            return f"{sign}{whole} {self.currency.code}"
        return f"{sign}{whole}.{frac:0{self.currency.exponent}d} {self.currency.code}"


# ---------------------------------------------------------------------------------------------- features and data


@dataclass(frozen=True)
class Features:
    """The feature vector a transaction is scored on. Every value is already transformed to a small range."""

    values: tuple

    NAMES = (
        "amount_log_ratio",   # ln(amount / account's trailing average amount); 0 means typical
        "new_device",         # 1 if the device fingerprint has never been seen on this account
        "geo_mismatch",       # 1 if the IP country differs from the card's issuing country
        "velocity_1h",        # attempts on this account in the last hour, capped at 10, divided by 10
        "account_young",      # max(0, 1 - account_age_days / 90)
        "failed_cvv_24h",     # CVV failures in the last 24 hours, capped at 5, divided by 5
    )
    COUNT = 6

    def __post_init__(self) -> None:
        object.__setattr__(self, "values", tuple(float(v) for v in self.values))
        if len(self.values) != Features.COUNT:
            raise ValueError(f"expected {Features.COUNT} features, got {len(self.values)}")

    @staticmethod
    def of(amount_log_ratio: float, new_device: bool, geo_mismatch: bool, attempts_1h: int, account_age_days: int,
           failed_cvv_24h: int) -> "Features":
        return Features((
            amount_log_ratio,
            1.0 if new_device else 0.0,
            1.0 if geo_mismatch else 0.0,
            min(10, max(0, attempts_1h)) / 10.0,
            max(0.0, 1.0 - max(0, account_age_days) / 90.0),
            min(5, max(0, failed_cvv_24h)) / 5.0,
        ))

    def get(self, i: int) -> float:
        return self.values[i]


@dataclass(frozen=True)
class Example:
    """One labelled row: features, the ground truth that arrived 60 days later as a chargeback, and the amount at stake."""

    features: Features
    fraud: bool
    amount: Money


class SyntheticDataset:
    """A seeded generator. Fraud and legitimate rows come from different distributions so a scorecard can separate them."""

    @staticmethod
    def generate(seed: int, n: int, fraud_rate: float) -> list[Example]:
        rng = JavaRandom(seed)
        rows: list[Example] = []
        for _ in range(n):
            fraud = rng.next_double() < fraud_rate
            if fraud:
                amount_log_ratio = 0.9 + 0.8 * rng.next_gaussian()
                new_device = rng.next_double() < 0.70
                geo_mismatch = rng.next_double() < 0.40
                attempts = 1 + SyntheticDataset.poisson(rng, 3.0)
                age_days = rng.next_int(30) if rng.next_double() < 0.5 else rng.next_int(1000)
                failed_cvv = SyntheticDataset.poisson(rng, 1.2)
            else:
                amount_log_ratio = 0.0 + 0.6 * rng.next_gaussian()
                new_device = rng.next_double() < 0.10
                geo_mismatch = rng.next_double() < 0.03
                attempts = 1 + SyntheticDataset.poisson(rng, 0.3)
                age_days = rng.next_int(1000)
                failed_cvv = 1 if rng.next_double() < 0.05 else 0
            cents = java_round(4000 * math.exp(amount_log_ratio)) + 100   # typical ticket 40.00 USD
            rows.append(Example(Features.of(amount_log_ratio, new_device, geo_mismatch, attempts, age_days, failed_cvv),
                                fraud, Money.of_minor(cents, USD)))
        return rows

    @staticmethod
    def poisson(rng: JavaRandom, mean: float) -> int:
        """Knuth's algorithm; fine for small means and keeps the generator fully seeded."""
        limit = math.exp(-mean)
        p = rng.next_double()
        k = 0
        while p > limit:
            k += 1
            p *= rng.next_double()
        return k

    @staticmethod
    def split(rows: list[Example], seed: int, train_fraction: float) -> list[list[Example]]:
        """Deterministic split: shuffle with the seed (Collections.shuffle), then cut."""
        copy = list(rows)
        java_shuffle(copy, JavaRandom(seed))
        cut = java_round(len(copy) * train_fraction)
        return [copy[:cut], copy[cut:]]


# ---------------------------------------------------------------------------------------------- the scorecard


@dataclass(frozen=True)
class Contribution:
    """One feature's share of a decision: weight times value."""

    feature: str
    value: float
    weight: float
    contribution: float


@dataclass(frozen=True)
class Explanation:
    """The explanation shipped with every decision: integer score, probability, and the features that drove it."""

    score: int
    probability: float
    top: tuple


def _exp_neg(z: float) -> float:
    try:
        return math.exp(-z)
    except OverflowError:   # Java's Math.exp returns Infinity here, and 1 / (1 + Infinity) is 0.0
        return math.inf


class Scorecard:
    """A logistic scorecard: probability = sigmoid(bias + sum(w_i * x_i)). Trained by plain gradient descent."""

    def __init__(self, weights, bias: float) -> None:
        if len(weights) != Features.COUNT:
            raise ValueError("weights must match features")
        self.weights = [float(w) for w in weights]
        self.bias = float(bias)

    @staticmethod
    def sigmoid(z: float) -> float:
        return 1.0 / (1.0 + _exp_neg(z))

    def logit(self, f: Features) -> float:
        z = self.bias
        for i in range(len(self.weights)):
            z += self.weights[i] * f.get(i)
        return z

    def probability(self, f: Features) -> float:
        return Scorecard.sigmoid(self.logit(f))

    def score(self, f: Features) -> int:
        """Integer 0..999 for storage, comparison, and thresholds in rules; never compare raw doubles across services."""
        return java_round(self.probability(f) * 999.0)

    @staticmethod
    def score_from_probability(p: float) -> int:
        return java_round(p * 999.0)

    @staticmethod
    def train(rows: list[Example], epochs: int, learning_rate: float) -> "Scorecard":
        """Batch gradient descent on log loss, bit-identical to the Java loop but column by column with map/reduce, so
        400 epochs over 14,000 rows take about two seconds instead of minutes. Three exact rewrites make that possible:
        - every element still goes through ((b + w0*x0) + w1*x1) + ..., and every gradient is a left-to-right sum;
        - -z is built as (-b) + (-w0)*x0 + ...: IEEE negation commutes with round-to-nearest, so it equals -(z) exactly;
        - a column that is mostly zero keeps only its non-zero (index, value) pairs: adding w*0.0 never changes a sum."""
        add, mul, sub, truediv = operator.add, operator.mul, operator.sub, operator.truediv
        cols = [[r.features.get(i) for r in rows] for i in range(Features.COUNT)]
        labels = [1.0 if r.fraud else 0.0 for r in rows]
        n = len(rows)
        sparse: list = []
        for i in range(Features.COUNT):
            idx = [j for j, v in enumerate(cols[i]) if v != 0.0]
            sparse.append((idx, [cols[i][j] for j in idx]) if len(idx) < n // 4 else None)
        w = [0.0] * Features.COUNT
        b = 0.0
        for _ in range(epochs):
            neg_z = [-b] * n
            for i in range(Features.COUNT):
                nw = -w[i]
                if sparse[i] is None:
                    neg_z = list(map(add, neg_z, map(mul, repeat(nw), cols[i])))
                else:
                    for j, v in zip(*sparse[i]):
                        neg_z[j] = neg_z[j] + nw * v
            try:
                e = list(map(math.exp, neg_z))
            except OverflowError:
                e = [_exp_neg(-v) for v in neg_z]
            p = map(truediv, repeat(1.0), map(add, repeat(1.0), e))   # sigmoid(z) = 1 / (1 + exp(-z))
            error = list(map(sub, p, labels))
            grad_w = []
            for i in range(Features.COUNT):
                if sparse[i] is None:
                    grad_w.append(reduce(add, map(mul, error, cols[i]), 0.0))
                else:
                    idx, vals = sparse[i]
                    grad_w.append(reduce(add, map(mul, map(error.__getitem__, idx), vals), 0.0))
            grad_b = reduce(add, error, 0.0)
            for i in range(Features.COUNT):
                w[i] -= learning_rate * grad_w[i] / n
            b -= learning_rate * grad_b / n
        return Scorecard(w, b)

    def log_loss(self, rows: list[Example]) -> float:
        total = 0.0
        for row in rows:
            p = min(1 - 1e-12, max(1e-12, self.probability(row.features)))
            total -= math.log(p) if row.fraud else math.log(1 - p)
        return total / len(rows)

    def explain(self, f: Features, k: int) -> Explanation:
        """The k features with the largest absolute contribution, most influential first (ties by name)."""
        contributions = [Contribution(Features.NAMES[i], f.get(i), self.weights[i], self.weights[i] * f.get(i))
                         for i in range(len(self.weights))]
        contributions.sort(key=lambda c: (-abs(c.contribution), c.feature))
        return Explanation(self.score(f), self.probability(f), tuple(contributions[:min(k, len(contributions))]))


# ---------------------------------------------------------------------------------------------- evaluation


@dataclass(frozen=True)
class Scored:
    """A probability paired with its label, for evaluation."""

    probability: float
    fraud: bool
    amount: Money


@dataclass(frozen=True)
class ConfusionMatrix:
    """Counts at one threshold. The invariant tp + fp + tn + fn == total holds for every threshold."""

    tp: int
    fp: int
    tn: int
    fn: int

    def total(self) -> int:
        return self.tp + self.fp + self.tn + self.fn

    def precision(self) -> float:
        return 0.0 if self.tp + self.fp == 0 else self.tp / (self.tp + self.fp)

    def recall(self) -> float:
        return 0.0 if self.tp + self.fn == 0 else self.tp / (self.tp + self.fn)

    def f1(self) -> float:
        p, r = self.precision(), self.recall()
        return 0.0 if p + r == 0 else 2 * p * r / (p + r)

    def false_positive_rate(self) -> float:
        """The insult rate: share of legitimate customers declined."""
        return 0.0 if self.fp + self.tn == 0 else self.fp / (self.fp + self.tn)


@dataclass(frozen=True)
class CostModel:
    """Money consequences of each cell. Costs are per case."""

    fraud_loss: Money
    insult_cost: Money
    review_cost: Money
    review_miss_rate: float
    review_insult_rate: float

    def cost(self, cm: ConfusionMatrix) -> Money:
        """Every fraud we miss costs fraud_loss, every good customer we block costs insult_cost."""
        return self.fraud_loss.times(cm.fn, 1, Rounding.HALF_EVEN).plus(self.insult_cost.times(cm.fp, 1, Rounding.HALF_EVEN))


class Evaluator:
    @staticmethod
    def score(model: Scorecard, rows: list[Example]) -> list[Scored]:
        return [Scored(model.probability(e.features), e.fraud, e.amount) for e in rows]

    @staticmethod
    def confusion(scored: list[Scored], threshold: float) -> ConfusionMatrix:
        """Declare fraud when probability >= threshold."""
        tp = fp = tn = fn = 0
        for s in scored:
            flagged = s.probability >= threshold
            if s.fraud and flagged:
                tp += 1
            elif not s.fraud and flagged:
                fp += 1
            elif not s.fraud:
                tn += 1
            else:
                fn += 1
        return ConfusionMatrix(tp, fp, tn, fn)

    @staticmethod
    def auc(scored: list[Scored]) -> float:
        """Area under the ROC curve by the rank-sum (Mann-Whitney) formula."""
        srt = sorted(scored, key=lambda s: s.probability)
        positives = sum(1 for s in srt if s.fraud)
        negatives = len(srt) - positives
        if positives == 0 or negatives == 0:
            return 0.5
        rank_sum = 0.0
        i = 0
        while i < len(srt):
            j = i
            while j < len(srt) and srt[j].probability == srt[i].probability:
                j += 1
            avg_rank = (i + 1 + j) / 2.0   # ranks are 1-based; ties share the average rank
            for k in range(i, j):
                if srt[k].fraud:
                    rank_sum += avg_rank
            i = j
        return (rank_sum - positives * (positives + 1) / 2.0) / (float(positives) * negatives)


@dataclass(frozen=True)
class Threshold:
    """A chosen threshold and what it costs."""

    value: float
    matrix: ConfusionMatrix
    expected_cost: Money


@dataclass(frozen=True)
class ReviewBand:
    """Two thresholds: approve below low, decline at or above high, send the rest to a human."""

    low: float
    high: float
    approved: int
    reviewed: int
    declined: int
    expected_cost: Money


class ThresholdSelector:
    STEPS = 100

    @staticmethod
    def best_by_f1(scored: list[Scored], costs: CostModel) -> Threshold:
        best: Optional[Threshold] = None
        for i in range(1, ThresholdSelector.STEPS):
            t = i / ThresholdSelector.STEPS
            cm = Evaluator.confusion(scored, t)
            if best is None or cm.f1() > best.matrix.f1():
                best = Threshold(t, cm, costs.cost(cm))
        return best

    @staticmethod
    def best_by_cost(scored: list[Scored], costs: CostModel) -> Threshold:
        best: Optional[Threshold] = None
        for i in range(1, ThresholdSelector.STEPS):
            t = i / ThresholdSelector.STEPS
            cm = Evaluator.confusion(scored, t)
            c = costs.cost(cm)
            if best is None or c.compare_to(best.expected_cost) < 0:
                best = Threshold(t, cm, c)
        return best

    @staticmethod
    def best_band(scored: list[Scored], costs: CostModel) -> ReviewBand:
        """Sweep every (low, high) pair on a 100-bin histogram. A reviewed fraud is caught with probability
        (1 - review_miss_rate); a reviewed good customer is wrongly declined with probability review_insult_rate;
        every review costs review_cost regardless of outcome."""
        steps = ThresholdSelector.STEPS
        fraud_bins, legit_bins = [0] * (steps + 1), [0] * (steps + 1)
        for s in scored:
            b = min(steps, math.floor(s.probability * steps))
            if s.fraud:
                fraud_bins[b] += 1
            else:
                legit_bins[b] += 1
        fraud_cum, legit_cum = ThresholdSelector.cumulative(fraud_bins), ThresholdSelector.cumulative(legit_bins)
        best: Optional[ReviewBand] = None
        for lo in range(1, steps):
            for hi in range(lo, steps):
                fraud_below, legit_below = fraud_cum[lo], legit_cum[lo]
                fraud_mid, legit_mid = fraud_cum[hi] - fraud_cum[lo], legit_cum[hi] - legit_cum[lo]
                fraud_above, legit_above = fraud_cum[steps + 1] - fraud_cum[hi], legit_cum[steps + 1] - legit_cum[hi]
                reviewed = fraud_mid + legit_mid
                missed_in_review = java_round(fraud_mid * costs.review_miss_rate)
                insulted_in_review = java_round(legit_mid * costs.review_insult_rate)
                cost = (costs.fraud_loss.times(fraud_below + missed_in_review, 1, Rounding.HALF_EVEN)
                        .plus(costs.insult_cost.times(legit_above + insulted_in_review, 1, Rounding.HALF_EVEN))
                        .plus(costs.review_cost.times(reviewed, 1, Rounding.HALF_EVEN)))
                if best is None or cost.compare_to(best.expected_cost) < 0:
                    best = ReviewBand(lo / steps, hi / steps, fraud_below + legit_below, reviewed, fraud_above + legit_above, cost)
        return best

    @staticmethod
    def cumulative(bins: list[int]) -> list[int]:
        """cum[i] = number of items in bins [0, i). Length is bins + 1."""
        cum = [0] * (len(bins) + 1)
        for i in range(len(bins)):
            cum[i + 1] = cum[i] + bins[i]
        return cum


@dataclass(frozen=True)
class Bin:
    lower: float
    upper: float
    count: int
    mean_predicted: float
    observed_rate: float


class Calibration:
    """Reliability: does "p = 0.30" mean 30% of such transactions are fraud?"""

    Bin = Bin

    @staticmethod
    def reliability(scored: list[Scored], bins: int) -> list[Bin]:
        counts = [0] * bins
        sum_pred = [0.0] * bins
        frauds = [0] * bins
        for s in scored:
            b = min(bins - 1, math.floor(s.probability * bins))
            counts[b] += 1
            sum_pred[b] += s.probability
            if s.fraud:
                frauds[b] += 1
        out: list[Bin] = []
        for b in range(bins):
            mean_pred = 0.0 if counts[b] == 0 else sum_pred[b] / counts[b]
            observed = 0.0 if counts[b] == 0 else frauds[b] / counts[b]
            out.append(Bin(b / bins, (b + 1) / bins, counts[b], mean_pred, observed))
        return out

    @staticmethod
    def expected_calibration_error(bins: list[Bin]) -> float:
        """Count-weighted mean of |predicted - observed| over the bins."""
        total = sum(b.count for b in bins)
        if total == 0:
            return 0.0
        return fsum_seq(b.count / total * abs(b.mean_predicted - b.observed_rate) for b in bins)


class Outcome(Enum):
    APPROVE = "APPROVE"
    REVIEW = "REVIEW"
    DECLINE = "DECLINE"


@dataclass(frozen=True)
class Decision:
    outcome: Outcome
    explanation: Explanation


class DecisionEngine:
    """The production object: a frozen scorecard, a review band, and an explanation on every call."""

    def __init__(self, model: Scorecard, band: ReviewBand) -> None:
        if band.low > band.high:
            raise ValueError("low must not exceed high")
        self._model = model
        self._band = band

    def decide(self, f: Features) -> Decision:
        p = self._model.probability(f)
        outcome = Outcome.DECLINE if p >= self._band.high else Outcome.REVIEW if p >= self._band.low else Outcome.APPROVE
        return Decision(outcome, self._model.explain(f, 3))


def main() -> None:
    all_rows = SyntheticDataset.generate(42, 20_000, 0.02)
    train, test = SyntheticDataset.split(all_rows, 7, 0.7)
    train_fraud = sum(1 for e in train if e.fraud)
    test_fraud = sum(1 for e in test if e.fraud)
    print("== Dataset ==")
    print(f"{len(all_rows)} rows, {len(train)} train ({train_fraud} fraud), {len(test)} test ({test_fraud} fraud), "
          f"base rate {jf(100.0 * (train_fraud + test_fraud) / len(all_rows), 2)}%")
    print()

    model = Scorecard.train(train, 400, 0.5)
    print("== Scorecard ==")
    print(f"bias {jf(model.bias, 3, plus=True)}")
    for i in range(Features.COUNT):
        print(f"  {Features.NAMES[i]:<18} {jf(model.weights[i], 3, plus=True)}")
    print(f"train log loss {jf(model.log_loss(train), 4)}, test log loss {jf(model.log_loss(test), 4)}")
    print()

    scored = Evaluator.score(model, test)
    print("== Separation ==")
    print(f"AUC on test = {jf(Evaluator.auc(scored), 3)}")
    print()

    print("== Threshold sweep (test set) ==")
    print("thr   tp    fp    fn    precision recall  f1     insult%")
    for t in (0.05, 0.10, 0.20, 0.30, 0.50, 0.70, 0.90):
        cm = Evaluator.confusion(scored, t)
        print(f"{jf(t, 2)}  {cm.tp:<5d} {cm.fp:<5d} {cm.fn:<5d} {jf(cm.precision(), 3)}     {jf(cm.recall(), 3)}   "
              f"{jf(cm.f1(), 3)}  {jf(100 * cm.false_positive_rate(), 2)}")

    costs = CostModel(Money.parse("105.00", USD), Money.parse("12.00", USD), Money.parse("4.00", USD), 0.10, 0.05)
    by_f1 = ThresholdSelector.best_by_f1(scored, costs)
    by_cost = ThresholdSelector.best_by_cost(scored, costs)
    print()
    print(f"best F1   : threshold {jf(by_f1.value, 2)}, F1 {jf(by_f1.matrix.f1(), 3)}, expected cost {by_f1.expected_cost}")
    print(f"best cost : threshold {jf(by_cost.value, 2)}, F1 {jf(by_cost.matrix.f1(), 3)}, expected cost {by_cost.expected_cost}"
          f"  (fn {by_cost.matrix.fn}, fp {by_cost.matrix.fp})")
    approve_all = costs.cost(Evaluator.confusion(scored, 1.01))
    decline_all = costs.cost(Evaluator.confusion(scored, 0.0))
    print(f"approve everything: {approve_all}   decline everything: {decline_all}")

    band = ThresholdSelector.best_band(scored, costs)
    print()
    print("== Review band ==")
    print(f"approve < {jf(band.low, 2)}, review [{jf(band.low, 2)}, {jf(band.high, 2)}), decline >= {jf(band.high, 2)}")
    print(f"approved {band.approved}, reviewed {band.reviewed} ({jf(100.0 * band.reviewed / len(scored), 1)}%), "
          f"declined {band.declined}, expected cost {band.expected_cost}")
    print()

    print("== Calibration (test set, 10 bins) ==")
    print("bin          n      mean p   observed")
    bins = Calibration.reliability(scored, 10)
    for b in bins:
        if b.count == 0:
            continue
        print(f"[{jf(b.lower, 1)}, {jf(b.upper, 1)})  {b.count:<6d} {jf(b.mean_predicted, 3)}    {jf(b.observed_rate, 3)}")
    print(f"expected calibration error = {jf(Calibration.expected_calibration_error(bins), 4)}")
    print()

    engine = DecisionEngine(model, band)
    print("== Decisions with explanations ==")
    samples = [
        Features.of(0.1, False, False, 1, 400, 0),
        Features.of(1.2, True, False, 3, 10, 1),
        Features.of(2.0, True, True, 6, 2, 3),
    ]
    labels = ["regular customer, usual amount", "new device, young account, 3 attempts", "everything wrong at once"]
    for label, sample in zip(labels, samples):
        d = engine.decide(sample)
        print(f"{label} -> {d.outcome.value} (score {d.explanation.score}, p={jf(d.explanation.probability, 3)})")
        for c in d.explanation.top:
            print(f"    {c.feature:<18} value {jf(c.value, 2)} x weight {jf(c.weight, 2, plus=True)} = {jf(c.contribution, 2, plus=True)}")


if __name__ == "__main__":
    main()
