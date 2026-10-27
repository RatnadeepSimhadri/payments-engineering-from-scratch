"""Tests for phases/12-payment-systems-design/14-testing-payment-systems. Run: python3 -m unittest -v"""

import unittest

from main import (NO_BUGS, USD, Account, Bugs, Command, CommandKind, FakeProcessor, Gens, IllegalStateError, Invariants,
                  JavaRandom, Ledger, Money, PaymentService, PaymentState, ProcessorBehaviour, Property, Scenario,
                  ScenarioRunner)


def usd(minor: int) -> Money:
    return Money.of_minor(minor, USD)


def cmd(k: CommandKind, amount: int, b: ProcessorBehaviour = ProcessorBehaviour.APPROVE) -> Command:
    return Command(k, amount, b)


class MoneyAndLedgerTest(unittest.TestCase):
    def test_fee_rounding_is_half_even(self) -> None:
        self.assertEqual(usd(10_000).basis_points(290).minor_units, 290)
        self.assertEqual(usd(18).basis_points(290).minor_units, 1, "0.522 rounds up")
        self.assertEqual(usd(17).basis_points(290).minor_units, 0, "0.493 rounds down")
        self.assertEqual(usd(125).basis_points(1000).minor_units, 12, "tie 12.5 -> 12")
        self.assertEqual(usd(135).basis_points(1000).minor_units, 14, "tie 13.5 -> 14")
        self.assertEqual(usd(-125).basis_points(1000).minor_units, -12, "negative tie truncates toward zero like Java")
        self.assertEqual(str(usd(-37_50)), "-37.50 USD")

    def test_double_entry_conserves_money(self) -> None:
        ledger = Ledger(USD)
        ledger.post(Account.CUSTOMER, Account.MERCHANT, usd(1000), "capture")
        self.assertEqual(ledger.balance(Account.CUSTOMER).minor_units, -1000)
        self.assertEqual(ledger.balance(Account.MERCHANT).minor_units, 1000)
        self.assertTrue(ledger.total().is_zero())
        with self.assertRaises(ValueError):
            ledger.post(Account.CUSTOMER, Account.MERCHANT, usd(-1), "x")


