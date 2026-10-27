// Lesson: phases/11-payment-rails/07-interchange-and-scheme-fees/docs/en.md
// Interchange, Scheme Fees, and Merchant Pricing: a fee engine that splits one card payment to the cent.
// Run:  go run .
// Test: go test ./...
// Sources: Regulation II (12 CFR 235.3-235.4, US debit cap); EU Regulation 2015/751 (IFR caps); scheme fee schedules.
// Go 1.22, standard library only. Rates in the demo table are illustrative, shaped like published schedules.
package main

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Sentinel errors: Java's exceptions become values a caller tests with errors.Is.
var (
	ErrInvalidArgument = errors.New("invalid argument")                // IllegalArgumentException
	ErrNoRate          = errors.New("no interchange rate")             // NoRateException
	ErrInvariant       = errors.New("fee split does not sum to gross") // IllegalStateException
)

// CardType is the interchange category of the card, as the issuer's BIN declares it (Lesson 04 routes on the same BIN).
type CardType int

const (
	ConsumerDebitRegulated CardType = iota // US: issuer >= $10bn assets, Regulation II cap. EEA: all consumer debit (IFR).
	ConsumerDebitExempt                    // US debit from an issuer under $10bn: exempt from the cap
	ConsumerCredit
	PremiumCredit    // rewards tiers: the issuer funds points from interchange
	CommercialCredit // corporate and purchasing cards: capped neither in the US nor the EEA
)

// CardTypes is Java's CardType.values(), in declaration order.
var CardTypes = []CardType{ConsumerDebitRegulated, ConsumerDebitExempt, ConsumerCredit, PremiumCredit, CommercialCredit}

func (c CardType) String() string {
	return [...]string{"CONSUMER_DEBIT_REGULATED", "CONSUMER_DEBIT_EXEMPT", "CONSUMER_CREDIT", "PREMIUM_CREDIT", "COMMERCIAL_CREDIT"}[c]
}
func (c CardType) IsConsumer() bool { return c != CommercialCredit }
func (c CardType) IsDebit() bool    { return c == ConsumerDebitRegulated || c == ConsumerDebitExempt }

// Region is where the issuer is relative to the merchant's acquirer.
type Region int

const (
	USDomestic    Region = iota // US card, US acquirer
	EEADomestic                 // EEA card, EEA acquirer: IFR caps apply to consumer cards
	InterRegional               // issuer and acquirer in different regions: cross-border scheme fee applies
)

func (r Region) String() string {
	return [...]string{"US_DOMESTIC", "EEA_DOMESTIC", "INTER_REGIONAL"}[r]
}

type Channel int

const (
	CardPresent Channel = iota
	CardNotPresent
)

var Channels = []Channel{CardPresent, CardNotPresent}

func (c Channel) String() string { return [...]string{"CARD_PRESENT", "CARD_NOT_PRESENT"}[c] }

// PPM is the denominator of a proportional rate: 1.80% = 18_000 ppm, 0.05% = 500 ppm.
const PPM int64 = 1_000_000

// Rate is a price with a proportional part and a fixed part, such as 1.80% + 0.10.
// The proportional part is in parts per million so every rate a scheme publishes is an exact integer.
// The fixed part is in minor units of the transaction's currency.
type Rate struct {
	Ppm        int64
	FixedMinor int64
}

func NewRate(ppm, fixedMinor int64) (Rate, error) {
	if ppm < 0 || fixedMinor < 0 {
		return Rate{}, fmt.Errorf("%w: rates are non-negative", ErrInvalidArgument)
	}
	return Rate{ppm, fixedMinor}, nil
}

