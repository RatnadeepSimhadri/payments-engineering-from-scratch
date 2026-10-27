"""Tests for phases/10-money-and-ledgers/11-ledger-concurrency. Run: python3 -m unittest -v"""
import threading
import unittest

from main import (EUR, USD, AtomicLong, ConcurrentLedger, CurrencyMismatchError, Exploration, InsufficientFundsError,
                  InterleavingChecker, Money, Op, OptimisticLedger, PessimisticLedger, ShardedLedger, Strategy, Workload)


def usd(minor: int) -> Money:
    return Money.of_minor(minor, USD)


def all_ledgers():
    return [PessimisticLedger(), OptimisticLedger(), ShardedLedger()]


class InterleavingTest(unittest.TestCase):
    def test_two_deposits(self):
        two_deposits = [Op("A", 100), Op("B", 100)]
        naive = InterleavingChecker.explore(Strategy.NAIVE, 0, two_deposits)
        self.assertEqual(naive.schedules, 6)      # 2 ops x 2 steps
        self.assertEqual(naive.violations, 4)     # 4 of 6 schedules lose a deposit
        self.assertEqual(InterleavingChecker.explore(Strategy.VERSIONED, 0, two_deposits).violations, 0)
        self.assertEqual(InterleavingChecker.explore(Strategy.LOCKED, 0, two_deposits).violations, 0)

    def test_double_payout(self):
        overdraft = [Op("P1", -60), Op("P2", -60)]
        naive_over = InterleavingChecker.explore(Strategy.NAIVE, 100, overdraft)
        self.assertGreater(naive_over.violations, 0)
        self.assertIn("lost update", naive_over.first_violation)
        self.assertEqual(InterleavingChecker.explore(Strategy.VERSIONED, 100, overdraft).violations, 0)
        self.assertEqual(InterleavingChecker.explore(Strategy.LOCKED, 100, overdraft).violations, 0)

    def test_three_ops(self):
        three = [Op("P1", -6_000), Op("P2", -6_000), Op("D", 3_000)]
        n3 = InterleavingChecker.explore(Strategy.NAIVE, 10_000, three)
        v3 = InterleavingChecker.explore(Strategy.VERSIONED, 10_000, three)
        l3 = InterleavingChecker.explore(Strategy.LOCKED, 10_000, three)
        self.assertEqual(n3.schedules, 90)        # 6!/(2!2!2!)
        self.assertEqual(n3.violations, 76)
        self.assertGreater(v3.schedules, 90)      # retries add schedules
        self.assertEqual(v3.violations, 0)
        self.assertEqual(l3.schedules, 6)         # only the 3! serial orders exist
        self.assertEqual(l3.violations, 0)
        self.assertIsInstance(n3, Exploration)


class SingleThreadedTest(unittest.TestCase):
    def test_each_ledger_single_threaded(self):
        for l in all_ledgers():
            with self.subTest(ledger=l.name()):
                l.open_account("a", USD, 1_000, False)
                l.open_account("b", USD, 0, False)
                l.open_account("e", EUR, 0, False)
                l.transfer("a", "b", usd(400))
                self.assertEqual(l.balance("a").minor_units, 600)
                self.assertEqual(l.balance("b").minor_units, 400)
                with self.assertRaises(InsufficientFundsError):
                    l.transfer("a", "b", usd(601))
                self.assertEqual(l.balance("a").minor_units, 600)
                self.assertEqual(l.total_minor(), 1_000)
                with self.assertRaises(CurrencyMismatchError):
                    l.transfer("a", "e", usd(1))
                with self.assertRaises(ValueError):
                    l.transfer("a", "a", usd(1))
                with self.assertRaises(ValueError):
                    l.transfer("a", "b", usd(0))
                with self.assertRaises(ValueError):
                    l.transfer("a", "zzz", usd(1))
                with self.assertRaises(ValueError):
                    l.open_account("a", USD, 0, False)

    def test_optimistic_versions(self):
        opt = OptimisticLedger()
        opt.open_account("x", USD, 100, False)
        opt.open_account("y", USD, 0, False)
        opt.transfer("x", "y", usd(10))
        opt.transfer("x", "y", usd(10))
        self.assertEqual(opt.version("x"), 2)
        self.assertEqual(opt.version("y"), 2)


