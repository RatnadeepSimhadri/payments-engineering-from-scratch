// Lesson: phases/03-systems-data-structures/13-unique-id-generation/docs/en.md
// Unique ID Generation: UUIDs, ULIDs, and Snowflake
// Run:  go run .
// Test: go test ./...
// Sources: RFC 9562 (UUIDs, versions 4 and 7, monotonic counters); ULID spec (ulid/spec on GitHub, Crockford base32);
// Twitter Snowflake announcement (2010) for the 41/10/12 layout. Go 1.22+, standard library only. Same design as the Java and Python versions.
package main

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
)

var (
	// ErrInvalidArgument is returned where the Java throws IllegalArgumentException.
	ErrInvalidArgument = errors.New("invalid argument")
	// ErrIllegalState is returned where the Java throws IllegalStateException.
	ErrIllegalState = errors.New("illegal state")
	// ErrClockMovedBackwards is matched by errors.Is for any *ClockMovedBackwardsError.
	ErrClockMovedBackwards = errors.New("clock moved backwards")
)

// ---------------------------------------------------------------- Clock ----

// Clock is the injected time source in milliseconds since the Unix epoch.
type Clock interface {
	NowMillis() int64
}

type ManualClock struct{ now int64 }

func NewManualClock(startMillis int64) *ManualClock { return &ManualClock{now: startMillis} }

func (c *ManualClock) NowMillis() int64 { return c.now }

func (c *ManualClock) Advance(millis int64) { c.now += millis }

// Set: clocks do go backwards in real systems (NTP step, VM migration). Tests need to simulate it.
func (c *ManualClock) Set(millis int64) { c.now = millis }

// ---------------------------------------------------------------- Uuid ----

// Uuid is 128 bits as two uint64s, with the RFC 9562 text form. Built here rather than using a library so the bits are visible.
// The halves are unsigned, so ordinary < is the unsigned comparison Java has to spell out with Long.compareUnsigned.
type Uuid struct {
	Msb, Lsb uint64
}

const hexDigits = "0123456789abcdef"

func (u Uuid) Version() int { return int((u.Msb >> 12) & 0xF) }

// Variant is 2 for the RFC 9562 variant (bit pattern 10xx in the top of the low half).
func (u Uuid) Variant() int { return int((u.Lsb >> 62) & 0x3) }

// TimestampMillis is, for version 7, the 48-bit Unix millisecond timestamp in the top of the high half.
func (u Uuid) TimestampMillis() int64 { return int64(u.Msb >> 16) }

func (u Uuid) Compare(o Uuid) int {
	if u.Msb != o.Msb {
		if u.Msb < o.Msb {
			return -1
		}
		return 1
	}
	if u.Lsb != o.Lsb {
		if u.Lsb < o.Lsb {
			return -1
		}
		return 1
	}
	return 0
}

func (u Uuid) String() string {
	out := make([]byte, 36)
	pos := 0
	pos = writeHex(out, pos, u.Msb>>32, 8)
	out[pos] = '-'
	pos++
	pos = writeHex(out, pos, u.Msb>>16, 4)
	out[pos] = '-'
	pos++
	pos = writeHex(out, pos, u.Msb, 4)
	out[pos] = '-'
	pos++
	pos = writeHex(out, pos, u.Lsb>>48, 4)
	out[pos] = '-'
	pos++
	writeHex(out, pos, u.Lsb, 12)
	return string(out)
}

func writeHex(out []byte, pos int, value uint64, digits int) int {
	for i := digits - 1; i >= 0; i-- {
		out[pos+i] = hexDigits[value&0xF]
		value >>= 4
	}
	return pos + digits
}

func ParseUuid(text string) (Uuid, error) {
	if len(text) != 36 || text[8] != '-' || text[13] != '-' || text[18] != '-' || text[23] != '-' {
		return Uuid{}, fmt.Errorf("%w: not a UUID: %s", ErrInvalidArgument, text)
	}
	hex := strings.ReplaceAll(text, "-", "")
	msb, err := strconv.ParseUint(hex[:16], 16, 64)
	if err != nil {
		return Uuid{}, fmt.Errorf("%w: not a UUID: %s", ErrInvalidArgument, text)
	}
	lsb, err := strconv.ParseUint(hex[16:], 16, 64)
	if err != nil {
		return Uuid{}, fmt.Errorf("%w: not a UUID: %s", ErrInvalidArgument, text)
	}
	return Uuid{msb, lsb}, nil
}

// ---------------------------------------------------------------- UUID generators ----

// UuidV4Generator is RFC 9562 version 4: 122 random bits. No order, no time, no coordination.
type UuidV4Generator struct{ rng *JavaRandom }

