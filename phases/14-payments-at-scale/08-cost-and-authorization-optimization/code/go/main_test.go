// Tests for phases/14-payments-at-scale/08-cost-and-authorization-optimization. Run: go test ./...
package main

import (
	"errors"
	"testing"
)

func check(t *testing.T, name string, condition bool) {
	t.Helper()
	if !condition {
		t.Errorf("%s", name)
	}
}

func txn(id string, minor int64, cardType CardType, token bool, base ResponseCode, dToken, dRetry, dUpdater int) Txn {
	return need(NewTxn(id, OfMinor(minor, USD), cardType, token, base, dToken, dRetry, dUpdater))
}

var exp = NewAuthExperiment(StandardPriceList(USD), USD)

func TestInterchangeAndNetworkFees(t *testing.T) {
	check(t, "L1 commercial on 1000.00 is 26.60", exp.Interchange(OfMinor(100_000, USD), Commercial, false).MinorUnits == 2_660)
	check(t, "L3 commercial on 1000.00 is 18.60", exp.Interchange(OfMinor(100_000, USD), Commercial, true).MinorUnits == 1_860)
	check(t, "L3 data does not change consumer credit", exp.Interchange(OfMinor(5_000, USD), ConsumerCredit, true) ==
		exp.Interchange(OfMinor(5_000, USD), ConsumerCredit, false))
	check(t, "regulated debit on 50.00 is 0.05% + 0.22 = 0.245 -> 0.24 (half-even)",
		exp.Interchange(OfMinor(5_000, USD), RegulatedDebit, false).MinorUnits == 24)
	check(t, "least-cost debit network fee on 100.00 is 0.06 vs 0.13",
		exp.NetworkFee(OfMinor(10_000, USD), RegulatedDebit, true).MinorUnits == 6 &&
			exp.NetworkFee(OfMinor(10_000, USD), RegulatedDebit, false).MinorUnits == 13)
	check(t, "response codes carry their ISO 8583 field 39 value", DoNotHonor.Code() == "05" && LostOrStolen.Code() == "43" && Approved.Code() == "00")
}

func TestPerTransactionPaths(t *testing.T) {
	expired := txn("e", 4_999, ConsumerCredit, true, ExpiredCard, 9_000, 9_000, 1_000)
	check(t, "expired card is not retried unchanged", exp.Simulate(expired, Of(SoftDeclineRetry)).Attempts == 1)
	upd := exp.Simulate(expired, Of(AccountUpdater))
	check(t, "updater match approves and charges the hit fee", upd.FinalOutcome.Approved() && upd.UpdaterHit && upd.Fees.MinorUnits == 20)
	expiredMiss := txn("m", 4_999, ConsumerCredit, false, ExpiredCard, 0, 0, 8_000)
	check(t, "updater miss (draw 8000 >= 8000) stays declined, no hit fee", !exp.Simulate(expiredMiss, Of(AccountUpdater)).UpdaterHit)
	tok := exp.Simulate(expired, Of(NetworkToken, AccountUpdater))
	check(t, "token fixes the expired card first, so no updater fee", tok.FinalOutcome.Approved() && !tok.UpdaterHit && tok.Fees.MinorUnits == 6)
	notEligible := txn("n", 4_999, ConsumerCredit, false, ExpiredCard, 0, 0, 9_999)
	check(t, "token lever does nothing for an ineligible card", !exp.Simulate(notEligible, Of(NetworkToken)).TokenUsed)

	dnh := txn("d", 2_000, ConsumerCredit, true, DoNotHonor, 2_499, 9_999, 0)
	check(t, "token recovers 05 when draw < 2500", exp.Simulate(dnh, Of(NetworkToken)).FinalOutcome.Approved())
	dnh2 := txn("d2", 2_000, ConsumerCredit, true, DoNotHonor, 2_500, 999, 0)
	r05 := exp.Simulate(dnh2, Of(SoftDeclineRetry))
	check(t, "05 gets exactly one retry, approved when draw < 1000", r05.Attempts == 2 && r05.Retried && r05.FinalOutcome.Approved())

	for _, hard := range []ResponseCode{LostOrStolen, NotPermitted} {
		t.Run("never retry "+hard.Code(), func(t *testing.T) {
			h := exp.Simulate(txn("h", 1_000, ConsumerCredit, true, hard, 0, 0, 0), AllLevers())
			check(t, "never retry "+hard.Code(), h.Attempts == 1 && !h.FinalOutcome.Approved())
		})
	}

	timeout := txn("t", 3_000, ConsumerCredit, false, ProcessorTimeout, 0, 8_999, 0)
	fo := exp.Simulate(timeout, Of(Routing, SoftDeclineRetry))
	check(t, "timeout fails over once and is not also retried", fo.FailedOver && !fo.Retried && fo.Attempts == 2 && fo.FinalOutcome.Approved())
	timeout2 := txn("t2", 3_000, ConsumerCredit, false, ProcessorTimeout, 0, 9_000, 0)
	check(t, "failover with draw 9000 ends as issuer unavailable", exp.Simulate(timeout2, Of(Routing)).FinalOutcome == IssuerUnavailable)
	check(t, "same txn, same levers, same trace", exp.Simulate(timeout, AllLevers()) == exp.Simulate(timeout, AllLevers()))
	_, err := NewTxn("x", OfMinor(1, USD), ConsumerCredit, false, Approved, 10_000, 0, 0)
	check(t, "draws must be basis points", errors.Is(err, ErrBadDraw))
	_, err = NewTxn("x", OfMinor(1, USD), ConsumerCredit, false, Approved, 0, -1, 0)
	check(t, "negative draws are rejected too", errors.Is(err, ErrBadDraw))
	check(t, "lever set prints in declaration order", Of(Routing, NetworkToken).String() == "[NETWORK_TOKEN, ROUTING]" && Of().String() == "[]")
}

