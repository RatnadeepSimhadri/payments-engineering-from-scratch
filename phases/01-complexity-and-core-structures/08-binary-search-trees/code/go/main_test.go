// Tests for phases/01-complexity-and-core-structures/08-binary-search-trees. Run: go test ./...
package main

import (
	"errors"
	"reflect"
	"sort"
	"testing"
)

func check(t *testing.T, name string, ok bool) {
	t.Helper()
	if !ok {
		t.Error(name)
	}
}

func keys[V any](t *BinarySearchTree[int, V]) []int {
	out := []int{}
	for _, e := range t.InOrder() {
		out = append(out, e.Key)
	}
	return out
}

func sample() *BinarySearchTree[int, string] {
	t := NewOrderedTree[int, string]()
	for _, k := range []int{50, 30, 70, 20, 40, 60, 80, 35, 45} {
		t.Put(k, "v"+itoa(k))
	}
	return t
}

func itoa(k int) string {
	if k == 0 {
		return "0"
	}
	s := ""
	for k > 0 {
		s = string(rune('0'+k%10)) + s
		k /= 10
	}
	return s
}

func rangeKeys(t *testing.T, tr *BinarySearchTree[int, string], lo, hi int) []int {
	t.Helper()
	entries, err := tr.RangeQuery(lo, hi)
	if err != nil {
		t.Fatalf("RangeQuery(%d, %d): %v", lo, hi, err)
	}
	out := []int{}
	for _, e := range entries {
		out = append(out, e.Key)
	}
	return out
}

func TestEmptyTree(t *testing.T) {
	empty := NewOrderedTree[int, string]()
	check(t, "empty size", empty.Size() == 0 && empty.IsEmpty())
	check(t, "empty height is -1", empty.Height() == -1)
	_, okMin := empty.MinKey()
	_, okMax := empty.MaxKey()
	check(t, "empty min/max absent", !okMin && !okMax)
	_, ok := empty.Get(1)
	check(t, "empty get absent", !ok)
	_, ok = empty.Remove(1)
	check(t, "empty remove absent", !ok)
	r, err := empty.RangeQuery(0, 10)
	check(t, "empty range empty", err == nil && len(r) == 0)
	check(t, "empty invariant", empty.CheckInvariant())
	_, err = NewBinarySearchTree[int, int](nil)
	check(t, "nil comparator rejected", errors.Is(err, ErrNilComparator))
}

func TestInsertSearchOrder(t *testing.T) {
	tr := sample()
	check(t, "size after 9 puts", tr.Size() == 9)
	check(t, "in-order is sorted", reflect.DeepEqual(keys(tr), []int{20, 30, 35, 40, 45, 50, 60, 70, 80}))
	check(t, "height of sample", tr.Height() == 3)
	v, ok := tr.Get(45)
	check(t, "get hit", ok && v == "v45")
	_, ok = tr.Get(55)
	check(t, "get miss", !ok)
	check(t, "containsKey", tr.ContainsKey(80) && !tr.ContainsKey(81))
	mn, _ := tr.MinKey()
	mx, _ := tr.MaxKey()
	check(t, "min/max", mn == 20 && mx == 80)
	old, existed := tr.Put(40, "new")
	nv, _ := tr.Get(40)
	check(t, "put replaces and returns old", existed && old == "v40" && nv == "new" && tr.Size() == 9)
	check(t, "invariant after puts", tr.CheckInvariant())

	// floor and ceiling
	fl, ok := tr.FloorKey(45)
	check(t, "floor exact", ok && fl == 45)
	fl, ok = tr.FloorKey(42)
	check(t, "floor between", ok && fl == 40)
	_, ok = tr.FloorKey(10)
	check(t, "floor below min is absent", !ok)
	ce, ok := tr.CeilingKey(42)
	check(t, "ceiling between", ok && ce == 45)
	_, ok = tr.CeilingKey(90)
	check(t, "ceiling above max is absent", !ok)
}

