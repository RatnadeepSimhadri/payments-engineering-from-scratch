// Tests for phases/10-money-and-ledgers/05-balances-and-snapshots. Run: go test ./...
package main

import (
	"errors"
	"testing"
)

const T0 int64 = 1_767_225_600

func ok[T any](t *testing.T) func(T, error) T {
	return func(v T, err error) T {
		t.Helper()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		return v
	}
}

func threeAccounts(t *testing.T, interval int) *Ledger {
	t.Helper()
	l := ok[*Ledger](t)(NewLedger(interval))
	ok[Account](t)(l.OpenAccount("1200", "Processor Receivable", Asset, USD))
	ok[Account](t)(l.OpenAccount("2100", "Merchant Payable", Liability, USD))
	ok[Account](t)(l.OpenAccount("4100", "Fee Revenue", Revenue, USD))
	return l
}

func sale(t *testing.T, id string, at, grossMinor, feeMinor int64) JournalEntry {
	t.Helper()
	gross, fee := OfMinor(grossMinor, USD), OfMinor(feeMinor, USD)
	net := ok[Money](t)(gross.Minus(fee))
	return ok[JournalEntry](t)(NewJournalEntry(id, at, []Posting{
		ok[Posting](t)(DebitPosting("1200", gross)), ok[Posting](t)(CreditPosting("2100", net)), ok[Posting](t)(CreditPosting("4100", fee))}))
}

func post(t *testing.T, l *Ledger, e JournalEntry) int64 {
	t.Helper()
	return ok[int64](t)(l.Post(e))
}

func balance(t *testing.T, l *Ledger, id string) int64 {
	t.Helper()
	return ok[Money](t)(l.Balance(id)).MinorUnits
}

func snapshotSeqs(l *Ledger) []int64 {
	out := []int64{}
	for _, s := range l.Snapshots() {
		out = append(out, s.Seq)
	}
	return out
}

