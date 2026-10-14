"""Tests for phases/07-storage-and-transactions/11-change-data-capture. Run: python3 -m unittest -v"""
import unittest

from main import (
    BalanceViewSink,
    CdcConnector,
    ChangeEvent,
    Currency,
    Database,
    FakeClock,
    IdempotentTotalsSink,
    LogRecord,
    LogTruncatedException,
    Money,
    NaiveTotalsSink,
    OffsetStore,
    Op,
    RecordingSink,
    account,
    payment,
    transfer,
)

CLOCK = FakeClock(1_000)


def seeded() -> Database:
    db = Database()
    db.begin().put("accounts", "acc_alice", account(10_000, 1)).put("accounts", "acc_bob", account(5_000, 1)).commit()
    transfer(db, "pay_001", "acc_alice", "acc_bob", 2_500)
    return db


def lsns(events) -> list[int]:
    return [e.lsn for e in events]


class CommitLogTest(unittest.TestCase):
    def test_lsns_atomicity_and_images(self):
        db = seeded()
        all_ = db.log().read_from(0, 100)
        self.assertEqual(len(all_), 5)
        self.assertTrue(all_[0].lsn == 1 and all_[4].lsn == 5)
        self.assertTrue(all(all_[i].lsn == all_[i - 1].lsn + 1 for i in range(1, len(all_))))
        self.assertTrue(all_[2].tx_id == all_[3].tx_id == all_[4].tx_id)
        self.assertTrue(all_[0].op is Op.INSERT and all_[0].before is None)
        self.assertTrue(all_[3].op is Op.UPDATE and all_[3].before["balance_minor"] == "10000"
                        and all_[3].after["balance_minor"] == "7500")
        self.assertEqual(db.log().head_lsn(), 5)
        self.assertEqual(db.log().read_from(3, 100)[0].lsn, 4)
        self.assertEqual(len(db.log().read_from(0, 2)), 2)
        self.assertEqual(db.log().read_from(5, 100), [])

    def test_rollback_deletes_and_immutability(self):
        db = seeded()
        all_ = db.log().read_from(0, 100)
        rolled = db.begin().put("accounts", "acc_carol", account(1, 1))
        rolled.rollback()
        self.assertTrue(db.log().head_lsn() == 5 and db.read("accounts", "acc_carol") is None)
        with self.assertRaises(RuntimeError):
            rolled.commit()
        none = db.begin().delete("accounts", "acc_nobody").commit()
        self.assertTrue(none == 0 and db.log().head_lsn() == 5)
        db.begin().delete("accounts", "acc_bob").commit()
        d = db.log().read_from(5, 1)[0]
        self.assertTrue(d.op is Op.DELETE and d.after is None and d.before["balance_minor"] == "7500")
        with self.assertRaises(TypeError):  # Java: UnsupportedOperationException on an unmodifiable map
            all_[3].after["x"] = "y"


class EnvelopeTest(unittest.TestCase):
    def test_mapping(self):
        all_ = seeded().log().read_from(0, 100)
        del_db = seeded()
        del_db.begin().delete("accounts", "acc_bob").commit()
        dl = del_db.log().read_from(5, 1)[0]
        c = ChangeEvent.from_log(all_[0], 7)
        u = ChangeEvent.from_log(all_[3], 7)
        d = ChangeEvent.from_log(dl, 7)
        self.assertTrue(c.op == "c" and not c.snapshot)
        self.assertEqual(u.op, "u")
        self.assertTrue(d.op == "d" and d.after is None)
        self.assertTrue(u.lsn == 4 and u.tx_id == 2 and u.ts_millis == 7)
        r = ChangeEvent.snapshot_row("accounts", "acc_x", account(1, 1), 42, 7)
        self.assertTrue(r.op == "r" and r.snapshot and r.lsn == 42 and r.before is None)