func TestDelete(t *testing.T) {
	d := sample()
	v, ok := d.Remove(20)
	check(t, "remove leaf", ok && v == "v20" && reflect.DeepEqual(keys(d), []int{30, 35, 40, 45, 50, 60, 70, 80}))
	_, ok = d.Remove(999)
	check(t, "remove missing is absent and keeps size", !ok && d.Size() == 8)
	v, ok = d.Remove(30)
	check(t, "remove node with two children uses successor", ok && v == "v30" && reflect.DeepEqual(keys(d), []int{35, 40, 45, 50, 60, 70, 80}) && d.CheckInvariant())
	v, ok = d.Remove(50)
	check(t, "remove root with two children", ok && v == "v50" && reflect.DeepEqual(keys(d), []int{35, 40, 45, 60, 70, 80}) && d.CheckInvariant())
	v, ok = d.Remove(70)
	check(t, "remove node with one child", ok && v == "v70" && reflect.DeepEqual(keys(d), []int{35, 40, 45, 60, 80}) && d.CheckInvariant())
	a, aok := d.Get(60)
	b, bok := d.Get(45)
	check(t, "successor key still reachable", aok && a == "v60" && bok && b == "v45")
	for _, k := range []int{35, 40, 45, 60, 80} {
		d.Remove(k)
	}
	check(t, "remove everything leaves empty tree", d.IsEmpty() && d.Height() == -1 && d.CheckInvariant())
}

func TestRangeQuery(t *testing.T) {
	r := sample()
	check(t, "range inclusive on both ends", reflect.DeepEqual(rangeKeys(t, r, 35, 60), []int{35, 40, 45, 50, 60}))
	check(t, "range with no hits", len(rangeKeys(t, r, 81, 100)) == 0 && len(rangeKeys(t, r, 0, 19)) == 0)
	check(t, "range single point", len(rangeKeys(t, r, 70, 70)) == 1)
	check(t, "range covering everything", len(rangeKeys(t, r, -1<<31, 1<<31-1)) == 9)
	_, err := r.RangeQuery(60, 35)
	check(t, "range lo > hi rejected", errors.Is(err, ErrInvalidRange))
	r.ResetComparisons()
	_, _ = r.RangeQuery(35, 45)
	check(t, "range query prunes: fewer comparisons than a full scan", r.Comparisons() < 3*9)
}

// sortedModel is the reference ordered map: a sorted key slice plus a map, standing in for java.util.TreeMap.
type sortedModel struct {
	keys   []int
	values map[int]int
}

func newSortedModel() *sortedModel { return &sortedModel{values: map[int]int{}} }

func (m *sortedModel) put(k, v int) (int, bool) {
	old, ok := m.values[k]
	if !ok {
		i := sort.SearchInts(m.keys, k)
		m.keys = append(m.keys, 0)
		copy(m.keys[i+1:], m.keys[i:])
		m.keys[i] = k
	}
	m.values[k] = v
	return old, ok
}

func (m *sortedModel) remove(k int) (int, bool) {
	old, ok := m.values[k]
	if ok {
		i := sort.SearchInts(m.keys, k)
		m.keys = append(m.keys[:i], m.keys[i+1:]...)
		delete(m.values, k)
	}
	return old, ok
}

func (m *sortedModel) subMap(lo, hi int) []int {
	i := sort.SearchInts(m.keys, lo)
	j := sort.SearchInts(m.keys, hi+1)
	return append([]int{}, m.keys[i:j]...)
}

func (m *sortedModel) floorKey(k int) (int, bool) {
	i := sort.SearchInts(m.keys, k+1) - 1
	if i < 0 {
		return 0, false
	}
	return m.keys[i], true
}

func (m *sortedModel) ceilingKey(k int) (int, bool) {
	i := sort.SearchInts(m.keys, k)
	if i >= len(m.keys) {
		return 0, false
	}
	return m.keys[i], true
}

