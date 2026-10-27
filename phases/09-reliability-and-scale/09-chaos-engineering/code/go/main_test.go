package main

import (
	"errors"
	"strings"
	"testing"
)

func check(t *testing.T, name string, condition bool) {
	t.Helper()
	if !condition {
		t.Errorf("%s", name)
	}
}

// World is a fresh, fully seeded world. Same seeds => same results, which is what makes chaos replayable.
type World struct {
	Clock     *SimClock
	Injector  *FaultInjector
	Processor *Processor
	Network   *SimNetwork
	Gateway   *Gateway
	Traffic   *TrafficGenerator
	Runner    *ExperimentRunner
}

func createWorld() *World {
	clock := NewSimClock(1_700_000_000_000)
	injector := NewFaultInjector(7)
	processor := NewProcessor(clock, 11)
	network := NewSimNetwork(clock, injector, 13)
	gateway := NewGateway(clock, network, processor)
	traffic := NewTrafficGenerator(17, "pay")
	return &World{clock, injector, processor, network, gateway, traffic, NewExperimentRunner(clock, injector, gateway, processor, traffic)}
}

func exp(target string, fault Fault, radius BlastRadius) Experiment {
	return Experiment{"t", "h", target, fault, radius, Steady, Abort, 300}
}

func latency(t *testing.T, ms int64) Latency {
	t.Helper()
	l, err := NewLatency(ms)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func percent(t *testing.T, p int) PercentOfRequests {
	t.Helper()
	r, err := NewPercentOfRequests(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

var amount = OfMinor(1999, USD)

func TestValueTypesAndInjector(t *testing.T) {
	check(t, "money prints minor units with currency", amount.String() == "19.99 USD")
	_, err := NewLatency(-1)
	check(t, "negative latency fault rejected", err != nil)
	_, err = NewPercentOfRequests(101)
	check(t, "blast radius percent bounded", err != nil)
	t.Run("clock never moves backwards", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Error("Advance(-5) should panic")
			}
		}()
		NewSimClock(0).Advance(-5)
	})
	inj := NewFaultInjector(1)
	check(t, "injector starts disarmed", !inj.IsArmed())
	inj.Inject("processor", Error{"HTTP_500"}, Everyone{})
	req := AuthRequest{"p1", "m-1", amount, "idem-p1", 0}
	check(t, "injector matches target", len(inj.FaultsFor("processor", req)) == 1)
	check(t, "injector ignores other targets", len(inj.FaultsFor("gateway->processor", req)) == 0)
	inj.Clear()
	check(t, "clear disarms and later requests see nothing", !inj.IsArmed() && len(inj.FaultsFor("processor", req)) == 0)
	check(t, "merchant blast radius is exact", NewMerchants("m-1").Includes(req, nil) && !NewMerchants("m-2").Includes(req, nil))
	check(t, "faults and radii render like the Java records", Latency{300}.String() == "Latency[addedMillis=300]" &&
		Partition{}.String() == "Partition[]" && NewMerchants("m-3").String() == "Merchants[merchantIds=[m-3]]" && Everyone{}.String() == "Everyone[]")
}

func TestHappyPath(t *testing.T) {
	w := createWorld()
	ok := w.Gateway.Authorize("p-1", "m-0", amount)
	check(t, "no fault: one attempt", ok.Attempts == 1)
	check(t, "no fault: latency within baseline band", ok.LatencyMillis >= 80 && ok.LatencyMillis <= 120)
	check(t, "no fault: good outcome", ok.Outcome.IsGood())
}

func TestLatencyBeyondTimeout(t *testing.T) {
	w := createWorld()
	w.Injector.Inject(LinkGatewayToProcessor, latency(t, 2_500), Everyone{})
	slow := w.Gateway.Authorize("p-2", "m-0", amount)
	check(t, "latency beyond timeout: both attempts time out", slow.Outcome == TIMEOUT && slow.Attempts == 2)
	check(t, "latency beyond timeout: latency is 2 x timeout", slow.LatencyMillis == 2*TimeoutMillis)
	check(t, "latency beyond timeout: nothing authorized", w.Processor.AuthorizationsFor("p-2") == 0)
}

