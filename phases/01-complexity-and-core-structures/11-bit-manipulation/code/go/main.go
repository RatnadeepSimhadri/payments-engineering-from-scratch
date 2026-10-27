// Lesson: phases/01-complexity-and-core-structures/11-bit-manipulation/docs/en.md
// Bit Manipulation and Compact Encodings
// Run:  go run .
// Test: go test ./...
// Sources: ISO 8583-1:2003 (bitmap and data-element layout); Twitter Snowflake (github.com/twitter-archive/snowflake);
//
//	Warren, "Hacker's Delight", 2nd ed., ch. 2 and 5; Go spec, "Arithmetic operators" (shift semantics).
//
// Go 1.22+, standard library only. Same design as the Java and Python versions.
package main

import (
	"errors"
	"fmt"
	"math"
	"math/bits"
	"sort"
	"strconv"
	"strings"
)

var (
	// ErrBadField is returned when shift+width leaves the 64-bit word.
	ErrBadField = errors.New("field outside 64 bits")
	// ErrFieldOverflow is returned when a value does not fit its field.
	ErrFieldOverflow = errors.New("value does not fit in field")
	// ErrUnknownFlag is returned for bits outside the defined flag set.
	ErrUnknownFlag = errors.New("unknown flag bits")
	// ErrCapacityMismatch is returned when combining bitmaps of different capacities.
	ErrCapacityMismatch = errors.New("bitmaps must have equal capacity")
	// ErrBadIsoField is returned for an ISO 8583 field outside 2..128.
	ErrBadIsoField = errors.New("field must be 2..128 (field 1 is derived)")
	// ErrBadBitmapHex is returned for a bitmap string of the wrong length or inconsistent field 1.
	ErrBadBitmapHex = errors.New("malformed ISO 8583 bitmap")
	// ErrTimestampWindow is returned when a timestamp leaves the 41-bit window.
	ErrTimestampWindow = errors.New("timestamp outside the 41-bit window")
	// ErrNegativeID is returned by Decompose for a negative id.
	ErrNegativeID = errors.New("negative id: sign bit must be zero")
	// ErrClockBackwards is returned when the injected clock moves backwards.
	ErrClockBackwards = errors.New("clock moved backwards; refusing to issue ids")
)

// Mask is a mask of width low bits set: Mask(3) = 0b111. Mask(64) is all ones; Mask(0) is 0.
// It panics on a width outside 0..64, the same way Go treats an impossible shift count: a programming error.
func Mask(width int) int64 {
	if width < 0 || width > 64 {
		panic(fmt.Sprintf("width must be 0..64: %d", width))
	}
	if width == 64 {
		return -1
	}
	return int64(1)<<width - 1
}

// IsSet tests one bit; the uint64 view is Java's >>>.
func IsSet(v int64, bit int) bool   { return (uint64(v)>>bit)&1 != 0 }
func Set(v int64, bit int) int64    { return v | int64(1)<<bit }
func Clear(v int64, bit int) int64  { return v &^ (int64(1) << bit) }
func Toggle(v int64, bit int) int64 { return v ^ int64(1)<<bit }

func checkField(shift, width int) error {
	if shift < 0 || width < 1 || shift+width > 64 {
		return fmt.Errorf("%w: %d+%d", ErrBadField, shift, width)
	}
	return nil
}

// Extract reads width bits starting at shift as an unsigned value.
func Extract(v int64, shift, width int) (int64, error) {
	if err := checkField(shift, width); err != nil {
		return 0, err
	}
	return int64(uint64(v)>>shift) & Mask(width), nil
}

// Insert writes field into the width bits at shift, rejecting a field that does not fit.
func Insert(v int64, shift, width int, field int64) (int64, error) {
	if err := checkField(shift, width); err != nil {
		return 0, err
	}
	if field&^Mask(width) != 0 {
		return 0, fmt.Errorf("%w: %d in %d bits", ErrFieldOverflow, field, width)
	}
	hole := ^(Mask(width) << shift)
	return (v & hole) | (field << shift), nil
}

// PopcountKernighan counts one bits by clearing the lowest set bit until nothing is left. O(popcount).
func PopcountKernighan(v int64) int {
	n := 0
	for v != 0 {
		v &= v - 1
		n++
	}
	return n
}

