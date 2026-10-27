"""Tests for phases/12-payment-systems-design/09-limits-and-velocity-controls. Run: python3 -m unittest -v"""

import threading
import unittest

from main import (DAY, EUR, HOUR, MINUTE, SECOND, USD, Attempt, CurrencyMismatchError, FakeClock, Limit, LimitEngine,
                  LimitPolicy, LimitScope, Money, Outcome, Severity, SlidingWindow, standard_policy)

ACME = "merchant_acme"


def usd(id: str, merchant: str, card: str, amount: str) -> Attempt:
    return Attempt(id, merchant, card, Money.parse(amount, USD))


class SlidingWindowTest(unittest.TestCase):
    def test_half_open_interval(self) -> None:
        w = SlidingWindow(HOUR)
        w.add(0, 100)
        w.add(10 * SECOND, 200)
        self.assertEqual(w.count(30 * MINUTE), 2)
        self.assertEqual(w.sum_minor(30 * MINUTE), 300)
        self.assertEqual(w.count(HOUR), 1, "entry exactly W old has left the window")
        self.assertEqual(w.sum_minor(HOUR), 200)
        self.assertEqual(w.next_expiry_millis(HOUR), HOUR + 10 * SECOND)
        self.assertEqual(w.count(2 * HOUR), 0)
        self.assertIsNone(w.next_expiry_millis(2 * HOUR))
        back = SlidingWindow(HOUR)
        back.add(10, 1)
        with self.assertRaises(ValueError):
            back.add(5, 1)


class CountLimitTest(unittest.TestCase):
    def test_five_per_card_per_hour(self) -> None:
        clock = FakeClock(0)
        per_card = Limit.count("card-attempts-1h", LimitScope.CARD, HOUR, 5, Severity.HARD)
        engine = LimitEngine(LimitPolicy().add_default(per_card), clock)
        for i in range(1, 6):
            self.assertIs(engine.check(usd(f"a{i}", ACME, "fp_1", "1.00")).outcome, Outcome.ALLOW, f"attempt {i} of 5")
            clock.advance(MINUTE)
        sixth = engine.check(usd("a6", ACME, "fp_1", "1.00"))
        self.assertIs(sixth.outcome, Outcome.DENY)
        self.assertEqual(engine.window_count(per_card, usd("x", ACME, "fp_1", "1.00")), 5, "denied attempt is not recorded")
        self.assertEqual(len(sixth.reasons), 1)
        self.assertEqual(sixth.reasons[0].limit_id, "card-attempts-1h")
        self.assertIn("5 attempts -> 6 attempts exceeds 5 attempts in 3600s", sixth.explanations()[0])
        self.assertEqual(sixth.retry_after_seconds(), 55 * 60)
        self.assertIs(engine.check(usd("b1", ACME, "fp_2", "1.00")).outcome, Outcome.ALLOW, "a different card is a different window")
        clock.advance(55 * MINUTE)  # now = 1h after attempt 1
        self.assertIs(engine.check(usd("a7", ACME, "fp_1", "1.00")).outcome, Outcome.ALLOW, "one slot frees exactly at W")
        self.assertIs(engine.check(usd("a8", ACME, "fp_1", "1.00")).outcome, Outcome.DENY, "and only one slot")


class AmountLimitTest(unittest.TestCase):
    def test_soft_limit_flags(self) -> None:
        clock = FakeClock(0)
        soft = Limit.amount("card-amount-24h", LimitScope.CARD, DAY, Money.parse("500.00", USD), Severity.SOFT)
        engine = LimitEngine(LimitPolicy().add_default(soft), clock)
        self.assertIs(engine.check(usd("s1", ACME, "fp_1", "480.00")).outcome, Outcome.ALLOW)
        self.assertIs(engine.check(usd("s2", ACME, "fp_1", "20.00")).outcome, Outcome.ALLOW, "exactly reaching the limit is allowed")
        over = engine.check(usd("s3", ACME, "fp_1", "0.01"))
        self.assertIs(over.outcome, Outcome.REVIEW)
        self.assertTrue(over.allowed())
        self.assertEqual(engine.window_count(soft, usd("x", ACME, "fp_1", "1.00")), 3, "soft breach is recorded")
        self.assertIn("500.00 USD -> 500.01 USD exceeds 500.00 USD", over.explanations()[0])
        self.assertEqual(over.retry_after_seconds(), 0)
        self.assertIs(engine.check(Attempt("e1", ACME, "fp_1", Money.parse("9000.00", EUR))).outcome, Outcome.ALLOW,
                      "EUR attempt is not judged by a USD limit")


