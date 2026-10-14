"""Lesson: phases/06-replication-and-consensus/07-raft-safety-and-membership/docs/en.md
Raft III: Safety Proofs, Membership Changes, Snapshots
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: Ongaro & Ousterhout, "In Search of an Understandable Consensus Algorithm" (USENIX ATC 2014): Figure 2, section 5.4
         (safety argument), section 6 (joint consensus, Figures 10-11), section 7 (log compaction, Figure 13: InstallSnapshot).
         Ongaro, "Consensus: Bridging Theory and Practice" (PhD thesis, 2014), chapter 4 (membership changes).
Python 3.11+, standard library only. Builds on Lesson 05's network and Lesson 06's log replication; same type and method names.
"""

from __future__ import annotations

import heapq
from dataclasses import dataclass
from enum import Enum
from typing import Callable, Iterable, Optional

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

    def __str__(self) -> str:
        return self.value


class MessageKind(Enum):
    REQUEST_VOTE = "REQUEST_VOTE"
    REQUEST_VOTE_REPLY = "REQUEST_VOTE_REPLY"
    APPEND_ENTRIES = "APPEND_ENTRIES"
    APPEND_ENTRIES_REPLY = "APPEND_ENTRIES_REPLY"
    INSTALL_SNAPSHOT = "INSTALL_SNAPSHOT"
    INSTALL_SNAPSHOT_REPLY = "INSTALL_SNAPSHOT_REPLY"


def fmt_set(members: Iterable[int]) -> str:
    """Java's TreeSet.toString(): "[0, 1, 2]"."""
    return "[" + ", ".join(str(m) for m in sorted(members)) + "]"


def fmt_bool(b: bool) -> str:
    return "true" if b else "false"


# ---------------------------------------------------------------------------------------------- configurations (section 6)


@dataclass(frozen=True)
class Configuration:
    """A cluster configuration. A simple configuration has only `old_members`. A joint configuration C_old,new has both
    sets, and every decision (votes, commits) needs a majority of EACH set: no quorum of C_old,new can be formed without
    a quorum of C_old and a quorum of C_new, so two leaders can never be elected by disjoint groups."""

    old_members: frozenset
    new_members: Optional[frozenset] = None

    @staticmethod
    def simple(members: Iterable[int]) -> "Configuration":
        return Configuration(frozenset(members), None)

    @staticmethod
    def joint(old: Iterable[int], nxt: Iterable[int]) -> "Configuration":
        return Configuration(frozenset(old), frozenset(nxt))

    def is_joint(self) -> bool:
        return self.new_members is not None

    def all(self) -> list[int]:
        """Everyone who must receive log entries: the union, sorted (Java iterates a TreeSet)."""
        s = set(self.old_members)
        if self.new_members is not None:
            s |= self.new_members
        return sorted(s)

    @staticmethod
    def majority_of(members: Iterable[int], acks: set) -> bool:
        members = list(members)
        count = sum(1 for m in members if m in acks)
        return count >= len(members) // 2 + 1

    def has_quorum(self, acks: set) -> bool:
        """The quorum rule. Members outside the configuration are simply not counted."""
        if not Configuration.majority_of(self.old_members, acks):
            return False
        return self.new_members is None or Configuration.majority_of(self.new_members, acks)

    @staticmethod
    def disjoint_quorums_possible(a: Iterable[int], b: Iterable[int]) -> bool:
        """Figure 10's hazard, checked by brute force: can a majority of `a` and a majority of `b` be disjoint?"""
        a, b = frozenset(a), frozenset(b)
        union = sorted(a | b)
        n = len(union)
        for mask_a in range(1 << n):
            qa = Configuration._subset(union, mask_a)
            if not Configuration.majority_of(a, qa):
                continue
            for mask_b in range(1 << n):
                if mask_a & mask_b:
                    continue
                if Configuration.majority_of(b, Configuration._subset(union, mask_b)):
                    return True
        return False

    @staticmethod
    def _subset(items: list[int], mask: int) -> set:
        return {items[i] for i in range(len(items)) if mask & (1 << i)}

    def encode(self) -> str:
        """Configurations live in the log as commands so they replicate and persist like everything else."""
        if self.new_members is None:
            return "CFG " + Configuration._join(self.old_members)
        return "CFG " + Configuration._join(self.old_members) + " -> " + Configuration._join(self.new_members)

    @staticmethod
    def decode(command: str) -> "Configuration":
        body = command[4:].strip()
        arrow = body.find("->")
        if arrow < 0:
            return Configuration.simple(Configuration._parse(body))
        return Configuration.joint(Configuration._parse(body[:arrow]), Configuration._parse(body[arrow + 2:]))

    @staticmethod
    def is_config_command(command: str) -> bool:
        return command.startswith("CFG ")

    @staticmethod
    def _join(s: Iterable[int]) -> str:
        return ",".join(str(m) for m in sorted(s))

    @staticmethod
    def _parse(s: str) -> set:
        return {int(p.strip()) for p in s.strip().split(",") if p.strip()}

    def __str__(self) -> str:
        if self.new_members is None:
            return fmt_set(self.old_members)
        return fmt_set(self.old_members) + "->" + fmt_set(self.new_members)


# ---------------------------------------------------------------------------------------------- log, snapshot, storage (section 7)


@dataclass(frozen=True)
class LogEntry:
    term: int
    command: str

    def __str__(self) -> str:
        return f"t{self.term}:{self.command}"

    __repr__ = __str__


