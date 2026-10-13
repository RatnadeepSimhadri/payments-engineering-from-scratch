// Tests for phases/13-risk-fraud-and-compliance/02-rules-engine-and-velocity-checks. Run: go test ./...
package main

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

const T0 int64 = 1_700_000_040_000 // aligned to a minute boundary, so bucket edges are predictable

func rule(name, field string, op Operator, value any, action Action, priority, score int) Rule {
	return MustRule(name, field, op, value, action, priority, score, "test rule "+name)
}

func apply(t *testing.T, op Operator, a, b any) bool {
	t.Helper()
	got, err := op.Apply(a, b)
	if err != nil {
		t.Fatalf("%s.Apply(%v, %v): %v", op, a, b, err)
	}
	return got
}

func decide(t *testing.T, e *RulesEngine, id string, f *Facts) Decision {
	t.Helper()
	d, err := e.Decide(id, f, T0)
	if err != nil {
		t.Fatalf("Decide(%s): %v", id, err)
	}
	return d
}

func screen(t *testing.T, s *Screener, tx Transaction) Decision {
	t.Helper()
	d, err := s.Screen(tx)
	if err != nil {
		t.Fatalf("Screen(%s): %v", tx.ID, err)
	}
	return d
}

func TestOperators(t *testing.T) {
	if !apply(t, EQ, "US", "US") || !apply(t, EQ, usd("1.00"), OfMinor(100, USD)) || !apply(t, NE, "US", "NG") {
		t.Error("EQ / NE")
	}
	if !apply(t, GT, int64(6), int64(5)) || apply(t, GT, int64(5), int64(5)) || !apply(t, GTE, int64(5), int64(5)) {
		t.Error("GT / GTE on int64")
	}
	if !apply(t, LT, int64(0), int64(1)) || apply(t, LT, int64(1), int64(1)) || !apply(t, LTE, int64(1), int64(1)) {
		t.Error("LT / LTE on int64")
	}
	if !apply(t, GT, usd("2000.01"), usd("2000.00")) || !apply(t, LT, usd("0.99"), usd("1.00")) {
		t.Error("ordering on Money")
	}
	if !apply(t, IN, "NG", SetOf("NG", "RU")) || !apply(t, NotIn, "US", SetOf("NG", "RU")) {
		t.Error("IN / NOT_IN")
	}
}

func TestOperatorTypeMismatchIsConfigurationError(t *testing.T) {
	for _, c := range []struct{ a, b any }{{"b", "a"}, {int64(5), usd("5.00")}} {
		if _, err := GT.Apply(c.a, c.b); !errors.Is(err, ErrConfiguration) {
			t.Errorf("GT.Apply(%v, %v) = %v, want ErrConfiguration", c.a, c.b, err)
		}
	}
	if _, err := IN.Apply("US", "US"); !errors.Is(err, ErrConfiguration) {
		t.Errorf("IN without a set: %v", err)
	}
	if _, err := GT.Apply(usd("1.00"), MustParse("1.00", EUR)); !errors.Is(err, ErrCurrencyMismatch) {
		t.Errorf("GT across currencies: %v", err)
	}
}

func TestSlidingWindowCountsAndExpires(t *testing.T) {
	c := mustCounter(10*Minute, 10)
	if c.BucketMillis != Minute {
		t.Fatalf("bucket width = %d, want %d", c.BucketMillis, Minute)
	}
	if c.Count("card:none", T0) != 0 || c.Sum("card:none", T0).MinorUnits != 0 {
		t.Error("unknown key should count zero")
	}
	for i := int64(0); i < 6; i++ {
		if err := c.Record("card:a", usd("1.00"), T0+i*10*Second); err != nil {
			t.Fatal(err)
		}
	}
	if got := c.Count("card:a", T0+Minute); got != 6 {
		t.Errorf("count inside window = %d, want 6", got)
	}
	if got := c.Sum("card:a", T0+Minute); got != usd("6.00") {
		t.Errorf("sum inside window = %v, want 6.00 USD", got)
	}
	if got := c.Count("card:a", T0+10*Minute-1); got != 6 {
		t.Errorf("count one ms before expiry = %d, want 6", got)
	}
	if got := c.Count("card:a", T0+10*Minute); got != 0 {
		t.Errorf("count at T0 + window = %d, want 0 (the bucket of T0 has rolled out)", got)
	}
	if c.Count("card:b", T0+Minute) != 0 {
		t.Error("keys must be independent")
	}
	if evicted := c.EvictIdle(T0 + Hour); evicted != 1 || c.TrackedKeys() != 0 {
		t.Errorf("EvictIdle = %d, tracked = %d", evicted, c.TrackedKeys())
	}
}

