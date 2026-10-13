// Lesson: phases/02-algorithms/15-numerical-algorithms/docs/en.md
// Numerical Algorithms: GCD, Modular Arithmetic, and Check Digits
// Run:  go run .
// Test: go test ./...
// Sources: ISO/IEC 7812-1 (Luhn, Annex B); ISO 7064:2003 (MOD 97-10); ISO 13616-1 (IBAN); Damm, "Totally
// anti-symmetric quasigroups" (2004); Knuth TAOCP vol. 2 sec. 4.5.2 (Euclid).
// Go 1.22+, standard library only. Same design as the Java and Python versions.
package main

import (
	"errors"
	"fmt"
	"strings"
)

// -----------------------------------------------------------------------------------------------------------
// Euclid and modular arithmetic
// -----------------------------------------------------------------------------------------------------------

func abs64(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}

// Gcd is gcd(a, b) with gcd(0, 0) = 0 and the result always non-negative. O(log min(a, b)) divisions.
func Gcd(a, b int64) int64 {
	a, b = abs64(a), abs64(b)
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

func Lcm(a, b int64) int64 {
	if a == 0 || b == 0 {
		return 0
	}
	return abs64(a / Gcd(a, b) * b)
}

// Bezout is g = gcd(a, b) together with x, y such that a*x + b*y = g.
type Bezout struct{ G, X, Y int64 }

// ExtendedGcd carries the coefficient pairs alongside the remainders.
func ExtendedGcd(a, b int64) Bezout {
	oldR, r := a, b
	oldX, x := int64(1), int64(0)
	oldY, y := int64(0), int64(1)
	for r != 0 {
		q := oldR / r
		oldR, r = r, oldR-q*r
		oldX, x = x, oldX-q*x
		oldY, y = y, oldY-q*y
	}
	if oldR < 0 {
		return Bezout{-oldR, -oldX, -oldY}
	}
	return Bezout{oldR, oldX, oldY}
}

// Mod returns a mod m in 0..m-1 regardless of the sign of a (Go's % keeps the dividend's sign).
func Mod(a, m int64) int64 {
	r := a % m
	if r < 0 {
		r += m
	}
	return r
}

// ErrNoInverse is returned when a and m are not coprime.
var ErrNoInverse = errors.New("no modular inverse")

// ModInverse is the x in 0..m-1 with a*x = 1 (mod m); exists iff gcd(a, m) = 1.
func ModInverse(a, m int64) (int64, error) {
	if m <= 1 {
		return 0, errors.New("modulus must be > 1")
	}
	b := ExtendedGcd(Mod(a, m), m)
	if b.G != 1 {
		return 0, fmt.Errorf("%w: %d modulo %d (gcd = %d)", ErrNoInverse, a, m, b.G)
	}
	return Mod(b.X, m), nil
}

// ModPow is base^exp mod m by repeated squaring, O(log exp) multiplications; exp must be non-negative.
func ModPow(base, exp, m int64) (int64, error) {
	if exp < 0 {
		return 0, errors.New("exponent must be non-negative")
	}
	result := int64(1) % m
	b := Mod(base, m)
	for exp > 0 {
		if exp&1 == 1 {
			result = result * b % m
		}
		b = b * b % m
		exp >>= 1
	}
	return result, nil
}

// ModOfDecimalString is the remainder of an arbitrarily long decimal string by Horner's rule:
// r = (r*10 + digit) mod m. Never forms the big number, so a 30-digit IBAN numeric needs no math/big.
func ModOfDecimalString(digits string, m int64) (int64, error) {
	if digits == "" {
		return 0, errors.New("empty digit string")
	}
	var r int64
	for i := 0; i < len(digits); i++ {
		c := digits[i]
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("not a digit at %d: %c", i, c)
		}
		r = (r*10 + int64(c-'0')) % m
	}
	return r, nil
}

// -----------------------------------------------------------------------------------------------------------
// Check digits
// -----------------------------------------------------------------------------------------------------------

// Validator accepts or rejects a full identifier (payload plus check).
type Validator func(string) bool

func allDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func luhnWeightedSum(digits string, doubleFirst bool) (int, error) {
	sum := 0
	dbl := doubleFirst
	for i := len(digits) - 1; i >= 0; i-- {
		c := digits[i]
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("not a digit at %d: %c", i, c)
		}
		d := int(c - '0')
		if dbl {
			d *= 2
			if d > 9 {
				d -= 9 // digit sum of a two-digit product: 16 -> 1 + 6 = 7 = 16 - 9
			}
		}
		sum += d
		dbl = !dbl
	}
	return sum, nil
}

