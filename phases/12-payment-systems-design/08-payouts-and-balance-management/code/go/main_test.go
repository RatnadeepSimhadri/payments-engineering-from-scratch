// Tests for phases/12-payment-systems-design/08-payouts-and-balance-management. Run: go test ./...
package main

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

func check(t *testing.T, name string, condition bool) {
	t.Helper()
	if !condition {
		t.Errorf("FAIL: %s", name)
	}
}

type fixture struct {
	clock *FakeClock
	files *InMemoryPayoutFileSink
	svc   *BalanceService
}

// newFixture: Monday 2026-03-02; merchant "m" pays out daily with a 10.00 minimum and a reserveBps / 90-day reserve.
func newFixture(t *testing.T, reserveBps int64) *fixture {
	t.Helper()
	clock := NewFakeClock(Date(2026, time.March, 2))
	files := NewInMemoryPayoutFileSink()
	svc := NewBalanceService(NewLedger(USD), clock, files, 7, USD)
	policy, err := NewReservePolicy(reserveBps, 90)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Onboard(Merchant{"m", "ba_tok_1", PayoutSchedule{DAILY, time.Sunday, usd(1_000)}, policy}); err != nil {
		t.Fatal(err)
	}
	return &fixture{clock, files, svc}
}

func (f *fixture) capture(t *testing.T, id string, gross Money, feeBps, fixed int64) CaptureRecord {
	t.Helper()
	c, err := f.svc.Capture(id, "m", gross, feeBps, fixed)
	if err != nil {
		t.Fatalf("capture %s: %v", id, err)
	}
	return c
}

func (f *fixture) settle(t *testing.T, id string, ids []string, total Money) {
	t.Helper()
	if err := f.svc.Settle(SettlementReport{id, f.clock.Today(), ids, total}); err != nil {
		t.Fatalf("settle %s: %v", id, err)
	}
}

func (f *fixture) run(t *testing.T) []*Payout {
	t.Helper()
	batch, err := f.svc.RunPayouts()
	if err != nil {
		t.Fatal(err)
	}
	return batch
}

