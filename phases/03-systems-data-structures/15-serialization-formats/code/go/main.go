// Lesson: phases/03-systems-data-structures/15-serialization-formats/docs/en.md
// Serialization: JSON, Protobuf-style Binary, and Schema Evolution
// Run:  go run .
// Test: go test ./...
// Sources: Protocol Buffers encoding specification (protobuf.dev/programming-guides/encoding): varints, ZigZag, tags,
// wire types, unknown-field handling; RFC 8259 (JSON); IEEE 754 for the 2^53 integer limit. Go 1.22+, standard library only.
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
	// ErrMalformed is the Java MalformedMessageException: truncated, overlong, mistyped, or otherwise undecodable input.
	ErrMalformed = errors.New("malformed message")
)

func malformed(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrMalformed, fmt.Sprintf(format, args...))
}

// ---------------------------------------------------------------- Currency and Money ----

// Currency is an ISO 4217 currency: code and minor-unit exponent. Copied from Phase 10, Lesson 01.
type Currency struct {
	Code     string
	Exponent int
}

var (
	USD = Currency{"USD", 2}
	JPY = Currency{"JPY", 0}
)

func NewCurrency(code string, exponent int) (Currency, error) {
	if len(code) != 3 {
		return Currency{}, fmt.Errorf("%w: currency code must be three letters", ErrInvalidArgument)
	}
	if exponent < 0 || exponent > 4 {
		return Currency{}, fmt.Errorf("%w: exponent must be 0..4", ErrInvalidArgument)
	}
	return Currency{code, exponent}, nil
}

func CurrencyOf(code string) Currency {
	switch code {
	case "USD":
		return USD
	case "JPY":
		return JPY
	default:
		return Currency{code, 2}
	}
}

// Money is integer minor units plus a currency. On the wire: sint64 minor units + a 3-byte currency string. Never a float.
type Money struct {
	MinorUnits int64
	Currency   Currency
}

func OfMinor(minorUnits int64, currency Currency) Money { return Money{minorUnits, currency} }

func (m Money) String() string {
	scale := int64(1)
	for i := 0; i < m.Currency.Exponent; i++ {
		scale *= 10
	}
	abs, sign := m.MinorUnits, ""
	if m.MinorUnits < 0 {
		abs, sign = -abs, "-"
	}
	whole := strconv.FormatInt(abs/scale, 10)
	if m.Currency.Exponent == 0 {
		return sign + whole + " " + m.Currency.Code
	}
	frac := strconv.FormatInt(abs%scale, 10)
	for len(frac) < m.Currency.Exponent {
		frac = "0" + frac
	}
	return sign + whole + "." + frac + " " + m.Currency.Code
}

// ---------------------------------------------------------------- ByteSink and ByteSource ----

// ByteSink is a growable byte buffer for encoding.
type ByteSink struct{ buf []byte }

func (s *ByteSink) Write(b byte)            { s.buf = append(s.buf, b) }
func (s *ByteSink) WriteBytes(bytes []byte) { s.buf = append(s.buf, bytes...) }
func (s *ByteSink) Size() int               { return len(s.buf) }
func (s *ByteSink) ToByteArray() []byte     { return slices.Clone(s.buf) }

// ByteSource is a cursor over an encoded buffer for decoding. Every read checks bounds: truncated input is an error, never a guess.
type ByteSource struct {
	buf   []byte
	pos   int
	limit int
}

func NewByteSource(buf []byte) *ByteSource { return &ByteSource{buf: buf, limit: len(buf)} }

func (s *ByteSource) HasMore() bool { return s.pos < s.limit }
func (s *ByteSource) Position() int { return s.pos }

func (s *ByteSource) ReadByte() (byte, error) {
	if s.pos >= s.limit {
		return 0, malformed("truncated: wanted a byte at offset %d", s.pos)
	}
	b := s.buf[s.pos]
	s.pos++
	return b, nil
}

func (s *ByteSource) ReadBytes(n int) ([]byte, error) {
	if n < 0 || s.pos+n > s.limit {
		return nil, malformed("truncated: wanted %d bytes at offset %d", n, s.pos)
	}
	out := slices.Clone(s.buf[s.pos : s.pos+n])
	s.pos += n
	return out, nil
}

