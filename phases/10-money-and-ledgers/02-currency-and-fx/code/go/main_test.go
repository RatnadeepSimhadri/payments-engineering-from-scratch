// Tests for phases/10-money-and-ledgers/02-currency-and-fx. Run: go test ./...
package main

import (
	"errors"
	"testing"
)

const now int64 = 1_700_000_000

// ok fails the test on an unexpected error and returns the value, so call sites stay one line:
// got := ok(t)(Convert(...)).
func ok[T any](t *testing.T) func(T, error) T {
	return func(v T, err error) T {
		t.Helper()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		return v
	}
}

func money(t *testing.T, text string, c Currency) Money {
	t.Helper()
	return ok[Money](t)(Parse(text, c))
}

func rate(t *testing.T, base, quote Currency, dec, source string, at int64, bps int) FxRate {
	t.Helper()
	return ok[FxRate](t)(FxRateOf(base, quote, dec, source, at, bps))
}

func cur(t *testing.T, r *CurrencyRegistry, code string) Currency {
	t.Helper()
	return ok[Currency](t)(r.Currency(code))
}

func fixture(t *testing.T) (*CurrencyRegistry, Currency, Currency, Currency, Currency, Currency) {
	t.Helper()
	r := ISO4217()
	return r, cur(t, r, "USD"), cur(t, r, "EUR"), cur(t, r, "GBP"), cur(t, r, "JPY"), cur(t, r, "KWD")
}

func TestRegistry(t *testing.T) {
	registry, USD, EUR, _, JPY, KWD := fixture(t)
	for _, c := range []struct {
		code string
		cur  Currency
		exp  int
	}{{"USD", USD, 2}, {"JPY", JPY, 0}, {"KWD", KWD, 3}, {"CLF", cur(t, registry, "CLF"), 4}} {
		if c.cur.Exponent != c.exp {
			t.Errorf("registry %s exponent = %d, want %d", c.code, c.cur.Exponent, c.exp)
		}
	}
	if ok[Currency](t)(registry.ByNumeric(978)) != EUR {
		t.Error("registry numeric lookup")
	}
	if ok[CurrencyInfo](t)(registry.Info("USD")).NumericCode != 840 {
		t.Error("registry info numeric code")
	}
	if _, err := registry.Currency("USX"); !errors.Is(err, ErrUnknownCurrency) {
		t.Errorf("registry rejects unknown code: %v", err)
	}
	if _, err := registry.Register("USD", 999, 2, "dup"); err == nil {
		t.Error("registry rejects duplicate code")
	}
	if _, err := registry.Register("ZZZ", 840, 2, "dup"); err == nil {
		t.Error("registry rejects duplicate numeric")
	}
	if registry.Size() != 15 {
		t.Errorf("failed duplicate registrations must not change size: %d", registry.Size())
	}
	if ok[Money](t)(registry.ParseAmount("1.250", "KWD")).MinorUnits != 1250 {
		t.Error("registry parse uses registered exponent")
	}
	if _, err := registry.ParseAmount("1.5", "JPY"); err == nil {
		t.Error("registry parse rejects excess precision")
	}
}

func TestRates(t *testing.T) {
	_, USD, EUR, _, _, _ := fixture(t)
	usdEur := rate(t, USD, EUR, "0.9217", "ecb-ref", now-60, 50)
	if usdEur.Numerator != 9217 || usdEur.Denominator != 10000 {
		t.Error("rate parsed as 9217/10000")
	}
	half := rate(t, USD, EUR, "0.50", "x", now, 0)
	if half.Numerator != 1 || half.Denominator != 2 {
		t.Error("rate is reduced by gcd")
	}
	if usdEur.Source != "ecb-ref" || usdEur.ObservedAtEpochSeconds != now-60 {
		t.Error("rate keeps source and timestamp")
	}
	inv := usdEur.Invert()
	if inv.Numerator != 10000 || inv.Denominator != 9217 || inv.Base != EUR || inv.Quote != USD {
		t.Error("invert swaps numerator and denominator")
	}
	if inv.Invert() != usdEur {
		t.Error("invert twice is identity")
	}
	if got := usdEur.ToDecimal(6); got != "0.921700" {
		t.Errorf("toDecimal 6 places = %q", got)
	}
	if got := rate(t, USD, EUR, "0.12345", "x", now, 0).ToDecimal(4); got != "0.1234" {
		t.Errorf("toDecimal rounds HALF_EVEN = %q", got)
	}
	for _, bad := range []struct {
		name  string
		quote Currency
		dec   string
	}{{"same base and quote", USD, "1"}, {"zero", EUR, "0"}, {"garbage", EUR, "0,92"}} {
		if _, err := FxRateOf(USD, bad.quote, bad.dec, "x", now, 0); err == nil {
			t.Errorf("rate rejects %s", bad.name)
		}
	}
}

