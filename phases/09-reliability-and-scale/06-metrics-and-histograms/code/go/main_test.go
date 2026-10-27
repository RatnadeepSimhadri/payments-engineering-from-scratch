package main

import (
	"math"
	"slices"
	"strings"
	"testing"
)

func check(t *testing.T, name string, condition bool) {
	t.Helper()
	if !condition {
		t.Errorf("%s", name)
	}
}

func hist(t *testing.T, bits int, highest int64) *LogLinearHistogram {
	t.Helper()
	h, err := NewLogLinearHistogram(bits, highest)
	if err != nil {
		t.Fatalf("NewLogLinearHistogram(%d, %d): %v", bits, highest, err)
	}
	return h
}

func record(t *testing.T, h *LogLinearHistogram, v int64) {
	t.Helper()
	if err := h.Record(v); err != nil {
		t.Fatalf("Record(%d): %v", v, err)
	}
}

func estimate(t *testing.T, h *LogLinearHistogram, p float64) Estimate {
	t.Helper()
	e, err := h.Percentile(p)
	if err != nil {
		t.Fatalf("Percentile(%v): %v", p, err)
	}
	return e
}

func TestCounters(t *testing.T) {
	c := &Counter{}
	c.Inc()
	if err := c.IncBy(4); err != nil {
		t.Fatal(err)
	}
	check(t, "counter accumulates", c.Value() == 5)
	check(t, "counter rejects negative increments", c.IncBy(-1) != nil)
	r, err := Rate(1000, 1250, 10)
	check(t, "rate over an interval", err == nil && r == 25.0)
	r, err = Rate(1250, 40, 10)
	check(t, "rate after a reset assumes restart from zero", err == nil && r == 4.0)
	_, err = Rate(0, 1, 0)
	check(t, "rate rejects a zero interval", err != nil)
	full := &Counter{value: math.MaxInt64}
	check(t, "counter reports overflow instead of wrapping", full.IncBy(1) == ErrOverflow)
}

func TestGauge(t *testing.T) {
	g := &Gauge{}
	g.Set(10)
	g.Inc()
	g.Dec()
	g.Dec()
	check(t, "gauge goes up and down", g.Value() == 9)
}

func TestBucketLayout(t *testing.T) {
	h := hist(t, 7, MaxMicros)
	inverse, contiguous, widthBound := true, true, true
	for i := 0; i < h.BucketCount()-1; i++ {
		lo, hi := h.LowerBound(i), h.UpperBound(i)
		if h.IndexOf(lo) != i || h.IndexOf(hi) != i {
			inverse = false
		}
		if h.LowerBound(i+1) != hi+1 {
			contiguous = false
		}
		if lo >= 128 && (hi-lo+1)*128 > lo {
			widthBound = false
		}
	}
	check(t, "indexOf(lowerBound(i)) == i and indexOf(upperBound(i)) == i for every bucket", inverse)
	check(t, "buckets are contiguous with no gaps", contiguous)
	check(t, "every bucket above the exact range is at most 1/128 of its lower bound wide", widthBound)
	check(t, "values below 2^precisionBits are exact", h.IndexOf(0) == 0 && h.IndexOf(127) == 127 && h.UpperBound(127) == 127)
	check(t, "first log-linear bucket starts at 128", h.LowerBound(128) == 128 && h.IndexOf(128) == 128)
	check(t, "bucket for 1,000,000 is 4,096 wide", h.UpperBound(h.IndexOf(1_000_000))-h.LowerBound(h.IndexOf(1_000_000))+1 == 4096)
	check(t, "highest trackable value has a bucket", h.IndexOf(MaxMicros) == h.BucketCount()-1)
	check(t, "negative values are rejected", h.Record(-1) != nil)
	check(t, "values above highestTrackable are rejected", h.Record(MaxMicros+1) != nil)
	_, err := NewLogLinearHistogram(0, MaxMicros)
	check(t, "precision must be at least 1 bit", err != nil)
	_, err = NewLogLinearHistogram(7, 100)
	check(t, "highestTrackable below 2^precisionBits is rejected", err != nil)
}

