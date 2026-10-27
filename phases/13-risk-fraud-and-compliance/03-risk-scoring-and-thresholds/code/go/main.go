// Lesson: phases/13-risk-fraud-and-compliance/03-risk-scoring-and-thresholds/docs/en.md
// Risk Scoring, Thresholds, and Precision-Recall Trade-offs
// Run:  go run .
// Test: go test ./...
// Sources: logistic regression by gradient descent (Hastie, Tibshirani, Friedman, "Elements of Statistical
// Learning", ch. 4); reliability diagrams and expected calibration error (Naeini et al., AAAI 2015).
// Go 1.22+, standard library only. Money is integer minor units (Phase 10, Lesson 01); scores are float64.
// java.util.Random is ported bit for bit so the synthetic dataset, and therefore every number, matches the Java.
package main

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// ---------------------------------------------------------------------------------------------- money (Phase 10, Lesson 01)

// Currency is an ISO 4217 currency: code and minor-unit exponent.
type Currency struct {
	Code     string
	Exponent int
}

var USD = Currency{"USD", 2}

func NewCurrency(code string, exponent int) (Currency, error) {
	if len(code) != 3 {
		return Currency{}, fmt.Errorf("currency code: %q", code)
	}
	if exponent < 0 || exponent > 4 {
		return Currency{}, fmt.Errorf("exponent must be 0..4: %d", exponent)
	}
	return Currency{code, exponent}, nil
}

func (c Currency) MinorUnitsPerMajor() int64 {
	scale := int64(1)
	for i := 0; i < c.Exponent; i++ {
		scale *= 10
	}
	return scale
}

type Rounding int

const (
	HalfEven Rounding = iota
	Down
)

var (
	ErrCurrencyMismatch = errors.New("currency mismatch")
	ErrOverflow         = errors.New("overflow")
	ErrTooManyDecimals  = errors.New("too many decimals")
	ErrBadDenominator   = errors.New("denominator must be positive")
)

func addChecked(a, b int64) (int64, error) {
	sum := a + b
	if (a > 0 && b > 0 && sum < 0) || (a < 0 && b < 0 && sum >= 0) {
		return 0, ErrOverflow
	}
	return sum, nil
}

func subChecked(a, b int64) (int64, error) {
	diff := a - b
	if (a >= 0 && b < 0 && diff < 0) || (a < 0 && b > 0 && diff >= 0) {
		return 0, ErrOverflow
	}
	return diff, nil
}

func mulChecked(a, b int64) (int64, error) {
	if a == 0 || b == 0 {
		return 0, nil
	}
	p := a * b
	if p/b != a || (a == -1 && b == math.MinInt64) || (b == -1 && a == math.MinInt64) {
		return 0, ErrOverflow
	}
	return p, nil
}

// Money is integer minor units plus a currency. Never a float.
type Money struct {
	MinorUnits int64
	Currency   Currency
}

func OfMinor(minorUnits int64, currency Currency) Money { return Money{minorUnits, currency} }

func Parse(text string, currency Currency) (Money, error) {
	s := strings.TrimSpace(text)
	negative := strings.HasPrefix(s, "-")
	if negative || strings.HasPrefix(s, "+") {
		s = s[1:]
	}
	whole, frac := s, ""
	if dot := strings.IndexByte(s, '.'); dot >= 0 {
		whole, frac = s[:dot], s[dot+1:]
	}
	if whole == "" {
		whole = "0"
	}
	if len(frac) > currency.Exponent {
		return Money{}, fmt.Errorf("%w: %s for %s", ErrTooManyDecimals, text, currency.Code)
	}
	for len(frac) < currency.Exponent {
		frac += "0"
	}
	w, err := strconv.ParseInt(whole, 10, 64)
	if err != nil {
		return Money{}, err
	}
	var f int64
	if frac != "" {
		if f, err = strconv.ParseInt(frac, 10, 64); err != nil {
			return Money{}, err
		}
	}
	scaled, err := mulChecked(w, currency.MinorUnitsPerMajor())
	if err != nil {
		return Money{}, err
	}
	minor, err := addChecked(scaled, f)
	if err != nil {
		return Money{}, err
	}
	if negative {
		minor = -minor
	}
	return Money{minor, currency}, nil
}

// MustParse is Parse for literal amounts in the demo and tests.
func MustParse(text string, currency Currency) Money {
	m, err := Parse(text, currency)
	if err != nil {
		panic(err)
	}
	return m
}

