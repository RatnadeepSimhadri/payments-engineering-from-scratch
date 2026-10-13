// Lesson: phases/01-complexity-and-core-structures/06-hash-maps-from-scratch/docs/en.md
// Hash Maps from Scratch: Chaining, Open Addressing, and Resizing
// Run:  go run .
// Test: go test ./...
// Sources: Knuth TAOCP Vol. 3 Section 6.4 (hashing, linear probing analysis); FNV-1a (Fowler, Noll, Vo);
// Stripe API "Idempotent requests" for the store framing. Go 1.22+, standard library only.
package main

import (
	"errors"
	"fmt"
	"math/bits"
)

// Hasher turns a key into a 32-bit hash. Go's built-in map seeds its hash per process, so a
// deterministic demo needs its own function.
type Hasher[K comparable] func(K) uint32

// FNV1a is FNV-1a, 32-bit, over the UTF-8 bytes of the string. Same result in Java, Python, and Go.
func FNV1a(s string) uint32 {
	h := uint32(0x811c9dc5)
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= 0x01000193
	}
	return h
}

// ConstantHasher is a pathological hasher: every key lands in bucket 0. Used to measure worst-case behaviour.
func ConstantHasher[K comparable](K) uint32 { return 0 }

// IndexFor maps a 32-bit hash to a slot in a power-of-two table, mixing the high bits into the low ones first.
func IndexFor(hash uint32, capacity int) int {
	spread := hash ^ (hash >> 16)
	return int(spread & uint32(capacity-1))
}

var errCapacity = errors.New("capacity must be a power of two")

func requirePowerOfTwo(capacity int) {
	if capacity <= 0 || bits.OnesCount(uint(capacity)) != 1 {
		panic(fmt.Errorf("%w: %d", errCapacity, capacity))
	}
}

// ---------------------------------------------------------------- separate chaining

type node[K comparable, V any] struct {
	key   K
	value V
	next  *node[K, V]
}

// ChainedHashMap is separate chaining: each bucket is the head of a singly linked list of entries.
type ChainedHashMap[K comparable, V any] struct {
	hasher     Hasher[K]
	table      []*node[K, V]
	size       int
	collisions int64
	resizes    int
}

const (
	DefaultCapacity = 8
	ChainedMaxLoad  = 0.75
	OpenMaxLoad     = 0.5
)

func NewChainedHashMap[K comparable, V any](hasher Hasher[K], capacity int) *ChainedHashMap[K, V] {
	requirePowerOfTwo(capacity)
	return &ChainedHashMap[K, V]{hasher: hasher, table: make([]*node[K, V], capacity)}
}

func (m *ChainedHashMap[K, V]) bucketOf(key K) int { return IndexFor(m.hasher(key), len(m.table)) }

// Put inserts or replaces; it returns the previous value and whether there was one.
func (m *ChainedHashMap[K, V]) Put(key K, value V) (old V, replaced bool) {
	b := m.bucketOf(key)
	for n := m.table[b]; n != nil; n = n.next {
		if n.key == key {
			old = n.value
			n.value = value
			return old, true
		}
	}
	if m.table[b] != nil {
		m.collisions++
	}
	m.table[b] = &node[K, V]{key, value, m.table[b]}
	m.size++
	if float64(m.size) > float64(len(m.table))*ChainedMaxLoad {
		m.resize(len(m.table) * 2)
	}
	return old, false
}

func (m *ChainedHashMap[K, V]) Get(key K) (V, bool) {
	for n := m.table[m.bucketOf(key)]; n != nil; n = n.next {
		if n.key == key {
			return n.value, true
		}
	}
	var zero V
	return zero, false
}

func (m *ChainedHashMap[K, V]) ContainsKey(key K) bool {
	_, ok := m.Get(key)
	return ok
}

