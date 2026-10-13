// Lesson: phases/03-systems-data-structures/04-b-trees/docs/en.md
// B-Trees and Why Databases Love Them
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: Bayer & McCreight (1972), "Organization and Maintenance of Large Ordered Indexes"; Comer (1979),
//          "The Ubiquitous B-Tree"; Knuth TAOCP Vol. 3 Sec. 6.2.4; PostgreSQL docs, "B-Tree Indexes" (src/backend/access/nbtree/README).
// Java 21, standard library only.

import java.util.ArrayList;
import java.util.Collections;
import java.util.List;
import java.util.Random;

/**
 * Counts page-level I/O. Every node is one page; a search touches one page per level.
 * The counters are what a database's buffer manager would report, and they are the reason B-trees exist.
 */
final class PageStats {
    long reads, writes, splits, merges;

    void reset() { reads = writes = splits = merges = 0; }

    @Override public String toString() {
        return "reads=" + reads + " writes=" + writes + " splits=" + splits + " merges=" + merges;
    }
}

/**
 * B-tree of a configurable order (maximum number of children). A node holds between minKeys and
 * maxKeys keys, except the root which may hold fewer; every leaf is at the same depth; keys in
 * a node are sorted and separate the key ranges of the children around them.
 */
final class BTree<K extends Comparable<K>, V> {

    static final class Node<K, V> {
        final List<K> keys = new ArrayList<>();
        final List<V> values = new ArrayList<>();
        final List<Node<K, V>> children = new ArrayList<>();   // empty for a leaf

        boolean isLeaf() { return children.isEmpty(); }
        int keyCount() { return keys.size(); }
    }

    final int order;      // max children per node
    final int maxKeys;    // order - 1
    final int minKeys;    // ceil(order / 2) - 1: at least half full
    final PageStats stats = new PageStats();
    private Node<K, V> root = new Node<>();
    private int size;

    BTree(int order) {
        if (order < 3) throw new IllegalArgumentException("order must be at least 3: " + order);
        this.order = order;
        this.maxKeys = order - 1;
        this.minKeys = (order + 1) / 2 - 1;
    }

    // ----- search ------------------------------------------------------------------------------

    /** Index of the first key >= key in the node (binary search within the page). */
    static <K extends Comparable<K>, V> int lowerBound(Node<K, V> n, K key) {
        int lo = 0, hi = n.keyCount();
        while (lo < hi) {
            int mid = (lo + hi) >>> 1;
            if (n.keys.get(mid).compareTo(key) < 0) lo = mid + 1; else hi = mid;
        }
        return lo;
    }

    V get(K key) {
        Node<K, V> n = root;
        while (true) {
            stats.reads++;
            int i = lowerBound(n, key);
            if (i < n.keyCount() && n.keys.get(i).compareTo(key) == 0) return n.values.get(i);
            if (n.isLeaf()) return null;
            n = n.children.get(i);
        }
    }

    boolean containsKey(K key) { return get(key) != null; }

    // ----- insert ------------------------------------------------------------------------------

    /** The median key promoted to the parent together with the new right sibling. */
    private record Split<K, V>(K key, V value, Node<K, V> right) {}

    void put(K key, V value) {
        Split<K, V> split = insert(root, key, value);
        if (split != null) {                              // the root overflowed: the tree grows by one level
            Node<K, V> newRoot = new Node<>();
            newRoot.keys.add(split.key());
            newRoot.values.add(split.value());
            newRoot.children.add(root);
            newRoot.children.add(split.right());
            root = newRoot;
            stats.writes++;
        }
    }

    /** Insert into the subtree at n; returns a Split if n overflowed and had to be divided. */
    private Split<K, V> insert(Node<K, V> n, K key, V value) {
        stats.reads++;
        int i = lowerBound(n, key);
        if (i < n.keyCount() && n.keys.get(i).compareTo(key) == 0) {
            n.values.set(i, value);
            stats.writes++;
            return null;
        }
        if (n.isLeaf()) {
            n.keys.add(i, key);
            n.values.add(i, value);
            size++;
        } else {
            Split<K, V> childSplit = insert(n.children.get(i), key, value);
            if (childSplit == null) return null;
            n.keys.add(i, childSplit.key());
            n.values.add(i, childSplit.value());
            n.children.add(i + 1, childSplit.right());
        }
        stats.writes++;
        return n.keyCount() > maxKeys ? split(n) : null;
    }

