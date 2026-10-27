"""Lesson: phases/08-messaging-and-streams/09-backpressure/docs/en.md
Backpressure and Flow Control
Run:  python3 main.py
Test: python3 -m unittest -v
Python 3.11+, standard library only. Same design as the Java version. Pull-based demand follows the Reactive Streams
specification (Subscription.request(n), rule 3.17: demand is additive and never exceeds Long.MAX_VALUE).
JavaRandom reproduces java.util.Random so the seeded simulation prints the same counts as the Java demo.
"""

from __future__ import annotations

import math
from collections import deque
from dataclasses import dataclass
from enum import Enum
from typing import Deque, Generic, List, Optional, TypeVar


@dataclass(frozen=True)
class Currency:
    """ISO 4217 currency: three-letter code plus minor-unit exponent. Copied from Phase 10, Lesson 01."""

    code: str
    exponent: int

    def __post_init__(self) -> None:
        if self.code is None or len(self.code) != 3:
            raise ValueError("currency code must be three letters")
        if self.exponent < 0 or self.exponent > 4:
            raise ValueError("exponent must be 0..4")


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

    @staticmethod
    def zero(currency: Currency) -> "Money":
        return Money(0, currency)

    def plus(self, other: "Money") -> "Money":
        if self.currency != other.currency:
            raise ValueError(f"cannot combine {self.currency.code} with {other.currency.code}")
        return Money(self.minor_units + other.minor_units, self.currency)

    def __str__(self) -> str:
        scale = 10 ** self.currency.exponent
        abs_units = abs(self.minor_units)
        frac = "" if self.currency.exponent == 0 else "." + str(abs_units % scale).rjust(self.currency.exponent, "0")
        return f"{'-' if self.minor_units < 0 else ''}{abs_units // scale}{frac} {self.currency.code}"


@dataclass(frozen=True)
class PaymentEvent:
    """A payment event flowing from the authorization service to the ledger poster."""

    id: int
    produced_at_tick: int
    amount: Money


class OverflowPolicy(Enum):
    """What a bounded queue does when it is full and another item is offered."""

    BLOCK = "BLOCK"                # Refuse the item; the caller keeps it and must retry. The producer slows down.
    DROP_NEWEST = "DROP_NEWEST"    # Refuse and discard the new item. Freshest data lost.
    DROP_OLDEST = "DROP_OLDEST"    # Evict the head to make room. Oldest data lost.


class OfferResult(Enum):
    """Outcome of a single offer."""

    ACCEPTED = "ACCEPTED"
    REJECTED_FULL = "REJECTED_FULL"
    DROPPED_NEWEST = "DROPPED_NEWEST"
    EVICTED_OLDEST = "EVICTED_OLDEST"


T = TypeVar("T")

INT_MAX = 2**31 - 1
LONG_MAX = 2**63 - 1


class BoundedQueue(Generic[T]):
    """A FIFO queue with a hard capacity and an explicit overflow policy. Capacity INT_MAX models
    the "unbounded" queue that every system has by default and that is the thing this lesson argues against.
    A plain deque, never deque(maxlen=n): that would drop the oldest silently and hide the policy."""

    def __init__(self, capacity: int, policy: OverflowPolicy) -> None:
        if capacity <= 0:
            raise ValueError("capacity must be positive")
        if policy is None:
            raise TypeError("policy")
        self._items: Deque[T] = deque()
        self._capacity = capacity
        self._policy = policy
        self._dropped = 0
        self._max_depth = 0

    def offer(self, item: T) -> OfferResult:
        if item is None:
            raise TypeError("item")
        if len(self._items) < self._capacity:
            self._items.append(item)
            self._max_depth = max(self._max_depth, len(self._items))
            return OfferResult.ACCEPTED
        if self._policy is OverflowPolicy.BLOCK:
            return OfferResult.REJECTED_FULL
        if self._policy is OverflowPolicy.DROP_NEWEST:
            self._dropped += 1
            return OfferResult.DROPPED_NEWEST
        self._items.popleft()
        self._dropped += 1
        self._items.append(item)
        return OfferResult.EVICTED_OLDEST

    def poll(self) -> Optional[T]:
        return self._items.popleft() if self._items else None

    def size(self) -> int:
        return len(self._items)

    def is_full(self) -> bool:
        return len(self._items) >= self._capacity

    def capacity(self) -> int:
        return self._capacity

    def dropped(self) -> int:
        return self._dropped

    def max_depth(self) -> int:
        return self._max_depth


