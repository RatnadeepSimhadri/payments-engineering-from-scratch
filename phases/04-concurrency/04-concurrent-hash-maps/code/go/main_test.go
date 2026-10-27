// Tests for phases/04-concurrency/04-concurrent-hash-maps. Run: go test -race ./...
package main

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

func check(t *testing.T, name string, ok bool) {
	t.Helper()
	if !ok {
		t.Error(name)
	}
}

type factory func() ConcurrentStore[string, int64]

var factories = map[string]factory{
	"GlobalLockMap":   func() ConcurrentStore[string, int64] { return NewGlobalLockMap[int64]() },
	"StripedHashMap":  func() ConcurrentStore[string, int64] { return mustStriped[int64](16) },
	"LockFreeReadMap": func() ConcurrentStore[string, int64] { return mustLockFree[int64](16, 64) },
}

var names = []string{"GlobalLockMap", "StripedHashMap", "LockFreeReadMap"}

func get(m ConcurrentStore[string, int64], k string) int64 { v, _ := m.Get(k); return v }

func TestHashing(t *testing.T) {
	check(t, "stableHash matches String.hashCode", StableHash("acct-1") == -1423448841)
	check(t, "adjacent keys differ only in low bits before spreading", StableHash("acct-2")-StableHash("acct-1") == 1)
	stripes := map[int]bool{}
	m := mustStriped[int64](16)
	for i := 1; i <= 8; i++ {
		stripes[m.SegmentIndex(fmt.Sprintf("acct-%d", i))] = true
	}
	check(t, "spread scatters acct-1..8 across several stripes", len(stripes) >= 4)
	inRange := true
	for i := 0; i < 1000; i++ {
		b := BucketIndex(fmt.Sprintf("k%d", i), 64)
		inRange = inRange && b >= 0 && b < 64
	}
	check(t, "bucketIndex stays within the table", inRange)
	check(t, "spread is deterministic", Spread(12345) == Spread(12345))
	// The placements the Java demo prints: acct-1->s8 acct-2->s4 ... acct-8->s5.
	want := []int{8, 4, 13, 7, 0, 1, 7, 5}
	for i, w := range want {
		if got := m.SegmentIndex(fmt.Sprintf("acct-%d", i+1)); got != w {
			t.Errorf("acct-%d lands in stripe %d, Java says %d", i+1, got, w)
		}
	}
}

func TestSingleThreadedContract(t *testing.T) {
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			m := factories[name]()
			_, ok := m.Get("a")
			check(t, "empty get is absent", !ok && m.Size() == 0)
			_, had := m.Put("a", 1)
			check(t, "first put reports no previous", !had)
			check(t, "get after put", get(m, "a") == 1 && m.Size() == 1)
			old, had := m.Put("a", 2)
			check(t, "second put returns previous", had && old == 1 && get(m, "a") == 2 && m.Size() == 1)
			ex, existed := m.PutIfAbsent("a", 9)
			check(t, "putIfAbsent on present returns existing and keeps it", existed && ex == 2 && get(m, "a") == 2)
			_, existed = m.PutIfAbsent("b", 5)
			check(t, "putIfAbsent on absent stores", !existed && get(m, "b") == 5 && m.Size() == 2)
			v, present := m.Compute("a", func(_ string, v int64, _ bool) (int64, bool) { return v + 10, false })
			check(t, "compute increments", present && v == 12 && get(m, "a") == 12)
			v, present = m.Compute("c", increment)
			check(t, "compute on absent creates", present && v == 1 && m.Size() == 3)
			_, present = m.Compute("c", func(string, int64, bool) (int64, bool) { return 0, true })
			_, ok = m.Get("c")
			check(t, "compute returning absent removes", !present && !ok && m.Size() == 2)
			r, had := m.Remove("b")
			_, ok = m.Get("b")
			check(t, "remove returns value", had && r == 5 && !ok && m.Size() == 1)
			_, had = m.Remove("zzz")
			check(t, "remove absent reports false", !had)
			_, had = m.Put("", 0)
			z, ok := m.Get("")
			check(t, "the empty key and the zero value are legal entries", !had && ok && z == 0)
			m.Remove("")
			for i := 0; i < 500; i++ {
				m.Put(fmt.Sprintf("fill-%d", i), int64(i))
			}
			allPresent := true
			for i := 0; i < 500; i++ {
				allPresent = allPresent && get(m, fmt.Sprintf("fill-%d", i)) == int64(i)
			}
			check(t, "500 keys all retrievable after growth", allPresent && m.Size() == 501)
		})
	}
}

