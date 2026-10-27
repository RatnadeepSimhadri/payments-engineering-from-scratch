"""Project: projects/raft-kv — reference solution (Python): a Raft-replicated key-value store on a deterministic
simulated network, all four stages in one module.
Stage 1: Clock, Rng (SplitMix64), Message, Config, Network (PROVIDED) and Storage (persistent state, the 1-indexed log).
Stage 2: RaftNode election (RequestVote, randomized timeouts, majority, step-down).
Stage 3: log replication (AppendEntries consistency check, next_index/match_index, truncation).
Stage 4: commit and apply (commit_index, last_applied, KVStore) and the Checker for linearizability.
Run:  python3 main.py
Grade: python3 scripts/project_test.py raft-kv --lang python --all --solution
Sources: Ongaro & Ousterhout, "In Search of an Understandable Consensus Algorithm" (USENIX ATC 2014), Figure 2;
         Herlihy & Wing, "Linearizability" (TOPLAS 1990); Steele, Lea, Flood, "Fast Splittable PRNGs" (OOPSLA 2014).
Python 3.11+, standard library only. Same design as the Java solution and as the simulated network of
phases/06-replication-and-consensus/05 and 06 (same Rng, same (deliver_at, seq) ordering, same rng draw order), so a
seed produces the same trace in every language.
"""

from __future__ import annotations

import heapq
from dataclasses import dataclass
from enum import Enum
from typing import Callable, Optional, Protocol, Sequence

# ---------------------------------------------------------------- Stage 1: simulation substrate (PROVIDED; do not edit)

MASK64 = (1 << 64) - 1


class Clock:
    """The injected clock. Time is an integer tick count; nothing in the project reads a wall clock."""

    def __init__(self) -> None:
        self._now = 0

    def now(self) -> int:
        return self._now

    def advance(self) -> None:
        self._now += 1


class Rng:
    """SplitMix64 (Steele, Lea, Flood, OOPSLA 2014), implemented by hand so that the same seed produces the same election
    timeouts, delays, and drops in Java, Python, and Go: a bug found in one language replays in all three."""

    def __init__(self, seed: int) -> None:
        self.state = seed & MASK64

    def next_long(self) -> int:
        """Next 64-bit output as an unsigned int (Java returns the same bits as a signed long)."""
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
        return (self.next_long() >> 11) * 2.0**-53


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
class LogEntry:
    """One log entry: the term in which the leader created it and the client command. Its index is its position in the log."""

    term: int
    command: str

    def __str__(self) -> str:
        return f"t{self.term}:{self.command}"

    __repr__ = __str__


@dataclass(frozen=True)
class Message:
    """One message. Every message carries the sender's current term (Figure 2: every RPC and reply includes the term).
    REQUEST_VOTE: prev_log_index/prev_log_term carry the candidate's last log index and term (section 5.4.1).
    APPEND_ENTRIES: prev_log_index/prev_log_term name the entry immediately before `entries` (the consistency check);
    leader_commit is the leader's commit_index. `success` doubles as vote_granted; `match_index` on a successful
    APPEND_ENTRIES_REPLY tells the leader how far the follower's log now matches."""

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
    def request_vote(from_: int, to: int, term: int, last_log_index: int, last_log_term: int) -> Message:
        return Message(MessageKind.REQUEST_VOTE, from_, to, term, last_log_index, last_log_term)

    @staticmethod
    def vote_reply(from_: int, to: int, term: int, granted: bool) -> Message:
        return Message(MessageKind.REQUEST_VOTE_REPLY, from_, to, term, success=granted)

    @staticmethod
    def append_entries(from_: int, to: int, term: int, prev_log_index: int, prev_log_term: int,
                       entries: Sequence[LogEntry], leader_commit: int) -> Message:
        return Message(MessageKind.APPEND_ENTRIES, from_, to, term, prev_log_index, prev_log_term, tuple(entries), leader_commit)

    @staticmethod
    def append_reply(from_: int, to: int, term: int, success: bool, match_index: int) -> Message:
        return Message(MessageKind.APPEND_ENTRIES_REPLY, from_, to, term, success=success, match_index=match_index)


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
Config.DEFAULT = DEFAULT_CONFIG  # type: ignore[attr-defined]

Tracer = Callable[[int, str], None]
"""Receives one line per interesting event, with the tick at which it happened."""


def no_trace(now: int, text: str) -> None:
    return None


NO_TRACE: Tracer = no_trace


def name(node_id: int) -> str:
    return f"n{node_id}"


class Node(Protocol):
    """A participant on the simulated network: an `id` attribute plus tick() and handle()."""

    id: int

    def tick(self, now: int) -> list[Message]:
        """Called once per tick with the current time; returns messages to send."""
        ...

    def handle(self, m: Message, now: int) -> list[Message]:
        """Called once per delivered message; returns replies to send."""
        ...