// RateOf("1.80", 10) is 1.80% + 10 minor units. Parses the percent exactly; never via a float64.
func RateOf(percent string, fixedMinor int64) (Rate, error) {
	whole, frac, _ := strings.Cut(percent, ".")
	if len(frac) > 4 {
		return Rate{}, fmt.Errorf("%w: at most 4 decimals of a percent: %s", ErrInvalidArgument, percent)
	}
	frac += strings.Repeat("0", 4-len(frac))
	w, err := strconv.ParseInt(whole, 10, 64)
	if err != nil {
		return Rate{}, fmt.Errorf("%w: %v", ErrInvalidArgument, err)
	}
	f, err := strconv.ParseInt(frac, 10, 64)
	if err != nil {
		return Rate{}, fmt.Errorf("%w: %v", ErrInvalidArgument, err)
	}
	return NewRate(w*10_000+f, fixedMinor)
}

// ApplyTo is amount x ppm / 1e6 rounded HALF_EVEN, plus the fixed part. Exactly one rounding per fee component.
func (r Rate) ApplyTo(amount Money) (Money, error) {
	proportional, err := amount.Times(r.Ppm, PPM, HalfEven)
	if err != nil {
		return Money{}, err
	}
	return proportional.Plus(OfMinor(r.FixedMinor, amount.Currency))
}

func (r Rate) String() string {
	frac := fmt.Sprintf("%04d", r.Ppm%10_000)
	for len(frac) > 2 && strings.HasSuffix(frac, "0") {
		frac = frac[:len(frac)-1]
	}
	return fmt.Sprintf("%d.%s%% + %d", r.Ppm/10_000, frac, r.FixedMinor)
}

type RateKey struct {
	CardType CardType
	Region   Region
	Channel  Channel
}

// String matches Java's record rendering so the demo's rejection lines agree.
func (k RateKey) String() string {
	return fmt.Sprintf("RateKey[cardType=%v, region=%v, channel=%v]", k.CardType, k.Region, k.Channel)
}

// IFR and Regulation II caps, checked when a rate is loaded.
const (
	// EU Regulation 2015/751, Articles 3 and 4: consumer debit 0.2%, consumer credit 0.3%, no fixed part.
	IFRDebitCapPpm  int64 = 2_000
	IFRCreditCapPpm int64 = 3_000
)

// RegIICap is Regulation II, 12 CFR 235.3 and 235.4: 0.05% + 21 cents, plus 1 cent fraud-prevention adjustment.
var RegIICap = Rate{500, 22}

// InterchangeTable is what the acquirer pays the issuer, published by the scheme, looked up per transaction.
// Regulatory caps are checked when a rate is loaded, so a bad table fails at startup rather than at month end.
type InterchangeTable struct {
	rates map[RateKey]Rate
}

func NewInterchangeTable() *InterchangeTable { return &InterchangeTable{rates: map[RateKey]Rate{}} }

func (t *InterchangeTable) Put(cardType CardType, region Region, channel Channel, rate Rate) error {
	key := RateKey{cardType, region, channel}
	if err := CheckCaps(key, rate); err != nil {
		return err
	}
	t.rates[key] = rate
	return nil
}

// PutBoth puts the same rate for both channels.
func (t *InterchangeTable) PutBoth(cardType CardType, region Region, rate Rate) error {
	for _, ch := range Channels {
		if err := t.Put(cardType, region, ch, rate); err != nil {
			return err
		}
	}
	return nil
}

func (t *InterchangeTable) Lookup(cardType CardType, region Region, channel Channel) (Rate, error) {
	key := RateKey{cardType, region, channel}
	rate, ok := t.rates[key]
	if !ok {
		return Rate{}, fmt.Errorf("%w for %v", ErrNoRate, key)
	}
	return rate, nil
}

func (t *InterchangeTable) Size() int { return len(t.rates) }

