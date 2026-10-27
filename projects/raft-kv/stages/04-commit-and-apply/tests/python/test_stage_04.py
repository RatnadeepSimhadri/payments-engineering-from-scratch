"""Stage 04 tests for projects/raft-kv (Python): commit_index by majority with the current-term rule, the KV state machine,
client PUT/GET through the leader, and a linearizability check on the recorded history.
Run by the grader after stages 01, 02 and 03. Port of Stage04Test.java: same checks, same names, same order."""

import unittest

from main import (
    DEFAULT_CONFIG,
    Checker,
    CheckResult,
    Cluster,
    History,
    KVStore,
    LogEntry,
    Message,
    Op,
    OpKind,
    RaftNode,
    Rng,
    Role,
    Storage,
    no_trace,
)


def peers_of(id: int, size: int) -> list[int]:
    return [j for j in range(size) if j != id]


def standalone(id: int, storage: Storage, seed: int) -> RaftNode:
    return RaftNode(id, peers_of(id, 5), storage, Rng(seed), DEFAULT_CONFIG, 0, no_trace)


def elected_leader(storage: Storage, seed: int) -> RaftNode:
    n = standalone(0, storage, seed)
    n.start_election(1)
    n.handle(Message.vote_reply(1, 0, n.current_term, True), 2)
    n.handle(Message.vote_reply(2, 0, n.current_term, True), 3)
    return n


def applied_prefix_matches_log(n: RaftNode) -> bool:
    if n.last_applied > n.commit_index or n.commit_index > n.last_log_index():
        return False
    if len(n.state_machine.applied_commands) != n.last_applied:
        return False
    return all(n.state_machine.applied_commands[k - 1] == n.log[k].command for k in range(1, n.last_applied + 1))


def respects_real_time(h: History, order: tuple[int, ...]) -> bool:
    for a in range(len(order)):
        for b in range(a + 1, len(order)):
            if h.op(order[b]).precedes(h.op(order[a])):
                return False
    return len(order) == h.size()