    /** Divide an overflowing node at its median. Left keeps the lower half, right takes the upper half. */
    private Split<K, V> split(Node<K, V> n) {
        int mid = n.keyCount() / 2;
        K midKey = n.keys.get(mid);
        V midValue = n.values.get(mid);
        Node<K, V> right = new Node<>();
        right.keys.addAll(n.keys.subList(mid + 1, n.keyCount()));
        right.values.addAll(n.values.subList(mid + 1, n.keyCount()));
        n.keys.subList(mid, n.keyCount()).clear();
        n.values.subList(mid, n.values.size()).clear();
        if (!n.isLeaf()) {
            right.children.addAll(n.children.subList(mid + 1, n.children.size()));
            n.children.subList(mid + 1, n.children.size()).clear();
        }
        stats.splits++;
        stats.writes++;                                   // the new right page
        return new Split<>(midKey, midValue, right);
    }

    // ----- delete ------------------------------------------------------------------------------

    void remove(K key) {
        remove(root, key);
        if (root.keyCount() == 0 && !root.isLeaf()) {      // the root emptied: the tree shrinks by one level
            root = root.children.get(0);
        }
    }

    private void remove(Node<K, V> n, K key) {
        stats.reads++;
        int i = lowerBound(n, key);
        boolean found = i < n.keyCount() && n.keys.get(i).compareTo(key) == 0;
        if (n.isLeaf()) {
            if (!found) return;
            n.keys.remove(i);
            n.values.remove(i);
            size--;
            stats.writes++;
            return;
        }
        if (found) {
            // Replace with the in-order predecessor (largest key in the left child), then delete that from the child.
            Node<K, V> pred = n.children.get(i);
            while (!pred.isLeaf()) { stats.reads++; pred = pred.children.get(pred.children.size() - 1); }
            stats.reads++;
            K predKey = pred.keys.get(pred.keyCount() - 1);
            V predValue = pred.values.get(pred.keyCount() - 1);
            n.keys.set(i, predKey);
            n.values.set(i, predValue);
            stats.writes++;
            remove(n.children.get(i), predKey);
        } else {
            remove(n.children.get(i), key);
        }
        if (n.children.get(i).keyCount() < minKeys) rebalance(n, i);
    }

    /** Child i of parent underflowed: borrow from a sibling that can spare a key, else merge with one. */
    private void rebalance(Node<K, V> parent, int i) {
        Node<K, V> child = parent.children.get(i);
        if (i > 0 && parent.children.get(i - 1).keyCount() > minKeys) {
            Node<K, V> left = parent.children.get(i - 1);          // rotate one key through the parent, from the left
            child.keys.add(0, parent.keys.get(i - 1));
            child.values.add(0, parent.values.get(i - 1));
            parent.keys.set(i - 1, left.keys.remove(left.keyCount() - 1));
            parent.values.set(i - 1, left.values.remove(left.values.size() - 1));
            if (!left.isLeaf()) child.children.add(0, left.children.remove(left.children.size() - 1));
            stats.writes += 3;
        } else if (i < parent.children.size() - 1 && parent.children.get(i + 1).keyCount() > minKeys) {
            Node<K, V> right = parent.children.get(i + 1);         // rotate one key through the parent, from the right
            child.keys.add(parent.keys.get(i));
            child.values.add(parent.values.get(i));
            parent.keys.set(i, right.keys.remove(0));
            parent.values.set(i, right.values.remove(0));
            if (!right.isLeaf()) child.children.add(right.children.remove(0));
            stats.writes += 3;
        } else {
            int leftIndex = i > 0 ? i - 1 : i;                     // merge child with a sibling around the separator
            Node<K, V> left = parent.children.get(leftIndex);
            Node<K, V> right = parent.children.get(leftIndex + 1);
            left.keys.add(parent.keys.remove(leftIndex));
            left.values.add(parent.values.remove(leftIndex));
            left.keys.addAll(right.keys);
            left.values.addAll(right.values);
            left.children.addAll(right.children);
            parent.children.remove(leftIndex + 1);
            stats.merges++;
            stats.writes += 2;
        }
    }