func NewUuidV4Generator(rng *JavaRandom) *UuidV4Generator { return &UuidV4Generator{rng} }

func (g *UuidV4Generator) Next() Uuid {
	msb, lsb := uint64(g.rng.NextLong()), uint64(g.rng.NextLong())
	msb = (msb &^ 0xF000) | 0x4000                        // version nibble = 4
	lsb = (lsb & 0x3FFFFFFFFFFFFFFF) | 0x8000000000000000 // variant bits = 10
	return Uuid{msb, lsb}
}

// UuidV7Generator is RFC 9562 version 7: 48-bit Unix ms timestamp, 12-bit counter (rand_a, "method 1"), 62 random bits (rand_b).
// Monotonic within one generator: same millisecond increments the counter; a backwards clock keeps using the
// last timestamp; a counter overflow borrows the next millisecond.
type UuidV7Generator struct {
	clock      Clock
	rng        *JavaRandom
	lastMillis int64
	counter    int
}

func NewUuidV7Generator(clock Clock, rng *JavaRandom) *UuidV7Generator {
	return &UuidV7Generator{clock: clock, rng: rng, lastMillis: -1}
}

func (g *UuidV7Generator) Next() Uuid {
	now := g.clock.NowMillis()
	if now <= g.lastMillis { // same ms, or clock went backwards
		g.counter++
		if g.counter > 0xFFF { // borrow the next ms
			g.lastMillis++
			g.counter = int(g.rng.NextIntN(1 << 11))
		}
	} else {
		g.lastMillis = now
		g.counter = int(g.rng.NextIntN(1 << 11)) // leave the top counter bit clear: room to count
	}
	msb := (uint64(g.lastMillis) << 16) | 0x7000 | uint64(g.counter)
	lsb := (uint64(g.rng.NextLong()) & 0x3FFFFFFFFFFFFFFF) | 0x8000000000000000
	return Uuid{msb, lsb}
}

// ---------------------------------------------------------------- Ulid ----

// Ulid: 48-bit ms timestamp + 80 random bits, Crockford base32, 26 characters, lexically sortable.
// RandomHi holds the top 16 random bits, RandomLo the low 64 (unsigned, so the Java -1L is math.MaxUint64 here).
type Ulid struct {
	TimestampMillis int64
	RandomHi        int64
	RandomLo        uint64
}

const UlidAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ" // no I, L, O, U

var ulidDecode = func() [128]int {
	var d [128]int
	for i := range d {
		d[i] = -1
	}
	for i := 0; i < len(UlidAlphabet); i++ {
		c := UlidAlphabet[i]
		d[c] = i
		d[strings.ToLower(string(c))[0]] = i
	}
	// Crockford's alphabet excludes I, L, O, U to avoid misreads; decoding accepts the first three leniently.
	d['I'], d['i'], d['L'], d['l'], d['O'], d['o'] = 1, 1, 1, 1, 0, 0
	return d
}()

func NewUlid(timestampMillis, randomHi int64, randomLo uint64) (Ulid, error) {
	if timestampMillis < 0 || timestampMillis >= 1<<48 {
		return Ulid{}, fmt.Errorf("%w: timestamp must fit in 48 bits", ErrInvalidArgument)
	}
	if randomHi < 0 || randomHi >= 1<<16 {
		return Ulid{}, fmt.Errorf("%w: randomHi must fit in 16 bits", ErrInvalidArgument)
	}
	return Ulid{timestampMillis, randomHi, randomLo}, nil
}

func (u Ulid) Compare(o Ulid) int {
	if c := cmpInt64(u.TimestampMillis, o.TimestampMillis); c != 0 {
		return c
	}
	if c := cmpInt64(u.RandomHi, o.RandomHi); c != 0 {
		return c
	}
	if u.RandomLo != o.RandomLo {
		if u.RandomLo < o.RandomLo {
			return -1
		}
		return 1
	}
	return 0
}

func cmpInt64(a, b int64) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}

// String: 10 chars of timestamp (50 bits, top two zero) + 16 chars of randomness (80 bits).
func (u Ulid) String() string {
	out := make([]byte, 26)
	t := uint64(u.TimestampMillis)
	for i := 9; i >= 0; i-- {
		out[i] = UlidAlphabet[t&0x1F]
		t >>= 5
	}
	// 80 random bits = RandomHi (16) followed by RandomLo (64): emit from the low end, 5 bits at a time
	lo, hi := u.RandomLo, uint64(u.RandomHi)
	for i := 25; i >= 10; i-- {
		out[i] = UlidAlphabet[lo&0x1F]
		lo = (lo >> 5) | ((hi & 0x1F) << 59)
		hi >>= 5
	}
	return string(out)
}