// PopcountParallel counts one bits in constant time: add neighbours in 2-, 4-, 8-bit groups, then fold.
func PopcountParallel(v int64) int {
	u := uint64(v)
	u = u - ((u >> 1) & 0x5555555555555555)
	u = (u & 0x3333333333333333) + ((u >> 2) & 0x3333333333333333)
	u = (u + (u >> 4)) & 0x0F0F0F0F0F0F0F0F
	return int((u * 0x0101010101010101) >> 56)
}

// LowestSetBit is the lowest set bit on its own: 0b101100 -> 0b100. Zero for zero.
func LowestSetBit(v int64) int64 { return v & -v }

// IsPowerOfTwo is true for 1, 2, 4, ...
func IsPowerOfTwo(v int64) bool { return v > 0 && v&(v-1) == 0 }

// RoundUpToPowerOfTwo is the smallest power of two >= v, for v in 1..2^62. Used by hash-map and ring-buffer capacities.
func RoundUpToPowerOfTwo(v int64) (int64, error) {
	if v < 1 || v > 1<<62 {
		return 0, fmt.Errorf("out of range: %d", v)
	}
	if v == 1 {
		return 1, nil
	}
	return int64(1) << bits.Len64(uint64(v-1)), nil
}

// ToBinary is a fixed-width binary rendering, most significant bit first.
func ToBinary(v int64, width int) string {
	var sb strings.Builder
	for i := width - 1; i >= 0; i-- {
		if IsSet(v, i) {
			sb.WriteByte('1')
		} else {
			sb.WriteByte('0')
		}
	}
	return sb.String()
}

// ToHex is fixed-width upper-case hex of the unsigned view.
func ToHex(v int64, digits int) string {
	s := strings.ToUpper(strconv.FormatUint(uint64(v), 16))
	return strings.Repeat("0", max(0, digits-len(s))) + s
}

// Account flags: each flag owns one bit; combinations are OR, tests are AND.
const (
	Frozen         int32 = 1 << 0
	KycVerified    int32 = 1 << 1
	PayoutsEnabled int32 = 1 << 2
	HighRisk       int32 = 1 << 3
	ChargebackHold int32 = 1 << 4
)

var flagNames = []string{"FROZEN", "KYC_VERIFIED", "PAYOUTS_ENABLED", "HIGH_RISK", "CHARGEBACK_HOLD"}

// AccountFlags is account state as a set of flags in one int32.
type AccountFlags struct {
	bits int32
}

// NewAccountFlags validates that only known bits are set.
func NewAccountFlags(b int32) (AccountFlags, error) {
	if int64(b)&^Mask(len(flagNames)) != 0 {
		return AccountFlags{}, fmt.Errorf("%w in %b", ErrUnknownFlag, b)
	}
	return AccountFlags{b}, nil
}

// NoFlags is the empty set.
func NoFlags() AccountFlags { return AccountFlags{} }

func (f AccountFlags) Has(flag int32) bool             { return f.bits&flag == flag }
func (f AccountFlags) HasAny(flags int32) bool         { return f.bits&flags != 0 }
func (f AccountFlags) With(flag int32) AccountFlags    { return AccountFlags{f.bits | flag} }
func (f AccountFlags) Without(flag int32) AccountFlags { return AccountFlags{f.bits &^ flag} }
func (f AccountFlags) Bits() int32                     { return f.bits }

// CanPayOut is the business rule in one expression: may this account receive a payout right now?
func (f AccountFlags) CanPayOut() bool {
	return f.Has(KycVerified|PayoutsEnabled) && !f.HasAny(Frozen|ChargebackHold)
}

func (f AccountFlags) String() string {
	var on []string
	for i, name := range flagNames {
		if IsSet(int64(f.bits), i) {
			on = append(on, name)
		}
	}
	if len(on) == 0 {
		return "{}"
	}
	return "{" + strings.Join(on, ", ") + "}"
}

// Bitmap is a dense bit set over uint64 words: one bit per index, 64 per word.
// The structure behind "which of N things happened today".
type Bitmap struct {
	words    []uint64
	capacity int
}

