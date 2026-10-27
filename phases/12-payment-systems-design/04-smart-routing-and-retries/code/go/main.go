// Lesson: phases/12-payment-systems-design/04-smart-routing-and-retries/docs/en.md
// Smart Routing, Cascading, and Retry Policies
// Run:  go run .
// Test: go test ./...
// Sources: ISO 8583 response codes (field 39); Mastercard Merchant Advice Codes; Visa excessive reattempt rules;
// Google SRE book ch. 22 (retry budgets); Nygard, "Release It!" (circuit breaker). Go 1.22+, stdlib only.
package main

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// ---------------------------------------------------------------------------------------------
// Time is injected so the circuit breaker's cool-down is driven by the test, never by sleep.
// ---------------------------------------------------------------------------------------------

type Clock interface{ NowMillis() int64 }

type FakeClock struct{ now int64 }

func NewFakeClock(startMillis int64) *FakeClock { return &FakeClock{now: startMillis} }
func (c *FakeClock) NowMillis() int64           { return c.now }
func (c *FakeClock) Advance(millis int64)       { c.now += millis }

// ---------------------------------------------------------------------------------------------
// The request and the processors we can send it to.
// ---------------------------------------------------------------------------------------------

type CardNetwork int

const (
	VISA CardNetwork = iota
	MASTERCARD
	AMEX
)

func (n CardNetwork) String() string { return [...]string{"VISA", "MASTERCARD", "AMEX"}[n] }

// ErrInvalidArgument is the Go spelling of Java's IllegalArgumentException for every constructor check.
var ErrInvalidArgument = errors.New("invalid argument")

type PaymentRequest struct {
	ID              string
	Amount          Money
	Network         CardNetwork
	CardFingerprint string
}

func NewPaymentRequest(id string, amount Money, network CardNetwork, cardFingerprint string) (PaymentRequest, error) {
	if !amount.IsPositive() {
		return PaymentRequest{}, fmt.Errorf("%w: amount must be positive: %v", ErrInvalidArgument, amount)
	}
	return PaymentRequest{id, amount, network, cardFingerprint}, nil
}

// ProcessorConfig is a processor's static terms: what it accepts and what it charges (basis points plus a fixed fee).
type ProcessorConfig struct {
	ID            string
	Networks      map[CardNetwork]bool
	Currencies    map[Currency]bool
	FeeBps        int64
	FixedFeeMinor int64
}

func NewProcessorConfig(id string, networks []CardNetwork, currencies []Currency, feeBps, fixedFeeMinor int64) (ProcessorConfig, error) {
	if feeBps < 0 || fixedFeeMinor < 0 {
		return ProcessorConfig{}, fmt.Errorf("%w: fees cannot be negative", ErrInvalidArgument)
	}
	c := ProcessorConfig{ID: id, Networks: map[CardNetwork]bool{}, Currencies: map[Currency]bool{}, FeeBps: feeBps, FixedFeeMinor: fixedFeeMinor}
	for _, n := range networks {
		c.Networks[n] = true
	}
	for _, cur := range currencies {
		c.Currencies[cur] = true
	}
	return c, nil
}

func (c ProcessorConfig) Accepts(r PaymentRequest) bool {
	return c.Networks[r.Network] && c.Currencies[r.Amount.Currency]
}

// Fee in minor units: amount * bps / 10000 rounded half-up, plus the fixed component. Integer arithmetic only;
// the checked helpers stand in for Java's multiplyExact/addExact.
func (c ProcessorConfig) Fee(amount Money) (Money, error) {
	scaled, err := mulChecked(amount.MinorUnits, c.FeeBps)
	if err != nil {
		return Money{}, err
	}
	rounded, err := addChecked(scaled, 5_000)
	if err != nil {
		return Money{}, err
	}
	variable := floorDiv(rounded, 10_000)
	return OfMinor(variable+c.FixedFeeMinor, amount.Currency), nil
}

// floorDiv is Java's Math.floorDiv: rounds toward negative infinity, unlike Go's / which truncates.
func floorDiv(a, b int64) int64 {
	q := a / b
	if (a%b != 0) && ((a < 0) != (b < 0)) {
		q--
	}
	return q
}

