// Lesson: phases/00-setup-and-tooling/07-decimal-and-integer-arithmetic/docs/en.md
// Decimal and Integer Arithmetic in Three Runtimes: floats, wrapping ints, checked ints, and a Fixed2 type
// Run:  go run .
// Test: go test ./...
// Sources: IEEE 754-2019 (binary64); Goldberg, "What Every Computer Scientist Should Know About Floating-Point
// Arithmetic" (1991); Go spec "Integer overflow" (wraps); math/big for the exact rational the stdlib offers.
// Go 1.22+, standard library only. Same design as the Java and Python versions.
package main

import (
	"errors"
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"
)

// Rounding says how to resolve a fraction when integer division is inexact. Same four modes as Phase 10, Lesson 01.
type Rounding int

const (
	HalfEven Rounding = iota
	HalfUp
	Down
	Up
)

var roundingNames = [...]string{"HALF_EVEN", "HALF_UP", "DOWN", "UP"}

func (r Rounding) String() string { return roundingNames[r] }

// ---- Floats: where they go wrong, isolated so the tests can pin each failure ----

// AddFloats adds at run time. A constant expression 0.1 + 0.2 would be folded exactly by the compiler and print 0.3.
func AddFloats(a, b float64) float64 { return a + b }

// NaiveCents is the penny-skimming bug: int64(19.99 * 100) is 1998 because 19.99 * 100 is really 1998.9999999999998.
func NaiveCents(amount float64) int64 { return int64(amount * 100) }

// SumRepeated adds term to 0.0 `times` times. Each addition rounds; the errors do not cancel.
func SumRepeated(term float64, times int) float64 {
	acc := 0.0
	for i := 0; i < times; i++ {
		acc += term
	}
	return acc
}

// ---- Checked arithmetic: int64 wraps silently in Go; these refuse to. Same algorithm as the Java version. ----

// ErrOverflow is returned by every checked operation whose true result does not fit in int64.
var ErrOverflow = errors.New("int64 overflow")

func CheckedAdd(a, b int64) (int64, error) {
	r := a + b
	if (a^r)&(b^r) < 0 { // overflow iff both operands disagree in sign with the result
		return 0, fmt.Errorf("%w: %d + %d", ErrOverflow, a, b)
	}
	return r, nil
}

func CheckedSub(a, b int64) (int64, error) {
	r := a - b
	if (a^b)&(a^r) < 0 {
		return 0, fmt.Errorf("%w: %d - %d", ErrOverflow, a, b)
	}
	return r, nil
}

func CheckedMul(a, b int64) (int64, error) {
	r := a * b
	// if it did not wrap, dividing back recovers b; the one case division cannot catch is -1 * MinInt64
	if a != 0 && (r/a != b || (a == -1 && b == math.MinInt64)) {
		return 0, fmt.Errorf("%w: %d * %d", ErrOverflow, a, b)
	}
	return r, nil
}

func CheckedNegate(a int64) (int64, error) {
	if a == math.MinInt64 {
		return 0, fmt.Errorf("%w: -(%d)", ErrOverflow, a)
	}
	return -a, nil
}

// WrappingAdd is what the hardware does: two's-complement wrap. Here only to show it next to the checked version.
func WrappingAdd(a, b int64) int64 { return a + b }

// DivideRounded is integer division with an explicit rounding rule; identical to Phase 10, Lesson 01.
func DivideRounded(dividend, divisor int64, rounding Rounding) (int64, error) {
	if divisor == 0 {
		return 0, errors.New("division by zero")
	}
	quotient := dividend / divisor  // truncates toward zero
	remainder := dividend % divisor // same sign as dividend
	if remainder == 0 {
		return quotient, nil
	}
	sign := int64(1)
	if (dividend < 0) != (divisor < 0) {
		sign = -1
	}
	twiceRemainder := abs64(remainder) * 2
	absDivisor := abs64(divisor)
	switch rounding {
	case Down:
		return quotient, nil
	case Up:
		return quotient + sign, nil
	case HalfUp:
		if twiceRemainder >= absDivisor {
			return quotient + sign, nil
		}
		return quotient, nil
	default: // HalfEven
		if twiceRemainder > absDivisor {
			return quotient + sign, nil
		}
		if twiceRemainder < absDivisor {
			return quotient, nil
		}
		if quotient%2 == 0 {
			return quotient, nil
		}
		return quotient + sign, nil
	}
}

func abs64(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}

