"""Lesson: phases/14-payments-at-scale/10-simulation-testing-of-payment-systems/docs/en.md
Deterministic Simulation Testing of Payment Systems
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: Steele, Lea, Flood, "Fast Splittable Pseudorandom Number Generators" (OOPSLA 2014) for SplitMix64;
FoundationDB "Simulation and Testing" docs; TigerBeetle VOPR design notes; FIPS 180-4 (SHA-256) for the trace fingerprint.
Python 3.11+, standard library only. Money is integer minor units (Phase 10, Lesson 01). No wall clock, no threads.
SplitMix64 and integer-only probabilities mean this port replays a seed to the same trace and fingerprint as the Java.
"""

from __future__ import annotations

import hashlib
import heapq
from dataclasses import dataclass, field
from enum import Enum
from typing import Callable, Optional

MASK64 = 0xFFFFFFFFFFFFFFFF


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


class CurrencyMismatchError(ValueError):
    def __init__(self, a: Currency, b: Currency) -> None:
        super().__init__(f"cannot combine {a.code} with {b.code}")


@dataclass(frozen=True)
class Money:
    """Exact amount of one currency as a signed count of minor units. Never a float."""

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


# ---------------------------------------------------------------- Deterministic primitives ----


class Rng:
    """SplitMix64. Six lines, trivially portable, so a seed produces the identical event sequence in every language.
    Python integers never wrap, so every addition and multiply is masked to 64 bits; the state is kept unsigned,
    which makes >> the unsigned shift that Java spells >>>."""

    def __init__(self, seed: int) -> None:
        self.state = seed & MASK64
        self.draws = 0

    def next_long(self) -> int:
        self.draws += 1
        self.state = (self.state + 0x9E3779B97F4A7C15) & MASK64
        z = self.state
        z = ((z ^ (z >> 30)) * 0xBF58476D1CE4E5B9) & MASK64
        z = ((z ^ (z >> 27)) * 0x94D049BB133111EB) & MASK64
        return z ^ (z >> 31)

    def next_int(self, bound: int) -> int:
        """Uniform in [0, bound). Uses the top 32 bits so the result does not depend on signed-modulo semantics."""
        if bound <= 0:
            raise ValueError("bound must be positive")
        return (self.next_long() >> 32) % bound

    def next_long_bounded(self, bound: int) -> int:
        """Uniform in [0, bound) for bounds beyond 32 bits (fault start times over long horizons). Unsigned modulo."""
        if bound <= 0:
            raise ValueError("bound must be positive")
        return self.next_long() % bound

    def between(self, lo: int, hi: int) -> int:
        """Uniform in [lo, hi]."""
        return lo + self.next_int(hi - lo + 1)

    def chance(self, basis_points: int) -> bool:
        """True with probability basis_points / 10000. Integer probabilities: no float compare, no platform rounding."""
        return self.next_int(10_000) < basis_points

    def split(self) -> "Rng":
        """A statistically independent child stream; used so adding a component never shifts another's draws."""
        return Rng(self.next_long())


class Fingerprint:
    """SHA-256 over the processed-event trace, one line per event ("time label\\n"). Two runs with the same fingerprint
    executed the same events in the same order; the Java version produces the same fingerprint for the same seed."""

    def __init__(self) -> None:
        self.digest = hashlib.sha256()
        self.count = 0

    def add(self, line: str) -> None:
        self.digest.update(line.encode("utf-8"))
        self.digest.update(b"\n")
        self.count += 1

    def hex(self) -> str:
        """First 16 hex digits of the digest: enough to compare runs, short enough to paste into a bug report."""
        return self.digest.copy().hexdigest()[:16]


@dataclass(frozen=True)
class Event:
    time: int
    seq: int
    label: str
    action: Callable[[], None]


