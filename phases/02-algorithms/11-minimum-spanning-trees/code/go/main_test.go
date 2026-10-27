// Tests for phases/02-algorithms/11-minimum-spanning-trees. Run: go test ./...
package main

import (
	"errors"
	"fmt"
	"slices"
	"testing"
)

func check(t *testing.T, name string, condition bool) {
	t.Helper()
	if !condition {
		t.Errorf("FAIL: %s", name)
	}
}

func weights(f SpanningForest) []int64 {
	w := make([]int64, len(f.Edges))
	for i, e := range f.Edges {
		w[i] = e.Weight
	}
	return w
}

func TestUnionFind(t *testing.T) {
	uf := NewUnionFind([]string{"a", "b", "c", "d"})
	check(t, "fresh sets are disjoint", uf.Components() == 4 && !uf.Connected("a", "b"))
	check(t, "union merges", uf.Union("a", "b") && uf.Connected("a", "b") && uf.Components() == 3)
	check(t, "union of same set returns false", !uf.Union("b", "a") && uf.Components() == 3)
	check(t, "transitive", uf.Union("c", "d") && uf.Union("b", "c") && uf.Connected("a", "d") && uf.Components() == 1)
	check(t, "find is a fixed point", uf.Find(uf.Find("d")) == uf.Find("d"))
}

func TestEdgeValidation(t *testing.T) {
	_, err := NewWeightedEdge("a", "a", 1)
	check(t, "self-loop rejected", errors.Is(err, ErrSelfLoop))
	_, err = NewWeightedEdge("a", "b", -1)
	check(t, "negative weight rejected", errors.Is(err, ErrNegativeWeight))
	check(t, "other endpoint", must(NewWeightedEdge("a", "b", 1)).Other("a") == "b")
	_, err = NewWeightedGraph().Incident("nope")
	check(t, "incident on unknown node is an error", errors.Is(err, ErrUnknownNode))
}

func TestKruskalAndPrim(t *testing.T) {
	g := SiteNetwork()
	// Kruskal on the site network: the hand trace
	k := must(Kruskal(g))
	check(t, "kruskal is a tree", k.IsTree() && len(k.Edges) == 7)
	check(t, "kruskal total 39", k.TotalWeight == 39)
	check(t, "kruskal order is lightest-first with cycle skips", slices.Equal(weights(k), []int64{2, 3, 4, 5, 6, 7, 12}))
	check(t, "kruskal skips newyork-chicago 9", !slices.ContainsFunc(k.Edges, func(e WeightedEdge) bool {
		return e.SameEndpoints(WeightedEdge{"newyork", "chicago", 0})
	}))
	check(t, "kruskal passes the independent checker", IsMinimumSpanningForest(g, k))

	// Prim agrees
	p := must(Prim(g, "ashburn"))
	check(t, "prim total 39", p.TotalWeight == 39 && p.IsTree())
	check(t, "prim grows from the start node", p.Edges[0].SameEndpoints(WeightedEdge{"ashburn", "newyork", 4}))
	check(t, "prim and kruskal choose the same edges (distinct weights)", SameEdgeSet(k, p))
	allStarts := true
	for _, n := range g.Nodes() {
		if must(Prim(g, n)).TotalWeight != 39 {
			allStarts = false
		}
	}
	check(t, "prim from any start gives the same total", allStarts)
	_, err := Prim(g, "mars")
	check(t, "prim rejects unknown start", errors.Is(err, ErrUnknownNode))
	check(t, "prim order matches the Java demo", slices.Equal(weights(p), []int64{4, 7, 6, 5, 3, 12, 2}))

	// the checker rejects wrong answers
	wrong := slices.Clone(k.Edges)
	wrong[len(wrong)-1] = g.Edges()[5] // swap newyork-london 12 for chicago-london 15
	check(t, "checker rejects a heavier spanning tree", !IsMinimumSpanningForest(g, SpanningForest{wrong, 42, 1}))
	check(t, "checker rejects too few edges", !IsMinimumSpanningForest(g, SpanningForest{k.Edges[:6], 27, 1}))
}

