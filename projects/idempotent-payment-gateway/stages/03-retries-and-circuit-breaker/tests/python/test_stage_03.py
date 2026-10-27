"""Stage 3 tests for projects/idempotent-payment-gateway (Python): backoff ceilings and full jitter from a seeded Rng,
retryable-error classification, the retry budget, the CLOSED/OPEN/HALF_OPEN breaker on an injected clock, and the
Gateway that ties the store, the state machine and the client together.
Port of Stage03Test.java: same checks, same names, same order. The Rng is xorshift64*, bit-exact with the Java one,
so the drawn delays (46, 26, 131, 226 for seed 42) are the same numbers."""

import unittest

from main import (
    USD,
    BackoffPolicy,
    ChargeRequest,
    CircuitBreaker,
    CircuitState,
    FakeClock,
    Gateway,
    IdempotencyStore,
    Money,
    PaymentState,
    ProcessorOutcome,
    RetryBudget,
    RetryingClient,
    Rng,
    ScriptedProcessor,
)


def s3_request(key: str, amount: str) -> ChargeRequest:
    return ChargeRequest("m_1", key, Money.parse(amount, USD), "tok_visa", "order-42")


def s3_client(script: str, clock: FakeClock, breaker: CircuitBreaker, budget: RetryBudget, seed: int) -> RetryingClient:
    return RetryingClient(ScriptedProcessor(script), BackoffPolicy(100, 2_000, 4), budget, breaker, Rng(seed), clock)


