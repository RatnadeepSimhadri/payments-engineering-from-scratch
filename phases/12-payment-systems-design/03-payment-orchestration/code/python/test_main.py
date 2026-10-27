"""Tests for phases/12-payment-systems-design/03-payment-orchestration. Run: python3 -m unittest -v"""

import unittest

from main import (USD, FakeClock, FakeProcessor, Fault, Ledger, Money, Orchestrator, ProcessCrashError,
                  SagaRecord, SagaState, SagaStep, SagaStore, StepStatus)


class World:
    """One fresh world per scenario: store, two processors, ledger, orchestrator with a budget of 3."""

    def __init__(self) -> None:
        self.clock = FakeClock(1_000)
        self.store = SagaStore()
        self.vault = FakeProcessor("vault")
        self.acquirer = FakeProcessor("acquirer")
        self.ledger = Ledger()
        self.orch = Orchestrator(self.store, self.vault, self.acquirer, self.ledger, self.clock, 3)

    def start(self, saga_id: str) -> SagaRecord:
        return self.orch.start(saga_id, "pay_" + saga_id, Money.of_minor(4999, USD), "4242424242424242")


def all_done(r: SagaRecord) -> bool:
    return all(r.steps[s] is StepStatus.DONE for s in SagaStep)


def nothing_left_done(r: SagaRecord) -> bool:
    return all(r.steps[s] is not StepStatus.DONE for s in SagaStep)


class HappyPathTest(unittest.TestCase):
    def test_happy_path(self) -> None:
        w = World()
        r = w.start("s1")
        self.assertIs(r.state, SagaState.COMPLETED)
        self.assertTrue(all_done(r))
        self.assertTrue(r.references[SagaStep.TOKENIZE].startswith("vault_tokenize"))
        self.assertTrue(r.references[SagaStep.AUTHORIZE].startswith("acquirer_authorize"))
        self.assertTrue(r.references[SagaStep.CAPTURE].startswith("acquirer_capture"))
        self.assertTrue(r.references[SagaStep.LEDGER_POST].startswith("le_"))
        self.assertEqual(w.ledger.net_merchant_payable("pay_s1", USD), Money.of_minor(4999, USD))
        self.assertEqual(len(w.ledger.entries()), 1)
        self.assertEqual((w.vault.side_effects("tokenize"), w.acquirer.side_effects("authorize"),
                          w.acquirer.side_effects("capture")), (1, 1, 1))
        self.assertEqual(w.store.load("s1").version, 6, "persisted once per step plus start and completion")
        self.assertIs(w.orch.run("s1").state, SagaState.COMPLETED)
        self.assertEqual(w.acquirer.side_effects("capture"), 1)
        self.assertEqual(w.acquirer.replays(), 0)


class CompensationTest(unittest.TestCase):
    def test_decline_at_capture_compensates_in_reverse(self) -> None:
        w = World()
        w.acquirer.fail_next("capture", Fault.DECLINE)
        r = w.start("s2")
        self.assertIs(r.state, SagaState.COMPENSATED)
        self.assertTrue(nothing_left_done(r))
        self.assertEqual(r.failure_reason, "CAPTURE declined with code auth_expired")
        self.assertLess(r.journal.index("AUTHORIZE compensated via voidAuth"),
                        r.journal.index("TOKENIZE compensated via deleteToken"))
        self.assertEqual(w.acquirer.side_effects("voidAuth"), 1)
        self.assertEqual(w.vault.side_effects("deleteToken"), 1)
        self.assertEqual(w.acquirer.side_effects("refund"), 0)
        self.assertIs(r.steps[SagaStep.CAPTURE], StepStatus.PENDING)
        self.assertIs(r.steps[SagaStep.LEDGER_POST], StepStatus.PENDING)
        self.assertEqual(len(w.ledger.entries()), 0)

    def test_decline_at_first_step_compensates_nothing(self) -> None:
        w = World()
        w.vault.fail_next("tokenize", Fault.DECLINE)
        r = w.start("s3")
        self.assertIs(r.state, SagaState.COMPENSATED)
        self.assertEqual(w.acquirer.side_effects("authorize"), 0)
        self.assertEqual(w.vault.side_effects("deleteToken"), 0)

    def test_ledger_outage_refunds_the_capture(self) -> None:
        w = World()
        w.ledger.fail_next(Fault.OUTAGE).fail_next(Fault.OUTAGE).fail_next(Fault.OUTAGE)
        r = w.start("s4")
        self.assertIs(r.state, SagaState.COMPENSATED)
        self.assertEqual(w.acquirer.side_effects("refund"), 1)
        self.assertEqual(w.acquirer.side_effects("voidAuth"), 0, "a captured authorization is not voided")
        self.assertIs(r.steps[SagaStep.AUTHORIZE], StepStatus.COMPENSATED)
        self.assertTrue(w.ledger.net_merchant_payable("pay_s4", USD).is_zero())


class RetryBudgetTest(unittest.TestCase):
    def test_two_outages_then_success(self) -> None:
        w = World()
        w.acquirer.fail_next("authorize", Fault.OUTAGE).fail_next("authorize", Fault.OUTAGE)
        r = w.start("s5")
        self.assertIs(r.state, SagaState.COMPLETED)
        self.assertEqual(r.forward_attempts[SagaStep.AUTHORIZE], 3)
        self.assertEqual(w.acquirer.side_effects("authorize"), 1)

    def test_three_outages_exhaust_the_budget(self) -> None:
        w = World()
        w.acquirer.fail_next("authorize", Fault.OUTAGE).fail_next("authorize", Fault.OUTAGE).fail_next("authorize", Fault.OUTAGE)
        r = w.start("s6")
        self.assertIs(r.state, SagaState.COMPENSATED)
        self.assertEqual(r.forward_attempts[SagaStep.AUTHORIZE], 3)
        self.assertEqual(w.vault.side_effects("deleteToken"), 1)
        self.assertEqual(w.acquirer.side_effects("voidAuth"), 0)