// LuhnCheckDigit is the digit c such that payload + c validates (ISO/IEC 7812-1 Annex B).
func LuhnCheckDigit(payload string) (int, error) {
	if payload == "" {
		return 0, errors.New("empty payload")
	}
	sum, err := luhnWeightedSum(payload, true)
	if err != nil {
		return 0, err
	}
	return (10 - sum%10) % 10, nil
}

func LuhnValidate(pan string) bool {
	if len(pan) < 2 || !allDigits(pan) {
		return false
	}
	sum, _ := luhnWeightedSum(pan, false)
	return sum%10 == 0
}

// IbanToNumeric moves the first four characters to the end, then replaces each letter with two digits (A=10 .. Z=35).
func IbanToNumeric(iban string) (string, error) {
	s := strings.ToUpper(strings.ReplaceAll(iban, " ", ""))
	if len(s) < 5 {
		return "", fmt.Errorf("IBAN too short: %s", iban)
	}
	var out strings.Builder
	for _, c := range s[4:] + s[:4] {
		switch {
		case c >= '0' && c <= '9':
			out.WriteRune(c)
		case c >= 'A' && c <= 'Z':
			out.WriteString(fmt.Sprintf("%d", int(c-'A')+10))
		default:
			return "", fmt.Errorf("invalid IBAN character: %c", c)
		}
	}
	return out.String(), nil
}

func ValidateIban(iban string) bool {
	numeric, err := IbanToNumeric(iban)
	if err != nil {
		return false
	}
	r, err := ModOfDecimalString(numeric, 97)
	return err == nil && r == 1
}

// IbanCheckDigits: compute with "00" in the check position, then 98 - remainder, zero-padded to two digits.
func IbanCheckDigits(countryCode, bban string) (string, error) {
	numeric, err := IbanToNumeric(countryCode + "00" + bban)
	if err != nil {
		return "", err
	}
	r, err := ModOfDecimalString(numeric, 97)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%02d", 98-r), nil
}

func BuildIban(countryCode, bban string) (string, error) {
	check, err := IbanCheckDigits(countryCode, bban)
	if err != nil {
		return "", err
	}
	return countryCode + check + bban, nil
}

// DammTable is Damm's totally anti-symmetric quasigroup of order 10 (2004).
var DammTable = [10][10]int{
	{0, 3, 1, 7, 5, 9, 8, 6, 4, 2},
	{7, 0, 9, 2, 1, 5, 4, 8, 6, 3},
	{4, 2, 0, 6, 8, 7, 1, 3, 5, 9},
	{1, 7, 5, 0, 9, 8, 3, 4, 2, 6},
	{6, 1, 2, 3, 0, 4, 5, 9, 7, 8},
	{3, 6, 7, 4, 2, 0, 9, 5, 8, 1},
	{5, 8, 6, 9, 7, 2, 0, 1, 3, 4},
	{8, 9, 4, 5, 3, 6, 2, 0, 1, 7},
	{9, 4, 3, 8, 6, 1, 7, 2, 0, 5},
	{2, 5, 8, 1, 4, 3, 6, 7, 9, 0},
}

func dammInterim(digits string) (int, error) {
	state := 0
	for i := 0; i < len(digits); i++ {
		c := digits[i]
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("not a digit at %d: %c", i, c)
		}
		state = DammTable[state][c-'0']
	}
	return state, nil
}

// DammCheckDigit is the final interim value; appending it drives the state to 0 because Table[s][s] = 0.
func DammCheckDigit(payload string) (int, error) {
	if payload == "" {
		return 0, errors.New("empty payload")
	}
	return dammInterim(payload)
}

func DammValidate(identifier string) bool {
	if identifier == "" || !allDigits(identifier) {
		return false
	}
	state, _ := dammInterim(identifier)
	return state == 0
}

// -----------------------------------------------------------------------------------------------------------
// Error analysis
// -----------------------------------------------------------------------------------------------------------

// ErrorClass is a kind of keying error a check digit is supposed to catch.
type ErrorClass int

const (
	SingleDigit           ErrorClass = iota // 123 -> 173
	AdjacentTransposition                   // 123 -> 213
	Twin                                    // 122 -> 155
	JumpTransposition                       // 123 -> 321
)

var AllErrorClasses = []ErrorClass{SingleDigit, AdjacentTransposition, Twin, JumpTransposition}