// RoundingBias rounds each of 0.5, 1.5, ..., 99.5 (stored as tenths: 5, 15, ..., 995) to an integer with the given
// mode and returns the sum minus the exact sum (5000). HALF_UP drifts by +50 over 100 ties; HALF_EVEN by 0.
func RoundingBias(rounding Rounding) int64 {
	var sum int64
	for tenths := int64(5); tenths < 1000; tenths += 10 {
		q, _ := DivideRounded(tenths, 10, rounding)
		sum += q
	}
	return sum - 5000
}

// ---- Fixed2: a fixed-point decimal with exactly two places, stored as hundredths ----

// Fixed2 has no currency: that, plus allocation, is what Phase 10, Lesson 01's Money adds on top of this type.
// Every operation that could produce a fraction takes a Rounding. Overflow is an error, never a wrap.
type Fixed2 struct {
	Hundredths int64
}

var Zero = Fixed2{0}

func OfHundredths(h int64) Fixed2 { return Fixed2{h} }

// ErrTooManyDecimals is returned for inputs such as "19.995": an error, not a rounding.
var ErrTooManyDecimals = errors.New("more than two decimals")

// Parse reads "19.99", "-0.5", "7" exactly. More than two decimals is an error, not a rounding.
func Parse(text string) (Fixed2, error) {
	s := strings.TrimSpace(text)
	negative := strings.HasPrefix(s, "-")
	if negative || strings.HasPrefix(s, "+") {
		s = s[1:]
	}
	if s == "" {
		return Fixed2{}, errors.New("empty amount")
	}
	whole, frac, _ := strings.Cut(s, ".")
	if whole == "" {
		whole = "0"
	}
	if !allDigits(whole) || !allDigits(frac) {
		return Fixed2{}, fmt.Errorf("not a decimal: %q", text)
	}
	if len(frac) > 2 {
		return Fixed2{}, fmt.Errorf("%w: %s", ErrTooManyDecimals, text)
	}
	for len(frac) < 2 {
		frac += "0"
	}
	w, err := strconv.ParseInt(whole, 10, 64)
	if err != nil {
		return Fixed2{}, fmt.Errorf("%w: %s", ErrOverflow, text)
	}
	f, _ := strconv.ParseInt(frac, 10, 64)
	scaled, err := CheckedMul(w, 100)
	if err != nil {
		return Fixed2{}, err
	}
	value, err := CheckedAdd(scaled, f)
	if err != nil {
		return Fixed2{}, err
	}
	if negative {
		value = -value
	}
	return Fixed2{value}, nil
}

func allDigits(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// FromDouble is the only correct way in from a float64: through its shortest round-trip decimal string, never
// through int64(v * 100). 19.99 -> "19.99" -> 1999; 19.995 is rejected like any 3-decimal input.
func FromDouble(value float64) (Fixed2, error) {
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return Fixed2{}, fmt.Errorf("not finite: %v", value)
	}
	return Parse(strconv.FormatFloat(value, 'f', -1, 64)) // precision -1: the shortest string that round-trips
}

func (f Fixed2) Plus(other Fixed2) (Fixed2, error) {
	h, err := CheckedAdd(f.Hundredths, other.Hundredths)
	return Fixed2{h}, err
}

func (f Fixed2) Minus(other Fixed2) (Fixed2, error) {
	h, err := CheckedSub(f.Hundredths, other.Hundredths)
	return Fixed2{h}, err
}

func (f Fixed2) Negate() (Fixed2, error) {
	h, err := CheckedNegate(f.Hundredths)
	return Fixed2{h}, err
}

// Times multiplies two Fixed2 values: the product has four decimals, so two must be rounded away, your way.
func (f Fixed2) Times(other Fixed2, rounding Rounding) (Fixed2, error) {
	product, err := CheckedMul(f.Hundredths, other.Hundredths) // scaled by 10^4
	if err != nil {
		return Fixed2{}, err
	}
	h, err := DivideRounded(product, 100, rounding)
	return Fixed2{h}, err
}

// TimesRate multiplies by an exact rational: 2.9% is 29/1000, an FX rate of 0.9217 is 9217/10000.
func (f Fixed2) TimesRate(numerator, denominator int64, rounding Rounding) (Fixed2, error) {
	if denominator <= 0 {
		return Fixed2{}, errors.New("denominator must be positive")
	}
	product, err := CheckedMul(f.Hundredths, numerator)
	if err != nil {
		return Fixed2{}, err
	}
	h, err := DivideRounded(product, denominator, rounding)
	return Fixed2{h}, err
}

