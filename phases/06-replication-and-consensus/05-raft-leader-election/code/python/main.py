"""Lesson: phases/06-replication-and-consensus/05-raft-leader-election/docs/en.md
Raft I: Terms, Leader Election, and a Simulated Network
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: Ongaro & Ousterhout, "In Search of an Understandable Consensus Algorithm" (USENIX ATC 2014), Figure 2;
         Ongaro, "Consensus: Bridging Theory and Practice" (PhD thesis, Stanford 2014) for Pre-Vote.
Python 3.11+, standard library only. Same design as the Java and Go versions.
"""

from __future__ import annotations

import heapq
from dataclasses import dataclass
from enum import Enum
from typing import Callable, Optional

MASK64 = (1 << 64) - 1


class Rng:
    """SplitMix64 (Steele, Lea, Flood, OOPSLA 2014), implemented by hand so that the same seed produces the same
    election timeouts, delays, and drops in Java, Python, and Go: a bug found in one language replays in all three."""

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
        """Uniform float in [0, 1) built from the top 53 bits."""
        return (self.next_long() >> 11) * 2.0 ** -53


class Role(Enum):
    FOLLOWER = "FOLLOWER"
    CANDIDATE = "CANDIDATE"
    LEADER = "LEADER"


class MessageKind(Enum):
    """Raft has two RPCs. On a simulated network each RPC is two one-way messages: the request and its reply."""

    REQUEST_VOTE = "REQUEST_VOTE"
    REQUEST_VOTE_REPLY = "REQUEST_VOTE_REPLY"
    APPEND_ENTRIES = "APPEND_ENTRIES"
    APPEND_ENTRIES_REPLY = "APPEND_ENTRIES_REPLY"


@dataclass(frozen=True)
class Message:
    """One message. Every message carries the sender's current term (Figure 2: every RPC and reply includes the term).
    last_log_index/last_log_term are the candidate's, used by the receiver's "at least as up-to-date" check (5.4.1).
    granted doubles as "success" on an AppendEntries reply."""

    kind: MessageKind
    from_: int
    to: int
    term: int
    last_log_index: int = 0
    last_log_term: int = 0
    granted: bool = False

    @staticmethod
    def request_vote(from_: int, to: int, term: int, last_log_index: int, last_log_term: int) -> "Message":
        return Message(MessageKind.REQUEST_VOTE, from_, to, term, last_log_index, last_log_term, False)

    @staticmethod
    def vote_reply(from_: int, to: int, term: int, granted: bool) -> "Message":
        return Message(MessageKind.REQUEST_VOTE_REPLY, from_, to, term, 0, 0, granted)

    @staticmethod
    def heartbeat(from_: int, to: int, term: int) -> "Message":
        """Lesson 05 sends AppendEntries with no entries: a heartbeat. Lesson 06 fills it with log entries."""
        return Message(MessageKind.APPEND_ENTRIES, from_, to, term, 0, 0, False)

    @staticmethod
    def heartbeat_reply(from_: int, to: int, term: int, success: bool) -> "Message":
        return Message(MessageKind.APPEND_ENTRIES_REPLY, from_, to, term, 0, 0, success)


class Storage:
    """Figure 2, "Persistent state on all servers (updated on stable storage before responding to RPCs)".
    In this simulation a Storage object survives a crash; everything else on the node is lost. voted_for == -1 is null."""

    def __init__(self) -> None:
        self.current_term = 0
        self.voted_for = -1
        self.writes = 0

    def save(self, term: int, voted_for: int) -> None:
        self.current_term = term
        self.voted_for = voted_for
        self.writes += 1


@dataclass(frozen=True)
class Config:
    """Timing in ticks. The paper's recommendation: broadcastTime << electionTimeout << MTBF; defaults mirror its 150-300 ms."""

    election_timeout_min: int = 150
    election_timeout_max: int = 300
    heartbeat_interval: int = 50

    def __post_init__(self) -> None:
        if self.election_timeout_min <= 0 or self.election_timeout_max < self.election_timeout_min or self.heartbeat_interval <= 0:
            raise ValueError("bad timing config")
        if self.heartbeat_interval >= self.election_timeout_min:
            raise ValueError("heartbeat interval must be well below the election timeout")


DEFAULT_CONFIG = Config()
Tracer = Callable[[int, str], None]


def no_trace(now: int, text: str) -> None:
    return None


def name(node_id: int) -> str:
    return f"n{node_id}"


