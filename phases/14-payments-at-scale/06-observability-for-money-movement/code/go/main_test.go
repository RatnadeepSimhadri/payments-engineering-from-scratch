// Tests for phases/14-payments-at-scale/06-observability-for-money-movement. Run: go test ./...
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

func usd(minor int64) Money { return OfMinor(minor, USD) }

func at(s string) time.Time {
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return ts
}

func has(alerts []Alert, name string) bool {
	for _, a := range alerts {
		if a.Name == name {
			return true
		}
	}
	return false
}

func get(alerts []Alert, name string) (Alert, bool) {
	for _, a := range alerts {
		if a.Name == name {
			return a, true
		}
	}
	return Alert{}, false
}

func TestBurnRateArithmetic(t *testing.T) {
	slo := Slo{"x", 1_000}
	cases := []struct {
		name       string
		bad, total int64
		want       int64
	}{
		{"burn 6/200 at 0.1% budget is 30.00", 6, 200, 3000},
		{"burn 40/2400 is 16.66 (rounded down)", 40, 2_400, 1666},
		{"burn exactly at budget is 1.00", 1, 1_000, 100},
		{"burn with no traffic is 0", 0, 0, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { check(t, c.name, slo.BurnRateX100(c.bad, c.total) == c.want) })
	}
	check(t, "bps rounds down", Ratio.Bps(1, 3) == 3333 && Ratio.Bps(0, 0) == -1)
}

func TestStuckDetection(t *testing.T) {
	clock := NewManualClock(at("2026-03-31T12:00:00Z"))
	m := NewMoneyMovementMonitor(clock, USD)
	check(t, "transition ok", m.Transition("p1", "proc_a", usd(40_000), CapturePending) == nil)
	check(t, "transition ok", m.Transition("p2", "proc_a", usd(5_000), Authorized) == nil)
	check(t, "advance ok", clock.Advance(15*time.Minute) == nil)
	check(t, "exactly at the limit is not stuck", len(m.Stuck()) == 0)
	check(t, "advance ok", clock.Advance(time.Minute) == nil)
	stuck := m.Stuck()
	check(t, "capture pending past 15m is stuck", len(stuck) == 1 && stuck[0].PaymentId == "p1")
	noneP2 := true
	for _, s := range stuck {
		if s.PaymentId == "p2" {
			noneP2 = false
		}
	}
	check(t, "authorized for 16m is not stuck", noneP2)
	check(t, "stuck age is measured from entering the state", stuck[0].Age == 16*time.Minute)
	stuckAlert, ok := get(m.EvaluateAlerts(), "stuck-payments")
	check(t, "400.00 stuck is a ticket (page threshold 1000.00)", ok && stuckAlert.Severity == Ticket)
	check(t, "transition ok", m.Transition("p3", "proc_b", usd(70_000), CapturePending) == nil)
	check(t, "advance ok", clock.Advance(16*time.Minute) == nil)
	paged, ok := get(m.EvaluateAlerts(), "stuck-payments")
	check(t, "1100.00 stuck pages", ok && paged.Severity == Page)
	check(t, "oldest stuck listed first", m.Stuck()[0].PaymentId == "p1")
	check(t, "transition ok", m.Transition("p1", "proc_a", usd(40_000), Captured) == nil)
	age, tracked := m.AgeOf("p1")
	check(t, "a transition resets the age", tracked && age == 0 && len(m.Stuck()) == 1)
	check(t, "transition ok", m.Transition("p1", "proc_a", usd(40_000), Settled) == nil)
	_, tracked = m.AgeOf("p1")
	check(t, "terminal state stops tracking", !tracked)

	t.Run("in-flight exposure per state", func(t *testing.T) {
		exposure := m.InFlightExposure()
		check(t, "exposure by state", exposure[Authorized] == usd(5_000) && exposure[CapturePending] == usd(70_000))
		check(t, "total in flight", m.TotalInFlight() == usd(75_000))
		_, hasSettled := exposure[Settled]
		_, hasFailed := exposure[Failed]
		check(t, "no terminal states in exposure", !hasSettled && !hasFailed)
		err := m.Transition("e", "proc_a", OfMinor(1, EUR), Created)
		check(t, "foreign currency rejected", errors.Is(err, ErrCurrencyMismatch))
		hist := m.StateAgeHistogram()[CapturePending]
		check(t, "histogram puts a 16m-old payment in the 15-60m band", hist[3] == 1)
	})

	t.Run("clock does not go backwards", func(t *testing.T) {
		check(t, "clock does not go backwards", errors.Is(clock.Advance(-time.Minute), ErrTimeBackwards))
	})
}

