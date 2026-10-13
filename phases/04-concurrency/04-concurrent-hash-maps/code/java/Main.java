// Lesson: phases/04-concurrency/04-concurrent-hash-maps/docs/en.md
// Concurrent Hash Maps: Striping and Lock-Free Reads
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: JLS 17.4 (Java Memory Model, volatile and final fields); java.util.concurrent.ConcurrentHashMap Javadoc
// and source comments (Doug Lea); Herlihy & Shavit, The Art of Multiprocessor Programming, ch. 13 (concurrent hashing).
// Java 21, standard library only.

import java.util.ArrayList;
import java.util.List;
import java.util.Objects;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.CyclicBarrier;
import java.util.concurrent.atomic.AtomicInteger;
import java.util.concurrent.atomic.AtomicReferenceArray;
import java.util.concurrent.locks.ReentrantLock;
import java.util.function.BiFunction;

/** The operations every map in this lesson supports; the same names as java.util.concurrent.ConcurrentMap. */
interface ConcurrentStore<K, V> {
    V get(K key);
    /** Returns the previous value or null. */
    V put(K key, V value);
    /** Atomic check-then-act: stores only if absent; returns the existing value, or null if this call stored. */
    V putIfAbsent(K key, V value);
    V remove(K key);
    /** Atomic read-modify-write for one key; fn receives the current value (or null) and returns the new one (null removes). */
    V compute(K key, BiFunction<? super K, ? super V, ? extends V> fn);
    int size();
}

/** Hashing shared by every implementation so the same key lands in the same bucket in every language port. */
final class Hashing {
    private Hashing() {}

    /** Java's String.hashCode written out; stable across processes and languages (Python's hash() is salted). */
    static int stableHash(Object key) {
        if (key instanceof String s) {
            int h = 0;
            for (int i = 0; i < s.length(); i++) h = 31 * h + s.charAt(i);
            return h;
        }
        return key.hashCode();
    }

    /**
     * Mix every input bit into every output bit (the finaliser of MurmurHash3). String.hashCode on short keys that
     * differ in one character differs only in the low bits; without this step every "acct-N" would share one stripe.
     * ConcurrentHashMap in Java 7 used a Wang/Jenkins hash for the same reason; Java 8 uses the cheaper h ^ (h >>> 16)
     * because it only selects bins and relies on treeified bins to absorb bad distributions.
     */
    static int spread(int h) {
        h ^= h >>> 16;
        h *= 0x85ebca6b;
        h ^= h >>> 13;
        h *= 0xc2b2ae35;
        h ^= h >>> 16;
        return h;
    }

    static int bucketIndex(Object key, int tableLength) { return spread(stableHash(key)) & (tableLength - 1); }
}

/** Baseline: one HashMap behind one lock. Correct, simple, and the throughput ceiling the other two beat. */
final class GlobalLockMap<K, V> implements ConcurrentStore<K, V> {
    private final java.util.HashMap<K, V> map = new java.util.HashMap<>();
    private final ReentrantLock lock = new ReentrantLock();

    private <R> R locked(java.util.function.Supplier<R> body) {
        lock.lock();
        try { return body.get(); } finally { lock.unlock(); }
    }

    private static <T> T nonNull(T x, String what) { return Objects.requireNonNull(x, what); }

    @Override public V get(K key) { nonNull(key, "key"); return locked(() -> map.get(key)); }
    @Override public V put(K key, V value) { nonNull(key, "key"); nonNull(value, "value"); return locked(() -> map.put(key, value)); }
    @Override public V putIfAbsent(K key, V value) { nonNull(key, "key"); nonNull(value, "value"); return locked(() -> map.putIfAbsent(key, value)); }
    @Override public V remove(K key) { nonNull(key, "key"); return locked(() -> map.remove(key)); }
    @Override public V compute(K key, BiFunction<? super K, ? super V, ? extends V> fn) { nonNull(key, "key"); return locked(() -> map.compute(key, fn)); }
    @Override public int size() { return locked(map::size); }
}

/**
 * Lock striping: the key space is split into N independent segments, each with its own lock, bucket table,
 * count, and resize. Two operations contend only when their keys hash to the same segment. Reads take the
 * segment lock too, which is the limitation LockFreeReadMap removes.
 */
