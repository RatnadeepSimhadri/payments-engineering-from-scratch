// Lesson: phases/07-storage-and-transactions/05-two-phase-locking/docs/en.md
// Two-Phase Locking and Deadlock Detection
// Run:  go run .
// Test: go test ./...
// Sources: Eswaran, Gray, Lorie, Traiger, "The Notions of Consistency and Predicate Locks in a Database System"
// (CACM 1976); Gray & Reuter, Transaction Processing (1993) ch. 7-8. Go 1.22+, standard library only. Same design as Java and Python.
package main

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
)

// ---------------------------------------------------------------------------------------------
// Money (Phase 10, Lesson 01 design): integer minor units plus a currency. Never a float.
// ---------------------------------------------------------------------------------------------

// Currency is an ISO 4217 code and the number of decimals in its minor unit.
type Currency struct {
	Code     string
	Exponent int
}

var USD = Currency{"USD", 2}

var (
	ErrCurrencyMismatch = errors.New("currency mismatch")
	ErrOverflow         = errors.New("int64 overflow")
)

// Money is an exact amount of one currency stored as a signed count of minor units.
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

func (m Money) Plus(other Money) (Money, error) {
	if m.Currency != other.Currency {
		return Money{}, ErrCurrencyMismatch
	}
	sum, err := addChecked(m.MinorUnits, other.MinorUnits)
	return Money{sum, m.Currency}, err
}

func (m Money) String() string {
	scale := int64(1)
	for i := 0; i < m.Currency.Exponent; i++ {
		scale *= 10
	}
	abs := m.MinorUnits
	if abs < 0 {
		abs = -abs
	}
	whole, frac := abs/scale, abs%scale
	sign := ""
	if m.MinorUnits < 0 {
		sign = "-"
	}
	if m.Currency.Exponent == 0 {
		return fmt.Sprintf("%s%d %s", sign, whole, m.Currency.Code)
	}
	return fmt.Sprintf("%s%d.%0*d %s", sign, whole, m.Currency.Exponent, frac, m.Currency.Code)
}

// ---------------------------------------------------------------------------------------------
// Lock manager: shared/exclusive locks per key, FIFO wait queues, and the wait-for graph.
// ---------------------------------------------------------------------------------------------

// LockMode is S (shared) or X (exclusive).
type LockMode int

const (
	S LockMode = iota
	X
)

func (m LockMode) String() string { return [...]string{"S", "X"}[m] }

// CompatibleWith is the compatibility matrix: S/S is the only pair that can coexist.
func (m LockMode) CompatibleWith(other LockMode) bool { return m == S && other == S }

// ErrTwoPhaseViolation is returned when a transaction requests a lock after it has released one: the two-phase rule.
var ErrTwoPhaseViolation = errors.New("two-phase violation")

// ErrSchedulerStuck is returned when no transaction can progress and no deadlock explains it.
var ErrSchedulerStuck = errors.New("scheduler stuck")

// ErrUnknownKey is returned when a script touches a key the database was never loaded with.
var ErrUnknownKey = errors.New("unknown key")

type LockRequest struct {
	Tx   int
	Mode LockMode
}

// LockEntry is per-key state: who holds the lock in which mode, and who is queued behind them in arrival order.
type LockEntry struct {
	Holders map[int]LockMode
	Queue   []LockRequest
}

func newLockEntry() *LockEntry { return &LockEntry{Holders: map[int]LockMode{}} }

// Compatible: could tx hold mode alongside every other current holder? Self is ignored (upgrades).
func (e *LockEntry) Compatible(tx int, mode LockMode) bool {
	for holder, held := range e.Holders {
		if holder != tx && !mode.CompatibleWith(held) {
			return false
		}
	}
	return true
}

func (e *LockEntry) IsEmpty() bool { return len(e.Holders) == 0 && len(e.Queue) == 0 }

// LockManager owns the lock table. It appends to the scheduler's log through a pointer, as the Java version shares the list.
type LockManager struct {
	Table map[string]*LockEntry
	log   *[]string
}

func NewLockManager(log *[]string) *LockManager {
	return &LockManager{Table: map[string]*LockEntry{}, log: log}
}

func (lm *LockManager) logf(format string, args ...any) {
	*lm.log = append(*lm.log, fmt.Sprintf(format, args...))
}

