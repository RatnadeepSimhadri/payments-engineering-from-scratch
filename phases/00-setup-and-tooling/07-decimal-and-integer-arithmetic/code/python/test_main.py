"""Tests for phases/00-setup-and-tooling/07-decimal-and-integer-arithmetic. Run: python3 -m unittest -v"""
import math
import unittest
from decimal import Decimal

from main import (INT64_MAX, INT64_MIN, ZERO, Fixed2, Rounding, add_floats, checked_add, checked_mul, checked_negate,
                  checked_sub, divide_rounded, naive_cents, rounding_bias, sum_repeated, wrapping_add)


class FloatFailuresTest(unittest.TestCase):
    def test_point_one_plus_point_two(self):
        self.assertNotEqual(add_floats(0.1, 0.2), 0.3)
        self.assertEqual(repr(add_floats(0.1, 0.2)), "0.30000000000000004")

    def test_naive_cents_loses_a_cent(self):
        self.assertEqual(naive_cents(19.99), 1998)

    def test_accumulation(self):
        self.assertNotEqual(sum_repeated(0.1, 10), 1.0)
        self.assertGreater(abs(sum_repeated(0.01, 1_000_000) - 10_000), 1e-7)
        self.assertEqual(sum_repeated(0.25, 4), 1.0, "powers of two are exact")

    def test_python_sum_hides_the_error_since_3_12(self):
        # Not a property of the lesson's code: a property of the runtime the lesson warns about.
        self.assertEqual(math.fsum([0.1] * 10), 1.0)


class CheckedArithmeticTest(unittest.TestCase):
    def test_python_ints_do_not_wrap(self):
        self.assertEqual(INT64_MAX + 1, 2**63)

    def test_wrapping_add_emulates_java_and_go(self):
        self.assertEqual(wrapping_add(INT64_MAX, 1), INT64_MIN)
        self.assertEqual(wrapping_add(1, 2), 3)

    def test_checked_add_sub(self):
        self.assertEqual(checked_add(1, 2), 3)
        self.assertEqual(checked_add(-1, -2), -3)
        self.assertEqual(checked_sub(1, 2), -1)
        with self.assertRaises(OverflowError):
            checked_add(INT64_MAX, 1)
        with self.assertRaises(OverflowError):
            checked_add(INT64_MIN, -1)
        with self.assertRaises(OverflowError):
            checked_sub(INT64_MIN, 1)

    def test_checked_mul_negate(self):
        self.assertEqual(checked_mul(2, 3), 6)
        self.assertEqual(checked_mul(INT64_MAX, 0), 0)
        self.assertEqual(checked_negate(5), -5)
        with self.assertRaises(OverflowError):
            checked_mul(1 << 62, 4)
        with self.assertRaises(OverflowError):
            checked_mul(INT64_MIN, -1)
        with self.assertRaises(OverflowError):
            checked_mul(-1, INT64_MIN)
        with self.assertRaises(OverflowError):
            checked_negate(INT64_MIN)