// TestConcurrentWithBarriers is deterministic: every phase starts behind a barrier and ends with a WaitGroup, so the
// assertions are about end states that every legal interleaving must produce, never about timing.
func TestConcurrentWithBarriers(t *testing.T) {
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			m := factories[name]()
			workers, perWorker, shared, bumps := 4, 5_000, 100, 1_000
			barrier := NewBarrier(workers)
			var done sync.WaitGroup
			var errs atomic.Int64
			for w := 0; w < workers; w++ {
				id := w
				done.Add(1)
				go func() {
					defer done.Done()
					barrier.Await() // phase 1: disjoint inserts
					for i := 0; i < perWorker; i++ {
						m.Put(fmt.Sprintf("t%d-%d", id, i), int64(i))
					}
					barrier.Await() // phase 2: everyone reads everyone's keys
					for o := 0; o < workers; o++ {
						for i := 0; i < perWorker; i += 7 {
							if v, ok := m.Get(fmt.Sprintf("t%d-%d", o, i)); !ok || v != int64(i) {
								errs.Add(1)
							}
						}
					}
					barrier.Await() // phase 3: contended increments on shared keys
					for round := 0; round < bumps; round++ {
						for k := 0; k < shared; k++ {
							m.Compute(fmt.Sprintf("shared-%d", k), increment)
						}
					}
					barrier.Await() // phase 4: each goroutine removes its own keys
					for i := 0; i < perWorker; i++ {
						if _, had := m.Remove(fmt.Sprintf("t%d-%d", id, i)); !had {
							errs.Add(1)
						}
					}
				}()
			}
			done.Wait()
			check(t, "no reader saw a missing or wrong value, no remove missed", errs.Load() == 0)
			sumsExact := true
			for k := 0; k < shared; k++ {
				sumsExact = sumsExact && get(m, fmt.Sprintf("shared-%d", k)) == int64(workers*bumps)
			}
			check(t, "every shared key counted exactly workers x bumps", sumsExact)
			check(t, "size after removals equals the shared keys", m.Size() == shared)
		})
	}
}

func TestStripedSpecifics(t *testing.T) {
	one := mustStriped[int64](1)
	for i := 0; i < 200; i++ {
		one.Put(fmt.Sprintf("k%d", i), int64(i))
	}
	check(t, "single stripe resizes as it fills", one.TotalResizes() >= 5)
	ok := true
	for i := 0; i < 200; i++ {
		ok = ok && get(one, fmt.Sprintf("k%d", i)) == int64(i)
	}
	check(t, "entries survive segment resize", ok && one.Size() == 200)
	_, err := NewStripedHashMap[int64](12)
	check(t, "stripe count must be a power of two", errors.Is(err, ErrBadShape))
	sixteen := mustStriped[int64](16)
	inRange := true
	for i := 0; i < 1000; i++ {
		s := sixteen.SegmentIndex(fmt.Sprintf("acct-%d", i))
		inRange = inRange && s >= 0 && s < 16
	}
	check(t, "segmentIndex within [0, stripes)", inRange && sixteen.Stripes() == 16)
}

