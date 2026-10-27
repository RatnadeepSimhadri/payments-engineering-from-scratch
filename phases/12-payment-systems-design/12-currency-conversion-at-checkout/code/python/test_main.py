"""Tests for phases/12-payment-systems-design/12-currency-conversion-at-checkout. Run: python3 -m unittest -v"""

import unittest

from main import (EUR, FIFTEEN_MINUTES, GBP, JPY, ONE, T0, USD, CurrencyMismatchError, Dcc, FakeClock, FakeRateSource,
                  FxConverter, FxDesk, FxRate, Money, QuoteExpiredError, Rational, RoundingMode)


def usd(s: str) -> Money:
    return Money.parse(s, USD)


def eur(s: str) -> Money:
    return Money.parse(s, EUR)


def gbp(s: str) -> Money:
    return Money.parse(s, GBP)


class Rig:
    def __init__(self) -> None:
        self.clock = FakeClock(T0)
        self.market = FakeRateSource()
        self.market.set(EUR, USD, "1.0850")
        self.desk = FxDesk(self.clock, self.market, 200, FIFTEEN_MINUTES)


class RationalTest(unittest.TestCase):
    def test_exact_and_normalized(self) -> None:
        self.assertEqual(Rational.parse("1.0850"), Rational.of(217, 200))
        self.assertEqual(Rational.of(2, -4), Rational.of(-1, 2), "sign lives in the numerator")
        self.assertEqual(Rational.parse("1.0850").inverse(), Rational.of(200, 217))
        self.assertGreater(Rational.of(1, 3).compare_to(Rational.parse("0.3333")), 0)
        self.assertEqual(Rational.bps(350), Rational.of(7, 200))
        with self.assertRaises(ArithmeticError):
            Rational.of(1, 0)

    def test_rounding_modes_by_integer_arithmetic(self) -> None:
        half = Rational.of(5, 2)        # 2.5
        neg_half = Rational.of(-5, 2)   # -2.5
        self.assertEqual(half.round_to_long(RoundingMode.HALF_EVEN), 2)
        self.assertEqual(Rational.of(7, 2).round_to_long(RoundingMode.HALF_EVEN), 4)
        self.assertEqual(half.round_to_long(RoundingMode.HALF_UP), 3)
        self.assertEqual(neg_half.round_to_long(RoundingMode.HALF_UP), -3)
        self.assertEqual(half.round_to_long(RoundingMode.HALF_DOWN), 2)
        self.assertEqual(neg_half.round_to_long(RoundingMode.CEILING), -2)
        self.assertEqual(neg_half.round_to_long(RoundingMode.FLOOR), -3)
        self.assertEqual(neg_half.round_to_long(RoundingMode.DOWN), -2)
        self.assertEqual(neg_half.round_to_long(RoundingMode.UP), -3)
        self.assertEqual(Rational.of(-41, 100).to_decimal(2), "-0.41")
        self.assertEqual(Rational.of(7, 2).to_decimal(0), "4")


class ConversionTest(unittest.TestCase):
    def test_one_rounding_on_the_amount(self) -> None:
        usd_eur = FxRate(USD, EUR, Rational.parse("1.0850").inverse()).adjusted_by(200)
        self.assertEqual(usd_eur.rate, Rational.of(204, 217), "customer rate is mid * 1.02")
        self.assertEqual(FxConverter.convert(usd("120.00"), usd_eur, RoundingMode.CEILING), eur("112.82"))
        self.assertEqual(FxConverter.convert(usd("120.00"), usd_eur, RoundingMode.HALF_EVEN), eur("112.81"))
        usd_jpy = FxRate(USD, JPY, Rational.parse("152.37"))
        self.assertEqual(FxConverter.convert(usd("120.00"), usd_jpy, RoundingMode.HALF_EVEN), Money.of_minor(18284, JPY))
        self.assertEqual(FxConverter.convert(Money.of_minor(18284, JPY), usd_jpy.inverse(), RoundingMode.HALF_EVEN), usd("120.00"))
        exact = FxConverter.convert(usd("50000.00"), usd_eur, RoundingMode.HALF_EVEN)
        rounded_rate = FxConverter.convert_with_rounded_rate(usd("50000.00"), usd_eur, 4, RoundingMode.HALF_EVEN)
        self.assertEqual(exact, eur("47004.61"))
        self.assertEqual(rounded_rate.minus(exact), eur("0.39"), "rounding the rate first costs 0.39 EUR")
        with self.assertRaises(CurrencyMismatchError):
            FxConverter.convert(eur("1.00"), usd_eur, RoundingMode.HALF_EVEN)
        with self.assertRaises(ValueError):
            FxRate(USD, EUR, Rational.of(0, 1))
        with self.assertRaises(ValueError):
            FxRate(USD, USD, Rational.parse("1.01"))
        self.assertEqual(FxRate(EUR, USD, ONE).adjusted_by(-200).rate, Rational.of(49, 50), "negative bps scales down")


