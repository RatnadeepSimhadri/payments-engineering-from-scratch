// Lesson: phases/03-systems-data-structures/08-lru-and-lfu-caches/docs/en.md
// LRU and LFU Caches from Scratch
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: Shah, Mitra, Matani (2010), "An O(1) algorithm for implementing the LFU cache eviction scheme";
//          Denning (1968), "The Working Set Model for Program Behavior"; RFC 9111 (HTTP Caching) for freshness lifetime.
// Java 21, standard library only.

import java.util.ArrayList;
import java.util.HashMap;
import java.util.List;
import java.util.Map;

/** Something that tells the time in milliseconds. Injected so tests never sleep. */
interface Clock {
    long nowMillis();
}

final class ManualClock implements Clock {
    private long now;

    ManualClock() { this(0); }

    ManualClock(long startMillis) { this.now = startMillis; }

    @Override
    public long nowMillis() { return now; }

    void advance(long millis) {
        if (millis < 0) throw new IllegalArgumentException("clock cannot go backwards");
        now += millis;
    }
}

/** Minor units + currency, the Phase 10 Lesson 01 discipline, reduced to what this lesson needs. */
record Money(long minorUnits, String currency) {
    Money plus(Money other) {
        requireSame(other);
        return new Money(Math.addExact(minorUnits, other.minorUnits), currency);
    }

    Money minus(Money other) {
        requireSame(other);
        return new Money(Math.subtractExact(minorUnits, other.minorUnits), currency);
    }

    private void requireSame(Money other) {
        if (!currency.equals(other.currency)) {
            throw new IllegalArgumentException("cannot combine " + currency + " with " + other.currency);
        }
    }

    @Override
    public String toString() {
        long mag = Math.abs(minorUnits);
        return (minorUnits < 0 ? "-" : "") + (mag / 100) + "." + String.format("%02d", mag % 100) + " " + currency;
    }
}

/**
 * Least-recently-used cache with O(1) get, put, and remove. Entries may carry a TTL; an expired
 * entry is a miss and is removed on access. The list runs most-recent (head) to least-recent (tail).
 */
final class LruCache<K, V> {
    static final long NO_EXPIRY = Long.MAX_VALUE;

    private static final class Node<K, V> {
        final K key;
        V value;
        long expiresAt;
        Node<K, V> prev, next;

        Node(K key, V value, long expiresAt) {
            this.key = key;
            this.value = value;
            this.expiresAt = expiresAt;
        }
    }

    final int capacity;
    private final Clock clock;
    private final Map<K, Node<K, V>> map = new HashMap<>();
    private final Node<K, V> head = new Node<>(null, null, 0);   // sentinel: most recent follows head
    private final Node<K, V> tail = new Node<>(null, null, 0);   // sentinel: least recent precedes tail
    long hits, misses, evictions, expirations;

    LruCache(int capacity) { this(capacity, new ManualClock()); }

    LruCache(int capacity, Clock clock) {
        if (capacity <= 0) throw new IllegalArgumentException("capacity must be positive: " + capacity);
        this.capacity = capacity;
        this.clock = clock;
        head.next = tail;
        tail.prev = head;
    }

    // list surgery: every operation is a constant number of pointer writes
    private void unlink(Node<K, V> node) {
        node.prev.next = node.next;
        node.next.prev = node.prev;
    }

    private void pushFront(Node<K, V> node) {
        node.next = head.next;
        node.prev = head;
        head.next.prev = node;
        head.next = node;
    }

    private boolean expired(Node<K, V> node) { return node.expiresAt <= clock.nowMillis(); }

    /** The value, or null on a miss. A hit moves the entry to the front. */
    V get(K key) {
        Node<K, V> node = map.get(key);
        if (node == null) {
            misses++;
            return null;
        }
        if (expired(node)) {
            unlink(node);
            map.remove(key);
            expirations++;
            misses++;
            return null;
        }
        unlink(node);
        pushFront(node);
        hits++;
        return node.value;
    }

