"""Lesson: phases/12-payment-systems-design/03-payment-orchestration/docs/en.md
Payment Orchestration Across Processors
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: Garcia-Molina & Salem, "Sagas" (SIGMOD 1987); IETF draft-ietf-httpapi-idempotency-key-header;
         Stripe API reference (PaymentIntent capture, Refund).
Python 3.11+, standard library only. Same design as the Java version.
"""

from __future__ import annotations

import copy
from collections import deque
from dataclasses import dataclass
from enum import Enum
from typing import Optional, Protocol


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

    def negate(self) -> "Money":
        return Money(-self.minor_units, self.currency)

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
# The saga vocabulary. Four forward steps in a fixed order; each has exactly one compensation.
# ---------------------------------------------------------------------------------------------


class SagaStep(Enum):
    TOKENIZE = ("vault", "deleteToken")
    AUTHORIZE = ("acquirer", "voidAuth")
    CAPTURE = ("acquirer", "refund")
    LEDGER_POST = ("ledger", "reverse")

    def __init__(self, system: str, compensation: str) -> None:
        self.system = system
        self.compensation = compensation

    def __str__(self) -> str:
        return self.name


class SagaState(Enum):
    RUNNING = "running"
    COMPLETED = "completed"
    COMPENSATING = "compensating"
    COMPENSATED = "compensated"
    DEAD_LETTERED = "dead_lettered"

    def is_terminal(self) -> bool:
        return self in (SagaState.COMPLETED, SagaState.COMPENSATED)

    def __str__(self) -> str:
        return self.name


class StepStatus(Enum):
    PENDING = "pending"
    DONE = "done"
    COMPENSATED = "compensated"


class ProcessorUnavailableError(RuntimeError):
    """Raised when a downstream system cannot be reached. The call may or may not have happened."""


class ProcessCrashError(RuntimeError):
    """Simulates our own process dying after the downstream committed but before we persisted."""


@dataclass(frozen=True)
class ProcessorResponse:
    approved: bool
    reference: Optional[str]
    code: str

    @staticmethod
    def approved_with(reference: str) -> "ProcessorResponse":
        return ProcessorResponse(True, reference, "00")

    @staticmethod
    def declined(code: str) -> "ProcessorResponse":
        return ProcessorResponse(False, None, code)


# ---------------------------------------------------------------------------------------------
# The persisted saga record. Every field is written to the store after every step; a crash at any
# point leaves a record that `recover` can resume from.
# ---------------------------------------------------------------------------------------------


class SagaRecord:
    def __init__(self, saga_id: str, payment_id: str, amount: Money, pan: str) -> None:
        if not amount.is_positive():
            raise ValueError(f"amount must be positive: {amount}")
        self.saga_id = saga_id
        self.payment_id = payment_id
        self.amount = amount
        self.pan = pan
        self.state = SagaState.RUNNING
        self.steps: dict[SagaStep, StepStatus] = {s: StepStatus.PENDING for s in SagaStep}
        self.references: dict[SagaStep, str] = {}
        self.forward_attempts: dict[SagaStep, int] = {s: 0 for s in SagaStep}
        self.compensation_attempts: dict[SagaStep, int] = {s: 0 for s in SagaStep}
        self.failure_reason: Optional[str] = None
        self.version = 0
        self.journal: list[str] = []

    def next_forward(self) -> Optional[SagaStep]:
        """The first step not yet DONE, in forward order; None when every step is DONE."""
        for s in SagaStep:
            if self.steps[s] is StepStatus.PENDING:
                return s
        return None

    def next_compensation(self) -> Optional[SagaStep]:
        """The last DONE step, in reverse order; None when nothing remains to undo."""
        for s in reversed(list(SagaStep)):
            if self.steps[s] is StepStatus.DONE:
                return s
        return None

    def mark_done(self, step: SagaStep, reference: str) -> None:
        self.steps[step] = StepStatus.DONE
        self.references[step] = reference
        self.journal.append(f"{step.name} done -> {reference}")

    def mark_compensated(self, step: SagaStep, how: str) -> None:
        self.steps[step] = StepStatus.COMPENSATED
        self.journal.append(f"{step.name} compensated via {how}")

    def begin_compensation(self, reason: str) -> None:
        self.state = SagaState.COMPENSATING
        self.failure_reason = reason
        self.journal.append(f"compensating: {reason}")

    def copy(self) -> "SagaRecord":
        return copy.deepcopy(self)

    def __str__(self) -> str:
        cells = " ".join(f"{s.name[0]}={self.steps[s].name[0]}" for s in SagaStep)
        return f"{self.saga_id} {self.state.name} v{self.version} [{cells}]"


