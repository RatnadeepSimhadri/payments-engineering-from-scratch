"""Tests for phases/08-messaging-and-streams/03-delivery-guarantees. Run: python3 -m unittest -v"""
import unittest

from main import (
    USD,
    CrashError,
    Currency,
    CurrencyMismatchError,
    FaultInjector,
    Money,
    Posting,
    Simulation,
    Store,
    Strategy,
    sample_log,
)


class LogTest(unittest.TestCase):
    def setUp(self):
        self.log = sample_log(10)

    def test_log_shape(self):
        self.assertEqual(self.log.end_offset(), 10)
        self.assertEqual(self.log.read(10, 4), [])
        self.assertEqual(len(self.log.read(0, 4)), 4)
        self.assertEqual(len(self.log.read(8, 4)), 2)
        with self.assertRaises(ValueError):
            self.log.read(11, 1)


class SimulationTest(unittest.TestCase):
    def setUp(self):
        self.log = sample_log(10)
        self.expected = Money.zero(USD)
        for r in self.log.read(0, 10):
            self.expected = self.expected.plus(r.amount)

    def test_no_crash_every_strategy_agrees(self):
        for s in Strategy:
            o = Simulation.run(s, self.log, -1, 4)
            self.assertTrue(o.effectively_once() and o.posted == 10 and o.crashes == 0, s)
            self.assertEqual(o.balance, self.expected, s)

    def test_crash_after_offset_5(self):
        amo = Simulation.run(Strategy.AT_MOST_ONCE, self.log, 5, 4)
        self.assertEqual((amo.lost, amo.duplicates, amo.posted), (2, 0, 8))
        self.assertLess(amo.balance.minor_units, self.expected.minor_units)
        alo = Simulation.run(Strategy.AT_LEAST_ONCE, self.log, 5, 4)
        self.assertEqual((alo.duplicates, alo.lost, alo.posted), (2, 0, 12))
        self.assertGreater(alo.balance.minor_units, self.expected.minor_units)
        idem = Simulation.run(Strategy.AT_LEAST_ONCE_IDEMPOTENT, self.log, 5, 4)
        self.assertTrue(idem.effectively_once())
        self.assertEqual(idem.posted, 10)
        self.assertEqual(idem.balance, self.expected)
        tx = Simulation.run(Strategy.TRANSACTIONAL, self.log, 5, 4)
        self.assertTrue(tx.effectively_once())
        self.assertEqual(tx.posted, 10)
        self.assertEqual(tx.balance, self.expected)
        self.assertEqual([o.crashes for o in (amo, alo, idem, tx)], [1, 1, 1, 1])

    def test_crash_at_batch_boundaries(self):
        self.assertEqual(Simulation.run(Strategy.AT_MOST_ONCE, self.log, 7, 4).lost, 0)
        self.assertEqual(Simulation.run(Strategy.AT_LEAST_ONCE, self.log, 7, 4).duplicates, 4)
        self.assertEqual(Simulation.run(Strategy.AT_LEAST_ONCE, self.log, 0, 4).duplicates, 1)

    def test_sweep_every_crash_offset(self):
        for s in (Strategy.AT_LEAST_ONCE_IDEMPOTENT, Strategy.TRANSACTIONAL):
            self.assertTrue(all(Simulation.run(s, self.log, at, 4).effectively_once() for at in range(10)), s)
        self.assertFalse(any(Simulation.run(Strategy.AT_LEAST_ONCE, self.log, at, 4).effectively_once() for at in range(10)))
        self.assertEqual(Simulation.run(Strategy.AT_LEAST_ONCE, self.log, 3, 1).duplicates, 1)
        self.assertEqual(Simulation.run(Strategy.AT_MOST_ONCE, self.log, 3, 1).lost, 0)
        self.assertEqual(Simulation.run(Strategy.AT_MOST_ONCE, self.log, 3, 2).lost, 0)
        self.assertEqual(Simulation.run(Strategy.AT_MOST_ONCE, self.log, 2, 2).lost, 1)


class StoreTest(unittest.TestCase):
    def test_post_if_new_and_transaction(self):
        store = Store()
        self.assertTrue(store.post_if_new(Posting("pay-1", Money.of_minor(100, USD))))
        self.assertFalse(store.post_if_new(Posting("pay-1", Money.of_minor(100, USD))))
        self.assertEqual(len(store.postings()), 1)
        self.assertEqual(store.balance(USD).minor_units, 100)
        t = store.begin()
        t.post(Posting("pay-2", Money.of_minor(250, USD)))
        t.commit_offset(7)
        self.assertEqual(len(store.postings()), 1)
        self.assertEqual(store.committed_offset(), 0)
        t.commit()
        self.assertEqual(len(store.postings()), 2)
        self.assertEqual(store.committed_offset(), 7)
        with self.assertRaises(RuntimeError):
            t.post(Posting("pay-3", Money.of_minor(1, USD)))
        with self.assertRaises(ValueError):
            store.commit_offset(-1)

    def test_context_manager_discards_buffer_on_crash(self):
        store = Store()
        with self.assertRaises(CrashError):
            with store.begin() as tx:
                tx.post(Posting("pay-9", Money.of_minor(5, USD)))
                tx.commit_offset(1)
                FaultInjector(0).after_side_effect(0)
        self.assertEqual(store.postings(), [])
        self.assertEqual(store.committed_offset(), 0)
        with store.begin() as tx:
            tx.post(Posting("pay-9", Money.of_minor(5, USD)))
            tx.commit_offset(1)
        self.assertEqual(store.committed_offset(), 1)


class FaultAndMoneyTest(unittest.TestCase):
    def test_fault_fires_once(self):
        f = FaultInjector(2)
        f.after_side_effect(1)
        with self.assertRaises(CrashError):
            f.after_side_effect(2)
        f.after_side_effect(2)
        self.assertTrue(f.fired())

    def test_money_discipline(self):
        self.assertEqual(Money.parse("19.99", USD).minor_units, 1999)
        with self.assertRaises(ArithmeticError):
            Money.parse("1.005", USD)
        with self.assertRaises(CurrencyMismatchError):
            Money.of_minor(1, USD).plus(Money.of_minor(1, Currency("EUR", 2)))
        with self.assertRaises(TypeError):
            Money(10.0, USD)


if __name__ == "__main__":
    unittest.main()
