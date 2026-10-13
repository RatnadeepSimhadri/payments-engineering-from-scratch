"""Tests for phases/11-payment-rails/08-ach-and-nacha. Run: python3 -m unittest -v"""
import unittest
from datetime import date

from main import (
    CREDITS_ONLY,
    DEBITS_ONLY,
    MIXED,
    USD,
    AchFile,
    Addenda,
    Batch,
    BatchHeader,
    EntryDetail,
    Fixed,
    Money,
    NachaFormatError,
    ReturnReason,
    RoutingNumber,
    SecCode,
    TransactionCode,
    entry_hash,
    payroll_file,
)


def credit(routing: str, cents: int) -> EntryDetail:
    return EntryDetail.of(TransactionCode.CHECKING_CREDIT, routing, "123", Money.of_minor(cents, USD), "ID", "NAME")


def debit(routing: str, cents: int) -> EntryDetail:
    return EntryDetail.of(TransactionCode.CHECKING_DEBIT, routing, "123", Money.of_minor(cents, USD), "ID", "NAME")


def header(service_class: int, sec: SecCode) -> BatchHeader:
    return BatchHeader(service_class, "ACME", "", "1234567890", sec, "PAYROLL", "", "261013", "02199999", 1)


class RoutingNumberTest(unittest.TestCase):
    def test_check_digit_weights_3_7_1(self):
        self.assertEqual(RoutingNumber.check_digit("02199999"), 6)
        self.assertEqual(RoutingNumber.check_digit("01199999"), 3)
        self.assertEqual(RoutingNumber.check_digit("12199999"), 3)

    def test_valid_numbers(self):
        self.assertTrue(RoutingNumber.is_valid("021999996"))
        self.assertTrue(RoutingNumber.is_valid("061999998"))

    def test_transposition_and_single_digit_errors_detected(self):
        self.assertFalse(RoutingNumber.is_valid("021999969"))
        self.assertFalse(RoutingNumber.is_valid("021999997"))

    def test_shape_errors(self):
        self.assertFalse(RoutingNumber.is_valid("02199999"))
        self.assertFalse(RoutingNumber.is_valid("02199999A"))
        with self.assertRaises(NachaFormatError):
            credit("021999969", 100)


class FixedWidthTest(unittest.TestCase):
    def test_alpha_left_justifies_uppercases_truncates(self):
        self.assertEqual(Fixed.alpha("abc", 5), "ABC  ")
        self.assertEqual(Fixed.alpha("ABCDEFG", 5), "ABCDE")

    def test_numeric_right_justifies_and_never_truncates(self):
        self.assertEqual(Fixed.numeric(42, 6), "000042")
        with self.assertRaises(NachaFormatError):
            Fixed.numeric(1234567, 6)

    def test_field_uses_one_based_inclusive_positions(self):
        self.assertEqual(Fixed.field("abcdef", 2, 4), "bcd")


class TransactionCodeTest(unittest.TestCase):
    def test_direction_and_kind(self):
        self.assertIs(TransactionCode.from_code(22), TransactionCode.CHECKING_CREDIT)
        self.assertTrue(TransactionCode.CHECKING_CREDIT.is_credit())
        self.assertTrue(TransactionCode.from_code(27).is_debit())
        self.assertFalse(TransactionCode.from_code(27).is_credit())
        self.assertTrue(TransactionCode.CHECKING_PRENOTE_CREDIT.is_prenote())
        self.assertTrue(TransactionCode.CHECKING_RETURN_DEBIT.is_return())
        self.assertTrue(TransactionCode.CHECKING_RETURN_DEBIT.is_debit())

    def test_unknown_code_rejected(self):
        with self.assertRaises(NachaFormatError):
            TransactionCode.from_code(25)

    def test_entry_validation(self):
        with self.assertRaises(NachaFormatError):
            EntryDetail.of(TransactionCode.CHECKING_PRENOTE_CREDIT, "021999996", "1", Money.of_minor(1, USD), "", "X")
        with self.assertRaises(NachaFormatError):
            EntryDetail.of(TransactionCode.CHECKING_CREDIT, "021999996", "1", Money.of_minor(-1, USD), "", "X")
        with self.assertRaises(NachaFormatError):
            EntryDetail.of(TransactionCode.CHECKING_CREDIT, "021999996", "1", Money.of_minor(10_000_000_000, USD), "", "X")


