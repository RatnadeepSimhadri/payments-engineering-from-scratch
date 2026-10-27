"""Lesson: phases/08-messaging-and-streams/03-delivery-guarantees/docs/en.md
At-Most-Once, At-Least-Once, and Effectively-Once
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: KIP-98 (Exactly Once Delivery and Transactional Messaging in Kafka); Kafka docs section 4.6 "Message Delivery
         Semantics"; Helland, "Idempotence Is Not a Medical Condition" (ACM Queue, 2012). Money follows Phase 10, Lesson 01.
Python 3.11+, standard library only. Same design as the Java version. Crashes are injected by a FaultInjector at a chosen
offset; nothing is random.
"""

from __future__ import annotations

from dataclasses import dataclass
from enum import Enum
from typing import Dict, List, Optional, Set

# ---------------------------------------------------------------------------------------------------------------------
# Money, copied from Phase 10, Lesson 01: integer minor units plus a currency. Never a float.
# ---------------------------------------------------------------------------------------------------------------------


@dataclass(frozen=True)
class Currency:
    code: str
    exponent: int

    def __post_init__(self) -> None:
        if self.code is None or len(self.code) != 3:
            raise ValueError("currency code must be three letters")
        if self.exponent < 0 or self.exponent > 4:
            raise ValueError("exponent must be 0..4")

    def minor_units_per_major(self) -> int:
        return 10 ** self.exponent


USD = Currency("USD", 2)


class CurrencyMismatchError(Exception):
    def __init__(self, a: Currency, b: Currency) -> None:
        super().__init__(f"cannot combine {a.code} with {b.code}")


@dataclass(frozen=True)
class Money:
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
        if whole == "":
            whole = "0"
        if len(frac) > currency.exponent:
            raise ArithmeticError(f"{text} has too many decimals for {currency.code}")
        padded = frac + "0" * (currency.exponent - len(frac))
        minor = int(whole) * currency.minor_units_per_major() + (0 if padded == "" else int(padded))
        return Money(-minor if negative else minor, currency)

    def plus(self, other: "Money") -> "Money":
        if self.currency != other.currency:
            raise CurrencyMismatchError(self.currency, other.currency)
        return Money(self.minor_units + other.minor_units, self.currency)

    def __str__(self) -> str:
        scale = self.currency.minor_units_per_major()
        abs_units = abs(self.minor_units)
        sign = "-" if self.minor_units < 0 else ""
        if self.currency.exponent == 0:
            return f"{sign}{abs_units} {self.currency.code}"
        frac = str(abs_units % scale).rjust(self.currency.exponent, "0")
        return f"{sign}{abs_units // scale}.{frac} {self.currency.code}"


# ---------------------------------------------------------------------------------------------------------------------
# The log and the records on it. One partition is enough; the delivery question is per partition anyway.
# ---------------------------------------------------------------------------------------------------------------------


@dataclass(frozen=True)
class Record:
    """One captured payment on the log. payment_id is the business key that makes idempotent processing possible."""

    offset: int
    payment_id: str
    amount: Money


class Log:
    """A single-partition append-only log, the shape of Phase 8, Lesson 01 reduced to what this lesson needs."""

    def __init__(self) -> None:
        self._records: List[Record] = []

    def append(self, payment_id: str, amount: Money) -> int:
        offset = len(self._records)
        self._records.append(Record(offset, payment_id, amount))
        return offset

    def end_offset(self) -> int:
        return len(self._records)

    def read(self, from_offset: int, max_records: int) -> List[Record]:
        """Up to max_records from from_offset; empty at the end offset."""
        if from_offset < 0 or from_offset > len(self._records):
            raise ValueError(f"offset {from_offset} out of range")
        to = min(len(self._records), from_offset + max_records)
        return list(self._records[from_offset:to])


# ---------------------------------------------------------------------------------------------------------------------
# Faults. The process dies at a chosen point; the test chooses the point.
# ---------------------------------------------------------------------------------------------------------------------


class CrashError(Exception):
    """Raised when the simulated process dies. Everything not yet durably committed is gone."""

    def __init__(self, offset: int) -> None:
        super().__init__(f"process crashed after the side effect for offset {offset}, before the commit")


class FaultInjector:
    """Fires exactly once: after the side effect for crash_at_offset has been performed and before the offset commit."""

    def __init__(self, crash_at_offset: int) -> None:
        self._crash_at_offset = crash_at_offset
        self._armed = True

    @staticmethod
    def none() -> "FaultInjector":
        return FaultInjector(-1)

    def after_side_effect(self, offset: int) -> None:
        if self._armed and offset == self._crash_at_offset:
            self._armed = False
            raise CrashError(offset)

    def fired(self) -> bool:
        return not self._armed


