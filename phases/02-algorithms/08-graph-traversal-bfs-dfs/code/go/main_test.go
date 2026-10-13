// Tests for phases/02-algorithms/08-graph-traversal-bfs-dfs. Run: go test ./...
package main

import (
	"errors"
	"fmt"
	"reflect"
	"testing"
)

// flowGraph: ring A->B->C->A, tail C->D->E, shortcut A->D, separate payroll cluster, one isolated node.
func flowGraph() *Graph {
	g := NewGraph(true)
	for _, e := range [][2]string{{"A", "B"}, {"B", "C"}, {"C", "A"}, {"C", "D"}, {"D", "E"}, {"A", "D"},
		{"payroll", "X"}, {"X", "landlord"}, {"X", "grocer"}} {
		g.AddEdge(e[0], e[1])
	}
	g.AddNode("dormant")
	return g
}

func TestGraphCountsAndOrder(t *testing.T) {
	g := flowGraph()
	if g.NodeCount() != 10 || g.EdgeCount() != 9 {
		t.Fatalf("counts: %d nodes %d edges", g.NodeCount(), g.EdgeCount())
	}
	if ns := must(g.Neighbors("A")); !reflect.DeepEqual(ns, []string{"B", "D"}) {
		t.Fatalf("neighbours of A: %v", ns)
	}
	if _, err := g.Neighbors("nope"); !errors.Is(err, ErrUnknownNode) {
		t.Fatalf("want ErrUnknownNode, got %v", err)
	}
}

func TestUndirected(t *testing.T) {
	und := NewGraph(false)
	und.AddEdge("p", "q")
	if ns := must(und.Neighbors("q")); !reflect.DeepEqual(ns, []string{"p"}) || und.EdgeCount() != 1 {
		t.Fatalf("undirected edge: %v, %d", ns, und.EdgeCount())
	}
	if ns := must(flowGraph().UndirectedView().Neighbors("B")); !reflect.DeepEqual(ns, []string{"A", "C"}) {
		t.Fatalf("undirected view of B: %v", ns)
	}
}

func TestBfsDistancesAndOrder(t *testing.T) {
	r := must(Bfs(flowGraph(), "A"))
	want := map[string]int{"A": 0, "B": 1, "D": 1, "C": 2, "E": 2}
	if !reflect.DeepEqual(r.Distance, want) {
		t.Fatalf("distances %v", r.Distance)
	}
	if !reflect.DeepEqual(r.Order, []string{"A", "B", "D", "C", "E"}) {
		t.Fatalf("order %v", r.Order)
	}
	if r.Reached("X") || r.Reached("dormant") {
		t.Fatal("must not reach the other cluster")
	}
	if r.Parent["E"] != "D" || r.Parent["D"] != "A" {
		t.Fatalf("parents %v", r.Parent)
	}
}

func TestShortestHopPath(t *testing.T) {
	g := flowGraph()
	if p := must(ShortestHopPath(g, "A", "E")); !reflect.DeepEqual(p, []string{"A", "D", "E"}) {
		t.Fatalf("A->E %v", p)
	}
	if p := must(ShortestHopPath(g, "A", "A")); !reflect.DeepEqual(p, []string{"A"}) {
		t.Fatalf("A->A %v", p)
	}
	if p := must(ShortestHopPath(g, "A", "X")); len(p) != 0 {
		t.Fatalf("unreachable should be empty, got %v", p)
	}
	if len(must(ShortestHopPath(g, "A", "C"))) != must(Bfs(g, "A")).Distance["C"]+1 {
		t.Fatal("path length must equal distance + 1")
	}
	if _, err := Bfs(g, "nope"); !errors.Is(err, ErrUnknownNode) {
		t.Fatal("bfs from unknown node must fail")
	}
}

func TestDfsPreorder(t *testing.T) {
	g := flowGraph()
	if o := must(DfsIterative(g, "A")); !reflect.DeepEqual(o, []string{"A", "B", "C", "D", "E"}) {
		t.Fatalf("dfs order %v", o)
	}
	if o := must(DfsIterative(g, "dormant")); !reflect.DeepEqual(o, []string{"dormant"}) {
		t.Fatalf("dfs isolated %v", o)
	}
}

