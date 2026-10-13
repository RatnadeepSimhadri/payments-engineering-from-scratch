// Lesson: phases/09-reliability-and-scale/03-caching-strategies/docs/en.md
// Caching Strategies: Cache-Aside, Write-Through, Write-Behind
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: Memcached protocol docs (cache-aside idiom); Redis documentation on EXPIRE semantics;
//          Phase 10 Lesson 01 for the Money type. Java 21, standard library only.

import java.util.ArrayDeque;
import java.util.ArrayList;
import java.util.HashMap;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.Objects;
import java.util.Random;

/** ISO 4217 currency: a three-letter code and the number of decimal places in its minor unit. */
record Currency(String code, int exponent) {
    static final Currency USD = new Currency("USD", 2);

    Currency {
        if (code == null || code.length() != 3 || !code.chars().allMatch(Character::isUpperCase)) {
            throw new IllegalArgumentException("currency code must be three uppercase letters: " + code);
        }
        if (exponent < 0 || exponent > 4) throw new IllegalArgumentException("exponent must be 0..4: " + exponent);
    }

    long minorUnitsPerMajor() {
        long scale = 1;
        for (int i = 0; i < exponent; i++) scale *= 10;
        return scale;
    }
}

final class CurrencyMismatchException extends IllegalArgumentException {
    CurrencyMismatchException(Currency a, Currency b) { super("cannot combine " + a.code() + " with " + b.code()); }
}

/** An exact amount of one currency as a signed count of minor units (Phase 10, Lesson 01). */
record Money(long minorUnits, Currency currency) implements Comparable<Money> {
    Money { Objects.requireNonNull(currency, "currency"); }

    static Money ofMinor(long minorUnits, Currency currency) { return new Money(minorUnits, currency); }

    private void requireSameCurrency(Money other) {
        if (!currency.equals(other.currency)) throw new CurrencyMismatchException(currency, other.currency);
    }

    Money plus(Money other) { requireSameCurrency(other); return new Money(Math.addExact(minorUnits, other.minorUnits), currency); }
    Money minus(Money other) { requireSameCurrency(other); return new Money(Math.subtractExact(minorUnits, other.minorUnits), currency); }
    boolean isNegative() { return minorUnits < 0; }

    @Override public int compareTo(Money other) { requireSameCurrency(other); return Long.compare(minorUnits, other.minorUnits); }

    @Override public String toString() {
        long scale = currency.minorUnitsPerMajor();
        long whole = Math.abs(minorUnits) / scale, frac = Math.abs(minorUnits) % scale;
        String sign = minorUnits < 0 ? "-" : "";
        if (currency.exponent() == 0) return sign + whole + " " + currency.code();
        return sign + whole + "." + String.format("%0" + currency.exponent() + "d", frac) + " " + currency.code();
    }
}

/** A clock the demo and the tests move by hand. Nothing in this lesson sleeps. */
final class ManualClock {
    private long nowMs;
    ManualClock(long startMs) { this.nowMs = startMs; }
    long now() { return nowMs; }
    void advance(long ms) { if (ms < 0) throw new IllegalArgumentException("clock only moves forward"); nowMs += ms; }
}

/**
 * The slow system of record: a database holding account balances. Every read and write costs a fixed
 * simulated latency that is accumulated, not slept. {@link #peek} is the oracle the staleness meter uses;
 * it is not a real operation and is not counted.
 */
final class BackingStore {
    private final Map<String, Money> rows = new HashMap<>();
    private final long readLatencyMs, writeLatencyMs;
    long reads, writes, simulatedLatencyMs;

    BackingStore(long readLatencyMs, long writeLatencyMs) {
        this.readLatencyMs = readLatencyMs;
        this.writeLatencyMs = writeLatencyMs;
    }

    Money read(String account) {
        reads++;
        simulatedLatencyMs += readLatencyMs;
        return rows.get(account);
    }

    void write(String account, Money balance) {
        writes++;
        simulatedLatencyMs += writeLatencyMs;
        rows.put(account, balance);
    }

    /** Truth without cost: what the balance really is right now. Used only to measure staleness. */
    Money peek(String account) { return rows.get(account); }
}

