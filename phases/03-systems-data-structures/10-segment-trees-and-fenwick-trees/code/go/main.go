// Lesson: phases/03-systems-data-structures/10-segment-trees-and-fenwick-trees/docs/en.md
// Segment Trees and Fenwick Trees
// Run:  go run .
// Test: go test ./...
// Sources: P. Fenwick, "A New Data Structure for Cumulative Frequency Tables" (Software: Practice and Experience, 1994);
// de Berg et al., "Computational Geometry", ch. 10 (segment trees). Go 1.22+, standard library only. Same design as the Java and Python versions.
package main

import (
	"errors"
	"fmt"
	"math"
	"math/bits"
	"strconv"
	"strings"
)

var (
	// ErrInvalidArgument is returned where the Java throws IllegalArgumentException.
	ErrInvalidArgument = errors.New("invalid argument")
	// ErrIndexOutOfBounds is returned where the Java throws IndexOutOfBoundsException (Objects.checkIndex).
	ErrIndexOutOfBounds = errors.New("index out of bounds")
	// ErrOverflow is returned where the Java's Math.addExact / multiplyExact throw ArithmeticException.
	ErrOverflow = errors.New("long overflow")
)

// ---------------------------------------------------------------- checked arithmetic ----

// addChecked is Math.addExact: int64 addition that reports overflow instead of wrapping.
func addChecked(a, b int64) (int64, error) {
	r := a + b
	if (a >= 0) == (b >= 0) && (r >= 0) != (a >= 0) {
		return 0, ErrOverflow
	}
	return r, nil
}

// subChecked is Math.subtractExact.
func subChecked(a, b int64) (int64, error) {
	r := a - b
	if (a >= 0) != (b >= 0) && (r >= 0) != (a >= 0) {
		return 0, ErrOverflow
	}
	return r, nil
}

// mulChecked is Math.multiplyExact.
func mulChecked(a, b int64) (int64, error) {
	if a == 0 || b == 0 {
		return 0, nil
	}
	r := a * b
	if r/b != a || (a == -1 && b == math.MinInt64) || (b == -1 && a == math.MinInt64) {
		return 0, ErrOverflow
	}
	return r, nil
}

// checkIndex is Objects.checkIndex(index, n).
func checkIndex(index, n int) error {
	if index < 0 || index >= n {
		return fmt.Errorf("%w: index %d out of bounds for length %d", ErrIndexOutOfBounds, index, n)
	}
	return nil
}

func checkRange(lo, hi, n int) error {
	if lo > hi {
		return fmt.Errorf("%w: lo > hi: %d > %d", ErrInvalidArgument, lo, hi)
	}
	if err := checkIndex(lo, n); err != nil {
		return err
	}
	return checkIndex(hi, n)
}

// ---------------------------------------------------------------- Currency and Money ----

// Currency is an ISO 4217 currency: code and minor-unit exponent. Copied from Phase 10, Lesson 01.
type Currency struct {
	Code     string
	Exponent int
}

var USD = Currency{"USD", 2}

func NewCurrency(code string, exponent int) (Currency, error) {
	if len(code) != 3 {
		return Currency{}, fmt.Errorf("%w: currency code must be three letters", ErrInvalidArgument)
	}
	if exponent < 0 || exponent > 4 {
		return Currency{}, fmt.Errorf("%w: exponent must be 0..4", ErrInvalidArgument)
	}
	return Currency{code, exponent}, nil
}

// Money is integer minor units plus a currency. The trees below hold raw minor units; Money is the display type.
type Money struct {
	MinorUnits int64
	Currency   Currency
}

func OfMinor(minorUnits int64, currency Currency) Money { return Money{minorUnits, currency} }

func (m Money) String() string {
	scale := int64(1)
	for i := 0; i < m.Currency.Exponent; i++ {
		scale *= 10
	}
	abs, sign := m.MinorUnits, ""
	if m.MinorUnits < 0 {
		abs, sign = -abs, "-"
	}
	whole := strconv.FormatInt(abs/scale, 10)
	if m.Currency.Exponent == 0 {
		return sign + whole + " " + m.Currency.Code
	}
	frac := strconv.FormatInt(abs%scale, 10)
	for len(frac) < m.Currency.Exponent {
		frac = "0" + frac
	}
	return sign + whole + "." + frac + " " + m.Currency.Code
}

