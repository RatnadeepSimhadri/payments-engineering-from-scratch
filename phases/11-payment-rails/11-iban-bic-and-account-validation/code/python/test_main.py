"""Tests for phases/11-payment-rails/11-iban-bic-and-account-validation. Run: python3 -m unittest -v"""
import unittest

from main import (
    COUNTRY_FORMATS,
    AccountValidator,
    Bic,
    Iban,
    Reason,
    RoutingNumber,
    Severity,
    mod97,
    normalize,
)

# One registry example per supported country; every one must pass structure, length, and mod-97.
REGISTRY_EXAMPLES = [
    "AE070331234567890123456",
    "AT611904300234573201",
    "BE68539007547034",
    "CH9300762011623852957",
    "DE89370400440532013000",
    "DK5000400440116243",
    "ES9121000418450200051332",
    "FI2112345600000785",
    "FR1420041010050500013M02606",
    "GB82WEST12345698765432",
    "IE29AIBK93115212345678",
    "IT60X0542811101000000123456",
    "LU280019400644750000",
    "NL91ABNA0417164300",
    "NO9386011117947",
    "PL61109010140000071219812874",
    "PT50000201231234567890154",
    "SA0380000000608010167519",
    "SE4550000000058398257466",
]


class Mod97Test(unittest.TestCase):
    def test_digits_only(self):
        self.assertEqual(mod97("1234567890"), 1234567890 % 97)

    def test_letters_expand_to_two_digits(self):
        # A=10, Z=35: "AZ" is the digit string "1035"
        self.assertEqual(mod97("AZ"), 1035 % 97)

    def test_rearranged_valid_iban_is_one(self):
        self.assertEqual(mod97("WEST12345698765432GB82"), 1)

    def test_rejects_other_characters(self):
        with self.assertRaises(ValueError):
            mod97("12-34")


class IbanTest(unittest.TestCase):
    def test_every_registry_example_is_valid(self):
        for example in REGISTRY_EXAMPLES:
            with self.subTest(example):
                result = Iban.validate(example)
                self.assertTrue(result.valid, [str(i) for i in result.issues])

    def test_every_country_in_table_has_an_example(self):
        self.assertEqual(sorted(COUNTRY_FORMATS), sorted(e[:2] for e in REGISTRY_EXAMPLES))

    def test_normalizes_paper_form(self):
        self.assertEqual(normalize("gb82 west-1234 5698 7654 32"), "GB82WEST12345698765432")
        self.assertTrue(Iban.validate("gb82 west 1234 5698 7654 32").valid)

    def test_parse_components(self):
        iban = Iban.parse("GB82 WEST 1234 5698 7654 32")
        self.assertEqual(iban.country, "GB")
        self.assertEqual(iban.check_digits, "82")
        self.assertEqual(iban.bban, "WEST12345698765432")
        self.assertEqual(iban.bank_identifier, "WEST")
        self.assertEqual(iban.branch_identifier, "123456")
        self.assertEqual(iban.formatted(), "GB82 WEST 1234 5698 7654 32")

    def test_italian_bank_identifier_skips_cin(self):
        iban = Iban.parse("IT60X0542811101000000123456")
        self.assertEqual(iban.bank_identifier, "05428")
        self.assertEqual(iban.branch_identifier, "11101")

    def test_single_digit_error_detected(self):
        for example in REGISTRY_EXAMPLES:
            # change the last character to a different digit
            last = example[-1]
            replacement = "1" if last != "1" else "2"
            mutated = example[:-1] + replacement
            with self.subTest(mutated):
                result = Iban.validate(mutated)
                self.assertTrue(result.has(Reason.CHECKSUM_FAILED) or result.has(Reason.BBAN_STRUCTURE))

    def test_adjacent_transposition_detected(self):
        result = Iban.validate("GB82WEST12345698765423")
        self.assertFalse(result.valid)
        self.assertTrue(result.has(Reason.CHECKSUM_FAILED))

    def test_wrong_length_reported_before_checksum(self):
        result = Iban.validate("DE8937040044053201300")
        self.assertEqual([i.reason for i in result.issues], [Reason.WRONG_LENGTH])

    def test_unknown_country(self):
        result = Iban.validate("XX82WEST12345698765432")
        self.assertEqual([i.reason for i in result.issues], [Reason.UNKNOWN_COUNTRY])

    def test_invalid_characters(self):
        result = Iban.validate("GB82 WEST 1234 5698 7654 3*")
        self.assertTrue(result.has(Reason.INVALID_CHARACTERS))

    def test_bban_structure_letters_where_digits_expected(self):
        result = Iban.validate("GB82WEST1234569876543X")
        self.assertTrue(result.has(Reason.BBAN_STRUCTURE))
        self.assertIn("positions 11-18", [i.message for i in result.issues if i.reason is Reason.BBAN_STRUCTURE][0])

    def test_structurally_invalid_check_digits(self):
        for bad in ("00", "01", "99"):
            with self.subTest(bad):
                self.assertTrue(Iban.validate("GB" + bad + "WEST12345698765432").has(Reason.INVALID_CHECK_DIGITS))

    def test_empty(self):
        self.assertTrue(Iban.validate("   ").has(Reason.EMPTY))

    def test_generate_reproduces_registry_check_digits(self):
        for example in REGISTRY_EXAMPLES:
            with self.subTest(example):
                self.assertEqual(Iban.check_digits_for(example[:2], example[4:]), example[2:4])
                self.assertEqual(Iban.generate(example[:2], example[4:]).value, example)

    def test_generate_rejects_wrong_bban_length(self):
        with self.assertRaises(ValueError):
            Iban.check_digits_for("DE", "12345")

    def test_parse_raises_with_reasons(self):
        with self.assertRaises(ValueError) as ctx:
            Iban.parse("DE8937040044053201300")
        self.assertIn("WRONG_LENGTH", str(ctx.exception))


