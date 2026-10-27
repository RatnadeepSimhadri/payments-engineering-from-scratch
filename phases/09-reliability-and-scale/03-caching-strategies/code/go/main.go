// Lesson: phases/09-reliability-and-scale/03-caching-strategies/docs/en.md
// Caching Strategies: Cache-Aside, Write-Through, Write-Behind
// Run:  go run .
// Test: go test ./...
// Sources: Memcached protocol docs (cache-aside idiom); Redis documentation on EXPIRE semantics;
//
//	Phase 10 Lesson 01 for the Money type. Go 1.22+, standard library only. Same design as Java and Python.
package main

import (
	"fmt"
	"math"
)

// ManualClock is a clock the demo and the tests move by hand. Nothing in this lesson sleeps.
type ManualClock struct{ nowMs int64 }

func NewManualClock(startMs int64) *ManualClock { return &ManualClock{nowMs: startMs} }

func (c *ManualClock) Now() int64 { return c.nowMs }

// Advance moves the clock forward; it panics on a negative step because a clock that runs backwards is a bug.
func (c *ManualClock) Advance(ms int64) {
	if ms < 0 {
		panic("clock only moves forward")
	}
	c.nowMs += ms
}

// BackingStore is the slow system of record: a database holding account balances. Every read and
// write costs a fixed simulated latency that is accumulated, not slept. Peek is the oracle the
// staleness meter uses; it is not a real operation and is not counted.
type BackingStore struct {
	rows               map[string]Money
	readLatencyMs      int64
	writeLatencyMs     int64
	Reads, Writes      int64
	SimulatedLatencyMs int64
}

func NewBackingStore(readLatencyMs, writeLatencyMs int64) *BackingStore {
	return &BackingStore{rows: map[string]Money{}, readLatencyMs: readLatencyMs, writeLatencyMs: writeLatencyMs}
}

func (s *BackingStore) Read(account string) (Money, bool) {
	s.Reads++
	s.SimulatedLatencyMs += s.readLatencyMs
	m, ok := s.rows[account]
	return m, ok
}

func (s *BackingStore) Write(account string, balance Money) {
	s.Writes++
	s.SimulatedLatencyMs += s.writeLatencyMs
	s.rows[account] = balance
}

// Peek is truth without cost: what the balance really is right now. Used only to measure staleness.
func (s *BackingStore) Peek(account string) (Money, bool) {
	m, ok := s.rows[account]
	return m, ok
}

// CacheStats holds hit, miss, and staleness counters for one strategy under one workload.
type CacheStats struct {
	Hits, Misses, StaleReads, CacheLatencyMs int64
	CacheLatencyPerOpMs                      int64
}

func NewCacheStats(cacheLatencyPerOpMs int64) *CacheStats {
	return &CacheStats{CacheLatencyPerOpMs: cacheLatencyPerOpMs}
}

func (s *CacheStats) Hit()  { s.Hits++; s.CacheLatencyMs += s.CacheLatencyPerOpMs }
func (s *CacheStats) Miss() { s.Misses++; s.CacheLatencyMs += s.CacheLatencyPerOpMs }

func (s *CacheStats) Reads() int64 { return s.Hits + s.Misses }

func (s *CacheStats) HitRate() float64 {
	if s.Reads() == 0 {
		return 0.0
	}
	return float64(s.Hits) / float64(s.Reads())
}

// Entry is a cached value with its absolute expiry time.
type Entry struct {
	Value       Money
	ExpiresAtMs int64
}

// NoExpiry is the TTL sentinel for entries that must never expire (write-behind's dirty data).
const NoExpiry = math.MaxInt64

// TtlCache is a map of key to (value, expiry). Expiry is checked lazily on read against the injected clock.
type TtlCache struct {
	entries map[string]Entry
	clock   *ManualClock
}

func NewTtlCache(clock *ManualClock) *TtlCache {
	return &TtlCache{entries: map[string]Entry{}, clock: clock}
}

