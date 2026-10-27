// Tests for phases/06-replication-and-consensus/03-quorums. Run: go test ./...
package main

import (
	"errors"
	"fmt"
	"testing"
)

func check(t *testing.T, name string, condition bool) {
	t.Helper()
	if !condition {
		t.Errorf("FAIL: %s", name)
	}
}

var strict222 = MustConfig(3, 2, 2, false, false)

const key = "merchant:m-77:settlement-currency"

func contains(xs []int, x int) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func equalInts(a, b []int) bool {
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

func value(t *testing.T, r ReadResult, err error) (string, bool) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return r.Value()
}

func TestJavaStringHash(t *testing.T) {
	cases := []struct {
		s    string
		want int32
	}{{"", 0}, {"hello", 99162322}, {"polygenelubricants", -2147483648}}
	for _, c := range cases {
		if got := JavaStringHash(c.s); got != c.want {
			t.Errorf("JavaStringHash(%q) = %d, want %d", c.s, got, c.want)
		}
	}
}

func TestConfigValidation(t *testing.T) {
	check(t, "R + W > N overlaps", strict222.Overlaps())
	check(t, "R + W = N does not overlap", !MustConfig(3, 1, 2, false, false).Overlaps())
	_, err := NewQuorumConfig(3, 4, 1, false, false)
	check(t, "R > N is rejected", errors.Is(err, ErrBadConfig))
	_, err = NewQuorumConfig(3, 1, 0, false, false)
	check(t, "W = 0 is rejected", errors.Is(err, ErrBadConfig))
	_, err = NewQuorumStore(2, strict222, 1, 0.0)
	check(t, "fewer nodes than N is rejected", errors.Is(err, ErrBadConfig))
}

func TestVersionedLWW(t *testing.T) {
	check(t, "later timestamp is newer", Versioned{"b", 2, 0}.NewerThan(Versioned{"a", 1, 9}))
	check(t, "equal timestamp: higher writer id wins", Versioned{"b", 2, 3}.NewerThan(Versioned{"a", 2, 1}))
	check(t, "a version is not newer than itself", !Versioned{"a", 2, 1}.NewerThan(Versioned{"a", 2, 1}))
	check(t, "anything is newer than NONE", Versioned{"a", 0, 0}.NewerThan(None))
}

func TestPlacementAndStrictQuorum(t *testing.T) {
	s := MustStore(5, strict222, 1, 0.0)
	check(t, "home set has N nodes", len(s.HomeNodes(key)) == 3)
	check(t, "fallback ring holds the other nodes", len(s.FallbackNodes(key)) == 2 && !contains(s.FallbackNodes(key), s.HomeNodes(key)[0]))
	check(t, "placement is a pure function of the key", equalInts(s.HomeNodes(key), MustStore(5, strict222, 99, 0.5).HomeNodes(key)))

	home := s.HomeNodes(key)
	acks, err := s.Put(home[0], key, "EUR")
	check(t, "write with all replicas up gets N acks", err == nil && acks == 3)
	r, err := s.Get(home[2], key)
	v, ok := value(t, r, err)
	check(t, "read returns the written value", ok && v == "EUR" && r.Responses == 2)
	check(t, "coordinator consults its own copy first", r.Respondents[0] == home[2])
	check(t, "replicasHolding counts the home set", s.ReplicasHolding(key, "EUR") == 3)

	s.Network.Crash(home[1])
	_, _ = s.Put(home[0], key, "GBP")
	s.Network.Recover(home[1])
	s.Network.Crash(home[0])
	overlapping, err := s.Get(home[1], key)
	v, ok = value(t, overlapping, err)
	check(t, "strict R=2 read overlaps the W=2 write set and sees the new value", ok && v == "GBP")
	check(t, "the stale replica was read but outvoted by the newer version", overlapping.Responses == 2)

	s.Network.Crash(home[2]) // now two of three home nodes are down
	_, err = s.Put(home[1], key, "CHF")
	check(t, "write with fewer than W reachable replicas fails", errors.Is(err, ErrQuorumNotReached))
	_, err = s.Get(home[1], key)
	check(t, "read with fewer than R reachable replicas fails", errors.Is(err, ErrQuorumNotReached))
	check(t, "the failed write still landed on the reachable replica (no rollback)", s.Nodes[home[1]].Data[key].Value == "CHF")
}

