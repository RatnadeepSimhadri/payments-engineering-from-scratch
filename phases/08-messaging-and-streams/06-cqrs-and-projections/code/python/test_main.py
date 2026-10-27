"""Tests for phases/08-messaging-and-streams/06-cqrs-and-projections. Run: python3 -m unittest -v"""
import unittest

from main import (
    USD,
    BalanceView,
    CheckpointStore,
    CommandHandler,
    CommandRejectedError,
    ConcurrencyError,
    ConsistencyProbe,
    CrashError,
    DailyActivityView,
    Deposit,
    Deposited,
    EventStore,
    IdempotentBalanceView,
    LargeWithdrawalsView,
    Money,
    OpenAccount,
    Projector,
    ReadSide,
    SimClock,
    Withdraw,
)


def usd(minor):
    return Money.of_minor(minor, USD)


def seeded(clock):
    """A store with two accounts and nine events, the same shape as the demo."""
    store = EventStore()
    h = CommandHandler(store, clock)
    h.handle("a", OpenAccount("a", "Alice", USD))
    h.handle("b", OpenAccount("b", "Bob", USD))
    h.handle("a", Deposit(usd(250_000), "d1"))
    h.handle("b", Deposit(usd(80_000), "d2"))
    clock.advance(86_400_000)
    h.handle("a", Withdraw(usd(120_000), "w1"))
    h.handle("b", Withdraw(usd(60_000), "w2"))
    h.handle("a", Withdraw(usd(75_000), "w3"))
    h.handle("b", Deposit(usd(15_000), "d3"))
    h.handle("a", Deposit(usd(5_000), "d4"))
    return store


class Base(unittest.TestCase):
    def setUp(self):
        self.clock = SimClock(1_700_000_000_000)
        self.store = seeded(self.clock)
        self.handler = CommandHandler(self.store, self.clock)
        self.checkpoints = CheckpointStore()


class WriteSideTest(Base):
    def test_store_and_write_model(self):
        self.assertEqual(self.store.last_position(), 9)
        self.assertEqual(self.store.read_all(0, 1)[0].position, 1)
        self.assertEqual(self.store.version("a"), 5)
        self.assertEqual(self.store.version("b"), 4)
        self.assertEqual(len(self.store.read_stream("a")), 5)
        self.assertEqual(self.store.read_stream("a")[4].version, 5)
        with self.assertRaises(CommandRejectedError):
            self.handler.handle("b", Withdraw(usd(999_999), "x"))
        with self.assertRaises(CommandRejectedError):
            self.handler.handle("zz", Deposit(usd(1), "x"))
        with self.assertRaises(CommandRejectedError):
            self.handler.handle("a", OpenAccount("a", "A", USD))
        with self.assertRaises(CommandRejectedError):
            self.handler.handle("a", Deposit(usd(0), "x"))
        with self.assertRaises(ConcurrencyError):
            self.store.append("a", 2, [Deposited(usd(1), "x")], self.clock.now())
        self.assertEqual(self.store.last_position(), 9)
        with self.assertRaises(ValueError):
            self.store.read_all(10, 1)
        self.assertEqual(self.store.read_all(9, 5), [])
        self.assertEqual(len(self.store.read_all(0, 4)), 4)
        self.assertEqual(self.store.read_all(0, 4)[3].position, 4)


class ProjectionTest(Base):
    def test_projections_fold_the_log(self):
        balances = IdempotentBalanceView()
        projector = Projector(self.store, self.checkpoints, balances, 3)
        self.assertEqual(projector.position(), 0)
        self.assertEqual(projector.lag(), 9)
        self.assertEqual(projector.run_once(self.clock.now()), 3)
        self.assertEqual(projector.position(), 3)
        self.assertEqual(projector.catch_up(self.clock.now()), 6)
        self.assertTrue(projector.caught_up())
        self.assertEqual(balances.balance("a"), usd(60_000))
        self.assertEqual(balances.balance("b"), usd(35_000))
        self.assertEqual(balances.owner("a"), "Alice")

        daily = DailyActivityView(USD)
        Projector(self.store, self.checkpoints, daily, 100).catch_up(self.clock.now())
        day0 = DailyActivityView.day_of(1_700_000_000_000)
        self.assertEqual((daily.day(day0).deposits, daily.day(day0).withdrawals, daily.day(day0 + 1).withdrawals), (2, 0, 3))
        self.assertEqual(daily.day(day0 + 1).net, usd(-235_000))

        v1 = LargeWithdrawalsView(1, usd(100_000))
        Projector(self.store, self.checkpoints, v1, 100).catch_up(self.clock.now())
        self.assertEqual(len(v1.alerts()), 1)
        self.assertIsNone(v1.alerts()[0].owner)


