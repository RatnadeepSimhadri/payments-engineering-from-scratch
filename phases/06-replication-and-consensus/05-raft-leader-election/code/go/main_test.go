// Tests for phases/06-replication-and-consensus/05-raft-leader-election. Run: go test ./...
package main

import (
	"reflect"
	"testing"
)

func standalone(id int, storage *Storage, seed int64) *RaftNode {
	var peers []int
	for p := 0; p < 5; p++ {
		if p != id {
			peers = append(peers, p)
		}
	}
	return NewRaftNode(id, peers, storage, NewRng(seed), DefaultConfig, 0, nil)
}

func TestSingleLeaderElected(t *testing.T) {
	c := NewCluster(5, 7, 0.0, 5)
	at := c.RunUntilStableLeader(2000)
	leader := c.StableLeader()
	if at <= 0 || leader == nil {
		t.Fatalf("no stable leader (at=%d)", at)
	}
	if at < DefaultConfig.ElectionTimeoutMin {
		t.Errorf("elected at %d, before the minimum timeout", at)
	}
	if len(c.Leaders()) != 1 {
		t.Errorf("want exactly one leader, got %d", len(c.Leaders()))
	}
	for _, n := range c.Nodes {
		if n.CurrentTerm() != leader.CurrentTerm() {
			t.Errorf("%s term %d != leader term %d", name(n.ID), n.CurrentTerm(), leader.CurrentTerm())
		}
		if n.Role != Leader && n.LeaderID != leader.ID {
			t.Errorf("%s follows %d, want %d", name(n.ID), n.LeaderID, leader.ID)
		}
	}
}

func TestHeartbeatsSuppressNewElections(t *testing.T) {
	c := NewCluster(5, 7, 0.0, 5)
	c.RunUntilStableLeader(2000)
	term := c.MaxTerm()
	c.Run(2000)
	if c.MaxTerm() != term || len(c.Leaders()) != 1 {
		t.Errorf("term drifted from %d to %d with %d leaders", term, c.MaxTerm(), len(c.Leaders()))
	}
}

func TestSameSeedSameTrace(t *testing.T) {
	c1, c2, c3 := NewCluster(5, 42, 0.1, 8), NewCluster(5, 42, 0.1, 8), NewCluster(5, 43, 0.1, 8)
	c1.Run(1500)
	c2.Run(1500)
	c3.Run(1500)
	if len(c1.Events) == 0 || !reflect.DeepEqual(c1.Events, c2.Events) {
		t.Errorf("same seed produced different traces")
	}
	if reflect.DeepEqual(c1.Events, c3.Events) {
		t.Errorf("different seeds produced the same trace")
	}
}

func TestAtMostOneLeaderPerTermOverManySeeds(t *testing.T) {
	elected := 0
	for seed := int64(1); seed <= 40; seed++ {
		s := NewCluster(5, seed, 0.2, 10)
		if s.RunUntilStableLeader(2500) > 0 {
			elected++
		}
		s.Run(500) // CheckElectionSafety panics on a violation after every delivery
		if len(s.LeadersByTerm) == 0 {
			t.Errorf("seed %d recorded no leader", seed)
		}
	}
	if elected != 40 {
		t.Errorf("only %d/40 lossy seeds elected a leader", elected)
	}
	for seed := int64(100); seed < 120; seed++ {
		if NewCluster(3, seed, 0.3, 10).RunUntilStableLeader(4000) <= 0 {
			t.Errorf("3-node seed %d never elected", seed)
		}
	}
}

func TestPartitionHealsWithHigherTermWinning(t *testing.T) {
	p := NewCluster(5, 7, 0.0, 5)
	p.RunUntilStableLeader(2000)
	old := p.StableLeader()
	oldTerm := old.CurrentTerm()
	p.Partition(old.ID, (old.ID+1)%5)
	p.Run(800)
	if old.Role != Leader || old.CurrentTerm() != oldTerm {
		t.Errorf("stale leader should keep leading its minority: role %v term %d", old.Role, old.CurrentTerm())
	}
	if len(p.Leaders()) != 2 || p.MaxTerm() <= oldTerm {
		t.Errorf("majority side should elect a higher-term leader: %d leaders, max term %d", len(p.Leaders()), p.MaxTerm())
	}
	p.Heal()
	if p.RunUntilStableLeader(2000) <= 0 {
		t.Fatal("cluster did not converge after heal")
	}
	if old.Role != Follower {
		t.Errorf("old leader should step down, is %v", old.Role)
	}
	if p.StableLeader() == old || p.StableLeader().CurrentTerm() <= oldTerm {
		t.Errorf("the higher term should win")
	}
}

