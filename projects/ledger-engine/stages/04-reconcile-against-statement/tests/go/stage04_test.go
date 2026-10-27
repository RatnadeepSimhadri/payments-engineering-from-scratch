// Stage 4 tests for projects/ledger-engine (Go): an account's ledger lines net each entry's effect, Reconcile
// matches by reference then by amount within a date window, every break lands in exactly one category,
// and the report's totals tie out.
// Port of Stage04Test.java: same checks, same names, same order. Helpers prefixed s04.
package main

import (
	"errors"
	"testing"
)

func s04check(t *testing.T, name string, condition bool) {
	t.Helper()
	if !condition {
		t.Errorf("FAIL: %s", name)
	}
}

func s04checkErr(t *testing.T, name string, err, want error) {
	t.Helper()
	if err == nil {
		t.Errorf("FAIL: %s (no error)", name)
		return
	}
	if want != nil && !errors.Is(err, want) {
		t.Errorf("FAIL: %s (wrong error: %v)", name, err)
	}
}

func s04usd(text string) Money { return must(Parse(text, USD)) }
func s04eur(text string) Money { return must(Parse(text, EUR)) }

func s04st(ref, amount, date string) StatementLine { return StatementLine{ref, s04usd(amount), date} }

func s04p(accountID string, side Side, amount Money) Posting {
	p, err := NewPosting(accountID, side, amount)
	if err != nil {
		panic(err)
	}
	return p
}

func s04post(t *testing.T, ledger *Ledger, id, date string, postings ...Posting) {
	t.Helper()
	e, err := NewJournalEntry(id, date, postings...)
	if err != nil {
		t.Fatalf("entry %s: %v", id, err)
	}
	if _, err := ledger.Post(e); err != nil {
		t.Fatalf("post %s: %v", id, err)
	}
}

func s04chart() *AccountRegistry {
	chart := NewAccountRegistry()
	chart.Open("bank", Asset, USD)
	chart.Open("sellers", Liability, USD)
	chart.Open("fees", Revenue, USD)
	chart.Open("eur-bank", Asset, EUR)
	return chart
}

// s04ledger is the seven-entry scenario from the stage doc. Bank lines: +100, -50, +20, -7.50, +15, +6 = 83.50.
func s04ledger(t *testing.T) *Ledger {
	t.Helper()
	ledger, err := NewLedger(s04chart())
	if err != nil {
		t.Fatal(err)
	}
	s04post(t, ledger, "sale-1", "2026-10-01",
		s04p("bank", Debit, s04usd("100.00")), s04p("sellers", Credit, s04usd("96.80")), s04p("fees", Credit, s04usd("3.20")))
	s04post(t, ledger, "payout-1", "2026-10-03",
		s04p("sellers", Debit, s04usd("50.00")), s04p("bank", Credit, s04usd("50.00")))
	s04post(t, ledger, "sale-2", "2026-10-04",
		s04p("bank", Debit, s04usd("20.00")), s04p("fees", Credit, s04usd("20.00")))
	s04post(t, ledger, "fee-adj", "2026-10-04",
		s04p("fees", Debit, s04usd("1.00")), s04p("sellers", Credit, s04usd("1.00")))
	s04post(t, ledger, "refund-1", "2026-10-06",
		s04p("sellers", Debit, s04usd("7.50")), s04p("bank", Credit, s04usd("7.50")))
	s04post(t, ledger, "sale-3", "2026-10-08",
		s04p("bank", Debit, s04usd("15.00")), s04p("fees", Credit, s04usd("15.00")))
	s04post(t, ledger, "multi", "2026-10-09",
		s04p("bank", Debit, s04usd("10.00")), s04p("bank", Credit, s04usd("4.00")), s04p("fees", Credit, s04usd("6.00")))
	return ledger
}

