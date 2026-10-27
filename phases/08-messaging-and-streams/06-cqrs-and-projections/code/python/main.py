"""Lesson: phases/08-messaging-and-streams/06-cqrs-and-projections/docs/en.md
CQRS and Projections
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: Greg Young, "CQRS Documents" (2010); Fowler, "CQRS" (martinfowler.com, 2011); Kleppmann, "Designing
         Data-Intensive Applications", ch. 11 (deriving state from an event log); EventStoreDB docs on $all and checkpoints.
Python 3.11+, standard library only. Same design as the Java version. Time comes from an injected SimClock; nothing sleeps.
"""

from __future__ import annotations

from dataclasses import dataclass, fields
from functools import total_ordering
from typing import Dict, List, Optional, Union


@dataclass(frozen=True)
class Currency:
    """ISO 4217 currency: code and minor-unit exponent. Copied from Phase 10, Lesson 01; lessons are self-contained."""

    code: str
    exponent: int

    def __post_init__(self) -> None:
        if self.code is None or len(self.code) != 3:
            raise ValueError(f"currency code must be three letters: {self.code}")
        if self.exponent < 0 or self.exponent > 4:
            raise ValueError(f"exponent must be 0..4: {self.exponent}")


USD = Currency("USD", 2)


class CurrencyMismatchError(Exception):
    def __init__(self, a: Currency, b: Currency) -> None:
        super().__init__(f"cannot combine {a.code} with {b.code}")


@total_ordering
@dataclass(frozen=True)
class Money:
    """Integer minor units plus a currency. Mixing currencies is an error."""

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
            raise CurrencyMismatchError(self.currency, other.currency)

    def plus(self, other: "Money") -> "Money":
        self._require_same_currency(other)
        return Money(self.minor_units + other.minor_units, self.currency)

    def minus(self, other: "Money") -> "Money":
        self._require_same_currency(other)
        return Money(self.minor_units - other.minor_units, self.currency)

    def is_positive(self) -> bool:
        return self.minor_units > 0

    def compare_to(self, other: "Money") -> int:
        self._require_same_currency(other)
        return (self.minor_units > other.minor_units) - (self.minor_units < other.minor_units)

    def __lt__(self, other: "Money") -> bool:
        return self.compare_to(other) < 0

    def __str__(self) -> str:
        scale = 10 ** self.currency.exponent
        abs_units = abs(self.minor_units)
        frac = str(abs_units % scale).rjust(self.currency.exponent, "0")
        return f"{'-' if self.minor_units < 0 else ''}{abs_units // scale}.{frac} {self.currency.code}"


class SimClock:
    """Simulated clock: the demo advances it explicitly. Milliseconds since an arbitrary epoch."""

    def __init__(self, start: int) -> None:
        self._now = start

    def now(self) -> int:
        return self._now

    def advance(self, millis: int) -> None:
        if millis < 0:
            raise ValueError("cannot go backwards")
        self._now += millis


# ---------------------------------------------------------------------------------------------------------------------
# Write side: commands are validated against the aggregate's current state; the result is events appended to the store.
# ---------------------------------------------------------------------------------------------------------------------


@dataclass(frozen=True)
class AccountOpened:
    account_id: str
    owner: str
    currency: Currency


@dataclass(frozen=True)
class Deposited:
    amount: Money
    reference: str


@dataclass(frozen=True)
class Withdrawn:
    amount: Money
    reference: str


Event = Union[AccountOpened, Deposited, Withdrawn]


@dataclass(frozen=True)
class OpenAccount:
    account_id: str
    owner: str
    currency: Currency


@dataclass(frozen=True)
class Deposit:
    amount: Money
    reference: str


@dataclass(frozen=True)
class Withdraw:
    amount: Money
    reference: str


Command = Union[OpenAccount, Deposit, Withdraw]


class CommandRejectedError(Exception):
    pass


class ConcurrencyError(Exception):
    def __init__(self, stream_id: str, expected: int, actual: int) -> None:
        super().__init__(f"stream {stream_id} expected version {expected} but is at {actual}")


