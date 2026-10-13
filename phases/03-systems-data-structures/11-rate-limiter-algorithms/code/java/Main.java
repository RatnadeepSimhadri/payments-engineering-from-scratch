// Lesson: phases/03-systems-data-structures/11-rate-limiter-algorithms/docs/en.md
// Rate Limiter Algorithms: Token Bucket, Leaky Bucket, Sliding Window
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: RFC 6585 (429 Too Many Requests); RFC 9110 section 10.2.3 (Retry-After); ATM Forum TM 4.0 / ITU-T I.371
//          (the Generic Cell Rate Algorithm, which the leaky bucket here is); Cloudflare (2017) on the sliding window counter.
// Java 21, standard library only.

import java.util.ArrayDeque;
import java.util.ArrayList;
import java.util.Collections;
import java.util.List;

/** Something that tells the time in milliseconds. Injected so tests never sleep. */
interface Clock {
    long nowMillis();
}

final class ManualClock implements Clock {
    private long now;

    ManualClock() { this(0); }

    ManualClock(long startMillis) { this.now = startMillis; }

    @Override
    public long nowMillis() { return now; }

    void advance(long millis) {
        if (millis < 0) throw new IllegalArgumentException("clock cannot go backwards");
        now += millis;
    }

    void set(long millis) {
        if (millis < now) throw new IllegalArgumentException("clock cannot go backwards");
        now = millis;
    }
}

/**
 * allowed: serve the request. retryAfterMillis: when denied, the earliest time a retry can succeed
 * (becomes the Retry-After header, rounded up to whole seconds). delayMillis: when allowed by a
 * shaper, how long the request waits before it is processed.
 */
record Decision(boolean allowed, long retryAfterMillis, long delayMillis) {
    static Decision allow() { return new Decision(true, 0, 0); }

    static Decision allow(long delayMillis) { return new Decision(true, 0, delayMillis); }

    static Decision deny(long retryAfterMillis) { return new Decision(false, Math.max(1, retryAfterMillis), 0); }

    /** Retry-After is delta-seconds; always round up so the client never retries early. */
    long retryAfterHeader() { return (retryAfterMillis + 999) / 1000; }
}

interface RateLimiter {
    String name();

    Decision tryAcquire();
}

final class Params {
    static final long SCALE = 1000; // tokens and water are tracked in thousandths so refill per millisecond is exact

    private Params() {}

    static void requirePositive(String name, long value) {
        if (value <= 0) throw new IllegalArgumentException(name + " must be positive: " + value);
    }

    static long ceilDiv(long a, long b) { return (a + b - 1) / b; }
}

/**
 * 1. Token bucket: capacity tokens, refilled continuously at refillPerSecond. A request takes one
 * token. Bursts up to capacity are admitted instantly; the long-run rate is refillPerSecond.
 */
final class TokenBucket implements RateLimiter {
    final long capacity;
    final long refillPerSecond;
    private final Clock clock;
    private long tokens;          // in thousandths
    private long lastRefill;

    TokenBucket(long capacity, long refillPerSecond, Clock clock) {
        Params.requirePositive("capacity", capacity);
        Params.requirePositive("refillPerSecond", refillPerSecond);
        this.capacity = capacity;
        this.refillPerSecond = refillPerSecond;
        this.clock = clock;
        this.tokens = capacity * Params.SCALE;   // start full
        this.lastRefill = clock.nowMillis();
    }

    private void refill() {
        long now = clock.nowMillis();
        long elapsed = now - lastRefill;
        if (elapsed > 0) {
            // refillPerSecond tokens/s == refillPerSecond thousandths per ms: exact integer math
            tokens = Math.min(capacity * Params.SCALE, tokens + elapsed * refillPerSecond);
            lastRefill = now;
        }
    }

    double tokens() {
        refill();
        return tokens / (double) Params.SCALE;
    }

    @Override
    public String name() { return "token bucket"; }

    @Override
    public Decision tryAcquire() {
        refill();
        if (tokens >= Params.SCALE) {
            tokens -= Params.SCALE;
            return Decision.allow();
        }
        long deficit = Params.SCALE - tokens;
        return Decision.deny(Params.ceilDiv(deficit, refillPerSecond));
    }
}

