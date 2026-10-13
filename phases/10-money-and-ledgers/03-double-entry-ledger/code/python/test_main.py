"""Tests for phases/10-money-and-ledgers/03-double-entry-ledger. Run: python3 -m unittest -v"""
import unittest

from main import (
    EUR,
    USD,
    AccountType,
    AlreadyReversedError,
    IdempotencyConflictError,
    InvalidPostingError,
    JournalEntry,
    Ledger,
    Money,
    Posting,
    RandomEntries,
    Side,
    UnbalancedEntryError,
    UnknownAccountError,
    UnknownEntryError,
)


def usd(text: str) -> Money:
    return Money.parse(text, USD)


def payments_ledger() -> Ledger:
    ledger = Ledger()
    ledger.open_account("1010", "Settlement Bank", AccountType.ASSET, USD)
    ledger.open_account("1200", "Processor Receivable", AccountType.ASSET, USD)
    ledger.open_account("2100", "Merchant Payable", AccountType.LIABILITY, USD)
    ledger.open_account("4100", "Fee Revenue", AccountType.REVENUE, USD)
    ledger.open_account("2200", "EUR Merchant Payable", AccountType.LIABILITY, EUR)
    return ledger


def sale(entry_id: str) -> JournalEntry:
    return JournalEntry(entry_id, "card payment", [
        Posting.debit("1200", usd("100.00")),
        Posting.credit("2100", usd("97.10")),
        Posting.credit("4100", usd("2.90")),
    ])


class NormalBalanceTest(unittest.TestCase):
    def test_debit_normal_types(self):
        self.assertIs(AccountType.ASSET.normal_balance, Side.DEBIT)
        self.assertIs(AccountType.EXPENSE.normal_balance, Side.DEBIT)

    def test_credit_normal_types(self):
        self.assertIs(AccountType.LIABILITY.normal_balance, Side.CREDIT)
        self.assertIs(AccountType.EQUITY.normal_balance, Side.CREDIT)
        self.assertIs(AccountType.REVENUE.normal_balance, Side.CREDIT)


class JournalEntryTest(unittest.TestCase):
    def test_unbalanced_entry_rejected(self):
        with self.assertRaises(UnbalancedEntryError):
            JournalEntry("u1", "x", [Posting.debit("1200", usd("100.00")), Posting.credit("2100", usd("97.10"))])

    def test_mixed_currency_entry_rejected(self):
        with self.assertRaises(UnbalancedEntryError):
            JournalEntry("m1", "x", [Posting.debit("1200", usd("100.00")), Posting.credit("2200", Money.parse("100.00", EUR))])

    def test_single_posting_rejected(self):
        with self.assertRaises(UnbalancedEntryError):
            JournalEntry("s1", "x", [Posting.debit("1200", usd("1.00"))])

    def test_zero_and_negative_postings_rejected(self):
        with self.assertRaises(InvalidPostingError):
            Posting.debit("1200", usd("0.00"))
        with self.assertRaises(InvalidPostingError):
            Posting.credit("1200", usd("-1.00"))

    def test_three_leg_entry_balances(self):
        self.assertEqual(sale("ok").total_debits(USD), usd("100.00"))

    def test_postings_are_immutable(self):
        self.assertIsInstance(sale("i").postings, tuple)


class PostingTest(unittest.TestCase):
    def setUp(self):
        self.ledger = payments_ledger()
        self.assertTrue(self.ledger.post(sale("pay-001")))

    def test_balances_in_normal_direction(self):
        self.assertEqual(self.ledger.balance("1200"), usd("100.00"))
        self.assertEqual(self.ledger.balance("2100"), usd("97.10"))
        self.assertEqual(self.ledger.balance("4100"), usd("2.90"))
        self.assertTrue(self.ledger.balance("1010").is_zero())

    def test_duplicate_entry_id_is_a_no_op(self):
        self.assertFalse(self.ledger.post(sale("pay-001")))
        self.assertEqual(self.ledger.size(), 1)
        self.assertEqual(self.ledger.balance("1200"), usd("100.00"))

    def test_same_id_different_content_is_a_conflict(self):
        with self.assertRaises(IdempotencyConflictError):
            self.ledger.post(JournalEntry("pay-001", "card payment", [
                Posting.debit("1200", usd("50.00")), Posting.credit("2100", usd("50.00"))]))
        self.assertEqual(self.ledger.size(), 1)
        self.assertEqual(self.ledger.balance("1200"), usd("100.00"))

    def test_unknown_account_rejected_without_partial_write(self):
        with self.assertRaises(UnknownAccountError):
            self.ledger.post(JournalEntry("x1", "x", [Posting.debit("9999", usd("1.00")), Posting.credit("2100", usd("1.00"))]))
        self.assertEqual(self.ledger.size(), 1)
        self.assertEqual(self.ledger.balance("2100"), usd("97.10"))

    def test_posting_currency_must_match_account_currency(self):
        with self.assertRaises(InvalidPostingError):
            self.ledger.post(JournalEntry("x2", "x", [Posting.debit("2200", usd("1.00")), Posting.credit("2100", usd("1.00"))]))
        self.assertEqual(self.ledger.size(), 1)

    def test_trial_balance_sums_to_zero_after_lifecycle(self):
        self.ledger.post(JournalEntry("stl-001", "settlement", [Posting.debit("1010", usd("100.00")), Posting.credit("1200", usd("100.00"))]))
        self.ledger.post(JournalEntry("out-001", "payout", [Posting.debit("2100", usd("97.10")), Posting.credit("1010", usd("97.10"))]))
        tb = self.ledger.trial_balance(USD)
        self.assertTrue(tb.balances())
        self.assertEqual(tb.total_debits, usd("2.90"))
        self.assertEqual(self.ledger.balance("1010"), usd("2.90"))
        self.assertTrue(self.ledger.balance("2100").is_zero())

    def test_entries_view_is_a_snapshot(self):
        self.assertIsInstance(self.ledger.entries(), tuple)
        self.assertEqual(self.ledger.entries()[0], sale("pay-001"))


