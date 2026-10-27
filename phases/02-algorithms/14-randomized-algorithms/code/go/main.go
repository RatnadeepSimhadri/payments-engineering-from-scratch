// Lesson: phases/02-algorithms/14-randomized-algorithms/docs/en.md
// Randomized Algorithms and Reservoir Sampling
// Run:  go run .
// Test: go test ./...
// Sources: Vitter, "Random sampling with a reservoir" (ACM TOMS 1985), Algorithm R; Knuth, TAOCP vol. 2, 3.4.2
// (Fisher-Yates / Algorithm P); Hoare, "Algorithm 65: Find" (CACM 1961); Steele, Lea, Flood, "Fast splittable
// pseudorandom number generators" (OOPSLA 2014) for SplitMix64; Rabin, "Probabilistic algorithm for testing
// primality" (J. Number Theory 1980). Go 1.22+, standard library only. Same design as the Java and Python versions.
package main

import (
	"errors"
	"fmt"
	"math"
	"math/bits"
	"slices"
	"strings"
)

// Sentinel errors: Go reports what Java throws as IllegalArgumentException.
var (
	ErrBadBound  = errors.New("bound must be positive")
	ErrBadK      = errors.New("k must be positive")
	ErrBadWeight = errors.New("weight must be positive")
	ErrEmpty     = errors.New("empty array")
	ErrKRange    = errors.New("k out of range")
	ErrBadQ      = errors.New("q must be in (0, 1]")
	ErrTooLarge  = errors.New("n must be below 2^31")
)

// Currency is an ISO 4217 currency, copied from Phase 10, Lesson 01.
type Currency struct {
	Code     string
	Exponent int
}

// USD is the demo's currency.
var USD = Currency{"USD", 2}

// Money is an exact amount in minor units. Subset of Phase 10, Lesson 01.
type Money struct {
	MinorUnits int64
	Currency   Currency
}

// OfMinor builds a Money from minor units.
func OfMinor(minorUnits int64, currency Currency) Money { return Money{minorUnits, currency} }

func (m Money) String() string {
	scale := int64(1)
	for i := 0; i < m.Currency.Exponent; i++ {
		scale *= 10
	}
	frac := m.MinorUnits % scale // Go's / and % truncate toward zero exactly as Java's do
	if frac < 0 {
		frac = -frac
	}
	return fmt.Sprintf("%d.%0*d %s", m.MinorUnits/scale, m.Currency.Exponent, frac, m.Currency.Code)
}

// Rng is SplitMix64: a small, fast, well-tested generator with a 64-bit state. Written out so the sequence is
// identical in every language the course ships (java.util.Random, Python's random, and Go's math/rand all
// use different algorithms, so seeding them identically does not give identical streams). Not for
// cryptography: given one output the whole future is predictable. For keys and tokens use crypto/rand.
type Rng struct {
	state uint64
}

// NewRng seeds the generator.
func NewRng(seed int64) *Rng { return &Rng{uint64(seed)} }

// NextLong is the next 64-bit output, as a signed value like Java's long.
func (r *Rng) NextLong() int64 {
	r.state += 0x9E3779B97F4A7C15
	z := r.state
	z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9 // uint64 wraps and >> is logical: Java's >>>
	z = (z ^ (z >> 27)) * 0x94D049BB133111EB
	return int64(z ^ (z >> 31))
}

// NextInt is a uniform integer in [0, bound). Rejection sampling, not `NextLong() % bound`: the modulo of a 64-bit
// value is biased toward small residues whenever bound does not divide 2^64, and that bias is exactly
// what a uniformity test on a shuffle or a sample will detect.
func (r *Rng) NextInt(bound int) (int, error) {
	if bound <= 0 {
		return 0, fmt.Errorf("%w: %d", ErrBadBound, bound)
	}
	mask := int64(highestOneBit(uint64(bound))*2 - 1) // smallest 2^k - 1 >= bound - 1
	for {
		candidate := r.NextLong() & mask
		if candidate < int64(bound) {
			return int(candidate), nil
		}
	}
}

// nextInt is NextInt for a bound the caller already validated.
func (r *Rng) nextInt(bound int) int {
	v, err := r.NextInt(bound)
	if err != nil {
		panic(err)
	}
	return v
}

// NextDouble is a uniform double in [0, 1) with 53 bits of randomness.
func (r *Rng) NextDouble() float64 { return float64(uint64(r.NextLong())>>11) * (1.0 / (1 << 53)) }

// highestOneBit is Long.highestOneBit for a positive value.
func highestOneBit(v uint64) uint64 { return uint64(1) << (63 - bits.LeadingZeros64(v)) }

