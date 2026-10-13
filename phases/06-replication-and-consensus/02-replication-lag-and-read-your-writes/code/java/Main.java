// Lesson: phases/06-replication-and-consensus/02-replication-lag-and-read-your-writes/docs/en.md
// Replication Lag and Read-Your-Writes
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: Terry et al., "Session Guarantees for Weakly Consistent Replicated Data" (PDIS 1994); PostgreSQL docs,
//          "Log-Shipping Standby Servers" and pg_current_wal_lsn(); Vogels, "Eventually Consistent" (CACM 2009).
// Java 21, standard library only. Deterministic: the only randomness is a seeded SplitMix64, time is a tick counter.

import java.util.ArrayList;
import java.util.Comparator;
import java.util.HashSet;
import java.util.List;
import java.util.Map;
import java.util.PriorityQueue;
import java.util.Set;
import java.util.TreeMap;

/** SplitMix64 (Steele, Lea, Flood, OOPSLA 2014). Same stream for the same seed in every language, so a bug replays. */
final class Rng {
    private long state;

    Rng(long seed) { this.state = seed; }

    long nextLong() {
        state += 0x9E3779B97F4A7C15L;
        long z = state;
        z = (z ^ (z >>> 30)) * 0xBF58476D1CE4E5B9L;
        z = (z ^ (z >>> 27)) * 0x94D049BB133111EBL;
        return z ^ (z >>> 31);
    }

    int nextInt(int bound) { return (int) ((nextLong() >>> 33) % bound); }
}

/** ISO 4217 currency, as in Phase 10 Lesson 01. Only what this lesson needs: a code and an exponent for formatting. */
record Currency(String code, int exponent) {
    static final Currency USD = new Currency("USD", 2);
    static final Currency EUR = new Currency("EUR", 2);

    Currency {
        if (code == null || code.length() != 3) throw new IllegalArgumentException("currency code must be three letters");
        if (exponent < 0 || exponent > 4) throw new IllegalArgumentException("exponent must be 0..4");
    }
}

/** Integer minor units plus a currency; never a float. Copied from Phase 10 Lesson 01, trimmed to what the demo uses. */
record Money(long minorUnits, Currency currency) {
    static Money ofMinor(long minorUnits, Currency currency) { return new Money(minorUnits, currency); }

    Money plus(Money other) {
        if (!currency.equals(other.currency)) throw new IllegalArgumentException("cannot combine " + currency.code() + " with " + other.currency.code());
        return new Money(Math.addExact(minorUnits, other.minorUnits), currency);
    }

    @Override public String toString() {
        long scale = 1;
        for (int i = 0; i < currency.exponent(); i++) scale *= 10;
        long whole = Math.abs(minorUnits) / scale, frac = Math.abs(minorUnits) % scale;
        String sign = minorUnits < 0 ? "-" : "";
        if (currency.exponent() == 0) return sign + whole + " " + currency.code();
        return sign + whole + "." + String.format("%0" + currency.exponent() + "d", frac) + " " + currency.code();
    }
}

/** One record in the leader's replication log. The LSN (log sequence number) is its 1-based position; it only grows. */
record LogRecord(long lsn, String key, String value) {
    @Override public String toString() { return "#" + lsn + " " + key + "=" + value; }
}

/** The single writer. Every write appends one record and returns its LSN, which is the token a client carries afterwards. */
final class Leader {
    final List<LogRecord> log = new ArrayList<>();
    final Map<String, String> data = new TreeMap<>();

    long lsn() { return log.size(); }

    long write(String key, String value) {
        LogRecord r = new LogRecord(log.size() + 1, key, value);
        log.add(r);
        data.put(key, value);
        return r.lsn();
    }

    String read(String key) { return data.get(key); }

    /** Records with LSN strictly greater than `afterLsn`: what a replica that has applied through afterLsn still needs. */
    List<LogRecord> since(long afterLsn) {
        if (afterLsn < 0 || afterLsn > log.size()) throw new IllegalArgumentException("lsn out of range: " + afterLsn);
        return List.copyOf(log.subList((int) afterLsn, log.size()));
    }
}

