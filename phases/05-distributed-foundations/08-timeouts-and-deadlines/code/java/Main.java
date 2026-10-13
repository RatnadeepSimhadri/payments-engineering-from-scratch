// Lesson: phases/05-distributed-foundations/08-timeouts-and-deadlines/docs/en.md
// Timeouts and Deadline Propagation
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: gRPC "grpc-timeout" header (PROTOCOL-HTTP2.md); Dean & Barroso, "The Tail at Scale" (CACM 2013); Go context package docs.
// Java 21, standard library only. Time is injected through Clock; nothing sleeps.

import java.util.ArrayList;
import java.util.Arrays;
import java.util.List;
import java.util.Random;

/** Source of time. Production uses System.currentTimeMillis(); demos and tests use FakeClock. */
interface Clock {
    long nowMs();
}

/** A clock that only moves when the simulation says so. */
final class FakeClock implements Clock {
    private long now;

    FakeClock(long startMs) { this.now = startMs; }

    @Override public long nowMs() { return now; }

    void advance(long ms) {
        if (ms < 0) throw new IllegalArgumentException("cannot move time backwards: " + ms);
        now += ms;
    }
}

/** Thrown when a call is refused or abandoned because its deadline has passed or cannot be met. */
final class DeadlineExceededException extends RuntimeException {
    final long remainingMs;

    DeadlineExceededException(String message, long remainingMs) {
        super(message);
        this.remainingMs = remainingMs;
    }
}

/** Thrown by a simulated dependency that is up but failing. Retryable. */
final class UnavailableException extends RuntimeException {
    UnavailableException(String message) { super(message); }
}

/**
 * An absolute point in time by which work must be finished. A timeout is relative ("500 ms from now");
 * a deadline is absolute ("by t = 1500"). Deadlines compose across a call chain, timeouts do not.
 */
record Deadline(long atMs) {
    /** No deadline. remainingMs() reports Long.MAX_VALUE and it never expires. */
    static final Deadline NONE = new Deadline(Long.MAX_VALUE);

    static Deadline after(Clock clock, long timeoutMs) {
        if (timeoutMs < 0) throw new IllegalArgumentException("timeout must be >= 0: " + timeoutMs);
        return new Deadline(saturatingAdd(clock.nowMs(), timeoutMs));
    }

    static long saturatingAdd(long a, long b) {
        long r = a + b;
        return ((a ^ r) & (b ^ r)) < 0 ? Long.MAX_VALUE : r;   // overflow iff both operands disagree in sign with the result
    }

    boolean isNone() { return atMs == Long.MAX_VALUE; }

    /** Milliseconds left, clamped at zero. Never negative, so callers can subtract margins safely. */
    long remainingMs(Clock clock) {
        if (isNone()) return Long.MAX_VALUE;
        return Math.max(0, atMs - clock.nowMs());
    }

    boolean isExpired(Clock clock) { return !isNone() && clock.nowMs() >= atMs; }

    /** The tighter of two deadlines. A child may shorten its parent's deadline, never extend it. */
    Deadline earliest(Deadline other) { return atMs <= other.atMs ? this : other; }
}

/**
 * What travels with a request down the call chain: an identifier for logs and the deadline.
 * Modelled on Go's context.Context and gRPC's per-call deadline.
 */
record Context(String requestId, Deadline deadline) {

    static Context root(String requestId) { return new Context(requestId, Deadline.NONE); }

    /** Derive a child context whose deadline is min(parent, now + timeout). Tightening only. */
    Context withTimeout(Clock clock, long timeoutMs) {
        return new Context(requestId, deadline.earliest(Deadline.after(clock, timeoutMs)));
    }

    /** What a service that drops the deadline effectively passes downstream. The bug this lesson is about. */
    Context withoutDeadline() { return new Context(requestId, Deadline.NONE); }

    long remainingMs(Clock clock) { return deadline.remainingMs(clock); }

