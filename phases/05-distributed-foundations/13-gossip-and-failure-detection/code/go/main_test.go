// Tests for phases/05-distributed-foundations/13-gossip-and-failure-detection. Run: go test ./...
package main

import (
	"errors"
	"math"
	"reflect"
	"testing"
)

func noTrace(int, string) {}

func TestPrecedenceRules(t *testing.T) {
	a0, s0 := Update{1, Alive, 0}, Update{1, Suspect, 0}
	a1, s1, d0 := Update{1, Alive, 1}, Update{1, Suspect, 1}, Update{1, Dead, 0}
	cases := []struct {
		name     string
		u, cur   Update
		known    bool
		expected bool
	}{
		{"anything overrides unknown", a0, Update{}, false, true},
		{"suspect(i) overrides alive(i)", s0, a0, true, true},
		{"alive(i) does not override suspect(i)", a0, s0, true, false},
		{"alive(i+1) overrides suspect(i)", a1, s0, true, true},
		{"suspect(i+1) overrides alive(i+1)", s1, a1, true, true},
		{"suspect(i) does not override suspect(i)", s0, s0, true, false},
		{"alive(i) does not override alive(i)", a0, a0, true, false},
		{"dead overrides alive of any incarnation", d0, a1, true, true},
		{"alive does not override dead", a1, d0, true, false},
		{"suspect does not override dead", s1, d0, true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.u.Overrides(c.cur, c.known); got != c.expected {
				t.Errorf("%s overrides %s = %t, want %t", c.u, c.cur, got, c.expected)
			}
		})
	}
	if s0.String() != "n1:SUSPECT@0" {
		t.Error("updates print as nID:STATE@incarnation")
	}
}

func TestConfigSanity(t *testing.T) {
	if _, err := NewConfig(10, 5, 3, 3, 6, 3); !errors.Is(err, ErrBadConfig) {
		t.Error("ack timeout must fit twice in the period")
	}
	if _, err := NewConfig(12, 4, -1, 4, 6, 3); !errors.Is(err, ErrBadConfig) {
		t.Error("negative indirect probes rejected")
	}
	if cfg, err := NewConfig(12, 4, 3, 4, 6, 3); err != nil || cfg != DefaultConfig {
		t.Error("the default config validates")
	}
}

func TestQuietClusterHasNoFalsePositives(t *testing.T) {
	quiet := NewCluster(6, 1, 0.0, 2)
	quiet.Run(500)
	if quiet.FalsePositives() != 0 {
		t.Error("no false positives in a healthy cluster")
	}
	for _, n := range quiet.Nodes {
		if n.SuspicionsRaised != 0 {
			t.Errorf("n%d raised a suspicion in a healthy cluster", n.id)
		}
		if len(n.AliveMembers()) != 6 {
			t.Errorf("n%d does not see every other node alive", n.id)
		}
	}
	if quiet.Node(0).DirectAcks == 0 {
		t.Error("direct acks flow")
	}
}

