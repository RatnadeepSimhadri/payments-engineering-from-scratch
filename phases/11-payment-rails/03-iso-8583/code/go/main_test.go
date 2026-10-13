package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

const expectedWire = "0200" + "7238040008C08000" +
	"16" + "4111111111111111" + "000000" + "000000001999" + "1012143015" + "000123" + "143015" + "1012" +
	"051" + "123456789012" + "TERM0001" + "MERCHANT0000001" + "840"

func purchase(t *testing.T, spec *Spec) *Message {
	t.Helper()
	m, err := NewMessage("0200", spec)
	if err != nil {
		t.Fatal(err)
	}
	for n, v := range map[int]string{
		2: TestPAN, 3: "000000", 7: "1012143015", 11: "000123", 12: "143015", 13: "1012",
		22: "051", 37: "123456789012", 41: "TERM0001", 42: "MERCHANT0000001",
	} {
		if err := m.Set(n, v); err != nil {
			t.Fatalf("Set(%d): %v", n, err)
		}
	}
	if err := m.SetAmount(OfMinor(1999, USD)); err != nil {
		t.Fatal(err)
	}
	return m
}

func encode(t *testing.T, m *Message) []byte {
	t.Helper()
	b, err := m.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	return b
}

func get(t *testing.T, m *Message, n int) string {
	t.Helper()
	v, err := m.Get(n)
	if err != nil {
		t.Fatalf("Get(%d): %v", n, err)
	}
	return v
}

func expectMalformed(t *testing.T, name string, err error) {
	t.Helper()
	if err == nil {
		t.Errorf("%s: expected an error", name)
	} else if !errors.Is(err, ErrMalformed) {
		t.Errorf("%s: wrong error type: %v", name, err)
	}
}

func TestBitmapHexForWorkedExample(t *testing.T) {
	var bm Bitmap
	for _, n := range []int{2, 3, 4, 7, 11, 12, 13, 22, 37, 41, 42, 49} {
		bm.Set(n)
	}
	if got := bm.ToHex(); got != "7238040008C08000" {
		t.Errorf("ToHex = %s", got)
	}
}

func TestBitmapRoundTripAndBit39(t *testing.T) {
	bm, err := BitmapFromHex("7238040008C08000")
	if err != nil {
		t.Fatal(err)
	}
	if !bm.IsSet(49) || bm.IsSet(39) {
		t.Error("bit 49 should be set, bit 39 clear")
	}
	bm.Set(39)
	if got := bm.ToHex(); got != "723804000AC08000" {
		t.Errorf("after setting 39: %s", got)
	}
}

func TestBitmapRejects(t *testing.T) {
	_, err := BitmapFromHex("ZZ38040008C08000")
	expectMalformed(t, "non-hex", err)
	_, err = BitmapFromHex("7238")
	expectMalformed(t, "wrong length", err)
}

func TestMTI(t *testing.T) {
	spec := Standard1987()
	req := purchase(t, spec)
	if req.MTIVersion() != 0 || req.MTIClass() != 2 || req.MTIFunction() != 0 || req.MTIOrigin() != 0 {
		t.Error("MTI digits of 0200 misread")
	}
	if r, _ := req.ResponseMTI(); r != "0210" {
		t.Errorf("ResponseMTI = %s", r)
	}
	rev, _ := NewMessage("0420", spec)
	if r, _ := rev.ResponseMTI(); r != "0430" {
		t.Errorf("ResponseMTI(0420) = %s", r)
	}
	resp, _ := NewMessage("0210", spec)
	_, err := resp.ResponseMTI()
	expectMalformed(t, "response of a response", err)
	_, err = NewMessage("02A0", spec)
	expectMalformed(t, "MTI with letter", err)
	_, err = NewMessage("200", spec)
	expectMalformed(t, "MTI too short", err)
}

func TestEncodeMatchesHandBuiltMessage(t *testing.T) {
	wire := encode(t, purchase(t, Standard1987()))
	if len(wire) != 123 {
		t.Errorf("length = %d, want 123", len(wire))
	}
	if string(wire) != expectedWire {
		t.Errorf("wire = %s", wire)
	}
	if string(wire[20:22]) != "16" {
		t.Error("LLVAR prefix for 16-digit PAN should be 16")
	}
}

func TestDecodeAndRoundTrip(t *testing.T) {
	spec := Standard1987()
	req := purchase(t, spec)
	wire := encode(t, req)
	back, err := Decode(wire, spec)
	if err != nil {
		t.Fatal(err)
	}
	if back.MTI != "0200" || get(t, back, 2) != TestPAN || get(t, back, 11) != "000123" || get(t, back, 37) != "123456789012" {
		t.Error("decoded fields differ")
	}
	if amt, _ := back.Amount(); amt != OfMinor(1999, USD) {
		t.Errorf("Amount = %v", amt)
	}
	if !bytes.Equal(encode(t, back), wire) {
		t.Error("round trip is not byte-identical")
	}
	if back.Has(39) {
		t.Error("DE39 should be absent")
	}
	_, err = back.Get(39)
	expectMalformed(t, "get absent", err)
	if len(back.FieldNumbers()) != len(req.FieldNumbers()) {
		t.Error("field set differs")
	}
}