/**
 * 2. Leaky bucket, a shaper: requests leave at exactly leakPerSecond; up to queueCapacity may wait.
 * Implemented with virtual scheduling (the GCRA): nextSlot is the time the next request will be released.
 */
final class LeakyBucket implements RateLimiter {
    final long queueCapacity;
    final long intervalMillis;
    private final Clock clock;
    private long nextSlot;

    LeakyBucket(long queueCapacity, long leakPerSecond, Clock clock) {
        Params.requirePositive("queueCapacity", queueCapacity);
        Params.requirePositive("leakPerSecond", leakPerSecond);
        if (1000 % leakPerSecond != 0) {
            throw new IllegalArgumentException("leakPerSecond must divide 1000 so the interval is a whole millisecond");
        }
        this.queueCapacity = queueCapacity;
        this.intervalMillis = 1000 / leakPerSecond;
        this.clock = clock;
        this.nextSlot = clock.nowMillis();
    }

    long queued() {
        long now = clock.nowMillis();
        return Math.max(0, Params.ceilDiv(nextSlot - now, intervalMillis));
    }

    @Override
    public String name() { return "leaky bucket"; }

    @Override
    public Decision tryAcquire() {
        long now = clock.nowMillis();
        long slot = Math.max(now, nextSlot);
        long delay = slot - now;
        if (delay >= queueCapacity * intervalMillis) {
            // the queue is full: the request would wait longer than queueCapacity slots
            return Decision.deny(delay - queueCapacity * intervalMillis + intervalMillis);
        }
        nextSlot = slot + intervalMillis;
        return Decision.allow(delay);
    }
}

/** 3. Fixed window: limit requests per window aligned to multiples of windowMillis. Cheap; admits up to 2x limit across a boundary. */
final class FixedWindow implements RateLimiter {
    final long limit;
    final long windowMillis;
    private final Clock clock;
    private long windowStart = -1;
    private long count;

    FixedWindow(long limit, long windowMillis, Clock clock) {
        Params.requirePositive("limit", limit);
        Params.requirePositive("windowMillis", windowMillis);
        this.limit = limit;
        this.windowMillis = windowMillis;
        this.clock = clock;
    }

    @Override
    public String name() { return "fixed window"; }

    @Override
    public Decision tryAcquire() {
        long now = clock.nowMillis();
        long start = now - now % windowMillis;
        if (start != windowStart) {
            windowStart = start;
            count = 0;
        }
        if (count < limit) {
            count++;
            return Decision.allow();
        }
        return Decision.deny(start + windowMillis - now);
    }
}

/** 4. Sliding window log: exact; keeps the timestamp of every admitted request in the last windowMillis. O(limit) memory per key. */
final class SlidingWindowLog implements RateLimiter {
    final long limit;
    final long windowMillis;
    private final Clock clock;
    private final ArrayDeque<Long> log = new ArrayDeque<>();

    SlidingWindowLog(long limit, long windowMillis, Clock clock) {
        Params.requirePositive("limit", limit);
        Params.requirePositive("windowMillis", windowMillis);
        this.limit = limit;
        this.windowMillis = windowMillis;
        this.clock = clock;
    }

    int size() { return log.size(); }

    @Override
    public String name() { return "sliding log"; }

    @Override
    public Decision tryAcquire() {
        long now = clock.nowMillis();
        long cutoff = now - windowMillis;
        while (!log.isEmpty() && log.peekFirst() <= cutoff) log.pollFirst();
        if (log.size() < limit) {
            log.addLast(now);
            return Decision.allow();
        }
        return Decision.deny(log.peekFirst() + windowMillis - now);
    }
}

/**
 * 5. Sliding window counter: approximate; two counters (previous and current fixed window), with the
 * previous one weighted by how much of it still overlaps the sliding window. O(1) memory.
 */
final class SlidingWindowCounter implements RateLimiter {
    final long limit;
    final long windowMillis;
    private final Clock clock;
    private long windowStart = -1;
    private long current;
    private long previous;

