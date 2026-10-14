"""Tests for phases/07-storage-and-transactions/12-saga-pattern. Run: python3 -m unittest -v"""
import unittest

from main import (
    CaptureFunds,
    CrashException,
    CrashPoint,
    Currency,
    CurrencyMismatchException,
    Money,
    Order,
    ReserveFunds,
    SagaLog,
    SagaOrchestrator,
    SagaStatus,
    ShipOrder,
    ShippingService,
    SimClock,
    StepFailedException,
    WalletService,
    usd,
)


class World:
    """A fresh world: alice has 100.00 USD, five widgets in stock, one orchestrator over one log."""

    def __init__(self) -> None:
        self.clock = SimClock(0)
        self.wallet = WalletService({"alice": usd("100.00")})
        self.shipping = ShippingService({"widget": 5})
        self.log = SagaLog(self.clock)
        self.saga = SagaOrchestrator(self.log, [ReserveFunds(self.wallet), CaptureFunds(self.wallet),
                                                ShipOrder(self.shipping)], self.clock, 10)

    def order(self, id: str, qty: int, amount: str) -> Order:
        return Order(id, "alice", "widget", qty, usd(amount))

    def kinds(self, saga_id: str) -> list[str]:
        return [r.kind + ("" if not r.step else ":" + r.step) for r in self.log.for_saga(saga_id)]


class HappyPathTest(unittest.TestCase):
    def test_completes(self):
        w = World()
        out = w.saga.run(w.order("s1", 2, "30.00"))
        self.assertIs(out.status, SagaStatus.COMPLETED)
        self.assertEqual(w.wallet.balance("alice"), usd("70.00"))
        self.assertTrue(not w.wallet.has_hold("s1") and w.wallet.available("alice") == usd("70.00"))
        self.assertTrue(w.shipping.stock_of("widget") == 3 and w.shipping.is_shipped("s1"))
        self.assertEqual(w.kinds("s1"), [
            "SAGA_STARTED", "STEP_STARTED:reserve-funds", "STEP_COMPLETED:reserve-funds",
            "STEP_STARTED:capture-funds", "STEP_COMPLETED:capture-funds",
            "STEP_STARTED:ship-order", "STEP_COMPLETED:ship-order", "SAGA_COMPLETED"])
        self.assertEqual(w.clock.now(), 30)


class CompensationTest(unittest.TestCase):
    def test_failure_at_step_three(self):
        w = World()
        out = w.saga.run(w.order("s2", 9, "40.00"))
        self.assertTrue(out.status is SagaStatus.COMPENSATED and out.failed_step == "ship-order")
        self.assertEqual(w.wallet.balance("alice"), usd("100.00"))
        self.assertTrue(w.wallet.available("alice") == usd("100.00") and not w.wallet.has_hold("s2"))
        self.assertEqual(w.shipping.stock_of("widget"), 5)
        kinds = w.kinds("s2")
        self.assertLess(kinds.index("COMP_COMPLETED:capture-funds"), kinds.index("COMP_STARTED:reserve-funds"))
        self.assertNotIn("COMP_STARTED:ship-order", kinds)
        self.assertEqual(kinds[-1], "SAGA_COMPENSATED")

    def test_failure_at_step_one(self):
        w = World()
        out = w.saga.run(w.order("s3", 1, "250.00"))
        self.assertTrue(out.status is SagaStatus.COMPENSATED and out.failed_step == "reserve-funds")
        self.assertFalse(any(k.startswith("COMP_") for k in w.kinds("s3")))
        self.assertEqual(w.wallet.balance("alice"), usd("100.00"))


class RecoveryTest(unittest.TestCase):
    def test_crash_after_execute(self):
        w = World()
        w.saga.crash_point = CrashPoint.after_execute("capture-funds")
        with self.assertRaises(CrashException):
            w.saga.run(w.order("s4", 1, "25.00"))
        self.assertEqual(w.wallet.balance("alice"), usd("75.00"))
        self.assertIs(SagaOrchestrator.status_of(w.log.for_saga("s4")), SagaStatus.RUNNING)
        calls_before = w.wallet.calls
        resumed = w.saga.recover()
        self.assertTrue(len(resumed) == 1 and resumed[0].status is SagaStatus.COMPLETED)
        self.assertEqual(w.wallet.calls, calls_before + 1)
        self.assertEqual(w.wallet.balance("alice"), usd("75.00"))
        self.assertEqual(w.shipping.stock_of("widget"), 4)
        self.assertEqual(w.saga.recover(), [])

    def test_crash_during_compensation(self):
        w = World()
        w.saga.crash_point = CrashPoint.after_compensate("capture-funds")
        with self.assertRaises(CrashException):
            w.saga.run(w.order("s5", 9, "10.00"))
        self.assertEqual(w.wallet.balance("alice"), usd("100.00"))
        self.assertIs(SagaOrchestrator.status_of(w.log.for_saga("s5")), SagaStatus.COMPENSATING)
        resumed = w.saga.recover()
        self.assertTrue(resumed[0].status is SagaStatus.COMPENSATED and resumed[0].failed_step == "ship-order")
        self.assertEqual(w.wallet.balance("alice"), usd("100.00"))
        self.assertTrue(not w.wallet.has_hold("s5") and w.wallet.available("alice") == usd("100.00"))


class SemanticLockTest(unittest.TestCase):
    def test_pending_hold_is_visible(self):
        w = World()
        w.saga.crash_point = CrashPoint.after_execute("reserve-funds")
        with self.assertRaises(CrashException):
            w.saga.run(w.order("A", 1, "80.00"))
        self.assertTrue(w.wallet.balance("alice") == usd("100.00") and w.wallet.available("alice") == usd("20.00"))
        b = w.saga.run(w.order("B", 1, "50.00"))
        self.assertTrue(b.status is SagaStatus.COMPENSATED and "insufficient available" in b.reason)
        w.saga.recover()
        self.assertTrue(w.wallet.balance("alice") == usd("20.00") and w.wallet.available("alice") == usd("20.00"))


class ServiceIdempotencyTest(unittest.TestCase):
    def test_duplicates_apply_once(self):
        wallet = WalletService({"alice": usd("10.00")})
        wallet.reserve("k", "alice", usd("4.00"))
        wallet.reserve("k", "alice", usd("4.00"))
        self.assertEqual(wallet.available("alice"), usd("6.00"))
        wallet.capture("k")
        wallet.capture("k")
        self.assertEqual(wallet.balance("alice"), usd("6.00"))
        wallet.refund("k", "alice")
        wallet.refund("k", "alice")
        self.assertEqual(wallet.balance("alice"), usd("10.00"))
        with self.assertRaises(StepFailedException):
            wallet.capture("never-reserved")
        wallet.release("never-reserved")
        self.assertEqual(wallet.available("alice"), usd("10.00"))


class MoneyTest(unittest.TestCase):
    def test_money_discipline(self):
        with self.assertRaises(CurrencyMismatchException):
            usd("1.00").plus(Money.of_minor(100, Currency("EUR", 2)))
        self.assertTrue(issubclass(CurrencyMismatchException, ValueError))  # Java: IllegalArgumentException
        self.assertEqual(str(usd("0.05")), "0.05 USD")
        with self.assertRaises(OverflowError):  # Python-only: Math.addExact checked by hand
            Money.of_minor((1 << 63) - 1, Currency.USD).plus(usd("0.01"))


if __name__ == "__main__":
    unittest.main()
