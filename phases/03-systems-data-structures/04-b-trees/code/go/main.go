// Lesson: phases/03-systems-data-structures/04-b-trees/docs/en.md
// B-Trees and Why Databases Love Them
// Run:  go run .
// Test: go test ./...
// Sources: Bayer & McCreight (1972), "Organization and Maintenance of Large Ordered Indexes"; Comer (1979),
// "The Ubiquitous B-Tree"; Knuth TAOCP Vol. 3 Sec. 6.2.4; PostgreSQL docs, "B-Tree Indexes" (src/backend/access/nbtree/README).
// Go 1.22+, standard library only. Same design as the Java and Python versions.
package main

import (
	"cmp"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
)

var (
	// ErrInvalidArgument is returned where the Java throws IllegalArgumentException.
	ErrInvalidArgument = errors.New("invalid argument")
	// ErrInvariantViolated is returned by CheckInvariants where the Java throws IllegalStateException.
	ErrInvariantViolated = errors.New("invariant violated")
)

// PageStats counts page-level I/O. Every node is one page; a search touches one page per level.
// The counters are what a database's buffer manager would report, and they are the reason B-trees exist.
type PageStats struct {
	Reads, Writes, Splits, Merges int64
}

func (s *PageStats) Reset() { s.Reads, s.Writes, s.Splits, s.Merges = 0, 0, 0, 0 }

func (s *PageStats) String() string {
	return fmt.Sprintf("reads=%d writes=%d splits=%d merges=%d", s.Reads, s.Writes, s.Splits, s.Merges)
}

// Node is one page: sorted keys, their values, and (for an internal node) keys+1 children.
type Node[K cmp.Ordered, V any] struct {
	Keys     []K
	Values   []V
	Children []*Node[K, V] // empty for a leaf
}

func (n *Node[K, V]) IsLeaf() bool  { return len(n.Children) == 0 }
func (n *Node[K, V]) KeyCount() int { return len(n.Keys) }

// BTree of a configurable order (maximum number of children). A node holds between MinKeys and
// MaxKeys keys, except the root which may hold fewer; every leaf is at the same depth; keys in
// a node are sorted and separate the key ranges of the children around them.
type BTree[K cmp.Ordered, V any] struct {
	Order   int // max children per node
	MaxKeys int // order - 1
	MinKeys int // ceil(order / 2) - 1: at least half full
	Stats   *PageStats
	root    *Node[K, V]
	size    int
}

func NewBTree[K cmp.Ordered, V any](order int) (*BTree[K, V], error) {
	if order < 3 {
		return nil, fmt.Errorf("%w: order must be at least 3: %d", ErrInvalidArgument, order)
	}
	return &BTree[K, V]{Order: order, MaxKeys: order - 1, MinKeys: (order+1)/2 - 1, Stats: &PageStats{}, root: &Node[K, V]{}}, nil
}

// ----- search ------------------------------------------------------------------------------

