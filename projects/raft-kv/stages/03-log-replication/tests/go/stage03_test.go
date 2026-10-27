// Stage 03 tests for projects/raft-kv (Go): log replication (AppendEntries consistency check, nextIndex/matchIndex,
// follower truncation, Log Matching). Run by the grader after stage01_test.go and stage02_test.go: go test ./...
// Port of Stage03Test.java: same checks, same names, same order. Helpers are prefixed s03.
package main

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func s03check(t *testing.T, name string, condition bool) {
	t.Helper()
	if !condition {
		t.Errorf("FAIL: %s", name)
	}
}

func s03scenario(t *testing.T, name string, body func(t *testing.T)) {
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

func s03peersOf(id, size int) []int {
	ps := make([]int, 0, size-1)
	for j := 0; j < size; j++ {
		if j != id {
			ps = append(ps, j)
		}
	}
	return ps
}

func s03standalone(id int, storage *Storage, seed int64) *RaftNode {
	return NewRaftNode(id, s03peersOf(id, 5), storage, NewRng(seed), DefaultConfig, 0, TracerNone)
}

// s03logged builds a Storage from entries written as "t1:PUT a 1".
func s03logged(entries ...string) *Storage {
	s := NewStorage()
	for _, e := range entries {
		colon := strings.IndexByte(e, ':')
		term, err := strconv.ParseInt(e[1:colon], 10, 64)
		if err != nil {
			panic(fmt.Sprintf("bad test entry %q: %v", e, err))
		}
		s.Append(LogEntry{term, e[colon+1:]})
	}
	return s
}

func s03electedLeader(storage *Storage, seed int64) *RaftNode {
	n := s03standalone(0, storage, seed)
	n.StartElection(1)
	n.Handle(NewVoteReply(1, 0, n.CurrentTerm(), true), 2)
	n.Handle(NewVoteReply(2, 0, n.CurrentTerm(), true), 3)
	return n
}

func s03logMatchingHolds(c *Cluster) bool {
	for i := 0; i < c.Size; i++ {
		for j := i + 1; j < c.Size; j++ {
			if CheckLogMatching(c.Node(i).Log(), c.Node(j).Log(), i, j) != nil {
				return false
			}
		}
	}
	return true
}

func s03logHas(log []LogEntry, command string) bool {
	for _, e := range log {
		if e.Command == command {
			return true
		}
	}
	return false
}

func s03everyLogContains(c *Cluster, command string) bool {
	for _, n := range c.Nodes {
		if !s03logHas(n.Log(), command) {
			return false
		}
	}
	return true
}

func s03noLogContains(c *Cluster, command string) bool {
	for _, n := range c.Nodes {
		if s03logHas(n.Log(), command) {
			return false
		}
	}
	return true
}

func s03allPeers(l *RaftNode, pred func(p int) bool) bool {
	for _, p := range l.Peers {
		if !pred(p) {
			return false
		}
	}
	return true
}

func s03allMsgs(ms []Message, pred func(m Message) bool) bool {
	for _, m := range ms {
		if !pred(m) {
			return false
		}
	}
	return true
}

func s03allNodes(nodes []*RaftNode, pred func(n *RaftNode) bool) bool {
	for _, n := range nodes {
		if !pred(n) {
			return false
		}
	}
	return true
}

func TestStage03(t *testing.T) {
	s03scenario(t, "propose appends to the leader's log only", func(t *testing.T) {
		fs := NewStorage()
		follower := s03standalone(1, fs, 1)
		s03check(t, "a follower refuses proposals", follower.Propose("PUT a 1") == -1 && fs.LastIndex() == 0)
		ls := NewStorage()
		leader := s03electedLeader(ls, 2)
		base := leader.LastLogIndex()
		i1 := leader.Propose("PUT a 1")
		i2 := leader.Propose("PUT b 2")
		s03check(t, "propose returns consecutive indexes", i1 == base+1 && i2 == base+2 && leader.LastLogIndex() == i2)
		s03check(t, "entries carry the leader's current term and the command", ls.TermAt(i2) == 1 && ls.Log[i2].Command == "PUT b 2")
		s03check(t, "propose persists (stable writes increased)", ls.Writes >= 3)
	})

	s03scenario(t, "a new leader initialises nextIndex/matchIndex and ships from nextIndex", func(t *testing.T) {
		ls := s03logged("t1:PUT a 1", "t1:PUT b 2")
		ls.Save(1, -1)
		leader := s03standalone(0, ls, 3)
		leader.StartElection(5)
		leader.Handle(NewVoteReply(1, 0, 2, true), 6)
		first := leader.Handle(NewVoteReply(2, 0, 2, true), 7)
		s03check(t, "nextIndex starts at (last index at election) + 1 for every peer", s03allPeers(leader, func(p int) bool { return leader.NextIndex[p] == 3 }))
		s03check(t, "matchIndex starts at 0 for every peer", s03allPeers(leader, func(p int) bool { return leader.MatchIndex[p] == 0 }))
		s03check(t, "the first AppendEntries names the entry before nextIndex as (prevLogIndex, prevLogTerm)", len(first) == 4 && s03allMsgs(first, func(m Message) bool { return m.PrevLogIndex == 2 && m.PrevLogTerm == 1 }))
		s03check(t, "the first AppendEntries ships exactly log[nextIndex..last]", s03allMsgs(first, func(m Message) bool { return len(m.Entries) == leader.LastLogIndex()-2 }))
		leader.Propose("PUT c 3")
		ae := leader.AppendEntriesFor(1)
		s03check(t, "appendEntriesFor ships everything from nextIndex, including the new proposal", len(ae.Entries) > 0 && ae.Entries[len(ae.Entries)-1].Command == "PUT c 3" && ae.PrevLogIndex == 2 && ae.Term == 2)
		leader.NextIndex[1] = 1
		back := leader.AppendEntriesFor(1)
		s03check(t, "with nextIndex 1 the prev is the sentinel (0, t0) and the whole log ships", back.PrevLogIndex == 0 && back.PrevLogTerm == 0 && len(back.Entries) == leader.LastLogIndex())
	})

	s03scenario(t, "AppendEntries consistency check (receiver rule 2)", func(t *testing.T) {
		fs := s03logged("t1:PUT a 1", "t1:PUT b 2", "t2:PUT c 3")
		f := s03standalone(1, fs, 4)
		r1 := f.Handle(NewAppendEntries(0, 1, 3, 5, 2, []LogEntry{{3, "PUT d 4"}}, 0), 10)[0]
		s03check(t, "prevLogIndex beyond the follower's log is refused", !r1.Success && f.ConsistencyRejections == 1)
		s03check(t, "a refused AppendEntries still makes the sender the leader and adopts its term", f.LeaderID == 0 && f.CurrentTerm() == 3)
		r2 := f.Handle(NewAppendEntries(0, 1, 3, 3, 1, []LogEntry{{3, "PUT d 4"}}, 0), 11)[0]
		s03check(t, "prevLogIndex present but with a different term is refused", !r2.Success && f.ConsistencyRejections == 2)
		s03check(t, "a refusal never changes the follower's log", fs.LastIndex() == 3 && fs.TermAt(3) == 2)
		r3 := f.Handle(NewAppendEntries(0, 1, 3, 3, 2, []LogEntry{{3, "PUT d 4"}}, 0), 12)[0]
		s03check(t, "a matching (prevLogIndex, prevLogTerm) is accepted", r3.Success && fs.LastIndex() == 4 && fs.TermAt(4) == 3)
		s03check(t, "a successful reply reports the index of the last entry now known to match", r3.MatchIndex == 4)
		hb := f.Handle(NewAppendEntries(0, 1, 3, 4, 3, nil, 0), 13)[0]
		s03check(t, "a heartbeat with a matching prev succeeds with matchIndex = prevLogIndex and leaves the log alone", hb.Success && hb.MatchIndex == 4 && fs.LastIndex() == 4)
	})

	s03scenario(t, "conflicting entries are truncated, duplicates are skipped (receiver rules 3 and 4)", func(t *testing.T) {
		fs := s03logged("t1:PUT a 1", "t1:PUT b 2", "t2:PUT c 3")
		f := s03standalone(2, fs, 5)
		r := f.Handle(NewAppendEntries(0, 2, 3, 1, 1, []LogEntry{{3, "PUT x 9"}}, 0), 10)[0]
		s03check(t, "a conflicting entry and everything after it are deleted, then the leader's entry is appended", r.Success && fs.LastIndex() == 2 && fs.Describe() == "[t1:PUT a 1, t3:PUT x 9]")
		writes := fs.Writes
		dup := f.Handle(NewAppendEntries(0, 2, 3, 1, 1, []LogEntry{{3, "PUT x 9"}}, 0), 11)[0]
		s03check(t, "re-delivering the same entries changes nothing (idempotent)", dup.Success && dup.MatchIndex == 2 && fs.LastIndex() == 2 && fs.Writes == writes)
		mixed := f.Handle(NewAppendEntries(0, 2, 3, 1, 1, []LogEntry{{3, "PUT x 9"}, {3, "PUT y 8"}}, 0), 12)[0]
		s03check(t, "a batch that overlaps the log appends only the new tail", mixed.Success && mixed.MatchIndex == 3 && fs.Describe() == "[t1:PUT a 1, t3:PUT x 9, t3:PUT y 8]")
		older := f.Handle(NewAppendEntries(0, 2, 3, 0, 0, []LogEntry{{1, "PUT a 1"}}, 0), 13)[0]
		s03check(t, "an old, shorter batch that matches a prefix does not truncate the newer tail", older.Success && older.MatchIndex == 1 && fs.LastIndex() == 3)
	})

	s03scenario(t, "the leader walks nextIndex back on refusal and forward on success", func(t *testing.T) {
		ls := s03logged("t1:PUT a 1", "t1:PUT b 2", "t1:PUT c 3")
		ls.Save(1, -1)
		l := s03electedLeader(ls, 6)
		term := l.CurrentTerm()
		start := l.NextIndex[1]
		retry := l.Handle(NewAppendReply(1, 0, term, false, 0), 10)
		s03check(t, "a refusal decrements nextIndex by one", l.NextIndex[1] == start-1)
		s03check(t, "and immediately resends with the earlier prev", len(retry) == 1 && retry[0].Kind == AppendEntries && retry[0].To == 1 && retry[0].PrevLogIndex == start-2)
		for i := 0; i < 20; i++ {
			l.Handle(NewAppendReply(1, 0, term, false, 0), 11+i)
		}
		s03check(t, "nextIndex never drops below 1", l.NextIndex[1] == 1)
		full := l.AppendEntriesFor(1)
		s03check(t, "at nextIndex 1 the whole log is shipped after the sentinel", full.PrevLogIndex == 0 && len(full.Entries) == l.LastLogIndex())
		l.Handle(NewAppendReply(1, 0, term, true, 3), 40)
		s03check(t, "a success sets matchIndex and nextIndex = matchIndex + 1", l.MatchIndex[1] == 3 && l.NextIndex[1] == 4)
		l.Handle(NewAppendReply(1, 0, term, true, 2), 41)
		s03check(t, "an older success (lower matchIndex) never moves matchIndex backwards", l.MatchIndex[1] == 3 && l.NextIndex[1] == 4)
		s03check(t, "replicatedCount counts the leader plus peers at or beyond the index", l.ReplicatedCount(3) == 2 && l.ReplicatedCount(4) == 1)
		l.Handle(NewAppendReply(2, 0, term-1, true, 3), 42)
		s03check(t, "a reply from an older term is ignored", l.MatchIndex[2] == 0)
		f := s03standalone(3, NewStorage(), 7)
		s03check(t, "a non-leader ignores AppendEntries replies", len(f.Handle(NewAppendReply(1, 3, 0, true, 5), 1)) == 0)
	})

	s03scenario(t, "three commands replicate to every log on a quiet 5-node cluster", func(t *testing.T) {
		c := NewCluster(5, 7, 0.0, 5)
		c.RunUntilStableLeader(2000)
		i1 := c.Propose("PUT alice 100")
		i2 := c.Propose("PUT bob 50")
		i3 := c.Propose("PUT alice 75")
		s03check(t, "proposals got consecutive indexes", i1 > 0 && i2 == i1+1 && i3 == i2+1)
		if i1 <= 0 {
			return
		}
		at := c.RunUntilLogsConverged(1000)
		s03check(t, "all five logs converge", at > 0 && c.LogsConverged())
		s03check(t, "every log holds the three commands at the leader's indexes", s03allNodes(c.Nodes, func(n *RaftNode) bool {
			return n.LastLogIndex() >= i3 && n.Log()[i1].Command == "PUT alice 100" && n.Log()[i3].Command == "PUT alice 75"
		}))
		s03check(t, "the entries carry the leader's term", c.StableLeader() != nil && s03allNodes(c.Nodes, func(n *RaftNode) bool {
			return n.LastLogIndex() >= i2 && n.Log()[i2].Term == c.StableLeader().CurrentTerm()
		}))
		c.Run(20) // let the last replies reach the leader
		s03check(t, "the leader knows every follower matches through the last index", c.StableLeader() != nil && c.StableLeader().ReplicatedCount(i3) == 5)
		s03check(t, "Log Matching holds", s03logMatchingHolds(c))
	})

	s03scenario(t, "a lagging follower catches up through nextIndex back-off", func(t *testing.T) {
		c := NewCluster(5, 9, 0.0, 5)
		c.RunUntilStableLeader(2000)
		leader := c.StableLeader()
		if leader == nil {
			t.Fatalf("precondition: no stable leader on seed 9")
		}
		lag := (leader.id + 2) % 5
		c.Crash(lag)
		for k := 1; k <= 4; k++ {
			c.Propose(fmt.Sprintf("PUT k%d v%d", k, k))
		}
		c.Run(300)
		none := true
		for _, e := range c.Node(lag).Log() {
			if strings.HasPrefix(e.Command, "PUT k") {
				none = false
			}
		}
		s03check(t, "the crashed follower has none of the new entries", none)
		c.Restart(lag)
		at := c.RunUntilLogsConverged(1500)
		s03check(t, "after restart its log converges with the leader's", at > 0 && slices.Equal(c.Node(lag).Log(), leader.Log()))
		s03check(t, "Log Matching holds after catch-up", s03logMatchingHolds(c))
	})

	s03scenario(t, "a stale leader's unreplicated entries are overwritten after a partition heals", func(t *testing.T) {
		c := NewCluster(5, 7, 0.0, 5)
		c.RunUntilStableLeader(2000)
		c.Propose("PUT alice 100")
		c.RunUntilLogsConverged(1000)
		old := c.StableLeader()
		if old == nil {
			t.Fatalf("precondition: no stable leader on seed 7")
		}
		follower := (old.id + 1) % 5
		c.Partition([]int{old.id, follower})
		staleIdx := c.ProposeTo(old.id, "PUT alice 999")
		c.ProposeTo(old.id, "PUT carol 1")
		c.Run(600)
		s03check(t, "the stale leader accepted writes it can never replicate to a majority", staleIdx > 0 && old.Log()[staleIdx].Command == "PUT alice 999")
		var fresh *RaftNode
		for _, l := range c.Leaders() {
			if fresh == nil || l.CurrentTerm() > fresh.CurrentTerm() {
				fresh = l
			}
		}
		s03check(t, "the majority elected a new leader in a higher term", fresh != nil && fresh != old && fresh.CurrentTerm() > old.CurrentTerm())
		if fresh == nil {
			return
		}
		c.ProposeTo(fresh.id, "PUT dave 7")
		c.Run(200)
		s03check(t, "the two sides diverge at the stale index", !slices.Equal(old.Log(), fresh.Log()))
		c.Heal()
		c.RunUntilStableLeader(2000)
		at := c.RunUntilLogsConverged(2000)
		s03check(t, "after heal every log converges", at > 0)
		s03check(t, "the stale writes are gone from every log", s03noLogContains(c, "PUT alice 999") && s03noLogContains(c, "PUT carol 1"))
		s03check(t, "the majority's write survives everywhere", s03everyLogContains(c, "PUT dave 7") && s03everyLogContains(c, "PUT alice 100"))
		s03check(t, "the old leader is now a follower whose log was truncated", old.Role == Follower && c.StableLeader() != nil && slices.Equal(old.Log(), c.StableLeader().Log()))
		s03check(t, "Log Matching holds after the heal", s03logMatchingHolds(c))
	})

	s03scenario(t, "Log Matching and convergence hold across lossy seeds with a leader crash", func(t *testing.T) {
		converged := 0
		for seed := int64(1); seed <= 12; seed++ {
			r := NewCluster(5, seed, 0.2, 10)
			r.RunUntilStableLeader(3000)
			for k := 1; k <= 4; k++ {
				for attempt := 0; attempt < 300 && r.Propose(fmt.Sprintf("PUT k%d v%d", k, k)) < 0; attempt++ {
					r.Run(10)
				}
				r.Run(30)
			}
			victim := r.StableLeader()
			if victim == nil {
				victim = r.Node(0)
			}
			r.Crash(victim.id)
			r.RunUntilStableLeader(3000)
			for k := 5; k <= 8; k++ {
				for attempt := 0; attempt < 300 && r.Propose(fmt.Sprintf("PUT k%d v%d", k, k)) < 0; attempt++ {
					r.Run(10)
				}
				r.Run(30)
			}
			r.Restart(victim.id)
			r.RunUntilStableLeader(3000)
			if r.RunUntilLogsConverged(3000) > 0 && s03logMatchingHolds(r) && s03everyLogContains(r, "PUT k8 v8") {
				converged++
			}
		}
		s03check(t, "12 lossy seeds: every log converged on the surviving writes with Log Matching intact", converged == 12)
	})
}
