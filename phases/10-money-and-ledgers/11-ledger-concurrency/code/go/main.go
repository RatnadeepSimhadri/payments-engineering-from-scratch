// Lesson: phases/10-money-and-ledgers/11-ledger-concurrency/docs/en.md
// Ledger Concurrency: Hot Accounts and Balance Checks
// Run:  go run .          (add --stats for contention counters and timings)
// Test: go test -race ./...
// Sources: Kung & Robinson, "On Optimistic Methods for Concurrency Control" (1981); the Go memory model (go.dev/ref/mem);
// sync, sync/atomic; Money from Lesson 01 (money.go). Go 1.22, standard library only.
package main

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ---------------------------------------------------------------------------------------------
// Part 1: a deterministic model checker. Each strategy is a tiny step machine; we explore EVERY
// interleaving of a few concurrent operations on one account and check the invariants at the end.
// ---------------------------------------------------------------------------------------------

type Strategy int

const (
	Naive Strategy = iota
	Versioned
	Locked
)

func (s Strategy) String() string { return [...]string{"NAIVE", "VERSIONED", "LOCKED"}[s] }

// Op is a deposit (Amount > 0, never rejected) or a payout (Amount < 0, rejected if it would overdraw).
type Op struct {
	Name   string
	Amount int64
}

// Exploration is the result of exhaustive exploration: how many complete schedules, and how many broke an invariant.
type Exploration struct {
	Strategy       Strategy
	Schedules      int
	Violations     int
	FirstViolation string // "" when every schedule held
}

const done = 99

// world is the mutable model state, copied at every branch.
type world struct {
	balance, version int64
	lockHolder       int
	pc               []int
	localBalance     []int64
	localVersion     []int64
	applied          []bool
	trace            strings.Builder
}

func (w *world) copy() *world {
	n := &world{balance: w.balance, version: w.version, lockHolder: w.lockHolder}
	n.pc = append([]int(nil), w.pc...)
	n.localBalance = append([]int64(nil), w.localBalance...)
	n.localVersion = append([]int64(nil), w.localVersion...)
	n.applied = append([]bool(nil), w.applied...)
	n.trace.WriteString(w.trace.String())
	return n
}

type exploration struct {
	schedules, violations int
	first                 string
}

func Explore(strategy Strategy, initialBalance int64, ops []Op) Exploration {
	n := len(ops)
	w := &world{balance: initialBalance, lockHolder: -1, pc: make([]int, n),
		localBalance: make([]int64, n), localVersion: make([]int64, n), applied: make([]bool, n)}
	var e exploration
	dfs(strategy, initialBalance, ops, w, &e, 0)
	return Exploration{strategy, e.schedules, e.violations, e.first}
}

func dfs(s Strategy, initial int64, ops []Op, w *world, e *exploration, depth int) {
	if depth > 200 {
		panic("exploration did not terminate")
	}
	anyEnabled := false
	for t := range ops {
		if w.pc[t] == done || !enabled(s, w, t) {
			continue
		}
		anyEnabled = true
		next := w.copy()
		step(s, next, t, ops[t])
		dfs(s, initial, ops, next, e, depth+1)
	}
	if !anyEnabled {
		e.schedules++
		if violation := check(initial, ops, w); violation != "" {
			e.violations++
			if e.first == "" {
				e.first = violation + " via " + strings.TrimSpace(w.trace.String())
			}
		}
	}
}

func enabled(s Strategy, w *world, t int) bool {
	return !(s == Locked && w.pc[t] == 0 && w.lockHolder != -1) // blocked waiting for the lock
}