// LowerBound is the index of the first key >= key in the node (binary search within the page).
func LowerBound[K cmp.Ordered, V any](n *Node[K, V], key K) int {
	lo, hi := 0, n.KeyCount()
	for lo < hi {
		mid := int(uint(lo+hi) >> 1)
		if n.Keys[mid] < key {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo
}

// Get returns the value and whether the key is present (Java returns null for a missing key).
func (t *BTree[K, V]) Get(key K) (V, bool) {
	n := t.root
	for {
		t.Stats.Reads++
		i := LowerBound(n, key)
		if i < n.KeyCount() && n.Keys[i] == key {
			return n.Values[i], true
		}
		if n.IsLeaf() {
			var zero V
			return zero, false
		}
		n = n.Children[i]
	}
}

func (t *BTree[K, V]) ContainsKey(key K) bool {
	_, ok := t.Get(key)
	return ok
}

// ----- insert ------------------------------------------------------------------------------

// split is the median key promoted to the parent together with the new right sibling.
type split[K cmp.Ordered, V any] struct {
	key   K
	value V
	right *Node[K, V]
}

func (t *BTree[K, V]) Put(key K, value V) {
	s := t.insert(t.root, key, value)
	if s != nil { // the root overflowed: the tree grows by one level
		newRoot := &Node[K, V]{Keys: []K{s.key}, Values: []V{s.value}, Children: []*Node[K, V]{t.root, s.right}}
		t.root = newRoot
		t.Stats.Writes++
	}
}

// insert into the subtree at n; returns a split if n overflowed and had to be divided.
func (t *BTree[K, V]) insert(n *Node[K, V], key K, value V) *split[K, V] {
	t.Stats.Reads++
	i := LowerBound(n, key)
	if i < n.KeyCount() && n.Keys[i] == key {
		n.Values[i] = value
		t.Stats.Writes++
		return nil
	}
	if n.IsLeaf() {
		n.Keys = slices.Insert(n.Keys, i, key)
		n.Values = slices.Insert(n.Values, i, value)
		t.size++
	} else {
		childSplit := t.insert(n.Children[i], key, value)
		if childSplit == nil {
			return nil
		}
		n.Keys = slices.Insert(n.Keys, i, childSplit.key)
		n.Values = slices.Insert(n.Values, i, childSplit.value)
		n.Children = slices.Insert(n.Children, i+1, childSplit.right)
	}
	t.Stats.Writes++
	if n.KeyCount() > t.MaxKeys {
		return t.split(n)
	}
	return nil
}

// split divides an overflowing node at its median. Left keeps the lower half, right takes the upper half.
// slices.Clone gives the right page its own backing array so the two pages never alias.
func (t *BTree[K, V]) split(n *Node[K, V]) *split[K, V] {
	mid := n.KeyCount() / 2
	midKey := n.Keys[mid]
	midValue := n.Values[mid]
	right := &Node[K, V]{
		Keys:   slices.Clone(n.Keys[mid+1:]),
		Values: slices.Clone(n.Values[mid+1:]),
	}
	n.Keys = n.Keys[:mid]
	n.Values = n.Values[:mid]
	if !n.IsLeaf() {
		right.Children = slices.Clone(n.Children[mid+1:])
		n.Children = n.Children[:mid+1]
	}
	t.Stats.Splits++
	t.Stats.Writes++ // the new right page
	return &split[K, V]{midKey, midValue, right}
}

// ----- delete ------------------------------------------------------------------------------

func (t *BTree[K, V]) Remove(key K) {
	t.remove(t.root, key)
	if t.root.KeyCount() == 0 && !t.root.IsLeaf() { // the root emptied: the tree shrinks by one level
		t.root = t.root.Children[0]
	}
}

func (t *BTree[K, V]) remove(n *Node[K, V], key K) {
	t.Stats.Reads++
	i := LowerBound(n, key)
	found := i < n.KeyCount() && n.Keys[i] == key
	if n.IsLeaf() {
		if !found {
			return
		}
		n.Keys = slices.Delete(n.Keys, i, i+1)
		n.Values = slices.Delete(n.Values, i, i+1)
		t.size--
		t.Stats.Writes++
		return
	}
	if found {
		// Replace with the in-order predecessor (largest key in the left child), then delete that from the child.
		pred := n.Children[i]
		for !pred.IsLeaf() {
			t.Stats.Reads++
			pred = pred.Children[len(pred.Children)-1]
		}
		t.Stats.Reads++
		predKey := pred.Keys[pred.KeyCount()-1]
		predValue := pred.Values[pred.KeyCount()-1]
		n.Keys[i] = predKey
		n.Values[i] = predValue
		t.Stats.Writes++
		t.remove(n.Children[i], predKey)
	} else {
		t.remove(n.Children[i], key)
	}
	if n.Children[i].KeyCount() < t.MinKeys {
		t.rebalance(n, i)
	}
}

// rebalance: child i of parent underflowed: borrow from a sibling that can spare a key, else merge with one.
func (t *BTree[K, V]) rebalance(parent *Node[K, V], i int) {
	child := parent.Children[i]
	switch {
	case i > 0 && parent.Children[i-1].KeyCount() > t.MinKeys:
		left := parent.Children[i-1] // rotate one key through the parent, from the left
		child.Keys = slices.Insert(child.Keys, 0, parent.Keys[i-1])
		child.Values = slices.Insert(child.Values, 0, parent.Values[i-1])
		last := left.KeyCount() - 1
		parent.Keys[i-1] = left.Keys[last]
		parent.Values[i-1] = left.Values[last]
		left.Keys = left.Keys[:last]
		left.Values = left.Values[:last]
		if !left.IsLeaf() {
			child.Children = slices.Insert(child.Children, 0, left.Children[len(left.Children)-1])
			left.Children = left.Children[:len(left.Children)-1]
		}
		t.Stats.Writes += 3
	case i < len(parent.Children)-1 && parent.Children[i+1].KeyCount() > t.MinKeys:
		right := parent.Children[i+1] // rotate one key through the parent, from the right
		child.Keys = append(child.Keys, parent.Keys[i])
		child.Values = append(child.Values, parent.Values[i])
		parent.Keys[i] = right.Keys[0]
		parent.Values[i] = right.Values[0]
		right.Keys = slices.Delete(right.Keys, 0, 1)
		right.Values = slices.Delete(right.Values, 0, 1)
		if !right.IsLeaf() {
			child.Children = append(child.Children, right.Children[0])
			right.Children = slices.Delete(right.Children, 0, 1)
		}
		t.Stats.Writes += 3
	default:
		leftIndex := i // merge child with a sibling around the separator
		if i > 0 {
			leftIndex = i - 1
		}
		left := parent.Children[leftIndex]
		right := parent.Children[leftIndex+1]
		left.Keys = append(left.Keys, parent.Keys[leftIndex])
		left.Values = append(left.Values, parent.Values[leftIndex])
		parent.Keys = slices.Delete(parent.Keys, leftIndex, leftIndex+1)
		parent.Values = slices.Delete(parent.Values, leftIndex, leftIndex+1)
		left.Keys = append(left.Keys, right.Keys...)
		left.Values = append(left.Values, right.Values...)
		left.Children = append(left.Children, right.Children...)
		parent.Children = slices.Delete(parent.Children, leftIndex+1, leftIndex+2)
		t.Stats.Merges++
		t.Stats.Writes += 2
	}
}

// ----- range scan and traversal -------------------------------------------------------------

// Range returns the keys in [lo, hi] ascending. Descends only into children whose range can intersect.
func (t *BTree[K, V]) Range(lo, hi K) []K {
	out := []K{}
	t.rangeInto(t.root, lo, hi, &out)
	return out
}

// rangeInto stops at the first key above hi; the recursive calls below it have already stopped for the same reason.
func (t *BTree[K, V]) rangeInto(n *Node[K, V], lo, hi K, out *[]K) {
	t.Stats.Reads++
	i := LowerBound(n, lo)
	if !n.IsLeaf() {
		t.rangeInto(n.Children[i], lo, hi, out)
	}
	for ; i < n.KeyCount(); i++ {
		k := n.Keys[i]
		if k > hi {
			return
		}
		*out = append(*out, k)
		if !n.IsLeaf() {
			t.rangeInto(n.Children[i+1], lo, hi, out)
		}
	}
}

func (t *BTree[K, V]) Keys() []K {
	out := make([]K, 0, t.size)
	t.collect(t.root, &out)
	return out
}

func (t *BTree[K, V]) collect(n *Node[K, V], out *[]K) {
	for i := 0; i < n.KeyCount(); i++ {
		if !n.IsLeaf() {
			t.collect(n.Children[i], out)
		}
		*out = append(*out, n.Keys[i])
	}
	if !n.IsLeaf() {
		t.collect(n.Children[len(n.Children)-1], out)
	}
}

// ----- shape -------------------------------------------------------------------------------

func (t *BTree[K, V]) Size() int { return t.size }

// Height is the number of levels, counting the root. Every leaf is at this depth.
func (t *BTree[K, V]) Height() int {
	h := 0
	for n := t.root; ; n = n.Children[0] {
		h++
		if n.IsLeaf() {
			return h
		}
	}
}

func (t *BTree[K, V]) NodeCount() int { return nodeCount(t.root) }

func nodeCount[K cmp.Ordered, V any](n *Node[K, V]) int {
	c := 1
	for _, ch := range n.Children {
		c += nodeCount(ch)
	}
	return c
}

// HeightBound is the upper bound on height from the paper: 1 + log_(ceil(order/2)) ((n + 1) / 2).
func HeightBound(order, n int) float64 {
	halfFanOut := (order + 1) / 2
	return 1 + math.Log(float64(n+1)/2.0)/math.Log(float64(halfFanOut))
}

// CheckInvariants: sorted keys, occupancy bounds, separator ranges, equal leaf depth. Returns the first violation.
func (t *BTree[K, V]) CheckInvariants() error {
	leafDepth := -1
	return t.verify(t.root, nil, nil, 0, &leafDepth)
}

func (t *BTree[K, V]) verify(n *Node[K, V], lo, hi *K, depth int, leafDepth *int) error {
	if n != t.root && n.KeyCount() < t.MinKeys {
		return fmt.Errorf("%w: underfull node: %v", ErrInvariantViolated, n.Keys)
	}
	if n.KeyCount() > t.MaxKeys {
		return fmt.Errorf("%w: overfull node: %v", ErrInvariantViolated, n.Keys)
	}
	if n != t.root && n.KeyCount() == 0 {
		return fmt.Errorf("%w: empty non-root node", ErrInvariantViolated)
	}
	for i, k := range n.Keys {
		if i > 0 && n.Keys[i-1] >= k {
			return fmt.Errorf("%w: keys not sorted: %v", ErrInvariantViolated, n.Keys)
		}
		if lo != nil && k <= *lo {
			return fmt.Errorf("%w: key %v below separator %v", ErrInvariantViolated, k, *lo)
		}
		if hi != nil && k >= *hi {
			return fmt.Errorf("%w: key %v above separator %v", ErrInvariantViolated, k, *hi)
		}
	}
	if n.IsLeaf() {
		if *leafDepth == -1 {
			*leafDepth = depth
		} else if *leafDepth != depth {
			return fmt.Errorf("%w: leaves at different depths: %d and %d", ErrInvariantViolated, *leafDepth, depth)
		}
		return nil
	}
	if len(n.Children) != n.KeyCount()+1 {
		return fmt.Errorf("%w: children != keys + 1 at %v", ErrInvariantViolated, n.Keys)
	}
	for i, child := range n.Children {
		childLo, childHi := lo, hi
		if i > 0 {
			childLo = &n.Keys[i-1]
		}
		if i < n.KeyCount() {
			childHi = &n.Keys[i]
		}
		if err := t.verify(child, childLo, childHi, depth+1, leafDepth); err != nil {
			return err
		}
	}
	return nil
}

// ---------------------------------------------------------------- Demo ----

func mustHold(err error) {
	if err != nil {
		panic(err)
	}
}

func must[T any](v T, err error) T {
	mustHold(err)
	return v
}

func joinInts[K cmp.Ordered](xs []K) string {
	parts := make([]string, len(xs))
	for i, x := range xs {
		parts[i] = fmt.Sprint(x)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func main() {
	fmt.Println("== Fan-out decides height: 10 million keys, one page read per level ==")
	fmt.Printf("binary tree: height ~ %.1f (one node per level: 23 dependent cache misses or disk reads)\n",
		math.Log(10_000_000)/math.Log(2))
	for _, order := range []int{8, 64, 400} {
		fmt.Printf("order %4d:  height <= %.1f\n", order, HeightBound(order, 10_000_000))
	}
	fmt.Println("PostgreSQL: 8 KB page, ~400 bigint keys per page -> 4 levels index billions of rows, and the top 3 live in RAM.")

	fmt.Println()
	fmt.Println("== A small tree you can draw: order 4, keys 1..10 ==")
	small := must(NewBTree[int, string](4))
	for k := 1; k <= 10; k++ {
		small.Put(k, fmt.Sprintf("v%d", k))
	}
	mustHold(small.CheckInvariants())
	fmt.Printf("height=%d nodes=%d keys=%s\n", small.Height(), small.NodeCount(), joinInts(small.Keys()))
	fmt.Println("stats after 10 inserts: " + small.Stats.String())

	fmt.Println()
	fmt.Println("== 200,000 payment ids, order 64: sequential vs random keys (UUIDv4-style) ==")
	n := 200_000
	sequential := must(NewBTree[int64, int](64))
	for id := int64(1); id <= int64(n); id++ {
		sequential.Put(id, 1)
	}
	mustHold(sequential.CheckInvariants())
	fmt.Printf("sequential: height=%d nodes=%d %s\n", sequential.Height(), sequential.NodeCount(), sequential.Stats)

	rng := NewJavaRandom(42)
	random := must(NewBTree[int64, int](64))
	for i := 0; i < n; i++ {
		random.Put(rng.NextLong()&math.MaxInt64, 1)
	}
	mustHold(random.CheckInvariants())
	fmt.Printf("random:     height=%d nodes=%d %s\n", random.Height(), random.NodeCount(), random.Stats)
	fmt.Printf("occupancy: sequential ~%.0f%% (every split leaves a half-empty page behind), random ~%.0f%% (ln 2)\n",
		100.0*float64(sequential.Size())/(float64(sequential.NodeCount())*63.0), 100.0*float64(random.Size())/(float64(random.NodeCount())*63.0))
	fmt.Println("real databases special-case the rightmost split (PostgreSQL fillfactor 90%) to fix the sequential case.")

	fmt.Println()
	fmt.Println("== Point lookup and range scan cost in page reads ==")
	sequential.Stats.Reset()
	sequential.Get(123_456)
	fmt.Printf("get(123456): %d page reads (= height %d)\n", sequential.Stats.Reads, sequential.Height())
	sequential.Stats.Reset()
	scan := sequential.Range(100_000, 100_999)
	fmt.Printf("range(100000, 100999): %d keys in %d page reads\n", len(scan), sequential.Stats.Reads)

	fmt.Println()
	fmt.Println("== Deletes: borrow from a sibling or merge, leaves stay level ==")
	sequential.Stats.Reset()
	for id := int64(1); id <= int64(n); id += 2 {
		sequential.Remove(id)
	}
	mustHold(sequential.CheckInvariants())
	fmt.Printf("after removing every odd id: size=%d height=%d nodes=%d %s\n",
		sequential.Size(), sequential.Height(), sequential.NodeCount(), sequential.Stats)

	fmt.Println()
	fmt.Println("== Shrinking all the way down ==")
	ids := make([]int, 5_000)
	for i := range ids {
		ids[i] = i
	}
	NewJavaRandom(7).Shuffle(len(ids), func(i, j int) { ids[i], ids[j] = ids[j], ids[i] })
	shrink := must(NewBTree[int, string](6))
	for _, id := range ids {
		shrink.Put(id, "x")
	}
	h0 := shrink.Height()
	for _, id := range ids {
		shrink.Remove(id)
	}
	mustHold(shrink.CheckInvariants())
	fmt.Printf("5000 inserts then 5000 deletes in random order: height %d -> %d, size=%d\n", h0, shrink.Height(), shrink.Size())
}
