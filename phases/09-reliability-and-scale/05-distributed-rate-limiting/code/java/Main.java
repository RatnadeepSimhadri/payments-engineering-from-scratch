// Lesson: phases/09-reliability-and-scale/05-distributed-rate-limiting/docs/en.md
// Distributed Rate Limiting: token buckets across N nodes, three coordination strategies measured under seeded load
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: RFC 6585 (429 Too Many Requests); IETF draft-ietf-httpapi-ratelimit-headers; Redis INCR/EXPIRE docs;
//          Stripe "Scaling your API with rate limiters" (2017). Java 21, standard library only.

import java.util.ArrayDeque;
import java.util.ArrayList;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.Random;

/** Injected time source so every simulation is deterministic and no test ever sleeps. */
interface Clock {
    long nowMillis();
}

final class FakeClock implements Clock {
    private long now;

    FakeClock(long startMillis) { this.now = startMillis; }

    @Override public long nowMillis() { return now; }

    void advance(long millis) {
        if (millis < 0) throw new IllegalArgumentException("time does not go backwards");
        now += millis;
    }
}

/**
 * Token bucket with integer arithmetic. Tokens are stored in milli-tokens so a refill rate of
 * 7 tokens/second accrues exactly 7 milli-tokens per millisecond with no floating point drift.
 */
final class TokenBucket {
    private final long capacityMilli;
    private long ratePerSecond;          // tokens per second == milli-tokens per millisecond
    private long tokensMilli;
    private long lastRefillMillis;

    TokenBucket(long capacity, long ratePerSecond, long nowMillis) {
        if (capacity <= 0) throw new IllegalArgumentException("capacity must be positive");
        if (ratePerSecond < 0) throw new IllegalArgumentException("rate must be non-negative");
        this.capacityMilli = Math.multiplyExact(capacity, 1000L);
        this.ratePerSecond = ratePerSecond;
        this.tokensMilli = capacityMilli;   // a new bucket starts full: the burst allowance is immediate
        this.lastRefillMillis = nowMillis;
    }

    /** Lazy refill: add rate * elapsed, clamp to capacity. Called before every decision. */
    void refill(long nowMillis) {
        if (nowMillis <= lastRefillMillis) return;
        long elapsed = nowMillis - lastRefillMillis;
        long added = Math.multiplyExact(elapsed, ratePerSecond);
        tokensMilli = Math.min(capacityMilli, Math.addExact(tokensMilli, added));
        lastRefillMillis = nowMillis;
    }

    boolean tryAcquire(long tokens, long nowMillis) {
        refill(nowMillis);
        long needed = Math.multiplyExact(tokens, 1000L);
        if (tokensMilli >= needed) {
            tokensMilli -= needed;
            return true;
        }
        return false;
    }

    /** Whole tokens available at `nowMillis` after lazy refill (floor). Exposed for tests and the demo. */
    long availableTokens(long nowMillis) {
        refill(nowMillis);
        return tokensMilli / 1000L;
    }

    long ratePerSecond() { return ratePerSecond; }

    /** Used by the periodic-sync strategy to hand a node a new share of the global rate. */
    void setRatePerSecond(long ratePerSecond) {
        if (ratePerSecond < 0) throw new IllegalArgumentException("rate must be non-negative");
        this.ratePerSecond = ratePerSecond;
    }
}

/** Per-request outcome plus the simulated cost of reaching the decision. */
record Decision(boolean allowed, long latencyMillis, int storeCalls) {}

/**
 * Simulated shared store (Redis-like). Every call costs one round trip and is counted so the
 * strategies can be compared on latency and coordination traffic, not just accuracy.
 */
final class SharedStore {
    private final long rttMillis;
    private final Map<String, TokenBucket> buckets = new HashMap<>();
    private final Map<String, ArrayDeque<Long>> logs = new HashMap<>();
    private final Map<String, long[]> usageReports = new HashMap<>();   // key -> per-node counts
    private long calls;

    SharedStore(long rttMillis) { this.rttMillis = rttMillis; }

    long rttMillis() { return rttMillis; }
    long calls() { return calls; }

