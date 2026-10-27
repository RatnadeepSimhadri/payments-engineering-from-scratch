// Lesson: phases/10-money-and-ledgers/02-currency-and-fx/docs/en.md
// Currencies, Exchange Rates, and FX Conversion
// Run:  go run .
// Test: go test ./...
// Sources: ISO 4217:2015 (alphabetic code, numeric code, minor-unit exponent); ECB reference-rate conventions
// (base currency quoting). Money type copied from Phase 10, Lesson 01 (money.go). Go 1.22, standard library only.
package main

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Zero is the zero amount of a currency.
func Zero(currency Currency) Money { return Money{0, currency} }

// ---------------------------------------------------------------------------------------------
// ISO 4217 registry.
// ---------------------------------------------------------------------------------------------

// ErrUnknownCurrency is returned for a code the registry has never seen.
var ErrUnknownCurrency = errors.New("unknown currency code")

// CurrencyInfo is one row of ISO 4217: alphabetic code, numeric code, minor-unit exponent, and a display name.
type CurrencyInfo struct {
	Code        string
	NumericCode int
	Exponent    int
	Name        string
}

func (i CurrencyInfo) Currency() Currency { return Currency{i.Code, i.Exponent} }

// CurrencyRegistry is the list of currencies this system is allowed to handle. The exponent lives here, in
// data, so a currency that redenominates (ISO 4217 amendments do this) is a data change, not a code change.
// Go maps do not keep insertion order, so a separate slice of codes preserves the LinkedHashMap ordering.
type CurrencyRegistry struct {
	order     []string
	byCode    map[string]CurrencyInfo
	byNumeric map[int]CurrencyInfo
}

func NewCurrencyRegistry() *CurrencyRegistry {
	return &CurrencyRegistry{byCode: map[string]CurrencyInfo{}, byNumeric: map[int]CurrencyInfo{}}
}

// ISO4217 is a registry pre-loaded with a representative slice of ISO 4217, including the 0- and 3-decimal cases.
func ISO4217() *CurrencyRegistry {
	r := NewCurrencyRegistry()
	rows := []CurrencyInfo{
		{"USD", 840, 2, "US Dollar"}, {"EUR", 978, 2, "Euro"}, {"GBP", 826, 2, "Pound Sterling"},
		{"INR", 356, 2, "Indian Rupee"}, {"CHF", 756, 2, "Swiss Franc"}, {"CAD", 124, 2, "Canadian Dollar"},
		{"AUD", 36, 2, "Australian Dollar"}, {"BRL", 986, 2, "Brazilian Real"}, {"JPY", 392, 0, "Yen"},
		{"KRW", 410, 0, "Won"}, {"VND", 704, 0, "Dong"}, {"KWD", 414, 3, "Kuwaiti Dinar"},
		{"BHD", 48, 3, "Bahraini Dinar"}, {"JOD", 400, 3, "Jordanian Dinar"}, {"CLF", 990, 4, "Unidad de Fomento"},
	}
	for _, row := range rows {
		if _, err := r.Register(row.Code, row.NumericCode, row.Exponent, row.Name); err != nil {
			panic(err)
		}
	}
	return r
}

func (r *CurrencyRegistry) Register(code string, numericCode, exponent int, name string) (CurrencyInfo, error) {
	if _, err := NewCurrency(code, exponent); err != nil { // validates code and exponent
		return CurrencyInfo{}, err
	}
	info := CurrencyInfo{code, numericCode, exponent, name}
	if _, dup := r.byCode[code]; dup {
		return CurrencyInfo{}, fmt.Errorf("already registered: %s", code)
	}
	if _, dup := r.byNumeric[numericCode]; dup {
		return CurrencyInfo{}, fmt.Errorf("numeric code already registered: %d", numericCode)
	}
	r.byCode[code] = info
	r.byNumeric[numericCode] = info
	r.order = append(r.order, code)
	return info, nil
}

func (r *CurrencyRegistry) Currency(code string) (Currency, error) {
	info, ok := r.byCode[code]
	if !ok {
		return Currency{}, fmt.Errorf("%w: %s", ErrUnknownCurrency, code)
	}
	return info.Currency(), nil
}

func (r *CurrencyRegistry) Info(code string) (CurrencyInfo, error) {
	info, ok := r.byCode[code]
	if !ok {
		return CurrencyInfo{}, fmt.Errorf("%w: %s", ErrUnknownCurrency, code)
	}
	return info, nil
}