// ---------------------------------------------------------------- FenwickTree ----

// FenwickTree (binary indexed tree) over n values: point add and prefix sum in O(log n), n+1 int64s of storage.
// Internally 1-indexed: tree[i] covers the (i & -i) values ending at position i. The public API is 0-indexed.
type FenwickTree struct {
	tree []int64 // tree[0] unused
	n    int
}

func NewFenwickTree(n int) (*FenwickTree, error) {
	if n <= 0 {
		return nil, fmt.Errorf("%w: size must be positive: %d", ErrInvalidArgument, n)
	}
	return &FenwickTree{tree: make([]int64, n+1), n: n}, nil
}

// BuildFenwick is the O(n) construction: each node pushes its total to its parent once.
func BuildFenwick(values []int64) (*FenwickTree, error) {
	f, err := NewFenwickTree(len(values))
	if err != nil {
		return nil, err
	}
	copy(f.tree[1:], values)
	for i := 1; i <= f.n; i++ {
		parent := i + (i & -i)
		if parent <= f.n {
			if f.tree[parent], err = addChecked(f.tree[parent], f.tree[i]); err != nil {
				return nil, err
			}
		}
	}
	return f, nil
}

func (f *FenwickTree) Size() int { return f.n }

// Add does values[index] += delta. Walks up: i += lowest set bit of i.
func (f *FenwickTree) Add(index int, delta int64) error {
	if err := checkIndex(index, f.n); err != nil {
		return err
	}
	for i := index + 1; i <= f.n; i += i & -i {
		var err error
		if f.tree[i], err = addChecked(f.tree[i], delta); err != nil {
			return err
		}
	}
	return nil
}

// PrefixSum is the sum of values[0..index] inclusive. Walks down: i -= lowest set bit of i. Returns 0 for index -1.
func (f *FenwickTree) PrefixSum(index int) (int64, error) {
	if index < -1 || index >= f.n {
		return 0, fmt.Errorf("%w: index %d for size %d", ErrIndexOutOfBounds, index, f.n)
	}
	sum := int64(0)
	for i := index + 1; i > 0; i -= i & -i {
		var err error
		if sum, err = addChecked(sum, f.tree[i]); err != nil {
			return 0, err
		}
	}
	return sum, nil
}

// RangeSum is the sum of values[lo..hi] inclusive, as the difference of two prefix sums.
func (f *FenwickTree) RangeSum(lo, hi int) (int64, error) {
	if err := checkRange(lo, hi, f.n); err != nil {
		return 0, err
	}
	high, err := f.PrefixSum(hi)
	if err != nil {
		return 0, err
	}
	low, err := f.PrefixSum(lo - 1)
	if err != nil {
		return 0, err
	}
	return subChecked(high, low)
}

// Get is the current value at one position.
func (f *FenwickTree) Get(index int) (int64, error) { return f.RangeSum(index, index) }

// FirstIndexWithPrefixAtLeast is the smallest index whose prefix sum is >= target, or -1 if the total is smaller.
// Requires all values >= 0. A binary descent over the implicit tree: O(log n) without calling PrefixSum repeatedly.
func (f *FenwickTree) FirstIndexWithPrefixAtLeast(target int64) int {
	if target <= 0 {
		return 0
	}
	pos := 0
	remaining := target
	for step := highestOneBit(f.n); step > 0; step >>= 1 {
		next := pos + step
		if next <= f.n && f.tree[next] < remaining {
			pos = next
			remaining -= f.tree[next]
		}
	}
	if pos < f.n {
		return pos // pos is the 1-indexed position of the last node that stayed below target
	}
	return -1
}

// highestOneBit is Integer.highestOneBit for a positive int.
func highestOneBit(n int) int { return 1 << (bits.Len(uint(n)) - 1) }

// ---------------------------------------------------------------- SegmentTree ----

