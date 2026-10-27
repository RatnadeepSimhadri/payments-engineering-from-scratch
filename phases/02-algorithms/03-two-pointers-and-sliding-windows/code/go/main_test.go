// Tests for phases/02-algorithms/03-two-pointers-and-sliding-windows. Run: go test ./...
package main

import (
	"errors"
	"math"
	"slices"
	"testing"
)

func check(t *testing.T, name string, condition bool) {
	t.Helper()
	if !condition {
		t.Errorf("FAIL: %s", name)
	}
}

// brute-force oracles
func bruteMaxSumFixed(a []int64, k int) int64 {
	best := int64(math.MinInt64)
	for i := 0; i+k <= len(a); i++ {
		var s int64
		for j := i; j < i+k; j++ {
			s += a[j]
		}
		best = max(best, s)
	}
	return best
}

func bruteShortestRun(a []int64, target int64) int {
	best := 0
	for i := range a {
		var s int64
		for j := i; j < len(a); j++ {
			s += a[j]
			if s >= target {
				if l := j - i + 1; best == 0 || l < best {
					best = l
				}
				break
			}
		}
	}
	return best
}

func bruteMaxOfEachWindow(a []int64, k int) []int64 {
	out := make([]int64, len(a)-k+1)
	for i := 0; i+k <= len(a); i++ {
		m := a[i]
		for j := i; j < i+k; j++ {
			m = max(m, a[j])
		}
		out[i] = m
	}
	return out
}

var daily = []int64{12000, 4500, 80000, 2575, 30000, 1999, 500, 75}

// rngAt replays java.util.Random(11) up to the block the Java test reaches, so each subtest sees the same arrays.
func rngAt(block int) *JavaRandom {
	rng := NewJavaRandom(11)
	if block > 0 { // pair oracle: 100 trials
		for t := 0; t < 100; t++ {
			n := rng.NextIntN(12)
			for i := 0; i < n; i++ {
				rng.NextIntN(30)
			}
			rng.NextIntN(60)
		}
	}
	if block > 1 { // fixed-window oracle: 100 trials
		for t := 0; t < 100; t++ {
			n := 1 + rng.NextIntN(20)
			for i := 0; i < n; i++ {
				rng.NextIntN(200)
			}
			rng.NextIntN(n)
		}
	}
	if block > 2 { // variable-window oracle: 200 trials
		for t := 0; t < 200; t++ {
			n := rng.NextIntN(15)
			for i := 0; i < n; i++ {
				rng.NextIntN(10)
			}
			rng.NextIntN(30)
		}
	}
	return rng
}

func TestTwoPointers(t *testing.T) {
	sorted := []int64{75, 500, 500, 1999, 1999, 2575, 4500, 12000}
	p, ok := PairWithSum(sorted, 4574)
	check(t, "pairWithSum finds a pair", ok && sorted[p[0]]+sorted[p[1]] == 4574 && p[0] < p[1])
	_, ok = PairWithSum(sorted, 1000)
	check(t, "pairWithSum with duplicates", ok)
	_, ok = PairWithSum(sorted, 1)
	check(t, "pairWithSum none", !ok)
	_, ok = PairWithSum(nil, 5)
	check(t, "pairWithSum empty", !ok)
	_, ok = PairWithSum([]int64{5}, 10)
	check(t, "pairWithSum single element", !ok)
	rng := rngAt(0)
	pairOracle := true
	for trial := 0; trial < 100; trial++ {
		a := make([]int64, rng.NextIntN(12))
		for i := range a {
			a[i] = int64(rng.NextIntN(30))
		}
		slices.Sort(a)
		target := int64(rng.NextIntN(60))
		exists := false
		for i := range a {
			for j := i + 1; j < len(a); j++ {
				if a[i]+a[j] == target {
					exists = true
				}
			}
		}
		got, found := PairWithSum(a, target)
		if exists != found {
			pairOracle = false
		}
		if found && a[got[0]]+a[got[1]] != target {
			pairOracle = false
		}
	}
	check(t, "pairWithSum agrees with brute force on 100 random arrays", pairOracle)

	dupes := []int64{1, 1, 2, 3, 3, 3, 7}
	n := DedupeSorted(dupes)
	check(t, "dedupeSorted length", n == 4)
	check(t, "dedupeSorted prefix", slices.Equal(dupes[:n], []int64{1, 2, 3, 7}))
	check(t, "dedupeSorted empty", DedupeSorted(nil) == 0)
	check(t, "dedupeSorted all equal", DedupeSorted([]int64{4, 4, 4}) == 1)
	check(t, "mergeSorted", slices.Equal(MergeSorted([]int64{1, 4, 7}, []int64{2, 3, 9}), []int64{1, 2, 3, 4, 7, 9}))
	check(t, "mergeSorted one empty", slices.Equal(MergeSorted(nil, []int64{2, 3}), []int64{2, 3}))
}

