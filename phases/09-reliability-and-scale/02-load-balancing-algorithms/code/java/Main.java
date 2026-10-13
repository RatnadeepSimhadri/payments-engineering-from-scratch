// Lesson: phases/09-reliability-and-scale/02-load-balancing-algorithms/docs/en.md
// Load Balancing: Round Robin, Least Connections, Power of Two Choices
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: Mitzenmacher, "The Power of Two Choices in Randomized Load Balancing" (2001);
//          Karger et al., "Consistent Hashing and Random Trees" (1997); nginx smooth weighted round robin.
// Java 21, standard library only. Same design as the Python and Go versions.

import java.util.ArrayDeque;
import java.util.ArrayList;
import java.util.Arrays;
import java.util.HashMap;
import java.util.List;
import java.util.Map;

/** SplitMix64: a tiny seeded generator that produces identical streams in Java, Python, and Go. */
final class Rng {
    private long state;

    Rng(long seed) { this.state = seed; }

    long nextLong() {
        state += 0x9E3779B97F4A7C15L;
        long z = state;
        z = (z ^ (z >>> 30)) * 0xBF58476D1CE4E5B9L;
        z = (z ^ (z >>> 27)) * 0x94D049BB133111EBL;
        return z ^ (z >>> 31);
    }

    /** Uniform in [0, 1) with 53 bits of precision. */
    double nextDouble() { return (nextLong() >>> 11) * 0x1.0p-53; }

    /** Uniform integer in [0, bound). */
    int nextInt(int bound) { return (int) (nextDouble() * bound); }

    /** Exponentially distributed with the given mean; the classic model of request work and inter-arrival gaps. */
    double nextExponential(double mean) { return -mean * Math.log(1.0 - nextDouble()); }
}

/** A server behind the balancer. Mutable because its load changes with every routed request. */
final class Backend {
    final String id;
    final int weight;          // relative capacity for weighted strategies
    final double speed;        // service-time divisor: 2.0 finishes work twice as fast as 1.0
    int activeConnections;     // requests assigned and not yet completed
    long totalAssigned;        // requests ever routed here
    boolean healthy = true;    // flipped by the HealthChecker, never by a strategy

    Backend(String id, int weight, double speed) {
        if (weight <= 0) throw new IllegalArgumentException("weight must be positive: " + id);
        if (speed <= 0) throw new IllegalArgumentException("speed must be positive: " + id);
        this.id = id;
        this.weight = weight;
        this.speed = speed;
    }

    Backend(String id) { this(id, 1, 1.0); }

    @Override public String toString() { return id; }
}

/** One unit of work: who it is for (the affinity key) and how much work it carries, in milliseconds on a speed-1.0 backend. */
record Request(long id, String key, double workMs) {}

/** A balancing strategy chooses among the currently healthy backends. It never mutates them. */
interface Balancer {
    String name();
    Backend pick(List<Backend> healthy, Request request);
}

/** Plain round robin: the i-th request goes to backend i mod n. Ignores weight, speed, and load. */
final class RoundRobin implements Balancer {
    private long counter;

    public String name() { return "round-robin"; }

    public Backend pick(List<Backend> healthy, Request request) {
        if (healthy.isEmpty()) throw new IllegalStateException("no healthy backends");
        Backend b = healthy.get((int) (counter % healthy.size()));
        counter++;
        return b;
    }
}

/**
 * Smooth weighted round robin (the nginx algorithm). Each pick adds every backend's weight to its
 * running score, chooses the highest score, and subtracts the total weight from the winner.
 * Weights 5:1:1 yield a a b a c a a rather than a a a a a b c.
 */
final class WeightedRoundRobin implements Balancer {
    private final Map<String, Long> current = new HashMap<>();

    public String name() { return "weighted-round-robin"; }

