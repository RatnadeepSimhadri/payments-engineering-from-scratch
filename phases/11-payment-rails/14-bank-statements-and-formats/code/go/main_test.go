// Tests for phases/11-payment-rails/14-bank-statements-and-formats. Run: go test ./...
package main

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

func check(t *testing.T, name string, condition bool) {
	t.Helper()
	if !condition {
		t.Errorf("%s", name)
	}
}

func checkParseErr(t *testing.T, name string, err error) {
	t.Helper()
	if err == nil {
		t.Errorf("%s (no error)", name)
	} else if !errors.Is(err, ErrStatementParse) {
		t.Errorf("%s (wrong error %v)", name, err)
	}
}

type parsed struct {
	s   Statement
	err error
}

func got(s Statement, err error) parsed { return parsed{s, err} }

func parse(t *testing.T, p parsed) Statement {
	t.Helper()
	if p.err != nil {
		t.Fatalf("parse: %v", p.err)
	}
	return p.s
}

type parsedAll struct {
	s   []Statement
	err error
}

func gotAll(s []Statement, err error) parsedAll { return parsedAll{s, err} }

func parseAll(t *testing.T, p parsedAll) []Statement {
	t.Helper()
	if p.err != nil {
		t.Fatalf("parse all: %v", p.err)
	}
	return p.s
}

func amounts(s Statement) []int64 {
	out := []int64{}
	for _, l := range s.Lines {
		out = append(out, l.Amount.MinorUnits)
	}
	return out
}

var expected = []int64{-150000, 325075, -1250}

func TestDetection(t *testing.T) {
	cases := []struct {
		text string
		want StatementFormat
	}{{SampleMT940, MT940}, {SampleCAMT053, CAMT053}, {SampleBAI2, BAI2}, {SampleOFX, OFX}}
	for _, c := range cases {
		got, err := DetectFormat(c.text)
		check(t, "detect "+c.want.String(), err == nil && got == c.want)
	}
	_, err := DetectFormat("Date,Amount\n")
	checkParseErr(t, "CSV is not a bank statement format", err)
}

func TestAllFourNormalizeToTheSameMovements(t *testing.T) {
	for _, sample := range []string{SampleMT940, SampleCAMT053, SampleBAI2, SampleOFX} {
		s := parse(t, got(StatementParsers.Parse(sample)))
		t.Run(s.Source.String(), func(t *testing.T) {
			check(t, fmt.Sprintf("%s movements", s.Source), slices.Equal(amounts(s), expected))
			check(t, fmt.Sprintf("%s closing 14238.25", s.Source), s.Closing.MinorUnits == 1423825)
			check(t, fmt.Sprintf("%s continuity holds", s.Source), len(s.ContinuityErrors()) == 0)
		})
	}
}

func TestMt940(t *testing.T) {
	mt := parse(t, got(Mt940Parser.Parse(SampleMT940)))
	check(t, "MT940 account from :25:", mt.AccountId == "DE89370400440532013000")
	check(t, "MT940 statement id includes :28C:", mt.StatementId == "STMT261012/00287/001")
	check(t, "MT940 opening date from :60F:", mt.OpeningDate.Equal(time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)))
	check(t, "MT940 :86: continuation joined", mt.Lines[0].Description == "Supplier payment INV-4411 Fournitures Lyon SARL")
	check(t, "MT940 NONREF becomes empty reference", mt.Lines[2].Reference == "")
	check(t, "MT940 bank reference after //", mt.Lines[1].BankReference == "COBA0002")
	rc := parse(t, got(Mt940Parser.Parse(strings.ReplaceAll(strings.ReplaceAll(SampleMT940, "2610121012C3250,75", "2610121012RC3250,75"),
		":62F:C261012EUR14238,25", ":62F:C261012EUR7736,75"))))
	check(t, "MT940 RC (reversal of credit) is a debit", rc.Lines[1].Amount.MinorUnits == -325075 && len(rc.ContinuityErrors()) == 0)
	yearEnd := parse(t, got(Mt940Parser.Parse(strings.ReplaceAll(SampleMT940, "2610121012D1500,00", "2701021231D1500,00"))))
	check(t, "MT940 entry date 1231 with value date 270102 is 2026-12-31", yearEnd.Lines[0].BookingDate.Equal(time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC)))
	debitOpen := parse(t, got(Mt940Parser.Parse(strings.ReplaceAll(strings.ReplaceAll(SampleMT940, ":60F:C261009EUR12500,00", ":60F:D261009EUR100,00"),
		":62F:C261012EUR14238,25", ":62F:C261012EUR1638,25"))))
	check(t, "MT940 D balance is negative (overdrawn)", debitOpen.Opening.MinorUnits == -10000 && len(debitOpen.ContinuityErrors()) == 0)
	_, err := Mt940Parser.Parse(strings.ReplaceAll(SampleMT940, "D12,50NCHG", "D12,505NCHG"))
	checkParseErr(t, "MT940 three decimals rejected", err)
	_, err = Mt940Parser.Parse(strings.ReplaceAll(SampleMT940, ":62F:C261012EUR14238,25\n", ""))
	checkParseErr(t, "MT940 missing closing balance", err)
}

