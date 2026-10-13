"""Tests for phases/13-risk-fraud-and-compliance/02-rules-engine-and-velocity-checks. Run: python3 -m unittest -v"""
import unittest

from main import (
    DAY,
    EUR,
    HOUR,
    MINUTE,
    SECOND,
    USD,
    Action,
    CurrencyMismatchError,
    Decision,
    Dimension,
    ManualClock,
    Metric,
    Money,
    Operator,
    Rule,
    RulesEngine,
    Screener,
    SlidingWindowCounter,
    Strategy,
    Transaction,
    card_testing_rules,
    usd,
    velocity,
)

T0 = 1_700_000_040_000  # aligned to a minute boundary, so bucket edges are predictable


def tx(id: str, account: str, card: str, bin: str, country: str, card_age: int, amount: str) -> Transaction:
    return Transaction(id, account, card, bin, country, card_age, usd(amount))


def rule(name, field, op, value, action, priority, score) -> Rule:
    return Rule(name, field, op, value, action, priority, score, "test rule " + name)


class OperatorTest(unittest.TestCase):
    def test_equality_operators(self):
        self.assertTrue(Operator.EQ.apply("US", "US"))
        self.assertTrue(Operator.EQ.apply(usd("1.00"), Money.of_minor(100, USD)))
        self.assertTrue(Operator.NE.apply("US", "NG"))

    def test_ordering_on_ints(self):
        self.assertTrue(Operator.GT.apply(6, 5))
        self.assertFalse(Operator.GT.apply(5, 5))
        self.assertTrue(Operator.GTE.apply(5, 5))
        self.assertTrue(Operator.LT.apply(0, 1))
        self.assertFalse(Operator.LT.apply(1, 1))
        self.assertTrue(Operator.LTE.apply(1, 1))

    def test_ordering_on_money(self):
        self.assertTrue(Operator.GT.apply(usd("2000.01"), usd("2000.00")))
        self.assertTrue(Operator.LT.apply(usd("0.99"), usd("1.00")))

    def test_set_operators(self):
        self.assertTrue(Operator.IN.apply("NG", {"NG", "RU"}))
        self.assertTrue(Operator.NOT_IN.apply("US", {"NG", "RU"}))

    def test_type_mismatch_is_a_configuration_error(self):
        with self.assertRaises(ValueError):
            Operator.GT.apply("b", "a")
        with self.assertRaises(ValueError):
            Operator.GT.apply(5, usd("5.00"))
        with self.assertRaises(ValueError):
            Operator.IN.apply("US", "US")
        with self.assertRaises(CurrencyMismatchError):
            Operator.GT.apply(usd("1.00"), Money.parse("1.00", EUR))


class SlidingWindowCounterTest(unittest.TestCase):
    def test_bucket_width(self):
        c = SlidingWindowCounter(10 * MINUTE, 10, USD)
        self.assertEqual(c.bucket_millis, MINUTE)

    def test_unknown_key_counts_zero(self):
        c = SlidingWindowCounter(10 * MINUTE, 10, USD)
        self.assertEqual(c.count("card:none", T0), 0)
        self.assertEqual(c.sum("card:none", T0).minor_units, 0)

    def test_count_and_sum_inside_window(self):
        c = SlidingWindowCounter(10 * MINUTE, 10, USD)
        for i in range(6):
            c.record("card:a", usd("1.00"), T0 + i * 10 * SECOND)
        self.assertEqual(c.count("card:a", T0 + MINUTE), 6)
        self.assertEqual(c.sum("card:a", T0 + MINUTE), usd("6.00"))

    def test_window_expiry_is_exact_at_the_boundary(self):
        c = SlidingWindowCounter(10 * MINUTE, 10, USD)
        for i in range(6):
            c.record("card:a", usd("1.00"), T0 + i * 10 * SECOND)
        self.assertEqual(c.count("card:a", T0 + 10 * MINUTE - 1), 6)
        self.assertEqual(c.count("card:a", T0 + 10 * MINUTE), 0)

    def test_effective_window_is_between_w_minus_b_and_w(self):
        d = SlidingWindowCounter(10 * MINUTE, 10, USD)
        d.record("k", usd("1.00"), T0 + 59 * SECOND)
        self.assertEqual(d.count("k", T0 + 9 * MINUTE + 59 * SECOND), 1)
        self.assertEqual(d.count("k", T0 + 10 * MINUTE), 0)  # expires with its bucket, 9m01s after the event

    def test_reused_slot_is_reset_not_accumulated(self):
        e = SlidingWindowCounter(10 * MINUTE, 10, USD)
        e.record("k", usd("5.00"), T0)
        e.record("k", usd("7.00"), T0 + 10 * MINUTE)  # same slot index, next lap
        self.assertEqual(e.count("k", T0 + 10 * MINUTE), 1)
        self.assertEqual(e.sum("k", T0 + 10 * MINUTE), usd("7.00"))

    def test_keys_are_independent_and_evictable(self):
        c = SlidingWindowCounter(10 * MINUTE, 10, USD)
        c.record("card:a", usd("1.00"), T0)
        self.assertEqual(c.count("card:b", T0), 0)
        self.assertEqual(c.evict_idle(T0 + HOUR), 1)
        self.assertEqual(c.tracked_keys(), 0)

    def test_configuration_errors(self):
        c = SlidingWindowCounter(10 * MINUTE, 10, USD)
        with self.assertRaises(CurrencyMismatchError):
            c.record("k", Money.parse("1.00", EUR), T0)
        with self.assertRaises(ValueError):
            SlidingWindowCounter(10 * MINUTE + 1, 10, USD)