func TestTiesAndBoundaries(t *testing.T) {
	// equal weights: both algorithms still produce minimum forests, possibly different edges
	square := NewWeightedGraph().AddEdge("a", "b", 1).AddEdge("b", "c", 1).AddEdge("c", "d", 1).AddEdge("d", "a", 1)
	ks, ps := must(Kruskal(square)), must(Prim(square, "c"))
	check(t, "ties: kruskal weight 3", ks.TotalWeight == 3 && IsMinimumSpanningForest(square, ks))
	check(t, "ties: prim weight 3", ps.TotalWeight == 3 && IsMinimumSpanningForest(square, ps))
	check(t, "ties: kruskal keeps insertion order", slices.Equal(ks.Edges, square.Edges()[:3]))
	check(t, "ties: deterministic across runs", slices.Equal(must(Kruskal(square)).Edges, ks.Edges) && slices.Equal(must(Prim(square, "c")).Edges, ps.Edges))

	// boundaries
	empty := must(Kruskal(NewWeightedGraph()))
	check(t, "empty graph", len(empty.Edges) == 0 && empty.ComponentCount == 0)
	check(t, "single node", must(Kruskal(NewWeightedGraph().AddNode("x"))).ComponentCount == 1 &&
		len(must(Prim(NewWeightedGraph().AddNode("x"), "x")).Edges) == 0)
	parallel := NewWeightedGraph().AddEdge("a", "b", 5).AddEdge("a", "b", 3)
	check(t, "parallel edges: lighter one chosen", must(Kruskal(parallel)).TotalWeight == 3 && must(Prim(parallel, "a")).TotalWeight == 3)

	// disconnected graph gives a forest, not an error
	two := NewWeightedGraph().AddEdge("a", "b", 1).AddEdge("c", "d", 2).AddNode("e")
	f := must(Kruskal(two))
	check(t, "forest: 3 components, 2 edges", f.ComponentCount == 3 && len(f.Edges) == 2 && !f.IsTree())
	pa := must(Prim(two, "a"))
	check(t, "forest: prim agrees", pa.ComponentCount == 3 && pa.TotalWeight == 3)
	check(t, "forest passes checker", IsMinimumSpanningForest(two, f))

	// int64 wraps: totals are checked
	huge := NewWeightedGraph().AddEdge("a", "b", 1<<62).AddEdge("b", "c", 1<<62)
	_, err := Kruskal(huge)
	check(t, "kruskal reports total overflow instead of wrapping", errors.Is(err, ErrOverflow))
	_, err = Prim(huge, "a")
	check(t, "prim reports total overflow instead of wrapping", errors.Is(err, ErrOverflow))
}

func TestRandomGraphs(t *testing.T) {
	// Kruskal == Prim total on 30 seeded graphs (the same java.util.Random(42) graphs as the Java test), both pass the checker
	rng := NewJavaRandom(42)
	agree, verified := true, true
	for trial := 0; trial < 30; trial++ {
		r := NewWeightedGraph()
		n := 4 + rng.NextIntN(6)
		for i := 0; i < n; i++ {
			r.AddNode(fmt.Sprintf("n%d", i))
		}
		m := n + rng.NextIntN(n*2)
		for i := 0; i < m; i++ {
			a, b := rng.NextIntN(n), rng.NextIntN(n)
			if a != b {
				r.AddEdge(fmt.Sprintf("n%d", a), fmt.Sprintf("n%d", b), int64(1+rng.NextIntN(20)))
			}
		}
		rk, rp := must(Kruskal(r)), must(Prim(r, "n0"))
		agree = agree && rk.TotalWeight == rp.TotalWeight && rk.ComponentCount == rp.ComponentCount
		verified = verified && IsMinimumSpanningForest(r, rk) && IsMinimumSpanningForest(r, rp)
	}
	check(t, "30 random graphs: kruskal and prim agree on total", agree)
	check(t, "30 random graphs: both pass the cut-property checker", verified)
}

func TestClustering(t *testing.T) {
	g := SiteNetwork()
	three := must(Clusters(g, 3))
	check(t, "3 clusters", len(three) == 3 &&
		slices.Equal(three[0], []string{"ashburn", "newyork"}) &&
		slices.Equal(three[1], []string{"chicago", "dallas", "phoenix", "sanjose"}) &&
		slices.Equal(three[2], []string{"london", "frankfurt"}))
	one := must(Clusters(g, 1))
	check(t, "1 cluster is everything", len(one) == 1 && len(one[0]) == 8)
	check(t, "k >= n gives singletons", len(must(Clusters(g, 10))) == 8)
	_, err := Clusters(g, 0)
	check(t, "k must be positive", errors.Is(err, ErrBadK))
}
