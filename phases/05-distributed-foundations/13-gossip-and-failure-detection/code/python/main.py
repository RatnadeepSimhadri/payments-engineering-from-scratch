"""Lesson: phases/05-distributed-foundations/13-gossip-and-failure-detection/docs/en.md
Gossip Protocols and Failure Detection
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: Das, Gupta, Motivala, "SWIM: Scalable Weakly-consistent Infection-style Process Group Membership Protocol"
(DSN 2002), sections 3, 4.1, 4.2, 4.3. Deterministic simulated network: seeded delays, drops, partitions, crashes.
"""

from __future__ import annotations

import heapq
import math
from dataclasses import dataclass
from enum import Enum
from typing import Callable, Optional

MASK64 = (1 << 64) - 1


def to_int64(x: int) -> int:
    """Wrap to a signed 64-bit long, as Java arithmetic does."""
    x &= MASK64
    return x - (1 << 64) if x >= (1 << 63) else x


class Rng:
    """SplitMix64: the same stream in every language, so a seed replays everywhere."""

    def __init__(self, seed: int) -> None:
        self._state = seed & MASK64

    def next_long(self) -> int:
        self._state = (self._state + 0x9E3779B97F4A7C15) & MASK64
        z = self._state
        z = ((z ^ (z >> 30)) * 0xBF58476D1CE4E5B9) & MASK64
        z = ((z ^ (z >> 27)) * 0x94D049BB133111EB) & MASK64
        return to_int64(z ^ (z >> 31))

    def next_int(self, bound: int) -> int:
        return ((self.next_long() & MASK64) >> 33) % bound

    def next_double(self) -> float:
        return ((self.next_long() & MASK64) >> 11) * (1.0 / (1 << 53))

    def shuffle(self, items: list) -> None:
        """Fisher-Yates with this generator (not random.shuffle), so permutations replay with the seed."""
        for i in range(len(items) - 1, 0, -1):
            j = self.next_int(i + 1)
            items[i], items[j] = items[j], items[i]


class MemberState(Enum):
    """DEAD is terminal for an incarnation; a node that comes back must rejoin with a fresh identity in production."""

    ALIVE = 0
    SUSPECT = 1
    DEAD = 2


@dataclass(frozen=True)
class Update:
    """One piece of membership information: node, state, incarnation. Only the node described may raise its incarnation,
    and it does so to refute a suspicion. Precedence rules (SWIM section 4.2):
      alive(i)   overrides alive(j), suspect(j)  when i > j
      suspect(i) overrides alive(j)              when i >= j
      suspect(i) overrides suspect(j)            when i > j
      dead(any)  overrides everything"""

    member: int
    state: MemberState
    incarnation: int

    def overrides(self, current: Optional["Update"]) -> bool:
        if current is None:
            return True
        if current.state is MemberState.DEAD:
            return False
        if self.state is MemberState.DEAD:
            return True
        if self.state is MemberState.ALIVE:
            return self.incarnation > current.incarnation
        # SUSPECT
        if current.state is MemberState.ALIVE:
            return self.incarnation >= current.incarnation
        return self.incarnation > current.incarnation

    def __str__(self) -> str:
        return f"n{self.member}:{self.state.name}@{self.incarnation}"


class MessageKind(Enum):
    PING = 0
    ACK = 1
    PING_REQ = 2
    INDIRECT_ACK = 3


@dataclass(frozen=True)
class Message:
    """One protocol message. PING/ACK carry a probe sequence number; PING_REQ asks the receiver to ping `target` on the
    sender's behalf, and the resulting INDIRECT_ACK is relayed back. Every message piggybacks a bounded list of
    membership updates, which is the entire dissemination mechanism."""

    kind: MessageKind
    from_: int
    to: int
    target: int
    seq: int
    piggyback: tuple

    @staticmethod
    def ping(frm: int, to: int, seq: int, gossip: list) -> "Message":
        return Message(MessageKind.PING, frm, to, to, seq, tuple(gossip))

    @staticmethod
    def ack(frm: int, to: int, seq: int, gossip: list) -> "Message":
        return Message(MessageKind.ACK, frm, to, frm, seq, tuple(gossip))

    @staticmethod
    def ping_req(frm: int, to: int, target: int, seq: int, gossip: list) -> "Message":
        return Message(MessageKind.PING_REQ, frm, to, target, seq, tuple(gossip))

    @staticmethod
    def indirect_ack(frm: int, to: int, target: int, seq: int, gossip: list) -> "Message":
        return Message(MessageKind.INDIRECT_ACK, frm, to, target, seq, tuple(gossip))


