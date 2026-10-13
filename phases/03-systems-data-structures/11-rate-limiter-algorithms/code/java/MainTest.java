// Tests for phases/03-systems-data-structures/11-rate-limiter-algorithms. Run: javac -d .out *.java && java -cp .out MainTest
import java.util.ArrayList;
import java.util.List;
import java.util.Map;

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

    public static void main(String[] args) {
        // Decision and the Retry-After header
        check("Retry-After rounds 1 ms up to 1 s", Decision.deny(1).retryAfterHeader() == 1);
        check("Retry-After 1000 ms is 1 s", Decision.deny(1000).retryAfterHeader() == 1);
        check("Retry-After 1001 ms is 2 s", Decision.deny(1001).retryAfterHeader() == 2);
        check("allow has no Retry-After", Decision.allow().retryAfterHeader() == 0);
        check("deny never reports zero wait", Decision.deny(0).retryAfterMillis() == 1 && !Decision.deny(5).allowed());
        check("allow with delay", Decision.allow(250).allowed() && Decision.allow(250).delayMillis() == 250);

        // token bucket
        TokenBucket tb = new TokenBucket(5, 2, new ManualClock());
        boolean burstOk = true;
        for (int i = 0; i < 7; i++) if (tb.tryAcquire().allowed() != (i < 5)) burstOk = false;
        check("token bucket starts full and admits a burst up to capacity", burstOk);
        ManualClock c1 = new ManualClock();
        TokenBucket tb2 = new TokenBucket(5, 2, c1);
        for (int i = 0; i < 5; i++) tb2.tryAcquire();
        c1.advance(499);
        check("token bucket: 0.998 tokens is a deny", !tb2.tryAcquire().allowed());
        c1.advance(1);
        check("token bucket: exactly one token at 500 ms", tb2.tryAcquire().allowed());
        c1.advance(60_000);
        check("token bucket never exceeds capacity", tb2.tokens() == 5.0);
        ManualClock c2 = new ManualClock();
        TokenBucket tb3 = new TokenBucket(1, 4, c2);
        tb3.tryAcquire();
        check("token bucket Retry-After is time to next token", tb3.tryAcquire().retryAfterMillis() == 250);
        c2.advance(250);
        check("token bucket admits after Retry-After", tb3.tryAcquire().allowed());
        checkThrows("token bucket rejects zero capacity", IllegalArgumentException.class, () -> new TokenBucket(0, 1, new ManualClock()));
        checkThrows("token bucket rejects zero rate", IllegalArgumentException.class, () -> new TokenBucket(1, 0, new ManualClock()));

        // leaky bucket
        LeakyBucket lb = new LeakyBucket(3, 10, new ManualClock());
        check("leaky bucket spaces output at the leak rate",
                lb.tryAcquire().delayMillis() == 0 && lb.tryAcquire().delayMillis() == 100 && lb.tryAcquire().delayMillis() == 200);
        Decision full = lb.tryAcquire();
        check("leaky bucket rejects when the queue is full", !full.allowed() && full.retryAfterMillis() == 100);
        check("leaky bucket reports queue depth", lb.queued() == 3);
        ManualClock c3 = new ManualClock();
        LeakyBucket lb2 = new LeakyBucket(3, 10, c3);
        for (int i = 0; i < 3; i++) lb2.tryAcquire();
        c3.advance(100);
        Decision drained = lb2.tryAcquire();
        check("leaky bucket drains as time passes", drained.allowed() && drained.delayMillis() == 200);
        c3.advance(10_000);
        check("leaky bucket empties after a pause", lb2.queued() == 0 && lb2.tryAcquire().delayMillis() == 0);
        checkThrows("leaky bucket rejects a rate that does not divide a second", IllegalArgumentException.class, () -> new LeakyBucket(3, 7, new ManualClock()));
        checkThrows("leaky bucket rejects zero queue", IllegalArgumentException.class, () -> new LeakyBucket(0, 10, new ManualClock()));

        // fixed window
        ManualClock c4 = new ManualClock();
        FixedWindow fw = new FixedWindow(2, 1000, c4);
        check("fixed window admits up to limit", fw.tryAcquire().allowed() && fw.tryAcquire().allowed());
        Decision fwDeny = fw.tryAcquire();
        check("fixed window denies with Retry-After to the boundary", !fwDeny.allowed() && fwDeny.retryAfterMillis() == 1000);
        c4.set(999);
        check("fixed window Retry-After shrinks toward the boundary", fw.tryAcquire().retryAfterMillis() == 1);
        c4.set(1000);
        check("fixed window resets at the boundary", fw.tryAcquire().allowed());
        ManualClock c5 = new ManualClock();
        FixedWindow fw2 = new FixedWindow(10, 1000, c5);
        SlidingWindowLog log2 = new SlidingWindowLog(10, 1000, c5);
        int admittedFw = 0, admittedLog = 0;
        for (long t = 950; t < 1050; t += 5) {
            c5.set(t);
            if (fw2.tryAcquire().allowed()) admittedFw++;
            if (log2.tryAcquire().allowed()) admittedLog++;
        }
        check("fixed window admits 2x across a boundary (the failure)", admittedFw == 20);
        check("sliding log admits exactly the limit across a boundary", admittedLog == 10);

        // sliding window log
        ManualClock c6 = new ManualClock();
        SlidingWindowLog log = new SlidingWindowLog(2, 1000, c6);
        c6.set(100);
        log.tryAcquire();
        c6.set(400);
        log.tryAcquire();
        c6.set(500);
        Decision logDeny = log.tryAcquire();
        check("sliding log Retry-After is when the oldest entry leaves", !logDeny.allowed() && logDeny.retryAfterMillis() == 600);
        c6.set(1100);
        check("sliding log admits once the oldest entry has aged out", log.tryAcquire().allowed() && log.size() == 2);
        ManualClock c7 = new ManualClock();
        SlidingWindowLog log3 = new SlidingWindowLog(1, 1000, c7);
        log3.tryAcquire();
        c7.set(999);
        boolean at999 = log3.tryAcquire().allowed();
        c7.set(1000);
        check("sliding log: entry at exactly window age has left", !at999 && log3.tryAcquire().allowed());

        // sliding window counter
        ManualClock c8 = new ManualClock();
        SlidingWindowCounter sc = new SlidingWindowCounter(10, 1000, c8);
        for (long t = 0; t < 1000; t += 100) {
            c8.set(t);
            sc.tryAcquire();
        }
        c8.set(1250);
        check("sliding counter weights previous window at 0.75", sc.weightedCountScaled() == 7500);
        check("sliding counter admits at 7.5", sc.tryAcquire().allowed());
        c8.set(1500);
        check("sliding counter weighted 10*0.5 + 1 = 6", sc.weightedCountScaled() == 6000 && sc.tryAcquire().allowed());
        ManualClock c9 = new ManualClock();
        SlidingWindowCounter sc2 = new SlidingWindowCounter(10, 1000, c9);
        for (long t = 0; t < 1000; t += 100) {
            c9.set(t);
            sc2.tryAcquire();
        }
        c9.set(1000);
        Decision scDeny = sc2.tryAcquire();
        check("sliding counter denies at the boundary after a full window, Retry-After 100", !scDeny.allowed() && scDeny.retryAfterMillis() == 100);
        ManualClock c10 = new ManualClock();
        SlidingWindowCounter sc3 = new SlidingWindowCounter(2, 1000, c10);
        sc3.tryAcquire();
        sc3.tryAcquire();
        c10.set(5000);
        check("sliding counter: a gap clears the previous window", sc3.weightedCountScaled() == 0 && sc3.tryAcquire().allowed());

        // the shared trace
        List<Long> trace = Trace.retryStormTrace();
        int inBurst = 0;
        for (long t : trace) if (t >= 3000 && t < 3020) inBurst++;
        check("trace has 99 requests with 40 in the burst", trace.size() == 99 && inBurst == 40);
        check("maxInWindow", Trace.maxInWindow(List.of(0L, 100L, 999L, 1000L), 1000) == 3 && Trace.maxInWindow(List.of(), 1000) == 0);
        Map<String, int[]> expected = Map.of(
                "token bucket", new int[] {69, 30, 10, 19, 95, 0},
                "leaky bucket", new int[] {69, 30, 11, 10, 195, 995},
                "fixed window", new int[] {60, 39, 10, 19, 995, 0},
                "sliding log", new int[] {60, 39, 1, 10, 100, 0},
                "sliding counter", new int[] {55, 44, 1, 10, 112, 0});
        for (Trace.Limiter l : Trace.makeLimiters()) {
            Trace.ReplaySummary s = Trace.replay(l.limiter(), l.clock(), trace, 3000, 3020);
            int[] e = expected.get(s.name());
            check("retry storm summary matches Python and Go: " + s.name(),
                    s.allowed() == e[0] && s.denied() == e[1] && s.allowedInBurst() == e[2]
                            && s.maxInAnySecond() == e[3] && s.maxRetryAfterMillis() == e[4] && s.maxDelayMillis() == e[5]);
            boolean windowed = !s.name().equals("token bucket") && !s.name().equals("fixed window");
            check("one-second ceiling held only by windowed limiters: " + s.name(), windowed ? s.maxInAnySecond() == 10 : s.maxInAnySecond() > 10);
        }

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }
}