// step is one atomic step of thread t. Each case is exactly one shared-memory access (or lock action).
func step(s Strategy, w *world, t int, op Op) {
	who := op.Name
	switch s {
	case Naive:
		if w.pc[t] == 0 {
			w.localBalance[t] = w.balance
			w.pc[t] = 1
			w.trace.WriteString(who + ":read ")
		} else {
			next := w.localBalance[t] + op.Amount
			if next >= 0 {
				w.balance = next
				w.applied[t] = true
				w.trace.WriteString(who + ":write ")
			} else {
				w.trace.WriteString(who + ":reject ")
			}
			w.pc[t] = done
		}
	case Versioned:
		switch {
		case w.pc[t] == 0:
			w.localBalance[t] = w.balance
			w.localVersion[t] = w.version
			w.pc[t] = 1
			w.trace.WriteString(who + ":read ")
		case w.version != w.localVersion[t]:
			w.pc[t] = 0 // stale read: retry from the top
			w.trace.WriteString(who + ":conflict ")
		default: // validate + write, one atomic step
			next := w.localBalance[t] + op.Amount
			if next >= 0 {
				w.balance = next
				w.version++
				w.applied[t] = true
				w.trace.WriteString(who + ":commit ")
			} else {
				w.trace.WriteString(who + ":reject ")
			}
			w.pc[t] = done
		}
	case Locked:
		switch w.pc[t] {
		case 0:
			w.lockHolder = t
			w.pc[t] = 1
			w.trace.WriteString(who + ":lock ")
		case 1:
			w.localBalance[t] = w.balance
			w.pc[t] = 2
			w.trace.WriteString(who + ":read ")
		case 2:
			next := w.localBalance[t] + op.Amount
			if next >= 0 {
				w.balance = next
				w.applied[t] = true
				w.trace.WriteString(who + ":write ")
			} else {
				w.trace.WriteString(who + ":reject ")
			}
			w.pc[t] = 3
		default:
			w.lockHolder = -1
			w.pc[t] = done
			w.trace.WriteString(who + ":unlock ")
		}
	}
}

// check tests the invariants at the end of a schedule: conservation (balance = initial + applied ops) and no overdraft.
func check(initial int64, ops []Op, w *world) string {
	expected := initial
	for t := range ops {
		if w.applied[t] {
			expected += ops[t].Amount
		}
	}
	if w.balance != expected {
		return fmt.Sprintf("lost update: balance %d but applied ops imply %d", w.balance, expected)
	}
	if w.balance < 0 {
		return fmt.Sprintf("overdraft: balance %d", w.balance)
	}
	return ""
}

// ---------------------------------------------------------------------------------------------
// Part 2: real goroutines. Three ledgers behind one interface, each a different answer to the hot account.
// ---------------------------------------------------------------------------------------------

// ErrInsufficientFunds is returned when a transfer would overdraw an account that does not allow it.
var ErrInsufficientFunds = errors.New("insufficient funds")

func insufficient(account string, balance, amount int64) error {
	return fmt.Errorf("%w: account %s has %d minor units, needs %d", ErrInsufficientFunds, account, balance, amount)
}

type ConcurrentLedger interface {
	Name() string
	// OpenAccount opens an account. overdraftAllowed=false means a transfer out of it must never drive it below zero.
	OpenAccount(id string, currency Currency, initialMinor int64, overdraftAllowed bool) error
	// Transfer moves amount from one account to another atomically, or returns an error without changing anything.
	Transfer(from, to string, amount Money) error
	// Balance is a consistent balance. For sharded accounts this sums the shards.
	Balance(id string) (Money, error)
	// TotalMinor is the sum of every account's balance: constant under transfers, the conservation check.
	TotalMinor() int64
	// Contention counts retries (optimistic) or lock waits (pessimistic): how much the hot account cost. Not deterministic.
	Contention() int64
}

// Cell is a mutable account cell for the lock-based ledgers. Guarded by its mutex.
type Cell struct {
	ID               string
	Currency         Currency
	OverdraftAllowed bool
	lock             sync.Mutex
	balance          int64
}

// PessimisticLedger: lock both accounts (in a global order), check, apply, unlock.
type PessimisticLedger struct {
	mu    sync.RWMutex // guards the cells map itself (Java: ConcurrentHashMap)
	cells map[string]*Cell
	waits atomic.Int64
}

func NewPessimisticLedger() *PessimisticLedger { return &PessimisticLedger{cells: map[string]*Cell{}} }

func (l *PessimisticLedger) Name() string { return "pessimistic" }

func (l *PessimisticLedger) OpenAccount(id string, currency Currency, initialMinor int64, overdraftAllowed bool) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, exists := l.cells[id]; exists {
		return fmt.Errorf("account exists: %s", id)
	}
	l.cells[id] = &Cell{ID: id, Currency: currency, OverdraftAllowed: overdraftAllowed, balance: initialMinor}
	return nil
}

