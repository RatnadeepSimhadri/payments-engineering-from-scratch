// Tests for phases/01-complexity-and-core-structures/11-bit-manipulation. Run: go test ./...
package main

import (
	"errors"
	"math"
	"math/bits"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func check(t *testing.T, name string, ok bool) {
	t.Helper()
	if !ok {
		t.Error(name)
	}
}

func mustPanic(t *testing.T, name string, f func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Errorf("%s: expected panic", name)
		}
	}()
	f()
}

// javaRandom is java.util.Random's 48-bit LCG with nextInt, nextLong, and nextBoolean.
type javaRandom struct{ seed int64 }

func newJavaRandom(seed int64) *javaRandom { return &javaRandom{(seed ^ 0x5DEECE66D) & (1<<48 - 1)} }

func (r *javaRandom) next(bitCount uint) int32 {
	r.seed = (r.seed*0x5DEECE66D + 0xB) & (1<<48 - 1)
	return int32(r.seed >> (48 - bitCount))
}

func (r *javaRandom) nextInt(bound int32) int32 {
	rr := r.next(31)
	m := bound - 1
	if bound&m == 0 {
		return int32((int64(bound) * int64(rr)) >> 31)
	}
	u := rr
	rr = u % bound
	for u-rr+m < 0 {
		u = r.next(31)
		rr = u % bound
	}
	return rr
}

func (r *javaRandom) nextLong() int64   { return int64(r.next(32))<<32 + int64(r.next(32)) }
func (r *javaRandom) nextBoolean() bool { return r.next(1) != 0 }

func ins(t *testing.T, v int64, shift, width int, field int64) int64 {
	t.Helper()
	out, err := Insert(v, shift, width, field)
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	return out
}

func ext(t *testing.T, v int64, shift, width int) int64 {
	t.Helper()
	out, err := Extract(v, shift, width)
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	return out
}

func TestMasksAndFields(t *testing.T) {
	check(t, "mask(0)", Mask(0) == 0)
	check(t, "mask(3)", Mask(3) == 0b111)
	check(t, "mask(63)", Mask(63) == math.MaxInt64)
	check(t, "mask(64) is all ones", Mask(64) == -1)
	mustPanic(t, "mask(65) rejected", func() { Mask(65) })
	check(t, "set/clear/toggle", Set(0, 5) == 32 && Clear(32, 5) == 0 && Toggle(Toggle(7, 1), 1) == 7)
	check(t, "isSet on sign bit uses unsigned shift", IsSet(math.MinInt64, 63) && !IsSet(math.MinInt64, 62))
	check(t, "set bit 63 gives negative int64", Set(0, 63) == math.MinInt64)

	check(t, "extract middle field", ext(t, 0b1011_0100, 4, 3) == 0b011)
	check(t, "extract top field of negative value is unsigned", ext(t, -1, 60, 4) == 15)
	check(t, "insert replaces only the field", ins(t, 0b1011_0100, 4, 3, 0b110) == 0b1110_0100)
	check(t, "insert then extract round-trips", ext(t, ins(t, 0, 22, 41, 123_456_789), 22, 41) == 123_456_789)
	_, err := Insert(0, 0, 3, 8)
	check(t, "insert rejects a field that does not fit", errors.Is(err, ErrFieldOverflow))
	_, err = Extract(0, 60, 5)
	check(t, "field beyond bit 63 rejected", errors.Is(err, ErrBadField))
}

func TestPopcountAndPowers(t *testing.T) {
	rnd := newJavaRandom(5)
	popAgree := true
	for i := 0; i < 10_000 && popAgree; i++ {
		v := rnd.nextLong()
		expected := bits.OnesCount64(uint64(v))
		popAgree = PopcountKernighan(v) == expected && PopcountParallel(v) == expected
	}
	check(t, "popcount implementations agree on 10,000 random int64s", popAgree)
	check(t, "popcount boundaries", PopcountParallel(0) == 0 && PopcountParallel(-1) == 64 && PopcountKernighan(math.MinInt64) == 1)
	check(t, "lowestSetBit", LowestSetBit(0b1011_0100) == 0b100 && LowestSetBit(0) == 0 && LowestSetBit(math.MinInt64) == math.MinInt64)
	check(t, "isPowerOfTwo", IsPowerOfTwo(1) && IsPowerOfTwo(1024) && !IsPowerOfTwo(0) && !IsPowerOfTwo(1000) && !IsPowerOfTwo(math.MinInt64))
	r1, _ := RoundUpToPowerOfTwo(1)
	r1000, _ := RoundUpToPowerOfTwo(1000)
	r1024, _ := RoundUpToPowerOfTwo(1024)
	r1025, _ := RoundUpToPowerOfTwo(1025)
	check(t, "roundUpToPowerOfTwo", r1 == 1 && r1000 == 1024 && r1024 == 1024 && r1025 == 2048)
	_, err := RoundUpToPowerOfTwo(0)
	check(t, "roundUpToPowerOfTwo rejects 0", err != nil)
	check(t, "toBinary/toHex fixed width", ToBinary(5, 8) == "00000101" && ToHex(255, 4) == "00FF" && ToHex(-1, 16) == "FFFFFFFFFFFFFFFF")
}

