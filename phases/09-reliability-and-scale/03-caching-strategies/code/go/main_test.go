package main

import (
	"errors"
	"reflect"
	"strconv"
	"testing"
)

func usd(minor int64) Money { return OfMinor(minor, USD) }

func check(t *testing.T, name string, condition bool) {
	t.Helper()
	if !condition {
		t.Errorf("%s", name)
	}
}

func readIs(t *testing.T, c BalanceCache, account string, want Money) bool {
	t.Helper()
	got, ok := c.Read(account)
	return ok && got == want
}

func measuredIs(t *testing.T, c BalanceCache, account string, want Money) bool {
	t.Helper()
	got, ok := ReadMeasured(c, account)
	return ok && got == want
}

func peekIs(t *testing.T, s *BackingStore, account string, want Money) bool {
	t.Helper()
	got, ok := s.Peek(account)
	return ok && got == want
}

func TestTtlCache(t *testing.T) {
	clock := NewManualClock(1_000)
	ttl := NewTtlCache(clock)
	ttl.Put("a", usd(100), 500)
	got, ok := ttl.Get("a")
	check(t, "ttl: fresh entry is returned", ok && got == usd(100))
	clock.Advance(499)
	check(t, "ttl: still present one ms before expiry", ttl.Contains("a"))
	clock.Advance(1)
	_, ok = ttl.Get("a")
	check(t, "ttl: expired exactly at expiresAt", !ok)
	check(t, "ttl: expired entry removed on read", ttl.Size() == 0)
	ttl.Put("b", usd(1), NoExpiry)
	clock.Advance(1_000_000_000)
	check(t, "ttl: NO_EXPIRY never expires", ttl.Contains("b"))
	check(t, "ttl: invalidate reports presence", ttl.Invalidate("b") && !ttl.Invalidate("b"))

	t.Run("clock refuses to move backwards", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Error("Advance(-1) should panic")
			}
		}()
		clock.Advance(-1)
	})
}

func TestCacheAsideTtlOnly(t *testing.T) {
	c1 := NewManualClock(0)
	s1 := NewBackingStore(20, 25)
	s1.Write("acct", usd(10_000))
	ttlOnly := NewCacheAside(s1, c1, 30_000, false, 1)
	check(t, "cache-aside: first read misses and loads", readIs(t, ttlOnly, "acct", usd(10_000)) && ttlOnly.Stats().Misses == 1)
	check(t, "cache-aside: second read hits", readIs(t, ttlOnly, "acct", usd(10_000)) && ttlOnly.Stats().Hits == 1)
	s1.Write("acct", usd(2_000)) // out-of-band debit straight to the store
	check(t, "cache-aside TTL-only: read after external write is stale", measuredIs(t, ttlOnly, "acct", usd(10_000)))
	check(t, "cache-aside TTL-only: staleness is counted", ttlOnly.Stats().StaleReads == 1)
	c1.Advance(30_000)
	check(t, "cache-aside TTL-only: correct again after TTL", measuredIs(t, ttlOnly, "acct", usd(2_000)) && ttlOnly.Stats().StaleReads == 1)
}

func TestCacheAsideInvalidate(t *testing.T) {
	c2 := NewManualClock(0)
	s2 := NewBackingStore(20, 25)
	s2.Write("acct", usd(10_000))
	inval := NewCacheAside(s2, c2, 30_000, true, 1)
	inval.Read("acct")
	inval.Write("acct", usd(2_000))
	check(t, "cache-aside+invalidate: write removes the cached entry", !inval.Cache().Contains("acct"))
	check(t, "cache-aside+invalidate: read after write is fresh", measuredIs(t, inval, "acct", usd(2_000)) && inval.Stats().StaleReads == 0)
	check(t, "cache-aside+invalidate: that read was a miss", inval.Stats().Misses == 2)
}

func TestReadThrough(t *testing.T) {
	c3 := NewManualClock(0)
	s3 := NewBackingStore(20, 25)
	s3.Write("acct", usd(500))
	rt := NewReadThrough(s3, c3, 1_000, 1)
	check(t, "read-through: loads on miss", readIs(t, rt, "acct", usd(500)) && s3.Reads == 1)
	rt.Read("acct")
	check(t, "read-through: hit does not touch the store", s3.Reads == 1 && rt.Stats().Hits == 1)
	rt.Write("acct", usd(400))
	check(t, "read-through: write invalidates", !rt.Cache().Contains("acct") && readIs(t, rt, "acct", usd(400)))
	_, ok := rt.Read("nope")
	check(t, "read-through: missing account is absent and not cached", !ok && !rt.Cache().Contains("nope"))
}

func TestWriteThrough(t *testing.T) {
	c4 := NewManualClock(0)
	s4 := NewBackingStore(20, 25)
	wt := NewWriteThrough(s4, c4, 1_000, 1)
	wt.Write("acct", usd(777))
	check(t, "write-through: store and cache both updated", peekIs(t, s4, "acct", usd(777)) && wt.Cache().Contains("acct"))
	check(t, "write-through: read after write hits", readIs(t, wt, "acct", usd(777)) && wt.Stats().Hits == 1 && wt.Stats().Misses == 0)
	check(t, "write-through: every write costs a store write", s4.Writes == 1)
}

