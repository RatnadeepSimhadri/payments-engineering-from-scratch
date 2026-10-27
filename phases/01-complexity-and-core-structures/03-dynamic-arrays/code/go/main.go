// Lesson: phases/01-complexity-and-core-structures/03-dynamic-arrays/docs/en.md
// Dynamic Arrays from Scratch
// Run:  go run .
// Test: go test ./...
// Sources: Cormen et al., Introduction to Algorithms, ch. 17 (amortized analysis of table doubling);
//
//	OpenJDK java.util.ArrayList.grow() (1.5x policy); Facebook folly FBVector.md (why 1.5x).
//
// Go 1.22+, standard library only. Same design as the Java and Python versions.
package main

import (
	"errors"
	"fmt"
	"strings"
)

// GrowthPolicy says how capacity grows when the backing array is full. The factor is the whole trade-off:
// a larger factor means fewer copies but more wasted memory and less chance to reuse freed blocks.
type GrowthPolicy int

const (
	Grow1_5X GrowthPolicy = iota
	Grow2X
)

// Policies lists every policy in declaration order, like GrowthPolicy.values().
var Policies = []GrowthPolicy{Grow1_5X, Grow2X}

func (p GrowthPolicy) String() string {
	if p == Grow1_5X {
		return "GROW_1_5X"
	}
	return "GROW_2X"
}

// Next is the capacity after one reallocation from capacity.
func (p GrowthPolicy) Next(capacity int) int {
	if p == Grow1_5X {
		// capacity + capacity/2, but always at least +1 so tiny arrays still grow (1 -> 2, 2 -> 3).
		return max(capacity+1, capacity+(capacity>>1))
	}
	return max(capacity+1, capacity<<1)
}

// ArrayStats are counters that make the amortized argument measurable rather than believed.
type ArrayStats struct {
	Reallocations  int64 // how many times a new backing array was allocated
	ElementsCopied int64 // total elements moved between backing arrays (grow + shrink)
	Shrinks        int64 // how many of the reallocations were shrinks
	PeakCapacity   int   // largest backing array ever held
}

func (s ArrayStats) String() string {
	return fmt.Sprintf("reallocations=%d copied=%d shrinks=%d peakCapacity=%d", s.Reallocations, s.ElementsCopied, s.Shrinks, s.PeakCapacity)
}

// MinCapacity is the floor the shrink policy never goes below.
const MinCapacity = 4

var (
	// ErrIndexOutOfBounds is returned for an index outside 0..size-1 (or 0..size for Insert).
	ErrIndexOutOfBounds = errors.New("index out of bounds")
	// ErrEmpty is returned by RemoveLast on an empty array.
	ErrEmpty = errors.New("empty")
)

// DynamicArray is a growable array with explicit capacity, a pluggable growth factor, and a shrink policy with hysteresis.
// Invariant: 0 <= size <= capacity, elements[0..size) are live, elements[size..capacity) hold the zero value.
// Every public operation preserves it; the tests check it after every mutation.
type DynamicArray[T any] struct {
	elements      []T // len(elements) is the capacity
	size          int
	policy        GrowthPolicy
	shrinkEnabled bool
	stats         ArrayStats
}

// NewDynamicArray creates an array with shrinking enabled.
func NewDynamicArray[T any](initialCapacity int, policy GrowthPolicy) (*DynamicArray[T], error) {
	return NewDynamicArrayShrink[T](initialCapacity, policy, true)
}

// NewDynamicArrayShrink creates an array and chooses whether it shrinks.
func NewDynamicArrayShrink[T any](initialCapacity int, policy GrowthPolicy, shrinkEnabled bool) (*DynamicArray[T], error) {
	if initialCapacity < 1 {
		return nil, errors.New("initialCapacity must be >= 1")
	}
	a := &DynamicArray[T]{elements: make([]T, initialCapacity), policy: policy, shrinkEnabled: shrinkEnabled}
	a.stats.PeakCapacity = initialCapacity
	return a, nil
}

func mustArray[T any](initialCapacity int, policy GrowthPolicy, shrinkEnabled bool) *DynamicArray[T] {
	a, err := NewDynamicArrayShrink[T](initialCapacity, policy, shrinkEnabled)
	if err != nil {
		panic(err)
	}
	return a
}

