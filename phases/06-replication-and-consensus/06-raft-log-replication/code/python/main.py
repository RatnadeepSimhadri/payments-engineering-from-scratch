"""Lesson: phases/06-replication-and-consensus/06-raft-log-replication/docs/en.md
Raft II: Log Replication and Commit
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: Ongaro & Ousterhout, "In Search of an Understandable Consensus Algorithm" (USENIX ATC 2014), Figure 2,
         sections 5.3 (log replication), 5.4.2 (committing entries from previous terms), 8 (no-op on election).
Python 3.11+, standard library only. Same design as the Java and Go versions; the network is Lesson 05's.
"""

from __future__ import annotations

import heapq
from dataclasses import dataclass, field
from enum import Enum
from typing import Callable, Optional

MASK64 = (1 << 64) - 1


class Rng:
    """SplitMix64 (Steele, Lea, Flood, OOPSLA 2014): identical streams in Java, Python, and Go, so a seed replays everywhere."""

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

    def next_double(self) -> float:
        return (self.next_long() >> 11) * 2.0 ** -53


class Role(Enum):
    FOLLOWER = "FOLLOWER"
    CANDIDATE = "CANDIDATE"
    LEADER = "LEADER"


class MessageKind(Enum):
    REQUEST_VOTE = "REQUEST_VOTE"
    REQUEST_VOTE_REPLY = "REQUEST_VOTE_REPLY"
    APPEND_ENTRIES = "APPEND_ENTRIES"
    APPEND_ENTRIES_REPLY = "APPEND_ENTRIES_REPLY"


@dataclass(frozen=True)
class LogEntry:
    """One log entry: the term in which the leader created it and the client command. Index is its position in the log."""

    term: int
    command: str

    def __str__(self) -> str:
        return f"t{self.term}:{self.command}"

    __repr__ = __str__


@dataclass(frozen=True)
class Message:
    """One message. For REQUEST_VOTE, prev_log_index/prev_log_term carry the candidate's last log index and term. For
    APPEND_ENTRIES they are the entry immediately before `entries`, which is the consistency check. `success` doubles as
    vote_granted; `match_index` on a successful APPEND_ENTRIES_REPLY tells the leader how far the follower's log now matches."""

    kind: MessageKind
    from_: int
    to: int
    term: int
    prev_log_index: int = 0
    prev_log_term: int = 0
    entries: tuple[LogEntry, ...] = ()
    leader_commit: int = 0
    success: bool = False
    match_index: int = 0

    @staticmethod
    def request_vote(from_: int, to: int, term: int, last_log_index: int, last_log_term: int) -> "Message":
        return Message(MessageKind.REQUEST_VOTE, from_, to, term, last_log_index, last_log_term)

    @staticmethod
    def vote_reply(from_: int, to: int, term: int, granted: bool) -> "Message":
        return Message(MessageKind.REQUEST_VOTE_REPLY, from_, to, term, success=granted)

    @staticmethod
    def append_entries(from_: int, to: int, term: int, prev_log_index: int, prev_log_term: int,
                       entries: list[LogEntry], leader_commit: int) -> "Message":
        return Message(MessageKind.APPEND_ENTRIES, from_, to, term, prev_log_index, prev_log_term, tuple(entries), leader_commit)

    @staticmethod
    def append_reply(from_: int, to: int, term: int, success: bool, match_index: int) -> "Message":
        return Message(MessageKind.APPEND_ENTRIES_REPLY, from_, to, term, success=success, match_index=match_index)


