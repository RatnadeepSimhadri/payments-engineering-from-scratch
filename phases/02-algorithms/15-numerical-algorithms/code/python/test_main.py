"""Tests for phases/02-algorithms/15-numerical-algorithms. Run: python3 -m unittest -v"""
import unittest

from main import (
    DAMM_TABLE, ErrorClass, SeededRandom, aggregate, analyse, build_iban, corruptions, damm_check_digit,
    damm_validate, extended_gcd, gcd, iban_check_digits, iban_to_numeric, lcm, luhn_check_digit, luhn_validate,
    mod, mod_inverse, mod_of_decimal_string, mod_pow, validate_iban,
)

PAN = "4539090122234563"
IBAN = "DE89370400440532013000"
DAMM_ID = "453909012223456" + str(damm_check_digit("453909012223456"))


class EuclidTest(unittest.TestCase):
    def test_gcd_lcm(self):
        self.assertEqual(gcd(1071, 462), 21)
        self.assertEqual(gcd(462, 1071), 21)
        self.assertEqual(gcd(0, 5), 5)
        self.assertEqual(gcd(0, 0), 0)
        self.assertEqual(gcd(-12, 18), 6)
        self.assertEqual(lcm(4, 6), 12)

    def test_bezout(self):
        b = extended_gcd(240, 46)
        self.assertEqual(b.g, 2)
        self.assertEqual(240 * b.x + 46 * b.y, 2)
        self.assertEqual((b.x, b.y), (-9, 47))
        b2 = extended_gcd(17, 5)
        self.assertEqual(17 * b2.x + 5 * b2.y, 1)

    def test_mod_inverse(self):
        self.assertEqual(mod_inverse(100, 97), 65)
        self.assertEqual(100 * 65 % 97, 1)
        self.assertEqual(mod_inverse(3, 11), 4)
        with self.assertRaises(ArithmeticError):
            mod_inverse(6, 10)

    def test_reachability_is_gcd(self):
        self.assertNotEqual(15 % gcd(6, 10), 0)
        self.assertEqual(16 % gcd(6, 10), 0)


class ModularTest(unittest.TestCase):
    def test_mod_and_pow(self):
        self.assertEqual((mod(-7, 5), mod(7, 5), mod(-10, 5)), (3, 2, 0))
        self.assertEqual(mod_pow(3, 200, 97), 62)
        self.assertEqual(mod_pow(7, 123456, 1_000_003), pow(7, 123456, 1_000_003))
        self.assertEqual(mod_pow(5, 0, 13), 1)

    def test_horner_mod(self):
        thirty = "370400440532013000131489123456"
        self.assertEqual(mod_of_decimal_string(thirty, 97), int(thirty) % 97)
        with self.assertRaises(ValueError):
            mod_of_decimal_string("12A4", 97)


class LuhnTest(unittest.TestCase):
    def test_check_digit_and_validate(self):
        self.assertEqual(luhn_check_digit("453909012223456"), 3)
        self.assertTrue(luhn_validate(PAN))
        self.assertTrue(luhn_validate("4539148803436467"))
        self.assertFalse(luhn_validate("4539148803436461"))
        self.assertEqual(luhn_check_digit("7992739871"), 3)
        self.assertTrue(luhn_validate("79927398713"))
        self.assertFalse(luhn_validate("4539-1488"))
        self.assertFalse(luhn_validate("7"))

    def test_known_blind_spots(self):
        self.assertTrue(luhn_validate("4530990122234563"))  # 09 <-> 90
        self.assertTrue(luhn_validate("4539090155234563"))  # 22 <-> 55


