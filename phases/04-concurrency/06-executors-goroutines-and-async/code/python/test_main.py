"""Tests for phases/04-concurrency/06-executors-goroutines-and-async. Run: python3 -m unittest -v"""
import asyncio
import unittest
from concurrent.futures import ThreadPoolExecutor

from main import (EUR, USD, BlockingFanOut, FanOut, ManualTimer, Money, Outcome, PaymentRequest, ProcessorAdapter,
                  ScriptedAdapter, ThreadPerTaskExecutor, count_started_before_release, then_apply,
                  wait_for_cancels_inner)

REQUEST = PaymentRequest("pay_001", Money.of_minor(12_999, USD))


def usd(minor):
    return Money.of_minor(minor, USD)


class ThrowingAdapter(ProcessorAdapter):
    """quote() raises synchronously, as a client library with a broken configuration would."""

    def name(self):
        return "broken"

    def quote(self, request):
        raise ValueError("no API key configured")  # Java: IllegalArgumentException


class MoneyTest(unittest.TestCase):
    def test_money(self):
        self.assertEqual(str(usd(305)), "3.05 USD")
        self.assertLess(usd(275).compare_to(usd(329)), 0)
        with self.assertRaises(ValueError):
            usd(1).compare_to(Money.of_minor(1, EUR))


class ManualTimerTest(unittest.TestCase):
    def test_manual_timer(self):
        timer = ManualTimer()
        a = timer.after(100)
        b = timer.after(50)
        self.assertTrue(not a.done() and not b.done() and timer.outstanding() == 2)
        timer.advance(49)
        self.assertTrue(not b.done() and timer.now_millis() == 49)
        timer.advance(1)
        self.assertTrue(b.done() and not a.done() and timer.outstanding() == 1)
        timer.advance(1000)
        self.assertTrue(a.done() and timer.outstanding() == 0)
        order = []
        then_apply(timer.after(30), lambda _: order.append("30"))
        then_apply(timer.after(10), lambda _: order.append("10"))
        then_apply(timer.after(10), lambda _: order.append("10b"))
        timer.advance(100)
        self.assertEqual(order, ["10", "10b", "30"])  # deadline order, ties in creation order
        cancelled = timer.after(5)
        cancelled.cancel()
        self.assertEqual(timer.outstanding(), 0)
        timer.advance(10)
        self.assertTrue(cancelled.cancelled())
        with self.assertRaises(ValueError):
            timer.after(-1)


class WithTimeoutTest(unittest.TestCase):
    def test_with_timeout(self):
        timer = ManualTimer()
        immediate = FanOut.with_timeout(ScriptedAdapter.immediate("alpha", usd(329)), REQUEST, 800, timer).result()
        self.assertTrue(immediate.outcome is Outcome.OK and immediate.fee == usd(329) and immediate.processor == "alpha")
        self.assertEqual(timer.outstanding(), 0)  # an answered call cancels its alarm

        beta = ScriptedAdapter.scripted("beta")
        beta_quote = FanOut.with_timeout(beta, REQUEST, 800, timer)
        timer.advance(799)
        self.assertFalse(beta_quote.done())
        beta.respond(usd(275))
        self.assertTrue(beta_quote.result().outcome is Outcome.OK and beta_quote.result().fee == usd(275))
        self.assertEqual(timer.outstanding(), 0)

        gamma = ScriptedAdapter.scripted("gamma")
        gamma_quote = FanOut.with_timeout(gamma, REQUEST, 800, timer)
        timer.advance(800)
        self.assertTrue(gamma_quote.done() and gamma_quote.result().outcome is Outcome.TIMED_OUT)
        self.assertEqual(gamma_quote.result().detail, "no answer within 800 ms")
        self.assertEqual(gamma.outstanding(), 1)  # the timeout abandons the wait; it does not cancel the request
        gamma.respond(usd(1))
        self.assertIs(gamma_quote.result().outcome, Outcome.TIMED_OUT)  # a late answer is ignored

        delta = ScriptedAdapter.scripted("delta")
        delta_quote = FanOut.with_timeout(delta, REQUEST, 800, timer)
        delta.fail("HTTP 503 from processor")
        self.assertTrue(delta_quote.result().outcome is Outcome.FAILED
                        and delta_quote.result().detail == "RuntimeError: HTTP 503 from processor")

        broken = FanOut.with_timeout(ThrowingAdapter(), REQUEST, 800, timer).result()
        self.assertTrue(broken.outcome is Outcome.FAILED and broken.detail.startswith("ValueError"))

    def test_answer_wins_over_its_own_alarm_cancellation(self):
        """Cancelling the alarm completes `deadline` exceptionally; the answer must still win the race."""
        timer = ManualTimer()
        beta = ScriptedAdapter.scripted("beta")
        q = FanOut.with_timeout(beta, REQUEST, 800, timer)
        beta.respond(usd(275))
        self.assertIs(q.result().outcome, Outcome.OK)


