"""Lesson: phases/06-replication-and-consensus/02-replication-lag-and-read-your-writes/docs/en.md
Replication Lag and Read-Your-Writes
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: Terry et al., "Session Guarantees for Weakly Consistent Replicated Data" (PDIS 1994); PostgreSQL docs,
         "Log-Shipping Standby Servers" and pg_current_wal_lsn(); Vogels, "Eventually Consistent" (CACM 2009).
Python 3.11+, standard library only. Deterministic: the only randomness is a seeded SplitMix64, time is a tick counter.
"""

from __future__ import annotations

import heapq
from dataclasses import dataclass
from enum import Enum
from typing import Optional

MASK64 = (1 << 64) - 1


class Rng:
    """SplitMix64 (Steele, Lea, Flood, OOPSLA 2014). Same stream for the same seed in every language, so a bug replays."""

    def __init__(self, seed: int) -> None:
        self.state = seed & MASK64

    def next_long(self) -> int:
        self.state = (self.state + 0x9E3779B97F4A7C15) & MASK64
        z = self.state
        z = ((z ^ (z >> 30)) * 0xBF58476D1CE4E5B9) & MASK64
        z = ((z ^ (z >> 27)) * 0x94D049BB133111EB) & MASK64
        return z ^ (z >> 31)

    def next_int(self, bound: int) -> int:
        return (self.next_long() >> 33) % bound


@dataclass(frozen=True)
class Currency:
    """ISO 4217 currency, as in Phase 10 Lesson 01. Only what this lesson needs: a code and an exponent for formatting."""

    code: str
    exponent: int

    def __post_init__(self) -> None:
        if self.code is None or len(self.code) != 3:
            raise ValueError("currency code must be three letters")
        if not 0 <= self.exponent <= 4:
            raise ValueError("exponent must be 0..4")


USD = Currency("USD", 2)
EUR = Currency("EUR", 2)


@dataclass(frozen=True)
class Money:
    """Integer minor units plus a currency; never a float. Copied from Phase 10 Lesson 01, trimmed to what the demo uses."""

    minor_units: int
    currency: Currency

    @staticmethod
    def of_minor(minor_units: int, currency: Currency) -> "Money":
        return Money(minor_units, currency)

    def plus(self, other: "Money") -> "Money":
        if self.currency != other.currency:
            raise ValueError(f"cannot combine {self.currency.code} with {other.currency.code}")
        return Money(self.minor_units + other.minor_units, self.currency)

    def __str__(self) -> str:
        scale = 10 ** self.currency.exponent
        whole, frac = divmod(abs(self.minor_units), scale)
        sign = "-" if self.minor_units < 0 else ""
        if self.currency.exponent == 0:
            return f"{sign}{whole} {self.currency.code}"
        return f"{sign}{whole}.{frac:0{self.currency.exponent}d} {self.currency.code}"


@dataclass(frozen=True)
class LogRecord:
    """One record in the leader's replication log. The LSN (log sequence number) is its 1-based position; it only grows."""

    lsn: int
    key: str
    value: str

    def __str__(self) -> str:
        return f"#{self.lsn} {self.key}={self.value}"


class Leader:
    """The single writer. Every write appends one record and returns its LSN, which is the token a client carries afterwards."""

    def __init__(self) -> None:
        self.log: list[LogRecord] = []
        self.data: dict[str, str] = {}

    def lsn(self) -> int:
        return len(self.log)

    def write(self, key: str, value: str) -> int:
        r = LogRecord(len(self.log) + 1, key, value)
        self.log.append(r)
        self.data[key] = value
        return r.lsn

    def read(self, key: str) -> Optional[str]:
        return self.data.get(key)

    def since(self, after_lsn: int) -> list[LogRecord]:
        """Records with LSN strictly greater than after_lsn: what a replica that has applied through after_lsn still needs."""
        if after_lsn < 0 or after_lsn > len(self.log):
            raise ValueError(f"lsn out of range: {after_lsn}")
        return list(self.log[after_lsn:])


