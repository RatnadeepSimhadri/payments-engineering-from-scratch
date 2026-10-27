// Tests for phases/06-replication-and-consensus/07-raft-safety-and-membership. Run: go test ./...
package main

import (
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

// checkPanics is the Go form of the Java checkThrows for IllegalStateException: the code panics on a broken invariant.
func checkPanics(t *testing.T, name string, action func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Errorf("FAIL: %s (no panic)", name)
		}
	}()
	action()
}

func kvGet(kv *KVStore, key string) string {
	v, ok := kv.Get(key)
	if !ok {
		return "<null>"
	}
	return v
}

func TestConfigurationsAndQuorums(t *testing.T) {
	three := Simple([]int{0, 1, 2})
	joint := Joint([]int{0, 1, 2}, []int{0, 1, 2, 3, 4})
	check(t, "simple majority of 3 is 2", three.HasQuorum(set(0, 1)) && !three.HasQuorum(set(0)))
	check(t, "members outside the configuration are not counted", !three.HasQuorum(set(0, 3, 4)))
	check(t, "joint needs majority of old AND new", joint.HasQuorum(set(0, 1, 3)) && !joint.HasQuorum(set(0, 1)) && !joint.HasQuorum(set(2, 3, 4)))
	check(t, "joint union is everyone", equalInts(joint.All(), []int{0, 1, 2, 3, 4}))
	check(t, "direct switch 3->5 admits disjoint majorities (Figure 10)", DisjointQuorumsPossible([]int{0, 1, 2}, []int{0, 1, 2, 3, 4}))
	check(t, "adding ONE server never admits disjoint majorities (thesis 4.1: single-server changes are safe)", !DisjointQuorumsPossible([]int{0, 1, 2}, []int{0, 1, 2, 3}))
	check(t, "removing one server is safe too", !DisjointQuorumsPossible([]int{0, 1, 2, 3, 4}, []int{0, 1, 2, 3}))
	check(t, "two quorums of the same configuration always intersect", !DisjointQuorumsPossible([]int{0, 1, 2}, []int{0, 1, 2}))
	check(t, "config round-trips through the log as a command", Decode(joint.Encode()).Equal(joint) && Decode(three.Encode()).Equal(three))
	check(t, "decoded joint is joint", Decode("CFG 0,1,2 -> 1,2,3").IsJoint())
}

func TestStorageWithCompaction(t *testing.T) {
	st := NewStorage()
	for i := 1; i <= 6; i++ {
		cmd := fmt.Sprintf("SET k%d v", i)
		if i == 3 {
			cmd = "CFG 0,1,2"
		}
		st.Append(LogEntry{1, cmd})
	}
	cfg := Simple([]int{0, 1, 2})
	snap := NewSnapshot(4, 1, map[string]string{"k1": "v"}, &cfg)
	st.Compact(snap)
	check(t, "compact keeps entries after the snapshot", st.SnapshotIndex == 4 && st.LastIndex() == 6 && st.FirstIndex() == 5)
	check(t, "termAt the snapshot index is the snapshot term", st.TermAt(4) == 1 && st.TermAt(6) == 1)
	checkPanics(t, "termAt below the snapshot is an error", func() { st.TermAt(2) })
	check(t, "configAt falls back to the snapshot's configuration", st.ConfigAt(6, nil).Equal(Simple([]int{0, 1, 2})) && st.LatestConfigIndex() == -1)
	st.Append(LogEntry{2, "CFG 0,1,2 -> 0,1"})
	check(t, "configAt finds the latest config in the log", st.ConfigAt(7, nil).IsJoint() && st.LatestConfigIndex() == 7)
	check(t, "configAt at an earlier index ignores later configs", !st.ConfigAt(6, nil).IsJoint())
}

func TestSpareServerNeverCampaigns(t *testing.T) {
	idle := NewClusterWithMembers(3, []int{0, 1}, 1, 0.0, 3, DefaultConfig)
	idle.Run(1500)
	check(t, "spare server with no config stays follower at term 0", idle.Node(2).Role == Follower && idle.Node(2).CurrentTerm() <= idle.Node(0).CurrentTerm())
	check(t, "the two configured servers elected a leader", idle.StableLeader() != nil)
}

