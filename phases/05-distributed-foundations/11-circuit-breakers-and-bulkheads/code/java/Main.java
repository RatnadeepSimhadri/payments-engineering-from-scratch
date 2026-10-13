// Lesson: phases/05-distributed-foundations/11-circuit-breakers-and-bulkheads/docs/en.md
// Circuit Breakers and Bulkheads
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: Nygard, "Release It!" (2007) ch. 5, Stability Patterns; Resilience4j CircuitBreaker and Bulkhead docs;
// Google SRE Book ch. 22 "Addressing Cascading Failures". Java 21, standard library only.

import java.util.ArrayList;
import java.util.Arrays;
import java.util.Collections;
import java.util.List;
import java.util.function.BooleanSupplier;

/** A clock the breaker reads. Tests and demos use FakeClock so no wall time passes and every transition is replayable. */
interface Clock {
    long nowMs();
}

final class FakeClock implements Clock {
    private long now;

    FakeClock(long startMs) { this.now = startMs; }

    @Override public long nowMs() { return now; }

    void advance(long ms) {
        if (ms < 0) throw new IllegalArgumentException("cannot advance a negative duration");
        now += ms;
    }
}

/** The three states of the breaker. There are exactly four legal transitions; see CircuitBreaker. */
enum State { CLOSED, OPEN, HALF_OPEN }

/** Why a call was or was not let through. REJECTED_* outcomes never touched the dependency. */
enum Admission { ADMITTED, REJECTED_OPEN, REJECTED_BULKHEAD }

/** The final result of one guarded call, from the caller's point of view. */
enum CallOutcome { SUCCESS, FAILURE, REJECTED_OPEN, REJECTED_BULKHEAD }

/** One recorded state change, for tests and for the operator's timeline. */
record Transition(State from, State to, long atMs) {}

/**
 * Tuning for the breaker. Every value is in the operator's vocabulary:
 * "open when at least minimumCalls calls in the last windowMs failed at failureRateThresholdPercent or worse;
 *  stay open for openDurationMs; then let halfOpenPermits probes through."
 */
record BreakerConfig(int failureRateThresholdPercent, int minimumCalls, long windowMs, long bucketMs,
                     long openDurationMs, int halfOpenPermits) {
    BreakerConfig {
        if (failureRateThresholdPercent < 1 || failureRateThresholdPercent > 100) throw new IllegalArgumentException("threshold must be 1..100");
        if (minimumCalls < 1) throw new IllegalArgumentException("minimumCalls must be >= 1");
        if (bucketMs <= 0 || windowMs < bucketMs || windowMs % bucketMs != 0) throw new IllegalArgumentException("windowMs must be a positive multiple of bucketMs");
        if (openDurationMs <= 0) throw new IllegalArgumentException("openDurationMs must be positive");
        if (halfOpenPermits < 1) throw new IllegalArgumentException("halfOpenPermits must be >= 1");
    }
}

/**
 * A time-based sliding window of call results: a ring of fixed-width buckets. A bucket is reused when the
 * clock reaches its slot again, and any bucket older than windowMs is ignored when counting. Eviction is
 * therefore implicit in the arithmetic: nothing is scheduled, nothing runs in the background.
 */
final class SlidingWindow {
    private final long windowMs;
    private final long bucketMs;
    private final long[] bucketStart;
    private final int[] successes;
    private final int[] failures;

    SlidingWindow(long windowMs, long bucketMs) {
        if (bucketMs <= 0 || windowMs < bucketMs || windowMs % bucketMs != 0) throw new IllegalArgumentException("windowMs must be a positive multiple of bucketMs");
        this.windowMs = windowMs;
        this.bucketMs = bucketMs;
        int n = (int) (windowMs / bucketMs);
        this.bucketStart = new long[n];
        this.successes = new int[n];
        this.failures = new int[n];
        Arrays.fill(bucketStart, Long.MIN_VALUE);
    }

