// Tests for phases/03-systems-data-structures/06-consistent-hashing. Run: go test ./...
package main

import (
	"errors"
	"reflect"
	"sort"
	"testing"
)

var shards = func() []string {
	var s []string
	for c := 'a'; c <= 'j'; c++ {
		s = append(s, "ledger-shard-"+string(c))
	}
	return s
}()

func TestHashKeyMatchesJavaAndPython(t *testing.T) {
	if got := HashKey("acct-000123"); got != 0xd43cbef4d0200057 {
		t.Errorf("HashKey = %#x", got)
	}
	if got := PointHash("ledger-shard-a", 0); got != 0xfb3be259e2c64489 {
		t.Errorf("PointHash = %#x", got)
	}
}

func TestLookupMatchesJavaAndPython(t *testing.T) {
	ring := ringWith(shards, 100)
	if owner, _ := ring.Lookup("acct-000123"); owner != "ledger-shard-c" {
		t.Errorf("Lookup = %s", owner)
	}
	prefs, _ := ring.LookupN("acct-000123", 3)
	if !reflect.DeepEqual(prefs, []string{"ledger-shard-c", "ledger-shard-d", "ledger-shard-e"}) {
		t.Errorf("LookupN = %v", prefs)
	}
	small := ringWith([]string{"alpha", "beta", "gamma"}, 3)
	if owner, _ := small.Lookup("acct-000001"); owner != "beta" {
		t.Errorf("small Lookup = %s", owner)
	}
	if p, _ := small.LookupN("acct-000001", 3); !reflect.DeepEqual(p, []string{"beta", "alpha", "gamma"}) {
		t.Errorf("small LookupN = %v", p)
	}
}

func TestRingStructure(t *testing.T) {
	three := ringWith(shards[:3], 50)
	if three.PointCount() != 150 {
		t.Errorf("PointCount = %d", three.PointCount())
	}
	for i := 1; i < three.PointCount(); i++ {
		if three.HashAt(i-1) > three.HashAt(i) {
			t.Fatalf("points not sorted at %d", i)
		}
	}
	if !reflect.DeepEqual(three.Nodes(), shards[:3]) {
		t.Errorf("Nodes = %v", three.Nodes())
	}
}

func TestErrors(t *testing.T) {
	three := ringWith(shards[:3], 50)
	if err := three.AddNode("ledger-shard-a"); !errors.Is(err, ErrNodeExists) {
		t.Errorf("duplicate add: %v", err)
	}
	if err := three.RemoveNode("ledger-shard-z"); !errors.Is(err, ErrNodeMissing) {
		t.Errorf("missing remove: %v", err)
	}
	if _, err := NewHashRing(0); !errors.Is(err, ErrBadParameter) {
		t.Errorf("zero vnodes: %v", err)
	}
	if _, err := mustRing(100).Lookup("acct-000001"); !errors.Is(err, ErrEmptyRing) {
		t.Errorf("empty lookup: %v", err)
	}
	if _, err := mustRing(100).LookupN("acct-000001", 2); !errors.Is(err, ErrEmptyRing) {
		t.Errorf("empty lookupN: %v", err)
	}
	if _, err := three.LookupN("acct-000001", 0); !errors.Is(err, ErrBadParameter) {
		t.Errorf("n = 0: %v", err)
	}
}

func TestSingleNodeOwnsEverything(t *testing.T) {
	only := ringWith([]string{"only"}, 100)
	for _, k := range AccountIDs(200) {
		if owner, _ := only.Lookup(k); owner != "only" {
			t.Fatalf("%s owned by %s", k, owner)
		}
	}
}

func TestWrapAround(t *testing.T) {
	ring := ringWith(shards, 100)
	top := ring.HashAt(ring.PointCount() - 1)
	wrapped := 0
	for _, k := range AccountIDs(20_000) {
		if HashKey(k) > top {
			wrapped++
			if owner, _ := ring.Lookup(k); owner != ring.OwnerAt(0) {
				t.Fatalf("%s should wrap to the first point", k)
			}
		}
	}
	if wrapped == 0 {
		t.Errorf("expected some keys above the highest point")
	}
}

