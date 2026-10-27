// Tests for phases/06-replication-and-consensus/09-conflict-resolution. Run: go test ./...
package main

import (
	"errors"
	"testing"
)

func check(t *testing.T, name string, condition bool) {
	t.Helper()
	if !condition {
		t.Errorf("FAIL: %s", name)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestVersionVectors(t *testing.T) {
	empty := NewVersionVector()
	a1 := empty.Increment("A")
	a1b1 := a1.Increment("B")
	a2 := a1.Increment("A")
	check(t, "vv increment is non-mutating", empty.Get("A") == 0 && a1.Get("A") == 1)
	check(t, "vv before", a1.Compare(a1b1) == Before)
	check(t, "vv after", a1b1.Compare(a1) == After)
	check(t, "vv equal", a1.Compare(a1.Copy()) == Equal)
	check(t, "vv concurrent", a1b1.Compare(a2) == Concurrent && a2.Compare(a1b1) == Concurrent)
	check(t, "vv empty is before anything", empty.Compare(a1) == Before)
	m := a1b1.Merge(a2)
	check(t, "vv merge is pointwise max", m.Get("A") == 2 && m.Get("B") == 1)
	check(t, "vv merge dominates both inputs", m.Dominates(a1b1) && m.Dominates(a2))
	check(t, "vv merge commutative", a1b1.Merge(a2).Equal(a2.Merge(a1b1)))
}

func TestLWWLosesData(t *testing.T) {
	east, west := NewLWWStore[string](), NewLWWStore[string]()
	east.Put("k", "east-value", 1000, "east")
	west.Put("k", "west-value", 1002, "west")
	merged := east.Merge(west)
	v, _ := merged.Get("k")
	check(t, "lww keeps the later timestamp", v == "west-value")
	ew, _ := east.Merge(west).Get("k")
	we, _ := west.Merge(east).Get("k")
	check(t, "lww merge commutative", ew == we)
	tie1, tie2 := NewLWWStore[string](), NewLWWStore[string]()
	tie1.Put("k", "from-A", 5, "A")
	tie2.Put("k", "from-B", 5, "B")
	t12, _ := tie1.Merge(tie2).Get("k")
	t21, _ := tie2.Merge(tie1).Get("k")
	check(t, "lww tie broken by writer id, both directions", t12 == "from-B" && t21 == "from-B")
	skew := NewLWWStore[string]()
	skew.Put("k", "happened-first-slow-clock", 995, "slow")
	skew.Put("k", "happened-second-fast-clock", 1000, "fast")
	skew.Put("k", "happened-third-slow-clock", 997, "slow")
	sv, _ := skew.Get("k")
	check(t, "lww with clock skew keeps a write that happened earlier in real time", sv == "happened-second-fast-clock")
	check(t, "lww counts every discarded write", skew.Discarded() == 2)
}

func TestSiblingStore(t *testing.T) {
	sA, sB := NewSiblingStore[string]("A"), NewSiblingStore[string]("B")
	v1 := sA.Put("k", "v1", NewVersionVector())
	check(t, "first write has version {A=1}", v1.Get("A") == 1 && sA.SiblingCount("k") == 1)
	sB.MergeFrom(sA)
	check(t, "anti-entropy copies the single version", sB.SiblingCount("k") == 1 && equalStrings(sB.Get("k").Values(), []string{"v1"}))
	readA := sA.Get("k")
	sA.Put("k", "v2-from-A", readA.Context)
	readB := sB.Get("k")
	sB.Put("k", "v2-from-B", readB.Context)
	check(t, "causal write replaces its ancestor locally", sA.SiblingCount("k") == 1 && equalStrings(sA.Get("k").Values(), []string{"v2-from-A"}))
	sA.MergeFrom(sB)
	check(t, "concurrent writes become two siblings", sA.SiblingCount("k") == 2 && sA.Get("k").HasConflict())
	check(t, "sibling context dominates both siblings", sA.Get("k").Context.Get("A") == 2 && sA.Get("k").Context.Get("B") == 1)
	sB.MergeFrom(sA)
	check(t, "merge is idempotent on siblings", sB.SiblingCount("k") == 2)
	sA.MergeFrom(sB)
	check(t, "re-merging does not duplicate siblings", sA.SiblingCount("k") == 2)
	// a write with the merged context collapses the siblings
	conflicted := sA.Get("k")
	sA.Put("k", "v3-resolved", conflicted.Context)
	check(t, "write with merged context collapses siblings", sA.SiblingCount("k") == 1 && equalStrings(sA.Get("k").Values(), []string{"v3-resolved"}))
	check(t, "resolved version dominates both former siblings", sA.Get("k").Context.Get("A") == 3 && sA.Get("k").Context.Get("B") == 1)
	// a blind write (no context) is concurrent with everything
	sA.PutBlind("k", "blind")
	check(t, "blind write adds a sibling instead of overwriting", sA.SiblingCount("k") == 2)
	// a stale write (old context) also becomes a sibling rather than clobbering newer data
	stale := NewSiblingStore[string]("A")
	first := stale.Put("k", "one", NewVersionVector())
	stale.Put("k", "two", first)
	stale.Put("k", "stale-client", first)
	hasTwo := false
	for _, v := range stale.Get("k").Values() {
		if v == "two" {
			hasTwo = true
		}
	}
	check(t, "stale context write becomes a sibling, newer value survives", stale.SiblingCount("k") == 2 && hasTwo)
}

func TestResolveCaseFile(t *testing.T) {
	cs := NewSiblingStore[CaseFile]("east")
	c0 := cs.Put("case", OpenCase(), NewVersionVector())
	cs.Put("case", OpenCase().WithNote("n1").MustStatus("FUNDS_HELD"), c0)
	cw := NewSiblingStore[CaseFile]("west")
	cw.Put("case", OpenCase().WithNote("n2").MustStatus("UNDER_REVIEW"), c0)
	cs.MergeFrom(cw)
	check(t, "case file has two siblings before resolve", cs.SiblingCount("case") == 2)
	resolved, ok := cs.Resolve("case", UnionNotesStrictestStatus)
	check(t, "case merge keeps both notes", ok && equalStrings(sortedKeys(resolved.Notes), []string{"n1", "n2"}))
	check(t, "case merge picks the strictest status", resolved.Status == "FUNDS_HELD")
	check(t, "resolve writes back a single sibling", cs.SiblingCount("case") == 1)
	again, ok := cs.Resolve("case", UnionNotesStrictestStatus)
	check(t, "resolve on a single sibling leaves it alone", ok && again.Equal(resolved) && cs.SiblingCount("case") == 1)
	_, ok = cs.Resolve("nope", UnionNotesStrictestStatus)
	check(t, "resolve on a missing key returns nothing", !ok)
	_, err := OpenCase().WithStatus("LOST")
	check(t, "case file rejects unknown status", errors.Is(err, ErrUnknownStatus))
}

func TestShoppingCartAnomaly(t *testing.T) {
	cA, cB := NewSiblingStore[Cart]("A"), NewSiblingStore[Cart]("B")
	cA.Put("cart", EmptyCart().Add("book", 1).Add("pen", 2), NewVersionVector())
	cB.MergeFrom(cA)
	ra := cA.Get("cart")
	cA.Put("cart", ra.Siblings[0].Value.Remove("pen"), ra.Context)
	rb := cB.Get("cart")
	cB.Put("cart", rb.Siblings[0].Value.Add("book", 1), rb.Context)
	cA.MergeFrom(cB)
	mergedCart, _ := cA.Resolve("cart", UnionMax)
	check(t, "cart merge keeps the max quantity of book", mergedCart.Items["book"] == 2)
	_, hasPen := mergedCart.Items["pen"]
	check(t, "cart merge resurrects the deleted pen (the Dynamo anomaly)", hasPen)
	check(t, "cart merge is order-independent", UnionMax([]Cart{EmptyCart().Add("x", 1), EmptyCart().Add("x", 3)}).
		Equal(UnionMax([]Cart{EmptyCart().Add("x", 3), EmptyCart().Add("x", 1)})))
}

func TestMaximal(t *testing.T) {
	a1 := NewVersionVector().Increment("A")
	a1b1 := a1.Increment("B")
	a2 := a1.Increment("A")
	old := Versioned[string]{"old", Dot{"A", 1}, NewVersionVector()} // version {A=1}
	x := Versioned[string]{"x", Dot{"B", 1}, a1}                     // saw A:1 -> {A=1,B=1}
	y := Versioned[string]{"y", Dot{"A", 2}, a1}                     // saw A:1 -> {A=2}
	max := Maximal([]Versioned[string]{old, x, y})
	check(t, "maximal drops obsoleted writes and keeps concurrent ones", len(max) == 2 && !containsVersioned(max, old) && containsVersioned(max, x) && containsVersioned(max, y))
	check(t, "versioned.version() is context plus own dot", x.Version().Equal(a1b1) && y.Version().Equal(a2))
	check(t, "obsoletedBy is exactly 'the other writer saw my dot'", old.ObsoletedBy(x) && !x.ObsoletedBy(y) && !y.ObsoletedBy(x))
}
