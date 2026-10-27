"""Tests for phases/08-messaging-and-streams/07-stream-processing-windows. Run: python3 -m unittest -v"""
import unittest

from main import (
    MIN,
    SEC,
    USD,
    Aggregate,
    Money,
    SessionAggregator,
    SlidingWindows,
    StreamRunner,
    TumblingWindows,
    VelocityRule,
    Window,
    WindowedAggregator,
    txn,
)


def find(results, key, start, end):
    for r in results:
        if r.key == key and r.window.start == start and r.window.end == end:
            return r
    return None


BURST = [txn("A", 100, 50 * SEC), txn("A", 100, 56 * SEC), txn("A", 100, 63 * SEC), txn("A", 100, 69 * SEC)]


class AssignerTest(unittest.TestCase):
    def test_tumbling(self):
        tumbling = TumblingWindows(MIN)
        self.assertEqual(tumbling.assign(MIN), [Window(MIN, 2 * MIN)])
        self.assertEqual(tumbling.assign(2 * MIN - 1), [Window(MIN, 2 * MIN)])
        self.assertEqual(len(tumbling.assign(12_345)), 1)
        self.assertTrue(tumbling.assign(12_345)[0].contains(12_345))
        with self.assertRaises(ValueError):
            TumblingWindows(0)
        with self.assertRaises(ValueError):
            Window(5, 5)

    def test_sliding(self):
        sliding = SlidingWindows(MIN, 10 * SEC)
        at63 = sliding.assign(63 * SEC)
        self.assertEqual(len(at63), 6)
        self.assertTrue(all(w.contains(63 * SEC) for w in at63))
        self.assertEqual(at63[0], Window(10 * SEC, 70 * SEC))
        self.assertEqual(at63[5], Window(60 * SEC, 120 * SEC))
        self.assertEqual(len(sliding.assign(60 * SEC)), 6)
        self.assertEqual(sliding.assign(60 * SEC)[5].start, 60 * SEC)
        with self.assertRaises(ValueError):
            SlidingWindows(MIN, 2 * MIN)
        with self.assertRaises(ValueError):
            SlidingWindows(MIN, 7 * SEC)


class AggregateTest(unittest.TestCase):
    def test_money_arithmetic(self):
        a = Aggregate.empty(USD).add(Money.of_minor(1_299, USD)).add(Money.of_minor(2_450, USD))
        self.assertEqual((a.count, a.sum.minor_units, a.max.minor_units), (2, 3_749, 2_450))
        self.assertEqual(a.merge(Aggregate.empty(USD).add(Money.of_minor(1, USD))).count, 3)
        with self.assertRaises(TypeError):
            Money(1.0, USD)


class TumblingOperatorTest(unittest.TestCase):
    def test_boundary_problem_and_trigger(self):
        tumbling_agg = WindowedAggregator(TumblingWindows(MIN), USD)
        out = StreamRunner(tumbling_agg, 10 * SEC).run(BURST, 3 * MIN)
        self.assertEqual(find(out, "A", 0, MIN).aggregate.count, 2)
        self.assertEqual(find(out, "A", MIN, 2 * MIN).aggregate.count, 2)
        self.assertEqual(find(out, "A", 0, MIN).emitted_at, MIN)
        self.assertEqual(find(out, "A", MIN, 2 * MIN).emitted_at, 2 * MIN)
        self.assertEqual(tumbling_agg.state_size(), 0)
        self.assertEqual(VelocityRule(3).breaches(out), [])
        self.assertEqual(sum(r.aggregate.count for r in out), 4)

    def test_direct_timer(self):
        direct = WindowedAggregator(TumblingWindows(MIN), USD)
        direct.process(txn("A", 100, 50 * SEC))
        self.assertEqual(direct.on_timer(MIN - 1), [])
        self.assertEqual(direct.peek("A", Window(0, MIN)).count, 1)
        self.assertEqual(len(direct.on_timer(MIN)), 1)
        self.assertEqual(direct.on_timer(MIN), [])
        self.assertEqual(direct.on_timer(10 * MIN), [])
        with self.assertRaises(ValueError):
            direct.process(txn("A", 1, 30 * SEC))


class SlidingOperatorTest(unittest.TestCase):
    def test_burst_is_caught(self):
        sliding_agg = WindowedAggregator(SlidingWindows(MIN, 10 * SEC), USD)
        runner = StreamRunner(sliding_agg, 10 * SEC)
        out = runner.run(BURST, 3 * MIN)
        breaches = VelocityRule(3).breaches(out)
        self.assertTrue(breaches)
        self.assertTrue(all(r.aggregate.count == 4 for r in breaches))
        self.assertEqual(find(out, "A", 10 * SEC, 70 * SEC).aggregate.count, 4)
        self.assertEqual(find(out, "A", 50 * SEC, 110 * SEC).aggregate.count, 4)
        self.assertEqual(find(out, "A", 0, MIN).aggregate.count, 2)
        self.assertEqual(find(out, "A", 60 * SEC, 120 * SEC).aggregate.count, 2)
        self.assertEqual(len(breaches), 5)
        self.assertTrue(1 < runner.max_state_size() <= 12)
        self.assertTrue(all(r.aggregate.sum.minor_units == 400 for r in breaches))


class SessionOperatorTest(unittest.TestCase):
    def test_extension_and_close(self):
        session = SessionAggregator(30 * SEC, USD)
        session.process(txn("C", 500, 0))
        self.assertEqual(session.open_sessions("C"), [Window(0, 30 * SEC)])
        session.process(txn("C", 500, 25 * SEC))
        self.assertEqual(session.open_sessions("C"), [Window(0, 55 * SEC)])
        session.process(txn("C", 500, 55 * SEC))
        self.assertEqual(len(session.open_sessions("C")), 2)
        self.assertEqual(session.on_timer(54 * SEC), [])
        closed = session.on_timer(55 * SEC)
        self.assertEqual(len(closed), 1)
        self.assertEqual(closed[0].window, Window(0, 55 * SEC))
        self.assertEqual(closed[0].aggregate.count, 2)
        self.assertEqual(session.state_size(), 1)
        with self.assertRaises(ValueError):
            SessionAggregator(0, USD)

    def test_mixed_stream(self):
        mixed = list(BURST) + [txn("B", 4_000, m * MIN + 30 * SEC) for m in range(3)]
        mixed.sort(key=lambda x: x.time)
        out = StreamRunner(SessionAggregator(30 * SEC, USD), 10 * SEC).run(mixed, 5 * MIN)
        a = [r for r in out if r.key == "A"]
        self.assertEqual(len(a), 1)
        self.assertEqual(a[0].aggregate.count, 4)
        self.assertEqual(len([r for r in out if r.key == "B"]), 3)
        self.assertEqual(a[0].window, Window(50 * SEC, 99 * SEC))


class RunnerTest(unittest.TestCase):
    def test_runner_invariants(self):
        with self.assertRaises(ValueError):
            StreamRunner(WindowedAggregator(TumblingWindows(MIN), USD), 0)
        coarse = StreamRunner(WindowedAggregator(TumblingWindows(MIN), USD), 45 * SEC).run(BURST, 3 * MIN)
        self.assertEqual(find(coarse, "A", 0, MIN).emitted_at, 90 * SEC)


if __name__ == "__main__":
    unittest.main()
