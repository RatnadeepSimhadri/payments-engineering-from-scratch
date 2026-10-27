// Lesson: phases/03-systems-data-structures/03-balanced-trees/docs/en.md
// Balanced Trees: AVL and Red-Black
// Run:  go run .
// Test: go test ./...
// Sources: Adelson-Velsky & Landis (1962); Guibas & Sedgewick (1978), "A Dichromatic Framework for Balanced Trees";
// Cormen, Leiserson, Rivest, Stein, Introduction to Algorithms, ch. 13 (RB-INSERT-FIXUP). java.util.TreeMap source.
// Go 1.22+, standard library only. Same design as the Java and Python versions.
package main

import (
	"cmp"
	"errors"
	"fmt"
	"math"
	"math/bits"
	"strings"
)

// ErrInvariantViolated is returned by the invariant checkers where the Java throws IllegalStateException.
var ErrInvariantViolated = errors.New("invariant violated")

// ---------------------------------------------------------------- NaiveBst ----

// NaiveBst is an unbalanced binary search tree, kept only to show what balancing prevents.
// Inserting sorted keys degenerates it into a linked list of height n.
type NaiveBst[K cmp.Ordered, V any] struct {
	root *naiveNode[K, V]
	size int
}

type naiveNode[K cmp.Ordered, V any] struct {
	key         K
	value       V
	left, right *naiveNode[K, V]
}

func (t *NaiveBst[K, V]) Put(key K, value V) {
	if t.root == nil {
		t.root = &naiveNode[K, V]{key: key, value: value}
		t.size++
		return
	}
	cur := t.root
	for {
		c := cmp.Compare(key, cur.key)
		if c == 0 {
			cur.value = value
			return
		}
		next := cur.right
		if c < 0 {
			next = cur.left
		}
		if next == nil {
			if c < 0 {
				cur.left = &naiveNode[K, V]{key: key, value: value}
			} else {
				cur.right = &naiveNode[K, V]{key: key, value: value}
			}
			t.size++
			return
		}
		cur = next
	}
}

func (t *NaiveBst[K, V]) Size() int { return t.size }

func (t *NaiveBst[K, V]) Height() int { return naiveHeight(t.root) }

func naiveHeight[K cmp.Ordered, V any](n *naiveNode[K, V]) int {
	if n == nil {
		return 0
	}
	return 1 + max(naiveHeight(n.left), naiveHeight(n.right))
}

// ---------------------------------------------------------------- AvlTree ----

// AvlTree: for every node, |height(left) - height(right)| <= 1. Height is stored in each node and
// recomputed on the way back up from every insert or delete; at most two rotations restore the
// invariant after an insert, and O(log n) after a delete.
type AvlTree[K cmp.Ordered, V any] struct {
	root      *AvlNode[K, V]
	size      int
	rotations int64
}

// AvlNode is exported so the tests can hand-build a corrupted tree for the invariant checker.
type AvlNode[K cmp.Ordered, V any] struct {
	Key         K
	Value       V
	Height      int
	Left, Right *AvlNode[K, V]
}

func NewAvlNode[K cmp.Ordered, V any](key K, value V) *AvlNode[K, V] {
	return &AvlNode[K, V]{Key: key, Value: value, Height: 1}
}

func avlHeight[K cmp.Ordered, V any](n *AvlNode[K, V]) int {
	if n == nil {
		return 0
	}
	return n.Height
}

func BalanceFactor[K cmp.Ordered, V any](n *AvlNode[K, V]) int {
	if n == nil {
		return 0
	}
	return avlHeight(n.Left) - avlHeight(n.Right)
}

func update[K cmp.Ordered, V any](n *AvlNode[K, V]) {
	n.Height = 1 + max(avlHeight(n.Left), avlHeight(n.Right))
}

// RotateRight around y: y's left child x becomes the subtree root; x's right subtree moves under y.
func (t *AvlTree[K, V]) RotateRight(y *AvlNode[K, V]) *AvlNode[K, V] {
	x := y.Left
	y.Left = x.Right
	x.Right = y
	update(y)
	update(x)
	t.rotations++
	return x
}

// RotateLeft around x: x's right child y becomes the subtree root; y's left subtree moves under x.
func (t *AvlTree[K, V]) RotateLeft(x *AvlNode[K, V]) *AvlNode[K, V] {
	y := x.Right
	x.Right = y.Left
	y.Left = x
	update(x)
	update(y)
	t.rotations++
	return y
}

