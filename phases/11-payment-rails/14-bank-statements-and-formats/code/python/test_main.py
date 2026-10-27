"""Tests for phases/11-payment-rails/14-bank-statements-and-formats. Run: python3 -m unittest -v"""
import unittest
from datetime import date

from main import (
    Bai2Parser, Camt053Parser, Mt940Parser, OfxParser, Samples, Statement, StatementChain, StatementFormat,
    StatementParseException, StatementParsers,
)

EXPECTED = [-150000, 325075, -1250]


def amounts(s: Statement) -> list:
    return [line.amount.minor_units for line in s.lines]


class DetectionTest(unittest.TestCase):
    def test_detect(self):
        self.assertIs(StatementFormat.detect(Samples.MT940), StatementFormat.MT940)
        self.assertIs(StatementFormat.detect(Samples.CAMT053), StatementFormat.CAMT053)
        self.assertIs(StatementFormat.detect(Samples.BAI2), StatementFormat.BAI2)
        self.assertIs(StatementFormat.detect(Samples.OFX), StatementFormat.OFX)
        with self.assertRaises(StatementParseException):
            StatementFormat.detect("Date,Amount\n")


class NormalizationTest(unittest.TestCase):
    def test_all_four_formats_agree(self):
        for sample in (Samples.MT940, Samples.CAMT053, Samples.BAI2, Samples.OFX):
            s = StatementParsers.parse(sample)
            with self.subTest(source=s.source):
                self.assertEqual(amounts(s), EXPECTED)
                self.assertEqual(s.closing.minor_units, 1423825)
                self.assertEqual(s.continuity_errors(), [])


class Mt940Test(unittest.TestCase):
    def test_fields(self):
        mt = Mt940Parser.parse(Samples.MT940)
        self.assertEqual(mt.account_id, "DE89370400440532013000")
        self.assertEqual(mt.statement_id, "STMT261012/00287/001")
        self.assertEqual(mt.opening_date, date(2026, 10, 9))
        self.assertEqual(mt.lines[0].description, "Supplier payment INV-4411 Fournitures Lyon SARL", ":86: continuation joined")
        self.assertEqual(mt.lines[2].reference, "", "NONREF becomes empty reference")
        self.assertEqual(mt.lines[1].bank_reference, "COBA0002")

    def test_marks_and_dates(self):
        rc = Mt940Parser.parse(Samples.MT940.replace("2610121012C3250,75", "2610121012RC3250,75")
                               .replace(":62F:C261012EUR14238,25", ":62F:C261012EUR7736,75"))
        self.assertEqual(rc.lines[1].amount.minor_units, -325075, "RC (reversal of credit) is a debit")
        self.assertEqual(rc.continuity_errors(), [])
        year_end = Mt940Parser.parse(Samples.MT940.replace("2610121012D1500,00", "2701021231D1500,00"))
        self.assertEqual(year_end.lines[0].booking_date, date(2026, 12, 31))
        debit_open = Mt940Parser.parse(Samples.MT940.replace(":60F:C261009EUR12500,00", ":60F:D261009EUR100,00")
                                       .replace(":62F:C261012EUR14238,25", ":62F:C261012EUR1638,25"))
        self.assertEqual(debit_open.opening.minor_units, -10000, "D balance is negative (overdrawn)")
        self.assertEqual(debit_open.continuity_errors(), [])

    def test_rejections(self):
        with self.assertRaises(StatementParseException):
            Mt940Parser.parse(Samples.MT940.replace("D12,50NCHG", "D12,505NCHG"))
        with self.assertRaises(StatementParseException):
            Mt940Parser.parse(Samples.MT940.replace(":62F:C261012EUR14238,25\n", ""))


class ContinuityAndChainTest(unittest.TestCase):
    def test_broken_closing(self):
        broken = Mt940Parser.parse(Samples.MT940.replace(":62F:C261012EUR14238,25", ":62F:C261012EUR14283,25"))
        errors = broken.continuity_errors()
        self.assertEqual(len(errors), 1)
        self.assertIn("difference 45.00 EUR", errors[0])

    def test_chains(self):
        chain = Mt940Parser.parse_all(Samples.MT940 + Samples.MT940_DAY2 % ("14238,25", "14338,25"))
        self.assertEqual(len(chain), 2, "two statements in one MT940 file")
        self.assertEqual(StatementChain.gaps(chain), [])
        gapped = Mt940Parser.parse_all(Samples.MT940 + Samples.MT940_DAY2 % ("14250,75", "14350,75"))
        self.assertEqual(gapped[1].continuity_errors(), [], "gapped day 2 is internally continuous")
        gaps = StatementChain.gaps(gapped)
        self.assertEqual(len(gaps), 1)
        self.assertIn("missing 12.50 EUR", gaps[0])
        self.assertNotEqual(StatementChain.gaps([chain[1], chain[0]]), [], "out-of-order chain detected")


