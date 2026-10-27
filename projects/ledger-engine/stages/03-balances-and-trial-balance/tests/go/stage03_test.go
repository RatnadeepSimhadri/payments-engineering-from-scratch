// Stage 3 tests for projects/ledger-engine (Go): balances are derived from the journal (signed in the account's
// normal direction), snapshots every K entries agree with full replay, BalanceAt reads any point in time,
// the trial balance ties per currency, and reversals mirror without editing history.
// Port of Stage03Test.java: same checks, same names, same order. Helpers prefixed s03.
package main

import (
	"errors"
	"testing"
)

func s03check(t *testing.T, name string, condition bool) {
	t.Helper()
	if !condition {
		t.Errorf("FAIL: %s", name)
	}
}

func s03checkErr(t *testing.T, name string, err, want error) {
	t.Helper()
	if err == nil {
		t.Errorf("FAIL: %s (no error)", name)
		return
	}
	if want != nil && !errors.Is(err, want) {
		t.Errorf("FAIL: %s (wrong error: %v)", name, err)
	}
}

func s03usd(text string) Money { return must(Parse(text, USD)) }
func s03eur(text string) Money { return must(Parse(text, EUR)) }

func s03p(accountID string, side Side, amount Money) Posting {
	p, err := NewPosting(accountID, side, amount)
	if err != nil {
		panic(err)
	}
	return p
}

func s03post(t *testing.T, ledger *Ledger, id, date string, postings ...Posting) PostedEntry {
	t.Helper()
	e, err := NewJournalEntry(id, date, postings...)
	if err != nil {
		t.Fatalf("entry %s: %v", id, err)
	}
	posted, err := ledger.Post(e)
	if err != nil {
		t.Fatalf("post %s: %v", id, err)
	}
	return posted
}

// s03bal is Balance(id) with the error folded into the test; an unexpected error fails the test loudly.
func s03bal(t *testing.T, ledger *Ledger, id string) Money {
	t.Helper()
	m, err := ledger.Balance(id)
	if err != nil {
		t.Errorf("Balance(%s): %v", id, err)
	}
	return m
}

func s03balAt(t *testing.T, ledger *Ledger, id string, seq int64) Money {
	t.Helper()
	m, err := ledger.BalanceAt(id, seq)
	if err != nil {
		t.Errorf("BalanceAt(%s, %d): %v", id, seq, err)
	}
	return m
}

func s03chart() *AccountRegistry {
	chart := NewAccountRegistry()
	chart.Open("bank", Asset, USD)
	chart.Open("receivable", Asset, USD)
	chart.Open("sellers", Liability, USD)
	chart.Open("fees", Revenue, USD)
	chart.Open("ops", Expense, USD)
	chart.Open("eur-bank", Asset, EUR)
	chart.Open("eur-capital", Equity, EUR)
	return chart
}

// s03ledger is the four-entry scenario from the stage doc, with a snapshot after every second entry.
func s03ledger(t *testing.T) *Ledger {
	t.Helper()
	ledger, err := NewLedgerWithSnapshots(s03chart(), 2)
	if err != nil {
		t.Fatal(err)
	}
	s03post(t, ledger, "sale-1", "2026-10-01",
		s03p("bank", Debit, s03usd("100.00")),
		s03p("sellers", Credit, s03usd("96.80")),
		s03p("fees", Credit, s03usd("3.20")))
	s03post(t, ledger, "payout-1", "2026-10-03",
		s03p("sellers", Debit, s03usd("50.00")),
		s03p("bank", Credit, s03usd("50.00")))
	s03post(t, ledger, "rent", "2026-10-04",
		s03p("ops", Debit, s03usd("30.00")),
		s03p("bank", Credit, s03usd("30.00")))
	s03post(t, ledger, "sale-2", "2026-10-05",
		s03p("bank", Debit, s03usd("20.00")),
		s03p("fees", Credit, s03usd("20.00")))
	return ledger
}

func s03line(t *testing.T, tb TrialBalance, accountID string) TrialBalanceLine {
	t.Helper()
	for _, l := range tb.Lines {
		if l.Account.ID == accountID {
			return l
		}
	}
	t.Fatalf("no trial balance line for %s", accountID)
	return TrialBalanceLine{}
}

