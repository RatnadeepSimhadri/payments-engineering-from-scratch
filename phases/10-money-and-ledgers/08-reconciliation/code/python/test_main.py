"""Tests for phases/10-money-and-ledgers/08-reconciliation. Run: python3 -m unittest -v"""
import random
import unittest
from datetime import date

from main import (EUR, USD, Break, BreakKind, CurrencyMismatchError, LedgerPosting, Match, MatchRule, Money, Reconciler,
                  StatementLine)


def ours(ref: str, day: str, amount: str) -> LedgerPosting:
    return LedgerPosting(ref, date.fromisoformat(day), Money.parse(amount, USD), "1010")


def theirs(ref: str, day: str, amount: str) -> StatementLine:
    return StatementLine(ref, date.fromisoformat(day), Money.parse(amount, USD), "stmt")


def demo_ledger():
    return [ours("ch_1001", "2026-03-02", "19.99"), ours("ch_1002", "2026-03-02", "250.00"),
            ours("ch_1003", "2026-03-02", "7.50"), ours("ch_1004", "2026-03-03", "120.00"),
            ours("ch_1005", "2026-03-03", "49.95"), ours("ch_1006", "2026-03-03", "1500.00"),
            ours("ch_1007", "2026-03-04", "33.10"), ours("int_8841", "2026-03-04", "64.00"),
            ours("ch_1009", "2026-03-04", "88.88"), ours("ch_1010", "2026-03-05", "12.00")]


def demo_statement():
    return [theirs("ch_1001", "2026-03-02", "19.99"), theirs("ch_1002", "2026-03-02", "250.00"),
            theirs("ch_1003", "2026-03-02", "7.50"), theirs("ch_1004", "2026-03-03", "120.00"),
            theirs("ch_1005", "2026-03-03", "49.95"), theirs("ch_1006", "2026-03-03", "1500.00"),
            theirs("ch_1007", "2026-03-04", "33.10"), theirs("ch_1007", "2026-03-04", "33.10"),
            theirs("txn_77f2", "2026-03-05", "64.00"), theirs("ch_1010", "2026-03-05", "21.00"),
            theirs("ch_1011", "2026-03-05", "5.00")]


R = Reconciler(USD, 1)


class HappyPathTest(unittest.TestCase):
    def test_identical_books_produce_no_breaks(self):
        clean = R.reconcile([ours("a", "2026-03-02", "10.00"), ours("b", "2026-03-02", "20.00")],
                            [theirs("b", "2026-03-02", "20.00"), theirs("a", "2026-03-02", "10.00")])
        self.assertEqual(len(clean.matches), 2)
        self.assertTrue(clean.is_clean())
        self.assertTrue(clean.ties_out())
        self.assertEqual(clean.matched_total(), Money.parse("30.00", USD))
        self.assertEqual(clean.matches[0].ours.reference, "a")
        self.assertTrue(all(m.rule is MatchRule.EXACT_REFERENCE for m in clean.matches))


class MissingTest(unittest.TestCase):
    def test_missing_on_each_side(self):
        missing = R.reconcile([ours("a", "2026-03-02", "10.00"), ours("only-ours", "2026-03-02", "88.88")],
                              [theirs("a", "2026-03-02", "10.00"), theirs("only-theirs", "2026-03-02", "5.00")])
        self.assertEqual(missing.count_breaks(BreakKind.MISSING_THEIRS), 1)
        self.assertEqual(missing.count_breaks(BreakKind.MISSING_OURS), 1)
        self.assertIs(missing.breaks[0].kind, BreakKind.MISSING_OURS)
        self.assertEqual(missing.ours_unmatched(), Money.parse("88.88", USD))
        self.assertEqual(missing.theirs_unmatched(), Money.parse("5.00", USD))
        self.assertEqual(missing.net_difference(), Money.parse("83.88", USD))
        self.assertTrue(missing.ties_out())


class AmountMismatchTest(unittest.TestCase):
    def test_shared_reference_different_amount(self):
        mismatch = R.reconcile([ours("x", "2026-03-05", "12.00")], [theirs("x", "2026-03-05", "21.00")])
        self.assertEqual(mismatch.count_breaks(BreakKind.AMOUNT_MISMATCH), 1)
        self.assertEqual(len(mismatch.matches), 0)
        self.assertEqual(mismatch.breaks[0].difference(USD), Money.parse("-9.00", USD))
        self.assertEqual(mismatch.ours_unmatched().minor_units, 1200)
        self.assertEqual(mismatch.theirs_unmatched().minor_units, 2100)
        self.assertTrue(mismatch.ties_out())


