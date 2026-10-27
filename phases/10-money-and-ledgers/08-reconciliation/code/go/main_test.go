// Tests for phases/10-money-and-ledgers/08-reconciliation. Run: go test ./...
package main

import (
	"errors"
	"testing"
)

func ok[T any](t *testing.T) func(T, error) T {
	return func(v T, err error) T {
		t.Helper()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		return v
	}
}

func usd(t *testing.T, s string) Money {
	t.Helper()
	return ok[Money](t)(Parse(s, USD))
}

func reconciler(t *testing.T, tolerance int) *Reconciler {
	t.Helper()
	return ok[*Reconciler](t)(NewReconciler(USD, tolerance))
}

func reconcile(t *testing.T, r *Reconciler, ledger []LedgerPosting, statement []StatementLine) ReconciliationReport {
	t.Helper()
	return ok[ReconciliationReport](t)(r.Reconcile(ledger, statement))
}

func TestCleanBooks(t *testing.T) {
	clean := reconcile(t, reconciler(t, 1),
		[]LedgerPosting{ours("a", "2026-03-02", "10.00"), ours("b", "2026-03-02", "20.00")},
		[]StatementLine{theirs("b", "2026-03-02", "20.00"), theirs("a", "2026-03-02", "10.00")})
	if len(clean.Matches) != 2 {
		t.Error("clean: two matches")
	}
	if !clean.IsClean() {
		t.Error("clean: no breaks")
	}
	if !clean.TiesOut() {
		t.Error("clean: ties out")
	}
	if clean.MatchedTotal() != usd(t, "30.00") {
		t.Error("clean: matched total is the ledger total")
	}
	if clean.Matches[0].Ours.Reference != "a" {
		t.Error("clean: matches ordered by date then reference")
	}
	for _, m := range clean.Matches {
		if m.Rule != ExactReference {
			t.Error("clean: rule is EXACT_REFERENCE")
		}
	}
}

func TestMissingOnEachSide(t *testing.T) {
	missing := reconcile(t, reconciler(t, 1),
		[]LedgerPosting{ours("a", "2026-03-02", "10.00"), ours("only-ours", "2026-03-02", "88.88")},
		[]StatementLine{theirs("a", "2026-03-02", "10.00"), theirs("only-theirs", "2026-03-02", "5.00")})
	if missing.CountBreaks(MissingTheirs) != 1 {
		t.Error("missing: one MISSING_THEIRS")
	}
	if missing.CountBreaks(MissingOurs) != 1 {
		t.Error("missing: one MISSING_OURS")
	}
	if missing.Breaks[0].Kind != MissingOurs {
		t.Error("missing: MISSING_OURS reported before MISSING_THEIRS")
	}
	if missing.OursUnmatched() != usd(t, "88.88") {
		t.Error("missing: ours unmatched 88.88")
	}
	if missing.TheirsUnmatched() != usd(t, "5.00") {
		t.Error("missing: theirs unmatched 5.00")
	}
	if missing.NetDifference() != usd(t, "83.88") {
		t.Error("missing: net difference 83.88")
	}
	if !missing.TiesOut() {
		t.Error("missing: ties out")
	}
}

func TestAmountMismatch(t *testing.T) {
	mismatch := reconcile(t, reconciler(t, 1),
		[]LedgerPosting{ours("x", "2026-03-05", "12.00")}, []StatementLine{theirs("x", "2026-03-05", "21.00")})
	if mismatch.CountBreaks(AmountMismatch) != 1 {
		t.Error("mismatch: one AMOUNT_MISMATCH break")
	}
	if len(mismatch.Matches) != 0 {
		t.Error("mismatch: no matches")
	}
	if mismatch.Breaks[0].Difference(USD) != usd(t, "-9.00") {
		t.Error("mismatch: difference is -9.00")
	}
	if mismatch.OursUnmatched().MinorUnits != 1200 || mismatch.TheirsUnmatched().MinorUnits != 2100 {
		t.Error("mismatch: both sides counted in unmatched totals")
	}
	if !mismatch.TiesOut() {
		t.Error("mismatch: ties out")
	}
}