func TestProcessorIdempotency(t *testing.T) {
	w := createWorld()
	p := w.Processor
	first := AuthRequest{"p-3", "m-0", amount, "idem-p-3", w.Clock.NowMillis()}
	o1, err1 := p.Handle(first)
	o2, err2 := p.Handle(first)
	check(t, "processor replays the same answer for the same idempotency key", err1 == nil && err2 == nil && o1 == o2)
	check(t, "processor records at most one authorization per payment", p.AuthorizationsFor("p-3") <= 1 && p.NoDoubleAuthorizations())
	stale := AuthRequest{"p-9", "m-0", amount, "idem-p-9", w.Clock.NowMillis() - TimestampToleranceMillis - 1}
	_, err := p.Handle(stale)
	check(t, "processor reports a stale request as ErrStaleRequest", errors.Is(err, ErrStaleRequest))
}

func TestPartition(t *testing.T) {
	w := createWorld()
	w.Injector.Inject(LinkGatewayToProcessor, Partition{}, NewMerchants("m-3"))
	cut := w.Gateway.Authorize("p-4", "m-3", amount)
	fine := w.Gateway.Authorize("p-5", "m-4", amount)
	check(t, "partition: canary merchant times out", cut.Outcome == TIMEOUT)
	check(t, "partition: other merchant unaffected", fine.Outcome.IsGood() && fine.Attempts == 1)
}

func TestClockSkew(t *testing.T) {
	w := createWorld()
	w.Injector.Inject(TargetProcessor, ClockSkew{6 * 60 * 1000}, Everyone{})
	check(t, "skew of 6 minutes: stale request", w.Gateway.Authorize("p-6", "m-0", amount).Outcome == STALE_REQUEST)
	w.Injector.Clear()
	w.Injector.Inject(TargetProcessor, ClockSkew{4 * 60 * 1000}, Everyone{})
	check(t, "skew of 4 minutes: accepted", w.Gateway.Authorize("p-7", "m-0", amount).Outcome.IsGood())
	check(t, "skew is not retried (not a transient fault)", w.Gateway.Authorize("p-8", "m-0", amount).Attempts == 1)
	check(t, "skew is reset after every call (the Java finally)", w.Processor.LocalNowMillis() == w.Clock.NowMillis())
}

func TestMetrics(t *testing.T) {
	m := NewMetrics()
	for i := int64(1); i <= 100; i++ {
		m.Record(AuthResult{AUTHORIZED, i, 1})
	}
	check(t, "p99 of 1..100 is 99", m.P99Millis() == 99)
	m.Record(AuthResult{TIMEOUT, 4_000, 2})
	check(t, "good rate counts timeouts as bad", m.GoodRatePercent() < 100.0 && m.GoodRatePercent() > 99.0)
	check(t, "empty metrics: 100% good", NewMetrics().GoodRatePercent() == 100.0)
}

