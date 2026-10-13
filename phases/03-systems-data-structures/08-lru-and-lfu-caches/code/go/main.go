// Lesson: phases/03-systems-data-structures/08-lru-and-lfu-caches/docs/en.md
// LRU and LFU Caches from Scratch
// Run:  go run .
// Test: go test ./...
// Sources: Shah, Mitra, Matani (2010), "An O(1) algorithm for implementing the LFU cache eviction scheme";
//          Denning (1968), "The Working Set Model for Program Behavior"; RFC 9111 (HTTP Caching) for freshness lifetime.
// Go 1.22+, standard library only. Same design as the Java and Python versions.
package main

import (
	"errors"
	"fmt"
	"math"
)

// ----- clock ---------------------------------------------------------------------------------------

// Clock is something that tells the time in milliseconds. Injected so tests never sleep.
type Clock interface {
	NowMillis() int64
}

// ManualClock advances only when told to.
type ManualClock struct{ now int64 }

// NewManualClock starts at startMillis.
func NewManualClock(startMillis int64) *ManualClock { return &ManualClock{now: startMillis} }

// NowMillis implements Clock.
func (c *ManualClock) NowMillis() int64 { return c.now }

// Advance moves the clock forward; it never goes backwards.
func (c *ManualClock) Advance(millis int64) {
	if millis < 0 {
		panic("clock cannot go backwards")
	}
	c.now += millis
}

// ----- money (minor units + currency, from Phase 10 Lesson 01) -------------------------------------

// ErrCurrencyMismatch is returned when two Money values of different currencies are combined.
var ErrCurrencyMismatch = errors.New("currency mismatch")

// Money is an exact amount: a signed count of minor units and a currency code.
type Money struct {
	MinorUnits int64
	Currency   string
}

// Plus adds two amounts of the same currency.
func (m Money) Plus(o Money) (Money, error) {
	if m.Currency != o.Currency {
		return Money{}, fmt.Errorf("%w: cannot combine %s with %s", ErrCurrencyMismatch, m.Currency, o.Currency)
	}
	return Money{m.MinorUnits + o.MinorUnits, m.Currency}, nil
}

// Minus subtracts two amounts of the same currency.
func (m Money) Minus(o Money) (Money, error) {
	if m.Currency != o.Currency {
		return Money{}, fmt.Errorf("%w: cannot combine %s with %s", ErrCurrencyMismatch, m.Currency, o.Currency)
	}
	return Money{m.MinorUnits - o.MinorUnits, m.Currency}, nil
}

func (m Money) String() string {
	mag := m.MinorUnits
	sign := ""
	if mag < 0 {
		sign, mag = "-", -mag
	}
	return fmt.Sprintf("%s%d.%02d %s", sign, mag/100, mag%100, m.Currency)
}

// ----- LRU: hash map + doubly linked list ----------------------------------------------------------

// NoExpiry marks an entry without a TTL.
const NoExpiry = math.MaxInt64

type lruNode[K comparable, V any] struct {
	key        K
	value      V
	expiresAt  int64
	prev, next *lruNode[K, V]
}

// LruCache is a least-recently-used cache with O(1) Get, Put, and Remove. Entries may carry a TTL;
// an expired entry is a miss and is removed on access. The list runs most-recent (head) to
// least-recent (tail).
type LruCache[K comparable, V any] struct {
	Capacity                             int
	clock                                Clock
	m                                    map[K]*lruNode[K, V]
	head, tail                           *lruNode[K, V] // sentinels
	Hits, Misses, Evictions, Expirations int64
}

// NewLruCache builds an empty cache; a nil clock means a ManualClock at 0.
func NewLruCache[K comparable, V any](capacity int, clock Clock) (*LruCache[K, V], error) {
	if capacity <= 0 {
		return nil, fmt.Errorf("capacity must be positive: %d", capacity)
	}
	if clock == nil {
		clock = NewManualClock(0)
	}
	c := &LruCache[K, V]{Capacity: capacity, clock: clock, m: map[K]*lruNode[K, V]{}, head: &lruNode[K, V]{}, tail: &lruNode[K, V]{}}
	c.head.next = c.tail
	c.tail.prev = c.head
	return c, nil
}

