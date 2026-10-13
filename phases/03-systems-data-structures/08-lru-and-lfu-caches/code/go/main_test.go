// Tests for phases/03-systems-data-structures/08-lru-and-lfu-caches. Run: go test ./...
package main

import (
	"errors"
	"math"
	"reflect"
	"testing"
)

func TestLruOrderingAndEviction(t *testing.T) {
	c := mustLru[string, string](3, nil)
	c.Put("a", "1")
	c.Put("b", "2")
	c.Put("c", "3")
	if !reflect.DeepEqual(c.Keys(), []string{"c", "b", "a"}) {
		t.Errorf("after puts: %v", c.Keys())
	}
	if v, ok := c.Get("a"); !ok || v != "1" {
		t.Errorf("Get(a) = %q, %v", v, ok)
	}
	if !reflect.DeepEqual(c.Keys(), []string{"a", "c", "b"}) {
		t.Errorf("after get: %v", c.Keys())
	}
	c.Put("d", "4")
	if !reflect.DeepEqual(c.Keys(), []string{"d", "a", "c"}) {
		t.Errorf("after eviction: %v", c.Keys())
	}
	if _, ok := c.Get("b"); ok || c.Evictions != 1 {
		t.Errorf("b should be evicted; evictions=%d", c.Evictions)
	}
}

func TestLruUpdateDoesNotEvict(t *testing.T) {
	c := mustLru[string, string](2, nil)
	c.Put("a", "1")
	c.Put("b", "2")
	c.Put("a", "one")
	if c.Len() != 2 || c.Evictions != 0 {
		t.Errorf("len=%d evictions=%d", c.Len(), c.Evictions)
	}
	if v, _ := c.Get("a"); v != "one" {
		t.Errorf("Get(a) = %q", v)
	}
	if !reflect.DeepEqual(c.Keys(), []string{"a", "b"}) {
		t.Errorf("keys %v", c.Keys())
	}
}

func TestLruCapacityNeverExceeded(t *testing.T) {
	c := mustLru[int, int](10, nil)
	for i := 0; i < 1000; i++ {
		c.Put(i, i)
		if c.Len() > 10 {
			t.Fatalf("len %d after %d puts", c.Len(), i+1)
		}
	}
	if !reflect.DeepEqual(c.Keys(), []int{999, 998, 997, 996, 995, 994, 993, 992, 991, 990}) || c.Evictions != 990 {
		t.Errorf("keys %v evictions %d", c.Keys(), c.Evictions)
	}
}

func TestLruRemoveAndEdgeCases(t *testing.T) {
	c := mustLru[string, string](2, nil)
	c.Put("a", "1")
	if !c.Remove("a") || c.Remove("a") || c.Len() != 0 {
		t.Errorf("remove semantics wrong")
	}
	one := mustLru[string, string](1, nil)
	one.Put("a", "1")
	one.Put("b", "2")
	if _, ok := one.Get("a"); ok {
		t.Errorf("capacity one kept a")
	}
	if v, _ := one.Get("b"); v != "2" {
		t.Errorf("capacity one lost b")
	}
	if _, err := NewLruCache[string, string](0, nil); err == nil {
		t.Errorf("capacity 0 accepted")
	}
	h := mustLru[string, string](2, nil)
	h.Put("a", "1")
	h.Get("a")
	h.Get("zz")
	if h.Hits != 1 || h.Misses != 1 || math.Abs(h.HitRate()-0.5) > 1e-9 {
		t.Errorf("counters hits=%d misses=%d rate=%f", h.Hits, h.Misses, h.HitRate())
	}
	if mustLru[string, string](2, nil).HitRate() != 0 {
		t.Errorf("empty hit rate")
	}
}

func TestTtlExpiresAtExactlyTtl(t *testing.T) {
	clock := NewManualClock(1000)
	c := mustLru[string, string](4, clock)
	_ = c.PutWithTTL("cfg", "v1", 5000)
	clock.Advance(4999)
	if v, ok := c.Get("cfg"); !ok || v != "v1" {
		t.Errorf("should be alive before expiry")
	}
	clock.Advance(1)
	if _, ok := c.Get("cfg"); ok {
		t.Errorf("should have expired")
	}
	if c.Expirations != 1 || c.Len() != 0 {
		t.Errorf("expirations=%d len=%d", c.Expirations, c.Len())
	}
}

