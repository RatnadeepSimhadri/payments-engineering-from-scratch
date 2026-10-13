"""Lesson: phases/05-distributed-foundations/03-lamport-and-vector-clocks/docs/en.md
Time, Order, and Clocks: Lamport and Vector Clocks
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: Lamport, "Time, Clocks, and the Ordering of Events in a Distributed System" (CACM 21(7), 1978);
         Fidge (1988) and Mattern (1989) on vector clocks. Python 3.11+, standard library only.
Same design as the Java and Go versions.
"""

from __future__ import annotations

import heapq
from dataclasses import dataclass, field
from enum import Enum
from typing import Optional


# --- Money, copied from Phase 10 Lesson 01 (only what this lesson needs) ---------------------------------


@dataclass(frozen=True)
class Currency:
    """ISO 4217 currency: a three-letter code and the number of decimals in its minor unit."""

    code: str
    exponent: int

    def minor_units_per_major(self) -> int:
        return 10 ** self.exponent


USD = Currency("USD", 2)


@dataclass(frozen=True)
class Money:
    """Integer minor units plus a currency. Never a float."""

    minor_units: int
    currency: Currency

    def __post_init__(self) -> None:
        if not isinstance(self.minor_units, int) or isinstance(self.minor_units, bool):
            raise TypeError("minor_units must be an int; never construct Money from a float")

    @staticmethod
    def of_minor(minor_units: int, currency: Currency) -> "Money":
        return Money(minor_units, currency)

    def plus(self, other: "Money") -> "Money":
        if self.currency != other.currency:
            raise ValueError("currency mismatch")
        return Money(self.minor_units + other.minor_units, self.currency)

    def __str__(self) -> str:
        scale = self.currency.minor_units_per_major()
        magnitude = abs(self.minor_units)
        whole = str(magnitude // scale)
        frac = "" if self.currency.exponent == 0 else f"{magnitude % scale:0{self.currency.exponent}d}"
        sign = "-" if self.minor_units < 0 else ""
        return f"{sign}{whole}{'.' + frac if frac else ''} {self.currency.code}"


# --- Clocks ---------------------------------------------------------------------------------------------


class Relation(Enum):
    """How two vector clocks relate. Lamport timestamps never produce CONCURRENT: they are a total order."""

    BEFORE = "BEFORE"
    AFTER = "AFTER"
    EQUAL = "EQUAL"
    CONCURRENT = "CONCURRENT"


@dataclass(frozen=True, order=True)
class LamportTimestamp:
    """A Lamport time plus the tie-break that makes it a total order: compare time, then node id.

    `order=True` on a dataclass compares fields in declaration order, which is exactly (time, node_id).
    """

    time: int
    node_id: str

    def __str__(self) -> str:
        return f"{self.time}@{self.node_id}"


class LamportClock:
    """Lamport's scalar clock. Three rules: tick on a local event, tick before send, max(local, remote) + 1 on receive."""

    def __init__(self) -> None:
        self._time = 0

    def time(self) -> int:
        return self._time

    def tick(self) -> int:
        """Rule 1: a local event advances the clock by exactly one."""
        self._time += 1
        return self._time

    def send(self) -> int:
        """Rule 2: sending is a local event; the value returned is what travels in the message."""
        return self.tick()

    def receive(self, remote_time: int) -> int:
        """Rule 3: on receive, jump past the sender's clock so the receive event is later than the send."""
        self._time = max(self._time, remote_time) + 1
        return self._time


@dataclass(frozen=True)
class VectorClock:
    """A vector clock over a fixed cluster of n nodes. Immutable: every operation returns a new clock.

    Component i counts the events node i has performed that this clock has observed.
    """

    counters: tuple[int, ...]

    @staticmethod
    def zero(size: int) -> "VectorClock":
        return VectorClock(tuple([0] * size))

    @staticmethod
    def of(*counters: int) -> "VectorClock":
        return VectorClock(tuple(counters))

    def size(self) -> int:
        return len(self.counters)

    def get(self, node: int) -> int:
        return self.counters[node]

    def increment(self, node: int) -> "VectorClock":
        """A local event on `node`: that component advances by one, nothing else changes."""
        next_counters = list(self.counters)
        next_counters[node] += 1
        return VectorClock(tuple(next_counters))

    def merge(self, other: "VectorClock") -> "VectorClock":
        """Componentwise maximum: everything either side has observed, the result has observed."""
        self._require_same_size(other)
        return VectorClock(tuple(max(a, b) for a, b in zip(self.counters, other.counters)))

    def compare(self, other: "VectorClock") -> Relation:
        """The partial order. BEFORE means self happened-before other; CONCURRENT means neither caused the other."""
        self._require_same_size(other)
        any_less = any(a < b for a, b in zip(self.counters, other.counters))
        any_greater = any(a > b for a, b in zip(self.counters, other.counters))
        if any_less and any_greater:
            return Relation.CONCURRENT
        if any_less:
            return Relation.BEFORE
        if any_greater:
            return Relation.AFTER
        return Relation.EQUAL

    def happens_before(self, other: "VectorClock") -> bool:
        return self.compare(other) == Relation.BEFORE

    def concurrent_with(self, other: "VectorClock") -> bool:
        return self.compare(other) == Relation.CONCURRENT

    def _require_same_size(self, other: "VectorClock") -> None:
        if len(other.counters) != len(self.counters):
            raise ValueError(f"vector clocks must cover the same cluster: {len(self.counters)} vs {len(other.counters)}")

    def __str__(self) -> str:
        return "[" + ",".join(str(c) for c in self.counters) + "]"


# --- Events, messages, nodes -----------------------------------------------------------------------------


@dataclass(frozen=True)
class Event:
    """One thing that happened on one node, stamped by both clocks so the two can be compared side by side."""

    node_id: str
    description: str
    lamport: LamportTimestamp
    vector: VectorClock

    def __str__(self) -> str:
        return f"{str(self.lamport):<11} {str(self.vector):<8} {self.node_id:<9} {self.description}"


@dataclass(frozen=True)
class Message:
    """A message in flight. Carries the sender's Lamport time and vector clock as of the send event."""

    sender: str
    to: str
    payload: str
    lamport_time: int
    vector: VectorClock


class Node:
    """One service: owns both clocks and an append-only log of its events."""

    def __init__(self, node_id: str, index: int, cluster_size: int) -> None:
        self.id = node_id
        self.index = index
        self.lamport = LamportClock()
        self.vector = VectorClock.zero(cluster_size)
        self.log: list[Event] = []

    def local_event(self, description: str) -> Event:
        """A local posting: both clocks tick; nothing leaves the node."""
        t = self.lamport.tick()
        self.vector = self.vector.increment(self.index)
        return self._record(description, t)

    def send(self, to: str, payload: str) -> Message:
        """Sending is an event too; the message carries the clock values after the tick."""
        t = self.lamport.send()
        self.vector = self.vector.increment(self.index)
        self._record(f"send {payload} -> {to}", t)
        return Message(self.id, to, payload, t, self.vector)

    def receive(self, m: Message) -> Event:
        """Receiving merges what the sender knew, then counts the receive as a local event."""
        t = self.lamport.receive(m.lamport_time)
        self.vector = self.vector.merge(m.vector).increment(self.index)
        return self._record(f"recv {m.payload} <- {m.sender}", t)

    def _record(self, description: str, lamport_time: int) -> Event:
        e = Event(self.id, description, LamportTimestamp(lamport_time, self.id), self.vector)
        self.log.append(e)
        return e


# --- Deterministic network -------------------------------------------------------------------------------

_MASK64 = (1 << 64) - 1


@dataclass(order=True)
class _Pending:
    deliver_at: int
    seq: int
    message: Message = field(compare=False)


class SimulatedNetwork:
    """A deterministic message scheduler.

    Each send is assigned a delivery tick = now + 1 + delay, where delay comes from a 64-bit linear congruential
    generator seeded by the test. Messages are delivered in (delivery_tick, sequence) order, so two messages sent
    in one order can arrive in the other, and the same seed always produces the same schedule.
    """

    def __init__(self, seed: int, max_delay: int) -> None:
        if max_delay < 0:
            raise ValueError("max_delay must be >= 0")
        self._nodes: dict[str, Node] = {}
        self._queue: list[_Pending] = []
        self._max_delay = max_delay
        self._state = seed & _MASK64
        self._tick = 0
        self._seq = 0
        self._delivered = 0

    def add(self, node: Node) -> None:
        self._nodes[node.id] = node

    def node(self, node_id: str) -> Node:
        if node_id not in self._nodes:
            raise ValueError(f"unknown node {node_id}")
        return self._nodes[node_id]

    def next_delay(self) -> int:
        """Knuth's MMIX LCG constants, wrapped to 64 bits by hand because Python ints do not overflow."""
        self._state = (self._state * 6364136223846793005 + 1442695040888963407) & _MASK64
        return (self._state >> 33) % (self._max_delay + 1)

    def send(self, m: Message) -> None:
        self.node(m.to)
        heapq.heappush(self._queue, _Pending(self._tick + 1 + self.next_delay(), self._seq, m))
        self._seq += 1

    def deliver_one(self) -> Optional[Event]:
        """Deliver the earliest pending message. Returns the receive event, or None if nothing is in flight."""
        if not self._queue:
            return None
        p = heapq.heappop(self._queue)
        self._tick = p.deliver_at
        self._delivered += 1
        return self.node(p.message.to).receive(p.message)

    def deliver_all(self) -> None:
        while self.deliver_one() is not None:
            pass

    def in_flight(self) -> int:
        return len(self._queue)

    def delivered(self) -> int:
        return self._delivered

    def tick(self) -> int:
        return self._tick

    def all_events(self) -> list[Event]:
        return [e for n in self._nodes.values() for e in n.log]


# --- Analysis helpers and the scenario -------------------------------------------------------------------

PAYMENTS, REFUNDS, LEDGER = "payments", "refunds", "ledger"
CAPTURE = Money.of_minor(4_999, USD)
REFUND = Money.of_minor(-4_999, USD)


def total_order(events: list[Event]) -> list[Event]:
    """The single global sequence a ledger can use: Lamport time, then node id. Consistent with causality."""
    return sorted(events, key=lambda e: e.lamport)


def concurrent_pairs(events: list[Event]) -> list[tuple[Event, Event]]:
    """Pairs of events that neither caused the other: the candidates for a write-write conflict."""
    pairs = []
    for i in range(len(events)):
        for j in range(i + 1, len(events)):
            if events[i].vector.concurrent_with(events[j].vector):
                pairs.append((events[i], events[j]))
    return pairs


def postings(events: list[Event]) -> list[Event]:
    """Only the ledger postings: send and receive events are bookkeeping, not account updates."""
    return [e for e in events if e.description.startswith("post ")]


def _cluster(seed: int) -> SimulatedNetwork:
    net = SimulatedNetwork(seed, 3)
    net.add(Node(PAYMENTS, 0, 3))
    net.add(Node(REFUNDS, 1, 3))
    net.add(Node(LEDGER, 2, 3))
    return net


def causal_refund_scenario(seed: int) -> SimulatedNetwork:
    """The healthy flow: refunds only refunds after hearing about the capture. Both clocks agree on the order."""
    net = _cluster(seed)
    payments, refunds, ledger = net.node(PAYMENTS), net.node(REFUNDS), net.node(LEDGER)

    payments.local_event(f"post captured {CAPTURE} pay_8f3")  # A
    net.send(payments.send(LEDGER, f"captured {CAPTURE} pay_8f3"))
    net.send(payments.send(REFUNDS, f"captured {CAPTURE} pay_8f3"))
    net.deliver_all()  # refunds now knows about A
    refunds.local_event(f"post refunded {REFUND} pay_8f3")  # B: caused by A
    net.send(refunds.send(LEDGER, f"refunded {REFUND} pay_8f3"))
    net.deliver_all()
    ledger.local_event(f"post settle pay_8f3 net {CAPTURE.plus(REFUND)}")  # C: after A and B
    return net


def concurrent_refund_scenario(seed: int) -> SimulatedNetwork:
    """The incident: the refund is issued before the capture notice arrives. Vector clocks reveal the concurrency."""
    net = _cluster(seed)
    payments, refunds, ledger = net.node(PAYMENTS), net.node(REFUNDS), net.node(LEDGER)

    payments.local_event(f"post captured {CAPTURE} pay_8f3")  # A
    net.send(payments.send(LEDGER, f"captured {CAPTURE} pay_8f3"))
    refunds.local_event(f"post refunded {REFUND} pay_8f3")  # B: concurrent with A
    net.send(refunds.send(LEDGER, f"refunded {REFUND} pay_8f3"))
    net.deliver_all()  # arrival order depends on seed
    ledger.local_event(f"post settle pay_8f3 net {CAPTURE.plus(REFUND)}")  # C: after A and B
    return net


def _print_logs(net: SimulatedNetwork) -> None:
    print(f"{'lamport':<11} {'vector':<8} {'node':<9} event")
    for node_id in (PAYMENTS, REFUNDS, LEDGER):
        for e in net.node(node_id).log:
            print(e)


def main() -> None:
    print("== Lamport clock: three rules ==")
    a, b = LamportClock(), LamportClock()
    print(f"a.tick()      -> {a.tick()}   (local event)")
    sent = a.send()
    print(f"a.send()      -> {sent}   (travels in the message)")
    for _ in range(5):
        b.tick()
    print(f"b is at {b.time()}; b.receive({sent}) -> {b.receive(sent)}   (max(5,2)+1: local clock already ahead)")
    c = LamportClock()
    print(f"c is at 0; c.receive({sent}) -> {c.receive(sent)}   (max(0,2)+1: jump past the sender)")

    print("\n== Vector clocks: the partial order ==")
    v1, v2 = VectorClock.of(2, 0, 0), VectorClock.of(0, 1, 0)
    v3 = v1.merge(v2).increment(1)
    print(f"{v1} vs {v2} -> {v1.compare(v2).value}")
    print(f"{v1} vs {v3} -> {v1.compare(v3).value}")
    print(f"{v3} vs {v1} -> {v3.compare(v1).value}")
    print(f"{v3} = merge({v1},{v2}).increment(refunds)")

    print("\n== Scenario 1: refund issued after the capture notice arrived (seed 7) ==")
    causal = causal_refund_scenario(7)
    _print_logs(causal)
    cap_a, ref_a = causal.node(PAYMENTS).log[0], causal.node(REFUNDS).log[1]
    print(f"captured {cap_a.vector} vs refunded {ref_a.vector} -> {cap_a.vector.compare(ref_a.vector).value}")

    print("\n== Scenario 2: refund issued before the capture notice arrived (seed 7) ==")
    conc = concurrent_refund_scenario(7)
    _print_logs(conc)

    print("\n== Total order by (lamport, node): the sequence the ledger would assign ==")
    events = conc.all_events()
    for e in total_order(events):
        print(e)

    print("\n== Concurrent postings by vector clock: the conflicts to resolve ==")
    for x, y in concurrent_pairs(postings(events)):
        print(f"{x.lamport} {x.vector} '{x.description}'  ||  {y.lamport} {y.vector} '{y.description}'")

    print("\n== What Lamport order cannot tell you ==")
    captured, refunded = conc.node(PAYMENTS).log[0], conc.node(REFUNDS).log[0]
    first, second = (captured, refunded) if captured.lamport < refunded.lamport else (refunded, captured)
    print(f"lamport total order puts {first.lamport} before {second.lamport}; "
          f"vector clocks say {first.vector} vs {second.vector} is {first.vector.compare(second.vector).value}")
    print("a smaller Lamport time is necessary for happens-before, never sufficient")
    print(f"messages delivered: {conc.delivered()}, final tick: {conc.tick()}")


if __name__ == "__main__":
    main()
