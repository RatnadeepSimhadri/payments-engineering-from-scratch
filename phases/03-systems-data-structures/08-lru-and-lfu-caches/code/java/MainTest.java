// Tests for phases/03-systems-data-structures/08-lru-and-lfu-caches. Run: javac -d .out *.java && java -cp .out MainTest
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
        // LRU ordering and eviction
        LruCache<String, String> c = new LruCache<>(3);
        c.put("a", "1");
        c.put("b", "2");
        c.put("c", "3");
        check("LRU: puts order most recent first", c.keys().equals(List.of("c", "b", "a")));
        check("LRU: get returns value", "1".equals(c.get("a")));
        check("LRU: get moves to front", c.keys().equals(List.of("a", "c", "b")));
        c.put("d", "4");
        check("LRU: put evicts least recent", c.keys().equals(List.of("d", "a", "c")));
        check("LRU: evicted key misses", c.get("b") == null);
        check("LRU: evictions counted", c.evictions == 1);

        LruCache<String, String> u = new LruCache<>(2);
        u.put("a", "1");
        u.put("b", "2");
        u.put("a", "one");
        check("LRU: update keeps size", u.size() == 2);
        check("LRU: update changes value", "one".equals(u.get("a")));
        check("LRU: update does not evict", u.evictions == 0 && u.keys().equals(List.of("a", "b")));

        LruCache<Integer, Integer> big = new LruCache<>(10);
        boolean bounded = true;
        for (int i = 0; i < 1_000; i++) {
            big.put(i, i);
            if (big.size() > 10) bounded = false;
        }
        check("LRU: capacity never exceeded", bounded);
        check("LRU: keeps the last ten", big.keys().equals(List.of(999, 998, 997, 996, 995, 994, 993, 992, 991, 990)));
        check("LRU: 990 evictions", big.evictions == 990);

        LruCache<String, String> r = new LruCache<>(2);
        r.put("a", "1");
        check("LRU: remove existing", r.remove("a"));
        check("LRU: remove missing", !r.remove("a"));
        check("LRU: removed is gone", r.size() == 0 && r.get("a") == null);

        LruCache<String, String> one = new LruCache<>(1);
        one.put("a", "1");
        one.put("b", "2");
        check("LRU: capacity one", one.get("a") == null && "2".equals(one.get("b")));
        checkThrows("LRU: rejects capacity 0", IllegalArgumentException.class, () -> new LruCache<String, String>(0));

        LruCache<String, String> h = new LruCache<>(2);
        h.put("a", "1");
        h.get("a");
        h.get("zz");
        check("LRU: hit and miss counters", h.hits == 1 && h.misses == 1 && Math.abs(h.hitRate() - 0.5) < 1e-9);
        check("LRU: empty hit rate is 0", new LruCache<String, String>(2).hitRate() == 0.0);

        // TTL with an injected clock
        ManualClock clock = new ManualClock(1_000);
        LruCache<String, String> t = new LruCache<>(4, clock);
        t.put("cfg", "v1", 5_000);
        clock.advance(4_999);
        check("TTL: alive just before expiry", "v1".equals(t.get("cfg")));
        clock.advance(1);
        check("TTL: expired at exactly ttl", t.get("cfg") == null);
        check("TTL: expiration counted and slot freed", t.expirations == 1 && t.size() == 0);

        ManualClock clock2 = new ManualClock();
        LruCache<String, String> f = new LruCache<>(2, clock2);
        f.put("short", "x", 10);
        f.put("long", "y");
        clock2.advance(10);
        check("TTL: expired read is a miss", f.get("short") == null);
        f.put("new", "z");
        check("TTL: expired entry freed its slot, live entry kept", "y".equals(f.get("long")) && f.evictions == 0);

        // the failure the lesson warns about: lazy expiry means a dead entry counts toward capacity
        ManualClock clock3 = new ManualClock();
        LruCache<String, String> lazy = new LruCache<>(2, clock3);
        lazy.put("dead", "x", 10);
        lazy.put("live", "y");
        clock3.advance(10);
        lazy.put("new", "z");
        check("TTL: untouched dead entry is evicted as LRU, not expired", "y".equals(lazy.get("live")) && lazy.evictions == 1 && lazy.expirations == 0);
        checkThrows("TTL: rejects ttl 0", IllegalArgumentException.class, () -> new LruCache<String, String>(2).put("a", "1", 0));
        checkThrows("clock cannot go backwards", IllegalArgumentException.class, () -> new ManualClock().advance(-1));

        // LFU
        LfuCache<String, String> l = new LfuCache<>(2);
        l.put("a", "1");
        l.put("b", "2");
        l.get("a");
        l.put("c", "3");
        check("LFU: evicts least frequent", l.get("b") == null && "1".equals(l.get("a")) && "3".equals(l.get("c")));
        check("LFU: evictions counted", l.evictions == 1);

        LfuCache<String, String> tie = new LfuCache<>(2);
        tie.put("a", "1");
        tie.put("b", "2");
        tie.put("c", "3");
        check("LFU: ties break least recently used", tie.get("a") == null && "2".equals(tie.get("b")));

        LfuCache<String, String> fr = new LfuCache<>(3);
        fr.put("a", "1");
        fr.put("a", "1");
        fr.put("a", "1");
        fr.put("b", "2");
        fr.get("b");
        fr.put("c", "3");
        check("LFU: frequencies tracked", fr.frequency("a") == 3 && fr.frequency("b") == 2 && fr.frequency("c") == 1);
        check("LFU: minFrequency is 1", fr.minFrequency() == 1);
        fr.get("c");
        check("LFU: minFrequency rises when bucket empties", fr.minFrequency() == 2);
        check("LFU: missing key frequency 0", fr.frequency("missing") == 0);

        LfuCache<String, String> reset = new LfuCache<>(2);
        reset.put("a", "1");
        reset.get("a");
        reset.get("a");
        reset.put("b", "2");
        reset.get("b");
        check("LFU: minFrequency 2 before insert", reset.minFrequency() == 2);
        reset.put("c", "3");
        check("LFU: insert resets minFrequency to 1 and evicts b", reset.minFrequency() == 1 && reset.get("b") == null);

        LfuCache<String, String> rm = new LfuCache<>(2);
        rm.put("a", "1");
        rm.get("a");
        rm.put("b", "2");
        check("LFU: remove existing", rm.remove("b"));
        check("LFU: remove missing", !rm.remove("b"));
        check("LFU: remove recomputes minFrequency", rm.minFrequency() == 2 && rm.size() == 1);
        checkThrows("LFU: rejects capacity 0", IllegalArgumentException.class, () -> new LfuCache<String, String>(0));

        LfuCache<Integer, Integer> lbig = new LfuCache<>(10);
        boolean lbounded = true;
        for (int i = 0; i < 1_000; i++) {
            lbig.put(i, i);
            if (lbig.size() > 10) lbounded = false;
        }
        check("LFU: capacity never exceeded", lbounded);

        // deterministic trace agrees with Python and Go
        check("Rng(42).next()", new Rng(42).next() == 0xBDD732262FEB6E95L);
        Rng rng = new Rng(42);
        check("Rng nextInt sequence", rng.nextInt(100) == 51 && rng.nextInt(100) == 53 && rng.nextInt(100) == 71 && rng.nextInt(100) == 35 && rng.nextInt(100) == 65);
        check("binTrace first six", Trace.binTrace(1, 6, 0).equals(List.of("bin-8", "bin-12", "bin-1271", "bin-15", "bin-7", "bin-382")));

        LruCache<String, String> lruT = new LruCache<>(32);
        LfuCache<String, String> lfuT = new LfuCache<>(32);
        List<String> phase1 = Trace.binTrace(1, 6000, 0);
        List<String> scan = Trace.scan(500);
        List<String> after = Trace.binTrace(2, 2000, 0).subList(0, 200);
        Trace.replay(lruT, phase1);
        Trace.replay(lfuT, phase1);
        Trace.replay(lruT, scan);
        Trace.replay(lfuT, scan);
        double lruAfter = Trace.replay(lruT, after), lfuAfter = Trace.replay(lfuT, after);
        check("trace: LFU survives the scan, LRU does not", lfuAfter > lruAfter + 0.1);
        check("trace: exact post-scan rates", Math.abs(lruAfter - 0.570) < 1e-3 && Math.abs(lfuAfter - 0.725) < 1e-3);

        LruCache<String, String> lruS = new LruCache<>(32);
        LfuCache<String, String> lfuS = new LfuCache<>(32);
        Trace.replay(lruS, Trace.binTrace(1, 6000, 0));
        Trace.replay(lfuS, Trace.binTrace(1, 6000, 0));
        List<String> phase2 = Trace.binTrace(3, 6000, 20);
        double lruShift = Trace.replay(lruS, phase2), lfuShift = Trace.replay(lfuS, phase2);
        check("trace: LRU adapts to a shifted hot set", lruShift > 0.7);
        check("trace: LFU lags behind a shifted hot set", lfuShift < 0.5);

        // stale cached balance approves an overdraft
        LruCache<String, Money> cache = new LruCache<>(4);
        Money ledger = new Money(10_000, "USD");
        cache.put("acct-7", ledger);
        ledger = ledger.minus(new Money(8_000, "USD"));
        Money cached = cache.get("acct-7");
        check("money: cache is stale", cached.toString().equals("100.00 USD") && ledger.toString().equals("20.00 USD"));
        check("money: cache approves what the ledger would decline",
                cached.minus(new Money(5_000, "USD")).minorUnits() >= 0 && ledger.minus(new Money(5_000, "USD")).minorUnits() < 0);
        final Money usd = ledger;
        checkThrows("money: currency mismatch", IllegalArgumentException.class, () -> usd.plus(new Money(1, "EUR")));

        System.out.printf("%d passed, %d failed%n", passed, failures.size());
        failures.forEach(x -> System.out.println("FAIL: " + x));
        if (!failures.isEmpty()) System.exit(1);
    }
}
