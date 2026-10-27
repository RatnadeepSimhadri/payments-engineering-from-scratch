"""Lesson: phases/14-payments-at-scale/04-reconciliation-at-scale/docs/en.md
Reconciliation at Scale: Streams, Buckets, and Breaks
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: ISO 20022 camt.053 (statement structure); Merkle, "A Digital Signature Based on a
Conventional Encryption Function" (hash-based set comparison); Phase 10 Lesson 08 (Reconciliation).
Python 3.11+, standard library only. Money is integer minor units + currency (Phase 10, Lesson 01).
"""

from __future__ import annotations

import gc
import time
from dataclasses import dataclass, field
from datetime import date, timedelta
from enum import Enum
from functools import total_ordering
from typing import Callable, Iterator, NamedTuple, Optional

MASK64 = 0xFFFFFFFFFFFFFFFF
EPOCH_ORDINAL = date(1970, 1, 1).toordinal()


def epoch_day(d: date) -> int:
    """LocalDate.toEpochDay: days since 1970-01-01."""
    return d.toordinal() - EPOCH_ORDINAL


def of_epoch_day(n: int) -> date:
    return date.fromordinal(n + EPOCH_ORDINAL)


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

    def is_zero(self) -> bool:
        return self.minor_units == 0

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


# ---------- Records and keys ----------


class Side(Enum):
    """Which feed a record came from. INTERNAL is our ledger; PROCESSOR is the acquirer's settlement file."""

    INTERNAL = "INTERNAL"
    PROCESSOR = "PROCESSOR"

    def __str__(self) -> str:
        return self.name


class BucketKey(NamedTuple):
    """The join key of the streaming merge: (txDate, currency, amount). Both feeds must be sorted by this key;
    the reconciler verifies the ordering and refuses to proceed on a violation, because a merge join over an
    unsorted feed silently reports every record as a break. A NamedTuple so that comparing, hashing and sorting
    a million keys run in C; a frozen dataclass with order=True does the same work in Python, several times slower."""

    epoch_day: int
    currency: str
    amount_minor: int

    def __str__(self) -> str:
        return f"{of_epoch_day(self.epoch_day)}/{self.currency}/{self.amount_minor}"


@dataclass(frozen=True)
class Rec:
    """One line of a feed. `ref` is the shared identifier both sides carry. `tx_date` is the date of the underlying
    transaction, which both sides know and which is therefore safe to join on. `value_date` is the date this side
    booked the record: capture date for us, settlement date for the processor."""

    ref: str
    tx_date: date
    value_date: date
    amount: Money
    side: Side

    def bucket(self) -> BucketKey:
        return BucketKey(self.tx_date.toordinal() - EPOCH_ORDINAL, self.amount.currency.code, self.amount.minor_units)

    def lag_days(self) -> int:
        """Days between the transaction and this side booking it; a processor lag beyond tolerance is a TIMING break."""
        return epoch_day(self.value_date) - epoch_day(self.tx_date)


class BucketDigest(NamedTuple):
    """Order-independent digest of a set of references. Each reference is mixed through a 64-bit finaliser and the
    results are summed; equal sets give equal digests regardless of order, and the count travels alongside.
    A NamedTuple: one is built per record, and tuple construction and equality run in C."""

    hash: int
    count: int
    total_minor: int

    @staticmethod
    def mix(ref: str) -> int:
        """The Java version relies on long overflow; here every multiply is masked to 64 bits and the shifts are
        logical because the value is kept unsigned. Iterating the UTF-8 bytes equals charAt for the ASCII refs."""
        h = 0x9E3779B97F4A7C15
        for b in ref.encode():
            h = ((h ^ b) * 0xBF58476D1CE4E5B9) & MASK64
        h ^= h >> 31
        h = (h * 0x94D049BB133111EB) & MASK64
        h ^= h >> 29
        return h

    def add(self, r: Rec) -> "BucketDigest":
        return BucketDigest((self[0] + BucketDigest.mix(r.ref)) & MASK64, self[1] + 1, self[2] + r.amount.minor_units)


