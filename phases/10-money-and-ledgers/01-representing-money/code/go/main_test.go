package main

import (
	"errors"
	"math"
	"testing"
)

func parse(t *testing.T, text string, c Currency) Money {
	t.Helper()
	m, err := Parse(text, c)
	if err != nil {
		t.Fatalf("Parse(%q): %v", text, err)
	}
	return m
}

func TestParse(t *testing.T) {
	cases := []struct {
		text string
		cur  Currency
		want int64
	}{
		{"12.34", USD, 1234}, {"12", USD, 1200}, {"0.5", USD, 50}, {".99", USD, 99},
		{"-0.07", USD, -7}, {"1200", JPY, 1200}, {"1.250", KWD, 1250},
	}
	for _, c := range cases {
		if got := parse(t, c.text, c.cur).MinorUnits; got != c.want {
			t.Errorf("Parse(%q) = %d, want %d", c.text, got, c.want)
		}
	}
}

func TestParseRejects(t *testing.T) {
	for _, bad := range []struct {
		text string
		cur  Currency
	}{{"1.5", JPY}, {"1.005", USD}, {"12,34", USD}, {"", USD}} {
		if _, err := Parse(bad.text, bad.cur); err == nil {
			t.Errorf("Parse(%q, %s) should fail", bad.text, bad.cur.Code)
		}
	}
}

func TestString(t *testing.T) {
	cases := map[string]Money{
		"12.34 USD": OfMinor(1234, USD), "12.05 USD": OfMinor(1205, USD), "-0.07 USD": OfMinor(-7, USD),
		"1200 JPY": OfMinor(1200, JPY), "1.250 KWD": OfMinor(1250, KWD),
	}
	for want, m := range cases {
		if got := m.String(); got != want {
			t.Errorf("String() = %q, want %q", got, want)
		}
	}
}

func TestPlusMinus(t *testing.T) {
	sum, err := parse(t, "1.10", USD).Plus(parse(t, "2.20", USD))
	if err != nil || sum != parse(t, "3.30", USD) {
		t.Fatalf("plus = %v, %v", sum, err)
	}
	diff, _ := parse(t, "1.00", USD).Minus(parse(t, "1.50", USD))
	if diff.MinorUnits != -50 {
		t.Fatalf("minus = %v", diff)
	}
	if parse(t, "5.00", USD).Negate().MinorUnits != -500 || OfMinor(-5, USD).Abs().MinorUnits != 5 {
		t.Fatal("negate/abs")
	}
}

func TestCurrencyMismatch(t *testing.T) {
	_, err := parse(t, "1.00", USD).Plus(parse(t, "1.00", EUR))
	if !errors.Is(err, ErrCurrencyMismatch) {
		t.Fatalf("expected ErrCurrencyMismatch, got %v", err)
	}
	defer func() {
		if recover() == nil {
			t.Fatal("Compare across currencies should panic")
		}
	}()
	parse(t, "1.00", USD).Compare(parse(t, "1.00", EUR))
}

func TestOverflowIsAnError(t *testing.T) {
	if _, err := OfMinor(math.MaxInt64, USD).Plus(OfMinor(1, USD)); !errors.Is(err, ErrOverflow) {
		t.Fatalf("expected ErrOverflow, got %v", err)
	}
	if _, err := OfMinor(math.MaxInt64, USD).Times(2, 1, Down); !errors.Is(err, ErrOverflow) {
		t.Fatalf("expected ErrOverflow on multiply, got %v", err)
	}
}

func TestDivideRounded(t *testing.T) {
	cases := []struct {
		dividend, divisor int64
		mode              Rounding
		want              int64
	}{
		{125, 10, HalfEven, 12}, {135, 10, HalfEven, 14}, {-125, 10, HalfEven, -12},
		{125, 10, HalfUp, 13}, {-125, 10, HalfUp, -13},
		{129, 10, Down, 12}, {-129, 10, Down, -12},
		{121, 10, Up, 13}, {-121, 10, Up, -13},
		{120, 10, HalfEven, 12},
	}
	for _, c := range cases {
		if got := DivideRounded(c.dividend, c.divisor, c.mode); got != c.want {
			t.Errorf("DivideRounded(%d, %d, %v) = %d, want %d", c.dividend, c.divisor, c.mode, got, c.want)
		}
	}
}

func TestTimes(t *testing.T) {
	fee, _ := parse(t, "19.99", USD).Times(29, 1000, HalfEven)
	if fee.MinorUnits != 58 {
		t.Fatalf("fee = %v", fee)
	}
	triple, _ := parse(t, "19.99", USD).Times(3, 1, HalfEven)
	if triple.MinorUnits != 5997 {
		t.Fatalf("triple = %v", triple)
	}
	if _, err := OfMinor(1, USD).Times(1, 0, Down); err == nil {
		t.Fatal("zero denominator should fail")
	}
}

func TestAllocate(t *testing.T) {
	thirds, err := parse(t, "100.00", USD).Allocate(1, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if thirds[0].MinorUnits != 3334 || thirds[1].MinorUnits != 3333 || thirds[2].MinorUnits != 3333 {
		t.Fatalf("thirds = %v", thirds)
	}
	total, _ := Sum(USD, thirds...)
	if total != parse(t, "100.00", USD) {
		t.Fatalf("thirds do not sum to whole: %v", total)
	}
	split, _ := parse(t, "0.05", USD).Allocate(70, 30)
	if split[0].MinorUnits != 4 || split[1].MinorUnits != 1 {
		t.Fatalf("70/30 split = %v", split)
	}
	neg, _ := parse(t, "-1.00", USD).Allocate(1, 1, 1)
	negTotal, _ := Sum(USD, neg...)
	if negTotal.MinorUnits != -100 {
		t.Fatalf("negative allocation = %v", neg)
	}
	one, _ := OfMinor(1, USD).Allocate(1, 1, 1)
	if one[0].MinorUnits != 1 || one[1].MinorUnits != 0 || one[2].MinorUnits != 0 {
		t.Fatalf("one cent three ways = %v", one)
	}
}

func TestAllocateRejects(t *testing.T) {
	if _, err := OfMinor(1, USD).Allocate(0, 0); err == nil {
		t.Fatal("all-zero ratios should fail")
	}
	if _, err := OfMinor(1, USD).Allocate(1, -1); err == nil {
		t.Fatal("negative ratio should fail")
	}
	if _, err := OfMinor(1, USD).Allocate(); err == nil {
		t.Fatal("no ratios should fail")
	}
}

func TestCurrencyValidation(t *testing.T) {
	if _, err := NewCurrency("usd", 2); err == nil {
		t.Fatal("lowercase code should fail")
	}
	if _, err := NewCurrency("ABC", 7); err == nil {
		t.Fatal("exponent 7 should fail")
	}
	if KWD.MinorUnitsPerMajor() != 1000 {
		t.Fatal("KWD scale")
	}
	empty, _ := Sum(USD)
	if !empty.IsZero() {
		t.Fatal("empty sum should be zero")
	}
}
