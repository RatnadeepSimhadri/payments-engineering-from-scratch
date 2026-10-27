// Tests for phases/07-storage-and-transactions/01-partitioning-and-rebalancing. Run: go test ./...
package main

import (
	"errors"
	"strconv"
	"strings"
	"testing"
)

var testKeys = PaymentIds(20_000)

func assert(t *testing.T, name string, condition bool) {
	t.Helper()
	if !condition {
		t.Errorf("%s", name)
	}
}

func hashP(t *testing.T, n int) *HashPartitioner {
	t.Helper()
	p, err := NewHashPartitioner(n)
	if err != nil {
		t.Fatalf("NewHashPartitioner(%d): %v", n, err)
	}
	return p
}

func newTable(t *testing.T, partitions int, nodes ...string) *RoutingTable {
	t.Helper()
	table, err := NewRoutingTable(hashP(t, partitions), nodes)
	if err != nil {
		t.Fatalf("NewRoutingTable: %v", err)
	}
	return table
}

func TestRangePartitioner(t *testing.T) {
	rng, err := NewRangePartitioner([]string{"f", "m", "s"})
	if err != nil {
		t.Fatal(err)
	}
	assert(t, "range: 4 partitions from 3 split points", rng.PartitionCount() == 4)
	cases := []struct {
		key  string
		want int
	}{{"acme", 0}, {"f", 1}, {"megamart", 2}, {"zenith", 3}}
	for _, c := range cases {
		t.Run(c.key, func(t *testing.T) {
			if got := rng.PartitionOf(c.key); got != c.want {
				t.Errorf("PartitionOf(%q) = %d, want %d (half-open boundaries)", c.key, got, c.want)
			}
		})
	}
	assert(t, "range: preserves order", rng.PartitionOf("alpha") <= rng.PartitionOf("omega"))
	_, err = NewRangePartitioner([]string{"m", "f"})
	assert(t, "range: rejects unsorted split points", errors.Is(err, ErrSplitPointsUnsorted))
}

func TestHashPartitioner(t *testing.T) {
	hash := hashP(t, 16)
	assert(t, "hash: deterministic", hash.PartitionOf("pay_1") == hash.PartitionOf("pay_1"))
	assert(t, "hash: fnv1a of empty string is offset basis", uint64(Fnv1a64("")) == 0xcbf29ce484222325)
	inRange := true
	spread := NewHotKeyDetector(16)
	for _, k := range testKeys {
		p := hash.PartitionOf(k)
		if p < 0 || p >= 16 {
			inRange = false
		}
		spread.Record(p)
	}
	assert(t, "hash: every partition in [0,16)", inRange)
	assert(t, "hash: 20k keys over 16 partitions skew < 1.2x", spread.Skew() < 1.2)
	_, err := NewHashPartitioner(0)
	assert(t, "hash: rejects zero partitions", errors.Is(err, ErrPartitionCount))
}

func TestHotKeyAndSalt(t *testing.T) {
	hash := hashP(t, 16)
	hot := NewHotKeyDetector(16)
	for i := 0; i < 1000; i++ {
		hot.Record(hash.PartitionOf("megamart"))
	}
	assert(t, "hot key: one key sends everything to one partition, skew 16x", hot.Skew() == 16.0)
	salted := NewHotKeyDetector(16)
	for i := 0; i < 1000; i++ {
		salted.Record(hash.PartitionOf(Salt("megamart", "pay_"+strconv.Itoa(i), 8)))
	}
	touched := 0
	for p := 0; p < 16; p++ {
		if salted.Hits(p) > 0 {
			touched++
		}
	}
	assert(t, "hot key: salt x8 spreads over at least 4 partitions and cuts skew in half", touched >= 4 && salted.Skew() < 8.0)
	assert(t, "salt: deterministic for the same secondary", Salt("m", "x", 8) == Salt("m", "x", 8))
	assert(t, "salt: keeps the logical key as prefix", strings.HasPrefix(Salt("megamart", "pay_1", 8), "megamart#"))
	assert(t, "skew: empty detector reports 1.0", NewHotKeyDetector(4).Skew() == 1.0)
}

