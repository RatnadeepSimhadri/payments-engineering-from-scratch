// Tests for phases/06-replication-and-consensus/06-raft-log-replication. Run: go test ./...
package main

import (
	"reflect"
	"strconv"
	"testing"
)

func standalone(id int, storage *Storage) *RaftNode {
	var peers []int
	for p := 0; p < 5; p++ {
		if p != id {
			peers = append(peers, p)
		}
	}
	return NewRaftNode(id, peers, storage, NewRng(int64(id+1)), DefaultConfig, 0, nil)
}

// follower builds node 1 with the given term sequence in its log, e.g. follower(1, 1, 2) has three entries with terms 1, 1, 2.
func follower(terms ...int64) *RaftNode {
	s := NewStorage()
	for i, t := range terms {
		s.Append(LogEntry{t, "SET k" + itoa(i+1) + " old"})
	}
	last := int64(0)
	if len(terms) > 0 {
		last = terms[len(terms)-1]
	}
	s.Save(last, -1)
	return standalone(1, s)
}

func leaderOfTerm(term int64, logTerms ...int64) *RaftNode {
	s := NewStorage()
	for i, t := range logTerms {
		s.Append(LogEntry{t, "SET k" + itoa(i+1) + " v"})
	}
	s.Save(term-1, 0)
	l := standalone(0, s)
	l.StartElection(1)
	l.Handle(voteReply(1, 0, l.CurrentTerm(), true), 2)
	l.Handle(voteReply(2, 0, l.CurrentTerm(), true), 3)
	return l
}

func itoa(i int) string { return strconv.Itoa(i) }

func allLogsEqual(c *Cluster) bool {
	first := c.LogOf(0)
	for i := 1; i < c.Size; i++ {
		if c.LogOf(i) != first {
			return false
		}
	}
	return true
}

func get(kv *KVStore, key string) string {
	v, _ := kv.Get(key)
	return v
}

func expectPanic(t *testing.T, name string, f func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Errorf("%s: expected a panic", name)
		}
	}()
	f()
}

func TestHappyPathReplicatesCommitsAndApplies(t *testing.T) {
	c := NewCluster(5, 7, 0.0, 5)
	c.RunUntilStableLeader(2000)
	leader := c.StableLeader()
	i1 := c.Propose("SET alice 100")
	i2 := c.Propose("SET bob 50")
	i3 := c.Propose("SET alice 75")
	if i1 != 2 || i2 != 3 || i3 != 4 {
		t.Errorf("indexes after the election no-op: got %d %d %d, want 2 3 4", i1, i2, i3)
	}
	if leader.Log()[i3].Term != leader.CurrentTerm() {
		t.Errorf("entry term %d != leader term %d", leader.Log()[i3].Term, leader.CurrentTerm())
	}
	if c.RunUntilApplied(i3, 1000) <= 0 {
		t.Fatal("not all nodes applied within 1000 ticks")
	}
	if !allLogsEqual(c) || !c.StateMachinesConverged() {
		t.Error("logs or state machines differ")
	}
	if get(c.Node(0).StateMachine, "alice") != "75" || get(c.Node(3).StateMachine, "bob") != "50" {
		t.Error("KV does not hold the last write")
	}
	for _, n := range c.Nodes {
		if n.CommitIndex != i3 || n.LastApplied != i3 {
			t.Errorf("%s commit=%d applied=%d, want %d", name(n.ID), n.CommitIndex, n.LastApplied, i3)
		}
	}
	if c.Node((leader.ID+1)%5).Propose("SET x 1") != -1 {
		t.Error("propose on a follower must be refused")
	}
}