func TestAmountAndDateTolerance(t *testing.T) {
	r := reconciler(t, 1)
	fuzzy := reconcile(t, r, []LedgerPosting{ours("int_1", "2026-03-04", "64.00")}, []StatementLine{theirs("txn_9", "2026-03-05", "64.00")})
	if len(fuzzy.Matches) != 1 || !fuzzy.IsClean() {
		t.Error("fuzzy: matched within tolerance")
	}
	if fuzzy.Matches[0].Rule != AmountDate {
		t.Error("fuzzy: rule is AMOUNT_DATE")
	}
	tooFar := reconcile(t, r, []LedgerPosting{ours("int_1", "2026-03-04", "64.00")}, []StatementLine{theirs("txn_9", "2026-03-06", "64.00")})
	if len(tooFar.Matches) != 0 || len(tooFar.Breaks) != 2 {
		t.Error("fuzzy: two days is outside a one-day tolerance")
	}
	zeroTol := reconcile(t, reconciler(t, 0), []LedgerPosting{ours("int_1", "2026-03-04", "64.00")}, []StatementLine{theirs("txn_9", "2026-03-05", "64.00")})
	if len(zeroTol.Matches) != 0 {
		t.Error("fuzzy: zero tolerance needs the same date")
	}
	nearest := reconcile(t, r, []LedgerPosting{ours("int_1", "2026-03-04", "64.00")},
		[]StatementLine{theirs("far", "2026-03-05", "64.00"), theirs("near", "2026-03-04", "64.00")})
	if nearest.Matches[0].Theirs.Reference != "near" {
		t.Error("fuzzy: nearest date wins over an earlier-sorted candidate")
	}
	if nearest.Breaks[0].Kind != MissingOurs || nearest.Breaks[0].Theirs.Reference != "far" {
		t.Error("fuzzy: the loser is MISSING_OURS")
	}
	wrongAmount := reconcile(t, r, []LedgerPosting{ours("int_1", "2026-03-04", "64.00")}, []StatementLine{theirs("txn_9", "2026-03-04", "64.01")})
	if len(wrongAmount.Matches) != 0 {
		t.Error("fuzzy: a one-cent difference is never a match")
	}
}

func TestDuplicates(t *testing.T) {
	r := reconciler(t, 1)
	dupTheirs := reconcile(t, r, []LedgerPosting{ours("d", "2026-03-04", "33.10")},
		[]StatementLine{theirs("d", "2026-03-04", "33.10"), theirs("d", "2026-03-04", "33.10")})
	if len(dupTheirs.Matches) != 1 || dupTheirs.CountBreaks(DuplicateTheirs) != 1 {
		t.Error("dup theirs: one match plus one DUPLICATE_THEIRS")
	}
	if dupTheirs.NetDifference().MinorUnits != -3310 {
		t.Error("dup theirs: statement total exceeds ledger by one copy")
	}
	if !dupTheirs.TiesOut() {
		t.Error("dup theirs: ties out")
	}
	dupOurs := reconcile(t, r, []LedgerPosting{ours("d", "2026-03-04", "33.10"), ours("d", "2026-03-04", "33.10")},
		[]StatementLine{theirs("d", "2026-03-04", "33.10")})
	if dupOurs.CountBreaks(DuplicateOurs) != 1 {
		t.Error("dup ours: one DUPLICATE_OURS")
	}
	bothTwice := reconcile(t, r, []LedgerPosting{ours("d", "2026-03-04", "33.10"), ours("d", "2026-03-04", "33.10")},
		[]StatementLine{theirs("d", "2026-03-04", "33.10"), theirs("d", "2026-03-04", "33.10")})
	if len(bothTwice.Matches) != 2 || !bothTwice.IsClean() {
		t.Error("same multiplicity on both sides is two matches, not a duplicate")
	}
}

func demoLedger() []LedgerPosting {
	return []LedgerPosting{
		ours("ch_1001", "2026-03-02", "19.99"), ours("ch_1002", "2026-03-02", "250.00"),
		ours("ch_1003", "2026-03-02", "7.50"), ours("ch_1004", "2026-03-03", "120.00"),
		ours("ch_1005", "2026-03-03", "49.95"), ours("ch_1006", "2026-03-03", "1500.00"),
		ours("ch_1007", "2026-03-04", "33.10"), ours("int_8841", "2026-03-04", "64.00"),
		ours("ch_1009", "2026-03-04", "88.88"), ours("ch_1010", "2026-03-05", "12.00"),
	}
}