// SegmentTree over n values with point set and range sum, no laziness. Array-backed, 1-indexed heap layout:
// node k covers a half-open range, children are 2k and 2k+1, leaves start at offset `size` (next power of two).
type SegmentTree struct {
	n    int
	size int     // smallest power of two >= n
	sum  []int64 // 2 * size entries
}

func NewSegmentTree(values []int64) (*SegmentTree, error) {
	if len(values) == 0 {
		return nil, fmt.Errorf("%w: size must be positive", ErrInvalidArgument)
	}
	s := 1
	for s < len(values) {
		s <<= 1
	}
	t := &SegmentTree{n: len(values), size: s, sum: make([]int64, 2*s)}
	copy(t.sum[s:], values)
	for k := s - 1; k >= 1; k-- {
		var err error
		if t.sum[k], err = addChecked(t.sum[2*k], t.sum[2*k+1]); err != nil {
			return nil, err
		}
	}
	return t, nil
}

func (t *SegmentTree) Size() int { return t.n }

// Set does values[index] = value and recomputes the log n ancestors.
func (t *SegmentTree) Set(index int, value int64) error {
	if err := checkIndex(index, t.n); err != nil {
		return err
	}
	k := index + t.size
	t.sum[k] = value
	for k >>= 1; k >= 1; k >>= 1 {
		var err error
		if t.sum[k], err = addChecked(t.sum[2*k], t.sum[2*k+1]); err != nil {
			return err
		}
	}
	return nil
}

func (t *SegmentTree) Get(index int) (int64, error) {
	if err := checkIndex(index, t.n); err != nil {
		return 0, err
	}
	return t.sum[index+t.size], nil
}

// RangeSum is the sum of values[lo..hi] inclusive. Bottom-up: shrink both ends toward each other, adding whole nodes.
// Separate left and right accumulators keep the code correct for non-commutative operations later.
func (t *SegmentTree) RangeSum(lo, hi int) (int64, error) {
	if err := checkRange(lo, hi, t.n); err != nil {
		return 0, err
	}
	var left, right int64
	var err error
	l, r := lo+t.size, hi+t.size+1 // half-open [l, r)
	for l < r {
		if l&1 == 1 {
			if left, err = addChecked(left, t.sum[l]); err != nil {
				return 0, err
			}
			l++
		}
		if r&1 == 1 {
			r--
			if right, err = addChecked(t.sum[r], right); err != nil {
				return 0, err
			}
		}
		l >>= 1
		r >>= 1
	}
	return addChecked(left, right)
}

// ---------------------------------------------------------------- LazySegmentTree ----

// LazySegmentTree: range add and range sum, both O(log n). Recursive, 1-indexed, node k covers [nodeLo, nodeHi].
// pending[k] is an addend that applies to every value under k but has not yet been pushed to k's children.
type LazySegmentTree struct {
	n       int
	sum     []int64
	pending []int64
}

func NewLazySegmentTree(values []int64) (*LazySegmentTree, error) {
	if len(values) == 0 {
		return nil, fmt.Errorf("%w: size must be positive", ErrInvalidArgument)
	}
	t := &LazySegmentTree{n: len(values), sum: make([]int64, 4*len(values)), pending: make([]int64, 4*len(values))}
	if err := t.build(1, 0, t.n-1, values); err != nil {
		return nil, err
	}
	return t, nil
}

func (t *LazySegmentTree) build(k, lo, hi int, values []int64) error {
	if lo == hi {
		t.sum[k] = values[lo]
		return nil
	}
	mid := int(uint(lo+hi) >> 1)
	if err := t.build(2*k, lo, mid, values); err != nil {
		return err
	}
	if err := t.build(2*k+1, mid+1, hi, values); err != nil {
		return err
	}
	var err error
	t.sum[k], err = addChecked(t.sum[2*k], t.sum[2*k+1])
	return err
}

func (t *LazySegmentTree) Size() int { return t.n }

// apply a pending addend to node k covering `count` values: the sum grows by count * delta.
func (t *LazySegmentTree) apply(k, count int, delta int64) error {
	grow, err := mulChecked(delta, int64(count))
	if err != nil {
		return err
	}
	if t.sum[k], err = addChecked(t.sum[k], grow); err != nil {
		return err
	}
	t.pending[k], err = addChecked(t.pending[k], delta)
	return err
}