    void put(K key, V value) { putInternal(key, value, NO_EXPIRY); }

    void put(K key, V value, long ttlMillis) {
        if (ttlMillis <= 0) throw new IllegalArgumentException("ttlMillis must be positive");
        putInternal(key, value, clock.nowMillis() + ttlMillis);
    }

    private void putInternal(K key, V value, long expiresAt) {
        Node<K, V> node = map.get(key);
        if (node != null) {
            node.value = value;
            node.expiresAt = expiresAt;
            unlink(node);
            pushFront(node);
            return;
        }
        if (map.size() >= capacity) {
            Node<K, V> victim = tail.prev;
            unlink(victim);
            map.remove(victim.key);
            evictions++;
        }
        node = new Node<>(key, value, expiresAt);
        map.put(key, node);
        pushFront(node);
    }

    boolean remove(K key) {
        Node<K, V> node = map.remove(key);
        if (node == null) return false;
        unlink(node);
        return true;
    }

    int size() { return map.size(); }

    /** Most recent first. O(n); for tests and debugging. */
    List<K> keys() {
        List<K> out = new ArrayList<>();
        for (Node<K, V> n = head.next; n != tail; n = n.next) out.add(n.key);
        return out;
    }

    double hitRate() {
        long total = hits + misses;
        return total == 0 ? 0.0 : (double) hits / total;
    }
}

/**
 * Least-frequently-used cache with O(1) get and put (Shah, Mitra, Matani 2010).
 * Nodes live in a bucket per frequency; minFreq tracks the lowest non-empty bucket. Ties within a
 * bucket are broken least-recently-used first.
 */
final class LfuCache<K, V> {
    private static final class Node<K, V> {
        final K key;
        V value;
        int freq = 1;
        Node<K, V> prev, next;

        Node(K key, V value) {
            this.key = key;
            this.value = value;
        }
    }

    /** A doubly linked list of nodes that share one frequency; most recent at the head. */
    private static final class Bucket<K, V> {
        final Node<K, V> head = new Node<>(null, null);
        final Node<K, V> tail = new Node<>(null, null);
        int size;

        Bucket() {
            head.next = tail;
            tail.prev = head;
        }

        void pushFront(Node<K, V> node) {
            node.next = head.next;
            node.prev = head;
            head.next.prev = node;
            head.next = node;
            size++;
        }

        void unlink(Node<K, V> node) {
            node.prev.next = node.next;
            node.next.prev = node.prev;
            size--;
        }

        Node<K, V> leastRecent() { return tail.prev; }
    }

    final int capacity;
    private final Map<K, Node<K, V>> map = new HashMap<>();
    private final Map<Integer, Bucket<K, V>> buckets = new HashMap<>();
    private int minFreq = 0;
    long hits, misses, evictions;

    LfuCache(int capacity) {
        if (capacity <= 0) throw new IllegalArgumentException("capacity must be positive: " + capacity);
        this.capacity = capacity;
    }

    /** Move node from bucket f to bucket f+1, maintaining minFreq. Constant time. */
    private void touch(Node<K, V> node) {
        Bucket<K, V> bucket = buckets.get(node.freq);
        bucket.unlink(node);
        if (bucket.size == 0) {
            buckets.remove(node.freq);
            if (minFreq == node.freq) minFreq = node.freq + 1;
        }
        node.freq++;
        buckets.computeIfAbsent(node.freq, f -> new Bucket<>()).pushFront(node);
    }

    V get(K key) {
        Node<K, V> node = map.get(key);
        if (node == null) {
            misses++;
            return null;
        }
        touch(node);
        hits++;
        return node.value;
    }

    void put(K key, V value) {
        Node<K, V> node = map.get(key);
        if (node != null) {
            node.value = value;
            touch(node);
            return;
        }
        if (map.size() >= capacity) {
            Bucket<K, V> bucket = buckets.get(minFreq);
            Node<K, V> victim = bucket.leastRecent();
            bucket.unlink(victim);
            if (bucket.size == 0) buckets.remove(minFreq);
            map.remove(victim.key);
            evictions++;
        }
        node = new Node<>(key, value);
        map.put(key, node);
        buckets.computeIfAbsent(1, f -> new Bucket<>()).pushFront(node);
        minFreq = 1;
    }

