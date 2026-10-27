// Tests for phases/12-payment-systems-design/12-currency-conversion-at-checkout. Run: go test ./...
package main

import (
	"errors"
	"strings"
	"testing"
)

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

// checkPanics covers the one place the Go port panics where Java throws: a zero denominator is a
// programming error (see Of), so the test recovers it instead of receiving an error value.
func checkPanics(t *testing.T, name string, want error, action func()) {
	t.Helper()
	defer func() {
		t.Helper()
		r := recover()
		if r == nil {
			t.Errorf("FAIL: %s (no panic)", name)
			return
		}
		if err, ok := r.(error); !ok || !errors.Is(err, want) {
			t.Errorf("FAIL: %s (wrong panic %v)", name, r)
		}
	}()
	action()
}

func usd(s string) Money { return mustParse(s, USD) }
func eur(s string) Money { return mustParse(s, EUR) }
func gbp(s string) Money { return mustParse(s, GBP) }

type rig struct {
	clock  *FakeClock
	market *FakeRateSource
	desk   *FxDesk
}

func newRig() *rig {
	clock, market := NewFakeClock(T0), NewFakeRateSource()
	mustOK(market.Set(EUR, USD, "1.0850"))
	return &rig{clock, market, must(NewFxDesk(clock, market, 200, FifteenMinutes))}
}

func convert(t *testing.T, amount Money, rate FxRate, mode Rounding) Money {
	t.Helper()
	m, err := Convert(amount, rate, mode)
	if err != nil {
		t.Fatalf("convert %v: %v", amount, err)
	}
	return m
}

func quote(t *testing.T, d *FxDesk, settlement, presentment Currency) RateQuote {
	t.Helper()
	q, err := d.Quote(settlement, presentment)
	if err != nil {
		t.Fatalf("quote: %v", err)
	}
	return q
}

func confirm(t *testing.T, d *FxDesk, id string, price Money) LockedCharge {
	t.Helper()
	c, err := d.Confirm(id, price)
	if err != nil {
		t.Fatalf("confirm %s: %v", id, err)
	}
	return c
}

func settle(t *testing.T, c LockedCharge, mid Rational) SettlementResult {
	t.Helper()
	r, err := Settle(c, mid)
	if err != nil {
		t.Fatalf("settle: %v", err)
	}
	return r
}

func TestRationalsAreExactAndNormalized(t *testing.T) {
	check(t, "parse 1.0850 = 217/200", rat("1.0850").Equal(Of(217, 200)))
	check(t, "sign lives in the numerator", Of(2, -4).Equal(Of(-1, 2)) && Of(2, -4).String() == "-1/2")
	check(t, "inverse is exact", rat("1.0850").Inverse().Equal(Of(200, 217)))
	check(t, "Compare is exact", Of(1, 3).Compare(rat("0.3333")) > 0)
	check(t, "bps", Bps(350).Equal(Of(7, 200)))
	checkPanics(t, "zero denominator rejected", ErrZeroDenominator, func() { Of(1, 0) })
}

