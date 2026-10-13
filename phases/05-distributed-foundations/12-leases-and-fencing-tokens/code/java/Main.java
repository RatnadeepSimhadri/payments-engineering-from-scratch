// Lesson: phases/05-distributed-foundations/12-leases-and-fencing-tokens/docs/en.md
// Leases, Fencing Tokens, and Distributed Locks
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: Gray & Cheriton, "Leases" (SOSP 1989); Burrows, "The Chubby lock service" (OSDI 2006);
//          Kleppmann, "How to do distributed locking" (2016); Redis "Distributed Locks with Redis" (Redlock) page.
// Java 21, standard library only. Time is injected through Clock; process pauses are clock.advance(), never sleep.

import java.util.ArrayList;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.Optional;
import java.util.Random;

/** Source of time. The lock service and every client read the same simulated clock in this lesson. */
interface Clock {
    long nowMs();
}

final class FakeClock implements Clock {
    private long now;

    FakeClock(long startMs) { this.now = startMs; }

    @Override public long nowMs() { return now; }

    /** A pause: GC stop-the-world, a VM migration, a swapped-out page, or a long network stall. */
    void advance(long ms) {
        if (ms < 0) throw new IllegalArgumentException("time only moves forward: " + ms);
        now += ms;
    }
}

/** Thrown when a client tries to renew or release a lease it no longer holds. */
final class LeaseExpiredException extends RuntimeException {
    LeaseExpiredException(String message) { super(message); }
}

/** Thrown by fenced storage when a writer presents a token older than one it has already accepted. */
final class StaleTokenException extends RuntimeException {
    final long presented;
    final long highestSeen;

    StaleTokenException(String key, long presented, long highestSeen) {
        super("stale fencing token " + presented + " for " + key + "; storage has already seen " + highestSeen);
        this.presented = presented;
        this.highestSeen = highestSeen;
    }
}

/**
 * What a client holds after acquiring the lock: who, until when, and a token that is strictly larger than
 * every token the lock has ever issued before. The token is the only part a remote resource can trust.
 */
record Lease(String resource, String owner, long token, long grantedAtMs, long expiresAtMs) {

    /** Expiry according to the lock service's clock. */
    boolean isExpired(Clock clock) { return clock.nowMs() >= expiresAtMs; }

    /**
     * The client-side question: is it still safe to act? Chubby-style, a client should stop trusting a lease
     * a safety margin before its nominal expiry, because its own clock may run slow relative to the service's.
     */
    boolean isSafeToUse(Clock clock, long marginMs) { return clock.nowMs() + marginMs < expiresAtMs; }

    long ttlMs() { return expiresAtMs - grantedAtMs; }
}

/**
 * A lease-based lock on one named resource. The holder gets exclusive access until the lease expires or is
 * released. The lock service is the only place tokens are minted, and it mints them from a counter that
 * never goes backwards.
 */
final class LeaseLock {
    private final Clock clock;
    private final String resource;
    private long nextToken = 1;
    private Lease current;                     // null when free
    private final List<String> history = new ArrayList<>();

    LeaseLock(Clock clock, String resource) {
        this.clock = clock;
        this.resource = resource;
    }

    /** Grant the lock if it is free or the previous lease has expired. Expiry is decided here, by this clock. */
    Optional<Lease> tryAcquire(String owner, long ttlMs) {
        if (ttlMs <= 0) throw new IllegalArgumentException("ttl must be positive: " + ttlMs);
        if (current != null && !current.isExpired(clock)) {
            history.add("t=" + clock.nowMs() + " " + owner + " denied (held by " + current.owner() + " token " + current.token() + ")");
            return Optional.empty();
        }
        if (current != null) {
            history.add("t=" + clock.nowMs() + " lease of " + current.owner() + " (token " + current.token() + ") expired at t=" + current.expiresAtMs());
        }
        current = new Lease(resource, owner, nextToken++, clock.nowMs(), clock.nowMs() + ttlMs);
        history.add("t=" + clock.nowMs() + " " + owner + " acquired token " + current.token() + " until t=" + current.expiresAtMs());
        return Optional.of(current);
    }

