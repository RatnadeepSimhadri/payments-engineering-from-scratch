// Tests for phases/12-payment-systems-design/09-limits-and-velocity-controls. Run: go test ./...
package main

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
)

const ACME = "merchant_acme"

func check(t *testing.T, name string, condition bool) {
	t.Helper()
	if !condition {
		t.Errorf("FAIL: %s", name)
	}
}

func checkErr(t *testing.T, name string, err error, want error) {
	t.Helper()
	if err == nil {
		t.Errorf("FAIL: %s (no error)", name)
	} else if !errors.Is(err, want) {
		t.Errorf("FAIL: %s (wrong error %v)", name, err)
	}
}

func usd(t *testing.T, id, merchant, card, amount string) Attempt {
	t.Helper()
	a, err := NewAttempt(id, merchant, card, mustParse(amount, USD))
	if err != nil {
		t.Fatalf("attempt %s: %v", id, err)
	}
	return a
}

func policyOf(t *testing.T, limits ...Limit) *LimitPolicy {
	t.Helper()
	p := NewLimitPolicy()
	for _, l := range limits {
		if err := p.AddDefault(l); err != nil {
			t.Fatalf("add default %s: %v", l.ID, err)
		}
	}
	return p
}

func TestSlidingWindowHalfOpenInterval(t *testing.T) {
	w := must(NewSlidingWindow(HOUR))
	mustOK(w.Add(0, 100))
	mustOK(w.Add(10*SECOND, 200))
	check(t, "window counts both entries inside the hour", w.Count(30*MINUTE) == 2)
	check(t, "window sums amounts inside the hour", w.SumMinor(30*MINUTE) == 300)
	check(t, "entry exactly W old has left the window", w.Count(HOUR) == 1)
	check(t, "sum tracks eviction", w.SumMinor(HOUR) == 200)
	expiry, ok := w.NextExpiryMillis(HOUR)
	check(t, "next expiry is oldest + W", ok && expiry == HOUR+10*SECOND)
	_, ok = w.NextExpiryMillis(2 * HOUR)
	check(t, "window empties after everything ages out", w.Count(2*HOUR) == 0 && !ok)
	back := must(NewSlidingWindow(HOUR))
	mustOK(back.Add(10, 1))
	checkErr(t, "window rejects a clock that goes backwards", back.Add(5, 1), ErrInvalidArgument)
}

func TestCountLimitFivePerCardPerHour(t *testing.T) {
	clock := NewFakeClock(0)
	perCard := must(CountLimit("card-attempts-1h", CARD, HOUR, 5, HARD))
	engine := NewLimitEngine(policyOf(t, perCard), clock)
	for i := 1; i <= 5; i++ {
		check(t, fmt.Sprintf("attempt %d of 5 allowed", i), engine.Check(usd(t, fmt.Sprintf("a%d", i), ACME, "fp_1", "1.00")).Outcome == ALLOW)
		clock.Advance(MINUTE)
	}
	sixth := engine.Check(usd(t, "a6", ACME, "fp_1", "1.00"))
	check(t, "sixth attempt denied", sixth.Outcome == DENY)
	check(t, "denied attempt is not recorded", engine.WindowCount(perCard, usd(t, "x", ACME, "fp_1", "1.00")) == 5)
	check(t, "denial names the limit", len(sixth.Reasons) == 1 && sixth.Reasons[0].LimitID == "card-attempts-1h")
	check(t, "denial explains observed -> wouldBe vs threshold",
		strings.Contains(sixth.Explanations()[0], "5 attempts -> 6 attempts exceeds 5 attempts in 3600s"))
	check(t, "retry-after is until the oldest attempt leaves the window (55 min)", sixth.RetryAfterSeconds() == 55*60)
	check(t, "a different card is a different window", engine.Check(usd(t, "b1", ACME, "fp_2", "1.00")).Outcome == ALLOW)
	clock.Advance(55 * MINUTE) // now = 1h after attempt 1
	check(t, "one slot frees exactly when the oldest entry is W old", engine.Check(usd(t, "a7", ACME, "fp_1", "1.00")).Outcome == ALLOW)
	check(t, "and only one slot", engine.Check(usd(t, "a8", ACME, "fp_1", "1.00")).Outcome == DENY)
}