class SagaStore:
    """Durable storage for saga records. Stores copies so an in-flight mutation that never reaches
    save() is lost on crash, as it would be in a database."""

    def __init__(self) -> None:
        self._records: dict[str, SagaRecord] = {}

    def save(self, r: SagaRecord) -> None:
        r.version += 1
        self._records[r.saga_id] = r.copy()

    def load(self, saga_id: str) -> SagaRecord:
        r = self._records.get(saga_id)
        if r is None:
            raise RuntimeError(f"unknown saga {saga_id}")
        return r.copy()

    def unfinished(self) -> list[SagaRecord]:
        return [r.copy() for r in self._records.values()
                if not r.state.is_terminal() and r.state is not SagaState.DEAD_LETTERED]


# ---------------------------------------------------------------------------------------------
# Downstream systems. Each operation takes an idempotency key: the same key returns the stored
# response without repeating the side effect. Faults are scripted per operation.
# ---------------------------------------------------------------------------------------------


class Fault(Enum):
    DECLINE = "decline"
    OUTAGE = "outage"
    CRASH_AFTER_COMMIT = "crash_after_commit"


class Processor(Protocol):
    def name(self) -> str: ...
    def tokenize(self, idempotency_key: str, pan: str) -> ProcessorResponse: ...
    def delete_token(self, idempotency_key: str, token: str) -> ProcessorResponse: ...
    def authorize(self, idempotency_key: str, token: str, amount: Money) -> ProcessorResponse: ...
    def void_auth(self, idempotency_key: str, auth_ref: str) -> ProcessorResponse: ...
    def capture(self, idempotency_key: str, auth_ref: str, amount: Money) -> ProcessorResponse: ...
    def refund(self, idempotency_key: str, capture_ref: str, amount: Money) -> ProcessorResponse: ...


class FakeProcessor:
    def __init__(self, name: str) -> None:
        self._name = name
        self._by_key: dict[str, ProcessorResponse] = {}
        self._faults: dict[str, deque[Fault]] = {}
        self._side_effects: dict[str, int] = {}
        self._replays = 0
        self._sequence = 0

    def name(self) -> str:
        return self._name

    def fail_next(self, operation: str, fault: Fault) -> "FakeProcessor":
        self._faults.setdefault(operation, deque()).append(fault)
        return self

    def side_effects(self, operation: str) -> int:
        return self._side_effects.get(operation, 0)

    def replays(self) -> int:
        return self._replays

    def _call(self, operation: str, key: str, decline_code: str) -> ProcessorResponse:
        stored = self._by_key.get(key)
        if stored is not None:
            self._replays += 1
            return stored
        q = self._faults.get(operation)
        fault = q.popleft() if q else None
        if fault is Fault.OUTAGE:
            raise ProcessorUnavailableError(f"{self._name}.{operation}: connection reset")
        if fault is Fault.DECLINE:
            response = ProcessorResponse.declined(decline_code)
        else:
            self._side_effects[operation] = self._side_effects.get(operation, 0) + 1
            self._sequence += 1
            response = ProcessorResponse.approved_with(f"{self._name}_{operation}_{self._sequence:04d}")
        self._by_key[key] = response
        if fault is Fault.CRASH_AFTER_COMMIT:
            raise ProcessCrashError(f"process died after {self._name}.{operation} committed")
        return response

    def tokenize(self, key: str, pan: str) -> ProcessorResponse:
        return self._call("tokenize", key, "invalid_card")

    def delete_token(self, key: str, token: str) -> ProcessorResponse:
        return self._call("deleteToken", key, "token_not_found")

    def authorize(self, key: str, token: str, amount: Money) -> ProcessorResponse:
        return self._call("authorize", key, "05")

    def void_auth(self, key: str, auth_ref: str) -> ProcessorResponse:
        return self._call("voidAuth", key, "already_captured")

    def capture(self, key: str, auth_ref: str, amount: Money) -> ProcessorResponse:
        return self._call("capture", key, "auth_expired")

    def refund(self, key: str, capture_ref: str, amount: Money) -> ProcessorResponse:
        return self._call("refund", key, "insufficient_merchant_balance")


