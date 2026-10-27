// Tests for phases/02-algorithms/10-topological-sort-and-dags. Run: go test ./...
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

func checkCycle(t *testing.T, name string, err error) *CycleError {
	t.Helper()
	var ce *CycleError
	if !errors.As(err, &ce) {
		t.Errorf("FAIL: %s (got %v, want *CycleError)", name, err)
	}
	return ce
}

func reversed(in []string) []string {
	out := slices.Clone(in)
	slices.Reverse(out)
	return out
}

func isCycle(g *Digraph, cyc []string) bool {
	for i := range cyc {
		if !slices.Contains(g.succ(cyc[i]), cyc[(i+1)%len(cyc)]) {
			return false
		}
	}
	return true
}

var handTrace = []string{"ingest-captures", "fetch-fx-rates", "compute-fees", "fx-fixing", "net-positions",
	"generate-payout-file", "post-ledger", "reconcile", "submit-to-bank"}

func TestGraphBasics(t *testing.T) {
	g := SettlementGraph()
	check(t, "node count", g.NodeCount() == 9)
	check(t, "edge count", g.EdgeCount() == 10)
	check(t, "duplicate edge ignored", NewDigraph().AddEdge("a", "b").AddEdge("a", "b").EdgeCount() == 1)
	check(t, "in-degree of net-positions is 2", g.InDegrees()["net-positions"] == 2)
	_, err := g.Successors("nope")
	check(t, "unknown node rejected", errors.Is(err, ErrUnknownNode))
	check(t, "nodes are in insertion order", slices.Equal(g.Nodes(), []string{"ingest-captures", "compute-fees", "fx-fixing", "fetch-fx-rates",
		"net-positions", "generate-payout-file", "post-ledger", "reconcile", "submit-to-bank"}))
}

func TestOrders(t *testing.T) {
	g := SettlementGraph()
	// Kahn: valid, deterministic, and exactly the order the lesson text traces
	kahn := must(Kahn(g))
	check(t, "kahn is a topological order", IsTopologicalOrder(g, kahn))
	check(t, "kahn order matches the hand trace", slices.Equal(kahn, handTrace))
	check(t, "kahn is deterministic", slices.Equal(kahn, must(Kahn(SettlementGraph()))))

	// DFS: also valid, usually a different order
	dfs := must(DFS(g))
	check(t, "dfs is a topological order", IsTopologicalOrder(g, dfs))
	check(t, "dfs starts with a source", g.InDegrees()[dfs[0]] == 0)
	check(t, "dfs ends with a sink", len(g.succ(dfs[len(dfs)-1])) == 0)
	check(t, "dfs order matches the Java demo", slices.Equal(dfs, []string{"fetch-fx-rates", "ingest-captures", "fx-fixing", "compute-fees",
		"net-positions", "post-ledger", "reconcile", "generate-payout-file", "submit-to-bank"}))

	// the validator itself
	check(t, "validator rejects a reversed edge", !IsTopologicalOrder(g, reversed(kahn)))
	check(t, "validator rejects a missing node", !IsTopologicalOrder(g, kahn[:8]))
	check(t, "validator rejects a duplicate node", !IsTopologicalOrder(NewDigraph().AddEdge("a", "b"), []string{"a", "a"}))

	// boundaries
	check(t, "empty graph sorts to empty", len(must(Kahn(NewDigraph()))) == 0 && len(must(DFS(NewDigraph()))) == 0)
	check(t, "single node", slices.Equal(must(Kahn(NewDigraph().AddNode("x"))), []string{"x"}))
	disconnected := NewDigraph().AddEdge("a", "b").AddEdge("c", "d")
	check(t, "disconnected components both ordered", IsTopologicalOrder(disconnected, must(Kahn(disconnected))) &&
		IsTopologicalOrder(disconnected, must(DFS(disconnected))))
}

func TestDeepChain(t *testing.T) {
	chain := NewDigraph()
	for i := 0; i < 100_000; i++ {
		chain.AddEdge(fmt.Sprintf("n%d", i), fmt.Sprintf("n%d", i+1))
	}
	check(t, "100k-node chain via dfs", len(must(DFS(chain))) == 100_001)
	check(t, "100k-node chain via kahn", must(Kahn(chain))[0] == "n0")
}

