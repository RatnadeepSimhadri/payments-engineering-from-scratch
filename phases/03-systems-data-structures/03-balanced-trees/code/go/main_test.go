// Tests for phases/03-systems-data-structures/03-balanced-trees. Run: go test ./...
package main

import (
	"errors"
	"slices"
	"sort"
	"testing"
)

const testN = 20_000

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

func shuffledKeys(seed int64) []int {
	xs := make([]int, testN)
	for i := range xs {
		xs[i] = i
	}
	NewJavaRandom(seed).Shuffle(len(xs), func(i, j int) { xs[i], xs[j] = xs[j], xs[i] })
	return xs
}

func TestNaiveBstDegenerates(t *testing.T) {
	naive := &NaiveBst[int, string]{}
	for i := 0; i < 1_000; i++ {
		naive.Put(i, "x")
	}
	if naive.Height() != 1_000 {
		t.Errorf("naive BST degenerates to height n on sorted input: %d", naive.Height())
	}
}

func TestAvlRotationCases(t *testing.T) {
	cases := []struct {
		name      string
		keys      []int
		rotations int64
	}{
		{"LL case: one rotation", []int{30, 20, 10}, 1},
		{"LR case: two rotations", []int{30, 10, 20}, 2},
		{"RR case: one rotation", []int{10, 20, 30}, 1},
		{"RL case: two rotations", []int{10, 30, 20}, 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tree := &AvlTree[int, string]{}
			for _, k := range c.keys {
				tree.Put(k, "v")
			}
			if tree.Rotations() != c.rotations {
				t.Errorf("rotations = %d, want %d", tree.Rotations(), c.rotations)
			}
			if tree.Height() != 2 {
				t.Errorf("height = %d, want 2", tree.Height())
			}
			if !slices.Equal(tree.Keys(), []int{10, 20, 30}) {
				t.Errorf("keys = %v", tree.Keys())
			}
			holds(t, "balanced", tree.CheckInvariants())
		})
	}
}

func TestAvlSortedAndRandom(t *testing.T) {
	avlSorted := &AvlTree[int, int]{}
	for i := 0; i < testN; i++ {
		avlSorted.Put(i, i)
	}
	holds(t, "AVL invariants hold after sorted inserts", avlSorted.CheckInvariants())
	if float64(avlSorted.Height()) > AvlHeightBound(testN) {
		t.Error("AVL height within 1.44 log2(n+2) on sorted input")
	}
	if avlSorted.Size() != testN {
		t.Error("AVL size counts distinct keys")
	}
	if !isSorted(avlSorted.Keys()) {
		t.Error("AVL inorder is sorted")
	}
	avlSorted.Put(5, 500)
	if v, _ := avlSorted.Get(5); v != 500 || avlSorted.Size() != testN {
		t.Error("AVL put on existing key replaces value, not size")
	}

	avlRandom := &AvlTree[int, int]{}
	for _, k := range shuffledKeys(11) {
		avlRandom.Put(k, k*2)
	}
	holds(t, "AVL invariants hold after random inserts", avlRandom.CheckInvariants())
	v1, _ := avlRandom.Get(777)
	v2, _ := avlRandom.Get(testN - 1)
	_, okMissing := avlRandom.Get(testN)
	if v1 != 1554 || v2 != 2*(testN-1) || okMissing {
		t.Error("AVL get finds every key")
	}

	// ----- delete -----
	for i := 0; i < testN; i += 3 {
		avlRandom.Remove(i)
	}
	wantSize := testN - (testN+2)/3
	if avlRandom.Size() != wantSize {
		t.Errorf("AVL remove shrinks size: %d", avlRandom.Size())
	}
	holds(t, "AVL invariants hold after deletes", avlRandom.CheckInvariants())
	if avlRandom.ContainsKey(0) || avlRandom.ContainsKey(999) || !avlRandom.ContainsKey(1) || !avlRandom.ContainsKey(1000) {
		t.Error("AVL removed keys are gone, others remain")
	}
	avlRandom.Remove(-1)
	if avlRandom.Size() != wantSize || avlRandom.CheckInvariants() != nil {
		t.Error("AVL remove of a missing key is a no-op")
	}
	tiny := &AvlTree[int, string]{}
	tiny.Put(1, "a")
	tiny.Remove(1)
	if _, ok := tiny.Get(1); tiny.Size() != 0 || tiny.Height() != 0 || ok {
		t.Error("AVL remove last key leaves empty tree")
	}
}

func TestAvlOrderedOperations(t *testing.T) {
	ord := &AvlTree[int, string]{}
	for _, k := range []int{50, 20, 70, 10, 30, 60, 80, 25, 35} {
		ord.Put(k, "v")
	}
	if !slices.Equal(ord.Range(22, 60), []int{25, 30, 35, 50, 60}) {
		t.Errorf("range query returns keys in [lo, hi] ascending: %v", ord.Range(22, 60))
	}
	if len(ord.Range(81, 100)) != 0 {
		t.Error("range query with no hits is empty")
	}
	if k, ok := ord.CeilingKey(30); !ok || k != 30 {
		t.Error("ceilingKey exact hit")
	}
	if k, ok := ord.CeilingKey(31); !ok || k != 35 {
		t.Error("ceilingKey rounds up to next key")
	}
	if _, ok := ord.CeilingKey(81); ok {
		t.Error("ceilingKey beyond max is absent")
	}
}