/** Hit, miss, and staleness counters for one strategy under one workload. */
final class CacheStats {
    long hits, misses, staleReads, cacheLatencyMs;
    final long cacheLatencyPerOpMs;

    CacheStats(long cacheLatencyPerOpMs) { this.cacheLatencyPerOpMs = cacheLatencyPerOpMs; }

    void hit() { hits++; cacheLatencyMs += cacheLatencyPerOpMs; }
    void miss() { misses++; cacheLatencyMs += cacheLatencyPerOpMs; }
    long reads() { return hits + misses; }
    double hitRate() { return reads() == 0 ? 0.0 : (double) hits / reads(); }
}

/** A map of key to (value, expiry). Expiry is checked lazily on read against the injected clock. */
final class TtlCache {
    record Entry(Money value, long expiresAtMs) {}
    static final long NO_EXPIRY = Long.MAX_VALUE;

    private final Map<String, Entry> entries = new HashMap<>();
    private final ManualClock clock;

    TtlCache(ManualClock clock) { this.clock = clock; }

    /** Returns the cached value, or null when absent or expired. An expired entry is removed on the way out. */
    Money get(String key) {
        Entry e = entries.get(key);
        if (e == null) return null;
        if (e.expiresAtMs() <= clock.now()) { entries.remove(key); return null; }
        return e.value();
    }

    void put(String key, Money value, long ttlMs) {
        long expires = ttlMs == NO_EXPIRY ? NO_EXPIRY : Math.addExact(clock.now(), ttlMs);
        entries.put(key, new Entry(value, expires));
    }

    boolean invalidate(String key) { return entries.remove(key) != null; }
    boolean contains(String key) { return get(key) != null; }
    int size() { return entries.size(); }
    void clear() { entries.clear(); }
}

/** The one interface every strategy implements, so the workload can drive them interchangeably. */
interface BalanceCache {
    String name();
    /** The balance the application sees. May be stale depending on the strategy. */
    Money read(String account);
    /** Record a new balance. Where it goes first is the strategy. */
    void write(String account, Money balance);
    CacheStats stats();
    BackingStore store();
    TtlCache cache();

    /** Counts a read as stale when the value handed to the caller differs from the store's current truth. */
    default Money readMeasured(String account) {
        Money seen = read(account);
        Money truth = store().peek(account);
        if (!Objects.equals(seen, truth)) stats().staleReads++;
        return seen;
    }
}

/**
 * Cache-aside (lazy loading): the application checks the cache, loads from the store on a miss, and
 * populates the cache with a TTL. Writes go to the store. Whether the write also invalidates the cached
 * entry is the whole difference between "eventually right" and "wrong for up to one TTL".
 */
final class CacheAside implements BalanceCache {
    private final BackingStore store;
    private final TtlCache cache;
    private final CacheStats stats;
    private final long ttlMs;
    private final boolean invalidateOnWrite;

    CacheAside(BackingStore store, ManualClock clock, long ttlMs, boolean invalidateOnWrite, long cacheLatencyMs) {
        this.store = store;
        this.cache = new TtlCache(clock);
        this.stats = new CacheStats(cacheLatencyMs);
        this.ttlMs = ttlMs;
        this.invalidateOnWrite = invalidateOnWrite;
    }

    @Override public String name() { return invalidateOnWrite ? "cache-aside + invalidate" : "cache-aside, TTL only"; }

    @Override public Money read(String account) {
        Money cached = cache.get(account);
        if (cached != null) { stats.hit(); return cached; }
        stats.miss();
        Money loaded = store.read(account);
        if (loaded != null) cache.put(account, loaded, ttlMs);
        return loaded;
    }

    @Override public void write(String account, Money balance) {
        store.write(account, balance);
        if (invalidateOnWrite) cache.invalidate(account);
    }

    @Override public CacheStats stats() { return stats; }
    @Override public BackingStore store() { return store; }
    @Override public TtlCache cache() { return cache; }
}

/**
 * Read-through: the cache owns the loader, so callers can never forget to populate it or populate it
 * with the wrong key. Same read path as cache-aside, moved behind the interface. Writes go to the store
 * and delete the entry (write-around with invalidation); the next read reloads.
 */
