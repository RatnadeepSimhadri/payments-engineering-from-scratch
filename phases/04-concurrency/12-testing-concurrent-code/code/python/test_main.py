"""Tests for phases/04-concurrency/12-testing-concurrent-code. Run: python3 -m unittest -v"""
import unittest

from main import (
    AtomicAdd, Explorer, IllegalStateError, JavaRandom, Load, Lock, LockedCounter, Machine, Program, Programs,
    RacyCounter, Store, StressTest, ThreadSpec, Unlock,
)


class MachineTest(unittest.TestCase):
    def test_one_instruction_at_a_time(self):
        racy = Programs.racy_increment(10_000, 2_500)
        m = Machine(racy)
        self.assertEqual(m.enabled_threads(), [0, 1], "both threads enabled at start")
        m.step(0)
        m.step(0)
        m.step(0)
        self.assertTrue(m.finished(0) and not m.finished(1), "thread 0 finished after three steps")
        self.assertEqual(m.memory["balance"], 12_500, "memory updated by the store")
        with self.assertRaises(IllegalStateError):
            m.step(0)
        m.step(1)
        m.step(1)
        m.step(1)
        self.assertTrue(m.all_finished())
        self.assertEqual(m.memory["balance"], 15_000, "sequential schedule is correct")
        self.assertEqual(len(m.trace()), 6)
        self.assertEqual(m.trace()[0].thread, "T1")
        self.assertEqual(m.trace()[5].thread, "T2")


class InterleavingCountTest(unittest.TestCase):
    def test_counts(self):
        self.assertEqual(Programs.racy_increment(10_000, 2_500).interleavings_upper_bound(), 20, "C(6,3)")
        self.assertEqual(Programs.locked_increment(0, 1).interleavings_upper_bound(), 252, "C(10,5)")
        three = Program({"x": 0}, [
            ThreadSpec("A", [AtomicAdd("x", 1), AtomicAdd("x", 1)]),
            ThreadSpec("B", [AtomicAdd("x", 1)]),
            ThreadSpec("C", [AtomicAdd("x", 1)]),
        ])
        self.assertEqual(three.interleavings_upper_bound(), 12, "multinomial 4!/(2!1!1!)")


class ExhaustiveTest(unittest.TestCase):
    def test_finds_lost_update(self):
        racy = Programs.racy_increment(10_000, 2_500)
        r = Explorer.exhaustive(racy, Programs.balance_equals(15_000))
        self.assertEqual(r.schedules_explored, 20, "explores every interleaving")
        self.assertEqual(r.violations, 18, "finds the lost update in 18 of 20 schedules")
        self.assertEqual(r.deadlocks, 0)
        self.assertEqual(r.final_memory["balance"], 12_500, "counterexample ends with balance 12500")
        self.assertEqual(len(r.counterexample), 6, "counterexample is a full six-step trace")
        trace = r.counterexample
        t1_store = max(i for i, s in enumerate(trace) if s.thread == "T1" and isinstance(s.instruction, Store))
        t2_load = max(i for i, s in enumerate(trace) if s.thread == "T2" and isinstance(s.instruction, Load))
        self.assertLess(t2_load, t1_store, "in the counterexample T2 loads before T1 stores")

    def test_lock_proves_correct(self):
        locked = Explorer.exhaustive(Programs.locked_increment(10_000, 2_500), Programs.balance_equals(15_000))
        self.assertEqual(locked.schedules_explored, 2, "the lock leaves exactly two schedules")
        self.assertTrue(locked.clean())
        self.assertIsNone(locked.counterexample)

    def test_check_then_act_overdraft(self):
        debit = Explorer.exhaustive(Programs.check_then_act_debit(150, 100), Programs.balance_non_negative())
        self.assertEqual(debit.violations, 18, "check-then-act overdraws in 18 of 20 schedules")
        self.assertEqual(debit.final_memory["balance"], -50)
        safe = Explorer.exhaustive(Programs.check_then_act_debit(250, 100), Programs.balance_non_negative())
        self.assertTrue(safe.clean(), "with enough funds no schedule overdraws")


class DeadlockTest(unittest.TestCase):
    def test_unordered_deadlocks(self):
        dl = Explorer.exhaustive(Programs.transfer_unordered(), Programs.conserved(200))
        self.assertEqual(dl.deadlocks, 2)
        self.assertEqual(dl.violations, 0)
        self.assertEqual(len(dl.counterexample), 2, "deadlock trace is the two lock acquisitions")
        self.assertTrue(all(isinstance(s.instruction, Lock) for s in dl.counterexample))

    def test_ordered_is_clean(self):
        ok = Explorer.exhaustive(Programs.transfer_ordered(), Programs.conserved(200))
        self.assertEqual(ok.schedules_explored, 2)
        self.assertTrue(ok.clean(), "ordered locks: no deadlock, money conserved")

    def test_machine_reports_deadlock(self):
        dead = Machine(Programs.transfer_unordered())
        dead.step(0)
        dead.step(1)
        self.assertTrue(dead.deadlocked())
        self.assertEqual(dead.enabled_threads(), [])

    def test_unlock_not_held(self):
        bad = Machine(Program({}, [ThreadSpec("X", [Unlock("L")])]))
        with self.assertRaises(IllegalStateError):
            bad.step(0)


class RandomExplorationTest(unittest.TestCase):
    def test_seeded_and_reproducible(self):
        racy = Programs.racy_increment(10_000, 2_500)
        s1 = Explorer.random(racy, Programs.balance_equals(15_000), 42, 500)
        s2 = Explorer.random(racy, Programs.balance_equals(15_000), 42, 500)
        self.assertEqual(s1, s2, "same seed, same result")
        self.assertEqual(s1.schedules_explored, 500)
        self.assertGreater(s1.violations, 300, "random schedules find the bug in most samples")
        s3 = Explorer.random(racy, Programs.balance_equals(15_000), 7, 500)
        self.assertTrue(s3.violations != s1.violations or s3.counterexample != s1.counterexample)
        self.assertTrue(Explorer.random(Programs.locked_increment(0, 1), Programs.balance_equals(2), 1, 200).clean())

    def test_java_random_matches_jdk(self):
        # new java.util.Random(42).nextInt(10) x5 == 0, 3, 8, 4, 0 in the JDK
        r = JavaRandom(42)
        self.assertEqual([r.next_int(10) for _ in range(5)], [0, 3, 8, 4, 0])


class StressHarnessTest(unittest.TestCase):
    def test_locked_exact_racy_never_gains(self):
        locked = StressTest.run(LockedCounter, 4, 5_000, 1, 5)
        self.assertEqual(locked.rounds_with_lost_updates, 0, "locked counter is exact in every round")
        self.assertEqual(locked.expected_per_round, 20_000)
        racy = StressTest.run(RacyCounter, 4, 5_000, 1, 5)
        self.assertGreaterEqual(racy.total_lost, 0, "racy counter never gains updates")
        self.assertEqual(racy.rounds, 5, "stress harness reports every round")


if __name__ == "__main__":
    unittest.main()
