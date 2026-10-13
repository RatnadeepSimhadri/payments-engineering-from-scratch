"""Lesson: phases/08-messaging-and-streams/05-event-sourcing/docs/en.md
Event Sourcing: State as a Fold Over Events
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: Fowler, "Event Sourcing" (martinfowler.com, 2005); Young, "CQRS Documents" (2010) on expected-version
         concurrency; EventStoreDB docs on optimistic concurrency and snapshots. Money follows Phase 10, Lesson 01.
Python 3.11+, standard library only. Same design as the Java and Go versions.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from functools import total_ordering
from types import MappingProxyType
from typing import Mapping, Optional, Sequence


# ---------------------------------------------------------------------------------------------------------------------
# Money: copied from Phase 10, Lesson 01. Lessons are self-contained; there is no shared library.
# ---------------------------------------------------------------------------------------------------------------------


@dataclass(frozen=True)
class Currency:
    code: str
    exponent: int

    def __post_init__(self) -> None:
        if len(self.code) != 3:
            raise ValueError(f"currency code must be three letters: {self.code!r}")
        if not 0 <= self.exponent <= 4:
            raise ValueError(f"exponent must be 0..4: {self.exponent}")


USD = Currency("USD", 2)
EUR = Currency("EUR", 2)
JPY = Currency("JPY", 0)


class CurrencyMismatchError(ValueError):
    def __init__(self, a: Currency, b: Currency) -> None:
        super().__init__(f"cannot combine {a.code} with {b.code}")


@total_ordering
@dataclass(frozen=True)
class Money:
    """Integer minor units plus a currency. Python ints never overflow; mixing currencies is an error."""

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

    def __lt__(self, other: "Money") -> bool:
        self._require_same_currency(other)
        return self.minor_units < other.minor_units

    def __str__(self) -> str:
        scale = 10 ** self.currency.exponent
        whole, frac = divmod(abs(self.minor_units), scale)
        text = str(whole)
        if self.currency.exponent > 0:
            text += "." + str(frac).rjust(self.currency.exponent, "0")
        return f"{'-' if self.minor_units < 0 else ''}{text} {self.currency.code}"

    __repr__ = __str__


# ---------------------------------------------------------------------------------------------------------------------
# Events: facts that happened. Past tense, immutable, never validated (validation happened before they were written).
# ---------------------------------------------------------------------------------------------------------------------


@dataclass(frozen=True)
class Opened:
    account_id: str
    currency: Currency


@dataclass(frozen=True)
class Deposited:
    amount: Money


@dataclass(frozen=True)
class Withdrawn:
    amount: Money


@dataclass(frozen=True)
class Held:
    hold_id: str
    amount: Money


@dataclass(frozen=True)
class Released:
    hold_id: str


Event = Opened | Deposited | Withdrawn | Held | Released


# ---------------------------------------------------------------------------------------------------------------------
# Commands: requests that may be refused. Imperative mood.
# ---------------------------------------------------------------------------------------------------------------------


@dataclass(frozen=True)
class OpenAccount:
    account_id: str
    currency: Currency


@dataclass(frozen=True)
class Deposit:
    amount: Money


@dataclass(frozen=True)
class Withdraw:
    amount: Money


@dataclass(frozen=True)
class PlaceHold:
    hold_id: str
    amount: Money


@dataclass(frozen=True)
class ReleaseHold:
    hold_id: str


Command = OpenAccount | Deposit | Withdraw | PlaceHold | ReleaseHold


class CommandRejectedError(Exception):
    """The command violates an invariant. No event is produced."""


class ConcurrencyError(Exception):
    """Another writer appended to the stream between load and append."""

    def __init__(self, stream_id: str, expected: int, actual: int) -> None:
        super().__init__(f"stream {stream_id}: expected version {expected} but found {actual}")
        self.expected = expected
        self.actual = actual


# ---------------------------------------------------------------------------------------------------------------------
# The aggregate: state is a pure function of the event sequence. apply() has no branches that can fail.
# ---------------------------------------------------------------------------------------------------------------------


@dataclass(frozen=True)
class AccountState:
    """Immutable account state. version = number of events applied so far."""

    account_id: Optional[str]
    currency: Optional[Currency]
    balance: Optional[Money]
    holds: Mapping[str, Money]
    version: int

    @staticmethod
    def empty() -> "AccountState":
        return AccountState(None, None, None, MappingProxyType({}), 0)

    def is_open(self) -> bool:
        return self.account_id is not None

    def available(self) -> Money:
        """Balance minus every active hold: what a withdrawal or new hold may use."""
        assert self.balance is not None and self.currency is not None
        held = Money.zero(self.currency)
        for m in self.holds.values():
            held = held.plus(m)
        return self.balance.minus(held)

    def apply(self, event: Event) -> "AccountState":
        """The fold step. Total: every event type is handled, every result is a new state with version + 1."""
        nxt = self.version + 1
        match event:
            case Opened(account_id, currency):
                return AccountState(account_id, currency, Money.zero(currency), MappingProxyType({}), nxt)
            case Deposited(amount):
                assert self.balance is not None
                return AccountState(self.account_id, self.currency, self.balance.plus(amount), self.holds, nxt)
            case Withdrawn(amount):
                assert self.balance is not None
                return AccountState(self.account_id, self.currency, self.balance.minus(amount), self.holds, nxt)
            case Held(hold_id, amount):
                holds = dict(self.holds)
                holds[hold_id] = amount
                return AccountState(self.account_id, self.currency, self.balance, MappingProxyType(holds), nxt)
            case Released(hold_id):
                holds = dict(self.holds)
                del holds[hold_id]  # the balance never moved; only the available amount changes
                return AccountState(self.account_id, self.currency, self.balance, MappingProxyType(holds), nxt)
        raise TypeError(f"unknown event {event!r}")

    def apply_all(self, events: Sequence[Event]) -> "AccountState":
        state = self
        for e in events:
            state = state.apply(e)
        return state

    @staticmethod
    def replay(events: Sequence[Event]) -> "AccountState":
        """Fold a whole history from the empty state. replay(events) == replay(prefix).apply_all(suffix)."""
        return AccountState.empty().apply_all(events)

    def decide(self, command: Command) -> list[Event]:
        """Validate a command against the current state and return the events it produces. The only place business
        rules live. Never mutates anything; the caller appends the events and folds them."""
        match command:
            case OpenAccount(account_id, currency):
                if self.is_open():
                    raise CommandRejectedError(f"account {self.account_id} is already open")
                return [Opened(account_id, currency)]
            case Deposit(amount):
                self._require_open()
                self._require_positive_same_currency(amount)
                return [Deposited(amount)]
            case Withdraw(amount):
                self._require_open()
                self._require_positive_same_currency(amount)
                if amount > self.available():
                    raise CommandRejectedError(f"insufficient available funds: {self.available()} < {amount}")
                return [Withdrawn(amount)]
            case PlaceHold(hold_id, amount):
                self._require_open()
                self._require_positive_same_currency(amount)
                if hold_id in self.holds:
                    raise CommandRejectedError(f"hold {hold_id} already exists")
                if amount > self.available():
                    raise CommandRejectedError(f"insufficient available funds for hold: {self.available()} < {amount}")
                return [Held(hold_id, amount)]
            case ReleaseHold(hold_id):
                self._require_open()
                if hold_id not in self.holds:
                    raise CommandRejectedError(f"no such hold: {hold_id}")
                return [Released(hold_id)]
        raise TypeError(f"unknown command {command!r}")

    def _require_open(self) -> None:
        if not self.is_open():
            raise CommandRejectedError("account is not open")

    def _require_positive_same_currency(self, amount: Money) -> None:
        if not amount.is_positive():
            raise CommandRejectedError(f"amount must be positive: {amount}")
        assert self.currency is not None
        if amount.currency != self.currency:
            raise CurrencyMismatchError(self.currency, amount.currency)


# ---------------------------------------------------------------------------------------------------------------------
# Storage: an append-only stream per aggregate with optimistic concurrency, plus a snapshot store.
# ---------------------------------------------------------------------------------------------------------------------


@dataclass(frozen=True)
class StoredEvent:
    """An event with the version it occupies in its stream (1-based)."""

    version: int
    event: Event


class EventStore:
    """In-memory event store. One list per stream id; append checks the expected version."""

    def __init__(self) -> None:
        self._streams: dict[str, list[StoredEvent]] = {}
        self._all: list[StoredEvent] = []

    def version(self, stream_id: str) -> int:
        """Current version of a stream (= number of events). 0 for a stream that does not exist."""
        return len(self._streams.get(stream_id, ()))

    def append(self, stream_id: str, expected_version: int, events: Sequence[Event]) -> int:
        """Append iff the stream is at expected_version. Returns the new version. Check-and-append is one step."""
        stream = self._streams.setdefault(stream_id, [])
        if len(stream) != expected_version:
            raise ConcurrencyError(stream_id, expected_version, len(stream))
        for e in events:
            stored = StoredEvent(len(stream) + 1, e)
            stream.append(stored)
            self._all.append(stored)
        return len(stream)

    def read(self, stream_id: str, after_version: int) -> list[Event]:
        """Events with version > after_version, in order. read(id, 0) is the whole stream."""
        return [s.event for s in self._streams.get(stream_id, ()) if s.version > after_version]

    def total_events(self) -> int:
        return len(self._all)


@dataclass(frozen=True)
class Snapshot:
    state: AccountState
    version: int


class SnapshotStore:
    """Latest snapshot per stream. A snapshot is a cache of a fold prefix; losing it costs time, never correctness."""

    def __init__(self) -> None:
        self._latest: dict[str, Snapshot] = {}
        self.writes = 0

    def save(self, stream_id: str, state: AccountState) -> None:
        self._latest[stream_id] = Snapshot(state, state.version)
        self.writes += 1

    def load(self, stream_id: str) -> Optional[Snapshot]:
        return self._latest.get(stream_id)


class AccountService:
    """The command side: load (snapshot + tail), decide, append with expected version, snapshot every N events."""

    def __init__(self, events: EventStore, snapshots: SnapshotStore, snapshot_every: int) -> None:
        if snapshot_every <= 0:
            raise ValueError("snapshot_every must be positive")
        self.events = events
        self.snapshots = snapshots
        self.snapshot_every = snapshot_every
        self.events_replayed_on_last_load = 0

    def load(self, stream_id: str) -> AccountState:
        """Current state: start from the latest snapshot (or empty) and fold the events after it."""
        snap = self.snapshots.load(stream_id)
        base = snap.state if snap else AccountState.empty()
        tail = self.events.read(stream_id, base.version)
        self.events_replayed_on_last_load = len(tail)
        return base.apply_all(tail)

    def replay_from_scratch(self, stream_id: str) -> AccountState:
        """Full replay from version 0, ignoring snapshots."""
        return AccountState.replay(self.events.read(stream_id, 0))

    def handle(self, stream_id: str, command: Command, expected_version: Optional[int] = None) -> AccountState:
        """Handle a command against the version the caller saw (or the latest). On ConcurrencyError the caller reloads,
        re-decides against the new state, and retries or gives up. Returns the new state."""
        current = self.load(stream_id)
        if expected_version is None:
            expected_version = current.version
        if current.version != expected_version:
            raise ConcurrencyError(stream_id, expected_version, current.version)
        produced = current.decide(command)
        new_version = self.events.append(stream_id, expected_version, produced)
        nxt = current.apply_all(produced)
        if new_version // self.snapshot_every > expected_version // self.snapshot_every:
            self.snapshots.save(stream_id, nxt)
        return nxt


def usd(cents: int) -> Money:
    return Money.of_minor(cents, USD)


def _print(state: AccountState, what: str) -> None:
    print(f"{what:<22} v{state.version} balance {state.balance} available {state.available()} holds {list(state.holds)}")


def main() -> None:
    store = EventStore()
    snapshots = SnapshotStore()
    service = AccountService(store, snapshots, 4)
    acct = "acct-42"

    print("== Commands become events; state is the fold ==")
    _print(service.handle(acct, OpenAccount(acct, USD)), "opened")
    _print(service.handle(acct, Deposit(usd(10_000))), "deposited 100.00")
    _print(service.handle(acct, PlaceHold("auth-1", usd(2_500))), "held 25.00 for auth-1")
    _print(service.handle(acct, Withdraw(usd(6_000))), "withdrew 60.00")

    print("\n== Rejected commands produce no events ==")
    try:
        service.handle(acct, Withdraw(usd(2_000)))
    except CommandRejectedError as e:
        print(f"withdraw 20.00 rejected: {e} (version still {store.version(acct)})")
    try:
        service.handle(acct, ReleaseHold("auth-9"))
    except CommandRejectedError as e:
        print(f"release auth-9 rejected: {e}")
    try:
        service.handle(acct, Deposit(Money.of_minor(500, EUR)))
    except CurrencyMismatchError as e:
        print(f"deposit 5.00 EUR rejected: {e}")

    print("\n== Optimistic concurrency: two writers load version 4 ==")
    seen = store.version(acct)
    a = service.handle(acct, ReleaseHold("auth-1"), seen)
    print(f"writer A released auth-1 at expected version {seen} -> version {a.version}, available {a.available()}")
    try:
        service.handle(acct, Withdraw(usd(4_000)), seen)
    except ConcurrencyError as e:
        print(f"writer B withdraw 40.00 at expected version {seen} rejected: {e}")
    b = service.handle(acct, Withdraw(usd(4_000)))
    print(f"writer B reloaded and retried -> version {b.version}, balance {b.balance}")

    print("\n== Snapshots: every 4 events, load folds only the tail ==")
    print(f"snapshot writes so far: {snapshots.writes} (versions 4)")
    service.handle(acct, Deposit(usd(1_000)))
    service.handle(acct, Deposit(usd(1_000)))
    latest = snapshots.load(acct)
    print(f"after 8 events: snapshot writes = {snapshots.writes}, latest snapshot version = {latest.version if latest else -1}")
    service.handle(acct, Deposit(usd(1_000)))
    loaded = service.load(acct)
    print(f"load at version {loaded.version} replayed {service.events_replayed_on_last_load} event(s) on top of the snapshot; "
          f"balance {loaded.balance}")
    full = service.replay_from_scratch(acct)
    print(f"full replay from version 0 gives the same state: {loaded == full}")

    print("\n== The stream as stored ==")
    for v, e in enumerate(store.read(acct, 0), start=1):
        print(f"v{v:<2} {e}")
    print(f"balance is derived: {full.balance} = sum of deposits - sum of withdrawals; available = {full.available()}")


if __name__ == "__main__":
    main()
