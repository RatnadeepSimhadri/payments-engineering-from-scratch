// Tests for phases/01-complexity-and-core-structures/06-hash-maps-from-scratch. Run: go test ./...
package main

import "testing"

func TestFNV1aVectors(t *testing.T) {
	if FNV1a("") != 0x811c9dc5 {
		t.Errorf("fnv1a(\"\") = %#x, want offset basis", FNV1a(""))
	}
	if FNV1a("a") != 0xe40c292c {
		t.Errorf("fnv1a(\"a\") = %#x, want 0xe40c292c", FNV1a("a"))
	}
	if FNV1a("idem-000001") != FNV1a("idem-000001") {
		t.Error("fnv1a must be deterministic")
	}
}

func TestIndexForStaysInTable(t *testing.T) {
	for _, capacity := range []int{8, 16, 1024} {
		if idx := IndexFor(0xdeadbeef, capacity); idx < 0 || idx >= capacity {
			t.Errorf("IndexFor out of range for capacity %d: %d", capacity, idx)
		}
	}
}

func TestNonPowerOfTwoCapacityPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic for capacity 12")
		}
	}()
	NewChainedHashMap[string, int](FNV1a, 12)
}

// sharedContract runs the same scenario against both tables; a learner switching implementations should notice nothing.
func sharedContract(t *testing.T, label string, m SimpleMap[string, int]) {
	t.Helper()
	t.Run(label+"/empty", func(t *testing.T) {
		if m.Size() != 0 {
			t.Errorf("size = %d, want 0", m.Size())
		}
		if _, ok := m.Get("nope"); ok {
			t.Error("Get on missing key reported ok")
		}
	})
	t.Run(label+"/put-get-replace-remove", func(t *testing.T) {
		if _, replaced := m.Put("a", 1); replaced {
			t.Error("first put reported a replacement")
		}
		if old, replaced := m.Put("a", 2); !replaced || old != 1 {
			t.Errorf("replace returned (%d, %v), want (1, true)", old, replaced)
		}
		if m.Size() != 1 {
			t.Errorf("replace changed size to %d", m.Size())
		}
		if v, ok := m.Get("a"); !ok || v != 2 {
			t.Errorf("Get(a) = (%d, %v), want (2, true)", v, ok)
		}
		if !m.ContainsKey("a") || m.ContainsKey("b") {
			t.Error("ContainsKey wrong")
		}
		if old, ok := m.Remove("a"); !ok || old != 2 {
			t.Errorf("Remove returned (%d, %v), want (2, true)", old, ok)
		}
		if _, ok := m.Remove("a"); ok {
			t.Error("second Remove reported ok")
		}
		if m.Size() != 0 {
			t.Errorf("size after remove = %d", m.Size())
		}
	})
	t.Run(label+"/resize-preserves-entries", func(t *testing.T) {
		ks := keys(1000)
		for i, k := range ks {
			m.Put(k, i)
		}
		if m.Size() != 1000 {
			t.Errorf("size = %d, want 1000", m.Size())
		}
		if m.Capacity() <= 8 {
			t.Errorf("capacity did not grow: %d", m.Capacity())
		}
		for i, k := range ks {
			if v, ok := m.Get(k); !ok || v != i {
				t.Fatalf("Get(%s) = (%d, %v), want (%d, true)", k, v, ok, i)
			}
		}
		for i := 0; i < len(ks); i += 3 {
			m.Remove(ks[i])
		}
		for i, k := range ks {
			v, ok := m.Get(k)
			if i%3 == 0 && ok {
				t.Fatalf("removed key %s still present", k)
			}
			if i%3 != 0 && (!ok || v != i) {
				t.Fatalf("kept key %s lost: (%d, %v)", k, v, ok)
			}
		}
		if m.Size() != 1000-334 {
			t.Errorf("size after removals = %d, want %d", m.Size(), 1000-334)
		}
	})
}

func TestChainedContract(t *testing.T) {
	sharedContract(t, "chained", NewChainedHashMap[string, int](FNV1a, DefaultCapacity))
}

func TestOpenAddressingContract(t *testing.T) {
	sharedContract(t, "open", NewOpenAddressingHashMap[string, int](FNV1a, DefaultCapacity))
}

func TestChainedResizesAtLoadFactor(t *testing.T) {
	m := NewChainedHashMap[string, int](FNV1a, DefaultCapacity)
	for i := 0; i < 7; i++ {
		m.Put(keys(7)[i], i)
	}
	if m.Resizes() != 1 || m.Capacity() != 16 {
		t.Errorf("after 7 keys: resizes=%d capacity=%d, want 1 and 16", m.Resizes(), m.Capacity())
	}
}

func TestOpenResizesAtLoadFactor(t *testing.T) {
	m := NewOpenAddressingHashMap[string, int](FNV1a, DefaultCapacity)
	for i := 0; i < 5; i++ {
		m.Put(keys(5)[i], i)
	}
	if m.Resizes() != 1 || m.Capacity() != 16 {
		t.Errorf("after 5 keys: resizes=%d capacity=%d, want 1 and 16", m.Resizes(), m.Capacity())
	}
}

