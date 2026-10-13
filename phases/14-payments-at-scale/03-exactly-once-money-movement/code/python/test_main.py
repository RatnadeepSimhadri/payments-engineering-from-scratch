"""Tests for phases/14-payments-at-scale/03-exactly-once-money-movement. Run: python3 -m unittest -v
Same test cases as MainTest.java and main_test.go: every fault scenario leaves exactly one posting and conserved money."""
import unittest

from main import (
    DOWNSTREAM_CLEARING,
    GATEWAY_CLEARING,
    PAYABLE,
    RECEIVABLE,
    USD,
    EUR,
    Bus,
    CrashError,
    CurrencyMismatchError,
    Defences,
    FaultInjector,
    FaultPoint,
    GatewayStore,
    HarnessConfig,
    IdempotencyConflictError,
    JournalEntry,
    Ledger,
    LedgerConsumer,
    LedgerStore,
    Money,
    OutboxRelay,
    PaymentApi,
    PaymentRequest,
    run_harness,
)


def request(key: str, cents: int) -> PaymentRequest:
    return PaymentRequest(key, "cust-1", "merch-1", Money.of_minor(cents, USD))


class Pipeline:
    """A full pipeline whose only fault is the given point, firing every time until disarmed."""

    def __init__(self, point: FaultPoint | None, defences: Defences = Defences.all()) -> None:
        self.faults = FaultInjector(1, {} if point is None else {point: 1.0})
        self.gateway = GatewayStore()
        self.api = PaymentApi(self.gateway, self.faults, defences)
        self.bus = Bus(self.faults)
        self.relay = OutboxRelay(self.gateway, self.bus, self.faults)
        self.downstream = LedgerStore()
        self.consumer = LedgerConsumer(self.downstream, self.faults, defences)

    def drain_and_count(self, event_id: str) -> int:
        """Drain relay + bus with faults off, then return how many times the one event was posted downstream."""
        self.faults.disarm()
        while self.gateway.unpublished() or self.bus.pending() > 0:
            self.relay.relay_once()
            self.bus.deliver(self.consumer.handle, 64)
        return self.downstream.ledger.count_entries(event_id)


class MoneyAndLedgerTest(unittest.TestCase):
    def test_money_plus_and_str(self):
        self.assertEqual(Money.of_minor(1999, USD).plus(Money.of_minor(1, USD)).minor_units, 2000)
        self.assertEqual(str(Money.of_minor(1999, USD)), "19.99 USD")

    def test_money_currency_mismatch(self):
        with self.assertRaises(CurrencyMismatchError):
            Money.of_minor(1, USD).plus(Money.of_minor(1, EUR))

    def test_ledger_balances_and_trial_balance(self):
        ledger = Ledger(USD)
        ledger.post(JournalEntry.transfer("e1", "asset:a", "liability:b", Money.of_minor(500, USD)))
        self.assertTrue(ledger.trial_balance().is_zero())
        self.assertEqual(ledger.balance("asset:a").minor_units, 500)
        self.assertEqual(ledger.balance("liability:b").minor_units, -500)
        self.assertEqual(ledger.credit_total("liability:").minor_units, 500)

    def test_ledger_rejects_non_positive_transfer(self):
        with self.assertRaises(ValueError):
            JournalEntry.transfer("e2", "a", "b", Money.zero(USD))

    def test_ledger_itself_does_not_dedupe(self):
        ledger = Ledger(USD)
        for _ in range(2):
            ledger.post(JournalEntry.transfer("e1", "asset:a", "liability:b", Money.of_minor(500, USD)))
        self.assertEqual(ledger.count_entries("e1"), 2)