class Replica:
    """An asynchronous replica. Records arrive after a seeded delay in [min_delay, max_delay] ticks. With apply_in_order
    the replica applies only the contiguous prefix of the log (so its state is always some prefix of the leader's
    history); without it the replica applies each record the moment it arrives, which violates consistent prefix."""

    def __init__(self, id: int, rng: Rng, min_delay: int, max_delay: int, apply_in_order: bool) -> None:
        if min_delay < 1 or max_delay < min_delay:
            raise ValueError("delays must satisfy 1 <= min <= max")
        self.id = id
        self.name = f"r{id}"
        self._rng = rng
        self.min_delay = min_delay
        self.max_delay = max_delay
        self.apply_in_order = apply_in_order
        self.data: dict[str, str] = {}
        self._inbox: list[tuple[int, int, LogRecord]] = []   # heap of (arrive_at, lsn, record)
        self._arrived: dict[int, LogRecord] = {}             # reorder buffer keyed by lsn
        self._applied_set: set[int] = set()
        # Highest LSN such that every record 1..applied_lsn has been applied. A session token is compared with this.
        self.applied_lsn = 0
        self.out_of_order_applies = 0
        self.applied = 0

    def receive(self, r: LogRecord, now: int) -> None:
        """The leader ships a record; it lands in the inbox with a seeded delay. Nothing is applied until tick()."""
        delay = self.min_delay + self._rng.next_int(self.max_delay - self.min_delay + 1)
        heapq.heappush(self._inbox, (now + delay, r.lsn, r))

    def tick(self, now: int) -> None:
        """Move everything that has arrived into the reorder buffer, then apply. In-order mode applies only while the
        next expected LSN is present; on-arrival mode applies the whole buffer in LSN order."""
        while self._inbox and self._inbox[0][0] <= now:
            _, lsn, record = heapq.heappop(self._inbox)
            self._arrived[lsn] = record
        if self.apply_in_order:
            while self.applied_lsn + 1 in self._arrived:
                self._apply(self._arrived.pop(self.applied_lsn + 1))
        else:
            for lsn in sorted(self._arrived):
                self._apply(self._arrived.pop(lsn))

    def _apply(self, r: LogRecord) -> None:
        if r.lsn != self.applied_lsn + 1:
            self.out_of_order_applies += 1
        self.data[r.key] = r.value
        self._applied_set.add(r.lsn)
        self.applied += 1
        while self.applied_lsn + 1 in self._applied_set:
            self.applied_lsn += 1

    def read(self, key: str) -> Optional[str]:
        return self.data.get(key)

    def pending(self) -> int:
        """Records shipped but not yet applied: still in flight, or arrived and waiting for a gap to fill."""
        return len(self._inbox) + len(self._arrived)

    def buffered(self) -> int:
        """Size of the reorder buffer: records that arrived early and wait for an earlier LSN. Always 0 on arrival."""
        return len(self._arrived)

    def lag_behind(self, leader: Leader) -> int:
        return leader.lsn() - self.applied_lsn


class ReadMode(Enum):
    """Which freshness rule a read follows. All but LEADER are implemented by one method: a floor on the replica's LSN."""

    ANY_REPLICA = "ANY_REPLICA"
    READ_YOUR_WRITES = "READ_YOUR_WRITES"
    MONOTONIC = "MONOTONIC"
    SESSION = "SESSION"
    LEADER = "LEADER"


class Session:
    """Per-client state that makes session guarantees possible. last_write_lsn is the token from the client's last
    write; last_read_lsn is the freshest point it has observed. Both are plain ints: cheap to carry in a cookie."""

    def __init__(self, id: int) -> None:
        self.id = id
        self.last_write_lsn = 0
        self.last_read_lsn = 0

    def floor(self, mode: ReadMode) -> int:
        if mode in (ReadMode.ANY_REPLICA, ReadMode.LEADER):
            return 0
        if mode == ReadMode.READ_YOUR_WRITES:
            return self.last_write_lsn
        if mode == ReadMode.MONOTONIC:
            return self.last_read_lsn
        return max(self.last_write_lsn, self.last_read_lsn)


@dataclass(frozen=True)
class ReadResult:
    """What a read returns, plus where it came from. The demo prints served_by to make the routing decision visible."""

    value: Optional[str]
    served_by: str
    at_lsn: int
    redirected: bool

    def __str__(self) -> str:
        v = "<nothing>" if self.value is None else self.value
        return f"{v}  (served by {self.served_by} at lsn {self.at_lsn}{', redirected' if self.redirected else ''})"


