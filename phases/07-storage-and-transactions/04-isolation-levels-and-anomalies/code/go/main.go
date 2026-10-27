// Lesson: phases/07-storage-and-transactions/04-isolation-levels-and-anomalies/docs/en.md
// Isolation Levels and Their Anomalies
// Run:  go run .
// Test: go test ./...
// Sources: ANSI/ISO SQL-92 (ISO/IEC 9075:1992) section 4.28; Berenson, Bernstein, Gray, Melton, O'Neil, O'Neil,
// "A Critique of ANSI SQL Isolation Levels" (SIGMOD 1995). Go 1.22+, standard library only. Same design as Java and Python.
package main

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Isolation is one of the five levels this store can run at. The first three are the lock-based ANSI levels; Snapshot is Berenson's SI.
type Isolation int

const (
	ReadUncommitted Isolation = iota
	ReadCommitted
	RepeatableRead
	Snapshot
	Serializable
)

// Levels lists every isolation level in Java's Isolation.values() order.
var Levels = []Isolation{ReadUncommitted, ReadCommitted, RepeatableRead, Snapshot, Serializable}

func (l Isolation) String() string {
	return [...]string{"READ_UNCOMMITTED", "READ_COMMITTED", "REPEATABLE_READ", "SNAPSHOT", "SERIALIZABLE"}[l]
}

// ErrConflict is the sentinel every *ConflictError matches via errors.Is. The transaction that receives it is aborted.
var ErrConflict = errors.New("conflict")

// ErrNotActive is returned when a committed or aborted transaction is used again (Java: IllegalStateException).
var ErrNotActive = errors.New("transaction is not active")

// ErrUnknownAnomaly is returned by AnomalyLab.Run for a name it does not know.
var ErrUnknownAnomaly = errors.New("unknown anomaly")

// ConflictError carries the human-readable reason; errors.Is(err, ErrConflict) is true for every ConflictError.
type ConflictError struct{ Msg string }

func (e *ConflictError) Error() string        { return e.Msg }
func (e *ConflictError) Is(target error) bool { return target == ErrConflict }

func conflict(format string, args ...any) error {
	return &ConflictError{Msg: fmt.Sprintf(format, args...)}
}

// LockEntry is per-key lock state: one exclusive holder, or any number of shared holders. Exclusive 0 means none.
type LockEntry struct {
	Shared    map[int]bool
	Exclusive int
}

func newLockEntry() *LockEntry { return &LockEntry{Shared: map[int]bool{}} }

func (e *LockEntry) HeldByOthers(me int) bool {
	if e.Exclusive != 0 && e.Exclusive != me {
		return true
	}
	for s := range e.Shared {
		if s != me {
			return true
		}
	}
	return false
}

// Store is a single-version key-value store (values are balances in minor units) with transactions.
// No MVCC: there is exactly one committed value per key. Isolation comes from locks (no-wait: a conflict aborts the
// requester instead of blocking) or, at Snapshot, from a private copy taken at begin plus first-committer-wins.
type Store struct {
	Committed      map[string]int64
	VersionOf      map[string]int64 // key -> commit number that last wrote it
	Locks          map[string]*LockEntry
	PredicateLocks map[string]map[int]bool // prefix -> txns that scanned it (Serializable)
	CommitClock    int64
	nextTxID       int
	Trace          []string
}

func NewStore() *Store {
	return &Store{
		Committed:      map[string]int64{},
		VersionOf:      map[string]int64{},
		Locks:          map[string]*LockEntry{},
		PredicateLocks: map[string]map[int]bool{},
		nextTxID:       1,
	}
}

func (s *Store) Load(key string, value int64) {
	s.Committed[key] = value
	s.VersionOf[key] = 0
}

func (s *Store) Begin(level Isolation) *Txn {
	t := newTxn(s, s.nextTxID, level)
	s.nextTxID++
	s.Trace = append(s.Trace, fmt.Sprintf("T%d begin %s", t.ID, level))
	return t
}

// ---- lock table (no-wait) ----

