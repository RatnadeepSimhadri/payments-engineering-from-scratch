// Project: projects/ledger-engine — stage 1 starter (Go): the Money value type.
// Docs: projects/ledger-engine/stages/01-money-and-accounts/docs/en.md
// Run:  go run .
// Grade: python3 scripts/project_test.py ledger-engine --lang go --stage 1 --path <workspace>
// Same design as phases/10-money-and-ledgers/01-representing-money/code/go. Currency, Rounding, the sentinel
// errors, the Money struct, addChecked/mulChecked, and the small helpers are given; Parse, Plus, Minus,
// Times, DivideRounded, and Allocate are yours to implement. Every stub panics until you replace it.
package main

import (
	"errors"
	"fmt"
	"math"
	"strings"
)

// Currency is an ISO 4217 currency: a three-letter code and the number of decimals in its minor unit.
type Currency struct {
	Code     string
	Exponent int
}

var (
	USD = Currency{"USD", 2}
	EUR = Currency{"EUR", 2}
	GBP = Currency{"GBP", 2}
	INR = Currency{"INR", 2}
	JPY = Currency{"JPY", 0}
	KWD = Currency{"KWD", 3}
)

// NewCurrency validates the code and exponent; the package-level values are known-good.
func NewCurrency(code string, exponent int) (Currency, error) {
	if len(code) != 3 || strings.Trim(code, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") != "" {
		return Currency{}, fmt.Errorf("currency code must be three uppercase letters: %q", code)
	}
	if exponent < 0 || exponent > 4 {
		return Currency{}, fmt.Errorf("exponent must be 0..4: %d", exponent)
	}
	return Currency{code, exponent}, nil
}

// MinorUnitsPerMajor is 10^exponent: 100 for USD, 1 for JPY, 1000 for KWD.
func (c Currency) MinorUnitsPerMajor() int64 {
	scale := int64(1)
	for i := 0; i < c.Exponent; i++ {
		scale *= 10
	}
	return scale
}

// Rounding says how to resolve a fractional minor unit. Named after the behaviour.
type Rounding int

const (
	HalfEven Rounding = iota // banker's rounding: ties go to the even neighbour; statistically unbiased
	HalfUp                   // ties go away from zero; what most people mean by "round"
	Down                     // truncate toward zero; what you owe never rounds up against the payer
	Up                       // away from zero; fees that must cover a cost
)

// Sentinel errors. Java's exception classes become values a caller tests with errors.Is.
var (
	ErrCurrencyMismatch = errors.New("currency mismatch")            // CurrencyMismatchException
	ErrOverflow         = errors.New("int64 overflow")               // ArithmeticException from Math.addExact
	ErrExcessPrecision  = errors.New("too many decimals")            // ArithmeticException from parse
	ErrNotDecimal       = errors.New("not a decimal amount")         // NumberFormatException from parse
	ErrBadDenominator   = errors.New("denominator must be positive") // ArithmeticException from times
	ErrBadRatios        = errors.New("bad allocation ratios")        // IllegalArgumentException from allocate
)

// Money is an exact amount of one currency stored as a signed count of minor units.
// Values are immutable: every method returns a new Money. Two Money values are == only when
// both the minor units and the currency match (struct equality, like a Java record).
type Money struct {
	MinorUnits int64
	Currency   Currency
}

func OfMinor(minorUnits int64, currency Currency) Money {
	return Money{minorUnits, currency}
}

// Parse reads "12.34", "-0.5", ".99", "1200" exactly and rejects more decimals than the currency allows.
func Parse(text string, currency Currency) (Money, error) {
	panic("not implemented: TODO stage 01 Money.Parse: sign, digits, optional fraction; ErrExcessPrecision / ErrNotDecimal / ErrOverflow")
}

func allDigits(s string) bool {
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	return len(s) > 0
}

// parseInt64 reads a non-empty string of digits with overflow detection (Long.parseLong).
func parseInt64(digits string) (int64, error) {
	var value int64
	for _, ch := range digits {
		next, err := mulChecked(value, 10)
		if err != nil {
			return 0, err
		}
		if next, err = addChecked(next, int64(ch-'0')); err != nil {
			return 0, err
		}
		value = next
	}
	return value, nil
}

func (m Money) requireSameCurrency(other Money) error {
	if m.Currency != other.Currency {
		return fmt.Errorf("%w: cannot combine %s with %s", ErrCurrencyMismatch, m.Currency.Code, other.Currency.Code)
	}
	return nil
}

