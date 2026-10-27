// Lesson: phases/04-concurrency/04-concurrent-hash-maps/docs/en.md
// Concurrent Hash Maps: Striping and Lock-Free Reads
// Run:  go run .
// Test: go test -race ./...
// Sources: The Go Memory Model (go.dev/ref/mem); java.util.concurrent.ConcurrentHashMap Javadoc and source comments
// (Doug Lea); Herlihy & Shavit, The Art of Multiprocessor Programming, ch. 13 (concurrent hashing).
// Go 1.22+, standard library only. Same design as the Java and Python versions.
package main

import (
	"errors"
	"fmt"
	"math/bits"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf16"
)

// ErrBadShape is returned when stripe or bucket counts are not powers of two.
var ErrBadShape = errors.New("stripes and buckets must be powers of two with buckets >= stripes")

// ComputeFn receives the current value and whether it exists, and returns the new value; absent=true removes.
type ComputeFn[K, V any] func(key K, current V, present bool) (next V, absent bool)

// ConcurrentStore is the operations every map in this lesson supports; the same names as java.util.concurrent.ConcurrentMap.
// Where Java returns null for "absent", Go returns a second bool.
type ConcurrentStore[K comparable, V any] interface {
	Get(key K) (V, bool)
	// Put returns the previous value and whether there was one.
	Put(key K, value V) (V, bool)
	// PutIfAbsent is an atomic check-then-act: stores only if absent; returns the existing value and true, or false if this call stored.
	PutIfAbsent(key K, value V) (V, bool)
	Remove(key K) (V, bool)
	// Compute is an atomic read-modify-write for one key; returns the stored value and true, or false if the key was removed.
	Compute(key K, fn ComputeFn[K, V]) (V, bool)
	Size() int
}

// StableHash is Java's String.hashCode written out over UTF-16 code units; stable across processes and languages.
func StableHash(key string) int32 {
	var h int32
	for _, cu := range utf16.Encode([]rune(key)) {
		h = 31*h + int32(cu)
	}
	return h
}

// Spread mixes every input bit into every output bit (the finaliser of MurmurHash3). String.hashCode on short keys that
// differ in one character differs only in the low bits; without this step every "acct-N" would share one stripe.
func Spread(h int32) int32 {
	u := uint32(h)
	u ^= u >> 16
	u *= 0x85ebca6b
	u ^= u >> 13
	u *= 0xc2b2ae35
	u ^= u >> 16
	return int32(u)
}

// BucketIndex is the low bits of the spread hash.
func BucketIndex(key string, tableLength int) int {
	return int(uint32(Spread(StableHash(key))) & uint32(tableLength-1))
}

// GlobalLockMap is the baseline: one map behind one lock. Correct, simple, and the throughput ceiling the other two beat.
type GlobalLockMap[V any] struct {
	mu sync.Mutex
	m  map[string]V
}

// NewGlobalLockMap allocates the map.
func NewGlobalLockMap[V any]() *GlobalLockMap[V] { return &GlobalLockMap[V]{m: map[string]V{}} }

func (g *GlobalLockMap[V]) Get(key string) (V, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	v, ok := g.m[key]
	return v, ok
}

func (g *GlobalLockMap[V]) Put(key string, value V) (V, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	old, ok := g.m[key]
	g.m[key] = value
	return old, ok
}

func (g *GlobalLockMap[V]) PutIfAbsent(key string, value V) (V, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if old, ok := g.m[key]; ok {
		return old, true
	}
	g.m[key] = value
	var zero V
	return zero, false
}

func (g *GlobalLockMap[V]) Remove(key string) (V, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	old, ok := g.m[key]
	delete(g.m, key)
	return old, ok
}

func (g *GlobalLockMap[V]) Compute(key string, fn ComputeFn[string, V]) (V, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	cur, ok := g.m[key]
	next, absent := fn(key, cur, ok)
	if absent {
		delete(g.m, key)
		var zero V
		return zero, false
	}
	g.m[key] = next
	return next, true
}

func (g *GlobalLockMap[V]) Size() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.m)
}

// stripedNode is a mutable chain cell; it is only ever touched under its segment's lock.
type stripedNode[V any] struct {
	key   string
	value V
	next  *stripedNode[V]
}

// Segment is one stripe: a tiny single-threaded hash map guarded by its own lock.
type Segment[V any] struct {
	mu      sync.Mutex
	table   []*stripedNode[V]
	count   int
	resizes int
}