func (s *Store) lockEntry(key string) *LockEntry {
	e, ok := s.Locks[key]
	if !ok {
		e = newLockEntry()
		s.Locks[key] = e
	}
	return e
}

func (s *Store) LockShared(tx int, key string) error {
	e := s.lockEntry(key)
	if e.Exclusive != 0 && e.Exclusive != tx {
		return conflict("T%d S-lock %s blocked by X of T%d", tx, key, e.Exclusive)
	}
	e.Shared[tx] = true
	return nil
}

func (s *Store) LockExclusive(tx int, key string) error {
	e := s.lockEntry(key)
	if e.HeldByOthers(tx) {
		return conflict("T%d X-lock %s blocked by %s", tx, key, describeHolders(e, tx))
	}
	delete(e.Shared, tx)
	e.Exclusive = tx
	return nil
}

func (s *Store) LockPredicate(tx int, prefix string) error {
	// conflict if another transaction holds X on any existing key under this prefix
	for _, key := range sortedKeys(s.Locks) {
		e := s.Locks[key]
		if strings.HasPrefix(key, prefix) && e.Exclusive != 0 && e.Exclusive != tx {
			return conflict("T%d predicate %s* blocked by X of T%d on %s", tx, prefix, e.Exclusive, key)
		}
	}
	if s.PredicateLocks[prefix] == nil {
		s.PredicateLocks[prefix] = map[int]bool{}
	}
	s.PredicateLocks[prefix][tx] = true
	return nil
}

func (s *Store) CheckPredicateConflict(tx int, key string) error {
	for _, prefix := range sortedKeys(s.PredicateLocks) {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		for _, holder := range sortedInts(s.PredicateLocks[prefix]) {
			if holder != tx {
				return conflict("T%d write %s blocked by predicate lock %s* of T%d", tx, key, prefix, holder)
			}
		}
	}
	return nil
}

func (s *Store) ReleaseAll(tx int) {
	for key, e := range s.Locks {
		delete(e.Shared, tx)
		if e.Exclusive == tx {
			e.Exclusive = 0
		}
		if e.Exclusive == 0 && len(e.Shared) == 0 {
			delete(s.Locks, key)
		}
	}
	for prefix, holders := range s.PredicateLocks {
		delete(holders, tx)
		if len(holders) == 0 {
			delete(s.PredicateLocks, prefix)
		}
	}
}

func describeHolders(e *LockEntry, me int) string {
	if e.Exclusive != 0 && e.Exclusive != me {
		return fmt.Sprintf("X of T%d", e.Exclusive)
	}
	others := []string{}
	for _, s := range sortedInts(e.Shared) {
		if s != me {
			others = append(others, fmt.Sprintf("%d", s))
		}
	}
	return "S of T[" + strings.Join(others, ", ") + "]"
}

// CommittedWithPrefix is TreeMap.tailMap + startsWith: every committed key under prefix.
func (s *Store) CommittedWithPrefix(prefix string) map[string]int64 {
	out := map[string]int64{}
	for k, v := range s.Committed {
		if strings.HasPrefix(k, prefix) {
			out[k] = v
		}
	}
	return out
}

// Status of a transaction.
type Status int

const (
	Active Status = iota
	Committed
	Aborted
)

func (s Status) String() string { return [...]string{"ACTIVE", "COMMITTED", "ABORTED"}[s] }

// orderedWrites is Java's LinkedHashMap<String, Long>: a map plus insertion order.
type orderedWrites struct {
	keys []string
	vals map[string]int64
}

func newOrderedWrites() *orderedWrites { return &orderedWrites{vals: map[string]int64{}} }

func (w *orderedWrites) put(key string, value int64) {
	if _, ok := w.vals[key]; !ok {
		w.keys = append(w.keys, key)
	}
	w.vals[key] = value
}

func (w *orderedWrites) get(key string) (int64, bool) {
	v, ok := w.vals[key]
	return v, ok
}

// undoEntry remembers the previous committed value for an in-place (ReadUncommitted) write; Present is false if the key was new.
type undoEntry struct {
	Value   int64
	Present bool
}

