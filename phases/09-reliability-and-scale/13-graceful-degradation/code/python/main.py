"""Lesson: phases/09-reliability-and-scale/13-graceful-degradation/docs/en.md
Graceful Degradation and Load Shedding: priority queues with CoDel, brownout modes, feature flags degrading fraud scoring
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: RFC 8289 (CoDel, incl. the reference pseudocode); Nichols & Jacobson, "Controlling Queue Delay" (ACM Queue 2012);
         Google SRE Book ch. 21-22 (overload, cascading failures); Netflix "Performance Under Load" (2018).
Python 3.11+, standard library only. Same design as the Java version.
"""

from __future__ import annotations

import math
from abc import ABC, abstractmethod
from collections import deque
from enum import Enum
from typing import Optional


class Priority(Enum):
    """Request classes in order of importance. Lower ordinal = more important = shed last."""

    CRITICAL = 0  # authorize, capture: money moving now
    HIGH = 1  # refunds, webhooks: money moving soon
    NORMAL = 2  # balance reads, status polls
    LOW = 3  # reporting, exports, analytics

    @property
    def ordinal(self) -> int:
        return self.value

    def __str__(self) -> str:
        return self.name


class Request:
    """One unit of work. ``enqueued_millis`` is stamped when it enters a queue; sojourn = dequeue time - enqueued_millis."""

    def __init__(self, id_: int, priority: Priority, arrival_millis: int) -> None:
        self.id = id_
        self.priority = priority
        self.arrival_millis = arrival_millis
        self.enqueued_millis = arrival_millis


class CoDelQueue:
    """Controlled Delay queue (RFC 8289). Drops from the head when the *minimum* sojourn time over an interval has stayed
    above the target, then drops at an increasing rate (interval / sqrt(count)) until sojourn falls back under target.
    Integer milliseconds throughout; ``sqrt`` is the one place floating point appears, exactly as in the RFC pseudocode."""

    def __init__(self, target_millis: int, interval_millis: int) -> None:
        if target_millis <= 0 or interval_millis <= target_millis:
            raise ValueError("need 0 < target < interval")
        self._target_millis = target_millis
        self._interval_millis = interval_millis
        self._queue: deque[Request] = deque()
        self._first_above_time = 0  # when sojourn first exceeded target, plus interval; 0 = not above
        self._drop_next = 0  # next scheduled drop time while in dropping state
        self._count = 0  # drops in the current dropping episode (drives the control law)
        self._last_count = 0
        self._dropping = False
        self.dropped = 0
        self.max_sojourn_seen = 0

    def enqueue(self, r: Request, now_millis: int) -> None:
        r.enqueued_millis = now_millis
        self._queue.append(r)

    def size(self) -> int:
        return len(self._queue)

    def is_dropping(self) -> bool:
        return self._dropping

    def _do_dequeue(self, now: int) -> tuple[Optional[Request], bool]:
        """RFC 8289 Section 5.3: pop one, and decide whether the sojourn signal says the queue is in a persistent-delay state."""
        if not self._queue:
            self._first_above_time = 0
            return None, False
        r = self._queue.popleft()
        sojourn = now - r.enqueued_millis
        self.max_sojourn_seen = max(self.max_sojourn_seen, sojourn)
        if sojourn < self._target_millis:
            self._first_above_time = 0  # delay is fine; reset the "above target since" clock
            return r, False
        if self._first_above_time == 0:
            self._first_above_time = now + self._interval_millis  # start the interval timer; do not drop yet
            return r, False
        return r, now >= self._first_above_time  # above target for a whole interval: ok to drop

    def _control_law(self, t: int, c: int) -> int:
        return t + int(self._interval_millis / math.sqrt(c))

    def dequeue(self, now: int) -> Optional[Request]:
        """RFC 8289 Section 5.4. Returns the request to serve, or None if the queue is empty."""
        r, ok = self._do_dequeue(now)
        if self._dropping:
            if not ok:
                self._dropping = False  # sojourn fell under target: leave dropping state
            else:
                while now >= self._drop_next and self._dropping:
                    self.dropped += 1  # drop r
                    self._count += 1
                    r, ok = self._do_dequeue(now)
                    if not ok:
                        self._dropping = False
                    else:
                        self._drop_next = self._control_law(self._drop_next, self._count)
        elif ok:
            self.dropped += 1  # first drop of a new episode
            r, ok = self._do_dequeue(now)
            self._dropping = True
            # If we were dropping recently, resume at a higher rate instead of restarting from count = 1.
            delta = self._count - self._last_count
            self._count = delta if (delta > 1 and now - self._drop_next < 16 * self._interval_millis) else 1
            self._drop_next = self._control_law(now, self._count)
            self._last_count = self._count
        return r