func (r *CurrencyRegistry) ByNumeric(numericCode int) (Currency, error) {
	info, ok := r.byNumeric[numericCode]
	if !ok {
		return Currency{}, fmt.Errorf("%w: %d", ErrUnknownCurrency, numericCode)
	}
	return info.Currency(), nil
}

func (r *CurrencyRegistry) Contains(code string) bool { _, ok := r.byCode[code]; return ok }

// ParseAmount parses a decimal string in a currency known to the registry. Unknown code: rejected before the amount is read.
func (r *CurrencyRegistry) ParseAmount(amount, code string) (Money, error) {
	c, err := r.Currency(code)
	if err != nil {
		return Money{}, err
	}
	return Parse(amount, c)
}

func (r *CurrencyRegistry) Size() int { return len(r.order) }

// ---------------------------------------------------------------------------------------------
// Exchange rates as exact rationals.
// ---------------------------------------------------------------------------------------------

// ErrStaleRate is returned when a rate is older than the caller allows.
var ErrStaleRate = errors.New("stale rate")

// FxRate: one unit of Base buys Numerator/Denominator units of Quote: an exact rational, so "0.9217" is
// 9217/10000 and never a float64. Carries provenance (source, observation time) and the spread in basis
// points the provider charges around this mid rate.
type FxRate struct {
	Base                   Currency
	Quote                  Currency
	Numerator              int64
	Denominator            int64
	Source                 string
	ObservedAtEpochSeconds int64
	SpreadBps              int
}

// NewFxRate validates and reduces the rational, as the Java compact constructor does.
func NewFxRate(base, quote Currency, numerator, denominator int64, source string, observedAt int64, spreadBps int) (FxRate, error) {
	if base == quote {
		return FxRate{}, fmt.Errorf("base and quote must differ: %s", base.Code)
	}
	if numerator <= 0 || denominator <= 0 {
		return FxRate{}, errors.New("rate must be positive")
	}
	if spreadBps < 0 || spreadBps >= 10_000 {
		return FxRate{}, errors.New("spread must be 0..9999 bps")
	}
	g := gcd(numerator, denominator)
	return FxRate{base, quote, numerator / g, denominator / g, source, observedAt, spreadBps}, nil
}

// FxRateOf builds a mid rate from a decimal string such as "0.9217" or "149.37" without going through a float.
func FxRateOf(base, quote Currency, decimalRate, source string, observedAt int64, spreadBps int) (FxRate, error) {
	s := strings.TrimSpace(decimalRate)
	whole, frac, _ := strings.Cut(s, ".")
	if whole == "" {
		whole = "0"
	}
	if !digitsOnly(whole) || !digitsOnly(frac) || whole+frac == "" {
		return FxRate{}, fmt.Errorf("not a decimal rate: %q", decimalRate)
	}
	den := int64(1)
	for range len(frac) {
		var err error
		if den, err = mulChecked(den, 10); err != nil {
			return FxRate{}, err
		}
	}
	num, err := strconv.ParseInt(whole+frac, 10, 64)
	if err != nil {
		return FxRate{}, fmt.Errorf("not a decimal rate: %q", decimalRate)
	}
	return NewFxRate(base, quote, num, den, source, observedAt, spreadBps)
}

func digitsOnly(s string) bool {
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	return true
}

func gcd(a, b int64) int64 {
	a, b = abs64(a), abs64(b)
	for b != 0 {
		a, b = b, a%b
	}
	if a == 0 {
		return 1
	}
	return a
}

// Invert is the same rate quoted the other way round: quote per base becomes base per quote. Exact, no rounding.
func (r FxRate) Invert() FxRate {
	return FxRate{r.Quote, r.Base, r.Denominator, r.Numerator, r.Source, r.ObservedAtEpochSeconds, r.SpreadBps}
}

// Bid is the rate the customer gets when they sell base and buy quote: mid less half the spread.
func (r FxRate) Bid() (FxRate, error) { return r.withSpreadApplied(-int64(r.SpreadBps)) }

// Ask is the rate the customer pays when they buy base with quote: mid plus half the spread.
func (r FxRate) Ask() (FxRate, error) { return r.withSpreadApplied(+int64(r.SpreadBps)) }