// Get returns the cached value, or false when absent or expired. An expired entry is removed on the way out.
func (c *TtlCache) Get(key string) (Money, bool) {
	e, ok := c.entries[key]
	if !ok {
		return Money{}, false
	}
	if e.ExpiresAtMs <= c.clock.Now() {
		delete(c.entries, key)
		return Money{}, false
	}
	return e.Value, true
}

// Put stores the value; ttlMs == NoExpiry keeps it forever. The expiry sum is overflow-checked like Math.addExact.
func (c *TtlCache) Put(key string, value Money, ttlMs int64) {
	expires := int64(NoExpiry)
	if ttlMs != NoExpiry {
		sum, err := addChecked(c.clock.Now(), ttlMs)
		if err != nil {
			panic(err)
		}
		expires = sum
	}
	c.entries[key] = Entry{value, expires}
}

func (c *TtlCache) Invalidate(key string) bool {
	_, ok := c.entries[key]
	delete(c.entries, key)
	return ok
}

func (c *TtlCache) Contains(key string) bool { _, ok := c.Get(key); return ok }
func (c *TtlCache) Size() int                { return len(c.entries) }
func (c *TtlCache) Clear()                   { c.entries = map[string]Entry{} }

// BalanceCache is the one interface every strategy implements, so the workload can drive them interchangeably.
type BalanceCache interface {
	Name() string
	// Read is the balance the application sees. May be stale depending on the strategy.
	Read(account string) (Money, bool)
	// Write records a new balance. Where it goes first is the strategy.
	Write(account string, balance Money)
	Stats() *CacheStats
	Store() *BackingStore
	Cache() *TtlCache
}

// ReadMeasured counts a read as stale when the value handed to the caller differs from the store's current truth.
// It is the Java interface's default method, written as a function because Go interfaces carry no bodies.
func ReadMeasured(c BalanceCache, account string) (Money, bool) {
	seen, seenOk := c.Read(account)
	truth, truthOk := c.Store().Peek(account)
	if seenOk != truthOk || (seenOk && seen != truth) {
		c.Stats().StaleReads++
	}
	return seen, seenOk
}

// cached is the state every strategy shares: the store, the cache, and the counters.
type cached struct {
	store *BackingStore
	cache *TtlCache
	stats *CacheStats
}

func (c *cached) Stats() *CacheStats   { return c.stats }
func (c *cached) Store() *BackingStore { return c.store }
func (c *cached) Cache() *TtlCache     { return c.cache }

// loadOnMiss is the shared read path: hit returns the cached value; miss loads from the store and populates.
func (c *cached) loadOnMiss(account string, ttlMs int64) (Money, bool) {
	if v, ok := c.cache.Get(account); ok {
		c.stats.Hit()
		return v, true
	}
	c.stats.Miss()
	loaded, ok := c.store.Read(account)
	if ok {
		c.cache.Put(account, loaded, ttlMs)
	}
	return loaded, ok
}

// CacheAside (lazy loading): the application checks the cache, loads from the store on a miss, and
// populates the cache with a TTL. Writes go to the store. Whether the write also invalidates the cached
// entry is the whole difference between "eventually right" and "wrong for up to one TTL".
type CacheAside struct {
	cached
	ttlMs             int64
	invalidateOnWrite bool
}

func NewCacheAside(store *BackingStore, clock *ManualClock, ttlMs int64, invalidateOnWrite bool, cacheLatencyMs int64) *CacheAside {
	return &CacheAside{cached{store, NewTtlCache(clock), NewCacheStats(cacheLatencyMs)}, ttlMs, invalidateOnWrite}
}

func (c *CacheAside) Name() string {
	if c.invalidateOnWrite {
		return "cache-aside + invalidate"
	}
	return "cache-aside, TTL only"
}

func (c *CacheAside) Read(account string) (Money, bool) { return c.loadOnMiss(account, c.ttlMs) }

func (c *CacheAside) Write(account string, balance Money) {
	c.store.Write(account, balance)
	if c.invalidateOnWrite {
		c.cache.Invalidate(account)
	}
}