// ---------------------------------------------------------------------------------------------
// Decline classification. The response code decides whether another processor could change the
// answer. Only SOFT_CASCADE may be cascaded; everything else stops the attempt chain.
// ---------------------------------------------------------------------------------------------

type DeclineClass int

const (
	APPROVED         DeclineClass = iota // 00
	SOFT_CASCADE                         // the path failed, not the card: another acquirer may succeed now
	SOFT_RETRY_LATER                     // the issuer said no for now (funds, limits): same answer anywhere today
	AUTH_REQUIRED                        // the issuer wants strong customer authentication: step up, do not cascade
	HARD                                 // the card or the cardholder is the problem: never retry
	AMBIGUOUS                            // we do not know whether it was authorized: reconcile before anything else
)

func (d DeclineClass) String() string {
	return [...]string{"APPROVED", "SOFT_CASCADE", "SOFT_RETRY_LATER", "AUTH_REQUIRED", "HARD", "AMBIGUOUS"}[d]
}

type DeclineRule struct {
	Code    string
	Meaning string
	Decline DeclineClass
}

const (
	CONNECT_ERROR = "CONNECT_ERROR"
	TIMEOUT       = "TIMEOUT"
)

// ProcessorResponse is the processor's answer; MerchantAdviceCode is "" when the network sent none (Java null).
type ProcessorResponse struct {
	Code               string
	MerchantAdviceCode string
}

func Approved() ProcessorResponse              { return ProcessorResponse{"00", ""} }
func Of(code string) ProcessorResponse         { return ProcessorResponse{code, ""} }
func OfMAC(code, mac string) ProcessorResponse { return ProcessorResponse{code, mac} }
func ConnectError() ProcessorResponse          { return ProcessorResponse{CONNECT_ERROR, ""} }
func Timeout() ProcessorResponse               { return ProcessorResponse{TIMEOUT, ""} }
func (r ProcessorResponse) Technical() bool    { return r.Code == CONNECT_ERROR || r.Code == TIMEOUT }

// DeclineTable is the ordered rule list (Java's LinkedHashMap); declineByCode is its index.
var DeclineTable = []DeclineRule{
	{"00", "Approved", APPROVED},
	{"05", "Do not honor (generic)", SOFT_CASCADE},
	{"06", "Error", SOFT_CASCADE},
	{"91", "Issuer or switch inoperative", SOFT_CASCADE},
	{"96", "System malfunction", SOFT_CASCADE},
	{CONNECT_ERROR, "Request never reached the processor", SOFT_CASCADE},
	{"51", "Insufficient funds", SOFT_RETRY_LATER},
	{"61", "Exceeds withdrawal amount limit", SOFT_RETRY_LATER},
	{"1A", "Additional customer authentication required", AUTH_REQUIRED},
	{"65", "Exceeds frequency limit / SCA required (EEA)", AUTH_REQUIRED},
	{"14", "Invalid card number", HARD},
	{"41", "Lost card, pick up", HARD},
	{"43", "Stolen card, pick up", HARD},
	{"54", "Expired card", HARD},
	{"57", "Transaction not permitted to cardholder", HARD},
	{"59", "Suspected fraud", HARD},
	{"62", "Restricted card", HARD},
	{"N7", "CVV2 mismatch", HARD},
	{TIMEOUT, "No response; authorization may exist", AMBIGUOUS},
}

var declineByCode = func() map[string]DeclineRule {
	m := map[string]DeclineRule{}
	for _, r := range DeclineTable {
		m[r.Code] = r
	}
	return m
}()

// Classify a response. Two overrides come before the table: a Mastercard Merchant Advice Code of
// 03 ("do not try again") or 21 ("payment cancellation") makes any decline HARD, and an unknown
// code is HARD because retrying what you do not understand is how a merchant gets fined.
func Classify(r ProcessorResponse) DeclineClass {
	rule, known := declineByCode[r.Code]
	if known && rule.Decline == APPROVED {
		return APPROVED
	}
	if r.MerchantAdviceCode == "03" || r.MerchantAdviceCode == "21" {
		return HARD
	}
	if !known {
		return HARD
	}
	return rule.Decline
}

