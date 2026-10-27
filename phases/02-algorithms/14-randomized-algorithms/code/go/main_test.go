// Tests for phases/02-algorithms/14-randomized-algorithms. Run: go test ./...
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

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func TestRng(t *testing.T) {
	check(t, "same seed, same stream", NewRng(42).NextLong() == NewRng(42).NextLong() && NewRng(42).NextLong() != NewRng(43).NextLong())
	check(t, "splitmix64 reference value for seed 0", uint64(NewRng(0).NextLong()) == 0xE220A8397B1DCDAF)
	check(t, "the reference value is negative as a signed 64-bit integer, like Java's long", NewRng(0).NextLong() < 0)
	r := NewRng(1)
	inRange := true
	var buckets [7]int
	for i := 0; i < 70_000; i++ {
		v := r.nextInt(7)
		inRange = inRange && v >= 0 && v < 7
		buckets[v]++
	}
	check(t, "nextInt in range", inRange)
	uniform := true
	for _, b := range buckets {
		uniform = uniform && abs(b-10_000) < 500
	}
	check(t, "nextInt(7) roughly uniform (each bucket within 5% of 10,000)", uniform)
	check(t, "nextInt(1) is always 0", NewRng(9).nextInt(1) == 0 && NewRng(10).nextInt(1) == 0)
	_, err := NewRng(1).NextInt(0)
	check(t, "nextInt rejects zero bound", errors.Is(err, ErrBadBound))
	doubles := true
	for i := 0; i < 10_000; i++ {
		d := r.NextDouble()
		doubles = doubles && d >= 0.0 && d < 1.0
	}
	check(t, "nextDouble in [0,1)", doubles)
}

func sampleIds(rng *Rng) []string {
	s := must(NewReservoirSampler[Transaction](3, rng))
	for _, tx := range DayOfTransactions(NewRng(0), 500) {
		s.Offer(tx)
	}
	return ids(s.Sample())
}

func TestReservoir(t *testing.T) {
	picked := map[int]int{}
	rr := NewRng(123)
	trials := 30_000
	for trial := 0; trial < trials; trial++ {
		s := must(NewReservoirSampler[int](3, rr))
		for item := 1; item <= 10; item++ {
			s.Offer(item)
		}
		for _, v := range s.Sample() {
			picked[v]++
		}
	}
	// each of 10 items should be in the 3-slot reservoir with probability 0.3 -> 9,000 of 30,000, +-5%
	within := len(picked) == 10
	for _, c := range picked {
		within = within && abs(c-9_000) < 450
	}
	check(t, "reservoir: every item selected with probability k/n", within)
	check(t, "reservoir: the last item is not favoured", abs(picked[10]-picked[1]) < 600)
	small := must(NewReservoirSampler[string](5, NewRng(1)))
	for _, s := range []string{"a", "b", "c"} {
		small.Offer(s)
	}
	check(t, "reservoir: fewer items than k keeps them all in order", slices.Equal(small.Sample(), []string{"a", "b", "c"}) && small.Seen() == 3)
	exact := must(NewReservoirSampler[int](4, NewRng(1)))
	for i := 0; i < 1000; i++ {
		exact.Offer(i)
	}
	check(t, "reservoir: size never exceeds k", len(exact.Sample()) == 4 && exact.Seen() == 1000)
	members := true
	seen := map[int]bool{}
	for _, v := range exact.Sample() {
		members = members && v >= 0 && v < 1000
		seen[v] = true
	}
	check(t, "reservoir: sampled items are distinct stream members", len(seen) == 4 && members)
	check(t, "reservoir: deterministic under a seed", slices.Equal(sampleIds(NewRng(77)), sampleIds(NewRng(77))))
	_, err := NewReservoirSampler[int](0, NewRng(1))
	check(t, "reservoir rejects k = 0", errors.Is(err, ErrBadK))
	check(t, "reservoir: demo sample matches the Java run", func() bool {
		u := must(NewReservoirSampler[Transaction](5, NewRng(1)))
		for _, tx := range DayOfTransactions(NewRng(2026), 10_000) {
			u.Offer(tx)
		}
		return slices.Equal(ids(u.Sample()), []string{"txn-08296", "txn-04614", "txn-02380", "txn-08666", "txn-09728"})
	}())
}

func TestWeightedReservoir(t *testing.T) {
	wpicked := map[string]int{}
	wr := NewRng(5)
	for trial := 0; trial < 20_000; trial++ {
		w := must(NewWeightedReservoirSampler[string](1, wr))
		must(0, w.Offer("small", 1))
		must(0, w.Offer("large", 9))
		wpicked[w.Sample()[0]]++
	}
	check(t, "weighted: 9:1 weights give ~90% large (within 2%)", abs(wpicked["large"]-18_000) < 400)
	err := must(NewWeightedReservoirSampler[string](1, NewRng(1))).Offer("x", 0)
	check(t, "weighted rejects zero weight", errors.Is(err, ErrBadWeight))
	// the heap's array order is the output order; the JDK transcription keeps it equal to the Java demo
	weighted := must(NewWeightedReservoirSampler[Transaction](5, NewRng(1)))
	for _, tx := range DayOfTransactions(NewRng(2026), 10_000) {
		must(0, weighted.Offer(tx, tx.Amount.MinorUnits))
	}
	check(t, "weighted: demo sample order matches the Java run", slices.Equal(ids(weighted.Sample()),
		[]string{"txn-09397", "txn-05887", "txn-01301", "txn-01590", "txn-09896"}))
}

