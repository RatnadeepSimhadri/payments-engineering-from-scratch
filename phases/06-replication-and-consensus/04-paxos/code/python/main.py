"""Lesson: phases/06-replication-and-consensus/04-paxos/docs/en.md
Single-Decree Paxos from Scratch
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: Lamport, "Paxos Made Simple" (2001); Lamport, "The Part-Time Parliament" (ACM TOCS 1998).
Python 3.11+, standard library only. Same design as the Java and Go versions.
"""

from __future__ import annotations

import heapq
from dataclasses import dataclass
from enum import Enum
from typing import Callable, Optional

MASK64 = 0xFFFFFFFFFFFFFFFF


class Rng:
    """SplitMix64 (Steele, Lea, Flood, OOPSLA 2014), implemented by hand so that the same seed produces
    the same message delays and drops in Java, Python, and Go: a bug found in one language replays in all three."""

    def __init__(self, seed: int) -> None:
        self.state = seed & MASK64

    def next_long(self) -> int:
        """Next 64-bit output as an unsigned integer (Java's signed long reinterpreted)."""
        self.state = (self.state + 0x9E3779B97F4A7C15) & MASK64
        z = self.state
        z = ((z ^ (z >> 30)) * 0xBF58476D1CE4E5B9) & MASK64
        z = ((z ^ (z >> 27)) * 0x94D049BB133111EB) & MASK64
        return z ^ (z >> 31)

    def next_int(self, bound: int) -> int:
        """Uniform integer in [0, bound). Uses the top 31 bits so the result is identical across languages."""
        return (self.next_long() >> 33) % bound

    def next_double(self) -> float:
        """Uniform float in [0, 1) built from the top 53 bits (exact: a 53-bit integer times a power of two)."""
        return (self.next_long() >> 11) * 2.0 ** -53


@dataclass(frozen=True, order=True)
class Ballot:
    """A ballot number: totally ordered, unique per proposer because the proposer id breaks ties between rounds."""

    round: int
    proposer_id: int

    def is_none(self) -> bool:
        return self.round == 0

    def __str__(self) -> str:
        return "none" if self.is_none() else f"{self.round}.{self.proposer_id}"


NONE = Ballot(0, 0)


class MessageKind(Enum):
    PREPARE = "PREPARE"
    PROMISE = "PROMISE"
    ACCEPT = "ACCEPT"
    ACCEPTED = "ACCEPTED"
    NACK = "NACK"


@dataclass(frozen=True)
class Message:
    """One message on the wire. Which fields are meaningful depends on the kind:
    PREPARE(ballot); PROMISE(ballot, prior_ballot + value previously accepted); ACCEPT(ballot, value);
    ACCEPTED(ballot, value); NACK(ballot rejected, prior_ballot = the ballot the acceptor has promised)."""

    kind: MessageKind
    from_: int
    to: int
    ballot: Ballot
    prior_ballot: Ballot = NONE
    value: Optional[str] = None

    @staticmethod
    def prepare(from_: int, to: int, b: Ballot) -> "Message":
        return Message(MessageKind.PREPARE, from_, to, b)

    @staticmethod
    def promise(from_: int, to: int, b: Ballot, prior: Ballot, v: Optional[str]) -> "Message":
        return Message(MessageKind.PROMISE, from_, to, b, prior, v)

    @staticmethod
    def accept(from_: int, to: int, b: Ballot, v: str) -> "Message":
        return Message(MessageKind.ACCEPT, from_, to, b, NONE, v)

    @staticmethod
    def accepted(from_: int, to: int, b: Ballot, v: str) -> "Message":
        return Message(MessageKind.ACCEPTED, from_, to, b, NONE, v)

    @staticmethod
    def nack(from_: int, to: int, rejected: Ballot, promised: Ballot) -> "Message":
        return Message(MessageKind.NACK, from_, to, rejected, promised)


class Node:
    """A node is a pure state machine: it consumes one message or one clock tick and returns the messages it wants sent."""

    id: int

    def handle(self, m: Message, now: int) -> list[Message]:
        raise NotImplementedError

    def tick(self, now: int) -> list[Message]:
        return []


