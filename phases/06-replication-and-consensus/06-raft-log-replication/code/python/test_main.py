"""Tests for phases/06-replication-and-consensus/06-raft-log-replication. Run: python3 -m unittest -v"""
import unittest

from main import (DEFAULT_CONFIG, Cluster, KVStore, LogEntry, Message, RaftNode, Rng, Role, Storage,
                  check_log_matching, propose_until_accepted)


def standalone(node_id: int, storage: Storage) -> RaftNode:
    return RaftNode(node_id, [p for p in range(5) if p != node_id], storage, Rng(node_id + 1), DEFAULT_CONFIG, 0)


def follower(*terms: int) -> RaftNode:
    """Node 1 with the given term sequence in its log, e.g. follower(1, 1, 2) has three entries with terms 1, 1, 2."""
    s = Storage()
    for i, t in enumerate(terms):
        s.append(LogEntry(t, f"SET k{i + 1} old"))
    s.save(terms[-1] if terms else 0, -1)
    return standalone(1, s)


def leader_of_term(term: int, *log_terms: int) -> RaftNode:
    s = Storage()
    for i, t in enumerate(log_terms):
        s.append(LogEntry(t, f"SET k{i + 1} v"))
    s.save(term - 1, 0)
    leader = standalone(0, s)
    leader.start_election(1)
    leader.handle(Message.vote_reply(1, 0, leader.current_term, True), 2)
    leader.handle(Message.vote_reply(2, 0, leader.current_term, True), 3)
    return leader


def all_logs_equal(c: Cluster) -> bool:
    return len({c.log_of(i) for i in range(c.size)}) == 1


class HappyPathTest(unittest.TestCase):
    def test_three_commands_replicate_commit_and_apply(self):
        c = Cluster(5, 7, 0.0, 5)
        c.run_until_stable_leader(2000)
        leader = c.stable_leader()
        i1, i2, i3 = c.propose("SET alice 100"), c.propose("SET bob 50"), c.propose("SET alice 75")
        self.assertEqual((i1, i2, i3), (2, 3, 4), "indexes are consecutive after the election no-op")
        self.assertEqual(leader.log[i3].term, leader.current_term)
        self.assertGreater(c.run_until_applied(i3, 1000), 0)
        self.assertTrue(all_logs_equal(c))
        self.assertTrue(c.state_machines_converged())
        self.assertEqual(c.node(0).state_machine.get("alice"), "75")
        self.assertEqual(c.node(3).state_machine.get("bob"), "50")
        self.assertTrue(all(n.commit_index == i3 and n.last_applied == i3 for n in c.nodes))

    def test_propose_on_a_follower_is_refused(self):
        c = Cluster(5, 7, 0.0, 5)
        c.run_until_stable_leader(2000)
        leader = c.stable_leader()
        self.assertEqual(c.node((leader.id + 1) % 5).propose("SET x 1"), -1)


class ConflictResolutionTest(unittest.TestCase):
    def test_stale_leader_entries_are_truncated_and_replaced(self):
        c = Cluster(5, 7, 0.0, 5)
        c.run_until_stable_leader(2000)
        c.propose("SET alice 100")
        c.propose("SET bob 50")
        c.run_until_applied(c.propose("SET alice 75"), 1000)
        old = c.stable_leader()
        minority = (old.id + 1) % 5
        c.partition({old.id, minority})
        stale1 = c.propose_to(old.id, "SET alice 999")
        stale2 = c.propose_to(old.id, "SET carol 1")
        c.run(600)
        self.assertEqual((stale1, stale2, old.last_log_index()), (5, 6, 6), "stale leader accepted the writes into its log")
        self.assertEqual(old.commit_index, 4, "stale leader could not commit them")
        fresh = next(n for n in c.leaders() if n is not old)
        self.assertGreater(fresh.current_term, old.current_term)
        self.assertEqual(fresh.commit_index, 5)
        self.assertEqual(fresh.log[5].command, "NOOP", "new leader committed its no-op at the stale entries' index")
        self.assertEqual(c.node(minority).last_log_index(), 6)
        c.heal()
        c.run_until_stable_leader(2000)
        c.run_until_applied(c.propose("SET dave 7"), 1000)
        self.assertTrue(all_logs_equal(c))
        self.assertEqual(c.node(minority).log[5].command, "NOOP")
        self.assertEqual(c.node(minority).log[6].command, "SET dave 7")
        self.assertEqual(c.node(minority).state_machine.get("alice"), "75", "uncommitted write never applied")
        self.assertIsNone(c.node(0).state_machine.get("carol"))
        self.assertEqual(old.role, Role.FOLLOWER)
        self.assertTrue(c.state_machines_converged())
        self.assertEqual(fresh.log[5].command, "NOOP", "new leader's committed prefix unchanged by the merge")
        self.assertEqual(fresh.role, Role.LEADER)


