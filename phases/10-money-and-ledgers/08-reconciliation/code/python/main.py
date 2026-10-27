"""Lesson: phases/10-money-and-ledgers/08-reconciliation/docs/en.md
Reconciliation: Matching Your Ledger Against Theirs
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: ISO 4217 (minor units); ISO 20022 camt.053 (bank statement structure the "theirs" side models).
Python 3.11+, standard library only. Money is integer minor units, copied from Phase 10, Lesson 01.
"""

from __future__ import annotations

from dataclasses import dataclass
from datetime import date
from enum import Enum
from functools import total_ordering
from typing import Dict, List, Optional, Sequence, Tuple


@dataclass(frozen=True)
class Currency:
    """ISO 4217 currency: three-letter code and minor-unit exponent. Same design as Phase 10, Lesson 01."""

    code: str
    exponent: int

    def __post_init__(self) -> None:
        if len(self.code) != 3 or not self.code.isalpha() or not self.code.isupper():
            raise ValueError(f"currency code must be three uppercase letters: {self.code}")
        if not 0 <= self.exponent <= 4:
            raise ValueError(f"exponent must be 0..4: {self.exponent}")

    def minor_units_per_major(self) -> int:
        return 10 ** self.exponent


USD = Currency("USD", 2)
EUR = Currency("EUR", 2)
GBP = Currency("GBP", 2)
JPY = Currency("JPY", 0)


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
            raise ValueError(f"not a decimal amount: {text}")
        if len(frac) > currency.exponent:
            raise ArithmeticError(f"{text} has more decimals than {currency.code} allows")
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

    def negate(self) -> "Money":
        return Money(-self.minor_units, self.currency)

    def abs(self) -> "Money":
        return self.negate() if self.minor_units < 0 else self

    def is_zero(self) -> bool:
        return self.minor_units == 0

    def __lt__(self, other: "Money") -> bool:
        self._require_same_currency(other)
        return self.minor_units < other.minor_units

    def __str__(self) -> str:
        scale = self.currency.minor_units_per_major()
        magnitude = abs(self.minor_units)
        sign = "-" if self.minor_units < 0 else ""
        if self.currency.exponent == 0:
            return f"{sign}{magnitude} {self.currency.code}"
        whole, frac = divmod(magnitude, scale)
        return f"{sign}{whole}.{str(frac).rjust(self.currency.exponent, '0')} {self.currency.code}"

    __repr__ = __str__


@dataclass(frozen=True)
class LedgerPosting:
    """One line from OUR ledger: a posting against the settlement account that we expect the processor to confirm."""

    reference: str
    date: date
    amount: Money
    account: str


@dataclass(frozen=True)
class StatementLine:
    """One line from THEIR statement: what the processor or bank says happened."""

    reference: str
    date: date
    amount: Money
    description: str


class MatchRule(Enum):
    """Which pass produced a match. EXACT_REFERENCE is strong evidence; AMOUNT_DATE is a heuristic a human may review."""

    EXACT_REFERENCE = "exact_reference"
    AMOUNT_DATE = "amount_date"


@dataclass(frozen=True)
class Match:
    """A posting of ours paired with a line of theirs. Amounts are equal by construction."""

    ours: LedgerPosting
    theirs: StatementLine
    rule: MatchRule

    def __post_init__(self) -> None:
        if self.ours.amount != self.theirs.amount:
            raise ValueError(f"a Match must have equal amounts; got {self.ours.amount} vs {self.theirs.amount}")


class BreakKind(Enum):
    """The five ways two books disagree. The value is the report order, which is also the order an operations
    analyst works them: money that is missing first, duplicates last."""

    MISSING_OURS = 0      # on their statement, nowhere in our ledger
    MISSING_THEIRS = 1    # in our ledger, nowhere on their statement
    AMOUNT_MISMATCH = 2   # same reference on both sides, different amount
    DUPLICATE_OURS = 3    # our ledger has more copies of a reference than their statement
    DUPLICATE_THEIRS = 4  # their statement has more copies of a reference than our ledger


