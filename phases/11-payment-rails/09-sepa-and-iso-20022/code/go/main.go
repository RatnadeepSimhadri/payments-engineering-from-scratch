// Lesson: phases/11-payment-rails/09-sepa-and-iso-20022/docs/en.md
// SEPA, Wires, and ISO 20022: generate and parse pain.001 and pacs.008 with encoding/xml, validate SEPA rules.
// Run:  go run .
// Test: go test ./...
// Sources: ISO 20022 message definitions pain.001.001.09 and pacs.008.001.08; EPC SEPA Credit Transfer Rulebook
// and Implementation Guidelines (EPC125-05, EPC115-06); ISO 13616 (IBAN); ISO 9362 (BIC). Go 1.22+, stdlib only.
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
	"unicode/utf8"
)

// ErrIso20022 is the sentinel every Iso20022Error matches with errors.Is: malformed XML, wrong message type,
// bad totals, rule violations. The Java version is one unchecked exception class.
var ErrIso20022 = errors.New("iso20022")

// Iso20022Error carries the message text; Is makes errors.Is(err, ErrIso20022) true.
type Iso20022Error struct {
	Msg   string
	Cause error
}

func (e *Iso20022Error) Error() string        { return e.Msg }
func (e *Iso20022Error) Unwrap() error        { return e.Cause }
func (e *Iso20022Error) Is(target error) bool { return target == ErrIso20022 }

func isoErr(format string, args ...any) error {
	return &Iso20022Error{Msg: fmt.Sprintf(format, args...)}
}

func isoWrap(cause error, format string, args ...any) error {
	return &Iso20022Error{Msg: fmt.Sprintf(format, args...), Cause: cause}
}

// Iban is the ISO 13616 IBAN check: country length table (SEPA subset) and mod-97 == 1. Lesson 11 builds the full registry.
var Iban ibanRules

type ibanRules struct{}

var IbanLengths = map[string]int{
	"AT": 20, "BE": 16, "DE": 22, "ES": 24, "FI": 18, "FR": 27, "GB": 22, "IE": 22, "IT": 27, "LU": 20, "NL": 18, "PT": 25,
}

