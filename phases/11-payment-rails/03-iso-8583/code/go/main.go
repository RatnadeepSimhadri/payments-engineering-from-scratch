// Lesson: phases/11-payment-rails/03-iso-8583/docs/en.md
// ISO 8583: Parsing the Card Network Message
// Run:  go run .
// Test: go test ./...
// Sources: ISO 8583-1:1987 (MTI, bitmaps, data element dictionary); ISO 4217 numeric currency codes.
// Go 1.22+, standard library only. Same design as the Java and Python versions. ASCII variant; test PANs only.
package main

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// ErrMalformed is the sentinel every parse, build, or validation error wraps. Nothing is ever guessed.
var ErrMalformed = errors.New("iso8583")

func malformed(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrMalformed, fmt.Sprintf(format, args...))
}

// Currency is an ISO 4217 currency with its numeric code, the form DE49 carries (840 = USD).
type Currency struct {
	Code        string
	NumericCode int
	Exponent    int
}

var (
	USD = Currency{"USD", 840, 2}
	EUR = Currency{"EUR", 978, 2}
	GBP = Currency{"GBP", 826, 2}
	INR = Currency{"INR", 356, 2}
	JPY = Currency{"JPY", 392, 0}
	KWD = Currency{"KWD", 414, 3}
)

var currencies = []Currency{USD, EUR, GBP, INR, JPY, KWD}

// CurrencyFromNumeric resolves "840" to USD.
func CurrencyFromNumeric(threeDigits string) (Currency, error) {
	for _, c := range currencies {
		if fmt.Sprintf("%03d", c.NumericCode) == threeDigits {
			return c, nil
		}
	}
	return Currency{}, malformed("unknown numeric currency code %s", threeDigits)
}

func (c Currency) MinorUnitsPerMajor() int64 {
	scale := int64(1)
	for i := 0; i < c.Exponent; i++ {
		scale *= 10
	}
	return scale
}

// Money is integer minor units plus currency, copied from Phase 10, Lesson 01. DE4 is always minor units.
type Money struct {
	MinorUnits int64
	Currency   Currency
}

func OfMinor(minorUnits int64, currency Currency) Money { return Money{minorUnits, currency} }

func (m Money) String() string {
	magnitude := m.MinorUnits
	if magnitude < 0 {
		magnitude = -magnitude
	}
	scale := m.Currency.MinorUnitsPerMajor()
	text := fmt.Sprintf("%d", magnitude/scale)
	if m.Currency.Exponent > 0 {
		text += fmt.Sprintf(".%0*d", m.Currency.Exponent, magnitude%scale)
	}
	if m.MinorUnits < 0 {
		text = "-" + text
	}
	return text + " " + m.Currency.Code
}

// FieldType says how a data element's length is known: fixed by the spec, or announced by a 2- or 3-digit prefix.
type FieldType int

const (
	Fixed  FieldType = 0
	LLVar  FieldType = 2
	LLLVar FieldType = 3
)

// PrefixDigits is the width of the length prefix (0 for fixed fields).
func (t FieldType) PrefixDigits() int { return int(t) }

// FieldSpec is one row of the data element dictionary: number, length rule, length (exact or max), character class.
type FieldSpec struct {
	Number  int
	Type    FieldType
	Length  int
	Numeric bool
	Name    string
}

// NewFieldSpec validates the row; the standard dictionary below is known-good.
func NewFieldSpec(number int, t FieldType, length int, numeric bool, name string) (FieldSpec, error) {
	switch {
	case number < 2 || number > 128:
		return FieldSpec{}, fmt.Errorf("data element number must be 2..128: %d", number)
	case length <= 0:
		return FieldSpec{}, errors.New("length must be positive")
	case t == LLVar && length > 99:
		return FieldSpec{}, errors.New("LLVAR max length is 99")
	case t == LLLVar && length > 999:
		return FieldSpec{}, errors.New("LLLVAR max length is 999")
	}
	return FieldSpec{number, t, length, numeric, name}, nil
}

