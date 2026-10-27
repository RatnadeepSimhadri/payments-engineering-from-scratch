// Tests for phases/00-setup-and-tooling/05-benchmarking-and-profiling. Run: go test ./...
package main

import (
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
)

func mustPanic(t *testing.T, name string, f func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Errorf("%s: expected panic", name)
		}
	}()
	f()
}

func eq[T comparable](t *testing.T, name string, got, want T) {
	t.Helper()
	if got != want {
		t.Errorf("%s: got %v, want %v", name, got, want)
	}
}

func TestStatsPercentiles(t *testing.T) {
	ten := []int64{10, 20, 30, 40, 50, 60, 70, 80, 90, 100}
	cases := []struct {
		name string
		p    float64
		want int64
	}{
		{"p50 of 1..10 is the 5th value (nearest rank, not interpolated)", 50, 50},
		{"p90 of ten values is the 9th", 90, 90},
		{"p99 of ten values is the max", 99, 100},
		{"p0 is the min", 0, 10},
		{"p100 is the max", 100, 100},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { eq(t, c.name, MustPercentile(ten, c.p), c.want) })
	}
	if ten[0] != 10 || ten[9] != 100 {
		t.Error("percentile does not mutate input")
	}
	unsorted := []int64{90, 10, 50}
	if MustPercentile(unsorted, 50) != 50 || unsorted[0] != 90 {
		t.Error("percentile sorts a copy")
	}
	if MustPercentile([]int64{7}, 99) != 7 || MustPercentile([]int64{7}, 1) != 7 {
		t.Error("single sample: every percentile is that sample")
	}
	eq(t, "mean", Mean(ten), 55.0)
	eq(t, "stddev of constant sample is zero", Stddev([]int64{5, 5, 5}), 0.0)
	eq(t, "cv of constant sample is zero", CoefficientOfVariation([]int64{5, 5, 5}), 0.0)
	if math.Abs(CoefficientOfVariation([]int64{10, 20})-CoefficientOfVariation([]int64{1000, 2000})) >= 1e-12 {
		t.Error("cv is dimensionless: scaling samples leaves it unchanged")
	}
	if _, err := Percentile(nil, 50); !errors.Is(err, ErrNoSamples) {
		t.Error("percentile rejects empty sample")
	}
	if _, err := Percentile(ten, 101); err == nil {
		t.Error("percentile rejects p > 100")
	}
}

func TestHarnessScriptedTicker(t *testing.T) {
	// 1 fork, 2 warm-up, 3 measured, 10 ops/iteration -> 10 readings. Durations: 1000, 900 | 100, 200, 300.
	ticker := NewFakeTicker(0, 1000, 1000, 1900, 1900, 2000, 2000, 2200, 2200, 2500)
	harness := NewHarness(ticker, HarnessConfig{2, 3, 1, 10})
	calls := 0
	result, err := harness.Run(mustBenchmark("scripted", func(bh *Blackhole) { calls++; bh.Consume(int64(calls)) }))
	if err != nil {
		t.Fatal(err)
	}
	eq(t, "body ran (warmup + measured) * ops times", calls, (2+3)*10)
	eq(t, "ticker read exactly twice per iteration", ticker.ReadingsConsumed(), 10)
	fork := result.Forks[0]
	if !reflect.DeepEqual(fork.WarmupNsPerOp, []int64{100, 90}) {
		t.Errorf("warm-up samples are kept for inspection but not pooled: %v", fork.WarmupNsPerOp)
	}
	if !reflect.DeepEqual(fork.MeasuredNsPerOp, []int64{10, 20, 30}) {
		t.Errorf("measured samples are ns/op (elapsed / ops): %v", fork.MeasuredNsPerOp)
	}
	if len(result.PooledNsPerOp) != 3 || result.Min() != 10 || result.Max() != 30 {
		t.Error("pooled excludes warm-up")
	}
	eq(t, "p50 of measured", result.P50(), 20)
	if harness.Blackhole().Value() == 0 {
		t.Error("blackhole saw the results")
	}
	if result.ForkSpread() != 0.0 || !result.Stable() {
		t.Error("single fork has zero spread and is stable")
	}
}

func TestTwoForksDisagree(t *testing.T) {
	two := NewFakeTicker(
		0, 100, 100, 200, // fork 1: measured 100 -> 100 ns/op (ops=1), warmup 0
		200, 300, 300, 1000) // fork 2: measured 700
	twoForks, err := NewHarness(two, HarnessConfig{1, 1, 2, 1}).Run(mustBenchmark("drift", func(bh *Blackhole) { bh.Consume(1) }))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(twoForks.ForkMedians(), []int64{100, 700}) {
		t.Error("per-fork medians reported in order")
	}
	if twoForks.Stable() || twoForks.ForkSpread() <= UnstableCV {
		t.Error("forks that disagree by 7x are UNSTABLE")
	}
	if !strings.Contains(twoForks.Row(), "UNSTABLE") {
		t.Error("row() carries the UNSTABLE marker")
	}
	eq(t, "pooled samples span both forks", len(twoForks.PooledNsPerOp), 2)
}