    SlidingWindowCounter(long limit, long windowMillis, Clock clock) {
        Params.requirePositive("limit", limit);
        Params.requirePositive("windowMillis", windowMillis);
        this.limit = limit;
        this.windowMillis = windowMillis;
        this.clock = clock;
    }

    /** Roll the windows forward to `now`; returns the elapsed time inside the current window. */
    private long roll(long now) {
        long start = now - now % windowMillis;
        if (start != windowStart) {
            previous = (start == windowStart + windowMillis) ? current : 0;   // adjacent window, or a gap
            current = 0;
            windowStart = start;
        }
        return now - start;
    }

    /** (previous * remainingOverlap + current * window): the weighted count times windowMillis, kept scaled so the comparison is exact. */
    long weightedCountScaled() {
        long elapsed = roll(clock.nowMillis());
        return previous * (windowMillis - elapsed) + current * windowMillis;
    }

    @Override
    public String name() { return "sliding counter"; }

    @Override
    public Decision tryAcquire() {
        long now = clock.nowMillis();
        long elapsed = roll(now);
        long w = windowMillis;
        long scaled = previous * (w - elapsed) + current * w;
        if (scaled + w <= limit * w) {
            current++;
            return Decision.allow();
        }
        if (current >= limit || previous == 0) return Decision.deny(w - elapsed);
        // smallest e' with previous*(w - e') + current*w + w <= limit*w
        long needed = w - ((limit - 1 - current) * w) / previous;
        return Decision.deny(Math.max(1, needed - elapsed));
    }
}

/** A trace and a replay harness shared by the demo and the tests. */
final class Trace {
    private Trace() {}

    /** Timestamps in ms. 3 s at a steady 10 rps, then at t=3000 a burst of 40 retries within 20 ms, then steady 10 rps until t=6000. */
    static List<Long> retryStormTrace() {
        List<Long> trace = new ArrayList<>();
        for (long t = 0; t < 3000; t += 100) trace.add(t);
        for (int i = 0; i < 40; i++) trace.add(3000L + (i % 20));
        for (long t = 3100; t < 6000; t += 100) trace.add(t);
        Collections.sort(trace);
        return trace;
    }

    record Limiter(RateLimiter limiter, ManualClock clock) {}

    /** Five limiters configured for the same nominal 10 requests per second, each with its own clock. */
    static List<Limiter> makeLimiters() {
        List<Limiter> out = new ArrayList<>();
        ManualClock c1 = new ManualClock(), c2 = new ManualClock(), c3 = new ManualClock(), c4 = new ManualClock(), c5 = new ManualClock();
        out.add(new Limiter(new TokenBucket(10, 10, c1), c1));
        out.add(new Limiter(new LeakyBucket(10, 10, c2), c2));
        out.add(new Limiter(new FixedWindow(10, 1000, c3), c3));
        out.add(new Limiter(new SlidingWindowLog(10, 1000, c4), c4));
        out.add(new Limiter(new SlidingWindowCounter(10, 1000, c5), c5));
        return out;
    }

    record ReplaySummary(String name, int allowed, int denied, int allowedInBurst, int maxInAnySecond, long maxRetryAfterMillis, long maxDelayMillis) {}

    /** Drive the injected clock through the trace and tally what the limiter did. */
    static ReplaySummary replay(RateLimiter limiter, ManualClock clock, List<Long> trace, long burstStart, long burstEnd) {
        List<Long> allowedTimes = new ArrayList<>();
        int denied = 0, inBurst = 0;
        long maxRetry = 0, maxDelay = 0;
        for (long t : trace) {
            clock.set(t);
            Decision d = limiter.tryAcquire();
            if (d.allowed()) {
                allowedTimes.add(t + d.delayMillis());
                maxDelay = Math.max(maxDelay, d.delayMillis());
                if (t >= burstStart && t < burstEnd) inBurst++;
            } else {
                denied++;
                maxRetry = Math.max(maxRetry, d.retryAfterMillis());
            }
        }
        return new ReplaySummary(limiter.name(), allowedTimes.size(), denied, inBurst, maxInWindow(allowedTimes, 1000), maxRetry, maxDelay);
    }

