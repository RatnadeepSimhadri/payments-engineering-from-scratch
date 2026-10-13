"""Tests for phases/06-replication-and-consensus/04-paxos. Run: python3 -m unittest -v"""
import unittest

from main import NONE, Acceptor, Ballot, Learner, Message, MessageKind, PaxosCluster, Proposer, Rng


class RngTest(unittest.TestCase):
    def test_splitmix64_reference_vector(self):
        # SplitMix64 reference outputs for seed 0 so all three languages share one random stream
        rng = Rng(0)
        self.assertEqual(rng.next_long(), 0xE220A8397B1DCDAF)
        self.assertEqual(rng.next_long(), 0x6E789E6AA1B965F4)

    def test_same_seed_same_stream(self):
        r1, r2 = Rng(42), Rng(42)
        self.assertEqual(r1.next_int(1000), r2.next_int(1000))
        self.assertEqual(r1.next_double(), r2.next_double())
        self.assertTrue(0.0 <= r1.next_double() < 1.0)


class BallotTest(unittest.TestCase):
    def test_proposer_id_breaks_ties(self):
        self.assertGreater(Ballot(1, 4), Ballot(1, 3))

    def test_round_dominates_proposer_id(self):
        self.assertGreater(Ballot(2, 0), Ballot(1, 9))

    def test_none_is_below_every_real_ballot(self):
        self.assertLess(NONE, Ballot(1, 0))
        self.assertTrue(NONE.is_none())
        self.assertEqual(str(Ballot(3, 4)), "3.4")


class AcceptorTest(unittest.TestCase):
    def test_promise_one_never_accept_below_promised(self):
        a = Acceptor(0, [])
        out = a.handle(Message.prepare(3, 0, Ballot(2, 3)), 0)
        self.assertEqual(len(out), 1)
        self.assertIs(out[0].kind, MessageKind.PROMISE)
        self.assertEqual(a.promised, Ballot(2, 3))
        self.assertTrue(out[0].prior_ballot.is_none())
        self.assertIsNone(out[0].value)
        out = a.handle(Message.prepare(4, 0, Ballot(1, 4)), 0)
        self.assertIs(out[0].kind, MessageKind.NACK)
        self.assertEqual(out[0].prior_ballot, Ballot(2, 3))
        out = a.handle(Message.accept(4, 0, Ballot(1, 4), "X"), 0)
        self.assertIs(out[0].kind, MessageKind.NACK)
        self.assertTrue(a.accepted_ballot.is_none())
        out = a.handle(Message.accept(3, 0, Ballot(2, 3), "Y"), 0)
        self.assertIs(out[0].kind, MessageKind.ACCEPTED)
        self.assertEqual(a.accepted_value, "Y")

    def test_accepted_is_forwarded_to_every_learner(self):
        out = Acceptor(1, [7, 8]).handle(Message.accept(3, 1, Ballot(1, 3), "v"), 0)
        self.assertEqual(len(out), 3)
        self.assertEqual(sorted(m.to for m in out), [3, 7, 8])

    def test_promise_two_reports_what_was_accepted(self):
        a = Acceptor(0, [])
        a.handle(Message.prepare(3, 0, Ballot(2, 3)), 0)
        a.handle(Message.accept(3, 0, Ballot(2, 3), "Y"), 0)
        out = a.handle(Message.prepare(5, 0, Ballot(3, 5)), 0)
        self.assertEqual(out[0].prior_ballot, Ballot(2, 3))
        self.assertEqual(out[0].value, "Y")


class ProposerTest(unittest.TestCase):
    def test_adopts_highest_prior_value_not_its_own(self):
        p = Proposer(3, [0, 1, 2], 10)
        prepares = p.propose("mine", 0)
        self.assertEqual(len(prepares), 3)
        self.assertTrue(all(m.kind is MessageKind.PREPARE for m in prepares))
        b = p.ballot
        p.handle(Message.promise(0, 3, b, Ballot(1, 9), "old"), 1)
        accepts = p.handle(Message.promise(1, 3, b, Ballot(2, 9), "newer"), 1)
        self.assertEqual(len(accepts), 3)
        self.assertIs(accepts[0].kind, MessageKind.ACCEPT)
        self.assertEqual(p.proposing, "newer")
        self.assertEqual(accepts[0].value, "newer")

    def test_stale_replies_for_an_old_ballot_are_ignored(self):
        p = Proposer(3, [0, 1, 2], 10)
        p.propose("mine", 0)
        self.assertEqual(p.handle(Message.promise(2, 3, Ballot(0, 3), NONE, None), 1), [])

    def test_nack_restarts_above_the_promised_ballot(self):
        q = Proposer(4, [0, 1, 2], 10)
        q.propose("q", 0)
        retry = q.handle(Message.nack(0, 4, q.ballot, Ballot(7, 3)), 1)
        self.assertEqual(q.ballot, Ballot(8, 4))
        self.assertEqual(len(retry), 3)
        self.assertEqual(q.restarts, 1)

    def test_timeout_restarts_a_stalled_round(self):
        q = Proposer(4, [0, 1, 2], 10)
        q.propose("q", 0)
        self.assertEqual(q.tick(9), [])
        self.assertEqual(len(q.tick(10)), 3)
        self.assertEqual(q.round, 2)
        self.assertEqual(q.tick(11), [])