func TestTtlExpiredEntryFreesSlot(t *testing.T) {
	clock := NewManualClock(0)
	c := mustLru[string, string](2, clock)
	_ = c.PutWithTTL("short", "x", 10)
	c.Put("long", "y")
	clock.Advance(10)
	if _, ok := c.Get("short"); ok {
		t.Errorf("short should be expired")
	}
	c.Put("new", "z")
	if v, _ := c.Get("long"); v != "y" || c.Evictions != 0 {
		t.Errorf("long lost or an eviction happened")
	}
}

func TestTtlLazyExpiryStillCountsTowardCapacity(t *testing.T) {
	// The failure the lesson warns about: an untouched dead entry is evicted as LRU, not expired.
	clock := NewManualClock(0)
	c := mustLru[string, string](2, clock)
	_ = c.PutWithTTL("dead", "x", 10)
	c.Put("live", "y")
	clock.Advance(10)
	c.Put("new", "z")
	if v, _ := c.Get("live"); v != "y" || c.Evictions != 1 || c.Expirations != 0 {
		t.Errorf("live=%q evictions=%d expirations=%d", v, c.Evictions, c.Expirations)
	}
	if err := c.PutWithTTL("a", "1", 0); err == nil {
		t.Errorf("ttl 0 accepted")
	}
}

func TestLfuEvictsLeastFrequent(t *testing.T) {
	c := mustLfu[string, string](2)
	c.Put("a", "1")
	c.Put("b", "2")
	c.Get("a")
	c.Put("c", "3")
	if _, ok := c.Get("b"); ok {
		t.Errorf("b should be evicted")
	}
	if v, _ := c.Get("a"); v != "1" {
		t.Errorf("a lost")
	}
	if v, _ := c.Get("c"); v != "3" {
		t.Errorf("c lost")
	}
	if c.Evictions != 1 {
		t.Errorf("evictions=%d", c.Evictions)
	}
}

func TestLfuTiesBreakLeastRecentlyUsed(t *testing.T) {
	c := mustLfu[string, string](2)
	c.Put("a", "1")
	c.Put("b", "2")
	c.Put("c", "3")
	if _, ok := c.Get("a"); ok {
		t.Errorf("a (older of the frequency-1 pair) should be evicted")
	}
	if v, _ := c.Get("b"); v != "2" {
		t.Errorf("b lost")
	}
}

func TestLfuFrequencyTracking(t *testing.T) {
	c := mustLfu[string, string](3)
	c.Put("a", "1")
	c.Put("a", "1")
	c.Put("a", "1")
	c.Put("b", "2")
	c.Get("b")
	c.Put("c", "3")
	if c.Frequency("a") != 3 || c.Frequency("b") != 2 || c.Frequency("c") != 1 || c.MinFrequency() != 1 {
		t.Errorf("freqs %d %d %d min %d", c.Frequency("a"), c.Frequency("b"), c.Frequency("c"), c.MinFrequency())
	}
	c.Get("c")
	if c.MinFrequency() != 2 || c.Frequency("missing") != 0 {
		t.Errorf("min %d missing %d", c.MinFrequency(), c.Frequency("missing"))
	}
	reset := mustLfu[string, string](2)
	reset.Put("a", "1")
	reset.Get("a")
	reset.Get("a")
	reset.Put("b", "2")
	reset.Get("b")
	if reset.MinFrequency() != 2 {
		t.Errorf("min before insert %d", reset.MinFrequency())
	}
	reset.Put("c", "3")
	if _, ok := reset.Get("b"); ok || reset.MinFrequency() != 1 {
		t.Errorf("insert should reset min to 1 and evict b")
	}
}

