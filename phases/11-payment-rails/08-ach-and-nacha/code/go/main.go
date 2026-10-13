// Lesson: phases/11-payment-rails/08-ach-and-nacha/docs/en.md
// ACH and the NACHA File Format
// Run:  go run .
// Test: go test ./...
// Sources: Nacha Operating Rules & Guidelines, Appendix Three (ACH record formats); ABA Routing Number
// Policy (check-digit weights 3-7-1); Nacha Appendix Four (return reason codes).
// Go 1.22+, standard library only. Same design as the Java and Python versions.
package main

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Money (the subset of Phase 10, Lesson 01 that ACH needs)
// ---------------------------------------------------------------------------

// Currency is an ISO 4217 currency. ACH moves USD only, but the type stays honest.
type Currency struct {
	Code     string
	Exponent int
}

var USD = Currency{"USD", 2}

// MinorUnitsPerMajor is 10^exponent.
func (c Currency) MinorUnitsPerMajor() int64 {
	scale := int64(1)
	for i := 0; i < c.Exponent; i++ {
		scale *= 10
	}
	return scale
}

// Money is an exact amount in minor units of one currency.
type Money struct {
	MinorUnits int64
	Currency   Currency
}

func OfMinor(minor int64, c Currency) Money { return Money{minor, c} }

// ParseMoney never touches a float: split at the dot, validate digits, pad, combine.
func ParseMoney(text string, c Currency) (Money, error) {
	s := strings.TrimSpace(text)
	negative := strings.HasPrefix(s, "-")
	if negative || strings.HasPrefix(s, "+") {
		s = s[1:]
	}
	if s == "" {
		return Money{}, errors.New("empty amount")
	}
	whole, frac := s, ""
	if dot := strings.IndexByte(s, '.'); dot >= 0 {
		whole, frac = s[:dot], s[dot+1:]
	}
	if whole == "" {
		whole = "0"
	}
	if !allDigits(whole) || (frac != "" && !allDigits(frac)) {
		return Money{}, fmt.Errorf("not a decimal amount: %q", text)
	}
	if len(frac) > c.Exponent {
		return Money{}, fmt.Errorf("%s has more decimals than %s allows", text, c.Code)
	}
	frac += strings.Repeat("0", c.Exponent-len(frac))
	w, _ := strconv.ParseInt(whole, 10, 64)
	minor := w * c.MinorUnitsPerMajor()
	if frac != "" {
		f, _ := strconv.ParseInt(frac, 10, 64)
		minor += f
	}
	if negative {
		minor = -minor
	}
	return Money{minor, c}, nil
}

func (m Money) Plus(other Money) Money {
	if m.Currency != other.Currency {
		panic("cannot combine " + m.Currency.Code + " with " + other.Currency.Code)
	}
	return Money{m.MinorUnits + other.MinorUnits, m.Currency}
}

func (m Money) IsNegative() bool { return m.MinorUnits < 0 }
func (m Money) IsZero() bool     { return m.MinorUnits == 0 }

func (m Money) String() string {
	magnitude := m.MinorUnits
	if magnitude < 0 {
		magnitude = -magnitude
	}
	scale := m.Currency.MinorUnitsPerMajor()
	result := strconv.FormatInt(magnitude/scale, 10)
	if m.Currency.Exponent > 0 {
		frac := strconv.FormatInt(magnitude%scale, 10)
		result += "." + strings.Repeat("0", m.Currency.Exponent-len(frac)) + frac
	}
	if m.MinorUnits < 0 {
		result = "-" + result
	}
	return result + " " + m.Currency.Code
}

// NachaFormatError: a line, a field, or a control total does not satisfy the Nacha file format.
type NachaFormatError struct{ Msg string }

func (e *NachaFormatError) Error() string { return e.Msg }

func formatErr(format string, args ...any) error {
	return &NachaFormatError{fmt.Sprintf(format, args...)}
}

