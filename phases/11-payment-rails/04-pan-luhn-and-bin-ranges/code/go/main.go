// Lesson: phases/11-payment-rails/04-pan-luhn-and-bin-ranges/docs/en.md
// PANs, Luhn, and BIN Range Routing
// Run:  go run .
// Test: go test ./...
// Sources: ISO/IEC 7812-1 (PAN structure, Luhn check digit, Annex B); PCI DSS v4.0 Req. 3.4.1 (masking);
// network public BIN prefix rules (Visa 4; Mastercard 51-55, 2221-2720; Amex 34, 37; Discover 6011, 65, 644-649).
// Go 1.22, standard library only. Only published test PANs appear anywhere in this lesson.
package main

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// ---- Luhn: the mod 10 check-digit algorithm from ISO/IEC 7812-1 Annex B. Works on any digit string. ----

func requireDigits(s string) error {
	if s == "" {
		return errors.New("digit string must not be empty")
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return fmt.Errorf("not a digit at position %d", i)
		}
	}
	return nil
}

// LuhnSum is the Luhn sum of the digits when the rightmost digit is at position 0.
// If rightmostIsCheckDigit is true, doubling starts at position 1; otherwise at position 0.
func LuhnSum(digits string, rightmostIsCheckDigit bool) int {
	total := 0
	dbl := !rightmostIsCheckDigit
	for i := len(digits) - 1; i >= 0; i-- {
		d := int(digits[i] - '0')
		if dbl {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}
		total += d
		dbl = !dbl
	}
	return total
}

// LuhnValid is true when the full number (including its check digit) satisfies sum mod 10 == 0.
func LuhnValid(digits string) (bool, error) {
	if err := requireDigits(digits); err != nil {
		return false, err
	}
	if len(digits) < 2 {
		return false, nil
	}
	return LuhnSum(digits, true)%10 == 0, nil
}

// LuhnCheckDigit is the check digit that makes partial + digit Luhn-valid.
func LuhnCheckDigit(partial string) (int, error) {
	if err := requireDigits(partial); err != nil {
		return 0, err
	}
	return (10 - LuhnSum(partial, false)%10) % 10, nil
}

// LuhnComplete is partial with its check digit appended.
func LuhnComplete(partial string) (string, error) {
	d, err := LuhnCheckDigit(partial)
	if err != nil {
		return "", err
	}
	return partial + strconv.Itoa(d), nil
}

// Network lists the networks this lesson routes to. Real routing tables carry many more
// (JCB, UnionPay, RuPay, domestic debit schemes).
type Network int

const (
	Visa Network = iota
	Mastercard
	Amex
	Discover
	Unknown
)

func (n Network) String() string {
	return [...]string{"VISA", "MASTERCARD", "AMEX", "DISCOVER", "UNKNOWN"}[n]
}

const (
	PanMinLength = 12
	PanMaxLength = 19
)

// Pan is a primary account number: 12 to 19 digits (ISO/IEC 7812-1) that pass the Luhn check. Never logged unmasked:
// the digits live in an unexported field so %v, %s and %+v all print the mask.
type Pan struct {
	digits string
}

// NewPan validates length and Luhn (the Java compact constructor).
func NewPan(digits string) (Pan, error) {
	if len(digits) < PanMinLength || len(digits) > PanMaxLength {
		return Pan{}, fmt.Errorf("PAN must be %d-%d digits, got %d", PanMinLength, PanMaxLength, len(digits))
	}
	valid, err := LuhnValid(digits)
	if err != nil {
		return Pan{}, err
	}
	if !valid {
		return Pan{}, errors.New("PAN fails the Luhn check")
	}
	return Pan{digits}, nil
}

// ParsePan parses a PAN that may contain spaces or hyphens (as typed on a form). Rejects anything else.
func ParsePan(text string) (Pan, error) {
	var sb strings.Builder
	for _, c := range text {
		switch {
		case c >= '0' && c <= '9':
			sb.WriteRune(c)
		case c != ' ' && c != '-':
			return Pan{}, errors.New("PAN contains a non-digit character")
		}
	}
	return NewPan(sb.String())
}

// Digits returns the full PAN. Call it only where the PAN is genuinely needed, never for logging.
func (p Pan) Digits() string { return p.digits }

func (p Pan) Length() int { return len(p.digits) }

