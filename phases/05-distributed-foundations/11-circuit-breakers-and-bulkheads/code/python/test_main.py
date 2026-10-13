"""Tests for phases/05-distributed-foundations/11-circuit-breakers-and-bulkheads. Run: python3 -m unittest -v
Same cases as MainTest.java and main_test.go."""
import unittest

from main import (Admission, BreakerConfig, Bulkhead, CallOutcome, CircuitBreaker, FakeClock, GuardedCall,
                  OutageSimulation, SlidingWindow, State, Transition)

# 50% of at least 10 calls in a 5 s window of 1 s buckets; open for 3 s; 3 half-open probes.
CONFIG = BreakerConfig(50, 10, 5_000, 1_000, 3_000, 3)


def drive(cb: CircuitBreaker, successes: int, failures: int) -> None:
    for _ in range(successes):
        cb.try_acquire(); cb.on_success()
    for _ in range(failures):
        cb.try_acquire(); cb.on_failure()


class SlidingWindowTest(unittest.TestCase):
    def setUp(self) -> None:
        self.w = SlidingWindow(5_000, 1_000)
        for _ in range(8):
            self.w.record(0, True)
        for _ in range(12):
            self.w.record(1_500, False)

    def test_counts_and_integer_rate(self):
        self.assertEqual((self.w.total(1_500), self.w.failures(1_500)), (20, 12))
        self.assertEqual(self.w.failure_rate_percent(1_500), 60)

    def test_old_bucket_ages_out(self):
        self.assertEqual(self.w.total(5_000), 12)
        self.assertEqual(self.w.failure_rate_percent(5_000), 100)

    def test_empty_window_rate_is_zero_not_division_error(self):
        self.assertEqual(self.w.total(6_500), 0)
        self.assertEqual(self.w.failure_rate_percent(6_500), 0)

    def test_reused_bucket_starts_from_zero(self):
        self.w.record(5_000, True)        # slot 0 is reused for the period starting at 5000
        self.assertEqual((self.w.total(5_000), self.w.failures(5_000)), (13, 12))
        self.w.reset()
        self.assertEqual(self.w.total(5_000), 0)

    def test_window_must_be_multiple_of_bucket(self):
        with self.assertRaises(ValueError):
            SlidingWindow(5_000, 1_500)


class CircuitBreakerTest(unittest.TestCase):
    def setUp(self) -> None:
        self.clock = FakeClock(0)
        self.cb = CircuitBreaker(CONFIG, self.clock)

    def test_stays_closed_below_minimum_calls(self):
        drive(self.cb, 5, 4)
        self.assertIs(self.cb.state(), State.CLOSED)
        self.assertEqual(self.cb.failure_rate_percent(), 44)

    def test_opens_at_threshold_and_records_transition(self):
        drive(self.cb, 5, 5)
        self.assertIs(self.cb.state(), State.OPEN)
        self.assertEqual(self.cb.transitions(), [Transition(State.CLOSED, State.OPEN, 0)])

    def test_open_rejects_until_duration_elapses(self):
        drive(self.cb, 5, 5)
        self.assertFalse(self.cb.try_acquire())
        self.assertEqual(self.cb.rejected(), 1)
        self.clock.advance(2_999)
        self.assertFalse(self.cb.try_acquire())
        self.assertIs(self.cb.state(), State.OPEN)
        self.clock.advance(1)
        self.assertTrue(self.cb.try_acquire())
        self.assertIs(self.cb.state(), State.HALF_OPEN)

    def test_half_open_admits_exactly_the_permits(self):
        drive(self.cb, 0, 10)
        self.clock.advance(3_000)
        admitted = [self.cb.try_acquire() for _ in range(4)]
        self.assertEqual(admitted, [True, True, True, False])
        self.assertEqual(self.cb.rejected(), 1)

    def test_probe_failure_reopens_and_restarts_timer(self):
        drive(self.cb, 0, 10)
        self.clock.advance(3_000)
        self.assertTrue(self.cb.try_acquire())
        self.cb.on_failure()
        self.assertIs(self.cb.state(), State.OPEN)
        self.clock.advance(2_999)
        self.assertFalse(self.cb.try_acquire())
        self.clock.advance(1)
        self.assertTrue(self.cb.try_acquire())
        self.assertIs(self.cb.state(), State.HALF_OPEN)

    def test_all_probes_succeed_closes_and_resets_window(self):
        drive(self.cb, 0, 10)
        self.clock.advance(3_000)
        self.cb.try_acquire(); self.cb.on_success()
        self.assertIs(self.cb.state(), State.HALF_OPEN)      # one of three is not enough
        self.cb.try_acquire(); self.cb.on_success()
        self.cb.try_acquire(); self.cb.on_success()
        self.assertIs(self.cb.state(), State.CLOSED)
        self.assertEqual(self.cb.failure_rate_percent(), 0)
        self.assertEqual(self.cb.transitions()[-1], Transition(State.HALF_OPEN, State.CLOSED, 3_000))
        self.assertEqual(len(self.cb.transitions()), 3)

    def test_late_success_while_open_is_ignored(self):
        drive(self.cb, 0, 10)
        self.cb.on_success()
        self.assertIs(self.cb.state(), State.OPEN)
        self.assertEqual(len(self.cb.transitions()), 1)

    def test_config_validation(self):
        with self.assertRaises(ValueError):
            BreakerConfig(0, 10, 5_000, 1_000, 3_000, 3)
        with self.assertRaises(ValueError):
            BreakerConfig(50, 10, 5_000, 1_500, 3_000, 3)
        with self.assertRaises(ValueError):
            BreakerConfig(50, 10, 5_000, 1_000, 3_000, 0)