func CheckCaps(key RateKey, rate Rate) error {
	if key.Region == EEADomestic && key.CardType.IsConsumer() {
		cap := IFRCreditCapPpm
		if key.CardType.IsDebit() {
			cap = IFRDebitCapPpm
		}
		if rate.Ppm > cap || rate.FixedMinor > 0 {
			return fmt.Errorf("%w: IFR cap exceeded for %v: %v", ErrInvalidArgument, key, rate)
		}
	}
	if key.Region == USDomestic && key.CardType == ConsumerDebitRegulated {
		if rate.Ppm > RegIICap.Ppm || rate.FixedMinor > RegIICap.FixedMinor {
			return fmt.Errorf("%w: Regulation II cap exceeded for %v: %v", ErrInvalidArgument, key, rate)
		}
	}
	return nil
}

// mustRate is for the literal demo table: a typo in a constant is a programming error, not a runtime condition.
func mustRate(percent string, fixedMinor int64) Rate {
	r, err := RateOf(percent, fixedMinor)
	if err != nil {
		panic(err)
	}
	return r
}

// DemoInterchangeTable is illustrative, shaped like published US and EEA schedules. There is no EEA "exempt debit" row.
func DemoInterchangeTable() *InterchangeTable {
	t := NewInterchangeTable()
	us, eea, xb := USDomestic, EEADomestic, InterRegional
	cp, cnp := CardPresent, CardNotPresent
	rows := []struct {
		cardType CardType
		region   Region
		channel  Channel
		both     bool
		rate     Rate
	}{
		{ConsumerDebitRegulated, us, cp, true, mustRate("0.05", 22)},
		{ConsumerDebitExempt, us, cp, false, mustRate("0.80", 15)},
		{ConsumerDebitExempt, us, cnp, false, mustRate("1.65", 15)},
		{ConsumerCredit, us, cp, false, mustRate("1.51", 10)},
		{ConsumerCredit, us, cnp, false, mustRate("1.80", 10)},
		{PremiumCredit, us, cp, false, mustRate("2.10", 10)},
		{PremiumCredit, us, cnp, false, mustRate("2.40", 10)},
		{CommercialCredit, us, cp, false, mustRate("2.50", 10)},
		{CommercialCredit, us, cnp, false, mustRate("2.70", 10)},

		{ConsumerDebitRegulated, eea, cp, true, mustRate("0.20", 0)},
		{ConsumerCredit, eea, cp, true, mustRate("0.30", 0)},
		{PremiumCredit, eea, cp, true, mustRate("0.30", 0)},
		{CommercialCredit, eea, cp, true, mustRate("1.50", 5)},

		{ConsumerDebitRegulated, xb, cp, false, mustRate("0.20", 0)},
		{ConsumerDebitRegulated, xb, cnp, false, mustRate("1.15", 0)},
		{ConsumerDebitExempt, xb, cp, false, mustRate("0.20", 0)},
		{ConsumerDebitExempt, xb, cnp, false, mustRate("1.15", 0)},
		{ConsumerCredit, xb, cp, false, mustRate("0.30", 0)},
		{ConsumerCredit, xb, cnp, false, mustRate("1.50", 0)},
		{PremiumCredit, xb, cp, false, mustRate("0.30", 0)},
		{PremiumCredit, xb, cnp, false, mustRate("1.50", 0)},
		{CommercialCredit, xb, cp, true, mustRate("2.00", 0)},
	}
	for _, row := range rows {
		var err error
		if row.both {
			err = t.PutBoth(row.cardType, row.region, row.rate)
		} else {
			err = t.Put(row.cardType, row.region, row.channel, row.rate)
		}
		if err != nil {
			panic(err) // the demo table is a constant; a cap violation here is a bug in the lesson
		}
	}
	return t
}

// SchemeFees is what the network charges the acquirer per transaction: an assessment on volume, a fixed
// per-transaction processing fee, and a cross-border assessment when issuer and acquirer are in different regions.
type SchemeFees struct {
	Assessment     Rate
	CrossBorderPpm int64
}

func (s SchemeFees) FeeFor(amount Money, region Region) (Money, error) {
	fee, err := s.Assessment.ApplyTo(amount)
	if err != nil {
		return Money{}, err
	}
	if region == InterRegional {
		xb, err := amount.Times(s.CrossBorderPpm, PPM, HalfEven)
		if err != nil {
			return Money{}, err
		}
		return fee.Plus(xb)
	}
	return fee, nil
}

