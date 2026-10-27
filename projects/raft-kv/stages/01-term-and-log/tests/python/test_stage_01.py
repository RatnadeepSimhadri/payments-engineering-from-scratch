"""Stage 01 tests for projects/raft-kv (Python): persistent state (Storage) and the provided simulation scaffolding.
Run by the grader: python3 -m unittest -v. Port of Stage01Test.java: same checks, same names, same order."""

import unittest

from main import DEFAULT_CONFIG, Clock, Config, LogEntry, Message, MessageKind, Network, Rng, Storage


class Stage01Probe:
    """A minimal network participant for the scaffolding tests. Records what it receives; echoes AppendEntries."""

    def __init__(self, id: int) -> None:
        self.id = id
        self.received: list[str] = []
        self.ticks = 0

    def tick(self, now: int) -> list[Message]:
        self.ticks += 1
        return []

    def handle(self, m: Message, now: int) -> list[Message]:
        self.received.append(f"{now}:{m.kind.name}:{m.from_}:{m.term}")
        if m.kind is MessageKind.APPEND_ENTRIES:
            return [Message.append_reply(self.id, m.from_, m.term, True, 0)]
        return []


def logged(*terms: int) -> Storage:
    s = Storage()
    for i, t in enumerate(terms, start=1):
        s.append(LogEntry(t, f"cmd{i}"))
    return s


class Stage01StorageTest(unittest.TestCase):
    def test_fresh_storage(self):
        s = Storage()
        self.assertEqual(s.current_term, 0, "fresh current_term is 0")
        self.assertEqual(s.voted_for, -1, "fresh voted_for is null (-1)")
        self.assertEqual(s.last_index(), 0, "fresh last_index is 0")
        self.assertEqual(s.last_term(), 0, "fresh last_term is 0")
        self.assertEqual(s.term_at(0), 0, "sentinel at index 0 has term 0")
        self.assertEqual(s.describe(), "[]", "fresh describe is []")
        self.assertEqual(s.entries_from(1), [], "fresh entries_from(1) is empty")
        self.assertEqual(s.writes, 0, "no writes yet")

    def test_save_persists_term_and_vote(self):
        s = Storage()
        s.save(3, 2)
        self.assertEqual(s.current_term, 3, "save sets current_term")
        self.assertEqual(s.voted_for, 2, "save sets voted_for")
        self.assertEqual(s.writes, 1, "save counts one stable write")
        s.save(3, 2)
        self.assertEqual(s.writes, 2, "re-saving the same pair still writes (it is a persist, not a cache)")
        s.save(4, -1)
        self.assertTrue(s.current_term == 4 and s.voted_for == -1, "a new term clears voted_for when asked")
        with self.assertRaises(ValueError, msg="term must never go backwards"):
            s.save(2, 0)
        self.assertTrue(s.current_term == 4 and s.voted_for == -1 and s.writes == 3, "a rejected save changes nothing")

    def test_append_and_the_1_indexed_log(self):
        s = Storage()
        s.append(LogEntry(1, "PUT a 1"))
        s.append(LogEntry(1, "PUT b 2"))
        s.append(LogEntry(3, "NOOP"))
        self.assertEqual(s.last_index(), 3, "last_index counts entries, not the sentinel")
        self.assertEqual(s.last_term(), 3, "last_term is the last entry's term")
        self.assertEqual(s.term_at(1), 1, "term_at(1) is 1")
        self.assertEqual(s.term_at(3), 3, "term_at(3) is 3")
        self.assertEqual(s.writes, 3, "each append is a stable write")
        self.assertEqual(s.describe(), "[t1:PUT a 1, t1:PUT b 2, t3:NOOP]", "describe lists entries from index 1")
        self.assertTrue(len(s.log) == 4 and s.log[0].term == 0, "log list itself holds sentinel + 3")
        with self.assertRaises(ValueError, msg="terms in a log never decrease"):
            s.append(LogEntry(2, "late"))
        self.assertTrue(s.last_index() == 3 and s.writes == 3, "a rejected append changes nothing")
        s.append(LogEntry(3, "PUT c 3"))
        self.assertTrue(s.last_index() == 4 and s.last_term() == 3, "same term may repeat")

    def test_term_at_bounds(self):
        s = logged(1, 1)
        with self.assertRaises(IndexError, msg="term_at beyond last index raises IndexError"):
            s.term_at(3)
        with self.assertRaises(IndexError, msg="term_at negative raises IndexError"):
            s.term_at(-1)

    def test_truncate(self):
        s = logged(1, 1, 2, 2, 2)
        before = s.writes
        s.truncate(3)
        self.assertTrue(s.last_index() == 2 and s.last_term() == 1, "truncate(3) removes index 3 and after")
        self.assertEqual(s.writes, before + 1, "truncate is one stable write")
        self.assertEqual(s.describe(), "[t1:cmd1, t1:cmd2]", "truncate keeps earlier entries intact")
        s.truncate(7)
        self.assertTrue(s.last_index() == 2 and s.writes == before + 1, "truncating past the end is a no-op")
        s.truncate(1)
        self.assertTrue(s.last_index() == 0 and s.last_term() == 0 and len(s.log) == 1, "truncate(1) empties the log but keeps the sentinel")
        with self.assertRaises(ValueError, msg="the sentinel cannot be truncated"):
            s.truncate(0)
        s.append(LogEntry(5, "after"))
        self.assertTrue(s.last_index() == 1 and s.term_at(1) == 5, "log is usable after a full truncate")

    def test_entries_from(self):
        s = logged(1, 2, 2)
        tail = s.entries_from(2)
        self.assertTrue(len(tail) == 2 and tail[0].command == "cmd2" and tail[1].command == "cmd3", "entries_from(2) returns indexes 2..3")
        self.assertEqual(len(s.entries_from(1)), 3, "entries_from(1) returns everything")
        self.assertEqual(s.entries_from(4), [], "entries_from(last_index+1) is empty (a heartbeat)")
        tail.clear()
        self.assertEqual(s.last_index(), 3, "entries_from returns a copy, not a view")
        with self.assertRaises(IndexError, msg="entries_from(0) would ship the sentinel"):
            s.entries_from(0)
        with self.assertRaises(IndexError, msg="entries_from beyond last_index+1 raises"):
            s.entries_from(5)

    def test_up_to_date_rule_section_5_4_1(self):
        s = logged(1, 2, 2)  # ends at (3, t2)
        self.assertTrue(s.up_to_date(1, 3), "higher last term wins even with a shorter log")
        self.assertTrue(s.up_to_date(4, 2), "same last term, longer log wins")
        self.assertTrue(s.up_to_date(3, 2), "same last term, same length is up-to-date")
        self.assertFalse(s.up_to_date(2, 2), "same last term, shorter log loses")
        self.assertFalse(s.up_to_date(10, 1), "lower last term loses even with a longer log")
        self.assertTrue(Storage().up_to_date(0, 0), "an empty log is up-to-date with another empty log")
        self.assertFalse(s.up_to_date(0, 0), "an empty candidate is not up-to-date with a non-empty voter")