class Simulator:
    """The virtual clock and the event loop are the same object: time is whatever the next event says it is.
    Events are ordered by (time, seq) so two events at the same instant run in the order they were scheduled.
    Nothing in the lesson reads time.time() or sleeps."""

    def __init__(self) -> None:
        self._queue: list = []  # heap of (time, seq, Event)
        self._seq = 0
        self.now = 0
        self.processed = 0
        self.trace = Fingerprint()

    def schedule(self, delay_ms: int, label: str, action: Callable[[], None]) -> None:
        if delay_ms < 0:
            raise ValueError("delay must be non-negative")
        e = Event(self.now + delay_ms, self._seq, label, action)
        self._seq += 1
        heapq.heappush(self._queue, (e.time, e.seq, e))

    def step(self) -> bool:
        if not self._queue:
            return False
        _, _, e = heapq.heappop(self._queue)
        self.now = e.time
        self.processed += 1
        self.trace.add(f"{self.now} {e.label}")
        e.action()
        return True

    def run_until(self, time_ms: int) -> None:
        while self._queue and self._queue[0][0] <= time_ms:
            self.step()
        if self.now < time_ms:
            self.now = time_ms

    def run_until_idle(self, max_events: int) -> None:
        """Drain every remaining event; the guard turns an accidental infinite loop into a test failure."""
        budget = max_events
        while self.step():
            budget -= 1
            if budget < 0:
                raise RuntimeError("simulation did not reach quiescence")

    def pending(self) -> int:
        return len(self._queue)


@dataclass(frozen=True)
class NetworkConfig:
    min_latency_ms: int
    max_latency_ms: int
    drop_basis_points: int


NetworkConfig.LAN = NetworkConfig(20, 200, 100)  # 1% loss


class Network:
    """Simulated network: every message takes a seeded latency, may be dropped, and is blocked while its link is partitioned."""

    def __init__(self, sim: Simulator, rng: Rng, cfg: NetworkConfig) -> None:
        self.sim = sim
        self.rng = rng
        self.cfg = cfg
        self.partitioned: set[str] = set()
        self.sent = self.delivered = self.dropped = self.blocked = 0

    def send(self, frm: str, to: str, label: str, deliver: Callable[[], None]) -> None:
        self.sent += 1
        if f"{frm}>{to}" in self.partitioned:
            self.blocked += 1
            return
        if self.rng.chance(self.cfg.drop_basis_points):
            self.dropped += 1
            return
        latency = self.rng.between(self.cfg.min_latency_ms, self.cfg.max_latency_ms)

        def delivered() -> None:
            self.delivered += 1
            deliver()

        self.sim.schedule(latency, f"deliver {to}<-{frm} {label}", delivered)

    def partition(self, a: str, b: str) -> None:
        self.partitioned.add(f"{a}>{b}")
        self.partitioned.add(f"{b}>{a}")

    def heal(self, a: str, b: str) -> None:
        self.partitioned.discard(f"{a}>{b}")
        self.partitioned.discard(f"{b}>{a}")

    def is_partitioned(self, a: str, b: str) -> bool:
        return f"{a}>{b}" in self.partitioned


# ---------------------------------------------------------------- Faults ----


class FaultKind(Enum):
    PARTITION_CLIENT_GATEWAY = 0
    PARTITION_GATEWAY_PROCESSOR = 1
    PROCESSOR_OUTAGE = 2
    PROCESSOR_SLOW = 3
    GATEWAY_CRASH = 4

    def __str__(self) -> str:
        return self.name


FAULT_KINDS = list(FaultKind)
MINUTE = 60_000


@dataclass(frozen=True)
class Fault:
    start_ms: int
    end_ms: int
    kind: FaultKind

    @staticmethod
    def draw(rng: Rng, horizon_ms: int) -> "Fault":
        """Durations are kind-specific and drawn from the fault stream, so one fault more or less never shifts the workload."""
        kind = FAULT_KINDS[rng.next_int(len(FAULT_KINDS))]
        start = rng.next_long_bounded(horizon_ms)
        if kind in (FaultKind.PARTITION_CLIENT_GATEWAY, FaultKind.PARTITION_GATEWAY_PROCESSOR):
            duration = rng.between(5, 120) * 1000
        elif kind is FaultKind.PROCESSOR_OUTAGE:
            duration = rng.between(5, 90) * 1000
        elif kind is FaultKind.PROCESSOR_SLOW:
            duration = rng.between(1, 10) * MINUTE
        else:
            duration = 0
        return Fault(start, min(horizon_ms, start + duration), kind)


