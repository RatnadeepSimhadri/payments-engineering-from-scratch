"""Stage 03 tests for projects/raft-kv (Python): log replication (AppendEntries consistency check, next_index/match_index,
follower truncation, Log Matching). Run by the grader after stages 01 and 02. Port of Stage03Test.java."""

import unittest

from main import DEFAULT_CONFIG, Cluster, LogEntry, Message, MessageKind, RaftNode, Rng, Role, Storage, check_log_matching, no_trace


def peers_of(id: int, size: int) -> list[int]:
    return [j for j in range(size) if j != id]


def standalone(id: int, storage: Storage, seed: int) -> RaftNode:
    return RaftNode(id, peers_of(id, 5), storage, Rng(seed), DEFAULT_CONFIG, 0, no_trace)


def logged(*entries: str) -> Storage:  # "t1:PUT a 1"
    s = Storage()
    for e in entries:
        term, _, command = e.partition(":")
        s.append(LogEntry(int(term[1:]), command))
    return s


def elected_leader(storage: Storage, seed: int) -> RaftNode:
    n = standalone(0, storage, seed)
    n.start_election(1)
    n.handle(Message.vote_reply(1, 0, n.current_term, True), 2)
    n.handle(Message.vote_reply(2, 0, n.current_term, True), 3)
    return n


def log_matching_holds(c: Cluster) -> bool:
    try:
        for i in range(c.size):
            for j in range(i + 1, c.size):
                Cluster.check_log_matching(c.node(i).log, c.node(j).log, i, j)
        return True
    except RuntimeError:
        return False


def every_log_contains(c: Cluster, command: str) -> bool:
    return all(any(e.command == command for e in n.log) for n in c.nodes)


def no_log_contains(c: Cluster, command: str) -> bool:
    return not any(any(e.command == command for e in n.log) for n in c.nodes)


