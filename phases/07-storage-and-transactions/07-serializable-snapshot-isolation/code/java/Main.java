// Lesson: phases/07-storage-and-transactions/07-serializable-snapshot-isolation/docs/en.md
// Serializable Snapshot Isolation: the SI store of Lesson 06 plus SIREAD locks, rw-antidependency tracking,
// and the dangerous-structure abort rule.
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: Cahill, Röhm, Fekete, "Serializable Isolation for Snapshot Databases" (SIGMOD 2008); Ports & Grittner,
//          "Serializable Snapshot Isolation in PostgreSQL" (VLDB 2012). Java 21, standard library only.

import java.util.ArrayList;
import java.util.HashSet;
import java.util.LinkedHashMap;
import java.util.LinkedHashSet;
import java.util.List;
import java.util.Map;
import java.util.Objects;
import java.util.Optional;
import java.util.Set;
import java.util.TreeMap;

/** ISO 4217 currency (Phase 10, Lesson 01). */
record Currency(String code, int exponent) {
    static final Currency USD = new Currency("USD", 2);

    Currency {
        if (code == null || code.length() != 3) throw new IllegalArgumentException("currency code must be three letters");
        if (exponent < 0 || exponent > 4) throw new IllegalArgumentException("exponent must be 0..4");
    }
}

/** Exact minor units in one currency. */
record Money(long minorUnits, Currency currency) {
    Money { Objects.requireNonNull(currency, "currency"); }

    static Money ofMinor(long minorUnits, Currency currency) { return new Money(minorUnits, currency); }

    Money plus(Money other) {
        requireSameCurrency(other);
        return new Money(Math.addExact(minorUnits, other.minorUnits), currency);
    }

    Money minus(Money other) {
        requireSameCurrency(other);
        return new Money(Math.subtractExact(minorUnits, other.minorUnits), currency);
    }

    boolean isNegative() { return minorUnits < 0; }

    private void requireSameCurrency(Money other) {
        if (!currency.equals(other.currency)) throw new IllegalArgumentException("currency mismatch");
    }

    @Override public String toString() {
        long scale = 1;
        for (int i = 0; i < currency.exponent(); i++) scale *= 10;
        long abs = Math.abs(minorUnits);
        String frac = currency.exponent() == 0 ? "" : "." + String.format("%0" + currency.exponent() + "d", abs % scale);
        return (minorUnits < 0 ? "-" : "") + (abs / scale) + frac + " " + currency.code();
    }
}

final class TimestampOracle {
    private long last = 0;
    long current() { return last; }
    long next() { return ++last; }
}

enum TxnStatus { ACTIVE, COMMITTED, ABORTED }

/** Lesson 06's rule: a newer version of a written key exists, first committer wins. */
final class WriteConflictException extends RuntimeException {
    WriteConflictException(String message) { super(message); }
}

/** This lesson's rule: the transaction is, or completes, a dangerous structure T_in -rw-> T -rw-> T_out. */
final class SerializationFailureException extends RuntimeException {
    SerializationFailureException(String message) { super(message); }
}

record Version<V>(long commitTs, long txnId, V value) {
    boolean isTombstone() { return value == null; }
}

/** An SI transaction plus the two conflict sets SSI needs. */
final class Transaction<V> {
    final long id;
    final long startTs;
    final Map<String, V> writes = new LinkedHashMap<>();
    /** Transactions T_in with T_in -rw-> this: they read a version this transaction overwrote. */
    final Set<Long> inConflicts = new LinkedHashSet<>();
    /** Transactions T_out with this -rw-> T_out: they wrote a newer version of something this transaction read. */
    final Set<Long> outConflicts = new LinkedHashSet<>();
    TxnStatus status = TxnStatus.ACTIVE;
    long commitTs = -1;
    /** Position in the store's event order: when this transaction began and when it finished. Two transactions are
     *  concurrent when neither finished before the other began. Commit timestamps cannot tell this, because a
     *  read-only commit consumes no timestamp. */
    final long beginSeq;
    long endSeq = Long.MAX_VALUE;
    String abortReason = null;

    Transaction(long id, long startTs, long beginSeq) { this.id = id; this.startTs = startTs; this.beginSeq = beginSeq; }

    boolean concurrentWith(Transaction<?> other) { return beginSeq < other.endSeq && other.beginSeq < endSeq; }

    boolean isActive() { return status == TxnStatus.ACTIVE; }
    boolean isCommitted() { return status == TxnStatus.COMMITTED; }
    boolean isPivot() { return !inConflicts.isEmpty() && !outConflicts.isEmpty(); }