class VelocityServiceTest(unittest.TestCase):
    def test_enrich_adds_velocity_facts(self):
        v = velocity()
        f1 = v.enrich(tx("t1", "acct", "card", "411111", "US", 10, "700.00"), T0)
        self.assertEqual(f1["country"], "US")
        self.assertEqual(f1["card_age_days"], 10)
        self.assertEqual(f1["attempts_per_card_10m"], 1)  # the attempt counts itself
        self.assertEqual(f1["amount_per_account_1d"], usd("700.00"))
        f2 = v.enrich(tx("t2", "acct", "card", "411111", "US", 10, "700.00"), T0 + 5 * MINUTE)
        self.assertEqual(f2["attempts_per_card_10m"], 2)
        self.assertEqual(f2["amount_per_account_1d"], usd("1400.00"))

    def test_dimensions_are_keyed_independently(self):
        v = velocity()
        v.enrich(tx("t1", "acct", "card", "411111", "US", 10, "700.00"), T0)
        f3 = v.enrich(tx("t3", "acct", "card2", "411111", "US", 10, "1.00"), T0 + 5 * MINUTE)
        self.assertEqual(f3["attempts_per_card_10m"], 1)
        self.assertEqual(f3["amount_per_account_1d"], usd("701.00"))

    def test_duplicate_fact_names_rejected(self):
        v = velocity()
        with self.assertRaises(ValueError):
            v.define("attempts_per_card_10m", Dimension.CARD, Metric.COUNT, SlidingWindowCounter(MINUTE, 1, USD))


class RulesEngineFirstMatchTest(unittest.TestCase):
    def setUp(self):
        self.engine = RulesEngine([
            rule("review-country", "country", Operator.IN, {"NG"}, Action.REVIEW, 30, 30),
            rule("block-bin", "bin", Operator.EQ, "999999", Action.DECLINE, 5, 100),
            rule("large", "amount", Operator.GTE, usd("5000.00"), Action.REVIEW, 50, 30),
        ], Strategy.FIRST_MATCH, 50, 80)

    def test_rules_sorted_by_priority(self):
        self.assertEqual([r.name for r in self.engine.rules], ["block-bin", "review-country", "large"])

    def test_no_match_allows_and_traces_everything(self):
        d = self.engine.decide("tx", tx("tx", "a", "c", "411111", "US", 10, "10.00").facts(), T0)
        self.assertEqual(d.action, Action.ALLOW)
        self.assertIsNone(d.deciding_rule)
        self.assertEqual(d.fired(), [])
        self.assertEqual(len(d.traces), 3)

    def test_first_match_by_priority_decides_but_all_fired_are_reported(self):
        d = self.engine.decide("tx", tx("tx", "a", "c", "999999", "NG", 10, "10.00").facts(), T0)
        self.assertEqual(d.action, Action.DECLINE)
        self.assertEqual(d.deciding_rule, "block-bin")
        self.assertEqual(d.fired_rule_names(), ["block-bin", "review-country"])

    def test_priority_not_position_picks_the_decider(self):
        d = self.engine.decide("tx", tx("tx", "a", "c", "411111", "NG", 10, "6000.00").facts(), T0)
        self.assertEqual(d.deciding_rule, "review-country")
        self.assertEqual(d.action, Action.REVIEW)

    def test_absent_fact_never_fires(self):
        facts = tx("tx", "a", "c", "411111", "US", 10, "10.00").facts()
        del facts["country"]
        d = self.engine.decide("tx", facts, T0)
        self.assertEqual(d.action, Action.ALLOW)
        self.assertIsNone(d.traces[1].observed)


