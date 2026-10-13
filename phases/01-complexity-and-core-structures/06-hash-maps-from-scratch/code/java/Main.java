// Lesson: phases/01-complexity-and-core-structures/06-hash-maps-from-scratch/docs/en.md
// Hash Maps from Scratch: Chaining, Open Addressing, and Resizing
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: Knuth TAOCP Vol. 3 Section 6.4 (hashing, linear probing analysis); FNV-1a (Fowler, Noll, Vo);
//          Stripe API "Idempotent requests" for the store framing. Java 21, standard library only.

import java.nio.charset.StandardCharsets;
import java.util.ArrayList;
import java.util.List;
import java.util.Objects;
import java.util.function.Supplier;
import java.util.function.ToIntFunction;

/** The operations both tables share, so the idempotency store can be built on either one. */
interface SimpleMap<K, V> {
    V put(K key, V value);
    V get(K key);
    V remove(K key);
    boolean containsKey(K key);
    int size();
    int capacity();
    /** How many new-key inserts found their home slot already taken (or, for probing, total displacement). */
    long collisions();
    default double loadFactor() { return (double) size() / capacity(); }
}

/** Deterministic hash functions. Java's String.hashCode is stable too, but FNV-1a is identical in all three languages. */
final class Hashers {
    private Hashers() {}

    /** FNV-1a, 32-bit, over the UTF-8 bytes of the string. Same result in Java, Python, and Go. */
    static int fnv1a(String s) {
        int hash = 0x811c9dc5;
        for (byte b : s.getBytes(StandardCharsets.UTF_8)) {
            hash ^= (b & 0xff);
            hash *= 0x01000193;
        }
        return hash;
    }

    /** A pathological hasher: every key lands in bucket 0. Used to measure worst-case behaviour. */
    static <K> ToIntFunction<K> constant() { return k -> 0; }

    /** Map a 32-bit hash to a slot in a power-of-two table, mixing the high bits into the low ones first. */
    static int indexFor(int hash, int capacity) {
        int spread = hash ^ (hash >>> 16);
        return spread & (capacity - 1);
    }
}

/** Separate chaining: each bucket is the head of a singly linked list of entries. */
final class ChainedHashMap<K, V> implements SimpleMap<K, V> {
    static final int DEFAULT_CAPACITY = 8;
    static final double MAX_LOAD = 0.75;

    private static final class Node<K, V> {
        final K key;
        V value;
        Node<K, V> next;
        Node(K key, V value, Node<K, V> next) { this.key = key; this.value = value; this.next = next; }
    }

    private final ToIntFunction<K> hasher;
    private Node<K, V>[] table;
    private int size;
    private long collisions;
    private int resizes;

    ChainedHashMap(ToIntFunction<K> hasher) { this(hasher, DEFAULT_CAPACITY); }

    @SuppressWarnings("unchecked")
    ChainedHashMap(ToIntFunction<K> hasher, int capacity) {
        if (Integer.bitCount(capacity) != 1) throw new IllegalArgumentException("capacity must be a power of two: " + capacity);
        this.hasher = Objects.requireNonNull(hasher);
        this.table = (Node<K, V>[]) new Node[capacity];
    }

    private int bucketOf(K key) { return Hashers.indexFor(hasher.applyAsInt(key), table.length); }

    @Override public V put(K key, V value) {
        Objects.requireNonNull(key, "key");
        int b = bucketOf(key);
        for (Node<K, V> n = table[b]; n != null; n = n.next) {
            if (n.key.equals(key)) { V old = n.value; n.value = value; return old; }
        }
        if (table[b] != null) collisions++;
        table[b] = new Node<>(key, value, table[b]);
        size++;
        if (size > table.length * MAX_LOAD) resize(table.length * 2);
        return null;
    }

    @Override public V get(K key) {
        for (Node<K, V> n = table[bucketOf(key)]; n != null; n = n.next) {
            if (n.key.equals(key)) return n.value;
        }
        return null;
    }

    @Override public boolean containsKey(K key) {
        for (Node<K, V> n = table[bucketOf(key)]; n != null; n = n.next) {
            if (n.key.equals(key)) return true;
        }
        return false;
    }

    @Override public V remove(K key) {
        int b = bucketOf(key);
        Node<K, V> prev = null;
        for (Node<K, V> n = table[b]; n != null; prev = n, n = n.next) {
            if (n.key.equals(key)) {
                if (prev == null) table[b] = n.next; else prev.next = n.next;
                size--;
                return n.value;
            }
        }
        return null;
    }

    @SuppressWarnings("unchecked")
    private void resize(int newCapacity) {
        Node<K, V>[] old = table;
        table = (Node<K, V>[]) new Node[newCapacity];
        for (Node<K, V> head : old) {
            for (Node<K, V> n = head; n != null; ) {
                Node<K, V> next = n.next;
                int b = Hashers.indexFor(hasher.applyAsInt(n.key), newCapacity);
                n.next = table[b];
                table[b] = n;
                n = next;
            }
        }
        resizes++;
    }

