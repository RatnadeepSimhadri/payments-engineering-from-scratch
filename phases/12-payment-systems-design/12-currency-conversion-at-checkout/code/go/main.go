// Lesson: phases/12-payment-systems-design/12-currency-conversion-at-checkout/docs/en.md
// Currency Conversion at Checkout and DCC
// Run:  go run .
// Test: go test ./...
// Sources: ISO 4217 (currency exponents); Regulation (EU) 2019/518 (currency conversion disclosure as a
// markup over the ECB reference rate); ECB euro foreign exchange reference rates. Go 1.22+, stdlib only.
package main

import (
	"errors"
	"fmt"
	"math/big"
	"strings"
)

var (
	// ErrInvalidArgument is Java's IllegalArgumentException: bad rates, unknown quotes, missing market rates.
	ErrInvalidArgument = errors.New("invalid argument")
	// ErrZeroDenominator is Java's ArithmeticException from the Rational constructor.
	ErrZeroDenominator = errors.New("zero denominator")
	// ErrQuoteExpired is returned when a confirmation arrives at or after the quote's expiry.
	ErrQuoteExpired = errors.New("quote expired")
)

// Rounding modes Money does not need but FX does: Java's RoundingMode.CEILING and FLOOR.
const (
	Ceiling Rounding = iota + 4 // toward positive infinity
	Floor                       // toward negative infinity
)

// ---------------------------------------------------------------------------------------------
// Exact rationals. A rate is a ratio; rounding happens once, on the final amount, never on the rate.
// Rational wraps *big.Rat and never mutates a shared value: every operation allocates a result.
// ---------------------------------------------------------------------------------------------

type Rational struct{ r *big.Rat }

var ONE = Of(1, 1)

// Of builds num/den in lowest terms. A zero denominator panics with ErrZeroDenominator, as the Java
// constructor throws: it is a programming error, not a runtime condition.
func Of(num, den int64) Rational {
	if den == 0 {
		panic(ErrZeroDenominator)
	}
	return Rational{new(big.Rat).SetFrac64(num, den)}
}

func ofBig(num, den *big.Int) Rational {
	if den.Sign() == 0 {
		panic(ErrZeroDenominator)
	}
	return Rational{new(big.Rat).SetFrac(num, den)}
}

// ParseRational is an exact parse of a decimal string: "1.0850" is 10850/10000 = 217/200.
func ParseRational(decimal string) (Rational, error) {
	r, ok := new(big.Rat).SetString(strings.TrimSpace(decimal))
	if !ok {
		return Rational{}, fmt.Errorf("%w: not a decimal: %q", ErrInvalidArgument, decimal)
	}
	return Rational{r}, nil
}

// Bps is basis points as a fraction: 350 bps = 350/10000.
func Bps(basisPoints int64) Rational { return Of(basisPoints, 10_000) }

func Pow10(exponent int) Rational {
	p := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(abs(exponent))), nil)
	if exponent >= 0 {
		return ofBig(p, big.NewInt(1))
	}
	return ofBig(big.NewInt(1), p)
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func (a Rational) Num() *big.Int { return new(big.Int).Set(a.r.Num()) }
func (a Rational) Den() *big.Int { return new(big.Int).Set(a.r.Denom()) }

func (a Rational) Times(o Rational) Rational { return Rational{new(big.Rat).Mul(a.r, o.r)} }
func (a Rational) DividedBy(o Rational) Rational {
	return ofBig(new(big.Int).Mul(a.r.Num(), o.r.Denom()), new(big.Int).Mul(a.r.Denom(), o.r.Num()))
}
func (a Rational) Plus(o Rational) Rational  { return Rational{new(big.Rat).Add(a.r, o.r)} }
func (a Rational) Minus(o Rational) Rational { return Rational{new(big.Rat).Sub(a.r, o.r)} }
func (a Rational) Inverse() Rational         { return ofBig(a.r.Denom(), a.r.Num()) }
func (a Rational) Signum() int               { return a.r.Sign() }
func (a Rational) Compare(o Rational) int    { return a.r.Cmp(o.r) }
func (a Rational) Equal(o Rational) bool     { return a.r.Cmp(o.r) == 0 }
func (a Rational) String() string            { return a.r.String() }