    public Backend pick(List<Backend> healthy, Request request) {
        if (healthy.isEmpty()) throw new IllegalStateException("no healthy backends");
        long total = 0;
        Backend best = null;
        long bestScore = Long.MIN_VALUE;
        for (Backend b : healthy) {
            long score = current.getOrDefault(b.id, 0L) + b.weight;
            current.put(b.id, score);
            total += b.weight;
            if (score > bestScore) { bestScore = score; best = b; }
        }
        current.put(best.id, bestScore - total);
        return best;
    }
}

/** Least connections: the backend with the fewest in-flight requests; ties go to the earliest in the list. */
final class LeastConnections implements Balancer {
    public String name() { return "least-connections"; }

    public Backend pick(List<Backend> healthy, Request request) {
        if (healthy.isEmpty()) throw new IllegalStateException("no healthy backends");
        Backend best = healthy.get(0);
        for (Backend b : healthy) if (b.activeConnections < best.activeConnections) best = b;
        return best;
    }
}

/**
 * Power of two choices: sample two distinct backends uniformly at random and take the less loaded one.
 * Costs two reads instead of n, and bounds the maximum load at log log n instead of log n / log log n.
 */
final class PowerOfTwoChoices implements Balancer {
    private final Rng rng;

    PowerOfTwoChoices(Rng rng) { this.rng = rng; }

    public String name() { return "power-of-two-choices"; }

    public Backend pick(List<Backend> healthy, Request request) {
        if (healthy.isEmpty()) throw new IllegalStateException("no healthy backends");
        if (healthy.size() == 1) return healthy.get(0);
        int i = rng.nextInt(healthy.size());
        int j = rng.nextInt(healthy.size() - 1);
        if (j >= i) j++;                                   // distinct second sample
        Backend a = healthy.get(i), b = healthy.get(j);
        return b.activeConnections < a.activeConnections ? b : a;
    }
}

/**
 * Consistent hashing with virtual nodes: the same key always lands on the same backend, and removing a
 * backend moves only the keys that were on it. Used for session or cache affinity, not for load spreading.
 */
final class ConsistentHash implements Balancer {
    final int virtualNodes;
    private long[] ringHashes = new long[0];
    private Backend[] ringOwners = new Backend[0];
    private List<String> builtFor = List.of();

    ConsistentHash(int virtualNodes) {
        if (virtualNodes <= 0) throw new IllegalArgumentException("virtualNodes must be positive");
        this.virtualNodes = virtualNodes;
    }

    public String name() { return "consistent-hash"; }

    /** FNV-1a, 64-bit: five lines and identical in every language. Its high bits barely depend on the last byte. */
    static long fnv1a64(String s) {
        long h = 0xcbf29ce484222325L;
        for (byte b : s.getBytes(java.nio.charset.StandardCharsets.UTF_8)) {
            h ^= (b & 0xff);
            h *= 0x100000001b3L;
        }
        return h;
    }

    /** MurmurHash3's 64-bit finaliser: spreads every input bit across the whole word. */
    static long fmix64(long z) {
        z ^= z >>> 33;
        z *= 0xff51afd7ed558ccdL;
        z ^= z >>> 33;
        z *= 0xc4ceb9fe1a85ec53L;
        z ^= z >>> 33;
        return z;
    }

    /** The ring hash: FNV-1a for bytes-to-word, fmix64 so "merchant-1" and "merchant-2" land far apart. */
    static long hash64(String s) { return fmix64(fnv1a64(s)); }

    private void rebuild(List<Backend> healthy) {
        int n = healthy.size() * virtualNodes;
        long[] hashes = new long[n];
        Backend[] owners = new Backend[n];
        Integer[] order = new Integer[n];
        int k = 0;
        for (Backend b : healthy) {
            for (int v = 0; v < virtualNodes; v++) {
                hashes[k] = hash64(b.id + "#" + v);
                owners[k] = b;
                order[k] = k;
                k++;
            }
        }
        Arrays.sort(order, (x, y) -> Long.compareUnsigned(hashes[x], hashes[y]));
        ringHashes = new long[n];
        ringOwners = new Backend[n];
        for (int i = 0; i < n; i++) { ringHashes[i] = hashes[order[i]]; ringOwners[i] = owners[order[i]]; }
        List<String> ids = new ArrayList<>();
        for (Backend b : healthy) ids.add(b.id);
        builtFor = ids;
    }