func TestRandomizedAgainstModel(t *testing.T) {
	rnd := NewJavaRandom(2024)
	mine := NewOrderedTree[int, int]()
	ref := newSortedModel()
	agree := true
	for i := 0; i < 3_000 && agree; i++ {
		k := int(rnd.NextInt(500))
		if rnd.NextInt(3) == 0 {
			a, aok := mine.Remove(k)
			b, bok := ref.remove(k)
			agree = aok == bok && a == b
		} else {
			a, aok := mine.Put(k, i)
			b, bok := ref.put(k, i)
			agree = aok == bok && a == b
		}
		if i%250 == 0 {
			lo := int(rnd.NextInt(500))
			hi := lo + int(rnd.NextInt(100))
			entries, _ := mine.RangeQuery(lo, hi)
			got := []int{}
			for _, e := range entries {
				got = append(got, e.Key)
			}
			agree = agree && reflect.DeepEqual(got, ref.subMap(lo, hi))
			fa, faok := mine.FloorKey(lo)
			fb, fbok := ref.floorKey(lo)
			agree = agree && faok == fbok && fa == fb
			ca, caok := mine.CeilingKey(hi)
			cb, cbok := ref.ceilingKey(hi)
			agree = agree && caok == cbok && ca == cb
		}
	}
	check(t, "randomized ops agree with the sorted model", agree && mine.Size() == len(ref.keys) && mine.CheckInvariant())
	check(t, "randomized in-order matches the sorted model", reflect.DeepEqual(keys(mine), ref.keys))

	// degenerate case: sorted insert order gives height n-1
	n := 1_000
	chain := NewOrderedTree[int, int]()
	for i := 0; i < n; i++ {
		chain.Put(i, i)
	}
	check(t, "sorted inserts produce a chain of height n-1", chain.Height() == n-1)
	chain.ResetComparisons()
	chain.Get(n - 1)
	check(t, "lookup in a chain costs n comparisons", chain.Comparisons() == int64(n))
	shuffled := NewOrderedTree[int, int]()
	ks := make([]int, n)
	for i := range ks {
		ks[i] = i
	}
	for i := n - 1; i > 0; i-- {
		j := int(rnd.NextInt(int32(i + 1)))
		ks[i], ks[j] = ks[j], ks[i]
	}
	for _, k := range ks {
		shuffled.Put(k, k)
	}
	check(t, "shuffled inserts stay shallow (< 4 log2 n)", shuffled.Height() < 4*10)
	check(t, "chain still iterable without recursion", len(chain.InOrder()) == n && chain.CheckInvariant())
}

func TestTransactionIndex(t *testing.T) {
	idx := NewTransactionIndex()
	base := int64(1_700_000_000_000)
	a := idx.Record("a", base+1_000, OfMinor(1_000, USD))
	b := idx.Record("b", base+1_000, OfMinor(2_000, USD))
	c := idx.Record("c", base+5_000, OfMinor(300, USD))
	idx.Record("d", base+9_000, OfMinor(44, USD))
	check(t, "same timestamp keeps both postings", idx.Size() == 4 && len(idx.Between(base+1_000, base+1_000)) == 2)
	ids := []string{}
	for _, tx := range idx.Between(base, base+5_000) {
		ids = append(ids, tx.ID)
	}
	check(t, "range by timestamp is oldest first", reflect.DeepEqual(ids, []string{"a", "b", "c"}))
	sum, err := idx.SumBetween(base, base+5_000, USD)
	check(t, "sum over window is exact Money", err == nil && sum == OfMinor(3_300, USD))
	check(t, "remove one of two same-timestamp postings", idx.Remove(a) && reflect.DeepEqual(idx.Between(base+1_000, base+1_000), []Transaction{b}))
	check(t, "remove twice is false", !idx.Remove(a) && idx.Remove(c) && idx.Size() == 2)
	check(t, "index invariant", idx.Tree().CheckInvariant())
	check(t, "money toString", OfMinor(3_300, USD).String() == "33.00 USD")
	_, err = OfMinor(1, USD).Plus(OfMinor(1, Currency{"EUR", 2}))
	check(t, "money plus rejects a currency mismatch", errors.Is(err, ErrCurrencyMismatch))
	_, err = OfMinor(1<<62, USD).Plus(OfMinor(1<<62, USD))
	check(t, "money plus refuses to overflow", errors.Is(err, ErrOverflow))
	check(t, "TxKey compares timestamp before sequence", CompareTxKey(TxKey{1, 9}, TxKey{2, 0}) < 0 && CompareTxKey(TxKey{1, 1}, TxKey{1, 0}) > 0)
}
