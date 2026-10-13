// Tests for phases/09-reliability-and-scale/03-caching-strategies. Run: javac -d .out *.java && java -cp .out MainTest
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

    static Money usd(long minor) { return Money.ofMinor(minor, Currency.USD); }

    public static void main(String[] args) {
        // TtlCache: expiry is lazy and driven by the injected clock
        ManualClock clock = new ManualClock(1_000);
        TtlCache ttl = new TtlCache(clock);
        ttl.put("a", usd(100), 500);
        check("ttl: fresh entry is returned", usd(100).equals(ttl.get("a")));
        clock.advance(499);
        check("ttl: still present one ms before expiry", ttl.contains("a"));
        clock.advance(1);
        check("ttl: expired exactly at expiresAt", ttl.get("a") == null);
        check("ttl: expired entry removed on read", ttl.size() == 0);
        ttl.put("b", usd(1), TtlCache.NO_EXPIRY);
        clock.advance(1_000_000_000L);
        check("ttl: NO_EXPIRY never expires", ttl.contains("b"));
        check("ttl: invalidate reports presence", ttl.invalidate("b") && !ttl.invalidate("b"));
        checkThrows("clock refuses to move backwards", IllegalArgumentException.class, () -> clock.advance(-1));

        // Cache-aside without invalidation: the stale balance hazard
        ManualClock c1 = new ManualClock(0);
        BackingStore s1 = new BackingStore(20, 25);
        s1.write("acct", usd(10_000));
        CacheAside ttlOnly = new CacheAside(s1, c1, 30_000, false, 1);
        check("cache-aside: first read misses and loads", usd(10_000).equals(ttlOnly.read("acct")) && ttlOnly.stats().misses == 1);
        check("cache-aside: second read hits", usd(10_000).equals(ttlOnly.read("acct")) && ttlOnly.stats().hits == 1);
        s1.write("acct", usd(2_000));                                   // out-of-band debit straight to the store
        check("cache-aside TTL-only: read after external write is stale", usd(10_000).equals(ttlOnly.readMeasured("acct")));
        check("cache-aside TTL-only: staleness is counted", ttlOnly.stats().staleReads == 1);
        c1.advance(30_000);
        check("cache-aside TTL-only: correct again after TTL", usd(2_000).equals(ttlOnly.readMeasured("acct")) && ttlOnly.stats().staleReads == 1);

        // Cache-aside with invalidate-on-write: a write through the strategy is never stale
        ManualClock c2 = new ManualClock(0);
        BackingStore s2 = new BackingStore(20, 25);
        s2.write("acct", usd(10_000));
        CacheAside inval = new CacheAside(s2, c2, 30_000, true, 1);
        inval.read("acct");
        inval.write("acct", usd(2_000));
        check("cache-aside+invalidate: write removes the cached entry", !inval.cache().contains("acct"));
        check("cache-aside+invalidate: read after write is fresh", usd(2_000).equals(inval.readMeasured("acct")) && inval.stats().staleReads == 0);
        check("cache-aside+invalidate: that read was a miss", inval.stats().misses == 2);

        // Read-through: same read semantics, write deletes
        ManualClock c3 = new ManualClock(0);
        BackingStore s3 = new BackingStore(20, 25);
        s3.write("acct", usd(500));
        ReadThrough rt = new ReadThrough(s3, c3, 1_000, 1);
        check("read-through: loads on miss", usd(500).equals(rt.read("acct")) && s3.reads == 1);
        rt.read("acct");
        check("read-through: hit does not touch the store", s3.reads == 1 && rt.stats().hits == 1);
        rt.write("acct", usd(400));
        check("read-through: write invalidates", !rt.cache().contains("acct") && usd(400).equals(rt.read("acct")));
        check("read-through: missing account is null and not cached", rt.read("nope") == null && !rt.cache().contains("nope"));

        // Write-through: writes populate the cache synchronously
        ManualClock c4 = new ManualClock(0);
        BackingStore s4 = new BackingStore(20, 25);
        WriteThrough wt = new WriteThrough(s4, c4, 1_000, 1);
        wt.write("acct", usd(777));
        check("write-through: store and cache both updated", usd(777).equals(s4.peek("acct")) && wt.cache().contains("acct"));
        check("write-through: read after write hits", usd(777).equals(wt.read("acct")) && wt.stats().hits == 1 && wt.stats().misses == 0);
        check("write-through: every write costs a store write", s4.writes == 1);

        // Write-behind: coalescing, flush order, and loss on crash
        ManualClock c5 = new ManualClock(0);
        BackingStore s5 = new BackingStore(20, 25);
        s5.write("a", usd(100)); s5.write("b", usd(200));
        s5.writes = 0;
        WriteBehind wb = new WriteBehind(s5, c5, 1);
        wb.write("a", usd(90));
        wb.write("b", usd(190));
        wb.write("a", usd(80));
        check("write-behind: cache sees the latest value", usd(80).equals(wb.read("a")));
        check("write-behind: store not yet written", s5.writes == 0 && usd(100).equals(s5.peek("a")));
        check("write-behind: dirty count coalesces per key", wb.dirtyCount() == 2);
        check("write-behind: read of dirty key is stale vs store", usd(80).equals(wb.readMeasured("a")) && wb.stats().staleReads == 1);
        int flushed = wb.flush();
        check("write-behind: flush writes each dirty key once", flushed == 2 && s5.writes == 2);
        check("write-behind: flush writes the last value", usd(80).equals(s5.peek("a")) && usd(190).equals(s5.peek("b")));
        check("write-behind: nothing dirty after flush", wb.dirtyCount() == 0);
        wb.write("a", usd(70));
        wb.crash();
        check("write-behind: crash loses the dirty write", wb.lostWrites == 1 && usd(80).equals(s5.peek("a")));
        check("write-behind: cache is empty after crash, read reloads old value", usd(80).equals(wb.read("a")));

        // Workload determinism and the measured comparison
        List<Workload.Op> ops1 = Workload.generate(42, 2_000, 100, 0.15, 0.2, 0.8);
        List<Workload.Op> ops2 = Workload.generate(42, 2_000, 100, 0.15, 0.2, 0.8);
        check("workload: same seed, same ops", ops1.equals(ops2));
        long hotReads = ops1.stream().filter(o -> Integer.parseInt(o.account().substring(5)) < 20).count();
        check("workload: hot 20% of accounts receive most ops", hotReads > ops1.size() * 0.7);

        ManualClock w1 = new ManualClock(0);
        CacheAside mTtl = new CacheAside(Workload.seededStore(100, 20, 25), w1, 5_000, false, 1);
        Workload.run(mTtl, ops1, w1, 10);
        ManualClock w2 = new ManualClock(0);
        CacheAside mInv = new CacheAside(Workload.seededStore(100, 20, 25), w2, 5_000, true, 1);
        Workload.run(mInv, ops1, w2, 10);
        ManualClock w3 = new ManualClock(0);
        WriteThrough mWt = new WriteThrough(Workload.seededStore(100, 20, 25), w3, 5_000, 1);
        Workload.run(mWt, ops1, w3, 10);
        check("measured: TTL-only serves stale balances", mTtl.stats().staleReads > 0);
        check("measured: invalidate-on-write serves none", mInv.stats().staleReads == 0);
        check("measured: write-through serves none", mWt.stats().staleReads == 0);
        check("measured: TTL-only has the highest hit rate (it never evicts on write)", mTtl.stats().hitRate() >= mInv.stats().hitRate());
        check("measured: caching beats no cache on store reads", mInv.store().reads < ops1.size());
        check("measured: write-through hit rate >= invalidating cache-aside", mWt.stats().hitRate() >= mInv.stats().hitRate());

        // Money guard rails still hold inside the cache
        checkThrows("money: mixing currencies is an error", CurrencyMismatchException.class,
                () -> usd(1).plus(Money.ofMinor(1, new Currency("EUR", 2))));
        check("money: toString", usd(-7).toString().equals("-0.07 USD"));

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(f -> System.out.println("FAIL: " + f));
        if (!failures.isEmpty()) System.exit(1);
    }
}