// Acquire requests a lock. Returns true if granted now, false if the request was queued and the caller must wait.
// FIFO: a request behind a queued request waits even if it would be compatible with the current holders,
// so a stream of readers cannot starve a writer. An upgrade (S held, X wanted) is queued at the front.
func (lm *LockManager) Acquire(tx int, key string, mode LockMode) bool {
	e, ok := lm.Table[key]
	if !ok {
		e = newLockEntry()
		lm.Table[key] = e
	}
	held, holding := e.Holders[tx]
	if holding && (held == X || (held == S && mode == S)) {
		return true // already covered
	}
	upgrade := holding && held == S && mode == X
	if (len(e.Queue) == 0 || upgrade) && e.Compatible(tx, mode) {
		e.Holders[tx] = mode
		lm.logf("  T%d %s(%s) granted", tx, mode, key)
		return true
	}
	if upgrade {
		e.Queue = append([]LockRequest{{tx, mode}}, e.Queue...)
	} else {
		e.Queue = append(e.Queue, LockRequest{tx, mode})
	}
	who := make([]string, 0)
	for _, b := range lm.BlockersOf(tx, key, mode) {
		who = append(who, fmt.Sprintf("T%d", b))
	}
	lm.logf("  T%d %s(%s) WAITS for %s", tx, mode, key, strings.Join(who, ","))
	return false
}

// BlockersOf lists (sorted) the transactions tx would wait for if it requested mode on key now.
func (lm *LockManager) BlockersOf(tx int, key string, mode LockMode) []int {
	set := map[int]bool{}
	e, ok := lm.Table[key]
	if !ok {
		return []int{}
	}
	for holder, held := range e.Holders {
		if holder != tx && !mode.CompatibleWith(held) {
			set[holder] = true
		}
	}
	for _, ahead := range e.Queue {
		if ahead.Tx == tx {
			break // only requests ahead of ours
		}
		if !mode.CompatibleWith(ahead.Mode) {
			set[ahead.Tx] = true
		}
	}
	return sortedInts(set)
}

// Release one lock early (basic 2PL). Returns the transactions whose queued requests were granted.
func (lm *LockManager) Release(tx int, key string) []int {
	e, ok := lm.Table[key]
	if !ok {
		return []int{}
	}
	if _, held := e.Holders[tx]; !held {
		return []int{}
	}
	delete(e.Holders, tx)
	lm.logf("  T%d releases %s", tx, key)
	granted := lm.grantWaiters(key, e)
	if e.IsEmpty() {
		delete(lm.Table, key)
	}
	return granted
}

// ReleaseAll releases every lock tx holds or waits for (commit or abort). Returns newly granted transactions.
func (lm *LockManager) ReleaseAll(tx int) []int {
	granted := []int{}
	for _, key := range sortedKeys(lm.Table) {
		e := lm.Table[key]
		_, held := e.Holders[tx]
		delete(e.Holders, tx)
		kept := e.Queue[:0]
		for _, r := range e.Queue {
			if r.Tx != tx {
				kept = append(kept, r)
			}
		}
		e.Queue = kept
		if held {
			granted = append(granted, lm.grantWaiters(key, e)...)
		}
		if e.IsEmpty() {
			delete(lm.Table, key)
		}
	}
	return granted
}

func (lm *LockManager) grantWaiters(key string, e *LockEntry) []int {
	granted := []int{}
	for len(e.Queue) > 0 && e.Compatible(e.Queue[0].Tx, e.Queue[0].Mode) {
		r := e.Queue[0]
		e.Queue = e.Queue[1:]
		e.Holders[r.Tx] = r.Mode
		lm.logf("  T%d %s(%s) granted (was waiting)", r.Tx, r.Mode, key)
		granted = append(granted, r.Tx)
	}
	return granted
}

// KeysHeldBy lists (sorted) the keys tx currently holds a lock on.
func (lm *LockManager) KeysHeldBy(tx int) []string {
	out := []string{}
	for _, key := range sortedKeys(lm.Table) {
		if _, held := lm.Table[key].Holders[tx]; held {
			out = append(out, key)
		}
	}
	return out
}

// WaitForGraph builds the edges tx -> every transaction it is waiting for, derived from the queues.
func (lm *LockManager) WaitForGraph() *WaitForGraph {
	g := NewWaitForGraph()
	for _, key := range sortedKeys(lm.Table) {
		for _, r := range lm.Table[key].Queue {
			for _, blocker := range lm.BlockersOf(r.Tx, key, r.Mode) {
				g.AddEdge(r.Tx, blocker)
			}
		}
	}
	return g
}

