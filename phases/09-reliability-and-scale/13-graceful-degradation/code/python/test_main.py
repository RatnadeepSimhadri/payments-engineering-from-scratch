"""Tests for phases/09-reliability-and-scale/13-graceful-degradation. Run: python3 -m unittest -v"""
import unittest

from main import (COOLDOWN, INTERVAL, TARGET, WINDOW, BrownoutController, CoDelQueue, DegradingPolicy, FifoPolicy,
                  Flags, FraudScorer, Level, Outcome, Priority, Request, Simulation, standard_scenario)


class CoDelTest(unittest.TestCase):
    def test_no_drops_under_target(self):
        calm = CoDelQueue(5, 100)
        calm_served = 0
        for now in range(1000):
            calm.enqueue(Request(now, Priority.NORMAL, now), now)
            if calm.dequeue(now + 2) is not None:  # every request waits 2 ms < 5 ms target
                calm_served += 1
        self.assertEqual(calm_served, 1000)
        self.assertEqual(calm.dropped, 0)
        self.assertFalse(calm.is_dropping())

    def test_drops_only_after_a_full_interval_above_target(self):
        q = CoDelQueue(5, 100)
        for i in range(400):
            q.enqueue(Request(i, Priority.NORMAL, 0), 0)
        first_drop_at = -1
        served = 0
        now = 0
        while now < 400 and q.size() > 0:
            before = q.dropped
            if q.dequeue(now) is not None:
                served += 1
            if q.dropped > before and first_drop_at < 0:
                first_drop_at = now
            now += 1
        self.assertEqual(first_drop_at, 105)  # t=5 + one 100 ms interval
        self.assertTrue(3 <= q.dropped <= 10)
        self.assertEqual(served + q.dropped, 400)
        self.assertTrue(not q.is_dropping() or q.size() == 0)
        with self.assertRaises(ValueError):
            CoDelQueue(100, 50)

    def test_transient_spike_shorter_than_interval_is_tolerated(self):
        spike = CoDelQueue(5, 100)
        for i in range(50):
            spike.enqueue(Request(i, Priority.HIGH, 0), 0)
        now = 0
        while spike.size() > 0:  # drains in 50 ms, max sojourn 49 ms > target but < interval
            spike.dequeue(now)
            now += 1
        self.assertEqual(spike.dropped, 0)
        self.assertGreaterEqual(spike.max_sojourn_seen, 40)


class BrownoutTest(unittest.TestCase):
    def test_escalation_hysteresis_and_flags(self):
        flags = Flags()
        bc = BrownoutController(5, 100, 3, flags, 0)
        self.assertTrue(bc.level() == Level.NORMAL and flags.get("fraud.mode", "") == "full")
        bc.observe(50)
        bc.tick(100)
        self.assertEqual(bc.level(), Level.SHED_LOW)
        self.assertEqual(flags.get("fraud.mode", ""), "rules_only")
        for t in (200, 300, 400):
            bc.observe(50)
            bc.tick(t)
        self.assertEqual(bc.level(), Level.CRITICAL_ONLY)
        self.assertTrue(bc.level().sheds(Priority.HIGH) and bc.level().sheds(Priority.LOW) and not bc.level().sheds(Priority.CRITICAL))
        bc.observe(1)
        bc.tick(500)
        bc.observe(1)
        bc.tick(600)
        self.assertEqual(bc.level(), Level.CRITICAL_ONLY)  # two calm windows are not enough with cooldown 3
        bc.observe(1)
        bc.tick(700)
        self.assertEqual(bc.level(), Level.SHED_NORMAL)
        for t in (800, 900, 1000):
            bc.observe(4)
            bc.tick(t)
        self.assertEqual(bc.level(), Level.SHED_NORMAL)  # hysteresis band (target/2, target] holds
        self.assertEqual(len(bc.transitions), 4)
        self.assertEqual(bc.millis_at_level[Level.NORMAL.ordinal], 100)
        self.assertTrue(Level.SHED_LOW.sheds(Priority.LOW) and not Level.SHED_LOW.sheds(Priority.NORMAL))
        self.assertFalse(Level.NORMAL.sheds(Priority.LOW))
        self.assertIs(BrownoutController.Level, Level)

    def test_fraud_scorer_follows_the_flag(self):
        fs = FraudScorer()
        f2 = Flags()
        self.assertEqual(fs.service_millis(f2), FraudScorer.FULL_MILLIS)
        f2.set("fraud.mode", "rules_only")
        self.assertEqual(fs.service_millis(f2), FraudScorer.RULES_ONLY_MILLIS)


