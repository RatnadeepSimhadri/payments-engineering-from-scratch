// Tests for phases/07-storage-and-transactions/06-mvcc-and-snapshot-isolation. Run: javac -d .out *.java && java -cp .out MainTest
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

    static MvccStore<Long> seeded() {
        MvccStore<Long> store = new MvccStore<>(new TimestampOracle());
        Transaction<Long> t = store.begin();
        store.write(t, "A", 100L);
        store.write(t, "B", 100L);
        store.commit(t);
        return store;
    }

    public static void main(String[] args) {
        // snapshot reads
        final MvccStore<Long> s = seeded();
        Transaction<Long> r = s.begin();
        check("snapshot sees committed setup", s.read(r, "A").equals(Optional.of(100L)));
        Transaction<Long> w = s.begin();
        s.write(w, "A", 40L);
        check("buffered write invisible to other txn", s.read(r, "A").equals(Optional.of(100L)));
        check("buffered write visible to itself", s.read(w, "A").equals(Optional.of(40L)));
        long ts = s.commit(w);
        check("commit returns a fresh timestamp", ts == 2);
        check("reader keeps its snapshot after a concurrent commit", s.read(r, "A").equals(Optional.of(100L)));
        check("new snapshot sees the commit", s.read(s.begin(), "A").equals(Optional.of(40L)));
        check("missing key reads empty", s.read(r, "Z").isEmpty());
        check("read-only commit consumes no timestamp", s.commit(r) == r.startTs);
        check("two versions of A exist", s.versionCount("A") == 2);

        // first committer wins
        final MvccStore<Long> f = seeded();
        Transaction<Long> t1 = f.begin();
        Transaction<Long> t2 = f.begin();
        f.write(t1, "A", 90L);
        f.write(t2, "A", 75L);
        f.commit(t1);
        checkThrows("second committer on the same key aborts", WriteConflictException.class, () -> f.commit(t2));
        check("loser is marked ABORTED", t2.status == TxnStatus.ABORTED);
        check("loser's write never became a version", f.read(f.begin(), "A").equals(Optional.of(90L)));
        check("abort counted", f.aborts() == 1 && f.commits() == 2);
        checkThrows("aborted txn cannot be used", IllegalStateException.class, () -> f.read(t2, "A"));

        // a later writer that started after the commit is not a conflict
        Transaction<Long> t3 = f.begin();
        f.write(t3, "A", 80L);
        f.commit(t3);
        check("writer with a fresh snapshot commits", f.read(f.begin(), "A").equals(Optional.of(80L)));

        // blind writes to different keys never conflict; write skew is allowed
        MvccStore<Long> skew = seeded();
        Transaction<Long> w1 = skew.begin();
        Transaction<Long> w2 = skew.begin();
        long sum1 = skew.read(w1, "A").get() + skew.read(w1, "B").get();
        long sum2 = skew.read(w2, "A").get() + skew.read(w2, "B").get();
        if (sum1 >= 150) skew.write(w1, "A", skew.read(w1, "A").get() - 150);
        if (sum2 >= 150) skew.write(w2, "B", skew.read(w2, "B").get() - 150);
        skew.commit(w1);
        skew.commit(w2);
        Transaction<Long> audit = skew.begin();
        check("write skew: both commit under SI", skew.commits() == 3);
        check("write skew: invariant A+B >= 0 is violated", skew.read(audit, "A").get() + skew.read(audit, "B").get() == -100);

        // deletes and tombstones
        MvccStore<Long> d = seeded();
        Transaction<Long> old = d.begin();
        Transaction<Long> del = d.begin();
        d.delete(del, "B");
        check("own delete reads as absent", d.read(del, "B").isEmpty());
        d.commit(del);
        check("older snapshot still sees deleted key", d.read(old, "B").equals(Optional.of(100L)));
        check("new snapshot does not see deleted key", d.read(d.begin(), "B").isEmpty());
        check("tombstone is a version", d.versionCount("B") == 2 && d.history("B").get(1).isTombstone());

        // vacuum respects the oldest active snapshot
        MvccStore<Long> g = seeded();
        Transaction<Long> pin = g.begin();
        for (int i = 0; i < 5; i++) {
            Transaction<Long> t = g.begin();
            g.write(t, "A", g.read(t, "A").get() + 1);
            g.commit(t);
        }
        check("six versions of A before vacuum", g.versionCount("A") == 6);
        check("vacuum with pinned snapshot removes nothing visible to it", g.vacuum() == 0 && g.versionCount("A") == 6);
        check("pinned reader still sees its version", g.read(pin, "A").equals(Optional.of(100L)));
        g.commit(pin);
        check("vacuum after pin released removes dead versions", g.vacuum() == 5 && g.versionCount("A") == 1);
        Transaction<Long> after = g.begin();
        check("surviving version is the newest", g.read(after, "A").equals(Optional.of(105L)));
        g.commit(after);
        Transaction<Long> del2 = g.begin();
        g.delete(del2, "B");
        g.commit(del2);
        check("vacuum removes a dead tombstone and its key", g.vacuum() == 2 && g.versionCount("B") == 0);
        check("no active transactions left", g.activeCount() == 0);

        // money helpers used by the demo
        check("usd parses cents", Main.usd("19.99").minorUnits() == 1999 && Main.usd("5").minorUnits() == 500);
        check("money toString", Main.usd("-50.00").minus(Main.usd("0.05")).toString().equals("-50.05 USD"));

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(msg -> System.out.println("FAIL: " + msg));
        if (!failures.isEmpty()) System.exit(1);
    }
}
