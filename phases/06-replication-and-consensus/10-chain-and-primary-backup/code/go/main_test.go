// Tests for phases/06-replication-and-consensus/10-chain-and-primary-backup. Run: go test ./...
package main

import (
	"fmt"
	"testing"
)

func check(t *testing.T, name string, condition bool) {
	t.Helper()
	if !condition {
		t.Errorf("FAIL: %s", name)
	}
}

func replyIs(c *ChainClient, r Request, cents int64) bool {
	m, ok := c.ReplyFor(r)
	return ok && m == OfMinor(cents, USD)
}

func contains(xs []int, x int) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// committedPreserved: every update any client got a reply for must be in the tail's history, and every replica in the chain must agree on balances.
func committedPreserved(c *ChainCluster) bool {
	inTail := map[string]bool{}
	for _, u := range c.Tail().History {
		inTail[u.Request.Key()] = true
	}
	knownUpdates := map[string]bool{}
	for _, n := range c.Nodes {
		for _, u := range n.History {
			knownUpdates[u.Request.Key()] = true
		}
	}
	for _, cl := range c.Clients {
		for k := range cl.Replies {
			if knownUpdates[k] && !inTail[k] { // a replied update is missing from the tail
				return false
			}
		}
	}
	first := ""
	for i, id := range c.Master.Chain {
		b := fmtBalances(c.Node(id).Balances)
		if i == 0 {
			first = b
		} else if b != first {
			return false
		}
	}
	return true
}

func TestMoneyDiscipline(t *testing.T) {
	sum, err := OfMinor(1999, USD).Plus(OfMinor(1, USD))
	check(t, "Money is integer minor units", err == nil && sum.MinorUnits == 2000)
	check(t, "Money prints canonically", OfMinor(-7, USD).String() == "-0.07 USD")
}

func TestHappyPath(t *testing.T) {
	c := NewChainCluster(1, 3, 3, 1, 2)
	u1 := c.Update(0, "alice", 10000)
	u2 := c.Update(0, "alice", -2550)
	check(t, "cluster settles", c.RunUntilQuiet(500, 60) > 0)
	check(t, "update replies carry the balance after the update", replyIs(c.Client(0), u1, 10000) && replyIs(c.Client(0), u2, 7450))
	check(t, "every replica applied both updates in order", len(c.Node(0).History) == 2 && c.Node(2).History[1].Request == u2)
	check(t, "head assigns increasing sequence numbers", c.Node(2).History[0].Seq == 1 && c.Node(2).History[1].Seq == 2)
	check(t, "Sent is empty once the tail has acked", c.Node(0).Sent.Len() == 0 && c.Node(1).Sent.Len() == 0)
	q := c.Query(1, "alice")
	c.RunUntilQuiet(500, 60)
	check(t, "tail answers queries with committed state", replyIs(c.Client(1), q, 7450))
	check(t, "only the tail handled the query", c.Node(2).QueriesHandled == 1 && c.Node(0).QueriesHandled == 0)
	check(t, "no invariant violations", len(c.Violations) == 0 && c.Checks > 0)
}

func TestStrongConsistency(t *testing.T) {
	v := NewChainCluster(2, 8, 3, 0, 2)
	w := v.Update(0, "bob", 5000)
	for len(v.Head().History) == 0 {
		v.Tick()
	}
	check(t, "head has applied, tail has not, writer has no reply", len(v.Head().History) == 1 && len(v.Tail().History) == 0 && !v.Client(0).HasReply(w))
	r := v.Query(1, "bob")
	v.RunUntilQuiet(500, 100)
	check(t, "a read issued before the write committed does not see it", replyIs(v.Client(1), r, 0))
	r2 := v.Query(1, "bob")
	v.RunUntilQuiet(500, 100)
	check(t, "a read issued after the write's reply sees it", replyIs(v.Client(1), r2, 5000))
}