class Storage:
    """Figure 2, persistent state: current_term, voted_for, and log[]. The log is 1-indexed as in the paper; slot 0 holds a
    sentinel entry with term 0 so that prev_log_index = 0 always matches. Survives crash(); everything else on the node is lost."""

    def __init__(self) -> None:
        self.current_term = 0
        self.voted_for = -1
        self.log: list[LogEntry] = [LogEntry(0, "<sentinel>")]
        self.writes = 0

    def save(self, term: int, voted_for: int) -> None:
        self.current_term = term
        self.voted_for = voted_for
        self.writes += 1

    def append(self, e: LogEntry) -> None:
        self.log.append(e)
        self.writes += 1

    def truncate(self, from_index: int) -> None:
        """Delete the entry at from_index and everything after it (Figure 2, AppendEntries receiver rule 3)."""
        del self.log[from_index:]
        self.writes += 1

    def last_index(self) -> int:
        return len(self.log) - 1

    def term_at(self, index: int) -> int:
        return self.log[index].term


class KVStore:
    """The replicated state machine: a key-value store. Commands are "SET k v", "DEL k", or "NOOP". Values are strings."""

    def __init__(self) -> None:
        self.data: dict[str, str] = {}
        self.applied_commands: list[str] = []

    def apply(self, command: str) -> None:
        parts = command.split(" ")
        if parts[0] == "SET":
            if len(parts) != 3:
                raise ValueError(f"SET needs key and value: {command}")
            self.data[parts[1]] = parts[2]
        elif parts[0] == "DEL":
            if len(parts) != 2:
                raise ValueError(f"DEL needs key: {command}")
            self.data.pop(parts[1], None)
        elif parts[0] == "NOOP":
            pass
        else:
            raise ValueError(f"unknown command: {command}")
        self.applied_commands.append(command)

    def get(self, key: str) -> Optional[str]:
        return self.data.get(key)

    def __str__(self) -> str:
        return "{" + ", ".join(f"{k}={v}" for k, v in sorted(self.data.items())) + "}"


