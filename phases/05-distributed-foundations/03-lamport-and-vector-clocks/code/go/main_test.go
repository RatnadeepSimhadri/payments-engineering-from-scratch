// Tests for phases/05-distributed-foundations/03-lamport-and-vector-clocks. Run: go test ./...
package main

import (
	"strings"
	"testing"
)

// clockCondition: happens-before (by vector clock) must imply a strictly smaller Lamport timestamp, for every pair.
func clockCondition(events []*Event) bool {
	for _, x := range events {
		for _, y := range events {
			if x.Vector.HappensBefore(y.Vector) && !x.Lamport.Less(y.Lamport) {
				return false
			}
		}
	}
	return true
}

func logOf(net *SimulatedNetwork) string {
	var sb strings.Builder
	for _, e := range TotalOrder(net.AllEvents()) {
		sb.WriteString(e.String())
		sb.WriteByte('\n')
	}
	return sb.String()
}

func TestLamportClockRules(t *testing.T) {
	var lc LamportClock
	if lc.Tick() != 1 || lc.Tick() != 2 || lc.Send() != 3 {
		t.Fatal("tick and send advance by exactly one")
	}
	if got := lc.Receive(10); got != 11 {
		t.Errorf("receive jumps past a remote clock that is ahead: got %d", got)
	}
	if got := lc.Receive(2); got != 12 {
		t.Errorf("receive still advances when local clock is ahead: got %d", got)
	}
	if got := lc.Receive(12); got != 13 {
		t.Errorf("receive of equal time advances by one: got %d", got)
	}
}

func TestLamportClockMonotonic(t *testing.T) {
	var lc LamportClock
	remotes := []int64{0, 5, 2, 9, 9, 1, 40, 3}
	var prev int64
	for i := 0; i < 50; i++ {
		var now int64
		if i%3 == 0 {
			now = lc.Receive(remotes[i%len(remotes)])
		} else {
			now = lc.Tick()
		}
		if now <= prev {
			t.Fatalf("clock went from %d to %d", prev, now)
		}
		prev = now
	}
	if lc.Time() != prev {
		t.Error("Time() must report the last value handed out")
	}
}

func TestLamportTimestampTotalOrder(t *testing.T) {
	t1, t2, t3 := LamportTimestamp{3, "payments"}, LamportTimestamp{3, "refunds"}, LamportTimestamp{2, "refunds"}
	if !t3.Less(t1) || t1.Less(t3) {
		t.Error("compares by time first")
	}
	if !t1.Less(t2) || t2.Less(t1) {
		t.Error("ties break by node id")
	}
	if t1.Compare(LamportTimestamp{3, "payments"}) != 0 {
		t.Error("equal only when both match")
	}
	if t1.String() != "3@payments" {
		t.Errorf("String() = %q", t1.String())
	}
}

func TestVectorClockIncrementAndMerge(t *testing.T) {
	zero := NewVectorClock(3)
	v := zero.Increment(0)
	if !v.Equals(VectorClockOf(1, 0, 0)) || !zero.Equals(VectorClockOf(0, 0, 0)) {
		t.Error("increment touches only its own component and is immutable")
	}
	a, b := VectorClockOf(2, 0, 1), VectorClockOf(1, 3, 1)
	if !a.Merge(b).Equals(VectorClockOf(2, 3, 1)) {
		t.Error("merge is componentwise max")
	}
	if !a.Merge(b).Equals(b.Merge(a)) {
		t.Error("merge is commutative")
	}
	if !a.Merge(a).Equals(a) || !a.Merge(b).Merge(b).Equals(a.Merge(b)) {
		t.Error("merge is idempotent")
	}
	if !a.Merge(b).Merge(zero).Equals(a.Merge(b.Merge(zero))) {
		t.Error("merge is associative")
	}
	if a.Compare(a.Merge(b)) != Before || b.Compare(a.Merge(b)) != Before {
		t.Error("merge result is after both inputs")
	}
}

