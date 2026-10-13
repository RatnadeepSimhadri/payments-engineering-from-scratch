"""Lesson: phases/12-payment-systems-design/02-payment-state-machine/docs/en.md
The Payment State Machine
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: Stripe API reference (PaymentIntent status lifecycle); EMV 3-D Secure protocol spec (challenge flow);
         Kung & Robinson, "On Optimistic Methods for Concurrency Control" (1981).
Python 3.11+, standard library only. Same design as the Java and Go versions.
"""

from __future__ import annotations

from collections import deque
from dataclasses import dataclass, field
from enum import Enum
from typing import Protocol


# ---------------------------------------------------------------------------------------------
# Money: integer minor units + ISO 4217 currency (Phase 10, Lesson 01). Copied, not imported.
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

    def _require_same_currency(self, other: "Money") -> None:
        if self.currency != other.currency:
            raise CurrencyMismatchError(self.currency, other.currency)

    def plus(self, other: "Money") -> "Money":
        self._require_same_currency(other)
        return Money(self.minor_units + other.minor_units, self.currency)

    def minus(self, other: "Money") -> "Money":
        self._require_same_currency(other)
        return Money(self.minor_units - other.minor_units, self.currency)

    def is_positive(self) -> bool:
        return self.minor_units > 0

    def is_zero(self) -> bool:
        return self.minor_units == 0

    def compare_to(self, other: "Money") -> int:
        self._require_same_currency(other)
        return (self.minor_units > other.minor_units) - (self.minor_units < other.minor_units)

    def __str__(self) -> str:
        magnitude = abs(self.minor_units)
        whole, frac = divmod(magnitude, self.currency.minor_units_per_major())
        text = str(whole)
        if self.currency.exponent > 0:
            text += "." + str(frac).rjust(self.currency.exponent, "0")
        return f"{'-' if self.minor_units < 0 else ''}{text} {self.currency.code}"

    __repr__ = __str__


# ---------------------------------------------------------------------------------------------
# Time is injected: deterministic log timestamps, and callbacks that arrive when the clock says so.
# ---------------------------------------------------------------------------------------------


class Clock(Protocol):
    def now_millis(self) -> int: ...


class FakeClock:
    def __init__(self, start_millis: int) -> None:
        self._now = start_millis

    def now_millis(self) -> int:
        return self._now

    def advance(self, millis: int) -> None:
        self._now += millis


# ---------------------------------------------------------------------------------------------
# States and events.
# ---------------------------------------------------------------------------------------------


class PaymentState(Enum):
    CREATED = "created"
    REQUIRES_ACTION = "requires_action"
    PROCESSING = "processing"
    SUCCEEDED = "succeeded"
    REFUNDING = "refunding"
    REFUNDED = "refunded"
    FAILED = "failed"
    CANCELED = "canceled"

    def is_terminal(self) -> bool:
        return self in (PaymentState.REFUNDED, PaymentState.FAILED, PaymentState.CANCELED)


class EventKind(Enum):
    CONFIRM = "confirm"
    REQUIRE_ACTION = "require_action"
    ACTION_COMPLETED = "action_completed"
    PROCESSOR_APPROVED = "processor_approved"
    PROCESSOR_DECLINED = "processor_declined"
    CANCEL = "cancel"
    REFUND_REQUESTED = "refund_requested"
    REFUND_SUCCEEDED = "refund_succeeded"
    REFUND_FAILED = "refund_failed"


@dataclass(frozen=True)
class PaymentEvent:
    """Carries an id so a redelivered callback is recognised; an amount only for refund requests."""

    event_id: str
    kind: EventKind
    amount: Money | None = None
    detail: str = ""

    def __post_init__(self) -> None:
        if self.kind is EventKind.REFUND_REQUESTED and self.amount is None:
            raise ValueError("REFUND_REQUESTED needs an amount")
        if self.kind is not EventKind.REFUND_REQUESTED and self.amount is not None:
            raise ValueError(f"{self.kind.name} does not carry an amount")

    @staticmethod
    def of(event_id: str, kind: EventKind, detail: str = "") -> "PaymentEvent":
        return PaymentEvent(event_id, kind, None, detail)

    @staticmethod
    def refund_requested(event_id: str, amount: Money) -> "PaymentEvent":
        return PaymentEvent(event_id, EventKind.REFUND_REQUESTED, amount)


