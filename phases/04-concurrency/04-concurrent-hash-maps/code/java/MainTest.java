// Tests for phases/04-concurrency/04-concurrent-hash-maps. Run: javac -d .out *.java && java -cp .out MainTest
import java.util.ArrayList;
import java.util.HashSet;
import java.util.List;
import java.util.Set;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.CyclicBarrier;
import java.util.concurrent.atomic.AtomicBoolean;
import java.util.concurrent.atomic.AtomicInteger;
import java.util.function.Supplier;

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

    public static void main(String[] args) throws Exception {
        hashing();
        for (String name : List.of("GlobalLockMap", "StripedHashMap", "LockFreeReadMap")) {
            singleThreadedContract(name, factory(name));
            concurrentWithBarriers(name, factory(name));
        }
        stripedSpecifics();
        lockFreeReadSpecifics();
        checkThenAct();
        readersNeverSeeTornState();

        System.out.println(passed + " passed, " + failures.size() + " failed");
        for (String f : failures) System.out.println("  FAIL: " + f);
        System.exit(failures.isEmpty() ? 0 : 1);
    }

    static Supplier<ConcurrentStore<String, Long>> factory(String name) {
        return switch (name) {
            case "GlobalLockMap" -> GlobalLockMap::new;
            case "StripedHashMap" -> () -> new StripedHashMap<>(16);
            default -> () -> new LockFreeReadMap<>(16, 64);
        };
    }

    static void hashing() {
        check("stableHash matches String.hashCode", Hashing.stableHash("acct-1") == "acct-1".hashCode());
        check("adjacent keys differ only in low bits before spreading", Hashing.stableHash("acct-2") - Hashing.stableHash("acct-1") == 1);
        Set<Integer> stripes = new HashSet<>();
        StripedHashMap<String, Long> map = new StripedHashMap<>(16);
        for (int i = 1; i <= 8; i++) stripes.add(map.segmentIndex("acct-" + i));
        check("spread scatters acct-1..8 across several stripes", stripes.size() >= 4);
        boolean inRange = true;
        for (int i = 0; i < 1000; i++) {
            int b = Hashing.bucketIndex("k" + i, 64);
            inRange &= b >= 0 && b < 64;
        }
        check("bucketIndex stays within the table", inRange);
        check("spread is deterministic", Hashing.spread(12345) == Hashing.spread(12345));
    }

    static void singleThreadedContract(String name, Supplier<ConcurrentStore<String, Long>> make) {
        ConcurrentStore<String, Long> m = make.get();
        check(name + ": empty get is null", m.get("a") == null && m.size() == 0);
        check(name + ": first put returns null", m.put("a", 1L) == null);
        check(name + ": get after put", m.get("a") == 1L && m.size() == 1);
        check(name + ": second put returns previous", m.put("a", 2L) == 1L && m.get("a") == 2L && m.size() == 1);
        check(name + ": putIfAbsent on present returns existing and keeps it", m.putIfAbsent("a", 9L) == 2L && m.get("a") == 2L);
        check(name + ": putIfAbsent on absent stores", m.putIfAbsent("b", 5L) == null && m.get("b") == 5L && m.size() == 2);
        check(name + ": compute increments", m.compute("a", (k, v) -> v + 10) == 12L && m.get("a") == 12L);
        check(name + ": compute on absent creates", m.compute("c", (k, v) -> v == null ? 1L : v + 1) == 1L && m.size() == 3);
        check(name + ": compute returning null removes", m.compute("c", (k, v) -> null) == null && m.get("c") == null && m.size() == 2);
        check(name + ": remove returns value", m.remove("b") == 5L && m.get("b") == null && m.size() == 1);
        check(name + ": remove absent is null", m.remove("zzz") == null);
        checkThrows(name + ": null key rejected", NullPointerException.class, () -> m.get(null));
        checkThrows(name + ": null value rejected", NullPointerException.class, () -> m.put("x", null));
        for (int i = 0; i < 500; i++) m.put("fill-" + i, (long) i);
        boolean allPresent = true;
        for (int i = 0; i < 500; i++) allPresent &= m.get("fill-" + i) == (long) i;
        check(name + ": 500 keys all retrievable after growth", allPresent && m.size() == 501);
    }

    /**
     * Deterministic concurrent test: every phase starts behind a barrier and ends with a latch, so the assertions
     * are about end states that every legal interleaving must produce, never about timing.
     */
    static void concurrentWithBarriers(String name, Supplier<ConcurrentStore<String, Long>> make) throws Exception {
        ConcurrentStore<String, Long> m = make.get();
        int threads = 4, perThread = 5_000, shared = 100, bumps = 1_000;
        CyclicBarrier barrier = new CyclicBarrier(threads);
        CountDownLatch done = new CountDownLatch(threads);
        AtomicInteger errors = new AtomicInteger();
        for (int t = 0; t < threads; t++) {
            final int id = t;
            new Thread(() -> {
                try {
                    barrier.await();                                                      // phase 1: disjoint inserts
                    for (int i = 0; i < perThread; i++) m.put("t" + id + "-" + i, (long) i);
                    barrier.await();                                                      // phase 2: everyone reads everyone's keys
                    for (int o = 0; o < threads; o++) {
                        for (int i = 0; i < perThread; i += 7) {
                            Long v = m.get("t" + o + "-" + i);
                            if (v == null || v != i) errors.incrementAndGet();
                        }
                    }
                    barrier.await();                                                      // phase 3: contended increments on shared keys
                    for (int round = 0; round < bumps; round++) {
                        for (int k = 0; k < shared; k++) m.compute("shared-" + k, (key, v) -> v == null ? 1L : v + 1);
                    }
                    barrier.await();                                                      // phase 4: each thread removes its own keys
                    for (int i = 0; i < perThread; i++) if (m.remove("t" + id + "-" + i) == null) errors.incrementAndGet();
                } catch (Exception e) {
                    errors.incrementAndGet();
                } finally {
                    done.countDown();
                }
            }).start();
        }
        done.await();
        check(name + ": no reader saw a missing or wrong value, no remove missed", errors.get() == 0);
        boolean sumsExact = true;
        for (int k = 0; k < shared; k++) sumsExact &= m.get("shared-" + k) == (long) threads * bumps;
        check(name + ": every shared key counted exactly threads x bumps", sumsExact);
        check(name + ": size after removals equals the shared keys", m.size() == shared);
    }

    static void stripedSpecifics() {
        StripedHashMap<String, Long> one = new StripedHashMap<>(1);
        for (int i = 0; i < 200; i++) one.put("k" + i, (long) i);
        check("single stripe resizes as it fills", one.totalResizes() >= 5);
        boolean ok = true;
        for (int i = 0; i < 200; i++) ok &= one.get("k" + i) == (long) i;
        check("entries survive segment resize", ok && one.size() == 200);
        checkThrows("stripe count must be a power of two", IllegalArgumentException.class, () -> new StripedHashMap<>(12));
        StripedHashMap<String, Long> sixteen = new StripedHashMap<>(16);
        boolean inRange = true;
        for (int i = 0; i < 1000; i++) { int s = sixteen.segmentIndex("acct-" + i); inRange &= s >= 0 && s < 16; }
        check("segmentIndex within [0, stripes)", inRange && sixteen.stripes() == 16);
    }

    static void lockFreeReadSpecifics() {
        LockFreeReadMap<String, Long> m = new LockFreeReadMap<>(1, 2);   // two buckets: every chain is long, every remove hits the middle
        for (int i = 0; i < 6; i++) m.put("k" + i, (long) i);
        check("grows past 75% load", m.buckets() > 2 && m.resizes() >= 1);
        m.resizeTo(64);
        check("explicit resize keeps every entry", m.buckets() == 64 && m.size() == 6 && m.get("k3") == 3L && m.get("k5") == 5L);
        m.resizeTo(8);
        check("resize to a smaller table is ignored", m.buckets() == 64);
        LockFreeReadMap<String, Long> chain = new LockFreeReadMap<>(1, 1);
        chain.put("a", 1L); chain.put("b", 2L); chain.put("c", 3L);      // all three share bucket 0 until growth; 3 > 0.75 so it grows to 2
        chain.resizeTo(1);                                                   // ignored, but harmless
        check("remove from a shared chain keeps the neighbours", chain.remove("b") == 2L && chain.get("a") == 1L && chain.get("c") == 3L && chain.get("b") == null);
        check("compute replaces in place within the chain", chain.compute("a", (k, v) -> v + 100) == 101L && chain.get("c") == 3L && chain.size() == 2);
        checkThrows("buckets must be >= stripes", IllegalArgumentException.class, () -> new LockFreeReadMap<>(16, 8));
    }

    static void checkThenAct() throws InterruptedException {
        check("get-then-put: all 8 threads create the record (striped)", Main.raceGetThenPut(new StripedHashMap<>(), 8) == 8);
        check("get-then-put: all 8 threads create the record (lock-free reads)", Main.raceGetThenPut(new LockFreeReadMap<>(), 8) == 8);
        check("putIfAbsent: exactly one creator (striped)", Main.racePutIfAbsent(new StripedHashMap<>(), 8) == 1);
        check("putIfAbsent: exactly one creator (lock-free reads)", Main.racePutIfAbsent(new LockFreeReadMap<>(), 8) == 1);
        check("putIfAbsent: exactly one creator (global lock)", Main.racePutIfAbsent(new GlobalLockMap<>(), 8) == 1);
    }

    /** Readers run without locks while writers rebuild chains and resize; a reader must see every stable key every time. */
    static void readersNeverSeeTornState() throws InterruptedException {
        LockFreeReadMap<String, Long> m = new LockFreeReadMap<>(4, 4);
        int stableKeys = 64;
        for (int i = 0; i < stableKeys; i++) m.put("stable-" + i, (long) i);
        AtomicBoolean stop = new AtomicBoolean();
        AtomicInteger anomalies = new AtomicInteger();
        AtomicInteger reads = new AtomicInteger();
        Thread[] readers = new Thread[2];
        for (int r = 0; r < readers.length; r++) {
            readers[r] = new Thread(() -> {
                while (!stop.get()) {
                    for (int i = 0; i < stableKeys; i++) {
                        Long v = m.get("stable-" + i);
                        if (v == null || v != i) anomalies.incrementAndGet();
                        reads.incrementAndGet();
                    }
                }
            });
            readers[r].start();
        }
        Thread[] writers = new Thread[2];
        for (int w = 0; w < writers.length; w++) {
            final int id = w;
            writers[w] = new Thread(() -> {
                for (int i = 0; i < 20_000; i++) {
                    String k = "churn-" + id + "-" + (i % 500);
                    if (i % 3 == 2) m.remove(k); else m.compute(k, (key, v) -> v == null ? 1L : v + 1);
                }
            });
            writers[w].start();
        }
        for (Thread w : writers) w.join();
        stop.set(true);
        for (Thread r : readers) r.join();
        check("lock-free readers never saw a stable key missing or wrong", anomalies.get() == 0);
        check("readers made progress during writes", reads.get() > stableKeys);
        check("table grew while readers were active", m.buckets() > 4);
        boolean stableIntact = true;
        for (int i = 0; i < stableKeys; i++) stableIntact &= m.get("stable-" + i) == (long) i;
        check("stable keys intact after churn", stableIntact);
    }
}
