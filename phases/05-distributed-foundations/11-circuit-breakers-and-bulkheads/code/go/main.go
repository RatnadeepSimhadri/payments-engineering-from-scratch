// Lesson: phases/05-distributed-foundations/11-circuit-breakers-and-bulkheads/docs/en.md
// Circuit Breakers and Bulkheads
// Run:  go run .
// Test: go test -race ./...
// Sources: Nygard, "Release It!" (2007) ch. 5, Stability Patterns; Resilience4j CircuitBreaker and Bulkhead docs;
// Google SRE Book ch. 22 "Addressing Cascading Failures". Go 1.22+, standard library only.
// Same design as the Java and Python versions: same states, same window arithmetic, same simulation.
package main

import (
	"errors"
	"fmt"
	"strings"
	"sync"
)

// Clock is what the breaker reads. Tests and demos use FakeClock so no wall time passes.
type Clock interface {
	NowMs() int64
}

// FakeClock only moves when Advance is called, so every transition is replayable.
type FakeClock struct{ now int64 }

func NewFakeClock(startMs int64) *FakeClock { return &FakeClock{now: startMs} }

func (c *FakeClock) NowMs() int64 { return c.now }

func (c *FakeClock) Advance(ms int64) {
	if ms < 0 {
		panic("cannot advance a negative duration")
	}
	c.now += ms
}

// State is one of the breaker's three states. There are exactly four legal transitions; see CircuitBreaker.
type State int

const (
	Closed State = iota
	Open
	HalfOpen
)

func (s State) String() string {
	switch s {
	case Closed:
		return "CLOSED"
	case Open:
		return "OPEN"
	default:
		return "HALF_OPEN"
	}
}

// Admission says why a call was or was not let through. Rejected* outcomes never touched the dependency.
type Admission int

const (
	Admitted Admission = iota
	RejectedOpen
	RejectedBulkhead
)

// CallOutcome is the final result of one guarded call, from the caller's point of view.
type CallOutcome int

const (
	OutcomeSuccess CallOutcome = iota
	OutcomeFailure
	OutcomeRejectedOpen
	OutcomeRejectedBulkhead
)

// Transition is one recorded state change, for tests and for the operator's timeline.
type Transition struct {
	From, To State
	AtMs     int64
}

// BreakerConfig is tuning in the operator's vocabulary: open when at least MinimumCalls calls in the last
// WindowMs failed at FailureRateThresholdPercent or worse; stay open for OpenDurationMs; then let
// HalfOpenPermits probes through.
type BreakerConfig struct {
	FailureRateThresholdPercent int
	MinimumCalls                int
	WindowMs                    int64
	BucketMs                    int64
	OpenDurationMs              int64
	HalfOpenPermits             int
}

var ErrConfig = errors.New("invalid breaker config")

// NewBreakerConfig validates every field so later methods can trust them.
func NewBreakerConfig(thresholdPercent, minimumCalls int, windowMs, bucketMs, openDurationMs int64, halfOpenPermits int) (BreakerConfig, error) {
	if thresholdPercent < 1 || thresholdPercent > 100 {
		return BreakerConfig{}, fmt.Errorf("%w: threshold must be 1..100", ErrConfig)
	}
	if minimumCalls < 1 {
		return BreakerConfig{}, fmt.Errorf("%w: minimumCalls must be >= 1", ErrConfig)
	}
	if bucketMs <= 0 || windowMs < bucketMs || windowMs%bucketMs != 0 {
		return BreakerConfig{}, fmt.Errorf("%w: windowMs must be a positive multiple of bucketMs", ErrConfig)
	}
	if openDurationMs <= 0 {
		return BreakerConfig{}, fmt.Errorf("%w: openDurationMs must be positive", ErrConfig)
	}
	if halfOpenPermits < 1 {
		return BreakerConfig{}, fmt.Errorf("%w: halfOpenPermits must be >= 1", ErrConfig)
	}
	return BreakerConfig{thresholdPercent, minimumCalls, windowMs, bucketMs, openDurationMs, halfOpenPermits}, nil
}

// SlidingWindow is a time-based sliding window of call results: a ring of fixed-width buckets. A bucket is
// reused when the clock reaches its slot again, and any bucket older than windowMs is ignored when counting.
// Eviction is implicit in the arithmetic: nothing runs in the background.
type SlidingWindow struct {
	windowMs    int64
	bucketMs    int64
	bucketStart []int64
	live        []bool
	successes   []int
	failures    []int
}