    public Backend pick(List<Backend> healthy, Request request) {
        if (healthy.isEmpty()) throw new IllegalStateException("no healthy backends");
        List<String> ids = new ArrayList<>();
        for (Backend b : healthy) ids.add(b.id);
        if (!ids.equals(builtFor)) rebuild(healthy);
        long h = hash64(request.key());
        // first ring position whose hash is >= h, wrapping to 0 (binary search on unsigned values)
        int lo = 0, hi = ringHashes.length;
        while (lo < hi) {
            int mid = (lo + hi) >>> 1;
            if (Long.compareUnsigned(ringHashes[mid], h) < 0) lo = mid + 1; else hi = mid;
        }
        return ringOwners[lo == ringHashes.length ? 0 : lo];
    }
}

/**
 * Passive health checking: N consecutive failures evict a backend, M consecutive successes readmit it.
 * Consecutive, not cumulative, so one flaky response in a thousand does not take a server out.
 */
final class HealthChecker {
    final int failThreshold;
    final int successThreshold;
    private final Map<String, Integer> consecutiveFailures = new HashMap<>();
    private final Map<String, Integer> consecutiveSuccesses = new HashMap<>();

    HealthChecker(int failThreshold, int successThreshold) {
        if (failThreshold <= 0 || successThreshold <= 0) throw new IllegalArgumentException("thresholds must be positive");
        this.failThreshold = failThreshold;
        this.successThreshold = successThreshold;
    }

    /** Returns true if the backend's health flag changed as a result of this observation. */
    boolean record(Backend b, boolean success) {
        if (success) {
            consecutiveFailures.put(b.id, 0);
            int s = consecutiveSuccesses.merge(b.id, 1, Integer::sum);
            if (!b.healthy && s >= successThreshold) { b.healthy = true; return true; }
        } else {
            consecutiveSuccesses.put(b.id, 0);
            int f = consecutiveFailures.merge(b.id, 1, Integer::sum);
            if (b.healthy && f >= failThreshold) { b.healthy = false; return true; }
        }
        return false;
    }
}

/** The balancer plus its backends plus the health checker: the thing a request actually hits. */
final class Pool {
    final List<Backend> backends;
    final Balancer balancer;
    final HealthChecker health;

    Pool(List<Backend> backends, Balancer balancer, HealthChecker health) {
        if (backends.isEmpty()) throw new IllegalArgumentException("pool needs at least one backend");
        this.backends = List.copyOf(backends);
        this.balancer = balancer;
        this.health = health;
    }

    List<Backend> healthy() {
        List<Backend> out = new ArrayList<>();
        for (Backend b : backends) if (b.healthy) out.add(b);
        return out;
    }

    /** Routes the request, or returns null when every backend is evicted (the caller sheds load). */
    Backend route(Request request) {
        List<Backend> h = healthy();
        if (h.isEmpty()) return null;
        Backend b = balancer.pick(h, request);
        b.activeConnections++;
        b.totalAssigned++;
        return b;
    }

    void complete(Backend b, boolean success) {
        b.activeConnections--;
        health.record(b, success);
    }
}

/** What one simulation run measured. Latencies are in milliseconds. */
record SimResult(String strategy, int requests, int failed, int shed, double meanMs, double p50Ms, double p99Ms,
                 int maxQueue, long maxAssigned, long minAssigned) {
    String line() {
        return String.format("%-22s mean %8.2f  p50 %7.2f  p99 %9.2f  max-queue %4d  assigned %4d..%-5d failed %d  shed %d",
                strategy, meanMs, p50Ms, p99Ms, maxQueue, minAssigned, maxAssigned, failed, shed);
    }
}

/**
 * A discrete-event simulation: every backend is a single FIFO server, so a request's latency is the time
 * it waits behind the requests already queued there plus its own service time. Each backend's queue
 * length is exactly its activeConnections, which is what least-connections and P2C read.
 */
