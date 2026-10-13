"""Tests for phases/10-money-and-ledgers/01-representing-money. Run: python3 -m unittest -v"""
import unittest

from main import EUR, JPY, KWD, USD, Currency, CurrencyMismatchError, Money, Rounding, divide_rounded


class ParseAndFormatTest(unittest.TestCase):
    def test_parse_two_decimals(self):
        self.assertEqual(Money.parse("12.34", USD).minor_units, 1234)

    def test_parse_pads_missing_decimals(self):
        self.assertEqual(Money.parse("12", USD).minor_units, 1200)
        self.assertEqual(Money.parse("0.5", USD).minor_units, 50)
        self.assertEqual(Money.parse(".99", USD).minor_units, 99)

    def test_parse_negative(self):
        self.assertEqual(Money.parse("-0.07", USD).minor_units, -7)

    def test_parse_other_exponents(self):
        self.assertEqual(Money.parse("1200", JPY).minor_units, 1200)
        self.assertEqual(Money.parse("1.250", KWD).minor_units, 1250)

    def test_parse_rejects_excess_precision(self):
        with self.assertRaises(ArithmeticError):
            Money.parse("1.5", JPY)
        with self.assertRaises(ArithmeticError):
            Money.parse("1.005", USD)

    def test_parse_rejects_garbage(self):
        with self.assertRaises(ValueError):
            Money.parse("12,34", USD)

    def test_str(self):
        self.assertEqual(str(Money.of_minor(1234, USD)), "12.34 USD")
        self.assertEqual(str(Money.of_minor(1205, USD)), "12.05 USD")
        self.assertEqual(str(Money.of_minor(-7, USD)), "-0.07 USD")
        self.assertEqual(str(Money.of_minor(1200, JPY)), "1200 JPY")
        self.assertEqual(str(Money.of_minor(1250, KWD)), "1.250 KWD")

    def test_never_from_float(self):
        with self.assertRaises(TypeError):
            Money(1.0, USD)  # type: ignore[arg-type]


class ArithmeticTest(unittest.TestCase):
    def test_plus_minus(self):
        self.assertEqual(Money.parse("1.10", USD).plus(Money.parse("2.20", USD)), Money.parse("3.30", USD))
        self.assertEqual(Money.parse("1.00", USD).minus(Money.parse("1.50", USD)).minor_units, -50)

    def test_negate_abs(self):
        self.assertEqual(Money.parse("5.00", USD).negate().minor_units, -500)
        self.assertEqual(Money.of_minor(-5, USD).abs().minor_units, 5)

    def test_currency_mismatch(self):
        with self.assertRaises(CurrencyMismatchError):
            Money.parse("1.00", USD).plus(Money.parse("1.00", EUR))
        with self.assertRaises(CurrencyMismatchError):
            Money.parse("1.00", USD) < Money.parse("1.00", EUR)

    def test_ordering_and_sum(self):
        self.assertLess(Money.parse("1.00", USD), Money.parse("2.00", USD))
        self.assertTrue(Money.sum(USD).is_zero())

    def test_big_values_do_not_overflow(self):
        huge = Money.of_minor(2**63 - 1, USD).plus(Money.of_minor(1, USD))
        self.assertEqual(huge.minor_units, 2**63)


class RoundingTest(unittest.TestCase):
    def test_half_even_ties_go_to_even(self):
        self.assertEqual(divide_rounded(125, 10, Rounding.HALF_EVEN), 12)
        self.assertEqual(divide_rounded(135, 10, Rounding.HALF_EVEN), 14)
        self.assertEqual(divide_rounded(-125, 10, Rounding.HALF_EVEN), -12)

    def test_half_up(self):
        self.assertEqual(divide_rounded(125, 10, Rounding.HALF_UP), 13)
        self.assertEqual(divide_rounded(-125, 10, Rounding.HALF_UP), -13)

    def test_down_truncates_toward_zero(self):
        self.assertEqual(divide_rounded(129, 10, Rounding.DOWN), 12)
        self.assertEqual(divide_rounded(-129, 10, Rounding.DOWN), -12)

    def test_up_away_from_zero(self):
        self.assertEqual(divide_rounded(121, 10, Rounding.UP), 13)
        self.assertEqual(divide_rounded(-121, 10, Rounding.UP), -13)

    def test_exact_division_unchanged(self):
        self.assertEqual(divide_rounded(120, 10, Rounding.HALF_EVEN), 12)

    def test_fee(self):
        self.assertEqual(Money.parse("19.99", USD).times(29, 1000, Rounding.HALF_EVEN).minor_units, 58)
        self.assertEqual(Money.parse("19.99", USD).times(3, 1, Rounding.HALF_EVEN).minor_units, 5997)

    def test_times_rejects_bad_denominator(self):
        with self.assertRaises(ArithmeticError):
            Money.of_minor(1, USD).times(1, 0, Rounding.DOWN)


class AllocationTest(unittest.TestCase):
    def test_thirds_sum_to_whole_first_gets_extra(self):
        thirds = Money.parse("100.00", USD).allocate(1, 1, 1)
        self.assertEqual([m.minor_units for m in thirds], [3334, 3333, 3333])
        self.assertEqual(Money.sum(USD, *thirds), Money.parse("100.00", USD))

    def test_seventy_thirty(self):
        self.assertEqual([m.minor_units for m in Money.parse("0.05", USD).allocate(70, 30)], [4, 1])

    def test_negative_allocation_conserves(self):
        parts = Money.parse("-1.00", USD).allocate(1, 1, 1)
        self.assertEqual(Money.sum(USD, *parts).minor_units, -100)

    def test_one_cent_three_ways(self):
        self.assertEqual([m.minor_units for m in Money.of_minor(1, USD).allocate(1, 1, 1)], [1, 0, 0])

    def test_rejects_bad_ratios(self):
        with self.assertRaises(ValueError):
            Money.of_minor(1, USD).allocate(0, 0)
        with self.assertRaises(ValueError):
            Money.of_minor(1, USD).allocate(1, -1)


class CurrencyTest(unittest.TestCase):
    def test_validation(self):
        with self.assertRaises(ValueError):
            Currency("usd", 2)
        with self.assertRaises(ValueError):
            Currency("ABC", 7)

    def test_scale(self):
        self.assertEqual(KWD.minor_units_per_major(), 1000)


if __name__ == "__main__":
    unittest.main()