func TestFixedWindow(t *testing.T) {
	check(t, "maxSumFixed k=3", must(MaxSumFixed(daily, 3)) == 112_575)
	check(t, "maxSumFixed k=1 is max element", must(MaxSumFixed(daily, 1)) == 80000)
	check(t, "maxSumFixed k=n is total", must(MaxSumFixed(daily, len(daily))) == 131_649)
	_, err := MaxSumFixed(daily, 0)
	check(t, "maxSumFixed rejects k=0", errors.Is(err, ErrBadWindow))
	_, err = MaxSumFixed(daily, 9)
	check(t, "maxSumFixed rejects k>n", errors.Is(err, ErrBadWindow))
	rng := rngAt(1)
	fixedOracle := true
	for trial := 0; trial < 100; trial++ {
		a := make([]int64, 1+rng.NextIntN(20))
		for i := range a {
			a[i] = int64(rng.NextIntN(200) - 50) // negatives allowed here
		}
		k := 1 + rng.NextIntN(len(a))
		if must(MaxSumFixed(a, k)) != bruteMaxSumFixed(a, k) {
			fixedOracle = false
		}
	}
	check(t, "maxSumFixed agrees with brute force (negatives included)", fixedOracle)
}

func TestVariableWindow(t *testing.T) {
	check(t, "shortestRun exact", must(ShortestRunWithSumAtLeast(daily, 100_000)) == 3)
	check(t, "shortestRun single element suffices", must(ShortestRunWithSumAtLeast(daily, 80_000)) == 1)
	check(t, "shortestRun impossible is 0", must(ShortestRunWithSumAtLeast(daily, 1_000_000)) == 0)
	check(t, "shortestRun whole array", must(ShortestRunWithSumAtLeast([]int64{1, 1, 1}, 3)) == 3)
	_, err := ShortestRunWithSumAtLeast([]int64{5, -1, 5}, 4)
	check(t, "shortestRun rejects negatives (window not monotone)", errors.Is(err, ErrNegative))
	rng := rngAt(2)
	varOracle := true
	for trial := 0; trial < 200; trial++ {
		a := make([]int64, rng.NextIntN(15))
		for i := range a {
			a[i] = int64(rng.NextIntN(10))
		}
		target := int64(1 + rng.NextIntN(30))
		if must(ShortestRunWithSumAtLeast(a, target)) != bruteShortestRun(a, target) {
			varOracle = false
		}
	}
	check(t, "shortestRun agrees with brute force on 200 random arrays", varOracle)
	check(t, "longestWithoutRepeat abcabcbb", LongestWithoutRepeat("abcabcbb") == 3)
	check(t, "longestWithoutRepeat bbbb", LongestWithoutRepeat("bbbb") == 1)
	check(t, "longestWithoutRepeat pwwkew", LongestWithoutRepeat("pwwkew") == 3)
	check(t, "longestWithoutRepeat empty", LongestWithoutRepeat("") == 0)
	check(t, "longestWithoutRepeat jumps left past the repeat, not by one", LongestWithoutRepeat("abba") == 2)
	check(t, "longestWithoutRepeat counts code points, not bytes", LongestWithoutRepeat("ééa") == 2)
}

func TestMonotonicDeque(t *testing.T) {
	check(t, "maxOfEachWindow classic", slices.Equal(must(MaxOfEachWindow([]int64{1, 3, -1, -3, 5, 3, 6, 7}, 3)), []int64{3, 3, 5, 5, 6, 7}))
	check(t, "maxOfEachWindow k=1 is identity", slices.Equal(must(MaxOfEachWindow(daily, 1)), daily))
	check(t, "maxOfEachWindow k=n is one global max", slices.Equal(must(MaxOfEachWindow(daily, len(daily))), []int64{80000}))
	check(t, "maxOfEachWindow descending input", slices.Equal(must(MaxOfEachWindow([]int64{9, 8, 7, 6}, 2)), []int64{9, 8, 7}))
	check(t, "maxOfEachWindow equal values", slices.Equal(must(MaxOfEachWindow([]int64{5, 5, 5, 5}, 2)), []int64{5, 5, 5}))
	_, err := MaxOfEachWindow(daily, 0)
	check(t, "maxOfEachWindow rejects k=0", errors.Is(err, ErrBadWindow))
	rng := rngAt(3)
	dequeOracle := true
	for trial := 0; trial < 200; trial++ {
		a := make([]int64, 1+rng.NextIntN(25))
		for i := range a {
			a[i] = int64(rng.NextIntN(10)) // small range: many ties
		}
		k := 1 + rng.NextIntN(len(a))
		if !slices.Equal(must(MaxOfEachWindow(a, k)), bruteMaxOfEachWindow(a, k)) {
			dequeOracle = false
		}
	}
	check(t, "maxOfEachWindow agrees with brute force on 200 random arrays", dequeOracle)
}

