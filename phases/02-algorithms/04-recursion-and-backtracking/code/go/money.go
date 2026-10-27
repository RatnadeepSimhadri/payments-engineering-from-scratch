package main

import (
	"cmp"
	"errors"
	"fmt"
)

// Currency is an ISO 4217 currency: a three-letter code and the number of decimal places in its minor unit.
type Currency struct {
	Code     string
	Exponent int
}

// USD is the only currency the demo needs.
var USD = Currency{"USD", 2}

// NewCurrency validates the code and exponent; the package-level constants are known-good.
func NewCurrency(code string, exponent int) (Currency, error) {
	if len(code) != 3 {
		return Currency{}, fmt.Errorf("currency code: %q", code)
	}
	if exponent < 0 || exponent > 4 {
		return Currency{}, fmt.Errorf("exponent must be 0..4: %d", exponent)
	}
	return Currency{code, exponent}, nil
}

// Money is an exact amount of one currency as a signed count of minor units (Phase 10, Lesson 01).
type Money struct {
	MinorUnits int64
	Currency   Currency
}

// ErrOverflow is returned where Java's Math.addExact would throw ArithmeticException.
var ErrOverflow = errors.New("long overflow")

// OfMinor builds a Money from minor units.
func OfMinor(minorUnits int64, currency Currency) Money { return Money{minorUnits, currency} }

// ErrCurrencyMismatch is returned where Java's requireSame throws IllegalArgumentException.
var ErrCurrencyMismatch = errors.New("cannot combine different currencies")

// Plus is checked addition of two amounts of the same currency.
func (m Money) Plus(o Money) (Money, error) {
	if m.Currency != o.Currency {
		return Money{}, fmt.Errorf("%w: %s with %s", ErrCurrencyMismatch, m.Currency.Code, o.Currency.Code)
	}
	sum, err := addChecked(m.MinorUnits, o.MinorUnits)
	return Money{sum, m.Currency}, err
}

// Compare orders two amounts of the same currency; a currency mismatch is a programming error and panics.
func (m Money) Compare(other Money) int {
	if m.Currency != other.Currency {
		panic("cannot compare " + m.Currency.Code + " with " + other.Currency.Code)
	}
	return cmp.Compare(m.MinorUnits, other.MinorUnits)
}

func (m Money) String() string {
	scale := int64(1)
	for i := 0; i < m.Currency.Exponent; i++ {
		scale *= 10
	}
	abs := m.MinorUnits
	if abs < 0 {
		abs = -abs
	}
	whole, frac := abs/scale, abs%scale
	sign := ""
	if m.MinorUnits < 0 {
		sign = "-"
	}
	if m.Currency.Exponent == 0 {
		return fmt.Sprintf("%s%d %s", sign, whole, m.Currency.Code)
	}
	return fmt.Sprintf("%s%d.%0*d %s", sign, whole, m.Currency.Exponent, frac, m.Currency.Code)
}

// addChecked is Math.addExact: int64 addition that reports overflow instead of wrapping.
func addChecked(a, b int64) (int64, error) {
	sum := a + b
	if (a >= 0) == (b >= 0) && (sum >= 0) != (a >= 0) {
		return 0, ErrOverflow
	}
	return sum, nil
}