final class StripedHashMap<K, V> implements ConcurrentStore<K, V> {
    static final class Node<K, V> {
        final K key;
        V value;
        Node<K, V> next;
        Node(K key, V value, Node<K, V> next) { this.key = key; this.value = value; this.next = next; }
    }

    /** One stripe: a tiny single-threaded hash map guarded by its own lock. */
    static final class Segment<K, V> {
        final ReentrantLock lock = new ReentrantLock();
        Node<K, V>[] table;
        int count;
        int resizes;

        @SuppressWarnings("unchecked")
        Segment(int initialBuckets) { table = (Node<K, V>[]) new Node[initialBuckets]; }

        Node<K, V> find(K key) {
            for (Node<K, V> n = table[Hashing.bucketIndex(key, table.length)]; n != null; n = n.next) {
                if (n.key.equals(key)) return n;
            }
            return null;
        }

        void insert(K key, V value) {
            if (count + 1 > table.length * 3 / 4) grow();
            int i = Hashing.bucketIndex(key, table.length);
            table[i] = new Node<>(key, value, table[i]);
            count++;
        }

        V unlink(K key) {
            int i = Hashing.bucketIndex(key, table.length);
            Node<K, V> prev = null;
            for (Node<K, V> n = table[i]; n != null; prev = n, n = n.next) {
                if (n.key.equals(key)) {
                    if (prev == null) table[i] = n.next; else prev.next = n.next;
                    count--;
                    return n.value;
                }
            }
            return null;
        }

        @SuppressWarnings("unchecked")
        void grow() {
            Node<K, V>[] old = table;
            Node<K, V>[] fresh = (Node<K, V>[]) new Node[old.length * 2];
            for (Node<K, V> head : old) {
                for (Node<K, V> n = head; n != null; n = n.next) {
                    int i = Hashing.bucketIndex(n.key, fresh.length);
                    fresh[i] = new Node<>(n.key, n.value, fresh[i]);
                }
            }
            table = fresh;
            resizes++;
        }
    }

    private final Segment<K, V>[] segments;
    private final int segmentShift;

    @SuppressWarnings("unchecked")
    StripedHashMap(int stripes) {
        if (Integer.bitCount(stripes) != 1) throw new IllegalArgumentException("stripes must be a power of two: " + stripes);
        segments = (Segment<K, V>[]) new Segment[stripes];
        for (int i = 0; i < stripes; i++) segments[i] = new Segment<>(4);
        segmentShift = 32 - Integer.numberOfTrailingZeros(stripes);
    }

    StripedHashMap() { this(16); }

    /** Segment is chosen by the HIGH bits of the spread hash; the bucket inside it by the low bits. They must not reuse the same bits. */
    int segmentIndex(K key) {
        return segments.length == 1 ? 0 : Hashing.spread(Hashing.stableHash(key)) >>> segmentShift;
    }

    private Segment<K, V> segmentFor(K key) { return segments[segmentIndex(Objects.requireNonNull(key, "key"))]; }

    @Override
    public V get(K key) {
        Segment<K, V> s = segmentFor(key);
        s.lock.lock();
        try {
            Node<K, V> n = s.find(key);
            return n == null ? null : n.value;
        } finally { s.lock.unlock(); }
    }

    @Override
    public V put(K key, V value) {
        Objects.requireNonNull(value, "value");
        Segment<K, V> s = segmentFor(key);
        s.lock.lock();
        try {
            Node<K, V> n = s.find(key);
            if (n != null) { V old = n.value; n.value = value; return old; }
            s.insert(key, value);
            return null;
        } finally { s.lock.unlock(); }
    }

    @Override
    public V putIfAbsent(K key, V value) {
        Objects.requireNonNull(value, "value");
        Segment<K, V> s = segmentFor(key);
        s.lock.lock();
        try {
            Node<K, V> n = s.find(key);          // check ...
            if (n != null) return n.value;
            s.insert(key, value);                // ... and act, under the same lock hold
            return null;
        } finally { s.lock.unlock(); }
    }