func TestTombstones(t *testing.T) {
	m := NewOpenAddressingHashMap[string, string](ConstantHasher[string], 16)
	m.Put("A", "a")
	m.Put("B", "b")
	m.Put("C", "c")
	if m.SlotOf("A") != 0 || m.SlotOf("B") != 1 || m.SlotOf("C") != 2 {
		t.Fatalf("cluster slots = %d %d %d, want 0 1 2", m.SlotOf("A"), m.SlotOf("B"), m.SlotOf("C"))
	}
	m.Remove("B")
	if m.Tombstones() != 1 || m.Size() != 2 {
		t.Errorf("after remove: tombstones=%d size=%d", m.Tombstones(), m.Size())
	}
	if v, ok := m.Get("C"); !ok || v != "c" {
		t.Error("C must stay reachable through B's grave")
	}
	if _, ok := m.Get("B"); ok || m.ContainsKey("B") {
		t.Error("B must be gone")
	}
	m.Put("D", "d")
	if m.SlotOf("D") != 1 || m.Tombstones() != 0 {
		t.Errorf("D should reuse slot 1: slot=%d tombstones=%d", m.SlotOf("D"), m.Tombstones())
	}
	if m.Collisions() != 0+1+2+1 {
		t.Errorf("displacement = %d, want 4", m.Collisions())
	}
	m.Put("C", "c2")
	if v, _ := m.Get("C"); v != "c2" || m.SlotOf("C") != 2 || m.Size() != 3 {
		t.Error("replacing C must keep it in slot 2 without a duplicate")
	}
}

func TestResizeDropsTombstones(t *testing.T) {
	r := NewOpenAddressingHashMap[string, int](FNV1a, 8)
	ks := keys(4)
	for i, k := range ks {
		r.Put(k, i)
	}
	for _, k := range ks {
		r.Remove(k)
	}
	if r.Tombstones() != 4 || r.Size() != 0 {
		t.Fatalf("tombstones=%d size=%d, want 4 and 0", r.Tombstones(), r.Size())
	}
	r.Put("y0", 0) // 0 + 4 + 1 > 0.5 * 8 forces a resize
	r.Put("y1", 1)
	if r.Tombstones() != 0 || r.Capacity() != 16 {
		t.Errorf("after resize: tombstones=%d capacity=%d", r.Tombstones(), r.Capacity())
	}
	if v, ok := r.Get("y1"); !ok || v != 1 {
		t.Error("y1 lost across resize")
	}
}

func TestWorstCaseHasher(t *testing.T) {
	ks := keys(64)
	badChain := NewChainedHashMap[string, int](ConstantHasher[string], DefaultCapacity)
	badOpen := NewOpenAddressingHashMap[string, int](ConstantHasher[string], DefaultCapacity)
	for i, k := range ks {
		badChain.Put(k, i)
		badOpen.Put(k, i)
	}
	for i, k := range ks {
		if v, ok := badChain.Get(k); !ok || v != i {
			t.Fatalf("chaining lost %s", k)
		}
		if v, ok := badOpen.Get(k); !ok || v != i {
			t.Fatalf("probing lost %s", k)
		}
	}
	if badChain.Collisions() != 63 || badChain.MaxChainLength() != 64 {
		t.Errorf("chaining: collisions=%d maxChain=%d, want 63 and 64", badChain.Collisions(), badChain.MaxChainLength())
	}
	if badOpen.Collisions() != 2016 || badOpen.MaxProbeRun() != 64 {
		t.Errorf("probing: displacement=%d maxProbeRun=%d, want 2016 and 64", badOpen.Collisions(), badOpen.MaxProbeRun())
	}
	good := NewChainedHashMap[string, int](FNV1a, DefaultCapacity)
	for i, k := range ks {
		good.Put(k, i)
	}
	if good.Collisions() >= badChain.Collisions()/2 || good.MaxChainLength() > 4 {
		t.Errorf("good hash: collisions=%d maxChain=%d, expected far better than constant hash", good.Collisions(), good.MaxChainLength())
	}
}

func TestIdempotencyStoreRunsHandlerOnce(t *testing.T) {
	store := NewIdempotencyStore(NewOpenAddressingHashMap[string, StoredResponse](FNV1a, DefaultCapacity))
	runs := 0
	first := store.Handle("k1", func() StoredResponse { runs++; return StoredResponse{201, "created"} })
	second := store.Handle("k1", func() StoredResponse { runs++; return StoredResponse{500, "should not run"} })
	if first.Replayed || first.Response.Status != 201 {
		t.Errorf("first = %+v", first)
	}
	if !second.Replayed || second.Response != first.Response {
		t.Errorf("second = %+v, want replay of %+v", second, first.Response)
	}
	if runs != 1 || store.HandlerRuns() != 1 {
		t.Errorf("handler ran %d times (store says %d), want 1", runs, store.HandlerRuns())
	}
	other := store.Handle("k2", func() StoredResponse { return StoredResponse{201, "other"} })
	if other.Replayed || store.Size() != 2 {
		t.Errorf("different key must be fresh: %+v size=%d", other, store.Size())
	}
}
