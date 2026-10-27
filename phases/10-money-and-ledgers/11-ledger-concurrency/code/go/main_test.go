// Tests for phases/10-money-and-ledgers/11-ledger-concurrency. Run: go test -race ./...
package main

import (
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func usd(minor int64) Money { return OfMinor(minor, USD) }

func ok[T any](t *testing.T) func(T, error) T {
	return func(v T, err error) T {
		t.Helper()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		return v
	}
}

func noErr(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func bal(t *testing.T, l ConcurrentLedger, id string) int64 {
	t.Helper()
	return ok[Money](t)(l.Balance(id)).MinorUnits
}

func allLedgers() []ConcurrentLedger {
	return []ConcurrentLedger{NewPessimisticLedger(), NewOptimisticLedger(), NewShardedLedger()}
}

func TestDeterministicInterleavings(t *testing.T) {
	twoDeposits := []Op{{"A", 100}, {"B", 100}}
	naiveDeposits := Explore(Naive, 0, twoDeposits)
	if naiveDeposits.Schedules != 6 {
		t.Errorf("naive: 2 ops x 2 steps = 6 schedules, got %d", naiveDeposits.Schedules)
	}
	if naiveDeposits.Violations != 4 {
		t.Errorf("naive: 4 of 6 schedules lose a deposit, got %d", naiveDeposits.Violations)
	}
	if Explore(Versioned, 0, twoDeposits).Violations != 0 {
		t.Error("versioned: two deposits never lose an update")
	}
	if Explore(Locked, 0, twoDeposits).Violations != 0 {
		t.Error("locked: two deposits never lose an update")
	}

	overdraft := []Op{{"P1", -60}, {"P2", -60}}
	naiveOver := Explore(Naive, 100, overdraft)
	if naiveOver.Violations == 0 || !strings.Contains(naiveOver.FirstViolation, "lost update") {
		t.Errorf("naive: two 60 payouts from 100 can both 'succeed': %+v", naiveOver)
	}
	if Explore(Versioned, 100, overdraft).Violations != 0 {
		t.Error("versioned: double payout prevented in every schedule")
	}
	if Explore(Locked, 100, overdraft).Violations != 0 {
		t.Error("locked: double payout prevented in every schedule")
	}

	three := []Op{{"P1", -6_000}, {"P2", -6_000}, {"D", 3_000}}
	n3, v3, l3 := Explore(Naive, 10_000, three), Explore(Versioned, 10_000, three), Explore(Locked, 10_000, three)
	if n3.Schedules != 90 {
		t.Errorf("naive: 6!/(2!2!2!) = 90 schedules, got %d", n3.Schedules)
	}
	if n3.Violations != 76 {
		t.Errorf("naive: most schedules violate: %d", n3.Violations)
	}
	if v3.Schedules <= 90 || v3.Violations != 0 {
		t.Errorf("versioned: retries add schedules but no violations: %+v", v3)
	}
	if l3.Schedules != 6 || l3.Violations != 0 {
		t.Errorf("locked: only the 3! serial orders exist: %+v", l3)
	}
}

func TestSingleThreadedBehaviour(t *testing.T) {
	for _, l := range allLedgers() {
		t.Run(l.Name(), func(t *testing.T) {
			noErr(t, l.OpenAccount("a", USD, 1_000, false))
			noErr(t, l.OpenAccount("b", USD, 0, false))
			noErr(t, l.OpenAccount("e", EUR, 0, false))
			noErr(t, l.Transfer("a", "b", usd(400)))
			if bal(t, l, "a") != 600 || bal(t, l, "b") != 400 {
				t.Error("transfer moves money")
			}
			if err := l.Transfer("a", "b", usd(601)); !errors.Is(err, ErrInsufficientFunds) {
				t.Errorf("overdraw rejected: %v", err)
			}
			if bal(t, l, "a") != 600 || l.TotalMinor() != 1_000 {
				t.Error("rejected transfer changed nothing")
			}
			if err := l.Transfer("a", "e", usd(1)); !errors.Is(err, ErrCurrencyMismatch) {
				t.Errorf("currency mismatch: %v", err)
			}
			for name, do := range map[string]func() error{
				"same account":        func() error { return l.Transfer("a", "a", usd(1)) },
				"non-positive amount": func() error { return l.Transfer("a", "b", usd(0)) },
				"unknown account":     func() error { return l.Transfer("a", "zzz", usd(1)) },
				"duplicate account":   func() error { return l.OpenAccount("a", USD, 0, false) },
			} {
				if do() == nil {
					t.Errorf("%s should be rejected", name)
				}
			}
		})
	}
	opt := NewOptimisticLedger()
	noErr(t, opt.OpenAccount("x", USD, 100, false))
	noErr(t, opt.OpenAccount("y", USD, 0, false))
	noErr(t, opt.Transfer("x", "y", usd(10)))
	noErr(t, opt.Transfer("x", "y", usd(10)))
	if ok[int64](t)(opt.Version("x")) != 2 || ok[int64](t)(opt.Version("y")) != 2 {
		t.Error("optimistic: every commit bumps the version")
	}
}

func TestSharding(t *testing.T) {
	sh := NewShardedLedger()
	noErr(t, sh.OpenHotAccount("m", USD, 10, 4))
	noErr(t, sh.OpenAccount("c", USD, 1_000, false))
	noErr(t, sh.OpenAccount("bank", USD, 0, true))
	shard := func(k int) int64 { return ok[int64](t)(sh.ShardBalance("m", k)) }
	if shard(0) != 3 || shard(1) != 3 || shard(2) != 2 || shard(3) != 2 {
		t.Error("hot float allocated across shards (3,3,2,2)")
	}
	for range 8 {
		noErr(t, sh.Transfer("c", "m", usd(100)))
	}
	if shard(0) != 203 || shard(3) != 202 {
		t.Error("credits spread round-robin, two per shard")
	}
	if bal(t, sh, "m") != 810 {
		t.Error("hot balance sums shards")
	}
	noErr(t, sh.Transfer("m", "bank", usd(500)))
	if bal(t, sh, "m") != 310 || bal(t, sh, "bank") != 500 {
		t.Error("debit spanning shards succeeds")
	}
	if err := sh.Transfer("m", "bank", usd(311)); !errors.Is(err, ErrInsufficientFunds) {
		t.Errorf("debit beyond all shards rejected: %v", err)
	}
	if bal(t, sh, "m") != 310 || bal(t, sh, "m#inflight") != 0 {
		t.Error("rejected sharded debit restored every shard")
	}
	before := sh.TotalMinor()
	ok[int64](t)(sh.RollUp("m"))
	if shard(0) != 310 || shard(1) != 0 || shard(3) != 0 {
		t.Error("roll-up moves everything to shard 0")
	}
	if sh.TotalMinor() != before {
		t.Error("roll-up conserves the total")
	}
	noErr(t, sh.OpenHotAccount("m2", USD, 0, 2))
	if sh.Transfer("m", "m2", usd(1)) == nil {
		t.Error("hot-to-hot rejected")
	}
}

func TestStressWithAuditor(t *testing.T) {
	w := Workload{4, 5_000, 50, 10_000_000, 500_000_000, 10, 7}
	expected := w.Expected()
	initialTotal := int64(50*10_000_000 + 500_000_000)
	for _, l := range allLedgers() {
		t.Run(l.Name(), func(t *testing.T) {
			noErr(t, w.Open(l, 4))
			var running atomic.Bool
			running.Store(true)
			var badCuts, cuts atomic.Int64
			var auditor sync.WaitGroup
			auditor.Add(1)
			go func() {
				defer auditor.Done()
				for running.Load() {
					cuts.Add(1)
					if l.TotalMinor() != initialTotal {
						badCuts.Add(1)
					}
				}
			}()
			failed := w.Run(l)
			running.Store(false)
			auditor.Wait()
			if s, ok := l.(*ShardedLedger); ok {
				if _, err := s.RollUp("merchant"); err != nil {
					t.Fatal(err)
				}
			}
			if failed != 0 {
				t.Errorf("stress: no operation failed, got %d", failed)
			}
			if l.TotalMinor() != initialTotal {
				t.Error("stress: total conserved")
			}
			for id, want := range expected {
				if got := bal(t, l, id); got != want {
					t.Errorf("stress: balance of %s = %d, want %d", id, got, want)
				}
			}
			if badCuts.Load() != 0 || cuts.Load() == 0 {
				t.Errorf("stress: every mid-run total the auditor took was exact (cuts %d, bad %d)", cuts.Load(), badCuts.Load())
			}
			if bal(t, l, "merchant") < 0 {
				t.Error("stress: no account overdrawn")
			}
		})
	}
}

func TestDeadlockFreedom(t *testing.T) {
	for _, l := range []ConcurrentLedger{NewPessimisticLedger(), NewOptimisticLedger()} {
		t.Run(l.Name(), func(t *testing.T) {
			noErr(t, l.OpenAccount("p", USD, 1_000_000, false))
			noErr(t, l.OpenAccount("q", USD, 1_000_000, false))
			finished := make(chan struct{})
			var wg sync.WaitGroup
			for _, pair := range [][2]string{{"p", "q"}, {"q", "p"}} {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for range 20_000 {
						if err := l.Transfer(pair[0], pair[1], usd(1)); err != nil {
							t.Error(err)
							return
						}
					}
				}()
			}
			go func() { wg.Wait(); close(finished) }()
			select {
			case <-finished:
			case <-time.After(10 * time.Second):
				t.Fatal("opposite transfers did not finish (deadlock?)")
			}
			if bal(t, l, "p") != 1_000_000 || bal(t, l, "q") != 1_000_000 {
				t.Error("pair balances restored")
			}
		})
	}
}

func TestScriptIsDeterministicAndMatchesJava(t *testing.T) {
	w := Workload{8, 20_000, 200, 50_000_000, 2_000_000_000, 20, 42}
	a, b := w.Script(3), w.Script(3)
	for i := range a {
		if a[i] != b[i] {
			t.Fatal("script is deterministic")
		}
	}
	// The Java demo prints this payout total for seed 42, so the Go scripts must generate the same ops.
	if got := w.PayoutTotal(); got != 39_580_783 {
		t.Errorf("payout total for seed 42 = %d, want 39580783 (java.util.Random parity)", got)
	}
}
