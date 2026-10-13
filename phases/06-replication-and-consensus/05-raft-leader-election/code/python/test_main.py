"""Tests for phases/06-replication-and-consensus/05-raft-leader-election. Run: python3 -m unittest -v"""
import unittest

from main import DEFAULT_CONFIG, Cluster, Config, Message, MessageKind, RaftNode, Rng, Role, Storage


def standalone(node_id: int, storage: Storage, seed: int) -> RaftNode:
    return RaftNode(node_id, [p for p in range(5) if p != node_id], storage, Rng(seed), DEFAULT_CONFIG, 0)


class ElectionTest(unittest.TestCase):
    def test_single_leader_elected(self):
        c = Cluster(5, 7, 0.0, 5)
        at = c.run_until_stable_leader(2000)
        leader = c.stable_leader()
        self.assertGreater(at, 0)
        self.assertIsNotNone(leader)
        self.assertGreaterEqual(at, DEFAULT_CONFIG.election_timeout_min)
        self.assertEqual(len(c.leaders()), 1)
        self.assertTrue(all(n.current_term == leader.current_term for n in c.nodes))
        self.assertTrue(all(n.leader_id == leader.id for n in c.nodes if n.role != Role.LEADER))

    def test_heartbeats_suppress_new_elections(self):
        c = Cluster(5, 7, 0.0, 5)
        c.run_until_stable_leader(2000)
        term = c.max_term()
        c.run(2000)
        self.assertEqual(c.max_term(), term)
        self.assertEqual(len(c.leaders()), 1)

    def test_same_seed_same_trace(self):
        c1, c2, c3 = Cluster(5, 42, 0.1, 8), Cluster(5, 42, 0.1, 8), Cluster(5, 43, 0.1, 8)
        c1.run(1500)
        c2.run(1500)
        c3.run(1500)
        self.assertTrue(c1.events)
        self.assertEqual(c1.events, c2.events)
        self.assertNotEqual(c1.events, c3.events)

    def test_at_most_one_leader_per_term_over_many_seeds(self):
        elected = 0
        for seed in range(1, 41):
            s = Cluster(5, seed, 0.2, 10)
            if s.run_until_stable_leader(2500) > 0:
                elected += 1
            s.run(500)  # check_election_safety raises on a violation after every delivery
            self.assertTrue(s.leaders_by_term)
        self.assertEqual(elected, 40)

    def test_three_node_lossy_clusters_elect(self):
        for seed in range(100, 120):
            s = Cluster(3, seed, 0.3, 10)
            self.assertGreater(s.run_until_stable_leader(4000), 0, f"seed {seed}")

    def test_partition_heals_with_higher_term_winning(self):
        p = Cluster(5, 7, 0.0, 5)
        p.run_until_stable_leader(2000)
        old = p.stable_leader()
        old_term = old.current_term
        p.partition({old.id, (old.id + 1) % 5})
        p.run(800)
        self.assertEqual(old.role, Role.LEADER)
        self.assertEqual(old.current_term, old_term)
        self.assertEqual(len(p.leaders()), 2)
        self.assertGreater(p.max_term(), old_term)
        p.heal()
        self.assertGreater(p.run_until_stable_leader(2000), 0)
        self.assertEqual(old.role, Role.FOLLOWER)
        self.assertGreater(p.stable_leader().current_term, old_term)
        self.assertIsNot(p.stable_leader(), old)

    def test_minority_partition_never_elects(self):
        m = Cluster(5, 11, 0.0, 5)
        m.partition({0, 1})
        m.run(3000)
        self.assertFalse(any(l.id in (0, 1) for l in m.leaders()))
        self.assertGreater(max(m.node(0).current_term, m.node(1).current_term), 3)
        self.assertEqual(len(m.leaders()), 1)

    def test_crash_and_restart_rejoins_as_follower(self):
        r = Cluster(5, 21, 0.05, 5)
        r.run_until_stable_leader(2000)
        lead = r.stable_leader()
        r.crash(lead.id)
        self.assertGreater(r.run_until_stable_leader(2000), 0)
        self.assertNotEqual(r.stable_leader().id, lead.id)
        back = r.restart(lead.id)
        self.assertGreaterEqual(back.current_term, lead.current_term)
        r.run_until_stable_leader(2000)
        self.assertEqual(back.role, Role.FOLLOWER)
        self.assertEqual(back.current_term, r.stable_leader().current_term)