    /** Call before any expensive or irreversible step. */
    void checkpoint(Clock clock, String where) {
        if (deadline.isExpired(clock)) {
            throw new DeadlineExceededException(where + ": deadline already passed", 0);
        }
    }

    /**
     * Refuse work that cannot finish in the budget. Cheaper than starting it and being abandoned,
     * and it is the step that prevents a side effect nobody will ever read the result of.
     */
    void requireBudget(Clock clock, long neededMs, String where) {
        long remaining = remainingMs(clock);
        if (remaining < neededMs) {
            throw new DeadlineExceededException(
                    where + " needs " + neededMs + " ms but only " + remaining + " ms remain", remaining);
        }
    }

    /**
     * Encode the remaining budget as a relative duration for the wire, in the shape of gRPC's grpc-timeout
     * header: 1-8 ASCII digits followed by a unit (H, M, S, m, u, n). Relative, not absolute, because the
     * receiver's clock is not ours (Phase 5, Lesson 04). Returns null when there is no deadline.
     */
    String toWireTimeout(Clock clock) {
        if (deadline.isNone()) return null;
        return remainingMs(clock) + "m";
    }

    /** Rebuild a context on the receiving side from the wire header against the receiver's own clock. */
    static Context fromWire(String requestId, String header, Clock clock) {
        if (header == null) return root(requestId);
        if (header.length() < 2 || header.length() > 9) throw new IllegalArgumentException("bad timeout header: " + header);
        char unit = header.charAt(header.length() - 1);
        long value = Long.parseLong(header.substring(0, header.length() - 1));
        long ms = switch (unit) {
            case 'H' -> value * 3_600_000L;
            case 'M' -> value * 60_000L;
            case 'S' -> value * 1_000L;
            case 'm' -> value;
            case 'u' -> value / 1_000L;
            case 'n' -> value / 1_000_000L;
            default -> throw new IllegalArgumentException("bad timeout unit: " + unit);
        };
        return root(requestId).withTimeout(clock, ms);
    }
}

/** Append-only record of irreversible things the simulated services did, with the time they did them. */
final class EffectLog {
    record Effect(long atMs, String what) {
        @Override public String toString() { return "t=" + atMs + "ms " + what; }
    }

    private final List<Effect> effects = new ArrayList<>();

    void record(long atMs, String what) { effects.add(new Effect(atMs, what)); }

    List<Effect> all() { return List.copyOf(effects); }

    boolean contains(String fragment) { return effects.stream().anyMatch(e -> e.what().contains(fragment)); }
}

/** Something that can be called with a context. */
interface Service {
    String call(Context ctx);
}

/**
 * A simulated remote service. Receiving the request costs one network hop; doing the work costs workMs;
 * if there is a downstream it is called next; a side effect (if any) is recorded once the work is done.
 * The response costs one more hop, even when the response is an error.
 */
final class RemoteService implements Service {
    static final long HOP_MS = 20;

    final String name;
    final long workMs;
    final String sideEffect;      // null when the service is pure
    final Service downstream;     // null at the end of the chain
    final boolean propagate;      // false models a service that forgets to forward the deadline
    private final FakeClock clock;
    private final EffectLog log;

    RemoteService(String name, long workMs, String sideEffect, Service downstream, boolean propagate,
                  FakeClock clock, EffectLog log) {
        this.name = name;
        this.workMs = workMs;
        this.sideEffect = sideEffect;
        this.downstream = downstream;
        this.propagate = propagate;
        this.clock = clock;
        this.log = log;
    }

    @Override public String call(Context incoming) {
        clock.advance(HOP_MS);                                     // request on the wire
        // What arrives is a header, not an object: rebuild the context against this service's clock.
        Context ctx = Context.fromWire(incoming.requestId(), incoming.toWireTimeout(clock), clock);
        try {
            ctx.requireBudget(clock, workMs, name);                 // admission: refuse what cannot finish
            clock.advance(workMs);
            if (downstream != null) {
                Context forwarded = propagate ? ctx : ctx.withoutDeadline();
                downstream.call(forwarded);
            }
            if (sideEffect != null) log.record(clock.nowMs(), name + ": " + sideEffect);
            return name + " ok";
        } finally {
            clock.advance(HOP_MS);                                 // response (or error) on the wire
        }
    }
}