class Flags:
    """Runtime switches. In production a flag service; here a map the brownout controller flips."""

    def __init__(self) -> None:
        self._values: dict[str, str] = {}

    def get(self, key: str, dflt: str) -> str:
        return self._values.get(key, dflt)

    def set(self, key: str, value: str) -> None:
        self._values[key] = value


class FraudScorer:
    """Fraud scoring whose cost depends on a flag: the full model, or a cheap rule set under brownout."""

    FULL_MILLIS = 8
    RULES_ONLY_MILLIS = 1

    def service_millis(self, flags: Flags) -> int:
        return self.RULES_ONLY_MILLIS if flags.get("fraud.mode", "full") == "rules_only" else self.FULL_MILLIS


class Level(Enum):
    """Brownout levels. Each level sheds one more priority class at the door and degrades one more feature."""

    NORMAL = (0, None, "full")
    SHED_LOW = (1, Priority.LOW, "rules_only")
    SHED_NORMAL = (2, Priority.NORMAL, "rules_only")
    CRITICAL_ONLY = (3, Priority.HIGH, "rules_only")

    def __init__(self, ordinal: int, shed_at_or_below: Optional[Priority], fraud_mode: str) -> None:
        self.ordinal = ordinal
        self.shed_at_or_below = shed_at_or_below  # reject requests with priority ordinal >= this one's ordinal
        self.fraud_mode = fraud_mode

    def sheds(self, p: Priority) -> bool:
        return self.shed_at_or_below is not None and p.ordinal >= self.shed_at_or_below.ordinal

    def __str__(self) -> str:
        return self.name


LEVELS = list(Level)


class BrownoutController:
    """Escalates when CRITICAL/HIGH sojourn exceeds target for a window; de-escalates after ``cooldown_windows`` calm windows."""

    Level = Level

    def __init__(self, target_millis: int, window_millis: int, cooldown_windows: int, flags: Flags, now_millis: int) -> None:
        self._target_millis = target_millis
        self._window_millis = window_millis
        self._cooldown_windows = cooldown_windows
        self._flags = flags
        self._level = Level.NORMAL
        self._window_start = now_millis
        self._window_max_sojourn = 0
        self._calm_windows = 0
        self.transitions: list[str] = []
        self.millis_at_level = [0] * len(LEVELS)
        self._last_tick = now_millis
        flags.set("fraud.mode", self._level.fraud_mode)

    def level(self) -> Level:
        return self._level

    def observe(self, sojourn_millis: int) -> None:
        """Called with the sojourn of every CRITICAL or HIGH request as it is dequeued."""
        self._window_max_sojourn = max(self._window_max_sojourn, sojourn_millis)

    def tick(self, now_millis: int) -> None:
        """Called every simulated millisecond. Closes a window when it elapses and moves at most one level."""
        self.millis_at_level[self._level.ordinal] += now_millis - self._last_tick
        self._last_tick = now_millis
        if now_millis - self._window_start < self._window_millis:
            return
        before = self._level
        if self._window_max_sojourn > self._target_millis:
            self._calm_windows = 0
            if self._level.ordinal < len(LEVELS) - 1:
                self._level = LEVELS[self._level.ordinal + 1]
        elif self._window_max_sojourn <= self._target_millis // 2:
            self._calm_windows += 1
            if self._calm_windows >= self._cooldown_windows and self._level != Level.NORMAL:
                self._level = LEVELS[self._level.ordinal - 1]
                self._calm_windows = 0
        else:
            self._calm_windows = 0  # in the hysteresis band: hold
        if self._level != before:
            self.transitions.append(f"{now_millis}ms {before} -> {self._level}")
            self._flags.set("fraud.mode", self._level.fraud_mode)
        self._window_max_sojourn = 0
        self._window_start = now_millis


