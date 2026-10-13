// Lesson: phases/07-storage-and-transactions/04-isolation-levels-and-anomalies/docs/en.md
// Isolation Levels and Their Anomalies
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: ANSI/ISO SQL-92 (ISO/IEC 9075:1992) section 4.28; Berenson, Bernstein, Gray, Melton, O'Neil, O'Neil,
//          "A Critique of ANSI SQL Isolation Levels" (SIGMOD 1995). Java 21, standard library only.

import java.util.ArrayList;
import java.util.HashMap;
import java.util.HashSet;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.Set;
import java.util.TreeMap;

/** The five levels this store can run at. The first three are the lock-based ANSI levels; SNAPSHOT is Berenson's SI. */
enum Isolation { READ_UNCOMMITTED, READ_COMMITTED, REPEATABLE_READ, SNAPSHOT, SERIALIZABLE }

/** Raised when an operation would conflict with another live transaction. The transaction that receives it is aborted. */
final class ConflictException extends RuntimeException {
    ConflictException(String message) { super(message); }
}

/** Per-key lock state. One exclusive holder, or any number of shared holders. */
final class LockEntry {
    final Set<Integer> shared = new HashSet<>();
    Integer exclusive;

    boolean heldByOthers(int me) {
        if (exclusive != null && exclusive != me) return true;
        for (int s : shared) if (s != me) return true;
        return false;
    }
}

/**
 * A single-version key-value store (values are balances in minor units) with transactions.
 * No MVCC: there is exactly one committed value per key. Isolation comes from locks (no-wait: a conflict aborts the
 * requester instead of blocking) or, at SNAPSHOT, from a private copy taken at begin plus first-committer-wins.
 */
final class Store {
    final TreeMap<String, Long> committed = new TreeMap<>();
    final Map<String, Long> versionOf = new HashMap<>();     // key -> commit number that last wrote it
    final Map<String, LockEntry> locks = new HashMap<>();
    final Map<String, Set<Integer>> predicateLocks = new HashMap<>();   // prefix -> txns that scanned it (SERIALIZABLE)
    long commitClock = 0;
    private int nextTxId = 1;
    final List<String> trace = new ArrayList<>();

    void load(String key, long value) {
        committed.put(key, value);
        versionOf.put(key, 0L);
    }

    Txn begin(Isolation level) {
        Txn t = new Txn(this, nextTxId++, level);
        trace.add("T" + t.id + " begin " + level);
        return t;
    }

    // ---- lock table (no-wait) ----

    void lockShared(int tx, String key) {
        LockEntry e = locks.computeIfAbsent(key, k -> new LockEntry());
        if (e.exclusive != null && e.exclusive != tx) throw new ConflictException("T" + tx + " S-lock " + key + " blocked by X of T" + e.exclusive);
        e.shared.add(tx);
    }

    void lockExclusive(int tx, String key) {
        LockEntry e = locks.computeIfAbsent(key, k -> new LockEntry());
        if (e.heldByOthers(tx)) throw new ConflictException("T" + tx + " X-lock " + key + " blocked by " + describeHolders(e, tx));
        e.shared.remove(tx);
        e.exclusive = tx;
    }

    void lockPredicate(int tx, String prefix) {
        // conflict if another transaction holds X on any existing key under this prefix
        for (Map.Entry<String, LockEntry> e : locks.entrySet()) {
            if (e.getKey().startsWith(prefix) && e.getValue().exclusive != null && e.getValue().exclusive != tx) {
                throw new ConflictException("T" + tx + " predicate " + prefix + "* blocked by X of T" + e.getValue().exclusive + " on " + e.getKey());
            }
        }
        predicateLocks.computeIfAbsent(prefix, p -> new HashSet<>()).add(tx);
    }

    void checkPredicateConflict(int tx, String key) {
        for (Map.Entry<String, Set<Integer>> e : predicateLocks.entrySet()) {
            if (key.startsWith(e.getKey())) {
                for (int holder : e.getValue()) {
                    if (holder != tx) throw new ConflictException("T" + tx + " write " + key + " blocked by predicate lock " + e.getKey() + "* of T" + holder);
                }
            }
        }
    }

