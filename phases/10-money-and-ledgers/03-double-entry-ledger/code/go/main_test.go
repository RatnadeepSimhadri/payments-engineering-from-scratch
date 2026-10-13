// Tests for phases/10-money-and-ledgers/03-double-entry-ledger. Run: go test ./...
package main

import (
	"errors"
	"testing"
)

func usd(t *testing.T, text string) Money {
	t.Helper()
	m, err := Parse(text, USD)
	if err != nil {
		t.Fatalf("parse %s: %v", text, err)
	}
	return m
}

func paymentsLedger(t *testing.T) *Ledger {
	t.Helper()
	l := NewLedger()
	for _, a := range []struct {
		id, name string
		t        AccountType
		c        Currency
	}{
		{"1010", "Settlement Bank", Asset, USD},
		{"1200", "Processor Receivable", Asset, USD},
		{"2100", "Merchant Payable", Liability, USD},
		{"4100", "Fee Revenue", Revenue, USD},
		{"2200", "EUR Merchant Payable", Liability, EUR},
	} {
		if _, err := l.OpenAccount(a.id, a.name, a.t, a.c); err != nil {
			t.Fatal(err)
		}
	}
	return l
}

func sale(t *testing.T, id string) JournalEntry {
	t.Helper()
	e, err := NewJournalEntry(id, "card payment", []Posting{
		Dr("1200", usd(t, "100.00")),
		Cr("2100", usd(t, "97.10")),
		Cr("4100", usd(t, "2.90")),
	})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func mustPost(t *testing.T, l *Ledger, e JournalEntry) bool {
	t.Helper()
	ok, err := l.Post(e)
	if err != nil {
		t.Fatalf("post %s: %v", e.ID, err)
	}
	return ok
}

func balance(t *testing.T, l *Ledger, id string) Money {
	t.Helper()
	b, err := l.Balance(id)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestNormalBalances(t *testing.T) {
	if Asset.NormalBalance() != Debit || Expense.NormalBalance() != Debit {
		t.Fatal("assets and expenses carry a debit normal balance")
	}
	if Liability.NormalBalance() != Credit || Equity.NormalBalance() != Credit || Revenue.NormalBalance() != Credit {
		t.Fatal("liabilities, equity and revenue carry a credit normal balance")
	}
}

func TestUnbalancedEntryRejected(t *testing.T) {
	_, err := NewJournalEntry("u1", "x", []Posting{Dr("1200", usd(t, "100.00")), Cr("2100", usd(t, "97.10"))})
	if !errors.Is(err, ErrUnbalanced) {
		t.Fatalf("want ErrUnbalanced, got %v", err)
	}
}

func TestMixedCurrencyEntryRejected(t *testing.T) {
	eur, _ := Parse("100.00", EUR)
	_, err := NewJournalEntry("m1", "x", []Posting{Dr("1200", usd(t, "100.00")), Cr("2200", eur)})
	if !errors.Is(err, ErrUnbalanced) {
		t.Fatalf("want ErrUnbalanced (per-currency net is non-zero), got %v", err)
	}
}

func TestSinglePostingRejected(t *testing.T) {
	_, err := NewJournalEntry("s1", "x", []Posting{Dr("1200", usd(t, "1.00"))})
	if !errors.Is(err, ErrUnbalanced) {
		t.Fatalf("want ErrUnbalanced, got %v", err)
	}
}

func TestZeroAndNegativePostingsRejected(t *testing.T) {
	if _, err := NewPosting("1200", Debit, usd(t, "0.00")); !errors.Is(err, ErrInvalidPosting) {
		t.Fatalf("zero posting: want ErrInvalidPosting, got %v", err)
	}
	if _, err := NewPosting("1200", Credit, usd(t, "-1.00")); !errors.Is(err, ErrInvalidPosting) {
		t.Fatalf("negative posting: want ErrInvalidPosting, got %v", err)
	}
}

func TestThreeLegEntryBalances(t *testing.T) {
	if got := sale(t, "ok").TotalDebits(USD); got != usd(t, "100.00") {
		t.Fatalf("total debits = %v", got)
	}
}

func TestBalancesInNormalDirection(t *testing.T) {
	l := paymentsLedger(t)
	if !mustPost(t, l, sale(t, "pay-001")) {
		t.Fatal("first post must append")
	}
	if balance(t, l, "1200") != usd(t, "100.00") {
		t.Fatalf("receivable = %v", balance(t, l, "1200"))
	}
	if balance(t, l, "2100") != usd(t, "97.10") {
		t.Fatalf("merchant payable = %v", balance(t, l, "2100"))
	}
	if balance(t, l, "4100") != usd(t, "2.90") {
		t.Fatalf("fee revenue = %v", balance(t, l, "4100"))
	}
	if !balance(t, l, "1010").IsZero() || l.Size() != 1 {
		t.Fatal("bank untouched and journal holds one entry")
	}
}

func TestDuplicateEntryIDIsNoOp(t *testing.T) {
	l := paymentsLedger(t)
	mustPost(t, l, sale(t, "pay-001"))
	if mustPost(t, l, sale(t, "pay-001")) {
		t.Fatal("replaying an identical entry must return false")
	}
	if l.Size() != 1 || balance(t, l, "1200") != usd(t, "100.00") {
		t.Fatal("replay must not change the journal or any balance")
	}
}

func TestSameIDDifferentContentIsConflict(t *testing.T) {
	l := paymentsLedger(t)
	mustPost(t, l, sale(t, "pay-001"))
	other, _ := NewJournalEntry("pay-001", "card payment", []Posting{Dr("1200", usd(t, "50.00")), Cr("2100", usd(t, "50.00"))})
	if _, err := l.Post(other); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("want ErrIdempotencyConflict, got %v", err)
	}
	if l.Size() != 1 || balance(t, l, "1200") != usd(t, "100.00") {
		t.Fatal("conflict must leave no trace")
	}
}

func TestLedgerLevelValidationLeavesNoPartialWrite(t *testing.T) {
	l := paymentsLedger(t)
	mustPost(t, l, sale(t, "pay-001"))
	unknown, _ := NewJournalEntry("x1", "x", []Posting{Dr("9999", usd(t, "1.00")), Cr("2100", usd(t, "1.00"))})
	if _, err := l.Post(unknown); !errors.Is(err, ErrUnknownAccount) {
		t.Fatalf("want ErrUnknownAccount, got %v", err)
	}
	wrongCurrency, _ := NewJournalEntry("x2", "x", []Posting{Dr("2200", usd(t, "1.00")), Cr("2100", usd(t, "1.00"))})
	if _, err := l.Post(wrongCurrency); !errors.Is(err, ErrInvalidPosting) {
		t.Fatalf("want ErrInvalidPosting (posting currency must match account), got %v", err)
	}
	if l.Size() != 1 || balance(t, l, "2100") != usd(t, "97.10") {
		t.Fatal("rejected entries must leave the journal untouched")
	}
}

func TestTrialBalanceSumsToZeroAfterLifecycle(t *testing.T) {
	l := paymentsLedger(t)
	mustPost(t, l, sale(t, "pay-001"))
	settle, _ := NewJournalEntry("stl-001", "settlement", []Posting{Dr("1010", usd(t, "100.00")), Cr("1200", usd(t, "100.00"))})
	payout, _ := NewJournalEntry("out-001", "payout", []Posting{Dr("2100", usd(t, "97.10")), Cr("1010", usd(t, "97.10"))})
	mustPost(t, l, settle)
	mustPost(t, l, payout)
	tb := l.TrialBalance(USD)
	if !tb.Balances() {
		t.Fatalf("trial balance does not balance: %v vs %v", tb.TotalDebits, tb.TotalCredits)
	}
	if tb.TotalDebits != usd(t, "2.90") {
		t.Fatalf("totals = %v", tb.TotalDebits)
	}
	if balance(t, l, "1010") != usd(t, "2.90") || !balance(t, l, "2100").IsZero() {
		t.Fatal("bank should hold the fee and the payable should be settled")
	}
}

func TestReversalRestoresPriorBalances(t *testing.T) {
	l := paymentsLedger(t)
	mustPost(t, l, sale(t, "pay-001"))
	settle, _ := NewJournalEntry("stl-001", "settlement", []Posting{Dr("1010", usd(t, "100.00")), Cr("1200", usd(t, "100.00"))})
	mustPost(t, l, settle)
	feeBefore, payableBefore, bankBefore := balance(t, l, "4100"), balance(t, l, "2100"), balance(t, l, "1010")
	rev, err := l.Reverse("pay-001", "rev-001", "refund")
	if err != nil {
		t.Fatal(err)
	}
	if rev.ReversalOf != "pay-001" {
		t.Fatal("reversal must record what it reverses")
	}
	sides := rev.Postings()
	if sides[0].Side != Credit || sides[1].Side != Debit || sides[2].Side != Debit {
		t.Fatal("reversal must flip every side")
	}
	if got, want := balance(t, l, "4100"), must(feeBefore.Minus(usd(t, "2.90"))); got != want {
		t.Fatalf("fee revenue after reversal = %v, want %v", got, want)
	}
	if got, want := balance(t, l, "2100"), must(payableBefore.Minus(usd(t, "97.10"))); got != want {
		t.Fatalf("payable after reversal = %v, want %v", got, want)
	}
	if balance(t, l, "1010") != bankBefore {
		t.Fatal("reversal must not touch the bank")
	}
	if !l.TrialBalance(USD).Balances() {
		t.Fatal("trial balance must still balance after a reversal")
	}
}

func TestOriginalEntryIsNeverEdited(t *testing.T) {
	l := paymentsLedger(t)
	mustPost(t, l, sale(t, "pay-001"))
	if _, err := l.Reverse("pay-001", "rev-001", "refund"); err != nil {
		t.Fatal(err)
	}
	original, err := l.Entry("pay-001")
	if err != nil || !original.Equal(sale(t, "pay-001")) {
		t.Fatal("original entry must remain in the journal unchanged")
	}
	if l.Size() != 2 {
		t.Fatalf("journal size = %d, want 2", l.Size())
	}
}

func TestPostThenReverseIsIdentity(t *testing.T) {
	l := paymentsLedger(t)
	mustPost(t, l, sale(t, "p"))
	if _, err := l.Reverse("p", "r", "undo"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"1010", "1200", "2100", "4100"} {
		if !balance(t, l, id).IsZero() {
			t.Fatalf("account %s should be zero after post+reverse, got %v", id, balance(t, l, id))
		}
	}
}

func TestCannotReverseTwiceOrReverseUnknown(t *testing.T) {
	l := paymentsLedger(t)
	mustPost(t, l, sale(t, "pay-001"))
	if _, err := l.Reverse("pay-001", "rev-001", "refund"); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Reverse("pay-001", "rev-002", "again"); !errors.Is(err, ErrAlreadyReversed) {
		t.Fatalf("want ErrAlreadyReversed, got %v", err)
	}
	if _, err := l.Reverse("nope", "rev-003", "x"); !errors.Is(err, ErrUnknownEntry) {
		t.Fatalf("want ErrUnknownEntry, got %v", err)
	}
}

func TestManyRandomEntriesKeepTrialBalanceZero(t *testing.T) {
	l := paymentsLedger(t)
	accounts := []string{"1010", "1200", "2100", "4100"}
	gen := NewRandomEntries(7, accounts, USD)
	for i := 0; i < 5_000; i++ {
		mustPost(t, l, gen.Next())
	}
	tb := l.TrialBalance(USD)
	if !tb.Balances() || !tb.TotalDebits.IsPositive() {
		t.Fatalf("trial balance after random entries: %v vs %v", tb.TotalDebits, tb.TotalCredits)
	}
	var netSum int64
	for _, id := range accounts {
		net, _ := l.NetDebit(id)
		recomputed, _ := l.NetDebitFromPostings(id)
		if net != recomputed {
			t.Fatalf("cached net debit %v differs from recomputation %v for %s", net, recomputed, id)
		}
		netSum += net.MinorUnits
	}
	if netSum != 0 {
		t.Fatalf("net debits across all accounts sum to %d, want 0", netSum)
	}
	if l.Size() != 5_000 {
		t.Fatalf("journal size = %d", l.Size())
	}
}

func TestRandomGeneratorIsDeterministic(t *testing.T) {
	a := NewRandomEntries(7, []string{"a", "b"}, USD).Next()
	b := NewRandomEntries(7, []string{"a", "b"}, USD).Next()
	if !a.Equal(b) {
		t.Fatal("same seed must produce the same entry")
	}
}

func TestJournalIsAppendOnlyFromOutside(t *testing.T) {
	l := paymentsLedger(t)
	mustPost(t, l, sale(t, "pay-001"))
	entries := l.Entries()
	entries[0] = JournalEntry{}
	if got, _ := l.Entry("pay-001"); !got.Equal(sale(t, "pay-001")) {
		t.Fatal("mutating the returned slice must not touch the journal")
	}
	e := sale(t, "i")
	ps := e.Postings()
	ps[0] = Posting{}
	if e.Postings()[0].AccountID != "1200" {
		t.Fatal("mutating the returned postings must not touch the entry")
	}
}
