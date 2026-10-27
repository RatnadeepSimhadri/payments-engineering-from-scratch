// Stage 1 tests for projects/ledger-engine (Go): Money is present, account types know their normal side,
// the registry rejects duplicates and unknown ids. Run by scripts/project_test.py via go test ./...
// Port of Stage01Test.java: same checks, same names, same order. Helpers are prefixed s01 because every
// stage's test file compiles into the same package in the grader's sandbox.
package main

import (
	"errors"
	"math"
	"testing"
)

func s01check(t *testing.T, name string, condition bool) {
	t.Helper()
	if !condition {
		t.Errorf("FAIL: %s", name)
	}
}

// s01checkErr expects err to be (or wrap) want; a nil want accepts any non-nil error.
func s01checkErr(t *testing.T, name string, err, want error) {
	t.Helper()
	if err == nil {
		t.Errorf("FAIL: %s (no error)", name)
		return
	}
	if want != nil && !errors.Is(err, want) {
		t.Errorf("FAIL: %s (wrong error: %v)", name, err)
	}
}

func s01usd(t *testing.T, text string) Money {
	t.Helper()
	m, err := Parse(text, USD)
	if err != nil {
		t.Fatalf("Parse(%q): %v", text, err)
	}
	return m
}

// s01minor folds a (Money, error) result into its minor units; an error becomes math.MinInt64, which no check expects.
func s01minor(m Money, err error) int64 {
	if err != nil {
		return math.MinInt64
	}
	return m.MinorUnits
}

func TestStage01Money(t *testing.T) {
	// Money: parse exactly, never from a float
	s01check(t, "money parse", s01minor(Parse("12.34", USD)) == 1234)
	s01check(t, "money parse pads", s01minor(Parse("12", USD)) == 1200)
	s01check(t, "money parse .99", s01minor(Parse(".99", USD)) == 99)
	s01check(t, "money parse negative", s01minor(Parse("-0.07", USD)) == -7)
	s01check(t, "money parse JPY", s01minor(Parse("1200", JPY)) == 1200)
	s01check(t, "money parse KWD", s01minor(Parse("1.250", KWD)) == 1250)
	_, err := Parse("1.005", USD)
	s01checkErr(t, "money parse excess precision", err, ErrExcessPrecision)
	_, err = Parse("12,34", USD)
	s01checkErr(t, "money parse garbage", err, ErrNotDecimal)
	s01check(t, "money String round-trips", s01usd(t, "12.05").String() == "12.05 USD")

	// Money: arithmetic stays in the currency and in integers
	sum, err := s01usd(t, "1.10").Plus(s01usd(t, "2.20"))
	s01check(t, "money plus", err == nil && sum == s01usd(t, "3.30"))
	diff, err := s01usd(t, "1.00").Minus(s01usd(t, "2.50"))
	s01check(t, "money minus goes negative", err == nil && diff == s01usd(t, "-1.50"))
	_, err = s01usd(t, "1.00").Plus(must(Parse("1.00", EUR)))
	s01checkErr(t, "money plus currency mismatch", err, ErrCurrencyMismatch)
	_, err = s01usd(t, "1.00").Minus(must(Parse("1.00", GBP)))
	s01checkErr(t, "money minus currency mismatch", err, ErrCurrencyMismatch)
	_, err = OfMinor(math.MaxInt64, USD).Plus(OfMinor(1, USD))
	s01checkErr(t, "money plus overflow", err, ErrOverflow)
	zero, err := Sum(USD)
	s01check(t, "money sum of nothing is zero", err == nil && zero.IsZero())

	// Money: times rounds exactly once, the way you asked
	hundred := s01usd(t, "100.00")
	s01check(t, "2.9% fee of 100.00", s01minor(hundred.Times(29, 1000, HalfEven)) == 290)
	tenPointOhFive := s01usd(t, "10.05")
	s01check(t, "half-even tie goes to even (10.05 * 50%)", s01minor(tenPointOhFive.Times(1, 2, HalfEven)) == 502)
	s01check(t, "half-up tie goes up (10.05 * 50%)", s01minor(tenPointOhFive.Times(1, 2, HalfUp)) == 503)
	s01check(t, "down truncates", s01minor(tenPointOhFive.Times(1, 2, Down)) == 502)
	s01check(t, "up ceils", s01minor(s01usd(t, "10.01").Times(1, 2, Up)) == 501)
	s01check(t, "negative half-up rounds away from zero", s01minor(s01usd(t, "-10.05").Times(1, 2, HalfUp)) == -503)
	s01check(t, "negative down truncates toward zero", s01minor(s01usd(t, "-10.05").Times(1, 2, Down)) == -502)
	_, err = hundred.Times(1, 0, HalfEven)
	s01checkErr(t, "times rejects zero denominator", err, ErrBadDenominator)
	s01check(t, "DivideRounded 7/2 half-even is 4", DivideRounded(7, 2, HalfEven) == 4)
	s01check(t, "DivideRounded 5/2 half-even is 2", DivideRounded(5, 2, HalfEven) == 2)
	s01check(t, "DivideRounded -7/2 half-up is -4", DivideRounded(-7, 2, HalfUp) == -4)

	// Money: allocate never loses or invents a minor unit
	thirds, err := hundred.Allocate(1, 1, 1)
	if err != nil || len(thirds) != 3 {
		t.Fatalf("allocate thirds: %v %v", thirds, err)
	}
	total, _ := Sum(USD, thirds...)
	s01check(t, "allocate thirds sums to whole", total == hundred)
	s01check(t, "allocate thirds gives 33.34/33.33/33.33", thirds[0].MinorUnits == 3334 && thirds[1].MinorUnits == 3333 && thirds[2].MinorUnits == 3333)
	split, err := s01usd(t, "0.05").Allocate(70, 30)
	s01check(t, "allocate 5 cents 70/30 is 4/1", err == nil && len(split) == 2 && split[0].MinorUnits == 4 && split[1].MinorUnits == 1)
	negative, err := s01usd(t, "-1.00").Allocate(1, 2)
	s01check(t, "allocate negative keeps sign and sums", err == nil && len(negative) == 2 && negative[0].MinorUnits == -33 && negative[1].MinorUnits == -67)
	_, err = hundred.Allocate()
	s01checkErr(t, "allocate rejects no ratios", err, ErrBadRatios)
	_, err = hundred.Allocate(0, 0)
	s01checkErr(t, "allocate rejects all-zero ratios", err, ErrBadRatios)
	_, err = hundred.Allocate(1, -1)
	s01checkErr(t, "allocate rejects negative ratio", err, ErrBadRatios)
}

