"""Stage 3 tests for projects/ledger-engine (Python): balances are derived from the journal (signed in the
account's normal direction), snapshots every K entries agree with full replay, balance_at reads any point in
time, the trial balance ties per currency, and reversals mirror without editing history.
Port of Stage03Test.java: same checks, same names, same order."""

import unittest

from main import (
    EUR,
    USD,
    AccountRegistry,
    AccountType,
    JournalEntry,
    Ledger,
    Money,
    Posting,
    Side,
    TrialBalance,
    TrialBalanceLine,
    UnknownAccountError,
    UnknownEntryError,
)


def usd(text: str) -> Money:
    return Money.parse(text, USD)


def eur(text: str) -> Money:
    return Money.parse(text, EUR)


def s3chart() -> AccountRegistry:
    chart = AccountRegistry()
    chart.open("bank", AccountType.ASSET, USD)
    chart.open("receivable", AccountType.ASSET, USD)
    chart.open("sellers", AccountType.LIABILITY, USD)
    chart.open("fees", AccountType.REVENUE, USD)
    chart.open("ops", AccountType.EXPENSE, USD)
    chart.open("eur-bank", AccountType.ASSET, EUR)
    chart.open("eur-capital", AccountType.EQUITY, EUR)
    return chart


def s3ledger() -> Ledger:
    """The four-entry scenario from the stage doc, with a snapshot after every second entry."""
    ledger = Ledger(s3chart(), 2)
    ledger.post(JournalEntry.of("sale-1", "2026-10-01",
                                Posting("bank", Side.DEBIT, usd("100.00")),
                                Posting("sellers", Side.CREDIT, usd("96.80")),
                                Posting("fees", Side.CREDIT, usd("3.20"))))
    ledger.post(JournalEntry.of("payout-1", "2026-10-03",
                                Posting("sellers", Side.DEBIT, usd("50.00")),
                                Posting("bank", Side.CREDIT, usd("50.00"))))
    ledger.post(JournalEntry.of("rent", "2026-10-04",
                                Posting("ops", Side.DEBIT, usd("30.00")),
                                Posting("bank", Side.CREDIT, usd("30.00"))))
    ledger.post(JournalEntry.of("sale-2", "2026-10-05",
                                Posting("bank", Side.DEBIT, usd("20.00")),
                                Posting("fees", Side.CREDIT, usd("20.00"))))
    return ledger


def line(tb: TrialBalance, account_id: str) -> TrialBalanceLine:
    for l in tb.lines:
        if l.account.id == account_id:
            return l
    raise AssertionError(f"no trial balance line for {account_id}")