class PaymentServiceTest(unittest.TestCase):
    def test_happy_path_and_guards(self) -> None:
        proc = FakeProcessor(USD)
        svc = PaymentService(USD, proc, NO_BUGS)
        self.assertTrue(svc.authorize(usd(5000)))
        self.assertIs(svc.state, PaymentState.AUTHORIZED)
        self.assertTrue(svc.capture(usd(2000)))
        self.assertIs(svc.state, PaymentState.PARTIALLY_CAPTURED)
        with self.assertRaises(ValueError):
            svc.capture(usd(3001))
        self.assertTrue(svc.capture(usd(3000)))
        self.assertIs(svc.state, PaymentState.CAPTURED)
        with self.assertRaises(ValueError):
            svc.refund(usd(5001))
        self.assertTrue(svc.refund(usd(1250)))
        self.assertEqual(svc.refunded.minor_units, 1250)
        with self.assertRaises(IllegalStateError):
            svc.void_authorization()
        with self.assertRaises(ValueError):
            svc.refund(usd(0))
        self.assertEqual(svc.ledger.balance(Account.PLATFORM_FEES).minor_units, 58 + 87)
        self.assertIsNone(Invariants.check(svc))
        self.assertEqual(svc.ledger.balance(Account.CUSTOMER).minor_units, -(5000 - 1250))

    def test_processor_behaviours(self) -> None:
        declining = FakeProcessor(USD).script(ProcessorBehaviour.DECLINE)
        s2 = PaymentService(USD, declining, NO_BUGS)
        self.assertFalse(s2.authorize(usd(100)))
        self.assertIs(s2.state, PaymentState.NEW)
        self.assertTrue(s2.authorize(usd(100)), "script exhausted means approve")
        self.assertEqual(declining.calls, 2)
        erroring = FakeProcessor(USD).script(ProcessorBehaviour.APPROVE, ProcessorBehaviour.ERROR)
        s3 = PaymentService(USD, erroring, NO_BUGS)
        s3.authorize(usd(100))
        with self.assertRaises(IllegalStateError):
            s3.capture(usd(100))
        self.assertTrue(s3.captured.is_zero())
        self.assertTrue(s3.ledger.total().is_zero())

    def test_timeouts_wait_for_reconcile(self) -> None:
        lost = FakeProcessor(USD).script(ProcessorBehaviour.APPROVE, ProcessorBehaviour.TIMEOUT_AFTER_APPLY)
        s4 = PaymentService(USD, lost, NO_BUGS)
        s4.authorize(usd(5000))
        self.assertFalse(s4.capture(usd(5000)))
        self.assertIs(s4.state, PaymentState.UNKNOWN)
        self.assertTrue(s4.captured.is_zero())
        self.assertEqual(s4.ledger.postings, [], "nothing posted on timeout")
        self.assertEqual(lost.record.captured, usd(5000), "processor did apply it")
        with self.assertRaises(IllegalStateError):
            s4.capture(usd(1))
        s4.reconcile()
        self.assertEqual(s4.captured, usd(5000))
        self.assertIs(s4.state, PaymentState.CAPTURED)
        self.assertIsNone(Invariants.check(s4))
        never = FakeProcessor(USD).script(ProcessorBehaviour.APPROVE, ProcessorBehaviour.TIMEOUT_BEFORE_APPLY)
        s5 = PaymentService(USD, never, NO_BUGS)
        s5.authorize(usd(5000))
        s5.capture(usd(5000))
        s5.reconcile()
        self.assertIs(s5.state, PaymentState.AUTHORIZED)
        self.assertTrue(s5.captured.is_zero())
        s5.reconcile()
        self.assertIsNone(Invariants.check(s5), "reconcile is a no-op when not UNKNOWN")


class InvariantsTest(unittest.TestCase):
    def test_each_injected_bug_is_caught(self) -> None:
        oc = ScenarioRunner.run(Scenario("oc", (cmd(CommandKind.AUTHORIZE, 1), cmd(CommandKind.CAPTURE, 2))), Bugs(True, False, False, False))
        self.assertTrue(oc.violation is not None and oc.violation.startswith("captured"))
        orf = ScenarioRunner.run(Scenario("or", (cmd(CommandKind.AUTHORIZE, 1), cmd(CommandKind.CAPTURE, 1), cmd(CommandKind.REFUND, 2))),
                                 Bugs(False, True, False, False))
        self.assertTrue(orf.violation is not None and orf.violation.startswith("refunded"))
        uf = ScenarioRunner.run(Scenario("uf", (cmd(CommandKind.AUTHORIZE, 18), cmd(CommandKind.CAPTURE, 18))), Bugs(False, False, True, False))
        self.assertTrue(uf.violation is not None and uf.violation.startswith("money not conserved"))
        pt = ScenarioRunner.run(Scenario("pt", (cmd(CommandKind.AUTHORIZE, 1), cmd(CommandKind.CAPTURE, 1, ProcessorBehaviour.TIMEOUT_BEFORE_APPLY),
                                                cmd(CommandKind.RECONCILE, 0))), Bugs(False, False, False, True))
        self.assertTrue(pt.violation is not None and "!= processor captured" in pt.violation)
        self.assertTrue(ScenarioRunner.run(Scenario("ok", (cmd(CommandKind.AUTHORIZE, 18), cmd(CommandKind.CAPTURE, 18))), NO_BUGS).passed())
        self.assertTrue(ScenarioRunner.run(Scenario("skip", (cmd(CommandKind.REFUND, 5), cmd(CommandKind.VOID, 0))), NO_BUGS).passed(),
                        "illegal commands are skipped, not failures")