    void releaseAll(int tx) {
        locks.values().forEach(e -> { e.shared.remove(tx); if (e.exclusive != null && e.exclusive == tx) e.exclusive = null; });
        locks.entrySet().removeIf(e -> e.getValue().exclusive == null && e.getValue().shared.isEmpty());
        predicateLocks.values().forEach(s -> s.remove(tx));
        predicateLocks.entrySet().removeIf(e -> e.getValue().isEmpty());
    }

    private static String describeHolders(LockEntry e, int me) {
        if (e.exclusive != null && e.exclusive != me) return "X of T" + e.exclusive;
        List<Integer> others = new ArrayList<>();
        for (int s : e.shared) if (s != me) others.add(s);
        return "S of T" + others;
    }

    Map<String, Long> committedWithPrefix(String prefix) {
        Map<String, Long> out = new TreeMap<>();
        for (Map.Entry<String, Long> e : committed.tailMap(prefix, true).entrySet()) {
            if (!e.getKey().startsWith(prefix)) break;
            out.put(e.getKey(), e.getValue());
        }
        return out;
    }
}

final class Txn {
    enum Status { ACTIVE, COMMITTED, ABORTED }

    final Store store;
    final int id;
    final Isolation level;
    Status status = Status.ACTIVE;
    final Map<String, Long> writes = new LinkedHashMap<>();        // buffered writes (all levels except READ_UNCOMMITTED)
    final Map<String, Long> undo = new LinkedHashMap<>();          // READ_UNCOMMITTED: previous committed value for in-place writes
    final TreeMap<String, Long> snapshot;                          // SNAPSHOT: private copy taken at begin
    final long snapshotVersion;
    final Set<String> readSet = new HashSet<>();

    Txn(Store store, int id, Isolation level) {
        this.store = store;
        this.id = id;
        this.level = level;
        this.snapshot = level == Isolation.SNAPSHOT ? new TreeMap<>(store.committed) : null;
        this.snapshotVersion = store.commitClock;
    }

    private void requireActive() {
        if (status != Status.ACTIVE) throw new IllegalStateException("T" + id + " is " + status);
    }

    /** Wraps an operation so a conflict aborts this transaction and releases its locks before propagating. */
    private <T> T guarded(java.util.function.Supplier<T> op) {
        requireActive();
        try {
            return op.get();
        } catch (ConflictException c) {
            store.trace.add("  " + c.getMessage() + " -> T" + id + " ABORTED");
            abort();
            throw c;
        }
    }

    Long read(String key) {
        return guarded(() -> {
            Long v = switch (level) {
                case READ_UNCOMMITTED -> store.committed.get(key);                 // sees in-place writes of others
                case READ_COMMITTED -> writes.containsKey(key) ? writes.get(key) : store.committed.get(key);
                case REPEATABLE_READ, SERIALIZABLE -> {
                    if (!writes.containsKey(key)) store.lockShared(id, key);
                    yield writes.containsKey(key) ? writes.get(key) : store.committed.get(key);
                }
                case SNAPSHOT -> writes.containsKey(key) ? writes.get(key) : snapshot.get(key);
            };
            readSet.add(key);
            store.trace.add("  T" + id + " read " + key + " = " + v);
            return v;
        });
    }