func DemoSchemeFees() SchemeFees { return SchemeFees{mustRate("0.14", 2), 6_000} }

// Pricing is how the acquirer prices the merchant: a sealed interface, closed by the unexported marker method.
type Pricing interface {
	pricing()
}

// IcPlusPlus is interchange ++: pass interchange and scheme fees through at cost, add a disclosed markup.
type IcPlusPlus struct{ Markup Rate }

// Blended is one rate for every card; the acquirer keeps whatever is left after interchange and scheme.
type Blended struct{ MerchantRate Rate }

func (IcPlusPlus) pricing() {}
func (Blended) pricing()    {}

type Transaction struct {
	ID       string
	Amount   Money
	CardType CardType
	Region   Region
	Channel  Channel
}

func NewTransaction(id string, amount Money, cardType CardType, region Region, channel Channel) (Transaction, error) {
	if !amount.IsPositive() {
		return Transaction{}, fmt.Errorf("%w: a purchase amount is positive: %v", ErrInvalidArgument, amount)
	}
	return Transaction{id, amount, cardType, region, channel}, nil
}

// FeeBreakdown is who gets what from one transaction. The invariant, checked on construction:
// interchange + schemeFees + acquirerMargin + merchantNet == gross, to the minor unit.
type FeeBreakdown struct {
	Gross           Money
	InterchangeRate Rate
	Interchange     Money
	SchemeFees      Money
	AcquirerMargin  Money
	MerchantNet     Money
}

func NewFeeBreakdown(gross Money, interchangeRate Rate, interchange, schemeFees, acquirerMargin, merchantNet Money) (FeeBreakdown, error) {
	total, err := Sum(gross.Currency, interchange, schemeFees, acquirerMargin, merchantNet)
	if err != nil {
		return FeeBreakdown{}, err
	}
	if total != gross {
		return FeeBreakdown{}, fmt.Errorf("%w: %v != %v", ErrInvariant, total, gross)
	}
	return FeeBreakdown{gross, interchangeRate, interchange, schemeFees, acquirerMargin, merchantNet}, nil
}

// MerchantDiscount is everything the merchant does not receive. The constructor proved the parts share the
// gross currency and sum to it, so the subtraction cannot fail.
func (f FeeBreakdown) MerchantDiscount() Money {
	d, err := f.Gross.Minus(f.MerchantNet)
	if err != nil {
		panic(err)
	}
	return d
}

// EffectiveRateBps is basis points of gross, rounded HALF_EVEN, for reporting only.
func (f FeeBreakdown) EffectiveRateBps() int64 {
	return DivideRounded(f.MerchantDiscount().MinorUnits*10_000, f.Gross.MinorUnits, HalfEven)
}

type FeeEngine struct {
	table  *InterchangeTable
	scheme SchemeFees
}

func NewFeeEngine(table *InterchangeTable, scheme SchemeFees) *FeeEngine {
	return &FeeEngine{table, scheme}
}

