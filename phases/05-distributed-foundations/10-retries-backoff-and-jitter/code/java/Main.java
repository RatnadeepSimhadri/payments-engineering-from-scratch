// Lesson: phases/05-distributed-foundations/10-retries-backoff-and-jitter/docs/en.md
// Retries, Exponential Backoff, and Jitter
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: AWS Architecture Blog "Exponential Backoff and Jitter" (Brooker, 2015); Finagle RetryBudget docs;
// Google SRE Book ch. 22 "Addressing Cascading Failures". Java 21, standard library only.

import java.util.ArrayList;
import java.util.Arrays;
import java.util.Comparator;
import java.util.List;
import java.util.PriorityQueue;

/**
 * Deterministic xorshift64* generator. Every language in this lesson uses the same algorithm and the
 * same seed, so the retry-storm histogram is identical in Java, Python, and Go. Never use this for
 * anything security-sensitive; it exists so tests and demos are replayable.
 */
final class Rng {
    private long state;

    Rng(long seed) {
        this.state = seed == 0 ? 0x9E3779B97F4A7C15L : seed;
    }

    long nextLong() {
        long x = state;
        x ^= x >>> 12;
        x ^= x << 25;
        x ^= x >>> 27;
        state = x;
        return x * 0x2545F4914F6CDD1DL;
    }

    /** Uniform integer in [0, bound). The modulo bias is irrelevant for a backoff delay. */
    long nextBelow(long bound) {
        if (bound <= 0) throw new IllegalArgumentException("bound must be positive: " + bound);
        return Long.remainderUnsigned(nextLong(), bound);
    }
}

/** A clock the retrier sleeps against. Tests and demos use FakeClock so no wall time passes. */
interface Clock {
    long nowMs();
    void sleep(long ms);
}

final class FakeClock implements Clock {
    private long now;

    FakeClock(long startMs) { this.now = startMs; }

    @Override public long nowMs() { return now; }

    @Override public void sleep(long ms) {
        if (ms < 0) throw new IllegalArgumentException("cannot sleep a negative duration");
        now += ms;
    }
}

/** How the random component of a delay is drawn. Names follow Brooker's 2015 post. */
enum Jitter {
    NONE,          // delay = exp(attempt): every client that failed together retries together
    FULL,          // delay = uniform[0, exp]: the widest spread, the lowest peak load
    EQUAL,         // delay = exp/2 + uniform[0, exp/2]: never retries sooner than half the backoff
    DECORRELATED,  // delay = min(cap, uniform[base, 3 * previous]): spread grows with history, not with attempt count
}

/**
 * What a call to the payment processor came back with. Retryability is a property of the outcome,
 * decided once here, not re-decided by every caller. A retry of a timed-out payment is only safe
 * when the request carries an idempotency key (Lesson 09); this enum assumes it does.
 */
enum Outcome {
    OK(false),
    TIMEOUT(true),           // no response: the processor may or may not have done the work
    CONNECTION_RESET(true),  // transport failed before or after the request was read
    RATE_LIMITED(true),      // HTTP 429: the processor asked us to slow down, so retry only with backoff
    SERVER_ERROR(true),      // HTTP 503/502: transient on the processor side
    BAD_REQUEST(false),      // HTTP 400: our payload is wrong; the same bytes will fail the same way
    UNAUTHORIZED(false),     // HTTP 401: our credentials are wrong; retrying is a brute-force attempt
    CARD_DECLINED(false);    // a valid business answer, not an error: the issuer said no

    private final boolean retryable;

    Outcome(boolean retryable) { this.retryable = retryable; }

    boolean retryable() { return retryable; }
}

/**
 * The schedule: how long to wait before retry number {@code attempt} (0 = the first retry).
 * exp(attempt) = min(cap, base * 2^attempt); jitter decides how much of that interval is random.
 */
record RetryPolicy(long baseMs, long capMs, int maxAttempts, Jitter jitter) {

    RetryPolicy {
        if (baseMs <= 0) throw new IllegalArgumentException("baseMs must be positive");
        if (capMs < baseMs) throw new IllegalArgumentException("capMs must be >= baseMs");
        if (maxAttempts < 1) throw new IllegalArgumentException("maxAttempts must be >= 1");
    }