class Stage03ResilienceTest(unittest.TestCase):
    def test_scripted_processor(self):
        req = s3_request("key-1", "19.99")
        scripted = ScriptedProcessor("UDA")
        self.assertIs(scripted.charge(req).outcome, ProcessorOutcome.UNAVAILABLE, "script step 1 unavailable")
        self.assertIs(scripted.charge(req).outcome, ProcessorOutcome.DECLINED, "script step 2 declined")
        self.assertTrue(scripted.charge(req).outcome is ProcessorOutcome.APPROVED and scripted.charge(req).reference == "auth-4", "script step 3 approved with reference")
        self.assertTrue(scripted.charge(req).outcome is ProcessorOutcome.APPROVED and scripted.calls() == 5, "last character repeats")
        with self.assertRaises(ValueError, msg="script must be A/D/U"):
            ScriptedProcessor("AXU")
        with self.assertRaises(ValueError, msg="script must be non-empty"):
            ScriptedProcessor("")

    def test_backoff_ceiling_and_full_jitter_from_the_seeded_rng(self):
        policy = BackoffPolicy(100, 2_000, 4)
        self.assertEqual(policy.ceiling_for(1), 100, "ceiling retry 1")
        self.assertEqual(policy.ceiling_for(2), 200, "ceiling retry 2")
        self.assertEqual(policy.ceiling_for(4), 800, "ceiling retry 4")
        self.assertTrue(policy.ceiling_for(6) == 2_000 and policy.ceiling_for(40) == 2_000, "ceiling is capped")
        self.assertEqual(policy.max_attempts(), 4, "max_attempts")
        with self.assertRaises(ValueError, msg="retry numbers start at 1"):
            policy.ceiling_for(0)
        with self.assertRaises(ValueError, msg="cap below base rejected"):
            BackoffPolicy(500, 100, 3)
        with self.assertRaises(ValueError, msg="max_attempts at least 1"):
            BackoffPolicy(100, 200, 0)
        rng = Rng(42)
        self.assertEqual([policy.delay_before(r, rng) for r in (1, 2, 3, 4)], [46, 26, 131, 226], "seed 42 draws 46, 26, 131, 226")
        self.assertTrue(policy.delay_before(1, Rng(42)) == 46 and policy.delay_before(1, Rng(42)) == 46, "same seed, same schedule")
        spread = Rng(7)
        self.assertTrue(all(0 <= policy.delay_before(3, spread) <= 400 for _ in range(500)), "full jitter never exceeds the ceiling")
        self.assertTrue(Rng(42).next_below(101) == 46 and Rng(7).next_below(1000) == 822, "Rng.next_below is uniform over [0, bound)")
        with self.assertRaises(ValueError, msg="next_below needs a positive bound"):
            Rng(1).next_below(0)

    def test_retry_budget(self):
        budget = RetryBudget(2, 3)
        self.assertEqual(budget.tokens(), 2, "budget starts with initial tokens")
        self.assertTrue(budget.try_withdraw() and budget.try_withdraw() and budget.tokens() == 0, "withdraw twice")
        self.assertFalse(budget.try_withdraw(), "empty budget refuses")
        budget.deposit(5)
        self.assertEqual(budget.tokens(), 3, "deposit is capped at max")
        with self.assertRaises(ValueError, msg="negative deposit"):
            budget.deposit(-1)
        with self.assertRaises(ValueError, msg="initial above max"):
            RetryBudget(5, 3)

    def test_circuit_breaker_on_the_injected_clock(self):
        clock = FakeClock(100_000)
        breaker = CircuitBreaker(clock, 3, 30_000, 2)
        self.assertTrue(breaker.state() is CircuitState.CLOSED and breaker.allow_request() and breaker.retry_after_millis() == 0, "starts CLOSED")
        breaker.record_failure()
        breaker.record_failure()
        self.assertIs(breaker.state(), CircuitState.CLOSED, "two failures still CLOSED")
        breaker.record_success()
        breaker.record_failure()
        breaker.record_failure()
        self.assertIs(breaker.state(), CircuitState.CLOSED, "success resets the consecutive count")
        breaker.record_failure()
        self.assertTrue(breaker.state() is CircuitState.OPEN and not breaker.allow_request(), "third consecutive failure OPENs")
        self.assertEqual(breaker.retry_after_millis(), 30_000, "retry_after is the full open window")
        clock.advance(29_999)
        self.assertTrue(breaker.state() is CircuitState.OPEN and breaker.retry_after_millis() == 1, "still OPEN one ms before the window ends")
        clock.advance(1)
        self.assertTrue(breaker.state() is CircuitState.HALF_OPEN and breaker.allow_request(), "HALF_OPEN after the window")
        breaker.record_failure()
        self.assertTrue(breaker.state() is CircuitState.OPEN and breaker.retry_after_millis() == 30_000, "one HALF_OPEN failure reopens")
        clock.advance(30_000)
        breaker.record_success()
        self.assertIs(breaker.state(), CircuitState.HALF_OPEN, "one of two successes keeps HALF_OPEN")
        breaker.record_success()
        self.assertIs(breaker.state(), CircuitState.CLOSED, "second success CLOSEs")
        with self.assertRaises(ValueError, msg="open window must be positive"):
            CircuitBreaker(clock, 1, 0, 1)

    def test_retrying_client_only_unavailable_is_retried(self):
        req = s3_request("key-1", "19.99")
        c1 = FakeClock(0)
        ok = s3_client("UUA", c1, CircuitBreaker(c1, 5, 30_000, 1), RetryBudget(10, 10), 42).charge(req)
        self.assertTrue(ok.outcome is ProcessorOutcome.APPROVED and ok.attempts == 3 and ok.reference == "auth-3" and ok.reason == "approved", "UUA approves on the third attempt")
        self.assertTrue(ok.waited_millis == 72 and c1.now_millis() == 72, "waited 46 + 26 ms from seed 42")

        c2 = FakeClock(0)
        declining = ScriptedProcessor("D")
        declined = RetryingClient(declining, BackoffPolicy(100, 2_000, 4), RetryBudget(10, 10), CircuitBreaker(c2, 5, 30_000, 1), Rng(42), c2).charge(req)
        self.assertTrue(declined.outcome is ProcessorOutcome.DECLINED and declined.attempts == 1 and declining.calls() == 1 and declined.reason == "declined", "a decline is never retried")
        self.assertTrue(declined.waited_millis == 0 and c2.now_millis() == 0, "no waiting on a decline")

        c3 = FakeClock(0)
        exhausted = s3_client("U", c3, CircuitBreaker(c3, 10, 30_000, 1), RetryBudget(10, 10), 42).charge(req)
        self.assertTrue(exhausted.outcome is ProcessorOutcome.UNAVAILABLE and exhausted.attempts == 4 and exhausted.reason == "retries exhausted", "dead processor exhausts max_attempts")
        self.assertTrue(exhausted.waited_millis == 203 and c3.now_millis() == 203, "three waits: 46 + 26 + 131")

        c4 = FakeClock(0)
        tiny = RetryBudget(1, 10)
        budgeted = s3_client("U", c4, CircuitBreaker(c4, 10, 30_000, 1), tiny, 42).charge(req)
        self.assertTrue(budgeted.outcome is ProcessorOutcome.UNAVAILABLE and budgeted.attempts == 2 and budgeted.reason == "retry budget exhausted" and tiny.tokens() == 0, "drained budget stops retries")

        c5 = FakeClock(0)
        trip = CircuitBreaker(c5, 2, 30_000, 1)
        dead = ScriptedProcessor("U")
        tripping = RetryingClient(dead, BackoffPolicy(100, 2_000, 4), RetryBudget(10, 10), trip, Rng(42), c5)
        tripped = tripping.charge(req)
        self.assertTrue(tripped.outcome is ProcessorOutcome.UNAVAILABLE and tripped.attempts == 2 and tripped.reason == "circuit open" and trip.state() is CircuitState.OPEN, "breaker opens mid-retry")
        refused = tripping.charge(req)
        self.assertTrue(refused.attempts == 0 and refused.reason == "circuit open" and dead.calls() == 2, "open breaker refuses without calling the processor")

    def test_gateway_store_plus_state_machine_plus_client(self):
        gc = FakeClock(1_000_000)
        store = IdempotencyStore(gc, 60_000)
        script = ScriptedProcessor("ADUUUA")
        client = RetryingClient(script, BackoffPolicy(100, 2_000, 2), RetryBudget(10, 10), CircuitBreaker(gc, 3, 30_000, 1), Rng(42), gc)
        gateway = Gateway(store, client, gc)

        first = gateway.charge(s3_request("key-1", "19.99"))
        self.assertTrue(first.status == 201 and not first.replayed, "approved charge is 201")
        self.assertTrue('"payment_id":"pay_1"' in first.body and '"state":"succeeded"' in first.body, "body names the payment and state")
        p1 = gateway.payment("pay_1")
        self.assertTrue(p1 is not None and p1.state() is PaymentState.SUCCEEDED and p1.captured() == Money.parse("19.99", USD), "payment is SUCCEEDED and captured")
        replay = gateway.charge(s3_request("key-1", "19.99"))
        self.assertTrue(replay.status == 201 and replay.body == first.body and replay.replayed, "same request replays the same body, flagged")
        self.assertTrue(script.calls() == 1 and len(gateway.payments()) == 1, "replay does not call the processor again")
        conflict = gateway.charge(s3_request("key-1", "29.99"))
        self.assertTrue(conflict.status == 422 and len(gateway.payments()) == 1, "same key, other amount is 422")

        declined_resp = gateway.charge(s3_request("key-2", "5.00"))
        p2 = gateway.payment("pay_2")
        self.assertTrue(declined_resp.status == 402 and p2 is not None and p2.state() is PaymentState.FAILED, "declined charge is 402 and FAILED")
        self.assertTrue(gateway.charge(s3_request("key-2", "5.00")).replayed and script.calls() == 2, "declines are stored and replayed")

        unavailable = gateway.charge(s3_request("key-3", "7.00"))
        self.assertTrue(unavailable.status == 503 and "retries exhausted" in unavailable.body and script.calls() == 4, "unavailable is 503 after max_attempts")
        p3 = gateway.payment("pay_3")
        self.assertTrue(p3 is not None and p3.state() is PaymentState.FAILED, "failed payment is recorded")
        self.assertIsNone(store.get("m_1", "key-3"), "503 releases the key")
        again = gateway.charge(s3_request("key-3", "7.00"))
        self.assertTrue(again.status == 503 and not again.replayed and len(gateway.payments()) == 4, "retrying a 503 is a fresh attempt, not a replay")
        open_resp = gateway.charge(s3_request("key-4", "7.00"))
        self.assertTrue(open_resp.status == 503 and "circuit open" in open_resp.body and script.calls() == 5, "open breaker short-circuits with 503")
        gc.advance(30_000)
        recovered = gateway.charge(s3_request("key-3", "7.00"))
        self.assertTrue(recovered.status == 201 and '"state":"succeeded"' in recovered.body and script.calls() == 6, "after the window the same key succeeds")


if __name__ == "__main__":
    unittest.main()
