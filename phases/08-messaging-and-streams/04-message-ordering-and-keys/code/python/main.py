"""Lesson: phases/08-messaging-and-streams/04-message-ordering-and-keys/docs/en.md
Message Ordering, Keys, and Head-of-Line Blocking
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: Apache Kafka documentation, section 4.4 "Message Delivery Semantics" and producer configs
         max.in.flight.requests.per.connection / enable.idempotence; KIP-98 (idempotent producer sequence numbers).
Python 3.11+, standard library only. Same design as the Java version. Time is simulated with a discrete-event clock;
nothing sleeps or touches the network. JavaRandom reproduces java.util.Random so the seeded run matches bit for bit.
"""

from __future__ import annotations

import heapq
import math
from collections import deque
from dataclasses import dataclass, field
from enum import Enum
from typing import Deque, Dict, List, Optional, Set


@dataclass(frozen=True)
class Currency:
    """ISO 4217 currency: code and minor-unit exponent. Copied from Phase 10, Lesson 01; lessons are self-contained."""

    code: str
    exponent: int

    def __post_init__(self) -> None:
        if self.code is None or len(self.code) != 3:
            raise ValueError(f"currency code must be three letters: {self.code}")
        if self.exponent < 0 or self.exponent > 4:
            raise ValueError(f"exponent must be 0..4: {self.exponent}")


USD = Currency("USD", 2)


@dataclass(frozen=True)
class Money:
    """Integer minor units plus a currency. Never a float."""

    minor_units: int
    currency: Currency

    def __post_init__(self) -> None:
        if not isinstance(self.minor_units, int) or isinstance(self.minor_units, bool):
            raise TypeError("minor_units must be an int; never construct Money from a float")
        if self.currency is None:
            raise TypeError("currency")

    @staticmethod
    def of_minor(minor_units: int, currency: Currency) -> "Money":
        return Money(minor_units, currency)

    def __str__(self) -> str:
        scale = 10 ** self.currency.exponent
        abs_units = abs(self.minor_units)
        frac = str(abs_units % scale).rjust(self.currency.exponent, "0")
        return f"{'-' if self.minor_units < 0 else ''}{abs_units // scale}.{frac} {self.currency.code}"


# ---------------------------------------------------------------------------------------------------------------------
# Records and the partitioner. The partition is a pure function of the key, so one key has one home and one order.
# ---------------------------------------------------------------------------------------------------------------------


@dataclass(frozen=True)
class Message:
    """One payment event as the producer hands it to the log. seq is the producer's per-key sequence number: the
    n-th event the producer emitted for this key. The log does not assign it; the producer does, before any retry."""

    key: str
    seq: int
    type: str
    amount: Money
    produced_at: int
    cost_millis: int

    def __post_init__(self) -> None:
        if self.key is None:
            raise TypeError("key")
        if self.seq < 0:
            raise ValueError("seq must be non-negative")
        if self.cost_millis < 0:
            raise ValueError("costMillis must be non-negative")


@dataclass(frozen=True)
class StoredMessage:
    """A message as it sits in a partition: the log's offset plus the producer's message."""

    offset: int
    message: Message


class Partitioner:
    """FNV-1a 32-bit over UTF-8 bytes; the same function Lesson 01 uses, so a key lands where Lesson 01 would put it."""

    @staticmethod
    def fnv1a32(data: bytes) -> int:
        h = 0x811C9DC5
        for b in data:
            h ^= b
            h = (h * 0x01000193) & 0xFFFFFFFF
        return h

    @staticmethod
    def partition_for(key: str, partition_count: int) -> int:
        """Partition for a key: hash masked to non-negative, then mod N. Same key, same partition, while N is fixed."""
        if partition_count <= 0:
            raise ValueError("partitionCount must be positive")
        h = Partitioner.fnv1a32(key.encode("utf-8"))
        return (h & 0x7FFFFFFF) % partition_count


class Partition:
    """A minimal single-partition log: dense offsets, append-only, read by offset. Lesson 01 has the segmented version."""

    def __init__(self, id: int) -> None:
        self.id = id
        self._records: List[StoredMessage] = []

    def append(self, message: Message) -> int:
        offset = len(self._records)
        self._records.append(StoredMessage(offset, message))
        return offset

    def log_end_offset(self) -> int:
        return len(self._records)

    def get(self, offset: int) -> StoredMessage:
        if offset < 0 or offset >= len(self._records):
            raise IndexError(f"offset {offset} not in [0, {len(self._records)})")
        return self._records[offset]

    def read_all(self) -> List[StoredMessage]:
        return list(self._records)

    def sequence_of(self, key: str) -> List[int]:
        """The sequence of seq values for one key, in offset order. Per-key order holds iff this is strictly increasing by one."""
        return [s.message.seq for s in self._records if s.message.key == key]


