// Tests for phases/06-replication-and-consensus/02-replication-lag-and-read-your-writes. Run: go test ./...
package main

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func check(t *testing.T, name string, condition bool) {
	t.Helper()
	if !condition {
		t.Errorf("FAIL: %s", name)
	}
}

func TestLeaderLogAndLsns(t *testing.T) {
	leader := NewLeader()
	check(t, "fresh leader has lsn 0", leader.Lsn() == 0)
	check(t, "first write is lsn 1", leader.Write("a", "1") == 1)
	check(t, "second write is lsn 2", leader.Write("b", "2") == 2)
	v, ok := leader.Read("b")
	check(t, "leader reads its own write", ok && v == "2")
	since1, err := leader.Since(1)
	check(t, "since(1) returns exactly the records after lsn 1", err == nil && len(since1) == 1 && since1[0].Lsn == 2)
	since2, err := leader.Since(2)
	check(t, "since(lsn) returns nothing when caught up", err == nil && len(since2) == 0)
	_, err = leader.Since(3)
	check(t, "since rejects an lsn beyond the log", errors.Is(err, ErrLsnOutOfRange))
}

func TestReplicasApplyWithDelay(t *testing.T) {
	store := Of(1, true, 3, 10)
	lsn := store.Write("payment:p1", "CAPTURED")
	check(t, "write returns lsn 1", lsn == 1)
	check(t, "replica has not applied before any tick", store.Replicas[0].AppliedLsn == 0 && store.Replicas[0].Pending() == 1)
	store.Run(10)
	check(t, "every replica applies within its max delay", store.Replicas[0].AppliedLsn == 1 && store.Replicas[1].AppliedLsn == 1)
	check(t, "lag is zero once applied", store.Replicas[1].LagBehind(store.Leader) == 0)
	v, ok := store.Replicas[1].Read("payment:p1")
	check(t, "replica serves the value after applying", ok && v == "CAPTURED")
}

func TestStaleReadAndReadYourWrites(t *testing.T) {
	lagging := Of(2, true, 20)
	alice := NewSession(1)
	lagging.WriteSession(alice, "payment:p2", "CAPTURED")
	lagging.Tick()
	stale := lagging.Read(alice, "payment:p2", AnyReplica)
	check(t, "ANY_REPLICA right after a write misses the write (lag > time since write)", !stale.Found && stale.AtLsn == 0)
	check(t, "the store counted that read as stale", lagging.StaleReads == 1)

	ryw := lagging.Read(alice, "payment:p2", ReadYourWrites)
	check(t, "READ_YOUR_WRITES sees the own write", ryw.Found && ryw.Value == "CAPTURED")
	check(t, "...served by the leader when no replica has caught up", ryw.ServedBy == "leader" && ryw.Redirected)
	check(t, "session floor for READ_YOUR_WRITES is the last write lsn", alice.Floor(ReadYourWrites) == 1)
	bob := NewSession(2)
	bobRead := lagging.Read(bob, "payment:p2", ReadYourWrites)
	check(t, "a session with no writes has floor 0 and may read stale (not its write to see)", bob.Floor(ReadYourWrites) == 0 && !bobRead.Found)
	lagging.Run(25)
	later := lagging.Read(alice, "payment:p2", ReadYourWrites)
	check(t, "once a replica catches up, READ_YOUR_WRITES is served by a replica, not the leader", later.ServedBy == "r0" && !later.Redirected)
}

func TestGuaranteeAcrossSeeds(t *testing.T) {
	rywStale, anyStale, sessionBackwards, anyBackwards := 0, 0, 0, 0
	for seed := int64(1); seed <= 15; seed++ {
		for _, mode := range []ReadMode{AnyReplica, ReadYourWrites, SessionMode} {
			s := Of(seed, true, 5, 15, 45)
			user := NewSession(seed)
			det := &AnomalyDetector{}
			for i := 1; i <= 100; i++ {
				s.WriteSession(user, "k", fmt.Sprintf("v%d", i))
				s.Run(1 + (i % 5))
				r := s.Read(user, "k", mode)
				det.Observe(r)
				missed := !r.Found || r.Value != fmt.Sprintf("v%d", i)
				if mode == AnyReplica && missed {
					anyStale++
				}
				if mode == ReadYourWrites && missed {
					rywStale++
				}
			}
			if mode == AnyReplica {
				anyBackwards += det.MonotonicViolations
			}
			if mode == SessionMode {
				sessionBackwards += det.MonotonicViolations
			}
		}
	}
	check(t, "ANY_REPLICA misses own writes in the seeded runs (the bug is reproducible)", anyStale > 0)
	check(t, "READ_YOUR_WRITES never misses an own write across 15 seeds x 100 writes", rywStale == 0)
	check(t, "ANY_REPLICA reads travel back in time in the seeded runs", anyBackwards > 0)
	check(t, "SESSION reads never go backwards across 15 seeds", sessionBackwards == 0)
}