@dataclass(frozen=True, eq=False)
class Snapshot:
    """Figure 13: the state machine's state through last_included_index, plus the latest configuration in that prefix."""

    last_included_index: int
    last_included_term: int
    data: dict
    config: Optional[Configuration]

    def __post_init__(self) -> None:
        object.__setattr__(self, "data", dict(sorted(self.data.items())))   # a copy, like new TreeMap<>(data)


@dataclass(frozen=True, eq=False)
class Message:
    kind: MessageKind
    from_: int
    to: int
    term: int
    prev_log_index: int = 0
    prev_log_term: int = 0
    entries: tuple = ()
    leader_commit: int = 0
    success: bool = False
    match_index: int = 0
    snapshot: Optional[Snapshot] = None

    @staticmethod
    def request_vote(from_: int, to: int, term: int, last_log_index: int, last_log_term: int) -> "Message":
        return Message(MessageKind.REQUEST_VOTE, from_, to, term, last_log_index, last_log_term)

    @staticmethod
    def vote_reply(from_: int, to: int, term: int, granted: bool) -> "Message":
        return Message(MessageKind.REQUEST_VOTE_REPLY, from_, to, term, success=granted)

    @staticmethod
    def append_entries(from_: int, to: int, term: int, prev_log_index: int, prev_log_term: int,
                       entries: list, leader_commit: int) -> "Message":
        return Message(MessageKind.APPEND_ENTRIES, from_, to, term, prev_log_index, prev_log_term, tuple(entries), leader_commit)

    @staticmethod
    def append_reply(from_: int, to: int, term: int, success: bool, match_index: int) -> "Message":
        return Message(MessageKind.APPEND_ENTRIES_REPLY, from_, to, term, success=success, match_index=match_index)

    @staticmethod
    def install_snapshot(from_: int, to: int, term: int, snapshot: Snapshot) -> "Message":
        return Message(MessageKind.INSTALL_SNAPSHOT, from_, to, term, snapshot.last_included_index,
                       snapshot.last_included_term, snapshot=snapshot)

    @staticmethod
    def snapshot_reply(from_: int, to: int, term: int, match_index: int) -> "Message":
        return Message(MessageKind.INSTALL_SNAPSHOT_REPLY, from_, to, term, success=True, match_index=match_index)


class Storage:
    """Persistent state with compaction. The log is 1-indexed; everything at or before snapshot_index has been discarded
    and is summarised by `snapshot`. entries[0] is a sentinel standing for index snapshot_index with term snapshot_term."""

    def __init__(self) -> None:
        self.current_term = 0
        self.voted_for = -1
        self.snapshot_index = 0
        self.snapshot_term = 0
        self.snapshot: Optional[Snapshot] = None
        self.entries: list[LogEntry] = [LogEntry(0, "<sentinel>")]
        self.writes = 0

    def save(self, term: int, voted_for: int) -> None:
        self.current_term = term
        self.voted_for = voted_for
        self.writes += 1

    def append(self, e: LogEntry) -> None:
        self.entries.append(e)
        self.writes += 1

    def last_index(self) -> int:
        return self.snapshot_index + len(self.entries) - 1

    def first_index(self) -> int:
        return self.snapshot_index + 1

    def has(self, index: int) -> bool:
        return self.snapshot_index < index <= self.last_index()

    def term_at(self, index: int) -> int:
        if index == self.snapshot_index:
            return self.snapshot_term
        if index < self.snapshot_index:
            raise RuntimeError(f"index {index} is compacted (snapshot at {self.snapshot_index})")
        return self.entries[index - self.snapshot_index].term

    def get(self, index: int) -> LogEntry:
        if not self.has(index):
            raise RuntimeError(f"no entry at {index}")
        return self.entries[index - self.snapshot_index]

    def truncate(self, from_index: int) -> None:
        while self.last_index() >= from_index:
            self.entries.pop()
        self.writes += 1

    def compact(self, s: Snapshot) -> None:
        """Discard entries through the snapshot's index, keep the rest (own compaction, and rule 6 of Figure 13)."""
        keep = [self.get(i) for i in range(s.last_included_index + 1, self.last_index() + 1)]
        self.entries = [LogEntry(s.last_included_term, "<sentinel>")] + keep
        self.snapshot_index = s.last_included_index
        self.snapshot_term = s.last_included_term
        self.snapshot = s
        self.writes += 1

    def replace_with_snapshot(self, s: Snapshot) -> None:
        """Rule 7 of Figure 13: the snapshot replaces the whole log."""
        self.entries = [LogEntry(s.last_included_term, "<sentinel>")]
        self.snapshot_index = s.last_included_index
        self.snapshot_term = s.last_included_term
        self.snapshot = s
        self.writes += 1

    def config_at(self, index: int, bootstrap: Optional[Configuration]) -> Optional[Configuration]:
        """The latest configuration at or before `index`, searching the log then the snapshot. None if none is known."""
        for i in range(min(index, self.last_index()), self.snapshot_index, -1):
            c = self.get(i).command
            if Configuration.is_config_command(c):
                return Configuration.decode(c)
        if self.snapshot is not None:
            return self.snapshot.config
        return bootstrap

    def latest_config_index(self) -> int:
        """Index of the latest configuration entry still in the log, or -1 if it is in the snapshot."""
        for i in range(self.last_index(), self.snapshot_index, -1):
            if Configuration.is_config_command(self.get(i).command):
                return i
        return -1


