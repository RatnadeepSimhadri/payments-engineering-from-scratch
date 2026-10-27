// Lesson: phases/02-algorithms/04-recursion-and-backtracking/docs/en.md
// Recursion and Backtracking
// Run:  go run .
// Test: go test ./...
// Sources: Knuth, TAOCP Vol. 4A, Section 7.2.1 (generating all n-tuples, permutations, combinations);
// Golomb & Baumert, "Backtrack Programming" (JACM 1965).
// Go 1.22+, standard library only. Same design as the Java and Python versions.
package main

import (
	"errors"
	"fmt"
	"math"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
)

// Sentinel errors: Go reports what Java throws.
var (
	ErrKOutOfRange    = errors.New("k out of range")
	ErrMixedCurrency  = errors.New("mixed currencies")
	ErrNonPositive    = errors.New("receivables must be positive")
	ErrDepthBudget    = errors.New("recursion depth budget exceeded")
	ErrPruningChanged = errors.New("pruning changed the answer")
)

// Counter counts how many nodes of the search tree were visited: the honest measure of pruning.
type Counter struct {
	Visited int64
}

// --- Exhaustive generation: every permutation and every subset, by recursion that undoes its own choices. ---

// Permutations returns all n! orderings of the elements, in lexicographic order of positions.
func Permutations[T any](items []T) [][]T {
	out := [][]T{}
	used := make([]bool, len(items))
	permute(items, used, []T{}, &out)
	return out
}

func permute[T any](items []T, used []bool, current []T, out *[][]T) {
	if len(current) == len(items) { // leaf: copy the path
		*out = append(*out, slices.Clone(current))
		return
	}
	for i := range items {
		if used[i] {
			continue
		}
		used[i] = true
		current = append(current, items[i]) // choose
		permute(items, used, current, out)  // explore
		current = current[:len(current)-1]  // un-choose: the backtrack step
		used[i] = false
	}
}

// Subsets returns all 2^n subsets, each as a slice preserving the input order; the empty set first.
func Subsets[T any](items []T) [][]T {
	out := [][]T{}
	subsetsFrom(items, 0, []T{}, &out)
	return out
}

func subsetsFrom[T any](items []T, index int, current []T, out *[][]T) {
	if index == len(items) {
		*out = append(*out, slices.Clone(current))
		return
	}
	subsetsFrom(items, index+1, current, out) // branch 1: leave items[index] out
	current = append(current, items[index])
	subsetsFrom(items, index+1, current, out) // branch 2: take it
}

// Combinations returns all C(n, k) combinations of size k, in lexicographic index order.
func Combinations[T any](items []T, k int) ([][]T, error) {
	if k < 0 || k > len(items) {
		return nil, fmt.Errorf("%w: %d", ErrKOutOfRange, k)
	}
	out := [][]T{}
	combine(items, k, 0, []T{}, &out)
	return out, nil
}

func combine[T any](items []T, k, start int, current []T, out *[][]T) {
	if len(current) == k {
		*out = append(*out, slices.Clone(current))
		return
	}
	// prune: if even taking every remaining item cannot reach k, stop
	for i := start; i <= len(items)-(k-len(current)); i++ {
		combine(items, k, i+1, append(current, items[i]), out)
	}
}

// --- The payments problem: a settlement of `target` must be covered by a subset of pending receivables. ---
// Enumerate every subset whose amounts sum exactly to the target (an exact-cover of the amount).
// Pruning: sort descending, stop a branch when the running sum exceeds the target, and stop when the
// remaining items cannot reach it. Still exponential in the worst case; that is the lesson.

func sortedWithSuffix(receivables []Money) ([]Money, []int64) {
	sorted := slices.Clone(receivables)
	slices.SortStableFunc(sorted, func(a, b Money) int { return -a.Compare(b) }) // largest first: prune earlier
	suffix := make([]int64, len(sorted)+1)                                       // suffix[i] = sum of sorted[i:]
	for i := len(sorted) - 1; i >= 0; i-- {
		suffix[i] = suffix[i+1] + sorted[i].MinorUnits
	}
	return sorted, suffix
}

// Allocations lists every subset of receivables that sums exactly to target, with or without pruning.
func Allocations(receivables []Money, target Money, prune bool, counter *Counter) ([][]Money, error) {
	for _, m := range receivables {
		if m.Currency != target.Currency {
			return nil, ErrMixedCurrency
		}
		if m.MinorUnits <= 0 {
			return nil, ErrNonPositive
		}
	}
	sorted, suffix := sortedWithSuffix(receivables)
	out := [][]Money{}
	search(sorted, suffix, 0, 0, target.MinorUnits, []Money{}, &out, prune, counter)
	return out, nil
}

