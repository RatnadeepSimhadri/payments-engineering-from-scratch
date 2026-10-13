// Lesson: phases/07-storage-and-transactions/06-mvcc-and-snapshot-isolation/docs/en.md
// MVCC and Snapshot Isolation: a multi-version store with snapshot reads, first-committer-wins, and vacuum
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: Berenson et al., "A Critique of ANSI SQL Isolation Levels" (SIGMOD 1995); PostgreSQL docs ch. 13
//          (Concurrency Control) and ch. 25 (Routine Vacuuming). Java 21, standard library only.

import java.util.ArrayList;
import java.util.Collections;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.Objects;
import java.util.Optional;
import java.util.TreeMap;

/** ISO 4217 currency: code plus the number of decimal places in its minor unit (Phase 10, Lesson 01). */
record Currency(String code, int exponent) {
    static final Currency USD = new Currency("USD", 2);

    Currency {
        if (code == null || code.length() != 3) throw new IllegalArgumentException("currency code must be three letters");
        if (exponent < 0 || exponent > 4) throw new IllegalArgumentException("exponent must be 0..4");
    }
}

/** An exact count of minor units in one currency. Never a float. */
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
        if (!currency.equals(other.currency)) throw new IllegalArgumentException("currency mismatch: " + currency.code() + " vs " + other.currency.code());
    }

    @Override public String toString() {
        long scale = 1;
        for (int i = 0; i < currency.exponent(); i++) scale *= 10;
        long abs = Math.abs(minorUnits);
        String frac = currency.exponent() == 0 ? "" : "." + String.format("%0" + currency.exponent() + "d", abs % scale);
        return (minorUnits < 0 ? "-" : "") + (abs / scale) + frac + " " + currency.code();
    }
}

/** Hands out strictly increasing logical timestamps. One per commit; snapshots read the current value. */
final class TimestampOracle {
    private long last = 0;

    /** The most recent timestamp handed out. A transaction beginning now sees everything committed at or before it. */
    long current() { return last; }

    /** A fresh timestamp, larger than every one handed out so far. Used as a commit timestamp. */
    long next() { return ++last; }
}

enum TxnStatus { ACTIVE, COMMITTED, ABORTED }

/** Thrown at commit when another transaction committed a newer version of a key this one wrote: first committer wins. */
final class WriteConflictException extends RuntimeException {
    WriteConflictException(String message) { super(message); }
}

/** One immutable version of a key. value == null is a tombstone (the key was deleted at commitTs). */
record Version<V>(long commitTs, long txnId, V value) {
    boolean isTombstone() { return value == null; }
}

/** A transaction: its snapshot, its private write set, and its outcome. */
final class Transaction<V> {
    final long id;
    final long startTs;
    final Map<String, V> writes = new LinkedHashMap<>();   // null value = delete
    TxnStatus status = TxnStatus.ACTIVE;
    long commitTs = -1;

    Transaction(long id, long startTs) { this.id = id; this.startTs = startTs; }

    boolean isActive() { return status == TxnStatus.ACTIVE; }

    @Override public String toString() { return "T" + id + "@" + startTs; }
}

/**
 * A multi-version key-value store under snapshot isolation.
 *
 * Invariants:
 *  - A transaction with startTs S sees, for each key, the newest version with commitTs <= S (plus its own writes).
 *  - Writes are buffered privately and become versions only at commit, stamped with a fresh commit timestamp.
 *  - First committer wins: commit fails if any written key has a version with commitTs > S.
 *  - Readers never block writers and writers never block readers; nothing in this class waits.
 */
final class MvccStore<V> {
    private final TimestampOracle oracle;
    private final Map<String, List<Version<V>>> versions = new TreeMap<>();   // per key, ascending commitTs
    private final Map<Long, Transaction<V>> active = new LinkedHashMap<>();
    private long nextTxnId = 1;
    private long commits = 0;
    private long aborts = 0;

    MvccStore(TimestampOracle oracle) { this.oracle = oracle; }

    /** Begin a transaction whose snapshot is "everything committed so far". */
    Transaction<V> begin() {
        Transaction<V> txn = new Transaction<>(nextTxnId++, oracle.current());
        active.put(txn.id, txn);
        return txn;
    }

    /** Snapshot read: own uncommitted write first, otherwise the newest version committed at or before startTs. */
    Optional<V> read(Transaction<V> txn, String key) {
        requireActive(txn);
        if (txn.writes.containsKey(key)) return Optional.ofNullable(txn.writes.get(key));
        Version<V> v = visibleVersion(key, txn.startTs);
        return v == null || v.isTombstone() ? Optional.empty() : Optional.of(v.value());
    }

