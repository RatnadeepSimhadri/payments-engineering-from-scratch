// Tests for phases/13-risk-fraud-and-compliance/06-aml-transaction-monitoring. Run: go test ./...
package main

import (
	"errors"
	"strconv"
	"strings"
	"testing"
)

func check(t *testing.T, name string, condition bool) {
	t.Helper()
	if !condition {
		t.Errorf("FAIL: %s", name)
	}
}

var seq = 0

func tx(account string, d Direction, ch Channel, minor, ts int64) Transaction {
	seq++
	t, err := NewTransaction("t"+itoa(seq), account, "cp-"+itoa(seq), d, ch, OfMinor(minor, USD), ts)
	if err != nil {
		panic(err)
	}
	return t
}

func txCp(account, cp string, d Direction, ch Channel, minor, ts int64) Transaction {
	seq++
	t, err := NewTransaction("t"+itoa(seq), account, cp, d, ch, OfMinor(minor, USD), ts)
	if err != nil {
		panic(err)
	}
	return t
}

func itoa(i int) string { return strconv.Itoa(i) }

func run(t *testing.T, s Scenario, txs []Transaction) []Alert {
	t.Helper()
	alerts, err := mustEngine([]Scenario{s}).Evaluate(txs)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	return alerts
}

var (
	str = StructuringScenario("STR", 1_000_000, 8_000, 3*Day, 3)
	rpt = RapidPassThroughScenario("RPT", 1_000_000, 9_000, 24*Hour)
	vel = VelocityScenario("VEL", 6*Hour, 6, 4, 150)
)

func TestMoneyHelpersAndValidation(t *testing.T) {
	bp, err := OfMinor(1_000_000, USD).BasisPoints(8_000)
	check(t, "80% of 10,000.00 is 8,000.00", err == nil && bp.MinorUnits == 800_000)
	bp, err = OfMinor(4_850_000, USD).BasisPoints(9_000)
	check(t, "90% of 48,500.00 is 43,650.00", err == nil && bp.MinorUnits == 4_365_000)
	_, err = NewTransaction("z", "a", "cp", In, Cash, OfMinor(0, USD), T0)
	check(t, "zero-amount transaction rejected", errors.Is(err, ErrNonPositiveAmount))
	_, err = NewScenario("X", Structuring, High, map[string]int64{}, "").Param("thresholdMinor")
	check(t, "missing scenario parameter is an error", errors.Is(err, ErrMissingParam))
	_, err = NewMonitoringEngine([]Scenario{str, str})
	check(t, "duplicate scenario ids rejected", errors.Is(err, ErrDuplicateScenario))
	_, err = mustEngine([]Scenario{NewScenario("X", Structuring, High, map[string]int64{}, "")}).Evaluate([]Transaction{tx("a", In, Cash, 1, T0)})
	check(t, "evaluate surfaces the missing parameter", errors.Is(err, ErrMissingParam))
}

func tenDeposits(account string) []Transaction {
	ten := []Transaction{}
	for i := 0; i < 10; i++ {
		ten = append(ten, tx(account, In, Cash, 990_000, T0+int64(i)*7*Hour))
	}
	return ten
}

