// Lesson: phases/10-money-and-ledgers/05-balances-and-snapshots/docs/en.md
// Balances, Running Totals, and Snapshots
// Run:  go run .          (add --timing to print wall-clock measurements)
// Test: go test ./...
// Sources: Fowler, "Event Sourcing" and "Snapshot" patterns; TigerBeetle account balances; Money from Lesson 01 (money.go).
// Go 1.22, standard library only. Same design as the Java and Python versions.
package main

import (
	"errors"
	"fmt"
	"math"
	"os"
	"sort"
	"time"
)

// Zero is the zero amount of a currency.
func Zero(currency Currency) Money { return Money{0, currency} }

// ---------------------------------------------------------------------------------------------
// The ledger (shape from Lesson 03, trimmed to what balances need) with three ways to read a balance.
// ---------------------------------------------------------------------------------------------

// ErrLedger is the sentinel every ledger rule violation wraps (Java's LedgerException).
var ErrLedger = errors.New("ledger")

func ledgerErr(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrLedger, fmt.Sprintf(format, args...))
}

type Side int

const (
	Debit Side = iota
	Credit
)

type AccountType int

const (
	Asset AccountType = iota
	Expense
	Liability
	Equity
	Revenue
)

func (t AccountType) NormalBalance() Side {
	if t == Asset || t == Expense {
		return Debit
	}
	return Credit
}

type Account struct {
	ID       string
	Name     string
	Type     AccountType
	Currency Currency
}

type Posting struct {
	AccountID string
	Side      Side
	Amount    Money
}

// NewPosting rejects non-positive amounts, as the Java compact constructor does.
func NewPosting(accountID string, side Side, amount Money) (Posting, error) {
	if !amount.IsPositive() {
		return Posting{}, ledgerErr("posting amount must be positive, got %v", amount)
	}
	return Posting{accountID, side, amount}, nil
}

func DebitPosting(accountID string, amount Money) (Posting, error) {
	return NewPosting(accountID, Debit, amount)
}
func CreditPosting(accountID string, amount Money) (Posting, error) {
	return NewPosting(accountID, Credit, amount)
}

// SignedMinor: debits count positive, credits negative: the "net debit" convention used by every fold below.
func (p Posting) SignedMinor() int64 {
	if p.Side == Debit {
		return p.Amount.MinorUnits
	}
	return -p.Amount.MinorUnits
}

// JournalEntry is a balanced entry with an effective timestamp (epoch seconds, injected by the caller,
// never read from a clock).
type JournalEntry struct {
	ID          string
	EffectiveAt int64
	Postings    []Posting
}

// NewJournalEntry validates balance per currency and copies the postings.
func NewJournalEntry(id string, effectiveAt int64, postings []Posting) (JournalEntry, error) {
	if len(postings) < 2 {
		return JournalEntry{}, ledgerErr("entry %s needs at least two postings", id)
	}
	copied := append([]Posting(nil), postings...)
	net := map[Currency]int64{}
	order := []Currency{}
	for _, p := range copied {
		if _, seen := net[p.Amount.Currency]; !seen {
			order = append(order, p.Amount.Currency)
		}
		sum, err := addChecked(net[p.Amount.Currency], p.SignedMinor())
		if err != nil {
			return JournalEntry{}, err
		}
		net[p.Amount.Currency] = sum
	}
	for _, c := range order {
		if net[c] != 0 {
			return JournalEntry{}, ledgerErr("entry %s unbalanced in %s", id, c.Code)
		}
	}
	return JournalEntry{id, effectiveAt, copied}, nil
}

// Equal compares two entries field by field (Java record equality).
func (e JournalEntry) Equal(other JournalEntry) bool {
	if e.ID != other.ID || e.EffectiveAt != other.EffectiveAt || len(e.Postings) != len(other.Postings) {
		return false
	}
	for i := range e.Postings {
		if e.Postings[i] != other.Postings[i] {
			return false
		}
	}
	return true
}

// Snapshot is a point-in-time copy of every account's net debit, valid after the entry at journal position Seq.
type Snapshot struct {
	Seq         int64
	EffectiveAt int64
	netDebits   map[string]int64
}