func TestStage03BalancesAndSnapshots(t *testing.T) {
	// balances are signed in the account's normal direction
	ledger := s03ledger(t)
	s03check(t, "asset balance grows on debit", s03bal(t, ledger, "bank") == s03usd("40.00"))
	s03check(t, "liability balance grows on credit", s03bal(t, ledger, "sellers") == s03usd("46.80"))
	s03check(t, "revenue balance grows on credit", s03bal(t, ledger, "fees") == s03usd("23.20"))
	s03check(t, "expense balance grows on debit", s03bal(t, ledger, "ops") == s03usd("30.00"))
	s03check(t, "untouched account is the currency's zero", s03bal(t, ledger, "receivable") == s03usd("0.00"))
	s03check(t, "untouched EUR account is EUR zero", s03bal(t, ledger, "eur-bank") == s03eur("0.00"))
	_, err := ledger.Balance("nope")
	s03checkErr(t, "balance of unknown account", err, ErrUnknownAccount)

	// BalanceAt reads the journal as of a sequence number
	s03check(t, "BalanceAt 0 is zero", s03balAt(t, ledger, "bank", 0) == s03usd("0.00"))
	s03check(t, "BalanceAt 1", s03balAt(t, ledger, "bank", 1) == s03usd("100.00"))
	s03check(t, "BalanceAt 2", s03balAt(t, ledger, "bank", 2) == s03usd("50.00"))
	s03check(t, "BalanceAt 3", s03balAt(t, ledger, "bank", 3) == s03usd("20.00"))
	s03check(t, "BalanceAt 4 equals Balance", s03balAt(t, ledger, "bank", 4) == s03bal(t, ledger, "bank"))
	s03check(t, "BalanceAt past the end is the current balance", s03balAt(t, ledger, "bank", 999) == s03usd("40.00"))
	s03check(t, "BalanceAt for a liability", s03balAt(t, ledger, "sellers", 1) == s03usd("96.80"))
	_, err = ledger.BalanceAt("nope", 1)
	s03checkErr(t, "BalanceAt unknown account", err, ErrUnknownAccount)

	// snapshots are taken after every K-th entry and hold every account
	s03check(t, "SnapshotEvery is kept", ledger.SnapshotEvery() == 2)
	snaps := ledger.Snapshots()
	if len(snaps) != 2 {
		t.Fatalf("expected 2 snapshots, got %d", len(snaps))
	}
	s03check(t, "snapshots at 2 and 4", snaps[0].Sequence == 2 && snaps[1].Sequence == 4)
	s03check(t, "snapshot 2 holds bank 50.00", snaps[0].Balances["bank"] == s03usd("50.00"))
	s03check(t, "snapshot 2 holds sellers 46.80", snaps[0].Balances["sellers"] == s03usd("46.80"))
	s03check(t, "snapshot covers every account", len(snaps[0].Balances) == 7 && snaps[0].Balances["eur-capital"] == s03eur("0.00"))
	s03check(t, "snapshot 4 agrees with BalanceAt 4", snaps[1].Balances["bank"] == s03balAt(t, ledger, "bank", 4))
	for k := range snaps[0].Balances {
		delete(snaps[0].Balances, k)
	}
	s03check(t, "snapshot balances are a copy", len(ledger.Snapshots()[0].Balances) == 7)
	plain, err := NewLedger(s03chart())
	s03check(t, "default snapshot interval is 100", err == nil && plain.SnapshotEvery() == DefaultSnapshotEvery && DefaultSnapshotEvery == 100)
	s03check(t, "no snapshot before the interval", len(plain.Snapshots()) == 0)
	_, err = NewLedgerWithSnapshots(s03chart(), 0)
	s03checkErr(t, "snapshotEvery must be >= 1", err, nil)

	// Balance() after a snapshot replays only the tail and agrees with full recomputation
	s03post(t, ledger, "sale-3", "2026-10-06",
		s03p("bank", Debit, s03usd("5.00")), s03p("fees", Credit, s03usd("5.00")))
	s03check(t, "balance after snapshot + tail", s03bal(t, ledger, "bank") == s03usd("45.00"))
	s03check(t, "balance agrees with BalanceAt(size)", s03bal(t, ledger, "bank") == s03balAt(t, ledger, "bank", int64(ledger.Size())))
	s03check(t, "no snapshot at an odd sequence", len(ledger.Snapshots()) == 2)
}