// Loader is the function a read-through cache owns, so callers can never forget to populate it.
type Loader func(account string) (Money, bool)

// ReadThrough: the cache owns the loader. Same read path as cache-aside, moved behind the interface.
// Writes go to the store and delete the entry (write-around with invalidation); the next read reloads.
type ReadThrough struct {
	cached
	loader Loader
	ttlMs  int64
}

func NewReadThrough(store *BackingStore, clock *ManualClock, ttlMs int64, cacheLatencyMs int64) *ReadThrough {
	return &ReadThrough{cached{store, NewTtlCache(clock), NewCacheStats(cacheLatencyMs)}, store.Read, ttlMs}
}

func (c *ReadThrough) Name() string { return "read-through" }

func (c *ReadThrough) Read(account string) (Money, bool) {
	if v, ok := c.cache.Get(account); ok {
		c.stats.Hit()
		return v, true
	}
	c.stats.Miss()
	loaded, ok := c.loader(account)
	if ok {
		c.cache.Put(account, loaded, c.ttlMs)
	}
	return loaded, ok
}

func (c *ReadThrough) Write(account string, balance Money) {
	c.store.Write(account, balance)
	c.cache.Invalidate(account)
}

// WriteThrough: every write goes to the store and then into the cache, synchronously. The cache is
// never stale with respect to writes that went through it; the cost is store latency on every write and
// cache space spent on keys nobody will read.
type WriteThrough struct {
	cached
	ttlMs int64
}

func NewWriteThrough(store *BackingStore, clock *ManualClock, ttlMs int64, cacheLatencyMs int64) *WriteThrough {
	return &WriteThrough{cached{store, NewTtlCache(clock), NewCacheStats(cacheLatencyMs)}, ttlMs}
}

func (c *WriteThrough) Name() string { return "write-through" }

func (c *WriteThrough) Read(account string) (Money, bool) { return c.loadOnMiss(account, c.ttlMs) }

func (c *WriteThrough) Write(account string, balance Money) {
	c.store.Write(account, balance) // store first: if this fails, the cache never sees the value
	c.cache.Put(account, balance, c.ttlMs)
}

// WriteBehind (write-back): writes land in the cache and a dirty queue; the store is updated later by
// Flush, which coalesces repeated writes to one key into one store write. Fast and cheap, and the
// only strategy here where a cache crash loses committed-looking data.
type WriteBehind struct {
	cached
	dirty      map[string]Money
	order      []string
	LostWrites int64
}

func NewWriteBehind(store *BackingStore, clock *ManualClock, cacheLatencyMs int64) *WriteBehind {
	return &WriteBehind{cached: cached{store, NewTtlCache(clock), NewCacheStats(cacheLatencyMs)}, dirty: map[string]Money{}}
}

func (c *WriteBehind) Name() string { return "write-behind" }

func (c *WriteBehind) Read(account string) (Money, bool) {
	return c.loadOnMiss(account, NoExpiry) // dirty data must never expire
}

func (c *WriteBehind) Write(account string, balance Money) {
	c.cache.Put(account, balance, NoExpiry)
	if _, present := c.dirty[account]; !present {
		c.order = append(c.order, account)
	}
	c.dirty[account] = balance
}

func (c *WriteBehind) DirtyCount() int { return len(c.dirty) }

// Flush writes every dirty key to the store once, in first-dirtied order. Returns the number of store writes.
func (c *WriteBehind) Flush() int {
	flushed := 0
	for _, key := range c.order {
		c.store.Write(key, c.dirty[key])
		delete(c.dirty, key)
		flushed++
	}
	c.order = c.order[:0]
	return flushed
}

// Crash: the cache process dies before flushing. Everything dirty is gone; the store keeps the old balances.
func (c *WriteBehind) Crash() {
	c.LostWrites += int64(len(c.dirty))
	c.dirty = map[string]Money{}
	c.order = nil
	c.cache.Clear()
}

// Op is one workload operation: a read, or a debit of DebitMinor from the account.
type Op struct {
	IsWrite    bool
	Account    string
	DebitMinor int64
}