func Meaning(code string) string {
	if rule, ok := declineByCode[code]; ok {
		return rule.Meaning
	}
	return "Unknown code"
}

// ---------------------------------------------------------------------------------------------
// Authorization-rate estimates: a Beta prior plus exponentially decayed counts per (processor,
// network). Recent outcomes weigh more, so a processor that degrades is noticed within hours.
// The estimate is a probability used to rank; it is never an amount of money.
// ---------------------------------------------------------------------------------------------

type counts struct{ approvals, attempts float64 }

type AuthRateEstimator struct {
	priorRate   float64
	priorWeight float64
	decay       float64
	counts      map[string]counts
}

func NewAuthRateEstimator(priorRate, priorWeight, decay float64) (*AuthRateEstimator, error) {
	if priorRate <= 0 || priorRate >= 1 {
		return nil, fmt.Errorf("%w: priorRate must be in (0,1)", ErrInvalidArgument)
	}
	if decay <= 0 || decay > 1 {
		return nil, fmt.Errorf("%w: decay must be in (0,1]", ErrInvalidArgument)
	}
	return &AuthRateEstimator{priorRate: priorRate, priorWeight: priorWeight, decay: decay, counts: map[string]counts{}}, nil
}

func estimatorKey(processorID string, network CardNetwork) string {
	return processorID + "/" + network.String()
}

// Estimate is (decayed approvals + prior pseudo-approvals) / (decayed attempts + prior weight).
func (e *AuthRateEstimator) Estimate(processorID string, network CardNetwork) float64 {
	c := e.counts[estimatorKey(processorID, network)]
	return (c.approvals + e.priorRate*e.priorWeight) / (c.attempts + e.priorWeight)
}

// Record one issuer decision. Technical failures are health signals and must not be recorded here.
// The explicit float64() conversions stop the compiler fusing multiply-add, so the counts match Java bit for bit.
func (e *AuthRateEstimator) Record(processorID string, network CardNetwork, approved bool) {
	k := estimatorKey(processorID, network)
	c := e.counts[k]
	add := 0.0
	if approved {
		add = 1
	}
	e.counts[k] = counts{float64(c.approvals*e.decay) + add, float64(c.attempts*e.decay) + 1}
}

func (e *AuthRateEstimator) Observations(processorID string, network CardNetwork) float64 {
	return e.counts[estimatorKey(processorID, network)].attempts
}

// ---------------------------------------------------------------------------------------------
// Processor health: a circuit breaker over the last N technical outcomes. OPEN removes the
// processor from routing until the cool-down passes; HALF_OPEN lets one request probe it.
// ---------------------------------------------------------------------------------------------

type CircuitState int

const (
	CLOSED CircuitState = iota
	OPEN
	HALF_OPEN
)

func (s CircuitState) String() string { return [...]string{"CLOSED", "OPEN", "HALF_OPEN"}[s] }

type ProcessorHealth struct {
	clock            Clock
	windowSize       int
	failureThreshold int
	coolDownMillis   int64
	window           []bool
	state            CircuitState
	openedAtMillis   int64
}

func NewProcessorHealth(clock Clock, windowSize, failureThreshold int, coolDownMillis int64) *ProcessorHealth {
	return &ProcessorHealth{clock: clock, windowSize: windowSize, failureThreshold: failureThreshold, coolDownMillis: coolDownMillis, state: CLOSED}
}

func (h *ProcessorHealth) State() CircuitState {
	if h.state == OPEN && h.clock.NowMillis()-h.openedAtMillis >= h.coolDownMillis {
		h.state = HALF_OPEN
	}
	return h.state
}

func (h *ProcessorHealth) Allows() bool { return h.State() != OPEN }

func (h *ProcessorHealth) RecordSuccess() {
	if h.State() == HALF_OPEN {
		h.state = CLOSED
		h.window = h.window[:0]
	}
	h.push(false)
}

func (h *ProcessorHealth) RecordFailure() {
	if h.State() == HALF_OPEN {
		h.trip()
		return
	}
	h.push(true)
	failures := 0
	for _, b := range h.window {
		if b {
			failures++
		}
	}
	if failures >= h.failureThreshold {
		h.trip()
	}
}