// NewBitmap allocates ceil(capacity/64) words.
func NewBitmap(capacity int) *Bitmap {
	if capacity < 0 {
		panic("capacity must be >= 0")
	}
	return &Bitmap{words: make([]uint64, (capacity+63)>>6), capacity: capacity}
}

func (b *Bitmap) Capacity() int       { return b.capacity }
func (b *Bitmap) WordsAllocated() int { return len(b.words) }

// checkIndex panics like a slice index would.
func (b *Bitmap) checkIndex(i int) {
	if i < 0 || i >= b.capacity {
		panic(fmt.Sprintf("bit %d not in 0..%d", i, b.capacity-1))
	}
}

func (b *Bitmap) Set(i int)   { b.checkIndex(i); b.words[i>>6] |= 1 << (i & 63) }
func (b *Bitmap) Clear(i int) { b.checkIndex(i); b.words[i>>6] &^= 1 << (i & 63) }
func (b *Bitmap) Get(i int) bool {
	b.checkIndex(i)
	return b.words[i>>6]&(1<<(i&63)) != 0
}

// Cardinality is the number of set bits.
func (b *Bitmap) Cardinality() int {
	n := 0
	for _, w := range b.words {
		n += PopcountParallel(int64(w))
	}
	return n
}

// NextSetBit is the index of the first set bit at or after from, or -1. Skips whole zero words.
func (b *Bitmap) NextSetBit(from int) int {
	if from < 0 {
		panic("from must be >= 0")
	}
	if from >= b.capacity {
		return -1
	}
	w := from >> 6
	word := b.words[w] & (math.MaxUint64 << (from & 63)) // drop bits below from
	for {
		if word != 0 {
			idx := (w << 6) + bits.TrailingZeros64(word)
			if idx < b.capacity {
				return idx
			}
			return -1
		}
		w++
		if w == len(b.words) {
			return -1
		}
		word = b.words[w]
	}
}

// And intersects two bitmaps of equal capacity.
func (b *Bitmap) And(other *Bitmap) (*Bitmap, error) { return b.combine(other, true) }

// Or unions two bitmaps of equal capacity.
func (b *Bitmap) Or(other *Bitmap) (*Bitmap, error) { return b.combine(other, false) }

func (b *Bitmap) combine(other *Bitmap, and bool) (*Bitmap, error) {
	if other.capacity != b.capacity {
		return nil, ErrCapacityMismatch
	}
	out := NewBitmap(b.capacity)
	for i := range b.words {
		if and {
			out.words[i] = b.words[i] & other.words[i]
		} else {
			out.words[i] = b.words[i] | other.words[i]
		}
	}
	return out, nil
}

// Iso8583Encode hex-encodes the bitmap(s) for a set of present fields: 16 hex characters, or 32 when any field > 64 is present.
// Field f (1..64) is bit (64 - f) of the primary bitmap, so field 1 is the most significant bit. Field 1 itself
// means "a secondary bitmap follows" and announces fields 65..128 the same way.
func Iso8583Encode(fields []int) (string, error) {
	var primary, secondary uint64
	for _, f := range fields {
		if f < 2 || f > 128 {
			return "", fmt.Errorf("%w: %d", ErrBadIsoField, f)
		}
		if f <= 64 {
			primary |= 1 << (64 - f)
		} else {
			secondary |= 1 << (128 - f)
		}
	}
	if secondary != 0 {
		primary |= 1 << 63 // field 1: secondary bitmap present
		return ToHex(int64(primary), 16) + ToHex(int64(secondary), 16), nil
	}
	return ToHex(int64(primary), 16), nil
}