func TestStaleClientRedirected(t *testing.T) {
	st := NewChainCluster(4, 2, 3, 0, 1)
	st.Update(0, "carol", 100)
	st.RunUntilQuiet(500, 60)
	stale, _ := st.Client(0).Query("carol", st.Now())
	st.Network.Send(query(100, st.Master.Head(), stale)) // deliberately ask the head
	st.RunUntilQuiet(500, 60)
	check(t, "non-tail replies NOT_TAIL and the client re-asks the real tail", st.Client(0).NotTailRedirects == 1 && replyIs(st.Client(0), stale, 100))
}

func TestDuplicateSuppression(t *testing.T) {
	d := NewChainCluster(5, 2, 3, 0, 1)
	dup := d.Update(0, "dave", 300)
	d.RunUntilQuiet(500, 60)
	d.Network.Send(updateMsg(100, d.Master.Head(), Update{0, dup})) // the client's retry arrives after commit
	d.Run(30)
	check(t, "retry after commit is not re-applied", len(d.Tail().History) == 1 && len(d.Head().History) == 1)
	check(t, "balance unchanged by the retry", d.Tail().Balance("dave") == OfMinor(300, USD))
}

func TestFailuresAndExtension(t *testing.T) {
	// middle failure with updates in flight: predecessor resends Sent to the new successor
	f := NewChainCluster(3, 3, 4, 2, 3)
	for i := 0; i < 6; i++ {
		f.Update(i%3, fmt.Sprintf("acct%d", i%2), int64(1000+i))
	}
	f.RunUntilQuiet(500, 60)
	for i := 0; i < 4; i++ {
		f.Update(i%3, "acct0", 100)
	}
	f.Run(2)
	middle := f.Master.Chain[1]
	f.CrashAndDetect(middle, 20)
	check(t, "middle failure settles", f.RunUntilQuiet(1000, 60) > 0)
	check(t, "chain shrank to three", len(f.Master.Chain) == 3 && !contains(f.Master.Chain, middle))
	check(t, "all ten updates committed after the middle failure", len(f.Tail().History) == 10 && committedPreserved(f))
	check(t, "the invariants held through the repair", len(f.Violations) == 0)

	// tail failure: the predecessor becomes tail and replies for everything it had in flight
	for i := 0; i < 3; i++ {
		f.Update(i%3, "acct1", 7)
	}
	f.Run(1)
	oldTail := f.Master.Tail()
	f.CrashAndDetect(oldTail, 20)
	check(t, "tail failure settles", f.RunUntilQuiet(1000, 60) > 0)
	check(t, "new tail has every update and an empty Sent", len(f.Tail().History) == 13 && f.Tail().Sent.Len() == 0 && committedPreserved(f))
	allAnswered := true
	for _, cl := range f.Clients {
		if len(cl.Pending) > 0 {
			allAnswered = false
		}
	}
	check(t, "every client got a reply for the in-flight updates", allAnswered)

	// head failure: unforwarded requests are lost and clients retry them against the new head
	for i := 0; i < 3; i++ {
		f.Update(i%3, "acct0", 3)
	}
	f.Run(1)
	oldHead := f.Master.Head()
	f.CrashAndDetect(oldHead, 20)
	check(t, "head failure settles", f.RunUntilQuiet(1000, 60) > 0)
	check(t, "new head is the old second node", f.Master.Head() != oldHead)
	check(t, "retries recovered the lost requests: 16 updates committed", len(f.Tail().History) == 16 && committedPreserved(f))
	check(t, "no committed update was lost across three failures", len(f.Violations) == 0)

	// extending the chain: the old tail keeps serving while the history is copied
	f.Master.Extend(4)
	f.Run(1)
	check(t, "old tail acts as tail during the transfer", f.Node(f.Master.Chain[len(f.Master.Chain)-2]).TransferPending)
	f.RunUntilQuiet(500, 60)
	check(t, "transfer complete: handover done", !f.Node(f.Master.Chain[len(f.Master.Chain)-2]).TransferPending && f.Master.Tail() == 4)
	check(t, "new tail has the full history", len(f.Node(4).History) == 16 && fmtBalances(f.Node(4).Balances) == fmtBalances(f.Head().Balances))
	f.Update(0, "acct0", 1)
	f.RunUntilQuiet(500, 60)
	check(t, "new tail replies for new updates", len(f.Node(4).History) == 17 && len(f.Client(0).Pending) == 0)
	check(t, "invariants held through the extension", len(f.Violations) == 0)
}