func TestSpread(t *testing.T) {
	_, USD, EUR, _, _, _ := fixture(t)
	usdEur := rate(t, USD, EUR, "0.9217", "ecb-ref", now-60, 50)
	bid, ask := ok[FxRate](t)(usdEur.Bid()), ok[FxRate](t)(usdEur.Ask())
	if !(bid.Numerator*usdEur.Denominator < usdEur.Numerator*bid.Denominator) {
		t.Error("bid < mid")
	}
	if !(ask.Numerator*usdEur.Denominator > usdEur.Numerator*ask.Denominator) {
		t.Error("ask > mid")
	}
	if got := bid.ToDecimal(6); got != "0.919396" {
		t.Errorf("bid at 50bps of 0.9217 = %q, want 0.919396", got)
	}
	if got := ask.ToDecimal(6); got != "0.924004" {
		t.Errorf("ask at 50bps of 0.9217 = %q, want 0.924004", got)
	}
	if bid.SpreadBps != 0 {
		t.Error("spread-applied rate has zero residual spread")
	}
}

func TestConversion(t *testing.T) {
	_, USD, EUR, _, JPY, KWD := fixture(t)
	usdEur := rate(t, USD, EUR, "0.9217", "ecb-ref", now-60, 50)
	cases := []struct {
		name     string
		amount   Money
		rate     FxRate
		rounding Rounding
		want     Money
		rounded  bool
	}{
		{"100.00 USD -> 92.17 EUR", money(t, "100.00", USD), usdEur, HalfEven, money(t, "92.17", EUR), false},
		{"19.99 USD -> 18.42 EUR", money(t, "19.99", USD), usdEur, HalfEven, money(t, "18.42", EUR), true},
		{"USD -> JPY lands on a whole yen", money(t, "19.99", USD), rate(t, USD, JPY, "149.37", "x", now, 0), HalfEven, money(t, "2986", JPY), true},
		{"USD -> KWD uses three decimals", money(t, "19.99", USD), rate(t, USD, KWD, "0.30825", "x", now, 0), HalfEven, money(t, "6.162", KWD), true},
		{"sub-unit conversion with DOWN is zero", money(t, "0.01", USD), usdEur, Down, OfMinor(0, EUR), true},
		{"sub-unit conversion with UP is one minor unit", money(t, "0.01", USD), usdEur, Up, OfMinor(1, EUR), true},
		{"negative amounts convert symmetrically", money(t, "-19.99", USD), usdEur, HalfEven, money(t, "-18.42", EUR), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ok[Conversion](t)(Convert(c.amount, c.rate, c.rounding))
			if got.To != c.want {
				t.Errorf("to = %v, want %v", got.To, c.want)
			}
			if got.Rounded() != c.rounded {
				t.Errorf("rounded = %v, want %v", got.Rounded(), c.rounded)
			}
		})
	}
	c := ok[Conversion](t)(Convert(money(t, "100.00", USD), usdEur, HalfEven))
	if c.RemainderNumerator != 0 {
		t.Error("exact conversion has zero remainder")
	}
	c2 := ok[Conversion](t)(Convert(money(t, "19.99", USD), usdEur, HalfEven))
	if c2.RemainderNumerator != 4783 || c2.RemainderDenominator != 10000 {
		t.Errorf("remainder is 4783/10000 of a cent, got %d/%d", c2.RemainderNumerator, c2.RemainderDenominator)
	}
	if _, err := Convert(money(t, "1.00", EUR), usdEur, HalfEven); !errors.Is(err, ErrCurrencyMismatch) {
		t.Errorf("convert rejects amount in wrong currency: %v", err)
	}
}

