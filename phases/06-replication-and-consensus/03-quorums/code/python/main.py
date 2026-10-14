"""Lesson: phases/06-replication-and-consensus/03-quorums/docs/en.md
Quorums: R + W > N and Its Limits
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: DeCandia et al., "Dynamo: Amazon's Highly Available Key-value Store" (SOSP 2007), sections 4.5-4.7;
         Gifford, "Weighted Voting for Replicated Data" (SOSP 1979); Apache Cassandra docs on consistency levels and read repair.
Python 3.11+, standard library only. Deterministic: seeded SplitMix64 for drops, a tick counter for time, no threads.
"""

from __future__ import annotations

from dataclasses import dataclass
from typing import Optional

MASK64 = (1 << 64) - 1


class Rng:
    """SplitMix64 (Steele, Lea, Flood, OOPSLA 2014). Same stream for the same seed in every language."""

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


def java_string_hash(s: str) -> int:
    """Java's String.hashCode(): h = 31*h + c over UTF-16 code units, wrapping at 32 bits, returned signed.
    Placement must hash keys exactly like the Java version or the home replicas (and the demo) differ."""
    h = 0
    data = s.encode("utf-16-be")
    for i in range(0, len(data), 2):
        h = (31 * h + ((data[i] << 8) | data[i + 1])) & 0xFFFFFFFF
    return h - (1 << 32) if h >= (1 << 31) else h


@dataclass(frozen=True)
class Versioned:
    """A value with its version. Last-writer-wins without version vectors: the version is (timestamp, writer_id),
    compared lexicographically so two writes at the same tick still have one deterministic winner. Concurrent writes
    are not detected: one of them silently loses, and that is a feature of the demo."""

    value: Optional[str]
    timestamp: int
    writer_id: int

    def newer_than(self, other: "Versioned") -> bool:
        return (self.timestamp, self.writer_id) > (other.timestamp, other.writer_id)

    def __str__(self) -> str:
        return "<none>" if self.value is None else f"{self.value}@{self.timestamp}/w{self.writer_id}"


NONE = Versioned(None, -1, -1)


@dataclass(frozen=True)
class ReadResult:
    """Result of a quorum read: the winning version and which replicas answered."""

    winner: Versioned
    responses: int
    respondents: list
    repaired: int

    def value(self) -> Optional[str]:
        return self.winner.value


class QuorumNotReached(Exception):
    """Raised when fewer than W acks or fewer than R responses arrive. The client must retry or report failure."""


class StorageNode:
    """One storage node. Holds its own data plus hints: writes it accepted on behalf of a node that was unreachable,
    to be handed off when that node comes back (Dynamo 4.6). Hints are keyed by the intended node."""

    def __init__(self, id: int) -> None:
        self.id = id
        self.data: dict[str, Versioned] = {}
        self.hints: dict[int, dict[str, Versioned]] = {}
        self.writes_accepted = 0
        self.reads_served = 0
        self.hints_stored = 0
        self.hints_delivered = 0
        self.repairs_applied = 0

    def put(self, key: str, v: Versioned) -> bool:
        """Store only if newer. Returns True when the stored version changed. Idempotent."""
        self.writes_accepted += 1
        current = self.data.get(key, NONE)
        if v.newer_than(current):
            self.data[key] = v
            return True
        return False

    def get(self, key: str) -> Versioned:
        self.reads_served += 1
        return self.data.get(key, NONE)

    def store_hint(self, for_node: int, key: str, v: Versioned) -> None:
        self.hints_stored += 1
        for_target = self.hints.setdefault(for_node, {})
        if v.newer_than(for_target.get(key, NONE)):
            for_target[key] = v

    def hint_count(self) -> int:
        return sum(len(m) for m in self.hints.values())


