// Tests for phases/09-reliability-and-scale/05-distributed-rate-limiting. Run: javac -d .out *.java && java -cp .out MainTest
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

    public static void main(String[] args) {
        // --- token bucket arithmetic ---
        FakeClock clock = new FakeClock(0);
        TokenBucket bucket = new TokenBucket(10, 5, clock.nowMillis());
        int burst = 0;
        while (bucket.tryAcquire(1, clock.nowMillis())) burst++;
        check("new bucket admits exactly capacity as a burst", burst == 10);
        check("empty bucket denies", !bucket.tryAcquire(1, clock.nowMillis()));
        clock.advance(100);
        check("5/s * 0.1 s = 0 whole tokens but 500 milli-tokens accrue", bucket.availableTokens(clock.nowMillis()) == 0);
        clock.advance(100);
        check("fractional refill accumulates to one whole token after 200 ms", bucket.availableTokens(clock.nowMillis()) == 1);
        clock.advance(60_000);
        check("refill clamps at capacity", bucket.availableTokens(clock.nowMillis()) == 10);
        check("multi-token acquire", bucket.tryAcquire(10, clock.nowMillis()) && !bucket.tryAcquire(1, clock.nowMillis()));
        checkThrows("zero capacity rejected", IllegalArgumentException.class, () -> new TokenBucket(0, 1, 0));
        checkThrows("clock cannot go backwards", IllegalArgumentException.class, () -> new FakeClock(0).advance(-1));

        // --- centralized: exact across nodes ---
        SharedStore store = new SharedStore(3);
        CentralizedLimiter central = new CentralizedLimiter(store, 100, 100);
        int admitted = 0;
        for (int i = 0; i < 400; i++) if (central.tryAcquire(i % 4, "k", 0).allowed()) admitted++;
        check("centralized admits exactly capacity across four nodes at t=0", admitted == 100);
        check("centralized charges one round trip per decision", central.tryAcquire(0, "k", 0).latencyMillis() == 3);
        check("centralized store calls == decisions", store.calls() == 401);
        admitted = 0;
        for (int i = 0; i < 400; i++) if (central.tryAcquire(i % 4, "k", 500).allowed()) admitted++;
        check("after 500 ms exactly 50 more tokens exist", admitted == 50);
        check("keys are independent", central.tryAcquire(0, "other", 500).allowed());

        // --- sliding window log: exact over any window, no burst allowance ---
        SharedStore logStore = new SharedStore(1);
        SlidingWindowLogLimiter log = new SlidingWindowLogLimiter(logStore, 5, 1000);
        admitted = 0;
        for (int t = 0; t < 10; t++) if (log.tryAcquire(t % 2, "k", t * 100L).allowed()) admitted++;
        check("sliding log admits exactly limit within the window", admitted == 5);
        check("sliding log memory == limit entries", log.logSize("k") == 5);
        check("sliding log admits again once the oldest entry ages out", log.tryAcquire(0, "k", 1000).allowed());
        check("sliding log then denies: window [1, 1000] still holds 5", !log.tryAcquire(0, "k", 1000).allowed());

        // --- local buckets with periodic sync: cheap, approximate ---
        SharedStore syncStore = new SharedStore(2);
        LocalSyncLimiter local = new LocalSyncLimiter(syncStore, 4, 100, 100, 1000);
        check("local decision costs no round trip", local.tryAcquire(0, "k", 0).latencyMillis() == 0);
        admitted = 0;
        for (int i = 0; i < 200; i++) if (local.tryAcquire(0, "k", 0).allowed()) admitted++;
        check("hot node is capped at its 1/N share (25 tokens, one already spent) before the first sync", admitted == 24);
        check("cold nodes still hold their unused share", local.tryAcquire(1, "k", 0).allowed());
        for (int i = 0; i < 50; i++) local.tryAcquire(0, "k", 999);
        check("no sync before the interval elapses", local.syncs() == 0);
        local.tryAcquire(0, "k", 1000);
        check("sync fires once the interval elapses", local.syncs() == 1);
        check("after sync the hot node's share grew", local.rateOf("k", 0) > 25);
        check("after sync the cold nodes keep the floor share", local.rateOf("k", 2) == 6);
        long total = 0;
        for (int n = 0; n < 4; n++) total += local.rateOf("k", n);
        check("shares never exceed the global rate", total <= 100);

        // --- seeded load: the measured trade-off the lesson is about ---
        List<LoadReport> even = Main.compare(400, 0.25, 7);
        List<LoadReport> skew = Main.compare(400, 0.85, 7);
        LoadReport centralEven = even.get(0), localEven = even.get(1), logEven = even.get(3);
        LoadReport centralSkew = skew.get(0), localSlow = skew.get(1), localFast = skew.get(2), logSkew = skew.get(3);
        check("centralized never admits more than rate + burst in any second", centralEven.peakPerSecond() <= Main.RATE + Main.CAPACITY);
        check("sliding log never admits more than the limit in any second", logEven.peakPerSecond() <= Main.RATE && logSkew.peakPerSecond() <= Main.RATE);
        check("centralized is within one request of ideal", Math.abs(centralEven.versusIdeal(Main.RATE, Main.CAPACITY)) <= 1);
        check("local+sync under-admits under skew", localSlow.admitted < centralSkew.admitted);
        check("faster sync narrows the gap", localFast.admitted > localSlow.admitted);
        check("faster sync costs more coordination", localFast.storeCalls > localSlow.storeCalls);
        check("local+sync is cheaper per request than centralized", localEven.meanLatencyMillis() < centralEven.meanLatencyMillis());
        check("all strategies see the same offered load for the same seed", centralEven.offered == localEven.offered && localEven.offered == logEven.offered);
        List<LoadReport> again = Main.compare(400, 0.85, 7);
        check("simulation is deterministic for a seed", again.get(1).admitted == localSlow.admitted && again.get(0).rejected == centralSkew.rejected);
        List<LoadReport> light = Main.compare(60, 0.25, 7);
        check("light load is never rejected by any strategy", light.stream().allMatch(r -> r.rejected == 0));

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }
}
