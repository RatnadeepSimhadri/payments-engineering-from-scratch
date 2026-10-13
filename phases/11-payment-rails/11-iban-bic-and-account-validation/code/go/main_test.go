// Tests for phases/11-payment-rails/11-iban-bic-and-account-validation. Run: go test ./...
// Same cases as MainTest.java and test_main.py.
package main

import (
	"strings"
	"testing"
)

// One registry example per supported country; every one must pass structure, length, and mod-97.
var registryExamples = []string{
	"AE070331234567890123456", "AT611904300234573201", "BE68539007547034", "CH9300762011623852957",
	"DE89370400440532013000", "DK5000400440116243", "ES9121000418450200051332", "FI2112345600000785",
	"FR1420041010050500013M02606", "GB82WEST12345698765432", "IE29AIBK93115212345678",
	"IT60X0542811101000000123456", "LU280019400644750000", "NL91ABNA0417164300", "NO9386011117947",
	"PL61109010140000071219812874", "PT50000201231234567890154", "SA0380000000608010167519",
	"SE4550000000058398257466",
}

func mustMod97(t *testing.T, s string) int {
	t.Helper()
	v, err := Mod97(s)
	if err != nil {
		t.Fatalf("Mod97(%q) returned error %v", s, err)
	}
	return v
}

func TestMod97(t *testing.T) {
	if got := mustMod97(t, "1234567890"); got != 1234567890%97 {
		t.Errorf("digits only: got %d", got)
	}
	if got := mustMod97(t, "AZ"); got != 1035%97 {
		t.Errorf("AZ should expand to 1035: got %d", got)
	}
	if got := mustMod97(t, "WEST12345698765432GB82"); got != 1 {
		t.Errorf("rearranged valid IBAN must give 1, got %d", got)
	}
	if _, err := Mod97("12-34"); err == nil {
		t.Error("Mod97 must reject characters outside A-Z 0-9")
	}
}

func TestRegistryExamplesValidAndRoundTrip(t *testing.T) {
	for _, ex := range registryExamples {
		r := ValidateIban(ex)
		if !r.Valid() || len(r.Issues) != 0 {
			t.Errorf("%s should be valid with no issues: %v", ex, r.Issues)
		}
		check, err := CheckDigitsFor(ex[:2], ex[4:])
		if err != nil || check != ex[2:4] {
			t.Errorf("%s: regenerated check digits %q err %v", ex, check, err)
		}
		gen, err := GenerateIban(ex[:2], ex[4:])
		if err != nil || gen.Value() != ex {
			t.Errorf("%s: generate round-trip gave %q err %v", ex, gen.Value(), err)
		}
	}
	for country := range CountryFormats {
		found := false
		for _, ex := range registryExamples {
			if strings.HasPrefix(ex, country) {
				found = true
			}
		}
		if !found {
			t.Errorf("table country %s has no example", country)
		}
	}
}

func TestNormalizeAndPaperForm(t *testing.T) {
	if got := Normalize("gb82 west-1234 5698 7654 32"); got != "GB82WEST12345698765432" {
		t.Errorf("normalize: %q", got)
	}
	if !ValidateIban("gb82 west 1234 5698 7654 32").Valid() {
		t.Error("paper form should validate")
	}
}

func TestParseIbanParts(t *testing.T) {
	gb, err := ParseIban("GB82 WEST 1234 5698 7654 32")
	if err != nil {
		t.Fatal(err)
	}
	if gb.Country != "GB" || gb.CheckDigits != "82" || gb.Bban != "WEST12345698765432" {
		t.Errorf("parts: %+v", gb)
	}
	if gb.BankIdentifier != "WEST" || gb.BranchIdentifier != "123456" {
		t.Errorf("bank/branch: %+v", gb)
	}
	if gb.Formatted() != "GB82 WEST 1234 5698 7654 32" {
		t.Errorf("formatted: %q", gb.Formatted())
	}
	it, _ := ParseIban("IT60X0542811101000000123456")
	if it.BankIdentifier != "05428" || it.BranchIdentifier != "11101" {
		t.Errorf("italian bank identifier must skip the CIN: %+v", it)
	}
	de, _ := ParseIban("DE89370400440532013000")
	if de.BranchIdentifier != "" {
		t.Errorf("country without branch should give empty branch, got %q", de.BranchIdentifier)
	}
}

func TestSingleDigitErrorsDetected(t *testing.T) {
	for _, ex := range registryExamples {
		last := ex[len(ex)-1]
		repl := byte('1')
		if last == '1' {
			repl = '2'
		}
		mutated := ex[:len(ex)-1] + string(repl)
		r := ValidateIban(mutated)
		if !(r.Has(ChecksumFailed) || r.Has(BbanStructure)) {
			t.Errorf("%s: single digit error not detected: %v", mutated, r.Issues)
		}
	}
}