class KVStore:
    """The replicated state machine. Configuration entries and NOOPs are applied as no-ops but still counted."""

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
        elif parts[0] in ("NOOP", "CFG"):
            pass
        else:
            raise ValueError(f"unknown command: {command}")
        self.applied_commands.append(command)

    def reset(self, snapshot_data: dict) -> None:
        self.data = dict(snapshot_data)
        self.applied_commands.append("<snapshot>")

    def get(self, key: str) -> Optional[str]:
        return self.data.get(key)

    def __str__(self) -> str:
        return "{" + ", ".join(f"{k}={v}" for k, v in sorted(self.data.items())) + "}"


@dataclass(frozen=True)
class Config:
    election_timeout_min: int = 150
    election_timeout_max: int = 300
    heartbeat_interval: int = 50
    snapshot_threshold: int = 8

    def __post_init__(self) -> None:
        if self.election_timeout_min <= 0 or self.election_timeout_max < self.election_timeout_min or self.heartbeat_interval <= 0:
            raise ValueError("bad timing config")
        if self.heartbeat_interval >= self.election_timeout_min:
            raise ValueError("heartbeat interval must be well below the election timeout")
        if self.snapshot_threshold < 1:
            raise ValueError("snapshotThreshold must be positive")


DEFAULT_CONFIG = Config()
Tracer = Callable[[int, str], None]


def no_trace(now: int, text: str) -> None:
    return None


def name(node_id: int) -> str:
    return f"n{node_id}"


# ---------------------------------------------------------------------------------------------- the server