// WaitForGraph is the directed graph "waits for". A cycle is a deadlock: nobody on it will ever be granted.
type WaitForGraph struct {
	Edges map[int]map[int]bool
}

func NewWaitForGraph() *WaitForGraph { return &WaitForGraph{Edges: map[int]map[int]bool{}} }

func (g *WaitForGraph) AddEdge(from, to int) {
	if g.Edges[from] == nil {
		g.Edges[from] = map[int]bool{}
	}
	g.Edges[from][to] = true
}

// FindCycle is a depth-first search with three colours; returns the first cycle found, in wait order, or false.
func (g *WaitForGraph) FindCycle() ([]int, bool) {
	colour := map[int]int{} // 0 white, 1 grey (on stack), 2 black
	path := []int{}
	for _, start := range sortedIntKeys(g.Edges) {
		if c, found := g.dfs(start, colour, &path); found {
			return c, true
		}
	}
	return nil, false
}

func (g *WaitForGraph) dfs(node int, colour map[int]int, path *[]int) ([]int, bool) {
	switch colour[node] {
	case 2:
		return nil, false
	case 1: // back edge: the cycle is the path from node to here
		cycle := []int{}
		in := false
		for _, n := range *path {
			if n == node {
				in = true
			}
			if in {
				cycle = append(cycle, n)
			}
		}
		return cycle, true
	}
	colour[node] = 1
	*path = append(*path, node)
	for _, next := range sortedInts(g.Edges[node]) {
		if found, ok := g.dfs(next, colour, path); ok {
			return found, true
		}
	}
	*path = (*path)[:len(*path)-1]
	colour[node] = 2
	return nil, false
}

func (g *WaitForGraph) String() string {
	parts := []string{}
	for _, from := range sortedIntKeys(g.Edges) {
		for _, to := range sortedInts(g.Edges[from]) {
			parts = append(parts, fmt.Sprintf("T%d -> T%d", from, to))
		}
	}
	if len(parts) == 0 {
		return "(no waits)"
	}
	return strings.Join(parts, ", ")
}

// ---------------------------------------------------------------------------------------------
// Transactions, operations, and the scheduler that interleaves them deterministically.
// ---------------------------------------------------------------------------------------------

// VictimPolicy decides which transaction on a cycle is aborted.
type VictimPolicy int

const (
	// Youngest aborts the transaction that started last: it has presumably done the least work.
	Youngest VictimPolicy = iota
	// FewestLocks aborts the transaction holding the fewest locks: the cheapest to undo and the least likely to be a batch job.
	FewestLocks
)

func (p VictimPolicy) String() string { return [...]string{"YOUNGEST", "FEWEST_LOCKS"}[p] }

// Op is a step a scripted transaction can take (Java's sealed interface Op). OpAdd is a read-modify-write that needs an X lock.
type Op interface{ isOp() }

type OpRead struct{ Key string }
type OpAdd struct {
	Key   string
	Delta Money
}
type OpUnlock struct{ Key string }
type OpCommit struct{}

func (OpRead) isOp()   {}
func (OpAdd) isOp()    {}
func (OpUnlock) isOp() {}
func (OpCommit) isOp() {}

// Status of a transaction.
type Status int

const (
	Active Status = iota
	Blocked
	Committed
	Aborted
)

func (s Status) String() string { return [...]string{"ACTIVE", "BLOCKED", "COMMITTED", "ABORTED"}[s] }

// Phase of two-phase locking.
type Phase int

const (
	Growing Phase = iota
	Shrinking
)

func (p Phase) String() string { return [...]string{"GROWING", "SHRINKING"}[p] }

type Txn struct {
	ID       int
	Script   []Op
	PC       int
	Status   Status
	Phase    Phase
	Restarts int
	undoKeys []string         // insertion order of Undo (LinkedHashMap)
	Undo     map[string]Money // key -> value before our first in-place write
	Reads    map[string]Money // what this transaction observed (for tests)
}

func newTxn(id int, script []Op) *Txn {
	copied := make([]Op, len(script))
	copy(copied, script)
	return &Txn{ID: id, Script: copied, Status: Active, Phase: Growing, Undo: map[string]Money{}, Reads: map[string]Money{}}
}

func (t *Txn) Finished() bool { return t.Status == Committed || t.Status == Aborted }