class FaultSchedule:
    """A list of faults with start and end times. Armed onto the simulator before the run begins."""

    def __init__(self) -> None:
        self.faults: list[Fault] = []

    @staticmethod
    def generate(rng: Rng, horizon_ms: int, count: int) -> "FaultSchedule":
        s = FaultSchedule()
        for _ in range(count):
            s.faults.append(Fault.draw(rng, horizon_ms))
        s.faults.sort(key=lambda f: (f.start_ms, f.kind.value))
        return s

    def arm(self, sim: Simulator, net: Network, processor: "Processor", gateway: "Gateway") -> None:
        for f in self.faults:
            sim.schedule(f.start_ms, f"fault-begin {f.kind}", lambda f=f: FaultSchedule._begin(f, net, processor, gateway))
            if f.end_ms > f.start_ms:
                sim.schedule(f.end_ms, f"fault-end {f.kind}", lambda f=f: FaultSchedule._end(f, net, processor))

    @staticmethod
    def _begin(f: Fault, net: Network, processor: "Processor", gateway: "Gateway") -> None:
        if f.kind is FaultKind.PARTITION_CLIENT_GATEWAY:
            for i in range(8):
                net.partition(f"client-{i}", "gateway")
        elif f.kind is FaultKind.PARTITION_GATEWAY_PROCESSOR:
            net.partition("gateway", "processor")
        elif f.kind is FaultKind.PROCESSOR_OUTAGE:
            processor.down = True
        elif f.kind is FaultKind.PROCESSOR_SLOW:
            processor.extra_latency_ms = 5_000
        else:
            gateway.crash_when_busy()

    @staticmethod
    def _end(f: Fault, net: Network, processor: "Processor") -> None:
        if f.kind is FaultKind.PARTITION_CLIENT_GATEWAY:
            for i in range(8):
                net.heal(f"client-{i}", "gateway")
        elif f.kind is FaultKind.PARTITION_GATEWAY_PROCESSOR:
            net.heal("gateway", "processor")
        elif f.kind is FaultKind.PROCESSOR_OUTAGE:
            processor.down = False
        elif f.kind is FaultKind.PROCESSOR_SLOW:
            processor.extra_latency_ms = 0


class Defect(Enum):
    """Defects the harness can plant in the gateway so the learner can watch the invariant checkers catch them."""

    NONE = 0
    FRESH_AUTH_KEY_ON_RETRY = 1  # the gateway mints a new processor idempotency key on every retry
    RECOVERY_SKIPS_PENDING = 2   # after a crash the gateway does not re-drive payments that were in flight

    def __str__(self) -> str:
        return self.name


# ---------------------------------------------------------------- The mini payment system ----


class Status(Enum):
    PENDING = 0
    APPROVED = 1
    DECLINED = 2

    def __str__(self) -> str:
        return self.name


@dataclass(frozen=True)
class PaymentRequest:
    key: str
    customer_id: str
    amount: Money


@dataclass(frozen=True)
class AuthRequest:
    auth_key: str
    payment_id: str
    customer_id: str
    amount: Money
    attempt: int


@dataclass(frozen=True)
class AuthResponse:
    auth_key: str
    payment_id: str
    approved: bool


@dataclass(frozen=True)
class Posting:
    account: str
    amount: Money


@dataclass(frozen=True)
class JournalEntry:
    entry_id: str
    postings: tuple

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
    """Append-only double-entry ledger with a unique entry id (Phase 10, Lesson 03)."""

    def __init__(self, currency: Currency) -> None:
        self.journal: list[JournalEntry] = []
        self._entry_counts: dict[str, int] = {}
        self._balances: dict[str, Money] = {}
        self.currency = currency

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
        total = Money.zero(self.currency)
        for m in self._balances.values():
            total = total.plus(m)
        return total

    def debit_total(self, prefix: str) -> Money:
        """Sum of the debit balances of every account with the prefix."""
        total = Money.zero(self.currency)
        for account, m in self._balances.items():
            if account.startswith(prefix):
                total = total.plus(m)
        return total

    def count_entries(self, entry_id: str) -> int:
        return self._entry_counts.get(entry_id, 0)

    def balances(self) -> dict:
        return {k: self._balances[k] for k in sorted(self._balances)}