final class ReadThrough implements BalanceCache {
    interface Loader { Money load(String account); }

    private final BackingStore store;
    private final TtlCache cache;
    private final CacheStats stats;
    private final Loader loader;
    private final long ttlMs;

    ReadThrough(BackingStore store, ManualClock clock, long ttlMs, long cacheLatencyMs) {
        this.store = store;
        this.cache = new TtlCache(clock);
        this.stats = new CacheStats(cacheLatencyMs);
        this.loader = store::read;
        this.ttlMs = ttlMs;
    }

    @Override public String name() { return "read-through"; }

    @Override public Money read(String account) {
        Money cached = cache.get(account);
        if (cached != null) { stats.hit(); return cached; }
        stats.miss();
        Money loaded = loader.load(account);
        if (loaded != null) cache.put(account, loaded, ttlMs);
        return loaded;
    }

    @Override public void write(String account, Money balance) {
        store.write(account, balance);
        cache.invalidate(account);
    }

    @Override public CacheStats stats() { return stats; }
    @Override public BackingStore store() { return store; }
    @Override public TtlCache cache() { return cache; }
}

/**
 * Write-through: every write goes to the store and then into the cache, synchronously. The cache is
 * never stale with respect to writes that went through it; the cost is store latency on every write and
 * cache space spent on keys nobody will read.
 */
final class WriteThrough implements BalanceCache {
    private final BackingStore store;
    private final TtlCache cache;
    private final CacheStats stats;
    private final long ttlMs;

    WriteThrough(BackingStore store, ManualClock clock, long ttlMs, long cacheLatencyMs) {
        this.store = store;
        this.cache = new TtlCache(clock);
        this.stats = new CacheStats(cacheLatencyMs);
        this.ttlMs = ttlMs;
    }

    @Override public String name() { return "write-through"; }

    @Override public Money read(String account) {
        Money cached = cache.get(account);
        if (cached != null) { stats.hit(); return cached; }
        stats.miss();
        Money loaded = store.read(account);
        if (loaded != null) cache.put(account, loaded, ttlMs);
        return loaded;
    }

    @Override public void write(String account, Money balance) {
        store.write(account, balance);           // store first: if this fails, the cache never sees the value
        cache.put(account, balance, ttlMs);
    }

    @Override public CacheStats stats() { return stats; }
    @Override public BackingStore store() { return store; }
    @Override public TtlCache cache() { return cache; }
}

/**
 * Write-behind (write-back): writes land in the cache and a dirty queue; the store is updated later by
 * {@link #flush}, which coalesces repeated writes to one key into one store write. Fast and cheap, and the
 * only strategy here where a cache crash loses committed-looking data.
 */
final class WriteBehind implements BalanceCache {
    private final BackingStore store;
    private final TtlCache cache;
    private final CacheStats stats;
    private final LinkedHashMap<String, Money> dirty = new LinkedHashMap<>();
    private final ArrayDeque<String> order = new ArrayDeque<>();
    long lostWrites;

    WriteBehind(BackingStore store, ManualClock clock, long cacheLatencyMs) {
        this.store = store;
        this.cache = new TtlCache(clock);
        this.stats = new CacheStats(cacheLatencyMs);
    }

    @Override public String name() { return "write-behind"; }

    @Override public Money read(String account) {
        Money cached = cache.get(account);
        if (cached != null) { stats.hit(); return cached; }
        stats.miss();
        Money loaded = store.read(account);
        if (loaded != null) cache.put(account, loaded, TtlCache.NO_EXPIRY);   // dirty data must never expire
        return loaded;
    }

    @Override public void write(String account, Money balance) {
        cache.put(account, balance, TtlCache.NO_EXPIRY);
        if (dirty.put(account, balance) == null) order.addLast(account);
    }

    int dirtyCount() { return dirty.size(); }

    /** Writes every dirty key to the store once, in first-dirtied order. Returns the number of store writes. */
    int flush() {
        int flushed = 0;
        while (!order.isEmpty()) {
            String key = order.pollFirst();
            store.write(key, dirty.remove(key));
            flushed++;
        }
        return flushed;
    }