func (m Money) requireSameCurrency(other Money) error {
	if m.Currency != other.Currency {
		return fmt.Errorf("%w: cannot combine %s with %s", ErrCurrencyMismatch, m.Currency.Code, other.Currency.Code)
	}
	return nil
}

func (m Money) Plus(other Money) (Money, error) {
	if err := m.requireSameCurrency(other); err != nil {
		return Money{}, err
	}
	sum, err := addChecked(m.MinorUnits, other.MinorUnits)
	return Money{sum, m.Currency}, err
}

func (m Money) Minus(other Money) (Money, error) {
	if err := m.requireSameCurrency(other); err != nil {
		return Money{}, err
	}
	diff, err := subChecked(m.MinorUnits, other.MinorUnits)
	return Money{diff, m.Currency}, err
}

func DivideRounded(dividend, divisor int64, rounding Rounding) int64 {
	quotient := dividend / divisor
	remainder := dividend % divisor
	if remainder == 0 {
		return quotient
	}
	sign := int64(1)
	if (dividend < 0) != (divisor < 0) {
		sign = -1
	}
	absRem, absDiv := remainder, divisor
	if absRem < 0 {
		absRem = -absRem
	}
	if absDiv < 0 {
		absDiv = -absDiv
	}
	twice := absRem * 2
	switch rounding {
	case Down:
		return quotient
	default: // HalfEven
		if twice > absDiv {
			return quotient + sign
		}
		if twice < absDiv {
			return quotient
		}
		if quotient%2 == 0 {
			return quotient
		}
		return quotient + sign
	}
}

func (m Money) Times(numerator, denominator int64, rounding Rounding) (Money, error) {
	if denominator <= 0 {
		return Money{}, ErrBadDenominator
	}
	product, err := mulChecked(m.MinorUnits, numerator)
	if err != nil {
		return Money{}, err
	}
	return Money{DivideRounded(product, denominator, rounding), m.Currency}, nil
}

// Compare panics on a currency mismatch: a comparison has no error slot and the mismatch is a programming error.
func (m Money) Compare(other Money) int {
	if err := m.requireSameCurrency(other); err != nil {
		panic(err)
	}
	switch {
	case m.MinorUnits < other.MinorUnits:
		return -1
	case m.MinorUnits > other.MinorUnits:
		return 1
	default:
		return 0
	}
}

func (m Money) String() string {
	scale := m.Currency.MinorUnitsPerMajor()
	abs := m.MinorUnits
	if abs < 0 {
		abs = -abs
	}
	sign := ""
	if m.MinorUnits < 0 {
		sign = "-"
	}
	if m.Currency.Exponent == 0 {
		return fmt.Sprintf("%s%d %s", sign, abs, m.Currency.Code)
	}
	return fmt.Sprintf("%s%d.%0*d %s", sign, abs/scale, m.Currency.Exponent, abs%scale, m.Currency.Code)
}

// must unwraps a Money operation whose failure would be a programming error in the cost model (no overflow is reachable).
func must(m Money, err error) Money {
	if err != nil {
		panic(err)
	}
	return m
}

// ---------------------------------------------------------------------------------------------- java.util.Random and Java formatting

// JavaRandom is java.util.Random, bit for bit: 48-bit LCG, next(bits), NextInt(bound), NextDouble, NextGaussian (polar
// method with the cached second value). Same seed, same synthetic dataset as the Java version.
type JavaRandom struct {
	seed                 int64
	nextNextGaussian     float64
	haveNextNextGaussian bool
}

const (
	lcgMultiplier = 0x5DEECE66D
	lcgAddend     = 0xB
	lcgMask       = (int64(1) << 48) - 1
)

func NewJavaRandom(seed int64) *JavaRandom {
	return &JavaRandom{seed: (seed ^ lcgMultiplier) & lcgMask}
}

func (r *JavaRandom) next(bits uint) int32 {
	r.seed = (r.seed*lcgMultiplier + lcgAddend) & lcgMask
	return int32(r.seed >> (48 - bits))
}

func (r *JavaRandom) NextInt(bound int) int {
	if bound <= 0 {
		panic("bound must be positive")
	}
	if bound&(-bound) == bound { // power of two
		return int((int64(bound) * int64(r.next(31))) >> 31)
	}
	for {
		bits := r.next(31)
		val := bits % int32(bound)
		if bits-val+int32(bound-1) >= 0 {
			return int(val)
		}
	}
}

