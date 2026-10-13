"""Tests for phases/08-messaging-and-streams/05-event-sourcing. Run: python3 -m unittest -v"""
import unittest

from main import (
    EUR, USD, AccountService, AccountState, CommandRejectedError, ConcurrencyError, CurrencyMismatchError, Deposit,
    Deposited, EventStore, Held, Money, OpenAccount, Opened, PlaceHold, Released, ReleaseHold, SnapshotStore, Withdraw,
    Withdrawn,
)


def usd(cents: int) -> Money:
    return Money.of_minor(cents, USD)


HISTORY = [Opened("a1", USD), Deposited(usd(10_000)), Held("h1", usd(2_500)), Withdrawn(usd(6_000)), Released("h1")]


class FoldTest(unittest.TestCase):
    def test_replay_version_equals_event_count(self):
        self.assertEqual(AccountState.replay(HISTORY).version, 5)

    def test_replay_balance_is_deposits_minus_withdrawals(self):
        s = AccountState.replay(HISTORY)
        self.assertEqual(s.balance, usd(4_000))
        self.assertEqual(s.available(), usd(4_000))
        self.assertEqual(dict(s.holds), {})

    def test_hold_reduces_available_not_balance(self):
        s = AccountState.replay(HISTORY[:3])
        self.assertEqual(s.balance, usd(10_000))
        self.assertEqual(s.available(), usd(7_500))

    def test_fold_is_incremental(self):
        prefix = AccountState.replay(HISTORY[:3])
        self.assertEqual(prefix.apply_all(HISTORY[3:]), AccountState.replay(HISTORY))

    def test_empty_state(self):
        self.assertFalse(AccountState.empty().is_open())
        self.assertEqual(AccountState.empty().version, 0)


class DecideTest(unittest.TestCase):
    def setUp(self):
        self.state = AccountState.replay(HISTORY[:3])  # balance 100.00, hold 25.00, available 75.00

    def test_overdraft_rejected(self):
        with self.assertRaises(CommandRejectedError):
            self.state.decide(Withdraw(usd(7_501)))

    def test_withdraw_exactly_available_allowed(self):
        self.assertEqual(self.state.decide(Withdraw(usd(7_500))), [Withdrawn(usd(7_500))])

    def test_hold_beyond_available_rejected(self):
        with self.assertRaises(CommandRejectedError):
            self.state.decide(PlaceHold("h2", usd(7_501)))

    def test_duplicate_hold_rejected(self):
        with self.assertRaises(CommandRejectedError):
            self.state.decide(PlaceHold("h1", usd(1)))

    def test_release_unknown_hold_rejected(self):
        with self.assertRaises(CommandRejectedError):
            self.state.decide(ReleaseHold("nope"))

    def test_non_positive_amounts_rejected(self):
        with self.assertRaises(CommandRejectedError):
            self.state.decide(Deposit(usd(0)))
        with self.assertRaises(CommandRejectedError):
            self.state.decide(Deposit(usd(-1)))

    def test_currency_mismatch_rejected(self):
        with self.assertRaises(CurrencyMismatchError):
            self.state.decide(Deposit(Money.of_minor(100, EUR)))

    def test_unopened_and_reopen_rejected(self):
        with self.assertRaises(CommandRejectedError):
            AccountState.empty().decide(Deposit(usd(1)))
        with self.assertRaises(CommandRejectedError):
            self.state.decide(OpenAccount("a1", USD))

    def test_decide_does_not_mutate(self):
        self.state.decide(Withdraw(usd(1)))
        self.assertEqual(self.state.version, 3)
        self.assertEqual(len(self.state.holds), 1)


class EventStoreTest(unittest.TestCase):
    def test_append_and_version(self):
        store = EventStore()
        self.assertEqual(store.version("x"), 0)
        self.assertEqual(store.append("x", 0, [Opened("x", USD), Deposited(usd(1))]), 2)

    def test_stale_expected_version_fails_and_writes_nothing(self):
        store = EventStore()
        store.append("x", 0, [Opened("x", USD), Deposited(usd(1))])
        with self.assertRaises(ConcurrencyError):
            store.append("x", 1, [Deposited(usd(1))])
        self.assertEqual(store.version("x"), 2)
        self.assertEqual(store.total_events(), 2)

    def test_read_tail(self):
        store = EventStore()
        store.append("x", 0, [Opened("x", USD), Deposited(usd(1))])
        self.assertEqual(store.read("x", 1), [Deposited(usd(1))])
        self.assertEqual(store.read("nope", 0), [])


class AccountServiceTest(unittest.TestCase):
    def setUp(self):
        self.store = EventStore()
        self.snaps = SnapshotStore()
        self.svc = AccountService(self.store, self.snaps, 3)
        self.svc.handle("acct", OpenAccount("acct", USD))
        self.svc.handle("acct", Deposit(usd(5_000)))

    def test_second_writer_at_same_version_loses(self):
        seen = self.store.version("acct")
        self.svc.handle("acct", Withdraw(usd(1_000)), seen)
        with self.assertRaises(ConcurrencyError):
            self.svc.handle("acct", Withdraw(usd(1_000)), seen)
        self.assertEqual(self.store.version("acct"), 3)
        retried = self.svc.handle("acct", Withdraw(usd(1_000)))
        self.assertEqual((retried.version, retried.balance), (4, usd(3_000)))

    def test_snapshot_every_n_and_tail_replay(self):
        self.svc.handle("acct", Withdraw(usd(1_000)))
        self.assertEqual(self.snaps.writes, 1)
        self.assertEqual(self.snaps.load("acct").version, 3)
        self.svc.handle("acct", Deposit(usd(1)))
        loaded = self.svc.load("acct")
        self.assertEqual(self.svc.events_replayed_on_last_load, 1)
        self.assertEqual(loaded.version, 4)
        self.assertEqual(loaded, self.svc.replay_from_scratch("acct"))
        self.svc.handle("acct", Deposit(usd(1)))
        self.svc.handle("acct", Deposit(usd(1)))
        self.assertEqual(self.snaps.writes, 2)
        self.assertEqual(self.snaps.load("acct").version, 6)

    def test_rejected_command_appends_nothing(self):
        before = self.store.version("acct")
        with self.assertRaises(CommandRejectedError):
            self.svc.handle("acct", Withdraw(usd(999_999)))
        self.assertEqual(self.store.version("acct"), before)

    def test_snapshot_every_must_be_positive(self):
        with self.assertRaises(ValueError):
            AccountService(self.store, self.snaps, 0)


if __name__ == "__main__":
    unittest.main()
