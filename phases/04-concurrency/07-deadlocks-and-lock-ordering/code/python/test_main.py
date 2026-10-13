"""Tests for phases/04-concurrency/07-deadlocks-and-lock-ordering. Run: python3 -m unittest -v"""

import threading
import time
import unittest

from main import (
    USD,
    Account,
    Backoff,
    DeadlockDetectedError,
    InsufficientFundsError,
    LockTracker,
    Money,
    TransferService,
    WaitForGraph,
    run_all,
)


def usd(minor: int) -> Money:
    return Money.of_minor(minor, USD)


class WaitForGraphTest(unittest.TestCase):
    def test_two_node_cycle(self) -> None:
        g = WaitForGraph()
        g.add_edge("T1", "T2")
        g.add_edge("T2", "T1")
        self.assertEqual(g.find_cycle(), ["T1", "T2"])

    def test_three_node_cycle_reported_from_smallest_id(self) -> None:
        g = WaitForGraph()
        g.add_edge("T2", "T3")
        g.add_edge("T3", "T1")
        g.add_edge("T1", "T2")
        self.assertEqual(g.find_cycle(), ["T1", "T2", "T3"])

    def test_chain_is_not_a_deadlock(self) -> None:
        g = WaitForGraph()
        g.add_edge("T1", "T2")
        g.add_edge("T2", "T3")
        g.add_edge("T4", "T3")
        self.assertEqual(g.find_cycle(), [])

    def test_waiter_behind_the_cycle_is_not_on_it(self) -> None:
        g = WaitForGraph()
        g.add_edge("T4", "T1")
        g.add_edge("T1", "T2")
        g.add_edge("T2", "T1")
        self.assertEqual(g.find_cycle(), ["T1", "T2"])

    def test_diamond_and_self_loop_and_empty(self) -> None:
        diamond = WaitForGraph()
        for waiter, holder in (("T1", "T2"), ("T1", "T3"), ("T2", "T4"), ("T3", "T4")):
            diamond.add_edge(waiter, holder)
        self.assertEqual(diamond.find_cycle(), [])
        loop = WaitForGraph()
        loop.add_edge("T1", "T1")
        self.assertEqual(loop.find_cycle(), ["T1"])
        self.assertEqual(WaitForGraph().find_cycle(), [])


class LockTrackerTest(unittest.TestCase):
    def test_scripted_two_transfer_deadlock(self) -> None:
        tracker = LockTracker()
        tracker.acquired("T1", "lock:A")
        tracker.acquired("T2", "lock:B")
        tracker.requesting("T1", "lock:B")
        self.assertEqual(tracker.detect(), [])
        with self.assertRaises(DeadlockDetectedError) as caught:
            tracker.requesting("T2", "lock:A")
        self.assertEqual(caught.exception.cycle, ["T1", "T2"])
        self.assertEqual(tracker.graph().successors("T2"), [])          # refused request is not left in the graph
        tracker.released("T2", "lock:B")
        tracker.acquired("T1", "lock:B")
        self.assertEqual(tracker.detect(), [])
        self.assertEqual(tracker.graph().nodes(), [])
        self.assertEqual(tracker.events()[0], "T1 acquired lock:A")
        self.assertIn("T2 aborted: deadlock ['T1', 'T2']", tracker.events())

    def test_re_requesting_a_held_lock_is_not_a_self_deadlock(self) -> None:
        tracker = LockTracker()
        tracker.acquired("T1", "lock:A")
        tracker.requesting("T1", "lock:A")
        self.assertEqual(tracker.detect(), [])


class LiveDeadlockTest(unittest.TestCase):
    def test_two_naive_transfers_forced_to_interleave_exactly_one_is_aborted(self) -> None:
        tracker = LockTracker()
        a = Account("A", USD, 10_000, tracker)
        b = Account("B", USD, 10_000, tracker)
        naive = TransferService(threading.Barrier(2).wait, Backoff(1))
        results: dict[str, int] = {"aborted": 0, "committed": 0}
        lock = threading.Lock()

        def attempt(frm: Account, to: Account, amount: Money, owner: str) -> None:
            try:
                naive.naive_transfer(frm, to, amount, owner)
                key = "committed"
            except DeadlockDetectedError:
                key = "aborted"
            with lock:
                results[key] += 1

        t1 = threading.Thread(target=attempt, args=(a, b, usd(2_500), "T1"), daemon=True)
        t2 = threading.Thread(target=attempt, args=(b, a, usd(1_000), "T2"), daemon=True)
        t1.start(); t2.start()
        t1.join(10); t2.join(10)
        self.assertFalse(t1.is_alive() or t2.is_alive(), "a thread hung: real deadlock")
        self.assertEqual(results, {"aborted": 1, "committed": 1})
        self.assertEqual(a.balance_minor() + b.balance_minor(), 20_000)
        self.assertIn((a.balance_minor(), b.balance_minor()), [(7_500, 12_500), (11_000, 9_000)])
        self.assertFalse(a.lock.is_locked() or b.lock.is_locked())


