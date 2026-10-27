// Tests for phases/07-storage-and-transactions/05-two-phase-locking. Run: go test ./...
package main

import (
	"errors"
	"maps"
	"slices"
	"testing"
)

func assert(t *testing.T, name string, condition bool) {
	t.Helper()
	if !condition {
		t.Errorf("%s", name)
	}
}

func run(t *testing.T, s *Scheduler, order ...int) {
	t.Helper()
	if err := s.Run(order...); err != nil {
		t.Fatalf("Run(%v): %v", order, err)
	}
}

func step(t *testing.T, s *Scheduler, tx *Txn) {
	t.Helper()
	if _, err := s.Step(tx); err != nil {
		t.Fatalf("Step(T%d): %v", tx.ID, err)
	}
}

func total(t *testing.T, s *Scheduler) Money {
	t.Helper()
	m, err := s.Total(USD)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestLockCompatibility(t *testing.T) {
	cases := []struct {
		a, b LockMode
		want bool
	}{{S, S, true}, {S, X, false}, {X, S, false}, {X, X, false}}
	for _, c := range cases {
		if got := c.a.CompatibleWith(c.b); got != c.want {
			t.Errorf("%s.CompatibleWith(%s) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

func TestLockTable(t *testing.T) {
	log := []string{}
	lm := NewLockManager(&log)
	assert(t, "first S granted", lm.Acquire(1, "a", S))
	assert(t, "second S granted alongside", lm.Acquire(2, "a", S))
	assert(t, "X waits behind two readers", !lm.Acquire(3, "a", X))
	assert(t, "a later S waits behind the queued X (FIFO, no writer starvation)", !lm.Acquire(4, "a", S))
	assert(t, "blockers of T4 include the holders and the queued writer", slices.Equal(lm.BlockersOf(4, "a", S), []int{3}))
	assert(t, "re-requesting a held lock is a no-op grant", lm.Acquire(1, "a", S))
	g1 := lm.ReleaseAll(1)
	assert(t, "releasing one reader grants nobody while the other reader remains", len(g1) == 0)
	g2 := lm.ReleaseAll(2)
	assert(t, "releasing the last reader grants the writer only", slices.Equal(g2, []int{3}) && lm.Table["a"].Holders[3] == X)
	g3 := lm.ReleaseAll(3)
	assert(t, "releasing the writer grants the queued reader", slices.Equal(g3, []int{4}))
	lm.ReleaseAll(4)
	assert(t, "lock table is empty after everyone releases", len(lm.Table) == 0)
	assert(t, "the log recorded the waits", slices.Contains(log, "  T3 X(a) WAITS for T1,T2"))
}

func TestWaitForGraph(t *testing.T) {
	g := NewWaitForGraph()
	g.AddEdge(1, 2)
	g.AddEdge(2, 3)
	_, found := g.FindCycle()
	assert(t, "a chain is not a deadlock", !found)
	g.AddEdge(3, 1)
	cycle, found := g.FindCycle()
	assert(t, "a three-way cycle is found in wait order", found && slices.Equal(cycle, []int{1, 2, 3}))
	g2b := NewWaitForGraph()
	g2b.AddEdge(5, 6)
	g2b.AddEdge(7, 5)
	g2b.AddEdge(6, 7)
	g2b.AddEdge(9, 5) // T9 waits on the cycle but is not part of it
	cycle, found = g2b.FindCycle()
	assert(t, "a transaction waiting on a cycle is not reported inside it", found && slices.Equal(cycle, []int{5, 6, 7}))
	assert(t, "graph prints its edges in order", g2b.String() == "T5 -> T6, T6 -> T7, T7 -> T5, T9 -> T5")
	assert(t, "empty graph prints (no waits)", NewWaitForGraph().String() == "(no waits)")
}

func TestTransferDeadlock(t *testing.T) {
	s := NewScheduler(Youngest, true)
	s.Load("acct:A", usd(100_000))
	s.Load("acct:B", usd(50_000))
	t1 := s.Begin(Transfer("acct:A", "acct:B", 10_000))
	t2 := s.Begin(Transfer("acct:B", "acct:A", 5_000))
	run(t, s, 1, 2, 1, 2)
	assert(t, "exactly one deadlock detected", s.DeadlocksDetected == 1)
	assert(t, "the cycle is T1 -> T2 -> T1", slices.Equal(s.Cycles[0], []int{1, 2}))
	assert(t, "YOUNGEST picks T2", slices.Equal(s.Victims, []int{2}))
	assert(t, "the victim was restarted once and then committed", t2.Restarts == 1 && t2.Status == Committed)
	assert(t, "T1 committed without restarting", t1.Restarts == 0 && t1.Status == Committed)
	assert(t, "both transfers applied: A = 950.00", s.DB["acct:A"] == usd(95_000))
	assert(t, "both transfers applied: B = 550.00", s.DB["acct:B"] == usd(55_000))
	assert(t, "money is conserved", total(t, s) == usd(150_000))
	assert(t, "strict 2PL released every lock at commit", s.LockTableEmpty())

	serial := NewScheduler(Youngest, true)
	serial.Load("acct:A", usd(100_000))
	serial.Load("acct:B", usd(50_000))
	serial.Begin(Transfer("acct:A", "acct:B", 10_000))
	serial.Begin(Transfer("acct:B", "acct:A", 5_000))
	run(t, serial, 1, 1, 1, 2, 2, 2)
	assert(t, "serial execution: no deadlock", serial.DeadlocksDetected == 0)
	assert(t, "serial execution: same final state as the resolved deadlock", maps.Equal(serial.DB, s.DB))
}

func TestVictimPolicy(t *testing.T) {
	f := NewScheduler(FewestLocks, true)
	f.Load("acct:A", usd(100_000))
	f.Load("acct:B", usd(50_000))
	f.Load("fees", usd(0))
	f.Begin(Transfer("acct:A", "acct:B", 10_000))
	f.Begin([]Op{OpAdd{"acct:B", usd(-5_050)}, OpAdd{"fees", usd(50)}, OpAdd{"acct:A", usd(5_000)}, OpCommit{}})
	run(t, f, 1, 2, 2, 1, 2)
	assert(t, "FEWEST_LOCKS aborts the transaction holding fewer locks (T1 with 1, not T2 with 2)", slices.Equal(f.Victims, []int{1}))
	assert(t, "FEWEST_LOCKS: everything still commits and balances", total(t, f) == usd(150_000) &&
		f.DB["acct:A"] == usd(95_000) && f.DB["acct:B"] == usd(54_950) && f.DB["fees"] == usd(50))
}

func TestUpgradeDeadlock(t *testing.T) {
	u := NewScheduler(Youngest, true)
	u.Load("acct:A", usd(100_000))
	u.Begin([]Op{OpRead{"acct:A"}, OpAdd{"acct:A", usd(1_000)}, OpCommit{}})
	u.Begin([]Op{OpRead{"acct:A"}, OpAdd{"acct:A", usd(2_000)}, OpCommit{}})
	run(t, u, 1, 2, 1, 2)
	assert(t, "S->X upgrade by two holders is a deadlock", u.DeadlocksDetected == 1)
	assert(t, "no lost update: both deposits landed", u.DB["acct:A"] == usd(103_000))
	assert(t, "the restarted deposit re-read the committed balance", u.Txns[2].Reads["acct:A"] == usd(101_000))
}

func TestAbortUndoesInReverse(t *testing.T) {
	a := NewScheduler(Youngest, false)
	a.Load("x", usd(10))
	a.Load("y", usd(20))
	ta := a.Begin([]Op{OpAdd{"x", usd(5)}, OpAdd{"y", usd(-5)}, OpAdd{"x", usd(1)}, OpCommit{}})
	step(t, a, ta)
	step(t, a, ta)
	step(t, a, ta)
	assert(t, "writes are applied in place while locks are held", a.DB["x"] == usd(16) && a.DB["y"] == usd(15))
	if err := a.Abort(ta); err != nil {
		t.Fatal(err)
	}
	assert(t, "abort restores both keys to their pre-transaction values", a.DB["x"] == usd(10) && a.DB["y"] == usd(20))
	assert(t, "abort without restart leaves the transaction ABORTED and lock-free", ta.Status == Aborted && a.LockTableEmpty())
}

func TestBasic2PLEarlyRelease(t *testing.T) {
	b := NewScheduler(Youngest, false)
	b.Load("acct:A", usd(100_000))
	writer := b.Begin([]Op{OpAdd{"acct:A", usd(-90_000)}, OpUnlock{"acct:A"}, OpRead{"acct:A"}})
	reader := b.Begin([]Op{OpRead{"acct:A"}, OpCommit{}})
	step(t, b, writer)
	step(t, b, writer)
	step(t, b, reader)
	step(t, b, reader)
	if err := b.Abort(writer); err != nil {
		t.Fatal(err)
	}
	assert(t, "early release exposes an uncommitted balance to a committed reader", reader.Reads["acct:A"] == usd(10_000) &&
		reader.Status == Committed && b.DB["acct:A"] == usd(100_000))
	assert(t, "releasing a lock moves the transaction to the shrinking phase", writer.Phase == Shrinking)

	v := NewScheduler(Youngest, false)
	v.Load("k", usd(1))
	v.Load("j", usd(1))
	tv := v.Begin([]Op{OpAdd{"k", usd(1)}, OpUnlock{"k"}, OpRead{"j"}})
	step(t, v, tv)
	step(t, v, tv)
	_, err := v.Step(tv)
	assert(t, "growing after shrinking is a TwoPhaseViolation", errors.Is(err, ErrTwoPhaseViolation))
}

func TestBlockedWithoutCycleJustWaits(t *testing.T) {
	w := NewScheduler(Youngest, true)
	w.Load("acct:A", usd(100_000))
	w.Begin([]Op{OpAdd{"acct:A", usd(-1)}, OpCommit{}})
	w.Begin([]Op{OpRead{"acct:A"}, OpCommit{}})
	step(t, w, w.Txns[1])
	step(t, w, w.Txns[2])
	assert(t, "reader blocks behind the writer without a deadlock", w.Txns[2].Status == Blocked && w.DeadlocksDetected == 0)
	step(t, w, w.Txns[1])
	assert(t, "commit wakes the waiter and it reads the committed value", w.Txns[2].Status == Active && w.Txns[2].Reads["acct:A"] == usd(99_999))
}

func TestMoney(t *testing.T) {
	assert(t, "money prints minor units with the currency's exponent", usd(95_000).String() == "950.00 USD" && usd(-7).String() == "-0.07 USD")
	_, err := usd(1).Plus(OfMinor(1, Currency{"EUR", 2}))
	assert(t, "plus rejects a currency mismatch", errors.Is(err, ErrCurrencyMismatch))
	_, err = OfMinor(1<<62, USD).Plus(OfMinor(1<<62, USD))
	assert(t, "plus reports int64 overflow instead of wrapping", errors.Is(err, ErrOverflow))
}