func ParseUlid(text string) (Ulid, error) {
	if len(text) != 26 {
		return Ulid{}, fmt.Errorf("%w: ULID must be 26 characters: %s", ErrInvalidArgument, text)
	}
	var t uint64
	for i := 0; i < 10; i++ {
		d, err := ulidDigit(text[i])
		if err != nil {
			return Ulid{}, err
		}
		t = (t << 5) | uint64(d)
	}
	if t >= 1<<48 {
		return Ulid{}, fmt.Errorf("%w: ULID timestamp overflows 48 bits: %s", ErrInvalidArgument, text)
	}
	var hi, lo uint64
	for i := 10; i < 26; i++ {
		d, err := ulidDigit(text[i])
		if err != nil {
			return Ulid{}, err
		}
		hi = (hi << 5) | (lo >> 59)
		lo = (lo << 5) | uint64(d)
	}
	return NewUlid(int64(t), int64(hi&0xFFFF), lo)
}

func ulidDigit(c byte) (int, error) {
	d := -1
	if c < 128 {
		d = ulidDecode[c]
	}
	if d < 0 {
		return 0, fmt.Errorf("%w: invalid Crockford base32 character: %c", ErrInvalidArgument, c)
	}
	return d, nil
}

// UlidGenerator is the monotonic ULID generator: same millisecond increments the 80-bit random part by one (ULID spec, "monotonicity").
type UlidGenerator struct {
	clock   Clock
	rng     *JavaRandom
	last    Ulid
	hasLast bool
}

func NewUlidGenerator(clock Clock, rng *JavaRandom) *UlidGenerator {
	return &UlidGenerator{clock: clock, rng: rng}
}

// Resume continues after a restart from the last id this process issued, so monotonicity survives the restart.
func (g *UlidGenerator) Resume(lastIssued Ulid) { g.last, g.hasLast = lastIssued, true }

func (g *UlidGenerator) Next() (Ulid, error) {
	now := g.clock.NowMillis()
	if g.hasLast && now <= g.last.TimestampMillis {
		lo := g.last.RandomLo + 1 // uint64 wraps to 0 exactly where the Java long does
		hi := g.last.RandomHi
		if lo == 0 {
			hi++
		}
		if hi > 0xFFFF {
			return Ulid{}, fmt.Errorf("%w: ULID randomness overflowed within one millisecond", ErrIllegalState)
		}
		g.last = Ulid{g.last.TimestampMillis, hi, lo}
	} else {
		u, err := NewUlid(now, int64(g.rng.NextIntN(1<<16)), uint64(g.rng.NextLong()))
		if err != nil {
			return Ulid{}, err
		}
		g.last = u
	}
	g.hasLast = true
	return g.last, nil
}

// ---------------------------------------------------------------- Snowflake ----

// SnowflakeGenerator: 63 usable bits as 41-bit ms since a custom epoch | 10-bit worker id | 12-bit sequence.
// Sortable by time within a worker, 4,096 ids per ms per worker, no coordination beyond assigning worker ids.
const (
	EpochMillis  int64 = 1_704_067_200_000 // 2024-01-01T00:00:00Z
	WorkerBits         = 10
	SequenceBits       = 12
	MaxWorker    int64 = (1 << WorkerBits) - 1
	MaxSequence  int64 = (1 << SequenceBits) - 1
	MaxTimestamp int64 = (1 << 41) - 1
)

type SnowflakeGenerator struct {
	clock              Clock
	workerID           int64
	maxBackwardsMillis int64
	lastMillis         int64
	sequence           int64
	BorrowedMillis     int64 // how many ms logical time ran ahead of the clock
}

func NewSnowflakeGenerator(clock Clock, workerID, maxBackwardsMillis int64) (*SnowflakeGenerator, error) {
	if workerID < 0 || workerID > MaxWorker {
		return nil, fmt.Errorf("%w: worker id must be 0..%d", ErrInvalidArgument, MaxWorker)
	}
	return &SnowflakeGenerator{clock: clock, workerID: workerID, maxBackwardsMillis: maxBackwardsMillis, lastMillis: -1}, nil
}