class Stage03ReplicationTest(unittest.TestCase):
    def test_propose_appends_to_the_leaders_log_only(self):
        fs = Storage()
        follower = standalone(1, fs, 1)
        self.assertTrue(follower.propose("PUT a 1") == -1 and fs.last_index() == 0, "a follower refuses proposals")
        ls = Storage()
        leader = elected_leader(ls, 2)
        base = leader.last_log_index()
        i1 = leader.propose("PUT a 1")
        i2 = leader.propose("PUT b 2")
        self.assertTrue(i1 == base + 1 and i2 == base + 2 and leader.last_log_index() == i2, "propose returns consecutive indexes")
        self.assertTrue(ls.term_at(i2) == 1 and ls.log[i2].command == "PUT b 2", "entries carry the leader's current term and the command")
        self.assertGreaterEqual(ls.writes, 3, "propose persists (stable writes increased)")

    def test_a_new_leader_initialises_next_index_match_index_and_ships_from_next_index(self):
        ls = logged("t1:PUT a 1", "t1:PUT b 2")
        ls.save(1, -1)
        leader = standalone(0, ls, 3)
        leader.start_election(5)
        leader.handle(Message.vote_reply(1, 0, 2, True), 6)
        first = leader.handle(Message.vote_reply(2, 0, 2, True), 7)
        self.assertTrue(all(leader.next_index[p] == 3 for p in leader.peers), "next_index starts at (last index at election) + 1 for every peer")
        self.assertTrue(all(leader.match_index[p] == 0 for p in leader.peers), "match_index starts at 0 for every peer")
        self.assertTrue(all(m.prev_log_index == 2 and m.prev_log_term == 1 for m in first), "the first AppendEntries names the entry before next_index as (prev_log_index, prev_log_term)")
        self.assertTrue(all(len(m.entries) == leader.last_log_index() - 2 for m in first), "the first AppendEntries ships exactly log[next_index..last]")
        leader.propose("PUT c 3")
        ae = leader.append_entries_for(1)
        self.assertTrue(ae.entries[-1].command == "PUT c 3" and ae.prev_log_index == 2 and ae.term == 2, "append_entries_for ships everything from next_index, including the new proposal")
        leader.next_index[1] = 1
        back = leader.append_entries_for(1)
        self.assertTrue(back.prev_log_index == 0 and back.prev_log_term == 0 and len(back.entries) == leader.last_log_index(), "with next_index 1 the prev is the sentinel (0, t0) and the whole log ships")

    def test_append_entries_consistency_check_receiver_rule_2(self):
        fs = logged("t1:PUT a 1", "t1:PUT b 2", "t2:PUT c 3")
        f = standalone(1, fs, 4)
        r1 = f.handle(Message.append_entries(0, 1, 3, 5, 2, [LogEntry(3, "PUT d 4")], 0), 10)[0]
        self.assertTrue(not r1.success and f.consistency_rejections == 1, "prev_log_index beyond the follower's log is refused")
        self.assertTrue(f.leader_id == 0 and f.current_term == 3, "a refused AppendEntries still makes the sender the leader and adopts its term")
        r2 = f.handle(Message.append_entries(0, 1, 3, 3, 1, [LogEntry(3, "PUT d 4")], 0), 11)[0]
        self.assertTrue(not r2.success and f.consistency_rejections == 2, "prev_log_index present but with a different term is refused")
        self.assertTrue(fs.last_index() == 3 and fs.term_at(3) == 2, "a refusal never changes the follower's log")
        r3 = f.handle(Message.append_entries(0, 1, 3, 3, 2, [LogEntry(3, "PUT d 4")], 0), 12)[0]
        self.assertTrue(r3.success and fs.last_index() == 4 and fs.term_at(4) == 3, "a matching (prev_log_index, prev_log_term) is accepted")
        self.assertEqual(r3.match_index, 4, "a successful reply reports the index of the last entry now known to match")
        hb = f.handle(Message.append_entries(0, 1, 3, 4, 3, [], 0), 13)[0]
        self.assertTrue(hb.success and hb.match_index == 4 and fs.last_index() == 4, "a heartbeat with a matching prev succeeds with match_index = prev_log_index and leaves the log alone")

    def test_conflicting_entries_are_truncated_duplicates_are_skipped(self):
        fs = logged("t1:PUT a 1", "t1:PUT b 2", "t2:PUT c 3")
        f = standalone(2, fs, 5)
        r = f.handle(Message.append_entries(0, 2, 3, 1, 1, [LogEntry(3, "PUT x 9")], 0), 10)[0]
        self.assertTrue(r.success and fs.last_index() == 2 and fs.describe() == "[t1:PUT a 1, t3:PUT x 9]", "a conflicting entry and everything after it are deleted, then the leader's entry is appended")
        writes = fs.writes
        dup = f.handle(Message.append_entries(0, 2, 3, 1, 1, [LogEntry(3, "PUT x 9")], 0), 11)[0]
        self.assertTrue(dup.success and dup.match_index == 2 and fs.last_index() == 2 and fs.writes == writes, "re-delivering the same entries changes nothing (idempotent)")
        mixed = f.handle(Message.append_entries(0, 2, 3, 1, 1, [LogEntry(3, "PUT x 9"), LogEntry(3, "PUT y 8")], 0), 12)[0]
        self.assertTrue(mixed.success and mixed.match_index == 3 and fs.describe() == "[t1:PUT a 1, t3:PUT x 9, t3:PUT y 8]", "a batch that overlaps the log appends only the new tail")
        older = f.handle(Message.append_entries(0, 2, 3, 0, 0, [LogEntry(1, "PUT a 1")], 0), 13)[0]
        self.assertTrue(older.success and older.match_index == 1 and fs.last_index() == 3, "an old, shorter batch that matches a prefix does not truncate the newer tail")

    def test_the_leader_walks_next_index_back_on_refusal_and_forward_on_success(self):
        ls = logged("t1:PUT a 1", "t1:PUT b 2", "t1:PUT c 3")
        ls.save(1, -1)
        l = elected_leader(ls, 6)
        term = l.current_term
        start = l.next_index[1]
        retry = l.handle(Message.append_reply(1, 0, term, False, 0), 10)
        self.assertEqual(l.next_index[1], start - 1, "a refusal decrements next_index by one")
        self.assertTrue(len(retry) == 1 and retry[0].kind is MessageKind.APPEND_ENTRIES and retry[0].to == 1 and retry[0].prev_log_index == start - 2, "and immediately resends with the earlier prev")
        for i in range(20):
            l.handle(Message.append_reply(1, 0, term, False, 0), 11 + i)
        self.assertEqual(l.next_index[1], 1, "next_index never drops below 1")
        full = l.append_entries_for(1)
        self.assertTrue(full.prev_log_index == 0 and len(full.entries) == l.last_log_index(), "at next_index 1 the whole log is shipped after the sentinel")
        l.handle(Message.append_reply(1, 0, term, True, 3), 40)
        self.assertTrue(l.match_index[1] == 3 and l.next_index[1] == 4, "a success sets match_index and next_index = match_index + 1")
        l.handle(Message.append_reply(1, 0, term, True, 2), 41)
        self.assertTrue(l.match_index[1] == 3 and l.next_index[1] == 4, "an older success (lower match_index) never moves match_index backwards")
        self.assertTrue(l.replicated_count(3) == 2 and l.replicated_count(4) == 1, "replicated_count counts the leader plus peers at or beyond the index")
        l.handle(Message.append_reply(2, 0, term - 1, True, 3), 42)
        self.assertEqual(l.match_index[2], 0, "a reply from an older term is ignored")
        f = standalone(3, Storage(), 7)
        self.assertEqual(f.handle(Message.append_reply(1, 3, 0, True, 5), 1), [], "a non-leader ignores AppendEntries replies")

    def test_three_commands_replicate_to_every_log_on_a_quiet_5_node_cluster(self):
        c = Cluster(5, 7, 0.0, 5)
        c.run_until_stable_leader(2000)
        i1 = c.propose("PUT alice 100")
        i2 = c.propose("PUT bob 50")
        i3 = c.propose("PUT alice 75")
        self.assertTrue(i1 > 0 and i2 == i1 + 1 and i3 == i2 + 1, "proposals got consecutive indexes")
        at = c.run_until_logs_converged(1000)
        self.assertTrue(at > 0 and c.logs_converged(), "all five logs converge")
        self.assertTrue(all(n.log[i1].command == "PUT alice 100" and n.log[i3].command == "PUT alice 75" for n in c.nodes), "every log holds the three commands at the leader's indexes")
        self.assertTrue(all(n.log[i2].term == c.stable_leader().current_term for n in c.nodes), "the entries carry the leader's term")
        c.run(20)  # let the last replies reach the leader
        self.assertEqual(c.stable_leader().replicated_count(i3), 5, "the leader knows every follower matches through the last index")
        self.assertTrue(log_matching_holds(c), "Log Matching holds")

    def test_a_lagging_follower_catches_up_through_next_index_back_off(self):
        c = Cluster(5, 9, 0.0, 5)
        c.run_until_stable_leader(2000)
        leader = c.stable_leader()
        lag = (leader.id + 2) % 5
        c.crash(lag)
        for k in range(1, 5):
            c.propose(f"PUT k{k} v{k}")
        c.run(300)
        self.assertFalse(any(e.command.startswith("PUT k") for e in c.node(lag).log), "the crashed follower has none of the new entries")
        c.restart(lag)
        at = c.run_until_logs_converged(1500)
        self.assertTrue(at > 0 and c.node(lag).log == leader.log, "after restart its log converges with the leader's")
        self.assertTrue(log_matching_holds(c), "Log Matching holds after catch-up")

    def test_a_stale_leaders_unreplicated_entries_are_overwritten_after_a_partition_heals(self):
        c = Cluster(5, 7, 0.0, 5)
        c.run_until_stable_leader(2000)
        c.propose("PUT alice 100")
        c.run_until_logs_converged(1000)
        old = c.stable_leader()
        follower = (old.id + 1) % 5
        c.partition({old.id, follower})
        stale_idx = c.propose_to(old.id, "PUT alice 999")
        c.propose_to(old.id, "PUT carol 1")
        c.run(600)
        self.assertTrue(stale_idx > 0 and old.log[stale_idx].command == "PUT alice 999", "the stale leader accepted writes it can never replicate to a majority")
        fresh = None
        for l in c.leaders():
            if fresh is None or l.current_term > fresh.current_term:
                fresh = l
        self.assertTrue(fresh is not None and fresh is not old and fresh.current_term > old.current_term, "the majority elected a new leader in a higher term")
        c.propose_to(fresh.id, "PUT dave 7")
        c.run(200)
        self.assertNotEqual(old.log, fresh.log, "the two sides diverge at the stale index")
        c.heal()
        c.run_until_stable_leader(2000)
        at = c.run_until_logs_converged(2000)
        self.assertGreater(at, 0, "after heal every log converges")
        self.assertTrue(no_log_contains(c, "PUT alice 999") and no_log_contains(c, "PUT carol 1"), "the stale writes are gone from every log")
        self.assertTrue(every_log_contains(c, "PUT dave 7") and every_log_contains(c, "PUT alice 100"), "the majority's write survives everywhere")
        self.assertTrue(old.role is Role.FOLLOWER and old.log == c.stable_leader().log, "the old leader is now a follower whose log was truncated")
        self.assertTrue(log_matching_holds(c), "Log Matching holds after the heal")

    def test_log_matching_and_convergence_hold_across_lossy_seeds_with_a_leader_crash(self):
        converged = 0
        for seed in range(1, 13):
            r = Cluster(5, seed, 0.2, 10)
            r.run_until_stable_leader(3000)
            for k in range(1, 5):
                attempt = 0
                while attempt < 300 and r.propose(f"PUT k{k} v{k}") < 0:
                    r.run(10)
                    attempt += 1
                r.run(30)
            victim = r.stable_leader() or r.node(0)
            r.crash(victim.id)
            r.run_until_stable_leader(3000)
            for k in range(5, 9):
                attempt = 0
                while attempt < 300 and r.propose(f"PUT k{k} v{k}") < 0:
                    r.run(10)
                    attempt += 1
                r.run(30)
            r.restart(victim.id)
            r.run_until_stable_leader(3000)
            if r.run_until_logs_converged(3000) > 0 and log_matching_holds(r) and every_log_contains(r, "PUT k8 v8"):
                converged += 1
        self.assertEqual(converged, 12, "12 lossy seeds: every log converged on the surviving writes with Log Matching intact")


if __name__ == "__main__":
    unittest.main()