func (l *PessimisticLedger) cell(id string) (*Cell, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	c, ok := l.cells[id]
	if !ok {
		return nil, fmt.Errorf("unknown account: %s", id)
	}
	return c, nil
}

func (l *PessimisticLedger) Transfer(from, to string, amount Money) error {
	a, err := l.cell(from)
	if err != nil {
		return err
	}
	b, err := l.cell(to)
	if err != nil {
		return err
	}
	if err := validate(a, b, amount); err != nil {
		return err
	}
	first, second := ordered(a, b) // global order: no deadlock
	l.lock(first)
	defer first.lock.Unlock()
	l.lock(second)
	defer second.lock.Unlock()
	m := amount.MinorUnits
	if !a.OverdraftAllowed && a.balance < m {
		return insufficient(a.ID, a.balance, m)
	}
	newA, err := addChecked(a.balance, -m)
	if err != nil {
		return err
	}
	newB, err := addChecked(b.balance, m)
	if err != nil {
		return err
	}
	a.balance, b.balance = newA, newB
	return nil
}

func ordered(a, b *Cell) (*Cell, *Cell) {
	if a.ID < b.ID {
		return a, b
	}
	return b, a
}

func (l *PessimisticLedger) lock(c *Cell) {
	if !c.lock.TryLock() {
		l.waits.Add(1)
		c.lock.Lock()
	}
}

// moveUpTo moves min(src.balance, max) from src to dst under both locks (global order). Returns what moved.
func (l *PessimisticLedger) moveUpTo(src, dst *Cell, max int64) (int64, error) {
	first, second := ordered(src, dst)
	l.lock(first)
	defer first.lock.Unlock()
	l.lock(second)
	defer second.lock.Unlock()
	take := min(max64(src.balance, 0), max)
	newDst, err := addChecked(dst.balance, take)
	if err != nil {
		return 0, err
	}
	src.balance -= take
	dst.balance = newDst
	return take, nil
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func validate(a, b *Cell, amount Money) error {
	if a == b {
		return errors.New("transfer to the same account")
	}
	if !amount.IsPositive() {
		return errors.New("amount must be positive")
	}
	if a.Currency != amount.Currency {
		return fmt.Errorf("%w: cannot combine %s with %s", ErrCurrencyMismatch, a.Currency.Code, amount.Currency.Code)
	}
	if b.Currency != amount.Currency {
		return fmt.Errorf("%w: cannot combine %s with %s", ErrCurrencyMismatch, b.Currency.Code, amount.Currency.Code)
	}
	return nil
}

func (l *PessimisticLedger) Balance(id string) (Money, error) {
	c, err := l.cell(id)
	if err != nil {
		return Money{}, err
	}
	c.lock.Lock()
	defer c.lock.Unlock()
	return OfMinor(c.balance, c.Currency), nil
}

func (l *PessimisticLedger) sortedCells() []*Cell {
	l.mu.RLock()
	all := make([]*Cell, 0, len(l.cells))
	for _, c := range l.cells {
		all = append(all, c)
	}
	l.mu.RUnlock()
	sort.Slice(all, func(i, j int) bool { return all[i].ID < all[j].ID })
	return all
}

// TotalMinor locks every account in order, so the total is a consistent cut, not a smear across in-flight transfers.
func (l *PessimisticLedger) TotalMinor() int64 {
	all := l.sortedCells()
	for _, c := range all {
		c.lock.Lock()
	}
	defer func() {
		for _, c := range all {
			c.lock.Unlock()
		}
	}()
	var sum int64
	for _, c := range all {
		sum += c.balance
	}
	return sum
}

func (l *PessimisticLedger) Contention() int64 { return l.waits.Load() }

// AccountState is immutable versioned state: the row an optimistic UPDATE ... WHERE version = ? would read.
type AccountState struct {
	Version int64
	Balance int64
}

type row struct {
	id               string
	currency         Currency
	overdraftAllowed bool
	state            atomic.Pointer[AccountState]
	commit           sync.Mutex
}

// OptimisticLedger: read both versions without locking, run the balance check, then commit only if neither version
// changed (a short validate-and-write critical section, as in Kung and Robinson's serial validation). On conflict, retry.
type OptimisticLedger struct {
	mu      sync.RWMutex
	rows    map[string]*row
	retries atomic.Int64
}

func NewOptimisticLedger() *OptimisticLedger { return &OptimisticLedger{rows: map[string]*row{}} }

func (l *OptimisticLedger) Name() string { return "optimistic" }

func (l *OptimisticLedger) OpenAccount(id string, currency Currency, initialMinor int64, overdraftAllowed bool) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, exists := l.rows[id]; exists {
		return fmt.Errorf("account exists: %s", id)
	}
	r := &row{id: id, currency: currency, overdraftAllowed: overdraftAllowed}
	r.state.Store(&AccountState{0, initialMinor})
	l.rows[id] = r
	return nil
}