class Network:
    """Deterministic simulated network. Delays and drops come from the seeded Rng; delivery order is by
    (deliver_at, sequence number), so a run is a pure function of (seed, program). Partitions, crashes, and restarts are
    explicit operations, which is what makes every bug replayable. No threads, no sleeping."""

    def __init__(self, clock: Clock, seed: int, drop_rate: float, max_delay: int) -> None:
        self.clock = clock
        self.rng = Rng(seed)
        self.drop_rate = drop_rate
        self.max_delay = max(1, max_delay)
        self.nodes: dict[int, Node] = {}
        self._in_flight: list[tuple[int, int, Message]] = []
        self.down: set[int] = set()
        self.side_a: frozenset[int] = frozenset()
        self.partitioned = False
        self.observer: Callable[[], None] = lambda: None
        self.seq = 0
        self.sent = 0
        self.delivered = 0
        self.dropped = 0

    def add(self, node: Node) -> None:
        self.nodes[node.id] = node

    def set_observer(self, observer: Callable[[], None]) -> None:
        self.observer = observer

    def now(self) -> int:
        return self.clock.now()

    def in_flight(self) -> int:
        return len(self._in_flight)

    def is_down(self, node_id: int) -> bool:
        return node_id in self.down

    def is_partitioned(self) -> bool:
        return self.partitioned

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

    def restart(self, node: Node) -> None:
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
        heapq.heappush(self._in_flight, (self.clock.now() + delay, self.seq, m))
        self.seq += 1

    def send_all(self, ms: Sequence[Message]) -> None:
        for m in ms:
            self.send(m)

    def tick(self) -> None:
        """Advance the clock one tick: let every live node act on the time, then deliver everything that is due."""
        self.clock.advance()
        now = self.clock.now()
        for node_id in sorted(self.nodes):
            if node_id not in self.down:
                self.send_all(self.nodes[node_id].tick(now))
        while self._in_flight and self._in_flight[0][0] <= now:
            self._deliver(heapq.heappop(self._in_flight))

    def _deliver(self, envelope: tuple[int, int, Message]) -> None:
        m = envelope[2]
        if m.to in self.down or self.crosses_partition(m.from_, m.to):
            self.dropped += 1
            return
        self.delivered += 1
        self.send_all(self.nodes[m.to].handle(m, self.clock.now()))
        self.observer()


# ---------------------------------------------------------------- Stage 1: persistent state and the log


class Storage:
    """Figure 2, "Persistent state on all servers (updated on stable storage before responding to RPCs)": current_term,
    voted_for, log[]. In this simulation a Storage object survives crash(); everything else on the node is lost.
    The log is 1-indexed as in the paper. Slot 0 holds a sentinel entry with term 0 so that prev_log_index = 0 always
    matches and last_term() of an empty log is 0. voted_for == -1 means null. `writes` counts stable-storage writes so a
    test can prove that term, vote, and log are persisted before any reply leaves the node."""

    def __init__(self) -> None:
        self.current_term = 0
        self.voted_for = -1
        self.log: list[LogEntry] = [LogEntry(0, "<sentinel>")]
        self.writes = 0

    def save(self, term: int, voted_for: int) -> None:
        """Persist a new (term, vote) pair. A node calls this before it sends any message that depends on the pair."""
        if term < self.current_term:
            raise ValueError(f"term must not go backwards: {term} < {self.current_term}")
        self.current_term = term
        self.voted_for = voted_for
        self.writes += 1

    def append(self, e: LogEntry) -> None:
        if e.term < self.last_term():
            raise ValueError(f"log terms must be non-decreasing: {e} after term {self.last_term()}")
        self.log.append(e)
        self.writes += 1

    def truncate(self, from_index: int) -> None:
        """Delete the entry at from_index and everything after it (Figure 2, AppendEntries receiver rule 3). The sentinel stays."""
        if from_index < 1:
            raise ValueError("cannot truncate the sentinel at index 0")
        if from_index > self.last_index():
            return
        del self.log[from_index:]
        self.writes += 1

    def last_index(self) -> int:
        return len(self.log) - 1

    def last_term(self) -> int:
        return self.log[-1].term

    def term_at(self, index: int) -> int:
        if index < 0 or index > self.last_index():
            raise IndexError(f"no log entry at index {index} (last is {self.last_index()})")
        return self.log[index].term

    def entries_from(self, from_index: int) -> list[LogEntry]:
        """A copy of log[from_index..last]; empty when from_index == last_index() + 1. This is what a leader ships in AppendEntries."""
        if from_index < 1 or from_index > self.last_index() + 1:
            raise IndexError(f"entries_from({from_index}) with last index {self.last_index()}")
        return list(self.log[from_index:])

    def up_to_date(self, candidate_last_index: int, candidate_last_term: int) -> bool:
        """Section 5.4.1: a candidate's log is at least as up-to-date as ours if its last term is higher, or the terms are equal
        and its log is at least as long. A voter grants its vote only when this is true; that is how committed entries survive."""
        return candidate_last_term > self.last_term() or (candidate_last_term == self.last_term() and candidate_last_index >= self.last_index())

    def describe(self) -> str:
        """log[1..last] as a string, for traces: [t1:PUT a 1, t1:NOOP]."""
        return "[" + ", ".join(str(e) for e in self.log[1:]) + "]"


# ---------------------------------------------------------------- Stage 4: the replicated state machine


