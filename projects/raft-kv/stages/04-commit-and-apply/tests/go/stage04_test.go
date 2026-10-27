// Stage 04 tests for projects/raft-kv (Go): commitIndex by majority with the current-term rule, the KV state machine,
// client PUT/GET through the leader, and a linearizability check on the recorded history.
// Run by the grader after stage01_test.go, stage02_test.go, and stage03_test.go: go test ./...
// Port of Stage04Test.java: same checks, same names, same order. Helpers are prefixed s04.
package main

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"testing"
)

func s04check(t *testing.T, name string, condition bool) {
	t.Helper()
	if !condition {
		t.Errorf("FAIL: %s", name)
	}
}

// s04checkErr expects err to wrap want: KVStore.Apply returns ErrBadCommand where the Java throws IllegalArgumentException.
func s04checkErr(t *testing.T, name string, err, want error) {
	t.Helper()
	if err == nil {
		t.Errorf("FAIL: %s (no error)", name)
		return
	}
	if !errors.Is(err, want) {
		t.Errorf("FAIL: %s (wrong error: %v)", name, err)
	}
}

func s04scenario(t *testing.T, name string, body func(t *testing.T)) {
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

func s04peersOf(id, size int) []int {
	ps := make([]int, 0, size-1)
	for j := 0; j < size; j++ {
		if j != id {
			ps = append(ps, j)
		}
	}
	return ps
}

func s04standalone(id int, storage *Storage, seed int64) *RaftNode {
	return NewRaftNode(id, s04peersOf(id, 5), storage, NewRng(seed), DefaultConfig, 0, TracerNone)
}

func s04electedLeader(storage *Storage, seed int64) *RaftNode {
	n := s04standalone(0, storage, seed)
	n.StartElection(1)
	n.Handle(NewVoteReply(1, 0, n.CurrentTerm(), true), 2)
	n.Handle(NewVoteReply(2, 0, n.CurrentTerm(), true), 3)
	return n
}

// s04apply folds Apply's (result, error) into the result; an error becomes a marker no check expects.
func s04apply(kv *KVStore, command string) string {
	r, err := kv.Apply(command)
	if err != nil {
		return "<error>"
	}
	return r
}

// s04get mirrors Java's kv.get(key): the value, or "<null>" when the key was never PUT.
func s04get(kv *KVStore, key string) string {
	v, ok := kv.Get(key)
	if !ok {
		return "<null>"
	}
	return v
}

func s04appliedPrefixMatchesLog(n *RaftNode) bool {
	if n.LastApplied > n.CommitIndex || n.CommitIndex > n.LastLogIndex() {
		return false
	}
	if len(n.StateMachine.AppliedCommands) != n.LastApplied {
		return false
	}
	for k := 1; k <= n.LastApplied; k++ {
		if n.StateMachine.AppliedCommands[k-1] != n.Log()[k].Command {
			return false
		}
	}
	return true
}

func s04respectsRealTime(h *History, order []int) bool {
	for a := 0; a < len(order); a++ {
		for b := a + 1; b < len(order); b++ {
			if h.Op(order[b]).Precedes(h.Op(order[a])) {
				return false
			}
		}
	}
	return len(order) == h.Size()
}

func s04allNodes(nodes []*RaftNode, pred func(n *RaftNode) bool) bool {
	for _, n := range nodes {
		if !pred(n) {
			return false
		}
	}
	return true
}

func s04firstGet(ops []Op) (Op, bool) {
	for _, o := range ops {
		if o.Kind == GET {
			return o, true
		}
	}
	return Op{}, false
}

// s04history folds Cluster.History()'s error into a nil history, which no check expects.
func s04history(c *Cluster) *History {
	h, err := c.History()
	if err != nil {
		return nil
	}
	return h
}

func s04linearizable(c *Cluster) bool {
	h := s04history(c)
	return h != nil && NewChecker().Check(h).OK
}

func s04mustSubmit(t *testing.T, c *Cluster, client, command string, maxAttempts int) {
	t.Helper()
	if _, err := c.SubmitUntilAccepted(client, command, maxAttempts); err != nil {
		t.Fatalf("submitUntilAccepted(%s): %v", command, err)
	}
}

// s04samePointer reports whether two maps are the same map (Java's `==` on the returned state).
func s04samePointer(a, b map[string]string) bool {
	return reflect.ValueOf(a).Pointer() == reflect.ValueOf(b).Pointer()
}

func TestStage04(t *testing.T) {
	s04scenario(t, "KVStore applies PUT, GET, NOOP and rejects garbage", func(t *testing.T) {
		kv := NewKVStore()
		s04check(t, "PUT returns OK and stores", s04apply(kv, "PUT balance:alice 100") == "OK" && s04get(kv, "balance:alice") == "100")
		s04check(t, "GET returns the stored value", s04apply(kv, "GET balance:alice") == "100")
		s04check(t, "GET of an absent key returns the empty string", s04apply(kv, "GET nobody") == "" && s04get(kv, "nobody") == "<null>")
		s04check(t, "NOOP returns the empty string and changes nothing", s04apply(kv, "NOOP") == "" && len(kv.Data) == 1)
		s04check(t, "a PUT value may contain spaces", s04apply(kv, "PUT note hello world") == "OK" && s04get(kv, "note") == "hello world")
		s04check(t, "PUT overwrites", s04apply(kv, "PUT balance:alice 75") == "OK" && s04get(kv, "balance:alice") == "75")
		s04check(t, "applied commands and results are recorded in order", kv.Applied() == 6 && kv.AppliedCommands[1] == "GET balance:alice" && kv.Results[1] == "100" && kv.Results[2] == "")
		_, err := kv.Apply("PUT lonely")
		s04checkErr(t, "PUT without a value is rejected", err, ErrBadCommand)
		_, err = kv.Apply("GET a b")
		s04checkErr(t, "GET with extra arguments is rejected", err, ErrBadCommand)
		_, err = kv.Apply("TRANSFER a b 5")
		s04checkErr(t, "unknown commands are rejected", err, ErrBadCommand)
		s04check(t, "a rejected command is not recorded", kv.Applied() == 6)
	})

	s04scenario(t, "a leader commits an entry once a majority holds it, then applies in order", func(t *testing.T) {
		ls := NewStorage()
		l := s04electedLeader(ls, 1)
		term := l.CurrentTerm()
		i1 := l.Propose("PUT a 1")
		i2 := l.Propose("PUT a 2")
		l.Handle(NewAppendReply(1, 0, term, true, i2), 10)
		s04check(t, "two of five replicas (leader + one) is not a majority: nothing commits", l.CommitIndex == 0 && l.LastApplied == 0 && l.StateMachine.Applied() == 0)
		l.Handle(NewAppendReply(2, 0, term, true, i1), 11)
		s04check(t, "three of five hold index i1: commitIndex advances to i1, not i2", l.CommitIndex == i1 && l.LastApplied == i1)
		s04check(t, "the state machine applied everything through i1 in log order", s04appliedPrefixMatchesLog(l) && s04get(l.StateMachine, "a") == "1")
		l.Handle(NewAppendReply(3, 0, term, true, i2), 12)
		s04check(t, "a third holder of i2 commits it", l.CommitIndex == i2 && s04get(l.StateMachine, "a") == "2" && s04appliedPrefixMatchesLog(l))
		l.Handle(NewAppendReply(4, 0, term, true, i1), 13)
		s04check(t, "a later, lower matchIndex never moves commitIndex backwards", l.CommitIndex == i2)
		s04check(t, "lastApplied never exceeds commitIndex", l.LastApplied <= l.CommitIndex)
	})

	s04scenario(t, "only current-term entries are committed by counting replicas (section 5.4.2)", func(t *testing.T) {
		s := NewStorage()
		s.Append(LogEntry{2, "PUT x 1"})
		s.Append(LogEntry{2, "PUT x 2"})
		s.Save(4, 0)
		l := s04standalone(0, s, 2)
		l.StartElection(1)
		l.Handle(NewVoteReply(1, 0, l.CurrentTerm(), true), 2)
		l.Handle(NewVoteReply(2, 0, l.CurrentTerm(), true), 3)
		s04check(t, "precondition: leader of term 5 with two term-2 entries", l.Role == Leader && l.CurrentTerm() == 5 && s.TermAt(2) == 2)
		l.Handle(NewAppendReply(1, 0, 5, true, 2), 4)
		l.Handle(NewAppendReply(2, 0, 5, true, 2), 5)
		s04check(t, "term-2 entries on 3/5 replicas do NOT commit under a term-5 leader", l.CommitIndex == 0 && l.StateMachine.Applied() == 0)
		cur := l.Propose("PUT x 3")
		s04check(t, "the new entry carries the current term", s.TermAt(cur) == 5)
		l.Handle(NewAppendReply(1, 0, 5, true, cur), 6)
		l.Handle(NewAppendReply(2, 0, 5, true, cur), 7)
		s04check(t, "a current-term entry on 3/5 commits itself and everything before it", l.CommitIndex == cur && l.LastApplied == cur)
		s04check(t, "the older entries were applied first, in order", len(l.StateMachine.AppliedCommands) > 0 && l.StateMachine.AppliedCommands[0] == "PUT x 1" && s04get(l.StateMachine, "x") == "3" && s04appliedPrefixMatchesLog(l))
	})

	s04scenario(t, "a follower commits up to min(leaderCommit, last new index) and applies in order", func(t *testing.T) {
		fs := NewStorage()
		f := s04standalone(1, fs, 3)
		f.Handle(NewAppendEntries(0, 1, 1, 0, 0, []LogEntry{{1, "PUT a 1"}, {1, "PUT b 2"}}, 5), 10)
		s04check(t, "leaderCommit beyond what the follower holds is capped at the last new entry", f.CommitIndex == 2 && f.LastApplied == 2)
		s04check(t, "the follower's state machine reflects the two applied writes", s04get(f.StateMachine, "a") == "1" && s04get(f.StateMachine, "b") == "2")
		f.Handle(NewAppendEntries(0, 1, 1, 2, 1, []LogEntry{{1, "PUT a 3"}}, 2), 11)
		s04check(t, "a new entry with an unchanged leaderCommit is stored but not applied", fs.LastIndex() == 3 && f.CommitIndex == 2 && s04get(f.StateMachine, "a") == "1")
		f.Handle(NewAppendEntries(0, 1, 1, 3, 1, nil, 3), 12)
		s04check(t, "a heartbeat that raises leaderCommit applies the pending entry", f.CommitIndex == 3 && s04get(f.StateMachine, "a") == "3" && s04appliedPrefixMatchesLog(f))
		f.Handle(NewAppendEntries(0, 1, 1, 3, 1, nil, 1), 13)
		s04check(t, "a lower leaderCommit never moves commitIndex backwards", f.CommitIndex == 3)
		f.Handle(NewAppendEntries(0, 1, 1, 7, 1, nil, 9), 14)
		s04check(t, "a refused AppendEntries (consistency check) does not touch commitIndex", f.CommitIndex == 3 && f.LastApplied == 3)
		s04check(t, "applied commands are exactly log[1..lastApplied]", s04appliedPrefixMatchesLog(f))
	})

	s04scenario(t, "client PUT/GET through the leader on a quiet cluster", func(t *testing.T) {
		c := NewCluster(5, 7, 0.0, 5)
		c.RunUntilStableLeader(2000)
		o1 := c.Submit("alice", "PUT balance:alice 100")
		o2 := c.Submit("bob", "PUT balance:bob 50")
		o3 := c.Submit("alice", "PUT balance:alice 75")
		o4 := c.Submit("audit", "GET balance:alice")
		s04check(t, "the leader accepted four client ops", o1 >= 0 && o2 >= 0 && o3 >= 0 && o4 >= 0 && c.PendingCount() == 4)
		settled := c.RunUntilSettled(1000)
		s04check(t, "every op completes once the leader applies it", settled > 0 && c.PendingCount() == 0 && len(c.Completed) == 4 && len(c.Failed) == 0)
		read, found := s04firstGet(c.Completed)
		s04check(t, "the read observes the latest committed write", found && read.Value == "75")
		afterInvoke := true
		for _, o := range c.Completed {
			if o.ReturnedAt <= o.InvokedAt {
				afterInvoke = false
			}
		}
		s04check(t, "ops return after they were invoked", afterInvoke)
		leader := c.StableLeader()
		if leader == nil {
			t.Fatalf("precondition: no stable leader on seed 7")
		}
		s04check(t, "the leader's commitIndex and lastApplied agree and cover the ops", leader.CommitIndex == leader.LastApplied && leader.CommitIndex >= 4)
		c.RunUntilApplied(leader.LastLogIndex(), 1000)
		s04check(t, "every follower applied the same prefix", c.StateMachinesConverged() && s04allNodes(c.Nodes, s04appliedPrefixMatchesLog))
		s04check(t, "every store agrees on the balances", s04allNodes(c.Nodes, func(n *RaftNode) bool {
			return s04get(n.StateMachine, "balance:alice") == "75" && s04get(n.StateMachine, "balance:bob") == "50"
		}))
		s04check(t, "the recorded history is linearizable", s04linearizable(c))
		s04check(t, "a follower refuses client ops: submit needs a stable leader", c.Node((leader.id+1)%5).Propose("PUT x 1") == -1)
	})

	s04scenario(t, "commit needs a majority of matchIndex, not a reply from anyone", func(t *testing.T) {
		q := NewCluster(5, 11, 0.0, 5)
		q.RunUntilStableLeader(2000)
		lead := q.StableLeader()
		if lead == nil {
			t.Fatalf("precondition: no stable leader on seed 11")
		}
		others := slices.Clone(lead.Peers)
		q.Crash(others[0])
		q.Crash(others[1])
		q.Crash(others[2])
		idx := q.Propose("PUT balance 1000")
		q.Run(400)
		s04check(t, "with 3 of 5 crashed the entry replicates to 2/5 and does not commit", idx > 0 && lead.CommitIndex < idx && lead.ReplicatedCount(idx) == 2)
		s04check(t, "nothing applied the uncommitted write", s04allNodes(q.Nodes, func(n *RaftNode) bool { return s04get(n.StateMachine, "balance") == "<null>" }))
		q.Restart(others[0])
		q.Run(400)
		s04check(t, "one node back (3 live) commits and applies it", lead.CommitIndex >= idx && s04get(lead.StateMachine, "balance") == "1000")
		s04check(t, "the restarted node applied it too, from its rebuilt state", s04get(q.Node(others[0]).StateMachine, "balance") == "1000")
	})

	s04scenario(t, "a restart rebuilds the state machine from the persisted log", func(t *testing.T) {
		c := NewCluster(5, 5, 0.0, 5)
		c.RunUntilStableLeader(2000)
		for k := 1; k <= 3; k++ {
			c.Submit("c", fmt.Sprintf("PUT k%d v%d", k, k))
		}
		c.RunUntilSettled(1000)
		if c.StableLeader() == nil {
			t.Fatalf("precondition: no stable leader on seed 5")
		}
		c.RunUntilApplied(c.StableLeader().LastLogIndex(), 1000)
		victim := (c.StableLeader().id + 1) % 5
		s04check(t, "precondition: the follower applied three writes", c.Node(victim).LastApplied >= 3 && s04get(c.Node(victim).StateMachine, "k3") == "v3")
		c.Crash(victim)
		back := c.Restart(victim)
		s04check(t, "volatile state is gone after restart: commitIndex 0, empty store, log intact", back.CommitIndex == 0 && back.LastApplied == 0 && back.StateMachine.Applied() == 0 && back.LastLogIndex() == c.Storages[victim].LastIndex())
		c.Run(300)
		s04check(t, "the next heartbeats carry leaderCommit and the node re-applies its log", c.StableLeader() != nil && back.LastApplied == c.StableLeader().LastApplied && s04get(back.StateMachine, "k3") == "v3" && s04appliedPrefixMatchesLog(back))
		s04check(t, "state machines converged", c.StateMachinesConverged())
	})

	s04scenario(t, "a stale leader's write is never applied and the client learns it failed", func(t *testing.T) {
		c := NewCluster(5, 7, 0.0, 5)
		c.RunUntilStableLeader(2000)
		c.Submit("alice", "PUT balance:alice 100")
		c.RunUntilSettled(1000)
		old := c.StableLeader()
		if old == nil {
			t.Fatalf("precondition: no stable leader on seed 7")
		}
		c.Partition([]int{old.id, (old.id + 1) % 5})
		lost := c.Submit("mallory", "PUT balance:alice 999")
		s04check(t, "the stale leader still accepts the write (it does not know it is stale)", lost >= 0)
		c.Run(600)
		s04check(t, "the write stays pending inside the minority", c.PendingCount() == 1 && s04get(old.StateMachine, "balance:alice") == "100")
		c.Heal()
		c.RunUntilStableLeader(2000)
		s04mustSubmit(t, c, "carol", "PUT balance:carol 7", 100)
		s04mustSubmit(t, c, "audit", "GET balance:alice", 100)
		settled := c.RunUntilSettled(2000)
		s04check(t, "after heal every op settles", settled > 0)
		s04check(t, "the minority write failed rather than completing", len(c.Failed) == 1 && c.Failed[0].Command == "PUT balance:alice 999")
		if c.StableLeader() != nil {
			c.RunUntilApplied(c.StableLeader().LastLogIndex(), 1000)
		}
		s04check(t, "no state machine ever applied the overwritten write", s04allNodes(c.Nodes, func(n *RaftNode) bool { return s04get(n.StateMachine, "balance:alice") == "100" }) && c.StateMachinesConverged())
		read, found := s04firstGet(c.Completed)
		s04check(t, "the post-heal read sees 100", found && read.Value == "100")
		s04check(t, "the completed history is linearizable", s04linearizable(c))
	})

	s04scenario(t, "the checker: sequential specification and real-time order", func(t *testing.T) {
		s0 := map[string]string{}
		s1, ok1 := Step(s0, Op{0, "a", PUT, "x", "1", 0, 1})
		s04check(t, "step applies a PUT to a copy, leaving the input alone", ok1 && s1 != nil && s1["x"] == "1" && len(s0) == 0)
		same, okSame := Step(s1, Op{1, "b", GET, "x", "1", 2, 3})
		_, okWrong := Step(s1, Op{2, "b", GET, "x", "2", 2, 3})
		s04check(t, "step accepts a GET that matches and rejects one that does not", okSame && s04samePointer(same, s1) && !okWrong)
		_, okAbsent := Step(s0, Op{3, "b", GET, "y", "", 0, 1})
		_, okPhantom := Step(s0, Op{4, "b", GET, "y", "0", 0, 1})
		s04check(t, "a GET of an absent key must observe the empty string", okAbsent && !okPhantom)

		good := NewHistory().Put("alice", "x", "1", 0, 5).Put("bob", "x", "2", 6, 10).Get("carol", "x", "2", 11, 15)
		rg := NewChecker().Check(good)
		s04check(t, "a read that sees the latest completed write is linearizable", rg.OK && s04respectsRealTime(good, rg.Order))
		stale := NewHistory().Put("alice", "x", "1", 0, 5).Put("bob", "x", "2", 6, 10).Get("carol", "x", "1", 11, 15)
		rs := NewChecker().Check(stale)
		s04check(t, "a stale read is not linearizable", !rs.OK && len(rs.Order) == 0)
		s04check(t, "the verdict names the longest prefix the search could linearize", len(rs.LongestPrefix) == 2)
		window := NewHistory().Put("alice", "x", "1", 0, 5).Put("bob", "x", "2", 6, 20).Get("carol", "x", "1", 11, 15)
		s04check(t, "a read that overlaps a write may see the old value", NewChecker().Check(window).OK)
		order := NewHistory().Put("alice", "x", "1", 0, 5).Get("carol", "x", "1", 6, 10).Put("bob", "x", "2", 2, 12).Get("dave", "x", "2", 13, 14)
		ro := NewChecker().Check(order)
		s04check(t, "the witness order respects every real-time precedence", ro.OK && s04respectsRealTime(order, ro.Order) && slices.Index(ro.Order, 1) < slices.Index(ro.Order, 2))
		twoKeys := NewHistory().Put("a", "x", "1", 0, 2).Put("b", "y", "9", 0, 2).Get("c", "y", "", 3, 4)
		s04check(t, "keys are independent registers: an absent read after a completed write is not linearizable", !NewChecker().Check(twoKeys).OK)
		s04check(t, "the empty history is linearizable", NewChecker().Check(NewHistory()).OK)
		s04check(t, "explored counts search states", rg.Explored >= 1 && rs.Explored >= 1)
	})

	s04scenario(t, "lossy seeds with a leader crash: histories linearizable, state machines converged", func(t *testing.T) {
		linearizable, converged := 0, 0
		const seeds = 12
		for seed := int64(1); seed <= seeds; seed++ {
			s := NewCluster(5, seed, 0.2, 10)
			s.RunUntilStableLeader(3000)
			for k := 1; k <= 5; k++ {
				s04mustSubmit(t, s, fmt.Sprintf("c%d", k%2), fmt.Sprintf("PUT k%d v%d", k, k), 100)
				s.Run(30)
			}
			victim := s.StableLeader()
			if victim == nil {
				victim = s.Node(0)
			}
			s.Crash(victim.id)
			s.RunUntilStableLeader(3000)
			for k := 1; k <= 5; k++ {
				s04mustSubmit(t, s, fmt.Sprintf("c%d", k%2), fmt.Sprintf("GET k%d", k), 100)
				s.Run(30)
			}
			s.Restart(victim.id)
			s.RunUntilStableLeader(3000)
			s.RunUntilSettled(3000)
			if s.PendingCount() == 0 && s04linearizable(s) {
				linearizable++
			}
			if s.StableLeader() != nil {
				s.RunUntilApplied(s.StableLeader().LastLogIndex(), 3000)
			}
			if s.StateMachinesConverged() && s04allNodes(s.Nodes, s04appliedPrefixMatchesLog) {
				converged++
			}
		}
		s04check(t, fmt.Sprintf("%d lossy seeds: every recorded history is linearizable", seeds), linearizable == seeds)
		s04check(t, fmt.Sprintf("%d lossy seeds: every state machine converged on the same applied prefix", seeds), converged == seeds)
	})
}
