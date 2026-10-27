package main

import (
	"reflect"
	"testing"
)

func usd(minor int64) Money { return OfMinor(minor, USD) }

func check(t *testing.T, name string, condition bool) {
	t.Helper()
	if !condition {
		t.Errorf("%s", name)
	}
}

func allEqual(values []Money, want Money) bool {
	for _, v := range values {
		if v != want {
			return false
		}
	}
	return true
}

// burst: N clients ask for the same cold key at the same instant. Returns the strategy after the simulation drains.
func burst(factory Factory, clients int, storeLatency int64, received *[]Money) Strategy {
	sim := NewSimulator()
	store := NewBackingStore(sim, storeLatency)
	store.Write("k", usd(100))
	s := factory(sim, store)
	for i := 0; i < clients; i++ {
		sim.Schedule(0, func() { s.Get("k", func(v Money) { *received = append(*received, v) }) })
	}
	sim.Run()
	return s
}

func naiveFactory(ttl int64) Factory {
	return func(sim *Simulator, st *BackingStore) Strategy { return NewNaiveStrategy(sim, st, ttl) }
}

func singleFlightFactory(ttl int64) Factory {
	return func(sim *Simulator, st *BackingStore) Strategy { return NewSingleFlightStrategy(sim, st, ttl) }
}

func earlyFactory(ttl int64, beta float64, seed int64) Factory {
	return func(sim *Simulator, st *BackingStore) Strategy {
		return NewEarlyExpirationStrategy(sim, st, ttl, beta, seed)
	}
}

func TestSimulator(t *testing.T) {
	sim := NewSimulator()
	var order []string
	sim.Schedule(50, func() { order = append(order, "b") })
	sim.Schedule(10, func() { order = append(order, "a") })
	sim.Schedule(50, func() { order = append(order, "c") })
	sim.Schedule(10, func() { sim.After(5, func() { order = append(order, "a2") }) })
	sim.Run()
	check(t, "simulator runs events in time order, ties in scheduling order", reflect.DeepEqual(order, []string{"a", "a2", "b", "c"}))
	check(t, "simulator clock stops at the last event", sim.Now() == 50)
	t.Run("simulator refuses to schedule in the past", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Error("Schedule(1) after t=50 should panic")
			}
		}()
		sim.Schedule(1, func() {})
	})
}

func TestBackingStore(t *testing.T) {
	s2 := NewSimulator()
	st := NewBackingStore(s2, 100)
	st.Write("k", usd(5))
	var completed []int64
	s2.Schedule(0, func() { st.Load("k", func(Money) { completed = append(completed, s2.Now()) }) })
	s2.Schedule(30, func() { st.Load("k", func(Money) { completed = append(completed, s2.Now()) }) })
	s2.Run()
	check(t, "store load completes after latency", reflect.DeepEqual(completed, []int64{100, 130}))
	check(t, "store counts overlapping loads", st.Loads == 2 && st.PeakInFlight == 2 && st.InFlight == 0)
}

func TestEntryHorizons(t *testing.T) {
	e := Entry{usd(1), 1_000, 1_500, 100}
	check(t, "entry is fresh before freshUntil", e.IsFresh(999) && !e.IsFresh(1_000))
	check(t, "entry is usable until staleUntil", e.IsUsable(1_499) && !e.IsUsable(1_500))
}

func TestNaiveStampede(t *testing.T) {
	var got []Money
	naive := burst(naiveFactory(5_000), 100, 100, &got)
	check(t, "naive: every cold client loads", naive.Store().Loads == 100 && naive.Store().PeakInFlight == 100)
	check(t, "naive: every client still gets the value", len(got) == 100 && allEqual(got, usd(100)))
	check(t, "naive: every client waits one store latency", naive.Stats().Percentile(0.5) == 100 && naive.Stats().Max() == 100)
}

