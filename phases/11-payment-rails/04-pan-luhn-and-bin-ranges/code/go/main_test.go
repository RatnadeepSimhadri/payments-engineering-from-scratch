// Tests for phases/11-payment-rails/04-pan-luhn-and-bin-ranges. Run: go test ./...
package main

import (
	"fmt"
	"strings"
	"testing"
)

func ok[T any](t *testing.T) func(T, error) T {
	return func(v T, err error) T {
		t.Helper()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		return v
	}
}

func valid(t *testing.T, digits string) bool {
	t.Helper()
	return ok[bool](t)(LuhnValid(digits))
}

func pan(t *testing.T, text string) Pan {
	t.Helper()
	return ok[Pan](t)(ParsePan(text))
}

func prefix(t *testing.T, p string, n Network, issuer, product string) BinRange {
	t.Helper()
	return ok[BinRange](t)(BinRangeOfPrefix(p, n, issuer, product))
}

func TestLuhnPublishedNumbers(t *testing.T) {
	for _, digits := range []string{"4111111111111111", "4242424242424242", "5555555555554444", "2223003122003222",
		"378282246310005", "6011111111111117", "79927398713"} {
		if !valid(t, digits) {
			t.Errorf("luhn %s should be valid", digits)
		}
	}
}

func TestLuhnCatchesAndMisses(t *testing.T) {
	if valid(t, "4111111111111112") {
		t.Error("luhn catches a single-digit error")
	}
	if valid(t, "4422424242424242") {
		t.Error("luhn catches an adjacent transposition 42 -> 24")
	}
	if !valid(t, "091") || !valid(t, "901") {
		t.Error("luhn misses the 09 <-> 90 transposition")
	}
	if ok[int](t)(LuhnCheckDigit("7992739871")) != 3 {
		t.Error("luhn check digit for 7992739871 is 3")
	}
	if ok[int](t)(LuhnCheckDigit("411111111111111")) != 1 {
		t.Error("luhn check digit for 411111111111111 is 1")
	}
	if ok[string](t)(LuhnComplete("411111111111111")) != "4111111111111111" {
		t.Error("luhn complete appends the digit")
	}
	rnd := NewJavaRandom(42)
	for range 1000 {
		n := 11 + int(rnd.NextInt(8))
		var sb strings.Builder
		for range n {
			sb.WriteByte(byte('0' + rnd.NextInt(10)))
		}
		if !valid(t, ok[string](t)(LuhnComplete(sb.String()))) {
			t.Fatalf("luhn complete is always valid: %s", sb.String())
		}
	}
	if ok[int](t)(LuhnCheckDigit("0")) != 0 {
		t.Error("luhn check digit when sum is a multiple of 10 is 0")
	}
	if _, err := LuhnValid("4111-1111"); err == nil {
		t.Error("luhn rejects non-digits")
	}
	if _, err := LuhnValid(""); err == nil {
		t.Error("luhn rejects empty")
	}
}

func TestPanParsingAndMasking(t *testing.T) {
	visa := pan(t, "4111 1111 1111 1111")
	if visa.Digits() != "4111111111111111" {
		t.Error("parse strips spaces")
	}
	if pan(t, "5555-5555-5555-4444").Length() != 16 {
		t.Error("parse strips hyphens")
	}
	if visa.Masked() != "411111******1111" {
		t.Error("mask default is 6 + 4")
	}
	if ok[string](t)(visa.MaskedPrefix(8)) != "41111111****1111" {
		t.Error("mask with 8-digit BIN")
	}
	if ok[string](t)(visa.MaskedPrefix(0)) != "************1111" {
		t.Error("mask with last four only")
	}
	if visa.String() != "411111******1111" || fmt.Sprintf("%v %s %+v", visa, visa, visa) != strings.Repeat("411111******1111 ", 3)[:50] {
		t.Error("String and the fmt verbs v, s and +v are all masked")
	}
	if len(pan(t, "378282246310005").Masked()) != 15 {
		t.Error("amex mask has 15 characters")
	}
	if ok[string](t)(visa.Bin(6)) != "411111" || ok[string](t)(visa.Bin(8)) != "41111111" {
		t.Error("bin(6) and bin(8)")
	}
	if pan(t, "378282246310005").MajorIndustryIdentifier() != 3 {
		t.Error("MII of an Amex is 3")
	}
	for name, text := range map[string]string{
		"reject Luhn failure": "4111111111111112",
		"reject too short":    "79927398713",
		"reject 20 digits":    ok[string](t)(LuhnComplete("4000000000000000000")),
		"reject letters":      "4111 1111 1111 111a",
	} {
		if _, err := ParsePan(text); err == nil {
			t.Error(name)
		}
	}
	if _, err := visa.MaskedPrefix(9); err == nil {
		t.Error("mask may not reveal more than 8 leading digits")
	}
	if _, err := ok[Pan](t)(NewPan(ok[string](t)(LuhnComplete("40000000000")))).MaskedPrefix(8); err == nil {
		t.Error("mask may not reveal the whole 12-digit PAN")
	}
}