func TestWeakQuorumsMiss(t *testing.T) {
	weak := MustStore(5, MustConfig(3, 1, 1, false, false), 1, 0.0)
	home := weak.HomeNodes(key)
	_, _ = weak.Put(home[0], key, "EUR")
	weak.Network.Crash(home[2])
	_, _ = weak.Put(home[1], key, "GBP")
	weak.Network.Recover(home[2])
	r, err := weak.Get(home[2], key)
	v, ok := value(t, r, err)
	check(t, "R=1 read from the briefly-unreachable node returns the old value", ok && v == "EUR")
	r, err = weak.Get(home[0], key)
	v, ok = value(t, r, err)
	check(t, "R=1 read from a node that saw the write returns the new value", ok && v == "GBP")
}

func TestSloppyQuorums(t *testing.T) {
	sloppy := MustStore(5, MustConfig(3, 2, 2, true, false), 1, 0.0)
	home := sloppy.HomeNodes(key)
	sloppy.Network.Crash(home[1])
	sloppy.Network.Crash(home[2])
	acks, err := sloppy.Put(home[0], key, "EUR")
	check(t, "sloppy write succeeds with two home nodes down by spilling onto the ring", err == nil && acks >= 2 && sloppy.HintedWrites == 2)
	check(t, "the spill nodes hold hints for the absent home nodes", sloppy.Nodes[sloppy.FallbackNodes(key)[0]].HintCount() == 1)
	check(t, "only one home replica actually holds the value", sloppy.ReplicasHolding(key, "EUR") == 1)
	strictSame := MustStore(5, strict222, 1, 0.0)
	strictSame.Network.Crash(home[1])
	strictSame.Network.Crash(home[2])
	_, err = strictSame.Put(home[0], key, "EUR")
	check(t, "the same write fails under a strict quorum", errors.Is(err, ErrQuorumNotReached))
	sloppy.Network.Recover(home[1])
	sloppy.Network.Recover(home[2])
	sloppy.Network.Crash(home[0])
	stale, err := sloppy.Get(home[1], key)
	_, ok := value(t, stale, err)
	check(t, "sloppy read with R=2 misses the value before handoff: R + W > N is not enough", !ok)
	delivered := sloppy.Handoff()
	check(t, "hinted handoff delivers one value per absent home node", delivered == 2)
	r, err := sloppy.Get(home[1], key)
	v, ok := value(t, r, err)
	check(t, "after handoff the same read sees the value", ok && v == "EUR")
	check(t, "hints are cleared once delivered", sloppy.Nodes[sloppy.FallbackNodes(key)[0]].HintCount() == 0)
	check(t, "handoff is idempotent", sloppy.Handoff() == 0)
}

func TestReadRepair(t *testing.T) {
	rr := MustStore(5, MustConfig(3, 2, 2, false, true), 1, 0.0)
	home := rr.HomeNodes(key)
	rr.Network.Crash(home[2])
	_, _ = rr.Put(home[0], key, "EUR")
	rr.Network.Recover(home[2])
	check(t, "replica that was down is stale before any read", rr.ReplicasHolding(key, "EUR") == 2)
	rr.Network.Crash(home[0])
	repaired, err := rr.Get(home[1], key)
	check(t, "read repair fixes the stale responder", err == nil && repaired.Repaired == 1 && rr.ReplicasHolding(key, "EUR") == 3)
	second, err := rr.Get(home[1], key)
	check(t, "a second read repairs nothing", err == nil && second.Repaired == 0)
	check(t, "read repair never writes an older version over a newer one", !rr.Nodes[home[1]].Put(key, Versioned{"OLD", 0, 0}))
}

