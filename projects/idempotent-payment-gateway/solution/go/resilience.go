// Project: projects/idempotent-payment-gateway — Stage 03: retries, backoff with full jitter, retry budget,
// circuit breaker, and the Gateway that ties the idempotency store, the state machine, and the processor together.
// Backoff follows the "full jitter" recipe: sleep = uniform(0, min(cap, base * 2^(retry-1))), drawn from the
// seeded Rng so every schedule is replayable. Time comes from the injected Clock; nothing here ever blocks.
// Go 1.22+, standard library only. Same design as the Java and Python solutions.
package main

import (
	"errors"
	"fmt"
	"strings"
)

type ProcessorOutcome int

const (
	Approved    ProcessorOutcome = iota // funds captured; the processor returns a reference
	Declined                            // a definitive answer: never retry a decline
	Unavailable                         // timeout, 5xx, connection reset: the only outcome worth retrying
)

func (o ProcessorOutcome) String() string {
	return [...]string{"APPROVED", "DECLINED", "UNAVAILABLE"}[o]
}

type ProcessorResult struct {
	Outcome   ProcessorOutcome
	Reference string
}

type Processor interface {
	Charge(request ChargeRequest) ProcessorResult
}

// ScriptedProcessor is a fake processor driven by a script: 'A' approve, 'D' decline, 'U' unavailable. "UUA" fails
// twice then approves. When the script runs out the last character repeats, so "A" is always healthy and "U" is dead.
type ScriptedProcessor struct {
	script string
	calls  int
}

func NewScriptedProcessor(script string) (*ScriptedProcessor, error) {
	if script == "" || strings.Trim(script, "ADU") != "" {
		return nil, fmt.Errorf("script must be a non-empty string of A, D, U: %q", script)
	}
	return &ScriptedProcessor{script: script}, nil
}

func (p *ScriptedProcessor) Charge(ChargeRequest) ProcessorResult {
	step := p.script[min(p.calls, len(p.script)-1)]
	p.calls++
	switch step {
	case 'A':
		return ProcessorResult{Approved, fmt.Sprintf("auth-%d", p.calls)}
	case 'D':
		return ProcessorResult{Declined, fmt.Sprintf("decl-%d", p.calls)}
	default:
		return ProcessorResult{Unavailable, ""}
	}
}

func (p *ScriptedProcessor) Calls() int { return p.calls }

// BackoffPolicy is exponential backoff with full jitter and a hard cap, plus the maximum number of attempts (first try included).
type BackoffPolicy struct {
	baseMillis  int64
	capMillis   int64
	maxAttempts int
}

func NewBackoffPolicy(baseMillis, capMillis int64, maxAttempts int) (*BackoffPolicy, error) {
	if baseMillis <= 0 || capMillis < baseMillis {
		return nil, errors.New("need 0 < base <= cap")
	}
	if maxAttempts < 1 {
		return nil, errors.New("maxAttempts must be at least 1")
	}
	return &BackoffPolicy{baseMillis, capMillis, maxAttempts}, nil
}

func (b *BackoffPolicy) MaxAttempts() int { return b.maxAttempts }

// CeilingFor is min(cap, base * 2^(retry-1)) for retry >= 1: the largest delay full jitter may draw.
// retry < 1 is a programming error and panics.
func (b *BackoffPolicy) CeilingFor(retry int) int64 {
	if retry < 1 {
		panic("retry numbers start at 1")
	}
	ceiling := b.baseMillis
	for i := 1; i < retry && ceiling < b.capMillis; i++ {
		ceiling = min(b.capMillis, ceiling*2)
	}
	return min(b.capMillis, ceiling)
}

// DelayBefore is uniform(0, ceiling) inclusive, from the injected Rng.
func (b *BackoffPolicy) DelayBefore(retry int, rng *Rng) int64 {
	return rng.NextBelow(b.CeilingFor(retry) + 1)
}

// RetryBudget is a token budget for retries. Every retry withdraws one token; every definitive answer from the
// processor deposits one (up to the cap). When the processor is down, the budget drains and retries stop: the
// retry storm that would otherwise finish the processor off never starts.
type RetryBudget struct {
	maxTokens int
	tokens    int
}

func NewRetryBudget(initialTokens, maxTokens int) (*RetryBudget, error) {
	if maxTokens < 1 || initialTokens < 0 || initialTokens > maxTokens {
		return nil, errors.New("bad budget")
	}
	return &RetryBudget{maxTokens, initialTokens}, nil
}

func (b *RetryBudget) Tokens() int { return b.tokens }

func (b *RetryBudget) TryWithdraw() bool {
	if b.tokens == 0 {
		return false
	}
	b.tokens--
	return true
}