func (r *JavaRandom) NextDouble() float64 {
	return float64((int64(r.next(26))<<27)+int64(r.next(27))) * (1.0 / (1 << 53))
}

func (r *JavaRandom) NextGaussian() float64 {
	if r.haveNextNextGaussian {
		r.haveNextNextGaussian = false
		return r.nextNextGaussian
	}
	for {
		v1 := 2*r.NextDouble() - 1
		v2 := 2*r.NextDouble() - 1
		s := v1*v1 + v2*v2
		if s >= 1 || s == 0 {
			continue
		}
		multiplier := math.Sqrt(-2 * math.Log(s) / s)
		r.nextNextGaussian = v2 * multiplier
		r.haveNextNextGaussian = true
		return v1 * multiplier
	}
}

// Shuffle is Collections.shuffle(list, rnd).
func Shuffle[T any](list []T, rng *JavaRandom) {
	for i := len(list); i > 1; i-- {
		j := rng.NextInt(i)
		list[i-1], list[j] = list[j], list[i-1]
	}
}

// javaRound is Math.round for doubles: floor(x + 0.5), so -2.5 rounds to -2 where Go's math.Round gives -3.
func javaRound(x float64) int64 { return int64(math.Floor(x + 0.5)) }

// jf formats like Java's String.format("%.Nf"): round the shortest decimal representation half up, where Go's %f
// rounds the exact binary value half to even (0.125 prints 0.13 in Java and 0.12 in Go). plus adds a leading '+'.
func jf(x float64, digits int, plus bool) string {
	s := strconv.FormatFloat(x, 'f', -1, 64)
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	whole, frac := s, ""
	if dot := strings.IndexByte(s, '.'); dot >= 0 {
		whole, frac = s[:dot], s[dot+1:]
	}
	for len(frac) < digits+1 {
		frac += "0"
	}
	digitsStr := whole + frac[:digits]
	roundUp := frac[digits] >= '5'
	b := []byte(digitsStr)
	if roundUp {
		i := len(b) - 1
		for ; i >= 0; i-- {
			if b[i] == '9' {
				b[i] = '0'
			} else {
				b[i]++
				break
			}
		}
		if i < 0 {
			b = append([]byte{'1'}, b...)
		}
	}
	out := string(b)
	if digits > 0 {
		out = out[:len(out)-digits] + "." + out[len(out)-digits:]
	}
	isZero := strings.Trim(out, "0.") == ""
	if neg && !isZero {
		out = "-" + out
	} else if plus {
		out = "+" + out
	}
	return out
}

// ---------------------------------------------------------------------------------------------- features and data

// Features is the feature vector a transaction is scored on. Every value is already transformed to a small range.
type Features struct{ Values []float64 }

var FeatureNames = []string{
	"amount_log_ratio", // ln(amount / account's trailing average amount); 0 means typical
	"new_device",       // 1 if the device fingerprint has never been seen on this account
	"geo_mismatch",     // 1 if the IP country differs from the card's issuing country
	"velocity_1h",      // attempts on this account in the last hour, capped at 10, divided by 10
	"account_young",    // max(0, 1 - account_age_days / 90)
	"failed_cvv_24h",   // CVV failures in the last 24 hours, capped at 5, divided by 5
}

var FeatureCount = len(FeatureNames)

// ErrFeatureCount is returned when a vector does not have exactly FeatureCount values.
var ErrFeatureCount = errors.New("wrong feature count")

func NewFeatures(values []float64) (Features, error) {
	if len(values) != FeatureCount {
		return Features{}, fmt.Errorf("%w: expected %d features, got %d", ErrFeatureCount, FeatureCount, len(values))
	}
	return Features{append([]float64(nil), values...)}, nil
}

func clampInt(x, lo, hi int) int {
	if x < lo {
		return lo
	}
	if x > hi {
		return hi
	}
	return x
}

func FeaturesOf(amountLogRatio float64, newDevice, geoMismatch bool, attempts1h, accountAgeDays, failedCvv24h int) Features {
	b2f := func(b bool) float64 {
		if b {
			return 1.0
		}
		return 0.0
	}
	age := accountAgeDays
	if age < 0 {
		age = 0
	}
	return Features{[]float64{
		amountLogRatio,
		b2f(newDevice),
		b2f(geoMismatch),
		float64(clampInt(attempts1h, 0, 10)) / 10.0,
		math.Max(0.0, 1.0-float64(age)/90.0),
		float64(clampInt(failedCvv24h, 0, 5)) / 5.0,
	}}
}

