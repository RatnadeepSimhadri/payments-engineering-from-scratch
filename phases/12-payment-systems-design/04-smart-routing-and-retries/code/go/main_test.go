// Tests for phases/12-payment-systems-design/04-smart-routing-and-retries. Run: go test ./...
package main

import (
	"errors"
	"math"
	"strconv"
	"strings"
	"testing"
)

var seq = 0

func visa(t *testing.T, minor int64) PaymentRequest {
	t.Helper()
	seq++
	r, err := NewPaymentRequest("pay_t"+strconv.Itoa(seq), OfMinor(minor, USD), VISA, "fp")
	if err != nil {
		t.Fatalf("visa(%d): %v", minor, err)
	}
	return r
}

func check(t *testing.T, name string, condition bool) {
	t.Helper()
	if !condition {
		t.Errorf("FAIL: %s", name)
	}
}

// trained: acq_a is cheaper; acq_b is trusted more. With these counts acq_b ranks first for Visa.
func trained(t *testing.T) *AuthRateEstimator {
	t.Helper()
	est, err := NewAuthRateEstimator(0.85, 20, 0.995)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		est.Record("acq_a", VISA, i < 86)
	}
	for i := 0; i < 100; i++ {
		est.Record("acq_b", VISA, i < 93)
	}
	return est
}

func estimator(t *testing.T, priorRate, priorWeight, decay float64) *AuthRateEstimator {
	t.Helper()
	est, err := NewAuthRateEstimator(priorRate, priorWeight, decay)
	if err != nil {
		t.Fatal(err)
	}
	return est
}

func testRouter(t *testing.T, gw *ScriptedGateway, clock *FakeClock, est *AuthRateEstimator, budget *RetryBudget, maxAttempts int) *Router {
	t.Helper()
	policy, err := NewRoutingPolicy(maxAttempts, 0)
	if err != nil {
		t.Fatal(err)
	}
	return NewRouter(Processors(), defaultHealth, est, budget, gw, clock, policy, NewJavaRandom(1))
}

func rank(t *testing.T, r *Router, req PaymentRequest) []Candidate {
	t.Helper()
	ranked, err := r.Rank(req)
	if err != nil {
		t.Fatal(err)
	}
	return ranked
}

func route(t *testing.T, r *Router, req PaymentRequest) RouteResult {
	t.Helper()
	res, err := r.Route(req)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func leader(t *testing.T, r *Router) string {
	t.Helper()
	return rank(t, r, visa(t, 10_000))[0].Processor.ID
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func TestFeesAndClassification(t *testing.T) {
	a := Processors()[0]
	fee, _ := a.Fee(OfMinor(10_000, USD))
	check(t, "fee 2.20% + 0.20 on 100.00 = 2.40", fee == OfMinor(240, USD))
	fee, _ = a.Fee(OfMinor(25, USD))
	check(t, "fee rounds half-up: 2.20% of 0.25 = 0.55 -> 1 minor, + 20", fee == OfMinor(21, USD))
	cases := []struct {
		name string
		resp ProcessorResponse
		want DeclineClass
	}{
		{"00 is APPROVED", Approved(), APPROVED},
		{"91 cascades", Of("91"), SOFT_CASCADE},
		{"connect error cascades", ConnectError(), SOFT_CASCADE},
		{"51 is retry-later, not cascade", Of("51"), SOFT_RETRY_LATER},
		{"43 stolen is HARD", Of("43"), HARD},
		{"1A asks for authentication", Of("1A"), AUTH_REQUIRED},
		{"timeout is AMBIGUOUS", Timeout(), AMBIGUOUS},
		{"unknown code fails safe to HARD", Of("Z9"), HARD},
		{"MAC 03 turns a soft 05 into HARD", OfMAC("05", "03"), HARD},
		{"MAC 21 turns a soft 91 into HARD", OfMAC("91", "21"), HARD},
		{"MAC 02 (try later) leaves 05 soft", OfMAC("05", "02"), SOFT_CASCADE},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Classify(c.resp); got != c.want {
				t.Errorf("Classify(%v) = %v, want %v", c.resp, got, c.want)
			}
		})
	}
	check(t, "technical() only for connect error and timeout", Timeout().Technical() && ConnectError().Technical() && !Of("91").Technical())
}