@dataclass(frozen=True)
class AccountState:
    """The write model: only what is needed to validate commands. No owner name, no history, no daily totals."""

    account_id: Optional[str]
    currency: Optional[Currency]
    balance: Optional[Money]
    version: int

    def exists(self) -> bool:
        return self.account_id is not None

    def apply(self, event: Event) -> "AccountState":
        match event:
            case AccountOpened(account_id, _, currency):
                return AccountState(account_id, currency, Money.zero(currency), self.version + 1)
            case Deposited(amount, _):
                return AccountState(self.account_id, self.currency, self.balance.plus(amount), self.version + 1)
            case Withdrawn(amount, _):
                return AccountState(self.account_id, self.currency, self.balance.minus(amount), self.version + 1)
        raise TypeError(f"unknown event {event!r}")

    def decide(self, command: Command) -> List[Event]:
        """Decide: validate the command against current state and return the events it produces, or reject it."""
        match command:
            case OpenAccount(account_id, owner, currency):
                if self.exists():
                    raise CommandRejectedError(f"account {account_id} already open")
                return [AccountOpened(account_id, owner, currency)]
            case Deposit(amount, reference):
                self._require_open()
                if not amount.is_positive():
                    raise CommandRejectedError("deposit must be positive")
                return [Deposited(amount, reference)]
            case Withdraw(amount, reference):
                self._require_open()
                if not amount.is_positive():
                    raise CommandRejectedError("withdrawal must be positive")
                if self.balance.compare_to(amount) < 0:
                    raise CommandRejectedError(f"insufficient funds: balance {self.balance}, requested {amount}")
                return [Withdrawn(amount, reference)]
        raise TypeError(f"unknown command {command!r}")

    def _require_open(self) -> None:
        if not self.exists():
            raise CommandRejectedError("account does not exist")


AccountState.EMPTY = AccountState(None, None, None, 0)


@dataclass(frozen=True)
class EventEnvelope:
    """An event as stored: its position in the global log (dense, 1-based, across all streams), its stream and version
    within that stream, and when the store recorded it. Projections consume the global log in position order."""

    position: int
    stream_id: str
    version: int
    recorded_at: int
    event: Event


class EventStore:
    """In-memory event store with per-stream optimistic concurrency and a single global log that projections read."""

    def __init__(self) -> None:
        self._streams: Dict[str, List[EventEnvelope]] = {}
        self._all: List[EventEnvelope] = []

    def version(self, stream_id: str) -> int:
        return len(self._streams.get(stream_id, []))

    def last_position(self) -> int:
        """Position of the newest event in the global log; 0 when empty."""
        return len(self._all)

    def append(self, stream_id: str, expected_version: int, events: List[Event], recorded_at: int) -> int:
        """Check-and-append in one step: the stream must be at expected_version. Returns the position of the last event written."""
        stream = self._streams.setdefault(stream_id, [])
        if len(stream) != expected_version:
            raise ConcurrencyError(stream_id, expected_version, len(stream))
        for e in events:
            env = EventEnvelope(len(self._all) + 1, stream_id, len(stream) + 1, recorded_at, e)
            stream.append(env)
            self._all.append(env)
        return len(self._all)

    def read_stream(self, stream_id: str) -> List[EventEnvelope]:
        return list(self._streams.get(stream_id, []))

    def read_all(self, after_position: int, max_events: int) -> List[EventEnvelope]:
        """Up to max_events events with position > after_position, in position order. The read-side's only entry point."""
        if after_position < 0 or after_position > len(self._all):
            raise ValueError(f"position {after_position} not in [0, {len(self._all)}]")
        to = min(len(self._all), after_position + max(0, max_events))
        return list(self._all[after_position:to])


class CommandHandler:
    """Loads the stream, folds it into state, decides, appends with the loaded version. Returns the global position written."""

    def __init__(self, store: EventStore, clock: SimClock) -> None:
        self._store = store
        self._clock = clock

    def handle(self, account_id: str, command: Command) -> int:
        state = AccountState.EMPTY
        for env in self._store.read_stream(account_id):
            state = state.apply(env.event)
        produced = state.decide(command)
        return self._store.append(account_id, state.version, produced, self._clock.now())


# ---------------------------------------------------------------------------------------------------------------------
# Read side: projections fold the global log into query-shaped state. Each has a name and a version; a checkpoint per
# (name, version) records how far into the log it has read.
# ---------------------------------------------------------------------------------------------------------------------


