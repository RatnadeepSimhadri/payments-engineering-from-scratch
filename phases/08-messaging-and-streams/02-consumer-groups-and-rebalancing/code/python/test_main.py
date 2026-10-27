"""Tests for phases/08-messaging-and-streams/02-consumer-groups-and-rebalancing. Run: python3 -m unittest -v"""
import unittest

from main import (
    Assignor,
    GroupCoordinator,
    HeartbeatResult,
    IllegalGenerationError,
    ManualClock,
    NotOwnerError,
    RangeAssignor,
    RoundRobinAssignor,
    StickyAssignor,
    UnknownMemberError,
)

THREE = ["c1", "c2", "c3"]
FOUR = ["c1", "c2", "c3", "c4"]


class AssignorTest(unittest.TestCase):
    def test_range_contiguous_first_members_get_extra(self):
        range3 = RangeAssignor().assign(THREE, 8, {})
        self.assertEqual(range3["c1"], [0, 1, 2])
        self.assertEqual(range3["c2"], [3, 4, 5])
        self.assertEqual(range3["c3"], [6, 7])
        self.assertEqual(len(Assignor.owners(range3)), 8)
        self.assertEqual(RangeAssignor().assign(THREE, 2, {})["c3"], [])

    def test_round_robin(self):
        rr3 = RoundRobinAssignor().assign(THREE, 8, {})
        self.assertEqual(rr3["c1"], [0, 3, 6])
        self.assertEqual(rr3["c3"], [2, 5])

    def test_sticky_minimises_movement(self):
        sticky = StickyAssignor()
        s3 = sticky.assign(THREE, 8, {})
        self.assertEqual((len(s3["c1"]), len(s3["c2"]), len(s3["c3"])), (3, 3, 2))
        s4 = sticky.assign(FOUR, 8, s3)
        self.assertEqual(Assignor.moved(s3, s4), 2)
        self.assertTrue(all(len(l) == 2 for l in s4.values()))
        self.assertEqual(len(Assignor.owners(s4)), 8)
        range3 = RangeAssignor().assign(THREE, 8, {})
        r4 = RangeAssignor().assign(FOUR, 8, range3)
        self.assertEqual(Assignor.moved(range3, r4), 5)
        rr3 = RoundRobinAssignor().assign(THREE, 8, {})
        rr4 = RoundRobinAssignor().assign(FOUR, 8, rr3)
        self.assertGreater(Assignor.moved(rr3, rr4), Assignor.moved(s3, s4))
        s_leave = sticky.assign(["c1", "c3"], 8, s3)
        self.assertTrue(set(s3["c1"]) <= set(s_leave["c1"]))
        self.assertTrue(set(s3["c3"]) <= set(s_leave["c3"]))
        self.assertEqual(len(s_leave["c1"]), 4)
        self.assertEqual(len(s_leave["c3"]), 4)
        self.assertEqual(sticky.assign(FOUR, 8, s3), s4)
        self.assertEqual(RangeAssignor().assign(THREE, 8, {}), range3)


class CoordinatorTest(unittest.TestCase):
    def setUp(self):
        self.clock = ManualClock(0)
        self.g = GroupCoordinator("ledger-poster", 8, RangeAssignor(), 10_000, self.clock)

    def join_three(self):
        a1 = self.g.join("c1")
        self.g.join("c2")
        self.g.join("c3")
        return a1

    def expire_c2(self):
        self.join_three()
        self.clock.advance(9_999)
        self.g.heartbeat("c1", 3)
        self.g.heartbeat("c3", 3)
        self.assertEqual(self.g.expire_sessions(), [])
        self.assertEqual(self.g.generation(), 3)
        self.clock.advance(1)
        return self.g.expire_sessions()

    def test_generations_and_heartbeats(self):
        a1 = self.join_three()
        self.assertEqual(a1.generation, 1)
        self.assertEqual(len(a1.partitions), 8)
        self.assertEqual(self.g.generation(), 3)
        self.assertEqual(self.g.heartbeat("c1", 1), HeartbeatResult.REBALANCE_IN_PROGRESS)
        self.assertEqual(self.g.heartbeat("ghost", 3), HeartbeatResult.UNKNOWN_MEMBER)
        a1b = self.g.join("c1")
        self.assertEqual(a1b.generation, 3)
        self.assertEqual(self.g.generation(), 3)
        self.assertEqual(self.g.heartbeat("c1", 3), HeartbeatResult.OK)
        self.assertEqual(self.g.owner_of(3), "c2")

    def test_session_timeout(self):
        dead = self.expire_c2()
        self.assertEqual(dead, ["c2"])
        self.assertEqual(self.g.generation(), 4)
        self.assertEqual(self.g.owner_of(3), "c1")
        self.assertEqual(len(Assignor.owners(self.g.assignment())), 8)

    def test_commit_fencing(self):
        self.expire_c2()
        with self.assertRaises(UnknownMemberError):
            self.g.commit_offset("c2", 3, 3, 100)
        with self.assertRaises(IllegalGenerationError):
            self.g.commit_offset("c1", 3, 3, 100)
        with self.assertRaises(NotOwnerError):
            self.g.commit_offset("c3", 4, 0, 100)
        self.g.commit_offset("c1", 4, 3, 1_040)
        self.assertEqual(self.g.committed_offset(3), 1_040)
        self.assertEqual(self.g.committed_offset(7), 0)
        with self.assertRaises(ValueError):
            self.g.commit_offset("c1", 4, 3, -1)

    def test_leave_is_immediate(self):
        self.expire_c2()
        self.g.leave("c3")
        self.assertEqual(self.g.generation(), 5)
        self.assertEqual(self.g.members(), ["c1"])
        self.assertEqual(self.g.owner_of(7), "c1")
        with self.assertRaises(UnknownMemberError):
            self.g.leave("c3")
        with self.assertRaises(ValueError):
            self.g.owner_of(8)
        self.assertEqual(len(self.g.history()), 5)


if __name__ == "__main__":
    unittest.main()
