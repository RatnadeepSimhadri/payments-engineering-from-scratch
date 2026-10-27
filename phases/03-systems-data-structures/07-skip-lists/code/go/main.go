// Lesson: phases/03-systems-data-structures/07-skip-lists/docs/en.md
// Skip Lists
// Run:  go run .
// Test: go test ./...
// Sources: Pugh (1990), "Skip Lists: A Probabilistic Alternative to Balanced Trees", CACM 33(6);
// Redis t_zset.c (zslInsert, zslDeleteNode, zslGetRank); java.util.concurrent.ConcurrentSkipListMap.
// Go 1.22+, standard library only. Same design as the Java and Python versions.
package main

import (
	"cmp"
	"errors"
	"fmt"
	"math"
	"strings"
)

var (
	// ErrInvalidArgument is returned where the Java throws IllegalArgumentException.
	ErrInvalidArgument = errors.New("invalid argument")
	// ErrInvariantViolated is returned by CheckInvariants where the Java throws IllegalStateException.
	ErrInvariantViolated = errors.New("invariant violated")
)

// Node of the skip list. forward[i] is the next node at level i; span[i] is the number of
// level-0 steps forward[i] jumps over.
type Node[K cmp.Ordered, V any] struct {
	Key     K
	Value   V
	forward []*Node[K, V]
	span    []int
}

func newNode[K cmp.Ordered, V any](key K, value V, levels int) *Node[K, V] {
	return &Node[K, V]{Key: key, Value: value, forward: make([]*Node[K, V], levels), span: make([]int, levels)}
}

// SkipList with Redis-style spans so that rank queries are O(log n). Levels are chosen by a coin
// flip with probability p per extra level, from an injected RNG so every run is reproducible.
type SkipList[K cmp.Ordered, V any] struct {
	MaxLevel    int
	P           float64
	levelSource func() int // returns a level in [1, MaxLevel]
	header      *Node[K, V]
	level       int // levels currently in use
	size        int
	SearchSteps int64 // pointer advances across all searches, for the complexity measurement
}

func NewSkipList[K cmp.Ordered, V any](maxLevel int, p float64, rng *JavaRandom) (*SkipList[K, V], error) {
	if maxLevel < 1 || maxLevel > 64 {
		return nil, fmt.Errorf("%w: maxLevel must be 1..64: %d", ErrInvalidArgument, maxLevel)
	}
	if !(p > 0 && p < 1) {
		return nil, fmt.Errorf("%w: p must be in (0,1): %g", ErrInvalidArgument, p)
	}
	return NewSkipListWithLevelSource[K, V](maxLevel, p, func() int {
		lvl := 1
		for lvl < maxLevel && rng.NextDouble() < p {
			lvl++
		}
		return lvl
	}), nil
}

// NewSkipListWithLevelSource is for the degenerate-case demo: a list whose "coin" is supplied by the caller.
func NewSkipListWithLevelSource[K cmp.Ordered, V any](maxLevel int, p float64, levelSource func() int) *SkipList[K, V] {
	var zeroK K
	var zeroV V
	return &SkipList[K, V]{MaxLevel: maxLevel, P: p, levelSource: levelSource, header: newNode(zeroK, zeroV, maxLevel), level: 1}
}

func DefaultMaxLevel(expectedSize int, p float64) int {
	return max(1, int(math.Ceil(math.Log(float64(expectedSize))/math.Log(1.0/p))))
}

func (s *SkipList[K, V]) RandomLevel() int { return s.levelSource() }

// ----- search ------------------------------------------------------------------------------

// Get counts one step per forward move and one per level descended: the comparisons Pugh's analysis bounds.
func (s *SkipList[K, V]) Get(key K) (V, bool) {
	x := s.header
	for i := s.level - 1; i >= 0; i-- {
		for x.forward[i] != nil && x.forward[i].Key < key {
			x = x.forward[i]
			s.SearchSteps++
		}
		s.SearchSteps++ // the comparison that failed, sending us down a level
	}
	x = x.forward[0]
	if x != nil && x.Key == key {
		return x.Value, true
	}
	var zero V
	return zero, false
}

func (s *SkipList[K, V]) ContainsKey(key K) bool {
	_, ok := s.Get(key)
	return ok
}

// ----- insert ------------------------------------------------------------------------------