    /** Extend a lease the caller still holds. Renewing an expired or superseded lease is an error, not a re-acquire. */
    Lease renew(Lease lease, long ttlMs) {
        requireHeld(lease, "renew");
        current = new Lease(resource, lease.owner(), lease.token(), lease.grantedAtMs(), clock.nowMs() + ttlMs);
        history.add("t=" + clock.nowMs() + " " + lease.owner() + " renewed token " + lease.token() + " until t=" + current.expiresAtMs());
        return current;
    }

    void release(Lease lease) {
        requireHeld(lease, "release");
        history.add("t=" + clock.nowMs() + " " + lease.owner() + " released token " + lease.token());
        current = null;
    }

    private void requireHeld(Lease lease, String action) {
        if (current == null || current.token() != lease.token() || current.isExpired(clock)) {
            throw new LeaseExpiredException(lease.owner() + " cannot " + action + ": token " + lease.token()
                    + " is no longer the live lease at t=" + clock.nowMs());
        }
    }

    Optional<Lease> holder() {
        return current == null || current.isExpired(clock) ? Optional.empty() : Optional.of(current);
    }

    long highestTokenIssued() { return nextToken - 1; }

    List<String> history() { return List.copyOf(history); }
}

/** The protected resource: a key-value store that writers reach after taking the lock. */
interface Storage {
    long read(String key);
    void write(String key, long value, long token);
    List<String> writeLog();
}

/** Trusts every writer. The lock is the only protection, and the lock cannot see a paused client. */
final class UnfencedStorage implements Storage {
    private final Map<String, Long> data = new HashMap<>();
    private final List<String> log = new ArrayList<>();

    @Override public long read(String key) { return data.getOrDefault(key, 0L); }

    @Override public void write(String key, long value, long token) {
        data.put(key, value);
        log.add(key + "=" + value + " (token " + token + ")");
    }

    @Override public List<String> writeLog() { return List.copyOf(log); }
}

/**
 * Checks the fencing token on every write and rejects anything older than the newest token it has seen for
 * that key. The check and the write are one atomic step at the resource, which is the property the lock
 * alone cannot provide.
 */
final class FencedStorage implements Storage {
    private final Map<String, Long> data = new HashMap<>();
    private final Map<String, Long> highestToken = new HashMap<>();
    private final List<String> log = new ArrayList<>();

    @Override public long read(String key) { return data.getOrDefault(key, 0L); }

    @Override public void write(String key, long value, long token) {
        long seen = highestToken.getOrDefault(key, 0L);
        if (token < seen) {
            log.add("REJECTED " + key + "=" + value + " (token " + token + " < " + seen + ")");
            throw new StaleTokenException(key, token, seen);
        }
        highestToken.put(key, token);
        data.put(key, value);
        log.add(key + "=" + value + " (token " + token + ")");
    }

    long highestTokenSeen(String key) { return highestToken.getOrDefault(key, 0L); }

    @Override public List<String> writeLog() { return List.copyOf(log); }
}

/**
 * A worker that applies a payout: lock, read the pending balance, subtract the payout, write it back.
 * pauseAfterReadMs models a stop-the-world pause between the read and the write, the moment where a lease can
 * expire under a client that still believes it holds the lock.
 */
final class PayoutWorker {
    final String name;
    private final FakeClock clock;
    private final LeaseLock lock;
    private final Storage storage;

    enum Result { APPLIED, LOCK_BUSY, FENCED_OFF }

    PayoutWorker(String name, FakeClock clock, LeaseLock lock, Storage storage) {
        this.name = name;
        this.clock = clock;
        this.lock = lock;
        this.storage = storage;
    }

