// Stage 3 tests for projects/idempotent-payment-gateway (Go): backoff ceilings and full jitter from a seeded Rng,
// retryable-error classification, the retry budget, the Closed/Open/HalfOpen breaker on an injected clock,
// and the Gateway that ties the store, the state machine and the client together.
// Port of Stage03Test.java: same checks, same names, same order.
package main

import (
	"errors"
	"strings"
	"testing"
)

func s03check(t *testing.T, name string, condition bool) {
	t.Helper()
	if !condition {
		t.Errorf("FAIL: %s", name)
	}
}

func s03checkErr(t *testing.T, name string, err, want error) {
	t.Helper()
	if err == nil {
		t.Errorf("FAIL: %s (no error)", name)
		return
	}
	if want != nil && !errors.Is(err, want) {
		t.Errorf("FAIL: %s (wrong error: %v)", name, err)
	}
}

// s03panics checks that a programming error (Java's IllegalArgumentException on a bad argument) panics.
func s03panics(t *testing.T, name string, action func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Errorf("FAIL: %s (no panic)", name)
		}
	}()
	action()
}

func s03request(key, amount string) ChargeRequest {
	return must1(NewChargeRequest("m_1", key, must(Parse(amount, USD)), "tok_visa", "order-42"))
}

func s03policy(base, capMillis int64, maxAttempts int) *BackoffPolicy {
	return must1(NewBackoffPolicy(base, capMillis, maxAttempts))
}

func s03breaker(clock Clock, threshold int, open int64, toClose int) *CircuitBreaker {
	return must1(NewCircuitBreaker(clock, threshold, open, toClose))
}

func s03budget(initial, maxTokens int) *RetryBudget { return must1(NewRetryBudget(initial, maxTokens)) }

func s03processor(script string) *ScriptedProcessor { return must1(NewScriptedProcessor(script)) }

func s03client(script string, clock *FakeClock, breaker *CircuitBreaker, budget *RetryBudget, seed int64) *RetryingClient {
	return NewRetryingClient(s03processor(script), s03policy(100, 2_000, 4), budget, breaker, NewRng(seed), clock)
}

// s03state folds Gateway.Payment into a state; a missing payment reads as a state no check expects.
func s03state(g *Gateway, id string) PaymentState {
	p, ok := g.Payment(id)
	if !ok {
		return PaymentState(-1)
	}
	return p.State()
}