func TestExperimentWithCommonRandomNumbers(t *testing.T) {
	pop := Population(20_000, 7, USD)
	control := exp.Run(pop, Of())
	control2 := exp.Run(Population(20_000, 7, USD), Of())
	check(t, "control is reproducible", control == control2)
	check(t, "control makes one attempt per transaction", control.Attempts == len(pop))
	baseApproved := 0
	for _, tx := range pop {
		if tx.BaseOutcome.Approved() {
			baseApproved++
		}
	}
	check(t, "control approvals equal base approvals", control.Approvals == baseApproved)

	singles := make([]int, len(Levers))
	for _, l := range Levers {
		arm := exp.Run(pop, Of(l))
		singles[l] = arm.Approvals - control.Approvals
		check(t, l.String()+" never lowers approvals", arm.Approvals >= control.Approvals)
	}
	all := exp.Run(pop, AllLevers())
	allUplift := all.Approvals - control.Approvals
	sum, best := 0, 0
	for _, s := range singles {
		sum += s
		best = max(best, s)
	}
	check(t, "all levers beat the best single lever", allUplift > best)
	check(t, "uplifts are not additive (overlap)", allUplift < sum)
	check(t, "LEVEL3_DATA changes cost, not approvals", singles[Level3Data] == 0 &&
		exp.Run(pop, Of(Level3Data)).Interchange.Compare(control.Interchange) < 0)
	expiredEligible, dnhRecovered, hardResent := 0, 0, 0
	for _, tx := range pop {
		if tx.BaseOutcome == ExpiredCard && tx.TokenEligible {
			expiredEligible++
		}
		if tx.BaseOutcome == DoNotHonor && tx.TokenEligible && tx.DrawToken < 2_500 {
			dnhRecovered++
		}
		if tx.BaseOutcome.DeclineClass() == NeverRetry && exp.Simulate(tx, AllLevers()).Attempts > 1 {
			hardResent++
		}
	}
	check(t, "token uplift is exactly eligible expired + recovered 05", singles[NetworkToken] == expiredEligible+dnhRecovered)
	check(t, "no hard decline is ever re-sent", hardResent == 0)
	u := UpliftOf("ALL", control, all, 3_000)
	check(t, "net value = 30% of extra volume - extra cost",
		u.NetValue == need(need(u.ExtraVolume.Times(3_000, 10_000, HalfEven)).Minus(u.ExtraCost)))
	check(t, "rate delta in bps matches approvals delta",
		u.RateDeltaBps == int64(all.Approvals)*10_000/int64(len(pop))-int64(control.Approvals)*10_000/int64(len(pop)))
	check(t, "arm result records its levers", all.Levers == AllLevers() && control.Levers.IsEmpty())
}

func TestSeededDemoFigures(t *testing.T) {
	// The numbers the Java demo prints for seed 2026: the Go port must draw the same population.
	pop := Population(100_000, 2026, USD)
	control := exp.Run(pop, Of())
	all := exp.Run(pop, AllLevers())
	check(t, "control approvals 83737", control.Approvals == 83_737)
	check(t, "control volume 29554906.35", control.ApprovedVolume == OfMinor(2_955_490_635, USD))
	check(t, "all-levers uplift 7823", all.Approvals-control.Approvals == 7_823)
}

func TestJavaRandomMatchesTheJdk(t *testing.T) {
	// Values taken from java.util.Random(2026): nextInt(), nextInt(66), nextInt(64), nextLong().
	r := NewJavaRandom(2026)
	check(t, "nextInt()", r.NextInt() == -1637959697)
	check(t, "nextInt(66)", r.NextIntBound(66) == 52)
	check(t, "nextInt(64)", r.NextIntBound(64) == 54)
	check(t, "nextLong()", r.NextLong() == 3301047695993903762)
}
