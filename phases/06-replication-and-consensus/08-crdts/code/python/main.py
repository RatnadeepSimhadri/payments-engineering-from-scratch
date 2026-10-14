"""Lesson: phases/06-replication-and-consensus/08-crdts/docs/en.md
CRDTs: Counters, Sets, and Convergence
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: Shapiro, Preguica, Baquero, Zawirski, "A comprehensive study of Convergent and Commutative
Replicated Data Types" (INRIA RR-7506, 2011); Money type from Phase 10 Lesson 01. Python 3.11+, stdlib only.
"""

from __future__ import annotations

from dataclasses import dataclass
from typing import Callable, Generic, Optional, Protocol, TypeVar

LONG_MIN = -(1 << 63)
LONG_MAX = (1 << 63) - 1


def add_exact(a: int, b: int) -> int:
    """Java's Math.addExact on longs: Python ints never overflow, so the 64-bit bound is checked by hand."""
    r = a + b
    if r < LONG_MIN or r > LONG_MAX:
        raise OverflowError("long overflow")
    return r


def subtract_exact(a: int, b: int) -> int:
    r = a - b
    if r < LONG_MIN or r > LONG_MAX:
        raise OverflowError("long overflow")
    return r


def to_int32(x: int) -> int:
    x &= 0xFFFFFFFF
    return x - (1 << 32) if x >= (1 << 31) else x


class JavaRandom:
    """java.util.Random, bit for bit: the 48-bit LCG, next(bits), nextInt(bound), nextBoolean. Same seed, same stream,
    so the property checks below draw exactly the states the Java version draws."""

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

    def next_boolean(self) -> bool:
        return self.next(1) != 0


def java_shuffle(items: list, rng: JavaRandom) -> None:
    """Collections.shuffle(list, rnd): Fisher-Yates from the end, swapping i-1 with nextInt(i)."""
    for i in range(len(items), 1, -1):
        j = rng.next_int(i)
        items[i - 1], items[j] = items[j], items[i - 1]


def fmt_map(m: dict) -> str:
    """Java's TreeMap.toString(): "{A=3, B=6}"."""
    return "{" + ", ".join(f"{k}={v}" for k, v in sorted(m.items())) + "}"


def fmt_list(xs) -> str:
    return "[" + ", ".join(str(x) for x in xs) + "]"


T = TypeVar("T")


class Crdt(Protocol):
    """A state-based (convergent) replicated data type. merge must be a join on a lattice: commutative, associative, and
    idempotent. Every implementation returns a NEW value from merge and leaves both inputs untouched."""

    def merge(self, other): ...


class GCounter:
    """Grow-only counter: one monotonically increasing slot per replica; value is the sum of slots."""

    def __init__(self, initial: Optional[dict[str, int]] = None) -> None:
        self._slots: dict[str, int] = dict(initial or {})

    def increment(self, replica: str, by: int) -> None:
        if by < 0:
            raise ValueError(f"a G-Counter only grows; got {by}")
        self._slots[replica] = add_exact(self._slots.get(replica, 0), by)

    def value(self) -> int:
        total = 0
        for v in self._slots.values():
            total = add_exact(total, v)
        return total

    def slots(self) -> dict[str, int]:
        return dict(self._slots)

    def merge(self, other: "GCounter") -> "GCounter":
        out = GCounter(self._slots)
        for k, v in other._slots.items():
            out._slots[k] = max(out._slots[k], v) if k in out._slots else v
        return out

    def __eq__(self, o: object) -> bool:
        return isinstance(o, GCounter) and self._slots == o._slots

    def __hash__(self) -> int:
        return hash(tuple(sorted(self._slots.items())))

    def __str__(self) -> str:
        return f"GCounter{fmt_map(self._slots)}={self.value()}"


class PNCounter:
    """Positive-negative counter: two G-Counters. value = increments - decrements."""

    def __init__(self, p: Optional[GCounter] = None, n: Optional[GCounter] = None) -> None:
        self.positive = p if p is not None else GCounter()
        self.negative = n if n is not None else GCounter()

    def increment(self, replica: str, by: int) -> None:
        self.positive.increment(replica, by)

    def decrement(self, replica: str, by: int) -> None:
        self.negative.increment(replica, by)

    def value(self) -> int:
        return subtract_exact(self.positive.value(), self.negative.value())

    def merge(self, other: "PNCounter") -> "PNCounter":
        return PNCounter(self.positive.merge(other.positive), self.negative.merge(other.negative))

    def __eq__(self, o: object) -> bool:
        return isinstance(o, PNCounter) and self.positive == o.positive and self.negative == o.negative

    def __hash__(self) -> int:
        return hash((self.positive, self.negative))

    def __str__(self) -> str:
        return f"PNCounter(+{self.positive.value()} -{self.negative.value()})={self.value()}"