class Stage04CommitTest(unittest.TestCase):
    def test_kvstore_applies_put_get_noop_and_rejects_garbage(self):
        kv = KVStore()
        self.assertTrue(kv.apply("PUT balance:alice 100") == "OK" and kv.get("balance:alice") == "100", "PUT returns OK and stores")
        self.assertEqual(kv.apply("GET balance:alice"), "100", "GET returns the stored value")
        self.assertTrue(kv.apply("GET nobody") == "" and kv.get("nobody") is None, "GET of an absent key returns the empty string")
        self.assertTrue(kv.apply("NOOP") == "" and len(kv.data) == 1, "NOOP returns the empty string and changes nothing")
        self.assertTrue(kv.apply("PUT note hello world") == "OK" and kv.get("note") == "hello world", "a PUT value may contain spaces")
        self.assertTrue(kv.apply("PUT balance:alice 75") == "OK" and kv.get("balance:alice") == "75", "PUT overwrites")
        self.assertTrue(kv.applied() == 6 and kv.applied_commands[1] == "GET balance:alice" and kv.results[1] == "100" and kv.results[2] == "",
                        "applied commands and results are recorded in order")
        with self.assertRaises(ValueError, msg="PUT without a value is rejected"):
            kv.apply("PUT lonely")
        with self.assertRaises(ValueError, msg="GET with extra arguments is rejected"):
            kv.apply("GET a b")
        with self.assertRaises(ValueError, msg="unknown commands are rejected"):
            kv.apply("TRANSFER a b 5")
        self.assertEqual(kv.applied(), 6, "a rejected command is not recorded")

    def test_a_leader_commits_an_entry_once_a_majority_holds_it_then_applies_in_order(self):
        ls = Storage()
        l = elected_leader(ls, 1)
        term = l.current_term
        i1 = l.propose("PUT a 1")
        i2 = l.propose("PUT a 2")
        l.handle(Message.append_reply(1, 0, term, True, i2), 10)
        self.assertTrue(l.commit_index == 0 and l.last_applied == 0 and l.state_machine.applied() == 0, "two of five replicas (leader + one) is not a majority: nothing commits")
        l.handle(Message.append_reply(2, 0, term, True, i1), 11)
        self.assertTrue(l.commit_index == i1 and l.last_applied == i1, "three of five hold index i1: commit_index advances to i1, not i2")
        self.assertTrue(applied_prefix_matches_log(l) and l.state_machine.get("a") == "1", "the state machine applied everything through i1 in log order")
        l.handle(Message.append_reply(3, 0, term, True, i2), 12)
        self.assertTrue(l.commit_index == i2 and l.state_machine.get("a") == "2" and applied_prefix_matches_log(l), "a third holder of i2 commits it")
        l.handle(Message.append_reply(4, 0, term, True, i1), 13)
        self.assertEqual(l.commit_index, i2, "a later, lower match_index never moves commit_index backwards")
        self.assertLessEqual(l.last_applied, l.commit_index, "last_applied never exceeds commit_index")

    def test_only_current_term_entries_are_committed_by_counting_replicas(self):
        s = Storage()
        s.append(LogEntry(2, "PUT x 1"))
        s.append(LogEntry(2, "PUT x 2"))
        s.save(4, 0)
        l = standalone(0, s, 2)
        l.start_election(1)
        l.handle(Message.vote_reply(1, 0, l.current_term, True), 2)
        l.handle(Message.vote_reply(2, 0, l.current_term, True), 3)
        self.assertTrue(l.role is Role.LEADER and l.current_term == 5 and s.term_at(2) == 2, "precondition: leader of term 5 with two term-2 entries")
        l.handle(Message.append_reply(1, 0, 5, True, 2), 4)
        l.handle(Message.append_reply(2, 0, 5, True, 2), 5)
        self.assertTrue(l.commit_index == 0 and l.state_machine.applied() == 0, "term-2 entries on 3/5 replicas do NOT commit under a term-5 leader")
        cur = l.propose("PUT x 3")
        self.assertEqual(s.term_at(cur), 5, "the new entry carries the current term")
        l.handle(Message.append_reply(1, 0, 5, True, cur), 6)
        l.handle(Message.append_reply(2, 0, 5, True, cur), 7)
        self.assertTrue(l.commit_index == cur and l.last_applied == cur, "a current-term entry on 3/5 commits itself and everything before it")
        self.assertTrue(l.state_machine.applied_commands[0] == "PUT x 1" and l.state_machine.get("x") == "3" and applied_prefix_matches_log(l), "the older entries were applied first, in order")

    def test_a_follower_commits_up_to_min_leader_commit_last_new_index_and_applies_in_order(self):
        fs = Storage()
        f = standalone(1, fs, 3)
        f.handle(Message.append_entries(0, 1, 1, 0, 0, [LogEntry(1, "PUT a 1"), LogEntry(1, "PUT b 2")], 5), 10)
        self.assertTrue(f.commit_index == 2 and f.last_applied == 2, "leader_commit beyond what the follower holds is capped at the last new entry")
        self.assertTrue(f.state_machine.get("a") == "1" and f.state_machine.get("b") == "2", "the follower's state machine reflects the two applied writes")
        f.handle(Message.append_entries(0, 1, 1, 2, 1, [LogEntry(1, "PUT a 3")], 2), 11)
        self.assertTrue(fs.last_index() == 3 and f.commit_index == 2 and f.state_machine.get("a") == "1", "a new entry with an unchanged leader_commit is stored but not applied")
        f.handle(Message.append_entries(0, 1, 1, 3, 1, [], 3), 12)
        self.assertTrue(f.commit_index == 3 and f.state_machine.get("a") == "3" and applied_prefix_matches_log(f), "a heartbeat that raises leader_commit applies the pending entry")
        f.handle(Message.append_entries(0, 1, 1, 3, 1, [], 1), 13)
        self.assertEqual(f.commit_index, 3, "a lower leader_commit never moves commit_index backwards")
        f.handle(Message.append_entries(0, 1, 1, 7, 1, [], 9), 14)
        self.assertTrue(f.commit_index == 3 and f.last_applied == 3, "a refused AppendEntries (consistency check) does not touch commit_index")
        self.assertTrue(applied_prefix_matches_log(f), "applied commands are exactly log[1..last_applied]")

    def test_client_put_get_through_the_leader_on_a_quiet_cluster(self):
        c = Cluster(5, 7, 0.0, 5)
        c.run_until_stable_leader(2000)
        o1 = c.submit("alice", "PUT balance:alice 100")
        o2 = c.submit("bob", "PUT balance:bob 50")
        o3 = c.submit("alice", "PUT balance:alice 75")
        o4 = c.submit("audit", "GET balance:alice")
        self.assertTrue(o1 >= 0 and o2 >= 0 and o3 >= 0 and o4 >= 0 and c.pending_count() == 4, "the leader accepted four client ops")
        settled = c.run_until_settled(1000)
        self.assertTrue(settled > 0 and c.pending_count() == 0 and len(c.completed) == 4 and not c.failed, "every op completes once the leader applies it")
        read = next((o for o in c.completed if o.kind is OpKind.GET), None)
        self.assertTrue(read is not None and read.value == "75", "the read observes the latest committed write")
        self.assertTrue(all(o.returned_at > o.invoked_at for o in c.completed), "ops return after they were invoked")
        leader = c.stable_leader()
        self.assertTrue(leader.commit_index == leader.last_applied and leader.commit_index >= 4, "the leader's commit_index and last_applied agree and cover the ops")
        c.run_until_applied(leader.last_log_index(), 1000)
        self.assertTrue(c.state_machines_converged() and all(applied_prefix_matches_log(n) for n in c.nodes), "every follower applied the same prefix")
        self.assertTrue(all(n.state_machine.get("balance:alice") == "75" and n.state_machine.get("balance:bob") == "50" for n in c.nodes), "every store agrees on the balances")
        self.assertTrue(Checker().check(c.history()).ok, "the recorded history is linearizable")
        self.assertEqual(c.node((leader.id + 1) % 5).propose("PUT x 1"), -1, "a follower refuses client ops: submit needs a stable leader")

    def test_commit_needs_a_majority_of_match_index_not_a_reply_from_anyone(self):
        q = Cluster(5, 11, 0.0, 5)
        q.run_until_stable_leader(2000)
        lead = q.stable_leader()
        others = list(lead.peers)
        q.crash(others[0])
        q.crash(others[1])
        q.crash(others[2])
        idx = q.propose("PUT balance 1000")
        q.run(400)
        self.assertTrue(idx > 0 and lead.commit_index < idx and lead.replicated_count(idx) == 2, "with 3 of 5 crashed the entry replicates to 2/5 and does not commit")
        self.assertTrue(all(n.state_machine.get("balance") is None for n in q.nodes), "nothing applied the uncommitted write")
        q.restart(others[0])
        q.run(400)
        self.assertTrue(lead.commit_index >= idx and lead.state_machine.get("balance") == "1000", "one node back (3 live) commits and applies it")
        self.assertEqual(q.node(others[0]).state_machine.get("balance"), "1000", "the restarted node applied it too, from its rebuilt state")

    def test_a_restart_rebuilds_the_state_machine_from_the_persisted_log(self):
        c = Cluster(5, 5, 0.0, 5)
        c.run_until_stable_leader(2000)
        for k in range(1, 4):
            c.submit("c", f"PUT k{k} v{k}")
        c.run_until_settled(1000)
        c.run_until_applied(c.stable_leader().last_log_index(), 1000)
        victim = (c.stable_leader().id + 1) % 5
        self.assertTrue(c.node(victim).last_applied >= 3 and c.node(victim).state_machine.get("k3") == "v3", "precondition: the follower applied three writes")
        c.crash(victim)
        back = c.restart(victim)
        self.assertTrue(back.commit_index == 0 and back.last_applied == 0 and back.state_machine.applied() == 0 and back.last_log_index() == c.storages[victim].last_index(),
                        "volatile state is gone after restart: commit_index 0, empty store, log intact")
        c.run(300)
        self.assertTrue(back.last_applied == c.stable_leader().last_applied and back.state_machine.get("k3") == "v3" and applied_prefix_matches_log(back),
                        "the next heartbeats carry leader_commit and the node re-applies its log")
        self.assertTrue(c.state_machines_converged(), "state machines converged")

    def test_a_stale_leaders_write_is_never_applied_and_the_client_learns_it_failed(self):
        c = Cluster(5, 7, 0.0, 5)
        c.run_until_stable_leader(2000)
        c.submit("alice", "PUT balance:alice 100")
        c.run_until_settled(1000)
        old = c.stable_leader()
        c.partition({old.id, (old.id + 1) % 5})
        lost = c.submit("mallory", "PUT balance:alice 999")
        self.assertGreaterEqual(lost, 0, "the stale leader still accepts the write (it does not know it is stale)")
        c.run(600)
        self.assertTrue(c.pending_count() == 1 and old.state_machine.get("balance:alice") == "100", "the write stays pending inside the minority")
        c.heal()
        c.run_until_stable_leader(2000)
        c.submit_until_accepted("carol", "PUT balance:carol 7", 100)
        c.submit_until_accepted("audit", "GET balance:alice", 100)
        settled = c.run_until_settled(2000)
        self.assertGreater(settled, 0, "after heal every op settles")
        self.assertTrue(len(c.failed) == 1 and c.failed[0].command == "PUT balance:alice 999", "the minority write failed rather than completing")
        c.run_until_applied(c.stable_leader().last_log_index(), 1000)
        self.assertTrue(all(n.state_machine.get("balance:alice") == "100" for n in c.nodes) and c.state_machines_converged(), "no state machine ever applied the overwritten write")
        read = next((o for o in c.completed if o.kind is OpKind.GET), None)
        self.assertTrue(read is not None and read.value == "100", "the post-heal read sees 100")
        self.assertTrue(Checker().check(c.history()).ok, "the completed history is linearizable")

    def test_the_checker_sequential_specification_and_real_time_order(self):
        s0: dict[str, str] = {}
        s1 = Checker.step(s0, Op(0, "a", OpKind.PUT, "x", "1", 0, 1))
        self.assertTrue(s1 is not None and s1.get("x") == "1" and not s0, "step applies a PUT to a copy, leaving the input alone")
        self.assertTrue(Checker.step(s1, Op(1, "b", OpKind.GET, "x", "1", 2, 3)) is s1 and Checker.step(s1, Op(2, "b", OpKind.GET, "x", "2", 2, 3)) is None,
                        "step accepts a GET that matches and rejects one that does not")
        self.assertTrue(Checker.step(s0, Op(3, "b", OpKind.GET, "y", "", 0, 1)) is not None and Checker.step(s0, Op(4, "b", OpKind.GET, "y", "0", 0, 1)) is None,
                        "a GET of an absent key must observe the empty string")

        good = History().put("alice", "x", "1", 0, 5).put("bob", "x", "2", 6, 10).get("carol", "x", "2", 11, 15)
        rg = Checker().check(good)
        self.assertTrue(rg.ok and respects_real_time(good, rg.order), "a read that sees the latest completed write is linearizable")
        stale = History().put("alice", "x", "1", 0, 5).put("bob", "x", "2", 6, 10).get("carol", "x", "1", 11, 15)
        rs = Checker().check(stale)
        self.assertTrue(not rs.ok and rs.order == (), "a stale read is not linearizable")
        self.assertEqual(len(rs.longest_prefix), 2, "the verdict names the longest prefix the search could linearize")
        window = History().put("alice", "x", "1", 0, 5).put("bob", "x", "2", 6, 20).get("carol", "x", "1", 11, 15)
        self.assertTrue(Checker().check(window).ok, "a read that overlaps a write may see the old value")
        order = History().put("alice", "x", "1", 0, 5).get("carol", "x", "1", 6, 10).put("bob", "x", "2", 2, 12).get("dave", "x", "2", 13, 14)
        ro = Checker().check(order)
        self.assertTrue(ro.ok and respects_real_time(order, ro.order) and ro.order.index(1) < ro.order.index(2), "the witness order respects every real-time precedence")
        two_keys = History().put("a", "x", "1", 0, 2).put("b", "y", "9", 0, 2).get("c", "y", "", 3, 4)
        self.assertFalse(Checker().check(two_keys).ok, "keys are independent registers: an absent read after a completed write is not linearizable")
        self.assertTrue(Checker().check(History()).ok, "the empty history is linearizable")
        self.assertTrue(rg.explored >= 1 and rs.explored >= 1, "explored counts search states")
        self.assertIsInstance(rg, CheckResult, "check returns a CheckResult")

    def test_lossy_seeds_with_a_leader_crash_histories_linearizable_state_machines_converged(self):
        linearizable = 0
        converged = 0
        seeds = 12
        for seed in range(1, seeds + 1):
            s = Cluster(5, seed, 0.2, 10)
            s.run_until_stable_leader(3000)
            for k in range(1, 6):
                s.submit_until_accepted(f"c{k % 2}", f"PUT k{k} v{k}", 100)
                s.run(30)
            victim = s.stable_leader() or s.node(0)
            s.crash(victim.id)
            s.run_until_stable_leader(3000)
            for k in range(1, 6):
                s.submit_until_accepted(f"c{k % 2}", f"GET k{k}", 100)
                s.run(30)
            s.restart(victim.id)
            s.run_until_stable_leader(3000)
            s.run_until_settled(3000)
            if s.pending_count() == 0 and Checker().check(s.history()).ok:
                linearizable += 1
            if s.stable_leader() is not None:
                s.run_until_applied(s.stable_leader().last_log_index(), 3000)
            if s.state_machines_converged() and all(applied_prefix_matches_log(n) for n in s.nodes):
                converged += 1
        self.assertEqual(linearizable, seeds, f"{seeds} lossy seeds: every recorded history is linearizable")
        self.assertEqual(converged, seeds, f"{seeds} lossy seeds: every state machine converged on the same applied prefix")


if __name__ == "__main__":
    unittest.main()
