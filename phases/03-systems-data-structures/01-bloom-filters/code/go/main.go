// Lesson: phases/03-systems-data-structures/01-bloom-filters/docs/en.md
// Bloom Filters from Scratch
// Run:  go run .
// Test: go test ./...
// Sources: Bloom (1970), CACM 13(7); Kirsch & Mitzenmacher (2006), "Less Hashing, Same Performance";
//          FNV-1a (Fowler/Noll/Vo, IETF draft-eastlake-fnv); Swamidass & Baldi (2007) for the cardinality estimate.
// Go 1.22+, standard library only. Same design as the Java and Python versions.
package main

import (
	"errors"
	"fmt"
	"math"
	"math/bits"
)

const (
	fnvOffset uint64 = 0xcbf29ce484222325
	fnvPrime  uint64 = 0x100000001b3
)

// Fnv1a64 is FNV-1a, 64-bit: xor the byte in, then multiply by the prime. uint64 wraps, which is the intent.
func Fnv1a64(data []byte) uint64 {
	h := fnvOffset
	for _, b := range data {
		h ^= uint64(b)
		h *= fnvPrime
	}
	return h
}

// Mix64 is MurmurHash3's fmix64 finalizer: a bijection on 64-bit words that spreads every input bit
// to every output bit. Used to derive the second hash from the first (double hashing).
func Mix64(h uint64) uint64 {
	h ^= h >> 33
	h *= 0xff51afd7ed558ccd
	h ^= h >> 33
	h *= 0xc4ceb9fe1a85ec53
	h ^= h >> 33
	return h
}

// ErrParameterMismatch is returned by Union when the two filters were not built with the same m and k.
var ErrParameterMismatch = errors.New("cannot union filters with different parameters")

// BloomFilter is a fixed-size bit array with k hash functions. It answers "definitely not present"
// or "possibly present"; never "definitely present". No deletes.
type BloomFilter struct {
	words      []uint64
	M          uint64 // number of bits
	K          int    // number of hash functions
	Insertions uint64 // how many Add calls so far
}

// NewBloomFilter allocates a filter with the given number of bits and hash functions.
func NewBloomFilter(bitsN uint64, hashes int) (*BloomFilter, error) {
	if bitsN == 0 {
		return nil, fmt.Errorf("bits must be positive: %d", bitsN)
	}
	if hashes <= 0 {
		return nil, fmt.Errorf("hashes must be positive: %d", hashes)
	}
	return &BloomFilter{words: make([]uint64, (bitsN+63)/64), M: bitsN, K: hashes}, nil
}

// mustNew is for the demo and tests, where the parameters are known-good.
func mustNew(bitsN uint64, hashes int) *BloomFilter {
	bf, err := NewBloomFilter(bitsN, hashes)
	if err != nil {
		panic(err)
	}
	return bf
}

// ----- sizing -------------------------------------------------------------------------

// OptimalBits is m = -n ln p / (ln 2)^2, rounded up.
func OptimalBits(expectedItems uint64, falsePositiveRate float64) (uint64, error) {
	if expectedItems == 0 {
		return 0, errors.New("expectedItems must be positive")
	}
	if !(falsePositiveRate > 0 && falsePositiveRate < 1) {
		return 0, errors.New("falsePositiveRate must be in (0, 1)")
	}
	ln2 := math.Log(2)
	return uint64(math.Ceil(-float64(expectedItems) * math.Log(falsePositiveRate) / (ln2 * ln2))), nil
}

// OptimalHashes is k = (m / n) ln 2, rounded to the nearest integer, at least 1.
func OptimalHashes(bitsN, expectedItems uint64) int {
	k := int(math.Round(float64(bitsN) / float64(expectedItems) * math.Log(2)))
	if k < 1 {
		return 1
	}
	return k
}

// ExpectedFalsePositiveRate is p = (1 - e^(-kn/m))^k: the classic approximation, exact enough for sizing.
func ExpectedFalsePositiveRate(bitsN uint64, hashes int, items uint64) float64 {
	if items == 0 {
		return 0
	}
	return math.Pow(1-math.Exp(-float64(hashes)*float64(items)/float64(bitsN)), float64(hashes))
}

