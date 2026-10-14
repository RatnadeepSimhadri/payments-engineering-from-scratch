"""Tests for phases/06-replication-and-consensus/09-conflict-resolution. Run: python3 -m unittest -v"""
import unittest

from main import Cart, CaseFile, Dot, LWWStore, Ordering, SiblingStore, Versioned, VersionVector


class VersionVectorTest(unittest.TestCase):
    def test_ordering_and_merge(self):
        empty = VersionVector()
        a1 = empty.increment("A")
        a1b1 = a1.increment("B")
        a2 = a1.increment("A")
        self.assertTrue(empty.get("A") == 0 and a1.get("A") == 1)
        self.assertEqual(a1.compare(a1b1), Ordering.BEFORE)
        self.assertEqual(a1b1.compare(a1), Ordering.AFTER)
        self.assertEqual(a1.compare(a1.copy()), Ordering.EQUAL)
        self.assertTrue(a1b1.compare(a2) == Ordering.CONCURRENT and a2.compare(a1b1) == Ordering.CONCURRENT)
        self.assertEqual(empty.compare(a1), Ordering.BEFORE)
        m = a1b1.merge(a2)
        self.assertTrue(m.get("A") == 2 and m.get("B") == 1)
        self.assertTrue(m.dominates(a1b1) and m.dominates(a2))
        self.assertEqual(a1b1.merge(a2), a2.merge(a1b1))


class LWWTest(unittest.TestCase):
    def test_lww_loses_data(self):
        east, west = LWWStore(), LWWStore()
        east.put("k", "east-value", 1000, "east")
        west.put("k", "west-value", 1002, "west")
        self.assertEqual(east.merge(west).get("k"), "west-value")
        self.assertEqual(east.merge(west).get("k"), west.merge(east).get("k"))
        tie1, tie2 = LWWStore(), LWWStore()
        tie1.put("k", "from-A", 5, "A")
        tie2.put("k", "from-B", 5, "B")
        self.assertTrue(tie1.merge(tie2).get("k") == "from-B" and tie2.merge(tie1).get("k") == "from-B")
        skew = LWWStore()
        skew.put("k", "happened-first-slow-clock", 995, "slow")
        skew.put("k", "happened-second-fast-clock", 1000, "fast")
        skew.put("k", "happened-third-slow-clock", 997, "slow")
        self.assertEqual(skew.get("k"), "happened-second-fast-clock")
        self.assertEqual(skew.discarded(), 2)


class SiblingStoreTest(unittest.TestCase):
    def test_siblings(self):
        s_a, s_b = SiblingStore("A"), SiblingStore("B")
        v1 = s_a.put("k", "v1", VersionVector())
        self.assertTrue(v1.get("A") == 1 and s_a.sibling_count("k") == 1)
        s_b.merge_from(s_a)
        self.assertTrue(s_b.sibling_count("k") == 1 and s_b.get("k").values() == ["v1"])
        read_a = s_a.get("k")
        s_a.put("k", "v2-from-A", read_a.context)
        read_b = s_b.get("k")
        s_b.put("k", "v2-from-B", read_b.context)
        self.assertTrue(s_a.sibling_count("k") == 1 and s_a.get("k").values() == ["v2-from-A"])
        s_a.merge_from(s_b)
        self.assertTrue(s_a.sibling_count("k") == 2 and s_a.get("k").has_conflict())
        self.assertTrue(s_a.get("k").context.get("A") == 2 and s_a.get("k").context.get("B") == 1)
        s_b.merge_from(s_a)
        self.assertEqual(s_b.sibling_count("k"), 2)
        s_a.merge_from(s_b)
        self.assertEqual(s_a.sibling_count("k"), 2)
        conflicted = s_a.get("k")
        s_a.put("k", "v3-resolved", conflicted.context)
        self.assertTrue(s_a.sibling_count("k") == 1 and s_a.get("k").values() == ["v3-resolved"])
        self.assertTrue(s_a.get("k").context.get("A") == 3 and s_a.get("k").context.get("B") == 1)
        s_a.put_blind("k", "blind")
        self.assertEqual(s_a.sibling_count("k"), 2)

    def test_stale_context_write_becomes_sibling(self):
        stale = SiblingStore("A")
        first = stale.put("k", "one", VersionVector())
        stale.put("k", "two", first)
        stale.put("k", "stale-client", first)
        self.assertTrue(stale.sibling_count("k") == 2 and "two" in stale.get("k").values())


class MergeFunctionTest(unittest.TestCase):
    def test_case_file_resolution(self):
        cs = SiblingStore("east")
        c0 = cs.put("case", CaseFile.open(), VersionVector())
        cs.put("case", CaseFile.open().with_note("n1").with_status("FUNDS_HELD"), c0)
        cw = SiblingStore("west")
        cw.put("case", CaseFile.open().with_note("n2").with_status("UNDER_REVIEW"), c0)
        cs.merge_from(cw)
        self.assertEqual(cs.sibling_count("case"), 2)
        resolved = cs.resolve("case", CaseFile.UNION_NOTES_STRICTEST_STATUS)
        self.assertEqual(resolved.notes, {"n1", "n2"})
        self.assertEqual(resolved.status, "FUNDS_HELD")
        self.assertEqual(cs.sibling_count("case"), 1)
        self.assertTrue(cs.resolve("case", CaseFile.UNION_NOTES_STRICTEST_STATUS) == resolved and cs.sibling_count("case") == 1)
        self.assertIsNone(cs.resolve("nope", CaseFile.UNION_NOTES_STRICTEST_STATUS))
        with self.assertRaises(ValueError):
            CaseFile.open().with_status("LOST")

    def test_shopping_cart_anomaly(self):
        c_a, c_b = SiblingStore("A"), SiblingStore("B")
        c_a.put("cart", Cart.empty().add("book", 1).add("pen", 2), VersionVector())
        c_b.merge_from(c_a)
        ra = c_a.get("cart")
        c_a.put("cart", ra.siblings[0].value.remove("pen"), ra.context)
        rb = c_b.get("cart")
        c_b.put("cart", rb.siblings[0].value.add("book", 1), rb.context)
        c_a.merge_from(c_b)
        merged_cart = c_a.resolve("cart", Cart.UNION_MAX)
        self.assertEqual(merged_cart.items["book"], 2)
        self.assertIn("pen", merged_cart.items)
        self.assertEqual(Cart.UNION_MAX.merge([Cart.empty().add("x", 1), Cart.empty().add("x", 3)]),
                         Cart.UNION_MAX.merge([Cart.empty().add("x", 3), Cart.empty().add("x", 1)]))


class MaximalTest(unittest.TestCase):
    def test_maximal_and_obsolescence(self):
        a1 = VersionVector().increment("A")
        a1b1 = a1.increment("B")
        a2 = a1.increment("A")
        old = Versioned("old", Dot("A", 1), VersionVector())   # version {A=1}
        x = Versioned("x", Dot("B", 1), a1)                    # saw A:1 -> {A=1,B=1}
        y = Versioned("y", Dot("A", 2), a1)                    # saw A:1 -> {A=2}
        mx = SiblingStore.maximal([old, x, y])
        self.assertTrue(len(mx) == 2 and old not in mx and x in mx and y in mx)
        self.assertTrue(x.version() == a1b1 and y.version() == a2)
        self.assertTrue(old.obsoleted_by(x) and not x.obsoleted_by(y) and not y.obsoleted_by(x))


if __name__ == "__main__":
    unittest.main()