// Txn is one transaction against a Store.
type Txn struct {
	Store           *Store
	ID              int
	Level           Isolation
	Status          Status
	Writes          *orderedWrites       // buffered writes (all levels except ReadUncommitted)
	undoKeys        []string             // ReadUncommitted: insertion order of undo
	Undo            map[string]undoEntry // ReadUncommitted: previous committed value for in-place writes
	Snapshot        map[string]int64     // Snapshot: private copy taken at begin
	SnapshotVersion int64
	ReadSet         map[string]bool
}

func newTxn(store *Store, id int, level Isolation) *Txn {
	t := &Txn{Store: store, ID: id, Level: level, Status: Active, Writes: newOrderedWrites(), Undo: map[string]undoEntry{}, ReadSet: map[string]bool{}}
	if level == Snapshot {
		t.Snapshot = make(map[string]int64, len(store.Committed))
		for k, v := range store.Committed {
			t.Snapshot[k] = v
		}
	}
	t.SnapshotVersion = store.CommitClock
	return t
}

// guarded wraps an operation so a conflict aborts this transaction and releases its locks before returning the error.
func (t *Txn) guarded(op func() error) error {
	if t.Status != Active {
		return fmt.Errorf("%w: T%d is %s", ErrNotActive, t.ID, t.Status)
	}
	err := op()
	if errors.Is(err, ErrConflict) {
		t.Store.Trace = append(t.Store.Trace, fmt.Sprintf("  %s -> T%d ABORTED", err.Error(), t.ID))
		t.Abort()
	}
	return err
}

func lookup(m map[string]int64, key string) (int64, bool) {
	v, ok := m[key]
	return v, ok
}

// Read returns the value, whether the key exists (Java's null), and an error on conflict or inactive transaction.
func (t *Txn) Read(key string) (value int64, present bool, err error) {
	err = t.guarded(func() error {
		switch t.Level {
		case ReadUncommitted:
			value, present = lookup(t.Store.Committed, key) // sees in-place writes of others
		case ReadCommitted:
			if value, present = t.Writes.get(key); !present {
				value, present = lookup(t.Store.Committed, key)
			}
		case RepeatableRead, Serializable:
			if _, buffered := t.Writes.get(key); !buffered {
				if e := t.Store.LockShared(t.ID, key); e != nil {
					return e
				}
			}
			if value, present = t.Writes.get(key); !present {
				value, present = lookup(t.Store.Committed, key)
			}
		case Snapshot:
			if value, present = t.Writes.get(key); !present {
				value, present = lookup(t.Snapshot, key)
			}
		}
		t.ReadSet[key] = true
		t.Store.Trace = append(t.Store.Trace, fmt.Sprintf("  T%d read %s = %s", t.ID, key, formatValue(value, present)))
		return nil
	})
	return value, present, err
}

// Scan returns all keys with the given prefix: a predicate read, the kind that phantoms attack.
func (t *Txn) Scan(prefix string) (result map[string]int64, err error) {
	err = t.guarded(func() error {
		var base map[string]int64
		switch t.Level {
		case Snapshot:
			base = map[string]int64{}
			for k, v := range t.Snapshot {
				if strings.HasPrefix(k, prefix) {
					base[k] = v
				}
			}
		case Serializable:
			if e := t.Store.LockPredicate(t.ID, prefix); e != nil {
				return e
			}
			base = t.Store.CommittedWithPrefix(prefix)
			for _, k := range sortedKeys(base) {
				if e := t.Store.LockShared(t.ID, k); e != nil {
					return e
				}
			}
		case RepeatableRead:
			base = t.Store.CommittedWithPrefix(prefix)
			for _, k := range sortedKeys(base) { // item locks only: new keys are unprotected
				if e := t.Store.LockShared(t.ID, k); e != nil {
					return e
				}
			}
		default:
			base = t.Store.CommittedWithPrefix(prefix)
		}
		result = map[string]int64{}
		for k, v := range base {
			result[k] = v
		}
		if t.Level != ReadUncommitted {
			for _, k := range t.Writes.keys {
				if strings.HasPrefix(k, prefix) {
					result[k] = t.Writes.vals[k]
				}
			}
		}
		t.Store.Trace = append(t.Store.Trace, fmt.Sprintf("  T%d scan %s* = %s", t.ID, prefix, formatMap(result)))
		return nil
	})
	return result, err
}