class Network:
    """The simulated network: a set of unreachable nodes, a partition, and a seeded drop rate. Synchronous RPCs."""

    def __init__(self, seed: int, drop_rate: float) -> None:
        self._rng = Rng(seed)
        self._drop_rate = drop_rate
        self._down: set[int] = set()
        self._side_a: frozenset[int] = frozenset()
        self._partitioned = False
        self.attempts = 0
        self.dropped = 0

    def crash(self, id: int) -> None:
        self._down.add(id)

    def recover(self, id: int) -> None:
        self._down.discard(id)

    def is_down(self, id: int) -> bool:
        return id in self._down

    def partition(self, a: set[int]) -> None:
        self._side_a = frozenset(a)
        self._partitioned = True

    def heal(self) -> None:
        self._partitioned = False
        self._side_a = frozenset()

    def reachable(self, from_: int, to: int) -> bool:
        """Can from_ (the coordinator) reach to right now? Deterministic given the seed and the call order."""
        self.attempts += 1
        if to in self._down or (self._partitioned and ((from_ in self._side_a) != (to in self._side_a))):
            self.dropped += 1
            return False
        if self._drop_rate > 0 and self._rng.next_double() < self._drop_rate:
            self.dropped += 1
            return False
        return True


@dataclass(frozen=True)
class QuorumConfig:
    """The N/R/W knobs plus the two Dynamo options this lesson is about."""

    n: int
    r: int
    w: int
    sloppy: bool
    read_repair: bool

    def __post_init__(self) -> None:
        if self.n < 1 or self.r < 1 or self.w < 1 or self.r > self.n or self.w > self.n:
            raise ValueError("need 1 <= R, W <= N")

    def overlaps(self) -> bool:
        return self.r + self.w > self.n

    def __str__(self) -> str:
        return (f"N={self.n} R={self.r} W={self.w}" + (" sloppy" if self.sloppy else " strict")
                + (" read-repair" if self.read_repair else ""))


