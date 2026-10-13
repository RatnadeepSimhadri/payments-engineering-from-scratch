// Lesson: phases/07-storage-and-transactions/11-change-data-capture/docs/en.md
// Change Data Capture: turn a database commit log into an ordered change stream with LSNs, consumer offsets,
// snapshot-then-stream bootstrap, and an idempotent sink.
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Java 21, standard library only. Event envelope follows the Debezium change-event shape (op c/u/d/r, before, after, source).

import java.util.ArrayList;
import java.util.Collections;
import java.util.HashMap;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.Objects;
import java.util.TreeMap;

/** ISO 4217 currency: three-letter code and minor-unit exponent. Copied from Phase 10, Lesson 01. */
record Currency(String code, int exponent) {
    static final Currency USD = new Currency("USD", 2);

    Currency {
        if (code == null || code.length() != 3 || !code.chars().allMatch(Character::isUpperCase)) {
            throw new IllegalArgumentException("currency code must be three uppercase letters: " + code);
        }
        if (exponent < 0 || exponent > 4) throw new IllegalArgumentException("exponent must be 0..4: " + exponent);
    }

    long minorUnitsPerMajor() {
        long scale = 1;
        for (int i = 0; i < exponent; i++) scale *= 10;
        return scale;
    }
}

/** Integer minor units plus a currency. Never a float. */
record Money(long minorUnits, Currency currency) {
    Money { Objects.requireNonNull(currency, "currency"); }

    static Money ofMinor(long minorUnits, Currency currency) { return new Money(minorUnits, currency); }

    Money plus(Money other) {
        if (!currency.equals(other.currency)) {
            throw new IllegalArgumentException("cannot combine " + currency.code() + " with " + other.currency.code());
        }
        return new Money(Math.addExact(minorUnits, other.minorUnits), currency);
    }

    Money minus(Money other) { return plus(new Money(Math.negateExact(other.minorUnits), currency)); }

    @Override public String toString() {
        long scale = currency.minorUnitsPerMajor();
        long abs = Math.abs(minorUnits);
        String whole = Long.toString(abs / scale);
        if (currency.exponent() == 0) return (minorUnits < 0 ? "-" : "") + whole + " " + currency.code();
        String frac = Long.toString(abs % scale);
        while (frac.length() < currency.exponent()) frac = "0" + frac;
        return (minorUnits < 0 ? "-" : "") + whole + "." + frac + " " + currency.code();
    }
}

/** Injected time source so demos and tests are deterministic. */
interface Clock { long nowMillis(); }

final class FakeClock implements Clock {
    private long now;
    FakeClock(long start) { now = start; }
    @Override public long nowMillis() { return now; }
    void advance(long millis) { now += millis; }
}

/** The kind of row change a log record describes. */
enum Op { INSERT, UPDATE, DELETE }

/**
 * One entry in the commit log. The LSN (log sequence number) is assigned at commit, is strictly increasing across
 * the whole log, and is the only ordering CDC relies on. {@code before} is null for INSERT, {@code after} is null
 * for DELETE. Column values are strings; an amount column carries integer minor units, never a decimal.
 */
record LogRecord(long lsn, long txId, Op op, String table, String key,
                 Map<String, String> before, Map<String, String> after) {
    LogRecord {
        Objects.requireNonNull(table);
        Objects.requireNonNull(key);
        before = before == null ? null : Collections.unmodifiableMap(new LinkedHashMap<>(before));
        after = after == null ? null : Collections.unmodifiableMap(new LinkedHashMap<>(after));
    }
}

/** Thrown when a reader asks for an LSN the log has already truncated: the reader must re-snapshot. */
final class LogTruncatedException extends RuntimeException {
    final long requested;
    final long oldestAvailable;
    LogTruncatedException(long requested, long oldestAvailable) {
        super("LSN " + requested + " has been truncated; oldest available is " + oldestAvailable);
        this.requested = requested;
        this.oldestAvailable = oldestAvailable;
    }
}

/**
 * Append-only commit log with retention. Records are appended in LSN order; {@code readFrom} returns records with
 * LSN strictly greater than the argument, which is the "offset" contract: an offset names the last LSN consumed.
 */
final class CommitLog {
    private final List<LogRecord> records = new ArrayList<>();
    private long truncatedBelow = 1;    // smallest LSN still present
    private long nextLsn = 1;