func (s *Segment[V]) find(key string) *stripedNode[V] {
	for n := s.table[BucketIndex(key, len(s.table))]; n != nil; n = n.next {
		if n.key == key {
			return n
		}
	}
	return nil
}

func (s *Segment[V]) insert(key string, value V) {
	if s.count+1 > len(s.table)*3/4 {
		s.grow()
	}
	i := BucketIndex(key, len(s.table))
	s.table[i] = &stripedNode[V]{key, value, s.table[i]}
	s.count++
}

func (s *Segment[V]) unlink(key string) (V, bool) {
	i := BucketIndex(key, len(s.table))
	var prev *stripedNode[V]
	for n := s.table[i]; n != nil; prev, n = n, n.next {
		if n.key == key {
			if prev == nil {
				s.table[i] = n.next
			} else {
				prev.next = n.next
			}
			s.count--
			return n.value, true
		}
	}
	var zero V
	return zero, false
}

func (s *Segment[V]) grow() {
	old := s.table
	fresh := make([]*stripedNode[V], len(old)*2)
	for _, head := range old {
		for n := head; n != nil; n = n.next {
			i := BucketIndex(n.key, len(fresh))
			fresh[i] = &stripedNode[V]{n.key, n.value, fresh[i]}
		}
	}
	s.table = fresh
	s.resizes++
}

// StripedHashMap is lock striping: the key space is split into N independent segments, each with its own lock, bucket
// table, count, and resize. Two operations contend only when their keys hash to the same segment. Reads take the
// segment lock too, which is the limitation LockFreeReadMap removes.
type StripedHashMap[V any] struct {
	segments     []*Segment[V]
	segmentShift uint
}

// NewStripedHashMap builds stripes segments; stripes must be a power of two.
func NewStripedHashMap[V any](stripes int) (*StripedHashMap[V], error) {
	if stripes < 1 || bits.OnesCount(uint(stripes)) != 1 {
		return nil, fmt.Errorf("%w: stripes=%d", ErrBadShape, stripes)
	}
	m := &StripedHashMap[V]{segments: make([]*Segment[V], stripes), segmentShift: uint(32 - bits.TrailingZeros(uint(stripes)))}
	for i := range m.segments {
		m.segments[i] = &Segment[V]{table: make([]*stripedNode[V], 4)}
	}
	return m, nil
}

func mustStriped[V any](stripes int) *StripedHashMap[V] {
	m, err := NewStripedHashMap[V](stripes)
	if err != nil {
		panic(err)
	}
	return m
}

// SegmentIndex is chosen by the HIGH bits of the spread hash; the bucket inside it by the low bits. They must not reuse the same bits.
func (m *StripedHashMap[V]) SegmentIndex(key string) int {
	if len(m.segments) == 1 {
		return 0
	}
	return int(uint32(Spread(StableHash(key))) >> m.segmentShift)
}

func (m *StripedHashMap[V]) segmentFor(key string) *Segment[V] {
	return m.segments[m.SegmentIndex(key)]
}

func (m *StripedHashMap[V]) Get(key string) (V, bool) {
	s := m.segmentFor(key)
	s.mu.Lock()
	defer s.mu.Unlock()
	if n := s.find(key); n != nil {
		return n.value, true
	}
	var zero V
	return zero, false
}

func (m *StripedHashMap[V]) Put(key string, value V) (V, bool) {
	s := m.segmentFor(key)
	s.mu.Lock()
	defer s.mu.Unlock()
	if n := s.find(key); n != nil {
		old := n.value
		n.value = value
		return old, true
	}
	s.insert(key, value)
	var zero V
	return zero, false
}

func (m *StripedHashMap[V]) PutIfAbsent(key string, value V) (V, bool) {
	s := m.segmentFor(key)
	s.mu.Lock()
	defer s.mu.Unlock()
	if n := s.find(key); n != nil { // check ...
		return n.value, true
	}
	s.insert(key, value) // ... and act, under the same lock hold
	var zero V
	return zero, false
}

func (m *StripedHashMap[V]) Remove(key string) (V, bool) {
	s := m.segmentFor(key)
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.unlink(key)
}

func (m *StripedHashMap[V]) Compute(key string, fn ComputeFn[string, V]) (V, bool) {
	s := m.segmentFor(key)
	s.mu.Lock()
	defer s.mu.Unlock()
	n := s.find(key)
	var cur V
	if n != nil {
		cur = n.value
	}
	next, absent := fn(key, cur, n != nil)
	if absent {
		if n != nil {
			s.unlink(key)
		}
		var zero V
		return zero, false
	}
	if n != nil {
		n.value = next
	} else {
		s.insert(key, next)
	}
	return next, true
}