class ShardingTest(unittest.TestCase):
    def test_sharding(self):
        sh = ShardedLedger()
        sh.open_hot_account("m", USD, 10, 4)
        sh.open_account("c", USD, 1_000, False)
        sh.open_account("bank", USD, 0, True)
        self.assertEqual([sh.shard_balance("m", k) for k in range(4)], [3, 3, 2, 2])
        for _ in range(8):
            sh.transfer("c", "m", usd(100))
        self.assertEqual(sh.shard_balance("m", 0), 203)
        self.assertEqual(sh.shard_balance("m", 3), 202)
        self.assertEqual(sh.balance("m").minor_units, 810)
        sh.transfer("m", "bank", usd(500))
        self.assertEqual(sh.balance("m").minor_units, 310)
        self.assertEqual(sh.balance("bank").minor_units, 500)
        with self.assertRaises(InsufficientFundsError):
            sh.transfer("m", "bank", usd(311))
        self.assertEqual(sh.balance("m").minor_units, 310)
        self.assertTrue(sh.balance("m#inflight").is_zero())
        before = sh.total_minor()
        sh.roll_up("m")
        self.assertEqual(sh.shard_balance("m", 0), 310)
        self.assertEqual(sh.shard_balance("m", 1), 0)
        self.assertEqual(sh.shard_balance("m", 3), 0)
        self.assertEqual(sh.total_minor(), before)
        sh.open_hot_account("m2", USD, 0, 2)
        with self.assertRaises(ValueError):
            sh.transfer("m", "m2", usd(1))


class StressTest(unittest.TestCase):
    def test_stress_with_concurrent_auditor(self):
        w = Workload(4, 5_000, 50, 10_000_000, 500_000_000, 10, 7)
        expected = w.expected()
        initial_total = 50 * 10_000_000 + 500_000_000
        for l in all_ledgers():
            with self.subTest(ledger=l.name()):
                w.open(l, 4)
                running = threading.Event()
                running.set()
                bad_cuts, cuts = AtomicLong(), AtomicLong()

                def audit(ledger: ConcurrentLedger = l) -> None:
                    while running.is_set():
                        cuts.increment_and_get()
                        if ledger.total_minor() != initial_total:
                            bad_cuts.increment_and_get()

                auditor = threading.Thread(target=audit)
                auditor.start()
                failed = w.run(l)
                running.clear()
                auditor.join()
                if isinstance(l, ShardedLedger):
                    l.roll_up("merchant")
                match = all(l.balance(k).minor_units == v for k, v in expected.items())
                self.assertEqual(failed, 0)
                self.assertEqual(l.total_minor(), initial_total)
                self.assertTrue(match)
                self.assertEqual(bad_cuts.get(), 0)
                self.assertGreater(cuts.get(), 0)
                self.assertGreaterEqual(l.balance("merchant").minor_units, 0)


class DeadlockFreedomTest(unittest.TestCase):
    def test_opposite_direction_transfers_finish(self):
        for l in (PessimisticLedger(), OptimisticLedger()):
            with self.subTest(ledger=l.name()):
                l.open_account("p", USD, 1_000_000, False)
                l.open_account("q", USD, 1_000_000, False)

                def pump(src: str, dst: str, ledger: ConcurrentLedger = l) -> None:
                    for _ in range(20_000):
                        ledger.transfer(src, dst, usd(1))

                t1 = threading.Thread(target=pump, args=("p", "q"))
                t2 = threading.Thread(target=pump, args=("q", "p"))
                t1.start()
                t2.start()
                t1.join(10)
                t2.join(10)
                self.assertFalse(t1.is_alive())
                self.assertFalse(t2.is_alive())
                self.assertEqual(l.balance("p").minor_units, 1_000_000)
                self.assertEqual(l.balance("q").minor_units, 1_000_000)


if __name__ == "__main__":
    unittest.main()
