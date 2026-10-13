// Tests for phases/07-storage-and-transactions/11-change-data-capture. Run: javac -d .out *.java && java -cp .out MainTest
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

    static Database seeded() {
        Database db = new Database();
        db.begin().put("accounts", "acc_alice", Main.account(10_000, 1)).put("accounts", "acc_bob", Main.account(5_000, 1)).commit();
        Main.transfer(db, "pay_001", "acc_alice", "acc_bob", 2_500);
        return db;
    }

    static List<Long> lsns(List<ChangeEvent> events) {
        List<Long> out = new ArrayList<>();
        for (ChangeEvent e : events) out.add(e.lsn());
        return out;
    }

    public static void main(String[] args) {
        FakeClock clock = new FakeClock(1_000);

        // --- commit log: LSNs, atomicity, before/after images
        Database db = seeded();
        List<LogRecord> all = db.log().readFrom(0, 100);
        check("two transactions produce five records", all.size() == 5);
        check("LSNs are 1..5 in order", all.get(0).lsn() == 1 && all.get(4).lsn() == 5);
        boolean increasing = true;
        for (int i = 1; i < all.size(); i++) increasing &= all.get(i).lsn() == all.get(i - 1).lsn() + 1;
        check("LSNs strictly increase by one", increasing);
        check("records of one transaction share a txId", all.get(2).txId() == all.get(3).txId() && all.get(3).txId() == all.get(4).txId());
        check("insert has null before", all.get(0).op() == Op.INSERT && all.get(0).before() == null);
        check("update carries before and after images",
                all.get(3).op() == Op.UPDATE && all.get(3).before().get("balance_minor").equals("10000") && all.get(3).after().get("balance_minor").equals("7500"));
        check("headLsn is the last record", db.log().headLsn() == 5);
        check("readFrom is exclusive of the offset", db.log().readFrom(3, 100).get(0).lsn() == 4);
        check("readFrom honours max", db.log().readFrom(0, 2).size() == 2);
        check("readFrom past head is empty", db.log().readFrom(5, 100).isEmpty());

        Transaction rolled = db.begin().put("accounts", "acc_carol", Main.account(1, 1));
        rolled.rollback();
        check("rollback writes nothing to the log", db.log().headLsn() == 5 && db.read("accounts", "acc_carol") == null);
        checkThrows("finished transaction cannot commit", IllegalStateException.class, rolled::commit);
        long none = db.begin().delete("accounts", "acc_nobody").commit();
        check("deleting a missing row logs nothing", none == 0 && db.log().headLsn() == 5);
        db.begin().delete("accounts", "acc_bob").commit();
        LogRecord del = db.log().readFrom(5, 1).get(0);
        check("delete has before image and null after", del.op() == Op.DELETE && del.after() == null && del.before().get("balance_minor").equals("7500"));
        checkThrows("log records are immutable", UnsupportedOperationException.class, () -> all.get(3).after().put("x", "y"));

        // --- envelope mapping
        ChangeEvent c = ChangeEvent.fromLog(all.get(0), 7);
        ChangeEvent u = ChangeEvent.fromLog(all.get(3), 7);
        ChangeEvent d = ChangeEvent.fromLog(del, 7);
        check("INSERT maps to op c", c.op().equals("c") && !c.snapshot());
        check("UPDATE maps to op u", u.op().equals("u"));
        check("DELETE maps to op d", d.op().equals("d") && d.after() == null);
        check("event keeps lsn, txId and timestamp", u.lsn() == 4 && u.txId() == 2 && u.tsMillis() == 7);
        ChangeEvent r = ChangeEvent.snapshotRow("accounts", "acc_x", Main.account(1, 1), 42, 7);
        check("snapshot row is op r at the snapshot lsn", r.op().equals("r") && r.snapshot() && r.lsn() == 42 && r.before() == null);

        // --- connector: offsets commit after apply; order preserved
        Database db2 = seeded();
        OffsetStore offsets = new OffsetStore();
        RecordingSink rec = new RecordingSink();
        CdcConnector conn = new CdcConnector("rec", db2, offsets, rec, clock);
        check("fresh consumer starts at offset 0", conn.offset() == 0);
        check("poll delivers in LSN order", conn.poll(3) == 3 && lsns(rec.events).equals(List.of(1L, 2L, 3L)));
        check("offset advances to last delivered lsn", conn.offset() == 3 && offsets.commits == 1);
        check("second poll continues from the offset", conn.poll(100) == 2 && lsns(rec.events).equals(List.of(1L, 2L, 3L, 4L, 5L)));
        check("poll with nothing new delivers nothing and commits nothing", conn.poll(100) == 0 && offsets.commits == 2);

        // --- at-least-once: crash before commit redelivers
        Database db3 = seeded();
        OffsetStore off3 = new OffsetStore();
        RecordingSink rec3 = new RecordingSink();
        CdcConnector crashy = new CdcConnector("crashy", db3, off3, rec3, clock);
        crashy.poll(3, true);
        check("crash before commit leaves offset at 0", crashy.offset() == 0 && rec3.events.size() == 3);
        crashy.poll(100);
        check("restart redelivers the uncommitted batch", lsns(rec3.events).equals(List.of(1L, 2L, 3L, 1L, 2L, 3L, 4L, 5L)));

        NaiveTotalsSink naive = new NaiveTotalsSink();
        IdempotentTotalsSink exact = new IdempotentTotalsSink();
        CdcConnector naiveConn = new CdcConnector("naive", db3, new OffsetStore(), naive, clock);
        CdcConnector exactConn = new CdcConnector("exact", db3, new OffsetStore(), exact, clock);
        naiveConn.poll(5, true); naiveConn.poll(100);
        exactConn.poll(5, true); exactConn.poll(100);
        check("naive sink double counts after redelivery", naive.paymentsSeen == 2 && naive.total.minorUnits() == 5_000);
        check("idempotent sink counts each payment once", exact.paymentsSeen() == 1 && exact.total.minorUnits() == 2_500);

        // --- snapshot + stream bootstrap, with writes landing between them
        Database db4 = seeded();
        OffsetStore off4 = new OffsetStore();
        BalanceViewSink view = new BalanceViewSink();
        CdcConnector boot = new CdcConnector("view", db4, off4, view, clock);
        long snap = boot.snapshot("accounts");
        check("snapshot lsn is the log head at snapshot time", snap == 5);
        check("snapshot delivers one r event per row", view.applied == 2 && view.balance("acc_alice").minorUnits() == 7_500);
        check("snapshot stores the snapshot lsn as the offset", boot.offset() == 5);
        Main.transfer(db4, "pay_002", "acc_bob", "acc_alice", 1_000);
        check("stream resumes exactly after the snapshot lsn", boot.poll(100) == 3 && boot.offset() == 8);
        check("view equals the database after catch-up",
                view.balance("acc_alice").minorUnits() == 8_500 && view.balance("acc_bob").minorUnits() == 6_500);

        // --- idempotent view skips redelivered and overlapping events by LSN
        Main.transfer(db4, "pay_003", "acc_alice", "acc_bob", 300);
        boot.poll(3, true);
        int appliedBefore = view.applied;
        boot.poll(100);
        check("replayed account events are skipped by lsn", view.applied == appliedBefore && view.skipped == 2);
        check("view still correct after replay", view.balance("acc_alice").minorUnits() == 8_200 && view.balance("acc_bob").minorUnits() == 6_800);
        view.apply(ChangeEvent.snapshotRow("accounts", "acc_alice", Main.account(1, 1), 5, 0));
        check("a stale snapshot row for an already-advanced key is ignored", view.balance("acc_alice").minorUnits() == 8_200);
        view.apply(ChangeEvent.fromLog(new LogRecord(999, 50, Op.DELETE, "accounts", "acc_bob", Main.account(1, 1), null), 0));
        check("delete removes the key from the view", view.balance("acc_bob") == null && view.size() == 1);
        view.apply(ChangeEvent.fromLog(new LogRecord(1, 1, Op.INSERT, "payments", "pay_x", null, Main.payment("a", "b", 1)), 0));
        check("view ignores other tables", view.size() == 1);

        // --- retention: truncated log forces a re-snapshot
        Database db5 = seeded();
        check("truncateBefore removes old records", db5.log().truncateBefore(4) == 3 && db5.log().oldestLsn() == 4 && db5.log().size() == 2);
        check("reading at the retention boundary works", db5.log().readFrom(3, 100).size() == 2);
        checkThrows("reading before the retention boundary fails", LogTruncatedException.class, () -> db5.log().readFrom(0, 100));
        CdcConnector lateConn = new CdcConnector("late", db5, new OffsetStore(), new RecordingSink(), clock);
        checkThrows("a consumer whose offset was truncated cannot poll", LogTruncatedException.class, () -> lateConn.poll(100));
        BalanceViewSink lateView = new BalanceViewSink();
        CdcConnector recovered = new CdcConnector("late2", db5, new OffsetStore(), lateView, clock);
        recovered.snapshot("accounts");
        check("re-snapshot recovers the full state", recovered.poll(100) == 0 && lateView.balance("acc_alice").minorUnits() == 7_500 && lateView.balance("acc_bob").minorUnits() == 7_500);

        // --- money discipline
        check("Money renders minor units", Money.ofMinor(8_200, Currency.USD).toString().equals("82.00 USD"));
        checkThrows("Money refuses mixed currencies", IllegalArgumentException.class,
                () -> Money.ofMinor(1, Currency.USD).plus(Money.ofMinor(1, new Currency("EUR", 2))));

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }
}