func (r FxRate) withSpreadApplied(signedBps int64) (FxRate, error) {
	// mid * (1 +- spread/2) = num/den * (20000 +- bps)/20000, all integer
	num, err := mulChecked(r.Numerator, 20_000+signedBps)
	if err != nil {
		return FxRate{}, err
	}
	den, err := mulChecked(r.Denominator, 20_000)
	if err != nil {
		return FxRate{}, err
	}
	return NewFxRate(r.Base, r.Quote, num, den, r.Source, r.ObservedAtEpochSeconds, 0)
}

// Cross derives a rate through a shared base. If this is USD/EUR (1 USD = x EUR) and other is USD/GBP
// (1 USD = y GBP), the result is EUR/GBP: 1 EUR = y/x GBP. Exact rational division; no intermediate rounding.
func (r FxRate) Cross(other FxRate) (FxRate, error) {
	if r.Base != other.Base {
		return FxRate{}, fmt.Errorf("cross rate needs a shared base: %s vs %s", r.Base.Code, other.Base.Code)
	}
	if r.Quote == other.Quote {
		return FxRate{}, errors.New("cross rate between identical quotes")
	}
	num, err := mulChecked(other.Numerator, r.Denominator)
	if err != nil {
		return FxRate{}, err
	}
	den, err := mulChecked(other.Denominator, r.Numerator)
	if err != nil {
		return FxRate{}, err
	}
	return NewFxRate(r.Quote, other.Quote, num, den, r.Source+"+"+other.Source,
		min(r.ObservedAtEpochSeconds, other.ObservedAtEpochSeconds), max(r.SpreadBps, other.SpreadBps))
}

// RequireFresh rejects a rate older than maxAgeSeconds at the given clock reading. Rates have a shelf life.
func (r FxRate) RequireFresh(nowEpochSeconds, maxAgeSeconds int64) (FxRate, error) {
	age := nowEpochSeconds - r.ObservedAtEpochSeconds
	if age > maxAgeSeconds {
		return FxRate{}, fmt.Errorf("%w: %s rate from %s is %ds old; max %ds", ErrStaleRate, r.Pair(), r.Source, age, maxAgeSeconds)
	}
	return r, nil
}

func (r FxRate) Pair() string { return r.Base.Code + "/" + r.Quote.Code }

// ToDecimal renders the rate with the requested number of places, HALF_EVEN. For display and logs only.
func (r FxRate) ToDecimal(places int) string {
	scale := int64(1)
	for range places {
		scale *= 10
	}
	scaled := DivideRounded(r.Numerator*scale, r.Denominator, HalfEven)
	whole := strconv.FormatInt(scaled/scale, 10)
	if places == 0 {
		return whole
	}
	return fmt.Sprintf("%s.%0*d", whole, places, scaled%scale)
}

func (r FxRate) String() string {
	return fmt.Sprintf("%s %s (%d/%d) from %s", r.Pair(), r.ToDecimal(6), r.Numerator, r.Denominator, r.Source)
}

// ---------------------------------------------------------------------------------------------
// Conversion: the one place a fraction is created, and the record of what was done with it.
// ---------------------------------------------------------------------------------------------

// Conversion is the result of one conversion: the amount that went in, the amount that came out, the rate and
// rounding used, and the exact fraction of a quote minor unit that the rounding discarded
// (RemainderNumerator/RemainderDenominator, in the open interval (-1, 1)). The fraction is what lets an audit
// recompute the conversion and account for every sub-unit of value.
type Conversion struct {
	From                 Money
	To                   Money
	Rate                 FxRate
	Rounding             Rounding
	RemainderNumerator   int64
	RemainderDenominator int64
}

// Rounded is true when the rounding moved the result away from the exact value in the quote currency.
func (c Conversion) Rounded() bool { return c.RemainderNumerator != 0 }

// Convert computes amount (base minor units) * rate * 10^quoteExp / 10^baseExp, as one exact rational,
// rounded once. The remainder of that single division is reported, not discarded.
func Convert(amount Money, rate FxRate, rounding Rounding) (Conversion, error) {
	if amount.Currency != rate.Base {
		return Conversion{}, fmt.Errorf("%w: cannot combine %s with %s", ErrCurrencyMismatch, amount.Currency.Code, rate.Base.Code)
	}
	num, err := mulChecked(amount.MinorUnits, rate.Numerator)
	if err != nil {
		return Conversion{}, err
	}
	if num, err = mulChecked(num, rate.Quote.MinorUnitsPerMajor()); err != nil {
		return Conversion{}, err
	}
	den, err := mulChecked(rate.Denominator, rate.Base.MinorUnitsPerMajor())
	if err != nil {
		return Conversion{}, err
	}
	rounded := DivideRounded(num, den, rounding)
	// exact - rounded, as a fraction of one quote minor unit: (num - rounded*den) / den
	product, err := mulChecked(rounded, den)
	if err != nil {
		return Conversion{}, err
	}
	remainderNum, err := addChecked(num, -product)
	if err != nil {
		return Conversion{}, err
	}
	g := gcd(remainderNum, den)
	return Conversion{amount, OfMinor(rounded, rate.Quote), rate, rounding, remainderNum / g, den / g}, nil
}