// Deposit adds tokens up to the cap; a negative deposit is a programming error and panics.
func (b *RetryBudget) Deposit(n int) {
	if n < 0 {
		panic("deposit must be non-negative")
	}
	b.tokens = min(b.maxTokens, b.tokens+n)
}

type CircuitState int

const (
	CircuitClosed CircuitState = iota
	CircuitOpen
	CircuitHalfOpen
)

func (s CircuitState) String() string { return [...]string{"CLOSED", "OPEN", "HALF_OPEN"}[s] }

// CircuitBreaker:
// Closed: everything flows; failureThreshold consecutive failures open the circuit.
// Open: every request is refused until openMillis have passed on the injected clock, then HalfOpen.
// HalfOpen: requests flow as probes; one failure reopens, successesToClose consecutive successes close.
type CircuitBreaker struct {
	clock               Clock
	failureThreshold    int
	openMillis          int64
	successesToClose    int
	state               CircuitState
	consecutiveFailures int
	halfOpenSuccesses   int
	openedAt            int64
}

func NewCircuitBreaker(clock Clock, failureThreshold int, openMillis int64, successesToClose int) (*CircuitBreaker, error) {
	if failureThreshold < 1 || openMillis <= 0 || successesToClose < 1 {
		return nil, errors.New("bad breaker settings")
	}
	if clock == nil {
		return nil, errors.New("clock is required")
	}
	return &CircuitBreaker{clock: clock, failureThreshold: failureThreshold, openMillis: openMillis, successesToClose: successesToClose}, nil
}

// State is the current state after applying the Open -> HalfOpen timer.
func (c *CircuitBreaker) State() CircuitState {
	if c.state == CircuitOpen && c.clock.NowMillis()-c.openedAt >= c.openMillis {
		c.state = CircuitHalfOpen
		c.halfOpenSuccesses = 0
	}
	return c.state
}

func (c *CircuitBreaker) AllowRequest() bool { return c.State() != CircuitOpen }

func (c *CircuitBreaker) RecordSuccess() {
	current := c.State()
	c.consecutiveFailures = 0
	if current == CircuitHalfOpen {
		c.halfOpenSuccesses++
		if c.halfOpenSuccesses >= c.successesToClose {
			c.state = CircuitClosed
			c.halfOpenSuccesses = 0
		}
	}
}

func (c *CircuitBreaker) RecordFailure() {
	current := c.State()
	c.consecutiveFailures++
	if current == CircuitHalfOpen || c.consecutiveFailures >= c.failureThreshold {
		c.trip()
	}
}

func (c *CircuitBreaker) trip() {
	c.state = CircuitOpen
	c.openedAt = c.clock.NowMillis()
	c.consecutiveFailures = 0
	c.halfOpenSuccesses = 0
}

// RetryAfterMillis is the time until an Open circuit becomes HalfOpen; zero when requests are allowed.
func (c *CircuitBreaker) RetryAfterMillis() int64 {
	if c.State() == CircuitOpen {
		return c.openedAt + c.openMillis - c.clock.NowMillis()
	}
	return 0
}

// ChargeOutcome is the result of a charge through the retrying client: the final outcome plus how it was reached.
type ChargeOutcome struct {
	Outcome      ProcessorOutcome
	Reference    string
	Attempts     int
	WaitedMillis int64
	Reason       string
}

// RetryingClient wraps a Processor with backoff, a retry budget, and a circuit breaker. Only Unavailable is retried.
type RetryingClient struct {
	processor Processor
	policy    *BackoffPolicy
	budget    *RetryBudget
	breaker   *CircuitBreaker
	rng       *Rng
	clock     Clock
}

func NewRetryingClient(processor Processor, policy *BackoffPolicy, budget *RetryBudget, breaker *CircuitBreaker, rng *Rng, clock Clock) *RetryingClient {
	if processor == nil || policy == nil || budget == nil || breaker == nil || rng == nil || clock == nil {
		panic("RetryingClient needs every collaborator")
	}
	return &RetryingClient{processor, policy, budget, breaker, rng, clock}
}

func (c *RetryingClient) Charge(request ChargeRequest) ChargeOutcome {
	if !c.breaker.AllowRequest() {
		return ChargeOutcome{Unavailable, "", 0, 0, "circuit open"}
	}
	attempts := 0
	var waited int64
	for {
		attempts++
		result := c.processor.Charge(request)
		if result.Outcome != Unavailable {
			c.breaker.RecordSuccess()
			c.budget.Deposit(1)
			reason := "declined"
			if result.Outcome == Approved {
				reason = "approved"
			}
			return ChargeOutcome{result.Outcome, result.Reference, attempts, waited, reason}
		}
		c.breaker.RecordFailure()
		if attempts >= c.policy.MaxAttempts() {
			return ChargeOutcome{Unavailable, "", attempts, waited, "retries exhausted"}
		}
		if !c.breaker.AllowRequest() {
			return ChargeOutcome{Unavailable, "", attempts, waited, "circuit open"}
		}
		if !c.budget.TryWithdraw() {
			return ChargeOutcome{Unavailable, "", attempts, waited, "retry budget exhausted"}
		}
		delay := c.policy.DelayBefore(attempts, c.rng)
		c.clock.Sleep(delay)
		waited += delay
	}
}

