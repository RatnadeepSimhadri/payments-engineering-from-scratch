// Lesson: phases/07-storage-and-transactions/07-serializable-snapshot-isolation/docs/en.md
// Serializable Snapshot Isolation: the SI store of Lesson 06 plus SIREAD locks, rw-antidependency tracking,
// and the dangerous-structure abort rule.
// Run:  go run .
// Test: go test ./...
// Sources: Cahill, Röhm, Fekete, "Serializable Isolation for Snapshot Databases" (SIGMOD 2008); Ports & Grittner,
// "Serializable Snapshot Isolation in PostgreSQL" (VLDB 2012). Go 1.22+, standard library only. Same design as Java and Python.
package main

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// Currency is an ISO 4217 code plus minor-unit exponent (Phase 10, Lesson 01).
type Currency struct {
	Code     string
	Exponent int
}

var USD = Currency{"USD", 2}

var (
	ErrCurrencyMismatch = errors.New("currency mismatch")
	ErrOverflow         = errors.New("int64 overflow")
)

// Money is exact minor units in one currency.
type Money struct {
	MinorUnits int64
	Currency   Currency
}

func OfMinor(minorUnits int64, currency Currency) Money { return Money{minorUnits, currency} }

func addChecked(a, b int64) (int64, error) {
	if (b > 0 && a > math.MaxInt64-b) || (b < 0 && a < math.MinInt64-b) {
		return 0, ErrOverflow
	}
	return a + b, nil
}

func subChecked(a, b int64) (int64, error) {
	if (b < 0 && a > math.MaxInt64+b) || (b > 0 && a < math.MinInt64+b) {
		return 0, ErrOverflow
	}
	return a - b, nil
}

func (m Money) Plus(other Money) (Money, error) {
	if m.Currency != other.Currency {
		return Money{}, ErrCurrencyMismatch
	}
	sum, err := addChecked(m.MinorUnits, other.MinorUnits)
	return Money{sum, m.Currency}, err
}

func (m Money) Minus(other Money) (Money, error) {
	if m.Currency != other.Currency {
		return Money{}, ErrCurrencyMismatch
	}
	diff, err := subChecked(m.MinorUnits, other.MinorUnits)
	return Money{diff, m.Currency}, err
}

func (m Money) IsNegative() bool { return m.MinorUnits < 0 }

func (m Money) String() string {
	scale := int64(1)
	for i := 0; i < m.Currency.Exponent; i++ {
		scale *= 10
	}
	abs := m.MinorUnits
	if abs < 0 {
		abs = -abs
	}
	frac := ""
	if m.Currency.Exponent > 0 {
		frac = fmt.Sprintf(".%0*d", m.Currency.Exponent, abs%scale)
	}
	sign := ""
	if m.MinorUnits < 0 {
		sign = "-"
	}
	return fmt.Sprintf("%s%d%s %s", sign, abs/scale, frac, m.Currency.Code)
}

type TimestampOracle struct{ last int64 }

func (o *TimestampOracle) Current() int64 { return o.last }
func (o *TimestampOracle) Next() int64 {
	o.last++
	return o.last
}

type TxnStatus int

const (
	Active TxnStatus = iota
	Committed
	Aborted
)

func (s TxnStatus) String() string { return [...]string{"ACTIVE", "COMMITTED", "ABORTED"}[s] }

// ErrWriteConflict is Lesson 06's rule: a newer version of a written key exists, first committer wins.
var ErrWriteConflict = errors.New("write conflict")

// ErrSerializationFailure is this lesson's rule: the transaction is, or completes, a dangerous structure T_in -rw-> T -rw-> T_out.
var ErrSerializationFailure = errors.New("serialization failure")

// ErrNotActive is returned when a finished transaction is used again (Java: IllegalStateException).
var ErrNotActive = errors.New("transaction is not active")

// AbortError carries the abort reason verbatim; errors.Is matches its Kind (ErrWriteConflict or ErrSerializationFailure).
type AbortError struct {
	Kind   error
	Reason string
}

func (e *AbortError) Error() string        { return e.Reason }
func (e *AbortError) Is(target error) bool { return target == e.Kind }

