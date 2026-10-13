// Lesson: phases/11-payment-rails/11-iban-bic-and-account-validation/docs/en.md
// IBAN, BIC, and Account Number Validation
// Run:  go run .
// Test: go test ./...
// Sources: ISO 13616-1 (IBAN) and ISO/IEC 7064 MOD 97-10; ISO 9362 (BIC); SWIFT IBAN Registry for the
// country table; ABA routing transit number 3-7-1 check digit. Go 1.22+, standard library only.
package main

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Severity distinguishes a rejection from an advisory.
type Severity string

const (
	Error   Severity = "ERROR"
	Warning Severity = "WARNING"
)

// Reason is a stable identifier a caller can branch on; messages are for humans.
type Reason string

const (
	Empty              Reason = "EMPTY"
	InvalidCharacters  Reason = "INVALID_CHARACTERS"
	UnknownCountry     Reason = "UNKNOWN_COUNTRY"
	WrongLength        Reason = "WRONG_LENGTH"
	InvalidCheckDigits Reason = "INVALID_CHECK_DIGITS"
	BbanStructure      Reason = "BBAN_STRUCTURE"
	ChecksumFailed     Reason = "CHECKSUM_FAILED"
	InvalidPrefix      Reason = "INVALID_PREFIX"
	TestBic            Reason = "TEST_BIC"
	CountryMismatch    Reason = "COUNTRY_MISMATCH"
)

// Issue is one finding against an identifier.
type Issue struct {
	Reason   Reason
	Severity Severity
	Message  string
}

func (i Issue) String() string { return fmt.Sprintf("%s %s: %s", i.Severity, i.Reason, i.Message) }

// ValidationResult is the outcome of validating one identifier. Valid is true when no ERROR-severity issue exists.
type ValidationResult struct {
	Kind       string
	Normalized string
	Issues     []Issue
}

func (r ValidationResult) Valid() bool {
	for _, i := range r.Issues {
		if i.Severity == Error {
			return false
		}
	}
	return true
}

func (r ValidationResult) Has(reason Reason) bool {
	for _, i := range r.Issues {
		if i.Reason == reason {
			return true
		}
	}
	return false
}

func (r ValidationResult) err() error {
	parts := make([]string, len(r.Issues))
	for k, i := range r.Issues {
		parts[k] = i.String()
	}
	return errors.New(strings.Join(parts, "; "))
}

// Normalize strips whitespace and hyphens and upper-cases. The paper form "GB82 WEST 1234 ..." becomes machine form.
func Normalize(text string) string {
	var sb strings.Builder
	for _, ch := range text {
		switch ch {
		case ' ', '-', '\t', '\r', '\n':
			continue
		}
		sb.WriteString(strings.ToUpper(string(ch)))
	}
	return sb.String()
}

func isDigit(ch byte) bool { return ch >= '0' && ch <= '9' }
func isUpper(ch byte) bool { return ch >= 'A' && ch <= 'Z' }

func isAlnumUpper(s string) bool {
	for i := 0; i < len(s); i++ {
		if !isDigit(s[i]) && !isUpper(s[i]) {
			return false
		}
	}
	return true
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !isDigit(s[i]) {
			return false
		}
	}
	return true
}

func isLetters(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !isUpper(s[i]) {
			return false
		}
	}
	return true
}

// Mod97 implements ISO 7064 MOD 97-10: letters become two-digit numbers (A=10 .. Z=35) and the whole digit string
// is reduced modulo 97. One digit at a time, so the running value never exceeds 97*10+9 and an int64 suffices.
func Mod97(text string) (int, error) {
	var remainder int64
	for i := 0; i < len(text); i++ {
		ch := text[i]
		switch {
		case isDigit(ch):
			remainder = (remainder*10 + int64(ch-'0')) % 97
		case isUpper(ch):
			value := int64(ch-'A') + 10
			remainder = (remainder*10 + value/10) % 97
			remainder = (remainder*10 + value%10) % 97
		default:
			return 0, fmt.Errorf("mod97 input must be A-Z or 0-9, got %q", ch)
		}
	}
	return int(remainder), nil
}

// Group is one run of characters in a BBAN: count and character class n (digits), a (upper letters), c (either).
type Group struct {
	Count     int
	CharClass byte
}