class KVStore:
    """Commands are "PUT key value", "GET key", or "NOOP". Applying the same log in the same order on every node yields
    the same store (State Machine Safety, Figure 3)."""

    def __init__(self) -> None:
        self.data: dict[str, str] = {}
        # Parallel lists: command i and its result, in apply order. Entry at log index k is applied_commands[k - 1].
        self.applied_commands: list[str] = []
        self.results: list[str] = []

    def apply(self, command: str) -> str:
        """Apply one committed command and return its result: "OK" for PUT, the stored value ("" when absent) for GET,
        "" for NOOP. A malformed or unknown command raises ValueError and is not recorded: the leader should have
        rejected it before it reached the log."""
        parts = command.split(" ", 2)
        if parts[0] == "PUT":
            if len(parts) != 3 or not parts[1]:
                raise ValueError(f"PUT needs key and value: {command}")
            self.data[parts[1]] = parts[2]
            result = "OK"
        elif parts[0] == "GET":
            if len(parts) != 2:
                raise ValueError(f"GET needs exactly a key: {command}")
            result = self.data.get(parts[1], "")
        elif parts[0] == "NOOP":
            if len(parts) != 1:
                raise ValueError(f"NOOP takes no arguments: {command}")
            result = ""
        else:
            raise ValueError(f"unknown command: {command}")
        self.applied_commands.append(command)
        self.results.append(result)
        return result

    def get(self, key: str) -> Optional[str]:
        """Current value, or None when the key was never PUT."""
        return self.data.get(key)

    def applied(self) -> int:
        return len(self.applied_commands)

    def __str__(self) -> str:
        return "{" + ", ".join(f"{k}={v}" for k, v in sorted(self.data.items())) + "}"

    __repr__ = __str__


# ---------------------------------------------------------------- Stages 2-4: one Raft server