func TestLockFreeReadSpecifics(t *testing.T) {
	m := mustLockFree[int64](1, 2) // two buckets: every chain is long, every remove hits the middle
	for i := 0; i < 6; i++ {
		m.Put(fmt.Sprintf("k%d", i), int64(i))
	}
	check(t, "grows past 75% load", m.Buckets() > 2 && m.Resizes() >= 1)
	_ = m.ResizeTo(64)
	check(t, "explicit resize keeps every entry", m.Buckets() == 64 && m.Size() == 6 && get(m, "k3") == 3 && get(m, "k5") == 5)
	_ = m.ResizeTo(8)
	check(t, "resize to a smaller table is ignored", m.Buckets() == 64)
	chain := mustLockFree[int64](1, 1)
	chain.Put("a", 1)
	chain.Put("b", 2)
	chain.Put("c", 3)     // all three share bucket 0 until growth; 3 > 0.75 so it grows to 2
	_ = chain.ResizeTo(1) // ignored, but harmless
	rb, had := chain.Remove("b")
	_, bOk := chain.Get("b")
	check(t, "remove from a shared chain keeps the neighbours", had && rb == 2 && get(chain, "a") == 1 && get(chain, "c") == 3 && !bOk)
	v, _ := chain.Compute("a", func(_ string, v int64, _ bool) (int64, bool) { return v + 100, false })
	check(t, "compute replaces in place within the chain", v == 101 && get(chain, "c") == 3 && chain.Size() == 2)
	_, err := NewLockFreeReadMap[int64](16, 8)
	check(t, "buckets must be >= stripes", errors.Is(err, ErrBadShape))
	check(t, "resize rejects a non power of two", errors.Is(m.ResizeTo(96), ErrBadShape))
}

func TestCheckThenAct(t *testing.T) {
	check(t, "get-then-put: all 8 goroutines create the record (striped)", RaceGetThenPut(mustStriped[IdempotencyRecord](16), 8) == 8)
	check(t, "get-then-put: all 8 goroutines create the record (lock-free reads)", RaceGetThenPut(mustLockFree[IdempotencyRecord](16, 64), 8) == 8)
	check(t, "putIfAbsent: exactly one creator (striped)", RacePutIfAbsent(mustStriped[IdempotencyRecord](16), 8) == 1)
	check(t, "putIfAbsent: exactly one creator (lock-free reads)", RacePutIfAbsent(mustLockFree[IdempotencyRecord](16, 64), 8) == 1)
	check(t, "putIfAbsent: exactly one creator (global lock)", RacePutIfAbsent(NewGlobalLockMap[IdempotencyRecord](), 8) == 1)
}

// TestReadersNeverSeeTornState: readers run without locks while writers rebuild chains and resize; a reader must see
// every stable key every time. Under -race this also proves every shared read goes through an atomic.
func TestReadersNeverSeeTornState(t *testing.T) {
	m := mustLockFree[int64](4, 4)
	stableKeys := 64
	for i := 0; i < stableKeys; i++ {
		m.Put(fmt.Sprintf("stable-%d", i), int64(i))
	}
	var stop atomic.Bool
	var anomalies, reads atomic.Int64
	var readers, writers sync.WaitGroup
	for r := 0; r < 2; r++ {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for !stop.Load() {
				for i := 0; i < stableKeys; i++ {
					if v, ok := m.Get(fmt.Sprintf("stable-%d", i)); !ok || v != int64(i) {
						anomalies.Add(1)
					}
					reads.Add(1)
				}
			}
		}()
	}
	for w := 0; w < 2; w++ {
		id := w
		writers.Add(1)
		go func() {
			defer writers.Done()
			for i := 0; i < 20_000; i++ {
				k := fmt.Sprintf("churn-%d-%d", id, i%500)
				if i%3 == 2 {
					m.Remove(k)
				} else {
					m.Compute(k, increment)
				}
			}
		}()
	}
	writers.Wait()
	stop.Store(true)
	readers.Wait()
	check(t, "lock-free readers never saw a stable key missing or wrong", anomalies.Load() == 0)
	check(t, "readers made progress during writes", reads.Load() > int64(stableKeys))
	check(t, "table grew while readers were active", m.Buckets() > 4)
	stableIntact := true
	for i := 0; i < stableKeys; i++ {
		stableIntact = stableIntact && get(m, fmt.Sprintf("stable-%d", i)) == int64(i)
	}
	check(t, "stable keys intact after churn", stableIntact)
}