class Camt053Test(unittest.TestCase):
    def test_entries(self):
        camt = Camt053Parser.parse(Samples.CAMT053)
        self.assertEqual(len(camt.lines), 3, "PDNG entry excluded")
        self.assertEqual(camt.lines[0].reference, "INV-4411")
        reversed_ = Camt053Parser.parse(Samples.CAMT053.replace(
            "<Sts><Cd>BOOK</Cd></Sts>\n        <BookgDt><Dt>2026-10-12</Dt></BookgDt><ValDt><Dt>2026-10-12</Dt></ValDt>\n        <AcctSvcrRef>COBA0003",
            "<Sts><Cd>BOOK</Cd></Sts><RvslInd>true</RvslInd>\n        <BookgDt><Dt>2026-10-12</Dt></BookgDt><ValDt><Dt>2026-10-12</Dt></ValDt>\n        <AcctSvcrRef>COBA0003"))
        self.assertEqual(reversed_.lines[2].amount.minor_units, 1250, "RvslInd flips the sign")

    def test_rejections(self):
        with self.assertRaises(StatementParseException):
            Camt053Parser.parse(Samples.CAMT053.replace('<Amt Ccy="EUR">3250.75', '<Amt Ccy="USD">3250.75'))
        with self.assertRaises(StatementParseException):
            Camt053Parser.parse('<?xml version="1.0"?><!DOCTYPE d [<!ENTITY x SYSTEM "file:///etc/passwd">]><Document xmlns="'
                                + Camt053Parser.NS + '">&x;</Document>')


class Bai2Test(unittest.TestCase):
    def test_records(self):
        bai = Bai2Parser.parse(Samples.BAI2)
        self.assertEqual(bai.lines[0].description, "Supplier payment INV-4411 Fournitures Lyon SARL", "88 continuation appended")
        self.assertTrue(bai.lines[0].amount.is_negative(), "type 495 is a debit")
        self.assertTrue(bai.lines[1].amount.is_positive(), "type 195 is a credit")

    def test_control_totals(self):
        with self.assertRaises(StatementParseException):
            Bai2Parser.parse(Samples.BAI2.replace("49,3150150,6/", "49,3150151,6/"))
        with self.assertRaises(StatementParseException):
            Bai2Parser.parse(Samples.BAI2.replace("49,3150150,6/", "49,3150150,5/"))
        with self.assertRaises(StatementParseException):
            Bai2Parser.parse(Samples.BAI2.replace("99,3150150,1,10/", "99,3150150,1,9/"))
        with self.assertRaises(StatementParseException):
            Bai2Parser.parse(Samples.BAI2.replace("99,3150150,1,10/\n", ""))


class OfxTest(unittest.TestCase):
    def test_statement(self):
        ofx = OfxParser.parse(Samples.OFX)
        self.assertFalse(ofx.opening_reported, "opening is derived, not reported")
        self.assertEqual(ofx.opening.minor_units, 1250000)
        self.assertEqual(ofx.lines[2].bank_reference, "COBA0003", "FITID is the bank reference")
        ofx_xml = OfxParser.parse(Samples.OFX.replace("<TRNAMT>-12.50", "<TRNAMT>-12.50</TRNAMT>").replace("<CURDEF>USD", "<CURDEF>USD</CURDEF>"))
        self.assertEqual(amounts(ofx_xml), EXPECTED, "OFX 2.x style closing tags on leaves parse the same")

    def test_missing_ledger(self):
        with self.assertRaises(StatementParseException):
            OfxParser.parse(Samples.OFX.replace("<LEDGERBAL><BALAMT>14238.25<DTASOF>20261012220000</LEDGERBAL>", ""))


if __name__ == "__main__":
    unittest.main()
