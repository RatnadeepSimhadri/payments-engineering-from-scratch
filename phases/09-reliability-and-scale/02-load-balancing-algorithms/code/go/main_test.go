// Tests for phases/09-reliability-and-scale/02-load-balancing-algorithms. Run: go test ./...
package main

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func req(i int64, key string) Request { return Request{i, key, 10.0} }

func pick(t *testing.T, b Balancer, healthy []*Backend, r Request) *Backend {
	t.Helper()
	out, err := b.Pick(healthy, r)
	if err != nil {
		t.Fatalf("Pick: %v", err)
	}
	return out
}

func TestRngMatchesJavaAndPython(t *testing.T) {
	if got := NewRng(42).NextLong(); got != -4767286540954276203 {
		t.Fatalf("splitmix64(42) = %d", got)
	}
	rng := NewRng(1)
	for i := 0; i < 100; i++ {
		if d := rng.NextDouble(); d < 0 || d >= 1 {
			t.Fatalf("NextDouble out of range: %v", d)
		}
		if n := rng.NextInt(5); n < 0 || n >= 5 {
			t.Fatalf("NextInt out of range: %d", n)
		}
	}
}

func TestRoundRobin(t *testing.T) {
	abc := []*Backend{NewBackend("a", 1, 1), NewBackend("b", 1, 1), NewBackend("c", 1, 1)}
	rr := &RoundRobin{}
	var seq strings.Builder
	for i := int64(0); i < 6; i++ {
		seq.WriteString(pick(t, rr, abc, req(i, "k")).ID)
	}
	if seq.String() != "abcabc" {
		t.Fatalf("sequence %q", seq.String())
	}
	ac := []*Backend{abc[0], abc[2]}
	if b := pick(t, rr, ac, req(7, "k")); b != abc[0] && b != abc[2] {
		t.Fatalf("picked outside healthy subset: %v", b)
	}
	if _, err := rr.Pick(nil, req(0, "k")); !errors.Is(err, ErrNoHealthyBackends) {
		t.Fatalf("expected ErrNoHealthyBackends, got %v", err)
	}
}

func TestWeightedRoundRobinSmooth(t *testing.T) {
	weighted := []*Backend{NewBackend("a", 5, 1), NewBackend("b", 1, 1), NewBackend("c", 1, 1)}
	wrr := NewWeightedRoundRobin()
	var seq strings.Builder
	for i := int64(0); i < 7; i++ {
		seq.WriteString(pick(t, wrr, weighted, req(i, "k")).ID)
	}
	if seq.String() != "aabacaa" {
		t.Fatalf("sequence %q", seq.String())
	}
	aCount := 0
	for i := int64(0); i < 70; i++ {
		if pick(t, wrr, weighted, req(i, "k")).ID == "a" {
			aCount++
		}
	}
	if aCount != 50 {
		t.Fatalf("a got %d of 70", aCount)
	}
}

func TestBackendRejectsZeroWeight(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic")
		}
	}()
	NewBackend("x", 0, 1.0)
}

func TestLeastConnections(t *testing.T) {
	l1, l2, l3 := NewBackend("l1", 1, 1), NewBackend("l2", 1, 1), NewBackend("l3", 1, 1)
	l1.ActiveConnections, l2.ActiveConnections, l3.ActiveConnections = 3, 1, 1
	if pick(t, LeastConnections{}, []*Backend{l1, l2, l3}, req(0, "k")) != l2 {
		t.Fatal("expected l2 (fewest, first on tie)")
	}
	l2.ActiveConnections = 5
	if pick(t, LeastConnections{}, []*Backend{l1, l2, l3}, req(0, "k")) != l3 {
		t.Fatal("expected l3 after load change")
	}
}

func TestPowerOfTwoChoices(t *testing.T) {
	p1, p2 := NewBackend("p1", 1, 1), NewBackend("p2", 1, 1)
	p1.ActiveConnections = 10
	p2c := NewPowerOfTwoChoices(NewRng(5))
	for i := int64(0); i < 50; i++ {
		if pick(t, p2c, []*Backend{p1, p2}, req(i, "k")) != p2 {
			t.Fatal("P2C picked the more loaded of two")
		}
	}
	if pick(t, p2c, []*Backend{p1}, req(0, "k")) != p1 {
		t.Fatal("single backend must be returned")
	}
}

func TestBallsInBins(t *testing.T) {
	one := BallsInBins(10_000, 10_000, 1, NewRng(7))
	two := BallsInBins(10_000, 10_000, 2, NewRng(7))
	if one != 6 || two != 3 {
		t.Fatalf("max loads one=%d two=%d", one, two)
	}
	if two >= one {
		t.Fatal("two choices should beat one")
	}
}

