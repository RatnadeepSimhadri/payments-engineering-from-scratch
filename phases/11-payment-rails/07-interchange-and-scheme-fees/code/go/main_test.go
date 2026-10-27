// Tests for phases/11-payment-rails/07-interchange-and-scheme-fees. Run: go test ./...
package main

import (
	"errors"
	"testing"
)

func ok[T any](t *testing.T) func(T, error) T {
	return func(v T, err error) T {
		t.Helper()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		return v
	}
}

func check(t *testing.T, name string, cond bool) {
	t.Helper()
	if !cond {
		t.Error(name)
	}
}

func checkErr(t *testing.T, name string, want, got error) {
	t.Helper()
	if !errors.Is(got, want) {
		t.Errorf("%s: want %v, got %v", name, want, got)
	}
}

func usd(t *testing.T, s string) Money {
	t.Helper()
	return ok[Money](t)(Parse(s, USD))
}

func rate(t *testing.T, percent string, fixed int64) Rate {
	t.Helper()
	return ok[Rate](t)(RateOf(percent, fixed))
}

func tx(t *testing.T, id string, amount Money, ct CardType, r Region, ch Channel) Transaction {
	t.Helper()
	return ok[Transaction](t)(NewTransaction(id, amount, ct, r, ch))
}

func fixture(t *testing.T) (*FeeEngine, Pricing, Pricing) {
	t.Helper()
	return NewFeeEngine(DemoInterchangeTable(), DemoSchemeFees()), IcPlusPlus{rate(t, "0.20", 5)}, Blended{rate(t, "2.90", 30)}
}

func TestRateParsingIsExact(t *testing.T) {
	for _, c := range []struct {
		percent string
		ppm     int64
	}{{"1.80", 18_000}, {"0.05", 500}, {"2", 20_000}, {"0.0013", 13}} {
		if got := rate(t, c.percent, 0).Ppm; got != c.ppm {
			t.Errorf("%s%% is %d ppm, got %d", c.percent, c.ppm, got)
		}
	}
	_, err := RateOf("0.00001", 0)
	checkErr(t, "five decimals of a percent rejected", ErrInvalidArgument, err)
	_, err = NewRate(-1, 0)
	checkErr(t, "negative rate rejected", ErrInvalidArgument, err)
	check(t, "rate String", rate(t, "1.80", 10).String() == "1.80% + 10")
	check(t, "rate String keeps two decimals minimum", rate(t, "2", 0).String() == "2.00% + 0")
	check(t, "rate String trims to four decimals", rate(t, "0.0013", 0).String() == "0.0013% + 0")
}

func TestWorkedExampleICPlusPlus(t *testing.T) {
	engine, icpp, _ := fixture(t)
	f := ok[FeeBreakdown](t)(engine.Quote(tx(t, "t1", usd(t, "100.00"), ConsumerCredit, USDomestic, CardNotPresent), icpp))
	check(t, "interchange 1.90", f.Interchange == usd(t, "1.90"))
	check(t, "scheme 0.16", f.SchemeFees == usd(t, "0.16"))
	check(t, "acquirer markup 0.25", f.AcquirerMargin == usd(t, "0.25"))
	check(t, "merchant net 97.69", f.MerchantNet == usd(t, "97.69"))
	check(t, "merchant discount 2.31", f.MerchantDiscount() == usd(t, "2.31"))
	check(t, "effective rate 231 bps", f.EffectiveRateBps() == 231)
}

func TestBlendedPricing(t *testing.T) {
	engine, _, blended := fixture(t)
	b1 := ok[FeeBreakdown](t)(engine.Quote(tx(t, "t", usd(t, "100.00"), ConsumerCredit, USDomestic, CardNotPresent), blended))
	b2 := ok[FeeBreakdown](t)(engine.Quote(tx(t, "t", usd(t, "100.00"), CommercialCredit, USDomestic, CardNotPresent), blended))
	check(t, "blended merchant net is card-independent", b1.MerchantNet == b2.MerchantNet && b1.MerchantNet == usd(t, "96.80"))
	check(t, "blended margin on consumer credit 1.14", b1.AcquirerMargin == usd(t, "1.14"))
	check(t, "blended margin on commercial 0.24", b2.AcquirerMargin == usd(t, "0.24"))
	big := ok[FeeBreakdown](t)(engine.Quote(tx(t, "t", usd(t, "10000.00"), CommercialCredit, USDomestic, CardNotPresent), Blended{rate(t, "2.70", 0)}))
	check(t, "blended can lose money: margin negative on commercial at 2.70% flat", big.AcquirerMargin.IsNegative())
}

func TestRegulationII(t *testing.T) {
	engine, icpp, _ := fixture(t)
	d := ok[FeeBreakdown](t)(engine.Quote(tx(t, "t", usd(t, "2.00"), ConsumerDebitRegulated, USDomestic, CardPresent), icpp))
	check(t, "regulated debit 2.00: 0.05% rounds to 0, + 0.22", d.Interchange == usd(t, "0.22"))
	checkErr(t, "Reg II cap enforced at load", ErrInvalidArgument,
		NewInterchangeTable().Put(ConsumerDebitRegulated, USDomestic, CardPresent, rate(t, "0.05", 25)))
	exempt := NewInterchangeTable()
	ok[int](t)(0, exempt.Put(ConsumerDebitExempt, USDomestic, CardPresent, rate(t, "0.80", 15)))
	check(t, "exempt debit is not capped", exempt.Size() == 1)
}

