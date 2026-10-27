// Tests for phases/03-systems-data-structures/15-serialization-formats. Run: go test ./...
package main

import (
	"bytes"
	"encoding/hex"
	"errors"
	"math"
	"slices"
	"strings"
	"testing"
)

func hexBytes(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(strings.ReplaceAll(s, " ", ""))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func varint(v uint64) []byte {
	var s ByteSink
	WriteUnsigned(&s, v)
	return s.ToByteArray()
}

func readVarint(b []byte) (uint64, error) { return ReadUnsigned(NewByteSource(b)) }

func sizeSigned(v int64) int {
	var s ByteSink
	WriteSigned(&s, v)
	return s.Size()
}

func encoded(t *testing.T, e *Encoder) []byte {
	t.Helper()
	b, err := e.ToByteArray()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func wantMalformed(t *testing.T, name string, err error) {
	t.Helper()
	if !errors.Is(err, ErrMalformed) {
		t.Errorf("%s: got %v, want ErrMalformed", name, err)
	}
}

func TestVarintSpecVectors(t *testing.T) {
	cases := []struct {
		name  string
		value uint64
		hex   string
	}{
		{"varint 0 -> 00", 0, "00"},
		{"varint 1 -> 01", 1, "01"},
		{"varint 127 -> 7f (largest single byte)", 127, "7f"},
		{"varint 128 -> 80 01", 128, "80 01"},
		{"varint 150 -> 96 01 (protobuf.dev example)", 150, "96 01"},
		{"varint 300 -> ac 02 (protobuf.dev example)", 300, "ac 02"},
		{"varint -1 (all ones) is 10 bytes ending in 01", math.MaxUint64, "ff ff ff ff ff ff ff ff ff 01"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := varint(c.value); !bytes.Equal(got, hexBytes(t, c.hex)) {
				t.Errorf("got % x", got)
			}
		})
	}
	if len(varint(math.MaxInt64)) != 9 {
		t.Error("varint 2^63-1 is 9 bytes")
	}
	if EncodedSize(127) != 1 || EncodedSize(128) != 2 || EncodedSize(math.MaxUint64) != 10 {
		t.Error("encodedSize matches writer")
	}
	rng := NewJavaRandom(3)
	for i := 0; i < 10_000; i++ {
		v := uint64(rng.NextLong()) >> rng.NextIntN(64)
		if got, err := readVarint(varint(v)); err != nil || got != v {
			t.Fatalf("varint round-trips 10000 random values of all magnitudes: %d -> %d, %v", v, got, err)
		}
	}
	if got, err := readVarint(varint(math.MaxUint64)); err != nil || got != math.MaxUint64 {
		t.Error("varint read of -1 round-trips")
	}
	_, err := readVarint(hexBytes(t, "80"))
	wantMalformed(t, "varint rejects truncated input", err)
	_, err = readVarint(hexBytes(t, "80 80 80 80 80 80 80 80 80 80 80 00"))
	wantMalformed(t, "varint rejects 11 continuation bytes", err)
	_, err = readVarint(hexBytes(t, "ff ff ff ff ff ff ff ff ff 7f"))
	wantMalformed(t, "varint rejects a 10th byte that overflows 64 bits", err)

	// ZigZag against the specification's table
	zz := []struct {
		in  int64
		out uint64
	}{
		{0, 0}, {-1, 1}, {1, 2}, {-2, 3},
		{2_147_483_647, 4_294_967_294}, {-2_147_483_648, 4_294_967_295},
		{math.MinInt64, math.MaxUint64}, {math.MaxInt64, math.MaxUint64 - 1},
	}
	for _, c := range zz {
		if ZigzagEncode(c.in) != c.out {
			t.Errorf("zigzag %d -> %d, want %d", c.in, ZigzagEncode(c.in), c.out)
		}
	}
	for i := 0; i < 10_000; i++ {
		v := rng.NextLong()
		if ZigzagDecode(ZigzagEncode(v)) != v {
			t.Fatalf("zigzag round-trips 10000 random signed values: %d", v)
		}
	}
	var refund ByteSink
	WriteSigned(&refund, -1999)
	if !bytes.Equal(refund.ToByteArray(), hexBytes(t, "9d 1f")) {
		t.Error("sint64 -1999 is two bytes (9d 1f)")
	}
	if sizeSigned(-64) != 1 || sizeSigned(-65) != 2 || sizeSigned(63) != 1 || sizeSigned(64) != 2 {
		t.Error("sint64 -64 is one byte, -65 is two")
	}
}

func TestTagsAndWireTypes(t *testing.T) {
	if !bytes.Equal(encoded(t, NewEncoder().Uint64(1, 150)), hexBytes(t, "08 96 01")) {
		t.Error("field 1 uint64 150 -> 08 96 01")
	}
	if !bytes.Equal(encoded(t, NewEncoder().String(2, "testing")), hexBytes(t, "12 07 74 65 73 74 69 6e 67")) {
		t.Error("field 2 string testing -> 12 07 74 65 73 74 69 6e 67")
	}
	if !bytes.Equal(encoded(t, NewEncoder().Message(3, NewEncoder().Uint64(1, 150))), hexBytes(t, "1a 03 08 96 01")) {
		t.Error("field 3 nested message with field 1 = 150 -> 1a 03 08 96 01")
	}
	if !bytes.Equal(encoded(t, NewEncoder().Fixed64(4, 0x0102030405060708)), hexBytes(t, "21 08 07 06 05 04 03 02 01")) {
		t.Error("fixed64 is little-endian")
	}
	if len(encoded(t, NewEncoder().Uint64(16, 1))) != 3 || len(encoded(t, NewEncoder().Uint64(15, 1))) != 2 {
		t.Error("field number 16 needs a two-byte tag")
	}
	if _, err := NewEncoder().Uint64(0, 1).ToByteArray(); !errors.Is(err, ErrInvalidArgument) {
		t.Error("field number 0 is rejected on encode")
	}
	_, err := NewDecoder(hexBytes(t, "00 01")).Next()
	wantMalformed(t, "field number 0 is rejected on decode", err)
	_, err = NewDecoder(hexBytes(t, "0b")).Next()
	wantMalformed(t, "wire type 3 (start group) is rejected", err)

	d := NewDecoder(hexBytes(t, "08 96 01 12 07 74 65 73 74 69 6e 67"))
	more, _ := d.Next()
	v, _ := d.Uint64()
	ok := more && d.Field() == 1 && d.Type() == VARINT && v == 150
	more, _ = d.Next()
	s, _ := d.String()
	ok = ok && more && d.Field() == 2 && d.Type() == LENGTH_DELIMITED && s == "testing"
	more, _ = d.Next()
	if !ok || more {
		t.Error("decoder walks fields in order")
	}
	x := NewDecoder(hexBytes(t, "08 96 01"))
	_, _ = x.Next()
	_, err = x.String()
	wantMalformed(t, "reading a string where a varint sits is a type error", err)
}

// readKnownField walks a message, returning field 1 as uint64 and skipping everything else.
func readKnownField(t *testing.T, d *Decoder) uint64 {
	t.Helper()
	found := uint64(math.MaxUint64)
	for {
		more, err := d.Next()
		if err != nil {
			t.Fatal(err)
		}
		if !more {
			return found
		}
		if d.Field() == 1 {
			if found, err = d.Uint64(); err != nil {
				t.Fatal(err)
			}
		} else if err := d.Skip(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSkipUnknownFields(t *testing.T) {
	mixed := encoded(t, NewEncoder().Uint64(9, 300).Fixed64(10, 7).String(11, "skip me").Message(12, NewEncoder().Uint64(1, 1)).Uint64(1, 42))
	sk := NewDecoder(mixed)
	if readKnownField(t, sk) != 42 || sk.Skipped != 4 {
		t.Error("unknown fields of every wire type are skipped and the known field is still read")
	}
	f32 := NewDecoder(hexBytes(t, "2d 01 02 03 04 08 05")) // field 5 FIXED32 (tag 0x2d), then field 1 = 5
	if readKnownField(t, f32) != 5 {
		t.Error("fixed32 unknown field is skipped as 4 bytes")
	}
}

var sample = PostingV2{1_048_577, OfMinor(-1999, USD), "acct:merchant:4471", "acct:customer:9", "refund order 5512",
	&Audit{"svc-refunds", 1_760_000_000_000}, true}

func TestPostingRoundTripsAndEvolution(t *testing.T) {
	bin := sample.Encode()
	if back, err := DecodePostingV2(bin); err != nil || !back.Equal(sample) {
		t.Error("v2 round trip")
	}
	if len(bin) != 94 {
		t.Errorf("v2 encoding is 94 bytes: %d", len(bin))
	}
	v1View := NewDecoder(bin)
	asV1, err := DecodePostingV1From(v1View)
	if err != nil || asV1 != sample.AsV1() {
		t.Error("v1 reader on v2 bytes keeps the core fields")
	}
	if v1View.Skipped != 3 {
		t.Error("v1 reader skipped exactly the three new fields")
	}
	old := PostingV1{7, OfMinor(1250, USD), "acct:a", "acct:b"}
	upgraded, err := DecodePostingV2(old.Encode())
	if err != nil || upgraded.Memo != "" || upgraded.Audit != nil || upgraded.Reversal || upgraded.ID != 7 || upgraded.Amount != old.Amount {
		t.Error("v2 reader on v1 bytes gets defaults for new fields")
	}
	if !bytes.Equal(PostingV2{7, old.Amount, "acct:a", "acct:b", "", nil, false}.Encode(), old.Encode()) {
		t.Error("v2 with defaults encodes identically to v1")
	}
	if back, _ := DecodePostingV2(bin); back.Amount.MinorUnits != -1999 {
		t.Error("negative amount survives as sint64")
	}
	if yen, err := DecodePostingV1(PostingV1{1, OfMinor(1200, JPY), "a", "b"}.Encode()); err != nil || yen.Amount.String() != "1200 JPY" {
		t.Error("JPY zero-exponent currency survives")
	}
	if utf, err := DecodePostingV1(PostingV1{1, OfMinor(1, USD), "acct:éè€", "b"}.Encode()); err != nil || utf.DebitAccount != "acct:éè€" {
		t.Error("UTF-8 account names round-trip")
	}
	_, err = DecodePostingV1(encoded(t, NewEncoder().Uint64(1, 1)))
	wantMalformed(t, "missing currency is rejected", err)
	_, err = DecodePostingV2(bin[:len(bin)-3])
	wantMalformed(t, "truncated message is rejected", err)
	_, err = DecodePostingV2(encoded(t, NewEncoder().String(F_AMOUNT, "19.99").String(3, "USD")))
	wantMalformed(t, "changed field type is rejected", err)

	r2 := NewJavaRandom(11)
	for i := 0; i < 2_000; i++ {
		q := randomPosting(r2)
		back, err := DecodePostingV2(q.Encode())
		if err != nil || !back.Equal(q) {
			t.Fatalf("2000 random postings round-trip: failed at %d (%v)", i, err)
		}
	}
}

func TestJson(t *testing.T) {
	if JsonQuote("a\"b\\c\nd\u0001") != `"a\"b\\c\nd\u0001"` {
		t.Errorf("json escapes quotes, backslashes, and control characters: %s", JsonQuote("a\"b\\c\nd\u0001"))
	}
	if got := JsonPosting(PostingV2{5, OfMinor(-1, USD), "a", "b", "", nil, false}, true); got != `{"id":"5","amountMinor":"-1","currency":"USD","debitAccount":"a","creditAccount":"b"}` {
		t.Errorf("json numbers-as-strings quotes id and amount: %s", got)
	}
	if uint64(float64(uint64(9_007_199_254_740_993))) != 9_007_199_254_740_992 {
		t.Error("2^53 + 1 is not representable as a double")
	}
	if uint64(float64(uint64(9_007_199_254_740_992))) != 9_007_199_254_740_992 {
		t.Error("2^53 is representable as a double")
	}
	if len(sample.Encode()) >= len(JsonPosting(sample, false)) {
		t.Error("binary is smaller than json for the sample posting")
	}
}

func TestByteSourceSliceIsBounded(t *testing.T) {
	src := NewByteSource(hexBytes(t, "01 02 03 04"))
	view, err := src.Slice(2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := view.ReadBytes(3); !errors.Is(err, ErrMalformed) {
		t.Error("a nested view cannot read past its length prefix")
	}
	if src.Position() != 2 {
		t.Error("slicing advances the outer cursor")
	}
	if _, err := src.Slice(3); !errors.Is(err, ErrMalformed) {
		t.Error("a slice beyond the buffer is malformed")
	}
	if !slices.Equal(varint(300), []byte{0xac, 0x02}) {
		t.Error("ByteSink hands out a copy of its bytes")
	}
}