func TestLWWLosesConcurrentUpdate(t *testing.T) {
	lww := MustStore(5, strict222, 1, 0.0)
	bal := "wallet:w-9:balance"
	_, _ = lww.Put(0, bal, "100.00 USD")
	_, _ = lww.Put(1, bal, "70.00 USD")
	_, _ = lww.Put(3, bal, "150.00 USD")
	r, err := lww.Get(2, bal)
	v, ok := value(t, r, err)
	check(t, "both concurrent writes succeeded and the later timestamp won", ok && v == "150.00 USD")
	check(t, "the debit is gone from every replica", lww.ReplicasHolding(bal, "70.00 USD") == 0)
}

func TestAntiEntropy(t *testing.T) {
	ae := MustStore(5, MustConfig(3, 2, 2, true, false), 1, 0.0)
	ae.Network.Crash(2)
	for i := 0; i < 8; i++ {
		coord := i % 5
		if coord == 2 {
			coord = 0
		}
		_, _ = ae.Put(coord, fmt.Sprintf("cfg:%d", i), fmt.Sprintf("value%d", i))
	}
	ae.Network.Recover(2)
	disagreeBefore := 0
	for i := 0; i < 8; i++ {
		if !ae.ReplicasAgree(fmt.Sprintf("cfg:%d", i)) {
			disagreeBefore++
		}
	}
	ae.AntiEntropy()
	disagreeAfter := 0
	for i := 0; i < 8; i++ {
		if !ae.ReplicasAgree(fmt.Sprintf("cfg:%d", i)) {
			disagreeAfter++
		}
	}
	check(t, "some keys disagree after a node missed writes", disagreeBefore > 0)
	check(t, "anti-entropy makes every key's replicas agree", disagreeAfter == 0)
	check(t, "anti-entropy is idempotent", ae.AntiEntropy() == 0)
}

func TestPropertyAcrossSeeds(t *testing.T) {
	strictStale, weakStale, sloppyStale := 0, 0, 0
	for seed := int64(1); seed <= 12; seed++ {
		strictStale += RunExperiment(strict222, 5, seed, 0.05, 300, 20).StaleReads
		strictStale += RunExperiment(MustConfig(3, 3, 1, false, false), 5, seed, 0.05, 300, 20).StaleReads
		weakStale += RunExperiment(MustConfig(3, 1, 1, false, false), 5, seed, 0.05, 300, 20).StaleReads
		sloppyStale += RunExperiment(MustConfig(3, 2, 2, true, false), 5, seed, 0.05, 300, 20).StaleReads
	}
	check(t, "strict R + W > N: zero stale reads across 12 seeds with drops and crashes", strictStale == 0)
	check(t, "R + W <= N: stale reads occur", weakStale > 0)
	check(t, "sloppy R + W > N: stale reads still occur", sloppyStale > 0)
}

func TestDeterminism(t *testing.T) {
	a := RunExperiment(MustConfig(3, 2, 2, true, true), 5, 42, 0.1, 200, 15)
	b := RunExperiment(MustConfig(3, 2, 2, true, true), 5, 42, 0.1, 200, 15)
	check(t, "same seed, same outcome", a == b)
}

func TestPartitions(t *testing.T) {
	part := MustStore(5, strict222, 1, 0.0)
	home := part.HomeNodes(key)
	part.Network.Partition(home[0])
	_, err := part.Put(home[0], key, "EUR")
	check(t, "a coordinator alone on its side cannot reach W", errors.Is(err, ErrQuorumNotReached))
	acks, err := part.Put(home[1], key, "EUR")
	check(t, "the majority side still writes", err == nil && acks == 2)
	part.Network.Heal()
	r, err := part.Get(home[0], key)
	check(t, "after heal the isolated coordinator reaches a read quorum again", err == nil && r.Responses == 2)
}
