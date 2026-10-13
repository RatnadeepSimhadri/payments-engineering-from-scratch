// Lesson: phases/04-concurrency/07-deadlocks-and-lock-ordering/docs/en.md
// Deadlocks, Lock Ordering, and Detection
// Run:  go run .
// Test: go test -race ./...
// Sources: Coffman, Elphick & Shoshani, "System Deadlocks" (ACM Computing Surveys, 1971); sync.Mutex.TryLock (Go 1.18+);
// PostgreSQL deadlock_timeout and wait-for-graph detector documentation.
// Go 1.22+, standard library only. Same design as the Java and Python versions.
package main

import (
	"errors"
	"fmt"
	"math/rand"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Money (copied from Phase 10, Lesson 01)
// ---------------------------------------------------------------------------

type Currency struct {
	Code     string
	Exponent int
}

var USD = Currency{"USD", 2}

func (c Currency) MinorUnitsPerMajor() int64 {
	scale := int64(1)
	for i := 0; i < c.Exponent; i++ {
		scale *= 10
	}
	return scale
}

type Money struct {
	MinorUnits int64
	Currency   Currency
}

func OfMinor(minorUnits int64, currency Currency) Money { return Money{minorUnits, currency} }

func (m Money) IsNegative() bool { return m.MinorUnits < 0 }

func (m Money) String() string {
	magnitude := m.MinorUnits
	if magnitude < 0 {
		magnitude = -magnitude
	}
	scale := m.Currency.MinorUnitsPerMajor()
	text := fmt.Sprintf("%d", magnitude/scale)
	if m.Currency.Exponent > 0 {
		text += fmt.Sprintf(".%0*d", m.Currency.Exponent, magnitude%scale)
	}
	if m.MinorUnits < 0 {
		text = "-" + text
	}
	return text + " " + m.Currency.Code
}

var (
	ErrInsufficientFunds = errors.New("insufficient funds")
	ErrNegativeAmount    = errors.New("amount must not be negative")
	ErrSameAccount       = errors.New("cannot transfer to the same account")
	ErrGaveUp            = errors.New("transfer gave up")
)

// DeadlockDetectedError is returned by LockTracker when granting a lock request would close a
// cycle in the wait-for graph.
type DeadlockDetectedError struct{ Cycle []string }

func (e *DeadlockDetectedError) Error() string {
	return "deadlock: " + strings.Join(e.Cycle, " -> ") + " -> " + e.Cycle[0]
}

// ---------------------------------------------------------------------------
// Wait-for graph and the tracker that builds it
// ---------------------------------------------------------------------------

// WaitForGraph is a directed graph of "owner X waits for a lock held by owner Y". A cycle is a
// deadlock. Nodes are sorted so the cycle reported is the same on every run.
type WaitForGraph struct {
	edges map[string]map[string]bool
}

func NewWaitForGraph() *WaitForGraph { return &WaitForGraph{edges: map[string]map[string]bool{}} }

func (g *WaitForGraph) AddEdge(waiter, holder string) {
	if g.edges[waiter] == nil {
		g.edges[waiter] = map[string]bool{}
	}
	g.edges[waiter][holder] = true
	if g.edges[holder] == nil {
		g.edges[holder] = map[string]bool{}
	}
}

func (g *WaitForGraph) Nodes() []string {
	nodes := make([]string, 0, len(g.edges))
	for n := range g.edges {
		nodes = append(nodes, n)
	}
	sort.Strings(nodes)
	return nodes
}

func (g *WaitForGraph) Successors(node string) []string {
	next := make([]string, 0, len(g.edges[node]))
	for n := range g.edges[node] {
		next = append(next, n)
	}
	sort.Strings(next)
	return next
}

// FindCycle returns the owners on the first cycle found, in edge order starting from the smallest
// id, or nil. Iterative DFS with three colours: white (unvisited), grey (on the path), black (done).
func (g *WaitForGraph) FindCycle() []string {
	colour := map[string]int{} // absent = white, 1 = grey, 2 = black
	parent := map[string]string{}
	for _, start := range g.Nodes() {
		if _, seen := colour[start]; seen {
			continue
		}
		stack := []string{start}
		for len(stack) > 0 {
			node := stack[len(stack)-1]
			if _, seen := colour[node]; !seen {
				colour[node] = 1
				for _, next := range g.Successors(node) {
					c, visited := colour[next]
					if !visited {
						parent[next] = node
						stack = append(stack, next)
					} else if c == 1 {
						return pathOfCycle(parent, node, next) // back edge: next is on the current path
					}
				}
			} else {
				stack = stack[:len(stack)-1]
				colour[node] = 2
			}
		}
	}
	return nil
}

func pathOfCycle(parent map[string]string, from, to string) []string {
	var cycle []string
	for cursor := from; cursor != to; cursor = parent[cursor] {
		cycle = append(cycle, cursor)
	}
	cycle = append(cycle, to)
	for i, j := 0, len(cycle)-1; i < j; i, j = i+1, j-1 { // edge order: to -> ... -> from
		cycle[i], cycle[j] = cycle[j], cycle[i]
	}
	smallest := 0
	for i := range cycle {
		if cycle[i] < cycle[smallest] {
			smallest = i
		}
	}
	return append(append([]string{}, cycle[smallest:]...), cycle[:smallest]...)
}

// LockTracker records every lock request, acquisition, and release so a wait-for graph can be
// built at any moment. Requesting is the detector: it refuses a request that would complete a cycle.
type LockTracker struct {
	mu      sync.Mutex
	holders map[string]string // lockID -> owner
	waiting map[string]string // owner -> lockID
	events  []string
}

func NewLockTracker() *LockTracker {
	return &LockTracker{holders: map[string]string{}, waiting: map[string]string{}}
}

func (t *LockTracker) Requesting(owner, lockID string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.waiting[owner] = lockID
	t.events = append(t.events, owner+" requests "+lockID)
	cycle := t.graphLocked().FindCycle()
	for _, n := range cycle {
		if n == owner {
			delete(t.waiting, owner)
			t.events = append(t.events, fmt.Sprintf("%s aborted: deadlock %v", owner, cycle))
			return &DeadlockDetectedError{Cycle: cycle}
		}
	}
	return nil
}

func (t *LockTracker) Acquired(owner, lockID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.waiting, owner)
	t.holders[lockID] = owner
	t.events = append(t.events, owner+" acquired "+lockID)
}