/**
 * An asynchronous replica. Records arrive after a seeded delay in [minDelay, maxDelay] ticks. With applyInOrder the
 * replica applies only the contiguous prefix of the log (so its state is always some prefix of the leader's history);
 * without it the replica applies each record the moment it arrives, which is faster and violates consistent prefix.
 */
final class Replica {
    record Pending(int arriveAt, long lsn, LogRecord record) {}

    final int id;
    final String name;
    private final Rng rng;
    final int minDelay;
    final int maxDelay;
    final boolean applyInOrder;
    final Map<String, String> data = new TreeMap<>();
    private final PriorityQueue<Pending> inbox = new PriorityQueue<>(Comparator.comparingInt(Pending::arriveAt).thenComparingLong(Pending::lsn));
    private final TreeMap<Long, LogRecord> arrived = new TreeMap<>();
    private final Set<Long> appliedSet = new HashSet<>();
    /** Highest LSN such that every record 1..appliedLsn has been applied. This is the number a session token is compared with. */
    long appliedLsn = 0;
    int outOfOrderApplies = 0;
    int applied = 0;

    Replica(int id, Rng rng, int minDelay, int maxDelay, boolean applyInOrder) {
        if (minDelay < 1 || maxDelay < minDelay) throw new IllegalArgumentException("delays must satisfy 1 <= min <= max");
        this.id = id;
        this.name = "r" + id;
        this.rng = rng;
        this.minDelay = minDelay;
        this.maxDelay = maxDelay;
        this.applyInOrder = applyInOrder;
    }

    /** The leader ships a record; it lands in the inbox with a seeded delay. Nothing is applied until tick(). */
    void receive(LogRecord r, int now) {
        int delay = minDelay + rng.nextInt(maxDelay - minDelay + 1);
        inbox.add(new Pending(now + delay, r.lsn(), r));
    }

    /**
     * Move everything that has arrived into the reorder buffer, then apply. In-order mode applies only while the next
     * expected LSN is present (a gap means a record is still in flight); on-arrival mode applies the whole buffer.
     */
    void tick(int now) {
        while (!inbox.isEmpty() && inbox.peek().arriveAt() <= now) {
            Pending p = inbox.poll();
            arrived.put(p.lsn(), p.record());
        }
        if (applyInOrder) {
            while (arrived.containsKey(appliedLsn + 1)) apply(arrived.remove(appliedLsn + 1));
        } else {
            while (!arrived.isEmpty()) apply(arrived.pollFirstEntry().getValue());
        }
    }

    private void apply(LogRecord r) {
        if (r.lsn() != appliedLsn + 1) outOfOrderApplies++;
        data.put(r.key(), r.value());
        appliedSet.add(r.lsn());
        applied++;
        while (appliedSet.contains(appliedLsn + 1)) appliedLsn++;
    }

    String read(String key) { return data.get(key); }

    /** Records shipped but not yet applied: still in flight, or arrived and waiting for a gap to fill. */
    int pending() { return inbox.size() + arrived.size(); }

    /** Size of the reorder buffer: records that arrived early and wait for an earlier LSN. Always 0 in on-arrival mode. */
    int buffered() { return arrived.size(); }

    long lagBehind(Leader leader) { return leader.lsn() - appliedLsn; }
}

/** Which freshness rule a read follows. All but LEADER are implemented by one method: a floor on the replica's LSN. */
enum ReadMode { ANY_REPLICA, READ_YOUR_WRITES, MONOTONIC, SESSION, LEADER }

/**
 * Per-client state that makes session guarantees possible. lastWriteLsn is the token the client got from its last write;
 * lastReadLsn is the freshest point it has observed. Both are plain longs: they cost nothing to carry in a cookie or header.
 */
