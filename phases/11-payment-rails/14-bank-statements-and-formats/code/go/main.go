// Lesson: phases/11-payment-rails/14-bank-statements-and-formats/docs/en.md
// Bank Statement Formats: MT940, CAMT.053, BAI2, OFX parsed into one normalized model with balance continuity checks.
// Run:  go run .
// Test: go test ./...
// Sources: SWIFT MT940 (Category 9 user handbook); ISO 20022 camt.053.001.08; BAI2 Cash Management Balance Reporting
// Specifications (X9.121 lineage); OFX Specification 1.0.2 / 2.x. Go 1.22+, standard library only. Samples are fictional.
package main

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ErrStatementParse is the sentinel every StatementParseError matches with errors.Is.
var ErrStatementParse = errors.New("statement parse error")

// StatementParseError is the Java StatementParseException: a message plus an optional cause.
type StatementParseError struct {
	Msg   string
	Cause error
}

func (e *StatementParseError) Error() string        { return e.Msg }
func (e *StatementParseError) Unwrap() error        { return e.Cause }
func (e *StatementParseError) Is(target error) bool { return target == ErrStatementParse }

func parseErr(format string, args ...any) error {
	return &StatementParseError{Msg: fmt.Sprintf(format, args...)}
}

func parseWrap(cause error, format string, args ...any) error {
	return &StatementParseError{Msg: fmt.Sprintf(format, args...), Cause: cause}
}

// CurrencyOf looks up a known ISO 4217 code; statements carry the code as text. Unknown codes are an error, not a guess.
func CurrencyOf(code string) (Currency, error) {
	switch code {
	case "USD":
		return USD, nil
	case "EUR":
		return EUR, nil
	case "GBP":
		return GBP, nil
	case "INR":
		return INR, nil
	case "JPY":
		return JPY, nil
	case "KWD":
		return KWD, nil
	default:
		return Currency{}, parseErr("unknown currency %s", code)
	}
}

type StatementFormat int

const (
	MT940 StatementFormat = iota
	CAMT053
	BAI2
	OFX
)

func (f StatementFormat) String() string { return [...]string{"MT940", "CAMT053", "BAI2", "OFX"}[f] }

// DetectFormat sniffs the format from the first bytes. Every format announces itself; none needs a file extension.
func DetectFormat(text string) (StatementFormat, error) {
	head := strings.TrimLeft(text, " \t\r\n\f\v")
	switch {
	case strings.HasPrefix(head, "{1:") || strings.HasPrefix(head, ":20:"):
		return MT940, nil
	case strings.HasPrefix(head, "01,"):
		return BAI2, nil
	case strings.HasPrefix(head, "OFXHEADER") || strings.HasPrefix(head, "<OFX>") || strings.Contains(head, "<?OFX"):
		return OFX, nil
	case strings.HasPrefix(head, "<") && strings.Contains(head, "camt.053"):
		return CAMT053, nil
	}
	return 0, parseErr("unrecognised statement format: %s", head[:min(20, len(head))])
}

// StatementLine is one booked movement. Amount is signed: positive is a credit to the account, negative a debit.
type StatementLine struct {
	BookingDate   time.Time
	ValueDate     time.Time
	Amount        Money
	Reference     string
	BankReference string
	Description   string
}

// Statement is the normalized statement every format is parsed into. OpeningReported is false when the format gives
// only a closing balance (OFX) and the opening was derived; such a statement cannot prove its own continuity.
type Statement struct {
	Source          StatementFormat
	AccountId       string
	Currency        Currency
	StatementId     string
	OpeningDate     time.Time
	Opening         Money
	ClosingDate     time.Time
	Closing         Money
	Lines           []StatementLine
	OpeningReported bool
}

func (s Statement) Movement() (Money, error) {
	sum := OfMinor(0, s.Currency)
	for _, l := range s.Lines {
		var err error
		if sum, err = sum.Plus(l.Amount); err != nil {
			return Money{}, err
		}
	}
	return sum, nil
}

// ContinuityErrors checks the invariant: opening + sum(lines) == closing. It returns problems rather than failing.
func (s Statement) ContinuityErrors() []string {
	errs := []string{}
	movement, err := s.Movement()
	if err != nil {
		return append(errs, s.StatementId+": "+err.Error())
	}
	expected, err := s.Opening.Plus(movement)
	if err != nil {
		return append(errs, s.StatementId+": "+err.Error())
	}
	if expected != s.Closing {
		diff, _ := s.Closing.Minus(expected)
		errs = append(errs, fmt.Sprintf("%s: opening %v + movements %v = %v, but closing is %v (difference %v)",
			s.StatementId, s.Opening, movement, expected, s.Closing, diff))
	}
	for _, l := range s.Lines {
		if l.BookingDate.After(s.ClosingDate) {
			errs = append(errs, s.StatementId+": line "+l.BankReference+" booked after closing date")
		}
	}
	return errs
}

// StatementChain checks consecutive statements of one account: each closing balance must be the next opening balance.
var StatementChain statementChain

type statementChain struct{}