class LockWindowTest(unittest.TestCase):
    def test_half_open_window(self) -> None:
        r = Rig()
        q = r.desk.quote(USD, EUR)
        self.assertEqual(q.customer_rate.rate, Rational.of(204, 217))
        self.assertEqual(q.spread_bps, 200)
        r.clock.advance_millis(FIFTEEN_MINUTES - 1)
        c = r.desk.confirm(q.id, usd("120.00"))
        self.assertEqual(c.presentment_amount, eur("112.82"), "confirm 1 ms before expiry succeeds")
        r.market.set(EUR, USD, "1.0000")
        self.assertEqual(r.desk.confirm(q.id, usd("120.00")).presentment_amount, eur("112.82"), "locked quote ignores the market")
        r.clock.advance_millis(1)
        with self.assertRaises(QuoteExpiredError):
            r.desk.confirm(q.id, usd("120.00"))
        with self.assertRaises(ValueError):
            r.desk.confirm("fxq_9999", usd("120.00"))
        with self.assertRaises(CurrencyMismatchError):
            r.desk.confirm(r.desk.quote(USD, EUR).id, gbp("120.00"))


class SettlementTest(unittest.TestCase):
    def test_merchant_always_gets_the_promised_amount(self) -> None:
        r = Rig()
        c = r.desk.confirm(r.desk.quote(USD, EUR).id, usd("120.00"))
        flat = FxDesk.settle(c, Rational.parse("1.0850"))
        down = FxDesk.settle(c, Rational.parse("1.0600"))
        self.assertEqual(flat.psp_fx_pnl, usd("2.41"))
        self.assertEqual(down.psp_fx_pnl, usd("-0.41"))
        self.assertEqual(flat.merchant_receives, usd("120.00"))
        self.assertEqual(down.merchant_receives, usd("120.00"))
        self.assertEqual(down.psp_proceeds, down.merchant_receives.plus(down.psp_fx_pnl))
        be = FxDesk.break_even_rate(c)
        self.assertEqual(be, Rational.of(12000, 11282))
        self.assertEqual(FxDesk.settle(c, be).psp_fx_pnl.minor_units, 0)
        r.market.set(EUR, USD, "1.0600")
        self.assertEqual(FxDesk.presentment_price(usd("120.00"), r.desk.quote(USD, EUR)), eur("115.48"))

    def test_zero_spread_and_validation(self) -> None:
        clock = FakeClock(0)
        market = FakeRateSource()
        market.set(EUR, USD, "1.2500")
        zero = FxDesk(clock, market, 0, 1000)
        self.assertEqual(FxDesk.presentment_price(usd("100.00"), zero.quote(USD, EUR)), eur("80.00"))
        with self.assertRaises(ValueError):
            FxDesk(clock, market, 0, 0)


class DccTest(unittest.TestCase):
    def test_disclosure_math(self) -> None:
        o = Dcc.offer(eur("100.00"), GBP, Rational.parse("0.8550"), 350)
        self.assertEqual(o.dcc_rate.rate, Rational.parse("0.884925"))
        self.assertEqual(o.card_amount, gbp("88.49"))
        self.assertEqual(o.markup_percent(), Rational.of(7, 2))
        self.assertLess(o.effective_markup_percent().compare_to(o.markup_percent()), 0)
        self.assertEqual(o.gross_margin(), gbp("2.99"))
        split = o.gross_margin().allocate(3000, 7000)
        self.assertEqual(split, [gbp("0.90"), gbp("2.09")])
        d = o.disclosure()
        for needle in ("88.49 GBP", "100.00 EUR", "0.884925", "3.50%", "0.8550"):
            self.assertIn(needle, d)
        self.assertEqual(Dcc.issuer_conversion(eur("100.00"), GBP, Rational.parse("0.8560"), 0), gbp("85.60"))
        self.assertEqual(Dcc.issuer_conversion(eur("100.00"), GBP, Rational.parse("0.8560"), 299), gbp("88.16"))
        self.assertEqual(Dcc.offer(eur("100.00"), GBP, Rational.parse("0.8550"), 0).card_amount, gbp("85.50"))
        with self.assertRaises(ValueError):
            Dcc.offer(eur("100.00"), EUR, ONE, 350)
        with self.assertRaises(CurrencyMismatchError):
            usd("1.00").plus(eur("1.00"))


if __name__ == "__main__":
    unittest.main()