BucketDigest.EMPTY = BucketDigest(0, 0, 0)


class BreakType(Enum):
    """How a break is explained. The order matters: the classifier tries them from most to least specific."""

    DUPLICATE = 0             # the same ref appears twice on one side
    AMOUNT_MISMATCH = 1       # same ref on both sides, different amount
    TIMING = 2                # same ref and amount, dates differ within the tolerance window
    MISSING_AT_PROCESSOR = 3  # we have it, they do not (unsettled capture, lost file line)
    MISSING_INTERNAL = 4      # they have it, we do not (unknown settlement, lost ledger write)

    def __str__(self) -> str:
        return self.name


class AgeBand(Enum):
    """Age bands for open breaks: how long since the record's value date, as of the run date."""

    D0_1 = 0
    D2_7 = 1
    D8_30 = 2
    D30_PLUS = 3

    def __str__(self) -> str:
        return self.name

    @staticmethod
    def of(days: int) -> "AgeBand":
        if days <= 1:
            return AgeBand.D0_1
        if days <= 7:
            return AgeBand.D2_7
        if days <= 30:
            return AgeBand.D8_30
        return AgeBand.D30_PLUS


@dataclass(frozen=True)
class Break:
    """One unexplained difference. `internal` or `processor` is None when that side is absent."""

    type: BreakType
    ref: str
    internal: Optional[Rec]
    processor: Optional[Rec]
    age_days: int

    def internal_amount(self, c: Currency) -> Money:
        return Money.zero(c) if self.internal is None else self.internal.amount

    def processor_amount(self, c: Currency) -> Money:
        return Money.zero(c) if self.processor is None else self.processor.amount

    def band(self) -> AgeBand:
        return AgeBand.of(self.age_days)

    def __str__(self) -> str:
        return (f"{self.type} ref={self.ref}"
                + ("" if self.internal is None else f" internal={self.internal.amount}@{self.internal.value_date}")
                + ("" if self.processor is None else f" processor={self.processor.amount}@{self.processor.value_date}")
                + f" age={self.age_days}d")


class ReconciliationReport:
    """The result of one reconciliation run. Totals are kept per side so the tie-out can be checked. The three
    totals are accumulated as plain minor-unit ints (`matched_minor`, ...) because a million `Money.plus` calls
    would dominate the run; the `Money` views below are what callers read."""

    def __init__(self, currency: Currency, as_of: date) -> None:
        self.currency = currency
        self.as_of = as_of
        self.buckets_seen = self.buckets_fast_matched = self.buckets_inspected = 0
        self.matched_count = 0
        self.matched_minor = 0
        self.internal_count = self.processor_count = 0
        self.internal_minor = 0
        self.processor_minor = 0
        self.breaks: list[Break] = []

    @property
    def matched_total(self) -> Money:
        return Money(self.matched_minor, self.currency)

    @property
    def internal_total(self) -> Money:
        return Money(self.internal_minor, self.currency)

    @property
    def processor_total(self) -> Money:
        return Money(self.processor_minor, self.currency)

    def count_by_type(self) -> dict:
        m: dict = {}
        for b in self.breaks:
            m[b.type] = m.get(b.type, 0) + 1
        return {t: m[t] for t in BreakType if t in m}  # enum order, like EnumMap

    def count_by_age(self) -> dict:
        m: dict = {}
        for b in self.breaks:
            m[b.band()] = m.get(b.band(), 0) + 1
        return {a: m[a] for a in AgeBand if a in m}

    def internal_in_breaks(self) -> Money:
        """Sum of internal-side amounts sitting in breaks (the "unexplained" column of our ledger)."""
        m = Money.zero(self.currency)
        for b in self.breaks:
            m = m.plus(b.internal_amount(self.currency))
        return m

    def processor_in_breaks(self) -> Money:
        m = Money.zero(self.currency)
        for b in self.breaks:
            m = m.plus(b.processor_amount(self.currency))
        return m

    def ties_out(self) -> bool:
        """The tie-out: every record on each side is either matched or inside exactly one break.
        A report that does not tie out has lost or double-counted a record and must not be published."""
        return (self.internal_total == self.matched_total.plus(self.internal_in_breaks())
                and self.processor_total == self.matched_total.plus(self.processor_in_breaks()))

    def summary(self) -> str:
        out = [f"as of {self.as_of}\n",
               f"internal:  {self.internal_count:,} records  {self.internal_total}\n",
               f"processor: {self.processor_count:,} records  {self.processor_total}\n",
               f"matched:   {self.matched_count:,} records  {self.matched_total}\n",
               f"buckets: {self.buckets_seen:,} seen, {self.buckets_fast_matched:,} fast-matched by digest, "
               f"{self.buckets_inspected:,} inspected record by record\n",
               f"breaks: {len(self.breaks):,}  (internal side {self.internal_in_breaks()}, processor side {self.processor_in_breaks()})\n"]
        for t, n in self.count_by_type().items():
            out.append(f"  {t.name:<22} {n:,}\n")
        out.append("aging: ")
        for a, n in self.count_by_age().items():
            out.append(f"{a.name}={n} ")
        out.append("\n")
        out.append(f"ties out: {str(self.ties_out()).lower()}\n")
        return "".join(out)