func (t *LockTracker) Released(owner, lockID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.holders[lockID] == owner {
		delete(t.holders, lockID)
	}
	t.events = append(t.events, owner+" released "+lockID)
}

func (t *LockTracker) Graph() *WaitForGraph {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.graphLocked()
}

// graphLocked builds one edge per waiting owner: waiter -> whoever holds the lock it wants.
func (t *LockTracker) graphLocked() *WaitForGraph {
	g := NewWaitForGraph()
	for waiter, lockID := range t.waiting {
		if holder, held := t.holders[lockID]; held && holder != waiter {
			g.AddEdge(waiter, holder)
		}
	}
	return g
}

func (t *LockTracker) Detect() []string { return t.Graph().FindCycle() }

func (t *LockTracker) Events() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string{}, t.events...)
}

// TrackedLock is a mutex that reports to a tracker. Lock may return a DeadlockDetectedError
// before blocking.
type TrackedLock struct {
	ID      string
	mu      sync.Mutex
	tracker *LockTracker // nil = untracked, plain lock
	held    sync.Mutex   // guards the locked flag below
	locked  bool
}

func NewTrackedLock(id string, tracker *LockTracker) *TrackedLock {
	return &TrackedLock{ID: id, tracker: tracker}
}

func (l *TrackedLock) Lock(owner string) error {
	if l.tracker != nil {
		if err := l.tracker.Requesting(owner, l.ID); err != nil {
			return err // refuse instead of blocking into a cycle
		}
	}
	l.mu.Lock()
	l.setLocked(true)
	if l.tracker != nil {
		l.tracker.Acquired(owner, l.ID)
	}
	return nil
}