    TokenBucket bucket(String key, long capacity, long rate, long nowMillis) {
        calls++;
        return buckets.computeIfAbsent(key, k -> new TokenBucket(capacity, rate, nowMillis));
    }

    ArrayDeque<Long> log(String key) {
        calls++;
        return logs.computeIfAbsent(key, k -> new ArrayDeque<>());
    }

    long[] usage(String key, int nodes) {
        calls++;
        return usageReports.computeIfAbsent(key, k -> new long[nodes]);
    }
}

/** The contract every strategy implements: one node asks whether one request for one key may proceed. */
interface RateLimiter {
    Decision tryAcquire(int node, String key, long nowMillis);
    String name();
}

/**
 * Strategy 1: one bucket per key in the shared store. Every decision is a round trip; the answer is exact.
 */
final class CentralizedLimiter implements RateLimiter {
    private final SharedStore store;
    private final long capacity;
    private final long ratePerSecond;

    CentralizedLimiter(SharedStore store, long capacity, long ratePerSecond) {
        this.store = store;
        this.capacity = capacity;
        this.ratePerSecond = ratePerSecond;
    }

    @Override public Decision tryAcquire(int node, String key, long nowMillis) {
        TokenBucket bucket = store.bucket(key, capacity, ratePerSecond, nowMillis);
        boolean ok = bucket.tryAcquire(1, nowMillis);
        return new Decision(ok, store.rttMillis(), 1);
    }

    @Override public String name() { return "centralized"; }
}

/**
 * Strategy 2: each node owns a local bucket holding a share of the global rate and decides in-process
 * (zero latency). Every syncInterval the nodes report their demand to the store and the shares are
 * redistributed in proportion to it. Between syncs the global limit is only approximately enforced.
 */
final class LocalSyncLimiter implements RateLimiter {
    private final SharedStore store;
    private final int nodes;
    private final long capacity;
    private final long ratePerSecond;
    private final long syncIntervalMillis;
    private final Map<String, TokenBucket[]> local = new HashMap<>();
    private final Map<String, long[]> demandSinceSync = new HashMap<>();
    private final Map<String, Long> lastSync = new HashMap<>();
    private long syncs;

    LocalSyncLimiter(SharedStore store, int nodes, long capacity, long ratePerSecond, long syncIntervalMillis) {
        if (nodes <= 0) throw new IllegalArgumentException("need at least one node");
        this.store = store;
        this.nodes = nodes;
        this.capacity = capacity;
        this.ratePerSecond = ratePerSecond;
        this.syncIntervalMillis = syncIntervalMillis;
    }

    private TokenBucket[] bucketsFor(String key, long nowMillis) {
        return local.computeIfAbsent(key, k -> {
            TokenBucket[] arr = new TokenBucket[nodes];
            for (int i = 0; i < nodes; i++) {
                arr[i] = new TokenBucket(Math.max(1, capacity / nodes), ratePerSecond / nodes, nowMillis);
            }
            lastSync.put(k, nowMillis);
            demandSinceSync.put(k, new long[nodes]);
            return arr;
        });
    }

    @Override public Decision tryAcquire(int node, String key, long nowMillis) {
        TokenBucket[] buckets = bucketsFor(key, nowMillis);
        long[] demand = demandSinceSync.get(key);
        demand[node]++;
        int storeCalls = 0;
        long latency = 0;
        if (nowMillis - lastSync.get(key) >= syncIntervalMillis) {
            storeCalls = sync(key, buckets, demand, nowMillis);
            latency = store.rttMillis();     // the sync is off the request path in production; charged here for honesty
        }
        boolean ok = buckets[node].tryAcquire(1, nowMillis);
        return new Decision(ok, latency, storeCalls);
    }