    void record(long nowMs, boolean success) {
        long start = nowMs - Math.floorMod(nowMs, bucketMs);
        int i = (int) Math.floorMod(start / bucketMs, bucketStart.length);
        if (bucketStart[i] != start) {           // the slot belongs to an older period: reuse it
            bucketStart[i] = start;
            successes[i] = 0;
            failures[i] = 0;
        }
        if (success) successes[i]++; else failures[i]++;
    }

    private boolean live(int i, long nowMs) {
        return bucketStart[i] != Long.MIN_VALUE && nowMs - bucketStart[i] < windowMs;
    }

    int total(long nowMs) {
        int t = 0;
        for (int i = 0; i < bucketStart.length; i++) if (live(i, nowMs)) t += successes[i] + failures[i];
        return t;
    }

    int failures(long nowMs) {
        int f = 0;
        for (int i = 0; i < bucketStart.length; i++) if (live(i, nowMs)) f += failures[i];
        return f;
    }

    /** Integer percent, 0 when the window is empty. Integer arithmetic so every language agrees on the threshold comparison. */
    int failureRatePercent(long nowMs) {
        int t = total(nowMs);
        return t == 0 ? 0 : (int) (100L * failures(nowMs) / t);
    }

    void reset() {
        Arrays.fill(bucketStart, Long.MIN_VALUE);
        Arrays.fill(successes, 0);
        Arrays.fill(failures, 0);
    }
}

/**
 * The state machine. CLOSED -> OPEN when the window's failure rate crosses the threshold with enough calls;
 * OPEN -> HALF_OPEN when openDurationMs has elapsed and someone asks for a permit; HALF_OPEN -> OPEN on any
 * probe failure; HALF_OPEN -> CLOSED when halfOpenPermits probes have all succeeded. Thread-safe by a single
 * monitor: every method is short and touches only counters.
 */
final class CircuitBreaker {
    private final BreakerConfig config;
    private final Clock clock;
    private final SlidingWindow window;
    private final List<Transition> transitions = new ArrayList<>();
    private State state = State.CLOSED;
    private long openedAtMs = Long.MIN_VALUE;
    private int probesIssued = 0;
    private int probeSuccesses = 0;
    private long rejected = 0;

    CircuitBreaker(BreakerConfig config, Clock clock) {
        this.config = config;
        this.clock = clock;
        this.window = new SlidingWindow(config.windowMs(), config.bucketMs());
    }

    synchronized State state() { return state; }

    synchronized long rejected() { return rejected; }

    synchronized List<Transition> transitions() { return Collections.unmodifiableList(new ArrayList<>(transitions)); }

    synchronized int failureRatePercent() { return window.failureRatePercent(clock.nowMs()); }

    private void transitionTo(State next) {
        transitions.add(new Transition(state, next, clock.nowMs()));
        state = next;
    }

    /**
     * Ask for permission to call the dependency. This is the only place OPEN becomes HALF_OPEN: the breaker
     * has no timer thread, so the first caller after the open duration performs the transition.
     */
    synchronized boolean tryAcquire() {
        long now = clock.nowMs();
        switch (state) {
            case CLOSED -> { return true; }
            case OPEN -> {
                if (now - openedAtMs < config.openDurationMs()) { rejected++; return false; }
                transitionTo(State.HALF_OPEN);
                probesIssued = 0;
                probeSuccesses = 0;
                // fall through to the HALF_OPEN rule by re-dispatching
                return tryAcquire();
            }
            case HALF_OPEN -> {
                if (probesIssued >= config.halfOpenPermits()) { rejected++; return false; }
                probesIssued++;
                return true;
            }
        }
        throw new IllegalStateException("unreachable");
    }

    synchronized void onSuccess() {
        long now = clock.nowMs();
        switch (state) {
            case CLOSED -> window.record(now, true);
            case HALF_OPEN -> {
                probeSuccesses++;
                if (probeSuccesses >= config.halfOpenPermits()) {
                    window.reset();
                    transitionTo(State.CLOSED);
                }
            }
            case OPEN -> { /* a late result from before the trip; it says nothing about the dependency now */ }
        }
    }