// Validate rejects a value that does not fit this element. Called on Set and again on Encode.
func (f FieldSpec) Validate(value string) error {
	label := fmt.Sprintf("DE%d (%s)", f.Number, f.Name)
	switch {
	case value == "":
		return malformed("%s must not be empty", label)
	case f.Type == Fixed && len(value) != f.Length:
		return malformed("%s must be exactly %d chars, got %d", label, f.Length, len(value))
	case f.Type != Fixed && len(value) > f.Length:
		return malformed("%s exceeds max length %d: %d", label, f.Length, len(value))
	case f.Numeric && !allDigits(value):
		return malformed("%s must be numeric", label)
	}
	for i := 0; i < len(value); i++ {
		if value[i] < 0x20 || value[i] > 0x7E {
			return malformed("DE%d contains a non-ASCII character", f.Number)
		}
	}
	return nil
}

func allDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return len(s) > 0
}

// Spec is the practical subset of the ISO 8583-1:1987 dictionary this lesson implements.
type Spec struct {
	fields map[int]FieldSpec
}

func NewSpec() *Spec { return &Spec{fields: map[int]FieldSpec{}} }

// Standard1987 returns the dictionary subset used throughout the lesson.
func Standard1987() *Spec {
	s := NewSpec()
	rows := []FieldSpec{
		{2, LLVar, 19, true, "primary account number"},
		{3, Fixed, 6, true, "processing code"},
		{4, Fixed, 12, true, "amount, transaction"},
		{7, Fixed, 10, true, "transmission date and time MMDDhhmmss"},
		{11, Fixed, 6, true, "system trace audit number"},
		{12, Fixed, 6, true, "time, local transaction hhmmss"},
		{13, Fixed, 4, true, "date, local transaction MMDD"},
		{22, Fixed, 3, true, "point of service entry mode"},
		{37, Fixed, 12, false, "retrieval reference number"},
		{39, Fixed, 2, false, "response code"},
		{41, Fixed, 8, false, "card acceptor terminal id"},
		{42, Fixed, 15, false, "card acceptor id"},
		{49, Fixed, 3, true, "currency code, transaction"},
		{102, LLVar, 28, false, "account identification 1"},
		{123, LLLVar, 999, false, "reserved for private use"},
	}
	for _, r := range rows {
		s.Add(r)
	}
	return s
}

func (s *Spec) Add(f FieldSpec) *Spec {
	s.fields[f.Number] = f
	return s
}

func (s *Spec) Require(number int) (FieldSpec, error) {
	f, ok := s.fields[number]
	if !ok {
		return FieldSpec{}, malformed("DE%d is not defined in this specification", number)
	}
	return f, nil
}

func (s *Spec) Defines(number int) bool {
	_, ok := s.fields[number]
	return ok
}

// Bitmap is the 8-byte presence map. Bit 1 is the most significant bit of the first byte and announces a second bitmap.
type Bitmap struct {
	bits [65]bool // index 1..64
}

func (b *Bitmap) Set(bit int)        { b.bits[bit] = true }
func (b *Bitmap) IsSet(bit int) bool { return b.bits[bit] }

// ToHex renders 16 uppercase hex characters, the ASCII variant of the 8 binary bytes.
func (b *Bitmap) ToHex() string {
	var sb strings.Builder
	for by := 0; by < 8; by++ {
		value := 0
		for i := 0; i < 8; i++ {
			if b.bits[by*8+i+1] {
				value |= 1 << (7 - i)
			}
		}
		fmt.Fprintf(&sb, "%02X", value)
	}
	return sb.String()
}

// BitmapFromHex parses 16 hex characters.
func BitmapFromHex(hex string) (*Bitmap, error) {
	if len(hex) != 16 {
		return nil, malformed("bitmap must be 16 hex characters, got %d", len(hex))
	}
	bm := &Bitmap{}
	for by := 0; by < 8; by++ {
		value, err := strconv.ParseUint(hex[by*2:by*2+2], 16, 8)
		if err != nil {
			return nil, malformed("bitmap is not hexadecimal: %s", hex)
		}
		for i := 0; i < 8; i++ {
			if value&(1<<(7-i)) != 0 {
				bm.bits[by*8+i+1] = true
			}
		}
	}
	return bm, nil
}

