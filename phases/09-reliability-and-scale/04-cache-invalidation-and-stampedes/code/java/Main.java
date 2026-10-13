// Lesson: phases/09-reliability-and-scale/04-cache-invalidation-and-stampedes/docs/en.md
// Cache Invalidation, Stampedes, and Request Coalescing
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: Vattani, Chierichetti, Lowenstein, "Optimal Probabilistic Cache Stampede Prevention" (VLDB 2015);
//          RFC 5861 (stale-while-revalidate); Nishtala et al., "Scaling Memcache at Facebook" (NSDI 2013). Java 21, stdlib only.

import java.util.ArrayList;
import java.util.Collections;
import java.util.HashMap;
import java.util.HashSet;
import java.util.List;
import java.util.Map;
import java.util.Objects;
import java.util.PriorityQueue;
import java.util.Random;
import java.util.Set;

/** ISO 4217 currency (Phase 10, Lesson 01). */
record Currency(String code, int exponent) {
    static final Currency USD = new Currency("USD", 2);

    Currency {
        if (code == null || code.length() != 3 || !code.chars().allMatch(Character::isUpperCase)) {
            throw new IllegalArgumentException("currency code must be three uppercase letters: " + code);
        }
        if (exponent < 0 || exponent > 4) throw new IllegalArgumentException("exponent must be 0..4: " + exponent);
    }

    long minorUnitsPerMajor() {
        long scale = 1;
        for (int i = 0; i < exponent; i++) scale *= 10;
        return scale;
    }
}

/** An exact amount of one currency as a signed count of minor units (Phase 10, Lesson 01). */
record Money(long minorUnits, Currency currency) {
    Money { Objects.requireNonNull(currency, "currency"); }

    static Money ofMinor(long minorUnits, Currency currency) { return new Money(minorUnits, currency); }

    Money minus(Money other) {
        if (!currency.equals(other.currency)) throw new IllegalArgumentException("cannot combine " + currency.code() + " with " + other.currency.code());
        return new Money(Math.subtractExact(minorUnits, other.minorUnits), currency);
    }

    @Override public String toString() {
        long scale = currency.minorUnitsPerMajor();
        long whole = Math.abs(minorUnits) / scale, frac = Math.abs(minorUnits) % scale;
        String sign = minorUnits < 0 ? "-" : "";
        if (currency.exponent() == 0) return sign + whole + " " + currency.code();
        return sign + whole + "." + String.format("%0" + currency.exponent() + "d", frac) + " " + currency.code();
    }
}

/**
 * A discrete-event simulator. Time only moves when the next event is popped, so a run that represents ten
 * seconds of traffic completes in milliseconds and is identical on every run. Events at the same instant run
 * in scheduling order.
 */
final class Simulator {
    private record Event(long atMs, long seq, Runnable action) {}

    private final PriorityQueue<Event> queue = new PriorityQueue<>(
            (a, b) -> a.atMs != b.atMs ? Long.compare(a.atMs, b.atMs) : Long.compare(a.seq, b.seq));
    private long nowMs;
    private long seq;

    long now() { return nowMs; }

    void schedule(long atMs, Runnable action) {
        if (atMs < nowMs) throw new IllegalArgumentException("cannot schedule in the past: " + atMs + " < " + nowMs);
        queue.add(new Event(atMs, seq++, action));
    }

    void after(long delayMs, Runnable action) { schedule(nowMs + delayMs, action); }

    /** Runs every event in time order until the queue is empty. */
    void run() {
        while (!queue.isEmpty()) {
            Event e = queue.poll();
            nowMs = e.atMs();
            e.action().run();
        }
    }
}

/** The database behind the cache. A load takes {@code latencyMs} of simulated time; the store counts how many are in flight. */
final class BackingStore {
    interface Callback { void onLoaded(Money value); }

    private final Simulator sim;
    private final long latencyMs;
    private final Map<String, Money> rows = new HashMap<>();
    long loads, inFlight, peakInFlight;

    BackingStore(Simulator sim, long latencyMs) { this.sim = sim; this.latencyMs = latencyMs; }

    void write(String key, Money value) { rows.put(key, value); }
    Money peek(String key) { return rows.get(key); }
    long latencyMs() { return latencyMs; }