// Gateway is the payment gateway a merchant talks to. One Charge() call: claim the idempotency key, create the
// payment, drive it through the state machine with the retrying client, store the response under the key.
// Responses that must not be replayed (processor unavailable, circuit open) release the key instead.
type Gateway struct {
	store      *IdempotencyStore
	client     *RetryingClient
	clock      Clock
	order      []string
	payments   map[string]*Payment
	dispatcher *WebhookDispatcher // Stage 04; nil until attached
	paymentSeq int
	eventSeq   int
}

func NewGateway(store *IdempotencyStore, client *RetryingClient, clock Clock) *Gateway {
	if store == nil || client == nil || clock == nil {
		panic("Gateway needs a store, a client and a clock")
	}
	return &Gateway{store: store, client: client, clock: clock, payments: map[string]*Payment{}}
}

func (g *Gateway) Charge(request ChargeRequest) ApiResponse {
	begin := g.store.Begin(request.MerchantID, request.IdempotencyKey, request.Fingerprint())
	switch begin.Decision {
	case DecisionReplay:
		return begin.Response.AsReplay()
	case DecisionInProgress:
		return ApiResponse{409, `{"error":"request in progress"}`, false}
	case DecisionConflict:
		return ApiResponse{422, `{"error":"idempotency key reused with a different request"}`, false}
	}
	g.paymentSeq++
	payment := must1(NewPayment(fmt.Sprintf("pay_%d", g.paymentSeq), request.Amount, g.clock))
	g.payments[payment.ID()] = payment
	g.order = append(g.order, payment.ID())
	_ = payment.StartProcessing()
	outcome := g.client.Charge(request)
	var response ApiResponse
	var eventType string
	switch outcome.Outcome {
	case Approved:
		_ = payment.Succeed(outcome.Reference)
		response = ApiResponse{201, body(payment), false}
		eventType = "payment.succeeded"
	case Declined:
		_ = payment.Fail("declined " + outcome.Reference)
		response = ApiResponse{402, body(payment), false}
		eventType = "payment.failed"
	default:
		_ = payment.Fail(outcome.Reason)
		_, _ = g.store.Release(request.MerchantID, request.IdempotencyKey)
		return ApiResponse{503, fmt.Sprintf(`{"error":"%s","payment_id":"%s"}`, outcome.Reason, payment.ID()), false}
	}
	// Complete the key first, then emit: a crash in between loses an event (detectable, repairable) rather
	// than leaving the key open so a retry charges again. The transactional outbox closes even that gap.
	_ = g.store.Complete(request.MerchantID, request.IdempotencyKey, response)
	g.emit(eventType, payment)
	return response
}

func body(p *Payment) string {
	return fmt.Sprintf(`{"payment_id":"%s","state":"%s","amount":"%v"}`, p.ID(), p.State().Wire(), p.Amount())
}

// AttachWebhooks (Stage 04) routes lifecycle events to a dispatcher.
func (g *Gateway) AttachWebhooks(dispatcher *WebhookDispatcher) {
	if dispatcher == nil {
		panic("dispatcher is required")
	}
	g.dispatcher = dispatcher
}

func (g *Gateway) emit(eventType string, payment *Payment) {
	if g.dispatcher == nil {
		return
	}
	g.eventSeq++
	event := must1(NewWebhookEvent(fmt.Sprintf("evt_%d", g.eventSeq), eventType, payment.ID(), fmt.Sprintf("amount=%v", payment.Amount()), g.clock.NowMillis()))
	g.dispatcher.Enqueue(event)
}

// Payment is the payment with this id, if the gateway created one (Optional -> value, ok).
func (g *Gateway) Payment(id string) (*Payment, bool) {
	p, ok := g.payments[id]
	return p, ok
}

// Payments in creation order; a fresh slice.
func (g *Gateway) Payments() []*Payment {
	out := make([]*Payment, 0, len(g.order))
	for _, id := range g.order {
		out = append(out, g.payments[id])
	}
	return out
}

// must1 unwraps a (T, error) pair for values the gateway built itself from inputs it has already validated.
func must1[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}