func (f Features) Get(i int) float64 { return f.Values[i] }

// Example is one labelled row: features, the ground truth that arrived 60 days later as a chargeback, and the amount at stake.
type Example struct {
	Features Features
	Fraud    bool
	Amount   Money
}

// Generate is a seeded generator. Fraud and legitimate rows are drawn from different distributions so a scorecard can separate them.
func Generate(seed int64, n int, fraudRate float64) []Example {
	rng := NewJavaRandom(seed)
	rows := make([]Example, 0, n)
	for i := 0; i < n; i++ {
		fraud := rng.NextDouble() < fraudRate
		var amountLogRatio float64
		var newDevice, geoMismatch bool
		var attempts, ageDays, failedCvv int
		if fraud {
			amountLogRatio = 0.9 + 0.8*rng.NextGaussian()
			newDevice = rng.NextDouble() < 0.70
			geoMismatch = rng.NextDouble() < 0.40
			attempts = 1 + Poisson(rng, 3.0)
			if rng.NextDouble() < 0.5 {
				ageDays = rng.NextInt(30)
			} else {
				ageDays = rng.NextInt(1000)
			}
			failedCvv = Poisson(rng, 1.2)
		} else {
			amountLogRatio = 0.0 + 0.6*rng.NextGaussian()
			newDevice = rng.NextDouble() < 0.10
			geoMismatch = rng.NextDouble() < 0.03
			attempts = 1 + Poisson(rng, 0.3)
			ageDays = rng.NextInt(1000)
			if rng.NextDouble() < 0.05 {
				failedCvv = 1
			}
		}
		cents := javaRound(4000*math.Exp(amountLogRatio)) + 100 // typical ticket 40.00 USD
		rows = append(rows, Example{FeaturesOf(amountLogRatio, newDevice, geoMismatch, attempts, ageDays, failedCvv), fraud, OfMinor(cents, USD)})
	}
	return rows
}

// Poisson is Knuth's algorithm; fine for small means and keeps the generator fully seeded.
func Poisson(rng *JavaRandom, mean float64) int {
	limit, p := math.Exp(-mean), rng.NextDouble()
	k := 0
	for p > limit {
		k++
		p *= rng.NextDouble()
	}
	return k
}

// Split is a deterministic split: shuffle with the seed, then cut.
func Split(rows []Example, seed int64, trainFraction float64) ([]Example, []Example) {
	copied := append([]Example(nil), rows...)
	Shuffle(copied, NewJavaRandom(seed))
	cut := int(javaRound(float64(len(copied)) * trainFraction))
	return copied[:cut], copied[cut:]
}

// ---------------------------------------------------------------------------------------------- the scorecard

// Contribution is one feature's share of a decision: weight times value.
type Contribution struct {
	Feature      string
	Value        float64
	Weight       float64
	Contribution float64
}

// Explanation is shipped with every decision: integer score, probability, and the features that drove it.
type Explanation struct {
	Score       int
	Probability float64
	Top         []Contribution
}

// Scorecard is a logistic scorecard: probability = sigmoid(bias + sum(w_i * x_i)). Trained by plain gradient descent.
type Scorecard struct {
	Weights []float64
	Bias    float64
}

func NewScorecard(weights []float64, bias float64) (*Scorecard, error) {
	if len(weights) != FeatureCount {
		return nil, fmt.Errorf("%w: weights must match features", ErrFeatureCount)
	}
	return &Scorecard{append([]float64(nil), weights...), bias}, nil
}

func Sigmoid(z float64) float64 { return 1.0 / (1.0 + math.Exp(-z)) }

func (s *Scorecard) Logit(f Features) float64 {
	z := s.Bias
	for i := range s.Weights {
		z += float64(s.Weights[i] * f.Get(i)) // explicit conversion: no fused multiply-add, same rounding as Java
	}
	return z
}

func (s *Scorecard) Probability(f Features) float64 { return Sigmoid(s.Logit(f)) }

// Score is an integer 0..999 for storage, comparison, and thresholds in rules; never compare raw doubles across services.
func (s *Scorecard) Score(f Features) int { return int(javaRound(s.Probability(f) * 999.0)) }