class CrashRecoveryTest(unittest.TestCase):
    def test_crash_after_authorize_commit(self) -> None:
        w = World()
        w.acquirer.fail_next("authorize", Fault.CRASH_AFTER_COMMIT)
        with self.assertRaises(ProcessCrashError):
            w.start("s7")
        persisted = w.store.load("s7")
        self.assertIs(persisted.state, SagaState.RUNNING)
        self.assertIs(persisted.steps[SagaStep.AUTHORIZE], StepStatus.PENDING)
        self.assertIs(persisted.steps[SagaStep.TOKENIZE], StepStatus.DONE)
        self.assertEqual(w.acquirer.side_effects("authorize"), 1)
        restarted = Orchestrator(w.store, w.vault, w.acquirer, w.ledger, w.clock, 3)
        self.assertEqual(restarted.recover(), 1)
        r = w.store.load("s7")
        self.assertIs(r.state, SagaState.COMPLETED)
        self.assertTrue(all_done(r))
        self.assertEqual(w.acquirer.side_effects("authorize"), 1)
        self.assertEqual(w.acquirer.replays(), 1)
        self.assertEqual(r.references[SagaStep.AUTHORIZE], "acquirer_authorize_0001")
        self.assertEqual(restarted.recover(), 0)

    def test_crash_after_ledger_commit(self) -> None:
        w = World()
        w.ledger.fail_next(Fault.CRASH_AFTER_COMMIT)
        with self.assertRaises(ProcessCrashError):
            w.start("s8")
        Orchestrator(w.store, w.vault, w.acquirer, w.ledger, w.clock, 3).recover()
        self.assertEqual(len(w.ledger.entries()), 1)
        self.assertIs(w.store.load("s8").state, SagaState.COMPLETED)

    def test_crash_during_compensation(self) -> None:
        w = World()
        w.acquirer.fail_next("capture", Fault.DECLINE).fail_next("voidAuth", Fault.CRASH_AFTER_COMMIT)
        with self.assertRaises(ProcessCrashError):
            w.start("s9")
        self.assertIs(w.store.load("s9").state, SagaState.COMPENSATING)
        self.assertIs(w.store.load("s9").steps[SagaStep.AUTHORIZE], StepStatus.DONE)
        Orchestrator(w.store, w.vault, w.acquirer, w.ledger, w.clock, 3).recover()
        self.assertIs(w.store.load("s9").state, SagaState.COMPENSATED)
        self.assertEqual(w.acquirer.side_effects("voidAuth"), 1)


class IdempotencyKeyTest(unittest.TestCase):
    def test_keys(self) -> None:
        r = SagaRecord("sx", "pay_x", Money.of_minor(100, USD), "4242")
        self.assertNotEqual(Orchestrator.key(r, SagaStep.AUTHORIZE, False), Orchestrator.key(r, SagaStep.AUTHORIZE, True))
        self.assertEqual(Orchestrator.key(r, SagaStep.CAPTURE, False), "sx:capture:fwd")
        with self.assertRaises(ValueError):
            SagaRecord("sy", "pay_y", Money.zero(USD), "4242")


class DeadLetterTest(unittest.TestCase):
    def test_outage_past_budget_dead_letters_then_redrive(self) -> None:
        w = World()
        w.acquirer.fail_next("capture", Fault.DECLINE)
        for _ in range(3):
            w.acquirer.fail_next("voidAuth", Fault.OUTAGE)
        r = w.start("s10")
        self.assertIs(r.state, SagaState.DEAD_LETTERED)
        self.assertEqual(r.compensation_attempts[SagaStep.AUTHORIZE], 3)
        self.assertEqual(len(w.orch.dead_letters()), 1)
        self.assertIs(w.orch.dead_letters()[0].step, SagaStep.AUTHORIZE)
        self.assertEqual(w.orch.dead_letters()[0].saga_id, "s10")
        self.assertIs(r.steps[SagaStep.AUTHORIZE], StepStatus.DONE)
        self.assertIs(r.steps[SagaStep.TOKENIZE], StepStatus.DONE)
        self.assertEqual(w.orch.recover(), 0, "recover does not touch dead letters")
        redriven = w.orch.redrive("s10")
        self.assertIs(redriven.state, SagaState.COMPENSATED)
        self.assertEqual(w.acquirer.side_effects("voidAuth"), 1)
        self.assertEqual(len(w.orch.dead_letters()), 0)
        with self.assertRaises(RuntimeError):
            w.orch.redrive("s10")

    def test_refused_compensation_dead_letters_immediately(self) -> None:
        w = World()
        w.acquirer.fail_next("capture", Fault.DECLINE).fail_next("voidAuth", Fault.DECLINE)
        r = w.start("s11")
        self.assertIs(r.state, SagaState.DEAD_LETTERED)
        self.assertIn("already_captured", r.journal[-1])


class StoreTest(unittest.TestCase):
    def test_store_models_persistence(self) -> None:
        store = SagaStore()
        r = SagaRecord("s12", "pay_12", Money.of_minor(100, USD), "4242")
        store.save(r)
        r.mark_done(SagaStep.TOKENIZE, "tok")
        self.assertIs(store.load("s12").steps[SagaStep.TOKENIZE], StepStatus.PENDING)
        with self.assertRaises(RuntimeError):
            store.load("nope")


if __name__ == "__main__":
    unittest.main()