/** How a call ended from the caller's point of view. */
enum Status {
    OK,            // response arrived before the deadline
    FAILED_FAST,   // a DeadlineExceededException came back before the deadline: no work was wasted
    TIMED_OUT,     // the caller stopped waiting; the response, if any, arrived to nobody
    UNAVAILABLE    // the dependency answered with a retryable error
}

record CallOutcome(Status status, long elapsedMs, String detail) {}

/**
 * The edge: the client that sets the top-level deadline. It is the only place a timeout is chosen;
 * everything below derives its budget from this one number.
 */
final class Client {
    private final FakeClock clock;

    Client(FakeClock clock) { this.clock = clock; }

    CallOutcome invoke(Service service, String requestId, long timeoutMs) {
        long start = clock.nowMs();
        Context ctx = Context.root(requestId).withTimeout(clock, timeoutMs);
        try {
            String result = service.call(ctx);
            long elapsed = clock.nowMs() - start;
            // The response may have arrived after we stopped listening. Then it is a timeout, whatever it says.
            return elapsed > timeoutMs
                    ? new CallOutcome(Status.TIMED_OUT, elapsed, "response discarded: arrived " + (elapsed - timeoutMs) + " ms late")
                    : new CallOutcome(Status.OK, elapsed, result);
        } catch (DeadlineExceededException e) {
            long elapsed = clock.nowMs() - start;
            return elapsed > timeoutMs
                    ? new CallOutcome(Status.TIMED_OUT, elapsed, e.getMessage())
                    : new CallOutcome(Status.FAILED_FAST, elapsed, e.getMessage());
        } catch (UnavailableException e) {
            return new CallOutcome(Status.UNAVAILABLE, clock.nowMs() - start, e.getMessage());
        }
    }
}

/** A dependency that fails its first N calls with UnavailableException, then succeeds. Each call costs latencyMs. */
final class FlakyService implements Service {
    private final FakeClock clock;
    private final long latencyMs;
    private int failuresLeft;
    int calls = 0;

    FlakyService(FakeClock clock, long latencyMs, int failBefore) {
        this.clock = clock;
        this.latencyMs = latencyMs;
        this.failuresLeft = failBefore;
    }

    @Override public String call(Context ctx) {
        calls++;
        ctx.requireBudget(clock, latencyMs, "flaky");
        clock.advance(latencyMs);
        if (failuresLeft > 0) {
            failuresLeft--;
            throw new UnavailableException("503 from flaky (call " + calls + ")");
        }
        return "flaky ok after " + calls + " call(s)";
    }
}

record RetryOutcome(Status status, int attempts, long elapsedMs, List<Long> attemptTimeoutsMs) {}

/**
 * Retries that live inside a deadline. Each attempt gets min(perAttemptTimeout, remaining), and no attempt
 * starts when the remaining budget is below the floor a useful attempt needs. Contrast with a retry loop
 * that counts attempts and knows nothing about the caller's deadline.
 */
final class DeadlineAwareRetrier {
    private final FakeClock clock;
    private final long perAttemptTimeoutMs;
    private final long backoffMs;
    private final long minUsefulAttemptMs;

    DeadlineAwareRetrier(FakeClock clock, long perAttemptTimeoutMs, long backoffMs, long minUsefulAttemptMs) {
        this.clock = clock;
        this.perAttemptTimeoutMs = perAttemptTimeoutMs;
        this.backoffMs = backoffMs;
        this.minUsefulAttemptMs = minUsefulAttemptMs;
    }

