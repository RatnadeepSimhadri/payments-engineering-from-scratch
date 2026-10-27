// Lesson: phases/06-replication-and-consensus/08-crdts/docs/en.md
// CRDTs: Counters, Sets, and Convergence
// Run:  go run .
// Test: go test ./...
// Sources: Shapiro, Preguica, Baquero, Zawirski, "A comprehensive study of Convergent and Commutative
// Replicated Data Types" (INRIA RR-7506, 2011); Money type from Phase 10 Lesson 01. Go 1.22+, stdlib only.
// Same design as the Java and Python versions; java.util.Random is ported bit for bit so the demo matches.
package main

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// Crdt is a state-based (convergent) replicated data type. Merge must be a join on a lattice: commutative,
// associative, and idempotent. Every implementation in this file returns a NEW value from Merge and leaves both
// inputs untouched, so the laws can be tested on values directly. Equal is what Java's equals() does.
type Crdt[T any] interface {
	Merge(other T) T
	Equal(other T) bool
}

// ErrOverflow is Java's ArithmeticException from Math.addExact / subtractExact.
var ErrOverflow = errors.New("long overflow")

// ErrNegativeIncrement: a G-Counter only grows.
var ErrNegativeIncrement = errors.New("a G-Counter only grows")

func addExact(a, b int64) (int64, error) {
	sum := a + b
	if (a > 0 && b > 0 && sum < 0) || (a < 0 && b < 0 && sum >= 0) {
		return 0, ErrOverflow
	}
	return sum, nil
}

func subtractExact(a, b int64) (int64, error) {
	diff := a - b
	if (a >= 0 && b < 0 && diff < 0) || (a < 0 && b > 0 && diff >= 0) {
		return 0, ErrOverflow
	}
	return diff, nil
}

func mustAdd(a, b int64) int64 {
	sum, err := addExact(a, b)
	if err != nil {
		panic(err)
	}
	return sum
}

// fmtMap prints a map the way Java prints a TreeMap: "{A=3, B=6}".
func fmtMap(m map[string]int64) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + "=" + strconv.FormatInt(m[k], 10)
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

// GCounter is a grow-only counter: one monotonically increasing slot per replica; value is the sum of slots.
type GCounter struct{ slots map[string]int64 }

func NewGCounter() *GCounter { return &GCounter{slots: map[string]int64{}} }

func NewGCounterFrom(initial map[string]int64) *GCounter {
	g := NewGCounter()
	for k, v := range initial {
		g.slots[k] = v
	}
	return g
}

func (g *GCounter) Increment(replica string, by int64) error {
	if by < 0 {
		return fmt.Errorf("%w; got %d", ErrNegativeIncrement, by)
	}
	sum, err := addExact(g.slots[replica], by)
	if err != nil {
		return err
	}
	g.slots[replica] = sum
	return nil
}

// Value sums the slots; the sum overflowing int64 is a broken invariant and panics, as Math.addExact throws.
func (g *GCounter) Value() int64 {
	var total int64
	for _, v := range g.slots {
		total = mustAdd(total, v)
	}
	return total
}

func (g *GCounter) Slots() map[string]int64 {
	out := make(map[string]int64, len(g.slots))
	for k, v := range g.slots {
		out[k] = v
	}
	return out
}

func (g *GCounter) Merge(other *GCounter) *GCounter {
	out := NewGCounterFrom(g.slots)
	for k, v := range other.slots {
		if cur, ok := out.slots[k]; !ok || v > cur { // a slot at 0 still exists, as TreeMap.merge would keep it
			out.slots[k] = v
		}
	}
	return out
}

func (g *GCounter) Equal(other *GCounter) bool {
	if len(g.slots) != len(other.slots) {
		return false
	}
	for k, v := range g.slots {
		if ov, ok := other.slots[k]; !ok || ov != v {
			return false
		}
	}
	return true
}

func (g *GCounter) String() string {
	return "GCounter" + fmtMap(g.slots) + "=" + strconv.FormatInt(g.Value(), 10)
}

// PNCounter is a positive-negative counter: two G-Counters. Value = increments - decrements.
type PNCounter struct {
	Positive *GCounter
	Negative *GCounter
}

func NewPNCounter() *PNCounter { return &PNCounter{NewGCounter(), NewGCounter()} }