type Scheduler struct {
	DB                map[string]Money
	Log               []string
	Locks             *LockManager
	Txns              map[int]*Txn
	Policy            VictimPolicy
	RestartVictims    bool
	nextID            int
	DeadlocksDetected int
	Cycles            [][]int
	Victims           []int
}

func NewScheduler(policy VictimPolicy, restartVictims bool) *Scheduler {
	s := &Scheduler{DB: map[string]Money{}, Txns: map[int]*Txn{}, Policy: policy, RestartVictims: restartVictims, nextID: 1}
	s.Locks = NewLockManager(&s.Log)
	return s
}

func (s *Scheduler) logf(format string, args ...any) {
	s.Log = append(s.Log, fmt.Sprintf(format, args...))
}

func (s *Scheduler) Load(key string, value Money) { s.DB[key] = value }

func (s *Scheduler) Begin(script []Op) *Txn {
	t := newTxn(s.nextID, script)
	s.nextID++
	s.Txns[t.ID] = t
	s.logf("T%d begin", t.ID)
	return t
}

// Run drives the transactions. order names which transaction takes its next step at each tick; a blocked or
// finished transaction's turn is skipped. When the explicit order is exhausted, remaining steps run round-robin.
func (s *Scheduler) Run(order ...int) error {
	for _, id := range order {
		if _, err := s.Step(s.Txns[id]); err != nil {
			return err
		}
	}
	guard := 0
	for s.anyUnfinished() {
		progressed := false
		for _, id := range sortedIntKeys(s.Txns) {
			advanced, err := s.Step(s.Txns[id])
			if err != nil {
				return err
			}
			progressed = progressed || advanced
		}
		if !progressed {
			if _, _, err := s.DetectAndResolve(); err != nil {
				return err
			}
			guard++
			if guard > 10_000 {
				return fmt.Errorf("%w: %s", ErrSchedulerStuck, s.Locks.WaitForGraph())
			}
		} else {
			guard = 0
		}
	}
	return nil
}

func (s *Scheduler) anyUnfinished() bool {
	for _, t := range s.Txns {
		if !t.Finished() {
			return true
		}
	}
	return false
}

// Step executes one step of t. Returns true if the transaction advanced (blocking counts as progress for the scheduler).
func (s *Scheduler) Step(t *Txn) (bool, error) {
	if t.Status != Active {
		return false, nil
	}
	ok, err := s.execute(t, t.Script[t.PC])
	if err != nil {
		return false, err
	}
	if ok {
		t.PC++
		return true, nil
	}
	t.Status = Blocked
	_, _, err = s.DetectAndResolve()
	return true, err
}

func (s *Scheduler) execute(t *Txn, op Op) (bool, error) {
	switch o := op.(type) {
	case OpRead:
		granted, err := s.lock(t, o.Key, S)
		if err != nil || !granted {
			return false, err
		}
		v := s.DB[o.Key]
		t.Reads[o.Key] = v
		s.logf("  T%d read %s = %s", t.ID, o.Key, v)
	case OpAdd:
		granted, err := s.lock(t, o.Key, X)
		if err != nil || !granted {
			return false, err
		}
		before, present := s.DB[o.Key]
		if !present {
			return false, fmt.Errorf("%w: %s", ErrUnknownKey, o.Key)
		}
		if _, seen := t.Undo[o.Key]; !seen {
			t.Undo[o.Key] = before
			t.undoKeys = append(t.undoKeys, o.Key)
		}
		after, err := before.Plus(o.Delta)
		if err != nil {
			return false, err
		}
		s.DB[o.Key] = after // in place: strict 2PL keeps it invisible until commit
		s.logf("  T%d %s %s -> %s", t.ID, o.Key, before, after)
	case OpUnlock:
		t.Phase = Shrinking
		if err := s.grant(s.Locks.Release(t.ID, o.Key)); err != nil {
			return false, err
		}
	case OpCommit:
		if err := s.Commit(t); err != nil {
			return false, err
		}
	}
	return true, nil
}

func (s *Scheduler) lock(t *Txn, key string, mode LockMode) (bool, error) {
	if t.Phase == Shrinking && !containsString(s.Locks.KeysHeldBy(t.ID), key) {
		return false, fmt.Errorf("%w: T%d requested %s(%s) after releasing a lock", ErrTwoPhaseViolation, t.ID, mode, key)
	}
	return s.Locks.Acquire(t.ID, key, mode), nil
}

