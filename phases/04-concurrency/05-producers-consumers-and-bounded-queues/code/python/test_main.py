"""Tests for phases/04-concurrency/05-producers-consumers-and-bounded-queues. Run: python3 -m unittest -v"""

import threading
import time
import unittest
from typing import Callable

from main import USD, BoundedBlockingQueue, FakeClock, Money, Payment, Producer


def spin_until(what: str, predicate: Callable[[], bool]) -> None:
    """Yield until the predicate holds; fail loudly instead of hanging if it never does."""
    deadline = time.monotonic_ns() + 5_000_000_000
    while not predicate():
        if time.monotonic_ns() > deadline:
            raise RuntimeError(f"timed out waiting for {what}")
        time.sleep(0)


class FifoAndRingBufferTest(unittest.TestCase):
    def test_fifo_order_and_wraparound(self) -> None:
        q: BoundedBlockingQueue[str] = BoundedBlockingQueue(3)
        q.put("a"); q.put("b"); q.put("c")
        self.assertEqual(q.size(), 3)
        self.assertTrue(q.is_full())
        self.assertEqual((q.take(), q.take()), ("a", "b"))
        q.put("d"); q.put("e")                       # tail wraps around the array
        self.assertEqual((q.take(), q.take(), q.take()), ("c", "d", "e"))
        self.assertTrue(q.is_empty())
        self.assertEqual(q.stats().high_water_mark, 3)

    def test_offer_and_poll_without_waiting(self) -> None:
        q: BoundedBlockingQueue[str] = BoundedBlockingQueue(2)
        self.assertTrue(q.offer("x", 0) and q.offer("y", 0))
        self.assertFalse(q.offer("z", 0))
        self.assertEqual(q.size(), 2)
        self.assertEqual((q.poll(0), q.poll(0), q.poll(0)), ("x", "y", None))


class TimedOperationsWithFakeClockTest(unittest.TestCase):
    def setUp(self) -> None:
        self.clock = FakeClock(1_000)
        self.q: BoundedBlockingQueue[str] = BoundedBlockingQueue(1, self.clock)
        self.q.put("only")

    def test_timed_offer_on_full_queue_times_out_exactly(self) -> None:
        self.assertFalse(self.q.offer("more", 250_000_000))
        self.assertEqual(self.clock.nanos(), 1_000 + 250_000_000)
        self.assertEqual(self.q.stats().producer_waits, 1)
        self.assertEqual(self.q.stats().waiting_producers, 0)

    def test_timed_poll_returns_at_once_when_an_item_is_present(self) -> None:
        before = self.clock.nanos()
        self.assertEqual(self.q.poll(250_000_000), "only")
        self.assertEqual(self.clock.nanos(), before)

    def test_timed_poll_on_empty_queue_times_out_exactly(self) -> None:
        self.q.take()
        before = self.clock.nanos()
        self.assertIsNone(self.q.poll(40_000_000))
        self.assertEqual(self.clock.nanos(), before + 40_000_000)
        self.assertEqual(self.q.stats().consumer_waits, 1)

    def test_timed_offer_on_non_full_queue_does_not_wait(self) -> None:
        self.q.take()
        before = self.clock.nanos()
        self.assertTrue(self.q.offer("back", 250_000_000))
        self.assertEqual(self.clock.nanos(), before)


class ValidationTest(unittest.TestCase):
    def test_capacity_must_be_positive(self) -> None:
        with self.assertRaises(ValueError):
            BoundedBlockingQueue(0)
        with self.assertRaises(ValueError):
            BoundedBlockingQueue(-1)

    def test_none_item_is_rejected(self) -> None:
        with self.assertRaises(ValueError):
            BoundedBlockingQueue(1).put(None)


class BlockingAndWakeupTest(unittest.TestCase):
    def test_blocked_producer_wakes_when_a_slot_opens(self) -> None:
        q: BoundedBlockingQueue[str] = BoundedBlockingQueue(1)
        q.put("first")
        done = threading.Event()

        def produce() -> None:
            q.put("second")
            done.set()

        threading.Thread(target=produce, daemon=True).start()
        spin_until("producer to block", lambda: q.stats().waiting_producers == 1)
        self.assertEqual(q.size(), 1)
        self.assertEqual(q.take(), "first")
        self.assertTrue(done.wait(5))
        self.assertEqual(q.take(), "second")
        self.assertEqual(q.stats().waiting_producers, 0)

    def test_blocked_consumer_wakes_when_an_item_arrives(self) -> None:
        q: BoundedBlockingQueue[str] = BoundedBlockingQueue(1)
        received: list[str] = []
        done = threading.Event()

        def consume() -> None:
            received.append(q.take())
            done.set()

        threading.Thread(target=consume, daemon=True).start()
        spin_until("consumer to block", lambda: q.stats().waiting_consumers == 1)
        q.put("hello")
        self.assertTrue(done.wait(5))
        self.assertEqual(received, ["hello"])
        self.assertGreaterEqual(q.stats().consumer_waits, 1)


class BackpressureTest(unittest.TestCase):
    def test_fast_producer_is_held_at_the_bound(self) -> None:
        pipe: BoundedBlockingQueue[Payment] = BoundedBlockingQueue(4)
        script = Producer.script("auth", 20, 1_999)
        done = threading.Event()
        Producer(pipe, script, done).start()
        spin_until("producer to hit the bound", lambda: pipe.stats().waiting_producers == 1)
        self.assertEqual(pipe.size(), 4)
        posted = Money.of_minor(0, USD)
        order_ok = True
        for expected in script:
            p = pipe.take()
            order_ok &= p.id == expected.id
            posted = posted.plus(p.amount)
        self.assertTrue(done.wait(5))
        self.assertTrue(order_ok)
        self.assertEqual(posted, Money.of_minor(20 * 1_999, USD))
        self.assertEqual(pipe.stats().high_water_mark, 4)
        self.assertGreaterEqual(pipe.stats().producer_waits, 1)
        self.assertEqual(pipe.stats().waiting_producers, 0)

    def test_many_producers_many_consumers(self) -> None:
        shared: BoundedBlockingQueue[Payment] = BoundedBlockingQueue(8)
        producers, per_producer, consumers = 4, 500, 2
        total = producers * per_producer
        done_events = [threading.Event() for _ in range(producers)]
        for p, done in enumerate(done_events):
            Producer(shared, Producer.script(f"p{p}", per_producer, 1), done).start()
        seen: list[list[Payment]] = [[] for _ in range(consumers)]
        workers = []
        for mine in seen:
            def consume(target: list[Payment] = mine) -> None:
                for _ in range(total // consumers):
                    target.append(shared.take())
            worker = threading.Thread(target=consume, daemon=True)
            worker.start()
            workers.append(worker)
        for done in done_events:
            self.assertTrue(done.wait(10))
        for worker in workers:
            worker.join(10)
            self.assertFalse(worker.is_alive())
        ids = {p.id for consumed in seen for p in consumed}
        self.assertEqual(len(ids), total)
        self.assertTrue(shared.is_empty())
        for consumed in seen:
            last: dict[str, int] = {}
            for p in consumed:
                producer, seq = p.id.split("-")
                self.assertGreater(int(seq), last.get(producer, -1))
                last[producer] = int(seq)
        self.assertLessEqual(shared.stats().high_water_mark, 8)


if __name__ == "__main__":
    unittest.main()