func (statementChain) Gaps(statements []Statement) []string {
	errs := []string{}
	for i := 1; i < len(statements); i++ {
		prev, next := statements[i-1], statements[i]
		if prev.AccountId != next.AccountId {
			errs = append(errs, "account changed between "+prev.StatementId+" and "+next.StatementId)
			continue
		}
		if !next.ClosingDate.After(prev.ClosingDate) {
			errs = append(errs, fmt.Sprintf("%s closes on %s, not after %s", next.StatementId, isoDate(next.ClosingDate), prev.StatementId))
		}
		if prev.Closing != next.Opening {
			missing, err := next.Opening.Minus(prev.Closing)
			detail := "missing " + missing.String()
			if err != nil {
				detail = err.Error()
			}
			errs = append(errs, fmt.Sprintf("gap between %s closing %v and %s opening %v (%s)",
				prev.StatementId, prev.Closing, next.StatementId, next.Opening, detail))
		}
	}
	return errs
}

// ParseHelpers are shared by the four parsers (the Java `Parse` class; the name avoids clashing with Money's Parse).
var ParseHelpers parseHelpers

type parseHelpers struct{}

// Decimal reads "1500,00" (MT940), "1500.00" (camt, OFX), "-1500" -> exact Money. Never via a float64.
func (parseHelpers) Decimal(text string, c Currency) (Money, error) {
	m, err := Parse(strings.ReplaceAll(strings.TrimSpace(text), ",", "."), c)
	if err != nil {
		return Money{}, parseWrap(err, "bad amount '%s': %v", text, err)
	}
	return m, nil
}

// Yymmdd reads YYMMDD, pivoting at 2000 (all formats here are 21st-century).
func (parseHelpers) Yymmdd(s string) (time.Time, error) {
	if len(s) < 6 {
		return time.Time{}, parseErr("bad date %q", s)
	}
	yy, e1 := strconv.Atoi(s[0:2])
	mm, e2 := strconv.Atoi(s[2:4])
	dd, e3 := strconv.Atoi(s[4:6])
	if e1 != nil || e2 != nil || e3 != nil {
		return time.Time{}, parseErr("bad date %q", s)
	}
	return date(2000+yy, mm, dd), nil
}

func (parseHelpers) NormalizeNewlines(text string) string {
	return strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
}

func date(y, m, d int) time.Time { return time.Date(y, time.Month(m), d, 0, 0, 0, 0, time.UTC) }

func isoDate(t time.Time) string { return t.Format("2006-01-02") }

func parseIsoDate(s string) (time.Time, error) {
	t, err := time.Parse("2006-01-02", strings.TrimSpace(s))
	if err != nil {
		return time.Time{}, parseWrap(err, "bad date %q", s)
	}
	return t, nil
}

// ---------- MT940 ----------

// Mt940Parser reads a SWIFT MT940 Customer Statement. Fields are ":tag:value" lines; a line that does not start a
// tag continues the previous field. :60F/:60M opening, :61 statement line, :86 information to account owner,
// :62F/:62M closing.
var Mt940Parser mt940Parser

type mt940Parser struct{}

var (
	mt940Tag     = regexp.MustCompile(`^:(\d{2}[A-Z]?):(.*)$`)
	mt940Balance = regexp.MustCompile(`^([CD])(\d{6})([A-Z]{3})(\d+,\d{0,2})$`)
	// :61: value date, [entry date], mark (C, D, RC, RD), [funds code], amount, type, customer ref, [//bank ref], [\n supplementary].
	mt940Line     = regexp.MustCompile(`(?s)^(\d{6})(\d{4})?(RC|RD|C|D)([A-Z])?(\d+,\d{0,2})([NFS][A-Z0-9]{3})([^\n]*?)(?://([^\n]{1,16}))?(?:\n(.*))?$`)
	mt940Envelope = regexp.MustCompile(`\{1:[^}]*\}(\{2:[^}]*\})?(\{3:(\{[^}]*\})*\})?\{4:`)
	mt940Trailer  = regexp.MustCompile(`(?m)^-\}.*$`)
)

type Field struct {
	Tag   string
	Value string
}

func (mt940Parser) Fields(block string) []Field {
	out := []Field{}
	tag := ""
	var value strings.Builder
	for _, line := range strings.Split(block, "\n") {
		if m := mt940Tag.FindStringSubmatch(line); m != nil {
			if tag != "" {
				out = append(out, Field{tag, value.String()})
			}
			tag = m[1]
			value.Reset()
			value.WriteString(m[2])
		} else if tag != "" && line != "" {
			value.WriteString("\n" + line)
		}
	}
	if tag != "" {
		out = append(out, Field{tag, value.String()})
	}
	return out
}

func (p mt940Parser) ParseAll(text string) ([]Statement, error) {
	// Strip SWIFT envelopes ({1:..}{2:..}[{3:..}]{4: ... -}); a file may concatenate several messages.
	body := mt940Trailer.ReplaceAllString(mt940Envelope.ReplaceAllString(ParseHelpers.NormalizeNewlines(text), ""), "")
	statements := []Statement{}
	current := []Field{}
	for _, f := range p.Fields(body) {
		if f.Tag == "20" && len(current) > 0 {
			s, err := p.build(current)
			if err != nil {
				return nil, err
			}
			statements = append(statements, s)
			current = []Field{}
		}
		current = append(current, f)
	}
	if len(current) > 0 {
		s, err := p.build(current)
		if err != nil {
			return nil, err
		}
		statements = append(statements, s)
	}
	if len(statements) == 0 {
		return nil, parseErr("MT940: no :20: field found")
	}
	return statements, nil
}