func TestEstimator(t *testing.T) {
	fresh := estimator(t, 0.85, 20, 1.0)
	check(t, "no data -> prior", math.Abs(fresh.Estimate("x", VISA)-0.85) < 1e-12)
	for i := 0; i < 80; i++ {
		fresh.Record("x", VISA, i < 40)
	}
	check(t, "80 obs at 50% + prior 20 at 85% -> 57%", math.Abs(fresh.Estimate("x", VISA)-0.57) < 1e-9)
	check(t, "estimates are per network", math.Abs(fresh.Estimate("x", MASTERCARD)-0.85) < 1e-12)
	decaying := estimator(t, 0.85, 20, 0.95)
	for i := 0; i < 200; i++ {
		decaying.Record("y", VISA, true)
	}
	for i := 0; i < 40; i++ {
		decaying.Record("y", VISA, false)
	}
	flat := estimator(t, 0.85, 20, 1.0)
	for i := 0; i < 200; i++ {
		flat.Record("y", VISA, true)
	}
	for i := 0; i < 40; i++ {
		flat.Record("y", VISA, false)
	}
	check(t, "decay reacts to a recent drop faster than plain counts", decaying.Estimate("y", VISA) < 0.5 && flat.Estimate("y", VISA) > 0.8)
	_, err := NewAuthRateEstimator(1.0, 20, 0.99)
	check(t, "prior outside (0,1) refused", errors.Is(err, ErrInvalidArgument))
}

func TestRanking(t *testing.T) {
	clock := NewFakeClock(0)
	gw := NewScriptedGateway()
	r := testRouter(t, gw, clock, trained(t), NewRetryBudget(100, 10_000, 10_000), 3)
	ranked := rank(t, r, visa(t, 10_000))
	check(t, "amex-only processor not eligible for Visa", len(ranked) == 2)
	check(t, "higher auth rate beats lower fee at 100.00", ranked[0].Processor.ID == "acq_b")
	check(t, "margin = rate x (amount - fee)", math.Abs(ranked[0].ExpectedMarginMinor-ranked[0].AuthRate*(10_000-320)) < 1e-6)
	tie := testRouter(t, gw, clock, estimator(t, 0.85, 20, 1.0), NewRetryBudget(100, 10_000, 10_000), 3)
	check(t, "equal auth rates -> cheaper processor first", rank(t, tie, visa(t, 10_000))[0].Processor.ID == "acq_a")
	amex, _ := NewPaymentRequest("p_amex", OfMinor(100, EUR), AMEX, "fp")
	check(t, "no processor for AMEX in EUR -> NO_ROUTE", route(t, r, amex).Outcome == NO_ROUTE)
}

func TestCascadeRules(t *testing.T) {
	clock := NewFakeClock(0)
	gw := NewScriptedGateway()
	r := testRouter(t, gw, clock, trained(t), NewRetryBudget(100, 10_000, 10_000), 3)
	gw.Script("acq_b", Of("91"))
	cascaded := route(t, r, visa(t, 10_000))
	check(t, "91 on acq_b cascades to acq_a and approves", cascaded.Outcome == ROUTE_APPROVED && len(cascaded.Attempts) == 2 && cascaded.ApprovedBy() == "acq_a")
	// Hard declines lower acq_b's estimate, so the ranking may flip; always script the current leader.
	for _, code := range []string{"43", "54", "59", "14", "N7"} {
		gw.Script(leader(t, r), Of(code))
		res := route(t, r, visa(t, 10_000))
		check(t, "hard "+code+" stops after one attempt", res.Outcome == DECLINED && len(res.Attempts) == 1)
	}
	gw.Script(leader(t, r), Of("51"))
	nsf := route(t, r, visa(t, 10_000))
	check(t, "51 stops: same issuer would say the same", len(nsf.Attempts) == 1 && strings.Contains(nsf.StopReason, "retry later"))
	gw.Script(leader(t, r), Of("1A"))
	check(t, "1A stops for step-up", strings.Contains(route(t, r, visa(t, 10_000)).StopReason, "3-D Secure"))
	callsBefore := len(gw.Calls())
	gw.Script(leader(t, r), Timeout())
	to := route(t, r, visa(t, 10_000))
	check(t, "timeout -> PENDING_UNKNOWN, never cascaded", to.Outcome == PENDING_UNKNOWN && len(to.Attempts) == 1 && len(gw.Calls()) == callsBefore+1)
	gw.Script("acq_b", Of("05")).Script("acq_a", Of("05"))
	both := route(t, r, visa(t, 10_000))
	check(t, "two soft declines exhaust the eligible list", both.Outcome == DECLINED && len(both.Attempts) == 2 && strings.Contains(both.StopReason, "all eligible"))
}

func TestAttemptCap(t *testing.T) {
	clock := NewFakeClock(0)
	three := []ProcessorConfig{
		mustConfig(NewProcessorConfig("p1", []CardNetwork{VISA}, []Currency{USD}, 200, 0)),
		mustConfig(NewProcessorConfig("p2", []CardNetwork{VISA}, []Currency{USD}, 250, 0)),
		mustConfig(NewProcessorConfig("p3", []CardNetwork{VISA}, []Currency{USD}, 300, 0)),
	}
	g3 := NewScriptedGateway().Script("p1", Of("91")).Script("p2", Of("91"))
	policy, _ := NewRoutingPolicy(2, 0)
	capped := NewRouter(three, defaultHealth, estimator(t, 0.85, 20, 1.0), NewRetryBudget(100, 10_000, 10_000), g3, clock, policy, NewJavaRandom(1))
	capRes := route(t, capped, visa(t, 1_000))
	check(t, "attempt cap 2 stops before p3", len(capRes.Attempts) == 2 && strings.Contains(capRes.StopReason, "attempt cap") && !contains(g3.Calls(), "p3:"+capRes.PaymentID))
}