func (g Group) Matches(segment string) bool {
	if len(segment) != g.Count {
		return false
	}
	for i := 0; i < len(segment); i++ {
		ch := segment[i]
		switch g.CharClass {
		case 'n':
			if !isDigit(ch) {
				return false
			}
		case 'a':
			if !isUpper(ch) {
				return false
			}
		default:
			if !isDigit(ch) && !isUpper(ch) {
				return false
			}
		}
	}
	return true
}

// CountryFormat is one row of the SWIFT IBAN Registry subset.
type CountryFormat struct {
	Country        string
	Length         int
	Groups         []Group
	BankIDStart    int
	BankIDLength   int
	BranchIDStart  int
	BranchIDLength int
}

func (f CountryFormat) BbanLength() int {
	n := 0
	for _, g := range f.Groups {
		n += g.Count
	}
	return n
}

func (f CountryFormat) HasBranch() bool { return f.BranchIDLength > 0 }

func newFormat(country string, length int, pattern string, bank [2]int, branch [2]int) CountryFormat {
	var groups []Group
	for _, token := range strings.Fields(pattern) {
		count, err := strconv.Atoi(token[:len(token)-1])
		if err != nil {
			panic(err)
		}
		groups = append(groups, Group{count, token[len(token)-1]})
	}
	f := CountryFormat{country, length, groups, bank[0], bank[1], branch[0], branch[1]}
	if f.BbanLength()+4 != length {
		panic("bad table entry " + country)
	}
	return f
}

var none = [2]int{0, 0}

// CountryFormats is the subset of the registry this lesson supports, keyed by ISO 3166-1 alpha-2 code.
var CountryFormats = func() map[string]CountryFormat {
	m := map[string]CountryFormat{}
	for _, f := range []CountryFormat{
		newFormat("AE", 23, "3n 16n", [2]int{0, 3}, none),
		newFormat("AT", 20, "5n 11n", [2]int{0, 5}, none),
		newFormat("BE", 16, "3n 7n 2n", [2]int{0, 3}, none),
		newFormat("CH", 21, "5n 12c", [2]int{0, 5}, none),
		newFormat("DE", 22, "8n 10n", [2]int{0, 8}, none),
		newFormat("DK", 18, "4n 9n 1n", [2]int{0, 4}, none),
		newFormat("ES", 24, "4n 4n 1n 1n 10n", [2]int{0, 4}, [2]int{4, 4}),
		newFormat("FI", 18, "3n 11n", [2]int{0, 3}, none),
		newFormat("FR", 27, "5n 5n 11c 2n", [2]int{0, 5}, [2]int{5, 5}),
		newFormat("GB", 22, "4a 6n 8n", [2]int{0, 4}, [2]int{4, 6}),
		newFormat("IE", 22, "4a 6n 8n", [2]int{0, 4}, [2]int{4, 6}),
		newFormat("IT", 27, "1a 5n 5n 12c", [2]int{1, 5}, [2]int{6, 5}),
		newFormat("LU", 20, "3n 13c", [2]int{0, 3}, none),
		newFormat("NL", 18, "4a 10n", [2]int{0, 4}, none),
		newFormat("NO", 15, "4n 6n 1n", [2]int{0, 4}, none),
		newFormat("PL", 28, "8n 16n", [2]int{0, 8}, none),
		newFormat("PT", 25, "4n 4n 11n 2n", [2]int{0, 4}, [2]int{4, 4}),
		newFormat("SA", 24, "2n 18c", [2]int{0, 2}, none),
		newFormat("SE", 24, "3n 16n 1n", [2]int{0, 3}, none),
	} {
		m[f.Country] = f
	}
	return m
}()

// KnownCountries are the ISO 3166-1 codes a BIC may carry: the IBAN countries plus common non-IBAN jurisdictions.
var KnownCountries = func() map[string]bool {
	m := map[string]bool{}
	for c := range CountryFormats {
		m[c] = true
	}
	for _, c := range []string{"US", "CA", "JP", "AU", "SG", "HK", "IN", "CN", "BR", "MX", "ZA", "NZ", "KR"} {
		m[c] = true
	}
	return m
}()

// Iban is a validated IBAN decomposed into its parts. Construct through ParseIban, never directly.
type Iban struct {
	Country          string
	CheckDigits      string
	Bban             string
	BankIdentifier   string
	BranchIdentifier string // empty when the country format has no branch field
}

