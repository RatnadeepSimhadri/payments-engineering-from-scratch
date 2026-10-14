"""Tests for phases/13-risk-fraud-and-compliance/06-aml-transaction-monitoring. Run: python3 -m unittest -v"""
import itertools
import unittest

from main import (T0, USD, Channel, Direction, Hours, MonitoringEngine, Money, Scenario, ScenarioKind, Severity,
                  SyntheticGenerator, Transaction, jdiv, standard_scenarios)

_seq = itertools.count(1)

STR = Scenario.structuring("STR", 1_000_000, 8_000, 3 * Hours.DAY, 3)
RPT = Scenario.rapid_pass_through("RPT", 1_000_000, 9_000, 24 * Hours.HOUR)
VEL = Scenario.velocity("VEL", 6 * Hours.HOUR, 6, 4, 150)


def tx(account, d, ch, minor, ts, cp=None):
    n = next(_seq)
    return Transaction(f"t{n}", account, cp if cp is not None else f"cp-{n}", d, ch, Money.of_minor(minor, USD), ts)


def run(s, txs):
    return MonitoringEngine([s]).evaluate(txs)


def cash(account, minor, ts):
    return tx(account, Direction.IN, Channel.CASH, minor, ts)


def ten_deposits():
    return [cash("s", 990_000, T0 + i * 7 * Hours.HOUR) for i in range(10)]


def mule_case():
    return [tx("m", Direction.IN, Channel.WIRE, 5_000_000, T0, "shell"),
            tx("m", Direction.OUT, Channel.WIRE, 2_000_000, T0 + 2 * Hours.HOUR, "b1"),
            tx("m", Direction.OUT, Channel.WIRE, 2_000_000, T0 + 5 * Hours.HOUR, "b2"),
            tx("m", Direction.OUT, Channel.WIRE, 600_000, T0 + 8 * Hours.HOUR, "b3")]


def burst_case():
    return [tx("v", Direction.OUT, Channel.ACH, 1_000, T0 + i * 300, f"peer-{i}") for i in range(20)]


class BasicsTest(unittest.TestCase):
    def test_money_and_validation(self):
        self.assertEqual(Money.of_minor(1_000_000, USD).basis_points(8_000).minor_units, 800_000)
        self.assertEqual(Money.of_minor(4_850_000, USD).basis_points(9_000).minor_units, 4_365_000)
        with self.assertRaises(ValueError):
            tx("a", Direction.IN, Channel.CASH, 0, T0)
        with self.assertRaises(ValueError):
            Scenario("X", ScenarioKind.STRUCTURING, Severity.HIGH, {}, "").param("thresholdMinor")
        with self.assertRaises(ValueError):
            MonitoringEngine([STR, STR])

    def test_java_integer_semantics(self):
        self.assertEqual(jdiv(-7, 2), -3)                                              # Java truncates; Python // gives -4
        self.assertEqual(Money.of_minor(-5, USD).basis_points(5_000).minor_units, -2)   # -2.5 half-even -> -2


class StructuringTest(unittest.TestCase):
    def test_textbook_case(self):
        a = run(STR, ten_deposits())
        self.assertTrue(len(a) == 1 and a[0].kind == ScenarioKind.STRUCTURING)
        self.assertEqual(len(a[0].evidence), 10)
        self.assertEqual(a[0].total().minor_units, 9_900_000)
        self.assertTrue(a[0].severity == Severity.HIGH and a[0].score == 100)
        self.assertTrue("cash deposits" in a[0].narrative and "3 days" in a[0].narrative)

    def test_boundaries(self):
        at_threshold = [cash("b", 1_000_000, T0), cash("b", 1_000_000, T0 + Hours.HOUR), cash("b", 1_000_000, T0 + 2 * Hours.HOUR)]
        self.assertEqual(run(STR, at_threshold), [])
        below_floor = [cash("c", 799_999, T0), cash("c", 799_999, T0 + 1), cash("c", 799_999, T0 + 2)]
        self.assertEqual(run(STR, below_floor), [])
        two_only = [cash("d", 990_000, T0), cash("d", 990_000, T0 + 1)]
        self.assertEqual(run(STR, two_only), [])
        spread = [cash("e", 990_000, T0), cash("e", 990_000, T0 + 4 * Hours.DAY), cash("e", 990_000, T0 + 8 * Hours.DAY)]
        self.assertEqual(run(STR, spread), [])
        edge = [cash("f", 990_000, T0), cash("f", 990_000, T0 + Hours.DAY), cash("f", 990_000, T0 + 3 * Hours.DAY)]
        self.assertEqual(len(run(STR, edge)), 1)
        wires = [tx("g", Direction.IN, Channel.WIRE, 990_000, T0 + k) for k in range(3)]
        self.assertEqual(run(STR, wires), [])
        unsorted = [cash("h", 990_000, T0 + 2 * Hours.HOUR), cash("h", 990_000, T0), cash("h", 990_000, T0 + Hours.HOUR)]
        self.assertTrue(len(run(STR, unsorted)) == 1 and run(STR, unsorted)[0].evidence[0].timestamp == T0)