@dataclass(frozen=True)
class Entry:
    entry_id: str
    payment_id: str
    debit_account: str
    credit_account: str
    amount: Money


class Ledger:
    """A two-account ledger with idempotent postings and reversals. The full ledger is Phase 10, Lesson 03."""

    def __init__(self) -> None:
        self._by_key: dict[str, Entry] = {}
        self._entries: list[Entry] = []
        self._faults: deque[Fault] = deque()
        self._sequence = 0

    def fail_next(self, fault: Fault) -> "Ledger":
        self._faults.append(fault)
        return self

    def post(self, key: str, payment_id: str, amount: Money) -> str:
        return self._append(key, payment_id, "acquirer_receivable", "merchant_payable", amount)

    def reverse(self, key: str, payment_id: str, amount: Money) -> str:
        return self._append(key, payment_id, "merchant_payable", "acquirer_receivable", amount)

    def _append(self, key: str, payment_id: str, debit: str, credit: str, amount: Money) -> str:
        stored = self._by_key.get(key)
        if stored is not None:
            return stored.entry_id
        f = self._faults.popleft() if self._faults else None
        if f is Fault.OUTAGE:
            raise ProcessorUnavailableError("ledger: write timeout")
        self._sequence += 1
        e = Entry(f"le_{self._sequence:04d}", payment_id, debit, credit, amount)
        self._by_key[key] = e
        self._entries.append(e)
        if f is Fault.CRASH_AFTER_COMMIT:
            raise ProcessCrashError("process died after ledger post committed")
        return e.entry_id

    def net_merchant_payable(self, payment_id: str, currency: Currency) -> Money:
        """Net movement into merchant_payable for one payment; zero after a completed compensation."""
        net = Money.zero(currency)
        for e in self._entries:
            if e.payment_id != payment_id:
                continue
            if e.credit_account == "merchant_payable":
                net = net.plus(e.amount)
            if e.debit_account == "merchant_payable":
                net = net.minus(e.amount)
        return net

    def entries(self) -> tuple[Entry, ...]:
        return tuple(self._entries)


# ---------------------------------------------------------------------------------------------
# Dead letters: sagas the orchestrator could not finish compensating. A human, not a loop, owns them.
# ---------------------------------------------------------------------------------------------


@dataclass(frozen=True)
class DeadLetter:
    saga_id: str
    step: SagaStep
    reason: str
    at_millis: int


# ---------------------------------------------------------------------------------------------
# The orchestrator. Load, do one thing, save. Repeat until terminal or until a crash propagates.
# ---------------------------------------------------------------------------------------------