func (l *OptimisticLedger) row(id string) (*row, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	r, ok := l.rows[id]
	if !ok {
		return nil, fmt.Errorf("unknown account: %s", id)
	}
	return r, nil
}

func (l *OptimisticLedger) Transfer(from, to string, amount Money) error {
	a, err := l.row(from)
	if err != nil {
		return err
	}
	b, err := l.row(to)
	if err != nil {
		return err
	}
	if a == b {
		return errors.New("transfer to the same account")
	}
	if !amount.IsPositive() {
		return errors.New("amount must be positive")
	}
	if a.currency != amount.Currency {
		return fmt.Errorf("%w: cannot combine %s with %s", ErrCurrencyMismatch, a.currency.Code, amount.Currency.Code)
	}
	if b.currency != amount.Currency {
		return fmt.Errorf("%w: cannot combine %s with %s", ErrCurrencyMismatch, b.currency.Code, amount.Currency.Code)
	}
	m := amount.MinorUnits
	first, second := a, b
	if b.id < a.id {
		first, second = b, a
	}
	for {
		sa, sb := a.state.Load(), b.state.Load()                   // read phase: no locks
		insufficientFunds := !a.overdraftAllowed && sa.Balance < m // business check on the read
		committed, err := func() (bool, error) {
			first.commit.Lock()
			defer first.commit.Unlock()
			second.commit.Lock()
			defer second.commit.Unlock()
			if a.state.Load() != sa || b.state.Load() != sb { // validate: nothing changed since the read
				return false, nil
			}
			if insufficientFunds {
				return false, insufficient(a.id, sa.Balance, m)
			}
			newA, err := addChecked(sa.Balance, -m)
			if err != nil {
				return false, err
			}
			newB, err := addChecked(sb.Balance, m)
			if err != nil {
				return false, err
			}
			a.state.Store(&AccountState{sa.Version + 1, newA})
			b.state.Store(&AccountState{sb.Version + 1, newB})
			return true, nil
		}()
		if err != nil || committed {
			return err
		}
		l.retries.Add(1) // stale read: go again
	}
}

func (l *OptimisticLedger) Balance(id string) (Money, error) {
	r, err := l.row(id)
	if err != nil {
		return Money{}, err
	}
	return OfMinor(r.state.Load().Balance, r.currency), nil
}

func (l *OptimisticLedger) Version(id string) (int64, error) {
	r, err := l.row(id)
	if err != nil {
		return 0, err
	}
	return r.state.Load().Version, nil
}

func (l *OptimisticLedger) TotalMinor() int64 {
	l.mu.RLock()
	all := make([]*row, 0, len(l.rows))
	for _, r := range l.rows {
		all = append(all, r)
	}
	l.mu.RUnlock()
	sort.Slice(all, func(i, j int) bool { return all[i].id < all[j].id })
	for _, r := range all {
		r.commit.Lock()
	}
	defer func() {
		for _, r := range all {
			r.commit.Unlock()
		}
	}()
	var sum int64
	for _, r := range all {
		sum += r.state.Load().Balance
	}
	return sum
}

func (l *OptimisticLedger) Contention() int64 { return l.retries.Load() }