    synchronized void onFailure() {
        long now = clock.nowMs();
        switch (state) {
            case CLOSED -> {
                window.record(now, false);
                if (window.total(now) >= config.minimumCalls()
                        && window.failureRatePercent(now) >= config.failureRateThresholdPercent()) {
                    openedAtMs = now;
                    transitionTo(State.OPEN);
                }
            }
            case HALF_OPEN -> {
                openedAtMs = now;
                transitionTo(State.OPEN);
            }
            case OPEN -> { /* late result; already open */ }
        }
    }
}

/**
 * A bulkhead: at most maxConcurrent calls to one dependency may be in flight at once. A hand-rolled
 * counting semaphore so the three languages share one design; java.util.concurrent.Semaphore is the
 * production equivalent. Acquisition never blocks: a full bulkhead rejects, it does not queue.
 */
final class Bulkhead {
    private final int maxConcurrent;
    private int inFlight = 0;
    private long rejected = 0;

    Bulkhead(int maxConcurrent) {
        if (maxConcurrent < 1) throw new IllegalArgumentException("maxConcurrent must be >= 1");
        this.maxConcurrent = maxConcurrent;
    }

    synchronized boolean tryAcquire() {
        if (inFlight >= maxConcurrent) { rejected++; return false; }
        inFlight++;
        return true;
    }

    synchronized void release() {
        if (inFlight == 0) throw new IllegalStateException("release without acquire");
        inFlight--;
    }

    synchronized int inFlight() { return inFlight; }

    synchronized int available() { return maxConcurrent - inFlight; }

    synchronized long rejected() { return rejected; }
}

/**
 * Breaker and bulkhead composed around one dependency. The bulkhead is checked first: a full bulkhead is a
 * statement about us (we are saturated), not about the dependency, so it must not count toward the failure
 * rate and must not consume a half-open probe. begin/complete split the call so a simulation can model calls
 * that stay in flight; call() is the synchronous convenience.
 */
final class GuardedCall {
    private final CircuitBreaker breaker;
    private final Bulkhead bulkhead;

    GuardedCall(CircuitBreaker breaker, Bulkhead bulkhead) {
        this.breaker = breaker;
        this.bulkhead = bulkhead;
    }

    Admission begin() {
        if (!bulkhead.tryAcquire()) return Admission.REJECTED_BULKHEAD;
        if (!breaker.tryAcquire()) {
            bulkhead.release();
            return Admission.REJECTED_OPEN;
        }
        return Admission.ADMITTED;
    }

    void complete(boolean success) {
        bulkhead.release();
        if (success) breaker.onSuccess(); else breaker.onFailure();
    }

    CallOutcome call(BooleanSupplier op) {
        Admission a = begin();
        if (a == Admission.REJECTED_BULKHEAD) return CallOutcome.REJECTED_BULKHEAD;
        if (a == Admission.REJECTED_OPEN) return CallOutcome.REJECTED_OPEN;
        boolean ok;
        try {
            ok = op.getAsBoolean();
        } catch (RuntimeException e) {
            ok = false;
        }
        complete(ok);
        return ok ? CallOutcome.SUCCESS : CallOutcome.FAILURE;
    }
}

/** Per-second counts of what the merchant's checkout saw, plus the state the breaker was in at the end of the second. */
record TimelineRow(long second, int success, int failure, int rejectedOpen, int rejectedBulkhead, State stateAtEnd) {}