// ReservoirSampler is Vitter's Algorithm R: a uniform sample of k items from a stream of unknown length,
// in O(k) memory and one pass. Invariant after n items: every item seen so far is in the reservoir with
// probability exactly min(1, k / n).
type ReservoirSampler[T any] struct {
	k         int
	rng       *Rng
	reservoir []T
	seen      int64
}

// NewReservoirSampler keeps k items.
func NewReservoirSampler[T any](k int, rng *Rng) (*ReservoirSampler[T], error) {
	if k <= 0 {
		return nil, fmt.Errorf("%w: %d", ErrBadK, k)
	}
	return &ReservoirSampler[T]{k: k, rng: rng, reservoir: []T{}}, nil
}

// Offer feeds one stream item.
func (s *ReservoirSampler[T]) Offer(item T) {
	s.seen++
	if len(s.reservoir) < s.k {
		s.reservoir = append(s.reservoir, item)
		return
	}
	// Item number `seen` is kept with probability k / seen: pick a slot in [0, seen); keep if it is < k.
	slot := s.nextLong(s.seen)
	if slot < int64(s.k) {
		s.reservoir[slot] = item
	}
}

func (s *ReservoirSampler[T]) nextLong(bound int64) int64 {
	if bound <= math.MaxInt32 {
		return int64(s.rng.nextInt(int(bound)))
	}
	mask := int64(highestOneBit(uint64(bound))*2 - 1)
	for {
		c := s.rng.NextLong() & mask
		if c >= 0 && c < bound {
			return c
		}
	}
}

// Sample is the current reservoir (a copy).
func (s *ReservoirSampler[T]) Sample() []T { return slices.Clone(s.reservoir) }

// Seen is the number of items offered so far.
func (s *ReservoirSampler[T]) Seen() int64 { return s.seen }

// Capacity is k.
func (s *ReservoirSampler[T]) Capacity() int { return s.k }

// WeightedReservoirSampler is weighted reservoir sampling (Efraimidis and Spirakis, A-Res): each item gets key
// u^(1/w) for u uniform in (0,1); keep the k largest keys. An item's chance of selection is proportional to its
// weight. Used to audit transactions with probability proportional to amount, so large payments are rarely missed.
type WeightedReservoirSampler[T any] struct {
	k    int
	rng  *Rng
	heap *javaPriorityQueue[weightedEntry[T]]
}

type weightedEntry[T any] struct {
	key  float64
	item T
}

// NewWeightedReservoirSampler keeps k items.
func NewWeightedReservoirSampler[T any](k int, rng *Rng) (*WeightedReservoirSampler[T], error) {
	if k <= 0 {
		return nil, fmt.Errorf("%w: %d", ErrBadK, k)
	}
	return &WeightedReservoirSampler[T]{k: k, rng: rng, heap: newJavaPriorityQueue(func(a, b weightedEntry[T]) int {
		return compareFloat64(a.key, b.key)
	})}, nil
}