class GSet(Generic[T]):
    """Grow-only set: add only; merge is set union."""

    def __init__(self) -> None:
        self._elements: set = set()

    def add(self, e: T) -> None:
        self._elements.add(e)

    def contains(self, e: T) -> bool:
        return e in self._elements

    def elements(self) -> frozenset:
        return frozenset(self._elements)

    def merge(self, other: "GSet[T]") -> "GSet[T]":
        out: GSet[T] = GSet()
        out._elements = self._elements | other._elements
        return out

    def __eq__(self, o: object) -> bool:
        return isinstance(o, GSet) and self._elements == o._elements

    def __hash__(self) -> int:
        return hash(frozenset(self._elements))

    def __str__(self) -> str:
        return "GSet" + fmt_list(sorted(self._elements))


@dataclass(frozen=True, order=True)
class Tag:
    """A globally unique tag minted by one replica: (replica, seq). Ordered so sets stay deterministic."""

    replica: str
    seq: int

    def __str__(self) -> str:
        return f"{self.replica}#{self.seq}"


class TagSource:
    """Per-replica tag source. Deterministic, no clock, no randomness."""

    def __init__(self, replica: str) -> None:
        self._replica = replica
        self._next = 1

    def next(self) -> Tag:
        t = Tag(self._replica, self._next)
        self._next += 1
        return t


class ORSet(Generic[T]):
    """Observed-Remove set (add-wins). Each add mints a unique tag; a remove tombstones only the tags the remover has
    observed. An element is present if it has at least one live (non-tombstoned) tag."""

    def __init__(self) -> None:
        self._added: dict = {}
        self._tombstones: set[Tag] = set()

    def add(self, e: T, tags: TagSource) -> Tag:
        t = tags.next()
        self._added.setdefault(e, set()).add(t)
        return t

    def remove(self, e: T) -> None:
        """Removes every tag of e that THIS replica currently observes. Tags it has not seen are unaffected."""
        tags = self._added.get(e)
        if tags is not None:
            self._tombstones |= tags

    def contains(self, e: T) -> bool:
        tags = self._added.get(e)
        if tags is None:
            return False
        return any(t not in self._tombstones for t in tags)

    def elements(self) -> list:
        return sorted(e for e in self._added if self.contains(e))

    def tombstone_count(self) -> int:
        return len(self._tombstones)

    def merge(self, other: "ORSet[T]") -> "ORSet[T]":
        out: ORSet[T] = ORSet()
        for k, v in self._added.items():
            out._added[k] = set(v)
        for k, v in other._added.items():
            out._added.setdefault(k, set()).update(v)
        out._tombstones = self._tombstones | other._tombstones
        return out

    def __eq__(self, o: object) -> bool:
        return isinstance(o, ORSet) and self._added == o._added and self._tombstones == o._tombstones

    def __hash__(self) -> int:
        return hash((frozenset((k, frozenset(v)) for k, v in self._added.items()), frozenset(self._tombstones)))

    def __str__(self) -> str:
        return f"ORSet{fmt_list(self.elements())} tombstones={len(self._tombstones)}"


@dataclass(frozen=True)
class LWWRegister(Generic[T]):
    """Last-writer-wins register. The writer is identified by (timestamp, replica); the replica id breaks timestamp ties
    so merge is deterministic. The losing value is discarded: that is the data loss Lesson 09 examines."""

    value: T
    timestamp: int
    replica: str

    def set(self, new_value: T, new_timestamp: int, writer: str) -> "LWWRegister[T]":
        return LWWRegister(new_value, new_timestamp, writer)

    def dominates(self, other: "LWWRegister[T]") -> bool:
        if self.timestamp != other.timestamp:
            return self.timestamp > other.timestamp
        return self.replica > other.replica

    def merge(self, other: "LWWRegister[T]") -> "LWWRegister[T]":
        return self if self.dominates(other) else other


@dataclass(frozen=True)
class Currency:
    """ISO 4217 currency, copied from Phase 10 Lesson 01."""

    code: str
    exponent: int

    def __post_init__(self) -> None:
        if self.code is None or len(self.code) != 3 or not all("A" <= ch <= "Z" for ch in self.code):
            raise ValueError(f"currency code must be three uppercase letters: {self.code}")
        if not 0 <= self.exponent <= 4:
            raise ValueError(f"exponent must be 0..4: {self.exponent}")

    def minor_units_per_major(self) -> int:
        return 10 ** self.exponent


USD = Currency("USD", 2)
EUR = Currency("EUR", 2)