func (l *TrackedLock) TryLock(owner string) bool {
	if !l.mu.TryLock() {
		return false
	}
	l.setLocked(true)
	if l.tracker != nil {
		l.tracker.Acquired(owner, l.ID)
	}
	return true
}

func (l *TrackedLock) Unlock(owner string) {
	if l.tracker != nil {
		l.tracker.Released(owner, l.ID)
	}
	l.setLocked(false)
	l.mu.Unlock()
}

func (l *TrackedLock) setLocked(v bool) {
	l.held.Lock()
	l.locked = v
	l.held.Unlock()
}

// IsLocked exists for tests; sync.Mutex has no query, so the flag is kept beside it.
func (l *TrackedLock) IsLocked() bool {
	l.held.Lock()
	defer l.held.Unlock()
	return l.locked
}

// ---------------------------------------------------------------------------
// Accounts and transfers
// ---------------------------------------------------------------------------

// Account is an account whose balance is guarded by its own lock.
type Account struct {
	ID       string
	Currency Currency
	Lock     *TrackedLock
	balance  int64 // guarded by Lock
}

func NewAccount(id string, currency Currency, openingMinorUnits int64, tracker *LockTracker) *Account {
	return &Account{ID: id, Currency: currency, Lock: NewTrackedLock("lock:"+id, tracker), balance: openingMinorUnits}
}

// BalanceMinor must be called with Lock held (or when no one else is running).
func (a *Account) BalanceMinor() int64 { return a.balance }
func (a *Account) Adjust(delta int64)  { a.balance += delta }

func (a *Account) Balance() Money {
	_ = a.Lock.Lock("reader")
	defer a.Lock.Unlock("reader")
	return OfMinor(a.balance, a.Currency)
}

// Backoff pauses between try-lock attempts: a seeded random spin then a yield. Counts pauses; never sleeps.
type Backoff struct {
	mu     sync.Mutex
	random *rand.Rand
	pauses int64
}

func NewBackoff(seed int64) *Backoff { return &Backoff{random: rand.New(rand.NewSource(seed))} }

func (b *Backoff) Pause(attempt int) {
	b.mu.Lock()
	b.pauses++
	shift := attempt
	if shift > 10 {
		shift = 10 // exponential cap at 1024
	}
	spins := b.random.Intn(1 << shift)
	b.mu.Unlock()
	for i := 0; i < spins; i++ {
		// spin
	}
	runtime.Gosched()
}

func (b *Backoff) Pauses() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.pauses
}

// TransferService offers three ways to move money between two accounts. One is a deadlock waiting to happen.
type TransferService struct {
	betweenLocks func() // test seam: runs after the first lock, before the second
	backoff      *Backoff
	maxAttempts  int
}

func NewTransferService(betweenLocks func(), backoff *Backoff, maxAttempts int) *TransferService {
	if betweenLocks == nil {
		betweenLocks = func() {}
	}
	if maxAttempts <= 0 {
		maxAttempts = 1_000_000
	}
	return &TransferService{betweenLocks: betweenLocks, backoff: backoff, maxAttempts: maxAttempts}
}

// NaiveTransfer locks from, then to. Two transfers in opposite directions can each take their
// first lock and wait forever.
func (s *TransferService) NaiveTransfer(from, to *Account, amount Money, owner string) error {
	if err := requireDistinct(from, to); err != nil {
		return err
	}
	if err := from.Lock.Lock(owner); err != nil {
		return err
	}
	defer from.Lock.Unlock(owner)
	s.betweenLocks()
	if err := to.Lock.Lock(owner); err != nil {
		return err
	}
	defer to.Lock.Unlock(owner)
	return move(from, to, amount)
}