class QuorumStore:
    """A leaderless replicated store. Any node may coordinate. Each key has N home replicas (the first N nodes on the
    ring starting at hash(key)); a sloppy quorum may spill onto the next nodes with a hint when a home node is down."""

    def __init__(self, total_nodes: int, config: QuorumConfig, seed: int, drop_rate: float) -> None:
        if total_nodes < config.n:
            raise ValueError("need at least N nodes")
        self.config = config
        self.network = Network(seed, drop_rate)
        self.nodes = [StorageNode(i) for i in range(total_nodes)]
        self._clock = 0
        self.writes = self.write_failures = self.reads = self.read_failures = 0
        self.hinted_writes = self.repairs = 0
        self.events: list[str] = []

    def now(self) -> int:
        return self._clock

    def tick(self) -> None:
        self._clock += 1

    def home_nodes(self, key: str) -> list[int]:
        """Deterministic placement: the key's home replicas are N consecutive ring positions."""
        start = java_string_hash(key) % len(self.nodes)   # Python % with a positive divisor is Math.floorMod
        return [(start + i) % len(self.nodes) for i in range(self.config.n)]

    def contact_order(self, coordinator: int, key: str) -> list[int]:
        """The home replicas with the coordinator first when it is one of them."""
        order = self.home_nodes(key)
        if coordinator in order:
            at = order.index(coordinator)
            order = order[at:] + order[:at]   # Collections.rotate(order, -at)
        if self.config.sloppy:
            order.extend(self.fallback_nodes(key))
        return order

    def fallback_nodes(self, key: str) -> list[int]:
        """The ring beyond the home set, in order: where a sloppy quorum spills."""
        start = java_string_hash(key) % len(self.nodes)
        return [(start + i) % len(self.nodes) for i in range(self.config.n, len(self.nodes))]

    def put(self, coordinator: int, key: str, value: str) -> int:
        """Write through a coordinator. With sloppy quorums, each home replica that does not answer is replaced by the
        next ring node, which stores the value plus a hint. Success means at least W acks. Nothing is rolled back."""
        self.writes += 1
        self._clock += 1
        v = Versioned(value, self._clock, coordinator)
        acks = 0
        acked: list[int] = []
        missed: list[int] = []
        for node in self.home_nodes(key):
            if node == coordinator or self.network.reachable(coordinator, node):
                self.nodes[node].put(key, v)
                acks += 1
                acked.append(node)
            else:
                missed.append(node)
        if self.config.sloppy and missed:
            spare = self.fallback_nodes(key)
            si = 0
            for absent in missed:
                while si < len(spare):
                    candidate = spare[si]
                    si += 1
                    if candidate == coordinator or self.network.reachable(coordinator, candidate):
                        self.nodes[candidate].put(key, v)
                        self.nodes[candidate].store_hint(absent, key, v)
                        self.hinted_writes += 1
                        acks += 1
                        acked.append(candidate)
                        break
        if acks < self.config.w:
            self.write_failures += 1
            self.events.append(f"t={self._clock} PUT {key}={value} via n{coordinator} FAILED: {acks} acks < W={self.config.w}")
            raise QuorumNotReached(f"write {key} got {acks} acks, needs W={self.config.w}")
        self.events.append(f"t={self._clock} PUT {key}={value} via n{coordinator} acked by {acked}"
                           + ("" if not missed else f" (missed {missed})"))
        return acks

    def get(self, coordinator: int, key: str) -> ReadResult:
        """Read through a coordinator: take the first R answers, return the newest version. With read repair, push the
        winner back to every responder that returned something older."""
        self.reads += 1
        winner = NONE
        respondents: list[int] = []
        seen: dict[int, Versioned] = {}
        for node in self.contact_order(coordinator, key):
            if len(respondents) >= self.config.r:
                break
            if node == coordinator or self.network.reachable(coordinator, node):
                v = self.nodes[node].get(key)
                respondents.append(node)
                seen[node] = v
                if v.newer_than(winner):
                    winner = v
        if len(respondents) < self.config.r:
            self.read_failures += 1
            raise QuorumNotReached(f"read {key} got {len(respondents)} responses, needs R={self.config.r}")
        repaired = 0
        if self.config.read_repair and winner.value is not None:
            for node in respondents:
                if winner.newer_than(seen[node]) and self.nodes[node].put(key, winner):
                    self.nodes[node].repairs_applied += 1
                    repaired += 1
            self.repairs += repaired
        return ReadResult(winner, len(respondents), respondents, repaired)

    def handoff(self) -> int:
        """Hinted handoff: every node holding hints for a now-reachable target delivers them."""
        delivered = 0
        for holder in self.nodes:
            for target in sorted(holder.hints):   # TreeMap order
                if not self.network.reachable(holder.id, target):
                    continue
                values = holder.hints[target]
                for k in sorted(values):
                    self.nodes[target].put(k, values[k])
                    delivered += 1
                holder.hints_delivered += len(values)
                del holder.hints[target]
        if delivered > 0:
            self.events.append(f"t={self._clock} hinted handoff delivered {delivered} value(s)")
        return delivered

    def anti_entropy(self) -> int:
        """Anti-entropy: for every key, converge all reachable home replicas to the newest version."""
        updates = 0
        keys: set[str] = set()
        for n in self.nodes:
            keys.update(n.data)
        for key in sorted(keys):
            home = self.home_nodes(key)
            newest = NONE
            for node in home:
                if not self.network.is_down(node):
                    v = self.nodes[node].data.get(key, NONE)
                    if v.newer_than(newest):
                        newest = v
            if newest.value is None:
                continue
            for node in home:
                if not self.network.is_down(node) and self.nodes[node].put(key, newest):
                    updates += 1
        if updates > 0:
            self.events.append(f"t={self._clock} anti-entropy updated {updates} replica value(s)")
        return updates

    def replicas_holding(self, key: str, value: str) -> int:
        """How many of the home replicas of key hold value as their current version."""
        return sum(1 for node in self.home_nodes(key) if self.nodes[node].data.get(key, NONE).value == value)

    def replicas_agree(self, key: str) -> bool:
        """Do all live home replicas of key hold the same version?"""
        versions = {self.nodes[node].data.get(key, NONE) for node in self.home_nodes(key) if not self.network.is_down(node)}
        return len(versions) <= 1

    def replica_view(self, key: str) -> str:
        parts = []
        for node in self.home_nodes(key):
            parts.append(f"n{node}={self.nodes[node].data.get(key, NONE)}" + ("(down)" if self.network.is_down(node) else ""))
        return " ".join(parts)


@dataclass(frozen=True)
class Outcome:
    writes: int
    write_failures: int
    read_failures: int
    stale_reads: int
    hinted_writes: int
    repairs: int

    def stale_rate(self) -> float:
        ok = self.writes - self.write_failures - self.read_failures
        return 0.0 if ok == 0 else self.stale_reads / ok


