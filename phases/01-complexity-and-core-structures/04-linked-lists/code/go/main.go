// Lesson: phases/01-complexity-and-core-structures/04-linked-lists/docs/en.md
// Linked Lists: Singly, Doubly, and Intrusive
// Run:  go run .
// Test: go test ./...
// Sources: Knuth, TAOCP Vol. 1, 2.2.3-2.2.5 (linked allocation, circular and doubly linked lists);
//
//	Linux kernel include/linux/list.h (intrusive list_head).
//
// Go 1.22+, standard library only. Same design as the Java and Python versions.
package main

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	// ErrEmpty is returned when removing from an empty list.
	ErrEmpty = errors.New("empty")
	// ErrIndexOutOfBounds is returned by Get for an index outside 0..size-1.
	ErrIndexOutOfBounds = errors.New("index out of bounds")
	// ErrNotOwned is returned when a node is not linked into the list it is handed to.
	ErrNotOwned = errors.New("node is not linked into this list")
	// ErrAlreadyLinked is returned when an intrusive element is added while it is in a list.
	ErrAlreadyLinked = errors.New("element is already in a list")
	// ErrSpliceSelf is returned by SpliceAll(self).
	ErrSpliceSelf = errors.New("cannot splice a list into itself")
)

// singlyNode is one cell of a SinglyLinkedList.
type singlyNode[T any] struct {
	value T
	next  *singlyNode[T]
}

// SinglyLinkedList has head and tail pointers.
// O(1): AddFirst, AddLast, RemoveFirst. O(n): Get, remove by value, removeLast (no back pointer).
type SinglyLinkedList[T comparable] struct {
	head *singlyNode[T]
	tail *singlyNode[T]
	size int
}

func (l *SinglyLinkedList[T]) Size() int     { return l.size }
func (l *SinglyLinkedList[T]) IsEmpty() bool { return l.size == 0 }

// AddFirst links a new head.
func (l *SinglyLinkedList[T]) AddFirst(value T) {
	n := &singlyNode[T]{value: value, next: l.head}
	l.head = n
	if l.tail == nil {
		l.tail = n
	}
	l.size++
}

// AddLast links a new tail.
func (l *SinglyLinkedList[T]) AddLast(value T) {
	n := &singlyNode[T]{value: value}
	if l.tail == nil {
		l.head, l.tail = n, n
	} else {
		l.tail.next = n
		l.tail = n
	}
	l.size++
}

// RemoveFirst unlinks the head.
func (l *SinglyLinkedList[T]) RemoveFirst() (T, error) {
	if l.head == nil {
		var zero T
		return zero, ErrEmpty
	}
	v := l.head.value
	l.head = l.head.next
	if l.head == nil {
		l.tail = nil
	}
	l.size--
	return v, nil
}

// Get is O(n): walk from the head. There is no shortcut; this is the operation arrays do in O(1).
func (l *SinglyLinkedList[T]) Get(index int) (T, error) {
	if index < 0 || index >= l.size {
		var zero T
		return zero, fmt.Errorf("%w: index %d size %d", ErrIndexOutOfBounds, index, l.size)
	}
	n := l.head
	for i := 0; i < index; i++ {
		n = n.next
	}
	return n.value, nil
}

// RemoveFirstOccurrence is O(n): find the node *before* the match so it can be unlinked.
// Singly linked lists cannot unlink a node given only that node.
func (l *SinglyLinkedList[T]) RemoveFirstOccurrence(value T) bool {
	var prev *singlyNode[T]
	cur := l.head
	for cur != nil {
		if cur.value == value {
			if prev == nil {
				l.head = cur.next
			} else {
				prev.next = cur.next
			}
			if cur == l.tail {
				l.tail = prev
			}
			l.size--
			return true
		}
		prev = cur
		cur = cur.next
	}
	return false
}

// Reverse is in-place with three pointers; O(n) time, O(1) extra space.
func (l *SinglyLinkedList[T]) Reverse() {
	var prev *singlyNode[T]
	cur := l.head
	l.tail = l.head
	for cur != nil {
		next := cur.next
		cur.next = prev
		prev = cur
		cur = next
	}
	l.head = prev
}

// Each visits every value from the head.
func (l *SinglyLinkedList[T]) Each(visit func(T)) {
	for n := l.head; n != nil; n = n.next {
		visit(n.value)
	}
}

func (l *SinglyLinkedList[T]) String() string {
	var sb strings.Builder
	sb.WriteString("[")
	for n := l.head; n != nil; n = n.next {
		fmt.Fprint(&sb, n.value)
		if n.next != nil {
			sb.WriteString(", ")
		}
	}
	sb.WriteString("]")
	return sb.String()
}