// Slice is a view over the next n bytes, for nested messages, without copying.
func (s *ByteSource) Slice(n int) (*ByteSource, error) {
	if n < 0 || s.pos+n > s.limit {
		return nil, malformed("truncated: nested message of %d bytes at offset %d", n, s.pos)
	}
	view := &ByteSource{buf: s.buf, pos: s.pos, limit: s.pos + n}
	s.pos += n
	return view, nil
}

// ---------------------------------------------------------------- Varint ----

// Varint and ZigZag: the two primitives everything else is built from. Unsigned values are uint64, which is what
// Java's long means here when read with >>>; signed values are int64.
const VarintMaxBytes = 10

// WriteUnsigned is little-endian base-128: 7 payload bits per byte, high bit set on every byte but the last.
func WriteUnsigned(out *ByteSink, value uint64) {
	for value&^0x7F != 0 {
		out.Write(byte(value&0x7F | 0x80))
		value >>= 7
	}
	out.Write(byte(value))
}

func ReadUnsigned(in *ByteSource) (uint64, error) {
	var result uint64
	for shift := 0; shift < 64; shift += 7 {
		b, err := in.ReadByte()
		if err != nil {
			return 0, err
		}
		result |= uint64(b&0x7F) << shift
		if b&0x80 == 0 {
			if shift == 63 && b&0x7E != 0 {
				return 0, malformed("varint overflows 64 bits")
			}
			return result, nil
		}
	}
	return 0, malformed("varint longer than 10 bytes")
}

// ZigzagEncode maps signed to unsigned so small negatives stay small: 0,-1,1,-2,2 -> 0,1,2,3,4.
func ZigzagEncode(n int64) uint64 { return uint64((n << 1) ^ (n >> 63)) }

func ZigzagDecode(n uint64) int64 { return int64(n>>1) ^ -int64(n&1) }

func WriteSigned(out *ByteSink, value int64) { WriteUnsigned(out, ZigzagEncode(value)) }

func ReadSigned(in *ByteSource) (int64, error) {
	u, err := ReadUnsigned(in)
	return ZigzagDecode(u), err
}

func EncodedSize(unsignedValue uint64) int {
	n := 1
	for unsignedValue&^0x7F != 0 {
		unsignedValue >>= 7
		n++
	}
	return n
}

// ---------------------------------------------------------------- WireType ----

// WireType: the protobuf wire types. The decoder needs only this to skip a field it does not know.
type WireType int

const (
	VARINT           WireType = 0
	FIXED64          WireType = 1
	LENGTH_DELIMITED WireType = 2
	FIXED32          WireType = 5
)

func (w WireType) String() string {
	switch w {
	case VARINT:
		return "VARINT"
	case FIXED64:
		return "FIXED64"
	case LENGTH_DELIMITED:
		return "LENGTH_DELIMITED"
	case FIXED32:
		return "FIXED32"
	}
	return fmt.Sprintf("WireType(%d)", int(w))
}

func WireTypeOf(code int) (WireType, error) {
	switch WireType(code) {
	case VARINT, FIXED64, LENGTH_DELIMITED, FIXED32:
		return WireType(code), nil
	}
	return 0, malformed("unknown wire type %d (groups are not supported)", code)
}

// ---------------------------------------------------------------- Encoder ----

// Encoder writes tag-prefixed fields. A tag is varint((fieldNumber << 3) | wireType).
// The methods chain like the Java builder; the first invalid field number is remembered and reported by ToByteArray.
type Encoder struct {
	out ByteSink
	err error
}

func NewEncoder() *Encoder { return &Encoder{} }

func (e *Encoder) tag(field int, t WireType) bool {
	if e.err != nil {
		return false
	}
	if field < 1 || field > 536_870_911 {
		e.err = fmt.Errorf("%w: field number out of range: %d", ErrInvalidArgument, field)
		return false
	}
	WriteUnsigned(&e.out, uint64(field)<<3|uint64(t))
	return true
}

func (e *Encoder) Uint64(field int, value uint64) *Encoder {
	if e.tag(field, VARINT) {
		WriteUnsigned(&e.out, value)
	}
	return e
}

func (e *Encoder) Sint64(field int, value int64) *Encoder {
	if e.tag(field, VARINT) {
		WriteSigned(&e.out, value)
	}
	return e
}