class Stage01ScaffoldingTest(unittest.TestCase):
    def test_rng_is_splitmix64_and_deterministic(self):
        a, b = Rng(42), Rng(42)
        self.assertTrue(a.next_long() == b.next_long() and a.next_int(1000) == b.next_int(1000) and a.next_double() == b.next_double(), "same seed, same stream")
        self.assertEqual(Rng(1).next_long(), 0x910A2DEC89025CC1, "SplitMix64(1) first output")
        c = Rng(42)
        self.assertEqual([c.next_int(1000) for _ in range(3)], [451, 953, 371], "Rng(42).next_int(1000) stream is 451, 953, 371")
        d = Rng(9)
        in_range = True
        for _ in range(1000):
            v = d.next_int(7)
            if v < 0 or v >= 7:
                in_range = False
            x = d.next_double()
            if x < 0 or x >= 1:
                in_range = False
        self.assertTrue(in_range, "next_int stays in [0, bound) and next_double in [0, 1)")

    def test_clock_and_network_scaffolding(self):
        clock = Clock()
        self.assertEqual(clock.now(), 0, "clock starts at 0")
        net = Network(clock, 7, 0.0, 5)
        n0, n1 = Stage01Probe(0), Stage01Probe(1)
        net.add(n0)
        net.add(n1)
        net.send(Message.append_entries(0, 1, 1, 0, 0, [], 0))
        net.send(Message.append_entries(0, 1, 1, 0, 0, [], 0))
        self.assertTrue(net.in_flight() == 2 and not n1.received, "messages are in flight, not delivered synchronously")
        for _ in range(12):
            net.tick()
        self.assertEqual(clock.now(), 12, "clock advanced one tick per tick()")
        self.assertEqual(n1.received, ["3:APPEND_ENTRIES:0:1", "3:APPEND_ENTRIES:0:1"], "both messages reached n1 at seed-7's schedule (tick 3, in send order)")
        self.assertEqual(n0.received, ["4:APPEND_ENTRIES_REPLY:1:1", "6:APPEND_ENTRIES_REPLY:1:1"], "replies came back to n0 at ticks 4 and 6")
        self.assertTrue(n0.ticks == 12 and n1.ticks == 12, "every live node ticks once per tick")
        self.assertTrue(net.sent == 4 and net.delivered == 4 and net.dropped == 0, "counters: 4 sent, 4 delivered, 0 dropped")
        self.assertFalse(any(r.startswith("0:") for r in n1.received), "delay is at least one tick: nothing is delivered in the tick it is sent")

        c2 = Clock()
        lossy = Network(c2, 7, 1.0, 5)
        p0, p1 = Stage01Probe(0), Stage01Probe(1)
        lossy.add(p0)
        lossy.add(p1)
        lossy.send(Message.request_vote(0, 1, 1, 0, 0))
        for _ in range(10):
            lossy.tick()
        self.assertTrue(lossy.dropped == 1 and not p1.received, "drop_rate 1.0 drops everything")

        c3 = Clock()
        parted = Network(c3, 7, 0.0, 5)
        q0, q1, q2 = Stage01Probe(0), Stage01Probe(1), Stage01Probe(2)
        parted.add(q0)
        parted.add(q1)
        parted.add(q2)
        parted.partition({0})
        parted.send(Message.request_vote(0, 1, 1, 0, 0))
        parted.send(Message.request_vote(1, 2, 1, 0, 0))
        for _ in range(10):
            parted.tick()
        self.assertTrue(not q1.received and len(q2.received) == 1, "a partition drops messages across the cut and delivers the rest")
        parted.heal()
        parted.send(Message.request_vote(0, 1, 1, 0, 0))
        for _ in range(10):
            parted.tick()
        self.assertEqual(len(q1.received), 1, "after heal the cut is gone")
        parted.crash(2)
        parted.send(Message.request_vote(1, 2, 2, 0, 0))
        for _ in range(10):
            parted.tick()
        self.assertTrue(len(q2.received) == 1 and parted.is_down(2) and q2.ticks == 20, "a crashed node receives nothing and does not tick")
        q2b = Stage01Probe(2)
        parted.restart(q2b)
        parted.send(Message.request_vote(1, 2, 3, 0, 0))
        for _ in range(10):
            parted.tick()
        self.assertTrue(not parted.is_down(2) and len(q2b.received) == 1 and q2b.ticks == 10, "a restarted node is live again")

    def test_message_helpers(self):
        rv = Message.request_vote(1, 2, 5, 7, 4)
        self.assertTrue(rv.kind is MessageKind.REQUEST_VOTE and rv.prev_log_index == 7 and rv.prev_log_term == 4 and rv.term == 5,
                        "request_vote carries the candidate's last (index, term) in prev_log_index/prev_log_term")
        ae = Message.append_entries(0, 1, 2, 3, 1, [LogEntry(2, "PUT k v")], 3)
        self.assertTrue(ae.prev_log_index == 3 and ae.prev_log_term == 1 and len(ae.entries) == 1 and ae.leader_commit == 3,
                        "append_entries carries prev (index, term), entries, and leader_commit")
        with self.assertRaises(ValueError, msg="Config rejects a heartbeat interval that is not well below the election timeout"):
            Config(100, 200, 100)
        self.assertTrue(DEFAULT_CONFIG.election_timeout_min == 150 and DEFAULT_CONFIG.election_timeout_max == 300
                        and DEFAULT_CONFIG.heartbeat_interval == 50 and Config.DEFAULT is DEFAULT_CONFIG, "default config is 150-300 / 50")


if __name__ == "__main__":
    unittest.main()