func (h *ProcessorHealth) trip() {
	h.state = OPEN
	h.openedAtMillis = h.clock.NowMillis()
	h.window = h.window[:0]
}

func (h *ProcessorHealth) push(failure bool) {
	h.window = append(h.window, failure)
	for len(h.window) > h.windowSize {
		h.window = h.window[1:]
	}
}

// ---------------------------------------------------------------------------------------------
// Retry budget: cascades are paid for by first attempts. Each first attempt deposits a fraction
// of a token; each cascade spends a whole one. When an outage turns every attempt into a soft
// decline, the budget runs dry and the router stops multiplying load on a struggling network.
// ---------------------------------------------------------------------------------------------

const TOKEN int64 = 1_000 // milli-tokens per retry

type RetryBudget struct {
	depositPerAttempt int64 // milli-tokens earned per first attempt (100 = 10% retry ratio)
	capacity          int64
	balance           int64
}

func NewRetryBudget(depositPerAttempt, capacity, initial int64) *RetryBudget {
	return &RetryBudget{depositPerAttempt: depositPerAttempt, capacity: capacity, balance: min(initial, capacity)}
}

func (b *RetryBudget) RecordFirstAttempt() {
	b.balance = min(b.capacity, b.balance+b.depositPerAttempt)
}

func (b *RetryBudget) TryAcquireRetry() bool {
	if b.balance < TOKEN {
		return false
	}
	b.balance -= TOKEN
	return true
}

func (b *RetryBudget) BalanceMilli() int64 { return b.balance }

// ---------------------------------------------------------------------------------------------
// The gateway is the network boundary. Scripted for tests, simulated with a seeded JavaRandom for
// the learning demo. Nothing here sleeps or opens a socket.
// ---------------------------------------------------------------------------------------------

type ProcessorGateway interface {
	Authorize(processorID string, request PaymentRequest) ProcessorResponse
}

type ScriptedGateway struct {
	scripts map[string][]ProcessorResponse
	calls   []string
}

func NewScriptedGateway() *ScriptedGateway {
	return &ScriptedGateway{scripts: map[string][]ProcessorResponse{}}
}

func (g *ScriptedGateway) Script(processorID string, responses ...ProcessorResponse) *ScriptedGateway {
	g.scripts[processorID] = append(g.scripts[processorID], responses...)
	return g
}

func (g *ScriptedGateway) Calls() []string { return append([]string(nil), g.calls...) }

func (g *ScriptedGateway) Authorize(processorID string, request PaymentRequest) ProcessorResponse {
	g.calls = append(g.calls, processorID+":"+request.ID)
	q := g.scripts[processorID]
	if len(q) == 0 {
		return Approved()
	}
	g.scripts[processorID] = q[1:]
	return q[0]
}

// SimulatedGateway has a true approval probability per (processor, network), decided by a seeded JavaRandom.
type SimulatedGateway struct {
	random    *JavaRandom
	trueRates map[string]float64
}

func NewSimulatedGateway(seed int64) *SimulatedGateway {
	return &SimulatedGateway{random: NewJavaRandom(seed), trueRates: map[string]float64{}}
}

func (g *SimulatedGateway) Rate(processorID string, network CardNetwork, approvalProbability float64) *SimulatedGateway {
	g.trueRates[processorID+"/"+network.String()] = approvalProbability
	return g
}

func (g *SimulatedGateway) Authorize(processorID string, request PaymentRequest) ProcessorResponse {
	p, ok := g.trueRates[processorID+"/"+request.Network.String()]
	if !ok {
		p = 0.9
	}
	roll := g.random.NextDouble()
	if roll < p {
		return Approved()
	}
	// Of the declines, a third are path problems (05/91) another acquirer might clear; the rest are issuer decisions.
	declineRoll := g.random.NextDouble()
	if declineRoll < 0.33 {
		if declineRoll < 0.2 {
			return Of("05")
		}
		return Of("91")
	}
	if declineRoll < 0.8 {
		return Of("51")
	}
	return Of("54")
}

// ---------------------------------------------------------------------------------------------
// The router.
// ---------------------------------------------------------------------------------------------

