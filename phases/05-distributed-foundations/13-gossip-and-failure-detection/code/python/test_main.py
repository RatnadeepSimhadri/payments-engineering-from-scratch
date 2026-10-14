"""Tests for phases/05-distributed-foundations/13-gossip-and-failure-detection. Run: python3 -m unittest -v"""
import unittest

from main import Cluster, Config, MemberState, Rng, SwimNode, Update

ALIVE, SUSPECT, DEAD = MemberState.ALIVE, MemberState.SUSPECT, MemberState.DEAD


def quiet_tracer(t, msg):
    pass


class PrecedenceTest(unittest.TestCase):
    def test_swim_section_4_2(self):
        a0, s0 = Update(1, ALIVE, 0), Update(1, SUSPECT, 0)
        a1, s1, d0 = Update(1, ALIVE, 1), Update(1, SUSPECT, 1), Update(1, DEAD, 0)
        self.assertTrue(a0.overrides(None), "anything overrides unknown")
        self.assertTrue(s0.overrides(a0), "suspect(i) overrides alive(i)")
        self.assertFalse(a0.overrides(s0), "alive(i) does not override suspect(i)")
        self.assertTrue(a1.overrides(s0), "alive(i+1) overrides suspect(i)")
        self.assertTrue(s1.overrides(a1), "suspect(i+1) overrides alive(i+1)")
        self.assertFalse(s0.overrides(s0), "suspect(i) does not override suspect(i)")
        self.assertFalse(a0.overrides(a0), "alive(i) does not override alive(i)")
        self.assertTrue(d0.overrides(a1), "dead overrides alive of any incarnation")
        self.assertFalse(a1.overrides(d0), "nothing overrides dead")
        self.assertFalse(s1.overrides(d0))

    def test_config_sanity(self):
        with self.assertRaises(ValueError):
            Config(10, 5, 3, 3, 6, 3)


class HealthyClusterTest(unittest.TestCase):
    def test_quiet_cluster(self):
        quiet = Cluster(6, 1, 0.0, 2)
        quiet.run(500)
        self.assertEqual(quiet.false_positives(), 0)
        self.assertTrue(all(n.suspicions_raised == 0 for n in quiet.nodes), "no suspicions in a healthy cluster")
        self.assertTrue(all(len(n.alive_members()) == 6 for n in quiet.nodes), "every node sees every other alive")
        self.assertGreater(quiet.node(0).direct_acks, 0, "direct acks flow")


class CrashDetectionTest(unittest.TestCase):
    def crash_run(self):
        c = Cluster(8, 3, 0.0, 2)
        c.run(100)
        c.crash(5)
        suspect_at = c.run_until_all_believe(5, SUSPECT, 2000)
        dead_at = c.run_until_all_believe(5, DEAD, 2000)
        return c, suspect_at, dead_at

    def test_suspect_then_dead_everywhere(self):
        c, suspect_at, dead_at = self.crash_run()
        self.assertGreater(suspect_at, 0, "crashed node becomes SUSPECT everywhere")
        self.assertGreater(dead_at, suspect_at, "then DEAD everywhere")
        self.assertGreaterEqual(dead_at - 100, c.config.suspicion_periods * c.config.protocol_period,
                                "not declared dead before the suspicion timeout")
        self.assertLessEqual(dead_at - 100, 12 * c.config.protocol_period, "declared dead within a bounded time")
        self.assertEqual(c.false_positives(), 0, "no live node was ever marked dead")
        self.assertTrue(c.views_converged(), "views converged on the dead set")
        self.assertEqual(len(c.node(0).alive_members()), 7)
        self.assertNotIn(5, c.node(0).alive_members(), "dead node is no longer probed")
        self.assertEqual((suspect_at, dead_at), (136, 188), "same timeline as the Java run")

    def test_determinism(self):
        c, _, _ = self.crash_run()
        c2, _, _ = self.crash_run()
        self.assertEqual(c2.events, c.events, "same seed replays the same events")