func TestRedBlackCases(t *testing.T) {
	rbCase1 := &RedBlackTree[int, string]{}
	rbCase1.Put(10, "a")
	if rbCase1.CheckInvariants() != nil || rbCase1.BlackHeight() != 1 {
		t.Error("RB single node: root black, black height 1")
	}
	rbCase1.Put(5, "b")
	rbCase1.Put(15, "c") // red children under black root
	if rbCase1.Rotations() != 0 || rbCase1.CheckInvariants() != nil {
		t.Error("RB three nodes need no rotation")
	}
	rbCase1.Put(1, "d") // uncle 15 is red: case 1 recolor
	if rbCase1.Rotations() != 0 || rbCase1.CheckInvariants() != nil || rbCase1.BlackHeight() != 2 {
		t.Error("RB case 1 recolors (uncle red), no rotation")
	}
	rbCase3 := &RedBlackTree[int, string]{}
	for _, k := range []int{10, 20, 30} { // outer grandchild: one rotation
		rbCase3.Put(k, "v")
	}
	if rbCase3.Rotations() != 1 || rbCase3.Height() != 2 || rbCase3.CheckInvariants() != nil {
		t.Error("RB case 3 rotates once on sorted triple")
	}
	rbCase2 := &RedBlackTree[int, string]{}
	for _, k := range []int{10, 30, 20} { // inner grandchild: two rotations
		rbCase2.Put(k, "v")
	}
	if rbCase2.Rotations() != 2 || rbCase2.Height() != 2 || rbCase2.CheckInvariants() != nil {
		t.Error("RB case 2 then 3 rotates twice on zig-zag")
	}
}

func TestRedBlackSortedAndRandom(t *testing.T) {
	avlSorted := &AvlTree[int, int]{}
	rbSorted := &RedBlackTree[int, int]{}
	for i := 0; i < testN; i++ {
		avlSorted.Put(i, i)
		rbSorted.Put(i, i)
	}
	holds(t, "RB invariants hold after sorted inserts", rbSorted.CheckInvariants())
	if float64(rbSorted.Height()) > RbHeightBound(testN) {
		t.Error("RB height within 2 log2(n+1)")
	}
	if rbSorted.Height() < avlSorted.Height() {
		t.Error("RB is looser than AVL on sorted input")
	}
	if !isSorted(rbSorted.Keys()) || len(rbSorted.Keys()) != testN {
		t.Error("RB inorder is sorted and complete")
	}
	shuffled := shuffledKeys(11)
	avlRandom := &AvlTree[int, int]{}
	rbRandom := &RedBlackTree[int, int]{}
	for _, k := range shuffled {
		avlRandom.Put(k, k*2)
		rbRandom.Put(k, k)
	}
	holds(t, "RB invariants hold after random inserts", rbRandom.CheckInvariants())
	if rbRandom.Rotations() >= avlRandom.Rotations() {
		t.Error("RB rotates less than AVL on random input")
	}
	rbRandom.Put(42, -1)
	if v, _ := rbRandom.Get(42); v != -1 || rbRandom.Size() != testN {
		t.Error("RB put on existing key replaces value")
	}

	// ----- agreement with a sorted reference (Java compares with java.util.TreeMap) -----
	reference := slices.Clone(shuffled)
	sort.Ints(reference)
	if !slices.Equal(rbRandom.Keys(), reference) {
		t.Error("RB and the sorted reference hold the same ordered key set")
	}
}

func TestInvariantCheckerCatchesCorruption(t *testing.T) {
	chain := NewAvlNode(1, "a")
	chain.Right = NewAvlNode(2, "b")
	chain.Right.Right = NewAvlNode(3, "c")
	chain.Right.Height = 2
	chain.Height = 3
	if _, err := Verify(chain, nil, nil); !errors.Is(err, ErrInvariantViolated) {
		t.Errorf("checkInvariants detects an unbalanced node: %v", err)
	}
	stale := NewAvlNode(2, "b")
	stale.Left = NewAvlNode(1, "a")
	stale.Height = 1 // should be 2
	if _, err := Verify(stale, nil, nil); !errors.Is(err, ErrInvariantViolated) {
		t.Errorf("checkInvariants detects a stale height: %v", err)
	}
	disordered := NewAvlNode(2, "b")
	disordered.Left = NewAvlNode(5, "x")
	disordered.Height = 2
	if _, err := Verify(disordered, nil, nil); !errors.Is(err, ErrInvariantViolated) {
		t.Errorf("checkInvariants detects BST order violation: %v", err)
	}
	// Red-black: a red root is the first thing the checker rejects.
	red := &RedBlackTree[int, string]{root: &RbNode[int, string]{Key: 1, Color: RED}, size: 1}
	if err := red.CheckInvariants(); !errors.Is(err, ErrInvariantViolated) {
		t.Errorf("RB checkInvariants detects a red root: %v", err)
	}
}
