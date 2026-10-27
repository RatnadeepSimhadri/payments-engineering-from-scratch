"""Tests for phases/14-payments-at-scale/05-batch-vs-real-time-settlement. Run: python3 -m unittest -v"""
import unittest
from datetime import date

from main import (
    CUTOFF, DAY, EUR, USD, BatchSettler, CurrencyMismatchError, EventType, FeeSchedule, JavaRandom, Ledger,
    LiquidityComparison, Merchant, Money, Platform, Posting, SettlementEvent, SettlementFile, UnbalancedJournalException,
    demo_day, demo_merchants, generated_day, generated_merchants, instant,
)

FEES = FeeSchedule.standard(USD)
T = instant("2026-03-31T10:00:00Z")


def usd(s: str) -> Money:
    return Money.parse(s, USD)


def demo_platform() -> Platform:
    p = Platform(USD, FeeSchedule.standard(USD))
    for m in demo_merchants():
        p.add_merchant(m)
    for e in demo_day():
        p.book(e)
    return p


class FeeTest(unittest.TestCase):
    def test_fees(self):
        self.assertEqual(FEES.fee_for(SettlementEvent("x", "m", EventType.CAPTURE, usd("45.50"), T)), usd("1.62"))
        self.assertEqual(FEES.fee_for(SettlementEvent("x", "m", EventType.CAPTURE, usd("1000.00"), T)), usd("29.30"))
        self.assertEqual(FEES.merchant_effect(SettlementEvent("x", "m", EventType.REFUND, usd("20.00"), T)), usd("-20.00"))
        self.assertEqual(FEES.merchant_effect(SettlementEvent("x", "m", EventType.CHARGEBACK, usd("60.00"), T)), usd("-75.00"))
        with self.assertRaises(ValueError):
            SettlementEvent("x", "m", EventType.REFUND, usd("-1.00"), T)


class BatchTest(unittest.TestCase):
    def setUp(self):
        self.p = demo_platform()
        self.s = BatchSettler(self.p, Money.zero(USD))
        self.b = self.s.run(DAY, CUTOFF)

    def test_netting(self):
        b, s, p = self.b, self.s, self.p
        self.assertEqual(len(b.instructions), 2)
        self.assertEqual(b.instructions[0].merchant_id, "m_acme")
        self.assertEqual(b.instructions[0].amount, usd("173.60"))
        self.assertEqual(b.instructions[1].amount, usd("1213.15"))
        self.assertEqual(b.total(), usd("1386.75"))
        self.assertEqual(b.carried_forward["m_bolt"], usd("-31.12"), "negative net is carried, not paid")
        self.assertEqual(b.processor_funding, usd("1415.50"))
        self.assertFalse(s.is_settled("c5"), "event after cutoff is not settled")
        self.assertTrue(s.is_settled("c4"))
        self.assertEqual(len(b.event_ids), 7)
        self.assertTrue(s.ties_out())
        self.assertEqual(p.ledger.trial_balance(), 0)

    def test_idempotent_rerun(self):
        journals = self.p.ledger.journal_count()
        again = self.s.run(DAY, CUTOFF)
        self.assertIs(again, self.b)
        self.assertEqual(self.p.ledger.journal_count(), journals)
        self.assertFalse(self.p.book(demo_day()[0]), "rebooking an event is a no-op")
        self.assertEqual(self.p.ledger.journal_count(), journals)

    def test_settlement_file(self):
        file = SettlementFile.render(self.b)
        self.assertIn("T|2|138675|0003200015", file)
        self.assertTrue(SettlementFile.validate(file))
        self.assertFalse(SettlementFile.validate(file.replace("|17360", "|17361")))

    def test_funding_then_payout_and_day_two(self):
        p, s, b = self.p, self.s, self.b
        p.receive_funding(b.batch_id, b.processor_funding)
        s.confirm_payouts(b)
        self.assertEqual(p.ledger.balance(Ledger.CASH), usd("28.75"), "cash after batch is fees minus bolt deficit")
        self.assertEqual(p.ledger.peak_prefunding(), Money.zero(USD), "batch needs no prefunding")
        self.assertEqual(p.ledger.balance(Ledger.PAYOUTS_IN_TRANSIT).minor_units, 0)
        p.book(SettlementEvent("c7", "m_bolt", EventType.CAPTURE, usd("100.00"), instant("2026-04-01T09:00:00Z")))
        day2 = s.run(date(2026, 4, 1), instant("2026-04-01T23:00:00Z"))
        self.assertEqual(len(day2.instructions), 2)
        self.assertEqual(day2.instructions[0].amount, usd("65.68"), "bolt day 2 = 96.80 - 31.12")
        self.assertEqual(day2.instructions[1].amount, usd("9.41"), "cora day 2 = c5 net 9.41")
        self.assertTrue(s.ties_out())
        self.assertEqual(s.carried("m_bolt").minor_units, 0)