class DemandSubscription:
    """Pull-based demand, as in Reactive Streams. The consumer calls request(n) to say how many more items
    it can take; the producer may emit only while outstanding demand is positive. Demand is additive and
    saturates at LONG_MAX (2**63 - 1) instead of growing further (spec rule 3.17). Python ints would not wrap,
    but the saturation point is part of the contract, so it is kept explicitly."""

    def __init__(self) -> None:
        self._demand = 0

    def request(self, n: int) -> None:
        if n <= 0:
            raise ValueError("request(n) requires n > 0 (Reactive Streams rule 3.9)")
        self._demand = min(self._demand + n, LONG_MAX)  # saturate, never wrap

    def has_demand(self) -> bool:
        return self._demand > 0

    def try_consume_one(self) -> bool:
        """Consume one unit of demand; returns False if none was available."""
        if self._demand <= 0:
            return False
        if self._demand != LONG_MAX:
            self._demand -= 1
        return True

    def outstanding(self) -> int:
        return self._demand


class Strategy(Enum):
    """The five ways to connect a fast producer to a slow consumer."""

    UNBOUNDED = "UNBOUNDED"
    DROP_NEWEST = "DROP_NEWEST"
    DROP_OLDEST = "DROP_OLDEST"
    BLOCK = "BLOCK"
    PULL = "PULL"

    def __str__(self) -> str:
        return self.name


@dataclass(frozen=True)
class Stats:
    """What happened over a run. Every count is exact and the two accounting identities tie them together:
      offered_load == produced + throttled           (every request was either admitted or turned away)
      produced == delivered + dropped + in_flight + in_queue   (every admitted event is in exactly one place)"""

    strategy: Strategy
    offered_load: int
    produced: int
    throttled: int
    delivered: int
    dropped: int
    in_flight: int
    in_queue: int
    rejected_offers: int
    producer_blocked_ticks: int
    max_queue_depth: int
    max_latency_ticks: int
    p99_latency_ticks: int
    delivered_amount: Money

    def conserves(self) -> bool:
        return (self.offered_load == self.produced + self.throttled
                and self.produced == self.delivered + self.dropped + self.in_flight + self.in_queue)


class JavaRandom:
    """java.util.Random: the 48-bit linear congruential generator, including nextInt(bound)'s rejection loop,
    so a seed yields the same jitter, amounts, and stalls as the Java demo."""

    _MASK = (1 << 48) - 1

    def __init__(self, seed: int) -> None:
        self._seed = (seed ^ 0x5DEECE66D) & self._MASK

    def next(self, bits: int) -> int:
        self._seed = (self._seed * 0x5DEECE66D + 0xB) & self._MASK
        value = (self._seed >> (48 - bits)) & 0xFFFFFFFF
        return value - (1 << 32) if value >= 1 << 31 else value  # Java casts to a signed 32-bit int

    def next_int(self, bound: int) -> int:
        if bound <= 0:
            raise ValueError("bound must be positive")
        if (bound & -bound) == bound:  # power of two
            return (bound * self.next(31)) >> 31
        while True:
            bits = self.next(31)
            val = bits % bound
            if bits - val + (bound - 1) <= INT_MAX:  # Java: while (bits - val + (bound-1) < 0), i.e. int overflow
                return val

    def next_double(self) -> float:
        return ((self.next(26) << 27) + self.next(27)) * (1.0 / (1 << 53))


