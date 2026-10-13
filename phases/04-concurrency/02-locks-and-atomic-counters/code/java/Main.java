// Lesson: phases/04-concurrency/02-locks-and-atomic-counters/docs/en.md
// Locks, Atomics, and a Balance That Never Lies
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: JLS 17.4 (Java Memory Model); java.util.concurrent.locks.ReentrantLock and
// java.util.concurrent.atomic.AtomicLong Javadoc; Herlihy & Shavit, The Art of Multiprocessor Programming, ch. 5.
// Java 21, standard library only. Same design as the Python and Go versions.

import java.util.Objects;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.atomic.AtomicLong;
import java.util.concurrent.atomic.AtomicReference;
import java.util.concurrent.locks.ReentrantLock;
import java.util.function.LongSupplier;

/** ISO 4217 currency: code and minor-unit exponent. Copied from Phase 10, Lesson 01. */
record Currency(String code, int exponent) {
    static final Currency USD = new Currency("USD", 2);
    static final Currency EUR = new Currency("EUR", 2);

    Currency {
        if (code == null || code.length() != 3) throw new IllegalArgumentException("currency code must be three letters: " + code);
        if (exponent < 0 || exponent > 4) throw new IllegalArgumentException("exponent must be 0..4: " + exponent);
    }

    long minorUnitsPerMajor() {
        long scale = 1;
        for (int i = 0; i < exponent; i++) scale *= 10;
        return scale;
    }
}

final class CurrencyMismatchException extends IllegalArgumentException {
    CurrencyMismatchException(Currency a, Currency b) {
        super("cannot combine " + a.code() + " with " + b.code());
    }
}

final class InsufficientFundsException extends IllegalStateException {
    InsufficientFundsException(String accountId, long balance, long requested) {
        super("account " + accountId + " has " + balance + " minor units, debit of " + requested + " refused");
    }
}

/** Exact amount in minor units of one currency; immutable, so safe to share between threads. */
record Money(long minorUnits, Currency currency) {
    Money { Objects.requireNonNull(currency, "currency"); }

    static Money ofMinor(long minorUnits, Currency currency) { return new Money(minorUnits, currency); }

    private void requireSameCurrency(Money other) {
        if (!currency.equals(other.currency)) throw new CurrencyMismatchException(currency, other.currency);
    }

    Money plus(Money other) {
        requireSameCurrency(other);
        return new Money(Math.addExact(minorUnits, other.minorUnits), currency);
    }

    Money minus(Money other) {
        requireSameCurrency(other);
        return new Money(Math.subtractExact(minorUnits, other.minorUnits), currency);
    }

    boolean isNegative() { return minorUnits < 0; }

    @Override
    public String toString() {
        long magnitude = Math.abs(minorUnits);
        long scale = currency.minorUnitsPerMajor();
        String result = Long.toString(magnitude / scale);
        if (currency.exponent() > 0) {
            String frac = Long.toString(magnitude % scale);
            result = result + "." + "0".repeat(currency.exponent() - frac.length()) + frac;
        }
        return (minorUnits < 0 ? "-" : "") + result + " " + currency.code();
    }
}

/** A balance in minor units that many threads add to. Three implementations, one contract. */
interface BalanceCounter {
    void add(long delta);
    long get();
    String name();
}

/**
 * The bug. `balance += delta` compiles to getfield, ladd, putfield: three steps any other thread
 * can interleave with. The hook runs between the load and the store so a test can force the
 * interleaving deterministically instead of hoping the scheduler produces it.
 */
final class RacyCounter implements BalanceCounter {
    private long balance;
    private final Runnable betweenLoadAndStore;

    RacyCounter() { this(() -> { }); }

    RacyCounter(Runnable betweenLoadAndStore) {
        this.balance = 0;
        this.betweenLoadAndStore = Objects.requireNonNull(betweenLoadAndStore);
    }

    @Override
    public void add(long delta) {
        long loaded = balance;          // 1. load
        betweenLoadAndStore.run();      //    (another thread may run here)
        balance = loaded + delta;       // 2. store a value computed from a possibly stale load
    }

    @Override public long get() { return balance; }
    @Override public String name() { return "racy"; }
}

/**
 * Fix 1: mutual exclusion. The lock makes load-add-store one indivisible step, and the
 * unlock-then-lock edge (JLS 17.4.5) makes every write visible to the next thread that locks.
 * ReentrantLock rather than synchronized so the same object can later be striped and tried.
 */
final class LockedCounter implements BalanceCounter {
    private final ReentrantLock lock = new ReentrantLock();
    private long balance;

