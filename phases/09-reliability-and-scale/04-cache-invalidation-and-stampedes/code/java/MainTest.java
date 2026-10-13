// Tests for phases/09-reliability-and-scale/04-cache-invalidation-and-stampedes. Run: javac -d .out *.java && java -cp .out MainTest
import java.util.ArrayList;
import java.util.List;

public final class MainTest {
    private static int passed = 0;
    private static final List<String> failures = new ArrayList<>();

    static void check(String name, boolean condition) {
        if (condition) passed++; else failures.add(name);
    }

    static void checkThrows(String name, Class<? extends Throwable> type, Runnable action) {
        try {
            action.run();
            failures.add(name + " (no exception)");
        } catch (Throwable t) {
            if (type.isInstance(t)) passed++; else failures.add(name + " (wrong exception " + t.getClass().getSimpleName() + ")");
        }
    }

    static Money usd(long minor) { return Money.ofMinor(minor, Currency.USD); }

    /** N clients ask for the same cold key at the same instant. Returns the strategy after the simulation drains. */
    static Strategy burst(Scenario.Factory factory, int clients, long storeLatency, List<Money> received) {
        Simulator sim = new Simulator();
        BackingStore store = new BackingStore(sim, storeLatency);
        store.write("k", usd(100));
        Strategy s = factory.create(sim, store);
        for (int i = 0; i < clients; i++) sim.schedule(0, () -> s.get("k", received::add));
        sim.run();
        return s;
    }