func TestPercentilesSmall(t *testing.T) {
	h := hist(t, 7, MaxMicros)
	for v := int64(1); v <= 100; v++ {
		record(t, h, v)
	}
	check(t, "count", h.Count() == 100)
	check(t, "min and max", h.Min() == 1 && h.Max() == 100)
	check(t, "mean is exact", h.Mean() == 50.5)
	check(t, "p50 of 1..100 is 50", h.Value(50) == 50)
	check(t, "p99 of 1..100 is 99", h.Value(99) == 99)
	check(t, "p100 is the max", h.Value(100) == 100)
	check(t, "p0 is the min", h.Value(0) == 1)
	e := estimate(t, h, 50)
	check(t, "exact-range estimate has zero width", e.Lower == 50 && e.Upper == 50)
	check(t, "fraction at or below 25 is 0.25", h.FractionAtOrBelow(25) == 0.25)
	empty := hist(t, 7, MaxMicros)
	check(t, "empty histogram reports zeros", empty.Count() == 0 && empty.Value(99) == 0 && empty.Mean() == 0 && empty.Min() == 0)
	_, err := h.Percentile(101)
	check(t, "percentile above 100 rejected", err != nil)
	check(t, "estimate renders like Java", e.String() == "p50 in [50, 50]" && Trim(99.9) == "99.9")
}

func TestErrorBoundOnSkewedData(t *testing.T) {
	rng := NewRng(2024)
	samples := AuthorizationLatencies(100_000, 0.01, rng)
	h := hist(t, 7, MaxMicros)
	for _, s := range samples {
		record(t, h, s)
	}
	sorted := slices.Clone(samples)
	slices.Sort(sorted)
	bracketed, bounded := true, true
	for _, p := range []float64{1, 10, 50, 90, 99, 99.9, 99.99} {
		e := estimate(t, h, p)
		exact := ExactPercentile(sorted, p)
		if exact < e.Lower || exact > e.Upper {
			bracketed = false
		}
		if e.RelativeError() > 1.0/128 {
			bounded = false
		}
	}
	check(t, "every exact percentile lies inside the histogram's bucket bounds", bracketed)
	check(t, "relative error of every estimate is at most 1/128", bounded)
	check(t, "mean sits far below p99 on the skewed data (averages lie)", h.Mean()*10 < float64(h.Value(99)))
	check(t, "p50 is near 12 ms", h.Value(50) > 11_000 && h.Value(50) < 13_500)
	check(t, "p99 is over a second", h.Value(99) > 1_000_000)
	sli := h.FractionAtOrBelow(100_000)
	var exactAtOrBelow int64
	for _, v := range sorted {
		if v <= 100_000 {
			exactAtOrBelow++
		}
	}
	check(t, "SLI from buckets within one bucket of the exact fraction", math.Abs(sli-float64(exactAtOrBelow)/100_000.0) < 0.001)
	fine := hist(t, 10, MaxMicros)
	for _, s := range samples {
		record(t, fine, s)
	}
	check(t, "more precision bits give a tighter p99 bracket", estimate(t, fine, 99).RelativeError() <= 1.0/1024 && fine.BucketCount() > h.BucketCount())
	// Demo parity: the same SplitMix64 stream and rounding give the same samples as the Java demo.
	check(t, "parity: histogram sum matches the Java exposition", javaRound(h.Mean()*float64(h.Count())) == 3529462222)
	check(t, "parity: p99 upper bound matches the Java demo", h.Value(99) == 1_155_071)
}

