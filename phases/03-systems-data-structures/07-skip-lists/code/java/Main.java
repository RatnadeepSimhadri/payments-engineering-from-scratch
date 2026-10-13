// Lesson: phases/03-systems-data-structures/07-skip-lists/docs/en.md
// Skip Lists
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: Pugh (1990), "Skip Lists: A Probabilistic Alternative to Balanced Trees", CACM 33(6);
//          Redis t_zset.c (zslInsert, zslDeleteNode, zslGetRank); java.util.concurrent.ConcurrentSkipListMap.
// Java 21, standard library only.

import java.util.ArrayList;
import java.util.Collections;
import java.util.List;
import java.util.Random;
import java.util.function.IntSupplier;

/**
 * Skip list with Redis-style spans so that rank queries are O(log n). Levels are chosen by a coin
 * flip with probability p per extra level, from an injected RNG so every run is reproducible.
 */
final class SkipList<K extends Comparable<K>, V> {

    static final class Node<K, V> {
        final K key;
        V value;
        final Node<K, V>[] forward;   // forward[i]: next node at level i
        final int[] span;             // span[i]: number of level-0 steps forward[i] jumps over

        @SuppressWarnings("unchecked")
        Node(K key, V value, int levels) {
            this.key = key;
            this.value = value;
            this.forward = (Node<K, V>[]) new Node[levels];
            this.span = new int[levels];
        }
    }

    final int maxLevel;
    final double p;
    private final IntSupplier levelSource;   // returns a level in [1, maxLevel]
    private final Node<K, V> header;
    private int level = 1;                    // levels currently in use
    private int size;
    long searchSteps;                         // pointer advances across all searches, for the complexity measurement

    SkipList(int maxLevel, double p, Random rng) {
        if (maxLevel < 1 || maxLevel > 64) throw new IllegalArgumentException("maxLevel must be 1..64: " + maxLevel);
        if (!(p > 0 && p < 1)) throw new IllegalArgumentException("p must be in (0,1): " + p);
        this.maxLevel = maxLevel;
        this.p = p;
        this.levelSource = () -> {
            int lvl = 1;
            while (lvl < maxLevel && rng.nextDouble() < p) lvl++;
            return lvl;
        };
        this.header = new Node<>(null, null, maxLevel);
    }

    /** For the degenerate-case demo: a list whose "coin" is supplied by the caller. */
    SkipList(int maxLevel, double p, IntSupplier levelSource) {
        this.maxLevel = maxLevel;
        this.p = p;
        this.levelSource = levelSource;
        this.header = new Node<>(null, null, maxLevel);
    }

    static <K, V> int defaultMaxLevel(int expectedSize, double p) {
        return Math.max(1, (int) Math.ceil(Math.log(expectedSize) / Math.log(1.0 / p)));
    }

    int randomLevel() { return levelSource.getAsInt(); }

    // ----- search ------------------------------------------------------------------------------

    /** Counts one step per forward move and one per level descended: the comparisons Pugh's analysis bounds. */
    V get(K key) {
        Node<K, V> x = header;
        for (int i = level - 1; i >= 0; i--) {
            while (x.forward[i] != null && x.forward[i].key.compareTo(key) < 0) { x = x.forward[i]; searchSteps++; }
            searchSteps++;                                     // the comparison that failed, sending us down a level
        }
        x = x.forward[0];
        return x != null && x.key.compareTo(key) == 0 ? x.value : null;
    }

    boolean containsKey(K key) { return get(key) != null; }

    // ----- insert ------------------------------------------------------------------------------

    @SuppressWarnings("unchecked")
    void put(K key, V value) {
        Node<K, V>[] update = (Node<K, V>[]) new Node[maxLevel];
        int[] rank = new int[maxLevel];
        Node<K, V> x = header;
        for (int i = level - 1; i >= 0; i--) {
            rank[i] = i == level - 1 ? 0 : rank[i + 1];
            while (x.forward[i] != null && x.forward[i].key.compareTo(key) < 0) {
                rank[i] += x.span[i];
                x = x.forward[i];
            }
            update[i] = x;
        }
        Node<K, V> existing = x.forward[0];
        if (existing != null && existing.key.compareTo(key) == 0) { existing.value = value; return; }

        int lvl = randomLevel();
        if (lvl > level) {
            for (int i = level; i < lvl; i++) {
                rank[i] = 0;
                update[i] = header;
                update[i].span[i] = size;            // the header's new level spans the whole list so far
            }
            level = lvl;
        }
        Node<K, V> node = new Node<>(key, value, lvl);
        for (int i = 0; i < lvl; i++) {
            node.forward[i] = update[i].forward[i];
            update[i].forward[i] = node;
            node.span[i] = update[i].span[i] - (rank[0] - rank[i]);
            update[i].span[i] = (rank[0] - rank[i]) + 1;
        }
        for (int i = lvl; i < level; i++) update[i].span[i]++;   // levels above the new node now jump over one more
        size++;
    }