func TestGrowAndRemoveLeader(t *testing.T) {
	five := []int{0, 1, 2, 3, 4}
	noSnap, err := NewConfig(150, 300, 50, 1000)
	if err != nil {
		t.Fatal(err)
	}
	c := NewClusterWithMembers(5, []int{0, 1, 2}, 7, 0.0, 5, noSnap)
	c.RunUntilStableLeader(2000)
	c.Propose("SET alice 100")
	growLeader := c.StableLeader()
	cfgIdx := c.ChangeMembership(five)
	check(t, "leader accepts the change and logs C_old,new", cfgIdx > 0 && growLeader.Configuration().IsJoint())
	check(t, "a second change is refused while the first is in flight", growLeader.ChangeMembership([]int{0, 1, 2, 3}) == -1)
	c.Propose("SET bob 50")
	done := c.RunUntilConfigured(five, 3000)
	check(t, "C_new is committed", done > 0 && c.StableLeader().Configuration().Equal(Simple(five)))
	c.Propose("SET carol 7")
	c.RunUntilApplied(c.StableLeader().LastLogIndex(), 2000)
	allSame := true
	for _, n := range c.Nodes {
		if !n.Configuration().Equal(Simple(five)) {
			allSame = false
		}
	}
	check(t, "all five servers hold the same configuration", allSame)
	check(t, "all five state machines converged including entries proposed mid-change", c.StateMachinesConverged(five) && kvGet(c.Node(3).StateMachine, "bob") == "50")
	check(t, "the joint entry precedes the simple entry in the log",
		strings.Contains(c.Node(4).Storage.Get(cfgIdx).Command, "->") && !strings.Contains(c.Node(4).Storage.Get(cfgIdx+1).Command, "->"))

	// remove the leader: it steps down after committing C_new and never campaigns again
	oldLeader := c.StableLeader()
	four := without(five, oldLeader.id)
	check(t, "removal change accepted", c.ChangeMembership(four) > 0)
	check(t, "C_new committed without the leader's own vote counting", c.RunUntilConfigured(four, 3000) > 0 || oldLeader.SteppedDownAfterRemoval)
	c.RunUntilStableLeader(3000)
	check(t, "old leader stepped down", oldLeader.SteppedDownAfterRemoval && oldLeader.Role != Leader)
	newLeader := c.StableLeader()
	check(t, "a new leader was elected among the remaining four", newLeader != nil && newLeader.id != oldLeader.id && Simple(four).Contains(newLeader.id))
	c.Run(1500)
	check(t, "removed server does not disrupt the cluster with elections", oldLeader.Role == Follower && c.StableLeader() != nil && c.StableLeader().CurrentTerm() == newLeader.CurrentTerm())
	after := c.Propose("SET dave 1")
	check(t, "cluster keeps accepting writes", after > 0 && c.RunUntilApplied(after, 2000) > 0)
}

func TestSnapshotsAndInstallSnapshot(t *testing.T) {
	s := NewCluster(3, 11, 0.0, 5)
	s.RunUntilStableLeader(2000)
	for k := 1; k <= 5; k++ {
		s.Propose(fmt.Sprintf("SET k%d v%d", k, k))
	}
	s.RunUntilApplied(s.StableLeader().LastLogIndex(), 1000)
	lead := s.StableLeader()
	follower := (lead.id + 1) % 3
	s.Crash(follower)
	for k := 6; k <= 16; k++ {
		s.Propose(fmt.Sprintf("SET k%d v%d", k, k))
		s.Run(20)
	}
	s.Run(200)
	check(t, "leader compacted its log", lead.SnapshotsTaken >= 1 && lead.Storage.SnapshotIndex >= 8)
	check(t, "leader's state machine is intact after compaction", kvGet(lead.StateMachine, "k1") == "v1" && kvGet(lead.StateMachine, "k16") == "v16")
	s.Restart(follower)
	s.RunUntilApplied(lead.LastLogIndex(), 3000)
	check(t, "lagging follower received a snapshot", s.Node(follower).SnapshotsInstalled >= 1 && lead.SnapshotsSent >= 1)
	check(t, "follower's log starts after the snapshot", s.Node(follower).Storage.SnapshotIndex >= 8 && s.Node(follower).Storage.FirstIndex() > 8)
	check(t, "follower state machine rebuilt from the snapshot plus the tail", kvGet(s.Node(follower).StateMachine, "k1") == "v1" && s.StateMachinesConverged([]int{0, 1, 2}))
	check(t, "snapshot carries the configuration", s.Node(follower).Storage.Snapshot.Config.Equal(Simple([]int{0, 1, 2})))
}

