// Tests for phases/11-payment-rails/08-ach-and-nacha. Run: go test ./...
package main

import (
	"strings"
	"testing"
	"time"
)

func credit(t *testing.T, routing string, cents int64) EntryDetail {
	t.Helper()
	e, err := EntryOf(CheckingCredit, routing, "123", OfMinor(cents, USD), "ID", "NAME")
	if err != nil {
		t.Fatalf("credit(%s): %v", routing, err)
	}
	return e
}

func debit(t *testing.T, routing string, cents int64) EntryDetail {
	t.Helper()
	e, err := EntryOf(CheckingDebit, routing, "123", OfMinor(cents, USD), "ID", "NAME")
	if err != nil {
		t.Fatalf("debit(%s): %v", routing, err)
	}
	return e
}

func header(t *testing.T, serviceClass int, sec SecCode) BatchHeader {
	t.Helper()
	h, err := NewBatchHeader(serviceClass, "ACME", "", "1234567890", sec, "PAYROLL", "", "261013", "02199999", 1)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func mustBatch(t *testing.T, h BatchHeader, entries []EntryDetail) Batch {
	t.Helper()
	b, err := NewBatch(h, entries)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestRoutingCheckDigit(t *testing.T) {
	for prefix, want := range map[string]int{"02199999": 6, "01199999": 3, "12199999": 3} {
		got, err := RoutingCheckDigit(prefix)
		if err != nil || got != want {
			t.Errorf("RoutingCheckDigit(%s) = %d, %v; want %d", prefix, got, err, want)
		}
	}
	if !RoutingIsValid("021999996") || !RoutingIsValid("061999998") {
		t.Error("known-good routing numbers rejected")
	}
	if RoutingIsValid("021999969") {
		t.Error("transposed digits accepted")
	}
	if RoutingIsValid("021999992") {
		t.Error("single digit error accepted")
	}
	if RoutingIsValid("02199999") || RoutingIsValid("02199999A") {
		t.Error("wrong length or letters accepted")
	}
	if _, err := EntryOf(CheckingCredit, "021999969", "1", OfMinor(100, USD), "", "X"); !IsFormatError(err) {
		t.Error("entry with bad check digit should be rejected")
	}
}

func TestFixedWidthHelpers(t *testing.T) {
	if Alpha("abc", 5) != "ABC  " {
		t.Error("alpha should left-justify, space-fill, uppercase")
	}
	if Alpha("ABCDEFG", 5) != "ABCDE" {
		t.Error("alpha should truncate")
	}
	if s, _ := Numeric(42, 6); s != "000042" {
		t.Error("numeric should right-justify and zero-fill")
	}
	if _, err := Numeric(1234567, 6); !IsFormatError(err) {
		t.Error("numeric must never truncate")
	}
	if Field("abcdef", 2, 4) != "bcd" {
		t.Error("field uses 1-based inclusive positions")
	}
}

func TestTransactionCodes(t *testing.T) {
	if c, _ := TransactionCodeFrom(22); c != CheckingCredit || !c.IsCredit() {
		t.Error("22 should be a checking credit")
	}
	if c, _ := TransactionCodeFrom(27); !c.IsDebit() || c.IsCredit() {
		t.Error("27 should be a checking debit")
	}
	if !CheckingPrenoteCredit.IsPrenote() {
		t.Error("23 should be a prenote")
	}
	if !CheckingReturnDebit.IsReturn() || !CheckingReturnDebit.IsDebit() {
		t.Error("26 should be a return and a debit")
	}
	if _, err := TransactionCodeFrom(25); !IsFormatError(err) {
		t.Error("unknown transaction code should be rejected")
	}
	if _, err := EntryOf(CheckingPrenoteCredit, "021999996", "1", OfMinor(1, USD), "", "X"); !IsFormatError(err) {
		t.Error("prenote with non-zero amount should be rejected")
	}
	if _, err := EntryOf(CheckingCredit, "021999996", "1", OfMinor(-1, USD), "", "X"); !IsFormatError(err) {
		t.Error("negative amount should be rejected")
	}
	if _, err := EntryOf(CheckingCredit, "021999996", "1", OfMinor(10_000_000_000, USD), "", "X"); !IsFormatError(err) {
		t.Error("amount over ten digits should be rejected")
	}
}

func TestEveryRecordIs94Characters(t *testing.T) {
	file := payrollFile()
	lines := file.ToLines()
	for i, l := range lines {
		if len(l) != 94 {
			t.Errorf("line %d has %d characters", i, len(l))
		}
	}
	if !strings.HasPrefix(lines[0], "101") {
		t.Error("file header should start with 101")
	}
	if Field(lines[0], 35, 40) != "094101" {
		t.Error("record size / blocking factor / format code fields wrong")
	}
}

func TestControlTotals(t *testing.T) {
	file := payrollFile()
	batch := file.Batches[0]
	if batch.EntryHash() != 1199999+6199999+12199999 {
		t.Errorf("entry hash = %d", batch.EntryHash())
	}
	if EntryHash([]string{"99999999", "99999999", "00000003"}) != 200000001 {
		t.Error("entry hash should sum 8-digit ids")
	}
	many := make([]string, 101)
	for i := range many {
		many[i] = "99999999"
	}
	if EntryHash(many) != (101*99999999)%HashModulus {
		t.Error("entry hash should keep the rightmost 10 digits")
	}
	if batch.EntryAddendaCount() != 4 {
		t.Errorf("entry/addenda count = %d, want 4", batch.EntryAddendaCount())
	}
	if want, _ := ParseMoney("7387.87", USD); batch.TotalCredit() != want {
		t.Errorf("total credit = %s", batch.TotalCredit())
	}
	if !batch.TotalDebit().IsZero() {
		t.Error("credits-only batch should have zero debit total")
	}
	fc := file.Control()
	if fc.TotalCredit != batch.TotalCredit() || fc.EntryHash != batch.EntryHash() {
		t.Error("file control should carry the batch totals")
	}
	if batch.Entries[0].TraceNumber != "021999990000001" || batch.Entries[2].TraceNumber != "021999990000003" {
		t.Error("trace numbers should be ODFI id + sequence")
	}
	mixed := mustBatch(t, header(t, Mixed, CCD), []EntryDetail{credit(t, "021999996", 1000), debit(t, "011999993", 250)})
	if mixed.TotalCredit().MinorUnits != 1000 || mixed.TotalDebit().MinorUnits != 250 {
		t.Error("mixed batch should split debit and credit totals")
	}
	if _, err := NewBatch(header(t, CreditsOnly, PPD), []EntryDetail{debit(t, "021999996", 1)}); !IsFormatError(err) {
		t.Error("debit in a 220 credits-only batch should be rejected")
	}
}

func TestBlockPadding(t *testing.T) {
	file := payrollFile()
	if file.RecordCount() != 8 || file.BlockCount() != 1 || len(file.ToLines()) != 10 {
		t.Errorf("8 records should pad to 10 lines: records=%d blocks=%d lines=%d", file.RecordCount(), file.BlockCount(), len(file.ToLines()))
	}
	if file.ToLines()[9] != strings.Repeat("9", 94) {
		t.Error("filler lines should be all nines")
	}
	nine := make([]EntryDetail, 9)
	for i := range nine {
		nine[i] = credit(t, "021999996", 100)
	}
	bigger, _ := NewAchFile(file.Header, []Batch{mustBatch(t, header(t, CreditsOnly, PPD), nine)})
	if bigger.RecordCount() != 13 || bigger.BlockCount() != 2 || len(bigger.ToLines()) != 20 {
		t.Error("13 records should pad to 20 lines in 2 blocks")
	}
	six := make([]EntryDetail, 6)
	for i := range six {
		six[i] = credit(t, "021999996", 100)
	}
	exact, _ := NewAchFile(file.Header, []Batch{mustBatch(t, header(t, CreditsOnly, PPD), six)})
	if exact.RecordCount() != 10 || len(exact.ToLines()) != 10 || strings.HasPrefix(exact.ToLines()[9], "9999") {
		t.Error("exactly 10 records should need no filler")
	}
}

func TestRoundTrip(t *testing.T) {
	file := payrollFile()
	text := file.Write()
	parsed, err := ParseAchFile(text)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !parsed.Equal(file) {
		t.Error("parse(write(file)) should equal file")
	}
	if parsed.Write() != text {
		t.Error("write(parse(text)) should equal text")
	}
	if parsed.Batches[0].Entries[2].Addenda == nil || parsed.Batches[0].Entries[2].Addenda.Info != "BONUS INCLUDED Q3" {
		t.Error("addenda should survive the round trip")
	}
	crlf, err := ParseAchFile(strings.ReplaceAll(text, "\n", "\r\n"))
	if err != nil || !crlf.Equal(file) {
		t.Error("CRLF line endings should be accepted")
	}
}

func TestTamperingDetected(t *testing.T) {
	file := payrollFile()
	text := file.Write()
	batch := file.Batches[0]
	control := batch.Control().ToLine()
	lines := file.ToLines()
	entryLine := lines[2]
	cases := map[string]string{
		"credit total":              strings.Replace(text, control, control[:43]+string(control[43]+1)+control[44:], 1),
		"entry hash":                strings.Replace(text, control, control[:19]+string(control[19]+1)+control[20:], 1),
		"entry count":               strings.Replace(text, control, control[:9]+"5"+control[10:], 1),
		"routing check digit":       strings.Replace(text, entryLine, entryLine[:11]+string(entryLine[11]+1)+entryLine[12:], 1),
		"short line":                strings.Replace(text, entryLine, entryLine[:93], 1),
		"addenda after indicator 0": strings.Replace(text, lines[5], lines[2], 1),
		"missing file control":      strings.Join(lines[:7], "\n") + "\n",
	}
	for name, mutated := range cases {
		if _, err := ParseAchFile(mutated); !IsFormatError(err) {
			t.Errorf("%s: tampering should be rejected, got %v", name, err)
		}
	}
}

func TestReturnEntry(t *testing.T) {
	original := debit(t, "011999993", 4999).WithTrace("02199999", 1)
	addenda, err := ReturnAddenda("R01", original.TraceNumber, "02199999", "")
	if err != nil {
		t.Fatal(err)
	}
	returned, _ := EntryOf(CheckingReturnDebit, "011999993", "123", original.Amount, "ID", "NAME")
	returned = returned.WithAddenda(addenda)
	h, _ := NewBatchHeader(DebitsOnly, "ACME", "", "1234567890", WEB, "RETURN", "", "261015", "01199999", 1)
	batch := mustBatch(t, h, []EntryDetail{returned})
	lines := batch.ToLines()
	if !strings.HasPrefix(lines[2], "799R01"+original.TraceNumber) {
		t.Errorf("return addenda should carry reason code and original trace: %s", lines[2])
	}
	back, err := ParseAddenda(lines[2])
	if err != nil || back != addenda {
		t.Error("return addenda should round trip")
	}
	if batch.TotalDebit().MinorUnits != 4999 || batch.EntryAddendaCount() != 2 {
		t.Error("return should count as a debit in the control")
	}
	if _, err := NewAddenda("02", "", "", "", ""); !IsFormatError(err) {
		t.Error("addenda type other than 05/99 should be rejected")
	}
	reason, err := back.Reason()
	if err != nil || reason.Description != "Insufficient funds" {
		t.Error("parsed return should expose its reason")
	}
	if _, err := PaymentAddenda("X").Reason(); !IsFormatError(err) {
		t.Error("Reason() on a payment addenda should be rejected")
	}
}

func TestReturnReasons(t *testing.T) {
	thursday := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)
	r01, _ := LookupReturnReason("R01")
	r02, _ := LookupReturnReason("R02")
	r03, _ := LookupReturnReason("R03")
	r10, _ := LookupReturnReason("R10")
	if r01.WindowDays() != 2 || !r01.Representable {
		t.Error("R01 should be a two-banking-day, re-presentable return")
	}
	if r02.Representable || r03.Representable {
		t.Error("R02 and R03 should not be re-presentable")
	}
	if !r10.ExtendedWindow || r10.WindowDays() != 60 {
		t.Error("R10 should have the extended 60-day window")
	}
	if got := r01.Deadline(thursday); got != time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC) {
		t.Errorf("two banking days from Thursday = %s, want 2026-10-19", got.Format("2006-01-02"))
	}
	if got := r10.Deadline(thursday); got != time.Date(2026, 12, 14, 0, 0, 0, 0, time.UTC) {
		t.Errorf("sixty calendar days from Thursday = %s, want 2026-12-14", got.Format("2006-01-02"))
	}
	if !r01.IsTimely(thursday, time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)) {
		t.Error("return on the deadline should be timely")
	}
	if r01.IsTimely(thursday, time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)) {
		t.Error("return after the deadline should be late")
	}
	if IsKnownReturnReason("R99") {
		t.Error("R99 should not be known")
	}
	if _, err := LookupReturnReason("R99"); !IsFormatError(err) {
		t.Error("unknown reason lookup should be a format error")
	}
}