class Experiment:
    """Write, then read from a different coordinator, and count how often the read misses the latest write."""

    Outcome = Outcome

    @staticmethod
    def run(config: QuorumConfig, total_nodes: int, seed: int, drop_rate: float, operations: int, crash_every: int) -> Outcome:
        store = QuorumStore(total_nodes, config, seed, drop_rate)
        rng = Rng(seed * 31 + 7)
        stale = write_failures = read_failures = 0
        for i in range(1, operations + 1):
            key = f"k{rng.next_int(5)}"
            value = f"v{i}"
            writer = rng.next_int(total_nodes)
            reader = rng.next_int(total_nodes)
            if crash_every > 0 and i % crash_every == 0:
                victim = rng.next_int(total_nodes)
                down_count = sum(1 for n in range(total_nodes) if store.network.is_down(n))
                if store.network.is_down(victim):
                    store.network.recover(victim)
                    store.handoff()
                elif down_count < 2:
                    store.network.crash(victim)
            try:
                store.put(writer, key, value)
            except QuorumNotReached:
                write_failures += 1
                continue
            try:
                r = store.get(reader, key)
                if r.value() != value:
                    stale += 1
            except QuorumNotReached:
                read_failures += 1
        return Outcome(operations, write_failures, read_failures, stale, store.hinted_writes, store.repairs)


def section(title: str) -> None:
    print()
    print(f"== {title} ==")