func ScoreFromProbability(p float64) int { return int(javaRound(p * 999.0)) }

// Train is batch gradient descent on log loss. Deterministic for a fixed dataset, epochs, and learning rate.
func Train(rows []Example, epochs int, learningRate float64) *Scorecard {
	w := make([]float64, FeatureCount)
	b := 0.0
	n := float64(len(rows))
	for epoch := 0; epoch < epochs; epoch++ {
		gradW := make([]float64, FeatureCount)
		gradB := 0.0
		for _, row := range rows {
			z := b
			for i := range w {
				z += float64(w[i] * row.Features.Get(i))
			}
			target := 0.0
			if row.Fraud {
				target = 1.0
			}
			err := Sigmoid(z) - target
			for i := range w {
				gradW[i] += float64(err * row.Features.Get(i))
			}
			gradB += err
		}
		for i := range w {
			w[i] -= float64(learningRate*gradW[i]) / n
		}
		b -= float64(learningRate*gradB) / n
	}
	return &Scorecard{w, b}
}

func (s *Scorecard) LogLoss(rows []Example) float64 {
	sum := 0.0
	for _, row := range rows {
		p := math.Min(1-1e-12, math.Max(1e-12, s.Probability(row.Features)))
		if row.Fraud {
			sum -= math.Log(p)
		} else {
			sum -= math.Log(1 - p)
		}
	}
	return sum / float64(len(rows))
}

// Explain returns the k features with the largest absolute contribution, most influential first.
func (s *Scorecard) Explain(f Features, k int) Explanation {
	all := make([]Contribution, 0, len(s.Weights))
	for i := range s.Weights {
		all = append(all, Contribution{FeatureNames[i], f.Get(i), s.Weights[i], s.Weights[i] * f.Get(i)})
	}
	sort.Slice(all, func(i, j int) bool {
		ai, aj := math.Abs(all[i].Contribution), math.Abs(all[j].Contribution)
		if ai != aj {
			return ai > aj
		}
		return all[i].Feature < all[j].Feature
	})
	if k > len(all) {
		k = len(all)
	}
	return Explanation{s.Score(f), s.Probability(f), append([]Contribution(nil), all[:k]...)}
}

// ---------------------------------------------------------------------------------------------- evaluation

// Scored is a probability paired with its label, for evaluation.
type Scored struct {
	Probability float64
	Fraud       bool
	Amount      Money
}

// ConfusionMatrix holds the counts at one threshold. The invariant tp + fp + tn + fn == total holds for every threshold.
type ConfusionMatrix struct{ TP, FP, TN, FN int64 }

func (c ConfusionMatrix) Total() int64 { return c.TP + c.FP + c.TN + c.FN }

func (c ConfusionMatrix) Precision() float64 {
	if c.TP+c.FP == 0 {
		return 0.0
	}
	return float64(c.TP) / float64(c.TP+c.FP)
}

func (c ConfusionMatrix) Recall() float64 {
	if c.TP+c.FN == 0 {
		return 0.0
	}
	return float64(c.TP) / float64(c.TP+c.FN)
}

func (c ConfusionMatrix) F1() float64 {
	p, r := c.Precision(), c.Recall()
	if p+r == 0 {
		return 0.0
	}
	return 2 * p * r / (p + r)
}

// FalsePositiveRate is the insult rate: share of legitimate customers declined.
func (c ConfusionMatrix) FalsePositiveRate() float64 {
	if c.FP+c.TN == 0 {
		return 0.0
	}
	return float64(c.FP) / float64(c.FP+c.TN)
}

// CostModel is the money consequences of each cell. Costs are per case; fraud loss is the average amount plus the chargeback fee.
type CostModel struct {
	FraudLoss, InsultCost, ReviewCost Money
	ReviewMissRate, ReviewInsultRate  float64
}

// Cost is the expected cost of a hard threshold: every fraud we miss costs FraudLoss, every good customer we block costs InsultCost.
func (c CostModel) Cost(cm ConfusionMatrix) Money {
	return must(must(c.FraudLoss.Times(cm.FN, 1, HalfEven)).Plus(must(c.InsultCost.Times(cm.FP, 1, HalfEven))))
}