// Size sums per-segment counts under all locks, taken in index order so two concurrent Size calls cannot deadlock.
func (m *StripedHashMap[V]) Size() int {
	for _, s := range m.segments {
		s.mu.Lock()
	}
	total := 0
	for _, s := range m.segments {
		total += s.count
	}
	for i := len(m.segments) - 1; i >= 0; i-- {
		m.segments[i].mu.Unlock()
	}
	return total
}

func (m *StripedHashMap[V]) Stripes() int { return len(m.segments) }

// TotalResizes sums per-segment resizes under the locks.
func (m *StripedHashMap[V]) TotalResizes() int {
	r := 0
	for _, s := range m.segments {
		s.mu.Lock()
		r += s.resizes
		s.mu.Unlock()
	}
	return r
}

// lfNode is an immutable chain cell: every field is set once, before the node is published by an atomic store.
type lfNode[V any] struct {
	key   string
	value V
	next  *lfNode[V]
}

// lfTable is one bucket array; the table itself is replaced wholesale on resize.
type lfTable[V any] struct {
	buckets []atomic.Pointer[lfNode[V]]
}

// LockFreeReadMap is lock striping for writers, no locks for readers. Each bucket holds an immutable chain of nodes.
// A writer, holding the stripe lock, builds a new chain and publishes it with an atomic store into the bucket;
// a reader does one atomic load of the table and one of the bucket and then walks immutable fields.
// The reader sees either the chain before the write or the chain after, never a half-built one.
type LockFreeReadMap[V any] struct {
	table       atomic.Pointer[lfTable[V]]
	stripeLocks []sync.Mutex
	count       atomic.Int64 // like ConcurrentHashMap, Size is a weakly consistent snapshot
	resizes     atomic.Int32
}

// NewLockFreeReadMap builds a map with stripes writer locks over initialBuckets buckets.
func NewLockFreeReadMap[V any](stripes, initialBuckets int) (*LockFreeReadMap[V], error) {
	if stripes < 1 || initialBuckets < 1 || bits.OnesCount(uint(stripes)) != 1 || bits.OnesCount(uint(initialBuckets)) != 1 || initialBuckets < stripes {
		return nil, fmt.Errorf("%w: stripes=%d buckets=%d", ErrBadShape, stripes, initialBuckets)
	}
	m := &LockFreeReadMap[V]{stripeLocks: make([]sync.Mutex, stripes)}
	m.table.Store(&lfTable[V]{buckets: make([]atomic.Pointer[lfNode[V]], initialBuckets)})
	return m, nil
}

func mustLockFree[V any](stripes, buckets int) *LockFreeReadMap[V] {
	m, err := NewLockFreeReadMap[V](stripes, buckets)
	if err != nil {
		panic(err)
	}
	return m
}

// stripeFor is bucketIndex mod stripes. Because both are powers of two, a bucket always maps to the same stripe across resizes.
func (m *LockFreeReadMap[V]) stripeFor(key string) int {
	return int(uint32(Spread(StableHash(key))) & uint32(len(m.stripeLocks)-1))
}

func (m *LockFreeReadMap[V]) Get(key string) (V, bool) {
	t := m.table.Load()                                     // one atomic read
	n := t.buckets[BucketIndex(key, len(t.buckets))].Load() // one atomic read
	for ; n != nil; n = n.next {                            // immutable fields: no further synchronization
		if n.key == key {
			return n.value, true
		}
	}
	var zero V
	return zero, false
}

// without rebuilds the chain without key; the removed node's value and presence come back alongside.
func without[V any](head *lfNode[V], key string) (rest *lfNode[V], removed V, found bool) {
	if head == nil {
		return nil, removed, false
	}
	if head.key == key {
		return head.next, head.value, true
	}
	rest, removed, found = without(head.next, key)
	if !found {
		return head, removed, false
	}
	return &lfNode[V]{head.key, head.value, rest}, removed, true
}

func (m *LockFreeReadMap[V]) Put(key string, value V) (V, bool) {
	s := m.stripeFor(key)
	m.stripeLocks[s].Lock()
	t := m.table.Load()
	i := BucketIndex(key, len(t.buckets))
	rest, old, had := without(t.buckets[i].Load(), key)
	t.buckets[i].Store(&lfNode[V]{key, value, rest}) // atomic store publishes the new chain
	if !had {
		m.count.Add(1)
	}
	m.stripeLocks[s].Unlock()
	if !had {
		m.growIfCrowded()
	}
	return old, had
}