// ShardedLedger: a hot account is K sub-accounts "id#0".."id#K-1". Credits pick one shard (by a caller-supplied
// routing key), so K writers proceed in parallel. Debits drain shards greedily, one shard lock at a time, and
// give everything back if the total is short, through an in-flight account. RollUp sweeps every shard into shard 0.
type ShardedLedger struct {
	*PessimisticLedger
	shardMu    sync.RWMutex
	shardCount map[string]int
	routing    atomic.Int64
}

func NewShardedLedger() *ShardedLedger {
	return &ShardedLedger{PessimisticLedger: NewPessimisticLedger(), shardCount: map[string]int{}}
}

func (l *ShardedLedger) Name() string { return "sharded" }

func (l *ShardedLedger) OpenHotAccount(id string, currency Currency, initialMinor int64, shards int) error {
	if shards < 1 {
		return errors.New("need at least one shard")
	}
	parts, err := OfMinor(initialMinor, currency).Allocate(evenRatios(shards)...)
	if err != nil {
		return err
	}
	for k := range shards {
		if err := l.PessimisticLedger.OpenAccount(ShardID(id, k), currency, parts[k].MinorUnits, false); err != nil {
			return err
		}
	}
	if err := l.PessimisticLedger.OpenAccount(id+"#inflight", currency, 0, false); err != nil {
		return err
	}
	l.shardMu.Lock()
	l.shardCount[id] = shards
	l.shardMu.Unlock()
	return nil
}

func ShardID(id string, k int) string { return fmt.Sprintf("%s#%d", id, k) }

func (l *ShardedLedger) IsHot(id string) bool {
	l.shardMu.RLock()
	defer l.shardMu.RUnlock()
	_, hot := l.shardCount[id]
	return hot
}

func (l *ShardedLedger) Shards(id string) int {
	l.shardMu.RLock()
	defer l.shardMu.RUnlock()
	if n, hot := l.shardCount[id]; hot {
		return n
	}
	return 1
}

// floorMod is Java's Math.floorMod for a non-negative modulus: the result has the sign of the divisor.
func floorMod(x, m int64) int64 { return ((x % m) + m) % m }

func (l *ShardedLedger) Transfer(from, to string, amount Money) error {
	switch {
	case l.IsHot(from) && l.IsHot(to):
		return errors.New("hot-to-hot transfers are not supported")
	case l.IsHot(to):
		k := int(floorMod(l.routing.Add(1)-1, int64(l.Shards(to)))) // spread credits across shards
		return l.PessimisticLedger.Transfer(from, ShardID(to, k), amount)
	case l.IsHot(from):
		return l.debitHot(from, to, amount)
	default:
		return l.PessimisticLedger.Transfer(from, to, amount)
	}
}

// debitHot is a greedy drain across shards into an in-flight account, then one move to the destination. Every step
// is a two-account locked move, so a consistent cut (TotalMinor) sees the money in exactly one place at all times.
// If the shards are short, every unit is moved back and the payout is rejected.
func (l *ShardedLedger) debitHot(hot, to string, amount Money) error {
	inflight, err := l.cell(hot + "#inflight")
	if err != nil {
		return err
	}
	dest, err := l.cell(to)
	if err != nil {
		return err
	}
	if err := validate(inflight, dest, amount); err != nil {
		return err
	}
	remaining := amount.MinorUnits
	n := l.Shards(hot)
	taken := make([]int64, n)
	start := int(floorMod(l.routing.Add(1)-1, int64(n)))
	shards := make([]*Cell, n)
	for k := range n {
		if shards[k], err = l.cell(ShardID(hot, k)); err != nil {
			return err
		}
	}
	for i := 0; i < n && remaining > 0; i++ {
		k := (start + i) % n
		got, err := l.moveUpTo(shards[k], inflight, remaining)
		if err != nil {
			return err
		}
		taken[k] += got
		remaining -= got
	}
	if remaining > 0 {
		for k := range n {
			if taken[k] > 0 {
				if _, err := l.moveUpTo(inflight, shards[k], taken[k]); err != nil {
					return err
				}
			}
		}
		return insufficient(hot, amount.MinorUnits-remaining, amount.MinorUnits)
	}
	_, err = l.moveUpTo(inflight, dest, amount.MinorUnits)
	return err
}

