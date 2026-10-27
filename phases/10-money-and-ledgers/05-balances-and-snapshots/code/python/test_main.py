"""Tests for phases/10-money-and-ledgers/05-balances-and-snapshots. Run: python3 -m unittest -v"""
import unittest

from main import (EUR, USD, AccountType, JavaRandom, JournalEntry, Ledger, LedgerError, Money, PaymentStream, Posting,
                  Snapshot)

T0 = 1_767_225_600


def three_accounts(interval: int) -> Ledger:
    ledger = Ledger(interval)
    ledger.open_account("1200", "Processor Receivable", AccountType.ASSET, USD)
    ledger.open_account("2100", "Merchant Payable", AccountType.LIABILITY, USD)
    ledger.open_account("4100", "Fee Revenue", AccountType.REVENUE, USD)
    return ledger


def sale(id: str, t: int, gross_minor: int, fee_minor: int) -> JournalEntry:
    gross, fee = Money.of_minor(gross_minor, USD), Money.of_minor(fee_minor, USD)
    return JournalEntry(id, t, [Posting.debit("1200", gross), Posting.credit("2100", gross.minus(fee)), Posting.credit("4100", fee)])


class SmallLedgerTest(unittest.TestCase):
    def setUp(self):
        self.l = three_accounts(2)
        self.l.post(sale("a", T0, 10_000, 290))
        self.l.post(sale("b", T0 + 10, 2_500, 72))
        self.l.post(sale("c", T0 + 20, 4_000, 116))

    def test_running_balance_and_normal_direction(self):
        l = self.l
        self.assertEqual(l.balance("1200").minor_units, 16_500)
        self.assertEqual(l.balance("2100").minor_units, 16_500 - 478)
        self.assertEqual(l.balance("4100").minor_units, 478)
        self.assertTrue(l.current_net_debit("1200").plus(l.current_net_debit("2100")).plus(l.current_net_debit("4100")).is_zero())

    def test_snapshot_schedule(self):
        l = self.l
        self.assertEqual([s.seq for s in l.snapshots()], [0, 2])
        self.assertEqual(l.nearest_snapshot(1).seq, 0)
        self.assertEqual(l.nearest_snapshot(3).seq, 2)
        self.assertEqual(l.take_snapshot().seq, 3)
        self.assertEqual(len(l.snapshots()), 3)
        self.assertEqual(l.take_snapshot().seq, 3)  # twice is a no-op
        self.assertEqual(len(l.snapshots()), 3)
        self.assertEqual(l.snapshot_net_debit("2100", 2).postings_scanned, 0)

    def test_three_read_paths_agree_at_every_seq(self):
        l = self.l
        for seq in range(l.head() + 1):
            for id in ("1200", "2100", "4100"):
                fold = l.fold_net_debit(id, seq).balance
                self.assertEqual(fold, l.snapshot_net_debit(id, seq).balance)
                self.assertEqual(fold, l.column_net_debit(id, seq).balance)
        self.assertTrue(l.column_net_debit("2100", 0).balance.is_zero())

    def test_time_based_queries(self):
        l = self.l
        self.assertEqual(l.seq_as_of(T0 - 1), 0)
        self.assertEqual(l.seq_as_of(T0 + 10), 2)
        self.assertEqual(l.seq_as_of(T0 + 15), 2)
        self.assertEqual(l.seq_as_of(T0 + 1_000_000), 3)
        self.assertEqual(l.balance_as_of("2100", T0 + 15).minor_units, 9_710 + 2_428)

    def test_equal_timestamps_allowed_backdating_rejected(self):
        l = self.l
        l.post(sale("d", T0 + 20, 1_999, 58))
        self.assertEqual(l.seq_as_of(T0 + 20), 4)
        with self.assertRaises(LedgerError):
            l.post(sale("late", T0, 100, 3))
        self.assertEqual(l.head(), 4)
        self.assertEqual(l.balance("4100").minor_units, 478 + 58)
        with self.assertRaises(LedgerError):
            l.post(JournalEntry("eur", T0 + 30, [
                Posting.debit("1200", Money.of_minor(100, EUR)), Posting.credit("2100", Money.of_minor(100, EUR))]))
        with self.assertRaises(LedgerError):
            JournalEntry("u", T0, [Posting.debit("1200", Money.of_minor(100, USD)), Posting.credit("2100", Money.of_minor(99, USD))])
        with self.assertRaises(LedgerError):
            l.snapshot_net_debit("2100", 99)
        with self.assertRaises(LedgerError):
            l.column_net_debit("2100", -1)

    def test_verification_catches_a_bad_snapshot(self):
        l = self.l
        l.take_snapshot()
        l.post(sale("d", T0 + 20, 1_999, 58))
        self.assertEqual(len(l.snapshots()), 4)
        for s in l.snapshots():
            self.assertTrue(l.verify(s), f"snapshot {s.seq} verifies")
        forged = Snapshot(2, T0 + 10, {"1200": 12_500, "2100": -12_138, "4100": -361})
        self.assertFalse(l.verify(forged))
        unknown_account = Snapshot(0, 0, {"9999": 0})
        self.assertFalse(l.verify(unknown_account))


class RandomizedPropertyTest(unittest.TestCase):
    def test_snapshot_plus_delta_equals_full_fold(self):
        stream = PaymentStream(11, 25, T0)
        big = Ledger(97)
        big.open_account("1200", "Processor Receivable", AccountType.ASSET, USD)
        for i in range(25):
            big.open_account(PaymentStream.merchant_id(i), "m", AccountType.LIABILITY, USD)
        for _ in range(5_000):
            big.post(stream.next())
        r = JavaRandom(3)
        prop = True
        max_scan = 0
        for _ in range(300):
            seq = r.next_int(big.head() + 1)
            id = "1200" if r.next_int(5) == 0 else PaymentStream.merchant_id(r.next_int(25))
            fold, snap, col = big.fold_net_debit(id, seq), big.snapshot_net_debit(id, seq), big.column_net_debit(id, seq)
            prop = prop and fold.balance == snap.balance and fold.balance == col.balance
            max_scan = max(max_scan, snap.postings_scanned)
        self.assertTrue(prop)
        self.assertLessEqual(max_scan, 96 * 2)
        self.assertTrue(all(big.verify(s) for s in big.snapshots()))
        self.assertEqual(len(big.snapshots()), 5_000 // 97 + 1)
        merchants_total = sum(big.balance(PaymentStream.merchant_id(i)).minor_units for i in range(25))
        self.assertEqual(big.balance("1200").minor_units, merchants_total)


class DeterminismTest(unittest.TestCase):
    def test_seeded_stream_is_deterministic(self):
        s1, s2 = PaymentStream(5, 10, T0), PaymentStream(5, 10, T0)
        self.assertTrue(all(s1.next() == s2.next() for _ in range(100)))

    def test_java_random_matches_jdk(self):
        # java.util.Random(42).nextInt(100) three times gives 30, 63, 48 on the JDK; the port must agree
        r = JavaRandom(42)
        self.assertEqual([r.next_int(100) for _ in range(3)], [30, 63, 48])

    def test_interval_must_be_positive(self):
        with self.assertRaises(ValueError):
            Ledger(0)


if __name__ == "__main__":
    unittest.main()