class CommitRuleTest(unittest.TestCase):
    def test_commit_only_on_majority(self):
        q = Cluster(5, 11, 0.0, 5)
        q.run_until_stable_leader(2000)
        lead = q.stable_leader()
        others = list(lead.peers)
        for victim in others[:3]:
            q.crash(victim)
        idx = q.propose("SET balance 1000")
        q.run(400)
        self.assertLess(lead.commit_index, idx)
        self.assertEqual(lead.replicated_count(idx), 2)
        self.assertIsNone(lead.state_machine.get("balance"), "leader has not applied the uncommitted entry")
        q.restart(others[0])
        q.run(400)
        self.assertGreaterEqual(lead.commit_index, idx)
        self.assertGreaterEqual(lead.replicated_count(idx), 3)
        self.assertEqual(lead.state_machine.get("balance"), "1000")
        self.assertEqual(q.node(others[0]).state_machine.get("balance"), "1000", "restarted node rebuilt from the log")

    def test_only_current_term_entries_commit_by_counting(self):
        f8 = leader_of_term(5, 2, 2)
        f8.handle(Message.append_reply(1, 0, 5, True, 2), 4)
        f8.handle(Message.append_reply(2, 0, 5, True, 2), 5)
        self.assertEqual(f8.commit_index, 0, "term-2 entries on 3/5 replicas are not committed in term 5")
        f8.handle(Message.append_reply(1, 0, 5, True, 3), 6)
        f8.handle(Message.append_reply(2, 0, 5, True, 3), 7)
        self.assertEqual(f8.commit_index, 3, "the current-term no-op commits itself and everything before it")
        self.assertEqual(len(f8.state_machine.applied_commands), 3)

    def test_restart_keeps_log_and_rebuilds_volatile_state(self):
        rs = Cluster(3, 5, 0.0, 3)
        rs.run_until_stable_leader(2000)
        w = rs.propose("SET k 1")
        rs.run_until_applied(w, 500)
        victim = (rs.stable_leader().id + 1) % 3
        length_before = rs.node(victim).last_log_index()
        rs.crash(victim)
        back = rs.restart(victim)
        self.assertEqual(back.last_log_index(), length_before)
        self.assertEqual(back.log[w].command, "SET k 1")
        self.assertEqual((back.commit_index, back.last_applied), (0, 0))
        self.assertIsNone(back.state_machine.get("k"))
        rs.run(200)
        self.assertGreaterEqual(back.last_applied, w)
        self.assertEqual(back.state_machine.get("k"), "1")


