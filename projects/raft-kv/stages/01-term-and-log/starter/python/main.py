"""Project: projects/raft-kv — stage 1 starter (Python): a Raft-replicated key-value store on a deterministic
simulated network. One module; everything the Java starter spreads over seven files lives here.
PROVIDED (do not edit): Clock, Rng, Role, MessageKind, LogEntry, Message, Config, Network, Op, History, CheckResult,
  ClientOp, Cluster, check_log_matching.
YOUR CODE: Storage (stage 1), RaftNode (stages 2-4), KVStore.apply and Checker (stage 4). Every method marked TODO
  raises NotImplementedError until you implement it; read each stage's docs/en.md for the exact rules.
Run:   python3 main.py
Grade: python3 scripts/project_test.py raft-kv --lang python --stage 1 --path <this workspace>
Sources: Ongaro & Ousterhout, "In Search of an Understandable Consensus Algorithm" (USENIX ATC 2014), Figure 2;
         Herlihy & Wing, "Linearizability" (TOPLAS 1990); Steele, Lea, Flood, "Fast Splittable PRNGs" (OOPSLA 2014).
Python 3.11+, standard library only. Same design as the Java starter and as phases/06-replication-and-consensus/05 and 06.
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
        """Persist a new (term, vote) pair and count one write. Must reject a term lower than current_term with ValueError."""
        raise NotImplementedError("TODO stage 01: Storage.save")

    def append(self, e: LogEntry) -> None:
        """Append one entry and count one write. Must reject an entry whose term is lower than last_term() with ValueError."""
        raise NotImplementedError("TODO stage 01: Storage.append")

    def truncate(self, from_index: int) -> None:
        """Delete the entry at from_index and everything after it (Figure 2, AppendEntries receiver rule 3). The sentinel stays."""
        raise NotImplementedError("TODO stage 01: Storage.truncate")

    def last_index(self) -> int:
        """Index of the last entry; 0 for an empty log."""
        raise NotImplementedError("TODO stage 01: Storage.last_index")

    def last_term(self) -> int:
        """Term of the last entry; 0 for an empty log."""
        raise NotImplementedError("TODO stage 01: Storage.last_term")

    def term_at(self, index: int) -> int:
        """Term of the entry at index (0 for the sentinel). Out of range -> IndexError."""
        raise NotImplementedError("TODO stage 01: Storage.term_at")

    def entries_from(self, from_index: int) -> list[LogEntry]:
        """A copy of log[from_index..last]; empty when from_index == last_index() + 1. This is what a leader ships in AppendEntries."""
        raise NotImplementedError("TODO stage 01: Storage.entries_from")

    def up_to_date(self, candidate_last_index: int, candidate_last_term: int) -> bool:
        """Section 5.4.1: a candidate's log is at least as up-to-date as ours if its last term is higher, or the terms are equal
        and its log is at least as long. A voter grants its vote only when this is true; that is how committed entries survive."""
        raise NotImplementedError("TODO stage 01: Storage.up_to_date")

    def describe(self) -> str:
        """log[1..last] as a string, for traces: [t1:PUT a 1, t1:NOOP]."""
        raise NotImplementedError("TODO stage 01: Storage.describe")


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
        raise NotImplementedError("TODO stage 04: KVStore.apply")

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
        raise NotImplementedError("TODO stage 02: RaftNode.reset_election_timer")

    def tick(self, now: int) -> list[Message]:
        """Leader: heartbeat when due. Follower/candidate: start an election when the deadline passes."""
        raise NotImplementedError("TODO stage 02: RaftNode.tick")

    def start_election(self, now: int) -> list[Message]:
        """Figure 2, Candidates, "On conversion to candidate": increment term, vote for self, persist, reset timer, ask everyone."""
        raise NotImplementedError("TODO stage 02: RaftNode.start_election")

    def handle(self, m: Message, now: int) -> list[Message]:
        """Figure 2, All Servers: a higher term converts any role to follower; then dispatch on m.kind."""
        raise NotImplementedError("TODO stage 02: RaftNode.handle")

    def become_follower(self, term: int, now: int) -> None:
        raise NotImplementedError("TODO stage 02: RaftNode.become_follower")

    def on_request_vote(self, m: Message, now: int) -> list[Message]:
        """Figure 2, RequestVote RPC, receiver implementation."""
        raise NotImplementedError("TODO stage 02: RaftNode.on_request_vote")

    def on_vote_reply(self, m: Message, now: int) -> list[Message]:
        """Count grants of the current term while a candidate; a majority makes a leader."""
        raise NotImplementedError("TODO stage 02: RaftNode.on_vote_reply")

    def become_leader(self, now: int) -> list[Message]:
        """Figure 2, Leaders: reinitialise next_index/match_index, append a no-op (section 8), send initial AppendEntries."""
        raise NotImplementedError("TODO stage 02: RaftNode.become_leader")

    def replicate_all(self, now: int) -> list[Message]:
        """One AppendEntries per peer; with nothing new to ship it is a heartbeat. Resets the heartbeat timer."""
        raise NotImplementedError("TODO stage 02: RaftNode.replicate_all")

    def propose(self, command: str) -> int:
        """Client entry point. Appends to the leader's log; the caller replicates. Returns the new index, or -1 if not leader."""
        raise NotImplementedError("TODO stage 03: RaftNode.propose")

    def append_entries_for(self, peer: int) -> Message:
        """The AppendEntries for one peer: everything from next_index[peer] onward, preceded by the (index, term) it must already hold."""
        raise NotImplementedError("TODO stage 03: RaftNode.append_entries_for")

    def on_append_entries(self, m: Message, now: int) -> list[Message]:
        """Figure 2, AppendEntries RPC, receiver implementation, rules 1-5 in order."""
        raise NotImplementedError("TODO stage 02: RaftNode.on_append_entries")

    def on_append_reply(self, m: Message, now: int) -> list[Message]:
        """Figure 2, Leaders: on success update next_index/match_index and try to commit; on failure decrement next_index and retry."""
        raise NotImplementedError("TODO stage 03: RaftNode.on_append_reply")

    def replicated_count(self, index: int) -> int:
        """How many nodes (counting the leader) are known to hold the entry at `index`."""
        raise NotImplementedError("TODO stage 03: RaftNode.replicated_count")

    def advance_commit_index(self, now: int) -> None:
        """Figure 2, Leaders, last rule: if there exists an N > commit_index such that a majority of match_index[i] >= N and
        log[N].term == current_term, set commit_index = N. The term condition is section 5.4.2 (Figure 8): a leader never
        commits an earlier-term entry by counting replicas; it commits it by committing one of its own entries after it."""
        raise NotImplementedError("TODO stage 04: RaftNode.advance_commit_index")

    def apply_committed(self, now: int) -> None:
        """Figure 2, All Servers: if commit_index > last_applied, increment last_applied and apply log[last_applied] to the state machine."""
        raise NotImplementedError("TODO stage 04: RaftNode.apply_committed")


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
        raise NotImplementedError("TODO stage 04: Checker.check")

    @staticmethod
    def step(state: dict[str, str], op: Op) -> Optional[dict[str, str]]:
        """The sequential specification: the next state, or None when a GET's observed value is impossible from this state.
        A matching GET returns the very same state object; a PUT returns a copy."""
        raise NotImplementedError("TODO stage 04: Checker.step")


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


