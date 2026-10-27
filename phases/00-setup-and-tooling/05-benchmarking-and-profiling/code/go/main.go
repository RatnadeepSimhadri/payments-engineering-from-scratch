// Lesson: phases/00-setup-and-tooling/05-benchmarking-and-profiling/docs/en.md
// Benchmarking and Profiling Without Lying to Yourself: a harness with warm-up, forks,
// percentiles, dead-code sinks, coordinated-omission correction, and a sampling-profile aggregator.
// Run:  go run .
// Test: go test ./...
// Sources: Gil Tene, "How NOT to Measure Latency" (Strange Loop 2015) for coordinated omission;
//
//	OpenJDK JMH documentation for warm-up/fork/blackhole rationale.
//
// Go 1.22+, standard library only. Same design as the Java and Python versions.
package main

import (
	"container/list"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// Ticker is a monotonic nanosecond source. Production uses SystemTicker; tests hand the harness a FakeTicker.
type Ticker interface {
	NanoTime() int64
}

// SystemTicker reads the monotonic clock: time.Since on a fixed origin never goes backwards.
type SystemTicker struct{}

var origin = time.Now()

// NanoTime returns nanoseconds elapsed since program start.
func (SystemTicker) NanoTime() int64 { return int64(time.Since(origin)) }

// FakeTicker returns scripted readings in order. The harness reads the ticker exactly twice per iteration.
type FakeTicker struct {
	readings []int64
	next     int
}

// NewFakeTicker copies the readings so the caller cannot mutate the script.
func NewFakeTicker(readings ...int64) *FakeTicker {
	return &FakeTicker{readings: append([]int64(nil), readings...)}
}

// NanoTime panics when exhausted: a benchmark that reads more than it scripted is a harness bug.
func (f *FakeTicker) NanoTime() int64 {
	if f.next >= len(f.readings) {
		panic(fmt.Sprintf("FakeTicker exhausted after %d readings", len(f.readings)))
	}
	v := f.readings[f.next]
	f.next++
	return v
}

// ReadingsConsumed is how many readings the harness has taken so far.
func (f *FakeTicker) ReadingsConsumed() int { return f.next }

// Blackhole is a sink the compiler cannot prove unused. Every benchmark body hands its result here;
// without a sink the compiler is entitled to delete a computation whose result nobody reads.
type Blackhole struct {
	sink int64
}

// Consume folds a value into the sink.
func (b *Blackhole) Consume(value int64) { b.sink ^= value }

// ConsumeAny folds an arbitrary value's hash in; nil is tolerated.
func (b *Blackhole) ConsumeAny(value any) {
	if value == nil {
		return
	}
	switch v := value.(type) {
	case int:
		b.sink ^= int64(v)
	case int64:
		b.sink ^= v
	default:
		b.sink ^= int64(len(fmt.Sprint(v)))
	}
}

// Value is the current fold.
func (b *Blackhole) Value() int64 { return b.sink }

// BenchmarkBody is a unit of work to time. The body must hand its result to the blackhole.
type BenchmarkBody func(bh *Blackhole)

// Benchmark names a body.
type Benchmark struct {
	Name string
	Body BenchmarkBody
}

// NewBenchmark validates the name and body.
func NewBenchmark(name string, body BenchmarkBody) (Benchmark, error) {
	if strings.TrimSpace(name) == "" {
		return Benchmark{}, errors.New("benchmark needs a name")
	}
	if body == nil {
		return Benchmark{}, errors.New("benchmark needs a body")
	}
	return Benchmark{name, body}, nil
}

func mustBenchmark(name string, body BenchmarkBody) Benchmark {
	b, err := NewBenchmark(name, body)
	if err != nil {
		panic(err)
	}
	return b
}

// ErrNoSamples is returned by the Stats functions on an empty sample.
var ErrNoSamples = errors.New("no samples")

// Percentile is the nearest-rank percentile: the smallest value such that at least p% of samples are <= it.
func Percentile(samples []int64, p float64) (int64, error) {
	if len(samples) == 0 {
		return 0, ErrNoSamples
	}
	if p < 0 || p > 100 {
		return 0, fmt.Errorf("percentile must be in 0..100: %v", p)
	}
	sorted := append([]int64(nil), samples...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	if p == 0 {
		return sorted[0], nil
	}
	rank := int(math.Ceil(p / 100.0 * float64(len(sorted)))) // 1-based rank
	return sorted[rank-1], nil
}

// MustPercentile is Percentile for callers that know the sample is valid.
func MustPercentile(samples []int64, p float64) int64 {
	v, err := Percentile(samples, p)
	if err != nil {
		panic(err)
	}
	return v
}

// Min is the 0th percentile.
func Min(samples []int64) int64 { return MustPercentile(samples, 0) }

// Max is the 100th percentile.
func Max(samples []int64) int64 { return MustPercentile(samples, 100) }

// Mean is the arithmetic mean.
func Mean(samples []int64) float64 {
	if len(samples) == 0 {
		panic(ErrNoSamples)
	}
	sum := 0.0
	for _, s := range samples {
		sum += float64(s)
	}
	return sum / float64(len(samples))
}

// Stddev is the population standard deviation.
func Stddev(samples []int64) float64 {
	mean := Mean(samples)
	acc := 0.0
	for _, s := range samples {
		acc += (float64(s) - mean) * (float64(s) - mean)
	}
	return math.Sqrt(acc / float64(len(samples)))
}

// CoefficientOfVariation is stddev / mean. Dimensionless, so comparable across benchmarks.
func CoefficientOfVariation(samples []int64) float64 {
	mean := Mean(samples)
	if mean == 0 {
		return 0
	}
	return Stddev(samples) / mean
}

// HarnessConfig says how a benchmark is run. Forks are repeated runs that each start cold on the warm-up schedule.
type HarnessConfig struct {
	WarmupIterations   int
	MeasuredIterations int
	Forks              int
	OpsPerIteration    int
}

// NewHarnessConfig validates the counts.
func NewHarnessConfig(warmup, measured, forks, ops int) (HarnessConfig, error) {
	switch {
	case warmup < 0:
		return HarnessConfig{}, errors.New("warmupIterations must be >= 0")
	case measured < 1:
		return HarnessConfig{}, errors.New("measuredIterations must be >= 1")
	case forks < 1:
		return HarnessConfig{}, errors.New("forks must be >= 1")
	case ops < 1:
		return HarnessConfig{}, errors.New("opsPerIteration must be >= 1")
	}
	return HarnessConfig{warmup, measured, forks, ops}, nil
}

var (
	// Quick: cheap bodies (a get, a relink): many ops per iteration so a sample is not dominated by the two ticker reads.
	Quick = HarnessConfig{10, 7, 3, 5_000}
	// Heavy: bodies that already walk thousands of elements; fewer ops per iteration keeps the demo under ten seconds.
	Heavy = HarnessConfig{5, 7, 3, 200}
)

// ForkResult is one fork's measured samples, in ns/op, plus the discarded warm-up samples for inspection.
type ForkResult struct {
	Fork            int
	WarmupNsPerOp   []int64
	MeasuredNsPerOp []int64
}

// Median of the measured samples.
func (f ForkResult) Median() int64 { return MustPercentile(f.MeasuredNsPerOp, 50) }

// BenchmarkResult pools every fork's samples, plus the cross-fork spread that tells you whether to believe the number.
type BenchmarkResult struct {
	Name          string
	Forks         []ForkResult
	PooledNsPerOp []int64
}

// UnstableCV is the fork-spread threshold above which a result is noise.
const UnstableCV = 0.10

func (r BenchmarkResult) P50() int64 { return MustPercentile(r.PooledNsPerOp, 50) }
func (r BenchmarkResult) P90() int64 { return MustPercentile(r.PooledNsPerOp, 90) }
func (r BenchmarkResult) P99() int64 { return MustPercentile(r.PooledNsPerOp, 99) }
func (r BenchmarkResult) Min() int64 { return Min(r.PooledNsPerOp) }
func (r BenchmarkResult) Max() int64 { return Max(r.PooledNsPerOp) }

// ForkMedians lists each fork's median in fork order.
func (r BenchmarkResult) ForkMedians() []int64 {
	medians := make([]int64, len(r.Forks))
	for i, f := range r.Forks {
		medians[i] = f.Median()
	}
	return medians
}

// ForkSpread is the spread of the per-fork medians. Above UnstableCV the forks disagree and the result is noise.
func (r BenchmarkResult) ForkSpread() float64 {
	if len(r.Forks) < 2 {
		return 0
	}
	return CoefficientOfVariation(r.ForkMedians())
}

// Stable reports whether the forks agree.
func (r BenchmarkResult) Stable() bool { return r.ForkSpread() <= UnstableCV }

// Row renders one result line.
func (r BenchmarkResult) Row() string {
	marker := ""
	if !r.Stable() {
		marker = "UNSTABLE"
	}
	return fmt.Sprintf("%-32s p50 %8d  p90 %8d  p99 %8d  max %9d  forks %s  spread %5.1f%% %s",
		r.Name, r.P50(), r.P90(), r.P99(), r.Max(), formatInts(r.ForkMedians()), r.ForkSpread()*100, marker)
}

func formatInts(xs []int64) string {
	parts := make([]string, len(xs))
	for i, x := range xs {
		parts[i] = fmt.Sprint(x)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// ErrTickerBackwards is returned when a ticker reading precedes the previous one.
var ErrTickerBackwards = errors.New("ticker went backwards")

// Harness runs benchmarks. For each fork: warmupIterations (timed, discarded), then measuredIterations (kept).
// One iteration = OpsPerIteration calls of the body between two ticker readings; the sample is ns/op.
// Invariant: exactly 2 * (warmup + measured) * forks ticker readings per benchmark, so a FakeTicker can script every sample.
type Harness struct {
	ticker    Ticker
	config    HarnessConfig
	blackhole Blackhole
}

// NewHarness binds a ticker and a config.
func NewHarness(ticker Ticker, config HarnessConfig) *Harness {
	return &Harness{ticker: ticker, config: config}
}

// Run executes every fork and pools the measured samples.
func (h *Harness) Run(benchmark Benchmark) (BenchmarkResult, error) {
	var forks []ForkResult
	var pooled []int64
	for fork := 1; fork <= h.config.Forks; fork++ {
		warm := make([]int64, h.config.WarmupIterations)
		for i := range warm {
			v, err := h.timeIteration(benchmark.Body)
			if err != nil {
				return BenchmarkResult{}, err
			}
			warm[i] = v
		}
		measured := make([]int64, h.config.MeasuredIterations)
		for i := range measured {
			v, err := h.timeIteration(benchmark.Body)
			if err != nil {
				return BenchmarkResult{}, err
			}
			measured[i] = v
			pooled = append(pooled, v)
		}
		forks = append(forks, ForkResult{fork, warm, measured})
	}
	return BenchmarkResult{benchmark.Name, forks, pooled}, nil
}

// MustRun is Run for demos whose ticker is trusted.
func (h *Harness) MustRun(benchmark Benchmark) BenchmarkResult {
	r, err := h.Run(benchmark)
	if err != nil {
		panic(err)
	}
	return r
}

func (h *Harness) timeIteration(body BenchmarkBody) (int64, error) {
	start := h.ticker.NanoTime()
	for op := 0; op < h.config.OpsPerIteration; op++ {
		body(&h.blackhole)
	}
	end := h.ticker.NanoTime()
	elapsed := end - start
	if elapsed < 0 {
		return 0, fmt.Errorf("%w: %d -> %d", ErrTickerBackwards, start, end)
	}
	return elapsed / int64(h.config.OpsPerIteration), nil
}

// Blackhole exposes the sink so tests can prove the bodies ran.
func (h *Harness) Blackhole() *Blackhole { return &h.blackhole }

// Header is the table header matching Row.
func Header() string {
	return fmt.Sprintf("%-32s %-13s %-13s %-13s %-14s %s", "benchmark (ns/op)", "p50", "p90", "p99", "max", "per-fork medians")
}

// LatencyRecorder records latencies from a load generator that sends one request per expectedInterval.
// Coordinated omission: when a request stalls for longer than the interval, the requests that should
// have been sent during the stall were never sent, so their (also high) latencies are missing from
// the sample. Record back-fills them the way HdrHistogram's recordValueWithExpectedInterval does.
type LatencyRecorder struct {
	raw       []int64
	corrected []int64
}

// Record stores one latency and back-fills the omitted requests.
func (l *LatencyRecorder) Record(latencyNanos, expectedIntervalNanos int64) error {
	if latencyNanos < 0 {
		return errors.New("latency must be >= 0")
	}
	if expectedIntervalNanos <= 0 {
		return errors.New("expected interval must be > 0")
	}
	l.raw = append(l.raw, latencyNanos)
	l.corrected = append(l.corrected, latencyNanos)
	for missing := latencyNanos - expectedIntervalNanos; missing >= expectedIntervalNanos; missing -= expectedIntervalNanos {
		l.corrected = append(l.corrected, missing)
	}
	return nil
}

func (l *LatencyRecorder) RawSamples() []int64       { return append([]int64(nil), l.raw...) }
func (l *LatencyRecorder) CorrectedSamples() []int64 { return append([]int64(nil), l.corrected...) }
func (l *LatencyRecorder) RawCount() int             { return len(l.raw) }
func (l *LatencyRecorder) CorrectedCount() int       { return len(l.corrected) }

// FrameCount is one row of a profile report.
type FrameCount struct {
	Frame string
	Count int
}

// SampleProfile is what a sampling profiler (pprof, JFR) computes: for each periodic stack snapshot, the leaf
// frame gets one unit of self time and every frame on the stack gets one unit of inclusive time.
// Stacks are given root-first. Deterministic given the same samples.
type SampleProfile struct {
	self      map[string]int
	inclusive map[string]int
	samples   int
}

// NewSampleProfile allocates the count maps.
func NewSampleProfile() *SampleProfile {
	return &SampleProfile{self: map[string]int{}, inclusive: map[string]int{}}
}

// Record adds one stack snapshot.
func (p *SampleProfile) Record(stackRootFirst ...string) error {
	if len(stackRootFirst) == 0 {
		return errors.New("empty stack")
	}
	p.samples++
	p.self[stackRootFirst[len(stackRootFirst)-1]]++
	seen := map[string]bool{}
	for _, frame := range stackRootFirst {
		if !seen[frame] {
			seen[frame] = true
			p.inclusive[frame]++
		}
	}
	return nil
}

func (p *SampleProfile) Samples() int                    { return p.samples }
func (p *SampleProfile) SelfCount(frame string) int      { return p.self[frame] }
func (p *SampleProfile) InclusiveCount(frame string) int { return p.inclusive[frame] }
func (p *SampleProfile) TopSelf(n int) []FrameCount      { return top(p.self, n) }
func (p *SampleProfile) TopInclusive(n int) []FrameCount { return top(p.inclusive, n) }

// top orders frames by count descending, ties broken by name so the output is deterministic.
func top(counts map[string]int, n int) []FrameCount {
	entries := make([]FrameCount, 0, len(counts))
	for k, v := range counts {
		entries = append(entries, FrameCount{k, v})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Count == entries[j].Count {
			return entries[i].Frame < entries[j].Frame
		}
		return entries[i].Count > entries[j].Count
	})
	if n < len(entries) {
		entries = entries[:n]
	}
	return entries
}

// Report renders the top-n frames by self time.
func (p *SampleProfile) Report(n int) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "%d samples\n%-28s %6s %6s\n", p.samples, "frame", "self", "incl")
	for _, e := range p.TopSelf(n) {
		fmt.Fprintf(&sb, "%-28s %5.1f%% %5.1f%%\n", e.Frame,
			100.0*float64(e.Count)/float64(p.samples), 100.0*float64(p.InclusiveCount(e.Frame))/float64(p.samples))
	}
	return sb.String()
}

// JavaRandom is java.util.Random, bit for bit (48-bit LCG), so SeededInts matches the Java demo exactly.
type JavaRandom struct {
	seed int64
}

const (
	javaMultiplier = 0x5DEECE66D
	javaAddend     = 0xB
	javaMask       = (1 << 48) - 1
)

// NewJavaRandom scrambles the seed as java.util.Random does.
func NewJavaRandom(seed int64) *JavaRandom {
	return &JavaRandom{seed: (seed ^ javaMultiplier) & javaMask}
}

func (r *JavaRandom) next(bits uint) int32 {
	r.seed = (r.seed*javaMultiplier + javaAddend) & javaMask
	return int32(r.seed >> (48 - bits))
}

// NextInt is java.util.Random.nextInt(bound), including the rejection loop.
func (r *JavaRandom) NextInt(bound int32) int32 {
	if bound <= 0 {
		panic("bound must be positive")
	}
	rr := r.next(31)
	m := bound - 1
	if bound&m == 0 {
		return int32((int64(bound) * int64(rr)) >> 31)
	}
	u := rr
	rr = u % bound
	for u-rr+m < 0 {
		u = r.next(31)
		rr = u % bound
	}
	return rr
}

// SeededInts returns deterministic pseudo-random ints in [0, bound); the same sequence as Java's Random(seed).
func SeededInts(bound int, seed int64) []int {
	random := NewJavaRandom(seed)
	out := make([]int, bound)
	for i := range out {
		out[i] = int(random.NextInt(int32(bound)))
	}
	return out
}

// RandomGet reads seq[indices[cursor]] and advances; a struct so the body does not capture a mutable local.
type RandomGet struct {
	get     func(i int) int
	indices []int
	cursor  int
}

// Run is the benchmark body.
func (g *RandomGet) Run(bh *Blackhole) {
	bh.Consume(int64(g.get(g.indices[g.cursor])))
	g.cursor = (g.cursor + 1) % len(g.indices)
}

// SortedMap is the ordered stand-in for TreeMap: a sorted key slice searched with sort.SearchInts.
type SortedMap struct {
	keys   []int
	values []int
}

// Put inserts or replaces, keeping keys sorted.
func (m *SortedMap) Put(key, value int) {
	i := sort.SearchInts(m.keys, key)
	if i < len(m.keys) && m.keys[i] == key {
		m.values[i] = value
		return
	}
	m.keys = append(m.keys, 0)
	m.values = append(m.values, 0)
	copy(m.keys[i+1:], m.keys[i:])
	copy(m.values[i+1:], m.values[i:])
	m.keys[i] = key
	m.values[i] = value
}

// Get is an O(log n) binary search.
func (m *SortedMap) Get(key int) (int, bool) {
	i := sort.SearchInts(m.keys, key)
	if i < len(m.keys) && m.keys[i] == key {
		return m.values[i], true
	}
	return 0, false
}

// Keys in ascending order.
func (m *SortedMap) Keys() []int { return m.keys }

// MapGet reads mapping(keys[cursor]) and advances.
type MapGet struct {
	get    func(k int) int
	keys   []int
	cursor int
}

// Run is the benchmark body.
func (g *MapGet) Run(bh *Blackhole) {
	bh.Consume(int64(g.get(g.keys[g.cursor])))
	g.cursor = (g.cursor + 1) % len(g.keys)
}

func main() {
	fmt.Println("== 1. Why warm-up matters: the first iterations pay for caches and allocation, not your code ==")
	warmDemo := NewHarness(SystemTicker{}, HarnessConfig{5, 5, 1, 20_000})
	data := SeededInts(1_000, 7)
	sum := warmDemo.MustRun(mustBenchmark("sum of 1000 ints", func(bh *Blackhole) {
		acc := int64(0)
		for _, v := range data {
			acc += int64(v)
		}
		bh.Consume(acc)
	}))
	only := sum.Forks[0]
	fmt.Println("warm-up samples (discarded) ns/op: " + formatInts(only.WarmupNsPerOp))
	fmt.Println("measured samples           ns/op: " + formatInts(only.MeasuredNsPerOp))
	fmt.Println("Go compiles ahead of time, so the warm-up drop is small; in Java the first sample is often several times the last.")

	fmt.Println()
	fmt.Println("== 2. slice vs container/list, honestly ==")
	harness := NewHarness(SystemTicker{}, Quick)
	heavy := NewHarness(SystemTicker{}, Heavy)
	n := 2_000
	arrayList := make([]int, 0, n)
	linkedList := list.New()
	for i := 0; i < n; i++ {
		arrayList = append(arrayList, i)
		linkedList.PushBack(i)
	}
	indices := SeededInts(n, 42)
	listGet := func(i int) int { // container/list has no index: walk from the front, the O(n) pointer chase
		e := linkedList.Front()
		for ; i > 0; i-- {
			e = e.Next()
		}
		return e.Value.(int)
	}
	fmt.Println(Header())
	sliceGet := &RandomGet{get: func(i int) int { return arrayList[i] }, indices: indices}
	linkGet := &RandomGet{get: listGet, indices: indices}
	for _, r := range []BenchmarkResult{
		harness.MustRun(mustBenchmark("slice[i] (random)", sliceGet.Run)),
		harness.MustRun(mustBenchmark("list.Front()+i (random)", linkGet.Run)),
		harness.MustRun(mustBenchmark("slice insert(0)+remove(0)", func(bh *Blackhole) {
			arrayList = append(arrayList, 0)
			copy(arrayList[1:], arrayList)
			arrayList[0] = -1
			v := arrayList[0]
			copy(arrayList, arrayList[1:])
			arrayList = arrayList[:len(arrayList)-1]
			bh.Consume(int64(v))
		})),
		harness.MustRun(mustBenchmark("list PushFront+Remove(Front)", func(bh *Blackhole) {
			linkedList.PushFront(-1)
			bh.Consume(int64(linkedList.Remove(linkedList.Front()).(int)))
		})),
		heavy.MustRun(mustBenchmark("slice iterate+sum", func(bh *Blackhole) {
			acc := int64(0)
			for _, v := range arrayList {
				acc += int64(v)
			}
			bh.Consume(acc)
		})),
		heavy.MustRun(mustBenchmark("list iterate+sum", func(bh *Blackhole) {
			acc := int64(0)
			for e := linkedList.Front(); e != nil; e = e.Next() {
				acc += int64(e.Value.(int))
			}
			bh.Consume(acc)
		})),
	} {
		fmt.Println(r.Row())
	}
	fmt.Println("[i]: O(1) vs O(n) pointer chase. Front insert: O(n) copy vs O(1) relink. Iteration: both O(n); the slice wins on cache locality.")
	fmt.Println("A benchmark that shows only one row is an argument, not a measurement.")

	fmt.Println()
	fmt.Println("== 3. map vs sorted map (binary search) ==")
	hashMap := map[int]int{}
	treeMap := &SortedMap{}
	for i := 0; i < n; i++ {
		hashMap[i*7919%100_003] = i
		treeMap.Put(i*7919%100_003, i)
	}
	keys := make([]int, n)
	for i := 0; i < n; i++ {
		keys[i] = (indices[i] % n) * 7919 % 100_003
	}
	fmt.Println(Header())
	hashGet := &MapGet{get: func(k int) int { return hashMap[k] }, keys: keys}
	treeGet := &MapGet{get: func(k int) int { v, _ := treeMap.Get(k); return v }, keys: keys}
	for _, r := range []BenchmarkResult{
		harness.MustRun(mustBenchmark("map lookup", hashGet.Run)),
		harness.MustRun(mustBenchmark("SortedMap.Get", treeGet.Run)),
		heavy.MustRun(mustBenchmark("map keys in order (sort)", func(bh *Blackhole) {
			ks := make([]int, 0, len(hashMap))
			for k := range hashMap {
				ks = append(ks, k)
			}
			sort.Ints(ks)
			bh.Consume(int64(ks[0]))
		})),
		heavy.MustRun(mustBenchmark("SortedMap keys in order (iterate)", func(bh *Blackhole) {
			first := -1
			for _, k := range treeMap.Keys() {
				if first < 0 {
					first = k
				}
			}
			bh.Consume(int64(first))
		})),
	} {
		fmt.Println(r.Row())
	}
	fmt.Println("get: O(1) expected vs O(log n). Ordered traversal: O(n log n) sort vs O(n) walk. Pick the map for the access pattern, not the headline.")

	fmt.Println()
	fmt.Println("== 4. Coordinated omission: one stall, two very different p99s (scripted, deterministic) ==")
	recorder := &LatencyRecorder{}
	interval := int64(1_000_000) // the load generator sends one request per 1 ms
	for i := 0; i < 999; i++ {
		_ = recorder.Record(200_000, interval) // 999 requests at 0.2 ms
	}
	_ = recorder.Record(2_000_000_000, interval) // one 2 s stall (a GC pause, a lock)
	raw := recorder.RawSamples()
	corrected := recorder.CorrectedSamples()
	fmt.Printf("raw:       %d samples, p50 %.1f ms, p99 %.1f ms, max %.0f ms\n",
		len(raw), float64(MustPercentile(raw, 50))/1e6, float64(MustPercentile(raw, 99))/1e6, float64(Max(raw))/1e6)
	fmt.Printf("corrected: %d samples, p50 %.1f ms, p99 %.1f ms, max %.0f ms\n",
		len(corrected), float64(MustPercentile(corrected, 50))/1e6, float64(MustPercentile(corrected, 99))/1e6, float64(Max(corrected))/1e6)
	fmt.Println("During the 2 s stall the generator should have sent 1,999 more requests, each of which would have waited.")
	fmt.Println("Raw p99 says the service is fine. Corrected p99 says a user hitting the stall waited over a second.")

	fmt.Println()
	fmt.Println("== 5. What a sampling profiler computes (scripted stacks) ==")
	profile := NewSampleProfile()
	for i := 0; i < 60; i++ {
		_ = profile.Record("main", "settleBatch", "computeFees", "Money.times")
	}
	for i := 0; i < 25; i++ {
		_ = profile.Record("main", "settleBatch", "writeFile", "FileChannel.write")
	}
	for i := 0; i < 10; i++ {
		_ = profile.Record("main", "settleBatch", "computeFees", "HashMap.get")
	}
	for i := 0; i < 5; i++ {
		_ = profile.Record("main", "settleBatch", "parseInput")
	}
	fmt.Print(profile.Report(5))
	fmt.Println("Self time names the hot leaf; inclusive time names the caller worth restructuring. computeFees is 70% inclusive, 0% self.")
}