func (c ErrorClass) String() string {
	return [...]string{"SINGLE_DIGIT", "ADJACENT_TRANSPOSITION", "TWIN", "JUMP_TRANSPOSITION"}[c]
}

// Report counts how many corruptions of one class the validator rejected.
type Report struct {
	Class  ErrorClass
	Tried  int
	Caught int
	Missed []string
}

func (r Report) MissedCount() int { return r.Tried - r.Caught }
func (r Report) Rate() string {
	if r.Tried == 0 {
		return "n/a"
	}
	return fmt.Sprintf("%d/%d", r.Caught, r.Tried)
}

func isDigit(b byte) bool { return b >= '0' && b <= '9' }

// Corruptions lists every corruption of the given class on valid (digit positions only; letters in an IBAN are left alone).
func Corruptions(valid string, cls ErrorClass) []string {
	out := []string{}
	c := []byte(valid)
	n := len(c)
	switch cls {
	case SingleDigit:
		for i := 0; i < n; i++ {
			if !isDigit(c[i]) {
				continue
			}
			for d := byte('0'); d <= '9'; d++ {
				if d == c[i] {
					continue
				}
				m := append([]byte(nil), c...)
				m[i] = d
				out = append(out, string(m))
			}
		}
	case AdjacentTransposition:
		for i := 0; i+1 < n; i++ {
			if !isDigit(c[i]) || !isDigit(c[i+1]) || c[i] == c[i+1] {
				continue
			}
			m := append([]byte(nil), c...)
			m[i], m[i+1] = c[i+1], c[i]
			out = append(out, string(m))
		}
	case Twin:
		for i := 0; i+1 < n; i++ {
			if !isDigit(c[i]) || c[i] != c[i+1] {
				continue
			}
			for d := byte('0'); d <= '9'; d++ {
				if d == c[i] {
					continue
				}
				m := append([]byte(nil), c...)
				m[i], m[i+1] = d, d
				out = append(out, string(m))
			}
		}
	case JumpTransposition:
		for i := 0; i+2 < n; i++ {
			if !isDigit(c[i]) || !isDigit(c[i+2]) || c[i] == c[i+2] {
				continue
			}
			m := append([]byte(nil), c...)
			m[i], m[i+2] = c[i+2], c[i]
			out = append(out, string(m))
		}
	}
	return out
}

func Analyse(v Validator, valid string, cls ErrorClass) (Report, error) {
	if !v(valid) {
		return Report{}, fmt.Errorf("the sample must validate before it is corrupted: %s", valid)
	}
	r := Report{Class: cls, Missed: []string{}}
	for _, bad := range Corruptions(valid, cls) {
		r.Tried++
		if v(bad) {
			r.Missed = append(r.Missed, bad)
		} else {
			r.Caught++
		}
	}
	return r, nil
}

// SeededRandom is a tiny 64-bit LCG, identical in all three languages, so the random payloads are the same everywhere.
type SeededRandom struct{ state uint64 }

func NewSeededRandom(seed int64) *SeededRandom { return &SeededRandom{uint64(seed)} }

func (r *SeededRandom) NextInt(bound int) int {
	r.state = r.state*6364136223846793005 + 1442695040888963407
	return int((r.state >> 33) % uint64(bound))
}

// Aggregate sums (tried, caught) over many seeded random payloads so a single lucky sample cannot hide a weakness.
func Aggregate(v Validator, complete func(string) string, samples, payloadLength int, seed int64) (map[ErrorClass][2]int, error) {
	rng := NewSeededRandom(seed)
	totals := map[ErrorClass][2]int{}
	for s := 0; s < samples; s++ {
		var payload strings.Builder
		for i := 0; i < payloadLength; i++ {
			payload.WriteByte(byte('0' + rng.NextInt(10)))
		}
		valid := complete(payload.String())
		for _, cls := range AllErrorClasses {
			r, err := Analyse(v, valid, cls)
			if err != nil {
				return nil, err
			}
			t := totals[cls]
			t[0] += r.Tried
			t[1] += r.Caught
			totals[cls] = t
		}
	}
	return totals, nil
}

const (
	PanPayload = "453909012223456"        // 15 digits; contains "90", "09", and "22" on purpose
	IbanSample = "DE89370400440532013000" // the ISO 13616 worked example
)

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func pct(t [2]int) string {
	return fmt.Sprintf("%d/%d (%.2f%%)", t[1], t[0], 100.0*float64(t[1])/float64(t[0]))
}