func TestStaleLeaderEntriesAreOverwritten(t *testing.T) {
	c := NewCluster(5, 7, 0.0, 5)
	c.RunUntilStableLeader(2000)
	c.Propose("SET alice 100")
	c.Propose("SET bob 50")
	i3 := c.Propose("SET alice 75")
	c.RunUntilApplied(i3, 1000)
	old := c.StableLeader()
	minority := (old.ID + 1) % 5
	c.Partition(old.ID, minority)
	stale1 := c.ProposeTo(old.ID, "SET alice 999")
	stale2 := c.ProposeTo(old.ID, "SET carol 1")
	c.Run(600)
	if stale1 != 5 || stale2 != 6 || old.LastLogIndex() != 6 {
		t.Errorf("stale leader should hold indexes 5 and 6, got %d %d last=%d", stale1, stale2, old.LastLogIndex())
	}
	if old.CommitIndex != 4 {
		t.Errorf("stale leader committed %d, want 4", old.CommitIndex)
	}
	var fresh *RaftNode
	for _, l := range c.Leaders() {
		if l != old {
			fresh = l
		}
	}
	if fresh == nil || fresh.CurrentTerm() <= old.CurrentTerm() {
		t.Fatal("majority side should elect a new leader with a higher term")
	}
	if fresh.CommitIndex != 5 || fresh.Log()[5].Command != "NOOP" {
		t.Errorf("new leader commit=%d log[5]=%s, want 5 and NOOP", fresh.CommitIndex, fresh.Log()[5])
	}
	if c.Node(minority).LastLogIndex() != 6 {
		t.Error("minority follower should hold the divergent entries")
	}
	c.Heal()
	c.RunUntilStableLeader(2000)
	dave := c.Propose("SET dave 7")
	c.RunUntilApplied(dave, 1000)
	if !allLogsEqual(c) {
		t.Error("after heal all logs should be identical")
	}
	if c.Node(minority).Log()[5].Command != "NOOP" || c.Node(minority).Log()[6].Command != "SET dave 7" {
		t.Errorf("divergent entries not replaced: %s", c.LogOf(minority))
	}
	if get(c.Node(minority).StateMachine, "alice") != "75" || get(c.Node(0).StateMachine, "carol") != "" {
		t.Error("an uncommitted write reached a state machine")
	}
	if old.Role != Follower || !c.StateMachinesConverged() {
		t.Error("old leader should step down and converge")
	}
	if fresh.Log()[5].Command != "NOOP" || fresh.Role != Leader {
		t.Error("new leader's committed prefix changed by the merge")
	}
}

func TestCommitOnlyOnMajority(t *testing.T) {
	q := NewCluster(5, 11, 0.0, 5)
	q.RunUntilStableLeader(2000)
	lead := q.StableLeader()
	others := append([]int(nil), lead.Peers...)
	q.Crash(others[0])
	q.Crash(others[1])
	q.Crash(others[2])
	idx := q.Propose("SET balance 1000")
	q.Run(400)
	if lead.CommitIndex >= idx || lead.ReplicatedCount(idx) != 2 {
		t.Errorf("with 2/5 replicas: commit=%d replicated=%d", lead.CommitIndex, lead.ReplicatedCount(idx))
	}
	if get(lead.StateMachine, "balance") != "" {
		t.Error("leader applied an uncommitted entry")
	}
	q.Restart(others[0])
	q.Run(400)
	if lead.CommitIndex < idx || lead.ReplicatedCount(idx) < 3 {
		t.Errorf("with 3/5 replicas: commit=%d replicated=%d", lead.CommitIndex, lead.ReplicatedCount(idx))
	}
	if get(lead.StateMachine, "balance") != "1000" || get(q.Node(others[0]).StateMachine, "balance") != "1000" {
		t.Error("committed entry not applied on leader and restarted node")
	}
}

func TestRestartKeepsLogRebuildsVolatileState(t *testing.T) {
	rs := NewCluster(3, 5, 0.0, 3)
	rs.RunUntilStableLeader(2000)
	w := rs.Propose("SET k 1")
	rs.RunUntilApplied(w, 500)
	victim := (rs.StableLeader().ID + 1) % 3
	lengthBefore := rs.Node(victim).LastLogIndex()
	rs.Crash(victim)
	back := rs.Restart(victim)
	if back.LastLogIndex() != lengthBefore || back.Log()[w].Command != "SET k 1" {
		t.Error("log did not survive the restart")
	}
	if back.CommitIndex != 0 || back.LastApplied != 0 || get(back.StateMachine, "k") != "" {
		t.Error("commitIndex and state machine should be volatile")
	}
	rs.Run(200)
	if back.LastApplied < w || get(back.StateMachine, "k") != "1" {
		t.Error("restarted follower did not re-apply from the leader's commit index")
	}
}

