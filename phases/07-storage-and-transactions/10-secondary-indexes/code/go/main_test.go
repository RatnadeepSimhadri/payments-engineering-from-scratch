// Tests for phases/07-storage-and-transactions/10-secondary-indexes. Run: go test ./...
package main

import (
	"errors"
	"slices"
	"testing"
)

func assert(t *testing.T, name string, condition bool) {
	t.Helper()
	if !condition {
		t.Errorf("%s", name)
	}
}

func pay(t *testing.T, id, customer string, cents, at int64) Payment {
	t.Helper()
	p, err := NewPayment(id, customer, "m_test", OfMinor(cents, USD), at)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func ids(r QueryResult) []string {
	out := []string{}
	for _, p := range r.Payments {
		out = append(out, p.ID)
	}
	return out
}

func newStore(t *testing.T, shards, parts int, mode IndexMode) *PartitionedStore {
	t.Helper()
	s, err := NewPartitionedStore(shards, parts, mode)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func loaded(t *testing.T, shards, parts int, mode IndexMode) *PartitionedStore {
	t.Helper()
	s := newStore(t, shards, parts, mode)
	for _, p := range SampleData() {
		s.Put(p)
	}
	return s
}

func TestHashing(t *testing.T) {
	assert(t, "fnv1a64 of empty string is the offset basis", uint64(Fnv1a64("")) == 0xcbf29ce484222325)
	assert(t, "fnv1a64 of 'a' matches the published vector", uint64(Fnv1a64("a")) == 0xaf63dc4c8601ec8c)
	assert(t, "partitionFor is stable", PartitionFor("pay_001", 4) == PartitionFor("pay_001", 4))
	assert(t, "partitionFor is in range for negative hashes", PartitionFor("pay_002", 4) >= 0 && PartitionFor("pay_002", 4) < 4)
	assert(t, "floorMod has the divisor's sign", floorMod(-7, 4) == 1 && floorMod(7, 4) == 3)
	// Java's Math.floorMod on the signed long, not the unsigned remainder: the two differ for negative hashes.
	h := Fnv1a64("cust_alice")
	assert(t, "sharding matches Java for a negative hash: floorMod, not the unsigned remainder",
		h < 0 && PartitionFor("cust_alice", 3) == 1 && int(uint64(h)%3) == 2)
	expected := map[string]int{"pay_001": 1, "pay_003": 3, "pay_005": 1, "pay_008": 2, "pay_011": 0, "pay_010": 3}
	for id, shard := range expected {
		if got := PartitionFor(id, 4); got != shard {
			t.Errorf("PartitionFor(%s, 4) = %d, want %d (Java placement)", id, got, shard)
		}
	}
}

func TestPrimaryKeyAccess(t *testing.T) {
	sync := loaded(t, 4, 2, Sync)
	before := sync.TotalShardRequests()
	p, ok := sync.Get("pay_005")
	assert(t, "get by primary key finds the row", ok && p.CustomerID == "cust_alice")
	assert(t, "get by primary key costs exactly one shard request", sync.TotalShardRequests()-before == 1)
	_, onShard := sync.Shard(sync.ShardFor("pay_005")).Get("pay_005")
	assert(t, "row lives on the shard the hash says", onShard)
	_, ok = sync.Get("pay_999")
	assert(t, "get of unknown id returns absent", !ok)
}

func TestBothStrategiesAgree(t *testing.T) {
	sync := loaded(t, 4, 2, Sync)
	scatter := sync.FindByCustomerScatterGather("cust_alice")
	global := sync.FindByCustomerGlobal("cust_alice")
	assert(t, "scatter-gather finds all five alice payments", len(scatter.Payments) == 5)
	assert(t, "global finds the same five", slices.Equal(global.Payments, scatter.Payments))
	assert(t, "results ordered by createdAt", slices.Equal(ids(scatter), []string{"pay_001", "pay_003", "pay_005", "pay_008", "pay_011"}))
	assert(t, "scatter-gather always costs N shard requests", scatter.ShardRequests == 4 && scatter.IndexRequests == 0)
	assert(t, "global costs one index request", global.IndexRequests == 1)
	assert(t, "global shard requests equal distinct shards holding hits", global.ShardRequests <= 4 && global.ShardRequests >= 1)

	erinScatter := sync.FindByCustomerScatterGather("cust_erin")
	erinGlobal := sync.FindByCustomerGlobal("cust_erin")
	assert(t, "single-payment customer: scatter still 4 requests", erinScatter.TotalRequests() == 4)
	assert(t, "single-payment customer: global is 2 requests", erinGlobal.TotalRequests() == 2 && len(erinGlobal.Payments) == 1)
	nobody := sync.FindByCustomerGlobal("cust_nobody")
	assert(t, "unknown customer: global returns empty with no shard fetch", len(nobody.Payments) == 0 && nobody.ShardRequests == 0)
	assert(t, "unknown customer: scatter returns empty", len(sync.FindByCustomerScatterGather("cust_nobody").Payments) == 0)

	assert(t, "SYNC: 12 puts produce 12 index-partition writes", sync.GlobalIndex().TotalRequests() >= 12)
	assert(t, "SYNC: nothing queued", sync.PendingIndexUpdates() == 0)
	assert(t, "SYNC: index audit is consistent", sync.AuditGlobalIndex().Consistent())
	assert(t, "SYNC: index has one entry per row", sync.GlobalIndex().EntryCount() == 12)
}

func TestAsyncLagAndStaleness(t *testing.T) {
	async := loaded(t, 4, 2, Async)
	assert(t, "ASYNC: no index writes during puts", async.GlobalIndex().TotalRequests() == 0)
	assert(t, "ASYNC: 12 updates queued", async.PendingIndexUpdates() == 12)
	assert(t, "ASYNC: global query before drain sees nothing", len(async.FindByCustomerGlobal("cust_alice").Payments) == 0)
	assert(t, "ASYNC: scatter-gather before drain sees everything", len(async.FindByCustomerScatterGather("cust_alice").Payments) == 5)
	assert(t, "ASYNC: audit reports 12 missing before drain", len(async.AuditGlobalIndex().Missing) == 12)
	assert(t, "ASYNC: partial drain applies in order", async.DrainIndexUpdates(3) == 3 && async.PendingIndexUpdates() == 9)
	assert(t, "ASYNC: drainAll applies the rest", async.DrainAll() == 9 && async.PendingIndexUpdates() == 0)
	assert(t, "ASYNC: after drain global matches scatter",
		slices.Equal(async.FindByCustomerGlobal("cust_alice").Payments, async.FindByCustomerScatterGather("cust_alice").Payments))
	assert(t, "ASYNC: audit consistent after drain", async.AuditGlobalIndex().Consistent())

	// staleness window on a fresh write
	async.Put(pay(t, "pay_013", "cust_erin", 1800, 13))
	_, ok := async.Get("pay_013")
	assert(t, "fresh row readable by primary key", ok)
	assert(t, "fresh row invisible to global index until drain", len(async.FindByCustomerGlobal("cust_erin").Payments) == 1)
	assert(t, "fresh row visible to local index immediately", len(async.FindByCustomerScatterGather("cust_erin").Payments) == 2)
	async.DrainAll()
	assert(t, "fresh row visible to global index after drain", len(async.FindByCustomerGlobal("cust_erin").Payments) == 2)

	// dangling entries: delete then query before the index hears about it
	assert(t, "delete returns true for existing row", async.Delete("pay_013"))
	assert(t, "delete returns false for missing row", !async.Delete("pay_013"))
	assert(t, "index still holds the locator before drain", len(async.GlobalIndex().Lookup("cust_erin")) == 2)
	assert(t, "audit sees one dangling entry", len(async.AuditGlobalIndex().Dangling) == 1)
	assert(t, "query filters the dangling locator", len(async.FindByCustomerGlobal("cust_erin").Payments) == 1)
	async.DrainAll()
	assert(t, "drain removes the dangling locator", len(async.GlobalIndex().Lookup("cust_erin")) == 1)
	stillThere := false
	for _, p := range async.Shard(async.ShardFor("pay_013")).LookupCustomer("cust_erin") {
		if p.ID == "pay_013" {
			stillThere = true
		}
	}
	assert(t, "local index drops the deleted row", !stillThere)
}

func TestReattribution(t *testing.T) {
	move := newStore(t, 2, 2, Async)
	move.Put(pay(t, "pay_100", "cust_x", 100, 1))
	move.DrainAll()
	move.Put(pay(t, "pay_100", "cust_y", 100, 1))
	assert(t, "re-attribution queues two updates", move.PendingIndexUpdates() == 2)
	assert(t, "before drain old customer still indexed (stale)", len(move.GlobalIndex().Lookup("cust_x")) == 1)
	audit := move.AuditGlobalIndex()
	assert(t, "before drain audit: 1 dangling, 1 missing", len(audit.Dangling) == 1 && len(audit.Missing) == 1)
	move.DrainAll()
	assert(t, "after drain old customer gone", len(move.GlobalIndex().Lookup("cust_x")) == 0)
	assert(t, "after drain new customer indexed", len(move.GlobalIndex().Lookup("cust_y")) == 1)
	assert(t, "local index also moved", len(move.FindByCustomerScatterGather("cust_x").Payments) == 0 &&
		len(move.FindByCustomerScatterGather("cust_y").Payments) == 1)

	s := newStore(t, 2, 2, Async)
	s.Put(pay(t, "pay_200", "cust_x", 100, 1))
	s.DrainAll()
	s.Put(pay(t, "pay_200", "cust_x", 250, 1))
	got, _ := s.Get("pay_200")
	assert(t, "same-customer overwrite queues nothing", s.PendingIndexUpdates() == 0 && got.Amount.MinorUnits == 250)
}

func TestRebuild(t *testing.T) {
	rebuild := loaded(t, 3, 2, Async)
	assert(t, "rebuild returns one entry per row", rebuild.RebuildGlobalIndex() == 12)
	assert(t, "rebuild discards the queue", rebuild.PendingIndexUpdates() == 0)
	assert(t, "rebuild yields a consistent index", rebuild.AuditGlobalIndex().Consistent())
	assert(t, "rebuild is idempotent", rebuild.RebuildGlobalIndex() == 12 && rebuild.GlobalIndex().EntryCount() == 12)

	gi := rebuild.GlobalIndex()
	p0 := gi.PartitionFor("cust_alice")
	reqBefore := gi.Requests(p0)
	gi.Lookup("cust_alice")
	assert(t, "customer lookup hits exactly its own partition", gi.Requests(p0) == reqBefore+1)
	assert(t, "lookup returns locators in (shard, id) order", slices.IsSortedFunc(gi.Lookup("cust_alice"), func(a, b Locator) int {
		if a.Less(b) {
			return -1
		}
		if b.Less(a) {
			return 1
		}
		return 0
	}))
}

func TestMoneyAndGuards(t *testing.T) {
	assert(t, "Money toString", OfMinor(12414, USD).String() == "124.14 USD")
	_, err := OfMinor(1, USD).Plus(OfMinor(1, EUR))
	assert(t, "Money refuses to mix currencies", errors.Is(err, ErrCurrencyMismatch))
	_, err = OfMinor(1<<62, USD).Plus(OfMinor(1<<62, USD))
	assert(t, "Money refuses to overflow", errors.Is(err, ErrOverflow))
	_, err = NewPartitionedStore(0, 1, Sync)
	assert(t, "store needs at least one shard", errors.Is(err, ErrNoShards))
	_, err = NewGlobalIndex(0)
	assert(t, "index needs at least one partition", errors.Is(err, ErrNoIndexPartition))
	_, err = NewPartitionedStore(1, 0, Sync)
	assert(t, "store propagates the index guard", errors.Is(err, ErrNoIndexPartition))
	_, err = NewPayment("p", "", "m", OfMinor(1, USD), 1)
	assert(t, "payment requires a customer", errors.Is(err, ErrMissingField))
}
