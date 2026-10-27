// Tests for phases/03-systems-data-structures/10-segment-trees-and-fenwick-trees. Run: go test ./...
package main

import (
	"errors"
	"math"
	"testing"
)

var values = []int64{12_500, -3_200, 0, 4_150, -900, 7_000, -2_250, 1_000}

// unwrap turns a (value, error) pair into the value and fails the test on an error: n64(t)(fw.PrefixSum(0)).
func unwrap[T any](t *testing.T) func(T, error) T {
	return func(v T, err error) T {
		t.Helper()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		return v
	}
}

func n64(t *testing.T) func(int64, error) int64                   { return unwrap[int64](t) }
func mny(t *testing.T) func(Money, error) Money                   { return unwrap[Money](t) }
func fenwick(t *testing.T) func(*FenwickTree, error) *FenwickTree { return unwrap[*FenwickTree](t) }
func segtree(t *testing.T) func(*SegmentTree, error) *SegmentTree { return unwrap[*SegmentTree](t) }
func lazytree(t *testing.T) func(*LazySegmentTree, error) *LazySegmentTree {
	return unwrap[*LazySegmentTree](t)
}
func history(t *testing.T) func(*BalanceHistory, error) *BalanceHistory {
	return unwrap[*BalanceHistory](t)
}

func wantErr(t *testing.T, name string, err, sentinel error) {
	t.Helper()
	if !errors.Is(err, sentinel) {
		t.Errorf("%s: got %v, want %v", name, err, sentinel)
	}
}

