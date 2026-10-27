"""Tests for phases/11-payment-rails/09-sepa-and-iso-20022. Run: python3 -m unittest -v"""
import unittest
from datetime import date

from main import (
    EUR, NOW, USD, Bic, CreditTransfer, Iban, InterbankMessage, Iso20022Exception, Money, Pacs008, Pain001,
    Party, PaymentInitiation, SepaRules, demo_directory, demo_initiation,
)


def eur(s: str) -> Money:
    return Money.parse(s, EUR)


def with_transfer(t: CreditTransfer) -> PaymentInitiation:
    p = demo_initiation()
    return PaymentInitiation(p.message_id, p.created_at, p.initiating_party, p.payment_info_id,
                             p.requested_execution_date, p.debtor, (t,))


CRED = Party("Delft Sensors BV", "NL91ABNA0417164300", "ABNANL2A")


class IdentifierTest(unittest.TestCase):
    def test_iban(self):
        self.assertTrue(Iban.is_valid("DE89370400440532013000"))
        self.assertTrue(Iban.is_valid("FR1420041010050500013M02606"))
        self.assertFalse(Iban.is_valid("DE89370400440532013001"), "one digit changed fails mod-97")
        self.assertFalse(Iban.is_valid("DE8937040044053201300"), "wrong length for country")
        self.assertFalse(Iban.is_valid("de89370400440532013000"), "lowercase rejected (normalize first)")

    def test_bic(self):
        self.assertTrue(Bic.is_valid("ABNANL2A"))
        self.assertTrue(Bic.is_valid("COBADEFFXXX"))
        self.assertFalse(Bic.is_valid("COBADEFFX"))

    def test_bank_code(self):
        self.assertEqual(Iban.bank_code("DE89370400440532013000"), "37040044")


class SepaRulesTest(unittest.TestCase):
    def test_demo_initiation_clean(self):
        p = demo_initiation()
        self.assertEqual(SepaRules.violations(p), [])
        self.assertEqual(p.control_sum(), eur("13600.49"))

    def test_currency_and_amount_range(self):
        self.assertTrue(any("must be EUR" in v for v in SepaRules.violations(
            with_transfer(CreditTransfer("E2E-1", Money.parse("1.00", USD), CRED, None)))))
        self.assertTrue(any("out of range" in v for v in SepaRules.violations(
            with_transfer(CreditTransfer("E2E-1", eur("0.00"), CRED, None)))))
        self.assertEqual(SepaRules.violations(with_transfer(CreditTransfer("E2E-1", eur("999999999.99"), CRED, None))), [])
        self.assertNotEqual(SepaRules.violations(with_transfer(CreditTransfer("E2E-1", eur("1000000000.00"), CRED, None))), [])

    def test_character_set_and_identifiers(self):
        self.assertTrue(any("outside the SEPA set" in v for v in SepaRules.violations(with_transfer(
            CreditTransfer("E2E-1", eur("1.00"), Party("Zürich AG", "NL91ABNA0417164300", None), None)))))
        self.assertNotEqual(SepaRules.violations(with_transfer(CreditTransfer("X" * 36, eur("1.00"), CRED, None))), [])
        self.assertNotEqual(SepaRules.violations(with_transfer(CreditTransfer("A//B", eur("1.00"), CRED, None))), [])
        self.assertNotEqual(SepaRules.violations(with_transfer(CreditTransfer("E2E-1", eur("1.00"), CRED, "R" * 141))), [])
        self.assertNotEqual(SepaRules.violations(with_transfer(
            CreditTransfer("E2E-1", eur("1.00"), Party("X", "NL91ABNA0417164301", None), None))), [])

    def test_write_refuses_violations(self):
        with self.assertRaises(Iso20022Exception):
            Pain001.write(with_transfer(CreditTransfer("E2E-1", eur("0.00"), CRED, None)))


class Pain001Test(unittest.TestCase):
    def setUp(self):
        self.p = demo_initiation()
        self.pain = Pain001.write(self.p)

    def test_shape_and_round_trip(self):
        self.assertIn("urn:iso:std:iso:20022:tech:xsd:pain.001.001.09", self.pain)
        self.assertIn('<InstdAmt Ccy="EUR">12000.50</InstdAmt>', self.pain)
        self.assertEqual(len(self.pain.split("<CdtrAgt>")), 3, "IBAN-only creditor has no CdtrAgt")
        self.assertEqual(Pain001.parse(self.pain), self.p)

    def test_totals_mismatch(self):
        with self.assertRaises(Iso20022Exception):
            Pain001.parse(self.pain.replace("<CtrlSum>13600.49</CtrlSum>", "<CtrlSum>13600.50</CtrlSum>"))
        with self.assertRaises(Iso20022Exception):
            Pain001.parse(self.pain.replace("<NbOfTxs>3</NbOfTxs>", "<NbOfTxs>4</NbOfTxs>", 1))

    def test_bad_amounts(self):
        with self.assertRaises(Iso20022Exception):
            Pain001.parse(self.pain.replace("349.99", "349.999"))
        with self.assertRaises(Iso20022Exception):
            Pain001.parse(self.pain.replace('Ccy="EUR">349.99', 'Ccy="USD">349.99'))

    def test_structure(self):
        with self.assertRaises(Iso20022Exception):
            Pain001.parse(self.pain.replace("<PmtInfId>PAYRUN-42</PmtInfId>", ""))
        with self.assertRaises(Iso20022Exception):
            Pain001.parse(self.pain[: len(self.pain) // 2])

    def test_doctype_rejected(self):
        xxe = ('<?xml version="1.0"?><!DOCTYPE d [<!ENTITY x SYSTEM "file:///etc/passwd">]><Document xmlns="'
               + Pain001.NS + '">&x;</Document>')
        with self.assertRaises(Iso20022Exception):
            Pain001.parse(xxe)


class Pacs008Test(unittest.TestCase):
    def setUp(self):
        self.p = demo_initiation()
        self.m: InterbankMessage = Pacs008.from_pain001(self.p, "COBA-1", NOW, demo_directory())

    def test_mapping(self):
        self.assertEqual(self.m.transfers[0].end_to_end_id, "INV-2026-0912")
        self.assertEqual(self.m.transfers[2].transaction_id, "COBA-1-0003")
        self.assertEqual(self.m.settlement_date, date(2026, 10, 14))
        self.assertEqual(self.m.transfers[2].creditor.bic, "CAIXESBBXXX")
        self.assertEqual(self.m.total_settlement_amount(), self.p.control_sum())

    def test_round_trip_and_tamper(self):
        pacs = Pacs008.write(self.m)
        self.assertIn("<ChrgBr>SLEV</ChrgBr>", pacs)
        self.assertIn("<SttlmMtd>CLRG</SttlmMtd>", pacs)
        self.assertEqual(Pacs008.parse(pacs), self.m)
        with self.assertRaises(Iso20022Exception):
            Pacs008.parse(pacs.replace(">13600.49<", ">13600.48<"))

    def test_wrong_message_type(self):
        with self.assertRaises(Iso20022Exception):
            Pacs008.parse(Pain001.write(self.p))

    def test_unknown_bank(self):
        with self.assertRaises(Iso20022Exception):
            Pacs008.from_pain001(with_transfer(CreditTransfer("E2E-1", eur("1.00"), Party("X", "BE68539007547034", None), None)),
                                 "M", NOW, demo_directory())


if __name__ == "__main__":
    unittest.main()