    /** All keys with the given prefix: a predicate read, the kind that phantoms attack. */
    Map<String, Long> scan(String prefix) {
        return guarded(() -> {
            Map<String, Long> base = switch (level) {
                case SNAPSHOT -> {
                    Map<String, Long> out = new TreeMap<>();
                    for (Map.Entry<String, Long> e : snapshot.tailMap(prefix, true).entrySet()) {
                        if (!e.getKey().startsWith(prefix)) break;
                        out.put(e.getKey(), e.getValue());
                    }
                    yield out;
                }
                case SERIALIZABLE -> {
                    store.lockPredicate(id, prefix);
                    Map<String, Long> out = store.committedWithPrefix(prefix);
                    for (String k : out.keySet()) store.lockShared(id, k);
                    yield out;
                }
                case REPEATABLE_READ -> {
                    Map<String, Long> out = store.committedWithPrefix(prefix);
                    for (String k : out.keySet()) store.lockShared(id, k);     // item locks only: new keys are unprotected
                    yield out;
                }
                default -> store.committedWithPrefix(prefix);
            };
            Map<String, Long> result = new TreeMap<>(base);
            if (level != Isolation.READ_UNCOMMITTED) {
                for (Map.Entry<String, Long> w : writes.entrySet()) if (w.getKey().startsWith(prefix)) result.put(w.getKey(), w.getValue());
            }
            store.trace.add("  T" + id + " scan " + prefix + "* = " + result);
            return result;
        });
    }

    void write(String key, long value) {
        guarded(() -> {
            switch (level) {
                case READ_UNCOMMITTED -> {
                    store.lockExclusive(id, key);                    // still no dirty WRITES: X lock held to commit
                    undo.putIfAbsent(key, store.committed.get(key));
                    store.committed.put(key, value);                 // applied in place, visible to everyone now
                }
                case READ_COMMITTED, REPEATABLE_READ -> {
                    store.lockExclusive(id, key);
                    writes.put(key, value);
                }
                case SERIALIZABLE -> {
                    store.checkPredicateConflict(id, key);
                    store.lockExclusive(id, key);
                    writes.put(key, value);
                }
                case SNAPSHOT -> writes.put(key, value);             // no locks; validated at commit
            }
            store.trace.add("  T" + id + " write " + key + " = " + value);
            return null;
        });
    }

    void commit() {
        guarded(() -> {
            if (level == Isolation.SNAPSHOT) {
                for (String k : writes.keySet()) {
                    long v = store.versionOf.getOrDefault(k, 0L);
                    if (v > snapshotVersion) throw new ConflictException("T" + id + " first-committer-wins: " + k + " committed at v" + v + " after snapshot v" + snapshotVersion);
                }
            }
            store.commitClock++;
            if (level == Isolation.READ_UNCOMMITTED) {
                for (String k : undo.keySet()) store.versionOf.put(k, store.commitClock);
            } else {
                for (Map.Entry<String, Long> w : writes.entrySet()) {
                    store.committed.put(w.getKey(), w.getValue());
                    store.versionOf.put(w.getKey(), store.commitClock);
                }
            }
            status = Status.COMMITTED;
            store.releaseAll(id);
            store.trace.add("  T" + id + " COMMIT (v" + store.commitClock + ")");
            return null;
        });
    }

    void abort() {
        if (status != Status.ACTIVE) return;
        if (level == Isolation.READ_UNCOMMITTED) {
            for (Map.Entry<String, Long> u : undo.entrySet()) {
                if (u.getValue() == null) store.committed.remove(u.getKey()); else store.committed.put(u.getKey(), u.getValue());
            }
        }
        status = Status.ABORTED;
        store.releaseAll(id);
        store.trace.add("  T" + id + " ABORT");
    }
}

/** Each scenario is a fixed interleaving of two transactions that produces one anomaly when the level allows it. */
final class AnomalyLab {
    record Outcome(String anomaly, Isolation level, boolean observed, String detail, List<String> trace) {}

    static final String[] ANOMALIES = {"dirty read", "non-repeatable read", "phantom", "lost update", "write skew"};

    static Outcome run(String anomaly, Isolation level) {
        return switch (anomaly) {
            case "dirty read" -> dirtyRead(level);
            case "non-repeatable read" -> nonRepeatableRead(level);
            case "phantom" -> phantom(level);
            case "lost update" -> lostUpdate(level);
            case "write skew" -> writeSkew(level);
            default -> throw new IllegalArgumentException(anomaly);
        };
    }