// javaRound is Math.round(double): floor(x + 0.5).
func javaRound(x float64) int { return int(math.Floor(x + 0.5)) }

// GenerateWorkload builds a seeded, skewed workload: most reads hit a few hot accounts (the 80/20 shape real
// balance traffic has), writes are debits of a random amount. Deterministic for a given seed.
func GenerateWorkload(seed int64, ops, accounts int, writeFraction, hotFraction, hotShare float64) []Op {
	rng := NewJavaRandom(seed)
	hotCount := max(1, javaRound(float64(accounts)*hotFraction))
	out := make([]Op, 0, ops)
	for i := 0; i < ops; i++ {
		hot := rng.NextDouble() < hotShare
		var idx int
		if hot {
			idx = rng.NextInt(hotCount)
		} else {
			idx = hotCount + rng.NextInt(max(1, accounts-hotCount))
		}
		account := fmt.Sprintf("acct-%04d", idx%accounts)
		isWrite := rng.NextDouble() < writeFraction
		var debit int64
		if isWrite {
			debit = int64(1 + rng.NextInt(2000))
		}
		out = append(out, Op{isWrite, account, debit})
	}
	return out
}

// SeededStore creates a store with every account at 1000.00 USD and the counters reset.
func SeededStore(accounts int, readLatencyMs, writeLatencyMs int64) *BackingStore {
	store := NewBackingStore(readLatencyMs, writeLatencyMs)
	for i := 0; i < accounts; i++ {
		store.Write(fmt.Sprintf("acct-%04d", i), OfMinor(100_000, USD))
	}
	store.Reads, store.Writes, store.SimulatedLatencyMs = 0, 0, 0 // seeding is not part of the measurement
	return store
}

// RunWorkload runs the ops. Each write reads the current balance through the cache, debits, and writes back.
func RunWorkload(target BalanceCache, ops []Op, clock *ManualClock, msPerOp int64) {
	for _, op := range ops {
		if op.IsWrite {
			current, _ := ReadMeasured(target, op.Account)
			target.Write(op.Account, must(current.Minus(OfMinor(op.DebitMinor, USD))))
		} else {
			ReadMeasured(target, op.Account)
		}
		clock.Advance(msPerOp)
	}
}

func row(c BalanceCache) string {
	s := c.Stats()
	st := c.Store()
	return fmt.Sprintf("%-26s hit rate %5.1f%%  store reads %5d  store writes %5d  divergent %5d  simulated latency %7d ms",
		c.Name(), 100*s.HitRate(), st.Reads, st.Writes, s.StaleReads, st.SimulatedLatencyMs+s.CacheLatencyMs)
}

func peek(store *BackingStore, account string) Money {
	m, _ := store.Peek(account)
	return m
}

func read(c BalanceCache, account string) Money {
	m, _ := c.Read(account)
	return m
}