class Cursor:
    """A peekable, order-checking cursor over one sorted feed."""

    def __init__(self, it: Iterator[Rec], side: Side) -> None:
        self.it = it
        self.side = side
        self.head: Optional[Rec] = None
        self.head_key: Optional[BucketKey] = None  # head.bucket(), computed once per record
        self.last: Optional[BucketKey] = None
        self._advance()

    def has_next(self) -> bool:
        return self.head is not None

    def key(self) -> BucketKey:
        return self.head_key

    def drain_bucket(self, key: BucketKey, out: list, defer: Callable[[Rec], bool], deferred: list) -> BucketDigest:
        """Drain every record of the current bucket into `out` and fold it into the digest, except records the
        `defer` test selects, which go to `deferred` undigested."""
        d = BucketDigest.EMPTY
        advance = self._advance
        while self.head is not None and self.head_key == key:
            head = self.head
            if defer(head):
                deferred.append(head)
            else:
                out.append(head)
                d = d.add(head)
            advance()
        return d

    def _advance(self) -> None:
        nxt = next(self.it, None)
        if nxt is None:
            self.head = None
            self.head_key = None
            return
        if nxt.side is not self.side:
            raise ValueError(f"record {nxt.ref} is on the wrong feed")
        k = nxt.bucket()
        if self.last is not None and k < self.last:
            raise RuntimeError(f"{self.side} feed is not sorted: {k} after {self.last}")
        self.last = k
        self.head = nxt
        self.head_key = k


