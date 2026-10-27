// Lesson: phases/01-complexity-and-core-structures/08-binary-search-trees/docs/en.md
// Binary Search Trees and Ordered Maps
// Run:  go run .
// Test: go test ./...
// Sources: Cormen, Leiserson, Rivest, Stein, "Introduction to Algorithms", ch. 12 (binary search trees);
//
//	Knuth, TAOCP vol. 3, 6.2.2. ISO 4217 for the Money type reused from Phase 10, Lesson 01.
//
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

// Currency is an ISO 4217 currency: code plus the number of decimal places in its minor unit (Phase 10, Lesson 01).
type Currency struct {
	Code     string
	Exponent int
}

// USD is the only currency the demo needs.
var USD = Currency{"USD", 2}

// MinorUnitsPerMajor is 10^exponent.
func (c Currency) MinorUnitsPerMajor() int64 {
	scale := int64(1)
	for i := 0; i < c.Exponent; i++ {
		scale *= 10
	}
	return scale
}

var (
	// ErrCurrencyMismatch is returned when adding two currencies.
	ErrCurrencyMismatch = errors.New("currency mismatch")
	// ErrOverflow is returned when minor units leave the int64 range.
	ErrOverflow = errors.New("amount overflows int64 minor units")
	// ErrInvalidRange is returned by RangeQuery when lo > hi.
	ErrInvalidRange = errors.New("lo must not exceed hi")
	// ErrNilComparator is returned when a tree is built without a comparator.
	ErrNilComparator = errors.New("comparator must not be nil")
)

// Money is integer minor units plus a currency. Never a float. Trimmed copy of the Phase 10 type.
type Money struct {
	MinorUnits int64
	Currency   Currency
}

// OfMinor builds a Money from minor units.
func OfMinor(minorUnits int64, currency Currency) Money { return Money{minorUnits, currency} }

func addChecked(a, b int64) (int64, error) {
	sum := a + b
	if (a > 0 && b > 0 && sum < 0) || (a < 0 && b < 0 && sum >= 0) {
		return 0, ErrOverflow
	}
	return sum, nil
}

// Plus adds two amounts of the same currency, refusing to overflow.
func (m Money) Plus(other Money) (Money, error) {
	if m.Currency != other.Currency {
		return Money{}, ErrCurrencyMismatch
	}
	sum, err := addChecked(m.MinorUnits, other.MinorUnits)
	if err != nil {
		return Money{}, err
	}
	return Money{sum, m.Currency}, nil
}

func (m Money) String() string {
	scale := m.Currency.MinorUnitsPerMajor()
	abs := m.MinorUnits
	if abs < 0 {
		abs = -abs
	}
	whole, frac := abs/scale, abs%scale
	sign := ""
	if m.MinorUnits < 0 {
		sign = "-"
	}
	if m.Currency.Exponent == 0 {
		return fmt.Sprintf("%s%d %s", sign, whole, m.Currency.Code)
	}
	return fmt.Sprintf("%s%d.%0*d %s", sign, whole, m.Currency.Exponent, frac, m.Currency.Code)
}

// TxKey is a composite key: order by timestamp, then by a sequence number so two postings in the same millisecond stay distinct.
type TxKey struct {
	TimestampMillis int64
	Sequence        int64
}

// LowestTxKey is the smallest key for a timestamp.
func LowestTxKey(timestampMillis int64) TxKey { return TxKey{timestampMillis, math.MinInt64} }

// HighestTxKey is the largest key for a timestamp.
func HighestTxKey(timestampMillis int64) TxKey { return TxKey{timestampMillis, math.MaxInt64} }

// CompareTxKey orders by timestamp then sequence: compare, never subtract.
func CompareTxKey(a, b TxKey) int {
	if c := cmp.Compare(a.TimestampMillis, b.TimestampMillis); c != 0 {
		return c
	}
	return cmp.Compare(a.Sequence, b.Sequence)
}

// Transaction is a posting in the index: the key it is filed under plus what was paid.
type Transaction struct {
	ID     string
	Key    TxKey
	Amount Money
}