class FlowSimulation:
    """A deterministic tick-based simulation of one producer, one queue, one consumer. Upstream requests
    arrive at ~producer_rate per tick with seeded jitter; the consumer drains consumer_rate per tick but
    stalls (processes nothing) on a seeded fraction of ticks, like a GC pause or a slow downstream call.
    The producer is a request handler: while it is blocked holding an event the queue refused, it cannot
    admit new requests, and those are counted as throttled (the HTTP 429 / slow-accept the caller sees).
    No threads, no sleeping: the tick counter is the clock."""

    def __init__(self, strategy: Strategy, producer_rate: int, producer_jitter: int, consumer_rate: int,
                 consumer_stall_probability: float, queue_capacity: int, seed: int) -> None:
        self._strategy = strategy
        self._producer_rate = producer_rate
        self._producer_jitter = producer_jitter
        self._consumer_rate = consumer_rate
        self._consumer_stall_probability = consumer_stall_probability
        self._queue_capacity = queue_capacity
        self._random = JavaRandom(seed)

    def run(self, ticks: int) -> Stats:
        strategy = self._strategy
        if strategy is Strategy.DROP_NEWEST:
            policy = OverflowPolicy.DROP_NEWEST
        elif strategy is Strategy.DROP_OLDEST:
            policy = OverflowPolicy.DROP_OLDEST
        else:
            policy = OverflowPolicy.BLOCK
        capacity = INT_MAX if strategy is Strategy.UNBOUNDED else self._queue_capacity
        queue: BoundedQueue[PaymentEvent] = BoundedQueue(capacity, policy)
        subscription = DemandSubscription()
        if strategy is Strategy.PULL:
            subscription.request(self._queue_capacity)  # initial demand = buffer size

        in_flight: Optional[PaymentEvent] = None  # produced, refused by the queue, held by the blocked producer
        latencies: List[int] = []
        offered = produced = throttled = delivered = rejected = blocked_ticks = 0
        next_id = 1
        delivered_amount = Money.zero(USD)
        rnd = self._random

        for tick in range(ticks):
            blocked_this_tick = False

            # Producer, step 1: retry the event it is holding from last tick, if any.
            if in_flight is not None:
                if queue.offer(in_flight) is OfferResult.REJECTED_FULL:
                    rejected += 1
                    blocked_this_tick = True
                else:
                    in_flight = None

            # Producer, step 2: admit this tick's requests, one at a time, until blocked.
            want = self._producer_rate + (0 if self._producer_jitter == 0 else rnd.next_int(self._producer_jitter + 1))
            for _ in range(want):
                offered += 1
                cents = 100 + rnd.next_int(9900)  # drawn for every request so all strategies see the identical load
                if in_flight is not None:                      # BLOCK: handler is stuck, caller sees 429 / slow accept
                    throttled += 1
                    continue
                if strategy is Strategy.PULL and not subscription.try_consume_one():  # PULL: no demand, refuse before producing
                    throttled += 1
                    blocked_this_tick = True
                    continue
                e = PaymentEvent(next_id, tick, Money.of_minor(cents, USD))
                next_id += 1
                produced += 1
                r = queue.offer(e)
                if r is OfferResult.REJECTED_FULL:
                    rejected += 1
                    in_flight = e
                    blocked_this_tick = True
                # ACCEPTED, DROPPED_NEWEST, EVICTED_OLDEST: the queue took responsibility for the event
            if blocked_this_tick:
                blocked_ticks += 1

            # Consumer: drain up to consumer_rate, unless stalled this tick.
            stalled = rnd.next_double() < self._consumer_stall_probability
            processed = 0
            if not stalled:
                for _ in range(self._consumer_rate):
                    e = queue.poll()
                    if e is None:
                        break
                    delivered += 1
                    processed += 1
                    latencies.append(tick - e.produced_at_tick)
                    delivered_amount = delivered_amount.plus(e.amount)
            if strategy is Strategy.PULL and processed > 0:
                subscription.request(processed)  # demand = capacity just freed

        latencies.sort()
        max_latency = latencies[-1] if latencies else 0
        p99 = latencies[min(len(latencies) - 1, math.floor(len(latencies) * 0.99))] if latencies else 0
        return Stats(strategy, offered, produced, throttled, delivered, queue.dropped(), 0 if in_flight is None else 1,
                     queue.size(), rejected, blocked_ticks, queue.max_depth(), max_latency, p99, delivered_amount)


def row(s: Stats) -> str:
    return (f"{str(s.strategy):<12} offered={s.offered_load:6d} produced={s.produced:6d} throttled={s.throttled:5d} "
            f"delivered={s.delivered:6d} dropped={s.dropped:5d} inQueue={s.in_queue:5d} rejectedOffers={s.rejected_offers:5d} "
            f"maxDepth={s.max_queue_depth:6d} maxLat={s.max_latency_ticks:5d} p99Lat={s.p99_latency_ticks:4d}")


def main() -> None:
    # Authorization service emits ~12 events/tick (10 + jitter 0..4); ledger poster handles 10/tick and stalls 10% of ticks.
    # Sustained overload: the consumer cannot keep up and something has to give.
    producer_rate, jitter, consumer_rate, capacity, ticks = 10, 4, 10, 64, 2000
    stall = 0.10
    seed = 42

    print(f"== Fast producer (~12/tick), slow consumer (10/tick, 10% stalls), queue capacity {capacity}, {ticks} ticks, seed {seed} ==")
    print("One tick is one scheduling quantum; latency is in ticks from production to delivery.")
    print()
    for s in Strategy:
        st = FlowSimulation(s, producer_rate, jitter, consumer_rate, stall, capacity, seed).run(ticks)
        print(f"{row(st)}  conserves={str(st.conserves()).lower()}")

    print()
    print("== Same strategies when the consumer is fast enough (12/tick, no stalls): backpressure costs nothing ==")
    for s in Strategy:
        st = FlowSimulation(s, producer_rate, jitter, 13, 0.0, capacity, seed).run(ticks)
        print(row(st))

    print()
    print("== Reading the table ==")
    print("UNBOUNDED : nothing dropped, nothing throttled, queue depth and latency grow without limit. This is the OOM at 3 a.m.")
    print("DROP_*    : bounded memory and latency, but admitted payment events are gone. Dropped money is a ledger incident.")
    print("BLOCK     : bounded memory and latency, nothing admitted is lost; overload surfaces as throttled requests, discovered by collision (rejectedOffers).")
    print("PULL      : same bounds and the same throttling, but the producer knows the demand up front and never builds an event it cannot deliver.")


if __name__ == "__main__":
    main()