class RulesEngineScoreTest(unittest.TestCase):
    def setUp(self):
        self.engine = RulesEngine([
            rule("country", "country", Operator.IN, {"NG"}, Action.REVIEW, 30, 30),
            rule("fresh", "card_age_days", Operator.LT, 1, Action.REVIEW, 20, 25),
            rule("large", "amount", Operator.GTE, usd("5000.00"), Action.REVIEW, 50, 30),
        ], Strategy.SCORE, 50, 80)

    def test_scores_accumulate(self):
        self.assertEqual(self.engine.decide("t", tx("t", "a", "c", "4", "NG", 10, "10.00").facts(), T0).action, Action.ALLOW)
        two = self.engine.decide("t", tx("t", "a", "c", "4", "NG", 0, "10.00").facts(), T0)
        self.assertEqual((two.score, two.action), (55, Action.REVIEW))
        three = self.engine.decide("t", tx("t", "a", "c", "4", "NG", 0, "6000.00").facts(), T0)
        self.assertEqual((three.score, three.action), (85, Action.DECLINE))  # no single rule says DECLINE

    def test_threshold_is_inclusive(self):
        engine = RulesEngine([rule("a", "country", Operator.EQ, "NG", Action.REVIEW, 1, 50)], Strategy.SCORE, 50, 80)
        self.assertEqual(engine.decide("t", tx("t", "a", "c", "4", "NG", 10, "1.00").facts(), T0).action, Action.REVIEW)


class ExplanationTest(unittest.TestCase):
    def test_explanation_content(self):
        engine = RulesEngine(card_testing_rules(), Strategy.FIRST_MATCH, 50, 80)
        text = engine.decide("tx-9", tx("tx-9", "a", "c", "999999", "NG", 10, "10.00").facts(), T0).explain()
        self.assertTrue(text.startswith("decision for tx-9: DECLINE"))
        self.assertIn('by "bin-blocklist"', text)
        self.assertIn("bin=999999 IN [888888, 999999]", text)
        self.assertIn("issuer range is on the internal blocklist", text)
        self.assertIn("[pass] p50 large-ticket: amount=10.00 USD GTE 5000.00 USD", text)
        self.assertIn("attempts_per_card_10m=<absent>", text)


class CardTestingScenarioTest(unittest.TestCase):
    def setUp(self):
        self.clock = ManualClock(T0)
        self.screener = Screener(RulesEngine(card_testing_rules(), Strategy.FIRST_MATCH, 50, 80), velocity(), self.clock)

    def test_velocity_threshold_trips_exactly_at_n(self):
        self.assertEqual(self.screener.screen(tx("n1", "acct_alice", "tok_alice", "411111", "US", 400, "42.50")).action, Action.ALLOW)
        seen = []
        for i in range(6):
            self.clock.advance(20 * SECOND)
            seen.append(self.screener.screen(tx(f"m{i}", "acct_mallory", "tok_mallory", "411111", "US", 0, "1.00")).action)
        self.assertEqual(seen[:5], [Action.REVIEW] * 5)  # fresh card: review, not decline
        self.assertEqual(seen[5], Action.DECLINE)        # the sixth attempt inside ten minutes

    def test_burst_on_one_card_does_not_touch_another(self):
        for i in range(6):
            self.clock.advance(20 * SECOND)
            self.screener.screen(tx(f"m{i}", "acct_mallory", "tok_mallory", "411111", "US", 0, "1.00"))
        self.assertEqual(self.screener.screen(tx("a2", "acct_alice", "tok_alice", "411111", "US", 400, "19.99")).action, Action.ALLOW)

    def test_window_expiry_releases_the_card(self):
        for i in range(6):
            self.clock.advance(20 * SECOND)
            self.screener.screen(tx(f"m{i}", "acct_mallory", "tok_mallory", "411111", "US", 0, "1.00"))
        self.clock.advance(10 * MINUTE)
        cooled = self.screener.screen(tx("m7", "acct_mallory", "tok_mallory", "411111", "US", 0, "1.00"))
        self.assertEqual(cooled.action, Action.REVIEW)
        self.assertNotIn("card-testing-burst", cooled.fired_rule_names())

    def test_sum_velocity_trips_daily_spend(self):
        for i in range(3):
            self.clock.advance(MINUTE)
            self.screener.screen(tx(f"d{i}", "acct_dave", "tok_dave", "411111", "US", 90, "700.00"))
        self.clock.advance(MINUTE)
        spend = self.screener.screen(tx("d4", "acct_dave", "tok_dave", "411111", "US", 90, "1.00"))
        self.assertEqual(spend.action, Action.REVIEW)
        self.assertEqual(spend.deciding_rule, "daily-spend")


