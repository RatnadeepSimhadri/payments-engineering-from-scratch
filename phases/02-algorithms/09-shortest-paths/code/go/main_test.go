// Tests for phases/02-algorithms/09-shortest-paths. Run: go test ./...
package main

import (
	"errors"
	"fmt"
	"reflect"
	"testing"
)

// SeededRandom is a tiny 64-bit LCG, identical in all three languages, so the random graph is the same everywhere.
type SeededRandom struct{ state uint64 }

func (r *SeededRandom) NextInt(bound int) int {
	r.state = r.state*6364136223846793005 + 1442695040888963407
	return int((r.state >> 33) % uint64(bound))
}

func spreads() *WeightedGraph {
	g := NewWeightedGraph()
	g.AddEdge("USD", "EUR", 20)
	g.AddEdge("EUR", "JPY", 20)
	g.AddEdge("USD", "JPY", 100)
	g.AddEdge("EUR", "GBP", 20)
	g.AddEdge("GBP", "JPY", 15)
	g.AddNode("CHF")
	return g
}

func negativeEdgeGraph() *WeightedGraph {
	g := NewWeightedGraph()
	g.AddEdge("A", "B", 5)
	g.AddEdge("A", "C", 2)
	g.AddEdge("C", "B", -4)
	return g
}

func TestDijkstraSpreads(t *testing.T) {
	d := must(Dijkstra(spreads(), "USD"))
	if d.Distance["JPY"] != 40 || !reflect.DeepEqual(d.PathTo("JPY"), []string{"USD", "EUR", "JPY"}) {
		t.Fatalf("JPY: %d %v", d.Distance["JPY"], d.PathTo("JPY"))
	}
	if d.Distance["GBP"] != 40 || !reflect.DeepEqual(d.PathTo("GBP"), []string{"USD", "EUR", "GBP"}) {
		t.Fatalf("GBP: %d %v", d.Distance["GBP"], d.PathTo("GBP"))
	}
	if d.Distance["USD"] != 0 || !reflect.DeepEqual(d.PathTo("USD"), []string{"USD"}) {
		t.Fatal("source distance and path")
	}
	if d.Reached("CHF") || len(d.PathTo("CHF")) != 0 {
		t.Fatal("unreachable node must be absent and have an empty path")
	}
}

func TestDijkstraDecreaseKeyAndZeroWeights(t *testing.T) {
	g := NewWeightedGraph()
	g.AddEdge("A", "B", 10)
	g.AddEdge("A", "C", 1)
	g.AddEdge("C", "B", 2)
	r := must(Dijkstra(g, "A"))
	if r.Distance["B"] != 3 || !reflect.DeepEqual(r.PathTo("B"), []string{"A", "C", "B"}) {
		t.Fatalf("decreaseKey: %d %v", r.Distance["B"], r.PathTo("B"))
	}
	zero := NewWeightedGraph()
	zero.AddEdge("A", "B", 0)
	zero.AddEdge("B", "C", 0)
	if must(Dijkstra(zero, "A")).Distance["C"] != 0 {
		t.Fatal("zero-weight edges")
	}
}

func TestDijkstraRejections(t *testing.T) {
	if _, err := Dijkstra(negativeEdgeGraph(), "A"); !errors.Is(err, ErrNegativeWeight) {
		t.Fatalf("want ErrNegativeWeight, got %v", err)
	}
	if _, err := Dijkstra(spreads(), "XXX"); !errors.Is(err, ErrUnknownNode) {
		t.Fatalf("want ErrUnknownNode, got %v", err)
	}
}

func TestBellmanFordMatchesDijkstra(t *testing.T) {
	bf := must(BellmanFord(spreads(), "USD"))
	if !reflect.DeepEqual(bf.Paths.Distance, must(Dijkstra(spreads(), "USD")).Distance) || bf.NegativeCycle != nil {
		t.Fatal("bellman-ford must match dijkstra on spreads")
	}
	rng := &SeededRandom{42}
	g := NewWeightedGraph()
	for i := 0; i < 40; i++ {
		g.AddNode(fmt.Sprintf("n%d", i))
	}
	for i := 0; i < 160; i++ {
		g.AddEdge(fmt.Sprintf("n%d", rng.NextInt(40)), fmt.Sprintf("n%d", rng.NextInt(40)), int64(rng.NextInt(100)))
	}
	dr := must(Dijkstra(g, "n0"))
	br := must(BellmanFord(g, "n0"))
	if !reflect.DeepEqual(dr.Distance, br.Paths.Distance) || len(dr.Distance) <= 1 {
		t.Fatal("random graph: distances differ")
	}
	for node, dist := range dr.Distance {
		path := dr.PathTo(node)
		var total int64
		for i := 0; i+1 < len(path); i++ {
			best := int64(1 << 62)
			out, _ := g.OutEdges(path[i])
			for _, e := range out {
				if e.To == path[i+1] && e.Weight < best {
					best = e.Weight
				}
			}
			total += best
		}
		if total != dist {
			t.Fatalf("path to %s sums to %d, distance %d", node, total, dist)
		}
	}
}

func TestBellmanFordNegativeEdge(t *testing.T) {
	r := must(BellmanFord(negativeEdgeGraph(), "A"))
	if r.Paths.Distance["B"] != -2 || r.NegativeCycle != nil {
		t.Fatalf("negative edge: %d %v", r.Paths.Distance["B"], r.NegativeCycle)
	}
}

