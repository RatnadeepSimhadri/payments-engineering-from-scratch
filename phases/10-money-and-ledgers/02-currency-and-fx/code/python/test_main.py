"""Tests for phases/10-money-and-ledgers/02-currency-and-fx. Run: python3 -m unittest -v"""
import unittest

from main import (CurrencyMismatchError, CurrencyRegistry, FxConverter, FxRate, Money, RateTable, Rounding,
                  StaleRateError, UnknownCurrencyError)

NOW = 1_700_000_000
REGISTRY = CurrencyRegistry.iso4217()
USD, EUR, GBP = REGISTRY.currency("USD"), REGISTRY.currency("EUR"), REGISTRY.currency("GBP")
JPY, KWD = REGISTRY.currency("JPY"), REGISTRY.currency("KWD")
USD_EUR = FxRate.of(USD, EUR, "0.9217", "ecb-ref", NOW - 60, 50)
USD_GBP = FxRate.of(USD, GBP, "0.7893", "ecb-ref", NOW - 60, 50)
HALF = FxRate.of(USD, EUR, "0.50", "x", NOW, 0)


class RegistryTest(unittest.TestCase):
    def test_exponents_are_data(self):
        self.assertEqual(USD.exponent, 2)
        self.assertEqual(JPY.exponent, 0)
        self.assertEqual(KWD.exponent, 3)
        self.assertEqual(REGISTRY.currency("CLF").exponent, 4)

    def test_numeric_lookup(self):
        self.assertEqual(REGISTRY.by_numeric(978), EUR)
        self.assertEqual(REGISTRY.info("USD").numeric_code, 840)

    def test_rejects_unknown_and_duplicates(self):
        registry = CurrencyRegistry.iso4217()
        with self.assertRaises(UnknownCurrencyError):
            registry.currency("USX")
        with self.assertRaises(ValueError):
            registry.register("USD", 999, 2, "dup")
        with self.assertRaises(ValueError):
            registry.register("ZZZ", 840, 2, "dup")

    def test_parse_uses_registered_exponent(self):
        self.assertEqual(REGISTRY.parse("1.250", "KWD").minor_units, 1250)
        with self.assertRaises(ArithmeticError):
            REGISTRY.parse("1.5", "JPY")


class FxRateTest(unittest.TestCase):
    def test_exact_rational_reduced_with_provenance(self):
        self.assertEqual((USD_EUR.numerator, USD_EUR.denominator), (9217, 10000))
        self.assertEqual((HALF.numerator, HALF.denominator), (1, 2))
        self.assertEqual(USD_EUR.source, "ecb-ref")
        self.assertEqual(USD_EUR.observed_at_epoch_seconds, NOW - 60)

    def test_invert(self):
        inv = USD_EUR.invert()
        self.assertEqual((inv.numerator, inv.denominator, inv.base, inv.quote), (10000, 9217, EUR, USD))
        self.assertEqual(inv.invert(), USD_EUR)

    def test_to_decimal(self):
        self.assertEqual(USD_EUR.to_decimal(6), "0.921700")
        self.assertEqual(FxRate.of(USD, EUR, "0.12345", "x", NOW, 0).to_decimal(4), "0.1234")

    def test_rejects_bad_rates(self):
        with self.assertRaises(ValueError):
            FxRate.of(USD, USD, "1", "x", NOW, 0)
        with self.assertRaises(ValueError):
            FxRate.of(USD, EUR, "0", "x", NOW, 0)
        with self.assertRaises(ValueError):
            FxRate.of(USD, EUR, "0,92", "x", NOW, 0)

    def test_spread_bid_below_mid_below_ask(self):
        bid, ask = USD_EUR.bid(), USD_EUR.ask()
        self.assertLess(bid.numerator * USD_EUR.denominator, USD_EUR.numerator * bid.denominator)
        self.assertGreater(ask.numerator * USD_EUR.denominator, USD_EUR.numerator * ask.denominator)
        self.assertEqual(bid.to_decimal(6), "0.919396")
        self.assertEqual(ask.to_decimal(6), "0.924004")
        self.assertEqual(bid.spread_bps, 0)