@dataclass(frozen=True)
class Config:
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
    """One Raft server: Lesson 05's election plus Figure 2's log replication, commit, and apply rules."""

    def __init__(self, id: int, peers: list[int], storage: Storage, rng: Rng, config: Config, now: int, tracer: Tracer = no_trace) -> None:
        self.id = id
        self.peers = list(peers)
        self.storage = storage
        self.rng = rng
        self.config = config
        self.tracer = tracer
        self.role = Role.FOLLOWER
        self.leader_id = -1
        # Volatile state on all servers (Figure 2). Rebuilt after a restart by re-applying the persisted log.
        self.commit_index = 0
        self.last_applied = 0
        self.state_machine = KVStore()
        # Volatile state on leaders, reinitialised after election.
        self.next_index: dict[int, int] = {}
        self.match_index: dict[int, int] = {}
        self.votes_granted: set[int] = set()
        self.election_deadline = 0
        self.next_heartbeat = 0
        self.consistency_rejections = 0
        self.reset_election_timer(now)

    @property
    def current_term(self) -> int:
        return self.storage.current_term

    @property
    def voted_for(self) -> int:
        return self.storage.voted_for

    @property
    def log(self) -> list[LogEntry]:
        return self.storage.log

    def cluster_size(self) -> int:
        return len(self.peers) + 1

    def majority(self) -> int:
        return self.cluster_size() // 2 + 1

    def last_log_index(self) -> int:
        return self.storage.last_index()

    def last_log_term(self) -> int:
        return self.storage.term_at(self.storage.last_index())

    def reset_election_timer(self, now: int) -> None:
        span = self.config.election_timeout_max - self.config.election_timeout_min + 1
        self.election_deadline = now + self.config.election_timeout_min + self.rng.next_int(span)

    def tick(self, now: int) -> list[Message]:
        if self.role == Role.LEADER:
            return self.replicate_all(now) if now >= self.next_heartbeat else []
        return self.start_election(now) if now >= self.election_deadline else []

    # ------------------------------------------------------------ election (Lesson 05, unchanged except the log check)

    def start_election(self, now: int) -> list[Message]:
        self.role = Role.CANDIDATE
        self.storage.save(self.storage.current_term + 1, self.id)
        self.leader_id = -1
        self.votes_granted = {self.id}
        self.reset_election_timer(now)
        self.tracer(now, f"{name(self.id)} election timeout -> candidate for term {self.current_term}")
        return [Message.request_vote(self.id, p, self.current_term, self.last_log_index(), self.last_log_term()) for p in self.peers]

    def handle(self, m: Message, now: int) -> list[Message]:
        if m.term > self.current_term:
            self.become_follower(m.term, now)
        if m.kind == MessageKind.REQUEST_VOTE:
            return self.on_request_vote(m, now)
        if m.kind == MessageKind.REQUEST_VOTE_REPLY:
            return self.on_vote_reply(m, now)
        if m.kind == MessageKind.APPEND_ENTRIES:
            return self.on_append_entries(m, now)
        return self.on_append_reply(m, now)

    def become_follower(self, term: int, now: int) -> None:
        before = self.role
        self.role = Role.FOLLOWER
        self.leader_id = -1
        self.votes_granted = set()
        if term > self.current_term:
            self.storage.save(term, -1)
            self.tracer(now, f"{name(self.id)} sees term {term} -> follower" + (" (was LEADER)" if before == Role.LEADER else ""))
        if before == Role.LEADER:
            self.reset_election_timer(now)

    def on_request_vote(self, m: Message, now: int) -> list[Message]:
        if m.term < self.current_term:
            return [Message.vote_reply(self.id, m.from_, self.current_term, False)]
        can_vote = self.voted_for == -1 or self.voted_for == m.from_
        # Section 5.4.1: the candidate's log must be at least as up-to-date as ours, now measured on the real log.
        log_up_to_date = m.prev_log_term > self.last_log_term() or (
            m.prev_log_term == self.last_log_term() and m.prev_log_index >= self.last_log_index())
        if can_vote and log_up_to_date:
            self.storage.save(self.current_term, m.from_)
            self.reset_election_timer(now)
            return [Message.vote_reply(self.id, m.from_, self.current_term, True)]
        return [Message.vote_reply(self.id, m.from_, self.current_term, False)]

    def on_vote_reply(self, m: Message, now: int) -> list[Message]:
        if self.role != Role.CANDIDATE or m.term != self.current_term:
            return []
        if m.success:
            self.votes_granted.add(m.from_)
            if len(self.votes_granted) >= self.majority():
                return self.become_leader(now)
        return []

    def become_leader(self, now: int) -> list[Message]:
        """Figure 2, Leaders: reinitialise nextIndex/matchIndex; section 8: commit a no-op to learn the commit index quickly."""
        self.role = Role.LEADER
        self.leader_id = self.id
        for p in self.peers:
            self.next_index[p] = self.last_log_index() + 1
            self.match_index[p] = 0
        self.tracer(now, f"{name(self.id)} wins -> LEADER of term {self.current_term}, log ends at index {self.last_log_index()}")
        self.storage.append(LogEntry(self.current_term, "NOOP"))
        return self.replicate_all(now)

    # ------------------------------------------------------------ log replication (section 5.3)

    def propose(self, command: str) -> int:
        """Client entry point. Appends to the leader's log. Returns the new index, or -1 if not leader."""
        if self.role != Role.LEADER:
            return -1
        self.storage.append(LogEntry(self.current_term, command))
        return self.last_log_index()

    def replicate_all(self, now: int) -> list[Message]:
        """One AppendEntries per peer, carrying everything from next_index onward (an empty list is a heartbeat)."""
        self.next_heartbeat = now + self.config.heartbeat_interval
        return [self.append_entries_for(p) for p in self.peers]

    def append_entries_for(self, peer: int) -> Message:
        nxt = self.next_index[peer]
        prev = nxt - 1
        return Message.append_entries(self.id, peer, self.current_term, prev, self.storage.term_at(prev), self.log[nxt:], self.commit_index)

    def on_append_entries(self, m: Message, now: int) -> list[Message]:
        """Figure 2, AppendEntries RPC, receiver implementation, rules 1-5 in order."""
        if m.term < self.current_term:  # 1
            return [Message.append_reply(self.id, m.from_, self.current_term, False, 0)]
        if self.role == Role.LEADER:
            raise RuntimeError(f"two leaders in term {self.current_term}")
        if self.role == Role.CANDIDATE:
            self.role = Role.FOLLOWER
            self.votes_granted = set()
        self.leader_id = m.from_
        self.reset_election_timer(now)
        if m.prev_log_index > self.last_log_index() or self.storage.term_at(m.prev_log_index) != m.prev_log_term:  # 2
            self.consistency_rejections += 1
            self.tracer(now, f"{name(self.id)} rejects AppendEntries: no entry ({m.prev_log_index}, t{m.prev_log_term}); "
                             f"my log ends at {self.last_log_index()}")
            return [Message.append_reply(self.id, m.from_, self.current_term, False, 0)]
        index = m.prev_log_index
        for e in m.entries:
            index += 1
            if index <= self.last_log_index():
                if self.storage.term_at(index) == e.term:
                    continue  # 4: already have it
                self.tracer(now, f"{name(self.id)} conflict at index {index}: has {self.log[index]}, leader has {e} "
                                 f"-> truncating {self.last_log_index() - index + 1} entries")
                self.storage.truncate(index)  # 3
            self.storage.append(e)  # 4
        last_new = m.prev_log_index + len(m.entries)
        if m.leader_commit > self.commit_index:  # 5
            self.commit_index = min(m.leader_commit, last_new)
            self.apply_committed(now)
        return [Message.append_reply(self.id, m.from_, self.current_term, True, last_new)]

    def on_append_reply(self, m: Message, now: int) -> list[Message]:
        """Figure 2, Leaders: on success update nextIndex/matchIndex and try to commit; on failure decrement nextIndex and retry."""
        if self.role != Role.LEADER or m.term != self.current_term:
            return []
        if m.success:
            if m.match_index > self.match_index[m.from_]:
                self.match_index[m.from_] = m.match_index
                self.next_index[m.from_] = m.match_index + 1
                self.advance_commit_index(now)
            return []
        self.next_index[m.from_] = max(1, self.next_index[m.from_] - 1)
        return [self.append_entries_for(m.from_)]

    def advance_commit_index(self, now: int) -> None:
        """Figure 2, Leaders, last rule: if there exists an N > commitIndex such that a majority of matchIndex[i] >= N and
        log[N].term == currentTerm, set commitIndex = N. The term condition is section 5.4.2 (Figure 8)."""
        for n in range(self.last_log_index(), self.commit_index, -1):
            if self.storage.term_at(n) != self.current_term:
                continue
            count = 1 + sum(1 for p in self.peers if self.match_index[p] >= n)  # the leader itself counts
            if count >= self.majority():
                self.tracer(now, f"{name(self.id)} commits through index {n} ({count}/{self.cluster_size()} replicas)")
                self.commit_index = n
                self.apply_committed(now)
                return

    def apply_committed(self, now: int) -> None:
        """Figure 2, All Servers: if commitIndex > lastApplied, increment lastApplied and apply log[lastApplied]."""
        while self.last_applied < self.commit_index:
            self.last_applied += 1
            self.state_machine.apply(self.log[self.last_applied].command)

    def replicated_count(self, index: int) -> int:
        return 1 + sum(1 for p in self.peers if self.match_index.get(p, 0) >= index)