// Message is one ISO 8583 message: a four-digit MTI and a map of present data elements.
// Values are stored as strings in their on-the-wire form; interpretation (amount, currency)
// happens in typed accessors so the wire format and the domain type never get confused.
type Message struct {
	MTI    string
	spec   *Spec
	fields map[int]string
}

// NewMessage validates the MTI.
func NewMessage(mti string, spec *Spec) (*Message, error) {
	if len(mti) != 4 || !allDigits(mti) {
		return nil, malformed("MTI must be four digits: %q", mti)
	}
	return &Message{MTI: mti, spec: spec, fields: map[int]string{}}, nil
}

// MTIVersion: 0 = 1987, 1 = 1993, 2 = 2003. This lesson only encodes version 0.
func (m *Message) MTIVersion() int { return int(m.MTI[0] - '0') }

// MTIClass: 1 = authorization, 2 = financial, 4 = reversal, 8 = network management.
func (m *Message) MTIClass() int { return int(m.MTI[1] - '0') }

// MTIFunction: 0 = request, 1 = request response, 2 = advice, 3 = advice response, 4 = notification.
func (m *Message) MTIFunction() int { return int(m.MTI[2] - '0') }

// MTIOrigin: 0 = acquirer, 1 = acquirer repeat, 2 = issuer, 3 = issuer repeat.
func (m *Message) MTIOrigin() int { return int(m.MTI[3] - '0') }

// ResponseMTI: 0100 -> 0110, 0200 -> 0210, 0420 -> 0430. Only requests and advices have a response.
func (m *Message) ResponseMTI() (string, error) {
	fn := m.MTIFunction()
	if fn != 0 && fn != 2 {
		return "", malformed("MTI %s is not a request or advice", m.MTI)
	}
	return m.MTI[:2] + strconv.Itoa(fn+1) + m.MTI[3:], nil
}

// Set validates against the spec and stores the on-the-wire string.
func (m *Message) Set(number int, value string) error {
	fs, err := m.spec.Require(number)
	if err != nil {
		return err
	}
	if err := fs.Validate(value); err != nil {
		return err
	}
	m.fields[number] = value
	return nil
}

// Get returns a present element or an error.
func (m *Message) Get(number int) (string, error) {
	v, ok := m.fields[number]
	if !ok {
		return "", malformed("DE%d is not present in this message", number)
	}
	return v, nil
}

func (m *Message) Has(number int) bool {
	_, ok := m.fields[number]
	return ok
}

// FieldNumbers lists present elements in ascending order.
func (m *Message) FieldNumbers() []int {
	out := make([]int, 0, len(m.fields))
	for n := range m.fields {
		out = append(out, n)
	}
	sort.Ints(out)
	return out
}

// Amount interprets DE4 (12 digits, minor units) in the currency named by DE49. The only place money is typed.
func (m *Message) Amount() (Money, error) {
	de4, err := m.Get(4)
	if err != nil {
		return Money{}, err
	}
	de49, err := m.Get(49)
	if err != nil {
		return Money{}, err
	}
	currency, err := CurrencyFromNumeric(de49)
	if err != nil {
		return Money{}, err
	}
	minor, err := strconv.ParseInt(de4, 10, 64)
	if err != nil {
		return Money{}, malformed("DE4 is not numeric: %s", de4)
	}
	return OfMinor(minor, currency), nil
}

// SetAmount writes DE4 zero-padded to 12 digits and DE49 as the numeric currency code.
func (m *Message) SetAmount(money Money) error {
	if money.MinorUnits < 0 {
		return malformed("DE4 cannot carry a negative amount; use the MTI class instead")
	}
	if err := m.Set(4, fmt.Sprintf("%012d", money.MinorUnits)); err != nil {
		return err
	}
	return m.Set(49, fmt.Sprintf("%03d", money.Currency.NumericCode))
}