class ReplicatedStore:
    """Leader plus replicas plus a tick clock. The load balancer is a seeded random pick, like a round-robin DNS answer."""

    def __init__(self, seed: int, apply_in_order: bool, min_delays: list[int], max_delays: list[int]) -> None:
        if len(min_delays) != len(max_delays) or not min_delays:
            raise ValueError("one delay range per replica")
        self.leader = Leader()
        self.replicas: list[Replica] = []
        self._balancer = Rng(seed)
        self._now = 0
        self.reads = 0
        self.stale_reads = 0
        self.redirects = 0
        self.leader_reads = 0
        for i in range(len(max_delays)):
            replica_seed = (seed * 7919 + 1000003 * (i + 1)) & MASK64
            self.replicas.append(Replica(i, Rng(replica_seed), min_delays[i], max_delays[i], apply_in_order))

    @staticmethod
    def of(seed: int, apply_in_order: bool, *max_delays: int) -> "ReplicatedStore":
        return ReplicatedStore(seed, apply_in_order, [1] * len(max_delays), list(max_delays))

    def now(self) -> int:
        return self._now

    def tick(self) -> None:
        self._now += 1
        for r in self.replicas:
            r.tick(self._now)

    def run(self, ticks: int) -> None:
        for _ in range(ticks):
            self.tick()

    def write(self, key: str, value: str) -> int:
        """Write through the leader and fan the record out. Returns the LSN; a client that keeps it can read its write."""
        lsn = self.leader.write(key, value)
        r = self.leader.log[lsn - 1]
        for rep in self.replicas:
            rep.receive(r, self._now)
        return lsn

    def write_session(self, s: Session, key: str, value: str) -> int:
        """Write on behalf of a session: the session remembers the LSN (Java overloads write(Session, ...))."""
        lsn = self.write(key, value)
        s.last_write_lsn = lsn
        return lsn

    def pick_replica(self, mode: ReadMode, s: Session) -> Replica:
        if mode in (ReadMode.MONOTONIC, ReadMode.SESSION):
            return self.replicas[s.id % len(self.replicas)]   # sticky
        return self.replicas[self._balancer.next_int(len(self.replicas))]

    def read(self, s: Session, key: str, mode: ReadMode) -> ReadResult:
        """The whole mechanism: pick a replica, and if it has not applied through the floor the session demands, find
        one that has, falling back to the leader. The floor is 0 for ANY_REPLICA, which is the bug."""
        self.reads += 1
        if mode == ReadMode.LEADER:
            self.leader_reads += 1
            return self._finish(s, ReadResult(self.leader.read(key), "leader", self.leader.lsn(), False))
        floor = s.floor(mode)
        chosen = self.pick_replica(mode, s)
        if chosen.applied_lsn >= floor:
            return self._finish(s, ReadResult(chosen.read(key), chosen.name, chosen.applied_lsn, False))
        for r in self.replicas:
            if r.applied_lsn >= floor:
                self.redirects += 1
                return self._finish(s, ReadResult(r.read(key), r.name, r.applied_lsn, True))
        self.redirects += 1
        self.leader_reads += 1
        return self._finish(s, ReadResult(self.leader.read(key), "leader", self.leader.lsn(), True))

    def _finish(self, s: Session, result: ReadResult) -> ReadResult:
        if result.at_lsn < self.leader.lsn():
            self.stale_reads += 1
        s.last_read_lsn = max(s.last_read_lsn, result.at_lsn)
        return result

    def catch_up(self, r: Replica) -> int:
        """Resynchronise a replica from its own watermark: the LSN is also the catch-up cursor."""
        missing = self.leader.since(r.applied_lsn)
        shipped = 0
        for rec in missing:
            if r.pending() == 0 or not r.apply_in_order:
                r.receive(rec, self._now)
                shipped += 1
        return shipped

    def lag_report(self) -> str:
        parts = [f"leader lsn={self.leader.lsn()}"]
        for r in self.replicas:
            parts.append(f"{r.name} applied={r.applied_lsn} (lag {r.lag_behind(self.leader)})")
        return ", ".join(parts)


class AnomalyDetector:
    """Watches a sequence of reads by one session and reports the two anomalies this lesson is about.
    - a monotonic violation: a later read was served at a lower LSN than an earlier one (time went backwards)
    - a consistent-prefix violation: the reader saw a later record's key but not an earlier one it depends on"""

    def __init__(self) -> None:
        self.monotonic_violations = 0
        self.prefix_violations = 0
        self._high_water = 0

    def observe(self, r: ReadResult) -> None:
        if r.at_lsn < self._high_water:
            self.monotonic_violations += 1
        self._high_water = max(self._high_water, r.at_lsn)

    def observe_pair(self, earlier: Optional[str], later: Optional[str]) -> None:
        """later was written after earlier. Seeing later without earlier cannot happen on any prefix of the log."""
        if later is not None and earlier is None:
            self.prefix_violations += 1