func (s *Scheduler) Commit(t *Txn) error {
	t.Status = Committed
	s.logf("  T%d COMMIT", t.ID)
	return s.grant(s.Locks.ReleaseAll(t.ID))
}

func (s *Scheduler) Abort(t *Txn) error {
	n := len(t.undoKeys)
	for i := n - 1; i >= 0; i-- {
		s.DB[t.undoKeys[i]] = t.Undo[t.undoKeys[i]]
	}
	t.Undo = map[string]Money{}
	t.undoKeys = nil
	t.Status = Aborted
	plural := "s"
	if n == 1 {
		plural = ""
	}
	s.logf("  T%d ABORT (undo %d write%s)", t.ID, n, plural)
	if err := s.grant(s.Locks.ReleaseAll(t.ID)); err != nil {
		return err
	}
	if s.RestartVictims {
		t.Status = Active
		t.Phase = Growing
		t.PC = 0
		t.Reads = map[string]Money{}
		t.Restarts++
		s.logf("  T%d restarted (attempt %d)", t.ID, t.Restarts+1)
	}
	return nil
}

// grant: transactions whose queued request was just granted continue with the step they were blocked on.
func (s *Scheduler) grant(granted []int) error {
	for _, id := range granted {
		w := s.Txns[id]
		if w.Status != Blocked {
			continue
		}
		w.Status = Active
		ok, err := s.execute(w, w.Script[w.PC])
		if err != nil {
			return err
		}
		if ok {
			w.PC++
		} else {
			w.Status = Blocked
		}
	}
	return nil
}

// DetectAndResolve builds the wait-for graph; if it has a cycle, picks a victim by policy and aborts it.
// Returns the victim id and whether a deadlock was found (Java's Optional<Integer>).
func (s *Scheduler) DetectAndResolve() (int, bool, error) {
	g := s.Locks.WaitForGraph()
	cycle, found := g.FindCycle()
	if !found {
		return 0, false, nil
	}
	s.DeadlocksDetected++
	s.Cycles = append(s.Cycles, cycle)
	victim := s.ChooseVictim(cycle)
	s.Victims = append(s.Victims, victim)
	s.logf("  DEADLOCK %s -> victim T%d (%s)", Describe(cycle), victim, s.Policy)
	if err := s.Abort(s.Txns[victim]); err != nil {
		return victim, true, err
	}
	return victim, true, nil
}

func (s *Scheduler) ChooseVictim(cycle []int) int {
	best := cycle[0]
	switch s.Policy {
	case Youngest:
		for _, id := range cycle {
			if id > best {
				best = id
			}
		}
	case FewestLocks:
		for _, id := range cycle {
			mine, theirs := len(s.Locks.KeysHeldBy(id)), len(s.Locks.KeysHeldBy(best))
			if mine < theirs || (mine == theirs && id > best) { // tie: youngest
				best = id
			}
		}
	}
	return best
}

func Describe(cycle []int) string {
	var sb strings.Builder
	for _, id := range cycle {
		fmt.Fprintf(&sb, "T%d -> ", id)
	}
	fmt.Fprintf(&sb, "T%d", cycle[0])
	return sb.String()
}

func (s *Scheduler) Total(currency Currency) (Money, error) {
	sum := OfMinor(0, currency)
	for _, key := range sortedKeys(s.DB) {
		var err error
		if sum, err = sum.Plus(s.DB[key]); err != nil {
			return Money{}, err
		}
	}
	return sum, nil
}

func (s *Scheduler) LockTableEmpty() bool { return len(s.Locks.Table) == 0 }