// MaskedPAN shows the first six and last four digits of DE2. Never log the raw PAN.
func (m *Message) MaskedPAN() (string, error) {
	pan, err := m.Get(2)
	if err != nil {
		return "", err
	}
	if len(pan) <= 10 {
		return strings.Repeat("*", len(pan)), nil
	}
	return pan[:6] + strings.Repeat("*", len(pan)-10) + pan[len(pan)-4:], nil
}

// Encode writes the MTI, the bitmaps, then every present element in ascending order. ASCII variant.
func (m *Message) Encode() ([]byte, error) {
	var primary, secondary Bitmap
	needSecondary := false
	for n := range m.fields {
		if n <= 64 {
			primary.Set(n)
		} else {
			secondary.Set(n - 64)
			needSecondary = true
		}
	}
	if needSecondary {
		primary.Set(1)
	}
	var out strings.Builder
	out.WriteString(m.MTI)
	out.WriteString(primary.ToHex())
	if needSecondary {
		out.WriteString(secondary.ToHex())
	}
	for _, n := range m.FieldNumbers() {
		fs, err := m.spec.Require(n)
		if err != nil {
			return nil, err
		}
		value := m.fields[n]
		if err := fs.Validate(value); err != nil {
			return nil, err
		}
		if fs.Type != Fixed {
			fmt.Fprintf(&out, "%0*d", fs.Type.PrefixDigits(), len(value))
		}
		out.WriteString(value)
	}
	return []byte(out.String()), nil
}

// Decode is the inverse of Encode. Every malformed input is a specific error; nothing is guessed.
func Decode(raw []byte, spec *Spec) (*Message, error) {
	s := string(raw)
	if len(s) < 4+16 {
		return nil, malformed("message too short for MTI and primary bitmap: %d bytes", len(s))
	}
	msg, err := NewMessage(s[:4], spec)
	if err != nil {
		return nil, err
	}
	pos := 4
	primary, err := BitmapFromHex(s[pos : pos+16])
	if err != nil {
		return nil, err
	}
	pos += 16
	var present [129]bool
	for bit := 2; bit <= 64; bit++ {
		present[bit] = primary.IsSet(bit)
	}
	if primary.IsSet(1) {
		if len(s) < pos+16 {
			return nil, malformed("bit 1 set but no secondary bitmap follows")
		}
		secondary, err := BitmapFromHex(s[pos : pos+16])
		if err != nil {
			return nil, err
		}
		pos += 16
		if secondary.IsSet(1) {
			return nil, malformed("tertiary bitmap (bit 65) is not supported")
		}
		for bit := 2; bit <= 64; bit++ {
			present[64+bit] = secondary.IsSet(bit)
		}
	}
	for n := 2; n <= 128; n++ {
		if !present[n] {
			continue
		}
		fs, err := spec.Require(n)
		if err != nil {
			return nil, err
		}
		length := fs.Length
		if fs.Type != Fixed {
			digits := fs.Type.PrefixDigits()
			if pos+digits > len(s) {
				return nil, malformed("truncated length prefix for DE%d", n)
			}
			prefix := s[pos : pos+digits]
			if !allDigits(prefix) {
				return nil, malformed("length prefix for DE%d is not numeric: %s", n, prefix)
			}
			length, _ = strconv.Atoi(prefix)
			if length > fs.Length {
				return nil, malformed("DE%d announces %d chars, max is %d", n, length, fs.Length)
			}
			pos += digits
		}
		if pos+length > len(s) {
			return nil, malformed("truncated: DE%d needs %d chars, %d remain", n, length, len(s)-pos)
		}
		if err := msg.Set(n, s[pos:pos+length]); err != nil {
			return nil, err
		}
		pos += length
	}
	if pos != len(s) {
		return nil, malformed("%d trailing bytes after the last data element", len(s)-pos)
	}
	return msg, nil
}

// Dump is human-readable, PAN masked, one element per line. For logs and the demo.
func (m *Message) Dump() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "MTI %s\n", m.MTI)
	for _, n := range m.FieldNumbers() {
		fs, _ := m.spec.Require(n)
		shown := m.fields[n]
		if n == 2 {
			shown, _ = m.MaskedPAN()
		}
		fmt.Fprintf(&sb, "  DE%-3d %-40s %s\n", n, fs.Name, shown)
	}
	return sb.String()
}