# ---------------------------------------------------------------- workspace entry point (the grader never runs it)


class Echo:
    """A trivial participant: replies to every APPEND_ENTRIES with a reply carrying the same term, sends nothing on tick."""

    def __init__(self, id: int) -> None:
        self.id = id
        self.seen: list[str] = []

    def tick(self, now: int) -> list[Message]:
        return []

    def handle(self, m: Message, now: int) -> list[Message]:
        self.seen.append(f"t={now} n{self.id} got {m.kind.name} term {m.term} from n{m.from_}")
        if m.kind is MessageKind.APPEND_ENTRIES:
            return [Message.append_reply(self.id, m.from_, m.term, True, 0)]
        return []


def main() -> None:
    print("== The seeded Rng: the same seed gives the same stream, forever ==")
    a, b = Rng(42), Rng(42)
    for i in range(3):
        print(f"draw {i}: {a.next_int(1000)} == {b.next_int(1000)}")

    print()
    print("== The simulated network: delivery order is (deliver_at, sequence); nothing depends on wall-clock time ==")
    clock = Clock()
    net = Network(clock, 7, 0.0, 5)
    n0, n1 = Echo(0), Echo(1)
    net.add(n0)
    net.add(n1)
    net.send(Message.append_entries(0, 1, 1, 0, 0, [], 0))
    net.send(Message.append_entries(0, 1, 1, 0, 0, [], 0))
    for _ in range(12):
        net.tick()
    for s in n1.seen:
        print(s)
    for s in n0.seen:
        print(s)
    print(f"sent={net.sent} delivered={net.delivered} dropped={net.dropped} in-flight={net.in_flight()} at tick {clock.now()}")

    print()
    try:
        Storage().describe()
    except NotImplementedError as todo:
        print(f"not implemented yet: {todo}")
    print("Stage 1: implement Storage in main.py, then run the grader:")
    print("  python3 scripts/project_test.py raft-kv --lang python --stage 1 --path <workspace>")


if __name__ == "__main__":
    main()