func TestStructuring(t *testing.T) {
	ten := tenDeposits("s")
	a := run(t, str, ten)
	check(t, "ten deposits of 9,900 produce one alert", len(a) == 1 && a[0].Kind == Structuring)
	check(t, "overlapping windows merged into one alert with all ten as evidence", len(a[0].Evidence) == 10)
	check(t, "alert total is 99,000.00", a[0].Total().MinorUnits == 9_900_000)
	check(t, "alert is HIGH with score 100", a[0].Severity == High && a[0].Score == 100)
	check(t, "narrative names the count and window", strings.Contains(a[0].Narrative, "cash deposits") && strings.Contains(a[0].Narrative, "3 days"))

	atThreshold := []Transaction{tx("b", In, Cash, 1_000_000, T0), tx("b", In, Cash, 1_000_000, T0+Hour), tx("b", In, Cash, 1_000_000, T0+2*Hour)}
	check(t, "deposits AT the threshold are not structuring (they are reportable)", len(run(t, str, atThreshold)) == 0)
	belowFloor := []Transaction{tx("c", In, Cash, 799_999, T0), tx("c", In, Cash, 799_999, T0+1), tx("c", In, Cash, 799_999, T0+2)}
	check(t, "deposits below the 80% floor do not qualify", len(run(t, str, belowFloor)) == 0)
	twoOnly := []Transaction{tx("d", In, Cash, 990_000, T0), tx("d", In, Cash, 990_000, T0+1)}
	check(t, "two deposits summing over threshold but under minCount do not alert", len(run(t, str, twoOnly)) == 0)
	spread := []Transaction{tx("e", In, Cash, 990_000, T0), tx("e", In, Cash, 990_000, T0+4*Day), tx("e", In, Cash, 990_000, T0+8*Day)}
	check(t, "three deposits four days apart never share a 3-day window", len(run(t, str, spread)) == 0)
	edge := []Transaction{tx("f", In, Cash, 990_000, T0), tx("f", In, Cash, 990_000, T0+Day), tx("f", In, Cash, 990_000, T0+3*Day)}
	check(t, "window is inclusive at exactly 3 days", len(run(t, str, edge)) == 1)
	wires := []Transaction{tx("g", In, Wire, 990_000, T0), tx("g", In, Wire, 990_000, T0+1), tx("g", In, Wire, 990_000, T0+2)}
	check(t, "wires are not cash: structuring scenario ignores them", len(run(t, str, wires)) == 0)
	unsorted := []Transaction{tx("h", In, Cash, 990_000, T0+2*Hour), tx("h", In, Cash, 990_000, T0), tx("h", In, Cash, 990_000, T0+Hour)}
	u := run(t, str, unsorted)
	check(t, "input order does not matter", len(u) == 1 && u[0].Evidence[0].Timestamp == T0)
}

func muleTxs() []Transaction {
	return []Transaction{
		txCp("m", "shell", In, Wire, 5_000_000, T0),
		txCp("m", "b1", Out, Wire, 2_000_000, T0+2*Hour),
		txCp("m", "b2", Out, Wire, 2_000_000, T0+5*Hour),
		txCp("m", "b3", Out, Wire, 600_000, T0+8*Hour),
	}
}

func TestRapidPassThrough(t *testing.T) {
	ra := run(t, rpt, muleTxs())
	check(t, "50,000 in, 46,000 out in 8h alerts", len(ra) == 1 && ra[0].Kind == RapidPassThrough)
	check(t, "evidence is the inbound plus the outbound legs that reached 90%", len(ra[0].Evidence) == 4)
	check(t, "faster pass-through scores higher", ra[0].Score > 60)
	slow := []Transaction{txCp("n", "shell", In, Wire, 5_000_000, T0), txCp("n", "b1", Out, Wire, 4_600_000, T0+25*Hour)}
	check(t, "outbound after the window does not count", len(run(t, rpt, slow)) == 0)
	partial := []Transaction{txCp("o", "shell", In, Wire, 5_000_000, T0), txCp("o", "b1", Out, Wire, 4_400_000, T0+Hour)}
	check(t, "88% out is under the 90% rule", len(run(t, rpt, partial)) == 0)
	cardSpend := []Transaction{txCp("p", "employer", In, ACH, 5_000_000, T0), txCp("p", "merchant", Out, Card, 4_800_000, T0+Hour)}
	check(t, "card spend is not onward transfer", len(run(t, rpt, cardSpend)) == 0)
	small := []Transaction{txCp("q", "friend", In, ACH, 50_000, T0), txCp("q", "other", Out, ACH, 50_000, T0+60)}
	check(t, "inbound under minIn is ignored", len(run(t, rpt, small)) == 0)
}

func burstTxs(account string) []Transaction {
	burst := []Transaction{}
	for i := 0; i < 20; i++ {
		burst = append(burst, txCp(account, "peer-"+itoa(i), Out, ACH, 1_000, T0+int64(i)*300))
	}
	return burst
}

