"""Tests for phases/06-replication-and-consensus/07-raft-safety-and-membership. Run: python3 -m unittest -v"""
import unittest

from main import (DEFAULT_CONFIG, Cluster, Config, Configuration, LogEntry, Message, RaftNode, Rng, Role, Snapshot,
                  Storage, change_until_accepted, propose_until_accepted)

FIVE = {0, 1, 2, 3, 4}


class ConfigurationTest(unittest.TestCase):
    def test_quorums(self):
        three = Configuration.simple({0, 1, 2})
        joint = Configuration.joint({0, 1, 2}, FIVE)
        self.assertTrue(three.has_quorum({0, 1}) and not three.has_quorum({0}))
        self.assertFalse(three.has_quorum({0, 3, 4}))
        self.assertTrue(joint.has_quorum({0, 1, 3}) and not joint.has_quorum({0, 1}) and not joint.has_quorum({2, 3, 4}))
        self.assertEqual(set(joint.all()), FIVE)

    def test_disjoint_majorities(self):
        self.assertTrue(Configuration.disjoint_quorums_possible({0, 1, 2}, FIVE))
        self.assertFalse(Configuration.disjoint_quorums_possible({0, 1, 2}, {0, 1, 2, 3}))
        self.assertFalse(Configuration.disjoint_quorums_possible(FIVE, {0, 1, 2, 3}))
        self.assertFalse(Configuration.disjoint_quorums_possible({0, 1, 2}, {0, 1, 2}))

    def test_encode_decode(self):
        three = Configuration.simple({0, 1, 2})
        joint = Configuration.joint({0, 1, 2}, FIVE)
        self.assertTrue(Configuration.decode(joint.encode()) == joint and Configuration.decode(three.encode()) == three)
        self.assertTrue(Configuration.decode("CFG 0,1,2 -> 1,2,3").is_joint())


class StorageTest(unittest.TestCase):
    def test_compaction(self):
        st = Storage()
        for i in range(1, 7):
            st.append(LogEntry(1, "CFG 0,1,2" if i == 3 else f"SET k{i} v"))
        st.compact(Snapshot(4, 1, {"k1": "v"}, Configuration.simple({0, 1, 2})))
        self.assertTrue(st.snapshot_index == 4 and st.last_index() == 6 and st.first_index() == 5)
        self.assertTrue(st.term_at(4) == 1 and st.term_at(6) == 1)
        with self.assertRaises(RuntimeError):
            st.term_at(2)
        self.assertTrue(st.config_at(6, None) == Configuration.simple({0, 1, 2}) and st.latest_config_index() == -1)
        st.append(LogEntry(2, "CFG 0,1,2 -> 0,1"))
        self.assertTrue(st.config_at(7, None).is_joint() and st.latest_config_index() == 7)
        self.assertFalse(st.config_at(6, None).is_joint())

    def test_spare_server_never_campaigns(self):
        idle = Cluster(3, 1, 0.0, 3, DEFAULT_CONFIG, initial_members={0, 1})
        idle.run(1500)
        self.assertTrue(idle.node(2).role == Role.FOLLOWER and idle.node(2).current_term <= idle.node(0).current_term)
        self.assertIsNotNone(idle.stable_leader())


class MembershipTest(unittest.TestCase):
    def test_grow_then_remove_leader(self):
        c = Cluster(5, 7, 0.0, 5, Config(150, 300, 50, 1000), initial_members={0, 1, 2})
        c.run_until_stable_leader(2000)
        c.propose("SET alice 100")
        grow_leader = c.stable_leader()
        cfg_idx = c.change_membership(FIVE)
        self.assertTrue(cfg_idx > 0 and grow_leader.configuration().is_joint())
        self.assertEqual(grow_leader.change_membership({0, 1, 2, 3}), -1)
        c.propose("SET bob 50")
        done = c.run_until_configured(FIVE, 3000)
        self.assertTrue(done > 0 and c.stable_leader().configuration() == Configuration.simple(FIVE))
        c.propose("SET carol 7")
        c.run_until_applied(c.stable_leader().last_log_index(), 2000)
        self.assertTrue(all(n.configuration() == Configuration.simple(FIVE) for n in c.nodes))
        self.assertTrue(c.state_machines_converged(FIVE) and c.node(3).state_machine.get("bob") == "50")
        self.assertTrue("->" in c.node(4).storage.get(cfg_idx).command and "->" not in c.node(4).storage.get(cfg_idx + 1).command)

        # remove the leader: it steps down after committing C_new and never campaigns again
        old_leader = c.stable_leader()
        four = set(FIVE)
        four.discard(old_leader.id)
        self.assertGreater(c.change_membership(four), 0)
        self.assertTrue(c.run_until_configured(four, 3000) > 0 or old_leader.stepped_down_after_removal)
        c.run_until_stable_leader(3000)
        self.assertTrue(old_leader.stepped_down_after_removal and old_leader.role != Role.LEADER)
        new_leader = c.stable_leader()
        self.assertTrue(new_leader is not None and new_leader.id != old_leader.id and new_leader.id in four)
        c.run(1500)
        self.assertTrue(old_leader.role == Role.FOLLOWER and c.stable_leader() is not None
                        and c.stable_leader().current_term == new_leader.current_term)
        after = c.propose("SET dave 1")
        self.assertTrue(after > 0 and c.run_until_applied(after, 2000) > 0)


