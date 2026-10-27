// Stage 02 tests for projects/raft-kv (Go): leader election (RequestVote, randomized timeouts, majority, step-down).
// Run by the grader after stage01_test.go: go test ./...
// Port of Stage02Test.java: same checks, same names, same order. Helpers are prefixed s02.
package main

import (
	"fmt"
	"strings"
	"testing"
)

func s02check(t *testing.T, name string, condition bool) {
	t.Helper()
	if !condition {
		t.Errorf("FAIL: %s", name)
	}
}

func s02scenario(t *testing.T, name string, body func(t *testing.T)) {
	t.Helper()
	t.Run(name, func(t *testing.T) {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("%s blew up: %v", name, r)
			}
		}()
		body(t)
	})
}

func s02peersOf(id, size int) []int {
	ps := make([]int, 0, size-1)
	for j := 0; j < size; j++ {
		if j != id {
			ps = append(ps, j)
		}
	}
	return ps
}

func s02standalone(id int, storage *Storage, seed int64) *RaftNode {
	return NewRaftNode(id, s02peersOf(id, 5), storage, NewRng(seed), DefaultConfig, 0, TracerNone)
}

func s02allOfKind(ms []Message, kind MessageKind, from int, term int64, expectedCount int) bool {
	if len(ms) != expectedCount {
		return false
	}
	targets := map[int]bool{}
	for _, m := range ms {
		if m.Kind != kind || m.From != from || m.Term != term {
			return false
		}
		targets[m.To] = true
	}
	return len(targets) == expectedCount && !targets[from]
}

// s02digest is a tracer-independent fingerprint of a 1500-tick run: node state every 50 ticks plus the network counters.
func s02digest(c *Cluster) string {
	var sb strings.Builder
	for t := 0; t < 30; t++ {
		c.Run(50)
		for _, n := range c.Nodes {
			fmt.Fprintf(&sb, "%d%v%d%d;", n.CurrentTerm(), n.Role, n.LeaderID, n.ElectionDeadline)
		}
		fmt.Fprintf(&sb, "%d/%d/%d\n", c.Network.Sent, c.Network.Delivered, c.Network.Dropped)
	}
	return sb.String()
}

// s02electedLeader drives a standalone node to leadership of term 1 with votes from peers 1 and 2.
func s02electedLeader(storage *Storage, seed int64) *RaftNode {
	n := s02standalone(0, storage, seed)
	n.StartElection(1)
	n.Handle(NewVoteReply(1, 0, n.CurrentTerm(), true), 2)
	n.Handle(NewVoteReply(2, 0, n.CurrentTerm(), true), 3)
	return n
}

func s02all(nodes []*RaftNode, pred func(n *RaftNode) bool) bool {
	for _, n := range nodes {
		if !pred(n) {
			return false
		}
	}
	return true
}

