// Tests for phases/02-algorithms/07-greedy-algorithms. Run: go test ./...
package main

import (
	"errors"
	"maps"
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

func pairwiseDisjoint(xs []Interval) bool {
	for i := range xs {
		for j := i + 1; j < len(xs); j++ {
			a, b := xs[i], xs[j]
			if a.Start < b.End && b.Start < a.End {
				return false
			}
		}
	}
	return true
}

// bruteMaxNonOverlapping: the largest pairwise-disjoint subset, by enumerating all 2^n subsets (n <= 12).
func bruteMaxNonOverlapping(xs []Interval) int {
	best := 0
	for mask := 0; mask < 1<<len(xs); mask++ {
		var sub []Interval
		for i := range xs {
			if mask&(1<<i) != 0 {
				sub = append(sub, xs[i])
			}
		}
		if pairwiseDisjoint(sub) {
			best = max(best, len(sub))
		}
	}
	return best
}

// maxDepth: maximum number of intervals covering any single point: the lower bound on resources.
func maxDepth(xs []Interval) int {
	best := 0
	for _, a := range xs {
		d := 0
		for _, b := range xs {
			if b.Start <= a.Start && a.Start < b.End {
				d++
			}
		}
		best = max(best, d)
	}
	return best
}

func TestIntervalScheduling(t *testing.T) {
	windows := Windows()
	chosen := MaxNonOverlapping(windows)
	check(t, "earliest-end picks 3", len(chosen) == 3)
	check(t, "earliest-end picks are disjoint", pairwiseDisjoint(chosen))
	check(t, "earliest-end specific picks", slices.Equal(names(chosen), []string{"card-settle", "payout-run", "reporting"}))
	check(t, "earliest-end matches brute force", len(chosen) == bruteMaxNonOverlapping(windows))
	check(t, "empty input", len(MaxNonOverlapping(nil)) == 0)
	check(t, "touching intervals are compatible ([0,5) and [5,8))", len(MaxNonOverlapping([]Interval{iv("a", 0, 5), iv("b", 5, 8)})) == 2)
	_, err := NewInterval("bad", 5, 5)
	check(t, "interval with end <= start rejected", errors.Is(err, ErrBadInterval))

	// the wrong greedy fails on a crafted input: one short interval in the middle of two long compatible ones
	trap := []Interval{iv("long-1", 0, 10), iv("short", 9, 12), iv("long-2", 11, 20)}
	check(t, "shortest-first picks 1 on the trap", len(ShortestFirst(trap)) == 1)
	check(t, "earliest-end picks 2 on the trap", len(MaxNonOverlapping(trap)) == 2)

	// randomized oracle: greedy equals brute force on 300 random instances (same java.util.Random(21) draws as the Java test)
	rng := NewJavaRandom(21)
	oracle, resourcesOk := true, true
	for trial := 0; trial < 300; trial++ {
		n := rng.NextIntN(11)
		xs := make([]Interval, 0, n)
		for i := 0; i < n; i++ {
			s := int64(rng.NextIntN(30))
			xs = append(xs, iv("i"+string(rune('0'+i)), s, s+1+int64(rng.NextIntN(10))))
		}
		g := MaxNonOverlapping(xs)
		if !pairwiseDisjoint(g) || len(g) != bruteMaxNonOverlapping(xs) {
			oracle = false
		}
		if MinResources(xs) != maxDepth(xs) {
			resourcesOk = false
		}
	}
	check(t, "earliest-end equals brute-force optimum on 300 random instances", oracle)
	check(t, "minResources equals maximum overlap depth on 300 random instances", resourcesOk)
	check(t, "minResources on the windows (four overlap at t=3)", MinResources(windows) == 4)
	check(t, "minResources of disjoint intervals is 1", MinResources([]Interval{iv("a", 0, 1), iv("b", 1, 2)}) == 1)
	check(t, "minResources of empty is 0", MinResources(nil) == 0)
}

func TestHuffman(t *testing.T) {
	freq := ResponseCodeFrequencies()
	tree := must(Build(freq))
	codes := Codes(tree)
	check(t, "huffman: one code per symbol", len(codes) == 7)
	check(t, "huffman: prefix-free", IsPrefixFree(codes))
	check(t, "huffman: most frequent symbol gets a 1-bit code", len(codes[0]) == 1)
	check(t, "huffman: rarest symbols get the longest codes", len(codes[96]) >= len(codes[5]) && len(codes[91]) >= len(codes[5]))
	bits := EncodedBits(freq, codes)
	var total int64
	for _, f := range freq {
		total += f
	}
	check(t, "huffman beats fixed 3-bit code", bits < 3*total)
	check(t, "huffman exact bit count", bits == 1_320_000)
	check(t, "huffman: deterministic across builds", maps.Equal(Codes(must(Build(freq))), codes))
	check(t, "huffman: codes match the Java demo", codes[0] == "1" && codes[5] == "00" && codes[51] == "010" && codes[14] == "0110" && codes[96] == "011100")
	msg := []int{0, 0, 0, 5, 0, 51, 0, 0, 14, 0}
	check(t, "huffman round trip", slices.Equal(must(Decode(Encode(msg, codes), tree)), msg))
	_, err := Decode(codes[96][:2], tree)
	check(t, "huffman rejects truncated input", errors.Is(err, ErrTruncated))
	check(t, "huffman single-symbol alphabet gets a 1-bit code", Codes(must(Build(map[int]int64{7: 10})))[7] == "0")
	_, err = Build(map[int]int64{})
	check(t, "huffman rejects empty alphabet", errors.Is(err, ErrNoSymbols))
	_, err = Build(map[int]int64{1: 0})
	check(t, "huffman rejects zero frequency", errors.Is(err, ErrBadFrequency))
	// optimality on the CLRS example alphabet
	small := map[int]int64{1: 45, 2: 13, 3: 12, 4: 16, 5: 9, 6: 5}
	smallCodes := Codes(must(Build(small)))
	check(t, "huffman CLRS example: 224 bits", EncodedBits(small, smallCodes) == 224)
	// Kraft equality: a full binary tree has sum 2^-len == 1
	kraft := 0.0
	for _, c := range smallCodes {
		kraft += math.Pow(2, -float64(len(c)))
	}
	check(t, "huffman tree is full (Kraft sum == 1)", math.Abs(kraft-1.0) < 1e-12)
}

func TestChangeMaking(t *testing.T) {
	usd := []int64{1, 5, 10, 25, 100, 500, 1000, 2000, 5000, 10000}
	odd := []int64{1, 3, 4}
	oldUk := []int64{1, 3, 6, 12, 24, 30, 60, 240}
	greedyTotal := func(d []int64, amt int64) int64 {
		t.Helper()
		g, ok := Greedy(d, amt)
		if !ok {
			t.Fatalf("Greedy(%v, %d) should succeed", d, amt)
		}
		return Total(g)
	}
	check(t, "USD denominations are canonical", IsCanonical(usd))
	check(t, "{1,3,4} is not canonical", !IsCanonical(odd))
	check(t, "{1,3,4}: greedy on 6 uses 3 coins (4+1+1)", greedyTotal(odd, 6) == 3)
	check(t, "{1,3,4}: optimal on 6 uses 2 coins (3+3)", OptimalCount(odd, 6) == 2)
	check(t, "old UK pence not canonical", !IsCanonical(oldUk))
	check(t, "old UK pence: greedy on 48 uses 3, optimal 2", greedyTotal(oldUk, 48) == 3 && OptimalCount(oldUk, 48) == 2)
	g, _ := Greedy(usd, 18_741)
	check(t, "greedy USD 187.41 -> 1x100.00 1x50.00 1x20.00 1x10.00 1x5.00 2x1.00 1x0.25 1x0.10 1x0.05 1x0.01", Total(g) == 11)
	check(t, "greedy USD matches optimal on 187.41", Total(g) == OptimalCount(usd, 18_741))
	// replay java.util.Random(21) past the 300 interval instances so the 200 amounts are the Java test's amounts
	rng := NewJavaRandom(21)
	for trial := 0; trial < 300; trial++ {
		n := rng.NextIntN(11)
		for i := 0; i < n; i++ {
			rng.NextIntN(30)
			rng.NextIntN(10)
		}
	}
	usdOracle := true
	for trial := 0; trial < 200; trial++ {
		amt := int64(rng.NextIntN(5000))
		if greedyTotal(usd, amt) != OptimalCount(usd, amt) {
			usdOracle = false
		}
	}
	check(t, "greedy equals DP optimum on 200 random USD amounts", usdOracle)
	_, ok := Greedy([]int64{5, 10}, 7)
	check(t, "greedy reports ok=false when amount cannot be made", !ok)
	check(t, "optimalCount reports impossible as Impossible (MaxInt64)", OptimalCount([]int64{5, 10}, 7) == Impossible)
	check(t, "greedy on zero amount is zero coins", greedyTotal(usd, 0) == 0)
	check(t, "no 1-coin means not canonical", !IsCanonical([]int64{2, 5}))
	_, err := OptimalTable(usd, -1)
	check(t, "optimalTable rejects a negative amount", errors.Is(err, ErrNegative))
}