# ---------------------------------------------------------------------------------------------
# The transition table: data, not code.
# ---------------------------------------------------------------------------------------------


class Guard(Enum):
    ALWAYS = "always"
    REFUND_WITHIN_BALANCE = "refund_within_balance"
    REFUND_LEAVES_BALANCE = "refund_leaves_balance"
    REFUND_CLEARS_BALANCE = "refund_clears_balance"


@dataclass(frozen=True)
class Rule:
    from_state: PaymentState
    kind: EventKind
    guard: Guard
    to_state: PaymentState


class IllegalTransitionError(RuntimeError):
    pass


RULES: tuple[Rule, ...] = (
    Rule(PaymentState.CREATED, EventKind.CONFIRM, Guard.ALWAYS, PaymentState.PROCESSING),
    Rule(PaymentState.CREATED, EventKind.CANCEL, Guard.ALWAYS, PaymentState.CANCELED),
    Rule(PaymentState.PROCESSING, EventKind.REQUIRE_ACTION, Guard.ALWAYS, PaymentState.REQUIRES_ACTION),
    Rule(PaymentState.PROCESSING, EventKind.PROCESSOR_APPROVED, Guard.ALWAYS, PaymentState.SUCCEEDED),
    Rule(PaymentState.PROCESSING, EventKind.PROCESSOR_DECLINED, Guard.ALWAYS, PaymentState.FAILED),
    Rule(PaymentState.REQUIRES_ACTION, EventKind.ACTION_COMPLETED, Guard.ALWAYS, PaymentState.PROCESSING),
    Rule(PaymentState.REQUIRES_ACTION, EventKind.CANCEL, Guard.ALWAYS, PaymentState.CANCELED),
    Rule(PaymentState.SUCCEEDED, EventKind.REFUND_REQUESTED, Guard.REFUND_WITHIN_BALANCE, PaymentState.REFUNDING),
    Rule(PaymentState.REFUNDING, EventKind.REFUND_SUCCEEDED, Guard.REFUND_LEAVES_BALANCE, PaymentState.SUCCEEDED),
    Rule(PaymentState.REFUNDING, EventKind.REFUND_SUCCEEDED, Guard.REFUND_CLEARS_BALANCE, PaymentState.REFUNDED),
    Rule(PaymentState.REFUNDING, EventKind.REFUND_FAILED, Guard.ALWAYS, PaymentState.SUCCEEDED),
)


class TransitionTable:
    RULES = RULES

    @staticmethod
    def has_row(from_state: PaymentState, kind: EventKind) -> bool:
        return any(r.from_state is from_state and r.kind is kind for r in RULES)

    @staticmethod
    def holds(guard: Guard, p: "Payment", e: PaymentEvent) -> bool:
        if guard is Guard.ALWAYS:
            return True
        if guard is Guard.REFUND_WITHIN_BALANCE:
            assert e.amount is not None
            return e.amount.is_positive() and p.refunded.plus(e.amount).compare_to(p.amount) <= 0
        settled = p.refunded.plus(p.pending_refund)
        if guard is Guard.REFUND_LEAVES_BALANCE:
            return settled.compare_to(p.amount) < 0
        return settled == p.amount  # REFUND_CLEARS_BALANCE

    @staticmethod
    def resolve(p: "Payment", event: PaymentEvent) -> Rule:
        if p.state.is_terminal():
            raise IllegalTransitionError(f"{p.id} is {p.state.name} (terminal); {event.kind.name} refused")
        candidates = [r for r in RULES if r.from_state is p.state and r.kind is event.kind]
        if not candidates:
            raise IllegalTransitionError(f"{p.id}: {event.kind.name} is not legal from {p.state.name}")
        for r in candidates:
            if TransitionTable.holds(r.guard, p, event):
                return r
        guard = candidates[0].guard
        if guard is Guard.REFUND_WITHIN_BALANCE and event.amount is not None and not event.amount.is_positive():
            why = "refund amount must be positive"
        elif guard is Guard.REFUND_WITHIN_BALANCE:
            why = f"refund {event.amount} exceeds refundable {p.refundable()}"
        else:
            why = "balance guard failed"
        raise IllegalTransitionError(
            f"{p.id}: {event.kind.name} from {p.state.name} refused by guard {guard.name} ({why})"
        )


