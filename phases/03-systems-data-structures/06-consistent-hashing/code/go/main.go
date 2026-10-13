// Lesson: phases/03-systems-data-structures/06-consistent-hashing/docs/en.md
// Consistent Hashing and Virtual Nodes
// Run:  go run .
// Test: go test ./...
// Sources: Karger et al. (1997), "Consistent Hashing and Random Trees"; DeCandia et al. (2007), "Dynamo";
//          FNV-1a (IETF draft-eastlake-fnv) and MurmurHash3 fmix64 for the point hash.
// Go 1.22+, standard library only. Same design as the Java and Python versions.
package main

import (
	"errors"
	"fmt"
	"math"
	"sort"
)

const (
	fnvOffset uint64 = 0xcbf29ce484222325
	fnvPrime  uint64 = 0x100000001b3
)

func fnv1a64(data []byte) uint64 {
	h := fnvOffset
	for _, b := range data {
		h ^= uint64(b)
		h *= fnvPrime
	}
	return h
}

func mix64(h uint64) uint64 {
	h ^= h >> 33
	h *= 0xff51afd7ed558ccd
	h ^= h >> 33
	h *= 0xc4ceb9fe1a85ec53
	h ^= h >> 33
	return h
}

// HashKey is the position of a key or a virtual node on the ring: FNV-1a for the bytes, fmix64 to
// spread the low bits that short, similar keys leave correlated.
func HashKey(key string) uint64 { return mix64(fnv1a64([]byte(key))) }

var (
	ErrEmptyRing    = errors.New("ring is empty")
	ErrNodeExists   = errors.New("node already on ring")
	ErrNodeMissing  = errors.New("node not on ring")
	ErrBadParameter = errors.New("bad parameter")
)

type point struct {
	hash  uint64
	owner string
}

// HashRing is a sorted slice of (hash, node) points. Each physical node owns VirtualNodes points.
// A key belongs to the first point clockwise from its own hash; the ring wraps.
type HashRing struct {
	VirtualNodes int
	points       []point
	nodes        map[string]bool
}

// NewHashRing creates an empty ring with the given number of virtual nodes per physical node.
func NewHashRing(virtualNodes int) (*HashRing, error) {
	if virtualNodes <= 0 {
		return nil, fmt.Errorf("%w: virtualNodes must be positive: %d", ErrBadParameter, virtualNodes)
	}
	return &HashRing{VirtualNodes: virtualNodes, nodes: map[string]bool{}}, nil
}

func mustRing(virtualNodes int) *HashRing {
	r, err := NewHashRing(virtualNodes)
	if err != nil {
		panic(err)
	}
	return r
}

// ----- membership of the ring ------------------------------------------------------------

// PointHash is where the i-th virtual node of node sits: hash(node + "#" + i).
func PointHash(node string, replica int) uint64 { return HashKey(fmt.Sprintf("%s#%d", node, replica)) }

// bisectLeft is the first index whose hash is >= h, or len(points) if none.
func (r *HashRing) bisectLeft(h uint64) int {
	return sort.Search(len(r.points), func(i int) bool { return r.points[i].hash >= h })
}

// AddNode places VirtualNodes points for node on the ring.
func (r *HashRing) AddNode(node string) error {
	if r.nodes[node] {
		return fmt.Errorf("%w: %s", ErrNodeExists, node)
	}
	for i := 0; i < r.VirtualNodes; i++ {
		h := PointHash(node, i)
		pos := r.bisectLeft(h)
		// Ties on a 64-bit hash are practically impossible; order them by owner name so every
		// language builds the identical ring.
		for pos < len(r.points) && r.points[pos].hash == h && r.points[pos].owner < node {
			pos++
		}
		r.points = append(r.points, point{})
		copy(r.points[pos+1:], r.points[pos:])
		r.points[pos] = point{h, node}
	}
	r.nodes[node] = true
	return nil
}

// RemoveNode deletes every point owned by node.
func (r *HashRing) RemoveNode(node string) error {
	if !r.nodes[node] {
		return fmt.Errorf("%w: %s", ErrNodeMissing, node)
	}
	kept := r.points[:0]
	for _, p := range r.points {
		if p.owner != node {
			kept = append(kept, p)
		}
	}
	r.points = kept
	delete(r.nodes, node)
	return nil
}