class VersioningTest(Base):
    def test_versioning_and_promotion(self):
        v1 = LargeWithdrawalsView(1, usd(100_000))
        read_side = ReadSide()
        live_large = Projector(self.store, self.checkpoints, v1, 100)
        live_large.catch_up(self.clock.now())
        read_side.register(live_large)
        v2 = Projector(self.store, self.checkpoints, LargeWithdrawalsView(2, usd(50_000)), 4)
        read_side.register(v2)
        self.assertEqual(v2.position(), 0)
        self.assertIs(read_side.live("large-withdrawals"), live_large)
        v2.run_once(self.clock.now())
        with self.assertRaises(RuntimeError):
            read_side.promote("large-withdrawals")
        v2.catch_up(self.clock.now())
        retired = read_side.promote("large-withdrawals")
        self.assertIs(read_side.live("large-withdrawals"), v2)
        self.assertIs(retired, live_large)
        v2_view = v2.projection()
        self.assertEqual(len(v2_view.alerts()), 3)
        self.assertEqual(v2_view.alerts()[1].owner, "Bob")
        self.assertEqual(retired.position(), 9)
        with self.assertRaises(RuntimeError):
            read_side.promote("large-withdrawals")
        with self.assertRaises(RuntimeError):
            self.checkpoints.save(v1, 1)


class ConsistencyTest(Base):
    def test_read_your_own_writes(self):
        balances = IdempotentBalanceView()
        balance_projector = Projector(self.store, self.checkpoints, balances, 3)
        balance_projector.catch_up(self.clock.now())
        read_side = ReadSide()
        read_side.register(balance_projector)
        pos = self.handler.handle("a", Deposit(usd(1_000), "late"))
        self.assertEqual(balance_projector.lag(), 1)
        self.assertEqual(balances.balance("a"), usd(60_000))
        self.assertTrue(read_side.wait_for("balances", pos, self.clock.now(), 5))
        self.assertEqual(balances.balance("a"), usd(61_000))
        self.assertFalse(read_side.wait_for("balances", pos + 1, self.clock.now(), 0))

    def test_crash_between_apply_and_checkpoint(self):
        self.handler.handle("a", Deposit(usd(1_000), "late"))
        naive = BalanceView()
        naive_projector = Projector(self.store, CheckpointStore(), naive, 4)
        naive_projector.run_once(self.clock.now())
        naive_projector.crash_after_applies(2)
        with self.assertRaises(CrashError):
            naive_projector.run_once(self.clock.now())
        self.assertEqual(naive_projector.position(), 4)
        naive_projector.catch_up(self.clock.now())
        self.assertEqual(naive.balance("a"), usd(61_000 - 120_000))
        safe = IdempotentBalanceView()
        safe_projector = Projector(self.store, CheckpointStore(), safe, 4)
        safe_projector.run_once(self.clock.now())
        safe_projector.crash_after_applies(2)
        try:
            safe_projector.run_once(self.clock.now())
        except CrashError:
            pass
        safe_projector.catch_up(self.clock.now())
        self.assertEqual(safe.balance("a"), usd(61_000))
        self.assertEqual(safe.skipped(), 2)

    def test_consistency_window(self):
        slow = ConsistencyProbe.run(100, 7, 50, 100)
        fast = ConsistencyProbe.run(100, 7, 10, 100)
        self.assertEqual((slow.events, fast.events), (100, 100))
        self.assertLessEqual(slow.max, 50)
        self.assertLessEqual(fast.max, 10)
        self.assertLess(fast.mean, slow.mean)
        overloaded = ConsistencyProbe.run(100, 7, 10, 1)
        self.assertGreater(overloaded.max, 100)
        with self.assertRaises(ValueError):
            Projector(self.store, self.checkpoints, BalanceView(), 0)


if __name__ == "__main__":
    unittest.main()
