"""Tests for phases/10-money-and-ledgers/09-multi-currency-ledgers. Run: python3 -m unittest -v"""
import unittest

from main import (EUR, GBP, JPY, USD, AccountType, CurrencyMismatchError, FxRate, JournalEntry, LedgerError,
                  MixedCurrencyError, Money, MultiCurrencyLedger, Posting, Rounding, Transaction)


class JavaRandom:
    """java.util.Random's LCG, so the property test replays the same 500 exchanges as MainTest.java."""

    def __init__(self, seed: int) -> None:
        self._seed = (seed ^ 0x5DEECE66D) & ((1 << 48) - 1)

    def next(self, bits: int) -> int:
        self._seed = (self._seed * 0x5DEECE66D + 0xB) & ((1 << 48) - 1)
        r = self._seed >> (48 - bits)
        return r - (1 << 32) if r >= (1 << 31) else r

    def next_int(self, bound: int) -> int:
        r = self.next(31)
        m = bound - 1
        if (bound & m) == 0:
            return (bound * r) >> 31
        u = r
        r = u % bound
        while u - r + m >= (1 << 31):
            u = self.next(31)
            r = u % bound
        return r

    def next_boolean(self) -> bool:
        return self.next(1) != 0


def eur_usd(num: int) -> FxRate:
    return FxRate(EUR, USD, num, 10_000, "test", 0)


def ledger() -> MultiCurrencyLedger:
    l = MultiCurrencyLedger(USD)
    l.open_account("1200-USD", "Receivable", AccountType.ASSET, USD)
    l.open_account("1010-USD", "Bank", AccountType.ASSET, USD)
    l.open_account("1010-EUR", "Bank", AccountType.ASSET, EUR)
    l.open_account("2100-EUR", "Merchant Payable", AccountType.LIABILITY, EUR)
    l.open_account("1010-GBP", "Bank", AccountType.ASSET, GBP)
    return l


class FxRateTest(unittest.TestCase):
    def test_conversion_both_directions(self):
        self.assertEqual(eur_usd(11_000).convert(Money.parse("100.00", EUR), Rounding.HALF_EVEN), Money.parse("110.00", USD))
        self.assertEqual(eur_usd(11_000).convert(Money.parse("110.00", USD), Rounding.HALF_EVEN), Money.parse("100.00", EUR))
        self.assertEqual(eur_usd(11_000).convert(Money.parse("1.00", USD), Rounding.HALF_EVEN).minor_units, 91)

    def test_exponent_scaling(self):
        jpy = FxRate(USD, JPY, 15_725, 100, "t", 0)
        self.assertEqual(jpy.convert(Money.parse("10.00", USD), Rounding.HALF_EVEN), Money.parse("1572", JPY))
        self.assertEqual(jpy.convert(Money.parse("5000", JPY), Rounding.HALF_EVEN), Money.parse("31.80", USD))

    def test_rejections(self):
        with self.assertRaises(CurrencyMismatchError):
            eur_usd(11_000).convert(Money.parse("1.00", GBP), Rounding.HALF_EVEN)
        with self.assertRaises(ValueError):
            FxRate(USD, USD, 1, 1, "t", 0)
        with self.assertRaises(ValueError):
            FxRate(EUR, USD, 0, 1, "t", 0)


class SubLedgerDisciplineTest(unittest.TestCase):
    def test_no_mixed_currency_entries(self):
        with self.assertRaises(MixedCurrencyError):
            JournalEntry("x", USD, [Posting.debit("1200-USD", Money.parse("1.00", USD)), Posting.credit("2100-EUR", Money.parse("1.00", EUR))])
        l = ledger()
        with self.assertRaises(LedgerError):
            l.post(Transaction("t", "d", (JournalEntry.of("t/EUR", Posting.debit("1010-EUR", Money.parse("1.00", EUR)),
                                                          Posting.credit("1200-USD", Money.parse("1.00", EUR))),), None))
        self.assertEqual(l.sub_ledger(EUR).size(), 0)
        self.assertEqual(l.sub_ledger(USD).size(), 0)


class ExchangeTest(unittest.TestCase):
    def setUp(self):
        self.l = ledger()
        self.tx = self.l.exchange("fx-1", "pay", "1200-USD", Money.parse("110.00", USD), "2100-EUR", eur_usd(11_000))

    def test_exchange_through_clearing(self):
        l, tx = self.l, self.tx
        self.assertEqual(len(tx.entries), 2)
        self.assertEqual(tx.entries[0].currency, USD)
        self.assertEqual(tx.entries[1].currency, EUR)
        self.assertEqual(l.net_debit("1900-USD").minor_units, -11_000)
        self.assertEqual(l.net_debit("1900-EUR").minor_units, 10_000)
        self.assertTrue(l.all_sub_ledgers_balance())
        self.assertEqual(l.position(EUR).minor_units, -10_000)
        self.assertEqual(l.position_report(EUR).basis.minor_units, -11_000)
        self.assertTrue(l.clearing_matches_basis())

    def test_exchange_idempotency_and_rejections(self):
        l, tx = self.l, self.tx
        self.assertEqual(l.exchange("fx-1", "pay", "1200-USD", Money.parse("110.00", USD), "2100-EUR", eur_usd(11_000)), tx)
        self.assertEqual(l.sub_ledger(USD).size(), 1)
        self.assertEqual(l.position_report(EUR).basis.minor_units, -11_000)
        with self.assertRaises(LedgerError):
            l.exchange("fx-1", "pay", "1200-USD", Money.parse("120.00", USD), "2100-EUR", eur_usd(11_000))
        with self.assertRaises(LedgerError):
            l.exchange("fx-x", "eur->gbp", "1010-EUR", Money.parse("10.00", EUR), "1010-GBP", FxRate(EUR, GBP, 85, 100, "t", 0))
        with self.assertRaises(LedgerError):
            l.exchange("fx-y", "usd->usd", "1200-USD", Money.parse("1.00", USD), "1010-USD", eur_usd(11_000))