// Version is one immutable version of a key; Tombstone replaces Java's null value.
type Version[V any] struct {
	CommitTs  int64
	TxnID     int64
	Value     V
	Tombstone bool
}

func (v Version[V]) IsTombstone() bool { return v.Tombstone }

type pendingWrite[V any] struct {
	Value  V
	Delete bool
}

// idSet is Java's LinkedHashSet<Long>: insertion-ordered, no duplicates.
type idSet struct {
	order []int64
	has   map[int64]bool
}

func newIDSet() *idSet { return &idSet{has: map[int64]bool{}} }

func (s *idSet) Add(id int64) {
	if !s.has[id] {
		s.has[id] = true
		s.order = append(s.order, id)
	}
}

func (s *idSet) Remove(id int64) {
	if !s.has[id] {
		return
	}
	delete(s.has, id)
	for i, x := range s.order {
		if x == id {
			s.order = append(s.order[:i], s.order[i+1:]...)
			break
		}
	}
}

func (s *idSet) Contains(id int64) bool { return s.has[id] }
func (s *idSet) IsEmpty() bool          { return len(s.order) == 0 }
func (s *idSet) Size() int              { return len(s.order) }
func (s *idSet) First() int64           { return s.order[0] }

// IDs returns the members in insertion order.
func (s *idSet) IDs() []int64 {
	out := make([]int64, len(s.order))
	copy(out, s.order)
	return out
}

func (s *idSet) String() string { return formatIDs(s.order) }

func formatIDs(ids []int64) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.FormatInt(id, 10)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// Transaction is an SI transaction plus the two conflict sets SSI needs.
type Transaction[V any] struct {
	ID        int64
	StartTs   int64
	writeKeys []string
	Writes    map[string]pendingWrite[V]
	// InConflicts: transactions T_in with T_in -rw-> this: they read a version this transaction overwrote.
	InConflicts *idSet
	// OutConflicts: transactions T_out with this -rw-> T_out: they wrote a newer version of something this transaction read.
	OutConflicts *idSet
	Status       TxnStatus
	CommitTs     int64
	// BeginSeq/EndSeq: position in the store's event order. Two transactions are concurrent when neither finished
	// before the other began. Commit timestamps cannot tell this, because a read-only commit consumes no timestamp.
	BeginSeq    int64
	EndSeq      int64
	AbortReason string
}

func newTransaction[V any](id, startTs, beginSeq int64) *Transaction[V] {
	return &Transaction[V]{ID: id, StartTs: startTs, Writes: map[string]pendingWrite[V]{}, InConflicts: newIDSet(), OutConflicts: newIDSet(),
		Status: Active, CommitTs: -1, BeginSeq: beginSeq, EndSeq: math.MaxInt64}
}

func (t *Transaction[V]) ConcurrentWith(other *Transaction[V]) bool {
	return t.BeginSeq < other.EndSeq && other.BeginSeq < t.EndSeq
}

func (t *Transaction[V]) IsActive() bool    { return t.Status == Active }
func (t *Transaction[V]) IsCommitted() bool { return t.Status == Committed }
func (t *Transaction[V]) IsPivot() bool     { return !t.InConflicts.IsEmpty() && !t.OutConflicts.IsEmpty() }
func (t *Transaction[V]) String() string    { return fmt.Sprintf("T%d", t.ID) }

func (t *Transaction[V]) put(key string, w pendingWrite[V]) {
	if _, seen := t.Writes[key]; !seen {
		t.writeKeys = append(t.writeKeys, key)
	}
	t.Writes[key] = w
}

// SsiStore is serializable snapshot isolation (Cahill et al. 2008) on top of the Lesson 06 store.
//
// Additions over SI:
//   - Every read takes a SIREAD "lock" on the key: a record, never a block, kept after commit while any concurrent
//     transaction is still active.
//   - Every read of a key that has a newer version (committed, or buffered in an active writer) records T -rw-> W.
//   - Every write of a key that a concurrent transaction read records R -rw-> T.
//   - A transaction with both an incoming and an outgoing rw-antidependency is a pivot and is aborted at commit;
//     a transaction that would attach a second edge to an already-committed pivot is aborted on the spot.
type SsiStore[V any] struct {
	oracle    *TimestampOracle
	versions  map[string][]Version[V]
	txns      map[int64]*Transaction[V] // active and recently committed
	txnOrder  []int64                   // insertion order of txns (LinkedHashMap)
	sireads   map[string]*idSet         // key -> transactions that read it
	nextTxnID int64
	seq       int64 // event order of begin/commit/abort
	commits   int64
	aborts    int64
}