class Processor:
    """The card processor: idempotent on auth_key, decides approve/decline from its own seeded stream, can be down or slow."""

    NAME = "processor"

    def __init__(self, sim: Simulator, net: Network, rng: Rng, decline_basis_points: int) -> None:
        self.sim = sim
        self.net = net
        self.rng = rng
        self.decline_basis_points = decline_basis_points
        self.decisions: dict[str, AuthResponse] = {}   # auth_key -> decision (durable)
        self.approvals: list[AuthResponse] = []        # every approval ever granted, in order
        self.down = False
        self.extra_latency_ms = 0
        self.received = self.replayed = self.swallowed = 0

    def receive(self, req: AuthRequest, gateway: "Gateway") -> None:
        self.received += 1
        if self.down:
            self.swallowed += 1  # an outage looks like a timeout to the caller
            return
        decision = self.decisions.get(req.auth_key)
        if decision is not None:
            self.replayed += 1
        else:
            approved = not self.rng.chance(self.decline_basis_points)
            decision = AuthResponse(req.auth_key, req.payment_id, approved)
            self.decisions[req.auth_key] = decision
            if approved:
                self.approvals.append(decision)
        reply = decision
        self.sim.schedule(self.extra_latency_ms, f"processor-respond {req.payment_id}",
                          lambda: self.net.send(Processor.NAME, Gateway.NAME, f"auth-response {reply.payment_id}",
                                                lambda: gateway.on_auth_response(reply)))

    def approvals_per_payment(self) -> dict:
        """Distinct payment ids that were approved, with how many times each was approved (sorted by id)."""
        m: dict[str, int] = {}
        for a in self.approvals:
            m[a.payment_id] = m.get(a.payment_id, 0) + 1
        return {k: m[k] for k in sorted(m)}


class Payment:
    def __init__(self, payment_id: str, key: str, customer_id: str, amount: Money) -> None:
        self.payment_id = payment_id
        self.key = key
        self.customer_id = customer_id
        self.amount = amount
        self.status = Status.PENDING
        self.auth_key = f"auth-{payment_id}"
        self.attempts = 0