    /**
     * pauseAfterReadMs stalls between the read and the client-side lease check; pauseAfterCheckMs stalls between
     * the check and the write. The second pause is the one a client-side check cannot defend against.
     */
    Result payout(String account, long amountMinor, long ttlMs, long pauseAfterReadMs, long pauseAfterCheckMs) {
        Optional<Lease> granted = lock.tryAcquire(name, ttlMs);
        if (granted.isEmpty()) return Result.LOCK_BUSY;
        Lease lease = granted.get();
        long balance = storage.read(account);
        clock.advance(pauseAfterReadMs);                               // the pause nobody planned for
        if (!lease.isSafeToUse(clock, 0)) {
            return Result.FENCED_OFF;                                  // client-side check: necessary, not sufficient
        }
        clock.advance(pauseAfterCheckMs);                              // the check is already stale by the time the write lands
        try {
            storage.write(account, balance - amountMinor, lease.token());
        } catch (StaleTokenException e) {
            return Result.FENCED_OFF;
        }
        try {
            lock.release(lease);
        } catch (LeaseExpiredException ignored) {
            // The lease is already gone; there is nothing to release. The write above is what mattered.
        }
        return Result.APPLIED;
    }
}

/**
 * Replays the two-worker race many times with random pause lengths and counts lost updates. The invariant:
 * after both payouts, the balance equals start - a - b exactly when both were applied, and start - (the one
 * applied) when one was fenced off. Any other value is a lost update.
 */
final class RaceStudy {
    record Report(int runs, int lostUpdates, int fencedOff, int bothApplied) {}

    static Report run(long seed, int runs, boolean fenced) {
        Random rng = new Random(seed);
        int lost = 0, fencedOff = 0, both = 0;
        for (int i = 0; i < runs; i++) {
            FakeClock clock = new FakeClock(0);
            LeaseLock lock = new LeaseLock(clock, "payout:acct-7");
            Storage storage = fenced ? new FencedStorage() : new UnfencedStorage();
            storage.write("acct-7", 100_000, 0);
            long ttl = 1_000;
            long pauseA = rng.nextInt(2_000);                          // sometimes shorter than the ttl, sometimes longer
            PayoutWorker b = new PayoutWorker("B", clock, lock, storage);

            // A acquires, reads, then stalls. B arrives while A is stalled and retries until the lock frees up.
            // Modelled as: A's pause runs, then B tries, then A's write lands.
            Optional<Lease> leaseA = lock.tryAcquire("A", ttl);
            long balanceA = storage.read("acct-7");
            clock.advance(pauseA);
            PayoutWorker.Result rb = b.payout("acct-7", 30_000, ttl, 0, 0);
            PayoutWorker.Result ra;
            try {
                storage.write("acct-7", balanceA - 20_000, leaseA.get().token());
                ra = PayoutWorker.Result.APPLIED;
            } catch (StaleTokenException e) {
                ra = PayoutWorker.Result.FENCED_OFF;
            }
            long expected = 100_000
                    - (ra == PayoutWorker.Result.APPLIED ? 20_000 : 0)
                    - (rb == PayoutWorker.Result.APPLIED ? 30_000 : 0);
            if (storage.read("acct-7") != expected) lost++;
            if (ra == PayoutWorker.Result.FENCED_OFF) fencedOff++;
            if (ra == PayoutWorker.Result.APPLIED && rb == PayoutWorker.Result.APPLIED) both++;
        }
        return new Report(runs, lost, fencedOff, both);
    }
}

public final class Main {