func NewSsiStore[V any](oracle *TimestampOracle) *SsiStore[V] {
	return &SsiStore[V]{oracle: oracle, versions: map[string][]Version[V]{}, txns: map[int64]*Transaction[V]{}, sireads: map[string]*idSet{}, nextTxnID: 1}
}

func (s *SsiStore[V]) Begin() *Transaction[V] {
	s.seq++
	txn := newTransaction[V](s.nextTxnID, s.oracle.Current(), s.seq)
	s.nextTxnID++
	s.txns[txn.ID] = txn
	s.txnOrder = append(s.txnOrder, txn.ID)
	return txn
}

func (s *SsiStore[V]) removeTxn(id int64) {
	if _, ok := s.txns[id]; !ok {
		return
	}
	delete(s.txns, id)
	for i, x := range s.txnOrder {
		if x == id {
			s.txnOrder = append(s.txnOrder[:i], s.txnOrder[i+1:]...)
			break
		}
	}
}

// orderedTxns returns the live transactions in insertion order (a copy: recordRw may abort and remove one).
func (s *SsiStore[V]) orderedTxns() []*Transaction[V] {
	out := make([]*Transaction[V], 0, len(s.txnOrder))
	for _, id := range s.txnOrder {
		out = append(out, s.txns[id])
	}
	return out
}

// Read takes a SIREAD lock, records rw-antidependencies, and returns the snapshot value. The bool is Java's Optional.
func (s *SsiStore[V]) Read(txn *Transaction[V], key string) (V, bool, error) {
	var zero V
	if err := s.requireActive(txn); err != nil {
		return zero, false, err
	}
	if s.sireads[key] == nil {
		s.sireads[key] = newIDSet()
	}
	s.sireads[key].Add(txn.ID)
	// rw-antidependency: we read an older version than someone has written or is writing
	for _, v := range s.versions[key] {
		if v.CommitTs > txn.StartTs {
			s.recordRw(txn, s.txns[v.TxnID])
		}
	}
	for _, other := range s.orderedTxns() {
		if other != txn && other.IsActive() {
			if _, writes := other.Writes[key]; writes {
				s.recordRw(txn, other)
			}
		}
	}
	if !txn.IsActive() {
		return zero, false, &AbortError{ErrSerializationFailure, txn.AbortReason}
	}
	if w, ok := txn.Writes[key]; ok {
		if w.Delete {
			return zero, false, nil
		}
		return w.Value, true, nil
	}
	v, ok := s.visibleVersion(key, txn.StartTs)
	if !ok || v.IsTombstone() {
		return zero, false, nil
	}
	return v.Value, true, nil
}

func (s *SsiStore[V]) Write(txn *Transaction[V], key string, value V) error {
	if err := s.requireActive(txn); err != nil {
		return err
	}
	txn.put(key, pendingWrite[V]{Value: value})
	return s.checkReadersOf(txn, key)
}

func (s *SsiStore[V]) Delete(txn *Transaction[V], key string) error {
	if err := s.requireActive(txn); err != nil {
		return err
	}
	txn.put(key, pendingWrite[V]{Delete: true})
	return s.checkReadersOf(txn, key)
}

// checkReadersOf: readers of this key that are concurrent with the writer form R -rw-> txn.
func (s *SsiStore[V]) checkReadersOf(txn *Transaction[V], key string) error {
	if readers := s.sireads[key]; readers != nil {
		for _, readerID := range readers.IDs() { // copy: recordRw may mutate the set
			reader, ok := s.txns[readerID]
			if !ok || reader == txn {
				continue
			}
			if reader.ConcurrentWith(txn) {
				s.recordRw(reader, txn)
			}
		}
	}
	if !txn.IsActive() {
		return &AbortError{ErrSerializationFailure, txn.AbortReason}
	}
	return nil
}