    // ----- range scan and traversal -------------------------------------------------------------

    /** Keys in [lo, hi] ascending. Descends only into children whose range can intersect. */
    List<K> range(K lo, K hi) {
        List<K> out = new ArrayList<>();
        range(root, lo, hi, out);
        return out;
    }

    private void range(Node<K, V> n, K lo, K hi, List<K> out) {
        stats.reads++;
        int i = lowerBound(n, lo);
        if (!n.isLeaf()) range(n.children.get(i), lo, hi, out);
        for (; i < n.keyCount(); i++) {
            K k = n.keys.get(i);
            if (k.compareTo(hi) > 0) return;
            out.add(k);
            if (!n.isLeaf()) range(n.children.get(i + 1), lo, hi, out);
        }
    }

    List<K> keys() {
        List<K> out = new ArrayList<>(size);
        collect(root, out);
        return out;
    }

    private void collect(Node<K, V> n, List<K> out) {
        for (int i = 0; i < n.keyCount(); i++) {
            if (!n.isLeaf()) collect(n.children.get(i), out);
            out.add(n.keys.get(i));
        }
        if (!n.isLeaf()) collect(n.children.get(n.children.size() - 1), out);
    }

    // ----- shape -------------------------------------------------------------------------------

    int size() { return size; }

    /** Number of levels, counting the root. Every leaf is at this depth. */
    int height() {
        int h = 0;
        for (Node<K, V> n = root; ; n = n.children.get(0)) {
            h++;
            if (n.isLeaf()) return h;
        }
    }

    int nodeCount() { return nodeCount(root); }

    private int nodeCount(Node<K, V> n) {
        int c = 1;
        for (Node<K, V> ch : n.children) c += nodeCount(ch);
        return c;
    }

    /** Upper bound on height from the paper: 1 + log_(ceil(order/2)) ((n + 1) / 2). */
    static double heightBound(int order, int n) {
        int halfFanOut = (order + 1) / 2;
        return 1 + Math.log((n + 1) / 2.0) / Math.log(halfFanOut);
    }

    /** Sorted keys, occupancy bounds, separator ranges, equal leaf depth. Throws on the first violation. */
    boolean checkInvariants() {
        verify(root, null, null, 0, new int[]{-1});
        return true;
    }

    private void verify(Node<K, V> n, K lo, K hi, int depth, int[] leafDepth) {
        if (n != root && n.keyCount() < minKeys) throw new IllegalStateException("underfull node: " + n.keys);
        if (n.keyCount() > maxKeys) throw new IllegalStateException("overfull node: " + n.keys);
        if (n != root && n.keyCount() == 0) throw new IllegalStateException("empty non-root node");
        for (int i = 0; i < n.keyCount(); i++) {
            K k = n.keys.get(i);
            if (i > 0 && n.keys.get(i - 1).compareTo(k) >= 0) throw new IllegalStateException("keys not sorted: " + n.keys);
            if (lo != null && k.compareTo(lo) <= 0) throw new IllegalStateException("key " + k + " below separator " + lo);
            if (hi != null && k.compareTo(hi) >= 0) throw new IllegalStateException("key " + k + " above separator " + hi);
        }
        if (n.isLeaf()) {
            if (leafDepth[0] == -1) leafDepth[0] = depth;
            else if (leafDepth[0] != depth) throw new IllegalStateException("leaves at different depths: " + leafDepth[0] + " and " + depth);
            return;
        }
        if (n.children.size() != n.keyCount() + 1) throw new IllegalStateException("children != keys + 1 at " + n.keys);
        for (int i = 0; i < n.children.size(); i++) {
            K childLo = i == 0 ? lo : n.keys.get(i - 1);
            K childHi = i == n.keyCount() ? hi : n.keys.get(i);
            verify(n.children.get(i), childLo, childHi, depth + 1, leafDepth);
        }
    }
}