// Balance is the sum of the shards, read under all shard locks: a consistent balance for one hot account.
func (l *ShardedLedger) Balance(id string) (Money, error) {
	if !l.IsHot(id) {
		return l.PessimisticLedger.Balance(id)
	}
	n := l.Shards(id)
	cs := make([]*Cell, n)
	for k := range n {
		var err error
		if cs[k], err = l.cell(ShardID(id, k)); err != nil {
			return Money{}, err
		}
	}
	for _, c := range cs {
		c.lock.Lock()
	}
	defer func() {
		for _, c := range cs {
			c.lock.Unlock()
		}
	}()
	var sum int64
	for _, c := range cs {
		sum += c.balance
	}
	return OfMinor(sum, cs[0].Currency), nil
}

// RollUp sweeps shards 1..K-1 into shard 0 (in a real ledger, one sweep entry per shard). Total is unchanged.
func (l *ShardedLedger) RollUp(id string) (int64, error) {
	var moved int64
	for k := 1; k < l.Shards(id); k++ {
		shard, err := l.cell(ShardID(id, k))
		if err != nil {
			return moved, err
		}
		l.lock(shard)
		amount := shard.balance
		shard.lock.Unlock()
		if amount > 0 {
			if err := l.PessimisticLedger.Transfer(ShardID(id, k), ShardID(id, 0), OfMinor(amount, shard.Currency)); err != nil {
				return moved, err
			}
			moved += amount
		}
	}
	return moved, nil
}

func (l *ShardedLedger) ShardBalance(id string, k int) (int64, error) {
	m, err := l.PessimisticLedger.Balance(ShardID(id, k))
	return m.MinorUnits, err
}

func evenRatios(n int) []int64 {
	r := make([]int64, n)
	for i := range r {
		r[i] = 1
	}
	return r
}

// Workload: many customers paying one merchant (the hot account), plus payouts out of it to the bank.
type Workload struct {
	Threads       int
	OpsPerThread  int
	Customers     int
	CustomerFloat int64
	MerchantFloat int64
	PayoutEvery   int
	Seed          int64
}

func CustomerID(i int) string { return fmt.Sprintf("c%03d", i) }

func (w Workload) Open(ledger ConcurrentLedger, merchantShards int) error {
	for i := range w.Customers {
		if err := ledger.OpenAccount(CustomerID(i), USD, w.CustomerFloat, false); err != nil {
			return err
		}
	}
	if err := ledger.OpenAccount("bank", USD, 0, true); err != nil {
		return err
	}
	if s, ok := ledger.(*ShardedLedger); ok && merchantShards > 1 {
		return s.OpenHotAccount("merchant", USD, w.MerchantFloat, merchantShards)
	}
	return ledger.OpenAccount("merchant", USD, w.MerchantFloat, false)
}

// ScriptOp is one scripted transfer: Customer < 0 means a payout from the merchant to the bank.
type ScriptOp struct {
	Customer int
	Amount   int64
}

// Script is the deterministic per-thread script: what each goroutine will do, independent of how they interleave.
func (w Workload) Script(thread int) []ScriptOp {
	r := NewJavaRandom(w.Seed*31 + int64(thread))
	ops := make([]ScriptOp, 0, w.OpsPerThread)
	for i := range w.OpsPerThread {
		payout := i%w.PayoutEvery == w.PayoutEvery-1
		customer := -1
		if !payout {
			customer = int(r.NextInt(int32(w.Customers)))
		}
		ops = append(ops, ScriptOp{customer, int64(1 + r.NextInt(10_000))})
	}
	return ops
}

// Expected final balances: computable from the scripts alone because every op is guaranteed to succeed.
func (w Workload) Expected() map[string]int64 {
	m := map[string]int64{}
	for i := range w.Customers {
		m[CustomerID(i)] = w.CustomerFloat
	}
	m["merchant"] = w.MerchantFloat
	m["bank"] = 0
	for t := range w.Threads {
		for _, op := range w.Script(t) {
			if op.Customer < 0 {
				m["merchant"] -= op.Amount
				m["bank"] += op.Amount
			} else {
				m[CustomerID(op.Customer)] -= op.Amount
				m["merchant"] += op.Amount
			}
		}
	}
	return m
}