// Candidate is one ranked option: the inputs to the score are kept so the decision can be logged and explained.
type Candidate struct {
	Processor           ProcessorConfig
	AuthRate            float64
	Fee                 Money
	ExpectedMarginMinor float64
}

func (c Candidate) String() string {
	return fmt.Sprintf("%-6s auth %.3f  fee %-10s expected margin %8.2f minor", c.Processor.ID, c.AuthRate, c.Fee, c.ExpectedMarginMinor)
}

type Attempt struct {
	Number      int
	ProcessorID string
	Code        string
	Decline     DeclineClass
	AtMillis    int64
}

func (a Attempt) String() string {
	return fmt.Sprintf("#%d %-6s %-13s %-16s %s", a.Number, a.ProcessorID, a.Code, a.Decline, Meaning(a.Code))
}

type RouteOutcome int

const (
	ROUTE_APPROVED RouteOutcome = iota
	DECLINED
	PENDING_UNKNOWN
	NO_ROUTE
)

func (o RouteOutcome) String() string {
	return [...]string{"APPROVED", "DECLINED", "PENDING_UNKNOWN", "NO_ROUTE"}[o]
}

type RouteResult struct {
	PaymentID  string
	Outcome    RouteOutcome
	Attempts   []Attempt
	StopReason string
}

// ApprovedBy is the processor that approved, or "" (Java null) when the payment was not approved.
func (r RouteResult) ApprovedBy() string {
	if r.Outcome == ROUTE_APPROVED {
		return r.Attempts[len(r.Attempts)-1].ProcessorID
	}
	return ""
}

type RoutingPolicy struct {
	MaxAttempts     int
	ExplorationRate float64
}

func NewRoutingPolicy(maxAttempts int, explorationRate float64) (RoutingPolicy, error) {
	if maxAttempts < 1 {
		return RoutingPolicy{}, fmt.Errorf("%w: maxAttempts must be >= 1", ErrInvalidArgument)
	}
	if explorationRate < 0 || explorationRate >= 1 {
		return RoutingPolicy{}, fmt.Errorf("%w: explorationRate must be in [0,1)", ErrInvalidArgument)
	}
	return RoutingPolicy{maxAttempts, explorationRate}, nil
}

// HealthFactory builds one circuit breaker per processor (Java's BiFunction<String, Clock, ProcessorHealth>).
type HealthFactory func(processorID string, clock Clock) *ProcessorHealth

type Router struct {
	order      []string // processor ids in registration order (LinkedHashMap)
	processors map[string]ProcessorConfig
	health     map[string]*ProcessorHealth
	estimator  *AuthRateEstimator
	budget     *RetryBudget
	gateway    ProcessorGateway
	clock      Clock
	policy     RoutingPolicy
	random     *JavaRandom
}

func NewRouter(configs []ProcessorConfig, healthFactory HealthFactory, estimator *AuthRateEstimator,
	budget *RetryBudget, gateway ProcessorGateway, clock Clock, policy RoutingPolicy, random *JavaRandom) *Router {
	r := &Router{processors: map[string]ProcessorConfig{}, health: map[string]*ProcessorHealth{}, estimator: estimator,
		budget: budget, gateway: gateway, clock: clock, policy: policy, random: random}
	for _, c := range configs {
		if _, seen := r.processors[c.ID]; !seen {
			r.order = append(r.order, c.ID)
		}
		r.processors[c.ID] = c
		r.health[c.ID] = healthFactory(c.ID, clock)
	}
	return r
}

func (r *Router) Health(processorID string) *ProcessorHealth { return r.health[processorID] }
func (r *Router) Estimator() *AuthRateEstimator              { return r.estimator }
func (r *Router) Budget() *RetryBudget                       { return r.budget }