class RaftNode:
    """One Raft server, election rules only (Figure 2 minus the log). The log is represented by last_log_index/last_log_term
    so the "at least as up-to-date" vote check can be built and tested now; Lesson 06 replaces the two fields with a real log."""

    def __init__(self, id: int, peers: list[int], storage: Storage, rng: Rng, config: Config, now: int, tracer: Tracer = no_trace) -> None:
        self.id = id
        self.peers = list(peers)
        self.storage = storage
        self.rng = rng
        self.config = config
        self.tracer = tracer
        self.role = Role.FOLLOWER
        self.leader_id = -1
        self.last_log_index = 0
        self.last_log_term = 0
        self.votes_granted: set[int] = set()
        self.election_deadline = 0
        self.next_heartbeat = 0
        self.elections_started = 0
        self.reset_election_timer(now)

    @property
    def current_term(self) -> int:
        return self.storage.current_term

    @property
    def voted_for(self) -> int:
        return self.storage.voted_for

    def cluster_size(self) -> int:
        return len(self.peers) + 1

    def majority(self) -> int:
        return self.cluster_size() // 2 + 1

    def votes(self) -> int:
        return len(self.votes_granted)

    def reset_election_timer(self, now: int) -> None:
        """Randomized timeout in [min, max]: the paper's whole answer to split votes (section 5.2)."""
        span = self.config.election_timeout_max - self.config.election_timeout_min + 1
        self.election_deadline = now + self.config.election_timeout_min + self.rng.next_int(span)

    def tick(self, now: int) -> list[Message]:
        if self.role == Role.LEADER:
            return self.send_heartbeats(now) if now >= self.next_heartbeat else []
        # Followers: election timeout elapses without AppendEntries from current leader or granting a vote -> candidate.
        # Candidates: election timeout elapses -> start a new election.
        return self.start_election(now) if now >= self.election_deadline else []

    def start_election(self, now: int) -> list[Message]:
        """Figure 2, Candidates, "On conversion to candidate"."""
        self.role = Role.CANDIDATE
        self.storage.save(self.storage.current_term + 1, self.id)  # increment currentTerm, vote for self, persist before sending
        self.leader_id = -1
        self.votes_granted = {self.id}
        self.elections_started += 1
        self.reset_election_timer(now)
        self.tracer(now, f"{name(self.id)} election timeout -> candidate for term {self.current_term}, votes for itself")
        return [Message.request_vote(self.id, p, self.current_term, self.last_log_index, self.last_log_term) for p in self.peers]

    def handle(self, m: Message, now: int) -> list[Message]:
        # Figure 2, All Servers: if RPC request or response contains term T > currentTerm, set currentTerm = T, convert to follower.
        if m.term > self.current_term:
            self.become_follower(m.term, now)
        if m.kind == MessageKind.REQUEST_VOTE:
            return self.on_request_vote(m, now)
        if m.kind == MessageKind.REQUEST_VOTE_REPLY:
            return self.on_vote_reply(m, now)
        if m.kind == MessageKind.APPEND_ENTRIES:
            return self.on_append_entries(m, now)
        return []  # APPEND_ENTRIES_REPLY: Lesson 06 uses replies to drive nextIndex/matchIndex

    def become_follower(self, term: int, now: int) -> None:
        before = self.role
        self.role = Role.FOLLOWER
        self.leader_id = -1
        self.votes_granted = set()
        if term > self.current_term:
            self.storage.save(term, -1)  # a new term means votedFor is null again
            suffix = " (was LEADER)" if before == Role.LEADER else " (was candidate)" if before == Role.CANDIDATE else ""
            self.tracer(now, f"{name(self.id)} sees term {term} > its own -> follower{suffix}")
        # A leader keeps no election timer running, so give it a fresh one when it steps down.
        if before == Role.LEADER:
            self.reset_election_timer(now)

    def on_request_vote(self, m: Message, now: int) -> list[Message]:
        """Figure 2, RequestVote RPC, "Receiver implementation"."""
        if m.term < self.current_term:  # 1. Reply false if term < currentTerm
            return [Message.vote_reply(self.id, m.from_, self.current_term, False)]
        # 2. If votedFor is null or candidateId, and candidate's log is at least as up-to-date as receiver's log, grant vote
        can_vote = self.voted_for == -1 or self.voted_for == m.from_
        log_up_to_date = m.last_log_term > self.last_log_term or (
            m.last_log_term == self.last_log_term and m.last_log_index >= self.last_log_index)
        if can_vote and log_up_to_date:
            self.storage.save(self.current_term, m.from_)  # persist the vote before the reply leaves
            self.reset_election_timer(now)  # granting a vote resets the election timer
            self.tracer(now, f"{name(self.id)} grants vote to {name(m.from_)} for term {m.term}")
            return [Message.vote_reply(self.id, m.from_, self.current_term, True)]
        return [Message.vote_reply(self.id, m.from_, self.current_term, False)]

    def on_vote_reply(self, m: Message, now: int) -> list[Message]:
        # A reply from an earlier election (older term) or after we stopped being a candidate is stale: ignore it.
        if self.role != Role.CANDIDATE or m.term != self.current_term:
            return []
        if m.granted:
            self.votes_granted.add(m.from_)
            if len(self.votes_granted) >= self.majority():
                return self.become_leader(now)
        return []

    def become_leader(self, now: int) -> list[Message]:
        self.role = Role.LEADER
        self.leader_id = self.id
        self.tracer(now, f"{name(self.id)} wins {len(self.votes_granted)}/{self.cluster_size()} votes -> LEADER of term {self.current_term}")
        return self.send_heartbeats(now)  # Leaders: upon election send initial empty AppendEntries to each server

    def send_heartbeats(self, now: int) -> list[Message]:
        self.next_heartbeat = now + self.config.heartbeat_interval
        return [Message.heartbeat(self.id, p, self.current_term) for p in self.peers]

    def on_append_entries(self, m: Message, now: int) -> list[Message]:
        """Figure 2, AppendEntries receiver rule 1, and Candidates: "if AppendEntries received from new leader: convert to follower"."""
        if m.term < self.current_term:
            return [Message.heartbeat_reply(self.id, m.from_, self.current_term, False)]
        if self.role == Role.LEADER:
            raise RuntimeError(f"two leaders in term {self.current_term}: {self.id} and {m.from_}")
        if self.role == Role.CANDIDATE:
            self.role = Role.FOLLOWER
            self.votes_granted = set()
            self.tracer(now, f"{name(self.id)} hears leader {name(m.from_)} for term {m.term} -> candidate steps down")
        self.leader_id = m.from_
        self.reset_election_timer(now)
        return [Message.heartbeat_reply(self.id, m.from_, self.current_term, True)]


