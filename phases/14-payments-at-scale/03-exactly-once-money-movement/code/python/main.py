"""Lesson: phases/14-payments-at-scale/03-exactly-once-money-movement/docs/en.md
Exactly-Once Money Movement End to End
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: Helland, "Idempotence Is Not a Medical Condition" (ACM Queue, 2012); Kleppmann, DDIA ch. 11;
         Richardson, microservices.io "Transactional outbox"; Stripe docs "Idempotent requests".
Python 3.11+, standard library only. Same design as the Java and Go versions. Money is integer minor units.
"""

from __future__ import annotations

import random
from collections import deque
from dataclasses import dataclass, field, replace
from enum import Enum
from typing import Callable

# ---------------------------------------------------------------- Money (Phase 10, Lesson 01) ----


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


@dataclass(frozen=True)
class Money:
    """Exact amount of one currency as a signed count of minor units. Never a float."""

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

    def is_zero(self) -> bool:
        return self.minor_units == 0

    def is_positive(self) -> bool:
        return self.minor_units > 0

    def __str__(self) -> str:
        magnitude = abs(self.minor_units)
        whole, frac = divmod(magnitude, self.currency.minor_units_per_major())
        text = str(whole)
        if self.currency.exponent > 0:
            text += "." + str(frac).rjust(self.currency.exponent, "0")
        return f"{'-' if self.minor_units < 0 else ''}{text} {self.currency.code}"

    __repr__ = __str__


# ---------------------------------------------------------------- Faults ----


class FaultPoint(Enum):
    """Where the harness may crash a component. Each point is a real place a process dies in production."""

    API_BEFORE_COMMIT = "api_before_commit"            # nothing is written
    API_AFTER_COMMIT = "api_after_commit"              # committed, response lost: the client will retry
    RELAY_AFTER_PUBLISH = "relay_after_publish"        # published but row not marked: a duplicate will follow
    BUS_DUPLICATE = "bus_duplicate"                    # the bus delivers the same message twice
    BUS_REORDER = "bus_reorder"                        # the bus delivers a batch out of order
    CONSUMER_BEFORE_COMMIT = "consumer_before_commit"  # nothing written, the message is redelivered
    CONSUMER_AFTER_COMMIT = "consumer_after_commit"    # committed but ack lost: redelivered anyway


class CrashError(RuntimeError):
    def __init__(self, point: FaultPoint) -> None:
        super().__init__(f"crash at {point.name}")
        self.point = point


class FaultInjector:
    """Seeded fault injector: the only source of randomness in the pipeline, so every run is replayable by seed."""

    def __init__(self, seed: int, rates: dict[FaultPoint, float]) -> None:
        self.random = random.Random(seed)
        self.rates = dict(rates)
        self.fired: dict[FaultPoint, int] = {}
        self.armed = True

    @staticmethod
    def none() -> "FaultInjector":
        return FaultInjector(0, {})

    @staticmethod
    def uniform(rate: float) -> dict[FaultPoint, float]:
        """Every fault point fires with the same probability."""
        return {p: rate for p in FaultPoint}

    @staticmethod
    def random_schedule(seed: int) -> dict[FaultPoint, float]:
        """A fault schedule drawn from a seed: each point gets its own rate in [0, 0.25)."""
        r = random.Random(seed ^ 0x5DEECE66D)
        return {p: r.randrange(25) / 100.0 for p in FaultPoint}

    def fire(self, point: FaultPoint) -> bool:
        if not self.armed:
            return False
        rate = self.rates.get(point, 0.0)
        if rate <= 0:
            return False
        hit = self.random.random() < rate
        if hit:
            self.fired[point] = self.fired.get(point, 0) + 1
        return hit

    def disarm(self) -> None:
        """Stop injecting faults so a run can drain to quiescence; counts are kept."""
        self.armed = False

    def count(self, point: FaultPoint) -> int:
        return self.fired.get(point, 0)

    def total_fired(self) -> int:
        return sum(self.fired.values())


# ---------------------------------------------------------------- Defences ----


@dataclass(frozen=True)
class Defences:
    """The three mechanisms that together give exactly-once effects. Each is a flag so the harness can
    remove it and show which invariant breaks. No two of them cover the same seam."""

    api_dedupe: bool = True
    atomic_outbox: bool = True
    consumer_dedupe: bool = True

    @staticmethod
    def all() -> "Defences":
        return Defences()

    def without(self, name: str) -> "Defences":
        if name not in ("api_dedupe", "atomic_outbox", "consumer_dedupe"):
            raise ValueError(f"unknown defence {name}")
        return replace(self, **{name: False})