func TestMinorityPartitionNeverElects(t *testing.T) {
	m := NewCluster(5, 11, 0.0, 5)
	m.Partition(0, 1)
	m.Run(3000)
	for _, l := range m.Leaders() {
		if l.ID == 0 || l.ID == 1 {
			t.Errorf("minority node %d became leader", l.ID)
		}
	}
	if m.Node(0).CurrentTerm() <= 3 && m.Node(1).CurrentTerm() <= 3 {
		t.Errorf("minority side should keep trying with growing terms")
	}
	if len(m.Leaders()) != 1 {
		t.Errorf("majority side should have one leader, got %d", len(m.Leaders()))
	}
}

func TestCrashAndRestartRejoinsAsFollower(t *testing.T) {
	r := NewCluster(5, 21, 0.05, 5)
	r.RunUntilStableLeader(2000)
	lead := r.StableLeader()
	r.Crash(lead.ID)
	if r.RunUntilStableLeader(2000) <= 0 || r.StableLeader().ID == lead.ID {
		t.Fatal("cluster did not re-elect after leader crash")
	}
	back := r.Restart(lead.ID)
	if back.CurrentTerm() < lead.CurrentTerm() {
		t.Errorf("restart lost the term: %d < %d", back.CurrentTerm(), lead.CurrentTerm())
	}
	r.RunUntilStableLeader(2000)
	if back.Role != Follower || back.CurrentTerm() != r.StableLeader().CurrentTerm() {
		t.Errorf("restarted node should rejoin as follower in the current term")
	}
}

func TestRequestVoteReceiverRules(t *testing.T) {
	n := standalone(1, NewStorage(), 1)
	n.Storage.Save(5, -1)
	stale := n.Handle(requestVote(0, 1, 3, 0, 0), 10)[0]
	if stale.Granted || stale.Term != 5 || stale.Kind != RequestVoteReply {
		t.Errorf("stale term must be rejected with the receiver's term: %+v", stale)
	}
	if n.CurrentTerm() != 5 || n.VotedFor() != -1 {
		t.Errorf("rejecting a stale request changed state")
	}
	if !n.Handle(requestVote(0, 1, 5, 0, 0), 11)[0].Granted || n.VotedFor() != 0 {
		t.Errorf("first vote in a term should be granted")
	}
	if n.Handle(requestVote(2, 1, 5, 0, 0), 12)[0].Granted {
		t.Errorf("second candidate in the same term must be rejected")
	}
	if !n.Handle(requestVote(0, 1, 5, 0, 0), 13)[0].Granted {
		t.Errorf("retransmit from the same candidate should be granted again")
	}
	higher := n.Handle(requestVote(3, 1, 6, 0, 0), 14)[0]
	if !higher.Granted || n.CurrentTerm() != 6 || n.VotedFor() != 3 {
		t.Errorf("higher term should reset votedFor and grant")
	}
}

func TestLogUpToDateCheck(t *testing.T) {
	u := standalone(2, NewStorage(), 2)
	u.LastLogIndex, u.LastLogTerm = 10, 3
	if u.Handle(requestVote(0, 2, 4, 20, 2), 1)[0].Granted {
		t.Errorf("older last term must be rejected")
	}
	if u.Handle(requestVote(0, 2, 4, 9, 3), 2)[0].Granted {
		t.Errorf("same term, shorter log must be rejected")
	}
	if !u.Handle(requestVote(0, 2, 4, 10, 3), 3)[0].Granted {
		t.Errorf("same term, same length should be granted")
	}
	u2 := standalone(2, NewStorage(), 2)
	u2.LastLogIndex, u2.LastLogTerm = 10, 3
	if !u2.Handle(requestVote(1, 2, 4, 2, 4), 3)[0].Granted {
		t.Errorf("higher last term with shorter log should be granted")
	}
}

func TestVotedForPersistsAcrossCrash(t *testing.T) {
	durable := NewStorage()
	a := standalone(1, durable, 6)
	if !a.Handle(requestVote(0, 1, 1, 0, 0), 10)[0].Granted {
		t.Fatal("vote should be granted before the crash")
	}
	b := standalone(1, durable, 7) // restart: fresh volatile state, same Storage
	if b.CurrentTerm() != 1 || b.VotedFor() != 0 || b.Role != Follower {
		t.Errorf("restart should restore term 1 and votedFor 0: %d %d %v", b.CurrentTerm(), b.VotedFor(), b.Role)
	}
	if b.Handle(requestVote(2, 1, 1, 0, 0), 12)[0].Granted {
		t.Errorf("restarted node granted a second vote in the same term")
	}
	lost := standalone(1, NewStorage(), 8)
	if !lost.Handle(requestVote(2, 1, 1, 0, 0), 12)[0].Granted {
		t.Errorf("without persistence the double vote would be granted; the test models that counterfactual")
	}
}

