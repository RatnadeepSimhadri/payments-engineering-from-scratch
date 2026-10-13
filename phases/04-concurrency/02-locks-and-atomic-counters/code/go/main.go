// Lesson: phases/04-concurrency/02-locks-and-atomic-counters/docs/en.md
// Locks, Atomics, and a Balance That Never Lies
// Run:  go run .
// Test: go test -race ./...
// Sources: The Go Memory Model (go.dev/ref/mem); sync and sync/atomic package docs;
// Herlihy & Shavit, The Art of Multiprocessor Programming, ch. 5.
// Go 1.22+, standard library only. Same design as the Java and Python versions.
package main

import (
	"errors"
	"fmt"
	"math"
	"sync"
	"sync/atomic"
	"time"
)

// ---------------------------------------------------------------------------
// Money (copied from Phase 10, Lesson 01)
// ---------------------------------------------------------------------------

type Currency struct {
	Code     string
	Exponent int
}

var (
	USD = Currency{"USD", 2}
	EUR = Currency{"EUR", 2}
)

func (c Currency) MinorUnitsPerMajor() int64 {
	scale := int64(1)
	for i := 0; i < c.Exponent; i++ {
		scale *= 10
	}
	return scale
}

var (
	ErrCurrencyMismatch  = errors.New("currency mismatch")
	ErrInsufficientFunds = errors.New("insufficient funds")
	ErrNegativeAmount    = errors.New("amount must not be negative")
	ErrNoSuchAccount     = errors.New("no such account")
)

// Money is an exact amount in minor units of one currency; a value type, so safe to share.
type Money struct {
	MinorUnits int64
	Currency   Currency
}

func OfMinor(minorUnits int64, currency Currency) Money { return Money{minorUnits, currency} }

func (m Money) Plus(other Money) (Money, error) {
	if m.Currency != other.Currency {
		return Money{}, fmt.Errorf("%w: cannot combine %s with %s", ErrCurrencyMismatch, m.Currency.Code, other.Currency.Code)
	}
	return Money{m.MinorUnits + other.MinorUnits, m.Currency}, nil
}

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

// ---------------------------------------------------------------------------
// Counters: one contract, three implementations plus the bug
// ---------------------------------------------------------------------------

// BalanceCounter is a balance in minor units that many goroutines add to.
type BalanceCounter interface {
	Add(delta int64)
	Get() int64
	Name() string
}

// RacyCounter is the bug. `balance += delta` is a load, an add, and a store; another goroutine
// can run between them. The hook runs between the load and the store so a test can force the
// interleaving deterministically. Never run it from two goroutines under -race: the detector
// will (correctly) fail the test.
type RacyCounter struct {
	balance             int64
	betweenLoadAndStore func()
}

func NewRacyCounter(betweenLoadAndStore func()) *RacyCounter {
	if betweenLoadAndStore == nil {
		betweenLoadAndStore = func() {}
	}
	return &RacyCounter{betweenLoadAndStore: betweenLoadAndStore}
}

func (c *RacyCounter) Add(delta int64) {
	loaded := c.balance        // 1. load
	c.betweenLoadAndStore()    //    (another goroutine may run here)
	c.balance = loaded + delta // 2. store a value computed from a possibly stale load
}

func (c *RacyCounter) Get() int64   { return c.balance }
func (c *RacyCounter) Name() string { return "racy" }

// LockedCounter is fix 1: mutual exclusion. Unlock happens-before the next Lock (Go memory
// model), so every write is visible to the next goroutine that locks.
type LockedCounter struct {
	mu      sync.Mutex
	balance int64
}

func (c *LockedCounter) Add(delta int64) {
	c.mu.Lock()
	c.balance += delta
	c.mu.Unlock()
}

func (c *LockedCounter) Get() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.balance
}

func (c *LockedCounter) Name() string { return "locked" }

// AtomicCounter is fix 2: compare-and-swap. Load, compute, publish only if nobody published
// since the load; otherwise retry. No goroutine ever blocks.
type AtomicCounter struct {
	value   atomic.Int64
	retries atomic.Int64
}

func (c *AtomicCounter) Add(delta int64) {
	for {
		current := c.value.Load()
		if c.value.CompareAndSwap(current, current+delta) {
			return
		}
		c.retries.Add(1)
	}
}

func (c *AtomicCounter) Get() int64        { return c.value.Load() }
func (c *AtomicCounter) Name() string      { return "atomic" }
func (c *AtomicCounter) CasRetries() int64 { return c.retries.Load() }

