"""Lesson: phases/07-storage-and-transactions/08-two-phase-commit/docs/en.md
Two-Phase Commit and Its Blocking Problem
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: Gray, "Notes on Data Base Operating Systems" (1978); Mohan, Lindsay, Obermarck,
"Transaction Management in the R* Distributed Database Management System" (1986); X/Open XA spec.
Python 3.11+, standard library only. Same design as the Java and Go versions.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from enum import Enum


# ---------------------------------------------------------------- Money (Phase 10, Lesson 01)


@dataclass(frozen=True)
class Currency:
    code: str
    exponent: int

    def minor_units_per_major(self) -> int:
        return 10 ** self.exponent


USD = Currency("USD", 2)


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

    def is_negative(self) -> bool:
        return self.minor_units < 0

    def __str__(self) -> str:
        magnitude = abs(self.minor_units)
        whole, frac = divmod(magnitude, self.currency.minor_units_per_major())
        text = str(whole)
        if self.currency.exponent > 0:
            text += "." + str(frac).rjust(self.currency.exponent, "0")
        return f"{'-' if self.minor_units < 0 else ''}{text} {self.currency.code}"

    __repr__ = __str__


def usd(text: str) -> Money:
    negative = text.startswith("-")
    digits = (text[1:] if negative else text).replace(".", "")
    minor = int(digits)
    return Money(-minor if negative else minor, USD)


# ---------------------------------------------------------------- simulation primitives


class SimClock:
    """A clock that only moves when the simulation says so."""

    def __init__(self, start_ms: int = 0) -> None:
        self._now = start_ms

    def now(self) -> int:
        return self._now

    def advance(self, ms: int) -> None:
        self._now += ms


class Vote(Enum):
    YES = "YES"
    NO = "NO"


class Decision(Enum):
    COMMIT = "COMMIT"
    ABORT = "ABORT"


class TxState(Enum):
    """What a participant knows about a transaction. PREPARED is the in-doubt state."""

    NONE = "NONE"
    PREPARED = "PREPARED"
    COMMITTED = "COMMITTED"
    ABORTED = "ABORTED"


class CrashPoint(Enum):
    """Where the coordinator dies, if anywhere; the interesting points sit on either side of the decision record."""

    NONE = "NONE"
    AFTER_VOTES_BEFORE_DECISION_LOG = "AFTER_VOTES_BEFORE_DECISION_LOG"
    AFTER_DECISION_LOG_BEFORE_SEND = "AFTER_DECISION_LOG_BEFORE_SEND"


@dataclass(frozen=True)
class Posting:
    """One account change on one participant. A negative delta is a debit."""

    account: str
    delta: Money


@dataclass(frozen=True)
class LogRecord:
    tx_id: str
    kind: str
    detail: str

    def __str__(self) -> str:
        return f"{self.tx_id} {self.kind}" + (f" {self.detail}" if self.detail else "")

    __repr__ = __str__


class DurableLog:
    """Append-only durable log. Append is the durability point: a record in the list survives a crash."""

    def __init__(self) -> None:
        self._records: list[LogRecord] = []

    def append(self, tx_id: str, kind: str, detail: str = "") -> None:
        self._records.append(LogRecord(tx_id, kind, detail))

    def records(self) -> list[LogRecord]:
        return list(self._records)

    def size(self) -> int:
        return len(self._records)


class CrashError(RuntimeError):
    """Raised by an injected crash point. The simulation catches it where the real process would die."""

    def __init__(self, where: str) -> None:
        super().__init__(f"crash at {where}")


# ---------------------------------------------------------------- participant


class Ledger:
    """A participant: one ledger with its own balances, locks, and log.

    prepare() is a promise: once it has voted YES it can no longer change its mind, so it holds the
    lock and keeps the posting durable until the coordinator tells it the decision.
    """

    def __init__(self, name: str, opening_balances: dict[str, Money], log: DurableLog | None = None) -> None:
        self.name = name
        self._initial = dict(opening_balances)
        self._balances = dict(opening_balances)
        self._prepared: dict[str, Posting] = {}
        self._states: dict[str, TxState] = {}
        self._locked: set[str] = set()
        self._log = log if log is not None else DurableLog()

    def balance(self, account: str) -> Money:
        return self._balances[account]

    def state_of(self, tx_id: str) -> TxState:
        return self._states.get(tx_id, TxState.NONE)

    def is_locked(self, account: str) -> bool:
        return account in self._locked

    def log_records(self) -> list[LogRecord]:
        return self._log.records()

    def in_doubt(self) -> set[str]:
        """Prepared and waiting. Non-empty means this ledger is blocked on the coordinator."""
        return set(self._prepared)

    def prepare(self, tx_id: str, posting: Posting) -> Vote:
        """Phase 1. Validate, lock, log PREPARED, then vote. The log write comes before the vote leaves."""
        if self._states.get(tx_id) == TxState.PREPARED:
            return Vote.YES  # duplicate prepare: same answer
        if tx_id in self._states:
            return Vote.NO  # already decided: too late
        current = self._balances.get(posting.account)
        if current is None or posting.account in self._locked:
            why = "unknown account" if current is None else "account locked"
            self._log.append(tx_id, "ABORTED", f"vote NO: {why}")
            self._states[tx_id] = TxState.ABORTED
            return Vote.NO
        if current.plus(posting.delta).is_negative():
            self._log.append(tx_id, "ABORTED", f"vote NO: insufficient funds in {posting.account}")
            self._states[tx_id] = TxState.ABORTED
            return Vote.NO
        self._locked.add(posting.account)
        self._prepared[tx_id] = posting
        self._states[tx_id] = TxState.PREPARED
        self._log.append(tx_id, "PREPARED", f"{posting.account} {posting.delta}")
        return Vote.YES

    def commit(self, tx_id: str) -> None:
        """Phase 2, commit. Idempotent: a second COMMIT for the same tx is acknowledged, not re-applied."""
        state = self.state_of(tx_id)
        if state == TxState.COMMITTED:
            return
        if state != TxState.PREPARED:
            raise RuntimeError(f"{self.name}: commit of {tx_id} in state {state.value}")
        posting = self._prepared.pop(tx_id)
        self._balances[posting.account] = self._balances[posting.account].plus(posting.delta)
        self._locked.discard(posting.account)
        self._states[tx_id] = TxState.COMMITTED
        self._log.append(tx_id, "COMMITTED")

    def abort(self, tx_id: str) -> None:
        """Phase 2, abort. Idempotent, and legal from NONE (the coordinator may abort a tx we never prepared)."""
        state = self.state_of(tx_id)
        if state == TxState.ABORTED:
            return
        if state == TxState.COMMITTED:
            raise RuntimeError(f"{self.name}: cannot abort committed {tx_id}")
        posting = self._prepared.pop(tx_id, None)
        if posting is not None:
            self._locked.discard(posting.account)
        self._states[tx_id] = TxState.ABORTED
        self._log.append(tx_id, "ABORTED")

    def crash_and_recover(self) -> "Ledger":
        """Lose all memory, then rebuild from the log. PREPARED records with no later decision come back
        as in-doubt: locks re-taken, posting held. The participant cannot resolve them on its own."""
        fresh = Ledger(self.name, self._initial, self._log)
        pending: dict[str, Posting] = {}
        for r in self._log.records():
            if r.kind == "PREPARED":
                account, amount, code = r.detail.split(" ")
                pending[r.tx_id] = Posting(account, _parse_money(amount, code))
                fresh._states[r.tx_id] = TxState.PREPARED
            elif r.kind == "COMMITTED":
                p = pending.pop(r.tx_id)
                fresh._balances[p.account] = fresh._balances[p.account].plus(p.delta)
                fresh._states[r.tx_id] = TxState.COMMITTED
            elif r.kind == "ABORTED":
                pending.pop(r.tx_id, None)
                fresh._states[r.tx_id] = TxState.ABORTED
            else:
                raise RuntimeError(f"unknown record {r}")
        for tx_id, p in pending.items():
            fresh._prepared[tx_id] = p
            fresh._locked.add(p.account)
        return fresh

    def total(self, currency: Currency) -> Money:
        acc = Money(0, currency)
        for m in self._balances.values():
            acc = acc.plus(m)
        return acc


def _parse_money(amount: str, code: str) -> Money:
    negative = amount.startswith("-")
    s = amount[1:] if negative else amount
    exponent = len(s) - s.index(".") - 1 if "." in s else 0
    minor = int(s.replace(".", ""))
    return Money(-minor if negative else minor, Currency(code, exponent))


# ---------------------------------------------------------------- network


class SimNetwork:
    """Deterministic network between the coordinator and its participants. A partitioned node never
    answers; the caller waits out the timeout and the clock advances. Every call is counted."""

    def __init__(self, clock: SimClock, latency_ms: int, timeout_ms: int) -> None:
        self._clock = clock
        self.latency_ms = latency_ms
        self.timeout_ms = timeout_ms
        self._nodes: dict[str, Ledger] = {}
        self._partitioned: set[str] = set()
        self._crash_after_vote: set[str] = set()
        self.messages_sent = 0
        self.timeouts = 0

    def register(self, ledger: Ledger) -> None:
        self._nodes[ledger.name] = ledger

    def replace(self, recovered: Ledger) -> None:
        """Replace a node after it crashed and recovered: the new object has the same name."""
        self._nodes[recovered.name] = recovered

    def partition(self, node: str) -> None:
        self._partitioned.add(node)

    def heal(self, node: str) -> None:
        self._partitioned.discard(node)

    def crash_after_vote(self, node: str) -> None:
        """The node answers the next prepare and then goes dark: a participant that crashes right after voting."""
        self._crash_after_vote.add(node)

    def _reachable(self, node: str) -> bool:
        self.messages_sent += 1
        if node in self._partitioned:
            self._clock.advance(self.timeout_ms)
            self.timeouts += 1
            return False
        self._clock.advance(self.latency_ms)
        return True

    def prepare(self, node: str, tx_id: str, posting: Posting) -> Vote | None:
        """None means the request timed out; the coordinator must treat that as NO."""
        if not self._reachable(node):
            return None
        vote = self._nodes[node].prepare(tx_id, posting)
        if node in self._crash_after_vote:
            self._crash_after_vote.discard(node)
            self._partitioned.add(node)
        return vote

    def decide(self, node: str, tx_id: str, decision: Decision) -> bool:
        """False means no acknowledgement arrived; the coordinator must retry later."""
        if not self._reachable(node):
            return False
        if decision == Decision.COMMIT:
            self._nodes[node].commit(tx_id)
        else:
            self._nodes[node].abort(tx_id)
        return True


# ---------------------------------------------------------------- coordinator


@dataclass(frozen=True)
class Outcome:
    tx_id: str
    decision: Decision
    complete: bool
    reason: str

    def __str__(self) -> str:
        return f"{self.tx_id} -> {self.decision.value}" + ("" if self.complete else f" (incomplete: {self.reason})")


class Coordinator:
    """The coordinator. Its log is the only place the atomic decision lives. The commit point of the
    whole protocol is the single append of a DECISION record; everything after that is delivery."""

    def __init__(self, net: SimNetwork) -> None:
        self._log = DurableLog()
        self._net = net
        self._participants: dict[str, dict[str, Posting]] = {}
        self._decisions: dict[str, Decision] = {}
        self._unacked: dict[str, set[str]] = {}
        self.crash_point = CrashPoint.NONE

    def log_records(self) -> list[LogRecord]:
        return self._log.records()

    def pending_transactions(self) -> set[str]:
        return set(self._unacked)

    def decision_of(self, tx_id: str) -> Decision | None:
        return self._decisions.get(tx_id)

    def transfer(self, tx_id: str, src: Ledger, src_account: str, dst: Ledger, dst_account: str, amount: Money) -> Outcome:
        """Move amount from one account on one ledger to another account on another ledger, atomically."""
        postings = {src.name: Posting(src_account, amount.negate()), dst.name: Posting(dst_account, amount)}
        return self.run(tx_id, postings)

    def run(self, tx_id: str, postings: dict[str, Posting]) -> Outcome:
        self._participants[tx_id] = postings
        self._log.append(tx_id, "BEGIN", ",".join(postings))

        # Phase 1: collect votes. Any NO or any timeout means ABORT.
        decision, reason = Decision.COMMIT, "all voted YES"
        for node, posting in postings.items():
            vote = self._net.prepare(node, tx_id, posting)
            if vote is None:
                decision, reason = Decision.ABORT, f"{node} timed out in prepare"
                break
            if vote == Vote.NO:
                decision, reason = Decision.ABORT, f"{node} voted NO"
                break

        if self.crash_point == CrashPoint.AFTER_VOTES_BEFORE_DECISION_LOG:
            raise CrashError("after votes, before decision log")

        # The commit point. Before this append the outcome is undecided; after it, it is fixed forever.
        self._log.append(tx_id, "DECISION", decision.value)
        self._decisions[tx_id] = decision
        self._unacked[tx_id] = set(postings)

        if self.crash_point == CrashPoint.AFTER_DECISION_LOG_BEFORE_SEND:
            raise CrashError("after decision log, before send")

        # Phase 2: deliver the decision until every participant acknowledges.
        complete = self.deliver(tx_id)
        if not complete:
            reason = f"{reason}; awaiting acks from {sorted(self._unacked[tx_id])}"
        return Outcome(tx_id, decision, complete, reason)

    def deliver(self, tx_id: str) -> bool:
        """One delivery round: send the decision to every unacked participant; log END when none remain."""
        waiting = self._unacked.get(tx_id)
        if waiting is None:
            return True
        decision = self._decisions[tx_id]
        for node in sorted(waiting):
            if self._net.decide(node, tx_id, decision):
                waiting.discard(node)
        if not waiting:
            del self._unacked[tx_id]
            self._log.append(tx_id, "END")
            return True
        return False

    def retry_pending(self) -> int:
        """Retry delivery for every transaction still waiting on acknowledgements."""
        return sum(1 for tx_id in list(self._unacked) if self.deliver(tx_id))

    @staticmethod
    def recover(crashed: "Coordinator", net: SimNetwork) -> "Coordinator":
        """Restart with nothing but the log. BEGIN without DECISION: presumed abort, because no commit
        record exists, so no participant can have been told COMMIT. DECISION without END: redeliver."""
        fresh = Coordinator(net)
        begun: dict[str, list[str]] = {}
        ended: set[str] = set()
        for r in crashed._log.records():
            fresh._log.append(r.tx_id, r.kind, r.detail)
            if r.kind == "BEGIN":
                begun[r.tx_id] = r.detail.split(",")
            elif r.kind == "DECISION":
                fresh._decisions[r.tx_id] = Decision(r.detail)
            elif r.kind == "END":
                ended.add(r.tx_id)
        for tx_id, nodes in begun.items():
            if tx_id in ended:
                continue
            if tx_id not in fresh._decisions:
                fresh._log.append(tx_id, "DECISION", Decision.ABORT.value)  # presumed abort
                fresh._decisions[tx_id] = Decision.ABORT
            fresh._unacked[tx_id] = set(nodes)
            fresh.deliver(tx_id)
        return fresh


# ---------------------------------------------------------------- demo


def _show(a: Ledger, b: Ledger) -> None:
    total = a.total(USD).plus(b.total(USD))
    print(f"  {a.name}: alice={a.balance('alice')} locked={str(a.is_locked('alice')).lower()}"
          f" | {b.name}: bob={b.balance('bob')} locked={str(b.is_locked('bob')).lower()} | total={total}")


def main() -> None:
    clock = SimClock(0)
    net = SimNetwork(clock, 5, 1_000)
    bank_a = Ledger("bank-a", {"alice": usd("500.00")})
    bank_b = Ledger("bank-b", {"bob": usd("100.00")})
    net.register(bank_a)
    net.register(bank_b)
    coord = Coordinator(net)

    print("== 1. Happy path: 150.00 USD alice@bank-a -> bob@bank-b ==")
    print(f"  {coord.transfer('tx-1', bank_a, 'alice', bank_b, 'bob', usd('150.00'))}")
    _show(bank_a, bank_b)
    print(f"  coordinator log: {coord.log_records()}")

    print("\n== 2. Participant votes NO: 900.00 exceeds alice's balance ==")
    print(f"  {coord.transfer('tx-2', bank_a, 'alice', bank_b, 'bob', usd('900.00'))}")
    _show(bank_a, bank_b)

    print("\n== 3. bank-b unreachable during prepare: timeout is a NO ==")
    net.partition("bank-b")
    before = clock.now()
    print(f"  {coord.transfer('tx-3', bank_a, 'alice', bank_b, 'bob', usd('10.00'))}")
    print(f"  waited {clock.now() - before} ms; bank-a state for tx-3 = {bank_a.state_of('tx-3').value}")
    net.heal("bank-b")
    _show(bank_a, bank_b)

    print("\n== 4. Coordinator dies after both YES votes, before logging the decision ==")
    coord.crash_point = CrashPoint.AFTER_VOTES_BEFORE_DECISION_LOG
    try:
        coord.transfer("tx-4", bank_a, "alice", bank_b, "bob", usd("25.00"))
    except CrashError as exc:
        print(f"  coordinator: {exc}")
    print(f"  bank-a in doubt: {sorted(bank_a.in_doubt())}, bank-b in doubt: {sorted(bank_b.in_doubt())}")
    _show(bank_a, bank_b)
    vote = bank_a.prepare("tx-5", Posting("alice", usd("-1.00")))
    print(f"  another coordinator tries alice while she is locked: {vote.value}  <- this is the blocking problem")
    print(f"  bank-b restarts from its own log and is still in doubt: {sorted(bank_b.crash_and_recover().in_doubt())}")
    coord = Coordinator.recover(coord, net)
    print(f"  recovered coordinator: presumed abort -> tx-4 = {coord.decision_of('tx-4').value}")
    _show(bank_a, bank_b)

    print("\n== 5. Coordinator dies after logging COMMIT, before telling anyone ==")
    coord.crash_point = CrashPoint.AFTER_DECISION_LOG_BEFORE_SEND
    try:
        coord.transfer("tx-6", bank_a, "alice", bank_b, "bob", usd("40.00"))
    except CrashError as exc:
        print(f"  coordinator: {exc}")
    print(f"  participants still PREPARED: {bank_a.state_of('tx-6').value}, {bank_b.state_of('tx-6').value}")
    coord = Coordinator.recover(coord, net)
    print(f"  recovered coordinator redelivers {coord.decision_of('tx-6').value}"
          f" -> {bank_a.state_of('tx-6').value}, {bank_b.state_of('tx-6').value}")
    _show(bank_a, bank_b)

    print("\n== 6. bank-b votes YES then crashes: the decision is final, delivery retries ==")
    net.crash_after_vote("bank-b")
    out = coord.transfer("tx-7", bank_a, "alice", bank_b, "bob", usd("5.00"))
    print(f"  {out}")
    print(f"  bank-a already {bank_a.state_of('tx-7').value}, bank-b crashed while {bank_b.state_of('tx-7').value}")
    bank_b = bank_b.crash_and_recover()
    net.replace(bank_b)
    net.heal("bank-b")
    print(f"  bank-b recovered from its log: in doubt {sorted(bank_b.in_doubt())}, bob locked={str(bank_b.is_locked('bob')).lower()}")
    print(f"  retry_pending finished {coord.retry_pending()} tx; bank-b now {bank_b.state_of('tx-7').value}")
    _show(bank_a, bank_b)
    print(f"\nmessages sent={net.messages_sent} timeouts={net.timeouts} clock={clock.now()} ms")


if __name__ == "__main__":
    main()
