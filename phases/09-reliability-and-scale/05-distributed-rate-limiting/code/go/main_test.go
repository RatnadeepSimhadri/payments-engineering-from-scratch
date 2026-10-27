package main

import (
	"errors"
	"testing"
)

func check(t *testing.T, name string, condition bool) {
	t.Helper()
	if !condition {
		t.Errorf("%s", name)
	}
}

func newBucket(t *testing.T, capacity, rate, now int64) *TokenBucket {
	t.Helper()
	b, err := NewTokenBucket(capacity, rate, now)
	if err != nil {
		t.Fatalf("NewTokenBucket: %v", err)
	}
	return b
}

func newLocal(t *testing.T, store *SharedStore, nodes int, capacity, rate, interval int64) *LocalSyncLimiter {
	t.Helper()
	l, err := NewLocalSyncLimiter(store, nodes, capacity, rate, interval)
	if err != nil {
		t.Fatalf("NewLocalSyncLimiter: %v", err)
	}
	return l
}

func TestTokenBucketArithmetic(t *testing.T) {
	clock := NewFakeClock(0)
	bucket := newBucket(t, 10, 5, clock.NowMillis())
	burst := 0
	for bucket.TryAcquire(1, clock.NowMillis()) {
		burst++
	}
	check(t, "new bucket admits exactly capacity as a burst", burst == 10)
	check(t, "empty bucket denies", !bucket.TryAcquire(1, clock.NowMillis()))
	clock.Advance(100)
	check(t, "5/s * 0.1 s = 0 whole tokens but 500 milli-tokens accrue", bucket.AvailableTokens(clock.NowMillis()) == 0)
	clock.Advance(100)
	check(t, "fractional refill accumulates to one whole token after 200 ms", bucket.AvailableTokens(clock.NowMillis()) == 1)
	clock.Advance(60_000)
	check(t, "refill clamps at capacity", bucket.AvailableTokens(clock.NowMillis()) == 10)
	check(t, "multi-token acquire", bucket.TryAcquire(10, clock.NowMillis()) && !bucket.TryAcquire(1, clock.NowMillis()))

	_, err := NewTokenBucket(0, 1, 0)
	check(t, "zero capacity rejected", err != nil)
	_, err = NewTokenBucket(1, -1, 0)
	check(t, "negative rate rejected", err != nil)
	_, err = NewTokenBucket(1<<62, 1, 0)
	check(t, "capacity whose milli-tokens overflow int64 is rejected with ErrOverflow", errors.Is(err, ErrOverflow))
	check(t, "negative rate rejected on SetRatePerSecond", bucket.SetRatePerSecond(-1) != nil)

	t.Run("clock cannot go backwards", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Error("Advance(-1) should panic")
			}
		}()
		NewFakeClock(0).Advance(-1)
	})
}

func TestCentralized(t *testing.T) {
	store := NewSharedStore(3)
	central := NewCentralizedLimiter(store, 100, 100)
	admitted := 0
	for i := 0; i < 400; i++ {
		if central.TryAcquire(i%4, "k", 0).Allowed {
			admitted++
		}
	}
	check(t, "centralized admits exactly capacity across four nodes at t=0", admitted == 100)
	check(t, "centralized charges one round trip per decision", central.TryAcquire(0, "k", 0).LatencyMillis == 3)
	check(t, "centralized store calls == decisions", store.Calls() == 401)
	admitted = 0
	for i := 0; i < 400; i++ {
		if central.TryAcquire(i%4, "k", 500).Allowed {
			admitted++
		}
	}
	check(t, "after 500 ms exactly 50 more tokens exist", admitted == 50)
	check(t, "keys are independent", central.TryAcquire(0, "other", 500).Allowed)
}