// RoundTripResult: convert out and back with the inverse rate. The residue is what the customer does not get
// back: positive means the round trip lost money (it belongs in an FX rounding gain account), negative means
// it manufactured money (an FX rounding loss). It is never silently zero.
type RoundTripResult struct {
	Out     Conversion
	Back    Conversion
	Residue Money
}

func RoundTrip(amount Money, rate FxRate, rounding Rounding) (RoundTripResult, error) {
	out, err := Convert(amount, rate, rounding)
	if err != nil {
		return RoundTripResult{}, err
	}
	back, err := Convert(out.To, rate.Invert(), rounding)
	if err != nil {
		return RoundTripResult{}, err
	}
	residue, err := amount.Minus(back.To)
	if err != nil {
		return RoundTripResult{}, err
	}
	return RoundTripResult{out, back, residue}, nil
}

// RateTable is a rate table keyed by pair, with a base currency through which any cross rate can be derived.
// Direct rates win; otherwise base/X and base/Y are combined. Every lookup is checked for staleness.
type RateTable struct {
	baseCurrency  Currency
	order         []string
	rates         map[string]FxRate
	maxAgeSeconds int64
}

func NewRateTable(baseCurrency Currency, maxAgeSeconds int64) *RateTable {
	return &RateTable{baseCurrency: baseCurrency, rates: map[string]FxRate{}, maxAgeSeconds: maxAgeSeconds}
}

func (t *RateTable) Put(rate FxRate) error {
	if rate.Base != t.baseCurrency {
		return fmt.Errorf("table quotes against %s; got %s", t.baseCurrency.Code, rate.Pair())
	}
	if _, seen := t.rates[rate.Quote.Code]; !seen {
		t.order = append(t.order, rate.Quote.Code)
	}
	t.rates[rate.Quote.Code] = rate
	return nil
}

func (t *RateTable) Rate(from, to Currency, nowEpochSeconds int64) (FxRate, error) {
	if from == to {
		return FxRate{}, fmt.Errorf("no rate needed for %s", from.Code)
	}
	if from == t.baseCurrency {
		return t.fresh(to, nowEpochSeconds)
	}
	if to == t.baseCurrency {
		r, err := t.fresh(from, nowEpochSeconds)
		if err != nil {
			return FxRate{}, err
		}
		return r.Invert(), nil
	}
	a, err := t.fresh(from, nowEpochSeconds)
	if err != nil {
		return FxRate{}, err
	}
	b, err := t.fresh(to, nowEpochSeconds)
	if err != nil {
		return FxRate{}, err
	}
	return a.Cross(b)
}

func (t *RateTable) fresh(quote Currency, now int64) (FxRate, error) {
	r, err := t.direct(quote)
	if err != nil {
		return FxRate{}, err
	}
	return r.RequireFresh(now, t.maxAgeSeconds)
}

func (t *RateTable) direct(quote Currency) (FxRate, error) {
	r, ok := t.rates[quote.Code]
	if !ok {
		return FxRate{}, fmt.Errorf("no %s/%s rate loaded", t.baseCurrency.Code, quote.Code)
	}
	return r, nil
}

