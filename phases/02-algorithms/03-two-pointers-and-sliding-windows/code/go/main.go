// Lesson: phases/02-algorithms/03-two-pointers-and-sliding-windows/docs/en.md
// Two Pointers and Sliding Windows
// Run:  go run .
// Test: go test ./...
// Sources: Cormen et al., Introduction to Algorithms (amortized analysis of two-index scans); the monotonic deque
// for sliding-window maximum follows the standard formulation (e.g. Tarjan, "Amortized Computational Complexity").
// Go 1.22+, standard library only. Same design as the Java and Python versions.
package main

import (
	"errors"
	"fmt"
	"math"
)

// Sentinel errors: Go reports what Java throws as IllegalArgumentException.
var (
	ErrBadWindow   = errors.New("k must be in 1..len(a)")
	ErrNegative    = errors.New("values must be non-negative for the window to be monotone")
	ErrOutOfOrder  = errors.New("events must arrive in time order")
	ErrBadVelocity = errors.New("window and count must be positive")
)

// Event is one card authorization attempt: when it happened (ms since epoch) and for how much. Events arrive in time order.
type Event struct {
	TimestampMs int64
	Amount      Money
}

// --- Two indices moving over one sorted slice ---

// PairWithSum finds indices (i, j), i < j, with a[i] + a[j] == target in a sorted slice. O(n). ok is false when none exists.
func PairWithSum(sorted []int64, target int64) (pair [2]int, ok bool) {
	i, j := 0, len(sorted)-1
	for i < j {
		sum := sorted[i] + sorted[j]
		if sum == target {
			return [2]int{i, j}, true
		}
		if sum < target {
			i++ // the smallest element can never pair with anything: drop it
		} else {
			j-- // the largest element can never pair with anything: drop it
		}
	}
	return [2]int{}, false
}

// DedupeSorted removes duplicates from a sorted slice in place; returns the new length. Reader and writer pointers.
func DedupeSorted(sorted []int64) int {
	if len(sorted) == 0 {
		return 0
	}
	write := 1
	for read := 1; read < len(sorted); read++ {
		if sorted[read] != sorted[write-1] {
			sorted[write] = sorted[read]
			write++
		}
	}
	return write
}

// MergeSorted merges two sorted slices into one sorted slice. The merge step from Lesson 01, standing alone.
func MergeSorted(a, b []int64) []int64 {
	out := make([]int64, 0, len(a)+len(b))
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		if a[i] <= b[j] {
			out = append(out, a[i])
			i++
		} else {
			out = append(out, b[j])
			j++
		}
	}
	out = append(out, a[i:]...)
	out = append(out, b[j:]...)
	return out
}

// --- Windows over a slice or an event stream. Each is O(n) total because both ends only move forward. ---

// MaxSumFixed: fixed width k, the maximum sum of any k consecutive elements. Errors if k is out of range.
func MaxSumFixed(a []int64, k int) (int64, error) {
	if k <= 0 || k > len(a) {
		return 0, fmt.Errorf("%w: got %d for len %d", ErrBadWindow, k, len(a))
	}
	var sum int64
	for i := 0; i < k; i++ {
		sum += a[i]
	}
	best := sum
	for right := k; right < len(a); right++ {
		sum += a[right] - a[right-k] // slide: add the new element, drop the one that fell off the left
		best = max(best, sum)
	}
	return best, nil
}

// ShortestRunWithSumAtLeast: variable width, the length of the shortest contiguous run with sum >= target; 0 if none.
// Non-negative values only.
func ShortestRunWithSumAtLeast(a []int64, target int64) (int, error) {
	for _, v := range a {
		if v < 0 {
			return 0, ErrNegative
		}
	}
	best, left := 0, 0
	var sum int64
	for right := 0; right < len(a); right++ {
		sum += a[right]
		for sum >= target && left <= right { // shrink from the left while the window still qualifies
			length := right - left + 1
			if best == 0 || length < best {
				best = length
			}
			sum -= a[left]
			left++
		}
	}
	return best, nil
}

// LongestWithoutRepeat: longest substring with no repeated character; the "at most one of each" window.
func LongestWithoutRepeat(s string) int {
	lastSeen := map[rune]int{}
	best, left, i := 0, 0, 0
	for _, c := range s { // Go strings are UTF-8: index code points, not bytes
		if prev, seen := lastSeen[c]; seen && prev >= left {
			left = prev + 1 // jump left past the previous copy of c
		}
		lastSeen[c] = i
		best = max(best, i-left+1)
		i++
	}
	return best
}

// MaxOfEachWindow is the sliding-window maximum with a monotonic deque: for every window of width k, the largest element.
// The deque holds indices whose values are strictly decreasing front to back; the front is always the max.
func MaxOfEachWindow(a []int64, k int) ([]int64, error) {
	if k <= 0 || k > len(a) {
		return nil, fmt.Errorf("%w: got %d for len %d", ErrBadWindow, k, len(a))
	}
	out := make([]int64, len(a)-k+1)
	dq := NewDeque[int]()
	for i := range a {
		for !dq.IsEmpty() && a[dq.PeekLast()] <= a[i] { // a[i] dominates everything smaller before it
			dq.PollLast()
		}
		dq.AddLast(i)
		if dq.PeekFirst() <= i-k { // the front fell out of the window
			dq.PollFirst()
		}
		if i >= k-1 {
			out[i-k+1] = a[dq.PeekFirst()]
		}
	}
	return out, nil
}

// Decision is the outcome of a velocity check.
type Decision int

// The three outcomes.
const (
	Allow Decision = iota
	DenyCount
	DenyAmount
)