    @Override
    public V remove(K key) {
        Segment<K, V> s = segmentFor(key);
        s.lock.lock();
        try { return s.unlink(key); } finally { s.lock.unlock(); }
    }

    @Override
    public V compute(K key, BiFunction<? super K, ? super V, ? extends V> fn) {
        Segment<K, V> s = segmentFor(key);
        s.lock.lock();
        try {
            Node<K, V> n = s.find(key);
            V next = fn.apply(key, n == null ? null : n.value);
            if (next == null) { if (n != null) s.unlink(key); return null; }
            if (n != null) n.value = next; else s.insert(key, next);
            return next;
        } finally { s.lock.unlock(); }
    }

    /** Sums per-segment counts under all locks, taken in index order so two concurrent size() calls cannot deadlock. */
    @Override
    public int size() {
        for (Segment<K, V> s : segments) s.lock.lock();
        try {
            int total = 0;
            for (Segment<K, V> s : segments) total += s.count;
            return total;
        } finally {
            for (int i = segments.length - 1; i >= 0; i--) segments[i].lock.unlock();
        }
    }

    int stripes() { return segments.length; }
    int totalResizes() { int r = 0; for (Segment<K, V> s : segments) r += s.resizes; return r; }
}

/**
 * Lock striping for writers, no locks for readers. Each bucket holds an immutable chain of nodes (all fields final).
 * A writer, holding the stripe lock, builds a new chain and publishes it with a volatile store into an
 * AtomicReferenceArray; a reader does one volatile load of the table and one of the bucket and then walks final fields.
 * The reader sees either the chain before the write or the chain after, never a half-built one.
 */
final class LockFreeReadMap<K, V> implements ConcurrentStore<K, V> {
    static final class Node<K, V> {
        final K key;
        final V value;
        final Node<K, V> next;
        Node(K key, V value, Node<K, V> next) { this.key = key; this.value = value; this.next = next; }
    }

    private volatile AtomicReferenceArray<Node<K, V>> table;
    private final ReentrantLock[] stripeLocks;
    private final AtomicInteger count = new AtomicInteger();   // like ConcurrentHashMap, size() is a weakly consistent snapshot
    private volatile int resizes;

    LockFreeReadMap(int stripes, int initialBuckets) {
        if (Integer.bitCount(stripes) != 1 || Integer.bitCount(initialBuckets) != 1 || initialBuckets < stripes) {
            throw new IllegalArgumentException("stripes and buckets must be powers of two with buckets >= stripes");
        }
        table = new AtomicReferenceArray<>(initialBuckets);
        stripeLocks = new ReentrantLock[stripes];
        for (int i = 0; i < stripes; i++) stripeLocks[i] = new ReentrantLock();
    }

    LockFreeReadMap() { this(16, 64); }

    /** Stripe is bucketIndex mod stripes. Because both are powers of two, a bucket always maps to the same stripe across resizes. */
    private int stripeFor(K key) {
        return Hashing.spread(Hashing.stableHash(key)) & (stripeLocks.length - 1);
    }

    @Override
    public V get(K key) {
        Objects.requireNonNull(key, "key");
        AtomicReferenceArray<Node<K, V>> t = table;                       // one volatile read
        Node<K, V> n = t.get(Hashing.bucketIndex(key, t.length()));       // one volatile read
        for (; n != null; n = n.next) {                                   // final fields: no further synchronization
            if (n.key.equals(key)) return n.value;
        }
        return null;
    }

    /** Rebuild the chain without {@code key}; returns the removed node's value through {@code removed[0]}. */
    private Node<K, V> without(Node<K, V> head, K key, Object[] removed) {
        if (head == null) return null;
        if (head.key.equals(key)) { removed[0] = head.value; return head.next; }
        Node<K, V> rest = without(head.next, key, removed);
        return removed[0] == null && rest == head.next ? head : new Node<>(head.key, head.value, rest);
    }