func TestSingleFlight(t *testing.T) {
	var got []Money
	sf := burst(singleFlightFactory(5_000), 100, 100, &got)
	check(t, "single-flight: exactly one load for 100 concurrent misses", sf.Store().Loads == 1 && sf.Store().PeakInFlight == 1)
	check(t, "single-flight: all 100 clients receive the value", len(got) == 100 && allEqual(got, usd(100)))
	check(t, "single-flight: waiters wait the same 100 ms as the leader", sf.Stats().Max() == 100 && sf.Stats().Percentile(0.5) == 100)
	check(t, "single-flight: 99 misses joined the flight, none waiting after", sf.Stats().Misses == 100 && sf.(*SingleFlightStrategy).WaitingFor("k") == 0)

	s3 := NewSimulator()
	st3 := NewBackingStore(s3, 100)
	st3.Write("k", usd(100))
	sf3 := NewSingleFlightStrategy(s3, st3, 5_000)
	s3.Schedule(0, func() { sf3.Get("k", discard) })
	s3.Schedule(200, func() { sf3.Get("k", discard) })
	s3.Schedule(5_100+50, func() { sf3.Get("k", discard) }) // past the TTL: a second load
	s3.Run()
	check(t, "single-flight: hit after warm-up, miss after TTL", sf3.Stats().Hits == 1 && sf3.Stats().Misses == 2 && st3.Loads == 2)
}

func TestLocking(t *testing.T) {
	var got []Money
	lock := burst(func(sim *Simulator, st *BackingStore) Strategy { return NewLockingStrategy(sim, st, 5_000, 30) }, 50, 100, &got)
	check(t, "lock: one load for 50 concurrent misses", lock.Store().Loads == 1)
	check(t, "lock: everyone is eventually served", len(got) == 50)
	check(t, "lock: each waiter failed at t=0,30,60,90 then hit at t=120", lock.Stats().LockRetries == 49*4)
	check(t, "lock: waiters pay up to one retry interval of extra latency (120 vs 100)", lock.Stats().Max() == 120 && lock.Stats().Percentile(0.01) == 100)
}

func TestEarlyExpiration(t *testing.T) {
	s4 := NewSimulator()
	st4 := NewBackingStore(s4, 100)
	early := NewEarlyExpirationStrategy(s4, st4, 5_000, 1.0, 1)
	farAway := Entry{usd(1), 1_000_000, 1_000_000, 100}
	anyEarly := false
	for i := 0; i < 1_000; i++ {
		anyEarly = anyEarly || early.ShouldRecompute(farAway, 0)
	}
	check(t, "early expiry: a fresh entry far from expiry is practically never recomputed", !anyEarly)
	atBoundary := Entry{usd(1), 1_000, 1_000, 100}
	check(t, "early expiry: at the boundary it always recomputes", early.ShouldRecompute(atBoundary, 1_000))
	nearCount := 0
	near := Entry{usd(1), 1_050, 1_050, 100} // 50 ms before expiry, load takes 100 ms
	for i := 0; i < 10_000; i++ {
		if early.ShouldRecompute(near, 1_000) {
			nearCount++
		}
	}
	check(t, "early expiry: 50 ms before expiry with a 100 ms load, recompute probability is about e^-0.5", nearCount > 5_500 && nearCount < 6_600)

	quiet := NewScenario(1_000, 15_000, 100, 5_000, -1, 7)
	xfetch := quiet.Run(earlyFactory(5_000, 1.0, 11))
	naiveQuiet := quiet.Run(naiveFactory(5_000))
	check(t, "early expiry: far fewer loads than naive on the same traffic", xfetch.Store().Loads < naiveQuiet.Store().Loads/5)
	check(t, "early expiry: peak concurrency stays small", xfetch.Store().PeakInFlight < 10 && naiveQuiet.Store().PeakInFlight > 90)
	check(t, "early expiry: refreshes happen before the TTL", xfetch.Stats().EarlyRefreshes > 0 && xfetch.Stats().AverageLeadMs() > 0)
	check(t, "early expiry: no client waits after warm-up (p99 is a hit)", xfetch.Stats().Percentile(0.99) == 0)
}

