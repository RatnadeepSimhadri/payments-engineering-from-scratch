// Tests for phases/10-money-and-ledgers/09-multi-currency-ledgers. Run: go test ./...
package main

import (
	"errors"
	"fmt"
	"testing"
)

func ok[T any](t *testing.T) func(T, error) T {
	return func(v T, err error) T {
		t.Helper()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		return v
	}
}

func m(t *testing.T, s string, c Currency) Money {
	t.Helper()
	return ok[Money](t)(Parse(s, c))
}

func eurUsd(t *testing.T, num int64) FxRate {
	t.Helper()
	return ok[FxRate](t)(NewFxRate(EUR, USD, num, 10_000, "test", 0))
}

func ledger(t *testing.T) *MultiCurrencyLedger {
	t.Helper()
	l := NewMultiCurrencyLedger(USD)
	for _, a := range []struct {
		id, name string
		typ      AccountType
		cur      Currency
	}{
		{"1200-USD", "Receivable", Asset, USD}, {"1010-USD", "Bank", Asset, USD}, {"1010-EUR", "Bank", Asset, EUR},
		{"2100-EUR", "Merchant Payable", Liability, EUR}, {"1010-GBP", "Bank", Asset, GBP},
	} {
		ok[Account](t)(l.OpenAccount(a.id, a.name, a.typ, a.cur))
	}
	return l
}

func exchange(t *testing.T, l *MultiCurrencyLedger, id, debit string, amount Money, credit string, rate FxRate) Transaction {
	t.Helper()
	return ok[Transaction](t)(l.Exchange(id, "x", debit, amount, credit, rate))
}

func netDebit(t *testing.T, l *MultiCurrencyLedger, id string) int64 {
	t.Helper()
	return ok[Money](t)(l.NetDebit(id)).MinorUnits
}

func balance(t *testing.T, l *MultiCurrencyLedger, id string) int64 {
	t.Helper()
	return ok[Money](t)(l.Balance(id)).MinorUnits
}

func size(t *testing.T, l *MultiCurrencyLedger, c Currency) int {
	t.Helper()
	return ok[*SubLedger](t)(l.SubLedger(c)).Size()
}