class Topic:
    """A topic with N partitions. Appending routes by key."""

    def __init__(self, name: str, partition_count: int) -> None:
        self.name = name
        self._partitions = [Partition(i) for i in range(partition_count)]

    def partition_count(self) -> int:
        return len(self._partitions)

    def partition(self, id: int) -> Partition:
        return self._partitions[id]

    def append(self, message: Message) -> int:
        p = Partitioner.partition_for(message.key, len(self._partitions))
        self._partitions[p].append(message)
        return p


# ---------------------------------------------------------------------------------------------------------------------
# Retry-induced reordering. A producer with several sends in flight that retries a failed send lands it after sends
# that were queued behind it. Sequence numbers detect this at the consumer and prevent it at the broker.
# ---------------------------------------------------------------------------------------------------------------------


class JavaRandom:
    """java.util.Random: the 48-bit linear congruential generator, so a seed yields the same sequence as the Java demo."""

    _MASK = (1 << 48) - 1

    def __init__(self, seed: int) -> None:
        self._seed = (seed ^ 0x5DEECE66D) & self._MASK

    def next(self, bits: int) -> int:
        self._seed = (self._seed * 0x5DEECE66D + 0xB) & self._MASK
        value = (self._seed >> (48 - bits)) & 0xFFFFFFFF
        if value >= 1 << 31:  # Java casts to a signed 32-bit int; only bits=32 can go negative
            value -= 1 << 32
        return value

    def next_double(self) -> float:
        return ((self.next(26) << 27) + self.next(27)) * (1.0 / (1 << 53))


class FaultInjector:
    """Decides which sends fail on their first attempt. Deterministic: either an explicit set or a seeded JavaRandom."""

    def __init__(self, fail_first_attempt: Set[int]) -> None:
        self._fail_first_attempt = set(fail_first_attempt)

    @staticmethod
    def random(seed: int, count: int, probability: float) -> "FaultInjector":
        """Fail roughly probability of the first count sends, chosen by a seeded generator."""
        rnd = JavaRandom(seed)
        fails = {i for i in range(count) if rnd.next_double() < probability}
        return FaultInjector(fails)

    def fails_first_attempt(self, send_index: int) -> bool:
        return send_index in self._fail_first_attempt


@dataclass(frozen=True)
class SendOutcome:
    """What one simulated producer run produced: the final partition contents and how many retries and rejections happened."""

    appended: List[Message]
    retries: int
    rejected: int


@dataclass
class _Send:
    index: int
    message: Message
    attempted: bool = False


class ProducerSim:
    """Simulates a producer sending one key's messages to one partition with up to max_in_flight sends outstanding.
    A send that fails its first attempt is retried after the rest of the in-flight window has been acknowledged, which
    is exactly how a retry overtakes its successors. With sequence_guard the broker rejects any send whose seq is
    not last_seq + 1 (KIP-98's idempotent producer), so the producer must re-send in order and the log stays ordered."""

    @staticmethod
    def deliver(messages: List[Message], max_in_flight: int, faults: FaultInjector, sequence_guard: bool) -> SendOutcome:
        if max_in_flight <= 0:
            raise ValueError("maxInFlight must be positive")
        pending: Deque[_Send] = deque(_Send(i, m) for i, m in enumerate(messages))
        appended: List[Message] = []
        last_seq: Dict[str, int] = {}
        retries = rejected = 0
        while pending:
            window: List[_Send] = []
            while pending and len(window) < max_in_flight:
                window.append(pending.popleft())
            resend: List[_Send] = []
            for send in window:
                if not send.attempted and faults.fails_first_attempt(send.index):
                    send.attempted = True
                    retries += 1
                    resend.append(send)
                    continue
                send.attempted = True
                key = send.message.key
                expected = last_seq.get(key, -1) + 1
                if sequence_guard and send.message.seq != expected:
                    rejected += 1
                    resend.append(send)
                    continue
                last_seq[key] = send.message.seq
                appended.append(send.message)
            resend.sort(key=lambda s: s.index)
            for s in reversed(resend):
                pending.appendleft(s)
        return SendOutcome(appended, retries, rejected)