class ConversionTest(unittest.TestCase):
    def test_exact_conversion_has_zero_remainder(self):
        c = FxConverter.convert(Money.parse("100.00", USD), USD_EUR, Rounding.HALF_EVEN)
        self.assertEqual(c.to, Money.parse("92.17", EUR))
        self.assertFalse(c.rounded())
        self.assertEqual(c.remainder_numerator, 0)

    def test_rounds_once_and_reports_remainder(self):
        c2 = FxConverter.convert(Money.parse("19.99", USD), USD_EUR, Rounding.HALF_EVEN)
        self.assertEqual(c2.to, Money.parse("18.42", EUR))
        self.assertTrue(c2.rounded())
        self.assertEqual((c2.remainder_numerator, c2.remainder_denominator), (4783, 10000))

    def test_quote_exponent_drives_the_scale(self):
        c3 = FxConverter.convert(Money.parse("19.99", USD), FxRate.of(USD, JPY, "149.37", "x", NOW, 0), Rounding.HALF_EVEN)
        self.assertEqual(c3.to, Money.parse("2986", JPY))
        c4 = FxConverter.convert(Money.parse("19.99", USD), FxRate.of(USD, KWD, "0.30825", "x", NOW, 0), Rounding.HALF_EVEN)
        self.assertEqual(c4.to, Money.parse("6.162", KWD))

    def test_sub_unit_conversion(self):
        c5 = FxConverter.convert(Money.parse("0.01", USD), USD_EUR, Rounding.DOWN)
        self.assertTrue(c5.to.is_zero())
        self.assertTrue(c5.rounded())
        c6 = FxConverter.convert(Money.parse("0.01", USD), USD_EUR, Rounding.UP)
        self.assertEqual(c6.to.minor_units, 1)

    def test_negative_amounts_convert_symmetrically(self):
        neg = FxConverter.convert(Money.parse("-19.99", USD), USD_EUR, Rounding.HALF_EVEN)
        self.assertEqual(neg.to, Money.parse("-18.42", EUR))

    def test_rejects_wrong_currency(self):
        with self.assertRaises(CurrencyMismatchError):
            FxConverter.convert(Money.parse("1.00", EUR), USD_EUR, Rounding.HALF_EVEN)


class RoundTripTest(unittest.TestCase):
    def test_residue_is_real_money(self):
        rt = FxConverter.round_trip(Money.parse("19.99", USD), USD_EUR, Rounding.HALF_EVEN)
        self.assertEqual(rt.back.to, Money.parse("19.98", USD))
        self.assertEqual(rt.residue, Money.parse("0.01", USD))
        self.assertEqual(rt.residue.currency, USD)

    def test_round_trip_can_manufacture_a_cent(self):
        manufactured = FxConverter.round_trip(Money.parse("0.06", USD), USD_EUR, Rounding.HALF_EVEN)
        self.assertEqual(manufactured.residue, Money.parse("-0.01", USD))

    def test_exact_round_trip_has_zero_residue(self):
        exact = FxConverter.round_trip(Money.parse("100.00", USD), HALF, Rounding.HALF_EVEN)
        self.assertTrue(exact.residue.is_zero())


class CrossRateTest(unittest.TestCase):
    def test_cross_through_base(self):
        eur_gbp = USD_EUR.cross(USD_GBP)
        self.assertEqual((eur_gbp.base, eur_gbp.quote, eur_gbp.numerator, eur_gbp.denominator), (EUR, GBP, 7893, 9217))
        self.assertEqual(eur_gbp.observed_at_epoch_seconds, NOW - 60)
        c7 = FxConverter.convert(Money.parse("50.00", EUR), eur_gbp, Rounding.HALF_EVEN)
        self.assertEqual(c7.to, Money.parse("42.82", GBP))
        with self.assertRaises(ValueError):
            USD_EUR.cross(eur_gbp)


class RateTableTest(unittest.TestCase):
    def setUp(self):
        self.table = RateTable(USD, 300)
        self.table.put(USD_EUR)
        self.table.put(USD_GBP)

    def test_direct_inverted_crossed(self):
        self.assertEqual(self.table.rate(USD, EUR, NOW), USD_EUR)
        self.assertEqual(self.table.rate(EUR, USD, NOW), USD_EUR.invert())
        self.assertEqual(self.table.rate(EUR, GBP, NOW).numerator, 7893)

    def test_rejections(self):
        with self.assertRaises(StaleRateError):
            self.table.rate(USD, EUR, NOW + 3600)
        with self.assertRaises(ValueError):
            self.table.put(USD_EUR.cross(USD_GBP))
        with self.assertRaises(ValueError):
            self.table.rate(USD, JPY, NOW)
        with self.assertRaises(ValueError):
            self.table.rate(USD, USD, NOW)


if __name__ == "__main__":
    unittest.main()