class Node:
    def id(self) -> int:
        raise NotImplementedError

    def tick(self, now: int) -> list[Message]:
        raise NotImplementedError

    def handle(self, m: Message, now: int) -> list[Message]:
        raise NotImplementedError


@dataclass(frozen=True)
class Config:
    """Timing of one protocol period, in ticks. Defaults: probe every 12 ticks, wait 4 for a direct ack, suspect for
    4 periods."""

    protocol_period: int
    ack_timeout: int
    indirect_probes: int
    suspicion_periods: int
    max_piggyback: int
    retransmit_factor: int

    def __post_init__(self) -> None:
        if self.ack_timeout <= 0 or self.ack_timeout * 2 >= self.protocol_period:
            raise ValueError("need ackTimeout*2 < protocolPeriod so the indirect probe fits in the period")
        if (self.indirect_probes < 0 or self.suspicion_periods <= 0 or self.max_piggyback <= 0
                or self.retransmit_factor <= 0):
            raise ValueError("bad config")


Config.DEFAULT = Config(12, 4, 3, 4, 6, 3)  # type: ignore[attr-defined]


class Probe:
    """A pending direct probe: who we pinged, when, and whether we already escalated to indirect probing."""

    def __init__(self, target: int, seq: int, started_at: int) -> None:
        self.target = target
        self.seq = seq
        self.started_at = started_at
        self.indirect_sent = False
        self.acked = False


Tracer = Callable[[int, str], None]