func TestConsistentHash(t *testing.T) {
	ch := NewConsistentHash(100)
	six := Fleet()
	if pick(t, ch, six, req(1, "merchant-77")) != pick(t, ch, six, req(2, "merchant-77")) {
		t.Fatal("same key must map to the same backend")
	}
	removed := six[2]
	five := append(append([]*Backend{}, six[:2]...), six[3:]...)
	moved, movedFromOther := 0, 0
	owners := map[string]bool{}
	for k := 0; k < 1000; k++ {
		r := req(int64(k), fmt.Sprintf("merchant-%d", k))
		before, after := pick(t, ch, six, r), pick(t, ch, five, r)
		owners[before.ID] = true
		if before != after {
			moved++
			if before != removed {
				movedFromOther++
			}
		}
	}
	if movedFromOther != 0 {
		t.Fatalf("%d keys moved from backends that stayed", movedFromOther)
	}
	if moved <= 100 || moved >= 250 {
		t.Fatalf("moved %d keys, expected about 167", moved)
	}
	if len(owners) != 6 {
		t.Fatalf("only %d backends own keys", len(owners))
	}
	if Hash64("merchant-1") == Hash64("merchant-2") {
		t.Fatal("hash should separate neighbours")
	}
}

func TestConsistentHashRejectsZeroVirtualNodes(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic")
		}
	}()
	NewConsistentHash(0)
}

func TestHealthCheckerConsecutive(t *testing.T) {
	h := NewBackend("h", 1, 1)
	hc := NewHealthChecker(3, 2)
	for _, ok := range []bool{false, false, true, false, false} {
		hc.Record(h, ok)
	}
	if !h.Healthy {
		t.Fatal("a success should reset the failure streak")
	}
	if changed := hc.Record(h, false); !changed || h.Healthy {
		t.Fatal("third consecutive failure should evict")
	}
	hc.Record(h, true)
	if h.Healthy {
		t.Fatal("one success is not enough to readmit")
	}
	if changed := hc.Record(h, true); !changed || !h.Healthy {
		t.Fatal("second consecutive success should readmit")
	}
}

func TestPoolRoutesAroundEvictedAndSheds(t *testing.T) {
	x, y := NewBackend("x", 1, 1), NewBackend("y", 1, 1)
	pool := NewPool([]*Backend{x, y}, &RoundRobin{}, NewHealthChecker(1, 1))
	first := pool.Route(req(0, "k"))
	if first.ActiveConnections != 1 || first.TotalAssigned != 1 {
		t.Fatal("route should count the connection")
	}
	pool.Complete(first, false)
	if first.Healthy || first.ActiveConnections != 0 {
		t.Fatal("one failure with threshold 1 should evict")
	}
	if pool.Route(req(1, "k")) == first || pool.Route(req(2, "k")) == first {
		t.Fatal("routing should avoid the evicted backend")
	}
	other := y
	if first == y {
		other = x
	}
	pool.Complete(other, false)
	if pool.Route(req(3, "k")) != nil {
		t.Fatal("no healthy backends should shed (nil)")
	}
}

func TestSimulationDeterministicAndLoadAwareWins(t *testing.T) {
	rrA := Simulate(&RoundRobin{}, 42, false)
	rrB := Simulate(&RoundRobin{}, 42, false)
	if rrA.P99Ms != rrB.P99Ms || rrA.MaxQueue != rrB.MaxQueue {
		t.Fatal("simulation must be deterministic under a seed")
	}
	lc := Simulate(LeastConnections{}, 42, false)
	p2c := Simulate(NewPowerOfTwoChoices(NewRng(11)), 42, false)
	if lc.P99Ms*10 >= rrA.P99Ms || p2c.P99Ms*10 >= rrA.P99Ms {
		t.Fatalf("load-aware p99 should be far below round robin: rr=%v lc=%v p2c=%v", rrA.P99Ms, lc.P99Ms, p2c.P99Ms)
	}
	if p2c.MaxQueue > 2*lc.MaxQueue {
		t.Fatalf("P2C max queue %d vs least-connections %d", p2c.MaxQueue, lc.MaxQueue)
	}
	if rrA.MaxAssigned-rrA.MinAssigned > 1 {
		t.Fatal("round robin should assign equally regardless of speed")
	}
	if lc.MinAssigned >= rrA.MinAssigned {
		t.Fatal("least connections should send less to the slow backend")
	}
}

func TestEvictionBoundsFailuresDuringOutage(t *testing.T) {
	outage := Simulate(LeastConnections{}, 42, true)
	if outage.Failed <= 0 || outage.Failed >= 20 {
		t.Fatalf("failed = %d", outage.Failed)
	}
	if outage.Shed != 0 {
		t.Fatalf("shed = %d", outage.Shed)
	}
}

func TestPercentileNearestRank(t *testing.T) {
	if Percentile([]float64{1, 2, 3, 4}, 50) != 2 || Percentile([]float64{1, 2, 3, 4}, 99) != 4 || Percentile(nil, 50) != 0 {
		t.Fatal("nearest-rank percentile")
	}
}
