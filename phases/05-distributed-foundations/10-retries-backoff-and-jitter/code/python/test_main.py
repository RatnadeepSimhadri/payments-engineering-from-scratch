"""Tests for phases/05-distributed-foundations/10-retries-backoff-and-jitter. Run: python3 -m unittest -v"""

import unittest

from main import (
    FakeClock,
    Jitter,
    Outcome,
    Retrier,
    RetryBudget,
    RetryPolicy,
    RetryStorm,
    Rng,
    StopReason,
)

PLAIN = RetryPolicy(100, 10_000, 6, Jitter.NONE)


class RngTest(unittest.TestCase):
    def test_seed_42_matches_java_and_go(self):
        rng = Rng(42)
        self.assertEqual(rng.next_long(), 6255019084209693600)
        self.assertEqual(rng.next_long(), -4016670646968046118)

    def test_next_below_in_range_and_replayable(self):
        self.assertTrue(0 <= Rng(9).next_below(7) < 7)
        self.assertEqual(Rng(123).next_long(), Rng(123).next_long())

    def test_rejects_non_positive_bound(self):
        with self.assertRaises(ValueError):
            Rng(1).next_below(0)


class BackoffTest(unittest.TestCase):
    def test_exponential_doubles_from_base(self):
        self.assertEqual((PLAIN.exponential(0), PLAIN.exponential(1), PLAIN.exponential(4)), (100, 200, 1600))

    def test_exponential_never_exceeds_cap(self):
        self.assertEqual(RetryPolicy(100, 1_000, 8, Jitter.NONE).exponential(7), 1000)
        self.assertEqual(RetryPolicy(100, 10_000, 100, Jitter.NONE).exponential(99), 10_000)

    def test_none_schedule_is_doubling_sequence(self):
        self.assertEqual(PLAIN.schedule(Rng(42)), [100, 200, 400, 800, 1600])
        self.assertEqual(len(PLAIN.schedule(Rng(1))), 5)

    def test_policy_validation(self):
        with self.assertRaises(ValueError):
            RetryPolicy(100, 50, 3, Jitter.NONE)
        with self.assertRaises(ValueError):
            RetryPolicy(100, 100, 0, Jitter.NONE)


