// Tests for phases/01-complexity-and-core-structures/10-disjoint-sets. Run: go test ./...
package main

import (
	"errors"
	"reflect"
	"testing"
)

func check(t *testing.T, name string, ok bool) {
	t.Helper()
	if !ok {
		t.Error(name)
	}
}

func mustPanic(t *testing.T, name string, f func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Errorf("%s: expected panic", name)
		}
	}()
	f()
}

func TestFreshForest(t *testing.T) {
	ds := NewDisjointSet(6)
	check(t, "n singletons", ds.ComponentCount() == 6 && ds.Elements() == 6)
	check(t, "find of fresh element is itself", ds.Find(3) == 3)
	check(t, "fresh elements not connected", !ds.Connected(0, 1))
	check(t, "fresh component size 1", ds.ComponentSize(4) == 1)
	check(t, "fresh invariant", ds.CheckInvariant())
	check(t, "empty forest", NewDisjointSet(0).ComponentCount() == 0 && NewDisjointSet(0).CheckInvariant())
	_, err := NewDisjointSetWith(-1, true, true)
	check(t, "negative n rejected", errors.Is(err, ErrNegativeSize))
	mustPanic(t, "out-of-range element rejected", func() { ds.Find(6) })

	// union merges, repeated union is a no-op, connectivity is transitive
	check(t, "first union returns true", ds.Union(0, 1))
	check(t, "union decrements components", ds.ComponentCount() == 5)
	check(t, "union makes connected", ds.Connected(0, 1) && ds.Find(0) == ds.Find(1))
	check(t, "repeat union returns false", !ds.Union(1, 0) && ds.ComponentCount() == 5)
	ds.Union(2, 3)
	ds.Union(1, 3)
	check(t, "transitive: 0 ~ 2 via 1 and 3", ds.Connected(0, 2) && ds.Connected(3, 0))
	check(t, "component size tracks merges", ds.ComponentSize(0) == 4 && ds.ComponentSize(5) == 1)
	check(t, "unrelated element stays separate", !ds.Connected(0, 5))
	check(t, "invariant after unions", ds.CheckInvariant())
	check(t, "all representatives agree", ds.Find(0) == ds.Find(1) && ds.Find(1) == ds.Find(2) && ds.Find(2) == ds.Find(3))
}

func TestUnionByRank(t *testing.T) {
	r := NewDisjointSet(8)
	r.Union(0, 1) // rank 1 at root
	check(t, "tie union raises rank to 1", r.RankOf(0) == 1)
	r.Union(0, 2) // singleton under rank-1 root: rank stays 1
	check(t, "unequal union keeps rank", r.RankOf(0) == 1 && r.Depth(2) == 1)
	r.Union(3, 4)
	r.Union(0, 3) // two rank-1 trees: rank 2
	check(t, "tie of rank-1 trees gives rank 2", r.RankOf(0) == 2)
	check(t, "rank bounds depth: depth <= rank everywhere", r.MaxDepth() <= r.RankOf(0))
	check(t, "rank never exceeds log2(size)", r.RankOf(0) <= log2Floor(r.ComponentSize(0)))
}

func TestPathCompression(t *testing.T) {
	pc := mustForest(5, false, true) // no rank so we can build a known chain
	pc.Union(1, 0)
	pc.Union(2, 1)
	pc.Union(3, 2)
	pc.Union(4, 3) // chain 0 -> 1 -> 2 -> 3 -> 4
	check(t, "naive chain has depth n-1 before find", pc.Depth(0) == 4)
	root := pc.Find(0)
	check(t, "find returns the chain root", root == 4)
	check(t, "path compression flattens the path", pc.Depth(0) == 1 && pc.Depth(1) == 1 && pc.Depth(2) == 1)
	check(t, "compression preserves membership", pc.Connected(0, 4) && pc.ComponentSize(0) == 5 && pc.CheckInvariant())

	// without either heuristic the forest still answers correctly, just slowly
	naive := mustForest(2_000, false, false)
	for i := 1; i < 2_000; i++ {
		naive.Union(i, i-1)
	}
	check(t, "naive forest is a chain of depth n-1", naive.MaxDepth() == 1_999)
	naive.ResetHops()
	naive.Find(0)
	check(t, "naive find walks the whole chain", naive.Hops() == 1_999)
	check(t, "naive still correct", naive.Connected(0, 1_999) && naive.ComponentCount() == 1 && naive.CheckInvariant())

	// with both heuristics the same workload stays shallow
	both := NewDisjointSet(2_000)
	for i := 1; i < 2_000; i++ {
		both.Union(i, i-1)
	}
	check(t, "rank keeps the adversarial chain shallow", both.MaxDepth() <= 11)
	both.ResetHops()
	for i := 0; i < 2_000; i++ {
		both.Find(i)
	}
	check(t, "near-constant pointers per find", both.Hops() < 2*2_000)
}