func TestConversionRoundsOnceOnTheAmount(t *testing.T) {
	usdEur := must(must(NewFxRate(USD, EUR, rat("1.0850").Inverse())).AdjustedBy(200))
	check(t, "customer rate is mid * 1.02 = 204/217", usdEur.Rate.Equal(Of(204, 217)))
	check(t, "120.00 USD -> 112.82 EUR rounding up", convert(t, usd("120.00"), usdEur, Ceiling) == eur("112.82"))
	check(t, "120.00 USD -> 112.81 EUR half-even", convert(t, usd("120.00"), usdEur, HalfEven) == eur("112.81"))
	usdJpy := must(NewFxRate(USD, JPY, rat("152.37")))
	check(t, "USD -> JPY shifts exponents 2 -> 0", convert(t, usd("120.00"), usdJpy, HalfEven) == OfMinor(18284, JPY))
	check(t, "JPY -> USD round trip", convert(t, OfMinor(18284, JPY), usdJpy.Inverse(), HalfEven) == usd("120.00"))
	exact := convert(t, usd("50000.00"), usdEur, HalfEven)
	roundedRate := must(ConvertWithRoundedRate(usd("50000.00"), usdEur, 4, HalfEven))
	check(t, "exact conversion 47004.61", exact == eur("47004.61"))
	check(t, "rounding the rate first costs 0.39 EUR", must(roundedRate.Minus(exact)) == eur("0.39"))
	_, err := Convert(eur("1.00"), usdEur, HalfEven)
	checkErr(t, "convert rejects the wrong source currency", err, ErrCurrencyMismatch)
	_, err = NewFxRate(USD, EUR, Of(0, 1))
	checkErr(t, "non-positive rate rejected", err, ErrInvalidArgument)
	_, err = NewFxRate(USD, USD, rat("1.01"))
	checkErr(t, "same-currency rate must be 1", err, ErrInvalidArgument)
	down := must(must(NewFxRate(EUR, USD, ONE)).AdjustedBy(-200))
	check(t, "negative bps scales the rate down", down.Rate.Equal(Of(49, 50)))
}

func TestRoundingModes(t *testing.T) {
	cases := []struct {
		name string
		v    Rational
		mode Rounding
		want int64
	}{
		{"half-even 2.5 -> 2", Of(5, 2), HalfEven, 2},
		{"half-even 3.5 -> 4", Of(7, 2), HalfEven, 4},
		{"half-even -2.5 -> -2", Of(-5, 2), HalfEven, -2},
		{"half-up 2.5 -> 3", Of(5, 2), HalfUp, 3},
		{"half-up -2.5 -> -3", Of(-5, 2), HalfUp, -3},
		{"ceiling 2.1 -> 3", Of(21, 10), Ceiling, 3},
		{"ceiling -2.9 -> -2", Of(-29, 10), Ceiling, -2},
		{"floor -2.1 -> -3", Of(-21, 10), Floor, -3},
		{"down -2.9 -> -2", Of(-29, 10), Down, -2},
		{"up 2.1 -> 3", Of(21, 10), Up, 3},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := c.v.RoundToInt64(c.mode)
			check(t, c.name, err == nil && got == c.want)
		})
	}
	check(t, "ToDecimal pads and rounds half-even", Of(7, 2).ToDecimal(4) == "3.5000" && Of(-1, 3).ToDecimal(3) == "-0.333" && Of(200, 217).ToDecimal(10) == "0.9216589862")
}

func TestLockWindowIsHalfOpen(t *testing.T) {
	r := newRig()
	q := quote(t, r.desk, USD, EUR)
	check(t, "quote locks mid + spread", q.CustomerRate.Rate.Equal(Of(204, 217)) && q.SpreadBps == 200)
	r.clock.AdvanceMillis(FifteenMinutes - 1)
	c := confirm(t, r.desk, q.ID, usd("120.00"))
	check(t, "confirm 1 ms before expiry succeeds", c.PresentmentAmount == eur("112.82"))
	mustOK(r.market.Set(EUR, USD, "1.0000"))
	check(t, "a market move inside the window does not change a locked quote", confirm(t, r.desk, q.ID, usd("120.00")).PresentmentAmount == eur("112.82"))
	r.clock.AdvanceMillis(1)
	_, err := r.desk.Confirm(q.ID, usd("120.00"))
	checkErr(t, "confirm at exactly expiresAt is expired", err, ErrQuoteExpired)
	_, err = r.desk.Confirm("fxq_9999", usd("120.00"))
	checkErr(t, "unknown quote rejected", err, ErrInvalidArgument)
	_, err = r.desk.Confirm(quote(t, r.desk, USD, EUR).ID, gbp("120.00"))
	checkErr(t, "quote is bound to its settlement currency", err, ErrCurrencyMismatch)
}

