package main

import (
	"errors"
	"math"
	"math/big"
	"strconv"
	"testing"
)

func parse(t *testing.T, text string) Fixed2 {
	t.Helper()
	f, err := Parse(text)
	if err != nil {
		t.Fatalf("Parse(%q): %v", text, err)
	}
	return f
}

func TestFloatFailures(t *testing.T) {
	a, b := 0.1, 0.2
	if AddFloats(a, b) == 0.3 {
		t.Error("0.1 + 0.2 should not equal 0.3 at run time")
	}
	if got := strconv.FormatFloat(AddFloats(a, b), 'g', -1, 64); got != "0.30000000000000004" {
		t.Errorf("0.1 + 0.2 formats as %s", got)
	}
	if got := NaiveCents(19.99); got != 1998 {
		t.Errorf("NaiveCents(19.99) = %d, want 1998 (the bug)", got)
	}
	if SumRepeated(0.1, 10) == 1.0 {
		t.Error("0.1 added ten times should not be exactly 1.0")
	}
	if math.Abs(SumRepeated(0.01, 1_000_000)-10_000) <= 1e-7 {
		t.Error("a million additions of 0.01 should be off by more than 1e-7")
	}
	if SumRepeated(0.25, 4) != 1.0 {
		t.Error("powers of two are exact")
	}
}

func TestCheckedArithmetic(t *testing.T) {
	if WrappingAdd(math.MaxInt64, 1) != math.MinInt64 {
		t.Error("plain + should wrap MaxInt64 to MinInt64")
	}
	ok := []struct {
		name string
		got  func() (int64, error)
		want int64
	}{
		{"add", func() (int64, error) { return CheckedAdd(1, 2) }, 3},
		{"add negatives", func() (int64, error) { return CheckedAdd(-1, -2) }, -3},
		{"sub", func() (int64, error) { return CheckedSub(1, 2) }, -1},
		{"mul", func() (int64, error) { return CheckedMul(2, 3) }, 6},
		{"mul by zero", func() (int64, error) { return CheckedMul(math.MaxInt64, 0) }, 0},
		{"negate", func() (int64, error) { return CheckedNegate(5) }, -5},
	}
	for _, tc := range ok {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.got()
			if err != nil || got != tc.want {
				t.Errorf("got %d, %v; want %d", got, err, tc.want)
			}
		})
	}
	overflow := []struct {
		name string
		got  func() (int64, error)
	}{
		{"add overflow", func() (int64, error) { return CheckedAdd(math.MaxInt64, 1) }},
		{"add underflow", func() (int64, error) { return CheckedAdd(math.MinInt64, -1) }},
		{"sub underflow", func() (int64, error) { return CheckedSub(math.MinInt64, 1) }},
		{"mul overflow", func() (int64, error) { return CheckedMul(1<<62, 4) }},
		{"mul MIN * -1", func() (int64, error) { return CheckedMul(math.MinInt64, -1) }},
		{"mul -1 * MIN", func() (int64, error) { return CheckedMul(-1, math.MinInt64) }},
		{"negate MIN", func() (int64, error) { return CheckedNegate(math.MinInt64) }},
	}
	for _, tc := range overflow {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := tc.got(); !errors.Is(err, ErrOverflow) {
				t.Errorf("want ErrOverflow, got %v", err)
			}
		})
	}
}

func TestDivideRounded(t *testing.T) {
	cases := []struct {
		dividend, divisor int64
		rounding          Rounding
		want              int64
	}{
		{125, 10, HalfEven, 12}, {135, 10, HalfEven, 14}, {125, 10, HalfUp, 13},
		{-125, 10, HalfUp, -13}, {-125, 10, HalfEven, -12}, {-129, 10, Down, -12}, {121, 10, Up, 13},
	}
	for _, c := range cases {
		got, err := DivideRounded(c.dividend, c.divisor, c.rounding)
		if err != nil || got != c.want {
			t.Errorf("DivideRounded(%d, %d, %v) = %d, %v; want %d", c.dividend, c.divisor, c.rounding, got, err, c.want)
		}
	}
	if _, err := DivideRounded(1, 0, Down); err == nil {
		t.Error("division by zero should error")
	}
}

func TestRoundingBias(t *testing.T) {
	want := map[Rounding]int64{HalfUp: 50, HalfEven: 0, Down: -50, Up: 50}
	for r, w := range want {
		if got := RoundingBias(r); got != w {
			t.Errorf("RoundingBias(%v) = %d, want %d", r, got, w)
		}
	}
}