func (e *Encoder) Bool(field int, value bool) *Encoder {
	if value {
		return e.Uint64(field, 1)
	}
	return e.Uint64(field, 0)
}

func (e *Encoder) Fixed64(field int, value int64) *Encoder {
	if e.tag(field, FIXED64) {
		for i := 0; i < 8; i++ {
			e.out.Write(byte(uint64(value) >> (8 * i))) // little-endian
		}
	}
	return e
}

func (e *Encoder) Bytes(field int, value []byte) *Encoder {
	if e.tag(field, LENGTH_DELIMITED) {
		WriteUnsigned(&e.out, uint64(len(value)))
		e.out.WriteBytes(value)
	}
	return e
}

func (e *Encoder) String(field int, value string) *Encoder { return e.Bytes(field, []byte(value)) }

// Message: a nested message is just bytes: encode it separately, then write it length-delimited.
func (e *Encoder) Message(field int, nested *Encoder) *Encoder {
	inner, err := nested.ToByteArray()
	if err != nil {
		if e.err == nil {
			e.err = err
		}
		return e
	}
	return e.Bytes(field, inner)
}

func (e *Encoder) ToByteArray() ([]byte, error) {
	if e.err != nil {
		return nil, e.err
	}
	return e.out.ToByteArray(), nil
}

// ---------------------------------------------------------------- Decoder ----

// Decoder reads tag-prefixed fields in order. Unknown fields are skipped by wire type; that is what makes evolution work.
type Decoder struct {
	in      *ByteSource
	field   int
	typ     WireType
	Skipped int
}

func NewDecoder(bytes []byte) *Decoder { return &Decoder{in: NewByteSource(bytes)} }

func NewDecoderFrom(source *ByteSource) *Decoder { return &Decoder{in: source} }

// Next advances to the next field. Returns false at end of message.
func (d *Decoder) Next() (bool, error) {
	if !d.in.HasMore() {
		return false, nil
	}
	tag, err := ReadUnsigned(d.in)
	if err != nil {
		return false, err
	}
	d.field = int(tag >> 3)
	if d.field == 0 {
		return false, malformed("field number 0 is reserved")
	}
	if d.typ, err = WireTypeOf(int(tag & 0x7)); err != nil {
		return false, err
	}
	return true, nil
}

func (d *Decoder) Field() int     { return d.field }
func (d *Decoder) Type() WireType { return d.typ }

func (d *Decoder) expect(expected WireType) error {
	if d.typ != expected {
		return malformed("field %d: expected %s but found %s", d.field, expected, d.typ)
	}
	return nil
}

func (d *Decoder) Uint64() (uint64, error) {
	if err := d.expect(VARINT); err != nil {
		return 0, err
	}
	return ReadUnsigned(d.in)
}

func (d *Decoder) Sint64() (int64, error) {
	if err := d.expect(VARINT); err != nil {
		return 0, err
	}
	return ReadSigned(d.in)
}

func (d *Decoder) Bool() (bool, error) {
	v, err := d.Uint64()
	return v != 0, err
}

func (d *Decoder) Fixed64() (int64, error) {
	if err := d.expect(FIXED64); err != nil {
		return 0, err
	}
	var v uint64
	for i := 0; i < 8; i++ {
		b, err := d.in.ReadByte()
		if err != nil {
			return 0, err
		}
		v |= uint64(b) << (8 * i)
	}
	return int64(v), nil
}

func (d *Decoder) Bytes() ([]byte, error) {
	if err := d.expect(LENGTH_DELIMITED); err != nil {
		return nil, err
	}
	n, err := ReadUnsigned(d.in)
	if err != nil {
		return nil, err
	}
	return d.in.ReadBytes(int(n))
}

// String reads a length-delimited UTF-8 field. (Not a fmt.Stringer: it mirrors the Java Decoder.string().)
func (d *Decoder) String() (string, error) {
	b, err := d.Bytes()
	return string(b), err
}

func (d *Decoder) Message() (*Decoder, error) {
	if err := d.expect(LENGTH_DELIMITED); err != nil {
		return nil, err
	}
	n, err := ReadUnsigned(d.in)
	if err != nil {
		return nil, err
	}
	view, err := d.in.Slice(int(n))
	if err != nil {
		return nil, err
	}
	return NewDecoderFrom(view), nil
}

