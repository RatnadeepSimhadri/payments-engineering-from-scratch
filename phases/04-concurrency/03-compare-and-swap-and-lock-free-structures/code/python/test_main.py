"""Tests for phases/04-concurrency/03-compare-and-swap-and-lock-free-structures. Run: python3 -m unittest -v"""
import threading
import unittest

from main import (AtomicLong, AtomicReference, AtomicStampedReference, CaptureJob, MichaelScottQueue, PooledStack,
                  Progress, StampedPooledStack, TreiberStack, on_spin_wait, stress_stack)


class TreiberBasicsTest(unittest.TestCase):
    def test_single_threaded(self):
        stack = TreiberStack()
        self.assertTrue(stack.is_empty() and stack.size() == 0)
        self.assertIsNone(stack.pop())
        self.assertIsNone(stack.peek())
        stack.push("a")
        stack.push("b")
        stack.push("c")
        self.assertEqual(stack.size(), 3)
        self.assertEqual([stack.pop(), stack.pop(), stack.pop()], ["c", "b", "a"])  # LIFO
        self.assertTrue(stack.is_empty() and stack.pop() is None)
        self.assertEqual(stack.cas_retries(), 0)  # no retries when single-threaded
        with self.assertRaises(TypeError):
            stack.push(None)


class TreiberStressTest(unittest.TestCase):
    def test_concurrent_pushes_then_mixed(self):
        stack = TreiberStack()
        threads, per_thread = 4, 20_000
        # Phase 1: everyone pushes, nobody pops -> exactly threads*per_thread distinct values must be present.
        start = threading.Event()

        def push_all(base):
            start.wait()
            for i in range(per_thread):
                stack.push(base + i)

        workers = [threading.Thread(target=push_all, args=(t * per_thread,)) for t in range(threads)]
        for w in workers:
            w.start()
        start.set()
        for w in workers:
            w.join()
        self.assertEqual(stack.size(), threads * per_thread)
        seen = set()
        v = stack.pop()
        while v is not None:
            seen.add(v)
            v = stack.pop()
        self.assertEqual(len(seen), threads * per_thread)

        # Phase 2: mixed push/pop; the stack must end empty.
        elapsed = stress_stack(stack, threads, per_thread)
        self.assertTrue(stack.is_empty())
        self.assertLess(elapsed, 10_000)


class AbaTest(unittest.TestCase):
    def test_aba_reproduced_deterministically(self):
        """The before_cas seam runs thread B's whole pop/pop/push inside thread A's read-to-CAS gap."""
        holder = []
        armed = [True]
        popped_by_b = []

        def thread_b():
            if not armed[0]:
                return
            armed[0] = False
            s = holder[0]
            popped_by_b.append(s.pop().payment_id)  # pay_003, N3 freed
            popped_by_b.append(s.pop().payment_id)  # pay_002, N2 freed
            s.push(CaptureJob("pay_004", 4_000))     # reuses N3: head is N3 again, with different contents

        stack = PooledStack(thread_b)
        holder.append(stack)
        stack.push(CaptureJob("pay_001", 1_000))
        stack.push(CaptureJob("pay_002", 2_000))
        stack.push(CaptureJob("pay_003", 3_000))
        self.assertEqual(stack.head_node().id, 3)

        got_by_a = stack.pop()
        self.assertEqual(popped_by_b, ["pay_003", "pay_002"])
        self.assertEqual(got_by_a.payment_id, "pay_004")  # the stale CAS succeeded: the reference matched
        self.assertEqual(stack.snapshot()[0], "N2=pay_002")  # pay_002 reappears although it was popped
        duplicate = stack.pop()
        self.assertTrue(duplicate is not None and duplicate.payment_id == "pay_002")  # double capture

    def test_aba_fixed_by_stamp(self):
        holder = []
        armed = [True]

        def thread_b():
            if not armed[0]:
                return
            armed[0] = False
            s = holder[0]
            s.pop()
            s.pop()
            s.push(CaptureJob("pay_004", 4_000))

        stack = StampedPooledStack(thread_b)
        holder.append(stack)
        stack.push(CaptureJob("pay_001", 1_000))
        stack.push(CaptureJob("pay_002", 2_000))
        stack.push(CaptureJob("pay_003", 3_000))
        self.assertEqual(stack.stamp(), 3)

        got_by_a = stack.pop()
        self.assertEqual(stack.stale_cas_rejected(), 1)
        self.assertEqual(got_by_a.payment_id, "pay_004")
        self.assertEqual(stack.snapshot(), ["N1=pay_001"])
        self.assertTrue(stack.pop().payment_id == "pay_001" and stack.pop() is None)  # pay_002 not popped twice
        self.assertEqual(stack.stamp(), 8)