// list surgery: every operation is a constant number of pointer writes
func (c *LruCache[K, V]) unlink(n *lruNode[K, V]) {
	n.prev.next = n.next
	n.next.prev = n.prev
}

func (c *LruCache[K, V]) pushFront(n *lruNode[K, V]) {
	n.next = c.head.next
	n.prev = c.head
	c.head.next.prev = n
	c.head.next = n
}

func (c *LruCache[K, V]) expired(n *lruNode[K, V]) bool { return n.expiresAt <= c.clock.NowMillis() }

// Get returns the value and true on a hit, which moves the entry to the front; the zero value and false on a miss.
func (c *LruCache[K, V]) Get(key K) (V, bool) {
	var zero V
	n, ok := c.m[key]
	if !ok {
		c.Misses++
		return zero, false
	}
	if c.expired(n) {
		c.unlink(n)
		delete(c.m, key)
		c.Expirations++
		c.Misses++
		return zero, false
	}
	c.unlink(n)
	c.pushFront(n)
	c.Hits++
	return n.value, true
}

// Put inserts or updates without a TTL.
func (c *LruCache[K, V]) Put(key K, value V) { c.putInternal(key, value, NoExpiry) }

// PutWithTTL inserts or updates an entry that expires ttlMillis from now.
func (c *LruCache[K, V]) PutWithTTL(key K, value V, ttlMillis int64) error {
	if ttlMillis <= 0 {
		return errors.New("ttlMillis must be positive")
	}
	c.putInternal(key, value, c.clock.NowMillis()+ttlMillis)
	return nil
}

func (c *LruCache[K, V]) putInternal(key K, value V, expiresAt int64) {
	if n, ok := c.m[key]; ok {
		n.value = value
		n.expiresAt = expiresAt
		c.unlink(n)
		c.pushFront(n)
		return
	}
	if len(c.m) >= c.Capacity {
		victim := c.tail.prev
		c.unlink(victim)
		delete(c.m, victim.key)
		c.Evictions++
	}
	n := &lruNode[K, V]{key: key, value: value, expiresAt: expiresAt}
	c.m[key] = n
	c.pushFront(n)
}

// Remove deletes a key; it reports whether the key was present.
func (c *LruCache[K, V]) Remove(key K) bool {
	n, ok := c.m[key]
	if !ok {
		return false
	}
	c.unlink(n)
	delete(c.m, key)
	return true
}

// Len is the number of live and not-yet-expired-but-untouched entries.
func (c *LruCache[K, V]) Len() int { return len(c.m) }

// Keys lists keys most recent first. O(n); for tests and debugging.
func (c *LruCache[K, V]) Keys() []K {
	out := make([]K, 0, len(c.m))
	for n := c.head.next; n != c.tail; n = n.next {
		out = append(out, n.key)
	}
	return out
}

// HitRate is hits over all lookups.
func (c *LruCache[K, V]) HitRate() float64 {
	total := c.Hits + c.Misses
	if total == 0 {
		return 0
	}
	return float64(c.Hits) / float64(total)
}

// ----- LFU: frequency buckets, O(1) --------------------------------------------------------------

type lfuNode[K comparable, V any] struct {
	key        K
	value      V
	freq       int
	prev, next *lfuNode[K, V]
}

// bucket is a doubly linked list of nodes that share one frequency; most recent at the head.
type bucket[K comparable, V any] struct {
	head, tail *lfuNode[K, V]
	size       int
}

func newBucket[K comparable, V any]() *bucket[K, V] {
	b := &bucket[K, V]{head: &lfuNode[K, V]{}, tail: &lfuNode[K, V]{}}
	b.head.next = b.tail
	b.tail.prev = b.head
	return b
}

func (b *bucket[K, V]) pushFront(n *lfuNode[K, V]) {
	n.next = b.head.next
	n.prev = b.head
	b.head.next.prev = n
	b.head.next = n
	b.size++
}