func TestSlidingWindowEffectiveWidth(t *testing.T) {
	d := mustCounter(10*Minute, 10)
	_ = d.Record("k", usd("1.00"), T0+59*Second)
	if d.Count("k", T0+9*Minute+59*Second) != 1 {
		t.Error("event aged 9m should still be counted")
	}
	if d.Count("k", T0+10*Minute) != 0 {
		t.Error("event aged 9m01s expires with its bucket, which started 10m ago")
	}
}

func TestSlidingWindowReusedSlotIsReset(t *testing.T) {
	e := mustCounter(10*Minute, 10)
	_ = e.Record("k", usd("5.00"), T0)
	_ = e.Record("k", usd("7.00"), T0+10*Minute) // same slot index, next lap
	if e.Count("k", T0+10*Minute) != 1 || e.Sum("k", T0+10*Minute) != usd("7.00") {
		t.Errorf("reused slot should hold only the new lap: count=%d sum=%v", e.Count("k", T0+10*Minute), e.Sum("k", T0+10*Minute))
	}
}

func TestSlidingWindowConfiguration(t *testing.T) {
	c := mustCounter(10*Minute, 10)
	if err := c.Record("k", MustParse("1.00", EUR), T0); !errors.Is(err, ErrCurrencyMismatch) {
		t.Errorf("one counter is one currency: %v", err)
	}
	if _, err := NewSlidingWindowCounter(10*Minute+1, 10, USD); !errors.Is(err, ErrConfiguration) {
		t.Errorf("window must divide into buckets: %v", err)
	}
}

func TestVelocityServiceEnrich(t *testing.T) {
	v := Velocity()
	f1, err := v.Enrich(tx("t1", "acct", "card", "411111", "US", 10, "700.00"), T0)
	if err != nil {
		t.Fatal(err)
	}
	if c, _ := f1.Get("country"); c != "US" {
		t.Error("enrich must keep transaction facts")
	}
	if n, _ := f1.Get("attempts_per_card_10m"); n != int64(1) {
		t.Errorf("the attempt counts itself: %v", n)
	}
	if s, _ := f1.Get("amount_per_account_1d"); s != usd("700.00") {
		t.Errorf("sum fact = %v", s)
	}
	f2, _ := v.Enrich(tx("t2", "acct", "card", "411111", "US", 10, "700.00"), T0+5*Minute)
	if n, _ := f2.Get("attempts_per_card_10m"); n != int64(2) {
		t.Errorf("second attempt should see 2: %v", n)
	}
	f3, _ := v.Enrich(tx("t3", "acct", "card2", "411111", "US", 10, "1.00"), T0+5*Minute)
	if n, _ := f3.Get("attempts_per_card_10m"); n != int64(1) {
		t.Errorf("card dimension is per card: %v", n)
	}
	if s, _ := f3.Get("amount_per_account_1d"); s != usd("1401.00") {
		t.Errorf("account dimension is per account: %v", s)
	}
	if err := v.Define("attempts_per_card_10m", DimensionCard, MetricCount, mustCounter(Minute, 1)); !errors.Is(err, ErrConfiguration) {
		t.Errorf("duplicate fact names: %v", err)
	}
}

func TestEngineFirstMatch(t *testing.T) {
	engine, err := NewRulesEngine([]Rule{
		rule("review-country", "country", IN, SetOf("NG"), Review, 30, 30),
		rule("block-bin", "bin", EQ, "999999", Decline, 5, 100),
		rule("large", "amount", GTE, usd("5000.00"), Review, 50, 30),
	}, FirstMatch, 50, 80)
	if err != nil {
		t.Fatal(err)
	}
	if engine.Rules[0].Name != "block-bin" || engine.Rules[2].Name != "large" {
		t.Error("rules must be sorted by priority regardless of declaration order")
	}
	clean := decide(t, engine, "tx", tx("tx", "a", "c", "411111", "US", 10, "10.00").Facts())
	if clean.Action != Allow || clean.DecidingRule != "" || len(clean.Fired()) != 0 || len(clean.Traces) != 3 {
		t.Errorf("clean transaction: %+v", clean)
	}
	both := decide(t, engine, "tx", tx("tx", "a", "c", "999999", "NG", 10, "10.00").Facts())
	if both.Action != Decline || both.DecidingRule != "block-bin" {
		t.Errorf("first match by priority should decide: %+v", both)
	}
	if !reflect.DeepEqual(both.FiredRuleNames(), []string{"block-bin", "review-country"}) {
		t.Errorf("all fired rules reported: %v", both.FiredRuleNames())
	}
	later := decide(t, engine, "tx", tx("tx", "a", "c", "411111", "NG", 10, "6000.00").Facts())
	if later.DecidingRule != "review-country" || later.Action != Review {
		t.Errorf("priority, not position, picks the decider: %+v", later)
	}
	missing := tx("tx", "a", "c", "411111", "US", 10, "10.00").Facts()
	missing.Remove("country")
	absent := decide(t, engine, "tx", missing)
	if absent.Action != Allow || absent.Traces[1].Observed != nil {
		t.Errorf("an absent fact never fires: %+v", absent)
	}
}