func Score(model *Scorecard, rows []Example) []Scored {
	out := make([]Scored, 0, len(rows))
	for _, e := range rows {
		out = append(out, Scored{model.Probability(e.Features), e.Fraud, e.Amount})
	}
	return out
}

// Confusion declares fraud when probability >= threshold.
func Confusion(scored []Scored, threshold float64) ConfusionMatrix {
	var cm ConfusionMatrix
	for _, s := range scored {
		flagged := s.Probability >= threshold
		switch {
		case s.Fraud && flagged:
			cm.TP++
		case !s.Fraud && flagged:
			cm.FP++
		case !s.Fraud:
			cm.TN++
		default:
			cm.FN++
		}
	}
	return cm
}

// AUC is the area under the ROC curve by the rank-sum (Mann-Whitney) formula: the probability a random fraud outscores a random legit.
func AUC(scored []Scored) float64 {
	sorted := append([]Scored(nil), scored...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Probability < sorted[j].Probability })
	var positives, negatives int64
	for _, s := range sorted {
		if s.Fraud {
			positives++
		} else {
			negatives++
		}
	}
	if positives == 0 || negatives == 0 {
		return 0.5
	}
	rankSum := 0.0
	i := 0
	for i < len(sorted) {
		j := i
		for j < len(sorted) && sorted[j].Probability == sorted[i].Probability {
			j++
		}
		avgRank := float64(i+1+j) / 2.0 // ranks are 1-based; ties share the average rank
		for k := i; k < j; k++ {
			if sorted[k].Fraud {
				rankSum += avgRank
			}
		}
		i = j
	}
	return (rankSum - float64(positives)*float64(positives+1)/2.0) / (float64(positives) * float64(negatives))
}

// Threshold is a chosen threshold and what it costs.
type Threshold struct {
	Value        float64
	Matrix       ConfusionMatrix
	ExpectedCost Money
}

// ReviewBand is two thresholds: approve below Low, decline at or above High, send the rest to a human.
type ReviewBand struct {
	Low, High                    float64
	Approved, Reviewed, Declined int64
	ExpectedCost                 Money
}

const Steps = 100

func BestByF1(scored []Scored, costs CostModel) Threshold {
	var best *Threshold
	for i := 1; i < Steps; i++ {
		t := float64(i) / float64(Steps)
		cm := Confusion(scored, t)
		if best == nil || cm.F1() > best.Matrix.F1() {
			best = &Threshold{t, cm, costs.Cost(cm)}
		}
	}
	return *best
}

func BestByCost(scored []Scored, costs CostModel) Threshold {
	var best *Threshold
	for i := 1; i < Steps; i++ {
		t := float64(i) / float64(Steps)
		cm := Confusion(scored, t)
		c := costs.Cost(cm)
		if best == nil || c.Compare(best.ExpectedCost) < 0 {
			best = &Threshold{t, cm, c}
		}
	}
	return *best
}

// BestBand sweeps every (low, high) pair on a 100-bin histogram. A reviewed fraud is caught with probability
// (1 - ReviewMissRate); a reviewed good customer is wrongly declined with probability ReviewInsultRate;
// every review costs ReviewCost regardless of outcome.
func BestBand(scored []Scored, costs CostModel) ReviewBand {
	fraudBins, legitBins := make([]int64, Steps+1), make([]int64, Steps+1)
	for _, s := range scored {
		bin := int(math.Floor(s.Probability * Steps))
		if bin > Steps {
			bin = Steps
		}
		if s.Fraud {
			fraudBins[bin]++
		} else {
			legitBins[bin]++
		}
	}
	fraudCum, legitCum := Cumulative(fraudBins), Cumulative(legitBins)
	var best *ReviewBand
	for lo := 1; lo < Steps; lo++ {
		for hi := lo; hi < Steps; hi++ {
			fraudBelow, legitBelow := fraudCum[lo], legitCum[lo]
			fraudMid, legitMid := fraudCum[hi]-fraudCum[lo], legitCum[hi]-legitCum[lo]
			fraudAbove, legitAbove := fraudCum[Steps+1]-fraudCum[hi], legitCum[Steps+1]-legitCum[hi]
			// approvals: missed fraud. reviews: review fee on all, a share of fraud slips, a share of legit insulted. declines: insults.
			reviewed := fraudMid + legitMid
			missedInReview := javaRound(float64(fraudMid) * costs.ReviewMissRate)
			insultedInReview := javaRound(float64(legitMid) * costs.ReviewInsultRate)
			cost := must(costs.FraudLoss.Times(fraudBelow+missedInReview, 1, HalfEven))
			cost = must(cost.Plus(must(costs.InsultCost.Times(legitAbove+insultedInReview, 1, HalfEven))))
			cost = must(cost.Plus(must(costs.ReviewCost.Times(reviewed, 1, HalfEven))))
			if best == nil || cost.Compare(best.ExpectedCost) < 0 {
				best = &ReviewBand{float64(lo) / float64(Steps), float64(hi) / float64(Steps),
					fraudBelow + legitBelow, reviewed, fraudAbove + legitAbove, cost}
			}
		}
	}
	return *best
}