class RaftNode:
    """One Raft server: Lesson 06 plus configuration-aware quorums, joint consensus, and snapshots."""

    def __init__(self, id: int, bootstrap: Optional[Configuration], storage: Storage, rng: Rng, config: Config, now: int,
                 tracer: Tracer = no_trace) -> None:
        self.id = id
        self.bootstrap = bootstrap      # None for a server that joins later and learns its membership from the log
        self.storage = storage
        self.rng = rng
        self.config = config
        self.tracer = tracer
        self.role = Role.FOLLOWER
        self.leader_id = -1
        self.commit_index = 0
        self.last_applied = 0
        self.state_machine = KVStore()
        self.next_index: dict[int, int] = {}
        self.match_index: dict[int, int] = {}
        self.votes_granted: set[int] = set()
        self.election_deadline = 0
        self.next_heartbeat = 0
        self.snapshots_taken = 0
        self.snapshots_installed = 0
        self.snapshots_sent = 0
        self.stepped_down_after_removal = False
        self.reset_election_timer(now)
        if storage.snapshot is not None:
            self.state_machine.reset(storage.snapshot.data)
            self.last_applied = storage.snapshot_index
            self.commit_index = storage.snapshot_index

    @property
    def current_term(self) -> int:
        return self.storage.current_term

    def last_log_index(self) -> int:
        return self.storage.last_index()

    def last_log_term(self) -> int:
        return self.storage.term_at(self.storage.last_index())

    def configuration(self) -> Optional[Configuration]:
        """The latest configuration in my log (Figure 11: a server uses the latest configuration it has, committed or not)."""
        return self.storage.config_at(self.storage.last_index(), self.bootstrap)

    def is_member(self) -> bool:
        c = self.configuration()
        return c is not None and self.id in c.all()

    def replication_targets(self) -> list[int]:
        """Everyone I replicate to: the current configuration, plus servers leaving C_old who still need C_new. Sorted."""
        c = self.configuration()
        if c is None:
            return []
        targets = set(c.all())
        cfg_idx = self.storage.latest_config_index()
        if not c.is_joint() and cfg_idx > 0:
            before = self.storage.config_at(cfg_idx - 1, self.bootstrap)
            if before is not None:
                for m in before.all():
                    if self.match_index.get(m, 0) < cfg_idx:
                        targets.add(m)
        targets.discard(self.id)
        return sorted(targets)

    def reset_election_timer(self, now: int) -> None:
        span = self.config.election_timeout_max - self.config.election_timeout_min + 1
        self.election_deadline = now + self.config.election_timeout_min + self.rng.next_int(span)

    def tick(self, now: int) -> list[Message]:
        if self.role == Role.LEADER:
            return self.replicate_all(now) if now >= self.next_heartbeat else []
        if now < self.election_deadline:
            return []
        if not self.is_member():   # joining or removed servers never campaign
            self.reset_election_timer(now)
            return []
        return self.start_election(now)

    # ---------------------------------------------------------------- election (Lesson 05/06, quorum now configuration-aware)

    def start_election(self, now: int) -> list[Message]:
        self.role = Role.CANDIDATE
        self.storage.save(self.storage.current_term + 1, self.id)
        self.leader_id = -1
        self.votes_granted = {self.id}
        self.reset_election_timer(now)
        self.tracer(now, f"{name(self.id)} election timeout -> candidate for term {self.current_term} in config {self.configuration()}")
        return [Message.request_vote(self.id, p, self.current_term, self.last_log_index(), self.last_log_term())
                for p in self.configuration().all() if p != self.id]

    def handle(self, m: Message, now: int) -> list[Message]:
        if m.term > self.storage.current_term:
            self.become_follower(m.term, now)
        handlers = {
            MessageKind.REQUEST_VOTE: self.on_request_vote,
            MessageKind.REQUEST_VOTE_REPLY: self.on_vote_reply,
            MessageKind.APPEND_ENTRIES: self.on_append_entries,
            MessageKind.APPEND_ENTRIES_REPLY: self.on_append_reply,
            MessageKind.INSTALL_SNAPSHOT: self.on_install_snapshot,
            MessageKind.INSTALL_SNAPSHOT_REPLY: self.on_snapshot_reply,
        }
        return handlers[m.kind](m, now)

    def become_follower(self, term: int, now: int) -> None:
        before = self.role
        self.role = Role.FOLLOWER
        self.leader_id = -1
        self.votes_granted = set()
        if term > self.storage.current_term:
            self.storage.save(term, -1)
            self.tracer(now, f"{name(self.id)} sees term {term} -> follower" + (" (was LEADER)" if before == Role.LEADER else ""))
        if before == Role.LEADER:
            self.reset_election_timer(now)

    def on_request_vote(self, m: Message, now: int) -> list[Message]:
        if m.term < self.storage.current_term:
            return [Message.vote_reply(self.id, m.from_, self.storage.current_term, False)]
        can_vote = self.storage.voted_for == -1 or self.storage.voted_for == m.from_
        log_up_to_date = m.prev_log_term > self.last_log_term() or (
            m.prev_log_term == self.last_log_term() and m.prev_log_index >= self.last_log_index())
        if can_vote and log_up_to_date:
            self.storage.save(self.storage.current_term, m.from_)
            self.reset_election_timer(now)
            return [Message.vote_reply(self.id, m.from_, self.storage.current_term, True)]
        return [Message.vote_reply(self.id, m.from_, self.storage.current_term, False)]

    def on_vote_reply(self, m: Message, now: int) -> list[Message]:
        if self.role != Role.CANDIDATE or m.term != self.storage.current_term:
            return []
        if m.success:
            self.votes_granted.add(m.from_)
            if self.configuration().has_quorum(self.votes_granted):
                return self.become_leader(now)
        return []

    def become_leader(self, now: int) -> list[Message]:
        self.role = Role.LEADER
        self.leader_id = self.id
        self.next_index.clear()
        self.match_index.clear()
        self.tracer(now, f"{name(self.id)} wins -> LEADER of term {self.storage.current_term} with {len(self.votes_granted)} votes in "
                         f"{self.configuration()}, log ends at {self.last_log_index()}")
        self.storage.append(LogEntry(self.storage.current_term, "NOOP"))
        return self.replicate_all(now)

    # ---------------------------------------------------------------- replication (Lesson 06) with snapshots (section 7)

    def propose(self, command: str) -> int:
        if self.role != Role.LEADER:
            return -1
        self.storage.append(LogEntry(self.storage.current_term, command))
        return self.last_log_index()

    def change_membership(self, new_members: Iterable[int]) -> int:
        """Section 6: start a membership change. Refused while another change is in flight."""
        if self.role != Role.LEADER:
            return -1
        current = self.configuration()
        if current.is_joint() or self.storage.latest_config_index() > self.commit_index:
            return -1
        return self.propose(Configuration.joint(current.old_members, new_members).encode())

    def replicate_all(self, now: int) -> list[Message]:
        self.next_heartbeat = now + self.config.heartbeat_interval
        return [self.append_entries_for(p) for p in self.replication_targets()]

    def append_entries_for(self, peer: int) -> Message:
        """AppendEntries from next_index, or InstallSnapshot when the follower needs entries we have already compacted."""
        if peer not in self.next_index:
            self.next_index[peer] = self.last_log_index() + 1
        nxt = self.next_index[peer]
        self.match_index.setdefault(peer, 0)
        if nxt <= self.storage.snapshot_index:
            self.snapshots_sent += 1
            return Message.install_snapshot(self.id, peer, self.storage.current_term, self.storage.snapshot)
        prev = nxt - 1
        entries = [self.storage.get(i) for i in range(nxt, self.last_log_index() + 1)]
        return Message.append_entries(self.id, peer, self.storage.current_term, prev, self.storage.term_at(prev), entries, self.commit_index)

    def on_append_entries(self, m: Message, now: int) -> list[Message]:
        if m.term < self.storage.current_term:
            return [Message.append_reply(self.id, m.from_, self.storage.current_term, False, 0)]
        if self.role == Role.LEADER:
            raise RuntimeError(f"two leaders in term {self.storage.current_term}")
        if self.role == Role.CANDIDATE:
            self.role = Role.FOLLOWER
            self.votes_granted = set()
        self.leader_id = m.from_
        self.reset_election_timer(now)
        if m.prev_log_index < self.storage.snapshot_index:
            # Everything the leader is sending about is already inside my snapshot; tell it where I really am.
            return [Message.append_reply(self.id, m.from_, self.storage.current_term, True, self.storage.snapshot_index)]
        if m.prev_log_index > self.last_log_index() or self.storage.term_at(m.prev_log_index) != m.prev_log_term:
            return [Message.append_reply(self.id, m.from_, self.storage.current_term, False, 0)]
        index = m.prev_log_index
        for e in m.entries:
            index += 1
            if index <= self.last_log_index():
                if self.storage.term_at(index) == e.term:
                    continue
                self.tracer(now, f"{name(self.id)} conflict at index {index}: has {self.storage.get(index)}, leader has {e} -> truncating")
                self.storage.truncate(index)
            self.storage.append(e)
        last_new = m.prev_log_index + len(m.entries)
        if m.leader_commit > self.commit_index:
            self.commit_index = min(m.leader_commit, last_new)
            self.apply_committed(now)
        return [Message.append_reply(self.id, m.from_, self.storage.current_term, True, last_new)]

    def on_append_reply(self, m: Message, now: int) -> list[Message]:
        if self.role != Role.LEADER or m.term != self.storage.current_term:
            return []
        if m.success:
            if m.match_index > self.match_index.get(m.from_, 0):
                self.match_index[m.from_] = m.match_index
                self.next_index[m.from_] = m.match_index + 1
                return self.advance_commit_index(now)
            return []
        self.next_index[m.from_] = max(1, self.next_index.get(m.from_, self.last_log_index() + 1) - 1)
        return [self.append_entries_for(m.from_)]

    def on_install_snapshot(self, m: Message, now: int) -> list[Message]:
        """Figure 13, receiver implementation. Rule 6 keeps a log that already agrees at last_included_index; rule 7 discards it."""
        if m.term < self.storage.current_term:
            return [Message.snapshot_reply(self.id, m.from_, self.storage.current_term, 0)]
        if self.role == Role.CANDIDATE:
            self.role = Role.FOLLOWER
            self.votes_granted = set()
        self.leader_id = m.from_
        self.reset_election_timer(now)
        s = m.snapshot
        if s.last_included_index <= self.storage.snapshot_index:
            return [Message.snapshot_reply(self.id, m.from_, self.storage.current_term, self.storage.snapshot_index)]   # already have it
        self.snapshots_installed += 1
        if self.storage.has(s.last_included_index) and self.storage.term_at(s.last_included_index) == s.last_included_term:
            self.storage.compact(s)                                                                                       # 6
            self.tracer(now, f"{name(self.id)} installs snapshot through {s.last_included_index}, keeps "
                             f"{self.last_log_index() - s.last_included_index} later entries")
        else:
            self.storage.replace_with_snapshot(s)                                                                         # 7
            self.tracer(now, f"{name(self.id)} installs snapshot through {s.last_included_index} (term {s.last_included_term}), "
                             f"log discarded, config {s.config}")
        if self.last_applied < s.last_included_index:                                                                     # 8
            self.state_machine.reset(s.data)
            self.last_applied = s.last_included_index
        self.commit_index = max(self.commit_index, s.last_included_index)
        return [Message.snapshot_reply(self.id, m.from_, self.storage.current_term, s.last_included_index)]

    def on_snapshot_reply(self, m: Message, now: int) -> list[Message]:
        if self.role != Role.LEADER or m.term != self.storage.current_term:
            return []
        if m.match_index > self.match_index.get(m.from_, 0):
            self.match_index[m.from_] = m.match_index
            self.next_index[m.from_] = m.match_index + 1
            return self.advance_commit_index(now)
        return []

    def advance_commit_index(self, now: int) -> list[Message]:
        """Figure 2's commit rule with a configuration-aware quorum. Returns messages because committing C_old,new makes
        the leader append C_new immediately (section 6), and committing C_new may make it step down."""
        for n in range(self.last_log_index(), self.commit_index, -1):
            if self.storage.term_at(n) != self.storage.current_term:
                continue
            acks = {self.id} | {p for p, mi in self.match_index.items() if mi >= n}
            if self.configuration().has_quorum(acks):
                self.tracer(now, f"{name(self.id)} commits through index {n} (acks {fmt_set(acks)} satisfy {self.configuration()})")
                self.commit_index = n
                self.apply_committed(now)
                return self.after_commit(now)
        return []

    def after_commit(self, now: int) -> list[Message]:
        """Section 6: once C_old,new is committed, append C_new; once C_new is committed and I am not in it, step down."""
        c = self.configuration()
        cfg_idx = self.storage.latest_config_index()
        latest_config_committed = cfg_idx == -1 or cfg_idx <= self.commit_index
        if c.is_joint() and latest_config_committed:
            nxt = Configuration.simple(c.new_members)
            self.tracer(now, f"{name(self.id)} C_old,new committed at {cfg_idx} -> appends C_new {nxt}")
            self.propose(nxt.encode())
            return self.replicate_all(now)
        if not c.is_joint() and latest_config_committed and self.id not in c.all():
            self.tracer(now, f"{name(self.id)} C_new {c} committed and I am not in it -> step down")
            self.stepped_down_after_removal = True
            final_beat = self.replicate_all(now)   # one last round so departing followers learn the commit index
            self.role = Role.FOLLOWER
            self.leader_id = -1
            return final_beat
        return []

    def apply_committed(self, now: int) -> None:
        while self.last_applied < self.commit_index:
            self.last_applied += 1
            self.state_machine.apply(self.storage.get(self.last_applied).command)
        if self.last_applied - self.storage.snapshot_index >= self.config.snapshot_threshold:
            self.take_snapshot(now)

    def take_snapshot(self, now: int) -> None:
        """Section 7: each server snapshots independently once enough applied entries have accumulated."""
        s = Snapshot(self.last_applied, self.storage.term_at(self.last_applied), self.state_machine.data,
                     self.storage.config_at(self.last_applied, self.bootstrap))
        self.storage.compact(s)
        self.snapshots_taken += 1
        self.tracer(now, f"{name(self.id)} snapshots through index {s.last_included_index} "
                         f"(log now {self.storage.first_index()}..{self.last_log_index()}, config {s.config})")