func search(items []Money, suffix []int64, index int, sum, target int64, chosen []Money, out *[][]Money, prune bool, counter *Counter) {
	counter.Visited++
	if sum == target { // found one; nothing more can be added (positive amounts)
		*out = append(*out, slices.Clone(chosen))
		return
	}
	if index == len(items) {
		return
	}
	if prune {
		if sum > target { // overshot: no positive amount can fix it
			return
		}
		if sum+suffix[index] < target { // even taking everything left falls short
			return
		}
	}
	search(items, suffix, index+1, sum+items[index].MinorUnits, target, append(chosen, items[index]), out, prune, counter)
	search(items, suffix, index+1, sum, target, chosen, out, prune, counter)
}

// Frame is one suspended call of the recursive search, made explicit.
type Frame struct {
	Index  int
	Sum    int64
	Chosen []Money
}

// AllocationsIterative is the same search, no recursion: an explicit stack of frames. Same allocations, same order.
func AllocationsIterative(receivables []Money, target Money, counter *Counter) [][]Money {
	sorted, suffix := sortedWithSuffix(receivables)
	t := target.MinorUnits
	out := [][]Money{}
	stack := []Frame{{0, 0, []Money{}}}
	for len(stack) > 0 {
		f := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		counter.Visited++
		if f.Sum == t {
			out = append(out, f.Chosen)
			continue
		}
		if f.Index == len(sorted) || f.Sum > t || f.Sum+suffix[f.Index] < t {
			continue
		}
		// push the "skip" branch first so the "take" branch is popped (explored) first, matching the recursion's order
		stack = append(stack, Frame{f.Index + 1, f.Sum, f.Chosen})
		taken := append(slices.Clone(f.Chosen), sorted[f.Index])
		stack = append(stack, Frame{f.Index + 1, f.Sum + sorted[f.Index].MinorUnits, taken})
	}
	return out
}

// --- N-Queens: the classic constraint-satisfaction backtrack, with a visit counter to show what pruning buys. ---

// CountSolutions is the number of solutions on an n x n board.
func CountSolutions(n int, counter *Counter) int64 {
	return place(n, 0, make([]bool, n), make([]bool, 2*n), make([]bool, 2*n), counter)
}

func place(n, row int, cols, diag1, diag2 []bool, counter *Counter) int64 {
	counter.Visited++
	if row == n {
		return 1
	}
	var found int64
	for col := 0; col < n; col++ {
		d1, d2 := row+col, row-col+n
		if cols[col] || diag1[d1] || diag2[d2] { // constraint check before descending: the prune
			continue
		}
		cols[col], diag1[d1], diag2[d2] = true, true, true
		found += place(n, row+1, cols, diag1, diag2, counter)
		cols[col], diag1[d1], diag2[d2] = false, false, false
	}
	return found
}

// --- Recursion depth is a resource. In Go the goroutine stack grows on demand, so the limit is far away, ---
// --- and reaching it is a fatal error, not a recoverable one: a depth budget is the only honest guard.    ---

// SumRecursive is the textbook recursive sum 1 + 2 + ... + n.
func SumRecursive(n int64) int64 {
	if n == 0 {
		return 0
	}
	return n + SumRecursive(n-1)
}

// SumIterative is the same sum with a loop: constant stack.
func SumIterative(n int64) int64 {
	var s int64
	for i := int64(1); i <= n; i++ {
		s += i
	}
	return s
}

// SumRecursiveBounded is SumRecursive with a self-imposed depth budget. Go's runtime cannot recover from a real
// stack overflow (it is `fatal error: stack overflow`, which `recover` never sees), so production code that recurses
// over untrusted depth carries its own budget and returns ErrDepthBudget instead of taking the process down.
func SumRecursiveBounded(n, maxDepth int64) (int64, error) {
	if maxDepth <= 0 {
		return 0, ErrDepthBudget
	}
	if n == 0 {
		return 0, nil
	}
	rest, err := SumRecursiveBounded(n-1, maxDepth-1)
	return n + rest, err
}

// ProbeDepth recurses `limit` trivial frames deep and reports the depth reached. The JVM's probe stops where the
// fixed thread stack ends (about 10,000 frames); Go grows the goroutine stack until debug.SetMaxStack (1 GB on 64-bit).
func ProbeDepth(limit int) int {
	depth := 0
	probe(&depth, limit)
	return depth
}

func probe(depth *int, limit int) {
	*depth++
	if *depth < limit {
		probe(depth, limit)
	}
}

// MaxStackBytes reports the runtime's goroutine stack ceiling without changing it.
func MaxStackBytes() int {
	limit := debug.SetMaxStack(1 << 30)
	debug.SetMaxStack(limit)
	return limit
}