// MajorIndustryIdentifier is the first digit. 3 = travel/entertainment, 4 and 5 = banking, 6 = merchandising/banking.
func (p Pan) MajorIndustryIdentifier() int { return int(p.digits[0] - '0') }

// Bin is the issuer identification number of the given length (6 or 8).
func (p Pan) Bin(length int) (string, error) {
	if length != 6 && length != 8 {
		return "", errors.New("BIN length must be 6 or 8")
	}
	return p.digits[:length], nil
}

func (p Pan) LastFour() string { return p.digits[len(p.digits)-4:] }

// MaskedPrefix is PCI DSS masking: the first prefix digits and the last four, everything else '*'. prefix is at most 8.
func (p Pan) MaskedPrefix(prefix int) (string, error) {
	if prefix < 0 || prefix > 8 {
		return "", errors.New("may show at most 8 leading digits")
	}
	if prefix+4 >= len(p.digits) {
		return "", errors.New("mask would reveal the whole PAN")
	}
	return p.digits[:prefix] + strings.Repeat("*", len(p.digits)-4-prefix) + p.LastFour(), nil
}

// Masked is the default mask: six leading digits plus the last four.
func (p Pan) Masked() string {
	m, err := p.MaskedPrefix(6)
	if err != nil {
		panic(err) // impossible for a 12..19-digit PAN
	}
	return m
}

// String is the mask on purpose: a Pan that ends up in a log line is masked.
func (p Pan) String() string { return p.Masked() }

// BinRange is one row of a BIN table: an inclusive range of account-number prefixes, normalized to BinWidth digits,
// and what it maps to. A 6-digit BIN 411111 is stored as [41111100, 41111199]; an 8-digit BIN 41111120 as a single point.
type BinRange struct {
	Low     int64
	High    int64
	Network Network
	Issuer  string
	Product string
}

const (
	BinWidth  = 8
	BinMinKey = int64(0)
	BinMaxKey = int64(99_999_999)
)

func NewBinRange(low, high int64, network Network, issuer, product string) (BinRange, error) {
	if low < BinMinKey || high > BinMaxKey || low > high {
		return BinRange{}, fmt.Errorf("bad range %d-%d", low, high)
	}
	return BinRange{low, high, network, issuer, product}, nil
}

// BinRangeOfPrefix builds a range covering every account number that starts with prefix.
func BinRangeOfPrefix(prefix string, network Network, issuer, product string) (BinRange, error) {
	return BinRangeOfPrefixRange(prefix, prefix, network, issuer, product)
}

// BinRangeOfPrefixRange builds a range from fromPrefix through toPrefix inclusive, both of the same length (e.g. "2221" to "2720").
func BinRangeOfPrefixRange(fromPrefix, toPrefix string, network Network, issuer, product string) (BinRange, error) {
	if len(fromPrefix) != len(toPrefix) {
		return BinRange{}, errors.New("prefix range bounds must have the same length")
	}
	low, err := NormalizeLow(fromPrefix)
	if err != nil {
		return BinRange{}, err
	}
	high, err := NormalizeHigh(toPrefix)
	if err != nil {
		return BinRange{}, err
	}
	return NewBinRange(low, high, network, issuer, product)
}

func NormalizeLow(prefix string) (int64, error)  { return pad(prefix, "0") }
func NormalizeHigh(prefix string) (int64, error) { return pad(prefix, "9") }

func pad(prefix, fill string) (int64, error) {
	if prefix == "" || len(prefix) > BinWidth {
		return 0, fmt.Errorf("prefix must be 1-%d digits", BinWidth)
	}
	if err := requireDigits(prefix); err != nil {
		return 0, err
	}
	return strconv.ParseInt(prefix+strings.Repeat(fill, BinWidth-len(prefix)), 10, 64)
}

// KeyOf is the lookup key of a PAN: its first BinWidth digits as a number.
func KeyOf(pan Pan) int64 {
	key, _ := strconv.ParseInt(pan.digits[:BinWidth], 10, 64) // digits are validated, so this cannot fail
	return key
}

func (r BinRange) Contains(key int64) bool { return r.Low <= key && key <= r.High }

func (r BinRange) Overlaps(other BinRange) bool { return r.Low <= other.High && other.Low <= r.High }

func (r BinRange) String() string {
	return fmt.Sprintf("BinRange[low=%d, high=%d, network=%v, issuer=%s, product=%s]", r.Low, r.High, r.Network, r.Issuer, r.Product)
}