    /** Starts a load. The value is the row as it stands when the load completes, delivered via the callback. */
    void load(String key, Callback cb) {
        loads++;
        inFlight++;
        peakInFlight = Math.max(peakInFlight, inFlight);
        sim.after(latencyMs, () -> {
            inFlight--;
            cb.onLoaded(rows.get(key));
        });
    }
}

/** A cached value with two horizons: fresh until, and (for stale-while-revalidate) usable until. */
record Entry(Money value, long freshUntilMs, long staleUntilMs, long loadTookMs) {
    boolean isFresh(long now) { return now < freshUntilMs; }
    boolean isUsable(long now) { return now < staleUntilMs; }
}

/** What every client records: when it asked, when it got an answer, and whether the answer matched the store. */
final class ClientStats {
    final List<Long> latencies = new ArrayList<>();
    long hits, misses, staleServes, lockRetries, earlyRefreshes, earlyLeadTotalMs;

    /** Average time before expiry at which an early refresh was triggered. */
    long averageLeadMs() { return earlyRefreshes == 0 ? 0 : earlyLeadTotalMs / earlyRefreshes; }

    void record(long latencyMs, boolean stale) {
        latencies.add(latencyMs);
        if (stale) staleServes++;
    }

    long percentile(double p) {
        if (latencies.isEmpty()) return 0;
        List<Long> sorted = new ArrayList<>(latencies);
        Collections.sort(sorted);
        int rank = (int) Math.ceil(p * sorted.size()) - 1;             // nearest-rank
        return sorted.get(Math.max(0, Math.min(sorted.size() - 1, rank)));
    }

    long max() { return latencies.isEmpty() ? 0 : Collections.max(latencies); }
}

/** A request's completion: the value the client was handed. */
interface Waiter { void complete(Money value); }

/**
 * The common shape of every strategy: a per-key map of entries, a store, the simulator clock, a stats block,
 * and the two operations the workload drives. Subclasses decide what happens on a miss and on an aging hit.
 */
abstract class Strategy {
    final Simulator sim;
    final BackingStore store;
    final ClientStats stats = new ClientStats();
    final Map<String, Entry> entries = new HashMap<>();
    final long ttlMs;

    Strategy(Simulator sim, BackingStore store, long ttlMs) { this.sim = sim; this.store = store; this.ttlMs = ttlMs; }

    abstract String name();

    /** A client asks for key at the current simulated time. The waiter is called when a value is available. */
    abstract void get(String key, Waiter waiter);

    /** Explicit invalidation: forget the key now. For a hot key this is an expiry you chose the moment of. */
    void invalidate(String key) { entries.remove(key); }

    Entry freshEntry(Money value, long loadTookMs, long graceMs) {
        long now = sim.now();
        return new Entry(value, now + ttlMs, now + ttlMs + graceMs, loadTookMs);
    }

    /** Wraps a waiter so that latency and staleness are measured against the store at completion time. */
    Waiter measured(String key, long arrivedMs, Waiter inner) {
        return value -> {
            boolean stale = !Objects.equals(value, store.peek(key));
            stats.record(sim.now() - arrivedMs, stale);
            inner.complete(value);
        };
    }
}

/** Every miss loads. The hot key expires and every request that arrives during one store latency starts its own load. */
final class NaiveStrategy extends Strategy {
    NaiveStrategy(Simulator sim, BackingStore store, long ttlMs) { super(sim, store, ttlMs); }

    @Override String name() { return "naive (every miss loads)"; }

    @Override void get(String key, Waiter waiter) {
        Waiter w = measured(key, sim.now(), waiter);
        Entry e = entries.get(key);
        if (e != null && e.isFresh(sim.now())) { stats.hits++; w.complete(e.value()); return; }
        stats.misses++;
        long started = sim.now();
        store.load(key, value -> {
            entries.put(key, freshEntry(value, sim.now() - started, 0));
            w.complete(value);
        });
    }
}

/**
 * Request coalescing (single-flight): the first miss starts a load and registers itself; every later miss for
 * the same key, while that load is in flight, joins the waiting list instead of loading. One load, N answers.
 */
final class SingleFlightStrategy extends Strategy {
    private final Map<String, List<Waiter>> inFlight = new HashMap<>();

    SingleFlightStrategy(Simulator sim, BackingStore store, long ttlMs) { super(sim, store, ttlMs); }

    @Override String name() { return "single-flight (coalesce)"; }