class RaftNode:
    """Stage 2 adds election (Figure 2: Followers, Candidates, RequestVote RPC), Stage 3 log replication (AppendEntries RPC,
    next_index/match_index), Stage 4 commit and apply (commit_index, last_applied, the KV state machine)."""

    def __init__(self, id: int, peers: Sequence[int], storage: Storage, rng: Rng, config: Config, now: int, tracer: Optional[Tracer] = None) -> None:
        self.id = id
        self.peers = list(peers)
        self.storage = storage
        self.rng = rng
        self.config = config
        self.tracer: Tracer = tracer if tracer is not None else no_trace
        self.role = Role.FOLLOWER
        self.leader_id = -1
        # Volatile state on all servers (Figure 2). Rebuilt after a restart by re-applying the persisted log.
        self.commit_index = 0
        self.last_applied = 0
        self.state_machine = KVStore()
        # Volatile state on leaders, reinitialised after every election.
        self.next_index: dict[int, int] = {}
        self.match_index: dict[int, int] = {}
        self.votes_granted: set[int] = set()
        self.election_deadline = 0
        self.next_heartbeat = 0
        self.elections_started = 0
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

    def votes(self) -> int:
        return len(self.votes_granted)

    def last_log_index(self) -> int:
        return self.storage.last_index()

    def last_log_term(self) -> int:
        return self.storage.last_term()

    name = staticmethod(name)

    # ------------------------------------------------------------------ Stage 2: election

    def reset_election_timer(self, now: int) -> None:
        """Randomized timeout in [min, max] from the node's own seeded Rng: the paper's whole answer to split votes (section 5.2)."""
        span = self.config.election_timeout_max - self.config.election_timeout_min + 1
        self.election_deadline = now + self.config.election_timeout_min + self.rng.next_int(span)

    def tick(self, now: int) -> list[Message]:
        if self.role is Role.LEADER:
            return self.replicate_all(now) if now >= self.next_heartbeat else []
        # Followers and candidates: election timeout elapses -> start (another) election.
        return self.start_election(now) if now >= self.election_deadline else []

    def start_election(self, now: int) -> list[Message]:
        """Figure 2, Candidates, "On conversion to candidate": increment term, vote for self, persist, reset timer, ask everyone."""
        self.role = Role.CANDIDATE
        self.storage.save(self.storage.current_term + 1, self.id)
        self.leader_id = -1
        self.votes_granted = {self.id}
        self.elections_started += 1
        self.reset_election_timer(now)
        self.tracer(now, f"{name(self.id)} election timeout -> candidate for term {self.storage.current_term}")
        return [Message.request_vote(self.id, p, self.storage.current_term, self.last_log_index(), self.last_log_term()) for p in self.peers]

    def handle(self, m: Message, now: int) -> list[Message]:
        # Figure 2, All Servers: if RPC request or response contains term T > current_term, set current_term = T, convert to follower.
        if m.term > self.storage.current_term:
            self.become_follower(m.term, now)
        if m.kind is MessageKind.REQUEST_VOTE:
            return self.on_request_vote(m, now)
        if m.kind is MessageKind.REQUEST_VOTE_REPLY:
            return self.on_vote_reply(m, now)
        if m.kind is MessageKind.APPEND_ENTRIES:
            return self.on_append_entries(m, now)
        return self.on_append_reply(m, now)

    def become_follower(self, term: int, now: int) -> None:
        before = self.role
        self.role = Role.FOLLOWER
        self.leader_id = -1
        self.votes_granted = set()
        if term > self.storage.current_term:
            self.storage.save(term, -1)  # a new term means voted_for is null again
            suffix = " (was LEADER)" if before is Role.LEADER else " (was candidate)" if before is Role.CANDIDATE else ""
            self.tracer(now, f"{name(self.id)} sees term {term} -> follower{suffix}")
        if before is Role.LEADER:
            self.reset_election_timer(now)  # a leader keeps no election timer; give it one when it steps down

    def on_request_vote(self, m: Message, now: int) -> list[Message]:
        """Figure 2, RequestVote RPC, receiver implementation."""
        if m.term < self.storage.current_term:  # 1. reply false if term < current_term
            return [Message.vote_reply(self.id, m.from_, self.storage.current_term, False)]
        can_vote = self.storage.voted_for == -1 or self.storage.voted_for == m.from_  # 2. voted_for is null or candidate_id
        log_ok = self.storage.up_to_date(m.prev_log_index, m.prev_log_term)  # and candidate's log is at least as up-to-date
        if can_vote and log_ok:
            self.storage.save(self.storage.current_term, m.from_)  # persist the vote before the reply leaves
            self.reset_election_timer(now)  # granting a vote resets the election timer
            self.tracer(now, f"{name(self.id)} grants vote to {name(m.from_)} for term {m.term}")
            return [Message.vote_reply(self.id, m.from_, self.storage.current_term, True)]
        return [Message.vote_reply(self.id, m.from_, self.storage.current_term, False)]

    def on_vote_reply(self, m: Message, now: int) -> list[Message]:
        # A reply from an earlier election (older term) or after we stopped being a candidate is stale: ignore it.
        if self.role is not Role.CANDIDATE or m.term != self.storage.current_term:
            return []
        if m.success:
            self.votes_granted.add(m.from_)
            if len(self.votes_granted) >= self.majority():
                return self.become_leader(now)
        return []

    def become_leader(self, now: int) -> list[Message]:
        """Figure 2, Leaders: reinitialise next_index/match_index, append a no-op (section 8), send initial AppendEntries."""
        self.role = Role.LEADER
        self.leader_id = self.id
        for p in self.peers:
            self.next_index[p] = self.last_log_index() + 1
            self.match_index[p] = 0
        self.tracer(now, f"{name(self.id)} wins {len(self.votes_granted)}/{self.cluster_size()} votes -> LEADER of term "
                         f"{self.storage.current_term}, log ends at index {self.last_log_index()}")
        self.storage.append(LogEntry(self.storage.current_term, "NOOP"))  # Stage 4: lets the leader commit earlier-term entries at once
        return self.replicate_all(now)

    def replicate_all(self, now: int) -> list[Message]:
        """One AppendEntries per peer; with nothing new to ship it is a heartbeat. Resets the heartbeat timer."""
        self.next_heartbeat = now + self.config.heartbeat_interval
        return [self.append_entries_for(p) for p in self.peers]

    # ------------------------------------------------------------------ Stage 3: log replication

    def propose(self, command: str) -> int:
        """Client entry point. Appends to the leader's log; the caller replicates. Returns the new index, or -1 if not leader."""
        if self.role is not Role.LEADER:
            return -1
        self.storage.append(LogEntry(self.storage.current_term, command))
        return self.last_log_index()

    def append_entries_for(self, peer: int) -> Message:
        """The AppendEntries for one peer: everything from next_index[peer] onward, preceded by the (index, term) it must already hold."""
        nxt = self.next_index[peer]
        prev = nxt - 1
        return Message.append_entries(self.id, peer, self.storage.current_term, prev, self.storage.term_at(prev),
                                      self.storage.entries_from(nxt), self.commit_index)

    def on_append_entries(self, m: Message, now: int) -> list[Message]:
        """Figure 2, AppendEntries RPC, receiver implementation, rules 1-5 in order."""
        if m.term < self.storage.current_term:  # 1
            return [Message.append_reply(self.id, m.from_, self.storage.current_term, False, 0)]
        if self.role is Role.LEADER:
            raise RuntimeError(f"two leaders in term {self.storage.current_term}: {self.id} and {m.from_}")
        if self.role is Role.CANDIDATE:
            self.role = Role.FOLLOWER
            self.votes_granted = set()
            self.tracer(now, f"{name(self.id)} hears leader {name(m.from_)} for term {m.term} -> candidate steps down")
        self.leader_id = m.from_
        self.reset_election_timer(now)
        if m.prev_log_index > self.last_log_index() or self.storage.term_at(m.prev_log_index) != m.prev_log_term:  # 2: consistency check
            self.consistency_rejections += 1
            self.tracer(now, f"{name(self.id)} rejects AppendEntries: no entry ({m.prev_log_index}, t{m.prev_log_term}); "
                             f"my log ends at {self.last_log_index()}")
            return [Message.append_reply(self.id, m.from_, self.storage.current_term, False, 0)]
        index = m.prev_log_index
        for e in m.entries:
            index += 1
            if index <= self.last_log_index():
                if self.storage.term_at(index) == e.term:
                    continue  # 4: already have it
                self.tracer(now, f"{name(self.id)} conflict at index {index}: has {self.storage.log[index]}, leader has {e} "
                                 f"-> truncating {self.last_log_index() - index + 1}")
                self.storage.truncate(index)  # 3: delete the conflict and all after it
            self.storage.append(e)  # 4: append the new entry
        last_new = m.prev_log_index + len(m.entries)
        if m.leader_commit > self.commit_index:  # 5 (Stage 4)
            self.commit_index = min(m.leader_commit, last_new)
            self.apply_committed(now)
        return [Message.append_reply(self.id, m.from_, self.storage.current_term, True, last_new)]

    def on_append_reply(self, m: Message, now: int) -> list[Message]:
        """Figure 2, Leaders: on success update next_index/match_index and try to commit; on failure decrement next_index and retry."""
        if self.role is not Role.LEADER or m.term != self.storage.current_term:
            return []
        if m.success:
            if m.match_index > self.match_index[m.from_]:
                self.match_index[m.from_] = m.match_index
                self.next_index[m.from_] = m.match_index + 1
                self.advance_commit_index(now)  # Stage 4
            return []
        self.next_index[m.from_] = max(1, self.next_index[m.from_] - 1)
        return [self.append_entries_for(m.from_)]

    def replicated_count(self, index: int) -> int:
        """How many nodes (counting the leader) are known to hold the entry at `index`."""
        return 1 + sum(1 for p in self.peers if self.match_index.get(p, 0) >= index)

    # ------------------------------------------------------------------ Stage 4: commit and apply

    def advance_commit_index(self, now: int) -> None:
        """Figure 2, Leaders, last rule: if there exists an N > commit_index such that a majority of match_index[i] >= N and
        log[N].term == current_term, set commit_index = N. The term condition is section 5.4.2 (Figure 8): a leader never
        commits an earlier-term entry by counting replicas; it commits it by committing one of its own entries after it."""
        if self.role is not Role.LEADER:
            return
        for n in range(self.last_log_index(), self.commit_index, -1):
            if self.storage.term_at(n) != self.storage.current_term:
                continue
            if self.replicated_count(n) >= self.majority():
                self.tracer(now, f"{name(self.id)} commits through index {n} ({self.replicated_count(n)}/{self.cluster_size()} replicas)")
                self.commit_index = n
                self.apply_committed(now)
                return

    def apply_committed(self, now: int) -> None:
        """Figure 2, All Servers: if commit_index > last_applied, increment last_applied and apply log[last_applied] to the state machine."""
        while self.last_applied < self.commit_index:
            self.last_applied += 1
            self.state_machine.apply(self.storage.log[self.last_applied].command)