func (m *ChainedHashMap[K, V]) Remove(key K) (V, bool) {
	b := m.bucketOf(key)
	var prev *node[K, V]
	for n := m.table[b]; n != nil; prev, n = n, n.next {
		if n.key == key {
			if prev == nil {
				m.table[b] = n.next
			} else {
				prev.next = n.next
			}
			m.size--
			return n.value, true
		}
	}
	var zero V
	return zero, false
}

func (m *ChainedHashMap[K, V]) resize(newCapacity int) {
	old := m.table
	m.table = make([]*node[K, V], newCapacity)
	for _, head := range old {
		for n := head; n != nil; {
			next := n.next
			b := IndexFor(m.hasher(n.key), newCapacity)
			n.next = m.table[b]
			m.table[b] = n
			n = next
		}
	}
	m.resizes++
}

// MaxChainLength is the longest chain in the table: the worst-case lookup cost right now.
func (m *ChainedHashMap[K, V]) MaxChainLength() int {
	best := 0
	for _, head := range m.table {
		length := 0
		for n := head; n != nil; n = n.next {
			length++
		}
		if length > best {
			best = length
		}
	}
	return best
}

func (m *ChainedHashMap[K, V]) Size() int           { return m.size }
func (m *ChainedHashMap[K, V]) Capacity() int       { return len(m.table) }
func (m *ChainedHashMap[K, V]) LoadFactor() float64 { return float64(m.size) / float64(len(m.table)) }
func (m *ChainedHashMap[K, V]) Collisions() int64   { return m.collisions }
func (m *ChainedHashMap[K, V]) Resizes() int        { return m.resizes }

// ---------------------------------------------------------------- open addressing

type slotState byte

const (
	empty slotState = iota
	occupied
	tombstone
)

// OpenAddressingHashMap is linear probing with tombstones: keys, values, and states live in flat slices.
type OpenAddressingHashMap[K comparable, V any] struct {
	hasher     Hasher[K]
	keys       []K
	values     []V
	states     []slotState
	size       int
	tombstones int
	collisions int64 // total displacement: sum over new-key inserts of (slot used - home slot)
	resizes    int
}

func NewOpenAddressingHashMap[K comparable, V any](hasher Hasher[K], capacity int) *OpenAddressingHashMap[K, V] {
	requirePowerOfTwo(capacity)
	m := &OpenAddressingHashMap[K, V]{hasher: hasher}
	m.allocate(capacity)
	return m
}

func (m *OpenAddressingHashMap[K, V]) allocate(capacity int) {
	m.keys = make([]K, capacity)
	m.values = make([]V, capacity)
	m.states = make([]slotState, capacity)
	m.tombstones = 0
}

func (m *OpenAddressingHashMap[K, V]) homeOf(key K) int { return IndexFor(m.hasher(key), len(m.keys)) }

// SlotOf returns the slot holding the key, or -1. Probing stops at empty and walks through tombstones.
func (m *OpenAddressingHashMap[K, V]) SlotOf(key K) int {
	capacity := len(m.keys)
	i := m.homeOf(key)
	for probes := 0; probes < capacity; probes++ {
		switch m.states[i] {
		case empty:
			return -1
		case occupied:
			if m.keys[i] == key {
				return i
			}
		}
		i = (i + 1) & (capacity - 1)
	}
	return -1
}

func (m *OpenAddressingHashMap[K, V]) Put(key K, value V) (old V, replaced bool) {
	if float64(m.size+m.tombstones+1) > float64(len(m.keys))*OpenMaxLoad {
		m.resize(len(m.keys) * 2)
	}
	capacity := len(m.keys)
	home := m.homeOf(key)
	i := home
	firstTombstone := -1
	displacement := 0
	for m.states[i] != empty {
		if m.states[i] == occupied && m.keys[i] == key {
			old = m.values[i]
			m.values[i] = value
			return old, true
		}
		if m.states[i] == tombstone && firstTombstone < 0 {
			firstTombstone = i
		}
		i = (i + 1) & (capacity - 1)
		displacement++
	}
	if firstTombstone >= 0 { // reuse the grave rather than extend the cluster
		i = firstTombstone
		m.tombstones--
		displacement = (i - home) & (capacity - 1)
	}
	m.collisions += int64(displacement)
	m.keys[i] = key
	m.values[i] = value
	m.states[i] = occupied
	m.size++
	return old, false
}