func TestAuthRateByProcessor(t *testing.T) {
	c2 := NewManualClock(at("2026-03-31T12:00:00Z"))
	a := NewMoneyMovementMonitor(c2, USD)
	for minute := 0; minute < 60; minute++ {
		for i := 0; i < 10; i++ {
			outA := Approved
			if i == 0 {
				outA = IssuerDecline
			}
			a.AuthResult("proc_a", outA)
			outB := Approved
			switch {
			case minute >= 50 && i < 5:
				outB = ProcessorError
			case i == 0:
				outB = IssuerDecline
			}
			a.AuthResult("proc_b", outB)
		}
		if minute < 59 {
			need(0, c2.Advance(time.Minute))
		}
	}
	check(t, "proc_a approves 90.00%", a.AuthRateBps("proc_a", time.Hour) == 9000)
	check(t, "proc_b 15m rate: (45 + 50) / 150 = 63.33%", a.AuthRateBps("proc_b", 15*time.Minute) == 6333)
	check(t, "50 attempts in 5m is below the 100 minimum: no rate", a.Attempts("proc_a", 5*time.Minute) == 50 &&
		a.AuthRateBps("proc_a", 5*time.Minute) == -1)
	check(t, "issuer declines do not burn the SLO", a.BurnRateX100(5*time.Minute) == 25_000)
	alerts := a.EvaluateAlerts()
	check(t, "processor-specific drop pages", has(alerts, "auth-rate-drop:proc_b") && !has(alerts, "auth-rate-drop:proc_a"))
	check(t, "1h page rule fires: 50 errors / 1200 = burn 41.66 and 5m burn 250", has(alerts, "slo-burn-60m"))
	check(t, "6h rule not armed with 1h of history", !has(alerts, "slo-burn-360m"))
	check(t, "unknown processor has no attempts", a.Attempts("proc_z", time.Hour) == 0 && a.AuthRateBps("proc_z", time.Hour) == -1)
	check(t, "processors are listed in sorted order", strings.Join(a.Processors(), ",") == "proc_a,proc_b")
}

func TestMultiwindowSpikeThatEnded(t *testing.T) {
	c3 := NewManualClock(at("2026-03-31T12:00:00Z"))
	s := NewMoneyMovementMonitor(c3, USD)
	for minute := 0; minute < 60; minute++ {
		for i := 0; i < 40; i++ {
			out := Approved
			if minute >= 40 && minute < 50 && i < 4 {
				out = ProcessorError
			}
			s.AuthResult("proc_a", out)
		}
		if minute < 59 {
			need(0, c3.Advance(time.Minute))
		}
	}
	check(t, "long window still hot after the spike", s.BurnRateX100(time.Hour) >= 1440)
	check(t, "short window has recovered", s.BurnRateX100(5*time.Minute) == 0)
	check(t, "so the page rule stays quiet", !has(s.EvaluateAlerts(), "slo-burn-60m"))
	check(t, "1h rule is armed, 6h rule is not", s.BurnRuleArmed(DefaultBurnRules[0]) && !s.BurnRuleArmed(DefaultBurnRules[1]))
}

func TestReconciliationBreakRate(t *testing.T) {
	clock := NewManualClock(at("2026-03-31T12:00:00Z"))
	r := NewMoneyMovementMonitor(clock, USD)
	r.ReconRun(9_990, 10)
	check(t, "10 breaks in 10,000 is 0.10%", r.ReconBreakRateBps(4) == 10 && !has(r.EvaluateAlerts(), "recon-break-rate"))
	r.ReconRun(9_900, 100)
	check(t, "110 breaks in 20,000 is 0.55% and tickets", r.ReconBreakRateBps(4) == 55 && has(r.EvaluateAlerts(), "recon-break-rate"))
	check(t, "no runs is n/a", NewMoneyMovementMonitor(clock, USD).ReconBreakRateBps(4) == -1)
}

func TestSeededSimulation(t *testing.T) {
	// alerts fire after the faults, never before, and in a sensible order
	sim := NewSimulation(42)
	sim.Run(SimulationMinutes, map[int]bool{430: true})
	stuckAt, okStuck := sim.FirstFired["stuck-payments"]
	dropAt, okDrop := sim.FirstFired["auth-rate-drop:proc_b"]
	fastAt, okFast := sim.FirstFired["slo-burn-60m"]
	slowAt, okSlow := sim.FirstFired["slo-burn-360m"]
	check(t, "stuck captures detected 16 minutes after the webhook was lost", okStuck && stuckAt == LostWebhookMinute+16)
	check(t, "auth drop pages within 10 minutes of the outage", okDrop && dropAt > OutageMinute && dropAt <= OutageMinute+10)
	check(t, "fast burn pages within 10 minutes", okFast && fastAt > OutageMinute && fastAt <= OutageMinute+10)
	check(t, "slow burn fires after fast burn", okSlow && slowAt > fastAt)
	quiet := true
	for _, x := range sim.FirstFired {
		if x < LostWebhookMinute {
			quiet = false
		}
	}
	check(t, "no alert fires in the quiet morning", quiet)
	check(t, "dashboard renders the outage", strings.Contains(sim.Snapshots[430], "ALERT PAGE slo-burn-60m"))
	again := NewSimulation(42)
	again.Run(SimulationMinutes, map[int]bool{430: true})
	check(t, "same seed, same dashboard", again.Snapshots[430] == sim.Snapshots[430])
	check(t, "first-fired order is insertion order", len(sim.FiredOrder) == len(sim.FirstFired) && sim.FiredOrder[0] == "stuck-payments")
}

func TestJavaRandomMatchesTheJdk(t *testing.T) {
	// Values taken from java.util.Random(2026): nextInt(), nextInt(66), nextInt(64), nextLong().
	r := NewJavaRandom(2026)
	check(t, "nextInt()", r.NextInt() == -1637959697)
	check(t, "nextInt(66)", r.NextIntBound(66) == 52)
	check(t, "nextInt(64)", r.NextIntBound(64) == 54)
	check(t, "nextLong()", r.NextLong() == 3301047695993903762)
}