// StableHash is the classic 31-multiplier string hash on 32-bit wrap-around; identical in
// Java, Python, and Go, so an account lands on the same stripe in every implementation.
func StableHash(s string) int32 {
	var h int32
	for i := 0; i < len(s); i++ {
		h = 31*h + int32(s[i])
	}
	return h
}

// StripedAccountMap is fix 3: lock striping. Many accounts, a fixed number of locks; an
// account's stripe is a hash of its id. A debit is a check-then-act under the stripe lock.
type StripedAccountMap struct {
	currency Currency
	locks    []sync.Mutex
	buckets  []map[string]int64
}

func NewStripedAccountMap(currency Currency, stripes int) (*StripedAccountMap, error) {
	if stripes <= 0 {
		return nil, fmt.Errorf("stripes must be positive: %d", stripes)
	}
	m := &StripedAccountMap{currency: currency, locks: make([]sync.Mutex, stripes), buckets: make([]map[string]int64, stripes)}
	for i := range m.buckets {
		m.buckets[i] = map[string]int64{}
	}
	return m, nil
}

func (m *StripedAccountMap) Stripes() int { return len(m.locks) }

// StripeIndex is stable, non-negative, and in [0, stripes).
func (m *StripedAccountMap) StripeIndex(accountID string) int {
	h := int(StableHash(accountID)) % len(m.locks)
	if h < 0 {
		h += len(m.locks)
	}
	return h
}

func (m *StripedAccountMap) Open(accountID string) {
	s := m.StripeIndex(accountID)
	m.locks[s].Lock()
	defer m.locks[s].Unlock()
	if _, ok := m.buckets[s][accountID]; !ok {
		m.buckets[s][accountID] = 0
	}
}

func (m *StripedAccountMap) Credit(accountID string, amount Money) error {
	if err := m.requireValid(amount); err != nil {
		return err
	}
	s := m.StripeIndex(accountID)
	m.locks[s].Lock()
	defer m.locks[s].Unlock()
	current, ok := m.buckets[s][accountID]
	if !ok {
		return fmt.Errorf("%w: %s", ErrNoSuchAccount, accountID)
	}
	m.buckets[s][accountID] = current + amount.MinorUnits
	return nil
}

// Debit is check-then-act under the stripe lock: the balance cannot change between the check and the write.
func (m *StripedAccountMap) Debit(accountID string, amount Money) error {
	if err := m.requireValid(amount); err != nil {
		return err
	}
	s := m.StripeIndex(accountID)
	m.locks[s].Lock()
	defer m.locks[s].Unlock()
	current, ok := m.buckets[s][accountID]
	if !ok {
		return fmt.Errorf("%w: %s", ErrNoSuchAccount, accountID)
	}
	if current < amount.MinorUnits {
		return fmt.Errorf("%w: account %s has %d minor units, debit of %d refused", ErrInsufficientFunds, accountID, current, amount.MinorUnits)
	}
	m.buckets[s][accountID] = current - amount.MinorUnits
	return nil
}

func (m *StripedAccountMap) Balance(accountID string) (Money, error) {
	s := m.StripeIndex(accountID)
	m.locks[s].Lock()
	defer m.locks[s].Unlock()
	current, ok := m.buckets[s][accountID]
	if !ok {
		return Money{}, fmt.Errorf("%w: %s", ErrNoSuchAccount, accountID)
	}
	return OfMinor(current, m.currency), nil
}

// Total takes every stripe lock in index order so the sum is a consistent snapshot and two
// concurrent Total calls cannot deadlock each other (Lesson 07 is about why the order matters).
func (m *StripedAccountMap) Total() Money {
	for i := range m.locks {
		m.locks[i].Lock()
	}
	defer func() {
		for i := len(m.locks) - 1; i >= 0; i-- {
			m.locks[i].Unlock()
		}
	}()
	var sum int64
	for _, bucket := range m.buckets {
		for _, v := range bucket {
			sum += v
		}
	}
	return OfMinor(sum, m.currency)
}

func (m *StripedAccountMap) TotalMinor() int64 { return m.Total().MinorUnits }

func (m *StripedAccountMap) requireValid(amount Money) error {
	if amount.Currency != m.currency {
		return fmt.Errorf("%w: cannot combine %s with %s", ErrCurrencyMismatch, m.currency.Code, amount.Currency.Code)
	}
	if amount.IsNegative() {
		return fmt.Errorf("%w: %v", ErrNegativeAmount, amount)
	}
	return nil
}

