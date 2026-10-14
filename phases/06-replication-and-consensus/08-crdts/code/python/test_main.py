"""Tests for phases/06-replication-and-consensus/08-crdts. Run: python3 -m unittest -v"""
import unittest

from main import (EUR, LONG_MAX, USD, CrdtLaws, CurrencyMismatchException, GCounter, GSet, JavaRandom, LWWRegister, Money,
                  NaiveSumCounter, ORSet, PNCounter, ReplicatedBalance, TagSource, random_g_counter, random_or_set,
                  random_pn_counter)


class CounterTest(unittest.TestCase):
    def test_g_counter(self):
        a, b = GCounter(), GCounter()
        a.increment("A", 3)
        b.increment("B", 5)
        b.increment("A", 1)
        self.assertTrue(a.value() == 3 and b.value() == 6)
        self.assertEqual(a.merge(b).value(), 8)
        self.assertEqual(a.merge(b), b.merge(a))
        self.assertEqual(a.merge(b).merge(b), a.merge(b))
        self.assertTrue(a.value() == 3 and b.value() == 6)
        with self.assertRaises(ValueError):
            a.increment("A", -1)

    def test_g_counter_overflow_is_an_error(self):
        big = GCounter()
        big.increment("A", LONG_MAX)
        with self.assertRaises(ArithmeticError):
            big.increment("A", 1)

    def test_pn_counter(self):
        p, q = PNCounter(), PNCounter()
        p.increment("A", 10)
        p.decrement("A", 4)
        q.increment("B", 2)
        q.decrement("B", 7)
        self.assertTrue(p.value() == 6 and q.value() == -5)
        self.assertEqual(p.merge(q).value(), 1)
        self.assertEqual(p.merge(q), q.merge(p))
        self.assertEqual(p.merge(p), p)

    def test_same_replica_id_loses_a_decrement(self):
        x, y = PNCounter(), PNCounter()
        x.decrement("same", 5)
        y.decrement("same", 5)
        self.assertEqual(x.merge(y).value(), -5)


class SetTest(unittest.TestCase):
    def test_g_set(self):
        s1, s2 = GSet(), GSet()
        s1.add("a")
        s2.add("b")
        s2.add("a")
        self.assertEqual(s1.merge(s2).elements(), {"a", "b"})
        self.assertEqual(s1.merge(s2), s2.merge(s1))

    def test_or_set_add_wins(self):
        tags_a, tags_b = TagSource("A"), TagSource("B")
        oa = ORSet()
        oa.add("book", tags_a)
        ob = oa.merge(ORSet())
        self.assertTrue(ob.contains("book"))
        oa.remove("book")
        self.assertFalse(oa.contains("book"))
        ob.add("book", tags_b)
        merged = oa.merge(ob)
        self.assertTrue(merged.contains("book"))
        self.assertEqual(oa.merge(ob), ob.merge(oa))
        self.assertEqual(merged.tombstone_count(), 1)
        oc = merged.merge(ORSet())
        oc.remove("book")
        self.assertTrue(not oc.contains("book") and oc.tombstone_count() == 2)
        stale = ORSet()
        stale.remove("never-added")
        self.assertTrue(not stale.contains("never-added") and stale.tombstone_count() == 0)
        self.assertFalse(oc.merge(ob).contains("book"))


class RegisterAndBalanceTest(unittest.TestCase):
    def test_lww_register(self):
        r1 = LWWRegister("v1", 10, "A")
        r2 = LWWRegister("v2", 12, "B")
        r3 = LWWRegister("v3", 12, "A")
        self.assertEqual(r1.merge(r2).value, "v2")
        self.assertEqual(r1.merge(r2), r2.merge(r1))
        self.assertTrue(r2.merge(r3).value == "v2" and r3.merge(r2).value == "v2")
        self.assertNotIn("v1", r1.merge(r2).value)

    def test_replicated_balance_goes_negative(self):
        east = ReplicatedBalance(USD)
        east.credit("east", Money.parse("100.00", USD))
        west = east.merge(ReplicatedBalance(USD))
        eighty = Money.parse("80.00", USD)
        if not east.balance().is_negative():
            east.debit("east", eighty)
        if not west.balance().is_negative():
            west.debit("west", eighty)
        self.assertTrue(east.balance().minor_units == 2000 and west.balance().minor_units == 2000)
        self.assertEqual(east.merge(west).balance().minor_units, -6000)
        self.assertEqual(east.merge(west), west.merge(east))
        self.assertEqual(str(east.merge(west).balance()), "-60.00 USD")
        with self.assertRaises(CurrencyMismatchException):
            east.credit("east", Money.parse("1.00", EUR))
        with self.assertRaises(ValueError):
            east.debit("east", Money.of_minor(-1, USD))


class LawsTest(unittest.TestCase):
    def test_laws_on_seeded_random_states(self):
        rng = JavaRandom(7)
        self.assertTrue(CrdtLaws.check(lambda: random_g_counter(rng), 300).all_hold())
        self.assertTrue(CrdtLaws.check(lambda: random_pn_counter(rng), 300).all_hold())
        sources = {"A": TagSource("A"), "B": TagSource("B"), "C": TagSource("C")}
        self.assertTrue(CrdtLaws.check(lambda: random_or_set(rng, sources), 300).all_hold())
        nl = CrdtLaws.check(lambda: NaiveSumCounter(1 + rng.next_int(100)), 100)
        self.assertTrue(nl.idempotent_failures == 100 and nl.commutative_failures == 0)
        replicas = [random_pn_counter(rng) for _ in range(6)]
        self.assertTrue(CrdtLaws.converges(replicas, rng))

    def test_java_random_matches_known_stream(self):
        # new java.util.Random(42).nextInt(100) twice yields 30, then 63 on every JDK.
        r = JavaRandom(42)
        self.assertEqual([r.next_int(100), r.next_int(100)], [30, 63])


if __name__ == "__main__":
    unittest.main()