// All returns the loaded rates in insertion order.
func (t *RateTable) All() []FxRate {
	out := make([]FxRate, 0, len(t.order))
	for _, code := range t.order {
		out = append(out, t.rates[code])
	}
	return out
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func main() {
	var now int64 = 1_700_000_000 // injected clock: 2023-11-14T22:13:20Z
	registry := ISO4217()
	USD, EUR, GBP := must(registry.Currency("USD")), must(registry.Currency("EUR")), must(registry.Currency("GBP"))
	JPY, KWD := must(registry.Currency("JPY")), must(registry.Currency("KWD"))

	fmt.Println("== ISO 4217 registry: the exponent is data ==")
	for _, code := range []string{"USD", "JPY", "KWD", "CLF"} {
		info := must(registry.Info(code))
		fmt.Printf("%s numeric=%03d exponent=%d  1 major = %d minor  (%s)\n",
			code, info.NumericCode, info.Exponent, info.Currency().MinorUnitsPerMajor(), info.Name)
	}
	fmt.Println("numeric 978 -> " + must(registry.ByNumeric(978)).Code)
	if _, err := registry.Currency("USX"); err != nil {
		fmt.Println("rejected:", err)
	}

	fmt.Println("\n== A rate is an exact rational with provenance ==")
	usdEur := must(FxRateOf(USD, EUR, "0.9217", "ecb-ref", now-60, 50))
	usdJpy := must(FxRateOf(USD, JPY, "149.37", "ecb-ref", now-60, 50))
	usdGbp := must(FxRateOf(USD, GBP, "0.7893", "ecb-ref", now-60, 50))
	usdKwd := must(FxRateOf(USD, KWD, "0.30825", "ecb-ref", now-60, 50))
	fmt.Println(usdEur)
	fmt.Printf("inverted: %v   (exact: 10000/9217, not 1.0849...)\n", usdEur.Invert())
	fmt.Printf("bid %s  mid %s  ask %s  (50 bps spread)\n",
		must(usdEur.Bid()).ToDecimal(6), usdEur.ToDecimal(6), must(usdEur.Ask()).ToDecimal(6))

	fmt.Println("\n== Conversion rounds exactly once, and reports what it dropped ==")
	c1 := must(Convert(must(Parse("100.00", USD)), usdEur, HalfEven))
	fmt.Printf("%v -> %v  dropped %d/%d of a cent\n", c1.From, c1.To, c1.RemainderNumerator, c1.RemainderDenominator)
	c2 := must(Convert(must(Parse("19.99", USD)), usdEur, HalfEven))
	fmt.Printf("%v -> %v  exact 18.424783, dropped %d/%d of a cent\n", c2.From, c2.To, c2.RemainderNumerator, c2.RemainderDenominator)
	c3 := must(Convert(must(Parse("19.99", USD)), usdJpy, HalfEven))
	fmt.Printf("%v -> %v  (JPY has no fraction: exact 2985.9063 yen)\n", c3.From, c3.To)
	c4 := must(Convert(must(Parse("19.99", USD)), usdKwd, HalfEven))
	fmt.Printf("%v -> %v  (KWD has three decimals)\n", c4.From, c4.To)
	c5 := must(Convert(must(Parse("0.01", USD)), usdEur, Down))
	fmt.Printf("%v -> %v  with DOWN: one US cent is less than one euro cent\n", c5.From, c5.To)

	fmt.Println("\n== Round trip: the residue is real money and needs a home ==")
	rt := must(RoundTrip(must(Parse("19.99", USD)), usdEur, HalfEven))
	fmt.Printf("%v -> %v -> %v   residue %v\n", rt.Out.From, rt.Out.To, rt.Back.To, rt.Residue)
	rt2 := must(RoundTrip(must(Parse("0.06", USD)), usdEur, HalfEven))
	fmt.Printf("%v -> %v -> %v   residue %v\n", rt2.Out.From, rt2.Out.To, rt2.Back.To, rt2.Residue)
	fmt.Println("Positive residue: the platform gained a cent (FX rounding gain). Negative: it manufactured one (FX rounding loss).")
	fmt.Println("Either way the residue is posted to an FX rounding account, never swallowed.")

	fmt.Println("\n== Cross rates through a base currency ==")
	table := NewRateTable(USD, 300)
	for _, r := range []FxRate{usdEur, usdGbp, usdJpy} {
		if err := table.Put(r); err != nil {
			panic(err)
		}
	}
	eurGbp := must(table.Rate(EUR, GBP, now))
	fmt.Printf("EUR/GBP via USD = %v\n", eurGbp)
	c6 := must(Convert(must(Parse("50.00", EUR)), eurGbp, HalfEven))
	fmt.Printf("%v -> %v  (50 * 7893/9217 = 42.8176...)\n", c6.From, c6.To)
	eurUsd := must(table.Rate(EUR, USD, now))
	fmt.Println("EUR/USD (inverted) = " + eurUsd.ToDecimal(6))

	fmt.Println("\n== Rates go stale ==")
	if _, err := table.Rate(USD, EUR, now+3600); err != nil {
		fmt.Println("rejected:", err)
	}
	if _, err := Convert(must(Parse("1.00", EUR)), usdEur, HalfEven); err != nil {
		fmt.Println("rejected:", err)
	}
}