// Rank returns the eligible processors, best first. Eligible: accepts the network and currency, and its
// circuit is not OPEN. Score: P(approve) * (amount - fee), the expected margin of sending the attempt there.
// Ties break on processor id so the order is deterministic.
func (r *Router) Rank(request PaymentRequest) ([]Candidate, error) {
	var out []Candidate
	for _, id := range r.order {
		p := r.processors[id]
		if !p.Accepts(request) || !r.health[p.ID].Allows() {
			continue
		}
		rate := r.estimator.Estimate(p.ID, request.Network)
		fee, err := p.Fee(request.Amount)
		if err != nil {
			return nil, err
		}
		net, err := request.Amount.Minus(fee)
		if err != nil {
			return nil, err
		}
		margin := rate * float64(net.MinorUnits)
		out = append(out, Candidate{p, rate, fee, margin})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].ExpectedMarginMinor != out[j].ExpectedMarginMinor {
			return out[i].ExpectedMarginMinor > out[j].ExpectedMarginMinor
		}
		return out[i].Processor.ID < out[j].Processor.ID
	})
	return out, nil
}

// Route tries the ranked processors in order. Stop on approval, on any decline that is not SOFT_CASCADE,
// on the per-payment attempt cap, or when the shared retry budget cannot pay for a cascade.
func (r *Router) Route(request PaymentRequest) (RouteResult, error) {
	ranked, err := r.Rank(request)
	if err != nil {
		return RouteResult{}, err
	}
	if len(ranked) == 0 {
		return RouteResult{request.ID, NO_ROUTE, nil, "no eligible processor"}, nil
	}
	if len(ranked) > 1 && r.policy.ExplorationRate > 0 && r.random.NextDouble() < r.policy.ExplorationRate {
		j := 1 + int(r.random.NextInt(int32(len(ranked)-1))) // explore: lead with a non-best candidate
		ranked[0], ranked[j] = ranked[j], ranked[0]
	}
	attempts := []Attempt{}
	r.budget.RecordFirstAttempt()
	for _, c := range ranked {
		if len(attempts) >= r.policy.MaxAttempts {
			return RouteResult{request.ID, DECLINED, attempts, fmt.Sprintf("attempt cap %d reached", r.policy.MaxAttempts)}, nil
		}
		if len(attempts) > 0 && !r.budget.TryAcquireRetry() {
			return RouteResult{request.ID, DECLINED, attempts, "retry budget exhausted"}, nil
		}
		pid := c.Processor.ID
		if !r.health[pid].Allows() {
			continue // tripped by an earlier attempt of this same payment
		}
		response := r.gateway.Authorize(pid, request)
		decline := Classify(response)
		attempts = append(attempts, Attempt{len(attempts) + 1, pid, response.Code, decline, r.clock.NowMillis()})
		r.learn(pid, request, response, decline)
		switch decline {
		case APPROVED:
			return RouteResult{request.ID, ROUTE_APPROVED, attempts, "approved"}, nil
		case SOFT_CASCADE:
			// the only class that may try the next processor
		case SOFT_RETRY_LATER:
			return RouteResult{request.ID, DECLINED, attempts, "issuer decline: retry later, not elsewhere"}, nil
		case AUTH_REQUIRED:
			return RouteResult{request.ID, DECLINED, attempts, "step up with 3-D Secure, then retry"}, nil
		case HARD:
			return RouteResult{request.ID, DECLINED, attempts, "hard decline: do not retry this card"}, nil
		case AMBIGUOUS:
			return RouteResult{request.ID, PENDING_UNKNOWN, attempts, "timeout: query or reverse at " + pid + " before any cascade"}, nil
		}
	}
	return RouteResult{request.ID, DECLINED, attempts, "all eligible processors tried"}, nil
}

// learn: health learns from technical outcomes; the auth-rate estimate learns only from issuer decisions.
func (r *Router) learn(pid string, request PaymentRequest, response ProcessorResponse, decline DeclineClass) {
	h := r.health[pid]
	if response.Technical() {
		h.RecordFailure()
		return
	}
	h.RecordSuccess()
	r.estimator.Record(pid, request.Network, decline == APPROVED)
}

// ---------------------------------------------------------------------------------------------
// Demo
// ---------------------------------------------------------------------------------------------

func mustConfig(c ProcessorConfig, err error) ProcessorConfig {
	if err != nil {
		panic(err)
	}
	return c
}

func mustRequest(r PaymentRequest, err error) PaymentRequest {
	if err != nil {
		panic(err)
	}
	return r
}

func mustEstimator(e *AuthRateEstimator, err error) *AuthRateEstimator {
	if err != nil {
		panic(err)
	}
	return e
}