class ExposureTest(unittest.TestCase):
    def test_open_authorizations(self) -> None:
        clock = FakeClock(0)
        exposure = Limit.exposure("merchant-exposure", LimitScope.MERCHANT, Money.parse("2000.00", USD), Severity.HARD)
        engine = LimitEngine(LimitPolicy().add_default(exposure), clock)
        self.assertIs(engine.check(usd("x1", ACME, "fp_1", "1500.00")).outcome, Outcome.ALLOW)
        self.assertIs(engine.check(usd("x2", ACME, "fp_2", "600.00")).outcome, Outcome.DENY, "2100 open")
        clock.advance(30 * DAY)
        self.assertIs(engine.check(usd("x3", ACME, "fp_2", "600.00")).outcome, Outcome.DENY, "exposure does not expire with time")
        engine.release("x1")
        self.assertEqual(engine.open_exposure(exposure, usd("q", ACME, "fp_1", "1.00")).minor_units, 0)
        self.assertIs(engine.check(usd("x4", ACME, "fp_2", "600.00")).outcome, Outcome.ALLOW)
        engine.release("x1")
        engine.release("never-seen")
        self.assertEqual(engine.open_exposure(exposure, usd("q", ACME, "fp_1", "1.00")).minor_units, 60000, "release is idempotent")
        self.assertIs(engine.check(usd("y1", "merchant_other", "fp_9", "1999.99")).outcome, Outcome.ALLOW)


class HierarchyTest(unittest.TestCase):
    def test_overrides_replace_by_id(self) -> None:
        policy = (LimitPolicy()
                  .add_default(Limit.count("card-attempts-1h", LimitScope.CARD, HOUR, 5, Severity.HARD))
                  .add_default(Limit.amount("merchant-amount-24h", LimitScope.MERCHANT, DAY, Money.parse("10000.00", USD), Severity.HARD))
                  .override("merchant_bigbox", Limit.amount("merchant-amount-24h", LimitScope.MERCHANT, DAY,
                                                            Money.parse("250000.00", USD), Severity.HARD)))
        acme, bigbox = policy.limits_for(ACME), policy.limits_for("merchant_bigbox")
        self.assertEqual(acme[1].max_amount, Money.parse("10000.00", USD))
        self.assertEqual(bigbox[1].max_amount, Money.parse("250000.00", USD))
        self.assertEqual(bigbox[0], acme[0], "override keeps the card limit untouched")
        self.assertEqual(len(acme), 2)
        self.assertEqual(acme[0].id, "card-attempts-1h", "defaults' declaration order")
        with self.assertRaises(ValueError):
            policy.override(ACME, Limit.count("made-up", LimitScope.CARD, HOUR, 1, Severity.HARD))
        with self.assertRaises(ValueError):
            policy.add_default(Limit.count("card-attempts-1h", LimitScope.CARD, HOUR, 1, Severity.HARD))
        engine = LimitEngine(policy, FakeClock(0))
        self.assertIs(engine.check(usd("h1", "merchant_bigbox", "fp_1", "15000.00")).outcome, Outcome.ALLOW)
        self.assertIs(engine.check(usd("h2", ACME, "fp_1", "15000.00")).outcome, Outcome.DENY)


class EveryBreachReportedTest(unittest.TestCase):
    def test_hard_wins_over_soft(self) -> None:
        engine = LimitEngine(standard_policy(), FakeClock(0))
        d = engine.check(usd("m1", ACME, "fp_1", "15000.00"))
        self.assertIs(d.outcome, Outcome.DENY)
        self.assertEqual(len(d.reasons), 3)
        self.assertTrue(any(r.severity is Severity.SOFT for r in d.reasons))
        self.assertTrue(any(r.severity is Severity.HARD for r in d.reasons))
        self.assertEqual(engine.window_count(standard_policy().limits_for(ACME)[0], usd("x", ACME, "fp_1", "1.00")), 0,
                         "denied attempt recorded nowhere")


class ConstructionGuardsTest(unittest.TestCase):
    def test_guards(self) -> None:
        with self.assertRaises(ValueError):
            Limit.count("c", LimitScope.CARD, HOUR, 0, Severity.HARD)
        with self.assertRaises(ValueError):
            Limit.amount("a", LimitScope.CARD, HOUR, Money.zero(USD), Severity.HARD)
        with self.assertRaises(ValueError):
            usd("z", ACME, "fp", "0.00")
        with self.assertRaises(CurrencyMismatchError):
            Money.parse("1.00", USD).plus(Money.parse("1.00", EUR))


class ConcurrencyTest(unittest.TestCase):
    def test_check_and_record_is_atomic(self) -> None:
        engine = LimitEngine(LimitPolicy().add_default(Limit.count("card-attempts-1h", LimitScope.CARD, HOUR, 5, Severity.HARD)),
                             FakeClock(0))
        start = threading.Event()
        allowed: list[int] = []
        tally = threading.Lock()

        def racer(n: int) -> None:
            start.wait()
            if engine.check(usd(f"r{n}", ACME, "fp_race", "1.00")).allowed():
                with tally:
                    allowed.append(n)

        threads = [threading.Thread(target=racer, args=(i,)) for i in range(32)]
        for t in threads:
            t.start()
        start.set()
        for t in threads:
            t.join()
        self.assertEqual(len(allowed), 5, "exactly 5 of 32 concurrent attempts are admitted")


if __name__ == "__main__":
    unittest.main()