    long append(long txId, Op op, String table, String key, Map<String, String> before, Map<String, String> after) {
        LogRecord r = new LogRecord(nextLsn++, txId, op, table, key, before, after);
        records.add(r);
        return r.lsn();
    }

    /** LSN of the newest record, or 0 if the log is empty. */
    long headLsn() { return nextLsn - 1; }

    long oldestLsn() { return truncatedBelow; }

    int size() { return records.size(); }

    /** Records with lsn > afterLsn, oldest first, at most {@code max}. */
    List<LogRecord> readFrom(long afterLsn, int max) {
        if (afterLsn + 1 < truncatedBelow) throw new LogTruncatedException(afterLsn + 1, truncatedBelow);
        List<LogRecord> out = new ArrayList<>();
        for (LogRecord r : records) {
            if (r.lsn() > afterLsn) {
                out.add(r);
                if (out.size() >= max) break;
            }
        }
        return out;
    }

    /** Drop every record with lsn < keepFromLsn. Models WAL recycling / binlog purge. */
    int truncateBefore(long keepFromLsn) {
        int before = records.size();
        records.removeIf(r -> r.lsn() < keepFromLsn);
        truncatedBelow = Math.max(truncatedBelow, keepFromLsn);
        return before - records.size();
    }
}

/**
 * A toy transactional database: tables of string-column rows, a commit log, and transactions whose writes become
 * visible and logged atomically at commit. Single-threaded, so "atomically" means "within one method call".
 */
final class Database {
    private final Map<String, TreeMap<String, Map<String, String>>> tables = new TreeMap<>();
    private final CommitLog log = new CommitLog();
    private long nextTxId = 1;

    CommitLog log() { return log; }

    Map<String, String> read(String table, String key) {
        TreeMap<String, Map<String, String>> t = tables.get(table);
        return t == null ? null : t.get(key);
    }

    /** Snapshot of one table: a copy of every row, in key order. */
    Map<String, Map<String, String>> scan(String table) {
        TreeMap<String, Map<String, String>> t = tables.get(table);
        Map<String, Map<String, String>> out = new TreeMap<>();
        if (t != null) for (Map.Entry<String, Map<String, String>> e : t.entrySet()) out.put(e.getKey(), new LinkedHashMap<>(e.getValue()));
        return out;
    }

    Transaction begin() { return new Transaction(this, nextTxId++); }

    /** Called by Transaction.commit: applies the buffered writes to the tables and appends them to the log. */
    long commit(Transaction tx) {
        long lastLsn = 0;
        for (Transaction.Write w : tx.writes) {
            TreeMap<String, Map<String, String>> t = tables.computeIfAbsent(w.table(), k -> new TreeMap<>());
            Map<String, String> before = t.get(w.key());
            Op op;
            if (w.after() == null) {
                if (before == null) continue;           // deleting a missing row logs nothing
                t.remove(w.key());
                op = Op.DELETE;
            } else {
                t.put(w.key(), new LinkedHashMap<>(w.after()));
                op = before == null ? Op.INSERT : Op.UPDATE;
            }
            lastLsn = log.append(tx.txId, op, w.table(), w.key(), before, w.after());
        }
        return lastLsn;
    }
}

/** Buffers writes until commit. Reads inside the transaction see the database state, not the buffer (kept simple). */
final class Transaction {
    record Write(String table, String key, Map<String, String> after) {}

    private final Database db;
    final long txId;
    final List<Write> writes = new ArrayList<>();
    private boolean done = false;

    Transaction(Database db, long txId) { this.db = db; this.txId = txId; }

    Transaction put(String table, String key, Map<String, String> columns) {
        requireOpen();
        writes.add(new Write(table, key, new LinkedHashMap<>(columns)));
        return this;
    }

    Transaction delete(String table, String key) {
        requireOpen();
        writes.add(new Write(table, key, null));
        return this;
    }

    /** Returns the LSN of the last record this transaction wrote, or 0 if it wrote nothing. */
    long commit() {
        requireOpen();
        done = true;
        return db.commit(this);
    }

    void rollback() { requireOpen(); done = true; writes.clear(); }

    private void requireOpen() { if (done) throw new IllegalStateException("transaction " + txId + " is finished"); }
}

