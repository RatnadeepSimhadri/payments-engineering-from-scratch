// Tests for phases/07-storage-and-transactions/07-serializable-snapshot-isolation. Run: javac -d .out *.java && java -cp .out MainTest
import java.util.ArrayList;
import java.util.List;
import java.util.Optional;
import java.util.Set;

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

    static SsiStore<Long> seeded() {
        SsiStore<Long> store = new SsiStore<>(new TimestampOracle());
        Transaction<Long> t = store.begin();
        store.write(t, "A", 100L);
        store.write(t, "B", 100L);
        store.commit(t);
        return store;
    }

    /** Lesson 06's write-skew program: check A+B >= amount, then withdraw from one account. */
    static boolean withdraw(SsiStore<Long> s, Transaction<Long> t, String from, String other, long amount) {
        long total = s.read(t, from).get() + s.read(t, other).get();
        if (total < amount) return false;
        s.write(t, from, s.read(t, from).get() - amount);
        return true;
    }

    public static void main(String[] args) {
        // SI behaviour is unchanged
        final SsiStore<Long> si = seeded();
        Transaction<Long> r = si.begin();
        Transaction<Long> w = si.begin();
        si.write(w, "A", 40L);
        check("buffered write invisible", si.read(r, "A").equals(Optional.of(100L)));
        si.commit(w);
        check("snapshot is stable after a concurrent commit", si.read(r, "A").equals(Optional.of(100L)));
        check("one rw edge: reader commits fine", si.commit(r) == r.startTs && r.status == TxnStatus.COMMITTED);
        final Transaction<Long> x1 = si.begin();
        final Transaction<Long> x2 = si.begin();
        si.write(x1, "A", 1L);
        si.write(x2, "A", 2L);
        si.commit(x1);
        checkThrows("first committer still wins", WriteConflictException.class, () -> si.commit(x2));

        // write skew: exactly one of the two aborts, and the invariant holds
        final SsiStore<Long> skew = seeded();
        final Transaction<Long> t1 = skew.begin();
        final Transaction<Long> t2 = skew.begin();
        check("t1 withdraws on its snapshot", withdraw(skew, t1, "A", "B", 150));
        check("t2 withdraws on its snapshot", withdraw(skew, t2, "B", "A", 150));
        check("both are pivots before either commits", t1.isPivot() && t2.isPivot());
        check("edges recorded in both directions", t1.inConflicts.equals(Set.of(t2.id)) && t1.outConflicts.equals(Set.of(t2.id)));
        checkThrows("first to commit is the pivot and aborts", SerializationFailureException.class, () -> skew.commit(t1));
        check("abort removed its edges from the survivor", t2.inConflicts.isEmpty() && t2.outConflicts.isEmpty());
        check("survivor commits", skew.commit(t2) > 0 && t2.status == TxnStatus.COMMITTED);
        Transaction<Long> audit = skew.begin();
        check("invariant A+B >= 0 holds", skew.read(audit, "A").get() + skew.read(audit, "B").get() == 50);
        skew.commit(audit);
        check("exactly one abort", skew.aborts() == 1 && skew.commits() == 3);
        checkThrows("aborted transaction is unusable", IllegalStateException.class, () -> skew.read(t1, "A"));

        // write skew where the first transaction commits before the second reads: abort on the spot
        final SsiStore<Long> skew2 = seeded();
        final Transaction<Long> p = skew2.begin();
        final Transaction<Long> q = skew2.begin();
        withdraw(skew2, p, "A", "B", 150);
        skew2.commit(p);
        check("pivot-to-be committed with one edge at most", p.status == TxnStatus.COMMITTED);
        long qSum = skew2.read(q, "A").get() + skew2.read(q, "B").get();
        check("q reads the old snapshot and records q -rw-> p", qSum == 200 && q.outConflicts.contains(p.id) && p.inConflicts.contains(q.id));
        checkThrows("q's write of B closes the structure: aborted immediately", SerializationFailureException.class, () -> skew2.write(q, "B", -50L));
        check("q is aborted with a reason naming the pivot", q.status == TxnStatus.ABORTED && q.abortReason.contains("T" + p.id));

        // disjoint transactions never conflict
        SsiStore<Long> dj = seeded();
        Transaction<Long> d1 = dj.begin();
        Transaction<Long> d2 = dj.begin();
        dj.write(d1, "A", dj.read(d1, "A").get() + 1);
        dj.write(d2, "B", dj.read(d2, "B").get() + 1);
        dj.commit(d1);
        dj.commit(d2);
        check("disjoint read/write sets both commit", dj.commits() == 3 && dj.aborts() == 0);

        // SIREAD locks are retained after commit while a concurrent transaction is active
        SsiStore<Long> ret = seeded();
        Transaction<Long> early = ret.begin();
        Transaction<Long> late = ret.begin();
        ret.read(early, "B");
        ret.commit(early);
        check("read lock retained after commit", ret.readersOf("B").contains(early.id));
        check("only the setup txn is releasable while late is active", ret.releaseFinished() == 1 && ret.readersOf("B").contains(early.id));
        ret.write(late, "B", 1L);
        check("late writer gets an incoming edge from the committed reader", late.inConflicts.contains(early.id));
        ret.commit(late);
        check("single edge: late commits", late.status == TxnStatus.COMMITTED);
        check("finished transactions released once nothing is concurrent", ret.releaseFinished() == 2 && ret.retainedReadLocks() == 0);

        // a reader that observes a committed pivot is aborted when it reads (classic read-only anomaly shape)
        SsiStore<Long> ro = seeded();
        Transaction<Long> tA = ro.begin();            // will write A after reading B
        Transaction<Long> tB = ro.begin();            // will write B
        ro.read(tA, "B");
        ro.write(tB, "B", 1L);                        // tA -rw-> tB
        ro.commit(tB);
        ro.write(tA, "A", 1L);
        ro.commit(tA);                                // tA has only an out edge: fine
        Transaction<Long> tR = ro.begin();            // started after tA's commit, so it is not concurrent with tA...
        check("later read-only txn sees both commits and is not aborted", ro.read(tR, "A").equals(Optional.of(1L)) && ro.read(tR, "B").equals(Optional.of(1L)));
        ro.commit(tR);
        check("no spurious aborts in a serializable history", ro.aborts() == 0);

        // deletes participate in conflict detection
        final SsiStore<Long> del = seeded();
        final Transaction<Long> rd = del.begin();
        final Transaction<Long> dl = del.begin();
        del.read(rd, "A");
        del.delete(dl, "A");
        check("delete records rd -rw-> dl", rd.outConflicts.contains(dl.id));
        del.commit(dl);
        check("reader keeps seeing the deleted key", del.read(rd, "A").equals(Optional.of(100L)));
        del.commit(rd);
        check("new snapshot sees the delete", del.read(del.begin(), "A").isEmpty());

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(msg -> System.out.println("FAIL: " + msg));
        if (!failures.isEmpty()) System.exit(1);
    }
}
