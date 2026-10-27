// Tests for phases/02-algorithms/13-interval-problems. Run: go test ./...
package main

import (
	"errors"
	"math"
	"math/bits"
	"slices"
	"testing"
)

func check(t *testing.T, name string, condition bool) {
	t.Helper()
	if !condition {
		t.Errorf("FAIL: %s", name)
	}
}

var (
	windows = []Interval{iv(840, 930), iv(900, 960), iv(960, 1005), iv(540, 600), iv(1080, 1080)}
	slots   = []Interval{iv(1, 4), iv(3, 5), iv(0, 6), iv(5, 7), iv(3, 9), iv(5, 9), iv(6, 10), iv(8, 11), iv(8, 12), iv(2, 14), iv(12, 16)}
)

func TestIntervalType(t *testing.T) {
	check(t, "length", iv(3, 10).Length() == 7)
	check(t, "touching intervals do not overlap", !iv(0, 10).Overlaps(iv(10, 20)))
	check(t, "touching intervals do touch", iv(0, 10).OverlapsOrTouches(iv(10, 20)))
	check(t, "overlap is symmetric", iv(0, 10).Overlaps(iv(5, 15)) && iv(5, 15).Overlaps(iv(0, 10)))
	check(t, "contains is half-open", iv(0, 10).Contains(0) && iv(0, 10).Contains(9) && !iv(0, 10).Contains(10))
	check(t, "intersect", iv(0, 10).Intersect(iv(5, 15)) == iv(5, 10))
	check(t, "intersect of disjoint is empty", iv(0, 5).Intersect(iv(7, 9)).IsEmpty())
	check(t, "sort by start then end", iv(1, 5).Compare(iv(1, 7)) < 0 && iv(2, 3).Compare(iv(1, 9)) > 0)
	_, err := NewInterval(5, 4)
	check(t, "end before start rejected", errors.Is(err, ErrEndBeforeStart))
	check(t, "empty interval allowed", iv(5, 5).IsEmpty())
}

func TestMerge(t *testing.T) {
	check(t, "merge: hand trace", slices.Equal(Merge(windows), []Interval{iv(540, 600), iv(840, 1005)}))
	check(t, "merge: touching intervals merge", slices.Equal(Merge([]Interval{iv(0, 5), iv(5, 10)}), []Interval{iv(0, 10)}))
	check(t, "merge: contained interval absorbed", slices.Equal(Merge([]Interval{iv(0, 100), iv(10, 20)}), []Interval{iv(0, 100)}))
	check(t, "merge: unsorted input", slices.Equal(Merge([]Interval{iv(50, 60), iv(0, 10), iv(5, 55)}), []Interval{iv(0, 60)}))
	check(t, "merge: empty input", len(Merge(nil)) == 0)
	check(t, "merge: empty intervals dropped", len(Merge([]Interval{iv(3, 3), iv(7, 7)})) == 0)
	check(t, "merge: input untouched", windows[0] == iv(840, 930))
	merged := Merge(windows)
	disjoint := true
	for i := 1; i < len(merged); i++ {
		disjoint = disjoint && merged[i-1].End < merged[i].Start
	}
	check(t, "merge: output is strictly disjoint and sorted", disjoint)
}

func TestCoverageAndGaps(t *testing.T) {
	check(t, "coverage 225", must(Coverage(windows)) == 225)
	check(t, "gaps in 08:00-19:00", slices.Equal(Gaps(windows, iv(480, 1140)), []Interval{iv(480, 540), iv(600, 840), iv(1005, 1140)}))
	check(t, "gaps: fully covered range has none", len(Gaps([]Interval{iv(0, 10)}, iv(2, 8))) == 0)
	check(t, "gaps: nothing covered is one gap", slices.Equal(Gaps(nil, iv(0, 10)), []Interval{iv(0, 10)}))
	check(t, "gaps: interval outside the range ignored", slices.Equal(Gaps([]Interval{iv(100, 200)}, iv(0, 10)), []Interval{iv(0, 10)}))
	_, err := Coverage([]Interval{iv(0, math.MaxInt64), iv(math.MaxInt64-1, math.MaxInt64), {-10, -1}})
	check(t, "coverage reports int64 overflow instead of wrapping", errors.Is(err, ErrOverflow))
}

func TestSweepLine(t *testing.T) {
	check(t, "maxOverlap: hand trace", MaxOverlap(windows) == Peak{2, 900})
	check(t, "maxOverlap: touching intervals count as 1", MaxOverlap([]Interval{iv(0, 10), iv(10, 20)}).Count == 1)
	check(t, "maxOverlap: three nested", MaxOverlap([]Interval{iv(0, 10), iv(2, 8), iv(4, 6)}) == Peak{3, 4})
	check(t, "maxOverlap: empty", MaxOverlap(nil) == Peak{0, 0})
	check(t, "maxOverlap: first instant reported", MaxOverlap([]Interval{iv(0, 5), iv(1, 5), iv(10, 15), iv(11, 15)}) == Peak{2, 1})
}