func (p mt940Parser) Parse(text string) (Statement, error) {
	all, err := p.ParseAll(text)
	if err != nil {
		return Statement{}, err
	}
	if len(all) != 1 {
		return Statement{}, parseErr("MT940: expected one statement, found %d", len(all))
	}
	return all[0], nil
}

func (p mt940Parser) build(fields []Field) (Statement, error) {
	var ref, account, seq string
	haveRef, haveAccount := false, false
	var opening, closing []string
	lines := []StatementLine{}
	var currency Currency
	var pendingLine []string
	pendingInfo, havePendingInfo := "", false
	flush := func() error {
		if pendingLine == nil {
			return nil
		}
		l, err := p.line(pendingLine, pendingInfo, havePendingInfo, currency)
		if err != nil {
			return err
		}
		lines = append(lines, l)
		return nil
	}
	for _, f := range fields {
		switch f.Tag {
		case "20":
			ref, haveRef = strings.TrimSpace(f.Value), true
		case "25":
			account, haveAccount = strings.TrimSpace(f.Value), true
		case "28C":
			seq = strings.TrimSpace(f.Value)
		case "60F", "60M":
			m, err := p.balance(f)
			if err != nil {
				return Statement{}, err
			}
			opening = m
			if currency, err = CurrencyOf(m[3]); err != nil {
				return Statement{}, err
			}
		case "61":
			if err := flush(); err != nil {
				return Statement{}, err
			}
			pendingLine = mt940Line.FindStringSubmatch(f.Value)
			if pendingLine == nil {
				return Statement{}, parseErr("MT940: bad :61: %s", f.Value)
			}
			pendingInfo, havePendingInfo = "", false
		case "86":
			if pendingLine != nil {
				pendingInfo, havePendingInfo = strings.TrimSpace(strings.ReplaceAll(f.Value, "\n", " ")), true
			}
		case "62F", "62M":
			m, err := p.balance(f)
			if err != nil {
				return Statement{}, err
			}
			closing = m
		default: // :21:, :64:, :65: and others are not needed for the normalized statement
		}
	}
	if err := flush(); err != nil {
		return Statement{}, err
	}
	if !haveRef || !haveAccount || opening == nil || closing == nil {
		return Statement{}, parseErr("MT940: statement is missing :20:, :25:, :60a: or :62a:")
	}
	if closing[3] != opening[3] {
		return Statement{}, parseErr("MT940: opening and closing currencies differ")
	}
	openingDate, err := ParseHelpers.Yymmdd(opening[2])
	if err != nil {
		return Statement{}, err
	}
	closingDate, err := ParseHelpers.Yymmdd(closing[2])
	if err != nil {
		return Statement{}, err
	}
	openingAmount, err := ParseHelpers.Decimal(opening[4], currency)
	if err != nil {
		return Statement{}, err
	}
	closingAmount, err := ParseHelpers.Decimal(closing[4], currency)
	if err != nil {
		return Statement{}, err
	}
	id := ref
	if seq != "" {
		id += "/" + seq
	}
	return Statement{MT940, account, currency, id, openingDate, signed(opening[1], openingAmount),
		closingDate, signed(closing[1], closingAmount), lines, true}, nil
}

func (mt940Parser) balance(f Field) ([]string, error) {
	m := mt940Balance.FindStringSubmatch(strings.TrimSpace(f.Value))
	if m == nil {
		return nil, parseErr("MT940: bad balance :%s: %s", f.Tag, f.Value)
	}
	return m, nil
}

func signed(mark string, m Money) Money {
	if mark == "D" {
		return m.Negate()
	}
	return m
}

func (mt940Parser) line(m []string, info string, haveInfo bool, currency Currency) (StatementLine, error) {
	value, err := ParseHelpers.Yymmdd(m[1])
	if err != nil {
		return StatementLine{}, err
	}
	booking := value
	if m[2] != "" {
		month, _ := strconv.Atoi(m[2][0:2])
		day, _ := strconv.Atoi(m[2][2:4])
		year := value.Year()
		if month-int(value.Month()) > 6 { // value 2027-01-02, entry 1231 -> 2026-12-31
			year--
		}
		if int(value.Month())-month > 6 { // value 2026-12-31, entry 0102 -> 2027-01-02
			year++
		}
		booking = date(year, month, day)
	}
	amount, err := ParseHelpers.Decimal(m[5], currency)
	if err != nil {
		return StatementLine{}, err
	}
	// C credit, D debit, RC reversal of a credit (a debit), RD reversal of a debit (a credit)
	credit := m[3] == "C" || m[3] == "RD"
	customerRef := strings.TrimSpace(m[7])
	supplementary := strings.TrimSpace(m[9])
	description := supplementary
	if haveInfo {
		description = info
	}
	if !credit {
		amount = amount.Negate()
	}
	if customerRef == "NONREF" {
		customerRef = ""
	}
	return StatementLine{booking, value, amount, customerRef, m[8], description}, nil
}