func TestBinRangeNormalization(t *testing.T) {
	if ok[int64](t)(NormalizeLow("411111")) != 41111100 {
		t.Error("6-digit BIN low bound")
	}
	if ok[int64](t)(NormalizeHigh("411111")) != 41111199 {
		t.Error("6-digit BIN high bound")
	}
	if ok[int64](t)(NormalizeLow("55555555")) != ok[int64](t)(NormalizeHigh("55555555")) {
		t.Error("8-digit BIN is a single point")
	}
	mc := ok[BinRange](t)(BinRangeOfPrefixRange("2221", "2720", Mastercard, "n", "p"))
	if mc.Low != 22210000 || mc.High != 27209999 {
		t.Error("network prefix range 2221-2720")
	}
	if _, err := BinRangeOfPrefixRange("51", "555", Mastercard, "n", "p"); err == nil {
		t.Error("prefix range bounds must be the same length")
	}
	if _, err := NewBinTable([]BinRange{prefix(t, "4111", Visa, "a", "p"), prefix(t, "411111", Visa, "b", "p")}); err == nil {
		t.Error("overlapping ranges are rejected")
	}
}

func TestBinarySearchLookup(t *testing.T) {
	issuers := SampleIssuerTable()
	visa := pan(t, "4111 1111 1111 1111")
	if r, found := issuers.LookupPan(visa); !found || r.Issuer != "Test Bank A" {
		t.Error("lookup 6-digit BIN hit")
	}
	if r, found := issuers.LookupPan(pan(t, "5555555555554444")); !found || !strings.Contains(r.Product, "8-digit") {
		t.Error("lookup 8-digit BIN hit")
	}
	for name, key := range map[string]int64{
		"lookup 8-digit BIN miss one digit off (above)": 55555556_00,
		"lookup 8-digit BIN miss one digit off (below)": 55555554*100 + 99,
		"lookup below the first range is empty":         0,
		"lookup above the last range is empty":          BinMaxKey,
		"lookup in a gap between ranges is empty":       41111200,
	} {
		if _, found := issuers.Lookup(key); found {
			t.Error(name)
		}
	}
	if _, found := issuers.Lookup(41111100); !found {
		t.Error("lookup at exact low bound")
	}
	if _, found := issuers.Lookup(41111199); !found {
		t.Error("lookup at exact high bound")
	}
	// binary search agrees with linear scan on 10k random keys, half of them near a boundary
	rnd := NewJavaRandom(7)
	all := []BinRange{
		prefix(t, "411111", Visa, "Test Bank A", "x"), prefix(t, "424242", Visa, "Test Bank B", "x"),
		prefix(t, "55555555", Mastercard, "Test Bank C", "x"), prefix(t, "222300", Mastercard, "Test Bank D", "x"),
		prefix(t, "378282", Amex, "Amex (test)", "x"), prefix(t, "601111", Discover, "Discover (test)", "x"),
	}
	for i := range 10_000 {
		var key int64
		if i%2 == 0 {
			key = rnd.NextLongBounded(BinMaxKey + 1)
		} else {
			key = 41111000 + int64(rnd.NextInt(400))
		}
		linear := ""
		for _, r := range all {
			if r.Contains(key) {
				linear = r.Issuer
				break
			}
		}
		binary := ""
		if r, found := issuers.Lookup(key); found {
			binary = r.Issuer
		}
		if linear != binary {
			t.Fatalf("key %d: linear %q vs binary %q", key, linear, binary)
		}
	}
}

func TestNetworkDetection(t *testing.T) {
	cases := []struct {
		name string
		pan  string
		want Network
	}{
		{"visa by 4", "4111111111111111", Visa},
		{"mastercard 5-series", "5555555555554444", Mastercard},
		{"mastercard 2-series", "2223003122003222", Mastercard},
		{"amex 37", "378282246310005", Amex},
		{"amex 34", "340000000000009", Amex},
		{"discover 6011", "6011111111111117", Discover},
		{"discover 65", "6500000000000002", Discover},
		{"unknown prefix", ok[string](t)(LuhnComplete("630400000000000")), Unknown},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := DetectNetwork(pan(t, c.pan)); got != c.want {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}
	if r, found := NetworkRanges().Lookup(27209999); !found || r.Network != Mastercard {
		t.Error("2720 is still mastercard")
	}
	if _, found := NetworkRanges().Lookup(27210000); found {
		t.Error("2721 is not mastercard")
	}
	if _, found := NetworkRanges().Lookup(22209999); found {
		t.Error("2220 is not mastercard")
	}
}

func TestNetworkLengthRules(t *testing.T) {
	if !LengthAllowed(Visa, 16) {
		t.Error("visa 16 ok")
	}
	if !LengthAllowed(Visa, 13) || LengthAllowed(Visa, 15) {
		t.Error("visa 13 ok, 15 not")
	}
	if !LengthAllowed(Amex, 15) || LengthAllowed(Amex, 16) {
		t.Error("amex is 15 only")
	}
	if LengthAllowed(Mastercard, 19) {
		t.Error("mastercard is 16 only")
	}
}

func TestEndToEndRouting(t *testing.T) {
	router := NewCardRouter(SampleIssuerTable())
	d := ok[RoutingDecision](t)(router.Route("2223 0031 2200 3222"))
	if d.Network != Mastercard || d.IssuerRange == nil || !d.LengthOk {
		t.Error("route: network, issuer, length")
	}
	u := ok[RoutingDecision](t)(router.Route("4000056655665556"))
	if u.IssuerRange != nil || u.Network != Visa {
		t.Error("route: unknown issuer BIN still detects network")
	}
	if strings.Contains(d.Describe(), "2223003122003222") || !strings.HasPrefix(d.Describe(), "222300******3222") {
		t.Error("route: describe is masked")
	}
	if _, err := router.Route("4111111111111112"); err == nil {
		t.Error("route rejects an invalid PAN")
	}
}