// ---- small helpers standing in for TreeMap/TreeSet ordering ----

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortedIntKeys[V any](m map[int]V) []int {
	keys := make([]int, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	return keys
}

func sortedInts(set map[int]bool) []int { return sortedIntKeys(set) }

func containsString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func formatInts(ids []int) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = fmt.Sprintf("%d", id)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// ---------------------------------------------------------------------------------------------
// Worked example
// ---------------------------------------------------------------------------------------------

func usd(minor int64) Money { return OfMinor(minor, USD) }

// Transfer amount from `from` to `to`: two read-modify-writes under X locks, then commit.
func Transfer(from, to string, amountMinor int64) []Op {
	return []Op{OpAdd{from, usd(-amountMinor)}, OpAdd{to, usd(amountMinor)}, OpCommit{}}
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}

func mustStep(s *Scheduler, t *Txn) {
	_, err := s.Step(t)
	must(err)
}

func main() {
	fmt.Println("== Two transfers in opposite directions: A->B and B->A, strict 2PL ==")
	s := NewScheduler(Youngest, true)
	s.Load("acct:A", usd(100_000))
	s.Load("acct:B", usd(50_000))
	s.Begin(Transfer("acct:A", "acct:B", 10_000)) // T1: 100.00 from A to B
	s.Begin(Transfer("acct:B", "acct:A", 5_000))  // T2:  50.00 from B to A
	must(s.Run(1, 2, 1, 2))                       // T1 locks A, T2 locks B, T1 wants B, T2 wants A
	for _, line := range s.Log {
		fmt.Println(line)
	}
	fmt.Printf("  => deadlocks detected: %d, victims: %s\n", s.DeadlocksDetected, formatInts(s.Victims))
	total, err := s.Total(USD)
	must(err)
	fmt.Printf("  => final A = %s, B = %s, total = %s\n", s.DB["acct:A"], s.DB["acct:B"], total)

	fmt.Println()
	fmt.Println("== Same shape, FEWEST_LOCKS policy: T2 also debits a fee account, so it holds two locks ==")
	f := NewScheduler(FewestLocks, true)
	f.Load("acct:A", usd(100_000))
	f.Load("acct:B", usd(50_000))
	f.Load("fees", usd(0))
	f.Begin(Transfer("acct:A", "acct:B", 10_000))
	f.Begin([]Op{OpAdd{"acct:B", usd(-5_050)}, OpAdd{"fees", usd(50)}, OpAdd{"acct:A", usd(5_000)}, OpCommit{}})
	must(f.Run(1, 2, 2, 1, 2)) // T2 holds B and fees (2 locks) when the cycle forms; T1 holds A
	for _, line := range f.Log {
		if strings.Contains(line, "DEADLOCK") || strings.Contains(line, "ABORT") {
			fmt.Println(line)
		}
	}
	fmt.Printf("  => victim T%d: T1 holds 1 lock, T2 holds 2, so the cheaper rollback is T1\n", f.Victims[0])

	fmt.Println()
	fmt.Println("== Lock upgrade deadlock: two deposits each read the balance (S) then write it (X) ==")
	u := NewScheduler(Youngest, true)
	u.Load("acct:A", usd(100_000))
	u.Begin([]Op{OpRead{"acct:A"}, OpAdd{"acct:A", usd(1_000)}, OpCommit{}})
	u.Begin([]Op{OpRead{"acct:A"}, OpAdd{"acct:A", usd(2_000)}, OpCommit{}})
	must(u.Run(1, 2, 1, 2))
	for _, line := range u.Log {
		fmt.Println(line)
	}
	fmt.Printf("  => final A = %s (both deposits landed; no lost update)\n", u.DB["acct:A"])

	fmt.Println()
	fmt.Println("== Why strict: basic 2PL releases X(A) before commit, a reader sees a balance that never existed ==")
	b := NewScheduler(Youngest, false)
	b.Load("acct:A", usd(100_000))
	writer := b.Begin([]Op{OpAdd{"acct:A", usd(-90_000)}, OpUnlock{"acct:A"}, OpRead{"acct:A"}})
	reader := b.Begin([]Op{OpRead{"acct:A"}, OpCommit{}})
	mustStep(b, writer)   // X(A), A := 10.00
	mustStep(b, writer)   // releases A early: legal in basic 2PL, phase -> SHRINKING
	mustStep(b, reader)   // reads 10.00, an uncommitted value
	mustStep(b, reader)   // and commits on it
	must(b.Abort(writer)) // writer rolls back: A is 100.00 again
	for _, line := range b.Log {
		fmt.Println(line)
	}
	fmt.Printf("  => T2 committed having read %s; the committed balance is %s\n", reader.Reads["acct:A"], b.DB["acct:A"])
	b2 := NewScheduler(Youngest, false)
	b2.Load("k", usd(1))
	b2.Load("j", usd(1))
	t := b2.Begin([]Op{OpAdd{"k", usd(1)}, OpUnlock{"k"}, OpRead{"j"}})
	mustStep(b2, t)
	mustStep(b2, t)
	if _, err := b2.Step(t); errors.Is(err, ErrTwoPhaseViolation) {
		msg := strings.TrimPrefix(err.Error(), ErrTwoPhaseViolation.Error()+": ")
		fmt.Printf("  => %s: the two-phase rule forbids growing after shrinking\n", msg)
	}
}