class QueueTest(unittest.TestCase):
    def test_basics(self):
        q = MichaelScottQueue()
        self.assertTrue(q.is_empty() and q.dequeue() is None)
        q.enqueue("a")
        q.enqueue("b")
        q.enqueue("c")
        self.assertEqual([q.dequeue(), q.dequeue(), q.dequeue()], ["a", "b", "c"])  # FIFO
        self.assertTrue(q.is_empty() and q.dequeue() is None)
        q.enqueue("d")
        self.assertEqual(q.dequeue(), "d")  # the sentinel moved and enqueue still works
        with self.assertRaises(TypeError):
            q.enqueue(None)

    def test_stress_producers_and_consumers(self):
        q = MichaelScottQueue()
        producers, consumers, per_producer = 4, 4, 20_000
        total = producers * per_producer
        counts = {}
        counts_lock = threading.Lock()  # plays ConcurrentHashMap.merge
        consumed = AtomicLong()

        def produce(base):
            for i in range(per_producer):
                q.enqueue(base + i)

        def consume():
            while consumed.get() < total:
                v = q.dequeue()
                if v is not None:
                    with counts_lock:
                        counts[v] = counts.get(v, 0) + 1
                    consumed.increment_and_get()
                else:
                    on_spin_wait()

        workers = [threading.Thread(target=produce, args=(p * per_producer,)) for p in range(producers)]
        workers += [threading.Thread(target=consume) for _ in range(consumers)]
        for w in workers:
            w.start()
        for w in workers:
            w.join()
        self.assertEqual(len(counts), total)
        self.assertTrue(all(n == 1 for n in counts.values()))
        self.assertTrue(q.is_empty())


class ProgressTest(unittest.TestCase):
    def test_progress_table(self):
        levels = list(Progress)
        self.assertTrue(len(levels) == 4 and levels[0] is Progress.BLOCKING and levels[3] is Progress.WAIT_FREE)
        self.assertLess(Progress.LOCK_FREE.ordinal, Progress.WAIT_FREE.ordinal)


class AtomicsTest(unittest.TestCase):
    """The emulated CAS must compare identity and must not lose increments under real threads."""

    def test_cas_compares_identity_not_equality(self):
        a, b = [1], [1]  # equal, but different objects
        ref = AtomicReference(a)
        self.assertFalse(ref.compare_and_set(b, None))
        self.assertTrue(ref.compare_and_set(a, b) and ref.get() is b)
        stamped = AtomicStampedReference(a, 0)
        self.assertFalse(stamped.compare_and_set(a, b, 1, 2))  # right reference, stale stamp
        self.assertTrue(stamped.compare_and_set(a, b, 0, 1) and stamped.get() == (b, 1))

    def test_atomic_long_counts_exactly(self):
        counter = AtomicLong()

        def bump():
            for _ in range(20_000):
                counter.increment_and_get()

        workers = [threading.Thread(target=bump) for _ in range(4)]
        for w in workers:
            w.start()
        for w in workers:
            w.join()
        self.assertEqual(counter.get(), 80_000)


if __name__ == "__main__":
    unittest.main()
