package main

import (
	"errors"
	"reflect"
	"testing"
)

func burst(writers, expectedRead int) *History {
	h := NewHistory()
	for i := 0; i < writers; i++ {
		h.Write("c"+string(rune('0'+i%10)), "x", i+1, 0, 100)
	}
	return h.Read("reader", "x", expectedRead, 101, 102)
}

func good() *History {
	return NewHistory().Write("ledger", "bal", 500, 0, 4).Write("ledger", "bal", 300, 6, 9).
		Read("risk", "bal", 300, 10, 12).Read("app", "bal", 300, 13, 15)
}

func stale() *History {
	return NewHistory().Write("ledger", "bal", 500, 0, 4).Write("ledger", "bal", 300, 6, 9).
		Read("risk", "bal", 500, 10, 12).Read("app", "bal", 300, 13, 15)
}

func seqOnly() *History {
	return NewHistory().Write("ledger", "bal", 300, 0, 4).Read("risk", "bal", 0, 6, 8)
}

func window() *History {
	return NewHistory().Write("ledger", "bal", 300, 0, 10).Read("risk", "bal", 0, 5, 15).Read("app", "bal", 300, 6, 8)
}

func shrunk() *History {
	return NewHistory().Write("ledger", "bal", 300, 0, 10).Read("risk", "bal", 0, 11, 15).Read("app", "bal", 300, 6, 8)
}

func TestKnownGoodHistoryPasses(t *testing.T) {
	r := NewChecker(RegisterModel{}).CheckLinearizable(good())
	if !r.OK {
		t.Fatal("known-good history should be linearizable")
	}
	if !reflect.DeepEqual(r.Order, []int{0, 1, 2, 3}) {
		t.Errorf("witness order = %v, want real-time order", r.Order)
	}
}

func TestEmptyHistoryAndInitialValue(t *testing.T) {
	c := NewChecker(RegisterModel{})
	if !c.CheckLinearizable(NewHistory()).OK {
		t.Error("empty history should pass")
	}
	if !c.CheckLinearizable(NewHistory().Read("a", "x", 0, 0, 1)).OK {
		t.Error("read of initial 0 should pass")
	}
	if c.CheckLinearizable(NewHistory().Read("a", "x", 7, 0, 1)).OK {
		t.Error("read of a never-written value should fail")
	}
}

func TestIndependentKeys(t *testing.T) {
	c := NewChecker(RegisterModel{})
	keys := NewHistory().Write("a", "x", 1, 0, 1).Write("a", "y", 2, 2, 3).Read("b", "x", 1, 4, 5).Read("b", "y", 2, 4, 5)
	if !c.CheckLinearizable(keys).OK {
		t.Error("independent keys should each hold their own value")
	}
	if c.CheckLinearizable(NewHistory().Write("a", "x", 1, 0, 1).Read("b", "y", 1, 2, 3)).OK {
		t.Error("read of the wrong key's value should fail")
	}
}

func TestStaleReadFails(t *testing.T) {
	r := NewChecker(RegisterModel{}).CheckLinearizable(stale())
	if r.OK {
		t.Fatal("stale read should not be linearizable")
	}
	if !reflect.DeepEqual(r.LongestPrefix, []int{0, 1}) {
		t.Errorf("longest prefix = %v, want [0 1]", r.LongestPrefix)
	}
	if len(r.Order) != 0 {
		t.Errorf("failure should carry no witness order, got %v", r.Order)
	}
}

func TestSequentiallyConsistentButNotLinearizable(t *testing.T) {
	c := NewChecker(RegisterModel{})
	lin, _ := c.Check(seqOnly(), Linearizable)
	if lin.OK {
		t.Error("write-then-stale-read by another client should fail linearizability")
	}
	seq, _ := c.Check(seqOnly(), Sequential)
	if !seq.OK || !reflect.DeepEqual(seq.Order, []int{1, 0}) {
		t.Errorf("should be sequentially consistent with the read first, got ok=%t order=%v", seq.OK, seq.Order)
	}
	programOrder := NewHistory().Write("a", "x", 1, 0, 1).Read("a", "x", 0, 2, 3)
	if r, _ := c.Check(programOrder, Sequential); r.OK {
		t.Error("sequential consistency must still enforce one client's program order")
	}
	if r, _ := c.Check(good(), Sequential); !r.OK {
		t.Error("every linearizable history is sequentially consistent")
	}
}

func TestConcurrencyWindow(t *testing.T) {
	c := NewChecker(RegisterModel{})
	r := c.CheckLinearizable(window())
	if !r.OK || !reflect.DeepEqual(r.Order, []int{1, 0, 2}) {
		t.Errorf("overlapping read of the old value should linearize before the write, got ok=%t order=%v", r.OK, r.Order)
	}
	if c.CheckLinearizable(shrunk()).OK {
		t.Error("read that starts after the write returned must see it")
	}
}

func TestTwoConcurrentWritesExploreBothOrders(t *testing.T) {
	c := NewChecker(RegisterModel{})
	a := NewHistory().Write("a", "x", 1, 0, 10).Write("b", "x", 2, 0, 10).Read("c", "x", 1, 11, 12)
	b := NewHistory().Write("a", "x", 1, 0, 10).Write("b", "x", 2, 0, 10).Read("c", "x", 2, 11, 12)
	neither := NewHistory().Write("a", "x", 1, 0, 10).Write("b", "x", 2, 0, 10).Read("c", "x", 0, 11, 12)
	if r := c.CheckLinearizable(a); !reflect.DeepEqual(r.Order, []int{1, 0, 2}) {
		t.Errorf("order for read->1 = %v, want [1 0 2]", r.Order)
	}
	if r := c.CheckLinearizable(b); !reflect.DeepEqual(r.Order, []int{0, 1, 2}) {
		t.Errorf("order for read->2 = %v, want [0 1 2]", r.Order)
	}
	if c.CheckLinearizable(neither).OK {
		t.Error("a read after both writes returned must see one of them")
	}
}