func (c *PNCounter) Increment(replica string, by int64) error {
	return c.Positive.Increment(replica, by)
}
func (c *PNCounter) Decrement(replica string, by int64) error {
	return c.Negative.Increment(replica, by)
}

func (c *PNCounter) Value() int64 {
	v, err := subtractExact(c.Positive.Value(), c.Negative.Value())
	if err != nil {
		panic(err)
	}
	return v
}

func (c *PNCounter) Merge(other *PNCounter) *PNCounter {
	return &PNCounter{c.Positive.Merge(other.Positive), c.Negative.Merge(other.Negative)}
}

func (c *PNCounter) Equal(other *PNCounter) bool {
	return c.Positive.Equal(other.Positive) && c.Negative.Equal(other.Negative)
}

func (c *PNCounter) String() string {
	return fmt.Sprintf("PNCounter(+%d -%d)=%d", c.Positive.Value(), c.Negative.Value(), c.Value())
}

func sortedKeys[E cmp.Ordered, V any](m map[E]V) []E {
	keys := make([]E, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

func fmtSet[E cmp.Ordered](elements []E) string {
	parts := make([]string, len(elements))
	for i, e := range elements {
		parts[i] = fmt.Sprint(e)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// GSet is a grow-only set: add only; merge is set union.
type GSet[E cmp.Ordered] struct{ elements map[E]bool }

func NewGSet[E cmp.Ordered]() *GSet[E] { return &GSet[E]{elements: map[E]bool{}} }

func (s *GSet[E]) Add(e E)           { s.elements[e] = true }
func (s *GSet[E]) Contains(e E) bool { return s.elements[e] }

// Elements returns the members in sorted order, as Java's TreeSet iterates.
func (s *GSet[E]) Elements() []E { return sortedKeys(s.elements) }

func (s *GSet[E]) Merge(other *GSet[E]) *GSet[E] {
	out := NewGSet[E]()
	for e := range s.elements {
		out.elements[e] = true
	}
	for e := range other.elements {
		out.elements[e] = true
	}
	return out
}

func (s *GSet[E]) Equal(other *GSet[E]) bool {
	if len(s.elements) != len(other.elements) {
		return false
	}
	for e := range s.elements {
		if !other.elements[e] {
			return false
		}
	}
	return true
}

func (s *GSet[E]) String() string { return "GSet" + fmtSet(s.Elements()) }

// Tag is a globally unique tag minted by one replica: (replica, sequence). Comparable so sets stay deterministic.
type Tag struct {
	Replica string
	Seq     int64
}

func (t Tag) String() string { return fmt.Sprintf("%s#%d", t.Replica, t.Seq) }

// TagSource is a per-replica tag source. Deterministic, no clock, no randomness.
type TagSource struct {
	replica string
	next    int64
}

func NewTagSource(replica string) *TagSource { return &TagSource{replica: replica, next: 1} }

func (s *TagSource) Next() Tag {
	t := Tag{s.replica, s.next}
	s.next++
	return t
}

// ORSet is an Observed-Remove set (add-wins). Each add mints a unique tag; a remove tombstones only the tags the
// remover has observed. An element is present if it has at least one live (non-tombstoned) tag.
type ORSet[E cmp.Ordered] struct {
	added      map[E]map[Tag]bool
	tombstones map[Tag]bool
}

func NewORSet[E cmp.Ordered]() *ORSet[E] {
	return &ORSet[E]{added: map[E]map[Tag]bool{}, tombstones: map[Tag]bool{}}
}

func (s *ORSet[E]) Add(e E, tags *TagSource) Tag {
	t := tags.Next()
	if s.added[e] == nil {
		s.added[e] = map[Tag]bool{}
	}
	s.added[e][t] = true
	return t
}

// Remove tombstones every tag of e that THIS replica currently observes. Tags it has not seen are unaffected.
func (s *ORSet[E]) Remove(e E) {
	for t := range s.added[e] {
		s.tombstones[t] = true
	}
}

func (s *ORSet[E]) Contains(e E) bool {
	for t := range s.added[e] {
		if !s.tombstones[t] {
			return true
		}
	}
	return false
}

func (s *ORSet[E]) Elements() []E {
	out := []E{}
	for e := range s.added {
		if s.Contains(e) {
			out = append(out, e)
		}
	}
	slices.Sort(out)
	return out
}

func (s *ORSet[E]) TombstoneCount() int { return len(s.tombstones) }

func (s *ORSet[E]) Merge(other *ORSet[E]) *ORSet[E] {
	out := NewORSet[E]()
	for _, src := range []*ORSet[E]{s, other} {
		for e, tags := range src.added {
			if out.added[e] == nil {
				out.added[e] = map[Tag]bool{}
			}
			for t := range tags {
				out.added[e][t] = true
			}
		}
		for t := range src.tombstones {
			out.tombstones[t] = true
		}
	}
	return out
}

func (s *ORSet[E]) Equal(other *ORSet[E]) bool {
	if len(s.added) != len(other.added) || len(s.tombstones) != len(other.tombstones) {
		return false
	}
	for e, tags := range s.added {
		otags, ok := other.added[e]
		if !ok || len(tags) != len(otags) {
			return false
		}
		for t := range tags {
			if !otags[t] {
				return false
			}
		}
	}
	for t := range s.tombstones {
		if !other.tombstones[t] {
			return false
		}
	}
	return true
}

func (s *ORSet[E]) String() string {
	return fmt.Sprintf("ORSet%s tombstones=%d", fmtSet(s.Elements()), len(s.tombstones))
}

// LWWRegister is a last-writer-wins register. The "writer" is identified by (timestamp, replica); the replica id
// breaks timestamp ties so that merge is deterministic. The losing value is discarded: that is the data loss
// Lesson 09 examines.
type LWWRegister[V comparable] struct {
	Value     V
	Timestamp int64
	Replica   string
}

func (r LWWRegister[V]) Set(newValue V, newTimestamp int64, writer string) LWWRegister[V] {
	return LWWRegister[V]{newValue, newTimestamp, writer}
}

func (r LWWRegister[V]) Dominates(other LWWRegister[V]) bool {
	if r.Timestamp != other.Timestamp {
		return r.Timestamp > other.Timestamp
	}
	return r.Replica > other.Replica
}

func (r LWWRegister[V]) Merge(other LWWRegister[V]) LWWRegister[V] {
	if r.Dominates(other) {
		return r
	}
	return other
}

func (r LWWRegister[V]) Equal(other LWWRegister[V]) bool { return r == other }

// Currency is an ISO 4217 currency, copied from Phase 10 Lesson 01.
type Currency struct {
	Code     string
	Exponent int
}

var (
	USD = Currency{"USD", 2}
	EUR = Currency{"EUR", 2}
)

func NewCurrency(code string, exponent int) (Currency, error) {
	if len(code) != 3 || strings.ToUpper(code) != code || strings.Trim(code, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") != "" {
		return Currency{}, fmt.Errorf("currency code must be three uppercase letters: %q", code)
	}
	if exponent < 0 || exponent > 4 {
		return Currency{}, fmt.Errorf("exponent must be 0..4: %d", exponent)
	}
	return Currency{code, exponent}, nil
}

func (c Currency) MinorUnitsPerMajor() int64 {
	scale := int64(1)
	for i := 0; i < c.Exponent; i++ {
		scale *= 10
	}
	return scale
}

// ErrCurrencyMismatch is the Java CurrencyMismatchException.
var ErrCurrencyMismatch = errors.New("currency mismatch")

// ErrNegativeAmount: credits and debits take non-negative amounts.
var ErrNegativeAmount = errors.New("amount must be non-negative")

// ErrTooManyDecimals is Java's ArithmeticException from Money.parse.
var ErrTooManyDecimals = errors.New("too many decimals")

// Money is integer minor units + currency. Only the operations this lesson needs.
type Money struct {
	MinorUnits int64
	Currency   Currency
}

func OfMinor(minorUnits int64, currency Currency) Money { return Money{minorUnits, currency} }

func Parse(text string, currency Currency) (Money, error) {
	s := strings.TrimSpace(text)
	negative := strings.HasPrefix(s, "-")
	if negative || strings.HasPrefix(s, "+") {
		s = s[1:]
	}
	whole, frac := s, ""
	if dot := strings.IndexByte(s, '.'); dot >= 0 {
		whole, frac = s[:dot], s[dot+1:]
	}
	if whole == "" {
		whole = "0"
	}
	if len(frac) > currency.Exponent {
		return Money{}, fmt.Errorf("%w: %s for %s", ErrTooManyDecimals, text, currency.Code)
	}
	for len(frac) < currency.Exponent {
		frac += "0"
	}
	w, err := strconv.ParseInt(whole, 10, 64)
	if err != nil {
		return Money{}, err
	}
	var f int64
	if frac != "" {
		if f, err = strconv.ParseInt(frac, 10, 64); err != nil {
			return Money{}, err
		}
	}
	scaled := w * currency.MinorUnitsPerMajor()
	if w != 0 && scaled/w != currency.MinorUnitsPerMajor() {
		return Money{}, ErrOverflow
	}
	minor, err := addExact(scaled, f)
	if err != nil {
		return Money{}, err
	}
	if negative {
		minor = -minor
	}
	return Money{minor, currency}, nil
}

// MustParse is Parse for literal amounts in the demo and tests.
func MustParse(text string, currency Currency) Money {
	m, err := Parse(text, currency)
	if err != nil {
		panic(err)
	}
	return m
}

func (m Money) IsNegative() bool { return m.MinorUnits < 0 }

func (m Money) String() string {
	scale := m.Currency.MinorUnitsPerMajor()
	abs := m.MinorUnits
	if abs < 0 {
		abs = -abs
	}
	sign := ""
	if m.MinorUnits < 0 {
		sign = "-"
	}
	if m.Currency.Exponent == 0 {
		return fmt.Sprintf("%s%d %s", sign, abs, m.Currency.Code)
	}
	return fmt.Sprintf("%s%d.%0*d %s", sign, abs/scale, m.Currency.Exponent, abs%scale, m.Currency.Code)
}

// ReplicatedBalance is a balance replicated as a PN-Counter of minor units. Credits increment, debits decrement,
// merge is coordination-free. The type can tell you the balance went negative; it cannot prevent it.
type ReplicatedBalance struct {
	Currency Currency
	Counter  *PNCounter
}

func NewReplicatedBalance(currency Currency) *ReplicatedBalance {
	return &ReplicatedBalance{currency, NewPNCounter()}
}

func (b *ReplicatedBalance) requireCurrency(m Money) error {
	if m.Currency != b.Currency {
		return fmt.Errorf("%w: cannot combine %s with %s", ErrCurrencyMismatch, b.Currency.Code, m.Currency.Code)
	}
	if m.MinorUnits < 0 {
		return fmt.Errorf("%w: %s", ErrNegativeAmount, m)
	}
	return nil
}

func (b *ReplicatedBalance) Credit(replica string, amount Money) error {
	if err := b.requireCurrency(amount); err != nil {
		return err
	}
	return b.Counter.Increment(replica, amount.MinorUnits)
}

// Debit debits unconditionally. A local check against Balance() is NOT a global guarantee; see the lesson.
func (b *ReplicatedBalance) Debit(replica string, amount Money) error {
	if err := b.requireCurrency(amount); err != nil {
		return err
	}
	return b.Counter.Decrement(replica, amount.MinorUnits)
}

func (b *ReplicatedBalance) Balance() Money { return OfMinor(b.Counter.Value(), b.Currency) }

// Merge panics on a currency mismatch: merging USD into EUR is a programming error and Merge has no error slot.
func (b *ReplicatedBalance) Merge(other *ReplicatedBalance) *ReplicatedBalance {
	if other.Currency != b.Currency {
		panic(fmt.Sprintf("%v: cannot combine %s with %s", ErrCurrencyMismatch, b.Currency.Code, other.Currency.Code))
	}
	return &ReplicatedBalance{b.Currency, b.Counter.Merge(other.Counter)}
}

func (b *ReplicatedBalance) Equal(other *ReplicatedBalance) bool {
	return b.Currency == other.Currency && b.Counter.Equal(other.Counter)
}

func (b *ReplicatedBalance) String() string { return "Balance " + b.Balance().String() }

// JavaRandom is java.util.Random: a 48-bit LCG. Ported bit for bit so the seeded property checks draw the same states.
type JavaRandom struct{ seed int64 }

const (
	multiplier = 0x5DEECE66D
	addend     = 0xB
	mask48     = (int64(1) << 48) - 1
)

func NewJavaRandom(seed int64) *JavaRandom { return &JavaRandom{seed: (seed ^ multiplier) & mask48} }

func (r *JavaRandom) next(bits uint) int32 {
	r.seed = (r.seed*multiplier + addend) & mask48
	return int32(r.seed >> (48 - bits))
}

func (r *JavaRandom) NextInt(bound int) int {
	if bound <= 0 {
		panic("bound must be positive")
	}
	if bound&(-bound) == bound { // power of two
		return int((int64(bound) * int64(r.next(31))) >> 31)
	}
	for {
		bits := r.next(31)
		val := bits % int32(bound)
		if bits-val+int32(bound-1) >= 0 { // int32 overflow check, as in the JDK
			return int(val)
		}
	}
}

func (r *JavaRandom) NextBoolean() bool { return r.next(1) != 0 }

// Shuffle is Collections.shuffle(list, rnd): swap from the end with a nextInt(i) partner.
func Shuffle[T any](list []T, rng *JavaRandom) {
	for i := len(list); i > 1; i-- {
		j := rng.NextInt(i)
		list[i-1], list[j] = list[j], list[i-1]
	}
}

// Result is one property-check run.
type Result struct {
	Trials, CommutativeFailures, AssociativeFailures, IdempotentFailures int
}

func (r Result) AllHold() bool {
	return r.CommutativeFailures == 0 && r.AssociativeFailures == 0 && r.IdempotentFailures == 0
}

func okOr(n int) string {
	if n == 0 {
		return "ok"
	}
	return fmt.Sprintf("%d failures", n)
}

func (r Result) String() string {
	return fmt.Sprintf("%d trials: commutative %s, associative %s, idempotent %s", r.Trials,
		okOr(r.CommutativeFailures), okOr(r.AssociativeFailures), okOr(r.IdempotentFailures))
}

// Check runs property-style checks of the three merge laws on random replica states. The generator is a function of
// a seeded JavaRandom, so every failure is replayable.
func Check[T Crdt[T]](gen func() T, trials int) Result {
	comm, assoc, idem := 0, 0, 0
	for i := 0; i < trials; i++ {
		a, b, c := gen(), gen(), gen()
		if !a.Merge(b).Equal(b.Merge(a)) {
			comm++
		}
		if !a.Merge(b).Merge(c).Equal(a.Merge(b.Merge(c))) {
			assoc++
		}
		if !a.Merge(a).Equal(a) {
			idem++
		}
	}
	return Result{trials, comm, assoc, idem}
}

// Converges merges every state into every other in a random order and reports whether all replicas agree.
func Converges[T Crdt[T]](states []T, rng *JavaRandom) bool {
	result := append([]T(nil), states...)
	for i := range result {
		order := []int{}
		for j := range states {
			if j != i {
				order = append(order, j)
			}
		}
		Shuffle(order, rng)
		acc := result[i]
		for _, j := range order {
			acc = acc.Merge(states[j])
		}
		result[i] = acc
	}
	for _, t := range result {
		if !t.Equal(result[0]) {
			return false
		}
	}
	return true
}

// NaiveSumCounter is a deliberately broken "counter" whose merge adds instead of taking the max. Fails idempotence.
type NaiveSumCounter struct{ Total int64 }

func (n NaiveSumCounter) Merge(other NaiveSumCounter) NaiveSumCounter {
	return NaiveSumCounter{n.Total + other.Total}
}
func (n NaiveSumCounter) Equal(other NaiveSumCounter) bool { return n == other }

var replicas = []string{"A", "B", "C"}

func RandomGCounter(rng *JavaRandom) *GCounter {
	g := NewGCounter()
	for _, r := range replicas {
		if rng.NextBoolean() {
			_ = g.Increment(r, int64(rng.NextInt(50)))
		}
	}
	return g
}

func RandomPNCounter(rng *JavaRandom) *PNCounter {
	c := NewPNCounter()
	for _, r := range replicas {
		if rng.NextBoolean() {
			_ = c.Increment(r, int64(rng.NextInt(50)))
		}
		if rng.NextBoolean() {
			_ = c.Decrement(r, int64(rng.NextInt(50)))
		}
	}
	return c
}

func RandomORSet(rng *JavaRandom, sources map[string]*TagSource) *ORSet[string] {
	s := NewORSet[string]()
	items := []string{"apple", "pear", "fig"}
	for i := 0; i < 4; i++ {
		replica := replicas[rng.NextInt(3)]
		item := items[rng.NextInt(len(items))]
		if rng.NextInt(3) == 0 {
			s.Remove(item)
		} else {
			s.Add(item, sources[replica])
		}
	}
	return s
}

func main() {
	rng := NewJavaRandom(20261013)

	fmt.Println("== G-Counter: page views on three replicas, merged in any order ==")
	a, b, c := NewGCounter(), NewGCounter(), NewGCounter()
	_ = a.Increment("A", 3)
	_ = b.Increment("B", 5)
	_ = c.Increment("C", 2)
	_ = b.Increment("B", 1)
	ab := a.Merge(b)
	abc, cba := ab.Merge(c), c.Merge(b).Merge(a)
	fmt.Printf("A=%s  B=%s  C=%s\n", a, b, c)
	fmt.Printf("merge(A,B,C) = %s   merge(C,B,A) = %s   equal: %v\n", abc, cba, abc.Equal(cba))
	fmt.Printf("merge(A,B) twice = %s   (idempotent: a stale message cannot double count)\n", ab.Merge(b))

	fmt.Println()
	fmt.Println("== PN-Counter as a wallet balance: the merge is safe, the invariant is not ==")
	east := NewReplicatedBalance(USD)
	_ = east.Credit("east", MustParse("100.00", USD))
	west := east.Merge(NewReplicatedBalance(USD)) // west syncs the deposit
	fmt.Printf("both regions see %s\n", east.Balance())
	// the partition: each region checks locally, each sees 100.00, each approves an 80.00 debit
	debit := MustParse("80.00", USD)
	if !east.Balance().IsNegative() {
		_ = east.Debit("east", debit)
	}
	if !west.Balance().IsNegative() {
		_ = west.Debit("west", debit)
	}
	fmt.Printf("east after local debit: %s   west after local debit: %s\n", east.Balance(), west.Balance())
	merged := east.Merge(west)
	fmt.Printf("after the partition heals: %s   negative=%v  (both debits survive; nothing was lost, and nothing was prevented)\n",
		merged.Balance(), merged.Balance().IsNegative())

	fmt.Println()
	fmt.Println("== G-Set and OR-Set: removing something a peer re-added ==")
	tagsA, tagsB := NewTagSource("A"), NewTagSource("B")
	cartA := NewORSet[string]()
	cartA.Add("book", tagsA)
	cartB := cartA.Merge(NewORSet[string]()) // B syncs
	cartA.Remove("book")                     // A removes the tag it observed (A#1)
	cartB.Add("book", tagsB)                 // B concurrently adds again (B#1)
	cart := cartA.Merge(cartB)
	fmt.Printf("A removed book, B concurrently added book -> merged: %s  (add wins: B's tag was never observed by A's remove)\n", fmtSet(cart.Elements()))
	gs := NewGSet[string]()
	gs.Add("book")
	fmt.Printf("G-Set has no remove at all: %s\n", gs)

	fmt.Println()
	fmt.Println("== LWW-Register: deterministic, and lossy ==")
	r1 := LWWRegister[string]{"addr=Oak St", 10, "A"}
	r2 := LWWRegister[string]{"addr=Elm St", 10, "B"}
	fmt.Printf("A wrote '%s' at t=10, B wrote '%s' at t=10 -> merged keeps '%s' (tie broken by replica id; the other write is gone)\n",
		r1.Value, r2.Value, r1.Merge(r2).Value)

	fmt.Println()
	fmt.Println("== Merge laws, property-style (seed 20261013) ==")
	fmt.Printf("G-Counter  %s\n", Check(func() *GCounter { return RandomGCounter(rng) }, 500))
	fmt.Printf("PN-Counter %s\n", Check(func() *PNCounter { return RandomPNCounter(rng) }, 500))
	sources := map[string]*TagSource{"A": NewTagSource("A"), "B": NewTagSource("B"), "C": NewTagSource("C")}
	fmt.Printf("OR-Set     %s\n", Check(func() *ORSet[string] { return RandomORSet(rng, sources) }, 500))
	fmt.Printf("NaiveSum   %s  <- merge by addition is not a join\n", Check(func() NaiveSumCounter { return NaiveSumCounter{int64(rng.NextInt(100))} }, 500))
	reps := []*PNCounter{}
	for i := 0; i < 5; i++ {
		reps = append(reps, RandomPNCounter(rng))
	}
	fmt.Printf("5 PN-Counter replicas, all-to-all merge in shuffled orders, converge: %v\n", Converges(reps, rng))
}