func s04statement() []StatementLine {
	return []StatementLine{
		s04st("sale-1", "100.00", "2026-10-01"),       // match by ref
		s04st("PO-77", "-50.00", "2026-10-05"),        // match payout-1 by amount within 3 days
		s04st("sale-2", "25.00", "2026-10-04"),        // amount mismatch (ledger has 20.00)
		s04st("sale-1", "100.00", "2026-10-01"),       // duplicate reference
		s04st("chargeback-3", "-12.00", "2026-10-07"), // missing in ledger
		s04st("sale-3", "15.00", "2026-10-08"),        // match by ref
		s04st("RF-9", "-7.50", "2026-10-20"),          // 14 days from refund-1: outside a 3-day window
	}
}

func s04breakFor(t *testing.T, report ReconciliationReport, ref string) ReconciliationBreak {
	t.Helper()
	for _, b := range report.Breaks {
		if b.Ref == ref {
			return b
		}
	}
	t.Fatalf("no break for %s", ref)
	return ReconciliationBreak{}
}

func s04reconcile(t *testing.T, ledger *Ledger, accountID string, statement []StatementLine, window int) ReconciliationReport {
	t.Helper()
	report, err := ledger.Reconcile(accountID, statement, window)
	if err != nil {
		t.Fatalf("Reconcile(%s): %v", accountID, err)
	}
	return report
}

func TestStage04Lines(t *testing.T) {
	// statement lines are values with a reference, a signed amount, and a date
	line, err := NewStatementLine("sale-1", s04usd("100.00"), "2026-10-01")
	s04check(t, "statement line keeps fields", err == nil && line.Ref == "sale-1" && line.Amount == s04usd("100.00") && line.Date == "2026-10-01")
	_, err = NewStatementLine(" ", s04usd("1.00"), "2026-10-01")
	s04checkErr(t, "blank statement ref rejected", err, nil)
	_, err = NewStatementLine("x", s04usd("1.00"), "")
	s04checkErr(t, "blank statement date rejected", err, nil)

	// ledger lines: one per entry that touches the account, netted, in journal order
	ledger := s04ledger(t)
	lines, err := ledger.Lines("bank")
	if err != nil || len(lines) != 6 {
		t.Fatalf("six entries touch bank: got %d lines, err %v", len(lines), err)
	}
	s04check(t, "six entries touch bank", len(lines) == 6)
	s04check(t, "lines are in journal order", lines[0].Ref == "sale-1" && lines[1].Ref == "payout-1" && lines[5].Ref == "multi")
	s04check(t, "asset debit is positive", lines[0].Amount == s04usd("100.00") && lines[0].Date == "2026-10-01")
	s04check(t, "asset credit is negative", lines[1].Amount == s04usd("-50.00"))
	s04check(t, "an entry touching the account twice nets to one line", lines[5].Amount == s04usd("6.00"))
	none := true
	for _, l := range lines {
		if l.Ref == "fee-adj" {
			none = false
		}
	}
	s04check(t, "entries that skip the account produce no line", none)
	sellers, _ := ledger.Lines("sellers")
	s04check(t, "liability lines are signed on the credit side", len(sellers) >= 2 && sellers[0].Amount == s04usd("96.80") && sellers[1].Amount == s04usd("-50.00"))
	eurLines, err := ledger.Lines("eur-bank")
	s04check(t, "untouched account has no lines", err == nil && len(eurLines) == 0)
	_, err = ledger.Lines("nope")
	s04checkErr(t, "lines of unknown account", err, ErrUnknownAccount)
}