func (t *Txn) Write(key string, value int64) error {
	return t.guarded(func() error {
		switch t.Level {
		case ReadUncommitted:
			if e := t.Store.LockExclusive(t.ID, key); e != nil { // still no dirty WRITES: X lock held to commit
				return e
			}
			if _, seen := t.Undo[key]; !seen {
				old, present := lookup(t.Store.Committed, key)
				t.Undo[key] = undoEntry{old, present}
				t.undoKeys = append(t.undoKeys, key)
			}
			t.Store.Committed[key] = value // applied in place, visible to everyone now
		case ReadCommitted, RepeatableRead:
			if e := t.Store.LockExclusive(t.ID, key); e != nil {
				return e
			}
			t.Writes.put(key, value)
		case Serializable:
			if e := t.Store.CheckPredicateConflict(t.ID, key); e != nil {
				return e
			}
			if e := t.Store.LockExclusive(t.ID, key); e != nil {
				return e
			}
			t.Writes.put(key, value)
		case Snapshot:
			t.Writes.put(key, value) // no locks; validated at commit
		}
		t.Store.Trace = append(t.Store.Trace, fmt.Sprintf("  T%d write %s = %d", t.ID, key, value))
		return nil
	})
}

func (t *Txn) Commit() error {
	return t.guarded(func() error {
		if t.Level == Snapshot {
			for _, k := range t.Writes.keys {
				v := t.Store.VersionOf[k]
				if v > t.SnapshotVersion {
					return conflict("T%d first-committer-wins: %s committed at v%d after snapshot v%d", t.ID, k, v, t.SnapshotVersion)
				}
			}
		}
		t.Store.CommitClock++
		if t.Level == ReadUncommitted {
			for _, k := range t.undoKeys {
				t.Store.VersionOf[k] = t.Store.CommitClock
			}
		} else {
			for _, k := range t.Writes.keys {
				t.Store.Committed[k] = t.Writes.vals[k]
				t.Store.VersionOf[k] = t.Store.CommitClock
			}
		}
		t.Status = Committed
		t.Store.ReleaseAll(t.ID)
		t.Store.Trace = append(t.Store.Trace, fmt.Sprintf("  T%d COMMIT (v%d)", t.ID, t.Store.CommitClock))
		return nil
	})
}

func (t *Txn) Abort() {
	if t.Status != Active {
		return
	}
	if t.Level == ReadUncommitted {
		for _, k := range t.undoKeys {
			u := t.Undo[k]
			if !u.Present {
				delete(t.Store.Committed, k)
			} else {
				t.Store.Committed[k] = u.Value
			}
		}
	}
	t.Status = Aborted
	t.Store.ReleaseAll(t.ID)
	t.Store.Trace = append(t.Store.Trace, fmt.Sprintf("  T%d ABORT", t.ID))
}

// ---- formatting helpers (Java's TreeMap / Long toString) ----

func formatValue(v int64, present bool) string {
	if !present {
		return "null"
	}
	return fmt.Sprintf("%d", v)
}