    @Override
    public void add(long delta) {
        lock.lock();
        try {
            balance = Math.addExact(balance, delta);
        } finally {
            lock.unlock();
        }
    }

    @Override
    public long get() {
        lock.lock();
        try {
            return balance;
        } finally {
            lock.unlock();
        }
    }

    @Override public String name() { return "locked"; }
}

/**
 * Fix 2: compare-and-swap. Load, compute, then publish only if nobody else published since the
 * load; otherwise retry. No thread ever blocks. The retry count is the contention you paid for.
 */
final class AtomicCounter implements BalanceCounter {
    private final AtomicLong value = new AtomicLong();
    private final AtomicLong retries = new AtomicLong();

    @Override
    public void add(long delta) {
        while (true) {
            long current = value.get();
            long next = Math.addExact(current, delta);
            if (value.compareAndSet(current, next)) return;
            retries.incrementAndGet();
        }
    }

    @Override public long get() { return value.get(); }
    @Override public String name() { return "atomic"; }

    long casRetries() { return retries.get(); }
}

/**
 * Fix 3: lock striping. Many accounts, a fixed number of locks; an account's stripe is a hash of
 * its id. Updates to different accounts on different stripes never contend. A debit is a
 * check-then-act (is the balance enough? then subtract), which a bare atomic cannot express
 * without a CAS loop; under the stripe lock it is one line.
 */
final class StripedAccountMap {
    private final Currency currency;
    private final ReentrantLock[] locks;
    private final java.util.HashMap<String, Long>[] buckets;

    @SuppressWarnings("unchecked")
    StripedAccountMap(Currency currency, int stripes) {
        if (stripes <= 0) throw new IllegalArgumentException("stripes must be positive: " + stripes);
        this.currency = Objects.requireNonNull(currency);
        this.locks = new ReentrantLock[stripes];
        this.buckets = (java.util.HashMap<String, Long>[]) new java.util.HashMap[stripes];
        for (int i = 0; i < stripes; i++) {
            locks[i] = new ReentrantLock();
            buckets[i] = new java.util.HashMap<>();
        }
    }

    /** Stable, non-negative, in [0, stripes). Same id always maps to the same lock, in every language. */
    int stripeIndex(String accountId) {
        return Math.floorMod(stableHash(accountId), locks.length);
    }

    /** The classic 31-multiplier string hash on 32-bit wrap-around; identical in Java, Python, and Go. */
    static int stableHash(String s) {
        int h = 0;
        for (int i = 0; i < s.length(); i++) h = 31 * h + s.charAt(i);
        return h;
    }

    int stripes() { return locks.length; }

    void open(String accountId) {
        int s = stripeIndex(accountId);
        locks[s].lock();
        try {
            buckets[s].putIfAbsent(accountId, 0L);
        } finally {
            locks[s].unlock();
        }
    }

    void credit(String accountId, Money amount) {
        requireValid(amount);
        int s = stripeIndex(accountId);
        locks[s].lock();
        try {
            long current = requireOpen(s, accountId);
            buckets[s].put(accountId, Math.addExact(current, amount.minorUnits()));
        } finally {
            locks[s].unlock();
        }
    }

    /** Check-then-act under the stripe lock: the balance cannot change between the check and the write. */
    void debit(String accountId, Money amount) {
        requireValid(amount);
        int s = stripeIndex(accountId);
        locks[s].lock();
        try {
            long current = requireOpen(s, accountId);
            if (current < amount.minorUnits()) throw new InsufficientFundsException(accountId, current, amount.minorUnits());
            buckets[s].put(accountId, current - amount.minorUnits());
        } finally {
            locks[s].unlock();
        }
    }

    Money balance(String accountId) {
        int s = stripeIndex(accountId);
        locks[s].lock();
        try {
            return Money.ofMinor(requireOpen(s, accountId), currency);
        } finally {
            locks[s].unlock();
        }
    }

    /**
     * Sum of every balance. Takes every stripe lock, in index order, so the sum is a consistent
     * snapshot and two concurrent total() calls cannot deadlock each other (Lesson 07 is about why
     * the order matters).
     */
    Money total() {
        for (ReentrantLock lock : locks) lock.lock();
        try {
            long sum = 0;
            for (var bucket : buckets) for (long v : bucket.values()) sum = Math.addExact(sum, v);
            return Money.ofMinor(sum, currency);
        } finally {
            for (int i = locks.length - 1; i >= 0; i--) locks[i].unlock();
        }
    }

    long totalMinor() { return total().minorUnits(); }