func (f *fixture) balance(t *testing.T) Balance {
	t.Helper()
	b, err := f.svc.Balance("m")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func (f *fixture) ledgerBalance(t *testing.T, acct string) Money {
	t.Helper()
	m, err := f.svc.Ledger().Balance(acct)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func (f *fixture) release(t *testing.T) Money {
	t.Helper()
	m, err := f.svc.ReleaseReserves()
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func consistent(t *testing.T, f *fixture) bool {
	t.Helper()
	return f.svc.Ledger().Balances() && f.svc.ScheduledReserve("m") == f.balance(t).Reserve
}

func TestPendingIsNotPayableAndSettlementReconciles(t *testing.T) {
	f := newFixture(t, 1_000)
	svc := f.svc
	c1 := f.capture(t, "c1", usd(10_000), 290, 30)
	check(t, "net = 100.00 - 3.20", c1.Net() == usd(9_680))
	check(t, "capture lands in pending", f.balance(t).Pending == usd(9_680) && f.balance(t).Available.IsZero())
	check(t, "no payout from pending funds", len(f.run(t)) == 0 && len(f.files.Files) == 0)
	f.clock.AdvanceDays(10)
	check(t, "time alone does not settle anything", len(f.run(t)) == 0 && !svc.IsSettled("c1"))

	err := svc.Settle(SettlementReport{"bad", f.clock.Today(), []string{"c1"}, usd(9_999)})
	check(t, "report total mismatch refused", errors.Is(err, ErrIllegalState))
	err = svc.Settle(SettlementReport{"bad2", f.clock.Today(), []string{"c1", "nope"}, usd(10_000)})
	check(t, "unknown capture in report refused", errors.Is(err, ErrIllegalState))
	check(t, "refused reports changed nothing", f.balance(t).Pending == usd(9_680) && f.ledgerBalance(t, BANK).IsZero())
	f.settle(t, "s1", []string{"c1"}, usd(10_000))
	check(t, "settled: reserve = floor(9680 x 10%) = 9.68", f.balance(t).Reserve == usd(968))
	check(t, "settled: available = net - reserve = 87.12", f.balance(t).Available == usd(8_712) && f.balance(t).Pending.IsZero())
	check(t, "cash arrived in bank", f.ledgerBalance(t, BANK) == usd(10_000))
	err = svc.Settle(SettlementReport{"s1b", f.clock.Today(), []string{"c1"}, usd(10_000)})
	check(t, "settling the same capture twice refused", errors.Is(err, ErrIllegalState))
	check(t, "tranches match reserve account", consistent(t, f))

	// --- payout and its file (2026-03-12, a Thursday)
	batch := f.run(t)
	check(t, "one payout of all of available", len(batch) == 1 && batch[0].Amount == usd(8_712))
	check(t, "available moved to in transit", f.balance(t).Available.IsZero() && f.balance(t).InTransit == usd(8_712))
	file := f.files.Last()
	check(t, "file has header, one detail, trailer", len(file) == 3 && file[0][:2] == "H," && file[2][:2] == "T,")
	check(t, "trailer control totals: 1 line, 8712 credits, 0 debits", file[2] == "T,1,8712,0")
	check(t, "detail line names payout, merchant, bank token, direction, minor units", file[1] == "D,"+batch[0].ID+",m,ba_tok_1,CREDIT,8712")
	check(t, "total owed unchanged by initiating a payout", f.balance(t).Total() == usd(9_680))
	if err := svc.Confirm(batch[0].ID); err != nil {
		t.Fatal(err)
	}
	check(t, "confirmed: cash left, in transit cleared", f.balance(t).InTransit.IsZero() && f.ledgerBalance(t, BANK) == usd(1_288) && batch[0].State == PAID)
	if err := svc.Confirm(batch[0].ID); err != nil {
		t.Fatal(err)
	}
	check(t, "duplicate confirmation is a no-op", f.ledgerBalance(t, BANK) == usd(1_288))
	check(t, "a second run the same day pays nothing", len(f.run(t)) == 0)
}

func TestMinimumsAndSchedules(t *testing.T) {
	g := newFixture(t, 0)
	g.capture(t, "g1", usd(1_000), 290, 30) // net 9.41 < 10.00 minimum
	g.settle(t, "gs", []string{"g1"}, usd(1_000))
	check(t, "below minimum: no payout", len(g.run(t)) == 0 && g.balance(t).Available == usd(941))
	g.capture(t, "g2", usd(100), 0, 0)
	g.settle(t, "gs2", []string{"g2"}, usd(100))
	check(t, "at or above minimum: paid", len(g.run(t)) == 1)
	weekly := PayoutSchedule{WEEKLY, time.Friday, usd(0)}
	check(t, "weekly schedule due only on its day", weekly.DueOn(Date(2026, time.March, 6)) && !weekly.DueOn(Date(2026, time.March, 5)))
	daily := PayoutSchedule{DAILY, time.Sunday, usd(0)}
	check(t, "daily schedule skips weekends", daily.DueOn(Date(2026, time.March, 2)) && !daily.DueOn(Date(2026, time.March, 7)) && !daily.DueOn(Date(2026, time.March, 8)))
}

func TestReserveRelease(t *testing.T) {
	r := newFixture(t, 1_000)
	r.capture(t, "r1", usd(10_000), 290, 30)
	r.settle(t, "rs", []string{"r1"}, usd(10_000))
	r.clock.AdvanceDays(89)
	check(t, "reserve not released on day 89", r.release(t).IsZero() && r.balance(t).Reserve == usd(968))
	r.clock.AdvanceDays(1)
	check(t, "reserve released on day 90", r.release(t) == usd(968) && r.balance(t).Reserve.IsZero() && r.balance(t).Available == usd(9_680))
	check(t, "release is not repeated", r.release(t).IsZero() && consistent(t, r))
}

func TestNegativeBalances(t *testing.T) {
	n := newFixture(t, 1_000)
	ns := n.svc
	n.capture(t, "n1", usd(10_000), 290, 30)
	n.settle(t, "ns1", []string{"n1"}, usd(10_000))
	n.run(t)
	if err := ns.DebitMerchant("m", usd(20_000), "chargeback"); err != nil {
		t.Fatal(err)
	}
	check(t, "chargeback drives available negative", n.balance(t).Available == usd(-20_000))
	since, negative := ns.NegativeSince("m")
	check(t, "negative since today", negative && since.Equal(n.clock.Today()))
	used, err := ns.CoverFromReserve("m")
	if err != nil {
		t.Fatal(err)
	}
	check(t, "reserve covers what it can", used == usd(968) && n.balance(t).Reserve.IsZero() && n.balance(t).Available == usd(-19_032) && consistent(t, n))
	check(t, "no credit payout while negative", len(n.run(t)) == 0)
	n.capture(t, "n2", usd(5_000), 0, 0)
	n.settle(t, "ns2", []string{"n2"}, usd(5_000))
	n.run(t)
	check(t, "settlement nets against the deficit (reserve share applied too)", n.balance(t).Available == usd(-14_032))
	n.clock.AdvanceDays(6)
	check(t, "day 6 negative: no collection yet", len(n.run(t)) == 0)
	n.clock.AdvanceDays(1)
	coll := n.run(t)
	check(t, "day 7 negative: one DEBIT for the deficit", len(coll) == 1 && coll[0].Direction == PAYOUT_DEBIT && coll[0].Amount == usd(14_032))
	_, stillNegative := ns.NegativeSince("m")
	check(t, "collection zeroes available and clears negativeSince", n.balance(t).Available.IsZero() && !stillNegative)
	collFile := n.files.Last()
	check(t, "collection trailer counts the debit", collFile[len(collFile)-1] == "T,1,0,14032")
	if err := ns.Returned(coll[0].ID, "R01"); err != nil {
		t.Fatal(err)
	}
	_, negativeAgain := ns.NegativeSince("m")
	check(t, "returned collection (R01) restores the deficit", n.balance(t).Available == usd(-14_032) && negativeAgain && coll[0].State == RETURNED)
	check(t, "cannot confirm a returned line", errors.Is(ns.Confirm(coll[0].ID), ErrIllegalState))
	check(t, "ledger still balances", ns.Ledger().Balances())
}

func TestReturnedCreditPausesPayouts(t *testing.T) {
	p := newFixture(t, 0)
	p.capture(t, "p1", usd(5_000), 0, 0)
	p.settle(t, "ps", []string{"p1"}, usd(5_000))
	po := p.run(t)[0]
	if err := p.svc.Returned(po.ID, "R03"); err != nil {
		t.Fatal(err)
	}
	check(t, "returned payout goes back to available", p.balance(t).Available == usd(5_000) && p.balance(t).InTransit.IsZero())
	check(t, "merchant paused after a return", p.svc.IsPaused("m"))
	p.clock.AdvanceDays(1)
	check(t, "paused merchant is not paid", len(p.run(t)) == 0)
	p.svc.Resume("m", "fixed")
	check(t, "resumed merchant is paid", len(p.run(t)) == 1)
}

func TestConservationAndValidation(t *testing.T) {
	k := newFixture(t, 1_000)
	ks := k.svc
	var gross int64
	var ids []string
	for i := 0; i < 20; i++ {
		id := fmt.Sprintf("k%d", i)
		k.capture(t, id, usd(1_000+37*int64(i)), 290, 30)
		gross += 1_000 + 37*int64(i)
		ids = append(ids, id)
	}
	k.settle(t, "ks", ids, usd(gross))
	kp := k.run(t)[0]
	if err := ks.Confirm(kp.ID); err != nil {
		t.Fatal(err)
	}
	owed := k.balance(t).Total()
	fees := k.ledgerBalance(t, FEES)
	check(t, "gross = paid out + still owed + fees", usd(gross) == mustPlus(mustPlus(kp.Amount, owed), fees))
	check(t, "bank = settled - paid", k.ledgerBalance(t, BANK) == mustMinus(usd(gross), kp.Amount))

	_, err := ks.Balance("nobody")
	check(t, "unknown merchant", errors.Is(err, ErrInvalidArgument))
	_, err = NewReservePolicy(10_001, 1)
	check(t, "reserve over 100% refused", errors.Is(err, ErrInvalidArgument))
	check(t, "non-positive debit refused", errors.Is(ks.DebitMerchant("m", usd(0), "x"), ErrInvalidArgument))
	_, err = ks.Capture("k0", "m", usd(1), 0, 0)
	check(t, "duplicate capture id refused", errors.Is(err, ErrIllegalState))
}