# ---------------------------------------------------------------------------------------------- network and cluster


class Network:
    """Lesson 05's deterministic network: (deliver_at, seq) ordering, seeded delays and drops, partitions, crashes."""

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

    def partition(self, side_a: Iterable[int]) -> None:
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
        if m.to not in self.nodes or m.from_ in self.down or m.to in self.down or self.crosses_partition(m.from_, m.to):
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
        for node_id in sorted(self.nodes):   # TreeMap order
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
    """Wires `size` servers onto one network. Only `initial_members` (default: all) start in the configuration; the others
    are spare servers that join via change_membership. Checks Election Safety, Leader Completeness, Log Matching, and
    State Machine Safety after every delivery."""

    def __init__(self, size: int, seed: int, drop_rate: float, max_delay: int, config: Config = DEFAULT_CONFIG,
                 initial_members: Optional[Iterable[int]] = None) -> None:
        self.size = size
        self.seed = seed
        self.config = config
        self.initial_members = frozenset(range(size) if initial_members is None else initial_members)
        self.network = Network(seed, drop_rate, max_delay)
        self.storages = [Storage() for _ in range(size)]
        self.nodes: list[RaftNode] = []
        self.events: list[str] = []
        self.leaders_by_term: dict[int, int] = {}
        self.committed_by_index: dict[int, LogEntry] = {}
        self.applied_by_index: dict[int, str] = {}
        self.applied_seen: dict[int, int] = {}
        self.completeness_checked: set[int] = set()
        self.restarts = [0] * size
        self.proposals = 0
        boot = Configuration.simple(self.initial_members)
        for i in range(size):
            n = RaftNode(i, boot if i in self.initial_members else None, self.storages[i], self.node_rng(seed, i, 0), config, 0, self.trace)
            self.nodes.append(n)
            self.network.add(n)
        self.network.set_observer(self.check_invariants)

    @staticmethod
    def node_rng(seed: int, node_id: int, restart: int) -> Rng:
        return Rng(seed * 7919 + 1000003 * (node_id + 1) + 17 * restart)

    def trace(self, now: int, text: str) -> None:
        self.events.append(f"t={now:<5d} {text}")

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
        """One live leader, and every live member of its configuration follows it in its term."""
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
        for m in leader.configuration().all():
            n = self.nodes[m]
            if self.network.is_down(m) or n is leader:
                continue
            if n.current_term != leader.current_term or n.leader_id != leader.id:
                return None
        return leader

    def sole_leader(self) -> Optional[RaftNode]:
        """The only live server claiming leadership, or None."""
        leader: Optional[RaftNode] = None
        for n in self.nodes:
            if self.network.is_down(n.id) or n.role != Role.LEADER:
                continue
            if leader is not None:
                return None
            leader = n
        return leader

    def propose(self, command: str) -> int:
        leader = self.sole_leader()
        if leader is None:
            return -1
        index = leader.propose(command)
        if index > 0:
            self.proposals += 1
            self.network.send_all(leader.replicate_all(self.now()))
        return index

    def change_membership(self, new_members: Iterable[int]) -> int:
        leader = self.sole_leader()
        if leader is None:
            return -1
        index = leader.change_membership(new_members)
        if index > 0:
            self.trace(self.now(), f"{name(leader.id)} appends C_old,new {leader.configuration()} at index {index}")
            self.network.send_all(leader.replicate_all(self.now()))
        return index

    def run_until_applied(self, index: int, max_ticks: int) -> int:
        """Run until every live member of the current configuration has applied through `index`."""
        for _ in range(max_ticks):
            self.tick()
            leader = self.stable_leader()
            if leader is None:
                continue
            if all(self.network.is_down(m) or self.nodes[m].last_applied >= index for m in leader.configuration().all()):
                return self.now()
        return -1

    def run_until_configured(self, members: Iterable[int], max_ticks: int) -> int:
        """Run until the latest config on the leader is simple, equals `members`, and is committed."""
        target = set(members)
        for _ in range(max_ticks):
            self.tick()
            leader = self.stable_leader()
            if leader is None:
                continue
            c = leader.configuration()
            if not c.is_joint() and set(c.all()) == target and leader.storage.latest_config_index() <= leader.commit_index:
                return self.now()
        return -1

    # ---------------------------------------------------------------- the safety checker (Figure 3)

    def check_invariants(self) -> None:
        for n in self.nodes:
            if n.role != Role.LEADER:
                continue
            # Election Safety: at most one leader per term.
            existing = self.leaders_by_term.setdefault(n.current_term, n.id)
            if existing != n.id:
                raise RuntimeError(f"ELECTION SAFETY: term {n.current_term} has leaders {existing} and {n.id}")
            # Leader Completeness: every entry committed before this leader's term is in its log (or inside its snapshot).
            if n.current_term not in self.completeness_checked:
                self.completeness_checked.add(n.current_term)
                for k in sorted(self.committed_by_index):
                    v = self.committed_by_index[k]
                    if k <= n.storage.snapshot_index:
                        continue
                    if not n.storage.has(k) or n.storage.get(k) != v:
                        raise RuntimeError(f"LEADER COMPLETENESS: leader {n.id} of term {n.current_term} lacks committed entry {k}={v}")
        for n in self.nodes:
            # The committed prefix is immutable.
            for k in range(max(1, n.storage.first_index()), min(n.commit_index, n.last_log_index()) + 1):
                entry = n.storage.get(k)
                seen = self.committed_by_index.setdefault(k, entry)
                if seen != entry:
                    raise RuntimeError(f"STATE MACHINE SAFETY: index {k} committed as {seen} and {entry} on {n.id}")
            # State Machine Safety, directly: the command applied at index k is the same on every server.
            start = self.applied_seen.get(n.id, 0)
            for k in range(start + 1, n.last_applied + 1):
                if not n.storage.has(k):
                    continue   # applied via snapshot: the snapshot's source recorded it
                cmd = n.storage.get(k).command
                prior = self.applied_by_index.setdefault(k, cmd)
                if prior != cmd:
                    raise RuntimeError(f"STATE MACHINE SAFETY: index {k} applied as '{prior}' and '{cmd}'")
            self.applied_seen[n.id] = n.last_applied
        # Log Matching on the overlapping, uncompacted ranges.
        for i in range(self.size):
            for j in range(i + 1, self.size):
                Cluster.check_log_matching(self.nodes[i].storage, self.nodes[j].storage, i, j)

    @staticmethod
    def check_log_matching(a: Storage, b: Storage, ia: int, ib: int) -> None:
        low = max(a.snapshot_index, b.snapshot_index) + 1
        high = min(a.last_index(), b.last_index())
        for k in range(high, low - 1, -1):
            if a.term_at(k) == b.term_at(k):
                for i in range(low, k + 1):
                    if a.get(i) != b.get(i):
                        raise RuntimeError(f"LOG MATCHING: nodes {ia} and {ib} agree at {k} but differ at {i}")
                return

    def partition(self, side_a: Iterable[int]) -> None:
        side_a = set(side_a)
        self.network.partition(side_a)
        self.trace(self.now(), f"network partition: {fmt_set(side_a)} | rest")

    def heal(self) -> None:
        self.network.heal()
        self.trace(self.now(), "network heals")

    def crash(self, node_id: int) -> None:
        self.network.crash(node_id)
        self.trace(self.now(), f"{name(node_id)} crashes")

    def restart(self, node_id: int) -> RaftNode:
        self.restarts[node_id] += 1
        boot = Configuration.simple(self.initial_members) if node_id in self.initial_members else None
        n = RaftNode(node_id, boot, self.storages[node_id], self.node_rng(self.seed, node_id, self.restarts[node_id]),
                     self.config, self.now(), self.trace)
        self.nodes[node_id] = n
        self.network.restart(n)
        self.trace(self.now(), f"{name(node_id)} restarts: term {n.current_term}, snapshot through {n.storage.snapshot_index}, "
                               f"log to {n.last_log_index()}, config {n.configuration()}")
        return n

    def state_machines_converged(self, members: Iterable[int]) -> bool:
        first: Optional[str] = None
        for m in members:
            if self.network.is_down(m):
                continue
            s = str(self.nodes[m].state_machine)
            if first is None:
                first = s
            elif first != s:
                return False
        return True

    def log_of(self, node_id: int) -> str:
        s = self.nodes[node_id].storage
        tail = [s.get(i) for i in range(s.first_index(), s.last_index() + 1)]
        return (f"[snap<={s.snapshot_index}]" if s.snapshot_index > 0 else "") + "[" + ", ".join(str(e) for e in tail) + "]"