func mustRoute(r RouteResult, err error) RouteResult {
	if err != nil {
		panic(err)
	}
	return r
}

func mustRank(c []Candidate, err error) []Candidate {
	if err != nil {
		panic(err)
	}
	return c
}

// Processors is the demo fleet: two general acquirers with different fees and one Amex-only acquirer.
func Processors() []ProcessorConfig {
	usdEur := []Currency{USD, EUR}
	return []ProcessorConfig{
		mustConfig(NewProcessorConfig("acq_a", []CardNetwork{VISA, MASTERCARD}, usdEur, 220, 20)),
		mustConfig(NewProcessorConfig("acq_b", []CardNetwork{VISA, MASTERCARD}, usdEur, 290, 30)),
		mustConfig(NewProcessorConfig("acq_c", []CardNetwork{AMEX}, []Currency{USD}, 350, 0)),
	}
}

func defaultHealth(id string, c Clock) *ProcessorHealth { return NewProcessorHealth(c, 10, 5, 30_000) }

func router(gateway ProcessorGateway, clock *FakeClock, est *AuthRateEstimator, budget *RetryBudget, explore float64) *Router {
	policy, err := NewRoutingPolicy(3, explore)
	if err != nil {
		panic(err)
	}
	return NewRouter(Processors(), defaultHealth, est, budget, gateway, clock, policy, NewJavaRandom(7))
}

func printResult(r RouteResult) {
	fmt.Printf("%s -> %v (%s)\n", r.PaymentID, r.Outcome, r.StopReason)
	for _, a := range r.Attempts {
		fmt.Println("   " + a.String())
	}
}

func ids(cs []Candidate) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.Processor.ID
	}
	return out
}