func (a *DynamicArray[T]) Size() int         { return a.size }
func (a *DynamicArray[T]) Capacity() int     { return len(a.elements) }
func (a *DynamicArray[T]) IsEmpty() bool     { return a.size == 0 }
func (a *DynamicArray[T]) Stats() ArrayStats { return a.stats }

// Add appends. O(1) amortized: a copy of all size elements happens only when size == capacity.
func (a *DynamicArray[T]) Add(value T) {
	if a.size == len(a.elements) {
		a.resize(a.policy.Next(len(a.elements)))
	}
	a.elements[a.size] = value
	a.size++
}

func (a *DynamicArray[T]) checkIndex(index int) error {
	if index < 0 || index >= a.size { // size, not capacity: a slot past size is not an element
		return fmt.Errorf("%w: index %d size %d", ErrIndexOutOfBounds, index, a.size)
	}
	return nil
}

// Get reads the element at index.
func (a *DynamicArray[T]) Get(index int) (T, error) {
	var zero T
	if err := a.checkIndex(index); err != nil {
		return zero, err
	}
	return a.elements[index], nil
}

// Set replaces the element at index and returns the old one.
func (a *DynamicArray[T]) Set(index int, value T) (T, error) {
	var zero T
	if err := a.checkIndex(index); err != nil {
		return zero, err
	}
	old := a.elements[index]
	a.elements[index] = value
	return old, nil
}

// Insert at index, shifting the tail right. O(n - index): the cost the ring buffer lesson avoids.
func (a *DynamicArray[T]) Insert(index int, value T) error {
	if index < 0 || index > a.size {
		return fmt.Errorf("%w: index %d size %d", ErrIndexOutOfBounds, index, a.size)
	}
	if a.size == len(a.elements) {
		a.resize(a.policy.Next(len(a.elements)))
	}
	copy(a.elements[index+1:a.size+1], a.elements[index:a.size])
	a.elements[index] = value
	a.size++
	return nil
}

// Remove at index, shifting the tail left, zeroing the vacated slot so the GC can reclaim it.
func (a *DynamicArray[T]) Remove(index int) (T, error) {
	var zero T
	if err := a.checkIndex(index); err != nil {
		return zero, err
	}
	old := a.elements[index]
	moved := a.size - index - 1
	if moved > 0 {
		copy(a.elements[index:], a.elements[index+1:a.size])
	}
	a.size--
	a.elements[a.size] = zero
	a.maybeShrink()
	return old, nil
}

// RemoveLast pops the final element.
func (a *DynamicArray[T]) RemoveLast() (T, error) {
	if a.size == 0 {
		var zero T
		return zero, ErrEmpty
	}
	return a.Remove(a.size - 1)
}

// Clear drops every element and applies the shrink policy.
func (a *DynamicArray[T]) Clear() {
	var zero T
	for i := 0; i < a.size; i++ {
		a.elements[i] = zero
	}
	a.size = 0
	a.maybeShrink()
}

// maybeShrink shrinks only when the array is less than a quarter full, and only to half capacity.
// The gap between the shrink trigger (1/4) and the post-shrink load (1/2) is the hysteresis:
// after a shrink you need capacity/2 more appends before the next grow, and capacity/4
// more removals before the next shrink, so an add/remove sequence at the boundary cannot thrash.
func (a *DynamicArray[T]) maybeShrink() {
	if !a.shrinkEnabled {
		return
	}
	capacity := len(a.elements)
	if capacity > MinCapacity && int64(a.size)*4 < int64(capacity) {
		target := max(MinCapacity, capacity/2)
		if target < capacity {
			a.stats.Shrinks++
			a.resize(target)
		}
	}
}

func (a *DynamicArray[T]) resize(newCapacity int) {
	if newCapacity < a.size {
		panic("cannot shrink below size")
	}
	fresh := make([]T, newCapacity)
	copy(fresh, a.elements[:a.size])
	a.elements = fresh
	a.stats.Reallocations++
	a.stats.ElementsCopied += int64(a.size)
	if newCapacity > a.stats.PeakCapacity {
		a.stats.PeakCapacity = newCapacity
	}
}

// CapacitySequence is the capacity sequence a policy produces from start, until it first reaches target.
func CapacitySequence(policy GrowthPolicy, start, target int) []int {
	seq := mustArray[int](8, Grow2X, false)
	c := start
	seq.Add(c)
	for c < target {
		c = policy.Next(c)
		seq.Add(c)
	}
	out := make([]int, seq.Size())
	for i := range out {
		out[i], _ = seq.Get(i)
	}
	return out
}