    @Override
    public V put(K key, V value) {
        Objects.requireNonNull(value, "value");
        int s = stripeFor(key);
        boolean inserted;
        V old;
        stripeLocks[s].lock();
        try {
            AtomicReferenceArray<Node<K, V>> t = table;
            int i = Hashing.bucketIndex(key, t.length());
            Object[] removed = new Object[1];
            Node<K, V> rest = without(t.get(i), key, removed);
            @SuppressWarnings("unchecked") V previous = (V) removed[0];
            old = previous;
            t.set(i, new Node<>(key, value, rest));                      // volatile store publishes the new chain
            inserted = old == null;
            if (inserted) count.incrementAndGet();
        } finally { stripeLocks[s].unlock(); }
        if (inserted) growIfCrowded();
        return old;
    }

    @Override
    public V putIfAbsent(K key, V value) {
        Objects.requireNonNull(value, "value");
        int s = stripeFor(key);
        stripeLocks[s].lock();
        try {
            AtomicReferenceArray<Node<K, V>> t = table;
            int i = Hashing.bucketIndex(key, t.length());
            Node<K, V> head = t.get(i);
            for (Node<K, V> n = head; n != null; n = n.next) if (n.key.equals(key)) return n.value;   // check ...
            t.set(i, new Node<>(key, value, head));                                                  // ... and act, same lock hold
            count.incrementAndGet();
        } finally { stripeLocks[s].unlock(); }
        growIfCrowded();
        return null;
    }

    @Override
    public V remove(K key) {
        int s = stripeFor(key);
        stripeLocks[s].lock();
        try {
            AtomicReferenceArray<Node<K, V>> t = table;
            int i = Hashing.bucketIndex(key, t.length());
            Object[] removed = new Object[1];
            Node<K, V> rebuilt = without(t.get(i), key, removed);
            if (removed[0] == null) return null;
            t.set(i, rebuilt);
            count.decrementAndGet();
            @SuppressWarnings("unchecked") V old = (V) removed[0];
            return old;
        } finally { stripeLocks[s].unlock(); }
    }

    @Override
    public V compute(K key, BiFunction<? super K, ? super V, ? extends V> fn) {
        int s = stripeFor(key);
        boolean inserted = false;
        V result;
        stripeLocks[s].lock();
        try {
            AtomicReferenceArray<Node<K, V>> t = table;
            int i = Hashing.bucketIndex(key, t.length());
            Object[] removed = new Object[1];
            Node<K, V> rest = without(t.get(i), key, removed);
            @SuppressWarnings("unchecked") V old = (V) removed[0];
            V next = fn.apply(key, old);
            if (next == null) {
                if (old != null) { t.set(i, rest); count.decrementAndGet(); }
                result = null;
            } else {
                t.set(i, new Node<>(key, next, rest));                   // updated node in front of the untouched rest
                if (old == null) { count.incrementAndGet(); inserted = true; }
                result = next;
            }
        } finally { stripeLocks[s].unlock(); }
        if (inserted) growIfCrowded();
        return result;
    }

    /**
     * Called with NO lock held: a writer must not hold one stripe and then take all of them, because that inverts
     * the index order resizeTo() uses and two writers could deadlock. Grows past 75% load; a stale read of the
     * count or the table length is harmless because resizeTo re-checks under all locks.
     */
    private void growIfCrowded() {
        AtomicReferenceArray<Node<K, V>> t = table;
        if (count.get() > t.length() * 3 / 4) resizeTo(t.length() * 2);
    }

    /** Lock-free and weakly consistent: an insert that has published its node may not yet have counted itself. */
    @Override
    public int size() { return count.get(); }

    int buckets() { return table.length(); }
    int resizes() { return resizes; }

    /**
     * Resize takes every stripe lock (no writer is mid-flight), builds a fresh table, and publishes it with one
     * volatile store. Readers that loaded the old table keep walking it; it is frozen because every write
     * goes through a lock this method holds.
     */
    void resizeTo(int newBuckets) {
        if (Integer.bitCount(newBuckets) != 1 || newBuckets < stripeLocks.length) throw new IllegalArgumentException("bad bucket count");
        for (ReentrantLock l : stripeLocks) l.lock();
        try {
            AtomicReferenceArray<Node<K, V>> old = table;
            if (newBuckets <= old.length()) return;                      // another thread grew first
            AtomicReferenceArray<Node<K, V>> fresh = new AtomicReferenceArray<>(newBuckets);
            for (int i = 0; i < old.length(); i++) {
                for (Node<K, V> n = old.get(i); n != null; n = n.next) {
                    int j = Hashing.bucketIndex(n.key, newBuckets);
                    fresh.set(j, new Node<>(n.key, n.value, fresh.get(j)));
                }
            }
            table = fresh;
            resizes++;
        } finally {
            for (int i = stripeLocks.length - 1; i >= 0; i--) stripeLocks[i].unlock();
        }
    }

}

