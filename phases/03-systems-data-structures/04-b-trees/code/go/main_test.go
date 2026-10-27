// Tests for phases/03-systems-data-structures/04-b-trees. Run: go test ./...
package main

import (
	"errors"
	"math"
	"slices"
	"sort"
	"testing"
)

func isSorted(xs []int) bool {
	for i := 1; i < len(xs); i++ {
		if xs[i-1] >= xs[i] {
			return false
		}
	}
	return true
}

func holds(t *testing.T, name string, err error) {
	t.Helper()
	if err != nil {
		t.Errorf("%s: %v", name, err)
	}
}

func newTree[K int | int64, V any](t *testing.T, order int) *BTree[K, V] {
	t.Helper()
	tree, err := NewBTree[K, V](order)
	if err != nil {
		t.Fatal(err)
	}
	return tree
}

// sortedKeys is the stand-in for java.util.TreeMap's key set.
func sortedKeys(m map[int]int) []int {
	out := make([]int, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Ints(out)
	return out
}

func TestConstructionAndOccupancyBounds(t *testing.T) {
	if _, err := NewBTree[int, string](2); !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("order below 3 rejected: %v", err)
	}
	o4 := newTree[int, string](t, 4)
	if o4.MaxKeys != 3 || o4.MinKeys != 1 {
		t.Error("order 4: maxKeys 3, minKeys 1")
	}
	o5 := newTree[int, string](t, 5)
	if o5.MaxKeys != 4 || o5.MinKeys != 2 {
		t.Error("order 5: maxKeys 4, minKeys 2 (ceil(5/2)-1)")
	}
	if _, ok := o4.Get(1); o4.Height() != 1 || o4.Size() != 0 || ok {
		t.Error("empty tree: height 1, size 0, get is absent")
	}
}

func TestFirstSplitByHand(t *testing.T) {
	o4 := newTree[int, string](t, 4)
	o4.Put(10, "a")
	o4.Put(20, "b")
	o4.Put(30, "c")
	if o4.Height() != 1 || o4.NodeCount() != 1 || o4.Stats.Splits != 0 {
		t.Error("three keys fit in one order-4 node")
	}
	o4.Put(40, "d") // overflow: split at median 20
	if o4.Height() != 2 || o4.NodeCount() != 3 || o4.Stats.Splits != 1 {
		t.Error("fourth key splits the root: height 2, 3 nodes, 1 split")
	}
	if !slices.Equal(o4.Keys(), []int{10, 20, 30, 40}) || o4.CheckInvariants() != nil {
		t.Error("split preserves order and content")
	}
	a, _ := o4.Get(10)
	b, _ := o4.Get(20)
	d, _ := o4.Get(40)
	_, missing := o4.Get(25)
	if a != "a" || b != "b" || d != "d" || missing {
		t.Error("get after split finds every key")
	}
	o4.Put(20, "B")
	if v, _ := o4.Get(20); v != "B" || o4.Size() != 4 {
		t.Error("put existing key replaces value without growing")
	}
}

const testN = 50_000

func sequentialTree(t *testing.T) *BTree[int, int] {
	seq := newTree[int, int](t, 32)
	for i := 0; i < testN; i++ {
		seq.Put(i, i)
	}
	return seq
}

func shuffledTree(t *testing.T) (*BTree[int, int], []int) {
	shuffled := make([]int, testN)
	for i := range shuffled {
		shuffled[i] = i
	}
	NewJavaRandom(3).Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
	rnd := newTree[int, int](t, 32)
	for _, k := range shuffled {
		rnd.Put(k, k)
	}
	return rnd, shuffled
}

func TestSequentialInvariantsAndPageReads(t *testing.T) {
	seq := sequentialTree(t)
	holds(t, "sequential inserts keep the invariants", seq.CheckInvariants())
	if seq.Size() != testN {
		t.Error("size counts every key")
	}
	if float64(seq.Height()) > HeightBound(32, testN) {
		t.Error("height within Bayer-McCreight bound")
	}
	if seq.Height() > 5 {
		t.Error("height is logarithmic in fan-out (<= 5 levels for 50k keys at order 32)")
	}
	if !isSorted(seq.Keys()) || len(seq.Keys()) != testN {
		t.Error("keys() is sorted and complete")
	}
	seq.Stats.Reset()
	seq.Get(31_337)
	if seq.Stats.Reads != int64(seq.Height()) {
		t.Errorf("a point lookup reads exactly height pages: %d", seq.Stats.Reads)
	}
	seq.Stats.Reset()
	hits := seq.Range(1_000, 1_099)
	if len(hits) != 100 || hits[0] != 1_000 || hits[99] != 1_099 {
		t.Error("range returns the inclusive window in order")
	}
	if seq.Stats.Reads > int64(seq.Height()+100/seq.MinKeys+2) {
		t.Errorf("range scan reads few pages beyond the leaves it touches: %d", seq.Stats.Reads)
	}
	if len(seq.Range(testN+1, testN+100)) != 0 {
		t.Error("range with no hits is empty")
	}
	if !slices.Equal(seq.Range(math.MinInt32, math.MaxInt32), seq.Keys()) {
		t.Error("range across whole tree equals keys()")
	}
}

