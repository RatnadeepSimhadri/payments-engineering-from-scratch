// Tests for phases/10-money-and-ledgers/06-immutability-and-reversals. Run: go test ./...
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

func ledger(t *testing.T) *Ledger {
	t.Helper()
	l := NewLedger()
	ok[Account](t)(l.OpenAccount("1200", "Processor Receivable", Asset, USD))
	ok[Account](t)(l.OpenAccount("2100", "Merchant Payable", Liability, USD))
	ok[Account](t)(l.OpenAccount("4100", "Fee Revenue", Revenue, USD))
	return l
}

func dr(id string, minor int64) Posting { return Posting{id, Debit, OfMinor(minor, USD)} }
func cr(id string, minor int64) Posting { return Posting{id, Credit, OfMinor(minor, USD)} }

func sale(t *testing.T, id string, gross, fee int64) JournalEntry {
	t.Helper()
	return ok[JournalEntry](t)(OriginalEntry(id, "sale", []Posting{dr("1200", gross), cr("2100", gross-fee), cr("4100", fee)}))
}

func bal(t *testing.T, l *Ledger, id string) int64 {
	t.Helper()
	return ok[Money](t)(l.Balance(id)).MinorUnits
}

func sumBalances(t *testing.T, l *Ledger) int64 {
	t.Helper()
	return bal(t, l, "1200") - bal(t, l, "2100") - bal(t, l, "4100")
}

func hasLeg(e JournalEntry, account string, minor int64) bool {
	for _, p := range e.Postings() {
		if p.AccountID == account && p.Amount.MinorUnits == minor {
			return true
		}
	}
	return false
}

func sameIDs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestImmutabilityOfValues(t *testing.T) {
	s := sale(t, "pay-1", 12_000, 348)
	copied := s.Postings()
	copied[0] = dr("9999", 1)
	if s.Postings()[0].AccountID != "1200" {
		t.Error("postings list is unmodifiable: Postings() must return a copy")
	}
	l := ledger(t)
	ok[bool](t)(l.Post(s))
	records := l.Records()
	records[0] = JournalRecord{}
	if l.Size() != 1 || l.Records()[0].Entry.ID != "pay-1" {
		t.Error("journal view is unmodifiable: Records() must return a copy")
	}
	if posted := ok[bool](t)(l.Post(sale(t, "pay-1", 12_000, 348))); posted || l.Size() != 1 {
		t.Error("idempotent post of identical entry")
	}
	if _, err := l.Post(sale(t, "pay-1", 12_000, 349)); !errors.Is(err, ErrIdempotencyConflict) {
		t.Errorf("same id different content conflicts: %v", err)
	}
	handBuilt := ok[JournalEntry](t)(NewJournalEntry("x", "d", []Posting{dr("2100", 1), cr("1200", 1)}, Reversal, "pay-1", "r"))
	if _, err := l.Post(handBuilt); !errors.Is(err, ErrLedger) {
		t.Errorf("post refuses a hand-built reversal: %v", err)
	}
	if _, err := NewJournalEntry("x", "d", []Posting{dr("2100", 1), cr("1200", 1)}, Reversal, "pay-1", " "); !errors.Is(err, ErrLedger) {
		t.Errorf("reversal without a reason cannot be built: %v", err)
	}
	if _, err := NewJournalEntry("x", "d", []Posting{dr("2100", 1), cr("1200", 1)}, Original, "pay-1", ""); !errors.Is(err, ErrLedger) {
		t.Errorf("original with a link cannot be built: %v", err)
	}
	if _, err := OriginalEntry("u", "d", []Posting{dr("2100", 2), cr("1200", 1)}); !errors.Is(err, ErrLedger) {
		t.Errorf("unbalanced entry cannot be built: %v", err)
	}
}