func NewSlidingWindow(windowMs, bucketMs int64) (*SlidingWindow, error) {
	if bucketMs <= 0 || windowMs < bucketMs || windowMs%bucketMs != 0 {
		return nil, fmt.Errorf("%w: windowMs must be a positive multiple of bucketMs", ErrConfig)
	}
	n := int(windowMs / bucketMs)
	return &SlidingWindow{windowMs, bucketMs, make([]int64, n), make([]bool, n), make([]int, n), make([]int, n)}, nil
}

func floorMod(a, b int64) int64 {
	m := a % b
	if m < 0 {
		m += b
	}
	return m
}

func (w *SlidingWindow) Record(nowMs int64, success bool) {
	start := nowMs - floorMod(nowMs, w.bucketMs)
	i := int(floorMod(start/w.bucketMs, int64(len(w.bucketStart))))
	if !w.live[i] || w.bucketStart[i] != start { // the slot belongs to an older period: reuse it
		w.bucketStart[i] = start
		w.live[i] = true
		w.successes[i] = 0
		w.failures[i] = 0
	}
	if success {
		w.successes[i]++
	} else {
		w.failures[i]++
	}
}

func (w *SlidingWindow) isLive(i int, nowMs int64) bool {
	return w.live[i] && nowMs-w.bucketStart[i] < w.windowMs
}

func (w *SlidingWindow) Total(nowMs int64) int {
	t := 0
	for i := range w.bucketStart {
		if w.isLive(i, nowMs) {
			t += w.successes[i] + w.failures[i]
		}
	}
	return t
}

func (w *SlidingWindow) Failures(nowMs int64) int {
	f := 0
	for i := range w.bucketStart {
		if w.isLive(i, nowMs) {
			f += w.failures[i]
		}
	}
	return f
}

// FailureRatePercent is an integer percent, 0 when the window is empty, so every language agrees on the threshold.
func (w *SlidingWindow) FailureRatePercent(nowMs int64) int {
	t := w.Total(nowMs)
	if t == 0 {
		return 0
	}
	return 100 * w.Failures(nowMs) / t
}

func (w *SlidingWindow) Reset() {
	for i := range w.bucketStart {
		w.live[i] = false
		w.bucketStart[i] = 0
		w.successes[i] = 0
		w.failures[i] = 0
	}
}

// CircuitBreaker is the state machine. CLOSED -> OPEN when the window's failure rate crosses the threshold
// with enough calls; OPEN -> HALF_OPEN when OpenDurationMs has elapsed and someone asks for a permit;
// HALF_OPEN -> OPEN on any probe failure; HALF_OPEN -> CLOSED when HalfOpenPermits probes have all succeeded.
// One mutex guards every method; each is short and touches only counters.
type CircuitBreaker struct {
	mu             sync.Mutex
	config         BreakerConfig
	clock          Clock
	window         *SlidingWindow
	transitions    []Transition
	state          State
	openedAtMs     int64
	probesIssued   int
	probeSuccesses int
	rejected       int64
}

func NewCircuitBreaker(config BreakerConfig, clock Clock) *CircuitBreaker {
	w, err := NewSlidingWindow(config.WindowMs, config.BucketMs)
	if err != nil {
		panic(err) // unreachable for a config that came from NewBreakerConfig
	}
	return &CircuitBreaker{config: config, clock: clock, window: w, state: Closed}
}

func (b *CircuitBreaker) State() State {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.state
}

func (b *CircuitBreaker) Rejected() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.rejected
}

func (b *CircuitBreaker) Transitions() []Transition {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]Transition, len(b.transitions))
	copy(out, b.transitions)
	return out
}

func (b *CircuitBreaker) FailureRatePercent() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.window.FailureRatePercent(b.clock.NowMs())
}

func (b *CircuitBreaker) transitionTo(next State) {
	b.transitions = append(b.transitions, Transition{b.state, next, b.clock.NowMs()})
	b.state = next
}

// TryAcquire asks for permission to call the dependency. This is the only place OPEN becomes HALF_OPEN: the
// breaker has no timer goroutine, so the first caller after the open duration performs the transition.
func (b *CircuitBreaker) TryAcquire() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.tryAcquireLocked()
}

