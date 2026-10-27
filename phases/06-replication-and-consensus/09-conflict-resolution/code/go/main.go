// Lesson: phases/06-replication-and-consensus/09-conflict-resolution/docs/en.md
// Conflict Resolution: LWW, Version Vectors, Merge Functions
// Run:  go run .
// Test: go test ./...
// Sources: DeCandia et al., "Dynamo: Amazon's Highly Available Key-value Store" (SOSP 2007), section 4.4;
// Parker et al., "Detection of Mutual Inconsistency in Distributed Systems" (IEEE TSE 1983). Go 1.22+, stdlib only.
// Same design as the Java and Python versions: same types, same method names, same demo.
package main

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Ordering says how two versions relate under the happens-before order.
type Ordering int

const (
	Before Ordering = iota
	After
	Equal
	Concurrent
)

func (o Ordering) String() string { return [...]string{"BEFORE", "AFTER", "EQUAL", "CONCURRENT"}[o] }

// fmtMap prints a map the way Java prints a TreeMap: "{A=1, B=1}".
func fmtMap[V any](m map[string]V) string {
	keys := sortedKeys(m)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%s=%v", k, m[k])
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

// fmtSet prints a set the way Java prints a TreeSet: "[a, b]".
func fmtSet(m map[string]bool) string { return "[" + strings.Join(sortedKeys(m), ", ") + "]" }

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// VersionVector holds per-replica counters. v1 dominates v2 when every entry of v1 >= v2's and at least one is greater.
// Every operation returns a new vector; a VersionVector is immutable in practice.
type VersionVector struct{ clocks map[string]int64 }

func NewVersionVector() VersionVector { return VersionVector{clocks: map[string]int64{}} }

func VersionVectorOf(initial map[string]int64) VersionVector {
	v := NewVersionVector()
	for k, c := range initial {
		v.clocks[k] = c
	}
	return v
}

func (v VersionVector) Copy() VersionVector { return VersionVectorOf(v.clocks) }

func (v VersionVector) Get(replica string) int64 { return v.clocks[replica] }

// Increment returns a new vector with this replica's entry advanced by one.
func (v VersionVector) Increment(replica string) VersionVector {
	out := v.Copy()
	out.clocks[replica]++
	return out
}

// Merge is the pointwise maximum: the smallest vector that dominates both.
func (v VersionVector) Merge(other VersionVector) VersionVector {
	out := v.Copy()
	for k, c := range other.clocks {
		if cur, ok := out.clocks[k]; !ok || c > cur {
			out.clocks[k] = c
		}
	}
	return out
}

func (v VersionVector) Compare(other VersionVector) Ordering {
	less, greater := false, false
	keys := map[string]bool{}
	for k := range v.clocks {
		keys[k] = true
	}
	for k := range other.clocks {
		keys[k] = true
	}
	for k := range keys {
		a, b := v.Get(k), other.Get(k)
		if a < b {
			less = true
		}
		if a > b {
			greater = true
		}
	}
	switch {
	case less && greater:
		return Concurrent
	case less:
		return Before
	case greater:
		return After
	default:
		return Equal
	}
}

func (v VersionVector) Dominates(other VersionVector) bool { return v.Compare(other) == After }
func (v VersionVector) ConcurrentWith(other VersionVector) bool {
	return v.Compare(other) == Concurrent
}

// Equal is Java's equals: the same entries. A struct with a map field has no ==, so it is written by hand.
func (v VersionVector) Equal(other VersionVector) bool {
	if len(v.clocks) != len(other.clocks) {
		return false
	}
	for k, c := range v.clocks {
		if oc, ok := other.clocks[k]; !ok || oc != c {
			return false
		}
	}
	return true
}

func (v VersionVector) String() string { return fmtMap(v.clocks) }

// Dot is a write's identity: the replica that coordinated it and that replica's monotonically increasing counter.
type Dot struct {
	Replica string
	Seq     int64
}

func (d Dot) String() string { return fmt.Sprintf("%s:%d", d.Replica, d.Seq) }

// Versioned is a value, the dot that identifies the write, and the context (version vector) the writer had observed.
// A write is obsolete exactly when some other write's context covers its dot: that writer had seen it.
type Versioned[V any] struct {
	Value   V
	Dot     Dot
	Context VersionVector
}

// Version is the full version vector of this write: everything it saw plus itself.
func (w Versioned[V]) Version() VersionVector {
	v := w.Context.Copy()
	if v.Get(w.Dot.Replica) >= w.Dot.Seq {
		return v
	}
	return v.Merge(VersionVectorOf(map[string]int64{w.Dot.Replica: w.Dot.Seq}))
}

func (w Versioned[V]) ObsoletedBy(other Versioned[V]) bool {
	return other.Context.Get(w.Dot.Replica) >= w.Dot.Seq
}

// Equal is the Java record equality. A dot is minted exactly once and always carries the same value and context, so
// comparing dot and context is the same relation without requiring V to be comparable.
func (w Versioned[V]) Equal(other Versioned[V]) bool {
	return w.Dot == other.Dot && w.Context.Equal(other.Context)
}

func (w Versioned[V]) String() string { return fmt.Sprintf("%v@%s", w.Value, w.Version()) }

// Entry is one LWW register: value, wall-clock timestamp, and the writer that breaks ties.
type Entry[V any] struct {
	Value     V
	Timestamp int64
	Writer    string
}

// Beats: the Java passes null for "no current entry"; Go passes the found flag.
func (e Entry[V]) Beats(other Entry[V], present bool) bool {
	if !present {
		return true
	}
	if e.Timestamp != other.Timestamp {
		return e.Timestamp > other.Timestamp
	}
	return e.Writer > other.Writer
}

// LWWStore is last-writer-wins by wall-clock timestamp (replica id breaks ties). Keeps one value per key.
// Any write that loses the comparison is discarded with no record that it ever happened.
type LWWStore[V any] struct {
	entries   map[string]Entry[V]
	discarded int64
}

func NewLWWStore[V any]() *LWWStore[V] { return &LWWStore[V]{entries: map[string]Entry[V]{}} }

func (s *LWWStore[V]) Put(key string, value V, timestamp int64, writer string) {
	incoming := Entry[V]{value, timestamp, writer}
	current, present := s.entries[key]
	if incoming.Beats(current, present) {
		if present {
			s.discarded++
		}
		s.entries[key] = incoming
	} else {
		s.discarded++
	}
}

// Get returns the value and whether the key exists (the Java returns null).
func (s *LWWStore[V]) Get(key string) (V, bool) {
	e, ok := s.entries[key]
	return e.Value, ok
}

// Discarded counts writes that were overwritten or rejected: the data LWW lost.
func (s *LWWStore[V]) Discarded() int64 { return s.discarded }

func (s *LWWStore[V]) Merge(other *LWWStore[V]) *LWWStore[V] {
	out := NewLWWStore[V]()
	for k, e := range s.entries {
		out.entries[k] = e
	}
	for k, e := range other.entries {
		cur, present := out.entries[k]
		if e.Beats(cur, present) {
			out.entries[k] = e
		}
	}
	return out
}

// MergeFunction is the application-level merge: given every concurrent sibling, produce the one value the application wants.
type MergeFunction[V any] func(siblings []V) V

// ReadResult is a read: every sibling plus the context (merged vector) a client must send back with its next write.
type ReadResult[V any] struct {
	Siblings []Versioned[V]
	Context  VersionVector
}

func (r ReadResult[V]) Values() []V {
	out := make([]V, 0, len(r.Siblings))
	for _, s := range r.Siblings {
		out = append(out, s.Value)
	}
	return out
}

func (r ReadResult[V]) HasConflict() bool { return len(r.Siblings) > 1 }

// SiblingStore is a Dynamo-style store with dotted version vectors: each key holds a set of concurrent siblings. A write
// carries the context the client read; the store drops every sibling that context covers and keeps the rest. Readers
// see every sibling and resolve them with a MergeFunction.
type SiblingStore[V any] struct {
	replica string
	counter int64
	data    map[string][]Versioned[V]
}

func NewSiblingStore[V any](replica string) *SiblingStore[V] {
	return &SiblingStore[V]{replica: replica, data: map[string][]Versioned[V]{}}
}

func (s *SiblingStore[V]) Replica() string { return s.replica }

func (s *SiblingStore[V]) Get(key string) ReadResult[V] {
	sibs := s.data[key]
	ctx := NewVersionVector()
	for _, sib := range sibs {
		ctx = ctx.Merge(sib.Version())
	}
	return ReadResult[V]{append([]Versioned[V](nil), sibs...), ctx}
}

// Put writes with the context the client read. The new write gets a fresh dot from this replica's counter; it obsoletes
// every sibling its context covers and is concurrent with every sibling it does not.
func (s *SiblingStore[V]) Put(key string, value V, context VersionVector) VersionVector {
	s.counter++
	write := Versioned[V]{value, Dot{s.replica, s.counter}, context.Copy()}
	kept := []Versioned[V]{}
	for _, sib := range s.data[key] {
		if !sib.ObsoletedBy(write) {
			kept = append(kept, sib)
		}
	}
	kept = append(kept, write)
	s.data[key] = kept
	return write.Version()
}

// PutBlind writes with no context at all: the write is concurrent with everything already stored.
func (s *SiblingStore[V]) PutBlind(key string, value V) VersionVector {
	return s.Put(key, value, NewVersionVector())
}

// MergeFrom is anti-entropy: take every sibling the other replica holds, keep only the ones nobody has obsoleted.
func (s *SiblingStore[V]) MergeFrom(other *SiblingStore[V]) {
	for _, key := range sortedKeys(other.data) {
		all := append([]Versioned[V](nil), s.data[key]...)
		for _, sib := range other.data[key] {
			if !containsVersioned(all, sib) {
				all = append(all, sib)
			}
		}
		s.data[key] = Maximal(all)
	}
}

// Resolve reads every sibling, resolves with the application merge, writes back with the merged context.
// The flag is false for a missing key (the Java returns null).
func (s *SiblingStore[V]) Resolve(key string, fn MergeFunction[V]) (V, bool) {
	read := s.Get(key)
	if len(read.Siblings) == 0 {
		var zero V
		return zero, false
	}
	merged := read.Siblings[0].Value
	if len(read.Siblings) > 1 {
		merged = fn(read.Values())
		s.Put(key, merged, read.Context)
	}
	return merged, true
}

func (s *SiblingStore[V]) SiblingCount(key string) int { return len(s.data[key]) }

func containsVersioned[V any](list []Versioned[V], w Versioned[V]) bool {
	for _, x := range list {
		if x.Equal(w) {
			return true
		}
	}
	return false
}

// Maximal keeps the siblings that no other sibling's context has observed. The Java compares `other != candidate` by
// reference; here the slice index plays that role, so two equal siblings never obsolete each other.
func Maximal[V any](all []Versioned[V]) []Versioned[V] {
	out := []Versioned[V]{}
	for i, candidate := range all {
		obsolete := false
		for j, other := range all {
			if j != i && candidate.ObsoletedBy(other) {
				obsolete = true
				break
			}
		}
		if !obsolete && !containsVersioned(out, candidate) {
			out = append(out, candidate)
		}
	}
	return out
}

// Cart is a shopping cart: item -> quantity. Immutable value; operations return new carts.
type Cart struct{ Items map[string]int }

func EmptyCart() Cart { return Cart{Items: map[string]int{}} }

func (c Cart) copyItems() map[string]int {
	m := make(map[string]int, len(c.Items))
	for k, v := range c.Items {
		m[k] = v
	}
	return m
}

func (c Cart) Add(item string, qty int) Cart {
	m := c.copyItems()
	m[item] += qty
	return Cart{m}
}

func (c Cart) Remove(item string) Cart {
	m := c.copyItems()
	delete(m, item)
	return Cart{m}
}

func (c Cart) Equal(other Cart) bool {
	if len(c.Items) != len(other.Items) {
		return false
	}
	for k, v := range c.Items {
		if ov, ok := other.Items[k]; !ok || ov != v {
			return false
		}
	}
	return true
}

// UnionMax is Dynamo's cart merge: union of items, max quantity. Removed items can come back.
var UnionMax MergeFunction[Cart] = func(siblings []Cart) Cart {
	m := map[string]int{}
	for _, c := range siblings {
		for item, qty := range c.Items {
			if cur, ok := m[item]; !ok || qty > cur {
				m[item] = qty
			}
		}
	}
	return Cart{m}
}

func (c Cart) String() string { return fmtMap(c.Items) }

// CaseFile is a dispute case file on a payment: free-text notes from agents plus a status. Two agents editing the
// same case during a partition is the ledger-adjacent conflict this lesson resolves.
type CaseFile struct {
	Status string
	Notes  map[string]bool
}

var Severity = []string{"OPEN", "UNDER_REVIEW", "FUNDS_HELD", "CLOSED_REFUNDED"}

// ErrUnknownStatus is returned by WithStatus for a status outside Severity.
var ErrUnknownStatus = errors.New("unknown status")

func severityIndex(s string) int {
	for i, x := range Severity {
		if x == s {
			return i
		}
	}
	return -1
}

func OpenCase() CaseFile { return CaseFile{"OPEN", map[string]bool{}} }

func (c CaseFile) copyNotes() map[string]bool {
	n := make(map[string]bool, len(c.Notes))
	for k := range c.Notes {
		n[k] = true
	}
	return n
}

func (c CaseFile) WithNote(note string) CaseFile {
	n := c.copyNotes()
	n[note] = true
	return CaseFile{c.Status, n}
}

func (c CaseFile) WithStatus(s string) (CaseFile, error) {
	if severityIndex(s) < 0 {
		return CaseFile{}, fmt.Errorf("%w %s", ErrUnknownStatus, s)
	}
	return CaseFile{s, c.copyNotes()}, nil
}

// MustStatus is WithStatus for literal statuses in the demo and tests.
func (c CaseFile) MustStatus(s string) CaseFile {
	out, err := c.WithStatus(s)
	if err != nil {
		panic(err)
	}
	return out
}

func (c CaseFile) Equal(other CaseFile) bool {
	if c.Status != other.Status || len(c.Notes) != len(other.Notes) {
		return false
	}
	for n := range c.Notes {
		if !other.Notes[n] {
			return false
		}
	}
	return true
}

// UnionNotesStrictestStatus is the application merge: keep every note (union), and for status pick the most
// restrictive one so a partition can never silently release held funds. The rule is a business decision made explicit.
var UnionNotesStrictestStatus MergeFunction[CaseFile] = func(siblings []CaseFile) CaseFile {
	notes := map[string]bool{}
	status := "OPEN"
	for _, c := range siblings {
		for n := range c.Notes {
			notes[n] = true
		}
		if severityIndex(c.Status) > severityIndex(status) {
			status = c.Status
		}
	}
	return CaseFile{status, notes}
}

func (c CaseFile) String() string { return c.Status + " notes=" + fmtSet(c.Notes) }

func main() {
	fmt.Println("== Version vectors: what 'concurrent' means ==")
	base := NewVersionVector()
	a1 := base.Increment("A") // {A=1}
	a1b1 := a1.Increment("B") // {A=1, B=1}: B wrote after seeing A's write
	a2 := a1.Increment("A")   // {A=2}: A wrote again without seeing B
	fmt.Printf("%s vs %s -> %s\n", a1, a1b1, a1.Compare(a1b1))
	fmt.Printf("%s vs %s -> %s  (neither saw the other: a real conflict)\n", a1b1, a2, a1b1.Compare(a2))
	fmt.Printf("merge = %s  (the context a resolver writes back with)\n", a1b1.Merge(a2))

	fmt.Println()
	fmt.Println("== LWW: deterministic, and it throws one write away ==")
	east, west := NewLWWStore[string](), NewLWWStore[string]()
	east.Put("case-9021/note", "agent-1: customer says card was stolen on 3 Oct", 1000, "east")
	west.Put("case-9021/note", "agent-2: merchant provided signed delivery receipt", 1002, "west")
	lww := east.Merge(west)
	v, _ := lww.Get("case-9021/note")
	fmt.Printf("east wrote at t=1000, west wrote at t=1002 -> merged value: \"%s\"\n", v)
	fmt.Println("agent-1's note is gone; nothing in the store records that it existed")
	skewed := NewLWWStore[string]()
	skewed.Put("balance-note", "newer write, but this node's clock is 5s behind", 995, "slow-clock")
	skewed.Put("balance-note", "older write from a node with a fast clock", 1000, "fast-clock")
	v, _ = skewed.Get("balance-note")
	fmt.Printf("clock skew: \"%s\" wins although it happened first\n", v)

	fmt.Println()
	fmt.Println("== Siblings: keep both, make the application decide ==")
	sEast, sWest := NewSiblingStore[CaseFile]("east"), NewSiblingStore[CaseFile]("west")
	sEast.Put("case-9021", OpenCase(), NewVersionVector())
	sWest.MergeFrom(sEast) // both replicas hold version {east=1}
	// partition: each agent reads {east=1}, edits, writes back with that context
	eastRead := sEast.Get("case-9021")
	sEast.Put("case-9021", eastRead.Siblings[0].Value.WithNote("agent-1: card reported stolen 3 Oct").MustStatus("FUNDS_HELD"), eastRead.Context)
	westRead := sWest.Get("case-9021")
	sWest.Put("case-9021", westRead.Siblings[0].Value.WithNote("agent-2: merchant has signed receipt").MustStatus("UNDER_REVIEW"), westRead.Context)
	sEast.MergeFrom(sWest)
	conflicted := sEast.Get("case-9021")
	fmt.Printf("after anti-entropy east holds %d siblings:\n", len(conflicted.Siblings))
	for _, s := range conflicted.Siblings {
		fmt.Printf("  %s\n", s)
	}
	resolved, _ := sEast.Resolve("case-9021", UnionNotesStrictestStatus)
	fmt.Printf("application merge (union notes, strictest status): %s\n", resolved)
	fmt.Printf("siblings after resolve: %d, version %s\n", sEast.SiblingCount("case-9021"), sEast.Get("case-9021").Context)

	fmt.Println()
	fmt.Println("== The shopping-cart anomaly from the Dynamo paper ==")
	cA, cB := NewSiblingStore[Cart]("A"), NewSiblingStore[Cart]("B")
	cA.Put("cart-7", EmptyCart().Add("book", 1).Add("pen", 2), NewVersionVector())
	cB.MergeFrom(cA)
	ra := cA.Get("cart-7")
	cA.Put("cart-7", ra.Siblings[0].Value.Remove("pen"), ra.Context) // A deletes pen
	rb := cB.Get("cart-7")
	cB.Put("cart-7", rb.Siblings[0].Value.Add("book", 1), rb.Context) // B adds another book
	cA.MergeFrom(cB)
	merged, _ := cA.Resolve("cart-7", UnionMax)
	fmt.Printf("A removed pen, B concurrently added a book -> union/max merge: %s\n", merged)
	fmt.Println("the deleted pen is back: union of states cannot see a deletion (an OR-Set, Lesson 08, can)")
}