final class Simulation {
    final List<Backend> backends;
    final Pool pool;
    final double meanArrivalGapMs;
    final double meanWorkMs;
    final Rng rng;
    /** Backend id -> time at which it is down (fails every request). Null means never. */
    final Map<String, double[]> outages = new HashMap<>();

    private final Map<String, ArrayDeque<double[]>> inFlight = new HashMap<>(); // per backend: [completionTime, success]
    private final Map<String, Double> freeAt = new HashMap<>();

    Simulation(List<Backend> backends, Balancer balancer, HealthChecker health,
               double meanArrivalGapMs, double meanWorkMs, Rng rng) {
        this.backends = backends;
        this.pool = new Pool(backends, balancer, health);
        this.meanArrivalGapMs = meanArrivalGapMs;
        this.meanWorkMs = meanWorkMs;
        this.rng = rng;
        for (Backend b : backends) { inFlight.put(b.id, new ArrayDeque<>()); freeAt.put(b.id, 0.0); }
    }

    /** Declares that a backend fails every request between from and to (milliseconds). */
    void outage(String backendId, double fromMs, double toMs) { outages.put(backendId, new double[]{fromMs, toMs}); }

    private boolean isDown(Backend b, double now) {
        double[] w = outages.get(b.id);
        return w != null && now >= w[0] && now < w[1];
    }

    private void retireCompleted(double now) {
        for (Backend b : backends) {
            ArrayDeque<double[]> q = inFlight.get(b.id);
            while (!q.isEmpty() && q.peekFirst()[0] <= now) {
                double[] done = q.pollFirst();
                pool.complete(b, done[1] == 1.0);
            }
        }
    }

    /**
     * Active probing: an evicted backend receives no traffic, so passive observation alone could never
     * readmit it. Every probeIntervalMs the simulation sends a synthetic request to each evicted backend.
     */
    final double probeIntervalMs = 500;
    private final Map<String, Double> nextProbe = new HashMap<>();

    private void probeEvicted(double now) {
        for (Backend b : backends) {
            if (b.healthy) continue;
            double due = nextProbe.getOrDefault(b.id, 0.0);
            if (now >= due) {
                pool.health.record(b, !isDown(b, now));
                nextProbe.put(b.id, now + probeIntervalMs);
            }
        }
    }

    SimResult run(int requests, int keySpace) {
        double now = 0;
        double[] latencies = new double[requests];
        int recorded = 0, failed = 0, shed = 0, maxQueue = 0;
        for (int i = 0; i < requests; i++) {
            now += rng.nextExponential(meanArrivalGapMs);
            double work = rng.nextExponential(meanWorkMs);
            String key = "merchant-" + rng.nextInt(keySpace);
            retireCompleted(now);
            probeEvicted(now);
            Backend b = pool.route(new Request(i, key, work));
            if (b == null) { shed++; continue; }
            maxQueue = Math.max(maxQueue, b.activeConnections);
            boolean down = isDown(b, now);
            double service = down ? 1.0 : work / b.speed;               // a dead backend fails fast
            double start = Math.max(now, freeAt.get(b.id));
            double done = start + service;
            freeAt.put(b.id, done);
            inFlight.get(b.id).addLast(new double[]{done, down ? 0.0 : 1.0});
            if (down) failed++; else latencies[recorded++] = done - now;
        }
        double[] lat = Arrays.copyOf(latencies, recorded);
        Arrays.sort(lat);
        double sum = 0;
        for (double l : lat) sum += l;
        long maxAssigned = Long.MIN_VALUE, minAssigned = Long.MAX_VALUE;
        for (Backend b : backends) { maxAssigned = Math.max(maxAssigned, b.totalAssigned); minAssigned = Math.min(minAssigned, b.totalAssigned); }
        return new SimResult(pool.balancer.name(), recorded, failed, shed, recorded == 0 ? 0 : sum / recorded,
                percentile(lat, 50), percentile(lat, 99), maxQueue, maxAssigned, minAssigned);
    }