func TestShuffles(t *testing.T) {
	fy := PermutationCounts(60_000, NewRng(7), true)
	nv := PermutationCounts(60_000, NewRng(7), false)
	allClose := true
	for _, c := range fy {
		allClose = allClose && abs(c-10_000) < 400
	}
	check(t, "fisher-yates: all 6 permutations within 4% of 10,000", allClose)
	check(t, "naive: acb, bac, bca near 5/27 (11,111)", abs(nv[1]-11_111) < 400 && abs(nv[2]-11_111) < 400 && abs(nv[3]-11_111) < 400)
	check(t, "naive: abc, cab, cba near 4/27 (8,889)", abs(nv[0]-8_889) < 400 && abs(nv[4]-8_889) < 400 && abs(nv[5]-8_889) < 400)
	check(t, "shuffle counts match the Java demo", fy == [6]int{10056, 9904, 9940, 9966, 10031, 10103} && nv == [6]int{8786, 11268, 10959, 11043, 8864, 9080})
	perm := []int{1, 2, 3, 4, 5, 6, 7, 8}
	FisherYates(perm, NewRng(3))
	sortedPerm := slices.Clone(perm)
	slices.Sort(sortedPerm)
	check(t, "fisher-yates is a permutation", slices.Equal(sortedPerm, []int{1, 2, 3, 4, 5, 6, 7, 8}))
	one := []int{42}
	FisherYates(one, NewRng(1))
	check(t, "fisher-yates on one element", slices.Equal(one, []int{42}))
	FisherYates([]int{}, NewRng(1))
	check(t, "fisher-yates on empty slice does nothing", true)
}

func TestQuickselect(t *testing.T) {
	data := []int64{9, 1, 8, 2, 7, 3, 6, 4, 5, 0}
	allRanks := true
	for k := range data {
		allRanks = allRanks && must(Select(slices.Clone(data), k, NewRng(int64(k)))) == int64(k)
	}
	check(t, "quickselect: every rank of 0..9", allRanks)
	check(t, "quickselect: duplicates", must(Select([]int64{5, 5, 5, 1, 5}, 0, NewRng(1))) == 1 && must(Select([]int64{5, 5, 5, 1, 5}, 3, NewRng(1))) == 5)
	check(t, "quickselect: single element", must(Select([]int64{7}, 0, NewRng(1))) == 7)
	check(t, "quickselect: already sorted and reversed", must(Select([]int64{1, 2, 3, 4, 5}, 4, NewRng(1))) == 5 && must(Select([]int64{5, 4, 3, 2, 1}, 0, NewRng(1))) == 1)
	_, err := Select([]int64{1, 2}, 2, NewRng(1))
	check(t, "quickselect rejects k out of range", errors.Is(err, ErrKRange))
	_, err = Select(nil, 0, NewRng(1))
	check(t, "quickselect rejects empty", errors.Is(err, ErrEmpty))
	amounts := make([]int64, 5000)
	ar := NewRng(99)
	for i := range amounts {
		amounts[i] = int64(ar.nextInt(1_000_000))
	}
	sorted := slices.Clone(amounts)
	slices.Sort(sorted)
	check(t, "quickselect median matches sort", must(Median(slices.Clone(amounts), NewRng(1))) == sorted[(len(sorted)-1)/2])
	check(t, "quickselect p99 matches sort", must(Percentile(slices.Clone(amounts), 0.99, NewRng(1))) == sorted[int(math.Ceil(0.99*float64(len(sorted))))-1])
	check(t, "quickselect p100 is the max", must(Percentile(slices.Clone(amounts), 1.0, NewRng(1))) == sorted[len(sorted)-1])
	_, err = Percentile(amounts, 0, NewRng(1))
	check(t, "quickselect rejects q = 0", errors.Is(err, ErrBadQ))
	seeds := true
	for seed := int64(0); seed < 20; seed++ {
		seeds = seeds && must(Select(slices.Clone(amounts), 1234, NewRng(seed))) == sorted[1234]
	}
	check(t, "quickselect: same answer under 20 different seeds", seeds)
}

func TestMillerRabin(t *testing.T) {
	prime := func(n int64, rounds int, seed int64) bool {
		t.Helper()
		return must(IsProbablePrime(n, rounds, NewRng(seed)))
	}
	check(t, "powMod", PowMod(2, 10, 1000) == 24 && PowMod(3, 0, 7) == 1)
	check(t, "primes are probable primes", prime(2, 5, 1) && prime(7919, 5, 1) && prime(2_147_483_647, 5, 1))
	check(t, "composites are rejected", !prime(1, 5, 1) && !prime(1_000_001, 5, 1) && !prime(7919*7927, 5, 1))
	check(t, "Carmichael number 561 is rejected", !prime(561, 10, 1))
	agrees := true
	for n := int64(2); n < 5000; n++ {
		agrees = agrees && prime(n, 8, n) == IsPrimeByTrialDivision(n)
	}
	check(t, "agrees with trial division on 2..4999 with 8 rounds", agrees)
	oneSided := true
	for n := int64(2); n < 5000; n++ {
		if IsPrimeByTrialDivision(n) {
			oneSided = oneSided && prime(n, 1, n)
		}
	}
	check(t, "a true prime is never declared composite, even with 1 round", oneSided)
	_, err := IsProbablePrime(1<<31, 1, NewRng(1))
	check(t, "rejects n >= 2^31", errors.Is(err, ErrTooLarge))
}

func TestMoneyString(t *testing.T) {
	check(t, "money String truncates toward zero like Java", OfMinor(-150, USD).String() == "-1.50 USD" && OfMinor(366356, USD).String() == "3663.56 USD")
}