    /** Pure exponential backoff with the cap applied; doubling stops as soon as the cap is reached. */
    long exponential(int attempt) {
        long delay = baseMs;
        for (int i = 0; i < attempt && delay < capMs; i++) delay = Math.min(capMs, delay * 2);
        return Math.min(capMs, delay);
    }

    /**
     * The delay before retry {@code attempt}. {@code previousMs} is the previous delay (or baseMs for
     * the first retry); only DECORRELATED reads it. Every mode returns a value in [0, capMs].
     */
    long delay(int attempt, long previousMs, Rng rng) {
        long exp = exponential(attempt);
        return switch (jitter) {
            case NONE -> exp;
            case FULL -> rng.nextBelow(exp + 1);
            case EQUAL -> exp / 2 + rng.nextBelow(exp / 2 + 1);
            case DECORRELATED -> {
                long upper = Math.min(capMs, Math.max(baseMs, previousMs) * 3);
                yield baseMs + rng.nextBelow(upper - baseMs + 1);
            }
        };
    }

    /** The full sequence of delays this policy would produce if every attempt failed: maxAttempts - 1 entries. */
    List<Long> schedule(Rng rng) {
        List<Long> delays = new ArrayList<>();
        long previous = baseMs;
        for (int attempt = 0; attempt < maxAttempts - 1; attempt++) {
            long d = delay(attempt, previous, rng);
            delays.add(d);
            previous = d;
        }
        return delays;
    }
}

/**
 * A retry budget in the style of Finagle's RetryBudget: every first attempt deposits
 * {@code percentCanRetry}% of a token, every retry withdraws a whole token. Under a sustained
 * outage the budget empties and retries stop, so the retry load can never exceed the
 * configured fraction of the organic load. Integer millitokens, so no float drift.
 */
final class RetryBudget {
    private static final long TOKEN = 1000;
    private final long depositMillis;
    private final long maxBalance;
    private long balance;

    RetryBudget(int percentCanRetry, int maxTokens) {
        if (percentCanRetry < 0 || percentCanRetry > 100) throw new IllegalArgumentException("percent must be 0..100");
        if (maxTokens < 1) throw new IllegalArgumentException("maxTokens must be >= 1");
        this.depositMillis = TOKEN * percentCanRetry / 100;
        this.maxBalance = TOKEN * maxTokens;
    }

    void deposit() { balance = Math.min(maxBalance, balance + depositMillis); }

    boolean tryWithdraw() {
        if (balance < TOKEN) return false;
        balance -= TOKEN;
        return true;
    }

    /** Whole tokens currently available. */
    long tokens() { return balance / TOKEN; }
}

enum StopReason { SUCCEEDED, NON_RETRYABLE, ATTEMPTS_EXHAUSTED, BUDGET_EXHAUSTED }

record RetryResult(Outcome finalOutcome, int attempts, long totalDelayMs, StopReason stopReason, List<Long> delays) {
    boolean succeeded() { return stopReason == StopReason.SUCCEEDED; }
}

/** The thing being retried: given the attempt number (0 = first try) it returns an Outcome. */
@FunctionalInterface
interface Operation {
    Outcome call(int attempt);
}

/** Runs an Operation under a policy, a budget (optional), an injected clock, and a seeded Rng. */
final class Retrier {
    private final RetryPolicy policy;
    private final RetryBudget budget;   // may be null: no budget
    private final Clock clock;
    private final Rng rng;

    Retrier(RetryPolicy policy, RetryBudget budget, Clock clock, Rng rng) {
        this.policy = policy;
        this.budget = budget;
        this.clock = clock;
        this.rng = rng;
    }