func (b *CircuitBreaker) tryAcquireLocked() bool {
	now := b.clock.NowMs()
	switch b.state {
	case Closed:
		return true
	case Open:
		if now-b.openedAtMs < b.config.OpenDurationMs {
			b.rejected++
			return false
		}
		b.transitionTo(HalfOpen)
		b.probesIssued = 0
		b.probeSuccesses = 0
		return b.tryAcquireLocked()
	default: // HalfOpen
		if b.probesIssued >= b.config.HalfOpenPermits {
			b.rejected++
			return false
		}
		b.probesIssued++
		return true
	}
}

func (b *CircuitBreaker) OnSuccess() {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.clock.NowMs()
	switch b.state {
	case Closed:
		b.window.Record(now, true)
	case HalfOpen:
		b.probeSuccesses++
		if b.probeSuccesses >= b.config.HalfOpenPermits {
			b.window.Reset()
			b.transitionTo(Closed)
		}
	case Open:
		// a late result from before the trip; it says nothing about the dependency now
	}
}

func (b *CircuitBreaker) OnFailure() {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.clock.NowMs()
	switch b.state {
	case Closed:
		b.window.Record(now, false)
		if b.window.Total(now) >= b.config.MinimumCalls &&
			b.window.FailureRatePercent(now) >= b.config.FailureRateThresholdPercent {
			b.openedAtMs = now
			b.transitionTo(Open)
		}
	case HalfOpen:
		b.openedAtMs = now
		b.transitionTo(Open)
	case Open:
		// late result; already open
	}
}

// Bulkhead allows at most maxConcurrent calls to one dependency in flight at once. A hand-rolled counting
// semaphore so the three languages share one design; a buffered channel or golang.org/x/sync/semaphore is
// the production equivalent. Acquisition never blocks: a full bulkhead rejects, it does not queue.
type Bulkhead struct {
	mu            sync.Mutex
	maxConcurrent int
	inFlight      int
	rejected      int64
}

var ErrReleaseWithoutAcquire = errors.New("release without acquire")

func NewBulkhead(maxConcurrent int) (*Bulkhead, error) {
	if maxConcurrent < 1 {
		return nil, fmt.Errorf("%w: maxConcurrent must be >= 1", ErrConfig)
	}
	return &Bulkhead{maxConcurrent: maxConcurrent}, nil
}

func (h *Bulkhead) TryAcquire() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.inFlight >= h.maxConcurrent {
		h.rejected++
		return false
	}
	h.inFlight++
	return true
}

func (h *Bulkhead) Release() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.inFlight == 0 {
		return ErrReleaseWithoutAcquire
	}
	h.inFlight--
	return nil
}

func (h *Bulkhead) InFlight() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.inFlight
}

func (h *Bulkhead) Available() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.maxConcurrent - h.inFlight
}

func (h *Bulkhead) Rejected() int64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.rejected
}

// GuardedCall composes breaker and bulkhead around one dependency. The bulkhead is checked first: a full
// bulkhead is a statement about us (we are saturated), not about the dependency, so it must not count toward
// the failure rate and must not consume a half-open probe. Begin/Complete split the call so a simulation can
// model calls that stay in flight; Call is the synchronous convenience.
type GuardedCall struct {
	Breaker  *CircuitBreaker
	Bulkhead *Bulkhead
}

func (g *GuardedCall) Begin() Admission {
	if !g.Bulkhead.TryAcquire() {
		return RejectedBulkhead
	}
	if !g.Breaker.TryAcquire() {
		_ = g.Bulkhead.Release()
		return RejectedOpen
	}
	return Admitted
}

func (g *GuardedCall) Complete(success bool) {
	_ = g.Bulkhead.Release()
	if success {
		g.Breaker.OnSuccess()
	} else {
		g.Breaker.OnFailure()
	}
}

// Call runs op under the guard. A panic inside op is a failure of the dependency, not of the caller.
func (g *GuardedCall) Call(op func() bool) CallOutcome {
	switch g.Begin() {
	case RejectedBulkhead:
		return OutcomeRejectedBulkhead
	case RejectedOpen:
		return OutcomeRejectedOpen
	}
	ok := runSafely(op)
	g.Complete(ok)
	if ok {
		return OutcomeSuccess
	}
	return OutcomeFailure
}

func runSafely(op func() bool) (ok bool) {
	defer func() {
		if r := recover(); r != nil {
			ok = false
		}
	}()
	return op()
}

// TimelineRow holds per-second counts of what the checkout saw, plus the breaker state at the end of the second.
type TimelineRow struct {
	Second                                           int64
	Success, Failure, RejectedOpen, RejectedBulkhead int
	StateAtEnd                                       State
}