func TestStage02(t *testing.T) {
	s02scenario(t, "randomized election timeout from the node's seeded Rng", func(t *testing.T) {
		n := s02standalone(0, NewStorage(), 1)
		s02check(t, "first deadline is drawn at construction, within [min, max] of now=0", n.ElectionDeadline >= 150 && n.ElectionDeadline <= 300)
		distinct := map[int]bool{}
		for seed := int64(1); seed <= 12; seed++ {
			distinct[s02standalone(0, NewStorage(), seed).ElectionDeadline] = true
		}
		s02check(t, "different seeds give different timeouts (split votes are broken by randomness)", len(distinct) >= 4)
		s02check(t, "same seed gives the same timeout (replayable)", s02standalone(0, NewStorage(), 5).ElectionDeadline == s02standalone(0, NewStorage(), 5).ElectionDeadline)
		n.ResetElectionTimer(1000)
		s02check(t, "resetElectionTimer draws a new deadline relative to now", n.ElectionDeadline >= 1150 && n.ElectionDeadline <= 1300)
		tightConfig, err := NewConfig(10, 10, 2)
		if err != nil {
			t.Fatalf("NewConfig(10, 10, 2): %v", err)
		}
		tight := NewRaftNode(0, s02peersOf(0, 3), NewStorage(), NewRng(3), tightConfig, 7, TracerNone)
		s02check(t, "with min == max the deadline is exactly now + min", tight.ElectionDeadline == 17)
	})

	s02scenario(t, "a follower whose timer expires becomes a candidate", func(t *testing.T) {
		s := NewStorage()
		n := s02standalone(2, s, 4)
		deadline := n.ElectionDeadline
		s02check(t, "ticks before the deadline send nothing and keep the follower role", len(n.Tick(deadline-1)) == 0 && n.Role == Follower && s.CurrentTerm == 0)
		out := n.Tick(deadline)
		s02check(t, "at the deadline the node becomes a candidate", n.Role == Candidate)
		s02check(t, "the term is incremented and persisted with a vote for itself", s.CurrentTerm == 1 && s.VotedFor == 2 && s.Writes >= 1)
		s02check(t, "one RequestVote per peer, carrying the new term", s02allOfKind(out, RequestVote, 2, 1, 4))
		s02check(t, "RequestVote carries the candidate's last log index and term", len(out) > 0 && out[0].PrevLogIndex == 0 && out[0].PrevLogTerm == 0)
		s02check(t, "the candidate counts its own vote", n.Votes() == 1 && n.ElectionsStarted == 1)
		s02check(t, "the election timer is re-armed for the next round", n.ElectionDeadline > deadline)
		second := n.ElectionDeadline
		again := n.Tick(second)
		s02check(t, "a candidate whose timer expires starts a fresh election in a higher term", s.CurrentTerm == 2 && n.Role == Candidate && s02allOfKind(again, RequestVote, 2, 2, 4) && n.Votes() == 1)
	})

	s02scenario(t, "votes are counted to a majority, stale and duplicate replies are ignored", func(t *testing.T) {
		s := NewStorage()
		n := s02standalone(0, s, 2)
		n.StartElection(1)
		s02check(t, "one stale reply (older term) does nothing", len(n.Handle(NewVoteReply(1, 0, 0, true), 2)) == 0 && n.Votes() == 1 && n.Role == Candidate)
		n.Handle(NewVoteReply(1, 0, 1, true), 3)
		n.Handle(NewVoteReply(1, 0, 1, true), 4)
		s02check(t, "a duplicate grant from the same peer counts once", n.Votes() == 2 && n.Role == Candidate)
		n.Handle(NewVoteReply(3, 0, 1, false), 5)
		s02check(t, "a denial does not count", n.Votes() == 2 && n.Role == Candidate)
		out := n.Handle(NewVoteReply(2, 0, 1, true), 6)
		s02check(t, "the third vote of five makes a leader", n.Role == Leader && n.LeaderID == 0)
		s02check(t, "a new leader immediately sends AppendEntries to every peer", s02allOfKind(out, AppendEntries, 0, 1, 4))
		s02check(t, "the leader's term is unchanged by winning", s.CurrentTerm == 1)
		s02check(t, "a late grant after winning is ignored", len(n.Handle(NewVoteReply(4, 0, 1, true), 7)) == 0 && n.Role == Leader)
	})

	s02scenario(t, "RequestVote receiver rules", func(t *testing.T) {
		s := NewStorage()
		s.Save(3, -1)
		v := s02standalone(1, s, 8)
		deny := v.Handle(NewRequestVote(0, 1, 2, 0, 0), 10)[0]
		s02check(t, "a lower-term candidate is denied with the voter's own term", deny.Kind == RequestVoteReply && !deny.Success && deny.Term == 3 && s.VotedFor == -1)
		before := v.ElectionDeadline
		writes := s.Writes
		grant := v.Handle(NewRequestVote(0, 1, 3, 0, 0), 20)[0]
		s02check(t, "first candidate of the term is granted", grant.Success && grant.To == 0 && grant.Term == 3)
		s02check(t, "the vote is persisted", s.VotedFor == 0 && s.Writes == writes+1)
		s02check(t, "granting a vote resets the election timer", v.ElectionDeadline != before && v.ElectionDeadline >= 170)
		other := v.Handle(NewRequestVote(2, 1, 3, 0, 0), 21)[0]
		s02check(t, "a second candidate in the same term is denied", !other.Success && s.VotedFor == 0)
		same := v.Handle(NewRequestVote(0, 1, 3, 0, 0), 22)[0]
		s02check(t, "the same candidate asking again is granted again (retries are safe)", same.Success)
		higher := v.Handle(NewRequestVote(4, 1, 5, 0, 0), 23)[0]
		s02check(t, "a higher-term candidate moves the voter to that term and gets the vote", higher.Success && s.CurrentTerm == 5 && s.VotedFor == 4 && v.Role == Follower)
	})

	s02scenario(t, "the up-to-date rule guards the vote (section 5.4.1)", func(t *testing.T) {
		s := NewStorage()
		s.Append(LogEntry{1, "PUT a 1"})
		s.Append(LogEntry{2, "PUT a 2"})
		v := s02standalone(1, s, 9)
		r1 := v.Handle(NewRequestVote(0, 1, 3, 5, 1), 10)[0]
		s02check(t, "a candidate with a longer but older-term log is denied", !r1.Success && s.VotedFor == -1 && s.CurrentTerm == 3)
		r2 := v.Handle(NewRequestVote(0, 1, 3, 1, 2), 11)[0]
		s02check(t, "a candidate with the same last term but a shorter log is denied", !r2.Success)
		r3 := v.Handle(NewRequestVote(0, 1, 3, 2, 2), 12)[0]
		s02check(t, "a candidate whose log is at least as up-to-date is granted", r3.Success && s.VotedFor == 0)
	})

	s02scenario(t, "AppendEntries from a leader: followers obey, candidates step down, stale leaders are refused", func(t *testing.T) {
		s := NewStorage()
		f := s02standalone(3, s, 11)
		before := f.ElectionDeadline
		ok := f.Handle(NewAppendEntries(0, 3, 1, 0, 0, nil, 0), 50)[0]
		s02check(t, "a follower adopts the leader and the term", f.LeaderID == 0 && s.CurrentTerm == 1 && f.Role == Follower)
		s02check(t, "the reply is an APPEND_ENTRIES_REPLY carrying the follower's term", ok.Kind == AppendEntriesReply && ok.Term == 1 && ok.To == 0)
		s02check(t, "a heartbeat resets the election timer", f.ElectionDeadline != before && f.ElectionDeadline >= 200)
		stale := f.Handle(NewAppendEntries(4, 3, 0, 0, 0, nil, 0), 51)[0]
		s02check(t, "a lower-term AppendEntries is refused and the leader is unchanged", !stale.Success && stale.Term == 1 && f.LeaderID == 0)

		c := s02standalone(2, NewStorage(), 12)
		c.StartElection(1)
		c.Handle(NewAppendEntries(0, 2, 1, 0, 0, nil, 0), 2)
		s02check(t, "a candidate that hears a leader of its term steps down", c.Role == Follower && c.LeaderID == 0 && c.CurrentTerm() == 1)
		s02check(t, "the stepped-down candidate ignores late vote grants", len(c.Handle(NewVoteReply(1, 2, 1, true), 3)) == 0 && c.Role == Follower)
	})

	s02scenario(t, "a leader steps down on a higher term and sends heartbeats on its interval", func(t *testing.T) {
		s := NewStorage()
		l := s02electedLeader(s, 13)
		s02check(t, "precondition: leader of term 1", l.Role == Leader && s.CurrentTerm == 1)
		hb := l.NextHeartbeat
		s02check(t, "no heartbeat before the interval elapses", len(l.Tick(hb-1)) == 0)
		beat := l.Tick(hb)
		s02check(t, "heartbeats go to every peer when the interval elapses", s02allOfKind(beat, AppendEntries, 0, 1, 4) && l.NextHeartbeat == hb+DefaultConfig.HeartbeatInterval)
		s02check(t, "heartbeats carry the leader's commit index", len(beat) > 0 && beat[0].LeaderCommit == l.CommitIndex)
		out := l.Handle(NewAppendReply(1, 0, 4, false, 0), hb+1)
		s02check(t, "a reply with a higher term dethrones the leader", l.Role == Follower && s.CurrentTerm == 4 && s.VotedFor == -1 && l.LeaderID == -1)
		s02check(t, "the dethroned leader re-arms an election timer", l.ElectionDeadline >= hb+1+150)
		s02check(t, "the dethroned leader sends nothing in response", len(out) == 0)

		l2 := s02electedLeader(NewStorage(), 14)
		l2.Handle(NewRequestVote(3, 0, 2, 1, 1), 10)
		s02check(t, "a higher-term RequestVote also dethrones a leader and gets the vote", l2.Role == Follower && l2.CurrentTerm() == 2 && l2.VotedFor() == 3)
	})

	s02scenario(t, "a single leader is elected on a quiet 5-node network (seed 7)", func(t *testing.T) {
		c := NewCluster(5, 7, 0.0, 5)
		at := c.RunUntilStableLeader(2000)
		s02check(t, "stable leader elected", at > 0 && c.StableLeader() != nil)
		if c.StableLeader() == nil {
			return
		}
		s02check(t, "elected after at least one minimum timeout", at >= DefaultConfig.ElectionTimeoutMin)
		s02check(t, "exactly one leader", len(c.Leaders()) == 1)
		s02check(t, "every node agrees on the leader's term", s02all(c.Nodes, func(n *RaftNode) bool { return n.CurrentTerm() == c.StableLeader().CurrentTerm() }))
		s02check(t, "followers know who leads", s02all(c.Nodes, func(n *RaftNode) bool { return n.Role == Leader || n.LeaderID == c.StableLeader().id }))
		allVoted := true
		for _, s := range c.Storages {
			if s.VotedFor < 0 || s.Writes < 1 {
				allVoted = false
			}
		}
		s02check(t, "every node persisted a vote for this term", allVoted)
		term := c.MaxTerm()
		c.Run(2000)
		s02check(t, "heartbeats keep the term stable for 2000 ticks", c.MaxTerm() == term && len(c.Leaders()) == 1)
		s02check(t, "heartbeats flowed", c.Network.Delivered > 100)
	})

	s02scenario(t, "same seed, same trace", func(t *testing.T) {
		d1, d2, d3 := s02digest(NewCluster(5, 42, 0.1, 8)), s02digest(NewCluster(5, 42, 0.1, 8)), s02digest(NewCluster(5, 43, 0.1, 8))
		s02check(t, "same seed gives an identical run (terms, roles, leaders, message counts at every 50th tick)", d1 == d2 && strings.Contains(d1, "LEADER"))
		s02check(t, "different seed gives a different run", d1 != d3)
	})

	s02scenario(t, "Election Safety over many lossy seeds: at most one leader per term", func(t *testing.T) {
		electedSeeds := 0
		safe := true
		for seed := int64(1); seed <= 40; seed++ {
			s := NewCluster(5, seed, 0.2, 10)
			leaderOfTerm := map[int64]int{}
			at := -1
			for tick := 0; tick < 2500; tick++ {
				s.Tick()
				for _, n := range s.Nodes {
					if n.Role != Leader {
						continue
					}
					if prev, ok := leaderOfTerm[n.CurrentTerm()]; !ok {
						leaderOfTerm[n.CurrentTerm()] = n.id
					} else if prev != n.id {
						safe = false
					}
				}
				if at < 0 && s.StableLeader() != nil {
					at = s.Now()
				}
			}
			if at > 0 {
				electedSeeds++
			}
		}
		s02check(t, "40 lossy seeds all elect a leader within 2500 ticks", electedSeeds == 40)
		s02check(t, "never two leaders in one term across 40 seeds x 2500 ticks", safe)
		threeNodeSeeds := 0
		for seed := int64(100); seed < 120; seed++ {
			s := NewCluster(3, seed, 0.3, 10)
			if s.RunUntilStableLeader(4000) > 0 {
				threeNodeSeeds++
			}
		}
		s02check(t, "3-node clusters with 30% drops still elect", threeNodeSeeds == 20)
	})

	s02scenario(t, "partition: the majority moves on, the minority leader steps down on heal", func(t *testing.T) {
		p := NewCluster(5, 7, 0.0, 5)
		p.RunUntilStableLeader(2000)
		old := p.StableLeader()
		if old == nil {
			t.Fatalf("precondition: no stable leader on seed 7")
		}
		oldTerm := old.CurrentTerm()
		p.Partition([]int{old.id, (old.id + 1) % 5})
		p.Run(800)
		s02check(t, "stale leader keeps leading its minority side", old.Role == Leader && old.CurrentTerm() == oldTerm)
		s02check(t, "majority side elected a leader with a higher term", len(p.Leaders()) == 2 && p.MaxTerm() > oldTerm)
		p.Heal()
		healed := p.RunUntilStableLeader(2000)
		s02check(t, "cluster converges after heal", healed > 0)
		s02check(t, "old leader stepped down", old.Role == Follower)
		s02check(t, "the higher term won", p.StableLeader() != nil && p.StableLeader().CurrentTerm() > oldTerm && p.StableLeader() != old)
		noNull := true
		for _, id := range p.LeadersByTerm {
			if id < 0 {
				noNull = false
			}
		}
		s02check(t, "a minority of two can never elect: no term in the trace has a 2-node leader", noNull)
	})

	s02scenario(t, "a crashed leader is replaced; votedFor survives a restart", func(t *testing.T) {
		c := NewCluster(5, 21, 0.0, 5)
		c.RunUntilStableLeader(2000)
		dead := c.StableLeader()
		if dead == nil {
			t.Fatalf("precondition: no stable leader on seed 21")
		}
		t0 := dead.CurrentTerm()
		c.Crash(dead.id)
		at := c.RunUntilStableLeader(2000)
		s02check(t, "a new leader is elected after the leader crashes", at > 0 && c.StableLeader() != nil && c.StableLeader().id != dead.id && c.StableLeader().CurrentTerm() > t0)
		back := c.Restart(dead.id)
		s02check(t, "a restarted node comes back as a follower with its persisted term", back.Role == Follower && back.CurrentTerm() == c.Storages[dead.id].CurrentTerm)
		c.Run(600)
		s02check(t, "the restarted node joins the current term and follows the leader", c.StableLeader() != nil && back.CurrentTerm() == c.StableLeader().CurrentTerm() && back.LeaderID == c.StableLeader().id)

		durable := NewStorage()
		n1 := s02standalone(1, durable, 31)
		first := n1.Handle(NewRequestVote(0, 1, 1, 0, 0), 10)[0].Success
		n1b := s02standalone(1, durable, 32)
		second := n1b.Handle(NewRequestVote(2, 1, 1, 0, 0), 12)[0].Success
		s02check(t, "after a crash and restart the node still refuses a second vote in the same term", first && !second && durable.VotedFor == 0)
	})
}