// ---------- camt.053 ----------

// Element is a minimal namespace-aware DOM node built from the encoding/xml token stream.
type Element struct {
	Namespace string
	Name      string
	Attrs     []xml.Attr
	Text      string
	Children  []*Element
}

func (e *Element) Attribute(name string) string {
	for _, a := range e.Attrs {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}

var doctype = regexp.MustCompile(`(?i)<!DOCTYPE`)

// decodeXml builds the tree. A DOCTYPE is rejected before decoding (the Java sets disallow-doctype-decl) and Strict
// mode refuses undefined entities: a statement file is never trusted input.
func decodeXml(doc string) (*Element, error) {
	if doctype.MatchString(doc) {
		return nil, errors.New("DOCTYPE is disallowed when processing a statement file")
	}
	d := xml.NewDecoder(strings.NewReader(doc))
	d.Strict = true
	var root *Element
	var stack []*Element
	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			e := &Element{Namespace: t.Name.Space, Name: t.Name.Local}
			for _, a := range t.Attr {
				if a.Name.Space == "" && a.Name.Local != "xmlns" {
					e.Attrs = append(e.Attrs, a)
				}
			}
			if len(stack) == 0 {
				if root != nil {
					return nil, errors.New("more than one root element")
				}
				root = e
			} else {
				parent := stack[len(stack)-1]
				parent.Children = append(parent.Children, e)
			}
			stack = append(stack, e)
		case xml.EndElement:
			stack = stack[:len(stack)-1]
		case xml.CharData:
			for _, e := range stack { // getTextContent(): every ancestor sees its descendants' text
				e.Text += string(t)
			}
		}
	}
	if root == nil {
		return nil, errors.New("no root element")
	}
	return root, nil
}

// Camt053Parser reads an ISO 20022 camt.053.001.08 Bank to Customer Statement. Only booked (BOOK) entries move the balance.
var Camt053Parser camt053Parser

type camt053Parser struct{}

const Camt053NS = "urn:iso:std:iso:20022:tech:xsd:camt.053.001.08"

// catch converts the StatementParseError a helper panicked with back into the return value at the API boundary.
func catch(err *error) {
	if r := recover(); r != nil {
		e, ok := r.(error)
		if !ok || !errors.Is(e, ErrStatementParse) {
			panic(r)
		}
		*err = e
	}
}

