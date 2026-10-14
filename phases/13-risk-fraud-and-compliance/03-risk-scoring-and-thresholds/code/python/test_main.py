"""Tests for phases/13-risk-fraud-and-compliance/03-risk-scoring-and-thresholds. Run: python3 -m unittest -v"""
import unittest

from main import (USD, Calibration, ConfusionMatrix, CostModel, DecisionEngine, Evaluator, Features, JavaRandom, Money,
                  Outcome, ReviewBand, Scorecard, Scored, SyntheticDataset, ThresholdSelector, jf, java_round)


def s(p: float, fraud: bool) -> Scored:
    return Scored(p, fraud, Money.of_minor(5000, USD))


TOY = [s(0.9, True), s(0.8, False), s(0.7, True), s(0.4, True), s(0.3, False), s(0.1, False)]
COSTS = CostModel(Money.parse("105.00", USD), Money.parse("12.00", USD), Money.parse("4.00", USD), 0.10, 0.05)


class FeaturesAndScorecardTest(unittest.TestCase):
    def test_features(self):
        f = Features.of(0.5, True, False, 25, 45, 9)
        self.assertEqual(f.get(3), 1.0)
        self.assertAlmostEqual(f.get(4), 0.5, delta=1e-12)
        self.assertEqual(f.get(5), 1.0)
        self.assertEqual(Features.of(0, False, False, 1, 400, 0).get(4), 0.0)
        with self.assertRaises(ValueError):
            Features((1, 2))

    def test_sigmoid_logit_score_explain(self):
        self.assertEqual(Scorecard.sigmoid(0), 0.5)
        self.assertAlmostEqual(Scorecard.sigmoid(2) + Scorecard.sigmoid(-2), 1.0, delta=1e-12)
        hand = Scorecard([1, 2, 0, 0, 0, 0], -3)
        x = Features.of(0.5, True, False, 0, 1000, 0)
        self.assertAlmostEqual(hand.logit(x), -0.5, delta=1e-12)
        self.assertTrue(Scorecard.score_from_probability(1.0) == 999 and Scorecard.score_from_probability(0.0) == 0)
        ex = hand.explain(x, 2)
        self.assertEqual(len(ex.top), 2)
        self.assertTrue(ex.top[0].feature == "new_device" and ex.top[1].feature == "amount_log_ratio")

    def test_sigmoid_of_huge_negative_logit_is_zero_like_java(self):
        self.assertEqual(Scorecard.sigmoid(-1000.0), 0.0)

    def test_java_numeric_helpers(self):
        self.assertEqual(java_round(2.5), 3)
        self.assertEqual(java_round(-2.5), -2)
        self.assertEqual(jf(0.125, 2), "0.13")          # Java's %.2f rounds half up; Python's would print 0.12
        self.assertEqual(jf(1.5, 3, plus=True), "+1.500")
        r = JavaRandom(42)
        self.assertEqual([r.next_int(100), r.next_int(100)], [30, 63])


class EvaluationTest(unittest.TestCase):
    def test_confusion_matrix_by_hand(self):
        cm = Evaluator.confusion(TOY, 0.5)
        self.assertTrue(cm.tp == 2 and cm.fp == 1 and cm.tn == 2 and cm.fn == 1)
        self.assertAlmostEqual(cm.precision(), 2.0 / 3, delta=1e-12)
        self.assertAlmostEqual(cm.recall(), 2.0 / 3, delta=1e-12)
        self.assertAlmostEqual(cm.f1(), 2.0 / 3, delta=1e-12)
        self.assertEqual(Evaluator.confusion(TOY, 0.4).tp, 3)
        self.assertTrue(all(Evaluator.confusion(TOY, i / 100.0).total() == len(TOY) for i in range(101)))
        self.assertEqual(ConfusionMatrix(0, 0, 5, 0).precision(), 0.0)

    def test_auc(self):
        self.assertEqual(Evaluator.auc([s(0.9, True), s(0.8, True), s(0.2, False), s(0.1, False)]), 1.0)
        self.assertEqual(Evaluator.auc([s(0.1, True), s(0.9, False)]), 0.0)
        self.assertEqual(Evaluator.auc([s(0.5, True), s(0.5, False), s(0.5, True), s(0.5, False)]), 0.5)
        self.assertAlmostEqual(Evaluator.auc(TOY), 7.0 / 9, delta=1e-12)

    def test_cost_in_money(self):
        self.assertEqual(COSTS.cost(Evaluator.confusion(TOY, 0.5)), Money.parse("117.00", USD))

    def test_calibration(self):
        calibrated = [s(0.25, i < 2) for i in range(10)] + [s(0.75, i < 3) for i in range(4)]
        bins = Calibration.reliability(calibrated, 4)
        self.assertEqual(sum(b.count for b in bins), 14)
        self.assertAlmostEqual(bins[1].observed_rate, 0.2, delta=1e-12)
        self.assertAlmostEqual(Calibration.expected_calibration_error(bins), 10.0 / 14 * 0.05, delta=1e-12)
        self.assertEqual(Calibration.reliability([s(1.0, True)], 10)[9].count, 1)

    def test_cumulative(self):
        cum = ThresholdSelector.cumulative([2, 0, 3])
        self.assertTrue(len(cum) == 4 and cum[0] == 0 and cum[3] == 5 and cum[2] == 2)


class TrainedModelTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.all = SyntheticDataset.generate(42, 20_000, 0.02)
        cls.parts = SyntheticDataset.split(cls.all, 7, 0.7)
        cls.m1 = Scorecard.train(cls.parts[0], 400, 0.5)
        cls.scored = Evaluator.score(cls.m1, cls.parts[1])

    def test_generator_and_split(self):
        a = SyntheticDataset.generate(42, 500, 0.02)
        b = SyntheticDataset.generate(42, 500, 0.02)
        self.assertTrue(all(x.fraud == y.fraud and x.amount == y.amount and x.features.values == y.features.values
                            for x, y in zip(a, b)))
        self.assertTrue(len(self.parts[0]) == 14_000 and len(self.parts[1]) == 6_000)

    def test_training(self):
        m2 = Scorecard.train(self.parts[0], 400, 0.5)
        self.assertTrue(self.m1.weights == m2.weights and self.m1.bias == m2.bias)
        self.assertTrue(all(w > 0 for w in self.m1.weights))
        self.assertLess(self.m1.bias, -3)
        untrained = Scorecard([0.0] * Features.COUNT, 0)
        self.assertLess(self.m1.log_loss(self.parts[1]), untrained.log_loss(self.parts[1]))
        self.assertGreater(Evaluator.auc(self.scored), 0.95)

    def test_threshold_selection(self):
        by_f1 = ThresholdSelector.best_by_f1(self.scored, COSTS)
        by_cost = ThresholdSelector.best_by_cost(self.scored, COSTS)
        approve_all = COSTS.cost(Evaluator.confusion(self.scored, 1.01))
        self.assertLessEqual(by_cost.expected_cost.compare_to(by_f1.expected_cost), 0)
        self.assertLess(by_cost.expected_cost.compare_to(approve_all), 0)
        self.assertLessEqual(by_cost.value, by_f1.value)
        expensive_insults = CostModel(Money.parse("105.00", USD), Money.parse("400.00", USD), Money.parse("4.00", USD), 0.10, 0.05)
        self.assertGreater(ThresholdSelector.best_by_cost(self.scored, expensive_insults).value, by_cost.value)

    def test_review_band(self):
        by_cost = ThresholdSelector.best_by_cost(self.scored, COSTS)
        band = ThresholdSelector.best_band(self.scored, COSTS)
        self.assertLessEqual(band.low, band.high)
        self.assertEqual(band.approved + band.reviewed + band.declined, len(self.scored))
        self.assertLessEqual(band.expected_cost.compare_to(by_cost.expected_cost), 0)
        pricy_review = CostModel(Money.parse("105.00", USD), Money.parse("12.00", USD), Money.parse("500.00", USD), 0.10, 0.05)
        self.assertEqual(ThresholdSelector.best_band(self.scored, pricy_review).reviewed, 0)

    def test_decision_engine(self):
        engine = DecisionEngine(self.m1, ReviewBand(0.06, 0.13, 0, 0, 0, Money.of_minor(0, USD)))
        self.assertEqual(engine.decide(Features.of(0.0, False, False, 1, 500, 0)).outcome, Outcome.APPROVE)
        self.assertEqual(engine.decide(Features.of(2.0, True, True, 6, 2, 3)).outcome, Outcome.DECLINE)
        self.assertEqual(len(engine.decide(Features.of(2.0, True, True, 6, 2, 3)).explanation.top), 3)
        with self.assertRaises(ValueError):
            DecisionEngine(self.m1, ReviewBand(0.5, 0.2, 0, 0, 0, Money.of_minor(0, USD)))


if __name__ == "__main__":
    unittest.main()