def main() -> None:
    print("== R + W > N: a strict quorum read always overlaps the last quorum write (N=3, R=2, W=2, node 1 crashed) ==")
    s = QuorumStore(5, QuorumConfig(3, 2, 2, False, False), 1, 0.0)
    key = "merchant:m-77:settlement-currency"
    print(f"home replicas of {key}: {s.home_nodes(key)}; fallback ring: {s.fallback_nodes(key)}")
    crashed = s.home_nodes(key)[1]
    s.network.crash(crashed)
    s.put(s.home_nodes(key)[0], key, "EUR")
    r = s.get(s.home_nodes(key)[2], key)
    print(s.events[-1])
    print(f"read from a different coordinator with R=2 saw {r.winner} from {r.respondents}  (replicas: {s.replica_view(key)})")
    print(f"the write set {{{s.home_nodes(key)[0]},{s.home_nodes(key)[2]}}} and the read set {r.respondents} share at least one node: 2 + 2 > 3")

    section("R + W <= N: the quorums can miss each other (N=3, R=1, W=1), one node briefly unreachable")
    weak = QuorumStore(5, QuorumConfig(3, 1, 1, False, False), 1, 0.0)
    wh = weak.home_nodes(key)
    weak.put(wh[0], key, "EUR")
    weak.network.crash(wh[2])
    weak.put(wh[1], key, "GBP")
    weak.network.recover(wh[2])
    w1, w2, w3 = weak.get(wh[0], key), weak.get(wh[1], key), weak.get(wh[2], key)
    print(f"PUT EUR; node {wh[2]} unreachable during PUT GBP (1 ack suffices), then back: {weak.replica_view(key)}")
    print(f"R=1 reads via n{wh[0]}, n{wh[1]}, n{wh[2]} return: {w1.value()}, {w2.value()}, {w3.value()}  <- every node is healthy and one of them is wrong")

    section("Sloppy quorum: the write reaches W nodes, but not the home set, so a later strict-looking read can miss it")
    sloppy = QuorumStore(5, QuorumConfig(3, 2, 2, True, False), 1, 0.0)
    home = sloppy.home_nodes(key)
    sloppy.network.crash(home[1])
    sloppy.network.crash(home[2])
    sloppy.put(home[0], key, "EUR")
    print(f"{sloppy.events[-1]}  <- 2 acks reached W=2, one of them is a hint on a non-home node")
    print(f"replicas now: {sloppy.replica_view(key)}; hints held: {sloppy.nodes[sloppy.fallback_nodes(key)[0]].hint_count()}")
    sloppy.network.recover(home[1])
    sloppy.network.recover(home[2])
    sloppy.network.crash(home[0])
    stale = sloppy.get(home[1], key)
    print(f"home nodes {home[1]},{home[2]} recover, node {home[0]} crashes; R=2 read sees {stale.winner} from {stale.respondents}  <- R + W > N held and the read is still stale")
    delivered = sloppy.handoff()
    fixed = sloppy.get(home[1], key)
    print(f"hinted handoff delivers {delivered} value(s); the same read now sees {fixed.winner}  (replicas: {sloppy.replica_view(key)})")

    section("Read repair: a stale replica is fixed by the next quorum read that notices it")
    rr = QuorumStore(5, QuorumConfig(3, 2, 2, False, True), 1, 0.0)
    rr.network.crash(home[2])
    rr.put(home[0], key, "EUR")
    rr.network.recover(home[2])
    print(f"after a write with node {home[2]} down and back: {rr.replica_view(key)}")
    rr.network.crash(home[0])
    repair_read = rr.get(home[1], key)
    print(f"R=2 read via n{home[1]} (node {home[0]} now down) saw {repair_read.winner} from {repair_read.respondents}, "
          f"repaired {repair_read.repaired} replica -> {rr.replica_view(key)}")

    section("Last-writer-wins: two concurrent writers, one of them silently loses, and why a balance must never be a register")
    lww = QuorumStore(5, QuorumConfig(3, 2, 2, False, True), 1, 0.0)
    bal = "wallet:w-9:balance"
    lww.put(0, bal, "100.00 USD")
    seen_by_a, seen_by_b = lww.get(1, bal), lww.get(3, bal)
    print(f"coordinator A reads {seen_by_a.value()}, coordinator B reads {seen_by_b.value()}; A applies a 30.00 USD debit, B applies a 50.00 USD credit")
    acks_a = lww.put(1, bal, "70.00 USD")
    print(f"A's write lands with {acks_a} acks: {lww.replica_view(bal)}")
    acks_b = lww.put(3, bal, "150.00 USD")
    print(f"B's write lands with {acks_b} acks one tick later and both clients receive success: {lww.replica_view(bal)}")
    after = lww.get(lww.home_nodes(bal)[2], bal)
    lww.anti_entropy()
    print(f"quorum read returns {after.winner}; after anti-entropy every replica holds {lww.replica_view(bal)}")
    print("the 30.00 USD debit vanished with no error anywhere: LWW chose by timestamp, not by meaning. Lesson 08 (CRDTs) and Phase 10 (ledgers) are the answer.")

    section("Anti-entropy converges replicas that no read ever touched")
    ae = QuorumStore(5, QuorumConfig(3, 2, 2, True, False), 1, 0.0)
    ae.network.crash(2)
    for i in range(8):
        ae.put(0 if i % 5 == 2 else i % 5, f"cfg:{i}", f"value{i}")
    ae.network.recover(2)
    before = sum(1 for i in range(8) if ae.replicas_agree(f"cfg:{i}"))
    updates = ae.anti_entropy()
    after_count = sum(1 for i in range(8) if ae.replicas_agree(f"cfg:{i}"))
    print(f"node 2 missed its share of 8 writes; keys whose home replicas agree before: {before}/8, anti-entropy applied {updates} update(s), after: {after_count}/8")

    section("20 seeds x 400 write-then-read pairs, 5 nodes, 5% drops, a crash or recovery every 25 ops: stale reads by configuration")
    configs = [
        QuorumConfig(3, 1, 1, False, False), QuorumConfig(3, 1, 3, False, False), QuorumConfig(3, 2, 2, False, False),
        QuorumConfig(3, 2, 2, False, True), QuorumConfig(3, 2, 2, True, False), QuorumConfig(3, 3, 1, False, False),
    ]
    print(f"{'config':<34s} {'stale':>8s} {'writeFail':>10s} {'readFail':>10s} {'hinted':>7s} {'repair':>7s}")
    for cfg in configs:
        stale2 = wf = rf = hinted = repairs_total = 0
        for seed in range(1, 21):
            o = Experiment.run(cfg, 5, seed, 0.05, 400, 25)
            stale2 += o.stale_reads
            wf += o.write_failures
            rf += o.read_failures
            hinted += o.hinted_writes
            repairs_total += o.repairs
        print(f"{str(cfg):<34s} {stale2:8d} {wf:10d} {rf:10d} {hinted:7d} {repairs_total:7d}")
    print("R + W > N buys zero stale reads only when every ack came from a home replica; sloppy quorums trade that for availability.")


if __name__ == "__main__":
    main()
