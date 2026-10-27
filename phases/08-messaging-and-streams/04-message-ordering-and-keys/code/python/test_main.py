"""Tests for phases/08-messaging-and-streams/04-message-ordering-and-keys. Run: python3 -m unittest -v"""
import unittest

from main import (
    USD,
    CommitTracker,
    Completion,
    FaultInjector,
    JavaRandom,
    KeyedLaneScheduler,
    LatencyStats,
    Message,
    Money,
    Partition,
    Partitioner,
    ProducerSim,
    SequenceTracker,
    SequentialScheduler,
    Topic,
    Workload,
)


def seqs(messages):
    return [m.seq for m in messages]


class PartitionerTest(unittest.TestCase):
    def test_partitioner(self):
        self.assertEqual(Partitioner.fnv1a32(b"a"), 0xE40C292C)
        self.assertEqual(Partitioner.partition_for("acct-1001", 3), Partitioner.partition_for("acct-1001", 3))
        self.assertTrue(all(0 <= Partitioner.partition_for(f"k{i}", 7) < 7 for i in range(1000)))
        self.assertTrue(any(Partitioner.partition_for(f"k{i}", 3) != Partitioner.partition_for(f"k{i}", 4) for i in range(100)))
        with self.assertRaises(ValueError):
            Partitioner.partition_for("x", 0)

    def test_topic_keeps_each_key_in_one_partition(self):
        topic = Topic("t", 4)
        keys = ["a", "b", "c", "d", "e", "f", "g"]
        for m in Workload.build(keys, 70, 1, 1, "none", 1):
            topic.append(m)
        one_home = ordered_per_key = True
        for k in keys:
            home = -1
            for p in range(4):
                s = topic.partition(p).sequence_of(k)
                if not s:
                    continue
                if home != -1:
                    one_home = False
                home = p
                if s != list(range(len(s))):
                    ordered_per_key = False
        self.assertTrue(one_home)
        self.assertTrue(ordered_per_key)
        self.assertEqual(sum(topic.partition(p).log_end_offset() for p in range(4)), 70)


class SchedulerTest(unittest.TestCase):
    def setUp(self):
        self.hol = Partition(0)
        self.hol_keys = ["fast-1", "fast-2", "fast-3", "slow", "fast-4", "fast-5"]
        for m in Workload.build(self.hol_keys, 60, 10, 2, "slow", 40):
            self.hol.append(m)

    def test_sequential_head_of_line_blocking(self):
        sequential = SequentialScheduler().run(self.hol)
        self.assertEqual(sequential[4].started_at, sequential[3].completed_at)
        self.assertGreaterEqual(sequential[4].latency_millis, 40 - 10 + 2)
        self.assertLessEqual(sequential[10].completed_at, sequential[11].started_at)
        self.assertTrue(Workload.per_key_order_holds(self.hol, sequential))
        self.assertGreaterEqual(LatencyStats.excluding(sequential, "slow").max, 30)

    def test_keyed_lanes(self):
        eight = KeyedLaneScheduler(8)
        laned = eight.run(self.hol)
        self.assertTrue(Workload.per_key_order_holds(self.hol, laned))
        laned_fast = LatencyStats.excluding(laned, "slow")
        slow_alone = all(k == "slow" or eight.lane_for(k) != eight.lane_for("slow") for k in self.hol_keys)
        self.assertTrue(not slow_alone or laned_fast.max == 2)
        self.assertGreaterEqual(LatencyStats.of([c for c in laned if c.key == "slow"]).max, 40)
        self.assertEqual(eight.final_committable(), 60)
        self.assertGreater(eight.max_completed_ahead(), 0)
        self.assertEqual(eight.lane_for("slow"), eight.lane_for("slow"))
        with self.assertRaises(ValueError):
            KeyedLaneScheduler(0)


class CommitTrackerTest(unittest.TestCase):
    def test_low_water_mark(self):
        tracker = CommitTracker(0)
        self.assertEqual(tracker.complete(1), 0)
        self.assertEqual(tracker.completed_ahead(), 1)
        self.assertEqual(tracker.complete(0), 2)
        self.assertEqual(tracker.completed_ahead(), 0)
        tracker.complete(3)
        self.assertEqual(tracker.committable(), 2)
        self.assertEqual(tracker.complete(2), 4)
        with self.assertRaises(RuntimeError):
            tracker.complete(3)
        with self.assertRaises(RuntimeError):
            tracker.complete(0)