func TestDeepChain(t *testing.T) {
	chain := NewGraph(true)
	for i := 0; i < 100_000; i++ {
		chain.AddEdge(fmt.Sprintf("n%d", i), fmt.Sprintf("n%d", i+1))
	}
	if n := len(must(DfsIterative(chain, "n0"))); n != 100_001 {
		t.Fatalf("visited %d", n)
	}
	if d := must(Bfs(chain, "n0")).Distance["n100000"]; d != 100_000 {
		t.Fatalf("distance %d", d)
	}
}

func TestComponents(t *testing.T) {
	comps := ConnectedComponents(flowGraph())
	want := [][]string{{"A", "B", "D", "C", "E"}, {"payroll", "X", "landlord", "grocer"}, {"dormant"}}
	if !reflect.DeepEqual(comps, want) {
		t.Fatalf("components %v", comps)
	}
	if len(Rings(flowGraph(), 3)) != 2 || len(Rings(flowGraph(), 5)) != 1 {
		t.Fatal("ring size filter")
	}
}

func TestCycleFound(t *testing.T) {
	if c := must(FindCycle(flowGraph())); !reflect.DeepEqual(c, []string{"A", "B", "C", "A"}) {
		t.Fatalf("cycle %v", c)
	}
}

func TestDiamondIsNotACycle(t *testing.T) {
	dag := NewGraph(true)
	dag.AddEdge("payroll", "X")
	dag.AddEdge("X", "landlord")
	dag.AddEdge("payroll", "landlord")
	if c := must(FindCycle(dag)); c != nil {
		t.Fatalf("diamond reported as cycle %v", c)
	}
}

func TestSelfLoopAndLaterComponent(t *testing.T) {
	self := NewGraph(true)
	self.AddEdge("s", "s")
	if c := must(FindCycle(self)); !reflect.DeepEqual(c, []string{"s", "s"}) {
		t.Fatalf("self loop %v", c)
	}
	later := NewGraph(true)
	later.AddEdge("a", "b")
	later.AddEdge("c", "d")
	later.AddEdge("d", "c")
	if c := must(FindCycle(later)); !reflect.DeepEqual(c, []string{"c", "d", "c"}) {
		t.Fatalf("later component %v", c)
	}
	two := NewGraph(true)
	two.AddEdge("p", "q")
	two.AddEdge("q", "r")
	two.AddEdge("r", "q")
	if c := must(FindCycle(two)); !reflect.DeepEqual(c, []string{"q", "r", "q"}) {
		t.Fatalf("cycle below root %v", c)
	}
	und := NewGraph(false)
	und.AddEdge("p", "q")
	if _, err := FindCycle(und); err == nil {
		t.Fatal("FindCycle must reject undirected graphs")
	}
}

func TestTransfersAndVolume(t *testing.T) {
	transfers := []Transfer{
		{"A", "B", OfMinor(50000, USD)},
		{"A", "B", OfMinor(12000, USD)},
		{"B", "A", OfMinor(100, USD)},
	}
	g := FromTransfers(transfers)
	if g.EdgeCount() != 2 || !reflect.DeepEqual(must(g.Neighbors("A")), []string{"B"}) {
		t.Fatal("parallel edges must be deduplicated")
	}
	keys, vol, err := Volume(transfers)
	if err != nil || !reflect.DeepEqual(keys, []string{"A->B", "B->A"}) {
		t.Fatalf("volume keys %v err %v", keys, err)
	}
	if vol["A->B"] != OfMinor(62000, USD) || vol["B->A"] != OfMinor(100, USD) {
		t.Fatalf("volume %v", vol)
	}
	if vol["A->B"].String() != "620.00 USD" {
		t.Fatalf("money string %s", vol["A->B"])
	}
	if c := must(CircularFlow(g)); !reflect.DeepEqual(c, []string{"A", "B", "A"}) {
		t.Fatalf("circular flow %v", c)
	}
	_, _, err = Volume([]Transfer{{"A", "B", OfMinor(1, USD)}, {"A", "B", OfMinor(1, EUR)}})
	if !errors.Is(err, ErrCurrencyMismatch) {
		t.Fatalf("want ErrCurrencyMismatch, got %v", err)
	}
}