class Network:
    """Lesson 05's deterministic network, unchanged: (deliver_at, seq) ordering, seeded delays and drops, partitions, crashes."""

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

    def is_down(self, node_id: int) -> bool:
        return node_id in self.down

    def partition(self, side_a: set[int]) -> None:
        self.side_a = frozenset(side_a)
        self.partitioned = True

    def heal(self) -> None:
        self.partitioned = False
        self.side_a = frozenset()

    def crash(self, node_id: int) -> None:
        self.down.add(node_id)

    def restart(self, node: RaftNode) -> None:
        self.down.discard(node.id)
        self.nodes[node.id] = node

    def crosses_partition(self, a: int, b: int) -> bool:
        return self.partitioned and ((a in self.side_a) != (b in self.side_a))

    def send(self, m: Message) -> None:
        self.sent += 1
        if m.from_ in self.down or m.to in self.down or self.crosses_partition(m.from_, m.to):
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


def check_log_matching(a: list[LogEntry], b: list[LogEntry], ia: int, ib: int) -> None:
    """Log Matching: if two logs contain an entry with the same index and term, the logs are identical up to that index."""
    for k in range(min(len(a), len(b)) - 1, 0, -1):
        if a[k].term == b[k].term:
            for i in range(1, k + 1):
                if a[i] != b[i]:
                    raise RuntimeError(f"LOG MATCHING: nodes {ia} and {ib} agree at index {k} (term {a[k].term}) "
                                       f"but differ at index {i}: {a[i]} vs {b[i]}")
            return


