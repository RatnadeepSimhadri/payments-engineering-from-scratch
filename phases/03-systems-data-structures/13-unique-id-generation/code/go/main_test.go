// Tests for phases/03-systems-data-structures/13-unique-id-generation. Run: go test ./...
package main

import (
	"errors"
	"math"
	"strings"
	"testing"
)

const base int64 = 1_760_000_000_000

func mustUlid(t *testing.T, ts, hi int64, lo uint64) Ulid {
	t.Helper()
	u, err := NewUlid(ts, hi, lo)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func nextUlid(t *testing.T, g *UlidGenerator) Ulid {
	t.Helper()
	u, err := g.Next()
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func nextFlake(t *testing.T, g *SnowflakeGenerator) int64 {
	t.Helper()
	id, err := g.Next()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestUuidTextForm(t *testing.T) {
	known := Uuid{0x0199c82cc00072c9, 0xbd984a5ce5b3f441}
	if known.String() != "0199c82c-c000-72c9-bd98-4a5ce5b3f441" {
		t.Errorf("uuid toString is 8-4-4-4-12 lowercase hex: %s", known)
	}
	if p, err := ParseUuid(known.String()); err != nil || p != known {
		t.Error("uuid parse round-trips")
	}
	if p, err := ParseUuid(strings.ToUpper(known.String())); err != nil || p != known {
		t.Error("uuid parse accepts uppercase")
	}
	if known.Version() != 7 || known.Variant() != 2 {
		t.Error("uuid version and variant nibbles")
	}
	if known.TimestampMillis() != 0x0199c82cc000 {
		t.Error("uuid v7 timestamp is the top 48 bits")
	}
	if (Uuid{}).String() != "00000000-0000-0000-0000-000000000000" {
		t.Error("uuid nil renders as all zeros")
	}
	if (Uuid{0x8000000000000000, 0}).Compare(Uuid{0x7fffffffffffffff, 0}) <= 0 {
		t.Error("uuid compare is unsigned")
	}
	if _, err := ParseUuid("0199c82cc00072c9bd984a5ce5b3f441"); !errors.Is(err, ErrInvalidArgument) {
		t.Error("uuid parse rejects bad shape")
	}
	if _, err := ParseUuid("0199c82c-c000-72c9-bd98-4a5ce5b3f4_1"); !errors.Is(err, ErrInvalidArgument) {
		t.Error("uuid parse rejects non-hex digits")
	}
}

func TestGeneratorsMatchJavaBits(t *testing.T) {
	// The demo's first ids, pinned from the Java run: same seed, same 128 bits.
	clock := NewManualClock(base)
	rng := NewJavaRandom(7)
	if got := NewUuidV4Generator(rng).Next().String(); got != "bb0f1798-a377-4418-bfc9-945a02770b39" {
		t.Errorf("v4 first id: %s", got)
	}
	if got := NewUuidV7Generator(clock, rng).Next().String(); got != "0199c82c-c000-72c9-bd98-4a5ce5b3f441" {
		t.Errorf("v7 first id: %s", got)
	}
	if got := nextUlid(t, NewUlidGenerator(clock, rng)).String(); got != "01K742SG00QRDBAJRS0J8V7JJX" {
		t.Errorf("ulid first id: %s", got)
	}
}

func TestUuidV4(t *testing.T) {
	v4 := NewUuidV4Generator(NewJavaRandom(1))
	seen := map[Uuid]bool{}
	bitsOk := true
	for i := 0; i < 10_000; i++ {
		u := v4.Next()
		bitsOk = bitsOk && u.Version() == 4 && u.Variant() == 2
		seen[u] = true
	}
	if !bitsOk {
		t.Error("v4: version 4 and variant 2 on every id")
	}
	if len(seen) != 10_000 {
		t.Error("v4: 10000 ids, no duplicates")
	}
	if NewUuidV4Generator(NewJavaRandom(1)).Next() != NewUuidV4Generator(NewJavaRandom(1)).Next() {
		t.Error("v4: seeded generator is reproducible")
	}
}

func TestUuidV7(t *testing.T) {
	c7 := NewManualClock(base)
	v7 := NewUuidV7Generator(c7, NewJavaRandom(2))
	first := v7.Next()
	if first.TimestampMillis() != base || first.Version() != 7 || first.Variant() != 2 {
		t.Error("v7: timestamp field equals the clock")
	}
	var burst []Uuid
	for i := 0; i < 5_000; i++ {
		burst = append(burst, v7.Next())
	}
	if !IsStrictlyIncreasing(burst, Uuid.Compare) {
		t.Error("v7: 5000 ids in a frozen millisecond are strictly increasing")
	}
	if burst[4_999].TimestampMillis() < base+1 {
		t.Error("v7: counter overflow borrows the next millisecond")
	}
	if burst[0].TimestampMillis() != burst[1].TimestampMillis() || burst[1].Msb&0xFFF != burst[0].Msb&0xFFF+1 {
		t.Error("v7: same-ms ids share the timestamp and differ in the counter")
	}
	c7.Advance(10)
	later := v7.Next()
	if later.TimestampMillis() != base+10 || later.Compare(burst[4_999]) <= 0 {
		t.Error("v7: clock advance resets to the wall clock")
	}
	c7.Set(base - 60_000)
	afterStepBack := v7.Next()
	if afterStepBack.TimestampMillis() != base+10 || afterStepBack.Compare(later) <= 0 {
		t.Error("v7: clock stepping back keeps the last timestamp and stays increasing")
	}
	if later.String() <= burst[0].String() {
		t.Error("v7: string order equals bit order")
	}
}

func TestUlidEncoding(t *testing.T) {
	if u, err := ParseUlid("01ARZ3NDEKTSV4RRFFQ69G5FAV"); err != nil || u.TimestampMillis != 1_469_922_850_259 {
		t.Error("ulid: spec timestamp vector 01ARZ3NDEK decodes to 1469922850259")
	}
	if !strings.HasPrefix(mustUlid(t, 1<<48-1, 0, 0).String(), "7ZZZZZZZZZ") {
		t.Error("ulid: max timestamp encodes as 7ZZZZZZZZZ")
	}
	if mustUlid(t, 0, 0, 0).String() != "00000000000000000000000000" {
		t.Error("ulid: zero renders as 26 zeros")
	}
	vec := mustUlid(t, 1_469_922_850_259, 0xABCD, 0x0123456789ABCDEF)
	if p, err := ParseUlid(vec.String()); err != nil || p != vec {
		t.Error("ulid: parse(toString) round-trips the 80 random bits")
	}
	lenient := strings.NewReplacer("1", "l", "0", "o").Replace(strings.ToLower(vec.String()))
	if p, err := ParseUlid(lenient); err != nil || p != vec {
		t.Error("ulid: lowercase and I/L/O are accepted on decode")
	}
	s := vec.String()
	allInAlphabet := len(s) == 26
	for _, ch := range s {
		allInAlphabet = allInAlphabet && strings.ContainsRune(UlidAlphabet, ch)
	}
	if !allInAlphabet {
		t.Error("ulid: always 26 characters from the alphabet")
	}
	for _, bad := range []struct{ name, text string }{
		{"ulid: rejects U", "01ARZ3NDEKTSV4RRFFQ69G5FAU"},
		{"ulid: rejects timestamp overflow 8ZZZ...", "8ZZZZZZZZZZZZZZZZZZZZZZZZZ"},
		{"ulid: rejects wrong length", "01ARZ3NDEK"},
	} {
		if _, err := ParseUlid(bad.text); !errors.Is(err, ErrInvalidArgument) {
			t.Error(bad.name)
		}
	}
}

func TestUlidGenerator(t *testing.T) {
	cu := NewManualClock(base)
	ug := NewUlidGenerator(cu, NewJavaRandom(3))
	var uburst []Ulid
	var ustr []string
	for i := 0; i < 3_000; i++ {
		u := nextUlid(t, ug)
		uburst = append(uburst, u)
		ustr = append(ustr, u.String())
	}
	if !IsStrictlyIncreasing(uburst, Ulid.Compare) || uburst[1].RandomLo != uburst[0].RandomLo+1 {
		t.Error("ulid: monotonic in a frozen ms (random part + 1)")
	}
	if !IsLexicallyIncreasing(ustr) {
		t.Error("ulid: string order equals generation order")
	}
	carry := NewUlidGenerator(NewManualClock(base), NewJavaRandom(4))
	carry.Resume(mustUlid(t, base, 5, math.MaxUint64)) // Java: new Ulid(base, 5, -1L)
	carried := nextUlid(t, carry)
	if carried.RandomHi != 6 || carried.RandomLo != 0 {
		t.Error("ulid: increment carries from the low word into the high word")
	}
	full := NewUlidGenerator(NewManualClock(base), NewJavaRandom(5))
	full.Resume(mustUlid(t, base, 0xFFFF, math.MaxUint64))
	if _, err := full.Next(); !errors.Is(err, ErrIllegalState) {
		t.Error("ulid: randomness overflow in one ms is an error")
	}
	resumed := NewUlidGenerator(NewManualClock(base+1), NewJavaRandom(6))
	resumed.Resume(mustUlid(t, base, 0xFFFF, math.MaxUint64))
	if nextUlid(t, resumed).TimestampMillis != base+1 {
		t.Error("ulid: a later clock after resume starts a fresh random part")
	}
}

func TestSnowflake(t *testing.T) {
	cs := NewManualClock(base)
	sf, err := NewSnowflakeGenerator(cs, 17, 5)
	if err != nil {
		t.Fatal(err)
	}
	id0 := nextFlake(t, sf)
	if TimestampMillisOf(id0) != base || WorkerIdOf(id0) != 17 || SequenceOf(id0) != 0 {
		t.Error("snowflake: fields decode")
	}
	if id0 <= 0 {
		t.Error("snowflake: positive int64")
	}
	var fburst []int64
	for i := 0; i < 5_000; i++ {
		fburst = append(fburst, nextFlake(t, sf))
	}
	if !IsStrictlyIncreasing(fburst, cmpInt64) {
		t.Error("snowflake: 5000 ids in one ms strictly increasing")
	}
	if sf.BorrowedMillis != 1 || TimestampMillisOf(fburst[4_999]) != base+1 {
		t.Error("snowflake: sequence wraps after 4096 and borrows one ms")
	}
	cs.Advance(1)
	sameAsBorrowed := nextFlake(t, sf)
	if TimestampMillisOf(sameAsBorrowed) != base+1 || sameAsBorrowed <= fburst[4_999] {
		t.Error("snowflake: wall clock catching up to borrowed time continues the sequence")
	}
	cs.Set(base - 2)
	smallDrift := nextFlake(t, sf)
	if smallDrift <= sameAsBorrowed || TimestampMillisOf(smallDrift) != base+1 {
		t.Error("snowflake: small backwards drift keeps issuing on the last timestamp")
	}
	cs.Set(base - 10_000)
	_, err = sf.Next()
	var moved *ClockMovedBackwardsError
	if !errors.As(err, &moved) || !errors.Is(err, ErrClockMovedBackwards) || moved.ByMillis < 10_000 {
		t.Errorf("snowflake: large backwards drift refuses to issue: %v", err)
	}
	if _, err := NewSnowflakeGenerator(cs, 1024, 5); !errors.Is(err, ErrInvalidArgument) {
		t.Error("snowflake: worker id out of range")
	}
	w1, _ := NewSnowflakeGenerator(NewManualClock(base), 1, 5)
	w2, _ := NewSnowflakeGenerator(NewManualClock(base), 2, 5)
	if nextFlake(t, w1) == nextFlake(t, w2) {
		t.Error("snowflake: two workers in the same ms never collide")
	}
	laterMs, _ := NewSnowflakeGenerator(NewManualClock(base+1), 0, 5)
	earlierMs, _ := NewSnowflakeGenerator(NewManualClock(base), 1023, 5)
	if nextFlake(t, laterMs) <= nextFlake(t, earlierMs) {
		t.Error("snowflake: ids from a later ms sort after any id from an earlier ms regardless of worker")
	}
}

func TestCollisionMath(t *testing.T) {
	if math.Abs(CollisionProbability(77_163, 32)-0.5) >= 0.01 {
		t.Error("birthday: 32 bits, 77163 ids is about 50%")
	}
	if CollisionProbability(1e12, 122) >= 1e-12 {
		t.Error("birthday: 122 bits, 1e12 ids is negligible")
	}
	if math.Abs(IdsForCollisionProbability(0.5, 32)-77_163) >= 10 {
		t.Error("birthday: ids for 50% at 32 bits is about 77163")
	}
	if CollisionProbability(1e6, 62) >= CollisionProbability(1e7, 62) {
		t.Error("birthday: probability is monotone in n")
	}
}