/**
 * Debezium-shaped change event. {@code op} is "c" (create), "u" (update), "d" (delete) or "r" (read, emitted by a
 * snapshot). {@code lsn} is the position in the source log; snapshot events carry the snapshot LSN.
 */
record ChangeEvent(String op, String table, String key, Map<String, String> before, Map<String, String> after,
                   long lsn, long txId, long tsMillis, boolean snapshot) {

    static ChangeEvent fromLog(LogRecord r, long tsMillis) {
        String op = switch (r.op()) { case INSERT -> "c"; case UPDATE -> "u"; case DELETE -> "d"; };
        return new ChangeEvent(op, r.table(), r.key(), r.before(), r.after(), r.lsn(), r.txId(), tsMillis, false);
    }

    static ChangeEvent snapshotRow(String table, String key, Map<String, String> row, long snapshotLsn, long tsMillis) {
        return new ChangeEvent("r", table, key, null, row, snapshotLsn, 0, tsMillis, true);
    }
}

/** Durable store of consumer offsets, one per consumer name. In production: Kafka Connect offsets topic, a slot, a file. */
final class OffsetStore {
    private final Map<String, Long> offsets = new HashMap<>();
    int commits = 0;

    long load(String consumer) { return offsets.getOrDefault(consumer, 0L); }

    void save(String consumer, long lsn) {
        offsets.put(consumer, lsn);
        commits++;
    }
}

/** Something that receives change events. Must tolerate seeing the same event more than once. */
interface Sink {
    void apply(ChangeEvent event);
}

/**
 * The CDC connector: reads the commit log from a stored offset, wraps records as change events, hands them to the
 * sink, and commits the offset only after the sink has applied the batch. A crash between apply and commit means
 * the batch is delivered again on restart: at-least-once.
 */
final class CdcConnector {
    final String name;
    private final Database db;
    private final OffsetStore offsets;
    private final Sink sink;
    private final Clock clock;
    long eventsDelivered = 0;

    CdcConnector(String name, Database db, OffsetStore offsets, Sink sink, Clock clock) {
        this.name = name; this.db = db; this.offsets = offsets; this.sink = sink; this.clock = clock;
    }

    long offset() { return offsets.load(name); }

    /**
     * Initial load: record the log head as the snapshot LSN, emit every current row as an "r" event, then store the
     * snapshot LSN as the offset so streaming resumes exactly where the snapshot was taken. Rows changed after the
     * snapshot LSN arrive again through the stream, which is why the sink must be idempotent.
     */
    long snapshot(String table) {
        long snapshotLsn = db.log().headLsn();
        for (Map.Entry<String, Map<String, String>> e : db.scan(table).entrySet()) {
            deliver(ChangeEvent.snapshotRow(table, e.getKey(), e.getValue(), snapshotLsn, clock.nowMillis()));
        }
        offsets.save(name, snapshotLsn);
        return snapshotLsn;
    }

    /** Stream one batch. Returns the number of events delivered. Commits the offset after the whole batch is applied. */
    int poll(int maxEvents) {
        return poll(maxEvents, false);
    }

    /** Same as poll, but if {@code crashBeforeCommit} the offset is not saved: simulates dying after apply. */
    int poll(int maxEvents, boolean crashBeforeCommit) {
        long from = offsets.load(name);
        List<LogRecord> batch = db.log().readFrom(from, maxEvents);
        if (batch.isEmpty()) return 0;
        long last = from;
        for (LogRecord r : batch) {
            deliver(ChangeEvent.fromLog(r, clock.nowMillis()));
            last = r.lsn();
        }
        if (!crashBeforeCommit) offsets.save(name, last);
        return batch.size();
    }

    private void deliver(ChangeEvent e) {
        sink.apply(e);
        eventsDelivered++;
    }
}

/**
 * Idempotent materialized view of the accounts table: balance per account, keyed by account id. Remembers the LSN
 * last applied per key and ignores any event at or below it, so redelivery and snapshot/stream overlap are harmless.
 */
final class BalanceViewSink implements Sink {
    private final Map<String, Money> balances = new TreeMap<>();
    private final Map<String, Long> appliedLsn = new HashMap<>();
    int applied = 0;
    int skipped = 0;