    boolean remove(K key) {
        Node<K, V> node = map.remove(key);
        if (node == null) return false;
        Bucket<K, V> bucket = buckets.get(node.freq);
        bucket.unlink(node);
        if (bucket.size == 0) {
            buckets.remove(node.freq);
            if (minFreq == node.freq) {
                // the next non-empty bucket; O(distinct frequencies), only on explicit remove
                minFreq = buckets.isEmpty() ? 0 : buckets.keySet().stream().min(Integer::compare).get();
            }
        }
        return true;
    }

    int frequency(K key) {
        Node<K, V> node = map.get(key);
        return node == null ? 0 : node.freq;
    }

    int minFrequency() { return minFreq; }

    int size() { return map.size(); }

    double hitRate() {
        long total = hits + misses;
        return total == 0 ? 0.0 : (double) hits / total;
    }
}

/** splitmix64: identical sequences in Java, Python, and Go for a given seed. */
final class Rng {
    private long state;

    Rng(long seed) { this.state = seed; }

    long next() {
        state += 0x9E3779B97F4A7C15L;
        long z = state;
        z = (z ^ (z >>> 30)) * 0xBF58476D1CE4E5B9L;
        z = (z ^ (z >>> 27)) * 0x94D049BB133111EBL;
        return z ^ (z >>> 31);
    }

    int nextInt(int bound) { return (int) ((next() >>> 33) % bound); }
}

/** Deterministic lookup traces and a replay harness shared by the demo and the tests. */
final class Trace {
    private Trace() {}

    /** hotPercent of lookups hit `hotCount` BINs starting at hotOffset; the rest spread over cold BINs. */
    static List<String> binTrace(long seed, int ops, int hotOffset) {
        return binTrace(seed, ops, hotOffset, 20, 80, 2000);
    }

    static List<String> binTrace(long seed, int ops, int hotOffset, int hotCount, int hotPercent, int coldCount) {
        Rng rng = new Rng(seed);
        List<String> keys = new ArrayList<>(ops);
        for (int i = 0; i < ops; i++) {
            if (rng.nextInt(100) < hotPercent) keys.add("bin-" + (hotOffset + rng.nextInt(hotCount)));
            else keys.add("bin-" + (2 * hotCount + rng.nextInt(coldCount)));
        }
        return keys;
    }

    static List<String> scan(int count) {
        List<String> keys = new ArrayList<>(count);
        for (int i = 0; i < count; i++) keys.add("scan-" + i);
        return keys;
    }

    /** Look each key up; on a miss, "load" it and insert. Returns the hit rate over this batch only. */
    static double replay(LruCache<String, String> cache, List<String> keys) {
        int hits = 0;
        for (String key : keys) {
            if (cache.get(key) != null) hits++; else cache.put(key, "record:" + key);
        }
        return keys.isEmpty() ? 0.0 : (double) hits / keys.size();
    }

    static double replay(LfuCache<String, String> cache, List<String> keys) {
        int hits = 0;
        for (String key : keys) {
            if (cache.get(key) != null) hits++; else cache.put(key, "record:" + key);
        }
        return keys.isEmpty() ? 0.0 : (double) hits / keys.size();
    }
}