class RecordLayoutTest(unittest.TestCase):
    def setUp(self):
        self.file = payroll_file()
        self.lines = self.file.to_lines()

    def test_every_line_is_94_characters(self):
        self.assertTrue(all(len(l) == 94 for l in self.lines))

    def test_file_header_fields(self):
        self.assertTrue(self.lines[0].startswith("101"))
        self.assertEqual(Fixed.field(self.lines[0], 35, 40), "094101")

    def test_entry_detail_positions(self):
        entry = self.lines[2]
        self.assertEqual(Fixed.field(entry, 2, 3), "22")
        self.assertEqual(Fixed.field(entry, 4, 12), "011999993")
        self.assertEqual(Fixed.field(entry, 30, 39), "0000251237")
        self.assertEqual(entry[78], "0")
        self.assertEqual(Fixed.field(entry, 80, 94), "021999990000001")


class ControlTotalsTest(unittest.TestCase):
    def setUp(self):
        self.file = payroll_file()
        self.batch = self.file.batches[0]

    def test_entry_hash_sums_8_digit_dfi_ids(self):
        self.assertEqual(self.batch.entry_hash(), 1199999 + 6199999 + 12199999)

    def test_entry_hash_keeps_rightmost_10_digits(self):
        self.assertEqual(entry_hash(["99999999", "99999999", "00000003"]), 200000001)
        self.assertEqual(entry_hash(["99999999"] * 101), (101 * 99999999) % 10_000_000_000)

    def test_counts_and_totals(self):
        self.assertEqual(self.batch.entry_addenda_count(), 4)
        self.assertEqual(self.batch.total_credit(), Money.parse("7387.87", USD))
        self.assertTrue(self.batch.total_debit().is_zero())
        self.assertEqual(self.file.control().total_credit, self.batch.total_credit())
        self.assertEqual(self.file.control().entry_hash, self.batch.entry_hash())

    def test_trace_numbers_are_odfi_id_plus_sequence(self):
        self.assertEqual(self.batch.entries[0].trace_number, "021999990000001")
        self.assertEqual(self.batch.entries[2].trace_number, "021999990000003")

    def test_mixed_batch_splits_totals(self):
        mixed = Batch(header(MIXED, SecCode.CCD), (credit("021999996", 1000), debit("011999993", 250)))
        self.assertEqual(mixed.total_credit().minor_units, 1000)
        self.assertEqual(mixed.total_debit().minor_units, 250)

    def test_service_class_enforced(self):
        with self.assertRaises(NachaFormatError):
            Batch(header(CREDITS_ONLY, SecCode.PPD), (debit("021999996", 1),))


class BlockPaddingTest(unittest.TestCase):
    def test_8_records_one_block_ten_lines(self):
        f = payroll_file()
        self.assertEqual((f.record_count(), f.block_count(), len(f.to_lines())), (8, 1, 10))
        self.assertEqual(f.to_lines()[9], "9" * 94)

    def test_13_records_two_blocks_twenty_lines(self):
        f = payroll_file()
        nine = tuple(credit("021999996", 100) for _ in range(9))
        bigger = AchFile(f.header, (Batch(header(CREDITS_ONLY, SecCode.PPD), nine),))
        self.assertEqual((bigger.record_count(), bigger.block_count(), len(bigger.to_lines())), (13, 2, 20))

    def test_exactly_ten_records_no_filler(self):
        f = payroll_file()
        six = tuple(credit("021999996", 100) for _ in range(6))
        exact = AchFile(f.header, (Batch(header(CREDITS_ONLY, SecCode.PPD), six),))
        self.assertEqual((exact.record_count(), len(exact.to_lines())), (10, 10))
        self.assertFalse(exact.to_lines()[9].startswith("9999"))


class RoundTripTest(unittest.TestCase):
    def setUp(self):
        self.file = payroll_file()
        self.text = self.file.write()

    def test_parse_write_identity(self):
        parsed = AchFile.parse(self.text)
        self.assertEqual(parsed, self.file)
        self.assertEqual(parsed.write(), self.text)

    def test_addenda_survives(self):
        self.assertEqual(AchFile.parse(self.text).batches[0].entries[2].addenda.info, "BONUS INCLUDED Q3")

    def test_crlf_accepted(self):
        self.assertEqual(AchFile.parse(self.text.replace("\n", "\r\n")), self.file)


