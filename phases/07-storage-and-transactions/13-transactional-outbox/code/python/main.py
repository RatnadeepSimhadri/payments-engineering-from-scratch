"""Lesson: phases/07-storage-and-transactions/13-transactional-outbox/docs/en.md
The Transactional Outbox and Dual-Write Problem
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: Richardson, "Microservices Patterns" ch. 3 (transactional outbox); Debezium outbox event router docs;
Helland, "Life beyond Distributed Transactions" (CIDR 2007). Money is integer minor units (Phase 10, Lesson 01).
"""

from __future__ import annotations

from dataclasses import dataclass, replace
from enum import Enum

INT64_MIN, INT64_MAX = -(1 << 63), (1 << 63) - 1


@dataclass(frozen=True)
class Currency:
    """ISO 4217 currency, copied from Phase 10 Lesson 01."""

    code: str
    exponent: int

    def minor_units_per_major(self) -> int:
        return 10 ** self.exponent


Currency.USD = Currency("USD", 2)


class CurrencyMismatchException(ValueError):
    """Java: extends IllegalArgumentException, so here it extends ValueError."""

    def __init__(self, a: Currency, b: Currency) -> None:
        super().__init__(f"cannot combine {a.code} with {b.code}")


@dataclass(frozen=True)
class Money:
    """Exact amount of one currency in signed minor units. Copied from Phase 10 Lesson 01."""

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

    def plus(self, other: "Money") -> "Money":
        if self.currency != other.currency:
            raise CurrencyMismatchException(self.currency, other.currency)
        r = self.minor_units + other.minor_units
        if r < INT64_MIN or r > INT64_MAX:  # Math.addExact: Python ints never overflow, so check by hand
            raise OverflowError("long overflow")
        return Money(r, self.currency)

    def __str__(self) -> str:
        magnitude = abs(self.minor_units)
        scale = self.currency.minor_units_per_major()
        text = str(magnitude // scale)
        if self.currency.exponent > 0:
            text += f".{magnitude % scale:0{self.currency.exponent}d}"
        return f"{'-' if self.minor_units < 0 else ''}{text} {self.currency.code}"


class SimClock:
    """A clock that only moves when the simulation says so."""

    def __init__(self, start_ms: int) -> None:
        self._now_ms = start_ms

    def now(self) -> int:
        return self._now_ms

    def advance(self, ms: int) -> None:
        self._now_ms += ms


class CrashException(RuntimeError):
    """Raised at an injected crash point. The simulation catches it where the real process would die."""

    def __init__(self, where: str) -> None:
        super().__init__(f"crash {where}")


class BrokerUnavailableException(RuntimeError):
    """The broker refused or lost a publish. Transient by assumption: the caller retries."""

    def __init__(self) -> None:
        super().__init__("broker unavailable")


@dataclass(frozen=True)
class Payment:
    """The business row. A payment that was accepted and must be reflected in the ledger."""

    payment_id: str
    merchant: str
    amount: Money
    created_at_ms: int


@dataclass(frozen=True)
class OutboxEvent:
    """One outbox row: seq is commit order, event_id is the consumer's dedupe key, aggregate_id the partition key."""

    seq: int
    event_id: str
    aggregate_id: str
    type: str
    payload: str
    created_at_ms: int
    published: bool

    def mark_published(self) -> "OutboxEvent":
        return replace(self, published=True)

    def __str__(self) -> str:
        return (f"#{self.seq} {self.event_id} {self.type} {self.payload}"
                + (" [published]" if self.published else " [pending]"))


class Store:
    """In-memory store with real transactions: writes are buffered in the Tx and applied only on commit, all or none.

    The payment row and the outbox row live in the same store, so one commit covers both.
    """

    def __init__(self) -> None:
        self._payments: dict[str, Payment] = {}
        self._outbox: dict[int, OutboxEvent] = {}  # insertion-ordered, like LinkedHashMap
        self._next_seq = 1
        self.commits = 0
        self.rollbacks = 0

    class Tx:
        def __init__(self, store: "Store") -> None:
            self._store = store
            self._pending_payments: list[Payment] = []
            self._pending_events: list[OutboxEvent] = []
            self._pending_marks: list[int] = []
            self._open = True

        def _require_open(self) -> None:
            if not self._open:
                raise RuntimeError("transaction already closed")

        def insert_payment(self, p: Payment) -> "Store.Tx":
            self._require_open()
            if p.payment_id in self._store._payments:
                raise RuntimeError(f"duplicate payment {p.payment_id}")
            self._pending_payments.append(p)
            return self

        def insert_outbox(self, event_id: str, aggregate_id: str, type: str, payload: str, at_ms: int) -> "Store.Tx":
            """Buffers an outbox row. The sequence number is assigned at commit so commit order equals seq order."""
            self._require_open()
            self._pending_events.append(OutboxEvent(0, event_id, aggregate_id, type, payload, at_ms, False))
            return self

        def mark_published(self, seq: int) -> "Store.Tx":
            self._require_open()
            self._pending_marks.append(seq)
            return self

        def commit(self) -> None:
            self._require_open()
            self._open = False
            s = self._store
            for p in self._pending_payments:
                s._payments[p.payment_id] = p
            for e in self._pending_events:
                seq = s._next_seq
                s._next_seq += 1
                s._outbox[seq] = replace(e, seq=seq, published=False)
            for seq in self._pending_marks:
                e = s._outbox.get(seq)
                if e is not None:
                    s._outbox[seq] = e.mark_published()
            s.commits += 1

        def rollback(self) -> None:
            self._require_open()
            self._open = False
            self._store.rollbacks += 1

        # Python-only convenience: `with store.begin() as tx:` commits on success, rolls back on an exception.
        def __enter__(self) -> "Store.Tx":
            return self

        def __exit__(self, exc_type, exc, tb) -> bool:
            if self._open:
                if exc_type is None:
                    self.commit()
                else:
                    self.rollback()
            return False

    def begin(self) -> "Store.Tx":
        return Store.Tx(self)

    def payment(self, id: str) -> Payment | None:
        return self._payments.get(id)

    def payment_count(self) -> int:
        return len(self._payments)

    def outbox_rows(self) -> list[OutboxEvent]:
        return list(self._outbox.values())

    def unpublished(self, limit: int) -> list[OutboxEvent]:
        """The relay's query: unpublished rows in commit order, at most limit."""
        out: list[OutboxEvent] = []
        for e in self._outbox.values():
            if not e.published:
                out.append(e)
            if len(out) == limit:
                break
        return out

    def unpublished_count(self) -> int:
        return sum(1 for e in self._outbox.values() if not e.published)


@dataclass(frozen=True)
class Message:
    """A message as the broker holds it. The broker assigns its own offset; the event_id travels inside."""

    offset: int
    event_id: str
    aggregate_id: str
    type: str
    payload: str


class Broker:
    """An in-memory broker topic. Append-only, with a switch to simulate an outage."""

    def __init__(self) -> None:
        self._log: list[Message] = []
        self.down = False
        self.publish_attempts = 0

    def publish(self, event_id: str, aggregate_id: str, type: str, payload: str) -> int:
        self.publish_attempts += 1
        if self.down:
            raise BrokerUnavailableException()
        offset = len(self._log)
        self._log.append(Message(offset, event_id, aggregate_id, type, payload))
        return offset

    def messages(self) -> list[Message]:
        return list(self._log)

    def from_(self, offset: int) -> list[Message]:
        """Java: from(offset); `from` is a Python keyword."""
        return [] if offset >= len(self._log) else list(self._log[offset:])

    def size(self) -> int:
        return len(self._log)


class CrashPoint(Enum):
    """Where a process dies."""

    NONE = "NONE"
    AFTER_COMMIT_BEFORE_PUBLISH = "AFTER_COMMIT_BEFORE_PUBLISH"
    AFTER_PUBLISH_BEFORE_COMMIT = "AFTER_PUBLISH_BEFORE_COMMIT"
    AFTER_PUBLISH_BEFORE_MARK = "AFTER_PUBLISH_BEFORE_MARK"


class Events:
    """Builds the payload once so the dual-write and outbox services emit byte-identical events."""

    PAYMENT_ACCEPTED = "PaymentAccepted"

    @staticmethod
    def event_id_for(p: Payment) -> str:
        return f"evt-{p.payment_id}"

    @staticmethod
    def payload_for(p: Payment) -> str:
        return f"{p.payment_id}|{p.merchant}|{p.amount}"


class DualWritePaymentService:
    """The naive service: write the row, then publish (or the other way round). Two commits, no atomicity."""

    def __init__(self, store: Store, broker: Broker) -> None:
        self._store = store
        self._broker = broker
        self.crash_point = CrashPoint.NONE
        self.publish_first = False

    def accept(self, p: Payment) -> None:
        if self.publish_first:
            self._broker.publish(Events.event_id_for(p), p.merchant, Events.PAYMENT_ACCEPTED, Events.payload_for(p))
            if self.crash_point is CrashPoint.AFTER_PUBLISH_BEFORE_COMMIT:
                raise CrashException("after publish, before commit")
            self._store.begin().insert_payment(p).commit()
        else:
            self._store.begin().insert_payment(p).commit()
            if self.crash_point is CrashPoint.AFTER_COMMIT_BEFORE_PUBLISH:
                raise CrashException("after commit, before publish")
            self._broker.publish(Events.event_id_for(p), p.merchant, Events.PAYMENT_ACCEPTED, Events.payload_for(p))


class OutboxPaymentService:
    """The fixed service: the payment row and the outbox row go into one transaction. The broker is never touched."""

    def __init__(self, store: Store, clock: SimClock) -> None:
        self._store = store
        self._clock = clock

    def accept(self, p: Payment) -> None:
        (self._store.begin()
         .insert_payment(p)
         .insert_outbox(Events.event_id_for(p), p.merchant, Events.PAYMENT_ACCEPTED, Events.payload_for(p),
                        self._clock.now())
         .commit())

    def accept_then_fail(self, p: Payment) -> None:
        """A request that fails validation after the row was staged: rollback must drop the outbox row with it."""
        tx = (self._store.begin()
              .insert_payment(p)
              .insert_outbox(Events.event_id_for(p), p.merchant, Events.PAYMENT_ACCEPTED, Events.payload_for(p),
                             self._clock.now()))
        tx.rollback()


class OutboxRelay:
    """Polling publisher: publish each unpublished row in seq order, then mark it in a second transaction.

    A crash between publish and mark re-publishes on the next poll: at-least-once.
    """

    def __init__(self, store: Store, broker: Broker, batch_size: int) -> None:
        self._store = store
        self._broker = broker
        self.batch_size = batch_size
        self.crash_point = CrashPoint.NONE
        self.published = 0
        self.failed_attempts = 0
        self.polls = 0

    def poll(self) -> int:
        """One poll. Returns how many rows were marked published. Stops at the first broker failure to keep order."""
        self.polls += 1
        marked = 0
        for e in self._store.unpublished(self.batch_size):
            try:
                self._broker.publish(e.event_id, e.aggregate_id, e.type, e.payload)
            except BrokerUnavailableException:
                self.failed_attempts += 1
                return marked  # leave the row pending; retry next poll
            self.published += 1
            if self.crash_point is CrashPoint.AFTER_PUBLISH_BEFORE_MARK:
                self.crash_point = CrashPoint.NONE
                raise CrashException(f"after publish of {e.event_id}, before mark")
            self._store.begin().mark_published(e.seq).commit()
            marked += 1
        return marked

    def drain(self, max_polls: int) -> int:
        """Poll until nothing is pending or the broker refuses. Bounded so a dead broker cannot loop forever."""
        total = 0
        for _ in range(max_polls):
            if self._store.unpublished_count() == 0:
                break
            n = self.poll()
            total += n
            if n == 0:
                break  # broker refused; nothing changes until it recovers
        return total


class LedgerConsumer:
    """The ledger projection: reads the broker from its offset and applies each event_id once."""

    def __init__(self, broker: Broker) -> None:
        self._broker = broker
        self._balances: dict[str, Money] = {}
        self._applied: set[str] = set()
        self._offset = 0
        self.duplicates_ignored = 0

    def consume(self) -> int:
        applied_now = 0
        for m in self._broker.from_(self._offset):
            self._offset = m.offset + 1
            if m.event_id in self._applied:
                self.duplicates_ignored += 1
                continue
            self._applied.add(m.event_id)
            parts = m.payload.split("|")
            amount = LedgerConsumer.parse_money(parts[2])
            prev = self._balances.get(m.aggregate_id)
            self._balances[m.aggregate_id] = amount if prev is None else prev.plus(amount)
            applied_now += 1
        return applied_now

    @staticmethod
    def parse_money(text: str) -> Money:
        p = text.split(" ")
        neg = p[0].startswith("-")
        digits = (p[0][1:] if neg else p[0]).replace(".", "")
        minor = int(digits)
        return Money.of_minor(-minor if neg else minor, Currency.USD)

    def balance(self, merchant: str) -> Money:
        return self._balances.get(merchant, Money.zero(Currency.USD))

    def applied_count(self) -> int:
        return len(self._applied)

    def offset(self) -> int:
        return self._offset


def usd(text: str) -> Money:
    return LedgerConsumer.parse_money(text + " USD")


def payment(id: str, merchant: str, amount: str, clock: SimClock) -> Payment:
    return Payment(id, merchant, usd(amount), clock.now())


def main() -> None:
    clock = SimClock(0)

    print("== 1. Dual write, commit then publish: the broker is down at the wrong moment ==")
    store = Store()
    broker = Broker()
    svc = DualWritePaymentService(store, broker)
    svc.accept(payment("pay-1", "acme", "20.00", clock))
    broker.down = True
    try:
        svc.accept(payment("pay-2", "acme", "35.00", clock))
    except BrokerUnavailableException as e:
        print(f"  pay-2: row committed, then publish failed: {e}")
    broker.down = False
    ledger = LedgerConsumer(broker)
    ledger.consume()
    print(f"  store has {store.payment_count()} payments; broker has {broker.size()} messages")
    print(f"  ledger balance for acme = {ledger.balance('acme')}  "
          "(store says 55.00: pay-2 is an orphan row, the ledger will never see it)")

    print("\n== 2. Dual write, publish then commit: the process dies between the two ==")
    store = Store()
    broker = Broker()
    svc = DualWritePaymentService(store, broker)
    svc.publish_first = True
    svc.crash_point = CrashPoint.AFTER_PUBLISH_BEFORE_COMMIT
    try:
        svc.accept(payment("pay-3", "acme", "12.50", clock))
    except CrashException as e:
        print(f"  pay-3: {e}")
    ledger = LedgerConsumer(broker)
    ledger.consume()
    print(f"  store has {store.payment_count()} payments; ledger balance for acme = {ledger.balance('acme')}"
          "  (a ghost event: the ledger credits a payment that does not exist)")

    print("\n== 3. Outbox: one transaction, two rows; the relay delivers when the broker comes back ==")
    store = Store()
    broker = Broker()
    osvc = OutboxPaymentService(store, clock)
    relay = OutboxRelay(store, broker, 10)
    ledger = LedgerConsumer(broker)
    broker.down = True
    osvc.accept(payment("pay-4", "acme", "20.00", clock))
    clock.advance(5)
    osvc.accept(payment("pay-5", "acme", "35.00", clock))
    print(f"  broker down; store has {store.payment_count()} payments and {store.unpublished_count()} pending outbox rows")
    for e in store.outbox_rows():
        print(f"    {e}")
    n = relay.drain(3)
    print(f"  relay polled {relay.polls} times, delivered {n}, failed attempts {relay.failed_attempts}")
    broker.down = False
    n = relay.drain(3)
    ledger.consume()
    print(f"  broker back; relay delivered {n}; pending now {store.unpublished_count()}")
    print(f"  ledger balance for acme = {ledger.balance('acme')}  (matches the store)")

    print("\n== 4. Relay crashes after publish, before mark: at-least-once, and the consumer dedupes ==")
    store = Store()
    broker = Broker()
    osvc = OutboxPaymentService(store, clock)
    relay = OutboxRelay(store, broker, 10)
    ledger = LedgerConsumer(broker)
    osvc.accept(payment("pay-6", "globex", "100.00", clock))
    osvc.accept(payment("pay-7", "globex", "1.00", clock))
    relay.crash_point = CrashPoint.AFTER_PUBLISH_BEFORE_MARK
    try:
        relay.poll()
    except CrashException as e:
        print(f"  relay: {e}")
    print(f"  broker has {broker.size()} message(s); store still has {store.unpublished_count()} pending rows")
    relay.drain(3)
    print(f"  relay restarted and drained; broker now has {broker.size()} messages for 2 payments")
    for m in broker.messages():
        print(f"    offset {m.offset} {m.event_id}")
    ledger.consume()
    print(f"  ledger applied {ledger.applied_count()}, ignored {ledger.duplicates_ignored} duplicate; "
          f"balance for globex = {ledger.balance('globex')}")

    print("\n== 5. Rollback drops the outbox row with the payment: no event for a payment that never existed ==")
    store = Store()
    osvc = OutboxPaymentService(store, clock)
    osvc.accept_then_fail(payment("pay-8", "acme", "999.00", clock))
    print(f"  payments={store.payment_count()} outbox rows={len(store.outbox_rows())} rollbacks={store.rollbacks}")

    print("\n== 6. Ordering: outbox seq is commit order, and the relay publishes in that order per aggregate ==")
    store = Store()
    broker = Broker()
    osvc = OutboxPaymentService(store, clock)
    relay = OutboxRelay(store, broker, 2)
    for i in range(1, 6):
        osvc.accept(payment(f"pay-{10 + i}", "acme", f"{i}.00", clock))
    polls = 0
    while store.unpublished_count() > 0:
        relay.poll()
        polls += 1
    order = " ".join(m.event_id.replace("evt-pay-", "") for m in broker.messages())
    print(f"  batch size 2, {polls} polls, broker order: {order}")


if __name__ == "__main__":
    main()