func TestEngineScore(t *testing.T) {
	engine, _ := NewRulesEngine([]Rule{
		rule("country", "country", IN, SetOf("NG"), Review, 30, 30),
		rule("fresh", "card_age_days", LT, int64(1), Review, 20, 25),
		rule("large", "amount", GTE, usd("5000.00"), Review, 50, 30),
	}, ScoreStrategy, 50, 80)
	if d := decide(t, engine, "t", tx("t", "a", "c", "4", "NG", 10, "10.00").Facts()); d.Action != Allow {
		t.Errorf("one signal below reviewAt: %v", d.Action)
	}
	if d := decide(t, engine, "t", tx("t", "a", "c", "4", "NG", 0, "10.00").Facts()); d.Score != 55 || d.Action != Review {
		t.Errorf("two signals: score=%d action=%v", d.Score, d.Action)
	}
	if d := decide(t, engine, "t", tx("t", "a", "c", "4", "NG", 0, "6000.00").Facts()); d.Score != 85 || d.Action != Decline {
		t.Errorf("three REVIEW rules should add up to DECLINE: score=%d action=%v", d.Score, d.Action)
	}
	edge, _ := NewRulesEngine([]Rule{rule("a", "country", EQ, "NG", Review, 1, 50)}, ScoreStrategy, 50, 80)
	if d := decide(t, edge, "t", tx("t", "a", "c", "4", "NG", 10, "1.00").Facts()); d.Action != Review {
		t.Errorf("score at exactly reviewAt is REVIEW: %v", d.Action)
	}
}

func TestExplanation(t *testing.T) {
	engine, _ := NewRulesEngine(CardTestingRules(), FirstMatch, 50, 80)
	text := decide(t, engine, "tx-9", tx("tx-9", "a", "c", "999999", "NG", 10, "10.00").Facts()).Explain()
	for _, want := range []string{
		"decision for tx-9: DECLINE",
		`by "bin-blocklist"`,
		"bin=999999 IN [888888, 999999]",
		"issuer range is on the internal blocklist",
		"[pass] p50 large-ticket: amount=10.00 USD GTE 5000.00 USD",
		"attempts_per_card_10m=<absent>",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("explanation missing %q:\n%s", want, text)
		}
	}
}

func TestCardTestingScenario(t *testing.T) {
	clock := NewManualClock(T0)
	engine, _ := NewRulesEngine(CardTestingRules(), FirstMatch, 50, 80)
	s := &Screener{engine, Velocity(), clock}

	if d := screen(t, s, tx("n1", "acct_alice", "tok_alice", "411111", "US", 400, "42.50")); d.Action != Allow {
		t.Errorf("normal traffic: %v", d.Action)
	}
	var seen []Action
	for i := 0; i < 6; i++ {
		clock.Advance(20 * Second)
		seen = append(seen, screen(t, s, tx("m", "acct_mallory", "tok_mallory", "411111", "US", 0, "1.00")).Action)
	}
	if seen[0] != Review || seen[4] != Review {
		t.Errorf("first five micro-auths on a fresh card go to REVIEW: %v", seen)
	}
	if seen[5] != Decline {
		t.Errorf("the sixth attempt inside ten minutes is DECLINED: %v", seen)
	}
	clock.Advance(20 * Second)
	if d := screen(t, s, tx("a2", "acct_alice", "tok_alice", "411111", "US", 400, "19.99")); d.Action != Allow {
		t.Errorf("a burst on one card must not touch another: %v", d.Action)
	}
	clock.Advance(10 * Minute)
	cooled := screen(t, s, tx("m7", "acct_mallory", "tok_mallory", "411111", "US", 0, "1.00"))
	if cooled.Action != Review || strings.Contains(strings.Join(cooled.FiredRuleNames(), ","), "card-testing-burst") {
		t.Errorf("after the window rolls the velocity rule must release: %+v", cooled.FiredRuleNames())
	}
	for i := 0; i < 3; i++ {
		clock.Advance(Minute)
		screen(t, s, tx("d", "acct_dave", "tok_dave", "411111", "US", 90, "700.00"))
	}
	clock.Advance(Minute)
	spend := screen(t, s, tx("d4", "acct_dave", "tok_dave", "411111", "US", 90, "1.00"))
	if spend.Action != Review || spend.DecidingRule != "daily-spend" {
		t.Errorf("sum velocity should trip daily-spend: %+v", spend)
	}
}