// Create sizes a filter from an expected item count and a target false-positive rate.
func Create(expectedItems uint64, falsePositiveRate float64) (*BloomFilter, error) {
	m, err := OptimalBits(expectedItems, falsePositiveRate)
	if err != nil {
		return nil, err
	}
	return NewBloomFilter(m, OptimalHashes(m, expectedItems))
}

// ----- hashing -----------------------------------------------------------------------

// Indexes returns the k bit positions for a key: index_i = (h1 + i * h2) mod m, Kirsch-Mitzenmacher
// double hashing. Two 64-bit hashes stand in for k independent ones.
func (bf *BloomFilter) Indexes(key []byte) []uint64 {
	h1 := Fnv1a64(key)
	h2 := Mix64(h1)
	out := make([]uint64, bf.K)
	for i := 0; i < bf.K; i++ {
		out[i] = (h1 + uint64(i)*h2) % bf.M
	}
	return out
}

// ----- membership ----------------------------------------------------------------------

// Add sets the k bits. It returns true if any bit changed (the key was definitely new), false if
// every bit was already set (the key is possibly a duplicate).
func (bf *BloomFilter) Add(key []byte) bool {
	changed := false
	for _, idx := range bf.Indexes(key) {
		word, mask := idx>>6, uint64(1)<<(idx&63)
		if bf.words[word]&mask == 0 {
			bf.words[word] |= mask
			changed = true
		}
	}
	bf.Insertions++
	return changed
}

// AddString is Add on the UTF-8 bytes of s.
func (bf *BloomFilter) AddString(s string) bool { return bf.Add([]byte(s)) }

// MightContain returns false if the key was definitely never added, true if it possibly was.
func (bf *BloomFilter) MightContain(key []byte) bool {
	for _, idx := range bf.Indexes(key) {
		if bf.words[idx>>6]&(uint64(1)<<(idx&63)) == 0 {
			return false
		}
	}
	return true
}

// MightContainString is MightContain on the UTF-8 bytes of s.
func (bf *BloomFilter) MightContainString(s string) bool { return bf.MightContain([]byte(s)) }

// ----- introspection -------------------------------------------------------------------

// BitCount is the number of set bits (the filter's fill).
func (bf *BloomFilter) BitCount() uint64 {
	var total uint64
	for _, w := range bf.words {
		total += uint64(bits.OnesCount64(w))
	}
	return total
}

// FillRatio is set bits over total bits.
func (bf *BloomFilter) FillRatio() float64 { return float64(bf.BitCount()) / float64(bf.M) }

// EstimatedFalsePositiveRate applies the formula to this filter's own insertion count.
func (bf *BloomFilter) EstimatedFalsePositiveRate() float64 {
	return ExpectedFalsePositiveRate(bf.M, bf.K, bf.Insertions)
}

// ApproximateCount is Swamidass & Baldi's n* = -(m / k) ln(1 - X / m) where X is the number of set
// bits. It lets a filter rebuilt from a snapshot report how full it is without a counter.
func (bf *BloomFilter) ApproximateCount() uint64 {
	x := bf.BitCount()
	if x >= bf.M {
		return bf.M // saturated; the estimate is unbounded
	}
	return uint64(math.Round(-(float64(bf.M) / float64(bf.K)) * math.Log(1-float64(x)/float64(bf.M))))
}

// Union ORs two filters built with identical m and k. The result answers for both key sets.
func (bf *BloomFilter) Union(other *BloomFilter) (*BloomFilter, error) {
	if bf.M != other.M || bf.K != other.K {
		return nil, fmt.Errorf("%w: (%d,%d) vs (%d,%d)", ErrParameterMismatch, bf.M, bf.K, other.M, other.K)
	}
	result := mustNew(bf.M, bf.K)
	for i := range bf.words {
		result.words[i] = bf.words[i] | other.words[i]
	}
	result.Insertions = bf.Insertions + other.Insertions
	return result, nil
}

// BitsPerItem is m / items.
func (bf *BloomFilter) BitsPerItem(items uint64) float64 { return float64(bf.M) / float64(items) }