func (ibanRules) IsValid(iban string) bool {
	if len(iban) < 15 || len(iban) > 34 {
		return false
	}
	for i := 0; i < len(iban); i++ {
		c := iban[i]
		if !((c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')) {
			return false
		}
	}
	expected, ok := IbanLengths[iban[:2]]
	if !ok || expected != len(iban) {
		return false
	}
	rearranged := iban[4:] + iban[:4]
	remainder := 0
	for i := 0; i < len(rearranged); i++ {
		c := rearranged[i]
		var value int
		if c >= '0' && c <= '9' {
			value = int(c - '0')
			remainder = (remainder*10 + value) % 97
		} else {
			value = int(c-'A') + 10
			remainder = (remainder*100 + value) % 97
		}
	}
	return remainder == 1
}

// BankCode is the national bank code inside the BBAN, for the countries the demo directory knows.
func (ibanRules) BankCode(iban string) string {
	switch iban[:2] {
	case "DE":
		return iban[4:12] // Bankleitzahl, 8 digits
	case "FR":
		return iban[4:9] // code banque, 5 digits
	default:
		return iban[4:8] // ES entidad, NL bank code, and most others: 4 characters
	}
}

// Bic is the ISO 9362 BIC: 4 letters institution, 2 letters country, 2 alphanumeric location, optional 3 branch.
var Bic bicRules

type bicRules struct{}

var bicFormat = regexp.MustCompile(`^[A-Z]{4}[A-Z]{2}[A-Z0-9]{2}([A-Z0-9]{3})?$`)

func (bicRules) IsValid(bic string) bool { return bicFormat.MatchString(bic) }

// Party is a debtor or creditor: name, account (IBAN), and the bank's BIC ("" when the customer gave IBAN only).
type Party struct {
	Name string
	Iban string
	Bic  string
}

// CreditTransfer is one payment inside a pain.001: CdtTrfTxInf. Remittance "" means absent.
type CreditTransfer struct {
	EndToEndId string
	Amount     Money
	Creditor   Party
	Remittance string
}

// PaymentInitiation is a customer-to-bank credit transfer initiation (pain.001) with one payment information block.
type PaymentInitiation struct {
	MessageId              string
	CreatedAt              time.Time
	InitiatingParty        string
	PaymentInfoId          string
	RequestedExecutionDate time.Time
	Debtor                 Party
	Transfers              []CreditTransfer
}

func (p PaymentInitiation) NumberOfTransactions() int { return len(p.Transfers) }

func (p PaymentInitiation) ControlSum() (Money, error) {
	sum := OfMinor(0, EUR)
	for _, t := range p.Transfers {
		var err error
		if sum, err = sum.Plus(t.Amount); err != nil {
			return Money{}, err
		}
	}
	return sum, nil
}

// Equals is the record equality the Java round-trip test relies on (slices are not comparable with ==).
func (p PaymentInitiation) Equals(o PaymentInitiation) bool {
	if p.MessageId != o.MessageId || !p.CreatedAt.Equal(o.CreatedAt) || p.InitiatingParty != o.InitiatingParty ||
		p.PaymentInfoId != o.PaymentInfoId || !p.RequestedExecutionDate.Equal(o.RequestedExecutionDate) ||
		p.Debtor != o.Debtor || len(p.Transfers) != len(o.Transfers) {
		return false
	}
	for i := range p.Transfers {
		if p.Transfers[i] != o.Transfers[i] {
			return false
		}
	}
	return true
}

// InterbankTransfer is one payment inside a pacs.008: the interbank view, with the transaction id the debtor bank assigned.
type InterbankTransfer struct {
	EndToEndId    string
	TransactionId string
	Amount        Money
	Debtor        Party
	Creditor      Party
	Remittance    string
}

// InterbankMessage is a bank-to-bank customer credit transfer (pacs.008).
type InterbankMessage struct {
	MessageId      string
	CreatedAt      time.Time
	SettlementDate time.Time
	Transfers      []InterbankTransfer
}

func (m InterbankMessage) TotalSettlementAmount() (Money, error) {
	sum := OfMinor(0, EUR)
	for _, t := range m.Transfers {
		var err error
		if sum, err = sum.Plus(t.Amount); err != nil {
			return Money{}, err
		}
	}
	return sum, nil
}

func (m InterbankMessage) Equals(o InterbankMessage) bool {
	if m.MessageId != o.MessageId || !m.CreatedAt.Equal(o.CreatedAt) || !m.SettlementDate.Equal(o.SettlementDate) ||
		len(m.Transfers) != len(o.Transfers) {
		return false
	}
	for i := range m.Transfers {
		if m.Transfers[i] != o.Transfers[i] {
			return false
		}
	}
	return true
}

// SepaRules are the SEPA Credit Transfer business rules from the EPC rulebook and implementation guidelines.
var SepaRules sepaRules

type sepaRules struct{}

// SepaLatin is the SEPA basic Latin character set (EPC217-08): letters, digits, and / - ? : ( ) . , ' + space.
var SepaLatin = regexp.MustCompile(`^[A-Za-z0-9/\-?:().,'+ ]*$`)

const MaxAmountMinor int64 = 99_999_999_999 // 999,999,999.99 EUR

func (r sepaRules) Violations(p PaymentInitiation) []string {
	v := []string{}
	r.id(&v, "MsgId", p.MessageId)
	r.id(&v, "PmtInfId", p.PaymentInfoId)
	r.text(&v, "InitgPty/Nm", p.InitiatingParty, 70)
	r.party(&v, "Dbtr", p.Debtor)
	if p.RequestedExecutionDate.Before(dateOnly(p.CreatedAt)) {
		v = append(v, "ReqdExctnDt "+isoDate(p.RequestedExecutionDate)+" is before CreDtTm "+isoDate(p.CreatedAt))
	}
	if len(p.Transfers) == 0 {
		v = append(v, "at least one CdtTrfTxInf is required")
	}
	seen := map[string]bool{}
	for _, t := range p.Transfers {
		where := "CdtTrfTxInf[" + t.EndToEndId + "]"
		r.id(&v, where+"/EndToEndId", t.EndToEndId)
		if seen[t.EndToEndId] {
			v = append(v, where+": duplicate EndToEndId in message")
		}
		seen[t.EndToEndId] = true
		if t.Amount.Currency != EUR {
			v = append(v, where+": SEPA amounts must be EUR, got "+t.Amount.Currency.Code)
		}
		if t.Amount.MinorUnits < 1 || t.Amount.MinorUnits > MaxAmountMinor {
			v = append(v, where+": amount out of range 0.01..999999999.99: "+t.Amount.String())
		}
		r.party(&v, where+"/Cdtr", t.Creditor)
		if t.Remittance != "" {
			r.text(&v, where+"/RmtInf/Ustrd", t.Remittance, 140)
		}
	}
	return v
}

func (sepaRules) id(v *[]string, field, value string) {
	n := utf8.RuneCountInString(value)
	switch {
	case n == 0 || n > 35:
		*v = append(*v, field+" must be 1..35 characters")
	case !SepaLatin.MatchString(value):
		*v = append(*v, field+" has characters outside the SEPA set: "+SepaRules.FirstOutside(value))
	case strings.HasPrefix(value, "/") || strings.HasSuffix(value, "/") || strings.Contains(value, "//"):
		*v = append(*v, field+" must not start or end with '/' or contain '//'")
	}
}

func (sepaRules) text(v *[]string, field, value string, max int) {
	n := utf8.RuneCountInString(value)
	switch {
	case n == 0 || n > max:
		*v = append(*v, fmt.Sprintf("%s must be 1..%d characters", field, max))
	case !SepaLatin.MatchString(value):
		*v = append(*v, field+" has characters outside the SEPA set: "+SepaRules.FirstOutside(value))
	}
}

// FirstOutside reports "U+00FC at index 1": the code point, so logs stay ASCII and the culprit is unambiguous.
func (sepaRules) FirstOutside(value string) string {
	for i, ch := range []rune(value) {
		if !SepaLatin.MatchString(string(ch)) {
			return fmt.Sprintf("U+%04X at index %d", ch, i)
		}
	}
	return "none"
}

func (r sepaRules) party(v *[]string, field string, party Party) {
	r.text(v, field+"/Nm", party.Name, 70)
	if !Iban.IsValid(party.Iban) {
		*v = append(*v, field+"Acct/IBAN invalid: "+party.Iban)
	}
	if party.Bic != "" && !Bic.IsValid(party.Bic) {
		*v = append(*v, field+"Agt/BICFI invalid: "+party.Bic)
	}
}

// Element is a minimal namespace-aware DOM node: what the Java org.w3c.dom.Element gives this lesson.
type Element struct {
	Namespace string
	Name      string // local name
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

// Xml is the secure parsing and small helpers shared by both message types.
var Xml xmlHelpers

type xmlHelpers struct{}

var doctype = regexp.MustCompile(`(?i)<!DOCTYPE`)

// ParseRoot is XXE-safe: encoding/xml never fetches external entities, but a DOCTYPE is rejected before the
// decoder sees it (the Java sets disallow-doctype-decl), and Strict mode refuses undefined entities. A payment
// file is never trusted input.
func (h xmlHelpers) ParseRoot(doc, expectedNamespace, expectedChild string) (*Element, error) {
	if doctype.MatchString(doc) {
		return nil, isoErr("malformed XML: DOCTYPE is disallowed when processing a payment file")
	}
	root, err := h.decode(doc)
	if err != nil {
		return nil, isoWrap(err, "malformed XML: %v", err)
	}
	if root.Name != "Document" || root.Namespace != expectedNamespace {
		return nil, isoErr("expected Document in %s, got %s", expectedNamespace, root.Namespace)
	}
	return h.Child(root, expectedChild)
}

func (xmlHelpers) decode(doc string) (*Element, error) {
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
			if len(stack) > 0 {
				stack[len(stack)-1].Text += string(t)
			}
		}
	}
	if root == nil {
		return nil, errors.New("no root element")
	}
	return root, nil
}