class Acceptor(Node):
    """Acceptor: the only role whose state matters for safety. Two fields, two promises.
    P1: never accept a ballot lower than one already promised. P2: report what was accepted in every promise."""

    def __init__(self, id: int, learner_ids: list[int]) -> None:
        self.id = id
        self.learner_ids = list(learner_ids)
        self.promised: Ballot = NONE
        self.accepted_ballot: Ballot = NONE
        self.accepted_value: Optional[str] = None

    def handle(self, m: Message, now: int) -> list[Message]:
        if m.kind is MessageKind.PREPARE:
            return self.on_prepare(m)
        if m.kind is MessageKind.ACCEPT:
            return self.on_accept(m)
        return []

    def on_prepare(self, m: Message) -> list[Message]:
        if m.ballot >= self.promised:
            self.promised = m.ballot
            return [Message.promise(self.id, m.from_, m.ballot, self.accepted_ballot, self.accepted_value)]
        return [Message.nack(self.id, m.from_, m.ballot, self.promised)]

    def on_accept(self, m: Message) -> list[Message]:
        if m.ballot >= self.promised:
            self.promised = m.ballot
            self.accepted_ballot = m.ballot
            self.accepted_value = m.value
            out = [Message.accepted(self.id, m.from_, m.ballot, m.value)]
            for learner in self.learner_ids:
                out.append(Message.accepted(self.id, learner, m.ballot, m.value))
            return out
        return [Message.nack(self.id, m.from_, m.ballot, self.promised)]


class Phase(Enum):
    IDLE = "IDLE"
    PREPARING = "PREPARING"
    ACCEPTING = "ACCEPTING"
    DONE = "DONE"


class Proposer(Node):
    """Proposer: drives the two phases, retries on NACK or timeout, and must adopt any value it learns was already accepted."""

    def __init__(self, id: int, acceptor_ids: list[int], timeout: int) -> None:
        self.id = id
        self.acceptor_ids = list(acceptor_ids)
        self.quorum = len(acceptor_ids) // 2 + 1
        self.timeout = timeout
        self.round = 0
        self.ballot: Ballot = NONE
        self.phase = Phase.IDLE
        self.wanted: Optional[str] = None  # the value this proposer would like chosen
        self.proposing: Optional[str] = None  # the value it is actually proposing in phase 2 (may differ from wanted)
        self.decided: Optional[str] = None
        self.promises: set[int] = set()
        self.accepts: set[int] = set()
        self.highest_prior: Ballot = NONE
        self.highest_prior_value: Optional[str] = None
        self.deadline = 2**62
        self.restarts = 0

    def propose(self, value: str, now: int) -> list[Message]:
        """Phase 1a: pick a ballot higher than any this proposer has used and ask every acceptor to promise."""
        self.wanted = value
        return self.start_round(now)

    def start_round(self, now: int) -> list[Message]:
        self.round += 1
        self.ballot = Ballot(self.round, self.id)
        self.phase = Phase.PREPARING
        self.promises.clear()
        self.accepts.clear()
        self.highest_prior = NONE
        self.highest_prior_value = None
        self.deadline = now + self.timeout
        return [Message.prepare(self.id, a, self.ballot) for a in self.acceptor_ids]

    def handle(self, m: Message, now: int) -> list[Message]:
        if self.phase is Phase.DONE or m.ballot != self.ballot:
            return []  # stale reply from an earlier round
        if m.kind is MessageKind.PROMISE:
            return self.on_promise(m, now)
        if m.kind is MessageKind.ACCEPTED:
            return self.on_accepted(m)
        if m.kind is MessageKind.NACK:
            return self.on_nack(m, now)
        return []

    def on_promise(self, m: Message, now: int) -> list[Message]:
        """Phase 1b -> 2a: once a majority has promised, propose the highest-ballot value any of them had accepted, else our own."""
        if self.phase is not Phase.PREPARING:
            return []
        self.promises.add(m.from_)
        if not m.prior_ballot.is_none() and m.prior_ballot > self.highest_prior:
            self.highest_prior = m.prior_ballot
            self.highest_prior_value = m.value
        if len(self.promises) < self.quorum:
            return []
        self.proposing = self.highest_prior_value if self.highest_prior_value is not None else self.wanted
        self.phase = Phase.ACCEPTING
        self.deadline = now + self.timeout
        return [Message.accept(self.id, a, self.ballot, self.proposing) for a in self.acceptor_ids]

    def on_accepted(self, m: Message) -> list[Message]:
        if self.phase is not Phase.ACCEPTING:
            return []
        self.accepts.add(m.from_)
        if len(self.accepts) >= self.quorum:
            self.decided = self.proposing
            self.phase = Phase.DONE
        return []

    def on_nack(self, m: Message, now: int) -> list[Message]:
        """A NACK carries the ballot the acceptor has promised; jump past it instead of creeping up one round at a time."""
        self.round = max(self.round, m.prior_ballot.round)
        self.restarts += 1
        return self.start_round(now)

    def tick(self, now: int) -> list[Message]:
        if self.phase in (Phase.PREPARING, Phase.ACCEPTING) and now >= self.deadline:
            self.restarts += 1
            return self.start_round(now)
        return []