func TestHarnessFailures(t *testing.T) {
	mustPanic(t, "exhausted ticker fails loudly", func() {
		_, _ = NewHarness(NewFakeTicker(0, 10), HarnessConfig{0, 2, 1, 1}).Run(mustBenchmark("short", func(bh *Blackhole) { bh.Consume(1) }))
	})
	_, err := NewHarness(NewFakeTicker(500, 400), HarnessConfig{0, 1, 1, 1}).Run(mustBenchmark("back", func(bh *Blackhole) { bh.Consume(1) }))
	if !errors.Is(err, ErrTickerBackwards) {
		t.Error("ticker going backwards is an error")
	}
	if _, err := NewHarnessConfig(1, 0, 1, 1); err == nil {
		t.Error("config rejects zero measured iterations")
	}
	if _, err := NewHarnessConfig(1, 1, 0, 1); err == nil {
		t.Error("config rejects zero forks")
	}
	if _, err := NewBenchmark(" ", func(bh *Blackhole) {}); err == nil {
		t.Error("benchmark rejects blank name")
	}
}

func TestBlackhole(t *testing.T) {
	bh := &Blackhole{}
	bh.Consume(0b1010)
	eq(t, "blackhole xors values in", bh.Value(), int64(0b1010))
	bh.Consume(0b1010)
	eq(t, "blackhole state is a fold of everything consumed", bh.Value(), 0)
	bh.ConsumeAny(nil)
	eq(t, "blackhole tolerates nil", bh.Value(), 0)
}

func TestCoordinatedOmission(t *testing.T) {
	rec := &LatencyRecorder{}
	_ = rec.Record(5_000, 1_000)
	if rec.RawCount() != 1 || rec.CorrectedCount() != 5 {
		t.Error("a 5 ms latency at a 1 ms interval records 5 corrected samples")
	}
	corrected := rec.CorrectedSamples()
	if corrected[0] != 5_000 || corrected[1] != 4_000 || corrected[4] != 1_000 {
		t.Error("back-filled samples step down by the interval")
	}
	fine := &LatencyRecorder{}
	_ = fine.Record(999, 1_000)
	eq(t, "latency below the interval adds nothing", fine.CorrectedCount(), 1)
	exact := &LatencyRecorder{}
	_ = exact.Record(2_000, 1_000)
	if exact.CorrectedCount() != 2 || exact.CorrectedSamples()[1] != 1_000 {
		t.Error("latency of exactly two intervals back-fills one")
	}
	load := &LatencyRecorder{}
	for i := 0; i < 999; i++ {
		_ = load.Record(1_000, 1_000)
	}
	_ = load.Record(1_000_000, 1_000)
	eq(t, "raw p99 hides a one-second stall", MustPercentile(load.RawSamples(), 99), 1_000)
	if MustPercentile(load.CorrectedSamples(), 99) <= 900_000 {
		t.Error("corrected p99 exposes it")
	}
	eq(t, "corrected count = raw + missing requests", load.CorrectedCount(), 1000+999)
	if err := (&LatencyRecorder{}).Record(1, 0); err == nil {
		t.Error("recorder rejects non-positive interval")
	}
}

func TestSampleProfile(t *testing.T) {
	profile := NewSampleProfile()
	_ = profile.Record("main", "a", "b")
	_ = profile.Record("main", "a", "b")
	_ = profile.Record("main", "a", "c")
	_ = profile.Record("main", "d")
	eq(t, "samples counted", profile.Samples(), 4)
	if profile.SelfCount("b") != 2 || profile.SelfCount("a") != 0 || profile.SelfCount("main") != 0 {
		t.Error("self time goes to the leaf only")
	}
	if profile.InclusiveCount("a") != 3 || profile.InclusiveCount("main") != 4 {
		t.Error("inclusive time goes to every frame on the stack")
	}
	if top := profile.TopSelf(3); top[0].Frame != "b" || top[1].Frame != "c" {
		t.Error("topSelf orders by count then name")
	}
	eq(t, "topInclusive puts the root first", profile.TopInclusive(1)[0].Frame, "main")
	recursive := NewSampleProfile()
	_ = recursive.Record("main", "f", "f", "f")
	if recursive.InclusiveCount("f") != 1 || recursive.SelfCount("f") != 1 {
		t.Error("recursive frame counted once per sample in inclusive time")
	}
	if err := NewSampleProfile().Record(); err == nil {
		t.Error("empty stack rejected")
	}
	if !strings.HasPrefix(profile.Report(2), "4 samples\n") {
		t.Error("report starts with the sample count")
	}
}

func TestSeededIntsDeterministic(t *testing.T) {
	if !reflect.DeepEqual(SeededInts(100, 42), SeededInts(100, 42)) {
		t.Error("seededInts is deterministic")
	}
	// java.util.Random(42).nextInt(100) opens with 30, 63, 48, 84, 70.
	if got := SeededInts(100, 42)[:5]; !reflect.DeepEqual(got, []int{30, 63, 48, 84, 70}) {
		t.Errorf("seededInts matches java.util.Random: %v", got)
	}
}