@dataclass(frozen=True)
class Break:
    """One unexplained item. Exactly one of ours/theirs is None for MISSING_* and DUPLICATE_*; both are set for AMOUNT_MISMATCH."""

    kind: BreakKind
    ours: Optional[LedgerPosting]
    theirs: Optional[StatementLine]

    def __post_init__(self) -> None:
        if not isinstance(self.kind, BreakKind):
            raise TypeError("kind")
        if self.kind in (BreakKind.MISSING_OURS, BreakKind.DUPLICATE_THEIRS):
            if self.ours is not None or self.theirs is None:
                raise ValueError(f"{self.kind.name} needs theirs only")
        elif self.kind in (BreakKind.MISSING_THEIRS, BreakKind.DUPLICATE_OURS):
            if self.ours is None or self.theirs is not None:
                raise ValueError(f"{self.kind.name} needs ours only")
        elif self.ours is None or self.theirs is None:
            raise ValueError(f"{self.kind.name} needs both sides")

    def ours_amount(self, currency: Currency) -> Money:
        return Money.zero(currency) if self.ours is None else self.ours.amount

    def theirs_amount(self, currency: Currency) -> Money:
        return Money.zero(currency) if self.theirs is None else self.theirs.amount

    def difference(self, currency: Currency) -> Money:
        """ours - theirs: positive means we recorded more than they did."""
        return self.ours_amount(currency).minus(self.theirs_amount(currency))

    def reference(self) -> str:
        return self.ours.reference if self.ours is not None else self.theirs.reference  # type: ignore[union-attr]

    def date(self) -> date:
        return self.ours.date if self.ours is not None else self.theirs.date  # type: ignore[union-attr]


@dataclass(frozen=True)
class ReconciliationReport:
    """The result of one reconciliation run. Every input record appears in exactly one Match or exactly one Break,
    which is what makes the totals tie out: ledger_total == matched_total + ours_unmatched and
    statement_total == matched_total + theirs_unmatched."""

    currency: Currency
    matches: Tuple[Match, ...]
    breaks: Tuple[Break, ...]
    ledger_total: Money
    statement_total: Money

    def __post_init__(self) -> None:
        object.__setattr__(self, "matches", tuple(self.matches))
        object.__setattr__(self, "breaks", tuple(self.breaks))

    def matched_total(self) -> Money:
        total = Money.zero(self.currency)
        for m in self.matches:
            total = total.plus(m.ours.amount)
        return total

    def ours_unmatched(self) -> Money:
        total = Money.zero(self.currency)
        for b in self.breaks:
            total = total.plus(b.ours_amount(self.currency))
        return total

    def theirs_unmatched(self) -> Money:
        total = Money.zero(self.currency)
        for b in self.breaks:
            total = total.plus(b.theirs_amount(self.currency))
        return total

    def net_difference(self) -> Money:
        """ledger_total - statement_total, which must equal ours_unmatched - theirs_unmatched."""
        return self.ledger_total.minus(self.statement_total)

    def ties_out(self) -> bool:
        """The tie-out: the only number a controller signs."""
        return (self.ledger_total == self.matched_total().plus(self.ours_unmatched())
                and self.statement_total == self.matched_total().plus(self.theirs_unmatched())
                and self.net_difference() == self.ours_unmatched().minus(self.theirs_unmatched()))

    def count_breaks(self, kind: BreakKind) -> int:
        return sum(1 for b in self.breaks if b.kind is kind)

    def is_clean(self) -> bool:
        return not self.breaks

    def render(self) -> str:
        out = [f"MATCHES ({len(self.matches)})"]
        for m in self.matches:
            refs = m.ours.reference if m.ours.reference == m.theirs.reference else f"{m.ours.reference} <-> {m.theirs.reference}"
            out.append(f"  {m.rule.name:<16} {m.ours.date} {str(m.ours.amount):>12}  {refs}")
        out.append(f"BREAKS ({len(self.breaks)})")
        c = self.currency
        for b in self.breaks:
            out.append(f"  {b.kind.name:<16} {b.date()} ours={str(b.ours_amount(c)):>12} theirs={str(b.theirs_amount(c)):>12} "
                       f"diff={str(b.difference(c)):>12}  {b.reference()}")
        out.append("TOTALS")
        out.append(f"  ledger total      {str(self.ledger_total):>14}")
        out.append(f"  statement total   {str(self.statement_total):>14}")
        out.append(f"  matched           {str(self.matched_total()):>14}")
        out.append(f"  ours unmatched    {str(self.ours_unmatched()):>14}")
        out.append(f"  theirs unmatched  {str(self.theirs_unmatched()):>14}")
        out.append(f"  net difference    {str(self.net_difference()):>14}  "
                   f"(ours unmatched - theirs unmatched = {self.ours_unmatched().minus(self.theirs_unmatched())})")
        out.append(f"  ties out          {'YES' if self.ties_out() else 'NO'}")
        return "\n".join(out) + "\n"