class RevaluationTest(unittest.TestCase):
    def setUp(self):
        self.l = ledger()
        self.l.exchange("fx-1", "pay", "1200-USD", Money.parse("110.00", USD), "2100-EUR", eur_usd(11_000))

    def test_revalue_short_position(self):
        l = self.l
        self.assertTrue(l.revalue(EUR, eur_usd(11_000)).is_zero())
        loss = l.revalue(EUR, eur_usd(11_200))
        self.assertEqual(loss.minor_units, -200)
        self.assertEqual(l.balance(MultiCurrencyLedger.UNREALIZED).minor_units, -200)
        self.assertEqual(l.net_debit("1950-EUR").minor_units, -200)
        self.assertEqual(l.sub_ledger(EUR).size(), 1)
        self.assertTrue(l.revalue(EUR, eur_usd(11_200)).is_zero())
        self.assertEqual(l.sub_ledger(USD).size(), 2)
        back = l.revalue(EUR, eur_usd(10_900))
        self.assertEqual(back.minor_units, 300)
        self.assertEqual(l.position_report(EUR).carrying_value().minor_units, -10_900)
        with self.assertRaises(LedgerError):
            l.revalue(USD, eur_usd(11_000))
        with self.assertRaises(LedgerError):
            l.realize(EUR)

    def test_realization(self):
        l = self.l
        l.revalue(EUR, eur_usd(11_200))
        l.revalue(EUR, eur_usd(10_900))
        l.post(Transaction("stl", "settle", (JournalEntry.of("stl/USD", Posting.debit("1010-USD", Money.parse("110.00", USD)),
                                                             Posting.credit("1200-USD", Money.parse("110.00", USD))),), None))
        l.exchange("fx-2", "buy EUR", "1010-EUR", Money.parse("100.00", EUR), "1010-USD", eur_usd(11_150))
        l.post(Transaction("out", "payout", (JournalEntry.of("out/EUR", Posting.debit("2100-EUR", Money.parse("100.00", EUR)),
                                                             Posting.credit("1010-EUR", Money.parse("100.00", EUR))),), None))
        self.assertTrue(l.position(EUR).is_zero())
        self.assertEqual(l.net_debit("1900-USD").minor_units, 150)
        self.assertTrue(l.clearing_matches_basis())
        realized = l.realize(EUR)
        self.assertEqual(realized.minor_units, -150)
        self.assertEqual(l.balance(MultiCurrencyLedger.REALIZED).minor_units, -150)
        self.assertTrue(l.balance(MultiCurrencyLedger.UNREALIZED).is_zero())
        self.assertTrue(l.net_debit("1950-EUR").is_zero())
        self.assertTrue(l.net_debit("1900-USD").is_zero())
        self.assertEqual(l.balance("1010-USD").minor_units, -150)
        self.assertTrue(l.all_sub_ledgers_balance())

    def test_long_position_gains_when_eur_strengthens(self):
        long_l = ledger()
        long_l.exchange("pre", "prefund", "1010-EUR", Money.parse("500.00", EUR), "1010-USD", eur_usd(10_900))
        self.assertEqual(long_l.position_report(EUR).basis.minor_units, 54_500)
        self.assertEqual(long_l.position(EUR).minor_units, 50_000)
        self.assertEqual(long_l.revalue(EUR, eur_usd(11_200)).minor_units, 1_500)
        self.assertEqual(long_l.balance(MultiCurrencyLedger.UNREALIZED).minor_units, 1_500)


class PropertyTest(unittest.TestCase):
    def test_random_exchanges_keep_invariants(self):
        r = ledger()
        rnd = JavaRandom(17)
        ok = True
        for i in range(500):
            rate = 10_000 + rnd.next_int(3_000)
            amt = Money.of_minor(1 + rnd.next_int(1_000_000), USD if rnd.next_boolean() else EUR)
            if amt.currency == USD:
                r.exchange(f"e{i}", "x", "1200-USD", amt, "2100-EUR", eur_usd(rate))
            else:
                r.exchange(f"e{i}", "x", "1010-EUR", amt, "1010-USD", eur_usd(rate))
            if i % 50 == 0:
                r.revalue(EUR, eur_usd(10_000 + rnd.next_int(3_000)))
            ok = ok and r.all_sub_ledgers_balance() and r.clearing_matches_basis()
        self.assertTrue(ok)
        pr = r.position_report(EUR)
        fair = eur_usd(11_000).convert(pr.position, Rounding.HALF_EVEN)
        r.revalue(EUR, eur_usd(11_000))
        self.assertEqual(r.position_report(EUR).carrying_value(), fair)


if __name__ == "__main__":
    unittest.main()
