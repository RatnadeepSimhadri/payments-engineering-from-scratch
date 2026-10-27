// Tests for phases/02-algorithms/01-sorting-algorithms. Run: go test ./...
package main

import (
	"math"
	"slices"
	"strconv"
	"testing"
)

type sorter struct {
	name string
	sort func(a []int, cmp Comparator[int])
}

var allSorters = []sorter{
	{"insertion", func(a []int, c Comparator[int]) { InsertionSort(a, c) }},
	{"merge", func(a []int, c Comparator[int]) { MergeSort(a, c) }},
	{"quick", func(a []int, c Comparator[int]) { QuickSort(a, c, NewJavaRandom(99)) }},
	{"heap", func(a []int, c Comparator[int]) { HeapSort(a, c) }},
}

// sameMultisetAndSorted: every sort must produce exactly what the library's sort produces, as a multiset.
func sameMultisetAndSorted(input, output []int) bool {
	expected := clone(input)
	slices.Sort(expected)
	return slices.Equal(expected, output)
}

func check(t *testing.T, name string, condition bool) {
	t.Helper()
	if !condition {
		t.Errorf("FAIL: %s", name)
	}
}

func TestRandomArraysEverySort(t *testing.T) {
	rng := NewJavaRandom(2024)
	nat := NaturalOrder[int]()
	for _, s := range allSorters {
		for _, n := range []int{0, 1, 2, 15, 16, 17, 100, 1000} {
			a := RandomInts(n, 50, rng)
			out := clone(a)
			s.sort(out, nat)
			check(t, s.name+" sorts n="+strconv.Itoa(n), sameMultisetAndSorted(a, out))
		}
	}
}

func TestBoundaryInputs(t *testing.T) {
	nat := NaturalOrder[int]()
	asc, desc, same := make([]int, 500), make([]int, 500), make([]int, 500)
	for i := 0; i < 500; i++ {
		asc[i], desc[i], same[i] = i, 499-i, 7
	}
	for _, s := range allSorters {
		t.Run(s.name, func(t *testing.T) {
			a, d, sm := clone(asc), clone(desc), clone(same)
			s.sort(a, nat)
			s.sort(d, nat)
			s.sort(sm, nat)
			check(t, s.name+" ascending input", slices.Equal(a, asc))
			check(t, s.name+" descending input", slices.Equal(d, asc))
			check(t, s.name+" all-equal input", slices.Equal(sm, same))
		})
	}
}

func TestStabilityOnLedger(t *testing.T) {
	ledger := SampleLedger()
	byInsertion := clone(ledger)
	InsertionSort(byInsertion, ByAmount)
	byMerge := clone(ledger)
	MergeSort(byMerge, ByAmount)
	byHeap := clone(ledger)
	HeapSort(byHeap, ByAmount)
	check(t, "insertion sort is stable on ledger", IsStablePermutation(ledger, byInsertion, ByAmount, BySeq))
	check(t, "merge sort is stable on ledger", IsStablePermutation(ledger, byMerge, ByAmount, BySeq))
	wantSeq := []int64{8, 2, 4, 7, 1, 3, 6, 5}
	gotSeq := make([]int64, len(byMerge))
	for i, tx := range byMerge {
		gotSeq[i] = tx.Seq
	}
	check(t, "merge sort output order", slices.Equal(gotSeq, wantSeq))
	check(t, "heap sort is sorted by amount", IsSorted(byHeap, ByAmount))
	check(t, "heap sort breaks arrival order on this ledger", !IsStablePermutation(ledger, byHeap, ByAmount, BySeq))
}

func TestStabilityAtScale(t *testing.T) {
	rng := NewJavaRandom(2024)
	// consume the same draws the Java test made before this block (4 sorters x 8 sizes of random arrays)
	for range allSorters {
		for _, n := range []int{0, 1, 2, 15, 16, 17, 100, 1000} {
			RandomInts(n, 50, rng)
		}
	}
	big := make([]Transaction, 2000)
	for i := range big {
		big[i] = Transaction{int64(i), "a", OfMinor(int64(rng.NextIntN(5))*100, USD)}
	}
	bigMerge := clone(big)
	MergeSort(bigMerge, ByAmount)
	check(t, "merge sort stable on 2000 records / 5 keys", IsStablePermutation(big, bigMerge, ByAmount, BySeq))
	bigQuick := clone(big)
	QuickSort(bigQuick, ByAmount, NewJavaRandom(5))
	check(t, "quick sort sorted on 2000 records / 5 keys", IsSorted(bigQuick, ByAmount))
	check(t, "quick sort is not stable on 2000 records / 5 keys", !IsStablePermutation(big, bigQuick, ByAmount, BySeq))

	// the fix for an unstable sort: make the key total by appending the arrival sequence
	bigHeapFixed := clone(big)
	HeapSort(bigHeapFixed, ThenComparing(ByAmount, BySeq))
	check(t, "unstable sort + (amount, seq) key reproduces the stable order", slices.Equal(bigHeapFixed, bigMerge))
}