// compareFloat64 is Double.compare for the finite values the keys take.
func compareFloat64(a, b float64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

// Offer feeds one item with its weight.
func (w *WeightedReservoirSampler[T]) Offer(item T, weight int64) error {
	if weight <= 0 {
		return fmt.Errorf("%w: %d", ErrBadWeight, weight)
	}
	u := w.rng.NextDouble()
	if u == 0.0 {
		u = 0x1p-1022 // Double.MIN_NORMAL
	}
	key := math.Pow(u, 1.0/float64(weight))
	if w.heap.Len() < w.k {
		w.heap.Add(weightedEntry[T]{key, item})
	} else if key > w.heap.Peek().key {
		w.heap.Poll()
		w.heap.Add(weightedEntry[T]{key, item})
	}
	return nil
}

// Sample is the kept items in the heap's array order, as iterating a PriorityQueue yields them.
func (w *WeightedReservoirSampler[T]) Sample() []T {
	items := w.heap.Items()
	out := make([]T, len(items))
	for i, e := range items {
		out[i] = e.item
	}
	return out
}

// --- Shuffles: the correct one and the common wrong one, kept side by side so a test can tell them apart. ---

// FisherYates (Knuth's Algorithm P): for i from n-1 down to 1, swap a[i] with a[j], j uniform in [0, i]. Every
// permutation equally likely.
func FisherYates[T any](a []T, rng *Rng) {
	for i := len(a) - 1; i > 0; i-- {
		j := rng.nextInt(i + 1)
		a[i], a[j] = a[j], a[i]
	}
}

// NaiveShuffle is the tempting mistake: swap every position with a uniformly random position in the WHOLE slice.
// n^n outcomes cannot map evenly onto n! permutations.
func NaiveShuffle[T any](a []T, rng *Rng) {
	for i := range a {
		j := rng.nextInt(len(a))
		a[i], a[j] = a[j], a[i]
	}
}

// Permutations of [a, b, c] in lexicographic order.
var Permutations = []string{"abc", "acb", "bac", "bca", "cab", "cba"}

// PermutationCounts shuffles [a, b, c] `trials` times and counts how often each of the 6 permutations appears.
func PermutationCounts(trials int, rng *Rng, fisher bool) [6]int {
	var counts [6]int
	for t := 0; t < trials; t++ {
		a := []byte{'a', 'b', 'c'}
		if fisher {
			FisherYates(a, rng)
		} else {
			NaiveShuffle(a, rng)
		}
		counts[slices.Index(Permutations, string(a))]++
	}
	return counts
}

// --- Randomized quickselect (Hoare's Find with a random pivot): the k-th smallest element in expected O(n), ---
// --- worst case O(n^2) with vanishing probability. A Las Vegas algorithm: the answer is always right, only the ---
// --- running time is random. Reorders the slice it is given; callers pass a copy if they care.               ---

// Select returns the k-th smallest (0-based) element of a.
func Select(a []int64, k int, rng *Rng) (int64, error) {
	if len(a) == 0 {
		return 0, ErrEmpty
	}
	if k < 0 || k >= len(a) {
		return 0, fmt.Errorf("%w: %d", ErrKRange, k)
	}
	lo, hi := 0, len(a)-1
	for lo < hi {
		pivotIndex := lo + rng.nextInt(hi-lo+1)
		p := Partition(a, lo, hi, pivotIndex)
		if k == p {
			return a[k], nil
		}
		if k < p {
			hi = p - 1
		} else {
			lo = p + 1
		}
	}
	return a[lo], nil
}

// Partition is the Lomuto partition around a[pivotIndex]; returns the pivot's final position. Everything left is <=,
// right is >.
func Partition(a []int64, lo, hi, pivotIndex int) int {
	pivot := a[pivotIndex]
	a[pivotIndex], a[hi] = a[hi], a[pivotIndex]
	store := lo
	for i := lo; i < hi; i++ {
		if a[i] < pivot {
			a[i], a[store] = a[store], a[i]
			store++
		}
	}
	a[store], a[hi] = a[hi], a[store]
	return store
}

// Median is the lower median: element at index (n-1)/2 of the sorted order.
func Median(a []int64, rng *Rng) (int64, error) { return Select(a, (len(a)-1)/2, rng) }

// Percentile is the value at or above which (1 - q) of the data lies, nearest-rank: index ceil(q * n) - 1.
func Percentile(a []int64, q float64, rng *Rng) (int64, error) {
	if q <= 0 || q > 1 {
		return 0, fmt.Errorf("%w: %g", ErrBadQ, q)
	}
	idx := int(math.Ceil(q*float64(len(a)))) - 1
	return Select(a, max(0, idx), rng)
}

// --- Miller-Rabin primality test: a Monte Carlo algorithm. "Composite" is always right; "probably prime" is ---
// --- wrong with probability at most 4^-rounds. Restricted to n < 2^31 so that a*b fits in an int64 without a ---
// --- mulmod routine; math/big's ProbablyPrime is the production version.                                    ---

// IsProbablePrime runs `rounds` random witness tests.
func IsProbablePrime(n int64, rounds int, rng *Rng) (bool, error) {
	if n >= 1<<31 {
		return false, fmt.Errorf("%w: %d", ErrTooLarge, n)
	}
	if n < 2 {
		return false, nil
	}
	if n < 4 {
		return true, nil
	}
	if n%2 == 0 {
		return false, nil
	}
	d := n - 1
	s := 0
	for d%2 == 0 { // n - 1 = 2^s * d with d odd
		d /= 2
		s++
	}
	for r := 0; r < rounds; r++ {
		a := 2 + int64(rng.nextInt(int(n-3))) // witness candidate in [2, n-2]
		if IsWitness(a, d, s, n) {            // a proves n composite
			return false, nil
		}
	}
	return true, nil // no witness found in `rounds` tries
}

// IsWitness is true if a is a witness to n's compositeness: a^d != 1 and a^(2^i d) != -1 for all i < s.
func IsWitness(a, d int64, s int, n int64) bool {
	x := PowMod(a, d, n)
	if x == 1 || x == n-1 {
		return false
	}
	for i := 1; i < s; i++ {
		x = (x * x) % n
		if x == n-1 {
			return false
		}
	}
	return true
}

// PowMod is base^exp mod mod by repeated squaring.
func PowMod(base, exp, mod int64) int64 {
	result := int64(1)
	base %= mod
	for exp > 0 {
		if exp&1 == 1 {
			result = (result * base) % mod
		}
		base = (base * base) % mod
		exp >>= 1
	}
	return result
}

// IsPrimeByTrialDivision is the deterministic reference for small n, used by the tests as the oracle.
func IsPrimeByTrialDivision(n int64) bool {
	if n < 2 {
		return false
	}
	for f := int64(2); f*f <= n; f++ {
		if n%f == 0 {
			return false
		}
	}
	return true
}

// Transaction is one payment: an id and an amount.
type Transaction struct {
	ID     string
	Amount Money
}

// DayOfTransactions generates `count` payments: most small, a few large: 90% in [1, 200], 10% in [200, 5000] dollars.
func DayOfTransactions(rng *Rng, count int) []Transaction {
	out := make([]Transaction, 0, count)
	for i := 0; i < count; i++ {
		var dollars int64
		if rng.nextInt(10) == 0 {
			dollars = 200 + int64(rng.nextInt(4801))
		} else {
			dollars = 1 + int64(rng.nextInt(200))
		}
		cents := int64(rng.nextInt(100))
		out = append(out, Transaction{fmt.Sprintf("txn-%05d", i), OfMinor(dollars*100+cents, USD)})
	}
	return out
}

func ids(ts []Transaction) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.ID
	}
	return out
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func main() {
	rng := NewRng(2026)
	day := DayOfTransactions(rng, 10_000)

	fmt.Println("== Reservoir sampling: 5 of 10,000 transactions for audit, one pass, O(k) memory ==")
	uniform := must(NewReservoirSampler[Transaction](5, NewRng(1)))
	for _, t := range day {
		uniform.Offer(t)
	}
	fmt.Printf("seen %d, sample: [%s]\n", uniform.Seen(), strings.Join(ids(uniform.Sample()), ", "))

	fmt.Println()
	fmt.Println("== Weighted reservoir: probability proportional to amount ==")
	weighted := must(NewWeightedReservoirSampler[Transaction](5, NewRng(1)))
	for _, t := range day {
		if err := weighted.Offer(t, t.Amount.MinorUnits); err != nil {
			panic(err)
		}
	}
	for _, t := range weighted.Sample() {
		fmt.Printf("  %s  %s\n", t.ID, t.Amount)
	}

	fmt.Println()
	fmt.Println("== Fisher-Yates vs the naive shuffle: 60,000 shuffles of [a, b, c] ==")
	fmt.Printf("%-6s %10s %10s\n", "perm", "fisher", "naive")
	fy, nv := PermutationCounts(60_000, NewRng(7), true), PermutationCounts(60_000, NewRng(7), false)
	for i, perm := range Permutations {
		fmt.Printf("%-6s %10d %10d\n", perm, fy[i], nv[i])
	}
	fmt.Println("expected 10,000 each; naive gives acb, bac, bca 5/27 each (11,111) and the others 4/27 (8,889): 27 outcomes cannot split evenly over 6")

	fmt.Println()
	fmt.Println("== Quickselect: median and p99 amount without sorting 10,000 values ==")
	amounts := make([]int64, len(day))
	for i, t := range day {
		amounts[i] = t.Amount.MinorUnits
	}
	median := must(Median(slices.Clone(amounts), NewRng(3)))
	p99 := must(Percentile(slices.Clone(amounts), 0.99, NewRng(3)))
	sorted := slices.Clone(amounts)
	slices.Sort(sorted)
	fmt.Printf("median %s (sorted says %s)\n", OfMinor(median, USD), OfMinor(sorted[(len(sorted)-1)/2], USD))
	fmt.Printf("p99    %s (sorted says %s)\n", OfMinor(p99, USD), OfMinor(sorted[int(math.Ceil(0.99*float64(len(sorted))))-1], USD))

	fmt.Println()
	fmt.Println("== Miller-Rabin: Monte Carlo primality, 561 is a Carmichael number that fools Fermat ==")
	for _, n := range []int64{561, 1_000_003, 2_147_483_647, 1_000_001, 7919 * 7927} {
		fmt.Printf("%12d  probable prime after 10 rounds: %-5t  trial division: %t\n", n,
			must(IsProbablePrime(n, 10, NewRng(5))), IsPrimeByTrialDivision(n))
	}
}