func TestBellmanFordNegativeCycle(t *testing.T) {
	g := NewWeightedGraph()
	g.AddEdge("A", "B", 1)
	g.AddEdge("B", "C", -3)
	g.AddEdge("C", "A", 1)
	c := must(BellmanFord(g, "A")).NegativeCycle
	if c == nil || len(c) != 4 || c[0] != c[3] {
		t.Fatalf("cycle %v", c)
	}
	seen := map[string]bool{}
	for _, n := range c {
		seen[n] = true
	}
	if !seen["A"] || !seen["B"] || !seen["C"] {
		t.Fatalf("cycle members %v", c)
	}
	unreachable := NewWeightedGraph()
	unreachable.AddEdge("S", "T", 1)
	unreachable.AddEdge("P", "Q", -5)
	unreachable.AddEdge("Q", "P", 1)
	if must(BellmanFord(unreachable, "S")).NegativeCycle != nil {
		t.Fatal("unreachable cycle must not be reported")
	}
}

func TestWeights(t *testing.T) {
	cases := map[int64]int64{RateScale: 0, 91_816_000: 85384, 108_478_260: -81380, 15_000_000_000: -5010635}
	for rate, want := range cases {
		if got := WeightOf(rate); got != want {
			t.Fatalf("WeightOf(%d) = %d, want %d", rate, got, want)
		}
	}
	if WeightOf(50_000_000)+WeightOf(200_000_000) != 0 {
		t.Fatal("reciprocal rates must cancel")
	}
}

func TestBestPathAndConversion(t *testing.T) {
	rates := Market()
	best := must(BestConversionPath(rates, "USD", "JPY"))
	if !reflect.DeepEqual(best, []string{"USD", "EUR", "JPY"}) {
		t.Fatalf("best path %v", best)
	}
	tenK := OfMinor(1_000_000, USD)
	if got := must(ConvertAlong(tenK, best, rates, Currencies)); got != OfMinor(1_493_975, JPY) {
		t.Fatalf("along best: %v", got)
	}
	if got := must(ConvertAlong(tenK, []string{"USD", "JPY"}, rates, Currencies)); got != OfMinor(1_485_000, JPY) {
		t.Fatalf("direct: %v", got)
	}
	if got := must(OfMinor(10_000, USD).Convert(15_000_000_000, JPY, HalfEven)); got != OfMinor(15_000, JPY) {
		t.Fatalf("USD->JPY exponent: %v", got)
	}
	if got := must(OfMinor(15_000, JPY).Convert(666_667, USD, HalfEven)); got != OfMinor(10_000, USD) {
		t.Fatalf("JPY->USD exponent: %v", got)
	}
	if _, err := ConvertAlong(tenK, []string{"EUR", "USD"}, rates, Currencies); err == nil {
		t.Fatal("path must start at the amount's currency")
	}
	if _, err := NewRate("A", "B", 0); err == nil {
		t.Fatal("rate must be positive")
	}
}

func TestArbitrage(t *testing.T) {
	rates := Market()
	if arb := must(Arbitrage(rates)); arb != nil {
		t.Fatalf("consistent market reported arbitrage %v", arb)
	}
	stale := append(append([]Rate(nil), rates[:8]...), Rate{"GBP", "USD", 128_000_000})
	arb := must(Arbitrage(stale))
	if arb == nil || len(arb) != 4 {
		t.Fatalf("arbitrage %v", arb)
	}
	p := must(ComputeCycleProduct(stale, arb))
	if !p.ExceedsOne() || p.GainBasisPoints() != 86 {
		t.Fatalf("product %v/%v gain %d", p.Numerator, p.Denominator, p.GainBasisPoints())
	}
	fromUsd := must(RotateTo(arb, "USD"))
	if fromUsd[0] != "USD" || fromUsd[3] != "USD" || len(fromUsd) != 4 {
		t.Fatalf("rotated %v", fromUsd)
	}
	if got := must(ConvertAlong(OfMinor(1_000_000, USD), fromUsd, stale, Currencies)); got != OfMinor(1_008_689, USD) {
		t.Fatalf("around the loop: %v", got)
	}
	if _, err := BestConversionPath(stale, "USD", "JPY"); !errors.Is(err, ErrArbitrage) {
		t.Fatalf("want ErrArbitrage, got %v", err)
	}
}

func TestRoundingArtefactRejectedByExactProduct(t *testing.T) {
	exact := []Rate{{"A", "B", 16_000_000}, {"B", "C", 25_000_000}, {"C", "A", 2_500_000_000}}
	if s := WeightOf(16_000_000) + WeightOf(25_000_000) + WeightOf(2_500_000_000); s != -1 {
		t.Fatalf("weight sum %d, want -1", s)
	}
	if must(BellmanFord(FromRates(exact), "A")).NegativeCycle == nil {
		t.Fatal("raw bellman-ford should report the rounded cycle")
	}
	if arb := must(Arbitrage(exact)); arb != nil {
		t.Fatalf("exact product check should reject %v", arb)
	}
	p := must(ComputeCycleProduct(exact, []string{"A", "B", "C", "A"}))
	if p.ExceedsOne() || p.GainBasisPoints() != 0 {
		t.Fatal("product of the exact cycle must be exactly one")
	}
}
