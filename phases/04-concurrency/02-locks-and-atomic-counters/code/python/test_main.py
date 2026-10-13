"""Tests for phases/04-concurrency/02-locks-and-atomic-counters. Run: python3 -m unittest -v"""

import threading
import unittest

from main import (
    USD,
    EUR,
    Account,
    AtomicCounter,
    BenchmarkResult,
    CurrencyMismatchError,
    FakeStopwatch,
    InsufficientFundsError,
    LockedCounter,
    Money,
    RacyCounter,
    StripedAccountMap,
    measure,
    measure_map,
    stable_hash,
)

IDS = [f"acct-{n}" for n in range(1, 9)]


def clock() -> FakeStopwatch:
    return FakeStopwatch(0, 2_000_000)


class RacyCounterTest(unittest.TestCase):
    """The unsafe counter is tested only through an injected interleaving, never by racing threads."""

    def test_scripted_interleaving_loses_one_update(self) -> None:
        holder: list[RacyCounter] = []
        fired = [False]

        def interleave() -> None:
            if not fired[0]:
                fired[0] = True
                holder[0].add(5)

        holder.append(RacyCounter(interleave))
        holder[0].add(5)
        self.assertEqual(holder[0].get(), 5)  # not 10

    def test_sequential_use_is_exact(self) -> None:
        counter = RacyCounter()
        for _ in range(1_000):
            counter.add(1)
        self.assertEqual(counter.get(), 1_000)


class SafeCounterTest(unittest.TestCase):
    def test_locked_counter_exact_under_four_threads(self) -> None:
        result = measure(LockedCounter(), 4, 5_000, 1, clock())
        self.assertTrue(result.exact())
        self.assertEqual(result.observed, 20_000)

    def test_atomic_counter_exact_under_four_threads(self) -> None:
        result = measure(AtomicCounter(), 4, 5_000, 1, clock())
        self.assertTrue(result.exact())
        self.assertEqual(result.observed, 20_000)

    def test_cas_never_retries_without_contention(self) -> None:
        solo = AtomicCounter()
        for _ in range(100):
            solo.add(3)
        self.assertEqual(solo.cas_retries(), 0)
        self.assertEqual(solo.get(), 300)

    def test_negative_deltas_are_exact(self) -> None:
        result = measure(LockedCounter(), 3, 1_000, -7, clock())
        self.assertEqual(result.observed, -21_000)
        self.assertTrue(result.exact())


class StripedAccountMapTest(unittest.TestCase):
    def setUp(self) -> None:
        self.map = StripedAccountMap(USD, 4)
        for account_id in IDS:
            self.map.open(account_id)

    def test_total_exact_under_eight_threads(self) -> None:
        result = measure_map(self.map, IDS, 8, 2_000, 1, clock())
        self.assertTrue(result.exact())
        self.assertEqual(result.observed, 16_000)
        for account_id in IDS:
            self.assertEqual(self.map.balance(account_id).minor_units, 2_000)
        self.assertEqual(self.map.total().minor_units, 16_000)

    def test_stripe_index_is_stable_and_in_range(self) -> None:
        self.assertEqual(self.map.stripe_index("acct-1"), self.map.stripe_index("acct-1"))
        self.assertTrue(0 <= self.map.stripe_index("acct-1") < 4)
        self.assertEqual(stable_hash("acct-1"), -1423448841)  # same value as Java's String.hashCode

    def test_rejections(self) -> None:
        with self.assertRaises(ValueError):
            self.map.credit("ghost", Money.of_minor(1, USD))
        with self.assertRaises(CurrencyMismatchError):
            self.map.credit("acct-1", Money.of_minor(1, EUR))
        with self.assertRaises(ValueError):
            self.map.credit("acct-1", Money.of_minor(-1, USD))
        with self.assertRaises(ValueError):
            StripedAccountMap(USD, 0)

    def test_debit_beyond_balance_is_refused_and_leaves_balance_untouched(self) -> None:
        wallet = StripedAccountMap(USD, 2)
        wallet.open("w")
        wallet.credit("w", Money.of_minor(150, USD))
        with self.assertRaises(InsufficientFundsError):
            wallet.debit("w", Money.of_minor(151, USD))
        self.assertEqual(wallet.balance("w").minor_units, 150)

    def test_two_racing_debits_exactly_one_succeeds(self) -> None:
        wallet = StripedAccountMap(USD, 2)
        wallet.open("w")
        wallet.credit("w", Money.of_minor(150, USD))
        go = threading.Event()
        successes: list[int] = []
        lock = threading.Lock()

        def attempt() -> None:
            go.wait()
            try:
                wallet.debit("w", Money.of_minor(100, USD))
                with lock:
                    successes.append(1)
            except InsufficientFundsError:
                pass  # exactly one thread lands here

        workers = [threading.Thread(target=attempt) for _ in range(2)]
        for w in workers:
            w.start()
        go.set()
        for w in workers:
            w.join()
        self.assertEqual(len(successes), 1)
        self.assertEqual(wallet.balance("w").minor_units, 50)
        self.assertEqual(wallet.total(), Money.of_minor(50, USD))


class AccountTest(unittest.TestCase):
    def test_balance_after_credits_and_debit(self) -> None:
        acct = Account("a", USD, LockedCounter())
        acct.credit(Money.of_minor(10_000, USD))
        acct.debit(Money.of_minor(1_999, USD))
        self.assertEqual(str(acct.balance()), "80.01 USD")

    def test_account_rejects_wrong_currency_and_negative(self) -> None:
        acct = Account("a", USD, LockedCounter())
        with self.assertRaises(CurrencyMismatchError):
            acct.credit(Money.of_minor(1, EUR))
        with self.assertRaises(ValueError):
            acct.credit(Money.of_minor(-1, USD))

    def test_money_plus_is_exact(self) -> None:
        self.assertEqual(Money.of_minor(110, USD).plus(Money.of_minor(220, USD)).minor_units, 330)


class BenchmarkResultTest(unittest.TestCase):
    def test_ops_per_second_with_fake_stopwatch(self) -> None:
        result = BenchmarkResult("x", 4, 1_000, 1, 4_000, 2_000_000)
        self.assertEqual(result.ops_per_second(), 2_000_000.0)
        self.assertEqual(result.expected(), 4_000)
        self.assertEqual(result.lost(), 0)
        self.assertTrue(result.exact())

    def test_short_result_reports_the_loss(self) -> None:
        self.assertEqual(BenchmarkResult("x", 2, 10, 1, 17, 1).lost(), 3)

    def test_benchmark_rejects_zero_threads(self) -> None:
        with self.assertRaises(ValueError):
            measure(LockedCounter(), 0, 1, 1, clock())


if __name__ == "__main__":
    unittest.main()