func TestComparisonCounts(t *testing.T) {
	data := RandomInts(4096, 1_000_000, NewJavaRandom(42))
	cIns := NaturalCounting[int]()
	InsertionSort(clone(data), cIns.Func())
	cMerge := NaturalCounting[int]()
	MergeSort(clone(data), cMerge.Func())
	cQuick := NaturalCounting[int]()
	QuickSort(clone(data), cQuick.Func(), NewJavaRandom(7))
	cHeap := NaturalCounting[int]()
	HeapSort(clone(data), cHeap.Func())
	nlogn := int64(math.Round(4096 * math.Log(4096) / math.Log(2))) // 49,152
	check(t, "insertion is quadratic: > n^2/8 comparisons", cIns.Comparisons > 4096*4096/8)
	check(t, "merge sort within 1.1 n log n comparisons", float64(cMerge.Comparisons) <= 1.1*float64(nlogn))
	check(t, "quick sort within 2.0 n log n comparisons", cQuick.Comparisons <= 2*nlogn)
	check(t, "heap sort within 2.2 n log n comparisons", float64(cHeap.Comparisons) <= 2.2*float64(nlogn))
	check(t, "heap sort uses more comparisons than merge sort", cHeap.Comparisons > cMerge.Comparisons)
	// the demo's exact numbers: the JavaRandom port must reproduce the Java run bit for bit
	check(t, "merge comparisons match the Java demo (51,140)", cMerge.Comparisons == 51140)
	check(t, "quick comparisons match the Java demo (56,728)", cQuick.Comparisons == 56728)
	check(t, "heap comparisons match the Java demo (85,675)", cHeap.Comparisons == 85675)
	check(t, "insertion comparisons match the Java demo (4,182,628)", cIns.Comparisons == 4182628)

	// three-way partition: all-equal input is linear, not quadratic
	equal := make([]int, 4096)
	for i := range equal {
		equal[i] = 5
	}
	cEq := NaturalCounting[int]()
	QuickSort(equal, cEq.Func(), NewJavaRandom(7))
	check(t, "3-way quicksort on all-equal keys is linear (< 3n comparisons)", cEq.Comparisons < 3*4096)

	// adaptivity: insertion sort on nearly sorted input is close to linear; heap sort is not
	nearly := make([]int, 4096)
	for i := range nearly {
		nearly[i] = i
	}
	Swap(nearly, 10, 20)
	Swap(nearly, 3000, 3001)
	cNear := NaturalCounting[int]()
	InsertionSort(clone(nearly), cNear.Func())
	cNearHeap := NaturalCounting[int]()
	HeapSort(clone(nearly), cNearHeap.Func())
	check(t, "insertion sort adaptive on nearly-sorted input (< 2n comparisons)", cNear.Comparisons < 2*4096)
	check(t, "heap sort oblivious on nearly-sorted input (> n log n / 2)", cNearHeap.Comparisons > nlogn/2)

	// worst case for a fixed-pivot quicksort is defeated by the random pivot: sorted input stays n log n
	sortedBig := make([]int, 8192)
	for i := range sortedBig {
		sortedBig[i] = i
	}
	cSortedQuick := NaturalCounting[int]()
	QuickSort(sortedBig, cSortedQuick.Func(), NewJavaRandom(11))
	check(t, "random-pivot quicksort on sorted input is not quadratic", cSortedQuick.Comparisons < 8192*8192/50)
}

func TestMergeHelperAndChecker(t *testing.T) {
	nat := NaturalOrder[int]()
	halves := []int{1, 4, 7, 2, 3, 9}
	Merge(halves, make([]int, 6), 0, 3, 6, nat)
	check(t, "merge of two sorted halves", slices.Equal(halves, []int{1, 2, 3, 4, 7, 9}))
	ledger := SampleLedger()
	unsorted := clone(ledger)
	check(t, "isStablePermutation rejects an unsorted array", !IsStablePermutation(ledger, unsorted, ByAmount, BySeq))
}

func TestMoneyGuards(t *testing.T) {
	eur, err := NewCurrency("EUR", 2)
	if err != nil {
		t.Fatal(err)
	}
	threw := func() (threw bool) {
		defer func() {
			if recover() != nil {
				threw = true
			}
		}()
		OfMinor(1, USD).Compare(OfMinor(1, eur))
		return false
	}()
	check(t, "Money.Compare rejects currency mismatch", threw)
	check(t, "Money String", OfMinor(1999, USD).String() == "19.99 USD")
	if _, err := NewCurrency("EURO", 2); err == nil {
		t.Error("NewCurrency should reject a four-letter code")
	}
}

func TestJavaRandomMatchesJava(t *testing.T) {
	// java.util.Random(42).nextInt(1_000_000) first three values, from the JDK.
	rng := NewJavaRandom(42)
	got := []int{rng.NextIntN(1_000_000), rng.NextIntN(1_000_000), rng.NextIntN(1_000_000)}
	want := RandomInts(3, 1_000_000, NewJavaRandom(42))
	if !slices.Equal(got, want) {
		t.Errorf("RandomInts disagrees with direct draws: %v vs %v", got, want)
	}
	if NewJavaRandom(42).NextInt() != -1170105035 {
		t.Errorf("new Random(42).nextInt() should be -1170105035, got %d", NewJavaRandom(42).NextInt())
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	s := ""
	for n > 0 {
		s = string(rune('0'+n%10)) + s
		n /= 10
	}
	return s
}