func TestVectorClockCompare(t *testing.T) {
	if VectorClockOf(1, 0, 0).Compare(VectorClockOf(2, 0, 0)) != Before {
		t.Error("BEFORE when every component <= and one <")
	}
	if VectorClockOf(2, 0, 0).Compare(VectorClockOf(1, 0, 0)) != After {
		t.Error("AFTER is the mirror of BEFORE")
	}
	if VectorClockOf(2, 2, 2).Compare(VectorClockOf(2, 2, 2)) != Equal {
		t.Error("EQUAL when identical")
	}
	a, b := VectorClockOf(2, 0, 1), VectorClockOf(1, 3, 1)
	if a.Compare(b) != Concurrent || !a.ConcurrentWith(b) || !b.ConcurrentWith(a) {
		t.Error("CONCURRENT when each is ahead somewhere")
	}
	if VectorClockOf(1, 1, 1).HappensBefore(VectorClockOf(1, 1, 1)) {
		t.Error("HappensBefore is strict")
	}
}

func TestVectorClockSizeMismatchPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("mismatched cluster sizes must panic")
		}
	}()
	VectorClockOf(1, 0).Compare(VectorClockOf(1, 0, 0))
}

func TestNodeSendReceive(t *testing.T) {
	payments, refunds := NewNode("payments", 0, 3), NewNode("refunds", 1, 3)
	payments.LocalEvent("a")
	m := payments.Send("refunds", "x")
	if m.LamportTime != 2 || !m.Vector.Equals(VectorClockOf(2, 0, 0)) {
		t.Error("message carries sender's clocks after the send tick")
	}
	refunds.LocalEvent("b")
	recv := refunds.Receive(m)
	if recv.Lamport.Time != 3 {
		t.Errorf("receiver lamport = max(1,2)+1, got %d", recv.Lamport.Time)
	}
	if !recv.Vector.Equals(VectorClockOf(2, 2, 0)) {
		t.Errorf("receiver vector = merge then increment own, got %s", recv.Vector)
	}
	if !m.Vector.HappensBefore(recv.Vector) {
		t.Error("send happens-before receive")
	}
	if !refunds.Log[0].Vector.ConcurrentWith(m.Vector) {
		t.Error("local event before the receive is concurrent with the send")
	}
}

func TestNetworkDeterminism(t *testing.T) {
	n1, n2 := ConcurrentRefundScenario(7), ConcurrentRefundScenario(7)
	if logOf(n1) != logOf(n2) {
		t.Error("same seed gives identical event logs")
	}
	if n1.InFlight() != 0 || n1.Delivered() != 2 || n1.Tick() < 1 {
		t.Error("all messages delivered and tick advanced")
	}
	lcg, _ := NewSimulatedNetwork(7, 3)
	if d := []int{lcg.NextDelay(), lcg.NextDelay(), lcg.NextDelay()}; d[0] != 2 || d[1] != 3 || d[2] != 1 {
		t.Errorf("LCG schedule for seed 7 must match Java and Python, got %v", d)
	}
	noDelay, _ := NewSimulatedNetwork(1, 0)
	if noDelay.NextDelay() != 0 || noDelay.NextDelay() != 0 {
		t.Error("maxDelay 0 always yields delay 0")
	}
	if _, err := NewSimulatedNetwork(1, -1); err == nil {
		t.Error("negative maxDelay rejected")
	}
	if err := noDelay.Send(Message{"payments", "mars", "p", 1, VectorClockOf(1, 0, 0)}); err == nil {
		t.Error("send to unknown node rejected")
	}
}

func TestSeedControlsArrivalOrder(t *testing.T) {
	if !strings.HasPrefix(ConcurrentRefundScenario(7).Node(Ledger).Log[0].Description, "recv captured") {
		t.Error("seed 7 delivers captured first at the ledger")
	}
	if !strings.HasPrefix(ConcurrentRefundScenario(1).Node(Ledger).Log[0].Description, "recv refunded") {
		t.Error("seed 1 delivers refunded first at the ledger")
	}
}

