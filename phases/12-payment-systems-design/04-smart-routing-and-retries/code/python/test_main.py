"""Tests for phases/12-payment-systems-design/04-smart-routing-and-retries. Run: python3 -m unittest -v"""

import unittest

from main import (EUR, USD, AuthRateEstimator, CardNetwork, CircuitState, DeclineClass, DeclineCodes, FakeClock,
                  JavaRandom, Money, PaymentRequest, ProcessorConfig, ProcessorHealth, ProcessorResponse, RetryBudget,
                  RouteOutcome, Router, RoutingPolicy, ScriptedGateway, processors)

_seq = 0


def visa(minor: int) -> PaymentRequest:
    global _seq
    _seq += 1
    return PaymentRequest(f"pay_t{_seq}", Money.of_minor(minor, USD), CardNetwork.VISA, "fp")


def trained() -> AuthRateEstimator:
    """acq_a is cheaper; acq_b is trusted more. With these counts acq_b ranks first for Visa."""
    est = AuthRateEstimator(0.85, 20, 0.995)
    for i in range(100):
        est.record("acq_a", CardNetwork.VISA, i < 86)
    for i in range(100):
        est.record("acq_b", CardNetwork.VISA, i < 93)
    return est


def router(gw: ScriptedGateway, clock: FakeClock, est: AuthRateEstimator, budget: RetryBudget, max_attempts: int) -> Router:
    return Router(processors(), lambda _id, c: ProcessorHealth(c, 10, 5, 30_000), est, budget, gw, clock,
                  RoutingPolicy(max_attempts, 0), JavaRandom(1))


def leader(r: Router) -> str:
    return r.rank(visa(10_000))[0].processor.id


class FeesAndClassificationTest(unittest.TestCase):
    def test_fees(self) -> None:
        a = processors()[0]
        self.assertEqual(a.fee(Money.of_minor(10_000, USD)), Money.of_minor(240, USD), "2.20% + 0.20 on 100.00 = 2.40")
        self.assertEqual(a.fee(Money.of_minor(25, USD)), Money.of_minor(21, USD), "rounds half-up: 0.55 -> 1 minor, + 20")

    def test_classification(self) -> None:
        self.assertIs(DeclineCodes.classify(ProcessorResponse.approved()), DeclineClass.APPROVED)
        self.assertIs(DeclineCodes.classify(ProcessorResponse.of("91")), DeclineClass.SOFT_CASCADE)
        self.assertIs(DeclineCodes.classify(ProcessorResponse.connect_error()), DeclineClass.SOFT_CASCADE)
        self.assertIs(DeclineCodes.classify(ProcessorResponse.of("51")), DeclineClass.SOFT_RETRY_LATER)
        self.assertIs(DeclineCodes.classify(ProcessorResponse.of("43")), DeclineClass.HARD)
        self.assertIs(DeclineCodes.classify(ProcessorResponse.of("1A")), DeclineClass.AUTH_REQUIRED)
        self.assertIs(DeclineCodes.classify(ProcessorResponse.timeout()), DeclineClass.AMBIGUOUS)
        self.assertIs(DeclineCodes.classify(ProcessorResponse.of("Z9")), DeclineClass.HARD, "unknown code fails safe")
        self.assertIs(DeclineCodes.classify(ProcessorResponse.of("05", "03")), DeclineClass.HARD)
        self.assertIs(DeclineCodes.classify(ProcessorResponse.of("91", "21")), DeclineClass.HARD)
        self.assertIs(DeclineCodes.classify(ProcessorResponse.of("05", "02")), DeclineClass.SOFT_CASCADE)
        self.assertTrue(ProcessorResponse.timeout().technical())
        self.assertTrue(ProcessorResponse.connect_error().technical())
        self.assertFalse(ProcessorResponse.of("91").technical())


class EstimatorTest(unittest.TestCase):
    def test_prior_and_counts(self) -> None:
        fresh = AuthRateEstimator(0.85, 20, 1.0)
        self.assertAlmostEqual(fresh.estimate("x", CardNetwork.VISA), 0.85, delta=1e-12)
        for i in range(80):
            fresh.record("x", CardNetwork.VISA, i < 40)
        self.assertAlmostEqual(fresh.estimate("x", CardNetwork.VISA), 0.57, delta=1e-9)
        self.assertAlmostEqual(fresh.estimate("x", CardNetwork.MASTERCARD), 0.85, delta=1e-12, msg="per network")

    def test_decay_reacts_faster(self) -> None:
        decaying = AuthRateEstimator(0.85, 20, 0.95)
        flat = AuthRateEstimator(0.85, 20, 1.0)
        for est in (decaying, flat):
            for _ in range(200):
                est.record("y", CardNetwork.VISA, True)
            for _ in range(40):
                est.record("y", CardNetwork.VISA, False)
        self.assertLess(decaying.estimate("y", CardNetwork.VISA), 0.5)
        self.assertGreater(flat.estimate("y", CardNetwork.VISA), 0.8)
        with self.assertRaises(ValueError):
            AuthRateEstimator(1.0, 20, 0.99)