// Node is one cell of a DoublyLinkedList. Handing out *Node makes Unlink and MoveToFront O(1).
type Node[T any] struct {
	Value T
	prev  *Node[T]
	next  *Node[T]
	owner *DoublyLinkedList[T] // which list this node is linked into; nil once unlinked
}

// Next is the following node, or nil at the end.
func (n *Node[T]) Next() *Node[T] {
	if n.owner == nil || n.next == &n.owner.sentinel {
		return nil
	}
	return n.next
}

// Linked reports whether the node is currently in a list.
func (n *Node[T]) Linked() bool { return n.owner != nil }

// Owner is the list the node is linked into, or nil.
func (n *Node[T]) Owner() *DoublyLinkedList[T] { return n.owner }

// DoublyLinkedList has a sentinel node. The sentinel's next is the first element and its prev is the last,
// so there is never a nil to check and insert/unlink are the same code at every position.
type DoublyLinkedList[T any] struct {
	sentinel Node[T]
	size     int
}

// NewDoublyLinkedList makes an empty list whose sentinel points at itself.
func NewDoublyLinkedList[T any]() *DoublyLinkedList[T] {
	l := &DoublyLinkedList[T]{}
	l.sentinel.prev = &l.sentinel
	l.sentinel.next = &l.sentinel
	return l
}

func (l *DoublyLinkedList[T]) Size() int     { return l.size }
func (l *DoublyLinkedList[T]) IsEmpty() bool { return l.size == 0 }

// First is the first node, or nil when empty.
func (l *DoublyLinkedList[T]) First() *Node[T] {
	if l.size == 0 {
		return nil
	}
	return l.sentinel.next
}

// Last is the last node, or nil when empty.
func (l *DoublyLinkedList[T]) Last() *Node[T] {
	if l.size == 0 {
		return nil
	}
	return l.sentinel.prev
}

// linkBefore links a fresh node holding value immediately before succ. Works for every position including the ends.
func (l *DoublyLinkedList[T]) linkBefore(value T, succ *Node[T]) *Node[T] {
	n := &Node[T]{Value: value}
	pred := succ.prev
	n.prev = pred
	n.next = succ
	pred.next = n
	succ.prev = n
	n.owner = l
	l.size++
	return n
}

func (l *DoublyLinkedList[T]) AddFirst(value T) *Node[T] { return l.linkBefore(value, l.sentinel.next) }
func (l *DoublyLinkedList[T]) AddLast(value T) *Node[T]  { return l.linkBefore(value, &l.sentinel) }

// InsertBefore links value immediately before node.
func (l *DoublyLinkedList[T]) InsertBefore(node *Node[T], value T) (*Node[T], error) {
	if err := l.requireOwned(node); err != nil {
		return nil, err
	}
	return l.linkBefore(value, node), nil
}

// Unlink is O(1): the node knows both neighbours, so no search is needed.
func (l *DoublyLinkedList[T]) Unlink(node *Node[T]) (T, error) {
	if err := l.requireOwned(node); err != nil {
		var zero T
		return zero, err
	}
	node.prev.next = node.next
	node.next.prev = node.prev
	node.prev, node.next = nil, nil // nil the links so the GC can collect the node
	node.owner = nil
	l.size--
	return node.Value, nil
}

// RemoveFirst unlinks the head.
func (l *DoublyLinkedList[T]) RemoveFirst() (T, error) {
	if l.size == 0 {
		var zero T
		return zero, ErrEmpty
	}
	return l.Unlink(l.sentinel.next)
}

// RemoveLast unlinks the tail.
func (l *DoublyLinkedList[T]) RemoveLast() (T, error) {
	if l.size == 0 {
		var zero T
		return zero, ErrEmpty
	}
	return l.Unlink(l.sentinel.prev)
}

// MoveToFront is an O(1) relink without allocation: the LRU "touch" operation.
func (l *DoublyLinkedList[T]) MoveToFront(node *Node[T]) error {
	if err := l.requireOwned(node); err != nil {
		return err
	}
	if l.sentinel.next == node {
		return nil
	}
	node.prev.next = node.next
	node.next.prev = node.prev
	node.prev = &l.sentinel
	node.next = l.sentinel.next
	l.sentinel.next.prev = node
	l.sentinel.next = node
	return nil
}