func TestParse(t *testing.T) {
	cases := map[string]int64{"19.99": 1999, "7": 700, "-0.5": -50, ".05": 5}
	for text, want := range cases {
		if got := parse(t, text).Hundredths; got != want {
			t.Errorf("Parse(%q) = %d, want %d", text, got, want)
		}
	}
	if _, err := Parse("19.995"); !errors.Is(err, ErrTooManyDecimals) {
		t.Errorf("three decimals: %v", err)
	}
	if _, err := Parse("19,99"); err == nil {
		t.Error("garbage should fail")
	}
}

func TestFromDouble(t *testing.T) {
	if got := must(FromDouble(19.99)).Hundredths; got != 1999 {
		t.Errorf("FromDouble(19.99) = %d", got)
	}
	a, b := 0.1, 0.2
	if _, err := FromDouble(a + b); !errors.Is(err, ErrTooManyDecimals) {
		t.Errorf("FromDouble(0.1+0.2) should surface the binary error, got %v", err)
	}
	if _, err := FromDouble(19.995); err == nil {
		t.Error("19.995 should be rejected")
	}
	if _, err := FromDouble(math.NaN()); err == nil {
		t.Error("NaN should be rejected")
	}
}

func TestString(t *testing.T) {
	cases := map[int64]string{1999: "19.99", -5: "-0.05", 0: "0.00"}
	for h, want := range cases {
		if got := OfHundredths(h).String(); got != want {
			t.Errorf("String(%d) = %q, want %q", h, got, want)
		}
	}
}

func TestPlusMinusOverflow(t *testing.T) {
	if got := must(parse(t, "1.10").Plus(parse(t, "2.20"))); got != parse(t, "3.30") {
		t.Errorf("plus = %v", got)
	}
	if got := must(parse(t, "1.00").Minus(parse(t, "1.50"))).Hundredths; got != -50 {
		t.Errorf("minus = %d", got)
	}
	if _, err := OfHundredths(math.MaxInt64).Plus(OfHundredths(1)); !errors.Is(err, ErrOverflow) {
		t.Errorf("overflow should be an error, got %v", err)
	}
}

func TestTimes(t *testing.T) {
	cases := []struct {
		a, b     string
		rounding Rounding
		want     string
	}{
		{"19.99", "3", HalfEven, "59.97"}, {"19.99", "1.08", HalfEven, "21.59"},
		{"0.05", "0.05", HalfEven, "0.00"}, {"0.05", "0.05", Up, "0.01"},
	}
	for _, c := range cases {
		if got := must(parse(t, c.a).Times(parse(t, c.b), c.rounding)); got != parse(t, c.want) {
			t.Errorf("%s x %s %v = %v, want %s", c.a, c.b, c.rounding, got, c.want)
		}
	}
	if got := must(parse(t, "19.99").TimesRate(29, 1000, HalfEven)); got != parse(t, "0.58") {
		t.Errorf("2.9%% of 19.99 = %v", got)
	}
	if _, err := parse(t, "1").TimesRate(1, 0, Down); err == nil {
		t.Error("zero denominator should error")
	}
}

func TestDivisionDoesNotConserve(t *testing.T) {
	third := must(parse(t, "100.00").DividedBy(3, HalfEven))
	if third != parse(t, "33.33") {
		t.Errorf("100/3 = %v", third)
	}
	if must(third.Times(parse(t, "3"), HalfEven)) == parse(t, "100.00") {
		t.Error("33.33 x 3 must not be 100.00; this is why Phase 10 adds Allocate")
	}
	if parse(t, "1.00").Compare(parse(t, "2.00")) >= 0 {
		t.Error("Compare")
	}
}

func TestMillionCents(t *testing.T) {
	acc, cent := Zero, parse(t, "0.01")
	for i := 0; i < 1_000_000; i++ {
		acc = must(acc.Plus(cent))
	}
	if acc != parse(t, "10000.00") {
		t.Errorf("a million cents = %v", acc)
	}
}

func TestBigRatIsExact(t *testing.T) {
	x, _ := new(big.Rat).SetString("0.1")
	y, _ := new(big.Rat).SetString("0.2")
	if got := new(big.Rat).Add(x, y).FloatString(2); got != "0.30" {
		t.Errorf("Rat sum = %s", got)
	}
	if new(big.Rat).SetFloat64(0.1).Cmp(x) == 0 {
		t.Error("the float64 0.1 is not the rational 1/10")
	}
}