func TestFxRateConversion(t *testing.T) {
	jpy := ok[FxRate](t)(NewFxRate(USD, JPY, 15_725, 100, "t", 0))
	cases := []struct {
		name   string
		rate   FxRate
		amount Money
		want   Money
	}{
		{"EUR->USD at 1.1000", eurUsd(t, 11_000), m(t, "100.00", EUR), m(t, "110.00", USD)},
		{"USD->EUR at 1.1000", eurUsd(t, 11_000), m(t, "110.00", USD), m(t, "100.00", EUR)},
		{"USD->EUR rounds half even (1.00 / 1.1 = 0.909 -> 0.91)", eurUsd(t, 11_000), m(t, "1.00", USD), OfMinor(91, EUR)},
		{"USD->JPY scales exponent, tie to even (10.00 USD = 1572.5 -> 1572 JPY)", jpy, m(t, "10.00", USD), m(t, "1572", JPY)},
		{"JPY->USD scales exponent (5000 JPY -> 31.80 USD)", jpy, m(t, "5000", JPY), m(t, "31.80", USD)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ok[Money](t)(c.rate.Convert(c.amount, HalfEven)); got != c.want {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}
	if _, err := eurUsd(t, 11_000).Convert(m(t, "1.00", GBP), HalfEven); !errors.Is(err, ErrCurrencyMismatch) {
		t.Errorf("rate rejects unrelated currency: %v", err)
	}
	if _, err := NewFxRate(USD, USD, 1, 1, "t", 0); err == nil {
		t.Error("rate rejects same currency")
	}
	if _, err := NewFxRate(EUR, USD, 0, 1, "t", 0); err == nil {
		t.Error("rate rejects non-positive")
	}
}

func TestNoMixedCurrencyEntries(t *testing.T) {
	_, err := NewJournalEntry("x", USD, []Posting{{"1200-USD", Debit, m(t, "1.00", USD)}, {"2100-EUR", Credit, m(t, "1.00", EUR)}})
	if !errors.Is(err, ErrMixedCurrency) || !errors.Is(err, ErrLedger) {
		t.Errorf("mixed-currency entry rejected with ErrMixedCurrency: %v", err)
	}
	l := ledger(t)
	e := ok[JournalEntry](t)(EntryOf("t/EUR", Posting{"1010-EUR", Debit, m(t, "1.00", EUR)}, Posting{"1200-USD", Credit, m(t, "1.00", EUR)}))
	if _, err := l.Post(NewTransaction("t", "d", []JournalEntry{e}, nil)); !errors.Is(err, ErrLedger) {
		t.Errorf("entry posted to an account of another currency rejected: %v", err)
	}
	if size(t, l, EUR) != 0 || size(t, l, USD) != 0 {
		t.Error("rejected transaction left no trace")
	}
}

func TestExchangeThroughClearing(t *testing.T) {
	l := ledger(t)
	tx := exchange(t, l, "fx-1", "1200-USD", m(t, "110.00", USD), "2100-EUR", eurUsd(t, 11_000))
	if len(tx.Entries) != 2 || tx.Entries[0].Currency != USD || tx.Entries[1].Currency != EUR {
		t.Error("exchange produces one entry per currency")
	}
	if netDebit(t, l, "1900-USD") != -11_000 || netDebit(t, l, "1900-EUR") != 10_000 {
		t.Error("clearing balances mirror the legs")
	}
	if !l.AllSubLedgersBalance() {
		t.Error("each sub-ledger balances")
	}
	if l.PositionOf(EUR).MinorUnits != -10_000 {
		t.Error("EUR position is -100.00")
	}
	if ok[Position](t)(l.PositionReport(EUR)).Basis.MinorUnits != -11_000 {
		t.Error("EUR basis is -110.00 USD")
	}
	if !l.ClearingMatchesBasis() {
		t.Error("clearing matches basis after exchange")
	}
	again := exchange(t, l, "fx-1", "1200-USD", m(t, "110.00", USD), "2100-EUR", eurUsd(t, 11_000))
	if !again.Equal(tx) || size(t, l, USD) != 1 || ok[Position](t)(l.PositionReport(EUR)).Basis.MinorUnits != -11_000 {
		t.Error("exchange replay is idempotent")
	}
	if _, err := l.Exchange("fx-1", "x", "1200-USD", m(t, "120.00", USD), "2100-EUR", eurUsd(t, 11_000)); !errors.Is(err, ErrLedger) {
		t.Errorf("exchange id reused with different amount: %v", err)
	}
	eurGbp := ok[FxRate](t)(NewFxRate(EUR, GBP, 85, 100, "t", 0))
	if _, err := l.Exchange("fx-x", "eur->gbp", "1010-EUR", m(t, "10.00", EUR), "1010-GBP", eurGbp); !errors.Is(err, ErrLedger) {
		t.Errorf("cross pair must route through functional: %v", err)
	}
	if _, err := l.Exchange("fx-y", "usd->usd", "1200-USD", m(t, "1.00", USD), "1010-USD", eurUsd(t, 11_000)); !errors.Is(err, ErrLedger) {
		t.Errorf("same-currency exchange rejected: %v", err)
	}
}

func TestRevaluationAndRealization(t *testing.T) {
	l := ledger(t)
	exchange(t, l, "fx-1", "1200-USD", m(t, "110.00", USD), "2100-EUR", eurUsd(t, 11_000))
	if !ok[Money](t)(l.Revalue(EUR, eurUsd(t, 11_000))).IsZero() {
		t.Error("revalue at the booked rate books nothing")
	}
	if loss := ok[Money](t)(l.Revalue(EUR, eurUsd(t, 11_200))); loss.MinorUnits != -200 {
		t.Errorf("EUR up to 1.12 on a short EUR position is a 2.00 loss: %v", loss)
	}
	if balance(t, l, Unrealized) != -200 {
		t.Error("loss lands in 4900 (revenue, negative)")
	}
	if netDebit(t, l, "1950-EUR") != -200 {
		t.Error("adjustment account carries the revaluation")
	}
	if size(t, l, EUR) != 1 {
		t.Error("revaluation posts only in functional currency")
	}
	if !ok[Money](t)(l.Revalue(EUR, eurUsd(t, 11_200))).IsZero() || size(t, l, USD) != 2 {
		t.Error("revalue twice at same rate is a no-op")
	}
	if back := ok[Money](t)(l.Revalue(EUR, eurUsd(t, 10_900))); back.MinorUnits != 300 {
		t.Errorf("EUR falls to 1.09: gain of 3.00 relative to carrying 112.00: %v", back)
	}
	if ok[Position](t)(l.PositionReport(EUR)).CarryingValue().MinorUnits != -10_900 {
		t.Error("cumulative unrealized = fair - basis")
	}
	if _, err := l.Revalue(USD, eurUsd(t, 11_000)); !errors.Is(err, ErrLedger) {
		t.Errorf("revalue functional currency rejected: %v", err)
	}
	if _, err := l.Realize(EUR); !errors.Is(err, ErrLedger) {
		t.Errorf("realize an open position rejected: %v", err)
	}

	// realization
	ok[bool](t)(l.Post(single("stl", "settle", "stl/USD", "1010-USD", "1200-USD", m(t, "110.00", USD))))
	exchange(t, l, "fx-2", "1010-EUR", m(t, "100.00", EUR), "1010-USD", eurUsd(t, 11_150))
	ok[bool](t)(l.Post(single("out", "payout", "out/EUR", "2100-EUR", "1010-EUR", m(t, "100.00", EUR))))
	if !l.PositionOf(EUR).IsZero() {
		t.Error("position flat after buying and paying out")
	}
	if netDebit(t, l, "1900-USD") != 150 || !l.ClearingMatchesBasis() {
		t.Error("functional clearing holds the round-trip cost")
	}
	if realized := ok[Money](t)(l.Realize(EUR)); realized.MinorUnits != -150 {
		t.Errorf("realized loss is 1.50: %v", realized)
	}
	if balance(t, l, Realized) != -150 {
		t.Error("4910 shows the realized loss")
	}
	if balance(t, l, Unrealized) != 0 || netDebit(t, l, "1950-EUR") != 0 {
		t.Error("unrealized fully unwound")
	}
	if netDebit(t, l, "1900-USD") != 0 {
		t.Error("functional clearing empty after realize")
	}
	if balance(t, l, "1010-USD") != -150 {
		t.Error("total FX P&L equals the bank's loss")
	}
	if !l.AllSubLedgersBalance() {
		t.Error("all sub-ledgers balance at the end")
	}
}

func TestLongPosition(t *testing.T) {
	l := ledger(t)
	exchange(t, l, "pre", "1010-EUR", m(t, "500.00", EUR), "1010-USD", eurUsd(t, 10_900))
	if ok[Position](t)(l.PositionReport(EUR)).Basis.MinorUnits != 54_500 || l.PositionOf(EUR).MinorUnits != 50_000 {
		t.Error("long position basis +545.00")
	}
	if ok[Money](t)(l.Revalue(EUR, eurUsd(t, 11_200))).MinorUnits != 1_500 || balance(t, l, Unrealized) != 1_500 {
		t.Error("long EUR position gains 15.00 at 1.12")
	}
}

func TestPropertyRandomExchanges(t *testing.T) {
	r := ledger(t)
	rnd := NewJavaRandom(17)
	for i := range 500 {
		rate := int64(10_000 + rnd.NextInt(3_000))
		amount := int64(1 + rnd.NextInt(1_000_000))
		cur := EUR
		if rnd.NextBoolean() {
			cur = USD
		}
		amt := OfMinor(amount, cur)
		id := fmt.Sprintf("e%d", i)
		if cur == USD {
			exchange(t, r, id, "1200-USD", amt, "2100-EUR", eurUsd(t, rate))
		} else {
			exchange(t, r, id, "1010-EUR", amt, "1010-USD", eurUsd(t, rate))
		}
		if i%50 == 0 {
			ok[Money](t)(r.Revalue(EUR, eurUsd(t, int64(10_000+rnd.NextInt(3_000)))))
		}
		if !r.AllSubLedgersBalance() || !r.ClearingMatchesBasis() {
			t.Fatalf("property broken after exchange %d", i)
		}
	}
	pr := ok[Position](t)(r.PositionReport(EUR))
	fair := ok[Money](t)(eurUsd(t, 11_000).Convert(pr.Position, HalfEven))
	ok[Money](t)(r.Revalue(EUR, eurUsd(t, 11_000)))
	if ok[Position](t)(r.PositionReport(EUR)).CarryingValue() != fair {
		t.Error("property: after revalue, carrying value == fair value")
	}
}
