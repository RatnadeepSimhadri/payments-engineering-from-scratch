// Stage 2 tests for projects/ledger-engine (Go): postings and entries are values, the journal only accepts
// balanced single-currency entries, sequence numbers are dense, posting is idempotent on id.
// Port of Stage02Test.java: same checks, same names, same order. Helpers prefixed s02.
package main

import (
	"errors"
	"testing"
)

func s02check(t *testing.T, name string, condition bool) {
	t.Helper()
	if !condition {
		t.Errorf("FAIL: %s", name)
	}
}

func s02checkErr(t *testing.T, name string, err, want error) {
	t.Helper()
	if err == nil {
		t.Errorf("FAIL: %s (no error)", name)
		return
	}
	if want != nil && !errors.Is(err, want) {
		t.Errorf("FAIL: %s (wrong error: %v)", name, err)
	}
}

func s02usd(text string) Money { return must(Parse(text, USD)) }
func s02eur(text string) Money { return must(Parse(text, EUR)) }

func s02posting(t *testing.T, accountID string, side Side, amount Money) Posting {
	t.Helper()
	p, err := NewPosting(accountID, side, amount)
	if err != nil {
		t.Fatalf("NewPosting(%s): %v", accountID, err)
	}
	return p
}

func s02entry(t *testing.T, id, date string, postings ...Posting) JournalEntry {
	t.Helper()
	e, err := NewJournalEntry(id, date, postings...)
	if err != nil {
		t.Fatalf("NewJournalEntry(%s): %v", id, err)
	}
	return e
}

func s02chart() *AccountRegistry {
	chart := NewAccountRegistry()
	chart.Open("bank", Asset, USD)
	chart.Open("sellers", Liability, USD)
	chart.Open("fees", Revenue, USD)
	chart.Open("eur-bank", Asset, EUR)
	chart.Open("eur-fees", Revenue, EUR)
	return chart
}

func TestStage02PostingsAndEntries(t *testing.T) {
	// postings are positive amounts on one side of one account
	debitBank := s02posting(t, "bank", Debit, s02usd("100.00"))
	s02check(t, "posting keeps fields", debitBank.AccountID == "bank" && debitBank.Side == Debit && debitBank.Amount == s02usd("100.00"))
	s02check(t, "mirrored flips the side only", debitBank.Mirrored() == Posting{"bank", Credit, s02usd("100.00")})
	_, err := NewPosting("bank", Debit, s02usd("0.00"))
	s02checkErr(t, "zero posting rejected", err, nil)
	_, err = NewPosting("bank", Debit, s02usd("-1.00"))
	s02checkErr(t, "negative posting rejected", err, nil)
	_, err = NewPosting(" ", Debit, s02usd("1.00"))
	s02checkErr(t, "blank posting account rejected", err, nil)

	// entries are immutable values that do not check balance themselves
	legs := []Posting{debitBank, s02posting(t, "fees", Credit, s02usd("100.00"))}
	sale := s02entry(t, "sale-1", "2026-10-01", legs...)
	legs[0] = Posting{"fees", Debit, s02usd("1.00")}
	legs = legs[:0]
	s02check(t, "entry copies its postings", len(sale.Postings) == 2 && sale.Postings[0] == debitBank)
	s02check(t, "entry postings are the entry's own", len(legs) == 0 && len(sale.Postings) == 2)
	currency, err := sale.Currency()
	s02check(t, "entry currency is the first posting's", err == nil && currency == USD)
	_, err = NewJournalEntry(" ", "2026-10-01", debitBank)
	s02checkErr(t, "blank entry id rejected", err, nil)
	_, err = NewJournalEntry("x", "", debitBank)
	s02checkErr(t, "blank entry date rejected", err, nil)
	_, err = s02entry(t, "empty", "2026-10-01").Currency()
	s02checkErr(t, "currency of an entry without postings", err, ErrNoPostings)
	unbalanced := s02entry(t, "bad", "2026-10-01", debitBank, s02posting(t, "fees", Credit, s02usd("99.99")))
	s02check(t, "constructing an unbalanced entry is allowed; posting it is not", len(unbalanced.Postings) == 2)
}