class Verdict(Enum):
    IN_ORDER = "IN_ORDER"
    DUPLICATE = "DUPLICATE"
    GAP = "GAP"

    def __str__(self) -> str:
        return self.name


class SequenceTracker:
    """Consumer-side check that a key's messages arrive with seq = last + 1. Classifies every message it sees."""

    Verdict = Verdict

    def __init__(self) -> None:
        self._last_seq: Dict[str, int] = {}
        self._duplicates = 0
        self._gaps = 0

    def observe(self, m: Message) -> Verdict:
        expected = self._last_seq.get(m.key, -1) + 1
        if m.seq == expected:
            self._last_seq[m.key] = m.seq
            return Verdict.IN_ORDER
        if m.seq < expected:
            self._duplicates += 1
            return Verdict.DUPLICATE
        self._gaps += 1
        self._last_seq[m.key] = m.seq
        return Verdict.GAP

    def duplicates(self) -> int:
        return self._duplicates

    def gaps(self) -> int:
        return self._gaps

    @staticmethod
    def is_ordered(messages: List[Message]) -> bool:
        """True iff every message in the list for every key was IN_ORDER."""
        t = SequenceTracker()
        return all(t.observe(m) is Verdict.IN_ORDER for m in messages)


# ---------------------------------------------------------------------------------------------------------------------
# Head-of-line blocking and per-key lanes. A discrete-event simulation: each message has an arrival time and a
# processing cost; the scheduler decides when it starts, and latency is completion minus arrival.
# ---------------------------------------------------------------------------------------------------------------------


@dataclass(frozen=True)
class Completion:
    """When a message was processed and how long it waited."""

    offset: int
    key: str
    started_at: int
    completed_at: int
    latency_millis: int