func TestStage03BackoffBudgetBreaker(t *testing.T) {
	req := s03request("key-1", "19.99")

	// Scripted processor: A approve, D decline, U unavailable; last character repeats
	scripted := s03processor("UDA")
	s03check(t, "script step 1 unavailable", scripted.Charge(req).Outcome == Unavailable)
	s03check(t, "script step 2 declined", scripted.Charge(req).Outcome == Declined)
	s03check(t, "script step 3 approved with reference", scripted.Charge(req).Outcome == Approved && scripted.Charge(req).Reference == "auth-4")
	s03check(t, "last character repeats", scripted.Charge(req).Outcome == Approved && scripted.Calls() == 5)
	_, err := NewScriptedProcessor("AXU")
	s03checkErr(t, "script must be A/D/U", err, nil)
	_, err = NewScriptedProcessor("")
	s03checkErr(t, "script must be non-empty", err, nil)

	// Backoff: ceiling = min(cap, base * 2^(retry-1)); delay = uniform(0, ceiling) from the seeded Rng
	policy := s03policy(100, 2_000, 4)
	s03check(t, "ceiling retry 1", policy.CeilingFor(1) == 100)
	s03check(t, "ceiling retry 2", policy.CeilingFor(2) == 200)
	s03check(t, "ceiling retry 4", policy.CeilingFor(4) == 800)
	s03check(t, "ceiling is capped", policy.CeilingFor(6) == 2_000 && policy.CeilingFor(40) == 2_000)
	s03check(t, "MaxAttempts", policy.MaxAttempts() == 4)
	s03panics(t, "retry numbers start at 1", func() { policy.CeilingFor(0) })
	_, err = NewBackoffPolicy(500, 100, 3)
	s03checkErr(t, "cap below base rejected", err, nil)
	_, err = NewBackoffPolicy(100, 200, 0)
	s03checkErr(t, "maxAttempts at least 1", err, nil)
	rng := NewRng(42)
	s03check(t, "seed 42 draws 46, 26, 131, 226", policy.DelayBefore(1, rng) == 46 && policy.DelayBefore(2, rng) == 26 &&
		policy.DelayBefore(3, rng) == 131 && policy.DelayBefore(4, rng) == 226)
	s03check(t, "same seed, same schedule", policy.DelayBefore(1, NewRng(42)) == 46 && policy.DelayBefore(1, NewRng(42)) == 46)
	withinCeiling := true
	spread := NewRng(7)
	for i := 0; i < 500; i++ {
		d := policy.DelayBefore(3, spread)
		if d < 0 || d > 400 {
			withinCeiling = false
		}
	}
	s03check(t, "full jitter never exceeds the ceiling", withinCeiling)
	s03check(t, "Rng.NextBelow is uniform over [0, bound)", NewRng(42).NextBelow(101) == 46 && NewRng(7).NextBelow(1000) == 822)
	s03panics(t, "NextBelow needs a positive bound", func() { NewRng(1).NextBelow(0) })

	// Retry budget
	budget := s03budget(2, 3)
	s03check(t, "budget starts with initial tokens", budget.Tokens() == 2)
	s03check(t, "withdraw twice", budget.TryWithdraw() && budget.TryWithdraw() && budget.Tokens() == 0)
	s03check(t, "empty budget refuses", !budget.TryWithdraw())
	budget.Deposit(5)
	s03check(t, "deposit is capped at max", budget.Tokens() == 3)
	s03panics(t, "negative deposit", func() { budget.Deposit(-1) })
	_, err = NewRetryBudget(5, 3)
	s03checkErr(t, "initial above max", err, nil)

	// Circuit breaker on the injected clock
	clock := NewFakeClock(100_000)
	breaker := s03breaker(clock, 3, 30_000, 2)
	s03check(t, "starts CLOSED", breaker.State() == CircuitClosed && breaker.AllowRequest() && breaker.RetryAfterMillis() == 0)
	breaker.RecordFailure()
	breaker.RecordFailure()
	s03check(t, "two failures still CLOSED", breaker.State() == CircuitClosed)
	breaker.RecordSuccess()
	breaker.RecordFailure()
	breaker.RecordFailure()
	s03check(t, "success resets the consecutive count", breaker.State() == CircuitClosed)
	breaker.RecordFailure()
	s03check(t, "third consecutive failure OPENs", breaker.State() == CircuitOpen && !breaker.AllowRequest())
	s03check(t, "RetryAfter is the full open window", breaker.RetryAfterMillis() == 30_000)
	clock.Advance(29_999)
	s03check(t, "still OPEN one ms before the window ends", breaker.State() == CircuitOpen && breaker.RetryAfterMillis() == 1)
	clock.Advance(1)
	s03check(t, "HALF_OPEN after the window", breaker.State() == CircuitHalfOpen && breaker.AllowRequest())
	breaker.RecordFailure()
	s03check(t, "one HALF_OPEN failure reopens", breaker.State() == CircuitOpen && breaker.RetryAfterMillis() == 30_000)
	clock.Advance(30_000)
	breaker.RecordSuccess()
	s03check(t, "one of two successes keeps HALF_OPEN", breaker.State() == CircuitHalfOpen)
	breaker.RecordSuccess()
	s03check(t, "second success CLOSEs", breaker.State() == CircuitClosed)
	_, err = NewCircuitBreaker(clock, 1, 0, 1)
	s03checkErr(t, "open window must be positive", err, nil)
}