type OutageReport struct {
	Rows                                                                 []TimelineRow
	Transitions                                                          []Transition
	TotalSuccess, TotalFailure, TotalRejectedOpen, TotalRejectedBulkhead int
}

func (r OutageReport) CountIn(fromMs, toMs int64, kind CallOutcome) int {
	n := 0
	for _, row := range r.Rows {
		start := row.Second * 1000
		if start < fromMs || start >= toMs {
			continue
		}
		switch kind {
		case OutcomeSuccess:
			n += row.Success
		case OutcomeFailure:
			n += row.Failure
		case OutcomeRejectedOpen:
			n += row.RejectedOpen
		case OutcomeRejectedBulkhead:
			n += row.RejectedBulkhead
		}
	}
	return n
}

func (r OutageReport) Table() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "%4s | %4s | %4s | %8s | %8s | %s\n", "sec", "ok", "fail", "rej_open", "rej_bulk", "state")
	for _, row := range r.Rows {
		fmt.Fprintf(&sb, "%4d | %4d | %4d | %8d | %8d | %s\n",
			row.Second, row.Success, row.Failure, row.RejectedOpen, row.RejectedBulkhead, row.StateAtEnd)
	}
	return sb.String()
}

// RunOutageSimulation models a card-processor outage in virtual time. The checkout sends one authorization
// every callIntervalMs. While the processor is healthy a call completes at once with success. During
// [outageStartMs, outageEndMs) a call hangs for timeoutMs and then fails: this is what a dead processor looks
// like from the client, and it is why the bulkhead matters. Completions are processed before the new call at
// the same tick, so the run is a pure function of its parameters and identical in all three languages.
func RunOutageSimulation(config BreakerConfig, bulkheadSize int, callIntervalMs, horizonMs, outageStartMs, outageEndMs, timeoutMs int64) OutageReport {
	clock := NewFakeClock(0)
	breaker := NewCircuitBreaker(config, clock)
	bulkhead, err := NewBulkhead(bulkheadSize)
	if err != nil {
		panic(err)
	}
	guarded := &GuardedCall{breaker, bulkhead}
	var pending []int64 // due times of hung calls; dues are monotone so insertion order is sorted

	seconds := int(horizonMs / 1000)
	ok := make([]int, seconds)
	fail := make([]int, seconds)
	rejOpen := make([]int, seconds)
	rejBulk := make([]int, seconds)
	stateAtEnd := make([]State, seconds)

	for t := int64(0); t < horizonMs; t += callIntervalMs {
		clock.Advance(t - clock.NowMs())
		sec := int(t / 1000)
		for len(pending) > 0 && pending[0] <= t { // hung calls time out: each is a failure
			pending = pending[1:]
			guarded.Complete(false)
			fail[sec]++
		}
		switch guarded.Begin() {
		case RejectedOpen:
			rejOpen[sec]++
		case RejectedBulkhead:
			rejBulk[sec]++
		case Admitted:
			healthy := t < outageStartMs || t >= outageEndMs
			if healthy {
				guarded.Complete(true)
				ok[sec]++
			} else {
				pending = append(pending, t+timeoutMs)
			}
		}
		stateAtEnd[sec] = breaker.State()
	}

	report := OutageReport{Transitions: breaker.Transitions()}
	for s := 0; s < seconds; s++ {
		report.Rows = append(report.Rows, TimelineRow{int64(s), ok[s], fail[s], rejOpen[s], rejBulk[s], stateAtEnd[s]})
		report.TotalSuccess += ok[s]
		report.TotalFailure += fail[s]
		report.TotalRejectedOpen += rejOpen[s]
		report.TotalRejectedBulkhead += rejBulk[s]
	}
	return report
}

func mustConfig(thresholdPercent, minimumCalls int, windowMs, bucketMs, openDurationMs int64, halfOpenPermits int) BreakerConfig {
	c, err := NewBreakerConfig(thresholdPercent, minimumCalls, windowMs, bucketMs, openDurationMs, halfOpenPermits)
	if err != nil {
		panic(err)
	}
	return c
}

var Demo = mustConfig(50, 10, 5_000, 1_000, 3_000, 3)

