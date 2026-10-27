// Project: projects/raft-kv — Raft-replicated key-value store on a deterministic simulated network (Go).
// Run:   go run .
// Test:  go test ./...
// Grade: python3 scripts/project_test.py raft-kv --lang go --all --path <workspace>
// Sources: Ongaro & Ousterhout, "In Search of an Understandable Consensus Algorithm" (USENIX ATC 2014);
//
//	Herlihy & Wing, "Linearizability" (TOPLAS 1990). Go 1.22+, standard library only.
package main

import "fmt"

func printEvents(c *Cluster, from int) {
	for i := from; i < len(c.Events); i++ {
		fmt.Println(c.Events[i])
	}
}

func printNodes(c *Cluster) {
	for _, n := range c.Nodes {
		fmt.Printf("  %s %-9s term %d log=%s commit=%d applied=%d kv=%v\n", NodeName(n.id), n.Role, n.CurrentTerm(),
			c.LogOf(n.id), n.CommitIndex, n.LastApplied, n.StateMachine)
	}
}

func mustSubmit(c *Cluster, client, command string, maxAttempts int) {
	if _, err := c.SubmitUntilAccepted(client, command, maxAttempts); err != nil {
		panic(err)
	}
}

func mustHistory(c *Cluster) *History {
	h, err := c.History()
	if err != nil {
		panic(err)
	}
	return h
}

func main() {
	fmt.Println("== Stage 2: election on a 5-node cluster (seed 7, no drops, delay 1-5 ticks) ==")
	c := NewCluster(5, 7, 0.0, 5)
	electedAt := c.RunUntilStableLeader(2000)
	printEvents(c, 0)
	leader := c.StableLeader()
	fmt.Printf("leader %s, term %d, stable at tick %d; messages sent=%d delivered=%d dropped=%d\n",
		NodeName(leader.id), leader.CurrentTerm(), electedAt, c.Network.Sent, c.Network.Delivered, c.Network.Dropped)

	fmt.Println()
	fmt.Println("== Stage 3-4: three client writes and a read through the leader ==")
	before := len(c.Events)
	c.Submit("alice", "PUT balance:alice 100")
	c.Submit("bob", "PUT balance:bob 50")
	c.Submit("alice", "PUT balance:alice 75")
	c.Submit("audit", "GET balance:alice")
	c.RunUntilSettled(1000)
	c.RunUntilApplied(c.StableLeader().LastLogIndex(), 1000)
	printEvents(c, before)
	for _, o := range c.Completed {
		fmt.Println("  " + o.String())
	}
	printNodes(c)

	fmt.Println()
	fmt.Println("== A stale leader's uncommitted writes are overwritten, never applied ==")
	old := c.StableLeader()
	follower := (old.id + 1) % 5
	before = len(c.Events)
	c.Partition([]int{old.id, follower})
	c.Submit("mallory", "PUT balance:alice 999")
	c.Run(600)
	var fresh *RaftNode
	for _, l := range c.Leaders() {
		if fresh == nil || l.CurrentTerm() > fresh.CurrentTerm() {
			fresh = l
		}
	}
	fmt.Printf("minority side: %s still leads term %d with an uncommitted write; majority elected %s for term %d\n",
		NodeName(old.id), old.CurrentTerm(), NodeName(fresh.id), fresh.CurrentTerm())
	c.Heal()
	c.RunUntilStableLeader(2000)
	mustSubmit(c, "carol", "PUT balance:carol 7", 100)
	c.RunUntilSettled(2000)
	c.RunUntilApplied(c.StableLeader().LastLogIndex(), 1000)
	printEvents(c, before)
	printNodes(c)
	firstFailed := "-"
	if len(c.Failed) > 0 {
		firstFailed = c.Failed[0].Command
	}
	fmt.Printf("failed client ops: %d (%s); state machines converged = %t\n", len(c.Failed), firstFailed, c.StateMachinesConverged())

	fmt.Println()
	fmt.Println("== Linearizability of the recorded history ==")
	h := mustHistory(c)
	r := NewChecker().Check(h)
	fmt.Println(r.Describe(h))

	fmt.Println()
	fmt.Println("== A history no Raft cluster should ever produce: a stale read ==")
	stale := NewHistory().
		Put("alice", "x", "1", 0, 5).
		Put("bob", "x", "2", 6, 10).
		Get("carol", "x", "1", 11, 15)
	fmt.Println(NewChecker().Check(stale).Describe(stale))

	fmt.Println()
	fmt.Println("== 20 seeds, 5 nodes, 20% drops, delay 1-10, leader crash and restart, 10 client ops each ==")
	linearizable, converged := 0, 0
	for seed := int64(1); seed <= 20; seed++ {
		s := NewCluster(5, seed, 0.2, 10)
		s.RunUntilStableLeader(3000)
		for k := 1; k <= 5; k++ {
			mustSubmit(s, fmt.Sprintf("c%d", k%2), fmt.Sprintf("PUT k%d v%d", k, k), 100)
			s.Run(30)
		}
		victim := s.StableLeader()
		if victim == nil {
			victim = s.Node(0)
		}
		s.Crash(victim.id)
		s.RunUntilStableLeader(3000)
		for k := 1; k <= 5; k++ {
			mustSubmit(s, fmt.Sprintf("c%d", k%2), fmt.Sprintf("GET k%d", k), 100)
			s.Run(30)
		}
		s.Restart(victim.id)
		s.RunUntilStableLeader(3000)
		s.RunUntilSettled(3000)
		if s.PendingCount() == 0 && NewChecker().Check(mustHistory(s)).OK {
			linearizable++
		}
		s.RunUntilApplied(s.StableLeader().LastLogIndex(), 3000)
		if s.StateMachinesConverged() {
			converged++
		}
	}
	fmt.Printf("linearizable histories: %d/20; converged state machines: %d/20; no invariant check fired\n", linearizable, converged)
}