record OutageReport(List<TimelineRow> rows, List<Transition> transitions, int totalSuccess, int totalFailure,
                    int totalRejectedOpen, int totalRejectedBulkhead) {

    int countIn(long fromMs, long toMs, CallOutcome kind) {
        int n = 0;
        for (TimelineRow r : rows) {
            long start = r.second() * 1000;
            if (start < fromMs || start >= toMs) continue;
            n += switch (kind) {
                case SUCCESS -> r.success();
                case FAILURE -> r.failure();
                case REJECTED_OPEN -> r.rejectedOpen();
                case REJECTED_BULKHEAD -> r.rejectedBulkhead();
            };
        }
        return n;
    }

    String table() {
        StringBuilder sb = new StringBuilder();
        sb.append(String.format("%4s | %4s | %4s | %8s | %8s | %s%n", "sec", "ok", "fail", "rej_open", "rej_bulk", "state"));
        for (TimelineRow r : rows) {
            sb.append(String.format("%4d | %4d | %4d | %8d | %8d | %s%n",
                    r.second(), r.success(), r.failure(), r.rejectedOpen(), r.rejectedBulkhead(), r.stateAtEnd()));
        }
        return sb.toString();
    }
}

/**
 * A card-processor outage in virtual time. The checkout sends one authorization every callIntervalMs. While
 * the processor is healthy a call completes at once with success. During the outage [outageStartMs, outageEndMs)
 * a call hangs for timeoutMs and then fails: this is what a dead processor looks like from the client, and it is
 * why the bulkhead matters. Completions are processed before the new call at the same tick, so the run is a
 * pure function of its parameters and identical in all three languages.
 */
final class OutageSimulation {
    record Pending(long dueMs) {}

    static OutageReport run(BreakerConfig config, int bulkheadSize, long callIntervalMs, long horizonMs,
                            long outageStartMs, long outageEndMs, long timeoutMs) {
        FakeClock clock = new FakeClock(0);
        CircuitBreaker breaker = new CircuitBreaker(config, clock);
        Bulkhead bulkhead = new Bulkhead(bulkheadSize);
        GuardedCall guarded = new GuardedCall(breaker, bulkhead);
        List<Long> pending = new ArrayList<>();   // due times of hung calls, kept sorted by insertion (dues are monotone)

        int seconds = (int) (horizonMs / 1000);
        int[] ok = new int[seconds], fail = new int[seconds], rejOpen = new int[seconds], rejBulk = new int[seconds];
        State[] stateAtEnd = new State[seconds];

        for (long t = 0; t < horizonMs; t += callIntervalMs) {
            clock.advance(t - clock.nowMs());
            int sec = (int) (t / 1000);
            while (!pending.isEmpty() && pending.get(0) <= t) {   // hung calls time out: each is a failure
                pending.remove(0);
                guarded.complete(false);
                fail[(int) (t / 1000)]++;
            }
            Admission a = guarded.begin();
            switch (a) {
                case REJECTED_OPEN -> rejOpen[sec]++;
                case REJECTED_BULKHEAD -> rejBulk[sec]++;
                case ADMITTED -> {
                    boolean healthy = t < outageStartMs || t >= outageEndMs;
                    if (healthy) { guarded.complete(true); ok[sec]++; }
                    else pending.add(t + timeoutMs);
                }
            }
            stateAtEnd[sec] = breaker.state();
        }

        List<TimelineRow> rows = new ArrayList<>();
        int tS = 0, tF = 0, tO = 0, tB = 0;
        for (int s = 0; s < seconds; s++) {
            rows.add(new TimelineRow(s, ok[s], fail[s], rejOpen[s], rejBulk[s], stateAtEnd[s]));
            tS += ok[s]; tF += fail[s]; tO += rejOpen[s]; tB += rejBulk[s];
        }
        return new OutageReport(rows, breaker.transitions(), tS, tF, tO, tB);
    }
}

public final class Main {
    static final BreakerConfig DEMO = new BreakerConfig(50, 10, 5_000, 1_000, 3_000, 3);