// Skip the current field's value using only its wire type.
func (d *Decoder) Skip() error {
	d.Skipped++
	switch d.typ {
	case VARINT:
		_, err := ReadUnsigned(d.in)
		return err
	case FIXED64:
		_, err := d.in.ReadBytes(8)
		return err
	case FIXED32:
		_, err := d.in.ReadBytes(4)
		return err
	default: // LENGTH_DELIMITED
		n, err := ReadUnsigned(d.in)
		if err != nil {
			return err
		}
		_, err = d.in.ReadBytes(int(n))
		return err
	}
}

// ---------------------------------------------------------------- PostingV1 ----

// PostingV1 is schema version 1 of a ledger posting:
//
//	1: id (uint64)   2: amount_minor (sint64)   3: currency (string)   4: debit_account (string)   5: credit_account (string)
type PostingV1 struct {
	ID            uint64
	Amount        Money
	DebitAccount  string
	CreditAccount string
}

const (
	F_ID       = 1
	F_AMOUNT   = 2
	F_CURRENCY = 3
	F_DEBIT    = 4
	F_CREDIT   = 5
)

func (p PostingV1) Encode() []byte {
	b, _ := NewEncoder(). // constant field numbers 1..5 cannot be rejected
				Uint64(F_ID, p.ID).
				Sint64(F_AMOUNT, p.Amount.MinorUnits).
				String(F_CURRENCY, p.Amount.Currency.Code).
				String(F_DEBIT, p.DebitAccount).
				String(F_CREDIT, p.CreditAccount).
				ToByteArray()
	return b
}

func (p PostingV1) String() string {
	return fmt.Sprintf("PostingV1[id=%d, amount=%s, debitAccount=%s, creditAccount=%s]", p.ID, p.Amount, p.DebitAccount, p.CreditAccount)
}

func DecodePostingV1(bytes []byte) (PostingV1, error) { return DecodePostingV1From(NewDecoder(bytes)) }

func DecodePostingV1From(d *Decoder) (PostingV1, error) {
	var id uint64
	var minor int64
	currency, debit, credit := "", "", ""
	for {
		more, err := d.Next()
		if err != nil {
			return PostingV1{}, err
		}
		if !more {
			break
		}
		switch d.Field() {
		case F_ID:
			id, err = d.Uint64()
		case F_AMOUNT:
			minor, err = d.Sint64()
		case F_CURRENCY:
			currency, err = d.String()
		case F_DEBIT:
			debit, err = d.String()
		case F_CREDIT:
			credit, err = d.String()
		default:
			err = d.Skip() // a field from the future: preserve nothing, break nothing
		}
		if err != nil {
			return PostingV1{}, err
		}
	}
	if currency == "" {
		return PostingV1{}, malformed("posting without a currency")
	}
	return PostingV1{id, OfMinor(minor, CurrencyOf(currency)), debit, credit}, nil
}

// ---------------------------------------------------------------- Audit and PostingV2 ----

// Audit is the audit trail added in version 2 as a nested message: 1: actor (string)  2: recorded_at_millis (fixed64).
type Audit struct {
	Actor            string
	RecordedAtMillis int64
}

func (a Audit) Encode() []byte {
	b, _ := NewEncoder().String(1, a.Actor).Fixed64(2, a.RecordedAtMillis).ToByteArray()
	return b
}

func (a Audit) String() string {
	return fmt.Sprintf("Audit[actor=%s, recordedAtMillis=%d]", a.Actor, a.RecordedAtMillis)
}

func DecodeAudit(d *Decoder) (Audit, error) {
	actor := ""
	var at int64
	for {
		more, err := d.Next()
		if err != nil {
			return Audit{}, err
		}
		if !more {
			break
		}
		switch d.Field() {
		case 1:
			actor, err = d.String()
		case 2:
			at, err = d.Fixed64()
		default:
			err = d.Skip()
		}
		if err != nil {
			return Audit{}, err
		}
	}
	return Audit{actor, at}, nil
}

// PostingV2 is schema version 2: fields 1-5 unchanged; 6: memo (string), 7: audit (nested message), 8: reversal (bool).
// Field numbers 1-5 keep their numbers and types forever; new fields take new numbers; nothing is renumbered.
// Audit is a pointer so that "absent" (Java null) is distinct from an empty audit.
type PostingV2 struct {
	ID            uint64
	Amount        Money
	DebitAccount  string
	CreditAccount string
	Memo          string
	Audit         *Audit
	Reversal      bool
}