func TestStage04Reconcile(t *testing.T) {
	ledger := s04ledger(t)
	lines, _ := ledger.Lines("bank")

	// the full reconciliation: three matches, six breaks, totals tie out
	report := s04reconcile(t, ledger, "bank", s04statement(), 3)
	s04check(t, "report names the account and currency", report.AccountID == "bank" && report.Currency == USD)
	s04check(t, "ledger total is the sum of the lines", report.LedgerTotal == s04usd("83.50"))
	s04check(t, "statement total is the sum of every statement line, duplicates included", report.StatementTotal == s04usd("170.50"))
	if len(report.Matches) != 3 {
		t.Fatalf("three matches: got %d", len(report.Matches))
	}
	s04check(t, "three matches", len(report.Matches) == 3)
	s04check(t, "pass 1 matches come first", report.Matches[0].LedgerRef == "sale-1" && report.Matches[0].By == ByRef &&
		report.Matches[1].LedgerRef == "sale-3" && report.Matches[1].By == ByRef)
	byAmount := report.Matches[2]
	s04check(t, "pass 2 matches by amount within the window", byAmount.LedgerRef == "payout-1" && byAmount.StatementRef == "PO-77" &&
		byAmount.Amount == s04usd("-50.00") && byAmount.By == ByAmountAndDate)
	s04check(t, "matched total", report.MatchedTotal() == s04usd("65.00"))
	s04check(t, "six breaks", len(report.Breaks) == 6)
	mismatch := s04breakFor(t, report, "sale-2")
	s04check(t, "amount mismatch carries both sides", mismatch.Type == AmountMismatch && mismatch.LedgerAmount == s04usd("20.00") && mismatch.StatementAmount == s04usd("25.00"))
	dup := s04breakFor(t, report, "sale-1")
	s04check(t, "duplicate statement reference", dup.Type == Duplicate && dup.LedgerAmount.IsZero() && dup.StatementAmount == s04usd("100.00"))
	chargeback := s04breakFor(t, report, "chargeback-3")
	s04check(t, "missing in ledger", chargeback.Type == MissingInLedger && chargeback.LedgerAmount.IsZero() && chargeback.StatementAmount == s04usd("-12.00"))
	s04check(t, "outside the window is missing in ledger", s04breakFor(t, report, "RF-9").Type == MissingInLedger)
	refund := s04breakFor(t, report, "refund-1")
	s04check(t, "missing in statement", refund.Type == MissingInStatement && refund.LedgerAmount == s04usd("-7.50") && refund.StatementAmount.IsZero())
	s04check(t, "netted multi-leg entry is missing in statement", s04breakFor(t, report, "multi").Type == MissingInStatement && s04breakFor(t, report, "multi").LedgerAmount == s04usd("6.00"))
	s04check(t, "BreaksOf filters by category", len(report.BreaksOf(MissingInLedger)) == 2 && len(report.BreaksOf(MissingInStatement)) == 2 &&
		len(report.BreaksOf(AmountMismatch)) == 1 && len(report.BreaksOf(Duplicate)) == 1)
	s04check(t, "ledger break total", report.LedgerBreakTotal() == s04usd("18.50"))
	s04check(t, "statement break total", report.StatementBreakTotal() == s04usd("105.50"))
	s04check(t, "ledger total = matched + ledger breaks", report.LedgerTotal == must(report.MatchedTotal().Plus(report.LedgerBreakTotal())))
	s04check(t, "statement total = matched + statement breaks", report.StatementTotal == must(report.MatchedTotal().Plus(report.StatementBreakTotal())))
	s04check(t, "report ties out but is not clean", report.TiesOut() && !report.IsClean())
	breaks := report.BreaksOf(Duplicate)
	breaks[0] = ReconciliationBreak{}
	s04check(t, "BreaksOf returns a copy", report.BreaksOf(Duplicate)[0].Ref == "sale-1" && len(report.Breaks) == 6)
	after, _ := ledger.Lines("bank")
	s04check(t, "reconcile does not change the journal", ledger.Size() == 7 && len(after) == 6)

	// the date window is inclusive and symmetric
	wide := s04reconcile(t, ledger, "bank", s04statement(), 14)
	s04check(t, "14-day window matches RF-9 to refund-1", len(wide.Matches) == 4 && len(wide.Breaks) == 4 &&
		wide.Matches[3].LedgerRef == "refund-1" && wide.Matches[3].StatementRef == "RF-9")
	s04check(t, "wide report still ties out", wide.TiesOut() && wide.MatchedTotal() == s04usd("57.50"))
	narrow := s04reconcile(t, ledger, "bank", s04statement(), 13)
	s04check(t, "13-day window does not", len(narrow.Matches) == 3)
	zero := s04reconcile(t, ledger, "bank", []StatementLine{s04st("PO-77", "-50.00", "2026-10-03")}, 0)
	s04check(t, "zero window matches the same day", len(zero.Matches) == 1 && zero.Matches[0].By == ByAmountAndDate)
	dayOff := s04reconcile(t, ledger, "bank", []StatementLine{s04st("PO-77", "-50.00", "2026-10-02")}, 0)
	s04check(t, "zero window rejects the day before", len(dayOff.Matches) == 0 && s04breakFor(t, dayOff, "PO-77").Type == MissingInLedger)
	sign := s04reconcile(t, ledger, "bank", []StatementLine{s04st("PO-77", "50.00", "2026-10-03")}, 3)
	s04check(t, "pass 2 compares signed amounts", len(sign.Matches) == 0)

	// a clean statement: every line matched, nothing to report
	clean := make([]StatementLine, 0, len(lines))
	for _, l := range lines {
		clean = append(clean, StatementLine{l.Ref, l.Amount, l.Date})
	}
	cleanReport := s04reconcile(t, ledger, "bank", clean, 0)
	s04check(t, "clean statement matches everything", len(cleanReport.Matches) == 6 && cleanReport.IsClean() && cleanReport.TiesOut())
	s04check(t, "clean statement totals agree", cleanReport.LedgerTotal == cleanReport.StatementTotal && cleanReport.MatchedTotal() == s04usd("83.50"))

	// an empty statement: everything is missing in statement
	empty := s04reconcile(t, ledger, "bank", nil, 3)
	s04check(t, "empty statement", len(empty.Matches) == 0 && len(empty.BreaksOf(MissingInStatement)) == 6 &&
		empty.StatementTotal.IsZero() && empty.LedgerBreakTotal() == s04usd("83.50") && empty.TiesOut())

	// an account with no lines: everything on the statement is missing in ledger
	noLines := s04reconcile(t, ledger, "eur-bank", []StatementLine{{"w-1", s04eur("9.99"), "2026-10-01"}}, 3)
	s04check(t, "statement against an empty account", len(noLines.Breaks) == 1 && noLines.Breaks[0].Type == MissingInLedger &&
		noLines.LedgerTotal == s04eur("0.00") && noLines.TiesOut())

	// pass 2 ties go to the first unmatched ledger line in journal order
	twins, _ := NewLedger(s04chart())
	s04post(t, twins, "a1", "2026-10-01", s04p("bank", Debit, s04usd("10.00")), s04p("fees", Credit, s04usd("10.00")))
	s04post(t, twins, "a2", "2026-10-02", s04p("bank", Debit, s04usd("10.00")), s04p("fees", Credit, s04usd("10.00")))
	twinReport := s04reconcile(t, twins, "bank", []StatementLine{s04st("X", "10.00", "2026-10-02")}, 3)
	s04check(t, "first unmatched ledger line wins", len(twinReport.Matches) == 1 && twinReport.Matches[0].LedgerRef == "a1" && s04breakFor(t, twinReport, "a2").Type == MissingInStatement)
	twinReport2 := s04reconcile(t, twins, "bank", []StatementLine{s04st("a2", "10.00", "2026-10-02"), s04st("X", "10.00", "2026-10-02")}, 3)
	s04check(t, "pass 1 consumes before pass 2 runs", len(twinReport2.Matches) == 2 && twinReport2.Matches[0].LedgerRef == "a2" && twinReport2.Matches[1].LedgerRef == "a1" && twinReport2.IsClean())

	// rejections: unknown account, negative window, wrong currency, bad date
	_, err := ledger.Reconcile("nope", nil, 3)
	s04checkErr(t, "reconcile unknown account", err, ErrUnknownAccount)
	_, err = ledger.Reconcile("bank", nil, -1)
	s04checkErr(t, "negative window", err, nil)
	_, err = ledger.Reconcile("bank", []StatementLine{{"e", s04eur("1.00"), "2026-10-01"}}, 3)
	s04checkErr(t, "statement currency must match the account", err, ErrCurrencyMismatch)
	_, err = ledger.Reconcile("bank", []StatementLine{s04st("d", "1.00", "10/01/2026")}, 3)
	s04checkErr(t, "statement date must be ISO-8601", err, ErrLedger)
	_, err = ledger.Reconcile("bank", []StatementLine{s04st("d", "1.00", "10/01/2026")}, 3)
	s04checkErr(t, "a bad date is ErrBadDate", err, ErrBadDate)
}