# ---------------------------------------------------------------------------------------------------------------------
# The durable store: ledger postings, the processed-id set, and the committed offset, with optional atomicity.
# ---------------------------------------------------------------------------------------------------------------------


@dataclass(frozen=True)
class Posting:
    payment_id: str
    amount: Money


class Store:
    """Everything the consumer persists lives here, so a transaction can cover any combination of it. Writes made directly
    are durable immediately (one write, one fsync, one failure point each). Writes made through a Transaction become
    durable together or not at all."""

    def __init__(self) -> None:
        self._postings: List[Posting] = []
        self._processed: Set[str] = set()
        self._committed_offset = 0

    # --- direct, non-atomic writes -----------------------------------------------------------------------------------

    def post(self, posting: Posting) -> None:
        self._postings.append(posting)

    def post_if_new(self, posting: Posting) -> bool:
        """Insert-if-absent on the business key, in the same durable store as the postings. Returns False on a duplicate."""
        if posting.payment_id in self._processed:
            return False
        self._processed.add(posting.payment_id)
        self._postings.append(posting)
        return True

    def commit_offset(self, next_offset: int) -> None:
        if next_offset < 0:
            raise ValueError("offset must be non-negative")
        self._committed_offset = next_offset

    # --- atomic writes -------------------------------------------------------------------------------------------------

    def begin(self) -> "Store.Transaction":
        return Store.Transaction(self)

    class Transaction:
        """Buffers writes and applies them all in commit(). A crash before commit() leaves the store untouched.
        Also usable as a context manager: `with store.begin() as tx:` commits on a clean exit and discards the buffer
        when an exception (such as CrashError) propagates."""

        def __init__(self, store: "Store") -> None:
            self._store = store
            self._buffered_postings: List[Posting] = []
            self._buffered_offset = -1
            self._done = False

        def post(self, posting: Posting) -> None:
            self._require_open()
            self._buffered_postings.append(posting)

        def commit_offset(self, next_offset: int) -> None:
            self._require_open()
            self._buffered_offset = next_offset

        def commit(self) -> None:
            self._require_open()
            self._store._postings.extend(self._buffered_postings)
            for p in self._buffered_postings:
                self._store._processed.add(p.payment_id)
            if self._buffered_offset >= 0:
                self._store._committed_offset = self._buffered_offset
            self._done = True

        def _require_open(self) -> None:
            if self._done:
                raise RuntimeError("transaction already committed")

        def __enter__(self) -> "Store.Transaction":
            return self

        def __exit__(self, exc_type, exc, tb) -> bool:
            if exc_type is None:
                self.commit()
            return False

    # --- reads ---------------------------------------------------------------------------------------------------------

    def committed_offset(self) -> int:
        return self._committed_offset

    def postings(self) -> List[Posting]:
        return list(self._postings)

    def balance(self, currency: Currency) -> Money:
        total = Money.zero(currency)
        for p in self._postings:
            total = total.plus(p.amount)
        return total


# ---------------------------------------------------------------------------------------------------------------------
# The four ways to order "process" and "commit".
# ---------------------------------------------------------------------------------------------------------------------


class Strategy(Enum):
    AT_MOST_ONCE = "AT_MOST_ONCE"                          # Commit first, then process. A crash loses whatever was committed but not yet processed.
    AT_LEAST_ONCE = "AT_LEAST_ONCE"                        # Process first, then commit. A crash re-delivers whatever was processed but not yet committed.
    AT_LEAST_ONCE_IDEMPOTENT = "AT_LEAST_ONCE_IDEMPOTENT"  # At-least-once offsets, but the side effect is an insert-if-absent on the business key.
    TRANSACTIONAL = "TRANSACTIONAL"                        # Side effect and offset commit in one atomic transaction per record.

    def __str__(self) -> str:
        return self.name


class Consumer:
    """One consumer process. It starts from the store's committed offset and runs until the log is exhausted or it crashes."""

    def __init__(self, strategy: Strategy, log: Log, store: Store, faults: FaultInjector, batch_size: int) -> None:
        if batch_size <= 0:
            raise ValueError("batchSize must be positive")
        self._strategy = strategy
        self._log = log
        self._store = store
        self._faults = faults
        self._batch_size = batch_size

    def run_to_end(self) -> None:
        position = self._store.committed_offset()
        while True:
            batch = self._log.read(position, self._batch_size)
            if not batch:
                return
            batch_end = batch[-1].offset + 1
            if self._strategy is Strategy.AT_MOST_ONCE:
                self._store.commit_offset(batch_end)
                for r in batch:
                    self._store.post(Posting(r.payment_id, r.amount))
                    self._faults.after_side_effect(r.offset)
            elif self._strategy is Strategy.AT_LEAST_ONCE:
                for r in batch:
                    self._store.post(Posting(r.payment_id, r.amount))
                    self._faults.after_side_effect(r.offset)
                self._store.commit_offset(batch_end)
            elif self._strategy is Strategy.AT_LEAST_ONCE_IDEMPOTENT:
                for r in batch:
                    self._store.post_if_new(Posting(r.payment_id, r.amount))
                    self._faults.after_side_effect(r.offset)
                self._store.commit_offset(batch_end)
            elif self._strategy is Strategy.TRANSACTIONAL:
                for r in batch:
                    tx = self._store.begin()
                    tx.post(Posting(r.payment_id, r.amount))
                    tx.commit_offset(r.offset + 1)
                    self._faults.after_side_effect(r.offset)
                    tx.commit()
            position = batch_end


