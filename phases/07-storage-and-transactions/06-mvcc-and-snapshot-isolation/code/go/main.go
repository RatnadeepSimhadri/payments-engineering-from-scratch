// Lesson: phases/07-storage-and-transactions/06-mvcc-and-snapshot-isolation/docs/en.md
// MVCC and Snapshot Isolation: a multi-version store with snapshot reads, first-committer-wins, and vacuum
// Run:  go run .
// Test: go test ./...
// Sources: Berenson et al., "A Critique of ANSI SQL Isolation Levels" (SIGMOD 1995); PostgreSQL docs ch. 13
// (Concurrency Control) and ch. 25 (Routine Vacuuming). Go 1.22+, standard library only. Same design as Java and Python.
package main

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// Currency is an ISO 4217 code plus the number of decimal places in its minor unit (Phase 10, Lesson 01).
type Currency struct {
	Code     string
	Exponent int
}

var USD = Currency{"USD", 2}

var (
	ErrCurrencyMismatch = errors.New("currency mismatch")
	ErrOverflow         = errors.New("int64 overflow")
)

// Money is an exact count of minor units in one currency. Never a float.
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

func (m Money) requireSameCurrency(other Money) error {
	if m.Currency != other.Currency {
		return fmt.Errorf("%w: %s vs %s", ErrCurrencyMismatch, m.Currency.Code, other.Currency.Code)
	}
	return nil
}

func (m Money) Plus(other Money) (Money, error) {
	if err := m.requireSameCurrency(other); err != nil {
		return Money{}, err
	}
	sum, err := addChecked(m.MinorUnits, other.MinorUnits)
	return Money{sum, m.Currency}, err
}