/** What the map stores in the demo: the record an idempotent payment endpoint keeps per key. */
record IdempotencyRecord(String paymentId, long amountMinor, String status) {}

public final class Main {

    /** Runs the same workload on any store: {@code threads} workers, each {@code ops} operations, 95% reads. Returns ops/ms. */
    static long benchmark(ConcurrentStore<String, Long> store, int threads, int ops, int keySpace) throws InterruptedException {
        String[] keyTable = new String[keySpace];
        for (int k = 0; k < keySpace; k++) { keyTable[k] = "key-" + k; store.put(keyTable[k], 0L); }
        CountDownLatch start = new CountDownLatch(1), done = new CountDownLatch(threads);
        for (int t = 0; t < threads; t++) {
            final int seed = t;
            new Thread(() -> {
                try { start.await(); } catch (InterruptedException e) { return; }
                long x = 0x9E3779B97F4A7C15L ^ seed;                      // per-thread deterministic PRNG, no shared state
                for (int i = 0; i < ops; i++) {
                    x ^= x << 13; x ^= x >>> 7; x ^= x << 17;
                    String key = keyTable[(int) Long.remainderUnsigned(x, keySpace)];
                    if (Long.remainderUnsigned(x >>> 32, 100) < 95) store.get(key);
                    else store.compute(key, (k, v) -> v == null ? 1L : v + 1);
                }
                done.countDown();
            }).start();
        }
        long t0 = System.nanoTime();
        start.countDown();
        done.await();
        long ms = Math.max(1, (System.nanoTime() - t0) / 1_000_000);
        return (long) threads * ops / ms;
    }

    /**
     * Forces the check-then-act race with a barrier placed in the gap: every thread reads "absent", then all wait
     * for each other, then all write. Returns how many threads believed they created the record.
     */
    static int raceGetThenPut(ConcurrentStore<String, IdempotencyRecord> store, int threads) throws InterruptedException {
        CyclicBarrier inTheGap = new CyclicBarrier(threads);
        CountDownLatch done = new CountDownLatch(threads);
        int[] winners = new int[threads];
        for (int t = 0; t < threads; t++) {
            final int id = t;
            new Thread(() -> {
                try {
                    IdempotencyRecord existing = store.get("idem-7");                 // check
                    inTheGap.await();                                                  // everyone has checked, nobody has acted
                    if (existing == null) {
                        store.put("idem-7", new IdempotencyRecord("pay_" + id, 4_999, "CAPTURED"));   // act
                        winners[id] = 1;
                    }
                } catch (Exception e) {
                    throw new RuntimeException(e);
                } finally { done.countDown(); }
            }).start();
        }
        done.await();
        int sum = 0;
        for (int w : winners) sum += w;
        return sum;
    }

    /** Same shape, but the check and the act are one putIfAbsent. */
    static int racePutIfAbsent(ConcurrentStore<String, IdempotencyRecord> store, int threads) throws InterruptedException {
        CyclicBarrier start = new CyclicBarrier(threads);
        CountDownLatch done = new CountDownLatch(threads);
        int[] winners = new int[threads];
        for (int t = 0; t < threads; t++) {
            final int id = t;
            new Thread(() -> {
                try {
                    start.await();
                    if (store.putIfAbsent("idem-7", new IdempotencyRecord("pay_" + id, 4_999, "CAPTURED")) == null) winners[id] = 1;
                } catch (Exception e) {
                    throw new RuntimeException(e);
                } finally { done.countDown(); }
            }).start();
        }
        done.await();
        int sum = 0;
        for (int w : winners) sum += w;
        return sum;
    }