// push k's pending addend one level down so that its children are exact before we look at them.
func (t *LazySegmentTree) push(k, lo, hi int) error {
	if t.pending[k] == 0 {
		return nil
	}
	mid := int(uint(lo+hi) >> 1)
	if err := t.apply(2*k, mid-lo+1, t.pending[k]); err != nil {
		return err
	}
	if err := t.apply(2*k+1, hi-mid, t.pending[k]); err != nil {
		return err
	}
	t.pending[k] = 0
	return nil
}

// RangeAdd does values[i] += delta for every i in [lo, hi].
func (t *LazySegmentTree) RangeAdd(lo, hi int, delta int64) error {
	if err := checkRange(lo, hi, t.n); err != nil {
		return err
	}
	return t.rangeAdd(1, 0, t.n-1, lo, hi, delta)
}

func (t *LazySegmentTree) rangeAdd(k, nodeLo, nodeHi, lo, hi int, delta int64) error {
	if hi < nodeLo || nodeHi < lo { // disjoint
		return nil
	}
	if lo <= nodeLo && nodeHi <= hi { // fully covered: stop here
		return t.apply(k, nodeHi-nodeLo+1, delta)
	}
	if err := t.push(k, nodeLo, nodeHi); err != nil {
		return err
	}
	mid := int(uint(nodeLo+nodeHi) >> 1)
	if err := t.rangeAdd(2*k, nodeLo, mid, lo, hi, delta); err != nil {
		return err
	}
	if err := t.rangeAdd(2*k+1, mid+1, nodeHi, lo, hi, delta); err != nil {
		return err
	}
	var err error
	t.sum[k], err = addChecked(t.sum[2*k], t.sum[2*k+1])
	return err
}

// RangeSum is the sum of values[lo..hi] inclusive.
func (t *LazySegmentTree) RangeSum(lo, hi int) (int64, error) {
	if err := checkRange(lo, hi, t.n); err != nil {
		return 0, err
	}
	return t.rangeSum(1, 0, t.n-1, lo, hi)
}

func (t *LazySegmentTree) rangeSum(k, nodeLo, nodeHi, lo, hi int) (int64, error) {
	if hi < nodeLo || nodeHi < lo {
		return 0, nil
	}
	if lo <= nodeLo && nodeHi <= hi {
		return t.sum[k], nil
	}
	if err := t.push(k, nodeLo, nodeHi); err != nil {
		return 0, err
	}
	mid := int(uint(nodeLo+nodeHi) >> 1)
	left, err := t.rangeSum(2*k, nodeLo, mid, lo, hi)
	if err != nil {
		return 0, err
	}
	right, err := t.rangeSum(2*k+1, mid+1, nodeHi, lo, hi)
	if err != nil {
		return 0, err
	}
	return addChecked(left, right)
}

func (t *LazySegmentTree) Get(index int) (int64, error) { return t.RangeSum(index, index) }

// ---------------------------------------------------------------- BalanceHistory ----

// BalanceHistory is the payments framing: one account's closing balance for each day of a period, with backdated
// postings and average-daily-balance queries. Backed by a LazySegmentTree over balances: a posting on day d shifts
// every balance from d to the end of the period, and a statement needs the sum of balances over a month.
type BalanceHistory struct {
	balances *LazySegmentTree
	currency Currency
}

func NewBalanceHistory(openingBalanceMinor int64, days int, currency Currency) (*BalanceHistory, error) {
	initial := make([]int64, days)
	for i := range initial {
		initial[i] = openingBalanceMinor
	}
	balances, err := NewLazySegmentTree(initial)
	if err != nil {
		return nil, err
	}
	return &BalanceHistory{balances: balances, currency: currency}, nil
}

func (h *BalanceHistory) Days() int { return h.balances.Size() }

// Post an amount effective on `day`: every closing balance from that day onward moves by the amount.
func (h *BalanceHistory) Post(day int, deltaMinor int64) error {
	return h.balances.RangeAdd(day, h.balances.Size()-1, deltaMinor)
}