// roundBig divides num by den (den > 0) to an integer with an explicit rounding mode: Java's
// BigDecimal.divide(..., 0, mode). Go has no rounding-mode enum, so the cases are spelled out.
func roundBig(num, den *big.Int, mode Rounding) *big.Int {
	q, rem := new(big.Int).QuoRem(num, den, new(big.Int)) // truncated toward zero, rem has num's sign
	if rem.Sign() == 0 {
		return q
	}
	negative := num.Sign() < 0
	towardZero, awayFromZero := q, new(big.Int)
	if negative {
		awayFromZero.Sub(q, big.NewInt(1))
	} else {
		awayFromZero.Add(q, big.NewInt(1))
	}
	twice := new(big.Int).Abs(rem)
	twice.Lsh(twice, 1)
	cmp := twice.Cmp(den) // <0 below half, 0 exactly half, >0 above half
	switch mode {
	case Down:
		return towardZero
	case Up:
		return awayFromZero
	case Ceiling:
		if negative {
			return towardZero
		}
		return awayFromZero
	case Floor:
		if negative {
			return awayFromZero
		}
		return towardZero
	case HalfUp:
		if cmp >= 0 {
			return awayFromZero
		}
		return towardZero
	default: // HalfEven
		if cmp > 0 || (cmp == 0 && q.Bit(0) == 1) {
			return awayFromZero
		}
		return towardZero
	}
}

// RoundToInt64 rounds to an integer with the given mode; ErrOverflow where Java's longValueExact throws.
func (a Rational) RoundToInt64(mode Rounding) (int64, error) {
	v := roundBig(a.r.Num(), a.r.Denom(), mode)
	if !v.IsInt64() {
		return 0, ErrOverflow
	}
	return v.Int64(), nil
}

// ToDecimal renders the value rounded HALF_EVEN to scale decimals, like BigDecimal.toPlainString().
func (a Rational) ToDecimal(scale int) string {
	scaled := new(big.Int).Mul(a.r.Num(), new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(scale)), nil))
	v := roundBig(scaled, a.r.Denom(), HalfEven)
	negative := v.Sign() < 0
	digits := new(big.Int).Abs(v).String()
	if scale > 0 {
		if len(digits) <= scale {
			digits = strings.Repeat("0", scale-len(digits)+1) + digits
		}
		digits = digits[:len(digits)-scale] + "." + digits[len(digits)-scale:]
	}
	if negative {
		digits = "-" + digits
	}
	return digits
}

// FxRate: 1 unit of From = Rate units of To, in major units.
type FxRate struct {
	From Currency
	To   Currency
	Rate Rational
}

func NewFxRate(from, to Currency, rate Rational) (FxRate, error) {
	if rate.Signum() <= 0 {
		return FxRate{}, fmt.Errorf("%w: rate must be positive", ErrInvalidArgument)
	}
	if from == to && rate.Compare(ONE) != 0 {
		return FxRate{}, fmt.Errorf("%w: same-currency rate must be 1", ErrInvalidArgument)
	}
	return FxRate{from, to, rate}, nil
}

// Inverse cannot fail: the inverse of a positive rate is positive.
func (f FxRate) Inverse() FxRate { return FxRate{f.To, f.From, f.Rate.Inverse()} }

// AdjustedBy multiplies the rate by (1 + bps/10000); a negative bps scales it down.
func (f FxRate) AdjustedBy(basisPoints int64) (FxRate, error) {
	return NewFxRate(f.From, f.To, f.Rate.Times(ONE.Plus(Bps(basisPoints))))
}