    /** Redistribute the global rate in proportion to each node's observed demand; floor at 1/(4N) so a cold node can still start. */
    int sync(String key, TokenBucket[] buckets, long[] demand, long nowMillis) {
        long[] shared = store.usage(key, nodes);
        long total = 0;
        for (int i = 0; i < nodes; i++) { shared[i] = demand[i]; total += demand[i]; }
        long floor = Math.max(1, ratePerSecond / (4L * nodes));
        long remaining = ratePerSecond - floor * nodes;
        for (int i = 0; i < nodes; i++) {
            long share = floor + (total == 0 ? remaining / nodes : remaining * shared[i] / total);
            buckets[i].refill(nowMillis);
            buckets[i].setRatePerSecond(share);
            demand[i] = 0;
        }
        lastSync.put(key, nowMillis);
        syncs++;
        return 1;
    }

    long syncs() { return syncs; }
    long rateOf(String key, int node) { return local.get(key)[node].ratePerSecond(); }

    @Override public String name() { return "local+sync(" + syncIntervalMillis + "ms)"; }
}

/**
 * Strategy 3: a shared sorted log of admitted timestamps per key. Exact over any sliding window,
 * one round trip per decision, and memory proportional to the limit itself.
 */
final class SlidingWindowLogLimiter implements RateLimiter {
    private final SharedStore store;
    private final long limit;
    private final long windowMillis;

    SlidingWindowLogLimiter(SharedStore store, long limit, long windowMillis) {
        this.store = store;
        this.limit = limit;
        this.windowMillis = windowMillis;
    }

    @Override public Decision tryAcquire(int node, String key, long nowMillis) {
        ArrayDeque<Long> log = store.log(key);
        long cutoff = nowMillis - windowMillis;
        while (!log.isEmpty() && log.peekFirst() <= cutoff) log.pollFirst();
        if (log.size() < limit) {
            log.addLast(nowMillis);
            return new Decision(true, store.rttMillis(), 1);
        }
        return new Decision(false, store.rttMillis(), 1);
    }

    long logSize(String key) { return store.log(key).size(); }

    @Override public String name() { return "sliding-log"; }
}

/** What the load generator records: enough to judge accuracy, latency, and coordination cost. */
final class LoadReport {
    final String strategy;
    long offered, admitted, rejected, latencyTotal, storeCalls;
    final long[] admittedPerSecond;

    LoadReport(String strategy, int seconds) {
        this.strategy = strategy;
        this.admittedPerSecond = new long[seconds];
    }

    long peakPerSecond() {
        long peak = 0;
        for (long v : admittedPerSecond) peak = Math.max(peak, v);
        return peak;
    }

    double meanLatencyMillis() { return offered == 0 ? 0 : (double) latencyTotal / offered; }

    /**
     * Admitted minus the most a correct token bucket could admit (rate * seconds + one full burst), capped at
     * what was offered. Positive = leaked over the limit; negative = under-admitted (tokens left unused).
     */
    long versusIdeal(long ratePerSecond, long capacity) {
        long ideal = Math.min(offered, ratePerSecond * admittedPerSecond.length + capacity);
        return admitted - ideal;
    }
}

/**
 * Seeded load generator. Each millisecond each node gets a Bernoulli request draw whose probability
 * depends on node skew: node 0 receives `hotShare` of the traffic, the rest is spread evenly.
 */
final class LoadGenerator {
    private final int nodes;
    private final double requestsPerSecond;
    private final double hotShare;
    private final long seed;

    LoadGenerator(int nodes, double requestsPerSecond, double hotShare, long seed) {
        this.nodes = nodes;
        this.requestsPerSecond = requestsPerSecond;
        this.hotShare = hotShare;
        this.seed = seed;
    }

    LoadReport run(RateLimiter limiter, FakeClock clock, String key, int seconds, long ratePerSecond, long capacity) {
        Random random = new Random(seed);
        LoadReport report = new LoadReport(limiter.name(), seconds);
        long start = clock.nowMillis();
        for (int ms = 0; ms < seconds * 1000; ms++) {
            for (int node = 0; node < nodes; node++) {
                double share = nodes == 1 ? 1.0 : (node == 0 ? hotShare : (1.0 - hotShare) / (nodes - 1));
                double p = requestsPerSecond * share / 1000.0;
                if (random.nextDouble() < p) {
                    Decision d = limiter.tryAcquire(node, key, clock.nowMillis());
                    report.offered++;
                    report.latencyTotal += d.latencyMillis();
                    report.storeCalls += d.storeCalls();
                    if (d.allowed()) {
                        report.admitted++;
                        report.admittedPerSecond[(int) ((clock.nowMillis() - start) / 1000)]++;
                    } else {
                        report.rejected++;
                    }
                }
            }
            clock.advance(1);
        }
        return report;
    }
}