def print_events(c: Cluster, start: int) -> None:
    for line in c.events[start:]:
        print(line)


def print_nodes(c: Cluster) -> None:
    for n in c.nodes:
        role = "DOWN" if c.network.is_down(n.id) else n.role.value
        print(f"  {name(n.id)} {role:<9} term {n.current_term} cfg={str(n.configuration()):<16} commit={n.commit_index:<3d} "
              f"applied={n.last_applied:<3d} log={c.log_of(n.id)} kv={n.state_machine}")


def propose_until_accepted(c: Cluster, command: str) -> int:
    for _ in range(500):
        index = c.propose(command)
        if index > 0:
            return index
        c.run(10)
    raise RuntimeError(f"no leader accepted {command}")


def change_until_accepted(c: Cluster, members: Iterable[int]) -> None:
    members = set(members)
    for _ in range(500):
        if c.change_membership(members) > 0:
            return
        c.run(10)
    raise RuntimeError(f"no leader accepted membership change to {fmt_set(members)}")


def main() -> None:
    print("== Why you cannot just swap the configuration (Figure 10) ==")
    three, five = {0, 1, 2}, {0, 1, 2, 3, 4}
    print(f"C_old = {fmt_set(three)} (majority 2), C_new = {fmt_set(five)} (majority 3)")
    print(f"disjoint majorities possible with a direct switch? {fmt_bool(Configuration.disjoint_quorums_possible(three, five))}"
          "   e.g. {0,1} is a majority of C_old and {2,3,4} is a majority of C_new: two leaders in one term")
    print(f"adding exactly one server, 3 -> 4: disjoint majorities possible? {fmt_bool(Configuration.disjoint_quorums_possible(three, {0, 1, 2, 3}))}"
          "   (majorities of 3 and of 4 always overlap: single-server changes need no joint phase, which is etcd's approach)")
    joint = Configuration.joint(three, five)
    print(f"joint C_old,new = {joint}: {{0,1}} has quorum? {fmt_bool(joint.has_quorum({0, 1}))}; {{2,3,4}}? {fmt_bool(joint.has_quorum({2, 3, 4}))}"
          f"; {{0,1,3,4}}? {fmt_bool(joint.has_quorum({0, 1, 3, 4}))}  (needs a majority of BOTH)")

    print()
    print("== Grow a 3-server cluster to 5 with joint consensus, under client traffic (seed 7, delay 1-5) ==")
    c = Cluster(5, 7, 0.0, 5, Config(150, 300, 50, 1000), initial_members={0, 1, 2})
    c.run_until_stable_leader(2000)
    c.propose("SET alice 100")
    idx = c.propose("SET bob 50")
    c.run_until_applied(idx, 1000)
    before = len(c.events)
    change_until_accepted(c, five)
    c.propose("SET carol 7")   # traffic continues during the change
    done = c.run_until_configured(five, 3000)
    c.propose("SET dave 1")
    c.run_until_applied(c.stable_leader().last_log_index(), 2000)
    print_events(c, before)
    print(f"configuration {c.stable_leader().configuration()} committed at tick {done}; all five state machines agree = "
          f"{fmt_bool(c.state_machines_converged(five))}")
    print_nodes(c)

    print()
    print("== Remove the leader: it commits C_new without counting itself, then steps down ==")
    leader = c.stable_leader()
    without_leader = set(five)
    without_leader.discard(leader.id)
    before = len(c.events)
    change_until_accepted(c, without_leader)
    c.run_until_configured(without_leader, 3000)
    c.run_until_stable_leader(3000)
    propose_until_accepted(c, "SET erin 9")
    c.run_until_applied(c.stable_leader().last_log_index(), 2000)
    print_events(c, before)
    print(f"old leader {name(leader.id)} is now {leader.role.value} (steppedDownAfterRemoval={fmt_bool(leader.stepped_down_after_removal)}"
          f"); new leader {name(c.stable_leader().id)} of term {c.stable_leader().current_term} in {c.stable_leader().configuration()}")

    print()
    print("== Snapshots: compaction every 8 applied entries; a follower that falls behind gets InstallSnapshot ==")
    s = Cluster(3, 11, 0.0, 5)   # snapshot_threshold 8 from DEFAULT_CONFIG
    s.run_until_stable_leader(2000)
    for k in range(1, 6):
        s.propose(f"SET k{k} v{k}")
    s.run_until_applied(s.stable_leader().last_log_index(), 1000)
    lead = s.stable_leader()
    follower = (lead.id + 1) % 3
    before = len(s.events)
    s.crash(follower)
    for k in range(6, 17):
        s.propose(f"SET k{k} v{k}")
        s.run(20)
    s.run(200)
    print(f"while {name(follower)} is down, the leader applies and compacts:")
    print_events(s, before)
    print(f"  leader log: {s.log_of(lead.id)} (snapshots taken {lead.snapshots_taken})")
    before = len(s.events)
    s.restart(follower)
    s.run_until_applied(lead.last_log_index(), 2000)
    print_events(s, before)
    print(f"after restart: {name(follower)} log={s.log_of(follower)} kv size={len(s.node(follower).state_machine.data)}"
          f"; snapshots sent by leader={lead.snapshots_sent}; state machines converged={fmt_bool(s.state_machines_converged({0, 1, 2}))}")

    print()
    print("== Safety checker over 12 seeds: 20% drops, delay 1-10, membership 3->5->4, crash+restart, snapshots every 8 entries ==")
    converged = snapshots_installed = changes_completed = 0
    for seed in range(1, 13):
        r = Cluster(5, seed, 0.2, 10, DEFAULT_CONFIG, initial_members={0, 1, 2})
        r.run_until_stable_leader(4000)
        for k in range(1, 6):
            propose_until_accepted(r, f"SET k{k} v{k}")
            r.run(30)
        change_until_accepted(r, five)
        for k in range(6, 11):
            propose_until_accepted(r, f"SET k{k} v{k}")
            r.run(30)
        if r.run_until_configured(five, 6000) > 0:
            changes_completed += 1
        victim = r.stable_leader() if r.stable_leader() is not None else r.node(0)
        r.crash(victim.id)
        r.run_until_stable_leader(4000)
        for k in range(11, 19):
            propose_until_accepted(r, f"SET k{k} v{k}")
            r.run(30)
        r.restart(victim.id)
        r.run_until_stable_leader(4000)
        four = set(five)
        four.discard((victim.id + 1) % 5)
        change_until_accepted(r, four)
        if r.run_until_configured(four, 6000) > 0:
            changes_completed += 1
        propose_until_accepted(r, "SET final yes")
        fin = r.stable_leader()
        if fin is not None:
            r.run_until_applied(fin.last_log_index(), 4000)
        snapshots_installed += sum(n.snapshots_installed for n in r.nodes)
        if fin is not None and r.state_machines_converged(four) and fin.state_machine.get("final") is not None \
                and fin.state_machine.get("k18") is not None:
            converged += 1
    print(f"seeds converged on all writes in the final configuration: {converged}/12; membership changes completed: "
          f"{changes_completed}/24; InstallSnapshot handled: {snapshots_installed}")
    print("no Election Safety, Leader Completeness, Log Matching, or State Machine Safety check fired (each would have thrown)")


if __name__ == "__main__":
    main()