// Convert is the exact conversion in minor units, one rounding at the end with an explicit mode.
func Convert(amount Money, rate FxRate, mode Rounding) (Money, error) {
	if amount.Currency != rate.From {
		return Money{}, fmt.Errorf("%w: cannot combine %s with %s", ErrCurrencyMismatch, amount.Currency.Code, rate.From.Code)
	}
	minor := Of(amount.MinorUnits, 1).Times(rate.Rate).Times(Pow10(rate.To.Exponent - rate.From.Exponent))
	rounded, err := minor.RoundToInt64(mode)
	if err != nil {
		return Money{}, err
	}
	return OfMinor(rounded, rate.To), nil
}

// ConvertWithRoundedRate is the anti-pattern: round the rate to a few decimals first, then convert.
func ConvertWithRoundedRate(amount Money, rate FxRate, rateDecimals int, mode Rounding) (Money, error) {
	rounded, err := ParseRational(rate.Rate.ToDecimal(rateDecimals))
	if err != nil {
		return Money{}, err
	}
	roundedRate, err := NewFxRate(rate.From, rate.To, rounded)
	if err != nil {
		return Money{}, err
	}
	return Convert(amount, roundedRate, mode)
}

// ---------------------------------------------------------------------------------------------
// Time and market rates are injected.
// ---------------------------------------------------------------------------------------------

type Clock interface{ NowMillis() int64 }

type FakeClock struct{ now int64 }

func NewFakeClock(start int64) *FakeClock   { return &FakeClock{start} }
func (c *FakeClock) NowMillis() int64       { return c.now }
func (c *FakeClock) AdvanceMillis(ms int64) { c.now += ms }

type RateSource interface {
	Mid(from, to Currency) (Rational, error)
}

// FakeRateSource holds mid-market rates set by hand; the inverse pair is derived exactly.
type FakeRateSource struct{ rates map[string]Rational }

func NewFakeRateSource() *FakeRateSource { return &FakeRateSource{map[string]Rational{}} }

func (s *FakeRateSource) Set(from, to Currency, decimal string) error {
	r, err := ParseRational(decimal)
	if err != nil {
		return err
	}
	s.rates[from.Code+to.Code] = r
	s.rates[to.Code+from.Code] = r.Inverse()
	return nil
}

func (s *FakeRateSource) Mid(from, to Currency) (Rational, error) {
	if from == to {
		return ONE, nil
	}
	r, ok := s.rates[from.Code+to.Code]
	if !ok {
		return Rational{}, fmt.Errorf("%w: no rate %s/%s", ErrInvalidArgument, from.Code, to.Code)
	}
	return r, nil
}

// ---------------------------------------------------------------------------------------------
// Presentment pricing with a rate lock: the merchant is promised a settlement amount; the shopper
// pays a presentment amount; whoever locked the rate owns the movement until settlement.
// ---------------------------------------------------------------------------------------------

// RateQuote is a locked customer rate settlement -> presentment, valid on [IssuedAt, ExpiresAt).
type RateQuote struct {
	ID              string
	CustomerRate    FxRate
	Mid             Rational
	SpreadBps       int64
	IssuedAtMillis  int64
	ExpiresAtMillis int64
}

func (q RateQuote) ValidAt(now int64) bool { return now >= q.IssuedAtMillis && now < q.ExpiresAtMillis }
func (q RateQuote) Settlement() Currency   { return q.CustomerRate.From }
func (q RateQuote) Presentment() Currency  { return q.CustomerRate.To }

type LockedCharge struct {
	Quote             RateQuote
	SettlementAmount  Money
	PresentmentAmount Money
}

type SettlementResult struct {
	MerchantReceives Money
	PspProceeds      Money
	PspFxPnl         Money
}

type FxDesk struct {
	clock      Clock
	rates      RateSource
	spreadBps  int64
	lockMillis int64
	quotes     map[string]RateQuote
	seq        int
}

func NewFxDesk(clock Clock, rates RateSource, spreadBps, lockMillis int64) (*FxDesk, error) {
	if spreadBps < 0 || lockMillis <= 0 {
		return nil, fmt.Errorf("%w: spread >= 0 and lock > 0 required", ErrInvalidArgument)
	}
	return &FxDesk{clock: clock, rates: rates, spreadBps: spreadBps, lockMillis: lockMillis, quotes: map[string]RateQuote{}}, nil
}