func TestMerge(t *testing.T) {
	r1, r2 := NewRng(7), NewRng(8)
	a, b := AuthorizationLatencies(90_000, 0.0, r1), AuthorizationLatencies(10_000, 0.10, r2)
	ha, hb, all := hist(t, 7, MaxMicros), hist(t, 7, MaxMicros), hist(t, 7, MaxMicros)
	for _, s := range a {
		record(t, ha, s)
		record(t, all, s)
	}
	for _, s := range b {
		record(t, hb, s)
		record(t, all, s)
	}
	merged := ha.Copy()
	if err := merged.Merge(hb); err != nil {
		t.Fatal(err)
	}
	check(t, "merged count is the sum", merged.Count() == 100_000)
	check(t, "merge equals recording everything into one histogram", merged.Value(99) == all.Value(99) && merged.Value(50) == all.Value(50) && merged.Mean() == all.Mean())
	other := hb.Copy()
	if err := other.Merge(ha); err != nil {
		t.Fatal(err)
	}
	check(t, "merge is commutative", other.Value(99) == merged.Value(99) && other.Min() == merged.Min() && other.Max() == merged.Max())
	avgOfP99 := (ha.Value(99) + hb.Value(99)) / 2
	diff := avgOfP99 - merged.Value(99)
	if diff < 0 {
		diff = -diff
	}
	check(t, "average of per-host p99s is not the fleet p99", diff > merged.Value(99)/4)
	check(t, "fleet p99 is between the two host p99s", merged.Value(99) > ha.Value(99) && merged.Value(99) < hb.Value(99))
	check(t, "copy does not alias the source", ha.Copy().Count() == ha.Count() && merged.Count() != ha.Count())
	check(t, "merging different layouts is rejected", ha.Merge(hist(t, 8, MaxMicros)) != nil)
}

func TestRecordManyAndCumulative(t *testing.T) {
	h := hist(t, 7, MaxMicros)
	for _, c := range []struct{ v, n int64 }{{10, 5}, {1_000, 3}, {5_000_000, 2}} {
		if err := h.RecordMany(c.v, c.n); err != nil {
			t.Fatal(err)
		}
	}
	check(t, "recordMany counts and sums", h.Count() == 10 && h.Mean() == float64(50+3_000+10_000_000)/10.0)
	cum := h.Cumulative([]int64{10, 999, 1_003, 1_000_000})
	check(t, "cumulative counts are monotone and match", cum[0] == 5 && cum[1] == 5 && cum[2] == 8 && cum[3] == 8)
	check(t, "recordMany rejects zero count", h.RecordMany(1, 0) != nil)
}

func TestRegistryExposition(t *testing.T) {
	reg := NewMetricsRegistry(7, MaxMicros)
	if err := reg.Counter("req").IncBy(3); err != nil {
		t.Fatal(err)
	}
	reg.Gauge("inflight").Set(2)
	record(t, reg.Histogram("lat"), 500)
	record(t, reg.Histogram("lat"), 2_000)
	check(t, "registry returns the same metric for the same name", reg.Counter("req") == reg.Counter("req") && reg.Histogram("lat").Count() == 2)
	text := reg.Render([]int64{1_000, 10_000})
	check(t, "counter rendered with _total", strings.Contains(text, "# TYPE req_total counter\nreq_total 3\n"))
	check(t, "gauge rendered", strings.Contains(text, "# TYPE inflight gauge\ninflight 2\n"))
	check(t, "histogram buckets are cumulative", strings.Contains(text, "lat_bucket{le=\"1000\"} 1\n") && strings.Contains(text, "lat_bucket{le=\"10000\"} 2\n") && strings.Contains(text, "lat_bucket{le=\"+Inf\"} 2\n"))
	check(t, "histogram sum and count rendered", strings.Contains(text, "lat_sum 2500\n") && strings.Contains(text, "lat_count 2\n"))
}

func TestGeneratorIsDeterministic(t *testing.T) {
	check(t, "splitmix64 seed 42 first value", NewRng(42).NextLong() == -4767286540954276203)
	check(t, "log-normal samples are positive", NewRng(1).NextLogNormalMicros(12_000, 0.5) > 0)
	check(t, "commas formats like %,d", commas(1_000_000) == "1,000,000" && commas(5) == "5" && commas(-12345) == "-12,345")
}