func TestHealthAndAuthRateAreSeparateSignals(t *testing.T) {
	clock := NewFakeClock(0)
	sep := estimator(t, 0.85, 20, 1.0)
	g4 := NewScriptedGateway()
	learner := testRouter(t, g4, clock, sep, NewRetryBudget(100, 10_000, 10_000), 3)
	g4.Script("acq_a", ConnectError())
	route(t, learner, visa(t, 1_000))
	check(t, "connect error does not touch auth-rate counts", sep.Observations("acq_a", VISA) == 0)
	g4.Script("acq_a", Of("05"))
	route(t, learner, visa(t, 1_000))
	check(t, "issuer decline is counted against acq_a", sep.Observations("acq_a", VISA) == 1 && sep.Estimate("acq_a", VISA) < 0.85)
}

func TestCircuitBreaker(t *testing.T) {
	hc := NewFakeClock(0)
	h := NewProcessorHealth(hc, 10, 5, 30_000)
	for i := 0; i < 4; i++ {
		h.RecordFailure()
	}
	check(t, "4 failures keep it CLOSED", h.State() == CLOSED)
	h.RecordFailure()
	check(t, "5th failure in the window trips OPEN", h.State() == OPEN && !h.Allows())
	hc.Advance(29_999)
	check(t, "still OPEN before cool-down", h.State() == OPEN)
	hc.Advance(1)
	check(t, "HALF_OPEN after cool-down", h.State() == HALF_OPEN && h.Allows())
	h.RecordFailure()
	check(t, "failed probe re-opens", h.State() == OPEN)
	hc.Advance(30_000)
	h.RecordSuccess()
	check(t, "successful probe closes", h.State() == CLOSED)
	spread := NewProcessorHealth(hc, 10, 5, 30_000)
	for i := 0; i < 20; i++ {
		if i%3 == 0 {
			spread.RecordFailure()
		} else {
			spread.RecordSuccess()
		}
	}
	check(t, "failures spread thinly never reach 5-in-10", spread.State() == CLOSED)

	g5 := NewScriptedGateway()
	cb := testRouter(t, g5, hc, trained(t), NewRetryBudget(100, 10_000, 10_000), 3)
	for i := 0; i < 5; i++ {
		g5.Script("acq_b", ConnectError())
		route(t, cb, visa(t, 1_000))
	}
	check(t, "open circuit removes acq_b from ranking", !contains(ids(rank(t, cb, visa(t, 1_000))), "acq_b"))
	before := len(g5.Calls())
	rerouted := route(t, cb, visa(t, 1_000))
	check(t, "traffic goes straight to acq_a while open", rerouted.ApprovedBy() == "acq_a" && len(g5.Calls()) == before+1)
}

func TestRetryBudget(t *testing.T) {
	clock := NewFakeClock(0)
	budget := NewRetryBudget(100, 10_000, 2_000)
	check(t, "initial 2 tokens pay for 2 retries", budget.TryAcquireRetry() && budget.TryAcquireRetry() && !budget.TryAcquireRetry())
	for i := 0; i < 10; i++ {
		budget.RecordFirstAttempt()
	}
	check(t, "10 first attempts earn exactly 1 retry at a 10% ratio", budget.TryAcquireRetry() && !budget.TryAcquireRetry())
	capB := NewRetryBudget(100, 1_500, 0)
	for i := 0; i < 100; i++ {
		capB.RecordFirstAttempt()
	}
	check(t, "budget is capped", capB.BalanceMilli() == 1_500)
	storm := NewScriptedGateway()
	sr := testRouter(t, storm, clock, trained(t), NewRetryBudget(100, 10_000, 3_000), 3)
	calls := 0
	for i := 0; i < 20; i++ {
		storm.Script("acq_a", Of("91")).Script("acq_b", Of("91"))
		calls += len(route(t, sr, visa(t, 1_000)).Attempts)
	}
	check(t, "storm of 20 soft declines: 20 first attempts + 5 budgeted cascades", calls == 25)
}

func TestValidation(t *testing.T) {
	_, err := NewPaymentRequest("pay_zero", OfMinor(0, USD), VISA, "fp")
	check(t, "zero amount refused", errors.Is(err, ErrInvalidArgument))
	_, err = NewRoutingPolicy(0, 0)
	check(t, "maxAttempts 0 refused", errors.Is(err, ErrInvalidArgument))
	_, err = NewProcessorConfig("neg", []CardNetwork{VISA}, []Currency{USD}, -1, 0)
	check(t, "negative fee refused", errors.Is(err, ErrInvalidArgument))
}