// Quote locks a rate now. The shopper's rate is mid plus the spread, so the shopper pays more presentment currency.
func (d *FxDesk) Quote(settlement, presentment Currency) (RateQuote, error) {
	now := d.clock.NowMillis()
	mid, err := d.rates.Mid(settlement, presentment)
	if err != nil {
		return RateQuote{}, err
	}
	base, err := NewFxRate(settlement, presentment, mid)
	if err != nil {
		return RateQuote{}, err
	}
	customer, err := base.AdjustedBy(d.spreadBps)
	if err != nil {
		return RateQuote{}, err
	}
	d.seq++
	q := RateQuote{fmt.Sprintf("fxq_%04d", d.seq), customer, mid, d.spreadBps, now, now + d.lockMillis}
	d.quotes[q.ID] = q
	return q, nil
}

// PresentmentPrice is the price to display. Rounded up so the shopper never pays less than the locked rate implies.
func PresentmentPrice(settlementPrice Money, q RateQuote) (Money, error) {
	return Convert(settlementPrice, q.CustomerRate, Ceiling)
}

// Confirm is checkout confirmation: only inside the lock window. After it, the shopper must see a new price.
func (d *FxDesk) Confirm(quoteID string, settlementPrice Money) (LockedCharge, error) {
	q, ok := d.quotes[quoteID]
	if !ok {
		return LockedCharge{}, fmt.Errorf("%w: unknown quote %s", ErrInvalidArgument, quoteID)
	}
	if settlementPrice.Currency != q.Settlement() {
		return LockedCharge{}, fmt.Errorf("%w: cannot combine %s with %s", ErrCurrencyMismatch, settlementPrice.Currency.Code, q.Settlement().Code)
	}
	now := d.clock.NowMillis()
	if !q.ValidAt(now) {
		return LockedCharge{}, fmt.Errorf("%w: quote %s expired at %d, now %d", ErrQuoteExpired, q.ID, q.ExpiresAtMillis, now)
	}
	presentment, err := PresentmentPrice(settlementPrice, q)
	if err != nil {
		return LockedCharge{}, err
	}
	return LockedCharge{q, settlementPrice, presentment}, nil
}

// Settle: days later the PSP sells the presentment currency at the then-market mid. The merchant gets the
// promised amount regardless; the difference is the PSP's FX P&L (spread minus market movement).
func Settle(charge LockedCharge, marketMidPresentmentToSettlement Rational) (SettlementResult, error) {
	market, err := NewFxRate(charge.Quote.Presentment(), charge.Quote.Settlement(), marketMidPresentmentToSettlement)
	if err != nil {
		return SettlementResult{}, err
	}
	proceeds, err := Convert(charge.PresentmentAmount, market, HalfEven)
	if err != nil {
		return SettlementResult{}, err
	}
	pnl, err := proceeds.Minus(charge.SettlementAmount)
	if err != nil {
		return SettlementResult{}, err
	}
	return SettlementResult{charge.SettlementAmount, proceeds, pnl}, nil
}

// BreakEvenRate is the settlement-time rate at which the PSP's P&L is exactly zero.
func BreakEvenRate(charge LockedCharge) Rational {
	return Of(charge.SettlementAmount.MinorUnits, 1).
		DividedBy(Of(charge.PresentmentAmount.MinorUnits, 1)).
		Times(Pow10(charge.Quote.Presentment().Exponent - charge.Quote.Settlement().Exponent))
}

// ---------------------------------------------------------------------------------------------
// Dynamic currency conversion: the cardholder is offered their card's currency at a disclosed markup.
// ---------------------------------------------------------------------------------------------

type DccOffer struct {
	MerchantAmount Money
	CardAmount     Money
	DccRate        FxRate
	ReferenceRate  Rational
	MarkupBps      int64
}