    RetryResult execute(Operation op) {
        if (budget != null) budget.deposit();
        List<Long> delays = new ArrayList<>();
        long totalDelay = 0;
        long previous = policy.baseMs();
        Outcome outcome = Outcome.TIMEOUT;
        for (int attempt = 0; attempt < policy.maxAttempts(); attempt++) {
            outcome = op.call(attempt);
            if (outcome == Outcome.OK) {
                return new RetryResult(outcome, attempt + 1, totalDelay, StopReason.SUCCEEDED, delays);
            }
            if (!outcome.retryable()) {
                return new RetryResult(outcome, attempt + 1, totalDelay, StopReason.NON_RETRYABLE, delays);
            }
            if (attempt == policy.maxAttempts() - 1) break;
            if (budget != null && !budget.tryWithdraw()) {
                return new RetryResult(outcome, attempt + 1, totalDelay, StopReason.BUDGET_EXHAUSTED, delays);
            }
            long d = policy.delay(attempt, previous, rng);
            clock.sleep(d);
            delays.add(d);
            totalDelay += d;
            previous = d;
        }
        return new RetryResult(outcome, policy.maxAttempts(), totalDelay, StopReason.ATTEMPTS_EXHAUSTED, delays);
    }
}

/**
 * What the processor saw during one run of the storm simulation: attempts per time bucket, retries per
 * bucket (attempt > 0), and how every client ended up.
 */
record StormReport(long[] loadPerBucket, long[] retriesPerBucket, int succeeded, int gaveUp, int stillWaiting,
                   long totalAttempts, long bucketMs) {

    /** Highest number of calls that landed in any single bucket, including the initial burst at t=0. */
    long peakLoad() { return Arrays.stream(loadPerBucket).max().orElse(0); }

    /** Highest number of calls in any bucket that starts at or after {@code fromMs}: the load the recovered processor sees. */
    long peakLoadFrom(long fromMs) {
        long peak = 0;
        for (int i = 0; i < loadPerBucket.length; i++) {
            if (i * bucketMs >= fromMs) peak = Math.max(peak, loadPerBucket[i]);
        }
        return peak;
    }

    /** Number of buckets that received at least one call: how spread out the load was. */
    int busyBuckets() {
        int n = 0;
        for (long l : loadPerBucket) if (l > 0) n++;
        return n;
    }

    String histogram(long scale) {
        StringBuilder sb = new StringBuilder();
        for (int i = 0; i < loadPerBucket.length; i++) {
            if (loadPerBucket[i] == 0) continue;
            long bars = (loadPerBucket[i] + scale - 1) / scale;
            sb.append(String.format("%5d ms | %4d | ", i * bucketMs, loadPerBucket[i]));
            for (int b = 0; b < bars; b++) sb.append('#');
            sb.append('\n');
        }
        return sb.toString();
    }
}

/**
 * A retry storm in virtual time, as a discrete-event simulation. N clients all send a payment at t=0
 * (a deploy, a cron, a cache expiry). The processor is unreachable until outageMs, then healthy but able
 * to serve only capacityPerBucket calls per bucket of bucketMs; calls over capacity time out. Each client
 * follows the policy. Events are processed in (time, client) order from a min-heap, so the RNG is drawn in
 * a fixed order and the whole run is replayable from its seed, in all three languages.
 */
final class RetryStorm {
    record Event(long at, int client) {}

    static StormReport run(int clients, long capacityPerBucket, long bucketMs, long outageMs,
                           int horizonBuckets, RetryPolicy policy, long seed) {
        Rng rng = new Rng(seed);
        PriorityQueue<Event> queue = new PriorityQueue<>(
                Comparator.comparingLong(Event::at).thenComparingInt(Event::client));
        for (int c = 0; c < clients; c++) queue.add(new Event(0, c));

        long[] load = new long[horizonBuckets];
        long[] retries = new long[horizonBuckets];
        long[] served = new long[horizonBuckets];
        long[] previousDelay = new long[clients];
        int[] attemptsMade = new int[clients];
        Arrays.fill(previousDelay, policy.baseMs());
        long horizonMs = (long) horizonBuckets * bucketMs;
        int succeeded = 0, gaveUp = 0;
        long totalAttempts = 0;

        while (!queue.isEmpty() && queue.peek().at() < horizonMs) {
            Event e = queue.poll();
            int c = e.client();
            int bucket = (int) (e.at() / bucketMs);
            load[bucket]++;
            if (attemptsMade[c] > 0) retries[bucket]++;
            attemptsMade[c]++;
            totalAttempts++;
            boolean healthy = e.at() >= outageMs;
            if (healthy && served[bucket] < capacityPerBucket) {
                served[bucket]++;
                succeeded++;
                continue;
            }
            if (attemptsMade[c] >= policy.maxAttempts()) {
                gaveUp++;
                continue;
            }
            long d = policy.delay(attemptsMade[c] - 1, previousDelay[c], rng);
            previousDelay[c] = d;
            queue.add(new Event(e.at() + Math.max(d, 1), c));   // a zero delay still lands strictly later
        }
        return new StormReport(load, retries, succeeded, gaveUp, queue.size(), totalAttempts, bucketMs);
    }
}