    int waitingFor(String key) { return inFlight.getOrDefault(key, List.of()).size(); }

    @Override void get(String key, Waiter waiter) {
        Waiter w = measured(key, sim.now(), waiter);
        Entry e = entries.get(key);
        if (e != null && e.isFresh(sim.now())) { stats.hits++; w.complete(e.value()); return; }
        stats.misses++;
        List<Waiter> waiters = inFlight.get(key);
        if (waiters != null) { waiters.add(w); return; }                 // join the flight already in the air
        List<Waiter> fresh = new ArrayList<>();
        fresh.add(w);
        inFlight.put(key, fresh);
        long started = sim.now();
        store.load(key, value -> {
            entries.put(key, freshEntry(value, sim.now() - started, 0));
            List<Waiter> done = inFlight.remove(key);
            for (Waiter each : done) each.complete(value);
        });
    }
}

/**
 * A per-key lock, the shape you get with Redis SET NX or a database advisory lock. The lock holder loads;
 * everyone else backs off and retries after retryMs. One load, but every retry is latency and a cache round trip.
 */
final class LockingStrategy extends Strategy {
    private final Set<String> locks = new HashSet<>();
    private final long retryMs;

    LockingStrategy(Simulator sim, BackingStore store, long ttlMs, long retryMs) { super(sim, store, ttlMs); this.retryMs = retryMs; }

    @Override String name() { return "lock + retry (" + retryMs + " ms)"; }

    @Override void get(String key, Waiter waiter) { attempt(key, sim.now(), waiter, true); }

    private void attempt(String key, long arrivedMs, Waiter waiter, boolean first) {
        Entry e = entries.get(key);
        if (e != null && e.isFresh(sim.now())) {
            if (first) stats.hits++;
            measured(key, arrivedMs, waiter).complete(e.value());
            return;
        }
        if (first) stats.misses++;
        if (locks.add(key)) {                                             // SET key NX: we own the reload
            long started = sim.now();
            store.load(key, value -> {
                entries.put(key, freshEntry(value, sim.now() - started, 0));
                locks.remove(key);
                measured(key, arrivedMs, waiter).complete(value);
            });
        } else {
            stats.lockRetries++;
            sim.after(retryMs, () -> attempt(key, arrivedMs, waiter, false));
        }
    }
}

/**
 * Probabilistic early expiration (XFetch, Vattani et al. 2015). On a hit, a client recomputes early with a
 * probability that rises as expiry approaches, scaled by how long the last load took:
 * recompute if now - delta * beta * ln(rand) >= freshUntil. The hot key is refreshed before it expires, by one
 * or a few clients rather than by everyone who arrives after the cliff. No coordination, no lock.
 */
final class EarlyExpirationStrategy extends Strategy {
    private final Random rng;
    private final double beta;

    EarlyExpirationStrategy(Simulator sim, BackingStore store, long ttlMs, double beta, long seed) {
        super(sim, store, ttlMs);
        this.beta = beta;
        this.rng = new Random(seed);
    }

    @Override String name() { return "probabilistic early expiry (beta=" + beta + ")"; }

    boolean shouldRecompute(Entry e, long now) {
        double u = rng.nextDouble();
        if (u == 0.0) u = Double.MIN_VALUE;
        double gap = e.loadTookMs() * beta * -Math.log(u);               // >= 0, heavy-tailed
        return now + gap >= e.freshUntilMs();
    }

    @Override void get(String key, Waiter waiter) {
        Waiter w = measured(key, sim.now(), waiter);
        Entry e = entries.get(key);
        if (e != null && e.isFresh(sim.now())) {
            if (!shouldRecompute(e, sim.now())) { stats.hits++; w.complete(e.value()); return; }
            stats.earlyRefreshes++;                                       // this client volunteers to pay for the reload
            stats.earlyLeadTotalMs += e.freshUntilMs() - sim.now();
        } else {
            stats.misses++;
        }
        long started = sim.now();
        store.load(key, value -> {
            entries.put(key, freshEntry(value, sim.now() - started, 0));
            w.complete(value);
        });
    }
}

/**
 * Stale-while-revalidate (RFC 5861). An entry has a fresh horizon and a grace horizon. Between the two, the
 * stale value is served immediately and one background refresh is started (coalesced). Clients never wait for
 * the store unless the entry is past its grace too; the price is bounded, explicit staleness.
 */