def _ours_order(p: LedgerPosting):
    return (p.date, p.reference, p.amount.minor_units)


def _theirs_order(l: StatementLine):
    return (l.date, l.reference, l.amount.minor_units)


class Reconciler:
    """Three-pass matcher. Pass 1 pairs by reference id (and finds amount mismatches and duplicates).
    Pass 2 pairs the leftovers by equal amount and a date within tolerance_days, nearest date first.
    Pass 3 labels whatever is still unmatched as missing on one side. Every step is deterministic:
    inputs are sorted by (date, reference, amount, input position) before any decision is made."""

    OURS_ORDER = staticmethod(_ours_order)
    THEIRS_ORDER = staticmethod(_theirs_order)

    def __init__(self, currency: Currency, tolerance_days: int) -> None:
        if tolerance_days < 0:
            raise ValueError("tolerance_days must be >= 0")
        self._currency = currency
        self._tolerance_days = tolerance_days

    def reconcile(self, ledger: Sequence[LedgerPosting], statement: Sequence[StatementLine]) -> ReconciliationReport:
        ours = list(ledger)
        theirs = list(statement)
        ledger_total = Money.zero(self._currency)
        for p in ours:
            ledger_total = ledger_total.plus(p.amount)  # raises on a foreign currency
        statement_total = Money.zero(self._currency)
        for l in theirs:
            statement_total = statement_total.plus(l.amount)
        ours.sort(key=_ours_order)  # stable: equal keys keep input order
        theirs.sort(key=_theirs_order)

        matches: List[Match] = []
        breaks: List[Break] = []
        ours_left: List[LedgerPosting] = []
        theirs_left: List[StatementLine] = []

        self._match_by_reference(ours, theirs, matches, breaks, ours_left, theirs_left)
        self._match_by_amount_and_date(ours_left, theirs_left, matches)
        for p in ours_left:
            breaks.append(Break(BreakKind.MISSING_THEIRS, p, None))
        for l in theirs_left:
            breaks.append(Break(BreakKind.MISSING_OURS, None, l))

        matches.sort(key=lambda m: (m.ours.date, m.ours.reference, m.ours.amount.minor_units))
        breaks.sort(key=lambda b: (b.kind.value, b.date(), b.reference()))
        report = ReconciliationReport(self._currency, tuple(matches), tuple(breaks), ledger_total, statement_total)
        if not report.ties_out():
            raise RuntimeError("reconciliation report does not tie out; this is a bug in the matcher")
        return report

    def _match_by_reference(self, ours: List[LedgerPosting], theirs: List[StatementLine], matches: List[Match],
                            breaks: List[Break], ours_left: List[LedgerPosting], theirs_left: List[StatementLine]) -> None:
        """Pass 1: group both sides by reference, visiting references in sorted order like Java's TreeMap."""
        ours_by_ref: Dict[str, List[LedgerPosting]] = {}
        theirs_by_ref: Dict[str, List[StatementLine]] = {}
        for p in ours:
            ours_by_ref.setdefault(p.reference, []).append(p)
        for l in theirs:
            theirs_by_ref.setdefault(l.reference, []).append(l)

        for ref in sorted(ours_by_ref):
            o = ours_by_ref[ref]
            t = theirs_by_ref.get(ref, [])
            if not t:
                ours_left.extend(o)  # nothing to pair with: try pass 2
                continue
            pairs = min(len(o), len(t))
            for i in range(pairs):
                if o[i].amount == t[i].amount:
                    matches.append(Match(o[i], t[i], MatchRule.EXACT_REFERENCE))
                else:
                    breaks.append(Break(BreakKind.AMOUNT_MISMATCH, o[i], t[i]))
            for i in range(pairs, len(o)):
                breaks.append(Break(BreakKind.DUPLICATE_OURS, o[i], None))
            for i in range(pairs, len(t)):
                breaks.append(Break(BreakKind.DUPLICATE_THEIRS, None, t[i]))
        for ref in sorted(theirs_by_ref):
            if ref not in ours_by_ref:
                theirs_left.extend(theirs_by_ref[ref])
        ours_left.sort(key=_ours_order)
        theirs_left.sort(key=_theirs_order)

    def _match_by_amount_and_date(self, ours_left: List[LedgerPosting], theirs_left: List[StatementLine],
                                  matches: List[Match]) -> None:
        """Pass 2: greedy nearest-date match on equal amounts. Removes matched items from both leftover lists."""
        still_ours: List[LedgerPosting] = []
        for p in ours_left:
            best = -1
            best_distance = None
            for j, l in enumerate(theirs_left):
                if l.amount != p.amount:
                    continue
                distance = abs((l.date - p.date).days)
                if distance > self._tolerance_days:
                    continue
                if best_distance is None or distance < best_distance:  # strict <: first in sorted order wins ties
                    best, best_distance = j, distance
            if best >= 0:
                matches.append(Match(p, theirs_left.pop(best), MatchRule.AMOUNT_DATE))
            else:
                still_ours.append(p)
        ours_left[:] = still_ours