class Learner(Node):
    """Learner: a value is chosen once a majority of acceptors have accepted the same ballot. It must never learn two values."""

    def __init__(self, id: int, acceptor_count: int) -> None:
        self.id = id
        self.quorum = acceptor_count // 2 + 1
        self.votes: dict[Ballot, set[int]] = {}
        self.learned_ballot: Ballot = NONE
        self.learned: Optional[str] = None

    def handle(self, m: Message, now: int) -> list[Message]:
        if m.kind is not MessageKind.ACCEPTED:
            return []
        voters = self.votes.setdefault(m.ballot, set())
        voters.add(m.from_)
        if len(voters) >= self.quorum:
            if self.learned is not None and self.learned != m.value:
                raise RuntimeError(f"SAFETY VIOLATION: learner {self.id} learned {self.learned} then {m.value}")
            self.learned = m.value
            self.learned_ballot = m.ballot
        return []


# Naming used by the trace output: ids 0..A-1 are acceptors, then proposers, then learners.
acceptors_for_names = 3
proposers_for_names = 2


def name(id: int) -> str:
    if id < acceptors_for_names:
        return f"a{id}"
    if id < acceptors_for_names + proposers_for_names:
        return f"p{id}"
    return f"l{id}"


def show(value: Optional[str]) -> str:
    return "(none)" if value is None else value


def describe(m: Message) -> str:
    k = m.kind
    if k is MessageKind.PREPARE:
        return f"PREPARE {m.ballot}"
    if k is MessageKind.PROMISE:
        detail = " (nothing accepted)" if m.prior_ballot.is_none() else f" (accepted {m.value} at {m.prior_ballot})"
        return f"PROMISE {m.ballot}{detail}"
    if k is MessageKind.ACCEPT:
        return f"ACCEPT {m.ballot} {m.value}"
    if k is MessageKind.ACCEPTED:
        return f"ACCEPTED {m.ballot} {m.value}"
    return f"NACK {m.ballot} (promised {m.prior_ballot})"


