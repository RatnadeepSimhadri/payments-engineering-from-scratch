"""Lesson: phases/05-distributed-foundations/12-leases-and-fencing-tokens/docs/en.md
Leases, Fencing Tokens, and Distributed Locks
Run:  python3 main.py
Test: python3 -m unittest -v
Sources: Gray & Cheriton, "Leases" (SOSP 1989); Burrows, "The Chubby lock service" (OSDI 2006); Kleppmann, "How to do
distributed locking" (2016); Redis Redlock page. Time is injected through Clock; process pauses are clock.advance().
"""

from __future__ import annotations

from dataclasses import dataclass
from enum import Enum
from typing import Optional


class JavaRandom:
    """java.util.Random's 48-bit LCG and nextInt(bound), ported exactly so seed 7 replays the same 2000 races."""

    _MULTIPLIER, _ADDEND, _MASK = 0x5DEECE66D, 0xB, (1 << 48) - 1

    def __init__(self, seed: int) -> None:
        self._seed = (seed ^ self._MULTIPLIER) & self._MASK

    def next(self, bits: int) -> int:
        self._seed = (self._seed * self._MULTIPLIER + self._ADDEND) & self._MASK
        result = self._seed >> (48 - bits)
        return result - (1 << 32) if result & (1 << 31) else result  # (int) cast: signed 32-bit

    def next_int(self, bound: int) -> int:
        if bound <= 0:
            raise ValueError("bound must be positive")
        r = self.next(31)
        m = bound - 1
        if bound & m == 0:
            return (bound * r) >> 31
        u = r
        while True:
            r = u % bound
            if ((u - r + m) & 0xFFFFFFFF) < 0x80000000:  # the JDK's int-overflow rejection test
                return r
            u = self.next(31)


class Clock:
    """Source of time. The lock service and every client read the same simulated clock in this lesson."""

    def now_ms(self) -> int:
        raise NotImplementedError


class FakeClock(Clock):
    def __init__(self, start_ms: int) -> None:
        self._now = start_ms

    def now_ms(self) -> int:
        return self._now

    def advance(self, ms: int) -> None:
        """A pause: GC stop-the-world, a VM migration, a swapped-out page, or a long network stall."""
        if ms < 0:
            raise ValueError(f"time only moves forward: {ms}")
        self._now += ms


class LeaseExpiredException(RuntimeError):
    """Raised when a client tries to renew or release a lease it no longer holds."""


class StaleTokenException(RuntimeError):
    """Raised by fenced storage when a writer presents a token older than one it has already accepted."""

    def __init__(self, key: str, presented: int, highest_seen: int) -> None:
        super().__init__(f"stale fencing token {presented} for {key}; storage has already seen {highest_seen}")
        self.presented = presented
        self.highest_seen = highest_seen


def or_else_throw(value: Optional["Lease"]) -> "Lease":
    """Optional.orElseThrow(): the demo's way of saying "this acquire must succeed"."""
    if value is None:
        raise LookupError("No value present")
    return value


@dataclass(frozen=True)
class Lease:
    """What a client holds after acquiring the lock: who, until when, and a token strictly larger than every token the
    lock has ever issued before. The token is the only part a remote resource can trust."""

    resource: str
    owner: str
    token: int
    granted_at_ms: int
    expires_at_ms: int

    def is_expired(self, clock: Clock) -> bool:
        """Expiry according to the lock service's clock."""
        return clock.now_ms() >= self.expires_at_ms

    def is_safe_to_use(self, clock: Clock, margin_ms: int) -> bool:
        """The client-side question: is it still safe to act? Stop trusting a lease a margin before nominal expiry."""
        return clock.now_ms() + margin_ms < self.expires_at_ms

    def ttl_ms(self) -> int:
        return self.expires_at_ms - self.granted_at_ms