func (m *LockFreeReadMap[V]) PutIfAbsent(key string, value V) (V, bool) {
	s := m.stripeFor(key)
	m.stripeLocks[s].Lock()
	t := m.table.Load()
	i := BucketIndex(key, len(t.buckets))
	head := t.buckets[i].Load()
	for n := head; n != nil; n = n.next { // check ...
		if n.key == key {
			m.stripeLocks[s].Unlock()
			return n.value, true
		}
	}
	t.buckets[i].Store(&lfNode[V]{key, value, head}) // ... and act, same lock hold
	m.count.Add(1)
	m.stripeLocks[s].Unlock()
	m.growIfCrowded()
	var zero V
	return zero, false
}

func (m *LockFreeReadMap[V]) Remove(key string) (V, bool) {
	s := m.stripeFor(key)
	m.stripeLocks[s].Lock()
	defer m.stripeLocks[s].Unlock()
	t := m.table.Load()
	i := BucketIndex(key, len(t.buckets))
	rebuilt, old, had := without(t.buckets[i].Load(), key)
	if !had {
		var zero V
		return zero, false
	}
	t.buckets[i].Store(rebuilt)
	m.count.Add(-1)
	return old, true
}

func (m *LockFreeReadMap[V]) Compute(key string, fn ComputeFn[string, V]) (V, bool) {
	s := m.stripeFor(key)
	m.stripeLocks[s].Lock()
	t := m.table.Load()
	i := BucketIndex(key, len(t.buckets))
	rest, old, had := without(t.buckets[i].Load(), key)
	next, absent := fn(key, old, had)
	inserted := false
	var result V
	present := false
	if absent {
		if had {
			t.buckets[i].Store(rest)
			m.count.Add(-1)
		}
	} else {
		t.buckets[i].Store(&lfNode[V]{key, next, rest}) // updated node in front of the untouched rest
		if !had {
			m.count.Add(1)
			inserted = true
		}
		result, present = next, true
	}
	m.stripeLocks[s].Unlock()
	if inserted {
		m.growIfCrowded()
	}
	return result, present
}

// growIfCrowded is called with NO lock held: a writer must not hold one stripe and then take all of them, because that
// inverts the index order ResizeTo uses and two writers could deadlock. Grows past 75% load; a stale read of the
// count or the table length is harmless because ResizeTo re-checks under all locks.
func (m *LockFreeReadMap[V]) growIfCrowded() {
	t := m.table.Load()
	if m.count.Load() > int64(len(t.buckets)*3/4) {
		_ = m.ResizeTo(len(t.buckets) * 2)
	}
}

// Size is lock-free and weakly consistent: an insert that has published its node may not yet have counted itself.
func (m *LockFreeReadMap[V]) Size() int { return int(m.count.Load()) }

func (m *LockFreeReadMap[V]) Buckets() int { return len(m.table.Load().buckets) }
func (m *LockFreeReadMap[V]) Resizes() int { return int(m.resizes.Load()) }

// ResizeTo takes every stripe lock (no writer is mid-flight), builds a fresh table, and publishes it with one
// atomic store. Readers that loaded the old table keep walking it; it is frozen because every write
// goes through a lock this method holds.
func (m *LockFreeReadMap[V]) ResizeTo(newBuckets int) error {
	if newBuckets < 1 || bits.OnesCount(uint(newBuckets)) != 1 || newBuckets < len(m.stripeLocks) {
		return fmt.Errorf("%w: buckets=%d", ErrBadShape, newBuckets)
	}
	for i := range m.stripeLocks {
		m.stripeLocks[i].Lock()
	}
	defer func() {
		for i := len(m.stripeLocks) - 1; i >= 0; i-- {
			m.stripeLocks[i].Unlock()
		}
	}()
	old := m.table.Load()
	if newBuckets <= len(old.buckets) { // another goroutine grew first
		return nil
	}
	fresh := &lfTable[V]{buckets: make([]atomic.Pointer[lfNode[V]], newBuckets)}
	for i := range old.buckets {
		for n := old.buckets[i].Load(); n != nil; n = n.next {
			j := BucketIndex(n.key, newBuckets)
			fresh.buckets[j].Store(&lfNode[V]{n.key, n.value, fresh.buckets[j].Load()})
		}
	}
	m.table.Store(fresh)
	m.resizes.Add(1)
	return nil
}