class BicTest(unittest.TestCase):
    def test_bic8_and_bic11(self):
        self.assertTrue(Bic.validate("DEUTDEFF").valid)
        self.assertTrue(Bic.validate("DEUTDEFF500").valid)
        self.assertTrue(Bic.validate("NWBKGB2L").valid)

    def test_parse_fills_primary_office_branch(self):
        bic = Bic.parse("deutdeff")
        self.assertEqual((bic.institution, bic.country, bic.location, bic.branch), ("DEUT", "DE", "FF", "XXX"))
        self.assertEqual(bic.bic8, "DEUTDEFF")
        self.assertFalse(bic.is_test)

    def test_test_bic_is_a_warning_not_an_error(self):
        result = Bic.validate("DEUTDEF0")
        self.assertTrue(result.valid)
        self.assertEqual(result.issues[0].severity, Severity.WARNING)
        self.assertTrue(Bic.parse("DEUTDEF0").is_test)

    def test_wrong_length(self):
        for bad in ("DEUTDE", "DEUTDEFFX", "DEUTDEFF5000"):
            with self.subTest(bad):
                self.assertTrue(Bic.validate(bad).has(Reason.WRONG_LENGTH))

    def test_unknown_country(self):
        self.assertTrue(Bic.validate("DEUTZZFF").has(Reason.UNKNOWN_COUNTRY))

    def test_digits_in_country_position(self):
        self.assertTrue(Bic.validate("DEUT1EFF").has(Reason.INVALID_CHARACTERS))

    def test_invalid_characters(self):
        self.assertTrue(Bic.validate("DEUT-EFF").has(Reason.WRONG_LENGTH) or Bic.validate("DEUT.EFF").has(Reason.INVALID_CHARACTERS))


class RoutingNumberTest(unittest.TestCase):
    def test_check_digit(self):
        self.assertEqual(RoutingNumber.check_digit("02199999"), "6")
        self.assertEqual(RoutingNumber.check_digit("01199999"), "3")
        self.assertEqual(RoutingNumber.check_digit("11100002"), "5")

    def test_valid(self):
        for rtn in ("021999996", "011999993", "061999998", "121999993"):
            with self.subTest(rtn):
                self.assertTrue(RoutingNumber.validate(rtn).valid)

    def test_transposition_detected(self):
        self.assertTrue(RoutingNumber.validate("021999969").has(Reason.CHECKSUM_FAILED))

    def test_prefix_outside_federal_reserve_ranges(self):
        result = RoutingNumber.validate("991999990")
        self.assertEqual([i.reason for i in result.issues], [Reason.INVALID_PREFIX])

    def test_wrong_length_and_characters(self):
        self.assertTrue(RoutingNumber.validate("02199999").has(Reason.WRONG_LENGTH))
        self.assertTrue(RoutingNumber.validate("02199999A").has(Reason.INVALID_CHARACTERS))
        self.assertTrue(RoutingNumber.validate("").has(Reason.EMPTY))

    def test_check_digit_requires_eight_digits(self):
        with self.assertRaises(ValueError):
            RoutingNumber.check_digit("0219999")


class AccountValidatorTest(unittest.TestCase):
    def test_matching_pair(self):
        result = AccountValidator.validate_pair("DE89 3704 0044 0532 0130 00", "DEUTDEFF")
        self.assertTrue(result.valid)
        self.assertEqual(result.issues, ())

    def test_country_mismatch_is_a_warning(self):
        result = AccountValidator.validate_pair("DE89 3704 0044 0532 0130 00", "NWBKGB2L")
        self.assertTrue(result.valid)
        self.assertTrue(result.has(Reason.COUNTRY_MISMATCH))

    def test_invalid_iban_propagates(self):
        result = AccountValidator.validate_pair("DE8937040044053201300", "DEUTDEFF")
        self.assertFalse(result.valid)
        self.assertTrue(result.has(Reason.WRONG_LENGTH))
        self.assertFalse(result.has(Reason.COUNTRY_MISMATCH))


if __name__ == "__main__":
    unittest.main()
