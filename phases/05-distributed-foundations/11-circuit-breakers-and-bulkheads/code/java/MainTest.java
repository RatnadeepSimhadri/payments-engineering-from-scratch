// Tests for phases/05-distributed-foundations/11-circuit-breakers-and-bulkheads. Run: javac -d .out *.java && java -cp .out MainTest
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

    /** 50% of at least 10 calls in a 5 s window of 1 s buckets; open for 3 s; 3 half-open probes. */
    static final BreakerConfig CONFIG = new BreakerConfig(50, 10, 5_000, 1_000, 3_000, 3);

    static void drive(CircuitBreaker cb, int successes, int failures) {
        for (int i = 0; i < successes; i++) { cb.tryAcquire(); cb.onSuccess(); }
        for (int i = 0; i < failures; i++) { cb.tryAcquire(); cb.onFailure(); }
    }

    public static void main(String[] args) {
        // sliding window: counting, ageing out, bucket reuse
        SlidingWindow w = new SlidingWindow(5_000, 1_000);
        for (int i = 0; i < 8; i++) w.record(0, true);
        for (int i = 0; i < 12; i++) w.record(1_500, false);
        check("window counts 20 calls and 12 failures at t=1500", w.total(1_500) == 20 && w.failures(1_500) == 12);
        check("window rate is integer percent (60)", w.failureRatePercent(1_500) == 60);
        check("window: bucket from t=0 ages out at t=5000", w.total(5_000) == 12 && w.failureRatePercent(5_000) == 100);
        check("window: everything aged out at t=6500 gives rate 0, not a division by zero", w.total(6_500) == 0 && w.failureRatePercent(6_500) == 0);
        w.record(5_000, true);   // slot 0 is reused for the period starting at 5000
        check("window: reused bucket starts from zero", w.total(5_000) == 13 && w.failures(5_000) == 12);
        w.reset();
        check("window: reset empties every bucket", w.total(5_000) == 0);
        checkThrows("window rejects a window that is not a multiple of the bucket", IllegalArgumentException.class, () -> new SlidingWindow(5_000, 1_500));

        // CLOSED -> OPEN requires both minimumCalls and the failure rate
        FakeClock clock = new FakeClock(0);
        CircuitBreaker cb = new CircuitBreaker(CONFIG, clock);
        drive(cb, 5, 4);
        check("stays CLOSED below minimumCalls even at 44% failures", cb.state() == State.CLOSED && cb.failureRatePercent() == 44);
        drive(cb, 0, 1);
        check("opens at exactly the threshold (50% of 10)", cb.state() == State.OPEN);
        check("transition CLOSED->OPEN recorded with the clock's time", cb.transitions().equals(List.of(new Transition(State.CLOSED, State.OPEN, 0))));

        // OPEN rejects without touching the dependency, until the open duration elapses
        check("OPEN rejects immediately", !cb.tryAcquire() && cb.rejected() == 1);
        clock.advance(2_999);
        check("OPEN still rejects at 2999 ms", !cb.tryAcquire() && cb.state() == State.OPEN);
        clock.advance(1);
        check("first caller at 3000 ms is admitted and performs OPEN->HALF_OPEN", cb.tryAcquire() && cb.state() == State.HALF_OPEN);
        check("HALF_OPEN admits exactly halfOpenPermits probes", cb.tryAcquire() && cb.tryAcquire() && !cb.tryAcquire());
        check("the fourth probe counts as a rejection", cb.rejected() == 3);

        // HALF_OPEN -> OPEN on a single probe failure, and the open timer restarts
        cb.onFailure();
        check("one failed probe re-opens", cb.state() == State.OPEN);
        clock.advance(2_999);
        check("re-opened timer runs from the failure, not the original trip", !cb.tryAcquire());
        clock.advance(1);
        check("half-open again after a full open duration", cb.tryAcquire() && cb.state() == State.HALF_OPEN);

        // HALF_OPEN -> CLOSED only after every probe succeeds, and the window is reset
        cb.onSuccess();
        check("one success out of three probes is not enough", cb.state() == State.HALF_OPEN);
        cb.tryAcquire(); cb.onSuccess();
        cb.tryAcquire(); cb.onSuccess();
        check("three successes close the breaker", cb.state() == State.CLOSED);
        check("closing resets the window so old failures cannot re-trip it", cb.failureRatePercent() == 0);
        check("full transition log", cb.transitions().size() == 5
                && cb.transitions().get(4).equals(new Transition(State.HALF_OPEN, State.CLOSED, 6_000)));

        // a late result from a call that started before the trip says nothing about the dependency now
        FakeClock clock2 = new FakeClock(0);
        CircuitBreaker cb2 = new CircuitBreaker(CONFIG, clock2);
        drive(cb2, 0, 10);
        cb2.onSuccess();
        check("late success while OPEN is ignored", cb2.state() == State.OPEN && cb2.transitions().size() == 1);

        // bulkhead: a bounded, non-blocking semaphore
        Bulkhead bh = new Bulkhead(3);
        check("bulkhead admits up to its limit", bh.tryAcquire() && bh.tryAcquire() && bh.tryAcquire() && bh.inFlight() == 3);
        check("bulkhead rejects when saturated and counts it", !bh.tryAcquire() && bh.rejected() == 1 && bh.available() == 0);
        bh.release();
        check("release returns one permit", bh.available() == 1 && bh.tryAcquire());
        checkThrows("release without acquire is a bug, not a no-op", IllegalStateException.class, () -> { Bulkhead b = new Bulkhead(1); b.release(); });
        checkThrows("bulkhead needs at least one permit", IllegalArgumentException.class, () -> new Bulkhead(0));

        // the composition: bulkhead first, and a bulkhead rejection is not a dependency failure
        FakeClock clock3 = new FakeClock(0);
        CircuitBreaker cb3 = new CircuitBreaker(CONFIG, clock3);
        Bulkhead bh3 = new Bulkhead(2);
        GuardedCall g = new GuardedCall(cb3, bh3);
        check("two calls in flight are admitted", g.begin() == Admission.ADMITTED && g.begin() == Admission.ADMITTED);
        check("third is rejected by the bulkhead", g.begin() == Admission.REJECTED_BULKHEAD);
        check("bulkhead rejections do not enter the breaker's window", cb3.failureRatePercent() == 0 && cb3.transitions().isEmpty());
        g.complete(false); g.complete(false);
        check("completions release permits and feed the breaker", bh3.inFlight() == 0 && cb3.failureRatePercent() == 100);
        for (int i = 0; i < 8; i++) g.call(() -> false);
        check("call() with a failing operation trips the breaker", cb3.state() == State.OPEN);
        check("REJECTED_OPEN returns the bulkhead permit it took", g.begin() == Admission.REJECTED_OPEN && bh3.inFlight() == 0);
        check("call() while OPEN reports REJECTED_OPEN", g.call(() -> true) == CallOutcome.REJECTED_OPEN);
        FakeClock clock4 = new FakeClock(0);
        GuardedCall g4 = new GuardedCall(new CircuitBreaker(CONFIG, clock4), new Bulkhead(1));
        check("an exception from the operation is a failure", g4.call(() -> { throw new RuntimeException("boom"); }) == CallOutcome.FAILURE);
        check("a normal operation is a success", g4.call(() -> true) == CallOutcome.SUCCESS);

        // config validation
        checkThrows("threshold must be 1..100", IllegalArgumentException.class, () -> new BreakerConfig(0, 10, 5_000, 1_000, 3_000, 3));
        checkThrows("window must be a multiple of bucket", IllegalArgumentException.class, () -> new BreakerConfig(50, 10, 5_000, 1_500, 3_000, 3));
        checkThrows("at least one half-open permit", IllegalArgumentException.class, () -> new BreakerConfig(50, 10, 5_000, 1_000, 3_000, 0));

        // the outage simulation is a pure function of its parameters, identical in all three languages
        OutageReport report = OutageSimulation.run(CONFIG, 10, 50, 25_000, 5_000, 15_000, 2_000);
        check("simulation totals", report.totalSuccess() == 260 && report.totalFailure() == 23
                && report.totalRejectedOpen() == 157 && report.totalRejectedBulkhead() == 60);
        check("simulation trips at t=9000", report.transitions().get(0).equals(new Transition(State.CLOSED, State.OPEN, 9_000)));
        check("simulation recovers: five transitions ending CLOSED", report.transitions().size() == 5
                && report.transitions().get(4).to() == State.CLOSED && report.rows().get(24).stateAtEnd() == State.CLOSED);
        check("no call waits on a dead processor once the breaker is open", report.countIn(9_000, 14_000, CallOutcome.FAILURE) == 10
                && report.countIn(15_000, 17_000, CallOutcome.FAILURE) == 0);
        BreakerConfig neverTrips = new BreakerConfig(50, 1_000_000, 5_000, 1_000, 3_000, 3);
        OutageReport noBreaker = OutageSimulation.run(neverTrips, 10, 50, 25_000, 5_000, 15_000, 2_000);
        check("without a breaker every outage second burns bulkhead permits on timeouts", noBreaker.totalFailure() == 50
                && noBreaker.totalRejectedOpen() == 0 && noBreaker.totalFailure() > report.totalFailure());

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }
}