class StreamingReconciler:
    """Streaming merge-join reconciler. Consumes two feeds sorted by BucketKey, matches whole buckets by digest when
    they agree, inspects a bucket record by record only when the digests differ, and holds in memory only the
    current bucket plus the unmatched leftovers. Memory is proportional to the number of breaks, not records."""

    def __init__(self, currency: Currency, timing_tolerance_days: int) -> None:
        if timing_tolerance_days < 0:
            raise ValueError("tolerance must be >= 0")
        self.currency = currency
        self.timing_tolerance_days = timing_tolerance_days

    def reconcile(self, internal: Iterator[Rec], processor: Iterator[Rec], as_of: date) -> ReconciliationReport:
        r = ReconciliationReport(self.currency, as_of)
        a = Cursor(internal, Side.INTERNAL)
        b = Cursor(processor, Side.PROCESSOR)
        # Unmatched records, keyed by ref, waiting for a partner from a different bucket.
        leftover_internal: dict[str, list[Rec]] = {}
        leftover_processor: dict[str, list[Rec]] = {}
        buf_a: list[Rec] = []
        buf_b: list[Rec] = []
        late: list[Rec] = []
        tolerance = self.timing_tolerance_days

        def never(x: Rec) -> bool:
            return False

        def settled_late(x: Rec) -> bool:
            return x.lag_days() > tolerance

        while a.head is not None or b.head is not None:
            ka, kb = a.head_key, b.head_key  # None once that feed is exhausted
            if ka is None:
                key = kb
            elif kb is None:
                key = ka
            else:
                key = ka if ka <= kb else kb
            r.buckets_seen += 1

            buf_a.clear()
            buf_b.clear()
            late.clear()
            da = a.drain_bucket(key, buf_a, never, late) if ka == key else BucketDigest.EMPTY
            db = b.drain_bucket(key, buf_b, settled_late, late) if kb == key else BucketDigest.EMPTY
            r.internal_count += da.count
            r.processor_count += db.count + len(late)
            r.internal_minor += da.total_minor
            r.processor_minor += db.total_minor
            for y in late:
                r.processor_minor += y.amount.minor_units
                leftover_processor.setdefault(y.ref, []).append(y)

            if da == db and not late:
                # Fast path: same count, same digest, same total -> the bucket matches as a whole.
                r.buckets_fast_matched += 1
                r.matched_count += da.count
                r.matched_minor += da.total_minor
                continue
            r.buckets_inspected += 1
            self._inspect_bucket(buf_a, buf_b, r, leftover_internal, leftover_processor, as_of)
        self._classify_leftovers(leftover_internal, leftover_processor, r, as_of)
        r.breaks.sort(key=lambda br: (br.type.value, br.ref))
        return r

    def _inspect_bucket(self, buf_a: list, buf_b: list, r: ReconciliationReport,
                        leftover_internal: dict, leftover_processor: dict, as_of: date) -> None:
        """Within one bucket, pair records by ref. A second copy of a ref already seen in this bucket is a DUPLICATE
        on that side; anything unpaired is parked as a leftover for the cross-bucket pass."""
        by_ref: dict[str, Rec] = {}
        matched_here: set[str] = set()
        for x in buf_a:
            if x.ref in by_ref:
                r.breaks.append(Break(BreakType.DUPLICATE, x.ref, x, None, StreamingReconciler.age_days(x.tx_date, as_of)))
            else:
                by_ref[x.ref] = x
        for y in buf_b:
            x = by_ref.pop(y.ref, None)
            if x is not None:
                r.matched_count += 1                        # count the pair once
                r.matched_minor += x.amount.minor_units     # one amount, matched on both sides
                matched_here.add(y.ref)
            elif y.ref in matched_here:
                r.breaks.append(Break(BreakType.DUPLICATE, y.ref, None, y, StreamingReconciler.age_days(y.tx_date, as_of)))
            else:
                leftover_processor.setdefault(y.ref, []).append(y)
        for x in by_ref.values():
            leftover_internal.setdefault(x.ref, []).append(x)

    def _classify_leftovers(self, li: dict, lp: dict, r: ReconciliationReport, as_of: date) -> None:
        """Cross-bucket pass: pair leftovers by ref and name the break. Runs once, over breaks only."""
        refs = list(li.keys())
        for ref in lp.keys():
            if ref not in li:
                refs.append(ref)
        for ref in refs:
            xs = li.get(ref, [])
            ys = lp.get(ref, [])
            pairs = min(len(xs), len(ys))
            for i in range(pairs):
                x, y = xs[i], ys[i]
                age = StreamingReconciler.age_days(x.tx_date, as_of)
                if x.amount != y.amount:
                    r.breaks.append(Break(BreakType.AMOUNT_MISMATCH, ref, x, y, age))
                elif abs(epoch_day(x.value_date) - epoch_day(y.value_date)) <= self.timing_tolerance_days:
                    # Same ref, same amount, dates within tolerance: matched, but recorded for visibility.
                    r.matched_count += 1
                    r.matched_minor += x.amount.minor_units
                else:
                    r.breaks.append(Break(BreakType.TIMING, ref, x, y, age))
            for i in range(pairs, len(xs)):
                x = xs[i]
                t = BreakType.DUPLICATE if (i > 0 or pairs > 0) else BreakType.MISSING_AT_PROCESSOR
                r.breaks.append(Break(t, ref, x, None, StreamingReconciler.age_days(x.tx_date, as_of)))
            for i in range(pairs, len(ys)):
                y = ys[i]
                t = BreakType.DUPLICATE if (i > 0 or pairs > 0) else BreakType.MISSING_INTERNAL
                r.breaks.append(Break(t, ref, None, y, StreamingReconciler.age_days(y.tx_date, as_of)))

    @staticmethod
    def age_days(record_date: date, as_of: date) -> int:
        return max(0, epoch_day(as_of) - epoch_day(record_date))