// recordRw records reader -rw-> writer. If the other side is already committed and already has the opposite edge, it is a
// committed pivot and the only transaction we can still abort is the one being executed now.
func (s *SsiStore[V]) recordRw(reader, writer *Transaction[V]) {
	if reader == nil || writer == nil || reader == writer {
		return
	}
	if !reader.IsActive() && !reader.IsCommitted() {
		return
	}
	if !writer.IsActive() && !writer.IsCommitted() {
		return
	}
	reader.OutConflicts.Add(writer.ID)
	writer.InConflicts.Add(reader.ID)
	if reader.IsActive() && writer.IsCommitted() && !writer.OutConflicts.IsEmpty() {
		s.Abort(reader, fmt.Sprintf("%s -rw-> %s -rw-> T%d: %s is a committed pivot", reader, writer, writer.OutConflicts.First(), writer))
	} else if writer.IsActive() && reader.IsCommitted() && !reader.InConflicts.IsEmpty() {
		s.Abort(writer, fmt.Sprintf("T%d -rw-> %s -rw-> %s: %s is a committed pivot", reader.InConflicts.First(), reader, writer, reader))
	}
}

func (s *SsiStore[V]) Commit(txn *Transaction[V]) (int64, error) {
	if err := s.requireActive(txn); err != nil {
		return 0, err
	}
	for _, key := range txn.writeKeys {
		if newest, ok := s.newestVersion(key); ok && newest.CommitTs > txn.StartTs {
			s.Abort(txn, "first committer wins on "+key)
			return 0, &AbortError{ErrWriteConflict, txn.AbortReason}
		}
	}
	if txn.IsPivot() {
		s.Abort(txn, fmt.Sprintf("T%d -rw-> %s -rw-> T%d: %s is the pivot", txn.InConflicts.First(), txn, txn.OutConflicts.First(), txn))
		return 0, &AbortError{ErrSerializationFailure, txn.AbortReason}
	}
	ts := txn.StartTs
	if len(txn.writeKeys) > 0 {
		ts = s.oracle.Next()
	}
	for _, key := range txn.writeKeys {
		w := txn.Writes[key]
		s.versions[key] = append(s.versions[key], Version[V]{CommitTs: ts, TxnID: txn.ID, Value: w.Value, Tombstone: w.Delete})
	}
	txn.Status = Committed
	txn.CommitTs = ts
	s.seq++
	txn.EndSeq = s.seq
	s.commits++
	return ts, nil
}

// Abort drops the write set, drops our SIREADs, and removes our edges from everyone else's conflict sets.
func (s *SsiStore[V]) Abort(txn *Transaction[V], reason string) {
	if !txn.IsActive() {
		return
	}
	txn.Status = Aborted
	txn.AbortReason = reason
	s.seq++
	txn.EndSeq = s.seq
	txn.Writes = map[string]pendingWrite[V]{}
	txn.writeKeys = nil
	for _, readers := range s.sireads {
		readers.Remove(txn.ID)
	}
	for _, other := range s.txns {
		other.InConflicts.Remove(txn.ID)
		other.OutConflicts.Remove(txn.ID)
	}
	s.removeTxn(txn.ID)
	s.aborts++
}

// ReleaseFinished forgets committed transactions that are no longer concurrent with any active one: they finished before
// every active transaction began. Their SIREAD locks can never be the "reader" side of a new edge, so they are released.
// Returns how many were released.
func (s *SsiStore[V]) ReleaseFinished() int {
	oldestActiveBegin := s.seq + 1
	for _, t := range s.txns {
		if t.IsActive() && t.BeginSeq < oldestActiveBegin {
			oldestActiveBegin = t.BeginSeq
		}
	}
	done := []int64{}
	for _, id := range s.txnOrder {
		if t := s.txns[id]; t.IsCommitted() && t.EndSeq < oldestActiveBegin {
			done = append(done, id)
		}
	}
	for _, id := range done {
		s.removeTxn(id)
		for _, readers := range s.sireads {
			readers.Remove(id)
		}
	}
	for key, readers := range s.sireads {
		if readers.IsEmpty() {
			delete(s.sireads, key)
		}
	}
	return len(done)
}