class LeaseLock:
    """A lease-based lock on one named resource. The lock service is the only place tokens are minted, and it mints
    them from a counter that never goes backwards."""

    def __init__(self, clock: Clock, resource: str) -> None:
        self._clock = clock
        self._resource = resource
        self._next_token = 1
        self._current: Optional[Lease] = None      # None when free
        self._history: list[str] = []

    def try_acquire(self, owner: str, ttl_ms: int) -> Optional[Lease]:
        """Grant the lock if it is free or the previous lease has expired. Expiry is decided here, by this clock."""
        if ttl_ms <= 0:
            raise ValueError(f"ttl must be positive: {ttl_ms}")
        now = self._clock.now_ms()
        cur = self._current
        if cur is not None and not cur.is_expired(self._clock):
            self._history.append(f"t={now} {owner} denied (held by {cur.owner} token {cur.token})")
            return None
        if cur is not None:
            self._history.append(f"t={now} lease of {cur.owner} (token {cur.token}) expired at t={cur.expires_at_ms}")
        self._current = Lease(self._resource, owner, self._next_token, now, now + ttl_ms)
        self._next_token += 1
        self._history.append(f"t={now} {owner} acquired token {self._current.token} until t={self._current.expires_at_ms}")
        return self._current

    def renew(self, lease: Lease, ttl_ms: int) -> Lease:
        """Extend a lease the caller still holds. Renewing an expired or superseded lease is an error."""
        self._require_held(lease, "renew")
        self._current = Lease(self._resource, lease.owner, lease.token, lease.granted_at_ms,
                              self._clock.now_ms() + ttl_ms)
        self._history.append(f"t={self._clock.now_ms()} {lease.owner} renewed token {lease.token} "
                             f"until t={self._current.expires_at_ms}")
        return self._current

    def release(self, lease: Lease) -> None:
        self._require_held(lease, "release")
        self._history.append(f"t={self._clock.now_ms()} {lease.owner} released token {lease.token}")
        self._current = None

    def _require_held(self, lease: Lease, action: str) -> None:
        cur = self._current
        if cur is None or cur.token != lease.token or cur.is_expired(self._clock):
            raise LeaseExpiredException(f"{lease.owner} cannot {action}: token {lease.token} is no longer the live "
                                        f"lease at t={self._clock.now_ms()}")

    def holder(self) -> Optional[Lease]:
        cur = self._current
        return None if cur is None or cur.is_expired(self._clock) else cur

    def highest_token_issued(self) -> int:
        return self._next_token - 1

    def history(self) -> list[str]:
        return list(self._history)


class Storage:
    """The protected resource: a key-value store that writers reach after taking the lock."""

    def read(self, key: str) -> int:
        raise NotImplementedError

    def write(self, key: str, value: int, token: int) -> None:
        raise NotImplementedError

    def write_log(self) -> list[str]:
        raise NotImplementedError


class UnfencedStorage(Storage):
    """Trusts every writer. The lock is the only protection, and the lock cannot see a paused client."""

    def __init__(self) -> None:
        self._data: dict[str, int] = {}
        self._log: list[str] = []

    def read(self, key: str) -> int:
        return self._data.get(key, 0)

    def write(self, key: str, value: int, token: int) -> None:
        self._data[key] = value
        self._log.append(f"{key}={value} (token {token})")

    def write_log(self) -> list[str]:
        return list(self._log)


class FencedStorage(Storage):
    """Checks the fencing token on every write and rejects anything older than the newest token seen for that key.
    The check and the write are one atomic step at the resource, which the lock alone cannot provide."""

    def __init__(self) -> None:
        self._data: dict[str, int] = {}
        self._highest_token: dict[str, int] = {}
        self._log: list[str] = []

    def read(self, key: str) -> int:
        return self._data.get(key, 0)

    def write(self, key: str, value: int, token: int) -> None:
        seen = self._highest_token.get(key, 0)
        if token < seen:
            self._log.append(f"REJECTED {key}={value} (token {token} < {seen})")
            raise StaleTokenException(key, token, seen)
        self._highest_token[key] = token
        self._data[key] = value
        self._log.append(f"{key}={value} (token {token})")

    def highest_token_seen(self, key: str) -> int:
        return self._highest_token.get(key, 0)

    def write_log(self) -> list[str]:
        return list(self._log)