class OrderedTransferTest(unittest.TestCase):
    def test_contended_transfers_terminate_and_conserve(self) -> None:
        c = Account("C", USD, 100_000, None)
        d = Account("D", USD, 100_000, None)
        ordered = TransferService(lambda: time.sleep(0), Backoff(7))

        def work(t: int) -> None:
            for _ in range(500):
                if t % 2 == 0:
                    ordered.ordered_transfer(c, d, usd(1), f"w{t}")
                else:
                    ordered.ordered_transfer(d, c, usd(1), f"w{t}")

        run_all(8, work)
        self.assertEqual(c.balance_minor() + d.balance_minor(), 200_000)
        self.assertEqual((c.balance_minor(), d.balance_minor()), (100_000, 100_000))

    def test_locks_smaller_id_first_even_when_sending_from_the_larger(self) -> None:
        tracker = LockTracker()
        e = Account("E", USD, 1_000, tracker)
        f = Account("F", USD, 1_000, tracker)
        TransferService(lambda: None, Backoff(3)).ordered_transfer(f, e, usd(300), "T9")
        self.assertEqual(tracker.events()[0], "T9 requests lock:E")
        self.assertEqual((e.balance_minor(), f.balance_minor()), (1_300, 700))


class TryLockTransferTest(unittest.TestCase):
    def test_contended_transfers_terminate_and_conserve(self) -> None:
        g = Account("G", USD, 100_000, None)
        h = Account("H", USD, 100_000, None)
        trying = TransferService(lambda: time.sleep(0), Backoff(7))

        def work(t: int) -> None:
            for _ in range(200):                         # fewer than Java/Go: each yield is a syscall under the GIL
                if t % 2 == 0:
                    trying.try_lock_transfer(g, h, usd(1), f"w{t}")
                else:
                    trying.try_lock_transfer(h, g, usd(1), f"w{t}")

        run_all(8, work)
        self.assertEqual(g.balance_minor() + h.balance_minor(), 200_000)
        self.assertEqual(g.balance_minor(), 100_000)
        self.assertFalse(g.lock.is_locked() or h.lock.is_locked())

    def test_gives_up_after_max_attempts_and_holds_nothing_while_backing_off(self) -> None:
        i = Account("I", USD, 100, None)
        j = Account("J", USD, 100, None)
        holding = threading.Event()
        release = threading.Event()

        def hold_j() -> None:                            # another thread keeps J busy
            j.lock.lock("someone-else")
            holding.set()
            release.wait()
            j.lock.unlock("someone-else")

        holder = threading.Thread(target=hold_j, daemon=True)
        holder.start()
        holding.wait()
        counting = Backoff(1)
        gives_up = TransferService(lambda: None, counting, max_attempts=5)
        with self.assertRaises(RuntimeError):
            gives_up.try_lock_transfer(i, j, usd(1), "T5")
        self.assertEqual(counting.pauses(), 5)
        self.assertFalse(i.lock.is_locked())
        release.set()
        holder.join()


class BusinessRulesTest(unittest.TestCase):
    def setUp(self) -> None:
        self.k = Account("K", USD, 100, None)
        self.l = Account("L", USD, 100, None)
        self.service = TransferService(lambda: None, Backoff(1))

    def test_insufficient_funds_refused_and_nothing_changes(self) -> None:
        with self.assertRaises(InsufficientFundsError):
            self.service.ordered_transfer(self.k, self.l, usd(101), "T6")
        self.assertEqual((self.k.balance_minor(), self.l.balance_minor()), (100, 100))
        self.assertFalse(self.k.lock.is_locked() or self.l.lock.is_locked())

    def test_negative_and_same_account_refused(self) -> None:
        with self.assertRaises(ValueError):
            self.service.ordered_transfer(self.k, self.l, usd(-1), "T7")
        with self.assertRaises(ValueError):
            self.service.ordered_transfer(self.k, self.k, usd(1), "T8")
        self.assertEqual(str(usd(12_500)), "125.00 USD")


if __name__ == "__main__":
    unittest.main()
