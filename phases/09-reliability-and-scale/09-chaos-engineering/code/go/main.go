// Lesson: phases/09-reliability-and-scale/09-chaos-engineering/docs/en.md
// Chaos Engineering and Fault Injection
// Run:  go run .
// Test: go test ./...
// Sources: Basiri et al., "Chaos Engineering", IEEE Software 2016; principlesofchaos.org; ISO 4217 for Money.
// Go 1.22+, standard library only. Deterministic: simulated clock, seeded java.util.Random port, in-memory network.
package main

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
)

// Currency is an ISO 4217 currency, copied from Phase 10 Lesson 01.
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
		return Currency{}, errors.New("exponent must be 0..4")
	}
	return Currency{code, exponent}, nil
}

// Money is integer minor units + currency. Never a float.
type Money struct {
	MinorUnits int64
	Currency   Currency
}

func OfMinor(minorUnits int64, currency Currency) Money { return Money{minorUnits, currency} }

func (m Money) String() string {
	if m.Currency.Exponent == 0 {
		return fmt.Sprintf("%d %s", m.MinorUnits, m.Currency.Code)
	}
	scale := int64(1)
	for i := 0; i < m.Currency.Exponent; i++ {
		scale *= 10
	}
	abs := m.MinorUnits
	if abs < 0 {
		abs = -abs
	}
	sign := ""
	if m.MinorUnits < 0 {
		sign = "-"
	}
	return fmt.Sprintf("%s%d.%0*d %s", sign, abs/scale, m.Currency.Exponent, abs%scale, m.Currency.Code)
}

// SimClock is a manual clock. Every "wait" in the simulation is an explicit advance, so demos and tests are deterministic.
type SimClock struct{ nowMillis int64 }

func NewSimClock(startMillis int64) *SimClock { return &SimClock{startMillis} }

func (c *SimClock) NowMillis() int64 { return c.nowMillis }

// Advance moves time forward; a negative step is a programming error and panics.
func (c *SimClock) Advance(millis int64) {
	if millis < 0 {
		panic("clock never moves backwards")
	}
	c.nowMillis += millis
}

// Fault is one of the four faults this lesson injects. Each is a value: it can be logged, diffed, and replayed.
// The unexported method seals the set, as the Java sealed interface does.
type Fault interface {
	fault()
	String() string
}

// Latency is added one-way delay on a network link.
type Latency struct{ AddedMillis int64 }

// Error: the callee fails the request with a 5xx-style error.
type Error struct{ Code string }

// Partition: the link is cut; requests never arrive and never return.
type Partition struct{}

// ClockSkew: the callee's clock runs ahead of (or behind) true time.
type ClockSkew struct{ SkewMillis int64 }

func NewLatency(addedMillis int64) (Latency, error) {
	if addedMillis < 0 {
		return Latency{}, errors.New("latency must be >= 0")
	}
	return Latency{addedMillis}, nil
}

func (Latency) fault()   {}
func (Error) fault()     {}
func (Partition) fault() {}
func (ClockSkew) fault() {}

func (l Latency) String() string   { return fmt.Sprintf("Latency[addedMillis=%d]", l.AddedMillis) }
func (e Error) String() string     { return fmt.Sprintf("Error[code=%s]", e.Code) }
func (Partition) String() string   { return "Partition[]" }
func (s ClockSkew) String() string { return fmt.Sprintf("ClockSkew[skewMillis=%d]", s.SkewMillis) }

// BlastRadius is who is affected. The whole point of chaos in production is that this is small and explicit.
type BlastRadius interface {
	Includes(request AuthRequest, rng *JavaRandom) bool
	String() string
}

type PercentOfRequests struct{ Percent int }

type Merchants struct{ MerchantIds map[string]bool }

type Everyone struct{}

func NewPercentOfRequests(percent int) (PercentOfRequests, error) {
	if percent < 0 || percent > 100 {
		return PercentOfRequests{}, errors.New("percent 0..100")
	}
	return PercentOfRequests{percent}, nil
}