func (b *bucket[K, V]) unlink(n *lfuNode[K, V]) {
	n.prev.next = n.next
	n.next.prev = n.prev
	b.size--
}

func (b *bucket[K, V]) leastRecent() *lfuNode[K, V] { return b.tail.prev }

// LfuCache is a least-frequently-used cache with O(1) Get and Put (Shah, Mitra, Matani 2010).
// Nodes live in a bucket per frequency; minFreq tracks the lowest non-empty bucket. Ties within a
// bucket are broken least-recently-used first.
type LfuCache[K comparable, V any] struct {
	Capacity                int
	m                       map[K]*lfuNode[K, V]
	buckets                 map[int]*bucket[K, V]
	minFreq                 int
	Hits, Misses, Evictions int64
}

// NewLfuCache builds an empty cache.
func NewLfuCache[K comparable, V any](capacity int) (*LfuCache[K, V], error) {
	if capacity <= 0 {
		return nil, fmt.Errorf("capacity must be positive: %d", capacity)
	}
	return &LfuCache[K, V]{Capacity: capacity, m: map[K]*lfuNode[K, V]{}, buckets: map[int]*bucket[K, V]{}}, nil
}

// touch moves n from bucket f to bucket f+1, maintaining minFreq. Constant time.
func (c *LfuCache[K, V]) touch(n *lfuNode[K, V]) {
	b := c.buckets[n.freq]
	b.unlink(n)
	if b.size == 0 {
		delete(c.buckets, n.freq)
		if c.minFreq == n.freq {
			c.minFreq = n.freq + 1
		}
	}
	n.freq++
	nb, ok := c.buckets[n.freq]
	if !ok {
		nb = newBucket[K, V]()
		c.buckets[n.freq] = nb
	}
	nb.pushFront(n)
}

// Get returns the value and true on a hit, which raises the entry's frequency; the zero value and false on a miss.
func (c *LfuCache[K, V]) Get(key K) (V, bool) {
	var zero V
	n, ok := c.m[key]
	if !ok {
		c.Misses++
		return zero, false
	}
	c.touch(n)
	c.Hits++
	return n.value, true
}

// Put inserts (at frequency 1, evicting from the minFreq bucket if full) or updates (which counts as a touch).
func (c *LfuCache[K, V]) Put(key K, value V) {
	if n, ok := c.m[key]; ok {
		n.value = value
		c.touch(n)
		return
	}
	if len(c.m) >= c.Capacity {
		b := c.buckets[c.minFreq]
		victim := b.leastRecent()
		b.unlink(victim)
		if b.size == 0 {
			delete(c.buckets, c.minFreq)
		}
		delete(c.m, victim.key)
		c.Evictions++
	}
	n := &lfuNode[K, V]{key: key, value: value, freq: 1}
	c.m[key] = n
	b, ok := c.buckets[1]
	if !ok {
		b = newBucket[K, V]()
		c.buckets[1] = b
	}
	b.pushFront(n)
	c.minFreq = 1
}

// Remove deletes a key; it reports whether the key was present.
func (c *LfuCache[K, V]) Remove(key K) bool {
	n, ok := c.m[key]
	if !ok {
		return false
	}
	b := c.buckets[n.freq]
	b.unlink(n)
	delete(c.m, key)
	if b.size == 0 {
		delete(c.buckets, n.freq)
		if c.minFreq == n.freq {
			// the next non-empty bucket; O(distinct frequencies), only on explicit remove
			c.minFreq = 0
			for f := range c.buckets {
				if c.minFreq == 0 || f < c.minFreq {
					c.minFreq = f
				}
			}
		}
	}
	return true
}

// Frequency is how many times the key has been put or hit, or 0 if absent.
func (c *LfuCache[K, V]) Frequency(key K) int {
	if n, ok := c.m[key]; ok {
		return n.freq
	}
	return 0
}