    // ----- delete ------------------------------------------------------------------------------

    @SuppressWarnings("unchecked")
    boolean remove(K key) {
        Node<K, V>[] update = (Node<K, V>[]) new Node[maxLevel];
        Node<K, V> x = header;
        for (int i = level - 1; i >= 0; i--) {
            while (x.forward[i] != null && x.forward[i].key.compareTo(key) < 0) x = x.forward[i];
            update[i] = x;
        }
        x = x.forward[0];
        if (x == null || x.key.compareTo(key) != 0) return false;
        for (int i = 0; i < level; i++) {
            if (update[i].forward[i] == x) {
                update[i].span[i] += x.span[i] - 1;
                update[i].forward[i] = x.forward[i];
            } else {
                update[i].span[i]--;
            }
        }
        while (level > 1 && header.forward[level - 1] == null) level--;
        size--;
        return true;
    }

    // ----- rank and range: the sorted-set operations ---------------------------------------------

    /** 1-based position of key in sorted order, or 0 if absent. O(log n) thanks to spans. */
    int rank(K key) {
        int traversed = 0;
        Node<K, V> x = header;
        for (int i = level - 1; i >= 0; i--) {
            while (x.forward[i] != null && x.forward[i].key.compareTo(key) <= 0) {
                traversed += x.span[i];
                x = x.forward[i];
            }
            if (x != header && x.key.compareTo(key) == 0) return traversed;
        }
        return 0;
    }

    /** The key at 1-based rank r, or null. O(log n). */
    K byRank(int r) {
        if (r < 1 || r > size) return null;
        int traversed = 0;
        Node<K, V> x = header;
        for (int i = level - 1; i >= 0; i--) {
            while (x.forward[i] != null && traversed + x.span[i] <= r) {
                traversed += x.span[i];
                x = x.forward[i];
            }
            if (traversed == r) return x.key;
        }
        return null;
    }

    /** Keys in [lo, hi]: descend to the first key >= lo, then walk level 0. O(log n + k). */
    List<K> range(K lo, K hi) {
        List<K> out = new ArrayList<>();
        Node<K, V> x = header;
        for (int i = level - 1; i >= 0; i--) {
            while (x.forward[i] != null && x.forward[i].key.compareTo(lo) < 0) x = x.forward[i];
        }
        for (x = x.forward[0]; x != null && x.key.compareTo(hi) <= 0; x = x.forward[0]) out.add(x.key);
        return out;
    }

    List<K> keys() {
        List<K> out = new ArrayList<>(size);
        for (Node<K, V> x = header.forward[0]; x != null; x = x.forward[0]) out.add(x.key);
        return out;
    }

    // ----- shape -------------------------------------------------------------------------------

    int size() { return size; }
    int level() { return level; }

    /** How many nodes reach each level: index i holds the count of nodes with at least i+1 levels. */
    int[] levelHistogram() {
        int[] h = new int[level];
        for (int i = 0; i < level; i++) for (Node<K, V> x = header.forward[i]; x != null; x = x.forward[i]) h[i]++;
        return h;
    }

    /** Pugh's expected search cost: about log_(1/p)(n) / p comparisons (forward moves plus level descents). */
    static double expectedSteps(int n, double p) { return Math.log(n) / Math.log(1.0 / p) / p; }