func TestInstallSnapshotRules(t *testing.T) {
	cfg := Simple([]int{0, 1, 2})
	r6 := NewStorage()
	for i := 1; i <= 5; i++ {
		r6.Append(LogEntry{1, fmt.Sprintf("SET k%d v", i)})
	}
	f6 := NewRaftNode(0, &cfg, r6, NewRng(1), DefaultConfig, 0, nil)
	f6.Storage.Save(1, -1)
	f6.Handle(installSnapshot(1, 0, 1, NewSnapshot(3, 1, map[string]string{"k1": "v", "k2": "v", "k3": "v"}, &cfg)), 1)
	check(t, "rule 6: matching snapshot keeps the following entries", f6.Storage.SnapshotIndex == 3 && f6.LastLogIndex() == 5 && f6.Storage.Get(5).Command == "SET k5 v")
	check(t, "rule 8: state machine reset to the snapshot and lastApplied advanced", f6.LastApplied == 3 && kvGet(f6.StateMachine, "k3") == "v" && f6.CommitIndex == 3)
	f6.Handle(installSnapshot(1, 0, 1, NewSnapshot(2, 1, map[string]string{}, &cfg)), 2)
	check(t, "an older snapshot is ignored", f6.Storage.SnapshotIndex == 3)

	// a restarted server reloads its snapshot into the state machine
	re := NewRaftNode(0, &cfg, r6, NewRng(2), DefaultConfig, 0, nil)
	check(t, "restart from storage rebuilds the state machine from the snapshot", re.LastApplied == 3 && kvGet(re.StateMachine, "k2") == "v")
}

func TestCheckerFires(t *testing.T) {
	forged := NewCluster(2, 3, 0.0, 2)
	forged.Node(0).Storage.Save(5, 0)
	forged.Node(0).Role = Leader
	forged.Node(1).Storage.Save(5, 1)
	forged.Node(1).Role = Leader
	checkPanics(t, "Election Safety violation is detected", forged.CheckInvariants)

	forged2 := NewCluster(2, 4, 0.0, 2)
	forged2.Node(0).Storage.Append(LogEntry{1, "SET x 1"})
	forged2.Node(0).CommitIndex = 1
	forged2.Node(0).Storage.Save(1, -1)
	forged2.CheckInvariants() // records index 1 as committed
	forged2.Node(1).Storage.Save(2, 1)
	forged2.Node(1).Role = Leader // empty log, claims term 2
	checkPanics(t, "Leader Completeness violation is detected", forged2.CheckInvariants)
}

func TestSafetyAndConvergenceAcrossSeeds(t *testing.T) {
	five := []int{0, 1, 2, 3, 4}
	converged, changes := 0, 0
	for seed := int64(1); seed <= 8; seed++ {
		r := NewClusterWithMembers(5, []int{0, 1, 2}, seed, 0.2, 10, DefaultConfig)
		r.RunUntilStableLeader(4000)
		for k := 1; k <= 5; k++ {
			proposeUntilAccepted(r, fmt.Sprintf("SET k%d v%d", k, k))
			r.Run(30)
		}
		changeUntilAccepted(r, five)
		for k := 6; k <= 10; k++ {
			proposeUntilAccepted(r, fmt.Sprintf("SET k%d v%d", k, k))
			r.Run(30)
		}
		if r.RunUntilConfigured(five, 6000) > 0 {
			changes++
		}
		victim := r.StableLeader()
		if victim == nil {
			victim = r.Node(0)
		}
		r.Crash(victim.id)
		r.RunUntilStableLeader(4000)
		for k := 11; k <= 18; k++ {
			proposeUntilAccepted(r, fmt.Sprintf("SET k%d v%d", k, k))
			r.Run(30)
		}
		r.Restart(victim.id)
		r.RunUntilStableLeader(4000)
		fourB := without(five, (victim.id+1)%5)
		changeUntilAccepted(r, fourB)
		if r.RunUntilConfigured(fourB, 6000) > 0 {
			changes++
		}
		proposeUntilAccepted(r, "SET final yes")
		fin := r.StableLeader()
		if fin != nil {
			r.RunUntilApplied(fin.LastLogIndex(), 4000)
		}
		if fin != nil && r.StateMachinesConverged(fourB) && kvGet(fin.StateMachine, "final") == "yes" && kvGet(fin.StateMachine, "k18") == "v18" {
			converged++
		}
	}
	check(t, "8/8 seeds converge through 3->5->4 with drops, a crash, and snapshots", converged == 8)
	check(t, "16/16 membership changes completed", changes == 16)
}