// Cumulative: cum[i] = number of items in bins [0, i). Length is bins + 1.
func Cumulative(bins []int64) []int64 {
	cum := make([]int64, len(bins)+1)
	for i := range bins {
		cum[i+1] = cum[i] + bins[i]
	}
	return cum
}

// Bin is one reliability bin: does "p = 0.30" mean 30% of such transactions are fraud?
type Bin struct {
	Lower, Upper  float64
	Count         int64
	MeanPredicted float64
	ObservedRate  float64
}

func Reliability(scored []Scored, bins int) []Bin {
	counts := make([]int64, bins)
	sumPred := make([]float64, bins)
	frauds := make([]int64, bins)
	for _, s := range scored {
		b := int(math.Floor(s.Probability * float64(bins)))
		if b > bins-1 {
			b = bins - 1
		}
		counts[b]++
		sumPred[b] += s.Probability
		if s.Fraud {
			frauds[b]++
		}
	}
	out := make([]Bin, 0, bins)
	for b := 0; b < bins; b++ {
		meanPred, observed := 0.0, 0.0
		if counts[b] != 0 {
			meanPred = sumPred[b] / float64(counts[b])
			observed = float64(frauds[b]) / float64(counts[b])
		}
		out = append(out, Bin{float64(b) / float64(bins), float64(b+1) / float64(bins), counts[b], meanPred, observed})
	}
	return out
}

// ExpectedCalibrationError is the count-weighted mean of |predicted - observed| over the bins.
func ExpectedCalibrationError(bins []Bin) float64 {
	var total int64
	for _, b := range bins {
		total += b.Count
	}
	if total == 0 {
		return 0.0
	}
	ece := 0.0
	for _, b := range bins {
		ece += float64(b.Count) / float64(total) * math.Abs(b.MeanPredicted-b.ObservedRate)
	}
	return ece
}

type Outcome int

const (
	Approve Outcome = iota
	Review
	Decline
)

func (o Outcome) String() string { return [...]string{"APPROVE", "REVIEW", "DECLINE"}[o] }

type Decision struct {
	Outcome     Outcome
	Explanation Explanation
}

// ErrInvertedBand is returned when a review band's low exceeds its high.
var ErrInvertedBand = errors.New("low must not exceed high")

// DecisionEngine is the production object: a frozen scorecard, a review band, and an explanation on every call.
type DecisionEngine struct {
	model *Scorecard
	band  ReviewBand
}

func NewDecisionEngine(model *Scorecard, band ReviewBand) (*DecisionEngine, error) {
	if band.Low > band.High {
		return nil, ErrInvertedBand
	}
	return &DecisionEngine{model, band}, nil
}

func (e *DecisionEngine) Decide(f Features) Decision {
	p := e.model.Probability(f)
	outcome := Approve
	if p >= e.band.High {
		outcome = Decline
	} else if p >= e.band.Low {
		outcome = Review
	}
	return Decision{outcome, e.model.Explain(f, 3)}
}

// ---------------------------------------------------------------------------------------------- demo

func countFraud(rows []Example) int {
	n := 0
	for _, r := range rows {
		if r.Fraud {
			n++
		}
	}
	return n
}