// IdempotencyRecord is what the map stores in the demo: the record an idempotent payment endpoint keeps per key.
type IdempotencyRecord struct {
	PaymentID   string
	AmountMinor int64
	Status      string
}

func (r IdempotencyRecord) String() string {
	return fmt.Sprintf("IdempotencyRecord[paymentId=%s, amountMinor=%d, status=%s]", r.PaymentID, r.AmountMinor, r.Status)
}

// Barrier is a reusable rendezvous: Await blocks until parties goroutines have arrived, then releases them all.
type Barrier struct {
	mu      sync.Mutex
	cond    *sync.Cond
	parties int
	waiting int
	gen     int
}

// NewBarrier builds a barrier for parties goroutines.
func NewBarrier(parties int) *Barrier {
	b := &Barrier{parties: parties}
	b.cond = sync.NewCond(&b.mu)
	return b
}

// Await is CyclicBarrier.await.
func (b *Barrier) Await() {
	b.mu.Lock()
	defer b.mu.Unlock()
	gen := b.gen
	b.waiting++
	if b.waiting == b.parties {
		b.waiting = 0
		b.gen++
		b.cond.Broadcast()
		return
	}
	for gen == b.gen {
		b.cond.Wait()
	}
}

func increment(_ string, v int64, present bool) (int64, bool) {
	if !present {
		return 1, false
	}
	return v + 1, false
}

// Benchmark runs the same workload on any store: workers goroutines, each ops operations, 95% reads. Returns ops/ms.
func Benchmark(store ConcurrentStore[string, int64], workers, ops, keySpace int) int64 {
	keyTable := make([]string, keySpace)
	for k := range keyTable {
		keyTable[k] = fmt.Sprintf("key-%d", k)
		store.Put(keyTable[k], 0)
	}
	start := make(chan struct{})
	var done sync.WaitGroup
	for w := 0; w < workers; w++ {
		seed := uint64(w)
		done.Add(1)
		go func() {
			defer done.Done()
			<-start
			x := uint64(0x9E3779B97F4A7C15) ^ seed // per-goroutine deterministic PRNG, no shared state
			for i := 0; i < ops; i++ {
				x ^= x << 13
				x ^= x >> 7
				x ^= x << 17
				key := keyTable[x%uint64(keySpace)]
				if (x>>32)%100 < 95 {
					store.Get(key)
				} else {
					store.Compute(key, increment)
				}
			}
		}()
	}
	t0 := time.Now()
	close(start)
	done.Wait()
	ms := max(int64(1), time.Since(t0).Milliseconds())
	return int64(workers) * int64(ops) / ms
}

// RaceGetThenPut forces the check-then-act race with a barrier placed in the gap: every goroutine reads "absent", then all
// wait for each other, then all write. Returns how many goroutines believed they created the record.
func RaceGetThenPut(store ConcurrentStore[string, IdempotencyRecord], workers int) int {
	inTheGap := NewBarrier(workers)
	var done sync.WaitGroup
	winners := make([]int, workers)
	for w := 0; w < workers; w++ {
		id := w
		done.Add(1)
		go func() {
			defer done.Done()
			_, exists := store.Get("idem-7") // check
			inTheGap.Await()                 // everyone has checked, nobody has acted
			if !exists {
				store.Put("idem-7", IdempotencyRecord{fmt.Sprintf("pay_%d", id), 4_999, "CAPTURED"}) // act
				winners[id] = 1
			}
		}()
	}
	done.Wait()
	sum := 0
	for _, w := range winners {
		sum += w
	}
	return sum
}

// RacePutIfAbsent is the same shape, but the check and the act are one PutIfAbsent.
func RacePutIfAbsent(store ConcurrentStore[string, IdempotencyRecord], workers int) int {
	start := NewBarrier(workers)
	var done sync.WaitGroup
	winners := make([]int, workers)
	for w := 0; w < workers; w++ {
		id := w
		done.Add(1)
		go func() {
			defer done.Done()
			start.Await()
			if _, existed := store.PutIfAbsent("idem-7", IdempotencyRecord{fmt.Sprintf("pay_%d", id), 4_999, "CAPTURED"}); !existed {
				winners[id] = 1
			}
		}()
	}
	done.Wait()
	sum := 0
	for _, w := range winners {
		sum += w
	}
	return sum
}

