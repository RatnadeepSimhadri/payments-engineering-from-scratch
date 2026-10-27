"""Stage 2 tests for projects/ledger-engine (Python): postings and entries are values, the journal only accepts
balanced single-currency entries, sequence numbers are dense, posting is idempotent on id.
Port of Stage02Test.java: same checks, same names, same order."""

import unittest

from main import (
    EUR,
    USD,
    AccountRegistry,
    AccountType,
    CurrencyMismatchError,
    InvalidEntryError,
    JournalEntry,
    Ledger,
    LedgerError,
    Money,
    Posting,
    Side,
    UnbalancedEntryError,
    UnknownAccountError,
    UnknownEntryError,
)


def usd(text: str) -> Money:
    return Money.parse(text, USD)


def s2chart() -> AccountRegistry:
    chart = AccountRegistry()
    chart.open("bank", AccountType.ASSET, USD)
    chart.open("sellers", AccountType.LIABILITY, USD)
    chart.open("fees", AccountType.REVENUE, USD)
    chart.open("eur-bank", AccountType.ASSET, EUR)
    chart.open("eur-fees", AccountType.REVENUE, EUR)
    return chart


class Stage02JournalTest(unittest.TestCase):
    def test_postings_are_positive_amounts_on_one_side_of_one_account(self):
        debit_bank = Posting("bank", Side.DEBIT, usd("100.00"))
        self.assertTrue(debit_bank.account_id == "bank" and debit_bank.side is Side.DEBIT and debit_bank.amount == usd("100.00"), "posting keeps fields")
        self.assertEqual(debit_bank.mirrored(), Posting("bank", Side.CREDIT, usd("100.00")), "mirrored flips the side only")
        with self.assertRaises(ValueError, msg="zero posting rejected"):
            Posting("bank", Side.DEBIT, usd("0.00"))
        with self.assertRaises(ValueError, msg="negative posting rejected"):
            Posting("bank", Side.DEBIT, usd("-1.00"))
        with self.assertRaises(ValueError, msg="blank posting account rejected"):
            Posting(" ", Side.DEBIT, usd("1.00"))

    def test_entries_are_immutable_values_that_do_not_check_balance_themselves(self):
        debit_bank = Posting("bank", Side.DEBIT, usd("100.00"))
        legs = [debit_bank, Posting("fees", Side.CREDIT, usd("100.00"))]
        sale = JournalEntry("sale-1", "2026-10-01", legs)
        legs.clear()
        self.assertEqual(len(sale.postings), 2, "entry copies its postings")
        with self.assertRaises((AttributeError, TypeError), msg="entry postings are read-only"):
            sale.postings.append(debit_bank)  # type: ignore[attr-defined]
        self.assertEqual(sale.currency(), USD, "entry currency is the first posting's")
        with self.assertRaises(ValueError, msg="blank entry id rejected"):
            JournalEntry.of(" ", "2026-10-01", debit_bank)
        with self.assertRaises(ValueError, msg="blank entry date rejected"):
            JournalEntry.of("x", "", debit_bank)
        unbalanced = JournalEntry.of("bad", "2026-10-01", debit_bank, Posting("fees", Side.CREDIT, usd("99.99")))
        self.assertEqual(len(unbalanced.postings), 2, "constructing an unbalanced entry is allowed; posting it is not")

    def test_the_journal_accepts_balanced_entries_and_numbers_them(self):
        debit_bank = Posting("bank", Side.DEBIT, usd("100.00"))
        sale = JournalEntry.of("sale-1", "2026-10-01", debit_bank, Posting("fees", Side.CREDIT, usd("100.00")))
        unbalanced = JournalEntry.of("bad", "2026-10-01", debit_bank, Posting("fees", Side.CREDIT, usd("99.99")))
        ledger = Ledger(s2chart())
        self.assertTrue(ledger.size() == 0 and len(ledger.journal()) == 0, "new ledger is empty")
        first = ledger.post(sale)
        self.assertEqual(first.sequence, 1, "first entry gets sequence 1")
        self.assertTrue(first.entry == sale and first.id == "sale-1", "posted entry carries the entry")
        second = ledger.post(JournalEntry.of("sale-2", "2026-10-02",
                                             Posting("bank", Side.DEBIT, usd("100.00")),
                                             Posting("sellers", Side.CREDIT, usd("96.80")),
                                             Posting("fees", Side.CREDIT, usd("3.20"))))
        self.assertEqual(second.sequence, 2, "three-leg entry balances 100.00 = 96.80 + 3.20")
        self.assertEqual(ledger.size(), 2, "size counts posted entries")
        self.assertEqual([p.id for p in ledger.journal()], ["sale-1", "sale-2"], "journal is in sequence order")
        self.assertEqual(ledger.entry("sale-2").sequence, 2, "entry(id) finds a posted entry")
        self.assertTrue(ledger.has_entry("sale-1") and not ledger.has_entry("nope"), "has_entry")
        with self.assertRaises(UnknownEntryError, msg="unknown entry id"):
            ledger.entry("nope")
        with self.assertRaises((AttributeError, TypeError), msg="journal() is read-only"):
            ledger.journal().clear()  # type: ignore[attr-defined]

        # posting the same id twice is a no-op that returns the original
        again = ledger.post(JournalEntry.of("sale-1", "2026-12-31",
                                            Posting("bank", Side.DEBIT, usd("1.00")), Posting("fees", Side.CREDIT, usd("1.00"))))
        self.assertEqual(again, first, "duplicate id returns the existing record")
        self.assertEqual(ledger.size(), 2, "duplicate id does not grow the journal")
        self.assertEqual(ledger.entry("sale-1").entry.date, "2026-10-01", "duplicate id does not replace the content")

        # rejections leave the journal untouched and consume no sequence number
        with self.assertRaises(UnbalancedEntryError, msg="unbalanced entry rejected"):
            ledger.post(unbalanced)
        self.assertTrue(not ledger.has_entry("bad") and ledger.size() == 2, "rejected entry is not in the journal")
        with self.assertRaises(InvalidEntryError, msg="single posting rejected"):
            ledger.post(JournalEntry.of("one", "2026-10-01", debit_bank))
        with self.assertRaises(InvalidEntryError, msg="no postings rejected"):
            ledger.post(JournalEntry("none", "2026-10-01", []))
        with self.assertRaises(UnknownAccountError, msg="unknown account rejected"):
            ledger.post(JournalEntry.of("ghost", "2026-10-01",
                                        Posting("ghost", Side.DEBIT, usd("1.00")), Posting("fees", Side.CREDIT, usd("1.00"))))
        with self.assertRaises(CurrencyMismatchError, msg="mixed currencies rejected"):
            ledger.post(JournalEntry.of("mixed", "2026-10-01",
                                        Posting("bank", Side.DEBIT, usd("1.00")), Posting("eur-fees", Side.CREDIT, Money.parse("1.00", EUR))))
        with self.assertRaises(CurrencyMismatchError, msg="USD posting to EUR account rejected"):
            ledger.post(JournalEntry.of("wrong-acct", "2026-10-01",
                                        Posting("eur-bank", Side.DEBIT, usd("1.00")), Posting("fees", Side.CREDIT, usd("1.00"))))
        self.assertEqual(ledger.size(), 2, "rejections consume no sequence numbers")
        third = ledger.post(JournalEntry.of("eur-sale", "2026-10-03",
                                            Posting("eur-bank", Side.DEBIT, Money.parse("10.00", EUR)),
                                            Posting("eur-fees", Side.CREDIT, Money.parse("10.00", EUR))))
        self.assertEqual(third.sequence, 3, "next accepted entry is sequence 3")
        self.assertTrue(ledger.size() == 3 and ledger.entry("eur-sale").entry.currency() == EUR, "a second currency lives in the same journal")

    def test_every_rejection_is_a_ledger_error(self):
        self.assertTrue(issubclass(UnbalancedEntryError, LedgerError), "Unbalanced is a LedgerError")
        self.assertTrue(issubclass(InvalidEntryError, LedgerError), "InvalidEntry is a LedgerError")
        self.assertTrue(issubclass(UnknownEntryError, LedgerError), "UnknownEntry is a LedgerError")
        with self.assertRaises((ValueError, TypeError), msg="ledger needs a registry"):
            Ledger(None)  # type: ignore[arg-type]


if __name__ == "__main__":
    unittest.main()