func TestMonotonicReads(t *testing.T) {
	mono := Of(5, true, 2, 30)
	carol := NewSession(1) // sticky replica r1 is the slow one
	mono.WriteSession(carol, "k", "v1")
	mono.Run(3)
	first := mono.Read(carol, "k", Monotonic)
	mono.Write("k", "v2") // not carol's write: her write floor stays at 1
	mono.Run(3)
	second := mono.Read(carol, "k", Monotonic)
	check(t, "MONOTONIC floor is the last read lsn", carol.Floor(Monotonic) >= first.AtLsn)
	check(t, "MONOTONIC never serves an lsn below the previous read", second.AtLsn >= first.AtLsn)
	check(t, "SESSION floor is the max of write and read floors", carol.Floor(SessionMode) == max64(carol.LastWriteLsn, carol.LastReadLsn))
}

func TestConsistentPrefix(t *testing.T) {
	ordered := Of(11, true, 20)
	unordered := Of(11, false, 20)
	orderedDet, unorderedDet := &AnomalyDetector{}, &AnomalyDetector{}
	for p := 1; p <= 100; p++ {
		for _, s := range []*ReplicatedStore{ordered, unordered} {
			s.Write(fmt.Sprintf("p%d:auth", p), "AUTHORIZED")
			s.Write(fmt.Sprintf("p%d:capture", p), "CAPTURED")
		}
		for tk := 0; tk < 8; tk++ {
			ordered.Tick()
			unordered.Tick()
			_, oa := ordered.Replicas[0].Read(fmt.Sprintf("p%d:auth", p))
			_, oc := ordered.Replicas[0].Read(fmt.Sprintf("p%d:capture", p))
			orderedDet.ObservePair(oa, oc)
			_, ua := unordered.Replicas[0].Read(fmt.Sprintf("p%d:auth", p))
			_, uc := unordered.Replicas[0].Read(fmt.Sprintf("p%d:capture", p))
			unorderedDet.ObservePair(ua, uc)
		}
	}
	check(t, "on-arrival replica shows 'captured' before 'authorized' (consistent prefix violated)", unorderedDet.PrefixViolations > 0 && unordered.Replicas[0].OutOfOrderApplies > 0)
	check(t, "in-order replica never shows 'captured' before 'authorized'", orderedDet.PrefixViolations == 0 && ordered.Replicas[0].OutOfOrderApplies == 0)
	ordered.Run(40)
	unordered.Run(40)
	check(t, "both replicas converge to the same watermark eventually", ordered.Replicas[0].AppliedLsn == 200 && unordered.Replicas[0].AppliedLsn == 200)
	check(t, "in-order replica's buffer drains to zero", ordered.Replicas[0].Buffered() == 0)
}

func TestCatchUpCursor(t *testing.T) {
	cu := Of(4, true, 2)
	for i := 1; i <= 5; i++ {
		cu.Write(fmt.Sprintf("k%d", i), "v")
	}
	cu.Run(5)
	check(t, "catch-up ships nothing to a replica that is current", cu.CatchUp(cu.Replicas[0]) == 0)
}

func TestDeterminism(t *testing.T) {
	d1, d2 := Of(21, true, 5, 15), Of(21, true, 5, 15)
	var t1, t2 strings.Builder
	for i := 0; i < 30; i++ {
		d1.Write("k", fmt.Sprintf("v%d", i))
		d2.Write("k", fmt.Sprintf("v%d", i))
		d1.Run(2)
		d2.Run(2)
		t1.WriteString(d1.Read(NewSession(int64(i)), "k", AnyReplica).String())
		t2.WriteString(d2.Read(NewSession(int64(i)), "k", AnyReplica).String())
	}
	check(t, "same seed produces the same sequence of reads", t1.String() == t2.String())
}

func TestMoneyAndBoundaries(t *testing.T) {
	check(t, "Money formats minor units with the currency exponent", OfMinor(4999, USD).String() == "49.99 USD")
	_, err := OfMinor(1, USD).Plus(OfMinor(1, EUR))
	check(t, "Money rejects mixing currencies", errors.Is(err, ErrCurrencyMismatch))
	_, err = NewReplica(0, NewRng(1), 0, 5, true)
	check(t, "replica rejects a zero delay", errors.Is(err, ErrBadDelay))
}