    public static void main(String[] args) {
        System.out.println("== Sliding window: 5 s of 1 s buckets, failures age out ==");
        SlidingWindow w = new SlidingWindow(5_000, 1_000);
        for (int i = 0; i < 8; i++) w.record(0, true);
        for (int i = 0; i < 12; i++) w.record(1_500, false);
        System.out.printf("t=1500: total=%d failures=%d rate=%d%%%n", w.total(1_500), w.failures(1_500), w.failureRatePercent(1_500));
        System.out.printf("t=5000: total=%d failures=%d rate=%d%%  (the 8 successes from t=0 have aged out)%n", w.total(5_000), w.failures(5_000), w.failureRatePercent(5_000));
        System.out.printf("t=6500: total=%d failures=%d rate=%d%%  (now the failures are gone too)%n", w.total(6_500), w.failures(6_500), w.failureRatePercent(6_500));

        System.out.println("\n== Breaker by hand: 50% of at least 10 calls, open 3 s, 3 probes ==");
        FakeClock clock = new FakeClock(0);
        CircuitBreaker cb = new CircuitBreaker(DEMO, clock);
        for (int i = 0; i < 5; i++) { cb.tryAcquire(); cb.onSuccess(); }
        for (int i = 0; i < 4; i++) { cb.tryAcquire(); cb.onFailure(); }
        System.out.printf("5 ok + 4 fail: rate=%d%% state=%s (9 calls, below minimumCalls=10)%n", cb.failureRatePercent(), cb.state());
        cb.tryAcquire(); cb.onFailure();
        System.out.printf("5 ok + 5 fail: rate=%d%% state=%s%n", cb.failureRatePercent(), cb.state());
        System.out.printf("t=%d tryAcquire=%s (fail fast, no call made)%n", clock.nowMs(), cb.tryAcquire());
        clock.advance(2_999);
        System.out.printf("t=%d tryAcquire=%s state=%s%n", clock.nowMs(), cb.tryAcquire(), cb.state());
        clock.advance(1);
        System.out.printf("t=%d tryAcquire=%s state=%s (first caller after the open duration performs the transition)%n", clock.nowMs(), cb.tryAcquire(), cb.state());
        cb.onFailure();
        System.out.printf("probe failed: state=%s, open timer restarted at t=%d%n", cb.state(), clock.nowMs());
        clock.advance(3_000);
        for (int i = 0; i < 3; i++) { cb.tryAcquire(); cb.onSuccess(); }
        System.out.printf("t=%d three probes succeeded: state=%s%n", clock.nowMs(), cb.state());
        System.out.println("transitions: " + cb.transitions());

        System.out.println("\n== Bulkhead: 3 permits ==");
        Bulkhead bh = new Bulkhead(3);
        System.out.printf("acquire x4: %s %s %s %s  inFlight=%d rejected=%d%n", bh.tryAcquire(), bh.tryAcquire(), bh.tryAcquire(), bh.tryAcquire(), bh.inFlight(), bh.rejected());
        bh.release();
        System.out.printf("after one release: acquire=%s inFlight=%d%n", bh.tryAcquire(), bh.inFlight());

        System.out.println("\n== Card processor outage: 20 auth/s, bulkhead 10, 2 s timeout, processor dead from t=5 s to t=15 s ==");
        OutageReport report = OutageSimulation.run(DEMO, 10, 50, 25_000, 5_000, 15_000, 2_000);
        System.out.print(report.table());
        System.out.println("transitions:");
        for (Transition tr : report.transitions()) System.out.printf("  t=%6d ms  %s -> %s%n", tr.atMs(), tr.from(), tr.to());
        System.out.printf("totals: ok=%d fail=%d rejected_open=%d rejected_bulkhead=%d%n",
                report.totalSuccess(), report.totalFailure(), report.totalRejectedOpen(), report.totalRejectedBulkhead());
        System.out.printf("calls that waited 2 s for a dead processor: %d of %d calls during the outage%n",
                report.totalFailure(), report.countIn(5_000, 15_000, CallOutcome.SUCCESS) + report.totalFailure()
                        + report.countIn(5_000, 15_000, CallOutcome.REJECTED_OPEN) + report.countIn(5_000, 15_000, CallOutcome.REJECTED_BULKHEAD));
    }
}