    /** Scripts keep going after a conflict: the aborted transaction's later steps are simply skipped, as a retry loop would. */
    private static void quietly(Runnable r) {
        try { r.run(); }
        catch (ConflictException ignored) { /* already aborted and traced by guarded() */ }
        catch (IllegalStateException ignored) { /* a later step of a transaction that was aborted earlier */ }
    }

    /** P1: T2 reads a value T1 wrote but then rolled back. */
    static Outcome dirtyRead(Isolation level) {
        Store s = new Store();
        s.load("acct:alice", 1000);
        Txn t1 = s.begin(level), t2 = s.begin(level);
        quietly(() -> t1.write("acct:alice", 900));         // alice pays 1.00, not yet committed
        Long[] seen = new Long[1];
        quietly(() -> seen[0] = t2.read("acct:alice"));
        t1.abort();                                           // the payment is declined and rolled back
        quietly(t2::commit);
        boolean observed = seen[0] != null && seen[0] == 900;
        return new Outcome("dirty read", level, observed, "T2 saw " + seen[0] + "; committed value is " + s.committed.get("acct:alice"), s.trace);
    }

    /** P2: T1 reads the same key twice and gets different values because T2 committed in between. */
    static Outcome nonRepeatableRead(Isolation level) {
        Store s = new Store();
        s.load("acct:alice", 1000);
        Txn t1 = s.begin(level), t2 = s.begin(level);
        Long[] first = new Long[1], second = new Long[1];
        quietly(() -> first[0] = t1.read("acct:alice"));
        quietly(() -> t2.write("acct:alice", 500));
        quietly(t2::commit);
        quietly(() -> second[0] = t1.read("acct:alice"));
        quietly(t1::commit);
        boolean observed = first[0] != null && second[0] != null && !first[0].equals(second[0]);
        return new Outcome("non-repeatable read", level, observed, "T1 read " + first[0] + " then " + second[0], s.trace);
    }

    /** P3: T1 runs the same predicate query twice; T2 inserted a matching row in between. */
    static Outcome phantom(Isolation level) {
        Store s = new Store();
        s.load("posting:m42:001", 500);
        s.load("posting:m42:002", 700);
        Txn t1 = s.begin(level), t2 = s.begin(level);
        int[] counts = new int[2];
        quietly(() -> counts[0] = t1.scan("posting:m42:").size());
        quietly(() -> t2.write("posting:m42:003", 300));     // a new posting for the same merchant
        quietly(t2::commit);
        quietly(() -> counts[1] = t1.scan("posting:m42:").size());
        quietly(t1::commit);
        boolean observed = t1.status != Txn.Status.ABORTED && counts[0] != counts[1];
        return new Outcome("phantom", level, observed, "T1 counted " + counts[0] + " then " + counts[1] + " postings", s.trace);
    }

    /** P4: two read-modify-write deposits; one increment vanishes. */
    static Outcome lostUpdate(Isolation level) {
        Store s = new Store();
        s.load("acct:alice", 1000);
        Txn t1 = s.begin(level), t2 = s.begin(level);
        Long[] r1 = new Long[1], r2 = new Long[1];
        quietly(() -> r1[0] = t1.read("acct:alice"));
        quietly(() -> r2[0] = t2.read("acct:alice"));
        quietly(() -> t1.write("acct:alice", r1[0] + 100));  // deposit 1.00
        quietly(t1::commit);
        quietly(() -> { if (r2[0] != null) t2.write("acct:alice", r2[0] + 200); });   // deposit 2.00
        quietly(t2::commit);
        long finalBalance = s.committed.get("acct:alice");
        boolean bothCommitted = t1.status == Txn.Status.COMMITTED && t2.status == Txn.Status.COMMITTED;
        boolean observed = bothCommitted && finalBalance != 1300;
        return new Outcome("lost update", level, observed, "final balance " + finalBalance + " (expected 1300 if both deposits landed); T1 " + t1.status + ", T2 " + t2.status, s.trace);
    }