func need[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func (p camt053Parser) Parse(doc string) (s Statement, err error) {
	defer catch(&err)
	root := need(p.parseRoot(doc))
	stmt := need(p.Child(need(p.Child(root, "BkToCstmrStmt")), "Stmt"))
	acct := need(p.Child(stmt, "Acct"))
	account := p.Text(need(p.Child(need(p.Child(acct, "Id")), "IBAN")))
	currency := need(CurrencyOf(p.Text(need(p.Child(acct, "Ccy")))))
	var opening, closing *Money
	var openingDate, closingDate time.Time
	for _, bal := range p.Children(stmt, "Bal") {
		code := p.Text(need(p.Child(need(p.Child(need(p.Child(bal, "Tp")), "CdOrPrtry")), "Cd")))
		amount := need(p.amount(bal, currency))
		d := need(parseIsoDate(p.Text(need(p.Child(need(p.Child(bal, "Dt")), "Dt")))))
		if code == "OPBD" || code == "PRCD" {
			a := amount
			opening, openingDate = &a, d
		}
		if code == "CLBD" {
			a := amount
			closing, closingDate = &a, d
		}
	}
	if opening == nil || closing == nil {
		return s, parseErr("camt.053: missing OPBD/PRCD or CLBD balance")
	}
	lines := []StatementLine{}
	for _, ntry := range p.Children(stmt, "Ntry") {
		status := p.Text(need(p.Child(need(p.Child(ntry, "Sts")), "Cd")))
		if status != "BOOK" {
			continue // PDNG and INFO entries are information, not balance movements
		}
		amount := need(p.amount(ntry, currency))
		if rvsl := p.OptChild(ntry, "RvslInd"); rvsl != nil && p.Text(rvsl) == "true" {
			amount = amount.Negate()
		}
		txDtls := p.OptChild(p.OptChild(ntry, "NtryDtls"), "TxDtls")
		refs := p.OptChild(txDtls, "Refs")
		e2e := ""
		if refs != nil && p.OptChild(refs, "EndToEndId") != nil {
			e2e = p.Text(need(p.Child(refs, "EndToEndId")))
		}
		addtl := p.OptChild(ntry, "AddtlNtryInf")
		rmt := p.OptChild(p.OptChild(txDtls, "RmtInf"), "Ustrd")
		description := ""
		if addtl != nil {
			description = p.Text(addtl)
		} else if rmt != nil {
			description = p.Text(rmt)
		}
		if e2e == "NOTPROVIDED" {
			e2e = ""
		}
		lines = append(lines, StatementLine{
			need(parseIsoDate(p.Text(need(p.Child(need(p.Child(ntry, "BookgDt")), "Dt"))))),
			need(parseIsoDate(p.Text(need(p.Child(need(p.Child(ntry, "ValDt")), "Dt"))))),
			amount, e2e, p.Text(need(p.Child(ntry, "AcctSvcrRef"))), description})
	}
	return Statement{CAMT053, account, currency, p.Text(need(p.Child(stmt, "Id"))),
		openingDate, *opening, closingDate, *closing, lines, true}, nil
}

// amount reads Amt with its Ccy attribute plus CdtDbtInd CRDT/DBIT; the currency must match the account.
func (p camt053Parser) amount(parent *Element, currency Currency) (Money, error) {
	amt, err := p.Child(parent, "Amt")
	if err != nil {
		return Money{}, err
	}
	if amt.Attribute("Ccy") != currency.Code {
		return Money{}, parseErr("camt.053: amount in %s on a %s account", amt.Attribute("Ccy"), currency.Code)
	}
	m, err := ParseHelpers.Decimal(amt.Text, currency)
	if err != nil {
		return Money{}, err
	}
	indEl, err := p.Child(parent, "CdtDbtInd")
	if err != nil {
		return Money{}, err
	}
	ind := p.Text(indEl)
	if ind != "CRDT" && ind != "DBIT" {
		return Money{}, parseErr("camt.053: bad CdtDbtInd %s", ind)
	}
	if ind == "DBIT" {
		return m.Negate(), nil
	}
	return m, nil
}

func (camt053Parser) parseRoot(doc string) (*Element, error) {
	root, err := decodeXml(doc)
	if err != nil {
		return nil, parseWrap(err, "camt.053: malformed XML: %v", err)
	}
	if root.Namespace != Camt053NS {
		return nil, parseErr("camt.053: unexpected namespace %s", root.Namespace)
	}
	return root, nil
}

func (camt053Parser) OptChild(parent *Element, name string) *Element {
	if parent == nil {
		return nil
	}
	for _, c := range parent.Children {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func (p camt053Parser) Child(parent *Element, name string) (*Element, error) {
	e := p.OptChild(parent, name)
	if e == nil {
		under := "?"
		if parent != nil {
			under = parent.Name
		}
		return nil, parseErr("camt.053: missing %s under %s", name, under)
	}
	return e, nil
}

func (camt053Parser) Children(parent *Element, name string) []*Element {
	out := []*Element{}
	for _, c := range parent.Children {
		if c.Name == name {
			out = append(out, c)
		}
	}
	return out
}

func (camt053Parser) Text(e *Element) string { return strings.TrimSpace(e.Text) }

// ---------- BAI2 ----------

// Bai2Parser reads BAI2 (Bank Administration Institute, version 2). Comma-delimited records ending in '/', 88
// continues the previous record. 01 file header, 02 group header, 03 account identifier and summaries, 16
// transaction detail, 49 account trailer, 98 group trailer, 99 file trailer. Amounts are integers in minor units.
var Bai2Parser bai2Parser

type bai2Parser struct{}

// LogicalRecord is one logical record (88 continuations joined) and how many physical lines it used.
type LogicalRecord struct {
	Type          string
	Fields        []string
	Text          string
	PhysicalLines int
}

func (p bai2Parser) Records(text string) ([]LogicalRecord, error) {
	out := []LogicalRecord{}
	for _, raw := range strings.Split(ParseHelpers.NormalizeNewlines(text), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if len(line) < 3 {
			return nil, parseErr("BAI2: short record %q", line)
		}
		typ := line[:2]
		if typ == "88" {
			if len(out) == 0 {
				return nil, parseErr("BAI2: 88 continuation with no previous record")
			}
			prev := out[len(out)-1]
			out = out[:len(out)-1]
			rest := line[3:]
			if prev.Type == "16" {
				out = append(out, LogicalRecord{"16", prev.Fields, strings.TrimSpace(prev.Text + " " + rest), prev.PhysicalLines + 1})
			} else {
				merged := append(append([]string{}, prev.Fields...), p.split(rest)...)
				out = append(out, LogicalRecord{prev.Type, merged, "", prev.PhysicalLines + 1})
			}
			continue
		}
		if typ == "16" {
			// 16: type code, amount, funds type, [funds fields], bank ref, customer ref, text-to-end-of-record
			parts := strings.Split(line, ",")
			if len(parts) < 4 {
				return nil, parseErr("BAI2: short 16 record %q", line)
			}
			var fundsExtra int
			switch parts[3] {
			case "0", "1", "2", "Z", "":
				fundsExtra = 0
			case "V":
				fundsExtra = 2
			case "S":
				fundsExtra = 3
			default:
				return nil, parseErr("BAI2: unsupported funds type %s", parts[3])
			}
			fixed := 4 + fundsExtra + 2 // record code, type, amount, funds, extras, bank ref, customer ref
			head := strings.SplitN(line, ",", fixed+1)
			if len(head) < fixed {
				return nil, parseErr("BAI2: short 16 record %q", line)
			}
			fields := append([]string{}, head[1:fixed]...)
			txt := ""
			if len(head) > fixed {
				txt = head[fixed]
			}
			txt = strings.TrimSuffix(txt, "/")
			out = append(out, LogicalRecord{"16", fields, strings.TrimSpace(txt), 1})
		} else {
			out = append(out, LogicalRecord{typ, p.split(line[3:]), "", 1})
		}
	}
	return out, nil
}

func (bai2Parser) split(body string) []string {
	return strings.Split(strings.TrimSuffix(body, "/"), ",")
}

func (bai2Parser) Amount(s string) (int64, error) {
	if s == "" {
		return 0, nil
	}
	v, err := strconv.ParseInt(strings.TrimPrefix(s, "+"), 10, 64)
	if err != nil {
		return 0, parseWrap(err, "BAI2: bad amount %s", s)
	}
	return v, nil
}

func (p bai2Parser) ParseAll(text string) (out []Statement, err error) {
	defer catch(&err)
	recs := need(p.Records(text))
	if len(recs) == 0 || recs[0].Type != "01" {
		return nil, parseErr("BAI2: file must start with 01")
	}
	out = []Statement{}
	var asOf time.Time
	var groupCurrency, currency Currency
	var account string
	var opening, closing *Money
	var lines []StatementLine
	var accountTotal, groupTotal, fileTotal int64
	var accountRecords, groupRecords, fileRecords, groups, accounts int
	atoi := func(s string) int {
		n, e := strconv.Atoi(s)
		if e != nil {
			panic(parseWrap(e, "BAI2: bad count %q", s))
		}
		return n
	}
	field := func(f []string, i int) string {
		if i >= len(f) {
			panic(parseErr("BAI2: record is missing field %d", i))
		}
		return f[i]
	}
	for _, r := range recs {
		fileRecords += r.PhysicalLines
		groupRecords += r.PhysicalLines
		accountRecords += r.PhysicalLines
		f := r.Fields
		switch r.Type {
		case "01":
		case "02":
			asOf = need(ParseHelpers.Yymmdd(field(f, 3)))
			groupCurrency = USD
			if field(f, 5) != "" {
				groupCurrency = need(CurrencyOf(f[5]))
			}
			groupRecords = r.PhysicalLines
			groupTotal = 0
			accounts = 0
			groups++
		case "03":
			account = field(f, 0)
			currency = groupCurrency
			if field(f, 1) != "" {
				currency = need(CurrencyOf(f[1]))
			}
			accountRecords = r.PhysicalLines
			accountTotal = 0
			lines = []StatementLine{}
			opening, closing = nil, nil
			// summary triplets: type code, amount, item count, funds type (only "" or 0 supported here)
			for i := 2; i+3 < len(f)+1 && f[i] != ""; i += 4 {
				amt := need(p.Amount(f[i+1]))
				accountTotal += amt
				if f[i] == "010" {
					m := OfMinor(amt, currency)
					opening = &m
				}
				if f[i] == "015" {
					m := OfMinor(amt, currency)
					closing = &m
				}
			}
		case "16":
			if lines == nil {
				return nil, parseErr("BAI2: 16 record outside an account")
			}
			typeCode := atoi(field(f, 0))
			amt := need(p.Amount(field(f, 1)))
			accountTotal += amt
			credit := typeCode >= 100 && typeCode <= 399
			debit := typeCode >= 400 && typeCode <= 699
			if !credit && !debit {
				return nil, parseErr("BAI2: type code %d is not a credit or debit detail", typeCode)
			}
			n := len(f)
			if !credit {
				amt = -amt
			}
			lines = append(lines, StatementLine{asOf, asOf, OfMinor(amt, currency), f[n-1], f[n-2], r.Text})
		case "49":
			declared := need(p.Amount(field(f, 0)))
			declaredRecords := atoi(field(f, 1))
			if declared != accountTotal {
				return nil, parseErr("BAI2: account %s control total %d != computed %d", account, declared, accountTotal)
			}
			if declaredRecords != accountRecords {
				return nil, parseErr("BAI2: account %s record count %d != %d", account, declaredRecords, accountRecords)
			}
			if opening == nil || closing == nil {
				return nil, parseErr("BAI2: account %s lacks 010 opening or 015 closing ledger", account)
			}
			out = append(out, Statement{BAI2, account, currency, "BAI2-" + account + "-" + isoDate(asOf), asOf, *opening, asOf, *closing, lines, true})
			groupTotal += accountTotal
			accounts++
			lines = nil
		case "98":
			if need(p.Amount(field(f, 0))) != groupTotal {
				return nil, parseErr("BAI2: group control total %s != computed %d", f[0], groupTotal)
			}
			if atoi(field(f, 1)) != accounts {
				return nil, parseErr("BAI2: group account count mismatch")
			}
			if atoi(field(f, 2)) != groupRecords {
				return nil, parseErr("BAI2: group record count %s != %d", f[2], groupRecords)
			}
			fileTotal += groupTotal
		case "99":
			if need(p.Amount(field(f, 0))) != fileTotal {
				return nil, parseErr("BAI2: file control total %s != computed %d", f[0], fileTotal)
			}
			if atoi(field(f, 1)) != groups {
				return nil, parseErr("BAI2: file group count mismatch")
			}
			if atoi(field(f, 2)) != fileRecords {
				return nil, parseErr("BAI2: file record count %s != %d", f[2], fileRecords)
			}
		default:
			return nil, parseErr("BAI2: unknown record type %s", r.Type)
		}
	}
	if recs[len(recs)-1].Type != "99" {
		return nil, parseErr("BAI2: file must end with 99")
	}
	return out, nil
}

func (p bai2Parser) Parse(text string) (Statement, error) {
	all, err := p.ParseAll(text)
	if err != nil {
		return Statement{}, err
	}
	if len(all) != 1 {
		return Statement{}, parseErr("BAI2: expected one account, found %d", len(all))
	}
	return all[0], nil
}

// ---------- OFX ----------

// OfxParser reads OFX 1.x (SGML: leaf elements have no closing tag) and 2.x (XML). A tiny tokenizer handles both.
// OFX reports only a closing (ledger) balance, so the opening balance is derived and cannot be verified.
var OfxParser ofxParser

type ofxParser struct{}

// OfxNode is a leaf (Leaf true, Value set) or an aggregate with Children.
type OfxNode struct {
	Name     string
	Value    string
	Leaf     bool
	Children []*OfxNode
}

func (n *OfxNode) Find(name string) *OfxNode {
	if n.Name == name {
		return n
	}
	for _, c := range n.Children {
		if hit := c.Find(name); hit != nil {
			return hit
		}
	}
	return nil
}

func (n *OfxNode) Req(name string) (string, error) {
	hit := n.Find(name)
	if hit == nil || !hit.Leaf {
		return "", parseErr("OFX: missing <%s>", name)
	}
	return hit.Value, nil
}

func (n *OfxNode) Opt(name string) string {
	hit := n.Find(name)
	if hit == nil || !hit.Leaf {
		return ""
	}
	return hit.Value
}

func (ofxParser) Tree(text string) (*OfxNode, error) {
	start := strings.Index(text, "<OFX>")
	if start < 0 {
		return nil, parseErr("OFX: no <OFX> root")
	}
	root := &OfxNode{Name: "#root"}
	stack := []*OfxNode{root}
	i := start
	for i < len(text) {
		lt := strings.IndexByte(text[i:], '<')
		if lt < 0 {
			break
		}
		lt += i
		gt := strings.IndexByte(text[lt:], '>')
		if gt < 0 {
			return nil, parseErr("OFX: unterminated tag")
		}
		gt += lt
		tag := strings.TrimSpace(text[lt+1 : gt])
		next := strings.IndexByte(text[gt:], '<')
		end := len(text)
		if next >= 0 {
			end = gt + next
		}
		value := strings.TrimSpace(text[gt+1 : end])
		top := stack[len(stack)-1]
		switch {
		case strings.HasPrefix(tag, "/"):
			name := tag[1:]
			// close an aggregate; a closing tag for a leaf (OFX 2.x XML) matches nothing on the stack and is ignored
			for k := len(stack) - 1; k >= 0; k-- {
				if stack[k].Name == name {
					stack = stack[:k]
					break
				}
			}
		case value != "":
			top.Children = append(top.Children, &OfxNode{Name: tag, Value: value, Leaf: true}) // leaf: <TRNAMT>-1500.00
		default:
			agg := &OfxNode{Name: tag} // aggregate: <STMTTRN>
			top.Children = append(top.Children, agg)
			stack = append(stack, agg)
		}
		i = end
	}
	return root, nil
}

func (ofxParser) Date(ofxDate string) (time.Time, error) {
	if len(ofxDate) < 8 {
		return time.Time{}, parseErr("OFX: bad date %q", ofxDate)
	}
	y, e1 := strconv.Atoi(ofxDate[0:4])
	m, e2 := strconv.Atoi(ofxDate[4:6])
	d, e3 := strconv.Atoi(ofxDate[6:8])
	if e1 != nil || e2 != nil || e3 != nil {
		return time.Time{}, parseErr("OFX: bad date %q", ofxDate)
	}
	return date(y, m, d), nil
}

func (p ofxParser) Parse(text string) (s Statement, err error) {
	defer catch(&err)
	root := need(p.Tree(ParseHelpers.NormalizeNewlines(text)))
	stmt := root.Find("STMTRS")
	if stmt == nil {
		return s, parseErr("OFX: no <STMTRS> (bank statement response)")
	}
	currency := need(CurrencyOf(need(stmt.Req("CURDEF"))))
	account := need(stmt.Req("ACCTID"))
	list := stmt.Find("BANKTRANLIST")
	if list == nil {
		return s, parseErr("OFX: no <BANKTRANLIST>")
	}
	lines := []StatementLine{}
	for _, t := range list.Children {
		if t.Name != "STMTTRN" {
			continue
		}
		posted := need(p.Date(need(t.Req("DTPOSTED"))))
		valueDate := posted
		if user := t.Opt("DTUSER"); user != "" {
			valueDate = need(p.Date(user))
		}
		description := strings.TrimSpace(t.Opt("NAME") + " " + t.Opt("MEMO"))
		lines = append(lines, StatementLine{posted, valueDate, need(ParseHelpers.Decimal(need(t.Req("TRNAMT")), currency)),
			t.Opt("REFNUM"), need(t.Req("FITID")), description})
	}
	ledger := stmt.Find("LEDGERBAL")
	if ledger == nil {
		return s, parseErr("OFX: no <LEDGERBAL>")
	}
	closing := need(ParseHelpers.Decimal(need(ledger.Req("BALAMT")), currency))
	closingDate := need(p.Date(need(ledger.Req("DTASOF"))))
	movement := OfMinor(0, currency)
	for _, l := range lines {
		movement = need(movement.Plus(l.Amount))
	}
	opening := need(closing.Minus(movement))
	return Statement{OFX, account, currency, "OFX-" + account + "-" + need(list.Req("DTEND")),
		need(p.Date(need(list.Req("DTSTART")))), opening, closingDate, closing, lines, false}, nil
}

// StatementParsers dispatches on the detected format.
var StatementParsers statementParsers

type statementParsers struct{}

func (statementParsers) Parse(text string) (Statement, error) {
	format, err := DetectFormat(text)
	if err != nil {
		return Statement{}, err
	}
	switch format {
	case MT940:
		return Mt940Parser.Parse(text)
	case CAMT053:
		return Camt053Parser.Parse(text)
	case BAI2:
		return Bai2Parser.Parse(text)
	default:
		return OfxParser.Parse(text)
	}
}

func mustStatement(s Statement, err error) Statement {
	if err != nil {
		panic(err)
	}
	return s
}

func mustStatements(s []Statement, err error) []Statement {
	if err != nil {
		panic(err)
	}
	return s
}

func formatList[T any](items []T) string {
	parts := make([]string, len(items))
	for i, it := range items {
		parts[i] = fmt.Sprint(it)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func show(s Statement) {
	derived := ""
	if !s.OpeningReported {
		derived = "  (opening derived)"
	}
	fmt.Printf("%s  account %s  %s  %s %v -> %s %v%s\n", s.Source, s.AccountId, s.StatementId,
		isoDate(s.OpeningDate), s.Opening, isoDate(s.ClosingDate), s.Closing, derived)
	for _, l := range s.Lines {
		fmt.Printf("   %s  %12s  ref=%-9s bank=%-9s %s\n", isoDate(l.BookingDate), l.Amount, l.Reference, l.BankReference, l.Description)
	}
	errs := s.ContinuityErrors()
	if len(errs) == 0 {
		movement, _ := s.Movement()
		fmt.Printf("   continuity: OK (opening + %v = closing)\n", movement)
	} else {
		fmt.Println("   continuity: " + formatList(errs))
	}
}

func main() {
	fmt.Println("== Four formats, one normalized model ==")
	for _, sample := range []string{SampleMT940, SampleCAMT053, SampleBAI2, SampleOFX} {
		show(mustStatement(StatementParsers.Parse(sample)))
	}

	fmt.Println("\n== Same day, same minor-unit movements? ==")
	for _, sample := range []string{SampleMT940, SampleCAMT053, SampleBAI2, SampleOFX} {
		s := mustStatement(StatementParsers.Parse(sample))
		amounts := []int64{}
		for _, l := range s.Lines {
			amounts = append(amounts, l.Amount.MinorUnits)
		}
		fmt.Printf("%s %s closing %d\n", s.Source, formatList(amounts), s.Closing.MinorUnits)
	}

	fmt.Println("\n== A statement that does not add up ==")
	broken := mustStatement(Mt940Parser.Parse(strings.ReplaceAll(SampleMT940, ":62F:C261012EUR14238,25", ":62F:C261012EUR14283,25")))
	for _, e := range broken.ContinuityErrors() {
		fmt.Println("   " + e)
	}

	fmt.Println("\n== Chains: day 2 must open where day 1 closed ==")
	twoDays := SampleMT940 + fmt.Sprintf(SampleMT940Day2, "14238,25", "14338,25")
	chain := mustStatements(Mt940Parser.ParseAll(twoDays))
	fmt.Printf("   statements: %d, gaps: %s\n", len(chain), formatList(StatementChain.Gaps(chain)))
	gapped := SampleMT940 + fmt.Sprintf(SampleMT940Day2, "14250,75", "14350,75")
	bad := mustStatements(Mt940Parser.ParseAll(gapped))
	allContinuous := true
	for _, s := range bad {
		allContinuous = allContinuous && len(s.ContinuityErrors()) == 0
	}
	fmt.Println("   each internally continuous:", allContinuous)
	for _, g := range StatementChain.Gaps(bad) {
		fmt.Println("   " + g)
	}

	fmt.Println("\n== Rejections ==")
	if _, err := Bai2Parser.Parse(strings.ReplaceAll(SampleBAI2, "49,3150150,6/", "49,3150151,6/")); err != nil {
		fmt.Println("   " + err.Error())
	}
	if _, err := Mt940Parser.Parse(strings.ReplaceAll(SampleMT940, "D12,50NCHG", "D12,505NCHG")); err != nil {
		fmt.Println("   " + err.Error())
	}
	if _, err := StatementParsers.Parse("Date,Description,Amount\n2026-10-12,Fee,-12.50\n"); err != nil {
		fmt.Println("   " + err.Error())
	}
}