func main() {
	fmt.Println("== Euclid: gcd, Bezout, inverse ==")
	fmt.Printf("gcd(1071, 462) = %d\n", Gcd(1071, 462))
	bz := ExtendedGcd(240, 46)
	fmt.Printf("240 * (%d) + 46 * (%d) = %d\n", bz.X, bz.Y, bz.G)
	g := Gcd(29, 1000)
	fmt.Printf("29/1000 reduced by gcd: %d/%d\n", 29/g, 1000/g)
	fmt.Printf("denominations {6, 10} reach amount 15? gcd=%d divides 15? %v\n", Gcd(6, 10), 15%Gcd(6, 10) == 0)
	inv := must(ModInverse(100, 97))
	fmt.Printf("inverse of 100 mod 97 = %d  (100 * %d mod 97 = %d)\n", inv, inv, 100*inv%97)
	if _, err := ModInverse(6, 10); err != nil {
		fmt.Println("rejected:", err)
	}
	fmt.Printf("3^200 mod 97 = %d (by repeated squaring); 10^30 mod 97 = %d\n", must(ModPow(3, 200, 97)), must(ModPow(10, 30, 97)))

	fmt.Println("\n== Luhn (ISO/IEC 7812) ==")
	check := must(LuhnCheckDigit(PanPayload))
	pan := fmt.Sprintf("%s%d", PanPayload, check)
	fmt.Printf("payload %s -> check digit %d -> PAN %s valid=%v\n", PanPayload, check, pan, LuhnValidate(pan))
	fmt.Printf("4539 1488 0343 6467 (a test PAN) valid=%v; with one digit changed valid=%v\n",
		LuhnValidate("4539148803436467"), LuhnValidate("4539148803436461"))

	fmt.Println("\n== ISO 7064 MOD 97-10 (IBAN) ==")
	numeric := must(IbanToNumeric(IbanSample))
	fmt.Printf("%s -> numeric %s\n", IbanSample, numeric)
	fmt.Printf("mod 97 = %d  valid=%v\n", must(ModOfDecimalString(numeric, 97)), ValidateIban(IbanSample))
	fmt.Printf("check digits recomputed for DE + 370400440532013000 = %s\n", must(IbanCheckDigits("DE", "370400440532013000")))
	fmt.Printf("GB82 WEST 1234 5698 7654 32 valid=%v; typo 5689 valid=%v\n",
		ValidateIban("GB82 WEST 1234 5698 7654 32"), ValidateIban("GB82 WEST 1234 5689 7654 32"))

	fmt.Println("\n== Damm (quasigroup) ==")
	dammId := fmt.Sprintf("%s%d", PanPayload, must(DammCheckDigit(PanPayload)))
	fmt.Printf("572 -> check digit %d; %s valid=%v\n", must(DammCheckDigit("572")), dammId, DammValidate(dammId))

	fmt.Println("\n== Which errors does each scheme catch? Exhaustive on one sample ==")
	fmt.Printf("%-24s %-10s %-10s %-10s\n", "error class", "Luhn", "MOD 97-10", "Damm")
	for _, cls := range AllErrorClasses {
		fmt.Printf("%-24s %-10s %-10s %-10s\n", cls,
			must(Analyse(LuhnValidate, pan, cls)).Rate(),
			must(Analyse(ValidateIban, IbanSample, cls)).Rate(),
			must(Analyse(DammValidate, dammId, cls)).Rate())
	}
	fmt.Printf("Luhn misses (adjacent): %v\n", must(Analyse(LuhnValidate, pan, AdjacentTransposition)).Missed)
	fmt.Printf("Luhn misses (twin)    : %v\n", must(Analyse(LuhnValidate, pan, Twin)).Missed)

	fmt.Println("\n== Aggregate over 200 seeded random 15-digit payloads (seed 7) ==")
	l := must(Aggregate(LuhnValidate, func(p string) string { return fmt.Sprintf("%s%d", p, must(LuhnCheckDigit(p))) }, 200, 15, 7))
	d := must(Aggregate(DammValidate, func(p string) string { return fmt.Sprintf("%s%d", p, must(DammCheckDigit(p))) }, 200, 15, 7))
	m := must(Aggregate(ValidateIban, func(p string) string { return must(BuildIban("XX", p)) }, 200, 15, 7))
	fmt.Printf("%-24s %-22s %-22s %-22s\n", "error class", "Luhn", "MOD 97-10", "Damm")
	for _, cls := range AllErrorClasses {
		fmt.Printf("%-24s %-22s %-22s %-22s\n", cls, pct(l[cls]), pct(m[cls]), pct(d[cls]))
	}
}