class SwimNode(Node):
    """One SWIM member. Holds its view of the group, runs the probe cycle, and gossips updates by piggybacking."""

    def __init__(self, id_: int, initial_members: list[int], config: Config, rng: Rng, now: int,
                 tracer: Tracer) -> None:
        self._id = id_
        self._config = config
        self._rng = rng
        self._tracer = tracer
        self.incarnation = 0                             # bumped only to refute a suspicion about myself
        self.view: dict[int, Update] = {}                # iterated in key order, like Java's TreeMap
        self.suspected_at: dict[int, int] = {}
        self._gossip_queue: dict[Update, int] = {}       # insertion-ordered, like LinkedHashMap
        for m in initial_members:
            self.view[m] = Update(m, MemberState.ALIVE, 0)
        self.view[id_] = Update(id_, MemberState.ALIVE, 0)
        self._probe_order: list[int] = []
        self._probe_cursor = 0
        self._next_period_at = now + 1 + rng.next_int(config.protocol_period)   # desynchronise periods
        self._next_seq = (id_ << 32) + 1                                       # seqs unique across the group
        self._current: Optional[Probe] = None
        self._relaying: dict[int, int] = {}              # indirect probes we relay for others: seq -> origin
        self.messages_sent = 0
        self.direct_acks = 0
        self.indirect_acks = 0
        self.suspicions_raised = 0
        self.refutations = 0
        self.declared_dead = 0

    def id(self) -> int:
        return self._id

    def _view_values(self) -> list[Update]:
        return [self.view[k] for k in sorted(self.view)]

    def state_of(self, member: int) -> Optional[MemberState]:
        u = self.view.get(member)
        return None if u is None else u.state

    def alive_members(self) -> list[int]:
        """Sorted, like iterating Java's TreeSet."""
        return [u.member for u in self._view_values() if u.state is MemberState.ALIVE]

    def dead_members(self) -> list[int]:
        return [u.member for u in self._view_values() if u.state is MemberState.DEAD]

    # ---------------------------------------------------------------- the protocol period (section 3, 4.3)

    def tick(self, now: int) -> list[Message]:
        out: list[Message] = []
        cur = self._current
        if cur is not None and not cur.acked and not cur.indirect_sent and now >= cur.started_at + self._config.ack_timeout:
            out.extend(self.send_indirect_probes(now))
        if now >= self._next_period_at:
            self._next_period_at = now + self._config.protocol_period
            self.finish_probe(now)
            self.expire_suspicions(now)
            target = self.next_probe_target()
            if target is not None:
                self._current = Probe(target, self._next_seq, now)
                self._next_seq += 1
                out.append(Message.ping(self._id, target, self._current.seq, self.take_gossip()))
        self.messages_sent += len(out)
        return out

    def next_probe_target(self) -> Optional[int]:
        """Round-robin over a random permutation of members, reshuffled each pass: bounded worst-case detection."""
        candidates = [u.member for u in self._view_values() if u.member != self._id and u.state is not MemberState.DEAD]
        if not candidates:
            return None
        if self._probe_cursor >= len(self._probe_order) or not set(self._probe_order) <= set(candidates):
            self._probe_order = candidates
            self._rng.shuffle(self._probe_order)
            self._probe_cursor = 0
        target = self._probe_order[self._probe_cursor]
        self._probe_cursor += 1
        return target

    def send_indirect_probes(self, now: int) -> list[Message]:
        """No direct ack in time: ask k other members to ping the target for us (section 3, step 2)."""
        cur = self._current
        cur.indirect_sent = True
        helpers = [u.member for u in self._view_values()
                   if u.member != self._id and u.member != cur.target and u.state is MemberState.ALIVE]
        self._rng.shuffle(helpers)
        return [Message.ping_req(self._id, helpers[i], cur.target, cur.seq, self.take_gossip())
                for i in range(min(self._config.indirect_probes, len(helpers)))]

    def finish_probe(self, now: int) -> None:
        """End of the period: a probe with no direct and no indirect ack makes the target SUSPECT, not DEAD."""
        cur = self._current
        if cur is None:
            return
        if not cur.acked:
            known = self.view.get(cur.target)
            if known is not None and known.state is MemberState.ALIVE:
                self.suspicions_raised += 1
                self._tracer(now, f"n{self._id} got no ack from n{cur.target} (direct + {self._config.indirect_probes}"
                                  " indirect) -> SUSPECT")
                self.apply_update(Update(cur.target, MemberState.SUSPECT, known.incarnation), now)
        self._current = None

    def expire_suspicions(self, now: int) -> None:
        """A suspicion nobody refuted within suspicion_periods periods becomes a confirmed death. Iterated in key order,
        which is also Java's HashMap order for the small integer ids this lesson uses."""
        for member, started in sorted(self.suspected_at.items()):   # a snapshot, like new ArrayList<>(entrySet())
            if now - started >= self._config.suspicion_periods * self._config.protocol_period:
                known = self.view.get(member)
                if known is not None and known.state is MemberState.SUSPECT:
                    self.declared_dead += 1
                    self._tracer(now, f"n{self._id} suspicion of n{member} expired -> DEAD")
                    self.apply_update(Update(member, MemberState.DEAD, known.incarnation), now)

    # ---------------------------------------------------------------- message handling

    def handle(self, m: Message, now: int) -> list[Message]:
        for u in m.piggyback:
            self.apply_update(u, now)
        out: list[Message] = []
        cur = self._current
        if m.kind is MessageKind.PING:
            out.append(Message.ack(self._id, m.from_, m.seq, self.take_gossip()))
        elif m.kind is MessageKind.ACK:
            if cur is not None and m.seq == cur.seq and m.from_ == cur.target:
                cur.acked = True
                self.direct_acks += 1
        elif m.kind is MessageKind.PING_REQ:
            self._relaying[m.seq] = m.from_
            out.append(Message.ping(self._id, m.target, m.seq, self.take_gossip()))
        elif m.kind is MessageKind.INDIRECT_ACK:
            if cur is not None and m.seq == cur.seq and m.target == cur.target:
                cur.acked = True
                self.indirect_acks += 1
        # An ACK for a seq we are relaying belongs to someone else's probe: forward it as INDIRECT_ACK.
        if m.kind is MessageKind.ACK and m.seq in self._relaying and (cur is None or m.seq != cur.seq):
            origin = self._relaying.pop(m.seq)
            out.append(Message.indirect_ack(self._id, origin, m.from_, m.seq, self.take_gossip()))
        self.messages_sent += len(out)
        return out

    def apply_update(self, u: Update, now: int) -> None:
        """Merge one update using the precedence rules; refute suspicions about myself; queue what changed."""
        if u.member == self._id:
            if u.state is not MemberState.ALIVE and u.incarnation >= self.incarnation:
                self.incarnation = u.incarnation + 1          # refutation (section 4.2): I am alive, and newer
                self.refutations += 1
                self._tracer(now, f"n{self._id} hears it is {u.state.name}@{u.incarnation} -> refutes with "
                                  f"incarnation {self.incarnation}")
                alive = Update(self._id, MemberState.ALIVE, self.incarnation)
                self.view[self._id] = alive
                self._enqueue(alive)
            return
        known = self.view.get(u.member)
        if not u.overrides(known):
            return
        self.view[u.member] = u
        if u.state is MemberState.SUSPECT:
            self.suspected_at.setdefault(u.member, now)
        else:
            self.suspected_at.pop(u.member, None)
        self._enqueue(u)

    def _enqueue(self, u: Update) -> None:
        for old in [k for k in self._gossip_queue if k.member == u.member]:   # one entry per member: newest wins
            del self._gossip_queue[old]
        self._gossip_queue[u] = 0

    def take_gossip(self) -> list[Update]:
        """Up to max_piggyback updates, fewest-transmissions first (stable); retire after lambda*log2(n) sends."""
        n = max(2, len(self.view))
        limit = math.ceil(self._config.retransmit_factor * (math.log(n) / math.log(2)))
        entries = sorted(self._gossip_queue.items(), key=lambda e: e[1])
        out: list[Update] = []
        for update, count in entries:
            if len(out) >= self._config.max_piggyback:
                break
            out.append(update)
            sent = count + 1
            if sent >= limit:
                del self._gossip_queue[update]
            else:
                self._gossip_queue[update] = sent       # re-assigning keeps the key's position, like LinkedHashMap.put
        return out

    def gossip_queue_size(self) -> int:
        return len(self._gossip_queue)


