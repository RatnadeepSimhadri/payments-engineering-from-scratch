"""Tests for phases/06-replication-and-consensus/10-chain-and-primary-backup. Run: python3 -m unittest -v"""
import unittest

from main import (USD, ChainClient, ChainCluster, Master, Message, Money, Network, PrimaryBackupMaster, PrimaryBackupNode,
                  Request, Update, fmt_map)


def committed_preserved(c: ChainCluster) -> bool:
    """Every update any client got a reply for must be in the tail's history, and every replica in the chain must agree."""
    in_tail = {u.request.key() for u in c.tail().history}
    known_updates = {u.request.key() for n in c.nodes.values() for u in n.history}
    for cl in c.clients.values():
        for k in cl.replies:
            if k in known_updates and k not in in_tail:
                return False   # a replied update is missing from the tail
    views = {fmt_map(c.node(i).balances) for i in c.master.chain}
    return len(views) <= 1


def usd(cents: int) -> Money:
    return Money.of_minor(cents, USD)


class MoneyTest(unittest.TestCase):
    def test_money(self):
        self.assertEqual(usd(1_999).plus(usd(1)).minor_units, 2_000)
        self.assertEqual(str(usd(-7)), "-0.07 USD")


class ChainHappyPathTest(unittest.TestCase):
    def test_updates_at_head_replies_from_tail(self):
        c = ChainCluster(1, 3, 3, 1, 2)
        u1 = c.update(0, "alice", 10_000)
        u2 = c.update(0, "alice", -2_550)
        self.assertGreater(c.run_until_quiet(500, 60), 0)
        self.assertTrue(c.client(0).reply_for(u1) == usd(10_000) and c.client(0).reply_for(u2) == usd(7_450))
        self.assertTrue(len(c.node(0).history) == 2 and c.node(2).history[1].request == u2)
        self.assertTrue(c.node(2).history[0].seq == 1 and c.node(2).history[1].seq == 2)
        self.assertTrue(not c.node(0).sent and not c.node(1).sent)
        q = c.query(1, "alice")
        c.run_until_quiet(500, 60)
        self.assertEqual(c.client(1).reply_for(q), usd(7_450))
        self.assertTrue(c.node(2).queries_handled == 1 and c.node(0).queries_handled == 0)
        self.assertTrue(not c.violations and c.checks > 0)

    def test_strong_consistency(self):
        v = ChainCluster(2, 8, 3, 0, 2)
        w = v.update(0, "bob", 5_000)
        while not v.head().history:
            v.tick()
        self.assertTrue(len(v.head().history) == 1 and not v.tail().history and not v.client(0).has_reply(w))
        r = v.query(1, "bob")
        v.run_until_quiet(500, 100)
        self.assertEqual(v.client(1).reply_for(r), usd(0))
        r2 = v.query(1, "bob")
        v.run_until_quiet(500, 100)
        self.assertEqual(v.client(1).reply_for(r2), usd(5_000))

    def test_stale_client_redirected(self):
        st = ChainCluster(4, 2, 3, 0, 1)
        st.update(0, "carol", 100)
        st.run_until_quiet(500, 60)
        stale = st.client(0).query("carol", st.now(), [])
        st.network.send(Message.query(100, st.master.head(), stale))   # deliberately ask the head
        st.run_until_quiet(500, 60)
        self.assertTrue(st.client(0).not_tail_redirects == 1 and st.client(0).reply_for(stale) == usd(100))

    def test_duplicate_suppression(self):
        d = ChainCluster(5, 2, 3, 0, 1)
        dup = d.update(0, "dave", 300)
        d.run_until_quiet(500, 60)
        d.network.send(Message.update_msg(100, d.master.head(), Update(0, dup)))   # the retry arrives after commit
        d.run(30)
        self.assertTrue(len(d.tail().history) == 1 and len(d.head().history) == 1)
        self.assertEqual(d.tail().balance("dave"), usd(300))