// TestPAN is a published Visa test number, never a real card.
const TestPAN = "4111111111111111"

func must(err error) {
	if err != nil {
		panic(err)
	}
}

func mustMsg(m *Message, err error) *Message {
	must(err)
	return m
}

func mustStr(s string, err error) string {
	must(err)
	return s
}

func mustBytes(b []byte, err error) []byte {
	must(err)
	return b
}

func mustMoney(m Money, err error) Money {
	must(err)
	return m
}

func setAll(m *Message, pairs map[int]string) {
	for n, v := range pairs {
		must(m.Set(n, v))
	}
}

func main() {
	spec := Standard1987()

	fmt.Println("== Building a 0200 financial request ==")
	request := mustMsg(NewMessage("0200", spec))
	setAll(request, map[int]string{
		2: TestPAN, 3: "000000", 7: "1012143015", 11: "000123", 12: "143015", 13: "1012",
		22: "051", 37: "123456789012", 41: "TERM0001", 42: "MERCHANT0000001",
	})
	must(request.SetAmount(OfMinor(1999, USD)))
	fmt.Print(request.Dump())
	wire := mustBytes(request.Encode())
	wireText := string(wire)
	fmt.Printf("encoded %d bytes:\n", len(wire))
	fmt.Println("  " + wireText)
	fmt.Printf("  MTI=%s bitmap=%s DE2 prefix=%s\n", wireText[:4], wireText[4:20], wireText[20:22])

	fmt.Println("\n== Decoding it back ==")
	decoded := mustMsg(Decode(wire, spec))
	fmt.Printf("amount = %v, STAN = %s, RRN = %s\n", mustMoney(decoded.Amount()), mustStr(decoded.Get(11)), mustStr(decoded.Get(37)))
	fmt.Printf("round trip equal: %v\n", string(mustBytes(decoded.Encode())) == wireText)

	fmt.Println("\n== The issuer answers with a 0210 ==")
	response := mustMsg(NewMessage(mustStr(request.ResponseMTI()), spec))
	for _, n := range []int{2, 3, 4, 7, 11, 12, 13, 37, 41, 42, 49} {
		must(response.Set(n, mustStr(request.Get(n))))
	}
	must(response.Set(39, "00"))
	responseText := string(mustBytes(response.Encode()))
	fmt.Printf("MTI %s bitmap=%s (bit 39 added, bit 22 dropped)\n", response.MTI, responseText[4:20])
	fmt.Printf("response code DE39 = %s -> approved\n", mustStr(response.Get(39)))

	fmt.Println("\n== A secondary bitmap appears when any DE above 64 is present ==")
	withAccount := mustMsg(NewMessage("0200", spec))
	setAll(withAccount, map[int]string{2: TestPAN, 3: "000000", 11: "000124", 102: "12345678901"})
	must(withAccount.SetAmount(OfMinor(500000, INR)))
	secondaryText := string(mustBytes(withAccount.Encode()))
	fmt.Printf("primary   = %s  (first hex digit F: bit 1 set)\n", secondaryText[4:20])
	fmt.Printf("secondary = %s  (bit 38 = DE 102)\n", secondaryText[20:36])
	fmt.Printf("amount = %v\n", mustMoney(mustMsg(Decode(mustBytes(withAccount.Encode()), spec)).Amount()))

	fmt.Println("\n== Malformed input is rejected, never guessed ==")
	bad := []string{
		wireText[:60],    // truncated mid-field
		wireText + "XYZ", // trailing bytes
		"0200" + "ZZ38040008C08000" + wireText[20:],        // bitmap not hex
		"0200" + "7238040008C08000" + "99" + wireText[22:], // LLVAR length over max
	}
	for _, b := range bad {
		if _, err := Decode([]byte(b), spec); err != nil {
			fmt.Println("rejected:", err)
		} else {
			fmt.Println("accepted (unexpected)")
		}
	}
	if err := mustMsg(NewMessage("0200", spec)).Set(4, "19.99"); err != nil {
		fmt.Println("rejected:", err)
	}
}