func TestLoadDistribution(t *testing.T) {
	lc := NewChainCluster(5, 2, 3, 0, 4)
	for i := 0; i < 50; i++ {
		lc.Update(i%4, fmt.Sprintf("a%d", i%5), 100)
	}
	for i := 0; i < 300; i++ {
		lc.Query(i%4, fmt.Sprintf("a%d", i%5))
	}
	lc.RunUntilQuiet(20000, 200)
	check(t, "head handles updates, tail handles queries, nobody else sees queries",
		lc.Head().UpdatesHandled == 50 && lc.Tail().QueriesHandled == 300 && lc.Head().QueriesHandled == 0 && lc.Node(1).QueriesHandled == 0)
}

func TestPrimaryBackup(t *testing.T) {
	pbNet := NewNetwork(9, 2)
	pbNodes := map[int]*PrimaryBackupNode{}
	for i := 0; i < 3; i++ {
		n := NewPrimaryBackupNode(i)
		pbNodes[i] = n
		pbNet.Add(n)
	}
	pbMaster := NewPrimaryBackupMaster(pbNet, pbNodes, []int{0, 1, 2})
	facade := NewMaster(pbNet, []int{0})
	pc := NewChainClient(100, facade)
	pbNet.Add(pc)
	for tk := 0; tk < 10; tk++ {
		pbNet.Tick()
	}
	p1, m1 := pc.Update("erin", OfMinor(2000, USD), pbNet.Now())
	p2, m2 := pc.Query("erin", pbNet.Now())
	pbNet.SendAll(append(m1, m2...))
	for !pbNet.Idle() {
		pbNet.Tick()
	}
	check(t, "primary replies after all backups ack", replyIs(pc, p1, 2000) && len(pbNodes[1].History) == 1 && len(pbNodes[2].History) == 1)
	check(t, "primary handles both updates and queries", pbNodes[0].UpdatesHandled == 1 && pbNodes[0].QueriesHandled == 1 && pc.HasReply(p2))
	// crash the primary with an update half-replicated: backup 1 got it, backup 2 did not
	half := Update{2, Request{100, 99, false, "erin", OfMinor(1, USD)}}
	pbNodes[1].Handle(pbUpdate(0, 1, half), pbNet.Now())
	pbNet.Crash(0)
	pbMaster.Fail(0)
	facade.Chain[0] = 1
	for !pbNet.Idle() {
		pbNet.Tick()
	}
	check(t, "promotion re-syncs backups from the new primary's history",
		pbNodes[1].IsPrimary && len(pbNodes[2].History) == 2 && fmtBalances(pbNodes[2].Balances) == fmtBalances(pbNodes[1].Balances))
}

func TestAcrossSeeds(t *testing.T) {
	clean := 0
	for seed := int64(1); seed <= 10; seed++ {
		r1 := NewChainCluster(seed, 4, 4, 1, 3)
		for i := 0; i < 8; i++ {
			r1.Update(i%3, fmt.Sprintf("x%d", i%3), int64(10+i))
		}
		r1.Run(3)
		r1.CrashAndDetect(r1.Master.Chain[int(seed%4)], 15)
		for i := 0; i < 6; i++ {
			r1.Update(i%3, fmt.Sprintf("x%d", i%3), 5)
			r1.Query(i%3, "x0")
		}
		r1.Run(2)
		r1.CrashAndDetect(r1.Master.Chain[int(seed%3)], 15)
		r1.Master.Extend(4)
		for i := 0; i < 4; i++ {
			r1.Update(i%3, "x1", 1)
		}
		settled := r1.RunUntilQuiet(5000, 60) > 0
		if settled && len(r1.Violations) == 0 && committedPreserved(r1) {
			clean++
		}
	}
	check(t, "10/10 seeds: invariants hold and no committed update is lost", clean == 10)
}
