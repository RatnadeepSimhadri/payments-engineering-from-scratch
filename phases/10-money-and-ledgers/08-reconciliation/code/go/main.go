// Lesson: phases/10-money-and-ledgers/08-reconciliation/docs/en.md
// Reconciliation: Matching Your Ledger Against Theirs
// Run:  go run .
// Test: go test ./...
// Sources: ISO 4217 (minor units); ISO 20022 camt.053 (bank statement structure the "theirs" side models).
// Go 1.22, standard library only. Money is integer minor units, copied from Phase 10, Lesson 01 (money.go).
package main

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Zero is the zero amount of a currency.
func Zero(currency Currency) Money { return Money{0, currency} }

// Date is a civil date at UTC midnight; LocalDate in the Java version.
type Date = time.Time

func ParseDate(s string) (Date, error) { return time.Parse("2006-01-02", s) }

func formatDate(d Date) string { return d.Format("2006-01-02") }

// daysBetween is ChronoUnit.DAYS.between for two civil dates.
func daysBetween(a, b Date) int64 { return int64(b.Sub(a).Hours() / 24) }

// LedgerPosting is one line from OUR ledger: a posting against the settlement account that we expect the processor to confirm.
type LedgerPosting struct {
	Reference string
	Date      Date
	Amount    Money
	Account   string
}

// StatementLine is one line from THEIR statement: what the processor or bank says happened.
type StatementLine struct {
	Reference   string
	Date        Date
	Amount      Money
	Description string
}

// MatchRule says which pass produced a match. ExactReference is strong evidence; AmountDate is a heuristic that a human may review.
type MatchRule int

const (
	ExactReference MatchRule = iota
	AmountDate
)

func (r MatchRule) String() string { return [...]string{"EXACT_REFERENCE", "AMOUNT_DATE"}[r] }

// Match is a posting of ours paired with a line of theirs. Amounts are equal by construction (NewMatch checks).
type Match struct {
	Ours   LedgerPosting
	Theirs StatementLine
	Rule   MatchRule
}

func NewMatch(ours LedgerPosting, theirs StatementLine, rule MatchRule) (Match, error) {
	if ours.Amount != theirs.Amount {
		return Match{}, fmt.Errorf("a Match must have equal amounts; got %v vs %v", ours.Amount, theirs.Amount)
	}
	return Match{ours, theirs, rule}, nil
}

// BreakKind lists the five ways two books disagree. The order of the constants is the order breaks are reported in,
// which is also the order an operations analyst works them: money that is missing first, duplicates last.
type BreakKind int

const (
	MissingOurs     BreakKind = iota // on their statement, nowhere in our ledger
	MissingTheirs                    // in our ledger, nowhere on their statement
	AmountMismatch                   // same reference on both sides, different amount
	DuplicateOurs                    // our ledger has more copies of a reference than their statement
	DuplicateTheirs                  // their statement has more copies of a reference than our ledger
)

func (k BreakKind) String() string {
	return [...]string{"MISSING_OURS", "MISSING_THEIRS", "AMOUNT_MISMATCH", "DUPLICATE_OURS", "DUPLICATE_THEIRS"}[k]
}

// Break is one unexplained item. Exactly one of Ours/Theirs is nil for Missing* and Duplicate*; both are set for AmountMismatch.
type Break struct {
	Kind   BreakKind
	Ours   *LedgerPosting
	Theirs *StatementLine
}

func NewBreak(kind BreakKind, ours *LedgerPosting, theirs *StatementLine) (Break, error) {
	switch kind {
	case MissingOurs, DuplicateTheirs:
		if ours != nil || theirs == nil {
			return Break{}, fmt.Errorf("%v needs theirs only", kind)
		}
	case MissingTheirs, DuplicateOurs:
		if ours == nil || theirs != nil {
			return Break{}, fmt.Errorf("%v needs ours only", kind)
		}
	case AmountMismatch:
		if ours == nil || theirs == nil {
			return Break{}, fmt.Errorf("%v needs both sides", kind)
		}
	}
	return Break{kind, ours, theirs}, nil
}

// OursAmount is our side's amount in this break, or zero.
func (b Break) OursAmount(currency Currency) Money {
	if b.Ours == nil {
		return Zero(currency)
	}
	return b.Ours.Amount
}

// TheirsAmount is their side's amount in this break, or zero.
func (b Break) TheirsAmount(currency Currency) Money {
	if b.Theirs == nil {
		return Zero(currency)
	}
	return b.Theirs.Amount
}

// Difference is ours - theirs: positive means we recorded more than they did.
func (b Break) Difference(currency Currency) Money {
	d, err := b.OursAmount(currency).Minus(b.TheirsAmount(currency))
	if err != nil {
		panic(err) // both sides were summed in this currency already
	}
	return d
}