class PayoutWorker:
    """Lock, read the pending balance, subtract the payout, write it back. The pauses model a stop-the-world stall
    between the read and the write, where a lease can expire under a client that still believes it holds the lock."""

    class Result(Enum):
        APPLIED = "APPLIED"
        LOCK_BUSY = "LOCK_BUSY"
        FENCED_OFF = "FENCED_OFF"

    def __init__(self, name: str, clock: FakeClock, lock: LeaseLock, storage: Storage) -> None:
        self.name = name
        self._clock = clock
        self._lock = lock
        self._storage = storage

    def payout(self, account: str, amount_minor: int, ttl_ms: int, pause_after_read_ms: int,
               pause_after_check_ms: int) -> "PayoutWorker.Result":
        """pause_after_read_ms stalls before the client-side lease check; pause_after_check_ms stalls between the check
        and the write. The second pause is the one a client-side check cannot defend against."""
        lease = self._lock.try_acquire(self.name, ttl_ms)
        if lease is None:
            return PayoutWorker.Result.LOCK_BUSY
        balance = self._storage.read(account)
        self._clock.advance(pause_after_read_ms)                   # the pause nobody planned for
        if not lease.is_safe_to_use(self._clock, 0):
            return PayoutWorker.Result.FENCED_OFF                  # client-side check: necessary, not sufficient
        self._clock.advance(pause_after_check_ms)                  # the check is already stale when the write lands
        try:
            self._storage.write(account, balance - amount_minor, lease.token)
        except StaleTokenException:
            return PayoutWorker.Result.FENCED_OFF
        try:
            self._lock.release(lease)
        except LeaseExpiredException:
            pass  # the lease is already gone; there is nothing to release. The write above is what mattered.
        return PayoutWorker.Result.APPLIED


@dataclass(frozen=True)
class RaceReport:
    """Java's RaceStudy.Report."""

    runs: int
    lost_updates: int
    fenced_off: int
    both_applied: int


class RaceStudy:
    """Replays the two-worker race many times with random pause lengths and counts lost updates."""

    Report = RaceReport

    @staticmethod
    def run(seed: int, runs: int, fenced: bool) -> RaceReport:
        rng = JavaRandom(seed)
        lost = fenced_off = both = 0
        applied, fenced_result = PayoutWorker.Result.APPLIED, PayoutWorker.Result.FENCED_OFF
        for _ in range(runs):
            clock = FakeClock(0)
            lock = LeaseLock(clock, "payout:acct-7")
            storage: Storage = FencedStorage() if fenced else UnfencedStorage()
            storage.write("acct-7", 100_000, 0)
            ttl = 1_000
            pause_a = rng.next_int(2_000)             # sometimes shorter than the ttl, sometimes longer
            b = PayoutWorker("B", clock, lock, storage)

            # A acquires, reads, then stalls; B tries while A is stalled; then A's write lands.
            lease_a = lock.try_acquire("A", ttl)
            balance_a = storage.read("acct-7")
            clock.advance(pause_a)
            rb = b.payout("acct-7", 30_000, ttl, 0, 0)
            try:
                storage.write("acct-7", balance_a - 20_000, lease_a.token)
                ra = applied
            except StaleTokenException:
                ra = fenced_result
            expected = 100_000 - (20_000 if ra is applied else 0) - (30_000 if rb is applied else 0)
            if storage.read("acct-7") != expected:
                lost += 1
            if ra is fenced_result:
                fenced_off += 1
            if ra is applied and rb is applied:
                both += 1
        return RaceReport(runs, lost, fenced_off, both)


def java_list(items: list[str]) -> str:
    """List.toString(): [a, b] without quotes."""
    return "[" + ", ".join(items) + "]"


def jbool(b: bool) -> str:
    return "true" if b else "false"


