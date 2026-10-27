"""Tests for phases/11-payment-rails/07-interchange-and-scheme-fees. Run: python3 -m unittest -v"""
import unittest

from main import (EUR, USD, Blended, CardType, Channel, CurrencyMismatchError, FeeBreakdown, FeeEngine, IcPlusPlus,
                  InterchangeTable, Money, NoRateError, Pricing, Rate, Region, SchemeFees, Transaction)


def usd(s: str) -> Money:
    return Money.parse(s, USD)


ENGINE = FeeEngine(InterchangeTable.demo(), SchemeFees.demo())
ICPP = IcPlusPlus(Rate.of("0.20", 5))
BLENDED = Blended(Rate.of("2.90", 30))
US = Region.US_DOMESTIC
CNP, CP = Channel.CARD_NOT_PRESENT, Channel.CARD_PRESENT


class RateTest(unittest.TestCase):
    def test_parsing_is_exact(self):
        self.assertEqual(Rate.of("1.80", 10).ppm, 18_000)
        self.assertEqual(Rate.of("0.05", 0).ppm, 500)
        self.assertEqual(Rate.of("2", 0).ppm, 20_000)
        self.assertEqual(Rate.of("0.0013", 0).ppm, 13)
        with self.assertRaises(ValueError):
            Rate.of("0.00001", 0)
        self.assertEqual(str(Rate.of("1.80", 10)), "1.80% + 10")
        self.assertIsInstance(ICPP, Pricing)
        self.assertIsInstance(BLENDED, Pricing.Blended)


class WorkedExampleTest(unittest.TestCase):
    def test_100_usd_consumer_credit_cnp_icpp(self):
        f = ENGINE.quote(Transaction("t1", usd("100.00"), CardType.CONSUMER_CREDIT, US, CNP), ICPP)
        self.assertEqual(f.interchange, usd("1.90"))
        self.assertEqual(f.scheme_fees, usd("0.16"))
        self.assertEqual(f.acquirer_margin, usd("0.25"))
        self.assertEqual(f.merchant_net, usd("97.69"))
        self.assertEqual(f.merchant_discount(), usd("2.31"))
        self.assertEqual(f.effective_rate_bps(), 231)

    def test_blended_merchant_price_fixed_margin_moves(self):
        b1 = ENGINE.quote(Transaction("t", usd("100.00"), CardType.CONSUMER_CREDIT, US, CNP), BLENDED)
        b2 = ENGINE.quote(Transaction("t", usd("100.00"), CardType.COMMERCIAL_CREDIT, US, CNP), BLENDED)
        self.assertEqual(b1.merchant_net, b2.merchant_net)
        self.assertEqual(b1.merchant_net, usd("96.80"))
        self.assertEqual(b1.acquirer_margin, usd("1.14"))
        self.assertEqual(b2.acquirer_margin, usd("0.24"))
        big = ENGINE.quote(Transaction("t", usd("10000.00"), CardType.COMMERCIAL_CREDIT, US, CNP), Blended(Rate.of("2.70", 0)))
        self.assertTrue(big.acquirer_margin.is_negative())


class RegulatoryCapTest(unittest.TestCase):
    def test_regulation_ii(self):
        d = ENGINE.quote(Transaction("t", usd("2.00"), CardType.CONSUMER_DEBIT_REGULATED, US, CP), ICPP)
        self.assertEqual(d.interchange, usd("0.22"))
        with self.assertRaises(ValueError):
            InterchangeTable().put(CardType.CONSUMER_DEBIT_REGULATED, US, CP, Rate.of("0.05", 25))
        self.assertEqual(InterchangeTable().put(CardType.CONSUMER_DEBIT_EXEMPT, US, CP, Rate.of("0.80", 15)).size(), 1)

    def test_ifr_caps_and_missing_rows(self):
        with self.assertRaises(ValueError):
            InterchangeTable().put(CardType.PREMIUM_CREDIT, Region.EEA_DOMESTIC, CP, Rate.of("0.31", 0))
        with self.assertRaises(ValueError):
            InterchangeTable().put(CardType.CONSUMER_DEBIT_REGULATED, Region.EEA_DOMESTIC, CP, Rate.of("0.10", 1))
        eur = Money.parse("100.00", EUR)
        e = ENGINE.quote(Transaction("t", eur, CardType.PREMIUM_CREDIT, Region.EEA_DOMESTIC, CP), ICPP)
        self.assertEqual(e.interchange, Money.parse("0.30", EUR))
        x = ENGINE.quote(Transaction("t", eur, CardType.CONSUMER_CREDIT, Region.INTER_REGIONAL, CNP), ICPP)
        self.assertEqual(x.scheme_fees, Money.parse("0.76", EUR))
        with self.assertRaises(NoRateError):
            ENGINE.quote(Transaction("t", eur, CardType.CONSUMER_DEBIT_EXEMPT, Region.EEA_DOMESTIC, CP), ICPP)


class RoundingAndInvariantTest(unittest.TestCase):
    def test_one_half_even_per_component(self):
        odd = ENGINE.quote(Transaction("t", usd("37.77"), CardType.CONSUMER_CREDIT, US, CNP), ICPP)
        self.assertEqual(odd.interchange, usd("0.78"))
        self.assertEqual(odd.scheme_fees, usd("0.07"))

    def test_split_sums_to_gross_for_many_amounts(self):
        for cents in range(1, 50_001, 97):
            for t in CardType:
                for p in (ICPP, BLENDED):
                    q = ENGINE.quote(Transaction("t", Money.of_minor(cents, USD), t, US, CNP), p)
                    self.assertEqual(Money.sum(USD, q.interchange, q.scheme_fees, q.acquirer_margin, q.merchant_net), q.gross)

    def test_construction_guards(self):
        with self.assertRaises(RuntimeError):
            FeeBreakdown(usd("1.00"), Rate.of("1", 0), usd("0.01"), usd("0.01"), usd("0.01"), usd("0.98"))
        with self.assertRaises(ValueError):
            Transaction("t", usd("0.00"), CardType.CONSUMER_CREDIT, US, CP)


class AttributionTest(unittest.TestCase):
    def test_aggregate_fee_attribution(self):
        month = [Transaction("a", usd("100.00"), CardType.CONSUMER_CREDIT, US, CP),
                 Transaction("b", usd("37.77"), CardType.PREMIUM_CREDIT, US, CNP),
                 Transaction("c", usd("2.00"), CardType.CONSUMER_DEBIT_REGULATED, US, CP)]
        parts = FeeEngine.attribute(usd("25.00"), month)
        self.assertEqual(Money.sum(USD, *parts), usd("25.00"))
        self.assertEqual(parts[0], usd("17.89"))
        with self.assertRaises(CurrencyMismatchError):
            FeeEngine.attribute(Money.parse("25.00", EUR), month)
        total = FeeEngine.total([ENGINE.quote(tx, ICPP) for tx in month])
        self.assertEqual(total.gross, usd("139.77"))


if __name__ == "__main__":
    unittest.main()