func (e *FeeEngine) Quote(tx Transaction, pricing Pricing) (FeeBreakdown, error) {
	gross := tx.Amount
	icRate, err := e.table.Lookup(tx.CardType, tx.Region, tx.Channel)
	if err != nil {
		return FeeBreakdown{}, err
	}
	interchange, err := icRate.ApplyTo(gross)
	if err != nil {
		return FeeBreakdown{}, err
	}
	schemeFees, err := e.scheme.FeeFor(gross, tx.Region)
	if err != nil {
		return FeeBreakdown{}, err
	}
	var merchantDiscount Money
	switch p := pricing.(type) {
	case IcPlusPlus:
		markup, err := p.Markup.ApplyTo(gross)
		if err != nil {
			return FeeBreakdown{}, err
		}
		if merchantDiscount, err = Sum(gross.Currency, interchange, schemeFees, markup); err != nil {
			return FeeBreakdown{}, err
		}
	case Blended:
		if merchantDiscount, err = p.MerchantRate.ApplyTo(gross); err != nil {
			return FeeBreakdown{}, err
		}
	default:
		return FeeBreakdown{}, fmt.Errorf("%w: unknown pricing %T", ErrInvalidArgument, pricing)
	}
	acquirerMargin, err := merchantDiscount.Minus(interchange)
	if err != nil {
		return FeeBreakdown{}, err
	}
	if acquirerMargin, err = acquirerMargin.Minus(schemeFees); err != nil {
		return FeeBreakdown{}, err
	}
	merchantNet, err := gross.Minus(merchantDiscount)
	if err != nil {
		return FeeBreakdown{}, err
	}
	return NewFeeBreakdown(gross, icRate, interchange, schemeFees, acquirerMargin, merchantNet)
}

// Attribute a fee billed in aggregate (a monthly scheme fee, a statement fee) back to the transactions that
// caused it, in proportion to their amounts. Largest-remainder allocation: the parts sum exactly to the fee.
func Attribute(aggregateFee Money, txs []Transaction) ([]Money, error) {
	weights := make([]int64, len(txs))
	for i, tx := range txs {
		if tx.Amount.Currency != aggregateFee.Currency {
			return nil, fmt.Errorf("%w: %s vs %s", ErrCurrencyMismatch, tx.Amount.Currency.Code, aggregateFee.Currency.Code)
		}
		weights[i] = tx.Amount.MinorUnits
	}
	return aggregateFee.Allocate(weights...)
}