func formatMap(m map[string]int64) string {
	parts := make([]string, 0, len(m))
	for _, k := range sortedKeys(m) {
		parts = append(parts, fmt.Sprintf("%s=%d", k, m[k]))
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortedInts(set map[int]bool) []int {
	out := make([]int, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Ints(out)
	return out
}

// Outcome is one scenario's result: did the anomaly show up at this level, and the trace that proves it.
type Outcome struct {
	Anomaly  string
	Level    Isolation
	Observed bool
	Detail   string
	Trace    []string
}

// Anomalies lists the five scenarios in Berenson et al. order.
var Anomalies = []string{"dirty read", "non-repeatable read", "phantom", "lost update", "write skew"}

// AnomalyLab: each scenario is a fixed interleaving of two transactions that produces one anomaly when the level allows it.
// Scripts keep going after a conflict: the aborted transaction's later steps return errors that are simply skipped, as a retry loop would.
type AnomalyLab struct{}

func (AnomalyLab) Run(anomaly string, level Isolation) (Outcome, error) {
	switch anomaly {
	case "dirty read":
		return DirtyRead(level), nil
	case "non-repeatable read":
		return NonRepeatableRead(level), nil
	case "phantom":
		return Phantom(level), nil
	case "lost update":
		return LostUpdate(level), nil
	case "write skew":
		return WriteSkew(level), nil
	default:
		return Outcome{}, fmt.Errorf("%w: %s", ErrUnknownAnomaly, anomaly)
	}
}

// DirtyRead is P1: T2 reads a value T1 wrote but then rolled back.
func DirtyRead(level Isolation) Outcome {
	s := NewStore()
	s.Load("acct:alice", 1000)
	t1, t2 := s.Begin(level), s.Begin(level)
	_ = t1.Write("acct:alice", 900) // alice pays 1.00, not yet committed
	seen, seenOK, _ := t2.Read("acct:alice")
	t1.Abort() // the payment is declined and rolled back
	_ = t2.Commit()
	observed := seenOK && seen == 900
	cv, cvOK := lookup(s.Committed, "acct:alice")
	return Outcome{"dirty read", level, observed, fmt.Sprintf("T2 saw %s; committed value is %s", formatValue(seen, seenOK), formatValue(cv, cvOK)), s.Trace}
}

// NonRepeatableRead is P2: T1 reads the same key twice and gets different values because T2 committed in between.
func NonRepeatableRead(level Isolation) Outcome {
	s := NewStore()
	s.Load("acct:alice", 1000)
	t1, t2 := s.Begin(level), s.Begin(level)
	first, firstOK, _ := t1.Read("acct:alice")
	_ = t2.Write("acct:alice", 500)
	_ = t2.Commit()
	second, secondOK, _ := t1.Read("acct:alice")
	_ = t1.Commit()
	observed := firstOK && secondOK && first != second
	return Outcome{"non-repeatable read", level, observed, fmt.Sprintf("T1 read %s then %s", formatValue(first, firstOK), formatValue(second, secondOK)), s.Trace}
}

// Phantom is P3: T1 runs the same predicate query twice; T2 inserted a matching row in between.
func Phantom(level Isolation) Outcome {
	s := NewStore()
	s.Load("posting:m42:001", 500)
	s.Load("posting:m42:002", 700)
	t1, t2 := s.Begin(level), s.Begin(level)
	var counts [2]int
	if r, err := t1.Scan("posting:m42:"); err == nil {
		counts[0] = len(r)
	}
	_ = t2.Write("posting:m42:003", 300) // a new posting for the same merchant
	_ = t2.Commit()
	if r, err := t1.Scan("posting:m42:"); err == nil {
		counts[1] = len(r)
	}
	_ = t1.Commit()
	observed := t1.Status != Aborted && counts[0] != counts[1]
	return Outcome{"phantom", level, observed, fmt.Sprintf("T1 counted %d then %d postings", counts[0], counts[1]), s.Trace}
}

// LostUpdate is P4: two read-modify-write deposits; one increment vanishes.
func LostUpdate(level Isolation) Outcome {
	s := NewStore()
	s.Load("acct:alice", 1000)
	t1, t2 := s.Begin(level), s.Begin(level)
	r1, r1OK, _ := t1.Read("acct:alice")
	r2, r2OK, _ := t2.Read("acct:alice")
	if r1OK {
		_ = t1.Write("acct:alice", r1+100) // deposit 1.00
	}
	_ = t1.Commit()
	if r2OK {
		_ = t2.Write("acct:alice", r2+200) // deposit 2.00
	}
	_ = t2.Commit()
	finalBalance := s.Committed["acct:alice"]
	bothCommitted := t1.Status == Committed && t2.Status == Committed
	observed := bothCommitted && finalBalance != 1300
	return Outcome{"lost update", level, observed,
		fmt.Sprintf("final balance %d (expected 1300 if both deposits landed); T1 %s, T2 %s", finalBalance, t1.Status, t2.Status), s.Trace}
}

// WriteSkew is A5B: two transactions each check an invariant over two keys and each write a different key; the invariant breaks.
func WriteSkew(level Isolation) Outcome {
	s := NewStore()
	s.Load("acct:alice:checking", 600)
	s.Load("acct:alice:savings", 600)
	t1, t2 := s.Begin(level), s.Begin(level)
	var c, sv [2]int64
	readBoth := func(t *Txn, i int) {
		var err error
		if c[i], _, err = t.Read("acct:alice:checking"); err != nil {
			return
		}
		sv[i], _, _ = t.Read("acct:alice:savings")
	}
	readBoth(t1, 0) // T1 checks: 1200 >= 1000
	readBoth(t2, 1) // T2 checks: 1200 >= 1000
	if c[0]+sv[0]-1000 >= 0 {
		_ = t1.Write("acct:alice:checking", c[0]-1000) // withdraw 10.00 from checking
	}
	if c[1]+sv[1]-1000 >= 0 {
		_ = t2.Write("acct:alice:savings", sv[1]-1000) // withdraw 10.00 from savings
	}
	_ = t1.Commit()
	_ = t2.Commit()
	sum := s.Committed["acct:alice:checking"] + s.Committed["acct:alice:savings"]
	observed := sum < 0
	return Outcome{"write skew", level, observed,
		fmt.Sprintf("combined balance %d (invariant: >= 0); T1 %s, T2 %s", sum, t1.Status, t2.Status), s.Trace}
}

// Matrix is anomaly -> level -> observed? Iterate with Anomalies and Levels for a stable order.
func (lab AnomalyLab) Matrix() map[string]map[Isolation]bool {
	m := map[string]map[Isolation]bool{}
	for _, a := range Anomalies {
		row := map[Isolation]bool{}
		for _, l := range Levels {
			out, _ := lab.Run(a, l)
			row[l] = out.Observed
		}
		m[a] = row
	}
	return m
}

func printOutcome(o Outcome) {
	for _, line := range o.Trace {
		fmt.Println(line)
	}
	fmt.Println("  => " + o.Detail)
}

func main() {
	var lab AnomalyLab
	fmt.Println("== Which anomalies does each level allow? (Y = anomaly observed) ==")
	fmt.Printf("%-20s %-18s %-16s %-16s %-10s %-12s\n", "anomaly", "READ_UNCOMMITTED", "READ_COMMITTED", "REPEATABLE_READ", "SNAPSHOT", "SERIALIZABLE")
	matrix := lab.Matrix()
	widths := []int{18, 16, 16, 10, 12}
	for _, a := range Anomalies {
		line := fmt.Sprintf("%-20s ", a)
		for i, l := range Levels {
			mark := "-"
			if matrix[a][l] {
				mark = "Y"
			}
			line += fmt.Sprintf("%-*s ", widths[i], mark)
		}
		fmt.Println(strings.TrimRight(line, " "))
	}

	fmt.Println()
	fmt.Println("== Write skew under SNAPSHOT: both pass the check, both commit, the invariant is gone ==")
	printOutcome(WriteSkew(Snapshot))

	fmt.Println()
	fmt.Println("== Same interleaving under SERIALIZABLE: the second writer is refused ==")
	printOutcome(WriteSkew(Serializable))

	fmt.Println()
	fmt.Println("== Phantom under REPEATABLE_READ (item locks cannot lock a row that does not exist yet) ==")
	printOutcome(Phantom(RepeatableRead))

	fmt.Println()
	fmt.Println("== Lost update under SNAPSHOT: first committer wins, second is told ==")
	printOutcome(LostUpdate(Snapshot))
}
