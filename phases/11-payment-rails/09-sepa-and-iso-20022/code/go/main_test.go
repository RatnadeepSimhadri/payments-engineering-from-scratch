// Tests for phases/11-payment-rails/09-sepa-and-iso-20022. Run: go test ./...
package main

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func eur(t *testing.T, s string) Money {
	t.Helper()
	m, err := Parse(s, EUR)
	if err != nil {
		t.Fatalf("Parse(%q): %v", s, err)
	}
	return m
}

func with(tr CreditTransfer) PaymentInitiation {
	p := DemoInitiation()
	return PaymentInitiation{p.MessageId, p.CreatedAt, p.InitiatingParty, p.PaymentInfoId,
		p.RequestedExecutionDate, p.Debtor, []CreditTransfer{tr}}
}

func anyContains(vs []string, needle string) bool {
	for _, v := range vs {
		if strings.Contains(v, needle) {
			return true
		}
	}
	return false
}

func check(t *testing.T, name string, condition bool) {
	t.Helper()
	if !condition {
		t.Errorf("%s", name)
	}
}

func checkIsoErr(t *testing.T, name string, err error) {
	t.Helper()
	if err == nil {
		t.Errorf("%s (no error)", name)
	} else if !errors.Is(err, ErrIso20022) {
		t.Errorf("%s (wrong error %v)", name, err)
	}
}

func writePain(t *testing.T, p PaymentInitiation) string {
	t.Helper()
	s, err := Pain001.Write(p)
	if err != nil {
		t.Fatalf("Pain001.Write: %v", err)
	}
	return s
}

func TestIdentifiers(t *testing.T) {
	cases := []struct {
		name  string
		valid bool
		iban  string
	}{
		{"valid DE IBAN", true, "DE89370400440532013000"},
		{"valid FR IBAN with letter in BBAN", true, "FR1420041010050500013M02606"},
		{"one digit changed fails mod-97", false, "DE89370400440532013001"},
		{"wrong length for country fails", false, "DE8937040044053201300"},
		{"lowercase rejected (normalize first)", false, "de89370400440532013000"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { check(t, c.name, Iban.IsValid(c.iban) == c.valid) })
	}
	check(t, "BIC8 valid", Bic.IsValid("ABNANL2A"))
	check(t, "BIC11 valid", Bic.IsValid("COBADEFFXXX"))
	check(t, "BIC with 9 chars invalid", !Bic.IsValid("COBADEFFX"))
	check(t, "DE bank code is the Bankleitzahl", Iban.BankCode("DE89370400440532013000") == "37040044")
}

func TestSepaRules(t *testing.T) {
	p := DemoInitiation()
	check(t, "demo initiation has no violations", len(SepaRules.Violations(p)) == 0)
	ctrl, err := p.ControlSum()
	check(t, "control sum is exact", err == nil && ctrl == eur(t, "13600.49"))
	cred := Party{"Delft Sensors BV", "NL91ABNA0417164300", "ABNANL2A"}
	usd, _ := Parse("1.00", USD)
	check(t, "non-EUR rejected", anyContains(SepaRules.Violations(with(CreditTransfer{"E2E-1", usd, cred, ""})), "must be EUR"))
	check(t, "zero amount rejected", anyContains(SepaRules.Violations(with(CreditTransfer{"E2E-1", eur(t, "0.00"), cred, ""})), "out of range"))
	check(t, "max amount 999999999.99 accepted", len(SepaRules.Violations(with(CreditTransfer{"E2E-1", eur(t, "999999999.99"), cred, ""}))) == 0)
	check(t, "1000000000.00 rejected", len(SepaRules.Violations(with(CreditTransfer{"E2E-1", eur(t, "1000000000.00"), cred, ""}))) > 0)
	check(t, "umlaut outside SEPA character set", anyContains(SepaRules.Violations(with(CreditTransfer{"E2E-1", eur(t, "1.00"),
		Party{"Zürich AG", "NL91ABNA0417164300", ""}, ""})), "outside the SEPA set"))
	check(t, "36-char EndToEndId rejected", len(SepaRules.Violations(with(CreditTransfer{strings.Repeat("X", 36), eur(t, "1.00"), cred, ""}))) > 0)
	check(t, "'//' in EndToEndId rejected", len(SepaRules.Violations(with(CreditTransfer{"A//B", eur(t, "1.00"), cred, ""}))) > 0)
	check(t, "141-char remittance rejected", len(SepaRules.Violations(with(CreditTransfer{"E2E-1", eur(t, "1.00"), cred, strings.Repeat("R", 141)}))) > 0)
	check(t, "bad creditor IBAN rejected", len(SepaRules.Violations(with(CreditTransfer{"E2E-1", eur(t, "1.00"),
		Party{"X", "NL91ABNA0417164301", ""}, ""}))) > 0)
	_, err = Pain001.Write(with(CreditTransfer{"E2E-1", eur(t, "0.00"), cred, ""}))
	checkIsoErr(t, "write refuses a message with violations", err)
	check(t, "first outside reports the code point", SepaRules.FirstOutside("Zürich") == "U+00FC at index 1")
}