# ---------------------------------------------------------------- Stage 4: linearizability (Op, History, CheckResult PROVIDED)


class OpKind(Enum):
    PUT = "PUT"
    GET = "GET"


@dataclass(frozen=True)
class Op:
    """One completed client operation. For a PUT, value is what was written; for a GET, value is what came back ("" when the
    key was absent). invoked_at is the tick the client submitted it, returned_at the tick the client saw the result."""

    id: int
    client: str
    kind: OpKind
    key: str
    value: str
    invoked_at: int
    returned_at: int

    def __post_init__(self) -> None:
        if self.returned_at < self.invoked_at:
            raise ValueError(f"op {self.id} returned before it was invoked")

    def precedes(self, other: Op) -> bool:
        """Real-time order: this op finished before the other started. Otherwise the two overlap and either may go first."""
        return self.returned_at < other.invoked_at

    def __str__(self) -> str:
        body = (f"put({self.key}={self.value})" if self.kind is OpKind.PUT
                else f"get({self.key})->{'<absent>' if self.value == '' else self.value}")
        return f"#{self.id} {self.client} {body} [{self.invoked_at},{self.returned_at}]"

    __repr__ = __str__


class History:
    """An ordered collection of completed ops with dense ids 0..n-1, so the checker can track "already linearized" in one bitmask."""

    MAX_OPS = 62

    def __init__(self) -> None:
        self._ops: list[Op] = []

    def add(self, client: str, kind: OpKind, key: str, value: str, invoked_at: int, returned_at: int) -> History:
        if len(self._ops) >= History.MAX_OPS:
            raise ValueError(f"this checker handles at most {History.MAX_OPS} ops per history")
        self._ops.append(Op(len(self._ops), client, kind, key, value, invoked_at, returned_at))
        return self

    def put(self, client: str, key: str, value: str, invoked_at: int, returned_at: int) -> History:
        return self.add(client, OpKind.PUT, key, value, invoked_at, returned_at)

    def get(self, client: str, key: str, value: str, invoked_at: int, returned_at: int) -> History:
        return self.add(client, OpKind.GET, key, value, invoked_at, returned_at)

    def ops(self) -> tuple[Op, ...]:
        return tuple(self._ops)

    def size(self) -> int:
        return len(self._ops)

    def op(self, id: int) -> Op:
        return self._ops[id]


@dataclass(frozen=True)
class CheckResult:
    """The verdict: a witness order (op ids) when linearizable, the longest prefix the search ever linearized when not."""

    ok: bool
    order: tuple[int, ...]
    longest_prefix: tuple[int, ...]
    explored: int

    def describe(self, h: History) -> str:
        lines = ["LINEARIZABLE, witness order:" if self.ok
                 else f"NOT LINEARIZABLE, longest prefix ({len(self.longest_prefix)} of {h.size()}):"]
        for id in (self.order if self.ok else self.longest_prefix):
            lines.append(f"  {h.op(id)}")
        if not self.ok:
            lines.append("  no remaining op can go next from here")
        lines.append(f"  ({self.explored} states explored)")
        return "\n".join(lines)