class CurrencyMismatchException(ValueError):
    def __init__(self, a: Currency, b: Currency) -> None:
        super().__init__(f"cannot combine {a.code} with {b.code}")


@dataclass(frozen=True)
class Money:
    """Integer minor units + currency. Only the operations this lesson needs."""

    minor_units: int
    currency: Currency

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
        minor = add_exact(int(whole) * currency.minor_units_per_major(), int(padded) if padded else 0)
        if minor > LONG_MAX:
            raise OverflowError("long overflow")
        return Money(-minor if negative else minor, currency)

    def is_negative(self) -> bool:
        return self.minor_units < 0

    def __str__(self) -> str:
        scale = self.currency.minor_units_per_major()
        a = abs(self.minor_units)
        sign = "-" if self.minor_units < 0 else ""
        if self.currency.exponent == 0:
            return f"{sign}{a} {self.currency.code}"
        whole, frac = divmod(a, scale)
        return f"{sign}{whole}.{frac:0{self.currency.exponent}d} {self.currency.code}"


class ReplicatedBalance:
    """A balance replicated as a PN-Counter of minor units. Credits increment, debits decrement, merge is
    coordination-free. The type can tell you the balance went negative; it cannot prevent it."""

    def __init__(self, currency: Currency, counter: Optional[PNCounter] = None) -> None:
        self.currency = currency
        self.counter = counter if counter is not None else PNCounter()

    def _require_currency(self, m: Money) -> None:
        if m.currency != self.currency:
            raise CurrencyMismatchException(self.currency, m.currency)
        if m.minor_units < 0:
            raise ValueError(f"amount must be non-negative: {m}")

    def credit(self, replica: str, amount: Money) -> None:
        self._require_currency(amount)
        self.counter.increment(replica, amount.minor_units)

    def debit(self, replica: str, amount: Money) -> None:
        """Debits unconditionally. A local check against balance() is NOT a global guarantee; see the lesson."""
        self._require_currency(amount)
        self.counter.decrement(replica, amount.minor_units)

    def balance(self) -> Money:
        return Money.of_minor(self.counter.value(), self.currency)

    def merge(self, other: "ReplicatedBalance") -> "ReplicatedBalance":
        if other.currency != self.currency:
            raise CurrencyMismatchException(self.currency, other.currency)
        return ReplicatedBalance(self.currency, self.counter.merge(other.counter))

    def __eq__(self, o: object) -> bool:
        return isinstance(o, ReplicatedBalance) and self.currency == o.currency and self.counter == o.counter

    def __hash__(self) -> int:
        return hash((self.currency, self.counter))

    def __str__(self) -> str:
        return f"Balance {self.balance()}"


@dataclass(frozen=True)
class Result:
    trials: int
    commutative_failures: int
    associative_failures: int
    idempotent_failures: int

    def all_hold(self) -> bool:
        return self.commutative_failures == 0 and self.associative_failures == 0 and self.idempotent_failures == 0

    def __str__(self) -> str:
        def part(n: int) -> str:
            return "ok" if n == 0 else f"{n} failures"
        return (f"{self.trials} trials: commutative {part(self.commutative_failures)}, associative "
                f"{part(self.associative_failures)}, idempotent {part(self.idempotent_failures)}")


class CrdtLaws:
    """Property-style checks of the three merge laws on random replica states drawn from a seeded generator."""

    Result = Result

    @staticmethod
    def check(gen: Callable[[], Crdt], trials: int) -> Result:
        comm = assoc = idem = 0
        for _ in range(trials):
            a, b, c = gen(), gen(), gen()
            if a.merge(b) != b.merge(a):
                comm += 1
            if a.merge(b).merge(c) != a.merge(b.merge(c)):
                assoc += 1
            if a.merge(a) != a:
                idem += 1
        return Result(trials, comm, assoc, idem)

    @staticmethod
    def converges(states: list, rng: JavaRandom) -> bool:
        """Merge every state into every other in a random order and report whether all replicas agree."""
        result = list(states)
        for i in range(len(result)):
            order = [j for j in range(len(states)) if j != i]
            java_shuffle(order, rng)
            acc = result[i]
            for j in order:
                acc = acc.merge(states[j])
            result[i] = acc
        return all(t == result[0] for t in result)


class NaiveSumCounter:
    """A deliberately broken "counter" whose merge adds instead of taking the max. Fails idempotence."""

    def __init__(self, total: int) -> None:
        self.total = total

    def merge(self, other: "NaiveSumCounter") -> "NaiveSumCounter":
        return NaiveSumCounter(self.total + other.total)

    def __eq__(self, o: object) -> bool:
        return isinstance(o, NaiveSumCounter) and o.total == self.total

    def __hash__(self) -> int:
        return hash(self.total)