    @Override public String toString() { return "T" + id; }
}

/**
 * Serializable snapshot isolation (Cahill et al. 2008) on top of the Lesson 06 store.
 *
 * Additions over SI:
 *  - Every read takes a SIREAD "lock" on the key: a record, never a block, kept after commit while any concurrent
 *    transaction is still active.
 *  - Every read of a key that has a newer version (committed, or buffered in an active writer) records T -rw-> W.
 *  - Every write of a key that a concurrent transaction read records R -rw-> T.
 *  - A transaction with both an incoming and an outgoing rw-antidependency is a pivot and is aborted at commit;
 *    a transaction that would attach a second edge to an already-committed pivot is aborted on the spot.
 */
final class SsiStore<V> {
    private final TimestampOracle oracle;
    private final Map<String, List<Version<V>>> versions = new TreeMap<>();
    private final Map<Long, Transaction<V>> txns = new LinkedHashMap<>();   // active and recently committed
    private final Map<String, Set<Long>> sireads = new TreeMap<>();          // key -> transactions that read it
    private long nextTxnId = 1;
    private long seq = 0;                                                      // event order of begin/commit/abort
    private long commits = 0;
    private long aborts = 0;

    SsiStore(TimestampOracle oracle) { this.oracle = oracle; }

    Transaction<V> begin() {
        Transaction<V> txn = new Transaction<>(nextTxnId++, oracle.current(), ++seq);
        txns.put(txn.id, txn);
        return txn;
    }

    Optional<V> read(Transaction<V> txn, String key) {
        requireActive(txn);
        sireads.computeIfAbsent(key, k -> new LinkedHashSet<>()).add(txn.id);
        // rw-antidependency: we read an older version than someone has written or is writing
        List<Version<V>> chain = versions.getOrDefault(key, List.of());
        for (Version<V> v : chain) {
            if (v.commitTs() > txn.startTs) recordRw(txn, txns.get(v.txnId()));
        }
        for (Transaction<V> other : new ArrayList<>(txns.values())) {   // copy: recordRw may abort and remove txn
            if (other != txn && other.isActive() && other.writes.containsKey(key)) recordRw(txn, other);
        }
        if (!txn.isActive()) throw new SerializationFailureException(txn.abortReason);
        if (txn.writes.containsKey(key)) return Optional.ofNullable(txn.writes.get(key));
        Version<V> v = visibleVersion(key, txn.startTs);
        return v == null || v.isTombstone() ? Optional.empty() : Optional.of(v.value());
    }

    void write(Transaction<V> txn, String key, V value) {
        requireActive(txn);
        txn.writes.put(key, Objects.requireNonNull(value, "use delete() for tombstones"));
        checkReadersOf(txn, key);
    }

    void delete(Transaction<V> txn, String key) {
        requireActive(txn);
        txn.writes.put(key, null);
        checkReadersOf(txn, key);
    }

    /** Readers of this key that are concurrent with the writer form R -rw-> txn. */
    private void checkReadersOf(Transaction<V> txn, String key) {
        for (Long readerId : new ArrayList<>(sireads.getOrDefault(key, Set.of()))) {
            Transaction<V> reader = txns.get(readerId);
            if (reader == null || reader == txn) continue;
            if (reader.concurrentWith(txn)) recordRw(reader, txn);
        }
        if (!txn.isActive()) throw new SerializationFailureException(txn.abortReason);
    }

    /**
     * Record reader -rw-> writer. If the other side is already committed and already has the opposite edge, it is a
     * committed pivot and the only transaction we can still abort is the one being executed now.
     */
    private void recordRw(Transaction<V> reader, Transaction<V> writer) {
        if (reader == null || writer == null || reader == writer) return;
        if (!reader.isActive() && !reader.isCommitted()) return;
        if (!writer.isActive() && !writer.isCommitted()) return;
        reader.outConflicts.add(writer.id);
        writer.inConflicts.add(reader.id);
        if (reader.isActive() && writer.isCommitted() && !writer.outConflicts.isEmpty()) {
            abort(reader, reader + " -rw-> " + writer + " -rw-> T" + writer.outConflicts.iterator().next() + ": " + writer + " is a committed pivot");
        } else if (writer.isActive() && reader.isCommitted() && !reader.inConflicts.isEmpty()) {
            abort(writer, "T" + reader.inConflicts.iterator().next() + " -rw-> " + reader + " -rw-> " + writer + ": " + reader + " is a committed pivot");
        }
    }

