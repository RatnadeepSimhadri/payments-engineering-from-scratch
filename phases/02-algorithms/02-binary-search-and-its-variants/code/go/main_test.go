// Tests for phases/02-algorithms/02-binary-search-and-its-variants. Run: go test ./...
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

func linearLowerBound(a []int64, t int64) int {
	i := 0
	for i < len(a) && a[i] < t {
		i++
	}
	return i
}

func linearUpperBound(a []int64, t int64) int {
	i := 0
	for i < len(a) && a[i] <= t {
		i++
	}
	return i
}

var (
	fees  = []int64{30, 30, 30, 50, 75, 75, 100, 250, 250, 250, 250, 500}
	empty = []int64{}
	one   = []int64{42}
)

func TestIndexOf(t *testing.T) {
	check(t, "indexOf finds a present key", fees[IndexOf(fees, 75)] == 75)
	check(t, "indexOf unique key", IndexOf(fees, 100) == 6)
	check(t, "indexOf absent key encodes insertion point", IndexOf(fees, 60) == -(4+1))
	check(t, "indexOf below all", IndexOf(fees, 1) == -1)
	check(t, "indexOf above all", IndexOf(fees, 9999) == -(len(fees)+1))
	check(t, "indexOf empty", IndexOf(empty, 5) == -1)
	check(t, "indexOf singleton hit", IndexOf(one, 42) == 0)
	check(t, "indexOf singleton miss", IndexOf(one, 43) == -2)
	// slices.BinarySearch returns (insertionPoint, found) instead of the JDK's encoded negative
	pos, found := slices.BinarySearch(fees, 60)
	check(t, "indexOf agrees with slices.BinarySearch on absent keys", !found && IndexOf(fees, 60) == -(pos+1))
}

func TestBounds(t *testing.T) {
	rng := NewJavaRandom(7)
	allAgree := true
	for trial := 0; trial < 200; trial++ {
		n := rng.NextIntN(40)
		a := make([]int64, n)
		for i := range a {
			a[i] = int64(rng.NextIntN(20))
		}
		slices.Sort(a)
		for tv := int64(-1); tv <= 20; tv++ {
			if LowerBound(a, tv) != linearLowerBound(a, tv) {
				allAgree = false
			}
			if UpperBound(a, tv) != linearUpperBound(a, tv) {
				allAgree = false
			}
		}
	}
	check(t, "lowerBound/upperBound agree with linear scan on 200 random arrays", allAgree)
	check(t, "lowerBound first of run", LowerBound(fees, 250) == 7)
	check(t, "upperBound one past run", UpperBound(fees, 250) == 11)
	check(t, "lowerBound absent equals upperBound", LowerBound(fees, 60) == 4 && UpperBound(fees, 60) == 4)
	check(t, "lowerBound above all is length", LowerBound(fees, 10_000) == len(fees))
	check(t, "upperBound below all is 0", UpperBound(fees, 0) == 0)
	check(t, "count of a run", Count(fees, 250) == 4)
	check(t, "count of absent", Count(fees, 60) == 0)
	check(t, "countInRange inclusive", CountInRange(fees, 30, 100) == 7)
	check(t, "countInRange empty interval", CountInRange(fees, 51, 74) == 0)
}

func TestFirstTrue(t *testing.T) {
	check(t, "firstTrue finds boundary", FirstTrue(0, len(fees), func(i int) bool { return fees[i] > 200 }) == 7)
	check(t, "firstTrue none true returns hi", FirstTrue(0, len(fees), func(int) bool { return false }) == len(fees))
	check(t, "firstTrue all true returns lo", FirstTrue(0, len(fees), func(int) bool { return true }) == 0)
	check(t, "firstTrue empty range returns lo", FirstTrue(5, 5, func(int) bool { return true }) == 5)
	check(t, "firstTrueLong range", FirstTrueLong(0, 10_000_000, func(x int64) bool { return x*x >= 1_000_000_000_000 }) == 1_000_000)

	// overflow-safe midpoint: a range whose lo + hi exceeds a 32-bit int
	calls := 0
	big := FirstTrue(1_500_000_000, 2_000_000_000, func(i int) bool { calls++; return i >= 1_999_999_999 })
	check(t, "firstTrue works on indices whose sum overflows int32", big == 1_999_999_999)
	check(t, "firstTrue takes about log2(range) steps", calls <= 30)
	// the same midpoint computed in int32 arithmetic, the way the demo shows the bug
	var lo, hi int32 = 1_500_000_000, 2_000_000_000
	check(t, "(lo + hi) / 2 wraps in int32 exactly as Java's int does", (lo+hi)/2 == -397483648)
	check(t, "lo + (hi - lo) >>> 1 does not wrap", lo+int32(uint32(hi-lo)>>1) == 1750000000)
}