    RetryOutcome call(Service service, Context ctx) {
        long start = clock.nowMs();
        List<Long> budgets = new ArrayList<>();
        int attempts = 0;
        while (true) {
            long remaining = ctx.remainingMs(clock);
            if (remaining < minUsefulAttemptMs) {
                return new RetryOutcome(Status.FAILED_FAST, attempts, clock.nowMs() - start, budgets);
            }
            long attemptBudget = Math.min(perAttemptTimeoutMs, remaining);
            budgets.add(attemptBudget);
            attempts++;
            Context attempt = ctx.withTimeout(clock, attemptBudget);
            try {
                service.call(attempt);
                return new RetryOutcome(Status.OK, attempts, clock.nowMs() - start, budgets);
            } catch (UnavailableException e) {
                long sleep = Math.min(backoffMs, ctx.remainingMs(clock));   // never sleep past the deadline
                clock.advance(sleep);
            } catch (DeadlineExceededException e) {
                return new RetryOutcome(Status.FAILED_FAST, attempts, clock.nowMs() - start, budgets);
            }
        }
    }
}

/**
 * Hedged request: send to the primary; if no answer within hedgeAfterMs, also send to a backup and take
 * whichever answers first. Only legal for idempotent reads (Phase 5, Lesson 09 covers why).
 */
final class Hedger {
    record Result(long latencyMs, int requestsSent, String winner) {}

    static Result call(long primaryLatencyMs, long backupLatencyMs, long hedgeAfterMs) {
        if (primaryLatencyMs <= hedgeAfterMs) {
            return new Result(primaryLatencyMs, 1, "primary");
        }
        long hedgedArrival = hedgeAfterMs + backupLatencyMs;
        return primaryLatencyMs <= hedgedArrival
                ? new Result(primaryLatencyMs, 2, "primary")
                : new Result(hedgedArrival, 2, "backup");
    }

    /** Draw a latency with a long tail: most calls are fast, a few percent hit a stall. Seeded, so reproducible. */
    static long sampleLatency(Random rng) {
        return rng.nextInt(100) < 5 ? 600 + rng.nextInt(400) : 20 + rng.nextInt(40);
    }

    static long percentile(long[] sorted, double p) {
        int idx = (int) Math.ceil(p * sorted.length) - 1;
        return sorted[Math.max(0, Math.min(sorted.length - 1, idx))];
    }

    record Study(long p50, long p99, long p99Hedged, double extraLoadPercent) {}

    static Study study(long seed, int samples, long hedgeAfterMs) {
        Random rng = new Random(seed);
        long[] plain = new long[samples];
        long[] hedged = new long[samples];
        long extraRequests = 0;
        for (int i = 0; i < samples; i++) {
            long primary = sampleLatency(rng);
            long backup = sampleLatency(rng);
            plain[i] = primary;
            Result r = call(primary, backup, hedgeAfterMs);
            hedged[i] = r.latencyMs();
            extraRequests += r.requestsSent() - 1;
        }
        Arrays.sort(plain);
        Arrays.sort(hedged);
        return new Study(percentile(plain, 0.50), percentile(plain, 0.99), percentile(hedged, 0.99),
                100.0 * extraRequests / samples);
    }
}

public final class Main {

    /** checkout -> payments -> processor, where the processor charges a card. */
    static Service buildChain(FakeClock clock, EffectLog log, boolean propagate) {
        Service processor = new RemoteService("processor", 400, "CHARGE 19.99 USD card 4242", null, propagate, clock, log);
        Service payments = new RemoteService("payments", 50, null, processor, propagate, clock, log);
        return new RemoteService("checkout", 50, null, payments, propagate, clock, log);
    }

