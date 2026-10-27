// Tests for phases/13-risk-fraud-and-compliance/03-risk-scoring-and-thresholds (see docs/en.md). Run: go test ./...
package main

import (
	"errors"
	"math"
	"testing"
)

func check(t *testing.T, name string, condition bool) {
	t.Helper()
	if !condition {
		t.Errorf("FAIL: %s", name)
	}
}

func close(a, b, eps float64) bool { return math.Abs(a-b) <= eps }

func sc(p float64, fraud bool) Scored { return Scored{p, fraud, OfMinor(5000, USD)} }

var toy = []Scored{sc(0.9, true), sc(0.8, false), sc(0.7, true), sc(0.4, true), sc(0.3, false), sc(0.1, false)}

var costs = CostModel{MustParse("105.00", USD), MustParse("12.00", USD), MustParse("4.00", USD), 0.10, 0.05}

func TestFeaturesAndScorecardArithmetic(t *testing.T) {
	f := FeaturesOf(0.5, true, false, 25, 45, 9)
	check(t, "velocity capped at 10 attempts -> 1.0", f.Get(3) == 1.0)
	check(t, "account_young is 1 - 45/90 = 0.5", close(f.Get(4), 0.5, 1e-12))
	check(t, "failed CVV capped at 5 -> 1.0", f.Get(5) == 1.0)
	check(t, "old account contributes 0", FeaturesOf(0, false, false, 1, 400, 0).Get(4) == 0.0)
	_, err := NewFeatures([]float64{1, 2})
	check(t, "wrong feature count rejected", errors.Is(err, ErrFeatureCount))
	check(t, "sigmoid(0) = 0.5", Sigmoid(0) == 0.5)
	check(t, "sigmoid is symmetric", close(Sigmoid(2)+Sigmoid(-2), 1.0, 1e-12))

	hand, err := NewScorecard([]float64{1, 2, 0, 0, 0, 0}, -3)
	if err != nil {
		t.Fatal(err)
	}
	x := FeaturesOf(0.5, true, false, 0, 1000, 0)
	check(t, "logit = bias + sum(w*x) = -3 + 0.5 + 2 = -0.5", close(hand.Logit(x), -0.5, 1e-12))
	check(t, "score maps p in [0,1] onto 0..999", ScoreFromProbability(1.0) == 999 && ScoreFromProbability(0.0) == 0)
	ex := hand.Explain(x, 2)
	check(t, "explanation keeps exactly k features", len(ex.Top) == 2)
	check(t, "explanation ranks by |contribution|", ex.Top[0].Feature == "new_device" && ex.Top[1].Feature == "amount_log_ratio")
	_, err = NewScorecard([]float64{1}, 0)
	check(t, "weights must match features", errors.Is(err, ErrFeatureCount))
}

func TestConfusionMatrixByHand(t *testing.T) {
	cm := Confusion(toy, 0.5)
	check(t, "toy at 0.5: tp=2 fp=1 tn=2 fn=1", cm.TP == 2 && cm.FP == 1 && cm.TN == 2 && cm.FN == 1)
	check(t, "precision 2/3", close(cm.Precision(), 2.0/3, 1e-12))
	check(t, "recall 2/3", close(cm.Recall(), 2.0/3, 1e-12))
	check(t, "F1 is the harmonic mean", close(cm.F1(), 2.0/3, 1e-12))
	check(t, "threshold is inclusive (p >= t flags)", Confusion(toy, 0.4).TP == 3)
	conserved := true
	for i := 0; i <= 100; i++ {
		conserved = conserved && Confusion(toy, float64(i)/100.0).Total() == int64(len(toy))
	}
	check(t, "tp+fp+tn+fn == n at every threshold", conserved)
	check(t, "empty denominators give 0, not NaN", ConfusionMatrix{0, 0, 5, 0}.Precision() == 0.0)
	check(t, "cost = 1 fn * 105.00 + 1 fp * 12.00 = 117.00", costs.Cost(cm) == MustParse("117.00", USD))
}

func TestAUCByRankSum(t *testing.T) {
	check(t, "perfect separation AUC = 1", AUC([]Scored{sc(0.9, true), sc(0.8, true), sc(0.2, false), sc(0.1, false)}) == 1.0)
	check(t, "inverted ranking AUC = 0", AUC([]Scored{sc(0.1, true), sc(0.9, false)}) == 0.0)
	check(t, "all tied AUC = 0.5", AUC([]Scored{sc(0.5, true), sc(0.5, false), sc(0.5, true), sc(0.5, false)}) == 0.5)
	check(t, "toy AUC = 7/9 (7 of 9 fraud-legit pairs ordered)", close(AUC(toy), 7.0/9, 1e-12))
}

func sameRows(a, b []Example) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Fraud != b[i].Fraud || a[i].Amount != b[i].Amount {
			return false
		}
		for k := range a[i].Features.Values {
			if a[i].Features.Values[k] != b[i].Features.Values[k] {
				return false
			}
		}
	}
	return true
}