func TestRotated(t *testing.T) {
	rotated := []int64{40, 50, 60, 70, 10, 20, 30}
	rp, err := RotationPoint(rotated)
	check(t, "rotationPoint", err == nil && rp == 4)
	rp0, _ := RotationPoint([]int64{1, 2, 3})
	check(t, "rotationPoint of unrotated array is 0", rp0 == 0)
	_, err = RotationPoint(empty)
	check(t, "rotationPoint of empty array is an error", errors.Is(err, ErrEmpty))
	check(t, "indexOfRotated hit in right part", IndexOfRotated(rotated, 20) == 5)
	check(t, "indexOfRotated hit in left part", IndexOfRotated(rotated, 60) == 2)
	check(t, "indexOfRotated miss", IndexOfRotated(rotated, 65) == -1)
	check(t, "indexOfRotated empty", IndexOfRotated(empty, 1) == -1)
	rotatedOk := true
	for shift := 0; shift < 10; shift++ {
		base := make([]int64, 10)
		for i := range base {
			base[i] = int64((i+shift)%10) * 3 // distinct, rotated by shift
		}
		for tv := int64(0); tv < 30; tv++ {
			idx := IndexOfRotated(base, tv)
			present := tv%3 == 0
			if present && (idx < 0 || base[idx] != tv) {
				rotatedOk = false
			}
			if !present && idx != -1 {
				rotatedOk = false
			}
		}
	}
	check(t, "indexOfRotated correct for every rotation of 10 elements", rotatedOk)
}

func sqrtOf(t *testing.T, n int64) int64 {
	t.Helper()
	v, err := IntegerSqrt(n)
	if err != nil {
		t.Fatalf("IntegerSqrt(%d): %v", n, err)
	}
	return v
}

func TestIntegerSqrt(t *testing.T) {
	check(t, "integerSqrt exact square", sqrtOf(t, 1_000_000) == 1000)
	check(t, "integerSqrt rounds down", sqrtOf(t, 999_999) == 999)
	check(t, "integerSqrt 0 and 1", sqrtOf(t, 0) == 0 && sqrtOf(t, 1) == 1)
	check(t, "integerSqrt MaxInt64 does not overflow", sqrtOf(t, math.MaxInt64) == 3_037_000_499)
	_, err := IntegerSqrt(-1)
	check(t, "integerSqrt rejects negatives", errors.Is(err, ErrNegative))
}

func TestCappedBatch(t *testing.T) {
	usd := USD
	payouts := []Money{OfMinor(12_000, usd), OfMinor(4_500, usd), OfMinor(80_000, usd), OfMinor(2_575, usd), OfMinor(30_000, usd)}
	capWithin := func(budget Money) int64 {
		t.Helper()
		c, err := LargestCapWithin(payouts, budget)
		if err != nil {
			t.Fatalf("LargestCapWithin: %v", err)
		}
		return c
	}
	total := func(c int64) int64 {
		t.Helper()
		m, err := TotalWithCap(payouts, c)
		if err != nil {
			t.Fatalf("TotalWithCap: %v", err)
		}
		return m.MinorUnits
	}
	cap := capWithin(OfMinor(60_000, usd))
	check(t, "cap keeps total within budget", total(cap) <= 60_000)
	check(t, "cap + 1 exceeds budget (maximality)", total(cap+1) > 60_000)
	check(t, "cap value: 190.75 fixed + 2 capped items, (600.00 - 190.75) / 2 rounded down", cap == 20_462)
	check(t, "budget larger than everything: cap is the largest item", capWithin(OfMinor(1_000_000, usd)) == 80_000)
	check(t, "zero budget: cap is 0", capWithin(OfMinor(0, usd)) == 0)
	check(t, "negative budget: -1 means infeasible", capWithin(OfMinor(-1, usd)) == -1)
	eur, _ := NewCurrency("EUR", 2)
	_, err := LargestCapWithin(payouts, OfMinor(1, eur))
	check(t, "capped batch rejects currency mismatch", errors.Is(err, ErrCurrencyMismatch))
	_, err = LargestCapWithin(nil, OfMinor(1, usd))
	check(t, "capped batch rejects an empty batch", errors.Is(err, ErrNoItems))
	_, err = TotalWithCap([]Money{OfMinor(math.MaxInt64, usd), OfMinor(1, usd)}, math.MaxInt64)
	check(t, "totalWithCap reports int64 overflow instead of wrapping", errors.Is(err, ErrOverflow))
}