func TestScheduling(t *testing.T) {
	chosen := MaxNonOverlapping(slots)
	check(t, "scheduling: textbook answer has 4", slices.Equal(chosen, []Interval{iv(1, 4), iv(5, 7), iv(8, 11), iv(12, 16)}))
	check(t, "scheduling: chosen are pairwise disjoint", MaxOverlap(chosen).Count == 1)
	check(t, "scheduling: touching allowed", len(MaxNonOverlapping([]Interval{iv(0, 5), iv(5, 10)})) == 2)
	check(t, "scheduling: earliest-start greedy would be wrong here", len(MaxNonOverlapping([]Interval{iv(0, 100), iv(1, 2), iv(3, 4)})) == 2)
	check(t, "scheduling: empty", len(MaxNonOverlapping(nil)) == 0)
	check(t, "stabbing", slices.Equal(Stabbing(slots, 5), []Interval{iv(0, 6), iv(2, 14), iv(3, 9), iv(5, 7), iv(5, 9)}))
}

// bruteForceMax: largest pairwise-disjoint subset by trying every subset (n <= 10).
func bruteForceMax(ivs []Interval) int {
	best := 0
	for mask := 0; mask < 1<<len(ivs); mask++ {
		ok := true
		for i := 0; i < len(ivs) && ok; i++ {
			if mask&(1<<i) == 0 {
				continue
			}
			for j := i + 1; j < len(ivs); j++ {
				if mask&(1<<j) != 0 && ivs[i].Overlaps(ivs[j]) {
					ok = false
					break
				}
			}
		}
		if ok {
			best = max(best, bits.OnesCount(uint(mask)))
		}
	}
	return best
}

// bruteForceOverlap: max number of intervals containing any single instant, by checking every instant.
func bruteForceOverlap(ivs []Interval) int {
	best := 0
	for tm := int64(0); tm < 40; tm++ {
		c := 0
		for _, iv := range ivs {
			if iv.Contains(tm) {
				c++
			}
		}
		best = max(best, c)
	}
	return best
}

func bruteForceCoverage(ivs []Interval) int64 {
	var c int64
	for tm := int64(0); tm < 40; tm++ {
		for _, iv := range ivs {
			if iv.Contains(tm) {
				c++
				break
			}
		}
	}
	return c
}

func TestRandomInstances(t *testing.T) {
	// greedy optimality cross-checked against brute force on 100 seeded instances (java.util.Random(13))
	rng := NewJavaRandom(13)
	optimal := true
	for trial := 0; trial < 100; trial++ {
		n := 1 + rng.NextIntN(10)
		rand := make([]Interval, 0, n)
		for i := 0; i < n; i++ {
			s := int64(rng.NextIntN(20))
			rand = append(rand, iv(s, s+1+int64(rng.NextIntN(8))))
		}
		optimal = optimal && len(MaxNonOverlapping(rand)) == bruteForceMax(rand)
		optimal = optimal && MaxOverlap(rand).Count == bruteForceOverlap(rand)
		optimal = optimal && must(Coverage(rand)) == bruteForceCoverage(rand)
	}
	check(t, "100 random instances: greedy size, sweep peak, and coverage match brute force", optimal)
}

func TestHoldBook(t *testing.T) {
	book := SampleHolds()
	usd := USD
	check(t, "exposure at 12:30", must(book.ExposureAt(T(12, 30))) == OfMinor(83_000, usd))
	check(t, "exposure at 17:00 is half-open: hotel gone, coffee on", must(book.ExposureAt(T(17, 0))) == OfMinor(600, usd))
	check(t, "exposure before any hold", must(book.ExposureAt(0)) == Zero(usd))
	peak := must(book.PeakExposure())
	check(t, "peak exposure 875.00 at 11:00 with 3 holds", peak == PeakExposure{OfMinor(87_500, usd), T(11, 0), 3})
	check(t, "peak exposure equals exposureAt(peak.at)", must(book.ExposureAt(peak.At)) == peak.Amount)
	check(t, "busy periods merge touching holds", slices.Equal(book.BusyPeriods(), []Interval{iv(T(9, 0), T(17, 15))}))
	check(t, "expired by 14:00", slices.Equal(ids(book.ExpiredBy(T(14, 0))), []string{"fuel", "restaurant", "parking"}))
	_, err := NewHold("x", Zero(usd), iv(0, 1))
	check(t, "hold amount must be positive", errors.Is(err, ErrNonPositive))
	err = NewHoldBook(usd).Add(must(NewHold("x", OfMinor(1, EUR), iv(0, 1))))
	check(t, "currency mismatch on the book", errors.Is(err, ErrWrongBook))
	check(t, "empty book peak is zero", must(NewHoldBook(usd).PeakExposure()) == PeakExposure{Zero(usd), 0, 0})
	check(t, "money String", OfMinor(87_500, usd).String() == "875.00 USD")
	_, err = OfMinor(math.MaxInt64, usd).Plus(OfMinor(1, usd))
	check(t, "money plus reports overflow", errors.Is(err, ErrOverflow))
	_, err = NewCurrency("usd", 2)
	check(t, "currency code must be uppercase", err != nil)
}
