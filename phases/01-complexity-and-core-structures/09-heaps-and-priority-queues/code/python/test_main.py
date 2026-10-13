"""Tests for phases/01-complexity-and-core-structures/09-heaps-and-priority-queues. Run: python3 -m unittest -v"""
import unittest

from main import (
    FakeClock,
    IndexedMinHeap,
    MinHeap,
    RetryScheduler,
    SeededRandom,
    natural_order,
    reverse_order,
)


class MinHeapTest(unittest.TestCase):
    def test_empty_heap(self):
        h: MinHeap[int] = MinHeap(natural_order)
        self.assertEqual(h.size(), 0)
        self.assertTrue(h.is_empty())
        with self.assertRaises(IndexError):
            h.peek()
        with self.assertRaises(IndexError):
            h.pop()

    def test_push_peek_pop(self):
        h: MinHeap[int] = MinHeap(natural_order)
        for v in (5, 3, 8, 1):
            h.push(v)
        self.assertEqual(h.peek(), 1)
        self.assertTrue(h.is_heap())
        self.assertEqual([h.pop() for _ in range(4)], [1, 3, 5, 8])
        self.assertTrue(h.is_empty())

    def test_duplicates_allowed(self):
        h: MinHeap[int] = MinHeap(natural_order)
        for _ in range(3):
            h.push(7)
        self.assertEqual(h.size(), 3)
        self.assertEqual(h.pop(), 7)

    def test_heapify_builds_valid_heap_and_pops_sorted(self):
        raw = [9, 4, 7, 1, 8, 2, 6, 3, 5, 0, 11, 10]
        built = MinHeap.heapify(raw, natural_order)
        self.assertEqual(built.size(), len(raw))
        self.assertTrue(built.is_heap())
        drained = [built.pop() for _ in range(len(raw))]
        self.assertEqual(drained, sorted(raw))

    def test_heapify_edge_cases(self):
        self.assertTrue(MinHeap.heapify([], natural_order).is_empty())
        self.assertEqual(MinHeap.heapify([42], natural_order).peek(), 42)

    def test_comparator_controls_order(self):
        rev = MinHeap.heapify([9, 4, 7, 1, 8, 2, 6, 3, 5, 0, 11, 10], reverse_order)
        self.assertEqual(rev.peek(), 11)
        self.assertTrue(rev.is_heap())

    def test_seeded_random_operations_keep_heap_property(self):
        rng = SeededRandom(42)
        stress: MinHeap[int] = MinHeap(natural_order)
        shadow: list[int] = []
        for _ in range(2_000):
            if stress.is_empty() or rng.next_int(3) != 0:
                v = rng.next_int(500)
                stress.push(v)
                shadow.append(v)
            else:
                popped = stress.pop()
                self.assertEqual(popped, min(shadow))
                shadow.remove(popped)
            self.assertTrue(stress.is_heap())
        self.assertEqual(stress.size(), len(shadow))


class IndexedMinHeapTest(unittest.TestCase):
    def test_stable_tie_break_is_insertion_order(self):
        ih: IndexedMinHeap[str] = IndexedMinHeap()
        for key in "ABC":
            ih.push(key, 50)
        ih.push("D", 10)
        self.assertTrue(ih.is_consistent())
        self.assertTrue(ih.contains("B"))
        self.assertEqual(ih.priority_of("B"), 50)
        self.assertFalse(ih.contains("Z"))
        self.assertEqual([ih.pop().key for _ in range(4)], ["D", "A", "B", "C"])
        self.assertTrue(ih.is_empty())
        self.assertTrue(ih.is_consistent())

    def test_duplicate_key_rejected(self):
        ih: IndexedMinHeap[str] = IndexedMinHeap()
        ih.push("A", 1)
        with self.assertRaises(ValueError):
            ih.push("A", 2)

    def test_decrease_key_moves_to_top_and_keeps_index(self):
        dk: IndexedMinHeap[str] = IndexedMinHeap()
        for i in range(20):
            dk.push(f"k{i}", 1_000 + i * 10)
        dk.decrease_key("k17", 5)
        self.assertEqual(dk.peek().key, "k17")
        self.assertEqual(dk.priority_of("k17"), 5)
        self.assertTrue(dk.is_consistent())
        dk.decrease_key("k3", 1_000)  # a new tie with k0, which was inserted earlier
        self.assertEqual([dk.pop().key for _ in range(3)], ["k17", "k0", "k3"])
        self.assertTrue(dk.is_consistent())

    def test_decrease_key_rejects_raise_and_unknown(self):
        dk: IndexedMinHeap[str] = IndexedMinHeap()
        dk.push("k5", 100)
        with self.assertRaises(ValueError):
            dk.decrease_key("k5", 999_999)
        with self.assertRaises(KeyError):
            dk.decrease_key("nope", 1)


class RetrySchedulerTest(unittest.TestCase):
    def test_backoff(self):
        self.assertEqual(RetryScheduler.backoff_millis(0), 1_000)
        self.assertEqual(RetryScheduler.backoff_millis(3), 8_000)
        self.assertEqual(RetryScheduler.backoff_millis(6), 60_000)
        self.assertEqual(RetryScheduler.backoff_millis(40), 60_000)
        with self.assertRaises(ValueError):
            RetryScheduler.backoff_millis(-1)

    def test_earliest_first_with_stable_ties_and_expedite(self):
        clock = FakeClock(1_000_000)
        s = RetryScheduler(clock)
        s.schedule_retry("A", 2)  # +4000
        s.schedule_retry("B", 0)  # +1000
        s.schedule_retry("C", 0)  # +1000, tie with B
        s.schedule_retry("D", 6)  # +60000
        self.assertEqual(s.poll_due(), [])
        self.assertEqual(s.pending(), 4)
        s.expedite("D", clock.now_millis() + 500)
        self.assertEqual(s.next_due_at_millis(), clock.now_millis() + 500)
        with self.assertRaises(ValueError):
            s.expedite("A", clock.now_millis() + 999_999)
        clock.advance(1_000)
        self.assertEqual([j.payment_id for j in s.poll_due()], ["D", "B", "C"])
        self.assertEqual(s.pending(), 1)
        self.assertFalse(s.is_scheduled("B"))
        self.assertTrue(s.is_scheduled("A"))
        clock.advance(2_999)
        self.assertEqual(s.poll_due(), [])  # one millisecond early is not due
        clock.advance(1)
        self.assertEqual(len(s.poll_due()), 1)
        self.assertEqual(s.pending(), 0)

    def test_clock_never_goes_backwards(self):
        with self.assertRaises(ValueError):
            FakeClock(0).advance(-1)


if __name__ == "__main__":
    unittest.main()