// IsFormatError reports whether err is a NachaFormatError (possibly wrapped).
func IsFormatError(err error) bool {
	var fe *NachaFormatError
	return errors.As(err, &fe)
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// Routing numbers, SEC codes, transaction codes
// ---------------------------------------------------------------------------

var routingWeights = [8]int{3, 7, 1, 3, 7, 1, 3, 7}

// RoutingCheckDigit is the ninth digit that makes the weighted sum of all nine digits a multiple of ten.
func RoutingCheckDigit(first8 string) (int, error) {
	if len(first8) != 8 || !allDigits(first8) {
		return 0, formatErr("routing prefix must be 8 digits: %q", first8)
	}
	sum := 0
	for i := 0; i < 8; i++ {
		sum += routingWeights[i] * int(first8[i]-'0')
	}
	return (10 - sum%10) % 10, nil
}

// RoutingIsValid checks shape and the ABA mod-10 check digit.
func RoutingIsValid(routing string) bool {
	if len(routing) != 9 || !allDigits(routing) {
		return false
	}
	cd, err := RoutingCheckDigit(routing[:8])
	return err == nil && cd == int(routing[8]-'0')
}

func requireRouting(routing string) error {
	if !RoutingIsValid(routing) {
		return formatErr("invalid routing number (check digit): %s", routing)
	}
	return nil
}

// SecCode is the Standard Entry Class: which rulebook chapter governs the batch.
type SecCode string

const (
	PPD SecCode = "PPD" // Prearranged Payment and Deposit: consumer, written/standing authorization
	CCD SecCode = "CCD" // Corporate Credit or Debit: business-to-business
	WEB SecCode = "WEB" // Internet-Initiated: consumer debit authorized online
)

func parseSecCode(s string) (SecCode, error) {
	switch SecCode(s) {
	case PPD, CCD, WEB:
		return SecCode(s), nil
	}
	return "", formatErr("unknown SEC code: %q", s)
}

// TransactionCode: account type (2x checking, 3x savings) and direction (credit, debit, prenote, return).
type TransactionCode int

const (
	CheckingReturnCredit  TransactionCode = 21
	CheckingCredit        TransactionCode = 22
	CheckingPrenoteCredit TransactionCode = 23
	CheckingReturnDebit   TransactionCode = 26
	CheckingDebit         TransactionCode = 27
	CheckingPrenoteDebit  TransactionCode = 28
	SavingsReturnCredit   TransactionCode = 31
	SavingsCredit         TransactionCode = 32
	SavingsPrenoteCredit  TransactionCode = 33
	SavingsReturnDebit    TransactionCode = 36
	SavingsDebit          TransactionCode = 37
	SavingsPrenoteDebit   TransactionCode = 38
)

var knownTransactionCodes = []TransactionCode{21, 22, 23, 26, 27, 28, 31, 32, 33, 36, 37, 38}

func (t TransactionCode) IsCredit() bool  { d := int(t) % 10; return d >= 1 && d <= 4 }
func (t TransactionCode) IsDebit() bool   { d := int(t) % 10; return d >= 6 && d <= 9 }
func (t TransactionCode) IsPrenote() bool { d := int(t) % 10; return d == 3 || d == 8 }
func (t TransactionCode) IsReturn() bool  { d := int(t) % 10; return d == 1 || d == 6 }

func TransactionCodeFrom(code int) (TransactionCode, error) {
	for _, k := range knownTransactionCodes {
		if int(k) == code {
			return k, nil
		}
	}
	return 0, formatErr("unknown transaction code: %d", code)
}

// ---------------------------------------------------------------------------
// Fixed-width helpers. Nacha positions are 1-based and inclusive; so are these.
// ---------------------------------------------------------------------------

// Alpha: left-justify, space-fill, truncate, uppercase.
func Alpha(value string, width int) string {
	s := strings.ToUpper(value)
	if len(s) > width {
		s = s[:width]
	}
	return s + strings.Repeat(" ", width-len(s))
}

// Numeric: right-justify, zero-fill; a value that does not fit is an error, never truncated.
func Numeric(value int64, width int) (string, error) {
	if value < 0 {
		return "", formatErr("numeric field cannot be negative: %d", value)
	}
	s := strconv.FormatInt(value, 10)
	if len(s) > width {
		return "", formatErr("value %d does not fit in %d digits", value, width)
	}
	return strings.Repeat("0", width-len(s)) + s, nil
}

// mustNumeric is for values already validated by a constructor (sequence numbers, counts).
func mustNumeric(value int64, width int) string {
	s, err := Numeric(value, width)
	if err != nil {
		panic(err)
	}
	return s
}

// Field returns characters start..end inclusive, 1-based, exactly as the Nacha layout tables number them.
func Field(line string, start, end int) string { return line[start-1 : end] }

func Number(line string, start, end int) (int64, error) {
	s := Field(line, start, end)
	if !allDigits(s) {
		return 0, formatErr("expected digits at %d-%d: %q", start, end, s)
	}
	return strconv.ParseInt(s, 10, 64)
}

func requireMatch(what string, declared, computed any) error {
	if declared != computed {
		return formatErr("%s mismatch: declared %v, computed %v", what, declared, computed)
	}
	return nil
}

func require94(line string) error {
	if len(line) != 94 {
		return formatErr("record must be 94 characters, got %d: %q", len(line), line)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Records
// ---------------------------------------------------------------------------

// FileHeader is record type 1. One per file.
type FileHeader struct {
	ImmediateDestination string
	ImmediateOrigin      string
	Created              time.Time
	FileIDModifier       byte
	DestinationName      string
	OriginName           string
	ReferenceCode        string
}

func NewFileHeader(destination, origin string, created time.Time, modifier byte, destinationName, originName, reference string) (FileHeader, error) {
	if err := requireRouting(destination); err != nil {
		return FileHeader{}, err
	}
	if len(origin) != 9 && len(origin) != 10 {
		return FileHeader{}, formatErr("immediate origin must be 9 or 10 characters")
	}
	if !((modifier >= 'A' && modifier <= 'Z') || (modifier >= '0' && modifier <= '9')) {
		return FileHeader{}, formatErr("file ID modifier must be A-Z or 0-9")
	}
	return FileHeader{destination, origin, created, modifier, destinationName, originName, reference}, nil
}

// FileHeaderOf builds a header with the clock injected so demos and tests are deterministic.
func FileHeaderOf(destination, origin string, now func() time.Time, destinationName, originName string) (FileHeader, error) {
	return NewFileHeader(destination, origin, now(), 'A', destinationName, originName, "")
}

func pad10(routing string) string {
	if len(routing) == 9 {
		return " " + routing
	}
	return routing
}

func (h FileHeader) ToLine() string {
	line := "1" + "01" + Alpha(pad10(h.ImmediateDestination), 10) + Alpha(pad10(h.ImmediateOrigin), 10) +
		h.Created.Format("060102") + h.Created.Format("1504") + string(h.FileIDModifier) + "094" + "10" + "1" +
		Alpha(h.DestinationName, 23) + Alpha(h.OriginName, 23) + Alpha(h.ReferenceCode, 8)
	if err := require94(line); err != nil {
		panic(err)
	}
	return line
}

func ParseFileHeader(line string) (FileHeader, error) {
	if err := require94(line); err != nil {
		return FileHeader{}, err
	}
	if line[0] != '1' {
		return FileHeader{}, formatErr("not a file header: %q", line)
	}
	if Field(line, 35, 37) != "094" || Field(line, 38, 39) != "10" || line[39] != '1' {
		return FileHeader{}, formatErr("file header must declare record size 094, blocking factor 10, format code 1")
	}
	created, err := time.Parse("0601021504", Field(line, 24, 29)+Field(line, 30, 33))
	if err != nil {
		return FileHeader{}, formatErr("bad file creation date/time: %v", err)
	}
	return NewFileHeader(strings.TrimSpace(Field(line, 4, 13)), strings.TrimSpace(Field(line, 14, 23)), created, line[33],
		strings.TrimSpace(Field(line, 41, 63)), strings.TrimSpace(Field(line, 64, 86)), strings.TrimSpace(Field(line, 87, 94)))
}

// Service class codes.
const (
	Mixed       = 200
	CreditsOnly = 220
	DebitsOnly  = 225
)

// BatchHeader is record type 5. One per batch.
type BatchHeader struct {
	ServiceClassCode         int
	CompanyName              string
	CompanyDiscretionaryData string
	CompanyID                string
	SecCode                  SecCode
	EntryDescription         string
	DescriptiveDate          string
	EffectiveEntryDate       string
	OdfiID                   string
	BatchNumber              int
}

func NewBatchHeader(serviceClass int, companyName, discretionary, companyID string, sec SecCode, description, descriptiveDate, effectiveDate, odfiID string, batchNumber int) (BatchHeader, error) {
	if serviceClass != Mixed && serviceClass != CreditsOnly && serviceClass != DebitsOnly {
		return BatchHeader{}, formatErr("service class code must be 200, 220, or 225: %d", serviceClass)
	}
	if len(odfiID) != 8 || !allDigits(odfiID) {
		return BatchHeader{}, formatErr("ODFI id must be 8 digits: %q", odfiID)
	}
	if len(effectiveDate) != 6 {
		return BatchHeader{}, formatErr("effective entry date must be yyMMdd")
	}
	if batchNumber < 1 {
		return BatchHeader{}, formatErr("batch number starts at 1")
	}
	return BatchHeader{serviceClass, companyName, discretionary, companyID, sec, description, descriptiveDate, effectiveDate, odfiID, batchNumber}, nil
}

func (h BatchHeader) ToLine() string {
	line := "5" + mustNumeric(int64(h.ServiceClassCode), 3) + Alpha(h.CompanyName, 16) + Alpha(h.CompanyDiscretionaryData, 20) +
		Alpha(h.CompanyID, 10) + string(h.SecCode) + Alpha(h.EntryDescription, 10) + Alpha(h.DescriptiveDate, 6) + h.EffectiveEntryDate +
		"   " /* settlement date: filled by the ACH Operator */ + "1" /* originator status */ +
		h.OdfiID + mustNumeric(int64(h.BatchNumber), 7)
	if err := require94(line); err != nil {
		panic(err)
	}
	return line
}

func ParseBatchHeader(line string) (BatchHeader, error) {
	if err := require94(line); err != nil {
		return BatchHeader{}, err
	}
	if line[0] != '5' {
		return BatchHeader{}, formatErr("not a batch header: %q", line)
	}
	sc, err := Number(line, 2, 4)
	if err != nil {
		return BatchHeader{}, err
	}
	sec, err := parseSecCode(Field(line, 51, 53))
	if err != nil {
		return BatchHeader{}, err
	}
	bn, err := Number(line, 88, 94)
	if err != nil {
		return BatchHeader{}, err
	}
	return NewBatchHeader(int(sc), strings.TrimSpace(Field(line, 5, 20)), strings.TrimSpace(Field(line, 21, 40)),
		strings.TrimSpace(Field(line, 41, 50)), sec, strings.TrimSpace(Field(line, 54, 63)), strings.TrimSpace(Field(line, 64, 69)),
		Field(line, 70, 75), Field(line, 80, 87), int(bn))
}

// ReturnReason is a Nacha Appendix Four return reason, the subset a payments team sees most.
// ExtendedWindow: sixty calendar days (unauthorized consumer debit) instead of two banking days.
// Representable: the Originator may re-present the entry, at most twice (R01 and R09 only).
type ReturnReason struct {
	Code           string
	Description    string
	ExtendedWindow bool
	Representable  bool
}

var ReturnReasons = []ReturnReason{
	{"R01", "Insufficient funds", false, true},
	{"R02", "Account closed", false, false},
	{"R03", "No account / unable to locate account", false, false},
	{"R04", "Invalid account number structure", false, false},
	{"R05", "Unauthorized debit to consumer account using corporate SEC code", true, false},
	{"R07", "Authorization revoked by customer", true, false},
	{"R08", "Payment stopped", false, false},
	{"R09", "Uncollected funds", false, true},
	{"R10", "Customer advises originator is not known to receiver and/or not authorized", true, false},
	{"R11", "Customer advises entry not in accordance with the terms of the authorization", true, false},
	{"R16", "Account frozen", false, false},
	{"R20", "Non-transaction account", false, false},
	{"R29", "Corporate customer advises not authorized", false, false},
}

func LookupReturnReason(code string) (ReturnReason, error) {
	for _, r := range ReturnReasons {
		if r.Code == code {
			return r, nil
		}
	}
	return ReturnReason{}, formatErr("unknown return reason code: %q", code)
}

func IsKnownReturnReason(code string) bool {
	_, err := LookupReturnReason(code)
	return err == nil
}

// WindowDays is 2 banking days or 60 calendar days.
func (r ReturnReason) WindowDays() int {
	if r.ExtendedWindow {
		return 60
	}
	return 2
}

// Deadline is the last date the RDFI may transmit the return. Banking days skip Sat/Sun; Fed holidays are out of scope.
func (r ReturnReason) Deadline(settlement time.Time) time.Time {
	if r.ExtendedWindow {
		return settlement.AddDate(0, 0, 60)
	}
	d, remaining := settlement, 2
	for remaining > 0 {
		d = d.AddDate(0, 0, 1)
		if d.Weekday() != time.Saturday && d.Weekday() != time.Sunday {
			remaining--
		}
	}
	return d
}

// IsTimely reports whether a return sent on returnDate is on or before the deadline.
func (r ReturnReason) IsTimely(settlement, returnDate time.Time) bool {
	return !returnDate.After(r.Deadline(settlement))
}

// Addenda is record type 7: payment-related (05, free text) or return (99, reason code + original trace).
type Addenda struct {
	TypeCode      string
	ReasonCode    string
	OriginalTrace string
	OriginalDfi   string
	Info          string
}

func NewAddenda(typeCode, reasonCode, originalTrace, originalDfi, info string) (Addenda, error) {
	if typeCode != "05" && typeCode != "99" {
		return Addenda{}, formatErr("addenda type must be 05 or 99: %q", typeCode)
	}
	if typeCode == "99" {
		if len(reasonCode) != 3 {
			return Addenda{}, formatErr("return reason code must be 3 characters: %q", reasonCode)
		}
		if len(originalTrace) != 15 {
			return Addenda{}, formatErr("original trace must be 15 digits")
		}
		if len(originalDfi) != 8 {
			return Addenda{}, formatErr("original DFI must be 8 digits")
		}
	}
	return Addenda{typeCode, reasonCode, originalTrace, originalDfi, info}, nil
}

func PaymentAddenda(info string) Addenda { return Addenda{"05", "", "", "", info} }

func ReturnAddenda(reasonCode, originalTrace, originalDfi, info string) (Addenda, error) {
	return NewAddenda("99", reasonCode, originalTrace, originalDfi, info)
}

func (a Addenda) IsReturn() bool { return a.TypeCode == "99" }

// Reason is the Appendix Four entry for this return; only meaningful on a type-99 addenda.
func (a Addenda) Reason() (ReturnReason, error) {
	if !a.IsReturn() {
		return ReturnReason{}, formatErr("not a return addenda")
	}
	return LookupReturnReason(a.ReasonCode)
}

func (a Addenda) ToLine(entrySequence int, traceNumber string) string {
	var line string
	if a.TypeCode == "05" {
		line = "7" + "05" + Alpha(a.Info, 80) + mustNumeric(1, 4) + mustNumeric(int64(entrySequence), 7)
	} else {
		line = "7" + "99" + Alpha(a.ReasonCode, 3) + a.OriginalTrace + Alpha("", 6) /* date of death */ +
			a.OriginalDfi + Alpha(a.Info, 44) + traceNumber
	}
	if err := require94(line); err != nil {
		panic(err)
	}
	return line
}

func ParseAddenda(line string) (Addenda, error) {
	if err := require94(line); err != nil {
		return Addenda{}, err
	}
	if line[0] != '7' {
		return Addenda{}, formatErr("not an addenda record: %q", line)
	}
	if Field(line, 2, 3) == "05" {
		return PaymentAddenda(strings.TrimSpace(Field(line, 4, 83))), nil
	}
	return ReturnAddenda(Field(line, 4, 6), Field(line, 7, 21), Field(line, 28, 35), strings.TrimSpace(Field(line, 36, 79)))
}

const maxAmountCents = 9_999_999_999

// EntryDetail is record type 6. One per payment.
type EntryDetail struct {
	TransactionCode   TransactionCode
	ReceivingDfi      string
	AccountNumber     string
	Amount            Money
	IndividualID      string
	IndividualName    string
	DiscretionaryData string
	Addenda           *Addenda
	TraceNumber       string
}

func NewEntryDetail(code TransactionCode, receivingDfi, account string, amount Money, individualID, name, discretionary string, addenda *Addenda, trace string) (EntryDetail, error) {
	if err := requireRouting(receivingDfi); err != nil {
		return EntryDetail{}, err
	}
	if strings.TrimSpace(account) == "" || len(account) > 17 {
		return EntryDetail{}, formatErr("account number must be 1-17 characters")
	}
	if amount.Currency != USD {
		return EntryDetail{}, formatErr("ACH carries USD only")
	}
	if amount.IsNegative() {
		return EntryDetail{}, formatErr("amount must not be negative; direction is the transaction code")
	}
	if amount.MinorUnits > maxAmountCents {
		return EntryDetail{}, formatErr("amount exceeds 10 digits of cents")
	}
	if code.IsPrenote() && !amount.IsZero() {
		return EntryDetail{}, formatErr("prenote entries must carry a zero amount")
	}
	if trace != "" && len(trace) != 15 {
		return EntryDetail{}, formatErr("trace number must be 15 digits")
	}
	return EntryDetail{code, receivingDfi, account, amount, individualID, name, discretionary, addenda, trace}, nil
}

// EntryOf is the common constructor: no discretionary data, no addenda, trace assigned by the batch.
func EntryOf(code TransactionCode, receivingDfi, account string, amount Money, individualID, name string) (EntryDetail, error) {
	return NewEntryDetail(code, receivingDfi, account, amount, individualID, name, "", nil, "")
}

func (e EntryDetail) WithAddenda(a Addenda) EntryDetail {
	e.Addenda = &a
	return e
}

func (e EntryDetail) WithTrace(odfiID string, sequence int) EntryDetail {
	e.TraceNumber = odfiID + mustNumeric(int64(sequence), 7)
	return e
}

// ReceivingDfiID is the first eight digits: what the entry hash sums. The check digit is excluded on purpose.
func (e EntryDetail) ReceivingDfiID() string { return e.ReceivingDfi[:8] }

func (e EntryDetail) RecordCount() int {
	if e.Addenda == nil {
		return 1
	}
	return 2
}

func (e EntryDetail) ToLine() string {
	indicator := "0"
	if e.Addenda != nil {
		indicator = "1"
	}
	line := "6" + mustNumeric(int64(e.TransactionCode), 2) + e.ReceivingDfi + Alpha(e.AccountNumber, 17) +
		mustNumeric(e.Amount.MinorUnits, 10) + Alpha(e.IndividualID, 15) + Alpha(e.IndividualName, 22) +
		Alpha(e.DiscretionaryData, 2) + indicator + e.TraceNumber
	if err := require94(line); err != nil {
		panic(err)
	}
	return line
}

func ParseEntryDetail(line string) (EntryDetail, error) {
	if err := require94(line); err != nil {
		return EntryDetail{}, err
	}
	if line[0] != '6' {
		return EntryDetail{}, formatErr("not an entry detail: %q", line)
	}
	codeNum, err := Number(line, 2, 3)
	if err != nil {
		return EntryDetail{}, err
	}
	code, err := TransactionCodeFrom(int(codeNum))
	if err != nil {
		return EntryDetail{}, err
	}
	cents, err := Number(line, 30, 39)
	if err != nil {
		return EntryDetail{}, err
	}
	e, err := NewEntryDetail(code, Field(line, 4, 12), strings.TrimSpace(Field(line, 13, 29)), OfMinor(cents, USD),
		strings.TrimSpace(Field(line, 40, 54)), strings.TrimSpace(Field(line, 55, 76)), strings.TrimSpace(Field(line, 77, 78)), nil, Field(line, 80, 94))
	if err != nil {
		return EntryDetail{}, err
	}
	if line[78] == '1' {
		e = e.WithAddenda(PaymentAddenda("")) // placeholder until the 7 record arrives
	}
	return e, nil
}

// BatchControl is record type 8: the totals the receiver recomputes.
type BatchControl struct {
	ServiceClassCode  int
	EntryAddendaCount int
	EntryHash         int64
	TotalDebit        Money
	TotalCredit       Money
	CompanyID         string
	OdfiID            string
	BatchNumber       int
}

// VerifyAgainst compares the declared control (from the file) with the computed one, naming the first difference.
func (c BatchControl) VerifyAgainst(computed BatchControl) error {
	b := fmt.Sprintf("batch %d ", c.BatchNumber)
	if err := requireMatch(b+"entry/addenda count", c.EntryAddendaCount, computed.EntryAddendaCount); err != nil {
		return err
	}
	if err := requireMatch(b+"entry hash", c.EntryHash, computed.EntryHash); err != nil {
		return err
	}
	if err := requireMatch(b+"total debit", c.TotalDebit, computed.TotalDebit); err != nil {
		return err
	}
	if err := requireMatch(b+"total credit", c.TotalCredit, computed.TotalCredit); err != nil {
		return err
	}
	return requireMatch(b+"control record", c, computed)
}

func (c BatchControl) ToLine() string {
	line := "8" + mustNumeric(int64(c.ServiceClassCode), 3) + mustNumeric(int64(c.EntryAddendaCount), 6) + mustNumeric(c.EntryHash, 10) +
		mustNumeric(c.TotalDebit.MinorUnits, 12) + mustNumeric(c.TotalCredit.MinorUnits, 12) + Alpha(c.CompanyID, 10) +
		Alpha("", 19) /* message authentication code */ + Alpha("", 6) + c.OdfiID + mustNumeric(int64(c.BatchNumber), 7)
	if err := require94(line); err != nil {
		panic(err)
	}
	return line
}

func ParseBatchControl(line string) (BatchControl, error) {
	if err := require94(line); err != nil {
		return BatchControl{}, err
	}
	if line[0] != '8' {
		return BatchControl{}, formatErr("not a batch control: %q", line)
	}
	nums := [][2]int{{2, 4}, {5, 10}, {11, 20}, {21, 32}, {33, 44}, {88, 94}}
	vals := make([]int64, len(nums))
	for i, n := range nums {
		v, err := Number(line, n[0], n[1])
		if err != nil {
			return BatchControl{}, err
		}
		vals[i] = v
	}
	return BatchControl{int(vals[0]), int(vals[1]), vals[2], OfMinor(vals[3], USD), OfMinor(vals[4], USD),
		strings.TrimSpace(Field(line, 45, 54)), Field(line, 80, 87), int(vals[5])}, nil
}

// FileControl is record type 9: file-level totals plus the block count that explains the 9-filler lines.
type FileControl struct {
	BatchCount        int
	BlockCount        int
	EntryAddendaCount int
	EntryHash         int64
	TotalDebit        Money
	TotalCredit       Money
}

func (c FileControl) VerifyAgainst(computed FileControl) error {
	checks := []struct {
		what               string
		declared, computed any
	}{
		{"file batch count", c.BatchCount, computed.BatchCount},
		{"file block count", c.BlockCount, computed.BlockCount},
		{"file entry/addenda count", c.EntryAddendaCount, computed.EntryAddendaCount},
		{"file entry hash", c.EntryHash, computed.EntryHash},
		{"file total debit", c.TotalDebit, computed.TotalDebit},
		{"file total credit", c.TotalCredit, computed.TotalCredit},
	}
	for _, k := range checks {
		if err := requireMatch(k.what, k.declared, k.computed); err != nil {
			return err
		}
	}
	return nil
}

func (c FileControl) ToLine() string {
	line := "9" + mustNumeric(int64(c.BatchCount), 6) + mustNumeric(int64(c.BlockCount), 6) + mustNumeric(int64(c.EntryAddendaCount), 8) +
		mustNumeric(c.EntryHash, 10) + mustNumeric(c.TotalDebit.MinorUnits, 12) + mustNumeric(c.TotalCredit.MinorUnits, 12) + Alpha("", 39)
	if err := require94(line); err != nil {
		panic(err)
	}
	return line
}

func ParseFileControl(line string) (FileControl, error) {
	if err := require94(line); err != nil {
		return FileControl{}, err
	}
	if line[0] != '9' {
		return FileControl{}, formatErr("not a file control: %q", line)
	}
	nums := [][2]int{{2, 7}, {8, 13}, {14, 21}, {22, 31}, {32, 43}, {44, 55}}
	vals := make([]int64, len(nums))
	for i, n := range nums {
		v, err := Number(line, n[0], n[1])
		if err != nil {
			return FileControl{}, err
		}
		vals[i] = v
	}
	return FileControl{int(vals[0]), int(vals[1]), int(vals[2]), vals[3], OfMinor(vals[4], USD), OfMinor(vals[5], USD)}, nil
}

const HashModulus = int64(10_000_000_000)

// EntryHash sums the receiving DFI identifiers (8 digits each), keeping only the rightmost 10 digits.
func EntryHash(receivingDfiIDs []string) int64 {
	var sum int64
	for _, id := range receivingDfiIDs {
		n, _ := strconv.ParseInt(id, 10, 64)
		sum += n
	}
	return sum % HashModulus
}

// Batch is a header, its entries (trace numbers assigned here), and the control record computed from them.
type Batch struct {
	Header  BatchHeader
	Entries []EntryDetail
}

func NewBatch(header BatchHeader, entries []EntryDetail) (Batch, error) {
	if len(entries) == 0 {
		return Batch{}, formatErr("a batch needs at least one entry")
	}
	traced := make([]EntryDetail, len(entries))
	for i, e := range entries {
		if e.TraceNumber == "" {
			e = e.WithTrace(header.OdfiID, i+1)
		}
		if header.ServiceClassCode == CreditsOnly && e.TransactionCode.IsDebit() {
			return Batch{}, formatErr("debit entry in a 220 credits-only batch")
		}
		if header.ServiceClassCode == DebitsOnly && e.TransactionCode.IsCredit() {
			return Batch{}, formatErr("credit entry in a 225 debits-only batch")
		}
		traced[i] = e
	}
	return Batch{header, traced}, nil
}

func (b Batch) EntryHash() int64 {
	ids := make([]string, len(b.Entries))
	for i, e := range b.Entries {
		ids[i] = e.ReceivingDfiID()
	}
	return EntryHash(ids)
}

func (b Batch) EntryAddendaCount() int {
	n := 0
	for _, e := range b.Entries {
		n += e.RecordCount()
	}
	return n
}

func (b Batch) TotalDebit() Money {
	total := OfMinor(0, USD)
	for _, e := range b.Entries {
		if e.TransactionCode.IsDebit() {
			total = total.Plus(e.Amount)
		}
	}
	return total
}

func (b Batch) TotalCredit() Money {
	total := OfMinor(0, USD)
	for _, e := range b.Entries {
		if e.TransactionCode.IsCredit() {
			total = total.Plus(e.Amount)
		}
	}
	return total
}

func (b Batch) Control() BatchControl {
	return BatchControl{b.Header.ServiceClassCode, b.EntryAddendaCount(), b.EntryHash(), b.TotalDebit(), b.TotalCredit(),
		b.Header.CompanyID, b.Header.OdfiID, b.Header.BatchNumber}
}

func (b Batch) ToLines() []string {
	lines := []string{b.Header.ToLine()}
	for i, e := range b.Entries {
		lines = append(lines, e.ToLine())
		if e.Addenda != nil {
			lines = append(lines, e.Addenda.ToLine(i+1, e.TraceNumber))
		}
	}
	return append(lines, b.Control().ToLine())
}

const BlockingFactor = 10

var Filler = strings.Repeat("9", 94)

// AchFile is the whole file: header, batches, control, and 9-filler to a multiple of ten records.
type AchFile struct {
	Header  FileHeader
	Batches []Batch
}

func NewAchFile(header FileHeader, batches []Batch) (AchFile, error) {
	if len(batches) == 0 {
		return AchFile{}, formatErr("a file needs at least one batch")
	}
	return AchFile{header, append([]Batch(nil), batches...)}, nil
}

func (f AchFile) RecordCount() int {
	n := 2
	for _, b := range f.Batches {
		n += 2 + b.EntryAddendaCount()
	}
	return n
}

func (f AchFile) BlockCount() int { return (f.RecordCount() + BlockingFactor - 1) / BlockingFactor }

func (f AchFile) Control() FileControl {
	debit, credit := OfMinor(0, USD), OfMinor(0, USD)
	var hash int64
	count := 0
	for _, b := range f.Batches {
		debit = debit.Plus(b.TotalDebit())
		credit = credit.Plus(b.TotalCredit())
		hash = (hash + b.EntryHash()) % HashModulus
		count += b.EntryAddendaCount()
	}
	return FileControl{len(f.Batches), f.BlockCount(), count, hash, debit, credit}
}

func (f AchFile) ToLines() []string {
	lines := []string{f.Header.ToLine()}
	for _, b := range f.Batches {
		lines = append(lines, b.ToLines()...)
	}
	lines = append(lines, f.Control().ToLine())
	for len(lines)%BlockingFactor != 0 {
		lines = append(lines, Filler)
	}
	return lines
}

func (f AchFile) Write() string { return strings.Join(f.ToLines(), "\n") + "\n" }

// Equal compares two files structurally (EntryDetail holds a pointer, so == on the struct is not enough).
func (f AchFile) Equal(other AchFile) bool { return f.Write() == other.Write() }

// ParseAchFile parses and verifies: every line 94 characters, every routing number valid, every control total recomputed and matched.
func ParseAchFile(text string) (AchFile, error) {
	var fileHeader *FileHeader
	var batches []Batch
	var batchHeader *BatchHeader
	var entries []EntryDetail
	inBatch := false
	var fileControl *FileControl
	for _, raw := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		if raw == "" {
			continue
		}
		if err := require94(raw); err != nil {
			return AchFile{}, err
		}
		line := raw
		if line == Filler {
			continue
		}
		switch line[0] {
		case '1':
			if fileHeader != nil {
				return AchFile{}, formatErr("duplicate file header")
			}
			h, err := ParseFileHeader(line)
			if err != nil {
				return AchFile{}, err
			}
			fileHeader = &h
		case '5':
			if fileHeader == nil {
				return AchFile{}, formatErr("batch header before file header")
			}
			if inBatch {
				return AchFile{}, formatErr("batch header before previous batch control")
			}
			h, err := ParseBatchHeader(line)
			if err != nil {
				return AchFile{}, err
			}
			batchHeader, entries, inBatch = &h, nil, true
		case '6':
			if !inBatch {
				return AchFile{}, formatErr("entry outside a batch")
			}
			e, err := ParseEntryDetail(line)
			if err != nil {
				return AchFile{}, err
			}
			entries = append(entries, e)
		case '7':
			if len(entries) == 0 {
				return AchFile{}, formatErr("addenda without an entry")
			}
			last := entries[len(entries)-1]
			if last.Addenda == nil {
				return AchFile{}, formatErr("addenda follows an entry whose addenda indicator is 0")
			}
			a, err := ParseAddenda(line)
			if err != nil {
				return AchFile{}, err
			}
			entries[len(entries)-1] = last.WithAddenda(a)
		case '8':
			if !inBatch {
				return AchFile{}, formatErr("batch control without a batch header")
			}
			batch, err := NewBatch(*batchHeader, entries)
			if err != nil {
				return AchFile{}, err
			}
			declared, err := ParseBatchControl(line)
			if err != nil {
				return AchFile{}, err
			}
			if err := declared.VerifyAgainst(batch.Control()); err != nil {
				return AchFile{}, err
			}
			batches = append(batches, batch)
			batchHeader, entries, inBatch = nil, nil, false
		case '9':
			if fileControl != nil {
				return AchFile{}, formatErr("duplicate file control")
			}
			c, err := ParseFileControl(line)
			if err != nil {
				return AchFile{}, err
			}
			fileControl = &c
		default:
			return AchFile{}, formatErr("unknown record type %c", line[0])
		}
	}
	if fileHeader == nil || fileControl == nil {
		return AchFile{}, formatErr("file header and file control are both required")
	}
	if inBatch {
		return AchFile{}, formatErr("file ended inside a batch")
	}
	file, err := NewAchFile(*fileHeader, batches)
	if err != nil {
		return AchFile{}, err
	}
	if err := fileControl.VerifyAgainst(file.Control()); err != nil {
		return AchFile{}, err
	}
	return file, nil
}

// ---------------------------------------------------------------------------
// Worked example
// ---------------------------------------------------------------------------

// ODFI is the originating bank (fictitious, passes the ABA checksum); its first 8 digits prefix every trace number.
const ODFI = "021999996"

func fixedClock() time.Time { return time.Date(2026, 10, 11, 9, 30, 0, 0, time.UTC) }

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func payrollFile() AchFile {
	header := must(FileHeaderOf(ODFI, "1234567890", fixedClock, "FIRST FICTIONAL BANK", "ACME PAYROLL INC"))
	batchHeader := must(NewBatchHeader(CreditsOnly, "ACME PAYROLL", "", "1234567890", PPD, "PAYROLL", "OCT 15", "261013", ODFI[:8], 1))
	entries := []EntryDetail{
		must(EntryOf(CheckingCredit, "011999993", "000123456789", must(ParseMoney("2512.37", USD)), "EMP0001", "ADA LOVELACE")),
		must(EntryOf(SavingsCredit, "061999998", "9876543210", must(ParseMoney("1800.00", USD)), "EMP0002", "GRACE HOPPER")),
		must(EntryOf(CheckingCredit, "121999993", "55501234", must(ParseMoney("3075.50", USD)), "EMP0003", "EDSGER DIJKSTRA")).
			WithAddenda(PaymentAddenda("BONUS INCLUDED Q3")),
	}
	return must(NewAchFile(header, []Batch{must(NewBatch(batchHeader, entries))}))
}

func main() {
	fmt.Println("== Routing number check digit (ABA mod-10, weights 3-7-1) ==")
	cd, _ := RoutingCheckDigit("02199999")
	fmt.Printf("02199999 -> check digit %d  (021999996 valid: %v)\n", cd, RoutingIsValid("021999996"))
	fmt.Printf("021999969 valid? %v  (two digits transposed)\n", RoutingIsValid("021999969"))

	fmt.Println("\n== A payroll file: 1 batch, 3 PPD credits, 1 addenda ==")
	file := payrollFile()
	text := file.Write()
	fmt.Print(text)
	batch := file.Batches[0]
	fmt.Printf("entry hash   = %d  (01199999 + 06199999 + 12199999)\n", batch.EntryHash())
	fmt.Printf("total credit = %s, total debit = %s\n", batch.TotalCredit(), batch.TotalDebit())
	fmt.Printf("records = %d, blocks = %d, lines written = %d\n", file.RecordCount(), file.BlockCount(), len(file.ToLines()))

	fmt.Println("\n== Round trip ==")
	parsed, err := ParseAchFile(text)
	fmt.Printf("parse(write(file)) == file ? %v\n", err == nil && parsed.Equal(file))

	fmt.Println("\n== A return: R01 Insufficient Funds on a WEB debit ==")
	original := must(EntryOf(CheckingDebit, "011999993", "000123456789", must(ParseMoney("49.99", USD)), "ORD-7781", "ADA LOVELACE")).WithTrace(ODFI[:8], 1)
	returned := must(EntryOf(CheckingReturnDebit, "011999993", "000123456789", original.Amount, "ORD-7781", "ADA LOVELACE")).
		WithAddenda(must(ReturnAddenda("R01", original.TraceNumber, ODFI[:8], "")))
	returnHeader := must(NewBatchHeader(DebitsOnly, "ACME STORE", "", "1234567890", WEB, "RETURN", "", "261015", "01199999", 1))
	for _, line := range must(NewBatch(returnHeader, []EntryDetail{returned})).ToLines() {
		fmt.Println(line)
	}
	reason := must(returned.Addenda.Reason())
	settled := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC) // a Thursday
	kind := "banking"
	if reason.ExtendedWindow {
		kind = "calendar"
	}
	fmt.Printf("%s %s: window %d %s days, deadline %s, re-presentable %v\n", reason.Code, reason.Description, reason.WindowDays(), kind,
		reason.Deadline(settled).Format("2006-01-02"), reason.Representable)
	r10 := must(LookupReturnReason("R10"))
	fmt.Printf("%s %s: deadline %s, re-presentable %v\n", r10.Code, r10.Description, r10.Deadline(settled).Format("2006-01-02"), r10.Representable)

	fmt.Println("\n== Rejections ==")
	if _, err := EntryOf(CheckingCredit, "021999969", "1", must(ParseMoney("1.00", USD)), "", "X"); err != nil {
		fmt.Println("rejected:", err)
	}
	ctl := batch.Control().ToLine() // bump the last digit of the credit total (position 44)
	bumped := byte('0')
	if ctl[43] != '9' {
		bumped = ctl[43] + 1
	}
	if _, err := ParseAchFile(strings.Replace(text, ctl, ctl[:43]+string(bumped)+ctl[44:], 1)); err != nil {
		fmt.Println("rejected:", err)
	}
}