class Network:
    """Deterministic simulated network. Delays and drops come from the seeded Rng; delivery order is by
    (deliver_at, sequence number), so a run is a pure function of (seed, program). No threads, no sleeping."""

    def __init__(self, seed: int, drop_rate: float, max_delay: int) -> None:
        self.rng = Rng(seed)
        self.drop_rate = drop_rate
        self.max_delay = max(1, max_delay)
        self.nodes: dict[int, Node] = {}
        self.in_flight: list[tuple[int, int, Message]] = []  # heap of (deliver_at, seq, message)
        self.isolated: set[int] = set()
        self.observer: Callable[[], None] = lambda: None
        self.now = 0
        self.seq = 0
        self.sent = 0
        self.delivered = 0
        self.dropped = 0
        self.trace = False

    def add(self, node: Node) -> None:
        self.nodes[node.id] = node

    def set_observer(self, observer: Callable[[], None]) -> None:
        self.observer = observer

    def in_flight_count(self) -> int:
        return len(self.in_flight)

    def isolate(self, node_id: int) -> None:
        self.isolated.add(node_id)

    def heal(self) -> None:
        self.isolated.clear()

    def send(self, m: Message) -> None:
        self.sent += 1
        if m.from_ in self.isolated or m.to in self.isolated:
            self.dropped += 1
            return
        roll = self.rng.next_double()
        delay = 1 + self.rng.next_int(self.max_delay)
        if roll < self.drop_rate:
            self.dropped += 1
            return
        heapq.heappush(self.in_flight, (self.now + delay, self.seq, m))
        self.seq += 1

    def send_all(self, ms: list[Message]) -> None:
        for m in ms:
            self.send(m)

    def tick(self) -> None:
        """Advance the clock one tick: let every node act on the time, then deliver everything that is due."""
        self.now += 1
        for node_id in sorted(self.nodes):
            self.send_all(self.nodes[node_id].tick(self.now))
        while self.in_flight and self.in_flight[0][0] <= self.now:
            self.deliver(heapq.heappop(self.in_flight))

    def deliver(self, envelope: tuple[int, int, Message]) -> None:
        self.delivered += 1
        m = envelope[2]
        target = self.nodes[m.to]
        if self.trace:
            print(f"t={self.now:<3d} {name(m.from_)} -> {name(m.to)} {describe(m)}")
        self.send_all(target.handle(m, self.now))
        self.observer()

    def drain(self, filter: Callable[[Message], bool]) -> int:
        """Deliver, ignoring delays, every in-flight message matching the filter, repeating until none match.
        Lets a test script an adversarial schedule."""
        count = 0
        while True:
            matching = [e for e in self.in_flight if filter(e[2])]
            if not matching:
                return count
            rest = [e for e in self.in_flight if not filter(e[2])]
            heapq.heapify(rest)
            self.in_flight = rest
            matching.sort(key=lambda e: e[1])
            for e in matching:
                self.deliver(e)
                count += 1


class PaxosCluster:
    """Wires acceptors, proposers, and learners onto one network and checks the safety invariant after every delivery."""

    def __init__(self, acceptor_count: int, proposer_count: int, learner_count: int,
                 seed: int, drop_rate: float, max_delay: int, timeout: int) -> None:
        self.acceptor_count = acceptor_count
        self.network = Network(seed, drop_rate, max_delay)
        acceptor_ids = list(range(acceptor_count))
        learner_ids = [acceptor_count + proposer_count + i for i in range(learner_count)]
        self.acceptors = [Acceptor(i, learner_ids) for i in acceptor_ids]
        self.proposers = [Proposer(acceptor_count + i, acceptor_ids, timeout) for i in range(proposer_count)]
        self.learners = [Learner(learner_ids[i], acceptor_count) for i in range(learner_count)]
        for node in [*self.acceptors, *self.proposers, *self.learners]:
            self.network.add(node)
        self.network.set_observer(self.check_safety)
        self.first_chosen: Optional[str] = None
        self.chosen_at = -1

    def propose(self, proposer_index: int, value: str) -> None:
        self.network.send_all(self.proposers[proposer_index].propose(value, self.network.now))

    def tick(self) -> None:
        self.network.tick()

    def run(self, max_ticks: int) -> bool:
        """Run until every learner has learned a value or max_ticks elapse. Returns True if a value was chosen."""
        for _ in range(max_ticks):
            self.tick()
            if self.all_learned():
                return True
        return self.chosen_value() is not None

    def all_learned(self) -> bool:
        return all(l.learned is not None for l in self.learners)

    def chosen_value(self) -> Optional[str]:
        """The definition of "chosen": some ballot has been accepted by a majority of acceptors. Computed from acceptor state alone."""
        counts: dict[Ballot, int] = {}
        for a in self.acceptors:
            if a.accepted_ballot.is_none():
                continue
            counts[a.accepted_ballot] = counts.get(a.accepted_ballot, 0) + 1
            if counts[a.accepted_ballot] >= self.acceptor_count // 2 + 1:
                return a.accepted_value
        return None

    def check_safety(self) -> None:
        current = self.chosen_value()
        if current is None:
            return
        if self.first_chosen is None:
            self.first_chosen = current
            self.chosen_at = self.network.now
            return
        if self.first_chosen != current:
            raise RuntimeError(f"SAFETY VIOLATION: chosen {self.first_chosen} at t={self.chosen_at} "
                               f"then {current} at t={self.network.now}")

    def max_round(self) -> int:
        return max((p.round for p in self.proposers), default=0)