class Stage03BalancesTest(unittest.TestCase):
    def test_balances_are_signed_in_the_accounts_normal_direction(self):
        ledger = s3ledger()
        self.assertEqual(ledger.balance("bank"), usd("40.00"), "asset balance grows on debit")
        self.assertEqual(ledger.balance("sellers"), usd("46.80"), "liability balance grows on credit")
        self.assertEqual(ledger.balance("fees"), usd("23.20"), "revenue balance grows on credit")
        self.assertEqual(ledger.balance("ops"), usd("30.00"), "expense balance grows on debit")
        self.assertEqual(ledger.balance("receivable"), usd("0.00"), "untouched account is the currency's zero")
        self.assertEqual(ledger.balance("eur-bank"), eur("0.00"), "untouched EUR account is EUR zero")
        with self.assertRaises(UnknownAccountError, msg="balance of unknown account"):
            ledger.balance("nope")

    def test_balance_at_reads_the_journal_as_of_a_sequence_number(self):
        ledger = s3ledger()
        self.assertEqual(ledger.balance_at("bank", 0), usd("0.00"), "balance_at 0 is zero")
        self.assertEqual(ledger.balance_at("bank", 1), usd("100.00"), "balance_at 1")
        self.assertEqual(ledger.balance_at("bank", 2), usd("50.00"), "balance_at 2")
        self.assertEqual(ledger.balance_at("bank", 3), usd("20.00"), "balance_at 3")
        self.assertEqual(ledger.balance_at("bank", 4), ledger.balance("bank"), "balance_at 4 equals balance")
        self.assertEqual(ledger.balance_at("bank", 999), usd("40.00"), "balance_at past the end is the current balance")
        self.assertEqual(ledger.balance_at("sellers", 1), usd("96.80"), "balance_at for a liability")
        with self.assertRaises(UnknownAccountError, msg="balance_at unknown account"):
            ledger.balance_at("nope", 1)

    def test_snapshots_are_taken_after_every_kth_entry_and_hold_every_account(self):
        ledger = s3ledger()
        self.assertEqual(ledger.snapshot_every(), 2, "snapshot_every is kept")
        snaps = ledger.snapshots()
        self.assertTrue(len(snaps) == 2 and snaps[0].sequence == 2 and snaps[1].sequence == 4, "snapshots at 2 and 4")
        self.assertEqual(snaps[0].balances["bank"], usd("50.00"), "snapshot 2 holds bank 50.00")
        self.assertEqual(snaps[0].balances["sellers"], usd("46.80"), "snapshot 2 holds sellers 46.80")
        self.assertTrue(len(snaps[0].balances) == 7 and snaps[0].balances["eur-capital"] == eur("0.00"), "snapshot covers every account")
        self.assertEqual(snaps[1].balances["bank"], ledger.balance_at("bank", 4), "snapshot 4 agrees with balance_at 4")
        with self.assertRaises((AttributeError, TypeError), msg="snapshot balances are read-only"):
            snaps[0].balances.clear()  # type: ignore[attr-defined]
        plain = Ledger(s3chart())
        self.assertTrue(plain.snapshot_every() == Ledger.DEFAULT_SNAPSHOT_EVERY and Ledger.DEFAULT_SNAPSHOT_EVERY == 100, "default snapshot interval is 100")
        self.assertEqual(len(plain.snapshots()), 0, "no snapshot before the interval")
        with self.assertRaises(ValueError, msg="snapshot_every must be >= 1"):
            Ledger(s3chart(), 0)

        # balance() after a snapshot replays only the tail and agrees with full recomputation
        ledger.post(JournalEntry.of("sale-3", "2026-10-06",
                                    Posting("bank", Side.DEBIT, usd("5.00")), Posting("fees", Side.CREDIT, usd("5.00"))))
        self.assertEqual(ledger.balance("bank"), usd("45.00"), "balance after snapshot + tail")
        self.assertEqual(ledger.balance("bank"), ledger.balance_at("bank", ledger.size()), "balance agrees with balance_at(size)")
        self.assertEqual(len(ledger.snapshots()), 2, "no snapshot at an odd sequence")

    def test_the_trial_balance_ties_out_per_currency(self):
        fresh = s3ledger()
        tbs = fresh.trial_balance()
        self.assertTrue(len(tbs) == 2 and tbs[0].currency == EUR and tbs[1].currency == USD, "one trial balance per currency, in code order")
        usd_tb = tbs[1]
        self.assertEqual(usd_tb.total_debits, usd("70.00"), "USD debits total 70.00")
        self.assertEqual(usd_tb.total_credits, usd("70.00"), "USD credits total 70.00")
        self.assertTrue(usd_tb.is_balanced() and usd_tb.difference() == usd("0.00"), "USD trial balance is balanced")
        self.assertEqual(len(usd_tb.lines), 5, "USD trial balance has five lines")
        self.assertTrue(line(usd_tb, "bank").debit == usd("40.00") and line(usd_tb, "bank").credit == usd("0.00"), "bank sits in the debit column")
        self.assertTrue(line(usd_tb, "sellers").credit == usd("46.80") and line(usd_tb, "sellers").debit == usd("0.00"), "sellers sits in the credit column")
        self.assertEqual(line(usd_tb, "fees").credit, usd("23.20"), "fees sits in the credit column")
        self.assertEqual(line(usd_tb, "ops").debit, usd("30.00"), "ops sits in the debit column")
        self.assertTrue(line(usd_tb, "receivable").debit.is_zero() and line(usd_tb, "receivable").credit.is_zero(), "zero account shows zero in both columns")
        eur_tb = tbs[0]
        self.assertTrue(eur_tb.is_balanced() and eur_tb.total_debits == eur("0.00") and len(eur_tb.lines) == 2, "empty EUR trial balance is balanced at zero")
        with self.assertRaises((AttributeError, TypeError), msg="trial balance lines are read-only"):
            usd_tb.lines.clear()  # type: ignore[attr-defined]

        # a liability driven negative moves to the debit column; the totals still tie
        fresh.post(JournalEntry.of("over-payout", "2026-10-06",
                                   Posting("sellers", Side.DEBIT, usd("60.00")), Posting("bank", Side.CREDIT, usd("60.00"))))
        self.assertEqual(fresh.balance("sellers"), usd("-13.20"), "overdrawn liability has a negative signed balance")
        after = fresh.trial_balance()[1]
        self.assertTrue(line(after, "sellers").debit == usd("13.20") and line(after, "sellers").credit.is_zero(), "negative liability shows in the debit column")
        self.assertTrue(line(after, "bank").credit == usd("20.00") and line(after, "bank").debit.is_zero(), "negative asset shows in the credit column")
        self.assertTrue(after.is_balanced() and after.total_debits == usd("43.20"), "trial balance still ties after the overdraw")

        # EUR activity lands only in the EUR trial balance
        fresh.post(JournalEntry.of("capital", "2026-10-06",
                                   Posting("eur-bank", Side.DEBIT, eur("10.00")), Posting("eur-capital", Side.CREDIT, eur("10.00"))))
        both = fresh.trial_balance()
        self.assertTrue(both[0].total_debits == eur("10.00") and both[0].is_balanced(), "EUR trial balance ties at 10.00")
        self.assertEqual(both[1].total_debits, usd("43.20"), "USD totals unchanged by EUR entry")

    def test_reversals_post_the_mirror_never_edit_the_original(self):
        rev = s3ledger()
        reversal = rev.reverse("rent")
        self.assertTrue(reversal.id == "rev:rent" and Ledger.reversal_id("rent") == "rev:rent", "reversal id is rev:<id>")
        self.assertTrue(reversal.sequence == 5 and rev.size() == 5, "reversal gets the next sequence")
        self.assertEqual(reversal.entry.date, "2026-10-04", "reversal keeps the original date")
        self.assertEqual(tuple(reversal.entry.postings),
                         (Posting("ops", Side.CREDIT, usd("30.00")), Posting("bank", Side.DEBIT, usd("30.00"))),
                         "reversal mirrors every posting")
        self.assertTrue(rev.entry("rent").sequence == 3 and rev.entry("rent").entry.postings[0].side is Side.DEBIT, "original is untouched")
        self.assertTrue(rev.balance("ops") == usd("0.00") and rev.balance("bank") == usd("70.00"), "reversal undoes the balance effect")
        self.assertTrue(rev.balance_at("ops", 3) == usd("30.00") and rev.balance_at("ops", 5).is_zero(), "history still shows the original at sequence 3")
        self.assertTrue(rev.is_reversed("rent") and not rev.is_reversed("sale-1"), "is_reversed")
        again = rev.reverse("rent")
        self.assertTrue(again == reversal and rev.size() == 5, "reversing twice returns the first reversal")
        with self.assertRaises(UnknownEntryError, msg="reversing an unknown entry"):
            rev.reverse("nope")
        self.assertTrue(rev.trial_balance()[1].is_balanced() and rev.trial_balance()[1].total_debits == usd("70.00"), "trial balance ties after a reversal")
        self.assertEqual(rev.journal()[4].id, "rev:rent", "reversal is a balanced entry in the journal")
        rr = rev.reverse("rev:rent")
        self.assertTrue(rr.id == "rev:rev:rent" and rev.balance("ops") == usd("30.00"), "a reversal can itself be reversed")


if __name__ == "__main__":
    unittest.main()