func TestPartialReversal(t *testing.T) {
	l := ledger(t)
	s := sale(t, "pay-1", 12_000, 348)
	ok[bool](t)(l.Post(s))
	r1 := ok[JournalEntry](t)(l.ReversePartially("pay-1", "ref-1", OfMinor(5_000, USD), "return"))
	if r1.DebitTotal() != 5_000 || len(r1.Postings()) != 3 {
		t.Error("partial reversal is balanced and sized")
	}
	if !hasLeg(r1, "2100", 4_855) {
		t.Error("partial: payable leg proportional (48.55)")
	}
	if !hasLeg(r1, "4100", 145) {
		t.Error("partial: fee leg proportional (1.45)")
	}
	for _, p := range r1.Postings() {
		want := Debit
		if p.AccountID == "1200" {
			want = Credit
		}
		if p.Side != want {
			t.Errorf("partial: sides flipped: %v", p)
		}
	}
	if ok[Money](t)(l.Remaining("pay-1")).MinorUnits != 7_000 {
		t.Error("remaining after partial")
	}
	if again := ok[JournalEntry](t)(l.ReversePartially("pay-1", "ref-1", OfMinor(5_000, USD), "retry")); !again.Equal(r1) || l.Size() != 2 {
		t.Error("idempotent partial reversal")
	}
	if _, err := l.ReversePartially("pay-1", "ref-1", OfMinor(4_000, USD), "retry"); !errors.Is(err, ErrIdempotencyConflict) {
		t.Errorf("same refund id different amount conflicts: %v", err)
	}
	if _, err := l.ReversePartially("pay-1", "ref-2", OfMinor(7_001, USD), "too much"); !errors.Is(err, ErrExcessiveReversal) {
		t.Errorf("refund larger than remaining: %v", err)
	}
	if l.Size() != 2 || ok[Money](t)(l.Remaining("pay-1")).MinorUnits != 7_000 {
		t.Error("rejected refund left no trace")
	}
	if _, err := l.ReversePartially("pay-1", "ref-3", OfMinor(1, EUR), "eur"); !errors.Is(err, ErrCurrencyMismatch) {
		t.Errorf("refund currency mismatch: %v", err)
	}
	if _, err := l.Reverse("ref-1", "x", "undo the refund"); !errors.Is(err, ErrLedger) {
		t.Errorf("cannot reverse a reversal: %v", err)
	}

	// full reversal of the remainder returns every balance to zero
	r2 := ok[JournalEntry](t)(l.Reverse("pay-1", "cb-1", "chargeback"))
	if r2.DebitTotal() != 7_000 {
		t.Error("full reversal covers exactly the remainder")
	}
	if !ok[bool](t)(l.IsFullyReversed("pay-1")) {
		t.Error("fully reversed")
	}
	if bal(t, l, "1200") != 0 || bal(t, l, "2100") != 0 || bal(t, l, "4100") != 0 {
		t.Error("every balance back to zero")
	}
	if again := ok[JournalEntry](t)(l.Reverse("pay-1", "cb-1", "retry")); !again.Equal(r2) || l.Size() != 3 {
		t.Error("idempotent full reversal")
	}
	if _, err := l.Reverse("pay-1", "cb-2", "dup"); !errors.Is(err, ErrAlreadyReversed) {
		t.Errorf("second reversal with new id rejected: %v", err)
	}
	if _, err := l.ReversePartially("pay-1", "ref-9", OfMinor(1, USD), "late"); !errors.Is(err, ErrAlreadyReversed) {
		t.Errorf("partial after full rejected: %v", err)
	}
	ok[bool](t)(l.Post(sale(t, "pay-2", 1_000, 29)))
	if _, err := l.Reverse("pay-2", "cb-1", "wrong id reuse"); !errors.Is(err, ErrIdempotencyConflict) {
		t.Errorf("child id reused for different original: %v", err)
	}
	if e := ok[JournalEntry](t)(l.Entry("pay-1")); !e.Equal(s) || !l.Records()[0].Entry.Equal(s) {
		t.Error("original still in journal, untouched")
	}
}

func corrected(t *testing.T) (*Ledger, JournalEntry) {
	t.Helper()
	c := ledger(t)
	ok[bool](t)(c.Post(sale(t, "pay-200", 8_000, 312)))
	fixed := ok[JournalEntry](t)(c.Correct("pay-200", "rev-200", "pay-200c",
		[]Posting{dr("1200", 8_000), cr("2100", 7_768), cr("4100", 232)}, "fee config"))
	return c, fixed
}