func (s *SkipList[K, V]) Put(key K, value V) {
	update := make([]*Node[K, V], s.MaxLevel)
	rank := make([]int, s.MaxLevel)
	x := s.header
	for i := s.level - 1; i >= 0; i-- {
		if i == s.level-1 {
			rank[i] = 0
		} else {
			rank[i] = rank[i+1]
		}
		for x.forward[i] != nil && x.forward[i].Key < key {
			rank[i] += x.span[i]
			x = x.forward[i]
		}
		update[i] = x
	}
	if existing := x.forward[0]; existing != nil && existing.Key == key {
		existing.Value = value
		return
	}

	lvl := s.RandomLevel()
	if lvl > s.level {
		for i := s.level; i < lvl; i++ {
			rank[i] = 0
			update[i] = s.header
			update[i].span[i] = s.size // the header's new level spans the whole list so far
		}
		s.level = lvl
	}
	node := newNode(key, value, lvl)
	for i := 0; i < lvl; i++ {
		node.forward[i] = update[i].forward[i]
		update[i].forward[i] = node
		node.span[i] = update[i].span[i] - (rank[0] - rank[i])
		update[i].span[i] = (rank[0] - rank[i]) + 1
	}
	for i := lvl; i < s.level; i++ {
		update[i].span[i]++ // levels above the new node now jump over one more
	}
	s.size++
}

// ----- delete ------------------------------------------------------------------------------

func (s *SkipList[K, V]) Remove(key K) bool {
	update := make([]*Node[K, V], s.MaxLevel)
	x := s.header
	for i := s.level - 1; i >= 0; i-- {
		for x.forward[i] != nil && x.forward[i].Key < key {
			x = x.forward[i]
		}
		update[i] = x
	}
	x = x.forward[0]
	if x == nil || x.Key != key {
		return false
	}
	for i := 0; i < s.level; i++ {
		if update[i].forward[i] == x {
			update[i].span[i] += x.span[i] - 1
			update[i].forward[i] = x.forward[i]
		} else {
			update[i].span[i]--
		}
	}
	for s.level > 1 && s.header.forward[s.level-1] == nil {
		s.level--
	}
	s.size--
	return true
}

// ----- rank and range: the sorted-set operations ---------------------------------------------

// Rank is the 1-based position of key in sorted order, or 0 if absent. O(log n) thanks to spans.
func (s *SkipList[K, V]) Rank(key K) int {
	traversed := 0
	x := s.header
	for i := s.level - 1; i >= 0; i-- {
		for x.forward[i] != nil && x.forward[i].Key <= key {
			traversed += x.span[i]
			x = x.forward[i]
		}
		if x != s.header && x.Key == key {
			return traversed
		}
	}
	return 0
}

// ByRank is the key at 1-based rank r, or false. O(log n).
func (s *SkipList[K, V]) ByRank(r int) (K, bool) {
	var zero K
	if r < 1 || r > s.size {
		return zero, false
	}
	traversed := 0
	x := s.header
	for i := s.level - 1; i >= 0; i-- {
		for x.forward[i] != nil && traversed+x.span[i] <= r {
			traversed += x.span[i]
			x = x.forward[i]
		}
		if traversed == r {
			return x.Key, true
		}
	}
	return zero, false
}

// Range returns the keys in [lo, hi]: descend to the first key >= lo, then walk level 0. O(log n + k).
func (s *SkipList[K, V]) Range(lo, hi K) []K {
	out := []K{}
	x := s.header
	for i := s.level - 1; i >= 0; i-- {
		for x.forward[i] != nil && x.forward[i].Key < lo {
			x = x.forward[i]
		}
	}
	for x = x.forward[0]; x != nil && x.Key <= hi; x = x.forward[0] {
		out = append(out, x.Key)
	}
	return out
}

func (s *SkipList[K, V]) Keys() []K {
	out := make([]K, 0, s.size)
	for x := s.header.forward[0]; x != nil; x = x.forward[0] {
		out = append(out, x.Key)
	}
	return out
}

// ----- shape -------------------------------------------------------------------------------

func (s *SkipList[K, V]) Size() int  { return s.size }
func (s *SkipList[K, V]) Level() int { return s.level }

// LevelHistogram: how many nodes reach each level: index i holds the count of nodes with at least i+1 levels.
func (s *SkipList[K, V]) LevelHistogram() []int {
	h := make([]int, s.level)
	for i := 0; i < s.level; i++ {
		for x := s.header.forward[i]; x != nil; x = x.forward[i] {
			h[i]++
		}
	}
	return h
}

// ExpectedSteps is Pugh's expected search cost: about log_(1/p)(n) / p comparisons (forward moves plus level descents).
func ExpectedSteps(n int, p float64) float64 { return math.Log(float64(n)) / math.Log(1.0/p) / p }