# ---------------------------------------------------------------- Ledger (both sides) ----


@dataclass(frozen=True)
class Posting:
    """A signed posting: positive debits the account, negative credits it."""

    account: str
    amount: Money


@dataclass(frozen=True)
class JournalEntry:
    entry_id: str
    postings: tuple[Posting, ...]

    @staticmethod
    def transfer(entry_id: str, debit_account: str, credit_account: str, amount: Money) -> "JournalEntry":
        if not amount.is_positive():
            raise ValueError(f"transfer amount must be positive: {amount}")
        return JournalEntry(entry_id, (Posting(debit_account, amount), Posting(credit_account, amount.negate())))

    def balanced(self) -> bool:
        total = Money.zero(self.postings[0].amount.currency)
        for p in self.postings:
            total = total.plus(p.amount)
        return total.is_zero()


class Ledger:
    """Append-only double-entry ledger. It checks that every entry balances; it does NOT check entry-id
    uniqueness, because dedupe is the job of the layer in front of the ledger (see Exercise 1)."""

    def __init__(self, currency: Currency) -> None:
        self.currency = currency
        self.journal: list[JournalEntry] = []
        self._entry_counts: dict[str, int] = {}
        self._balances: dict[str, Money] = {}

    def post(self, entry: JournalEntry) -> None:
        if not entry.balanced():
            raise ValueError(f"unbalanced entry {entry.entry_id}")
        self.journal.append(entry)
        self._entry_counts[entry.entry_id] = self._entry_counts.get(entry.entry_id, 0) + 1
        for p in entry.postings:
            self._balances[p.account] = self._balances.get(p.account, Money.zero(self.currency)).plus(p.amount)

    def balance(self, account: str) -> Money:
        return self._balances.get(account, Money.zero(self.currency))

    def trial_balance(self) -> Money:
        """Debits positive, credits negative: a correct ledger always sums to zero."""
        total = Money.zero(self.currency)
        for m in self._balances.values():
            total = total.plus(m)
        return total

    def credit_total(self, prefix: str) -> Money:
        """Sum of the credit balances of every account starting with prefix, as a positive amount."""
        total = Money.zero(self.currency)
        for account, m in self._balances.items():
            if account.startswith(prefix):
                total = total.plus(m.negate())
        return total

    def count_entries(self, entry_id: str) -> int:
        return self._entry_counts.get(entry_id, 0)

    def balances(self) -> dict[str, Money]:
        return dict(sorted(self._balances.items()))


# ---------------------------------------------------------------- Gateway side ----


@dataclass(frozen=True)
class PaymentRequest:
    idempotency_key: str
    customer_id: str
    merchant_id: str
    amount: Money

    def fingerprint(self) -> str:
        """What must be identical for two requests with the same key to be "the same request"."""
        return f"{self.customer_id}|{self.merchant_id}|{self.amount}"


@dataclass(frozen=True)
class Payment:
    payment_id: str
    idempotency_key: str
    customer_id: str
    merchant_id: str
    amount: Money

    def fingerprint(self) -> str:
        return f"{self.customer_id}|{self.merchant_id}|{self.amount}"


@dataclass(frozen=True)
class PaymentEvent:
    """The event written to the outbox in the same transaction as the payment and later carried by the bus."""

    event_id: str
    payment_id: str
    customer_id: str
    merchant_id: str
    amount: Money

    @staticmethod
    def accepted(p: Payment) -> "PaymentEvent":
        return PaymentEvent(f"evt-{p.payment_id}", p.payment_id, p.customer_id, p.merchant_id, p.amount)


@dataclass(frozen=True)
class OutboxRow:
    event: PaymentEvent
    published: bool


class IdempotencyConflictError(ValueError):
    def __init__(self, key: str) -> None:
        super().__init__(f"idempotency key {key} reused with a different request")


RECEIVABLE = "asset:receivable:"            # + customer_id (gateway books)
GATEWAY_CLEARING = "liability:clearing:acquirer"
DOWNSTREAM_CLEARING = "asset:clearing:acquirer"
PAYABLE = "liability:payable:"              # + merchant_id (downstream books)


