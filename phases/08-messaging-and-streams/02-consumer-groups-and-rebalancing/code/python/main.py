"""Lesson: phases/08-messaging-and-streams/02-consumer-groups-and-rebalancing/docs/en.md
Consumer Groups, Offsets, and Rebalancing
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: Apache Kafka documentation, section 4.5 "Consumer group protocol"; KIP-54 (sticky assignor); KIP-429
         (incremental cooperative rebalancing); Kafka Consumer configs session.timeout.ms / heartbeat.interval.ms.
Python 3.11+, standard library only. Same design as the Java version. Time is injected through ManualClock; nothing sleeps.
"""

from __future__ import annotations

from dataclasses import dataclass
from enum import Enum
from typing import Dict, List, Optional, Tuple


class ManualClock:
    """Injected time source. Tests and the demo advance it by hand."""

    def __init__(self, start_millis: int) -> None:
        self._now = start_millis

    def now(self) -> int:
        return self._now

    def advance(self, millis: int) -> None:
        if millis < 0:
            raise ValueError("clock only moves forward")
        self._now += millis


@dataclass(frozen=True)
class Assignment:
    """What a member receives after a successful join: the generation it belongs to and the partitions it owns."""

    generation: int
    partitions: Tuple[int, ...]


class HeartbeatResult(Enum):
    """Answer to a heartbeat. A member that sees anything but OK must stop processing and act."""

    OK = "OK"
    REBALANCE_IN_PROGRESS = "REBALANCE_IN_PROGRESS"
    UNKNOWN_MEMBER = "UNKNOWN_MEMBER"

    def __str__(self) -> str:
        return self.name


class UnknownMemberError(Exception):
    def __init__(self, member_id: str) -> None:
        super().__init__(f"unknown member {member_id}")


class IllegalGenerationError(Exception):
    def __init__(self, member_id: str, sent: int, current: int) -> None:
        super().__init__(f"member {member_id} sent generation {sent} but the group is at {current}")


class NotOwnerError(Exception):
    def __init__(self, member_id: str, partition: int, owner: Optional[str]) -> None:
        super().__init__(f"member {member_id} does not own partition {partition} (owner: {owner})")


AssignmentMap = Dict[str, List[int]]


def format_assignment(assignment: AssignmentMap) -> str:
    """Java's Map.toString shape, so the demo and history lines read identically in both languages."""
    return "{" + ", ".join(f"{m}={ps}" for m, ps in assignment.items()) + "}"


def format_members(members: List[str]) -> str:
    """Java's List.toString shape for strings: [c1, c2] rather than ['c1', 'c2']."""
    return "[" + ", ".join(members) + "]"


class Assignor:
    """A partition assignment strategy. Input: the sorted member ids, the partition count, and the previous
    assignment (empty on the first generation). Output: member id -> sorted partitions. Deterministic by construction:
    the same inputs produce the same dict on every coordinator, every language, every run."""

    def name(self) -> str:
        raise NotImplementedError

    def assign(self, members: List[str], partition_count: int, previous: AssignmentMap) -> AssignmentMap:
        raise NotImplementedError

    @staticmethod
    def moved(before: AssignmentMap, after: AssignmentMap) -> int:
        """Number of partitions whose owner changed between two assignments (newly created partitions do not count)."""
        owner_before = Assignor.owners(before)
        owner_after = Assignor.owners(after)
        return sum(1 for p, m in owner_before.items() if owner_after.get(p) != m)

    @staticmethod
    def owners(assignment: AssignmentMap) -> Dict[int, str]:
        owners: Dict[int, str] = {}
        for m, ps in assignment.items():
            for p in ps:
                if p in owners:
                    raise RuntimeError(f"partition {p} assigned twice")
                owners[p] = m
        return dict(sorted(owners.items()))


class RangeAssignor(Assignor):
    """Contiguous ranges: the first (partitions mod members) members receive one extra partition. Kafka's default before 2.4."""

    def name(self) -> str:
        return "range"

    def assign(self, members: List[str], partition_count: int, previous: AssignmentMap) -> AssignmentMap:
        out: AssignmentMap = {}
        if not members:
            return out
        per_member = partition_count // len(members)
        extra = partition_count % len(members)
        nxt = 0
        for i, m in enumerate(members):
            count = per_member + (1 if i < extra else 0)
            out[m] = list(range(nxt, nxt + count))
            nxt += count
        return out


class RoundRobinAssignor(Assignor):
    """Deal partitions like cards: partition p goes to member p mod n. Balanced, but a membership change moves almost everything."""

    def name(self) -> str:
        return "round-robin"

    def assign(self, members: List[str], partition_count: int, previous: AssignmentMap) -> AssignmentMap:
        out: AssignmentMap = {}
        if not members:
            return out
        for m in members:
            out[m] = []
        for p in range(partition_count):
            out[members[p % len(members)]].append(p)
        return out


