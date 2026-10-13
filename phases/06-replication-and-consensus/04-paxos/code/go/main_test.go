// Tests for phases/06-replication-and-consensus/04-paxos. Run: go test ./...
package main

import "testing"

func TestSplitMix64ReferenceVector(t *testing.T) {
	// SplitMix64 reference outputs for seed 0 so all three languages share one random stream
	rng := NewRng(0)
	if got := rng.NextLong(); got != 0xE220A8397B1DCDAF {
		t.Errorf("first output = %x", got)
	}
	if got := rng.NextLong(); got != 0x6E789E6AA1B965F4 {
		t.Errorf("second output = %x", got)
	}
	r1, r2 := NewRng(42), NewRng(42)
	if r1.NextInt(1000) != r2.NextInt(1000) || r1.NextDouble() != r2.NextDouble() {
		t.Error("same seed must give the same stream")
	}
}

func TestBallotOrdering(t *testing.T) {
	if (Ballot{1, 4}).Compare(Ballot{1, 3}) <= 0 {
		t.Error("proposer id must break ties")
	}
	if (Ballot{2, 0}).Compare(Ballot{1, 9}) <= 0 {
		t.Error("round must dominate proposer id")
	}
	if None.Compare(Ballot{1, 0}) >= 0 || !None.IsNone() {
		t.Error("None must be below every real ballot")
	}
	if (Ballot{3, 4}).String() != "3.4" {
		t.Errorf("String() = %s", Ballot{3, 4})
	}
}

func TestAcceptorPromiseOne(t *testing.T) {
	a := NewAcceptor(0, nil)
	out := a.Handle(PrepareMsg(3, 0, Ballot{2, 3}), 0)
	if len(out) != 1 || out[0].Kind != Promise || a.Promised != (Ballot{2, 3}) {
		t.Fatalf("expected a promise, got %+v", out)
	}
	if !out[0].PriorBallot.IsNone() || out[0].Value != "" {
		t.Error("first promise must report nothing accepted")
	}
	out = a.Handle(PrepareMsg(4, 0, Ballot{1, 4}), 0)
	if out[0].Kind != Nack || out[0].PriorBallot != (Ballot{2, 3}) {
		t.Error("lower prepare must be NACKed with the promised ballot")
	}
	out = a.Handle(AcceptMsg(4, 0, Ballot{1, 4}, "X"), 0)
	if out[0].Kind != Nack || !a.AcceptedBallot.IsNone() {
		t.Error("acceptor must refuse to accept below its promise")
	}
	out = a.Handle(AcceptMsg(3, 0, Ballot{2, 3}, "Y"), 0)
	if out[0].Kind != Accepted || a.AcceptedValue != "Y" {
		t.Error("acceptor must accept the ballot it promised")
	}
	// promise two: a later prepare learns what was accepted
	out = a.Handle(PrepareMsg(5, 0, Ballot{3, 5}), 0)
	if out[0].PriorBallot != (Ballot{2, 3}) || out[0].Value != "Y" {
		t.Error("later promise must carry the accepted ballot and value")
	}
}

func TestAcceptorForwardsAcceptedToLearners(t *testing.T) {
	out := NewAcceptor(1, []int{7, 8}).Handle(AcceptMsg(3, 1, Ballot{1, 3}, "v"), 0)
	if len(out) != 3 {
		t.Fatalf("expected 3 messages (proposer + 2 learners), got %d", len(out))
	}
}

func TestProposerAdoptsHighestPriorValue(t *testing.T) {
	p := NewProposer(3, []int{0, 1, 2}, 10)
	prepares := p.Propose("mine", 0)
	if len(prepares) != 3 || prepares[0].Kind != Prepare {
		t.Fatal("propose must broadcast PREPARE to every acceptor")
	}
	b := p.Ballot
	p.Handle(PromiseMsg(0, 3, b, Ballot{1, 9}, "old"), 1)
	accepts := p.Handle(PromiseMsg(1, 3, b, Ballot{2, 9}, "newer"), 1)
	if len(accepts) != 3 || accepts[0].Kind != Accept {
		t.Fatal("majority of promises must trigger ACCEPT broadcast")
	}
	if p.Proposing != "newer" || accepts[0].Value != "newer" {
		t.Errorf("proposer must adopt the highest prior value, proposing %q", p.Proposing)
	}
	if len(p.Handle(PromiseMsg(2, 3, Ballot{0, 3}, None, ""), 1)) != 0 {
		t.Error("stale replies for an old ballot must be ignored")
	}
}

func TestProposerRetries(t *testing.T) {
	q := NewProposer(4, []int{0, 1, 2}, 10)
	q.Propose("q", 0)
	retry := q.Handle(NackMsg(0, 4, q.Ballot, Ballot{7, 3}), 1)
	if q.Ballot != (Ballot{8, 4}) || len(retry) != 3 || q.Restarts != 1 {
		t.Errorf("NACK must restart above the promised ballot, got %s", q.Ballot)
	}
	if len(q.Tick(11)) != 3 || q.Round != 9 {
		t.Error("timeout must restart a stalled round")
	}
	if len(q.Tick(12)) != 0 {
		t.Error("no timeout before the deadline")
	}
}