public final class Main {
    public static void main(String[] args) {
        System.out.println("== LRU: merchant config, most recent at the head ==");
        ManualClock clock = new ManualClock(1_700_000_000_000L);
        LruCache<String, String> lru = new LruCache<>(3, clock);
        for (String m : new String[] {"merchant-1", "merchant-2", "merchant-3"}) lru.put(m, "config(" + m + ")");
        System.out.println("after 3 puts: " + lru.keys());
        lru.get("merchant-1");
        System.out.println("after get(merchant-1): " + lru.keys());
        lru.put("merchant-4", "config(merchant-4)");
        System.out.println("after put(merchant-4): " + lru.keys() + "  evicted merchant-2, evictions=" + lru.evictions);

        System.out.println("\n== TTL: a config entry that must be re-read after five minutes ==");
        lru.put("merchant-9", "fee_bps=290", 5 * 60_000L);
        System.out.println("t+0:     get(merchant-9) = " + lru.get("merchant-9"));
        clock.advance(4 * 60_000L + 59_999);
        System.out.println("t+4:59.9 get(merchant-9) = " + lru.get("merchant-9"));
        clock.advance(1);
        System.out.println("t+5:00   get(merchant-9) = " + lru.get("merchant-9") + "  expirations=" + lru.expirations);

        System.out.println("\n== LFU: BIN lookups, frequency buckets ==");
        LfuCache<String, String> lfu = new LfuCache<>(3);
        for (String b : new String[] {"bin-411111", "bin-411111", "bin-411111", "bin-555555", "bin-555555", "bin-601100"}) {
            lfu.put(b, "brand(" + b + ")");
        }
        System.out.printf("frequencies: 411111=%d 555555=%d 601100=%d min=%d%n", lfu.frequency("bin-411111"),
                lfu.frequency("bin-555555"), lfu.frequency("bin-601100"), lfu.minFrequency());
        lfu.put("bin-371449", "brand(bin-371449)");
        System.out.printf("put(bin-371449) evicts the least frequent: 601100 present = %b, 411111 present = %b%n",
                lfu.get("bin-601100") != null, lfu.get("bin-411111") != null);

        System.out.println("\n== Same trace, both policies, capacity 32 (20 hot BINs carry 80% of lookups) ==");
        String[] names = {"phase 1: hot set A", "scan: 500 one-off BINs", "phase 1 again", "phase 2: hot set B"};
        List<List<String>> segments = List.of(Trace.binTrace(1, 6000, 0), Trace.scan(500), Trace.binTrace(2, 2000, 0), Trace.binTrace(3, 6000, 20));
        LruCache<String, String> lru2 = new LruCache<>(32);
        LfuCache<String, String> lfu2 = new LfuCache<>(32);
        System.out.printf("%-24s %12s %12s%n", "segment", "LRU hit rate", "LFU hit rate");
        for (int i = 0; i < names.length; i++) {
            System.out.printf("%-24s %12.3f %12.3f%n", names[i], Trace.replay(lru2, segments.get(i)), Trace.replay(lfu2, segments.get(i)));
        }
        // the scan flushed LRU's hot set; LFU kept it. Show the first 200 lookups after the scan.
        LruCache<String, String> lru3 = new LruCache<>(32);
        LfuCache<String, String> lfu3 = new LfuCache<>(32);
        for (int i = 0; i < 2; i++) {
            Trace.replay(lru3, segments.get(i));
            Trace.replay(lfu3, segments.get(i));
        }
        List<String> first200 = segments.get(2).subList(0, 200);
        System.out.printf("first 200 lookups after the scan: LRU %.3f  LFU %.3f%n", Trace.replay(lru3, first200), Trace.replay(lfu3, first200));

        System.out.println("\n== Why you do not cache balances ==");
        Money ledgerBalance = new Money(10_000, "USD");
        LruCache<String, Money> balanceCache = new LruCache<>(16);
        balanceCache.put("acct-7", ledgerBalance);
        ledgerBalance = ledgerBalance.minus(new Money(8_000, "USD"));  // a posting lands: 100.00 -> 20.00
        Money cached = balanceCache.get("acct-7");
        Money authorization = new Money(5_000, "USD");
        System.out.println("ledger says " + ledgerBalance + ", cache says " + cached);
        System.out.println("authorize " + authorization + " against the cache: "
                + (cached.minus(authorization).minorUnits() >= 0 ? "approved" : "declined"));
        System.out.println("actual balance after that approval would be " + ledgerBalance.minus(authorization)
                + ": an overdraft the ledger never allowed");
    }
}