class Projection:
    def name(self) -> str:
        raise NotImplementedError

    def version(self) -> int:
        raise NotImplementedError

    def apply(self, envelope: EventEnvelope) -> None:
        """Apply one event. Must be safe to call twice with the same envelope (the projector may redeliver after a crash)."""
        raise NotImplementedError


class CheckpointStore:
    """Durable "how far has this projection read": one position per (name, version). In production a row next to the view's tables."""

    def __init__(self) -> None:
        self._positions: Dict[str, int] = {}

    @staticmethod
    def key(p: Projection) -> str:
        return f"{p.name()}@v{p.version()}"

    def position(self, p: Projection) -> int:
        return self._positions.get(self.key(p), 0)

    def save(self, p: Projection, position: int) -> None:
        if position < self.position(p):
            raise RuntimeError(f"checkpoint for {self.key(p)} would move backwards")
        self._positions[self.key(p)] = position

    def reset(self, p: Projection) -> None:
        self._positions.pop(self.key(p), None)


class CrashError(Exception):
    """Raised by the fault injector to simulate a process dying between applying events and saving the checkpoint."""


class Projector:
    """Drives one projection: read from its checkpoint, apply, save the checkpoint. If the process dies after apply and
    before save (crash_after_applies), the next run re-applies the same events, so apply must be idempotent."""

    def __init__(self, store: EventStore, checkpoints: CheckpointStore, projection: Projection, batch_size: int) -> None:
        if batch_size <= 0:
            raise ValueError("batchSize must be positive")
        self._store = store
        self._checkpoints = checkpoints
        self._projection = projection
        self._batch_size = batch_size
        self._crash_after_applies = -1
        self._last_run_at: Optional[int] = None

    def projection(self) -> Projection:
        return self._projection

    def position(self) -> int:
        return self._checkpoints.position(self._projection)

    def lag(self) -> int:
        return self._store.last_position() - self.position()

    def caught_up(self) -> bool:
        return self.lag() == 0

    def crash_after_applies(self, n: int) -> None:
        self._crash_after_applies = n

    def run_once(self, now: int) -> int:
        """One batch: returns how many events were applied. Call repeatedly (or use catch_up) to drain the log."""
        self._last_run_at = now
        batch = self._store.read_all(self.position(), self._batch_size)
        applied = 0
        for env in batch:
            self._projection.apply(env)
            applied += 1
            if 0 <= self._crash_after_applies <= applied:
                self._crash_after_applies = -1
                raise CrashError(f"projector for {CheckpointStore.key(self._projection)} died after applying {applied} events, "
                                 "before saving the checkpoint")
        if batch:
            self._checkpoints.save(self._projection, batch[-1].position)
        return applied

    def catch_up(self, now: int) -> int:
        """Run batches until the projection has read everything. Returns total events applied."""
        total = 0
        while not self.caught_up():
            total += self.run_once(now)
        return total

    def last_run_at(self) -> Optional[int]:
        return self._last_run_at


class BalanceView(Projection):
    """Read model 1: current balance per account. Naive version: applies whatever it is given."""

    def __init__(self) -> None:
        self._balances: Dict[str, Money] = {}
        self._owners: Dict[str, str] = {}

    def name(self) -> str:
        return "balances"

    def version(self) -> int:
        return 1

    def apply(self, env: EventEnvelope) -> None:
        match env.event:
            case AccountOpened(account_id, owner, currency):
                self._balances[account_id] = Money.zero(currency)
                self._owners[account_id] = owner
            case Deposited(amount, _):
                current = self._balances.get(env.stream_id)
                self._balances[env.stream_id] = amount if current is None else current.plus(amount)
            case Withdrawn(amount, _):
                self._balances[env.stream_id] = self._balances[env.stream_id].minus(amount)

    def balance(self, account_id: str) -> Optional[Money]:
        return self._balances.get(account_id)

    def owner(self, account_id: str) -> Optional[str]:
        return self._owners.get(account_id)

    def all(self) -> Dict[str, Money]:
        return dict(sorted(self._balances.items()))