class ReversalTest(unittest.TestCase):
    def setUp(self):
        self.ledger = payments_ledger()
        self.ledger.post(sale("pay-001"))
        self.ledger.post(JournalEntry("stl-001", "settlement", [Posting.debit("1010", usd("100.00")), Posting.credit("1200", usd("100.00"))]))

    def test_reversal_restores_prior_balances(self):
        fee_before, payable_before, bank_before = (self.ledger.balance(a) for a in ("4100", "2100", "1010"))
        reversal = self.ledger.reverse("pay-001", "rev-001", "refund")
        self.assertEqual(reversal.reversal_of, "pay-001")
        self.assertEqual([p.side for p in reversal.postings], [Side.CREDIT, Side.DEBIT, Side.DEBIT])
        self.assertEqual(self.ledger.balance("4100"), fee_before.minus(usd("2.90")))
        self.assertEqual(self.ledger.balance("2100"), payable_before.minus(usd("97.10")))
        self.assertEqual(self.ledger.balance("1010"), bank_before)
        self.assertTrue(self.ledger.trial_balance(USD).balances())

    def test_original_entry_is_never_edited(self):
        self.ledger.reverse("pay-001", "rev-001", "refund")
        self.assertEqual(self.ledger.entry("pay-001"), sale("pay-001"))
        self.assertEqual(self.ledger.size(), 3)

    def test_post_then_reverse_is_identity(self):
        fresh = payments_ledger()
        fresh.post(sale("p"))
        fresh.reverse("p", "r", "undo")
        for account_id in ("1010", "1200", "2100", "4100"):
            self.assertTrue(fresh.balance(account_id).is_zero(), account_id)

    def test_cannot_reverse_twice_or_reverse_unknown(self):
        self.ledger.reverse("pay-001", "rev-001", "refund")
        with self.assertRaises(AlreadyReversedError):
            self.ledger.reverse("pay-001", "rev-002", "again")
        with self.assertRaises(UnknownEntryError):
            self.ledger.reverse("nope", "rev-003", "x")


class StressTest(unittest.TestCase):
    ACCOUNTS = ["1010", "1200", "2100", "4100"]

    def test_trial_balance_zero_after_many_random_entries(self):
        ledger = payments_ledger()
        gen = RandomEntries(7, self.ACCOUNTS, USD)
        for _ in range(5_000):
            ledger.post(gen.next())
        tb = ledger.trial_balance(USD)
        self.assertTrue(tb.balances())
        self.assertTrue(tb.total_debits.is_positive())
        self.assertEqual(sum(ledger.net_debit(a).minor_units for a in self.ACCOUNTS), 0)
        self.assertEqual(ledger.size(), 5_000)

    def test_cached_balances_equal_recomputation(self):
        ledger = payments_ledger()
        gen = RandomEntries(11, self.ACCOUNTS, USD)
        for _ in range(2_000):
            ledger.post(gen.next())
        for account_id in self.ACCOUNTS:
            self.assertEqual(ledger.net_debit(account_id), ledger.net_debit_from_postings(account_id))

    def test_generator_is_deterministic(self):
        self.assertEqual(RandomEntries(7, ["a", "b"], USD).next(), RandomEntries(7, ["a", "b"], USD).next())


if __name__ == "__main__":
    unittest.main()
