// Tests for phases/14-payments-at-scale/04-reconciliation-at-scale. Run: go test ./...
package main

import (
	"errors"
	"testing"
	"time"
)

func check(t *testing.T, name string, condition bool) {
	t.Helper()
	if !condition {
		t.Errorf("%s", name)
	}
}

func day(s string) time.Time {
	d, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}
	return d
}

func in(ref, date string, cents int64) Rec {
	return Rec{ref, day(date), day(date), OfMinor(cents, USD), Internal}
}

func pr(ref, date string, cents int64) Rec {
	return Rec{ref, day(date), day(date), OfMinor(cents, USD), Processor}
}

func prLate(ref, txDate, valueDate string, cents int64) Rec {
	return Rec{ref, day(txDate), day(valueDate), OfMinor(cents, USD), Processor}
}

var asOf = day("2026-03-31")

func run(t *testing.T, a, b []Rec, tolerance int) *ReconciliationReport {
	t.Helper()
	x, y := append([]Rec{}, a...), append([]Rec{}, b...)
	SortFeed(x)
	SortFeed(y)
	rec, err := NewStreamingReconciler(USD, tolerance)
	if err != nil {
		t.Fatalf("reconciler: %v", err)
	}
	r, err := rec.Reconcile(SliceIterator(x), SliceIterator(y), asOf)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	return r
}

func count(r *ReconciliationReport, bt BreakType) int64 { return r.CountByType()[bt] }

func digest(t *testing.T, recs ...Rec) BucketDigest {
	t.Helper()
	d := EmptyDigest
	for _, r := range recs {
		var err error
		if d, err = d.Add(r); err != nil {
			t.Fatalf("digest: %v", err)
		}
	}
	return d
}

func TestDigest(t *testing.T) {
	d1 := digest(t, in("A", "2026-03-01", 100), in("B", "2026-03-01", 100))
	d2 := digest(t, in("B", "2026-03-01", 100), in("A", "2026-03-01", 100))
	check(t, "digest is order independent", d1 == d2)
	check(t, "digest differs on a different ref", d1 != digest(t, in("A", "2026-03-01", 100), in("C", "2026-03-01", 100)))
	check(t, "digest counts records", d1.Count == 2 && d1.TotalMinor == 200)
	check(t, "mix wraps like Java's long", Mix("TX") != 0 && Mix("TX") != Mix("XT"))
}

func TestHappyPath(t *testing.T) {
	a := []Rec{in("A", "2026-03-01", 1999), in("B", "2026-03-01", 1999), in("C", "2026-03-02", 500)}
	b := []Rec{pr("A", "2026-03-01", 1999), pr("B", "2026-03-01", 1999), pr("C", "2026-03-02", 500)}
	r := run(t, a, b, 2)
	check(t, "all matched", r.MatchedCount == 3 && len(r.Breaks) == 0)
	check(t, "fast path used for every bucket", r.BucketsFastMatched == 2 && r.BucketsInspected == 0)
	check(t, "totals agree", r.InternalTotal == r.ProcessorTotal && r.MatchedTotal.MinorUnits == 4498)
	check(t, "ties out", r.TiesOut())
}