func TestRoutingTable(t *testing.T) {
	table := newTable(t, 8, "a", "b")
	assert(t, "table: round robin owners", table.OwnerOf(0) == "a" && table.OwnerOf(1) == "b" && table.OwnerOf(7) == "b")
	assert(t, "table: starts at version 1", table.Version() == 1)
	table.Assign(0, "b")
	assert(t, "table: assign bumps version", table.Version() == 2 && table.OwnerOf(0) == "b")
	table.Assign(0, "b")
	assert(t, "table: no-op assign does not bump version", table.Version() == 2)
	assert(t, "table: nodeFor agrees with partitioner", table.NodeFor("pay_42") == table.OwnerOf(table.Partitioner().PartitionOf("pay_42")))
	_, err := NewRoutingTable(hashP(t, 4), nil)
	assert(t, "table: rejects empty node list", errors.Is(err, ErrNoNodes))
}

func TestRebalancer(t *testing.T) {
	var rebalancer Rebalancer
	fixed := newTable(t, 64, "a", "b", "c", "d")
	before := map[string]string{}
	for _, k := range testKeys {
		before[k] = fixed.NodeFor(k)
	}
	plan, err := rebalancer.PlanAddNode(fixed, "e")
	if err != nil {
		t.Fatal(err)
	}
	assert(t, "rebalance add: moves exactly floor(64/5)=12 partitions", len(plan) == 12)
	allToE := true
	for _, m := range plan {
		if m.To != "e" {
			allToE = false
		}
	}
	assert(t, "rebalance add: every move targets the new node", allToE)
	rebalancer.Apply(fixed, plan)
	load := fixed.PartitionsPerNode()
	max, min := 0, 1<<30
	for _, c := range load {
		if c > max {
			max = c
		}
		if c < min {
			min = c
		}
	}
	assert(t, "rebalance add: max-min load <= 1", max-min <= 1)
	moved := MovedFraction(testKeys, func(k string) string { return before[k] }, fixed.NodeFor)
	assert(t, "rebalance add: about 12/64 of keys moved", moved > 0.15 && moved < 0.23)
	stayed := true
	for _, k := range testKeys {
		if fixed.NodeFor(k) != "e" && before[k] != fixed.NodeFor(k) {
			stayed = false
		}
	}
	assert(t, "rebalance add: keys not in moved partitions stayed put", stayed)
	_, err = rebalancer.PlanAddNode(fixed, "a")
	assert(t, "rebalance add: rejects duplicate node", errors.Is(err, ErrNodePresent))

	removal, err := rebalancer.PlanRemoveNode(fixed, "e")
	if err != nil {
		t.Fatal(err)
	}
	allFromE := true
	for _, m := range removal {
		if m.From != "e" {
			allFromE = false
		}
	}
	assert(t, "rebalance remove: moves exactly the leaving node's partitions", len(removal) == 12 && allFromE)
	rebalancer.Apply(fixed, removal)
	assert(t, "rebalance remove: node gone, 4 nodes remain", len(fixed.Nodes()) == 4 && !contains(fixed.Nodes(), "e"))
	_, err = rebalancer.PlanRemoveNode(newTable(t, 4, "only"), "only")
	assert(t, "rebalance remove: cannot remove last node", errors.Is(err, ErrLastNode))
	_, err = rebalancer.PlanRemoveNode(fixed, "zzz")
	assert(t, "rebalance remove: rejects unknown node", errors.Is(err, ErrNodeAbsent))
}

func TestModNVersusRing(t *testing.T) {
	mod4, mod5 := hashP(t, 4), hashP(t, 5)
	naive := MovedFraction(testKeys,
		func(k string) string { return strconv.Itoa(mod4.PartitionOf(k)) },
		func(k string) string { return strconv.Itoa(mod5.PartitionOf(k)) })
	assert(t, "mod-N resize moves most keys (> 70%)", naive > 0.70)

	ring, err := NewConsistentHashRing(150)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"a", "b", "c", "d"} {
		ring.AddNode(n)
	}
	assert(t, "ring: 4 nodes x 150 vnodes", ring.Size() == 600)
	ringBefore := map[string]string{}
	for _, k := range testKeys {
		ringBefore[k] = ring.NodeFor(k)
	}
	ring.AddNode("e")
	ringMoved := MovedFraction(testKeys, func(k string) string { return ringBefore[k] }, ring.NodeFor)
	assert(t, "ring: adding 5th node moves roughly 1/5 of keys", ringMoved > 0.14 && ringMoved < 0.27)
	movedToE := true
	for _, k := range testKeys {
		if ringBefore[k] != ring.NodeFor(k) && ring.NodeFor(k) != "e" {
			movedToE = false
		}
	}
	assert(t, "ring: keys that moved all moved to the new node", movedToE)
	ring.RemoveNode("e")
	restored := true
	for _, k := range testKeys {
		if ringBefore[k] != ring.NodeFor(k) {
			restored = false
		}
	}
	assert(t, "ring: removing the node restores every key's owner", restored)
	assert(t, "ring: removing the node frees its vnodes", ring.Size() == 600)
	_, err = NewConsistentHashRing(0)
	assert(t, "ring: rejects zero virtual nodes", errors.Is(err, ErrVirtualNodes))
}