func TestCrashedNodeIsSuspectedThenDeclaredDead(t *testing.T) {
	c := NewCluster(8, 3, 0.0, 2)
	c.Run(100)
	c.Crash(5)
	suspectAt := c.RunUntilAllBelieve(5, Suspect, 2000)
	if suspectAt <= 0 {
		t.Fatal("crashed node becomes SUSPECT everywhere")
	}
	deadAt := c.RunUntilAllBelieve(5, Dead, 2000)
	if deadAt <= suspectAt {
		t.Error("then DEAD everywhere")
	}
	if deadAt-100 < c.Config.SuspicionPeriods*c.Config.ProtocolPeriod {
		t.Error("not declared dead before the suspicion timeout")
	}
	if deadAt-100 > 12*c.Config.ProtocolPeriod {
		t.Error("declared dead within a bounded time")
	}
	if c.FalsePositives() != 0 {
		t.Error("no live node was ever marked dead")
	}
	if !c.ViewsConverged() {
		t.Error("views converged on the dead set")
	}
	alive := c.Node(0).AliveMembers()
	if len(alive) != 7 || containsAll(alive, []int{5}) {
		t.Error("dead node is no longer probed")
	}
	// Parity with the Java and Python demos.
	if suspectAt != 136 || deadAt != 188 {
		t.Errorf("seed 3 timings match Java (136, 188): got %d, %d", suspectAt, deadAt)
	}
	if len(c.Events) < 2 || c.Events[1] != "t=122  n2 got no ack from n5 (direct + 3 indirect) -> SUSPECT" {
		t.Errorf("first suspicion event matches Java: %v", c.Events[:min(2, len(c.Events))])
	}

	c2 := NewCluster(8, 3, 0.0, 2)
	c2.Run(100)
	c2.Crash(5)
	c2.RunUntilAllBelieve(5, Suspect, 2000)
	c2.RunUntilAllBelieve(5, Dead, 2000)
	if !reflect.DeepEqual(c2.Events, c.Events) {
		t.Error("same seed replays the same events")
	}
}

func TestIndirectProbingSavesACutLink(t *testing.T) {
	cut := NewCluster(5, 11, 0.0, 2)
	cut.Run(50)
	cut.Network.CutLink(1, 2)
	cut.Run(600)
	if cut.FalsePositives() != 0 {
		t.Error("with ping-req, a one-link cut yields no false positive")
	}
	if cut.Node(1).StateOf(2) != Alive {
		t.Error("n1 still believes n2 alive")
	}
	if cut.Node(1).IndirectAcks == 0 {
		t.Error("n1 relied on indirect acks")
	}
	if cut.Node(1).IndirectAcks != 36 {
		t.Errorf("seed 11 yields 36 indirect acks in Java; Go got %d", cut.Node(1).IndirectAcks)
	}
}

func TestWithoutIndirectProbesTheCutIsRefutedThenEventuallyWrong(t *testing.T) {
	cfg, err := NewConfig(12, 4, 0, 4, 6, 3)
	if err != nil {
		t.Fatal(err)
	}
	naive := NewClusterWith(5, 11, 0.0, 2, cfg)
	naive.Run(50)
	naive.Network.CutLink(1, 2)
	naive.Run(1000)
	if naive.Node(1).SuspicionsRaised == 0 && naive.Node(2).SuspicionsRaised == 0 {
		t.Error("without ping-req the cut raises suspicions")
	}
	if naive.Node(1).Refutations+naive.Node(2).Refutations == 0 {
		t.Error("the suspected node refuted at least once")
	}
	if naive.Node(1).Incarnation+naive.Node(2).Incarnation == 0 {
		t.Error("refutation raised the incarnation")
	}
	if naive.FalsePositives() == 0 {
		t.Error("but a persistent cut with no indirect probes eventually becomes a false positive")
	}
}

func TestStaleSuspicionCannotOverrideRefutation(t *testing.T) {
	solo := NewSwimNode(0, []int{1, 2}, DefaultConfig, NewRng(1), 0, noTrace)
	solo.ApplyUpdate(Update{1, Alive, 3}, 0)
	solo.ApplyUpdate(Update{1, Suspect, 2}, 0)
	if solo.StateOf(1) != Alive {
		t.Error("old suspicion ignored after newer alive")
	}
	solo.ApplyUpdate(Update{1, Suspect, 3}, 0)
	if solo.StateOf(1) != Suspect {
		t.Error("suspicion at the same incarnation is accepted")
	}
	solo.ApplyUpdate(Update{0, Suspect, 0}, 0)
	if solo.Incarnation != 1 || solo.StateOf(0) != Alive {
		t.Error("hearing a suspicion about myself bumps my incarnation")
	}
	if solo.GossipQueueSize() < 1 {
		t.Error("the refutation is queued for gossip")
	}
	if solo.StateOf(99) != Unknown {
		t.Error("an unknown member has state Unknown")
	}
}