REPLICAS = ["A", "B", "C"]


def random_g_counter(rng: JavaRandom) -> GCounter:
    g = GCounter()
    for r in REPLICAS:
        if rng.next_boolean():
            g.increment(r, rng.next_int(50))
    return g


def random_pn_counter(rng: JavaRandom) -> PNCounter:
    c = PNCounter()
    for r in REPLICAS:
        if rng.next_boolean():
            c.increment(r, rng.next_int(50))
        if rng.next_boolean():
            c.decrement(r, rng.next_int(50))
    return c


def random_or_set(rng: JavaRandom, sources: dict[str, TagSource]) -> ORSet:
    s: ORSet[str] = ORSet()
    items = ["apple", "pear", "fig"]
    for _ in range(4):
        replica = REPLICAS[rng.next_int(3)]
        item = items[rng.next_int(len(items))]
        if rng.next_int(3) == 0:
            s.remove(item)
        else:
            s.add(item, sources[replica])
    return s


def main() -> None:
    rng = JavaRandom(20261013)

    print("== G-Counter: page views on three replicas, merged in any order ==")
    a, b, c = GCounter(), GCounter(), GCounter()
    a.increment("A", 3)
    b.increment("B", 5)
    c.increment("C", 2)
    b.increment("B", 1)
    ab = a.merge(b)
    abc = ab.merge(c)
    cba = c.merge(b).merge(a)
    print(f"A={a}  B={b}  C={c}")
    print(f"merge(A,B,C) = {abc}   merge(C,B,A) = {cba}   equal: {str(abc == cba).lower()}")
    print(f"merge(A,B) twice = {ab.merge(b)}   (idempotent: a stale message cannot double count)")

    print()
    print("== PN-Counter as a wallet balance: the merge is safe, the invariant is not ==")
    east = ReplicatedBalance(USD)
    east.credit("east", Money.parse("100.00", USD))
    west = east.merge(ReplicatedBalance(USD))   # west syncs the deposit
    print(f"both regions see {east.balance()}")
    # the partition: each region checks locally, each sees 100.00, each approves an 80.00 debit
    debit = Money.parse("80.00", USD)
    if not east.balance().is_negative():
        east.debit("east", debit)
    if not west.balance().is_negative():
        west.debit("west", debit)
    print(f"east after local debit: {east.balance()}   west after local debit: {west.balance()}")
    merged = east.merge(west)
    print(f"after the partition heals: {merged.balance()}   negative={str(merged.balance().is_negative()).lower()}"
          "  (both debits survive; nothing was lost, and nothing was prevented)")

    print()
    print("== G-Set and OR-Set: removing something a peer re-added ==")
    tags_a, tags_b = TagSource("A"), TagSource("B")
    cart_a: ORSet[str] = ORSet()
    cart_a.add("book", tags_a)
    cart_b = cart_a.merge(ORSet())   # B syncs
    cart_a.remove("book")            # A removes the tag it observed (A#1)
    cart_b.add("book", tags_b)       # B concurrently adds again (B#1)
    cart = cart_a.merge(cart_b)
    print(f"A removed book, B concurrently added book -> merged: {fmt_list(cart.elements())}"
          "  (add wins: B's tag was never observed by A's remove)")
    gs: GSet[str] = GSet()
    gs.add("book")
    print(f"G-Set has no remove at all: {gs}")

    print()
    print("== LWW-Register: deterministic, and lossy ==")
    r1 = LWWRegister("addr=Oak St", 10, "A")
    r2 = LWWRegister("addr=Elm St", 10, "B")
    print(f"A wrote '{r1.value}' at t=10, B wrote '{r2.value}' at t=10 -> merged keeps '{r1.merge(r2).value}'"
          " (tie broken by replica id; the other write is gone)")

    print()
    print("== Merge laws, property-style (seed 20261013) ==")
    print(f"G-Counter  {CrdtLaws.check(lambda: random_g_counter(rng), 500)}")
    print(f"PN-Counter {CrdtLaws.check(lambda: random_pn_counter(rng), 500)}")
    sources = {"A": TagSource("A"), "B": TagSource("B"), "C": TagSource("C")}
    print(f"OR-Set     {CrdtLaws.check(lambda: random_or_set(rng, sources), 500)}")
    print(f"NaiveSum   {CrdtLaws.check(lambda: NaiveSumCounter(rng.next_int(100)), 500)}  <- merge by addition is not a join")
    replicas = [random_pn_counter(rng) for _ in range(5)]
    print(f"5 PN-Counter replicas, all-to-all merge in shuffled orders, converge: {str(CrdtLaws.converges(replicas, rng)).lower()}")


if __name__ == "__main__":
    main()