class PassThroughAndVelocityTest(unittest.TestCase):
    def test_rapid_pass_through(self):
        ra = run(RPT, mule_case())
        self.assertTrue(len(ra) == 1 and ra[0].kind == ScenarioKind.RAPID_PASS_THROUGH)
        self.assertEqual(len(ra[0].evidence), 4)
        self.assertGreater(ra[0].score, 60)
        slow = [tx("n", Direction.IN, Channel.WIRE, 5_000_000, T0, "shell"), tx("n", Direction.OUT, Channel.WIRE, 4_600_000, T0 + 25 * Hours.HOUR, "b1")]
        self.assertEqual(run(RPT, slow), [])
        partial = [tx("o", Direction.IN, Channel.WIRE, 5_000_000, T0, "shell"), tx("o", Direction.OUT, Channel.WIRE, 4_400_000, T0 + Hours.HOUR, "b1")]
        self.assertEqual(run(RPT, partial), [])
        card_spend = [tx("p", Direction.IN, Channel.ACH, 5_000_000, T0, "employer"), tx("p", Direction.OUT, Channel.CARD, 4_800_000, T0 + Hours.HOUR, "merchant")]
        self.assertEqual(run(RPT, card_spend), [])
        small = [tx("q", Direction.IN, Channel.ACH, 50_000, T0, "friend"), tx("q", Direction.OUT, Channel.ACH, 50_000, T0 + 60, "other")]
        self.assertEqual(run(RPT, small), [])

    def test_velocity(self):
        va = run(VEL, burst_case())
        self.assertTrue(len(va) == 1 and va[0].kind == ScenarioKind.VELOCITY)
        self.assertEqual(len(va[0].evidence), 20)
        same_cp = [tx("w", Direction.OUT, Channel.ACH, 1_000, T0 + i * 300, "landlord") for i in range(9)]
        self.assertEqual(run(VEL, same_cp), [])
        slow_burst = [tx("x", Direction.OUT, Channel.ACH, 1_000, T0 + i * 2 * Hours.HOUR, f"peer-{i}") for i in range(20)]
        self.assertEqual(run(VEL, slow_burst), [])


class EngineTest(unittest.TestCase):
    def test_ordering_and_independence(self):
        mixed = ten_deposits() + mule_case() + burst_case()
        all_alerts = MonitoringEngine([STR, RPT, VEL]).evaluate(mixed)
        self.assertEqual(len(all_alerts), 3)
        self.assertTrue(all_alerts[0].severity == Severity.HIGH and all_alerts[2].severity == Severity.MEDIUM)
        self.assertEqual({a.account_id for a in all_alerts}, {"s", "m", "v"})
        self.assertIn("10 transactions", next(a for a in all_alerts if a.kind == ScenarioKind.STRUCTURING).narrative)
        self.assertEqual(MonitoringEngine([STR, RPT, VEL]).evaluate([]), [])


class PopulationTest(unittest.TestCase):
    def test_planted_patterns(self):
        g1, g2 = SyntheticGenerator(7), SyntheticGenerator(7)
        p1, p2 = g1.population(60, T0), g2.population(60, T0)
        self.assertTrue(len(p1) == len(p2) and p1[-1] == p2[-1])
        self.assertTrue(1500 < len(p1) < 2200)
        pa = MonitoringEngine(standard_scenarios()).evaluate(p1)
        found = {a.account_id for a in pa if a.kind.value == g1.planted.get(a.account_id)}
        self.assertEqual(found, set(g1.planted))
        fps = sum(1 for a in pa if a.kind.value != g1.planted.get(a.account_id))
        self.assertTrue(fps >= 1 and any(a.account_id == "acct-laundromat" for a in pa))
        self.assertFalse(any(a.account_id.startswith("acct-0") for a in pa))
        tighter = Scenario.structuring("STR-TIGHT", 1_000_000, 9_300, 3 * Hours.DAY, 3)
        tight = MonitoringEngine([tighter]).evaluate(p1)
        self.assertTrue(any(a.account_id == "acct-structurer" for a in tight) and not any(a.account_id == "acct-laundromat" for a in tight))
        other = SyntheticGenerator(99).population(60, T0)
        self.assertTrue(len(other) != len(p1) or other != p1)

    def test_population_matches_the_java_generator(self):
        # java.util.Random(7) drives the generator; these figures are what the Java demo prints.
        g = SyntheticGenerator(7)
        p = g.population(60, T0)
        self.assertEqual(len(p), 1792)
        self.assertEqual(len(MonitoringEngine(standard_scenarios()).evaluate(p)), 4)


if __name__ == "__main__":
    unittest.main()