// SpliceAll moves every node of other to the end of this list in O(1) relinks, leaving other empty.
func (l *DoublyLinkedList[T]) SpliceAll(other *DoublyLinkedList[T]) error {
	if other == l {
		return ErrSpliceSelf
	}
	if other.size == 0 {
		return nil
	}
	otherFirst := other.sentinel.next
	otherLast := other.sentinel.prev
	myLast := l.sentinel.prev
	myLast.next = otherFirst
	otherFirst.prev = myLast
	otherLast.next = &l.sentinel
	l.sentinel.prev = otherLast
	// Ownership must follow the nodes: this is the one O(n) walk the splice cannot avoid if nodes track their owner.
	for n := otherFirst; n != &l.sentinel; n = n.next {
		n.owner = l
	}
	l.size += other.size
	other.sentinel.next = &other.sentinel
	other.sentinel.prev = &other.sentinel
	other.size = 0
	return nil
}

func (l *DoublyLinkedList[T]) requireOwned(node *Node[T]) error {
	if node == nil || node.owner != l {
		return ErrNotOwned
	}
	return nil
}

// InvariantHolds is the structural check used by the tests: forward and backward walks agree with each other and with size.
func (l *DoublyLinkedList[T]) InvariantHolds() bool {
	forward := 0
	for n := l.sentinel.next; n != &l.sentinel; n = n.next {
		if n.next.prev != n || n.owner != l {
			return false
		}
		forward++
	}
	backward := 0
	for n := l.sentinel.prev; n != &l.sentinel; n = n.prev {
		backward++
	}
	return forward == l.size && backward == l.size
}

// Each visits every value from the front.
func (l *DoublyLinkedList[T]) Each(visit func(T)) {
	for n := l.sentinel.next; n != &l.sentinel; n = n.next {
		visit(n.Value)
	}
}

func (l *DoublyLinkedList[T]) String() string {
	var sb strings.Builder
	sb.WriteString("[")
	for n := l.sentinel.next; n != &l.sentinel; n = n.next {
		fmt.Fprint(&sb, n.Value)
		if n.next != &l.sentinel {
			sb.WriteString(", ")
		}
	}
	sb.WriteString("]")
	return sb.String()
}

// IntrusiveNode is embedded in elements of an intrusive list: the element carries its own links, so there is
// no separate node object, no allocation on insert, and removal is O(1) given only the element. An element can
// be in at most one IntrusiveList at a time; owner enforces that. Compare Linux's struct list_head.
type IntrusiveNode struct {
	prev, next *IntrusiveNode
	owner      any // the *IntrusiveList[T] this element is linked into; nil once unlinked
	self       any // the element that embeds this hook: Go's stand-in for container_of
}

// IsLinked reports whether the element is in a list.
func (n *IntrusiveNode) IsLinked() bool { return n.owner != nil }

// hook lets IntrusiveList reach the embedded links through any element type.
func (n *IntrusiveNode) hook() *IntrusiveNode { return n }

// Intrusive is satisfied by any pointer type that embeds IntrusiveNode.
type Intrusive interface {
	comparable
	hook() *IntrusiveNode
}

// IntrusiveList is an intrusive doubly linked list with a sentinel.
type IntrusiveList[T Intrusive] struct {
	sentinel IntrusiveNode
	size     int
}

// NewIntrusiveList makes an empty list.
func NewIntrusiveList[T Intrusive]() *IntrusiveList[T] {
	l := &IntrusiveList[T]{}
	l.sentinel.prev = &l.sentinel
	l.sentinel.next = &l.sentinel
	return l
}

func (l *IntrusiveList[T]) Size() int     { return l.size }
func (l *IntrusiveList[T]) IsEmpty() bool { return l.size == 0 }

// AddLast links the element at the tail without allocating.
func (l *IntrusiveList[T]) AddLast(element T) error {
	h := element.hook()
	if h.owner != nil {
		return ErrAlreadyLinked
	}
	last := l.sentinel.prev
	h.prev = last
	h.next = &l.sentinel
	last.next = h
	l.sentinel.prev = h
	h.owner = l
	h.self = element
	l.size++
	return nil
}

// Remove is O(1), no lookup: the element is its own handle.
func (l *IntrusiveList[T]) Remove(element T) error {
	h := element.hook()
	if h.owner != any(l) {
		return fmt.Errorf("%w: element is not in this list", ErrNotOwned)
	}
	h.prev.next = h.next
	h.next.prev = h.prev
	h.prev, h.next = nil, nil
	h.owner = nil
	h.self = nil
	l.size--
	return nil
}

// Contains is an O(1) membership test, because ownership is recorded in the element.
func (l *IntrusiveList[T]) Contains(element T) bool { return element.hook().owner == any(l) }