# ---------------------------------------------------------------------------------------------------------------------
# The experiment: run a strategy, crash it once, restart it, and count what the ledger ended up with.
# ---------------------------------------------------------------------------------------------------------------------


@dataclass(frozen=True)
class Outcome:
    strategy: Strategy
    posted: int
    duplicates: int
    lost: int
    crashes: int
    balance: Money

    def effectively_once(self) -> bool:
        return self.duplicates == 0 and self.lost == 0


class Simulation:
    @staticmethod
    def run(strategy: Strategy, log: Log, crash_at_offset: int, batch_size: int) -> Outcome:
        store = Store()
        faults = FaultInjector(crash_at_offset)
        crashes = 0
        while True:
            try:
                Consumer(strategy, log, store, faults, batch_size).run_to_end()
                break
            except CrashError:
                crashes += 1  # the process is gone; a fresh one starts from store.committed_offset()
        return Simulation.measure(strategy, log, store, crashes)

    @staticmethod
    def measure(strategy: Strategy, log: Log, store: Store, crashes: int) -> Outcome:
        counts: Dict[str, int] = {}
        for r in log.read(0, log.end_offset()):
            counts[r.payment_id] = 0
        for p in store.postings():
            counts[p.payment_id] = counts.get(p.payment_id, 0) + 1
        duplicates = lost = 0
        for c in counts.values():
            if c == 0:
                lost += 1
            if c > 1:
                duplicates += c - 1
        return Outcome(strategy, len(store.postings()), duplicates, lost, crashes, store.balance(USD))


def sample_log(count: int) -> Log:
    log = Log()
    for i in range(count):
        log.append(f"pay-{1000 + i}", Money.parse(f"{10 + i}.{(i * 37) % 100:02d}", USD))
    return log


def main() -> None:
    log = sample_log(10)
    expected = Money.zero(USD)
    for r in log.read(0, 10):
        expected = expected.plus(r.amount)
    print(f"== 10 captured payments on the log, batch size 4, expected ledger total {expected} ==")
    for r in log.read(0, 10):
        print(f"  offset {r.offset}  {r.payment_id}  {r.amount}")

    print("\n== Crash after the side effect for offset 5, before its commit; then restart ==")
    print(f"  {'strategy':<26} {'posted':<7} {'duplicates':<11} {'lost':<5} {'crashes':<8} ledger total")
    for s in Strategy:
        o = Simulation.run(s, log, 5, 4)
        tail = "  <- matches" if o.effectively_once() else "  <- " + ("money missing" if o.lost > 0 else "money counted twice")
        print(f"  {str(s):<26} {o.posted:<7} {o.duplicates:<11} {o.lost:<5} {o.crashes:<8} {o.balance}{tail}")

    print("\n== Why: the order of 'process' and 'commit' around the crash ==")
    print("  AT_MOST_ONCE    commit(8) -> post 4 -> post 5 -> CRASH -> restart at 8          : offsets 6,7 never posted")
    print("  AT_LEAST_ONCE   post 4 -> post 5 -> CRASH -> restart at 4 -> post 4,5 again      : offsets 4,5 posted twice")
    print("  IDEMPOTENT      same as above, but postIfNew(pay-1004) and (pay-1005) return false : duplicates absorbed")
    print("  TRANSACTIONAL   tx{post 5, commit 6} -> CRASH before tx.commit -> restart at 5     : nothing to absorb")

    print("\n== No crash at all: every strategy agrees ==")
    for s in Strategy:
        o = Simulation.run(s, log, -1, 4)
        print(f"  {str(s):<26} posted={o.posted} duplicates={o.duplicates} lost={o.lost}")

    print("\n== Crash at every offset 0..9: how many runs end with the right ledger? ==")
    for s in Strategy:
        correct = sum(1 for crash_at in range(10) if Simulation.run(s, log, crash_at, 4).effectively_once())
        print(f"  {str(s):<26} {correct} of 10")


if __name__ == "__main__":
    main()