func TestCausalRefundScenario(t *testing.T) {
	net := CausalRefundScenario(7)
	captured, refunded := net.Node(Payments).Log[0], net.Node(Refunds).Log[1]
	if !strings.HasPrefix(refunded.Description, "post refunded") {
		t.Fatalf("unexpected event %s", refunded)
	}
	if !captured.Vector.HappensBefore(refunded.Vector) {
		t.Error("captured happens-before refunded")
	}
	if !captured.Lamport.Less(refunded.Lamport) {
		t.Error("captured lamport < refunded lamport")
	}
	if len(ConcurrentPairs(Postings(net.AllEvents()))) != 0 {
		t.Error("no concurrent postings in the causal flow")
	}
	settle := net.Node(Ledger).Log[2]
	for _, e := range net.AllEvents() {
		if !strings.HasPrefix(e.Description, "post settle") && !e.Vector.HappensBefore(settle.Vector) {
			t.Errorf("%s should happen before settle", e)
		}
	}
}

func TestConcurrentRefundScenario(t *testing.T) {
	net := ConcurrentRefundScenario(7)
	events := net.AllEvents()
	captured, refunded := net.Node(Payments).Log[0], net.Node(Refunds).Log[0]
	ledgerLog := net.Node(Ledger).Log
	settle := ledgerLog[len(ledgerLog)-1]
	if !captured.Vector.ConcurrentWith(refunded.Vector) {
		t.Error("captured and refunded are concurrent")
	}
	if captured.Lamport.Time != refunded.Lamport.Time || !captured.Lamport.Less(refunded.Lamport) {
		t.Error("lamport still orders them, captured first by tie-break")
	}
	if !captured.Vector.HappensBefore(settle.Vector) || !refunded.Vector.HappensBefore(settle.Vector) {
		t.Error("settle happens after both")
	}
	if len(Postings(events)) != 3 {
		t.Errorf("postings = %d, want 3", len(Postings(events)))
	}
	if len(ConcurrentPairs(Postings(events))) != 1 {
		t.Error("exactly one concurrent pair among postings")
	}
	for _, p := range ConcurrentPairs(events) {
		if p[0].NodeID == p[1].NodeID {
			t.Error("concurrent pairs are always between services")
		}
	}
}

func TestClockConditionAndTotalOrder(t *testing.T) {
	net := ConcurrentRefundScenario(7)
	events := net.AllEvents()
	if !clockCondition(events) {
		t.Error("happens-before implies lamport <")
	}
	order := TotalOrder(events)
	index := map[*Event]int{}
	for i, e := range order {
		index[e] = i
	}
	for _, x := range events {
		for _, y := range events {
			if x.Vector.HappensBefore(y.Vector) && index[x] > index[y] {
				t.Errorf("total order violates causality: %s after %s", x, y)
			}
		}
	}
	converseFails := false
	for i := range order {
		for j := i + 1; j < len(order); j++ {
			if order[i].Vector.ConcurrentWith(order[j].Vector) {
				converseFails = true
			}
		}
	}
	if !converseFails {
		t.Error("some lamport-ordered pair is actually concurrent")
	}
	if len(order) != len(events) || len(index) != len(events) {
		t.Error("total order keeps every event exactly once")
	}
}

func TestClockConditionAcrossSeeds(t *testing.T) {
	for seed := uint64(1); seed <= 30; seed++ {
		if !clockCondition(ConcurrentRefundScenario(seed).AllEvents()) {
			t.Errorf("seed %d: concurrent scenario breaks the clock condition", seed)
		}
		if !clockCondition(CausalRefundScenario(seed).AllEvents()) {
			t.Errorf("seed %d: causal scenario breaks the clock condition", seed)
		}
	}
}

func TestMoneyIsIntegerMinorUnits(t *testing.T) {
	if OfMinor(4_999, USD).String() != "49.99 USD" || OfMinor(-4_999, USD).String() != "-49.99 USD" {
		t.Error("money formatting")
	}
	net, err := Capture.Plus(Refund)
	if err != nil || net.MinorUnits != 0 {
		t.Error("capture plus refund nets to zero")
	}
	if _, err := Capture.Plus(OfMinor(1, Currency{"EUR", 2})); err == nil {
		t.Error("currency mismatch is an error")
	}
}