class Checker:
    """Wing-Gong backtracking search with Lowe's (linearized-set, state) memo over a map of string registers, each initially
    absent (""). Finds a sequential order of all ops that the register model accepts and that respects real time: an op
    may be placed only when every op that returned before it was invoked has already been placed."""

    def __init__(self) -> None:
        self._ops: tuple[Op, ...] = ()
        self._failed_configs: set[str] = set()
        self._order: list[int] = []
        self._longest_prefix: list[int] = []
        self._explored = 0

    def check(self, history: History) -> CheckResult:
        self._ops = history.ops()
        self._failed_configs = set()
        self._order = []
        self._longest_prefix = []
        self._explored = 0
        ok = self._search(0, {})
        return CheckResult(ok, tuple(self._order), tuple(self._longest_prefix), self._explored)

    @staticmethod
    def step(state: dict[str, str], op: Op) -> Optional[dict[str, str]]:
        """The sequential specification: the next state, or None when a GET's observed value is impossible from this state.
        A matching GET returns the very same state object; a PUT returns a copy."""
        current = state.get(op.key, "")
        if op.kind is OpKind.GET:
            return state if current == op.value else None
        nxt = dict(state)
        nxt[op.key] = op.value
        return nxt

    def _search(self, done: int, state: dict[str, str]) -> bool:
        self._explored += 1
        if len(self._order) > len(self._longest_prefix):
            self._longest_prefix = list(self._order)
        if done.bit_count() == len(self._ops):
            return True
        memo_key = f"{done}|{sorted(state.items())}"
        if memo_key in self._failed_configs:
            return False
        for i in range(len(self._ops)):
            if done & (1 << i) or not self._may_go_next(i, done):
                continue
            nxt = Checker.step(state, self._ops[i])
            if nxt is None:
                continue
            self._order.append(i)
            if self._search(done | (1 << i), nxt):
                return True
            self._order.pop()
        self._failed_configs.add(memo_key)
        return False

    def _may_go_next(self, i: int, done: int) -> bool:
        """Op i may go next only if no not-yet-linearized op returned before i was invoked."""
        candidate = self._ops[i]
        for j, other in enumerate(self._ops):
            if j == i or done & (1 << j):
                continue
            if other.precedes(candidate):
                return False
        return True


# ---------------------------------------------------------------- test harness (PROVIDED; do not edit)


def check_log_matching(a: list[LogEntry], b: list[LogEntry], ia: int, ib: int) -> None:
    """Log Matching: if two logs contain an entry with the same index and term, the logs are identical up to that index."""
    for k in range(min(len(a), len(b)) - 1, 0, -1):
        if a[k].term == b[k].term:
            for i in range(1, k + 1):
                if a[i] != b[i]:
                    raise RuntimeError(f"LOG MATCHING: nodes {ia} and {ib} agree at index {k} (term {a[k].term}) "
                                       f"but differ at index {i}: {a[i]} vs {b[i]}")
            return


@dataclass(frozen=True)
class ClientOp:
    """A client operation the harness is waiting on: proposed to `leader_id` at log position (index, term)."""

    op_id: int
    client: str
    command: str
    leader_id: int
    index: int
    term: int
    invoked_at: int