class QuoteAllTest(unittest.TestCase):
    def test_quote_all_and_cheapest(self):
        timer = ManualTimer()
        alpha = ScriptedAdapter.immediate("alpha", usd(329))
        beta = ScriptedAdapter.scripted("beta")
        gamma = ScriptedAdapter.scripted("gamma")
        delta = ScriptedAdapter.scripted("delta")
        all_quotes = FanOut.quote_all([alpha, beta, gamma, delta], REQUEST, 800, timer)
        self.assertTrue(alpha.calls() == 1 and beta.calls() == 1 and gamma.calls() == 1 and delta.calls() == 1)
        self.assertFalse(all_quotes.done())  # waits for the slowest
        timer.advance(250)
        beta.respond(usd(275))
        timer.advance(300)
        delta.fail("HTTP 503 from processor")
        self.assertFalse(all_quotes.done())
        timer.advance(250)
        self.assertTrue(all_quotes.done())
        quotes = all_quotes.result()
        self.assertEqual([q.processor for q in quotes], ["alpha", "beta", "gamma", "delta"])
        self.assertEqual([q.outcome for q in quotes], [Outcome.OK, Outcome.OK, Outcome.TIMED_OUT, Outcome.FAILED])
        cheapest = FanOut.cheapest(quotes)
        self.assertTrue(cheapest is not None and cheapest.processor == "beta")
        self.assertIsNone(FanOut.cheapest([quotes[2], quotes[3]]))
        self.assertEqual(FanOut.quote_all([], REQUEST, 800, timer).result(), [])


class FirstOkTest(unittest.TestCase):
    def test_first_ok(self):
        timer = ManualTimer()
        slow = ScriptedAdapter.scripted("slow")
        fast = ScriptedAdapter.scripted("fast")
        first = FanOut.first_ok([slow, fast], REQUEST, 800, timer)
        self.assertFalse(first.done())
        fast.respond(usd(350))
        self.assertTrue(first.done() and first.result().processor == "fast")
        slow.respond(usd(1))
        self.assertEqual(first.result().processor, "fast")  # a later, cheaper answer does not change the winner

        timer2 = ManualTimer()
        a = ScriptedAdapter.scripted("a")
        b = ScriptedAdapter.scripted("b")
        none = FanOut.first_ok([a, b], REQUEST, 800, timer2)
        a.fail("down")
        self.assertFalse(none.done())  # one failure is not a verdict
        timer2.advance(800)
        self.assertTrue(none.done() and none.result() is None)


class ExecutorModelsTest(unittest.TestCase):
    def test_pool_starvation_and_thread_per_task(self):
        with ThreadPoolExecutor(max_workers=2) as pool:
            self.assertEqual(count_started_before_release(pool, 3, 2), 2)
        with ThreadPoolExecutor(max_workers=4) as pool:
            self.assertEqual(count_started_before_release(pool, 3, 3), 3)
        with ThreadPerTaskExecutor() as per_task:
            self.assertEqual(count_started_before_release(per_task, 3, 3), 3)
            self.assertEqual(count_started_before_release(per_task, 200, 200), 200)

    def test_blocking_fan_out(self):
        with ThreadPerTaskExecutor() as per_task:
            alpha = ScriptedAdapter.immediate("alpha", usd(329))
            beta = ScriptedAdapter.immediate("beta", usd(301))
            quotes = BlockingFanOut.quote_all(per_task, [alpha, ThrowingAdapter(), beta], REQUEST)
            self.assertTrue(quotes[0].processor == "alpha" and quotes[2].processor == "beta")
            self.assertTrue(quotes[1].outcome is Outcome.FAILED and quotes[1].processor == "broken")
            self.assertEqual(FanOut.cheapest(quotes).processor, "beta")


class AsyncioContrastTest(unittest.TestCase):
    def test_wait_for_cancels_the_inner_task(self):
        """Unlike FanOut.with_timeout (and Java, and the abandoned gamma call above), asyncio.wait_for cancels."""
        timed_out, inner_cancelled = asyncio.run(wait_for_cancels_inner())
        self.assertTrue(timed_out)
        self.assertTrue(inner_cancelled)


if __name__ == "__main__":
    unittest.main()