def main() -> None:
    print("== The bug: a customer pays, refreshes, and sees no payment (seed 3, replicas with max delay 4, 12, 40) ==")
    store = ReplicatedStore.of(3, True, 4, 12, 40)
    alice = Session(1)
    amount = Money.of_minor(4999, USD)
    lsn = store.write_session(alice, "payment:p-1001", f"CAPTURED {amount}")
    print(f"t={store.now()}  POST /payments -> leader appends #{lsn}, returns 201 with lsn token {lsn}")
    store.run(2)
    print(f"t={store.now()}  {store.lag_report()}")
    for _ in range(3):
        r = store.read(alice, "payment:p-1001", ReadMode.ANY_REPLICA)
        print(f"t={store.now()}  GET /payments/p-1001 (any replica)       -> {r}")
        store.tick()

    print()
    print("== Read-your-writes: the session carries the LSN of its last write and reads only from replicas at or past it ==")
    for _ in range(3):
        r = store.read(alice, "payment:p-1001", ReadMode.READ_YOUR_WRITES)
        print(f"t={store.now()}  GET /payments/p-1001 (floor lsn {alice.floor(ReadMode.READ_YOUR_WRITES)}) -> {r}")
        store.tick()
    bob = Session(2)
    bob_sees = store.read(bob, "payment:p-1001", ReadMode.READ_YOUR_WRITES)
    print(f"t={store.now()}  bob (never wrote, floor 0) reads p-1001         -> {bob_sees}   <- allowed: he has no write to see")

    print()
    print("== Monotonic reads: without a sticky replica, a refresh can travel back in time ==")
    tt = ReplicatedStore.of(5, True, 2, 30)
    carol = Session(0)
    tt.write_session(carol, "payment:p-2002", f"CAPTURED {Money.of_minor(1250, USD)}")
    naive = AnomalyDetector()
    for _ in range(6):
        tt.run(2)
        r = tt.read(carol, "payment:p-2002", ReadMode.ANY_REPLICA)
        naive.observe(r)
        print(f"t={tt.now()}  refresh (any replica) -> {r}")
    print(f"time-travel anomalies seen by the naive reader: {naive.monotonic_violations}")
    mt = ReplicatedStore.of(5, True, 2, 30)
    dave = Session(1)   # sticky to r1, the slow one, which makes the floor do the work
    mt.write_session(dave, "payment:p-2002", f"CAPTURED {Money.of_minor(1250, USD)}")
    guarded = AnomalyDetector()
    for _ in range(6):
        mt.run(2)
        r = mt.read(dave, "payment:p-2002", ReadMode.SESSION)
        guarded.observe(r)
        print(f"t={mt.now()}  refresh (session floor {dave.floor(ReadMode.SESSION)}) -> {r}")
    print(f"time-travel anomalies seen by the session reader: {guarded.monotonic_violations}")

    print()
    print("== Consistent prefix: 'captured' must never be visible without 'authorized' (200 payments, seed 11) ==")
    for in_order in (False, True):
        cp = ReplicatedStore.of(11, in_order, 20)
        det = AnomalyDetector()
        for p in range(1, 201):
            cp.write(f"payment:p-{p}:auth", "AUTHORIZED")
            cp.write(f"payment:p-{p}:capture", "CAPTURED")
            for _ in range(10):
                cp.tick()
                r0 = cp.replicas[0]
                det.observe_pair(r0.read(f"payment:p-{p}:auth"), r0.read(f"payment:p-{p}:capture"))
        cp.run(50)
        label = "in LSN order" if in_order else "on arrival"
        r0 = cp.replicas[0]
        print(f"replica applies {label:<13s} -> out-of-order applies={r0.out_of_order_applies}, "
              f"'captured before authorized' observed {det.prefix_violations} times, "
              f"final appliedLsn={r0.applied_lsn} of {cp.leader.lsn()}")

    print()
    print("== 20 seeds x 300 write-then-read pairs, 3 replicas (max delay 5, 15, 45): stale-read rate by read mode ==")
    for mode in (ReadMode.ANY_REPLICA, ReadMode.READ_YOUR_WRITES, ReadMode.SESSION, ReadMode.LEADER):
        stale = total = redirects = leader_reads = monotonic = 0
        for seed in range(1, 21):
            s = ReplicatedStore.of(seed, True, 5, 15, 45)
            user = Session(seed)
            det = AnomalyDetector()
            for i in range(1, 301):
                s.write_session(user, "k", f"v{i}")
                s.run(1 + (i % 7))
                r = s.read(user, "k", mode)
                det.observe(r)
                total += 1
                if r.value != f"v{i}":
                    stale += 1
            redirects += s.redirects
            leader_reads += s.leader_reads
            monotonic += det.monotonic_violations
        print(f"{mode.name:<17s} stale own-write reads {stale:4d}/{total}  redirects {redirects:4d}  "
              f"leader reads {leader_reads:4d}  time-travel {monotonic}")
    print("the token costs one long per session; the leader took the hit only when no replica had caught up.")


if __name__ == "__main__":
    main()