// Iso8583Decode decodes 16 or 32 hex characters into the ascending list of present fields (field 1 excluded, it is structural).
func Iso8583Decode(hex string) ([]int, error) {
	if len(hex) != 16 && len(hex) != 32 {
		return nil, fmt.Errorf("%w: must be 16 or 32 hex chars, got %d", ErrBadBitmapHex, len(hex))
	}
	primary, err := strconv.ParseUint(hex[:16], 16, 64)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadBitmapHex, err)
	}
	hasSecondary := IsSet(int64(primary), 63)
	if hasSecondary != (len(hex) == 32) {
		return nil, fmt.Errorf("%w: field 1 disagrees with the length", ErrBadBitmapHex)
	}
	fields := []int{}
	for f := 2; f <= 64; f++ {
		if IsSet(int64(primary), 64-f) {
			fields = append(fields, f)
		}
	}
	if hasSecondary {
		secondary, err := strconv.ParseUint(hex[16:], 16, 64)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrBadBitmapHex, err)
		}
		for f := 65; f <= 128; f++ {
			if IsSet(int64(secondary), 128-f) {
				fields = append(fields, f)
			}
		}
	}
	return fields, nil
}

// Snowflake-style 64-bit id: 0 | 41-bit milliseconds since a custom epoch | 10-bit worker | 12-bit sequence.
// Ids are unique per worker and increase with time, so they sort by creation order as plain int64s.
const (
	EpochMillis    int64 = 1_700_000_000_000 // 2023-11-14T22:13:20Z; ids stay positive until 2093
	SequenceBits         = 12
	WorkerBits           = 10
	TimestampBits        = 41
	WorkerShift          = SequenceBits
	TimestampShift       = SequenceBits + WorkerBits
	MaxSequence    int64 = 1<<SequenceBits - 1
	MaxWorker      int64 = 1<<WorkerBits - 1
	MaxTimestamp   int64 = 1<<TimestampBits - 1
)

// Parts is a decomposed id.
type Parts struct {
	TimestampMillis int64
	Worker          int64
	Sequence        int64
}

func (p Parts) String() string {
	return fmt.Sprintf("Parts[timestampMillis=%d, worker=%d, sequence=%d]", p.TimestampMillis, p.Worker, p.Sequence)
}

// Compose packs the three fields.
func Compose(timestampMillis, worker, sequence int64) (int64, error) {
	delta := timestampMillis - EpochMillis
	if delta < 0 || delta > MaxTimestamp {
		return 0, ErrTimestampWindow
	}
	id, err := Insert(0, TimestampShift, TimestampBits, delta)
	if err != nil {
		return 0, err
	}
	if id, err = Insert(id, WorkerShift, WorkerBits, worker); err != nil {
		return 0, err
	}
	return Insert(id, 0, SequenceBits, sequence)
}

// Decompose unpacks an id.
func Decompose(id int64) (Parts, error) {
	if id < 0 {
		return Parts{}, ErrNegativeID
	}
	ts, _ := Extract(id, TimestampShift, TimestampBits)
	worker, _ := Extract(id, WorkerShift, WorkerBits)
	seq, _ := Extract(id, 0, SequenceBits)
	return Parts{ts + EpochMillis, worker, seq}, nil
}

// Generator is a per-worker id generator. The clock is injected so tests and the demo are deterministic.
type Generator struct {
	worker     int64
	clock      func() int64
	lastMillis int64
	sequence   int64
	waits      int64 // instrumentation: how often the sequence overflowed within one millisecond
}

// NewGenerator validates the worker id.
func NewGenerator(worker int64, clock func() int64) (*Generator, error) {
	if worker < 0 || worker > MaxWorker {
		return nil, fmt.Errorf("worker must be 0..%d", MaxWorker)
	}
	return &Generator{worker: worker, clock: clock, lastMillis: -1}, nil
}

// Next issues the next id, refusing when the clock has moved backwards.
func (g *Generator) Next() (int64, error) {
	now := g.clock()
	if now < g.lastMillis {
		return 0, fmt.Errorf("%w: by %d ms", ErrClockBackwards, g.lastMillis-now)
	}
	if now == g.lastMillis {
		g.sequence = (g.sequence + 1) & MaxSequence
		if g.sequence == 0 { // 4096 ids in one millisecond: wait for the clock to advance
			g.waits++
			for now <= g.lastMillis {
				now = g.clock()
			}
		}
	} else {
		g.sequence = 0
	}
	g.lastMillis = now
	return Compose(now, g.worker, g.sequence)
}

// Waits is how many times the sequence overflowed within one millisecond.
func (g *Generator) Waits() int64 { return g.waits }

