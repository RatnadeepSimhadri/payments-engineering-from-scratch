// Tests for phases/05-distributed-foundations/12-leases-and-fencing-tokens. Run: javac -d .out *.java && java -cp .out MainTest
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
        // Lease mechanics
        FakeClock clock = new FakeClock(0);
        LeaseLock lock = new LeaseLock(clock, "r");
        Lease a = lock.tryAcquire("A", 1_000).orElseThrow();
        check("first lease gets token 1", a.token() == 1);
        check("lease expires at grant + ttl", a.expiresAtMs() == 1_000 && a.ttlMs() == 1_000);
        check("holder is A while live", lock.holder().map(Lease::owner).orElse("").equals("A"));
        check("second acquirer is denied while lease is live", lock.tryAcquire("B", 1_000).isEmpty());
        clock.advance(999);
        check("not expired one ms before expiry", !a.isExpired(clock));
        clock.advance(1);
        check("expired exactly at expiry (>=)", a.isExpired(clock));
        check("holder is empty once expired", lock.holder().isEmpty());
        Lease b = lock.tryAcquire("B", 1_000).orElseThrow();
        check("next lease gets a strictly larger token", b.token() == 2 && b.token() > a.token());
        checkThrows("renewing an expired lease fails", LeaseExpiredException.class, () -> lock.renew(a, 1_000));
        checkThrows("releasing a superseded lease fails", LeaseExpiredException.class, () -> lock.release(a));
        Lease bRenewed = lock.renew(b, 500);
        check("renew keeps the token and moves expiry", bRenewed.token() == 2 && bRenewed.expiresAtMs() == clock.nowMs() + 500);
        lock.release(bRenewed);
        check("released lock is free immediately", lock.tryAcquire("C", 10).map(Lease::token).orElse(-1L) == 3);
        checkThrows("ttl must be positive", IllegalArgumentException.class, () -> lock.tryAcquire("D", 0));
        check("tokens are monotonic across release and expiry", lock.highestTokenIssued() == 3);

        // Safety margin: stop trusting the lease before the service does
        FakeClock c2 = new FakeClock(0);
        Lease m = new LeaseLock(c2, "m").tryAcquire("A", 1_000).orElseThrow();
        c2.advance(850);
        check("safe to use with no margin at 850/1000", m.isSafeToUse(c2, 0));
        check("not safe with a 200 ms margin at 850/1000", !m.isSafeToUse(c2, 200));

        // Fenced storage rejects stale tokens, accepts equal and newer ones
        FencedStorage fs = new FencedStorage();
        fs.write("k", 1, 5);
        fs.write("k", 2, 5);
        check("same token may write again (holder's own retries)", fs.read("k") == 2);
        fs.write("k", 3, 7);
        check("newer token accepted and recorded", fs.read("k") == 3 && fs.highestTokenSeen("k") == 7);
        checkThrows("older token rejected", StaleTokenException.class, () -> fs.write("k", 99, 6));
        check("rejected write did not change the value", fs.read("k") == 3);
        fs.write("other", 1, 1);
        check("tokens are tracked per key", fs.highestTokenSeen("other") == 1 && fs.highestTokenSeen("k") == 7);

        // The bug: paused holder writes after expiry into unfenced storage
        FakeClock c3 = new FakeClock(0);
        LeaseLock l3 = new LeaseLock(c3, "payout");
        UnfencedStorage us = new UnfencedStorage();
        us.write("acct", 100_000, 0);
        Lease la = l3.tryAcquire("A", 1_000).orElseThrow();
        long seenByA = us.read("acct");
        c3.advance(1_500);
        PayoutWorker.Result rb = new PayoutWorker("B", c3, l3, us).payout("acct", 30_000, 1_000, 0, 0);
        us.write("acct", seenByA - 20_000, la.token());
        check("B applied its payout after A's lease expired", rb == PayoutWorker.Result.APPLIED);
        check("unfenced storage accepts A's stale write and loses B's update", us.read("acct") == 80_000);

        // The fix: same sequence against fenced storage
        FakeClock c4 = new FakeClock(0);
        LeaseLock l4 = new LeaseLock(c4, "payout");
        FencedStorage fs4 = new FencedStorage();
        fs4.write("acct", 100_000, 0);
        Lease la4 = l4.tryAcquire("A", 1_000).orElseThrow();
        long seenByA4 = fs4.read("acct");
        c4.advance(1_500);
        PayoutWorker.Result rb4 = new PayoutWorker("B", c4, l4, fs4).payout("acct", 30_000, 1_000, 0, 0);
        checkThrows("fenced storage rejects A's stale write", StaleTokenException.class,
                () -> fs4.write("acct", seenByA4 - 20_000, la4.token()));
        check("balance reflects only B's payout", rb4 == PayoutWorker.Result.APPLIED && fs4.read("acct") == 70_000);

        // A client-side lease check is necessary but not sufficient
        FakeClock c5 = new FakeClock(0);
        LeaseLock l5 = new LeaseLock(c5, "payout");
        UnfencedStorage us5 = new UnfencedStorage();
        us5.write("acct", 100_000, 0);
        PayoutWorker w5 = new PayoutWorker("A", c5, l5, us5);
        PayoutWorker.Result r5 = w5.payout("acct", 20_000, 1_000, 1_500, 0);
        check("pause before the check: client-side check catches it", r5 == PayoutWorker.Result.FENCED_OFF && us5.read("acct") == 100_000);
        FakeClock c6 = new FakeClock(0);
        LeaseLock l6 = new LeaseLock(c6, "payout");
        UnfencedStorage us6 = new UnfencedStorage();
        us6.write("acct", 100_000, 0);
        PayoutWorker.Result r6 = new PayoutWorker("A", c6, l6, us6).payout("acct", 20_000, 1_000, 0, 1_500);
        check("pause after the check: the stale write lands anyway on unfenced storage", r6 == PayoutWorker.Result.APPLIED && c6.nowMs() == 1_500);
        FakeClock c7 = new FakeClock(0);
        LeaseLock l7 = new LeaseLock(c7, "payout");
        FencedStorage fs7 = new FencedStorage();
        fs7.write("acct", 100_000, 0);
        Lease blocker = l7.tryAcquire("A", 1_000).orElseThrow();
        long seen7 = fs7.read("acct");
        c7.advance(1_500);
        new PayoutWorker("B", c7, l7, fs7).payout("acct", 30_000, 1_000, 0, 0);
        checkThrows("pause after the check: fenced storage still rejects", StaleTokenException.class,
                () -> fs7.write("acct", seen7 - 20_000, blocker.token()));

        // Randomized races
        RaceStudy.Report plain = RaceStudy.run(7, 2_000, false);
        RaceStudy.Report fenced = RaceStudy.run(7, 2_000, true);
        check("unfenced storage loses updates whenever the pause outlives the lease", plain.lostUpdates() > 0 && plain.lostUpdates() == plain.bothApplied());
        check("fenced storage never loses an update", fenced.lostUpdates() == 0);
        check("fenced storage rejected exactly the runs that would have lost", fenced.fencedOff() == plain.lostUpdates());
        check("study is deterministic under the seed", RaceStudy.run(7, 2_000, true).equals(fenced));

        System.out.println(passed + " passed, " + failures.size() + " failed");
        for (String f : failures) System.out.println("FAIL: " + f);
        if (!failures.isEmpty()) System.exit(1);
    }
}