func main() {
	clock := NewFakeClock(1_700_000_000_000)

	fmt.Println("== Decline classification table ==")
	for _, r := range DeclineTable {
		fmt.Printf("%-13s %-17v %s\n", r.Code, r.Decline, r.Meaning)
	}

	fmt.Println()
	fmt.Println("== Ranking a 100.00 USD Visa payment (expected margin = P(approve) x (amount - fee)) ==")
	est := mustEstimator(NewAuthRateEstimator(0.85, 20, 0.995))
	for i := 0; i < 100; i++ {
		est.Record("acq_a", VISA, i%100 < 86)
	}
	for i := 0; i < 100; i++ {
		est.Record("acq_b", VISA, i%100 < 93)
	}
	gw := NewScriptedGateway()
	r := router(gw, clock, est, NewRetryBudget(100, 10_000, 3_000), 0)
	p100 := mustRequest(NewPaymentRequest("pay_100", OfMinor(10_000, USD), VISA, "fp_1"))
	for _, c := range mustRank(r.Rank(p100)) {
		fmt.Println(c)
	}

	fmt.Println()
	fmt.Println("== Cascading: only SOFT_CASCADE moves to the next processor ==")
	gw.Script("acq_b", Of("91")).Script("acq_a", Approved())
	printResult(mustRoute(r.Route(p100)))
	gw.Script("acq_b", Of("43"))
	printResult(mustRoute(r.Route(mustRequest(NewPaymentRequest("pay_stolen", OfMinor(10_000, USD), VISA, "fp_2")))))
	gw.Script("acq_b", Of("51"))
	printResult(mustRoute(r.Route(mustRequest(NewPaymentRequest("pay_nsf", OfMinor(10_000, USD), VISA, "fp_3")))))
	gw.Script("acq_b", OfMAC("05", "03"))
	printResult(mustRoute(r.Route(mustRequest(NewPaymentRequest("pay_mac03", OfMinor(10_000, USD), VISA, "fp_4")))))
	gw.Script("acq_b", Timeout())
	printResult(mustRoute(r.Route(mustRequest(NewPaymentRequest("pay_timeout", OfMinor(10_000, USD), VISA, "fp_5")))))
	gw.Script("acq_b", Of("96")).Script("acq_a", Of("05"))
	printResult(mustRoute(r.Route(mustRequest(NewPaymentRequest("pay_exhaust", OfMinor(10_000, USD), VISA, "fp_6")))))
	printResult(mustRoute(r.Route(mustRequest(NewPaymentRequest("pay_amex", OfMinor(10_000, EUR), AMEX, "fp_7")))))

	fmt.Println()
	fmt.Println("== Circuit breaker: 5 technical failures in the last 10 calls take acq_b out of the ranking ==")
	flaky := NewScriptedGateway()
	cb := router(flaky, clock, est, NewRetryBudget(100, 10_000, 10_000), 0)
	sent := 0
	for cb.Health("acq_b").Allows() {
		flaky.Script("acq_b", ConnectError())
		res := mustRoute(cb.Route(mustRequest(NewPaymentRequest(fmt.Sprintf("pay_ce%d", sent), OfMinor(5_000, USD), VISA, "fp_ce"))))
		fmt.Printf("pay_ce%d -> %v via %s after %d attempts\n", sent, res.Outcome, res.ApprovedBy(), len(res.Attempts))
		sent++
	}
	fmt.Printf("acq_b circuit: %v; ranking now [%s]\n", cb.Health("acq_b").State(), strings.Join(ids(mustRank(cb.Rank(p100))), ", "))
	clock.Advance(30_000)
	fmt.Printf("after 30s cool-down: %v; the next payment probes it\n", cb.Health("acq_b").State())
	probe := mustRoute(cb.Route(mustRequest(NewPaymentRequest("pay_probe", OfMinor(5_000, USD), VISA, "fp_p"))))
	fmt.Printf("probe approved by %s; acq_b circuit: %v\n", probe.ApprovedBy(), cb.Health("acq_b").State())

	fmt.Println()
	fmt.Println("== Retry budget: an issuer outage makes every first attempt a 91 ==")
	outage := NewScriptedGateway()
	storm := router(outage, clock, mustEstimator(NewAuthRateEstimator(0.85, 20, 0.995)), NewRetryBudget(100, 10_000, 3_000), 0)
	cascades, attempts := 0, 0
	for i := 0; i < 20; i++ {
		outage.Script("acq_a", Of("91")).Script("acq_b", Of("91"))
		res := mustRoute(storm.Route(mustRequest(NewPaymentRequest(fmt.Sprintf("pay_s%d", i), OfMinor(2_000, USD), VISA, fmt.Sprintf("fp_s%d", i)))))
		attempts += len(res.Attempts)
		cascades += len(res.Attempts) - 1
	}
	fmt.Printf("20 payments -> %d processor calls, %d cascades; budget left %d milli-tokens (without a budget: 40 calls)\n",
		attempts, cascades, storm.Budget().BalanceMilli())

	fmt.Println()
	fmt.Println("== Learning: acq_a's true Visa approval rate is 0.80, acq_b's is 0.93; both start at the 0.85 prior ==")
	learn := mustEstimator(NewAuthRateEstimator(0.85, 20, 0.995))
	sim := NewSimulatedGateway(42).Rate("acq_a", VISA, 0.80).Rate("acq_b", VISA, 0.93)
	lr := router(sim, clock, learn, NewRetryBudget(100, 10_000, 3_000), 0.05)
	firstChoice := map[string]int{}
	var firstOrder []string
	approved := 0
	for i := 0; i < 2_000; i++ {
		res := mustRoute(lr.Route(mustRequest(NewPaymentRequest(fmt.Sprintf("pay_l%d", i), OfMinor(10_000, USD), VISA, fmt.Sprintf("fp_l%d", i)))))
		first := res.Attempts[0].ProcessorID
		if _, seen := firstChoice[first]; !seen {
			firstOrder = append(firstOrder, first)
		}
		firstChoice[first]++
		if res.Outcome == ROUTE_APPROVED {
			approved++
		}
	}
	fmt.Printf("estimates after 2000 payments: acq_a %.3f, acq_b %.3f\n", learn.Estimate("acq_a", VISA), learn.Estimate("acq_b", VISA))
	parts := make([]string, len(firstOrder))
	for i, id := range firstOrder {
		parts[i] = fmt.Sprintf("%s=%d", id, firstChoice[id])
	}
	fmt.Printf("first attempts: {%s}; approved %d/2000\n", strings.Join(parts, ", "), approved)
}
