// Tests for phases/02-algorithms/04-recursion-and-backtracking. Run: go test ./...
package main

import (
	"errors"
	"fmt"
	"slices"
	"testing"
)

func check(t *testing.T, name string, condition bool) {
	t.Helper()
	if !condition {
		t.Errorf("FAIL: %s", name)
	}
}

func sum(ms []Money) int64 {
	var s int64
	for _, m := range ms {
		s += m.MinorUnits
	}
	return s
}

// distinct counts the distinct slices by their printed form, the role HashSet<List<T>> plays in the Java test.
func distinct[T any](xss [][]T) int {
	seen := map[string]bool{}
	for _, xs := range xss {
		seen[fmt.Sprint(xs)] = true
	}
	return len(seen)
}

func TestPermutations(t *testing.T) {
	perms := Permutations([]string{"A", "B", "C"})
	check(t, "3! permutations", len(perms) == 6)
	check(t, "permutations are distinct", distinct(perms) == 6)
	check(t, "first permutation is identity order", slices.Equal(perms[0], []string{"A", "B", "C"}))
	check(t, "last permutation is reverse", slices.Equal(perms[5], []string{"C", "B", "A"}))
	check(t, "permutations of empty list: one empty permutation", len(Permutations([]string{})) == 1)
	check(t, "5! = 120", len(Permutations([]int{1, 2, 3, 4, 5})) == 120)
}

func TestSubsets(t *testing.T) {
	subs := Subsets([]string{"A", "B", "C"})
	check(t, "2^3 subsets", len(subs) == 8)
	check(t, "subsets are distinct", distinct(subs) == 8)
	check(t, "empty set first", len(subs[0]) == 0)
	check(t, "full set last", slices.Equal(subs[7], []string{"A", "B", "C"}))
	ordered := true
	for _, s := range subs {
		for i := 1; i < len(s); i++ {
			if s[i-1] >= s[i] {
				ordered = false
			}
		}
	}
	check(t, "subsets preserve input order within each", ordered)
	check(t, "2^10 subsets", len(Subsets([]int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9})) == 1024)
	// the backtrack must not alias: appending to a shared slice would overwrite earlier subsets
	check(t, "subsets do not share backing arrays", slices.Equal(subs[1], []string{"C"}) && slices.Equal(subs[2], []string{"B"}))
}

func TestCombinations(t *testing.T) {
	check(t, "C(4,2) = 6", len(must(Combinations([]string{"A", "B", "C", "D"}, 2))) == 6)
	check(t, "C(5,0) = 1", len(must(Combinations([]int{1, 2, 3, 4, 5}, 0))) == 1)
	check(t, "C(5,5) = 1", len(must(Combinations([]int{1, 2, 3, 4, 5}, 5))) == 1)
	check(t, "C(10,3) = 120", len(must(Combinations([]int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}, 3))) == 120)
	_, err := Combinations([]int{1, 2}, 3)
	check(t, "combinations reject k > n", errors.Is(err, ErrKOutOfRange))
}