func TestCycles(t *testing.T) {
	g := SettlementGraph()
	broken := SettlementGraph().AddEdge("submit-to-bank", "reconcile")
	_, err := Kahn(broken)
	ce := checkCycle(t, "kahn reports a cycle", err)
	_, err = DFS(broken)
	checkCycle(t, "dfs reports a cycle", err)
	check(t, "reported cycle is submit-to-bank -> reconcile", slices.Equal(FindCycle(broken), []string{"submit-to-bank", "reconcile"}))
	check(t, "acyclic graph reports no cycle", len(FindCycle(g)) == 0)
	self := NewDigraph().AddEdge("a", "a")
	check(t, "self-loop is a cycle of length one", slices.Equal(FindCycle(self), []string{"a"}))
	triangle := NewDigraph().AddEdge("x", "y").AddEdge("y", "z").AddEdge("z", "x").AddEdge("w", "x")
	cyc := FindCycle(triangle)
	check(t, "triangle cycle has three nodes in edge order", len(cyc) == 3 && isCycle(triangle, cyc))
	check(t, "cycle message names the loop", ce != nil && ce.Error() == "cycle detected: submit-to-bank -> reconcile -> submit-to-bank")
	check(t, "a cycle is not an argument error", !errors.Is(err, ErrUnknownNode))
}

func TestScheduling(t *testing.T) {
	g := SettlementGraph()
	scheduler := must(NewDagScheduler(g, SettlementTasks()))
	levels := must(scheduler.Levels())
	check(t, "six levels", len(levels) == 6)
	check(t, "level 0 = sources", slices.Equal(levels[0], []string{"ingest-captures", "fetch-fx-rates"}))
	check(t, "level 3 = payout file and ledger posting", slices.Equal(levels[3], []string{"generate-payout-file", "post-ledger"}))
	check(t, "last level = submit", slices.Equal(levels[5], []string{"submit-to-bank"}))

	s := must(scheduler.Schedule())
	check(t, "makespan 115", s.Makespan == 115)
	check(t, "critical path", slices.Equal(s.CriticalPath, []string{
		"ingest-captures", "compute-fees", "net-positions", "post-ledger", "reconcile", "submit-to-bank"}))
	zeroSlack := true
	var pathMinutes int64
	for _, n := range s.CriticalPath {
		if s.Slots[n].Slack != 0 {
			zeroSlack = false
		}
		for _, task := range SettlementTasks() {
			if task.Name == n {
				pathMinutes += task.Minutes
			}
		}
	}
	check(t, "critical tasks have zero slack", zeroSlack)
	check(t, "fetch-fx-rates slack 20", s.Slots["fetch-fx-rates"].Slack == 20)
	check(t, "fx-fixing slack 5", s.Slots["fx-fixing"].Slack == 5)
	check(t, "generate-payout-file slack 40", s.Slots["generate-payout-file"].Slack == 40)
	check(t, "net-positions ES 35 EF 60", s.Slots["net-positions"].EarliestStart == 35 && s.Slots["net-positions"].EarliestFinish == 60)
	check(t, "critical path length equals makespan", pathMinutes == 115)
	_, err := NewDagScheduler(g, []Task{{"ingest-captures", 1}})
	check(t, "scheduler needs every duration", errors.Is(err, ErrNoDuration))
	_, err = NewTask("t", -1)
	check(t, "negative duration rejected", errors.Is(err, ErrNegativeDuration))
	broken := SettlementGraph().AddEdge("submit-to-bank", "reconcile")
	_, err = must(NewDagScheduler(broken, SettlementTasks())).Schedule()
	checkCycle(t, "scheduling a cyclic graph fails", err)
	_, err = must(NewDagScheduler(broken, SettlementTasks())).Levels()
	checkCycle(t, "levelling a cyclic graph fails", err)
	huge := NewDigraph().AddEdge("a", "b")
	_, err = must(NewDagScheduler(huge, []Task{{"a", 1 << 62}, {"b", 1 << 62}})).Schedule()
	check(t, "durations that overflow int64 are reported, not wrapped", errors.Is(err, ErrOverflow))
}
