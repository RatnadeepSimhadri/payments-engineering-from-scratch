// Lesson: phases/03-systems-data-structures/02-count-min-sketch-and-hyperloglog/docs/en.md
// Count-Min Sketch and HyperLogLog: frequency and cardinality in constant memory
// Run:  go run .
// Test: go test ./...
// Sources: Cormode & Muthukrishnan (2005), "An Improved Data Stream Summary: The Count-Min Sketch";
// Flajolet, Fusy, Gandouet, Meunier (2007), "HyperLogLog"; Heule, Nunkesser, Hall (2013), "HyperLogLog in Practice".
// Go 1.22+, standard library only. Same design as the Java and Python versions.
package main

import (
	"errors"
	"fmt"
	"math"
	"math/bits"
	"strconv"
)

// ErrInvalidArgument is returned where the Java throws IllegalArgumentException.
var ErrInvalidArgument = errors.New("invalid argument")

// ---------------------------------------------------------------- Hashing ----

// The two 64-bit hash functions both sketches are built on. Pure functions over bytes.
// Results are uint64 so the multiplies wrap exactly like a Java long read as unsigned.
const (
	fnvOffset uint64 = 0xcbf29ce484222325
	fnvPrime  uint64 = 0x100000001b3
)

// Fnv1a64 is FNV-1a over the UTF-8 bytes of the key. Fast, simple, and identical in every language.
func Fnv1a64(key string) uint64 {
	h := fnvOffset
	for _, b := range []byte(key) {
		h ^= uint64(b)
		h *= fnvPrime
	}
	return h
}

// Mix64 is MurmurHash3's fmix64 finalizer: a bijection that spreads every input bit to every output bit.
func Mix64(h uint64) uint64 {
	h ^= h >> 33
	h *= 0xff51afd7ed558ccd
	h ^= h >> 33
	h *= 0xc4ceb9fe1a85ec53
	h ^= h >> 33
	return h
}

// Hash64 is a well-mixed 64-bit hash: FNV-1a alone has weak low bits for short keys, so finalize it.
func Hash64(key string) uint64 { return Mix64(Fnv1a64(key)) }

// ---------------------------------------------------------------- CountMinSketch ----

// CountMinSketch: depth rows of width counters. Every key maps to one counter per row; the
// estimate is the minimum over its counters. Never underestimates; overestimates by at most
// epsilon * N with probability at least 1 - delta, where N is the total count added.
type CountMinSketch struct {
	Width int // counters per row; controls epsilon = e / width
	Depth int // rows; controls delta = e^(-depth)
	table [][]int64
	total int64
}

func NewCountMinSketch(width, depth int) (*CountMinSketch, error) {
	if width <= 0 {
		return nil, fmt.Errorf("%w: width must be positive: %d", ErrInvalidArgument, width)
	}
	if depth <= 0 {
		return nil, fmt.Errorf("%w: depth must be positive: %d", ErrInvalidArgument, depth)
	}
	table := make([][]int64, depth)
	for r := range table {
		table[r] = make([]int64, width)
	}
	return &CountMinSketch{Width: width, Depth: depth, table: table}, nil
}

// ForErrorBounds sizes a sketch so that estimate <= true + epsilon * N with probability >= 1 - delta.
func ForErrorBounds(epsilon, delta float64) (*CountMinSketch, error) {
	if !(epsilon > 0 && epsilon < 1) {
		return nil, fmt.Errorf("%w: epsilon must be in (0,1): %g", ErrInvalidArgument, epsilon)
	}
	if !(delta > 0 && delta < 1) {
		return nil, fmt.Errorf("%w: delta must be in (0,1): %g", ErrInvalidArgument, delta)
	}
	width := int(math.Ceil(math.E / epsilon))
	depth := int(math.Ceil(math.Log(1.0 / delta)))
	return NewCountMinSketch(width, depth)
}

// Column for key in row i, by double hashing: h1 + i * h2 (Kirsch and Mitzenmacher).
// The sum wraps in uint64 exactly as the Java long does, and the unsigned remainder matches Long.remainderUnsigned.
func (c *CountMinSketch) Column(h1, h2 uint64, row int) int {
	combined := h1 + uint64(row)*h2
	return int(combined % uint64(c.Width))
}

func (c *CountMinSketch) AddCount(key string, count int64) error {
	if count < 0 {
		return fmt.Errorf("%w: count must be non-negative: %d", ErrInvalidArgument, count)
	}
	h1 := Hash64(key)
	h2 := Mix64(h1)
	for row := 0; row < c.Depth; row++ {
		c.table[row][c.Column(h1, h2, row)] += count
	}
	c.total += count
	return nil
}

func (c *CountMinSketch) Add(key string) { _ = c.AddCount(key, 1) }

// Estimate is the minimum of the key's counters. Each counter is true count plus collisions, so min is the tightest.
func (c *CountMinSketch) Estimate(key string) int64 {
	h1 := Hash64(key)
	h2 := Mix64(h1)
	best := int64(math.MaxInt64)
	for row := 0; row < c.Depth; row++ {
		if v := c.table[row][c.Column(h1, h2, row)]; v < best {
			best = v
		}
	}
	return best
}

