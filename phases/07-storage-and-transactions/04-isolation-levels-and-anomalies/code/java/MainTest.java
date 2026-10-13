// Tests for phases/07-storage-and-transactions/04-isolation-levels-and-anomalies. Run: javac -d .out *.java && java -cp .out MainTest
import java.util.ArrayList;
import java.util.List;
import java.util.Map;

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
        // --- the matrix is Berenson et al. (1995), Table 4 ---
        Map<String, Map<Isolation, Boolean>> m = AnomalyLab.matrix();
        check("dirty read: only READ_UNCOMMITTED allows it",
                m.get("dirty read").get(Isolation.READ_UNCOMMITTED) && !m.get("dirty read").get(Isolation.READ_COMMITTED)
                && !m.get("dirty read").get(Isolation.REPEATABLE_READ) && !m.get("dirty read").get(Isolation.SNAPSHOT)
                && !m.get("dirty read").get(Isolation.SERIALIZABLE));
        check("non-repeatable read: RU and RC allow it",
                m.get("non-repeatable read").get(Isolation.READ_UNCOMMITTED) && m.get("non-repeatable read").get(Isolation.READ_COMMITTED));
        check("non-repeatable read: RR, SNAPSHOT, SERIALIZABLE prevent it",
                !m.get("non-repeatable read").get(Isolation.REPEATABLE_READ) && !m.get("non-repeatable read").get(Isolation.SNAPSHOT)
                && !m.get("non-repeatable read").get(Isolation.SERIALIZABLE));
        check("phantom: lock-based REPEATABLE_READ still allows it", m.get("phantom").get(Isolation.REPEATABLE_READ));
        check("phantom: SNAPSHOT prevents it (reads from a fixed snapshot)", !m.get("phantom").get(Isolation.SNAPSHOT));
        check("phantom: SERIALIZABLE prevents it (predicate locks)", !m.get("phantom").get(Isolation.SERIALIZABLE));
        check("lost update: RU and RC allow it",
                m.get("lost update").get(Isolation.READ_UNCOMMITTED) && m.get("lost update").get(Isolation.READ_COMMITTED));
        check("lost update: RR, SNAPSHOT, SERIALIZABLE prevent it",
                !m.get("lost update").get(Isolation.REPEATABLE_READ) && !m.get("lost update").get(Isolation.SNAPSHOT)
                && !m.get("lost update").get(Isolation.SERIALIZABLE));
        check("write skew: SNAPSHOT allows it", m.get("write skew").get(Isolation.SNAPSHOT));
        check("write skew: lock-based REPEATABLE_READ prevents it", !m.get("write skew").get(Isolation.REPEATABLE_READ));
        check("write skew: SERIALIZABLE prevents it", !m.get("write skew").get(Isolation.SERIALIZABLE));
        check("write skew: RU and RC allow it", m.get("write skew").get(Isolation.READ_UNCOMMITTED) && m.get("write skew").get(Isolation.READ_COMMITTED));
        check("SERIALIZABLE allows no anomaly at all", m.values().stream().noneMatch(row -> row.get(Isolation.SERIALIZABLE)));
        check("READ_UNCOMMITTED allows every anomaly", m.values().stream().allMatch(row -> row.get(Isolation.READ_UNCOMMITTED)));
        check("RR and SNAPSHOT are incomparable: each allows one the other prevents",
                m.get("phantom").get(Isolation.REPEATABLE_READ) != m.get("phantom").get(Isolation.SNAPSHOT)
                && m.get("write skew").get(Isolation.REPEATABLE_READ) != m.get("write skew").get(Isolation.SNAPSHOT));

        // --- the scenarios say WHY, not just whether ---
        AnomalyLab.Outcome skew = AnomalyLab.writeSkew(Isolation.SNAPSHOT);
        check("write skew under SNAPSHOT: both commit and the combined balance is -800", skew.observed() && skew.detail().startsWith("combined balance -800"));
        AnomalyLab.Outcome skewSer = AnomalyLab.writeSkew(Isolation.SERIALIZABLE);
        check("write skew under SERIALIZABLE: exactly one transaction aborts", skewSer.detail().contains("T1 ABORTED") && skewSer.detail().contains("T2 COMMITTED"));
        AnomalyLab.Outcome lu = AnomalyLab.lostUpdate(Isolation.SNAPSHOT);
        check("lost update under SNAPSHOT: first committer wins, T2 aborted", lu.detail().contains("T1 COMMITTED, T2 ABORTED"));
        check("lost update under SNAPSHOT: trace names the rule", lu.trace().stream().anyMatch(l -> l.contains("first-committer-wins")));
        AnomalyLab.Outcome luRc = AnomalyLab.lostUpdate(Isolation.READ_COMMITTED);
        check("lost update under READ_COMMITTED: final balance 1200, one deposit vanished", luRc.detail().startsWith("final balance 1200"));
        AnomalyLab.Outcome dr = AnomalyLab.dirtyRead(Isolation.READ_UNCOMMITTED);
        check("dirty read under RU: T2 saw 900 though committed value is 1000", dr.detail().equals("T2 saw 900; committed value is 1000"));
        AnomalyLab.Outcome ph = AnomalyLab.phantom(Isolation.REPEATABLE_READ);
        check("phantom under RR: 2 then 3 postings", ph.detail().equals("T1 counted 2 then 3 postings"));

        // --- store mechanics ---
        Store s = new Store();
        s.load("a", 10);
        Txn t1 = s.begin(Isolation.READ_UNCOMMITTED);
        t1.write("a", 5);
        check("RU writes in place", s.committed.get("a") == 5);
        t1.abort();
        check("RU abort restores the old value", s.committed.get("a") == 10);
        check("abort releases locks", s.locks.isEmpty());

        Store s2 = new Store();
        s2.load("a", 10);
        Txn w = s2.begin(Isolation.READ_COMMITTED);
        w.write("a", 7);
        check("RC write is buffered, not visible", s2.committed.get("a") == 10 && w.read("a") == 7);
        Txn other = s2.begin(Isolation.READ_COMMITTED);
        checkThrows("no dirty writes at any level: second X lock conflicts", ConflictException.class, () -> other.write("a", 99));
        check("conflict aborted the requester", other.status == Txn.Status.ABORTED);
        checkThrows("an aborted transaction cannot be used", IllegalStateException.class, () -> other.read("a"));
        w.commit();
        check("RC commit publishes the write and bumps the clock", s2.committed.get("a") == 7 && s2.commitClock == 1 && s2.versionOf.get("a") == 1);
        checkThrows("commit twice is an error", IllegalStateException.class, w::commit);

        Store s3 = new Store();
        s3.load("x", 1);
        Txn r = s3.begin(Isolation.REPEATABLE_READ);
        r.read("x");
        Txn r2 = s3.begin(Isolation.REPEATABLE_READ);
        check("RR: two readers share the S lock", r2.read("x") == 1 && s3.locks.get("x").shared.size() == 2);
        checkThrows("RR: writer conflicts with another reader's S lock", ConflictException.class, () -> r2.write("x", 2));
        r.write("x", 3);
        check("RR: sole reader upgrades S to X", s3.locks.get("x").exclusive == r.id);
        r.commit();
        check("RR: commit applies buffered write", s3.committed.get("x") == 3);

        Store s4 = new Store();
        s4.load("p:1", 1);
        Txn scanner = s4.begin(Isolation.SERIALIZABLE);
        check("SERIALIZABLE scan sees existing rows", scanner.scan("p:").size() == 1);
        Txn inserter = s4.begin(Isolation.SERIALIZABLE);
        checkThrows("SERIALIZABLE: insert under a predicate lock conflicts", ConflictException.class, () -> inserter.write("p:2", 2));
        Txn elsewhere = s4.begin(Isolation.SERIALIZABLE);
        elsewhere.write("q:1", 5);
        elsewhere.commit();
        check("SERIALIZABLE: write outside the predicate is allowed", s4.committed.get("q:1") == 5);
        scanner.commit();
        check("commit releases predicate locks", s4.predicateLocks.isEmpty());

        Store s5 = new Store();
        s5.load("k", 1);
        Txn snap = s5.begin(Isolation.SNAPSHOT);
        Txn writer = s5.begin(Isolation.READ_COMMITTED);
        writer.write("k", 2);
        writer.commit();
        check("SNAPSHOT reads the value as of begin", snap.read("k") == 1);
        snap.write("k", 10);
        checkThrows("SNAPSHOT: first-committer-wins rejects a write over a newer commit", ConflictException.class, snap::commit);
        check("SNAPSHOT: the committed value is the first committer's", s5.committed.get("k") == 2);
        Txn snapReadOnly = s5.begin(Isolation.SNAPSHOT);
        Txn w2 = s5.begin(Isolation.READ_COMMITTED);
        w2.write("k", 3);
        w2.commit();
        snapReadOnly.read("k");
        snapReadOnly.commit();
        check("SNAPSHOT: a transaction with no writes always commits", snapReadOnly.status == Txn.Status.COMMITTED);

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }
}