func demoStripes() {
	fmt.Println("== Where keys land: 16 stripes, top 4 bits of the mixed hash ==")
	m := mustStriped[int64](16)
	var placements []string
	for i := 1; i <= 8; i++ {
		placements = append(placements, fmt.Sprintf("acct-%d->s%d", i, m.SegmentIndex(fmt.Sprintf("acct-%d", i))))
	}
	fmt.Println(strings.Join(placements, "  "))
	fmt.Printf("stableHash(\"acct-1\") = %d, stableHash(\"acct-2\") = %d (differ by 1: all low bits); after spread: %d and %d\n",
		StableHash("acct-1"), StableHash("acct-2"), Spread(StableHash("acct-1")), Spread(StableHash("acct-2")))
	fmt.Println()
}

func demoLockFreeRead() {
	fmt.Println("== Lock-free reads: a writer replaces the chain, a reader sees old or new, never half ==")
	m := mustLockFree[IdempotencyRecord](4, 4) // tiny on purpose: growth is visible
	m.Put("idem-1", IdempotencyRecord{"pay_001", 1_999, "CAPTURED"})
	m.Put("idem-2", IdempotencyRecord{"pay_002", 2_500, "CAPTURED"})
	m.Put("idem-3", IdempotencyRecord{"pay_003", 4_999, "PENDING"})
	fmt.Printf("3 puts into a 4-bucket table -> buckets = %d, size = %d (load 3/4: not yet)\n", m.Buckets(), m.Size())
	m.Put("idem-4", IdempotencyRecord{"pay_004", 100, "PENDING"})
	fmt.Printf("4th put -> buckets = %d, resizes = %d (load crossed 75%%)\n", m.Buckets(), m.Resizes())
	r, _ := m.Get("idem-3")
	fmt.Printf("get idem-3 -> %v\n", r)
	m.Compute("idem-3", func(_ string, r IdempotencyRecord, _ bool) (IdempotencyRecord, bool) {
		return IdempotencyRecord{r.PaymentID, r.AmountMinor, "CAPTURED"}, false
	})
	r, _ = m.Get("idem-3")
	fmt.Printf("after compute: get idem-3 -> %v\n", r)
	removed, _ := m.Remove("idem-2")
	_, still := m.Get("idem-2")
	fmt.Printf("remove idem-2 -> %v; get idem-2 -> present=%v\n", removed, still)
	_ = m.ResizeTo(32)
	r, _ = m.Get("idem-1")
	fmt.Printf("explicit resize to %d buckets; get idem-1 -> %v; size = %d\n", m.Buckets(), r, m.Size())
	fmt.Println()
}

func main() {
	demoStripes()
	demoLockFreeRead()

	fmt.Println("== Check-then-act, forced with a barrier in the gap (8 goroutines, one idempotency key) ==")
	fmt.Printf("get-then-put on StripedHashMap:   %d goroutines believe they created the record\n", RaceGetThenPut(mustStriped[IdempotencyRecord](16), 8))
	fmt.Printf("putIfAbsent on StripedHashMap:    %d goroutine created it\n", RacePutIfAbsent(mustStriped[IdempotencyRecord](16), 8))
	fmt.Printf("get-then-put on LockFreeReadMap:  %d goroutines believe they created the record\n", RaceGetThenPut(mustLockFree[IdempotencyRecord](16, 64), 8))
	fmt.Printf("putIfAbsent on LockFreeReadMap:   %d goroutine created it\n", RacePutIfAbsent(mustLockFree[IdempotencyRecord](16, 64), 8))
	fmt.Println("A thread-safe map does not make your check-then-act atomic. Only a per-key atomic operation does.")
	fmt.Println()

	fmt.Println("== Throughput, 4 goroutines x 100,000 ops, 95% get / 5% compute, 1,024 keys (ops per ms; varies by machine) ==")
	workers, ops, keys := 4, 100_000, 1024
	global := Benchmark(NewGlobalLockMap[int64](), workers, ops, keys)
	striped := Benchmark(mustStriped[int64](16), workers, ops, keys)
	lockFree := Benchmark(mustLockFree[int64](16, 2048), workers, ops, keys)
	fmt.Printf("%-18s %8d\n", "GlobalLockMap", global)
	fmt.Printf("%-18s %8d\n", "StripedHashMap", striped)
	fmt.Printf("%-18s %8d\n", "LockFreeReadMap", lockFree)
	fmt.Println("(Wall-clock numbers vary. With 4+ cores the ordering is global < striped < lock-free reads; with 1-2 cores" +
		" they converge because there is no parallelism left to win. The ratio, not the number, is the lesson.)")
}