public final class Main {
    public static void main(String[] args) {
        System.out.println("== The schedule: base 100 ms, cap 10 s, 6 attempts, seed 42 ==");
        for (Jitter j : Jitter.values()) {
            RetryPolicy p = new RetryPolicy(100, 10_000, 6, j);
            System.out.printf("%-13s %s%n", j, p.schedule(new Rng(42)));
        }
        System.out.printf("%-13s %s  (cap reached at attempt 4)%n", "NONE, cap 1 s",
                new RetryPolicy(100, 1_000, 8, Jitter.NONE).schedule(new Rng(42)));

        System.out.println("\n== One payment, processor flapping: TIMEOUT, TIMEOUT, OK ==");
        FakeClock clock = new FakeClock(0);
        Retrier retrier = new Retrier(new RetryPolicy(100, 10_000, 5, Jitter.NONE), null, clock, new Rng(7));
        Outcome[] script = {Outcome.TIMEOUT, Outcome.TIMEOUT, Outcome.OK};
        RetryResult r = retrier.execute(attempt -> script[Math.min(attempt, script.length - 1)]);
        System.out.printf("result=%s attempts=%d waited=%d ms delays=%s clock=%d ms%n",
                r.stopReason(), r.attempts(), r.totalDelayMs(), r.delays(), clock.nowMs());

        System.out.println("\n== Non-retryable: the issuer declined; a retry would be a second authorization ==");
        RetryResult declined = retrier.execute(attempt -> Outcome.CARD_DECLINED);
        System.out.printf("result=%s attempts=%d waited=%d ms%n", declined.stopReason(), declined.attempts(), declined.totalDelayMs());

        System.out.println("\n== Retry budget: 20% of organic traffic may be retries, max 10 tokens banked ==");
        RetryBudget budget = new RetryBudget(20, 10);
        Retrier budgeted = new Retrier(new RetryPolicy(100, 1_000, 3, Jitter.NONE), budget, new FakeClock(0), new Rng(1));
        for (int i = 0; i < 50; i++) budgeted.execute(attempt -> Outcome.OK);   // healthy traffic banks tokens
        System.out.printf("after 50 healthy requests: %d tokens banked%n", budget.tokens());
        for (int i = 1; i <= 8; i++) {
            RetryResult rr = budgeted.execute(attempt -> Outcome.SERVER_ERROR);
            System.out.printf("outage request %d: attempts=%d stop=%-18s tokens left=%d%n",
                    i, rr.attempts(), rr.stopReason(), budget.tokens());
        }

        System.out.println("\n== Retry storm: 1000 clients, processor down 1 s, then 200 calls per 100 ms bucket, 8 attempts ==");
        for (Jitter j : Jitter.values()) {
            RetryPolicy p = new RetryPolicy(100, 5_000, 8, j);
            StormReport report = RetryStorm.run(1000, 200, 100, 1_000, 60, p, 2024);
            System.out.printf("%n-- %s: peak after recovery=%d/bucket busy buckets=%d succeeded=%d gave up=%d waiting=%d attempts=%d%n",
                    j, report.peakLoadFrom(1_000), report.busyBuckets(), report.succeeded(), report.gaveUp(),
                    report.stillWaiting(), report.totalAttempts());
            if (j == Jitter.NONE || j == Jitter.FULL) System.out.print(report.histogram(25));
        }
    }
}