func (f Fixed2) DividedBy(divisor int64, rounding Rounding) (Fixed2, error) {
	h, err := DivideRounded(f.Hundredths, divisor, rounding)
	return Fixed2{h}, err
}

func (f Fixed2) Compare(other Fixed2) int {
	switch {
	case f.Hundredths < other.Hundredths:
		return -1
	case f.Hundredths > other.Hundredths:
		return 1
	}
	return 0
}

// String renders "19.99", "-0.05", "0.00": always two decimals, never scientific notation.
func (f Fixed2) String() string {
	magnitude := abs64(f.Hundredths)
	sign := ""
	if f.Hundredths < 0 {
		sign = "-"
	}
	return fmt.Sprintf("%s%d.%02d", sign, magnitude/100, magnitude%100)
}

func must(f Fixed2, err error) Fixed2 {
	if err != nil {
		panic(err)
	}
	return f
}

func main() {
	fmt.Println("== 0.1 + 0.2 ==")
	a, b := 0.1, 0.2 // variables, not constants: constant folding would print 0.3
	fmt.Printf("0.1 + 0.2 = %v  (equals 0.3? %v)\n", AddFloats(a, b), AddFloats(a, b) == 0.3)
	fmt.Printf("int64(19.99 * 100) = %d  <- the penny-skimming bug (19.99 * 100 = 1998.9999999999998)\n", NaiveCents(19.99))
	fmt.Printf("FromDouble(19.99) = %v  (via the shortest decimal string \"19.99\")\n", must(FromDouble(19.99)))

	fmt.Println("\n== Accumulation ==")
	fmt.Printf("0.1 added 10 times as float64 = %v\n", SumRepeated(0.1, 10))
	million := SumRepeated(0.01, 1_000_000)
	fmt.Printf("0.01 added 1,000,000 times as float64 = %.10f (error %.3e)\n", million, million-10_000)
	acc, cent := Zero, must(Parse("0.01"))
	for i := 0; i < 1_000_000; i++ {
		acc = must(acc.Plus(cent))
	}
	fmt.Printf("0.01 added 1,000,000 times as Fixed2 = %v exactly\n", acc)

	fmt.Println("\n== Rounding bias on 100 exact half-ties (0.5, 1.5, ..., 99.5) ==")
	for _, r := range []Rounding{HalfEven, HalfUp, Down, Up} {
		fmt.Printf("%-9s drift = %+d\n", r, RoundingBias(r))
	}

	fmt.Println("\n== Overflow ==")
	fmt.Printf("math.MaxInt64 + 1 with plain + = %d  (wrapped to math.MinInt64)\n", WrappingAdd(math.MaxInt64, 1))
	if _, err := CheckedAdd(math.MaxInt64, 1); err != nil {
		fmt.Println("CheckedAdd(math.MaxInt64, 1) -> error:", err)
	}

	fmt.Println("\n== Fixed2: two decimals, explicit rounding ==")
	price := must(Parse("19.99"))
	fmt.Printf("%v x 3        = %v\n", price, must(price.Times(must(Parse("3")), HalfEven)))
	fmt.Printf("%v x 2.9%%     = %v  (57.971 hundredths -> 58)\n", price, must(price.TimesRate(29, 1000, HalfEven)))
	fmt.Printf("%v x 1.08     = %v  (2158.92 hundredths -> 2159)\n", price, must(price.Times(must(Parse("1.08")), HalfEven)))
	third := must(must(Parse("100.00")).DividedBy(3, HalfEven))
	fmt.Printf("100.00 / 3       = %v, and %v x 3 = %v  <- why Phase 10 adds allocate()\n",
		third, third, must(third.Times(must(Parse("3")), HalfEven)))
	if _, err := Parse("19.995"); err != nil {
		fmt.Println("rejected:", err)
	}

	fmt.Println("\n== Decimal types in the standard library ==")
	fmt.Println("Go has no decimal type; math/big.Rat is the exact rational:")
	x, _ := new(big.Rat).SetString("0.1")
	y, _ := new(big.Rat).SetString("0.2")
	sum := new(big.Rat).Add(x, y)
	fmt.Printf("Rat(\"0.1\") + Rat(\"0.2\") = %s = %s\n", sum.RatString(), sum.FloatString(2))
	fmt.Printf("Rat from the float64 0.1    = %s\n", new(big.Rat).SetFloat64(0.1).RatString())
}