func TestStage01Accounts(t *testing.T) {
	// normal balance side per account class
	s01check(t, "Asset grows on Debit", Asset.NormalSide() == Debit)
	s01check(t, "Expense grows on Debit", Expense.NormalSide() == Debit)
	s01check(t, "Liability grows on Credit", Liability.NormalSide() == Credit)
	s01check(t, "Equity grows on Credit", Equity.NormalSide() == Credit)
	s01check(t, "Revenue grows on Credit", Revenue.NormalSide() == Credit)
	s01check(t, "Debit opposite is Credit", Debit.Opposite() == Credit)
	s01check(t, "Credit opposite is Debit", Credit.Opposite() == Debit)

	// the account value
	bank, err := NewAccount("bank", Asset, USD)
	s01check(t, "account keeps id", err == nil && bank.ID == "bank")
	s01check(t, "account keeps type", bank.Type == Asset)
	s01check(t, "account keeps currency", bank.Currency == USD)
	s01check(t, "account normal side delegates to type", bank.NormalSide() == Debit)
	sellers, _ := NewAccount("sellers", Liability, USD)
	s01check(t, "liability account normal side", sellers.NormalSide() == Credit)
	s01check(t, "accounts are values", bank == Account{"bank", Asset, USD})
	_, err = NewAccount("  ", Asset, USD)
	s01checkErr(t, "blank account id rejected", err, nil)
	_, err = NewAccount("", Asset, USD)
	s01checkErr(t, "empty account id rejected", err, nil)
	_, err = NewAccount("bank", Asset, Currency{})
	s01checkErr(t, "zero currency rejected", err, nil)

	// the registry
	chart := NewAccountRegistry()
	s01check(t, "empty registry has size 0", chart.Size() == 0)
	s01check(t, "empty registry contains nothing", !chart.Contains("bank"))
	opened, err := chart.Open("bank", Asset, USD)
	s01check(t, "open returns the account", err == nil && opened == bank)
	s01check(t, "open makes it visible", chart.Contains("bank"))
	got, err := chart.Get("bank")
	s01check(t, "get returns the same account", err == nil && got == bank)
	chart.Open("sellers", Liability, USD)
	chart.Open("eur-bank", Asset, EUR)
	s01check(t, "size counts opened accounts", chart.Size() == 3)
	_, err = chart.Open("bank", Asset, USD)
	s01checkErr(t, "duplicate id rejected", err, ErrDuplicateAccount)
	_, err = chart.Open("bank", Liability, EUR)
	s01checkErr(t, "duplicate id rejected even with different type", err, ErrDuplicateAccount)
	s01check(t, "failed open does not change size", chart.Size() == 3)
	got, _ = chart.Get("bank")
	s01check(t, "failed open does not change the original", got.Type == Asset)
	_, err = chart.Get("nope")
	s01checkErr(t, "unknown id rejected", err, ErrUnknownAccount)
	_, err = chart.Open("bank", Asset, USD)
	s01checkErr(t, "duplicate is a ledger error", err, ErrLedger)
	_, err = chart.Get("nope")
	s01checkErr(t, "unknown is a ledger error", err, ErrLedger)
	all := chart.All()
	s01check(t, "All() keeps opening order", len(all) == 3 && all[0].ID == "bank" && all[1].ID == "sellers" && all[2].ID == "eur-bank")
	all[0] = Account{"x", Asset, USD}
	all = append(all, Account{"y", Asset, USD})
	s01check(t, "All() is a read-only view", chart.All()[0].ID == "bank" && len(chart.All()) == 3)
	s01check(t, "All() is a copy", chart.Size() == 3 && len(chart.All()) == 3)
}