class Gateway:
    """The gateway. Durable state (payments, idempotency keys, ledger) survives a crash; the in-memory table of
    clients waiting for a reply does not, and neither do timers armed before the crash (checked via epoch)."""

    NAME = "gateway"
    RECEIVABLE = "asset:receivable:"
    CLEARING = "liability:clearing:processor"

    def __init__(self, sim: Simulator, net: Network, processor: Processor, defect: Defect,
                 processor_timeout_ms: int, recovery_delay_ms: int) -> None:
        self.sim = sim
        self.net = net
        self.processor = processor
        self.defect = defect
        self.processor_timeout_ms = processor_timeout_ms
        self.recovery_delay_ms = recovery_delay_ms
        # durable
        self.idempotency_keys: dict[str, str] = {}  # key -> payment_id
        self.payments: dict[str, Payment] = {}
        self.ledger = Ledger(USD)
        # volatile
        self._waiting: dict[str, list] = {}
        self._epoch = 0
        self._crash_armed = False
        self._sequence = 0  # durable: never reused after a crash
        self.crashes = self.auth_sends = self.auth_timeouts = self.replays = self.recovered = 0

    def handle(self, req: PaymentRequest, reply: Callable[[Status], None]) -> None:
        """Handle a client request; `reply` is how the response gets back to the client (over the network)."""
        existing_id = self.idempotency_keys.get(req.key)
        if existing_id is not None:
            self.replays += 1
            p = self.payments[existing_id]
            if p.status is not Status.PENDING:
                reply(p.status)
                return
            self._waiting.setdefault(p.payment_id, []).append(lambda: reply(p.status))
            return
        self._sequence += 1
        payment_id = f"pay-{self._sequence:06d}"
        p = Payment(payment_id, req.key, req.customer_id, req.amount)
        self.payments[payment_id] = p  # one transaction: payment + key
        self.idempotency_keys[req.key] = payment_id
        self._waiting.setdefault(payment_id, []).append(lambda: reply(p.status))
        self._send_auth(p)
        if self._crash_armed:
            self.crash()  # the armed crash fires in the worst window: durable, in flight, unanswered

    def _send_auth(self, p: Payment) -> None:
        p.attempts += 1
        if self.defect is Defect.FRESH_AUTH_KEY_ON_RETRY and p.attempts > 1:
            p.auth_key = f"auth-{p.payment_id}-{p.attempts}"
        self.auth_sends += 1
        req = AuthRequest(p.auth_key, p.payment_id, p.customer_id, p.amount, p.attempts)
        self.net.send(Gateway.NAME, Processor.NAME, f"auth-request {p.payment_id} a{p.attempts}",
                      lambda: self.processor.receive(req, self))
        armed_epoch, armed_attempt = self._epoch, p.attempts

        def timeout() -> None:
            if armed_epoch != self._epoch or p.status is not Status.PENDING or p.attempts != armed_attempt:
                return
            self.auth_timeouts += 1
            self._send_auth(p)  # same auth_key: the processor will dedupe

        self.sim.schedule(self.processor_timeout_ms, f"auth-timeout {p.payment_id} a{armed_attempt}", timeout)

    def on_auth_response(self, r: AuthResponse) -> None:
        p = self.payments.get(r.payment_id)
        if p is None or p.status is not Status.PENDING:
            return  # late duplicate: already resolved
        if r.approved:
            p.status = Status.APPROVED  # one transaction: status + ledger entry
            self.ledger.post(JournalEntry.transfer(p.payment_id, Gateway.RECEIVABLE + p.customer_id, Gateway.CLEARING, p.amount))
        else:
            p.status = Status.DECLINED
        for reply in self._waiting.pop(p.payment_id, []):
            reply()

    def crash_when_busy(self) -> None:
        """A crash at 3 a.m. with nothing in flight tests nothing. The fault schedule arms the crash; it fires immediately
        if a payment is pending, otherwise the next time one is accepted."""
        if self.pending_count() > 0:
            self.crash()
        else:
            self._crash_armed = True

    def crash(self) -> None:
        """Lose everything in memory; durable state stays. Schedule the recovery sweep that re-drives in-flight payments."""
        self.crashes += 1
        self._epoch += 1
        self._crash_armed = False
        self._waiting = {}
        if self.defect is Defect.RECOVERY_SKIPS_PENDING:
            return

        def recover() -> None:
            for p in list(self.payments.values()):
                if p.status is Status.PENDING:
                    self.recovered += 1
                    self._send_auth(p)

        self.sim.schedule(self.recovery_delay_ms, "gateway-recover", recover)

    def pending_count(self) -> int:
        return sum(1 for p in self.payments.values() if p.status is Status.PENDING)


@dataclass(frozen=True)
class ClientConfig:
    timeout_ms: int
    max_attempts: int
    backoff_ms: int


ClientConfig.DEFAULT = ClientConfig(8_000, 6, 2_000)


class Client:
    """A client that retries on timeout with the same idempotency key and records the final status it believes."""

    def __init__(self, name: str, sim: Simulator, net: Network, gateway: Gateway, cfg: ClientConfig) -> None:
        self.name = name
        self.sim = sim
        self.net = net
        self.gateway = gateway
        self.cfg = cfg
        self.outcomes: dict[str, Status] = {}  # key -> status the client believes (absent = unknown)
        self.unknown: set[str] = set()
        self.disagreements: list[str] = []
        self.requests = self.retries = 0

    def pay(self, req: PaymentRequest) -> None:
        self.requests += 1
        self._attempt(req, 1)

    def _attempt(self, req: PaymentRequest, n: int) -> None:
        answered = [False]

        def on_reply(status: Status) -> None:
            def record() -> None:
                answered[0] = True
                previous = self.outcomes.get(req.key)
                if previous is None:
                    self.outcomes[req.key] = status
                elif previous is not status:
                    self.disagreements.append(f"{req.key} saw {previous} then {status}")

            self.net.send(Gateway.NAME, self.name, f"payment-reply {req.key} {status}", record)

        self.net.send(self.name, Gateway.NAME, f"payment {req.key} a{n}", lambda: self.gateway.handle(req, on_reply))

        def timeout() -> None:
            if answered[0] or req.key in self.outcomes:
                return
            if n >= self.cfg.max_attempts:
                self.unknown.add(req.key)
                return
            self.retries += 1
            self.sim.schedule(self.cfg.backoff_ms * n, f"client-retry {req.key} a{n + 1}", lambda: self._attempt(req, n + 1))

        self.sim.schedule(self.cfg.timeout_ms, f"client-timeout {req.key} a{n}", timeout)