func sameSeqs(a, b []int64) bool {
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

func threePosted(t *testing.T) *Ledger {
	t.Helper()
	l := threeAccounts(t, 2)
	post(t, l, sale(t, "a", T0, 10_000, 290))
	post(t, l, sale(t, "b", T0+10, 2_500, 72))
	post(t, l, sale(t, "c", T0+20, 4_000, 116))
	return l
}

func TestRunningBalanceAndNormalDirection(t *testing.T) {
	l := threePosted(t)
	if got := balance(t, l, "1200"); got != 16_500 {
		t.Errorf("receivable is debit-normal positive: %d", got)
	}
	if got := balance(t, l, "2100"); got != 16_500-478 {
		t.Errorf("payable is credit-normal positive: %d", got)
	}
	if got := balance(t, l, "4100"); got != 478 {
		t.Errorf("revenue is credit-normal positive: %d", got)
	}
	sum := ok[Money](t)(l.CurrentNetDebit("1200"))
	for _, id := range []string{"2100", "4100"} {
		sum = ok[Money](t)(sum.Plus(ok[Money](t)(l.CurrentNetDebit(id))))
	}
	if !sum.IsZero() {
		t.Errorf("net debits sum to zero: %v", sum)
	}
}

func TestSnapshotSchedule(t *testing.T) {
	l := threePosted(t)
	if !sameSeqs(snapshotSeqs(l), []int64{0, 2}) {
		t.Errorf("snapshots at 0 and 2: %v", snapshotSeqs(l))
	}
	if l.NearestSnapshot(1).Seq != 0 {
		t.Error("nearest snapshot to seq 1 is 0")
	}
	if l.NearestSnapshot(3).Seq != 2 {
		t.Error("nearest snapshot to seq 3 is 2")
	}
	if s := l.TakeSnapshot(); s.Seq != 3 || len(l.Snapshots()) != 3 {
		t.Error("takeSnapshot at head is added")
	}
	if s := l.TakeSnapshot(); s.Seq != 3 || len(l.Snapshots()) != 3 {
		t.Error("takeSnapshot twice is a no-op")
	}
	if ok[Reading](t)(l.SnapshotNetDebit("2100", 2)).PostingsScanned != 0 {
		t.Error("snapshot replay at exact snapshot seq scans nothing")
	}
}

func TestThreeReadPathsAgree(t *testing.T) {
	l := threePosted(t)
	for seq := int64(0); seq <= l.Head(); seq++ {
		for _, id := range []string{"1200", "2100", "4100"} {
			fold := ok[Reading](t)(l.FoldNetDebit(id, seq)).Balance
			snap := ok[Reading](t)(l.SnapshotNetDebit(id, seq)).Balance
			col := ok[Reading](t)(l.ColumnNetDebit(id, seq)).Balance
			if fold != snap || fold != col {
				t.Errorf("fold == snapshot+delta == column at seq %d account %s: %v %v %v", seq, id, fold, snap, col)
			}
		}
	}
	if !ok[Reading](t)(l.ColumnNetDebit("2100", 0)).Balance.IsZero() {
		t.Error("before first entry balance is zero")
	}
}

func TestTimeBasedQueries(t *testing.T) {
	l := threePosted(t)
	cases := []struct {
		name string
		at   int64
		want int64
	}{
		{"seqAsOf before start is 0", T0 - 1, 0},
		{"seqAsOf exact timestamp includes the entry", T0 + 10, 2},
		{"seqAsOf between entries", T0 + 15, 2},
		{"seqAsOf far future is head", T0 + 1_000_000, 3},
	}
	for _, c := range cases {
		if got := l.SeqAsOf(c.at); got != c.want {
			t.Errorf("%s: got %d, want %d", c.name, got, c.want)
		}
	}
	if got := ok[Money](t)(l.BalanceAsOf("2100", T0+15)).MinorUnits; got != 9_710+2_428 {
		t.Errorf("balanceAsOf mid-stream: %d", got)
	}
}

func TestBackdatingAndValidation(t *testing.T) {
	l := threePosted(t)
	post(t, l, sale(t, "d", T0+20, 1_999, 58))
	if l.SeqAsOf(T0+20) != 4 {
		t.Error("same-timestamp entries both counted")
	}
	if _, err := l.Post(sale(t, "late", T0, 100, 3)); !errors.Is(err, ErrLedger) {
		t.Errorf("backdated entry rejected: %v", err)
	}
	if l.Head() != 4 || balance(t, l, "4100") != 478+58 {
		t.Error("rejected entry left no trace")
	}
	eur := ok[JournalEntry](t)(NewJournalEntry("eur", T0+30, []Posting{
		ok[Posting](t)(DebitPosting("1200", OfMinor(100, EUR))), ok[Posting](t)(CreditPosting("2100", OfMinor(100, EUR)))}))
	if _, err := l.Post(eur); !errors.Is(err, ErrLedger) {
		t.Errorf("currency mismatch rejected: %v", err)
	}
	if _, err := NewJournalEntry("u", T0, []Posting{
		ok[Posting](t)(DebitPosting("1200", OfMinor(100, USD))), ok[Posting](t)(CreditPosting("2100", OfMinor(99, USD)))}); !errors.Is(err, ErrLedger) {
		t.Errorf("unbalanced entry cannot be built: %v", err)
	}
	if _, err := NewPosting("1200", Debit, OfMinor(0, USD)); !errors.Is(err, ErrLedger) {
		t.Errorf("zero posting cannot be built: %v", err)
	}
	if _, err := l.SnapshotNetDebit("2100", 99); !errors.Is(err, ErrLedger) {
		t.Errorf("seq beyond head rejected: %v", err)
	}
	if _, err := l.ColumnNetDebit("2100", -1); !errors.Is(err, ErrLedger) {
		t.Errorf("negative seq rejected: %v", err)
	}
}

func TestVerification(t *testing.T) {
	l := threePosted(t)
	post(t, l, sale(t, "d", T0+20, 1_999, 58))
	for _, s := range l.Snapshots() {
		if !l.Verify(s) {
			t.Errorf("snapshot %d verifies", s.Seq)
		}
	}
	forged := NewSnapshot(2, T0+10, map[string]int64{"1200": 12_500, "2100": -12_138, "4100": -361})
	if l.Verify(forged) {
		t.Error("forged snapshot fails verification")
	}
	unknown := NewSnapshot(0, 0, map[string]int64{"9999": 0})
	if l.Verify(unknown) {
		t.Error("snapshot with unknown account fails verification")
	}
}

func TestRandomizedProperty(t *testing.T) {
	stream := NewPaymentStream(11, 25, T0)
	big := ok[*Ledger](t)(NewLedger(97))
	ok[Account](t)(big.OpenAccount("1200", "Processor Receivable", Asset, USD))
	for i := range 25 {
		ok[Account](t)(big.OpenAccount(MerchantID(i), "m", Liability, USD))
	}
	for range 5_000 {
		post(t, big, stream.Next())
	}
	r := NewJavaRandom(3)
	var maxScan int64
	for range 300 {
		seq := int64(r.NextInt(int32(big.Head()) + 1))
		id := "1200"
		if r.NextInt(5) != 0 {
			id = MerchantID(int(r.NextInt(25)))
		}
		fold := ok[Reading](t)(big.FoldNetDebit(id, seq))
		snap := ok[Reading](t)(big.SnapshotNetDebit(id, seq))
		col := ok[Reading](t)(big.ColumnNetDebit(id, seq))
		if fold.Balance != snap.Balance || fold.Balance != col.Balance {
			t.Errorf("property: three paths disagree at (%s, %d)", id, seq)
		}
		maxScan = max(maxScan, snap.PostingsScanned)
	}
	if maxScan > 96*2 {
		t.Errorf("snapshot replay bounded by interval * legs: %d", maxScan)
	}
	for _, s := range big.Snapshots() {
		if !big.Verify(s) {
			t.Errorf("automatic snapshot %d verifies", s.Seq)
		}
	}
	if len(big.Snapshots()) != 5_000/97+1 {
		t.Errorf("snapshot count = head / interval + 1: %d", len(big.Snapshots()))
	}
	var merchantsTotal int64
	for i := range 25 {
		merchantsTotal += balance(t, big, MerchantID(i))
	}
	if balance(t, big, "1200") != merchantsTotal {
		t.Error("conservation: receivable equals sum of payables")
	}
}

func TestDeterminism(t *testing.T) {
	s1, s2 := NewPaymentStream(5, 10, T0), NewPaymentStream(5, 10, T0)
	for range 100 {
		if a, b := s1.Next(), s2.Next(); !a.Equal(b) {
			t.Fatalf("seeded stream is deterministic: %v vs %v", a, b)
		}
	}
	if _, err := NewLedger(0); err == nil {
		t.Error("interval must be positive")
	}
	// java.util.Random(42).nextInt(100) three times gives 30, 63, 48 on the JDK; the port must agree
	r := NewJavaRandom(42)
	got := []int32{r.NextInt(100), r.NextInt(100), r.NextInt(100)}
	if got[0] != 30 || got[1] != 63 || got[2] != 48 {
		t.Errorf("JavaRandom(42).nextInt(100) x3 = %v, want [30 63 48]", got)
	}
	r = NewJavaRandom(42)
	if l, d, i, b := r.NextLong(), r.NextDouble(), r.NextInt32(), r.NextBoolean(); l != -5025562857975149833 || d != 0.6832234717598454 || i != 1325939940 || !b {
		t.Errorf("JavaRandom(42) nextLong/nextDouble/nextInt/nextBoolean = %d %v %d %v", l, d, i, b)
	}
}