// Clear detaches every element so it can be linked elsewhere. O(n): each element must forget its owner.
func (l *IntrusiveList[T]) Clear() {
	cur := l.sentinel.next
	for cur != &l.sentinel {
		next := cur.next
		cur.prev, cur.next = nil, nil
		cur.owner = nil
		cur.self = nil
		cur = next
	}
	l.sentinel.prev = &l.sentinel
	l.sentinel.next = &l.sentinel
	l.size = 0
}

// First is the first element and true, or the zero value and false when empty.
func (l *IntrusiveList[T]) First() (T, bool) {
	if l.size == 0 {
		var zero T
		return zero, false
	}
	return l.sentinel.next.self.(T), true
}

// Each visits every element from the front.
func (l *IntrusiveList[T]) Each(visit func(T)) {
	for n := l.sentinel.next; n != &l.sentinel; n = n.next {
		visit(n.self.(T))
	}
}

// PendingAuthorization is a domain object that lives in an intrusive list: a card authorization awaiting capture or expiry.
type PendingAuthorization struct {
	IntrusiveNode
	ID             string
	AmountMinor    int64 // integer minor units, never a float (Phase 10, Lesson 01)
	ExpiresAtMilli int64
}

// NewPendingAuthorization builds an unlinked authorization.
func NewPendingAuthorization(id string, amountMinor, expiresAtMillis int64) *PendingAuthorization {
	return &PendingAuthorization{ID: id, AmountMinor: amountMinor, ExpiresAtMilli: expiresAtMillis}
}

func (p *PendingAuthorization) String() string { return fmt.Sprintf("%s(%d)", p.ID, p.AmountMinor) }

// Cell is used by the traversal benchmark: one int64, linked intrusively so allocation order and list order can differ.
type Cell struct {
	IntrusiveNode
	Value int64
}

// SumArray is the sequential scan.
func SumArray(values []int64) int64 {
	sum := int64(0)
	for _, v := range values {
		sum += v
	}
	return sum
}

// SumList is the pointer chase.
func SumList(list *IntrusiveList[*Cell]) int64 {
	sum := int64(0)
	list.Each(func(c *Cell) { sum += c.Value })
	return sum
}

// LinkInOrder builds an intrusive list over pre-allocated cells, linking them in the given order.
func LinkInOrder(cells []*Cell, order []int) *IntrusiveList[*Cell] {
	list := NewIntrusiveList[*Cell]()
	for _, i := range order {
		_ = list.AddLast(cells[i])
	}
	return list
}

// Identity is 0..n-1.
func Identity(n int) []int {
	order := make([]int, n)
	for i := range order {
		order[i] = i
	}
	return order
}

// Shuffled is a Fisher-Yates permutation driven by java.util.Random(seed), so it matches the Java demo.
func Shuffled(n int, seed int64) []int {
	order := Identity(n)
	rnd := NewJavaRandom(seed)
	for i := n - 1; i > 0; i-- {
		j := int(rnd.NextInt(int32(i + 1)))
		order[i], order[j] = order[j], order[i]
	}
	return order
}

// JavaRandom is java.util.Random, bit for bit (48-bit LCG).
type JavaRandom struct {
	seed int64
}

const (
	javaMultiplier = 0x5DEECE66D
	javaAddend     = 0xB
	javaMask       = (1 << 48) - 1
)

// NewJavaRandom scrambles the seed as java.util.Random does.
func NewJavaRandom(seed int64) *JavaRandom {
	return &JavaRandom{seed: (seed ^ javaMultiplier) & javaMask}
}