# ---------------------------------------------------------------- Harness ----


@dataclass(frozen=True)
class SimConfig:
    seed: int
    virtual_hours: int
    clients: int
    mean_inter_arrival_ms: int
    faults: int
    defect: Defect

    @staticmethod
    def standard(seed: int, virtual_hours: int) -> "SimConfig":
        """Four clients, one payment per client every ten virtual minutes on average, one fault every four virtual hours."""
        return SimConfig(seed, virtual_hours, 4, 10 * 60_000, max(1, virtual_hours // 4), Defect.NONE)

    def with_defect(self, d: Defect) -> "SimConfig":
        return SimConfig(self.seed, self.virtual_hours, self.clients, self.mean_inter_arrival_ms, self.faults, d)

    def with_seed(self, s: int) -> "SimConfig":
        return SimConfig(s, self.virtual_hours, self.clients, self.mean_inter_arrival_ms, self.faults, self.defect)

    def horizon_ms(self) -> int:
        return self.virtual_hours * 3_600_000


@dataclass(frozen=True)
class SimReport:
    seed: int
    virtual_hours: int
    events: int
    payments: int
    approved: int
    declined: int
    pending: int
    unknown_at_client: int
    client_retries: int
    auth_timeouts: int
    net_dropped: int
    net_blocked: int
    crashes: int
    faults: int
    trial_balance: Money
    first_violation_ms: int
    violations: tuple
    fingerprint: str

    def passed(self) -> bool:
        return not self.violations

    def first_broken_invariant(self) -> str:
        if not self.violations:
            return ""
        v = self.violations[0]
        return v[1:v.index("]")]

    def summary(self) -> str:
        return (f"seed={self.seed} hours={self.virtual_hours} events={self.events} payments={self.payments} approved={self.approved}"
                f" declined={self.declined} pending={self.pending} unknown={self.unknown_at_client} retries={self.client_retries}"
                f" authTimeouts={self.auth_timeouts} dropped={self.net_dropped} blocked={self.net_blocked} crashes={self.crashes}"
                f" faults={self.faults} trial={self.trial_balance} violations={len(self.violations)} fp={self.fingerprint}")

    def reproduction(self) -> str:
        """What you paste into the bug report: enough to replay the exact run."""
        text = f"reproduce with: Harness.run(SimConfig.standard({self.seed}, {self.virtual_hours}))"
        if self.violations:
            text += f"; first violation at t={self.first_violation_ms}ms: {self.violations[0]}"
        return text


class Harness:
    """Wires the system, runs it for the configured virtual horizon, checks the safety invariants once per virtual day and
    the quiescence invariants after the drain. Checks are O(payments), so checking every event would be quadratic."""

    HOUR = 3_600_000
    CHECK_INTERVAL = 24 * HOUR

    @staticmethod
    def run(cfg: SimConfig) -> SimReport:
        sim = Simulator()
        master = Rng(cfg.seed)
        net_rng, processor_rng, fault_rng, workload_rng = master.split(), master.split(), master.split(), master.split()
        net = Network(sim, net_rng, NetworkConfig.LAN)
        processor = Processor(sim, net, processor_rng, 500)  # 5% declines
        gateway = Gateway(sim, net, processor, cfg.defect, 3_000, 2_000)
        clients = [Client(f"client-{i}", sim, net, gateway, ClientConfig.DEFAULT) for i in range(cfg.clients)]
        schedule = FaultSchedule.generate(fault_rng, cfg.horizon_ms(), cfg.faults)
        schedule.arm(sim, net, processor, gateway)

        # Workload: each client's next payment is drawn from the workload stream; the stream never sees faults.
        issued = [0]
        for c in clients:
            Harness._schedule_next_payment(sim, c, workload_rng, cfg, issued)

        violations: list[str] = []
        first_violation = [-1]

        def check() -> None:
            if violations:
                return
            violations.extend(Harness.check_safety(gateway, processor, clients))
            if violations:
                first_violation[0] = sim.now

        t = Harness.CHECK_INTERVAL
        while t <= cfg.horizon_ms():
            sim.schedule(t, "invariant-check", check)
            t += Harness.CHECK_INTERVAL
        sim.run_until(cfg.horizon_ms())
        sim.run_until_idle(5_000_000)  # drain: no new payments, faults all ended

        if not violations:
            violations.extend(Harness.check_safety(gateway, processor, clients))
            violations.extend(Harness.check_quiescent(gateway, processor))
            if violations:
                first_violation[0] = sim.now
        approved = sum(1 for p in gateway.payments.values() if p.status is Status.APPROVED)
        declined = sum(1 for p in gateway.payments.values() if p.status is Status.DECLINED)
        unknown = sum(len(c.unknown) for c in clients)
        retries = sum(c.retries for c in clients)
        return SimReport(cfg.seed, cfg.virtual_hours, sim.processed, len(gateway.payments), approved, declined, gateway.pending_count(),
                         unknown, retries, gateway.auth_timeouts, net.dropped, net.blocked, gateway.crashes, len(schedule.faults),
                         gateway.ledger.trial_balance(), first_violation[0], tuple(violations), sim.trace.hex())

    @staticmethod
    def _schedule_next_payment(sim: Simulator, c: Client, rng: Rng, cfg: SimConfig, issued: list) -> None:
        gap = rng.between(cfg.mean_inter_arrival_ms // 2, cfg.mean_inter_arrival_ms * 3 // 2)

        def pay() -> None:
            if sim.now >= cfg.horizon_ms():
                return  # the horizon has passed: stop generating load
            key = f"key-{issued[0]}"
            issued[0] += 1
            amount = Money.of_minor(100 + rng.next_int(50_000), USD)
            c.pay(PaymentRequest(key, f"cust-{rng.next_int(200)}", amount))
            Harness._schedule_next_payment(sim, c, rng, cfg, issued)

        sim.schedule(gap, f"client-pay {c.name}", pay)

    @staticmethod
    def check_safety(gateway: Gateway, processor: Processor, clients: list) -> list:
        """Safety: true at every instant, so checked during the run. Each violation is "[invariant] detail".
        Note what is NOT here: "every approval is posted". An approval whose response is still on the wire is not a bug,
        so that property is only checkable at quiescence (check_quiescent)."""
        v: list[str] = []
        ledger = gateway.ledger
        approvals = processor.approvals_per_payment()

        for payment_id, n in approvals.items():
            if n != 1:
                v.append(f"[no-double-authorization] {payment_id} approved {n} times by the processor")
        for payment_id in approvals:
            n = ledger.count_entries(payment_id)
            if n > 1:
                v.append(f"[approval-posted-once] {payment_id} posted {n} times")
        for e in ledger.journal:
            if e.entry_id not in approvals:
                v.append(f"[approval-posted-once] {e.entry_id} posted without a processor approval")
        approved_at_gateway = Money.zero(USD)
        for p in gateway.payments.values():
            if p.status is Status.APPROVED:
                approved_at_gateway = approved_at_gateway.plus(p.amount)
                if p.payment_id not in approvals:
                    v.append(f"[approval-posted-once] {p.payment_id} marked APPROVED without an approval")
                if ledger.count_entries(p.payment_id) != 1:
                    v.append(f"[approval-posted-once] {p.payment_id} APPROVED but posted {ledger.count_entries(p.payment_id)} times")
        if not ledger.trial_balance().is_zero():
            v.append(f"[trial-balance] {ledger.trial_balance()}")
        receivables = ledger.debit_total(Gateway.RECEIVABLE)
        if receivables != approved_at_gateway:
            v.append(f"[money-conserved] receivables {receivables} != approved at gateway {approved_at_gateway}")
        per_key: dict[str, int] = {}
        for p in gateway.payments.values():
            per_key[p.key] = per_key.get(p.key, 0) + 1
        for key in sorted(per_key):
            if per_key[key] != 1:
                v.append(f"[one-payment-per-key] {key} has {per_key[key]} payments")
        for c in clients:
            for d in c.disagreements:
                v.append(f"[client-gateway-agree] {c.name} {d}")
            for key, status in c.outcomes.items():
                p = gateway.payments.get(gateway.idempotency_keys.get(key, ""))
                if p is None or p.status is not status:
                    v.append(f"[client-gateway-agree] {c.name} believes {key} is {status} but gateway has "
                             f"{'nothing' if p is None else p.status}")
        return v

    @staticmethod
    def check_quiescent(gateway: Gateway, processor: Processor) -> list:
        """Quiescence: true only once every message has been delivered or dropped and every fault has ended."""
        v: list[str] = []
        for p in gateway.payments.values():
            if p.status is Status.PENDING:
                v.append(f"[no-stuck-payments] {p.payment_id} still PENDING after quiescence ({p.attempts} auth attempts)")
        for payment_id in processor.approvals_per_payment():
            n = gateway.ledger.count_entries(payment_id)
            if n != 1:
                v.append(f"[approval-posted-once] {payment_id} approved by the processor but posted {n} times")
        approved_at_processor = Money.zero(USD)
        for a in processor.approvals:
            approved_at_processor = approved_at_processor.plus(gateway.payments[a.payment_id].amount)
        receivables = gateway.ledger.debit_total(Gateway.RECEIVABLE)
        if receivables != approved_at_processor:
            v.append(f"[money-conserved] receivables {receivables} != approved at processor {approved_at_processor}")
        return v

    @staticmethod
    def find_failing_seed(cfg: SimConfig, first_seed: int, max_seeds: int) -> Optional[SimReport]:
        """Try seeds in order until one fails; the report carries everything needed to replay it."""
        for seed in range(first_seed, first_seed + max_seeds):
            r = Harness.run(cfg.with_seed(seed))
            if not r.passed():
                return r
        return None


# ---------------------------------------------------------------- Demo ----


def main() -> None:
    print("== SplitMix64: the same seed is the same universe, on every machine and every run ==")
    rng = Rng(0)
    print(f"seed 0 first draw = {rng.next_long():016x} (reference value e220a8397b1dcdaf)")

    print("\n== Same seed twice: identical event trace ==")
    a = Harness.run(SimConfig.standard(1, 48))
    b = Harness.run(SimConfig.standard(1, 48))
    print(a.summary())
    print(f"fingerprints equal: {str(a.fingerprint == b.fingerprint).lower()}")

    print("\n== 480 virtual hours (20 days) of payments under injected faults, seed 2 ==")
    r = Harness.run(SimConfig.standard(2, 480))
    print(("PASS " if r.passed() else "FAIL ") + r.summary())

    print("\n== Four more seeds, 120 virtual hours each ==")
    for seed in range(3, 7):
        s = Harness.run(SimConfig.standard(seed, 120))
        print(("PASS " if s.passed() else "FAIL ") + s.summary())

    print("\n== Plant a defect; the harness finds a failing seed and prints how to replay it ==")
    for d in (Defect.FRESH_AUTH_KEY_ON_RETRY, Defect.RECOVERY_SKIPS_PENDING):
        failing = Harness.find_failing_seed(SimConfig.standard(1, 24).with_defect(d), 1, 40)
        if failing is None:
            print(f"{d}: no failing seed in 40 tries")
            continue
        print(f"{d} -> seed {failing.seed} breaks [{failing.first_broken_invariant()}]")
        print(f"  {failing.violations[0]}")
        print(f"  {failing.reproduction()}")


if __name__ == "__main__":
    main()