// Total sums one month of quotes into the totals an IC++ merchant statement shows.
func Total(quotes []FeeBreakdown) (FeeBreakdown, error) {
	c := quotes[0].Gross.Currency
	g, ic, sf, am, net := OfMinor(0, c), OfMinor(0, c), OfMinor(0, c), OfMinor(0, c), OfMinor(0, c)
	for _, q := range quotes {
		var err error
		if g, err = g.Plus(q.Gross); err != nil {
			return FeeBreakdown{}, err
		}
		if ic, err = ic.Plus(q.Interchange); err != nil {
			return FeeBreakdown{}, err
		}
		if sf, err = sf.Plus(q.SchemeFees); err != nil {
			return FeeBreakdown{}, err
		}
		if am, err = am.Plus(q.AcquirerMargin); err != nil {
			return FeeBreakdown{}, err
		}
		if net, err = net.Plus(q.MerchantNet); err != nil {
			return FeeBreakdown{}, err
		}
	}
	return NewFeeBreakdown(g, Rate{0, 0}, ic, sf, am, net)
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

// reason strips the sentinel prefix so the demo prints the same message Java's getMessage() gives.
func reason(err error, sentinel error) string {
	return strings.TrimPrefix(err.Error(), sentinel.Error()+": ")
}

func printBreakdown(label string, f FeeBreakdown) {
	fmt.Printf("%-34s gross %-11s IC %-9s scheme %-8s acquirer %-9s merchant %-11s (%d bps, IC rate %s)\n",
		label, f.Gross, f.Interchange, f.SchemeFees, f.AcquirerMargin, f.MerchantNet, f.EffectiveRateBps(), f.InterchangeRate)
}

func main() {
	engine := NewFeeEngine(DemoInterchangeTable(), DemoSchemeFees())
	icpp := IcPlusPlus{mustRate("0.20", 5)}
	blended := Blended{mustRate("2.90", 30)}
	hundred := must(Parse("100.00", USD))
	tx := func(id string, amount Money, t CardType, r Region, ch Channel) Transaction {
		return must(NewTransaction(id, amount, t, r, ch))
	}

	fmt.Println("== Who gets paid on 100.00 USD (US consumer credit, card-not-present, IC++) ==")
	f := must(engine.Quote(tx("t1", hundred, ConsumerCredit, USDomestic, CardNotPresent), icpp))
	fmt.Println("issuer   (interchange 1.80% + 0.10)   " + f.Interchange.String())
	fmt.Println("network  (assessment 0.14% + 0.02)    " + f.SchemeFees.String())
	fmt.Println("acquirer (markup 0.20% + 0.05)        " + f.AcquirerMargin.String())
	fmt.Println("merchant (net settlement)             " + f.MerchantNet.String())
	fmt.Println("sum                                   " + must(Sum(USD, f.Interchange, f.SchemeFees, f.AcquirerMargin, f.MerchantNet)).String())

	fmt.Println("\n== The same 100.00 USD across card types, CNP ==")
	for _, t := range CardTypes {
		printBreakdown(t.String()+" IC++", must(engine.Quote(tx("t", hundred, t, USDomestic, CardNotPresent), icpp)))
	}

	fmt.Println("\n== Blended 2.90% + 0.30: the merchant pays the same, the acquirer's margin moves ==")
	for _, t := range CardTypes {
		printBreakdown(t.String()+" blended", must(engine.Quote(tx("t", hundred, t, USDomestic, CardNotPresent), blended)))
	}

	fmt.Println("\n== Small tickets: 2.00 USD regulated debit ==")
	two := must(Parse("2.00", USD))
	printBreakdown("regulated debit IC++", must(engine.Quote(tx("t", two, ConsumerDebitRegulated, USDomestic, CardPresent), icpp)))
	printBreakdown("regulated debit blended", must(engine.Quote(tx("t", two, ConsumerDebitRegulated, USDomestic, CardPresent), blended)))

	fmt.Println("\n== EEA: IFR caps and cross-border ==")
	eur := must(Parse("100.00", EUR))
	printBreakdown("EEA premium credit CP", must(engine.Quote(tx("t", eur, PremiumCredit, EEADomestic, CardPresent), icpp)))
	printBreakdown("inter-regional credit CNP", must(engine.Quote(tx("t", eur, ConsumerCredit, InterRegional, CardNotPresent), icpp)))
	if err := NewInterchangeTable().Put(ConsumerCredit, EEADomestic, CardPresent, mustRate("0.35", 0)); errors.Is(err, ErrInvalidArgument) {
		fmt.Println("rejected: " + reason(err, ErrInvalidArgument))
	}
	if _, err := engine.Quote(tx("t", eur, ConsumerDebitExempt, EEADomestic, CardPresent), icpp); errors.Is(err, ErrNoRate) {
		fmt.Println("rejected: " + err.Error())
	}

	fmt.Println("\n== Rounding: 37.77 USD consumer credit CNP ==")
	odd := must(engine.Quote(tx("t", must(Parse("37.77", USD)), ConsumerCredit, USDomestic, CardNotPresent), icpp))
	fmt.Println("interchange = 37.77 x 1.80% = 0.67986 -> 0.68, + 0.10 = " + odd.Interchange.String())
	printBreakdown("37.77", odd)

	fmt.Println("\n== Attributing a 25.00 USD monthly scheme fee to three transactions ==")
	month := []Transaction{
		tx("a", must(Parse("100.00", USD)), ConsumerCredit, USDomestic, CardPresent),
		tx("b", must(Parse("37.77", USD)), PremiumCredit, USDomestic, CardNotPresent),
		tx("c", must(Parse("2.00", USD)), ConsumerDebitRegulated, USDomestic, CardPresent),
	}
	parts := must(Attribute(must(Parse("25.00", USD)), month))
	for i, p := range parts {
		fmt.Printf("%s %v -> %v\n", month[i].ID, month[i].Amount, p)
	}
	fmt.Println("sum = " + must(Sum(USD, parts...)).String())
	quotes := make([]FeeBreakdown, 0, len(month))
	for _, t := range month {
		quotes = append(quotes, must(engine.Quote(t, icpp)))
	}
	printBreakdown("month total IC++", must(Total(quotes)))
}