final class StaleWhileRevalidateStrategy extends Strategy {
    private final long graceMs;
    private final Set<String> refreshing = new HashSet<>();
    private final Map<String, List<Waiter>> coldWaiters = new HashMap<>();

    StaleWhileRevalidateStrategy(Simulator sim, BackingStore store, long ttlMs, long graceMs) { super(sim, store, ttlMs); this.graceMs = graceMs; }

    @Override String name() { return "stale-while-revalidate (grace " + graceMs + " ms)"; }

    boolean isRefreshing(String key) { return refreshing.contains(key); }

    @Override void get(String key, Waiter waiter) {
        Waiter w = measured(key, sim.now(), waiter);
        Entry e = entries.get(key);
        long now = sim.now();
        if (e != null && e.isFresh(now)) { stats.hits++; w.complete(e.value()); return; }
        if (e != null && e.isUsable(now)) {                               // stale but within grace: serve it, refresh once
            stats.hits++;
            w.complete(e.value());
            startRefresh(key);
            return;
        }
        stats.misses++;                                                   // cold or past grace: must wait, but coalesce
        List<Waiter> waiters = coldWaiters.computeIfAbsent(key, k -> new ArrayList<>());
        waiters.add(w);
        startRefresh(key);
    }

    private void startRefresh(String key) {
        if (!refreshing.add(key)) return;
        long started = sim.now();
        store.load(key, value -> {
            entries.put(key, freshEntry(value, sim.now() - started, graceMs));
            refreshing.remove(key);
            List<Waiter> waiting = coldWaiters.remove(key);
            if (waiting != null) for (Waiter each : waiting) each.complete(value);
        });
    }
}

/** The hot-key scenario: steady traffic on one key for a while, a write to the store, and the TTL cliff. */
final class Scenario {
    static final long WARM_UP_MS = 200;                                  // traffic starts after the warm-up load has landed
    final long requestsPerSecond, durationMs, storeLatencyMs, ttlMs, writeAtMs;
    final long seed;

    Scenario(long requestsPerSecond, long durationMs, long storeLatencyMs, long ttlMs, long writeAtMs, long seed) {
        this.requestsPerSecond = requestsPerSecond;
        this.durationMs = durationMs;
        this.storeLatencyMs = storeLatencyMs;
        this.ttlMs = ttlMs;
        this.writeAtMs = writeAtMs;
        this.seed = seed;
    }

    interface Factory { Strategy create(Simulator sim, BackingStore store); }

    /** Runs the scenario against a freshly built strategy and returns it with its statistics filled in. */
    Strategy run(Factory factory) {
        Simulator sim = new Simulator();
        BackingStore store = new BackingStore(sim, storeLatencyMs);
        String key = "balance:acct-hot";
        store.write(key, Money.ofMinor(100_000, Currency.USD));
        Strategy s = factory.create(sim, store);
        Random jitter = new Random(seed);
        long gapMicros = 1_000_000L / requestsPerSecond;
        sim.schedule(0, () -> s.get(key, v -> {}));                        // one warm-up request fills the cache at t=0
        for (long t = WARM_UP_MS * 1000; t < durationMs * 1000; t += gapMicros) {
            long at = t / 1000 + jitter.nextInt(2);                      // one request every gap, with 0-1 ms of jitter
            sim.schedule(at, () -> s.get(key, v -> {}));
        }
        if (writeAtMs >= 0) sim.schedule(writeAtMs, () -> store.write(key, Money.ofMinor(100_000, Currency.USD).minus(Money.ofMinor(8_000, Currency.USD))));
        sim.run();
        return s;
    }

    /** Same traffic, but the key is explicitly invalidated at invalidateAtMs instead of waiting for the TTL. */
    Strategy runWithInvalidation(Factory factory, long invalidateAtMs) {
        Simulator sim = new Simulator();
        BackingStore store = new BackingStore(sim, storeLatencyMs);
        String key = "balance:acct-hot";
        store.write(key, Money.ofMinor(100_000, Currency.USD));
        Strategy s = factory.create(sim, store);
        long gapMicros = 1_000_000L / requestsPerSecond;
        sim.schedule(0, () -> s.get(key, v -> {}));
        for (long t = WARM_UP_MS * 1000; t < durationMs * 1000; t += gapMicros) sim.schedule(t / 1000, () -> s.get(key, v -> {}));
        sim.schedule(invalidateAtMs, () -> s.invalidate(key));
        sim.run();
        return s;
    }
}