func TestSettlementMerchantAlwaysGetsThePromisedAmount(t *testing.T) {
	r := newRig()
	c := confirm(t, r.desk, quote(t, r.desk, USD, EUR).ID, usd("120.00"))
	flat := settle(t, c, rat("1.0850"))
	down := settle(t, c, rat("1.0600"))
	check(t, "unchanged market: PSP earns 2.41", flat.PspFxPnl == usd("2.41"))
	check(t, "EUR weakens to 1.06: PSP loses 0.41", down.PspFxPnl == usd("-0.41"))
	check(t, "merchant receives 120.00 either way", flat.MerchantReceives == usd("120.00") && down.MerchantReceives == usd("120.00"))
	check(t, "proceeds = merchant + P&L", down.PspProceeds == must(down.MerchantReceives.Plus(down.PspFxPnl)))
	be := BreakEvenRate(c)
	check(t, "break-even rate is 12000/11282", be.Equal(Of(12000, 11282)))
	check(t, "settling at break-even gives zero P&L", settle(t, c, be).PspFxPnl.MinorUnits == 0)
	mustOK(r.market.Set(EUR, USD, "1.0600"))
	check(t, "re-quote after the move shows a new price", must(PresentmentPrice(usd("120.00"), quote(t, r.desk, USD, EUR))) == eur("115.48"))

	clock, market := NewFakeClock(0), NewFakeRateSource()
	mustOK(market.Set(EUR, USD, "1.2500"))
	zero := must(NewFxDesk(clock, market, 0, 1000))
	check(t, "zero spread presents at mid", must(PresentmentPrice(usd("100.00"), quote(t, zero, USD, EUR))) == eur("80.00"))
	_, err := NewFxDesk(clock, market, 0, 0)
	checkErr(t, "lock window must be positive", err, ErrInvalidArgument)
	_, err = zero.Quote(USD, GBP)
	checkErr(t, "missing market rate is reported", err, ErrInvalidArgument)
}

func TestDccDisclosureMath(t *testing.T) {
	o := must(Offer(eur("100.00"), GBP, rat("0.8550"), 350))
	check(t, "DCC rate is 0.8550 * 1.035 = 0.884925", o.DccRate.Rate.Equal(rat("0.884925")))
	check(t, "cardholder pays 88.49 GBP", o.CardAmount == gbp("88.49"))
	check(t, "disclosed markup is exactly 3.5%", o.MarkupPercent().Equal(Of(7, 2)))
	check(t, "effective markup after rounding is below the disclosed", o.EffectiveMarkupPercent().Compare(o.MarkupPercent()) < 0)
	check(t, "gross margin 2.99 GBP", o.GrossMargin() == gbp("2.99"))
	split := must(o.GrossMargin().Allocate(3000, 7000))
	check(t, "margin split 0.90 / 2.09 sums exactly", split[0] == gbp("0.90") && split[1] == gbp("2.09"))
	d := o.Disclosure()
	check(t, "disclosure names both amounts, the rate and the markup",
		strings.Contains(d, "88.49 GBP") && strings.Contains(d, "100.00 EUR") && strings.Contains(d, "0.884925") &&
			strings.Contains(d, "3.50%") && strings.Contains(d, "0.8550"))
	check(t, "declining DCC with no issuer fee costs 85.60", must(IssuerConversion(eur("100.00"), GBP, rat("0.8560"), 0)) == gbp("85.60"))
	check(t, "declining DCC with a 2.99% issuer fee costs 88.16", must(IssuerConversion(eur("100.00"), GBP, rat("0.8560"), 299)) == gbp("88.16"))
	check(t, "zero markup DCC equals the reference conversion", must(Offer(eur("100.00"), GBP, rat("0.8550"), 0)).CardAmount == gbp("85.50"))
	_, err := Offer(eur("100.00"), EUR, ONE, 350)
	checkErr(t, "DCC requires a different card currency", err, ErrInvalidArgument)
}

func TestMoneyDiscipline(t *testing.T) {
	_, err := usd("1.00").Plus(eur("1.00"))
	checkErr(t, "Money rejects currency mix", err, ErrCurrencyMismatch)
}