# ---------------------------------------------------------------------------------------------
# The persisted transition and the aggregate.
# ---------------------------------------------------------------------------------------------


@dataclass(frozen=True)
class Transition:
    payment_id: str
    version: int
    event_id: str
    kind: EventKind
    from_state: PaymentState
    to_state: PaymentState
    amount: Money | None
    reason: str
    at_millis: int

    def __str__(self) -> str:
        return f"v{self.version:<2} {self.kind.name:<20} {self.from_state.name:<16} -> {self.to_state.name:<16} {self.reason}"


class ConcurrencyConflictError(RuntimeError):
    pass


class ApplyOutcome(Enum):
    APPLIED = "applied"
    DUPLICATE = "duplicate"


class Payment:
    def __init__(self, id: str, amount: Money, clock: Clock) -> None:
        if not amount.is_positive():
            raise ValueError(f"amount must be positive: {amount}")
        self.id = id
        self.amount = amount
        self._clock = clock
        self.state = PaymentState.CREATED
        self.refunded = Money.zero(amount.currency)
        self.pending_refund = Money.zero(amount.currency)
        self.version = 0
        self._history: list[Transition] = []
        self._uncommitted: list[Transition] = []
        self._applied_event_ids: set[str] = set()

    def refundable(self) -> Money:
        return self.amount.minus(self.refunded).minus(self.pending_refund)

    def history(self) -> tuple[Transition, ...]:
        return tuple(self._history)

    def uncommitted(self) -> tuple[Transition, ...]:
        return tuple(self._uncommitted)

    def can_apply(self, event: PaymentEvent) -> bool:
        if event.event_id in self._applied_event_ids:
            return False
        try:
            TransitionTable.resolve(self, event)
            return True
        except IllegalTransitionError:
            return False

    def apply(self, event: PaymentEvent, reason: str) -> ApplyOutcome:
        """Exactly one of: state moves and one transition is recorded; DUPLICATE and nothing changes;
        IllegalTransitionError and nothing changes."""
        if event.event_id in self._applied_event_ids:
            return ApplyOutcome.DUPLICATE
        rule = TransitionTable.resolve(self, event)  # raises before any mutation
        t = Transition(self.id, self.version + 1, event.event_id, event.kind, self.state, rule.to_state,
                       event.amount, reason, self._clock.now_millis())
        self._mutate(t)
        self._uncommitted.append(t)
        return ApplyOutcome.APPLIED

    def _mutate(self, t: Transition) -> None:
        """The single place state changes; shared by apply() and replay() so they agree by construction."""
        if t.kind is EventKind.REFUND_REQUESTED:
            assert t.amount is not None
            self.pending_refund = self.pending_refund.plus(t.amount)
        elif t.kind is EventKind.REFUND_SUCCEEDED:
            self.refunded = self.refunded.plus(self.pending_refund)
            self.pending_refund = Money.zero(self.amount.currency)
        elif t.kind is EventKind.REFUND_FAILED:
            self.pending_refund = Money.zero(self.amount.currency)
        self.state = t.to_state
        self.version = t.version
        self._applied_event_ids.add(t.event_id)
        self._history.append(t)

    def mark_committed(self) -> None:
        self._uncommitted.clear()

    @staticmethod
    def replay(id: str, amount: Money, transitions: list[Transition] | tuple[Transition, ...], clock: Clock) -> "Payment":
        """Rebuild from persisted history. A version gap or a mismatched from_state is corruption."""
        p = Payment(id, amount, clock)
        for t in transitions:
            if t.version != p.version + 1:
                raise RuntimeError(f"{id}: history gap, expected v{p.version + 1} got v{t.version}")
            if t.from_state is not p.state:
                raise RuntimeError(f"{id}: v{t.version} claims from_state {t.from_state.name} but payment is {p.state.name}")
            p._mutate(t)
        return p