    /** Longest chain in the table: the worst-case lookup cost right now. */
    int maxChainLength() {
        int max = 0;
        for (Node<K, V> head : table) {
            int len = 0;
            for (Node<K, V> n = head; n != null; n = n.next) len++;
            max = Math.max(max, len);
        }
        return max;
    }

    @Override public int size() { return size; }
    @Override public int capacity() { return table.length; }
    @Override public long collisions() { return collisions; }
    int resizes() { return resizes; }
}

/** Open addressing with linear probing and tombstones: keys, values, and states live in flat arrays. */
final class OpenAddressingHashMap<K, V> implements SimpleMap<K, V> {
    static final int DEFAULT_CAPACITY = 8;
    static final double MAX_LOAD = 0.5;

    private static final byte EMPTY = 0, OCCUPIED = 1, TOMBSTONE = 2;

    private final ToIntFunction<K> hasher;
    private Object[] keys;
    private Object[] values;
    private byte[] states;
    private int size;
    private int tombstones;
    private long collisions;   // total displacement: sum over new-key inserts of (slot used - home slot)
    private int resizes;

    OpenAddressingHashMap(ToIntFunction<K> hasher) { this(hasher, DEFAULT_CAPACITY); }

    OpenAddressingHashMap(ToIntFunction<K> hasher, int capacity) {
        if (Integer.bitCount(capacity) != 1) throw new IllegalArgumentException("capacity must be a power of two: " + capacity);
        this.hasher = Objects.requireNonNull(hasher);
        allocate(capacity);
    }

    private void allocate(int capacity) {
        keys = new Object[capacity];
        values = new Object[capacity];
        states = new byte[capacity];
        tombstones = 0;
    }

    private int homeOf(K key) { return Hashers.indexFor(hasher.applyAsInt(key), keys.length); }

    /** Slot holding the key, or -1. Probing stops at EMPTY and walks through TOMBSTONEs. */
    int slotOf(K key) {
        int i = homeOf(key);
        for (int probes = 0; probes < keys.length; probes++) {
            if (states[i] == EMPTY) return -1;
            if (states[i] == OCCUPIED && keys[i].equals(key)) return i;
            i = (i + 1) & (keys.length - 1);
        }
        return -1;
    }

    @Override public V put(K key, V value) {
        Objects.requireNonNull(key, "key");
        if ((size + tombstones + 1) > keys.length * MAX_LOAD) resize(keys.length * 2);
        int home = homeOf(key);
        int i = home;
        int firstTombstone = -1;
        int displacement = 0;
        while (true) {
            if (states[i] == EMPTY) break;
            if (states[i] == OCCUPIED && keys[i].equals(key)) {
                @SuppressWarnings("unchecked") V old = (V) values[i];
                values[i] = value;
                return old;
            }
            if (states[i] == TOMBSTONE && firstTombstone < 0) firstTombstone = i;
            i = (i + 1) & (keys.length - 1);
            displacement++;
        }
        if (firstTombstone >= 0) {           // reuse the grave rather than extend the cluster
            i = firstTombstone;
            tombstones--;
            displacement = (i - home) & (keys.length - 1);
        }
        collisions += displacement;
        keys[i] = key;
        values[i] = value;
        states[i] = OCCUPIED;
        size++;
        return null;
    }

    @Override public V get(K key) {
        int i = slotOf(key);
        @SuppressWarnings("unchecked") V v = i < 0 ? null : (V) values[i];
        return v;
    }

    @Override public boolean containsKey(K key) { return slotOf(key) >= 0; }

    @Override public V remove(K key) {
        int i = slotOf(key);
        if (i < 0) return null;
        @SuppressWarnings("unchecked") V old = (V) values[i];
        keys[i] = null;
        values[i] = null;
        states[i] = TOMBSTONE;      // not EMPTY: a probe chain may run through this slot
        tombstones++;
        size--;
        return old;
    }

    @SuppressWarnings("unchecked")
    private void resize(int newCapacity) {
        Object[] oldKeys = keys, oldValues = values;
        byte[] oldStates = states;
        allocate(newCapacity);
        for (int j = 0; j < oldKeys.length; j++) {
            if (oldStates[j] != OCCUPIED) continue;        // tombstones are dropped here
            K k = (K) oldKeys[j];
            int i = Hashers.indexFor(hasher.applyAsInt(k), newCapacity);
            while (states[i] != EMPTY) i = (i + 1) & (newCapacity - 1);
            keys[i] = k;
            values[i] = oldValues[j];
            states[i] = OCCUPIED;
        }
        resizes++;
    }

    /** Longest run of non-EMPTY slots: the worst-case probe length for a miss right now. */
    int maxProbeRun() {
        int max = 0, run = 0;
        for (int pass = 0; pass < 2; pass++) {              // two passes so a run wrapping the end is seen whole
            for (byte s : states) {
                if (s == EMPTY) { max = Math.max(max, run); run = 0; }
                else run = Math.min(run + 1, states.length);
            }
        }
        return Math.max(max, run);
    }