// Nodes returns the physical nodes in sorted order.
func (r *HashRing) Nodes() []string {
	out := make([]string, 0, len(r.nodes))
	for n := range r.nodes {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// PointCount is the number of virtual nodes on the ring.
func (r *HashRing) PointCount() int { return len(r.points) }

// HashAt and OwnerAt expose a point for tests and debugging.
func (r *HashRing) HashAt(i int) uint64  { return r.points[i].hash }
func (r *HashRing) OwnerAt(i int) string { return r.points[i].owner }

// ----- lookups ---------------------------------------------------------------------------

// firstIndexAtOrAfter binary-searches for the first point with hash >= h, wrapping to 0 past the end.
func (r *HashRing) firstIndexAtOrAfter(h uint64) int {
	pos := r.bisectLeft(h)
	if pos == len(r.points) {
		return 0
	}
	return pos
}

// Lookup returns the node that owns key: O(log P) for P points.
func (r *HashRing) Lookup(key string) (string, error) {
	if len(r.points) == 0 {
		return "", ErrEmptyRing
	}
	return r.points[r.firstIndexAtOrAfter(HashKey(key))].owner, nil
}

// LookupN returns the owner plus the next n-1 distinct nodes clockwise: a replication preference
// list. It returns fewer than n if the ring has fewer than n nodes.
func (r *HashRing) LookupN(key string, n int) ([]string, error) {
	if n <= 0 {
		return nil, fmt.Errorf("%w: n must be positive", ErrBadParameter)
	}
	if len(r.points) == 0 {
		return nil, ErrEmptyRing
	}
	start := r.firstIndexAtOrAfter(HashKey(key))
	result := make([]string, 0, n)
	seen := map[string]bool{}
	total := len(r.points)
	for step := 0; step < total; step++ {
		owner := r.points[(start+step)%total].owner
		if !seen[owner] {
			seen[owner] = true
			result = append(result, owner)
			if len(result) == n || len(result) == len(r.nodes) {
				break
			}
		}
	}
	return result, nil
}

// Assign maps every key to its owner. It panics on an empty ring, which is a programming error in a demo.
func (r *HashRing) Assign(keys []string) map[string]string {
	out := make(map[string]string, len(keys))
	for _, k := range keys {
		owner, err := r.Lookup(k)
		if err != nil {
			panic(err)
		}
		out[k] = owner
	}
	return out
}

// ModNSharder is the naive alternative: shard = hash(key) mod N over a sorted node list. Adding a
// node renumbers almost every key.
type ModNSharder struct{ nodes []string }

// NewModNSharder sorts the node list so every process agrees on the numbering.
func NewModNSharder(nodes []string) (*ModNSharder, error) {
	if len(nodes) == 0 {
		return nil, fmt.Errorf("%w: need at least one node", ErrBadParameter)
	}
	sorted := append([]string(nil), nodes...)
	sort.Strings(sorted)
	return &ModNSharder{sorted}, nil
}

// Lookup is hash(key) mod N.
func (s *ModNSharder) Lookup(key string) string { return s.nodes[HashKey(key)%uint64(len(s.nodes))] }

// Assign maps every key to its shard.
func (s *ModNSharder) Assign(keys []string) map[string]string {
	out := make(map[string]string, len(keys))
	for _, k := range keys {
		out[k] = s.Lookup(k)
	}
	return out
}

// ----- measurement -------------------------------------------------------------------------------

// FractionMoved is the share of keys whose owner changed between two assignments of the same key set.
func FractionMoved(before, after map[string]string) float64 {
	if len(before) == 0 {
		return 0
	}
	moved := 0
	for k, owner := range before {
		if after[k] != owner {
			moved++
		}
	}
	return float64(moved) / float64(len(before))
}

// LoadPerNode counts keys per node.
func LoadPerNode(assignment map[string]string, nodes []string) map[string]int {
	counts := make(map[string]int, len(nodes))
	for _, n := range nodes {
		counts[n] = 0
	}
	for _, owner := range assignment {
		counts[owner]++
	}
	return counts
}

// Imbalance returns (max load / mean load, coefficient of variation). 1.0 and 0.0 would be perfect balance.
func Imbalance(assignment map[string]string, nodes []string) (float64, float64) {
	counts := LoadPerNode(assignment, nodes)
	var mean, max float64
	for _, c := range counts {
		mean += float64(c)
		max = math.Max(max, float64(c))
	}
	mean /= float64(len(counts))
	var variance float64
	for _, c := range counts {
		variance += (float64(c) - mean) * (float64(c) - mean)
	}
	variance /= float64(len(counts))
	return max / mean, math.Sqrt(variance) / mean
}

// AccountIDs generates acct-000000 .. acct-<count-1>.
func AccountIDs(count int) []string {
	ids := make([]string, count)
	for i := range ids {
		ids[i] = fmt.Sprintf("acct-%06d", i)
	}
	return ids
}

func ringWith(nodes []string, virtualNodes int) *HashRing {
	r := mustRing(virtualNodes)
	for _, n := range nodes {
		if err := r.AddNode(n); err != nil {
			panic(err)
		}
	}
	return r
}

func main() {
	var shards []string
	for c := 'a'; c <= 'j'; c++ {
		shards = append(shards, "ledger-shard-"+string(c))
	}
	keys := AccountIDs(20_000)

	fmt.Println("== Where an account lands ==")
	ring := ringWith(shards, 100)
	key := "acct-000123"
	fmt.Printf("hashKey(%q) = 0x%016x\n", key, HashKey(key))
	fmt.Printf("%d points on the ring for %d shards\n", ring.PointCount(), len(ring.Nodes()))
	owner, _ := ring.Lookup(key)
	prefs, _ := ring.LookupN(key, 3)
	fmt.Printf("lookup(%q) = %s\n", key, owner)
	fmt.Printf("lookupN(%q, 3) = %v  (owner + 2 replicas)\n", key, prefs)

	fmt.Println("\n== Adding an eleventh shard: how many accounts move? ==")
	beforeRing := ring.Assign(keys)
	modTen, _ := NewModNSharder(shards)
	beforeMod := modTen.Assign(keys)
	_ = ring.AddNode("ledger-shard-k")
	afterRing := ring.Assign(keys)
	modEleven, _ := NewModNSharder(append(append([]string(nil), shards...), "ledger-shard-k"))
	afterMod := modEleven.Assign(keys)
	fmt.Printf("consistent hash ring: %.3f of keys moved (ideal 1/11 = %.3f)\n", FractionMoved(beforeRing, afterRing), 1.0/11)
	fmt.Printf("hash(key) mod N:      %.3f of keys moved (expected 10/11 = %.3f)\n", FractionMoved(beforeMod, afterMod), 10.0/11)
	movedToNew, movedElsewhere := 0, 0
	for _, k := range keys {
		if afterRing[k] != beforeRing[k] {
			if afterRing[k] == "ledger-shard-k" {
				movedToNew++
			} else {
				movedElsewhere++
			}
		}
	}
	fmt.Printf("ring: %d keys moved to the new shard, %d moved anywhere else\n", movedToNew, movedElsewhere)

	fmt.Println("\n== Removing a shard: only its keys move ==")
	_ = ring.RemoveNode("ledger-shard-c")
	afterRemove := ring.Assign(keys)
	stayed, notOnC, onC := 0, 0, 0
	for _, k := range keys {
		if afterRing[k] == "ledger-shard-c" {
			onC++
			continue
		}
		notOnC++
		if afterRemove[k] == afterRing[k] {
			stayed++
		}
	}
	fmt.Printf("%d keys not on shard-c kept their owner (out of %d)\n", stayed, notOnC)
	fmt.Printf("%.3f of keys moved (shard-c held %.3f)\n", FractionMoved(afterRing, afterRemove), float64(onC)/float64(len(keys)))

	fmt.Println("\n== Virtual nodes and balance (10 shards, 20,000 accounts) ==")
	fmt.Printf("%6s %9s %20s\n", "vnodes", "max/mean", "coeff. of variation")
	for _, v := range []int{1, 10, 100, 1000} {
		ratio, cv := Imbalance(ringWith(shards, v).Assign(keys), shards)
		fmt.Printf("%6d %9.3f %20.3f\n", v, ratio, cv)
	}

	fmt.Println("\n== Replication: the preference list for an account ==")
	five := ringWith(shards[:5], 100)
	for _, k := range []string{"acct-000001", "acct-000002"} {
		p, _ := five.LookupN(k, 3)
		fmt.Printf("lookupN(%q, 3) = %v\n", k, p)
	}
	all, _ := five.LookupN("acct-000001", 8)
	fmt.Printf("lookupN with n > nodes returns every node once: %v\n", all)
	if _, err := mustRing(100).Lookup("acct-000001"); err != nil {
		fmt.Println("rejected:", err)
	}
}