final class Session {
    final long id;
    long lastWriteLsn = 0;
    long lastReadLsn = 0;

    Session(long id) { this.id = id; }

    long floor(ReadMode mode) {
        return switch (mode) {
            case ANY_REPLICA, LEADER -> 0;
            case READ_YOUR_WRITES -> lastWriteLsn;
            case MONOTONIC -> lastReadLsn;
            case SESSION -> Math.max(lastWriteLsn, lastReadLsn);
        };
    }
}

/** What a read returns, plus where it came from. The demo prints servedBy to make the routing decision visible. */
record ReadResult(String value, String servedBy, long atLsn, boolean redirected) {
    @Override public String toString() { return (value == null ? "<nothing>" : value) + "  (served by " + servedBy + " at lsn " + atLsn + (redirected ? ", redirected" : "") + ")"; }
}

/** Leader plus replicas plus a tick clock. The load balancer is a seeded random pick, like a round-robin DNS answer. */
final class ReplicatedStore {
    final Leader leader = new Leader();
    final List<Replica> replicas = new ArrayList<>();
    private final Rng balancer;
    private int now = 0;
    int reads = 0, staleReads = 0, redirects = 0, leaderReads = 0;

    ReplicatedStore(long seed, boolean applyInOrder, int[] minDelays, int[] maxDelays) {
        if (minDelays.length != maxDelays.length || minDelays.length == 0) throw new IllegalArgumentException("one delay range per replica");
        this.balancer = new Rng(seed);
        for (int i = 0; i < maxDelays.length; i++) {
            replicas.add(new Replica(i, new Rng(seed * 7919L + 1000003L * (i + 1)), minDelays[i], maxDelays[i], applyInOrder));
        }
    }

    static ReplicatedStore of(long seed, boolean applyInOrder, int... maxDelays) {
        int[] mins = new int[maxDelays.length];
        java.util.Arrays.fill(mins, 1);
        return new ReplicatedStore(seed, applyInOrder, mins, maxDelays);
    }

    int now() { return now; }

    void tick() { now++; for (Replica r : replicas) r.tick(now); }

    void run(int ticks) { for (int i = 0; i < ticks; i++) tick(); }

    /** Write through the leader and fan the record out. Returns the LSN; a client that keeps it can read its own write. */
    long write(String key, String value) {
        long lsn = leader.write(key, value);
        LogRecord r = leader.log.get((int) lsn - 1);
        for (Replica rep : replicas) rep.receive(r, now);
        return lsn;
    }

    /** Write on behalf of a session: the session remembers the LSN. */
    long write(Session s, String key, String value) {
        long lsn = write(key, value);
        s.lastWriteLsn = lsn;
        return lsn;
    }

    Replica pickReplica(ReadMode mode, Session s) {
        if (mode == ReadMode.MONOTONIC || mode == ReadMode.SESSION) return replicas.get((int) (s.id % replicas.size()));   // sticky
        return replicas.get(balancer.nextInt(replicas.size()));
    }

    /**
     * The whole mechanism: pick a replica, and if it has not applied through the floor the session demands, find one
     * that has, falling back to the leader. The floor is 0 for ANY_REPLICA, which is the bug, and a token otherwise.
     */
    ReadResult read(Session s, String key, ReadMode mode) {
        reads++;
        if (mode == ReadMode.LEADER) { leaderReads++; return finish(s, new ReadResult(leader.read(key), "leader", leader.lsn(), false)); }
        long floor = s.floor(mode);
        Replica chosen = pickReplica(mode, s);
        if (chosen.appliedLsn >= floor) return finish(s, new ReadResult(chosen.read(key), chosen.name, chosen.appliedLsn, false));
        for (Replica r : replicas) {
            if (r.appliedLsn >= floor) { redirects++; return finish(s, new ReadResult(r.read(key), r.name, r.appliedLsn, true)); }
        }
        redirects++;
        leaderReads++;
        return finish(s, new ReadResult(leader.read(key), "leader", leader.lsn(), true));
    }