func TestAccountFlags(t *testing.T) {
	f := NoFlags().With(KycVerified).With(PayoutsEnabled)
	check(t, "flags set and tested", f.Has(KycVerified) && f.Has(KycVerified|PayoutsEnabled) && !f.Has(Frozen))
	check(t, "payout allowed when verified and enabled", f.CanPayOut())
	check(t, "hold blocks payout", !f.With(ChargebackHold).CanPayOut())
	check(t, "frozen blocks payout", !f.With(Frozen).CanPayOut())
	check(t, "without removes exactly one flag", f.With(Frozen).Without(Frozen).Bits() == f.Bits())
	check(t, "without an absent flag is a no-op", f.Without(HighRisk).Bits() == f.Bits())
	check(t, "flags toString", f.String() == "{KYC_VERIFIED, PAYOUTS_ENABLED}" && NoFlags().String() == "{}")
	_, err := NewAccountFlags(1 << 7)
	check(t, "unknown flag bit rejected", errors.Is(err, ErrUnknownFlag))
}

// refBitSet is the brute-force reference standing in for java.util.BitSet.
type refBitSet []bool

func (r refBitSet) cardinality() int {
	n := 0
	for _, b := range r {
		if b {
			n++
		}
	}
	return n
}

func (r refBitSet) nextSetBit(from int) int {
	for i := from; i < len(r); i++ {
		if r[i] {
			return i
		}
	}
	return -1
}

func TestBitmap(t *testing.T) {
	rnd := newJavaRandom(5)
	for i := 0; i < 10_000; i++ { // consume the popcount test's draws so the bitmap ops match Java's stream
		rnd.nextLong()
	}
	bm := NewBitmap(200)
	ref := make(refBitSet, 200)
	for i := 0; i < 2_000; i++ {
		idx := int(rnd.nextInt(200))
		if rnd.nextBoolean() {
			bm.Set(idx)
			ref[idx] = true
		} else {
			bm.Clear(idx)
			ref[idx] = false
		}
	}
	bitmapAgree := bm.Cardinality() == ref.cardinality()
	for i := 0; i < 200 && bitmapAgree; i++ {
		bitmapAgree = bm.Get(i) == ref[i] && bm.NextSetBit(i) == ref.nextSetBit(i)
	}
	check(t, "bitmap agrees with the reference bit set after 2,000 random ops", bitmapAgree)
	check(t, "bitmap word allocation rounds up", NewBitmap(0).WordsAllocated() == 0 && NewBitmap(1).WordsAllocated() == 1 && NewBitmap(64).WordsAllocated() == 1 && NewBitmap(65).WordsAllocated() == 2)
	sparse := NewBitmap(1_000)
	sparse.Set(999)
	check(t, "nextSetBit skips zero words", sparse.NextSetBit(0) == 999 && sparse.NextSetBit(999) == 999 && sparse.NextSetBit(1_000) == -1)
	check(t, "nextSetBit on empty is -1", NewBitmap(1_000).NextSetBit(0) == -1)
	a, b := NewBitmap(130), NewBitmap(130)
	a.Set(0)
	a.Set(129)
	b.Set(129)
	b.Set(64)
	and, _ := a.And(b)
	or, _ := a.Or(b)
	check(t, "and/or across word boundaries", and.Cardinality() == 1 && and.Get(129) && or.Cardinality() == 3)
	mustPanic(t, "bitmap index out of range", func() { bm.Set(200) })
	_, err := a.And(bm)
	check(t, "bitmap and with different capacity", errors.Is(err, ErrCapacityMismatch))
}