    private long requireOpen(int stripe, String accountId) {
        Long current = buckets[stripe].get(accountId);
        if (current == null) throw new IllegalArgumentException("no such account: " + accountId);
        return current;
    }

    private void requireValid(Money amount) {
        if (!amount.currency().equals(currency)) throw new CurrencyMismatchException(currency, amount.currency());
        if (amount.isNegative()) throw new IllegalArgumentException("amount must not be negative: " + amount);
    }
}

/** An account whose balance lives in a counter. Money in, Money out; minor units inside. */
final class Account {
    private final String id;
    private final Currency currency;
    private final BalanceCounter counter;

    Account(String id, Currency currency, BalanceCounter counter) {
        this.id = Objects.requireNonNull(id);
        this.currency = Objects.requireNonNull(currency);
        this.counter = Objects.requireNonNull(counter);
    }

    void credit(Money amount) {
        requireValid(amount);
        counter.add(amount.minorUnits());
    }

    void debit(Money amount) {
        requireValid(amount);
        counter.add(Math.negateExact(amount.minorUnits()));
    }

    private void requireValid(Money amount) {
        if (!amount.currency().equals(currency)) throw new CurrencyMismatchException(currency, amount.currency());
        if (amount.isNegative()) throw new IllegalArgumentException("amount must not be negative: " + amount);
    }

    Money balance() { return Money.ofMinor(counter.get(), currency); }
    String id() { return id; }
}

/** Injected time source so throughput math is testable without a real clock. */
interface Stopwatch {
    long nanos();

    static Stopwatch system() { return System::nanoTime; }
}

/** Returns scripted readings in order; repeats the last one. */
final class FakeStopwatch implements Stopwatch {
    private final long[] readings;
    private int next;

    FakeStopwatch(long... readings) {
        if (readings.length == 0) throw new IllegalArgumentException("need at least one reading");
        this.readings = readings.clone();
    }

    @Override
    public long nanos() {
        long value = readings[Math.min(next, readings.length - 1)];
        next++;
        return value;
    }
}

record BenchmarkResult(String name, int threads, int opsPerThread, long delta, long observed, long elapsedNanos) {
    long expected() { return Math.multiplyExact(Math.multiplyExact((long) threads, opsPerThread), delta); }
    long lost() { return expected() - observed; }
    boolean exact() { return lost() == 0; }

    double opsPerSecond() {
        long ops = (long) threads * opsPerThread;
        return elapsedNanos <= 0 ? Double.POSITIVE_INFINITY : ops * 1_000_000_000.0 / elapsedNanos;
    }
}

/** One unit of work for a benchmark thread: which thread, which iteration. */
interface Worker {
    void run(int threadIndex, int iteration);
}

/** Runs N platform threads, all released together by a latch, then reads the observed total. */
final class Benchmark {
    static BenchmarkResult run(String name, int threads, int opsPerThread, long delta, Worker worker,
                               LongSupplier observe, Stopwatch stopwatch) throws InterruptedException {
        if (threads <= 0 || opsPerThread < 0) throw new IllegalArgumentException("threads > 0, opsPerThread >= 0");
        CountDownLatch start = new CountDownLatch(1);
        CountDownLatch done = new CountDownLatch(threads);
        AtomicReference<Throwable> failure = new AtomicReference<>();
        Thread[] pool = new Thread[threads];
        for (int t = 0; t < threads; t++) {
            final int threadIndex = t;
            pool[t] = new Thread(() -> {
                try {
                    start.await();
                    for (int i = 0; i < opsPerThread; i++) worker.run(threadIndex, i);
                } catch (Throwable e) {
                    failure.compareAndSet(null, e);
                } finally {
                    done.countDown();
                }
            }, "bench-" + t);
            pool[t].start();
        }
        long before = stopwatch.nanos();
        start.countDown();               // all threads begin together: maximum contention
        done.await();                    // countDown() happens-before await() returning, so observe() sees every write
        long after = stopwatch.nanos();
        for (Thread thread : pool) thread.join();
        if (failure.get() != null) throw new IllegalStateException("worker failed: " + failure.get(), failure.get());
        return new BenchmarkResult(name, threads, opsPerThread, delta, observe.getAsLong(), after - before);
    }

    /** Every thread adds `delta` to the same counter `opsPerThread` times. */
    static BenchmarkResult measure(BalanceCounter counter, int threads, int opsPerThread, long delta, Stopwatch stopwatch)
            throws InterruptedException {
        return run(counter.name(), threads, opsPerThread, delta, (t, i) -> counter.add(delta), counter::get, stopwatch);
    }

