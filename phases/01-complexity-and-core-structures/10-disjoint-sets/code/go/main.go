// Lesson: phases/01-complexity-and-core-structures/10-disjoint-sets/docs/en.md
// Disjoint Sets (Union-Find) with Path Compression
// Run:  go run .
// Test: go test ./...
// Sources: Tarjan, "Efficiency of a Good But Not Linear Set Union Algorithm", JACM 22(2), 1975;
//
//	Cormen et al., "Introduction to Algorithms", ch. 19 (data structures for disjoint sets).
//
// Go 1.22+, standard library only. Same design as the Java and Python versions.
package main

import (
	"errors"
	"fmt"
	"math/bits"
	"sort"
	"strings"
)

var (
	// ErrNegativeSize is returned by NewDisjointSetWith for n < 0.
	ErrNegativeSize = errors.New("n must be >= 0")
	// ErrCapacityExceeded is returned when an EntityLinker runs out of slots.
	ErrCapacityExceeded = errors.New("linker capacity exceeded")
)

// DisjointSet is a disjoint-set forest over the integers 0..n-1.
//
// Invariant: parent encodes a forest; every element reaches exactly one root by following parent
// pointers, and two elements are in the same set iff they reach the same root. rank[r] is an upper
// bound on the height of the tree rooted at r and is only meaningful at roots. size[r] is the exact
// number of elements in r's set.
//
// The two heuristics can be switched off independently so the lesson can measure what each buys.
type DisjointSet struct {
	parent          []int32
	rank            []int32
	size            []int32
	components      int
	unionByRank     bool
	pathCompression bool
	hops            int64 // instrumentation: parent pointers followed inside Find
}

// NewDisjointSet builds a forest with both heuristics on.
func NewDisjointSet(n int) *DisjointSet {
	ds, err := NewDisjointSetWith(n, true, true)
	if err != nil {
		panic(err)
	}
	return ds
}

// NewDisjointSetWith builds a forest with each heuristic chosen explicitly.
func NewDisjointSetWith(n int, unionByRank, pathCompression bool) (*DisjointSet, error) {
	if n < 0 {
		return nil, ErrNegativeSize
	}
	ds := &DisjointSet{
		parent: make([]int32, n), rank: make([]int32, n), size: make([]int32, n),
		components: n, unionByRank: unionByRank, pathCompression: pathCompression,
	}
	for i := range ds.parent {
		ds.parent[i] = int32(i)
		ds.size[i] = 1
	}
	return ds, nil
}

func mustForest(n int, unionByRank, pathCompression bool) *DisjointSet {
	ds, err := NewDisjointSetWith(n, unionByRank, pathCompression)
	if err != nil {
		panic(err)
	}
	return ds
}

func (d *DisjointSet) Elements() int       { return len(d.parent) }
func (d *DisjointSet) ComponentCount() int { return d.components }
func (d *DisjointSet) Hops() int64         { return d.hops }
func (d *DisjointSet) ResetHops()          { d.hops = 0 }

// checkIndex panics like a slice index would: an out-of-range element is a programming error, not a runtime condition.
func (d *DisjointSet) checkIndex(x int) {
	if x < 0 || x >= len(d.parent) {
		panic(fmt.Sprintf("element %d not in 0..%d", x, len(d.parent)-1))
	}
}

// Find returns the representative of x's set. Two passes: walk to the root, then point every node on the path
// straight at it. Iterative, so a long uncompressed chain cannot overflow the stack.
func (d *DisjointSet) Find(x int) int {
	d.checkIndex(x)
	root := int32(x)
	for d.parent[root] != root {
		root = d.parent[root]
		d.hops++
	}
	if d.pathCompression {
		cur := int32(x)
		for d.parent[cur] != root {
			next := d.parent[cur]
			d.parent[cur] = root
			cur = next
		}
	}
	return int(root)
}