func TestVelocity(t *testing.T) {
	va := run(t, vel, burstTxs("v"))
	check(t, "20 transfers to 20 counterparties in 100 minutes alerts once", len(va) == 1 && va[0].Kind == Velocity)
	check(t, "velocity evidence covers the whole burst", len(va[0].Evidence) == 20)
	sameCp := []Transaction{}
	for i := 0; i < 9; i++ {
		sameCp = append(sameCp, txCp("w", "landlord", Out, ACH, 1_000, T0+int64(i)*300))
	}
	check(t, "nine transfers to one counterparty score 50*9/6 + 50*1/4 = 87, under 150", len(run(t, vel, sameCp)) == 0)
	slowBurst := []Transaction{}
	for i := 0; i < 20; i++ {
		slowBurst = append(slowBurst, txCp("x", "peer-"+itoa(i), Out, ACH, 1_000, T0+int64(i)*2*Hour))
	}
	check(t, "the same 20 spread over 40 hours stays under threshold", len(run(t, vel, slowBurst)) == 0)
}

func TestEngineOrderingAndIndependence(t *testing.T) {
	mixed := append(append(tenDeposits("s"), muleTxs()...), burstTxs("v")...)
	engine := mustEngine([]Scenario{str, rpt, vel})
	all, err := engine.Evaluate(mixed)
	check(t, "three accounts, three scenarios, three alerts", err == nil && len(all) == 3)
	check(t, "HIGH alerts sort before MEDIUM", all[0].Severity == High && all[2].Severity == Medium)
	accounts := map[string]bool{}
	for _, a := range all {
		accounts[a.AccountID] = true
	}
	check(t, "accounts do not contaminate each other", len(accounts) == 3 && accounts["s"] && accounts["m"] && accounts["v"])
	mergedNarrative := false
	for _, a := range all {
		if a.Kind == Structuring && strings.Contains(a.Narrative, "10 transactions") {
			mergedNarrative = true
		}
	}
	check(t, "merged narrative reports the full evidence count", mergedNarrative)
	empty, err := engine.Evaluate(nil)
	check(t, "empty input, no alerts", err == nil && len(empty) == 0)
}

func samePopulation(a, b []Transaction) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestSyntheticPopulation(t *testing.T) {
	g1, g2 := NewSyntheticGenerator(7), NewSyntheticGenerator(7)
	p1, p2 := g1.Population(60, T0), g2.Population(60, T0)
	check(t, "same seed, same population", len(p1) == len(p2) && p1[len(p1)-1] == p2[len(p2)-1])
	check(t, "population is in the expected size range", len(p1) > 1500 && len(p1) < 2200)
	check(t, "population matches the Java generator exactly (1792 rows)", len(p1) == 1792)
	pa, err := mustEngine(StandardScenarios()).Evaluate(p1)
	if err != nil {
		t.Fatal(err)
	}
	foundPlanted := map[string]bool{}
	fps := 0
	laundromat, retail := false, false
	for _, x := range pa {
		if x.Kind.String() == g1.Planted[x.AccountID] {
			foundPlanted[x.AccountID] = true
		} else {
			fps++
		}
		if x.AccountID == "acct-laundromat" {
			laundromat = true
		}
		if strings.HasPrefix(x.AccountID, "acct-0") {
			retail = true
		}
	}
	allFound := len(foundPlanted) == len(g1.Planted)
	for acct := range g1.Planted {
		allFound = allFound && foundPlanted[acct]
	}
	check(t, "all three planted patterns are found", allFound)
	check(t, "the cash-intensive business is a false positive at an 80% floor", fps >= 1 && laundromat)
	check(t, "no ordinary retail account alerts", !retail)
	tighter := StructuringScenario("STR-TIGHT", 1_000_000, 9_300, 3*Day, 3)
	tight, _ := mustEngine([]Scenario{tighter}).Evaluate(p1)
	hasStructurer, hasLaundromat := false, false
	for _, x := range tight {
		hasStructurer = hasStructurer || x.AccountID == "acct-structurer"
		hasLaundromat = hasLaundromat || x.AccountID == "acct-laundromat"
	}
	check(t, "raising the floor to 93% keeps the structurer and drops the laundromat", hasStructurer && !hasLaundromat)
	other := NewSyntheticGenerator(99).Population(60, T0)
	check(t, "a different seed changes amounts but not the planted accounts", !samePopulation(other, p1))
}