func main() {
	all := Generate(42, 20000, 0.02)
	train, test := Split(all, 7, 0.7)
	trainFraud, testFraud := countFraud(train), countFraud(test)
	fmt.Printf("== Dataset ==\n%d rows, %d train (%d fraud), %d test (%d fraud), base rate %s%%\n\n",
		len(all), len(train), trainFraud, len(test), testFraud, jf(100.0*float64(trainFraud+testFraud)/float64(len(all)), 2, false))

	model := Train(train, 400, 0.5)
	fmt.Println("== Scorecard ==")
	fmt.Printf("bias %s\n", jf(model.Bias, 3, true))
	for i := 0; i < FeatureCount; i++ {
		fmt.Printf("  %-18s %s\n", FeatureNames[i], jf(model.Weights[i], 3, true))
	}
	fmt.Printf("train log loss %s, test log loss %s\n\n", jf(model.LogLoss(train), 4, false), jf(model.LogLoss(test), 4, false))

	scored := Score(model, test)
	fmt.Printf("== Separation ==\nAUC on test = %s\n\n", jf(AUC(scored), 3, false))

	fmt.Println("== Threshold sweep (test set) ==")
	fmt.Println("thr   tp    fp    fn    precision recall  f1     insult%")
	for _, t := range []float64{0.05, 0.10, 0.20, 0.30, 0.50, 0.70, 0.90} {
		cm := Confusion(scored, t)
		fmt.Printf("%s  %-5d %-5d %-5d %s     %s   %s  %s\n", jf(t, 2, false), cm.TP, cm.FP, cm.FN,
			jf(cm.Precision(), 3, false), jf(cm.Recall(), 3, false), jf(cm.F1(), 3, false), jf(100*cm.FalsePositiveRate(), 2, false))
	}

	costs := CostModel{MustParse("105.00", USD), MustParse("12.00", USD), MustParse("4.00", USD), 0.10, 0.05}
	byF1 := BestByF1(scored, costs)
	byCost := BestByCost(scored, costs)
	fmt.Printf("\nbest F1   : threshold %s, F1 %s, expected cost %s\n", jf(byF1.Value, 2, false), jf(byF1.Matrix.F1(), 3, false), byF1.ExpectedCost)
	fmt.Printf("best cost : threshold %s, F1 %s, expected cost %s  (fn %d, fp %d)\n",
		jf(byCost.Value, 2, false), jf(byCost.Matrix.F1(), 3, false), byCost.ExpectedCost, byCost.Matrix.FN, byCost.Matrix.FP)
	approveAll := costs.Cost(Confusion(scored, 1.01))
	declineAll := costs.Cost(Confusion(scored, 0.0))
	fmt.Printf("approve everything: %s   decline everything: %s\n", approveAll, declineAll)

	band := BestBand(scored, costs)
	fmt.Printf("\n== Review band ==\napprove < %s, review [%s, %s), decline >= %s\n", jf(band.Low, 2, false), jf(band.Low, 2, false), jf(band.High, 2, false), jf(band.High, 2, false))
	fmt.Printf("approved %d, reviewed %d (%s%%), declined %d, expected cost %s\n\n",
		band.Approved, band.Reviewed, jf(100.0*float64(band.Reviewed)/float64(len(scored)), 1, false), band.Declined, band.ExpectedCost)

	fmt.Println("== Calibration (test set, 10 bins) ==")
	fmt.Println("bin          n      mean p   observed")
	bins := Reliability(scored, 10)
	for _, b := range bins {
		if b.Count == 0 {
			continue
		}
		fmt.Printf("[%s, %s)  %-6d %s    %s\n", jf(b.Lower, 1, false), jf(b.Upper, 1, false), b.Count, jf(b.MeanPredicted, 3, false), jf(b.ObservedRate, 3, false))
	}
	fmt.Printf("expected calibration error = %s\n\n", jf(ExpectedCalibrationError(bins), 4, false))

	engine, err := NewDecisionEngine(model, band)
	if err != nil {
		panic(err)
	}
	fmt.Println("== Decisions with explanations ==")
	samples := []Features{
		FeaturesOf(0.1, false, false, 1, 400, 0),
		FeaturesOf(1.2, true, false, 3, 10, 1),
		FeaturesOf(2.0, true, true, 6, 2, 3),
	}
	labels := []string{"regular customer, usual amount", "new device, young account, 3 attempts", "everything wrong at once"}
	for i, f := range samples {
		d := engine.Decide(f)
		fmt.Printf("%s -> %s (score %d, p=%s)\n", labels[i], d.Outcome, d.Explanation.Score, jf(d.Explanation.Probability, 3, false))
		for _, c := range d.Explanation.Top {
			fmt.Printf("    %-18s value %s x weight %s = %s\n", c.Feature, jf(c.Value, 2, false), jf(c.Weight, 2, true), jf(c.Contribution, 2, true))
		}
	}
}
