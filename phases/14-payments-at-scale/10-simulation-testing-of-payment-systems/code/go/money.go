// Money: exact amounts in integer minor units. Copied from Phase 10, Lesson 01 (code/go/main.go), trimmed to
// what this lesson uses.

package main

import (
	"errors"
	"fmt"
	"math"
	"strconv"
)

// Currency is an ISO 4217 currency: three-letter code and minor-unit exponent.
type Currency struct {
	Code     string
	Exponent int
}

var USD = Currency{"USD", 2}

func (c Currency) MinorUnitsPerMajor() int64 {
	scale := int64(1)
	for i := 0; i < c.Exponent; i++ {
		scale *= 10
	}
	return scale
}

var (
	ErrCurrencyMismatch = errors.New("currency mismatch")
	ErrOverflow         = errors.New("int64 overflow")
)

// Money is an exact amount of one currency as a signed count of minor units.
type Money struct {
	MinorUnits int64
	Currency   Currency
}

func OfMinor(minorUnits int64, currency Currency) Money { return Money{minorUnits, currency} }
func Zero(currency Currency) Money                      { return Money{0, currency} }

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
func (m Money) IsPositive() bool { return m.MinorUnits > 0 }

func abs64(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}

func (m Money) String() string {
	magnitude := abs64(m.MinorUnits)
	scale := m.Currency.MinorUnitsPerMajor()
	text := strconv.FormatInt(magnitude/scale, 10)
	if m.Currency.Exponent > 0 {
		text += fmt.Sprintf(".%0*d", m.Currency.Exponent, magnitude%scale)
	}
	if m.MinorUnits < 0 {
		text = "-" + text
	}
	return text + " " + m.Currency.Code
}

// need panics on an error where the Java threw an unchecked exception from arithmetic that cannot fail on the
// lesson's inputs (int64 overflow of a sum of minor units, or a currency mismatch inside one experiment).
func need[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}
