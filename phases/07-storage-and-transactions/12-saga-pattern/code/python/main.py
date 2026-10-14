"""Lesson: phases/07-storage-and-transactions/12-saga-pattern/docs/en.md
The Saga Pattern: Compensations Instead of Locks
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: Garcia-Molina & Salem, "Sagas" (SIGMOD 1987); Richardson, "Microservices Patterns" ch. 4 (semantic lock).
Python 3.11+, standard library only. Money is integer minor units (Phase 10, Lesson 01).
"""

from __future__ import annotations

from dataclasses import dataclass
from enum import Enum
from functools import total_ordering
from typing import Protocol

INT64_MIN, INT64_MAX = -(1 << 63), (1 << 63) - 1


def _exact(r: int) -> int:
    """Math.addExact / subtractExact: Python ints never overflow, so the signed 64-bit range is checked by hand."""
    if r < INT64_MIN or r > INT64_MAX:
        raise OverflowError("long overflow")
    return r


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


@total_ordering
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

    def _require_same_currency(self, other: "Money") -> None:
        if self.currency != other.currency:
            raise CurrencyMismatchException(self.currency, other.currency)

    def plus(self, other: "Money") -> "Money":
        self._require_same_currency(other)
        return Money(_exact(self.minor_units + other.minor_units), self.currency)

    def minus(self, other: "Money") -> "Money":
        self._require_same_currency(other)
        return Money(_exact(self.minor_units - other.minor_units), self.currency)

    def is_negative(self) -> bool:
        return self.minor_units < 0

    def compare_to(self, other: "Money") -> int:
        self._require_same_currency(other)
        return (self.minor_units > other.minor_units) - (self.minor_units < other.minor_units)

    def __lt__(self, other: "Money") -> bool:
        return self.compare_to(other) < 0

    def __str__(self) -> str:
        magnitude = abs(self.minor_units)
        scale = self.currency.minor_units_per_major()
        text = str(magnitude // scale)
        if self.currency.exponent > 0:
            text += f".{magnitude % scale:0{self.currency.exponent}d}"
        return f"{'-' if self.minor_units < 0 else ''}{text} {self.currency.code}"


class SimClock:
    """A clock that only moves when the simulation says so. Timestamps in the saga log come from here."""

    def __init__(self, start_ms: int) -> None:
        self._now_ms = start_ms

    def now(self) -> int:
        return self._now_ms

    def advance(self, ms: int) -> None:
        self._now_ms += ms


class StepFailedException(RuntimeError):
    """A step refused to do its work. The saga reacts by compensating everything before it."""


class CrashException(RuntimeError):
    """Raised at an injected crash point. The simulation catches it where the real process would die."""

    def __init__(self, where: str) -> None:
        super().__init__(f"crash at {where}")


@dataclass(frozen=True)
class CrashPoint:
    """Where the orchestrator dies: after a step's side effect happened but before the log recorded it."""

    step: str
    during_compensation: bool

    @staticmethod
    def after_execute(step: str) -> "CrashPoint":
        return CrashPoint(step, False)

    @staticmethod
    def after_compensate(step: str) -> "CrashPoint":
        return CrashPoint(step, True)


CrashPoint.NONE = CrashPoint("", False)


@dataclass(frozen=True)
class Order:
    """The business request the saga carries from step to step."""

    saga_id: str
    customer: str
    sku: str
    quantity: int
    amount: Money


class SagaStatus(Enum):
    RUNNING = "RUNNING"
    COMPENSATING = "COMPENSATING"
    COMPLETED = "COMPLETED"
    COMPENSATED = "COMPENSATED"


@dataclass(frozen=True)
class SagaRecord:
    """One durable saga-log record. The orchestrator appends one before and one after every action."""

    seq: int
    at_ms: int
    saga_id: str
    kind: str
    step: str
    detail: str
    order: Order | None

    def __str__(self) -> str:
        return (f"{self.seq} t={self.at_ms} {self.saga_id} {self.kind}" + ("" if not self.step else f" {self.step}")
                + ("" if not self.detail else f" ({self.detail})"))


class SagaLog:
    """Append-only saga log. Append is the durability point. SAGA_STARTED carries the Order so recovery can rebuild it."""

    SAGA_STARTED = "SAGA_STARTED"
    STEP_STARTED = "STEP_STARTED"
    STEP_COMPLETED = "STEP_COMPLETED"
    STEP_FAILED = "STEP_FAILED"
    COMP_STARTED = "COMP_STARTED"
    COMP_COMPLETED = "COMP_COMPLETED"
    SAGA_COMPLETED = "SAGA_COMPLETED"
    SAGA_COMPENSATED = "SAGA_COMPENSATED"

    def __init__(self, clock: SimClock) -> None:
        self._records: list[SagaRecord] = []
        self._clock = clock

    def append(self, saga_id: str, kind: str, step: str, detail: str, order: Order | None = None) -> None:
        self._records.append(SagaRecord(len(self._records) + 1, self._clock.now(), saga_id, kind, step, detail, order))

    def records(self) -> list[SagaRecord]:
        return list(self._records)  # a copy, like List.copyOf

    def for_saga(self, saga_id: str) -> list[SagaRecord]:
        return [r for r in self._records if r.saga_id == saga_id]

    def saga_ids(self) -> list[str]:
        return list(dict.fromkeys(r.saga_id for r in self._records))  # insertion-ordered set (LinkedHashSet)


class WalletService:
    """A reservation is a hold: it reduces what is available without moving the balance.

    Every mutating call takes an idempotency key (the saga id) and replays its earlier answer on a repeat.
    """

    def __init__(self, opening: dict[str, Money]) -> None:
        self._balances: dict[str, Money] = dict(opening)
        self._hold_account: dict[str, str] = {}  # key -> account
        self._hold_amount: dict[str, Money] = {}  # key -> amount still held
        self._captured: dict[str, Money] = {}  # key -> amount captured
        self._released: set[str] = set()
        self._refunded: set[str] = set()
        self.calls = 0

    def balance(self, account: str) -> Money | None:
        return self._balances.get(account)

    def available(self, account: str) -> Money:
        """Balance minus every live hold on the account: the number a second saga must consult."""
        a = self._balances[account]
        for key, acct in self._hold_account.items():
            if acct == account:
                a = a.minus(self._hold_amount[key])
        return a

    def has_hold(self, key: str) -> bool:
        return key in self._hold_amount and self._hold_amount[key] != Money.zero(self._hold_amount[key].currency)

    def is_captured(self, key: str) -> bool:
        return key in self._captured and key not in self._refunded

    def reserve(self, key: str, account: str, amount: Money) -> None:
        """Step 1: reserve. Fails if the available balance cannot cover the amount. Idempotent on key."""
        self.calls += 1
        if key in self._hold_account:
            return  # replay
        if account not in self._balances:
            raise StepFailedException(f"unknown account {account}")
        if self.available(account).compare_to(amount) < 0:
            raise StepFailedException(f"insufficient available funds: {self.available(account)} < {amount}")
        self._hold_account[key] = account
        self._hold_amount[key] = amount

    def release(self, key: str) -> None:
        """Compensation of reserve: drop the hold. Legal even if the hold never existed."""
        self.calls += 1
        if key in self._released:
            return
        if key in self._hold_account:
            self._hold_amount[key] = Money.zero(self._hold_amount[key].currency)
            del self._hold_account[key]
        self._released.add(key)

    def capture(self, key: str) -> None:
        """Step 2: capture. Turns the hold into a debit. Idempotent on key."""
        self.calls += 1
        if key in self._captured:
            return  # replay
        if key not in self._hold_account:
            raise StepFailedException(f"no hold to capture for {key}")
        account = self._hold_account[key]
        amount = self._hold_amount[key]
        self._balances[account] = self._balances[account].minus(amount)
        self._hold_amount[key] = Money.zero(amount.currency)
        del self._hold_account[key]
        self._captured[key] = amount

    def refund(self, key: str, account: str) -> None:
        """Compensation of capture: credit the money back. Not an undo: the debit and the refund both stay in history."""
        self.calls += 1
        if key in self._refunded:
            return
        if key in self._captured:
            self._balances[account] = self._balances[account].plus(self._captured[key])
        self._refunded.add(key)


class ShippingService:
    """Stock per SKU and shipments keyed by saga id."""

    def __init__(self, opening: dict[str, int]) -> None:
        self._stock: dict[str, int] = dict(opening)
        self._shipments: dict[str, int] = {}  # key -> quantity shipped
        self._cancelled: set[str] = set()
        self.calls = 0

    def stock_of(self, sku: str) -> int:
        return self._stock.get(sku, 0)

    def is_shipped(self, key: str) -> bool:
        return key in self._shipments and key not in self._cancelled

    def ship(self, key: str, sku: str, quantity: int) -> None:
        """Step 3: ship. Fails when stock is short. Idempotent on key."""
        self.calls += 1
        if key in self._shipments:
            return  # replay
        if self.stock_of(sku) < quantity:
            raise StepFailedException(f"out of stock: {sku} has {self.stock_of(sku)}, need {quantity}")
        self._stock[sku] = self.stock_of(sku) - quantity
        self._shipments[key] = quantity

    def cancel_shipment(self, key: str, sku: str) -> None:
        """Compensation of ship: cancel and restock."""
        self.calls += 1
        if key in self._cancelled:
            return
        if key in self._shipments:
            self._stock[sku] = self.stock_of(sku) + self._shipments[key]
        self._cancelled.add(key)


class SagaStep(Protocol):
    """A forward action and the compensation that semantically undoes it. Both must be idempotent."""

    def name(self) -> str: ...

    def execute(self, order: Order) -> None: ...

    def compensate(self, order: Order) -> None: ...


class ReserveFunds:
    def __init__(self, wallet: WalletService) -> None:
        self._wallet = wallet

    def name(self) -> str:
        return "reserve-funds"

    def execute(self, o: Order) -> None:
        self._wallet.reserve(o.saga_id, o.customer, o.amount)

    def compensate(self, o: Order) -> None:
        self._wallet.release(o.saga_id)


class CaptureFunds:
    def __init__(self, wallet: WalletService) -> None:
        self._wallet = wallet

    def name(self) -> str:
        return "capture-funds"

    def execute(self, o: Order) -> None:
        self._wallet.capture(o.saga_id)

    def compensate(self, o: Order) -> None:
        self._wallet.refund(o.saga_id, o.customer)


class ShipOrder:
    def __init__(self, shipping: ShippingService) -> None:
        self._shipping = shipping

    def name(self) -> str:
        return "ship-order"

    def execute(self, o: Order) -> None:
        self._shipping.ship(o.saga_id, o.sku, o.quantity)

    def compensate(self, o: Order) -> None:
        self._shipping.cancel_shipment(o.saga_id, o.sku)


@dataclass(frozen=True)
class SagaOutcome:
    saga_id: str
    status: SagaStatus
    failed_step: str
    reason: str

    def __str__(self) -> str:
        tail = "" if not self.failed_step else f" (failed at {self.failed_step}: {self.reason})"
        return f"{self.saga_id} -> {self.status.name}{tail}"


def _jlist(items) -> str:
    """Render a list like Java's List.toString, using each element's str()."""
    return "[" + ", ".join(str(x) for x in items) + "]"


class SagaOrchestrator:
    """Runs the steps in order; on failure compensates every completed step in reverse order.

    The log is appended before and after every action so a restart can resume from the log alone.
    """

    def __init__(self, log: SagaLog, steps: list[SagaStep], clock: SimClock, step_latency_ms: int) -> None:
        self._log = log
        self._steps = tuple(steps)
        self._clock = clock
        self._step_latency_ms = step_latency_ms
        self.crash_point = CrashPoint.NONE

    def log(self) -> SagaLog:
        return self._log

    def run(self, order: Order) -> SagaOutcome:
        self._log.append(order.saga_id, SagaLog.SAGA_STARTED, "", "", order)
        return self._forward(order, 0)

    def _forward(self, order: Order, frm: int) -> SagaOutcome:
        """Execute steps from index frm. A failure at step i compensates i-1 .. 0."""
        for i in range(frm, len(self._steps)):
            step = self._steps[i]
            self._log.append(order.saga_id, SagaLog.STEP_STARTED, step.name(), "")
            self._clock.advance(self._step_latency_ms)
            try:
                step.execute(order)
            except StepFailedException as e:
                self._log.append(order.saga_id, SagaLog.STEP_FAILED, step.name(), str(e))
                return self._backward(order, i - 1, step.name(), str(e))
            if self.crash_point == CrashPoint.after_execute(step.name()):
                raise CrashException(f"after {step.name()} executed, before STEP_COMPLETED logged")
            self._log.append(order.saga_id, SagaLog.STEP_COMPLETED, step.name(), "")
        self._log.append(order.saga_id, SagaLog.SAGA_COMPLETED, "", "")
        return SagaOutcome(order.saga_id, SagaStatus.COMPLETED, "", "")

    def _backward(self, order: Order, frm: int, failed_step: str, reason: str) -> SagaOutcome:
        """Compensate steps from index frm down to 0. Compensations are not allowed to fail; they are retried."""
        for i in range(frm, -1, -1):
            step = self._steps[i]
            self._log.append(order.saga_id, SagaLog.COMP_STARTED, step.name(), "")
            self._clock.advance(self._step_latency_ms)
            step.compensate(order)
            if self.crash_point == CrashPoint.after_compensate(step.name()):
                raise CrashException(f"after {step.name()} compensated, before COMP_COMPLETED logged")
            self._log.append(order.saga_id, SagaLog.COMP_COMPLETED, step.name(), "")
        self._log.append(order.saga_id, SagaLog.SAGA_COMPENSATED, "", "")
        return SagaOutcome(order.saga_id, SagaStatus.COMPENSATED, failed_step, reason)

    @staticmethod
    def status_of(records: list[SagaRecord]) -> SagaStatus:
        """Where a saga stands according to the log alone."""
        status = SagaStatus.RUNNING
        for r in records:
            match r.kind:
                case SagaLog.STEP_FAILED | SagaLog.COMP_STARTED:
                    status = SagaStatus.COMPENSATING
                case SagaLog.SAGA_COMPLETED:
                    status = SagaStatus.COMPLETED
                case SagaLog.SAGA_COMPENSATED:
                    status = SagaStatus.COMPENSATED
        return status

    def recover(self) -> list[SagaOutcome]:
        """Resume every unfinished saga from the log; idempotency makes re-executing an unlogged step safe."""
        self.crash_point = CrashPoint.NONE
        resumed: list[SagaOutcome] = []
        for saga_id in self._log.saga_ids():
            records = self._log.for_saga(saga_id)
            status = SagaOrchestrator.status_of(records)
            if status in (SagaStatus.COMPLETED, SagaStatus.COMPENSATED):
                continue
            order = records[0].order
            index = {s.name(): i for i, s in enumerate(self._steps)}
            last_completed, last_compensated = -1, len(self._steps)
            failed_step, reason = "", ""
            for r in records:
                match r.kind:
                    case SagaLog.STEP_COMPLETED:
                        last_completed = max(last_completed, index[r.step])
                    case SagaLog.STEP_FAILED:
                        failed_step, reason = r.step, r.detail
                    case SagaLog.COMP_COMPLETED:
                        last_compensated = min(last_compensated, index[r.step])
            if status is SagaStatus.RUNNING:
                resumed.append(self._forward(order, last_completed + 1))
            else:
                frm = min(last_compensated - 1, len(self._steps) - 1 if not failed_step else index[failed_step] - 1)
                resumed.append(self._backward(order, frm, failed_step, reason if reason else "resumed after crash"))
        return resumed


def usd(text: str) -> Money:
    neg = text.startswith("-")
    s = text[1:] if neg else text
    minor = int(s.replace(".", ""))
    return Money.of_minor(-minor if neg else minor, Currency.USD)


def show(wallet: WalletService, shipping: ShippingService) -> None:
    print(f"  alice balance={wallet.balance('alice')} available={wallet.available('alice')} "
          f"| widget stock={shipping.stock_of('widget')}")


def print_log(log: SagaLog, saga_id: str) -> None:
    for r in log.for_saga(saga_id):
        print(f"    {r}")


def main() -> None:
    clock = SimClock(0)
    wallet = WalletService({"alice": usd("100.00")})
    shipping = ShippingService({"widget": 5})
    log = SagaLog(clock)
    steps = [ReserveFunds(wallet), CaptureFunds(wallet), ShipOrder(shipping)]
    saga = SagaOrchestrator(log, steps, clock, 10)

    print("== 1. Happy path: alice buys 2 widgets for 30.00 USD ==")
    print(f"  {saga.run(Order('saga-1', 'alice', 'widget', 2, usd('30.00')))}")
    show(wallet, shipping)
    print_log(log, "saga-1")

    print("\n== 2. Step three fails: 4 widgets wanted, 3 in stock. Compensate in reverse ==")
    print(f"  {saga.run(Order('saga-2', 'alice', 'widget', 4, usd('40.00')))}")
    show(wallet, shipping)
    print_log(log, "saga-2")

    print("\n== 3. Crash after capture executed, before it was logged. Recovery re-runs capture; "
          "the key makes it a no-op ==")
    saga.crash_point = CrashPoint.after_execute("capture-funds")
    try:
        saga.run(Order("saga-3", "alice", "widget", 1, usd("25.00")))
    except CrashException as e:
        print(f"  orchestrator: {e}")
    print(f"  before recovery: balance={wallet.balance('alice')} (already captured), log status="
          f"{SagaOrchestrator.status_of(log.for_saga('saga-3')).name}")
    print(f"  recover -> {_jlist(saga.recover())}")
    show(wallet, shipping)
    print("  wallet calls for saga-3 capture replayed once; balance debited once")

    print("\n== 4. Crash mid-compensation: refund done, not logged. Recovery re-runs refund; no double credit ==")
    saga.crash_point = CrashPoint.after_compensate("capture-funds")
    try:
        saga.run(Order("saga-4", "alice", "widget", 9, usd("10.00")))
    except CrashException as e:
        print(f"  orchestrator: {e}")
    print(f"  before recovery: balance={wallet.balance('alice')} hold={'true' if wallet.has_hold('saga-4') else 'false'}")
    print(f"  recover -> {_jlist(saga.recover())}")
    show(wallet, shipping)

    print("\n== 5. The semantic lock: a hold is visible to a concurrent saga, and the trade-off it forces ==")
    w2 = WalletService({"alice": usd("100.00")})
    s2 = ShippingService({"widget": 0})
    saga2 = SagaOrchestrator(SagaLog(clock), [ReserveFunds(w2), CaptureFunds(w2), ShipOrder(s2)], clock, 10)
    saga2.crash_point = CrashPoint.after_execute("reserve-funds")
    try:
        saga2.run(Order("saga-A", "alice", "widget", 1, usd("80.00")))
    except CrashException as e:
        print(f"  saga-A reserved 80.00 and its orchestrator died: {e}")
    print(f"  alice balance={w2.balance('alice')} available={w2.available('alice')}  <- the hold is the semantic lock")
    print(f"  saga-B wants 50.00 while A is pending: {saga2.run(Order('saga-B', 'alice', 'widget', 1, usd('50.00')))}")
    print(f"  saga-A resumes, fails at shipping, compensates: {_jlist(saga2.recover())}")
    print(f"  alice balance={w2.balance('alice')} available={w2.available('alice')}")
    print("  B was refused on a hold that A later released: that is the price of the lock. "
          "Without it B would have read 100.00 and alice would be overdrawn.")
    print(f"\nclock={clock.now()} ms, log records={len(log.records())}")


if __name__ == "__main__":
    main()