@dataclass(frozen=True)
class LatencyStats:
    """Summary of completion latencies. All values are simulated milliseconds."""

    count: int
    max: int
    mean: int
    p50: int
    p99: int

    @staticmethod
    def of(completions: List[Completion]) -> "LatencyStats":
        if not completions:
            return LatencyStats(0, 0, 0, 0, 0)
        lat = sorted(c.latency_millis for c in completions)
        return LatencyStats(len(lat), lat[-1], sum(lat) // len(lat), LatencyStats._percentile(lat, 50), LatencyStats._percentile(lat, 99))

    @staticmethod
    def _percentile(sorted_values: List[int], p: int) -> int:
        idx = math.ceil(p / 100.0 * len(sorted_values)) - 1
        return sorted_values[max(0, min(len(sorted_values) - 1, idx))]

    @staticmethod
    def excluding(completions: List[Completion], excluded: str) -> "LatencyStats":
        """Stats restricted to completions whose key is not excluded: "how did everyone else fare?\""""
        return LatencyStats.of([c for c in completions if c.key != excluded])


class CommitTracker:
    """Tracks which offsets of a partition have finished processing and reports the offset that may be committed: the
    smallest offset not yet completed. With out-of-order completion this is the "committed offset is the next offset to
    read" contract from Lesson 01 applied to parallel processing: commit the low-water mark, never the highest offset."""

    def __init__(self, start_offset: int) -> None:
        self._committable = start_offset
        self._done_ahead: List[int] = []   # heap; TreeSet in Java
        self._done_set: Set[int] = set()

    def complete(self, offset: int) -> int:
        if offset < self._committable:
            raise RuntimeError(f"offset {offset} already committed")
        if offset in self._done_set:
            raise RuntimeError(f"offset {offset} completed twice")
        self._done_set.add(offset)
        heapq.heappush(self._done_ahead, offset)
        while self._done_ahead and self._done_ahead[0] == self._committable:
            self._done_set.discard(heapq.heappop(self._done_ahead))
            self._committable += 1
        return self._committable

    def committable(self) -> int:
        return self._committable

    def completed_ahead(self) -> int:
        return len(self._done_ahead)


class Scheduler:
    """A processing strategy for one partition's records."""

    def run(self, partition: Partition) -> List[Completion]:
        raise NotImplementedError


class SequentialScheduler(Scheduler):
    """One consumer, one thread, offsets in order. The slow record at offset k delays every record after k."""

    def run(self, partition: Partition) -> List[Completion]:
        out: List[Completion] = []
        busy_until = 0
        for s in partition.read_all():
            m = s.message
            start = max(busy_until, m.produced_at)
            end = start + m.cost_millis
            busy_until = end
            out.append(Completion(s.offset, m.key, start, end, end - m.produced_at))
        return out


class KeyedLaneScheduler(Scheduler):
    """Per-key lanes inside one partition: lane = hash(key) mod lanes. Each lane processes its records in offset order,
    so per-key order holds; different lanes run independently, so a slow key blocks only the keys sharing its lane.
    Also drives a CommitTracker so the demo can show how far behind the committable offset trails completion."""

    def __init__(self, lanes: int) -> None:
        if lanes <= 0:
            raise ValueError("lanes must be positive")
        self._lanes = lanes
        self._final_committable = 0
        self._max_completed_ahead = 0

    def lane_for(self, key: str) -> int:
        return Partitioner.partition_for(key, self._lanes)

    def run(self, partition: Partition) -> List[Completion]:
        lane_busy_until = [0] * self._lanes
        out: List[Completion] = []
        for s in partition.read_all():
            m = s.message
            lane = self.lane_for(m.key)
            start = max(lane_busy_until[lane], m.produced_at)
            end = start + m.cost_millis
            lane_busy_until[lane] = end
            out.append(Completion(s.offset, m.key, start, end, end - m.produced_at))
        # Replay completions in time order through a CommitTracker to find how far the commit lags.
        by_time = sorted(out, key=lambda c: (c.completed_at, c.offset))
        tracker = CommitTracker(0)
        self._max_completed_ahead = 0
        for c in by_time:
            tracker.complete(c.offset)
            self._max_completed_ahead = max(self._max_completed_ahead, tracker.completed_ahead())
        self._final_committable = tracker.committable()
        return out

    def final_committable(self) -> int:
        return self._final_committable

    def max_completed_ahead(self) -> int:
        """The most offsets that were ever finished but uncommittable because an earlier offset was still running."""
        return self._max_completed_ahead


class Workload:
    """Builds the demo's workload and verifies per-key order in a list of completions."""

    @staticmethod
    def build(keys: List[str], count: int, gap_millis: int, fast_cost: int, slow_key: str, slow_cost: int) -> List[Message]:
        """count messages arriving one every gap_millis, spread round-robin over keys. Every message
        costs fast_cost, except messages for slow_key, which cost slow_cost."""
        seqs: Dict[str, int] = {}
        out: List[Message] = []
        for i in range(count):
            key = keys[i % len(keys)]
            seq = seqs.get(key, 0)
            seqs[key] = seq + 1
            cost = slow_cost if key == slow_key else fast_cost
            type_ = "AUTH" if seq % 3 == 0 else "CAPTURE" if seq % 3 == 1 else "REVERSAL"
            out.append(Message(key, seq, type_, Money.of_minor(1999 + i, USD), i * gap_millis, cost))
        return out

    @staticmethod
    def per_key_order_holds(partition: Partition, completions: List[Completion]) -> bool:
        """True iff, for every key, completions happen in offset order (started_at non-decreasing along the key's offsets)."""
        by_offset = {c.offset: c for c in completions}
        last_end: Dict[str, int] = {}
        for s in partition.read_all():
            c = by_offset[s.offset]
            key = s.message.key
            previous_end = last_end.get(key)
            if previous_end is not None and c.started_at < previous_end:
                return False
            last_end[key] = c.completed_at
        return True


def _fmt_list(values) -> str:
    """Java's List.toString shape for strings, so the demo lines match."""
    return "[" + ", ".join(str(v) for v in values) + "]"


def main() -> None:
    print("== 1. Keys choose partitions; a partition is the unit of order ==")
    topic = Topic("payments.events", 3)
    accounts = ["acct-1001", "acct-1002", "acct-1003", "acct-1004", "acct-1005", "acct-1006"]
    for a in accounts:
        print(f"{a:<10} -> partition {Partitioner.partition_for(a, 3)}")
    keyed = Workload.build(accounts, 18, 10, 5, "none", 5)
    for m in keyed:
        topic.append(m)
    for p in range(topic.partition_count()):
        part = topic.partition(p)
        line = " ".join(f"{s.message.key[5:]}#{s.message.seq}" for s in part.read_all())
        print(f"partition {p} ({part.log_end_offset()} records): {line}")
    print(f"acct-1001 seqs in its partition: {topic.partition(Partitioner.partition_for('acct-1001', 3)).sequence_of('acct-1001')}")
    print(f"same keys with 4 partitions: {_fmt_list(a[5:] + '->' + str(Partitioner.partition_for(a, 4)) for a in accounts)}")

    print("\n== 2. Head-of-line blocking: one slow account in a partition of fast ones ==")
    hol = Partition(0)
    keys = ["acct-fast-1", "acct-fast-2", "acct-fast-3", "acct-slow", "acct-fast-4", "acct-fast-5"]
    for m in Workload.build(keys, 60, 10, 2, "acct-slow", 40):
        hol.append(m)
    print("60 messages, one every 10 ms; fast accounts cost 2 ms, acct-slow costs 40 ms")
    sequential = SequentialScheduler().run(hol)
    seq_all = LatencyStats.of(sequential)
    seq_fast = LatencyStats.excluding(sequential, "acct-slow")
    print(f"sequential   : all p50={seq_all.p50} p99={seq_all.p99} max={seq_all.max} | "
          f"fast accounts only p50={seq_fast.p50} max={seq_fast.max}")
    fast_victim = sequential[4]
    print(f"  offset 4 ({fast_victim.key}, cost 2 ms) arrived at {hol.get(4).message.produced_at} ms, "
          f"started at {fast_victim.started_at} ms: it waited behind acct-slow at offset 3")

    print("\n== 3. Per-key lanes inside the partition: order per key, parallelism across keys ==")
    for lanes in (2, 4, 8):
        scheduler = KeyedLaneScheduler(lanes)
        laned = scheduler.run(hol)
        fast = LatencyStats.excluding(laned, "acct-slow")
        slow = LatencyStats.of([c for c in laned if c.key == "acct-slow"])
        print(f"{lanes} lanes: fast accounts p50={fast.p50} max={fast.max} | acct-slow max={slow.max} | "
              f"per-key order holds={str(Workload.per_key_order_holds(hol, laned)).lower()} | "
              f"committable at end={scheduler.final_committable()}, max finished-but-uncommittable={scheduler.max_completed_ahead()}")
        if lanes == 2:
            sharing = sorted(k for k in keys if scheduler.lane_for(k) == scheduler.lane_for("acct-slow") and k != "acct-slow")
            print(f"  keys sharing acct-slow's lane (still blocked): {_fmt_list(sharing)}")

    print("\n== 4. Commit the low-water mark, not the highest finished offset ==")
    tracker = CommitTracker(0)
    for off in (1, 2, 0, 4, 3):
        print(f"offset {off} done -> committable {tracker.complete(off)} (finished ahead: {tracker.completed_ahead()})")

    print("\n== 5. Retries reorder when more than one send is in flight ==")
    sends = Workload.build(["acct-7"], 8, 1, 1, "none", 1)
    faults = FaultInjector({2, 5})
    for in_flight in (1, 5):
        outcome = ProducerSim.deliver(sends, in_flight, faults, False)
        print(f"maxInFlight={in_flight}, sends 2 and 5 fail once: log seqs={[m.seq for m in outcome.appended]} "
              f"retries={outcome.retries} ordered={str(SequenceTracker.is_ordered(outcome.appended)).lower()}")
    reordered = ProducerSim.deliver(sends, 5, faults, False)
    tracker2 = SequenceTracker()
    verdicts = " ".join(f"{m.seq}:{tracker2.observe(m)}" for m in reordered.appended)
    print(f"consumer-side SequenceTracker on the reordered log: {verdicts}")
    print(f"  gaps={tracker2.gaps()} duplicates={tracker2.duplicates()} (a GAP followed by a lower seq is the signature of a reordered retry)")
    guarded = ProducerSim.deliver(sends, 5, faults, True)
    print(f"maxInFlight=5 with broker sequence guard: log seqs={[m.seq for m in guarded.appended]} retries={guarded.retries} "
          f"rejected={guarded.rejected} ordered={str(SequenceTracker.is_ordered(guarded.appended)).lower()}")

    print("\n== 6. The same thing at scale, seeded ==")
    many = Workload.build(["acct-9"], 2000, 1, 1, "none", 1)
    seeded = FaultInjector.random(42, 2000, 0.05)
    unguarded = ProducerSim.deliver(many, 5, seeded, False)
    guarded_many = ProducerSim.deliver(many, 5, seeded, True)
    t3 = SequenceTracker()
    for m in unguarded.appended:
        t3.observe(m)
    print(f"2000 sends, 5% fail once, maxInFlight=5: unguarded retries={unguarded.retries} gaps seen by consumer={t3.gaps()}; "
          f"guarded rejected={guarded_many.rejected} ordered={str(SequenceTracker.is_ordered(guarded_many.appended)).lower()}")


if __name__ == "__main__":
    main()