// Account is an account whose balance lives in a counter. Money in, Money out; minor units inside.
type Account struct {
	ID       string
	Currency Currency
	counter  BalanceCounter
}

func NewAccount(id string, currency Currency, counter BalanceCounter) *Account {
	return &Account{ID: id, Currency: currency, counter: counter}
}

func (a *Account) Credit(amount Money) error {
	if err := a.requireValid(amount); err != nil {
		return err
	}
	a.counter.Add(amount.MinorUnits)
	return nil
}

func (a *Account) Debit(amount Money) error {
	if err := a.requireValid(amount); err != nil {
		return err
	}
	a.counter.Add(-amount.MinorUnits)
	return nil
}

func (a *Account) Balance() Money { return OfMinor(a.counter.Get(), a.Currency) }

func (a *Account) requireValid(amount Money) error {
	if amount.Currency != a.Currency {
		return fmt.Errorf("%w: cannot combine %s with %s", ErrCurrencyMismatch, a.Currency.Code, amount.Currency.Code)
	}
	if amount.IsNegative() {
		return fmt.Errorf("%w: %v", ErrNegativeAmount, amount)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Benchmark harness with an injected stopwatch
// ---------------------------------------------------------------------------

// Stopwatch is an injected time source so throughput math is testable without a real clock.
type Stopwatch interface{ Nanos() int64 }

type SystemStopwatch struct{ start time.Time }

func NewSystemStopwatch() *SystemStopwatch { return &SystemStopwatch{start: time.Now()} }
func (s *SystemStopwatch) Nanos() int64    { return int64(time.Since(s.start)) }

// FakeStopwatch returns scripted readings in order and repeats the last one.
type FakeStopwatch struct {
	readings []int64
	next     int
}

func NewFakeStopwatch(readings ...int64) *FakeStopwatch {
	if len(readings) == 0 {
		panic("need at least one reading")
	}
	return &FakeStopwatch{readings: readings}
}

func (f *FakeStopwatch) Nanos() int64 {
	i := f.next
	if i >= len(f.readings) {
		i = len(f.readings) - 1
	}
	f.next++
	return f.readings[i]
}

type BenchmarkResult struct {
	Name         string
	Threads      int
	OpsPerThread int
	Delta        int64
	Observed     int64
	ElapsedNanos int64
}

func (r BenchmarkResult) Expected() int64 { return int64(r.Threads) * int64(r.OpsPerThread) * r.Delta }
func (r BenchmarkResult) Lost() int64     { return r.Expected() - r.Observed }
func (r BenchmarkResult) Exact() bool     { return r.Lost() == 0 }

func (r BenchmarkResult) OpsPerSecond() float64 {
	ops := float64(r.Threads) * float64(r.OpsPerThread)
	if r.ElapsedNanos <= 0 {
		return math.Inf(1)
	}
	return ops * 1e9 / float64(r.ElapsedNanos)
}

// Worker is one unit of work for a benchmark goroutine: which goroutine, which iteration.
type Worker func(threadIndex, iteration int)

// RunBenchmark starts N goroutines, releases them together, waits for all, then reads the total.
func RunBenchmark(name string, threads, opsPerThread int, delta int64, worker Worker, observe func() int64, stopwatch Stopwatch) (BenchmarkResult, error) {
	if threads <= 0 || opsPerThread < 0 {
		return BenchmarkResult{}, errors.New("threads > 0, opsPerThread >= 0")
	}
	start := make(chan struct{})
	var done sync.WaitGroup
	done.Add(threads)
	for t := 0; t < threads; t++ {
		go func(threadIndex int) {
			defer done.Done()
			<-start
			for i := 0; i < opsPerThread; i++ {
				worker(threadIndex, i)
			}
		}(t)
	}
	before := stopwatch.Nanos()
	close(start) // all goroutines begin together: maximum contention
	done.Wait()  // Done() happens-before Wait() returning, so observe() sees every write
	after := stopwatch.Nanos()
	return BenchmarkResult{name, threads, opsPerThread, delta, observe(), after - before}, nil
}

// Measure has every goroutine add delta to the same counter opsPerThread times.
func Measure(counter BalanceCounter, threads, opsPerThread int, delta int64, stopwatch Stopwatch) (BenchmarkResult, error) {
	return RunBenchmark(counter.Name(), threads, opsPerThread, delta,
		func(t, i int) { counter.Add(delta) }, counter.Get, stopwatch)
}

// MeasureMap has goroutine t credit ids[t % len(ids)]; observed is the map total.
func MeasureMap(m *StripedAccountMap, ids []string, threads, opsPerThread int, delta int64, stopwatch Stopwatch) (BenchmarkResult, error) {
	amount := OfMinor(delta, m.currency)
	return RunBenchmark(fmt.Sprintf("striped-map(%d)", m.Stripes()), threads, opsPerThread, delta,
		func(t, i int) {
			if err := m.Credit(ids[t%len(ids)], amount); err != nil {
				panic(err)
			}
		}, m.TotalMinor, stopwatch)
}

func line(r BenchmarkResult, c Currency, note string) string {
	return fmt.Sprintf("%-16s observed %14s  lost %12s  %14s ops/s%s",
		r.Name, OfMinor(r.Observed, c), OfMinor(r.Lost(), c), withCommas(int64(r.OpsPerSecond())), note)
}

func withCommas(n int64) string {
	s := fmt.Sprintf("%d", n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

func must(r BenchmarkResult, err error) BenchmarkResult {
	if err != nil {
		panic(err)
	}
	return r
}

func main() {
	fmt.Println("== A lost update, forced deterministically ==")
	var racy *RacyCounter
	fired := false
	racy = NewRacyCounter(func() {
		if !fired { // the first Add pauses between load and store...
			fired = true
			racy.Add(1) // ...while a second Add runs to completion
		}
	})
	racy.Add(1)
	fmt.Printf("two credits of 0.01 USD, balance = %v (expected 0.02 USD): the second write overwrote the first\n", OfMinor(racy.Get(), USD))

	const threads, ops = 4, 250_000
	fmt.Printf("\n== %d goroutines each credit 0.01 USD %s times: expected %v ==\n", threads, withCommas(ops), OfMinor(threads*ops, USD))
	clock := NewSystemStopwatch()
	fmt.Println(line(must(Measure(NewRacyCounter(nil), threads, ops, 1, clock)), USD, "   <- varies run to run; any loss is the proof (and -race would flag it)"))
	fmt.Println(line(must(Measure(&LockedCounter{}, threads, ops, 1, clock)), USD, ""))
	atomicCounter := &AtomicCounter{}
	fmt.Println(line(must(Measure(atomicCounter, threads, ops, 1, clock)), USD, fmt.Sprintf("   CAS retries: %s (varies)", withCommas(atomicCounter.CasRetries()))))
	ids := []string{"acct-1", "acct-2", "acct-3", "acct-4", "acct-5", "acct-6", "acct-7", "acct-8"}
	accountMap, _ := NewStripedAccountMap(USD, 16)
	for _, id := range ids {
		accountMap.Open(id)
	}
	fmt.Println(line(must(MeasureMap(accountMap, ids, threads, ops, 1, clock)), USD, ""))
	fmt.Println("(ops/s are wall-clock on this machine and vary; the lost column is the lesson)")

	fmt.Println("\n== Check-then-act: a debit that cannot overdraw ==")
	_ = accountMap.Credit("acct-1", OfMinor(500, USD))
	bal, _ := accountMap.Balance("acct-1")
	fmt.Printf("acct-1 balance %v on stripe %d\n", bal, accountMap.StripeIndex("acct-1"))
	if err := accountMap.Debit("acct-1", OfMinor(1_000_000, USD)); err != nil {
		fmt.Println("rejected:", err)
	}
	fmt.Printf("map total = %v\n", accountMap.Total())

	fmt.Println("\n== The same counter behind an Account ==")
	acct := NewAccount("acct-7", USD, &LockedCounter{})
	_ = acct.Credit(OfMinor(10_000, USD))
	_ = acct.Credit(OfMinor(2_500, USD))
	_ = acct.Credit(OfMinor(2_500, USD))
	_ = acct.Debit(OfMinor(1_999, USD))
	fmt.Printf("%s balance after 100.00 + 25.00 + 25.00 - 19.99 = %v\n", acct.ID, acct.Balance())
	if err := acct.Credit(OfMinor(100, EUR)); err != nil {
		fmt.Println("rejected:", err)
	}

	fmt.Println("\n== Deterministic replay with an injected stopwatch ==")
	replay := must(Measure(&LockedCounter{}, 4, 1_000, 1, NewFakeStopwatch(1_000_000, 3_000_000)))
	fmt.Printf("%d goroutines x %s ops in %.3f ms = %s ops/s, lost %d\n",
		replay.Threads, withCommas(int64(replay.OpsPerThread)), float64(replay.ElapsedNanos)/1e6, withCommas(int64(replay.OpsPerSecond())), replay.Lost())
}
