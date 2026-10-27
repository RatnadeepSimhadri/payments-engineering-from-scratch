// Lesson: phases/02-algorithms/02-binary-search-and-its-variants/docs/en.md
// Binary Search and Its Variants
// Run:  go run .
// Test: go test ./...
// Sources: Bentley, "Programming Pearls" ch. 4 (binary search correctness); Bloch, "Extra, Extra - Read All
// About It: Nearly All Binary Searches and Mergesorts are Broken" (2006, overflow-safe midpoint).
// Go 1.22+, standard library only. Same design as the Java and Python versions.
package main

import (
	"errors"
	"fmt"
	"math"
	"strings"
)

// Sentinel errors: Go reports what Java throws.
var (
	ErrNegative         = errors.New("negative")
	ErrEmpty            = errors.New("empty array")
	ErrNoItems          = errors.New("no items")
	ErrCurrencyMismatch = errors.New("currency mismatch")
)

// Every search below is the same loop: maintain an invariant over a half-open range [lo, hi),
// shrink it by half per step, stop when it is empty. The variants differ only in the predicate.

// IndexOf is the classic search: index of `target` in a sorted slice, or -(insertionPoint + 1) if absent (the JDK convention).
func IndexOf(a []int64, target int64) int {
	lo, hi := 0, len(a)-1 // closed range [lo, hi]
	for lo <= hi {
		mid := lo + (hi-lo)/2 // never overflows; (lo + hi) / 2 does near the top of the int range
		if a[mid] < target {
			lo = mid + 1
		} else if a[mid] > target {
			hi = mid - 1
		} else {
			return mid
		}
	}
	return -(lo + 1)
}

// FirstTrue is the one primitive everything else is built from: the smallest index i in [lo, hi) such that
// predicate(i) is true, or hi if none. Requires the predicate to be monotone: false...false true...true.
func FirstTrue(lo, hi int, predicate func(int) bool) int {
	for lo < hi {
		mid := lo + (hi-lo)/2
		if predicate(mid) {
			hi = mid // mid is a candidate: keep it in the range
		} else {
			lo = mid + 1 // mid is false: the answer is strictly to the right
		}
	}
	return lo // invariant: everything < lo is false, everything >= hi is true
}

// FirstTrueLong is the same primitive over a range of int64 values rather than indices (for search over an answer space).
func FirstTrueLong(lo, hi int64, predicate func(int64) bool) int64 {
	for lo < hi {
		mid := lo + (hi-lo)/2
		if predicate(mid) {
			hi = mid
		} else {
			lo = mid + 1
		}
	}
	return lo
}

// LowerBound is the first index whose value is >= target: the insertion point before any existing equal keys.
func LowerBound(a []int64, target int64) int {
	return FirstTrue(0, len(a), func(i int) bool { return a[i] >= target })
}

// UpperBound is the first index whose value is > target: one past the last equal key.
func UpperBound(a []int64, target int64) int {
	return FirstTrue(0, len(a), func(i int) bool { return a[i] > target })
}

// Count is the number of elements equal to target, in O(log n) via the two bounds.
func Count(a []int64, target int64) int { return UpperBound(a, target) - LowerBound(a, target) }

// CountInRange is the number of elements in the closed value range [low, high].
func CountInRange(a []int64, low, high int64) int { return UpperBound(a, high) - LowerBound(a, low) }

// RotationPoint is the index of the minimum in a slice that was sorted ascending, then rotated; distinct values required.
func RotationPoint(a []int64) (int, error) {
	if len(a) == 0 {
		return 0, ErrEmpty
	}
	last := a[len(a)-1]
	// predicate "a[i] <= last" is false on the rotated prefix (all larger than last) and true from the minimum on
	return FirstTrue(0, len(a), func(i int) bool { return a[i] <= last }), nil
}

// IndexOfRotated searches a rotated sorted slice without un-rotating it: locate the pivot, then search the correct half.
func IndexOfRotated(a []int64, target int64) int {
	if len(a) == 0 {
		return -1
	}
	pivot, _ := RotationPoint(a) // non-empty: cannot fail
	n := len(a)
	// the slice is sorted when read as a[(pivot + k) % n] for k in [0, n)
	k := FirstTrue(0, n, func(i int) bool { return a[(pivot+i)%n] >= target })
	if k == n || a[(pivot+k)%n] != target {
		return -1
	}
	return (pivot + k) % n
}

// IntegerSqrt is the largest x >= 0 with x * x <= n: search over the answer space, not over a slice.
func IntegerSqrt(n int64) (int64, error) {
	if n < 0 {
		return 0, ErrNegative
	}
	// first x such that x*x > n, minus one; the guard x > n / x avoids overflow of x*x
	first := FirstTrueLong(0, 3_037_000_500, func(x int64) bool { return x > 0 && x > n/x })
	return first - 1, nil
}

