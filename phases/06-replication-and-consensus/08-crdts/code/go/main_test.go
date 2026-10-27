// Tests for phases/06-replication-and-consensus/08-crdts. Run: go test ./...
package main

import (
	"errors"
	"math"
	"strings"
	"testing"
)

func check(t *testing.T, name string, condition bool) {
	t.Helper()
	if !condition {
		t.Errorf("FAIL: %s", name)
	}
}

func TestGCounter(t *testing.T) {
	a, b := NewGCounter(), NewGCounter()
	_ = a.Increment("A", 3)
	_ = b.Increment("B", 5)
	_ = b.Increment("A", 1)
	check(t, "gcounter value sums slots", a.Value() == 3 && b.Value() == 6)
	check(t, "gcounter merge takes per-slot max, not sum", a.Merge(b).Value() == 8)
	check(t, "gcounter merge is commutative", a.Merge(b).Equal(b.Merge(a)))
	check(t, "gcounter merge is idempotent", a.Merge(b).Merge(b).Equal(a.Merge(b)))
	check(t, "gcounter merge leaves inputs untouched", a.Value() == 3 && b.Value() == 6)
	check(t, "gcounter rejects negative increment", errors.Is(a.Increment("A", -1), ErrNegativeIncrement))
	big := NewGCounter()
	_ = big.Increment("A", math.MaxInt64)
	check(t, "gcounter overflow is an error", errors.Is(big.Increment("A", 1), ErrOverflow))
}

func TestPNCounter(t *testing.T) {
	p, q := NewPNCounter(), NewPNCounter()
	_ = p.Increment("A", 10)
	_ = p.Decrement("A", 4)
	_ = q.Increment("B", 2)
	_ = q.Decrement("B", 7)
	check(t, "pncounter value is P minus N", p.Value() == 6 && q.Value() == -5)
	check(t, "pncounter merge", p.Merge(q).Value() == 1)
	check(t, "pncounter merge commutative", p.Merge(q).Equal(q.Merge(p)))
	check(t, "pncounter idempotent", p.Merge(p).Equal(p))
	// concurrent decrements on the same replica id are the classic mistake: merge keeps the max, not both
	x, y := NewPNCounter(), NewPNCounter()
	_ = x.Decrement("same", 5)
	_ = y.Decrement("same", 5)
	check(t, "same replica id on two nodes loses a decrement (why ids must be unique)", x.Merge(y).Value() == -5)
}

func TestGSet(t *testing.T) {
	s1, s2 := NewGSet[string](), NewGSet[string]()
	s1.Add("a")
	s2.Add("b")
	s2.Add("a")
	check(t, "gset merge is union", fmtSet(s1.Merge(s2).Elements()) == "[a, b]")
	check(t, "gset commutative", s1.Merge(s2).Equal(s2.Merge(s1)))
}

func TestORSet(t *testing.T) {
	tagsA, tagsB := NewTagSource("A"), NewTagSource("B")
	oa := NewORSet[string]()
	oa.Add("book", tagsA)
	ob := oa.Merge(NewORSet[string]())
	check(t, "orset sync carries element", ob.Contains("book"))
	oa.Remove("book")
	check(t, "orset local remove hides element", !oa.Contains("book"))
	ob.Add("book", tagsB)
	merged := oa.Merge(ob)
	check(t, "orset concurrent add wins over remove", merged.Contains("book"))
	check(t, "orset merge commutative", oa.Merge(ob).Equal(ob.Merge(oa)))
	check(t, "orset tombstone retained after merge", merged.TombstoneCount() == 1)
	oc := merged.Merge(NewORSet[string]())
	oc.Remove("book")
	check(t, "orset remove after observing both tags removes element", !oc.Contains("book") && oc.TombstoneCount() == 2)
	stale := NewORSet[string]()
	stale.Remove("never-added")
	check(t, "orset remove of unseen element is a no-op", !stale.Contains("never-added") && stale.TombstoneCount() == 0)
	check(t, "orset re-merging the stale pre-remove state does not resurrect", !oc.Merge(ob).Contains("book"))
}

func TestLWWRegister(t *testing.T) {
	r1 := LWWRegister[string]{"v1", 10, "A"}
	r2 := LWWRegister[string]{"v2", 12, "B"}
	r3 := LWWRegister[string]{"v3", 12, "A"}
	check(t, "lww higher timestamp wins", r1.Merge(r2).Value == "v2")
	check(t, "lww commutative", r1.Merge(r2).Equal(r2.Merge(r1)))
	check(t, "lww timestamp tie broken by replica id", r2.Merge(r3).Value == "v2" && r3.Merge(r2).Value == "v2")
	check(t, "lww loses the other write entirely", !strings.Contains(r1.Merge(r2).Value, "v1"))
}

func TestReplicatedBalance(t *testing.T) {
	east := NewReplicatedBalance(USD)
	_ = east.Credit("east", MustParse("100.00", USD))
	west := east.Merge(NewReplicatedBalance(USD))
	eighty := MustParse("80.00", USD)
	if !east.Balance().IsNegative() {
		_ = east.Debit("east", eighty)
	}
	if !west.Balance().IsNegative() {
		_ = west.Debit("west", eighty)
	}
	check(t, "each replica locally saw a valid balance", east.Balance().MinorUnits == 2000 && west.Balance().MinorUnits == 2000)
	check(t, "merged balance goes negative despite local checks", east.Merge(west).Balance().MinorUnits == -6000)
	check(t, "merged balance commutative", east.Merge(west).Equal(west.Merge(east)))
	check(t, "balance toString", east.Merge(west).Balance().String() == "-60.00 USD")
	check(t, "balance rejects currency mismatch", errors.Is(east.Credit("east", MustParse("1.00", EUR)), ErrCurrencyMismatch))
	check(t, "balance rejects negative amounts", errors.Is(east.Debit("east", OfMinor(-1, USD)), ErrNegativeAmount))
}

func TestMergeLawsOnSeededStates(t *testing.T) {
	rng := NewJavaRandom(7)
	gl := Check(func() *GCounter { return RandomGCounter(rng) }, 300)
	check(t, "gcounter laws hold on 300 random states", gl.AllHold())
	pl := Check(func() *PNCounter { return RandomPNCounter(rng) }, 300)
	check(t, "pncounter laws hold on 300 random states", pl.AllHold())
	sources := map[string]*TagSource{"A": NewTagSource("A"), "B": NewTagSource("B"), "C": NewTagSource("C")}
	ol := Check(func() *ORSet[string] { return RandomORSet(rng, sources) }, 300)
	check(t, "orset laws hold on 300 random states", ol.AllHold())
	nl := Check(func() NaiveSumCounter { return NaiveSumCounter{int64(1 + rng.NextInt(100))} }, 100)
	check(t, "naive sum counter fails idempotence", nl.IdempotentFailures == 100 && nl.CommutativeFailures == 0)
	reps := []*PNCounter{}
	for i := 0; i < 6; i++ {
		reps = append(reps, RandomPNCounter(rng))
	}
	check(t, "replicas converge regardless of merge order", Converges(reps, rng))
}

func TestJavaRandomMatchesJDK(t *testing.T) {
	// new Random(42).nextInt(100) x3 in the JDK is 30, 63, 48.
	r := NewJavaRandom(42)
	got := []int{r.NextInt(100), r.NextInt(100), r.NextInt(100)}
	check(t, "nextInt(100) stream matches java.util.Random(42)", got[0] == 30 && got[1] == 63 && got[2] == 48)
}