class Outcome:
    """What a policy did with the offered load, per priority."""

    def __init__(self) -> None:
        self.offered: dict[Priority, int] = {p: 0 for p in Priority}
        self.shed_at_door: dict[Priority, int] = {p: 0 for p in Priority}
        self.dropped_in_queue: dict[Priority, int] = {p: 0 for p in Priority}
        self.latencies: dict[Priority, list[int]] = {p: [] for p in Priority}
        self.still_queued = 0

    def completed(self, p: Priority) -> int:
        return len(self.latencies[p])

    def percentile(self, p: Priority, q: float) -> int:
        ordered = sorted(self.latencies[p])
        if not ordered:
            return -1
        idx = math.ceil(q * len(ordered)) - 1
        return ordered[max(0, min(len(ordered) - 1, idx))]

    def total_offered(self) -> int:
        return sum(self.offered.values())

    def total_completed(self) -> int:
        return sum(self.completed(p) for p in Priority)

    def total_shed(self) -> int:
        return sum(self.shed_at_door.values())

    def total_dropped(self) -> int:
        return sum(self.dropped_in_queue.values())


def base_service_millis(p: Priority) -> int:
    return {Priority.CRITICAL: 4, Priority.HIGH: 4, Priority.NORMAL: 3, Priority.LOW: 6}[p]


class Policy(ABC):
    """A serving policy: how arriving requests are admitted and which one a free worker serves next."""

    @abstractmethod
    def name(self) -> str: ...

    @abstractmethod
    def admit(self, r: Request, now: int, out: Outcome) -> bool:
        """Returns False if the request is rejected at the door."""

    @abstractmethod
    def next(self, now: int, out: Outcome) -> Optional[Request]:
        """Next request to serve, or None; may drop stale requests as a side effect, recorded in ``out``."""

    @abstractmethod
    def service_millis(self, r: Request) -> int: ...

    @abstractmethod
    def tick(self, now: int) -> None: ...

    @abstractmethod
    def queued(self) -> int: ...


class FifoPolicy(Policy):
    """The baseline everyone ships first: one FIFO queue, no limit, full fraud scoring for everyone."""

    def __init__(self) -> None:
        self._queue: deque[Request] = deque()
        self._flags = Flags()
        self._fraud = FraudScorer()

    def name(self) -> str:
        return "fifo, no shedding"

    def admit(self, r: Request, now: int, out: Outcome) -> bool:
        self._queue.append(r)
        return True

    def next(self, now: int, out: Outcome) -> Optional[Request]:
        return self._queue.popleft() if self._queue else None

    def service_millis(self, r: Request) -> int:
        return base_service_millis(r.priority) + (self._fraud.service_millis(self._flags) if r.priority == Priority.CRITICAL else 0)

    def tick(self, now: int) -> None:
        pass

    def queued(self) -> int:
        return len(self._queue)