class Orchestrator:
    def __init__(self, store: SagaStore, vault: Processor, acquirer: Processor, ledger: Ledger,
                 clock: Clock, max_attempts: int) -> None:
        self._store = store
        self._vault = vault
        self._acquirer = acquirer
        self._ledger = ledger
        self._clock = clock
        self._max_attempts = max_attempts
        self._dead_letters: list[DeadLetter] = []

    def dead_letters(self) -> tuple[DeadLetter, ...]:
        return tuple(self._dead_letters)

    @staticmethod
    def key(r: SagaRecord, step: SagaStep, compensation: bool) -> str:
        """The idempotency key for a step is a pure function of the saga and the step: retries and recoveries reuse it."""
        return f"{r.saga_id}:{step.name.lower()}" + (":comp" if compensation else ":fwd")

    def start(self, saga_id: str, payment_id: str, amount: Money, pan: str) -> SagaRecord:
        r = SagaRecord(saga_id, payment_id, amount, pan)
        self._store.save(r)
        return self.run(saga_id)

    def run(self, saga_id: str) -> SagaRecord:
        """Drive a saga until it is terminal or dead-lettered. A ProcessCrashError propagates: the process is gone."""
        r = self._store.load(saga_id)
        while True:
            if r.state is SagaState.RUNNING:
                step = r.next_forward()
                if step is None:
                    r.state = SagaState.COMPLETED
                    r.journal.append("completed")
                    self._store.save(r)
                    return r
                r.forward_attempts[step] += 1
                try:
                    response = self._forward(step, r)
                except ProcessorUnavailableError as e:
                    r.journal.append(f"{step.name} attempt {r.forward_attempts[step]} failed: {e}")
                    if r.forward_attempts[step] >= self._max_attempts:
                        r.begin_compensation(f"{step.name} exhausted {self._max_attempts} attempts: {e}")
                    self._store.save(r)
                    continue
                if not response.approved:
                    r.begin_compensation(f"{step.name} declined with code {response.code}")
                    self._store.save(r)
                    continue
                assert response.reference is not None
                r.mark_done(step, response.reference)
                self._store.save(r)
            elif r.state is SagaState.COMPENSATING:
                step = r.next_compensation()
                if step is None:
                    r.state = SagaState.COMPENSATED
                    r.journal.append("compensated")
                    self._store.save(r)
                    return r
                r.compensation_attempts[step] += 1
                try:
                    response = self._compensate(step, r)
                except ProcessorUnavailableError as e:
                    r.journal.append(f"{step.compensation} attempt {r.compensation_attempts[step]} failed: {e}")
                    if r.compensation_attempts[step] >= self._max_attempts:
                        self._dead_letter(r, step, str(e))
                    self._store.save(r)
                    if r.state is SagaState.DEAD_LETTERED:
                        return r
                    continue
                if not response.approved:
                    self._dead_letter(r, step, f"{step.compensation} refused with code {response.code}")
                    self._store.save(r)
                    return r
                assert response.reference is not None
                how = response.reference if response.reference.startswith("noop:") else step.compensation
                r.mark_compensated(step, how)
                self._store.save(r)
            else:  # COMPLETED, COMPENSATED, DEAD_LETTERED
                return r

    def _forward(self, step: SagaStep, r: SagaRecord) -> ProcessorResponse:
        k = Orchestrator.key(r, step, False)
        if step is SagaStep.TOKENIZE:
            return self._vault.tokenize(k, r.pan)
        if step is SagaStep.AUTHORIZE:
            return self._acquirer.authorize(k, r.references[SagaStep.TOKENIZE], r.amount)
        if step is SagaStep.CAPTURE:
            return self._acquirer.capture(k, r.references[SagaStep.AUTHORIZE], r.amount)
        return ProcessorResponse.approved_with(self._ledger.post(k, r.payment_id, r.amount))

    def _compensate(self, step: SagaStep, r: SagaRecord) -> ProcessorResponse:
        k = Orchestrator.key(r, step, True)
        if step is SagaStep.TOKENIZE:
            return self._vault.delete_token(k, r.references[SagaStep.TOKENIZE])
        if step is SagaStep.AUTHORIZE:
            # A captured authorization is consumed; the refund already returned the money. Voiding it would be
            # refused by the acquirer, so the compensation is semantic: nothing to undo.
            if r.steps[SagaStep.CAPTURE] is StepStatus.COMPENSATED:
                return ProcessorResponse.approved_with("noop:auth-consumed-by-capture")
            return self._acquirer.void_auth(k, r.references[SagaStep.AUTHORIZE])
        if step is SagaStep.CAPTURE:
            return self._acquirer.refund(k, r.references[SagaStep.CAPTURE], r.amount)
        return ProcessorResponse.approved_with(self._ledger.reverse(k, r.payment_id, r.amount))

    def _dead_letter(self, r: SagaRecord, step: SagaStep, reason: str) -> None:
        r.state = SagaState.DEAD_LETTERED
        r.journal.append(f"dead-lettered at {step.compensation}: {reason}")
        self._dead_letters.append(DeadLetter(r.saga_id, step, reason, self._clock.now_millis()))

    def recover(self) -> int:
        """After a restart: resume every saga the store says is unfinished. Each resumes from its persisted step."""
        resumed = 0
        for r in self._store.unfinished():
            self.run(r.saga_id)
            resumed += 1
        return resumed

    def redrive(self, saga_id: str) -> SagaRecord:
        """Operator action on a dead letter: the downstream is back, try the remaining compensations again."""
        r = self._store.load(saga_id)
        if r.state is not SagaState.DEAD_LETTERED:
            raise RuntimeError(f"{saga_id} is {r.state.name}, not DEAD_LETTERED")
        r.state = SagaState.COMPENSATING
        for s in SagaStep:
            r.compensation_attempts[s] = 0
        r.journal.append("redriven by operator")
        self._store.save(r)
        self._dead_letters = [d for d in self._dead_letters if d.saga_id != saga_id]
        return self.run(saga_id)


# ---------------------------------------------------------------------------------------------
# Demo
# ---------------------------------------------------------------------------------------------