func TestPain001RoundTripAndTotals(t *testing.T) {
	p := DemoInitiation()
	pain := writePain(t, p)
	check(t, "pain.001 namespace", strings.Contains(pain, "urn:iso:std:iso:20022:tech:xsd:pain.001.001.09"))
	check(t, "pain.001 amount format", strings.Contains(pain, "<InstdAmt Ccy=\"EUR\">12000.50</InstdAmt>"))
	check(t, "pain.001 IBAN-only creditor has no CdtrAgt", strings.Count(pain, "<CdtrAgt>") == 2)
	back, err := Pain001.Parse(pain)
	check(t, "pain.001 round trip", err == nil && back.Equals(p))

	rejects := []struct {
		name string
		doc  string
	}{
		{"CtrlSum mismatch rejected", strings.ReplaceAll(pain, "<CtrlSum>13600.49</CtrlSum>", "<CtrlSum>13600.50</CtrlSum>")},
		{"NbOfTxs mismatch rejected", strings.Replace(pain, "<NbOfTxs>3</NbOfTxs>", "<NbOfTxs>4</NbOfTxs>", 1)},
		{"three decimals rejected", strings.ReplaceAll(pain, "349.99", "349.999")},
		{"non-EUR Ccy attribute rejected", strings.ReplaceAll(pain, "Ccy=\"EUR\">349.99", "Ccy=\"USD\">349.99")},
		{"missing mandatory element rejected", strings.ReplaceAll(pain, "<PmtInfId>PAYRUN-42</PmtInfId>", "")},
		{"malformed XML rejected", pain[:len(pain)/2]},
		{"DOCTYPE rejected (XXE)", "<?xml version=\"1.0\"?><!DOCTYPE d [<!ENTITY x SYSTEM \"file:///etc/passwd\">]><Document xmlns=\"" + Pain001NS + "\">&x;</Document>"},
		{"undefined entity rejected (Strict)", strings.Replace(pain, "<Nm>Acme Robotics GmbH</Nm>", "<Nm>&x;</Nm>", 1)},
	}
	for _, r := range rejects {
		t.Run(r.name, func(t *testing.T) {
			_, err := Pain001.Parse(r.doc)
			checkIsoErr(t, r.name, err)
		})
	}
}

func TestPacs008MappingAndRoundTrip(t *testing.T) {
	p := DemoInitiation()
	m, err := Pacs008.FromPain001(p, "COBA-1", Now, DemoDirectory())
	if err != nil {
		t.Fatalf("FromPain001: %v", err)
	}
	check(t, "EndToEndId passes through", m.Transfers[0].EndToEndId == "INV-2026-0912")
	check(t, "TxId assigned by debtor bank", m.Transfers[2].TransactionId == "COBA-1-0003")
	check(t, "settlement date is requested execution date", m.SettlementDate.Equal(time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)))
	check(t, "IBAN-only creditor BIC derived from directory", m.Transfers[2].Creditor.Bic == "CAIXESBBXXX")
	total, _ := m.TotalSettlementAmount()
	ctrl, _ := p.ControlSum()
	check(t, "total settlement amount equals control sum", total == ctrl)
	pacs, err := Pacs008.Write(m)
	if err != nil {
		t.Fatalf("Pacs008.Write: %v", err)
	}
	check(t, "pacs.008 has SLEV and CLRG", strings.Contains(pacs, "<ChrgBr>SLEV</ChrgBr>") && strings.Contains(pacs, "<SttlmMtd>CLRG</SttlmMtd>"))
	parsed, err := Pacs008.Parse(pacs)
	check(t, "pacs.008 round trip", err == nil && parsed.Equals(m))
	_, err = Pacs008.Parse(strings.ReplaceAll(pacs, ">13600.49<", ">13600.48<"))
	checkIsoErr(t, "TtlIntrBkSttlmAmt mismatch rejected", err)
	pain := writePain(t, p)
	_, err = Pacs008.Parse(pain)
	checkIsoErr(t, "pain.001 is not a pacs.008", err)
	_, err = Pacs008.FromPain001(with(CreditTransfer{"E2E-1", eur(t, "1.00"), Party{"X", "BE68539007547034", ""}, ""}), "M", Now, DemoDirectory())
	checkIsoErr(t, "unknown bank in directory", err)
}