def scenario(title: str, storage: Storage) -> None:
    print(f"== {title} ==")
    clock = FakeClock(0)
    lock = LeaseLock(clock, "payout:acct-7")
    storage.write("acct-7", 100_000, 0)
    print(f"acct-7 starts at {storage.read('acct-7')} minor units (1000.00 USD)")

    # Worker A takes the lock with a 1 s lease and reads the balance.
    a = or_else_throw(lock.try_acquire("A", 1_000))
    balance_seen_by_a = storage.read("acct-7")
    print(f"t={clock.now_ms()} A holds token {a.token}, reads {balance_seen_by_a}, plans payout of 20000")

    # A stalls for 1.5 s: a stop-the-world GC. The lease expires at t=1000 while A is frozen.
    clock.advance(1_500)
    print(f"t={clock.now_ms()} A has been paused 1500 ms; lease expired at t={a.expires_at_ms}")

    # Worker B acquires the now-free lock, applies its own payout of 30000, releases.
    rb = PayoutWorker("B", clock, lock, storage).payout("acct-7", 30_000, 1_000, 0, 0)
    print(f"t={clock.now_ms()} B: {rb.name}, balance now {storage.read('acct-7')} "
          f"(B held token {lock.highest_token_issued()})")

    # A wakes up and writes what it computed before the pause, still believing it holds the lock.
    try:
        storage.write("acct-7", balance_seen_by_a - 20_000, a.token)
        print(f"t={clock.now_ms()} A writes {balance_seen_by_a - 20_000} with token {a.token}: ACCEPTED")
    except StaleTokenException as e:
        print(f"t={clock.now_ms()} A writes with token {a.token}: {e}")
    final_balance = storage.read("acct-7")
    correct = 100_000 - 20_000 - 30_000
    print(f"final balance {final_balance}; both payouts applied would be {correct}; "
          f"only B applied would be {100_000 - 30_000}")
    if final_balance != correct and final_balance != 100_000 - 30_000:
        print("LOST UPDATE: B's payout of 30000 vanished from the balance but the money left the building.")
    elif final_balance == 100_000 - 30_000:
        print("A's stale write was fenced off. A must re-acquire, re-read, and recompute.")
    print(f"storage write log: {java_list(storage.write_log())}")
    print()


def main() -> None:
    print("== Leases expire on the service's clock; tokens never go backwards ==")
    clock = FakeClock(0)
    lock = LeaseLock(clock, "demo")
    l1 = or_else_throw(lock.try_acquire("A", 500))
    print(f"A acquired token {l1.token}, expires t={l1.expires_at_ms}")
    clock.advance(300)
    print(f"t=300 B tryAcquire -> {'granted' if lock.try_acquire('B', 500) is not None else 'denied'}")
    l1r = lock.renew(l1, 500)
    print(f"t=300 A renews -> same token {l1r.token}, expires t={l1r.expires_at_ms}")
    clock.advance(900)
    l2 = or_else_throw(lock.try_acquire("B", 500))
    print(f"t=1200 B tryAcquire -> granted token {l2.token} (A's lease expired at t=800)")
    try:
        lock.renew(l1r, 500)
    except LeaseExpiredException as e:
        print(f"t=1200 A renews -> {e}")
    print(f"safe-to-use with 100 ms margin at t=1200 for B: {jbool(l2.is_safe_to_use(clock, 100))}"
          f"; at t=1650: {jbool(l2.is_safe_to_use(FakeClock(1_650), 100))}")
    print()

    scenario("The bug: lock with a timeout, storage that trusts the lock", UnfencedStorage())
    scenario("The fix: the same lock, storage that checks the fencing token", FencedStorage())

    print("== 2000 randomized races, seed 7 ==")
    plain = RaceStudy.run(7, 2_000, False)
    fenced = RaceStudy.run(7, 2_000, True)
    print(f"unfenced: lost updates={plain.lost_updates} fencedOff={plain.fenced_off} bothApplied={plain.both_applied}")
    print(f"fenced  : lost updates={fenced.lost_updates} fencedOff={fenced.fenced_off} bothApplied={fenced.both_applied}")
    print("every run where A's pause outlived its lease is a lost update without fencing and a rejected write with it.")


if __name__ == "__main__":
    main()