class PolicyTest(unittest.TestCase):
    def test_admission_and_priority_order(self):
        pol = DegradingPolicy(5, 100, 100, 3, 0)
        o = Outcome()
        self.assertTrue(pol.admit(Request(1, Priority.LOW, 0), 0, o))
        self.assertTrue(pol.admit(Request(2, Priority.CRITICAL, 0), 0, o) and pol.next(0, o).priority == Priority.CRITICAL)
        self.assertEqual(pol.next(0, o).priority, Priority.LOW)
        self.assertIsNone(pol.next(0, o))


class MeasuredTest(unittest.TestCase):
    def test_under_overload_shedding_protects_critical(self):
        sim = standard_scenario(11)
        fifo = sim.run(FifoPolicy())
        deg = DegradingPolicy(TARGET, INTERVAL, WINDOW, COOLDOWN, 0)
        shed = sim.run(deg)
        self.assertEqual(fifo.total_offered(), shed.total_offered())
        self.assertGreater(fifo.percentile(Priority.CRITICAL, 0.99), 1000)
        self.assertLess(shed.percentile(Priority.CRITICAL, 0.99), 100)
        self.assertLessEqual(shed.percentile(Priority.CRITICAL, 0.50), 10)
        self.assertGreaterEqual(shed.completed(Priority.CRITICAL), shed.offered[Priority.CRITICAL] - 10)
        self.assertEqual(shed.shed_at_door[Priority.CRITICAL], 0)
        self.assertTrue(shed.shed_at_door[Priority.LOW] > shed.shed_at_door[Priority.NORMAL] * 0.9 and shed.shed_at_door[Priority.LOW] > 1000)
        self.assertTrue(fifo.total_shed() == 0 and fifo.still_queued > 1000)
        self.assertLess(shed.still_queued, 50)
        self.assertEqual(shed.total_offered(), shed.total_completed() + shed.total_shed() + shed.total_dropped() + shed.still_queued)
        self.assertEqual(fifo.total_offered(), fifo.total_completed() + fifo.still_queued)
        self.assertTrue(deg.brownout.transitions and deg.brownout.level() == Level.NORMAL and deg.flags.get("fraud.mode", "") == "full")
        self.assertEqual(deg.brownout.millis_at_level[Level.CRITICAL_ONLY.ordinal], 0)

    def test_java_random_parity_with_the_docs(self):
        shed = standard_scenario(11).run(DegradingPolicy(TARGET, INTERVAL, WINDOW, COOLDOWN, 0))
        self.assertEqual(shed.total_offered(), 12038)
        self.assertTrue(shed.offered[Priority.CRITICAL] == 3577 and shed.shed_at_door[Priority.LOW] == 1456
                        and shed.total_dropped() == 3)

    def test_light_load_is_invisible(self):
        light = Simulation(8, 3, 600, 600, 0, 0, 3000)
        quiet = DegradingPolicy(TARGET, INTERVAL, WINDOW, COOLDOWN, 0)
        calm_out = light.run(quiet)
        self.assertTrue(calm_out.total_shed() == 0 and calm_out.total_dropped() == 0)
        self.assertEqual(quiet.brownout.transitions, [])

    def test_determinism(self):
        fifo = standard_scenario(11).run(FifoPolicy())
        again = standard_scenario(11).run(FifoPolicy())
        self.assertTrue(again.total_completed() == fifo.total_completed()
                        and again.percentile(Priority.LOW, 0.99) == fifo.percentile(Priority.LOW, 0.99))


if __name__ == "__main__":
    unittest.main()