func (b Break) Reference() string {
	if b.Ours != nil {
		return b.Ours.Reference
	}
	return b.Theirs.Reference
}

func (b Break) Date() Date {
	if b.Ours != nil {
		return b.Ours.Date
	}
	return b.Theirs.Date
}

// ReconciliationReport is the result of one reconciliation run. Every input record appears in exactly one Match or
// exactly one Break, which is what makes the totals tie out: LedgerTotal == MatchedTotal + OursUnmatched and
// StatementTotal == MatchedTotal + TheirsUnmatched.
type ReconciliationReport struct {
	Currency       Currency
	Matches        []Match
	Breaks         []Break
	LedgerTotal    Money
	StatementTotal Money
}

func sumMoney(currency Currency, n int, at func(int) Money) Money {
	total := Zero(currency)
	for i := 0; i < n; i++ {
		var err error
		if total, err = total.Plus(at(i)); err != nil {
			panic(err) // every amount was already checked against the report currency
		}
	}
	return total
}

func (r ReconciliationReport) MatchedTotal() Money {
	return sumMoney(r.Currency, len(r.Matches), func(i int) Money { return r.Matches[i].Ours.Amount })
}

func (r ReconciliationReport) OursUnmatched() Money {
	return sumMoney(r.Currency, len(r.Breaks), func(i int) Money { return r.Breaks[i].OursAmount(r.Currency) })
}

func (r ReconciliationReport) TheirsUnmatched() Money {
	return sumMoney(r.Currency, len(r.Breaks), func(i int) Money { return r.Breaks[i].TheirsAmount(r.Currency) })
}

// NetDifference is LedgerTotal - StatementTotal, which must equal OursUnmatched - TheirsUnmatched.
func (r ReconciliationReport) NetDifference() Money {
	d, _ := r.LedgerTotal.Minus(r.StatementTotal)
	return d
}

// TiesOut is the tie-out: the only number a controller signs.
func (r ReconciliationReport) TiesOut() bool {
	matched := r.MatchedTotal()
	a, _ := matched.Plus(r.OursUnmatched())
	b, _ := matched.Plus(r.TheirsUnmatched())
	c, _ := r.OursUnmatched().Minus(r.TheirsUnmatched())
	return r.LedgerTotal == a && r.StatementTotal == b && r.NetDifference() == c
}

func (r ReconciliationReport) CountBreaks(kind BreakKind) int {
	n := 0
	for _, b := range r.Breaks {
		if b.Kind == kind {
			n++
		}
	}
	return n
}

func (r ReconciliationReport) IsClean() bool { return len(r.Breaks) == 0 }

func (r ReconciliationReport) Render() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "MATCHES (%d)\n", len(r.Matches))
	for _, m := range r.Matches {
		ref := m.Ours.Reference
		if m.Ours.Reference != m.Theirs.Reference {
			ref = m.Ours.Reference + " <-> " + m.Theirs.Reference
		}
		fmt.Fprintf(&sb, "  %-16v %s %12v  %s\n", m.Rule, formatDate(m.Ours.Date), m.Ours.Amount, ref)
	}
	fmt.Fprintf(&sb, "BREAKS (%d)\n", len(r.Breaks))
	for _, b := range r.Breaks {
		fmt.Fprintf(&sb, "  %-16v %s ours=%12v theirs=%12v diff=%12v  %s\n", b.Kind, formatDate(b.Date()),
			b.OursAmount(r.Currency), b.TheirsAmount(r.Currency), b.Difference(r.Currency), b.Reference())
	}
	sb.WriteString("TOTALS\n")
	fmt.Fprintf(&sb, "  ledger total      %14v\n", r.LedgerTotal)
	fmt.Fprintf(&sb, "  statement total   %14v\n", r.StatementTotal)
	fmt.Fprintf(&sb, "  matched           %14v\n", r.MatchedTotal())
	fmt.Fprintf(&sb, "  ours unmatched    %14v\n", r.OursUnmatched())
	fmt.Fprintf(&sb, "  theirs unmatched  %14v\n", r.TheirsUnmatched())
	diff, _ := r.OursUnmatched().Minus(r.TheirsUnmatched())
	fmt.Fprintf(&sb, "  net difference    %14v  (ours unmatched - theirs unmatched = %v)\n", r.NetDifference(), diff)
	tie := "NO"
	if r.TiesOut() {
		tie = "YES"
	}
	sb.WriteString("  ties out          " + tie + "\n")
	return sb.String()
}