    static void demoStripes() {
        System.out.println("== Where keys land: 16 stripes, top 4 bits of the mixed hash ==");
        StripedHashMap<String, Long> map = new StripedHashMap<>(16);
        List<String> placements = new ArrayList<>();
        for (int i = 1; i <= 8; i++) placements.add("acct-" + i + "->s" + map.segmentIndex("acct-" + i));
        System.out.println(String.join("  ", placements));
        System.out.println("stableHash(\"acct-1\") = " + Hashing.stableHash("acct-1") + ", stableHash(\"acct-2\") = " + Hashing.stableHash("acct-2")
                + " (differ by 1: all low bits); after spread: " + Hashing.spread(Hashing.stableHash("acct-1")) + " and " + Hashing.spread(Hashing.stableHash("acct-2")));
        System.out.println();
    }

    static void demoLockFreeRead() {
        System.out.println("== Lock-free reads: a writer replaces the chain, a reader sees old or new, never half ==");
        LockFreeReadMap<String, IdempotencyRecord> map = new LockFreeReadMap<>(4, 4);     // tiny on purpose: growth is visible
        map.put("idem-1", new IdempotencyRecord("pay_001", 1_999, "CAPTURED"));
        map.put("idem-2", new IdempotencyRecord("pay_002", 2_500, "CAPTURED"));
        map.put("idem-3", new IdempotencyRecord("pay_003", 4_999, "PENDING"));
        System.out.println("3 puts into a 4-bucket table -> buckets = " + map.buckets() + ", size = " + map.size() + " (load 3/4: not yet)");
        map.put("idem-4", new IdempotencyRecord("pay_004", 100, "PENDING"));
        System.out.println("4th put -> buckets = " + map.buckets() + ", resizes = " + map.resizes() + " (load crossed 75%)");
        System.out.println("get idem-3 -> " + map.get("idem-3"));
        map.compute("idem-3", (k, r) -> new IdempotencyRecord(r.paymentId(), r.amountMinor(), "CAPTURED"));
        System.out.println("after compute: get idem-3 -> " + map.get("idem-3"));
        System.out.println("remove idem-2 -> " + map.remove("idem-2") + "; get idem-2 -> " + map.get("idem-2"));
        map.resizeTo(32);
        System.out.println("explicit resize to " + map.buckets() + " buckets; get idem-1 -> " + map.get("idem-1") + "; size = " + map.size());
        System.out.println();
    }

    public static void main(String[] args) throws InterruptedException {
        demoStripes();
        demoLockFreeRead();

        System.out.println("== Check-then-act, forced with a barrier in the gap (8 threads, one idempotency key) ==");
        System.out.println("get-then-put on StripedHashMap:   " + raceGetThenPut(new StripedHashMap<>(), 8) + " threads believe they created the record");
        System.out.println("putIfAbsent on StripedHashMap:    " + racePutIfAbsent(new StripedHashMap<>(), 8) + " thread created it");
        System.out.println("get-then-put on LockFreeReadMap:  " + raceGetThenPut(new LockFreeReadMap<>(), 8) + " threads believe they created the record");
        System.out.println("putIfAbsent on LockFreeReadMap:   " + racePutIfAbsent(new LockFreeReadMap<>(), 8) + " thread created it");
        System.out.println("A thread-safe map does not make your check-then-act atomic. Only a per-key atomic operation does.");
        System.out.println();

        System.out.println("== Throughput, 4 threads x 100,000 ops, 95% get / 5% compute, 1,024 keys (ops per ms; varies by machine) ==");
        int threads = 4, ops = 100_000, keys = 1024;
        long global = benchmark(new GlobalLockMap<>(), threads, ops, keys);
        long striped = benchmark(new StripedHashMap<>(16), threads, ops, keys);
        long lockFree = benchmark(new LockFreeReadMap<>(16, 2048), threads, ops, keys);
        System.out.println(String.format("%-18s %8d", "GlobalLockMap", global));
        System.out.println(String.format("%-18s %8d", "StripedHashMap", striped));
        System.out.println(String.format("%-18s %8d", "LockFreeReadMap", lockFree));
        System.out.println("(Wall-clock numbers vary. With 4+ cores the ordering is global < striped < lock-free reads; with 1-2 cores"
                + " they converge because there is no parallelism left to win. The ratio, not the number, is the lesson.)");
    }
}