func (h *BalanceHistory) BalanceOn(day int) (Money, error) {
	v, err := h.balances.Get(day)
	return OfMinor(v, h.currency), err
}

// SumOfBalances is the sum of closing balances over [fromDay, toDay], the numerator of average daily balance.
func (h *BalanceHistory) SumOfBalances(fromDay, toDay int) (Money, error) {
	v, err := h.balances.RangeSum(fromDay, toDay)
	return OfMinor(v, h.currency), err
}

// AverageDailyBalance over [fromDay, toDay], rounded half-even to a minor unit.
func (h *BalanceHistory) AverageDailyBalance(fromDay, toDay int) (Money, error) {
	total, err := h.balances.RangeSum(fromDay, toDay)
	if err != nil {
		return Money{}, err
	}
	days := int64(toDay - fromDay + 1)
	return OfMinor(DivideHalfEven(total, days), h.currency), nil
}

// DivideHalfEven rounds the quotient to the nearest integer, ties to even. Go's / and % truncate toward zero like Java's.
func DivideHalfEven(dividend, divisor int64) int64 {
	q, r := dividend/divisor, dividend%divisor
	if r == 0 {
		return q
	}
	sign := int64(1)
	if (dividend < 0) != (divisor < 0) {
		sign = -1
	}
	twice, d := abs64(r)*2, abs64(divisor)
	if twice > d {
		return q + sign
	}
	if twice < d {
		return q
	}
	if q%2 == 0 {
		return q
	}
	return q + sign
}

func abs64(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}

// ---------------------------------------------------------------- Demo ----