func TestStage02Journal(t *testing.T) {
	debitBank := s02posting(t, "bank", Debit, s02usd("100.00"))
	sale := s02entry(t, "sale-1", "2026-10-01", debitBank, s02posting(t, "fees", Credit, s02usd("100.00")))
	unbalanced := s02entry(t, "bad", "2026-10-01", debitBank, s02posting(t, "fees", Credit, s02usd("99.99")))

	// the journal accepts balanced entries and numbers them 1, 2, 3, ...
	ledger, err := NewLedger(s02chart())
	if err != nil {
		t.Fatal(err)
	}
	s02check(t, "new ledger is empty", ledger.Size() == 0 && len(ledger.Journal()) == 0)
	first, err := ledger.Post(sale)
	s02check(t, "first entry gets sequence 1", err == nil && first.Sequence == 1)
	s02check(t, "posted entry carries the entry", first.Entry.Equal(sale) && first.ID() == "sale-1")
	second, err := ledger.Post(s02entry(t, "sale-2", "2026-10-02",
		s02posting(t, "bank", Debit, s02usd("100.00")),
		s02posting(t, "sellers", Credit, s02usd("96.80")),
		s02posting(t, "fees", Credit, s02usd("3.20"))))
	s02check(t, "three-leg entry balances 100.00 = 96.80 + 3.20", err == nil && second.Sequence == 2)
	s02check(t, "size counts posted entries", ledger.Size() == 2)
	journal := ledger.Journal()
	s02check(t, "journal is in sequence order", len(journal) == 2 && journal[0].ID() == "sale-1" && journal[1].ID() == "sale-2")
	found, err := ledger.Entry("sale-2")
	s02check(t, "Entry(id) finds a posted entry", err == nil && found.Sequence == 2)
	s02check(t, "HasEntry", ledger.HasEntry("sale-1") && !ledger.HasEntry("nope"))
	_, err = ledger.Entry("nope")
	s02checkErr(t, "unknown entry id", err, ErrUnknownEntry)
	journal[0] = PostedEntry{}
	journal = journal[:0]
	s02check(t, "Journal() is a copy", len(journal) == 0 && ledger.Size() == 2 && ledger.Journal()[0].ID() == "sale-1")

	// posting the same id twice is a no-op that returns the original
	again, err := ledger.Post(s02entry(t, "sale-1", "2026-12-31",
		s02posting(t, "bank", Debit, s02usd("1.00")), s02posting(t, "fees", Credit, s02usd("1.00"))))
	s02check(t, "duplicate id returns the existing record", err == nil && again.Equal(first))
	s02check(t, "duplicate id does not grow the journal", ledger.Size() == 2)
	found, _ = ledger.Entry("sale-1")
	s02check(t, "duplicate id does not replace the content", found.Entry.Date == "2026-10-01")

	// rejections leave the journal untouched and consume no sequence number
	_, err = ledger.Post(unbalanced)
	s02checkErr(t, "unbalanced entry rejected", err, ErrUnbalancedEntry)
	s02check(t, "rejected entry is not in the journal", !ledger.HasEntry("bad") && ledger.Size() == 2)
	_, err = ledger.Post(s02entry(t, "one", "2026-10-01", debitBank))
	s02checkErr(t, "single posting rejected", err, ErrInvalidEntry)
	_, err = ledger.Post(s02entry(t, "none", "2026-10-01"))
	s02checkErr(t, "no postings rejected", err, ErrInvalidEntry)
	_, err = ledger.Post(s02entry(t, "ghost", "2026-10-01",
		s02posting(t, "ghost", Debit, s02usd("1.00")), s02posting(t, "fees", Credit, s02usd("1.00"))))
	s02checkErr(t, "unknown account rejected", err, ErrUnknownAccount)
	_, err = ledger.Post(s02entry(t, "mixed", "2026-10-01",
		s02posting(t, "bank", Debit, s02usd("1.00")), s02posting(t, "eur-fees", Credit, s02eur("1.00"))))
	s02checkErr(t, "mixed currencies rejected", err, ErrCurrencyMismatch)
	_, err = ledger.Post(s02entry(t, "wrong-acct", "2026-10-01",
		s02posting(t, "eur-bank", Debit, s02usd("1.00")), s02posting(t, "fees", Credit, s02usd("1.00"))))
	s02checkErr(t, "USD posting to EUR account rejected", err, ErrCurrencyMismatch)
	s02check(t, "rejections consume no sequence numbers", ledger.Size() == 2)
	third, err := ledger.Post(s02entry(t, "eur-sale", "2026-10-03",
		s02posting(t, "eur-bank", Debit, s02eur("10.00")),
		s02posting(t, "eur-fees", Credit, s02eur("10.00"))))
	s02check(t, "next accepted entry is sequence 3", err == nil && third.Sequence == 3)
	eurSale, _ := ledger.Entry("eur-sale")
	eurCurrency, _ := eurSale.Entry.Currency()
	s02check(t, "a second currency lives in the same journal", ledger.Size() == 3 && eurCurrency == EUR)

	// every rejection wraps ErrLedger so callers can test "the ledger said no" once
	_, err = ledger.Post(unbalanced)
	s02checkErr(t, "Unbalanced is a ledger error", err, ErrLedger)
	_, err = ledger.Post(s02entry(t, "one", "2026-10-01", debitBank))
	s02checkErr(t, "InvalidEntry is a ledger error", err, ErrLedger)
	_, err = ledger.Entry("nope")
	s02checkErr(t, "UnknownEntry is a ledger error", err, ErrLedger)
	_, err = NewLedger(nil)
	s02checkErr(t, "ledger needs a registry", err, nil)
}