@dataclass(frozen=True)
class Envelope:
    deliver_at: int
    seq: int
    message: Message


class Network:
    """Deterministic network: (deliver_at, seq) ordering, seeded delays and drops, partitions, crashes, link cuts."""

    def __init__(self, seed: int, drop_rate: float, max_delay: int) -> None:
        self._rng = Rng(seed)
        self._drop_rate = drop_rate
        self._max_delay = max(1, max_delay)
        self._nodes: dict[int, Node] = {}
        self._in_flight: list[tuple[int, int, Envelope]] = []      # heap keyed by (deliver_at, seq)
        self._down: set[int] = set()
        self._cut_links: set[tuple[int, int]] = set()              # directed a->b pairs that silently drop
        self._side_a: frozenset[int] = frozenset()
        self._partitioned = False
        self._now = 0
        self._seq = 0
        self.sent = 0
        self.delivered = 0
        self.dropped = 0
        self.by_kind: dict[MessageKind, int] = {}

    def add(self, node: Node) -> None:
        self._nodes[node.id()] = node

    def now(self) -> int:
        return self._now

    def is_down(self, id_: int) -> bool:
        return id_ in self._down

    def crash(self, id_: int) -> None:
        self._down.add(id_)

    def partition(self, side_a: set[int]) -> None:
        self._side_a = frozenset(side_a)
        self._partitioned = True

    def heal(self) -> None:
        self._partitioned = False
        self._side_a = frozenset()
        self._cut_links.clear()

    def cut_link(self, a: int, b: int) -> None:
        """Cut traffic in both directions between exactly two nodes; everyone else still reaches both."""
        self._cut_links.add((a, b))
        self._cut_links.add((b, a))

    def _blocked(self, a: int, b: int) -> bool:
        return (a in self._down or b in self._down
                or (self._partitioned and (a in self._side_a) != (b in self._side_a))
                or (a, b) in self._cut_links)

    def send(self, m: Message) -> None:
        self.sent += 1
        self.by_kind[m.kind] = self.by_kind.get(m.kind, 0) + 1
        if self._blocked(m.from_, m.to):
            self.dropped += 1
            return
        roll = self._rng.next_double()
        delay = 1 + self._rng.next_int(self._max_delay)
        if roll < self._drop_rate:
            self.dropped += 1
            return
        heapq.heappush(self._in_flight, (self._now + delay, self._seq, Envelope(self._now + delay, self._seq, m)))
        self._seq += 1

    def send_all(self, ms: list[Message]) -> None:
        for m in ms:
            self.send(m)

    def tick(self) -> None:
        self._now += 1
        while self._in_flight and self._in_flight[0][0] <= self._now:
            m = heapq.heappop(self._in_flight)[2].message
            if self._blocked(m.from_, m.to):
                self.dropped += 1
                continue
            self.delivered += 1
            self.send_all(self._nodes[m.to].handle(m, self._now))
        for node_id in sorted(self._nodes):                        # TreeMap order
            if node_id not in self._down:
                self.send_all(self._nodes[node_id].tick(self._now))


