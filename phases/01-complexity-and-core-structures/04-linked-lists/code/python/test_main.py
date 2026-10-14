"""Tests for phases/01-complexity-and-core-structures/04-linked-lists. Run: python3 -m unittest -v"""
import unittest

from main import (Cell, DoublyLinkedList, IntrusiveList, PendingAuthorization, SinglyLinkedList, identity,
                  link_in_order, shuffled, sum_array, sum_list)


class SinglyLinkedListTest(unittest.TestCase):
    def test_add_get_remove(self):
        s = SinglyLinkedList()
        self.assertTrue(s.is_empty() and s.size() == 0)
        s.add_last(2)
        s.add_last(3)
        s.add_first(1)
        self.assertEqual(str(s), "[1, 2, 3]")
        self.assertEqual(s.size(), 3)
        self.assertTrue(s.get(0) == 1 and s.get(2) == 3)
        with self.assertRaises(IndexError):
            s.get(3)
        self.assertEqual(s.remove_first(), 1)
        self.assertEqual(s.size(), 2)
        self.assertTrue(s.remove_first_occurrence(2))
        self.assertEqual(str(s), "[3]")
        self.assertFalse(s.remove_first_occurrence(99))
        self.assertTrue(s.remove_first_occurrence(3))  # removing the tail resets tail
        self.assertTrue(s.is_empty())
        s.add_last(10)
        self.assertTrue(s.get(0) == 10 and s.size() == 1)

    def test_remove_first_on_empty(self):
        with self.assertRaises(IndexError):
            SinglyLinkedList().remove_first()

    def test_reverse(self):
        r = SinglyLinkedList()
        for i in range(1, 6):
            r.add_last(i)
        r.reverse()
        self.assertEqual(str(r), "[5, 4, 3, 2, 1]")
        r.add_last(0)  # tail must point at the old head
        self.assertEqual(str(r), "[5, 4, 3, 2, 1, 0]")


class DoublyLinkedListTest(unittest.TestCase):
    def test_sentinel_operations(self):
        d = DoublyLinkedList()
        self.assertTrue(d.invariant_holds() and d.first() is None and d.last() is None)
        a = d.add_last("a")
        c = d.add_last("c")
        b = d.insert_before(c, "b")
        self.assertTrue(str(d) == "[a, b, c]" and d.size() == 3)
        self.assertTrue(d.invariant_holds())
        self.assertTrue(d.first() is a and d.last() is c)
        self.assertEqual(d.unlink(b), "b")
        self.assertEqual(str(d), "[a, c]")
        self.assertTrue(b.owner is None and b.prev is None and b.next is None)
        with self.assertRaises(ValueError):
            d.unlink(b)  # twice
        with self.assertRaises(ValueError):
            DoublyLinkedList().unlink(a)  # foreign node
        d.add_first("z")
        self.assertEqual(str(d), "[z, a, c]")
        d.move_to_front(c)
        self.assertTrue(str(d) == "[c, z, a]" and d.invariant_holds())
        d.move_to_front(c)
        self.assertTrue(str(d) == "[c, z, a]" and d.size() == 3)  # no-op on head
        self.assertEqual(d.remove_first(), "c")
        self.assertEqual(d.remove_last(), "a")
        self.assertEqual(str(d), "[z]")
        d.remove_last()
        self.assertTrue(d.is_empty() and d.invariant_holds())
        with self.assertRaises(IndexError):
            d.remove_last()

    def test_splice(self):
        x = DoublyLinkedList()
        y = DoublyLinkedList()
        for i in range(1, 4):
            x.add_last(i)
        for i in range(4, 7):
            y.add_last(i)
        five = y.first().next
        x.splice_all(y)
        self.assertTrue(str(x) == "[1, 2, 3, 4, 5, 6]" and x.size() == 6)
        self.assertTrue(y.is_empty() and y.invariant_holds())
        self.assertIs(five.owner, x)  # ownership moved, so the node can be unlinked from the new list
        self.assertEqual(x.unlink(five), 5)
        self.assertTrue(x.invariant_holds())
        x.splice_all(y)
        self.assertEqual(x.size(), 5)  # splicing an empty list is a no-op
        with self.assertRaises(ValueError):
            x.splice_all(x)


class IntrusiveListTest(unittest.TestCase):
    def test_intrusive(self):
        p = IntrusiveList()
        p1 = PendingAuthorization("p1", 100, 1)
        p2 = PendingAuthorization("p2", 200, 2)
        p3 = PendingAuthorization("p3", 300, 3)
        p.add_last(p1)
        p.add_last(p2)
        p.add_last(p3)
        self.assertTrue(p.size() == 3 and p.first() is p1)
        self.assertTrue(p.contains(p2) and p1.is_linked())
        p.remove(p2)
        self.assertTrue(p.size() == 2 and not p.contains(p2) and not p2.is_linked())
        self.assertEqual("".join(e.id for e in p), "p1p3")
        with self.assertRaises(ValueError):
            p.remove(p2)  # twice
        with self.assertRaises(RuntimeError):
            IntrusiveList().add_last(p1)  # cannot be in two lists
        q = IntrusiveList()
        q.add_last(p2)
        self.assertTrue(q.contains(p2) and not p.contains(p2))
        with self.assertRaises(ValueError):
            p.remove(p2)  # wrong list


class BenchmarkHelpersTest(unittest.TestCase):
    def test_same_sum_regardless_of_link_order(self):
        n = 10_000
        arr = list(range(n))
        cells = [Cell(i) for i in range(n)]
        expected = n * (n - 1) // 2
        self.assertEqual(sum_array(arr), expected)
        seq = link_in_order(cells, identity(n))
        self.assertEqual(sum_list(seq), expected)
        perm = shuffled(n, 7)
        self.assertTrue(sorted(perm) == identity(n) and perm != identity(n))
        self.assertEqual(perm, shuffled(n, 7))
        seq.clear()
        self.assertTrue(seq.is_empty() and not cells[0].is_linked() and not cells[n - 1].is_linked())
        self.assertEqual(sum_list(link_in_order(cells, perm)), expected)

    def test_shuffle_matches_java_random(self):
        # Fisher-Yates over java.util.Random(42) on 10 elements, checked against the JDK.
        self.assertEqual(shuffled(10, 42), JAVA_SHUFFLE_10_SEED_42)


JAVA_SHUFFLE_10_SEED_42 = [4, 6, 2, 1, 7, 9, 8, 5, 3, 0]  # java.util.Random(42) Fisher-Yates


if __name__ == "__main__":
    unittest.main()