class FuzzyMatchTest(unittest.TestCase):
    def test_amount_and_date_within_tolerance(self):
        fuzzy = R.reconcile([ours("int_1", "2026-03-04", "64.00")], [theirs("txn_9", "2026-03-05", "64.00")])
        self.assertEqual(len(fuzzy.matches), 1)
        self.assertTrue(fuzzy.is_clean())
        self.assertIs(fuzzy.matches[0].rule, MatchRule.AMOUNT_DATE)

    def test_outside_tolerance(self):
        too_far = R.reconcile([ours("int_1", "2026-03-04", "64.00")], [theirs("txn_9", "2026-03-06", "64.00")])
        self.assertEqual(len(too_far.matches), 0)
        self.assertEqual(len(too_far.breaks), 2)
        zero_tol = Reconciler(USD, 0).reconcile([ours("int_1", "2026-03-04", "64.00")], [theirs("txn_9", "2026-03-05", "64.00")])
        self.assertEqual(len(zero_tol.matches), 0)

    def test_nearest_date_wins(self):
        nearest = R.reconcile([ours("int_1", "2026-03-04", "64.00")],
                              [theirs("far", "2026-03-05", "64.00"), theirs("near", "2026-03-04", "64.00")])
        self.assertEqual(nearest.matches[0].theirs.reference, "near")
        self.assertIs(nearest.breaks[0].kind, BreakKind.MISSING_OURS)
        self.assertEqual(nearest.breaks[0].theirs.reference, "far")

    def test_one_cent_difference_is_never_a_match(self):
        wrong_amount = R.reconcile([ours("int_1", "2026-03-04", "64.00")], [theirs("txn_9", "2026-03-04", "64.01")])
        self.assertEqual(len(wrong_amount.matches), 0)


class DuplicateTest(unittest.TestCase):
    def test_duplicates(self):
        dup_theirs = R.reconcile([ours("d", "2026-03-04", "33.10")],
                                 [theirs("d", "2026-03-04", "33.10"), theirs("d", "2026-03-04", "33.10")])
        self.assertEqual(len(dup_theirs.matches), 1)
        self.assertEqual(dup_theirs.count_breaks(BreakKind.DUPLICATE_THEIRS), 1)
        self.assertEqual(dup_theirs.net_difference().minor_units, -3310)
        self.assertTrue(dup_theirs.ties_out())
        dup_ours = R.reconcile([ours("d", "2026-03-04", "33.10"), ours("d", "2026-03-04", "33.10")],
                               [theirs("d", "2026-03-04", "33.10")])
        self.assertEqual(dup_ours.count_breaks(BreakKind.DUPLICATE_OURS), 1)
        both_twice = R.reconcile([ours("d", "2026-03-04", "33.10"), ours("d", "2026-03-04", "33.10")],
                                 [theirs("d", "2026-03-04", "33.10"), theirs("d", "2026-03-04", "33.10")])
        self.assertEqual(len(both_twice.matches), 2)
        self.assertTrue(both_twice.is_clean())


class DemoDataTest(unittest.TestCase):
    def test_partition_invariant(self):
        demo = R.reconcile(demo_ledger(), demo_statement())
        self.assertEqual(len(demo.matches), 8)
        self.assertEqual(len(demo.breaks), 4)
        self.assertEqual(len(demo.matches) + sum(1 for b in demo.breaks if b.ours is not None), len(demo_ledger()))
        self.assertEqual(len(demo.matches) + sum(1 for b in demo.breaks if b.theirs is not None), len(demo_statement()))
        self.assertTrue(demo.ties_out())
        self.assertEqual(demo.net_difference(), Money.parse("41.78", USD))

    def test_shuffled_input_renders_the_same_report(self):
        demo = R.reconcile(demo_ledger(), demo_statement())
        shuffled_ledger, shuffled_statement = demo_ledger(), demo_statement()
        random.Random(7).shuffle(shuffled_ledger)
        random.Random(11).shuffle(shuffled_statement)
        again = R.reconcile(shuffled_ledger, shuffled_statement)
        self.assertEqual(again.render(), demo.render())
        kinds = [b.kind.value for b in demo.breaks]
        self.assertEqual(kinds, sorted(kinds))


class FailureTest(unittest.TestCase):
    def test_failures_the_lesson_warns_about(self):
        with self.assertRaises(CurrencyMismatchError):
            R.reconcile([LedgerPosting("e", date.fromisoformat("2026-03-04"), Money.parse("1.00", EUR), "1010")], [])
        with self.assertRaises(ValueError):
            Reconciler(USD, -1)
        with self.assertRaises(ValueError):
            Match(ours("a", "2026-03-04", "1.00"), theirs("a", "2026-03-04", "2.00"), MatchRule.EXACT_REFERENCE)
        with self.assertRaises(ValueError):
            Break(BreakKind.MISSING_OURS, ours("a", "2026-03-04", "1.00"), None)
        empty = R.reconcile([], [])
        self.assertTrue(empty.ties_out())
        self.assertTrue(empty.ledger_total.is_zero())
        self.assertTrue(empty.is_clean())


if __name__ == "__main__":
    unittest.main()