# ---------------------------------------------------------------------------------------------
# The store: append-only log per payment, guarded by a version check.
# ---------------------------------------------------------------------------------------------


class PaymentStore:
    def __init__(self, clock: Clock) -> None:
        self._clock = clock
        self._created: dict[str, tuple[Money, int]] = {}
        self._log: dict[str, list[Transition]] = {}

    def create(self, id: str, amount: Money) -> Payment:
        if id in self._created:
            raise RuntimeError(f"{id} already exists")
        self._created[id] = (amount, self._clock.now_millis())
        self._log[id] = []
        return Payment(id, amount, self._clock)

    def history(self, id: str) -> tuple[Transition, ...]:
        if id not in self._log:
            raise RuntimeError(f"unknown payment {id}")
        return tuple(self._log[id])

    def current_version(self, id: str) -> int:
        return len(self.history(id))

    def load(self, id: str) -> Payment:
        if id not in self._created:
            raise RuntimeError(f"unknown payment {id}")
        amount, _ = self._created[id]
        return Payment.replay(id, amount, self._log[id], self._clock)

    def save(self, p: Payment) -> None:
        """Append uncommitted transitions; each must be exactly current_version + 1. All-or-nothing."""
        if p.id not in self._log:
            raise RuntimeError(f"unknown payment {p.id}")
        target = self._log[p.id]
        expected = len(target)
        for t in p.uncommitted():
            if t.version != expected + 1:
                raise ConcurrencyConflictError(
                    f"{p.id}: expected to write v{t.version} but store is at v{len(target)}; reload and retry"
                )
            expected += 1
        target.extend(p.uncommitted())
        p.mark_committed()


# ---------------------------------------------------------------------------------------------
# The processor adapter and the clock-driven fake.
# ---------------------------------------------------------------------------------------------


class ProcessorOutcome(Enum):
    APPROVED = "approved"
    DECLINED = "declined"
    CHALLENGE = "challenge"
    REFUND_OK = "refund_ok"
    REFUND_FAILED = "refund_failed"


@dataclass(frozen=True)
class ProcessorCallback:
    reference: str
    payment_id: str
    outcome: ProcessorOutcome
    detail: str
    due_at_millis: int


class ProcessorAdapter(Protocol):
    def authorize(self, payment_id: str, amount: Money) -> str: ...
    def refund(self, payment_id: str, amount: Money) -> str: ...
    def due_callbacks(self) -> list[ProcessorCallback]: ...


class FakeProcessor:
    def __init__(self, clock: Clock, latency_millis: int) -> None:
        self._clock = clock
        self._latency = latency_millis
        self._script: dict[str, deque[ProcessorOutcome]] = {}
        self._pending: list[ProcessorCallback] = []
        self.calls: list[str] = []
        self._sequence = 0

    def script(self, payment_id: str, *outcomes: ProcessorOutcome) -> "FakeProcessor":
        self._script.setdefault(payment_id, deque()).extend(outcomes)
        return self

    def authorize(self, payment_id: str, amount: Money) -> str:
        return self._schedule(payment_id, amount, "authorize", ProcessorOutcome.APPROVED)

    def refund(self, payment_id: str, amount: Money) -> str:
        return self._schedule(payment_id, amount, "refund", ProcessorOutcome.REFUND_OK)

    def _schedule(self, payment_id: str, amount: Money, op: str, fallback: ProcessorOutcome) -> str:
        q = self._script.get(payment_id)
        outcome = q.popleft() if q else fallback
        self._sequence += 1
        reference = f"pr_{self._sequence:04d}"
        self.calls.append(f"{op} {payment_id} {amount} -> {reference}")
        detail = "51 insufficient funds" if outcome is ProcessorOutcome.DECLINED else outcome.value
        self._pending.append(ProcessorCallback(reference, payment_id, outcome, detail, self._clock.now_millis() + self._latency))
        return reference

    def due_callbacks(self) -> list[ProcessorCallback]:
        now = self._clock.now_millis()
        due = [cb for cb in self._pending if cb.due_at_millis <= now]
        self._pending = [cb for cb in self._pending if cb.due_at_millis > now]
        return due


