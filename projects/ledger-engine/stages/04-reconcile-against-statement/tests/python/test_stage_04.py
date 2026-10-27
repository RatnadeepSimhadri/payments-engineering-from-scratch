"""Stage 4 tests for projects/ledger-engine (Python): an account's ledger lines net each entry's effect, reconcile
matches by reference then by amount within a date window, every break lands in exactly one category, and the
report's totals tie out. Port of Stage04Test.java: same checks, same names, same order."""

import unittest

from main import (
    EUR,
    USD,
    AccountRegistry,
    AccountType,
    BreakType,
    CurrencyMismatchError,
    JournalEntry,
    Ledger,
    LedgerError,
    MatchedBy,
    Money,
    Posting,
    ReconciliationBreak,
    ReconciliationReport,
    Side,
    StatementLine,
    UnknownAccountError,
)


def usd(text: str) -> Money:
    return Money.parse(text, USD)


def eur(text: str) -> Money:
    return Money.parse(text, EUR)


def st(ref: str, amount: str, date: str) -> StatementLine:
    return StatementLine(ref, usd(amount), date)


def s4chart() -> AccountRegistry:
    chart = AccountRegistry()
    chart.open("bank", AccountType.ASSET, USD)
    chart.open("sellers", AccountType.LIABILITY, USD)
    chart.open("fees", AccountType.REVENUE, USD)
    chart.open("eur-bank", AccountType.ASSET, EUR)
    return chart


def s4ledger() -> Ledger:
    """The seven-entry scenario from the stage doc. Bank lines: +100, -50, +20, -7.50, +15, +6 = 83.50."""
    ledger = Ledger(s4chart())
    ledger.post(JournalEntry.of("sale-1", "2026-10-01",
                                Posting("bank", Side.DEBIT, usd("100.00")), Posting("sellers", Side.CREDIT, usd("96.80")), Posting("fees", Side.CREDIT, usd("3.20"))))
    ledger.post(JournalEntry.of("payout-1", "2026-10-03",
                                Posting("sellers", Side.DEBIT, usd("50.00")), Posting("bank", Side.CREDIT, usd("50.00"))))
    ledger.post(JournalEntry.of("sale-2", "2026-10-04",
                                Posting("bank", Side.DEBIT, usd("20.00")), Posting("fees", Side.CREDIT, usd("20.00"))))
    ledger.post(JournalEntry.of("fee-adj", "2026-10-04",
                                Posting("fees", Side.DEBIT, usd("1.00")), Posting("sellers", Side.CREDIT, usd("1.00"))))
    ledger.post(JournalEntry.of("refund-1", "2026-10-06",
                                Posting("sellers", Side.DEBIT, usd("7.50")), Posting("bank", Side.CREDIT, usd("7.50"))))
    ledger.post(JournalEntry.of("sale-3", "2026-10-08",
                                Posting("bank", Side.DEBIT, usd("15.00")), Posting("fees", Side.CREDIT, usd("15.00"))))
    ledger.post(JournalEntry.of("multi", "2026-10-09",
                                Posting("bank", Side.DEBIT, usd("10.00")), Posting("bank", Side.CREDIT, usd("4.00")), Posting("fees", Side.CREDIT, usd("6.00"))))
    return ledger


def s4statement() -> list[StatementLine]:
    return [
        st("sale-1", "100.00", "2026-10-01"),  # match by ref
        st("PO-77", "-50.00", "2026-10-05"),  # match payout-1 by amount within 3 days
        st("sale-2", "25.00", "2026-10-04"),  # amount mismatch (ledger has 20.00)
        st("sale-1", "100.00", "2026-10-01"),  # duplicate reference
        st("chargeback-3", "-12.00", "2026-10-07"),  # missing in ledger
        st("sale-3", "15.00", "2026-10-08"),  # match by ref
        st("RF-9", "-7.50", "2026-10-20"),  # 14 days from refund-1: outside a 3-day window
    ]


def break_for(report: ReconciliationReport, ref: str) -> ReconciliationBreak:
    for b in report.breaks:
        if b.ref == ref:
            return b
    raise AssertionError(f"no break for {ref}")