func (m *OpenAddressingHashMap[K, V]) Get(key K) (V, bool) {
	i := m.SlotOf(key)
	if i < 0 {
		var zero V
		return zero, false
	}
	return m.values[i], true
}

func (m *OpenAddressingHashMap[K, V]) ContainsKey(key K) bool { return m.SlotOf(key) >= 0 }

func (m *OpenAddressingHashMap[K, V]) Remove(key K) (V, bool) {
	i := m.SlotOf(key)
	if i < 0 {
		var zero V
		return zero, false
	}
	old := m.values[i]
	var zeroK K
	var zeroV V
	m.keys[i] = zeroK
	m.values[i] = zeroV
	m.states[i] = tombstone // not empty: a probe chain may run through this slot
	m.tombstones++
	m.size--
	return old, true
}

func (m *OpenAddressingHashMap[K, V]) resize(newCapacity int) {
	oldKeys, oldValues, oldStates := m.keys, m.values, m.states
	m.allocate(newCapacity)
	for j, state := range oldStates {
		if state != occupied { // tombstones are dropped here
			continue
		}
		i := IndexFor(m.hasher(oldKeys[j]), newCapacity)
		for m.states[i] != empty {
			i = (i + 1) & (newCapacity - 1)
		}
		m.keys[i] = oldKeys[j]
		m.values[i] = oldValues[j]
		m.states[i] = occupied
	}
	m.resizes++
}

// MaxProbeRun is the longest run of non-empty slots: the worst-case probe length for a miss right now.
func (m *OpenAddressingHashMap[K, V]) MaxProbeRun() int {
	best, run := 0, 0
	capacity := len(m.states)
	for pass := 0; pass < 2; pass++ { // two passes so a run wrapping the end is seen whole
		for _, s := range m.states {
			if s == empty {
				if run > best {
					best = run
				}
				run = 0
			} else if run < capacity {
				run++
			}
		}
	}
	if run > best {
		best = run
	}
	return best
}

func (m *OpenAddressingHashMap[K, V]) Size() int     { return m.size }
func (m *OpenAddressingHashMap[K, V]) Capacity() int { return len(m.keys) }
func (m *OpenAddressingHashMap[K, V]) LoadFactor() float64 {
	return float64(m.size) / float64(len(m.keys))
}
func (m *OpenAddressingHashMap[K, V]) Collisions() int64 { return m.collisions }
func (m *OpenAddressingHashMap[K, V]) Tombstones() int   { return m.tombstones }
func (m *OpenAddressingHashMap[K, V]) Resizes() int      { return m.resizes }

// ---------------------------------------------------------------- idempotency store

// SimpleMap is what both tables share, so the idempotency store can be built on either one.
type SimpleMap[K comparable, V any] interface {
	Put(key K, value V) (V, bool)
	Get(key K) (V, bool)
	Remove(key K) (V, bool)
	ContainsKey(key K) bool
	Size() int
	Capacity() int
	Collisions() int64
}

// StoredResponse is what an API server stores per idempotency key: the status and body it already sent.
type StoredResponse struct {
	Status int
	Body   string
}

// Outcome is the result of asking the store to handle a request: the response, and whether it was replayed.
type Outcome struct {
	Response StoredResponse
	Replayed bool
}

// IdempotencyStore: key = client-supplied idempotency key, value = the response first produced for it.
// The first call with a key runs the handler and records its response; every later call replays it
// without running the handler again, so a retried "charge 19.99 USD" charges once.
type IdempotencyStore struct {
	responses   SimpleMap[string, StoredResponse]
	handlerRuns int
}

func NewIdempotencyStore(backing SimpleMap[string, StoredResponse]) *IdempotencyStore {
	return &IdempotencyStore{responses: backing}
}