    public static void main(String[] args) {
        System.out.println("== A deadline is absolute; a timeout is relative ==");
        FakeClock clock = new FakeClock(1_000);
        Context ctx = Context.root("req-1").withTimeout(clock, 500);
        System.out.println("now=" + clock.nowMs() + " deadline=" + ctx.deadline().atMs() + " remaining=" + ctx.remainingMs(clock));
        clock.advance(180);
        System.out.println("now=" + clock.nowMs() + " remaining=" + ctx.remainingMs(clock) + " wire header=" + ctx.toWireTimeout(clock));
        Context child = ctx.withTimeout(clock, 1_000);
        System.out.println("child asked for 1000 ms, got " + child.remainingMs(clock) + " ms (a child can only tighten)");
        clock.advance(400);
        System.out.println("now=" + clock.nowMs() + " remaining=" + ctx.remainingMs(clock) + " expired=" + ctx.deadline().isExpired(clock));

        System.out.println();
        System.out.println("== The downstream call that outlives its caller ==");
        FakeClock c1 = new FakeClock(0);
        EffectLog log1 = new EffectLog();
        CallOutcome o1 = new Client(c1).invoke(buildChain(c1, log1, false), "pay-1", 500);
        System.out.println("no propagation : " + o1.status() + " after " + o1.elapsedMs() + " ms; " + o1.detail());
        for (EffectLog.Effect e : log1.all()) System.out.println("  side effect: " + e);
        System.out.println("  customer saw a timeout and will retry; the card was charged anyway.");

        FakeClock c2 = new FakeClock(0);
        EffectLog log2 = new EffectLog();
        CallOutcome o2 = new Client(c2).invoke(buildChain(c2, log2, true), "pay-2", 500);
        System.out.println("propagation    : " + o2.status() + " after " + o2.elapsedMs() + " ms; " + o2.detail());
        System.out.println("  side effects recorded: " + log2.all().size());

        FakeClock c3 = new FakeClock(0);
        EffectLog log3 = new EffectLog();
        CallOutcome o3 = new Client(c3).invoke(buildChain(c3, log3, true), "pay-3", 700);
        System.out.println("budget 700 ms  : " + o3.status() + " after " + o3.elapsedMs() + " ms; " + o3.detail());
        for (EffectLog.Effect e : log3.all()) System.out.println("  side effect: " + e);

        System.out.println();
        System.out.println("== Retries live inside the deadline, not beside it ==");
        FakeClock c4 = new FakeClock(0);
        FlakyService flaky4 = new FlakyService(c4, 150, 2);
        RetryOutcome r4 = new DeadlineAwareRetrier(c4, 300, 50, 50)
                .call(flaky4, Context.root("rt-1").withTimeout(c4, 1_000));
        System.out.println("deadline 1000 ms: " + r4.status() + " after " + r4.attempts() + " attempts, " + r4.elapsedMs()
                + " ms, per-attempt budgets " + r4.attemptTimeoutsMs());
        FakeClock c5 = new FakeClock(0);
        FlakyService flaky5 = new FlakyService(c5, 150, 2);
        RetryOutcome r5 = new DeadlineAwareRetrier(c5, 300, 50, 50)
                .call(flaky5, Context.root("rt-2").withTimeout(c5, 400));
        System.out.println("deadline  400 ms: " + r5.status() + " after " + r5.attempts() + " attempts, " + r5.elapsedMs()
                + " ms, per-attempt budgets " + r5.attemptTimeoutsMs() + " (third attempt never started)");

        System.out.println();
        System.out.println("== Hedged requests cut the tail for a few percent more load ==");
        Hedger.Result h1 = Hedger.call(800, 40, 60);
        System.out.println("primary 800 ms, backup 40 ms, hedge after 60 ms -> " + h1.latencyMs() + " ms from " + h1.winner()
                + " using " + h1.requestsSent() + " requests");
        Hedger.Result h2 = Hedger.call(30, 40, 60);
        System.out.println("primary  30 ms                                  -> " + h2.latencyMs() + " ms from " + h2.winner()
                + " using " + h2.requestsSent() + " request");
        Hedger.Study s = Hedger.study(42, 10_000, 60);
        System.out.printf("10000 calls, seed 42: p50=%d ms p99=%d ms; hedged p99=%d ms; extra load=%.1f%%%n",
                s.p50(), s.p99(), s.p99Hedged(), s.extraLoadPercent());
    }
}