func TestCorrection(t *testing.T) {
	c, fixed := corrected(t)
	if c.Size() != 3 || c.Records()[1].Entry.Kind != Reversal || c.Records()[2].Entry.Kind != Correction {
		t.Error("correction appends reversal then replacement")
	}
	if fixed.RelatesTo != "pay-200" || ok[JournalEntry](t)(c.Entry("rev-200")).RelatesTo != "pay-200" {
		t.Error("correction links to original")
	}
	if bal(t, c, "4100") != 232 || bal(t, c, "2100") != 7_768 {
		t.Error("balances reflect only the corrected entry")
	}
	if !ok[bool](t)(c.IsFullyReversed("pay-200")) {
		t.Error("original fully reversed by correction")
	}
	if again := ok[JournalEntry](t)(c.Correct("pay-200", "rev-200", "pay-200c", fixed.Postings(), "fee config")); !again.Equal(fixed) || c.Size() != 3 {
		t.Error("correction is idempotent")
	}
	if _, err := c.Correct("pay-200c", "rev-x", "pay-x", []Posting{dr("1200", 8_000), cr("2100", 7_000)}, "bad"); !errors.Is(err, ErrLedger) {
		t.Errorf("unbalanced replacement rejected before anything is posted: %v", err)
	}
	if c.Size() != 3 {
		t.Error("failed correction left no trace")
	}
	fixed2 := ok[JournalEntry](t)(c.Correct("pay-200c", "rev-200c", "pay-200cc",
		[]Posting{dr("1200", 8_000), cr("2100", 7_800), cr("4100", 200)}, "fee waiver"))
	if fixed2.RelatesTo != "pay-200c" || bal(t, c, "4100") != 200 {
		t.Error("a correction can itself be corrected")
	}
	if got := ids(ok[[]JournalRecord](t)(c.History("pay-200"))); !sameIDs(got, []string{"pay-200", "rev-200", "pay-200c", "rev-200c", "pay-200cc"}) {
		t.Errorf("history follows the chain transitively: %v", got)
	}
}

func TestHashChain(t *testing.T) {
	c, _ := corrected(t)
	ok[JournalEntry](t)(c.Correct("pay-200c", "rev-200c", "pay-200cc",
		[]Posting{dr("1200", 8_000), cr("2100", 7_800), cr("4100", 200)}, "fee waiver"))
	if VerifyChain(c.Records()) != -1 {
		t.Error("chain intact")
	}
	edited := c.Records()
	v := edited[1]
	e := ok[JournalEntry](t)(NewJournalEntry(v.Entry.ID, "edited", v.Entry.Postings(), v.Entry.Kind, v.Entry.RelatesTo, v.Entry.Reason))
	edited[1] = JournalRecord{v.Seq, e, v.PrevHash, v.Hash}
	if got := VerifyChain(edited); got != 2 {
		t.Errorf("edited record detected at its seq: %d", got)
	}
	dropped := c.Records()
	dropped = append(dropped[:2], dropped[3:]...)
	if got := VerifyChain(dropped); got != 3 {
		t.Errorf("deleted record detected: %d", got)
	}
	if len(c.AuditView()) != c.Size() {
		t.Error("audit view has one line per record")
	}
	// Cross-language check: the canonical string is byte-identical to Java's, so the genesis hash of this entry is too.
	first := c.Records()[0]
	if want := "pay-200|ORIGINAL|null|null|sale|1200:DEBIT:8000:USD|2100:CREDIT:7688:USD|4100:CREDIT:312:USD"; first.Entry.Canonical() != want {
		t.Errorf("canonical = %q", first.Entry.Canonical())
	}
	if first.PrevHash != Genesis || len(first.Hash) != 64 {
		t.Error("first record links to genesis with a 64-hex SHA-256")
	}
}

func TestPropertyRandomRefundsConserve(t *testing.T) {
	r := NewJavaRandom(9)
	for trial := range 200 {
		p := ledger(t)
		gross := int64(1 + r.NextInt(100_000))
		fee := int64(r.NextInt(int32(min(gross, 5_000))))
		if fee == gross {
			fee = 0
		}
		var e JournalEntry
		if fee == 0 {
			e = ok[JournalEntry](t)(OriginalEntry("t", "s", []Posting{dr("1200", gross), cr("2100", gross)}))
		} else {
			e = sale(t, "t", gross, fee)
		}
		ok[bool](t)(p.Post(e))
		k := 0
		for ok[Money](t)(p.Remaining("t")).MinorUnits > 0 && k < 6 {
			rem := ok[Money](t)(p.Remaining("t")).MinorUnits
			amount := int64(1 + r.NextInt(int32(min(rem, int64(1<<31-1)))))
			ok[JournalEntry](t)(p.ReversePartially("t", "r"+string(rune('0'+k)), OfMinor(amount, USD), "refund"))
			k++
			if sumBalances(t, p) != 0 {
				t.Fatalf("trial %d: balances do not conserve after refund %d", trial, k)
			}
		}
		if !ok[bool](t)(p.IsFullyReversed("t")) {
			ok[JournalEntry](t)(p.Reverse("t", "final", "close out"))
		}
		if bal(t, p, "1200") != 0 || bal(t, p, "2100") != 0 || bal(t, p, "4100") != 0 {
			t.Fatalf("trial %d: balances did not end at zero", trial)
		}
	}
}