class GatewayStore:
    """In-memory stand-in for the gateway's database. The only thing it models faithfully is atomicity:
    transaction() applies every write or none, and a crash is only possible strictly before or after commit."""

    def __init__(self) -> None:
        self.payments: dict[str, Payment] = {}          # payment_id -> payment
        self.idempotency_keys: dict[str, str] = {}      # key -> payment_id (unique index)
        self.outbox: dict[str, OutboxRow] = {}          # event_id -> row, insertion order
        self.ledger = Ledger(USD)                       # the gateway's own books
        self.commits = 0

    def transaction(self, faults: FaultInjector, before: FaultPoint | None, after: FaultPoint | None,
                    writes: Callable[[], None]) -> None:
        if before is not None and faults.fire(before):
            raise CrashError(before)
        writes()
        self.commits += 1
        if after is not None and faults.fire(after):
            raise CrashError(after)

    def unpublished(self) -> list[PaymentEvent]:
        return [row.event for row in self.outbox.values() if not row.published]


@dataclass(frozen=True)
class Response:
    payment: Payment
    replayed: bool


class PaymentApi:
    """The idempotent API handler: look up the key, else write payment + key + ledger entry + outbox row in one transaction."""

    def __init__(self, store: GatewayStore, faults: FaultInjector, defences: Defences) -> None:
        self.store = store
        self.faults = faults
        self.defences = defences
        self._sequence = 0

    def create_payment(self, request: PaymentRequest) -> Response:
        store = self.store
        if self.defences.api_dedupe:
            existing_id = store.idempotency_keys.get(request.idempotency_key)
            if existing_id is not None:
                existing = store.payments[existing_id]
                if existing.fingerprint() != request.fingerprint():
                    raise IdempotencyConflictError(request.idempotency_key)
                return Response(existing, True)
        self._sequence += 1
        payment_id = f"pay-{self._sequence:06d}"
        payment = Payment(payment_id, request.idempotency_key, request.customer_id, request.merchant_id, request.amount)
        event = PaymentEvent.accepted(payment)
        entry = JournalEntry.transfer(payment_id, RECEIVABLE + request.customer_id, GATEWAY_CLEARING, request.amount)

        def record() -> None:
            store.payments[payment_id] = payment
            store.idempotency_keys[request.idempotency_key] = payment_id
            store.ledger.post(entry)

        def enqueue() -> None:
            store.outbox[event.event_id] = OutboxRow(event, False)

        if self.defences.atomic_outbox:
            def both() -> None:
                record()
                enqueue()
            store.transaction(self.faults, FaultPoint.API_BEFORE_COMMIT, FaultPoint.API_AFTER_COMMIT, both)
        else:
            # The dual-write anti-pattern: record, then enqueue in a second transaction. A crash between them
            # (API_AFTER_COMMIT) leaves a payment that no downstream system will ever hear about.
            store.transaction(self.faults, FaultPoint.API_BEFORE_COMMIT, FaultPoint.API_AFTER_COMMIT, record)
            store.transaction(self.faults, None, None, enqueue)
        return Response(payment, False)


class OutboxRelay:
    """Publishes unpublished outbox rows to the bus. At-least-once: a crash between publish and mark republishes."""

    def __init__(self, store: GatewayStore, bus: "Bus", faults: FaultInjector) -> None:
        self.store = store
        self.bus = bus
        self.faults = faults

    def relay_once(self) -> int:
        published = 0
        for event in self.store.unpublished():
            self.bus.publish(event)
            published += 1
            if self.faults.fire(FaultPoint.RELAY_AFTER_PUBLISH):
                raise CrashError(FaultPoint.RELAY_AFTER_PUBLISH)

            def mark(event: PaymentEvent = event) -> None:
                self.store.outbox[event.event_id] = OutboxRow(event, True)

            self.store.transaction(self.faults, None, None, mark)
        return published


# ---------------------------------------------------------------- Bus ----