class RankingAndCascadeTest(unittest.TestCase):
    def test_ranking(self) -> None:
        clock = FakeClock(0)
        gw = ScriptedGateway()
        r = router(gw, clock, trained(), RetryBudget(100, 10_000, 10_000), 3)
        ranked = r.rank(visa(10_000))
        self.assertEqual(len(ranked), 2, "amex-only processor not eligible for Visa")
        self.assertEqual(ranked[0].processor.id, "acq_b", "higher auth rate beats lower fee at 100.00")
        self.assertAlmostEqual(ranked[0].expected_margin_minor, ranked[0].auth_rate * (10_000 - 320), delta=1e-6)
        tie = router(gw, clock, AuthRateEstimator(0.85, 20, 1.0), RetryBudget(100, 10_000, 10_000), 3)
        self.assertEqual(tie.rank(visa(10_000))[0].processor.id, "acq_a", "equal auth rates -> cheaper first")
        self.assertIs(r.route(PaymentRequest("p_amex", Money.of_minor(100, EUR), CardNetwork.AMEX, "fp")).outcome,
                      RouteOutcome.NO_ROUTE)

    def test_cascade_rules(self) -> None:
        clock = FakeClock(0)
        gw = ScriptedGateway()
        r = router(gw, clock, trained(), RetryBudget(100, 10_000, 10_000), 3)
        gw.script("acq_b", ProcessorResponse.of("91"))
        cascaded = r.route(visa(10_000))
        self.assertIs(cascaded.outcome, RouteOutcome.APPROVED)
        self.assertEqual(len(cascaded.attempts), 2)
        self.assertEqual(cascaded.approved_by(), "acq_a")
        # Hard declines lower acq_b's estimate, so the ranking may flip; always script the current leader.
        for code in ("43", "54", "59", "14", "N7"):
            with self.subTest(code=code):
                gw.script(leader(r), ProcessorResponse.of(code))
                res = r.route(visa(10_000))
                self.assertIs(res.outcome, RouteOutcome.DECLINED)
                self.assertEqual(len(res.attempts), 1)
        gw.script(leader(r), ProcessorResponse.of("51"))
        nsf = r.route(visa(10_000))
        self.assertEqual(len(nsf.attempts), 1)
        self.assertIn("retry later", nsf.stop_reason)
        gw.script(leader(r), ProcessorResponse.of("1A"))
        self.assertIn("3-D Secure", r.route(visa(10_000)).stop_reason)
        calls_before = len(gw.calls())
        gw.script(leader(r), ProcessorResponse.timeout())
        to = r.route(visa(10_000))
        self.assertIs(to.outcome, RouteOutcome.PENDING_UNKNOWN)
        self.assertEqual(len(to.attempts), 1)
        self.assertEqual(len(gw.calls()), calls_before + 1, "never cascaded")
        gw.script("acq_b", ProcessorResponse.of("05")).script("acq_a", ProcessorResponse.of("05"))
        both = r.route(visa(10_000))
        self.assertIs(both.outcome, RouteOutcome.DECLINED)
        self.assertEqual(len(both.attempts), 2)
        self.assertIn("all eligible", both.stop_reason)

    def test_attempt_cap(self) -> None:
        clock = FakeClock(0)
        three = [
            ProcessorConfig("p1", frozenset({CardNetwork.VISA}), frozenset({USD}), 200, 0),
            ProcessorConfig("p2", frozenset({CardNetwork.VISA}), frozenset({USD}), 250, 0),
            ProcessorConfig("p3", frozenset({CardNetwork.VISA}), frozenset({USD}), 300, 0),
        ]
        g3 = ScriptedGateway().script("p1", ProcessorResponse.of("91")).script("p2", ProcessorResponse.of("91"))
        capped = Router(three, lambda _id, c: ProcessorHealth(c, 10, 5, 30_000), AuthRateEstimator(0.85, 20, 1.0),
                        RetryBudget(100, 10_000, 10_000), g3, clock, RoutingPolicy(2, 0), JavaRandom(1))
        cap_res = capped.route(visa(1_000))
        self.assertEqual(len(cap_res.attempts), 2)
        self.assertIn("attempt cap", cap_res.stop_reason)
        self.assertNotIn(f"p3:{cap_res.payment_id}", g3.calls())

    def test_health_and_auth_rate_are_separate_signals(self) -> None:
        clock = FakeClock(0)
        sep = AuthRateEstimator(0.85, 20, 1.0)
        g4 = ScriptedGateway()
        learner = router(g4, clock, sep, RetryBudget(100, 10_000, 10_000), 3)
        g4.script("acq_a", ProcessorResponse.connect_error())
        learner.route(visa(1_000))
        self.assertEqual(sep.observations("acq_a", CardNetwork.VISA), 0, "connect error does not touch auth-rate counts")
        g4.script("acq_a", ProcessorResponse.of("05"))
        learner.route(visa(1_000))
        self.assertEqual(sep.observations("acq_a", CardNetwork.VISA), 1)
        self.assertLess(sep.estimate("acq_a", CardNetwork.VISA), 0.85)