// NaiveRangeSum is the reference implementation: the thing the trees must agree with on every query.
func NaiveRangeSum(values []int64, lo, hi int) int64 {
	s := int64(0)
	for i := lo; i <= hi; i++ {
		s += values[i]
	}
	return s
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func mustHold(err error) {
	if err != nil {
		panic(err)
	}
}

func joinInt64(xs []int64) string {
	parts := make([]string, len(xs))
	for i, x := range xs {
		parts[i] = strconv.FormatInt(x, 10)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func fenwickInternals(n int) string {
	var sb strings.Builder
	sb.WriteString("[")
	for i := 1; i <= n; i++ {
		span := i & -i
		if i > 1 {
			sb.WriteString(", ")
		}
		fmt.Fprintf(&sb, "t%d=sum(%d..%d)", i, i-span, i-1)
	}
	sb.WriteString("]")
	return sb.String()
}

func main() {
	fmt.Println("== 1. Fenwick tree: daily net postings, balance by prefix sum ==")
	dailyNet := []int64{12_500, -3_200, 0, 4_150, -900, 7_000, -2_250, 1_000} // minor units, 8 days
	fw := must(BuildFenwick(dailyNet))
	fmt.Println("  daily net   = " + joinInt64(dailyNet))
	prefixes := []int64{}
	for d := range dailyNet {
		prefixes = append(prefixes, must(fw.PrefixSum(d)))
	}
	fmt.Println("  balances    = " + joinInt64(prefixes))
	fmt.Println("  net days 2..5 = " + OfMinor(must(fw.RangeSum(2, 5)), USD).String())
	fmt.Println("  backdated correction: day 1 was one dollar more negative than recorded, add(1, -100)")
	mustHold(fw.Add(1, -100))
	fmt.Printf("  balance day 1 = %s, day 7 = %s (every later balance moved by one dollar)\n",
		OfMinor(must(fw.PrefixSum(1)), USD), OfMinor(must(fw.PrefixSum(7)), USD))
	fmt.Println("  tree[] internals (1-indexed, node i covers i & -i values): " + fenwickInternals(len(dailyNet)))

	fmt.Println()
	fmt.Println("== 2. Segment tree: same queries, point set instead of point add ==")
	st := must(NewSegmentTree(dailyNet))
	fmt.Printf("  sum days 2..5 before = %d\n", must(st.RangeSum(2, 5)))
	mustHold(st.Set(3, 10_000))
	fmt.Printf("  set day 3 to 10000: sum days 2..5 after = %d, naive = %d\n", must(st.RangeSum(2, 5)),
		NaiveRangeSum([]int64{12_500, -3_200, 0, 10_000, -900, 7_000, -2_250, 1_000}, 2, 5))

	fmt.Println()
	fmt.Println("== 3. Lazy segment tree: backdated posting = range add over closing balances ==")
	history := must(NewBalanceHistory(100_000, 30, USD)) // 1000.00 USD opening, 30 days
	mustHold(history.Post(0, 12_500))                    // +125.00 on day 0
	mustHold(history.Post(10, -40_000))                  // -400.00 on day 10
	mustHold(history.Post(20, 5_000))                    // +50.00 on day 20
	fmt.Printf("  balance day 0 = %s, day 10 = %s, day 29 = %s\n",
		must(history.BalanceOn(0)), must(history.BalanceOn(10)), must(history.BalanceOn(29)))
	fmt.Printf("  average daily balance days 0..29 = %s\n", must(history.AverageDailyBalance(0, 29)))
	fmt.Println("  a backdated posting arrives: -75.00 effective day 5")
	mustHold(history.Post(5, -7_500))
	fmt.Printf("  balance day 4 = %s, day 5 = %s, day 29 = %s\n",
		must(history.BalanceOn(4)), must(history.BalanceOn(5)), must(history.BalanceOn(29)))
	fmt.Printf("  average daily balance days 0..29 = %s (25 of 30 days moved by -75.00: 25 * 75 / 30 = 62.50 lower)\n",
		must(history.AverageDailyBalance(0, 29)))
	fmt.Println("  one rangeAdd touched O(log 30) nodes, not 25 days")

	fmt.Println()
	fmt.Println("== 4. Fenwick descent: which day does cumulative volume cross 20000 minor units? ==")
	volume := []int64{5_000, 3_000, 8_000, 2_000, 6_000, 1_000}
	vol := must(BuildFenwick(volume))
	fmt.Println("  volume      = " + joinInt64(volume))
	fmt.Printf("  first index with prefix >= 20000 = %d (prefix sums 5000, 8000, 16000, 18000, 24000)\n", vol.FirstIndexWithPrefixAtLeast(20_000))
	fmt.Printf("  first index with prefix >= 25001 = %d (total is 25000)\n", vol.FirstIndexWithPrefixAtLeast(25_001))

	fmt.Println()
	fmt.Println("== 5. Randomized agreement: 20000 mixed operations against the naive array ==")
	rng := NewJavaRandom(42)
	n := 257 // deliberately not a power of two
	model := make([]int64, n)
	lazy := must(NewLazySegmentTree(model))
	fenwick := must(NewFenwickTree(n))
	mismatches := 0
	for op := 0; op < 20_000; op++ {
		a, b := int(rng.NextIntN(int32(n))), int(rng.NextIntN(int32(n)))
		lo, hi := min(a, b), max(a, b)
		if rng.NextBoolean() {
			delta := int64(rng.NextIntN(2001)) - 1000
			for i := lo; i <= hi; i++ {
				model[i] += delta
				mustHold(fenwick.Add(i, delta))
			}
			mustHold(lazy.RangeAdd(lo, hi, delta))
		} else {
			expect := NaiveRangeSum(model, lo, hi)
			if must(lazy.RangeSum(lo, hi)) != expect || must(fenwick.RangeSum(lo, hi)) != expect {
				mismatches++
			}
		}
	}
	fmt.Printf("  mismatches = %d\n", mismatches)

	fmt.Println()
	fmt.Println("== 6. Cost: 10 million days, one query ==")
	big := 10_000_000
	fmt.Printf("  naive range sum: up to %d additions; Fenwick: 2 * %d node reads; segment tree: about 2 * %d node reads\n",
		big, bits.Len(uint(big)), bits.Len(uint(big)))
	fmt.Printf("  memory: Fenwick %d longs; segment tree %d longs; lazy tree 2 x %d longs\n", big+1, 2*highestOneBit(big)*2, 4*big)
}