class HoldsAndLedgerSafetyTest(unittest.TestCase):
    def setUp(self):
        self.h = Platform(USD, FEES)
        self.h.add_merchant(Merchant("m_hold", "021000021", "1", True))
        self.h.add_merchant(Merchant("m_tiny", "021000021", "2", False))
        self.h.book(SettlementEvent("h1", "m_hold", EventType.CAPTURE, usd("500.00"), T))
        self.h.book(SettlementEvent("h2", "m_tiny", EventType.CAPTURE, usd("1.00"), T))

    def test_holds_and_minimum(self):
        hs = BatchSettler(self.h, usd("5.00"))
        hb = hs.run(DAY, CUTOFF)
        self.assertEqual(hb.instructions, ())
        self.assertFalse(hs.is_settled("h1"), "merchant on hold is not paid and its events stay open")
        self.assertEqual(hs.carried("m_tiny"), usd("0.67"), "below-minimum net is carried positive")
        self.assertTrue(hs.ties_out())

    def test_ledger_safety(self):
        with self.assertRaises(UnbalancedJournalException):
            self.h.ledger.post("bad", [Posting("a", 100), Posting("b", -99)])
        with self.assertRaises(CurrencyMismatchError):
            self.h.book(SettlementEvent("e1", "m_tiny", EventType.CAPTURE, Money.parse("1.00", EUR), T))
        with self.assertRaises(ValueError):
            self.h.book(SettlementEvent("u1", "m_nobody", EventType.CAPTURE, usd("1.00"), T))
        with self.assertRaises(ValueError):
            Merchant("m_bad", "12345", "1", False)


class ComparisonTest(unittest.TestCase):
    def test_real_time_vs_batch(self):
        cmp = LiquidityComparison.run(demo_merchants(), demo_day(), DAY, CUTOFF, USD)
        self.assertEqual(cmp.real_time_transfers, 5, "one transfer per positive sweep")
        self.assertEqual(cmp.batch_transfers, 2)
        self.assertEqual(cmp.real_time_peak_prefunding, usd("1450.63"))
        self.assertEqual(cmp.real_time_exposure, usd("95.00"), "refund plus chargeback")
        self.assertEqual(cmp.real_time_cash_after, usd("-35.13"))
        self.assertEqual(cmp.batch_cash_after, usd("28.75"))
        self.assertEqual(cmp.batch_peak_prefunding.minor_units, 0)


class LargeSeededDayTest(unittest.TestCase):
    def test_large_batch(self):
        many = generated_merchants(50)
        big = Platform(USD, FEES)
        for m in many:
            big.add_merchant(m)
        for e in generated_day(many, 5_000, 11):
            big.book(e)
        bs = BatchSettler(big, usd("1.00"))
        bb = bs.run(DAY, instant("2026-04-01T00:00:00Z"))
        self.assertTrue(bs.ties_out())
        self.assertTrue(SettlementFile.validate(SettlementFile.render(bb)))
        self.assertLessEqual(len(bb.instructions), len(many))

    def test_java_random_sequence(self):
        """Values taken from `new java.util.Random(2026)` on the JDK; they pin the LCG that seeds the generated day."""
        r = JavaRandom(2026)
        self.assertEqual([r.next_int(66), r.next_int(100)], [29, 50])


if __name__ == "__main__":
    unittest.main()
