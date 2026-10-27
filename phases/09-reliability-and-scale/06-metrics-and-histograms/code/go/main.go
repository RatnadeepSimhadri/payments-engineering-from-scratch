// Lesson: phases/09-reliability-and-scale/06-metrics-and-histograms/docs/en.md
// Metrics: Counters, Gauges, and Histograms Without Averages
// Run:  go run .
// Test: go test ./...
// Sources: Gil Tene, HdrHistogram (log-linear bucketing, "How NOT to Measure Latency"); Prometheus exposition format
//
//	and metric types documentation; Dean & Barroso, "The Tail at Scale" (CACM 2013). Go 1.22+, standard library only.
package main

import (
	"errors"
	"fmt"
	"math"
	"math/bits"
	"slices"
	"strconv"
	"strings"
)

// Rng is SplitMix64: a tiny seeded generator so every number in the demo and tests is reproducible.
// State is uint64 so the multiplications wrap exactly as Java's long arithmetic does.
type Rng struct{ state uint64 }

func NewRng(seed int64) *Rng { return &Rng{state: uint64(seed)} }

// NextLong returns the next 64-bit value as a signed int64, matching the Java stream bit for bit.
func (r *Rng) NextLong() int64 {
	r.state += 0x9E3779B97F4A7C15
	z := r.state
	z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
	z = (z ^ (z >> 27)) * 0x94D049BB133111EB
	return int64(z ^ (z >> 31))
}

func (r *Rng) NextDouble() float64 { return float64(uint64(r.NextLong())>>11) * 0x1.0p-53 }

// NextGaussian is a standard normal via Box-Muller; one draw per call, the second is discarded for simplicity.
func (r *Rng) NextGaussian() float64 {
	u1, u2 := 1.0-r.NextDouble(), r.NextDouble()
	return math.Sqrt(-2.0*math.Log(u1)) * math.Cos(2*math.Pi*u2)
}

// javaRound is Math.round(double) for the magnitudes used here: floor(x + 0.5).
func javaRound(x float64) int64 { return int64(math.Floor(x + 0.5)) }

// NextLogNormalMicros is log-normal: the usual shape of service latency, a hump with a long right tail.
func (r *Rng) NextLogNormalMicros(medianMicros, sigma float64) int64 {
	return max(1, javaRound(medianMicros*math.Exp(sigma*r.NextGaussian())))
}

// ErrOverflow is returned when an int64 would wrap; Go's int64 wraps silently where Java's Math.addExact throws.
var ErrOverflow = errors.New("int64 overflow")

func addChecked(a, b int64) (int64, error) {
	sum := a + b
	if (a > 0 && b > 0 && sum < 0) || (a < 0 && b < 0 && sum >= 0) {
		return 0, ErrOverflow
	}
	return sum, nil
}

func mulChecked(a, b int64) (int64, error) {
	if a == 0 || b == 0 {
		return 0, nil
	}
	product := a * b
	if product/b != a || (a == -1 && b == math.MinInt64) || (b == -1 && a == math.MinInt64) {
		return 0, ErrOverflow
	}
	return product, nil
}

func mustInt(v int64, err error) int64 {
	if err != nil {
		panic(err)
	}
	return v
}

// Counter is a monotonically non-decreasing count. Rates are derived by the scraper, never stored here.
type Counter struct{ value int64 }

func (c *Counter) Inc() { c.IncBy(1) }

// IncBy adds n; a negative n is an error because a counter can only go up.
func (c *Counter) IncBy(n int64) error {
	if n < 0 {
		return fmt.Errorf("counter can only go up: %d", n)
	}
	v, err := addChecked(c.value, n)
	if err != nil {
		return err
	}
	c.value = v
	return nil
}

func (c *Counter) Value() int64 { return c.value }

// Rate is the scraper's rate calculation over two samples. A counter that went down was reset by a process
// restart, and the only honest assumption is that it restarted from zero.
func Rate(previous, current int64, seconds float64) (float64, error) {
	if seconds <= 0 {
		return 0, errors.New("interval must be positive")
	}
	delta := current
	if current >= previous {
		delta = current - previous
	}
	return float64(delta) / seconds, nil
}

// Gauge is a value that can go up or down: queue depth, in-flight requests, pending outbox rows.
type Gauge struct{ value int64 }

func (g *Gauge) Set(v int64)  { g.value = v }
func (g *Gauge) Inc()         { g.value++ }
func (g *Gauge) Dec()         { g.value-- }
func (g *Gauge) Value() int64 { return g.value }