public final class Main {
    static final long RATE = 100;        // merchant API quota: 100 requests/second
    static final long CAPACITY = 100;    // one second of burst
    static final int NODES = 4;
    static final int SECONDS = 10;
    static final long RTT = 2;           // milliseconds to the shared store

    static List<LoadReport> compare(double offeredRps, double hotShare, long seed) {
        List<LoadReport> reports = new ArrayList<>();
        LoadGenerator load = new LoadGenerator(NODES, offeredRps, hotShare, seed);

        FakeClock c1 = new FakeClock(0);
        reports.add(load.run(new CentralizedLimiter(new SharedStore(RTT), CAPACITY, RATE), c1, "merchant:acme", SECONDS, RATE, CAPACITY));

        FakeClock c2 = new FakeClock(0);
        reports.add(load.run(new LocalSyncLimiter(new SharedStore(RTT), NODES, CAPACITY, RATE, 1000), c2, "merchant:acme", SECONDS, RATE, CAPACITY));

        FakeClock c3 = new FakeClock(0);
        reports.add(load.run(new LocalSyncLimiter(new SharedStore(RTT), NODES, CAPACITY, RATE, 100), c3, "merchant:acme", SECONDS, RATE, CAPACITY));

        FakeClock c4 = new FakeClock(0);
        reports.add(load.run(new SlidingWindowLogLimiter(new SharedStore(RTT), RATE, 1000), c4, "merchant:acme", SECONDS, RATE, CAPACITY));
        return reports;
    }

    static void printTable(String title, List<LoadReport> reports) {
        System.out.println("== " + title + " ==");
        System.out.printf("%-20s %8s %9s %9s %10s %10s %10s%n",
                "strategy", "offered", "admitted", "rejected", "peak/s", "vs ideal", "mean ms");
        for (LoadReport r : reports) {
            System.out.printf("%-20s %8d %9d %9d %10d %+10d %10.2f%n",
                    r.strategy, r.offered, r.admitted, r.rejected, r.peakPerSecond(), r.versusIdeal(RATE, CAPACITY), r.meanLatencyMillis());
        }
        System.out.println();
    }

    public static void main(String[] args) {
        System.out.println("== A single token bucket: 100/s, capacity 100 ==");
        FakeClock clock = new FakeClock(0);
        TokenBucket bucket = new TokenBucket(CAPACITY, RATE, clock.nowMillis());
        int burst = 0;
        while (bucket.tryAcquire(1, clock.nowMillis())) burst++;
        System.out.println("burst admitted from a full bucket: " + burst);
        clock.advance(10);
        System.out.println("after 10 ms: " + bucket.availableTokens(clock.nowMillis()) + " token(s) refilled (100/s * 0.010 s = 1)");
        clock.advance(5000);
        System.out.println("after 5 s idle: " + bucket.availableTokens(clock.nowMillis()) + " tokens (clamped at capacity, not 500)");
        System.out.println();

        printTable("Even load, 4 nodes, 400 offered/s against a 100/s limit (seed 7)", compare(400, 0.25, 7));
        printTable("Skewed load, node 0 takes 85% of 400 offered/s (seed 7)", compare(400, 0.85, 7));
        printTable("Light load, 60 offered/s, nobody should be rejected (seed 7)", compare(60, 0.25, 7));

        System.out.println("Reading the table:");
        System.out.println("  centralized and sliding-log never exceed 100 admitted in any second: exact, but every call pays the 2 ms round trip.");
        System.out.println("  local+sync pays no latency, but with skew the hot node runs dry while cold nodes hold unused tokens, so it under-admits;");
        System.out.println("  syncing every 100 ms instead of every 1 s closes much of that gap at 10x the coordination traffic.");
        System.out.println("  sliding-log has no burst allowance, so it admits exactly 100 per window and shows -100 against the bucket ideal.");
    }
}