// BinTable is a sorted, non-overlapping set of BinRanges with O(log n) lookup by binary search.
type BinTable struct {
	ranges []BinRange
}

func NewBinTable(input []BinRange) (*BinTable, error) {
	sorted := append([]BinRange(nil), input...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Low != sorted[j].Low {
			return sorted[i].Low < sorted[j].Low
		}
		return sorted[i].High < sorted[j].High
	})
	for i := 1; i < len(sorted); i++ {
		if sorted[i-1].Overlaps(sorted[i]) {
			return nil, fmt.Errorf("overlapping BIN ranges: %v and %v", sorted[i-1], sorted[i])
		}
	}
	return &BinTable{sorted}, nil
}

func (t *BinTable) Size() int { return len(t.ranges) }

// Lookup binary-searches for the last range whose Low <= key, then checks key <= High. The bool is Java's Optional.
func (t *BinTable) Lookup(key int64) (BinRange, bool) {
	lo, hi, candidate := 0, len(t.ranges)-1, -1
	for lo <= hi {
		mid := int(uint(lo+hi) >> 1)
		if t.ranges[mid].Low <= key {
			candidate = mid
			lo = mid + 1
		} else {
			hi = mid - 1
		}
	}
	if candidate >= 0 && t.ranges[candidate].Contains(key) {
		return t.ranges[candidate], true
	}
	return BinRange{}, false
}

func (t *BinTable) LookupPan(pan Pan) (BinRange, bool) { return t.Lookup(KeyOf(pan)) }

func mustRange(r BinRange, err error) BinRange {
	if err != nil {
		panic(err)
	}
	return r
}

// NetworkRanges is the public network prefix rules, as a BinTable. Real tables are licensed from the networks and updated monthly.
func NetworkRanges() *BinTable {
	t, err := NewBinTable([]BinRange{
		mustRange(BinRangeOfPrefix("4", Visa, "network", "Visa")),
		mustRange(BinRangeOfPrefixRange("51", "55", Mastercard, "network", "Mastercard 5-series")),
		mustRange(BinRangeOfPrefixRange("2221", "2720", Mastercard, "network", "Mastercard 2-series")),
		mustRange(BinRangeOfPrefix("34", Amex, "network", "American Express")),
		mustRange(BinRangeOfPrefix("37", Amex, "network", "American Express")),
		mustRange(BinRangeOfPrefix("6011", Discover, "network", "Discover")),
		mustRange(BinRangeOfPrefixRange("644", "649", Discover, "network", "Discover")),
		mustRange(BinRangeOfPrefix("65", Discover, "network", "Discover")),
	})
	if err != nil {
		panic(err)
	}
	return t
}

// DetectNetwork: network detection is a lookup in the network table.
func DetectNetwork(pan Pan) Network {
	if r, ok := NetworkRanges().LookupPan(pan); ok {
		return r.Network
	}
	return Unknown
}

// LengthAllowed lists the lengths each network issues. ISO 7812 allows 12-19; the networks are narrower.
func LengthAllowed(network Network, length int) bool {
	switch network {
	case Visa:
		return length == 13 || length == 16 || length == 19
	case Mastercard:
		return length == 16
	case Amex:
		return length == 15
	case Discover:
		return length == 16 || length == 19
	default:
		return length >= PanMinLength && length <= PanMaxLength
	}
}

// RoutingDecision is what a card-processing front end does with a PAN before it touches an authorization:
// validate, detect, route, mask. IssuerRange is nil when the BIN is unknown (Java's Optional.empty()).
type RoutingDecision struct {
	Pan         Pan
	Network     Network
	IssuerRange *BinRange
	LengthOk    bool
}

func (d RoutingDecision) Describe() string {
	s := fmt.Sprintf("%s  len=%d  %v", d.Pan.Masked(), d.Pan.Length(), d.Network)
	if !d.LengthOk {
		s += " (length not issued by this network)"
	}
	if d.IssuerRange != nil {
		return s + fmt.Sprintf("  issuer=%s product=%s", d.IssuerRange.Issuer, d.IssuerRange.Product)
	}
	return s + "  issuer=unknown BIN"
}

type CardRouter struct {
	issuerTable *BinTable
}

func NewCardRouter(issuerTable *BinTable) *CardRouter { return &CardRouter{issuerTable} }