func TestSettlementAllocations(t *testing.T) {
	usd := USD
	receivables := []Money{OfMinor(4_500, usd), OfMinor(7_500, usd), OfMinor(2_000, usd),
		OfMinor(12_000, usd), OfMinor(5_500, usd), OfMinor(1_000, usd), OfMinor(3_000, usd)}
	target := OfMinor(12_000, usd)
	cNo, cYes, cIt := &Counter{}, &Counter{}, &Counter{}
	noPrune := must(Allocations(receivables, target, false, cNo))
	pruned := must(Allocations(receivables, target, true, cYes))
	iter := AllocationsIterative(receivables, target, cIt)
	allSum := true
	for _, a := range pruned {
		if sum(a) != 12_000 {
			allSum = false
		}
	}
	check(t, "every allocation sums to the target", allSum)
	check(t, "allocations are distinct", distinct(pruned) == len(pruned))
	check(t, "expected number of allocations", len(pruned) == 3)
	check(t, "pruning does not change the answer", equalAllocations(noPrune, pruned))
	check(t, "iterative version matches the recursive one, same order", equalAllocations(iter, pruned))
	check(t, "pruning visits fewer nodes", cYes.Visited < cNo.Visited)
	check(t, "unpruned search visits a node per subset prefix (2^(n+1) - 1 bound)", cNo.Visited <= (1<<8)-1)
	check(t, "iterative visits the same number of nodes as pruned recursion", cIt.Visited == cYes.Visited)
	check(t, "demo node counts: 113 unpruned, 21 pruned", cNo.Visited == 113 && cYes.Visited == 21)
	// the single-item allocation [120.00] must be among them
	check(t, "single receivable equal to target is an allocation",
		slices.ContainsFunc(pruned, func(a []Money) bool { return slices.Equal(a, []Money{OfMinor(12_000, usd)}) }))
	// an impossible target
	check(t, "no allocation for an unreachable target", len(must(Allocations(receivables, OfMinor(1, usd), true, &Counter{}))) == 0)
	zero := must(Allocations(receivables, OfMinor(0, usd), true, &Counter{}))
	check(t, "target zero has exactly the empty allocation", len(zero) == 1 && len(zero[0]) == 0)
	eur, _ := NewCurrency("EUR", 2)
	_, err := Allocations([]Money{OfMinor(1, eur)}, target, true, &Counter{})
	check(t, "rejects mixed currencies", errors.Is(err, ErrMixedCurrency))
	_, err = Allocations([]Money{OfMinor(0, usd)}, target, true, &Counter{})
	check(t, "rejects non-positive receivables", errors.Is(err, ErrNonPositive))

	// pruning at scale: same answers, far fewer nodes
	var many []Money
	for i := int64(1); i <= 20; i++ {
		many = append(many, OfMinor(100*i*(i%5+1), usd))
	}
	big1, big2 := &Counter{}, &Counter{}
	r1 := must(Allocations(many, OfMinor(30_000, usd), false, big1))
	r2 := must(Allocations(many, OfMinor(30_000, usd), true, big2))
	check(t, "20 receivables: pruned and unpruned agree", equalAllocations(r1, r2))
	check(t, "20 receivables: pruning cuts nodes by more than 10x", big2.Visited*10 < big1.Visited)
}

func TestNQueens(t *testing.T) {
	q4, q6, q8 := &Counter{}, &Counter{}, &Counter{}
	check(t, "4-queens has 2 solutions", CountSolutions(4, q4) == 2)
	check(t, "6-queens has 4 solutions", CountSolutions(6, q6) == 4)
	check(t, "8-queens has 92 solutions", CountSolutions(8, q8) == 92)
	check(t, "8-queens visits far fewer than 8^8 nodes", q8.Visited < 20_000)
	check(t, "demo node counts: 17, 153, 2057", q4.Visited == 17 && q6.Visited == 153 && q8.Visited == 2057)
	check(t, "1-queen has 1 solution", CountSolutions(1, &Counter{}) == 1)
	check(t, "3-queens has no solution", CountSolutions(3, &Counter{}) == 0)
}

func TestDepth(t *testing.T) {
	check(t, "recursive and iterative sums agree", SumRecursive(1_000) == SumIterative(1_000) && SumIterative(1_000) == 500_500)
	// Go cannot recover from a real stack overflow (it is fatal, not a panic), so the failure the lesson warns about
	// is reported through a depth budget instead. Nothing here depends on the exact depth the runtime allows.
	_, err := SumRecursiveBounded(50_000_000, 1_000_000)
	check(t, "deep recursion exhausts the depth budget; the failure the lesson warns about", errors.Is(err, ErrDepthBudget))
	v, err := SumRecursiveBounded(1_000, 2_000)
	check(t, "bounded sum within budget agrees with the plain one", err == nil && v == 500_500)
	check(t, "iterative sum handles the same depth", SumIterative(50_000_000) == 50_000_000*50_000_001/2)
	check(t, "the probe reaches a depth the JVM's fixed stack cannot", ProbeDepth(200_000) == 200_000)
	check(t, "the goroutine stack ceiling is finite and reported without being changed", MaxStackBytes() > 0 && MaxStackBytes() == MaxStackBytes())
}