func (m Money) Minus(other Money) (Money, error) {
	if err := m.requireSameCurrency(other); err != nil {
		return Money{}, err
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

// TimestampOracle hands out strictly increasing logical timestamps. One per commit; snapshots read the current value.
type TimestampOracle struct {
	last int64
}

// Current is the most recent timestamp handed out. A transaction beginning now sees everything committed at or before it.
func (o *TimestampOracle) Current() int64 { return o.last }

// Next is a fresh timestamp, larger than every one handed out so far. Used as a commit timestamp.
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

// ErrWriteConflict is returned at commit when another transaction committed a newer version of a key this one wrote: first committer wins.
var ErrWriteConflict = errors.New("write conflict")

// ErrNotActive is returned when a committed or aborted transaction is used again (Java: IllegalStateException).
var ErrNotActive = errors.New("transaction is not active")

// Version is one immutable version of a key. Tombstone means the key was deleted at CommitTs (Java: value == null).
type Version[V any] struct {
	CommitTs  int64
	TxnID     int64
	Value     V
	Tombstone bool
}

func (v Version[V]) IsTombstone() bool { return v.Tombstone }

// pendingWrite is one buffered write; Delete marks a tombstone (Java: null value in the write map).
type pendingWrite[V any] struct {
	Value  V
	Delete bool
}

// Transaction is a snapshot, a private write set, and an outcome.
type Transaction[V any] struct {
	ID        int64
	StartTs   int64
	writeKeys []string // insertion order (LinkedHashMap)
	Writes    map[string]pendingWrite[V]
	Status    TxnStatus
	CommitTs  int64
}

func newTransaction[V any](id, startTs int64) *Transaction[V] {
	return &Transaction[V]{ID: id, StartTs: startTs, Writes: map[string]pendingWrite[V]{}, Status: Active, CommitTs: -1}
}

func (t *Transaction[V]) IsActive() bool { return t.Status == Active }

func (t *Transaction[V]) String() string { return fmt.Sprintf("T%d@%d", t.ID, t.StartTs) }

func (t *Transaction[V]) put(key string, w pendingWrite[V]) {
	if _, seen := t.Writes[key]; !seen {
		t.writeKeys = append(t.writeKeys, key)
	}
	t.Writes[key] = w
}

// MvccStore is a multi-version key-value store under snapshot isolation.
//
// Invariants:
//   - A transaction with StartTs S sees, for each key, the newest version with CommitTs <= S (plus its own writes).
//   - Writes are buffered privately and become versions only at commit, stamped with a fresh commit timestamp.
//   - First committer wins: commit fails if any written key has a version with CommitTs > S.
//   - Readers never block writers and writers never block readers; nothing in this type waits.
type MvccStore[V any] struct {
	oracle     *TimestampOracle
	versions   map[string][]Version[V] // per key, ascending CommitTs
	active     map[int64]*Transaction[V]
	nextTxnID  int64
	commits    int64
	aborts     int64
	activeKeys []int64 // insertion order of active (LinkedHashMap)
}

func NewMvccStore[V any](oracle *TimestampOracle) *MvccStore[V] {
	return &MvccStore[V]{oracle: oracle, versions: map[string][]Version[V]{}, active: map[int64]*Transaction[V]{}, nextTxnID: 1}
}

// Begin a transaction whose snapshot is "everything committed so far".
func (s *MvccStore[V]) Begin() *Transaction[V] {
	txn := newTransaction[V](s.nextTxnID, s.oracle.Current())
	s.nextTxnID++
	s.active[txn.ID] = txn
	s.activeKeys = append(s.activeKeys, txn.ID)
	return txn
}

// Read is a snapshot read: own uncommitted write first, otherwise the newest version committed at or before StartTs.
// The bool is false when the key is absent in this snapshot (Java's Optional.empty()).
func (s *MvccStore[V]) Read(txn *Transaction[V], key string) (V, bool, error) {
	var zero V
	if err := s.requireActive(txn); err != nil {
		return zero, false, err
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

// Write buffers a write. Nothing is visible to anyone else until commit.
func (s *MvccStore[V]) Write(txn *Transaction[V], key string, value V) error {
	if err := s.requireActive(txn); err != nil {
		return err
	}
	txn.put(key, pendingWrite[V]{Value: value})
	return nil
}

func (s *MvccStore[V]) Delete(txn *Transaction[V], key string) error {
	if err := s.requireActive(txn); err != nil {
		return err
	}
	txn.put(key, pendingWrite[V]{Delete: true})
	return nil
}

// Commit validates then publishes. For each written key, if a version newer than our snapshot exists, somebody committed
// first and we lose (first committer wins). Otherwise every write becomes a version at one fresh CommitTs.
func (s *MvccStore[V]) Commit(txn *Transaction[V]) (int64, error) {
	if err := s.requireActive(txn); err != nil {
		return 0, err
	}
	for _, key := range txn.writeKeys {
		if newest, ok := s.newestVersion(key); ok && newest.CommitTs > txn.StartTs {
			s.Abort(txn)
			return 0, fmt.Errorf("%w: %s lost %s to T%d (committed at %d > snapshot %d)", ErrWriteConflict, txn, key, newest.TxnID, newest.CommitTs, txn.StartTs)
		}
	}
	ts := txn.StartTs
	if len(txn.writeKeys) > 0 { // read-only transactions consume no timestamp
		ts = s.oracle.Next()
	}
	for _, key := range txn.writeKeys {
		w := txn.Writes[key]
		s.versions[key] = append(s.versions[key], Version[V]{CommitTs: ts, TxnID: txn.ID, Value: w.Value, Tombstone: w.Delete})
	}
	txn.Status = Committed
	txn.CommitTs = ts
	s.removeActive(txn.ID)
	s.commits++
	return ts, nil
}

func (s *MvccStore[V]) Abort(txn *Transaction[V]) {
	if !txn.IsActive() {
		return
	}
	txn.Status = Aborted
	txn.Writes = map[string]pendingWrite[V]{}
	txn.writeKeys = nil
	s.removeActive(txn.ID)
	s.aborts++
}

func (s *MvccStore[V]) removeActive(id int64) {
	delete(s.active, id)
	for i, k := range s.activeKeys {
		if k == id {
			s.activeKeys = append(s.activeKeys[:i], s.activeKeys[i+1:]...)
			break
		}
	}
}

// Vacuum removes versions no snapshot can see any more. The horizon is the oldest active snapshot (or "now" if none).
// A version is dead when a newer version of the same key has CommitTs <= horizon: every current and future
// reader sees that newer one or something later. A tombstone that is itself the newest version at or below the
// horizon takes the whole key with it. Returns the number of versions removed.
func (s *MvccStore[V]) Vacuum() int {
	horizon := s.OldestActiveSnapshot()
	removed := 0
	for _, key := range s.sortedKeys() {
		chain := s.versions[key]
		keepFrom := 0 // index of the newest version with CommitTs <= horizon; everything before it is dead
		for i, v := range chain {
			if v.CommitTs <= horizon {
				keepFrom = i
			}
		}
		if keepFrom > 0 {
			removed += keepFrom
			chain = chain[keepFrom:]
		}
		if oldestKept := chain[0]; oldestKept.IsTombstone() && oldestKept.CommitTs <= horizon {
			chain = chain[1:]
			removed++
		}
		if len(chain) == 0 {
			delete(s.versions, key)
		} else {
			s.versions[key] = chain
		}
	}
	return removed
}

func (s *MvccStore[V]) OldestActiveSnapshot() int64 {
	horizon := s.oracle.Current()
	for _, t := range s.active {
		if t.StartTs < horizon {
			horizon = t.StartTs
		}
	}
	return horizon
}

func (s *MvccStore[V]) VersionCount(key string) int { return len(s.versions[key]) }

func (s *MvccStore[V]) TotalVersions() int {
	total := 0
	for _, chain := range s.versions {
		total += len(chain)
	}
	return total
}

func (s *MvccStore[V]) ActiveCount() int { return len(s.active) }

func (s *MvccStore[V]) Commits() int64 { return s.commits }

func (s *MvccStore[V]) Aborts() int64 { return s.aborts }

// History returns a copy of the version chain for key, oldest first.
func (s *MvccStore[V]) History(key string) []Version[V] {
	chain := s.versions[key]
	out := make([]Version[V], len(chain))
	copy(out, chain)
	return out
}

func (s *MvccStore[V]) sortedKeys() []string {
	keys := make([]string, 0, len(s.versions))
	for k := range s.versions {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func (s *MvccStore[V]) visibleVersion(key string, snapshotTs int64) (Version[V], bool) {
	chain := s.versions[key]
	for i := len(chain) - 1; i >= 0; i-- {
		if chain[i].CommitTs <= snapshotTs {
			return chain[i], true
		}
	}
	return Version[V]{}, false
}

func (s *MvccStore[V]) newestVersion(key string) (Version[V], bool) {
	chain := s.versions[key]
	if len(chain) == 0 {
		return Version[V]{}, false
	}
	return chain[len(chain)-1], true
}

func (s *MvccStore[V]) requireActive(txn *Transaction[V]) error {
	if !txn.IsActive() {
		return fmt.Errorf("%w: %s is %s", ErrNotActive, txn, txn.Status)
	}
	return nil
}

// ---------------------------------------------------------------------------------------------
// Worked example
// ---------------------------------------------------------------------------------------------

// usd parses "19.99" into 1999 cents without ever touching a float64. Demo helper; the full parser is in Phase 10, Lesson 01.
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

func balance(store *MvccStore[Money], t *Transaction[Money], key string) Money {
	m, ok, err := store.Read(t, key)
	if err != nil {
		panic(err)
	}
	if !ok {
		return OfMinor(0, USD)
	}
	return m
}

// withdrawIfJointlyCovered tries to withdraw from `from` as long as the two joint accounts together stay at or above zero.
func withdrawIfJointlyCovered(store *MvccStore[Money], t *Transaction[Money], from, other string, amount Money) (bool, error) {
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
	store := NewMvccStore[Money](oracle)

	setup := store.Begin()
	check(store.Write(setup, "acct:A", usd("100.00")))
	check(store.Write(setup, "acct:B", usd("100.00")))
	must(store.Commit(setup))

	fmt.Println("== 1. Readers never block writers (and never see them) ==")
	reader := store.Begin()
	fmt.Printf("%s reads A = %s\n", reader, balance(store, reader, "acct:A"))
	writer := store.Begin()
	check(store.Write(writer, "acct:A", usd("40.00")))
	ts := must(store.Commit(writer))
	fmt.Printf("%s wrote A = 40.00 and committed at ts %d without waiting for %s\n", writer, ts, reader)
	fmt.Printf("%s reads A again = %s  (same snapshot, same answer)\n", reader, balance(store, reader, "acct:A"))
	later := store.Begin()
	fmt.Printf("%s (new snapshot) reads A = %s\n", later, balance(store, later, "acct:A"))
	must(store.Commit(reader))
	must(store.Commit(later))

	fmt.Println()
	fmt.Println("== 2. Lost update: first committer wins, second aborts ==")
	t1 := store.Begin()
	t2 := store.Begin()
	check(store.Write(t1, "acct:A", must(balance(store, t1, "acct:A").Minus(usd("10.00")))))
	check(store.Write(t2, "acct:A", must(balance(store, t2, "acct:A").Minus(usd("25.00")))))
	must(store.Commit(t1))
	peek := store.Begin()
	fmt.Printf("%s committed A = %s\n", t1, balance(store, peek, "acct:A"))
	must(store.Commit(peek))
	if _, err := store.Commit(t2); errors.Is(err, ErrWriteConflict) {
		fmt.Println("rejected: " + strings.TrimPrefix(err.Error(), ErrWriteConflict.Error()+": "))
	}

	fmt.Println()
	fmt.Println("== 3. Write skew: the anomaly snapshot isolation lets through ==")
	reset := store.Begin()
	check(store.Write(reset, "acct:A", usd("100.00")))
	check(store.Write(reset, "acct:B", usd("100.00")))
	must(store.Commit(reset))
	w1 := store.Begin()
	w2 := store.Begin()
	ok1 := must(withdrawIfJointlyCovered(store, w1, "acct:A", "acct:B", usd("150.00")))
	ok2 := must(withdrawIfJointlyCovered(store, w2, "acct:B", "acct:A", usd("150.00")))
	fmt.Printf("%s checks A+B = 200.00 >= 150.00 -> %t, writes A = -50.00\n", w1, ok1)
	fmt.Printf("%s checks A+B = 200.00 >= 150.00 -> %t, writes B = -50.00\n", w2, ok2)
	must(store.Commit(w1))
	must(store.Commit(w2))
	audit := store.Begin()
	a, b := balance(store, audit, "acct:A"), balance(store, audit, "acct:B")
	fmt.Printf("both committed (disjoint write sets). A = %s, B = %s, A+B = %s  <- constraint violated\n", a, b, must(a.Plus(b)))
	must(store.Commit(audit))

	fmt.Println()
	fmt.Println("== 4. Garbage collection: a long-running snapshot pins old versions ==")
	longRunner := store.Begin()
	for i := 0; i < 5; i++ {
		t := store.Begin()
		check(store.Write(t, "acct:A", must(balance(store, t, "acct:A").Plus(usd("1.00")))))
		must(store.Commit(t))
	}
	fmt.Printf("versions of A before vacuum: %d, total: %d\n", store.VersionCount("acct:A"), store.TotalVersions())
	removed := store.Vacuum()
	fmt.Printf("vacuum with %s still open removed %d versions (horizon = ts %d)\n", longRunner, removed, store.OldestActiveSnapshot())
	must(store.Commit(longRunner))
	removed = store.Vacuum()
	fmt.Printf("vacuum after it finished removed %d; versions of A now: %d, total: %d\n", removed, store.VersionCount("acct:A"), store.TotalVersions())
	fmt.Printf("commits = %d, aborts = %d\n", store.Commits(), store.Aborts())
}