// Estimate is a percentile estimate with the bucket bounds that contain the true value.
type Estimate struct {
	Percentile   float64
	Lower, Upper int64
}

// RelativeError is the worst-case relative error of reporting Upper as the answer.
func (e Estimate) RelativeError() float64 {
	if e.Lower == 0 {
		return 0.0
	}
	return float64(e.Upper-e.Lower) / float64(e.Lower)
}

func (e Estimate) String() string {
	return fmt.Sprintf("p%s in [%d, %d]", Trim(e.Percentile), e.Lower, e.Upper)
}

// Trim renders 99 as "99" and 99.9 as "99.9", the way the Java demo labels percentiles.
func Trim(p float64) string {
	if p == math.Trunc(p) {
		return strconv.FormatInt(int64(p), 10)
	}
	return strconv.FormatFloat(p, 'f', -1, 64)
}

// LogLinearHistogram is a log-linear histogram in the style of HdrHistogram. Values below 2^precisionBits are
// counted exactly, one bucket per integer. Above that, each power-of-two range [2^e, 2^(e+1)) is split into
// 2^precisionBits equal sub-buckets, so every bucket's width is at most 2^-precisionBits of its lower bound: the
// relative error of any percentile is bounded by that fraction regardless of how large the value is.
type LogLinearHistogram struct {
	PrecisionBits    int
	HighestTrackable int64
	linear           int // 2^precisionBits sub-buckets per power of two
	counts           []int64
	total            int64
	sum              int64 // exact, for the mean: kept so the demo can show what the mean hides
	min              int64
	max              int64
}

func NewLogLinearHistogram(precisionBits int, highestTrackable int64) (*LogLinearHistogram, error) {
	if precisionBits < 1 || precisionBits > 16 {
		return nil, errors.New("precisionBits must be 1..16")
	}
	if highestTrackable < int64(1)<<precisionBits {
		return nil, errors.New("highestTrackable too small")
	}
	h := &LogLinearHistogram{PrecisionBits: precisionBits, HighestTrackable: highestTrackable, linear: 1 << precisionBits, min: math.MaxInt64, max: math.MinInt64}
	h.counts = make([]int64, h.IndexOf(highestTrackable)+1)
	return h, nil
}

// IndexOf is which bucket holds v. Exact below 2^precisionBits; log-linear above.
func (h *LogLinearHistogram) IndexOf(v int64) int {
	if v < int64(h.linear) {
		return int(v)
	}
	e := bits.Len64(uint64(v)) - 1                    // floor(log2 v), e >= precisionBits
	sub := (v - int64(1)<<e) >> (e - h.PrecisionBits) // which of the 2^precisionBits slices of [2^e, 2^(e+1))
	return int(int64(e-h.PrecisionBits+1)*int64(h.linear) + sub)
}

// LowerBound is the smallest value that lands in bucket index. Inverse of IndexOf.
func (h *LogLinearHistogram) LowerBound(index int) int64 {
	if index < h.linear {
		return int64(index)
	}
	k := index / h.linear
	e := k + h.PrecisionBits - 1
	sub := int64(index % h.linear)
	return int64(1)<<e + sub<<(e-h.PrecisionBits)
}

// UpperBound is the largest value that lands in bucket index.
func (h *LogLinearHistogram) UpperBound(index int) int64 { return h.LowerBound(index+1) - 1 }

func (h *LogLinearHistogram) Record(v int64) error { return h.RecordMany(v, 1) }

func (h *LogLinearHistogram) RecordMany(v, n int64) error {
	if v < 0 || v > h.HighestTrackable {
		return fmt.Errorf("value out of range: %d", v)
	}
	if n <= 0 {
		return errors.New("count must be positive")
	}
	h.counts[h.IndexOf(v)] += n
	h.total = mustInt(addChecked(h.total, n))
	h.sum = mustInt(addChecked(h.sum, mustInt(mulChecked(v, n))))
	if v < h.min {
		h.min = v
	}
	if v > h.max {
		h.max = v
	}
	return nil
}

func (h *LogLinearHistogram) Count() int64 { return h.total }

func (h *LogLinearHistogram) Min() int64 {
	if h.total == 0 {
		return 0
	}
	return h.min
}

func (h *LogLinearHistogram) Max() int64 {
	if h.total == 0 {
		return 0
	}
	return h.max
}