func TestSoftAmountLimitAllowedButFlagged(t *testing.T) {
	clock := NewFakeClock(0)
	soft := must(AmountLimit("card-amount-24h", CARD, DAY, mustParse("500.00", USD), SOFT))
	engine := NewLimitEngine(policyOf(t, soft), clock)
	check(t, "480 of 500 allowed", engine.Check(usd(t, "s1", ACME, "fp_1", "480.00")).Outcome == ALLOW)
	check(t, "exactly reaching the limit is allowed (<=)", engine.Check(usd(t, "s2", ACME, "fp_1", "20.00")).Outcome == ALLOW)
	over := engine.Check(usd(t, "s3", ACME, "fp_1", "0.01"))
	check(t, "soft breach is REVIEW, not DENY", over.Outcome == REVIEW)
	check(t, "soft breach still Allowed()", over.Allowed())
	check(t, "soft breach is recorded (it was allowed)", engine.WindowCount(soft, usd(t, "x", ACME, "fp_1", "1.00")) == 3)
	check(t, "soft breach explanation uses Money strings", strings.Contains(over.Explanations()[0], "500.00 USD -> 500.01 USD exceeds 500.00 USD"))
	check(t, "soft breach contributes no Retry-After", over.RetryAfterSeconds() == 0)
	eur := must(NewAttempt("e1", ACME, "fp_1", mustParse("9000.00", EUR)))
	check(t, "EUR attempt is not judged by a USD limit", engine.Check(eur).Outcome == ALLOW)
}

func TestExposureReleasedOnCaptureOrVoid(t *testing.T) {
	clock := NewFakeClock(0)
	exposure := must(ExposureLimit("merchant-exposure", MERCHANT, mustParse("2000.00", USD), HARD))
	engine := NewLimitEngine(policyOf(t, exposure), clock)
	check(t, "first 1500 auth allowed", engine.Check(usd(t, "x1", ACME, "fp_1", "1500.00")).Outcome == ALLOW)
	check(t, "second 600 auth denied: 2100 open", engine.Check(usd(t, "x2", ACME, "fp_2", "600.00")).Outcome == DENY)
	clock.Advance(30 * DAY)
	check(t, "exposure does not expire with time", engine.Check(usd(t, "x3", ACME, "fp_2", "600.00")).Outcome == DENY)
	engine.Release("x1")
	check(t, "release lowers exposure to zero", engine.OpenExposure(exposure, usd(t, "q", ACME, "fp_1", "1.00")).MinorUnits == 0)
	check(t, "after capture the 600 auth is allowed", engine.Check(usd(t, "x4", ACME, "fp_2", "600.00")).Outcome == ALLOW)
	engine.Release("x1")
	engine.Release("never-seen")
	check(t, "release is idempotent and tolerates unknown ids", engine.OpenExposure(exposure, usd(t, "q", ACME, "fp_1", "1.00")).MinorUnits == 60000)
	check(t, "other merchant has its own exposure", engine.Check(usd(t, "y1", "merchant_other", "fp_9", "1999.99")).Outcome == ALLOW)
}