func TestTrainingAndThresholds(t *testing.T) {
	all := Generate(42, 20000, 0.02)
	a, b := Generate(42, 500, 0.02), Generate(42, 500, 0.02)
	check(t, "generator is seeded (same seed, same rows)", sameRows(a, b))
	train, test := Split(all, 7, 0.7)
	check(t, "split sizes 14000 / 6000", len(train) == 14000 && len(test) == 6000)
	m1 := Train(train, 400, 0.5)
	m2 := Train(train, 400, 0.5)
	same := m1.Bias == m2.Bias
	for i := range m1.Weights {
		same = same && m1.Weights[i] == m2.Weights[i]
	}
	check(t, "same data + epochs + rate -> identical weights", same)
	allPositive := true
	for _, w := range m1.Weights {
		allPositive = allPositive && w > 0
	}
	check(t, "every risk feature learns a positive weight", allPositive)
	check(t, "bias is strongly negative (base rate ~2%)", m1.Bias < -3)
	untrained := &Scorecard{make([]float64, FeatureCount), 0}
	check(t, "training lowers test log loss vs p=0.5", m1.LogLoss(test) < untrained.LogLoss(test))
	scored := Score(m1, test)
	check(t, "test AUC above 0.95", AUC(scored) > 0.95)

	byF1 := BestByF1(scored, costs)
	byCost := BestByCost(scored, costs)
	approveAll := costs.Cost(Confusion(scored, 1.01))
	check(t, "cost-optimal threshold never costs more than the F1-optimal one", byCost.ExpectedCost.Compare(byF1.ExpectedCost) <= 0)
	check(t, "cost-optimal beats approve-everything", byCost.ExpectedCost.Compare(approveAll) < 0)
	check(t, "cheap insults push the cost threshold at or below the F1 threshold", byCost.Value <= byF1.Value)
	expensiveInsults := CostModel{MustParse("105.00", USD), MustParse("400.00", USD), MustParse("4.00", USD), 0.10, 0.05}
	check(t, "expensive insults raise the threshold", BestByCost(scored, expensiveInsults).Value > byCost.Value)

	band := BestBand(scored, costs)
	check(t, "band low <= high", band.Low <= band.High)
	check(t, "every row lands in exactly one bucket", band.Approved+band.Reviewed+band.Declined == int64(len(scored)))
	check(t, "band cost <= best single threshold", band.ExpectedCost.Compare(byCost.ExpectedCost) <= 0)
	pricyReview := CostModel{MustParse("105.00", USD), MustParse("12.00", USD), MustParse("500.00", USD), 0.10, 0.05}
	check(t, "review dearer than a fraud loss -> empty band", BestBand(scored, pricyReview).Reviewed == 0)
	cum := Cumulative([]int64{2, 0, 3})
	check(t, "cumulative counts are prefix sums with a leading zero", len(cum) == 4 && cum[0] == 0 && cum[3] == 5 && cum[2] == 2)

	// decision engine
	engine, err := NewDecisionEngine(m1, ReviewBand{0.06, 0.13, 0, 0, 0, OfMinor(0, USD)})
	check(t, "engine accepts an ordered band", err == nil)
	check(t, "clean customer approved", engine.Decide(FeaturesOf(0.0, false, false, 1, 500, 0)).Outcome == Approve)
	check(t, "everything wrong declined", engine.Decide(FeaturesOf(2.0, true, true, 6, 2, 3)).Outcome == Decline)
	check(t, "decision carries 3 reasons", len(engine.Decide(FeaturesOf(2.0, true, true, 6, 2, 3)).Explanation.Top) == 3)
	_, err = NewDecisionEngine(m1, ReviewBand{0.5, 0.2, 0, 0, 0, OfMinor(0, USD)})
	check(t, "inverted band rejected", errors.Is(err, ErrInvertedBand))
}

func TestCalibration(t *testing.T) {
	calibrated := []Scored{}
	for i := 0; i < 10; i++ { // 2 of 10 at p=0.25 -> off by 0.05
		calibrated = append(calibrated, sc(0.25, i < 2))
	}
	for i := 0; i < 4; i++ { // 3 of 4 at p=0.75 -> exact
		calibrated = append(calibrated, sc(0.75, i < 3))
	}
	bins := Reliability(calibrated, 4)
	var total int64
	for _, b := range bins {
		total += b.Count
	}
	check(t, "reliability bins conserve the count", total == 14)
	check(t, "bin [0.25,0.5) observed rate 0.2", close(bins[1].ObservedRate, 0.2, 1e-12))
	check(t, "ECE = 10/14 * 0.05 + 4/14 * 0 ", close(ExpectedCalibrationError(bins), 10.0/14*0.05, 1e-12))
	check(t, "p = 1.0 lands in the last bin, not out of range", Reliability([]Scored{sc(1.0, true)}, 10)[9].Count == 1)
}

func TestJavaFormatting(t *testing.T) {
	cases := []struct {
		x      float64
		digits int
		plus   bool
		want   string
	}{{0.125, 2, false, "0.13"}, {-4.5114, 3, true, "-4.511"}, {1.2945, 3, true, "+1.295"}, {0.0, 2, true, "+0.00"}, {99.996, 2, false, "100.00"}}
	for _, c := range cases {
		if got := jf(c.x, c.digits, c.plus); got != c.want {
			t.Errorf("jf(%v, %d, %v) = %q, want %q", c.x, c.digits, c.plus, got, c.want)
		}
	}
	check(t, "Math.round rounds half up, not half to even", javaRound(2.5) == 3 && javaRound(-2.5) == -2)
}
