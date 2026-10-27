package main

// Money, ported from Phase 10, Lesson 01 (the canonical Go Money): integer minor units plus a currency. Never a float.

import (
	"errors"
	"fmt"
)

// Currency is an ISO 4217 code and the number of decimals in its minor unit.
type Currency struct {
	Code     string
	Exponent int
}

var USD = Currency{"USD", 2}

// NewCurrency validates the code and exponent as the Java compact constructor does.
func NewCurrency(code string, exponent int) (Currency, error) {
	if len(code) != 3 {
		return Currency{}, errors.New("currency code must be three letters")
	}
	if exponent < 0 || exponent > 4 {
		return Currency{}, errors.New("exponent must be 0..4")
	}
	return Currency{code, exponent}, nil
}

var (
	ErrCurrencyMismatch = errors.New("currency mismatch")
	ErrOverflow         = errors.New("amount overflows int64 minor units")
)

// Money is an amount in integer minor units of one currency.
type Money struct {
	MinorUnits int64
	Currency   Currency
}

func OfMinor(minorUnits int64, currency Currency) Money { return Money{minorUnits, currency} }

func Zero(currency Currency) Money { return Money{0, currency} }

func addChecked(a, b int64) (int64, error) {
	sum := a + b
	if (a > 0 && b > 0 && sum < 0) || (a < 0 && b < 0 && sum >= 0) {
		return 0, ErrOverflow
	}
	return sum, nil
}

func (m Money) Plus(other Money) (Money, error) {
	if m.Currency != other.Currency {
		return Money{}, fmt.Errorf("%w: cannot combine %s with %s", ErrCurrencyMismatch, m.Currency.Code, other.Currency.Code)
	}
	sum, err := addChecked(m.MinorUnits, other.MinorUnits)
	return Money{sum, m.Currency}, err
}

func (m Money) String() string {
	scale := int64(1)
	for i := 0; i < m.Currency.Exponent; i++ {
		scale *= 10
	}
	abs := m.MinorUnits
	sign := ""
	if abs < 0 {
		abs = -abs
		sign = "-"
	}
	if m.Currency.Exponent == 0 {
		return fmt.Sprintf("%s%d %s", sign, abs, m.Currency.Code)
	}
	return fmt.Sprintf("%s%d.%0*d %s", sign, abs/scale, m.Currency.Exponent, abs%scale, m.Currency.Code)
}