func TestSecondaryBitmap(t *testing.T) {
	spec := Standard1987()
	m, _ := NewMessage("0200", spec)
	for n, v := range map[int]string{2: TestPAN, 3: "000000", 11: "000124", 102: "12345678901"} {
		if err := m.Set(n, v); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.SetAmount(OfMinor(500000, INR)); err != nil {
		t.Fatal(err)
	}
	text := string(encode(t, m))
	if text[4:20] != "F020000000008000" {
		t.Errorf("primary = %s", text[4:20])
	}
	if text[20:36] != "0000000004000000" {
		t.Errorf("secondary = %s", text[20:36])
	}
	back, err := Decode([]byte(text), spec)
	if err != nil {
		t.Fatal(err)
	}
	if get(t, back, 102) != "12345678901" {
		t.Error("DE102 lost")
	}
	if amt, _ := back.Amount(); amt != OfMinor(500000, INR) {
		t.Errorf("Amount = %v", amt)
	}
	if plain := string(encode(t, purchase(t, spec))); plain[4] != '7' {
		t.Error("no secondary bitmap expected when all DEs are below 65")
	}
}

func TestLLLVar(t *testing.T) {
	spec := Standard1987()
	data := strings.Repeat("A", 250)
	m, _ := NewMessage("0800", spec)
	must(m.Set(11, "000001"))
	must(m.Set(123, data))
	text := string(encode(t, m))
	if !strings.HasSuffix(text, "250"+data) {
		t.Error("LLLVAR prefix should be three digits")
	}
	back, err := Decode([]byte(text), spec)
	if err != nil {
		t.Fatal(err)
	}
	if get(t, back, 123) != data {
		t.Error("LLLVAR round trip failed")
	}
}

func TestFieldValidationOnSet(t *testing.T) {
	spec := Standard1987()
	m, _ := NewMessage("0200", spec)
	expectMalformed(t, "fixed wrong length", m.Set(3, "00000"))
	expectMalformed(t, "numeric with dot", m.Set(4, "000000019.99"))
	expectMalformed(t, "LLVAR over max", m.Set(2, strings.Repeat("4", 20)))
	expectMalformed(t, "undefined DE", m.Set(64, "DEADBEEF"))
	expectMalformed(t, "empty value", m.Set(41, ""))
	expectMalformed(t, "negative amount", m.SetAmount(OfMinor(-1, USD)))
	if err := m.SetAmount(OfMinor(5, JPY)); err != nil {
		t.Fatal(err)
	}
	if get(t, m, 4) != "000000000005" || get(t, m, 49) != "392" {
		t.Error("SetAmount should zero-pad DE4 and write DE49")
	}
	if amt, _ := m.Amount(); amt.String() != "5 JPY" {
		t.Errorf("JPY amount = %v", amt)
	}
}

func TestMalformedDecode(t *testing.T) {
	spec := Standard1987()
	cases := map[string]string{
		"truncated mid-field":        expectedWire[:60],
		"too short for header":       "0200723804",
		"trailing bytes":             expectedWire + "XYZ",
		"LLVAR length over max":      "0200" + "7238040008C08000" + "99" + expectedWire[22:],
		"non-numeric length prefix":  "0200" + "7238040008C08000" + "1A" + expectedWire[22:],
		"bit set for undefined DE64": "0200" + "7238040008C08001" + expectedWire[20:] + "X",
		"bit 1 without secondary":    "0200" + "F000000000000000",
		"tertiary bitmap":            "0200" + "8000000000000000" + "8000000000000000",
	}
	for name, text := range cases {
		_, err := Decode([]byte(text), spec)
		expectMalformed(t, name, err)
	}
}

func TestMaskedPANAndDump(t *testing.T) {
	req := purchase(t, Standard1987())
	masked, err := req.MaskedPAN()
	if err != nil || masked != "411111******1111" {
		t.Errorf("MaskedPAN = %q, %v", masked, err)
	}
	dump := req.Dump()
	if strings.Contains(dump, TestPAN) || !strings.Contains(dump, "411111******1111") {
		t.Error("dump must mask the PAN")
	}
}

func TestUnknownCurrency(t *testing.T) {
	_, err := CurrencyFromNumeric("999")
	expectMalformed(t, "unknown currency", err)
	if c, _ := CurrencyFromNumeric("840"); c != USD {
		t.Error("840 should be USD")
	}
}