func TestExperiments(t *testing.T) {
	w := createWorld()
	held := w.Runner.Run(exp(LinkGatewayToProcessor, latency(t, 300), percent(t, 10)))
	check(t, "300ms on 10%: baseline established", held.BaselineHeld)
	check(t, "300ms on 10%: hypothesis held", held.HypothesisHeld && !held.Aborted)
	check(t, "300ms on 10%: p99 rose but stayed under the SLO", held.Experimental.P99Millis() > held.Baseline.P99Millis() &&
		held.Experimental.P99Millis() <= Steady.MaxP99Millis)

	aborted := w.Runner.Run(exp(TargetProcessor, Error{"HTTP_500"}, percent(t, 70)))
	check(t, "500s on 70%: abort fired", aborted.Aborted)
	check(t, "500s on 70%: abort fired at the first checkpoint", aborted.RequestsBeforeAbort == Abort.CheckEvery)
	check(t, "500s on 70%: faults cleared after abort", !w.Injector.IsArmed())
	check(t, "500s on 70%: system recovered", aborted.Recovered)
	check(t, "500s on 70%: verdict mentions ABORTED", strings.HasPrefix(aborted.Verdict(), "ABORTED"))

	skewed := w.Runner.Run(exp(TargetProcessor, ClockSkew{6 * 60 * 1000}, Everyone{}))
	check(t, "skew experiment: aborted, the processor rejected everything", skewed.Aborted && skewed.Experimental.Count(STALE_REQUEST) > 0)
	check(t, "skew experiment: invariant held throughout", skewed.NoDoubleAuthorizations)

	partition := w.Runner.Run(exp(LinkGatewayToProcessor, Partition{}, NewMerchants("m-3")))
	check(t, "partition on one merchant: not aborted (good rate stays above 80%)", !partition.Aborted)
	check(t, "partition on one merchant: hypothesis violated via p99", !partition.HypothesisHeld &&
		partition.Experimental.P99Millis() > Steady.MaxP99Millis)
	check(t, "partition on one merchant: no double authorization", partition.NoDoubleAuthorizations)

	w = createWorld()
	impossible := Experiment{"x", "h", TargetProcessor, Error{"E"}, Everyone{}, SteadyState{100.0, 50}, Abort, 100}
	notRun := w.Runner.Run(impossible)
	check(t, "impossible steady state: not run", !notRun.BaselineHeld && strings.HasPrefix(notRun.Verdict(), "NOT RUN"))
	check(t, "impossible steady state: injector never armed", !w.Injector.IsArmed() && w.Injector.AppliedCount() == 0)

	a := createWorld().Runner.Run(exp(LinkGatewayToProcessor, latency(t, 2_500), percent(t, 10)))
	b := createWorld().Runner.Run(exp(LinkGatewayToProcessor, latency(t, 2_500), percent(t, 10)))
	check(t, "determinism: identical seeds give identical reports", a.Render() == b.Render())
}

func TestGameDay(t *testing.T) {
	w := createWorld()
	day := NewGameDay(w.Runner)
	day.Run(exp(LinkGatewayToProcessor, latency(t, 300), percent(t, 10)))
	day.Run(exp(TargetProcessor, Error{"HTTP_500"}, percent(t, 70)))
	check(t, "gameday keeps every report", len(day.Reports()) == 2)
	check(t, "gameday summary lists both verdicts", strings.Contains(day.Summary(), "HYPOTHESIS HELD") && strings.Contains(day.Summary(), "ABORTED"))
}

func TestDemoParity(t *testing.T) {
	// The full demo plan reproduces the Java GameDay digit for digit because every RNG is java.util.Random.
	w := createWorld()
	day := NewGameDay(w.Runner)
	first := day.Run(NewExperiment("a", "h", LinkGatewayToProcessor, latency(t, 300), percent(t, 10)))
	check(t, "parity: baseline 488 authorized / 12 declined, p99 120 ms", first.Baseline.Count(AUTHORIZED) == 488 && first.Baseline.Count(DECLINED) == 12 && first.Baseline.P99Millis() == 120)
	check(t, "parity: experimental p99 417 ms", first.Experimental.P99Millis() == 417)
	day.Run(NewExperiment("b", "h", LinkGatewayToProcessor, latency(t, 2_500), percent(t, 10)))
	day.Run(NewExperiment("c", "h", TargetProcessor, Error{"HTTP_500"}, percent(t, 70)))
	day.Run(NewExperiment("d", "h", LinkGatewayToProcessor, Partition{}, NewMerchants("m-3")))
	day.Run(NewExperiment("e", "h", TargetProcessor, ClockSkew{6 * 60 * 1000}, Everyone{}))
	check(t, "parity: 298 faults applied over the whole GameDay", w.Injector.AppliedCount() == 298)
}