func TestAppendEntriesReceiverRules(t *testing.T) {
	f := follower(1, 1, 2)
	r1 := f.Handle(appendEntries(0, 1, 2, 5, 2, nil, 0), 10)[0]
	if r1.Success || f.ConsistencyRejections != 1 {
		t.Error("rule 2: prevLogIndex beyond the log must be rejected")
	}
	r2 := f.Handle(appendEntries(0, 1, 2, 3, 1, nil, 0), 11)[0]
	if r2.Success || f.LastLogIndex() != 3 {
		t.Error("rule 2: prevLogTerm mismatch must be rejected")
	}
	r3 := f.Handle(appendEntries(0, 1, 2, 3, 2, nil, 0), 12)[0]
	if !r3.Success || r3.MatchIndex != 3 {
		t.Error("matching prevLogIndex/prevLogTerm must be accepted with matchIndex 3")
	}
	two := []LogEntry{{3, "SET k2 new"}, {3, "SET k3 new"}}
	r4 := f.Handle(appendEntries(0, 1, 3, 1, 1, two, 0), 13)[0]
	if !r4.Success || f.LastLogIndex() != 3 || f.Log()[2].Term != 3 || f.Log()[3].Command != "SET k3 new" {
		t.Errorf("rule 3: conflicting entries not truncated and replaced: %v", f.Log())
	}
	r5 := f.Handle(appendEntries(0, 1, 3, 1, 1, two, 0), 14)[0]
	if !r5.Success || f.LastLogIndex() != 3 {
		t.Error("rule 4: a duplicate AppendEntries must be idempotent")
	}
	r6 := f.Handle(appendEntries(0, 1, 3, 3, 3, []LogEntry{{3, "SET k4 new"}}, 10), 15)[0]
	if !r6.Success || f.CommitIndex != 4 || f.LastApplied != 4 {
		t.Errorf("rule 5: commitIndex should be min(leaderCommit, lastNew)=4, got %d", f.CommitIndex)
	}
	want := []string{"SET k1 old", "SET k2 new", "SET k3 new", "SET k4 new"}
	if !reflect.DeepEqual(f.StateMachine.AppliedCommands, want) {
		t.Errorf("applied %v, want %v", f.StateMachine.AppliedCommands, want)
	}
	r7 := f.Handle(appendEntries(0, 1, 3, 2, 3, nil, 2), 16)[0]
	if !r7.Success || f.LastLogIndex() != 4 || f.CommitIndex != 4 {
		t.Error("heartbeat with an older prefix must not truncate newer matching entries")
	}
	r8 := f.Handle(appendEntries(9, 1, 2, 4, 3, nil, 4), 17)[0]
	if r8.Success || r8.Term != 3 || f.LastLogIndex() != 4 {
		t.Error("rule 1: stale-term AppendEntries must be rejected without touching the log")
	}
}

func TestLeaderBacksOffNextIndexAndCommitsOnMajority(t *testing.T) {
	l := leaderOfTerm(3, 1, 1, 2)
	if l.NextIndex[1] != 4 || l.LastLogIndex() != 4 || l.Log()[4].Command != "NOOP" {
		t.Error("leader should initialise nextIndex to last index + 1 and append a no-op")
	}
	retry := l.Handle(appendReply(1, 0, 3, false, 0), 10)
	if l.NextIndex[1] != 3 || len(retry) != 1 || retry[0].PrevLogIndex != 2 || len(retry[0].Entries) != 2 {
		t.Error("rejection must decrement nextIndex and resend immediately")
	}
	l.Handle(appendReply(1, 0, 3, false, 0), 11)
	l.Handle(appendReply(1, 0, 3, false, 0), 12)
	floor := l.Handle(appendReply(1, 0, 3, false, 0), 13)
	if l.NextIndex[1] != 1 || floor[0].PrevLogIndex != 0 || floor[0].PrevLogTerm != 0 {
		t.Error("nextIndex must never drop below 1")
	}
	l.Handle(appendReply(1, 0, 3, true, 4), 14)
	if l.MatchIndex[1] != 4 || l.NextIndex[1] != 5 {
		t.Error("success must set matchIndex and nextIndex")
	}
	if l.CommitIndex != 0 {
		t.Error("one follower is not a majority of five")
	}
	l.Handle(appendReply(2, 0, 3, true, 4), 15)
	if l.CommitIndex != 4 || l.LastApplied != 4 {
		t.Errorf("second follower makes a majority: commit=%d applied=%d, want 4", l.CommitIndex, l.LastApplied)
	}
	l.Handle(appendReply(3, 0, 3, true, 2), 16)
	if l.CommitIndex != 4 {
		t.Error("an older matchIndex must never move commitIndex backwards")
	}
	l.Handle(appendReply(3, 0, 2, true, 4), 17)
	if l.MatchIndex[3] != 2 {
		t.Error("replies from an older term must be ignored")
	}
	stale := l.Handle(appendEntries(7, 0, 2, 0, 0, []LogEntry{{2, "SET hijack 1"}}, 0), 18)[0]
	if stale.Success || l.LastLogIndex() != 4 || l.Role != Leader {
		t.Error("leader must reject a stale leader's entries and keep its log")
	}
}