func incremental(t *testing.T, vals []int64) *FenwickTree {
	t.Helper()
	f := fenwick(t)(NewFenwickTree(len(vals)))
	for i, v := range vals {
		if err := f.Add(i, v); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func TestFenwickHappyPath(t *testing.T) {
	fw := fenwick(t)(BuildFenwick(values))
	cases := []struct {
		name  string
		index int
		want  int64
	}{
		{"fenwick prefix(0)", 0, 12_500},
		{"fenwick prefix(1)", 1, 9_300},
		{"fenwick prefix(7) is the total", 7, 18_300},
		{"fenwick prefix(-1) is zero", -1, 0},
	}
	for _, c := range cases {
		if got := n64(t)(fw.PrefixSum(c.index)); got != c.want {
			t.Errorf("%s: %d", c.name, got)
		}
	}
	if n64(t)(fw.RangeSum(2, 5)) != 10_250 {
		t.Error("fenwick rangeSum(2,5)")
	}
	if n64(t)(fw.RangeSum(4, 4)) != -900 {
		t.Error("fenwick rangeSum single element")
	}
	if n64(t)(fw.Get(5)) != 7_000 {
		t.Error("fenwick get")
	}
	if err := fw.Add(1, -100); err != nil {
		t.Fatal(err)
	}
	if n64(t)(fw.PrefixSum(1)) != 9_200 {
		t.Error("fenwick add moves prefix at index")
	}
	if n64(t)(fw.PrefixSum(7)) != 18_200 {
		t.Error("fenwick add moves every later prefix")
	}
	if n64(t)(fw.PrefixSum(0)) != 12_500 {
		t.Error("fenwick add leaves earlier prefix alone")
	}
	if n64(t)(fenwick(t)(BuildFenwick(values)).RangeSum(0, 7)) != n64(t)(incremental(t, values).RangeSum(0, 7)) {
		t.Error("fenwick build equals incremental adds")
	}
	one := fenwick(t)(NewFenwickTree(1))
	_ = one.Add(0, 42)
	if n64(t)(one.PrefixSum(0)) != 42 || n64(t)(one.RangeSum(0, 0)) != 42 {
		t.Error("fenwick size 1")
	}
}

func TestFenwickErrors(t *testing.T) {
	fw := fenwick(t)(BuildFenwick(values))
	_, err := NewFenwickTree(0)
	wantErr(t, "fenwick rejects size 0", err, ErrInvalidArgument)
	wantErr(t, "fenwick add out of range", fw.Add(8, 1), ErrIndexOutOfBounds)
	_, err = fw.PrefixSum(8)
	wantErr(t, "fenwick prefix out of range", err, ErrIndexOutOfBounds)
	_, err = fw.RangeSum(5, 2)
	wantErr(t, "fenwick lo > hi", err, ErrInvalidArgument)

	// The Java wraps all three calls in one checkThrows; here any of them may be the one that reports the overflow.
	f := fenwick(t)(NewFenwickTree(2))
	err = errors.Join(f.Add(0, math.MaxInt64), f.Add(1, 1))
	_, prefixErr := f.PrefixSum(1)
	wantErr(t, "fenwick overflow is an error, not a wrap", errors.Join(err, prefixErr), ErrOverflow)
}

func TestFenwickDescent(t *testing.T) {
	vol := fenwick(t)(BuildFenwick([]int64{5_000, 3_000, 8_000, 2_000, 6_000, 1_000}))
	cases := []struct {
		name   string
		target int64
		want   int
	}{
		{"exact hit on a prefix", 8_000, 1},
		{"between prefixes", 20_000, 4},
		{"first element", 1, 0},
		{"total exactly", 25_000, 5},
		{"beyond total is -1", 25_001, -1},
		{"target 0 is index 0", 0, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := vol.FirstIndexWithPrefixAtLeast(c.target); got != c.want {
				t.Errorf("descent(%d) = %d, want %d", c.target, got, c.want)
			}
		})
	}
	pow2 := fenwick(t)(BuildFenwick([]int64{1, 1, 1, 1, 1, 1, 1, 1}))
	if pow2.FirstIndexWithPrefixAtLeast(8) != 7 || pow2.FirstIndexWithPrefixAtLeast(9) != -1 {
		t.Error("descent on power-of-two size")
	}
}

func TestSegmentTree(t *testing.T) {
	st := segtree(t)(NewSegmentTree(values))
	if n64(t)(st.RangeSum(0, 7)) != 18_300 {
		t.Error("segtree rangeSum whole")
	}
	if n64(t)(st.RangeSum(2, 5)) != 10_250 {
		t.Error("segtree rangeSum(2,5)")
	}
	if n64(t)(st.RangeSum(0, 0)) != 12_500 {
		t.Error("segtree rangeSum(0,0)")
	}
	if n64(t)(st.RangeSum(7, 7)) != 1_000 {
		t.Error("segtree rangeSum(7,7)")
	}
	if err := st.Set(3, 10_000); err != nil {
		t.Fatal(err)
	}
	if n64(t)(st.Get(3)) != 10_000 {
		t.Error("segtree set then get")
	}
	if n64(t)(st.RangeSum(2, 5)) != 16_100 || n64(t)(st.RangeSum(0, 7)) != 24_150 {
		t.Error("segtree set updates ancestors")
	}
	odd := segtree(t)(NewSegmentTree([]int64{1, 2, 3, 4, 5})) // n not a power of two
	if n64(t)(odd.RangeSum(0, 4)) != 15 {
		t.Error("segtree non-power-of-two size: full sum")
	}
	if n64(t)(odd.RangeSum(3, 4)) != 9 {
		t.Error("segtree non-power-of-two size: tail range")
	}
	if n64(t)(odd.RangeSum(4, 4)) != 5 {
		t.Error("segtree non-power-of-two size: padding is not counted")
	}
	_, err := NewSegmentTree(nil)
	wantErr(t, "segtree rejects empty", err, ErrInvalidArgument)
	_, err = odd.RangeSum(3, 1)
	wantErr(t, "segtree lo > hi", err, ErrInvalidArgument)
	_, err = odd.RangeSum(0, 5)
	wantErr(t, "segtree out of range", err, ErrIndexOutOfBounds)
}

func TestLazySegmentTree(t *testing.T) {
	lazy := lazytree(t)(NewLazySegmentTree(make([]int64, 10)))
	if err := lazy.RangeAdd(2, 6, 10); err != nil {
		t.Fatal(err)
	}
	if n64(t)(lazy.RangeSum(2, 6)) != 50 {
		t.Error("lazy rangeAdd then sum inside")
	}
	if n64(t)(lazy.RangeSum(0, 3)) != 20 {
		t.Error("lazy rangeAdd then sum overlapping")
	}
	if n64(t)(lazy.RangeSum(7, 9)) != 0 || n64(t)(lazy.Get(1)) != 0 {
		t.Error("lazy rangeAdd leaves outside alone")
	}
	_ = lazy.RangeAdd(5, 9, -3)
	if n64(t)(lazy.Get(5)) != 7 || n64(t)(lazy.Get(6)) != 7 || n64(t)(lazy.Get(7)) != -3 || n64(t)(lazy.Get(4)) != 10 {
		t.Error("lazy overlapping adds compose")
	}
	if n64(t)(lazy.RangeSum(0, 9)) != 50-15 {
		t.Error("lazy total after two adds")
	}
	_ = lazy.RangeAdd(0, 9, 1)
	if n64(t)(lazy.RangeSum(0, 9)) != 45 || n64(t)(lazy.Get(0)) != 1 || n64(t)(lazy.Get(9)) != -2 {
		t.Error("lazy whole-range add stops at the root")
	}
	single := lazytree(t)(NewLazySegmentTree([]int64{5}))
	_ = single.RangeAdd(0, 0, 7)
	if n64(t)(single.Get(0)) != 12 || n64(t)(single.RangeSum(0, 0)) != 12 {
		t.Error("lazy size 1")
	}
	_, err := NewLazySegmentTree([]int64{math.MaxInt64, 1})
	wantErr(t, "lazy overflow is an error", err, ErrOverflow)
}

func TestRandomizedAgreement(t *testing.T) {
	rng := NewJavaRandom(7)
	n := 131
	model := make([]int64, n)
	lz := lazytree(t)(NewLazySegmentTree(model))
	fz := fenwick(t)(NewFenwickTree(n))
	sz := segtree(t)(NewSegmentTree(model))
	for op := 0; op < 5_000; op++ {
		a, b := int(rng.NextIntN(int32(n))), int(rng.NextIntN(int32(n)))
		lo, hi := min(a, b), max(a, b)
		if rng.NextIntN(3) == 0 {
			delta := int64(rng.NextIntN(201)) - 100
			for i := lo; i <= hi; i++ {
				model[i] += delta
				_ = fz.Add(i, delta)
				_ = sz.Set(i, model[i])
			}
			_ = lz.RangeAdd(lo, hi, delta)
		} else {
			expect := NaiveRangeSum(model, lo, hi)
			if n64(t)(lz.RangeSum(lo, hi)) != expect || n64(t)(fz.RangeSum(lo, hi)) != expect || n64(t)(sz.RangeSum(lo, hi)) != expect {
				t.Fatalf("all three structures agree with the naive array over 5000 random ops: diverged at op %d", op)
			}
		}
	}
}

func TestBalanceHistory(t *testing.T) {
	h := history(t)(NewBalanceHistory(100_000, 30, USD))
	if mny(t)(h.BalanceOn(0)).MinorUnits != 100_000 || mny(t)(h.BalanceOn(29)).MinorUnits != 100_000 {
		t.Error("opening balance on every day")
	}
	_ = h.Post(10, -40_000)
	if mny(t)(h.BalanceOn(9)).MinorUnits != 100_000 {
		t.Error("posting on day 10 does not move day 9")
	}
	if mny(t)(h.BalanceOn(10)).MinorUnits != 60_000 || mny(t)(h.BalanceOn(29)).MinorUnits != 60_000 {
		t.Error("posting on day 10 moves day 10 and after")
	}
	if mny(t)(h.SumOfBalances(0, 29)).MinorUnits != 10*100_000+20*60_000 {
		t.Error("sum of balances over the month")
	}
	if mny(t)(h.AverageDailyBalance(0, 29)).MinorUnits != 73_333 { // 2,200,000 / 30 = 73333.33
		t.Error("average daily balance rounds half-even")
	}
	_ = h.Post(5, -7_500)
	if mny(t)(h.SumOfBalances(0, 29)).MinorUnits != 2_200_000-25*7_500 {
		t.Error("backdated posting shifts 25 of 30 days")
	}
	if mny(t)(h.BalanceOn(29)).String() != "525.00 USD" {
		t.Error("Money toString")
	}
	if DivideHalfEven(25, 10) != 2 || DivideHalfEven(35, 10) != 4 {
		t.Error("divideHalfEven tie to even")
	}
	if DivideHalfEven(-25, 10) != -2 || DivideHalfEven(-26, 10) != -3 {
		t.Error("divideHalfEven negative")
	}
}

func TestCheckedArithmetic(t *testing.T) {
	if _, err := addChecked(math.MaxInt64, 1); !errors.Is(err, ErrOverflow) {
		t.Error("addChecked detects positive overflow")
	}
	if _, err := subChecked(math.MinInt64, 1); !errors.Is(err, ErrOverflow) {
		t.Error("subChecked detects negative overflow")
	}
	if _, err := mulChecked(math.MinInt64, -1); !errors.Is(err, ErrOverflow) {
		t.Error("mulChecked detects MinInt64 * -1")
	}
	if v, err := mulChecked(-7_500, 25); err != nil || v != -187_500 {
		t.Error("mulChecked passes ordinary products")
	}
}