func TestLfuRemoveAndCapacity(t *testing.T) {
	c := mustLfu[string, string](2)
	c.Put("a", "1")
	c.Get("a")
	c.Put("b", "2")
	if !c.Remove("b") || c.Remove("b") || c.MinFrequency() != 2 || c.Len() != 1 {
		t.Errorf("remove: min %d len %d", c.MinFrequency(), c.Len())
	}
	if _, err := NewLfuCache[string, string](0); err == nil {
		t.Errorf("capacity 0 accepted")
	}
	big := mustLfu[int, int](10)
	for i := 0; i < 1000; i++ {
		big.Put(i, i)
		if big.Len() > 10 {
			t.Fatalf("len %d", big.Len())
		}
	}
}

func TestRngMatchesJavaAndPython(t *testing.T) {
	if got := NewRng(42).Next(); got != 0xBDD732262FEB6E95 {
		t.Errorf("Next = %#x", got)
	}
	r := NewRng(42)
	got := []int{r.NextInt(100), r.NextInt(100), r.NextInt(100), r.NextInt(100), r.NextInt(100)}
	if !reflect.DeepEqual(got, []int{51, 53, 71, 35, 65}) {
		t.Errorf("NextInt sequence %v", got)
	}
	if !reflect.DeepEqual(DefaultBinTrace(1, 6, 0), []string{"bin-8", "bin-12", "bin-1271", "bin-15", "bin-7", "bin-382"}) {
		t.Errorf("trace %v", DefaultBinTrace(1, 6, 0))
	}
}

func TestLfuSurvivesScanAndLruDoesNot(t *testing.T) {
	lru := mustLru[string, string](32, nil)
	lfu := mustLfu[string, string](32)
	for _, keys := range [][]string{DefaultBinTrace(1, 6000, 0), Scan(500)} {
		Replay(lru, keys)
		Replay(lfu, keys)
	}
	after := DefaultBinTrace(2, 2000, 0)[:200]
	lruAfter, lfuAfter := Replay(lru, after), Replay(lfu, after)
	if lfuAfter <= lruAfter+0.1 {
		t.Errorf("LFU %.3f should beat LRU %.3f by more than 0.1", lfuAfter, lruAfter)
	}
	if math.Abs(lruAfter-0.570) > 1e-3 || math.Abs(lfuAfter-0.725) > 1e-3 {
		t.Errorf("exact rates LRU %.4f LFU %.4f", lruAfter, lfuAfter)
	}
}

func TestLruAdaptsToShiftAndLfuLags(t *testing.T) {
	lru := mustLru[string, string](32, nil)
	lfu := mustLfu[string, string](32)
	Replay(lru, DefaultBinTrace(1, 6000, 0))
	Replay(lfu, DefaultBinTrace(1, 6000, 0))
	phase2 := DefaultBinTrace(3, 6000, 20)
	lruRate, lfuRate := Replay(lru, phase2), Replay(lfu, phase2)
	if lruRate <= 0.7 || lfuRate >= 0.5 {
		t.Errorf("LRU %.3f LFU %.3f", lruRate, lfuRate)
	}
}

func TestStaleCachedBalanceApprovesOverdraft(t *testing.T) {
	cache := mustLru[string, Money](4, nil)
	ledger := Money{10_000, "USD"}
	cache.Put("acct-7", ledger)
	ledger, _ = ledger.Minus(Money{8_000, "USD"})
	cached, _ := cache.Get("acct-7")
	if cached.String() != "100.00 USD" || ledger.String() != "20.00 USD" {
		t.Errorf("cached %s ledger %s", cached, ledger)
	}
	fromCache, _ := cached.Minus(Money{5_000, "USD"})
	fromLedger, _ := ledger.Minus(Money{5_000, "USD"})
	if fromCache.MinorUnits < 0 || fromLedger.MinorUnits >= 0 {
		t.Errorf("cache should approve (%d) and ledger decline (%d)", fromCache.MinorUnits, fromLedger.MinorUnits)
	}
	if _, err := ledger.Plus(Money{1, "EUR"}); !errors.Is(err, ErrCurrencyMismatch) {
		t.Errorf("currency mismatch not detected: %v", err)
	}
	if (Money{-30, "USD"}).String() != "-0.30 USD" {
		t.Errorf("negative formatting")
	}
}
