// Lesson: phases/09-reliability-and-scale/04-cache-invalidation-and-stampedes/docs/en.md
// Cache Invalidation, Stampedes, and Request Coalescing
// Run:  go run .
// Test: go test ./...
// Sources: Vattani, Chierichetti, Lowenstein, "Optimal Probabilistic Cache Stampede Prevention" (VLDB 2015);
//
//	RFC 5861 (stale-while-revalidate); Nishtala et al., "Scaling Memcache at Facebook" (NSDI 2013). Go 1.22+, stdlib only.
package main

import (
	"container/heap"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// Currency is an ISO 4217 currency (Phase 10, Lesson 01).
type Currency struct {
	Code     string
	Exponent int
}

var USD = Currency{"USD", 2}

// NewCurrency validates the code and exponent; USD is known-good.
func NewCurrency(code string, exponent int) (Currency, error) {
	if len(code) != 3 || strings.ToUpper(code) != code || strings.Trim(code, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") != "" {
		return Currency{}, fmt.Errorf("currency code must be three uppercase letters: %q", code)
	}
	if exponent < 0 || exponent > 4 {
		return Currency{}, fmt.Errorf("exponent must be 0..4: %d", exponent)
	}
	return Currency{code, exponent}, nil
}

func (c Currency) MinorUnitsPerMajor() int64 {
	scale := int64(1)
	for i := 0; i < c.Exponent; i++ {
		scale *= 10
	}
	return scale
}

var (
	ErrCurrencyMismatch = errors.New("currency mismatch")
	ErrOverflow         = errors.New("int64 overflow")
)

// Money is an exact amount of one currency as a signed count of minor units (Phase 10, Lesson 01).
type Money struct {
	MinorUnits int64
	Currency   Currency
}

func OfMinor(minorUnits int64, currency Currency) Money { return Money{minorUnits, currency} }

func addChecked(a, b int64) (int64, error) {
	sum := a + b
	if (a > 0 && b > 0 && sum < 0) || (a < 0 && b < 0 && sum >= 0) {
		return 0, ErrOverflow
	}
	return sum, nil
}

func (m Money) Minus(other Money) (Money, error) {
	if m.Currency != other.Currency {
		return Money{}, fmt.Errorf("%w: cannot combine %s with %s", ErrCurrencyMismatch, m.Currency.Code, other.Currency.Code)
	}
	diff, err := addChecked(m.MinorUnits, -other.MinorUnits)
	return Money{diff, m.Currency}, err
}

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

func must(m Money, err error) Money {
	if err != nil {
		panic(err)
	}
	return m
}

// event is one scheduled action; seq keeps same-instant events in scheduling order.
type event struct {
	atMs   int64
	seq    int64
	action func()
}

type eventQueue []event

func (q eventQueue) Len() int { return len(q) }
func (q eventQueue) Less(i, j int) bool {
	if q[i].atMs != q[j].atMs {
		return q[i].atMs < q[j].atMs
	}
	return q[i].seq < q[j].seq
}
func (q eventQueue) Swap(i, j int) { q[i], q[j] = q[j], q[i] }
func (q *eventQueue) Push(x any)   { *q = append(*q, x.(event)) }
func (q *eventQueue) Pop() any     { old := *q; e := old[len(old)-1]; *q = old[:len(old)-1]; return e }

// Simulator is a discrete-event simulator. Time only moves when the next event is popped, so a run that
// represents ten seconds of traffic completes in milliseconds and is identical on every run.
type Simulator struct {
	queue eventQueue
	nowMs int64
	seq   int64
}

func NewSimulator() *Simulator { return &Simulator{} }

func (s *Simulator) Now() int64 { return s.nowMs }

// Schedule queues an action at an absolute time; it panics on time travel because that is a bug in the caller.
func (s *Simulator) Schedule(atMs int64, action func()) {
	if atMs < s.nowMs {
		panic(fmt.Sprintf("cannot schedule in the past: %d < %d", atMs, s.nowMs))
	}
	heap.Push(&s.queue, event{atMs, s.seq, action})
	s.seq++
}

func (s *Simulator) After(delayMs int64, action func()) { s.Schedule(s.nowMs+delayMs, action) }

// Run executes every event in time order until the queue is empty.
func (s *Simulator) Run() {
	for s.queue.Len() > 0 {
		e := heap.Pop(&s.queue).(event)
		s.nowMs = e.atMs
		e.action()
	}
}

// Callback receives the loaded value.
type Callback func(value Money)

// BackingStore is the database behind the cache. A load takes latencyMs of simulated time; the store counts
// how many are in flight.
type BackingStore struct {
	sim                           *Simulator
	latencyMs                     int64
	rows                          map[string]Money
	Loads, InFlight, PeakInFlight int64
}

func NewBackingStore(sim *Simulator, latencyMs int64) *BackingStore {
	return &BackingStore{sim: sim, latencyMs: latencyMs, rows: map[string]Money{}}
}

func (s *BackingStore) Write(key string, value Money) { s.rows[key] = value }

func (s *BackingStore) Peek(key string) (Money, bool) {
	m, ok := s.rows[key]
	return m, ok
}

func (s *BackingStore) LatencyMs() int64 { return s.latencyMs }

// Load starts a load. The value is the row as it stands when the load completes, delivered via the callback.
func (s *BackingStore) Load(key string, cb Callback) {
	s.Loads++
	s.InFlight++
	s.PeakInFlight = max(s.PeakInFlight, s.InFlight)
	s.sim.After(s.latencyMs, func() {
		s.InFlight--
		cb(s.rows[key])
	})
}

// Entry is a cached value with two horizons: fresh until, and (for stale-while-revalidate) usable until.
type Entry struct {
	Value        Money
	FreshUntilMs int64
	StaleUntilMs int64
	LoadTookMs   int64
}

func (e Entry) IsFresh(now int64) bool  { return now < e.FreshUntilMs }
func (e Entry) IsUsable(now int64) bool { return now < e.StaleUntilMs }

// ClientStats is what every client records: when it asked, when it got an answer, and whether it matched the store.
type ClientStats struct {
	Latencies                                                                []int64
	Hits, Misses, StaleServes, LockRetries, EarlyRefreshes, EarlyLeadTotalMs int64
}

// AverageLeadMs is the average time before expiry at which an early refresh was triggered.
func (c *ClientStats) AverageLeadMs() int64 {
	if c.EarlyRefreshes == 0 {
		return 0
	}
	return c.EarlyLeadTotalMs / c.EarlyRefreshes
}

func (c *ClientStats) Record(latencyMs int64, stale bool) {
	c.Latencies = append(c.Latencies, latencyMs)
	if stale {
		c.StaleServes++
	}
}

// Percentile is nearest-rank: the value at rank ceil(p * n), clamped to the slice.
func (c *ClientStats) Percentile(p float64) int64 {
	if len(c.Latencies) == 0 {
		return 0
	}
	sorted := append([]int64(nil), c.Latencies...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	rank := int(math.Ceil(p*float64(len(sorted)))) - 1
	return sorted[max(0, min(len(sorted)-1, rank))]
}

func (c *ClientStats) Max() int64 {
	var m int64
	for i, v := range c.Latencies {
		if i == 0 || v > m {
			m = v
		}
	}
	return m
}

// Waiter is a request's completion: the value the client was handed.
type Waiter func(value Money)

// Strategy is the common interface of every stampede strategy; the workload drives Get and Invalidate.
type Strategy interface {
	Name() string
	// Get: a client asks for key at the current simulated time. The waiter is called when a value is available.
	Get(key string, waiter Waiter)
	// Invalidate: forget the key now. For a hot key this is an expiry you chose the moment of.
	Invalidate(key string)
	Stats() *ClientStats
	Store() *BackingStore
}

// strategyBase is the shared shape: a per-key map of entries, a store, the simulator clock, and a stats block.
// It stands in for the Java abstract class; concrete strategies embed it and add Name and Get.
type strategyBase struct {
	sim     *Simulator
	store   *BackingStore
	stats   *ClientStats
	entries map[string]Entry
	ttlMs   int64
}

func newStrategyBase(sim *Simulator, store *BackingStore, ttlMs int64) strategyBase {
	return strategyBase{sim: sim, store: store, stats: &ClientStats{}, entries: map[string]Entry{}, ttlMs: ttlMs}
}

func (b *strategyBase) Stats() *ClientStats   { return b.stats }
func (b *strategyBase) Store() *BackingStore  { return b.store }
func (b *strategyBase) Invalidate(key string) { delete(b.entries, key) }

func (b *strategyBase) freshEntry(value Money, loadTookMs, graceMs int64) Entry {
	now := b.sim.Now()
	return Entry{value, now + b.ttlMs, now + b.ttlMs + graceMs, loadTookMs}
}

// measured wraps a waiter so that latency and staleness are measured against the store at completion time.
func (b *strategyBase) measured(key string, arrivedMs int64, inner Waiter) Waiter {
	return func(value Money) {
		truth, ok := b.store.Peek(key)
		stale := !ok || value != truth
		b.stats.Record(b.sim.Now()-arrivedMs, stale)
		inner(value)
	}
}

// freshHit returns the entry when it exists and is fresh now.
func (b *strategyBase) freshHit(key string) (Entry, bool) {
	e, ok := b.entries[key]
	return e, ok && e.IsFresh(b.sim.Now())
}

// NaiveStrategy: every miss loads. The hot key expires and every request that arrives during one store
// latency starts its own load.
type NaiveStrategy struct{ strategyBase }

func NewNaiveStrategy(sim *Simulator, store *BackingStore, ttlMs int64) *NaiveStrategy {
	return &NaiveStrategy{newStrategyBase(sim, store, ttlMs)}
}

func (s *NaiveStrategy) Name() string { return "naive (every miss loads)" }

func (s *NaiveStrategy) Get(key string, waiter Waiter) {
	w := s.measured(key, s.sim.Now(), waiter)
	if e, ok := s.freshHit(key); ok {
		s.stats.Hits++
		w(e.Value)
		return
	}
	s.stats.Misses++
	started := s.sim.Now()
	s.store.Load(key, func(value Money) {
		s.entries[key] = s.freshEntry(value, s.sim.Now()-started, 0)
		w(value)
	})
}

// SingleFlightStrategy (request coalescing): the first miss starts a load and registers itself; every later
// miss for the same key, while that load is in flight, joins the waiting list instead of loading.
type SingleFlightStrategy struct {
	strategyBase
	inFlight map[string][]Waiter
}

func NewSingleFlightStrategy(sim *Simulator, store *BackingStore, ttlMs int64) *SingleFlightStrategy {
	return &SingleFlightStrategy{newStrategyBase(sim, store, ttlMs), map[string][]Waiter{}}
}

func (s *SingleFlightStrategy) Name() string { return "single-flight (coalesce)" }

func (s *SingleFlightStrategy) WaitingFor(key string) int { return len(s.inFlight[key]) }

func (s *SingleFlightStrategy) Get(key string, waiter Waiter) {
	w := s.measured(key, s.sim.Now(), waiter)
	if e, ok := s.freshHit(key); ok {
		s.stats.Hits++
		w(e.Value)
		return
	}
	s.stats.Misses++
	if waiters, flying := s.inFlight[key]; flying { // join the flight already in the air
		s.inFlight[key] = append(waiters, w)
		return
	}
	s.inFlight[key] = []Waiter{w}
	started := s.sim.Now()
	s.store.Load(key, func(value Money) {
		s.entries[key] = s.freshEntry(value, s.sim.Now()-started, 0)
		done := s.inFlight[key]
		delete(s.inFlight, key)
		for _, each := range done {
			each(value)
		}
	})
}

// LockingStrategy: a per-key lock, the shape you get with Redis SET NX or a database advisory lock. The lock
// holder loads; everyone else backs off and retries after retryMs.
type LockingStrategy struct {
	strategyBase
	locks   map[string]bool
	retryMs int64
}

func NewLockingStrategy(sim *Simulator, store *BackingStore, ttlMs, retryMs int64) *LockingStrategy {
	return &LockingStrategy{newStrategyBase(sim, store, ttlMs), map[string]bool{}, retryMs}
}

func (s *LockingStrategy) Name() string { return fmt.Sprintf("lock + retry (%d ms)", s.retryMs) }

func (s *LockingStrategy) Get(key string, waiter Waiter) { s.attempt(key, s.sim.Now(), waiter, true) }

func (s *LockingStrategy) attempt(key string, arrivedMs int64, waiter Waiter, first bool) {
	if e, ok := s.freshHit(key); ok {
		if first {
			s.stats.Hits++
		}
		s.measured(key, arrivedMs, waiter)(e.Value)
		return
	}
	if first {
		s.stats.Misses++
	}
	if !s.locks[key] { // SET key NX: we own the reload
		s.locks[key] = true
		started := s.sim.Now()
		s.store.Load(key, func(value Money) {
			s.entries[key] = s.freshEntry(value, s.sim.Now()-started, 0)
			delete(s.locks, key)
			s.measured(key, arrivedMs, waiter)(value)
		})
	} else {
		s.stats.LockRetries++
		s.sim.After(s.retryMs, func() { s.attempt(key, arrivedMs, waiter, false) })
	}
}

// EarlyExpirationStrategy is probabilistic early expiration (XFetch, Vattani et al. 2015). On a hit, a client
// recomputes early with a probability that rises as expiry approaches, scaled by how long the last load took:
// recompute if now - delta * beta * ln(rand) >= freshUntil. No coordination, no lock.
type EarlyExpirationStrategy struct {
	strategyBase
	rng  *JavaRandom
	beta float64
}

func NewEarlyExpirationStrategy(sim *Simulator, store *BackingStore, ttlMs int64, beta float64, seed int64) *EarlyExpirationStrategy {
	return &EarlyExpirationStrategy{newStrategyBase(sim, store, ttlMs), NewJavaRandom(seed), beta}
}

// javaDouble renders a float64 the way Java's Double.toString does for the simple values used here (1.0, 0.5).
func javaDouble(f float64) string {
	s := strconv.FormatFloat(f, 'f', -1, 64)
	if !strings.Contains(s, ".") {
		s += ".0"
	}
	return s
}

func (s *EarlyExpirationStrategy) Name() string {
	return "probabilistic early expiry (beta=" + javaDouble(s.beta) + ")"
}

func (s *EarlyExpirationStrategy) ShouldRecompute(e Entry, now int64) bool {
	u := s.rng.NextDouble()
	if u == 0.0 {
		u = math.SmallestNonzeroFloat64
	}
	gap := float64(e.LoadTookMs) * s.beta * -math.Log(u) // >= 0, heavy-tailed
	return float64(now)+gap >= float64(e.FreshUntilMs)
}

func (s *EarlyExpirationStrategy) Get(key string, waiter Waiter) {
	w := s.measured(key, s.sim.Now(), waiter)
	if e, ok := s.freshHit(key); ok {
		if !s.ShouldRecompute(e, s.sim.Now()) {
			s.stats.Hits++
			w(e.Value)
			return
		}
		s.stats.EarlyRefreshes++ // this client volunteers to pay for the reload
		s.stats.EarlyLeadTotalMs += e.FreshUntilMs - s.sim.Now()
	} else {
		s.stats.Misses++
	}
	started := s.sim.Now()
	s.store.Load(key, func(value Money) {
		s.entries[key] = s.freshEntry(value, s.sim.Now()-started, 0)
		w(value)
	})
}

// StaleWhileRevalidateStrategy (RFC 5861). An entry has a fresh horizon and a grace horizon. Between the two,
// the stale value is served immediately and one background refresh is started (coalesced).
type StaleWhileRevalidateStrategy struct {
	strategyBase
	graceMs     int64
	refreshing  map[string]bool
	coldWaiters map[string][]Waiter
}

func NewStaleWhileRevalidateStrategy(sim *Simulator, store *BackingStore, ttlMs, graceMs int64) *StaleWhileRevalidateStrategy {
	return &StaleWhileRevalidateStrategy{newStrategyBase(sim, store, ttlMs), graceMs, map[string]bool{}, map[string][]Waiter{}}
}

func (s *StaleWhileRevalidateStrategy) Name() string {
	return fmt.Sprintf("stale-while-revalidate (grace %d ms)", s.graceMs)
}

func (s *StaleWhileRevalidateStrategy) IsRefreshing(key string) bool { return s.refreshing[key] }

func (s *StaleWhileRevalidateStrategy) Get(key string, waiter Waiter) {
	w := s.measured(key, s.sim.Now(), waiter)
	e, present := s.entries[key]
	now := s.sim.Now()
	if present && e.IsFresh(now) {
		s.stats.Hits++
		w(e.Value)
		return
	}
	if present && e.IsUsable(now) { // stale but within grace: serve it, refresh once
		s.stats.Hits++
		w(e.Value)
		s.startRefresh(key)
		return
	}
	s.stats.Misses++ // cold or past grace: must wait, but coalesce
	s.coldWaiters[key] = append(s.coldWaiters[key], w)
	s.startRefresh(key)
}

func (s *StaleWhileRevalidateStrategy) startRefresh(key string) {
	if s.refreshing[key] {
		return
	}
	s.refreshing[key] = true
	started := s.sim.Now()
	s.store.Load(key, func(value Money) {
		s.entries[key] = s.freshEntry(value, s.sim.Now()-started, s.graceMs)
		delete(s.refreshing, key)
		waiting := s.coldWaiters[key]
		delete(s.coldWaiters, key)
		for _, each := range waiting {
			each(value)
		}
	})
}

// Factory builds a fresh strategy for one scenario run.
type Factory func(sim *Simulator, store *BackingStore) Strategy

// WarmUpMs: traffic starts after the warm-up load has landed.
const WarmUpMs int64 = 200

// Scenario is the hot-key scenario: steady traffic on one key for a while, a write to the store, and the TTL cliff.
type Scenario struct {
	RequestsPerSecond, DurationMs, StoreLatencyMs, TtlMs, WriteAtMs int64
	Seed                                                            int64
}

func NewScenario(requestsPerSecond, durationMs, storeLatencyMs, ttlMs, writeAtMs, seed int64) Scenario {
	return Scenario{requestsPerSecond, durationMs, storeLatencyMs, ttlMs, writeAtMs, seed}
}

func discard(Money) {}

// Run executes the scenario against a freshly built strategy and returns it with its statistics filled in.
func (sc Scenario) Run(factory Factory) Strategy {
	sim := NewSimulator()
	store := NewBackingStore(sim, sc.StoreLatencyMs)
	key := "balance:acct-hot"
	store.Write(key, OfMinor(100_000, USD))
	s := factory(sim, store)
	jitter := NewJavaRandom(sc.Seed)
	gapMicros := 1_000_000 / sc.RequestsPerSecond
	sim.Schedule(0, func() { s.Get(key, discard) }) // one warm-up request fills the cache at t=0
	for t := WarmUpMs * 1000; t < sc.DurationMs*1000; t += gapMicros {
		at := t/1000 + int64(jitter.NextInt(2)) // one request every gap, with 0-1 ms of jitter
		sim.Schedule(at, func() { s.Get(key, discard) })
	}
	if sc.WriteAtMs >= 0 {
		sim.Schedule(sc.WriteAtMs, func() { store.Write(key, must(OfMinor(100_000, USD).Minus(OfMinor(8_000, USD)))) })
	}
	sim.Run()
	return s
}

// RunWithInvalidation: same traffic, but the key is explicitly invalidated at invalidateAtMs instead of waiting for the TTL.
func (sc Scenario) RunWithInvalidation(factory Factory, invalidateAtMs int64) Strategy {
	sim := NewSimulator()
	store := NewBackingStore(sim, sc.StoreLatencyMs)
	key := "balance:acct-hot"
	store.Write(key, OfMinor(100_000, USD))
	s := factory(sim, store)
	gapMicros := 1_000_000 / sc.RequestsPerSecond
	sim.Schedule(0, func() { s.Get(key, discard) })
	for t := WarmUpMs * 1000; t < sc.DurationMs*1000; t += gapMicros {
		at := t / 1000
		sim.Schedule(at, func() { s.Get(key, discard) })
	}
	sim.Schedule(invalidateAtMs, func() { s.Invalidate(key) })
	sim.Run()
	return s
}

func row(s Strategy) string {
	c := s.Stats()
	return fmt.Sprintf("%-42s loads %4d  peak in-flight %4d  p50 %4d ms  p99 %4d ms  max %4d ms  stale %5d  retries %5d  early %3d",
		s.Name(), s.Store().Loads, s.Store().PeakInFlight, c.Percentile(0.50), c.Percentile(0.99), c.Max(),
		c.StaleServes, c.LockRetries, c.EarlyRefreshes)
}

func main() {
	fmt.Println("== The stampede: one hot balance key, 1,000 requests/s, 10 s of traffic, TTL 5 s, store latency 100 ms ==")
	fmt.Println("   The store is debited at t=4.0 s (so values served from 4.0 s to the next reload are stale by 80.00 USD).")
	sc := NewScenario(1_000, 10_000, 100, 5_000, 4_000, 7)
	results := []Strategy{
		sc.Run(func(sim *Simulator, st *BackingStore) Strategy { return NewNaiveStrategy(sim, st, 5_000) }),
		sc.Run(func(sim *Simulator, st *BackingStore) Strategy { return NewSingleFlightStrategy(sim, st, 5_000) }),
		sc.Run(func(sim *Simulator, st *BackingStore) Strategy { return NewLockingStrategy(sim, st, 5_000, 10) }),
		sc.Run(func(sim *Simulator, st *BackingStore) Strategy {
			return NewEarlyExpirationStrategy(sim, st, 5_000, 1.0, 11)
		}),
		sc.Run(func(sim *Simulator, st *BackingStore) Strategy {
			return NewStaleWhileRevalidateStrategy(sim, st, 5_000, 2_000)
		}),
	}
	for _, s := range results {
		fmt.Println(row(s))
	}

	fmt.Println()
	fmt.Println("Reading the table:")
	fmt.Println("  naive: when the key expires at t=5000 every request in the next 100 ms misses and loads: ~100 concurrent loads for one row.")
	fmt.Println("  single-flight: one load per cliff; the ~100 waiters share it and wait at most one store latency; the store sees 1 request.")
	fmt.Println("  lock + retry: one load, but waiters poll; each retry is a cache round trip and 10 ms of added latency.")
	fmt.Println("  early expiry: a few clients volunteer to reload before the cliff, so the cliff never arrives; no coordination at all.")
	fmt.Println("  stale-while-revalidate: nobody waits after warm-up; the cost is the stale column, which is bounded by the grace period.")
	fmt.Println("  stale: every strategy serves ~1,000 stale answers between the write at 4.0 s and the TTL at 5.0 s. That is the TTL hazard from Lesson 03;")
	fmt.Println("         only invalidation at write time removes it, and invalidation is itself a cliff (below).")

	fmt.Println()
	fmt.Println("== Invalidation is a cliff you chose: same traffic, key invalidated at t=2.5 s ==")
	n := sc.RunWithInvalidation(func(sim *Simulator, st *BackingStore) Strategy { return NewNaiveStrategy(sim, st, 5_000) }, 2_500)
	f := sc.RunWithInvalidation(func(sim *Simulator, st *BackingStore) Strategy { return NewSingleFlightStrategy(sim, st, 5_000) }, 2_500)
	fmt.Println(row(n))
	fmt.Println(row(f))
	fmt.Println("Invalidating a hot key on every write is correct (Lesson 03) and produces exactly one stampede per write unless misses are coalesced.")

	fmt.Println()
	fmt.Println("== Why beta matters: early-expiry behaviour over 30 s of traffic, no writes, at different beta ==")
	quiet := NewScenario(1_000, 30_000, 100, 5_000, -1, 7)
	for _, beta := range []float64{0.5, 1.0, 2.0, 4.0} {
		e := quiet.Run(func(sim *Simulator, st *BackingStore) Strategy {
			return NewEarlyExpirationStrategy(sim, st, 5_000, beta, 11)
		})
		fmt.Printf("  beta=%.1f  loads %3d  peak in-flight %2d  early refreshes %3d  avg lead before expiry %4d ms  p99 %3d ms\n",
			beta, e.Store().Loads, e.Store().PeakInFlight, e.Stats().EarlyRefreshes, e.Stats().AverageLeadMs(), e.Stats().Percentile(0.99))
	}
	fmt.Println("Low beta waits until the last moment, so several clients roll a refresh during the same 100 ms load and duplicate it.\n" +
		"High beta refreshes well before expiry and so runs more refresh cycles. The cliff is never reached either way; beta=1 is the paper's default.")
}