func (c *CountMinSketch) Total() int64 { return c.total }

// ErrorBound is the bound on overestimate per the paper: epsilon * N, with epsilon = e / width.
func (c *CountMinSketch) ErrorBound() float64 { return math.E / float64(c.Width) * float64(c.total) }

// FailureProbability per the paper: e^(-depth).
func (c *CountMinSketch) FailureProbability() float64 { return math.Exp(-float64(c.Depth)) }

// Merge is the counter-wise sum. Valid only for sketches with identical width and depth (same hash layout).
func (c *CountMinSketch) Merge(other *CountMinSketch) (*CountMinSketch, error) {
	if other.Width != c.Width || other.Depth != c.Depth {
		return nil, fmt.Errorf("%w: cannot merge sketches of different shape", ErrInvalidArgument)
	}
	out, _ := NewCountMinSketch(c.Width, c.Depth)
	for r := 0; r < c.Depth; r++ {
		for col := 0; col < c.Width; col++ {
			out.table[r][col] = c.table[r][col] + other.table[r][col]
		}
	}
	out.total = c.total + other.total
	return out, nil
}

func (c *CountMinSketch) Bytes() int64 { return int64(c.Width) * int64(c.Depth) * 8 }

// ---------------------------------------------------------------- HyperLogLog ----

// HyperLogLog: m = 2^precision registers, each holding the longest run of leading zeros seen in
// the hash bits that follow the register index. Cardinality is a bias-corrected harmonic mean.
// Standard error is about 1.04 / sqrt(m).
type HyperLogLog struct {
	Precision int // p in the paper; 4..16
	Registers int // m = 2^p
	register  []byte
}

func NewHyperLogLog(precision int) (*HyperLogLog, error) {
	if precision < 4 || precision > 16 {
		return nil, fmt.Errorf("%w: precision must be 4..16: %d", ErrInvalidArgument, precision)
	}
	m := 1 << precision
	return &HyperLogLog{Precision: precision, Registers: m, register: make([]byte, m)}, nil
}

// Rho is the position of the first 1-bit in the low 64-p bits, counted from 1. Zero bits give the maximum 65-p.
func Rho(hash uint64, precision int) int {
	rest := hash << precision // discard the index bits; the rest now leads
	if rest == 0 {
		return 64 - precision + 1 // all remaining bits were zero
	}
	return bits.LeadingZeros64(rest) + 1
}

func (h *HyperLogLog) Add(key string) {
	hash := Hash64(key)
	index := int(hash >> (64 - h.Precision)) // top p bits pick the register
	r := Rho(hash, h.Precision)
	if r > int(h.register[index]) {
		h.register[index] = byte(r)
	}
}

// Alpha is alpha_m from the paper: the constant that corrects the harmonic mean's multiplicative bias.
func Alpha(m int) float64 {
	switch m {
	case 16:
		return 0.673
	case 32:
		return 0.697
	case 64:
		return 0.709
	default:
		return 0.7213 / (1.0 + 1.079/float64(m))
	}
}

// RawEstimate: alpha_m * m^2 / sum(2^-M[j]). The harmonic mean tames outlier registers.
func (h *HyperLogLog) RawEstimate() float64 {
	sum := 0.0
	for _, r := range h.register {
		sum += math.Pow(2.0, -float64(r))
	}
	m := float64(h.Registers)
	return Alpha(h.Registers) * m * m / sum
}

func (h *HyperLogLog) ZeroRegisters() int {
	zeros := 0
	for _, r := range h.register {
		if r == 0 {
			zeros++
		}
	}
	return zeros
}

// Cardinality is the bias-corrected count. For small sets the raw estimate is biased high, so when
// E <= 2.5m and some registers are still empty, fall back to linear counting over the
// number of empty registers: m * ln(m / V). With a 64-bit hash no large-range correction is needed.
func (h *HyperLogLog) Cardinality() int64 {
	estimate := h.RawEstimate()
	if estimate <= 2.5*float64(h.Registers) {
		zeros := h.ZeroRegisters()
		if zeros > 0 {
			estimate = float64(h.Registers) * math.Log(float64(h.Registers)/float64(zeros))
		}
	}
	return javaRound(estimate)
}

// javaRound is Math.round(double): floor(x + 0.5), not Go's half-away-from-zero math.Round.
func javaRound(x float64) int64 { return int64(math.Floor(x + 0.5)) }

// StandardError is the relative standard error from the paper: 1.04 / sqrt(m).
func (h *HyperLogLog) StandardError() float64 { return 1.04 / math.Sqrt(float64(h.Registers)) }