// rebalance restores |balance| <= 1 at n. Four cases: LL and RR need one rotation, LR and RL need two.
func (t *AvlTree[K, V]) rebalance(n *AvlNode[K, V]) *AvlNode[K, V] {
	update(n)
	bf := BalanceFactor(n)
	if bf > 1 {
		if BalanceFactor(n.Left) < 0 {
			n.Left = t.RotateLeft(n.Left) // LR: straighten first
		}
		return t.RotateRight(n) // LL
	}
	if bf < -1 {
		if BalanceFactor(n.Right) > 0 {
			n.Right = t.RotateRight(n.Right) // RL: straighten first
		}
		return t.RotateLeft(n) // RR
	}
	return n
}

func (t *AvlTree[K, V]) Put(key K, value V) { t.root = t.put(t.root, key, value) }

func (t *AvlTree[K, V]) put(n *AvlNode[K, V], key K, value V) *AvlNode[K, V] {
	if n == nil {
		t.size++
		return NewAvlNode(key, value)
	}
	c := cmp.Compare(key, n.Key)
	switch {
	case c < 0:
		n.Left = t.put(n.Left, key, value)
	case c > 0:
		n.Right = t.put(n.Right, key, value)
	default:
		n.Value = value
		return n
	}
	return t.rebalance(n)
}

// Get returns the value and whether the key is present (Java returns null for a missing key).
func (t *AvlTree[K, V]) Get(key K) (V, bool) {
	cur := t.root
	for cur != nil {
		c := cmp.Compare(key, cur.Key)
		if c == 0 {
			return cur.Value, true
		}
		if c < 0 {
			cur = cur.Left
		} else {
			cur = cur.Right
		}
	}
	var zero V
	return zero, false
}

func (t *AvlTree[K, V]) ContainsKey(key K) bool {
	_, ok := t.Get(key)
	return ok
}

func (t *AvlTree[K, V]) Remove(key K) { t.root = t.remove(t.root, key) }

func (t *AvlTree[K, V]) remove(n *AvlNode[K, V], key K) *AvlNode[K, V] {
	if n == nil {
		return nil
	}
	c := cmp.Compare(key, n.Key)
	switch {
	case c < 0:
		n.Left = t.remove(n.Left, key)
	case c > 0:
		n.Right = t.remove(n.Right, key)
	default:
		if n.Left == nil || n.Right == nil { // zero or one child: splice out
			t.size--
			if n.Left == nil {
				return n.Right
			}
			return n.Left
		}
		successor := n.Right // two children: replace with in-order successor
		for successor.Left != nil {
			successor = successor.Left
		}
		n.Key = successor.Key
		n.Value = successor.Value
		n.Right = t.remove(n.Right, successor.Key) // the successor has no left child, so this splices
	}
	return t.rebalance(n)
}

// CeilingKey is the smallest key >= key, or false: the operation a hash map cannot do.
func (t *AvlTree[K, V]) CeilingKey(key K) (K, bool) {
	cur := t.root
	var best K
	found := false
	for cur != nil {
		c := cmp.Compare(key, cur.Key)
		if c == 0 {
			return cur.Key, true
		}
		if c < 0 {
			best, found = cur.Key, true
			cur = cur.Left
		} else {
			cur = cur.Right
		}
	}
	return best, found
}

// Range returns the keys in [lo, hi], ascending, visiting only the subtrees that can contain them.
func (t *AvlTree[K, V]) Range(lo, hi K) []K {
	out := []K{}
	t.rangeInto(t.root, lo, hi, &out)
	return out
}

func (t *AvlTree[K, V]) rangeInto(n *AvlNode[K, V], lo, hi K, out *[]K) {
	if n == nil {
		return
	}
	if lo < n.Key {
		t.rangeInto(n.Left, lo, hi, out)
	}
	if lo <= n.Key && hi >= n.Key {
		*out = append(*out, n.Key)
	}
	if hi > n.Key {
		t.rangeInto(n.Right, lo, hi, out)
	}
}

func (t *AvlTree[K, V]) Keys() []K {
	out := make([]K, 0, t.size)
	avlInorder(t.root, &out)
	return out
}

func avlInorder[K cmp.Ordered, V any](n *AvlNode[K, V], out *[]K) {
	if n == nil {
		return
	}
	avlInorder(n.Left, out)
	*out = append(*out, n.Key)
	avlInorder(n.Right, out)
}