func (g *SnowflakeGenerator) Next() (int64, error) {
	now := g.clock.NowMillis() - EpochMillis
	if now < g.lastMillis {
		if g.lastMillis-now > g.maxBackwardsMillis {
			return 0, &ClockMovedBackwardsError{ByMillis: g.lastMillis - now}
		}
		now = g.lastMillis // small drift: keep issuing on the last timestamp
	}
	if now == g.lastMillis {
		g.sequence = (g.sequence + 1) & MaxSequence
		if g.sequence == 0 { // 4096 in one ms: borrow the next
			g.lastMillis++
			g.BorrowedMillis++
		}
	} else {
		g.lastMillis = now
		g.sequence = 0
	}
	if g.lastMillis > MaxTimestamp {
		return 0, fmt.Errorf("%w: snowflake epoch exhausted", ErrIllegalState)
	}
	return (g.lastMillis << (WorkerBits + SequenceBits)) | (g.workerID << SequenceBits) | g.sequence, nil
}

func TimestampMillisOf(id int64) int64 {
	return int64(uint64(id)>>(WorkerBits+SequenceBits)) + EpochMillis
}
func WorkerIdOf(id int64) int64 { return int64(uint64(id)>>SequenceBits) & MaxWorker }
func SequenceOf(id int64) int64 { return id & MaxSequence }

// ClockMovedBackwardsError is the Java ClockMovedBackwardsException: an error value carrying the drift.
type ClockMovedBackwardsError struct{ ByMillis int64 }

func (e *ClockMovedBackwardsError) Error() string {
	return fmt.Sprintf("clock moved backwards by %d ms; refusing to issue ids that could collide", e.ByMillis)
}

// Is lets errors.Is(err, ErrClockMovedBackwards) match any instance, whatever its drift.
func (e *ClockMovedBackwardsError) Is(target error) bool { return target == ErrClockMovedBackwards }

// ---------------------------------------------------------------- Demo ----

// CollisionProbability is the birthday bound: probability of at least one collision among n ids drawn uniformly from 2^bits values.
func CollisionProbability(n float64, bitCount int) float64 {
	pairs := n * (n - 1) / 2.0
	return 1.0 - math.Exp(-pairs/math.Pow(2.0, float64(bitCount)))
}

// IdsForCollisionProbability: how many ids until the collision probability reaches p: n ~ sqrt(2 * 2^bits * ln(1/(1-p))).
func IdsForCollisionProbability(p float64, bitCount int) float64 {
	return math.Sqrt(2.0 * math.Pow(2.0, float64(bitCount)) * math.Log(1.0/(1.0-p)))
}

func IsStrictlyIncreasing[T any](items []T, compare func(a, b T) int) bool {
	for i := 1; i < len(items); i++ {
		if compare(items[i-1], items[i]) >= 0 {
			return false
		}
	}
	return true
}