class Bus:
    """In-memory at-least-once bus: may duplicate on publish, may reorder a batch, redelivers on consumer crash."""

    def __init__(self, faults: FaultInjector) -> None:
        self.faults = faults
        self._queue: deque[PaymentEvent] = deque()
        self.published = self.duplicated = self.delivered = self.redelivered = self.reordered_batches = 0

    def publish(self, event: PaymentEvent) -> None:
        self._queue.append(event)
        self.published += 1
        if self.faults.fire(FaultPoint.BUS_DUPLICATE):
            self._queue.append(event)
            self.duplicated += 1

    def deliver(self, handler: Callable[[PaymentEvent], None], batch_size: int) -> int:
        batch: list[PaymentEvent] = []
        while self._queue and len(batch) < batch_size:
            batch.append(self._queue.popleft())
        if len(batch) > 1 and self.faults.fire(FaultPoint.BUS_REORDER):
            self.faults.random.shuffle(batch)
            self.reordered_batches += 1
        ok = 0
        for i, event in enumerate(batch):
            try:
                handler(event)
                self.delivered += 1
                ok += 1
            except CrashError:
                # nothing from this message on was acknowledged: put the rest back, in order, for redelivery
                for j in range(len(batch) - 1, i - 1, -1):
                    self._queue.appendleft(batch[j])
                self.redelivered += 1
                raise
        return ok

    def pending(self) -> int:
        return len(self._queue)


# ---------------------------------------------------------------- Downstream ledger side ----


class LedgerStore:
    """The consumer's database: processed-message inbox and the downstream ledger, written in one transaction."""

    def __init__(self) -> None:
        self.inbox: set[str] = set()
        self.ledger = Ledger(USD)
        self.commits = 0

    def transaction(self, faults: FaultInjector, before: FaultPoint | None, after: FaultPoint | None,
                    writes: Callable[[], None]) -> None:
        if before is not None and faults.fire(before):
            raise CrashError(before)
        writes()
        self.commits += 1
        if after is not None and faults.fire(after):
            raise CrashError(after)


class LedgerConsumer:
    """Dedupes by event id (inbox pattern), then posts to the downstream ledger with entry_id == event_id."""

    def __init__(self, store: LedgerStore, faults: FaultInjector, defences: Defences) -> None:
        self.store = store
        self.faults = faults
        self.defences = defences
        self.skipped_duplicates = 0

    def handle(self, event: PaymentEvent) -> None:
        if self.defences.consumer_dedupe and event.event_id in self.store.inbox:
            self.skipped_duplicates += 1
            return
        entry = JournalEntry.transfer(event.event_id, DOWNSTREAM_CLEARING, PAYABLE + event.merchant_id, event.amount)

        def writes() -> None:
            self.store.inbox.add(event.event_id)
            self.store.ledger.post(entry)

        self.store.transaction(self.faults, FaultPoint.CONSUMER_BEFORE_COMMIT, FaultPoint.CONSUMER_AFTER_COMMIT, writes)


# ---------------------------------------------------------------- Harness ----


@dataclass(frozen=True)
class HarnessConfig:
    seed: int
    operations: int
    rates: dict[FaultPoint, float]
    defences: Defences

    @staticmethod
    def standard(seed: int, operations: int) -> "HarnessConfig":
        return HarnessConfig(seed, operations, FaultInjector.uniform(0.08), Defences.all())

    @staticmethod
    def random_schedule(seed: int, operations: int) -> "HarnessConfig":
        return HarnessConfig(seed, operations, FaultInjector.random_schedule(seed), Defences.all())

    def without(self, defence: str) -> "HarnessConfig":
        return replace(self, defences=self.defences.without(defence))


@dataclass(frozen=True)
class HarnessReport:
    seed: int
    client_requests: int
    api_calls: int
    crashes: int
    accepted_payments: int
    bus_published: int
    bus_duplicated: int
    bus_redelivered: int
    gateway_entries: int
    downstream_entries: int
    consumer_skipped: int
    gateway_trial_balance: Money
    downstream_trial_balance: Money
    violations: list[str] = field(default_factory=list)

    def passed(self) -> bool:
        return not self.violations

    def first_broken_invariant(self) -> str:
        """The invariant named in the first violation, e.g. "one-payment-per-key". Empty when the run passed."""
        if not self.violations:
            return ""
        v = self.violations[0]
        return v[1:v.index("]")]

    def summary(self) -> str:
        return (f"seed={self.seed} requests={self.client_requests} apiCalls={self.api_calls} crashes={self.crashes}"
                f" accepted={self.accepted_payments} published={self.bus_published} dup={self.bus_duplicated}"
                f" redelivered={self.bus_redelivered} gatewayEntries={self.gateway_entries}"
                f" downstreamEntries={self.downstream_entries} skipped={self.consumer_skipped}"
                f" trial={self.gateway_trial_balance}/{self.downstream_trial_balance} violations={len(self.violations)}")


MAX_CLIENT_ATTEMPTS = 12