# ---------------------------------------------------------------------------------------------
# The service: load, decide, save; then talk to the processor.
# ---------------------------------------------------------------------------------------------

_OUTCOME_TO_KIND = {
    ProcessorOutcome.APPROVED: EventKind.PROCESSOR_APPROVED,
    ProcessorOutcome.DECLINED: EventKind.PROCESSOR_DECLINED,
    ProcessorOutcome.CHALLENGE: EventKind.REQUIRE_ACTION,
    ProcessorOutcome.REFUND_OK: EventKind.REFUND_SUCCEEDED,
    ProcessorOutcome.REFUND_FAILED: EventKind.REFUND_FAILED,
}


class PaymentService:
    def __init__(self, store: PaymentStore, processor: ProcessorAdapter) -> None:
        self._store = store
        self._processor = processor

    def create(self, id: str, amount: Money) -> Payment:
        return self._store.create(id, amount)

    def confirm(self, id: str, event_id: str) -> Payment:
        p = self._store.load(id)
        p.apply(PaymentEvent.of(event_id, EventKind.CONFIRM), "merchant confirmed")
        self._store.save(p)  # persist PROCESSING before calling out
        self._processor.authorize(id, p.amount)
        return p

    def complete_action(self, id: str, event_id: str) -> Payment:
        p = self._store.load(id)
        p.apply(PaymentEvent.of(event_id, EventKind.ACTION_COMPLETED), "customer completed challenge")
        self._store.save(p)
        self._processor.authorize(id, p.amount)
        return p

    def cancel(self, id: str, event_id: str) -> Payment:
        p = self._store.load(id)
        p.apply(PaymentEvent.of(event_id, EventKind.CANCEL), "merchant canceled")
        self._store.save(p)
        return p

    def request_refund(self, id: str, event_id: str, amount: Money) -> Payment:
        p = self._store.load(id)
        p.apply(PaymentEvent.refund_requested(event_id, amount), "refund requested")
        self._store.save(p)
        self._processor.refund(id, amount)
        return p

    def pump(self) -> int:
        """Drain due callbacks into events. The reference is the event id, so redelivery is a no-op."""
        applied = 0
        for cb in self._processor.due_callbacks():
            if self.deliver(cb) is ApplyOutcome.APPLIED:
                applied += 1
        return applied

    def deliver(self, cb: ProcessorCallback) -> ApplyOutcome:
        p = self._store.load(cb.payment_id)
        outcome = p.apply(PaymentEvent.of(cb.reference, _OUTCOME_TO_KIND[cb.outcome], cb.detail), f"processor: {cb.detail}")
        if outcome is ApplyOutcome.APPLIED:
            self._store.save(p)
        return outcome


# ---------------------------------------------------------------------------------------------
# Demo
# ---------------------------------------------------------------------------------------------