func (t *AvlTree[K, V]) Size() int        { return t.size }
func (t *AvlTree[K, V]) Height() int      { return avlHeight(t.root) }
func (t *AvlTree[K, V]) Rotations() int64 { return t.rotations }

// AvlHeightBound is the upper bound from the AVL analysis: height < 1.4405 * log2(n + 2) - 0.3277.
func AvlHeightBound(n int) float64 { return 1.4405*math.Log(float64(n+2))/math.Log(2) - 0.3277 }

// CheckInvariants: every node in BST order, stored height correct, |balance| <= 1. Returns the first violation.
func (t *AvlTree[K, V]) CheckInvariants() error {
	_, err := Verify(t.root, nil, nil)
	return err
}

// Verify checks the subtree at n with exclusive bounds lo and hi (nil means unbounded) and returns its height.
func Verify[K cmp.Ordered, V any](n *AvlNode[K, V], lo, hi *K) (int, error) {
	if n == nil {
		return 0, nil
	}
	if lo != nil && n.Key <= *lo {
		return 0, fmt.Errorf("%w: BST order violated at %v", ErrInvariantViolated, n.Key)
	}
	if hi != nil && n.Key >= *hi {
		return 0, fmt.Errorf("%w: BST order violated at %v", ErrInvariantViolated, n.Key)
	}
	hl, err := Verify(n.Left, lo, &n.Key)
	if err != nil {
		return 0, err
	}
	hr, err := Verify(n.Right, &n.Key, hi)
	if err != nil {
		return 0, err
	}
	if n.Height != 1+max(hl, hr) {
		return 0, fmt.Errorf("%w: stale height at %v", ErrInvariantViolated, n.Key)
	}
	if abs(hl-hr) > 1 {
		return 0, fmt.Errorf("%w: AVL balance violated at %v", ErrInvariantViolated, n.Key)
	}
	return n.Height, nil
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// ---------------------------------------------------------------- RedBlackTree ----

// RedBlackTree with the CLRS insert fix-up. Invariants: every node is red or black; the root is
// black; nil leaves are black; a red node has no red child; every path from a node to its nil
// descendants has the same number of black nodes. Height <= 2 * log2(n + 1).
type RedBlackTree[K cmp.Ordered, V any] struct {
	root        *RbNode[K, V]
	size        int
	rotations   int64
	recolorings int64
}

const (
	RED   = true
	BLACK = false
)

type RbNode[K cmp.Ordered, V any] struct {
	Key                 K
	Value               V
	Color               bool
	Left, Right, Parent *RbNode[K, V]
}

func isRed[K cmp.Ordered, V any](n *RbNode[K, V]) bool { return n != nil && n.Color == RED }

func (t *RedBlackTree[K, V]) rotateLeft(x *RbNode[K, V]) {
	y := x.Right
	x.Right = y.Left
	if y.Left != nil {
		y.Left.Parent = x
	}
	y.Parent = x.Parent
	switch {
	case x.Parent == nil:
		t.root = y
	case x == x.Parent.Left:
		x.Parent.Left = y
	default:
		x.Parent.Right = y
	}
	y.Left = x
	x.Parent = y
	t.rotations++
}

func (t *RedBlackTree[K, V]) rotateRight(y *RbNode[K, V]) {
	x := y.Left
	y.Left = x.Right
	if x.Right != nil {
		x.Right.Parent = y
	}
	x.Parent = y.Parent
	switch {
	case y.Parent == nil:
		t.root = x
	case y == y.Parent.Right:
		y.Parent.Right = x
	default:
		y.Parent.Left = x
	}
	x.Right = y
	y.Parent = x
	t.rotations++
}

func (t *RedBlackTree[K, V]) Put(key K, value V) {
	var parent *RbNode[K, V]
	cur := t.root
	c := 0
	for cur != nil {
		parent = cur
		c = cmp.Compare(key, cur.Key)
		if c == 0 {
			cur.Value = value
			return
		}
		if c < 0 {
			cur = cur.Left
		} else {
			cur = cur.Right
		}
	}
	z := &RbNode[K, V]{Key: key, Value: value, Color: RED, Parent: parent}
	switch {
	case parent == nil:
		t.root = z
	case c < 0:
		parent.Left = z
	default:
		parent.Right = z
	}
	t.size++
	t.insertFixup(z)
}

// insertFixup: z is red and may have a red parent. Walk up while that is true. Three cases on each side:
// (1) uncle red: recolor and move up; (2) z is an inner grandchild: rotate parent to make (3);
// (3) z is an outer grandchild: recolor parent and grandparent, rotate grandparent. Done.
func (t *RedBlackTree[K, V]) insertFixup(z *RbNode[K, V]) {
	for isRed(z.Parent) {
		p, g := z.Parent, z.Parent.Parent // g exists: a red parent is never the root
		if p == g.Left {
			uncle := g.Right
			if isRed(uncle) { // case 1
				p.Color, uncle.Color, g.Color = BLACK, BLACK, RED
				t.recolorings += 3
				z = g
			} else {
				if z == p.Right { // case 2 -> 3
					z = p
					t.rotateLeft(z)
					p = z.Parent
				}
				p.Color, g.Color = BLACK, RED // case 3
				t.recolorings += 2
				t.rotateRight(g)
			}
		} else {
			uncle := g.Left
			if isRed(uncle) {
				p.Color, uncle.Color, g.Color = BLACK, BLACK, RED
				t.recolorings += 3
				z = g
			} else {
				if z == p.Left {
					z = p
					t.rotateRight(z)
					p = z.Parent
				}
				p.Color, g.Color = BLACK, RED
				t.recolorings += 2
				t.rotateLeft(g)
			}
		}
	}
	if t.root.Color == RED {
		t.root.Color = BLACK
		t.recolorings++
	}
}

func (t *RedBlackTree[K, V]) Get(key K) (V, bool) {
	cur := t.root
	for cur != nil {
		c := cmp.Compare(key, cur.Key)
		if c == 0 {
			return cur.Value, true
		}
		if c < 0 {
			cur = cur.Left
		} else {
			cur = cur.Right
		}
	}
	var zero V
	return zero, false
}

func (t *RedBlackTree[K, V]) Keys() []K {
	out := make([]K, 0, t.size)
	rbInorder(t.root, &out)
	return out
}

func rbInorder[K cmp.Ordered, V any](n *RbNode[K, V], out *[]K) {
	if n == nil {
		return
	}
	rbInorder(n.Left, out)
	*out = append(*out, n.Key)
	rbInorder(n.Right, out)
}

func (t *RedBlackTree[K, V]) Size() int          { return t.size }
func (t *RedBlackTree[K, V]) Rotations() int64   { return t.rotations }
func (t *RedBlackTree[K, V]) Recolorings() int64 { return t.recolorings }

func (t *RedBlackTree[K, V]) Height() int { return rbHeight(t.root) }

func rbHeight[K cmp.Ordered, V any](n *RbNode[K, V]) int {
	if n == nil {
		return 0
	}
	return 1 + max(rbHeight(n.Left), rbHeight(n.Right))
}

// BlackHeight is the number of black nodes on any root-to-nil path (they are all equal when the tree is valid).
func (t *RedBlackTree[K, V]) BlackHeight() int {
	h := 0
	for n := t.root; n != nil; n = n.Left {
		if n.Color == BLACK {
			h++
		}
	}
	return h
}

// RbHeightBound is the upper bound from the red-black analysis: height <= 2 * log2(n + 1).
func RbHeightBound(n int) float64 { return 2.0 * math.Log(float64(n+1)) / math.Log(2) }

// CheckInvariants: BST order, root black, no red-red edge, equal black height on every path. Returns the first violation.
func (t *RedBlackTree[K, V]) CheckInvariants() error {
	if isRed(t.root) {
		return fmt.Errorf("%w: root is red", ErrInvariantViolated)
	}
	_, err := t.verify(t.root, nil, nil)
	return err
}

func (t *RedBlackTree[K, V]) verify(n *RbNode[K, V], lo, hi *K) (int, error) {
	if n == nil {
		return 1, nil // nil leaves count as one black
	}
	if lo != nil && n.Key <= *lo {
		return 0, fmt.Errorf("%w: BST order violated at %v", ErrInvariantViolated, n.Key)
	}
	if hi != nil && n.Key >= *hi {
		return 0, fmt.Errorf("%w: BST order violated at %v", ErrInvariantViolated, n.Key)
	}
	if isRed(n) && (isRed(n.Left) || isRed(n.Right)) {
		return 0, fmt.Errorf("%w: red node with red child at %v", ErrInvariantViolated, n.Key)
	}
	if n.Left != nil && n.Left.Parent != n {
		return 0, fmt.Errorf("%w: broken parent link at %v", ErrInvariantViolated, n.Left.Key)
	}
	if n.Right != nil && n.Right.Parent != n {
		return 0, fmt.Errorf("%w: broken parent link at %v", ErrInvariantViolated, n.Right.Key)
	}
	bl, err := t.verify(n.Left, lo, &n.Key)
	if err != nil {
		return 0, err
	}
	br, err := t.verify(n.Right, &n.Key, hi)
	if err != nil {
		return 0, err
	}
	if bl != br {
		return 0, fmt.Errorf("%w: black height differs at %v: %d vs %d", ErrInvariantViolated, n.Key, bl, br)
	}
	if n.Color == BLACK {
		return bl + 1, nil
	}
	return bl, nil
}

// ---------------------------------------------------------------- Demo ----

func log2(n int) int { return 32 - bits.LeadingZeros32(uint32(n)) }

func mustHold(err error) {
	if err != nil {
		panic(err)
	}
}

func main() {
	n := 100_000

	fmt.Println("== Sorted inserts: what a plain BST does with timestamps ==")
	naive := &NaiveBst[int, string]{}
	for i := 0; i < 2_000; i++ {
		naive.Put(i, "auth")
	}
	fmt.Printf("naive BST, 2000 sorted keys: height=%d (a linked list; log2(2000)=%d)\n", naive.Height(), log2(2000))

	fmt.Println()
	fmt.Printf("== AVL vs red-black on %d sorted keys (expiry timestamps arriving in order) ==\n", n)
	avl := &AvlTree[int, string]{}
	rb := &RedBlackTree[int, string]{}
	for i := 0; i < n; i++ {
		avl.Put(i, "auth")
		rb.Put(i, "auth")
	}
	mustHold(avl.CheckInvariants())
	mustHold(rb.CheckInvariants())
	fmt.Printf("AVL        height=%d  bound=%.1f  rotations=%d\n", avl.Height(), AvlHeightBound(n), avl.Rotations())
	fmt.Printf("red-black  height=%d  bound=%.1f  rotations=%d  recolorings=%d  black-height=%d\n",
		rb.Height(), RbHeightBound(n), rb.Rotations(), rb.Recolorings(), rb.BlackHeight())

	fmt.Println()
	fmt.Println("== Same keys, random order (seeded) ==")
	shuffled := make([]int, n)
	for i := range shuffled {
		shuffled[i] = i
	}
	NewJavaRandom(2024).Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
	avl2 := &AvlTree[int, string]{}
	rb2 := &RedBlackTree[int, string]{}
	for _, k := range shuffled {
		avl2.Put(k, "auth")
		rb2.Put(k, "auth")
	}
	mustHold(avl2.CheckInvariants())
	mustHold(rb2.CheckInvariants())
	fmt.Printf("AVL        height=%d  rotations=%d\n", avl2.Height(), avl2.Rotations())
	fmt.Printf("red-black  height=%d  rotations=%d  recolorings=%d\n", rb2.Height(), rb2.Rotations(), rb2.Recolorings())

	fmt.Println()
	fmt.Println("== The operation a hash map cannot do: expiries between two instants ==")
	expiries := &AvlTree[int64, string]{}
	t0 := int64(1_700_000_000_000)
	rng := NewJavaRandom(7)
	for i := 0; i < 20; i++ {
		expiries.Put(t0+int64(rng.NextIntN(60_000)), fmt.Sprintf("auth_%d", i))
	}
	offsets := []string{}
	for _, t := range expiries.Range(t0+10_000, t0+19_999) {
		offsets = append(offsets, fmt.Sprint(t-t0))
	}
	fmt.Println("expiries in [t0+10s, t0+20s): [" + strings.Join(offsets, ", ") + "]")
	first, _ := expiries.CeilingKey(t0 + 30_000)
	fmt.Printf("first expiry at or after t0+30s: +%d ms\n", first-t0)

	fmt.Println()
	fmt.Println("== Deletes keep the AVL invariant ==")
	for i := 0; i < n; i += 2 {
		avl.Remove(i)
	}
	mustHold(avl.CheckInvariants())
	fmt.Printf("after removing every even key: size=%d height=%d, invariants hold\n", avl.Size(), avl.Height())
}