class RequestVoteReceiverTest(unittest.TestCase):
    def test_stale_term_rejected_without_state_change(self):
        n = standalone(1, Storage(), 1)
        n.storage.save(5, -1)
        reply = n.handle(Message.request_vote(0, 1, 3, 0, 0), 10)[0]
        self.assertEqual(reply.kind, MessageKind.REQUEST_VOTE_REPLY)
        self.assertFalse(reply.granted)
        self.assertEqual(reply.term, 5)
        self.assertEqual((n.current_term, n.voted_for), (5, -1))

    def test_one_vote_per_term(self):
        n = standalone(1, Storage(), 1)
        n.storage.save(5, -1)
        self.assertTrue(n.handle(Message.request_vote(0, 1, 5, 0, 0), 11)[0].granted)
        self.assertEqual(n.voted_for, 0)
        self.assertFalse(n.handle(Message.request_vote(2, 1, 5, 0, 0), 12)[0].granted)
        self.assertTrue(n.handle(Message.request_vote(0, 1, 5, 0, 0), 13)[0].granted)  # retransmit from same candidate
        higher = n.handle(Message.request_vote(3, 1, 6, 0, 0), 14)[0]
        self.assertTrue(higher.granted)
        self.assertEqual((n.current_term, n.voted_for), (6, 3))

    def test_log_up_to_date_check(self):
        u = standalone(2, Storage(), 2)
        u.last_log_index, u.last_log_term = 10, 3
        self.assertFalse(u.handle(Message.request_vote(0, 2, 4, 20, 2), 1)[0].granted)  # older last term
        self.assertFalse(u.handle(Message.request_vote(0, 2, 4, 9, 3), 2)[0].granted)   # same term, shorter log
        self.assertTrue(u.handle(Message.request_vote(0, 2, 4, 10, 3), 3)[0].granted)   # same term, same length
        u2 = standalone(2, Storage(), 2)
        u2.last_log_index, u2.last_log_term = 10, 3
        self.assertTrue(u2.handle(Message.request_vote(1, 2, 4, 2, 4), 3)[0].granted)   # higher term, shorter log

    def test_voted_for_persists_across_crash(self):
        durable = Storage()
        a = standalone(1, durable, 6)
        self.assertTrue(a.handle(Message.request_vote(0, 1, 1, 0, 0), 10)[0].granted)
        b = standalone(1, durable, 7)  # restart: fresh volatile state, same Storage
        self.assertEqual((b.current_term, b.voted_for, b.role), (1, 0, Role.FOLLOWER))
        self.assertFalse(b.handle(Message.request_vote(2, 1, 1, 0, 0), 12)[0].granted)
        lost = standalone(1, Storage(), 8)
        self.assertTrue(lost.handle(Message.request_vote(2, 1, 1, 0, 0), 12)[0].granted)  # the double vote without persistence


class CandidateAndLeaderTest(unittest.TestCase):
    def test_candidate_requests_votes_and_steps_down_on_higher_term(self):
        cand = standalone(0, Storage(), 3)
        reqs = cand.start_election(100)
        self.assertEqual(len(reqs), 4)
        self.assertTrue(all(r.kind == MessageKind.REQUEST_VOTE and r.term == 1 for r in reqs))
        self.assertEqual((cand.role, cand.voted_for), (Role.CANDIDATE, 0))
        self.assertGreaterEqual(cand.storage.writes, 1)
        cand.handle(Message.vote_reply(1, 0, 1, True), 101)
        self.assertEqual((cand.role, cand.votes()), (Role.CANDIDATE, 2))
        cand.handle(Message.request_vote(3, 0, 2, 0, 0), 102)
        self.assertEqual((cand.role, cand.current_term, cand.voted_for), (Role.FOLLOWER, 2, 3))

    def test_candidate_steps_down_when_it_hears_a_leader(self):
        cand = standalone(0, Storage(), 4)
        cand.start_election(100)
        hb = cand.handle(Message.heartbeat(4, 0, 1), 101)[0]
        self.assertTrue(hb.granted)
        self.assertEqual((cand.role, cand.leader_id), (Role.FOLLOWER, 4))

    def test_stale_vote_replies_ignored_and_majority_makes_leader(self):
        w = standalone(0, Storage(), 5)
        w.start_election(50)
        w.start_election(300)  # term 2
        w.handle(Message.vote_reply(1, 0, 1, True), 301)
        self.assertEqual((w.votes(), w.role), (1, Role.CANDIDATE))
        w.handle(Message.vote_reply(1, 0, 2, True), 302)
        hbs = w.handle(Message.vote_reply(2, 0, 2, True), 303)
        self.assertEqual((w.role, w.leader_id), (Role.LEADER, 0))
        self.assertEqual(len(hbs), 4)
        self.assertTrue(all(h.kind == MessageKind.APPEND_ENTRIES for h in hbs))
        self.assertEqual(w.tick(304), [])
        self.assertEqual(len(w.tick(303 + DEFAULT_CONFIG.heartbeat_interval)), 4)

    def test_leader_rejects_stale_heartbeat_and_steps_down_on_higher(self):
        w = standalone(0, Storage(), 5)
        w.start_election(50)
        w.handle(Message.vote_reply(1, 0, 1, True), 51)
        w.handle(Message.vote_reply(2, 0, 1, True), 52)
        self.assertEqual(w.role, Role.LEADER)
        w.storage.save(2, 0)
        rej = w.handle(Message.heartbeat(3, 0, 1), 60)[0]
        self.assertFalse(rej.granted)
        self.assertEqual(w.role, Role.LEADER)
        w.handle(Message.heartbeat(3, 0, 3), 61)
        self.assertEqual((w.role, w.leader_id, w.current_term), (Role.FOLLOWER, 3, 3))

    def test_two_leaders_in_one_term_detected(self):
        l1 = standalone(0, Storage(), 9)
        l1.start_election(1)
        l1.handle(Message.vote_reply(1, 0, 1, True), 2)
        l1.handle(Message.vote_reply(2, 0, 1, True), 3)
        with self.assertRaises(RuntimeError):
            l1.handle(Message.heartbeat(4, 0, 1), 4)


class ConfigAndRngTest(unittest.TestCase):
    def test_config_validation(self):
        with self.assertRaises(ValueError):
            Config(100, 200, 150)
        with self.assertRaises(ValueError):
            Config(300, 150, 50)

    def test_splitmix_reference_value(self):
        self.assertEqual(Rng(12345).next_long(), 2454886589211414944)

    def test_next_int_in_bound(self):
        t = Rng(1)
        self.assertTrue(all(0 <= t.next_int(151) < 151 for _ in range(1000)))


if __name__ == "__main__":
    unittest.main()