    private ReadResult finish(Session s, ReadResult result) {
        if (result.atLsn() < leader.lsn()) staleReads++;
        s.lastReadLsn = Math.max(s.lastReadLsn, result.atLsn());
        return result;
    }

    /** Resynchronise a replica from its own watermark: the LSN is also the catch-up cursor. Records arrive with the usual delay. */
    int catchUp(Replica r) {
        List<LogRecord> missing = leader.since(r.appliedLsn);
        int shipped = 0;
        for (LogRecord rec : missing) { if (r.pending() == 0 || !r.applyInOrder) { r.receive(rec, now); shipped++; } }
        return shipped;
    }

    String lagReport() {
        StringBuilder sb = new StringBuilder("leader lsn=" + leader.lsn());
        for (Replica r : replicas) sb.append(", ").append(r.name).append(" applied=").append(r.appliedLsn).append(" (lag ").append(r.lagBehind(leader)).append(")");
        return sb.toString();
    }
}

/**
 * Watches a sequence of reads by one session and reports the two anomalies this lesson is about.
 * - a monotonic violation: a later read was served at a lower LSN than an earlier one (time went backwards)
 * - a consistent-prefix violation: the reader saw a later record's key but not an earlier one it depends on
 */
final class AnomalyDetector {
    int monotonicViolations = 0;
    int prefixViolations = 0;
    private long highWater = 0;

    void observe(ReadResult r) {
        if (r.atLsn() < highWater) monotonicViolations++;
        highWater = Math.max(highWater, r.atLsn());
    }

    /** `later` was written after `earlier`. Seeing `later` without `earlier` cannot happen on any prefix of the log. */
    void observePair(String earlier, String later) {
        if (later != null && earlier == null) prefixViolations++;
    }
}

public final class Main {
    static final Currency USD = Currency.USD;