func TestOnlyCurrentTermEntriesCommitByCounting(t *testing.T) {
	f8 := leaderOfTerm(5, 2, 2)
	f8.Handle(appendReply(1, 0, 5, true, 2), 4)
	f8.Handle(appendReply(2, 0, 5, true, 2), 5)
	if f8.CommitIndex != 0 {
		t.Errorf("term-2 entries on 3/5 replicas committed in term 5: commit=%d", f8.CommitIndex)
	}
	f8.Handle(appendReply(1, 0, 5, true, 3), 6)
	f8.Handle(appendReply(2, 0, 5, true, 3), 7)
	if f8.CommitIndex != 3 || len(f8.StateMachine.AppliedCommands) != 3 {
		t.Errorf("the current-term no-op should commit itself and everything before it: commit=%d", f8.CommitIndex)
	}
}

func TestElectionLogCheckUsesRealLog(t *testing.T) {
	voter := follower(1, 2)
	if voter.Handle(requestVote(3, 1, 3, 1, 2), 1)[0].Success {
		t.Error("candidate with a shorter log of the same last term must be refused")
	}
	if !voter.Handle(requestVote(3, 1, 3, 1, 3), 2)[0].Success {
		t.Error("candidate with a higher last term must be granted")
	}
}

func TestManyLossySeedsConverge(t *testing.T) {
	converged := 0
	for seed := int64(1); seed <= 25; seed++ {
		r := NewCluster(5, seed, 0.2, 10)
		r.RunUntilStableLeader(3000)
		for k := 1; k <= 6; k++ {
			proposeUntilAccepted(r, "SET k"+itoa(k)+" v"+itoa(k))
			r.Run(40)
		}
		victim := r.StableLeader()
		if victim == nil {
			victim = r.Node(0)
		}
		r.Crash(victim.ID)
		r.RunUntilStableLeader(3000)
		for k := 7; k <= 12; k++ {
			proposeUntilAccepted(r, "SET k"+itoa(k)+" v"+itoa(k))
			r.Run(40)
		}
		r.Restart(victim.ID)
		r.RunUntilStableLeader(3000)
		fin := r.StableLeader()
		if fin != nil {
			r.RunUntilApplied(fin.LastLogIndex(), 3000)
		}
		if fin != nil && r.StateMachinesConverged() && get(fin.StateMachine, "k12") == "v12" && get(fin.StateMachine, "k1") == "v1" {
			converged++
		}
	}
	if converged != 25 {
		t.Errorf("%d/25 lossy seeds converged on the same 12 writes", converged)
	}
}

func TestSameSeedSameTrace(t *testing.T) {
	d1, d2 := NewCluster(5, 42, 0.15, 8), NewCluster(5, 42, 0.15, 8)
	d1.RunUntilStableLeader(3000)
	d2.RunUntilStableLeader(3000)
	d1.Propose("SET a 1")
	d2.Propose("SET a 1")
	d1.Run(500)
	d2.Run(500)
	if len(d1.Events) <= 3 || !reflect.DeepEqual(d1.Events, d2.Events) {
		t.Error("same seed must give the same trace with replication")
	}
}

func TestCheckersFire(t *testing.T) {
	sentinel := LogEntry{0, "<sentinel>"}
	err := CheckLogMatching(
		[]LogEntry{sentinel, {1, "SET a 1"}, {2, "SET b 2"}},
		[]LogEntry{sentinel, {1, "SET a 9"}, {2, "SET b 2"}}, 0, 1)
	if err == nil {
		t.Error("Log Matching checker must detect a violation")
	}
	err = CheckLogMatching(
		[]LogEntry{sentinel, {1, "SET a 1"}, {2, "SET b 2"}},
		[]LogEntry{sentinel, {1, "SET a 1"}, {3, "SET c 3"}}, 0, 1)
	if err != nil {
		t.Errorf("Log Matching must allow divergence after the last common term: %v", err)
	}
	expectPanic(t, "two leaders in one term", func() {
		leaderOfTerm(2).Handle(appendEntries(4, 0, 2, 0, 0, nil, 0), 9)
	})
}

func TestKVStore(t *testing.T) {
	kv := NewKVStore()
	for _, cmd := range []string{"SET a 1", "SET b 2", "DEL a", "NOOP"} {
		if err := kv.Apply(cmd); err != nil {
			t.Fatalf("Apply(%q): %v", cmd, err)
		}
	}
	if _, ok := kv.Get("a"); ok || get(kv, "b") != "2" || len(kv.AppliedCommands) != 4 {
		t.Error("KV should apply SET, DEL, NOOP")
	}
	if kv.Apply("INCR a") == nil {
		t.Error("KV must reject unknown commands")
	}
	if kv.String() != "{b=2}" {
		t.Errorf("String() = %s", kv.String())
	}
}