func mustInsert(v int64, shift, width int, field int64) int64 {
	out, err := Insert(v, shift, width, field)
	if err != nil {
		panic(err)
	}
	return out
}

func mustExtract(v int64, shift, width int) int64 {
	out, err := Extract(v, shift, width)
	if err != nil {
		panic(err)
	}
	return out
}

func formatInts(xs []int) string {
	parts := make([]string, len(xs))
	for i, x := range xs {
		parts[i] = strconv.Itoa(x)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func demoMasksAndFields() {
	fmt.Println("== Masks, fields, popcount ==")
	v := int64(0b1011_0100)
	fmt.Println("v            = " + ToBinary(v, 8))
	fmt.Println("set bit 0    = " + ToBinary(Set(v, 0), 8))
	fmt.Println("clear bit 7  = " + ToBinary(Clear(v, 7), 8))
	fmt.Println("toggle bit 2 = " + ToBinary(Toggle(v, 2), 8))
	fmt.Printf("extract bits 4..6 = %d  (0b011)\n", mustExtract(v, 4, 3))
	fmt.Println("insert 0b110 at 4 = " + ToBinary(mustInsert(v, 4, 3, 0b110), 8))
	fmt.Printf("popcount(v) kernighan=%d parallel=%d bits.OnesCount64=%d\n", PopcountKernighan(v), PopcountParallel(v), bits.OnesCount64(uint64(v)))
	fmt.Println("lowest set bit of 0b1011_0100 = " + ToBinary(LowestSetBit(v), 8))
	r, _ := RoundUpToPowerOfTwo(1000)
	fmt.Printf("roundUpToPowerOfTwo(1000) = %d, isPowerOfTwo(1024) = %v\n", r, IsPowerOfTwo(1024))
	fmt.Println()
}

func demoFlags() {
	fmt.Println("== Account flags: five booleans in one int ==")
	f := NoFlags().With(KycVerified).With(PayoutsEnabled)
	fmt.Printf("%v bits=%s canPayOut=%v\n", f, ToBinary(int64(f.Bits()), 5), f.CanPayOut())
	f = f.With(ChargebackHold)
	fmt.Printf("%v bits=%s canPayOut=%v\n", f, ToBinary(int64(f.Bits()), 5), f.CanPayOut())
	f = f.Without(ChargebackHold).With(HighRisk)
	fmt.Printf("%v bits=%s canPayOut=%v (high risk alone does not block)\n", f, ToBinary(int64(f.Bits()), 5), f.CanPayOut())
	fmt.Println()
}

func firstN(b *Bitmap, n int) []int {
	out := []int{}
	for i := b.NextSetBit(0); i >= 0 && len(out) < n; i = b.NextSetBit(i + 1) {
		out = append(out, i)
	}
	return out
}

func demoBitmap() {
	fmt.Println("== Bitmap: which of 1,000,000 merchants settled today ==")
	merchants := 1_000_000
	settledToday, activeThisMonth := NewBitmap(merchants), NewBitmap(merchants)
	for m := 0; m < merchants; m += 7 {
		settledToday.Set(m)
	}
	for m := 0; m < merchants; m += 5 {
		activeThisMonth.Set(m)
	}
	both, _ := activeThisMonth.And(settledToday)
	fmt.Printf("memory: %d words = %d KB for %d merchants (a []bool would be %d KB, a map[int]struct{} several times that)\n",
		settledToday.WordsAllocated(), settledToday.WordsAllocated()*8/1024, merchants, merchants/1024)
	fmt.Printf("settled today: %d, active this month: %d, both: %d\n", settledToday.Cardinality(), activeThisMonth.Cardinality(), both.Cardinality())
	fmt.Printf("first five settled merchants: %s; first set bit at or after 100: %d\n", formatInts(firstN(settledToday, 5)), settledToday.NextSetBit(100))
	fmt.Println()
}

func demoIso8583() {
	fmt.Println("== ISO 8583 bitmap ==")
	auth := []int{2, 3, 4, 7, 11, 12, 13, 22, 24, 25, 35, 41, 42, 49}
	primary, _ := Iso8583Encode(auth)
	fmt.Println("0100 authorisation request fields " + formatInts(auth))
	fmt.Println("primary bitmap hex   = " + primary)
	raw, _ := strconv.ParseUint(primary, 16, 64)
	fmt.Println("primary bitmap bits  = " + ToBinary(int64(raw), 64))
	decoded, _ := Iso8583Decode(primary)
	fmt.Println("decoded              = " + formatInts(decoded))
	withSecondary := append(append([]int{}, auth...), 102)
	sort.Ints(withSecondary)
	both, _ := Iso8583Encode(withSecondary)
	fmt.Println("adding field 102 (account id 1) sets field 1 and appends a secondary bitmap:")
	fmt.Printf("bitmaps hex          = %s  (%d hex chars)\n", both, len(both))
	decodedBoth, _ := Iso8583Decode(both)
	fmt.Println("decoded              = " + formatInts(decodedBoth))
	fmt.Println()
}

func demoSnowflake() {
	fmt.Println("== Snowflake-style ids: 41 + 10 + 12 bits ==")
	fakeNow := EpochMillis + 86_400_000 // one day after the epoch
	calls := 0
	clock := func() int64 { // advances 1 ms every 5,000 reads
		calls++
		if calls%5_000 == 0 {
			fakeNow++
		}
		return fakeNow
	}
	gen, _ := NewGenerator(7, clock)
	prev := int64(-1)
	monotonic := true
	var first int64
	for i := 0; i < 10_000; i++ {
		id, err := gen.Next()
		if err != nil {
			panic(err)
		}
		if i == 0 {
			first = id
		}
		if id <= prev {
			monotonic = false
		}
		prev = id
	}
	p, _ := Decompose(first)
	fmt.Printf("first id = %d = %s\n", first, ToBinary(first, 64))
	fmt.Printf("  timestamp %d ms, worker %d, sequence %d\n", p.TimestampMillis, p.Worker, p.Sequence)
	fmt.Printf("10,000 ids from one worker: strictly increasing = %v, sequence overflows waited out = %d\n", monotonic, gen.Waits())
	last, _ := Decompose(prev)
	fmt.Printf("last id decomposed: %v\n", last)
	fmt.Println()
}

func demoTwosComplementTraps() {
	fmt.Println("== Two's complement traps ==")
	minInt := int64(math.MinInt64)
	abs := minInt
	if abs < 0 {
		abs = -abs
	}
	fmt.Printf("-math.MinInt64                = %d  (still negative: there is no +2^63)\n", abs)
	maxInt := int64(math.MaxInt64)
	fmt.Printf("math.MaxInt64 + 1             = %d  (wraps silently; a checked add must test for it)\n", maxInt+1)
	raw := 0x80
	fmt.Printf("int8(0x80)                    = %d  and uint8(int8(0x80)) = %d  (mask before you widen)\n", int8(raw), uint8(int8(raw)))
	minusEight := int32(-8)
	fmt.Printf("-8 >> 1  (arithmetic)         = %d   uint32(-8) >> 1 (logical) = %d\n", minusEight>>1, uint32(minusEight)>>1)
	one := int32(1)
	fmt.Printf("1 << 31  (int32)              = %d  1 << 31 (int64) = %d\n", one<<31, int64(one)<<31)
	shift := uint(32)
	fmt.Printf("1 << 32  (int32)              = %d  (Go does not reduce the shift count: a shift past the width is zero; Java gives 1)\n", one<<shift)
	lo, hi := int32(2_000_000_000), int32(2_100_000_000)
	fmt.Printf("(lo + hi) / 2 with int32      = %d  vs  lo + int32(uint32(hi-lo)>>1) = %d  (the binary-search midpoint bug)\n", (lo+hi)/2, lo+int32(uint32(hi-lo)>>1))
	seven := int32(-7)
	fmt.Printf("-7 / 2 = %d  -7 >> 1 = %d  (division truncates toward zero, shift floors)\n", seven/2, seven>>1)
	fmt.Printf("-7 %% 2 = %d   -7 & 1 = %d  (use & for \"is odd\" on negatives)\n", seven%2, seven&1)
}

func main() {
	demoMasksAndFields()
	demoFlags()
	demoBitmap()
	demoIso8583()
	demoSnowflake()
	demoTwosComplementTraps()
}