func (h *LogLinearHistogram) Mean() float64 {
	if h.total == 0 {
		return 0
	}
	return float64(h.sum) / float64(h.total)
}

func (h *LogLinearHistogram) BucketCount() int        { return len(h.counts) }
func (h *LogLinearHistogram) CountAt(index int) int64 { return h.counts[index] }

// Percentile is nearest-rank: the bucket containing the value at rank ceil(p/100 * count). The estimate carries
// the bucket's bounds; the true value lies inside them. Reports Upper as the point value, never below the truth.
func (h *LogLinearHistogram) Percentile(p float64) (Estimate, error) {
	if p < 0 || p > 100 {
		return Estimate{}, errors.New("percentile must be 0..100")
	}
	if h.total == 0 {
		return Estimate{p, 0, 0}, nil
	}
	rank := max(1, int64(math.Ceil(p/100.0*float64(h.total))))
	var seen int64
	for i := range h.counts {
		seen += h.counts[i]
		if seen >= rank {
			return Estimate{p, max(h.LowerBound(i), h.min), min(h.UpperBound(i), h.max)}, nil
		}
	}
	return Estimate{p, h.max, h.max}, nil
}

// Value is the point estimate for p; it panics on a p outside 0..100 because that is a caller bug, not a condition.
func (h *LogLinearHistogram) Value(p float64) int64 {
	e, err := h.Percentile(p)
	if err != nil {
		panic(err)
	}
	return e.Upper
}

// FractionAtOrBelow is the fraction of recorded values at or below threshold: an SLO attainment query answered from the buckets.
func (h *LogLinearHistogram) FractionAtOrBelow(threshold int64) float64 {
	if h.total == 0 {
		return 1.0
	}
	idx := h.IndexOf(min(threshold, h.HighestTrackable))
	var seen int64
	for i := 0; i <= idx; i++ {
		seen += h.counts[i]
	}
	return float64(seen) / float64(h.total)
}

// Merge adds another histogram with identical layout into this one. Percentiles of the merge are exact to the same bound.
func (h *LogLinearHistogram) Merge(other *LogLinearHistogram) error {
	if other.PrecisionBits != h.PrecisionBits || other.HighestTrackable != h.HighestTrackable {
		return errors.New("cannot merge histograms with different layouts")
	}
	for i := range h.counts {
		h.counts[i] = mustInt(addChecked(h.counts[i], other.counts[i]))
	}
	h.total = mustInt(addChecked(h.total, other.total))
	h.sum = mustInt(addChecked(h.sum, other.sum))
	if other.total > 0 {
		h.min = min(h.min, other.min)
		h.max = max(h.max, other.max)
	}
	return nil
}

func (h *LogLinearHistogram) Copy() *LogLinearHistogram {
	c, err := NewLogLinearHistogram(h.PrecisionBits, h.HighestTrackable)
	if err != nil {
		panic(err)
	}
	if err := c.Merge(h); err != nil {
		panic(err)
	}
	return c
}

// Cumulative is the cumulative counts at the given upper bounds: the shape Prometheus classic histograms export (le).
func (h *LogLinearHistogram) Cumulative(boundaries []int64) []int64 {
	out := make([]int64, len(boundaries))
	for b, bound := range boundaries {
		idx := h.IndexOf(min(bound, h.HighestTrackable))
		var seen int64
		for i := 0; i <= idx; i++ {
			seen += h.counts[i]
		}
		out[b] = seen
	}
	return out
}

// MetricsRegistry is a name-to-metric registry with a text exposition in the Prometheus format.
// Names are kept in slices so the exposition preserves registration order, as the Java LinkedHashMap does.
type MetricsRegistry struct {
	counterNames, gaugeNames, histogramNames []string
	counters                                 map[string]*Counter
	gauges                                   map[string]*Gauge
	histograms                               map[string]*LogLinearHistogram
	PrecisionBits                            int
	HighestTrackable                         int64
}

func NewMetricsRegistry(precisionBits int, highestTrackable int64) *MetricsRegistry {
	return &MetricsRegistry{counters: map[string]*Counter{}, gauges: map[string]*Gauge{}, histograms: map[string]*LogLinearHistogram{}, PrecisionBits: precisionBits, HighestTrackable: highestTrackable}
}

func (r *MetricsRegistry) Counter(name string) *Counter {
	c, ok := r.counters[name]
	if !ok {
		c = &Counter{}
		r.counters[name] = c
		r.counterNames = append(r.counterNames, name)
	}
	return c
}

