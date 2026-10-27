// Project: projects/ledger-engine — reference solution demo (Go): open accounts, post, reverse, reconcile.
// Run:  go run .
// Test: python3 scripts/project_test.py ledger-engine --lang go --all --solution
// Go 1.22+, standard library only. Deterministic: no clock, no randomness, no I/O beyond stdout.
// Same worked example as the Java and Python solutions.
package main

import (
	"fmt"
	"strings"
)

func usd(text string) Money { return must(Parse(text, USD)) }

func posting(accountID string, side Side, amount Money) Posting {
	p, err := NewPosting(accountID, side, amount)
	if err != nil {
		panic(err)
	}
	return p
}

func entry(id, date string, postings ...Posting) JournalEntry {
	e, err := NewJournalEntry(id, date, postings...)
	if err != nil {
		panic(err)
	}
	return e
}

func mustPost(ledger *Ledger, e JournalEntry) PostedEntry {
	posted, err := ledger.Post(e)
	if err != nil {
		panic(err)
	}
	return posted
}

func main() {
	fmt.Println("== Ledger Engine: a double-entry ledger that reconciles itself ==")

	// Stage 1: a chart of accounts
	chart := NewAccountRegistry()
	chart.Open("bank", Asset, USD)
	chart.Open("receivable", Asset, USD)
	chart.Open("payable:sellers", Liability, USD)
	chart.Open("revenue:fees", Revenue, USD)
	for _, account := range chart.All() {
		fmt.Printf("  %-16s %-9s %s  grows on %v\n", account.ID, account.Type, account.Currency.Code, account.NormalSide())
	}

	// Stage 2: post balanced entries; the ledger refuses anything else
	ledger, _ := NewLedgerWithSnapshots(chart, 2)
	sale := usd("100.00")
	fee := must(must(sale.Times(29, 1000, HalfEven)).Plus(OfMinor(30, USD)))
	mustPost(ledger, entry("sale-1", "2026-10-01",
		posting("bank", Debit, sale),
		posting("payable:sellers", Credit, must(sale.Minus(fee))),
		posting("revenue:fees", Credit, fee)))
	mustPost(ledger, entry("payout-1", "2026-10-03",
		posting("payable:sellers", Debit, usd("50.00")),
		posting("bank", Credit, usd("50.00"))))
	mustPost(ledger, entry("sale-2", "2026-10-04",
		posting("bank", Debit, usd("20.00")),
		posting("revenue:fees", Credit, usd("20.00"))))
	if _, err := ledger.Post(entry("bad", "2026-10-04",
		posting("bank", Debit, usd("1.00")),
		posting("revenue:fees", Credit, usd("0.99")))); err != nil {
		fmt.Println("\nrejected:", err)
	}
	fmt.Printf("journal has %d entries; fee on 100.00 = %v\n", ledger.Size(), fee)

	// Stage 3: balances are derived, the trial balance ties, reversals mirror
	bank, _ := ledger.Balance("bank")
	sellers, _ := ledger.Balance("payable:sellers")
	fees, _ := ledger.Balance("revenue:fees")
	fmt.Printf("\nbalances: bank %v, sellers %v, fees %v\n", bank, sellers, fees)
	tb := ledger.TrialBalance()[0]
	fmt.Printf("trial balance %s: debits %v credits %v difference %v\n", tb.Currency.Code, tb.TotalDebits, tb.TotalCredits, tb.Difference())
	ledger.Reverse("sale-2")
	bank, _ = ledger.Balance("bank")
	fees, _ = ledger.Balance("revenue:fees")
	var at []string
	for _, s := range ledger.Snapshots() {
		at = append(at, fmt.Sprint(s.Sequence))
	}
	fmt.Printf("after reversing sale-2: bank %v, fees %v, snapshots at [%s]\n", bank, fees, strings.Join(at, ", "))

	// Stage 4: the processor's statement does not quite agree
	statement := []StatementLine{
		{"sale-1", usd("100.00"), "2026-10-01"},
		{"PAYOUT-0001", usd("-50.00"), "2026-10-04"},
		{"sale-2", usd("20.00"), "2026-10-04"},
		{"sale-2", usd("20.00"), "2026-10-04"},
		{"refund-9", usd("-5.00"), "2026-10-05"},
	}
	report, err := ledger.Reconcile("bank", statement, 3)
	if err != nil {
		panic(err)
	}
	fmt.Printf("\nreconcile bank: %d matched, %d breaks\n", len(report.Matches), len(report.Breaks))
	for _, m := range report.Matches {
		fmt.Printf("  match  %s ~ %s %v by %v\n", m.LedgerRef, m.StatementRef, m.Amount, m.By)
	}
	for _, b := range report.Breaks {
		fmt.Printf("  break  %v %s ledger %v statement %v\n", b.Type, b.Ref, b.LedgerAmount, b.StatementAmount)
	}
	fmt.Printf("ledger total %v = matched %v + breaks %v; statement total %v = matched + breaks %v; ties out: %t\n",
		report.LedgerTotal, report.MatchedTotal(), report.LedgerBreakTotal(), report.StatementTotal, report.StatementBreakTotal(), report.TiesOut())
}