// El creates an element in the parent's namespace and appends it; with text it is a leaf.
func (xmlHelpers) El(parent *Element, name string, text ...string) *Element {
	e := &Element{Namespace: parent.Namespace, Name: name}
	if len(text) > 0 {
		e.Text = text[0]
	}
	parent.Children = append(parent.Children, e)
	return e
}

// Child walks direct children by local name: Child(e, "DbtrAcct", "Id", "IBAN"). Errors if any step is missing.
func (xmlHelpers) Child(parent *Element, path ...string) (*Element, error) {
	current := parent
	for _, name := range path {
		var next *Element
		for _, c := range current.Children {
			if c.Name == name {
				next = c
				break
			}
		}
		if next == nil {
			return nil, isoErr("missing element %s under %s", strings.Join(path, "/"), parent.Name)
		}
		current = next
	}
	return current, nil
}

func (h xmlHelpers) OptChild(parent *Element, path ...string) *Element {
	e, err := h.Child(parent, path...)
	if err != nil {
		return nil
	}
	return e
}

func (xmlHelpers) Children(parent *Element, name string) []*Element {
	var out []*Element
	for _, c := range parent.Children {
		if c.Name == name {
			out = append(out, c)
		}
	}
	return out
}

func (h xmlHelpers) Text(parent *Element, path ...string) (string, error) {
	e, err := h.Child(parent, path...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(e.Text), nil
}

func (h xmlHelpers) OptText(parent *Element, path ...string) string {
	e := h.OptChild(parent, path...)
	if e == nil {
		return ""
	}
	return strings.TrimSpace(e.Text)
}

// Amount reads "1234.56" with Ccy="EUR" -> Money; rejects a third decimal and a non-EUR currency attribute.
func (xmlHelpers) Amount(amountElement *Element) (Money, error) {
	ccy := amountElement.Attribute("Ccy")
	if ccy != "EUR" {
		return Money{}, isoErr("unsupported currency %s (SEPA is EUR only)", ccy)
	}
	m, err := Parse(amountElement.Text, EUR)
	if err != nil {
		return Money{}, isoWrap(err, "bad amount %s: %v", amountElement.Text, err)
	}
	return m, nil
}

// Decimal renders Money -> "1234.56": the ISO 20022 decimal form, '.' separator, no grouping, no currency in the text.
func (xmlHelpers) Decimal(m Money) string {
	s := m.String()
	return s[:strings.IndexByte(s, ' ')]
}

func (h xmlHelpers) AmountEl(parent *Element, name string, m Money) {
	e := h.El(parent, name, h.Decimal(m))
	e.Attrs = append(e.Attrs, xml.Attr{Name: xml.Name{Local: "Ccy"}, Value: m.Currency.Code})
}

// Serialize prints the tree the way the Java Transformer does: declaration, xmlns on the root, two-space indent.
func (h xmlHelpers) Serialize(root *Element) string {
	var sb strings.Builder
	sb.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n")
	h.write(&sb, root, 0, true)
	return sb.String()
}

func (h xmlHelpers) write(sb *strings.Builder, e *Element, depth int, isRoot bool) {
	indent := strings.Repeat("  ", depth)
	sb.WriteString(indent + "<" + e.Name)
	if isRoot && e.Namespace != "" {
		sb.WriteString(` xmlns="` + e.Namespace + `"`)
	}
	for _, a := range e.Attrs {
		sb.WriteString(" " + a.Name.Local + `="`)
		_ = xml.EscapeText(sb, []byte(a.Value))
		sb.WriteString(`"`)
	}
	if len(e.Children) == 0 {
		sb.WriteString(">")
		_ = xml.EscapeText(sb, []byte(e.Text))
		sb.WriteString("</" + e.Name + ">\n")
		return
	}
	sb.WriteString(">\n")
	for _, c := range e.Children {
		h.write(sb, c, depth+1, false)
	}
	sb.WriteString(indent + "</" + e.Name + ">\n")
}

// DateTimeLayout is ISODateTime with seconds, no zone: 2026-10-13T09:30:00.
const DateTimeLayout = "2006-01-02T15:04:05"

func isoDate(t time.Time) string { return t.Format("2006-01-02") }

func dateOnly(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

func parseDateTime(s string) (time.Time, error) {
	t, err := time.Parse(DateTimeLayout, s)
	if err != nil {
		t, err = time.Parse("2006-01-02T15:04", s)
	}
	return t, err
}

func (h xmlHelpers) Agent(parent *Element, name, bic string) {
	h.El(h.El(h.El(parent, name), "FinInstnId"), "BICFI", bic)
}

func (h xmlHelpers) Account(parent *Element, name, iban string) {
	h.El(h.El(h.El(parent, name), "Id"), "IBAN", iban)
}

// catch turns the Iso20022Error a parser helper panicked with back into an error value at the API boundary.
// The parsers use panic internally so every field read does not need its own if err != nil (encoding/json
// does the same); nothing escapes the Parse functions.
func catch(err *error) {
	if r := recover(); r != nil {
		e, ok := r.(error)
		if !ok || !errors.Is(e, ErrIso20022) {
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

// Pain001 is pain.001.001.09: Customer Credit Transfer Initiation, the file a company sends its bank.
var Pain001 pain001

type pain001 struct{}

const Pain001NS = "urn:iso:std:iso:20022:tech:xsd:pain.001.001.09"

func (pain001) Write(p PaymentInitiation) (string, error) {
	if violations := SepaRules.Violations(p); len(violations) > 0 {
		return "", isoErr("SEPA rule violations: %s", formatList(violations))
	}
	ctrl, err := p.ControlSum()
	if err != nil {
		return "", isoWrap(err, "control sum: %v", err)
	}
	root := &Element{Namespace: Pain001NS, Name: "Document"}
	init := Xml.El(root, "CstmrCdtTrfInitn")

	grp := Xml.El(init, "GrpHdr")
	Xml.El(grp, "MsgId", p.MessageId)
	Xml.El(grp, "CreDtTm", p.CreatedAt.Format(DateTimeLayout))
	Xml.El(grp, "NbOfTxs", strconv.Itoa(p.NumberOfTransactions()))
	Xml.El(grp, "CtrlSum", Xml.Decimal(ctrl))
	Xml.El(Xml.El(grp, "InitgPty"), "Nm", p.InitiatingParty)

	pmt := Xml.El(init, "PmtInf")
	Xml.El(pmt, "PmtInfId", p.PaymentInfoId)
	Xml.El(pmt, "PmtMtd", "TRF")
	Xml.El(pmt, "NbOfTxs", strconv.Itoa(p.NumberOfTransactions()))
	Xml.El(pmt, "CtrlSum", Xml.Decimal(ctrl))
	Xml.El(Xml.El(Xml.El(pmt, "PmtTpInf"), "SvcLvl"), "Cd", "SEPA")
	Xml.El(Xml.El(pmt, "ReqdExctnDt"), "Dt", isoDate(p.RequestedExecutionDate))
	Xml.El(Xml.El(pmt, "Dbtr"), "Nm", p.Debtor.Name)
	Xml.Account(pmt, "DbtrAcct", p.Debtor.Iban)
	if p.Debtor.Bic != "" {
		Xml.Agent(pmt, "DbtrAgt", p.Debtor.Bic)
	}
	Xml.El(pmt, "ChrgBr", "SLEV")

	for _, t := range p.Transfers {
		tx := Xml.El(pmt, "CdtTrfTxInf")
		Xml.El(Xml.El(tx, "PmtId"), "EndToEndId", t.EndToEndId)
		Xml.AmountEl(Xml.El(tx, "Amt"), "InstdAmt", t.Amount)
		if t.Creditor.Bic != "" {
			Xml.Agent(tx, "CdtrAgt", t.Creditor.Bic)
		}
		Xml.El(Xml.El(tx, "Cdtr"), "Nm", t.Creditor.Name)
		Xml.Account(tx, "CdtrAcct", t.Creditor.Iban)
		if t.Remittance != "" {
			Xml.El(Xml.El(tx, "RmtInf"), "Ustrd", t.Remittance)
		}
	}
	return Xml.Serialize(root), nil
}

func (pain001) Parse(doc string) (p PaymentInitiation, err error) {
	defer catch(&err)
	init := need(Xml.ParseRoot(doc, Pain001NS, "CstmrCdtTrfInitn"))
	grp := need(Xml.Child(init, "GrpHdr"))
	pmts := Xml.Children(init, "PmtInf")
	if len(pmts) != 1 {
		return p, isoErr("this parser handles exactly one PmtInf, found %d", len(pmts))
	}
	pmt := pmts[0]
	if need(Xml.Text(pmt, "PmtMtd")) != "TRF" {
		return p, isoErr("PmtMtd must be TRF")
	}
	debtor := Party{need(Xml.Text(pmt, "Dbtr", "Nm")), need(Xml.Text(pmt, "DbtrAcct", "Id", "IBAN")),
		Xml.OptText(pmt, "DbtrAgt", "FinInstnId", "BICFI")}
	transfers := []CreditTransfer{}
	for _, tx := range Xml.Children(pmt, "CdtTrfTxInf") {
		creditor := Party{need(Xml.Text(tx, "Cdtr", "Nm")), need(Xml.Text(tx, "CdtrAcct", "Id", "IBAN")),
			Xml.OptText(tx, "CdtrAgt", "FinInstnId", "BICFI")}
		transfers = append(transfers, CreditTransfer{need(Xml.Text(tx, "PmtId", "EndToEndId")),
			need(Xml.Amount(need(Xml.Child(tx, "Amt", "InstdAmt")))), creditor, Xml.OptText(tx, "RmtInf", "Ustrd")})
	}
	createdAt, e := parseDateTime(need(Xml.Text(grp, "CreDtTm")))
	if e != nil {
		return p, isoWrap(e, "bad CreDtTm: %v", e)
	}
	execDate, e := time.Parse("2006-01-02", need(Xml.Text(pmt, "ReqdExctnDt", "Dt")))
	if e != nil {
		return p, isoWrap(e, "bad ReqdExctnDt: %v", e)
	}
	p = PaymentInitiation{need(Xml.Text(grp, "MsgId")), createdAt, need(Xml.Text(grp, "InitgPty", "Nm")),
		need(Xml.Text(pmt, "PmtInfId")), execDate, debtor, transfers}
	if err := checkTotals("GrpHdr", grp, p); err != nil {
		return p, err
	}
	if err := checkTotals("PmtInf", pmt, p); err != nil {
		return p, err
	}
	return p, nil
}

// checkTotals: the declared NbOfTxs and CtrlSum must equal what the message actually contains.
func checkTotals(where string, header *Element, p PaymentInitiation) error {
	declaredText, err := Xml.Text(header, "NbOfTxs")
	if err != nil {
		return err
	}
	declaredCount, err := strconv.Atoi(declaredText)
	if err != nil {
		return isoWrap(err, "%s NbOfTxs is not a number: %s", where, declaredText)
	}
	if declaredCount != p.NumberOfTransactions() {
		return isoErr("%s NbOfTxs %d != %d transactions", where, declaredCount, p.NumberOfTransactions())
	}
	computed, err := p.ControlSum()
	if err != nil {
		return isoWrap(err, "control sum: %v", err)
	}
	if ctrl := Xml.OptText(header, "CtrlSum"); ctrl != "" {
		declared, err := Parse(ctrl, EUR)
		if err != nil {
			return isoWrap(err, "bad CtrlSum %s: %v", ctrl, err)
		}
		if declared != computed {
			return isoErr("%s CtrlSum %s != computed %s", where, ctrl, Xml.Decimal(computed))
		}
	}
	return nil
}

// Pacs008 is pacs.008.001.08: FI to FI Customer Credit Transfer, what the debtor bank sends to the clearing system.
var Pacs008 pacs008

type pacs008 struct{}

const Pacs008NS = "urn:iso:std:iso:20022:tech:xsd:pacs.008.001.08"

// FromPain001 is the field mapping pain.001 -> pacs.008. EndToEndId passes through unchanged; TxId is new and
// owned by the debtor bank; ReqdExctnDt becomes IntrBkSttlmDt; InstdAmt becomes IntrBkSttlmAmt (same currency
// in SEPA). A missing creditor BIC (IBAN-only initiation) is derived from the IBAN through the bank's directory,
// which returns "" for an unknown bank.
func (pacs008) FromPain001(p PaymentInitiation, messageId string, createdAt time.Time, bicDirectory func(iban string) string) (InterbankMessage, error) {
	out := []InterbankTransfer{}
	seq := 1
	for _, t := range p.Transfers {
		creditor := t.Creditor
		if creditor.Bic == "" {
			bic := bicDirectory(creditor.Iban)
			if bic == "" {
				return InterbankMessage{}, isoErr("no BIC for IBAN %s in directory", creditor.Iban)
			}
			creditor = Party{creditor.Name, creditor.Iban, bic}
		}
		txId := fmt.Sprintf("%s-%04d", messageId, seq)
		seq++
		out = append(out, InterbankTransfer{t.EndToEndId, txId, t.Amount, p.Debtor, creditor, t.Remittance})
	}
	return InterbankMessage{messageId, createdAt, p.RequestedExecutionDate, out}, nil
}

func (pacs008) Write(m InterbankMessage) (string, error) {
	total, err := m.TotalSettlementAmount()
	if err != nil {
		return "", isoWrap(err, "total settlement amount: %v", err)
	}
	root := &Element{Namespace: Pacs008NS, Name: "Document"}
	msg := Xml.El(root, "FIToFICstmrCdtTrf")
	grp := Xml.El(msg, "GrpHdr")
	Xml.El(grp, "MsgId", m.MessageId)
	Xml.El(grp, "CreDtTm", m.CreatedAt.Format(DateTimeLayout))
	Xml.El(grp, "NbOfTxs", strconv.Itoa(len(m.Transfers)))
	Xml.AmountEl(grp, "TtlIntrBkSttlmAmt", total)
	Xml.El(grp, "IntrBkSttlmDt", isoDate(m.SettlementDate))
	Xml.El(Xml.El(grp, "SttlmInf"), "SttlmMtd", "CLRG")
	for _, t := range m.Transfers {
		tx := Xml.El(msg, "CdtTrfTxInf")
		pmtId := Xml.El(tx, "PmtId")
		Xml.El(pmtId, "EndToEndId", t.EndToEndId)
		Xml.El(pmtId, "TxId", t.TransactionId)
		Xml.El(Xml.El(Xml.El(tx, "PmtTpInf"), "SvcLvl"), "Cd", "SEPA")
		Xml.AmountEl(tx, "IntrBkSttlmAmt", t.Amount)
		Xml.El(tx, "ChrgBr", "SLEV")
		Xml.El(Xml.El(tx, "Dbtr"), "Nm", t.Debtor.Name)
		Xml.Account(tx, "DbtrAcct", t.Debtor.Iban)
		Xml.Agent(tx, "DbtrAgt", t.Debtor.Bic)
		Xml.Agent(tx, "CdtrAgt", t.Creditor.Bic)
		Xml.El(Xml.El(tx, "Cdtr"), "Nm", t.Creditor.Name)
		Xml.Account(tx, "CdtrAcct", t.Creditor.Iban)
		if t.Remittance != "" {
			Xml.El(Xml.El(tx, "RmtInf"), "Ustrd", t.Remittance)
		}
	}
	return Xml.Serialize(root), nil
}

func (pacs008) Parse(doc string) (m InterbankMessage, err error) {
	defer catch(&err)
	msg := need(Xml.ParseRoot(doc, Pacs008NS, "FIToFICstmrCdtTrf"))
	grp := need(Xml.Child(msg, "GrpHdr"))
	transfers := []InterbankTransfer{}
	for _, tx := range Xml.Children(msg, "CdtTrfTxInf") {
		debtor := Party{need(Xml.Text(tx, "Dbtr", "Nm")), need(Xml.Text(tx, "DbtrAcct", "Id", "IBAN")), need(Xml.Text(tx, "DbtrAgt", "FinInstnId", "BICFI"))}
		creditor := Party{need(Xml.Text(tx, "Cdtr", "Nm")), need(Xml.Text(tx, "CdtrAcct", "Id", "IBAN")), need(Xml.Text(tx, "CdtrAgt", "FinInstnId", "BICFI"))}
		transfers = append(transfers, InterbankTransfer{need(Xml.Text(tx, "PmtId", "EndToEndId")), need(Xml.Text(tx, "PmtId", "TxId")),
			need(Xml.Amount(need(Xml.Child(tx, "IntrBkSttlmAmt")))), debtor, creditor, Xml.OptText(tx, "RmtInf", "Ustrd")})
	}
	createdAt, e := parseDateTime(need(Xml.Text(grp, "CreDtTm")))
	if e != nil {
		return m, isoWrap(e, "bad CreDtTm: %v", e)
	}
	settlement, e := time.Parse("2006-01-02", need(Xml.Text(grp, "IntrBkSttlmDt")))
	if e != nil {
		return m, isoWrap(e, "bad IntrBkSttlmDt: %v", e)
	}
	m = InterbankMessage{need(Xml.Text(grp, "MsgId")), createdAt, settlement, transfers}
	declared, e := strconv.Atoi(need(Xml.Text(grp, "NbOfTxs")))
	if e != nil {
		return m, isoWrap(e, "NbOfTxs is not a number")
	}
	if declared != len(transfers) {
		return m, isoErr("NbOfTxs %d != %d transactions", declared, len(transfers))
	}
	total := need(Xml.Amount(need(Xml.Child(grp, "TtlIntrBkSttlmAmt"))))
	computed := need(m.TotalSettlementAmount())
	if total != computed {
		return m, isoErr("TtlIntrBkSttlmAmt %v != computed %v", total, computed)
	}
	return m, nil
}

// formatList prints a slice the way Java prints a List: [a, b, c].
func formatList(items []string) string { return "[" + strings.Join(items, ", ") + "]" }

var Now = time.Date(2026, 10, 13, 9, 30, 0, 0, time.UTC)

func mustMoney(m Money, err error) Money {
	if err != nil {
		panic(err)
	}
	return m
}

func DemoInitiation() PaymentInitiation {
	debtor := Party{"Acme Robotics GmbH", "DE89370400440532013000", "COBADEFFXXX"}
	return PaymentInitiation{"ACME-20261013-001", Now, "Acme Robotics GmbH", "PAYRUN-42", time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC), debtor, []CreditTransfer{
		{"INV-2026-0912", mustMoney(Parse("1250.00", EUR)),
			Party{"Fournitures Lyon SARL", "FR1420041010050500013M02606", "PSSTFRPPLYO"}, "Invoice 2026-0912"},
		{"INV-2026-0913", mustMoney(Parse("349.99", EUR)),
			Party{"Delft Sensors BV", "NL91ABNA0417164300", "ABNANL2A"}, "Invoice 2026-0913"},
		{"INV-2026-0914", mustMoney(Parse("12000.50", EUR)),
			Party{"Componentes Bilbao SL", "ES9121000418450200051332", ""}, "Invoice 2026-0914 batch 7"},
	}}
}

func DemoDirectory() func(iban string) string {
	byBankCode := map[string]string{"ES2100": "CAIXESBBXXX", "NLABNA": "ABNANL2A", "DE37040044": "COBADEFFXXX"}
	return func(iban string) string { return byBankCode[iban[:2]+Iban.BankCode(iban)] }
}

func main() {
	p := DemoInitiation()
	fmt.Println("== SEPA validation ==")
	fmt.Println("violations: " + formatList(SepaRules.Violations(p)))
	ctrl := mustMoney(p.ControlSum())
	fmt.Printf("NbOfTxs %d, CtrlSum %v\n", p.NumberOfTransactions(), ctrl)

	fmt.Println("\n== pain.001.001.09 (customer -> debtor bank) ==")
	pain, err := Pain001.Write(p)
	if err != nil {
		panic(err)
	}
	fmt.Print(pain)
	back, err := Pain001.Parse(pain)
	if err != nil {
		panic(err)
	}
	fmt.Println("round trip equal:", back.Equals(p))

	fmt.Println("\n== pacs.008.001.08 (debtor bank -> clearing) ==")
	m, err := Pacs008.FromPain001(back, "COBA-20261013-7781", Now.Add(5*time.Minute), DemoDirectory())
	if err != nil {
		panic(err)
	}
	pacs, err := Pacs008.Write(m)
	if err != nil {
		panic(err)
	}
	firstTx := strings.Index(pacs, "</CdtTrfTxInf>") + len("</CdtTrfTxInf>")
	fmt.Println(pacs[:firstTx])
	fmt.Println("    ... 2 more CdtTrfTxInf ...")
	parsed, err := Pacs008.Parse(pacs)
	if err != nil {
		panic(err)
	}
	fmt.Println("round trip equal:", parsed.Equals(m))
	for _, t := range parsed.Transfers {
		fmt.Printf("%s -> TxId %s, %v to %s\n", t.EndToEndId, t.TransactionId, t.Amount, t.Creditor.Bic)
	}

	fmt.Println("\n== Rejections ==")
	bad := PaymentInitiation{"ACME/BAD//1", Now, "Acme", "PAYRUN-43", time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC),
		Party{"Acme Robotics GmbH", "DE89370400440532013001", "COBADEFF"}, []CreditTransfer{
			{"INV-1", mustMoney(Parse("10.00", USD)), Party{"Zürich Präzision AG", "NL91ABNA0417164300", ""}, ""}}}
	for _, v := range SepaRules.Violations(bad) {
		fmt.Println("rule: " + v)
	}
	tampered := strings.ReplaceAll(pain, "<CtrlSum>13600.49</CtrlSum>", "<CtrlSum>13600.50</CtrlSum>")
	if _, err := Pain001.Parse(tampered); err != nil {
		fmt.Println("rejected:", err)
	}
	if _, err := Pain001.Parse(strings.ReplaceAll(pain, "349.99", "349.999")); err != nil {
		fmt.Println("rejected:", err)
	}
	if _, err := Pacs008.Parse(pain); err != nil {
		fmt.Println("rejected:", err)
	}
	xxe := "<?xml version=\"1.0\"?><!DOCTYPE d [<!ENTITY x SYSTEM \"file:///etc/passwd\">]><Document xmlns=\"" + Pain001NS + "\">&x;</Document>"
	if _, err := Pain001.Parse(xxe); err != nil {
		fmt.Println("rejected: DOCTYPE (XXE guard)")
	}
}