func TestWriteBehind(t *testing.T) {
	c5 := NewManualClock(0)
	s5 := NewBackingStore(20, 25)
	s5.Write("a", usd(100))
	s5.Write("b", usd(200))
	s5.Writes = 0
	wb := NewWriteBehind(s5, c5, 1)
	wb.Write("a", usd(90))
	wb.Write("b", usd(190))
	wb.Write("a", usd(80))
	check(t, "write-behind: cache sees the latest value", readIs(t, wb, "a", usd(80)))
	check(t, "write-behind: store not yet written", s5.Writes == 0 && peekIs(t, s5, "a", usd(100)))
	check(t, "write-behind: dirty count coalesces per key", wb.DirtyCount() == 2)
	check(t, "write-behind: read of dirty key is stale vs store", measuredIs(t, wb, "a", usd(80)) && wb.Stats().StaleReads == 1)
	flushed := wb.Flush()
	check(t, "write-behind: flush writes each dirty key once", flushed == 2 && s5.Writes == 2)
	check(t, "write-behind: flush writes the last value", peekIs(t, s5, "a", usd(80)) && peekIs(t, s5, "b", usd(190)))
	check(t, "write-behind: nothing dirty after flush", wb.DirtyCount() == 0)
	wb.Write("a", usd(70))
	wb.Crash()
	check(t, "write-behind: crash loses the dirty write", wb.LostWrites == 1 && peekIs(t, s5, "a", usd(80)))
	check(t, "write-behind: cache is empty after crash, read reloads old value", readIs(t, wb, "a", usd(80)))
}

func TestWorkload(t *testing.T) {
	ops1 := GenerateWorkload(42, 2_000, 100, 0.15, 0.2, 0.8)
	ops2 := GenerateWorkload(42, 2_000, 100, 0.15, 0.2, 0.8)
	check(t, "workload: same seed, same ops", reflect.DeepEqual(ops1, ops2))
	hotReads := 0
	for _, o := range ops1 {
		idx, err := strconv.Atoi(o.Account[5:])
		if err != nil {
			t.Fatal(err)
		}
		if idx < 20 {
			hotReads++
		}
	}
	check(t, "workload: hot 20% of accounts receive most ops", float64(hotReads) > float64(len(ops1))*0.7)

	w1 := NewManualClock(0)
	mTtl := NewCacheAside(SeededStore(100, 20, 25), w1, 5_000, false, 1)
	RunWorkload(mTtl, ops1, w1, 10)
	w2 := NewManualClock(0)
	mInv := NewCacheAside(SeededStore(100, 20, 25), w2, 5_000, true, 1)
	RunWorkload(mInv, ops1, w2, 10)
	w3 := NewManualClock(0)
	mWt := NewWriteThrough(SeededStore(100, 20, 25), w3, 5_000, 1)
	RunWorkload(mWt, ops1, w3, 10)
	check(t, "measured: TTL-only serves stale balances", mTtl.Stats().StaleReads > 0)
	check(t, "measured: invalidate-on-write serves none", mInv.Stats().StaleReads == 0)
	check(t, "measured: write-through serves none", mWt.Stats().StaleReads == 0)
	check(t, "measured: TTL-only has the highest hit rate (it never evicts on write)", mTtl.Stats().HitRate() >= mInv.Stats().HitRate())
	check(t, "measured: caching beats no cache on store reads", mInv.Store().Reads < int64(len(ops1)))
	check(t, "measured: write-through hit rate >= invalidating cache-aside", mWt.Stats().HitRate() >= mInv.Stats().HitRate())
}

func TestJavaRandomMatchesJava(t *testing.T) {
	// java.util.Random(42): first nextInt(100) values and first nextDouble are well-known reference values.
	r := NewJavaRandom(42)
	if got := r.NextInt(100); got != 30 {
		t.Errorf("first nextInt(100) with seed 42 = %d, want 30", got)
	}
	if got := r.NextInt(100); got != 63 {
		t.Errorf("second nextInt(100) with seed 42 = %d, want 63", got)
	}
	if got := r.NextDouble(); got != 0.6832234717598454 {
		t.Errorf("nextDouble with seed 42 = %v, want 0.6832234717598454", got)
	}
	// The full-size demo workload reproduces the Java measurement exactly.
	ops := GenerateWorkload(42, 20_000, 500, 0.15, 0.20, 0.80)
	w := NewManualClock(0)
	m := NewCacheAside(SeededStore(500, 20, 25), w, 5_000, false, 1)
	RunWorkload(m, ops, w, 10)
	check(t, "demo parity: 6433 store reads for TTL-only", m.Store().Reads == 6433)
	check(t, "demo parity: 4686 divergent reads for TTL-only", m.Stats().StaleReads == 4686)
}

func TestMoneyGuardRails(t *testing.T) {
	eur, err := NewCurrency("EUR", 2)
	if err != nil {
		t.Fatal(err)
	}
	_, err = usd(1).Plus(OfMinor(1, eur))
	check(t, "money: mixing currencies is an error", errors.Is(err, ErrCurrencyMismatch))
	check(t, "money: toString", usd(-7).String() == "-0.07 USD")
}