func NewMerchants(ids ...string) Merchants {
	m := Merchants{map[string]bool{}}
	for _, id := range ids {
		m.MerchantIds[id] = true
	}
	return m
}

func (p PercentOfRequests) Includes(_ AuthRequest, rng *JavaRandom) bool {
	return rng.NextInt(100) < p.Percent
}
func (m Merchants) Includes(r AuthRequest, _ *JavaRandom) bool { return m.MerchantIds[r.MerchantId] }
func (Everyone) Includes(AuthRequest, *JavaRandom) bool        { return true }

func (p PercentOfRequests) String() string {
	return fmt.Sprintf("PercentOfRequests[percent=%d]", p.Percent)
}
func (m Merchants) String() string {
	ids := make([]string, 0, len(m.MerchantIds))
	for id := range m.MerchantIds {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return "Merchants[merchantIds=[" + strings.Join(ids, ", ") + "]]"
}
func (Everyone) String() string { return "Everyone[]" }

// Injection is a fault bound to a target and a blast radius.
type Injection struct {
	Target      string
	Fault       Fault
	BlastRadius BlastRadius
}

// FaultInjector is the fault-injection layer. Lives between caller and callee, decides per request whether an
// injection applies, and is the only component with a "clear everything" switch (the abort path depends on it).
type FaultInjector struct {
	injections []Injection
	rng        *JavaRandom
	applied    int
}

func NewFaultInjector(seed int64) *FaultInjector { return &FaultInjector{rng: NewJavaRandom(seed)} }

func (fi *FaultInjector) Inject(target string, fault Fault, radius BlastRadius) {
	fi.injections = append(fi.injections, Injection{target, fault, radius})
}

func (fi *FaultInjector) Clear()            { fi.injections = nil }
func (fi *FaultInjector) IsArmed() bool     { return len(fi.injections) > 0 }
func (fi *FaultInjector) AppliedCount() int { return fi.applied }

// FaultsFor returns the faults whose target and blast radius match this request, in injection order.
func (fi *FaultInjector) FaultsFor(target string, request AuthRequest) []Fault {
	var out []Fault
	for _, inj := range fi.injections {
		if inj.Target == target && inj.BlastRadius.Includes(request, fi.rng) {
			out = append(out, inj.Fault)
			fi.applied++
		}
	}
	return out
}

// Outcome of one authorization attempt.
type Outcome int

const (
	AUTHORIZED      Outcome = iota // good
	DECLINED                       // good: a legitimate business answer
	TIMEOUT                        // bad: caller gave up
	PROCESSOR_ERROR                // bad: callee failed
	STALE_REQUEST                  // bad: callee rejected the request timestamp
)

var outcomeNames = [...]string{"AUTHORIZED", "DECLINED", "TIMEOUT", "PROCESSOR_ERROR", "STALE_REQUEST"}

func (o Outcome) String() string { return outcomeNames[o] }
func (o Outcome) IsGood() bool   { return o == AUTHORIZED || o == DECLINED }

type AuthRequest struct {
	PaymentId      string
	MerchantId     string
	Amount         Money
	IdempotencyKey string
	SentAtMillis   int64
}

type AuthResult struct {
	Outcome       Outcome
	LatencyMillis int64
	Attempts      int
}

// Sentinel errors for the two ways the processor can fail a request; they replace the Java runtime exceptions.
var (
	ErrProcessorError = errors.New("processor error")
	ErrStaleRequest   = errors.New("stale request")
)

// TimestampToleranceMillis is the processor's signing window.
const TimestampToleranceMillis int64 = 5 * 60 * 1000

// Processor is the downstream card processor. Verifies the request timestamp against ITS clock (a signing window,
// as real processors do), deduplicates by idempotency key, and declines a seeded 2% of traffic.
type Processor struct {
	clock                   *SimClock
	rng                     *JavaRandom
	clockSkewMillis         int64
	byIdempotencyKey        map[string]Outcome
	authorizationsByPayment map[string]int
}

func NewProcessor(clock *SimClock, seed int64) *Processor {
	return &Processor{clock: clock, rng: NewJavaRandom(seed), byIdempotencyKey: map[string]Outcome{}, authorizationsByPayment: map[string]int{}}
}

func (p *Processor) SetClockSkew(millis int64) { p.clockSkewMillis = millis }

func (p *Processor) LocalNowMillis() int64 { return p.clock.NowMillis() + p.clockSkewMillis }

func (p *Processor) Handle(request AuthRequest) (Outcome, error) {
	age := p.LocalNowMillis() - request.SentAtMillis
	if age < 0 {
		age = -age
	}
	if age > TimestampToleranceMillis {
		return 0, fmt.Errorf("%w: request timestamp outside signing window by %d ms", ErrStaleRequest, age)
	}
	if seen, ok := p.byIdempotencyKey[request.IdempotencyKey]; ok {
		return seen, nil // replay: same answer, no second authorization
	}
	outcome := AUTHORIZED
	if p.rng.NextInt(100) < 2 {
		outcome = DECLINED
	}
	p.byIdempotencyKey[request.IdempotencyKey] = outcome
	if outcome == AUTHORIZED {
		p.authorizationsByPayment[request.PaymentId]++
	}
	return outcome, nil
}

func (p *Processor) AuthorizationsFor(paymentId string) int {
	return p.authorizationsByPayment[paymentId]
}

func (p *Processor) NoDoubleAuthorizations() bool {
	for _, n := range p.authorizationsByPayment {
		if n > 1 {
			return false
		}
	}
	return true
}

const (
	LinkGatewayToProcessor = "gateway->processor"
	TargetProcessor        = "processor"
)

// SimNetwork is the in-memory network between gateway and processor. Computes the round-trip latency from a seeded
// base plus any injected latency, applies partitions, errors, and clock skew, and advances the shared clock.
type SimNetwork struct {
	clock    *SimClock
	injector *FaultInjector
	rng      *JavaRandom
}

func NewSimNetwork(clock *SimClock, injector *FaultInjector, seed int64) *SimNetwork {
	return &SimNetwork{clock, injector, NewJavaRandom(seed)}
}

// Call returns the outcome after advancing the clock by the observed latency, or TIMEOUT after timeoutMillis.
func (n *SimNetwork) Call(request AuthRequest, processor *Processor, timeoutMillis int64) Outcome {
	latency := int64(80 + n.rng.NextInt(41)) // 80..120 ms baseline
	partitioned := false
	for _, f := range n.injector.FaultsFor(LinkGatewayToProcessor, request) {
		switch f := f.(type) {
		case Latency:
			latency += f.AddedMillis
		case Partition:
			partitioned = true
		}
	}
	if partitioned || latency > timeoutMillis {
		n.clock.Advance(timeoutMillis)
		return TIMEOUT
	}
	n.clock.Advance(latency)
	var skew int64
	var errorCode *string
	for _, f := range n.injector.FaultsFor(TargetProcessor, request) {
		switch f := f.(type) {
		case ClockSkew:
			skew = f.SkewMillis
		case Error:
			code := f.Code
			errorCode = &code
		}
	}
	processor.SetClockSkew(skew)
	defer processor.SetClockSkew(0) // the Java finally: skew never leaks into the next request
	if errorCode != nil {
		return PROCESSOR_ERROR
	}
	outcome, err := processor.Handle(request)
	switch {
	case errors.Is(err, ErrStaleRequest):
		return STALE_REQUEST
	case errors.Is(err, ErrProcessorError):
		return PROCESSOR_ERROR
	case err != nil:
		panic(err)
	}
	return outcome
}

// Gateway is the system under test: times out, retries once with the same idempotency key, never retries a decline.
const (
	TimeoutMillis int64 = 2_000
	MaxAttempts         = 2
)

type Gateway struct {
	clock     *SimClock
	network   *SimNetwork
	processor *Processor
}

func NewGateway(clock *SimClock, network *SimNetwork, processor *Processor) *Gateway {
	return &Gateway{clock, network, processor}
}

func (g *Gateway) Authorize(paymentId, merchantId string, amount Money) AuthResult {
	started := g.clock.NowMillis()
	idempotencyKey := "idem-" + paymentId
	outcome := TIMEOUT
	attempts := 0
	for attempts < MaxAttempts {
		attempts++
		request := AuthRequest{paymentId, merchantId, amount, idempotencyKey, g.clock.NowMillis()}
		outcome = g.network.Call(request, g.processor, TimeoutMillis)
		if outcome != TIMEOUT && outcome != PROCESSOR_ERROR {
			break
		}
	}
	return AuthResult{outcome, g.clock.NowMillis() - started, attempts}
}

// TrafficGenerator is seeded traffic: payment ids, ten merchants, amounts between 1.00 and 500.00 USD.
type TrafficGenerator struct {
	rng      *JavaRandom
	sequence int
	prefix   string
}

func NewTrafficGenerator(seed int64, prefix string) *TrafficGenerator {
	return &TrafficGenerator{rng: NewJavaRandom(seed), prefix: prefix}
}

func (t *TrafficGenerator) NextPaymentId() string {
	t.sequence++
	return fmt.Sprintf("%s-%d", t.prefix, t.sequence)
}

func (t *TrafficGenerator) NextMerchantId() string { return fmt.Sprintf("m-%d", t.rng.NextInt(10)) }

func (t *TrafficGenerator) NextAmount() Money { return OfMinor(int64(100+t.rng.NextInt(49_901)), USD) }

// Metrics counts outcomes and keeps latency samples; p99 is computed from the sorted samples, never from an average.
type Metrics struct {
	counts    map[Outcome]int
	latencies []int64
}

func NewMetrics() *Metrics { return &Metrics{counts: map[Outcome]int{}} }

func (m *Metrics) Record(result AuthResult) {
	m.counts[result.Outcome]++
	m.latencies = append(m.latencies, result.LatencyMillis)
}

func (m *Metrics) Total() int          { return len(m.latencies) }
func (m *Metrics) Count(o Outcome) int { return m.counts[o] }

// GoodRatePercent is good events / all events, in percent, 100.0 when empty.
func (m *Metrics) GoodRatePercent() float64 {
	if len(m.latencies) == 0 {
		return 100.0
	}
	good := m.Count(AUTHORIZED) + m.Count(DECLINED)
	return 100.0 * float64(good) / float64(len(m.latencies))
}

func (m *Metrics) P99Millis() int64 {
	if len(m.latencies) == 0 {
		return 0
	}
	sorted := append([]int64(nil), m.latencies...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	index := int(math.Ceil(0.99*float64(len(sorted)))) - 1
	return sorted[max(0, index)]
}

func (m *Metrics) String() string {
	return fmt.Sprintf("n=%d good=%.1f%% p99=%dms auth=%d decl=%d timeout=%d error=%d stale=%d",
		m.Total(), m.GoodRatePercent(), m.P99Millis(), m.Count(AUTHORIZED), m.Count(DECLINED),
		m.Count(TIMEOUT), m.Count(PROCESSOR_ERROR), m.Count(STALE_REQUEST))
}

// SteadyState is what "normal" looks like, as thresholds on measurable SLIs. If you cannot write this, you are not ready to inject.
type SteadyState struct {
	MinGoodRatePercent float64
	MaxP99Millis       int64
}

func (s SteadyState) Holds(m *Metrics) bool {
	return m.GoodRatePercent() >= s.MinGoodRatePercent && m.P99Millis() <= s.MaxP99Millis
}

// AbortCondition is the tripwire: evaluated every CheckEvery requests on the cumulative experimental metrics.
type AbortCondition struct {
	MinGoodRatePercent float64
	CheckEvery         int
}

func (a AbortCondition) Triggered(m *Metrics) bool { return m.GoodRatePercent() < a.MinGoodRatePercent }

type Experiment struct {
	Name, Hypothesis, Target string
	Fault                    Fault
	BlastRadius              BlastRadius
	SteadyState              SteadyState
	Abort                    AbortCondition
	Requests                 int
}

// ExperimentReport is everything the GameDay needs to decide what to fix.
type ExperimentReport struct {
	Experiment                       Experiment
	Baseline, Experimental, Recovery *Metrics
	BaselineHeld, Aborted            bool
	RequestsBeforeAbort              int
	HypothesisHeld, Recovered        bool
	NoDoubleAuthorizations           bool
}

func (r ExperimentReport) Verdict() string {
	if !r.BaselineHeld {
		return "NOT RUN (steady state not established)"
	}
	if r.Aborted {
		return fmt.Sprintf("ABORTED after %d requests", r.RequestsBeforeAbort)
	}
	if r.HypothesisHeld {
		return "HYPOTHESIS HELD"
	}
	return "HYPOTHESIS VIOLATED"
}

func yesNo(b bool, yes, no string) string {
	if b {
		return yes
	}
	return no
}

func (r ExperimentReport) Render() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Experiment: %s\n", r.Experiment.Name)
	fmt.Fprintf(&sb, "  Hypothesis: %s\n", r.Experiment.Hypothesis)
	fmt.Fprintf(&sb, "  Fault: %s on %s, blast radius %s\n", r.Experiment.Fault, r.Experiment.Target, r.Experiment.BlastRadius)
	fmt.Fprintf(&sb, "  Baseline:     %s\n", r.Baseline)
	fmt.Fprintf(&sb, "  Experimental: %s\n", r.Experimental)
	fmt.Fprintf(&sb, "  Recovery:     %s\n", r.Recovery)
	fmt.Fprintf(&sb, "  Invariant (no payment authorized twice): %s\n", yesNo(r.NoDoubleAuthorizations, "HELD", "VIOLATED"))
	fmt.Fprintf(&sb, "  Recovered to steady state after clearing faults: %s\n", yesNo(r.Recovered, "yes", "NO"))
	fmt.Fprintf(&sb, "  Verdict: %s\n", r.Verdict())
	return sb.String()
}

// ExperimentRunner runs one experiment: baseline, inject, observe with abort checks, clear, verify recovery.
type ExperimentRunner struct {
	clock     *SimClock
	injector  *FaultInjector
	gateway   *Gateway
	processor *Processor
	traffic   *TrafficGenerator
}

func NewExperimentRunner(clock *SimClock, injector *FaultInjector, gateway *Gateway, processor *Processor, traffic *TrafficGenerator) *ExperimentRunner {
	return &ExperimentRunner{clock, injector, gateway, processor, traffic}
}

// drive sends requests and returns the metrics plus the request index at which the abort fired, or -1.
func (r *ExperimentRunner) drive(requests int, abort *AbortCondition) (*Metrics, int) {
	m := NewMetrics()
	for i := 1; i <= requests; i++ {
		m.Record(r.gateway.Authorize(r.traffic.NextPaymentId(), r.traffic.NextMerchantId(), r.traffic.NextAmount()))
		r.clock.Advance(10) // inter-arrival gap
		if abort != nil && i%abort.CheckEvery == 0 && abort.Triggered(m) {
			return m, i
		}
	}
	return m, -1
}

func (r *ExperimentRunner) Run(e Experiment) ExperimentReport {
	r.injector.Clear()
	baseline, _ := r.drive(e.Requests, nil)
	baselineHeld := e.SteadyState.Holds(baseline)
	if !baselineHeld {
		empty := NewMetrics()
		return ExperimentReport{e, baseline, empty, empty, false, false, 0, false, false, r.processor.NoDoubleAuthorizations()}
	}
	r.injector.Inject(e.Target, e.Fault, e.BlastRadius)
	experimental, stop := r.drive(e.Requests, &e.Abort)
	aborted := stop > 0
	requestsBeforeAbort := experimental.Total()
	if aborted {
		requestsBeforeAbort = stop
	}
	r.injector.Clear() // the abort path and the normal path both end here
	recovery, _ := r.drive(e.Requests, nil)
	hypothesisHeld := !aborted && e.SteadyState.Holds(experimental)
	return ExperimentReport{e, baseline, experimental, recovery, true, aborted, requestsBeforeAbort,
		hypothesisHeld, e.SteadyState.Holds(recovery), r.processor.NoDoubleAuthorizations()}
}

// GameDay is an ordered list of experiments with a summary at the end.
type GameDay struct {
	runner  *ExperimentRunner
	reports []ExperimentReport
}

func NewGameDay(runner *ExperimentRunner) *GameDay { return &GameDay{runner: runner} }

func (g *GameDay) Run(e Experiment) ExperimentReport {
	r := g.runner.Run(e)
	g.reports = append(g.reports, r)
	return r
}

func (g *GameDay) Reports() []ExperimentReport { return g.reports }

func (g *GameDay) Summary() string {
	var sb strings.Builder
	sb.WriteString("GameDay summary\n")
	for _, r := range g.reports {
		fmt.Fprintf(&sb, "  %-44s %s\n", r.Experiment.Name, r.Verdict())
	}
	return sb.String()
}

var (
	Steady = SteadyState{99.0, 1_500}
	Abort  = AbortCondition{80.0, 50}
)

func NewExperiment(name, hypothesis, target string, fault Fault, radius BlastRadius) Experiment {
	return Experiment{name, hypothesis, target, fault, radius, Steady, Abort, 500}
}

func mustLatency(l Latency, err error) Latency {
	if err != nil {
		panic(err)
	}
	return l
}

func mustPercent(p PercentOfRequests, err error) PercentOfRequests {
	if err != nil {
		panic(err)
	}
	return p
}

func main() {
	clock := NewSimClock(1_700_000_000_000)
	injector := NewFaultInjector(7)
	processor := NewProcessor(clock, 11)
	network := NewSimNetwork(clock, injector, 13)
	gateway := NewGateway(clock, network, processor)
	traffic := NewTrafficGenerator(17, "pay")
	day := NewGameDay(NewExperimentRunner(clock, injector, gateway, processor, traffic))

	fmt.Printf("Steady state: good rate >= %.1f%%, p99 <= %d ms\n", Steady.MinGoodRatePercent, Steady.MaxP99Millis)
	fmt.Printf("Abort if good rate < %.1f%% at any %d-request checkpoint\n\n", Abort.MinGoodRatePercent, Abort.CheckEvery)

	plan := []Experiment{
		NewExperiment("latency +300ms, 10% of requests",
			"A 300 ms slower processor stays inside the 2 s timeout; p99 rises but the SLO holds",
			LinkGatewayToProcessor, mustLatency(NewLatency(300)), mustPercent(NewPercentOfRequests(10))),
		NewExperiment("latency +2500ms, 10% of requests",
			"Requests beyond the timeout are retried once with the same idempotency key; no double authorization",
			LinkGatewayToProcessor, mustLatency(NewLatency(2_500)), mustPercent(NewPercentOfRequests(10))),
		NewExperiment("processor 500s, 70% of requests",
			"One retry absorbs 5xx bursts; the abort tripwire fires before the whole fleet is affected",
			TargetProcessor, Error{"HTTP_500"}, mustPercent(NewPercentOfRequests(70))),
		NewExperiment("partition, canary merchant m-3",
			"A cut link fails fast for the canary merchant and leaves every other merchant inside the SLO",
			LinkGatewayToProcessor, Partition{}, NewMerchants("m-3")),
		NewExperiment("processor clock +6 minutes, everyone",
			"Six minutes of skew is inside what NTP drift could produce; the processor should still accept requests",
			TargetProcessor, ClockSkew{6 * 60 * 1000}, Everyone{}),
	}

	for _, e := range plan {
		fmt.Println(day.Run(e).Render())
	}
	fmt.Print(day.Summary())
	fmt.Printf("\nFaults applied in total: %d; injector armed now: %t\n", injector.AppliedCount(), injector.IsArmed())
}