func main() {
	fmt.Println("== Sliding window: 5 s of 1 s buckets, failures age out ==")
	w, _ := NewSlidingWindow(5_000, 1_000)
	for i := 0; i < 8; i++ {
		w.Record(0, true)
	}
	for i := 0; i < 12; i++ {
		w.Record(1_500, false)
	}
	fmt.Printf("t=1500: total=%d failures=%d rate=%d%%\n", w.Total(1_500), w.Failures(1_500), w.FailureRatePercent(1_500))
	fmt.Printf("t=5000: total=%d failures=%d rate=%d%%  (the 8 successes from t=0 have aged out)\n", w.Total(5_000), w.Failures(5_000), w.FailureRatePercent(5_000))
	fmt.Printf("t=6500: total=%d failures=%d rate=%d%%  (now the failures are gone too)\n", w.Total(6_500), w.Failures(6_500), w.FailureRatePercent(6_500))

	fmt.Println("\n== Breaker by hand: 50% of at least 10 calls, open 3 s, 3 probes ==")
	clock := NewFakeClock(0)
	cb := NewCircuitBreaker(Demo, clock)
	for i := 0; i < 5; i++ {
		cb.TryAcquire()
		cb.OnSuccess()
	}
	for i := 0; i < 4; i++ {
		cb.TryAcquire()
		cb.OnFailure()
	}
	fmt.Printf("5 ok + 4 fail: rate=%d%% state=%s (9 calls, below minimumCalls=10)\n", cb.FailureRatePercent(), cb.State())
	cb.TryAcquire()
	cb.OnFailure()
	fmt.Printf("5 ok + 5 fail: rate=%d%% state=%s\n", cb.FailureRatePercent(), cb.State())
	fmt.Printf("t=%d tryAcquire=%v (fail fast, no call made)\n", clock.NowMs(), cb.TryAcquire())
	clock.Advance(2_999)
	fmt.Printf("t=%d tryAcquire=%v state=%s\n", clock.NowMs(), cb.TryAcquire(), cb.State())
	clock.Advance(1)
	fmt.Printf("t=%d tryAcquire=%v state=%s (first caller after the open duration performs the transition)\n", clock.NowMs(), cb.TryAcquire(), cb.State())
	cb.OnFailure()
	fmt.Printf("probe failed: state=%s, open timer restarted at t=%d\n", cb.State(), clock.NowMs())
	clock.Advance(3_000)
	for i := 0; i < 3; i++ {
		cb.TryAcquire()
		cb.OnSuccess()
	}
	fmt.Printf("t=%d three probes succeeded: state=%s\n", clock.NowMs(), cb.State())
	parts := make([]string, 0, 5)
	for _, tr := range cb.Transitions() {
		parts = append(parts, fmt.Sprintf("%s->%s@%d", tr.From, tr.To, tr.AtMs))
	}
	fmt.Println("transitions: " + strings.Join(parts, ", "))

	fmt.Println("\n== Bulkhead: 3 permits ==")
	bh, _ := NewBulkhead(3)
	a, b, c, d := bh.TryAcquire(), bh.TryAcquire(), bh.TryAcquire(), bh.TryAcquire()
	fmt.Printf("acquire x4: %v %v %v %v  inFlight=%d rejected=%d\n", a, b, c, d, bh.InFlight(), bh.Rejected())
	_ = bh.Release()
	fmt.Printf("after one release: acquire=%v inFlight=%d\n", bh.TryAcquire(), bh.InFlight())

	fmt.Println("\n== Card processor outage: 20 auth/s, bulkhead 10, 2 s timeout, processor dead from t=5 s to t=15 s ==")
	report := RunOutageSimulation(Demo, 10, 50, 25_000, 5_000, 15_000, 2_000)
	fmt.Print(report.Table())
	fmt.Println("transitions:")
	for _, tr := range report.Transitions {
		fmt.Printf("  t=%6d ms  %s -> %s\n", tr.AtMs, tr.From, tr.To)
	}
	fmt.Printf("totals: ok=%d fail=%d rejected_open=%d rejected_bulkhead=%d\n",
		report.TotalSuccess, report.TotalFailure, report.TotalRejectedOpen, report.TotalRejectedBulkhead)
	during := report.CountIn(5_000, 15_000, OutcomeSuccess) + report.TotalFailure +
		report.CountIn(5_000, 15_000, OutcomeRejectedOpen) + report.CountIn(5_000, 15_000, OutcomeRejectedBulkhead)
	fmt.Printf("calls that waited 2 s for a dead processor: %d of %d calls during the outage\n", report.TotalFailure, during)
}