def demo_lossless() -> None:
    print("== 1. Three acceptors, two proposers, one learner, lossless network, delay 1 ==")
    c = PaxosCluster(3, 2, 1, 7, 0.0, 1, 10)
    c.network.trace = True
    c.propose(0, "debit:acct-42:19.99")
    c.propose(1, "credit:acct-42:5.00")
    for _ in range(8):  # long enough for the losing proposer to retry and adopt the chosen value
        c.tick()
    l = c.learners[0]
    print(f"chosen = {c.first_chosen} at t={c.chosen_at}; learner learned {l.learned} at ballot {l.learned_ballot}; "
          f"p3.decided={c.proposers[0].decided} p4.decided={c.proposers[1].decided}\n")


def demo_seeded_runs() -> None:
    print("== 2. 200 seeded runs: 5 acceptors, 3 proposers, 20% message loss, delays 1..3 ==")
    decided = 0
    total_ticks = 0
    max_rounds = 0
    winners: dict[str, int] = {}
    for seed in range(1, 201):
        c = PaxosCluster(5, 3, 2, seed, 0.20, 3, 12)
        c.propose(0, "A")
        c.propose(1, "B")
        c.propose(2, "C")
        if c.run(400):
            decided += 1
            total_ticks += c.chosen_at
            winners[c.first_chosen] = winners.get(c.first_chosen, 0) + 1
        max_rounds = max(max_rounds, c.max_round())
    print(f"decided in {decided}/200 runs, distinct chosen values per run: never more than 1 (checked after every delivery)")
    summary = "{" + ", ".join(f"{k}={v}" for k, v in sorted(winners.items())) + "}"
    mean = 0.0 if decided == 0 else total_ticks / decided
    print(f"winners: {summary}; mean ticks to choose: {mean:.1f}; highest round seen: {max_rounds}\n")


def demo_duel() -> None:
    print("== 3. Constructed duel: two proposers whose prepares keep landing between each other's promise and accept ==")
    c = PaxosCluster(3, 2, 1, 1, 0.0, 1, 1000)
    not_accept = lambda m: m.kind is not MessageKind.ACCEPT  # noqa: E731
    c.propose(0, "A")
    c.network.drain(not_accept)  # p3: prepare 1.3 -> promises -> ACCEPT 1.3 held in flight
    c.propose(1, "B")
    c.network.drain(not_accept)  # p4: prepare 1.4 -> promises -> ACCEPT 1.4 held in flight
    for k in range(1, 4):
        c.network.drain(lambda m: m.kind is MessageKind.ACCEPT and m.from_ == 3)  # p3's accept arrives: NACK (promised 1.4)
        c.network.drain(not_accept)  # p3 re-prepares at a higher round and gets promises
        c.network.drain(lambda m: m.kind is MessageKind.ACCEPT and m.from_ == 4)  # p4's accept arrives: NACK
        c.network.drain(not_accept)
        print(f"after exchange {k}: p3 at ballot {c.proposers[0].ballot}, p4 at ballot {c.proposers[1].ballot}, "
              f"chosen={show(c.chosen_value())}")
    c.network.drain(lambda m: True)  # stop scheduling adversarially: whichever accept lands first wins
    print(f"schedule released: chosen={show(c.chosen_value())}, rounds used={c.max_round()}")
    print("fix: one distinguished proposer (a leader), or randomized backoff before retrying -- Lesson 05 builds the leader.")


def main() -> None:
    global acceptors_for_names, proposers_for_names
    demo_lossless()
    acceptors_for_names, proposers_for_names = 5, 3
    demo_seeded_runs()
    acceptors_for_names, proposers_for_names = 3, 2
    demo_duel()


if __name__ == "__main__":
    main()