    @Override public int size() { return size; }
    @Override public int capacity() { return keys.length; }
    @Override public long collisions() { return collisions; }
    int tombstones() { return tombstones; }
    int resizes() { return resizes; }
}

/** What an API server stores per idempotency key: the status and body it already sent. */
record StoredResponse(int status, String body) {}

/** The result of asking the store to handle a request: the response, and whether it was replayed. */
record Outcome(StoredResponse response, boolean replayed) {}

/**
 * An idempotency store: key = client-supplied idempotency key, value = the response first produced for it.
 * The first call with a key runs the handler and records its response; every later call replays it
 * without running the handler again, so a retried "charge 19.99 USD" charges once.
 */
final class IdempotencyStore {
    private final SimpleMap<String, StoredResponse> responses;
    private int handlerRuns;

    IdempotencyStore(SimpleMap<String, StoredResponse> backing) { this.responses = backing; }

    Outcome handle(String idempotencyKey, Supplier<StoredResponse> handler) {
        StoredResponse existing = responses.get(idempotencyKey);
        if (existing != null) return new Outcome(existing, true);
        StoredResponse fresh = handler.get();
        handlerRuns++;
        responses.put(idempotencyKey, fresh);
        return new Outcome(fresh, false);
    }

    int handlerRuns() { return handlerRuns; }
    int size() { return responses.size(); }
}

public final class Main {
    static List<String> keys(int n) {
        List<String> out = new ArrayList<>(n);
        for (int i = 0; i < n; i++) out.add(String.format("idem-%06d", i));
        return out;
    }

    public static void main(String[] args) {
        System.out.println("== FNV-1a is the same in every language ==");
        System.out.printf("fnv1a(\"idem-000001\") = 0x%08x%n", Hashers.fnv1a("idem-000001"));
        System.out.printf("fnv1a(\"\") = 0x%08x%n", Hashers.fnv1a(""));

        System.out.println("\n== 10,000 idempotency keys, separate chaining ==");
        ChainedHashMap<String, Integer> chained = new ChainedHashMap<>(Hashers::fnv1a);
        List<String> keys = keys(10_000);
        for (int i = 0; i < keys.size(); i++) chained.put(keys.get(i), i);
        System.out.printf("size=%d capacity=%d load=%.3f resizes=%d collisions=%d maxChain=%d%n",
                chained.size(), chained.capacity(), chained.loadFactor(), chained.resizes(),
                chained.collisions(), chained.maxChainLength());

        System.out.println("\n== Same keys, open addressing (linear probing) ==");
        OpenAddressingHashMap<String, Integer> open = new OpenAddressingHashMap<>(Hashers::fnv1a);
        for (int i = 0; i < keys.size(); i++) open.put(keys.get(i), i);
        System.out.printf("size=%d capacity=%d load=%.3f resizes=%d displacement=%d maxProbeRun=%d%n",
                open.size(), open.capacity(), open.loadFactor(), open.resizes(),
                open.collisions(), open.maxProbeRun());

        System.out.println("\n== Tombstones: remove half, re-insert, probe chains survive ==");
        for (int i = 0; i < keys.size(); i += 2) open.remove(keys.get(i));
        System.out.printf("after removing 5,000: size=%d tombstones=%d get(idem-000001)=%s%n",
                open.size(), open.tombstones(), open.get("idem-000001"));
        for (int i = 0; i < keys.size(); i += 2) open.put(keys.get(i), -i);
        System.out.printf("after re-inserting: size=%d tombstones=%d (graves were reused)%n", open.size(), open.tombstones());

        System.out.println("\n== Worst case: a hasher that returns 0 for every key ==");
        ChainedHashMap<String, Integer> bad = new ChainedHashMap<>(Hashers.constant());
        OpenAddressingHashMap<String, Integer> badOpen = new OpenAddressingHashMap<>(Hashers.constant());
        for (int i = 0; i < 64; i++) { bad.put(keys.get(i), i); badOpen.put(keys.get(i), i); }
        System.out.printf("chaining: 64 keys -> collisions=%d maxChain=%d (every lookup is O(n))%n",
                bad.collisions(), bad.maxChainLength());
        System.out.printf("probing:  64 keys -> displacement=%d maxProbeRun=%d (sum 0..63 = 2016)%n",
                badOpen.collisions(), badOpen.maxProbeRun());

        System.out.println("\n== Idempotency store: retry the same charge three times ==");
        IdempotencyStore store = new IdempotencyStore(new ChainedHashMap<>(Hashers::fnv1a));
        for (int attempt = 1; attempt <= 3; attempt++) {
            Outcome o = store.handle("order-8841-charge", () -> new StoredResponse(201, "{\"charge\":\"ch_1\",\"amount\":1999,\"currency\":\"USD\"}"));
            System.out.printf("attempt %d: status=%d replayed=%s body=%s%n", attempt, o.response().status(), o.replayed(), o.response().body());
        }
        System.out.printf("handler ran %d time(s) for 3 attempts%n", store.handlerRuns());
    }
}