class Cluster:
    """Wires N RaftNodes onto one simulated Network, drives the clock, checks the Figure 3 safety properties after every
    delivery, and records client operations as a History for the Stage 4 linearizability check."""

    ClientOp = ClientOp
    check_log_matching = staticmethod(check_log_matching)

    def __init__(self, size: int, seed: int, drop_rate: float, max_delay: int, config: Config = DEFAULT_CONFIG) -> None:
        self.size = size
        self.seed = seed
        self.config = config
        self.clock = Clock()
        self.network = Network(self.clock, seed, drop_rate, max_delay)
        self.storages: list[Storage] = []
        self.nodes: list[RaftNode] = []
        self.events: list[str] = []
        self.leaders_by_term: dict[int, int] = {}
        self.committed_by_index: dict[int, LogEntry] = {}
        self.pending: list[ClientOp] = []
        self.completed: list[Op] = []
        self.failed: list[ClientOp] = []
        self._leader_log_snapshot: dict[int, list[LogEntry]] = {}
        self._restarts = [0] * size
        self.proposals = 0
        self._next_op_id = 0
        for i in range(size):
            self.storages.append(Storage())
            n = RaftNode(i, self.peers_of(i), self.storages[i], self.node_rng(seed, i, 0), config, 0, self.trace)
            self.nodes.append(n)
            self.network.add(n)
        self.network.set_observer(self.check_invariants)

    def peers_of(self, node_id: int) -> list[int]:
        return [j for j in range(self.size) if j != node_id]

    @staticmethod
    def node_rng(seed: int, node_id: int, restart: int) -> Rng:
        """Each node gets its own rng stream derived from the seed, so the network's and the nodes' draws never interleave."""
        return Rng(seed * 7919 + 1000003 * (node_id + 1) + 17 * restart)

    def trace(self, now: int, text: str) -> None:
        self.events.append(f"t={now:<4d} {text}")

    def now(self) -> int:
        return self.clock.now()

    def node(self, node_id: int) -> RaftNode:
        return self.nodes[node_id]

    def tick(self) -> None:
        self.network.tick()
        self.check_invariants()
        self._settle_pending()

    def run(self, ticks: int) -> None:
        for _ in range(ticks):
            self.tick()

    def run_until_stable_leader(self, max_ticks: int) -> int:
        """Run until exactly one live node leads and every other live node follows it in the same term. Returns the tick, or -1."""
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
            if n.role is Role.LEADER:
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
        return [n for n in self.nodes if not self.network.is_down(n.id) and n.role is Role.LEADER]

    def max_term(self) -> int:
        return max(n.current_term for n in self.nodes)

    # ------------------------------------------------------------------ proposals (Stage 3) and client ops (Stage 4)

    def propose(self, command: str) -> int:
        """Submit a command to the current stable leader and replicate at once; returns its log index or -1."""
        leader = self.stable_leader()
        return -1 if leader is None else self.propose_to(leader.id, command)

    def propose_to(self, node_id: int, command: str) -> int:
        """Submit to a specific node (which may be a stale leader inside a partition)."""
        n = self.nodes[node_id]
        if self.network.is_down(node_id):
            return -1
        index = n.propose(command)
        if index > 0:
            self.proposals += 1
            self.trace(self.now(), f"{name(node_id)} appends index {index} {n.log[index]} and replicates")
            self.network.send_all(n.replicate_all(self.now()))
        return index

    def submit(self, client: str, command: str) -> int:
        """A client submits PUT/GET through the stable leader. The op is pending until the node that accepted it applies that
        log position: same term -> completed with the apply-time result; different term -> the entry was overwritten, failed.
        Returns the op id, or -1 when there is no stable leader (the client should retry later)."""
        leader = self.stable_leader()
        if leader is None:
            return -1
        invoked_at = self.now()
        index = self.propose_to(leader.id, command)
        if index < 0:
            return -1
        op_id = self._next_op_id
        self._next_op_id += 1
        self.pending.append(ClientOp(op_id, client, command, leader.id, index, leader.current_term, invoked_at))
        return op_id

    def submit_until_accepted(self, client: str, command: str, max_attempts: int) -> int:
        """Retry submit every 10 ticks until a leader accepts, like a client that follows redirects."""
        for _ in range(max_attempts):
            op_id = self.submit(client, command)
            if op_id >= 0:
                return op_id
            self.run(10)
        raise RuntimeError(f"no leader accepted {command} after {max_attempts} attempts")

    def _settle_pending(self) -> None:
        for i in range(len(self.pending) - 1, -1, -1):
            p = self.pending[i]
            n = self.nodes[p.leader_id]
            if n.last_applied < p.index:
                continue
            del self.pending[i]
            if n.storage.term_at(p.index) == p.term:
                parts = p.command.split(" ", 2)
                kind = OpKind.GET if parts[0] == "GET" else OpKind.PUT
                value = n.state_machine.results[p.index - 1] if kind is OpKind.GET else parts[2]
                self.completed.append(Op(p.op_id, p.client, kind, parts[1], value, p.invoked_at, self.now()))
            else:
                self.failed.append(p)
                self.trace(self.now(), f"client op #{p.op_id} {p.command} lost: index {p.index} was overwritten")

    def pending_count(self) -> int:
        return len(self.pending)

    def run_until_settled(self, max_ticks: int) -> int:
        """Run until no client op is pending, or max_ticks pass. Returns the tick or -1."""
        for _ in range(max_ticks):
            if not self.pending:
                return self.now()
            self.tick()
        return self.now() if not self.pending else -1

    def history(self) -> History:
        """The completed ops as a History (ids renumbered densely in completion order). Pending ops are an error."""
        if self.pending:
            raise RuntimeError(f"{len(self.pending)} client ops still pending; run until settled first")
        h = History()
        for o in self.completed:
            h.add(o.client, o.kind, o.key, o.value, o.invoked_at, o.returned_at)
        return h

    def run_until_applied(self, index: int, max_ticks: int) -> int:
        """Run until every live node has applied through `index`, or max_ticks pass. Returns the tick or -1."""
        for _ in range(max_ticks):
            self.tick()
            if all(n.last_applied >= index for n in self.nodes if not self.network.is_down(n.id)):
                return self.now()
        return -1

    def run_until_logs_converged(self, max_ticks: int) -> int:
        """Run until every live node has the same log, or max_ticks pass. Returns the tick or -1."""
        for _ in range(max_ticks):
            self.tick()
            if self.logs_converged():
                return self.now()
        return -1

    def logs_converged(self) -> bool:
        first: Optional[list[LogEntry]] = None
        for n in self.nodes:
            if self.network.is_down(n.id):
                continue
            if first is None:
                first = n.log
            elif first != n.log:
                return False
        return True

    # ------------------------------------------------------------------ invariants (Figure 3)

    def check_invariants(self) -> None:
        """Election Safety, Leader Append-Only, Log Matching, and State Machine Safety, checked after every delivery."""
        for n in self.nodes:
            if n.role is Role.LEADER:
                existing = self.leaders_by_term.setdefault(n.current_term, n.id)
                if existing != n.id:
                    raise RuntimeError(f"ELECTION SAFETY: term {n.current_term} has leaders {existing} and {n.id}")
                previous = self._leader_log_snapshot.get(n.id)
                if previous is not None and n.log[:len(previous)] != previous:
                    raise RuntimeError(f"LEADER APPEND-ONLY: {n.id} changed an existing entry while leader")
                self._leader_log_snapshot[n.id] = list(n.log)
            else:
                self._leader_log_snapshot.pop(n.id, None)
        for i in range(self.size):
            for j in range(i + 1, self.size):
                check_log_matching(self.nodes[i].log, self.nodes[j].log, i, j)
        for n in self.nodes:
            for k in range(1, n.commit_index + 1):
                seen = self.committed_by_index.setdefault(k, n.log[k])
                if seen != n.log[k]:
                    raise RuntimeError(f"STATE MACHINE SAFETY: index {k} committed as {seen} and {n.log[k]}")

    # ------------------------------------------------------------------ faults

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
        self._restarts[node_id] += 1
        n = RaftNode(node_id, self.peers_of(node_id), self.storages[node_id],
                     self.node_rng(self.seed, node_id, self._restarts[node_id]), self.config, self.now(), self.trace)
        self.nodes[node_id] = n
        self.network.restart(n)
        self.trace(self.now(), f"{name(node_id)} restarts: term {n.current_term}, log length {n.last_log_index()}, "
                               f"commitIndex 0, empty state machine")
        return n

    def expire_timer(self, node_id: int) -> None:
        """Force a node's election timer to expire on the next tick (for scripted scenarios)."""
        self.nodes[node_id].election_deadline = self.now()

    def state_machines_converged(self) -> bool:
        views = {f"{n.state_machine}@{n.last_applied}" for n in self.nodes if not self.network.is_down(n.id)}
        return len(views) <= 1

    def log_of(self, node_id: int) -> str:
        return self.nodes[node_id].storage.describe()