class StickyAssignor(Assignor):
    """Keep what survivors already own, hand orphaned partitions to the least-loaded member, then move single partitions
    from the most-loaded to the least-loaded member until the spread is at most one. Minimises movement (KIP-54)."""

    def name(self) -> str:
        return "sticky"

    def assign(self, members: List[str], partition_count: int, previous: AssignmentMap) -> AssignmentMap:
        out: AssignmentMap = {}
        if not members:
            return out
        for m in members:
            out[m] = []
        unassigned = set(range(partition_count))
        # 1. survivors keep their partitions
        for m in members:
            for p in previous.get(m, []):
                if p < partition_count and p in unassigned:
                    unassigned.remove(p)
                    out[m].append(p)
        # 2. orphans go to whoever has the fewest (ties: lowest member id, because members are sorted)
        for p in sorted(unassigned):
            out[self._least_loaded(members, out)].append(p)
        # 3. level: move one partition at a time from the heaviest to the lightest while the spread exceeds one
        while True:
            most, least = self._most_loaded(members, out), self._least_loaded(members, out)
            if len(out[most]) - len(out[least]) <= 1:
                break
            p = out[most].pop()
            out[least].append(p)
        for l in out.values():
            l.sort()
        return out

    @staticmethod
    def _least_loaded(members: List[str], out: AssignmentMap) -> str:
        best = members[0]
        for m in members:
            if len(out[m]) < len(out[best]):
                best = m
        return best

    @staticmethod
    def _most_loaded(members: List[str], out: AssignmentMap) -> str:
        best = members[0]
        for m in members:
            if len(out[m]) > len(out[best]):
                best = m
        return best


class GroupCoordinator:
    """The group coordinator for one topic. Owns membership, liveness (heartbeats against an injected clock), the
    generation counter, the current assignment, and the committed offsets. Every mutation that changes membership
    bumps the generation and recomputes the assignment; commits carrying an older generation are fenced."""

    def __init__(self, group_id: str, partition_count: int, assignor: Assignor, session_timeout_ms: int, clock: ManualClock) -> None:
        if partition_count <= 0:
            raise ValueError("partitionCount must be positive")
        if session_timeout_ms <= 0:
            raise ValueError("sessionTimeoutMs must be positive")
        self._group_id = group_id
        self._partition_count = partition_count
        self._assignor = assignor
        self._session_timeout_ms = session_timeout_ms
        self._clock = clock
        self._generation = 0
        self._last_heartbeat: Dict[str, int] = {}      # memberId -> millis of last contact (kept sorted on read)
        self._assignment: AssignmentMap = {}
        self._committed: Dict[int, int] = {}           # partition -> next offset to read
        self._history: List[str] = []                  # human-readable rebalance log

    def generation(self) -> int:
        return self._generation

    def members(self) -> List[str]:
        return sorted(self._last_heartbeat.keys())

    def history(self) -> List[str]:
        return list(self._history)

    def assignment(self) -> AssignmentMap:
        return {m: list(ps) for m, ps in self._assignment.items()}

    def join(self, member_id: str) -> Assignment:
        """A member joins (or re-joins after being told to). Membership changes trigger a rebalance; a re-join does not."""
        is_new = member_id not in self._last_heartbeat
        self._last_heartbeat[member_id] = self._clock.now()
        if is_new:
            self._rebalance(f"join of {member_id}")
        return Assignment(self._generation, tuple(self._assignment.get(member_id, [])))

    def leave(self, member_id: str) -> None:
        """A member announces it is leaving. Its partitions are reassigned immediately rather than after the session timeout."""
        if self._last_heartbeat.pop(member_id, None) is None:
            raise UnknownMemberError(member_id)
        self._rebalance(f"leave of {member_id}")

    def heartbeat(self, member_id: str, member_generation: int) -> HeartbeatResult:
        """Heartbeat from a member that believes it is in member_generation. OK refreshes the session; REBALANCE_IN_PROGRESS
        tells the member its generation is stale and it must re-join before touching any partition."""
        if member_id not in self._last_heartbeat:
            return HeartbeatResult.UNKNOWN_MEMBER
        self._last_heartbeat[member_id] = self._clock.now()
        return HeartbeatResult.OK if member_generation == self._generation else HeartbeatResult.REBALANCE_IN_PROGRESS

    def expire_sessions(self) -> List[str]:
        """Evict every member whose last heartbeat is older than the session timeout. Called on the coordinator's own tick."""
        deadline = self._clock.now() - self._session_timeout_ms
        dead = [m for m in self.members() if self._last_heartbeat[m] <= deadline]
        if dead:
            for m in dead:
                del self._last_heartbeat[m]
            self._rebalance(f"session timeout of {format_members(dead)}")
        return dead

    def _rebalance(self, reason: str) -> None:
        previous = self._assignment
        self._assignment = self._assignor.assign(self.members(), self._partition_count, previous)
        self._generation += 1
        self._history.append(f"generation {self._generation} ({reason}): {format_assignment(self._assignment)} "
                             f"moved={Assignor.moved(previous, self._assignment)}")

    def owner_of(self, partition: int) -> Optional[str]:
        """The member that owns partition in the current generation, or None when the group is empty."""
        if partition < 0 or partition >= self._partition_count:
            raise ValueError(f"no partition {partition}")
        return Assignor.owners(self._assignment).get(partition)

    def commit_offset(self, member_id: str, member_generation: int, partition: int, next_offset: int) -> None:
        """Commit the next offset to read for a partition. Fenced three ways: the member must be known, must be in the
        current generation, and must own the partition. A zombie that lost its partitions cannot overwrite the new
        owner's progress."""
        if member_id not in self._last_heartbeat:
            raise UnknownMemberError(member_id)
        if member_generation != self._generation:
            raise IllegalGenerationError(member_id, member_generation, self._generation)
        owner = self.owner_of(partition)
        if member_id != owner:
            raise NotOwnerError(member_id, partition, owner)
        if next_offset < 0:
            raise ValueError("offset must be non-negative")
        self._committed[partition] = next_offset

    def committed_offset(self, partition: int) -> int:
        """The committed next-offset-to-read for a partition, or 0 when nothing has been committed yet."""
        return self._committed.get(partition, 0)

    def group_id(self) -> str:
        return self._group_id