class SnapshotTest(unittest.TestCase):
    def test_install_snapshot_to_lagging_follower(self):
        s = Cluster(3, 11, 0.0, 5)
        s.run_until_stable_leader(2000)
        for k in range(1, 6):
            s.propose(f"SET k{k} v{k}")
        s.run_until_applied(s.stable_leader().last_log_index(), 1000)
        lead = s.stable_leader()
        follower = (lead.id + 1) % 3
        s.crash(follower)
        for k in range(6, 17):
            s.propose(f"SET k{k} v{k}")
            s.run(20)
        s.run(200)
        self.assertTrue(lead.snapshots_taken >= 1 and lead.storage.snapshot_index >= 8)
        self.assertTrue(lead.state_machine.get("k1") == "v1" and lead.state_machine.get("k16") == "v16")
        s.restart(follower)
        s.run_until_applied(lead.last_log_index(), 3000)
        f = s.node(follower)
        self.assertTrue(f.snapshots_installed >= 1 and lead.snapshots_sent >= 1)
        self.assertTrue(f.storage.snapshot_index >= 8 and f.storage.first_index() > 8)
        self.assertTrue(f.state_machine.get("k1") == "v1" and s.state_machines_converged({0, 1, 2}))
        self.assertEqual(f.storage.snapshot.config, Configuration.simple({0, 1, 2}))

    def test_rule_6_rule_8_and_restart(self):
        r6 = Storage()
        for i in range(1, 6):
            r6.append(LogEntry(1, f"SET k{i} v"))
        f6 = RaftNode(0, Configuration.simple({0, 1, 2}), r6, Rng(1), DEFAULT_CONFIG, 0)
        f6.storage.save(1, -1)
        f6.handle(Message.install_snapshot(1, 0, 1, Snapshot(3, 1, {"k1": "v", "k2": "v", "k3": "v"}, Configuration.simple({0, 1, 2}))), 1)
        self.assertTrue(f6.storage.snapshot_index == 3 and f6.last_log_index() == 5 and f6.storage.get(5).command == "SET k5 v")
        self.assertTrue(f6.last_applied == 3 and f6.state_machine.get("k3") == "v" and f6.commit_index == 3)
        f6.handle(Message.install_snapshot(1, 0, 1, Snapshot(2, 1, {}, Configuration.simple({0, 1, 2}))), 2)
        self.assertEqual(f6.storage.snapshot_index, 3)
        re = RaftNode(0, Configuration.simple({0, 1, 2}), r6, Rng(2), DEFAULT_CONFIG, 0)
        self.assertTrue(re.last_applied == 3 and re.state_machine.get("k2") == "v")


class CheckerTest(unittest.TestCase):
    def test_election_safety_violation_detected(self):
        forged = Cluster(2, 3, 0.0, 2)
        forged.node(0).storage.save(5, 0)
        forged.node(0).role = Role.LEADER
        forged.node(1).storage.save(5, 1)
        forged.node(1).role = Role.LEADER
        with self.assertRaises(RuntimeError):
            forged.check_invariants()

    def test_leader_completeness_violation_detected(self):
        forged2 = Cluster(2, 4, 0.0, 2)
        forged2.node(0).storage.append(LogEntry(1, "SET x 1"))
        forged2.node(0).commit_index = 1
        forged2.node(0).storage.save(1, -1)
        forged2.check_invariants()   # records index 1 as committed
        forged2.node(1).storage.save(2, 1)
        forged2.node(1).role = Role.LEADER   # empty log, claims term 2
        with self.assertRaises(RuntimeError):
            forged2.check_invariants()


class SeedsTest(unittest.TestCase):
    def test_eight_seeds_converge_through_3_5_4(self):
        converged = changes = 0
        for seed in range(1, 9):
            r = Cluster(5, seed, 0.2, 10, DEFAULT_CONFIG, initial_members={0, 1, 2})
            r.run_until_stable_leader(4000)
            for k in range(1, 6):
                propose_until_accepted(r, f"SET k{k} v{k}")
                r.run(30)
            change_until_accepted(r, FIVE)
            for k in range(6, 11):
                propose_until_accepted(r, f"SET k{k} v{k}")
                r.run(30)
            if r.run_until_configured(FIVE, 6000) > 0:
                changes += 1
            victim = r.stable_leader() if r.stable_leader() is not None else r.node(0)
            r.crash(victim.id)
            r.run_until_stable_leader(4000)
            for k in range(11, 19):
                propose_until_accepted(r, f"SET k{k} v{k}")
                r.run(30)
            r.restart(victim.id)
            r.run_until_stable_leader(4000)
            four_b = set(FIVE)
            four_b.discard((victim.id + 1) % 5)
            change_until_accepted(r, four_b)
            if r.run_until_configured(four_b, 6000) > 0:
                changes += 1
            propose_until_accepted(r, "SET final yes")
            fin = r.stable_leader()
            if fin is not None:
                r.run_until_applied(fin.last_log_index(), 4000)
            if fin is not None and r.state_machines_converged(four_b) and fin.state_machine.get("final") == "yes" \
                    and fin.state_machine.get("k18") == "v18":
                converged += 1
        self.assertEqual(converged, 8)
        self.assertEqual(changes, 16)


if __name__ == "__main__":
    unittest.main()