// addChecked is Math.addExact: int64 wraps silently in Go, so overflow is detected by hand.
func addChecked(a, b int64) (int64, error) {
	sum := a + b
	if (a > 0 && b > 0 && sum < 0) || (a < 0 && b < 0 && sum >= 0) {
		return 0, ErrOverflow
	}
	return sum, nil
}

// mulChecked is Math.multiplyExact.
func mulChecked(a, b int64) (int64, error) {
	if a == 0 || b == 0 {
		return 0, nil
	}
	product := a * b
	if product/b != a || (a == -1 && b == math.MinInt64) || (b == -1 && a == math.MinInt64) {
		return 0, ErrOverflow
	}
	return product, nil
}

func (m Money) Plus(other Money) (Money, error) {
	panic("not implemented: TODO stage 01 Money.Plus: requireSameCurrency then addChecked")
}

func (m Money) Minus(other Money) (Money, error) {
	panic("not implemented: TODO stage 01 Money.Minus: requireSameCurrency then addChecked with the negation")
}

func (m Money) Negate() Money    { return Money{-m.MinorUnits, m.Currency} }
func (m Money) IsZero() bool     { return m.MinorUnits == 0 }
func (m Money) IsNegative() bool { return m.MinorUnits < 0 }
func (m Money) IsPositive() bool { return m.MinorUnits > 0 }

func (m Money) Abs() Money {
	if m.MinorUnits < 0 {
		return m.Negate()
	}
	return m
}

// Times multiplies by an exact rational (2.9% is 29/1000) and rounds once, the way you asked.
func (m Money) Times(numerator, denominator int64, rounding Rounding) (Money, error) {
	panic("not implemented: TODO stage 01 Money.Times: ErrBadDenominator on d <= 0, mulChecked, then DivideRounded")
}

// DivideRounded is integer division with an explicit rounding rule. Go's / truncates toward zero
// and % keeps the dividend's sign, exactly like Java, so the logic mirrors the Java version.
func DivideRounded(dividend, divisor int64, rounding Rounding) int64 {
	panic("not implemented: TODO stage 01 DivideRounded: truncating / and %, then the four rounding rules")
}

func abs64(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}

// Allocate splits the amount in the given ratios so the parts sum exactly to the whole.
// Largest-remainder method: hand out the floor of each share, then give the leftover minor units
// one at a time to the shares with the largest remainder; ties keep index order (stable sort).
func (m Money) Allocate(ratios ...int64) ([]Money, error) {
	panic("not implemented: TODO stage 01 Money.Allocate: largest-remainder on the magnitude, stable index sort, ErrBadRatios")
}

// Sum adds any number of same-currency amounts; the empty sum is that currency's zero.
func Sum(currency Currency, amounts ...Money) (Money, error) {
	acc := Money{0, currency}
	for _, a := range amounts {
		var err error
		if acc, err = acc.Plus(a); err != nil {
			return Money{}, err
		}
	}
	return acc, nil
}

// Compare returns -1, 0, or 1; it panics on a currency mismatch because comparing USD with EUR is
// a programming error, not a runtime condition (the Java version throws for the same reason).
func (m Money) Compare(other Money) int {
	if err := m.requireSameCurrency(other); err != nil {
		panic(err)
	}
	switch {
	case m.MinorUnits < other.MinorUnits:
		return -1
	case m.MinorUnits > other.MinorUnits:
		return 1
	}
	return 0
}

// String renders "12.34 USD", "-0.50 EUR", "1200 JPY", "1.250 KWD".
func (m Money) String() string {
	magnitude := abs64(m.MinorUnits)
	scale := m.Currency.MinorUnitsPerMajor()
	text := fmt.Sprintf("%d", magnitude/scale)
	if m.Currency.Exponent > 0 {
		text += fmt.Sprintf(".%0*d", m.Currency.Exponent, magnitude%scale)
	}
	if m.MinorUnits < 0 {
		text = "-" + text
	}
	return text + " " + m.Currency.Code
}

// must unwraps a (Money, error) pair where the caller has already guaranteed the currency and range
// (demo code and sums of amounts the ledger has validated). A failure here is a programming error.
func must(m Money, err error) Money {
	if err != nil {
		panic(err)
	}
	return m
}
