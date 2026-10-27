// Project: projects/ledger-engine — stage 4: statement lines, breaks, and the reconciliation report.
// Docs: projects/ledger-engine/stages/04-reconcile-against-statement/docs/en.md
// Run:  go run .
// Test: python3 scripts/project_test.py ledger-engine --lang go --all --solution
// Go 1.22+, standard library only. Same design as the Java and Python solutions.
package main

import (
	"errors"
	"strings"
)

// StatementLine is one line of an external statement (a processor settlement file, a bank statement). The
// reference is what we match on first; the amount is signed in the account's normal direction (money in is
// positive for a bank asset account); the date is ISO-8601 ("2026-10-01").
type StatementLine struct {
	Ref    string
	Amount Money
	Date   string
}

// NewStatementLine rejects a blank reference or date.
func NewStatementLine(ref string, amount Money, date string) (StatementLine, error) {
	if strings.TrimSpace(ref) == "" {
		return StatementLine{}, errors.New("statement line needs a reference")
	}
	if strings.TrimSpace(date) == "" {
		return StatementLine{}, errors.New("statement line needs a date")
	}
	return StatementLine{ref, amount, date}, nil
}

// LedgerLine is our side of the same shape: one journal entry's net effect on one account, keyed by the entry id.
type LedgerLine struct {
	Ref    string
	Amount Money
	Date   string
}

// BreakType is one of the four ways a statement line and a journal entry can fail to agree.
type BreakType int

const (
	MissingInLedger    BreakType = iota // the statement has it, our journal does not
	MissingInStatement                  // our journal has it, the statement does not
	AmountMismatch                      // same reference on both sides, different amounts
	Duplicate                           // the statement listed the same reference twice
)

func (b BreakType) String() string {
	return [...]string{"MISSING_IN_LEDGER", "MISSING_IN_STATEMENT", "AMOUNT_MISMATCH", "DUPLICATE"}[b]
}

// MatchedBy says how a match was found: by shared reference, or by equal amount within the date window.
type MatchedBy int

const (
	ByRef MatchedBy = iota
	ByAmountAndDate
)

func (m MatchedBy) String() string {
	if m == ByRef {
		return "REF"
	}
	return "AMOUNT_AND_DATE"
}

type ReconciliationMatch struct {
	LedgerRef    string
	StatementRef string
	Amount       Money
	By           MatchedBy
}

// ReconciliationBreak carries both sides; the absent side is the currency's zero, and the type says which side is absent.
type ReconciliationBreak struct {
	Type            BreakType
	Ref             string
	LedgerAmount    Money
	StatementAmount Money
}

// ReconciliationReport is everything the reconciliation found, with totals that prove nothing was dropped:
// LedgerTotal == MatchedTotal() + LedgerBreakTotal() and StatementTotal == MatchedTotal() + StatementBreakTotal().
// The slices are the report's own; Reconcile builds fresh ones for every call.
type ReconciliationReport struct {
	AccountID      string
	Currency       Currency
	Matches        []ReconciliationMatch
	Breaks         []ReconciliationBreak
	LedgerTotal    Money
	StatementTotal Money
}

func (r ReconciliationReport) MatchedTotal() Money {
	acc := OfMinor(0, r.Currency)
	for _, m := range r.Matches {
		acc = must(acc.Plus(m.Amount))
	}
	return acc
}

// LedgerBreakTotal is the ledger-side amounts of every break (zero for breaks the ledger never saw).
func (r ReconciliationReport) LedgerBreakTotal() Money {
	acc := OfMinor(0, r.Currency)
	for _, b := range r.Breaks {
		acc = must(acc.Plus(b.LedgerAmount))
	}
	return acc
}

// StatementBreakTotal is the statement-side amounts of every break (zero for breaks the statement never saw).
func (r ReconciliationReport) StatementBreakTotal() Money {
	acc := OfMinor(0, r.Currency)
	for _, b := range r.Breaks {
		acc = must(acc.Plus(b.StatementAmount))
	}
	return acc
}

func (r ReconciliationReport) BreaksOf(kind BreakType) []ReconciliationBreak {
	out := []ReconciliationBreak{}
	for _, b := range r.Breaks {
		if b.Type == kind {
			out = append(out, b)
		}
	}
	return out
}

// TiesOut is true when every minor unit on both sides is accounted for by exactly one match or one break.
func (r ReconciliationReport) TiesOut() bool {
	matched := r.MatchedTotal()
	return r.LedgerTotal == must(matched.Plus(r.LedgerBreakTotal())) &&
		r.StatementTotal == must(matched.Plus(r.StatementBreakTotal()))
}

func (r ReconciliationReport) IsClean() bool { return len(r.Breaks) == 0 }
