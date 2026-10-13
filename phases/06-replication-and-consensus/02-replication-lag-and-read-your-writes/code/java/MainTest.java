// Tests for phases/06-replication-and-consensus/02-replication-lag-and-read-your-writes. Run: javac -d .out *.java && java -cp .out MainTest
import java.util.ArrayList;
import java.util.List;

public final class MainTest {
    private static int passed = 0;
    private static final List<String> failures = new ArrayList<>();

    static void check(String name, boolean condition) {
        if (condition) passed++; else failures.add(name);
    }

    static void checkThrows(String name, Class<? extends Throwable> type, Runnable action) {
        try {
            action.run();
            failures.add(name + " (no exception)");
        } catch (Throwable t) {
            if (type.isInstance(t)) passed++; else failures.add(name + " (wrong exception " + t.getClass().getSimpleName() + ")");
        }
    }

    public static void main(String[] args) {
        // --- the leader's log and LSNs
        Leader leader = new Leader();
        check("fresh leader has lsn 0", leader.lsn() == 0);
        check("first write is lsn 1", leader.write("a", "1") == 1);
        check("second write is lsn 2", leader.write("b", "2") == 2);
        check("leader reads its own write", "2".equals(leader.read("b")));
        check("since(1) returns exactly the records after lsn 1", leader.since(1).size() == 1 && leader.since(1).get(0).lsn() == 2);
        check("since(lsn) returns nothing when caught up", leader.since(2).isEmpty());
        checkThrows("since rejects an lsn beyond the log", IllegalArgumentException.class, () -> leader.since(3));

        // --- replicas apply with delay and expose their watermark
        ReplicatedStore store = ReplicatedStore.of(1, true, 3, 10);
        long lsn = store.write("payment:p1", "CAPTURED");
        check("write returns lsn 1", lsn == 1);
        check("replica has not applied before any tick", store.replicas.get(0).appliedLsn == 0 && store.replicas.get(0).pending() == 1);
        store.run(10);
        check("every replica applies within its max delay", store.replicas.get(0).appliedLsn == 1 && store.replicas.get(1).appliedLsn == 1);
        check("lag is zero once applied", store.replicas.get(1).lagBehind(store.leader) == 0);
        check("replica serves the value after applying", "CAPTURED".equals(store.replicas.get(1).read("payment:p1")));

        // --- the bug: a read right after a write from a lagging replica returns nothing
        ReplicatedStore lagging = ReplicatedStore.of(2, true, 20);
        Session alice = new Session(1);
        lagging.write(alice, "payment:p2", "CAPTURED");
        lagging.tick();
        ReadResult stale = lagging.read(alice, "payment:p2", ReadMode.ANY_REPLICA);
        check("ANY_REPLICA right after a write misses the write (lag > time since write)", stale.value() == null && stale.atLsn() == 0);
        check("the store counted that read as stale", lagging.staleReads == 1);

        // --- read-your-writes: the floor is the session's last write lsn; the leader is the fallback
        ReadResult ryw = lagging.read(alice, "payment:p2", ReadMode.READ_YOUR_WRITES);
        check("READ_YOUR_WRITES sees the own write", "CAPTURED".equals(ryw.value()));
        check("...served by the leader when no replica has caught up", "leader".equals(ryw.servedBy()) && ryw.redirected());
        check("session floor for READ_YOUR_WRITES is the last write lsn", alice.floor(ReadMode.READ_YOUR_WRITES) == 1);
        Session bob = new Session(2);
        ReadResult bobRead = lagging.read(bob, "payment:p2", ReadMode.READ_YOUR_WRITES);
        check("a session with no writes has floor 0 and may read stale (not its write to see)", bob.floor(ReadMode.READ_YOUR_WRITES) == 0 && bobRead.value() == null);
        lagging.run(25);
        ReadResult later = lagging.read(alice, "payment:p2", ReadMode.READ_YOUR_WRITES);
        check("once a replica catches up, READ_YOUR_WRITES is served by a replica, not the leader", "r0".equals(later.servedBy()) && !later.redirected());

        // --- the guarantee holds across seeds: no session ever misses its own write; ANY_REPLICA does
        int rywStale = 0, anyStale = 0, sessionBackwards = 0, anyBackwards = 0;
        for (long seed = 1; seed <= 15; seed++) {
            for (ReadMode mode : new ReadMode[] {ReadMode.ANY_REPLICA, ReadMode.READ_YOUR_WRITES, ReadMode.SESSION}) {
                ReplicatedStore s = ReplicatedStore.of(seed, true, 5, 15, 45);
                Session user = new Session(seed);
                AnomalyDetector det = new AnomalyDetector();
                for (int i = 1; i <= 100; i++) {
                    s.write(user, "k", "v" + i);
                    s.run(1 + (i % 5));
                    ReadResult r = s.read(user, "k", mode);
                    det.observe(r);
                    boolean missed = !("v" + i).equals(r.value());
                    if (mode == ReadMode.ANY_REPLICA && missed) anyStale++;
                    if (mode == ReadMode.READ_YOUR_WRITES && missed) rywStale++;
                }
                if (mode == ReadMode.ANY_REPLICA) anyBackwards += det.monotonicViolations;
                if (mode == ReadMode.SESSION) sessionBackwards += det.monotonicViolations;
            }
        }
        check("ANY_REPLICA misses own writes in the seeded runs (the bug is reproducible)", anyStale > 0);
        check("READ_YOUR_WRITES never misses an own write across 15 seeds x 100 writes", rywStale == 0);
        check("ANY_REPLICA reads travel back in time in the seeded runs", anyBackwards > 0);
        check("SESSION reads never go backwards across 15 seeds", sessionBackwards == 0);

        // --- monotonic reads: a sticky, floored read never returns an older lsn than a previous read
        ReplicatedStore mono = ReplicatedStore.of(5, true, 2, 30);
        Session carol = new Session(1);   // sticky replica r1 is the slow one
        mono.write(carol, "k", "v1");
        mono.run(3);
        ReadResult first = mono.read(carol, "k", ReadMode.MONOTONIC);
        mono.write("k", "v2");            // not carol's write: her write floor stays at 1
        mono.run(3);
        ReadResult second = mono.read(carol, "k", ReadMode.MONOTONIC);
        check("MONOTONIC floor is the last read lsn", carol.floor(ReadMode.MONOTONIC) >= first.atLsn());
        check("MONOTONIC never serves an lsn below the previous read", second.atLsn() >= first.atLsn());
        check("SESSION floor is the max of write and read floors", carol.floor(ReadMode.SESSION) == Math.max(carol.lastWriteLsn, carol.lastReadLsn));

        // --- consistent prefix: in-order application is a prefix of the leader's history; on-arrival is not
        ReplicatedStore ordered = ReplicatedStore.of(11, true, 20);
        ReplicatedStore unordered = ReplicatedStore.of(11, false, 20);
        AnomalyDetector orderedDet = new AnomalyDetector(), unorderedDet = new AnomalyDetector();
        for (int p = 1; p <= 100; p++) {
            for (ReplicatedStore s : new ReplicatedStore[] {ordered, unordered}) {
                s.write("p" + p + ":auth", "AUTHORIZED");
                s.write("p" + p + ":capture", "CAPTURED");
            }
            for (int t = 0; t < 8; t++) {
                ordered.tick(); unordered.tick();
                orderedDet.observePair(ordered.replicas.get(0).read("p" + p + ":auth"), ordered.replicas.get(0).read("p" + p + ":capture"));
                unorderedDet.observePair(unordered.replicas.get(0).read("p" + p + ":auth"), unordered.replicas.get(0).read("p" + p + ":capture"));
            }
        }
        check("on-arrival replica shows 'captured' before 'authorized' (consistent prefix violated)", unorderedDet.prefixViolations > 0 && unordered.replicas.get(0).outOfOrderApplies > 0);
        check("in-order replica never shows 'captured' before 'authorized'", orderedDet.prefixViolations == 0 && ordered.replicas.get(0).outOfOrderApplies == 0);
        ordered.run(40); unordered.run(40);
        check("both replicas converge to the same watermark eventually", ordered.replicas.get(0).appliedLsn == 200 && unordered.replicas.get(0).appliedLsn == 200);
        check("in-order replica's buffer drains to zero", ordered.replicas.get(0).buffered() == 0);

        // --- the watermark is the catch-up cursor
        ReplicatedStore cu = ReplicatedStore.of(4, true, 2);
        for (int i = 1; i <= 5; i++) cu.write("k" + i, "v");
        cu.run(5);
        check("catch-up ships nothing to a replica that is current", cu.catchUp(cu.replicas.get(0)) == 0);

        // --- determinism: same seed, same trace
        ReplicatedStore d1 = ReplicatedStore.of(21, true, 5, 15), d2 = ReplicatedStore.of(21, true, 5, 15);
        StringBuilder t1 = new StringBuilder(), t2 = new StringBuilder();
        for (int i = 0; i < 30; i++) {
            d1.write("k", "v" + i); d2.write("k", "v" + i);
            d1.run(2); d2.run(2);
            t1.append(d1.read(new Session(i), "k", ReadMode.ANY_REPLICA)); t2.append(d2.read(new Session(i), "k", ReadMode.ANY_REPLICA));
        }
        check("same seed produces the same sequence of reads", t1.toString().equals(t2.toString()));

        // --- money is never a float
        check("Money formats minor units with the currency exponent", Money.ofMinor(4999, Currency.USD).toString().equals("49.99 USD"));
        checkThrows("Money rejects mixing currencies", IllegalArgumentException.class, () -> Money.ofMinor(1, Currency.USD).plus(Money.ofMinor(1, Currency.EUR)));
        checkThrows("replica rejects a zero delay", IllegalArgumentException.class, () -> new Replica(0, new Rng(1), 0, 5, true));

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }
}