class Cluster:
    """N SWIM nodes on one network, with the measurements the lesson is about: detection time, dissemination time,
    false positives."""

    def __init__(self, size: int, seed: int, drop_rate: float, max_delay: int, config: Config = Config.DEFAULT) -> None:
        self.size = size
        self.seed = seed
        self.config = config
        self.network = Network(seed, drop_rate, max_delay)
        self.nodes: list[SwimNode] = []
        self.events: list[str] = []
        all_ids = list(range(size))
        for i in range(size):
            n = SwimNode(i, all_ids, config, Rng(to_int64(seed * 7919 + 1000003 * (i + 1))), 0, self.trace)
            self.nodes.append(n)
            self.network.add(n)

    def trace(self, now: int, text: str) -> None:
        self.events.append(f"t={now:<4} {text}")

    def now(self) -> int:
        return self.network.now()

    def node(self, id_: int) -> SwimNode:
        return self.nodes[id_]

    def tick(self) -> None:
        self.network.tick()

    def run(self, ticks: int) -> None:
        for _ in range(ticks):
            self.tick()

    def crash(self, id_: int) -> None:
        self.network.crash(id_)
        self.trace(self.now(), f"n{id_} crashes")

    def run_until_all_believe(self, member: int, state: MemberState, max_ticks: int) -> int:
        """Ticks until every live node holds `state` for `member`, or -1."""
        for _ in range(max_ticks):
            self.tick()
            if self.all_believe(member, state):
                return self.now()
        return -1

    def all_believe(self, member: int, state: MemberState) -> bool:
        return all(n.state_of(member) is state for n in self.nodes
                   if not self.network.is_down(n.id()) and n.id() != member)

    def count_believing(self, member: int, state: MemberState) -> int:
        return sum(1 for n in self.nodes
                   if not self.network.is_down(n.id()) and n.id() != member and n.state_of(member) is state)

    def false_positives(self) -> int:
        """A false positive: a live node marked DEAD by any other live node."""
        c = 0
        for n in self.nodes:
            if self.network.is_down(n.id()):
                continue
            c += sum(1 for dead in n.dead_members() if not self.network.is_down(dead))
        return c

    def views_converged(self) -> bool:
        views = [n.dead_members() for n in self.nodes if not self.network.is_down(n.id())]
        return all(v == views[0] for v in views)

    def total_sent(self) -> int:
        return sum(n.messages_sent for n in self.nodes)


def jbool(b: bool) -> str:
    return "true" if b else "false"


def print_events(c: Cluster, start: int) -> None:
    for line in c.events[start:]:
        print(line)