class IdempotentApiTest(unittest.TestCase):
    def setUp(self):
        self.store = GatewayStore()
        self.api = PaymentApi(self.store, FaultInjector.none(), Defences.all())

    def test_same_key_replays_same_payment(self):
        first = self.api.create_payment(request("k1", 1000))
        again = self.api.create_payment(request("k1", 1000))
        self.assertFalse(first.replayed)
        self.assertTrue(again.replayed)
        self.assertEqual(again.payment.payment_id, first.payment.payment_id)
        self.assertEqual((len(self.store.payments), len(self.store.idempotency_keys), len(self.store.outbox),
                          len(self.store.ledger.journal)), (1, 1, 1, 1))

    def test_gateway_ledger_debits_receivable_credits_clearing(self):
        self.api.create_payment(request("k1", 1000))
        self.assertEqual(self.store.ledger.balance(RECEIVABLE + "cust-1").minor_units, 1000)
        self.assertEqual(self.store.ledger.balance(GATEWAY_CLEARING).minor_units, -1000)

    def test_same_key_different_amount_is_a_conflict_and_writes_nothing(self):
        self.api.create_payment(request("k1", 1000))
        with self.assertRaises(IdempotencyConflictError):
            self.api.create_payment(request("k1", 1001))
        self.assertEqual(len(self.store.payments), 1)
        self.assertEqual(self.store.commits, 1)


class FaultScenarioTest(unittest.TestCase):
    def test_crash_before_commit_writes_nothing(self):
        p = Pipeline(FaultPoint.API_BEFORE_COMMIT)
        with self.assertRaises(CrashError):
            p.api.create_payment(request("k1", 1000))
        self.assertEqual(len(p.gateway.payments), 0)
        self.assertEqual(len(p.gateway.outbox), 0)
        self.assertEqual(len(p.gateway.ledger.journal), 0)

    def test_crash_after_commit_then_retry_posts_once(self):
        q = Pipeline(FaultPoint.API_AFTER_COMMIT)
        with self.assertRaises(CrashError):
            q.api.create_payment(request("k1", 1000))
        self.assertEqual((len(q.gateway.payments), len(q.gateway.outbox), len(q.gateway.ledger.journal)), (1, 1, 1))
        q.faults.disarm()
        retried = q.api.create_payment(request("k1", 1000))
        self.assertTrue(retried.replayed)
        self.assertEqual(len(q.gateway.payments), 1)
        self.assertEqual(q.drain_and_count(f"evt-{retried.payment.payment_id}"), 1)

    def test_relay_crash_after_publish_duplicates_on_bus_and_consumer_absorbs(self):
        r = Pipeline(FaultPoint.RELAY_AFTER_PUBLISH)
        rp = r.api.create_payment(request("k1", 1000))
        with self.assertRaises(CrashError):
            r.relay.relay_once()
        self.assertEqual(r.bus.pending(), 1)
        self.assertEqual(len(r.gateway.unpublished()), 1)
        self.assertEqual(r.drain_and_count(f"evt-{rp.payment.payment_id}"), 1)
        self.assertEqual(r.consumer.skipped_duplicates, 1)
        self.assertEqual(r.bus.published, 2)

    def test_bus_duplicate_delivery_posts_once(self):
        d = Pipeline(FaultPoint.BUS_DUPLICATE)
        dp = d.api.create_payment(request("k1", 1000))
        d.relay.relay_once()
        self.assertEqual(d.bus.pending(), 2)
        self.assertEqual(d.bus.duplicated, 1)
        self.assertEqual(d.drain_and_count(f"evt-{dp.payment.payment_id}"), 1)

    def test_consumer_crash_before_commit_is_redelivered_and_posts_once(self):
        cb = Pipeline(FaultPoint.CONSUMER_BEFORE_COMMIT)
        cbp = cb.api.create_payment(request("k1", 1000))
        cb.relay.relay_once()
        with self.assertRaises(CrashError):
            cb.bus.deliver(cb.consumer.handle, 8)
        self.assertEqual(cb.bus.pending(), 1)
        self.assertEqual(cb.bus.redelivered, 1)
        self.assertEqual(len(cb.downstream.ledger.journal), 0)
        self.assertEqual(cb.drain_and_count(f"evt-{cbp.payment.payment_id}"), 1)

    def test_consumer_crash_after_commit_lost_ack_posts_once(self):
        ca = Pipeline(FaultPoint.CONSUMER_AFTER_COMMIT)
        cap = ca.api.create_payment(request("k1", 1000))
        ca.relay.relay_once()
        with self.assertRaises(CrashError):
            ca.bus.deliver(ca.consumer.handle, 8)
        self.assertEqual(len(ca.downstream.ledger.journal), 1)
        self.assertEqual(ca.bus.pending(), 1)
        self.assertEqual(ca.drain_and_count(f"evt-{cap.payment.payment_id}"), 1)

    def test_reordered_batch_posts_every_event_once(self):
        ro = Pipeline(FaultPoint.BUS_REORDER)
        for i in range(5):
            ro.api.create_payment(request(f"k{i}", 100 + i))
        ro.relay.relay_once()
        ro.bus.deliver(ro.consumer.handle, 8)
        self.assertEqual(ro.bus.reordered_batches, 1)
        self.assertEqual(len(ro.downstream.ledger.journal), 5)
        self.assertEqual(ro.downstream.ledger.credit_total(PAYABLE).minor_units, 510)
        self.assertEqual(ro.downstream.ledger.balance(DOWNSTREAM_CLEARING).minor_units, 510)