func TestSingleProposerChooses(t *testing.T) {
	c := NewPaxosCluster(3, 1, 1, 1, 0.0, 1, 10)
	c.Propose(0, "only")
	if !c.Run(20) || c.FirstChosen != "only" {
		t.Fatalf("single proposer must decide, chosen=%q", c.FirstChosen)
	}
	if c.ChosenAt > 4 {
		t.Errorf("chosen at t=%d, expected within two round trips", c.ChosenAt)
	}
	if c.Learners[0].Learned != "only" || c.Proposers[0].Decided != "only" {
		t.Error("learner and proposer must see the chosen value")
	}
}

func TestDuelingProposersConverge(t *testing.T) {
	c := NewPaxosCluster(3, 2, 1, 7, 0.0, 1, 10)
	c.Propose(0, "A")
	c.Propose(1, "B")
	for i := 0; i < 12; i++ {
		c.Tick()
	}
	if c.FirstChosen == "" || c.FirstChosen != c.ChosenValue() {
		t.Fatal("two proposers: exactly one value must be chosen")
	}
	if c.Proposers[0].Decided == "" || c.Proposers[0].Decided != c.Proposers[1].Decided {
		t.Error("losing proposer must decide the same value")
	}
}

func TestLostMessagesNeverViolateSafety(t *testing.T) {
	decided := 0
	for seed := int64(1); seed <= 200; seed++ {
		c := NewPaxosCluster(5, 3, 2, seed, 0.25, 3, 12)
		c.Propose(0, "A")
		c.Propose(1, "B")
		c.Propose(2, "C")
		if c.Run(400) { // checkSafety panics on a second distinct chosen value
			decided++
		}
		for _, l := range c.Learners {
			if l.Learned != "" && l.Learned != c.FirstChosen {
				t.Fatalf("seed %d: learner learned %s but chosen is %s", seed, l.Learned, c.FirstChosen)
			}
		}
		for _, p := range c.Proposers {
			if p.Decided != "" && p.Decided != c.FirstChosen {
				t.Fatalf("seed %d: proposer decided %s but chosen is %s", seed, p.Decided, c.FirstChosen)
			}
		}
	}
	if decided < 180 {
		t.Errorf("liveness: only %d/200 lossy runs decided", decided)
	}
}

func TestSameSeedReplaysTheSameRun(t *testing.T) {
	type outcome struct {
		delivered, dropped, chosenAt int
		chosen                       string
	}
	var runs [2]outcome
	for i := range runs {
		d := NewPaxosCluster(5, 3, 1, 99, 0.3, 3, 12)
		d.Propose(0, "A")
		d.Propose(1, "B")
		d.Propose(2, "C")
		d.Run(400)
		runs[i] = outcome{d.Network.Delivered, d.Network.Dropped, d.ChosenAt, d.FirstChosen}
	}
	if runs[0] != runs[1] {
		t.Errorf("determinism: %+v != %+v", runs[0], runs[1])
	}
}

func TestMinorityPartitionCannotChoose(t *testing.T) {
	part := NewPaxosCluster(5, 1, 1, 3, 0.0, 2, 8)
	part.Network.Isolate(0)
	part.Network.Isolate(1)
	part.Network.Isolate(2)
	part.Propose(0, "P")
	if part.Run(100) || part.ChosenValue() != "" {
		t.Fatal("three of five acceptors isolated: nothing can be chosen")
	}
	part.Network.Heal()
	if !part.Run(100) || part.FirstChosen != "P" {
		t.Error("after heal the proposer's retries must succeed")
	}
}

func TestConstructedDuel(t *testing.T) {
	duel := NewPaxosCluster(3, 2, 1, 1, 0.0, 1, 1000)
	notAccept := func(m Message) bool { return m.Kind != Accept }
	acceptFrom := func(id int) func(Message) bool {
		return func(m Message) bool { return m.Kind == Accept && m.From == id }
	}
	duel.Propose(0, "A")
	duel.Network.Drain(notAccept)
	duel.Propose(1, "B")
	duel.Network.Drain(notAccept)
	for k := 0; k < 4; k++ {
		duel.Network.Drain(acceptFrom(3))
		duel.Network.Drain(notAccept)
		duel.Network.Drain(acceptFrom(4))
		duel.Network.Drain(notAccept)
	}
	if duel.ChosenValue() != "" || duel.MaxRound() < 8 {
		t.Errorf("duel: nothing should be chosen while rounds climb (round %d)", duel.MaxRound())
	}
	for _, a := range duel.Acceptors {
		if !a.AcceptedBallot.IsNone() {
			t.Error("duel: no acceptor should have accepted anything")
		}
	}
	duel.Network.Drain(func(Message) bool { return true })
	if duel.ChosenValue() == "" || duel.ChosenValue() != duel.FirstChosen {
		t.Error("duel: released schedule must choose exactly one value")
	}
}

func TestLearnerRefusesToLearnTwoValues(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("learner fed a majority for two different values must panic")
		}
	}()
	l := NewLearner(9, 3)
	l.Handle(AcceptedMsg(0, 9, Ballot{1, 3}, "X"), 0)
	l.Handle(AcceptedMsg(1, 9, Ballot{1, 3}, "X"), 0)
	if l.Learned != "X" {
		t.Fatal("learner should have learned X")
	}
	l.Handle(AcceptedMsg(0, 9, Ballot{2, 4}, "Y"), 0)
	l.Handle(AcceptedMsg(2, 9, Ballot{2, 4}, "Y"), 0)
}