func TestContinuityAndChains(t *testing.T) {
	broken := parse(t, got(Mt940Parser.Parse(strings.ReplaceAll(SampleMT940, ":62F:C261012EUR14238,25", ":62F:C261012EUR14283,25"))))
	errs := broken.ContinuityErrors()
	check(t, "transposed closing balance detected", len(errs) == 1 && strings.Contains(errs[0], "difference 45.00 EUR"))
	chain := parseAll(t, gotAll(Mt940Parser.ParseAll(SampleMT940+fmt.Sprintf(SampleMT940Day2, "14238,25", "14338,25"))))
	check(t, "two statements in one MT940 file", len(chain) == 2)
	check(t, "continuous chain has no gaps", len(StatementChain.Gaps(chain)) == 0)
	gapped := parseAll(t, gotAll(Mt940Parser.ParseAll(SampleMT940+fmt.Sprintf(SampleMT940Day2, "14250,75", "14350,75"))))
	check(t, "gapped day 2 is internally continuous", len(gapped[1].ContinuityErrors()) == 0)
	gaps := StatementChain.Gaps(gapped)
	check(t, "chain gap of 12.50 detected", len(gaps) == 1 && strings.Contains(gaps[0], "missing 12.50 EUR"))
	check(t, "out-of-order chain detected", len(StatementChain.Gaps([]Statement{chain[1], chain[0]})) > 0)
}

func TestCamt053(t *testing.T) {
	camt := parse(t, got(Camt053Parser.Parse(SampleCAMT053)))
	check(t, "camt PDNG entry excluded", len(camt.Lines) == 3)
	check(t, "camt EndToEndId is the reference", camt.Lines[0].Reference == "INV-4411")
	reversed := parse(t, got(Camt053Parser.Parse(strings.ReplaceAll(SampleCAMT053,
		"<Sts><Cd>BOOK</Cd></Sts>\n        <BookgDt><Dt>2026-10-12</Dt></BookgDt><ValDt><Dt>2026-10-12</Dt></ValDt>\n        <AcctSvcrRef>COBA0003",
		"<Sts><Cd>BOOK</Cd></Sts><RvslInd>true</RvslInd>\n        <BookgDt><Dt>2026-10-12</Dt></BookgDt><ValDt><Dt>2026-10-12</Dt></ValDt>\n        <AcctSvcrRef>COBA0003"))))
	check(t, "camt RvslInd flips the sign", reversed.Lines[2].Amount.MinorUnits == 1250)
	_, err := Camt053Parser.Parse(strings.ReplaceAll(SampleCAMT053, "<Amt Ccy=\"EUR\">3250.75", "<Amt Ccy=\"USD\">3250.75"))
	checkParseErr(t, "camt amount in wrong currency", err)
	_, err = Camt053Parser.Parse("<?xml version=\"1.0\"?><!DOCTYPE d [<!ENTITY x SYSTEM \"file:///etc/passwd\">]><Document xmlns=\"" + Camt053NS + "\">&x;</Document>")
	checkParseErr(t, "camt DOCTYPE rejected", err)
	_, err = Camt053Parser.Parse(strings.Replace(SampleCAMT053, "<Id>STMT261012</Id>", "<Id>&x;</Id>", 1))
	checkParseErr(t, "camt undefined entity rejected (Strict)", err)
	_, err = Camt053Parser.Parse(strings.ReplaceAll(SampleCAMT053, "camt.053.001.08", "camt.053.001.02"))
	checkParseErr(t, "camt unexpected namespace rejected", err)
}

func TestBai2(t *testing.T) {
	bai := parse(t, got(Bai2Parser.Parse(SampleBAI2)))
	check(t, "BAI2 88 continuation appended to 16 text", bai.Lines[0].Description == "Supplier payment INV-4411 Fournitures Lyon SARL")
	check(t, "BAI2 type 495 is a debit, 195 a credit", bai.Lines[0].Amount.IsNegative() && bai.Lines[1].Amount.IsPositive())
	rejects := []struct{ name, from, to string }{
		{"BAI2 account control total", "49,3150150,6/", "49,3150151,6/"},
		{"BAI2 record count includes 88", "49,3150150,6/", "49,3150150,5/"},
		{"BAI2 file record count", "99,3150150,1,10/", "99,3150150,1,9/"},
		{"BAI2 missing 99 trailer", "99,3150150,1,10/\n", ""},
	}
	for _, r := range rejects {
		t.Run(r.name, func(t *testing.T) {
			_, err := Bai2Parser.Parse(strings.ReplaceAll(SampleBAI2, r.from, r.to))
			checkParseErr(t, r.name, err)
		})
	}
}

func TestOfx(t *testing.T) {
	ofx := parse(t, got(OfxParser.Parse(SampleOFX)))
	check(t, "OFX opening is derived, not reported", !ofx.OpeningReported && ofx.Opening.MinorUnits == 1250000)
	check(t, "OFX FITID is the bank reference", ofx.Lines[2].BankReference == "COBA0003")
	ofxXml := parse(t, got(OfxParser.Parse(strings.ReplaceAll(strings.ReplaceAll(SampleOFX, "<TRNAMT>-12.50", "<TRNAMT>-12.50</TRNAMT>"), "<CURDEF>USD", "<CURDEF>USD</CURDEF>"))))
	check(t, "OFX 2.x style closing tags on leaves parse the same", slices.Equal(amounts(ofxXml), expected))
	_, err := OfxParser.Parse(strings.ReplaceAll(SampleOFX, "<LEDGERBAL><BALAMT>14238.25<DTASOF>20261012220000</LEDGERBAL>", ""))
	checkParseErr(t, "OFX without LEDGERBAL", err)
}