    long commit(Transaction<V> txn) {
        requireActive(txn);
        for (String key : txn.writes.keySet()) {
            Version<V> newest = newestVersion(key);
            if (newest != null && newest.commitTs() > txn.startTs) {
                abort(txn, "first committer wins on " + key);
                throw new WriteConflictException(txn.abortReason);
            }
        }
        if (txn.isPivot()) {
            abort(txn, "T" + txn.inConflicts.iterator().next() + " -rw-> " + txn + " -rw-> T" + txn.outConflicts.iterator().next() + ": " + txn + " is the pivot");
            throw new SerializationFailureException(txn.abortReason);
        }
        long ts = txn.writes.isEmpty() ? txn.startTs : oracle.next();
        for (Map.Entry<String, V> e : txn.writes.entrySet()) {
            versions.computeIfAbsent(e.getKey(), k -> new ArrayList<>()).add(new Version<>(ts, txn.id, e.getValue()));
        }
        txn.status = TxnStatus.COMMITTED;
        txn.commitTs = ts;
        txn.endSeq = ++seq;
        commits++;
        return ts;
    }

    /** Abort: drop the write set, drop our SIREADs, and remove our edges from everyone else's conflict sets. */
    void abort(Transaction<V> txn, String reason) {
        if (!txn.isActive()) return;
        txn.status = TxnStatus.ABORTED;
        txn.abortReason = reason;
        txn.endSeq = ++seq;
        txn.writes.clear();
        for (Set<Long> readers : sireads.values()) readers.remove(txn.id);
        for (Transaction<V> other : txns.values()) {
            other.inConflicts.remove(txn.id);
            other.outConflicts.remove(txn.id);
        }
        txns.remove(txn.id);
        aborts++;
    }

    /**
     * Forget committed transactions that are no longer concurrent with any active one: they finished before every
     * active transaction began. Their SIREAD locks can never be the "reader" side of a new edge, so they are released.
     * Returns how many were released.
     */
    int releaseFinished() {
        long oldestActiveBegin = seq + 1;
        for (Transaction<V> t : txns.values()) if (t.isActive()) oldestActiveBegin = Math.min(oldestActiveBegin, t.beginSeq);
        List<Long> done = new ArrayList<>();
        for (Transaction<V> t : txns.values()) if (t.isCommitted() && t.endSeq < oldestActiveBegin) done.add(t.id);
        for (Long id : done) {
            txns.remove(id);
            for (Set<Long> readers : sireads.values()) readers.remove(id);
        }
        sireads.values().removeIf(Set::isEmpty);
        return done.size();
    }

    long oldestActiveSnapshot() {
        long horizon = oracle.current();
        for (Transaction<V> t : txns.values()) if (t.isActive()) horizon = Math.min(horizon, t.startTs);
        return horizon;
    }

    int retainedReadLocks() { return sireads.values().stream().mapToInt(Set::size).sum(); }

    Set<Long> readersOf(String key) { return new HashSet<>(sireads.getOrDefault(key, Set.of())); }

    long commits() { return commits; }

    long aborts() { return aborts; }

    private Version<V> visibleVersion(String key, long snapshotTs) {
        List<Version<V>> chain = versions.get(key);
        if (chain == null) return null;
        for (int i = chain.size() - 1; i >= 0; i--) if (chain.get(i).commitTs() <= snapshotTs) return chain.get(i);
        return null;
    }

    private Version<V> newestVersion(String key) {
        List<Version<V>> chain = versions.get(key);
        return chain == null || chain.isEmpty() ? null : chain.get(chain.size() - 1);
    }

    private void requireActive(Transaction<V> txn) {
        if (!txn.isActive()) throw new IllegalStateException(txn + " is " + txn.status + (txn.abortReason == null ? "" : " (" + txn.abortReason + ")"));
    }
}

public final class Main {
    static Money usd(String major) {
        boolean negative = major.startsWith("-");
        String[] parts = (negative ? major.substring(1) : major).split("\\.");
        long cents = Long.parseLong(parts[0]) * 100 + (parts.length > 1 ? Long.parseLong((parts[1] + "00").substring(0, 2)) : 0);
        return Money.ofMinor(negative ? -cents : cents, Currency.USD);
    }

    static Money balance(SsiStore<Money> store, Transaction<Money> t, String key) {
        return store.read(t, key).orElse(Money.ofMinor(0, Currency.USD));
    }

    static boolean withdrawIfJointlyCovered(SsiStore<Money> store, Transaction<Money> t, String from, String other, Money amount) {
        Money total = balance(store, t, from).plus(balance(store, t, other));
        if (total.minus(amount).isNegative()) return false;
        store.write(t, from, balance(store, t, from).minus(amount));
        return true;
    }