    /** Largest number of admitted requests whose (processing) times fall in any half-open window of width windowMillis. */
    static int maxInWindow(List<Long> times, long windowMillis) {
        List<Long> sorted = new ArrayList<>(times);
        Collections.sort(sorted);
        int best = 0, lo = 0;
        for (int hi = 0; hi < sorted.size(); hi++) {
            while (sorted.get(hi) - sorted.get(lo) >= windowMillis) lo++;
            best = Math.max(best, hi - lo + 1);
        }
        return best;
    }
}

public final class Main {
    public static void main(String[] args) {
        System.out.println("== Token bucket: burst now, steady later ==");
        ManualClock clock = new ManualClock();
        TokenBucket tb = new TokenBucket(5, 2, clock);
        List<Boolean> burst = new ArrayList<>();
        for (int i = 0; i < 7; i++) burst.add(tb.tryAcquire().allowed());
        System.out.println("7 requests at t=0 against capacity 5: " + burst);
        Decision d = tb.tryAcquire();
        System.out.printf("denied: retry after %d ms -> Retry-After: %d%n", d.retryAfterMillis(), d.retryAfterHeader());
        clock.advance(500);
        System.out.printf("t=500ms: tokens = %.1f, request allowed = %b%n", tb.tokens(), tb.tryAcquire().allowed());

        System.out.println("\n== Leaky bucket: admits, but spaces the output ==");
        clock = new ManualClock();
        LeakyBucket lb = new LeakyBucket(3, 10, clock);
        for (int i = 0; i < 5; i++) {
            Decision r = lb.tryAcquire();
            System.out.printf("request %d at t=0: %s%n", i, r.allowed()
                    ? "queued, runs at +" + r.delayMillis() + " ms"
                    : "rejected, Retry-After " + r.retryAfterMillis() + " ms");
        }

        System.out.println("\n== Fixed window: the 2x problem at the boundary ==");
        clock = new ManualClock();
        FixedWindow fw = new FixedWindow(10, 1000, clock);
        SlidingWindowLog log = new SlidingWindowLog(10, 1000, clock);
        int admittedFw = 0, admittedLog = 0;
        for (long t = 950; t < 1050; t += 5) {
            clock.set(t);
            if (fw.tryAcquire().allowed()) admittedFw++;
            if (log.tryAcquire().allowed()) admittedLog++;
        }
        System.out.printf("20 requests between t=950 and t=1050 ms, limit 10/s: fixed window admitted %d, sliding log admitted %d%n", admittedFw, admittedLog);

        System.out.println("\n== Sliding counter: weighting the previous window ==");
        clock = new ManualClock();
        SlidingWindowCounter sc = new SlidingWindowCounter(10, 1000, clock);
        for (long t = 0; t < 1000; t += 100) {
            clock.set(t);
            sc.tryAcquire();
        }
        clock.set(1250);
        System.out.printf("10 requests in window 0; at t=1250 weighted count = %.2f, next request allowed = %b%n", sc.weightedCountScaled() / 1000.0, sc.tryAcquire().allowed());
        clock.set(1500);
        System.out.printf("at t=1500 weighted count = %.2f, next request allowed = %b%n", sc.weightedCountScaled() / 1000.0, sc.tryAcquire().allowed());

        System.out.println("\n== The same retry storm through all five (limit 10 rps, burst of 40 at t=3.0 s) ==");
        List<Long> trace = Trace.retryStormTrace();
        System.out.printf("%d requests over 6 s: 59 steady plus 40 in the burst; a limiter at exactly 10 rps admits 59 + 10%n", trace.size());
        System.out.printf("%-16s %7s %6s %8s %6s %15s %9s%n", "limiter", "allowed", "denied", "in burst", "max/1s", "max Retry-After", "max delay");
        for (Trace.Limiter l : Trace.makeLimiters()) {
            Trace.ReplaySummary s = Trace.replay(l.limiter(), l.clock(), trace, 3000, 3020);
            System.out.printf("%-16s %7d %6d %8d %6d %12d ms %6d ms%n", s.name(), s.allowed(), s.denied(), s.allowedInBurst(), s.maxInAnySecond(), s.maxRetryAfterMillis(), s.maxDelayMillis());
        }
    }
}