// Entry is one key/value pair returned by traversals and range queries.
type Entry[K, V any] struct {
	Key   K
	Value V
}

type node[K, V any] struct {
	key         K
	value       V
	left, right *node[K, V]
}

// BinarySearchTree is an unbalanced binary search tree used as an ordered map.
//
// Invariant: for every node n, every key in n.left is strictly less than n.key and every key in
// n.right is strictly greater. Keys are unique; Put on an existing key replaces the value.
// Every operation is O(h) where h is the height, which is O(log n) for random insert order and
// O(n) for sorted insert order. The lesson measures both.
type BinarySearchTree[K, V any] struct {
	cmp         func(a, b K) int
	root        *node[K, V]
	size        int
	comparisons int64 // instrumentation: how many key comparisons the tree has done
}

// NewBinarySearchTree builds a tree over the given ordering.
func NewBinarySearchTree[K, V any](compare func(a, b K) int) (*BinarySearchTree[K, V], error) {
	if compare == nil {
		return nil, ErrNilComparator
	}
	return &BinarySearchTree[K, V]{cmp: compare}, nil
}

// NewOrderedTree is the common case: keys are a built-in ordered type.
func NewOrderedTree[K cmp.Ordered, V any]() *BinarySearchTree[K, V] {
	t, _ := NewBinarySearchTree[K, V](cmp.Compare[K])
	return t
}

func (t *BinarySearchTree[K, V]) Size() int          { return t.size }
func (t *BinarySearchTree[K, V]) IsEmpty() bool      { return t.size == 0 }
func (t *BinarySearchTree[K, V]) Comparisons() int64 { return t.comparisons }
func (t *BinarySearchTree[K, V]) ResetComparisons()  { t.comparisons = 0 }

func (t *BinarySearchTree[K, V]) compare(a, b K) int {
	t.comparisons++
	return t.cmp(a, b)
}

// Put inserts or replaces. Returns the previous value and true when the key existed. Iterative so a degenerate tree cannot overflow the stack.
func (t *BinarySearchTree[K, V]) Put(key K, value V) (V, bool) {
	var zero V
	if t.root == nil {
		t.root = &node[K, V]{key: key, value: value}
		t.size = 1
		return zero, false
	}
	cur := t.root
	for {
		c := t.compare(key, cur.key)
		if c == 0 {
			old := cur.value
			cur.value = value
			return old, true
		}
		if c < 0 {
			if cur.left == nil {
				cur.left = &node[K, V]{key: key, value: value}
				t.size++
				return zero, false
			}
			cur = cur.left
		} else {
			if cur.right == nil {
				cur.right = &node[K, V]{key: key, value: value}
				t.size++
				return zero, false
			}
			cur = cur.right
		}
	}
}

// Get is an exact lookup: follow one path from the root. O(h).
func (t *BinarySearchTree[K, V]) Get(key K) (V, bool) {
	cur := t.root
	for cur != nil {
		c := t.compare(key, cur.key)
		if c == 0 {
			return cur.value, true
		}
		if c < 0 {
			cur = cur.left
		} else {
			cur = cur.right
		}
	}
	var zero V
	return zero, false
}

// ContainsKey reports whether the key is present.
func (t *BinarySearchTree[K, V]) ContainsKey(key K) bool {
	_, ok := t.Get(key)
	return ok
}

// MinKey is the smallest key: walk left from the root.
func (t *BinarySearchTree[K, V]) MinKey() (K, bool) {
	if t.root == nil {
		var zero K
		return zero, false
	}
	n := t.root
	for n.left != nil {
		n = n.left
	}
	return n.key, true
}

// MaxKey is the largest key: walk right from the root.
func (t *BinarySearchTree[K, V]) MaxKey() (K, bool) {
	if t.root == nil {
		var zero K
		return zero, false
	}
	n := t.root
	for n.right != nil {
		n = n.right
	}
	return n.key, true
}

// FloorKey is the largest key less than or equal to the argument.
func (t *BinarySearchTree[K, V]) FloorKey(key K) (K, bool) {
	var best K
	found := false
	cur := t.root
	for cur != nil {
		c := t.compare(key, cur.key)
		if c == 0 {
			return cur.key, true
		}
		if c < 0 {
			cur = cur.left
		} else {
			best, found = cur.key, true
			cur = cur.right
		}
	}
	return best, found
}

