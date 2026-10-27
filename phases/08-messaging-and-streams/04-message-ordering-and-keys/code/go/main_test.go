// Tests for phases/08-messaging-and-streams/04-message-ordering-and-keys. Run: go test ./...
package main

import (
	"errors"
	"math"
	"reflect"
	"strconv"
	"testing"
)

func check(t *testing.T, name string, condition bool) {
	t.Helper()
	if !condition {
		t.Errorf("%s", name)
	}
}

func checkPanics(t *testing.T, name string, action func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Errorf("%s (no panic)", name)
		}
	}()
	action()
}

func deliver(t *testing.T, messages []Message, maxInFlight int, faults *FaultInjector, guard bool) SendOutcome {
	t.Helper()
	out, err := Deliver(messages, maxInFlight, faults, guard)
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	return out
}

func TestPartitioner(t *testing.T) {
	check(t, "fnv1a32('a') known vector", Fnv1a32([]byte("a")) == 0xE40C292C)
	check(t, "same key, same partition", PartitionFor("acct-1001", 3) == PartitionFor("acct-1001", 3))
	inRange := true
	for i := 0; i < 1000; i++ {
		p := PartitionFor("k"+strconv.Itoa(i), 7)
		if p < 0 || p >= 7 {
			inRange = false
		}
	}
	check(t, "partition index always in [0, N)", inRange)
	someMoved := false
	for i := 0; i < 100 && !someMoved; i++ {
		someMoved = PartitionFor("k"+strconv.Itoa(i), 3) != PartitionFor("k"+strconv.Itoa(i), 4)
	}
	check(t, "changing partition count moves keys", someMoved)
	checkPanics(t, "partitionCount must be positive", func() { PartitionFor("x", 0) })
}

func TestTopicKeepsKeysHomeAndOrdered(t *testing.T) {
	topic := NewTopic("t", 4)
	keys := []string{"a", "b", "c", "d", "e", "f", "g"}
	for _, m := range BuildWorkload(keys, 70, 1, 1, "none", 1) {
		topic.Append(m)
	}
	oneHome, orderedPerKey := true, true
	for _, k := range keys {
		home := -1
		for p := 0; p < 4; p++ {
			s := topic.Partition(p).SequenceOf(k)
			if len(s) == 0 {
				continue
			}
			if home != -1 {
				oneHome = false
			}
			home = p
			for i, seq := range s {
				if seq != int64(i) {
					orderedPerKey = false
				}
			}
		}
	}
	check(t, "each key lives in exactly one partition", oneHome)
	check(t, "per-key seqs are 0,1,2,... in offset order", orderedPerKey)
	var total int64
	for p := 0; p < 4; p++ {
		total += topic.Partition(p).LogEndOffset()
	}
	check(t, "offsets are dense: partition sizes sum to the message count", total == 70)
}

func holPartition() (*Partition, []string) {
	hol := NewPartition(0)
	holKeys := []string{"fast-1", "fast-2", "fast-3", "slow", "fast-4", "fast-5"}
	for _, m := range BuildWorkload(holKeys, 60, 10, 2, "slow", 40) {
		hol.Append(m)
	}
	return hol, holKeys
}

func TestHeadOfLineBlockingSequential(t *testing.T) {
	hol, _ := holPartition()
	sequential := SequentialScheduler{}.Run(hol)
	check(t, "sequential: offset 4 (fast) starts only after offset 3 (slow) completes", sequential[4].StartedAt == sequential[3].CompletedAt)
	check(t, "sequential: a fast record waits at least the slow cost minus its arrival gap", sequential[4].LatencyMillis >= 40-10+2)
	check(t, "sequential: completions are in offset order", sequential[10].CompletedAt <= sequential[11].StartedAt)
	check(t, "sequential: per-key order holds trivially", PerKeyOrderHolds(hol, sequential))
	seqFast := StatsExcluding(sequential, "slow")
	check(t, "sequential: fast accounts' max latency is inflated by the slow key", seqFast.Max >= 30)
}

func TestKeyedLanes(t *testing.T) {
	hol, holKeys := holPartition()
	eight, err := NewKeyedLaneScheduler(8)
	if err != nil {
		t.Fatal(err)
	}
	laned := eight.Run(hol)
	check(t, "lanes: per-key order holds", PerKeyOrderHolds(hol, laned))
	lanedFast := StatsExcluding(laned, "slow")
	slowAlone := true
	for _, k := range holKeys {
		if k != "slow" && eight.LaneFor(k) == eight.LaneFor("slow") {
			slowAlone = false
		}
	}
	check(t, "lanes: with the slow key alone in its lane, fast accounts see only their own cost", !slowAlone || lanedFast.Max == 2)
	check(t, "lanes: the slow key still pays its own cost", StatsOf(onlyKey(laned, "slow")).Max >= 40)
	check(t, "lanes: everything was eventually committable", eight.FinalCommittable() == 60)
	check(t, "lanes: some offsets finished before an earlier one (commit must lag)", eight.MaxCompletedAhead() > 0)
	check(t, "lanes: the same key always maps to the same lane", eight.LaneFor("slow") == eight.LaneFor("slow"))
	_, err = NewKeyedLaneScheduler(0)
	check(t, "lanes must be positive", err != nil)
}