// Reconciler is a three-pass matcher. Pass 1 pairs by reference id (and finds amount mismatches and duplicates).
// Pass 2 pairs the leftovers by equal amount and a date within toleranceDays, nearest date first.
// Pass 3 labels whatever is still unmatched as missing on one side. Every step is deterministic:
// inputs are sorted by (date, reference, amount, input position) before any decision is made.
type Reconciler struct {
	currency      Currency
	toleranceDays int
}

func NewReconciler(currency Currency, toleranceDays int) (*Reconciler, error) {
	if toleranceDays < 0 {
		return nil, errors.New("toleranceDays must be >= 0")
	}
	return &Reconciler{currency, toleranceDays}, nil
}

func oursLess(a, b LedgerPosting) bool {
	if !a.Date.Equal(b.Date) {
		return a.Date.Before(b.Date)
	}
	if a.Reference != b.Reference {
		return a.Reference < b.Reference
	}
	return a.Amount.MinorUnits < b.Amount.MinorUnits
}

func theirsLess(a, b StatementLine) bool {
	if !a.Date.Equal(b.Date) {
		return a.Date.Before(b.Date)
	}
	if a.Reference != b.Reference {
		return a.Reference < b.Reference
	}
	return a.Amount.MinorUnits < b.Amount.MinorUnits
}

func (r *Reconciler) Reconcile(ledger []LedgerPosting, statement []StatementLine) (ReconciliationReport, error) {
	ours := append([]LedgerPosting(nil), ledger...)
	theirs := append([]StatementLine(nil), statement...)
	ledgerTotal := Zero(r.currency)
	for _, p := range ours {
		var err error
		if ledgerTotal, err = ledgerTotal.Plus(p.Amount); err != nil { // a foreign currency is an error, never summed
			return ReconciliationReport{}, err
		}
	}
	statementTotal := Zero(r.currency)
	for _, l := range theirs {
		var err error
		if statementTotal, err = statementTotal.Plus(l.Amount); err != nil {
			return ReconciliationReport{}, err
		}
	}
	sort.SliceStable(ours, func(i, j int) bool { return oursLess(ours[i], ours[j]) }) // stable: equal keys keep input order
	sort.SliceStable(theirs, func(i, j int) bool { return theirsLess(theirs[i], theirs[j]) })

	var matches []Match
	var breaks []Break
	var oursLeft []LedgerPosting
	var theirsLeft []StatementLine

	matchByReference(ours, theirs, &matches, &breaks, &oursLeft, &theirsLeft)
	r.matchByAmountAndDate(&oursLeft, &theirsLeft, &matches)
	for i := range oursLeft {
		breaks = append(breaks, Break{MissingTheirs, &oursLeft[i], nil})
	}
	for i := range theirsLeft {
		breaks = append(breaks, Break{MissingOurs, nil, &theirsLeft[i]})
	}

	sort.SliceStable(matches, func(i, j int) bool { return oursLess(matches[i].Ours, matches[j].Ours) })
	sort.SliceStable(breaks, func(i, j int) bool {
		a, b := breaks[i], breaks[j]
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if !a.Date().Equal(b.Date()) {
			return a.Date().Before(b.Date())
		}
		return a.Reference() < b.Reference()
	})
	report := ReconciliationReport{r.currency, matches, breaks, ledgerTotal, statementTotal}
	if !report.TiesOut() {
		panic("reconciliation report does not tie out; this is a bug in the matcher")
	}
	return report, nil
}