func TestIbanFailureReasons(t *testing.T) {
	transposed := ValidateIban("GB82WEST12345698765423")
	if transposed.Valid() || !transposed.Has(ChecksumFailed) {
		t.Error("adjacent transposition must fail the checksum")
	}
	short := ValidateIban("DE8937040044053201300")
	if len(short.Issues) != 1 || !short.Has(WrongLength) {
		t.Errorf("wrong length should be reported alone: %v", short.Issues)
	}
	unknown := ValidateIban("XX82WEST12345698765432")
	if len(unknown.Issues) != 1 || !unknown.Has(UnknownCountry) {
		t.Errorf("unknown country should be reported alone: %v", unknown.Issues)
	}
	if !ValidateIban("GB82 WEST 1234 5698 7654 3*").Has(InvalidCharacters) {
		t.Error("invalid characters")
	}
	structure := ValidateIban("GB82WEST1234569876543X")
	if !structure.Has(BbanStructure) {
		t.Error("letters where digits expected must be a BBAN_STRUCTURE issue")
	}
	named := false
	for _, i := range structure.Issues {
		if strings.Contains(i.Message, "positions 11-18") {
			named = true
		}
	}
	if !named {
		t.Errorf("structure message must name the positions: %v", structure.Issues)
	}
	for _, bad := range []string{"00", "01", "99"} {
		if !ValidateIban("GB" + bad + "WEST12345698765432").Has(InvalidCheckDigits) {
			t.Errorf("check digits %s are structurally invalid", bad)
		}
	}
	if !ValidateIban("   ").Has(Empty) {
		t.Error("empty")
	}
	if _, err := CheckDigitsFor("DE", "12345"); err == nil {
		t.Error("generate must reject wrong BBAN length")
	}
	if _, err := ParseIban("DE8937040044053201300"); err == nil || !strings.Contains(err.Error(), "WRONG_LENGTH") {
		t.Errorf("parse error must name the reason, got %v", err)
	}
}

func TestBicValidation(t *testing.T) {
	for _, ok := range []string{"DEUTDEFF", "DEUTDEFF500", "NWBKGB2L"} {
		if !ValidateBic(ok).Valid() {
			t.Errorf("%s should be valid", ok)
		}
	}
	deut, err := ParseBic("deutdeff")
	if err != nil {
		t.Fatal(err)
	}
	if deut.Institution != "DEUT" || deut.Country != "DE" || deut.Location != "FF" || deut.Branch != "XXX" {
		t.Errorf("parts: %+v", deut)
	}
	if deut.Bic8() != "DEUTDEFF" || deut.IsTest() {
		t.Errorf("bic8/isTest: %+v", deut)
	}
	testBic := ValidateBic("DEUTDEF0")
	if !testBic.Valid() || len(testBic.Issues) != 1 || testBic.Issues[0].Severity != Warning {
		t.Errorf("test BIC is a warning, not an error: %v", testBic.Issues)
	}
	if b, _ := ParseBic("DEUTDEF0"); !b.IsTest() {
		t.Error("IsTest flag")
	}
	for _, bad := range []string{"DEUTDE", "DEUTDEFFX", "DEUTDEFF5000"} {
		if !ValidateBic(bad).Has(WrongLength) {
			t.Errorf("%s wrong length", bad)
		}
	}
	if !ValidateBic("DEUTZZFF").Has(UnknownCountry) {
		t.Error("unknown country")
	}
	if !ValidateBic("DEUT1EFF").Has(InvalidCharacters) {
		t.Error("digit in country position")
	}
	if !ValidateBic("DEUT.EFF").Has(InvalidCharacters) {
		t.Error("invalid characters")
	}
}

func TestRoutingNumber(t *testing.T) {
	cases := map[string]byte{"02199999": '6', "01199999": '3', "11100002": '5'}
	for in, want := range cases {
		got, err := RoutingCheckDigit(in)
		if err != nil || got != want {
			t.Errorf("check digit %s: got %c err %v", in, got, err)
		}
	}
	for _, rtn := range []string{"021999996", "011999993", "061999998", "121999993"} {
		if !ValidateRoutingNumber(rtn).Valid() {
			t.Errorf("%s should be valid", rtn)
		}
	}
	if !ValidateRoutingNumber("021999969").Has(ChecksumFailed) {
		t.Error("transposition must fail the 3-7-1 checksum")
	}
	prefix := ValidateRoutingNumber("991999990")
	if len(prefix.Issues) != 1 || !prefix.Has(InvalidPrefix) {
		t.Errorf("prefix outside ranges should be reported alone: %v", prefix.Issues)
	}
	if !ValidateRoutingNumber("02199999").Has(WrongLength) {
		t.Error("wrong length")
	}
	if !ValidateRoutingNumber("02199999A").Has(InvalidCharacters) {
		t.Error("invalid characters")
	}
	if !ValidateRoutingNumber("").Has(Empty) {
		t.Error("empty")
	}
	if _, err := RoutingCheckDigit("0219999"); err == nil {
		t.Error("check digit requires eight digits")
	}
}

func TestValidatePair(t *testing.T) {
	pair := ValidatePair("DE89 3704 0044 0532 0130 00", "DEUTDEFF")
	if !pair.Valid() || len(pair.Issues) != 0 {
		t.Errorf("matching pair: %v", pair.Issues)
	}
	mismatch := ValidatePair("DE89 3704 0044 0532 0130 00", "NWBKGB2L")
	if !mismatch.Valid() || !mismatch.Has(CountryMismatch) {
		t.Errorf("country mismatch is a warning: %v", mismatch.Issues)
	}
	bad := ValidatePair("DE8937040044053201300", "DEUTDEFF")
	if bad.Valid() || !bad.Has(WrongLength) || bad.Has(CountryMismatch) {
		t.Errorf("invalid iban propagates without mismatch: %v", bad.Issues)
	}
}