// OrderedTransfer is fix 1: a global order. Everyone locks the smaller account id first, so no cycle can form.
func (s *TransferService) OrderedTransfer(from, to *Account, amount Money, owner string) error {
	if err := requireDistinct(from, to); err != nil {
		return err
	}
	first, second := from, to
	if to.ID < from.ID {
		first, second = to, from
	}
	if err := first.Lock.Lock(owner); err != nil {
		return err
	}
	defer first.Lock.Unlock(owner)
	s.betweenLocks()
	if err := second.Lock.Lock(owner); err != nil {
		return err
	}
	defer second.Lock.Unlock(owner)
	return move(from, to, amount)
}

// TryLockTransfer is fix 2: never wait while holding. Try both; if the second is busy, release
// the first, back off, retry.
func (s *TransferService) TryLockTransfer(from, to *Account, amount Money, owner string) error {
	if err := requireDistinct(from, to); err != nil {
		return err
	}
	for attempt := 1; attempt <= s.maxAttempts; attempt++ {
		if from.Lock.TryLock(owner) {
			done, err := s.tryBoth(from, to, amount, owner)
			if done {
				return err
			}
		}
		s.backoff.Pause(attempt)
	}
	return fmt.Errorf("%w: %s -> %s after %d attempts", ErrGaveUp, from.ID, to.ID, s.maxAttempts)
}

// tryBoth runs with from locked and always releases it before returning, so nothing is held while backing off.
func (s *TransferService) tryBoth(from, to *Account, amount Money, owner string) (done bool, err error) {
	defer from.Lock.Unlock(owner)
	s.betweenLocks()
	if !to.Lock.TryLock(owner) {
		return false, nil
	}
	defer to.Lock.Unlock(owner)
	return true, move(from, to, amount)
}

// move runs with both locks held: the check-then-act and the two writes are one atomic step to any observer.
func move(from, to *Account, amount Money) error {
	if amount.IsNegative() {
		return fmt.Errorf("%w: %v", ErrNegativeAmount, amount)
	}
	if from.BalanceMinor() < amount.MinorUnits {
		return fmt.Errorf("%w: account %s has %d minor units, debit of %d refused", ErrInsufficientFunds, from.ID, from.BalanceMinor(), amount.MinorUnits)
	}
	from.Adjust(-amount.MinorUnits)
	to.Adjust(amount.MinorUnits)
	return nil
}

func requireDistinct(from, to *Account) error {
	if from.ID == to.ID {
		return fmt.Errorf("%w: %s", ErrSameAccount, from.ID)
	}
	return nil
}

// Barrier is a two-party (or n-party) meeting point; all must arrive before any continues. Single use.
type Barrier struct {
	arrive *sync.WaitGroup
}

func NewBarrier(parties int) *Barrier {
	var wg sync.WaitGroup
	wg.Add(parties)
	return &Barrier{arrive: &wg}
}

func (b *Barrier) Await() {
	b.arrive.Done()
	b.arrive.Wait()
}

// RunAll runs body on n goroutines and waits for all; fails loudly rather than hanging on a real deadlock.
func RunAll(n int, body func(index int) error) error {
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for t := 0; t < n; t++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			if err := body(index); err != nil {
				errs <- err
			}
		}(t)
	}
	finished := make(chan struct{})
	go func() { wg.Wait(); close(finished) }()
	select {
	case <-finished:
	case <-time.After(10 * time.Second):
		return errors.New("goroutines did not finish: deadlock?")
	}
	close(errs)
	for err := range errs {
		return fmt.Errorf("worker failure: %w", err)
	}
	return nil
}

