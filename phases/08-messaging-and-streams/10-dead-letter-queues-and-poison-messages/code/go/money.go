package main

// Money, ported from Phase 10, Lesson 01 (the canonical Go Money): integer minor units plus a currency. Never a float.
// Parse rejects excess precision instead of rounding, exactly as the Java version does.

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Currency is an ISO 4217 code and the number of decimals in its minor unit.
type Currency struct {
	Code     string
	Exponent int
}

var (
	USD = Currency{"USD", 2}
	JPY = Currency{"JPY", 0}
)

// Sentinel errors. The error text is the Java exception message verbatim (the handler copies it into the dead
// letter), and errors.Is still matches the sentinel through the Is method on classifiedError.
var (
	ErrUnknownCurrency  = errors.New("unknown currency")
	ErrNotDecimal       = errors.New("not a decimal amount")               // Java NumberFormatException
	ErrExcessPrecision  = errors.New("excess precision")                   // Java ArithmeticException
	ErrCurrencyMismatch = errors.New("currency mismatch")                  // Java IllegalArgumentException
	ErrOverflow         = errors.New("amount overflows int64 minor units") // Java ArithmeticException from addExact/multiplyExact
)

// classifiedError carries a Java-shaped message and answers errors.Is for its sentinel.
type classifiedError struct {
	sentinel error
	msg      string
}

func (e *classifiedError) Error() string        { return e.msg }
func (e *classifiedError) Is(target error) bool { return target == e.sentinel }

func classified(sentinel error, format string, args ...any) error {
	return &classifiedError{sentinel: sentinel, msg: fmt.Sprintf(format, args...)}
}

// CurrencyOf resolves the codes this lesson knows; anything else is a poison payload.
func CurrencyOf(code string) (Currency, error) {
	switch code {
	case "USD":
		return USD, nil
	case "JPY":
		return JPY, nil
	default:
		return Currency{}, classified(ErrUnknownCurrency, "unknown currency: %s", code)
	}
}

func (c Currency) MinorUnitsPerMajor() int64 {
	scale := int64(1)
	for i := 0; i < c.Exponent; i++ {
		scale *= 10
	}
	return scale
}

// Money is an amount in integer minor units of one currency.
type Money struct {
	MinorUnits int64
	Currency   Currency
}

func Zero(currency Currency) Money { return Money{0, currency} }

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
	if product/b != a || (a == -1 && b == -1<<63) || (b == -1 && a == -1<<63) {
		return 0, ErrOverflow
	}
	return product, nil
}

func allDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// ParseMoney is Money.parse: a decimal string in major units, rejected (never rounded) when it carries more
// decimals than the currency allows.
func ParseMoney(text string, currency Currency) (Money, error) {
	s := strings.TrimSpace(text)
	negative := strings.HasPrefix(s, "-")
	if negative || strings.HasPrefix(s, "+") {
		s = s[1:]
	}
	if s == "" {
		return Money{}, classified(ErrNotDecimal, "empty amount")
	}
	whole, frac := s, ""
	if dot := strings.IndexByte(s, '.'); dot >= 0 {
		whole, frac = s[:dot], s[dot+1:]
	}
	if whole == "" {
		whole = "0"
	}
	if !allDigits(whole) || !allDigits(frac) {
		return Money{}, classified(ErrNotDecimal, "not a decimal amount: %s", text)
	}
	if len(frac) > currency.Exponent {
		return Money{}, classified(ErrExcessPrecision, "%s has more decimals than %s allows (%d)", text, currency.Code, currency.Exponent)
	}
	padded := frac + strings.Repeat("0", currency.Exponent-len(frac))
	wholeUnits, err := strconv.ParseInt(whole, 10, 64)
	if err != nil {
		return Money{}, classified(ErrNotDecimal, "not a decimal amount: %s", text)
	}
	major, err := mulChecked(wholeUnits, currency.MinorUnitsPerMajor())
	if err != nil {
		return Money{}, err
	}
	var fracUnits int64
	if padded != "" {
		fracUnits, _ = strconv.ParseInt(padded, 10, 64) // at most 4 digits: cannot fail or overflow
	}
	minor, err := addChecked(major, fracUnits)
	if err != nil {
		return Money{}, err
	}
	if negative {
		minor = -minor
	}
	return Money{minor, currency}, nil
}

func (m Money) Plus(other Money) (Money, error) {
	if m.Currency != other.Currency {
		return Money{}, classified(ErrCurrencyMismatch, "cannot combine %s with %s", m.Currency.Code, other.Currency.Code)
	}
	sum, err := addChecked(m.MinorUnits, other.MinorUnits)
	return Money{sum, m.Currency}, err
}

func (m Money) String() string {
	scale := m.Currency.MinorUnitsPerMajor()
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