def _step(action: Callable[[], object]) -> int:
    try:
        action()
        return 0
    except CrashError:
        return 1


def run_harness(cfg: HarnessConfig) -> HarnessReport:
    """Drive thousands of operations through the pipeline under injected faults and check the invariants."""
    faults = FaultInjector(cfg.seed, cfg.rates)
    workload = random.Random(cfg.seed * 1_000_003 + 17)
    gateway = GatewayStore()
    api = PaymentApi(gateway, faults, cfg.defences)
    bus = Bus(faults)
    relay = OutboxRelay(gateway, bus, faults)
    downstream = LedgerStore()
    consumer = LedgerConsumer(downstream, faults, cfg.defences)

    issued: list[PaymentRequest] = []            # every distinct request a client ever made
    acknowledged: dict[str, Payment] = {}        # key -> payment the client saw
    api_calls = crashes = client_requests = 0

    for i in range(cfg.operations):
        if issued and workload.randrange(100) < 15:
            request = issued[workload.randrange(len(issued))]   # a client re-sends an old key (retry storm)
        else:
            amount = Money.of_minor(100 + workload.randrange(20_000), USD)
            request = PaymentRequest(f"key-{len(issued)}", f"cust-{workload.randrange(50)}",
                                     f"merch-{workload.randrange(8)}", amount)
            issued.append(request)
        client_requests += 1
        for _attempt in range(MAX_CLIENT_ATTEMPTS):
            api_calls += 1
            try:
                acknowledged[request.idempotency_key] = api.create_payment(request).payment
                break
            except CrashError:
                crashes += 1   # the client saw a timeout and retries with the same key
        if i % 3 == 0:
            crashes += _step(relay.relay_once)
        if i % 5 == 0:
            crashes += _step(lambda: bus.deliver(consumer.handle, 8))

    faults.disarm()
    while gateway.unpublished() or bus.pending() > 0:
        relay.relay_once()
        bus.deliver(consumer.handle, 64)

    violations = check_invariants(gateway, downstream.ledger, issued, acknowledged)
    return HarnessReport(cfg.seed, client_requests, api_calls, crashes, len(gateway.payments), bus.published,
                         bus.duplicated, bus.redelivered, len(gateway.ledger.journal), len(downstream.ledger.journal),
                         consumer.skipped_duplicates, gateway.ledger.trial_balance(), downstream.ledger.trial_balance(),
                         violations)


def check_invariants(gateway: GatewayStore, downstream: Ledger, issued: list[PaymentRequest],
                     acknowledged: dict[str, Payment]) -> list[str]:
    """Each violation is "[invariant-name] detail" so a failing test can say which layer is missing."""
    violations: list[str] = []
    books = gateway.ledger

    # 1. One payment per idempotency key, and the payment the client was told about is the stored one.
    per_key: dict[str, int] = {}
    for p in gateway.payments.values():
        per_key[p.idempotency_key] = per_key.get(p.idempotency_key, 0) + 1
    for key, n in sorted(per_key.items()):
        if n != 1:
            violations.append(f"[one-payment-per-key] {key} has {n} payments")
    for key, payment in acknowledged.items():
        stored = gateway.idempotency_keys.get(key)
        if payment.payment_id != stored:
            violations.append(f"[one-payment-per-key] {key} acknowledged {payment.payment_id} but store has {stored}")

    # 2. Atomicity of record + enqueue: a payment never exists without its outbox row.
    for p in gateway.payments.values():
        if f"evt-{p.payment_id}" not in gateway.outbox:
            violations.append(f"[payment-has-outbox-row] {p.payment_id} has no outbox row")

    # 3. Every accepted payment is posted exactly once in each ledger; nothing else is posted.
    for p in gateway.payments.values():
        g = books.count_entries(p.payment_id)
        if g != 1:
            violations.append(f"[posted-once-gateway] {p.payment_id} posted {g} times")
        d = downstream.count_entries(f"evt-{p.payment_id}")
        if d != 1:
            violations.append(f"[posted-once-downstream] {p.payment_id} posted {d} times downstream")
    if len(books.journal) != len(gateway.payments):
        violations.append(f"[posted-once-gateway] gatewayEntries={len(books.journal)} payments={len(gateway.payments)}")
    if len(downstream.journal) != len(gateway.payments):
        violations.append(f"[posted-once-downstream] downstreamEntries={len(downstream.journal)} payments={len(gateway.payments)}")

    # 4. Money is conserved: both trial balances are zero and the clearing amounts agree with the accepted total.
    if not books.trial_balance().is_zero():
        violations.append(f"[trial-balance] gateway {books.trial_balance()}")
    if not downstream.trial_balance().is_zero():
        violations.append(f"[trial-balance] downstream {downstream.trial_balance()}")
    accepted = Money.zero(USD)
    for p in gateway.payments.values():
        accepted = accepted.plus(p.amount)
    gateway_clearing = books.credit_total(GATEWAY_CLEARING)
    payables = downstream.credit_total(PAYABLE)
    if gateway_clearing != accepted:
        violations.append(f"[money-conserved] gateway clearing {gateway_clearing} != accepted {accepted}")
    if payables != accepted:
        violations.append(f"[money-conserved] merchant payables {payables} != accepted {accepted}")

    # 5. Per customer, the receivable equals the sum of that customer's distinct acknowledged requests.
    intended: dict[str, Money] = {}
    for r in issued:
        if r.idempotency_key in acknowledged:
            intended[r.customer_id] = intended.get(r.customer_id, Money.zero(USD)).plus(r.amount)
    for customer, amount in sorted(intended.items()):
        receivable = books.balance(RECEIVABLE + customer)
        if receivable != amount:
            violations.append(f"[charged-what-was-asked] {customer} intended {amount} receivable {receivable}")
    return violations