func (i Iban) Value() string { return i.Country + i.CheckDigits + i.Bban }

// Formatted is the paper form: groups of four separated by spaces (ISO 13616-1, section 6).
func (i Iban) Formatted() string {
	v := i.Value()
	var parts []string
	for k := 0; k < len(v); k += 4 {
		end := k + 4
		if end > len(v) {
			end = len(v)
		}
		parts = append(parts, v[k:end])
	}
	return strings.Join(parts, " ")
}

// ValidateIban returns every structured reason the text is not an IBAN; an empty Issues slice means valid.
func ValidateIban(text string) ValidationResult {
	s := Normalize(text)
	r := ValidationResult{Kind: "IBAN", Normalized: s}
	if s == "" {
		r.Issues = append(r.Issues, Issue{Empty, Error, "empty IBAN"})
		return r
	}
	if !isAlnumUpper(s) {
		r.Issues = append(r.Issues, Issue{InvalidCharacters, Error, "IBAN may contain only A-Z and 0-9"})
		return r
	}
	if len(s) < 4 || !isLetters(s[:2]) {
		r.Issues = append(r.Issues, Issue{UnknownCountry, Error, fmt.Sprintf("no country code in %q", s)})
		return r
	}
	country := s[:2]
	fmtRow, ok := CountryFormats[country]
	if !ok {
		r.Issues = append(r.Issues, Issue{UnknownCountry, Error, country + " is not in the IBAN registry subset"})
		return r
	}
	if len(s) != fmtRow.Length {
		r.Issues = append(r.Issues, Issue{WrongLength, Error, fmt.Sprintf("%s IBAN must be %d characters, got %d", country, fmtRow.Length, len(s))})
		return r
	}
	check := s[2:4]
	checkIsDigits := isDigits(check)
	if !checkIsDigits || check == "00" || check == "01" || check == "99" {
		r.Issues = append(r.Issues, Issue{InvalidCheckDigits, Error, fmt.Sprintf("check digits %q are structurally invalid", check)})
	}
	bban := s[4:]
	pos := 0
	for _, g := range fmtRow.Groups {
		end := pos + g.Count
		if end > len(bban) {
			end = len(bban)
		}
		segment := bban[pos:end]
		if !g.Matches(segment) {
			r.Issues = append(r.Issues, Issue{BbanStructure, Error, fmt.Sprintf("BBAN positions %d-%d must be %d%c, got %q", pos+1, pos+g.Count, g.Count, g.CharClass, segment)})
			break
		}
		pos += g.Count
	}
	if checkIsDigits {
		if rem, _ := Mod97(bban + country + check); rem != 1 {
			r.Issues = append(r.Issues, Issue{ChecksumFailed, Error, "mod-97 remainder is not 1; a digit is wrong or transposed"})
		}
	}
	return r
}

// ParseIban validates and decomposes; the error message lists every reason.
func ParseIban(text string) (Iban, error) {
	r := ValidateIban(text)
	if !r.Valid() {
		return Iban{}, r.err()
	}
	s := r.Normalized
	f := CountryFormats[s[:2]]
	bban := s[4:]
	branch := ""
	if f.HasBranch() {
		branch = bban[f.BranchIDStart : f.BranchIDStart+f.BranchIDLength]
	}
	return Iban{s[:2], s[2:4], bban, bban[f.BankIDStart : f.BankIDStart+f.BankIDLength], branch}, nil
}

// CheckDigitsFor implements ISO 13616-1 generation: set the check digits to 00, compute 98 - mod97, zero-pad.
func CheckDigitsFor(country, bban string) (string, error) {
	country = strings.ToUpper(country)
	bban = Normalize(bban)
	f, ok := CountryFormats[country]
	if !ok {
		return "", fmt.Errorf("unknown country %s", country)
	}
	if len(bban) != f.BbanLength() {
		return "", fmt.Errorf("%s BBAN must be %d characters, got %d", country, f.BbanLength(), len(bban))
	}
	rem, err := Mod97(bban + country + "00")
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%02d", 98-rem), nil
}

// GenerateIban builds a full IBAN from a country and BBAN.
func GenerateIban(country, bban string) (Iban, error) {
	check, err := CheckDigitsFor(country, bban)
	if err != nil {
		return Iban{}, err
	}
	return ParseIban(strings.ToUpper(country) + check + Normalize(bban))
}