// CheckInvariants: level 0 is sorted and holds every key; each higher level is a subsequence of the level below;
// every span equals the level-0 distance it claims; no node exists above the current level.
func (s *SkipList[K, V]) CheckInvariants() error {
	base := s.Keys()
	if len(base) != s.size {
		return fmt.Errorf("%w: level 0 holds %d keys, size says %d", ErrInvariantViolated, len(base), s.size)
	}
	for i := 1; i < len(base); i++ {
		if base[i-1] >= base[i] {
			return fmt.Errorf("%w: level 0 not sorted at %v", ErrInvariantViolated, base[i])
		}
	}
	for i := 0; i < s.level; i++ {
		position := 0 // 1-based rank of x; header is 0
		for x := s.header; x.forward[i] != nil; x = x.forward[i] {
			target := position + x.span[i]
			if target < 1 || target > s.size || base[target-1] != x.forward[i].Key {
				after := "header"
				if x != s.header {
					after = fmt.Sprint(x.Key)
				}
				return fmt.Errorf("%w: span mismatch at level %d after %s", ErrInvariantViolated, i, after)
			}
			position = target
		}
		if i == 0 && position != s.size {
			return fmt.Errorf("%w: level 0 spans do not reach the end", ErrInvariantViolated)
		}
	}
	for i := s.level; i < s.MaxLevel; i++ {
		if s.header.forward[i] != nil {
			return fmt.Errorf("%w: node above current level %d", ErrInvariantViolated, i)
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

func joinInts[T any](xs []T) string {
	parts := make([]string, len(xs))
	for i, x := range xs {
		parts[i] = fmt.Sprint(x)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func main() {
	rng := NewJavaRandom(2024)

	fmt.Println("== Merchant risk scores as a sorted set (Redis ZADD / ZRANK / ZRANGE) ==")
	scores := must(NewSkipList[int, string](16, 0.5, rng))
	sample := []int{742, 118, 905, 367, 512, 288, 674, 59, 831, 456}
	for i, k := range sample {
		scores.Put(k, fmt.Sprintf("merchant_%d", i))
	}
	fmt.Println("sorted: " + joinInts(scores.Keys()))
	at3, _ := scores.ByRank(3)
	fmt.Printf("rank of 512 = %d, key at rank 3 = %d, scores in [300, 700] = %s\n", scores.Rank(512), at3, joinInts(scores.Range(300, 700)))
	fmt.Printf("levels in use: %d, nodes per level: %s\n", scores.Level(), joinInts(scores.LevelHistogram()))

	fmt.Println()
	fmt.Println("== 100,000 keys: the geometric level distribution ==")
	n := 100_000
	big := must(NewSkipList[int, int](DefaultMaxLevel(n, 0.5), 0.5, rng))
	keys := make([]int, n)
	for i := range keys {
		keys[i] = i
	}
	rng.Shuffle(len(keys), func(i, j int) { keys[i], keys[j] = keys[j], keys[i] })
	for _, k := range keys {
		big.Put(k, k)
	}
	mustHold(big.CheckInvariants())
	hist := big.LevelHistogram()
	fmt.Printf("maxLevel=%d levels in use=%d\n", big.MaxLevel, big.Level())
	for i := 0; i < min(len(hist), 8); i++ {
		fmt.Printf("level %2d: %6d nodes  (expected n/2^%d = %6.0f)\n", i, hist[i], i, float64(n)/math.Pow(2, float64(i)))
	}

	fmt.Println()
	fmt.Println("== Search cost: measured pointer steps vs Pugh's bound ==")
	big.SearchSteps = 0
	for i := 0; i < 10_000; i++ {
		big.Get(int(rng.NextIntN(int32(n))))
	}
	fmt.Printf("average comparisons per get = %.1f, Pugh's expectation log2(n)/p = %.1f, log2(n) = %.1f\n",
		float64(big.SearchSteps)/10_000.0, ExpectedSteps(n, 0.5), math.Log(float64(n))/math.Log(2))

	fmt.Println()
	fmt.Println("== Sorted inserts do not hurt: the shape depends on coins, not on keys ==")
	sorted := must(NewSkipList[int, int](17, 0.5, NewJavaRandom(7)))
	for i := 0; i < n; i++ {
		sorted.Put(i, i)
	}
	sorted.SearchSteps = 0
	for i := 0; i < 10_000; i++ {
		sorted.Get(i * 10)
	}
	fmt.Printf("sorted input: levels=%d, average steps per get = %.1f\n", sorted.Level(), float64(sorted.SearchSteps)/10_000.0)

	fmt.Println()
	fmt.Println("== What the coin buys: a list whose coin always says 1 is a linked list ==")
	flat := NewSkipListWithLevelSource[int, int](16, 0.5, func() int { return 1 })
	for i := 0; i < 20_000; i++ {
		flat.Put(i, i)
	}
	flat.SearchSteps = 0
	for i := 0; i < 100; i++ {
		flat.Get(int(rng.NextIntN(20_000)))
	}
	fmt.Printf("level=%d, average steps per get = %.0f (n/2 = %d)\n", flat.Level(), float64(flat.SearchSteps)/100.0, 10_000)

	fmt.Println()
	fmt.Println("== Removes keep spans and ranks consistent ==")
	for k := 0; k < n; k += 2 {
		big.Remove(k)
	}
	mustHold(big.CheckInvariants())
	r1, _ := big.ByRank(1)
	r50000, _ := big.ByRank(50_000)
	fmt.Printf("after removing even keys: size=%d, rank(99999)=%d, byRank(1)=%d, byRank(50000)=%d\n",
		big.Size(), big.Rank(99_999), r1, r50000)
}