    static void scenario(String title, Storage storage) {
        System.out.println("== " + title + " ==");
        FakeClock clock = new FakeClock(0);
        LeaseLock lock = new LeaseLock(clock, "payout:acct-7");
        storage.write("acct-7", 100_000, 0);
        System.out.println("acct-7 starts at " + storage.read("acct-7") + " minor units (1000.00 USD)");

        // Worker A takes the lock with a 1 s lease and reads the balance.
        Lease a = lock.tryAcquire("A", 1_000).orElseThrow();
        long balanceSeenByA = storage.read("acct-7");
        System.out.println("t=" + clock.nowMs() + " A holds token " + a.token() + ", reads " + balanceSeenByA + ", plans payout of 20000");

        // A stalls for 1.5 s: a stop-the-world GC. The lease expires at t=1000 while A is frozen.
        clock.advance(1_500);
        System.out.println("t=" + clock.nowMs() + " A has been paused 1500 ms; lease expired at t=" + a.expiresAtMs());

        // Worker B acquires the now-free lock, applies its own payout of 30000, releases.
        PayoutWorker b = new PayoutWorker("B", clock, lock, storage);
        PayoutWorker.Result rb = b.payout("acct-7", 30_000, 1_000, 0, 0);
        System.out.println("t=" + clock.nowMs() + " B: " + rb + ", balance now " + storage.read("acct-7")
                + " (B held token " + lock.highestTokenIssued() + ")");

        // A wakes up and writes what it computed before the pause, still believing it holds the lock.
        try {
            storage.write("acct-7", balanceSeenByA - 20_000, a.token());
            System.out.println("t=" + clock.nowMs() + " A writes " + (balanceSeenByA - 20_000) + " with token " + a.token() + ": ACCEPTED");
        } catch (StaleTokenException e) {
            System.out.println("t=" + clock.nowMs() + " A writes with token " + a.token() + ": " + e.getMessage());
        }
        long finalBalance = storage.read("acct-7");
        long correct = 100_000 - 20_000 - 30_000;
        System.out.println("final balance " + finalBalance + "; both payouts applied would be " + correct
                + "; only B applied would be " + (100_000 - 30_000));
        if (finalBalance != correct && finalBalance != 100_000 - 30_000) {
            System.out.println("LOST UPDATE: B's payout of 30000 vanished from the balance but the money left the building.");
        } else if (finalBalance == 100_000 - 30_000) {
            System.out.println("A's stale write was fenced off. A must re-acquire, re-read, and recompute.");
        }
        System.out.println("storage write log: " + storage.writeLog());
        System.out.println();
    }

    public static void main(String[] args) {
        System.out.println("== Leases expire on the service's clock; tokens never go backwards ==");
        FakeClock clock = new FakeClock(0);
        LeaseLock lock = new LeaseLock(clock, "demo");
        Lease l1 = lock.tryAcquire("A", 500).orElseThrow();
        System.out.println("A acquired token " + l1.token() + ", expires t=" + l1.expiresAtMs());
        clock.advance(300);
        System.out.println("t=300 B tryAcquire -> " + (lock.tryAcquire("B", 500).isPresent() ? "granted" : "denied"));
        Lease l1r = lock.renew(l1, 500);
        System.out.println("t=300 A renews -> same token " + l1r.token() + ", expires t=" + l1r.expiresAtMs());
        clock.advance(900);
        Lease l2 = lock.tryAcquire("B", 500).orElseThrow();
        System.out.println("t=1200 B tryAcquire -> granted token " + l2.token() + " (A's lease expired at t=800)");
        try {
            lock.renew(l1r, 500);
        } catch (LeaseExpiredException e) {
            System.out.println("t=1200 A renews -> " + e.getMessage());
        }
        System.out.println("safe-to-use with 100 ms margin at t=1200 for B: " + l2.isSafeToUse(clock, 100)
                + "; at t=1650: " + l2.isSafeToUse(new FakeClock(1_650), 100));
        System.out.println();

        scenario("The bug: lock with a timeout, storage that trusts the lock", new UnfencedStorage());
        scenario("The fix: the same lock, storage that checks the fencing token", new FencedStorage());

        System.out.println("== 2000 randomized races, seed 7 ==");
        RaceStudy.Report plain = RaceStudy.run(7, 2_000, false);
        RaceStudy.Report fenced = RaceStudy.run(7, 2_000, true);
        System.out.println("unfenced: lost updates=" + plain.lostUpdates() + " fencedOff=" + plain.fencedOff() + " bothApplied=" + plain.bothApplied());
        System.out.println("fenced  : lost updates=" + fenced.lostUpdates() + " fencedOff=" + fenced.fencedOff() + " bothApplied=" + fenced.bothApplied());
        System.out.println("every run where A's pause outlived its lease is a lost update without fencing and a rejected write with it.");
    }
}