class Stage04ReconciliationTest(unittest.TestCase):
    def test_statement_lines_are_values(self):
        line = st("sale-1", "100.00", "2026-10-01")
        self.assertTrue(line.ref == "sale-1" and line.amount == usd("100.00") and line.date == "2026-10-01", "statement line keeps fields")
        with self.assertRaises(ValueError, msg="blank statement ref rejected"):
            st(" ", "1.00", "2026-10-01")
        with self.assertRaises(ValueError, msg="blank statement date rejected"):
            st("x", "1.00", "")

    def test_ledger_lines_one_per_entry_that_touches_the_account(self):
        ledger = s4ledger()
        lines = ledger.lines("bank")
        self.assertEqual(len(lines), 6, "six entries touch bank")
        self.assertTrue(lines[0].ref == "sale-1" and lines[1].ref == "payout-1" and lines[5].ref == "multi", "lines are in journal order")
        self.assertTrue(lines[0].amount == usd("100.00") and lines[0].date == "2026-10-01", "asset debit is positive")
        self.assertEqual(lines[1].amount, usd("-50.00"), "asset credit is negative")
        self.assertEqual(lines[5].amount, usd("6.00"), "an entry touching the account twice nets to one line")
        self.assertFalse(any(l.ref == "fee-adj" for l in lines), "entries that skip the account produce no line")
        self.assertTrue(ledger.lines("sellers")[0].amount == usd("96.80") and ledger.lines("sellers")[1].amount == usd("-50.00"), "liability lines are signed on the credit side")
        self.assertEqual(len(ledger.lines("eur-bank")), 0, "untouched account has no lines")
        with self.assertRaises(UnknownAccountError, msg="lines of unknown account"):
            ledger.lines("nope")

    def test_the_full_reconciliation_three_matches_six_breaks_totals_tie_out(self):
        ledger = s4ledger()
        report = ledger.reconcile("bank", s4statement(), 3)
        self.assertTrue(report.account_id == "bank" and report.currency == USD, "report names the account and currency")
        self.assertEqual(report.ledger_total, usd("83.50"), "ledger total is the sum of the lines")
        self.assertEqual(report.statement_total, usd("170.50"), "statement total is the sum of every statement line, duplicates included")
        self.assertEqual(len(report.matches), 3, "three matches")
        self.assertTrue(report.matches[0].ledger_ref == "sale-1" and report.matches[0].by is MatchedBy.REF
                        and report.matches[1].ledger_ref == "sale-3" and report.matches[1].by is MatchedBy.REF, "pass 1 matches come first")
        by_amount = report.matches[2]
        self.assertTrue(by_amount.ledger_ref == "payout-1" and by_amount.statement_ref == "PO-77"
                        and by_amount.amount == usd("-50.00") and by_amount.by is MatchedBy.AMOUNT_AND_DATE, "pass 2 matches by amount within the window")
        self.assertEqual(report.matched_total(), usd("65.00"), "matched total")
        self.assertEqual(len(report.breaks), 6, "six breaks")
        mismatch = break_for(report, "sale-2")
        self.assertTrue(mismatch.type is BreakType.AMOUNT_MISMATCH and mismatch.ledger_amount == usd("20.00") and mismatch.statement_amount == usd("25.00"), "amount mismatch carries both sides")
        dup = break_for(report, "sale-1")
        self.assertTrue(dup.type is BreakType.DUPLICATE and dup.ledger_amount.is_zero() and dup.statement_amount == usd("100.00"), "duplicate statement reference")
        chargeback = break_for(report, "chargeback-3")
        self.assertTrue(chargeback.type is BreakType.MISSING_IN_LEDGER and chargeback.ledger_amount.is_zero() and chargeback.statement_amount == usd("-12.00"), "missing in ledger")
        self.assertIs(break_for(report, "RF-9").type, BreakType.MISSING_IN_LEDGER, "outside the window is missing in ledger")
        refund = break_for(report, "refund-1")
        self.assertTrue(refund.type is BreakType.MISSING_IN_STATEMENT and refund.ledger_amount == usd("-7.50") and refund.statement_amount.is_zero(), "missing in statement")
        self.assertTrue(break_for(report, "multi").type is BreakType.MISSING_IN_STATEMENT and break_for(report, "multi").ledger_amount == usd("6.00"), "netted multi-leg entry is missing in statement")
        self.assertTrue(len(report.breaks_of(BreakType.MISSING_IN_LEDGER)) == 2 and len(report.breaks_of(BreakType.MISSING_IN_STATEMENT)) == 2
                        and len(report.breaks_of(BreakType.AMOUNT_MISMATCH)) == 1 and len(report.breaks_of(BreakType.DUPLICATE)) == 1, "breaks_of filters by category")
        self.assertEqual(report.ledger_break_total(), usd("18.50"), "ledger break total")
        self.assertEqual(report.statement_break_total(), usd("105.50"), "statement break total")
        self.assertEqual(report.ledger_total, report.matched_total().plus(report.ledger_break_total()), "ledger total = matched + ledger breaks")
        self.assertEqual(report.statement_total, report.matched_total().plus(report.statement_break_total()), "statement total = matched + statement breaks")
        self.assertTrue(report.ties_out() and not report.is_clean(), "report ties out but is not clean")
        with self.assertRaises((AttributeError, TypeError), msg="report breaks are read-only"):
            report.breaks.clear()  # type: ignore[attr-defined]
        self.assertTrue(ledger.size() == 7 and len(ledger.lines("bank")) == 6, "reconcile does not change the journal")

    def test_the_date_window_is_inclusive_and_symmetric(self):
        ledger = s4ledger()
        wide = ledger.reconcile("bank", s4statement(), 14)
        self.assertTrue(len(wide.matches) == 4 and len(wide.breaks) == 4
                        and wide.matches[3].ledger_ref == "refund-1" and wide.matches[3].statement_ref == "RF-9", "14-day window matches RF-9 to refund-1")
        self.assertTrue(wide.ties_out() and wide.matched_total() == usd("57.50"), "wide report still ties out")
        narrow = ledger.reconcile("bank", s4statement(), 13)
        self.assertEqual(len(narrow.matches), 3, "13-day window does not")
        zero = ledger.reconcile("bank", [st("PO-77", "-50.00", "2026-10-03")], 0)
        self.assertTrue(len(zero.matches) == 1 and zero.matches[0].by is MatchedBy.AMOUNT_AND_DATE, "zero window matches the same day")
        day_off = ledger.reconcile("bank", [st("PO-77", "-50.00", "2026-10-02")], 0)
        self.assertTrue(len(day_off.matches) == 0 and break_for(day_off, "PO-77").type is BreakType.MISSING_IN_LEDGER, "zero window rejects the day before")
        sign = ledger.reconcile("bank", [st("PO-77", "50.00", "2026-10-03")], 3)
        self.assertEqual(len(sign.matches), 0, "pass 2 compares signed amounts")

    def test_clean_empty_and_no_line_statements(self):
        ledger = s4ledger()
        lines = ledger.lines("bank")
        # a clean statement: every line matched, nothing to report
        clean = [StatementLine(l.ref, l.amount, l.date) for l in lines]
        clean_report = ledger.reconcile("bank", clean, 0)
        self.assertTrue(len(clean_report.matches) == 6 and clean_report.is_clean() and clean_report.ties_out(), "clean statement matches everything")
        self.assertTrue(clean_report.ledger_total == clean_report.statement_total and clean_report.matched_total() == usd("83.50"), "clean statement totals agree")

        # an empty statement: everything is missing in statement
        empty = ledger.reconcile("bank", [], 3)
        self.assertTrue(len(empty.matches) == 0 and len(empty.breaks_of(BreakType.MISSING_IN_STATEMENT)) == 6
                        and empty.statement_total.is_zero() and empty.ledger_break_total() == usd("83.50") and empty.ties_out(), "empty statement")

        # an account with no lines: everything on the statement is missing in ledger
        no_lines = ledger.reconcile("eur-bank", [StatementLine("w-1", eur("9.99"), "2026-10-01")], 3)
        self.assertTrue(len(no_lines.breaks) == 1 and no_lines.breaks[0].type is BreakType.MISSING_IN_LEDGER
                        and no_lines.ledger_total == eur("0.00") and no_lines.ties_out(), "statement against an empty account")

    def test_pass_2_ties_go_to_the_first_unmatched_ledger_line(self):
        twins = Ledger(s4chart())
        twins.post(JournalEntry.of("a1", "2026-10-01", Posting("bank", Side.DEBIT, usd("10.00")), Posting("fees", Side.CREDIT, usd("10.00"))))
        twins.post(JournalEntry.of("a2", "2026-10-02", Posting("bank", Side.DEBIT, usd("10.00")), Posting("fees", Side.CREDIT, usd("10.00"))))
        twin_report = twins.reconcile("bank", [st("X", "10.00", "2026-10-02")], 3)
        self.assertTrue(twin_report.matches[0].ledger_ref == "a1" and break_for(twin_report, "a2").type is BreakType.MISSING_IN_STATEMENT, "first unmatched ledger line wins")
        twin_report2 = twins.reconcile("bank", [st("a2", "10.00", "2026-10-02"), st("X", "10.00", "2026-10-02")], 3)
        self.assertTrue(len(twin_report2.matches) == 2 and twin_report2.matches[0].ledger_ref == "a2" and twin_report2.matches[1].ledger_ref == "a1" and twin_report2.is_clean(), "pass 1 consumes before pass 2 runs")

    def test_rejections(self):
        ledger = s4ledger()
        with self.assertRaises(UnknownAccountError, msg="reconcile unknown account"):
            ledger.reconcile("nope", [], 3)
        with self.assertRaises(ValueError, msg="negative window"):
            ledger.reconcile("bank", [], -1)
        with self.assertRaises(CurrencyMismatchError, msg="statement currency must match the account"):
            ledger.reconcile("bank", [StatementLine("e", eur("1.00"), "2026-10-01")], 3)
        with self.assertRaises(LedgerError, msg="statement date must be ISO-8601"):
            ledger.reconcile("bank", [st("d", "1.00", "10/01/2026")], 3)


if __name__ == "__main__":
    unittest.main()