func TestCandidateStepsDownOnHigherTerm(t *testing.T) {
	cand := standalone(0, NewStorage(), 3)
	reqs := cand.StartElection(100)
	if len(reqs) != 4 {
		t.Fatalf("candidate should ask every peer, sent %d", len(reqs))
	}
	for _, r := range reqs {
		if r.Kind != RequestVote || r.Term != 1 {
			t.Errorf("bad request %+v", r)
		}
	}
	if cand.Role != Candidate || cand.VotedFor() != 0 || cand.Storage.Writes < 1 {
		t.Errorf("candidate should vote for itself and persist")
	}
	cand.Handle(voteReply(1, 0, 1, true), 101)
	if cand.Role != Candidate || cand.Votes() != 2 {
		t.Errorf("two of five is not a majority")
	}
	cand.Handle(requestVote(3, 0, 2, 0, 0), 102)
	if cand.Role != Follower || cand.CurrentTerm() != 2 || cand.VotedFor() != 3 {
		t.Errorf("candidate should step down on a higher term and grant the vote: %v %d %d", cand.Role, cand.CurrentTerm(), cand.VotedFor())
	}
	cand2 := standalone(0, NewStorage(), 4)
	cand2.StartElection(100)
	hb := cand2.Handle(heartbeat(4, 0, 1), 101)[0]
	if cand2.Role != Follower || cand2.LeaderID != 4 || !hb.Granted {
		t.Errorf("candidate hearing a leader of its term should become follower")
	}
}

func TestStaleVoteRepliesIgnoredAndMajorityMakesLeader(t *testing.T) {
	w := standalone(0, NewStorage(), 5)
	w.StartElection(50)
	w.StartElection(300) // term 2
	w.Handle(voteReply(1, 0, 1, true), 301)
	if w.Votes() != 1 || w.Role != Candidate {
		t.Errorf("vote reply from an earlier term must be ignored")
	}
	w.Handle(voteReply(1, 0, 2, true), 302)
	hbs := w.Handle(voteReply(2, 0, 2, true), 303)
	if w.Role != Leader || w.LeaderID != 0 {
		t.Fatalf("third vote should make a leader")
	}
	if len(hbs) != 4 {
		t.Errorf("new leader should heartbeat every peer, sent %d", len(hbs))
	}
	for _, h := range hbs {
		if h.Kind != AppendEntries {
			t.Errorf("heartbeat has kind %v", h.Kind)
		}
	}
	if len(w.Tick(304)) != 0 || len(w.Tick(303+DefaultConfig.HeartbeatInterval)) != 4 {
		t.Errorf("leader should heartbeat only when due")
	}
	rej := w.Handle(heartbeat(3, 0, 1), 310)[0]
	if rej.Granted || w.Role != Leader {
		t.Errorf("leader should reject a stale-term heartbeat")
	}
	w.Handle(heartbeat(3, 0, 3), 311)
	if w.Role != Follower || w.LeaderID != 3 || w.CurrentTerm() != 3 {
		t.Errorf("leader should step down on a higher-term heartbeat")
	}
}

func TestTwoLeadersInOneTermDetected(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Errorf("expected a panic on two leaders in one term")
		}
	}()
	l1 := standalone(0, NewStorage(), 9)
	l1.StartElection(1)
	l1.Handle(voteReply(1, 0, 1, true), 2)
	l1.Handle(voteReply(2, 0, 1, true), 3)
	l1.Handle(heartbeat(4, 0, 1), 4) // a second leader for term 1 announces itself
}

func TestConfigValidation(t *testing.T) {
	if _, err := NewConfig(100, 200, 150); err == nil {
		t.Errorf("heartbeat >= election timeout should be rejected")
	}
	if _, err := NewConfig(300, 150, 50); err == nil {
		t.Errorf("max below min should be rejected")
	}
	if _, err := NewConfig(150, 300, 50); err != nil {
		t.Errorf("default config should be valid: %v", err)
	}
}

func TestRngContract(t *testing.T) {
	if got := NewRng(12345).NextLong(); got != 2454886589211414944 {
		t.Errorf("SplitMix64 first output = %d", got)
	}
	r := NewRng(1)
	for i := 0; i < 1000; i++ {
		if v := r.NextInt(151); v < 0 || v >= 151 {
			t.Fatalf("NextInt out of range: %d", v)
		}
	}
}