func TestHierarchyOverridesReplaceDefaultsByID(t *testing.T) {
	policy := policyOf(t,
		must(CountLimit("card-attempts-1h", CARD, HOUR, 5, HARD)),
		must(AmountLimit("merchant-amount-24h", MERCHANT, DAY, mustParse("10000.00", USD), HARD)))
	mustOK(policy.Override("merchant_bigbox", must(AmountLimit("merchant-amount-24h", MERCHANT, DAY, mustParse("250000.00", USD), HARD))))
	acme, bigbox := policy.LimitsFor(ACME), policy.LimitsFor("merchant_bigbox")
	check(t, "acme sees defaults", acme[1].MaxAmount == mustParse("10000.00", USD))
	check(t, "bigbox sees its override", bigbox[1].MaxAmount == mustParse("250000.00", USD))
	check(t, "override keeps the card limit untouched", bigbox[0] == acme[0])
	check(t, "order is stable: defaults' declaration order", len(acme) == 2 && acme[0].ID == "card-attempts-1h")
	checkErr(t, "cannot override a limit that has no default",
		policy.Override(ACME, must(CountLimit("made-up", CARD, HOUR, 1, HARD))), ErrInvalidArgument)
	checkErr(t, "duplicate default id rejected",
		policy.AddDefault(must(CountLimit("card-attempts-1h", CARD, HOUR, 1, HARD))), ErrInvalidArgument)
	clock := NewFakeClock(0)
	engine := NewLimitEngine(policy, clock)
	check(t, "bigbox passes 15k", engine.Check(usd(t, "h1", "merchant_bigbox", "fp_1", "15000.00")).Outcome == ALLOW)
	check(t, "acme is denied 15k", engine.Check(usd(t, "h2", ACME, "fp_1", "15000.00")).Outcome == DENY)
}

func TestEveryBreachReportedHardWinsOverSoft(t *testing.T) {
	clock := NewFakeClock(0)
	engine := NewLimitEngine(standardPolicy(), clock)
	d := engine.Check(usd(t, "m1", ACME, "fp_1", "15000.00"))
	check(t, "outcome is DENY when any HARD limit breaks", d.Outcome == DENY)
	check(t, "all three breached limits are listed", len(d.Reasons) == 3)
	anySoft, anyHard := false, false
	for _, r := range d.Reasons {
		anySoft = anySoft || r.Severity == SOFT
		anyHard = anyHard || r.Severity == HARD
	}
	check(t, "soft and hard reasons both present", anySoft && anyHard)
	check(t, "denied attempt recorded nowhere", engine.WindowCount(standardPolicy().LimitsFor(ACME)[0], usd(t, "x", ACME, "fp_1", "1.00")) == 0)
}

func TestConstructionGuards(t *testing.T) {
	cases := []struct {
		name string
		err  func() error
		want error
	}{
		{"COUNT needs a positive max", func() error { _, err := CountLimit("c", CARD, HOUR, 0, HARD); return err }, ErrInvalidArgument},
		{"AMOUNT needs a positive money threshold", func() error { _, err := AmountLimit("a", CARD, HOUR, Zero(USD), HARD); return err }, ErrInvalidArgument},
		{"attempt amount must be positive", func() error { _, err := NewAttempt("z", ACME, "fp", mustParse("0.00", USD)); return err }, ErrInvalidArgument},
		{"Money rejects currency mix", func() error { _, err := mustParse("1.00", USD).Plus(mustParse("1.00", EUR)); return err }, ErrCurrencyMismatch},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { checkErr(t, c.name, c.err(), c.want) })
	}
}

// Check-and-record is atomic, so a 5-per-hour limit admits exactly 5 of 32 racing goroutines.
func TestConcurrentCheckAndRecordIsAtomic(t *testing.T) {
	clock := NewFakeClock(0)
	engine := NewLimitEngine(policyOf(t, must(CountLimit("card-attempts-1h", CARD, HOUR, 5, HARD))), clock)
	const goroutines = 32
	start := make(chan struct{})
	var wg sync.WaitGroup
	var mu sync.Mutex
	allowed := 0
	for n := 0; n < goroutines; n++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			<-start
			if engine.Check(usd(t, fmt.Sprintf("r%d", n), ACME, "fp_race", "1.00")).Allowed() {
				mu.Lock()
				allowed++
				mu.Unlock()
			}
		}(n)
	}
	close(start)
	wg.Wait()
	check(t, "exactly 5 of 32 concurrent attempts are admitted", allowed == 5)
}