def main() -> None:
    clock = FakeClock(1_700_000_000_000)
    store = PaymentStore(clock)
    processor = FakeProcessor(clock, 800)
    service = PaymentService(store, processor)

    print("== The transition table ==")
    for r in RULES:
        print(f"{r.from_state.name:<16} + {r.kind.name:<19} [{r.guard.name:<22}] -> {r.to_state.name}")

    print("\n== A 3-D Secure payment: created -> processing -> requires_action -> processing -> succeeded ==")
    processor.script("pay_1001", ProcessorOutcome.CHALLENGE, ProcessorOutcome.APPROVED)
    service.create("pay_1001", Money.of_minor(4999, USD))
    service.confirm("pay_1001", "evt_confirm_1")
    print(f"after confirm:   {store.load('pay_1001').state.name} v{store.current_version('pay_1001')}")
    print(f"pump before latency elapsed -> {service.pump()} callbacks applied")
    clock.advance(800)
    print(f"pump at +800ms              -> {service.pump()} callbacks applied; state {store.load('pay_1001').state.name}")
    clock.advance(8_400)
    service.complete_action("pay_1001", "evt_action_1")
    clock.advance(800)
    service.pump()
    p = store.load("pay_1001")
    print(f"after approval:  {p.state.name} v{p.version}")

    print("\n== The processor redelivers the approval: recognised by event id, not applied twice ==")
    again = ProcessorCallback("pr_0002", "pay_1001", ProcessorOutcome.APPROVED, "approved", clock.now_millis())
    print(f"deliver(pr_0002 again) -> {service.deliver(again).name}, version still {store.current_version('pay_1001')}")

    print("\n== Refunds: SUCCEEDED -> REFUNDING -> SUCCEEDED (partial) or REFUNDED (full) ==")
    clock.advance(86_400_000)
    service.request_refund("pay_1001", "evt_refund_1", Money.of_minor(1500, USD))
    p = store.load("pay_1001")
    print(f"after request:   {p.state.name}, refundable = {p.refundable()} (15.00 pending at the processor)")
    try:
        service.request_refund("pay_1001", "evt_refund_2", Money.of_minor(100, USD))
    except IllegalTransitionError as exc:
        print(f"refused: {exc}")
    clock.advance(800)
    service.pump()
    p = store.load("pay_1001")
    print(f"after settle:    {p.state.name}, refunded = {p.refunded}, refundable = {p.refundable()}")
    try:
        service.request_refund("pay_1001", "evt_refund_3", Money.of_minor(4000, USD))
    except IllegalTransitionError as exc:
        print(f"refused: {exc}")
    service.request_refund("pay_1001", "evt_refund_4", Money.of_minor(3499, USD))
    clock.advance(800)
    service.pump()
    p = store.load("pay_1001")
    print(f"after full:      {p.state.name} (terminal: {p.state.is_terminal()}), refunded = {p.refunded}")

    store.create("pay_2002", Money.of_minor(2500, USD))
    v = store.current_version("pay_2002")
    print(f"\n== Optimistic concurrency: two handlers load pay_2002 at v{v}; only one may write v{v + 1} ==")
    handler_a = store.load("pay_2002")
    handler_b = store.load("pay_2002")
    handler_a.apply(PaymentEvent.of("evt_a", EventKind.CONFIRM), "handler A: confirm")
    handler_b.apply(PaymentEvent.of("evt_b", EventKind.CANCEL), "handler B: cancel")
    store.save(handler_a)
    print("handler A saved CONFIRM as v1")
    try:
        store.save(handler_b)
    except ConcurrencyConflictError as exc:
        print(f"handler B refused: {exc}")
    print(f"stored state is {store.load('pay_2002').state.name}; B reloads and finds CANCEL is now illegal: "
          f"can_apply={store.load('pay_2002').can_apply(PaymentEvent.of('evt_b2', EventKind.CANCEL))}")

    print("\n== Illegal transitions are refused before any mutation ==")
    fresh = store.create("pay_3003", Money.of_minor(100, USD))
    for k in (EventKind.PROCESSOR_APPROVED, EventKind.REFUND_SUCCEEDED, EventKind.ACTION_COMPLETED):
        try:
            fresh.apply(PaymentEvent.of(f"evt_x_{k.name}", k), "spoofed")
        except IllegalTransitionError as exc:
            print(f"refused: {exc}")
    print(f"pay_3003 still {fresh.state.name} v{fresh.version}, uncommitted = {len(fresh.uncommitted())}")

    print("\n== Replay: the history of pay_1001 rebuilds the same payment ==")
    for t in store.history("pay_1001"):
        print(t)
    rebuilt = Payment.replay("pay_1001", Money.of_minor(4999, USD), store.history("pay_1001"), clock)
    print(f"replayed -> {rebuilt.state.name} v{rebuilt.version}, refunded {rebuilt.refunded}")
    print(f"processor calls: {len(processor.calls)}")


if __name__ == "__main__":
    main()