class TamperingTest(unittest.TestCase):
    def setUp(self):
        self.file = payroll_file()
        self.text = self.file.write()
        self.control = self.file.batches[0].control().to_line()
        self.lines = self.file.to_lines()

    def bumped(self, index: int) -> str:
        c = self.control
        return c[:index] + chr(ord(c[index]) + 1) + c[index + 1 :]

    def test_credit_total_mismatch(self):
        with self.assertRaises(NachaFormatError):
            AchFile.parse(self.text.replace(self.control, self.bumped(43)))

    def test_entry_hash_mismatch(self):
        with self.assertRaises(NachaFormatError):
            AchFile.parse(self.text.replace(self.control, self.bumped(19)))

    def test_entry_count_mismatch(self):
        with self.assertRaises(NachaFormatError):
            AchFile.parse(self.text.replace(self.control, self.control[:9] + "5" + self.control[10:]))

    def test_bad_routing_check_digit_on_parse(self):
        entry = self.lines[2]
        bad = entry[:11] + ("0" if entry[11] == "9" else chr(ord(entry[11]) + 1)) + entry[12:]
        with self.assertRaises(NachaFormatError):
            AchFile.parse(self.text.replace(entry, bad))

    def test_short_line_rejected(self):
        entry = self.lines[2]
        with self.assertRaises(NachaFormatError):
            AchFile.parse(self.text.replace(entry, entry[:93]))

    def test_addenda_after_indicator_zero_rejected(self):
        with self.assertRaises(NachaFormatError):
            AchFile.parse(self.text.replace(self.lines[5], self.lines[2]))

    def test_file_without_control_rejected(self):
        with self.assertRaises(NachaFormatError):
            AchFile.parse("\n".join(self.lines[:7]) + "\n")


class ReturnEntryTest(unittest.TestCase):
    def test_return_with_99_addenda(self):
        original = debit("011999993", 4999).with_trace("02199999", 1)
        returned = EntryDetail.of(TransactionCode.CHECKING_RETURN_DEBIT, "011999993", "123", original.amount, "ID", "NAME").with_addenda(
            Addenda.returned("R01", original.trace_number, "02199999", "")
        )
        batch = Batch(BatchHeader(DEBITS_ONLY, "ACME", "", "1234567890", SecCode.WEB, "RETURN", "", "261015", "01199999", 1), (returned,))
        lines = batch.to_lines()
        self.assertTrue(lines[2].startswith("799R01" + original.trace_number))
        self.assertEqual(Addenda.parse(lines[2]), returned.addenda)
        self.assertEqual(batch.total_debit().minor_units, 4999)
        self.assertEqual(batch.entry_addenda_count(), 2)

    def test_addenda_type_restricted(self):
        with self.assertRaises(NachaFormatError):
            Addenda("02", "", "", "", "")



class ReturnReasonTest(unittest.TestCase):
    THURSDAY = date(2026, 10, 15)

    def test_r01_two_banking_days_and_representable(self):
        r01 = ReturnReason.lookup("R01")
        self.assertEqual(r01.window_days(), 2)
        self.assertTrue(r01.representable)

    def test_r02_r03_not_representable(self):
        self.assertFalse(ReturnReason.lookup("R02").representable)
        self.assertFalse(ReturnReason.lookup("R03").representable)

    def test_r10_extended_window(self):
        r10 = ReturnReason.lookup("R10")
        self.assertTrue(r10.extended_window)
        self.assertEqual(r10.window_days(), 60)

    def test_deadline_skips_weekend(self):
        self.assertEqual(ReturnReason.lookup("R01").deadline(self.THURSDAY), date(2026, 10, 19))

    def test_deadline_sixty_calendar_days(self):
        self.assertEqual(ReturnReason.lookup("R10").deadline(self.THURSDAY), date(2026, 12, 14))

    def test_timeliness(self):
        r01 = ReturnReason.lookup("R01")
        self.assertTrue(r01.is_timely(self.THURSDAY, date(2026, 10, 19)))
        self.assertFalse(r01.is_timely(self.THURSDAY, date(2026, 10, 20)))

    def test_unknown_code(self):
        self.assertFalse(ReturnReason.is_known("R99"))
        with self.assertRaises(NachaFormatError):
            ReturnReason.lookup("R99")

    def test_parsed_return_exposes_reason(self):
        original = debit("011999993", 4999).with_trace("02199999", 1)
        addenda = Addenda.returned("R01", original.trace_number, "02199999", "")
        line = addenda.to_line(1, "011999990000001")
        self.assertEqual(Addenda.parse(line).reason().description, "Insufficient funds")
        with self.assertRaises(NachaFormatError):
            Addenda.payment("X").reason()


if __name__ == "__main__":
    unittest.main()