class IdempotentBalanceView(BalanceView):
    """Read model 1, done right: each row remembers the last position applied to it and ignores anything at or below it.
    This is what makes redelivery after a crash harmless."""

    def __init__(self) -> None:
        super().__init__()
        self._applied_through: Dict[str, int] = {}
        self._skipped = 0

    def version(self) -> int:
        return 2

    def apply(self, env: EventEnvelope) -> None:
        seen = self._applied_through.get(env.stream_id, 0)
        if env.position <= seen:
            self._skipped += 1
            return
        super().apply(env)
        self._applied_through[env.stream_id] = env.position

    def skipped(self) -> int:
        return self._skipped


@dataclass(frozen=True)
class Day:
    deposits: int
    withdrawals: int
    net: Money


class DailyActivityView(Projection):
    """Read model 2: activity per day, which the write model has no reason to know about."""

    Day = Day

    def __init__(self, currency: Currency) -> None:
        self._days: Dict[int, Day] = {}
        self._applied_through = 0
        self._currency = currency

    @staticmethod
    def day_of(millis: int) -> int:
        return millis // 86_400_000

    def name(self) -> str:
        return "daily-activity"

    def version(self) -> int:
        return 1

    def apply(self, env: EventEnvelope) -> None:
        if env.position <= self._applied_through:
            return
        self._applied_through = env.position
        day = self.day_of(env.recorded_at)
        current = self._days.get(day, Day(0, 0, Money.zero(self._currency)))
        match env.event:
            case Deposited(amount, _):
                self._days[day] = Day(current.deposits + 1, current.withdrawals, current.net.plus(amount))
            case Withdrawn(amount, _):
                self._days[day] = Day(current.deposits, current.withdrawals + 1, current.net.minus(amount))
            case AccountOpened():
                pass

    def day(self, day: int) -> Optional[Day]:
        return self._days.get(day)

    def all(self) -> Dict[int, Day]:
        return dict(sorted(self._days.items()))


@dataclass(frozen=True)
class Alert:
    position: int
    account_id: str
    owner: Optional[str]
    amount: Money
    reference: str


class LargeWithdrawalsView(Projection):
    """Read model 3: withdrawals at or above a threshold, for a compliance screen. Version 1 reports the account id and
    amount above 1000.00; version 2 lowers the threshold to 500.00 and adds the owner name, which requires having seen
    the AccountOpened event. Changing either is a new version, rebuilt from position 0."""

    Alert = Alert

    def __init__(self, version: int, threshold: Money) -> None:
        self._version = version
        self._threshold = threshold
        self._owners: Dict[str, str] = {}
        self._alerts: List[Alert] = []
        self._applied_through = 0

    def name(self) -> str:
        return "large-withdrawals"

    def version(self) -> int:
        return self._version

    def apply(self, env: EventEnvelope) -> None:
        if env.position <= self._applied_through:
            return
        self._applied_through = env.position
        match env.event:
            case AccountOpened(account_id, owner, _):
                self._owners[account_id] = owner
            case Withdrawn(amount, reference):
                if amount.compare_to(self._threshold) >= 0:
                    owner = self._owners.get(env.stream_id, "?") if self._version >= 2 else None
                    self._alerts.append(Alert(env.position, env.stream_id, owner, amount, reference))
            case Deposited():
                pass

    def alerts(self) -> List[Alert]:
        return list(self._alerts)


class ReadSide:
    """The query side's front door: by projection name, which version is live. A new version is registered, caught up in
    the background, and promoted only once it has read the whole log; queries never see a half-built view."""

    def __init__(self) -> None:
        self._live: Dict[str, Projector] = {}
        self._building: Dict[str, Projector] = {}

    def register(self, projector: Projector) -> None:
        name = projector.projection().name()
        if name in self._live:
            self._building[name] = projector
        else:
            self._live[name] = projector

    def live(self, name: str) -> Projector:
        p = self._live.get(name)
        if p is None:
            raise ValueError(f"no live projection named {name}")
        return p

    def building(self, name: str) -> Optional[Projector]:
        return self._building.get(name)

    def promote(self, name: str) -> Projector:
        """Promote the building version to live. Refuses while it is behind the log. Returns the retired projector."""
        nxt = self._building.get(name)
        if nxt is None:
            raise RuntimeError(f"nothing is being built for {name}")
        if not nxt.caught_up():
            raise RuntimeError(f"{CheckpointStore.key(nxt.projection())} is {nxt.lag()} events behind; not promoting")
        old = self._live[name]
        self._live[name] = nxt
        del self._building[name]
        return old

    def wait_for(self, name: str, position: int, now: int, max_runs: int) -> bool:
        """Block until the live view has read at least position: read-your-own-writes for one request."""
        p = self.live(name)
        i = 0
        while i < max_runs and p.position() < position:
            p.run_once(now)
            i += 1
        return p.position() >= position