// A shadow rule is evaluated and traced but never acts.
func TestShadowMode(t *testing.T) {
	live := rule("live-review", "country", EQ, "NG", Review, 20, 30)
	candidate := rule("candidate-decline", "amount", GTE, usd("100.00"), Decline, 10, 60).AsShadow()
	facts := tx("t", "a", "c", "4", "NG", 1, "250.00").Facts()

	fmEngine, _ := NewRulesEngine([]Rule{live, candidate}, FirstMatch, 50, 80)
	fm := must(fmEngine.Decide("t", facts, T0))
	if fm.Action != Review || fm.DecidingRule != "live-review" {
		t.Errorf("shadow rule with higher priority must not decide: %s by %q", fm.Action, fm.DecidingRule)
	}
	if !reflect.DeepEqual(fm.FiredRuleNames(), []string{"live-review"}) {
		t.Errorf("Fired must exclude shadow rules: %v", fm.FiredRuleNames())
	}
	if sf := fm.ShadowFired(); len(sf) != 1 || sf[0].Rule != "candidate-decline" {
		t.Errorf("ShadowFired must report the candidate: %v", sf)
	}
	if !strings.Contains(fm.Explain(), "[shadow] p10 candidate-decline") {
		t.Errorf("explanation must mark the shadow rule:\n%s", fm.Explain())
	}

	scEngine, _ := NewRulesEngine([]Rule{live, candidate}, ScoreStrategy, 50, 80)
	sc := must(scEngine.Decide("t", facts, T0))
	if sc.Score != 30 || sc.Action != Allow {
		t.Errorf("shadow score must not accumulate: score=%d action=%s", sc.Score, sc.Action)
	}

	aloneEngine, _ := NewRulesEngine([]Rule{candidate}, FirstMatch, 50, 80)
	alone := must(aloneEngine.Decide("t", facts, T0))
	if alone.Action != Allow || alone.DecidingRule != "" {
		t.Errorf("only-shadow rule set must default to ALLOW: %s by %q", alone.Action, alone.DecidingRule)
	}
	if !strings.HasSuffix(candidate.Describe(), "[shadow]") || candidate.Score != 60 {
		t.Errorf("AsShadow must keep fields and mark Describe: %s", candidate.Describe())
	}
}

func TestConfigurationErrors(t *testing.T) {
	if _, err := NewRulesEngine([]Rule{
		rule("x", "country", EQ, "US", Allow, 1, 0),
		rule("x", "country", EQ, "NG", Decline, 2, 0),
	}, FirstMatch, 50, 80); !errors.Is(err, ErrConfiguration) {
		t.Errorf("duplicate rule names: %v", err)
	}
	if _, err := NewRulesEngine(nil, ScoreStrategy, 80, 50); !errors.Is(err, ErrConfiguration) {
		t.Errorf("thresholds must be ordered: %v", err)
	}
	if _, err := NewRule("x", "country", EQ, "US", Allow, 1, 0, ""); !errors.Is(err, ErrConfiguration) {
		t.Errorf("rule needs an explanation: %v", err)
	}
	engine, _ := NewRulesEngine([]Rule{rule("bad", "country", GT, int64(5), Decline, 1, 0)}, FirstMatch, 50, 80)
	if _, err := engine.Decide("t", tx("t", "a", "c", "4", "US", 1, "1.00").Facts(), T0); !errors.Is(err, ErrConfiguration) {
		t.Errorf("type mismatch should fail loudly at evaluation: %v", err)
	}
}

func TestMoneyBasics(t *testing.T) {
	if usd("19.99").MinorUnits != 1999 || usd("19.99").String() != "19.99 USD" {
		t.Error("parse / format round trip")
	}
	if _, err := Parse("1.005", USD); err == nil {
		t.Error("excess precision must be rejected")
	}
	if _, err := usd("1.00").Plus(MustParse("1.00", EUR)); !errors.Is(err, ErrCurrencyMismatch) {
		t.Errorf("plus across currencies: %v", err)
	}
	if _, err := OfMinor(1<<62, USD).Plus(OfMinor(1<<62, USD)); !errors.Is(err, ErrOverflow) {
		t.Errorf("overflow is an error, not a wrap: %v", err)
	}
}