func TestDisseminationIsBounded(t *testing.T) {
	g := NewSwimNode(0, []int{1, 2, 3, 4, 5, 6, 7}, DefaultConfig, NewRng(2), 0, noTrace)
	g.ApplyUpdate(Update{3, Suspect, 0}, 0)
	sends := 0
	for g.GossipQueueSize() > 0 && sends < 100 {
		g.TakeGossip()
		sends++
	}
	if sends != 9 {
		t.Errorf("update retired after lambda*log2(n) piggybacks (3*log2(8) = 9): %d", sends)
	}
	// Fewest-transmissions first, ties in queue order; one entry per member.
	g.ApplyUpdate(Update{4, Suspect, 0}, 0)
	g.TakeGossip()
	g.ApplyUpdate(Update{5, Suspect, 0}, 0)
	g.ApplyUpdate(Update{4, Alive, 1}, 0)
	got := g.TakeGossip()
	if !reflect.DeepEqual(got, []Update{{5, Suspect, 0}, {4, Alive, 1}}) {
		t.Errorf("newest update per member replaces the old one and the queue keeps order: %v", got)
	}
}

func TestConvergenceUnderLoss(t *testing.T) {
	converged, falsePositives := 0, 0
	for seed := int64(1); seed <= 10; seed++ {
		r := NewCluster(12, seed, 0.10, 2)
		r.Run(200)
		r.Crash(7)
		if r.RunUntilAllBelieve(7, Dead, 3000) > 0 {
			converged++
		}
		falsePositives += r.FalsePositives()
	}
	if converged != 10 {
		t.Errorf("10/10 seeds converge on the crash under 10%% loss: %d", converged)
	}
	if falsePositives != 0 {
		t.Error("zero false positives across seeds")
	}
}

func TestLoadIsConstantInN(t *testing.T) {
	small, big := NewCluster(8, 5, 0.0, 2), NewCluster(32, 5, 0.0, 2)
	small.Run(600)
	big.Run(600)
	perSmall := float64(small.TotalSent()) / (8.0 * 50)
	perBig := float64(big.TotalSent()) / (32.0 * 50)
	if math.Abs(perSmall-perBig) >= 0.5 {
		t.Errorf("messages per node per period independent of N: %.2f vs %.2f", perSmall, perBig)
	}
}

func TestPartitionEachSideDeclaresTheOtherDead(t *testing.T) {
	p := NewCluster(6, 21, 0.0, 2)
	p.Run(100)
	p.Network.Partition([]int{0, 1, 2})
	p.Run(300)
	if !reflect.DeepEqual(p.Node(0).DeadMembers(), []int{3, 4, 5}) {
		t.Errorf("minority side sees the other side dead: %v", p.Node(0).DeadMembers())
	}
	if !reflect.DeepEqual(p.Node(4).DeadMembers(), []int{0, 1, 2}) {
		t.Errorf("majority side sees the minority dead: %v", p.Node(4).DeadMembers())
	}
	if p.CountBelieving(3, Dead) != 3 {
		t.Error("exactly the three minority nodes believe n3 dead")
	}
}

func TestRngParity(t *testing.T) {
	// SplitMix64(1): first output 0x910A2DEC89025CC1, as in every other port in the course.
	if got := uint64(NewRng(1).NextLong()); got != 0x910A2DEC89025CC1 {
		t.Errorf("SplitMix64 first output = %x", got)
	}
	a, b := []int{0, 1, 2, 3, 4}, []int{0, 1, 2, 3, 4}
	Shuffle(NewRng(9), a)
	Shuffle(NewRng(9), b)
	if !reflect.DeepEqual(a, b) || reflect.DeepEqual(a, []int{0, 1, 2, 3, 4}) {
		t.Error("the shuffle replays with the seed and actually permutes")
	}
}