func (s *IdempotencyStore) Handle(idempotencyKey string, handler func() StoredResponse) Outcome {
	if existing, ok := s.responses.Get(idempotencyKey); ok {
		return Outcome{existing, true}
	}
	fresh := handler()
	s.handlerRuns++
	s.responses.Put(idempotencyKey, fresh)
	return Outcome{fresh, false}
}

func (s *IdempotencyStore) HandlerRuns() int { return s.handlerRuns }
func (s *IdempotencyStore) Size() int        { return s.responses.Size() }

// ---------------------------------------------------------------- demo

func keys(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("idem-%06d", i)
	}
	return out
}

func main() {
	fmt.Println("== FNV-1a is the same in every language ==")
	fmt.Printf("fnv1a(\"idem-000001\") = 0x%08x\n", FNV1a("idem-000001"))
	fmt.Printf("fnv1a(\"\") = 0x%08x\n", FNV1a(""))

	fmt.Println("\n== 10,000 idempotency keys, separate chaining ==")
	chained := NewChainedHashMap[string, int](FNV1a, DefaultCapacity)
	ks := keys(10_000)
	for i, k := range ks {
		chained.Put(k, i)
	}
	fmt.Printf("size=%d capacity=%d load=%.3f resizes=%d collisions=%d maxChain=%d\n",
		chained.Size(), chained.Capacity(), chained.LoadFactor(), chained.Resizes(), chained.Collisions(), chained.MaxChainLength())

	fmt.Println("\n== Same keys, open addressing (linear probing) ==")
	open := NewOpenAddressingHashMap[string, int](FNV1a, DefaultCapacity)
	for i, k := range ks {
		open.Put(k, i)
	}
	fmt.Printf("size=%d capacity=%d load=%.3f resizes=%d displacement=%d maxProbeRun=%d\n",
		open.Size(), open.Capacity(), open.LoadFactor(), open.Resizes(), open.Collisions(), open.MaxProbeRun())

	fmt.Println("\n== Tombstones: remove half, re-insert, probe chains survive ==")
	for i := 0; i < len(ks); i += 2 {
		open.Remove(ks[i])
	}
	v, _ := open.Get("idem-000001")
	fmt.Printf("after removing 5,000: size=%d tombstones=%d get(idem-000001)=%d\n", open.Size(), open.Tombstones(), v)
	for i := 0; i < len(ks); i += 2 {
		open.Put(ks[i], -i)
	}
	fmt.Printf("after re-inserting: size=%d tombstones=%d (graves were reused)\n", open.Size(), open.Tombstones())

	fmt.Println("\n== Worst case: a hasher that returns 0 for every key ==")
	bad := NewChainedHashMap[string, int](ConstantHasher[string], DefaultCapacity)
	badOpen := NewOpenAddressingHashMap[string, int](ConstantHasher[string], DefaultCapacity)
	for i := 0; i < 64; i++ {
		bad.Put(ks[i], i)
		badOpen.Put(ks[i], i)
	}
	fmt.Printf("chaining: 64 keys -> collisions=%d maxChain=%d (every lookup is O(n))\n", bad.Collisions(), bad.MaxChainLength())
	fmt.Printf("probing:  64 keys -> displacement=%d maxProbeRun=%d (sum 0..63 = 2016)\n", badOpen.Collisions(), badOpen.MaxProbeRun())

	fmt.Println("\n== Idempotency store: retry the same charge three times ==")
	store := NewIdempotencyStore(NewChainedHashMap[string, StoredResponse](FNV1a, DefaultCapacity))
	for attempt := 1; attempt <= 3; attempt++ {
		o := store.Handle("order-8841-charge", func() StoredResponse {
			return StoredResponse{201, `{"charge":"ch_1","amount":1999,"currency":"USD"}`}
		})
		fmt.Printf("attempt %d: status=%d replayed=%v body=%s\n", attempt, o.Response.Status, o.Replayed, o.Response.Body)
	}
	fmt.Printf("handler ran %d time(s) for 3 attempts\n", store.HandlerRuns())
}