func (c *CardRouter) Route(rawPan string) (RoutingDecision, error) {
	pan, err := ParsePan(rawPan)
	if err != nil {
		return RoutingDecision{}, err
	}
	network := DetectNetwork(pan)
	var issuer *BinRange
	if r, ok := c.issuerTable.LookupPan(pan); ok {
		issuer = &r
	}
	return RoutingDecision{pan, network, issuer, LengthAllowed(network, pan.Length())}, nil
}

// SampleIssuerTable is a small issuer table made only of well-known test BINs. Issuer names are illustrative.
func SampleIssuerTable() *BinTable {
	t, err := NewBinTable([]BinRange{
		mustRange(BinRangeOfPrefix("411111", Visa, "Test Bank A", "Visa Classic credit")),
		mustRange(BinRangeOfPrefix("424242", Visa, "Test Bank B", "Visa debit")),
		mustRange(BinRangeOfPrefix("55555555", Mastercard, "Test Bank C", "Mastercard World (8-digit BIN)")),
		mustRange(BinRangeOfPrefix("222300", Mastercard, "Test Bank D", "Mastercard 2-series credit")),
		mustRange(BinRangeOfPrefix("378282", Amex, "Amex (test)", "Amex Green")),
		mustRange(BinRangeOfPrefix("601111", Discover, "Discover (test)", "Discover it")),
	})
	if err != nil {
		panic(err)
	}
	return t
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func main() {
	fmt.Println("== Luhn by hand: 4111 1111 1111 111? ==")
	partial := "411111111111111"
	fmt.Printf("sum of weighted digits (no check digit) = %d\n", LuhnSum(partial, false))
	fmt.Printf("check digit = (10 - %d) mod 10 = %d\n", LuhnSum(partial, false)%10, must(LuhnCheckDigit(partial)))
	complete := must(LuhnComplete(partial))
	fmt.Printf("complete PAN = %s  valid=%v\n", complete, must(LuhnValid(complete)))
	fmt.Printf("single-digit typo 4111111111111112 valid=%v\n", must(LuhnValid("4111111111111112")))
	fmt.Printf("adjacent transposition 4242424242424242 -> 4422424242424242 valid=%v\n", must(LuhnValid("4422424242424242")))
	fmt.Printf("09 <-> 90 transposition is the one Luhn misses: 091 valid=%v, 901 valid=%v\n", must(LuhnValid("091")), must(LuhnValid("901")))
	fmt.Println()

	fmt.Printf("== BIN table normalized to %d digits ==\n", BinWidth)
	issuers := SampleIssuerTable()
	fmt.Printf("411111 (6-digit)   -> [%d, %d]\n", must(NormalizeLow("411111")), must(NormalizeHigh("411111")))
	fmt.Printf("55555555 (8-digit) -> [%d, %d]\n", must(NormalizeLow("55555555")), must(NormalizeHigh("55555555")))
	fmt.Printf("2221-2720 (network) -> [%d, %d]\n", must(NormalizeLow("2221")), must(NormalizeHigh("2720")))
	fmt.Printf("%d issuer ranges, %d network ranges, binary search on both\n", issuers.Size(), NetworkRanges().Size())
	fmt.Println()

	fmt.Println("== Routing test PANs ==")
	router := NewCardRouter(issuers)
	for _, raw := range []string{"4111 1111 1111 1111", "4242424242424242", "5555-5555-5555-4444", "2223003122003222",
		"378282246310005", "6011111111111117", "4000056655665556", "6304000000000000"} {
		fmt.Println(must(router.Route(raw)).Describe())
	}
	fmt.Println()
	fmt.Println("== Masking ==")
	amex := must(ParsePan("378282246310005"))
	fmt.Printf("default (6+4): %s   8-digit BIN visible: %s   last four only: %s\n", amex.Masked(), must(amex.MaskedPrefix(8)), must(amex.MaskedPrefix(0)))
	fmt.Printf("toString() is the mask, so a stray log line prints %v\n", amex)
	fmt.Println()
	for _, bad := range []string{"4111111111111112", "1234", "4111 1111 1111 111a"} {
		if _, err := ParsePan(bad); err != nil {
			fmt.Println("rejected:", err)
		} else {
			fmt.Printf("accepted %s (should not happen)\n", bad)
		}
	}
}