@dataclass(frozen=True)
class Window:
    events: int
    min: int
    mean: int
    max: int


class ConsistencyProbe:
    """Measures the eventual-consistency window: for each event, projected_at - recorded_at, under a polling projector."""

    Window = Window

    @staticmethod
    def run(commands: int, command_gap_millis: int, poll_interval_millis: int, batch_size: int) -> Window:
        """Simulate commands commands issued one every command_gap_millis, with a projector that runs every
        poll_interval_millis. Returns the staleness distribution. Deterministic: driven by the SimClock only."""
        clock = SimClock(1_700_000_000_000)
        store = EventStore()
        handler = CommandHandler(store, clock)
        view = IdempotentBalanceView()
        projector = Projector(store, CheckpointStore(), view, batch_size)
        handler.handle("acct-probe", OpenAccount("acct-probe", "Probe", USD))
        projector.catch_up(clock.now())
        recorded_at: Dict[int, int] = {}
        staleness: List[int] = []
        next_command, next_poll = clock.now(), clock.now() + poll_interval_millis
        issued = 0
        while issued < commands or not projector.caught_up():
            if issued < commands and clock.now() >= next_command:
                pos = handler.handle("acct-probe", Deposit(Money.of_minor(100 + issued, USD), f"d-{issued}"))
                recorded_at[pos] = clock.now()
                issued += 1
                next_command += command_gap_millis
            if clock.now() >= next_poll:
                before = projector.position()
                projector.run_once(clock.now())
                for p in range(before + 1, projector.position() + 1):
                    staleness.append(clock.now() - recorded_at[p])
                next_poll += poll_interval_millis
            clock.advance(1)
        if not staleness:
            return Window(0, 0, 0, 0)
        return Window(len(staleness), min(staleness), sum(staleness) // len(staleness), max(staleness))


def usd(minor: int) -> Money:
    return Money.of_minor(minor, USD)


def _java_style(value) -> str:
    """Render a record the way Java's record toString does, so the demo listing matches the Java run."""
    if isinstance(value, Money):
        return str(value)
    if value is None:
        return "null"
    if hasattr(value, "__dataclass_fields__"):
        parts = []
        for f in fields(value):
            name = "".join(w if i == 0 else w.capitalize() for i, w in enumerate(f.name.split("_")))
            parts.append(f"{name}={_java_style(getattr(value, f.name))}")
        return f"{type(value).__name__}[{', '.join(parts)}]"
    return str(value)


def main() -> None:
    clock = SimClock(1_700_000_000_000)
    store = EventStore()
    commands = CommandHandler(store, clock)
    checkpoints = CheckpointStore()

    print("== 1. Command side: validate against the write model, append events ==")
    commands.handle("acct-alice", OpenAccount("acct-alice", "Alice Ng", USD))
    commands.handle("acct-bob", OpenAccount("acct-bob", "Bob Osei", USD))
    commands.handle("acct-alice", Deposit(usd(250_000), "payroll-oct"))
    commands.handle("acct-bob", Deposit(usd(80_000), "refund-4471"))
    clock.advance(3_600_000)
    commands.handle("acct-alice", Withdraw(usd(120_000), "rent"))
    commands.handle("acct-bob", Withdraw(usd(60_000), "card-9921"))
    clock.advance(86_400_000)
    commands.handle("acct-alice", Withdraw(usd(75_000), "tuition"))
    last_write = commands.handle("acct-bob", Deposit(usd(15_000), "cashback"))
    try:
        commands.handle("acct-bob", Withdraw(usd(99_999), "overdraft-attempt"))
    except CommandRejectedError as e:
        print(f"rejected: {e}")
    print(f"global log has {store.last_position()} events; acct-alice at version {store.version('acct-alice')}, "
          f"acct-bob at version {store.version('acct-bob')}")
    for env in store.read_all(0, 100):
        print(f"  pos {env.position}  {env.stream_id:<10} v{env.version}  {_java_style(env.event)}")

    print("\n== 2. Read side: three projections fold the same log into three shapes ==")
    read_side = ReadSide()
    balances = Projector(store, checkpoints, IdempotentBalanceView(), 3)
    daily = Projector(store, checkpoints, DailyActivityView(USD), 3)
    large = Projector(store, checkpoints, LargeWithdrawalsView(1, usd(100_000)), 3)
    for p in (balances, daily, large):
        read_side.register(p)
        applied = p.catch_up(clock.now())
        print(f"{CheckpointStore.key(p.projection()):<22} applied {applied} events, checkpoint at position {p.position()}")
    balance_view = read_side.live("balances").projection()
    print("balances: {" + ", ".join(f"{k}={v}" for k, v in balance_view.all().items()) + "}")
    daily_view = read_side.live("daily-activity").projection()
    for day, d in daily_view.all().items():
        print(f"day {day}: {_java_style(d)}")
    large_v1 = read_side.live("large-withdrawals").projection()
    print("large withdrawals v1 (>= 1000.00): [" + ", ".join(_java_style(a) for a in large_v1.alerts()) + "]")

    print("\n== 3. New events arrive; projections are behind until they poll ==")
    clock.advance(500)
    pos = commands.handle("acct-alice", Deposit(usd(5_000), "interest"))
    print(f"wrote position {pos}; balances projection is at {balances.position()} (lag {balances.lag()}): "
          f"a read now returns {balance_view.balance('acct-alice')}")
    ok = read_side.wait_for("balances", pos, clock.now(), 10)
    print(f"waitFor(position {pos}) -> {str(ok).lower()}; a read now returns {balance_view.balance('acct-alice')}")

    print("\n== 4. Projection versioning: rebuild v2 from position 0, promote when caught up ==")
    large_v2 = Projector(store, checkpoints, LargeWithdrawalsView(2, usd(50_000)), 4)
    read_side.register(large_v2)
    print(f"v2 registered; checkpoint {CheckpointStore.key(large_v2.projection())} = {large_v2.position()}, "
          f"live is still v{read_side.live('large-withdrawals').projection().version()}")
    large_v2.run_once(clock.now())
    try:
        read_side.promote("large-withdrawals")
    except RuntimeError as e:
        print(f"promote refused: {e}")
    large_v2.catch_up(clock.now())
    retired = read_side.promote("large-withdrawals")
    print(f"promoted v{read_side.live('large-withdrawals').projection().version()}; retired v{retired.projection().version()} "
          f"(its checkpoint stays at {retired.position()} until it is deleted)")
    for a in large_v2.projection().alerts():
        print(f"  {_java_style(a)}")

    print("\n== 5. Crash between apply and checkpoint: naive vs idempotent projection ==")
    for view in (BalanceView(), IdempotentBalanceView()):
        p = Projector(store, CheckpointStore(), view, 4)
        p.run_once(clock.now())
        p.crash_after_applies(2)
        try:
            p.run_once(clock.now())
        except CrashError as e:
            print(f"{type(view).__name__}: {e}")
        p.catch_up(clock.now())
        print(f"  after restart: alice={view.balance('acct-alice')} bob={view.balance('acct-bob')} "
              f"(truth: alice={balance_view.balance('acct-alice')} bob={balance_view.balance('acct-bob')})")

    print("\n== 6. The eventual-consistency window, measured ==")
    for poll in (50, 10, 1):
        w = ConsistencyProbe.run(200, 7, poll, 100)
        print(f"200 commands every 7 ms, projector polls every {poll:>2} ms: staleness min={w.min} mean={w.mean} max={w.max} ms "
              f"over {w.events} events")
    small = ConsistencyProbe.run(200, 7, 10, 1)
    print(f"same, polling every 10 ms but batchSize=1 (projector slower than the producer): staleness mean={small.mean} max={small.max} ms")
    print(f"\nlast write position in the main store: {store.last_position()}; every live projection is at {read_side.live('balances').position()}")
    if last_write <= 0:
        raise RuntimeError("unreachable")


if __name__ == "__main__":
    main()