// Bic is an ISO 9362 Business Identifier Code: 4 party prefix, 2 country, 2 location, optional 3 branch.
type Bic struct {
	Institution string
	Country     string
	Location    string
	Branch      string // "XXX" for the primary office
}

func (b Bic) Value() string { return b.Institution + b.Country + b.Location + b.Branch }
func (b Bic) Bic8() string  { return b.Institution + b.Country + b.Location }
func (b Bic) IsTest() bool  { return b.Location[1] == '0' }

// ValidateBic returns every structured reason the text is not a BIC.
func ValidateBic(text string) ValidationResult {
	s := Normalize(text)
	r := ValidationResult{Kind: "BIC", Normalized: s}
	if s == "" {
		r.Issues = append(r.Issues, Issue{Empty, Error, "empty BIC"})
		return r
	}
	if len(s) != 8 && len(s) != 11 {
		r.Issues = append(r.Issues, Issue{WrongLength, Error, fmt.Sprintf("BIC must be 8 or 11 characters, got %d", len(s))})
		return r
	}
	if !isAlnumUpper(s) {
		r.Issues = append(r.Issues, Issue{InvalidCharacters, Error, "BIC may contain only A-Z and 0-9"})
		return r
	}
	country := s[4:6]
	if !isLetters(country) {
		r.Issues = append(r.Issues, Issue{InvalidCharacters, Error, fmt.Sprintf("country code %q must be two letters", country)})
	} else if !KnownCountries[country] {
		r.Issues = append(r.Issues, Issue{UnknownCountry, Error, country + " is not a known ISO 3166-1 country"})
	}
	if s[7] == '0' {
		r.Issues = append(r.Issues, Issue{TestBic, Warning, "location code ending in 0 denotes a test BIC"})
	}
	return r
}

// ParseBic validates and decomposes a BIC, filling the branch with XXX for an 8-character code.
func ParseBic(text string) (Bic, error) {
	r := ValidateBic(text)
	if !r.Valid() {
		return Bic{}, r.err()
	}
	s := r.Normalized
	branch := "XXX"
	if len(s) == 11 {
		branch = s[8:]
	}
	return Bic{s[:4], s[4:6], s[6:8], branch}, nil
}

// AbaWeights are the 3-7-1 weights of the ABA routing transit number check digit.
var AbaWeights = [9]int{3, 7, 1, 3, 7, 1, 3, 7, 1}

// RoutingCheckDigit returns the ninth digit for the first eight.
func RoutingCheckDigit(firstEight string) (byte, error) {
	if len(firstEight) != 8 || !isDigits(firstEight) {
		return 0, errors.New("need exactly eight digits")
	}
	total := 0
	for i := 0; i < 8; i++ {
		total += AbaWeights[i] * int(firstEight[i]-'0')
	}
	return byte('0' + (10-total%10)%10), nil
}

// RoutingPrefixIsValid: Federal Reserve routing symbol ranges 00-12 banks, 21-32 thrifts, 61-72 electronic, 80 traveler's cheques.
func RoutingPrefixIsValid(prefix int) bool {
	return (prefix >= 0 && prefix <= 12) || (prefix >= 21 && prefix <= 32) || (prefix >= 61 && prefix <= 72) || prefix == 80
}

// ValidateRoutingNumber returns every structured reason the text is not an ABA routing number.
func ValidateRoutingNumber(text string) ValidationResult {
	s := Normalize(text)
	r := ValidationResult{Kind: "ABA", Normalized: s}
	if s == "" {
		r.Issues = append(r.Issues, Issue{Empty, Error, "empty routing number"})
		return r
	}
	if !isDigits(s) {
		r.Issues = append(r.Issues, Issue{InvalidCharacters, Error, "routing number must be digits"})
		return r
	}
	if len(s) != 9 {
		r.Issues = append(r.Issues, Issue{WrongLength, Error, fmt.Sprintf("routing number must be 9 digits, got %d", len(s))})
		return r
	}
	prefix, _ := strconv.Atoi(s[:2])
	if !RoutingPrefixIsValid(prefix) {
		r.Issues = append(r.Issues, Issue{InvalidPrefix, Error, "prefix " + s[:2] + " is not a Federal Reserve routing symbol"})
	}
	total := 0
	for i := 0; i < 9; i++ {
		total += AbaWeights[i] * int(s[i]-'0')
	}
	if total%10 != 0 {
		r.Issues = append(r.Issues, Issue{ChecksumFailed, Error, fmt.Sprintf("3-7-1 weighted sum %d is not a multiple of 10", total)})
	}
	return r
}