const (
	F_MEMO     = 6
	F_AUDIT    = 7
	F_REVERSAL = 8
)

func (p PostingV2) Encode() []byte {
	e := NewEncoder().
		Uint64(F_ID, p.ID).
		Sint64(F_AMOUNT, p.Amount.MinorUnits).
		String(F_CURRENCY, p.Amount.Currency.Code).
		String(F_DEBIT, p.DebitAccount).
		String(F_CREDIT, p.CreditAccount)
	if p.Memo != "" { // default values are omitted, as protobuf does
		e.String(F_MEMO, p.Memo)
	}
	if p.Audit != nil {
		e.Message(F_AUDIT, NewEncoder().String(1, p.Audit.Actor).Fixed64(2, p.Audit.RecordedAtMillis))
	}
	if p.Reversal {
		e.Bool(F_REVERSAL, true)
	}
	b, _ := e.ToByteArray()
	return b
}

func DecodePostingV2(bytes []byte) (PostingV2, error) {
	d := NewDecoder(bytes)
	var id uint64
	var minor int64
	currency, debit, credit, memo := "", "", "", ""
	var audit *Audit
	reversal := false
	for {
		more, err := d.Next()
		if err != nil {
			return PostingV2{}, err
		}
		if !more {
			break
		}
		switch d.Field() {
		case F_ID:
			id, err = d.Uint64()
		case F_AMOUNT:
			minor, err = d.Sint64()
		case F_CURRENCY:
			currency, err = d.String()
		case F_DEBIT:
			debit, err = d.String()
		case F_CREDIT:
			credit, err = d.String()
		case F_MEMO:
			memo, err = d.String()
		case F_AUDIT:
			var nested *Decoder
			if nested, err = d.Message(); err == nil {
				var a Audit
				a, err = DecodeAudit(nested)
				audit = &a
			}
		case F_REVERSAL:
			reversal, err = d.Bool()
		default:
			err = d.Skip()
		}
		if err != nil {
			return PostingV2{}, err
		}
	}
	if currency == "" {
		return PostingV2{}, malformed("posting without a currency")
	}
	return PostingV2{id, OfMinor(minor, CurrencyOf(currency)), debit, credit, memo, audit, reversal}, nil
}

func (p PostingV2) AsV1() PostingV1 {
	return PostingV1{p.ID, p.Amount, p.DebitAccount, p.CreditAccount}
}

// Equal is the record equality the Java gets for free: Audit compares by value, absent equals absent.
func (p PostingV2) Equal(o PostingV2) bool {
	if p.AsV1() != o.AsV1() || p.Memo != o.Memo || p.Reversal != o.Reversal {
		return false
	}
	if p.Audit == nil || o.Audit == nil {
		return p.Audit == nil && o.Audit == nil
	}
	return *p.Audit == *o.Audit
}

// ---------------------------------------------------------------- Json ----

// JsonPosting is a minimal JSON writer for the size comparison and the 2^53 demonstration. RFC 8259 string escaping; 64-bit numbers optionally quoted.
func JsonPosting(p PostingV2, numbersAsStrings bool) string {
	var sb strings.Builder
	sb.WriteString(`{"id":`)
	if numbersAsStrings {
		sb.WriteString(`"` + strconv.FormatUint(p.ID, 10) + `"`)
	} else {
		sb.WriteString(strconv.FormatUint(p.ID, 10))
	}
	sb.WriteString(`,"amountMinor":`)
	if numbersAsStrings {
		sb.WriteString(`"` + strconv.FormatInt(p.Amount.MinorUnits, 10) + `"`)
	} else {
		sb.WriteString(strconv.FormatInt(p.Amount.MinorUnits, 10))
	}
	sb.WriteString(`,"currency":` + JsonQuote(p.Amount.Currency.Code))
	sb.WriteString(`,"debitAccount":` + JsonQuote(p.DebitAccount))
	sb.WriteString(`,"creditAccount":` + JsonQuote(p.CreditAccount))
	if p.Memo != "" {
		sb.WriteString(`,"memo":` + JsonQuote(p.Memo))
	}
	if p.Audit != nil {
		sb.WriteString(`,"audit":{"actor":` + JsonQuote(p.Audit.Actor) + `,"recordedAtMillis":` + strconv.FormatInt(p.Audit.RecordedAtMillis, 10) + `}`)
	}
	if p.Reversal {
		sb.WriteString(`,"reversal":true`)
	}
	sb.WriteByte('}')
	return sb.String()
}