func TestRandomInputOccupancy(t *testing.T) {
	seq := sequentialTree(t)
	rnd, shuffled := shuffledTree(t)
	holds(t, "random inserts keep the invariants", rnd.CheckInvariants())
	if !slices.Equal(rnd.Keys(), seq.Keys()) {
		t.Error("random order yields the same key set")
	}
	seqFill := float64(seq.Size()) / float64(seq.NodeCount()*seq.MaxKeys)
	rndFill := float64(rnd.Size()) / float64(rnd.NodeCount()*rnd.MaxKeys)
	if !(seqFill > 0.45 && seqFill < 0.55) {
		t.Errorf("naive median split leaves sequential pages ~half full: %.3f", seqFill)
	}
	if !(rndFill > 0.60 && rndFill < 0.78) {
		t.Errorf("random input fills pages to ~ln 2 (69%%): %.3f", rndFill)
	}
	reference := map[int]int{}
	for _, k := range shuffled {
		reference[k] = k
	}
	want := []int{}
	for _, k := range sortedKeys(reference) {
		if k >= 123 && k <= 4567 {
			want = append(want, k)
		}
	}
	if !slices.Equal(rnd.Range(123, 4567), want) {
		t.Error("agrees with the sorted reference on a range")
	}
}

func TestDeletesBorrowMergeShrink(t *testing.T) {
	rnd, _ := shuffledTree(t)
	for i := 0; i < testN; i += 2 {
		rnd.Remove(i)
	}
	if rnd.Size() != testN/2 {
		t.Error("remove halves the size")
	}
	if rnd.CheckInvariants() != nil || rnd.Stats.Merges == 0 {
		t.Error("invariants hold after mass deletes (borrow and merge paths exercised)")
	}
	_, ok0 := rnd.Get(0)
	_, ok1000 := rnd.Get(1_000)
	v1, _ := rnd.Get(1)
	v999, _ := rnd.Get(999)
	if ok0 || ok1000 || v1 != 1 || v999 != 999 {
		t.Error("removed keys gone, survivors present")
	}
	rnd.Remove(-5)
	if rnd.Size() != testN/2 || rnd.CheckInvariants() != nil {
		t.Error("removing a missing key is a no-op")
	}

	shrink := newTree[int, string](t, 4)
	for i := 0; i < 1_000; i++ {
		shrink.Put(i, "x")
	}
	before := shrink.Height()
	for i := 0; i < 1_000; i++ {
		shrink.Remove(i)
	}
	if !(before > 1 && shrink.Height() == 1 && shrink.Size() == 0 && shrink.CheckInvariants() == nil) {
		t.Error("deleting everything shrinks height back to 1")
	}
}

func TestHandTracedDeletes(t *testing.T) {
	// the hand-traced deletes from the lesson: borrow, then merge, then separator replacement
	traced := newTree[int, string](t, 4)
	for i := 1; i <= 10; i++ {
		traced.Put(i, "v")
	}
	traced.Stats.Reset()
	traced.Remove(10)
	if traced.NodeCount() != 5 || traced.Stats.Merges != 0 || traced.CheckInvariants() != nil {
		t.Error("remove 10 borrows from [7 8]: still 5 nodes, no merge")
	}
	traced.Remove(9)
	if traced.NodeCount() != 4 || traced.Stats.Merges != 1 || traced.CheckInvariants() != nil {
		t.Error("remove 9 merges [7] + 8 + []: 4 nodes, one merge")
	}
	traced.Remove(6)
	if traced.NodeCount() != 4 || !slices.Equal(traced.Keys(), []int{1, 2, 3, 4, 5, 7, 8}) || traced.CheckInvariants() != nil {
		t.Error("remove separator 6 replaces it with predecessor 5")
	}

	// deleting an internal key: replaced by predecessor from a leaf
	internal := newTree[int, string](t, 4)
	for i := 1; i <= 10; i++ {
		internal.Put(i, "v")
	}
	all := slices.Clone(internal.Keys())
	for _, k := range []int{4, 2, 8, 6} { // separators in the order-4 tree built from 1..10
		internal.Remove(k)
		all = slices.DeleteFunc(all, func(x int) bool { return x == k })
		if !slices.Equal(internal.Keys(), all) || internal.CheckInvariants() != nil {
			t.Errorf("deleting separator key %d keeps order and invariants", k)
		}
	}
}

func TestChurnAgainstModel(t *testing.T) {
	rng := NewJavaRandom(99)
	churn := newTree[int, int](t, 5)
	model := map[int]int{}
	for step := 0; step < 20_000; step++ {
		key := int(rng.NextIntN(2_000))
		if rng.NextIntN(3) == 0 {
			churn.Remove(key)
			delete(model, key)
		} else {
			churn.Put(key, step)
			model[key] = step
		}
		if step%1_000 == 999 {
			if !slices.Equal(churn.Keys(), sortedKeys(model)) || churn.CheckInvariants() != nil || churn.Size() != len(model) {
				t.Fatalf("20k mixed operations agree with the model and keep invariants: diverged at step %d", step)
			}
		}
	}
}

func TestLowerBound(t *testing.T) {
	node := &Node[int, string]{Keys: []int{10, 20, 30}}
	cases := []struct {
		name string
		key  int
		want int
	}{
		{"below all keys is 0", 5, 0},
		{"on an exact key is its index", 20, 1},
		{"between keys picks the child slot", 25, 2},
		{"above all keys is keyCount", 31, 3},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := LowerBound(node, c.key); got != c.want {
				t.Errorf("lowerBound(%d) = %d, want %d", c.key, got, c.want)
			}
		})
	}
}