func TestStaleWhileRevalidate(t *testing.T) {
	s5 := NewSimulator()
	st5 := NewBackingStore(s5, 100)
	st5.Write("k", usd(100))
	swr := NewStaleWhileRevalidateStrategy(s5, st5, 1_000, 500)
	var swrGot []Money
	collect := func(v Money) { swrGot = append(swrGot, v) }
	s5.Schedule(0, func() { swr.Get("k", collect) })     // cold: waits 100 ms
	s5.Schedule(900, func() { st5.Write("k", usd(20)) }) // store changes
	s5.Schedule(1_200, func() { swr.Get("k", collect) }) // stale, within grace: served old value now
	s5.Schedule(1_210, func() { swr.Get("k", collect) }) // still refreshing: no second load
	s5.Schedule(1_400, func() { swr.Get("k", collect) }) // refresh landed at 1300: fresh new value
	s5.Run()
	if len(swrGot) != 4 {
		t.Fatalf("expected 4 answers, got %d", len(swrGot))
	}
	check(t, "swr: stale value served immediately within grace", swrGot[1] == usd(100) && swrGot[2] == usd(100))
	check(t, "swr: exactly one background refresh for the stale window", st5.Loads == 2)
	check(t, "swr: after the refresh the new value is served", swrGot[3] == usd(20))
	check(t, "swr: stale serves are counted honestly", swr.Stats().StaleServes == 2)
	check(t, "swr: only the cold request waited", swr.Stats().Max() == 100 && swr.Stats().Percentile(0.5) == 0)
	check(t, "swr: not refreshing once the load has landed", !swr.IsRefreshing("k"))

	s6 := NewSimulator()
	st6 := NewBackingStore(s6, 100)
	st6.Write("k", usd(100))
	swr2 := NewStaleWhileRevalidateStrategy(s6, st6, 1_000, 500)
	s6.Schedule(0, func() { swr2.Get("k", discard) })
	for i := int64(0); i < 20; i++ {
		s6.Schedule(2_000+i, func() { swr2.Get("k", discard) })
	}
	s6.Run()
	check(t, "swr: past grace, 20 concurrent misses coalesce into one load", st6.Loads == 2 && swr2.Stats().Misses == 21)
}

func TestInvalidationCliff(t *testing.T) {
	sc := NewScenario(1_000, 4_000, 100, 5_000, -1, 7)
	nInv := sc.RunWithInvalidation(naiveFactory(5_000), 2_500)
	fInv := sc.RunWithInvalidation(singleFlightFactory(5_000), 2_500)
	check(t, "invalidation: naive reloads once per request in the store-latency window", nInv.Store().Loads > 90 && nInv.Store().PeakInFlight > 90)
	check(t, "invalidation: single-flight reloads once", fInv.Store().Loads == 2 && fInv.Store().PeakInFlight == 1)

	r1 := sc.Run(earlyFactory(5_000, 1.0, 3))
	r2 := sc.Run(earlyFactory(5_000, 1.0, 3))
	check(t, "scenario: same seeds, same loads and latencies", r1.Store().Loads == r2.Store().Loads && reflect.DeepEqual(r1.Stats().Latencies, r2.Stats().Latencies))
}

func TestPercentiles(t *testing.T) {
	cs := &ClientStats{}
	for _, v := range []int64{5, 1, 4, 2, 3} {
		cs.Record(v, false)
	}
	check(t, "percentile: nearest-rank p50 of 1..5 is 3, p99 is 5, max 5", cs.Percentile(0.5) == 3 && cs.Percentile(0.99) == 5 && cs.Max() == 5)
}

func TestDemoParity(t *testing.T) {
	// The headline table matches the Java demo digit for digit because both RNGs are java.util.Random.
	sc := NewScenario(1_000, 10_000, 100, 5_000, 4_000, 7)
	naive := sc.Run(naiveFactory(5_000))
	check(t, "parity: naive loads 103, peak in-flight 102, stale 1099", naive.Store().Loads == 103 && naive.Store().PeakInFlight == 102 && naive.Stats().StaleServes == 1099)
	early := sc.Run(earlyFactory(5_000, 1.0, 11))
	check(t, "parity: early expiry loads 5, peak 3, stale 761, early 4", early.Store().Loads == 5 && early.Store().PeakInFlight == 3 && early.Stats().StaleServes == 761 && early.Stats().EarlyRefreshes == 4)
	check(t, "parity: strategy name renders beta like Java", early.Name() == "probabilistic early expiry (beta=1.0)")
}