func (r *MetricsRegistry) Gauge(name string) *Gauge {
	g, ok := r.gauges[name]
	if !ok {
		g = &Gauge{}
		r.gauges[name] = g
		r.gaugeNames = append(r.gaugeNames, name)
	}
	return g
}

func (r *MetricsRegistry) Histogram(name string) *LogLinearHistogram {
	h, ok := r.histograms[name]
	if !ok {
		var err error
		if h, err = NewLogLinearHistogram(r.PrecisionBits, r.HighestTrackable); err != nil {
			panic(err)
		}
		r.histograms[name] = h
		r.histogramNames = append(r.histogramNames, name)
	}
	return h
}

// Render is the Prometheus text format. Histograms are exported at the given le boundaries plus +Inf, _sum and _count.
func (r *MetricsRegistry) Render(histogramBoundaries []int64) string {
	var sb strings.Builder
	for _, name := range r.counterNames {
		fmt.Fprintf(&sb, "# TYPE %s_total counter\n", name)
		fmt.Fprintf(&sb, "%s_total %d\n", name, r.counters[name].Value())
	}
	for _, name := range r.gaugeNames {
		fmt.Fprintf(&sb, "# TYPE %s gauge\n", name)
		fmt.Fprintf(&sb, "%s %d\n", name, r.gauges[name].Value())
	}
	for _, name := range r.histogramNames {
		h := r.histograms[name]
		fmt.Fprintf(&sb, "# TYPE %s histogram\n", name)
		cum := h.Cumulative(histogramBoundaries)
		for i, bound := range histogramBoundaries {
			fmt.Fprintf(&sb, "%s_bucket{le=\"%d\"} %d\n", name, bound, cum[i])
		}
		fmt.Fprintf(&sb, "%s_bucket{le=\"+Inf\"} %d\n", name, h.Count())
		fmt.Fprintf(&sb, "%s_sum %d\n", name, javaRound(h.Mean()*float64(h.Count())))
		fmt.Fprintf(&sb, "%s_count %d\n", name, h.Count())
	}
	return sb.String()
}

// MaxMicros is one minute in microseconds, the highest trackable latency in the demo.
const MaxMicros int64 = 60 * 1_000_000

// ExactPercentile is the exact nearest-rank percentile on raw samples, the ground truth the histogram is compared against.
func ExactPercentile(sorted []int64, p float64) int64 {
	rank := int(max(1, math.Ceil(p/100.0*float64(len(sorted)))))
	return sorted[rank-1]
}

func ms(micros int64) string { return fmt.Sprintf("%.1f ms", float64(micros)/1000.0) }

// AuthorizationLatencies simulates a payments authorization endpoint: most calls are fast, a slice hit a slow issuer path.
func AuthorizationLatencies(n int, slowFraction float64, rng *Rng) []int64 {
	out := make([]int64, n)
	for i := range out {
		if rng.NextDouble() < slowFraction {
			out[i] = rng.NextLogNormalMicros(2_000_000, 0.3)
		} else {
			out[i] = rng.NextLogNormalMicros(12_000, 0.5)
		}
	}
	return out
}

// commas renders n with thousands separators, as Java's %,d does.
func commas(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	var out []byte
	for i, ch := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, ch)
	}
	if neg {
		return "-" + string(out)
	}
	return string(out)
}

// indent prefixes every non-empty line with two spaces, as String.indent(2) does.
func indent(text string) string {
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	for i, l := range lines {
		lines[i] = "  " + l
	}
	return strings.Join(lines, "\n") + "\n"
}

func mustHistogram(h *LogLinearHistogram, err error) *LogLinearHistogram {
	if err != nil {
		panic(err)
	}
	return h
}

func recordAll(h *LogLinearHistogram, samples []int64) {
	for _, s := range samples {
		if err := h.Record(s); err != nil {
			panic(err)
		}
	}
}