func TestRandomizedAgainstBruteForce(t *testing.T) {
	n := 300
	mine := NewDisjointSet(n)
	label := make([]int, n)
	for i := range label {
		label[i] = i
	}
	rnd := NewJavaRandom(99)
	agree := true
	for step := 0; step < 2_000 && agree; step++ {
		a := int(rnd.NextInt(int32(n)))
		b := int(rnd.NextInt(int32(n)))
		merged := mine.Union(a, b)
		wasSeparate := label[a] != label[b]
		if merged != wasSeparate {
			agree = false
		}
		if wasSeparate {
			from, to := label[b], label[a]
			for i := range label {
				if label[i] == from {
					label[i] = to
				}
			}
		}
		if step%97 == 0 {
			for i := 0; i < n && agree; i++ {
				j := int(rnd.NextInt(int32(n)))
				if mine.Connected(i, j) != (label[i] == label[j]) {
					agree = false
				}
			}
		}
	}
	distinct := map[int]bool{}
	for _, l := range label {
		distinct[l] = true
	}
	check(t, "randomized unions agree with brute force", agree)
	check(t, "randomized component count matches", mine.ComponentCount() == len(distinct) && mine.CheckInvariant())
}

func TestEntityLinker(t *testing.T) {
	linker := NewEntityLinker(16)
	merged, err := linker.Link("acct:1", "device:A")
	check(t, "link new identifiers merges", err == nil && merged)
	merged, _ = linker.Link("acct:2", "device:A")
	check(t, "second account on same device merges", merged)
	check(t, "acct:1 and acct:2 share a ring through the device", linker.SameRing("acct:1", "acct:2"))
	check(t, "ring members sorted", reflect.DeepEqual(linker.Ring("acct:2"), []string{"acct:1", "acct:2", "device:A"}))
	merged, _ = linker.Link("acct:2", "device:A")
	check(t, "re-observing a link returns false", !merged)
	_, _ = linker.Link("acct:3", "card:X")
	check(t, "separate ring not connected", !linker.SameRing("acct:1", "acct:3") && linker.Rings() == 2)
	check(t, "unknown identifier is in no ring", !linker.SameRing("acct:1", "acct:999") && linker.RingSize("acct:999") == 0 && len(linker.Ring("nobody")) == 0)
	_, _ = linker.Link("card:X", "acct:1")
	check(t, "linking the rings merges them", linker.SameRing("acct:3", "acct:2") && linker.RingSize("acct:3") == 5 && linker.Rings() == 1)
	check(t, "ringsOfAtLeast filters by size", len(linker.RingsOfAtLeast(5)) == 1 && len(linker.RingsOfAtLeast(6)) == 0)
	tiny := NewEntityLinker(2)
	_, _ = tiny.Link("a", "b")
	_, err = tiny.Link("a", "c")
	check(t, "capacity exceeded is an error", errors.Is(err, ErrCapacityExceeded))
}

func TestDemoTableMatchesJava(t *testing.T) {
	// The heuristics table for a 2,000-element chain: the numbers the Java algorithm prints at that size.
	n := 2_000
	want := []struct {
		depthBefore int
		hopsPerFind int64
		depthAfter  int
	}{{1_999, 999, 1_999}, {1, 0, 1}, {1_999, 1, 1}, {1, 0, 1}}
	for v := range heuristicLabels {
		ds := mustForest(n, heuristicFlags[v][0], heuristicFlags[v][1])
		for i := 1; i < n; i++ {
			ds.Union(i, i-1)
		}
		depthBefore := ds.MaxDepth()
		ds.ResetHops()
		for i := 0; i < n; i++ {
			ds.Find(i)
		}
		got := struct {
			depthBefore int
			hopsPerFind int64
			depthAfter  int
		}{depthBefore, ds.Hops() / int64(n), ds.MaxDepth()}
		if got != want[v] {
			t.Errorf("%s: got %+v, want %+v", heuristicLabels[v], got, want[v])
		}
	}
}