# ---------- Seeded feeds ----------


class JavaRandom:
    """java.util.Random, bit for bit (the JDK's 48-bit LCG), so seed 42 builds the same feeds as the Java demo."""

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
        # next(31), inlined: this is the hot path of the feed generator.
        seed = (self.seed * self.MULTIPLIER + self.ADDEND) & self.MASK
        self.seed = seed
        r = seed >> 17
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


_B36 = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ"
_B36_PAIRS = [a + b for a in _B36 for b in _B36]  # 1296 two-digit strings, so the loop below takes half the steps


def to_base36(n: int) -> str:
    """Long.toString(n, 36).toUpperCase() for non-negative n."""
    out = []
    while n >= 36:
        n, rem = divmod(n, 1296)
        out.append(_B36_PAIRS[rem])
    out.append(_B36[n])
    text = "".join(reversed(out))
    return text.lstrip("0") or "0"


@dataclass(frozen=True)
class Feeds:
    internal: list
    processor: list
    injected_breaks: int


class FeedGenerator:
    """Seeded generator of two feeds with a known number of injected breaks, so the demo and the tests can assert
    exactly what the reconciler must find. Each feed is returned sorted by BucketKey."""

    @staticmethod
    def generate(seed: int, records: int, start: date, days: int, c: Currency, break_rate: float) -> Feeds:
        rnd = JavaRandom(seed)
        internal: list[Rec] = []
        processor: list[Rec] = []
        injected = 0
        one_day = timedelta(days=1)
        day = [start + timedelta(days=k) for k in range(days)]  # the same dates, without a million timedelta additions
        for i in range(records):
            ref = "TX" + to_base36(1_000_000_000 + i)
            d = day[rnd.next_int(days)]
            cents = 100 + rnd.next_int(50_000)                 # 1.00 .. 500.99
            if rnd.next_int(5) == 0:
                cents = (cents // 100) * 100                   # round prices collide in buckets
            amt = Money.of_minor(cents, c)
            internal.append(Rec(ref, d, d, amt, Side.INTERNAL))
            if rnd.next_double() < break_rate:
                injected += 1
                case = rnd.next_int(4)
                if case == 0:
                    pass  # missing at processor: emit nothing
                elif case == 1:
                    processor.append(Rec(ref, d, d + one_day, Money.of_minor(cents + 1, c), Side.PROCESSOR))  # off by a cent
                elif case == 2:
                    processor.append(Rec(ref, d, d + timedelta(days=5), amt, Side.PROCESSOR))                # settled late
                else:                                                                                         # file line twice
                    processor.append(Rec(ref, d, d + one_day, amt, Side.PROCESSOR))
                    processor.append(Rec(ref, d, d + one_day, amt, Side.PROCESSOR))
            else:
                # Normal case: processor settles same day or next day (within tolerance).
                processor.append(Rec(ref, d, d + timedelta(days=rnd.next_int(2)), amt, Side.PROCESSOR))
        # A handful of records only the processor knows about.
        for i in range(max(1, records // 200_000)):
            injected += 1
            d = day[rnd.next_int(days)]
            processor.append(Rec(f"UNKNOWN{i}", d, d + one_day, Money.of_minor(999, c), Side.PROCESSOR))
        FeedGenerator.sort_feed(internal)
        FeedGenerator.sort_feed(processor)
        return Feeds(internal, processor, injected)

    @staticmethod
    def sort_feed(feed: list) -> None:
        # The same (BucketKey, ref) order as the Java comparator, spelled as one flat tuple so the sort key is cheap.
        feed.sort(key=lambda r: (r.tx_date.toordinal(), r.amount.currency.code, r.amount.minor_units, r.ref))


def main() -> None:
    as_of = date(2026, 3, 31)

    print("== A small run you can read ==")
    m28, m29, m1 = date(2026, 3, 28), date(2026, 3, 29), date(2026, 3, 1)
    internal = [
        Rec("A1", m28, m28, Money.of_minor(1999, USD), Side.INTERNAL),
        Rec("A2", m28, m28, Money.of_minor(1999, USD), Side.INTERNAL),
        Rec("B1", m28, m28, Money.of_minor(5000, USD), Side.INTERNAL),
        Rec("C1", m29, m29, Money.of_minor(1250, USD), Side.INTERNAL),
        Rec("D1", m1, m1, Money.of_minor(700, USD), Side.INTERNAL),
    ]
    processor = [
        Rec("A1", m28, m29, Money.of_minor(1999, USD), Side.PROCESSOR),             # settled T+1: fine
        Rec("A2", m28, m29, Money.of_minor(1999, USD), Side.PROCESSOR),
        Rec("B1", m28, date(2026, 4, 3), Money.of_minor(5000, USD), Side.PROCESSOR),  # settled T+6: TIMING
        Rec("C1", m29, m29, Money.of_minor(1200, USD), Side.PROCESSOR),             # 12.00 vs 12.50: AMOUNT_MISMATCH
        Rec("E9", m29, m29, Money.of_minor(4200, USD), Side.PROCESSOR),             # we never saw it: MISSING_INTERNAL
    ]
    FeedGenerator.sort_feed(internal)
    FeedGenerator.sort_feed(processor)
    rec = StreamingReconciler(USD, 2)
    small = rec.reconcile(iter(internal), iter(processor), as_of)
    print(small.summary(), end="")
    for b in small.breaks:
        print(f"  {b}")

    print("\n== 1,000,000 records, seeded, 0.1% injected breaks ==")
    # A million live records make CPython's cyclic collector rescan the heap on every allocation burst; the feeds
    # hold no cycles, so the collector is paused for the benchmark. It is a measurement choice, not a correctness one.
    gc.disable()
    t0 = time.perf_counter()
    feeds = FeedGenerator.generate(42, 500_000, date(2026, 3, 1), 30, USD, 0.001)
    t1 = time.perf_counter()
    big = rec.reconcile(iter(feeds.internal), iter(feeds.processor), as_of)
    t2 = time.perf_counter()
    print(f"generated {len(feeds.internal):,} + {len(feeds.processor):,} records in {int((t1 - t0) * 1000)} ms "
          f"({feeds.injected_breaks} break situations injected)")
    print(f"reconciled in {int((t2 - t1) * 1000)} ms")
    print(big.summary(), end="")
    print("first three breaks: ")
    for b in big.breaks[:3]:
        print(f"  {b}")

    print("\n== An unsorted feed is refused, not silently mis-reconciled ==")
    try:
        shuffled = list(internal)
        shuffled[0], shuffled[-1] = shuffled[-1], shuffled[0]
        rec.reconcile(iter(shuffled), iter(processor), as_of)
    except RuntimeError as e:
        print(f"rejected: {e}")
    if not big.ties_out() or not small.ties_out():
        print("TIE-OUT FAILED")
        raise SystemExit(1)


if __name__ == "__main__":
    main()