// Union merges the sets containing a and b. Returns false if they were already the same set.
func (d *DisjointSet) Union(a, b int) bool {
	ra, rb := int32(d.Find(a)), int32(d.Find(b))
	if ra == rb {
		return false
	}
	if d.unionByRank {
		if d.rank[ra] < d.rank[rb] {
			ra, rb = rb, ra // ra is now the taller (or equal) root
		}
		d.parent[rb] = ra
		if d.rank[ra] == d.rank[rb] {
			d.rank[ra]++
		}
	} else {
		d.parent[rb] = ra // naive: always hang b's root under a's root
	}
	d.size[ra] += d.size[rb]
	d.components--
	return true
}

// Connected reports whether a and b share a root.
func (d *DisjointSet) Connected(a, b int) bool { return d.Find(a) == d.Find(b) }

// ComponentSize is the number of elements in x's set.
func (d *DisjointSet) ComponentSize(x int) int { return int(d.size[d.Find(x)]) }

// RankOf is the rank of the root of x's set: an upper bound on the height of its tree.
func (d *DisjointSet) RankOf(x int) int { return int(d.rank[d.Find(x)]) }

// Depth is the actual depth of x: how many parent pointers separate it from its root. Does not compress.
func (d *DisjointSet) Depth(x int) int {
	d.checkIndex(x)
	cur := int32(x)
	depth := 0
	for d.parent[cur] != cur {
		cur = d.parent[cur]
		depth++
	}
	return depth
}

// MaxDepth is the deepest element in the forest. O(n * depth); diagnostic only.
func (d *DisjointSet) MaxDepth() int {
	m := 0
	for i := range d.parent {
		m = max(m, d.Depth(i))
	}
	return m
}

// CheckInvariant verifies the forest invariants: parents are in range, every walk terminates, sizes and component count agree.
func (d *DisjointSet) CheckInvariant() bool {
	n := len(d.parent)
	roots := 0
	total := int64(0)
	counted := make([]int32, n)
	for i := range d.parent {
		if d.parent[i] < 0 || int(d.parent[i]) >= n {
			return false
		}
		x, steps := int32(i), 0
		for d.parent[x] != x {
			x = d.parent[x]
			steps++
			if steps > n { // cycle guard
				return false
			}
		}
		counted[x]++
		if int(d.parent[i]) == i {
			roots++
			total += int64(d.size[i])
		}
	}
	if roots != d.components || total != int64(n) {
		return false
	}
	for i := range d.parent {
		if int(d.parent[i]) == i && counted[i] != d.size[i] {
			return false
		}
	}
	return true
}

// EntityLinker does entity linking for fraud: every identifier (account, device, card fingerprint, email) is an
// element, every shared identifier is a union, and a "ring" is a connected component. Names map to indices
// lazily so callers never see integers.
type EntityLinker struct {
	index    map[string]int
	names    []string
	capacity int
	sets     *DisjointSet
	next     int
}

// NewEntityLinker reserves capacity identifier slots.
func NewEntityLinker(capacity int) *EntityLinker {
	return &EntityLinker{index: map[string]int{}, capacity: capacity, sets: NewDisjointSet(capacity)}
}

func (l *EntityLinker) id(name string) (int, error) {
	if existing, ok := l.index[name]; ok {
		return existing, nil
	}
	if l.next >= l.capacity {
		return 0, fmt.Errorf("%w: %d", ErrCapacityExceeded, l.capacity)
	}
	l.index[name] = l.next
	l.names = append(l.names, name)
	l.next++
	return l.next - 1, nil
}

// Link records that two identifiers were seen together. Returns true if this observation merged two rings.
func (l *EntityLinker) Link(a, b string) (bool, error) {
	ia, err := l.id(a)
	if err != nil {
		return false, err
	}
	ib, err := l.id(b)
	if err != nil {
		return false, err
	}
	return l.sets.Union(ia, ib), nil
}