// ValidatePair validates an IBAN and BIC together, as a payment system receives them on a payout instruction.
func ValidatePair(ibanText, bicText string) ValidationResult {
	iban := ValidateIban(ibanText)
	bic := ValidateBic(bicText)
	issues := append([]Issue{}, iban.Issues...)
	issues = append(issues, bic.Issues...)
	if iban.Valid() && bic.Valid() && iban.Normalized[:2] != bic.Normalized[4:6] {
		issues = append(issues, Issue{CountryMismatch, Warning, fmt.Sprintf("IBAN country %s differs from BIC country %s", iban.Normalized[:2], bic.Normalized[4:6])})
	}
	return ValidationResult{"IBAN+BIC", iban.Normalized + "/" + bic.Normalized, issues}
}

func show(r ValidationResult) {
	status := "valid"
	if !r.Valid() {
		status = "INVALID"
	}
	fmt.Printf("  %-8s %-32s %s\n", r.Kind, r.Normalized, status)
	for _, i := range r.Issues {
		fmt.Println("           - " + i.String())
	}
}

func main() {
	fmt.Println("== Mod-97 by hand: GB82 WEST 1234 5698 7654 32 ==")
	iban, err := ParseIban("GB82 WEST 1234 5698 7654 32")
	if err != nil {
		panic(err)
	}
	rearranged := iban.Bban + iban.Country + iban.CheckDigits
	rem, _ := Mod97(rearranged)
	fmt.Println("  rearranged      = " + rearranged)
	fmt.Printf("  mod 97          = %d (must be 1)\n", rem)
	fmt.Printf("  bank identifier = %s, branch (sort code) = %s\n", iban.BankIdentifier, iban.BranchIdentifier)
	check, _ := CheckDigitsFor("GB", iban.Bban)
	fmt.Println("  check digits regenerated from BBAN = " + check)

	fmt.Println()
	fmt.Println("== What mod-97 catches ==")
	for _, m := range [][2]string{
		{"one digit changed  ", "GB82WEST12345698765431"},
		{"adjacent transposed", "GB82WEST12345698765423"},
		{"two groups swapped ", "GB82WEST56981234765432"},
	} {
		verdict := "rejected"
		if ValidateIban(m[1]).Valid() {
			verdict = "valid"
		}
		fmt.Printf("  %s %s: %s\n", m[0], m[1], verdict)
	}

	fmt.Println()
	fmt.Println("== Structured failures ==")
	for _, sample := range []string{
		"DE89 3704 0044 0532 0130 00",
		"FR14 2004 1010 0505 0001 3M02 606",
		"DE89 3704 0044 0532 0130 0",
		"GB82 WEST 1234 5698 7654 3X",
		"GB00 WEST 1234 5698 7654 32",
		"XX82 WEST 1234 5698 7654 32",
		"gb82-west-1234-5698-7654-32",
	} {
		show(ValidateIban(sample))
	}

	fmt.Println()
	fmt.Println("== BIC (ISO 9362) ==")
	for _, sample := range []string{"DEUTDEFF", "DEUTDEFF500", "NWBKGB2L", "DEUTDEF0", "DEUTDE", "DEUTZZFF"} {
		show(ValidateBic(sample))
	}

	fmt.Println()
	fmt.Println("== ABA routing numbers (fictitious prefixes, real check digits) ==")
	for _, sample := range []string{"021999996", "011999993", "021999969", "991999990", "02199999"} {
		show(ValidateRoutingNumber(sample))
	}

	fmt.Println()
	fmt.Println("== Payout instruction: IBAN + BIC together ==")
	show(ValidatePair("DE89 3704 0044 0532 0130 00", "DEUTDEFF"))
	show(ValidatePair("DE89 3704 0044 0532 0130 00", "NWBKGB2L"))

	fmt.Println()
	fmt.Println("== Generating an IBAN from a BBAN ==")
	generated, err := GenerateIban("NL", "ABNA0417164300")
	if err != nil {
		panic(err)
	}
	fmt.Println("  NL + ABNA0417164300 -> " + generated.Formatted())
}
