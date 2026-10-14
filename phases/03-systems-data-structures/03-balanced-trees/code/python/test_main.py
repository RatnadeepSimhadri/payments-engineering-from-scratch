"""Tests for phases/03-systems-data-structures/03-balanced-trees. Run: python3 -m unittest -v"""
import unittest

from main import AvlTree, IllegalStateError, JavaRandom, NaiveBst, RedBlackTree, java_shuffle

N = 20_000


def is_sorted(xs):
    return all(xs[i - 1] < xs[i] for i in range(1, len(xs)))


def shuffled_keys():
    xs = list(range(N))
    java_shuffle(xs, JavaRandom(11))
    return xs


class NaiveTest(unittest.TestCase):
    def test_naive_degenerates(self):
        naive = NaiveBst()
        for i in range(1_000):
            naive.put(i, "x")
        self.assertEqual(naive.height(), 1_000)


class AvlRotationTest(unittest.TestCase):
    def test_ll(self):
        ll = AvlTree()
        for k, v in ((30, "c"), (20, "b"), (10, "a")):
            ll.put(k, v)
        self.assertEqual(ll.rotations(), 1)
        self.assertEqual(ll.height(), 2)
        self.assertEqual(ll.keys(), [10, 20, 30])

    def test_lr(self):
        lr = AvlTree()
        for k, v in ((30, "c"), (10, "a"), (20, "b")):
            lr.put(k, v)
        self.assertEqual(lr.rotations(), 2)
        self.assertTrue(lr.height() == 2 and lr.check_invariants())

    def test_rr_and_rl(self):
        rr = AvlTree()
        for k in (10, 20, 30):
            rr.put(k, "v")
        self.assertTrue(rr.rotations() == 1 and rr.height() == 2)
        rl = AvlTree()
        for k in (10, 30, 20):
            rl.put(k, "v")
        self.assertTrue(rl.rotations() == 2 and rl.height() == 2)


class AvlBulkTest(unittest.TestCase):
    def test_sorted_inserts(self):
        avl = AvlTree()
        for i in range(N):
            avl.put(i, i)
        self.assertTrue(avl.check_invariants())
        self.assertLessEqual(avl.height(), AvlTree.height_bound(N))
        self.assertEqual(avl.size(), N)
        self.assertTrue(is_sorted(avl.keys()))
        avl.put(5, 500)
        self.assertTrue(avl.get(5) == 500 and avl.size() == N)

    def test_random_inserts_and_deletes(self):
        avl = AvlTree()
        for k in shuffled_keys():
            avl.put(k, k * 2)
        self.assertTrue(avl.check_invariants())
        self.assertTrue(avl.get(777) == 1554 and avl.get(N - 1) == 2 * (N - 1) and avl.get(N) is None)
        for i in range(0, N, 3):
            avl.remove(i)
        expected = N - (N + 2) // 3
        self.assertEqual(avl.size(), expected)
        self.assertTrue(avl.check_invariants())
        self.assertTrue(not avl.contains_key(0) and not avl.contains_key(999)
                        and avl.contains_key(1) and avl.contains_key(1000))
        avl.remove(-1)
        self.assertTrue(avl.size() == expected and avl.check_invariants())

    def test_remove_last_key(self):
        tiny = AvlTree()
        tiny.put(1, "a")
        tiny.remove(1)
        self.assertTrue(tiny.size() == 0 and tiny.height() == 0 and tiny.get(1) is None)


class AvlOrderedTest(unittest.TestCase):
    def test_range_and_ceiling(self):
        ord_ = AvlTree()
        for k in (50, 20, 70, 10, 30, 60, 80, 25, 35):
            ord_.put(k, "v")
        self.assertEqual(ord_.range(22, 60), [25, 30, 35, 50, 60])
        self.assertEqual(ord_.range(81, 100), [])
        self.assertEqual(ord_.ceiling_key(30), 30)
        self.assertEqual(ord_.ceiling_key(31), 35)
        self.assertIsNone(ord_.ceiling_key(81))


class RedBlackCasesTest(unittest.TestCase):
    def test_case1_recolor(self):
        rb = RedBlackTree()
        rb.put(10, "a")
        self.assertTrue(rb.check_invariants() and rb.black_height() == 1)
        rb.put(5, "b")
        rb.put(15, "c")
        self.assertTrue(rb.rotations() == 0 and rb.check_invariants())
        rb.put(1, "d")
        self.assertTrue(rb.rotations() == 0 and rb.check_invariants() and rb.black_height() == 2)

    def test_case3_and_case2(self):
        rb3 = RedBlackTree()
        for k in (10, 20, 30):
            rb3.put(k, "v")
        self.assertTrue(rb3.rotations() == 1 and rb3.height() == 2 and rb3.check_invariants())
        rb2 = RedBlackTree()
        for k in (10, 30, 20):
            rb2.put(k, "v")
        self.assertTrue(rb2.rotations() == 2 and rb2.height() == 2 and rb2.check_invariants())


class RedBlackBulkTest(unittest.TestCase):
    def test_sorted_and_random(self):
        avl_sorted = AvlTree()
        rb_sorted = RedBlackTree()
        for i in range(N):
            avl_sorted.put(i, i)
            rb_sorted.put(i, i)
        self.assertTrue(rb_sorted.check_invariants())
        self.assertLessEqual(rb_sorted.height(), RedBlackTree.height_bound(N))
        self.assertGreaterEqual(rb_sorted.height(), avl_sorted.height())
        self.assertTrue(is_sorted(rb_sorted.keys()) and len(rb_sorted.keys()) == N)

        keys = shuffled_keys()
        avl_random = AvlTree()
        rb_random = RedBlackTree()
        for k in keys:
            avl_random.put(k, k * 2)
            rb_random.put(k, k)
        for i in range(0, N, 3):
            avl_random.remove(i)
        self.assertTrue(rb_random.check_invariants())
        self.assertLess(rb_random.rotations(), avl_random.rotations())
        rb_random.put(42, -1)
        self.assertTrue(rb_random.get(42) == -1 and rb_random.size() == N)
        # Agreement with the reference ordered map: sorted(dict) plays java.util.TreeMap's role.
        reference = {k: k for k in keys}
        self.assertEqual(rb_random.keys(), sorted(reference))


class InvariantCheckerTest(unittest.TestCase):
    def test_detects_unbalanced(self):
        chain = AvlTree.Node(1, "a")
        chain.right = AvlTree.Node(2, "b")
        chain.right.right = AvlTree.Node(3, "c")
        chain.right.height = 2
        chain.height = 3
        with self.assertRaises(IllegalStateError):
            AvlTree.verify(chain, None, None)

    def test_detects_stale_height(self):
        stale = AvlTree.Node(2, "b")
        stale.left = AvlTree.Node(1, "a")
        stale.height = 1
        with self.assertRaises(IllegalStateError):
            AvlTree.verify(stale, None, None)

    def test_detects_order_violation(self):
        disordered = AvlTree.Node(2, "b")
        disordered.left = AvlTree.Node(5, "x")
        disordered.height = 2
        with self.assertRaises(IllegalStateError):
            AvlTree.verify(disordered, None, None)


if __name__ == "__main__":
    unittest.main()