class Network:
    """Deterministic simulated network. Delays and drops come from the seeded Rng; delivery order is by
    (deliver_at, sequence number), so a run is a pure function of (seed, program). Partitions, crashes, and restarts are
    explicit operations, which is what makes every bug replayable. No threads, no sleeping."""

    def __init__(self, seed: int, drop_rate: float, max_delay: int) -> None:
        self.rng = Rng(seed)
        self.drop_rate = drop_rate
        self.max_delay = max(1, max_delay)
        self.nodes: dict[int, RaftNode] = {}
        self.in_flight: list[tuple[int, int, Message]] = []
        self.down: set[int] = set()
        self.side_a: frozenset[int] = frozenset()
        self.partitioned = False
        self.observer: Callable[[], None] = lambda: None
        self.now = 0
        self.seq = 0
        self.sent = 0
        self.delivered = 0
        self.dropped = 0

    def add(self, node: RaftNode) -> None:
        self.nodes[node.id] = node

    def set_observer(self, observer: Callable[[], None]) -> None:
        self.observer = observer

    def in_flight_count(self) -> int:
        return len(self.in_flight)

    def is_down(self, node_id: int) -> bool:
        return node_id in self.down

    def partition(self, side_a: set[int]) -> None:
        """Split the cluster into side_a and everyone else; messages across the cut are dropped."""
        self.side_a = frozenset(side_a)
        self.partitioned = True

    def heal(self) -> None:
        self.partitioned = False
        self.side_a = frozenset()

    def crash(self, node_id: int) -> None:
        """A crashed node receives nothing, sends nothing, and does not tick. In-flight messages to it are lost."""
        self.down.add(node_id)

    def restart(self, node: RaftNode) -> None:
        """Bring a node back with fresh volatile state (the caller rebuilds it from its Storage)."""
        self.down.discard(node.id)
        self.nodes[node.id] = node

    def crosses_partition(self, a: int, b: int) -> bool:
        return self.partitioned and ((a in self.side_a) != (b in self.side_a))

    def send(self, m: Message) -> None:
        self.sent += 1
        if m.from_ in self.down or m.to in self.down or self.crosses_partition(m.from_, m.to):
            self.dropped += 1
            return
        roll = self.rng.next_double()  # always draw both numbers so the rng stream is identical across languages
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
        """Advance the clock one tick: let every live node act on the time, then deliver everything that is due."""
        self.now += 1
        for node_id in sorted(self.nodes):
            if node_id not in self.down:
                self.send_all(self.nodes[node_id].tick(self.now))
        while self.in_flight and self.in_flight[0][0] <= self.now:
            self.deliver(heapq.heappop(self.in_flight))

    def deliver(self, envelope: tuple[int, int, Message]) -> None:
        m = envelope[2]
        if m.to in self.down or self.crosses_partition(m.from_, m.to):
            self.dropped += 1
            return
        self.delivered += 1
        self.send_all(self.nodes[m.to].handle(m, self.now))
        self.observer()


