// Tests for phases/08-messaging-and-streams/03-delivery-guarantees. Run: go test ./...
package main

import (
	"errors"
	"testing"
)

func check(t *testing.T, name string, condition bool) {
	t.Helper()
	if !condition {
		t.Errorf("%s", name)
	}
}

func expectedTotal(t *testing.T, log *Log) Money {
	t.Helper()
	total := Zero(USD)
	all, err := log.Read(0, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range all {
		total, err = total.Plus(r.Amount)
		if err != nil {
			t.Fatal(err)
		}
	}
	return total
}

func readLen(t *testing.T, log *Log, from int64, max int) int {
	t.Helper()
	out, err := log.Read(from, max)
	if err != nil {
		t.Fatalf("Read(%d, %d): %v", from, max, err)
	}
	return len(out)
}

func TestLog(t *testing.T) {
	log := SampleLog(10)
	check(t, "log has 10 records", log.EndOffset() == 10)
	check(t, "read at end is empty", readLen(t, log, 10, 4) == 0)
	check(t, "read respects batch size", readLen(t, log, 0, 4) == 4 && readLen(t, log, 8, 4) == 2)
	_, err := log.Read(11, 1)
	check(t, "read past end is an error", err != nil)
}

func TestNoCrashEveryStrategyAgrees(t *testing.T) {
	log := SampleLog(10)
	expected := expectedTotal(t, log)
	for _, s := range Strategies {
		t.Run(s.String(), func(t *testing.T) {
			o := Run(s, log, -1, 4)
			check(t, s.String()+" without a crash is effectively once", o.EffectivelyOnce() && o.Posted == 10 && o.Crashes == 0)
			check(t, s.String()+" without a crash has the expected total", o.Balance == expected)
		})
	}
}

func TestCrashAfterOffsetFive(t *testing.T) {
	log := SampleLog(10)
	expected := expectedTotal(t, log)
	amo := Run(AtMostOnce, log, 5, 4)
	check(t, "at-most-once loses the rest of the batch", amo.Lost == 2 && amo.Duplicates == 0 && amo.Posted == 8)
	check(t, "at-most-once total is short", amo.Balance.MinorUnits < expected.MinorUnits)
	alo := Run(AtLeastOnce, log, 5, 4)
	check(t, "at-least-once re-posts the start of the batch", alo.Duplicates == 2 && alo.Lost == 0 && alo.Posted == 12)
	check(t, "at-least-once total is over", alo.Balance.MinorUnits > expected.MinorUnits)
	idem := Run(AtLeastOnceIdempotent, log, 5, 4)
	check(t, "idempotent consumer absorbs the redelivery", idem.EffectivelyOnce() && idem.Posted == 10)
	check(t, "idempotent consumer total is exact", idem.Balance == expected)
	tx := Run(Transactional, log, 5, 4)
	check(t, "transactional consumer never double-posts", tx.EffectivelyOnce() && tx.Posted == 10)
	check(t, "transactional consumer total is exact", tx.Balance == expected)
	check(t, "every strategy crashed exactly once", amo.Crashes == 1 && alo.Crashes == 1 && idem.Crashes == 1 && tx.Crashes == 1)
}

func TestCrashAtBatchBoundaries(t *testing.T) {
	log := SampleLog(10)
	check(t, "at-most-once crash at batch end loses nothing", Run(AtMostOnce, log, 7, 4).Lost == 0)
	check(t, "at-least-once crash at batch end duplicates the whole batch", Run(AtLeastOnce, log, 7, 4).Duplicates == 4)
	check(t, "at-least-once crash at offset 0 duplicates exactly one", Run(AtLeastOnce, log, 0, 4).Duplicates == 1)
}

func TestSweepEveryCrashOffset(t *testing.T) {
	log := SampleLog(10)
	for _, s := range []Strategy{AtLeastOnceIdempotent, Transactional} {
		all := true
		for at := int64(0); at < 10; at++ {
			all = all && Run(s, log, at, 4).EffectivelyOnce()
		}
		check(t, s.String()+" is correct at every crash offset", all)
	}
	aloEverRight := false
	for at := int64(0); at < 10; at++ {
		aloEverRight = aloEverRight || Run(AtLeastOnce, log, at, 4).EffectivelyOnce()
	}
	check(t, "plain at-least-once is never right when it crashes mid-flight", !aloEverRight)
	check(t, "batch size 1 does not save at-least-once", Run(AtLeastOnce, log, 3, 1).Duplicates == 1)
	check(t, "batch size 1 does not save at-most-once either", Run(AtMostOnce, log, 3, 1).Lost == 0 &&
		Run(AtMostOnce, log, 3, 2).Lost == 0 && Run(AtMostOnce, log, 2, 2).Lost == 1)
}

func TestStorePrimitives(t *testing.T) {
	store := NewStore()
	check(t, "postIfNew accepts the first", store.PostIfNew(Posting{"pay-1", OfMinor(100, USD)}))
	check(t, "postIfNew rejects the duplicate", !store.PostIfNew(Posting{"pay-1", OfMinor(100, USD)}))
	check(t, "duplicate did not change the ledger", len(store.Postings()) == 1 && store.Balance(USD).MinorUnits == 100)
	tx := store.Begin()
	if err := tx.Post(Posting{"pay-2", OfMinor(250, USD)}); err != nil {
		t.Fatal(err)
	}
	if err := tx.CommitOffset(7); err != nil {
		t.Fatal(err)
	}
	check(t, "uncommitted transaction is invisible", len(store.Postings()) == 1 && store.CommittedOffset() == 0)
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	check(t, "committed transaction applies both writes", len(store.Postings()) == 2 && store.CommittedOffset() == 7)
	check(t, "transaction cannot be reused", errors.Is(tx.Post(Posting{"pay-3", OfMinor(1, USD)}), ErrTransactionDone))
	check(t, "transaction cannot be committed twice", errors.Is(tx.Commit(), ErrTransactionDone))
	check(t, "negative offset commit rejected", store.CommitOffset(-1) != nil)
	_, err := NewConsumer(AtLeastOnce, &Log{}, store, NoFaults(), 0)
	check(t, "batch size must be positive", err != nil)
}

func TestFaultInjectorFiresOnce(t *testing.T) {
	f := NewFaultInjector(2)
	check(t, "fault is silent before its offset", f.AfterSideEffect(1) == nil && !f.Fired())
	check(t, "fault fires at its offset", errors.Is(f.AfterSideEffect(2), ErrCrash))
	check(t, "fault fires only once", f.AfterSideEffect(2) == nil && f.Fired())
}

func TestMoneyDiscipline(t *testing.T) {
	m, err := Parse("19.99", USD)
	check(t, "Money.parse 19.99", err == nil && m.MinorUnits == 1999)
	_, err = Parse("1.005", USD)
	check(t, "Money rejects excess precision", err != nil)
	eur, _ := NewCurrency("EUR", 2)
	_, err = OfMinor(1, USD).Plus(OfMinor(1, eur))
	check(t, "Money rejects currency mix", errors.Is(err, ErrCurrencyMismatch))
	_, err = NewCurrency("EURO", 2)
	check(t, "Currency rejects a four-letter code", err != nil)
}