class JitterTest(unittest.TestCase):
    def test_seed_42_schedules_match_java_and_go(self):
        self.assertEqual(RetryPolicy(100, 10_000, 6, Jitter.FULL).schedule(Rng(42)), [46, 26, 131, 226, 241])
        self.assertEqual(RetryPolicy(100, 10_000, 6, Jitter.EQUAL).schedule(Rng(42)), [68, 179, 379, 654, 994])
        self.assertEqual(RetryPolicy(100, 10_000, 6, Jitter.DECORRELATED).schedule(Rng(42)), [190, 474, 675, 569, 810])

    def test_bounds_hold_for_many_seeds(self):
        full = RetryPolicy(100, 10_000, 6, Jitter.FULL)
        equal = RetryPolicy(100, 10_000, 6, Jitter.EQUAL)
        decorr = RetryPolicy(100, 10_000, 6, Jitter.DECORRELATED)
        for seed in range(1, 201):
            rng = Rng(seed)
            for attempt in range(6):
                exp = PLAIN.exponential(attempt)
                self.assertTrue(0 <= full.delay(attempt, 100, rng) <= exp)
                self.assertTrue(exp // 2 <= equal.delay(attempt, 100, rng) <= exp)
                self.assertTrue(100 <= decorr.delay(attempt, 300, rng) <= 900)

    def test_decorrelated_respects_cap(self):
        self.assertLessEqual(RetryPolicy(100, 500, 6, Jitter.DECORRELATED).delay(3, 400, Rng(5)), 500)


class RetrierTest(unittest.TestCase):
    def test_succeeds_on_third_attempt_and_waits_on_fake_clock(self):
        clock = FakeClock(0)
        flap = [Outcome.TIMEOUT, Outcome.TIMEOUT, Outcome.OK]
        r = Retrier(PLAIN, None, clock, Rng(7)).execute(lambda a: flap[min(a, len(flap) - 1)])
        self.assertTrue(r.succeeded())
        self.assertEqual(r.attempts, 3)
        self.assertEqual((r.total_delay_ms, clock.now_ms()), (300, 300))
        self.assertEqual(r.delays, [100, 200])

    def test_non_retryable_outcomes_are_not_retried(self):
        r = Retrier(PLAIN, None, FakeClock(0), Rng(7)).execute(lambda a: Outcome.CARD_DECLINED)
        self.assertEqual((r.stop_reason, r.attempts, r.total_delay_ms), (StopReason.NON_RETRYABLE, 1, 0))
        r = Retrier(PLAIN, None, FakeClock(0), Rng(7)).execute(lambda a: Outcome.BAD_REQUEST)
        self.assertIs(r.stop_reason, StopReason.NON_RETRYABLE)

    def test_exhausts_attempts_without_final_sleep(self):
        r = Retrier(RetryPolicy(100, 10_000, 4, Jitter.NONE), None, FakeClock(0), Rng(7)).execute(lambda a: Outcome.TIMEOUT)
        self.assertEqual((r.stop_reason, r.attempts), (StopReason.ATTEMPTS_EXHAUSTED, 4))
        self.assertEqual(r.total_delay_ms, 100 + 200 + 400)

    def test_retryable_classification(self):
        self.assertTrue(Outcome.TIMEOUT.retryable() and Outcome.RATE_LIMITED.retryable())
        self.assertFalse(Outcome.UNAUTHORIZED.retryable() or Outcome.OK.retryable())
        self.assertIsNot(Outcome.CARD_DECLINED, Outcome.OK)  # enum members must not alias


class RetryBudgetTest(unittest.TestCase):
    def test_deposits_and_cap(self):
        budget = RetryBudget(20, 10)
        self.assertEqual(budget.tokens(), 0)
        self.assertFalse(budget.try_withdraw())
        for _ in range(5):
            budget.deposit()
        self.assertEqual(budget.tokens(), 1)
        self.assertTrue(budget.try_withdraw())
        self.assertFalse(budget.try_withdraw())
        for _ in range(1000):
            budget.deposit()
        self.assertEqual(budget.tokens(), 10)

    def test_budget_stops_retries_once_drained(self):
        budget = RetryBudget(20, 10)
        for _ in range(1000):
            budget.deposit()
        retrier = Retrier(RetryPolicy(100, 1_000, 3, Jitter.NONE), budget, FakeClock(0), Rng(1))
        spent = 0
        last = None
        for _ in range(8):
            rr = retrier.execute(lambda a: Outcome.SERVER_ERROR)
            spent += rr.attempts - 1
            last = rr.stop_reason
        self.assertIs(last, StopReason.BUDGET_EXHAUSTED)
        self.assertEqual(budget.tokens(), 0)
        self.assertLessEqual(spent, 10 + 8)

    def test_rejects_bad_percent(self):
        with self.assertRaises(ValueError):
            RetryBudget(101, 1)


class RetryStormTest(unittest.TestCase):
    NONE = RetryPolicy(100, 5_000, 8, Jitter.NONE)
    FULL = RetryPolicy(100, 5_000, 8, Jitter.FULL)

    def test_lockstep_without_jitter(self):
        s = RetryStorm.run(200, 40, 100, 1_000, 60, self.NONE, 2024)
        self.assertEqual((s.peak_load(), s.peak_load_from(1_000)), (200, 200))
        self.assertEqual((s.succeeded, s.busy_buckets()), (80, 6))
        self.assertEqual(s.succeeded + s.gave_up + s.still_waiting, 200)

    def test_jitter_lowers_peak_and_spreads_load(self):
        s_none = RetryStorm.run(200, 40, 100, 1_000, 60, self.NONE, 2024)
        s_full = RetryStorm.run(200, 40, 100, 1_000, 60, self.FULL, 2024)
        self.assertLess(s_full.peak_load_from(1_000), s_none.peak_load_from(1_000))
        self.assertGreater(s_full.busy_buckets(), s_none.busy_buckets())
        self.assertEqual((s_full.succeeded, s_full.still_waiting), (200, 0))
        self.assertEqual(sum(s_full.load_per_bucket), s_full.total_attempts)

    def test_storm_is_deterministic_for_seed(self):
        a = RetryStorm.run(200, 40, 100, 1_000, 60, self.FULL, 2024)
        b = RetryStorm.run(200, 40, 100, 1_000, 60, self.FULL, 2024)
        self.assertEqual((a.peak_load_from(1_000), a.total_attempts), (b.peak_load_from(1_000), b.total_attempts))

    def test_decorrelated_also_beats_lockstep(self):
        s_none = RetryStorm.run(200, 40, 100, 1_000, 60, self.NONE, 2024)
        s_dec = RetryStorm.run(200, 40, 100, 1_000, 60, RetryPolicy(100, 5_000, 8, Jitter.DECORRELATED), 2024)
        self.assertLess(s_dec.peak_load_from(1_000), s_none.peak_load_from(1_000))
        self.assertEqual(s_dec.succeeded, 200)


if __name__ == "__main__":
    unittest.main()