    /**
     * Level 0 is sorted and holds every key; each higher level is a subsequence of the level below;
     * every span equals the level-0 distance it claims; no node exists above the current level.
     */
    boolean checkInvariants() {
        List<K> base = keys();
        if (base.size() != size) throw new IllegalStateException("level 0 holds " + base.size() + " keys, size says " + size);
        for (int i = 1; i < base.size(); i++) {
            if (base.get(i - 1).compareTo(base.get(i)) >= 0) throw new IllegalStateException("level 0 not sorted at " + base.get(i));
        }
        for (int i = 0; i < level; i++) {
            int position = 0;                                 // 1-based rank of x; header is 0
            for (Node<K, V> x = header; x.forward[i] != null; x = x.forward[i]) {
                int target = position + x.span[i];
                if (target < 1 || target > size || !base.get(target - 1).equals(x.forward[i].key)) {
                    throw new IllegalStateException("span mismatch at level " + i + " after " + (x == header ? "header" : x.key));
                }
                position = target;
            }
            if (i == 0 && position != size) throw new IllegalStateException("level 0 spans do not reach the end");
        }
        for (int i = level; i < maxLevel; i++) {
            if (header.forward[i] != null) throw new IllegalStateException("node above current level " + i);
        }
        return true;
    }
}

public final class Main {

    public static void main(String[] args) {
        Random rng = new Random(2024);

        System.out.println("== Merchant risk scores as a sorted set (Redis ZADD / ZRANK / ZRANGE) ==");
        SkipList<Integer, String> scores = new SkipList<>(16, 0.5, rng);
        int[] sample = {742, 118, 905, 367, 512, 288, 674, 59, 831, 456};
        for (int i = 0; i < sample.length; i++) scores.put(sample[i], "merchant_" + i);
        System.out.println("sorted: " + scores.keys());
        System.out.println("rank of 512 = " + scores.rank(512) + ", key at rank 3 = " + scores.byRank(3) + ", scores in [300, 700] = " + scores.range(300, 700));
        System.out.println("levels in use: " + scores.level() + ", nodes per level: " + java.util.Arrays.toString(scores.levelHistogram()));

        System.out.println();
        System.out.println("== 100,000 keys: the geometric level distribution ==");
        int n = 100_000;
        SkipList<Integer, Integer> big = new SkipList<>(SkipList.defaultMaxLevel(n, 0.5), 0.5, rng);
        List<Integer> keys = new ArrayList<>();
        for (int i = 0; i < n; i++) keys.add(i);
        Collections.shuffle(keys, rng);
        for (int k : keys) big.put(k, k);
        big.checkInvariants();
        int[] hist = big.levelHistogram();
        System.out.printf("maxLevel=%d levels in use=%d%n", big.maxLevel, big.level());
        for (int i = 0; i < Math.min(hist.length, 8); i++) {
            System.out.printf("level %2d: %6d nodes  (expected n/2^%d = %6.0f)%n", i, hist[i], i, n / Math.pow(2, i));
        }

        System.out.println();
        System.out.println("== Search cost: measured pointer steps vs Pugh's bound ==");
        big.searchSteps = 0;
        for (int i = 0; i < 10_000; i++) big.get(rng.nextInt(n));
        System.out.printf("average comparisons per get = %.1f, Pugh's expectation log2(n)/p = %.1f, log2(n) = %.1f%n",
                big.searchSteps / 10_000.0, SkipList.expectedSteps(n, 0.5), Math.log(n) / Math.log(2));

        System.out.println();
        System.out.println("== Sorted inserts do not hurt: the shape depends on coins, not on keys ==");
        SkipList<Integer, Integer> sorted = new SkipList<>(17, 0.5, new Random(7));
        for (int i = 0; i < n; i++) sorted.put(i, i);
        sorted.searchSteps = 0;
        for (int i = 0; i < 10_000; i++) sorted.get(i * 10);
        System.out.printf("sorted input: levels=%d, average steps per get = %.1f%n", sorted.level(), sorted.searchSteps / 10_000.0);

        System.out.println();
        System.out.println("== What the coin buys: a list whose coin always says 1 is a linked list ==");
        SkipList<Integer, Integer> flat = new SkipList<>(16, 0.5, () -> 1);
        for (int i = 0; i < 20_000; i++) flat.put(i, i);
        flat.searchSteps = 0;
        for (int i = 0; i < 100; i++) flat.get(rng.nextInt(20_000));
        System.out.printf("level=%d, average steps per get = %.0f (n/2 = %d)%n", flat.level(), flat.searchSteps / 100.0, 10_000);

        System.out.println();
        System.out.println("== Removes keep spans and ranks consistent ==");
        for (int k = 0; k < n; k += 2) big.remove(k);
        big.checkInvariants();
        System.out.printf("after removing even keys: size=%d, rank(99999)=%d, byRank(1)=%d, byRank(50000)=%d%n",
                big.size(), big.rank(99_999), big.byRank(1), big.byRank(50_000));
    }
}