// MarkupPercent is the markup over the reference rate, as an exact percentage: (dcc / reference - 1) * 100.
func (o DccOffer) MarkupPercent() Rational {
	return o.DccRate.Rate.DividedBy(o.ReferenceRate).Minus(ONE).Times(Of(100, 1))
}

// atReference converts the merchant amount at the reference rate. The currencies were validated by
// Offer, so the only error paths are programming errors.
func (o DccOffer) atReference() Money {
	rate := must(NewFxRate(o.MerchantAmount.Currency, o.CardAmount.Currency, o.ReferenceRate))
	return must(Convert(o.MerchantAmount, rate, HalfEven))
}

// EffectiveMarkupPercent is what the cardholder actually pays over the reference conversion, after rounding the amount.
func (o DccOffer) EffectiveMarkupPercent() Rational {
	return Of(o.CardAmount.MinorUnits, o.atReference().MinorUnits).Minus(ONE).Times(Of(100, 1))
}

// GrossMargin is the DCC gross margin in card currency: cardholder amount minus the reference conversion.
func (o DccOffer) GrossMargin() Money { return must(o.CardAmount.Minus(o.atReference())) }

func (o DccOffer) Disclosure() string {
	return "Pay " + o.CardAmount.String() + " (rate 1 " + o.MerchantAmount.Currency.Code + " = " + o.DccRate.Rate.ToDecimal(6) + " " +
		o.CardAmount.Currency.Code + ", including a " + o.MarkupPercent().ToDecimal(2) +
		"% markup over the ECB reference rate " + o.ReferenceRate.ToDecimal(4) + ") or pay " + o.MerchantAmount.String() +
		" and let your card issuer convert."
}

func Offer(merchantAmount Money, cardCurrency Currency, referenceRate Rational, markupBps int64) (DccOffer, error) {
	if merchantAmount.Currency == cardCurrency {
		return DccOffer{}, fmt.Errorf("%w: DCC needs a different card currency", ErrInvalidArgument)
	}
	if markupBps < 0 {
		return DccOffer{}, fmt.Errorf("%w: markup must be >= 0", ErrInvalidArgument)
	}
	base, err := NewFxRate(merchantAmount.Currency, cardCurrency, referenceRate)
	if err != nil {
		return DccOffer{}, err
	}
	rate, err := base.AdjustedBy(markupBps)
	if err != nil {
		return DccOffer{}, err
	}
	cardAmount, err := Convert(merchantAmount, rate, HalfEven)
	if err != nil {
		return DccOffer{}, err
	}
	return DccOffer{merchantAmount, cardAmount, rate, referenceRate, markupBps}, nil
}

// IssuerConversion is what declining DCC costs: the network converts at its rate and the issuer may add a foreign transaction fee.
func IssuerConversion(merchantAmount Money, cardCurrency Currency, networkRate Rational, issuerFeeBps int64) (Money, error) {
	rate, err := NewFxRate(merchantAmount.Currency, cardCurrency, networkRate)
	if err != nil {
		return Money{}, err
	}
	converted, err := Convert(merchantAmount, rate, HalfEven)
	if err != nil {
		return Money{}, err
	}
	feeMinor, err := Of(converted.MinorUnits, 1).Times(Bps(issuerFeeBps)).RoundToInt64(HalfEven)
	if err != nil {
		return Money{}, err
	}
	return converted.Plus(OfMinor(feeMinor, cardCurrency))
}

// ---------------------------------------------------------------------------------------------
// Demo.
// ---------------------------------------------------------------------------------------------

const (
	T0             int64 = 1_760_000_000_000
	FifteenMinutes int64 = 15 * 60 * 1000
)

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func mustOK(err error) {
	if err != nil {
		panic(err)
	}
}

// rat is for literal rates in demos and tests; a typo there is a bug.
func rat(decimal string) Rational { return must(ParseRational(decimal)) }