class GeneratorTest(unittest.TestCase):
    def test_generators_and_shrinkers(self) -> None:
        rng = JavaRandom(1)
        longs = Gens.longs(1, 100)
        self.assertTrue(all(1 <= longs.generate(rng) <= 100 for _ in range(1000)))
        self.assertEqual(longs.shrink(100)[0], 1)
        self.assertIn(99, longs.shrink(100))
        self.assertEqual(longs.shrink(1), [])
        self.assertEqual(Gens.one_of(["a", "b", "c"]).shrink("c"), ["a", "b"])
        lists = Gens.lists(longs, 5)
        sample = lists.generate(JavaRandom(3))
        self.assertTrue(1 <= len(sample) <= 5)
        shrunk = lists.shrink([10, 20, 30, 40])
        self.assertEqual(shrunk[0], [30, 40])
        self.assertEqual(shrunk[1], [10, 20])
        self.assertTrue(all(len(c) > 0 for c in shrunk), "list shrink never yields empty")
        self.assertEqual(lists.generate(JavaRandom(9)), lists.generate(JavaRandom(9)), "same seed, same sequence")

    def test_java_random_matches_the_jdk(self) -> None:
        r = JavaRandom(42)
        self.assertEqual([r.next_int(100) for _ in range(3)], [30, 63, 48])
        self.assertAlmostEqual(r.next_double(), 0.047939305137387644, places=15)


class PropertyHarnessTest(unittest.TestCase):
    def test_harness(self) -> None:
        ok = Property.for_all("n >= 1", Gens.longs(1, 1000), lambda n: None if n >= 1 else "negative", 5, 200)
        self.assertTrue(ok.passed)
        self.assertEqual(ok.trials_run, 200)
        self.assertIsNone(ok.counterexample)
        big = Property.for_all("n < 500", Gens.longs(1, 100_000), lambda n: None if n < 500 else f"n={n}", 5, 200)
        self.assertFalse(big.passed)
        assert big.counterexample is not None
        self.assertEqual(big.counterexample.shrunk, 500, "failing property shrinks to boundary")
        self.assertGreaterEqual(big.counterexample.original, 500)

        def boom(n: int) -> str:
            raise ArithmeticError("boom")

        crash = Property.for_all("crash", Gens.longs(1, 10), boom, 5, 10)
        self.assertFalse(crash.passed)
        assert crash.counterexample is not None
        self.assertTrue(crash.counterexample.reason.startswith("crash"))

        def not_accepted(n: int) -> str:
            raise ValueError("not accepted")

        self.assertTrue(Property.for_all("rejects", Gens.longs(1, 10), not_accepted, 5, 10).passed,
                        "ValueError counts as rejected input, not failure")


class HeadlinePropertyTest(unittest.TestCase):
    def test_invariants_hold_without_bugs(self) -> None:
        self.assertTrue(ScenarioRunner.invariants_hold(NO_BUGS, 42, 300, 12).passed)
        self.assertTrue(ScenarioRunner.invariants_hold(NO_BUGS, 2024, 300, 12).passed)

    def test_bugs_are_found_and_shrunk(self) -> None:
        oc = ScenarioRunner.invariants_hold(Bugs(True, False, False, False), 42, 500, 12)
        self.assertFalse(oc.passed)
        assert oc.counterexample is not None
        self.assertEqual(len(oc.counterexample.shrunk), 2, "over-capture shrunk to two commands")
        self.assertEqual(oc.counterexample.shrunk[0].amount_minor, 1)
        self.assertEqual(oc.counterexample.shrunk[1].amount_minor, 2)
        self.assertEqual(oc.trials_run, 12, "same seed finds the same trial as the Java run")
        pt = ScenarioRunner.invariants_hold(Bugs(False, False, False, True), 42, 2000, 12)
        self.assertFalse(pt.passed)
        assert pt.counterexample is not None
        self.assertEqual(len(pt.counterexample.shrunk), 3, "timeout bug found with enough trials")
        replay = ScenarioRunner.run(Scenario("replay", tuple(pt.counterexample.shrunk)), Bugs(False, False, False, True))
        self.assertIsNotNone(replay.violation, "counterexample is reproducible")


if __name__ == "__main__":
    unittest.main()