def _print_assignment(g: GroupCoordinator) -> None:
    print(f"  generation {g.generation()}, members {format_members(g.members())}")
    for m, ps in g.assignment().items():
        print(f"    {m:<4} owns {ps}")


def main() -> None:
    clock = ManualClock(1_700_000_000_000)
    partitions = 8
    session_timeout = 10_000

    print("== Three consumers join group 'ledger-poster' (8 partitions, range assignor) ==")
    rng = GroupCoordinator("ledger-poster", partitions, RangeAssignor(), session_timeout, clock)
    a1 = rng.join("c1")
    rng.join("c2")
    a3 = rng.join("c3")
    _print_assignment(rng)
    print(f"  c1 joined in generation {a1.generation} and was handed {list(a1.partitions)}"
          f"; after c2 and c3 the group is at generation {rng.generation()}")
    print(f"  c1's heartbeat with generation {a1.generation} -> {rng.heartbeat('c1', a1.generation)}")
    a1 = rng.join("c1")
    print(f"  c1 re-joins and now holds generation {a1.generation} with {list(a1.partitions)}")

    print("\n== c2 stops heartbeating; who owns partition 3? ==")
    clock.advance(4_000)
    rng.heartbeat("c1", rng.generation())
    rng.heartbeat("c3", a3.generation)
    print(f"  t+4s : expired = {format_members(rng.expire_sessions())} (c2 still inside its 10s session)")
    clock.advance(6_000)
    rng.heartbeat("c1", rng.generation())
    rng.heartbeat("c3", rng.generation())
    print(f"  t+10s: expired = {format_members(rng.expire_sessions())}")
    _print_assignment(rng)
    print(f"  partition 3 is owned by {rng.owner_of(3)}")

    print("\n== The zombie: c2 comes back from its GC pause and tries to commit partition 3 ==")
    try:
        rng.commit_offset("c2", 3, 3, 1_042)
    except UnknownMemberError as e:
        print(f"  rejected: {e}")
    rng.commit_offset(rng.owner_of(3), rng.generation(), 3, 1_040)
    print(f"  new owner committed next offset {rng.committed_offset(3)} for partition 3")
    zombie = rng.join("c2")
    print(f"  c2 re-joins -> generation {zombie.generation}, owns {list(zombie.partitions)}")
    try:
        rng.commit_offset("c2", zombie.generation - 1, zombie.partitions[0], 7)
    except IllegalGenerationError as e:
        print(f"  stale generation rejected: {e}")

    print("\n== Adding a fourth consumer: how many partitions move? ==")
    for assignor in (RangeAssignor(), RoundRobinAssignor(), StickyAssignor()):
        three = ["c1", "c2", "c3"]
        before = assignor.assign(three, partitions, {})
        after = assignor.assign(["c1", "c2", "c3", "c4"], partitions, before)
        print(f"  {assignor.name():<11} before={format_assignment(before)}")
        print(f"  {'':<11} after ={format_assignment(after)} moved={Assignor.moved(before, after)}")

    print("\n== Rebalance history (range group) ==")
    for h in rng.history():
        print(f"  {h}")


if __name__ == "__main__":
    main()