func TestDequeRingBuffer(t *testing.T) {
	d := NewDeque[int]()
	for i := 0; i < 20; i++ { // past the initial capacity: forces grow() with a wrapped head
		d.AddLast(i)
		if i%3 == 0 {
			d.PollFirst()
		}
	}
	check(t, "deque length after mixed pushes and pops", d.Len() == 13)
	check(t, "deque front and back", d.PeekFirst() == 7 && d.PeekLast() == 19)
	check(t, "deque pollLast", d.PollLast() == 19 && d.PeekLast() == 18)
}

func TestVelocityChecker(t *testing.T) {
	usd := USD
	vc := must(NewVelocityChecker(60_000, 4, OfMinor(50_000, usd)))
	t0 := int64(1_700_000_000_000)
	rec := func(ts, minor int64) Decision {
		t.Helper()
		return must(vc.Record(Event{ts, OfMinor(minor, usd)}))
	}
	check(t, "vc 1st allowed", rec(t0, 12_000) == Allow)
	check(t, "vc 2nd allowed", rec(t0+10_000, 4_500) == Allow)
	check(t, "vc 3rd allowed", rec(t0+20_000, 25_000) == Allow)
	check(t, "vc 4th allowed (count == max is fine)", rec(t0+25_000, 1_999) == Allow)
	check(t, "vc 5th within window denied by count", rec(t0+30_000, 100) == DenyCount)
	check(t, "vc denied event still counted", vc.CountInWindow() == 5 && vc.TotalInWindow().MinorUnits == 43_599)
	check(t, "vc at t0+70s two events aged out, amount rule fires", rec(t0+70_000, 30_000) == DenyAmount)
	check(t, "vc total after eviction", vc.CountInWindow() == 4 && vc.TotalInWindow().MinorUnits == 25_000+1_999+100+30_000)
	check(t, "vc far future clears most of the window", rec(t0+125_000, 75) == Allow && vc.CountInWindow() == 2)
	_, err := vc.Record(Event{t0, OfMinor(1, usd)})
	check(t, "vc rejects out-of-order events", errors.Is(err, ErrOutOfOrder))
	_, err = NewVelocityChecker(0, 3, OfMinor(1, usd))
	check(t, "vc rejects non-positive window", errors.Is(err, ErrBadVelocity))

	// boundary: an event exactly windowMs after another is outside the trailing window (strict)
	edge := must(NewVelocityChecker(60_000, 1, OfMinor(1_000_000, usd)))
	must(edge.Record(Event{t0, OfMinor(1, usd)}))
	check(t, "vc event at exactly +window evicts the old one", must(edge.Record(Event{t0 + 60_000, OfMinor(1, usd)})) == Allow)
	edge2 := must(NewVelocityChecker(60_000, 1, OfMinor(1_000_000, usd)))
	must(edge2.Record(Event{t0, OfMinor(1, usd)}))
	check(t, "vc event at +window-1ms is still inside", must(edge2.Record(Event{t0 + 59_999, OfMinor(1, usd)})) == DenyCount)
}

func TestMoneyDiscipline(t *testing.T) {
	usd := USD
	sum := must(OfMinor(1999, usd).Plus(OfMinor(1, usd)))
	check(t, "Money plus/minus exact", must(sum.Minus(OfMinor(2000, usd))).MinorUnits == 0)
	eur, _ := NewCurrency("EUR", 2)
	_, err := OfMinor(1, usd).Plus(OfMinor(1, eur))
	check(t, "Money rejects mixed currencies", errors.Is(err, ErrCurrencyMismatch))
	_, err = OfMinor(math.MaxInt64, usd).Plus(OfMinor(1, usd))
	check(t, "Money plus reports overflow instead of wrapping", errors.Is(err, ErrOverflow))
	_, err = OfMinor(math.MinInt64, usd).Minus(OfMinor(1, usd))
	check(t, "Money minus reports overflow instead of wrapping", errors.Is(err, ErrOverflow))
}