    /** Nearest-rank percentile on a sorted array. */
    static double percentile(double[] sorted, double p) {
        if (sorted.length == 0) return 0;
        int rank = (int) Math.ceil(p / 100.0 * sorted.length);
        return sorted[Math.max(0, Math.min(sorted.length - 1, rank - 1))];
    }
}

public final class Main {

    /** Balls into bins: the maximum load after dropping balls into bins with d random choices each. */
    static int ballsInBins(int balls, int bins, int choices, Rng rng) {
        int[] load = new int[bins];
        int max = 0;
        for (int i = 0; i < balls; i++) {
            int best = rng.nextInt(bins);
            for (int c = 1; c < choices; c++) {
                int cand = rng.nextInt(bins);
                if (load[cand] < load[best]) best = cand;
            }
            load[best]++;
            max = Math.max(max, load[best]);
        }
        return max;
    }

    static List<Backend> fleet() {
        // five fast servers and one that is half as fast: the shape of a fleet mid-rollout or with a noisy neighbour
        return List.of(new Backend("auth-1", 2, 1.0), new Backend("auth-2", 2, 1.0), new Backend("auth-3", 2, 1.0),
                new Backend("auth-4", 2, 1.0), new Backend("auth-5", 2, 1.0), new Backend("auth-6", 1, 0.5));
    }

    static SimResult simulate(Balancer balancer, long seed, boolean withOutage) {
        List<Backend> fleet = fleet();
        Simulation sim = new Simulation(fleet, balancer, new HealthChecker(3, 2), 2.2, 10.0, new Rng(seed));
        if (withOutage) sim.outage("auth-3", 5_000, 8_000);
        return sim.run(20_000, 500);
    }

    public static void main(String[] args) {
        System.out.println("== Balls into bins: 10,000 balls, 10,000 bins ==");
        for (int d = 1; d <= 3; d++) {
            System.out.printf("%d choice(s): max load %d%n", d, ballsInBins(10_000, 10_000, d, new Rng(7)));
        }

        System.out.println();
        System.out.println("== Authorization fleet: 5 fast servers + 1 at half speed, 20,000 requests, ~83% utilisation ==");
        List<Balancer> strategies = List.of(new RoundRobin(), new WeightedRoundRobin(), new LeastConnections(),
                new PowerOfTwoChoices(new Rng(11)), new ConsistentHash(100));
        for (Balancer s : strategies) System.out.println(simulate(s, 42, false).line());

        System.out.println();
        System.out.println("== Same fleet, auth-3 dies from t=5s to t=8s; health checker evicts after 3 failures ==");
        List<Balancer> withHealth = List.of(new RoundRobin(), new LeastConnections(), new PowerOfTwoChoices(new Rng(11)));
        for (Balancer s : withHealth) System.out.println(simulate(s, 42, true).line());

        System.out.println();
        System.out.println("== Smooth weighted round robin, weights a=5 b=1 c=1 ==");
        WeightedRoundRobin wrr = new WeightedRoundRobin();
        List<Backend> abc = List.of(new Backend("a", 5, 1), new Backend("b", 1, 1), new Backend("c", 1, 1));
        StringBuilder seq = new StringBuilder();
        for (int i = 0; i < 7; i++) seq.append(wrr.pick(abc, new Request(i, "k", 1)).id).append(' ');
        System.out.println(seq.toString().trim());

        System.out.println();
        System.out.println("== Consistent hashing: remove one of 6 backends, how many of 1,000 keys move? ==");
        ConsistentHash ch = new ConsistentHash(100);
        List<Backend> six = fleet();
        List<Backend> five = new ArrayList<>(six);
        five.remove(2);
        int moved = 0;
        for (int k = 0; k < 1000; k++) {
            Request r = new Request(k, "merchant-" + k, 1);
            if (ch.pick(six, r) != ch.pick(five, r)) moved++;
        }
        System.out.printf("moved %d of 1000 keys (ideal 1/6 = 167); modulo hashing would move ~833%n", moved);
    }
}