class IndirectProbeTest(unittest.TestCase):
    def test_cut_link_is_not_a_failure(self):
        cut = Cluster(5, 11, 0.0, 2)
        cut.run(50)
        cut.network.cut_link(1, 2)
        cut.run(600)
        self.assertEqual(cut.false_positives(), 0, "with ping-req, a one-link cut yields no false positive")
        self.assertIs(cut.node(1).state_of(2), ALIVE)
        self.assertGreater(cut.node(1).indirect_acks, 0, "n1 relied on indirect acks")

    def test_without_indirect_probes(self):
        naive = Cluster(5, 11, 0.0, 2, Config(12, 4, 0, 4, 6, 3))
        naive.run(50)
        naive.network.cut_link(1, 2)
        naive.run(1000)
        self.assertTrue(naive.node(1).suspicions_raised > 0 or naive.node(2).suspicions_raised > 0)
        self.assertGreater(naive.node(1).refutations + naive.node(2).refutations, 0, "the suspected node refuted")
        self.assertGreater(naive.node(1).incarnation + naive.node(2).incarnation, 0, "refutation raised incarnation")
        self.assertGreater(naive.false_positives(), 0, "a persistent cut with no indirect probes ends in a false positive")


class UpdateMergeTest(unittest.TestCase):
    def test_stale_suspicion_and_refutation(self):
        solo = SwimNode(0, [1, 2], Config.DEFAULT, Rng(1), 0, quiet_tracer)
        solo.apply_update(Update(1, ALIVE, 3), 0)
        solo.apply_update(Update(1, SUSPECT, 2), 0)
        self.assertIs(solo.state_of(1), ALIVE, "old suspicion ignored after newer alive")
        solo.apply_update(Update(1, SUSPECT, 3), 0)
        self.assertIs(solo.state_of(1), SUSPECT, "suspicion at the same incarnation is accepted")
        solo.apply_update(Update(0, SUSPECT, 0), 0)
        self.assertEqual(solo.incarnation, 1, "hearing a suspicion about myself bumps my incarnation")
        self.assertIs(solo.state_of(0), ALIVE)
        self.assertGreaterEqual(solo.gossip_queue_size(), 1, "the refutation is queued for gossip")

    def test_bounded_retransmission(self):
        g = SwimNode(0, [1, 2, 3, 4, 5, 6, 7], Config.DEFAULT, Rng(2), 0, quiet_tracer)
        g.apply_update(Update(3, SUSPECT, 0), 0)
        sends = 0
        while g.gossip_queue_size() > 0 and sends < 100:
            g.take_gossip()
            sends += 1
        self.assertEqual(sends, 9, "update retired after lambda*log2(n) piggybacks (3*log2(8) = 9)")

    def test_shuffle_is_seeded_fisher_yates(self):
        a, b = list(range(10)), list(range(10))
        Rng(42).shuffle(a)
        Rng(42).shuffle(b)
        self.assertEqual(a, b)
        self.assertEqual(sorted(a), list(range(10)))


class ConvergenceTest(unittest.TestCase):
    def test_across_seeds_under_loss(self):
        converged = false_positives = 0
        for seed in range(1, 11):
            r = Cluster(12, seed, 0.10, 2)
            r.run(200)
            r.crash(7)
            if r.run_until_all_believe(7, DEAD, 3000) > 0:
                converged += 1
            false_positives += r.false_positives()
        self.assertEqual(converged, 10, "10/10 seeds converge on the crash under 10% loss")
        self.assertEqual(false_positives, 0, "zero false positives across seeds")

    def test_load_independent_of_n(self):
        small, big = Cluster(8, 5, 0.0, 2), Cluster(32, 5, 0.0, 2)
        small.run(600)
        big.run(600)
        per_small, per_big = small.total_sent() / (8.0 * 50), big.total_sent() / (32.0 * 50)
        self.assertLess(abs(per_small - per_big), 0.5, "messages per node per period independent of N")

    def test_partition(self):
        p = Cluster(6, 21, 0.0, 2)
        p.run(100)
        p.network.partition({0, 1, 2})
        p.run(300)
        self.assertEqual(p.node(0).dead_members(), [3, 4, 5], "minority side sees the other side dead")
        self.assertEqual(p.node(4).dead_members(), [0, 1, 2], "majority side sees the minority dead")


if __name__ == "__main__":
    unittest.main()