class ShadowModeTest(unittest.TestCase):
    """A shadow rule is evaluated and traced but never acts."""

    def setUp(self):
        self.live = rule("live-review", "country", Operator.EQ, "NG", Action.REVIEW, 20, 30)
        self.candidate = rule("candidate-decline", "amount", Operator.GTE, usd("100.00"), Action.DECLINE, 10, 60).as_shadow()
        self.facts = tx("t", "a", "c", "4", "NG", 1, "250.00").facts()

    def test_shadow_rule_with_higher_priority_does_not_decide(self):
        d = RulesEngine([self.live, self.candidate], Strategy.FIRST_MATCH, 50, 80).decide("t", self.facts, T0)
        self.assertEqual(d.action, Action.REVIEW)
        self.assertEqual(d.deciding_rule, "live-review")

    def test_shadow_rule_is_reported_separately(self):
        d = RulesEngine([self.live, self.candidate], Strategy.FIRST_MATCH, 50, 80).decide("t", self.facts, T0)
        self.assertEqual(d.fired_rule_names(), ["live-review"])
        self.assertEqual([t.rule for t in d.shadow_fired()], ["candidate-decline"])
        self.assertIn("[shadow] p10 candidate-decline", d.explain())

    def test_shadow_score_is_not_accumulated(self):
        d = RulesEngine([self.live, self.candidate], Strategy.SCORE, 50, 80).decide("t", self.facts, T0)
        self.assertEqual(d.score, 30)
        self.assertEqual(d.action, Action.ALLOW)

    def test_only_shadow_rules_decide_allow_by_default(self):
        d = RulesEngine([self.candidate], Strategy.FIRST_MATCH, 50, 80).decide("t", self.facts, T0)
        self.assertEqual(d.action, Action.ALLOW)
        self.assertIsNone(d.deciding_rule)
        self.assertTrue(self.candidate.describe().endswith("[shadow]"))


class ConfigurationErrorTest(unittest.TestCase):
    def test_duplicate_rule_names(self):
        with self.assertRaises(ValueError):
            RulesEngine([rule("x", "country", Operator.EQ, "US", Action.ALLOW, 1, 0),
                         rule("x", "country", Operator.EQ, "NG", Action.DECLINE, 2, 0)], Strategy.FIRST_MATCH, 50, 80)

    def test_thresholds_must_be_ordered(self):
        with self.assertRaises(ValueError):
            RulesEngine([], Strategy.SCORE, 80, 50)

    def test_rule_needs_an_explanation(self):
        with self.assertRaises(ValueError):
            Rule("x", "country", Operator.EQ, "US", Action.ALLOW, 1, 0, "")

    def test_type_mismatch_fails_loudly_at_evaluation(self):
        engine = RulesEngine([rule("bad", "country", Operator.GT, 5, Action.DECLINE, 1, 0)], Strategy.FIRST_MATCH, 50, 80)
        with self.assertRaises(ValueError):
            engine.decide("t", tx("t", "a", "c", "4", "US", 1, "1.00").facts(), T0)

    def test_clock_never_goes_backwards(self):
        with self.assertRaises(ValueError):
            ManualClock(T0).advance(-1)


if __name__ == "__main__":
    unittest.main()