class ClusterTest(unittest.TestCase):
    def test_single_proposer_chooses(self):
        c = PaxosCluster(3, 1, 1, 1, 0.0, 1, 10)
        c.propose(0, "only")
        self.assertTrue(c.run(20))
        self.assertEqual(c.first_chosen, "only")
        self.assertLessEqual(c.chosen_at, 4)
        self.assertEqual(c.learners[0].learned, "only")
        self.assertEqual(c.proposers[0].decided, "only")

    def test_dueling_proposers_converge_on_one_value(self):
        c = PaxosCluster(3, 2, 1, 7, 0.0, 1, 10)
        c.propose(0, "A")
        c.propose(1, "B")
        for _ in range(12):
            c.tick()
        self.assertIsNotNone(c.first_chosen)
        self.assertEqual(c.first_chosen, c.chosen_value())
        self.assertIsNotNone(c.proposers[0].decided)
        self.assertEqual(c.proposers[0].decided, c.proposers[1].decided)

    def test_lost_messages_never_violate_safety(self):
        decided = 0
        for seed in range(1, 201):
            c = PaxosCluster(5, 3, 2, seed, 0.25, 3, 12)
            c.propose(0, "A")
            c.propose(1, "B")
            c.propose(2, "C")
            if c.run(400):  # check_safety raises on a second distinct chosen value
                decided += 1
            for l in c.learners:
                if l.learned is not None:
                    self.assertEqual(l.learned, c.first_chosen)
            for p in c.proposers:
                if p.decided is not None:
                    self.assertEqual(p.decided, c.first_chosen)
        self.assertGreaterEqual(decided, 180)

    def test_same_seed_replays_the_same_run(self):
        runs = []
        for _ in range(2):
            d = PaxosCluster(5, 3, 1, 99, 0.3, 3, 12)
            d.propose(0, "A")
            d.propose(1, "B")
            d.propose(2, "C")
            d.run(400)
            runs.append((d.network.delivered, d.network.dropped, d.chosen_at, d.first_chosen))
        self.assertEqual(runs[0], runs[1])

    def test_minority_partition_cannot_choose_and_heal_recovers(self):
        part = PaxosCluster(5, 1, 1, 3, 0.0, 2, 8)
        part.network.isolate(0)
        part.network.isolate(1)
        part.network.isolate(2)
        part.propose(0, "P")
        self.assertFalse(part.run(100))
        self.assertIsNone(part.chosen_value())
        part.network.heal()
        self.assertTrue(part.run(100))
        self.assertEqual(part.first_chosen, "P")

    def test_constructed_duel_livelocks_until_the_schedule_relaxes(self):
        duel = PaxosCluster(3, 2, 1, 1, 0.0, 1, 1000)
        not_accept = lambda m: m.kind is not MessageKind.ACCEPT  # noqa: E731
        duel.propose(0, "A")
        duel.network.drain(not_accept)
        duel.propose(1, "B")
        duel.network.drain(not_accept)
        for _ in range(4):
            duel.network.drain(lambda m: m.kind is MessageKind.ACCEPT and m.from_ == 3)
            duel.network.drain(not_accept)
            duel.network.drain(lambda m: m.kind is MessageKind.ACCEPT and m.from_ == 4)
            duel.network.drain(not_accept)
        self.assertIsNone(duel.chosen_value())
        self.assertGreaterEqual(duel.max_round(), 8)
        self.assertTrue(all(a.accepted_ballot.is_none() for a in duel.acceptors))
        duel.network.drain(lambda m: True)
        self.assertIsNotNone(duel.chosen_value())
        self.assertEqual(duel.chosen_value(), duel.first_chosen)

    def test_learner_refuses_to_learn_two_values(self):
        l = Learner(9, 3)
        l.handle(Message.accepted(0, 9, Ballot(1, 3), "X"), 0)
        l.handle(Message.accepted(1, 9, Ballot(1, 3), "X"), 0)
        self.assertEqual(l.learned, "X")
        l.handle(Message.accepted(0, 9, Ballot(2, 4), "Y"), 0)
        with self.assertRaises(RuntimeError):
            l.handle(Message.accepted(2, 9, Ballot(2, 4), "Y"), 0)


if __name__ == "__main__":
    unittest.main()
