// Money (copied from Phase 10, Lesson 01: phases/10-money-and-ledgers/01-representing-money/code/go/main.go).
// Integer minor units + ISO 4217 currency. Never a float. Same design as the Java Money record.
package main

import (
	"errors"
	"fmt"
	"math"
	"sort"
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

// NewCurrency validates the code and exponent; the package-level constants are known-good.
func NewCurrency(code string, exponent int) (Currency, error) {
	if len(code) != 3 || strings.ToUpper(code) != code || strings.Trim(code, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") != "" {
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

// Rounding says how to resolve a fractional minor unit.
type Rounding int

const (
	HalfEven Rounding = iota // banker's rounding: ties go to the even neighbour; unbiased
	HalfUp                   // ties go away from zero; what most people mean by "round"
	Down                     // truncate toward zero
	Up                       // away from zero
)

// ErrCurrencyMismatch is returned when two amounts of different currencies are combined.
var ErrCurrencyMismatch = errors.New("currency mismatch")

// ErrOverflow is returned when an int64 would wrap. Money never silently wraps.
var ErrOverflow = errors.New("int64 overflow")

// Money is an exact amount of one currency stored as a signed count of minor units.
// Values are immutable: every method returns a new Money.
type Money struct {
	MinorUnits int64
	Currency   Currency
}

func OfMinor(minorUnits int64, currency Currency) Money {
	return Money{minorUnits, currency}
}

// Parse reads "12.34", "-0.5", "1200" exactly and rejects more decimals than the currency allows.
func Parse(text string, currency Currency) (Money, error) {
	s := strings.TrimSpace(text)
	negative := strings.HasPrefix(s, "-")
	if negative || strings.HasPrefix(s, "+") {
		s = s[1:]
	}
	if s == "" {
		return Money{}, errors.New("empty amount")
	}
	whole, frac, _ := strings.Cut(s, ".")
	if whole == "" {
		whole = "0"
	}
	if !allDigits(whole) || (frac != "" && !allDigits(frac)) {
		return Money{}, fmt.Errorf("not a decimal amount: %q", text)
	}
	if len(frac) > currency.Exponent {
		return Money{}, fmt.Errorf("%s has more decimals than %s allows (%d)", text, currency.Code, currency.Exponent)
	}
	frac += strings.Repeat("0", currency.Exponent-len(frac))
	var minor int64
	for _, ch := range whole + frac {
		next := minor*10 + int64(ch-'0')
		if next < minor {
			return Money{}, ErrOverflow
		}
		minor = next
	}
	if negative {
		minor = -minor
	}
	return Money{minor, currency}, nil
}

func allDigits(s string) bool {
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	return len(s) > 0
}

func (m Money) requireSameCurrency(other Money) error {
	if m.Currency != other.Currency {
		return fmt.Errorf("%w: cannot combine %s with %s", ErrCurrencyMismatch, m.Currency.Code, other.Currency.Code)
	}
	return nil
}

func addChecked(a, b int64) (int64, error) {
	sum := a + b
	if (a > 0 && b > 0 && sum < 0) || (a < 0 && b < 0 && sum >= 0) {
		return 0, ErrOverflow
	}
	return sum, nil
}

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
	if err := m.requireSameCurrency(other); err != nil {
		return Money{}, err
	}
	sum, err := addChecked(m.MinorUnits, other.MinorUnits)
	return Money{sum, m.Currency}, err
}

func (m Money) Minus(other Money) (Money, error) {
	if err := m.requireSameCurrency(other); err != nil {
		return Money{}, err
	}
	diff, err := addChecked(m.MinorUnits, -other.MinorUnits)
	return Money{diff, m.Currency}, err
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

// DivideRounded is integer division with an explicit rounding rule. Go's / truncates toward zero
// and % keeps the dividend's sign, exactly like Java, so the logic mirrors the Java version.
func DivideRounded(dividend, divisor int64, rounding Rounding) int64 {
	quotient := dividend / divisor
	remainder := dividend % divisor
	if remainder == 0 {
		return quotient
	}
	sign := int64(1)
	if (dividend < 0) != (divisor < 0) {
		sign = -1
	}
	twice := abs64(remainder) * 2
	absDivisor := abs64(divisor)
	switch rounding {
	case Down:
		return quotient
	case Up:
		return quotient + sign
	case HalfUp:
		if twice >= absDivisor {
			return quotient + sign
		}
		return quotient
	default: // HalfEven
		if twice > absDivisor {
			return quotient + sign
		}
		if twice < absDivisor {
			return quotient
		}
		if quotient%2 == 0 {
			return quotient
		}
		return quotient + sign
	}
}

func abs64(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}

// Times multiplies by an exact rational (2.9% is 29/1000) and rounds once, the way you asked.
func (m Money) Times(numerator, denominator int64, rounding Rounding) (Money, error) {
	if denominator <= 0 {
		return Money{}, errors.New("denominator must be positive")
	}
	product, err := mulChecked(m.MinorUnits, numerator)
	if err != nil {
		return Money{}, err
	}
	return Money{DivideRounded(product, denominator, rounding), m.Currency}, nil
}

// Allocate splits the amount in the given ratios so the parts sum exactly to the whole
// (largest-remainder method; ties resolved by index order, so the result is deterministic).
func (m Money) Allocate(ratios ...int64) ([]Money, error) {
	if len(ratios) == 0 {
		return nil, errors.New("need at least one ratio")
	}
	var total int64
	for _, r := range ratios {
		if r < 0 {
			return nil, errors.New("ratios must be non-negative")
		}
		total += r
	}
	if total == 0 {
		return nil, errors.New("ratios must not all be zero")
	}
	magnitude := abs64(m.MinorUnits)
	shares := make([]int64, len(ratios))
	remainders := make([]int64, len(ratios))
	var distributed int64
	for i, r := range ratios {
		scaled, err := mulChecked(magnitude, r)
		if err != nil {
			return nil, err
		}
		shares[i] = scaled / total
		remainders[i] = scaled % total
		distributed += shares[i]
	}
	leftover := magnitude - distributed
	order := make([]int, len(ratios))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return remainders[order[a]] > remainders[order[b]] })
	for k := int64(0); k < leftover; k++ {
		shares[order[k]]++
	}
	result := make([]Money, len(ratios))
	for i, s := range shares {
		if m.MinorUnits < 0 {
			s = -s
		}
		result[i] = Money{s, m.Currency}
	}
	return result, nil
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