    public static void main(String[] args) {
        // Simulator: ordering and the no-time-travel rule
        Simulator sim = new Simulator();
        List<String> order = new ArrayList<>();
        sim.schedule(50, () -> order.add("b"));
        sim.schedule(10, () -> order.add("a"));
        sim.schedule(50, () -> order.add("c"));
        sim.schedule(10, () -> sim.after(5, () -> order.add("a2")));
        sim.run();
        check("simulator runs events in time order, ties in scheduling order", order.equals(List.of("a", "a2", "b", "c")));
        check("simulator clock stops at the last event", sim.now() == 50);
        checkThrows("simulator refuses to schedule in the past", IllegalArgumentException.class, () -> sim.schedule(1, () -> {}));

        // BackingStore: a load completes after latency and reports concurrency
        Simulator s2 = new Simulator();
        BackingStore st = new BackingStore(s2, 100);
        st.write("k", usd(5));
        List<Long> completed = new ArrayList<>();
        s2.schedule(0, () -> st.load("k", v -> completed.add(s2.now())));
        s2.schedule(30, () -> st.load("k", v -> completed.add(s2.now())));
        s2.run();
        check("store load completes after latency", completed.equals(List.of(100L, 130L)));
        check("store counts overlapping loads", st.loads == 2 && st.peakInFlight == 2 && st.inFlight == 0);

        // Entry horizons
        Entry e = new Entry(usd(1), 1_000, 1_500, 100);
        check("entry is fresh before freshUntil", e.isFresh(999) && !e.isFresh(1_000));
        check("entry is usable until staleUntil", e.isUsable(1_499) && !e.isUsable(1_500));

        // The stampede itself: 100 cold clients, naive strategy
        List<Money> got = new ArrayList<>();
        Strategy naive = burst((sm, store) -> new NaiveStrategy(sm, store, 5_000), 100, 100, got);
        check("naive: every cold client loads", naive.store.loads == 100 && naive.store.peakInFlight == 100);
        check("naive: every client still gets the value", got.size() == 100 && got.stream().allMatch(usd(100)::equals));
        check("naive: every client waits one store latency", naive.stats.percentile(0.5) == 100 && naive.stats.max() == 100);

        // Single-flight: one load, everyone served from it
        got.clear();
        Strategy sf = burst((sm, store) -> new SingleFlightStrategy(sm, store, 5_000), 100, 100, got);
        check("single-flight: exactly one load for 100 concurrent misses", sf.store.loads == 1 && sf.store.peakInFlight == 1);
        check("single-flight: all 100 clients receive the value", got.size() == 100 && got.stream().allMatch(usd(100)::equals));
        check("single-flight: waiters wait the same 100 ms as the leader", sf.stats.max() == 100 && sf.stats.percentile(0.5) == 100);
        check("single-flight: 99 misses joined the flight, none waiting after", sf.stats.misses == 100 && ((SingleFlightStrategy) sf).waitingFor("k") == 0);

        // Single-flight after warm-up: a later request is a hit, no load
        Simulator s3 = new Simulator();
        BackingStore st3 = new BackingStore(s3, 100);
        st3.write("k", usd(100));
        SingleFlightStrategy sf3 = new SingleFlightStrategy(s3, st3, 5_000);
        s3.schedule(0, () -> sf3.get("k", v -> {}));
        s3.schedule(200, () -> sf3.get("k", v -> {}));
        s3.schedule(5_100 + 50, () -> sf3.get("k", v -> {}));              // past the TTL: a second load
        s3.run();
        check("single-flight: hit after warm-up, miss after TTL", sf3.stats.hits == 1 && sf3.stats.misses == 2 && st3.loads == 2);

        // Locking: one load, waiters retry
        got.clear();
        Strategy lock = burst((sm, store) -> new LockingStrategy(sm, store, 5_000, 30), 50, 100, got);
        check("lock: one load for 50 concurrent misses", lock.store.loads == 1);
        check("lock: everyone is eventually served", got.size() == 50);
        check("lock: each waiter failed at t=0,30,60,90 then hit at t=120", lock.stats.lockRetries == 49 * 4);
        check("lock: waiters pay up to one retry interval of extra latency (120 vs 100)", lock.stats.max() == 120 && lock.stats.percentile(0.01) == 100);

        // Early expiration: never recompute far from expiry, always recompute at the boundary
        Simulator s4 = new Simulator();
        BackingStore st4 = new BackingStore(s4, 100);
        EarlyExpirationStrategy early = new EarlyExpirationStrategy(s4, st4, 5_000, 1.0, 1);
        Entry farAway = new Entry(usd(1), 1_000_000, 1_000_000, 100);
        boolean anyEarly = false;
        for (int i = 0; i < 1_000; i++) anyEarly |= early.shouldRecompute(farAway, 0);
        check("early expiry: a fresh entry far from expiry is practically never recomputed", !anyEarly);
        Entry atBoundary = new Entry(usd(1), 1_000, 1_000, 100);
        check("early expiry: at the boundary it always recomputes", early.shouldRecompute(atBoundary, 1_000));
        int nearCount = 0;
        Entry near = new Entry(usd(1), 1_050, 1_050, 100);                  // 50 ms before expiry, load takes 100 ms
        for (int i = 0; i < 10_000; i++) if (early.shouldRecompute(near, 1_000)) nearCount++;
        check("early expiry: 50 ms before expiry with a 100 ms load, recompute probability is about e^-0.5", nearCount > 5_500 && nearCount < 6_600);

        // Early expiration on the real scenario: the cliff is avoided
        Scenario quiet = new Scenario(1_000, 15_000, 100, 5_000, -1, 7);
        Strategy xfetch = quiet.run((sm, store) -> new EarlyExpirationStrategy(sm, store, 5_000, 1.0, 11));
        Strategy naiveQuiet = quiet.run((sm, store) -> new NaiveStrategy(sm, store, 5_000));
        check("early expiry: far fewer loads than naive on the same traffic", xfetch.store.loads < naiveQuiet.store.loads / 5);
        check("early expiry: peak concurrency stays small", xfetch.store.peakInFlight < 10 && naiveQuiet.store.peakInFlight > 90);
        check("early expiry: refreshes happen before the TTL", xfetch.stats.earlyRefreshes > 0 && xfetch.stats.averageLeadMs() > 0);
        check("early expiry: no client waits after warm-up (p99 is a hit)", xfetch.stats.percentile(0.99) == 0);

        // Stale-while-revalidate: serve stale within grace, one refresh, no waiting
        Simulator s5 = new Simulator();
        BackingStore st5 = new BackingStore(s5, 100);
        st5.write("k", usd(100));
        StaleWhileRevalidateStrategy swr = new StaleWhileRevalidateStrategy(s5, st5, 1_000, 500);
        List<Money> swrGot = new ArrayList<>();
        s5.schedule(0, () -> swr.get("k", swrGot::add));                    // cold: waits 100 ms
        s5.schedule(900, () -> st5.write("k", usd(20)));                    // store changes
        s5.schedule(1_200, () -> swr.get("k", swrGot::add));                // stale, within grace: served old value now
        s5.schedule(1_210, () -> swr.get("k", swrGot::add));                // still refreshing: no second load
        s5.schedule(1_400, () -> swr.get("k", swrGot::add));                // refresh landed at 1300: fresh new value
        s5.run();
        check("swr: stale value served immediately within grace", swrGot.get(1).equals(usd(100)) && swrGot.get(2).equals(usd(100)));
        check("swr: exactly one background refresh for the stale window", st5.loads == 2);
        check("swr: after the refresh the new value is served", swrGot.get(3).equals(usd(20)));
        check("swr: stale serves are counted honestly", swr.stats.staleServes == 2);
        check("swr: only the cold request waited", swr.stats.max() == 100 && swr.stats.percentile(0.5) == 0);
        check("swr: not refreshing once the load has landed", !swr.isRefreshing("k"));

        // Past the grace horizon SWR behaves like single-flight
        Simulator s6 = new Simulator();
        BackingStore st6 = new BackingStore(s6, 100);
        st6.write("k", usd(100));
        StaleWhileRevalidateStrategy swr2 = new StaleWhileRevalidateStrategy(s6, st6, 1_000, 500);
        s6.schedule(0, () -> swr2.get("k", v -> {}));
        for (int i = 0; i < 20; i++) s6.schedule(2_000 + i, () -> swr2.get("k", v -> {}));
        s6.run();
        check("swr: past grace, 20 concurrent misses coalesce into one load", st6.loads == 2 && swr2.stats.misses == 21);

        // Invalidation is a cliff: naive stampedes on it, single-flight does not
        Scenario sc = new Scenario(1_000, 4_000, 100, 5_000, -1, 7);
        Strategy nInv = sc.runWithInvalidation((sm, store) -> new NaiveStrategy(sm, store, 5_000), 2_500);
        Strategy fInv = sc.runWithInvalidation((sm, store) -> new SingleFlightStrategy(sm, store, 5_000), 2_500);
        check("invalidation: naive reloads once per request in the store-latency window", nInv.store.loads > 90 && nInv.store.peakInFlight > 90);
        check("invalidation: single-flight reloads once", fInv.store.loads == 2 && fInv.store.peakInFlight == 1);

        // Scenario is deterministic
        Strategy r1 = sc.run((sm, store) -> new EarlyExpirationStrategy(sm, store, 5_000, 1.0, 3));
        Strategy r2 = sc.run((sm, store) -> new EarlyExpirationStrategy(sm, store, 5_000, 1.0, 3));
        check("scenario: same seeds, same loads and latencies", r1.store.loads == r2.store.loads && r1.stats.latencies.equals(r2.stats.latencies));

        // Percentiles
        ClientStats cs = new ClientStats();
        for (long v : new long[]{5, 1, 4, 2, 3}) cs.record(v, false);
        check("percentile: nearest-rank p50 of 1..5 is 3, p99 is 5, max 5", cs.percentile(0.5) == 3 && cs.percentile(0.99) == 5 && cs.max() == 5);

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }
}