def main() -> None:
    print("== Precedence rules for membership updates (SWIM section 4.2) ==")
    alive0, suspect0 = Update(3, MemberState.ALIVE, 0), Update(3, MemberState.SUSPECT, 0)
    alive1, dead0 = Update(3, MemberState.ALIVE, 1), Update(3, MemberState.DEAD, 0)
    print(f"{suspect0} overrides {alive0} = {jbool(suspect0.overrides(alive0))}   (same incarnation: suspicion wins)")
    print(f"{alive0} overrides {suspect0} = {jbool(alive0.overrides(suspect0))}  (a stale alive cannot clear a suspicion)")
    print(f"{alive1} overrides {suspect0} = {jbool(alive1.overrides(suspect0))}   (refutation: higher incarnation wins)")
    print(f"{dead0} overrides {alive1} = {jbool(dead0.overrides(alive1))}    (dead is terminal)")

    print()
    print("== 8 nodes, seed 3, delay 1-2, no drops: n5 crashes at t=100 ==")
    c = Cluster(8, 3, 0.0, 2)
    c.run(100)
    before = len(c.events)
    c.crash(5)
    suspect_at = c.run_until_all_believe(5, MemberState.SUSPECT, 2000)
    dead_at = c.run_until_all_believe(5, MemberState.DEAD, 2000)
    print_events(c, before)
    print(f"first suspicion reached everyone by t={suspect_at}, DEAD everywhere by t={dead_at} (protocol period "
          f"{c.config.protocol_period}, suspicion timeout {c.config.suspicion_periods * c.config.protocol_period})")
    per_period = c.total_sent() / (8 * (c.now() // c.config.protocol_period))
    print(f"messages per node per period: {per_period:.2f}  (constant: does not grow with N)")
    print(f"false positives: {c.false_positives()}")

    print()
    print("== Load per node is constant in N (the reason gossip beats all-to-all heartbeats) ==")
    for n in (8, 16, 32, 64):
        load = Cluster(n, 5, 0.0, 2)
        load.run(600)
        per_node_per_period = load.total_sent() / (n * (600 // load.config.protocol_period))
        print(f"N={n:<3} messages per node per period = {per_node_per_period:.2f}   "
              f"(all-to-all heartbeats would need {n - 1})")

    print()
    print("== A slow link is not a dead node: n1 cannot reach n2, but n0, n3, n4 can; indirect probes save n2 ==")
    s = Cluster(5, 11, 0.0, 2)
    s.run(50)
    before = len(s.events)
    s.network.cut_link(1, 2)
    s.run(600)
    print_events(s, before)
    print(f"after 600 ticks with the n1<->n2 link cut: n1 believes n2 is {s.node(1).state_of(2).name}; "
          f"false positives = {s.false_positives()}; indirect acks received by n1 = {s.node(1).indirect_acks}")

    print()
    print("== Without indirect probes the same cut becomes a false suspicion, and n2 refutes it by bumping its "
          "incarnation ==")
    no_indirect = Cluster(5, 11, 0.0, 2, Config(12, 4, 0, 4, 6, 3))
    no_indirect.run(50)
    before = len(no_indirect.events)
    no_indirect.network.cut_link(1, 2)
    no_indirect.run(120)
    print_events(no_indirect, before)
    print(f"n2 incarnation = {no_indirect.node(2).incarnation}, refutations = {no_indirect.node(2).refutations}"
          f"; n1 believes n2 is {no_indirect.node(1).state_of(2).name} (the refutation arrived via other members' "
          "piggyback)")

    print()
    print("== Convergence over 20 seeds: 12 nodes, 10% message loss, delay 1-2, one crash at t=200 ==")
    converged = worst_dead = total_false = sum_dead = 0
    for seed in range(1, 21):
        r = Cluster(12, seed, 0.10, 2)
        r.run(200)
        r.crash(7)
        t = r.run_until_all_believe(7, MemberState.DEAD, 3000)
        if t > 0:
            converged += 1
            sum_dead += t - 200
            worst_dead = max(worst_dead, t - 200)
        total_false += r.false_positives()
    mean = 0 if converged == 0 else sum_dead // converged
    print(f"seeds where every live node declared n7 DEAD: {converged}/20; mean detection+dissemination {mean} ticks, "
          f"worst {worst_dead} ticks; false positives across all seeds: {total_false}")
    print("expected: suspicion timeout (48) + one or two periods to notice + a few ticks to gossip; never a false "
          f"positive because every suspect gets {Config.DEFAULT.suspicion_periods} periods to refute")


if __name__ == "__main__":
    main()