    /** Buffer a write. Nothing is visible to anyone else until commit. */
    void write(Transaction<V> txn, String key, V value) {
        requireActive(txn);
        txn.writes.put(key, Objects.requireNonNull(value, "use delete() for tombstones"));
    }

    void delete(Transaction<V> txn, String key) {
        requireActive(txn);
        txn.writes.put(key, null);
    }

    /**
     * Validate then publish. For each written key, if a version newer than our snapshot exists, somebody committed
     * first and we lose (first committer wins). Otherwise every write becomes a version at one fresh commitTs.
     */
    long commit(Transaction<V> txn) {
        requireActive(txn);
        for (String key : txn.writes.keySet()) {
            Version<V> newest = newestVersion(key);
            if (newest != null && newest.commitTs() > txn.startTs) {
                abort(txn);
                throw new WriteConflictException(txn + " lost " + key + " to T" + newest.txnId() + " (committed at " + newest.commitTs() + " > snapshot " + txn.startTs + ")");
            }
        }
        long ts = txn.writes.isEmpty() ? txn.startTs : oracle.next();   // read-only transactions consume no timestamp
        for (Map.Entry<String, V> e : txn.writes.entrySet()) {
            versions.computeIfAbsent(e.getKey(), k -> new ArrayList<>()).add(new Version<>(ts, txn.id, e.getValue()));
        }
        txn.status = TxnStatus.COMMITTED;
        txn.commitTs = ts;
        active.remove(txn.id);
        commits++;
        return ts;
    }

    void abort(Transaction<V> txn) {
        if (!txn.isActive()) return;
        txn.status = TxnStatus.ABORTED;
        txn.writes.clear();
        active.remove(txn.id);
        aborts++;
    }

    /**
     * Remove versions no snapshot can see any more. The horizon is the oldest active snapshot (or "now" if none).
     * A version is dead when a newer version of the same key has commitTs <= horizon: every current and future
     * reader sees that newer one or something later. A tombstone that is itself the newest version at or below the
     * horizon takes the whole key with it. Returns the number of versions removed.
     */
    int vacuum() {
        long horizon = oldestActiveSnapshot();
        int removed = 0;
        List<String> emptyKeys = new ArrayList<>();
        for (Map.Entry<String, List<Version<V>>> e : versions.entrySet()) {
            List<Version<V>> chain = e.getValue();
            int keepFrom = 0;   // index of the newest version with commitTs <= horizon; everything before it is dead
            for (int i = 0; i < chain.size(); i++) if (chain.get(i).commitTs() <= horizon) keepFrom = i;
            if (keepFrom > 0) {
                removed += keepFrom;
                chain.subList(0, keepFrom).clear();
            }
            Version<V> oldestKept = chain.get(0);
            if (oldestKept.isTombstone() && oldestKept.commitTs() <= horizon) {
                chain.remove(0);
                removed++;
            }
            if (chain.isEmpty()) emptyKeys.add(e.getKey());
        }
        emptyKeys.forEach(versions::remove);
        return removed;
    }

    long oldestActiveSnapshot() {
        long horizon = oracle.current();
        for (Transaction<V> t : active.values()) horizon = Math.min(horizon, t.startTs);
        return horizon;
    }

    int versionCount(String key) { return versions.getOrDefault(key, List.of()).size(); }

    int totalVersions() { return versions.values().stream().mapToInt(List::size).sum(); }

    int activeCount() { return active.size(); }

    long commits() { return commits; }

    long aborts() { return aborts; }

    List<Version<V>> history(String key) { return Collections.unmodifiableList(versions.getOrDefault(key, List.of())); }

    private Version<V> visibleVersion(String key, long snapshotTs) {
        List<Version<V>> chain = versions.get(key);
        if (chain == null) return null;
        for (int i = chain.size() - 1; i >= 0; i--) {
            if (chain.get(i).commitTs() <= snapshotTs) return chain.get(i);
        }
        return null;
    }

    private Version<V> newestVersion(String key) {
        List<Version<V>> chain = versions.get(key);
        return chain == null || chain.isEmpty() ? null : chain.get(chain.size() - 1);
    }

    private void requireActive(Transaction<V> txn) {
        if (!txn.isActive()) throw new IllegalStateException(txn + " is " + txn.status);
    }
}

public final class Main {
    /** Parse "19.99" into 1999 cents without ever touching a double. Demo helper; the full parser is in Phase 10, Lesson 01. */
    static Money usd(String major) {
        boolean negative = major.startsWith("-");
        String[] parts = (negative ? major.substring(1) : major).split("\\.");
        long cents = Long.parseLong(parts[0]) * 100 + (parts.length > 1 ? Long.parseLong((parts[1] + "00").substring(0, 2)) : 0);
        return Money.ofMinor(negative ? -cents : cents, Currency.USD);
    }