class ConnectorTest(unittest.TestCase):
    def test_offsets_commit_after_apply(self):
        offsets = OffsetStore()
        rec = RecordingSink()
        conn = CdcConnector("rec", seeded(), offsets, rec, CLOCK)
        self.assertEqual(conn.offset(), 0)
        self.assertTrue(conn.poll(3) == 3 and lsns(rec.events) == [1, 2, 3])
        self.assertTrue(conn.offset() == 3 and offsets.commits == 1)
        self.assertTrue(conn.poll(100) == 2 and lsns(rec.events) == [1, 2, 3, 4, 5])
        self.assertTrue(conn.poll(100) == 0 and offsets.commits == 2)

    def test_at_least_once_and_idempotent_totals(self):
        db3 = seeded()
        rec3 = RecordingSink()
        crashy = CdcConnector("crashy", db3, OffsetStore(), rec3, CLOCK)
        crashy.poll(3, True)
        self.assertTrue(crashy.offset() == 0 and len(rec3.events) == 3)
        crashy.poll(100)
        self.assertEqual(lsns(rec3.events), [1, 2, 3, 1, 2, 3, 4, 5])

        naive = NaiveTotalsSink()
        exact = IdempotentTotalsSink()
        naive_conn = CdcConnector("naive", db3, OffsetStore(), naive, CLOCK)
        exact_conn = CdcConnector("exact", db3, OffsetStore(), exact, CLOCK)
        naive_conn.poll(5, True)
        naive_conn.poll(100)
        exact_conn.poll(5, True)
        exact_conn.poll(100)
        self.assertTrue(naive.payments_seen == 2 and naive.total.minor_units == 5_000)
        self.assertTrue(exact.payments_seen() == 1 and exact.total.minor_units == 2_500)


class SnapshotThenStreamTest(unittest.TestCase):
    def test_bootstrap_replay_and_lsn_guard(self):
        db4 = seeded()
        view = BalanceViewSink()
        boot = CdcConnector("view", db4, OffsetStore(), view, CLOCK)
        snap = boot.snapshot("accounts")
        self.assertEqual(snap, 5)
        self.assertTrue(view.applied == 2 and view.balance("acc_alice").minor_units == 7_500)
        self.assertEqual(boot.offset(), 5)
        transfer(db4, "pay_002", "acc_bob", "acc_alice", 1_000)
        self.assertTrue(boot.poll(100) == 3 and boot.offset() == 8)
        self.assertTrue(view.balance("acc_alice").minor_units == 8_500 and view.balance("acc_bob").minor_units == 6_500)

        transfer(db4, "pay_003", "acc_alice", "acc_bob", 300)
        boot.poll(3, True)
        applied_before = view.applied
        boot.poll(100)
        self.assertTrue(view.applied == applied_before and view.skipped == 2)
        self.assertTrue(view.balance("acc_alice").minor_units == 8_200 and view.balance("acc_bob").minor_units == 6_800)
        view.apply(ChangeEvent.snapshot_row("accounts", "acc_alice", account(1, 1), 5, 0))
        self.assertEqual(view.balance("acc_alice").minor_units, 8_200)
        view.apply(ChangeEvent.from_log(LogRecord(999, 50, Op.DELETE, "accounts", "acc_bob", account(1, 1), None), 0))
        self.assertTrue(view.balance("acc_bob") is None and view.size() == 1)
        view.apply(ChangeEvent.from_log(LogRecord(1, 1, Op.INSERT, "payments", "pay_x", None, payment("a", "b", 1)), 0))
        self.assertEqual(view.size(), 1)


class RetentionTest(unittest.TestCase):
    def test_truncated_log_forces_resnapshot(self):
        db5 = seeded()
        self.assertTrue(db5.log().truncate_before(4) == 3 and db5.log().oldest_lsn() == 4 and db5.log().size() == 2)
        self.assertEqual(len(db5.log().read_from(3, 100)), 2)
        with self.assertRaises(LogTruncatedException):
            db5.log().read_from(0, 100)
        late_conn = CdcConnector("late", db5, OffsetStore(), RecordingSink(), CLOCK)
        with self.assertRaises(LogTruncatedException) as ctx:
            late_conn.poll(100)
        self.assertTrue(ctx.exception.requested == 1 and ctx.exception.oldest_available == 4)
        late_view = BalanceViewSink()
        recovered = CdcConnector("late2", db5, OffsetStore(), late_view, CLOCK)
        recovered.snapshot("accounts")
        self.assertTrue(recovered.poll(100) == 0 and late_view.balance("acc_alice").minor_units == 7_500
                        and late_view.balance("acc_bob").minor_units == 7_500)


class MoneyTest(unittest.TestCase):
    def test_money_discipline(self):
        self.assertEqual(str(Money.of_minor(8_200, Currency.USD)), "82.00 USD")
        with self.assertRaises(ValueError):
            Money.of_minor(1, Currency.USD).plus(Money.of_minor(1, Currency("EUR", 2)))
        with self.assertRaises(OverflowError):  # Python-only: Math.addExact checked by hand
            Money.of_minor((1 << 63) - 1, Currency.USD).plus(Money.of_minor(1, Currency.USD))


if __name__ == "__main__":
    unittest.main()