class Cluster:
    """Wires N RaftNodes onto one network, drives the clock, and checks Election Safety after every delivery."""

    def __init__(self, size: int, seed: int, drop_rate: float, max_delay: int, config: Config = DEFAULT_CONFIG) -> None:
        self.size = size
        self.seed = seed
        self.config = config
        self.network = Network(seed, drop_rate, max_delay)
        self.storages = [Storage() for _ in range(size)]
        self.events: list[str] = []
        self.leaders_by_term: dict[int, int] = {}
        self.restarts = [0] * size
        self.nodes: list[RaftNode] = []
        for i in range(size):
            node = RaftNode(i, self.peers_of(i), self.storages[i], self.node_rng(seed, i, 0), config, 0, self.trace)
            self.nodes.append(node)
            self.network.add(node)
        self.network.set_observer(self.check_election_safety)

    def peers_of(self, node_id: int) -> list[int]:
        return [j for j in range(self.size) if j != node_id]

    @staticmethod
    def node_rng(seed: int, node_id: int, restart: int) -> Rng:
        """Each node gets its own rng stream derived from the seed, so the network's and the nodes' draws never interleave."""
        return Rng(seed * 7919 + 1000003 * (node_id + 1) + 17 * restart)

    def trace(self, now: int, text: str) -> None:
        self.events.append(f"t={now:<4d} {text}")

    def now(self) -> int:
        return self.network.now

    def node(self, node_id: int) -> RaftNode:
        return self.nodes[node_id]

    def tick(self) -> None:
        self.network.tick()
        self.check_election_safety()

    def run(self, ticks: int) -> None:
        for _ in range(ticks):
            self.tick()

    def run_until_stable_leader(self, max_ticks: int) -> int:
        """Run until exactly one live node is a leader and every other live node follows it (same term). Returns the tick, or -1."""
        for _ in range(max_ticks):
            self.tick()
            if self.stable_leader() is not None:
                return self.now()
        return -1

    def stable_leader(self) -> Optional[RaftNode]:
        """The leader whose term every live, reachable node agrees on, or None."""
        leader: Optional[RaftNode] = None
        for n in self.nodes:
            if self.network.is_down(n.id):
                continue
            if n.role == Role.LEADER:
                if leader is not None:
                    return None
                leader = n
        if leader is None:
            return None
        for n in self.nodes:
            if self.network.is_down(n.id) or n is leader:
                continue
            if n.current_term != leader.current_term or n.leader_id != leader.id:
                return None
        return leader

    def leaders(self) -> list[RaftNode]:
        return [n for n in self.nodes if not self.network.is_down(n.id) and n.role == Role.LEADER]

    def max_term(self) -> int:
        return max(n.current_term for n in self.nodes)

    def check_election_safety(self) -> None:
        """Election Safety (Figure 3): at most one leader can be elected in a given term."""
        for n in self.nodes:
            if n.role != Role.LEADER:
                continue
            existing = self.leaders_by_term.setdefault(n.current_term, n.id)
            if existing != n.id:
                raise RuntimeError(f"ELECTION SAFETY VIOLATED: term {n.current_term} has leaders {existing} and {n.id}")

    def partition(self, side_a: set[int]) -> None:
        self.network.partition(side_a)
        self.trace(self.now(), f"network partition: {sorted(side_a)} | rest")

    def heal(self) -> None:
        self.network.heal()
        self.trace(self.now(), "network heals")

    def crash(self, node_id: int) -> None:
        self.network.crash(node_id)
        self.trace(self.now(), f"{name(node_id)} crashes (volatile state lost, Storage kept)")

    def restart(self, node_id: int) -> RaftNode:
        """Restart from stable storage: currentTerm and votedFor survive; role, leader_id, votes do not."""
        self.restarts[node_id] += 1
        n = RaftNode(node_id, self.peers_of(node_id), self.storages[node_id],
                     self.node_rng(self.seed, node_id, self.restarts[node_id]), self.config, self.now(), self.trace)
        self.nodes[node_id] = n
        self.network.restart(n)
        voted = "null" if n.voted_for < 0 else name(n.voted_for)
        self.trace(self.now(), f"{name(node_id)} restarts as follower, term {n.current_term}, votedFor {voted}")
        return n

    def expire_timer(self, node_id: int) -> None:
        """Force a node's election timer to expire on the next tick (for scripted scenarios)."""
        self.nodes[node_id].election_deadline = self.now()