func TestCommitTrackerLowWaterMark(t *testing.T) {
	tracker := NewCommitTracker(0)
	mark, err := tracker.Complete(1)
	check(t, "completing offset 1 first commits nothing", err == nil && mark == 0 && tracker.CompletedAhead() == 1)
	mark, err = tracker.Complete(0)
	check(t, "completing offset 0 then releases 0 and 1", err == nil && mark == 2 && tracker.CompletedAhead() == 0)
	if _, err := tracker.Complete(3); err != nil {
		t.Fatal(err)
	}
	check(t, "gap at 2 holds the mark at 2", tracker.Committable() == 2)
	mark, err = tracker.Complete(2)
	check(t, "filling the gap advances past everything finished", err == nil && mark == 4)
	_, err = tracker.Complete(3)
	check(t, "completing an offset twice is a bug", errors.Is(err, ErrAlreadyCommitted) || errors.Is(err, ErrCompletedTwice))
	_, err = tracker.Complete(0)
	check(t, "completing an already-committed offset is a bug", errors.Is(err, ErrAlreadyCommitted))
	fresh := NewCommitTracker(0)
	fresh.Complete(5)
	_, err = fresh.Complete(5)
	check(t, "completing an offset ahead of the mark twice is a bug", errors.Is(err, ErrCompletedTwice))
}

func TestLatencyStats(t *testing.T) {
	fixed := []Completion{{0, "k", 0, 10, 10}, {1, "k", 0, 30, 30}, {2, "k", 0, 20, 20}, {3, "k", 0, 40, 40}}
	stats := StatsOf(fixed)
	check(t, "stats: max, mean, p50 over 10,20,30,40", stats.Max == 40 && stats.Mean == 25 && stats.P50 == 20 && stats.Count == 4)
	check(t, "stats: empty list is all zeros", StatsOf(nil).Count == 0)
}

func TestRetryReorderingAndSequenceNumbers(t *testing.T) {
	sends := BuildWorkload([]string{"acct"}, 8, 1, 1, "none", 1)
	faults := NewFaultInjector(2, 5)
	one := deliver(t, sends, 1, faults, false)
	check(t, "maxInFlight=1: retries keep order", reflect.DeepEqual(seqsOf(one.Appended), []int64{0, 1, 2, 3, 4, 5, 6, 7}) && one.Retries == 2)
	five := deliver(t, sends, 5, faults, false)
	check(t, "maxInFlight=5: a retried send lands after its successors", reflect.DeepEqual(seqsOf(five.Appended), []int64{0, 1, 3, 4, 2, 6, 7, 5}))
	check(t, "nothing is lost or duplicated by the retry itself", len(five.Appended) == 8)
	check(t, "SequenceTracker flags the reordered log", !IsOrdered(five.Appended) && IsOrdered(one.Appended))
	st := NewSequenceTracker()
	for _, m := range five.Appended {
		st.Observe(m)
	}
	check(t, "tracker counts 2 gaps and 2 late arrivals classified as duplicates", st.Gaps() == 2 && st.Duplicates() == 2)
	guarded := deliver(t, sends, 5, faults, true)
	check(t, "broker sequence guard restores order with the same faults", IsOrdered(guarded.Appended) && len(guarded.Appended) == 8)
	check(t, "the guard works by rejecting out-of-sequence sends", guarded.Rejected == 3 && guarded.Retries == 2)
	noFaults := deliver(t, sends, 5, NewFaultInjector(), false)
	check(t, "no faults: no reordering even with 5 in flight", IsOrdered(noFaults.Appended) && noFaults.Retries == 0)
	seeded := RandomFaults(42, 2000, 0.05)
	many := BuildWorkload([]string{"acct"}, 2000, 1, 1, "none", 1)
	unguarded := deliver(t, many, 5, seeded, false)
	guardedMany := deliver(t, many, 5, seeded, true)
	check(t, "seeded run: unguarded is reordered, guarded is not, both complete", !IsOrdered(unguarded.Appended) &&
		IsOrdered(guardedMany.Appended) && len(unguarded.Appended) == 2000 && len(guardedMany.Appended) == 2000)
	check(t, "seeded fault injector is deterministic", RandomFaults(42, 2000, 0.05).FailsFirstAttempt(7) == seeded.FailsFirstAttempt(7))
	_, err := Deliver(sends, 0, faults, false)
	check(t, "maxInFlight must be positive", err != nil)
}

func TestJavaRandomMatchesTheJDK(t *testing.T) {
	// new java.util.Random(42).nextDouble() == 0.7275636800328681 (first value), 0.6832234717598454 (second)
	r := NewJavaRandom(42)
	first, second := r.NextDouble(), r.NextDouble()
	check(t, "first nextDouble of seed 42", math.Abs(first-0.7275636800328681) < 1e-15)
	check(t, "second nextDouble of seed 42", math.Abs(second-0.6832234717598454) < 1e-15)
}

func TestMessageConstructionAndMoney(t *testing.T) {
	_, err := NewMessage("k", -1, "AUTH", OfMinor(1, USD), 0, 1)
	check(t, "negative seq rejected", err != nil)
	_, err = NewMessage("k", 0, "AUTH", OfMinor(1, USD), 0, -1)
	check(t, "negative cost rejected", err != nil)
	check(t, "money prints in major units", OfMinor(1999, USD).String() == "19.99 USD")
}