func TestIFRCapsAndMissingRows(t *testing.T) {
	engine, icpp, _ := fixture(t)
	checkErr(t, "IFR credit cap enforced", ErrInvalidArgument,
		NewInterchangeTable().Put(PremiumCredit, EEADomestic, CardPresent, rate(t, "0.31", 0)))
	checkErr(t, "IFR forbids a fixed part on consumer cards", ErrInvalidArgument,
		NewInterchangeTable().Put(ConsumerDebitRegulated, EEADomestic, CardPresent, rate(t, "0.10", 1)))
	check(t, "IFR does not cap commercial cards", NewInterchangeTable().Put(CommercialCredit, EEADomestic, CardPresent, rate(t, "1.50", 5)) == nil)
	eur := ok[Money](t)(Parse("100.00", EUR))
	e := ok[FeeBreakdown](t)(engine.Quote(tx(t, "t", eur, PremiumCredit, EEADomestic, CardPresent), icpp))
	check(t, "EEA premium credit capped at 0.30", e.Interchange == ok[Money](t)(Parse("0.30", EUR)))
	x := ok[FeeBreakdown](t)(engine.Quote(tx(t, "t", eur, ConsumerCredit, InterRegional, CardNotPresent), icpp))
	check(t, "inter-regional scheme fee adds 0.60% cross-border", x.SchemeFees == ok[Money](t)(Parse("0.76", EUR)))
	_, err := engine.Quote(tx(t, "t", eur, ConsumerDebitExempt, EEADomestic, CardPresent), icpp)
	checkErr(t, "no EEA exempt-debit row", ErrNoRate, err)
	check(t, "demo table has 28 rows (10 US, 8 EEA, 10 inter-regional)", DemoInterchangeTable().Size() == 28)
}

func TestRoundingOncePerComponent(t *testing.T) {
	engine, icpp, _ := fixture(t)
	odd := ok[FeeBreakdown](t)(engine.Quote(tx(t, "t", usd(t, "37.77"), ConsumerCredit, USDomestic, CardNotPresent), icpp))
	check(t, "37.77 interchange 0.78", odd.Interchange == usd(t, "0.78"))
	check(t, "37.77 scheme 0.05 + 0.02 = 0.07", odd.SchemeFees == usd(t, "0.07"))
}

func TestSplitSumsToGross(t *testing.T) {
	engine, icpp, blended := fixture(t)
	for cents := int64(1); cents <= 50_000; cents += 97 {
		for _, ct := range CardTypes {
			for _, p := range []Pricing{icpp, blended} {
				q := ok[FeeBreakdown](t)(engine.Quote(tx(t, "t", OfMinor(cents, USD), ct, USDomestic, CardNotPresent), p))
				if sum := ok[Money](t)(Sum(USD, q.Interchange, q.SchemeFees, q.AcquirerMargin, q.MerchantNet)); sum != q.Gross {
					t.Fatalf("split does not sum to gross at %d cents, %v, %T: %v != %v", cents, ct, p, sum, q.Gross)
				}
			}
		}
	}
	_, err := NewFeeBreakdown(usd(t, "1.00"), rate(t, "1", 0), usd(t, "0.01"), usd(t, "0.01"), usd(t, "0.01"), usd(t, "0.98"))
	checkErr(t, "a non-summing breakdown cannot be constructed", ErrInvariant, err)
	_, err = NewTransaction("t", usd(t, "0.00"), ConsumerCredit, USDomestic, CardPresent)
	checkErr(t, "zero amount is not a purchase", ErrInvalidArgument, err)
	_, err = NewTransaction("t", usd(t, "-1.00"), ConsumerCredit, USDomestic, CardPresent)
	checkErr(t, "negative amount is not a purchase", ErrInvalidArgument, err)
}

func TestAttribution(t *testing.T) {
	engine, icpp, _ := fixture(t)
	month := []Transaction{
		tx(t, "a", usd(t, "100.00"), ConsumerCredit, USDomestic, CardPresent),
		tx(t, "b", usd(t, "37.77"), PremiumCredit, USDomestic, CardNotPresent),
		tx(t, "c", usd(t, "2.00"), ConsumerDebitRegulated, USDomestic, CardPresent),
	}
	parts := ok[[]Money](t)(Attribute(usd(t, "25.00"), month))
	check(t, "attribution sums exactly", ok[Money](t)(Sum(USD, parts...)) == usd(t, "25.00"))
	check(t, "largest remainder: a gets 17.89 (floor 17.88 + leftover cent)", parts[0] == usd(t, "17.89"))
	check(t, "b gets 6.75 and c gets 0.36", parts[1] == usd(t, "6.75") && parts[2] == usd(t, "0.36"))
	_, err := Attribute(ok[Money](t)(Parse("25.00", EUR)), month)
	checkErr(t, "attribution rejects mixed currency", ErrCurrencyMismatch, err)
	quotes := make([]FeeBreakdown, 0, len(month))
	for _, x := range month {
		quotes = append(quotes, ok[FeeBreakdown](t)(engine.Quote(x, icpp)))
	}
	total := ok[FeeBreakdown](t)(Total(quotes))
	check(t, "month total gross 139.77", total.Gross == usd(t, "139.77"))
	check(t, "month total merchant net is the sum of the parts", total.MerchantNet == ok[Money](t)(Sum(USD, quotes[0].MerchantNet, quotes[1].MerchantNet, quotes[2].MerchantNet)))
}