func (r *JavaRandom) next(bits uint) int32 {
	r.seed = (r.seed*javaMultiplier + javaAddend) & javaMask
	return int32(r.seed >> (48 - bits))
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

func bestOf(rounds int, work func() int64) time.Duration {
	best := time.Duration(1<<63 - 1)
	for r := 0; r < rounds; r++ {
		t0 := time.Now()
		work()
		if d := time.Since(t0); d < best {
			best = d
		}
	}
	return best
}

func joinAuths(list *IntrusiveList[*PendingAuthorization]) string {
	var parts []string
	list.Each(func(p *PendingAuthorization) { parts = append(parts, p.String()) })
	return strings.Join(parts, " ")
}

func main() {
	fmt.Println("== Singly linked: O(1) at the head, O(n) everywhere else ==")
	singly := &SinglyLinkedList[string]{}
	singly.AddLast("auth-1")
	singly.AddLast("auth-2")
	singly.AddLast("auth-3")
	singly.AddFirst("auth-0")
	fmt.Printf("list     = %v size=%d\n", singly, singly.Size())
	v, _ := singly.Get(2)
	fmt.Printf("get(2)   = %s  (walked 2 links to find it)\n", v)
	singly.RemoveFirstOccurrence("auth-2")
	fmt.Printf("remove auth-2 by value -> %v  (walked from the head to find the predecessor)\n", singly)
	singly.Reverse()
	fmt.Printf("reverse  = %v\n", singly)
	fmt.Println()

	fmt.Println("== Doubly linked with a sentinel: O(1) unlink given the node ==")
	lru := NewDoublyLinkedList[string]()
	k1 := lru.AddLast("idem-key-1")
	k2 := lru.AddLast("idem-key-2")
	k3 := lru.AddLast("idem-key-3")
	fmt.Printf("recency order (front = most recent): %v\n", lru)
	_ = lru.MoveToFront(k3)
	fmt.Printf("touch key-3 -> %v\n", lru)
	_ = lru.MoveToFront(k1)
	fmt.Printf("touch key-1 -> %v\n", lru)
	evicted, _ := lru.RemoveLast()
	fmt.Printf("evict least recent (removeLast) -> %s, now %v\n", evicted, lru)
	unlinked, _ := lru.Unlink(k3)
	fmt.Printf("unlink key-3 directly -> %s, now %v size=%d\n", unlinked, lru, lru.Size())
	fmt.Printf("k2 still linked? %v; invariant holds? %v\n", k2.Linked(), lru.InvariantHolds())
	fmt.Println()

	fmt.Println("== Splice: merge two queues in O(1), not O(n) ==")
	shardA := NewDoublyLinkedList[string]()
	shardB := NewDoublyLinkedList[string]()
	for i := 1; i <= 3; i++ {
		shardA.AddLast(fmt.Sprintf("a%d", i))
	}
	for i := 1; i <= 4; i++ {
		shardB.AddLast(fmt.Sprintf("b%d", i))
	}
	_ = shardA.SpliceAll(shardB)
	fmt.Printf("A after splice = %v size=%d; B = %v size=%d\n", shardA, shardA.Size(), shardB, shardB.Size())
	fmt.Println()

	fmt.Println("== Intrusive list: the element is its own node ==")
	pending := NewIntrusiveList[*PendingAuthorization]()
	a1 := NewPendingAuthorization("auth-7f3a", 1999, 1_000)
	a2 := NewPendingAuthorization("auth-9c21", 4500, 2_000)
	a3 := NewPendingAuthorization("auth-0b77", 250, 3_000)
	_ = pending.AddLast(a1)
	_ = pending.AddLast(a2)
	_ = pending.AddLast(a3)
	fmt.Println("pending: " + joinAuths(pending))
	fmt.Printf("capture auth-9c21: remove(a2) is O(1) with no search; linked before? %v\n", pending.Contains(a2))
	_ = pending.Remove(a2)
	fmt.Printf("pending: %s  linked after? %v\n", joinAuths(pending), pending.Contains(a2))
	if err := pending.Remove(a2); err != nil {
		fmt.Println("remove twice -> " + err.Error())
	}
	fmt.Println()

	fmt.Println("== Measured: summing 1,000,000 int64s, slice vs linked list (sequential vs shuffled memory order) ==")
	n := 1_000_000
	array := make([]int64, n)
	cellsA := make([]*Cell, n)
	cellsB := make([]*Cell, n)
	for i := 0; i < n; i++ {
		array[i] = int64(i)
		cellsA[i] = &Cell{Value: int64(i)}
		cellsB[i] = &Cell{Value: int64(i)}
	}
	sequential := LinkInOrder(cellsA, Identity(n))    // list order == allocation order
	scattered := LinkInOrder(cellsB, Shuffled(n, 42)) // list order is a random walk through memory
	expected := int64(n) * int64(n-1) / 2
	tArray := bestOf(5, func() int64 { return SumArray(array) })
	tSeq := bestOf(5, func() int64 { return SumList(sequential) })
	tScat := bestOf(5, func() int64 { return SumList(scattered) })
	fmt.Printf("all three sums equal %d: %v\n", expected,
		SumArray(array) == expected && SumList(sequential) == expected && SumList(scattered) == expected)
	fmt.Printf("[]int64 sequential scan : %6.2f ms\n", float64(tArray)/1e6)
	fmt.Printf("list, nodes in memory order : %6.2f ms  (%.1fx the array)\n", float64(tSeq)/1e6, float64(tSeq)/float64(tArray))
	fmt.Printf("list, nodes in shuffled order: %6.2f ms  (%.1fx the array)\n", float64(tScat)/1e6, float64(tScat)/float64(tArray))
	fmt.Println("Same n, same O(n), same sum. The difference is cache misses; timings vary by machine, the ordering does not.")
}