def print_events(c: Cluster, start: int) -> None:
    for line in c.events[start:]:
        print(line)


def main() -> None:
    print("== Election on a 5-node cluster (seed 7, no drops, delay 1-5 ticks) ==")
    print("t=0    all nodes are followers in term 0; election timeouts drawn from [150, 300] ticks, heartbeat every 50")
    c = Cluster(5, 7, 0.0, 5)
    elected_at = c.run_until_stable_leader(2000)
    print_events(c, 0)
    leader = c.stable_leader()
    assert leader is not None
    print(f"leader {name(leader.id)}, term {leader.current_term}, stable at tick {elected_at}; "
          f"messages sent={c.network.sent} delivered={c.network.delivered} dropped={c.network.dropped}")
    before = len(c.events)
    c.run(1000)
    print(f"after 1000 more ticks of heartbeats: term still {c.max_term()}, {len(c.events) - before} new events, "
          f"leader unchanged = {str(c.stable_leader() is leader).lower()}")

    print()
    print("== Partition: isolate the leader with one follower ==")
    follower = (leader.id + 1) % 5
    before = len(c.events)
    c.partition({leader.id, follower})
    c.run(700)
    print_events(c, before)
    newest = max(c.leaders(), key=lambda l: l.current_term)
    print(f"minority side: {name(leader.id)} still believes it leads term {leader.current_term}; majority side elected "
          f"{name(newest.id)} for term {newest.current_term} ({len(c.leaders())} leaders alive, different terms)")
    before = len(c.events)
    c.heal()
    healed_at = c.run_until_stable_leader(2000)
    print_events(c, before)
    after = c.stable_leader()
    assert after is not None
    print(f"after heal: single leader {name(after.id)} in term {after.current_term} at tick {healed_at}; "
          f"old leader {name(leader.id)} is now {leader.role.value}")

    print()
    print("== Split vote on a 4-node cluster: first seed whose first leader needs more than one term ==")
    for seed in range(1, 501):
        s = Cluster(4, seed, 0.0, 3)
        s.run_until_stable_leader(3000)
        l = s.stable_leader()
        if l is not None and l.current_term > 1:
            print(f"seed {seed}:")
            print_events(s, 0)
            break

    print()
    print("== votedFor must be persisted ==")
    durable = Storage()
    n1 = RaftNode(1, [0, 2], durable, Rng(1), DEFAULT_CONFIG, 0)
    r1 = n1.handle(Message.request_vote(0, 1, 1, 0, 0), 10)[0]
    print(f"n1 receives RequestVote(term 1) from n0 -> granted={str(r1.granted).lower()}, votedFor={name(n1.voted_for)}")
    n1b = RaftNode(1, [0, 2], durable, Rng(2), DEFAULT_CONFIG, 11)
    r2 = n1b.handle(Message.request_vote(2, 1, 1, 0, 0), 12)[0]
    print(f"n1 crashes and restarts from Storage (term {n1b.current_term}, votedFor {name(n1b.voted_for)}); "
          f"RequestVote(term 1) from n2 -> granted={str(r2.granted).lower()}")
    n1c = RaftNode(1, [0, 2], Storage(), Rng(3), DEFAULT_CONFIG, 11)
    r3 = n1c.handle(Message.request_vote(2, 1, 1, 0, 0), 12)[0]
    print(f"counterfactual, votedFor lost on restart: RequestVote(term 1) from n2 -> granted={str(r3.granted).lower()}  "
          f"(two votes in one term: Election Safety breaks)")

    print()
    print("== 30 seeds, 5 nodes, 20% drops, delay 1-10, 3000 ticks each: Election Safety holds ==")
    elected = 0
    worst = 0
    terms = 0
    for seed in range(1, 31):
        s = Cluster(5, seed, 0.2, 10)
        t = s.run_until_stable_leader(3000)
        if t > 0:
            elected += 1
            worst = max(worst, t)
        s.run(3000 - max(t, 0))
        terms += len(s.leaders_by_term)
    print(f"seeds with a stable leader: {elected}/30, slowest election at tick {worst}, {terms} (term, leader) pairs recorded, "
          f"never two leaders in one term")


if __name__ == "__main__":
    main()