// CeilingKey is the smallest key greater than or equal to the argument.
func (t *BinarySearchTree[K, V]) CeilingKey(key K) (K, bool) {
	var best K
	found := false
	cur := t.root
	for cur != nil {
		c := t.compare(key, cur.key)
		if c == 0 {
			return cur.key, true
		}
		if c > 0 {
			cur = cur.right
		} else {
			best, found = cur.key, true
			cur = cur.left
		}
	}
	return best, found
}

// Remove deletes by key. Returns the removed value and true, or false when absent.
// Case 0/1 child: splice the child into the parent's slot.
// Case 2 children: copy the in-order successor (min of the right subtree) into this node,
// then delete the successor node, which has at most one child by construction.
func (t *BinarySearchTree[K, V]) Remove(key K) (V, bool) {
	var parent *node[K, V]
	cur := t.root
	for cur != nil {
		c := t.compare(key, cur.key)
		if c == 0 {
			break
		}
		parent = cur
		if c < 0 {
			cur = cur.left
		} else {
			cur = cur.right
		}
	}
	if cur == nil {
		var zero V
		return zero, false
	}
	removed := cur.value

	if cur.left != nil && cur.right != nil {
		// find successor and its parent
		succParent, succ := cur, cur.right
		for succ.left != nil {
			succParent, succ = succ, succ.left
		}
		cur.key = succ.key
		cur.value = succ.value
		// detach successor: it has no left child, so splice in its right child
		if succParent == cur {
			succParent.right = succ.right
		} else {
			succParent.left = succ.right
		}
	} else {
		child := cur.left
		if child == nil {
			child = cur.right
		}
		switch {
		case parent == nil:
			t.root = child
		case parent.left == cur:
			parent.left = child
		default:
			parent.right = child
		}
	}
	t.size--
	return removed, true
}

// InOrder lists all entries in ascending key order. Iterative in-order traversal with an explicit stack. O(n).
func (t *BinarySearchTree[K, V]) InOrder() []Entry[K, V] {
	out := make([]Entry[K, V], 0, t.size)
	var stack []*node[K, V]
	cur := t.root
	for cur != nil || len(stack) > 0 {
		for cur != nil {
			stack = append(stack, cur)
			cur = cur.left
		}
		cur = stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		out = append(out, Entry[K, V]{cur.key, cur.value})
		cur = cur.right
	}
	return out
}

// RangeQuery lists all entries with lo <= key <= hi, ascending. O(h + k) where k is the number of results:
// subtrees that cannot contain a key in the range are never entered.
func (t *BinarySearchTree[K, V]) RangeQuery(lo, hi K) ([]Entry[K, V], error) {
	if t.compare(lo, hi) > 0 {
		return nil, ErrInvalidRange
	}
	out := []Entry[K, V]{}
	var stack []*node[K, V]
	cur := t.root
	for cur != nil || len(stack) > 0 {
		for cur != nil {
			stack = append(stack, cur)
			// prune: nothing in cur.left can be >= lo if cur.key < lo
			if t.compare(cur.key, lo) < 0 {
				cur = nil
			} else {
				cur = cur.left
			}
		}
		cur = stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		vsLo, vsHi := t.compare(cur.key, lo), t.compare(cur.key, hi)
		if vsHi > 0 {
			return out, nil // every later in-order key is larger still
		}
		if vsLo >= 0 {
			out = append(out, Entry[K, V]{cur.key, cur.value})
		}
		cur = cur.right
	}
	return out, nil
}

// Height is the number of edges on the longest root-to-leaf path; -1 for an empty tree. Iterative level-order walk.
func (t *BinarySearchTree[K, V]) Height() int {
	if t.root == nil {
		return -1
	}
	h := -1
	level := []*node[K, V]{t.root}
	for len(level) > 0 {
		h++
		next := make([]*node[K, V], 0, 2*len(level))
		for _, n := range level {
			if n.left != nil {
				next = append(next, n.left)
			}
			if n.right != nil {
				next = append(next, n.right)
			}
		}
		level = next
	}
	return h
}