public final class Main {
    static String row(Strategy s) {
        ClientStats c = s.stats;
        return String.format("%-42s loads %4d  peak in-flight %4d  p50 %4d ms  p99 %4d ms  max %4d ms  stale %5d  retries %5d  early %3d",
                s.name(), s.store.loads, s.store.peakInFlight, c.percentile(0.50), c.percentile(0.99), c.max(),
                c.staleServes, c.lockRetries, c.earlyRefreshes);
    }

    public static void main(String[] args) {
        System.out.println("== The stampede: one hot balance key, 1,000 requests/s, 10 s of traffic, TTL 5 s, store latency 100 ms ==");
        System.out.println("   The store is debited at t=4.0 s (so values served from 4.0 s to the next reload are stale by 80.00 USD).");
        Scenario sc = new Scenario(1_000, 10_000, 100, 5_000, 4_000, 7);
        List<Strategy> results = new ArrayList<>();
        results.add(sc.run((sim, st) -> new NaiveStrategy(sim, st, 5_000)));
        results.add(sc.run((sim, st) -> new SingleFlightStrategy(sim, st, 5_000)));
        results.add(sc.run((sim, st) -> new LockingStrategy(sim, st, 5_000, 10)));
        results.add(sc.run((sim, st) -> new EarlyExpirationStrategy(sim, st, 5_000, 1.0, 11)));
        results.add(sc.run((sim, st) -> new StaleWhileRevalidateStrategy(sim, st, 5_000, 2_000)));
        for (Strategy s : results) System.out.println(row(s));

        System.out.println();
        System.out.println("Reading the table:");
        System.out.println("  naive: when the key expires at t=5000 every request in the next 100 ms misses and loads: ~100 concurrent loads for one row.");
        System.out.println("  single-flight: one load per cliff; the ~100 waiters share it and wait at most one store latency; the store sees 1 request.");
        System.out.println("  lock + retry: one load, but waiters poll; each retry is a cache round trip and 10 ms of added latency.");
        System.out.println("  early expiry: a few clients volunteer to reload before the cliff, so the cliff never arrives; no coordination at all.");
        System.out.println("  stale-while-revalidate: nobody waits after warm-up; the cost is the stale column, which is bounded by the grace period.");
        System.out.println("  stale: every strategy serves ~1,000 stale answers between the write at 4.0 s and the TTL at 5.0 s. That is the TTL hazard from Lesson 03;");
        System.out.println("         only invalidation at write time removes it, and invalidation is itself a cliff (below).");

        System.out.println();
        System.out.println("== Invalidation is a cliff you chose: same traffic, key invalidated at t=2.5 s ==");
        Strategy n = sc.runWithInvalidation((sim, st) -> new NaiveStrategy(sim, st, 5_000), 2_500);
        Strategy f = sc.runWithInvalidation((sim, st) -> new SingleFlightStrategy(sim, st, 5_000), 2_500);
        System.out.println(row(n));
        System.out.println(row(f));
        System.out.println("Invalidating a hot key on every write is correct (Lesson 03) and produces exactly one stampede per write unless misses are coalesced.");

        System.out.println();
        System.out.println("== Why beta matters: early-expiry behaviour over 30 s of traffic, no writes, at different beta ==");
        Scenario quiet = new Scenario(1_000, 30_000, 100, 5_000, -1, 7);
        for (double beta : new double[]{0.5, 1.0, 2.0, 4.0}) {
            Strategy e = quiet.run((sim, st) -> new EarlyExpirationStrategy(sim, st, 5_000, beta, 11));
            System.out.printf("  beta=%.1f  loads %3d  peak in-flight %2d  early refreshes %3d  avg lead before expiry %4d ms  p99 %3d ms%n",
                    beta, e.store.loads, e.store.peakInFlight, e.stats.earlyRefreshes, e.stats.averageLeadMs(), e.stats.percentile(0.99));
        }
        System.out.println("Low beta waits until the last moment, so several clients roll a refresh during the same 100 ms load and duplicate it.\n"
                + "High beta refreshes well before expiry and so runs more refresh cycles. The cliff is never reached either way; beta=1 is the paper's default.");
    }
}