    @Override public void apply(ChangeEvent e) {
        if (!e.table().equals("accounts")) return;
        long seen = appliedLsn.getOrDefault(e.key(), 0L);
        if (e.lsn() <= seen) { skipped++; return; }
        if (e.op().equals("d")) {
            balances.remove(e.key());
        } else {
            balances.put(e.key(), Money.ofMinor(Long.parseLong(e.after().get("balance_minor")), Currency.USD));
        }
        appliedLsn.put(e.key(), e.lsn());
        applied++;
    }

    Money balance(String accountId) { return balances.get(accountId); }
    int size() { return balances.size(); }
    Map<String, Money> all() { return Collections.unmodifiableMap(balances); }
}

/** A sink that counts and sums payments as they arrive. Deliberately NOT idempotent: redelivery double-counts. */
final class NaiveTotalsSink implements Sink {
    int paymentsSeen = 0;
    Money total = Money.ofMinor(0, Currency.USD);

    @Override public void apply(ChangeEvent e) {
        if (!e.table().equals("payments") || !e.op().equals("c")) return;
        paymentsSeen++;
        total = total.plus(Money.ofMinor(Long.parseLong(e.after().get("amount_minor")), Currency.USD));
    }
}

/** Same totals, made idempotent by remembering which payment keys have been counted. */
final class IdempotentTotalsSink implements Sink {
    private final java.util.Set<String> counted = new java.util.HashSet<>();
    Money total = Money.ofMinor(0, Currency.USD);

    @Override public void apply(ChangeEvent e) {
        if (!e.table().equals("payments") || !(e.op().equals("c") || e.op().equals("r"))) return;
        if (!counted.add(e.key())) return;
        total = total.plus(Money.ofMinor(Long.parseLong(e.after().get("amount_minor")), Currency.USD));
    }

    int paymentsSeen() { return counted.size(); }
}

/** Records every event it sees, for assertions on order and envelope content. */
final class RecordingSink implements Sink {
    final List<ChangeEvent> events = new ArrayList<>();
    @Override public void apply(ChangeEvent e) { events.add(e); }
}

public final class Main {
    static Map<String, String> account(long balanceMinor, int version) {
        Map<String, String> m = new LinkedHashMap<>();
        m.put("balance_minor", Long.toString(balanceMinor));
        m.put("currency", "USD");
        m.put("version", Integer.toString(version));
        return m;
    }

    static Map<String, String> payment(String from, String to, long amountMinor) {
        Map<String, String> m = new LinkedHashMap<>();
        m.put("from", from);
        m.put("to", to);
        m.put("amount_minor", Long.toString(amountMinor));
        m.put("currency", "USD");
        return m;
    }

    /** Move money between two accounts in one transaction: one payments row, two account updates, one commit. */
    static long transfer(Database db, String paymentId, String from, String to, long amountMinor) {
        long fromBal = Long.parseLong(db.read("accounts", from).get("balance_minor"));
        long toBal = Long.parseLong(db.read("accounts", to).get("balance_minor"));
        int fromVer = Integer.parseInt(db.read("accounts", from).get("version"));
        int toVer = Integer.parseInt(db.read("accounts", to).get("version"));
        return db.begin()
                .put("payments", paymentId, payment(from, to, amountMinor))
                .put("accounts", from, account(fromBal - amountMinor, fromVer + 1))
                .put("accounts", to, account(toBal + amountMinor, toVer + 1))
                .commit();
    }

    static String describe(ChangeEvent e) {
        return String.format("lsn=%d tx=%d op=%s %s/%s before=%s after=%s", e.lsn(), e.txId(), e.op(), e.table(), e.key(),
                e.before() == null ? "null" : e.before().get("balance_minor") != null ? e.before().get("balance_minor") : e.before(),
                e.after() == null ? "null" : e.after().get("balance_minor") != null ? e.after().get("balance_minor") : e.after());
    }

