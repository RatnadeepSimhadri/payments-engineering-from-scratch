// Tests for phases/14-payments-at-scale/05-batch-vs-real-time-settlement. Run: go test ./...
package main

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func check(t *testing.T, name string, condition bool) {
	t.Helper()
	if !condition {
		t.Errorf("%s", name)
	}
}

// ok unwraps a (value, error) pair; a test calls ok(t)(f()) because Go cannot splice a multi-value call
// after another argument. Fails the test on an error.
func ok[T any](t *testing.T) func(T, error) T {
	return func(v T, err error) T {
		t.Helper()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		return v
	}
}

func instant(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func event(id, merchant string, typ EventType, amount Money, at time.Time) SettlementEvent {
	e, err := NewSettlementEvent(id, merchant, typ, amount, at)
	if err != nil {
		panic(err)
	}
	return e
}

func demoPlatform(t *testing.T) *Platform {
	t.Helper()
	p := NewPlatform(USD, StandardFees(USD))
	for _, m := range DemoMerchants() {
		p.AddMerchant(m)
	}
	for _, e := range DemoDay() {
		okBool(t)(p.Book(e))
	}
	return p
}

var t10 = instant("2026-03-31T10:00:00Z")

func TestFees(t *testing.T) {
	fees := StandardFees(USD)
	check(t, "fee on 45.50 is 1.62", okMoney(t)(fees.FeeFor(event("x", "m", Capture, usd("45.50"), t10))) == usd("1.62"))
	check(t, "fee on 1000.00 is 29.30", okMoney(t)(fees.FeeFor(event("x", "m", Capture, usd("1000.00"), t10))) == usd("29.30"))
	check(t, "refund returns no fee", okMoney(t)(fees.MerchantEffect(event("x", "m", Refund, usd("20.00"), t10))) == usd("-20.00"))
	check(t, "chargeback costs amount plus 15.00", okMoney(t)(fees.MerchantEffect(event("x", "m", Chargeback, usd("60.00"), t10))) == usd("-75.00"))
	_, err := NewSettlementEvent("x", "m", Refund, usd("-1.00"), t10)
	check(t, "event amounts must be positive", err != nil)
	_, err = NewMerchant("m", "12345", "a", false)
	check(t, "routing number must be 9 digits", err != nil)
}

func TestWorkedExampleNetting(t *testing.T) {
	p := demoPlatform(t)
	s := NewBatchSettler(p, Zero(USD))
	b := okBatch(t)(s.Run(Day, Cutoff))
	check(t, "two payout instructions", len(b.Instructions) == 2)
	check(t, "acme nets 173.60", b.Instructions[0].MerchantId == "m_acme" && b.Instructions[0].Amount == usd("173.60"))
	check(t, "cora nets 1213.15", b.Instructions[1].Amount == usd("1213.15"))
	check(t, "batch total 1386.75", b.Total() == usd("1386.75"))
	check(t, "bolt negative net is carried, not paid", b.CarriedForward["m_bolt"] == usd("-31.12"))
	check(t, "acquirer funding 1415.50", b.ProcessorFunding == usd("1415.50"))
	check(t, "event after cutoff is not settled", !s.IsSettled("c5") && s.IsSettled("c4"))
	check(t, "seven events in the batch", len(b.EventIds) == 7)
	check(t, "ties out after batch", s.TiesOut())
	check(t, "trial balance zero", p.Ledger.TrialBalance() == 0)

	// idempotent rerun
	journals := p.Ledger.JournalCount()
	again := okBatch(t)(s.Run(Day, Cutoff))
	check(t, "rerun returns the same batch", again == b)
	check(t, "rerun posts nothing", p.Ledger.JournalCount() == journals)
	fresh := okBool(t)(p.Book(DemoDay()[0]))
	check(t, "rebooking an event is a no-op", !fresh && p.Ledger.JournalCount() == journals)

	// settlement file
	file := SettlementFile.Render(b)
	check(t, "file trailer count, total, hash", strings.Contains(file, "T|2|138675|0003200015"))
	check(t, "file validates", SettlementFile.Validate(file))
	check(t, "tampered file fails validation", !SettlementFile.Validate(strings.ReplaceAll(file, "|17360", "|17361")))

	// funding then payout: cash never negative in batch mode
	okNil(t, p.ReceiveFunding(b.BatchId, b.ProcessorFunding))
	okNil(t, s.ConfirmPayouts(b))
	check(t, "cash after batch is fees minus bolt deficit", p.Ledger.Balance(Cash) == usd("28.75"))
	check(t, "batch needs no prefunding", p.Ledger.PeakPrefunding() == Zero(USD))
	check(t, "in transit cleared", p.Ledger.Balance(PayoutsInTransit).MinorUnits == 0)

	// next day: c5 rolls in, bolt's deficit nets against new captures
	okBool(t)(p.Book(event("c7", "m_bolt", Capture, usd("100.00"), instant("2026-04-01T09:00:00Z"))))
	day2 := okBatch(t)(s.Run(time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC), instant("2026-04-01T23:00:00Z")))
	check(t, "day 2 has two payouts", len(day2.Instructions) == 2)
	check(t, "bolt day 2 = 96.80 - 31.12", day2.Instructions[0].Amount == usd("65.68"))
	check(t, "cora day 2 = c5 net 9.41", day2.Instructions[1].Amount == usd("9.41"))
	check(t, "ties out after day 2", s.TiesOut() && s.Carried("m_bolt").MinorUnits == 0)
}