func IsLexicallyIncreasing(items []string) bool {
	return IsStrictlyIncreasing(items, strings.Compare)
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func main() {
	rng := NewJavaRandom(7)
	clock := NewManualClock(1_760_000_000_000) // 2025-10-09T08:53:20Z

	fmt.Println("== 1. Four ids for the same posting, four shapes ==")
	v4 := NewUuidV4Generator(rng)
	v7 := NewUuidV7Generator(clock, rng)
	ulid := NewUlidGenerator(clock, rng)
	flake := must(NewSnowflakeGenerator(clock, 17, 5))
	a4, a7, au, af := v4.Next(), v7.Next(), must(ulid.Next()), must(flake.Next())
	fmt.Printf("  UUIDv4    %s  version=%d variant=%d  (122 random bits)\n", a4, a4.Version(), a4.Variant())
	fmt.Printf("  UUIDv7    %s  version=%d ts=%d (48-bit ms, 12-bit counter, 62 random)\n", a7, a7.Version(), a7.TimestampMillis())
	fmt.Printf("  ULID      %s  ts=%d (48-bit ms, 80 random, base32)\n", au, au.TimestampMillis)
	fmt.Printf("  Snowflake %d  ts=%d worker=%d seq=%d (63 bits)\n", af, TimestampMillisOf(af), WorkerIdOf(af), SequenceOf(af))

	fmt.Println()
	fmt.Println("== 2. Sortability: 1000 ids over 1000 ms, then sort the strings ==")
	var flakes []int64
	var v4Str, v7Str, ulidStr []string
	for i := 0; i < 1000; i++ {
		clock.Advance(1)
		x4, x7, xu, xf := v4.Next(), v7.Next(), must(ulid.Next()), must(flake.Next())
		flakes = append(flakes, xf)
		v4Str = append(v4Str, x4.String())
		v7Str = append(v7Str, x7.String())
		ulidStr = append(ulidStr, xu.String())
	}
	fmt.Printf("  generation order == string sort order?  v4=%t  v7=%t  ulid=%t  snowflake=%t\n",
		IsLexicallyIncreasing(v4Str), IsLexicallyIncreasing(v7Str), IsLexicallyIncreasing(ulidStr), IsStrictlyIncreasing(flakes, cmpInt64))
	sorted4 := slices.Clone(v4Str)
	slices.Sort(sorted4)
	displaced := 0
	for i := 0; i < 1000; i++ {
		if sorted4[i] != v4Str[i] {
			displaced++
		}
	}
	fmt.Printf("  v4: %d of 1000 ids change position when sorted -> random B-tree page writes\n", displaced)

	fmt.Println()
	fmt.Println("== 3. Monotonic within one millisecond ==")
	frozen := NewManualClock(1_760_000_000_000)
	fv7 := NewUuidV7Generator(frozen, NewJavaRandom(1))
	fu := NewUlidGenerator(frozen, NewJavaRandom(1))
	ff := must(NewSnowflakeGenerator(frozen, 3, 5))
	var burst7 []Uuid
	var burstU []Ulid
	var burstF []int64
	for i := 0; i < 5000; i++ {
		burst7 = append(burst7, fv7.Next())
		burstU = append(burstU, must(fu.Next()))
		burstF = append(burstF, must(ff.Next()))
	}
	fmt.Printf("  5000 ids, clock frozen: v7 increasing=%t ulid increasing=%t snowflake increasing=%t\n",
		IsStrictlyIncreasing(burst7, Uuid.Compare), IsStrictlyIncreasing(burstU, Ulid.Compare), IsStrictlyIncreasing(burstF, cmpInt64))
	fmt.Printf("  v7 first two: %s -> %s (counter +1)\n", burst7[0], burst7[1])
	fmt.Printf("  ulid first two: %s -> %s (random part +1)\n", burstU[0], burstU[1])
	fmt.Printf("  snowflake borrowed %d ms of logical time to issue 5000 ids at 4096/ms\n", ff.BorrowedMillis)

	fmt.Println()
	fmt.Println("== 4. The clock moves backwards ==")
	drift := NewManualClock(1_760_000_000_000)
	sf := must(NewSnowflakeGenerator(drift, 9, 5))
	before := must(sf.Next())
	drift.Set(1_760_000_000_000 - 3)
	after := must(sf.Next())
	fmt.Printf("  3 ms backwards (within tolerance 5): issued %d then %d, still increasing=%t\n", before, after, after > before)
	drift.Set(1_760_000_000_000 - 60_000)
	if _, err := sf.Next(); err != nil {
		var moved *ClockMovedBackwardsError
		if errors.As(err, &moved) {
			fmt.Println("  60 s backwards: " + moved.Error())
		}
	}
	dv7 := NewUuidV7Generator(drift, NewJavaRandom(2))
	drift.Set(1_760_000_000_000)
	u1 := dv7.Next()
	drift.Set(1_760_000_000_000 - 60_000)
	u2 := dv7.Next()
	fmt.Printf("  v7 after a 60 s step back: keeps the last timestamp, increasing=%t\n", u2.Compare(u1) > 0)

	fmt.Println()
	fmt.Println("== 5. Collision math (birthday bound) ==")
	fmt.Printf("  UUIDv4, 122 random bits: 10^12 ids -> p = %.1e; 50%% at %.2e ids\n", CollisionProbability(1e12, 122), IdsForCollisionProbability(0.5, 122))
	fmt.Printf("  ULID, 80 random bits per ms: 10^6 ids in one ms -> p = %.1e; 10^9 in one ms -> p = %.1e\n", CollisionProbability(1e6, 80), CollisionProbability(1e9, 80))
	fmt.Printf("  UUIDv7, 62 random bits per ms across uncoordinated generators: 10^6 ids in one ms -> p = %.1e\n", CollisionProbability(1e6, 62))
	fmt.Printf("  a 32-bit random id: 10^5 ids -> p = %.3f (50%% at about 77,000 ids)\n", CollisionProbability(1e5, 32))
	fmt.Println("  Snowflake: zero collision probability by construction, 4096 ids/ms/worker, 1024 workers, 69.7 years of 41-bit ms")

	fmt.Println()
	fmt.Println("== 6. Round trips ==")
	fmt.Printf("  Uuid.parse(toString) == original: %t\n", must(ParseUuid(a7.String())) == a7)
	fmt.Printf("  Ulid.parse(toString) == original: %t\n", must(ParseUlid(au.String())) == au)
	fmt.Printf("  Ulid lenient decode: parse accepts lowercase and maps I/L->1, O->0: %t\n", must(ParseUlid(strings.ToLower(au.String()))) == au)
}