    /** The cache process dies before flushing. Everything dirty is gone; the store keeps the old balances. */
    void crash() {
        lostWrites += dirty.size();
        dirty.clear();
        order.clear();
        cache.clear();
    }

    @Override public CacheStats stats() { return stats; }
    @Override public BackingStore store() { return store; }
    @Override public TtlCache cache() { return cache; }
}

/**
 * A seeded, skewed workload: most reads hit a few hot accounts (the 80/20 shape real balance traffic has),
 * writes are debits of a random amount. Deterministic for a given seed so every strategy sees the same ops.
 */
final class Workload {
    record Op(boolean isWrite, String account, long debitMinor) {}

    static List<Op> generate(long seed, int ops, int accounts, double writeFraction, double hotFraction, double hotShare) {
        Random rng = new Random(seed);
        int hotCount = Math.max(1, (int) Math.round(accounts * hotFraction));
        List<Op> out = new ArrayList<>(ops);
        for (int i = 0; i < ops; i++) {
            boolean hot = rng.nextDouble() < hotShare;
            int idx = hot ? rng.nextInt(hotCount) : hotCount + rng.nextInt(Math.max(1, accounts - hotCount));
            String account = String.format("acct-%04d", idx % accounts);
            boolean isWrite = rng.nextDouble() < writeFraction;
            out.add(new Op(isWrite, account, isWrite ? 1 + rng.nextInt(2000) : 0));
        }
        return out;
    }

    static BackingStore seededStore(int accounts, long readLatencyMs, long writeLatencyMs) {
        BackingStore store = new BackingStore(readLatencyMs, writeLatencyMs);
        for (int i = 0; i < accounts; i++) store.write(String.format("acct-%04d", i), Money.ofMinor(100_000, Currency.USD));
        store.reads = 0; store.writes = 0; store.simulatedLatencyMs = 0;   // seeding is not part of the measurement
        return store;
    }

    /** Runs the ops. Each write reads the current balance through the cache, debits, and writes back. */
    static void run(BalanceCache target, List<Op> ops, ManualClock clock, long msPerOp) {
        for (Op op : ops) {
            if (op.isWrite()) {
                Money current = target.readMeasured(op.account());
                target.write(op.account(), current.minus(Money.ofMinor(op.debitMinor(), Currency.USD)));
            } else {
                target.readMeasured(op.account());
            }
            clock.advance(msPerOp);
        }
    }
}

public final class Main {
    static String row(BalanceCache c) {
        CacheStats s = c.stats();
        BackingStore st = c.store();
        return String.format("%-26s hit rate %5.1f%%  store reads %5d  store writes %5d  divergent %5d  simulated latency %7d ms",
                c.name(), 100 * s.hitRate(), st.reads, st.writes, s.staleReads, st.simulatedLatencyMs + s.cacheLatencyMs);
    }