func TestBreakClassification(t *testing.T) {
	t.Run("missing at processor", func(t *testing.T) {
		r := run(t, []Rec{in("A", "2026-03-01", 100)}, nil, 2)
		check(t, "missing at processor", count(r, MissingAtProcessor) == 1 && r.Breaks[0].Processor == nil)
		check(t, "missing at processor ties out", r.TiesOut() && r.InternalInBreaks().MinorUnits == 100)
	})
	t.Run("missing internal", func(t *testing.T) {
		r := run(t, nil, []Rec{pr("Z", "2026-03-01", 4200)}, 2)
		check(t, "missing internal", count(r, MissingInternal) == 1 && r.ProcessorInBreaks().MinorUnits == 4200 && r.TiesOut())
	})
	t.Run("amount mismatch", func(t *testing.T) {
		r := run(t, []Rec{in("C", "2026-03-02", 1250)}, []Rec{pr("C", "2026-03-02", 1200)}, 2)
		check(t, "amount mismatch classified", count(r, AmountMismatch) == 1)
		check(t, "amount mismatch carries both sides", r.Breaks[0].Internal.Amount.MinorUnits == 1250 && r.Breaks[0].Processor.Amount.MinorUnits == 1200)
		check(t, "amount mismatch ties out", r.TiesOut() && r.MatchedCount == 0)
	})
	t.Run("timing", func(t *testing.T) {
		r := run(t, []Rec{in("B", "2026-03-01", 5000)}, []Rec{prLate("B", "2026-03-01", "2026-03-02", 5000)}, 2)
		check(t, "one day lag inside tolerance is matched on the fast path", r.MatchedCount == 1 && len(r.Breaks) == 0 && r.BucketsFastMatched == 1 && r.TiesOut())
		r = run(t, []Rec{in("B", "2026-03-01", 5000)}, []Rec{prLate("B", "2026-03-01", "2026-03-06", 5000)}, 2)
		check(t, "five day lag is a TIMING break", count(r, Timing) == 1 && r.TiesOut() && r.ProcessorInBreaks().MinorUnits == 5000)
		r = run(t, []Rec{in("B", "2026-03-01", 5000)}, []Rec{prLate("B", "2026-03-01", "2026-03-02", 5000)}, 0)
		check(t, "zero tolerance turns the same lag into a break", count(r, Timing) == 1)
	})
	t.Run("duplicates", func(t *testing.T) {
		r := run(t, []Rec{in("A", "2026-03-01", 100)}, []Rec{pr("A", "2026-03-01", 100), prLate("A", "2026-03-01", "2026-03-02", 100)}, 2)
		check(t, "duplicate settlement line", count(r, Duplicate) == 1 && r.MatchedCount == 1 && r.TiesOut())
		check(t, "duplicate break names the processor side", r.Breaks[0].Internal == nil && r.Breaks[0].Processor != nil)
		r = run(t, []Rec{in("A", "2026-03-01", 100), in("A", "2026-03-01", 100)}, nil, 2)
		check(t, "two internal copies with no partner: one missing, one duplicate",
			count(r, MissingAtProcessor) == 1 && count(r, Duplicate) == 1 && r.TiesOut())
	})
}

func TestAging(t *testing.T) {
	r := run(t, []Rec{in("D", "2026-03-01", 700), in("E", "2026-03-30", 700)}, nil, 2)
	ages := r.CountByAge()
	check(t, "aging bands", ages[D8_30] == 1 && ages[D0_1] == 1)
	check(t, "age band boundaries", AgeBandOf(1) == D0_1 && AgeBandOf(2) == D2_7 && AgeBandOf(7) == D2_7 &&
		AgeBandOf(8) == D8_30 && AgeBandOf(31) == D30Plus)
	check(t, "age never negative", AgeDays(day("2026-04-30"), day("2026-03-31")) == 0)
}

func TestRefusals(t *testing.T) {
	rec, _ := NewStreamingReconciler(USD, 2)
	unsorted := []Rec{in("B", "2026-03-02", 100), in("A", "2026-03-01", 100)}
	_, err := rec.Reconcile(SliceIterator(unsorted), SliceIterator(nil), asOf)
	check(t, "unsorted feed refused", errors.Is(err, ErrUnsortedFeed))
	_, err = rec.Reconcile(SliceIterator([]Rec{pr("A", "2026-03-01", 1)}), SliceIterator(nil), asOf)
	check(t, "wrong-side record refused", errors.Is(err, ErrWrongFeed))
	_, err = NewStreamingReconciler(USD, -1)
	check(t, "negative tolerance refused", err != nil)
}

func TestSeededLargeRun(t *testing.T) {
	feeds := FeedGenerator.Generate(7, 100_000, day("2026-03-01"), 30, USD, 0.001)
	rec, _ := NewStreamingReconciler(USD, 2)
	big, err := rec.Reconcile(SliceIterator(feeds.Internal), SliceIterator(feeds.Processor), asOf)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	check(t, "large run ties out", big.TiesOut())
	check(t, "large run finds every injected break", len(big.Breaks) == feeds.InjectedBreaks)
	var withInternal int64
	for _, b := range big.Breaks {
		if b.Internal != nil {
			withInternal++
		}
	}
	check(t, "large run matched + breaks cover internal feed", big.MatchedCount+withInternal == int64(len(feeds.Internal)))
	check(t, "large run mostly fast path", big.BucketsFastMatched > big.BucketsInspected*10)
	check(t, "determinism: same seed, same report",
		FeedGenerator.Generate(7, 100_000, day("2026-03-01"), 30, USD, 0.001).InjectedBreaks == feeds.InjectedBreaks)
}

func TestJavaRandomMatchesTheJdk(t *testing.T) {
	// Values taken from java.util.Random(2026): nextInt(), nextInt(66), nextInt(64), nextLong().
	r := NewJavaRandom(2026)
	check(t, "nextInt()", r.NextInt() == -1637959697)
	check(t, "nextInt(66)", r.NextIntBound(66) == 52)
	check(t, "nextInt(64)", r.NextIntBound(64) == 54)
	check(t, "nextLong()", r.NextLong() == 3301047695993903762)
}