// MinFrequency is the frequency of the next eviction candidate.
func (c *LfuCache[K, V]) MinFrequency() int { return c.minFreq }

// Len is the number of entries.
func (c *LfuCache[K, V]) Len() int { return len(c.m) }

// HitRate is hits over all lookups.
func (c *LfuCache[K, V]) HitRate() float64 {
	total := c.Hits + c.Misses
	if total == 0 {
		return 0
	}
	return float64(c.Hits) / float64(total)
}

// ----- deterministic trace and replay -------------------------------------------------------------

// Rng is splitmix64: identical sequences in Java, Python, and Go for a given seed.
type Rng struct{ state uint64 }

// NewRng seeds the generator.
func NewRng(seed uint64) *Rng { return &Rng{seed} }

// Next returns the next 64-bit value.
func (r *Rng) Next() uint64 {
	r.state += 0x9E3779B97F4A7C15
	z := r.state
	z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
	z = (z ^ (z >> 27)) * 0x94D049BB133111EB
	return z ^ (z >> 31)
}

// NextInt returns a value in [0, bound).
func (r *Rng) NextInt(bound int) int { return int((r.Next() >> 33) % uint64(bound)) }

// BinTrace: hotPercent of lookups hit hotCount BINs starting at hotOffset; the rest spread over coldCount cold BINs.
func BinTrace(seed uint64, ops, hotOffset, hotCount, hotPercent, coldCount int) []string {
	rng := NewRng(seed)
	keys := make([]string, 0, ops)
	for i := 0; i < ops; i++ {
		if rng.NextInt(100) < hotPercent {
			keys = append(keys, fmt.Sprintf("bin-%d", hotOffset+rng.NextInt(hotCount)))
		} else {
			keys = append(keys, fmt.Sprintf("bin-%d", 2*hotCount+rng.NextInt(coldCount)))
		}
	}
	return keys
}

// DefaultBinTrace is BinTrace with 20 hot BINs carrying 80% of lookups over 2000 cold BINs.
func DefaultBinTrace(seed uint64, ops, hotOffset int) []string {
	return BinTrace(seed, ops, hotOffset, 20, 80, 2000)
}

// Scan is count one-off keys.
func Scan(count int) []string {
	keys := make([]string, count)
	for i := range keys {
		keys[i] = fmt.Sprintf("scan-%d", i)
	}
	return keys
}

// cache is what Replay needs from either policy.
type cache interface {
	Get(key string) (string, bool)
	Put(key, value string)
}

// Replay looks each key up; on a miss it "loads" the value and inserts. Returns the hit rate over this batch only.
func Replay(c cache, keys []string) float64 {
	if len(keys) == 0 {
		return 0
	}
	hits := 0
	for _, k := range keys {
		if _, ok := c.Get(k); ok {
			hits++
		} else {
			c.Put(k, "record:"+k)
		}
	}
	return float64(hits) / float64(len(keys))
}

func mustLru[K comparable, V any](capacity int, clock Clock) *LruCache[K, V] {
	c, err := NewLruCache[K, V](capacity, clock)
	if err != nil {
		panic(err)
	}
	return c
}

func mustLfu[K comparable, V any](capacity int) *LfuCache[K, V] {
	c, err := NewLfuCache[K, V](capacity)
	if err != nil {
		panic(err)
	}
	return c
}

func present(ok bool) string {
	if ok {
		return "true"
	}
	return "false"
}