// TotalWithCap is a payments application of search over the answer space: the batch total when every line item is
// capped at capMinor. Monotone: raising the cap never lowers the total.
func TotalWithCap(items []Money, capMinor int64) (Money, error) {
	var total int64
	for _, m := range items {
		var err error
		if total, err = addChecked(total, min(m.MinorUnits, capMinor)); err != nil {
			return Money{}, err
		}
	}
	return OfMinor(total, items[0].Currency), nil
}

// LargestCapWithin is the largest cap (in minor units) such that TotalWithCap <= budget; -1 if even a cap of 0 exceeds the budget.
func LargestCapWithin(items []Money, budget Money) (int64, error) {
	if len(items) == 0 {
		return 0, ErrNoItems
	}
	var maxItem int64
	for _, m := range items {
		if m.Currency != budget.Currency {
			return 0, ErrCurrencyMismatch
		}
		maxItem = max(maxItem, m.MinorUnits)
	}
	// predicate: "a cap of c already exceeds the budget"; false for small c, true for large c
	var firstErr error
	firstOver := FirstTrueLong(0, maxItem+1, func(c int64) bool {
		total, err := TotalWithCap(items, c)
		if err != nil {
			firstErr = err
			return true
		}
		return total.Compare(budget) > 0
	})
	if firstErr != nil {
		return 0, firstErr
	}
	return firstOver - 1, nil
}

func fmtSlice(a []int64) string {
	parts := make([]string, len(a))
	for i, v := range a {
		parts[i] = fmt.Sprint(v)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func main() {
	fees := []int64{30, 30, 30, 50, 75, 75, 100, 250, 250, 250, 250, 500}
	fmt.Println("== Sorted fee schedule (cents): " + fmtSlice(fees) + " ==")
	fmt.Println("indexOf(75)       =", IndexOf(fees, 75), "  (some index of a 75; which one is unspecified)")
	fmt.Printf("indexOf(60)       = %d   (negative: insertion point is %d)\n", IndexOf(fees, 60), -IndexOf(fees, 60)-1)
	fmt.Println("lowerBound(250)   =", LowerBound(fees, 250), "  (first 250)")
	fmt.Println("upperBound(250)   =", UpperBound(fees, 250), " (one past the last 250)")
	fmt.Println("count(250)        =", Count(fees, 250))
	fmt.Println("count(60)         =", Count(fees, 60), "  (absent: lowerBound == upperBound)")
	fmt.Println("countInRange(30, 100) =", CountInRange(fees, 30, 100))

	fmt.Println()
	fmt.Println("== Overflow-safe midpoint ==")
	var lo, hi int32 = 1_500_000_000, 2_000_000_000 // int32 shows the wrap Java's int suffers; Go's int is 64-bit here
	fmt.Println("(lo + hi) / 2            =", (lo+hi)/2, "  (wrapped negative)")
	fmt.Println("lo + ((hi - lo) >>> 1)   =", lo+int32(uint32(hi-lo)>>1))

	fmt.Println()
	fmt.Println("== firstTrue over a monotone predicate ==")
	firstOver200 := FirstTrue(0, len(fees), func(i int) bool { return fees[i] > 200 })
	fmt.Printf("first index with fee > 200: %d (value %d)\n", firstOver200, fees[firstOver200])
	fmt.Println("none true -> returns hi:   ", FirstTrue(0, len(fees), func(i int) bool { return fees[i] > 10_000 }))

	fmt.Println()
	fmt.Println("== Rotated sorted array ==")
	rotated := []int64{40, 50, 60, 70, 10, 20, 30}
	fmt.Println("array           =", fmtSlice(rotated))
	fmt.Println("rotationPoint   =", must(RotationPoint(rotated)), "(index of the minimum)")
	fmt.Println("indexOfRotated(20) =", IndexOfRotated(rotated, 20))
	fmt.Println("indexOfRotated(65) =", IndexOfRotated(rotated, 65))

	fmt.Println()
	fmt.Println("== Search over the answer space ==")
	fmt.Println("integerSqrt(1_000_000_007) =", must(IntegerSqrt(1_000_000_007)))
	fmt.Println("integerSqrt(Long.MAX_VALUE) =", must(IntegerSqrt(math.MaxInt64)))
	usd := USD
	payouts := []Money{OfMinor(12_000, usd), OfMinor(4_500, usd), OfMinor(80_000, usd), OfMinor(2_575, usd), OfMinor(30_000, usd)}
	budget := OfMinor(60_000, usd)
	cap := must(LargestCapWithin(payouts, budget))
	fmt.Println("payouts: 120.00, 45.00, 800.00, 25.75, 300.00; budget 600.00 USD")
	fmt.Printf("largest per-payout cap within budget = %s -> total %s; one cent more -> %s\n",
		OfMinor(cap, usd), must(TotalWithCap(payouts, cap)), must(TotalWithCap(payouts, cap+1)))
}