func TestLiveMigration(t *testing.T) {
	t2 := newTable(t, 8, "a", "b")
	a, e := NewStorageNode("a", t2), NewStorageNode("e", t2)
	var p7, other []string
	for _, k := range testKeys {
		if t2.Partitioner().PartitionOf(k) == 6 {
			p7 = append(p7, k)
		} else if other == nil {
			other = append(other, k)
		}
	}
	for _, k := range p7[:100] {
		if err := a.Put(k, "v1"); err != nil {
			t.Fatal(err)
		}
	}
	var wrong *WrongOwnerError
	assert(t, "node refuses writes for partitions it does not own", errors.As(e.Put(p7[0], "x"), &wrong))
	mig, err := NewPartitionMigration(6, a, e, t2)
	if err != nil {
		t.Fatal(err)
	}
	assert(t, "migration starts PLANNED", mig.State() == Planned)
	assert(t, "migration: cannot cut over before copying", errors.Is(mig.CutOver(), ErrWrongState))
	assert(t, "migration: pre-copy write accepted", mig.Write(p7[100], "pre-copy") == nil)
	assert(t, "migration: startCopy succeeds", mig.StartCopy() == nil)
	assert(t, "migration: copied all 101 keys", mig.Copied == 101 && mig.State() == DualWrite)
	assert(t, "migration: dual write during accepted", mig.Write(p7[101], "during") == nil)
	assert(t, "migration: dual write update accepted", mig.Write(p7[0], "v1-updated") == nil)
	assert(t, "migration: dual writes counted", mig.DualWrites == 2)
	got, err := a.Get(p7[0])
	assert(t, "migration: reads before cutover still served by source", err == nil && got == "v1-updated")
	assert(t, "migration: rejects key outside the partition", errors.Is(mig.Write(other[0], "x"), ErrKeyNotInPartition))
	vBefore := t2.Version()
	assert(t, "migration: cutOver succeeds", mig.CutOver() == nil)
	assert(t, "migration: cutover bumps routing version and flips owner", t2.Version() == vBefore+1 && t2.OwnerOf(6) == "e")
	_, err = a.Get(p7[0])
	assert(t, "migration: stale client at source gets WrongOwner", errors.As(err, &wrong))
	if wrong != nil {
		assert(t, "migration: WrongOwner carries current version", wrong.CurrentVersion == t2.Version() && wrong.Partition == 6)
		assert(t, "migration: WrongOwner message names the version", strings.Contains(wrong.Error(), "version 2"))
	}
	v100, _ := e.Get(p7[100])
	v101, _ := e.Get(p7[101])
	v0, _ := e.Get(p7[0])
	assert(t, "migration: destination has the pre-copy, during, and updated values", v100 == "pre-copy" && v101 == "during" && v0 == "v1-updated")
	assert(t, "migration: post-cutover write accepted", mig.Write(p7[102], "post-cutover") == nil)
	v102, _ := e.Get(p7[102])
	assert(t, "migration: post-cutover writes go to destination only", v102 == "post-cutover" && a.Size() == 102)
	dropped, err := mig.DropSource()
	assert(t, "migration: drop removes exactly the old copy", err == nil && dropped == 102 && a.Size() == 0 && mig.State() == Done)
	assert(t, "migration: destination ends with 103 keys", e.Size() == 103)
	_, err = NewPartitionMigration(6, a, e, t2)
	assert(t, "migration: constructor checks source owns partition", errors.Is(err, ErrNotOwner))
	_, err = mig.DropSource()
	assert(t, "migration: cannot drop twice", errors.Is(err, ErrWrongState))
}
