// Lesson: phases/13-risk-fraud-and-compliance/03-risk-scoring-and-thresholds/docs/en.md
// Risk Scoring, Thresholds, and Precision-Recall Trade-offs
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: logistic regression by gradient descent (Hastie, Tibshirani, Friedman, "Elements of Statistical
// Learning", ch. 4); reliability diagrams and expected calibration error (Naeini et al., AAAI 2015).
// Java 21, standard library only. Money is integer minor units (Phase 10, Lesson 01); scores are doubles.

import java.util.ArrayList;
import java.util.Arrays;
import java.util.Collections;
import java.util.Comparator;
import java.util.List;
import java.util.Objects;
import java.util.Random;

/** ISO 4217 currency: code and minor-unit exponent. Copied from Phase 10, Lesson 01. */
record Currency(String code, int exponent) {
    static final Currency USD = new Currency("USD", 2);

    Currency {
        if (code == null || code.length() != 3) throw new IllegalArgumentException("currency code: " + code);
        if (exponent < 0 || exponent > 4) throw new IllegalArgumentException("exponent must be 0..4: " + exponent);
    }

    long minorUnitsPerMajor() {
        long scale = 1;
        for (int i = 0; i < exponent; i++) scale *= 10;
        return scale;
    }
}

enum Rounding { HALF_EVEN, DOWN }

/** Integer minor units plus a currency. Never a float. Copied from Phase 10, Lesson 01 (subset). */
record Money(long minorUnits, Currency currency) {
    Money { Objects.requireNonNull(currency, "currency"); }

    static Money ofMinor(long minorUnits, Currency currency) { return new Money(minorUnits, currency); }

    static Money parse(String text, Currency currency) {
        String s = text.strip();
        boolean negative = s.startsWith("-");
        if (negative || s.startsWith("+")) s = s.substring(1);
        String whole = s, frac = "";
        int dot = s.indexOf('.');
        if (dot >= 0) { whole = s.substring(0, dot); frac = s.substring(dot + 1); }
        if (whole.isEmpty()) whole = "0";
        if (frac.length() > currency.exponent()) throw new ArithmeticException(text + " has too many decimals for " + currency.code());
        StringBuilder padded = new StringBuilder(frac);
        while (padded.length() < currency.exponent()) padded.append('0');
        long minor = Math.addExact(Math.multiplyExact(Long.parseLong(whole), currency.minorUnitsPerMajor()),
                padded.length() == 0 ? 0 : Long.parseLong(padded.toString()));
        return new Money(negative ? -minor : minor, currency);
    }

    private void requireSameCurrency(Money other) {
        if (!currency.equals(other.currency)) throw new IllegalArgumentException("cannot combine " + currency.code() + " with " + other.currency.code());
    }

    Money plus(Money other) { requireSameCurrency(other); return new Money(Math.addExact(minorUnits, other.minorUnits), currency); }

    Money minus(Money other) { requireSameCurrency(other); return new Money(Math.subtractExact(minorUnits, other.minorUnits), currency); }

    static long divideRounded(long dividend, long divisor, Rounding rounding) {
        long quotient = dividend / divisor;
        long remainder = dividend % divisor;
        if (remainder == 0) return quotient;
        int sign = (dividend < 0) ^ (divisor < 0) ? -1 : 1;
        long twice = Math.abs(remainder) * 2, absDivisor = Math.abs(divisor);
        return switch (rounding) {
            case DOWN -> quotient;
            case HALF_EVEN -> {
                if (twice > absDivisor) yield quotient + sign;
                if (twice < absDivisor) yield quotient;
                yield (quotient % 2 == 0) ? quotient : quotient + sign;
            }
        };
    }

    Money times(long numerator, long denominator, Rounding rounding) {
        if (denominator <= 0) throw new ArithmeticException("denominator must be positive");
        return new Money(divideRounded(Math.multiplyExact(minorUnits, numerator), denominator, rounding), currency);
    }

    int compareTo(Money other) { requireSameCurrency(other); return Long.compare(minorUnits, other.minorUnits); }

    @Override public String toString() {
        long scale = currency.minorUnitsPerMajor();
        long abs = Math.abs(minorUnits);
        String sign = minorUnits < 0 ? "-" : "";
        if (currency.exponent() == 0) return sign + abs + " " + currency.code();
        String frac = Long.toString(abs % scale);
        while (frac.length() < currency.exponent()) frac = "0" + frac;
        return sign + (abs / scale) + "." + frac + " " + currency.code();
    }
}

/** The feature vector a transaction is scored on. Every value is already transformed to a small range. */
record Features(double[] values) {
    static final String[] NAMES = {
        "amount_log_ratio",   // ln(amount / account's trailing average amount); 0 means typical
        "new_device",         // 1 if the device fingerprint has never been seen on this account
        "geo_mismatch",       // 1 if the IP country differs from the card's issuing country
        "velocity_1h",        // attempts on this account in the last hour, capped at 10, divided by 10
        "account_young",      // max(0, 1 - account_age_days / 90)
        "failed_cvv_24h"      // CVV failures in the last 24 hours, capped at 5, divided by 5
    };
    static final int COUNT = NAMES.length;

    Features {
        if (values.length != COUNT) throw new IllegalArgumentException("expected " + COUNT + " features, got " + values.length);
    }

    static Features of(double amountLogRatio, boolean newDevice, boolean geoMismatch, int attempts1h, int accountAgeDays, int failedCvv24h) {
        return new Features(new double[] {
            amountLogRatio,
            newDevice ? 1.0 : 0.0,
            geoMismatch ? 1.0 : 0.0,
            Math.min(10, Math.max(0, attempts1h)) / 10.0,
            Math.max(0.0, 1.0 - Math.max(0, accountAgeDays) / 90.0),
            Math.min(5, Math.max(0, failedCvv24h)) / 5.0
        });
    }

    double get(int i) { return values[i]; }
}

/** One labelled row: features, the ground truth that arrived 60 days later as a chargeback, and the amount at stake. */
record Example(Features features, boolean fraud, Money amount) {}

/** A seeded generator. Fraud and legitimate rows are drawn from different distributions so a scorecard can separate them. */
final class SyntheticDataset {
    static List<Example> generate(long seed, int n, double fraudRate) {
        Random rng = new Random(seed);
        List<Example> rows = new ArrayList<>(n);
        for (int i = 0; i < n; i++) {
            boolean fraud = rng.nextDouble() < fraudRate;
            double amountLogRatio;
            boolean newDevice, geoMismatch;
            int attempts, ageDays, failedCvv;
            if (fraud) {
                amountLogRatio = 0.9 + 0.8 * rng.nextGaussian();
                newDevice = rng.nextDouble() < 0.70;
                geoMismatch = rng.nextDouble() < 0.40;
                attempts = 1 + poisson(rng, 3.0);
                ageDays = rng.nextDouble() < 0.5 ? rng.nextInt(30) : rng.nextInt(1000);
                failedCvv = poisson(rng, 1.2);
            } else {
                amountLogRatio = 0.0 + 0.6 * rng.nextGaussian();
                newDevice = rng.nextDouble() < 0.10;
                geoMismatch = rng.nextDouble() < 0.03;
                attempts = 1 + poisson(rng, 0.3);
                ageDays = rng.nextInt(1000);
                failedCvv = rng.nextDouble() < 0.05 ? 1 : 0;
            }
            long cents = Math.round(4000 * Math.exp(amountLogRatio)) + 100;   // typical ticket 40.00 USD
            rows.add(new Example(Features.of(amountLogRatio, newDevice, geoMismatch, attempts, ageDays, failedCvv),
                    fraud, Money.ofMinor(cents, Currency.USD)));
        }
        return rows;
    }

    /** Knuth's algorithm; fine for small means and keeps the generator fully seeded. */
    static int poisson(Random rng, double mean) {
        double limit = Math.exp(-mean), p = rng.nextDouble();
        int k = 0;
        while (p > limit) { k++; p *= rng.nextDouble(); }
        return k;
    }

    /** Deterministic split: shuffle with the seed, then cut. */
    static List<List<Example>> split(List<Example> rows, long seed, double trainFraction) {
        List<Example> copy = new ArrayList<>(rows);
        Collections.shuffle(copy, new Random(seed));
        int cut = (int) Math.round(copy.size() * trainFraction);
        return List.of(copy.subList(0, cut), copy.subList(cut, copy.size()));
    }
}

/** One feature's share of a decision: weight times value. */
record Contribution(String feature, double value, double weight, double contribution) {}

/** The explanation shipped with every decision: integer score, probability, and the features that drove it. */
record Explanation(int score, double probability, List<Contribution> top) {}

/** A logistic scorecard: probability = sigmoid(bias + sum(w_i * x_i)). Trained by plain gradient descent. */
final class Scorecard {
    final double[] weights;
    final double bias;

    Scorecard(double[] weights, double bias) {
        if (weights.length != Features.COUNT) throw new IllegalArgumentException("weights must match features");
        this.weights = weights.clone();
        this.bias = bias;
    }

    static double sigmoid(double z) { return 1.0 / (1.0 + Math.exp(-z)); }

    double logit(Features f) {
        double z = bias;
        for (int i = 0; i < weights.length; i++) z += weights[i] * f.get(i);
        return z;
    }

    double probability(Features f) { return sigmoid(logit(f)); }

    /** Integer 0..999 for storage, comparison, and thresholds in rules; never compare raw doubles across services. */
    int score(Features f) { return (int) Math.round(probability(f) * 999.0); }

    static int scoreFromProbability(double p) { return (int) Math.round(p * 999.0); }

    /** Batch gradient descent on log loss. Deterministic for a fixed dataset, epochs, and learning rate. */
    static Scorecard train(List<Example> rows, int epochs, double learningRate) {
        double[] w = new double[Features.COUNT];
        double b = 0.0;
        int n = rows.size();
        for (int epoch = 0; epoch < epochs; epoch++) {
            double[] gradW = new double[Features.COUNT];
            double gradB = 0.0;
            for (Example row : rows) {
                double z = b;
                for (int i = 0; i < w.length; i++) z += w[i] * row.features().get(i);
                double error = sigmoid(z) - (row.fraud() ? 1.0 : 0.0);
                for (int i = 0; i < w.length; i++) gradW[i] += error * row.features().get(i);
                gradB += error;
            }
            for (int i = 0; i < w.length; i++) w[i] -= learningRate * gradW[i] / n;
            b -= learningRate * gradB / n;
        }
        return new Scorecard(w, b);
    }

    double logLoss(List<Example> rows) {
        double sum = 0.0;
        for (Example row : rows) {
            double p = Math.min(1 - 1e-12, Math.max(1e-12, probability(row.features())));
            sum -= row.fraud() ? Math.log(p) : Math.log(1 - p);
        }
        return sum / rows.size();
    }

    /** The k features with the largest absolute contribution, most influential first. */
    Explanation explain(Features f, int k) {
        List<Contribution> all = new ArrayList<>();
        for (int i = 0; i < weights.length; i++) {
            all.add(new Contribution(Features.NAMES[i], f.get(i), weights[i], weights[i] * f.get(i)));
        }
        all.sort(Comparator.comparingDouble((Contribution c) -> -Math.abs(c.contribution())).thenComparing(Contribution::feature));
        return new Explanation(score(f), probability(f), List.copyOf(all.subList(0, Math.min(k, all.size()))));
    }
}

/** A probability paired with its label, for evaluation. */
record Scored(double probability, boolean fraud, Money amount) {}

/** Counts at one threshold. The invariant tp + fp + tn + fn == total holds for every threshold. */
record ConfusionMatrix(long tp, long fp, long tn, long fn) {
    long total() { return tp + fp + tn + fn; }
    double precision() { return tp + fp == 0 ? 0.0 : (double) tp / (tp + fp); }
    double recall() { return tp + fn == 0 ? 0.0 : (double) tp / (tp + fn); }
    double f1() { double p = precision(), r = recall(); return p + r == 0 ? 0.0 : 2 * p * r / (p + r); }
    /** The insult rate: share of legitimate customers declined. */
    double falsePositiveRate() { return fp + tn == 0 ? 0.0 : (double) fp / (fp + tn); }
}

/** Money consequences of each cell. Costs are per case; fraud loss is the average amount plus the chargeback fee. */
record CostModel(Money fraudLoss, Money insultCost, Money reviewCost, double reviewMissRate, double reviewInsultRate) {
    /** Expected cost of a hard threshold: every fraud we miss costs fraudLoss, every good customer we block costs insultCost. */
    Money cost(ConfusionMatrix cm) {
        return fraudLoss.times(cm.fn(), 1, Rounding.HALF_EVEN).plus(insultCost.times(cm.fp(), 1, Rounding.HALF_EVEN));
    }
}

final class Evaluator {
    static List<Scored> score(Scorecard model, List<Example> rows) {
        List<Scored> out = new ArrayList<>(rows.size());
        for (Example e : rows) out.add(new Scored(model.probability(e.features()), e.fraud(), e.amount()));
        return out;
    }

    /** Declare fraud when probability >= threshold. */
    static ConfusionMatrix confusion(List<Scored> scored, double threshold) {
        long tp = 0, fp = 0, tn = 0, fn = 0;
        for (Scored s : scored) {
            boolean flagged = s.probability() >= threshold;
            if (s.fraud() && flagged) tp++;
            else if (!s.fraud() && flagged) fp++;
            else if (!s.fraud()) tn++;
            else fn++;
        }
        return new ConfusionMatrix(tp, fp, tn, fn);
    }

    /** Area under the ROC curve by the rank-sum (Mann-Whitney) formula: the probability a random fraud outscores a random legit. */
    static double auc(List<Scored> scored) {
        List<Scored> sorted = new ArrayList<>(scored);
        sorted.sort(Comparator.comparingDouble(Scored::probability));
        long positives = 0, negatives = 0;
        for (Scored s : sorted) { if (s.fraud()) positives++; else negatives++; }
        if (positives == 0 || negatives == 0) return 0.5;
        double rankSum = 0;
        int i = 0;
        while (i < sorted.size()) {
            int j = i;
            while (j < sorted.size() && sorted.get(j).probability() == sorted.get(i).probability()) j++;
            double avgRank = (i + 1 + j) / 2.0;      // ranks are 1-based; ties share the average rank
            for (int k = i; k < j; k++) if (sorted.get(k).fraud()) rankSum += avgRank;
            i = j;
        }
        return (rankSum - positives * (positives + 1) / 2.0) / ((double) positives * negatives);
    }
}

/** A chosen threshold and what it costs. */
record Threshold(double value, ConfusionMatrix matrix, Money expectedCost) {}

/** Two thresholds: approve below low, decline at or above high, send the rest to a human. */
record ReviewBand(double low, double high, long approved, long reviewed, long declined, Money expectedCost) {}

final class ThresholdSelector {
    static final int STEPS = 100;

    static Threshold bestByF1(List<Scored> scored, CostModel costs) {
        Threshold best = null;
        for (int i = 1; i < STEPS; i++) {
            double t = i / (double) STEPS;
            ConfusionMatrix cm = Evaluator.confusion(scored, t);
            if (best == null || cm.f1() > best.matrix().f1()) best = new Threshold(t, cm, costs.cost(cm));
        }
        return best;
    }

    static Threshold bestByCost(List<Scored> scored, CostModel costs) {
        Threshold best = null;
        for (int i = 1; i < STEPS; i++) {
            double t = i / (double) STEPS;
            ConfusionMatrix cm = Evaluator.confusion(scored, t);
            Money c = costs.cost(cm);
            if (best == null || c.compareTo(best.expectedCost()) < 0) best = new Threshold(t, cm, c);
        }
        return best;
    }

    /**
     * Sweep every (low, high) pair on a 100-bin histogram. A reviewed fraud is caught with probability
     * (1 - reviewMissRate); a reviewed good customer is wrongly declined with probability reviewInsultRate;
     * every review costs reviewCost regardless of outcome.
     */
    static ReviewBand bestBand(List<Scored> scored, CostModel costs) {
        long[] fraudBins = new long[STEPS + 1], legitBins = new long[STEPS + 1];
        for (Scored s : scored) {
            int bin = Math.min(STEPS, (int) Math.floor(s.probability() * STEPS));
            if (s.fraud()) fraudBins[bin]++; else legitBins[bin]++;
        }
        long[] fraudCum = cumulative(fraudBins), legitCum = cumulative(legitBins);
        ReviewBand best = null;
        for (int lo = 1; lo < STEPS; lo++) {
            for (int hi = lo; hi < STEPS; hi++) {
                long fraudBelow = fraudCum[lo], legitBelow = legitCum[lo];
                long fraudMid = fraudCum[hi] - fraudCum[lo], legitMid = legitCum[hi] - legitCum[lo];
                long fraudAbove = fraudCum[STEPS + 1] - fraudCum[hi], legitAbove = legitCum[STEPS + 1] - legitCum[hi];
                // approvals: missed fraud. reviews: review fee on all, a share of fraud slips, a share of legit insulted. declines: insults.
                long reviewed = fraudMid + legitMid;
                long missedInReview = Math.round(fraudMid * costs.reviewMissRate());
                long insultedInReview = Math.round(legitMid * costs.reviewInsultRate());
                Money cost = costs.fraudLoss().times(fraudBelow + missedInReview, 1, Rounding.HALF_EVEN)
                        .plus(costs.insultCost().times(legitAbove + insultedInReview, 1, Rounding.HALF_EVEN))
                        .plus(costs.reviewCost().times(reviewed, 1, Rounding.HALF_EVEN));
                if (best == null || cost.compareTo(best.expectedCost()) < 0) {
                    best = new ReviewBand(lo / (double) STEPS, hi / (double) STEPS,
                            fraudBelow + legitBelow, reviewed, fraudAbove + legitAbove, cost);
                }
            }
        }
        return best;
    }

    /** cum[i] = number of items in bins [0, i). Length is bins + 1. */
    static long[] cumulative(long[] bins) {
        long[] cum = new long[bins.length + 1];
        for (int i = 0; i < bins.length; i++) cum[i + 1] = cum[i] + bins[i];
        return cum;
    }
}

/** Reliability: does "p = 0.30" mean 30% of such transactions are fraud? */
final class Calibration {
    record Bin(double lower, double upper, long count, double meanPredicted, double observedRate) {}

    static List<Bin> reliability(List<Scored> scored, int bins) {
        long[] counts = new long[bins];
        double[] sumPred = new double[bins];
        long[] frauds = new long[bins];
        for (Scored s : scored) {
            int b = Math.min(bins - 1, (int) Math.floor(s.probability() * bins));
            counts[b]++;
            sumPred[b] += s.probability();
            if (s.fraud()) frauds[b]++;
        }
        List<Bin> out = new ArrayList<>();
        for (int b = 0; b < bins; b++) {
            double meanPred = counts[b] == 0 ? 0 : sumPred[b] / counts[b];
            double observed = counts[b] == 0 ? 0 : (double) frauds[b] / counts[b];
            out.add(new Bin(b / (double) bins, (b + 1) / (double) bins, counts[b], meanPred, observed));
        }
        return out;
    }

    /** Expected calibration error: count-weighted mean of |predicted - observed| over the bins. */
    static double expectedCalibrationError(List<Bin> bins) {
        long total = 0;
        for (Bin b : bins) total += b.count();
        if (total == 0) return 0.0;
        double ece = 0;
        for (Bin b : bins) ece += (double) b.count() / total * Math.abs(b.meanPredicted() - b.observedRate());
        return ece;
    }
}

enum Outcome { APPROVE, REVIEW, DECLINE }

record Decision(Outcome outcome, Explanation explanation) {}

/** The production object: a frozen scorecard, a review band, and an explanation on every call. */
final class DecisionEngine {
    private final Scorecard model;
    private final ReviewBand band;

    DecisionEngine(Scorecard model, ReviewBand band) {
        if (band.low() > band.high()) throw new IllegalArgumentException("low must not exceed high");
        this.model = model;
        this.band = band;
    }

    Decision decide(Features f) {
        double p = model.probability(f);
        Outcome outcome = p >= band.high() ? Outcome.DECLINE : p >= band.low() ? Outcome.REVIEW : Outcome.APPROVE;
        return new Decision(outcome, model.explain(f, 3));
    }
}

public final class Main {
    public static void main(String[] args) {
        List<Example> all = SyntheticDataset.generate(42L, 20_000, 0.02);
        List<List<Example>> parts = SyntheticDataset.split(all, 7L, 0.7);
        List<Example> train = parts.get(0), test = parts.get(1);
        long trainFraud = train.stream().filter(Example::fraud).count();
        long testFraud = test.stream().filter(Example::fraud).count();
        System.out.printf("== Dataset ==%n%d rows, %d train (%d fraud), %d test (%d fraud), base rate %.2f%%%n%n",
                all.size(), train.size(), trainFraud, test.size(), testFraud, 100.0 * (trainFraud + testFraud) / all.size());

        Scorecard model = Scorecard.train(train, 400, 0.5);
        System.out.println("== Scorecard ==");
        System.out.printf("bias %+.3f%n", model.bias);
        for (int i = 0; i < Features.COUNT; i++) System.out.printf("  %-18s %+.3f%n", Features.NAMES[i], model.weights[i]);
        System.out.printf("train log loss %.4f, test log loss %.4f%n%n", model.logLoss(train), model.logLoss(test));

        List<Scored> scored = Evaluator.score(model, test);
        System.out.printf("== Separation ==%nAUC on test = %.3f%n%n", Evaluator.auc(scored));

        System.out.println("== Threshold sweep (test set) ==");
        System.out.println("thr   tp    fp    fn    precision recall  f1     insult%");
        for (double t : new double[] {0.05, 0.10, 0.20, 0.30, 0.50, 0.70, 0.90}) {
            ConfusionMatrix cm = Evaluator.confusion(scored, t);
            System.out.printf("%.2f  %-5d %-5d %-5d %.3f     %.3f   %.3f  %.2f%n", t, cm.tp(), cm.fp(), cm.fn(),
                    cm.precision(), cm.recall(), cm.f1(), 100 * cm.falsePositiveRate());
        }

        CostModel costs = new CostModel(Money.parse("105.00", Currency.USD), Money.parse("12.00", Currency.USD),
                Money.parse("4.00", Currency.USD), 0.10, 0.05);
        Threshold byF1 = ThresholdSelector.bestByF1(scored, costs);
        Threshold byCost = ThresholdSelector.bestByCost(scored, costs);
        System.out.printf("%nbest F1   : threshold %.2f, F1 %.3f, expected cost %s%n", byF1.value(), byF1.matrix().f1(), byF1.expectedCost());
        System.out.printf("best cost : threshold %.2f, F1 %.3f, expected cost %s  (fn %d, fp %d)%n",
                byCost.value(), byCost.matrix().f1(), byCost.expectedCost(), byCost.matrix().fn(), byCost.matrix().fp());
        Money approveAll = costs.cost(Evaluator.confusion(scored, 1.01));
        Money declineAll = costs.cost(Evaluator.confusion(scored, 0.0));
        System.out.printf("approve everything: %s   decline everything: %s%n", approveAll, declineAll);

        ReviewBand band = ThresholdSelector.bestBand(scored, costs);
        System.out.printf("%n== Review band ==%napprove < %.2f, review [%.2f, %.2f), decline >= %.2f%n", band.low(), band.low(), band.high(), band.high());
        System.out.printf("approved %d, reviewed %d (%.1f%%), declined %d, expected cost %s%n%n",
                band.approved(), band.reviewed(), 100.0 * band.reviewed() / scored.size(), band.declined(), band.expectedCost());

        System.out.println("== Calibration (test set, 10 bins) ==");
        System.out.println("bin          n      mean p   observed");
        List<Calibration.Bin> bins = Calibration.reliability(scored, 10);
        for (Calibration.Bin b : bins) {
            if (b.count() == 0) continue;
            System.out.printf("[%.1f, %.1f)  %-6d %.3f    %.3f%n", b.lower(), b.upper(), b.count(), b.meanPredicted(), b.observedRate());
        }
        System.out.printf("expected calibration error = %.4f%n%n", Calibration.expectedCalibrationError(bins));

        DecisionEngine engine = new DecisionEngine(model, band);
        System.out.println("== Decisions with explanations ==");
        Features[] samples = {
            Features.of(0.1, false, false, 1, 400, 0),
            Features.of(1.2, true, false, 3, 10, 1),
            Features.of(2.0, true, true, 6, 2, 3),
        };
        String[] labels = {"regular customer, usual amount", "new device, young account, 3 attempts", "everything wrong at once"};
        for (int i = 0; i < samples.length; i++) {
            Decision d = engine.decide(samples[i]);
            System.out.printf("%s -> %s (score %d, p=%.3f)%n", labels[i], d.outcome(), d.explanation().score(), d.explanation().probability());
            for (Contribution c : d.explanation().top()) {
                System.out.printf("    %-18s value %.2f x weight %+.2f = %+.2f%n", c.feature(), c.value(), c.weight(), c.contribution());
            }
        }
    }
}