// SameRing reports whether both identifiers are known and connected.
func (l *EntityLinker) SameRing(a, b string) bool {
	ia, okA := l.index[a]
	ib, okB := l.index[b]
	return okA && okB && l.sets.Connected(ia, ib)
}

// RingSize is the size of name's ring, or 0 for an unknown identifier.
func (l *EntityLinker) RingSize(name string) int {
	i, ok := l.index[name]
	if !ok {
		return 0
	}
	return l.sets.ComponentSize(i)
}

// Ring lists all identifiers in the same ring as name, sorted for deterministic output.
func (l *EntityLinker) Ring(name string) []string {
	out := []string{}
	i, ok := l.index[name]
	if !ok {
		return out
	}
	root := l.sets.Find(i)
	for j := 0; j < l.next; j++ {
		if l.sets.Find(j) == root {
			out = append(out, l.names[j])
		}
	}
	sort.Strings(out)
	return out
}

// RingsOfAtLeast lists every ring with at least minSize members, largest first; ties broken by the smallest member name.
func (l *EntityLinker) RingsOfAtLeast(minSize int) [][]string {
	byRoot := map[int][]string{}
	for j := 0; j < l.next; j++ {
		r := l.sets.Find(j)
		byRoot[r] = append(byRoot[r], l.names[j])
	}
	out := [][]string{}
	for _, members := range byRoot {
		if len(members) >= minSize {
			sort.Strings(members)
			out = append(out, members)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if len(out[i]) != len(out[j]) {
			return len(out[i]) > len(out[j])
		}
		return out[i][0] < out[j][0]
	})
	return out
}

func (l *EntityLinker) Identifiers() int { return l.next }

// Rings excludes the unused slots, which are singletons we have not handed out.
func (l *EntityLinker) Rings() int { return l.sets.ComponentCount() - (l.capacity - l.next) }

// JavaRandom is java.util.Random, bit for bit (48-bit LCG), so seeds 3, 11, and 99 drive identical unions.
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

func demoBasics() {
	fmt.Println("== Union, find, connected ==")
	ds := NewDisjointSet(8)
	fmt.Printf("8 elements, %d components\n", ds.ComponentCount())
	ds.Union(0, 1)
	ds.Union(2, 3)
	ds.Union(1, 3)
	ds.Union(5, 6)
	fmt.Printf("after union(0,1) union(2,3) union(1,3) union(5,6): %d components\n", ds.ComponentCount())
	fmt.Printf("connected(0,2)=%v  connected(0,5)=%v  size of 0's set=%d  size of 7's set=%d\n",
		ds.Connected(0, 2), ds.Connected(0, 5), ds.ComponentSize(0), ds.ComponentSize(7))
	fmt.Printf("union(0,3) again -> %v (already joined; a cycle in the input, not in the forest)\n", ds.Union(0, 3))
	fmt.Printf("invariant holds: %v\n", ds.CheckInvariant())
	fmt.Println()
}

var heuristicLabels = []string{"naive (neither)", "union by rank only", "path compression only", "both"}
var heuristicFlags = [][2]bool{{false, false}, {true, false}, {false, true}, {true, true}}