func (s *SsiStore[V]) OldestActiveSnapshot() int64 {
	horizon := s.oracle.Current()
	for _, t := range s.txns {
		if t.IsActive() && t.StartTs < horizon {
			horizon = t.StartTs
		}
	}
	return horizon
}

func (s *SsiStore[V]) RetainedReadLocks() int {
	total := 0
	for _, readers := range s.sireads {
		total += readers.Size()
	}
	return total
}

// ReadersOf returns the transaction ids holding a SIREAD on key, sorted ascending (Java returns a HashSet).
func (s *SsiStore[V]) ReadersOf(key string) []int64 {
	out := []int64{}
	if readers := s.sireads[key]; readers != nil {
		out = readers.IDs()
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func (s *SsiStore[V]) Commits() int64 { return s.commits }
func (s *SsiStore[V]) Aborts() int64  { return s.aborts }

func (s *SsiStore[V]) visibleVersion(key string, snapshotTs int64) (Version[V], bool) {
	chain := s.versions[key]
	for i := len(chain) - 1; i >= 0; i-- {
		if chain[i].CommitTs <= snapshotTs {
			return chain[i], true
		}
	}
	return Version[V]{}, false
}

func (s *SsiStore[V]) newestVersion(key string) (Version[V], bool) {
	chain := s.versions[key]
	if len(chain) == 0 {
		return Version[V]{}, false
	}
	return chain[len(chain)-1], true
}

func (s *SsiStore[V]) requireActive(txn *Transaction[V]) error {
	if !txn.IsActive() {
		reason := ""
		if txn.AbortReason != "" {
			reason = " (" + txn.AbortReason + ")"
		}
		return fmt.Errorf("%w: %s is %s%s", ErrNotActive, txn, txn.Status, reason)
	}
	return nil
}

// ---------------------------------------------------------------------------------------------
// Worked example
// ---------------------------------------------------------------------------------------------

func usd(major string) Money {
	negative := strings.HasPrefix(major, "-")
	parts := strings.Split(strings.TrimPrefix(major, "-"), ".")
	whole, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		panic(err)
	}
	cents := whole * 100
	if len(parts) > 1 {
		frac, err := strconv.ParseInt((parts[1] + "00")[:2], 10, 64)
		if err != nil {
			panic(err)
		}
		cents += frac
	}
	if negative {
		cents = -cents
	}
	return OfMinor(cents, USD)
}

// balance reads the key's balance, treating a missing key as zero. A read that aborts the transaction yields zero too:
// the caller checks the transaction's status, as the Java demo does through the exception.
func balance(store *SsiStore[Money], t *Transaction[Money], key string) Money {
	m, ok, err := store.Read(t, key)
	if err != nil || !ok {
		return OfMinor(0, USD)
	}
	return m
}

func withdrawIfJointlyCovered(store *SsiStore[Money], t *Transaction[Money], from, other string, amount Money) (bool, error) {
	total, err := balance(store, t, from).Plus(balance(store, t, other))
	if err != nil {
		return false, err
	}
	remaining, err := total.Minus(amount)
	if err != nil {
		return false, err
	}
	if remaining.IsNegative() {
		return false, nil
	}
	after, err := balance(store, t, from).Minus(amount)
	if err != nil {
		return false, err
	}
	return true, store.Write(t, from, after)
}

func tryCommit(store *SsiStore[Money], t *Transaction[Money]) string {
	ts, err := store.Commit(t)
	if errors.Is(err, ErrSerializationFailure) || errors.Is(err, ErrWriteConflict) {
		return fmt.Sprintf("%s ABORTED: %s", t, err.Error())
	}
	if err != nil {
		panic(err)
	}
	return fmt.Sprintf("%s committed at ts %d", t, ts)
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func check(err error) {
	if err != nil {
		panic(err)
	}
}

func main() {
	oracle := &TimestampOracle{}
	store := NewSsiStore[Money](oracle)
	setup := store.Begin()
	check(store.Write(setup, "acct:A", usd("100.00")))
	check(store.Write(setup, "acct:B", usd("100.00")))
	must(store.Commit(setup))

	fmt.Println("== 1. Write skew from Lesson 06, now under SSI ==")
	t1 := store.Begin()
	t2 := store.Begin()
	must(withdrawIfJointlyCovered(store, t1, "acct:A", "acct:B", usd("150.00")))
	fmt.Printf("%s read A,B (SIREAD on both), wrote A = -50.00; readers of A = %s\n", t1, formatIDs(store.ReadersOf("acct:A")))
	must(withdrawIfJointlyCovered(store, t2, "acct:B", "acct:A", usd("150.00")))
	fmt.Printf("%s read A,B, wrote B = -50.00\n", t2)
	fmt.Printf("edges: %s -rw-> %s (%s read A, %s writes A); %s -rw-> %s (%s read B, %s writes B)\n", t2, t1, t2, t1, t1, t2, t1, t2)
	fmt.Printf("  %s in=%s out=%s pivot=%t\n", t1, t1.InConflicts, t1.OutConflicts, t1.IsPivot())
	fmt.Printf("  %s in=%s out=%s pivot=%t\n", t2, t2.InConflicts, t2.OutConflicts, t2.IsPivot())
	fmt.Println(tryCommit(store, t1))
	fmt.Printf("after the abort, %s in=%s out=%s\n", t2, t2.InConflicts, t2.OutConflicts)
	fmt.Println(tryCommit(store, t2))
	audit := store.Begin()
	a, b := balance(store, audit, "acct:A"), balance(store, audit, "acct:B")
	fmt.Printf("A = %s, B = %s, A+B = %s  <- constraint holds\n", a, b, must(a.Plus(b)))
	must(store.Commit(audit))

	fmt.Println()
	fmt.Println("== 2. One rw-antidependency alone is harmless ==")
	reader := store.Begin()
	writer := store.Begin()
	fmt.Printf("%s reads A = %s\n", reader, balance(store, reader, "acct:A"))
	check(store.Write(writer, "acct:A", usd("75.00")))
	fmt.Printf("%s writes A = 75.00; edge %s -rw-> %s\n", writer, reader, writer)
	fmt.Println(tryCommit(store, writer))
	fmt.Printf("%s  (serializable order: %s before %s)\n", tryCommit(store, reader), reader, writer)

	fmt.Println()
	fmt.Println("== 3. SIREAD locks outlive their transaction ==")
	early := store.Begin()
	late := store.Begin()
	earlyBalance := balance(store, early, "acct:B")
	fmt.Printf("%s reads B = %s and commits: %s\n", early, earlyBalance, tryCommit(store, early))
	fmt.Printf("readers of B still recorded: %s (retained locks = %d)\n", formatIDs(store.ReadersOf("acct:B")), store.RetainedReadLocks())
	check(store.Write(late, "acct:B", usd("1.00")))
	fmt.Printf("%s (started before %s committed) writes B: edge %s -rw-> %s; %s in=%s\n", late, early, early, late, late, late.InConflicts)
	fmt.Printf("%s  (one edge, no pivot)\n", tryCommit(store, late))
	fmt.Printf("released %d finished transactions; retained locks now %d\n", store.ReleaseFinished(), store.RetainedReadLocks())

	fmt.Println()
	fmt.Println("== 4. The pivot has already committed: abort the newcomer on the spot ==")
	p := store.Begin()                           // will be the pivot
	q := store.Begin()                           // concurrent with p
	balance(store, p, "acct:A")                  // p reads A
	check(store.Write(p, "acct:B", usd("5.00"))) // p writes B
	fmt.Printf("%s reads A, writes B, commits: %s\n", p, tryCommit(store, p))
	fmt.Printf("%s reads B (older version than %s wrote): edge %s -rw-> %s\n", q, p, q, p)
	balance(store, q, "acct:B")
	if err := store.Write(q, "acct:A", usd("6.00")); errors.Is(err, ErrSerializationFailure) {
		fmt.Printf("%s tries to write A, which %s read: ABORTED immediately: %s\n", q, p, err.Error())
	} else {
		check(err)
		fmt.Printf("%s wrote A\n", q)
	}
	fmt.Printf("commits = %d, aborts = %d\n", store.Commits(), store.Aborts())
}