func main() {
	fmt.Println("== 1. Bucket layout: precisionBits=7 means at most 1/128 = 0.78% relative error ==")
	layout := mustHistogram(NewLogLinearHistogram(7, MaxMicros))
	for _, v := range []int64{5, 127, 128, 129, 255, 256, 1_000, 12_345, 1_000_000, 2_000_000} {
		i := layout.IndexOf(v)
		fmt.Printf("  value %10s -> bucket %5d covering [%s, %s]  width %s\n", commas(v), i, commas(layout.LowerBound(i)), commas(layout.UpperBound(i)), commas(layout.UpperBound(i)-layout.LowerBound(i)+1))
	}
	fmt.Printf("  buckets allocated for values up to one minute in microseconds: %d\n", layout.BucketCount())

	fmt.Println("\n== 2. Averages lie: 100,000 authorizations, 1% hit a slow issuer path ==")
	rng := NewRng(2024)
	samples := AuthorizationLatencies(100_000, 0.01, rng)
	h := mustHistogram(NewLogLinearHistogram(7, MaxMicros))
	recordAll(h, samples)
	sorted := slices.Clone(samples)
	slices.Sort(sorted)
	fmt.Println("  mean   = " + ms(javaRound(h.Mean())) + "   <- under a 100 ms SLO, this looks healthy")
	for _, p := range []float64{50, 90, 99, 99.9} {
		e, _ := h.Percentile(p)
		fmt.Printf("  p%-5s = %-12s histogram says [%s, %s], exact = %s, bound %.2f%%\n", Trim(p), ms(e.Upper), ms(e.Lower), ms(e.Upper), ms(ExactPercentile(sorted, p)), e.RelativeError()*100)
	}
	fmt.Printf("  fraction of requests at or below 100 ms = %.3f%%  (the SLI, answered from the buckets)\n", 100*h.FractionAtOrBelow(100_000))
	fmt.Println("  one in a hundred customers waited more than a second; the average never showed it")

	fmt.Println("\n== 3. Averaging percentiles is wrong; merging histograms is right ==")
	r1, r2 := NewRng(7), NewRng(8)
	hostA, hostB := mustHistogram(NewLogLinearHistogram(7, MaxMicros)), mustHistogram(NewLogLinearHistogram(7, MaxMicros))
	recordAll(hostA, AuthorizationLatencies(90_000, 0.0, r1))  // healthy host, lots of traffic
	recordAll(hostB, AuthorizationLatencies(10_000, 0.10, r2)) // sick host, little traffic
	fleet := hostA.Copy()
	if err := fleet.Merge(hostB); err != nil {
		panic(err)
	}
	fmt.Println("  host A (90,000 requests): p99 = " + ms(hostA.Value(99)) + "   host B (10,000 requests, 10% slow): p99 = " + ms(hostB.Value(99)))
	fmt.Println("  average of the two p99s      = " + ms((hostA.Value(99)+hostB.Value(99))/2) + "   <- meaningless")
	fmt.Println("  max of the two p99s          = " + ms(max(hostA.Value(99), hostB.Value(99))) + "   <- pessimistic")
	fmt.Println("  p99 of the merged histogram  = " + ms(fleet.Value(99)) + "   <- the fleet p99, computed correctly")
	fmt.Println("  average of means (A, B)      = " + ms(javaRound((hostA.Mean()+hostB.Mean())/2)) + "   vs true fleet mean " + ms(javaRound(fleet.Mean())) + "  (unweighted means are wrong too)")

	fmt.Println("\n== 4. Counters only go up; the scraper computes the rate and must handle a restart ==")
	auths := &Counter{}
	_ = auths.IncBy(1_000)
	t0 := auths.Value()
	_ = auths.IncBy(250)
	rate, _ := Rate(t0, auths.Value(), 10)
	fmt.Printf("  authorizations_total %d -> %d over 10 s: rate = %.1f/s\n", t0, auths.Value(), rate)
	afterRestart := &Counter{}
	_ = afterRestart.IncBy(40)
	resetAware, _ := Rate(auths.Value(), afterRestart.Value(), 10)
	fmt.Printf("  after a process restart the counter reads %d: naive rate = %.1f/s, reset-aware rate = %.1f/s\n",
		afterRestart.Value(), float64(afterRestart.Value()-auths.Value())/10.0, resetAware)

	fmt.Println("\n== 5. Exposition: what a scraper sees ==")
	reg := NewMetricsRegistry(7, MaxMicros)
	_ = reg.Counter("auth_requests").IncBy(100_000)
	_ = reg.Counter("auth_failures").IncBy(312)
	reg.Gauge("auth_inflight").Set(17)
	if err := reg.Histogram("auth_latency_micros").Merge(h); err != nil {
		panic(err)
	}
	fmt.Print(indent(reg.Render([]int64{10_000, 25_000, 50_000, 100_000, 250_000, 1_000_000, 5_000_000})))
}