func TestAddingANodeOnlyMovesKeysToIt(t *testing.T) {
	keys := AccountIDs(5_000)
	ring := ringWith(shards, 100)
	before := ring.Assign(keys)
	_ = ring.AddNode("ledger-shard-k")
	after := ring.Assign(keys)
	for _, k := range keys {
		if after[k] != before[k] && after[k] != "ledger-shard-k" {
			t.Fatalf("%s moved from %s to %s", k, before[k], after[k])
		}
	}
}

func TestRemovingANodeOnlyMovesItsKeys(t *testing.T) {
	keys := AccountIDs(5_000)
	ring := ringWith(shards, 100)
	before := ring.Assign(keys)
	_ = ring.RemoveNode("ledger-shard-c")
	after := ring.Assign(keys)
	for _, k := range keys {
		if before[k] == "ledger-shard-c" {
			if after[k] == "ledger-shard-c" {
				t.Fatalf("%s still on removed node", k)
			}
		} else if after[k] != before[k] {
			t.Fatalf("%s moved from %s to %s", k, before[k], after[k])
		}
	}
}

func TestFractionMoved(t *testing.T) {
	keys := AccountIDs(20_000)
	ring := ringWith(shards, 100)
	before := ring.Assign(keys)
	_ = ring.AddNode("ledger-shard-k")
	moved := FractionMoved(before, ring.Assign(keys))
	if moved < 0.05 || moved > 0.14 {
		t.Errorf("ring moved %.3f, expected about 1/11", moved)
	}
	ten, _ := NewModNSharder(shards)
	eleven, _ := NewModNSharder(append(append([]string(nil), shards...), "ledger-shard-k"))
	if m := FractionMoved(ten.Assign(keys), eleven.Assign(keys)); m < 0.85 {
		t.Errorf("mod-N moved only %.3f", m)
	}
	if FractionMoved(before, before) != 0 || FractionMoved(map[string]string{}, map[string]string{}) != 0 {
		t.Errorf("identical or empty assignments should move nothing")
	}
}

func TestVirtualNodesImproveBalance(t *testing.T) {
	keys := AccountIDs(20_000)
	oneRatio, oneCV := Imbalance(ringWith(shards, 1).Assign(keys), shards)
	hundredRatio, hundredCV := Imbalance(ringWith(shards, 100).Assign(keys), shards)
	if oneRatio <= 2.0 {
		t.Errorf("one vnode: max/mean = %.3f, expected > 2", oneRatio)
	}
	if hundredRatio >= 1.25 {
		t.Errorf("100 vnodes: max/mean = %.3f, expected < 1.25", hundredRatio)
	}
	if hundredCV >= oneCV {
		t.Errorf("CV did not fall: %.3f vs %.3f", hundredCV, oneCV)
	}
	counts := LoadPerNode(ringWith(shards, 100).Assign(keys), shards)
	total := 0
	for _, c := range counts {
		total += c
		if c == 0 {
			t.Errorf("a node got no load")
		}
	}
	if total != 20_000 {
		t.Errorf("total load %d", total)
	}
}

func TestLookupNReplication(t *testing.T) {
	ring := ringWith(shards, 100)
	for _, k := range AccountIDs(300) {
		prefs, _ := ring.LookupN(k, 3)
		owner, _ := ring.Lookup(k)
		if len(prefs) != 3 || prefs[0] != owner {
			t.Fatalf("prefs for %s: %v", k, prefs)
		}
		if prefs[0] == prefs[1] || prefs[1] == prefs[2] || prefs[0] == prefs[2] {
			t.Fatalf("prefs not distinct for %s: %v", k, prefs)
		}
	}
	three := ringWith(shards[:3], 50)
	all, _ := three.LookupN("acct-000001", 8)
	sort.Strings(all)
	if !reflect.DeepEqual(all, shards[:3]) {
		t.Errorf("LookupN with n > nodes = %v", all)
	}
	fail := ringWith(shards, 100)
	prefs, _ := fail.LookupN("acct-000123", 3)
	_ = fail.RemoveNode(prefs[0])
	if owner, _ := fail.Lookup("acct-000123"); owner != prefs[1] {
		t.Errorf("after losing the owner, got %s, expected first replica %s", owner, prefs[1])
	}
}