class BulkheadTest(unittest.TestCase):
    def test_admits_up_to_limit_then_rejects_and_counts(self):
        bh = Bulkhead(3)
        self.assertEqual([bh.try_acquire() for _ in range(4)], [True, True, True, False])
        self.assertEqual((bh.in_flight(), bh.available(), bh.rejected()), (3, 0, 1))

    def test_release_returns_a_permit(self):
        bh = Bulkhead(3)
        for _ in range(3):
            bh.try_acquire()
        bh.release()
        self.assertEqual(bh.available(), 1)
        self.assertTrue(bh.try_acquire())

    def test_release_without_acquire_is_an_error(self):
        with self.assertRaises(RuntimeError):
            Bulkhead(1).release()
        with self.assertRaises(ValueError):
            Bulkhead(0)


class GuardedCallTest(unittest.TestCase):
    def setUp(self) -> None:
        self.clock = FakeClock(0)
        self.cb = CircuitBreaker(CONFIG, self.clock)
        self.bh = Bulkhead(2)
        self.g = GuardedCall(self.cb, self.bh)

    def test_bulkhead_rejection_is_not_a_dependency_failure(self):
        self.assertIs(self.g.begin(), Admission.ADMITTED)
        self.assertIs(self.g.begin(), Admission.ADMITTED)
        self.assertIs(self.g.begin(), Admission.REJECTED_BULKHEAD)
        self.assertEqual(self.cb.failure_rate_percent(), 0)
        self.assertEqual(self.cb.transitions(), [])
        self.g.complete(False); self.g.complete(False)
        self.assertEqual(self.bh.in_flight(), 0)
        self.assertEqual(self.cb.failure_rate_percent(), 100)

    def test_rejected_open_returns_the_bulkhead_permit(self):
        for _ in range(10):
            self.g.call(lambda: False)
        self.assertIs(self.cb.state(), State.OPEN)
        self.assertIs(self.g.begin(), Admission.REJECTED_OPEN)
        self.assertEqual(self.bh.in_flight(), 0)
        self.assertIs(self.g.call(lambda: True), CallOutcome.REJECTED_OPEN)

    def test_exception_is_a_failure_and_true_is_a_success(self):
        def boom() -> bool:
            raise RuntimeError("boom")
        self.assertIs(self.g.call(boom), CallOutcome.FAILURE)
        self.assertIs(self.g.call(lambda: True), CallOutcome.SUCCESS)
        self.assertEqual(self.bh.in_flight(), 0)


class OutageSimulationTest(unittest.TestCase):
    def test_totals_match_java_and_go(self):
        r = OutageSimulation.run(CONFIG, 10, 50, 25_000, 5_000, 15_000, 2_000)
        self.assertEqual((r.total_success, r.total_failure, r.total_rejected_open, r.total_rejected_bulkhead),
                         (260, 23, 157, 60))

    def test_trips_at_9s_and_recovers(self):
        r = OutageSimulation.run(CONFIG, 10, 50, 25_000, 5_000, 15_000, 2_000)
        self.assertEqual(r.transitions[0], Transition(State.CLOSED, State.OPEN, 9_000))
        self.assertEqual(len(r.transitions), 5)
        self.assertIs(r.transitions[-1].to_state, State.CLOSED)
        self.assertIs(r.rows[24].state_at_end, State.CLOSED)

    def test_no_call_waits_on_dead_processor_once_open(self):
        r = OutageSimulation.run(CONFIG, 10, 50, 25_000, 5_000, 15_000, 2_000)
        self.assertEqual(r.count_in(9_000, 14_000, CallOutcome.FAILURE), 10)
        self.assertEqual(r.count_in(15_000, 17_000, CallOutcome.FAILURE), 0)

    def test_without_breaker_more_calls_burn_on_timeouts(self):
        never_trips = BreakerConfig(50, 1_000_000, 5_000, 1_000, 3_000, 3)
        with_breaker = OutageSimulation.run(CONFIG, 10, 50, 25_000, 5_000, 15_000, 2_000)
        without = OutageSimulation.run(never_trips, 10, 50, 25_000, 5_000, 15_000, 2_000)
        self.assertEqual((without.total_failure, without.total_rejected_open), (50, 0))
        self.assertGreater(without.total_failure, with_breaker.total_failure)


if __name__ == "__main__":
    unittest.main()