func TestStage03TrialBalance(t *testing.T) {
	// the trial balance ties out per currency
	fresh := s03ledger(t)
	tbs := fresh.TrialBalance()
	if len(tbs) != 2 {
		t.Fatalf("expected 2 trial balances, got %d", len(tbs))
	}
	s03check(t, "one trial balance per currency, in code order", tbs[0].Currency == EUR && tbs[1].Currency == USD)
	usdTb := tbs[1]
	s03check(t, "USD debits total 70.00", usdTb.TotalDebits == s03usd("70.00"))
	s03check(t, "USD credits total 70.00", usdTb.TotalCredits == s03usd("70.00"))
	s03check(t, "USD trial balance is balanced", usdTb.IsBalanced() && usdTb.Difference() == s03usd("0.00"))
	s03check(t, "USD trial balance has five lines", len(usdTb.Lines) == 5)
	s03check(t, "bank sits in the debit column", s03line(t, usdTb, "bank").Debit == s03usd("40.00") && s03line(t, usdTb, "bank").Credit == s03usd("0.00"))
	s03check(t, "sellers sits in the credit column", s03line(t, usdTb, "sellers").Credit == s03usd("46.80") && s03line(t, usdTb, "sellers").Debit == s03usd("0.00"))
	s03check(t, "fees sits in the credit column", s03line(t, usdTb, "fees").Credit == s03usd("23.20"))
	s03check(t, "ops sits in the debit column", s03line(t, usdTb, "ops").Debit == s03usd("30.00"))
	s03check(t, "zero account shows zero in both columns", s03line(t, usdTb, "receivable").Debit.IsZero() && s03line(t, usdTb, "receivable").Credit.IsZero())
	eurTb := tbs[0]
	s03check(t, "empty EUR trial balance is balanced at zero", eurTb.IsBalanced() && eurTb.TotalDebits == s03eur("0.00") && len(eurTb.Lines) == 2)
	usdTb.Lines[0] = TrialBalanceLine{}
	s03check(t, "trial balance lines are the caller's copy", fresh.TrialBalance()[1].Lines[0].Account.ID == "bank")

	// a liability driven negative moves to the debit column; the totals still tie
	s03post(t, fresh, "over-payout", "2026-10-06",
		s03p("sellers", Debit, s03usd("60.00")), s03p("bank", Credit, s03usd("60.00")))
	s03check(t, "overdrawn liability has a negative signed balance", s03bal(t, fresh, "sellers") == s03usd("-13.20"))
	after := fresh.TrialBalance()[1]
	s03check(t, "negative liability shows in the debit column", s03line(t, after, "sellers").Debit == s03usd("13.20") && s03line(t, after, "sellers").Credit.IsZero())
	s03check(t, "negative asset shows in the credit column", s03line(t, after, "bank").Credit == s03usd("20.00") && s03line(t, after, "bank").Debit.IsZero())
	s03check(t, "trial balance still ties after the overdraw", after.IsBalanced() && after.TotalDebits == s03usd("43.20"))

	// EUR activity lands only in the EUR trial balance
	s03post(t, fresh, "capital", "2026-10-06",
		s03p("eur-bank", Debit, s03eur("10.00")), s03p("eur-capital", Credit, s03eur("10.00")))
	both := fresh.TrialBalance()
	s03check(t, "EUR trial balance ties at 10.00", both[0].TotalDebits == s03eur("10.00") && both[0].IsBalanced())
	s03check(t, "USD totals unchanged by EUR entry", both[1].TotalDebits == s03usd("43.20"))
}

func TestStage03Reversals(t *testing.T) {
	// reversals: post the mirror, never edit the original
	rev := s03ledger(t)
	reversal, err := rev.Reverse("rent")
	if err != nil {
		t.Fatal(err)
	}
	s03check(t, "reversal id is rev:<id>", reversal.ID() == "rev:rent" && ReversalID("rent") == "rev:rent")
	s03check(t, "reversal gets the next sequence", reversal.Sequence == 5 && rev.Size() == 5)
	s03check(t, "reversal keeps the original date", reversal.Entry.Date == "2026-10-04")
	s03check(t, "reversal mirrors every posting", len(reversal.Entry.Postings) == 2 &&
		reversal.Entry.Postings[0] == Posting{"ops", Credit, s03usd("30.00")} && reversal.Entry.Postings[1] == Posting{"bank", Debit, s03usd("30.00")})
	rent, _ := rev.Entry("rent")
	s03check(t, "original is untouched", rent.Sequence == 3 && rent.Entry.Postings[0].Side == Debit)
	s03check(t, "reversal undoes the balance effect", s03bal(t, rev, "ops") == s03usd("0.00") && s03bal(t, rev, "bank") == s03usd("70.00"))
	s03check(t, "history still shows the original at sequence 3", s03balAt(t, rev, "ops", 3) == s03usd("30.00") && s03balAt(t, rev, "ops", 5).IsZero())
	s03check(t, "IsReversed", rev.IsReversed("rent") && !rev.IsReversed("sale-1"))
	again, err := rev.Reverse("rent")
	s03check(t, "reversing twice returns the first reversal", err == nil && again.Equal(reversal) && rev.Size() == 5)
	_, err = rev.Reverse("nope")
	s03checkErr(t, "reversing an unknown entry", err, ErrUnknownEntry)
	s03check(t, "trial balance ties after a reversal", rev.TrialBalance()[1].IsBalanced() && rev.TrialBalance()[1].TotalDebits == s03usd("70.00"))
	s03check(t, "reversal is a balanced entry in the journal", rev.Journal()[4].ID() == "rev:rent")
	rr, err := rev.Reverse("rev:rent")
	s03check(t, "a reversal can itself be reversed", err == nil && rr.ID() == "rev:rev:rent" && s03bal(t, rev, "ops") == s03usd("30.00"))
}