class DegradingPolicy(Policy):
    """Priority CoDel queues, brownout controller, and flag-driven fraud degradation."""

    def __init__(self, target_millis: int, interval_millis: int, window_millis: int, cooldown_windows: int,
                 now_millis: int) -> None:
        self.queues: dict[Priority, CoDelQueue] = {p: CoDelQueue(target_millis, interval_millis) for p in Priority}
        self.flags = Flags()
        self.brownout = BrownoutController(target_millis, window_millis, cooldown_windows, self.flags, now_millis)
        self._fraud = FraudScorer()

    def name(self) -> str:
        return "priority + CoDel + brownout"

    def admit(self, r: Request, now: int, out: Outcome) -> bool:
        if self.brownout.level().sheds(r.priority):
            out.shed_at_door[r.priority] += 1
            return False
        self.queues[r.priority].enqueue(r, now)
        return True

    def next(self, now: int, out: Outcome) -> Optional[Request]:
        for p in Priority:
            q = self.queues[p]
            before = q.dropped
            r = q.dequeue(now)
            dropped_now = q.dropped - before
            if dropped_now > 0:
                out.dropped_in_queue[p] += dropped_now
            if r is not None:
                if p in (Priority.CRITICAL, Priority.HIGH):
                    self.brownout.observe(now - r.enqueued_millis)
                return r
        return None

    def service_millis(self, r: Request) -> int:
        return base_service_millis(r.priority) + (self._fraud.service_millis(self.flags) if r.priority == Priority.CRITICAL else 0)

    def tick(self, now: int) -> None:
        self.brownout.tick(now)

    def queued(self) -> int:
        return sum(q.size() for q in self.queues.values())


class JavaRandom:
    """java.util.Random, bit for bit (48-bit LCG), so the seeded arrivals match the Java demo exactly."""

    _MULTIPLIER = 0x5DEECE66D
    _ADDEND = 0xB
    _MASK = (1 << 48) - 1

    def __init__(self, seed: int) -> None:
        self._seed = (seed ^ self._MULTIPLIER) & self._MASK

    def next(self, bits: int) -> int:
        self._seed = (self._seed * self._MULTIPLIER + self._ADDEND) & self._MASK
        r = self._seed >> (48 - bits)
        return r - (1 << 32) if r >= (1 << 31) else r

    def next_double(self) -> float:
        return ((self.next(26) << 27) + self.next(27)) * (1.0 / (1 << 53))


def pick(u: float) -> Priority:
    """Traffic mix: 30% critical, 10% high, 40% normal, 20% low."""
    if u < 0.30:
        return Priority.CRITICAL
    if u < 0.40:
        return Priority.HIGH
    if u < 0.80:
        return Priority.NORMAL
    return Priority.LOW


class Simulation:
    """Discrete-time simulation: ``workers`` parallel servers, 1 ms ticks, Bernoulli arrivals per tick from a seeded RNG,
    with an overload phase in the middle. Deterministic for a given seed."""

    def __init__(self, workers: int, seed: int, normal_rps: float, overload_rps: float, overload_from: int,
                 overload_to: int, duration: int) -> None:
        self.workers = workers
        self.seed = seed
        self.normal_rps = normal_rps
        self.overload_rps = overload_rps
        self.overload_from_millis = overload_from
        self.overload_to_millis = overload_to
        self.duration_millis = duration

    base_service_millis = staticmethod(base_service_millis)
    pick = staticmethod(pick)

    def run(self, policy: Policy) -> Outcome:
        random = JavaRandom(self.seed)
        out = Outcome()
        busy_until = [0] * self.workers
        in_service: list[Optional[Request]] = [None] * self.workers
        next_id = 1
        for now in range(self.duration_millis):
            rps = self.overload_rps if self.overload_from_millis <= now < self.overload_to_millis else self.normal_rps
            # arrivals this millisecond: expected rps/1000, drawn as up to three Bernoulli trials for a Poisson-ish shape
            p = rps / 3000.0
            for _ in range(3):
                if random.next_double() < p:
                    r = Request(next_id, pick(random.next_double()), now)
                    next_id += 1
                    out.offered[r.priority] += 1
                    policy.admit(r, now, out)
            # workers: finish what is due, then pull the next request
            for w in range(self.workers):
                cur = in_service[w]
                if cur is not None and busy_until[w] <= now:
                    out.latencies[cur.priority].append(now - cur.arrival_millis)
                    in_service[w] = None
                if in_service[w] is None:
                    r = policy.next(now, out)
                    if r is not None:
                        in_service[w] = r
                        busy_until[w] = now + policy.service_millis(r)
            policy.tick(now)
        out.still_queued = policy.queued() + sum(1 for r in in_service if r is not None)
        return out