func TestIso8583Bitmap(t *testing.T) {
	fields := []int{2, 3, 4, 7, 11, 12, 13, 22, 24, 25, 35, 41, 42, 49}
	hex, err := Iso8583Encode(fields)
	check(t, "primary bitmap is 16 hex chars", err == nil && len(hex) == 16)
	check(t, "field 2 is bit 62, so the first hex digit is 7 for fields 2,3,4", strings.HasPrefix(hex, "7"))
	check(t, "known vector: fields 2,3,4,7,11,12,13,22,24,25,35,41,42,49", hex == "7238058020C08000")
	decoded, _ := Iso8583Decode(hex)
	check(t, "decode inverts encode", reflect.DeepEqual(decoded, fields))
	withSecondary := append(append([]int{}, fields...), 102)
	sort.Ints(withSecondary)
	both, _ := Iso8583Encode(withSecondary)
	check(t, "secondary bitmap doubles the length and sets field 1", len(both) == 32 && strings.HasPrefix(both, "F"))
	check(t, "field 102 is bit 26 of the secondary bitmap", both[16:] == "0000000004000000")
	decodedBoth, _ := Iso8583Decode(both)
	check(t, "decode with secondary", decodedBoth[len(decodedBoth)-1] == 102 && len(decodedBoth) == 15)
	f64, _ := Iso8583Encode([]int{64})
	check(t, "field 64 is the lowest primary bit", f64 == "0000000000000001")
	_, err = Iso8583Encode([]int{1})
	check(t, "field 1 cannot be requested directly", errors.Is(err, ErrBadIsoField))
	_, err = Iso8583Encode([]int{129})
	check(t, "field 129 out of range", errors.Is(err, ErrBadIsoField))
	_, err = Iso8583Decode("F000000000000000")
	check(t, "decode rejects inconsistent length", errors.Is(err, ErrBadBitmapHex))
	_, err = Iso8583Decode("7234")
	check(t, "decode rejects odd length", errors.Is(err, ErrBadBitmapHex))
}

func TestSnowflake(t *testing.T) {
	ts := EpochMillis + 123_456_789
	id, err := Compose(ts, 7, 42)
	if err != nil {
		t.Fatal(err)
	}
	parts, _ := Decompose(id)
	check(t, "compose/decompose round-trip", parts.TimestampMillis == ts && parts.Worker == 7 && parts.Sequence == 42)
	check(t, "id is positive", id > 0)
	later, _ := Compose(ts+1, 0, 0)
	earlier, _ := Compose(ts, 1023, 4095)
	check(t, "later timestamp gives larger id regardless of sequence", later > earlier)
	maxID, _ := Compose(EpochMillis+MaxTimestamp, 1023, 4095)
	check(t, "max fields fit", maxID == math.MaxInt64)
	_, err = Compose(EpochMillis-1, 0, 0)
	check(t, "timestamp before epoch rejected", errors.Is(err, ErrTimestampWindow))
	_, err = Compose(ts, 1024, 0)
	check(t, "worker 1024 does not fit", errors.Is(err, ErrFieldOverflow))
	_, err = Decompose(-1)
	check(t, "negative id rejected", errors.Is(err, ErrNegativeID))

	now := EpochMillis + 1_000
	gen, _ := NewGenerator(3, func() int64 { return now })
	first, _ := gen.Next()
	second, _ := gen.Next()
	p1, _ := Decompose(first)
	p2, _ := Decompose(second)
	check(t, "same millisecond increments sequence", p1.Sequence == 0 && p2.Sequence == 1 && second > first)
	now++
	third, _ := gen.Next()
	p3, _ := Decompose(third)
	check(t, "new millisecond resets sequence", p3.Sequence == 0)
	now -= 5
	_, err = gen.Next()
	check(t, "clock moving backwards is refused", errors.Is(err, ErrClockBackwards))
	_, err = NewGenerator(1024, func() int64 { return now })
	check(t, "worker id out of range rejected", err != nil)

	reads := 0
	slow := EpochMillis + 5_000
	busy, _ := NewGenerator(1, func() int64 {
		reads++
		if reads%10_000 == 0 {
			slow++
		}
		return slow
	})
	prev := int64(-1)
	increasing := true
	for i := 0; i < 9_000; i++ {
		x, err := busy.Next()
		if err != nil || x <= prev {
			increasing = false
		}
		prev = x
	}
	check(t, "sequence overflow waits for the next millisecond and stays unique", increasing && busy.Waits() >= 1)
}

func TestTwosComplementTraps(t *testing.T) {
	minInt := int64(math.MinInt64)
	check(t, "-MinInt64 is still negative", -minInt < 0)
	raw := 0x80
	check(t, "int8(0x80) widens to -128 unless masked", int8(raw) == -128 && uint8(int8(raw)) == 128)
	minusEight := int32(-8)
	check(t, "arithmetic vs logical shift", minusEight>>1 == -4 && uint32(minusEight)>>1 == 2_147_483_644)
	one := int32(1)
	shift := uint(32)
	check(t, "int32 shift past the width is zero in Go (Java reduces mod 32)", one<<shift == 0 && one<<31 < 0)
	lo, hi := int32(2_000_000_000), int32(2_100_000_000)
	check(t, "overflow-safe midpoint", lo+int32(uint32(hi-lo)>>1) == 2_050_000_000 && (lo+hi)/2 < 0)
	seven := int32(-7)
	check(t, "is-odd on negatives uses & not %", seven%2 == -1 && seven&1 == 1)
	check(t, "division truncates, shift floors", seven/2 == -3 && seven>>1 == -4)
}