func demoStatement() []StatementLine {
	return []StatementLine{
		theirs("ch_1001", "2026-03-02", "19.99"), theirs("ch_1002", "2026-03-02", "250.00"),
		theirs("ch_1003", "2026-03-02", "7.50"), theirs("ch_1004", "2026-03-03", "120.00"),
		theirs("ch_1005", "2026-03-03", "49.95"), theirs("ch_1006", "2026-03-03", "1500.00"),
		theirs("ch_1007", "2026-03-04", "33.10"), theirs("ch_1007", "2026-03-04", "33.10"),
		theirs("txn_77f2", "2026-03-05", "64.00"), theirs("ch_1010", "2026-03-05", "21.00"),
		theirs("ch_1011", "2026-03-05", "5.00"),
	}
}

// shuffle is Collections.shuffle(list, rnd): swap from the end with java.util.Random draws.
func shuffle[T any](items []T, rnd *JavaRandom) {
	for i := len(items); i > 1; i-- {
		j := int(rnd.NextInt(int32(i)))
		items[i-1], items[j] = items[j], items[i-1]
	}
}

func TestDemoPartitionInvariantAndDeterminism(t *testing.T) {
	r := reconciler(t, 1)
	demo := reconcile(t, r, demoLedger(), demoStatement())
	if len(demo.Matches) != 8 {
		t.Errorf("demo: 8 matches, got %d", len(demo.Matches))
	}
	if len(demo.Breaks) != 4 {
		t.Errorf("demo: 4 breaks, got %d", len(demo.Breaks))
	}
	oursInBreaks, theirsInBreaks := 0, 0
	for _, b := range demo.Breaks {
		if b.Ours != nil {
			oursInBreaks++
		}
		if b.Theirs != nil {
			theirsInBreaks++
		}
	}
	if len(demo.Matches)+oursInBreaks != len(demoLedger()) || len(demo.Matches)+theirsInBreaks != len(demoStatement()) {
		t.Error("demo: every input is in exactly one match or break")
	}
	if !demo.TiesOut() {
		t.Error("demo: ties out")
	}
	if demo.NetDifference() != usd(t, "41.78") {
		t.Errorf("demo: net difference 88.88 - 9.00 - 33.10 - 5.00 = 41.78, got %v", demo.NetDifference())
	}

	shuffledLedger, shuffledStatement := demoLedger(), demoStatement()
	shuffle(shuffledLedger, NewJavaRandom(7))
	shuffle(shuffledStatement, NewJavaRandom(11))
	again := reconcile(t, r, shuffledLedger, shuffledStatement)
	if again.Render() != demo.Render() {
		t.Error("determinism: shuffled input renders byte-for-byte the same report")
	}
	for i := 1; i < len(demo.Breaks); i++ {
		if demo.Breaks[i-1].Kind > demo.Breaks[i].Kind {
			t.Error("determinism: breaks sorted by kind order")
		}
	}
}

func TestFailuresTheLessonWarnsAbout(t *testing.T) {
	r := reconciler(t, 1)
	eur := LedgerPosting{"e", ok[Date](t)(ParseDate("2026-03-04")), usdOrEur(t, "1.00", EUR), "1010"}
	if _, err := r.Reconcile([]LedgerPosting{eur}, nil); !errors.Is(err, ErrCurrencyMismatch) {
		t.Errorf("foreign currency is rejected, not summed: %v", err)
	}
	if _, err := NewReconciler(USD, -1); err == nil {
		t.Error("negative tolerance is rejected")
	}
	if _, err := NewMatch(ours("a", "2026-03-04", "1.00"), theirs("a", "2026-03-04", "2.00"), ExactReference); err == nil {
		t.Error("a Match with unequal amounts cannot be constructed")
	}
	o := ours("a", "2026-03-04", "1.00")
	if _, err := NewBreak(MissingOurs, &o, nil); err == nil {
		t.Error("a MISSING_OURS break needs their side")
	}
	empty := reconcile(t, r, nil, nil)
	if !empty.TiesOut() || !empty.LedgerTotal.IsZero() || !empty.IsClean() {
		t.Error("empty books tie out at zero")
	}
}

func usdOrEur(t *testing.T, s string, c Currency) Money {
	t.Helper()
	return ok[Money](t)(Parse(s, c))
}
