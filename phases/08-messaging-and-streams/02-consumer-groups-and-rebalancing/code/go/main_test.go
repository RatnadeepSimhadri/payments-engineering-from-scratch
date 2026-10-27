// Tests for phases/08-messaging-and-streams/02-consumer-groups-and-rebalancing. Run: go test ./...
package main

import (
	"errors"
	"reflect"
	"testing"
)

func check(t *testing.T, name string, condition bool) {
	t.Helper()
	if !condition {
		t.Errorf("%s", name)
	}
}

func checkIs(t *testing.T, name string, err, target error) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Errorf("%s: got %v, want %v", name, err, target)
	}
}

func eq(a, b []int) bool { return reflect.DeepEqual(a, b) }

func containsAll(have, want []int) bool {
	set := map[int]bool{}
	for _, p := range have {
		set[p] = true
	}
	for _, p := range want {
		if !set[p] {
			return false
		}
	}
	return true
}

var (
	three = []string{"c1", "c2", "c3"}
	four  = []string{"c1", "c2", "c3", "c4"}
	none  = map[string][]int{}
)

func TestRangeAssignor(t *testing.T) {
	range3 := RangeAssignor{}.Assign(three, 8, none)
	check(t, "range 8/3 c1", eq(range3["c1"], []int{0, 1, 2}))
	check(t, "range 8/3 c2", eq(range3["c2"], []int{3, 4, 5}))
	check(t, "range 8/3 c3", eq(range3["c3"], []int{6, 7}))
	check(t, "range covers every partition exactly once", len(Owners(range3)) == 8)
	check(t, "range more members than partitions leaves some idle", len(RangeAssignor{}.Assign(three, 2, none)["c3"]) == 0)
	check(t, "range with no members is empty", len(RangeAssignor{}.Assign(nil, 8, none)) == 0)
}

func TestRoundRobinAssignor(t *testing.T) {
	rr3 := RoundRobinAssignor{}.Assign(three, 8, none)
	check(t, "round-robin c1", eq(rr3["c1"], []int{0, 3, 6}))
	check(t, "round-robin c3", eq(rr3["c3"], []int{2, 5}))
}

func TestStickyAssignor(t *testing.T) {
	sticky := StickyAssignor{}
	range3 := RangeAssignor{}.Assign(three, 8, none)
	rr3 := RoundRobinAssignor{}.Assign(three, 8, none)
	s3 := sticky.Assign(three, 8, none)
	check(t, "sticky first generation is balanced", len(s3["c1"]) == 3 && len(s3["c2"]) == 3 && len(s3["c3"]) == 2)
	s4 := sticky.Assign(four, 8, s3)
	check(t, "sticky join moves exactly two partitions", Moved(s3, s4) == 2)
	balanced := true
	for _, l := range s4 {
		if len(l) != 2 {
			balanced = false
		}
	}
	check(t, "sticky join stays balanced", balanced)
	check(t, "sticky covers every partition exactly once", len(Owners(s4)) == 8)
	r4 := RangeAssignor{}.Assign(four, 8, range3)
	check(t, "range join moves five partitions", Moved(range3, r4) == 5)
	rr4 := RoundRobinAssignor{}.Assign(four, 8, rr3)
	check(t, "round-robin join moves more than sticky", Moved(rr3, rr4) > Moved(s3, s4))
	sLeave := sticky.Assign([]string{"c1", "c3"}, 8, s3)
	check(t, "sticky leave: survivors keep everything they had", containsAll(sLeave["c1"], s3["c1"]) && containsAll(sLeave["c3"], s3["c3"]))
	check(t, "sticky leave: orphans are redistributed and balanced", len(sLeave["c1"]) == 4 && len(sLeave["c3"]) == 4)
	check(t, "assignors are deterministic", reflect.DeepEqual(sticky.Assign(four, 8, s3), s4) && reflect.DeepEqual(RangeAssignor{}.Assign(three, 8, none), range3))
}