// CheckInvariant verifies the ordering invariant and the size counter. Used by tests after every mutation.
func (t *BinarySearchTree[K, V]) CheckInvariant() bool {
	all := t.InOrder()
	if len(all) != t.size {
		return false
	}
	for i := 1; i < len(all); i++ {
		if t.cmp(all[i-1].Key, all[i].Key) >= 0 {
			return false
		}
	}
	return true
}

// TransactionIndex indexes transactions by (timestamp, sequence) so that "everything between t1 and t2" is one range query.
type TransactionIndex struct {
	tree         *BinarySearchTree[TxKey, Transaction]
	nextSequence int64
}

// NewTransactionIndex builds an empty index.
func NewTransactionIndex() *TransactionIndex {
	t, _ := NewBinarySearchTree[TxKey, Transaction](CompareTxKey)
	return &TransactionIndex{tree: t}
}

// Record files a transaction under the next sequence number.
func (x *TransactionIndex) Record(id string, timestampMillis int64, amount Money) Transaction {
	tx := Transaction{id, TxKey{timestampMillis, x.nextSequence}, amount}
	x.nextSequence++
	x.tree.Put(tx.Key, tx)
	return tx
}

// Remove unfiles a transaction; false when it was not present.
func (x *TransactionIndex) Remove(tx Transaction) bool {
	_, ok := x.tree.Remove(tx.Key)
	return ok
}

// Between lists every transaction with fromInclusive <= timestamp <= toInclusive, oldest first.
func (x *TransactionIndex) Between(fromInclusive, toInclusive int64) []Transaction {
	entries, _ := x.tree.RangeQuery(LowestTxKey(fromInclusive), HighestTxKey(toInclusive))
	out := make([]Transaction, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Value)
	}
	return out
}

// SumBetween totals the window in exact Money.
func (x *TransactionIndex) SumBetween(fromInclusive, toInclusive int64, currency Currency) (Money, error) {
	total := OfMinor(0, currency)
	for _, tx := range x.Between(fromInclusive, toInclusive) {
		var err error
		if total, err = total.Plus(tx.Amount); err != nil {
			return Money{}, err
		}
	}
	return total, nil
}

func (x *TransactionIndex) Size() int                                   { return x.tree.Size() }
func (x *TransactionIndex) Height() int                                 { return x.tree.Height() }
func (x *TransactionIndex) Tree() *BinarySearchTree[TxKey, Transaction] { return x.tree }

// JavaRandom is java.util.Random, bit for bit (48-bit LCG), so the demo data matches Java exactly.
type JavaRandom struct{ seed int64 }

const (
	javaMultiplier = 0x5DEECE66D
	javaAddend     = 0xB
	javaMask       = (1 << 48) - 1
)

// NewJavaRandom scrambles the seed as java.util.Random does.
func NewJavaRandom(seed int64) *JavaRandom { return &JavaRandom{(seed ^ javaMultiplier) & javaMask} }

func (r *JavaRandom) next(bitCount uint) int32 {
	r.seed = (r.seed*javaMultiplier + javaAddend) & javaMask
	return int32(r.seed >> (48 - bitCount))
}

// NextInt is java.util.Random.nextInt(bound), including the rejection loop.
func (r *JavaRandom) NextInt(bound int32) int32 {
	if bound <= 0 {
		panic("bound must be positive")
	}
	rr := r.next(31)
	m := bound - 1
	if bound&m == 0 {
		return int32((int64(bound) * int64(rr)) >> 31)
	}
	u := rr
	rr = u % bound
	for u-rr+m < 0 {
		u = r.next(31)
		rr = u % bound
	}
	return rr
}

const (
	t0     = 1_700_000_000_000 // 2023-11-14T22:13:20Z, a fixed epoch for determinism
	minute = 60_000
)