    public static void main(String[] args) {
        Currency USD = Currency.USD;

        System.out.println("== The stale balance hazard: cache-aside with TTL, no invalidation ==");
        ManualClock clock = new ManualClock(0);
        BackingStore store = new BackingStore(20, 25);
        store.write("acct-0001", Money.ofMinor(10_000, USD));
        CacheAside ttlOnly = new CacheAside(store, clock, 30_000, false, 1);
        System.out.println("balance read through cache:        " + ttlOnly.read("acct-0001") + "  (miss, now cached for 30s)");
        store.write("acct-0001", Money.ofMinor(2_000, USD));        // a batch job debits 80.00 straight to the database
        System.out.println("batch job debits 80.00 in the DB:  store = " + store.peek("acct-0001"));
        Money seen = ttlOnly.read("acct-0001");
        System.out.println("authorization reads the cache:     " + seen + "  (hit; STALE by " + seen.minus(store.peek("acct-0001")) + ")");
        Money request = Money.ofMinor(5_000, USD);
        boolean approved = seen.compareTo(request) >= 0;
        System.out.println("approve 50.00 withdrawal?          " + approved + "  -> store balance would become "
                + store.peek("acct-0001").minus(request));
        clock.advance(30_000);
        System.out.println("after the 30s TTL:                 " + ttlOnly.read("acct-0001") + "  (miss, reloaded; correct again)");

        System.out.println();
        System.out.println("== Same scenario with invalidate-on-write ==");
        ManualClock clock2 = new ManualClock(0);
        BackingStore store2 = new BackingStore(20, 25);
        store2.write("acct-0001", Money.ofMinor(10_000, USD));
        CacheAside invalidating = new CacheAside(store2, clock2, 30_000, true, 1);
        invalidating.read("acct-0001");
        invalidating.write("acct-0001", Money.ofMinor(2_000, USD));
        System.out.println("read after write:                  " + invalidating.read("acct-0001") + "  (miss, cache entry was removed by the write)");

        System.out.println();
        System.out.println("== Write-behind: fast until the cache dies ==");
        ManualClock clock3 = new ManualClock(0);
        BackingStore store3 = new BackingStore(20, 25);
        store3.write("acct-0007", Money.ofMinor(50_000, USD));
        WriteBehind wb = new WriteBehind(store3, clock3, 1);
        wb.write("acct-0007", Money.ofMinor(45_000, USD));
        wb.write("acct-0007", Money.ofMinor(41_000, USD));
        wb.write("acct-0007", Money.ofMinor(40_000, USD));
        System.out.println("three debits applied; cache says   " + wb.read("acct-0007") + ", store still says " + store3.peek("acct-0007")
                + ", dirty keys = " + wb.dirtyCount());
        long before = store3.writes;
        int flushed = wb.flush();
        System.out.println("flush(): " + flushed + " store write for 3 debits (coalesced); store now " + store3.peek("acct-0007")
                + "; store writes went " + before + " -> " + store3.writes);
        wb.write("acct-0007", Money.ofMinor(30_000, USD));
        wb.crash();
        System.out.println("one more debit, then crash before flush: store = " + store3.peek("acct-0007") + ", lost writes = " + wb.lostWrites);

        System.out.println();
        System.out.println("== Measured: 20,000 ops, 500 accounts, 20% of accounts take 80% of traffic, 15% writes ==");
        System.out.println("   store read = 20 ms, store write = 25 ms, cache op = 1 ms, TTL = 5 s, one op per 10 ms");
        List<Workload.Op> ops = Workload.generate(42, 20_000, 500, 0.15, 0.20, 0.80);
        List<BalanceCache> strategies = new ArrayList<>();
        ManualClock c1 = new ManualClock(0); strategies.add(new CacheAside(Workload.seededStore(500, 20, 25), c1, 5_000, false, 1));
        ManualClock c2 = new ManualClock(0); strategies.add(new CacheAside(Workload.seededStore(500, 20, 25), c2, 5_000, true, 1));
        ManualClock c3 = new ManualClock(0); strategies.add(new ReadThrough(Workload.seededStore(500, 20, 25), c3, 5_000, 1));
        ManualClock c4 = new ManualClock(0); strategies.add(new WriteThrough(Workload.seededStore(500, 20, 25), c4, 5_000, 1));
        ManualClock c5 = new ManualClock(0); strategies.add(new WriteBehind(Workload.seededStore(500, 20, 25), c5, 1));
        ManualClock[] clocks = {c1, c2, c3, c4, c5};
        for (int i = 0; i < strategies.size(); i++) {
            Workload.run(strategies.get(i), ops, clocks[i], 10);
            if (strategies.get(i) instanceof WriteBehind w) w.flush();
            System.out.println(row(strategies.get(i)));
        }
        long noCacheLatency = 0;   // every read 20 ms; every write is a read plus a write, 45 ms
        for (Workload.Op op : ops) noCacheLatency += op.isWrite() ? 45 : 20;
        System.out.printf("%-26s hit rate   0.0%%  store reads %5d  store writes %5d  divergent     0  simulated latency %7d ms%n",
                "no cache", ops.size(), ops.stream().filter(Workload.Op::isWrite).count(), noCacheLatency);
        System.out.println();
        System.out.println("\"divergent\" = reads where the value handed to the caller differed from the store at that moment.");
        System.out.println("For cache-aside that is a stale balance an authorization trusted. For write-behind the arrow is reversed:");
        System.out.println("the cache is right and the store is behind, which is durability exposure rather than staleness.");
    }
}