func newGroup(t *testing.T) (*GroupCoordinator, *ManualClock) {
	t.Helper()
	clock := NewManualClock(0)
	g, err := NewGroupCoordinator("ledger-poster", 8, RangeAssignor{}, 10_000, clock)
	if err != nil {
		t.Fatal(err)
	}
	return g, clock
}

func owner(t *testing.T, g *GroupCoordinator, p int) string {
	t.Helper()
	o, err := g.OwnerOf(p)
	if err != nil {
		t.Fatalf("OwnerOf(%d): %v", p, err)
	}
	return o
}

func TestCoordinatorGenerationsHeartbeatsAndSessionTimeout(t *testing.T) {
	g, clock := newGroup(t)
	a1 := g.Join("c1")
	check(t, "first join is generation 1 and owns everything", a1.Generation == 1 && len(a1.Partitions) == 8)
	g.Join("c2")
	g.Join("c3")
	check(t, "each new member bumps the generation", g.Generation() == 3)
	check(t, "stale generation heartbeat demands a rebalance", g.Heartbeat("c1", 1) == RebalanceInProgress)
	check(t, "unknown member heartbeat", g.Heartbeat("ghost", 3) == UnknownMember)
	a1b := g.Join("c1")
	check(t, "re-join does not bump the generation", a1b.Generation == 3 && g.Generation() == 3)
	check(t, "current generation heartbeat is OK", g.Heartbeat("c1", 3) == OK)
	check(t, "partition 3 owned by c2 before failure", owner(t, g, 3) == "c2")

	clock.Advance(9_999)
	g.Heartbeat("c1", 3)
	g.Heartbeat("c3", 3)
	check(t, "nobody expires before the session timeout", len(g.ExpireSessions()) == 0 && g.Generation() == 3)
	clock.Advance(1)
	dead := g.ExpireSessions()
	check(t, "c2 expires exactly at the session timeout", reflect.DeepEqual(dead, []string{"c2"}))
	check(t, "expiry bumps the generation", g.Generation() == 4)
	check(t, "partition 3 has a new owner and it is deterministic", owner(t, g, 3) == "c1")
	check(t, "every partition still has exactly one owner", len(Owners(g.Assignment())) == 8)

	t.Run("commit fencing", func(t *testing.T) {
		checkIs(t, "evicted zombie cannot commit", g.CommitOffset("c2", 3, 3, 100), ErrUnknownMember)
		checkIs(t, "stale generation cannot commit", g.CommitOffset("c1", 3, 3, 100), ErrIllegalGeneration)
		checkIs(t, "non-owner cannot commit", g.CommitOffset("c3", 4, 0, 100), ErrNotOwner)
		if err := g.CommitOffset("c1", 4, 3, 1_040); err != nil {
			t.Fatal(err)
		}
		check(t, "owner in current generation commits", g.CommittedOffset(3) == 1_040)
		check(t, "uncommitted partition reads as 0", g.CommittedOffset(7) == 0)
		check(t, "negative offset rejected", g.CommitOffset("c1", 4, 3, -1) != nil)
	})

	t.Run("leave is immediate", func(t *testing.T) {
		if err := g.Leave("c3"); err != nil {
			t.Fatal(err)
		}
		check(t, "leave rebalances immediately", g.Generation() == 5 && reflect.DeepEqual(g.Members(), []string{"c1"}) && owner(t, g, 7) == "c1")
		checkIs(t, "leaving twice is an error", g.Leave("c3"), ErrUnknownMember)
		_, err := g.OwnerOf(8)
		check(t, "ownerOf rejects bad partition", err != nil)
		check(t, "history records every generation", len(g.History()) == 5)
	})
}

func TestCoordinatorValidation(t *testing.T) {
	clock := NewManualClock(0)
	if _, err := NewGroupCoordinator("g", 0, RangeAssignor{}, 10, clock); err == nil {
		t.Error("partitionCount must be positive")
	}
	if _, err := NewGroupCoordinator("g", 1, RangeAssignor{}, 0, clock); err == nil {
		t.Error("sessionTimeoutMs must be positive")
	}
}