class LatencyStatsTest(unittest.TestCase):
    def test_stats(self):
        fixed = [Completion(0, "k", 0, 10, 10), Completion(1, "k", 0, 30, 30), Completion(2, "k", 0, 20, 20), Completion(3, "k", 0, 40, 40)]
        stats = LatencyStats.of(fixed)
        self.assertEqual((stats.max, stats.mean, stats.p50, stats.count), (40, 25, 20, 4))
        self.assertEqual(LatencyStats.of([]).count, 0)


class ReorderingTest(unittest.TestCase):
    def setUp(self):
        self.sends = Workload.build(["acct"], 8, 1, 1, "none", 1)
        self.faults = FaultInjector({2, 5})

    def test_retries_reorder_with_several_in_flight(self):
        one = ProducerSim.deliver(self.sends, 1, self.faults, False)
        self.assertEqual(seqs(one.appended), [0, 1, 2, 3, 4, 5, 6, 7])
        self.assertEqual(one.retries, 2)
        five = ProducerSim.deliver(self.sends, 5, self.faults, False)
        self.assertEqual(seqs(five.appended), [0, 1, 3, 4, 2, 6, 7, 5])
        self.assertEqual(len(five.appended), 8)
        self.assertFalse(SequenceTracker.is_ordered(five.appended))
        self.assertTrue(SequenceTracker.is_ordered(one.appended))
        st = SequenceTracker()
        for m in five.appended:
            st.observe(m)
        self.assertEqual((st.gaps(), st.duplicates()), (2, 2))
        guarded = ProducerSim.deliver(self.sends, 5, self.faults, True)
        self.assertTrue(SequenceTracker.is_ordered(guarded.appended))
        self.assertEqual(len(guarded.appended), 8)
        self.assertEqual((guarded.rejected, guarded.retries), (3, 2))
        no_faults = ProducerSim.deliver(self.sends, 5, FaultInjector(set()), False)
        self.assertTrue(SequenceTracker.is_ordered(no_faults.appended))
        self.assertEqual(no_faults.retries, 0)
        with self.assertRaises(ValueError):
            ProducerSim.deliver(self.sends, 0, self.faults, False)

    def test_seeded_run(self):
        seeded = FaultInjector.random(42, 2000, 0.05)
        many = Workload.build(["acct"], 2000, 1, 1, "none", 1)
        unguarded = ProducerSim.deliver(many, 5, seeded, False)
        guarded_many = ProducerSim.deliver(many, 5, seeded, True)
        self.assertFalse(SequenceTracker.is_ordered(unguarded.appended))
        self.assertTrue(SequenceTracker.is_ordered(guarded_many.appended))
        self.assertEqual(len(unguarded.appended), 2000)
        self.assertEqual(len(guarded_many.appended), 2000)
        self.assertEqual(FaultInjector.random(42, 2000, 0.05).fails_first_attempt(7), seeded.fails_first_attempt(7))
        # the same seed yields the same counts as java.util.Random(42) in the Java demo
        self.assertEqual((unguarded.retries, guarded_many.rejected), (90, 151))

    def test_java_random_matches_jdk(self):
        # new java.util.Random(42).nextDouble() == 0.7275636800328681 (first value), 0.6832234717598454 (second)
        r = JavaRandom(42)
        self.assertAlmostEqual(r.next_double(), 0.7275636800328681, places=15)
        self.assertAlmostEqual(r.next_double(), 0.6832234717598454, places=15)


class MessageTest(unittest.TestCase):
    def test_construction(self):
        with self.assertRaises(ValueError):
            Message("k", -1, "AUTH", Money.of_minor(1, USD), 0, 1)
        self.assertEqual(str(Money.of_minor(1999, USD)), "19.99 USD")
        with self.assertRaises(TypeError):
            Money(19.99, USD)


if __name__ == "__main__":
    unittest.main()