class CircuitBreakerTest(unittest.TestCase):
    def test_state_machine(self) -> None:
        hc = FakeClock(0)
        h = ProcessorHealth(hc, 10, 5, 30_000)
        for _ in range(4):
            h.record_failure()
        self.assertIs(h.state(), CircuitState.CLOSED)
        h.record_failure()
        self.assertIs(h.state(), CircuitState.OPEN)
        self.assertFalse(h.allows())
        hc.advance(29_999)
        self.assertIs(h.state(), CircuitState.OPEN)
        hc.advance(1)
        self.assertIs(h.state(), CircuitState.HALF_OPEN)
        self.assertTrue(h.allows())
        h.record_failure()
        self.assertIs(h.state(), CircuitState.OPEN, "failed probe re-opens")
        hc.advance(30_000)
        h.record_success()
        self.assertIs(h.state(), CircuitState.CLOSED, "successful probe closes")
        spread = ProcessorHealth(hc, 10, 5, 30_000)
        for i in range(20):
            if i % 3 == 0:
                spread.record_failure()
            else:
                spread.record_success()
        self.assertIs(spread.state(), CircuitState.CLOSED, "failures spread thinly never reach 5-in-10")

    def test_open_circuit_removes_processor(self) -> None:
        hc = FakeClock(0)
        g5 = ScriptedGateway()
        cb = router(g5, hc, trained(), RetryBudget(100, 10_000, 10_000), 3)
        for _ in range(5):
            g5.script("acq_b", ProcessorResponse.connect_error())
            cb.route(visa(1_000))
        self.assertTrue(all(c.processor.id != "acq_b" for c in cb.rank(visa(1_000))))
        before = len(g5.calls())
        rerouted = cb.route(visa(1_000))
        self.assertEqual(rerouted.approved_by(), "acq_a")
        self.assertEqual(len(g5.calls()), before + 1)


class RetryBudgetTest(unittest.TestCase):
    def test_tokens(self) -> None:
        budget = RetryBudget(100, 10_000, 2_000)
        self.assertTrue(budget.try_acquire_retry())
        self.assertTrue(budget.try_acquire_retry())
        self.assertFalse(budget.try_acquire_retry())
        for _ in range(10):
            budget.record_first_attempt()
        self.assertTrue(budget.try_acquire_retry(), "10 first attempts earn exactly 1 retry at a 10% ratio")
        self.assertFalse(budget.try_acquire_retry())
        cap_b = RetryBudget(100, 1_500, 0)
        for _ in range(100):
            cap_b.record_first_attempt()
        self.assertEqual(cap_b.balance_milli(), 1_500)

    def test_storm(self) -> None:
        clock = FakeClock(0)
        storm = ScriptedGateway()
        sr = router(storm, clock, trained(), RetryBudget(100, 10_000, 3_000), 3)
        calls = 0
        for _ in range(20):
            storm.script("acq_a", ProcessorResponse.of("91")).script("acq_b", ProcessorResponse.of("91"))
            calls += len(sr.route(visa(1_000)).attempts)
        self.assertEqual(calls, 25, "20 first attempts + 5 budgeted cascades")


class ValidationTest(unittest.TestCase):
    def test_validation(self) -> None:
        with self.assertRaises(ValueError):
            visa(0)
        with self.assertRaises(ValueError):
            RoutingPolicy(0, 0)


class JavaRandomTest(unittest.TestCase):
    def test_matches_java_util_random(self) -> None:
        # Reference values from `new java.util.Random(42)`: nextInt(100) x3, then nextDouble().
        r = JavaRandom(42)
        self.assertEqual([r.next_int(100) for _ in range(3)], [30, 63, 48])
        self.assertAlmostEqual(r.next_double(), 0.047939305137387644, places=15)
        q = JavaRandom(7)
        self.assertAlmostEqual(q.next_double(), 0.7306990420600421, places=15)
        self.assertEqual((q.next_int(3), q.next_int(7)), (0, 2))


if __name__ == "__main__":
    unittest.main()