func (w Workload) PayoutTotal() int64 {
	var sum int64
	for t := range w.Threads {
		for _, op := range w.Script(t) {
			if op.Customer < 0 {
				sum += op.Amount
			}
		}
	}
	return sum
}

// Run every goroutine's script concurrently, released together by closing a channel. Returns failures seen (should be 0).
func (w Workload) Run(ledger ConcurrentLedger) int64 {
	start := make(chan struct{})
	var failures atomic.Int64
	var wg sync.WaitGroup
	for t := range w.Threads {
		ops := w.Script(t)
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for _, op := range ops {
				m := OfMinor(op.Amount, USD)
				var err error
				if op.Customer < 0 {
					err = ledger.Transfer("merchant", "bank", m)
				} else {
					err = ledger.Transfer(CustomerID(op.Customer), "merchant", m)
				}
				if err != nil {
					failures.Add(1)
				}
			}
		}()
	}
	close(start)
	wg.Wait()
	return failures.Load()
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func mustDo(err error) {
	if err != nil {
		panic(err)
	}
}

func main() {
	stats := len(os.Args) > 1 && os.Args[1] == "--stats"

	fmt.Println("== Exhaustive interleavings: balance 100.00, payouts of 60.00 and 60.00, deposit of 30.00 ==")
	ops := []Op{{"P1", -6_000}, {"P2", -6_000}, {"D", 3_000}}
	for _, s := range []Strategy{Naive, Versioned, Locked} {
		e := Explore(s, 10_000, ops)
		fmt.Printf("%-9v schedules %4d  violations %4d\n", s, e.Schedules, e.Violations)
		if e.FirstViolation != "" {
			fmt.Println("          e.g. " + e.FirstViolation)
		}
	}

	fmt.Println("\n== 8 threads x 20,000 ops: customers pay one hot merchant account; every 20th op pays out ==")
	w := Workload{8, 20_000, 200, 50_000_000, 2_000_000_000, 20, 42}
	expected := w.Expected()
	initialTotal := int64(200*50_000_000 + 2_000_000_000)
	fmt.Printf("payouts requested %v against a merchant float of %v (every op can succeed in any order)\n",
		OfMinor(w.PayoutTotal(), USD), OfMinor(2_000_000_000, USD))
	for _, ledger := range []ConcurrentLedger{NewPessimisticLedger(), NewOptimisticLedger(), NewShardedLedger()} {
		mustDo(w.Open(ledger, 8))
		t0 := time.Now()
		failures := w.Run(ledger)
		ms := time.Since(t0).Milliseconds()
		if s, ok := ledger.(*ShardedLedger); ok {
			must(s.RollUp("merchant"))
		}
		match := true
		for id, want := range expected {
			match = match && must(ledger.Balance(id)).MinorUnits == want
		}
		fmt.Printf("%-12s failures %d  total conserved %-5v  every balance as expected %-5v  merchant %v\n",
			ledger.Name(), failures, ledger.TotalMinor() == initialTotal, match, must(ledger.Balance("merchant")))
		if stats {
			fmt.Printf("             contention %d  elapsed %d ms\n", ledger.Contention(), ms)
		}
	}

	fmt.Println("\n== A payout that would overdraw is rejected atomically, in every strategy ==")
	for _, ledger := range []ConcurrentLedger{NewPessimisticLedger(), NewOptimisticLedger(), NewShardedLedger()} {
		mustDo(ledger.OpenAccount("bank", USD, 0, true))
		if s, ok := ledger.(*ShardedLedger); ok {
			mustDo(s.OpenHotAccount("merchant", USD, 10_000, 4))
		} else {
			mustDo(ledger.OpenAccount("merchant", USD, 10_000, false))
		}
		if err := ledger.Transfer("merchant", "bank", must(Parse("100.01", USD))); errors.Is(err, ErrInsufficientFunds) {
			fmt.Printf("%s: rejected; merchant still %v, bank %v\n", ledger.Name(), must(ledger.Balance("merchant")), must(ledger.Balance("bank")))
		}
	}
}