# ---------------------------------------------------------------- Demo ----


def main() -> None:
    print("== One payment, a crash at every seam, exactly one posting in each ledger ==")
    faults = FaultInjector(7, {FaultPoint.API_AFTER_COMMIT: 1.0})
    gateway = GatewayStore()
    api = PaymentApi(gateway, faults, Defences.all())
    request = PaymentRequest("key-1", "cust-7", "merch-2", Money.of_minor(1999, USD))
    try:
        api.create_payment(request)
    except CrashError as c:
        print(f"attempt 1: {c}  (payment + ledger entry + outbox row committed, client saw a timeout)")
    faults.disarm()
    retry = api.create_payment(request)
    print(f"attempt 2: replayed={retry.replayed} paymentId={retry.payment.payment_id}  payments={len(gateway.payments)}"
          f" gatewayEntries={len(gateway.ledger.journal)} outboxRows={len(gateway.outbox)}")

    faults = FaultInjector(7, {FaultPoint.RELAY_AFTER_PUBLISH: 1.0})
    bus = Bus(faults)
    relay = OutboxRelay(gateway, bus, faults)
    try:
        relay.relay_once()
    except CrashError as c:
        print(f"relay 1: {c}  (event on the bus, row still unpublished)")
    faults.disarm()
    relay.relay_once()
    print(f"relay 2: bus now holds {bus.pending()} copies of evt-{retry.payment.payment_id}")

    downstream = LedgerStore()
    consumer = LedgerConsumer(downstream, FaultInjector.none(), Defences.all())
    bus.deliver(consumer.handle, 10)
    print(f"consumer: delivered={bus.delivered} skippedDuplicates={consumer.skipped_duplicates}"
          f" downstreamEntries={len(downstream.ledger.journal)} trialBalance={downstream.ledger.trial_balance()}")
    print(f"  gateway:    {RECEIVABLE}cust-7 = {gateway.ledger.balance(RECEIVABLE + 'cust-7')},"
          f" {GATEWAY_CLEARING} = {gateway.ledger.balance(GATEWAY_CLEARING)}")
    print(f"  downstream: {DOWNSTREAM_CLEARING} = {downstream.ledger.balance(DOWNSTREAM_CLEARING)},"
          f" {PAYABLE}merch-2 = {downstream.ledger.balance(PAYABLE + 'merch-2')}")

    print("\n== Seeded fault-injection harness: 3000 operations, 8% fault rate at every seam ==")
    for seed in (1, 2, 3):
        report = run_harness(HarnessConfig.standard(seed, 3000))
        print(("PASS " if report.passed() else "FAIL ") + report.summary())

    print("\n== Remove one defence at a time: the harness names the invariant that breaks ==")
    for defence in ("api_dedupe", "atomic_outbox", "consumer_dedupe"):
        broken = run_harness(HarnessConfig.standard(1, 3000).without(defence))
        outcome = "(still holds)" if broken.passed() else broken.violations[0]
        print(f"{'PASS' if broken.passed() else 'FAIL'} without {defence:<15} -> {outcome}")


if __name__ == "__main__":
    main()