func TestPrecedesAndConcurrentWith(t *testing.T) {
	g, w := good(), window()
	if !g.Op(0).Precedes(g.Op(1)) || !w.Op(0).ConcurrentWith(w.Op(1)) || w.Op(1).Precedes(w.Op(0)) {
		t.Error("real-time relations are wrong")
	}
}

func TestRecorder(t *testing.T) {
	clock := &FakeClock{}
	rec := NewHistoryRecorder(clock)
	w1 := rec.InvokeWrite("ledger", "bal", 500)
	clock.Advance(2)
	if err := rec.Complete(w1, 0); err != nil {
		t.Fatal(err)
	}
	clock.Advance(1)
	r1 := rec.InvokeRead("risk", "bal")
	clock.Advance(1)
	if err := rec.Complete(r1, 500); err != nil {
		t.Fatal(err)
	}
	h, err := rec.History()
	if err != nil {
		t.Fatal(err)
	}
	if h.Op(0).InvokedAt != 0 || h.Op(0).ReturnedAt != 2 || h.Op(1).InvokedAt != 3 || h.Op(1).ReturnedAt != 4 {
		t.Errorf("timestamps wrong: %v %v", h.Op(0), h.Op(1))
	}
	if h.Op(0).Value != 500 || h.Op(1).Value != 500 || h.Op(1).Kind != Read {
		t.Errorf("values wrong: %v %v", h.Op(0), h.Op(1))
	}
	if !NewChecker(RegisterModel{}).CheckLinearizable(h).OK {
		t.Error("recorded history should be linearizable")
	}
}

func TestRecorderRefusesIncompleteHistories(t *testing.T) {
	rec := NewHistoryRecorder(&FakeClock{})
	rec.InvokeWrite("a", "x", 1)
	if rec.PendingCount() != 1 {
		t.Error("pending count should be 1")
	}
	if _, err := rec.History(); err == nil {
		t.Error("History() should refuse incomplete ops")
	}
	if err := rec.Complete(99, 0); err == nil {
		t.Error("completing an unknown op should be an error")
	}
}

func TestValidation(t *testing.T) {
	if err := NewHistory().Add("a", Read, "x", 0, 5, 4); !errors.Is(err, ErrBadOp) {
		t.Error("return before invoke should be rejected")
	}
	h := NewHistory()
	var err error
	for i := 0; i <= MaxOps && err == nil; i++ {
		err = h.Add("a", Write, "x", i, i, i)
	}
	if !errors.Is(err, ErrBadOp) {
		t.Error("more than 64 ops should be rejected")
	}
}

func TestMemoPrunesRepeatedConfigurations(t *testing.T) {
	withMemo := NewCheckerWith(RegisterModel{}, true, 5_000_000).CheckLinearizable(burst(8, 1))
	withoutMemo := NewCheckerWith(RegisterModel{}, false, 5_000_000).CheckLinearizable(burst(8, 1))
	if !withMemo.OK || !withoutMemo.OK || withMemo.Order[7] != 0 {
		t.Fatalf("both searches should succeed with write #0 last: %v / %v", withMemo.Order, withoutMemo.Order)
	}
	if withMemo.Explored*5 >= withoutMemo.Explored || withMemo.MemoHits == 0 {
		t.Errorf("memo should prune: %d vs %d states, %d hits", withMemo.Explored, withoutMemo.Explored, withMemo.MemoHits)
	}
	if withMemo.Explored > 8*256 {
		t.Errorf("memoised search explored %d states, want <= n*2^n", withMemo.Explored)
	}
}

func TestStateBudget(t *testing.T) {
	if r, err := NewCheckerWith(RegisterModel{}, true, 100_000).Check(burst(12, 1), Linearizable); err != nil || !r.OK {
		t.Errorf("12 concurrent writes should check within budget with the memo: ok=%t err=%v", r.OK, err)
	}
	if _, err := NewCheckerWith(RegisterModel{}, false, 100_000).Check(burst(12, 1), Linearizable); !errors.Is(err, ErrBudget) {
		t.Errorf("without the memo the same history should blow the budget, got %v", err)
	}
}

func TestGeneratedHistoriesPassWithinBound(t *testing.T) {
	c := NewChecker(RegisterModel{})
	maxExplored := 0
	for seed := int64(1); seed <= 50; seed++ {
		r := c.CheckLinearizable(GenerateLinearizable(seed, 20, 3, 15))
		if !r.OK {
			t.Errorf("seed %d: generated history should be linearizable", seed)
		}
		if r.Explored > maxExplored {
			maxExplored = r.Explored
		}
	}
	if maxExplored > 500 {
		t.Errorf("max explored %d, want <= 500", maxExplored)
	}
}

func TestGeneratorIsDeterministic(t *testing.T) {
	a, b := GenerateLinearizable(7, 20, 3, 15), GenerateLinearizable(7, 20, 3, 15)
	if !reflect.DeepEqual(a.Ops(), b.Ops()) || a.Size() != 20 {
		t.Error("generator should be a pure function of the seed")
	}
}