WORKERS = 8
TARGET, INTERVAL, WINDOW = 5, 100, 100
COOLDOWN = 5


def standard_scenario(seed: int) -> Simulation:
    # 8 workers, mean service ~6.4 ms at full fraud -> capacity ~1,250 rps. Normal 800 rps; overload 2,400 rps for 3 s.
    return Simulation(WORKERS, seed, 800, 2400, 2000, 5000, 9000)


def print_outcome(o: Outcome) -> None:
    print(f"{'priority':<9} {'offered':>8} {'shed':>8} {'dropped':>8} {'completed':>9} {'p50 ms':>8} {'p99 ms':>8}")
    for p in Priority:
        print(f"{p.name:<9} {o.offered[p]:8d} {o.shed_at_door[p]:8d} {o.dropped_in_queue[p]:8d} {o.completed(p):9d} "
              f"{o.percentile(p, 0.50):8d} {o.percentile(p, 0.99):8d}")
    print(f"total: offered {o.total_offered()}, completed {o.total_completed()}, shed at door {o.total_shed()}, "
          f"dropped in queue {o.total_dropped()}, still queued at end {o.still_queued}\n")


def main() -> None:
    print("== CoDel on one queue: 5 ms target, 100 ms interval ==")
    q = CoDelQueue(TARGET, INTERVAL)
    now = 0
    for i in range(400):
        q.enqueue(Request(i, Priority.NORMAL, now), now)
    served = 0
    now = 0
    while now < 400 and q.size() > 0:  # drain one per ms: a 400-deep queue sojourns up to 400 ms
        if q.dequeue(now) is not None:
            served += 1
        now += 1
    print(f"400 queued, drained at 1/ms: served {served}, dropped {q.dropped}, max sojourn seen {q.max_sojourn_seen} ms")
    print("(sojourn reaches the 5 ms target at t=5; the first drop waits a full interval, at t=105; later drops are spaced interval/sqrt(count))")
    print()

    sim = standard_scenario(11)
    print("== 8 workers, 800 rps normal, 2,400 rps overload from t=2s to t=5s, 9 s total (seed 11) ==")
    fifo_policy = FifoPolicy()
    print(f"-- {fifo_policy.name()} --")
    fifo = sim.run(fifo_policy)
    print_outcome(fifo)

    degrading = DegradingPolicy(TARGET, INTERVAL, WINDOW, COOLDOWN, 0)
    print(f"-- {degrading.name()} --")
    shed = sim.run(degrading)
    print_outcome(shed)

    print("brownout transitions:")
    for t in degrading.brownout.transitions:
        print(f"  {t}")
    print("time at each level (ms):" + "".join(f"  {lv}={degrading.brownout.millis_at_level[lv.ordinal]}" for lv in Level))
    print(f"fraud.mode at end: {degrading.flags.get('fraud.mode', 'full')}")
    print()
    print("Reading the tables: FIFO completes more LOW requests and makes every CRITICAL authorization wait behind them,")
    print("so CRITICAL p99 explodes and the backlog is still draining when the run ends. The degrading policy sheds LOW")
    print("and NORMAL at the door, flips fraud to rules-only, lets CoDel trim stale queue entries, and keeps CRITICAL p99")
    print("within tens of milliseconds through the whole overload. It completes fewer requests and the right ones.")


if __name__ == "__main__":
    main()