def ours(ref: str, day: str, amount: str) -> LedgerPosting:
    return LedgerPosting(ref, date.fromisoformat(day), Money.parse(amount, USD), "1010 Settlement Bank")


def theirs(ref: str, day: str, amount: str) -> StatementLine:
    return StatementLine(ref, date.fromisoformat(day), Money.parse(amount, USD), "CARD SETTLEMENT")


def main() -> None:
    ledger = [
        ours("ch_1001", "2026-03-02", "19.99"),
        ours("ch_1002", "2026-03-02", "250.00"),
        ours("ch_1003", "2026-03-02", "7.50"),
        ours("ch_1004", "2026-03-03", "120.00"),
        ours("ch_1005", "2026-03-03", "49.95"),
        ours("ch_1006", "2026-03-03", "1500.00"),
        ours("ch_1007", "2026-03-04", "33.10"),
        ours("int_8841", "2026-03-04", "64.00"),    # our reference; processor used its own id
        ours("ch_1009", "2026-03-04", "88.88"),     # we have it, they do not
        ours("ch_1010", "2026-03-05", "12.00"),     # amount disagrees with theirs
    ]
    statement = [
        theirs("ch_1001", "2026-03-02", "19.99"),
        theirs("ch_1002", "2026-03-02", "250.00"),
        theirs("ch_1003", "2026-03-02", "7.50"),
        theirs("ch_1004", "2026-03-03", "120.00"),
        theirs("ch_1005", "2026-03-03", "49.95"),
        theirs("ch_1006", "2026-03-03", "1500.00"),
        theirs("ch_1007", "2026-03-04", "33.10"),
        theirs("ch_1007", "2026-03-04", "33.10"),   # they posted it twice
        theirs("txn_77f2", "2026-03-05", "64.00"),  # their id for our int_8841, settled a day later
        theirs("ch_1010", "2026-03-05", "21.00"),   # we say 12.00
        theirs("ch_1011", "2026-03-05", "5.00"),    # they have it, we do not
    ]

    reconciler = Reconciler(USD, 1)
    report = reconciler.reconcile(ledger, statement)

    print("== Reconciling 10 ledger postings against 11 statement lines (tolerance 1 day) ==")
    print(report.render(), end="")
    print()
    print("== Reading the breaks ==")
    print("MISSING_OURS ch_1011 5.00: they settled something we never recorded; look for a lost webhook.")
    print("MISSING_THEIRS ch_1009 88.88: we recorded a settlement they never sent; still in transit or declined.")
    print("AMOUNT_MISMATCH ch_1010 12.00 vs 21.00: a transposed digit on one side; the diff of -9.00 is the classic sign.")
    print("DUPLICATE_THEIRS ch_1007: their file double-posted; dispute it, do not book it.")
    print(f"net difference {report.net_difference()} = {report.ours_unmatched()} - {report.theirs_unmatched()}")


if __name__ == "__main__":
    main()