func JsonQuote(s string) string {
	var sb strings.Builder
	sb.WriteByte('"')
	for i := 0; i < len(s); i++ { // byte-wise: every byte of a multi-byte UTF-8 sequence is >= 0x80 and passes through
		c := s[i]
		switch c {
		case '"':
			sb.WriteString(`\"`)
		case '\\':
			sb.WriteString(`\\`)
		case '\n':
			sb.WriteString(`\n`)
		case '\r':
			sb.WriteString(`\r`)
		case '\t':
			sb.WriteString(`\t`)
		default:
			if c < 0x20 {
				fmt.Fprintf(&sb, `\u%04x`, c)
			} else {
				sb.WriteByte(c)
			}
		}
	}
	sb.WriteByte('"')
	return sb.String()
}

// ---------------------------------------------------------------- Demo ----

// Hex formats bytes as space-separated lowercase hex, like HexFormat.ofDelimiter(" ").
func Hex(b []byte) string {
	parts := make([]string, len(b))
	for i, x := range b {
		parts[i] = fmt.Sprintf("%02x", x)
	}
	return strings.Join(parts, " ")
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func auditString(a *Audit) string {
	if a == nil {
		return "null"
	}
	return a.String()
}

// randomPosting draws the fields in Java's left-to-right argument order so the seeded stream lines up.
func randomPosting(rng *JavaRandom) PostingV2 {
	id := uint64(rng.NextLong()) >> 1
	amount := OfMinor(int64(rng.NextIntN(2_000_001))-1_000_000, USD)
	debit := "acct:" + strconv.Itoa(int(rng.NextIntN(100_000)))
	credit := "acct:" + strconv.Itoa(int(rng.NextIntN(100_000)))
	memo := ""
	if rng.NextBoolean() {
		memo = "memo " + strconv.Itoa(int(rng.NextIntN(1000)))
	}
	var audit *Audit
	if rng.NextBoolean() {
		audit = &Audit{"svc-" + strconv.Itoa(int(rng.NextIntN(10))), 1_760_000_000_000 + int64(rng.NextIntN(1_000_000))}
	}
	reversal := rng.NextIntN(20) == 0
	return PostingV2{id, amount, debit, credit, memo, audit, reversal}
}

func main() {
	fmt.Println("== 1. Varints: 7 bits per byte, little-endian, continuation bit ==")
	for _, v := range []int64{0, 1, 127, 128, 150, 300, 16_383, 16_384, 1_000_000_000, math.MaxInt64} {
		var s ByteSink
		WriteUnsigned(&s, uint64(v))
		fmt.Printf("  %20d -> %-30s (%d byte%s)\n", v, Hex(s.ToByteArray()), s.Size(), plural(s.Size()))
	}
	var neg ByteSink
	WriteUnsigned(&neg, math.MaxUint64) // Java: writeUnsigned(-1)
	fmt.Println("  -1 as a plain varint = " + Hex(neg.ToByteArray()) + " (10 bytes: the sign bit is 63 bits up)")

	fmt.Println()
	fmt.Println("== 2. ZigZag: small negatives stay small ==")
	for _, v := range []int64{0, -1, 1, -2, 2, -64, 63, -65, 64, math.MinInt64} {
		var s ByteSink
		WriteSigned(&s, v)
		fmt.Printf("  %20d -> zigzag %20d -> %-30s (%d byte%s)\n", v, int64(ZigzagEncode(v)), Hex(s.ToByteArray()), s.Size(), plural(s.Size()))
	}
	var refund ByteSink
	WriteSigned(&refund, -1999)
	fmt.Println("  a refund of -19.99 USD as sint64 = " + Hex(refund.ToByteArray()) + " (2 bytes, versus 10 as uint64)")

	fmt.Println()
	fmt.Println("== 3. Tags and wire types: the protobuf.dev test vectors ==")
	f1 := must(NewEncoder().Uint64(1, 150).ToByteArray())
	f2 := must(NewEncoder().String(2, "testing").ToByteArray())
	fmt.Println("  field 1 = 150        -> " + Hex(f1) + "   (tag 0x08 = field 1 << 3 | VARINT)")
	fmt.Println("  field 2 = \"testing\"  -> " + Hex(f2) + "   (tag 0x12 = field 2 << 3 | LENGTH_DELIMITED, length 7)")

	fmt.Println()
	fmt.Println("== 4. A posting on the wire ==")
	p := PostingV2{1_048_577, OfMinor(-1999, USD), "acct:merchant:4471", "acct:customer:9", "refund order 5512",
		&Audit{"svc-refunds", 1_760_000_000_000}, true}
	bin := p.Encode()
	fmt.Printf("  binary (%d bytes): %s\n", len(bin), Hex(bin))
	json := JsonPosting(p, false)
	fmt.Printf("  json   (%d bytes): %s\n", len(json), json)
	back := must(DecodePostingV2(bin))
	fmt.Printf("  decode(encode(p)) equals p: %t  amount = %s\n", back.Equal(p), back.Amount)

	fmt.Println()
	fmt.Println("== 5. Schema evolution ==")
	v1View := NewDecoder(bin)
	oldReader := must(DecodePostingV1From(v1View))
	fmt.Printf("  v1 reader on v2 bytes: %s  (skipped %d unknown fields by wire type)\n", oldReader, v1View.Skipped)
	v1 := PostingV1{7, OfMinor(1250, USD), "acct:a", "acct:b"}
	newReader := must(DecodePostingV2(v1.Encode()))
	fmt.Printf("  v2 reader on v1 bytes: memo=\"%s\" audit=%s reversal=%t  (defaults, not errors)\n", newReader.Memo, auditString(newReader.Audit), newReader.Reversal)
	fmt.Printf("  v2 -> v1 -> compare core fields: %t\n", back.AsV1() == oldReader)

	fmt.Println()
	fmt.Println("== 6. What JSON numbers cannot carry ==")
	bigID := uint64(9_007_199_254_740_993) // 2^53 + 1
	asDouble := float64(bigID)
	fmt.Printf("  id %d through a JSON parser that uses double -> %d  (lost: %d)\n", bigID, uint64(asDouble), bigID-uint64(asDouble))
	fmt.Println("  Twitter, Stripe, and this lesson send 64-bit ids as strings for this reason: " + JsonPosting(PostingV2{bigID, p.Amount, "a", "b", "", nil, false}, true))
	tenth, fifth := 0.1, 0.2 // variables, not constants: Go folds constant expressions exactly and would print 0.3
	fmt.Printf("  0.1 + 0.2 in JSON-as-double = %v; minor units as integers avoid the question entirely\n", tenth+fifth)

	fmt.Println()
	fmt.Println("== 7. Size over 10,000 random postings ==")
	rng := NewJavaRandom(11)
	var binTotal, jsonTotal int64
	for i := 0; i < 10_000; i++ {
		q := randomPosting(rng)
		b := q.Encode()
		if !must(DecodePostingV2(b)).Equal(q) {
			panic("round trip failed")
		}
		binTotal += int64(len(b))
		jsonTotal += int64(len(JsonPosting(q, true)))
	}
	fmt.Printf("  binary %d bytes, json %d bytes, ratio %.2fx; all 10000 round-tripped\n", binTotal, jsonTotal, float64(jsonTotal)/float64(binTotal))

	fmt.Println()
	fmt.Println("== 8. Malformed input is an error, not a guess ==")
	truncated := bin[:len(bin)-3]
	if _, err := DecodePostingV2(truncated); errors.Is(err, ErrMalformed) {
		fmt.Println("  last 3 bytes cut off: " + strings.TrimPrefix(err.Error(), ErrMalformed.Error()+": "))
	}
	wrongType := must(NewEncoder().String(F_AMOUNT, "19.99").ToByteArray())
	if _, err := DecodePostingV2(wrongType); errors.Is(err, ErrMalformed) {
		fmt.Println("  type changed on field 2: " + strings.TrimPrefix(err.Error(), ErrMalformed.Error()+": "))
	}
}