    /** Thread t credits account ids[t % ids.length]; observed is the map total. */
    static BenchmarkResult measureMap(StripedAccountMap map, String[] ids, int threads, int opsPerThread, long delta,
                                      Stopwatch stopwatch) throws InterruptedException {
        Money amount = Money.ofMinor(delta, Currency.USD);
        return run("striped-map(" + map.stripes() + ")", threads, opsPerThread, delta,
                (t, i) -> map.credit(ids[t % ids.length], amount), map::totalMinor, stopwatch);
    }
}

public final class Main {
    static String line(BenchmarkResult r, Currency c, String note) {
        return String.format("%-16s observed %14s  lost %12s  %,12.0f ops/s%s",
                r.name(), Money.ofMinor(r.observed(), c), Money.ofMinor(r.lost(), c), r.opsPerSecond(), note);
    }

    public static void main(String[] args) throws InterruptedException {
        Currency usd = Currency.USD;

        System.out.println("== A lost update, forced deterministically ==");
        RacyCounter[] holder = new RacyCounter[1];
        boolean[] interleaved = {false};
        holder[0] = new RacyCounter(() -> {
            if (!interleaved[0]) {           // the first add pauses between load and store...
                interleaved[0] = true;
                holder[0].add(1);            // ...while a second add runs to completion
            }
        });
        holder[0].add(1);
        System.out.println("two credits of 0.01 USD, balance = " + Money.ofMinor(holder[0].get(), usd)
                + " (expected 0.02 USD): the second write overwrote the first");

        final int threads = 4, ops = 250_000;
        Money expected = Money.ofMinor((long) threads * ops, usd);
        System.out.printf("%n== %d threads each credit 0.01 USD %,d times: expected %s ==%n", threads, ops, expected);

        BenchmarkResult racy = Benchmark.measure(new RacyCounter(), threads, ops, 1, Stopwatch.system());
        System.out.println(line(racy, usd, "   <- varies run to run; any loss is the proof"));

        BenchmarkResult locked = Benchmark.measure(new LockedCounter(), threads, ops, 1, Stopwatch.system());
        System.out.println(line(locked, usd, ""));

        AtomicCounter atomic = new AtomicCounter();
        BenchmarkResult cas = Benchmark.measure(atomic, threads, ops, 1, Stopwatch.system());
        System.out.println(line(cas, usd, String.format("   CAS retries: %,d (varies)", atomic.casRetries())));

        String[] ids = {"acct-1", "acct-2", "acct-3", "acct-4", "acct-5", "acct-6", "acct-7", "acct-8"};
        StripedAccountMap map = new StripedAccountMap(usd, 16);
        for (String id : ids) map.open(id);
        BenchmarkResult striped = Benchmark.measureMap(map, ids, threads, ops, 1, Stopwatch.system());
        System.out.println(line(striped, usd, ""));
        System.out.println("(ops/s are wall-clock on this machine and vary; the lost column is the lesson)");

        System.out.println("\n== Check-then-act: a debit that cannot overdraw ==");
        map.credit("acct-1", Money.ofMinor(500, usd));
        System.out.println("acct-1 balance " + map.balance("acct-1") + " on stripe " + map.stripeIndex("acct-1"));
        try {
            map.debit("acct-1", Money.ofMinor(1_000_000, usd));
        } catch (InsufficientFundsException e) {
            System.out.println("rejected: " + e.getMessage());
        }
        System.out.println("map total = " + map.total());

        System.out.println("\n== The same counter behind an Account ==");
        Account acct = new Account("acct-7", usd, new LockedCounter());
        acct.credit(Money.ofMinor(10_000, usd));
        acct.credit(Money.ofMinor(2_500, usd));
        acct.credit(Money.ofMinor(2_500, usd));
        acct.debit(Money.ofMinor(1_999, usd));
        System.out.println(acct.id() + " balance after 100.00 + 25.00 + 25.00 - 19.99 = " + acct.balance());
        try {
            acct.credit(Money.ofMinor(100, Currency.EUR));
        } catch (CurrencyMismatchException e) {
            System.out.println("rejected: " + e.getMessage());
        }

        System.out.println("\n== Deterministic replay with an injected stopwatch ==");
        BenchmarkResult replay = Benchmark.measure(new LockedCounter(), 4, 1_000, 1,
                new FakeStopwatch(1_000_000L, 3_000_000L));
        System.out.printf("%d threads x %,d ops in %.3f ms = %,.0f ops/s, lost %d%n",
                replay.threads(), replay.opsPerThread(), replay.elapsedNanos() / 1e6, replay.opsPerSecond(), replay.lost());
    }
}
