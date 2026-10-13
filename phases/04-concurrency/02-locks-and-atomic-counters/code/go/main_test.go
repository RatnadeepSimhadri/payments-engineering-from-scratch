// Tests for phases/04-concurrency/02-locks-and-atomic-counters. Run: go test -race ./...
package main

import (
	"errors"
	"sync"
	"testing"
)

var ids = []string{"acct-1", "acct-2", "acct-3", "acct-4", "acct-5", "acct-6", "acct-7", "acct-8"}

func clock() *FakeStopwatch { return NewFakeStopwatch(0, 2_000_000) }

// The unsafe counter is tested only through an injected interleaving, never by racing goroutines.
func TestScriptedInterleavingLosesOneUpdate(t *testing.T) {
	var racy *RacyCounter
	fired := false
	racy = NewRacyCounter(func() {
		if !fired {
			fired = true
			racy.Add(5)
		}
	})
	racy.Add(5)
	if got := racy.Get(); got != 5 {
		t.Fatalf("expected the lost update to leave 5, got %d", got)
	}
}

func TestRacyCounterIsExactWhenSequential(t *testing.T) {
	c := NewRacyCounter(nil)
	for i := 0; i < 1_000; i++ {
		c.Add(1)
	}
	if c.Get() != 1_000 {
		t.Fatalf("got %d", c.Get())
	}
}

func TestLockedCounterExactUnderContention(t *testing.T) {
	r := must(Measure(&LockedCounter{}, 4, 5_000, 1, clock()))
	if !r.Exact() || r.Observed != 20_000 {
		t.Fatalf("locked counter lost %d", r.Lost())
	}
}

func TestAtomicCounterExactUnderContention(t *testing.T) {
	r := must(Measure(&AtomicCounter{}, 4, 5_000, 1, clock()))
	if !r.Exact() || r.Observed != 20_000 {
		t.Fatalf("atomic counter lost %d", r.Lost())
	}
}

func TestCasNeverRetriesWithoutContention(t *testing.T) {
	solo := &AtomicCounter{}
	for i := 0; i < 100; i++ {
		solo.Add(3)
	}
	if solo.CasRetries() != 0 || solo.Get() != 300 {
		t.Fatalf("retries %d, value %d", solo.CasRetries(), solo.Get())
	}
}

func TestNegativeDeltasAreExact(t *testing.T) {
	r := must(Measure(&LockedCounter{}, 3, 1_000, -7, clock()))
	if r.Observed != -21_000 || !r.Exact() {
		t.Fatalf("observed %d", r.Observed)
	}
}

func TestStripedMapTotalExactUnderContention(t *testing.T) {
	m, _ := NewStripedAccountMap(USD, 4)
	for _, id := range ids {
		m.Open(id)
	}
	r := must(MeasureMap(m, ids, 8, 2_000, 1, clock()))
	if !r.Exact() || r.Observed != 16_000 {
		t.Fatalf("striped map lost %d", r.Lost())
	}
	for _, id := range ids {
		if b, _ := m.Balance(id); b.MinorUnits != 2_000 {
			t.Fatalf("%s has %v", id, b)
		}
	}
	if m.Total().MinorUnits != 16_000 {
		t.Fatalf("total %v", m.Total())
	}
}

func TestStripeIndexIsStableAndInRange(t *testing.T) {
	m, _ := NewStripedAccountMap(USD, 4)
	if m.StripeIndex("acct-1") != m.StripeIndex("acct-1") || m.StripeIndex("acct-1") < 0 || m.StripeIndex("acct-1") >= 4 {
		t.Fatalf("stripe index %d", m.StripeIndex("acct-1"))
	}
	if StableHash("acct-1") != -1423448841 { // same value as Java's String.hashCode
		t.Fatalf("stable hash %d", StableHash("acct-1"))
	}
}

func TestRejections(t *testing.T) {
	m, _ := NewStripedAccountMap(USD, 4)
	m.Open("acct-1")
	if err := m.Credit("ghost", OfMinor(1, USD)); !errors.Is(err, ErrNoSuchAccount) {
		t.Fatalf("expected ErrNoSuchAccount, got %v", err)
	}
	if err := m.Credit("acct-1", OfMinor(1, EUR)); !errors.Is(err, ErrCurrencyMismatch) {
		t.Fatalf("expected ErrCurrencyMismatch, got %v", err)
	}
	if err := m.Credit("acct-1", OfMinor(-1, USD)); !errors.Is(err, ErrNegativeAmount) {
		t.Fatalf("expected ErrNegativeAmount, got %v", err)
	}
	if _, err := NewStripedAccountMap(USD, 0); err == nil {
		t.Fatal("zero stripes must be rejected")
	}
}

func TestDebitBeyondBalanceIsRefused(t *testing.T) {
	w, _ := NewStripedAccountMap(USD, 2)
	w.Open("w")
	_ = w.Credit("w", OfMinor(150, USD))
	if err := w.Debit("w", OfMinor(151, USD)); !errors.Is(err, ErrInsufficientFunds) {
		t.Fatalf("expected ErrInsufficientFunds, got %v", err)
	}
	if b, _ := w.Balance("w"); b.MinorUnits != 150 {
		t.Fatalf("refused debit changed the balance to %v", b)
	}
}

func TestTwoRacingDebitsExactlyOneSucceeds(t *testing.T) {
	w, _ := NewStripedAccountMap(USD, 2)
	w.Open("w")
	_ = w.Credit("w", OfMinor(150, USD))
	start := make(chan struct{})
	var wg sync.WaitGroup
	var successes AtomicCounter
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if err := w.Debit("w", OfMinor(100, USD)); err == nil {
				successes.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()
	if successes.Get() != 1 {
		t.Fatalf("expected exactly one success, got %d", successes.Get())
	}
	if b, _ := w.Balance("w"); b.MinorUnits != 50 {
		t.Fatalf("balance %v", b)
	}
	if w.Total() != OfMinor(50, USD) {
		t.Fatalf("total %v", w.Total())
	}
}

func TestAccountFacade(t *testing.T) {
	a := NewAccount("a", USD, &LockedCounter{})
	_ = a.Credit(OfMinor(10_000, USD))
	_ = a.Debit(OfMinor(1_999, USD))
	if a.Balance().String() != "80.01 USD" {
		t.Fatalf("balance %v", a.Balance())
	}
	if err := a.Credit(OfMinor(1, EUR)); !errors.Is(err, ErrCurrencyMismatch) {
		t.Fatalf("expected ErrCurrencyMismatch, got %v", err)
	}
	if err := a.Credit(OfMinor(-1, USD)); !errors.Is(err, ErrNegativeAmount) {
		t.Fatalf("expected ErrNegativeAmount, got %v", err)
	}
	sum, _ := OfMinor(110, USD).Plus(OfMinor(220, USD))
	if sum.MinorUnits != 330 {
		t.Fatalf("plus %v", sum)
	}
}

func TestBenchmarkResultArithmetic(t *testing.T) {
	r := BenchmarkResult{"x", 4, 1_000, 1, 4_000, 2_000_000}
	if r.OpsPerSecond() != 2_000_000 || r.Expected() != 4_000 || r.Lost() != 0 || !r.Exact() {
		t.Fatalf("%+v", r)
	}
	if (BenchmarkResult{"x", 2, 10, 1, 17, 1}).Lost() != 3 {
		t.Fatal("short result must report the loss")
	}
	if _, err := Measure(&LockedCounter{}, 0, 1, 1, clock()); err == nil {
		t.Fatal("zero threads must be rejected")
	}
}