    /** A5B: two transactions each check an invariant over two keys and each write a different key; the invariant breaks. */
    static Outcome writeSkew(Isolation level) {
        Store s = new Store();
        s.load("acct:alice:checking", 600);
        s.load("acct:alice:savings", 600);
        Txn t1 = s.begin(level), t2 = s.begin(level);
        long[] c = new long[2], sv = new long[2];
        quietly(() -> { c[0] = t1.read("acct:alice:checking"); sv[0] = t1.read("acct:alice:savings"); });   // T1 checks: 1200 >= 1000
        quietly(() -> { c[1] = t2.read("acct:alice:checking"); sv[1] = t2.read("acct:alice:savings"); });   // T2 checks: 1200 >= 1000
        quietly(() -> { if (c[0] + sv[0] - 1000 >= 0) t1.write("acct:alice:checking", c[0] - 1000); });   // withdraw 10.00 from checking
        quietly(() -> { if (c[1] + sv[1] - 1000 >= 0) t2.write("acct:alice:savings", sv[1] - 1000); });   // withdraw 10.00 from savings
        quietly(t1::commit);
        quietly(t2::commit);
        long sum = s.committed.get("acct:alice:checking") + s.committed.get("acct:alice:savings");
        boolean observed = sum < 0;
        return new Outcome("write skew", level, observed, "combined balance " + sum + " (invariant: >= 0); T1 " + t1.status + ", T2 " + t2.status, s.trace);
    }

    /** anomaly -> level -> observed? */
    static Map<String, Map<Isolation, Boolean>> matrix() {
        Map<String, Map<Isolation, Boolean>> m = new LinkedHashMap<>();
        for (String a : ANOMALIES) {
            Map<Isolation, Boolean> row = new LinkedHashMap<>();
            for (Isolation l : Isolation.values()) row.put(l, run(a, l).observed());
            m.put(a, row);
        }
        return m;
    }
}

public final class Main {
    public static void main(String[] args) {
        System.out.println("== Which anomalies does each level allow? (Y = anomaly observed) ==");
        System.out.printf("%-20s %-18s %-16s %-16s %-10s %-12s%n", "anomaly", "READ_UNCOMMITTED", "READ_COMMITTED", "REPEATABLE_READ", "SNAPSHOT", "SERIALIZABLE");
        for (Map.Entry<String, Map<Isolation, Boolean>> row : AnomalyLab.matrix().entrySet()) {
            StringBuilder line = new StringBuilder(String.format("%-20s ", row.getKey()));
            int[] widths = {18, 16, 16, 10, 12};
            int i = 0;
            for (Boolean b : row.getValue().values()) line.append(String.format("%-" + widths[i++] + "s ", b ? "Y" : "-"));
            System.out.println(line.toString().stripTrailing());
        }

        System.out.println();
        System.out.println("== Write skew under SNAPSHOT: both pass the check, both commit, the invariant is gone ==");
        AnomalyLab.Outcome skew = AnomalyLab.writeSkew(Isolation.SNAPSHOT);
        skew.trace().forEach(System.out::println);
        System.out.println("  => " + skew.detail());

        System.out.println();
        System.out.println("== Same interleaving under SERIALIZABLE: the second writer is refused ==");
        AnomalyLab.Outcome fixed = AnomalyLab.writeSkew(Isolation.SERIALIZABLE);
        fixed.trace().forEach(System.out::println);
        System.out.println("  => " + fixed.detail());

        System.out.println();
        System.out.println("== Phantom under REPEATABLE_READ (item locks cannot lock a row that does not exist yet) ==");
        AnomalyLab.Outcome ph = AnomalyLab.phantom(Isolation.REPEATABLE_READ);
        ph.trace().forEach(System.out::println);
        System.out.println("  => " + ph.detail());

        System.out.println();
        System.out.println("== Lost update under SNAPSHOT: first committer wins, second is told ==");
        AnomalyLab.Outcome lu = AnomalyLab.lostUpdate(Isolation.SNAPSHOT);
        lu.trace().forEach(System.out::println);
        System.out.println("  => " + lu.detail());
    }
}
