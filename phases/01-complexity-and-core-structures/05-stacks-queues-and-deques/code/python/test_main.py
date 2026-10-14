"""Tests for phases/01-complexity-and-core-structures/05-stacks-queues-and-deques. Run: python3 -m unittest -v"""
import collections
import unittest

from main import ArrayStack, OverflowPolicy, RingBuffer, brackets_balanced


class JavaRandom:
    """java.util.Random(seed) bit for bit, so the model-based test replays the same 2,000 operations as Java."""

    def __init__(self, seed):
        self._seed = (seed ^ 0x5DEECE66D) & ((1 << 48) - 1)

    def next(self, bits):
        self._seed = (self._seed * 0x5DEECE66D + 0xB) & ((1 << 48) - 1)
        r = self._seed >> (48 - bits)
        return r - (1 << 32) if r >= (1 << 31) else r

    def next_int(self, bound):
        r = self.next(31)
        m = bound - 1
        if bound & m == 0:
            return (bound * r) >> 31
        u = r
        r = u % bound
        while u - r + m >= (1 << 31):
            u = self.next(31)
            r = u % bound
        return r


def drain(r):
    out = []
    while (v := r.poll_first()) is not None:
        out.append(str(v))
    return " ".join(out)


class StackTest(unittest.TestCase):
    def test_lifo(self):
        st = ArrayStack(2)
        self.assertTrue(st.is_empty() and st.size() == 0)
        st.push(1)
        st.push(2)
        st.push(3)
        self.assertTrue(st.capacity() == 4 and st.size() == 3)
        self.assertEqual(st.peek(), 3)
        self.assertEqual([st.pop(), st.pop(), st.pop()], [3, 2, 1])
        with self.assertRaises(IndexError):
            st.pop()
        with self.assertRaises(IndexError):
            st.peek()
        st.push(7)
        st.push(8)
        self.assertEqual("".join(str(v) for v in st), "87")  # iterates top-down

    def test_brackets(self):
        self.assertTrue(brackets_balanced('{"a":[1,2,{"b":3}]}') and brackets_balanced(""))
        self.assertTrue(not brackets_balanced('{"a":[1,2}') and not brackets_balanced(")(")
                        and not brackets_balanced("(("))


class RingBufferTest(unittest.TestCase):
    def test_basics_wraparound_and_drain(self):
        r = RingBuffer(4, OverflowPolicy.REJECT)
        self.assertTrue(r.is_empty() and not r.is_full() and r.size() == 0)
        self.assertTrue(r.poll_first() is None and r.poll_last() is None
                        and r.peek_first() is None and r.peek_last() is None)
        self.assertTrue(r.offer_last("a") and r.offer_last("b") and r.offer_last("c") and r.offer_last("d"))
        self.assertTrue(r.is_full() and r.size() == 4 and not r.is_empty())  # no wasted slot
        self.assertTrue(not r.offer_last("e") and r.size() == 4)  # REJECT
        self.assertTrue(r.peek_first() == "a" and r.peek_last() == "d")
        self.assertTrue(r.poll_first() == "a" and r.poll_first() == "b")
        self.assertTrue(r.head_sequence() == 2 and r.tail_sequence() == 4 and r.size() == 2)
        # wraparound
        self.assertTrue(r.offer_last("e") and r.offer_last("f") and r.index_of(4) == 0 and r.index_of(5) == 1)
        self.assertTrue(str(r).startswith("[c, d, e, f]"))
        self.assertTrue(r.get(0) == "c" and r.get(2) == "e" and r.get(3) == "f")
        with self.assertRaises(IndexError):
            r.get(4)
        self.assertEqual("".join(r), "cdef")  # iterator follows sequences across the wrap
        self.assertTrue(r.poll_last() == "f" and r.peek_last() == "e" and r.tail_sequence() == 5)
        self.assertEqual(drain(r), "c d e")
        self.assertTrue(r.is_empty())
        self.assertTrue(r.head_sequence() == 5 and r.tail_sequence() == 5)

    def test_matches_model_deque_over_2000_random_operations(self):
        cyc = RingBuffer(3, OverflowPolicy.REJECT)
        model = collections.deque()
        rnd = JavaRandom(11)
        ok = True
        i = 0
        while i < 2000 and ok:
            op = rnd.next_int(4)
            if op == 0:
                accepted = cyc.offer_last(i)
                if len(model) < 3:
                    model.append(i)
                    ok &= accepted
                else:
                    ok &= not accepted
            elif op == 1:
                accepted = cyc.offer_first(i)
                if len(model) < 3:
                    model.appendleft(i)
                    ok &= accepted
                else:
                    ok &= not accepted
            elif op == 2:
                ok &= cyc.poll_first() == (model.popleft() if model else None)
            else:
                ok &= cyc.poll_last() == (model.pop() if model else None)
            ok &= cyc.size() == len(model) and cyc.peek_first() == (model[0] if model else None)
            i += 1
        self.assertTrue(ok)

    def test_deque_operations(self):
        d = RingBuffer(4, OverflowPolicy.REJECT)
        d.offer_last(2)
        d.offer_last(3)
        # offer_first when head is 0 rebases sequences and keeps order
        self.assertTrue(d.offer_first(1) and d.offer_first(0) and d.get(0) == 0 and d.get(3) == 3 and d.size() == 4)
        self.assertTrue(d.is_full() and not d.offer_first(9))
        self.assertTrue(d.poll_last() == 3 and d.poll_first() == 0 and d.size() == 2
                        and d.peek_first() == 1 and d.peek_last() == 2)

    def test_drop_oldest(self):
        drop = RingBuffer(4, OverflowPolicy.DROP_OLDEST)
        for i in range(10):
            self.assertTrue(drop.offer_last(i), f"drop offer always succeeds {i}")
        self.assertTrue(str(drop).startswith("[6, 7, 8, 9]") and drop.size() == 4)
        self.assertTrue(drop.dropped() == 6 and drop.head_sequence() == 6 and drop.tail_sequence() == 10)
        # offer_first when full drops the newest
        self.assertTrue(drop.offer_first(5) and str(drop).startswith("[5, 6, 7, 8]") and drop.dropped() == 7)
        drop.clear()
        self.assertTrue(drop.is_empty() and drop.head_sequence() == 0 and drop.poll_first() is None)

    def test_construction(self):
        with self.assertRaises(ValueError):
            RingBuffer(0, OverflowPolicy.REJECT)
        with self.assertRaises(ValueError):
            ArrayStack(0)


if __name__ == "__main__":
    unittest.main()