    public static void main(String[] args) {
        FakeClock clock = new FakeClock(1_700_000_000_000L);
        Database db = new Database();

        System.out.println("== 1. Transactions become log records with strictly increasing LSNs ==");
        db.begin().put("accounts", "acc_alice", account(10_000, 1)).put("accounts", "acc_bob", account(5_000, 1)).commit();
        transfer(db, "pay_001", "acc_alice", "acc_bob", 2_500);
        for (LogRecord r : db.log().readFrom(0, 100)) {
            System.out.println("  " + describe(ChangeEvent.fromLog(r, clock.nowMillis())));
        }

        System.out.println();
        System.out.println("== 2. Snapshot, then stream from the snapshot LSN ==");
        OffsetStore offsets = new OffsetStore();
        BalanceViewSink view = new BalanceViewSink();
        CdcConnector connector = new CdcConnector("balance-view", db, offsets, view, clock);
        long snapLsn = connector.snapshot("accounts");
        System.out.printf("  snapshot at lsn=%d delivered %d rows; view: %s%n", snapLsn, view.applied, view.all());
        clock.advance(10);
        transfer(db, "pay_002", "acc_bob", "acc_alice", 1_000);          // committed after the snapshot
        int n = connector.poll(100);
        System.out.printf("  streamed %d events after lsn=%d; offset now %d; view: %s%n", n, snapLsn, connector.offset(), view.all());
        System.out.printf("  database says alice=%s bob=%s%n", db.read("accounts", "acc_alice").get("balance_minor"), db.read("accounts", "acc_bob").get("balance_minor"));

        System.out.println();
        System.out.println("== 3. Crash after apply, before offset commit: at-least-once redelivery ==");
        clock.advance(10);
        transfer(db, "pay_003", "acc_alice", "acc_bob", 300);
        transfer(db, "pay_004", "acc_alice", "acc_bob", 700);
        NaiveTotalsSink naive = new NaiveTotalsSink();
        IdempotentTotalsSink exact = new IdempotentTotalsSink();
        OffsetStore offsets2 = new OffsetStore();
        CdcConnector naiveConn = new CdcConnector("naive-totals", db, offsets2, naive, clock);
        CdcConnector exactConn = new CdcConnector("exact-totals", db, offsets2, exact, clock);
        naiveConn.poll(6, true);     // applies lsn 1..6, dies before saving the offset
        exactConn.poll(6, true);
        System.out.printf("  after crash: naive total=%s (%d payments), exact total=%s (%d payments), offsets still %d/%d%n",
                naive.total, naive.paymentsSeen, exact.total, exact.paymentsSeen(), naiveConn.offset(), exactConn.offset());
        naiveConn.poll(100);         // restart: replays 1..6 then continues
        exactConn.poll(100);
        System.out.printf("  after restart: naive total=%s (%d payments)  <- double counted%n", naive.total, naive.paymentsSeen);
        System.out.printf("                 exact total=%s (%d payments)  <- idempotent by payment key%n", exact.total, exact.paymentsSeen());
        System.out.printf("  true total of pay_001..pay_004 = %s%n", Money.ofMinor(2_500 + 1_000 + 300 + 700, Currency.USD));

        System.out.println();
        System.out.println("== 4. The view catches up and the redelivered events are skipped by LSN ==");
        n = connector.poll(100);
        System.out.printf("  streamed %d events; view applied=%d skipped=%d; view: %s%n", n, view.applied, view.skipped, view.all());
        transfer(db, "pay_005", "acc_bob", "acc_alice", 1_500);
        connector.poll(3, true);     // applies the three records of pay_005, dies before committing the offset
        int redelivered = connector.poll(100);
        System.out.printf("  crash/replay: %d events redelivered; view applied=%d skipped=%d; view: %s%n",
                redelivered, view.applied, view.skipped, view.all());

        System.out.println();
        System.out.println("== 5. Log retention: an offset older than the log forces a re-snapshot ==");
        OffsetStore lateOffsets = new OffsetStore();
        BalanceViewSink lateView = new BalanceViewSink();
        CdcConnector late = new CdcConnector("late-consumer", db, lateOffsets, lateView, clock);
        int dropped = db.log().truncateBefore(8);
        System.out.printf("  truncated %d records; oldest lsn now %d; late consumer offset is %d%n", dropped, db.log().oldestLsn(), late.offset());
        try {
            late.poll(100);
        } catch (LogTruncatedException e) {
            System.out.println("  poll failed: " + e.getMessage());
        }
        long resnap = late.snapshot("accounts");
        late.poll(100);
        System.out.printf("  re-snapshot at lsn=%d then stream; late view: %s%n", resnap, lateView.all());
        System.out.printf("  both views agree: %s%n", lateView.all().equals(view.all()));
    }
}