    static Money balance(MvccStore<Money> store, Transaction<Money> t, String key) {
        return store.read(t, key).orElse(Money.ofMinor(0, Currency.USD));
    }

    /** Try to withdraw from `from` as long as the two joint accounts together stay at or above zero. */
    static boolean withdrawIfJointlyCovered(MvccStore<Money> store, Transaction<Money> t, String from, String other, Money amount) {
        Money total = balance(store, t, from).plus(balance(store, t, other));
        if (total.minus(amount).isNegative()) return false;
        store.write(t, from, balance(store, t, from).minus(amount));
        return true;
    }

    public static void main(String[] args) {
        TimestampOracle oracle = new TimestampOracle();
        MvccStore<Money> store = new MvccStore<>(oracle);

        Transaction<Money> setup = store.begin();
        store.write(setup, "acct:A", usd("100.00"));
        store.write(setup, "acct:B", usd("100.00"));
        store.commit(setup);

        System.out.println("== 1. Readers never block writers (and never see them) ==");
        Transaction<Money> reader = store.begin();
        System.out.println(reader + " reads A = " + balance(store, reader, "acct:A"));
        Transaction<Money> writer = store.begin();
        store.write(writer, "acct:A", usd("40.00"));
        long ts = store.commit(writer);
        System.out.println(writer + " wrote A = 40.00 and committed at ts " + ts + " without waiting for " + reader);
        System.out.println(reader + " reads A again = " + balance(store, reader, "acct:A") + "  (same snapshot, same answer)");
        Transaction<Money> later = store.begin();
        System.out.println(later + " (new snapshot) reads A = " + balance(store, later, "acct:A"));
        store.commit(reader);
        store.commit(later);

        System.out.println();
        System.out.println("== 2. Lost update: first committer wins, second aborts ==");
        Transaction<Money> t1 = store.begin();
        Transaction<Money> t2 = store.begin();
        store.write(t1, "acct:A", balance(store, t1, "acct:A").minus(usd("10.00")));
        store.write(t2, "acct:A", balance(store, t2, "acct:A").minus(usd("25.00")));
        store.commit(t1);
        Transaction<Money> peek = store.begin();
        System.out.println(t1 + " committed A = " + balance(store, peek, "acct:A"));
        store.commit(peek);
        try {
            store.commit(t2);
        } catch (WriteConflictException e) {
            System.out.println("rejected: " + e.getMessage());
        }

        System.out.println();
        System.out.println("== 3. Write skew: the anomaly snapshot isolation lets through ==");
        Transaction<Money> reset = store.begin();
        store.write(reset, "acct:A", usd("100.00"));
        store.write(reset, "acct:B", usd("100.00"));
        store.commit(reset);
        Transaction<Money> w1 = store.begin();
        Transaction<Money> w2 = store.begin();
        boolean ok1 = withdrawIfJointlyCovered(store, w1, "acct:A", "acct:B", usd("150.00"));
        boolean ok2 = withdrawIfJointlyCovered(store, w2, "acct:B", "acct:A", usd("150.00"));
        System.out.println(w1 + " checks A+B = 200.00 >= 150.00 -> " + ok1 + ", writes A = -50.00");
        System.out.println(w2 + " checks A+B = 200.00 >= 150.00 -> " + ok2 + ", writes B = -50.00");
        store.commit(w1);
        store.commit(w2);
        Transaction<Money> audit = store.begin();
        Money a = balance(store, audit, "acct:A"), b = balance(store, audit, "acct:B");
        System.out.println("both committed (disjoint write sets). A = " + a + ", B = " + b + ", A+B = " + a.plus(b) + "  <- constraint violated");
        store.commit(audit);

        System.out.println();
        System.out.println("== 4. Garbage collection: a long-running snapshot pins old versions ==");
        Transaction<Money> longRunner = store.begin();
        for (int i = 0; i < 5; i++) {
            Transaction<Money> t = store.begin();
            store.write(t, "acct:A", balance(store, t, "acct:A").plus(usd("1.00")));
            store.commit(t);
        }
        System.out.println("versions of A before vacuum: " + store.versionCount("acct:A") + ", total: " + store.totalVersions());
        int removed = store.vacuum();
        System.out.println("vacuum with " + longRunner + " still open removed " + removed + " versions (horizon = ts " + store.oldestActiveSnapshot() + ")");
        store.commit(longRunner);
        removed = store.vacuum();
        System.out.println("vacuum after it finished removed " + removed + "; versions of A now: " + store.versionCount("acct:A") + ", total: " + store.totalVersions());
        System.out.println("commits = " + store.commits() + ", aborts = " + store.aborts());
    }
}