class Cluster:
    """Wires N nodes onto one network and checks the Figure 3 properties after every delivery."""

    def __init__(self, size: int, seed: int, drop_rate: float, max_delay: int, config: Config = DEFAULT_CONFIG) -> None:
        self.size = size
        self.seed = seed
        self.config = config
        self.network = Network(seed, drop_rate, max_delay)
        self.storages = [Storage() for _ in range(size)]
        self.events: list[str] = []
        self.leaders_by_term: dict[int, int] = {}
        self.committed_by_index: dict[int, LogEntry] = {}
        self.leader_log_snapshot: dict[int, list[LogEntry]] = {}
        self.restarts = [0] * size
        self.proposals = 0
        self.nodes: list[RaftNode] = []
        for i in range(size):
            node = RaftNode(i, self.peers_of(i), self.storages[i], self.node_rng(seed, i, 0), config, 0, self.trace)
            self.nodes.append(node)
            self.network.add(node)
        self.network.set_observer(self.check_invariants)

    def peers_of(self, node_id: int) -> list[int]:
        return [j for j in range(self.size) if j != node_id]

    @staticmethod
    def node_rng(seed: int, node_id: int, restart: int) -> Rng:
        return Rng(seed * 7919 + 1000003 * (node_id + 1) + 17 * restart)

    def trace(self, now: int, text: str) -> None:
        self.events.append(f"t={now:<4d} {text}")

    def now(self) -> int:
        return self.network.now

    def node(self, node_id: int) -> RaftNode:
        return self.nodes[node_id]

    def tick(self) -> None:
        self.network.tick()
        self.check_invariants()

    def run(self, ticks: int) -> None:
        for _ in range(ticks):
            self.tick()

    def run_until_stable_leader(self, max_ticks: int) -> int:
        for _ in range(max_ticks):
            self.tick()
            if self.stable_leader() is not None:
                return self.now()
        return -1

    def stable_leader(self) -> Optional[RaftNode]:
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

    def propose(self, command: str) -> int:
        """Submit a client command to the current stable leader; returns its log index or -1."""
        leader = self.stable_leader()
        return -1 if leader is None else self.propose_to(leader.id, command)

    def propose_to(self, node_id: int, command: str) -> int:
        """Submit to a specific node (which may be a stale leader inside a partition)."""
        n = self.nodes[node_id]
        index = n.propose(command)
        if index > 0:
            self.proposals += 1
            self.trace(self.now(), f"{name(node_id)} appends index {index} {n.log[index]} and replicates")
            self.network.send_all(n.replicate_all(self.now()))
        return index

    def run_until_applied(self, index: int, max_ticks: int) -> int:
        """Run until every live node has applied through `index`, or max_ticks pass. Returns the tick or -1."""
        for _ in range(max_ticks):
            self.tick()
            if all(n.last_applied >= index for n in self.nodes if not self.network.is_down(n.id)):
                return self.now()
        return -1

    def check_invariants(self) -> None:
        """Election Safety, Leader Append-Only, Log Matching, and State Machine Safety (Figure 3), checked after every delivery."""
        for n in self.nodes:
            if n.role == Role.LEADER:
                existing = self.leaders_by_term.setdefault(n.current_term, n.id)
                if existing != n.id:
                    raise RuntimeError(f"ELECTION SAFETY: term {n.current_term} has leaders {existing} and {n.id}")
                previous = self.leader_log_snapshot.get(n.id)
                if previous is not None and n.log[:len(previous)] != previous:
                    raise RuntimeError(f"LEADER APPEND-ONLY: {n.id} changed an existing entry while leader")
                self.leader_log_snapshot[n.id] = list(n.log)
            else:
                self.leader_log_snapshot.pop(n.id, None)
        for i in range(self.size):
            for j in range(i + 1, self.size):
                check_log_matching(self.nodes[i].log, self.nodes[j].log, i, j)
        for n in self.nodes:
            for k in range(1, n.commit_index + 1):
                seen = self.committed_by_index.setdefault(k, n.log[k])
                if seen != n.log[k]:
                    raise RuntimeError(f"STATE MACHINE SAFETY: index {k} committed as {seen} and {n.log[k]}")

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
        """Restart from stable storage: term, vote, and log survive; commit_index, last_applied, and the KV store are rebuilt."""
        self.restarts[node_id] += 1
        n = RaftNode(node_id, self.peers_of(node_id), self.storages[node_id],
                     self.node_rng(self.seed, node_id, self.restarts[node_id]), self.config, self.now(), self.trace)
        self.nodes[node_id] = n
        self.network.restart(n)
        self.trace(self.now(), f"{name(node_id)} restarts: term {n.current_term}, log length {n.last_log_index()}, "
                               f"commitIndex 0, empty state machine")
        return n

    def state_machines_converged(self) -> bool:
        views = {f"{n.state_machine}@{n.last_applied}" for n in self.nodes if not self.network.is_down(n.id)}
        return len(views) == 1

    def log_of(self, node_id: int) -> str:
        return "[" + ", ".join(str(e) for e in self.nodes[node_id].log[1:]) + "]"


