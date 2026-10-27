"""Tests for phases/08-messaging-and-streams/05-event-sourcing. Run: python3 -m unittest -v"""
import unittest

from main import (
    EUR,
    USD,
    AccountService,
    AccountState,
    CommandRejectedError,
    ConcurrencyError,
    CurrencyMismatchError,
    Deposit,
    Deposited,
    EventStore,
    Held,
    Money,
    OpenAccount,
    Opened,
    PlaceHold,
    ReleaseHold,
    Released,
    SnapshotStore,
    Withdraw,
    Withdrawn,
)


def usd(cents):
    return Money.of_minor(cents, USD)


HISTORY = [Opened("a1", USD), Deposited(usd(10_000)), Held("h1", usd(2_500)), Withdrawn(usd(6_000)), Released("h1")]


class FoldTest(unittest.TestCase):
    def test_replay(self):
        s = AccountState.replay(HISTORY)
        self.assertEqual(s.version, 5)
        self.assertEqual(s.balance, usd(4_000))
        self.assertEqual(s.available(), usd(4_000))
        self.assertEqual(len(s.holds), 0)
        after_hold = AccountState.replay(HISTORY[:3])
        self.assertEqual(after_hold.balance, usd(10_000))
        self.assertEqual(after_hold.available(), usd(7_500))
        self.assertEqual(s, after_hold.apply_all(HISTORY[3:]))
        self.assertFalse(AccountState.empty().is_open())
        self.assertEqual(AccountState.empty().version, 0)

    def test_state_is_immutable(self):
        s = AccountState.replay(HISTORY[:3])
        with self.assertRaises(TypeError):
            s.holds["h2"] = usd(1)
        with self.assertRaises(AttributeError):
            s.version = 9


class DecideTest(unittest.TestCase):
    def setUp(self):
        self.after_hold = AccountState.replay(HISTORY[:3])

    def test_invariants(self):
        with self.assertRaises(CommandRejectedError):
            self.after_hold.decide(Withdraw(usd(7_501)))
        self.assertEqual(len(self.after_hold.decide(Withdraw(usd(7_500)))), 1)
        with self.assertRaises(CommandRejectedError):
            self.after_hold.decide(PlaceHold("h2", usd(7_501)))
        with self.assertRaises(CommandRejectedError):
            self.after_hold.decide(PlaceHold("h1", usd(1)))
        with self.assertRaises(CommandRejectedError):
            self.after_hold.decide(ReleaseHold("nope"))
        with self.assertRaises(CommandRejectedError):
            self.after_hold.decide(Deposit(usd(0)))
        with self.assertRaises(CommandRejectedError):
            self.after_hold.decide(Deposit(usd(-1)))
        with self.assertRaises(CurrencyMismatchError):
            self.after_hold.decide(Deposit(Money.of_minor(100, EUR)))
        with self.assertRaises(CommandRejectedError):
            AccountState.empty().decide(Deposit(usd(1)))
        with self.assertRaises(CommandRejectedError):
            self.after_hold.decide(OpenAccount("a1", USD))
        self.assertEqual(self.after_hold.version, 3)
        self.assertEqual(len(self.after_hold.holds), 1)


class EventStoreTest(unittest.TestCase):
    def test_optimistic_concurrency(self):
        store = EventStore()
        self.assertEqual(store.version("x"), 0)
        self.assertEqual(store.append("x", 0, [Opened("x", USD), Deposited(usd(1))]), 2)
        with self.assertRaises(ConcurrencyError):
            store.append("x", 1, [Deposited(usd(1))])
        self.assertEqual(store.version("x"), 2)
        self.assertEqual(store.total_events(), 2)
        tail = store.read("x", 1)
        self.assertEqual(len(tail), 1)
        self.assertIsInstance(tail[0], Deposited)
        self.assertEqual(store.read("nope", 0), [])


class ServiceTest(unittest.TestCase):
    def test_handle_retry_and_snapshots(self):
        store = EventStore()
        snaps = SnapshotStore()
        svc = AccountService(store, snaps, 3)
        svc.handle("acct", OpenAccount("acct", USD))
        svc.handle("acct", Deposit(usd(5_000)))
        seen = store.version("acct")
        svc.handle("acct", Withdraw(usd(1_000)), seen)
        with self.assertRaises(ConcurrencyError):
            svc.handle("acct", Withdraw(usd(1_000)), seen)
        self.assertEqual(store.version("acct"), 3)
        retried = svc.handle("acct", Withdraw(usd(1_000)))
        self.assertEqual(retried.version, 4)
        self.assertEqual(retried.balance, usd(3_000))
        self.assertEqual(snaps.writes(), 1)
        self.assertEqual(snaps.load("acct").version, 3)
        loaded = svc.load("acct")
        self.assertEqual(svc.events_replayed_on_last_load(), 1)
        self.assertEqual(loaded.version, 4)
        self.assertEqual(loaded, svc.replay_from_scratch("acct"))
        svc.handle("acct", Deposit(usd(1)))
        svc.handle("acct", Deposit(usd(1)))
        self.assertEqual(snaps.writes(), 2)
        self.assertEqual(snaps.load("acct").version, 6)
        before = store.version("acct")
        with self.assertRaises(CommandRejectedError):
            svc.handle("acct", Withdraw(usd(999_999)))
        self.assertEqual(store.version("acct"), before)
        with self.assertRaises(ValueError):
            AccountService(store, snaps, 0)


class MoneyTest(unittest.TestCase):
    def test_money_discipline(self):
        with self.assertRaises(TypeError):
            Money(1.0, USD)
        self.assertEqual(str(usd(-5)), "-0.05 USD")
        self.assertTrue(usd(1) < usd(2))


if __name__ == "__main__":
    unittest.main()