// Merge is the register-wise maximum. The merged sketch is exactly the sketch of the union.
func (h *HyperLogLog) Merge(other *HyperLogLog) (*HyperLogLog, error) {
	if other.Precision != h.Precision {
		return nil, fmt.Errorf("%w: cannot merge different precisions", ErrInvalidArgument)
	}
	out, _ := NewHyperLogLog(h.Precision)
	for i := 0; i < h.Registers; i++ {
		out.register[i] = max(h.register[i], other.register[i])
	}
	return out, nil
}

func (h *HyperLogLog) RegisterAt(i int) byte { return h.register[i] }

func (h *HyperLogLog) Bytes() int64 { return int64(h.Registers) }

// ---------------------------------------------------------------- Demo ----

// cardToken: a tokenized PAN stands in for the card: 16 digits, never a real number.
func cardToken(rng *JavaRandom) string {
	v := rng.NextLong()
	if v < 0 {
		v = -v // Math.abs: wraps for MinInt64 exactly as Java does
	}
	return "tok_" + fmt.Sprintf("%016d", v%10_000_000_000_000_000)
}

// commas formats like Java's %,d.
func commas(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := ""
	if s[0] == '-' {
		neg, s = "-", s[1:]
	}
	out := ""
	for i, ch := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			out += ","
		}
		out += string(ch)
	}
	return neg + out
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func main() {
	rng := NewJavaRandom(42)

	fmt.Println("== Count-Min Sketch: authorizations per merchant, 11 KB instead of a hash map ==")
	cms := must(ForErrorBounds(0.01, 0.01))
	fmt.Printf("width=%d depth=%d bytes=%d (epsilon=0.01, delta=0.01)\n", cms.Width, cms.Depth, cms.Bytes())

	exact := map[string]int64{}
	merchants := make([]string, 1_000)
	for i := range merchants {
		merchants[i] = "merchant_" + strconv.Itoa(i)
	}
	for i := 0; i < 200_000; i++ {
		// skewed traffic: a few merchants dominate, which is where the min-of-rows matters
		m := int(math.Min(float64(len(merchants)-1), math.Floor(math.Abs(rng.NextGaussian())*120)))
		cms.Add(merchants[m])
		exact[merchants[m]]++
	}
	fmt.Printf("total events = %d, error bound = %.0f\n", cms.Total(), cms.ErrorBound())
	for _, m := range []string{"merchant_0", "merchant_5", "merchant_50", "merchant_300", "merchant_999"} {
		truth := exact[m]
		fmt.Printf("%-14s true=%6d  estimate=%6d  over by %d\n", m, truth, cms.Estimate(m), cms.Estimate(m)-truth)
	}

	fmt.Println()
	fmt.Println("== HyperLogLog: distinct cards per merchant, 4 KB instead of a set ==")
	hll := must(NewHyperLogLog(12))
	distinct := map[string]bool{}
	cards := make([]string, 50_000)
	for i := range cards {
		cards[i] = cardToken(rng)
	}
	for i := 0; i < 300_000; i++ {
		card := cards[rng.NextIntN(int32(len(cards)))]
		hll.Add(card)
		distinct[card] = true
	}
	fmt.Printf("precision=%d registers=%d bytes=%d standard error=%.2f%%\n",
		hll.Precision, hll.Registers, hll.Bytes(), hll.StandardError()*100)
	fmt.Printf("300000 events, true distinct = %d, HLL estimate = %d (%.2f%% off)\n",
		len(distinct), hll.Cardinality(), 100.0*float64(hll.Cardinality()-int64(len(distinct)))/float64(len(distinct)))

	fmt.Println()
	fmt.Println("== Small sets: linear counting takes over ==")
	small := must(NewHyperLogLog(12))
	for i := 1; i <= 100; i++ {
		small.Add("card_" + strconv.Itoa(i))
	}
	fmt.Printf("100 distinct cards -> raw %.1f, corrected %d\n", small.RawEstimate(), small.Cardinality())

	fmt.Println()
	fmt.Println("== Merging two merchants' sketches is the union, not the sum ==")
	a, b := must(NewHyperLogLog(12)), must(NewHyperLogLog(12))
	for i := 0; i < 20_000; i++ {
		a.Add(cards[i])
	}
	for i := 10_000; i < 30_000; i++ {
		b.Add(cards[i])
	}
	fmt.Printf("a=%d b=%d merged=%d (true union 30000, sum would say %d)\n",
		a.Cardinality(), b.Cardinality(), must(a.Merge(b)).Cardinality(), a.Cardinality()+b.Cardinality())

	fmt.Println()
	fmt.Println("== Memory: exact vs sketch for 1,000 merchants x 50,000 cards ==")
	exactBytes := int64(1_000) * 50_000 * 40 // ~40 bytes per set entry is optimistic
	fmt.Printf("exact sets  ~ %s bytes\n", commas(exactBytes))
	fmt.Printf("1000 HLLs    = %s bytes\n", commas(1_000*hll.Bytes()))
	fmt.Printf("[%d, %d, %d] <- rho when the first rest-bit is 1, when it is the 8th, and when all 52 are zero\n",
		Rho(0x0008000000000000, 12), Rho(0x0000100000000000, 12), Rho(0, 12))
}