    public static void main(String[] args) {
        System.out.println("== The bug: a customer pays, refreshes, and sees no payment (seed 3, replicas with max delay 4, 12, 40) ==");
        ReplicatedStore store = ReplicatedStore.of(3, true, 4, 12, 40);
        Session alice = new Session(1);
        Money amount = Money.ofMinor(4999, USD);
        long lsn = store.write(alice, "payment:p-1001", "CAPTURED " + amount);
        System.out.println("t=" + store.now() + "  POST /payments -> leader appends #" + lsn + ", returns 201 with lsn token " + lsn);
        store.run(2);
        System.out.println("t=" + store.now() + "  " + store.lagReport());
        for (int i = 0; i < 3; i++) {
            ReadResult r = store.read(alice, "payment:p-1001", ReadMode.ANY_REPLICA);
            System.out.println("t=" + store.now() + "  GET /payments/p-1001 (any replica)       -> " + r);
            store.tick();
        }

        System.out.println();
        System.out.println("== Read-your-writes: the session carries the LSN of its last write and reads only from replicas at or past it ==");
        for (int i = 0; i < 3; i++) {
            ReadResult r = store.read(alice, "payment:p-1001", ReadMode.READ_YOUR_WRITES);
            System.out.println("t=" + store.now() + "  GET /payments/p-1001 (floor lsn " + alice.floor(ReadMode.READ_YOUR_WRITES) + ") -> " + r);
            store.tick();
        }
        Session bob = new Session(2);
        ReadResult bobSees = store.read(bob, "payment:p-1001", ReadMode.READ_YOUR_WRITES);
        System.out.println("t=" + store.now() + "  bob (never wrote, floor 0) reads p-1001         -> " + bobSees + "   <- allowed: he has no write to see");

        System.out.println();
        System.out.println("== Monotonic reads: without a sticky replica, a refresh can travel back in time ==");
        ReplicatedStore tt = ReplicatedStore.of(5, true, 2, 30);
        Session carol = new Session(0);
        tt.write(carol, "payment:p-2002", "CAPTURED " + Money.ofMinor(1250, USD));
        AnomalyDetector naive = new AnomalyDetector();
        for (int i = 0; i < 6; i++) {
            tt.run(2);
            ReadResult r = tt.read(carol, "payment:p-2002", ReadMode.ANY_REPLICA);
            naive.observe(r);
            System.out.println("t=" + tt.now() + "  refresh (any replica) -> " + r);
        }
        System.out.println("time-travel anomalies seen by the naive reader: " + naive.monotonicViolations);
        ReplicatedStore mt = ReplicatedStore.of(5, true, 2, 30);
        Session dave = new Session(1);   // sticky to r1, the slow one, which makes the floor do the work
        mt.write(dave, "payment:p-2002", "CAPTURED " + Money.ofMinor(1250, USD));
        AnomalyDetector guarded = new AnomalyDetector();
        for (int i = 0; i < 6; i++) {
            mt.run(2);
            ReadResult r = mt.read(dave, "payment:p-2002", ReadMode.SESSION);
            guarded.observe(r);
            System.out.println("t=" + mt.now() + "  refresh (session floor " + dave.floor(ReadMode.SESSION) + ") -> " + r);
        }
        System.out.println("time-travel anomalies seen by the session reader: " + guarded.monotonicViolations);

        System.out.println();
        System.out.println("== Consistent prefix: 'captured' must never be visible without 'authorized' (200 payments, seed 11) ==");
        for (boolean inOrder : new boolean[] {false, true}) {
            ReplicatedStore cp = ReplicatedStore.of(11, inOrder, 20);
            AnomalyDetector det = new AnomalyDetector();
            Session auditor = new Session(9);
            for (int p = 1; p <= 200; p++) {
                cp.write("payment:p-" + p + ":auth", "AUTHORIZED");
                cp.write("payment:p-" + p + ":capture", "CAPTURED");
                for (int t = 0; t < 10; t++) {
                    cp.tick();
                    det.observePair(cp.replicas.get(0).read("payment:p-" + p + ":auth"), cp.replicas.get(0).read("payment:p-" + p + ":capture"));
                }
            }
            cp.run(50);
            System.out.printf("replica applies %-13s -> out-of-order applies=%d, 'captured before authorized' observed %d times, final appliedLsn=%d of %d%n",
                    inOrder ? "in LSN order" : "on arrival", cp.replicas.get(0).outOfOrderApplies, det.prefixViolations, cp.replicas.get(0).appliedLsn, cp.leader.lsn());
        }

        System.out.println();
        System.out.println("== 20 seeds x 300 write-then-read pairs, 3 replicas (max delay 5, 15, 45): stale-read rate by read mode ==");
        for (ReadMode mode : new ReadMode[] {ReadMode.ANY_REPLICA, ReadMode.READ_YOUR_WRITES, ReadMode.SESSION, ReadMode.LEADER}) {
            int stale = 0, total = 0, redirects = 0, leaderReads = 0, monotonic = 0;
            for (long seed = 1; seed <= 20; seed++) {
                ReplicatedStore s = ReplicatedStore.of(seed, true, 5, 15, 45);
                Session user = new Session(seed);
                AnomalyDetector det = new AnomalyDetector();
                for (int i = 1; i <= 300; i++) {
                    s.write(user, "k", "v" + i);
                    s.run(1 + (i % 7));
                    ReadResult r = s.read(user, "k", mode);
                    det.observe(r);
                    total++;
                    if (!("v" + i).equals(r.value())) stale++;
                }
                redirects += s.redirects;
                leaderReads += s.leaderReads;
                monotonic += det.monotonicViolations;
            }
            System.out.printf("%-17s stale own-write reads %4d/%d  redirects %4d  leader reads %4d  time-travel %d%n", mode, stale, total, redirects, leaderReads, monotonic);
        }
        System.out.println("the token costs one long per session; the leader took the hit only when no replica had caught up.");
    }
}