class AppendEntriesReceiverTest(unittest.TestCase):
    def test_rules_one_to_five_in_order(self):
        f = follower(1, 1, 2)
        r1 = f.handle(Message.append_entries(0, 1, 2, 5, 2, [], 0), 10)[0]
        self.assertFalse(r1.success, "rule 2: prevLogIndex beyond the log is rejected")
        self.assertEqual(f.consistency_rejections, 1)
        r2 = f.handle(Message.append_entries(0, 1, 2, 3, 1, [], 0), 11)[0]
        self.assertFalse(r2.success, "rule 2: prevLogTerm mismatch is rejected")
        self.assertEqual(f.last_log_index(), 3)
        r3 = f.handle(Message.append_entries(0, 1, 2, 3, 2, [], 0), 12)[0]
        self.assertTrue(r3.success)
        self.assertEqual(r3.match_index, 3)
        two = [LogEntry(3, "SET k2 new"), LogEntry(3, "SET k3 new")]
        r4 = f.handle(Message.append_entries(0, 1, 3, 1, 1, two, 0), 13)[0]
        self.assertTrue(r4.success, "rule 3: conflicting entries truncated and replaced")
        self.assertEqual(f.last_log_index(), 3)
        self.assertEqual(f.log[2].term, 3)
        self.assertEqual(f.log[3].command, "SET k3 new")
        r5 = f.handle(Message.append_entries(0, 1, 3, 1, 1, two, 0), 14)[0]
        self.assertTrue(r5.success, "rule 4: a duplicate AppendEntries is idempotent")
        self.assertEqual(f.last_log_index(), 3)
        r6 = f.handle(Message.append_entries(0, 1, 3, 3, 3, [LogEntry(3, "SET k4 new")], 10), 15)[0]
        self.assertTrue(r6.success)
        self.assertEqual((f.commit_index, f.last_applied), (4, 4), "rule 5: commitIndex = min(leaderCommit, last new)")
        self.assertEqual(f.state_machine.applied_commands, ["SET k1 old", "SET k2 new", "SET k3 new", "SET k4 new"])
        r7 = f.handle(Message.append_entries(0, 1, 3, 2, 3, [], 2), 16)[0]
        self.assertTrue(r7.success, "heartbeat with an older prefix does not truncate newer matching entries")
        self.assertEqual((f.last_log_index(), f.commit_index), (4, 4))
        r8 = f.handle(Message.append_entries(9, 1, 2, 4, 3, [], 4), 17)[0]
        self.assertFalse(r8.success, "rule 1: stale-term AppendEntries rejected")
        self.assertEqual(r8.term, 3)
        self.assertEqual(f.last_log_index(), 4)

    def test_two_leaders_in_one_term_is_a_safety_violation(self):
        with self.assertRaises(RuntimeError):
            leader_of_term(2).handle(Message.append_entries(4, 0, 2, 0, 0, [], 0), 9)


class LeaderSideTest(unittest.TestCase):
    def test_next_index_backs_off_and_commit_needs_majority(self):
        l = leader_of_term(3, 1, 1, 2)
        self.assertEqual(l.next_index[1], 4)
        self.assertEqual(l.log[4].command, "NOOP", "leader appends a no-op on election")
        retry = l.handle(Message.append_reply(1, 0, 3, False, 0), 10)
        self.assertEqual(l.next_index[1], 3)
        self.assertEqual(len(retry), 1)
        self.assertEqual(retry[0].prev_log_index, 2)
        self.assertEqual(len(retry[0].entries), 2, "rejection decrements nextIndex and resends immediately")
        for t in (11, 12):
            l.handle(Message.append_reply(1, 0, 3, False, 0), t)
        floor = l.handle(Message.append_reply(1, 0, 3, False, 0), 13)
        self.assertEqual(l.next_index[1], 1, "nextIndex never drops below 1")
        self.assertEqual((floor[0].prev_log_index, floor[0].prev_log_term), (0, 0))
        l.handle(Message.append_reply(1, 0, 3, True, 4), 14)
        self.assertEqual((l.match_index[1], l.next_index[1]), (4, 5))
        self.assertEqual(l.commit_index, 0, "one follower is not a majority of five")
        l.handle(Message.append_reply(2, 0, 3, True, 4), 15)
        self.assertEqual((l.commit_index, l.last_applied), (4, 4), "second follower makes a majority")
        l.handle(Message.append_reply(3, 0, 3, True, 2), 16)
        self.assertEqual(l.commit_index, 4, "an older matchIndex never moves commitIndex backwards")
        l.handle(Message.append_reply(3, 0, 2, True, 4), 17)
        self.assertEqual(l.match_index[3], 2, "replies from an older term are ignored")
        stale = l.handle(Message.append_entries(7, 0, 2, 0, 0, [LogEntry(2, "SET hijack 1")], 0), 18)[0]
        self.assertFalse(stale.success)
        self.assertEqual(l.last_log_index(), 4, "leader never overwrites its own log")
        self.assertEqual(l.role, Role.LEADER)

    def test_election_log_check_uses_the_real_log(self):
        voter = follower(1, 2)
        self.assertFalse(voter.handle(Message.request_vote(3, 1, 3, 1, 2), 1)[0].success)
        self.assertTrue(voter.handle(Message.request_vote(3, 1, 3, 1, 3), 2)[0].success)