public final class Main {

    public static void main(String[] args) {
        System.out.println("== Fan-out decides height: 10 million keys, one page read per level ==");
        System.out.printf("binary tree: height ~ %.1f (one node per level: 23 dependent cache misses or disk reads)%n",
                Math.log(10_000_000) / Math.log(2));
        for (int order : new int[]{8, 64, 400}) {
            System.out.printf("order %4d:  height <= %.1f%n", order, BTree.heightBound(order, 10_000_000));
        }
        System.out.println("PostgreSQL: 8 KB page, ~400 bigint keys per page -> 4 levels index billions of rows, and the top 3 live in RAM.");

        System.out.println();
        System.out.println("== A small tree you can draw: order 4, keys 1..10 ==");
        BTree<Integer, String> small = new BTree<>(4);
        for (int k = 1; k <= 10; k++) small.put(k, "v" + k);
        small.checkInvariants();
        System.out.printf("height=%d nodes=%d keys=%s%n", small.height(), small.nodeCount(), small.keys());
        System.out.println("stats after 10 inserts: " + small.stats);

        System.out.println();
        System.out.println("== 200,000 payment ids, order 64: sequential vs random keys (UUIDv4-style) ==");
        int n = 200_000;
        BTree<Long, Integer> sequential = new BTree<>(64);
        for (long id = 1; id <= n; id++) sequential.put(id, 1);
        sequential.checkInvariants();
        System.out.printf("sequential: height=%d nodes=%d %s%n", sequential.height(), sequential.nodeCount(), sequential.stats);

        Random rng = new Random(42);
        BTree<Long, Integer> random = new BTree<>(64);
        for (int i = 0; i < n; i++) random.put(rng.nextLong() & Long.MAX_VALUE, 1);
        random.checkInvariants();
        System.out.printf("random:     height=%d nodes=%d %s%n", random.height(), random.nodeCount(), random.stats);
        System.out.printf("occupancy: sequential ~%.0f%% (every split leaves a half-empty page behind), random ~%.0f%% (ln 2)%n",
                100.0 * sequential.size() / (sequential.nodeCount() * 63.0), 100.0 * random.size() / (random.nodeCount() * 63.0));
        System.out.println("real databases special-case the rightmost split (PostgreSQL fillfactor 90%) to fix the sequential case.");

        System.out.println();
        System.out.println("== Point lookup and range scan cost in page reads ==");
        sequential.stats.reset();
        sequential.get(123_456L);
        System.out.printf("get(123456): %d page reads (= height %d)%n", sequential.stats.reads, sequential.height());
        sequential.stats.reset();
        List<Long> scan = sequential.range(100_000L, 100_999L);
        System.out.printf("range(100000, 100999): %d keys in %d page reads%n", scan.size(), sequential.stats.reads);

        System.out.println();
        System.out.println("== Deletes: borrow from a sibling or merge, leaves stay level ==");
        sequential.stats.reset();
        for (long id = 1; id <= n; id += 2) sequential.remove(id);
        sequential.checkInvariants();
        System.out.printf("after removing every odd id: size=%d height=%d nodes=%d %s%n",
                sequential.size(), sequential.height(), sequential.nodeCount(), sequential.stats);

        System.out.println();
        System.out.println("== Shrinking all the way down ==");
        List<Integer> ids = new ArrayList<>();
        for (int i = 0; i < 5_000; i++) ids.add(i);
        Collections.shuffle(ids, new Random(7));
        BTree<Integer, String> shrink = new BTree<>(6);
        for (int id : ids) shrink.put(id, "x");
        int h0 = shrink.height();
        for (int id : ids) { shrink.remove(id); }
        shrink.checkInvariants();
        System.out.printf("5000 inserts then 5000 deletes in random order: height %d -> %d, size=%d%n", h0, shrink.height(), shrink.size());
    }
}