func main() {
	fmt.Println("== A wait-for graph, by hand ==")
	g := NewWaitForGraph()
	g.AddEdge("T1", "T2") // T1 waits for a lock T2 holds
	g.AddEdge("T2", "T3")
	g.AddEdge("T3", "T1")
	g.AddEdge("T4", "T1") // T4 is stuck behind the cycle but not on it
	fmt.Printf("edges T1->T2, T2->T3, T3->T1, T4->T1: cycle = %v\n", g.FindCycle())
	chain := NewWaitForGraph()
	chain.AddEdge("T1", "T2")
	chain.AddEdge("T2", "T3")
	fmt.Printf("edges T1->T2, T2->T3: cycle = %v (a chain waits, it does not deadlock)\n", chain.FindCycle())

	fmt.Println("\n== The classic: A->B while B->A, replayed from recorded lock requests ==")
	scripted := NewLockTracker()
	scripted.Acquired("T1", "lock:A")
	scripted.Acquired("T2", "lock:B")
	_ = scripted.Requesting("T1", "lock:B") // T1 now waits for T2: no cycle yet
	fmt.Printf("after T1 requests lock:B: cycle = %v\n", scripted.Detect())
	if err := scripted.Requesting("T2", "lock:A"); err != nil {
		fmt.Println("T2 requests lock:A ->", err)
	}

	fmt.Println("\n== Live: two naive transfers, forced to interleave, caught by the detector ==")
	tracker := NewLockTracker()
	a := NewAccount("A", USD, 10_000, tracker)
	b := NewAccount("B", USD, 10_000, tracker)
	naive := NewTransferService(NewBarrier(2).Await, NewBackoff(42), 0)
	outcomes := make(chan string, 2)
	attempt := func(from, to *Account, amount Money, owner, label string) {
		if err := naive.NaiveTransfer(from, to, amount, owner); err != nil {
			outcomes <- fmt.Sprintf("%s aborted: %v", owner+" "+from.ID+"->"+to.ID, err)
		} else {
			outcomes <- label + " committed"
		}
	}
	go attempt(a, b, OfMinor(2_500, USD), "T1", "T1 A->B 25.00")
	go attempt(b, a, OfMinor(1_000, USD), "T2", "T2 B->A 10.00")
	results := []string{<-outcomes, <-outcomes}
	sort.Strings(results)
	for _, r := range results {
		fmt.Println("  " + r)
	}
	fmt.Println("  recorded events:")
	for _, e := range tracker.Events() {
		fmt.Println("    " + e)
	}
	fmt.Printf("  balances A=%v B=%v (sum %v)\n", a.Balance(), b.Balance(), OfMinor(a.BalanceMinor()+b.BalanceMinor(), USD))

	fmt.Println("\n== Fix 1: lock ordering, 8 goroutines x 500 transfers each way ==")
	c := NewAccount("C", USD, 100_000, nil)
	d := NewAccount("D", USD, 100_000, nil)
	ordered := NewTransferService(runtime.Gosched, NewBackoff(7), 0)
	if err := RunAll(8, func(t int) error {
		for i := 0; i < 500; i++ {
			var err error
			if t%2 == 0 {
				err = ordered.OrderedTransfer(c, d, OfMinor(1, USD), fmt.Sprintf("worker-%d", t))
			} else {
				err = ordered.OrderedTransfer(d, c, OfMinor(1, USD), fmt.Sprintf("worker-%d", t))
			}
			if err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		panic(err)
	}
	fmt.Printf("  completed; C=%v D=%v sum %v\n", c.Balance(), d.Balance(), OfMinor(c.BalanceMinor()+d.BalanceMinor(), USD))

	fmt.Println("\n== Fix 2: try-lock with backoff, same load ==")
	e := NewAccount("E", USD, 100_000, nil)
	f := NewAccount("F", USD, 100_000, nil)
	backoff := NewBackoff(7)
	trying := NewTransferService(runtime.Gosched, backoff, 0)
	if err := RunAll(8, func(t int) error {
		for i := 0; i < 500; i++ {
			var err error
			if t%2 == 0 {
				err = trying.TryLockTransfer(e, f, OfMinor(1, USD), fmt.Sprintf("worker-%d", t))
			} else {
				err = trying.TryLockTransfer(f, e, OfMinor(1, USD), fmt.Sprintf("worker-%d", t))
			}
			if err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		panic(err)
	}
	fmt.Printf("  completed; E=%v F=%v sum %v; backoff pauses: %d (varies)\n", e.Balance(), f.Balance(), OfMinor(e.BalanceMinor()+f.BalanceMinor(), USD), backoff.Pauses())
}