func TestHoldsMinimumPayoutAndLedgerSafety(t *testing.T) {
	h := NewPlatform(USD, StandardFees(USD))
	h.AddMerchant(okMerchant(t)(NewMerchant("m_hold", "021000021", "1", true)))
	h.AddMerchant(okMerchant(t)(NewMerchant("m_tiny", "021000021", "2", false)))
	okBool(t)(h.Book(event("h1", "m_hold", Capture, usd("500.00"), t10)))
	okBool(t)(h.Book(event("h2", "m_tiny", Capture, usd("1.00"), t10)))
	hs := NewBatchSettler(h, usd("5.00"))
	hb := okBatch(t)(hs.Run(Day, Cutoff))
	check(t, "merchant on hold is not paid and its events stay open", len(hb.Instructions) == 0 && !hs.IsSettled("h1"))
	check(t, "below-minimum net is carried positive", hs.Carried("m_tiny") == usd("0.67"))
	check(t, "holds and carries still tie out", hs.TiesOut())

	_, err := h.Ledger.Post("bad", []Posting{{"a", 100}, {"b", -99}})
	check(t, "unbalanced journal rejected", errors.Is(err, ErrUnbalancedJournal))
	eur := okMoney(t)(Parse("1.00", EUR))
	_, err = h.Book(event("e1", "m_tiny", Capture, eur, t10))
	check(t, "foreign-currency event rejected", errors.Is(err, ErrCurrencyMismatch))
	_, err = h.Book(event("u1", "m_nobody", Capture, usd("1.00"), t10))
	check(t, "unknown merchant rejected", errors.Is(err, ErrUnknownMerchant))
}

func TestRealTimeVersusBatch(t *testing.T) {
	cmp := okCmp(t)(RunLiquidityComparison(DemoMerchants(), DemoDay(), Day, Cutoff, USD))
	check(t, "real time sends one transfer per positive sweep", cmp.RealTimeTransfers == 5 && cmp.BatchTransfers == 2)
	check(t, "real time prefunds everything it pays", cmp.RealTimePeakPrefunding == usd("1450.63"))
	check(t, "real time exposure is refund plus chargeback", cmp.RealTimeExposure == usd("95.00"))
	check(t, "real time cash after funding is fees minus exposure", cmp.RealTimeCashAfter == usd("-35.13"))
	check(t, "batch cash after funding", cmp.BatchCashAfter == usd("28.75") && cmp.BatchPeakPrefunding.MinorUnits == 0)
}

func TestLargeSeededDay(t *testing.T) {
	many := GeneratedMerchants(50)
	big := NewPlatform(USD, StandardFees(USD))
	for _, m := range many {
		big.AddMerchant(m)
	}
	for _, e := range GeneratedDay(many, 5_000, 11) {
		okBool(t)(big.Book(e))
	}
	bs := NewBatchSettler(big, usd("1.00"))
	bb := okBatch(t)(bs.Run(Day, instant("2026-04-01T00:00:00Z")))
	check(t, "large batch ties out", bs.TiesOut())
	check(t, "large batch file validates", SettlementFile.Validate(SettlementFile.Render(bb)))
	check(t, "at most one payout per merchant", len(bb.Instructions) <= len(many))
}

func TestJavaRandomMatchesTheJdk(t *testing.T) {
	// Values taken from java.util.Random(2026): nextInt(), nextInt(66), nextInt(64), nextLong().
	r := NewJavaRandom(2026)
	check(t, "nextInt()", r.NextInt() == -1637959697)
	check(t, "nextInt(66)", r.NextIntBound(66) == 52)
	check(t, "nextInt(64)", r.NextIntBound(64) == 54)
	check(t, "nextLong()", r.NextLong() == 3301047695993903762)
}

var (
	okMoney    = ok[Money]
	okBool     = ok[bool]
	okBatch    = ok[*SettlementBatch]
	okMerchant = ok[Merchant]
	okCmp      = ok[LiquidityComparison]
)

func okNil(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