func main() {
	clock := NewFakeClock(T0)
	market := NewFakeRateSource()
	mustOK(market.Set(EUR, USD, "1.0850"))
	desk := must(NewFxDesk(clock, market, 200, FifteenMinutes))
	price := mustParse("120.00", USD)

	fmt.Println("== Presentment vs settlement: a US merchant shows EUR to a German shopper ==")
	q := must(desk.Quote(USD, EUR))
	fmt.Printf("mid USD->EUR      = %v = %s\n", q.Mid, q.Mid.ToDecimal(10))
	fmt.Printf("customer USD->EUR = %v = %s (mid + 2.00%%)\n", q.CustomerRate.Rate, q.CustomerRate.Rate.ToDecimal(10))
	fmt.Println("lock window       = [t0, t0 + 15 min)")
	clock.AdvanceMillis(9 * 60 * 1000)
	charge := must(desk.Confirm(q.ID, price))
	fmt.Printf("confirmed at +9m: shopper pays %v, merchant is promised %v\n", charge.PresentmentAmount, charge.SettlementAmount)

	fmt.Println("\n== Who owns the FX risk: the PSP, from lock to settlement ==")
	for _, eurUsd := range []string{"1.0850", "1.0700", "1.0600", "1.1000"} {
		r := must(Settle(charge, rat(eurUsd)))
		fmt.Printf("EUR/USD at settlement %s: PSP sells %v for %v, merchant gets %v, PSP FX P&L %v\n",
			eurUsd, charge.PresentmentAmount, r.PspProceeds, r.MerchantReceives, r.PspFxPnl)
	}
	fmt.Println("break-even EUR/USD = " + BreakEvenRate(charge).ToDecimal(6))

	fmt.Println("\n== The lock expires: a late confirmation must re-quote ==")
	clock.AdvanceMillis(6 * 60 * 1000)
	if _, err := desk.Confirm(q.ID, price); errors.Is(err, ErrQuoteExpired) {
		fmt.Println("at +15m: " + strings.TrimPrefix(err.Error(), ErrQuoteExpired.Error()+": "))
	}
	mustOK(market.Set(EUR, USD, "1.0600"))
	fresh := must(desk.Quote(USD, EUR))
	fmt.Printf("re-quoted at EUR/USD 1.0600: shopper now pays %v\n", must(PresentmentPrice(price, fresh)))

	fmt.Println("\n== Round the amount once, never the rate ==")
	big := mustParse("50000.00", USD)
	fmt.Printf("exact rate:          %v\n", must(Convert(big, q.CustomerRate, HalfEven)))
	fmt.Printf("rate rounded to 4dp: %v  (rate %s)\n", must(ConvertWithRoundedRate(big, q.CustomerRate, 4, HalfEven)), q.CustomerRate.Rate.ToDecimal(4))
	fmt.Printf("USD 120.00 -> JPY at 152.37: %v\n", must(Convert(price, must(NewFxRate(USD, JPY, rat("152.37"))), HalfEven)))

	fmt.Println("\n== DCC: a UK card at a German merchant, EUR 100.00 ==")
	eur100 := mustParse("100.00", EUR)
	offer := must(Offer(eur100, GBP, rat("0.8550"), 350))
	fmt.Println(offer.Disclosure())
	fmt.Printf("disclosed markup  = %s%% (exact: %v)\n", offer.MarkupPercent().ToDecimal(4), offer.MarkupPercent())
	fmt.Printf("effective markup  = %s%% after rounding the amount\n", offer.EffectiveMarkupPercent().ToDecimal(4))
	split := must(offer.GrossMargin().Allocate(3000, 7000))
	fmt.Printf("DCC gross margin  = %v: merchant rebate %v, DCC provider %v\n", offer.GrossMargin(), split[0], split[1])
	fmt.Printf("decline DCC, network 0.8560, no issuer fee:   %v\n", must(IssuerConversion(eur100, GBP, rat("0.8560"), 0)))
	fmt.Printf("decline DCC, network 0.8560, 2.99%% issuer fee: %v\n", must(IssuerConversion(eur100, GBP, rat("0.8560"), 299)))
}