class RoundedDivisionTest(unittest.TestCase):
    def test_ties(self):
        self.assertEqual(divide_rounded(125, 10, Rounding.HALF_EVEN), 12)
        self.assertEqual(divide_rounded(135, 10, Rounding.HALF_EVEN), 14)
        self.assertEqual(divide_rounded(125, 10, Rounding.HALF_UP), 13)
        self.assertEqual(divide_rounded(-125, 10, Rounding.HALF_UP), -13)
        self.assertEqual(divide_rounded(-125, 10, Rounding.HALF_EVEN), -12)

    def test_truncation_not_floor(self):
        self.assertEqual(divide_rounded(-129, 10, Rounding.DOWN), -12)
        self.assertEqual(-129 // 10, -13, "Python's // floors; divide_rounded must not")
        self.assertEqual(divide_rounded(121, 10, Rounding.UP), 13)

    def test_division_by_zero(self):
        with self.assertRaises(ZeroDivisionError):
            divide_rounded(1, 0, Rounding.DOWN)

    def test_bias_over_100_ties(self):
        self.assertEqual(rounding_bias(Rounding.HALF_UP), 50)
        self.assertEqual(rounding_bias(Rounding.HALF_EVEN), 0)
        self.assertEqual(rounding_bias(Rounding.DOWN), -50)
        self.assertEqual(rounding_bias(Rounding.UP), 50)


class Fixed2Test(unittest.TestCase):
    def test_parse(self):
        self.assertEqual(Fixed2.parse("19.99").hundredths, 1999)
        self.assertEqual(Fixed2.parse("7").hundredths, 700)
        self.assertEqual(Fixed2.parse("-0.5").hundredths, -50)
        self.assertEqual(Fixed2.parse(".05").hundredths, 5)

    def test_parse_rejects(self):
        with self.assertRaises(ArithmeticError):
            Fixed2.parse("19.995")
        with self.assertRaises(ValueError):
            Fixed2.parse("19,99")

    def test_from_double(self):
        self.assertEqual(Fixed2.from_double(19.99).hundredths, 1999)
        with self.assertRaises(ArithmeticError):
            Fixed2.from_double(0.1 + 0.2)  # the binary error is surfaced, not hidden
        with self.assertRaises(ArithmeticError):
            Fixed2.from_double(19.995)
        with self.assertRaises(ArithmeticError):
            Fixed2.from_double(float("nan"))

    def test_never_from_float_directly(self):
        with self.assertRaises(TypeError):
            Fixed2(19.99)  # type: ignore[arg-type]

    def test_str(self):
        self.assertEqual(str(Fixed2.of_hundredths(1999)), "19.99")
        self.assertEqual(str(Fixed2.of_hundredths(-5)), "-0.05")
        self.assertEqual(str(ZERO), "0.00")

    def test_plus_minus_overflow(self):
        self.assertEqual(Fixed2.parse("1.10").plus(Fixed2.parse("2.20")), Fixed2.parse("3.30"))
        self.assertEqual(Fixed2.parse("1.00").minus(Fixed2.parse("1.50")).hundredths, -50)
        with self.assertRaises(OverflowError):
            Fixed2.of_hundredths(INT64_MAX).plus(Fixed2.of_hundredths(1))

    def test_times(self):
        self.assertEqual(Fixed2.parse("19.99").times(Fixed2.parse("3"), Rounding.HALF_EVEN), Fixed2.parse("59.97"))
        self.assertEqual(Fixed2.parse("19.99").times(Fixed2.parse("1.08"), Rounding.HALF_EVEN), Fixed2.parse("21.59"))
        self.assertEqual(Fixed2.parse("0.05").times(Fixed2.parse("0.05"), Rounding.HALF_EVEN), ZERO)
        self.assertEqual(Fixed2.parse("0.05").times(Fixed2.parse("0.05"), Rounding.UP), Fixed2.parse("0.01"))

    def test_times_rate(self):
        self.assertEqual(Fixed2.parse("19.99").times_rate(29, 1000, Rounding.HALF_EVEN), Fixed2.parse("0.58"))
        with self.assertRaises(ArithmeticError):
            Fixed2.parse("1").times_rate(1, 0, Rounding.DOWN)

    def test_division_does_not_conserve(self):
        third = Fixed2.parse("100.00").divided_by(3, Rounding.HALF_EVEN)
        self.assertEqual(third, Fixed2.parse("33.33"))
        self.assertNotEqual(third.times(Fixed2.parse("3"), Rounding.HALF_EVEN), Fixed2.parse("100.00"))

    def test_ordering_and_million_cents(self):
        self.assertLess(Fixed2.parse("1.00"), Fixed2.parse("2.00"))
        acc, cent = ZERO, Fixed2.parse("0.01")
        for _ in range(1_000_000):
            acc = acc.plus(cent)
        self.assertEqual(acc, Fixed2.parse("10000.00"))


class DecimalModuleTest(unittest.TestCase):
    def test_decimal_from_string_is_exact_and_from_float_is_not(self):
        self.assertEqual(Decimal("0.1") + Decimal("0.2"), Decimal("0.3"))
        self.assertNotEqual(Decimal(0.1), Decimal("0.1"))


if __name__ == "__main__":
    unittest.main()