func main() {
	fmt.Println("== The stale balance hazard: cache-aside with TTL, no invalidation ==")
	clock := NewManualClock(0)
	store := NewBackingStore(20, 25)
	store.Write("acct-0001", OfMinor(10_000, USD))
	ttlOnly := NewCacheAside(store, clock, 30_000, false, 1)
	fmt.Printf("balance read through cache:        %v  (miss, now cached for 30s)\n", read(ttlOnly, "acct-0001"))
	store.Write("acct-0001", OfMinor(2_000, USD)) // a batch job debits 80.00 straight to the database
	fmt.Printf("batch job debits 80.00 in the DB:  store = %v\n", peek(store, "acct-0001"))
	seen := read(ttlOnly, "acct-0001")
	fmt.Printf("authorization reads the cache:     %v  (hit; STALE by %v)\n", seen, must(seen.Minus(peek(store, "acct-0001"))))
	request := OfMinor(5_000, USD)
	approved := seen.Compare(request) >= 0
	fmt.Printf("approve 50.00 withdrawal?          %v  -> store balance would become %v\n", approved, must(peek(store, "acct-0001").Minus(request)))
	clock.Advance(30_000)
	fmt.Printf("after the 30s TTL:                 %v  (miss, reloaded; correct again)\n", read(ttlOnly, "acct-0001"))

	fmt.Println()
	fmt.Println("== Same scenario with invalidate-on-write ==")
	clock2 := NewManualClock(0)
	store2 := NewBackingStore(20, 25)
	store2.Write("acct-0001", OfMinor(10_000, USD))
	invalidating := NewCacheAside(store2, clock2, 30_000, true, 1)
	invalidating.Read("acct-0001")
	invalidating.Write("acct-0001", OfMinor(2_000, USD))
	fmt.Printf("read after write:                  %v  (miss, cache entry was removed by the write)\n", read(invalidating, "acct-0001"))

	fmt.Println()
	fmt.Println("== Write-behind: fast until the cache dies ==")
	clock3 := NewManualClock(0)
	store3 := NewBackingStore(20, 25)
	store3.Write("acct-0007", OfMinor(50_000, USD))
	wb := NewWriteBehind(store3, clock3, 1)
	wb.Write("acct-0007", OfMinor(45_000, USD))
	wb.Write("acct-0007", OfMinor(41_000, USD))
	wb.Write("acct-0007", OfMinor(40_000, USD))
	fmt.Printf("three debits applied; cache says   %v, store still says %v, dirty keys = %d\n",
		read(wb, "acct-0007"), peek(store3, "acct-0007"), wb.DirtyCount())
	before := store3.Writes
	flushed := wb.Flush()
	fmt.Printf("flush(): %d store write for 3 debits (coalesced); store now %v; store writes went %d -> %d\n",
		flushed, peek(store3, "acct-0007"), before, store3.Writes)
	wb.Write("acct-0007", OfMinor(30_000, USD))
	wb.Crash()
	fmt.Printf("one more debit, then crash before flush: store = %v, lost writes = %d\n", peek(store3, "acct-0007"), wb.LostWrites)

	fmt.Println()
	fmt.Println("== Measured: 20,000 ops, 500 accounts, 20% of accounts take 80% of traffic, 15% writes ==")
	fmt.Println("   store read = 20 ms, store write = 25 ms, cache op = 1 ms, TTL = 5 s, one op per 10 ms")
	ops := GenerateWorkload(42, 20_000, 500, 0.15, 0.20, 0.80)
	clocks := []*ManualClock{NewManualClock(0), NewManualClock(0), NewManualClock(0), NewManualClock(0), NewManualClock(0)}
	strategies := []BalanceCache{
		NewCacheAside(SeededStore(500, 20, 25), clocks[0], 5_000, false, 1),
		NewCacheAside(SeededStore(500, 20, 25), clocks[1], 5_000, true, 1),
		NewReadThrough(SeededStore(500, 20, 25), clocks[2], 5_000, 1),
		NewWriteThrough(SeededStore(500, 20, 25), clocks[3], 5_000, 1),
		NewWriteBehind(SeededStore(500, 20, 25), clocks[4], 1),
	}
	for i, s := range strategies {
		RunWorkload(s, ops, clocks[i], 10)
		if w, ok := s.(*WriteBehind); ok {
			w.Flush()
		}
		fmt.Println(row(s))
	}
	var noCacheLatency, writes int64 // every read 20 ms; every write is a read plus a write, 45 ms
	for _, op := range ops {
		if op.IsWrite {
			noCacheLatency += 45
			writes++
		} else {
			noCacheLatency += 20
		}
	}
	fmt.Printf("%-26s hit rate   0.0%%  store reads %5d  store writes %5d  divergent     0  simulated latency %7d ms\n",
		"no cache", len(ops), writes, noCacheLatency)
	fmt.Println()
	fmt.Println("\"divergent\" = reads where the value handed to the caller differed from the store at that moment.")
	fmt.Println("For cache-aside that is a stale balance an authorization trusted. For write-behind the arrow is reversed:")
	fmt.Println("the cache is right and the store is behind, which is durability exposure rather than staleness.")
}