# ---------------------------------------------------------------- demo


def print_events(c: Cluster, start: int) -> None:
    for line in c.events[start:]:
        print(line)


def print_nodes(c: Cluster) -> None:
    for n in c.nodes:
        print(f"  {name(n.id)} {n.role.name:<9} term {n.current_term} log={c.log_of(n.id)} commit={n.commit_index} "
              f"applied={n.last_applied} kv={n.state_machine}")


def main() -> None:
    print("== Stage 2: election on a 5-node cluster (seed 7, no drops, delay 1-5 ticks) ==")
    c = Cluster(5, 7, 0.0, 5)
    elected_at = c.run_until_stable_leader(2000)
    print_events(c, 0)
    leader = c.stable_leader()
    assert leader is not None
    print(f"leader {name(leader.id)}, term {leader.current_term}, stable at tick {elected_at}; messages sent={c.network.sent} "
          f"delivered={c.network.delivered} dropped={c.network.dropped}")

    print()
    print("== Stage 3-4: three client writes and a read through the leader ==")
    before = len(c.events)
    c.submit("alice", "PUT balance:alice 100")
    c.submit("bob", "PUT balance:bob 50")
    c.submit("alice", "PUT balance:alice 75")
    c.submit("audit", "GET balance:alice")
    c.run_until_settled(1000)
    leader = c.stable_leader()
    assert leader is not None
    c.run_until_applied(leader.last_log_index(), 1000)
    print_events(c, before)
    for o in c.completed:
        print(f"  {o}")
    print_nodes(c)

    print()
    print("== A stale leader's uncommitted writes are overwritten, never applied ==")
    old = c.stable_leader()
    assert old is not None
    follower = (old.id + 1) % 5
    before = len(c.events)
    c.partition({old.id, follower})
    c.submit("mallory", "PUT balance:alice 999")
    c.run(600)
    fresh: Optional[RaftNode] = None
    for l in c.leaders():
        if fresh is None or l.current_term > fresh.current_term:
            fresh = l
    assert fresh is not None
    print(f"minority side: {name(old.id)} still leads term {old.current_term} with an uncommitted write; "
          f"majority elected {name(fresh.id)} for term {fresh.current_term}")
    c.heal()
    c.run_until_stable_leader(2000)
    c.submit_until_accepted("carol", "PUT balance:carol 7", 100)
    c.run_until_settled(2000)
    leader = c.stable_leader()
    assert leader is not None
    c.run_until_applied(leader.last_log_index(), 1000)
    print_events(c, before)
    print_nodes(c)
    print(f"failed client ops: {len(c.failed)} ({c.failed[0].command if c.failed else '-'}); "
          f"state machines converged = {str(c.state_machines_converged()).lower()}")

    print()
    print("== Linearizability of the recorded history ==")
    h = c.history()
    r = Checker().check(h)
    print(r.describe(h))

    print()
    print("== A history no Raft cluster should ever produce: a stale read ==")
    stale = (History()
             .put("alice", "x", "1", 0, 5)
             .put("bob", "x", "2", 6, 10)
             .get("carol", "x", "1", 11, 15))
    print(Checker().check(stale).describe(stale))

    print()
    print("== 20 seeds, 5 nodes, 20% drops, delay 1-10, leader crash and restart, 10 client ops each ==")
    linearizable = 0
    converged = 0
    for seed in range(1, 21):
        s = Cluster(5, seed, 0.2, 10)
        s.run_until_stable_leader(3000)
        for k in range(1, 6):
            s.submit_until_accepted(f"c{k % 2}", f"PUT k{k} v{k}", 100)
            s.run(30)
        victim = s.stable_leader() or s.node(0)
        s.crash(victim.id)
        s.run_until_stable_leader(3000)
        for k in range(1, 6):
            s.submit_until_accepted(f"c{k % 2}", f"GET k{k}", 100)
            s.run(30)
        s.restart(victim.id)
        s.run_until_stable_leader(3000)
        s.run_until_settled(3000)
        if s.pending_count() == 0 and Checker().check(s.history()).ok:
            linearizable += 1
        fin = s.stable_leader()
        if fin is not None:
            s.run_until_applied(fin.last_log_index(), 3000)
        if s.state_machines_converged():
            converged += 1
    print(f"linearizable histories: {linearizable}/20; converged state machines: {converged}/20; no invariant check fired")


if __name__ == "__main__":
    main()
