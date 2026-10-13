// Tests for phases/13-risk-fraud-and-compliance/03-risk-scoring-and-thresholds (see docs/en.md).
// Run: javac -d .out *.java && java -cp .out MainTest
// Self-contained harness: counts failures and exits 1 if any check fails.
import java.util.ArrayList;
import java.util.List;

public final class MainTest {
    private static int passed = 0;
    private static final List<String> failures = new ArrayList<>();

    static void check(String name, boolean condition) {
        if (condition) passed++; else failures.add(name);
    }

    static void checkThrows(String name, Class<? extends Throwable> type, Runnable action) {
        try {
            action.run();
            failures.add(name + " (no exception)");
        } catch (Throwable t) {
            if (type.isInstance(t)) passed++; else failures.add(name + " (wrong exception " + t.getClass().getSimpleName() + ")");
        }
    }

    static boolean close(double a, double b, double eps) { return Math.abs(a - b) <= eps; }

    static Scored s(double p, boolean fraud) { return new Scored(p, fraud, Money.ofMinor(5000, Currency.USD)); }

    public static void main(String[] args) {
        Currency USD = Currency.USD;

        // --- features and the scorecard arithmetic ---
        Features f = Features.of(0.5, true, false, 25, 45, 9);
        check("velocity capped at 10 attempts -> 1.0", f.get(3) == 1.0);
        check("account_young is 1 - 45/90 = 0.5", close(f.get(4), 0.5, 1e-12));
        check("failed CVV capped at 5 -> 1.0", f.get(5) == 1.0);
        check("old account contributes 0", Features.of(0, false, false, 1, 400, 0).get(4) == 0.0);
        checkThrows("wrong feature count rejected", IllegalArgumentException.class, () -> new Features(new double[] {1, 2}));
        check("sigmoid(0) = 0.5", Scorecard.sigmoid(0) == 0.5);
        check("sigmoid is symmetric", close(Scorecard.sigmoid(2) + Scorecard.sigmoid(-2), 1.0, 1e-12));

        Scorecard hand = new Scorecard(new double[] {1, 2, 0, 0, 0, 0}, -3);
        Features x = Features.of(0.5, true, false, 0, 1000, 0);
        check("logit = bias + sum(w*x) = -3 + 0.5 + 2 = -0.5", close(hand.logit(x), -0.5, 1e-12));
        check("score maps p in [0,1] onto 0..999", Scorecard.scoreFromProbability(1.0) == 999 && Scorecard.scoreFromProbability(0.0) == 0);
        Explanation ex = hand.explain(x, 2);
        check("explanation keeps exactly k features", ex.top().size() == 2);
        check("explanation ranks by |contribution|", ex.top().get(0).feature().equals("new_device") && ex.top().get(1).feature().equals("amount_log_ratio"));

        // --- confusion matrix, precision, recall, F1 by hand ---
        List<Scored> toy = List.of(s(0.9, true), s(0.8, false), s(0.7, true), s(0.4, true), s(0.3, false), s(0.1, false));
        ConfusionMatrix cm = Evaluator.confusion(toy, 0.5);
        check("toy at 0.5: tp=2 fp=1 tn=2 fn=1", cm.tp() == 2 && cm.fp() == 1 && cm.tn() == 2 && cm.fn() == 1);
        check("precision 2/3", close(cm.precision(), 2.0 / 3, 1e-12));
        check("recall 2/3", close(cm.recall(), 2.0 / 3, 1e-12));
        check("F1 is the harmonic mean", close(cm.f1(), 2.0 / 3, 1e-12));
        check("threshold is inclusive (p >= t flags)", Evaluator.confusion(toy, 0.4).tp() == 3);
        boolean conserved = true;
        for (int i = 0; i <= 100; i++) conserved &= Evaluator.confusion(toy, i / 100.0).total() == toy.size();
        check("tp+fp+tn+fn == n at every threshold", conserved);
        check("empty denominators give 0, not NaN", new ConfusionMatrix(0, 0, 5, 0).precision() == 0.0);

        // --- AUC by rank sum ---
        check("perfect separation AUC = 1", Evaluator.auc(List.of(s(0.9, true), s(0.8, true), s(0.2, false), s(0.1, false))) == 1.0);
        check("inverted ranking AUC = 0", Evaluator.auc(List.of(s(0.1, true), s(0.9, false))) == 0.0);
        check("all tied AUC = 0.5", Evaluator.auc(List.of(s(0.5, true), s(0.5, false), s(0.5, true), s(0.5, false))) == 0.5);
        check("toy AUC = 7/9 (7 of 9 fraud-legit pairs ordered)", close(Evaluator.auc(toy), 7.0 / 9, 1e-12));

        // --- cost model in money, never floats ---
        CostModel costs = new CostModel(Money.parse("105.00", USD), Money.parse("12.00", USD), Money.parse("4.00", USD), 0.10, 0.05);
        check("cost = 1 fn * 105.00 + 1 fp * 12.00 = 117.00", costs.cost(cm).equals(Money.parse("117.00", USD)));

        // --- training is deterministic and learns the signal ---
        List<Example> all = SyntheticDataset.generate(42L, 20_000, 0.02);
        List<Example> a = SyntheticDataset.generate(42L, 500, 0.02), b = SyntheticDataset.generate(42L, 500, 0.02);
        boolean same = true;
        for (int i = 0; i < a.size(); i++) {
            same &= a.get(i).fraud() == b.get(i).fraud() && a.get(i).amount().equals(b.get(i).amount())
                    && java.util.Arrays.equals(a.get(i).features().values(), b.get(i).features().values());
        }
        check("generator is seeded (same seed, same rows)", same);
        List<List<Example>> parts = SyntheticDataset.split(all, 7L, 0.7);
        check("split sizes 14000 / 6000", parts.get(0).size() == 14_000 && parts.get(1).size() == 6_000);
        Scorecard m1 = Scorecard.train(parts.get(0), 400, 0.5);
        Scorecard m2 = Scorecard.train(parts.get(0), 400, 0.5);
        check("same data + epochs + rate -> identical weights", java.util.Arrays.equals(m1.weights, m2.weights) && m1.bias == m2.bias);
        boolean allPositive = true;
        for (double w : m1.weights) allPositive &= w > 0;
        check("every risk feature learns a positive weight", allPositive);
        check("bias is strongly negative (base rate ~2%)", m1.bias < -3);
        Scorecard untrained = new Scorecard(new double[Features.COUNT], 0);
        check("training lowers test log loss vs p=0.5", m1.logLoss(parts.get(1)) < untrained.logLoss(parts.get(1)));
        List<Scored> scored = Evaluator.score(m1, parts.get(1));
        double auc = Evaluator.auc(scored);
        check("test AUC above 0.95", auc > 0.95);

        // --- threshold selection ---
        Threshold byF1 = ThresholdSelector.bestByF1(scored, costs);
        Threshold byCost = ThresholdSelector.bestByCost(scored, costs);
        Money approveAll = costs.cost(Evaluator.confusion(scored, 1.01));
        check("cost-optimal threshold never costs more than the F1-optimal one", byCost.expectedCost().compareTo(byF1.expectedCost()) <= 0);
        check("cost-optimal beats approve-everything", byCost.expectedCost().compareTo(approveAll) < 0);
        check("cheap insults push the cost threshold at or below the F1 threshold", byCost.value() <= byF1.value());
        CostModel expensiveInsults = new CostModel(Money.parse("105.00", USD), Money.parse("400.00", USD), Money.parse("4.00", USD), 0.10, 0.05);
        check("expensive insults raise the threshold", ThresholdSelector.bestByCost(scored, expensiveInsults).value() > byCost.value());

        // --- review band ---
        ReviewBand band = ThresholdSelector.bestBand(scored, costs);
        check("band low <= high", band.low() <= band.high());
        check("every row lands in exactly one bucket", band.approved() + band.reviewed() + band.declined() == scored.size());
        check("band cost <= best single threshold", band.expectedCost().compareTo(byCost.expectedCost()) <= 0);
        CostModel pricyReview = new CostModel(Money.parse("105.00", USD), Money.parse("12.00", USD), Money.parse("500.00", USD), 0.10, 0.05);
        check("review dearer than a fraud loss -> empty band", ThresholdSelector.bestBand(scored, pricyReview).reviewed() == 0);
        long[] cum = ThresholdSelector.cumulative(new long[] {2, 0, 3});
        check("cumulative counts are prefix sums with a leading zero", cum.length == 4 && cum[0] == 0 && cum[3] == 5 && cum[2] == 2);

        // --- calibration ---
        List<Scored> calibrated = new ArrayList<>();
        for (int i = 0; i < 10; i++) calibrated.add(s(0.25, i < 2 || i == 9 ? i < 2 : false));    // 2 of 10 at p=0.25 -> off by 0.05
        for (int i = 0; i < 4; i++) calibrated.add(s(0.75, i < 3));                               // 3 of 4 at p=0.75 -> exact
        List<Calibration.Bin> bins = Calibration.reliability(calibrated, 4);
        check("reliability bins conserve the count", bins.stream().mapToLong(Calibration.Bin::count).sum() == 14);
        check("bin [0.25,0.5) observed rate 0.2", close(bins.get(1).observedRate(), 0.2, 1e-12));
        check("ECE = 10/14 * 0.05 + 4/14 * 0 ", close(Calibration.expectedCalibrationError(bins), 10.0 / 14 * 0.05, 1e-12));
        check("p = 1.0 lands in the last bin, not out of range", Calibration.reliability(List.of(s(1.0, true)), 10).get(9).count() == 1);

        // --- decision engine ---
        DecisionEngine engine = new DecisionEngine(m1, new ReviewBand(0.06, 0.13, 0, 0, 0, Money.ofMinor(0, USD)));
        check("clean customer approved", engine.decide(Features.of(0.0, false, false, 1, 500, 0)).outcome() == Outcome.APPROVE);
        check("everything wrong declined", engine.decide(Features.of(2.0, true, true, 6, 2, 3)).outcome() == Outcome.DECLINE);
        check("decision carries 3 reasons", engine.decide(Features.of(2.0, true, true, 6, 2, 3)).explanation().top().size() == 3);
        checkThrows("inverted band rejected", IllegalArgumentException.class,
                () -> new DecisionEngine(m1, new ReviewBand(0.5, 0.2, 0, 0, 0, Money.ofMinor(0, USD))));

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(msg -> System.out.println("FAIL: " + msg));
        if (!failures.isEmpty()) System.exit(1);
    }
}
