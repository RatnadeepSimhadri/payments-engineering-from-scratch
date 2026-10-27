"""Stage 02 tests for projects/raft-kv (Python): leader election (RequestVote, randomized timeouts, majority, step-down).
Run by the grader after test_stage_01. Port of Stage02Test.java: same checks, same names, same order."""

import unittest

from main import (
    DEFAULT_CONFIG,
    Cluster,
    Config,
    LogEntry,
    Message,
    MessageKind,
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


def all_of_kind(ms: list[Message], kind: MessageKind, from_: int, term: int, expected_count: int) -> bool:
    if len(ms) != expected_count:
        return False
    targets = set()
    for m in ms:
        if m.kind is not kind or m.from_ != from_ or m.term != term:
            return False
        targets.add(m.to)
    return len(targets) == expected_count and from_ not in targets


def digest(c: Cluster) -> str:
    """A tracer-independent fingerprint of a 1500-tick run: node state every 50 ticks plus the network counters."""
    out = []
    for _ in range(30):
        c.run(50)
        for n in c.nodes:
            out.append(f"{n.current_term}{n.role.name}{n.leader_id}{n.election_deadline};")
        out.append(f"{c.network.sent}/{c.network.delivered}/{c.network.dropped}\n")
    return "".join(out)


def elected_leader(storage: Storage, seed: int) -> RaftNode:
    """Drives a standalone node to leadership of term 1 with votes from peers 1 and 2."""
    n = standalone(0, storage, seed)
    n.start_election(1)
    n.handle(Message.vote_reply(1, 0, n.current_term, True), 2)
    n.handle(Message.vote_reply(2, 0, n.current_term, True), 3)
    return n


class Stage02ElectionTest(unittest.TestCase):
    def test_randomized_election_timeout_from_the_nodes_seeded_rng(self):
        n = standalone(0, Storage(), 1)
        self.assertTrue(150 <= n.election_deadline <= 300, "first deadline is drawn at construction, within [min, max] of now=0")
        distinct = {standalone(0, Storage(), seed).election_deadline for seed in range(1, 13)}
        self.assertGreaterEqual(len(distinct), 4, "different seeds give different timeouts (split votes are broken by randomness)")
        self.assertEqual(standalone(0, Storage(), 5).election_deadline, standalone(0, Storage(), 5).election_deadline, "same seed gives the same timeout (replayable)")
        n.reset_election_timer(1000)
        self.assertTrue(1150 <= n.election_deadline <= 1300, "reset_election_timer draws a new deadline relative to now")
        tight = RaftNode(0, peers_of(0, 3), Storage(), Rng(3), Config(10, 10, 2), 7, no_trace)
        self.assertEqual(tight.election_deadline, 17, "with min == max the deadline is exactly now + min")

    def test_a_follower_whose_timer_expires_becomes_a_candidate(self):
        s = Storage()
        n = standalone(2, s, 4)
        deadline = n.election_deadline
        self.assertTrue(n.tick(deadline - 1) == [] and n.role is Role.FOLLOWER and s.current_term == 0, "ticks before the deadline send nothing and keep the follower role")
        out = n.tick(deadline)
        self.assertIs(n.role, Role.CANDIDATE, "at the deadline the node becomes a candidate")
        self.assertTrue(s.current_term == 1 and s.voted_for == 2 and s.writes >= 1, "the term is incremented and persisted with a vote for itself")
        self.assertTrue(all_of_kind(out, MessageKind.REQUEST_VOTE, 2, 1, 4), "one RequestVote per peer, carrying the new term")
        self.assertTrue(out[0].prev_log_index == 0 and out[0].prev_log_term == 0, "RequestVote carries the candidate's last log index and term")
        self.assertTrue(n.votes() == 1 and n.elections_started == 1, "the candidate counts its own vote")
        self.assertGreater(n.election_deadline, deadline, "the election timer is re-armed for the next round")
        second = n.election_deadline
        again = n.tick(second)
        self.assertTrue(s.current_term == 2 and n.role is Role.CANDIDATE and all_of_kind(again, MessageKind.REQUEST_VOTE, 2, 2, 4) and n.votes() == 1,
                        "a candidate whose timer expires starts a fresh election in a higher term")

    def test_votes_are_counted_to_a_majority_stale_and_duplicate_replies_are_ignored(self):
        s = Storage()
        n = standalone(0, s, 2)
        n.start_election(1)
        self.assertTrue(n.handle(Message.vote_reply(1, 0, 0, True), 2) == [] and n.votes() == 1 and n.role is Role.CANDIDATE, "one stale reply (older term) does nothing")
        n.handle(Message.vote_reply(1, 0, 1, True), 3)
        n.handle(Message.vote_reply(1, 0, 1, True), 4)
        self.assertTrue(n.votes() == 2 and n.role is Role.CANDIDATE, "a duplicate grant from the same peer counts once")
        n.handle(Message.vote_reply(3, 0, 1, False), 5)
        self.assertTrue(n.votes() == 2 and n.role is Role.CANDIDATE, "a denial does not count")
        out = n.handle(Message.vote_reply(2, 0, 1, True), 6)
        self.assertTrue(n.role is Role.LEADER and n.leader_id == 0, "the third vote of five makes a leader")
        self.assertTrue(all_of_kind(out, MessageKind.APPEND_ENTRIES, 0, 1, 4), "a new leader immediately sends AppendEntries to every peer")
        self.assertEqual(s.current_term, 1, "the leader's term is unchanged by winning")
        self.assertTrue(n.handle(Message.vote_reply(4, 0, 1, True), 7) == [] and n.role is Role.LEADER, "a late grant after winning is ignored")

    def test_request_vote_receiver_rules(self):
        s = Storage()
        s.save(3, -1)
        v = standalone(1, s, 8)
        deny = v.handle(Message.request_vote(0, 1, 2, 0, 0), 10)[0]
        self.assertTrue(deny.kind is MessageKind.REQUEST_VOTE_REPLY and not deny.success and deny.term == 3 and s.voted_for == -1, "a lower-term candidate is denied with the voter's own term")
        before = v.election_deadline
        writes = s.writes
        grant = v.handle(Message.request_vote(0, 1, 3, 0, 0), 20)[0]
        self.assertTrue(grant.success and grant.to == 0 and grant.term == 3, "first candidate of the term is granted")
        self.assertTrue(s.voted_for == 0 and s.writes == writes + 1, "the vote is persisted")
        self.assertTrue(v.election_deadline != before and v.election_deadline >= 170, "granting a vote resets the election timer")
        other = v.handle(Message.request_vote(2, 1, 3, 0, 0), 21)[0]
        self.assertTrue(not other.success and s.voted_for == 0, "a second candidate in the same term is denied")
        same = v.handle(Message.request_vote(0, 1, 3, 0, 0), 22)[0]
        self.assertTrue(same.success, "the same candidate asking again is granted again (retries are safe)")
        higher = v.handle(Message.request_vote(4, 1, 5, 0, 0), 23)[0]
        self.assertTrue(higher.success and s.current_term == 5 and s.voted_for == 4 and v.role is Role.FOLLOWER, "a higher-term candidate moves the voter to that term and gets the vote")

    def test_the_up_to_date_rule_guards_the_vote(self):
        s = Storage()
        s.append(LogEntry(1, "PUT a 1"))
        s.append(LogEntry(2, "PUT a 2"))
        v = standalone(1, s, 9)
        r1 = v.handle(Message.request_vote(0, 1, 3, 5, 1), 10)[0]
        self.assertTrue(not r1.success and s.voted_for == -1 and s.current_term == 3, "a candidate with a longer but older-term log is denied")
        r2 = v.handle(Message.request_vote(0, 1, 3, 1, 2), 11)[0]
        self.assertFalse(r2.success, "a candidate with the same last term but a shorter log is denied")
        r3 = v.handle(Message.request_vote(0, 1, 3, 2, 2), 12)[0]
        self.assertTrue(r3.success and s.voted_for == 0, "a candidate whose log is at least as up-to-date is granted")

    def test_append_entries_from_a_leader(self):
        s = Storage()
        f = standalone(3, s, 11)
        before = f.election_deadline
        ok = f.handle(Message.append_entries(0, 3, 1, 0, 0, [], 0), 50)[0]
        self.assertTrue(f.leader_id == 0 and s.current_term == 1 and f.role is Role.FOLLOWER, "a follower adopts the leader and the term")
        self.assertTrue(ok.kind is MessageKind.APPEND_ENTRIES_REPLY and ok.term == 1 and ok.to == 0, "the reply is an APPEND_ENTRIES_REPLY carrying the follower's term")
        self.assertTrue(f.election_deadline != before and f.election_deadline >= 200, "a heartbeat resets the election timer")
        stale = f.handle(Message.append_entries(4, 3, 0, 0, 0, [], 0), 51)[0]
        self.assertTrue(not stale.success and stale.term == 1 and f.leader_id == 0, "a lower-term AppendEntries is refused and the leader is unchanged")

        c = standalone(2, Storage(), 12)
        c.start_election(1)
        c.handle(Message.append_entries(0, 2, 1, 0, 0, [], 0), 2)
        self.assertTrue(c.role is Role.FOLLOWER and c.leader_id == 0 and c.current_term == 1, "a candidate that hears a leader of its term steps down")
        self.assertTrue(c.handle(Message.vote_reply(1, 2, 1, True), 3) == [] and c.role is Role.FOLLOWER, "the stepped-down candidate ignores late vote grants")

    def test_a_leader_steps_down_on_a_higher_term_and_sends_heartbeats_on_its_interval(self):
        s = Storage()
        l = elected_leader(s, 13)
        self.assertTrue(l.role is Role.LEADER and s.current_term == 1, "precondition: leader of term 1")
        hb = l.next_heartbeat
        self.assertEqual(l.tick(hb - 1), [], "no heartbeat before the interval elapses")
        beat = l.tick(hb)
        self.assertTrue(all_of_kind(beat, MessageKind.APPEND_ENTRIES, 0, 1, 4) and l.next_heartbeat == hb + DEFAULT_CONFIG.heartbeat_interval, "heartbeats go to every peer when the interval elapses")
        self.assertEqual(beat[0].leader_commit, l.commit_index, "heartbeats carry the leader's commit index")
        out = l.handle(Message.append_reply(1, 0, 4, False, 0), hb + 1)
        self.assertTrue(l.role is Role.FOLLOWER and s.current_term == 4 and s.voted_for == -1 and l.leader_id == -1, "a reply with a higher term dethrones the leader")
        self.assertGreaterEqual(l.election_deadline, hb + 1 + 150, "the dethroned leader re-arms an election timer")
        self.assertEqual(out, [], "the dethroned leader sends nothing in response")

        l2 = elected_leader(Storage(), 14)
        l2.handle(Message.request_vote(3, 0, 2, 1, 1), 10)
        self.assertTrue(l2.role is Role.FOLLOWER and l2.current_term == 2 and l2.voted_for == 3, "a higher-term RequestVote also dethrones a leader and gets the vote")

    def test_a_single_leader_is_elected_on_a_quiet_5_node_network_seed_7(self):
        c = Cluster(5, 7, 0.0, 5)
        at = c.run_until_stable_leader(2000)
        self.assertTrue(at > 0 and c.stable_leader() is not None, "stable leader elected")
        self.assertGreaterEqual(at, DEFAULT_CONFIG.election_timeout_min, "elected after at least one minimum timeout")
        self.assertEqual(len(c.leaders()), 1, "exactly one leader")
        leader = c.stable_leader()
        self.assertTrue(all(n.current_term == leader.current_term for n in c.nodes), "every node agrees on the leader's term")
        self.assertTrue(all(n.leader_id == leader.id for n in c.nodes if n.role is not Role.LEADER), "followers know who leads")
        self.assertTrue(all(s.voted_for >= 0 and s.writes >= 1 for s in c.storages), "every node persisted a vote for this term")
        term = c.max_term()
        c.run(2000)
        self.assertTrue(c.max_term() == term and len(c.leaders()) == 1, "heartbeats keep the term stable for 2000 ticks")
        self.assertGreater(c.network.delivered, 100, "heartbeats flowed")

    def test_same_seed_same_trace(self):
        d1, d2, d3 = digest(Cluster(5, 42, 0.1, 8)), digest(Cluster(5, 42, 0.1, 8)), digest(Cluster(5, 43, 0.1, 8))
        self.assertTrue(d1 == d2 and "LEADER" in d1, "same seed gives an identical run (terms, roles, leaders, message counts at every 50th tick)")
        self.assertNotEqual(d1, d3, "different seed gives a different run")

    def test_election_safety_over_many_lossy_seeds(self):
        elected_seeds = 0
        safe = True
        for seed in range(1, 41):
            s = Cluster(5, seed, 0.2, 10)
            leader_of_term: dict[int, int] = {}
            at = -1
            for _ in range(2500):
                s.tick()
                for n in s.nodes:
                    if n.role is not Role.LEADER:
                        continue
                    prev = leader_of_term.setdefault(n.current_term, n.id)
                    if prev != n.id:
                        safe = False
                if at < 0 and s.stable_leader() is not None:
                    at = s.now()
            if at > 0:
                elected_seeds += 1
        self.assertEqual(elected_seeds, 40, "40 lossy seeds all elect a leader within 2500 ticks")
        self.assertTrue(safe, "never two leaders in one term across 40 seeds x 2500 ticks")
        three_node_seeds = 0
        for seed in range(100, 120):
            s = Cluster(3, seed, 0.3, 10)
            if s.run_until_stable_leader(4000) > 0:
                three_node_seeds += 1
        self.assertEqual(three_node_seeds, 20, "3-node clusters with 30% drops still elect")

    def test_partition_the_majority_moves_on_the_minority_leader_steps_down_on_heal(self):
        p = Cluster(5, 7, 0.0, 5)
        p.run_until_stable_leader(2000)
        old = p.stable_leader()
        old_term = old.current_term
        p.partition({old.id, (old.id + 1) % 5})
        p.run(800)
        self.assertTrue(old.role is Role.LEADER and old.current_term == old_term, "stale leader keeps leading its minority side")
        self.assertTrue(len(p.leaders()) == 2 and p.max_term() > old_term, "majority side elected a leader with a higher term")
        p.heal()
        healed = p.run_until_stable_leader(2000)
        self.assertGreater(healed, 0, "cluster converges after heal")
        self.assertIs(old.role, Role.FOLLOWER, "old leader stepped down")
        self.assertTrue(p.stable_leader().current_term > old_term and p.stable_leader() is not old, "the higher term won")
        self.assertTrue(all(id is not None for id in p.leaders_by_term.values()), "a minority of two can never elect: no term in the trace has a 2-node leader")

    def test_a_crashed_leader_is_replaced_voted_for_survives_a_restart(self):
        c = Cluster(5, 21, 0.0, 5)
        c.run_until_stable_leader(2000)
        dead = c.stable_leader()
        t0 = dead.current_term
        c.crash(dead.id)
        at = c.run_until_stable_leader(2000)
        self.assertTrue(at > 0 and c.stable_leader().id != dead.id and c.stable_leader().current_term > t0, "a new leader is elected after the leader crashes")
        back = c.restart(dead.id)
        self.assertTrue(back.role is Role.FOLLOWER and back.current_term == c.storages[dead.id].current_term, "a restarted node comes back as a follower with its persisted term")
        c.run(600)
        self.assertTrue(back.current_term == c.stable_leader().current_term and back.leader_id == c.stable_leader().id, "the restarted node joins the current term and follows the leader")

        durable = Storage()
        n1 = standalone(1, durable, 31)
        first = n1.handle(Message.request_vote(0, 1, 1, 0, 0), 10)[0].success
        n1b = standalone(1, durable, 32)
        second = n1b.handle(Message.request_vote(2, 1, 1, 0, 0), 12)[0].success
        self.assertTrue(first and not second and durable.voted_for == 0, "after a crash and restart the node still refuses a second vote in the same term")


if __name__ == "__main__":
    unittest.main()