// NewSnapshot copies the map: the copy is the snapshot.
func NewSnapshot(seq, effectiveAt int64, netDebits map[string]int64) Snapshot {
	copied := make(map[string]int64, len(netDebits))
	for k, v := range netDebits {
		copied[k] = v
	}
	return Snapshot{seq, effectiveAt, copied}
}

func (s Snapshot) NetDebit(accountID string) int64 { return s.netDebits[accountID] }

// AccountIDs returns the snapshot's account ids in sorted order (Go maps have no stable iteration order).
func (s Snapshot) AccountIDs() []string {
	ids := make([]string, 0, len(s.netDebits))
	for id := range s.netDebits {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// Reading is a balance reading plus how many postings had to be examined to produce it: the cost, made visible.
type Reading struct {
	Balance         Money
	PostingsScanned int64
}

// LongList is a growable primitive int64 array (a Go slice; kept as a named type to mirror the Java design).
type LongList struct {
	data []int64
}

func (l *LongList) Add(value int64)     { l.data = append(l.data, value) }
func (l *LongList) Get(index int) int64 { return l.data[index] }
func (l *LongList) Size() int           { return len(l.data) }

// Ledger is an append-only journal with:
//  1. a running balance per account (O(1) current balance),
//  2. a running-balance column per account (balance after each of its postings; O(log n) as-of reads),
//  3. periodic snapshots of every account every snapshotInterval entries (bounded replay).
//
// Every read path must agree with FoldNetDebit, the slow, obviously-correct full scan.
type Ledger struct {
	snapshotInterval int
	accountOrder     []string
	accounts         map[string]Account
	journal          []JournalEntry       // journal[i] has seq i + 1
	running          map[string]int64     // current net debit per account
	columnSeq        map[string]*LongList // per account: seq of each posting
	columnAfter      map[string]*LongList // per account: net debit after it
	snapshots        []Snapshot           // ascending by seq; seq 0 is empty
}

func NewLedger(snapshotInterval int) (*Ledger, error) {
	if snapshotInterval < 1 {
		return nil, errors.New("snapshotInterval must be >= 1")
	}
	l := &Ledger{
		snapshotInterval: snapshotInterval,
		accounts:         map[string]Account{},
		running:          map[string]int64{},
		columnSeq:        map[string]*LongList{},
		columnAfter:      map[string]*LongList{},
	}
	l.snapshots = append(l.snapshots, NewSnapshot(0, math.MinInt64, nil))
	return l, nil
}

func (l *Ledger) OpenAccount(id, name string, typ AccountType, currency Currency) (Account, error) {
	if _, exists := l.accounts[id]; exists {
		return Account{}, ledgerErr("account exists: %s", id)
	}
	account := Account{id, name, typ, currency}
	l.accounts[id] = account
	l.accountOrder = append(l.accountOrder, id)
	l.running[id] = 0
	l.columnSeq[id] = &LongList{}
	l.columnAfter[id] = &LongList{}
	return account, nil
}

func (l *Ledger) Account(id string) (Account, error) {
	a, ok := l.accounts[id]
	if !ok {
		return Account{}, ledgerErr("unknown account: %s", id)
	}
	return a, nil
}

func (l *Ledger) Head() int64 { return int64(len(l.journal)) }

func (l *Ledger) Snapshots() []Snapshot { return append([]Snapshot(nil), l.snapshots...) }

// Post validates everything, then mutates everything. Returns the entry's journal position (seq).
func (l *Ledger) Post(entry JournalEntry) (int64, error) {
	for _, p := range entry.Postings {
		a, err := l.Account(p.AccountID)
		if err != nil {
			return 0, err
		}
		if a.Currency != p.Amount.Currency {
			return 0, ledgerErr("entry %s: currency does not match account %s", entry.ID, p.AccountID)
		}
	}
	if len(l.journal) > 0 && entry.EffectiveAt < l.journal[len(l.journal)-1].EffectiveAt {
		return 0, ledgerErr("entry %s is backdated; post a correction at the current time instead", entry.ID)
	}
	// Overflow is checked before any mutation so a rejected entry leaves no trace.
	after := make([]int64, len(entry.Postings))
	pending := map[string]int64{}
	for i, p := range entry.Postings {
		base, ok := pending[p.AccountID]
		if !ok {
			base = l.running[p.AccountID]
		}
		sum, err := addChecked(base, p.SignedMinor())
		if err != nil {
			return 0, err
		}
		after[i] = sum
		pending[p.AccountID] = sum
	}
	l.journal = append(l.journal, entry)
	seq := int64(len(l.journal))
	for i, p := range entry.Postings {
		l.running[p.AccountID] = after[i]
		l.columnSeq[p.AccountID].Add(seq)
		l.columnAfter[p.AccountID].Add(after[i])
	}
	if seq%int64(l.snapshotInterval) == 0 {
		l.TakeSnapshot()
	}
	return seq, nil
}

// TakeSnapshot copies every running total. O(accounts). Called automatically every snapshotInterval entries.
func (l *Ledger) TakeSnapshot() Snapshot {
	seq := l.Head()
	last := l.snapshots[len(l.snapshots)-1]
	if last.Seq == seq {
		return last
	}
	s := NewSnapshot(seq, l.journal[seq-1].EffectiveAt, l.running)
	l.snapshots = append(l.snapshots, s)
	return s
}

// ---- read path 0: the definition. Slow, obviously correct, used to check every other path. ----

// FoldNetDebit is the net debit of an account after the entry at seq, by summing every posting from the start. O(postings).
func (l *Ledger) FoldNetDebit(accountID string, seq int64) (Reading, error) {
	a, err := l.Account(accountID)
	if err != nil {
		return Reading{}, err
	}
	var acc, scanned int64
	for i := int64(0); i < seq; i++ {
		for _, p := range l.journal[i].Postings {
			scanned++
			if p.AccountID == accountID {
				if acc, err = addChecked(acc, p.SignedMinor()); err != nil {
					return Reading{}, err
				}
			}
		}
	}
	return Reading{OfMinor(acc, a.Currency), scanned}, nil
}

// ---- read path 1: the running total. O(1), but only for "now". ----

func (l *Ledger) CurrentNetDebit(accountID string) (Money, error) {
	a, err := l.Account(accountID)
	if err != nil {
		return Money{}, err
	}
	return OfMinor(l.running[accountID], a.Currency), nil
}

// Balance is the balance in the account's normal direction (positive = what you expect for that account type).
func (l *Ledger) Balance(accountID string) (Money, error) {
	a, err := l.Account(accountID)
	if err != nil {
		return Money{}, err
	}
	return toNormal(a, l.running[accountID])
}

// ---- read path 2: nearest snapshot at or before seq, then replay the delta. O(snapshotInterval). ----

func (l *Ledger) SnapshotNetDebit(accountID string, seq int64) (Reading, error) {
	a, err := l.Account(accountID)
	if err != nil {
		return Reading{}, err
	}
	if err := l.requireSeq(seq); err != nil {
		return Reading{}, err
	}
	base := l.NearestSnapshot(seq)
	acc, scanned := base.NetDebit(accountID), int64(0)
	for s := base.Seq; s < seq; s++ {
		for _, p := range l.journal[s].Postings {
			scanned++
			if p.AccountID == accountID {
				if acc, err = addChecked(acc, p.SignedMinor()); err != nil {
					return Reading{}, err
				}
			}
		}
	}
	return Reading{OfMinor(acc, a.Currency), scanned}, nil
}

// NearestSnapshot is a binary search: the last snapshot whose seq <= target. Snapshot 0 (all zero) always qualifies.
func (l *Ledger) NearestSnapshot(seq int64) Snapshot {
	lo, hi := 0, len(l.snapshots)-1
	for lo < hi {
		mid := int(uint(lo+hi+1) >> 1)
		if l.snapshots[mid].Seq <= seq {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return l.snapshots[lo]
}

// ---- read path 3: the per-account running-balance column. O(log postings of that account). ----

func (l *Ledger) ColumnNetDebit(accountID string, seq int64) (Reading, error) {
	a, err := l.Account(accountID)
	if err != nil {
		return Reading{}, err
	}
	if err := l.requireSeq(seq); err != nil {
		return Reading{}, err
	}
	seqs, after := l.columnSeq[accountID], l.columnAfter[accountID]
	lo, hi, found, probes := 0, seqs.Size()-1, -1, int64(0)
	for lo <= hi { // last posting of this account with seq <= target
		mid := int(uint(lo+hi) >> 1)
		probes++
		if seqs.Get(mid) <= seq {
			found = mid
			lo = mid + 1
		} else {
			hi = mid - 1
		}
	}
	var value int64
	if found >= 0 {
		value = after.Get(found)
	}
	return Reading{OfMinor(value, a.Currency), probes}, nil
}

// ---- time-based queries: translate a timestamp into a journal position, then read. ----

// SeqAsOf is the journal position of the last entry with EffectiveAt <= t; 0 if none. Binary search: timestamps never decrease.
func (l *Ledger) SeqAsOf(timestamp int64) int64 {
	lo, hi := 0, len(l.journal) // answer in [0, size]
	for lo < hi {
		mid := int(uint(lo+hi+1) >> 1)
		if l.journal[mid-1].EffectiveAt <= timestamp {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return int64(lo)
}

func (l *Ledger) BalanceAsOf(accountID string, timestamp int64) (Money, error) {
	a, err := l.Account(accountID)
	if err != nil {
		return Money{}, err
	}
	r, err := l.SnapshotNetDebit(accountID, l.SeqAsOf(timestamp))
	if err != nil {
		return Money{}, err
	}
	return toNormal(a, r.Balance.MinorUnits)
}

// Verify checks a snapshot against the definition: every account's value must equal the full fold at its seq.
func (l *Ledger) Verify(s Snapshot) bool {
	for _, id := range l.accountOrder {
		fold, err := l.FoldNetDebit(id, s.Seq)
		if err != nil || s.NetDebit(id) != fold.Balance.MinorUnits {
			return false
		}
	}
	for _, id := range s.AccountIDs() {
		if _, known := l.accounts[id]; !known {
			return false
		}
	}
	return true
}

func (l *Ledger) requireSeq(seq int64) error {
	if seq < 0 || seq > l.Head() {
		return ledgerErr("seq %d outside journal 0..%d", seq, l.Head())
	}
	return nil
}

func toNormal(a Account, netDebit int64) (Money, error) {
	signed := netDebit
	if a.Type.NormalBalance() == Credit {
		if netDebit == math.MinInt64 {
			return Money{}, ErrOverflow
		}
		signed = -netDebit
	}
	return OfMinor(signed, a.Currency), nil
}

// PaymentStream is a seeded generator of two-leg card payments into many merchant accounts: deterministic, no wall clock.
type PaymentStream struct {
	random    *JavaRandom
	merchants int32
	clock     int64
	counter   int64
}

func NewPaymentStream(seed int64, merchants int, startEpochSeconds int64) *PaymentStream {
	return &PaymentStream{random: NewJavaRandom(seed), merchants: int32(merchants), clock: startEpochSeconds}
}

func MerchantID(i int) string { return fmt.Sprintf("m%04d", i) }

func (s *PaymentStream) Next() JournalEntry {
	s.clock += int64(s.random.NextInt(3))           // 0..2 seconds between payments
	amount := int64(100 + s.random.NextInt(50_000)) // 1.00 .. 500.99 USD
	merchant := MerchantID(int(s.random.NextInt(s.merchants)))
	m := OfMinor(amount, USD)
	id := fmt.Sprintf("p%d", s.counter)
	s.counter++
	return JournalEntry{id, s.clock, []Posting{{"1200", Debit, m}, {merchant, Credit, m}}}
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func entry(id string, t int64, postings ...Posting) JournalEntry {
	return must(NewJournalEntry(id, t, postings))
}

func main() {
	timing := len(os.Args) > 1 && os.Args[1] == "--timing"

	fmt.Println("== A small ledger, snapshot every 3 entries ==")
	ledger := must(NewLedger(3))
	must(ledger.OpenAccount("1200", "Processor Receivable", Asset, USD))
	must(ledger.OpenAccount("2100", "Merchant Payable", Liability, USD))
	must(ledger.OpenAccount("4100", "Fee Revenue", Revenue, USD))
	var day int64 = 1_767_225_600 // 2026-01-01T00:00:00Z
	sales := []int64{10_000, 2_500, 4_000, 1_999, 7_500}
	for i, sale := range sales {
		gross := OfMinor(sale, USD)
		fee := must(gross.Times(29, 1000, HalfEven))
		seq := must(ledger.Post(entry(fmt.Sprintf("pay-%d", i+1), day+int64(i)*3600,
			must(DebitPosting("1200", gross)), must(CreditPosting("2100", must(gross.Minus(fee)))), must(CreditPosting("4100", fee)))))
		fmt.Printf("seq %d  t=+%dh  gross %-11v fee %-9v  payable now %v\n", seq, i, gross, fee, must(ledger.Balance("2100")))
	}
	seqs := []int64{}
	for _, s := range ledger.Snapshots() {
		seqs = append(seqs, s.Seq)
	}
	fmt.Println("snapshots at seq", seqs)
	for seq := int64(0); seq <= ledger.Head(); seq++ {
		fold := must(ledger.FoldNetDebit("2100", seq))
		snap := must(ledger.SnapshotNetDebit("2100", seq))
		col := must(ledger.ColumnNetDebit("2100", seq))
		fmt.Printf("2100 net debit after seq %d: fold %-12v (%2d postings)  snapshot %-12v (%d)  column %-12v (%d probes)\n",
			seq, fold.Balance, fold.PostingsScanned, snap.Balance, snap.PostingsScanned, col.Balance, col.PostingsScanned)
	}
	fmt.Printf("payable as of t=+2h30m: %v (seq %d)\n", must(ledger.BalanceAsOf("2100", day+9000)), ledger.SeqAsOf(day+9000))
	fmt.Printf("payable as of before the first payment: %v\n", must(ledger.BalanceAsOf("2100", day-1)))
	if _, err := ledger.Post(entry("late", day,
		must(DebitPosting("1200", OfMinor(100, USD))), must(CreditPosting("2100", OfMinor(100, USD))))); err != nil {
		fmt.Println("rejected:", err)
	}

	fmt.Println("\n== One million postings, 1,000 merchants, snapshot every 10,000 entries ==")
	big := must(NewLedger(10_000))
	must(big.OpenAccount("1200", "Processor Receivable", Asset, USD))
	merchants := 1_000
	for i := range merchants {
		must(big.OpenAccount(MerchantID(i), fmt.Sprintf("Merchant %d", i), Liability, USD))
	}
	stream := NewPaymentStream(7, merchants, day)
	t0 := time.Now()
	for range 500_000 { // 2 postings each
		must(big.Post(stream.Next()))
	}
	t1 := time.Now()
	var target int64 = 437_123
	merchant := MerchantID(42)
	t2 := time.Now()
	fold := must(big.FoldNetDebit(merchant, target))
	t3 := time.Now()
	snap := must(big.SnapshotNetDebit(merchant, target))
	t4 := time.Now()
	col := must(big.ColumnNetDebit(merchant, target))
	t5 := time.Now()
	fmt.Printf("entries=%d snapshots=%d\n", big.Head(), len(big.Snapshots()))
	fmt.Printf("merchant %s as of seq %d:\n", merchant, target)
	fmt.Printf("  full fold      %v  postings scanned %d\n", fold.Balance, fold.PostingsScanned)
	fmt.Printf("  snapshot+delta %v  postings scanned %d  (from snapshot at seq %d)\n", snap.Balance, snap.PostingsScanned, big.NearestSnapshot(target).Seq)
	fmt.Printf("  running column %v  binary-search probes %d\n", col.Balance, col.PostingsScanned)
	fmt.Printf("  all three agree: %v\n", fold.Balance == snap.Balance && snap.Balance == col.Balance)
	sum := Zero(USD)
	for i := range merchants {
		sum = must(sum.Plus(must(big.Balance(MerchantID(i)))))
	}
	receivable := must(big.Balance("1200"))
	fmt.Printf("receivable %v == sum of merchant payables %v: %v\n", receivable, sum, receivable == sum)
	if timing {
		fmt.Printf("timing: post 500k entries %d ms; fold %.3f ms; snapshot %.3f ms; column %.4f ms\n",
			t1.Sub(t0).Milliseconds(), float64(t3.Sub(t2).Nanoseconds())/1e6, float64(t4.Sub(t3).Nanoseconds())/1e6, float64(t5.Sub(t4).Nanoseconds())/1e6)
	}
}