// Each visits the live prefix in order; the Go spelling of Iterable.
func (a *DynamicArray[T]) Each(visit func(value T)) {
	for i := 0; i < a.size; i++ {
		visit(a.elements[i])
	}
}

func (a *DynamicArray[T]) String() string {
	var sb strings.Builder
	sb.WriteString("[")
	for i := 0; i < a.size; i++ {
		if i > 0 {
			sb.WriteString(", ")
		}
		fmt.Fprint(&sb, a.elements[i])
	}
	fmt.Fprintf(&sb, "] size=%d capacity=%d", a.size, len(a.elements))
	return sb.String()
}

// MeasureAppends appends n values under a policy and returns the counters.
func MeasureAppends(policy GrowthPolicy, n, initialCapacity int) ArrayStats {
	array := mustArray[int](initialCapacity, policy, false)
	for i := 0; i < n; i++ {
		array.Add(i)
	}
	return array.Stats()
}

func formatInts(xs []int) string {
	parts := make([]string, len(xs))
	for i, x := range xs {
		parts[i] = fmt.Sprint(x)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func main() {
	fmt.Println("== Capacity versus size ==")
	postings := mustArray[string](4, Grow2X, true)
	for _, p := range []string{"debit:cash:1999", "credit:revenue:1941", "credit:fees:58"} {
		postings.Add(p)
		fmt.Printf("add %s -> size=%d capacity=%d\n", p, postings.Size(), postings.Capacity())
	}
	postings.Add("debit:cash:500")
	postings.Add("credit:revenue:500")
	fmt.Println("5th add forced a grow: " + postings.String())
	fmt.Println()

	fmt.Println("== Capacity sequences from 8 to 1,000,000 ==")
	for _, policy := range Policies {
		seq := CapacitySequence(policy, 8, 1_000_000)
		fmt.Printf("%v: %d reallocations, final capacity %d\n", policy, len(seq)-1, seq[len(seq)-1])
		fmt.Println("  first ten: " + formatInts(seq[:min(10, len(seq))]))
	}
	fmt.Println()

	fmt.Println("== Measured copies for 1,000,000 appends (initial capacity 8) ==")
	n := 1_000_000
	for _, policy := range Policies {
		s := MeasureAppends(policy, n, 8)
		fmt.Printf("%v: %v  copies/append=%.3f  slack=%.1f%%\n", policy, s,
			float64(s.ElementsCopied)/float64(n), 100.0*float64(s.PeakCapacity-n)/float64(n))
	}
	fmt.Println("Both are O(1) amortized; the factor moves the constant between 'fewer copies' and 'less slack'.")
	fmt.Println()

	fmt.Println("== Can freed blocks be reused? (sum of earlier capacities vs the next one) ==")
	for _, policy := range Policies {
		seq := CapacitySequence(policy, 8, 200)
		freed := int64(0)
		var sb strings.Builder
		for i := 1; i < len(seq); i++ {
			freed += int64(seq[i-1])
			verdict := "(no)"
			if freed >= int64(seq[i]) {
				verdict = "(fits)"
			}
			fmt.Fprintf(&sb, "%d%s ", seq[i], verdict)
		}
		fmt.Printf("%v: %s\n", policy, strings.TrimSpace(sb.String()))
	}
	fmt.Println()

	fmt.Println("== Shrink with hysteresis: add/remove at the boundary does not thrash ==")
	queue := mustArray[int](4, Grow2X, true)
	for i := 0; i < 64; i++ {
		queue.Add(i)
	}
	fmt.Printf("after 64 adds: size=%d capacity=%d %v\n", queue.Size(), queue.Capacity(), queue.Stats())
	for queue.Size() > 16 {
		_, _ = queue.RemoveLast()
	}
	fmt.Printf("down to 16:    size=%d capacity=%d %v\n", queue.Size(), queue.Capacity(), queue.Stats())
	_, _ = queue.RemoveLast()
	fmt.Printf("down to 15:    size=%d capacity=%d (15*4 < 64 -> shrink to 32)\n", queue.Size(), queue.Capacity())
	before := queue.Stats().Reallocations
	for i := 0; i < 1000; i++ {
		queue.Add(i)
		_, _ = queue.RemoveLast()
	}
	fmt.Printf("1000 add/remove pairs at the boundary: reallocations added = %d\n", queue.Stats().Reallocations-before)
}