// jstr formats a slice as Java's List.toString does: [a, b, c].
func jstr[T any](xs []T) string {
	parts := make([]string, len(xs))
	for i, x := range xs {
		parts[i] = fmt.Sprint(x)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func jstr2[T any](xss [][]T) string {
	parts := make([]string, len(xss))
	for i, xs := range xss {
		parts[i] = jstr(xs)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// commas renders 1234567 as "1,234,567" (Java's %,d).
func commas(n int64) string {
	s := strconv.FormatInt(n, 10)
	var b strings.Builder
	for i, ch := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(ch)
	}
	return b.String()
}

// equalAllocations compares two allocation lists element by element.
func equalAllocations(a, b [][]Money) bool {
	return slices.EqualFunc(a, b, func(x, y []Money) bool { return slices.Equal(x, y) })
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func main() {
	usd := USD

	fmt.Println("== Permutations of [A, B, C] ==")
	for _, p := range Permutations([]string{"A", "B", "C"}) {
		fmt.Println("  " + jstr(p))
	}
	fmt.Println("== Subsets of [A, B, C] ==")
	fmt.Println("  " + jstr2(Subsets([]string{"A", "B", "C"})))
	fmt.Println("== Combinations C(4, 2) of [A, B, C, D] ==")
	fmt.Println("  " + jstr2(must(Combinations([]string{"A", "B", "C", "D"}, 2))))

	fmt.Println()
	fmt.Println("== Settlement allocation: which receivables exactly cover 120.00 USD? ==")
	receivables := []Money{OfMinor(4_500, usd), OfMinor(7_500, usd), OfMinor(2_000, usd),
		OfMinor(12_000, usd), OfMinor(5_500, usd), OfMinor(1_000, usd), OfMinor(3_000, usd)}
	target := OfMinor(12_000, usd)
	noPrune, withPrune, iterative := &Counter{}, &Counter{}, &Counter{}
	a1 := must(Allocations(receivables, target, false, noPrune))
	a2 := must(Allocations(receivables, target, true, withPrune))
	a3 := AllocationsIterative(receivables, target, iterative)
	fmt.Println("receivables: " + jstr(receivables))
	for _, alloc := range a2 {
		fmt.Println("  " + jstr(alloc))
	}
	fmt.Printf("%d allocations; nodes visited: no pruning %d, pruning %d, iterative %d; same results: %t\n",
		len(a2), noPrune.Visited, withPrune.Visited, iterative.Visited, equalAllocations(a1, a2) && equalAllocations(a2, a3))

	fmt.Println()
	fmt.Println("== Pruning at scale: 24 receivables, one target ==")
	var many []Money
	for i := int64(1); i <= 24; i++ { // 24 distinct amounts
		many = append(many, OfMinor(100*i*(i%5+1), usd))
	}
	big := OfMinor(50_000, usd)
	c1, c2 := &Counter{}, &Counter{}
	n1 := len(must(Allocations(many, big, false, c1)))
	n2 := len(must(Allocations(many, big, true, c2)))
	fmt.Printf("%d allocations found both ways; nodes visited without pruning: %s (2^25 - 1 = %s), with pruning: %s\n",
		n2, commas(c1.Visited), commas((1<<25)-1), commas(c2.Visited))
	if n1 != n2 {
		panic(ErrPruningChanged)
	}

	fmt.Println()
	fmt.Println("== N-Queens: constraint checks before descending ==")
	for _, n := range []int{4, 6, 8} {
		c := &Counter{}
		solutions := CountSolutions(n, c)
		fmt.Printf("  n=%d: %d solutions, %s nodes visited (of %s leaves in the unpruned tree)\n",
			n, solutions, commas(c.Visited), commas(int64(math.Pow(float64(n), float64(n)))))
	}

	fmt.Println()
	fmt.Println("== Recursion depth is finite ==")
	fmt.Printf("sumRecursive(1_000) = %d, sumIterative(10_000_000) = %d\n", SumRecursive(1_000), SumIterative(10_000_000))
	probed := ProbeDepth(1_000_000)
	fmt.Printf("the probe that overflows a JVM thread at about 10,000 trivial frames reached %s frames here: Go grows the goroutine stack on demand, up to debug.SetMaxStack = %s bytes\n",
		commas(int64(probed)), commas(int64(MaxStackBytes())))
	_, err := SumRecursiveBounded(2_000_000, 100_000)
	fmt.Printf("past that ceiling the runtime dies with an unrecoverable 'fatal error: stack overflow', so SumRecursiveBounded carries its own budget: %v\n", err)
	fmt.Println("a recursive sum over a day's 2,000,000 ledger lines would not survive a budget set for a request; the explicit-stack version does not care")
}