// matchByReference is pass 1: group both sides by reference. Keys are sorted so the pass is deterministic (TreeMap in Java).
func matchByReference(ours []LedgerPosting, theirs []StatementLine,
	matches *[]Match, breaks *[]Break, oursLeft *[]LedgerPosting, theirsLeft *[]StatementLine) {
	oursByRef := map[string][]LedgerPosting{}
	theirsByRef := map[string][]StatementLine{}
	for _, p := range ours {
		oursByRef[p.Reference] = append(oursByRef[p.Reference], p)
	}
	for _, l := range theirs {
		theirsByRef[l.Reference] = append(theirsByRef[l.Reference], l)
	}
	for _, ref := range sortedKeys(oursByRef) {
		o, t := oursByRef[ref], theirsByRef[ref]
		if len(t) == 0 { // nothing to pair with: try pass 2
			*oursLeft = append(*oursLeft, o...)
			continue
		}
		pairs := min(len(o), len(t))
		for i := 0; i < pairs; i++ {
			if o[i].Amount == t[i].Amount {
				*matches = append(*matches, Match{o[i], t[i], ExactReference})
			} else {
				*breaks = append(*breaks, Break{AmountMismatch, &o[i], &t[i]})
			}
		}
		for i := pairs; i < len(o); i++ {
			*breaks = append(*breaks, Break{DuplicateOurs, &o[i], nil})
		}
		for i := pairs; i < len(t); i++ {
			*breaks = append(*breaks, Break{DuplicateTheirs, nil, &t[i]})
		}
	}
	for _, ref := range sortedKeys(theirsByRef) {
		if _, shared := oursByRef[ref]; !shared {
			*theirsLeft = append(*theirsLeft, theirsByRef[ref]...)
		}
	}
	sort.SliceStable(*oursLeft, func(i, j int) bool { return oursLess((*oursLeft)[i], (*oursLeft)[j]) })
	sort.SliceStable(*theirsLeft, func(i, j int) bool { return theirsLess((*theirsLeft)[i], (*theirsLeft)[j]) })
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// matchByAmountAndDate is pass 2: greedy nearest-date match on equal amounts. Removes matched items from both leftover lists.
func (r *Reconciler) matchByAmountAndDate(oursLeft *[]LedgerPosting, theirsLeft *[]StatementLine, matches *[]Match) {
	var stillOurs []LedgerPosting
	for _, p := range *oursLeft {
		best, bestDistance := -1, int64(1<<62)
		for j, l := range *theirsLeft {
			if l.Amount != p.Amount {
				continue
			}
			distance := abs64(daysBetween(p.Date, l.Date))
			if distance > int64(r.toleranceDays) {
				continue
			}
			if distance < bestDistance { // strict <: first in sorted order wins ties
				best, bestDistance = j, distance
			}
		}
		if best >= 0 {
			*matches = append(*matches, Match{p, (*theirsLeft)[best], AmountDate})
			*theirsLeft = append((*theirsLeft)[:best], (*theirsLeft)[best+1:]...)
		} else {
			stillOurs = append(stillOurs, p)
		}
	}
	*oursLeft = stillOurs
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func ours(ref, date, amount string) LedgerPosting {
	return LedgerPosting{ref, must(ParseDate(date)), must(Parse(amount, USD)), "1010 Settlement Bank"}
}

func theirs(ref, date, amount string) StatementLine {
	return StatementLine{ref, must(ParseDate(date)), must(Parse(amount, USD)), "CARD SETTLEMENT"}
}

func main() {
	ledger := []LedgerPosting{
		ours("ch_1001", "2026-03-02", "19.99"),
		ours("ch_1002", "2026-03-02", "250.00"),
		ours("ch_1003", "2026-03-02", "7.50"),
		ours("ch_1004", "2026-03-03", "120.00"),
		ours("ch_1005", "2026-03-03", "49.95"),
		ours("ch_1006", "2026-03-03", "1500.00"),
		ours("ch_1007", "2026-03-04", "33.10"),
		ours("int_8841", "2026-03-04", "64.00"), // our reference; processor used its own id
		ours("ch_1009", "2026-03-04", "88.88"),  // we have it, they do not
		ours("ch_1010", "2026-03-05", "12.00"),  // amount disagrees with theirs
	}
	statement := []StatementLine{
		theirs("ch_1001", "2026-03-02", "19.99"),
		theirs("ch_1002", "2026-03-02", "250.00"),
		theirs("ch_1003", "2026-03-02", "7.50"),
		theirs("ch_1004", "2026-03-03", "120.00"),
		theirs("ch_1005", "2026-03-03", "49.95"),
		theirs("ch_1006", "2026-03-03", "1500.00"),
		theirs("ch_1007", "2026-03-04", "33.10"),
		theirs("ch_1007", "2026-03-04", "33.10"),  // they posted it twice
		theirs("txn_77f2", "2026-03-05", "64.00"), // their id for our int_8841, settled a day later
		theirs("ch_1010", "2026-03-05", "21.00"),  // we say 12.00
		theirs("ch_1011", "2026-03-05", "5.00"),   // they have it, we do not
	}

	reconciler := must(NewReconciler(USD, 1))
	report := must(reconciler.Reconcile(ledger, statement))

	fmt.Println("== Reconciling 10 ledger postings against 11 statement lines (tolerance 1 day) ==")
	fmt.Print(report.Render())
	fmt.Println()
	fmt.Println("== Reading the breaks ==")
	fmt.Println("MISSING_OURS ch_1011 5.00: they settled something we never recorded; look for a lost webhook.")
	fmt.Println("MISSING_THEIRS ch_1009 88.88: we recorded a settlement they never sent; still in transit or declined.")
	fmt.Println("AMOUNT_MISMATCH ch_1010 12.00 vs 21.00: a transposed digit on one side; the diff of -9.00 is the classic sign.")
	fmt.Println("DUPLICATE_THEIRS ch_1007: their file double-posted; dispute it, do not book it.")
	fmt.Printf("net difference %v = %v - %v\n", report.NetDifference(), report.OursUnmatched(), report.TheirsUnmatched())
}