func main() {
	fmt.Println("== LRU: merchant config, most recent at the head ==")
	clock := NewManualClock(1_700_000_000_000)
	lru := mustLru[string, string](3, clock)
	for _, m := range []string{"merchant-1", "merchant-2", "merchant-3"} {
		lru.Put(m, "config("+m+")")
	}
	fmt.Printf("after 3 puts: %v\n", lru.Keys())
	lru.Get("merchant-1")
	fmt.Printf("after get(merchant-1): %v\n", lru.Keys())
	lru.Put("merchant-4", "config(merchant-4)")
	fmt.Printf("after put(merchant-4): %v  evicted merchant-2, evictions=%d\n", lru.Keys(), lru.Evictions)

	fmt.Println("\n== TTL: a config entry that must be re-read after five minutes ==")
	_ = lru.PutWithTTL("merchant-9", "fee_bps=290", 5*60_000)
	v, _ := lru.Get("merchant-9")
	fmt.Printf("t+0:     get(merchant-9) = %s\n", v)
	clock.Advance(4*60_000 + 59_999)
	v, _ = lru.Get("merchant-9")
	fmt.Printf("t+4:59.9 get(merchant-9) = %s\n", v)
	clock.Advance(1)
	_, ok := lru.Get("merchant-9")
	fmt.Printf("t+5:00   get(merchant-9) present = %s  expirations=%d\n", present(ok), lru.Expirations)

	fmt.Println("\n== LFU: BIN lookups, frequency buckets ==")
	lfu := mustLfu[string, string](3)
	for _, b := range []string{"bin-411111", "bin-411111", "bin-411111", "bin-555555", "bin-555555", "bin-601100"} {
		lfu.Put(b, "brand("+b+")")
	}
	fmt.Printf("frequencies: 411111=%d 555555=%d 601100=%d min=%d\n", lfu.Frequency("bin-411111"), lfu.Frequency("bin-555555"), lfu.Frequency("bin-601100"), lfu.MinFrequency())
	lfu.Put("bin-371449", "brand(bin-371449)")
	_, ok601 := lfu.Get("bin-601100")
	_, ok411 := lfu.Get("bin-411111")
	fmt.Printf("put(bin-371449) evicts the least frequent: 601100 present = %s, 411111 present = %s\n", present(ok601), present(ok411))

	fmt.Println("\n== Same trace, both policies, capacity 32 (20 hot BINs carry 80% of lookups) ==")
	names := []string{"phase 1: hot set A", "scan: 500 one-off BINs", "phase 1 again", "phase 2: hot set B"}
	segments := [][]string{DefaultBinTrace(1, 6000, 0), Scan(500), DefaultBinTrace(2, 2000, 0), DefaultBinTrace(3, 6000, 20)}
	lru2 := mustLru[string, string](32, nil)
	lfu2 := mustLfu[string, string](32)
	fmt.Printf("%-24s %12s %12s\n", "segment", "LRU hit rate", "LFU hit rate")
	for i, name := range names {
		fmt.Printf("%-24s %12.3f %12.3f\n", name, Replay(lru2, segments[i]), Replay(lfu2, segments[i]))
	}
	// the scan flushed LRU's hot set; LFU kept it. Show the first 200 lookups after the scan.
	lru3 := mustLru[string, string](32, nil)
	lfu3 := mustLfu[string, string](32)
	for i := 0; i < 2; i++ {
		Replay(lru3, segments[i])
		Replay(lfu3, segments[i])
	}
	first200 := segments[2][:200]
	fmt.Printf("first 200 lookups after the scan: LRU %.3f  LFU %.3f\n", Replay(lru3, first200), Replay(lfu3, first200))

	fmt.Println("\n== Why you do not cache balances ==")
	ledgerBalance := Money{10_000, "USD"}
	balanceCache := mustLru[string, Money](16, nil)
	balanceCache.Put("acct-7", ledgerBalance)
	ledgerBalance, _ = ledgerBalance.Minus(Money{8_000, "USD"}) // a posting lands: 100.00 -> 20.00
	cached, _ := balanceCache.Get("acct-7")
	authorization := Money{5_000, "USD"}
	fmt.Printf("ledger says %s, cache says %s\n", ledgerBalance, cached)
	afterCache, _ := cached.Minus(authorization)
	verdict := "declined"
	if afterCache.MinorUnits >= 0 {
		verdict = "approved"
	}
	fmt.Printf("authorize %s against the cache: %s\n", authorization, verdict)
	actual, _ := ledgerBalance.Minus(authorization)
	fmt.Printf("actual balance after that approval would be %s: an overdraft the ledger never allowed\n", actual)
}