func demoHeuristics() {
	fmt.Println("== What union by rank and path compression buy ==")
	n := 20_000
	for v := range heuristicLabels {
		ds := mustForest(n, heuristicFlags[v][0], heuristicFlags[v][1])
		// adversarial order for the naive forest: always union the growing set's element under a fresh singleton
		for i := 1; i < n; i++ {
			ds.Union(i, i-1)
		}
		depthBefore := ds.MaxDepth()
		ds.ResetHops()
		for i := 0; i < n; i++ {
			ds.Find(i)
		}
		hopsPerFind := ds.Hops() / int64(n)
		fmt.Printf("%-24s max depth before finds %6d, pointers followed per find %5d, max depth after %3d\n",
			heuristicLabels[v], depthBefore, hopsPerFind, ds.MaxDepth())
	}
	fmt.Println("chain workload: rank refuses to build the chain; compression repairs it after one find")
	fmt.Println()
	fmt.Println("random workload: 20,000 elements, 30,000 random unions, then one find per element")
	for v := range heuristicLabels {
		ds := mustForest(n, heuristicFlags[v][0], heuristicFlags[v][1])
		rnd := NewJavaRandom(3)
		for i := 0; i < 30_000; i++ {
			a := int(rnd.NextInt(int32(n)))
			b := int(rnd.NextInt(int32(n)))
			ds.Union(a, b)
		}
		depthBefore := ds.MaxDepth()
		ds.ResetHops()
		for i := 0; i < n; i++ {
			ds.Find(i)
		}
		fmt.Printf("%-24s max depth before finds %6d, pointers followed per find %5.2f, max depth after %3d\n",
			heuristicLabels[v], depthBefore, float64(ds.Hops())/float64(n), ds.MaxDepth())
	}
	fmt.Println("rank alone caps depth at log2(n) = 14; both together make almost every find a single hop")
	fmt.Println()
}

func demoFraudRing() {
	fmt.Println("== Entity linking: accounts that share devices, cards, or emails ==")
	linker := NewEntityLinker(64)
	observations := [][2]string{
		{"acct:1001", "device:A"}, {"acct:1002", "device:A"}, // two accounts, one phone
		{"acct:1002", "card:4111-xx-0001"}, {"acct:1003", "card:4111-xx-0001"}, // card fingerprint reused
		{"acct:1003", "email:x@example.com"}, {"acct:1004", "email:x@example.com"}, // same email
		{"acct:2001", "device:B"}, {"acct:2002", "device:B"}, // a separate, smaller ring
		{"acct:3001", "device:C"}, // an honest singleton pair
	}
	for _, o := range observations {
		merged, _ := linker.Link(o[0], o[1])
		verdict := "already linked"
		if merged {
			verdict = "merged"
		}
		fmt.Printf("  link %-10s <-> %-20s %s\n", o[0], o[1], verdict)
	}
	fmt.Printf("identifiers seen: %d, rings: %d\n", linker.Identifiers(), linker.Rings())
	fmt.Printf("acct:1001 and acct:1004 share a ring: %v (no direct link between them; three hops through device, card, email)\n",
		linker.SameRing("acct:1001", "acct:1004"))
	fmt.Printf("acct:1001 and acct:2001 share a ring: %v\n", linker.SameRing("acct:1001", "acct:2001"))
	for _, ring := range linker.RingsOfAtLeast(4) {
		fmt.Printf("ring of %d flagged for review: [%s]\n", len(ring), strings.Join(ring, ", "))
	}
	fmt.Println()
}

func demoRandomMerges() {
	fmt.Println("== 100,000 random unions over 50,000 elements ==")
	n := 50_000
	ds := NewDisjointSet(n)
	rnd := NewJavaRandom(11)
	merges := 0
	for i := 0; i < 100_000; i++ {
		a := int(rnd.NextInt(int32(n)))
		b := int(rnd.NextInt(int32(n)))
		if ds.Union(a, b) {
			merges++
		}
	}
	ds.ResetHops()
	for i := 0; i < n; i++ {
		ds.Find(i)
	}
	fmt.Printf("effective merges %d, components left %d, largest set %d, max depth %d, avg pointers per find %.2f\n",
		merges, ds.ComponentCount(), largest(ds), ds.MaxDepth(), float64(ds.Hops())/float64(n))
}

func largest(ds *DisjointSet) int {
	best := 0
	for i := 0; i < ds.Elements(); i++ {
		best = max(best, ds.ComponentSize(i))
	}
	return best
}

// log2Floor is 31 - Integer.numberOfLeadingZeros(n) for n > 0.
func log2Floor(n int) int { return bits.Len(uint(n)) - 1 }

func main() {
	demoBasics()
	demoHeuristics()
	demoFraudRing()
	demoRandomMerges()
}