    static String tryCommit(SsiStore<Money> store, Transaction<Money> t) {
        try {
            long ts = store.commit(t);
            return t + " committed at ts " + ts;
        } catch (SerializationFailureException | WriteConflictException e) {
            return t + " ABORTED: " + e.getMessage();
        }
    }

    public static void main(String[] args) {
        TimestampOracle oracle = new TimestampOracle();
        SsiStore<Money> store = new SsiStore<>(oracle);
        Transaction<Money> setup = store.begin();
        store.write(setup, "acct:A", usd("100.00"));
        store.write(setup, "acct:B", usd("100.00"));
        store.commit(setup);

        System.out.println("== 1. Write skew from Lesson 06, now under SSI ==");
        Transaction<Money> t1 = store.begin();
        Transaction<Money> t2 = store.begin();
        withdrawIfJointlyCovered(store, t1, "acct:A", "acct:B", usd("150.00"));
        System.out.println(t1 + " read A,B (SIREAD on both), wrote A = -50.00; readers of A = " + store.readersOf("acct:A"));
        withdrawIfJointlyCovered(store, t2, "acct:B", "acct:A", usd("150.00"));
        System.out.println(t2 + " read A,B, wrote B = -50.00");
        System.out.println("edges: " + t2 + " -rw-> " + t1 + " (" + t2 + " read A, " + t1 + " writes A); " + t1 + " -rw-> " + t2 + " (" + t1 + " read B, " + t2 + " writes B)");
        System.out.println("  " + t1 + " in=" + t1.inConflicts + " out=" + t1.outConflicts + " pivot=" + t1.isPivot());
        System.out.println("  " + t2 + " in=" + t2.inConflicts + " out=" + t2.outConflicts + " pivot=" + t2.isPivot());
        System.out.println(tryCommit(store, t1));
        System.out.println("after the abort, " + t2 + " in=" + t2.inConflicts + " out=" + t2.outConflicts);
        System.out.println(tryCommit(store, t2));
        Transaction<Money> audit = store.begin();
        Money a = balance(store, audit, "acct:A"), b = balance(store, audit, "acct:B");
        System.out.println("A = " + a + ", B = " + b + ", A+B = " + a.plus(b) + "  <- constraint holds");
        store.commit(audit);

        System.out.println();
        System.out.println("== 2. One rw-antidependency alone is harmless ==");
        Transaction<Money> reader = store.begin();
        Transaction<Money> writer = store.begin();
        System.out.println(reader + " reads A = " + balance(store, reader, "acct:A"));
        store.write(writer, "acct:A", usd("75.00"));
        System.out.println(writer + " writes A = 75.00; edge " + reader + " -rw-> " + writer);
        System.out.println(tryCommit(store, writer));
        System.out.println(tryCommit(store, reader) + "  (serializable order: " + reader + " before " + writer + ")");

        System.out.println();
        System.out.println("== 3. SIREAD locks outlive their transaction ==");
        Transaction<Money> early = store.begin();
        Transaction<Money> late = store.begin();
        System.out.println(early + " reads B = " + balance(store, early, "acct:B") + " and commits: " + tryCommit(store, early));
        System.out.println("readers of B still recorded: " + store.readersOf("acct:B") + " (retained locks = " + store.retainedReadLocks() + ")");
        store.write(late, "acct:B", usd("1.00"));
        System.out.println(late + " (started before " + early + " committed) writes B: edge " + early + " -rw-> " + late + "; " + late + " in=" + late.inConflicts);
        System.out.println(tryCommit(store, late) + "  (one edge, no pivot)");
        System.out.println("released " + store.releaseFinished() + " finished transactions; retained locks now " + store.retainedReadLocks());

        System.out.println();
        System.out.println("== 4. The pivot has already committed: abort the newcomer on the spot ==");
        Transaction<Money> p = store.begin();     // will be the pivot
        Transaction<Money> q = store.begin();     // concurrent with p
        balance(store, p, "acct:A");               // p reads A
        store.write(p, "acct:B", usd("5.00"));    // p writes B
        System.out.println(p + " reads A, writes B, commits: " + tryCommit(store, p));
        System.out.println(q + " reads B (older version than " + p + " wrote): edge " + q + " -rw-> " + p);
        balance(store, q, "acct:B");
        try {
            store.write(q, "acct:A", usd("6.00"));
            System.out.println(q + " wrote A");
        } catch (SerializationFailureException e) {
            System.out.println(q + " tries to write A, which " + p + " read: ABORTED immediately: " + e.getMessage());
        }
        System.out.println("commits = " + store.commits() + ", aborts = " + store.aborts());
    }
}