func TestRoundTrip(t *testing.T) {
	_, USD, EUR, _, _, _ := fixture(t)
	usdEur := rate(t, USD, EUR, "0.9217", "ecb-ref", now-60, 50)
	rt := ok[RoundTripResult](t)(RoundTrip(money(t, "19.99", USD), usdEur, HalfEven))
	if rt.Back.To != money(t, "19.98", USD) {
		t.Errorf("round trip comes back 19.98, got %v", rt.Back.To)
	}
	if rt.Residue != money(t, "0.01", USD) {
		t.Errorf("round trip residue is 0.01 USD, got %v", rt.Residue)
	}
	if rt.Residue.Currency != USD {
		t.Error("residue is in the original currency")
	}
	manufactured := ok[RoundTripResult](t)(RoundTrip(money(t, "0.06", USD), usdEur, HalfEven))
	if manufactured.Residue != money(t, "-0.01", USD) {
		t.Errorf("round trip can manufacture a cent: residue -0.01, got %v", manufactured.Residue)
	}
	exact := ok[RoundTripResult](t)(RoundTrip(money(t, "100.00", USD), rate(t, USD, EUR, "0.50", "x", now, 0), HalfEven))
	if !exact.Residue.IsZero() {
		t.Error("exact round trip has zero residue")
	}
}

func TestCrossRates(t *testing.T) {
	_, USD, EUR, GBP, _, _ := fixture(t)
	usdEur := rate(t, USD, EUR, "0.9217", "ecb-ref", now-60, 50)
	usdGbp := rate(t, USD, GBP, "0.7893", "ecb-ref", now-60, 50)
	eurGbp := ok[FxRate](t)(usdEur.Cross(usdGbp))
	if eurGbp.Base != EUR || eurGbp.Quote != GBP || eurGbp.Numerator != 7893 || eurGbp.Denominator != 9217 {
		t.Errorf("cross rate EUR/GBP = 7893/9217, got %v", eurGbp)
	}
	if eurGbp.ObservedAtEpochSeconds != now-60 {
		t.Error("cross rate keeps older timestamp")
	}
	c7 := ok[Conversion](t)(Convert(money(t, "50.00", EUR), eurGbp, HalfEven))
	if c7.To != money(t, "42.82", GBP) {
		t.Errorf("50.00 EUR -> 42.82 GBP via USD, got %v", c7.To)
	}
	if _, err := usdEur.Cross(eurGbp); err == nil {
		t.Error("cross rate needs shared base")
	}
}

func TestRateTable(t *testing.T) {
	_, USD, EUR, GBP, JPY, _ := fixture(t)
	usdEur := rate(t, USD, EUR, "0.9217", "ecb-ref", now-60, 50)
	usdGbp := rate(t, USD, GBP, "0.7893", "ecb-ref", now-60, 50)
	eurGbp := ok[FxRate](t)(usdEur.Cross(usdGbp))
	table := NewRateTable(USD, 300)
	for _, r := range []FxRate{usdEur, usdGbp} {
		if err := table.Put(r); err != nil {
			t.Fatalf("put %v: %v", r, err)
		}
	}
	if ok[FxRate](t)(table.Rate(USD, EUR, now)) != usdEur {
		t.Error("table direct")
	}
	if ok[FxRate](t)(table.Rate(EUR, USD, now)) != usdEur.Invert() {
		t.Error("table inverted")
	}
	if ok[FxRate](t)(table.Rate(EUR, GBP, now)).Numerator != 7893 {
		t.Error("table crossed")
	}
	if len(table.All()) != 2 {
		t.Error("table lists both rates in insertion order")
	}
	if _, err := table.Rate(USD, EUR, now+3600); !errors.Is(err, ErrStaleRate) {
		t.Errorf("table rejects stale rate: %v", err)
	}
	if err := table.Put(eurGbp); err == nil {
		t.Error("table rejects foreign base")
	}
	if _, err := table.Rate(USD, JPY, now); err == nil {
		t.Error("table rejects missing pair")
	}
	if _, err := table.Rate(USD, USD, now); err == nil {
		t.Error("table rejects same currency")
	}
}