class ChainFailureTest(unittest.TestCase):
    def test_middle_tail_head_failures_and_extension(self):
        f = ChainCluster(3, 3, 4, 2, 3)
        for i in range(6):
            f.update(i % 3, f"acct{i % 2}", 1_000 + i)
        f.run_until_quiet(500, 60)
        for i in range(4):
            f.update(i % 3, "acct0", 100)
        f.run(2)
        middle = f.master.chain[1]
        f.crash_and_detect(middle, 20)
        self.assertGreater(f.run_until_quiet(1000, 60), 0)
        self.assertTrue(len(f.master.chain) == 3 and middle not in f.master.chain)
        self.assertTrue(len(f.tail().history) == 10 and committed_preserved(f))
        self.assertFalse(f.violations)

        # tail failure: the predecessor becomes tail and replies for everything it had in flight
        for i in range(3):
            f.update(i % 3, "acct1", 7)
        f.run(1)
        f.crash_and_detect(f.master.tail(), 20)
        self.assertGreater(f.run_until_quiet(1000, 60), 0)
        self.assertTrue(len(f.tail().history) == 13 and not f.tail().sent and committed_preserved(f))
        self.assertTrue(all(not cl.pending for cl in f.clients.values()))

        # head failure: unforwarded requests are lost and clients retry them against the new head
        for i in range(3):
            f.update(i % 3, "acct0", 3)
        f.run(1)
        old_head = f.master.head()
        f.crash_and_detect(old_head, 20)
        self.assertGreater(f.run_until_quiet(1000, 60), 0)
        self.assertNotEqual(f.master.head(), old_head)
        self.assertTrue(len(f.tail().history) == 16 and committed_preserved(f))
        self.assertFalse(f.violations)

        # extending the chain: the old tail keeps serving while the history is copied
        f.master.extend(4)
        f.run(1)
        self.assertTrue(f.node(f.master.chain[-2]).transfer_pending)
        f.run_until_quiet(500, 60)
        self.assertTrue(not f.node(f.master.chain[-2]).transfer_pending and f.master.tail() == 4)
        self.assertTrue(len(f.node(4).history) == 16 and f.node(4).balances == f.head().balances)
        f.update(0, "acct0", 1)
        f.run_until_quiet(500, 60)
        self.assertTrue(len(f.node(4).history) == 17 and not f.client(0).pending)
        self.assertFalse(f.violations)


class LoadAndPrimaryBackupTest(unittest.TestCase):
    def test_load_distribution(self):
        lc = ChainCluster(5, 2, 3, 0, 4)
        for i in range(50):
            lc.update(i % 4, f"a{i % 5}", 100)
        for i in range(300):
            lc.query(i % 4, f"a{i % 5}")
        lc.run_until_quiet(20_000, 200)
        self.assertTrue(lc.head().updates_handled == 50 and lc.tail().queries_handled == 300
                        and lc.head().queries_handled == 0 and lc.node(1).queries_handled == 0)

    def test_primary_backup(self):
        pb_net = Network(9, 2)
        pb_nodes = {}
        for i in range(3):
            n = PrimaryBackupNode(i)
            pb_nodes[i] = n
            pb_net.add(n)
        pb_master = PrimaryBackupMaster(pb_net, pb_nodes, [0, 1, 2])
        facade = Master(pb_net, [0])
        pc = ChainClient(100, facade)
        pb_net.add(pc)
        for _ in range(10):
            pb_net.tick()
        out = []
        p1 = pc.update("erin", usd(2_000), pb_net.now(), out)
        p2 = pc.query("erin", pb_net.now(), out)
        pb_net.send_all(out)
        while not pb_net.idle():
            pb_net.tick()
        self.assertTrue(pc.reply_for(p1) == usd(2_000) and len(pb_nodes[1].history) == 1 and len(pb_nodes[2].history) == 1)
        self.assertTrue(pb_nodes[0].updates_handled == 1 and pb_nodes[0].queries_handled == 1 and pc.has_reply(p2))
        # crash the primary with an update half-replicated: backup 1 got it, backup 2 did not
        half = Update(2, Request(100, 99, False, "erin", usd(1)))
        pb_nodes[1].handle(Message.pb_update(0, 1, half), pb_net.now())
        pb_net.crash(0)
        pb_master.fail(0)
        facade.chain[0] = 1
        while not pb_net.idle():
            pb_net.tick()
        self.assertTrue(pb_nodes[1].is_primary and len(pb_nodes[2].history) == 2 and pb_nodes[2].balances == pb_nodes[1].balances)


class SeedsTest(unittest.TestCase):
    def test_ten_seeds_with_random_failures(self):
        clean = 0
        for seed in range(1, 11):
            r1 = ChainCluster(seed, 4, 4, 1, 3)
            for i in range(8):
                r1.update(i % 3, f"x{i % 3}", 10 + i)
            r1.run(3)
            r1.crash_and_detect(r1.master.chain[seed % 4], 15)
            for i in range(6):
                r1.update(i % 3, f"x{i % 3}", 5)
                r1.query(i % 3, "x0")
            r1.run(2)
            r1.crash_and_detect(r1.master.chain[seed % 3], 15)
            r1.master.extend(4)
            for i in range(4):
                r1.update(i % 3, "x1", 1)
            settled = r1.run_until_quiet(5000, 60) > 0
            if settled and not r1.violations and committed_preserved(r1):
                clean += 1
        self.assertEqual(clean, 10)


if __name__ == "__main__":
    unittest.main()