func TestSlidingWindowLog(t *testing.T) {
	logStore := NewSharedStore(1)
	log := NewSlidingWindowLogLimiter(logStore, 5, 1000)
	admitted := 0
	for tt := 0; tt < 10; tt++ {
		if log.TryAcquire(tt%2, "k", int64(tt)*100).Allowed {
			admitted++
		}
	}
	check(t, "sliding log admits exactly limit within the window", admitted == 5)
	check(t, "sliding log memory == limit entries", log.LogSize("k") == 5)
	check(t, "sliding log admits again once the oldest entry ages out", log.TryAcquire(0, "k", 1000).Allowed)
	check(t, "sliding log then denies: window [1, 1000] still holds 5", !log.TryAcquire(0, "k", 1000).Allowed)
}

func TestLocalSync(t *testing.T) {
	syncStore := NewSharedStore(2)
	local := newLocal(t, syncStore, 4, 100, 100, 1000)
	check(t, "local decision costs no round trip", local.TryAcquire(0, "k", 0).LatencyMillis == 0)
	admitted := 0
	for i := 0; i < 200; i++ {
		if local.TryAcquire(0, "k", 0).Allowed {
			admitted++
		}
	}
	check(t, "hot node is capped at its 1/N share (25 tokens, one already spent) before the first sync", admitted == 24)
	check(t, "cold nodes still hold their unused share", local.TryAcquire(1, "k", 0).Allowed)
	for i := 0; i < 50; i++ {
		local.TryAcquire(0, "k", 999)
	}
	check(t, "no sync before the interval elapses", local.Syncs() == 0)
	local.TryAcquire(0, "k", 1000)
	check(t, "sync fires once the interval elapses", local.Syncs() == 1)
	check(t, "after sync the hot node's share grew", local.RateOf("k", 0) > 25)
	check(t, "after sync the cold nodes keep the floor share", local.RateOf("k", 2) == 6)
	var total int64
	for n := 0; n < 4; n++ {
		total += local.RateOf("k", n)
	}
	check(t, "shares never exceed the global rate", total <= 100)

	_, err := NewLocalSyncLimiter(syncStore, 0, 100, 100, 1000)
	check(t, "zero nodes rejected", err != nil)
}

func TestSeededLoad(t *testing.T) {
	even := Compare(400, 0.25, 7)
	skew := Compare(400, 0.85, 7)
	centralEven, localEven, logEven := even[0], even[1], even[3]
	centralSkew, localSlow, localFast, logSkew := skew[0], skew[1], skew[2], skew[3]
	check(t, "centralized never admits more than rate + burst in any second", centralEven.PeakPerSecond() <= Rate+Capacity)
	check(t, "sliding log never admits more than the limit in any second", logEven.PeakPerSecond() <= Rate && logSkew.PeakPerSecond() <= Rate)
	vs := centralEven.VersusIdeal(Rate, Capacity)
	check(t, "centralized is within one request of ideal", vs >= -1 && vs <= 1)
	check(t, "local+sync under-admits under skew", localSlow.Admitted < centralSkew.Admitted)
	check(t, "faster sync narrows the gap", localFast.Admitted > localSlow.Admitted)
	check(t, "faster sync costs more coordination", localFast.StoreCalls > localSlow.StoreCalls)
	check(t, "local+sync is cheaper per request than centralized", localEven.MeanLatencyMillis() < centralEven.MeanLatencyMillis())
	check(t, "all strategies see the same offered load for the same seed", centralEven.Offered == localEven.Offered && localEven.Offered == logEven.Offered)
	again := Compare(400, 0.85, 7)
	check(t, "simulation is deterministic for a seed", again[1].Admitted == localSlow.Admitted && again[0].Rejected == centralSkew.Rejected)
	light := Compare(60, 0.25, 7)
	allZero := true
	for _, r := range light {
		allZero = allZero && r.Rejected == 0
	}
	check(t, "light load is never rejected by any strategy", allZero)

	// Demo parity with the Java tables (java.util.Random, seed 7).
	check(t, "parity: even load offers 3893 and centralized admits 1099", centralEven.Offered == 3893 && centralEven.Admitted == 1099)
	check(t, "parity: skewed load offers 4024, local+sync(1000ms) admits 1052 with peak 131", localSlow.Offered == 4024 && localSlow.Admitted == 1052 && localSlow.PeakPerSecond() == 131)
}