class PropertyTest(unittest.TestCase):
    def test_lossy_seeds_with_crash_all_converge(self):
        converged = 0
        for seed in range(1, 26):
            r = Cluster(5, seed, 0.2, 10)
            r.run_until_stable_leader(3000)
            for k in range(1, 7):
                propose_until_accepted(r, f"SET k{k} v{k}")
                r.run(40)
            victim = r.stable_leader() or r.node(0)
            r.crash(victim.id)
            r.run_until_stable_leader(3000)
            for k in range(7, 13):
                propose_until_accepted(r, f"SET k{k} v{k}")
                r.run(40)
            r.restart(victim.id)
            r.run_until_stable_leader(3000)
            fin = r.stable_leader()
            if fin is not None:
                r.run_until_applied(fin.last_log_index(), 3000)
            if (r.state_machines_converged() and fin is not None
                    and fin.state_machine.get("k12") == "v12" and fin.state_machine.get("k1") == "v1"):
                converged += 1
        self.assertEqual(converged, 25, "25 lossy seeds with a leader crash all converge on the same 12 writes")

    def test_same_seed_same_trace(self):
        d1, d2 = Cluster(5, 42, 0.15, 8), Cluster(5, 42, 0.15, 8)
        d1.run_until_stable_leader(3000)
        d2.run_until_stable_leader(3000)
        d1.propose("SET a 1")
        d2.propose("SET a 1")
        d1.run(500)
        d2.run(500)
        self.assertGreater(len(d1.events), 3)
        self.assertEqual(d1.events, d2.events)

    def test_log_matching_checker_fires_and_allows_divergence_after_last_common_term(self):
        sentinel = LogEntry(0, "<sentinel>")
        with self.assertRaises(RuntimeError):
            check_log_matching([sentinel, LogEntry(1, "SET a 1"), LogEntry(2, "SET b 2")],
                               [sentinel, LogEntry(1, "SET a 9"), LogEntry(2, "SET b 2")], 0, 1)
        check_log_matching([sentinel, LogEntry(1, "SET a 1"), LogEntry(2, "SET b 2")],
                           [sentinel, LogEntry(1, "SET a 1"), LogEntry(3, "SET c 3")], 0, 1)


class KVStoreTest(unittest.TestCase):
    def test_set_del_noop(self):
        kv = KVStore()
        for cmd in ("SET a 1", "SET b 2", "DEL a", "NOOP"):
            kv.apply(cmd)
        self.assertIsNone(kv.get("a"))
        self.assertEqual(kv.get("b"), "2")
        self.assertEqual(len(kv.applied_commands), 4)
        self.assertEqual(str(kv), "{b=2}")

    def test_rejects_unknown_commands(self):
        with self.assertRaises(ValueError):
            KVStore().apply("INCR a")


if __name__ == "__main__":
    unittest.main()