def main() -> None:
    clock = FakeClock(1_700_000_000_000)
    store = SagaStore()
    vault = FakeProcessor("vault")
    acquirer = FakeProcessor("acquirer")
    ledger = Ledger()
    orch = Orchestrator(store, vault, acquirer, ledger, clock, 3)

    print("== The saga: four steps, four compensations ==")
    for s in SagaStep:
        print(f"{s.name:<12} on {s.system:<9} undone by {s.compensation}")

    print("\n== Happy path: tokenize -> authorize -> capture -> ledger post ==")
    r = orch.start("saga_1", "pay_1001", Money.of_minor(4999, USD), "4242424242424242")
    print(r)
    for line in r.journal:
        print("  " + line)
    print(f"merchant_payable for pay_1001: {ledger.net_merchant_payable('pay_1001', USD)}")

    print("\n== Decline at CAPTURE: compensations run in reverse order (void, delete token) ==")
    acquirer.fail_next("capture", Fault.DECLINE)
    r = orch.start("saga_2", "pay_1002", Money.of_minor(1250, USD), "4000000000000002")
    print(f"{r}  reason: {r.failure_reason}")
    for line in r.journal:
        print("  " + line)
    print(f"merchant_payable for pay_1002: {ledger.net_merchant_payable('pay_1002', USD)}")

    print("\n== Outage at AUTHORIZE: retried up to the budget, then compensated ==")
    acquirer.fail_next("authorize", Fault.OUTAGE).fail_next("authorize", Fault.OUTAGE)
    r = orch.start("saga_3", "pay_1003", Money.of_minor(800, USD), "4242424242424242")
    print(f"{r}  authorize attempts: {r.forward_attempts[SagaStep.AUTHORIZE]}")
    acquirer.fail_next("authorize", Fault.OUTAGE).fail_next("authorize", Fault.OUTAGE).fail_next("authorize", Fault.OUTAGE)
    r = orch.start("saga_4", "pay_1004", Money.of_minor(800, USD), "4242424242424242")
    print(f"{r}  reason: {r.failure_reason}")

    print("\n== Crash after the acquirer committed the authorization but before we saved ==")
    auths_before = acquirer.side_effects("authorize")
    acquirer.fail_next("authorize", Fault.CRASH_AFTER_COMMIT)
    try:
        orch.start("saga_5", "pay_1005", Money.of_minor(2000, USD), "4242424242424242")
    except ProcessCrashError as e:
        print(f"crash: {e}")
    persisted = store.load("saga_5")
    print(f"persisted after crash: {persisted}  (AUTHORIZE still PENDING; acquirer holds 1 auth)")
    restarted = Orchestrator(store, vault, acquirer, ledger, clock, 3)
    print(f"recover() resumed {restarted.recover()} saga(s)")
    r = store.load("saga_5")
    print(f"after recovery: {r}  acquirer authorizations: {acquirer.side_effects('authorize') - auths_before}"
          f", idempotent replays: {acquirer.replays()}")

    print("\n== Ledger post fails: the capture must be refunded, not voided ==")
    ledger.fail_next(Fault.OUTAGE).fail_next(Fault.OUTAGE).fail_next(Fault.OUTAGE)
    r = orch.start("saga_6", "pay_1006", Money.of_minor(3000, USD), "4242424242424242")
    print(f"{r}  reason: {r.failure_reason}")
    for line in r.journal:
        if "compensated via" in line:
            print("  " + line)
    print(f"acquirer refunds so far: {acquirer.side_effects('refund')}, merchant_payable for pay_1006: "
          f"{ledger.net_merchant_payable('pay_1006', USD)}")

    print("\n== Compensation itself fails: dead letter, then operator redrive ==")
    acquirer.fail_next("capture", Fault.DECLINE)
    for _ in range(3):
        acquirer.fail_next("voidAuth", Fault.OUTAGE)
    r = orch.start("saga_7", "pay_1007", Money.of_minor(5500, USD), "4242424242424242")
    print(r)
    for d in orch.dead_letters():
        print(f"  dead letter: {d.saga_id} at {d.step.compensation} ({d.reason})")
    clock.advance(15 * 60_000)
    r = orch.redrive("saga_7")
    print(f"after redrive: {r}  dead letters remaining: {len(orch.dead_letters())}")

    print("\n== Running a finished saga again is a no-op ==")
    before = acquirer.side_effects("capture")
    orch.run("saga_1")
    print(f"saga_1 still {store.load('saga_1').state.name}, captures unchanged: "
          f"{str(acquirer.side_effects('capture') == before).lower()}")


if __name__ == "__main__":
    main()