class HarnessTest(unittest.TestCase):
    def test_uniform_fault_schedule_holds_every_invariant_across_seeds(self):
        crashes = duplicates = 0
        for seed in range(1, 13):
            report = run_harness(HarnessConfig.standard(seed, 1500))
            self.assertEqual(report.violations, [], f"seed {seed}: {report.violations[:3]}")
            crashes += report.crashes
            duplicates += report.bus_duplicated
        self.assertGreater(crashes, 100)
        self.assertGreater(duplicates, 10)

    def test_random_fault_schedules_hold_every_invariant(self):
        for seed in range(100, 120):
            report = run_harness(HarnessConfig.random_schedule(seed, 1000))
            self.assertTrue(report.passed(), f"seed {seed}: {report.violations[:3]}")

    def test_report_counts_and_trial_balances(self):
        one = run_harness(HarnessConfig.standard(5, 1500))
        self.assertTrue(one.gateway_trial_balance.is_zero())
        self.assertTrue(one.downstream_trial_balance.is_zero())
        self.assertEqual(one.gateway_entries, one.accepted_payments)
        self.assertEqual(one.downstream_entries, one.accepted_payments)

    def test_same_seed_reproduces_the_same_run(self):
        self.assertEqual(run_harness(HarnessConfig.standard(5, 1500)).summary(),
                         run_harness(HarnessConfig.standard(5, 1500)).summary())

    def test_without_api_dedupe_breaks_one_payment_per_key(self):
        report = run_harness(HarnessConfig.standard(1, 1500).without("api_dedupe"))
        self.assertFalse(report.passed())
        self.assertEqual(report.first_broken_invariant(), "one-payment-per-key")

    def test_without_atomic_outbox_breaks_payment_has_outbox_row(self):
        report = run_harness(HarnessConfig.standard(1, 1500).without("atomic_outbox"))
        self.assertFalse(report.passed())
        self.assertEqual(report.first_broken_invariant(), "payment-has-outbox-row")

    def test_without_consumer_dedupe_double_posts_and_breaks_conservation(self):
        report = run_harness(HarnessConfig.standard(1, 1500).without("consumer_dedupe"))
        self.assertFalse(report.passed())
        self.assertEqual(report.first_broken_invariant(), "posted-once-downstream")
        self.assertTrue(any(v.startswith("[money-conserved]") for v in report.violations))

    def test_unknown_defence_name_is_rejected(self):
        with self.assertRaises(ValueError):
            Defences.all().without("ledger")


if __name__ == "__main__":
    unittest.main()