func keysOf[K, V any](t *BinarySearchTree[K, V]) string {
	parts := make([]string, 0, t.Size())
	for _, e := range t.InOrder() {
		parts = append(parts, fmt.Sprint(e.Key))
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func demoOrderedMap() {
	fmt.Println("== Insert, search, delete with successor ==")
	t := NewOrderedTree[int, string]()
	for _, k := range []int{50, 30, 70, 20, 40, 60, 80, 35, 45} {
		t.Put(k, fmt.Sprintf("v%d", k))
	}
	fmt.Println("in-order keys: " + keysOf(t))
	mn, _ := t.MinKey()
	mx, _ := t.MaxKey()
	fmt.Printf("size=%d height=%d min=%d max=%d\n", t.Size(), t.Height(), mn, mx)
	fl, _ := t.FloorKey(42)
	ce, _ := t.CeilingKey(42)
	g, _ := t.Get(45)
	fmt.Printf("floor(42)=%d ceiling(42)=%d get(45)=%s\n", fl, ce, g)
	t.Remove(30) // two children: successor is 35
	fmt.Printf("after remove(30), in-order: %s invariant=%v\n", keysOf(t), t.CheckInvariant())
	t.Remove(50) // root with two children: successor is 60
	fmt.Printf("after remove(50), in-order: %s height=%d\n", keysOf(t), t.Height())
	fmt.Println()
}

func demoRangeQuery() {
	fmt.Println("== Range query over transactions by timestamp ==")
	idx := NewTransactionIndex()
	rnd := NewJavaRandom(42)
	// 12 transactions spread over 90 minutes, inserted in arrival order (not timestamp order)
	offsets := []int64{5, 71, 12, 44, 44, 88, 3, 60, 29, 15, 52, 44}
	for i, off := range offsets {
		cents := 100 + int64(rnd.NextInt(9_900))
		idx.Record(fmt.Sprintf("tx-%d", i+1), t0+off*minute, OfMinor(cents, USD))
	}
	fmt.Printf("indexed %d transactions, tree height %d\n", idx.Size(), idx.Height())
	from, to := int64(t0+10*minute), int64(t0+45*minute)
	window := idx.Between(from, to)
	fmt.Printf("between +10min and +45min (inclusive): %d transactions\n", len(window))
	for _, tx := range window {
		fmt.Printf("  %-6s +%2d min  %v\n", tx.ID, (tx.Key.TimestampMillis-t0)/minute, tx.Amount)
	}
	sum, _ := idx.SumBetween(from, to, USD)
	fmt.Printf("sum in window = %v\n", sum)
	fmt.Println("three transactions share +44 min and all three are kept (composite key)")
	fmt.Println()
}

func demoDegenerate() {
	fmt.Println("== Why unbalanced trees need balancing ==")
	n := 4_096
	sorted := NewOrderedTree[int, int]()
	for i := 0; i < n; i++ {
		sorted.Put(i, i)
	}
	sorted.ResetComparisons()
	for i := 0; i < n; i++ {
		sorted.Get(i)
	}
	sortedCmp := sorted.Comparisons()

	shuffled := NewOrderedTree[int, int]()
	keys := make([]int, n)
	for i := range keys {
		keys[i] = i
	}
	rnd := NewJavaRandom(7)
	for i := n - 1; i > 0; i-- {
		j := int(rnd.NextInt(int32(i + 1)))
		keys[i], keys[j] = keys[j], keys[i]
	}
	for _, k := range keys {
		shuffled.Put(k, k)
	}
	shuffled.ResetComparisons()
	for i := 0; i < n; i++ {
		shuffled.Get(i)
	}
	shuffledCmp := shuffled.Comparisons()

	fmt.Printf("%d keys inserted in sorted order:   height %d, %.1f comparisons per lookup\n", n, sorted.Height(), float64(sortedCmp)/float64(n))
	fmt.Printf("%d keys inserted in shuffled order: height %d, %.1f comparisons per lookup\n", n, shuffled.Height(), float64(shuffledCmp)/float64(n))
	fmt.Printf("ideal balanced height for %d keys: %d\n", n, bits.Len(uint(n))-1)
	fmt.Println("transactions arrive in timestamp order, which is the worst case; Phase 3 fixes this with self-balancing trees")
}

func main() {
	demoOrderedMap()
	demoRangeQuery()
	demoDegenerate()
}