class IbanTest(unittest.TestCase):
    def test_numeric_and_validate(self):
        self.assertEqual(iban_to_numeric(IBAN), "370400440532013000131489")
        self.assertTrue(validate_iban(IBAN))
        self.assertTrue(validate_iban("gb82 west 1234 5698 7654 32"))
        self.assertFalse(validate_iban("GB82 WEST 1234 5689 7654 32"))
        self.assertFalse(validate_iban("DE89 3704 0044 0532 0130 0$"))
        self.assertFalse(validate_iban("DE8"))

    def test_check_digits(self):
        self.assertEqual(iban_check_digits("DE", "370400440532013000"), "89")
        self.assertEqual(build_iban("GB", "WEST12345698765432"), "GB82WEST12345698765432")
        self.assertTrue(validate_iban(build_iban("GB", "WEST12345698765432")))
        self.assertEqual(len(iban_check_digits("XX", "000000000000001")), 2)


class DammTest(unittest.TestCase):
    def test_table_properties(self):
        for i in range(10):
            self.assertEqual(sorted(DAMM_TABLE[i]), list(range(10)))
            self.assertEqual(sorted(DAMM_TABLE[j][i] for j in range(10)), list(range(10)))
            self.assertEqual(DAMM_TABLE[i][i], 0)

    def test_check_digit_and_validate(self):
        self.assertEqual(damm_check_digit("572"), 4)
        self.assertTrue(damm_validate("5724"))
        self.assertFalse(damm_validate("5725"))
        self.assertFalse(damm_validate("5734"))
        self.assertFalse(damm_validate("7524"))
        self.assertFalse(damm_validate(""))
        self.assertFalse(damm_validate("57a4"))


class ErrorAnalysisTest(unittest.TestCase):
    def test_luhn_classes(self):
        r = analyse(luhn_validate, PAN, ErrorClass.SINGLE_DIGIT)
        self.assertEqual((r.tried, r.caught), (144, 144))
        r = analyse(luhn_validate, PAN, ErrorClass.ADJACENT_TRANSPOSITION)
        self.assertEqual((r.tried, r.missed_count()), (13, 3))
        r = analyse(luhn_validate, PAN, ErrorClass.TWIN)
        self.assertEqual(r.missed, ["4539090155234563", "4539090125534563"])
        r = analyse(luhn_validate, PAN, ErrorClass.JUMP_TRANSPOSITION)
        self.assertEqual((r.tried, r.caught), (11, 0))

    def test_mod97_catches_everything(self):
        for cls in ErrorClass:
            r = analyse(validate_iban, IBAN, cls)
            self.assertGreater(r.tried, 0, cls)
            self.assertEqual(r.missed_count(), 0, cls)

    def test_damm_classes(self):
        r = analyse(damm_validate, DAMM_ID, ErrorClass.SINGLE_DIGIT)
        self.assertEqual((r.tried, r.caught), (144, 144))
        r = analyse(damm_validate, DAMM_ID, ErrorClass.ADJACENT_TRANSPOSITION)
        self.assertEqual((r.tried, r.caught), (13, 13))
        self.assertGreater(analyse(damm_validate, DAMM_ID, ErrorClass.TWIN).missed_count(), 0)

    def test_corruptions_and_guard(self):
        self.assertEqual(len(corruptions(IBAN, ErrorClass.SINGLE_DIGIT)), 20 * 9)
        with self.assertRaises(ValueError):
            analyse(luhn_validate, "4539090122234560", ErrorClass.SINGLE_DIGIT)

    def test_seeded_aggregate(self):
        agg = aggregate(luhn_validate, lambda p: p + str(luhn_check_digit(p)), 50, 15, 7)
        self.assertEqual(agg[ErrorClass.SINGLE_DIGIT], (7200, 7200))
        self.assertLess(agg[ErrorClass.ADJACENT_TRANSPOSITION][1], agg[ErrorClass.ADJACENT_TRANSPOSITION][0])
        self.assertEqual(agg[ErrorClass.JUMP_TRANSPOSITION][1], 0)
        rng = SeededRandom(7)
        self.assertEqual([rng.next_int(10) for _ in range(3)], [8, 1, 3])


if __name__ == "__main__":
    unittest.main()