func (bf *BloomFilter) String() string {
	return fmt.Sprintf("BloomFilter(m=%d, k=%d, insertions=%d, set_bits=%d)", bf.M, bf.K, bf.Insertions, bf.BitCount())
}

// MeasureFalsePositiveRate inserts `inserted` synthetic keys, then probes `probes` keys that were
// never inserted. Deterministic: keys are "k-<i>" and "q-<i>", so every language measures the same number.
func MeasureFalsePositiveRate(bf *BloomFilter, inserted, probes int) float64 {
	for i := 0; i < inserted; i++ {
		bf.AddString(fmt.Sprintf("k-%d", i))
	}
	positives := 0
	for i := 0; i < probes; i++ {
		if bf.MightContainString(fmt.Sprintf("q-%d", i)) {
			positives++
		}
	}
	return float64(positives) / float64(probes)
}

func main() {
	fmt.Println("== Sizing a filter for 10,000 idempotency keys at 1% false positives ==")
	var n uint64 = 10_000
	p := 0.01
	m, _ := OptimalBits(n, p)
	k := OptimalHashes(m, n)
	fmt.Printf("m = %d bits (%.1f KiB), k = %d hashes, %.2f bits per key\n", m, float64(m)/8/1024, k, float64(m)/float64(n))
	fmt.Printf("formula predicts p = %.5f\n", ExpectedFalsePositiveRate(m, k, n))

	fmt.Println("\n== Where a key lands ==")
	small := mustNew(1000, 3)
	key := []byte("idem-key-1")
	fmt.Printf("fnv1a64(\"idem-key-1\") = 0x%016x\n", Fnv1a64(key))
	fmt.Printf("mix64(h1)             = 0x%016x\n", Mix64(Fnv1a64(key)))
	fmt.Printf("indexes in m=1000, k=3: %v\n", small.Indexes(key))

	fmt.Println("\n== Definitely not seen vs possibly seen ==")
	idem, _ := Create(n, p)
	for _, req := range []string{"pay_7f3a", "pay_7f3b", "pay_7f3c"} {
		fmt.Printf("add(%q) -> changed bits: %v\n", req, idem.AddString(req))
	}
	for _, req := range []string{"pay_7f3a", "pay_9999"} {
		maybe := idem.MightContainString(req)
		verdict := "definitely new -> skip the lookup"
		if maybe {
			verdict = "possibly seen -> check the database"
		}
		fmt.Printf("mightContain(%q) = %v: %s\n", req, maybe, verdict)
	}
	fmt.Printf("re-adding %q changes bits: %v\n", "pay_7f3a", idem.AddString("pay_7f3a"))

	fmt.Println("\n== Measured false-positive rate against the formula ==")
	probe, _ := Create(n, p)
	measured := MeasureFalsePositiveRate(probe, int(n), 50_000)
	fmt.Println(probe)
	fmt.Printf("fill ratio = %.4f (theory: 1 - e^(-kn/m) = %.4f)\n", probe.FillRatio(), 1-math.Exp(-float64(k)*float64(n)/float64(m)))
	fmt.Printf("measured p = %.5f, formula p = %.5f\n", measured, probe.EstimatedFalsePositiveRate())
	fmt.Printf("approximateCount = %d (actual %d)\n", probe.ApproximateCount(), n)

	fmt.Println("\n== What happens when you overfill it ==")
	for _, factor := range []int{1, 2, 5, 10} {
		over := mustNew(m, k)
		for i := 0; i < int(n)*factor; i++ {
			over.AddString(fmt.Sprintf("k-%d", i))
		}
		fmt.Printf("%2dx capacity: fill = %.3f, predicted p = %.4f\n", factor, over.FillRatio(), over.EstimatedFalsePositiveRate())
	}

	fmt.Println("\n== Union of two shards' filters ==")
	a, b := mustNew(4096, 4), mustNew(4096, 4)
	a.AddString("shard-a-key")
	b.AddString("shard-b-key")
	both, _ := a.Union(b)
	fmt.Printf("union mightContain shard-a-key: %v, shard-b-key: %v\n", both.MightContainString("shard-a-key"), both.MightContainString("shard-b-key"))
	if _, err := a.Union(mustNew(2048, 4)); err != nil {
		fmt.Println("rejected:", err)
	}
}
