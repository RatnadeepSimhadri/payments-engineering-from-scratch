// Tests for phases/07-storage-and-transactions/05-two-phase-locking. Run: javac -d .out *.java && java -cp .out MainTest
import java.util.ArrayList;
import java.util.List;
import java.util.Optional;

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

    static Money usd(long minor) { return Money.ofMinor(minor, Currency.USD); }

    public static void main(String[] args) {
        // --- lock compatibility and the lock table ---
        check("S is compatible with S", LockMode.S.compatibleWith(LockMode.S));
        check("S is not compatible with X", !LockMode.S.compatibleWith(LockMode.X) && !LockMode.X.compatibleWith(LockMode.S));
        check("X is not compatible with X", !LockMode.X.compatibleWith(LockMode.X));

        LockManager lm = new LockManager(new ArrayList<>());
        check("first S granted", lm.acquire(1, "a", LockMode.S));
        check("second S granted alongside", lm.acquire(2, "a", LockMode.S));
        check("X waits behind two readers", !lm.acquire(3, "a", LockMode.X));
        check("a later S waits behind the queued X (FIFO, no writer starvation)", !lm.acquire(4, "a", LockMode.S));
        check("blockers of T4 include the holders and the queued writer", lm.blockersOf(4, "a", LockMode.S).equals(java.util.Set.of(3)));
        check("re-requesting a held lock is a no-op grant", lm.acquire(1, "a", LockMode.S));
        List<Integer> g1 = lm.releaseAll(1);
        check("releasing one reader grants nobody while the other reader remains", g1.isEmpty());
        List<Integer> g2 = lm.releaseAll(2);
        check("releasing the last reader grants the writer only", g2.equals(List.of(3)) && lm.table.get("a").holders.get(3) == LockMode.X);
        List<Integer> g3 = lm.releaseAll(3);
        check("releasing the writer grants the queued reader", g3.equals(List.of(4)));
        lm.releaseAll(4);
        check("lock table is empty after everyone releases", lm.table.isEmpty());

        // --- wait-for graph and cycle detection ---
        WaitForGraph g = new WaitForGraph();
        g.addEdge(1, 2);
        g.addEdge(2, 3);
        check("a chain is not a deadlock", g.findCycle().isEmpty());
        g.addEdge(3, 1);
        Optional<List<Integer>> cycle = g.findCycle();
        check("a three-way cycle is found in wait order", cycle.isPresent() && cycle.get().equals(List.of(1, 2, 3)));
        WaitForGraph g2b = new WaitForGraph();
        g2b.addEdge(5, 6);
        g2b.addEdge(7, 5);
        g2b.addEdge(6, 7);
        g2b.addEdge(9, 5);                                    // T9 waits on the cycle but is not part of it
        check("a transaction waiting on a cycle is not reported inside it", g2b.findCycle().get().equals(List.of(5, 6, 7)));

        // --- the transfer deadlock, reproduced and resolved ---
        Scheduler s = new Scheduler(VictimPolicy.YOUNGEST, true);
        s.load("acct:A", usd(100_000));
        s.load("acct:B", usd(50_000));
        Txn t1 = s.begin(Main.transfer("acct:A", "acct:B", 10_000));
        Txn t2 = s.begin(Main.transfer("acct:B", "acct:A", 5_000));
        s.run(1, 2, 1, 2);
        check("exactly one deadlock detected", s.deadlocksDetected == 1);
        check("the cycle is T1 -> T2 -> T1", s.cycles.get(0).equals(List.of(1, 2)));
        check("YOUNGEST picks T2", s.victims.equals(List.of(2)));
        check("the victim was restarted once and then committed", t2.restarts == 1 && t2.status == Txn.Status.COMMITTED);
        check("T1 committed without restarting", t1.restarts == 0 && t1.status == Txn.Status.COMMITTED);
        check("both transfers applied: A = 950.00", s.db.get("acct:A").equals(usd(95_000)));
        check("both transfers applied: B = 550.00", s.db.get("acct:B").equals(usd(55_000)));
        check("money is conserved", s.total(Currency.USD).equals(usd(150_000)));
        check("strict 2PL released every lock at commit", s.lockTableEmpty());

        // --- the serial schedule has no deadlock and the same result ---
        Scheduler serial = new Scheduler(VictimPolicy.YOUNGEST, true);
        serial.load("acct:A", usd(100_000));
        serial.load("acct:B", usd(50_000));
        serial.begin(Main.transfer("acct:A", "acct:B", 10_000));
        serial.begin(Main.transfer("acct:B", "acct:A", 5_000));
        serial.run(1, 1, 1, 2, 2, 2);
        check("serial execution: no deadlock", serial.deadlocksDetected == 0);
        check("serial execution: same final state as the resolved deadlock", serial.db.equals(s.db));

        // --- victim policy ---
        Scheduler f = new Scheduler(VictimPolicy.FEWEST_LOCKS, true);
        f.load("acct:A", usd(100_000));
        f.load("acct:B", usd(50_000));
        f.load("fees", usd(0));
        f.begin(Main.transfer("acct:A", "acct:B", 10_000));
        f.begin(List.of(new Op.Add("acct:B", usd(-5_050)), new Op.Add("fees", usd(50)), new Op.Add("acct:A", usd(5_000)), new Op.Commit()));
        f.run(1, 2, 2, 1, 2);
        check("FEWEST_LOCKS aborts the transaction holding fewer locks (T1 with 1, not T2 with 2)", f.victims.equals(List.of(1)));
        check("FEWEST_LOCKS: everything still commits and balances", f.total(Currency.USD).equals(usd(150_000))
                && f.db.get("acct:A").equals(usd(95_000)) && f.db.get("acct:B").equals(usd(54_950)) && f.db.get("fees").equals(usd(50)));

        // --- upgrade deadlock: read-then-write on the same row from two transactions ---
        Scheduler u = new Scheduler(VictimPolicy.YOUNGEST, true);
        u.load("acct:A", usd(100_000));
        u.begin(List.of(new Op.Read("acct:A"), new Op.Add("acct:A", usd(1_000)), new Op.Commit()));
        u.begin(List.of(new Op.Read("acct:A"), new Op.Add("acct:A", usd(2_000)), new Op.Commit()));
        u.run(1, 2, 1, 2);
        check("S->X upgrade by two holders is a deadlock", u.deadlocksDetected == 1);
        check("no lost update: both deposits landed", u.db.get("acct:A").equals(usd(103_000)));
        check("the restarted deposit re-read the committed balance", u.txns.get(2).reads.get("acct:A").equals(usd(101_000)));

        // --- abort undoes in-place writes in reverse order ---
        Scheduler a = new Scheduler(VictimPolicy.YOUNGEST, false);
        a.load("x", usd(10));
        a.load("y", usd(20));
        Txn ta = a.begin(List.of(new Op.Add("x", usd(5)), new Op.Add("y", usd(-5)), new Op.Add("x", usd(1)), new Op.Commit()));
        a.step(ta); a.step(ta); a.step(ta);
        check("writes are applied in place while locks are held", a.db.get("x").equals(usd(16)) && a.db.get("y").equals(usd(15)));
        a.abort(ta);
        check("abort restores both keys to their pre-transaction values", a.db.get("x").equals(usd(10)) && a.db.get("y").equals(usd(20)));
        check("abort without restart leaves the transaction ABORTED and lock-free", ta.status == Txn.Status.ABORTED && a.lockTableEmpty());

        // --- basic 2PL: early release lets a reader see an uncommitted value (why strict 2PL exists) ---
        Scheduler b = new Scheduler(VictimPolicy.YOUNGEST, false);
        b.load("acct:A", usd(100_000));
        Txn writer = b.begin(List.of(new Op.Add("acct:A", usd(-90_000)), new Op.Unlock("acct:A"), new Op.Read("acct:A")));
        Txn reader = b.begin(List.of(new Op.Read("acct:A"), new Op.Commit()));
        b.step(writer); b.step(writer); b.step(reader); b.step(reader);
        b.abort(writer);
        check("early release exposes an uncommitted balance to a committed reader", reader.reads.get("acct:A").equals(usd(10_000))
                && reader.status == Txn.Status.COMMITTED && b.db.get("acct:A").equals(usd(100_000)));
        check("releasing a lock moves the transaction to the shrinking phase", writer.phase == Txn.Phase.SHRINKING);

        // --- the two-phase rule: no lock acquisition after the first release ---
        Scheduler v = new Scheduler(VictimPolicy.YOUNGEST, false);
        v.load("k", usd(1));
        v.load("j", usd(1));
        Txn tv = v.begin(List.of(new Op.Add("k", usd(1)), new Op.Unlock("k"), new Op.Read("j")));
        v.step(tv); v.step(tv);
        checkThrows("growing after shrinking is a TwoPhaseViolation", TwoPhaseViolationException.class, () -> v.step(tv));

        // --- a blocked transaction that is not in a cycle simply waits ---
        Scheduler w = new Scheduler(VictimPolicy.YOUNGEST, true);
        w.load("acct:A", usd(100_000));
        w.begin(List.of(new Op.Add("acct:A", usd(-1)), new Op.Commit()));
        w.begin(List.of(new Op.Read("acct:A"), new Op.Commit()));
        w.step(w.txns.get(1));
        w.step(w.txns.get(2));
        check("reader blocks behind the writer without a deadlock", w.txns.get(2).status == Txn.Status.BLOCKED && w.deadlocksDetected == 0);
        w.step(w.txns.get(1));
        check("commit wakes the waiter and it reads the committed value", w.txns.get(2).status == Txn.Status.ACTIVE
                && w.txns.get(2).reads.get("acct:A").equals(usd(99_999)));

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f2 -> System.out.println("FAIL: " + f2));
        if (!failures.isEmpty()) System.exit(1);
    }
}