func (d Decision) String() string {
	switch d {
	case Allow:
		return "ALLOW"
	case DenyCount:
		return "DENY_COUNT"
	default:
		return "DENY_AMOUNT"
	}
}

// VelocityChecker enforces "no more than maxCount authorizations and no more than maxTotal in any trailing window of
// windowMs". Events are fed in timestamp order; the window is the last windowMs of events, inclusive of the newest.
// This is the shape of every card-testing and account-takeover rule in a fraud engine.
type VelocityChecker struct {
	windowMs      int64
	maxCount      int
	maxTotal      Money
	window        *Deque[Event]
	total         Money
	lastTimestamp int64
}

// NewVelocityChecker validates the rule parameters.
func NewVelocityChecker(windowMs int64, maxCount int, maxTotal Money) (*VelocityChecker, error) {
	if windowMs <= 0 || maxCount <= 0 {
		return nil, ErrBadVelocity
	}
	return &VelocityChecker{
		windowMs: windowMs, maxCount: maxCount, maxTotal: maxTotal,
		window: NewDeque[Event](), total: OfMinor(0, maxTotal.Currency), lastTimestamp: math.MinInt64,
	}, nil
}

// Record stores the event and decides. Denied events still count: an attacker's failed attempts are the signal.
func (vc *VelocityChecker) Record(e Event) (Decision, error) {
	if e.TimestampMs < vc.lastTimestamp {
		return 0, ErrOutOfOrder
	}
	vc.lastTimestamp = e.TimestampMs
	if err := vc.evictOlderThan(e.TimestampMs - vc.windowMs); err != nil {
		return 0, err
	}
	vc.window.AddLast(e)
	total, err := vc.total.Plus(e.Amount)
	if err != nil {
		return 0, err
	}
	vc.total = total
	if vc.window.Len() > vc.maxCount {
		return DenyCount, nil
	}
	if vc.total.Compare(vc.maxTotal) > 0 {
		return DenyAmount, nil
	}
	return Allow, nil
}

func (vc *VelocityChecker) evictOlderThan(cutoffExclusive int64) error {
	for !vc.window.IsEmpty() && vc.window.PeekFirst().TimestampMs <= cutoffExclusive {
		total, err := vc.total.Minus(vc.window.PollFirst().Amount)
		if err != nil {
			return err
		}
		vc.total = total
	}
	return nil
}

// CountInWindow is the number of events currently inside the trailing window.
func (vc *VelocityChecker) CountInWindow() int { return vc.window.Len() }

// TotalInWindow is the sum of amounts currently inside the trailing window.
func (vc *VelocityChecker) TotalInWindow() Money { return vc.total }

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func main() {
	usd := USD

	fmt.Println("== Two pointers on a sorted array ==")
	amounts := []int64{75, 500, 500, 1999, 1999, 2575, 4500, 12000}
	pair, _ := PairWithSum(amounts, 4574)
	fmt.Printf("pairWithSum(4574) -> indices %v = %d + %d\n", pair, amounts[pair[0]], amounts[pair[1]])
	_, ok := PairWithSum(amounts, 1)
	fmt.Printf("pairWithSum(1)    -> found=%t\n", ok)
	cp := append([]int64(nil), amounts...)
	n := DedupeSorted(cp)
	fmt.Println("dedupeSorted      ->", cp[:n])
	fmt.Println("mergeSorted       ->", MergeSorted([]int64{1, 4, 7}, []int64{2, 3, 9}))

	fmt.Println()
	fmt.Println("== Fixed window: best 3-day settlement total (cents per day) ==")
	daily := []int64{12000, 4500, 80000, 2575, 30000, 1999, 500, 75}
	fmt.Println("days:", daily)
	fmt.Printf("maxSumFixed(k=3) = %d (days 2..4: 80000 + 2575 + 30000)\n", must(MaxSumFixed(daily, 3)))

	fmt.Println()
	fmt.Println("== Variable window: shortest run of days totalling at least 100,000 cents ==")
	fmt.Printf("shortestRunWithSumAtLeast(100000) = %d days\n", must(ShortestRunWithSumAtLeast(daily, 100_000)))
	fmt.Printf("shortestRunWithSumAtLeast(1000000) = %d (impossible -> 0)\n", must(ShortestRunWithSumAtLeast(daily, 1_000_000)))
	fmt.Printf("longestWithoutRepeat(\"abcabcbb\") = %d\n", LongestWithoutRepeat("abcabcbb"))

	fmt.Println()
	fmt.Println("== Sliding-window maximum with a monotonic deque, k = 3 ==")
	prices := []int64{1, 3, -1, -3, 5, 3, 6, 7}
	fmt.Println("input ", prices)
	fmt.Println("maxima", must(MaxOfEachWindow(prices, 3)))

	fmt.Println()
	fmt.Println("== Velocity check: max 4 authorizations and 500.00 USD per trailing 60 s ==")
	vc := must(NewVelocityChecker(60_000, 4, OfMinor(50_000, usd)))
	t0 := int64(1_700_000_000_000)
	script := [][2]int64{
		{t0 + 0, 12_000}, {t0 + 10_000, 4_500}, {t0 + 20_000, 25_000}, {t0 + 25_000, 1_999},
		{t0 + 30_000, 100}, {t0 + 70_000, 30_000}, {t0 + 125_000, 75},
	}
	for _, row := range script {
		e := Event{row[0], OfMinor(row[1], usd)}
		d := must(vc.Record(e))
		fmt.Printf("  t=+%3ds  %-10s -> %-12s window: %d events, %s\n",
			(e.TimestampMs-t0)/1000, e.Amount, d, vc.CountInWindow(), vc.TotalInWindow())
	}
}
