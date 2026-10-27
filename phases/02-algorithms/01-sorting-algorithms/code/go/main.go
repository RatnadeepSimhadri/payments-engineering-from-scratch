// Lesson: phases/02-algorithms/01-sorting-algorithms/docs/en.md
// Sorting: Merge, Quick, Heap, and Why Libraries Use Timsort
// Run:  go run .
// Test: go test ./...
// Sources: Knuth, TAOCP Vol. 3 (sorting stability, heapsort, quicksort analysis); Bentley & McIlroy,
// "Engineering a Sort Function" (1993, three-way partition); Peters, "Timsort" (CPython listsort.txt).
// Go 1.22+, standard library only. Same design as the Java and Python versions.
package main

import (
	"cmp"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Comparator orders two values: negative when a < b, zero when equal, positive when a > b.
type Comparator[T any] func(a, b T) int

// NaturalOrder is Comparator.naturalOrder() for ordered types.
func NaturalOrder[T cmp.Ordered]() Comparator[T] {
	return func(a, b T) int { return cmp.Compare(a, b) }
}

// ThenComparing is Comparator.thenComparing: break ties of `first` with `second`.
func ThenComparing[T any](first, second Comparator[T]) Comparator[T] {
	return func(a, b T) int {
		if c := first(a, b); c != 0 {
			return c
		}
		return second(a, b)
	}
}

// Currency is an ISO 4217 currency: a three-letter code and the number of decimal places in its minor unit.
type Currency struct {
	Code     string
	Exponent int
}

var USD = Currency{"USD", 2}

// NewCurrency validates the code and exponent; the package-level constants are known-good.
func NewCurrency(code string, exponent int) (Currency, error) {
	if len(code) != 3 {
		return Currency{}, fmt.Errorf("currency code: %q", code)
	}
	if exponent < 0 || exponent > 4 {
		return Currency{}, fmt.Errorf("exponent must be 0..4: %d", exponent)
	}
	return Currency{code, exponent}, nil
}

// Money is an exact amount of one currency as a signed count of minor units (Phase 10, Lesson 01).
type Money struct {
	MinorUnits int64
	Currency   Currency
}

// OfMinor builds a Money from minor units.
func OfMinor(minorUnits int64, currency Currency) Money { return Money{minorUnits, currency} }

// Compare orders two amounts of the same currency; a currency mismatch is a programming error and panics.
func (m Money) Compare(other Money) int {
	if m.Currency != other.Currency {
		panic("cannot compare " + m.Currency.Code + " with " + other.Currency.Code)
	}
	return cmp.Compare(m.MinorUnits, other.MinorUnits)
}

func (m Money) String() string {
	scale := int64(1)
	for i := 0; i < m.Currency.Exponent; i++ {
		scale *= 10
	}
	abs := m.MinorUnits
	if abs < 0 {
		abs = -abs
	}
	whole, frac := abs/scale, abs%scale
	sign := ""
	if m.MinorUnits < 0 {
		sign = "-"
	}
	if m.Currency.Exponent == 0 {
		return fmt.Sprintf("%s%d %s", sign, whole, m.Currency.Code)
	}
	return fmt.Sprintf("%s%d.%0*d %s", sign, whole, m.Currency.Exponent, frac, m.Currency.Code)
}

// Transaction is a ledger line: the sequence number is the arrival order, which a stable sort by amount must preserve.
type Transaction struct {
	Seq     int64
	Account string
	Amount  Money
}

// ByAmount and BySeq are the two comparators the demo and tests use.
var (
	ByAmount Comparator[Transaction] = func(a, b Transaction) int { return a.Amount.Compare(b.Amount) }
	BySeq    Comparator[Transaction] = func(a, b Transaction) int { return cmp.Compare(a.Seq, b.Seq) }
)

func (t Transaction) String() string { return fmt.Sprintf("#%d %s %s", t.Seq, t.Account, t.Amount) }

// CountingComparator wraps a comparator and counts how many times it is consulted.
type CountingComparator[T any] struct {
	inner       Comparator[T]
	Comparisons int64
}

// NewCountingComparator wraps `inner`.
func NewCountingComparator[T any](inner Comparator[T]) *CountingComparator[T] {
	return &CountingComparator[T]{inner: inner}
}

// NaturalCounting is CountingComparator.natural().
func NaturalCounting[T cmp.Ordered]() *CountingComparator[T] {
	return NewCountingComparator(NaturalOrder[T]())
}

// Compare counts the call and delegates.
func (c *CountingComparator[T]) Compare(a, b T) int { c.Comparisons++; return c.inner(a, b) }

// Func exposes the counter as a Comparator the sorts accept.
func (c *CountingComparator[T]) Func() Comparator[T] { return c.Compare }

// InsertionCutoff is the subarray size below which merge and quick sort switch to insertion sort.
const InsertionCutoff = 16

// InsertionSort: O(n^2) worst and average, O(n) on nearly-sorted input, stable, in place. The workhorse for short runs.
func InsertionSort[T any](a []T, cmp Comparator[T]) { InsertionSortRange(a, 0, len(a), cmp) }

// InsertionSortRange sorts a[from:to] in place.
func InsertionSortRange[T any](a []T, from, to int, cmp Comparator[T]) {
	for i := from + 1; i < to; i++ {
		key := a[i]
		j := i - 1
		// strict > keeps equal keys in their original order: this is what makes it stable
		for j >= from && cmp(a[j], key) > 0 {
			a[j+1] = a[j]
			j--
		}
		a[j+1] = key
	}
}

// MergeSort: O(n log n) always, stable, O(n) extra memory. Top-down with an insertion-sort base case.
func MergeSort[T any](a []T, cmp Comparator[T]) {
	buffer := make([]T, len(a))
	mergeSort(a, buffer, 0, len(a), cmp)
}

func mergeSort[T any](a, buffer []T, from, to int, cmp Comparator[T]) {
	if to-from <= InsertionCutoff {
		InsertionSortRange(a, from, to, cmp)
		return
	}
	mid := from + (to-from)/2
	mergeSort(a, buffer, from, mid, cmp)
	mergeSort(a, buffer, mid, to, cmp)
	if cmp(a[mid-1], a[mid]) <= 0 { // halves already in order: skip the merge
		return
	}
	Merge(a, buffer, from, mid, to, cmp)
}

// Merge combines the sorted halves a[from:mid] and a[mid:to] through `buffer`.
func Merge[T any](a, buffer []T, from, mid, to int, cmp Comparator[T]) {
	copy(buffer[from:to], a[from:to])
	i, j, k := from, mid, from
	for i < mid && j < to {
		// <= takes from the left half on ties, so the earlier element stays earlier: stability
		if cmp(buffer[i], buffer[j]) <= 0 {
			a[k] = buffer[i]
			i++
		} else {
			a[k] = buffer[j]
			j++
		}
		k++
	}
	for i < mid {
		a[k] = buffer[i]
		i++
		k++
	}
	for j < to {
		a[k] = buffer[j]
		j++
		k++
	}
}

// QuickSort: O(n log n) expected, O(n^2) worst case against an adversary who knows the pivot sequence, not stable,
// O(log n) stack. Three-way (Dutch national flag) partition makes it O(n) on all-equal keys.
func QuickSort[T any](a []T, cmp Comparator[T], rng *JavaRandom) { quickSort(a, 0, len(a)-1, cmp, rng) }

func quickSort[T any](a []T, lo, hi int, cmp Comparator[T], rng *JavaRandom) {
	for hi-lo+1 > InsertionCutoff {
		Swap(a, lo, lo+rng.NextIntN(hi-lo+1)) // random pivot: no fixed input is a worst case
		pivot := a[lo]
		lt, gt, i := lo, hi, lo+1
		for i <= gt {
			c := cmp(a[i], pivot)
			if c < 0 {
				Swap(a, lt, i)
				lt++
				i++
			} else if c > 0 {
				Swap(a, i, gt)
				gt--
			} else {
				i++
			}
		}
		// a[lo..lt-1] < pivot, a[lt..gt] == pivot, a[gt+1..hi] > pivot
		// recurse on the smaller side, loop on the larger: stack depth stays O(log n)
		if lt-lo < hi-gt {
			quickSort(a, lo, lt-1, cmp, rng)
			lo = gt + 1
		} else {
			quickSort(a, gt+1, hi, cmp, rng)
			hi = lt - 1
		}
	}
	InsertionSortRange(a, lo, hi+1, cmp)
}

// HeapSort: O(n log n) always, in place, O(1) extra memory, not stable.
func HeapSort[T any](a []T, cmp Comparator[T]) {
	n := len(a)
	for i := n/2 - 1; i >= 0; i-- { // build max-heap bottom-up: O(n)
		siftDown(a, i, n, cmp)
	}
	for end := n - 1; end > 0; end-- {
		Swap(a, 0, end)          // largest to its final slot
		siftDown(a, 0, end, cmp) // restore the heap on the prefix
	}
}

func siftDown[T any](a []T, root, size int, cmp Comparator[T]) {
	for {
		left, right, largest := 2*root+1, 2*root+2, root
		if left < size && cmp(a[left], a[largest]) > 0 {
			largest = left
		}
		if right < size && cmp(a[right], a[largest]) > 0 {
			largest = right
		}
		if largest == root {
			return
		}
		Swap(a, root, largest)
		root = largest
	}
}

// Swap exchanges a[i] and a[j].
func Swap[T any](a []T, i, j int) { a[i], a[j] = a[j], a[i] }

// IsSorted is true when a[i] <= a[i+1] for every i.
func IsSorted[T any](a []T, cmp Comparator[T]) bool {
	for i := 1; i < len(a); i++ {
		if cmp(a[i-1], a[i]) > 0 {
			return false
		}
	}
	return true
}

// IsStablePermutation is true when every run of equal keys in `sorted` appears in the same relative order as in
// `original`. The `identity` comparator must distinguish the elements completely (for example by sequence number).
func IsStablePermutation[T any](original, sorted []T, key, identity Comparator[T]) bool {
	if len(original) != len(sorted) || !IsSorted(sorted, key) {
		return false
	}
	remaining := append([]T(nil), original...)
	for i := 1; i < len(sorted); i++ {
		if key(sorted[i-1], sorted[i]) == 0 {
			p, q := indexOf(remaining, sorted[i-1], identity), indexOf(remaining, sorted[i], identity)
			if p < 0 || q < 0 || p > q {
				return false
			}
		}
	}
	return true
}

func indexOf[T any](xs []T, x T, identity Comparator[T]) int {
	for i := range xs {
		if identity(xs[i], x) == 0 {
			return i
		}
	}
	return -1
}

// SampleLedger is the eight-line ledger the demo and tests sort.
func SampleLedger() []Transaction {
	usd := USD
	return []Transaction{
		{1, "acct-A", OfMinor(1999, usd)},
		{2, "acct-B", OfMinor(500, usd)},
		{3, "acct-C", OfMinor(1999, usd)},
		{4, "acct-A", OfMinor(500, usd)},
		{5, "acct-D", OfMinor(12000, usd)},
		{6, "acct-B", OfMinor(1999, usd)},
		{7, "acct-E", OfMinor(500, usd)},
		{8, "acct-C", OfMinor(75, usd)},
	}
}

// RandomInts draws n values in [0, bound) from the seeded generator.
func RandomInts(n, bound int, rng *JavaRandom) []int {
	a := make([]int, n)
	for i := range a {
		a[i] = rng.NextIntN(bound)
	}
	return a
}

// commas renders 1234567 as "1,234,567" (Java's %,d).
func commas(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	var b strings.Builder
	for i, ch := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(ch)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

func clone[T any](a []T) []T { return append([]T(nil), a...) }

func main() {
	fmt.Println("== Stable vs unstable: sort a ledger by amount, keep arrival order inside each amount ==")
	original := SampleLedger()
	stable := clone(original)
	MergeSort(stable, ByAmount)
	fmt.Println("mergeSort (stable):")
	for _, t := range stable {
		fmt.Println("  " + t.String())
	}
	unstable := clone(original)
	HeapSort(unstable, ByAmount)
	fmt.Println("heapSort (not stable):")
	for _, t := range unstable {
		fmt.Println("  " + t.String())
	}
	fmt.Println("merge preserves seq order within equal amounts:", IsStablePermutation(original, stable, ByAmount, BySeq))
	fmt.Println("heap preserves seq order within equal amounts: ", IsStablePermutation(original, unstable, ByAmount, BySeq))
	fixed := clone(original)
	HeapSort(fixed, ThenComparing(ByAmount, BySeq))
	fmt.Println("heapSort with a (amount, seq) tie-break is deterministic:", IsStablePermutation(original, fixed, ByAmount, BySeq))

	fmt.Println()
	fmt.Println("== Comparison counts, n = 4096 random ints, seed 42 ==")
	rng := NewJavaRandom(42)
	data := RandomInts(4096, 1_000_000, rng)
	for _, name := range []string{"insertion", "merge", "quick(3-way)", "heap"} {
		cp := clone(data)
		cc := NaturalCounting[int]()
		switch name {
		case "insertion":
			InsertionSort(cp, cc.Func())
		case "merge":
			MergeSort(cp, cc.Func())
		case "quick(3-way)":
			QuickSort(cp, cc.Func(), NewJavaRandom(7))
		default:
			HeapSort(cp, cc.Func())
		}
		n := cc.Comparisons
		fmt.Printf("  %-13s %10s comparisons  sorted=%t\n", name, commas(n), IsSorted(cp, cc.Func()))
	}
	fmt.Printf("  n log2 n       %10s\n", commas(int64(math.Round(4096*(math.Log(4096)/math.Log(2))))))

	fmt.Println()
	fmt.Println("== Duplicate-heavy input: 4096 values drawn from only 3 keys ==")
	dupes := RandomInts(4096, 3, NewJavaRandom(1))
	q3 := NaturalCounting[int]()
	QuickSort(clone(dupes), q3.Func(), NewJavaRandom(7))
	mg := NaturalCounting[int]()
	MergeSort(clone(dupes), mg.Func())
	fmt.Printf("  quick(3-way)  %8s comparisons (three-way partition finishes equal keys in one pass)\n", commas(q3.Comparisons))
	fmt.Printf("  merge         %8s comparisons\n", commas(mg.Comparisons))

	fmt.Println()
	fmt.Println("== Nearly sorted input: 4096 ascending ints with 8 adjacent swaps ==")
	nearly := make([]int, 4096)
	for i := range nearly {
		nearly[i] = i
	}
	swapper := NewJavaRandom(3)
	for k := 0; k < 8; k++ { // 8 adjacent swaps
		i := swapper.NextIntN(4095)
		Swap(nearly, i, i+1)
	}
	ins := NaturalCounting[int]()
	InsertionSort(clone(nearly), ins.Func())
	hp := NaturalCounting[int]()
	HeapSort(clone(nearly), hp.Func())
	fmt.Printf("  insertion     %8s comparisons (adaptive: close to n)\n", commas(ins.Comparisons))
	fmt.Printf("  heap          %8s comparisons (oblivious: always n log n)\n", commas(hp.Comparisons))
	fmt.Println()
	fmt.Println("Library rule of thumb: slices.SortStableFunc / sort.Stable are the stable calls (insertion + symmerge, O(1) buffer);")
	fmt.Println("slices.Sort / sort.Sort are pattern-defeating quicksort (unstable, in place): same choice Java makes for int[], for every type.")
}