func TestStage03RetryingClientAndGateway(t *testing.T) {
	req := s03request("key-1", "19.99")

	// Retrying client: only Unavailable is retried, with full-jitter waits on the clock
	c1 := NewFakeClock(0)
	ok := s03client("UUA", c1, s03breaker(c1, 5, 30_000, 1), s03budget(10, 10), 42).Charge(req)
	s03check(t, "UUA approves on the third attempt", ok.Outcome == Approved && ok.Attempts == 3 && ok.Reference == "auth-3" && ok.Reason == "approved")
	s03check(t, "waited 46 + 26 ms from seed 42", ok.WaitedMillis == 72 && c1.NowMillis() == 72)

	c2 := NewFakeClock(0)
	declining := s03processor("D")
	declined := NewRetryingClient(declining, s03policy(100, 2_000, 4), s03budget(10, 10), s03breaker(c2, 5, 30_000, 1), NewRng(42), c2).Charge(req)
	s03check(t, "a decline is never retried", declined.Outcome == Declined && declined.Attempts == 1 && declining.Calls() == 1 && declined.Reason == "declined")
	s03check(t, "no waiting on a decline", declined.WaitedMillis == 0 && c2.NowMillis() == 0)

	c3 := NewFakeClock(0)
	exhausted := s03client("U", c3, s03breaker(c3, 10, 30_000, 1), s03budget(10, 10), 42).Charge(req)
	s03check(t, "dead processor exhausts maxAttempts", exhausted.Outcome == Unavailable && exhausted.Attempts == 4 && exhausted.Reason == "retries exhausted")
	s03check(t, "three waits: 46 + 26 + 131", exhausted.WaitedMillis == 203 && c3.NowMillis() == 203)

	c4 := NewFakeClock(0)
	tiny := s03budget(1, 10)
	budgeted := s03client("U", c4, s03breaker(c4, 10, 30_000, 1), tiny, 42).Charge(req)
	s03check(t, "drained budget stops retries", budgeted.Outcome == Unavailable && budgeted.Attempts == 2 && budgeted.Reason == "retry budget exhausted" && tiny.Tokens() == 0)

	c5 := NewFakeClock(0)
	trip := s03breaker(c5, 2, 30_000, 1)
	dead := s03processor("U")
	tripping := NewRetryingClient(dead, s03policy(100, 2_000, 4), s03budget(10, 10), trip, NewRng(42), c5)
	tripped := tripping.Charge(req)
	s03check(t, "breaker opens mid-retry", tripped.Outcome == Unavailable && tripped.Attempts == 2 && tripped.Reason == "circuit open" && trip.State() == CircuitOpen)
	refused := tripping.Charge(req)
	s03check(t, "open breaker refuses without calling the processor", refused.Attempts == 0 && refused.Reason == "circuit open" && dead.Calls() == 2)

	// Gateway: store + state machine + client
	gc := NewFakeClock(1_000_000)
	store := must1(NewIdempotencyStore(gc, 60_000))
	script := s03processor("ADUUUA")
	client := NewRetryingClient(script, s03policy(100, 2_000, 2), s03budget(10, 10), s03breaker(gc, 3, 30_000, 1), NewRng(42), gc)
	gateway := NewGateway(store, client, gc)

	first := gateway.Charge(s03request("key-1", "19.99"))
	s03check(t, "approved charge is 201", first.Status == 201 && !first.Replayed)
	s03check(t, "body names the payment and state", strings.Contains(first.Body, `"payment_id":"pay_1"`) && strings.Contains(first.Body, `"state":"succeeded"`))
	pay1, found := gateway.Payment("pay_1")
	s03check(t, "payment is SUCCEEDED and captured", found && pay1.State() == Succeeded && pay1.Captured() == must(Parse("19.99", USD)))
	replay := gateway.Charge(s03request("key-1", "19.99"))
	s03check(t, "same request replays the same body, flagged", replay.Status == 201 && replay.Body == first.Body && replay.Replayed)
	s03check(t, "replay does not call the processor again", script.Calls() == 1 && len(gateway.Payments()) == 1)
	conflict := gateway.Charge(s03request("key-1", "29.99"))
	s03check(t, "same key, other amount is 422", conflict.Status == 422 && len(gateway.Payments()) == 1)

	declinedResp := gateway.Charge(s03request("key-2", "5.00"))
	s03check(t, "declined charge is 402 and FAILED", declinedResp.Status == 402 && s03state(gateway, "pay_2") == Failed)
	s03check(t, "declines are stored and replayed", gateway.Charge(s03request("key-2", "5.00")).Replayed && script.Calls() == 2)

	unavailable := gateway.Charge(s03request("key-3", "7.00"))
	s03check(t, "unavailable is 503 after maxAttempts", unavailable.Status == 503 && strings.Contains(unavailable.Body, "retries exhausted") && script.Calls() == 4)
	s03check(t, "failed payment is recorded", s03state(gateway, "pay_3") == Failed)
	_, live := store.Get("m_1", "key-3")
	s03check(t, "503 releases the key", !live)
	again := gateway.Charge(s03request("key-3", "7.00"))
	s03check(t, "retrying a 503 is a fresh attempt, not a replay", again.Status == 503 && !again.Replayed && len(gateway.Payments()) == 4)
	open := gateway.Charge(s03request("key-4", "7.00"))
	s03check(t, "open breaker short-circuits with 503", open.Status == 503 && strings.Contains(open.Body, "circuit open") && script.Calls() == 5)
	gc.Advance(30_000)
	recovered := gateway.Charge(s03request("key-3", "7.00"))
	s03check(t, "after the window the same key succeeds", recovered.Status == 201 && strings.Contains(recovered.Body, `"state":"succeeded"`) && script.Calls() == 6)
	_, missing := gateway.Payment("pay_99")
	s03check(t, "unknown payment id reads as absent", !missing)
}