def propose_until_accepted(c: Cluster, command: str) -> int:
    """A real client retries against the new leader after an election; here that is "run until a stable leader accepts"."""
    for _ in range(500):
        index = c.propose(command)
        if index > 0:
            return index
        c.run(10)
    raise RuntimeError(f"no leader accepted {command}")


def print_events(c: Cluster, start: int) -> None:
    for line in c.events[start:]:
        print(line)


def print_logs(c: Cluster) -> None:
    for n in c.nodes:
        print(f"  {name(n.id)} {n.role.value:<9} term {n.current_term} log={c.log_of(n.id)} commit={n.commit_index} "
              f"applied={n.last_applied} kv={n.state_machine}")


def main() -> None:
    print("== Replicate three commands on a 5-node cluster (seed 7, no drops, delay 1-5) ==")
    c = Cluster(5, 7, 0.0, 5)
    c.run_until_stable_leader(2000)
    before = len(c.events)
    c.propose("SET alice 100")
    c.propose("SET bob 50")
    last = c.propose("SET alice 75")
    applied_at = c.run_until_applied(last, 1000)
    print_events(c, before)
    print(f"all five state machines applied through index {last} at tick {applied_at}:")
    print_logs(c)

    print()
    print("== A stale leader's uncommitted entries are overwritten ==")
    old = c.stable_leader()
    assert old is not None
    follower = (old.id + 1) % 5
    before = len(c.events)
    c.partition({old.id, follower})
    c.propose_to(old.id, "SET alice 999")
    c.propose_to(old.id, "SET carol 1")
    c.run(600)
    print_events(c, before)
    print("during the partition (minority side cannot commit; majority elected a new leader and committed its own entries):")
    print_logs(c)
    before = len(c.events)
    c.heal()
    c.run_until_stable_leader(2000)
    c.propose("SET dave 7")
    leader = c.stable_leader()
    assert leader is not None
    c.run_until_applied(leader.last_log_index(), 1000)
    print_events(c, before)
    print("after heal: the minority's term-1 entries at the conflicting indexes are truncated and replaced:")
    print_logs(c)
    print(f"state machines converged = {str(c.state_machines_converged()).lower()}; alice={c.node(0).state_machine.get('alice')} "
          f"(the 999 write was never committed, so it never happened)")

    print()
    print("== Commit needs a majority of matchIndex, not a reply from anyone ==")
    q = Cluster(5, 11, 0.0, 5)
    q.run_until_stable_leader(2000)
    lead = q.stable_leader()
    assert lead is not None
    others = list(lead.peers)
    q.crash(others[0])
    q.crash(others[1])
    q.crash(others[2])
    idx = q.propose("SET balance 1000")
    q.run(400)
    print(f"3 of 5 crashed; proposed index {idx}; after 400 ticks leader commitIndex={lead.commit_index}, "
          f"entry replicated on {lead.replicated_count(idx)}/5 nodes -> not committed")
    q.restart(others[0])
    q.run(400)
    print(f"one node restarted (2 live followers + leader = 3): commitIndex={lead.commit_index}, "
          f"replicated on {lead.replicated_count(idx)}/5 -> committed and applied: balance={lead.state_machine.get('balance')}")

    print()
    print("== Only entries from the leader's current term are committed by counting replicas (section 5.4.2) ==")
    s = Storage()
    s.append(LogEntry(2, "SET x 1"))
    s.append(LogEntry(2, "SET x 2"))
    s.save(4, 0)
    l = RaftNode(0, [1, 2, 3, 4], s, Rng(1), DEFAULT_CONFIG, 0)
    l.start_election(1)
    l.handle(Message.vote_reply(1, 0, l.current_term, True), 2)
    l.handle(Message.vote_reply(2, 0, l.current_term, True), 3)
    print(f"leader of term {l.current_term} with log [{', '.join(str(e) for e in l.log[1:])}]")
    l.handle(Message.append_reply(1, 0, l.current_term, True, 2), 4)
    l.handle(Message.append_reply(2, 0, l.current_term, True, 2), 5)
    print(f"term-2 entries replicated on 3/5 (indexes 1-2): commitIndex={l.commit_index}  "
          f"(counting replicas of an older-term entry would be unsafe)")
    l.handle(Message.append_reply(1, 0, l.current_term, True, 3), 6)
    l.handle(Message.append_reply(2, 0, l.current_term, True, 3), 7)
    print(f"term-{l.current_term} NOOP at index 3 replicated on 3/5: commitIndex={l.commit_index}  "
          f"(the current-term entry commits everything before it)")

    print()
    print("== 20 seeds, 5 nodes, 20% drops, delay 1-10, a crash and restart, 12 commands: all four Figure 3 properties hold ==")
    converged = 0
    for seed in range(1, 21):
        r = Cluster(5, seed, 0.2, 10)
        r.run_until_stable_leader(3000)
        for k in range(1, 7):
            propose_until_accepted(r, f"SET k{k} v{k}")
            r.run(40)
        victim = r.stable_leader() or r.node(0)
        r.crash(victim.id)
        r.run_until_stable_leader(3000)
        for k in range(7, 13):
            propose_until_accepted(r, f"SET k{k} v{k}")
            r.run(40)
        r.restart(victim.id)
        r.run_until_stable_leader(3000)
        fin = r.stable_leader()
        if fin is not None:
            r.run_until_applied(fin.last_log_index(), 3000)
        if r.state_machines_converged() and fin is not None and fin.state_machine.get("k12") is not None:
            converged += 1
    print(f"seeds whose five state machines converged on all 12 writes: {converged}/20; no invariant check fired")


if __name__ == "__main__":
    main()
