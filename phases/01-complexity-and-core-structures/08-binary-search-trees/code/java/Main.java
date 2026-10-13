// Lesson: phases/01-complexity-and-core-structures/08-binary-search-trees/docs/en.md
// Binary Search Trees and Ordered Maps
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: Cormen, Leiserson, Rivest, Stein, "Introduction to Algorithms", ch. 12 (binary search trees);
//          Knuth, TAOCP vol. 3, 6.2.2. ISO 4217 for the Money type reused from Phase 10, Lesson 01.
// Java 21, standard library only.

import java.util.ArrayDeque;
import java.util.ArrayList;
import java.util.Deque;
import java.util.List;
import java.util.Objects;
import java.util.Random;

/** ISO 4217 currency: code plus the number of decimal places in its minor unit (Phase 10, Lesson 01). */
record Currency(String code, int exponent) {
    static final Currency USD = new Currency("USD", 2);

    Currency {
        if (code == null || code.length() != 3) throw new IllegalArgumentException("currency code: " + code);
        if (exponent < 0 || exponent > 4) throw new IllegalArgumentException("exponent must be 0..4");
    }

    long minorUnitsPerMajor() {
        long scale = 1;
        for (int i = 0; i < exponent; i++) scale *= 10;
        return scale;
    }
}

/** Integer minor units plus a currency. Never a float. Trimmed copy of the Phase 10 type. */
record Money(long minorUnits, Currency currency) {
    Money { Objects.requireNonNull(currency, "currency"); }

    static Money ofMinor(long minorUnits, Currency currency) { return new Money(minorUnits, currency); }

    Money plus(Money other) {
        if (!currency.equals(other.currency)) throw new IllegalArgumentException("currency mismatch");
        return new Money(Math.addExact(minorUnits, other.minorUnits), currency);
    }

    @Override public String toString() {
        long scale = currency.minorUnitsPerMajor();
        long whole = Math.abs(minorUnits) / scale, frac = Math.abs(minorUnits) % scale;
        String sign = minorUnits < 0 ? "-" : "";
        if (currency.exponent() == 0) return sign + whole + " " + currency.code();
        return String.format("%s%d.%0" + currency.exponent() + "d %s", sign, whole, frac, currency.code());
    }
}

/** Composite key: order by timestamp, then by a sequence number so two postings in the same millisecond stay distinct. */
record TxKey(long timestampMillis, long sequence) implements Comparable<TxKey> {
    static TxKey lowest(long timestampMillis) { return new TxKey(timestampMillis, Long.MIN_VALUE); }
    static TxKey highest(long timestampMillis) { return new TxKey(timestampMillis, Long.MAX_VALUE); }

    @Override public int compareTo(TxKey o) {
        int c = Long.compare(timestampMillis, o.timestampMillis);
        return c != 0 ? c : Long.compare(sequence, o.sequence);
    }
}

/** A posting in the index: the key it is filed under plus what was paid. */
record Transaction(String id, TxKey key, Money amount) {}

/** One key/value pair returned by traversals and range queries. */
record Entry<K, V>(K key, V value) {}

/**
 * Unbalanced binary search tree used as an ordered map.
 *
 * Invariant: for every node n, every key in n.left is strictly less than n.key and every key in
 * n.right is strictly greater. Keys are unique; put on an existing key replaces the value.
 * Every operation is O(h) where h is the height, which is O(log n) for random insert order and
 * O(n) for sorted insert order. The lesson measures both.
 */
final class BinarySearchTree<K extends Comparable<K>, V> {

    static final class Node<K, V> {
        K key;
        V value;
        Node<K, V> left, right;
        Node(K key, V value) { this.key = key; this.value = value; }
    }

    private Node<K, V> root;
    private int size;
    private long comparisons; // instrumentation: how many key comparisons the tree has done

    int size() { return size; }
    boolean isEmpty() { return size == 0; }
    long comparisons() { return comparisons; }
    void resetComparisons() { comparisons = 0; }

    private int compare(K a, K b) {
        comparisons++;
        return a.compareTo(b);
    }

    /** Insert or replace. Returns the previous value or null. Iterative so a degenerate tree cannot overflow the stack. */
    V put(K key, V value) {
        Objects.requireNonNull(key, "key");
        if (root == null) {
            root = new Node<>(key, value);
            size = 1;
            return null;
        }
        Node<K, V> cur = root;
        while (true) {
            int c = compare(key, cur.key);
            if (c == 0) {
                V old = cur.value;
                cur.value = value;
                return old;
            }
            if (c < 0) {
                if (cur.left == null) { cur.left = new Node<>(key, value); size++; return null; }
                cur = cur.left;
            } else {
                if (cur.right == null) { cur.right = new Node<>(key, value); size++; return null; }
                cur = cur.right;
            }
        }
    }

    /** Exact lookup: follow one path from the root. O(h). */
    V get(K key) {
        Node<K, V> cur = root;
        while (cur != null) {
            int c = compare(key, cur.key);
            if (c == 0) return cur.value;
            cur = c < 0 ? cur.left : cur.right;
        }
        return null;
    }

    boolean containsKey(K key) { return get(key) != null; }

    /** Smallest key, or null when empty: walk left from the root. */
    K minKey() {
        if (root == null) return null;
        Node<K, V> n = root;
        while (n.left != null) n = n.left;
        return n.key;
    }

    /** Largest key, or null when empty: walk right from the root. */
    K maxKey() {
        if (root == null) return null;
        Node<K, V> n = root;
        while (n.right != null) n = n.right;
        return n.key;
    }

    /** Largest key less than or equal to the argument, or null. */
    K floorKey(K key) {
        Node<K, V> cur = root;
        K best = null;
        while (cur != null) {
            int c = compare(key, cur.key);
            if (c == 0) return cur.key;
            if (c < 0) cur = cur.left;
            else { best = cur.key; cur = cur.right; }
        }
        return best;
    }

    /** Smallest key greater than or equal to the argument, or null. */
    K ceilingKey(K key) {
        Node<K, V> cur = root;
        K best = null;
        while (cur != null) {
            int c = compare(key, cur.key);
            if (c == 0) return cur.key;
            if (c > 0) cur = cur.right;
            else { best = cur.key; cur = cur.left; }
        }
        return best;
    }

    /**
     * Delete by key. Returns the removed value or null when absent.
     * Case 0/1 child: splice the child into the parent's slot.
     * Case 2 children: copy the in-order successor (min of the right subtree) into this node,
     * then delete the successor node, which has at most one child by construction.
     */
    V remove(K key) {
        Node<K, V> parent = null, cur = root;
        while (cur != null) {
            int c = compare(key, cur.key);
            if (c == 0) break;
            parent = cur;
            cur = c < 0 ? cur.left : cur.right;
        }
        if (cur == null) return null;
        V removed = cur.value;

        if (cur.left != null && cur.right != null) {
            // find successor and its parent
            Node<K, V> succParent = cur, succ = cur.right;
            while (succ.left != null) { succParent = succ; succ = succ.left; }
            cur.key = succ.key;
            cur.value = succ.value;
            // detach successor: it has no left child, so splice in its right child
            if (succParent == cur) succParent.right = succ.right;
            else succParent.left = succ.right;
        } else {
            Node<K, V> child = cur.left != null ? cur.left : cur.right;
            if (parent == null) root = child;
            else if (parent.left == cur) parent.left = child;
            else parent.right = child;
        }
        size--;
        return removed;
    }

    /** All entries in ascending key order. Iterative in-order traversal with an explicit stack. O(n). */
    List<Entry<K, V>> inOrder() {
        List<Entry<K, V>> out = new ArrayList<>(size);
        Deque<Node<K, V>> stack = new ArrayDeque<>();
        Node<K, V> cur = root;
        while (cur != null || !stack.isEmpty()) {
            while (cur != null) { stack.push(cur); cur = cur.left; }
            cur = stack.pop();
            out.add(new Entry<>(cur.key, cur.value));
            cur = cur.right;
        }
        return out;
    }

    /**
     * All entries with lo <= key <= hi, ascending. O(h + k) where k is the number of results:
     * subtrees that cannot contain a key in the range are never entered.
     */
    List<Entry<K, V>> rangeQuery(K lo, K hi) {
        if (compare(lo, hi) > 0) throw new IllegalArgumentException("lo must not exceed hi");
        List<Entry<K, V>> out = new ArrayList<>();
        Deque<Node<K, V>> stack = new ArrayDeque<>();
        Node<K, V> cur = root;
        while (cur != null || !stack.isEmpty()) {
            while (cur != null) {
                stack.push(cur);
                // prune: nothing in cur.left can be >= lo if cur.key < lo
                cur = compare(cur.key, lo) < 0 ? null : cur.left;
            }
            cur = stack.pop();
            int vsLo = compare(cur.key, lo), vsHi = compare(cur.key, hi);
            if (vsHi > 0) return out; // every later in-order key is larger still
            if (vsLo >= 0) out.add(new Entry<>(cur.key, cur.value));
            cur = cur.right;
        }
        return out;
    }

    /** Number of edges on the longest root-to-leaf path; -1 for an empty tree. Iterative level-order walk. */
    int height() {
        if (root == null) return -1;
        int h = -1;
        Deque<Node<K, V>> level = new ArrayDeque<>();
        level.add(root);
        while (!level.isEmpty()) {
            h++;
            for (int i = level.size(); i > 0; i--) {
                Node<K, V> n = level.poll();
                if (n.left != null) level.add(n.left);
                if (n.right != null) level.add(n.right);
            }
        }
        return h;
    }

    /** Verifies the ordering invariant and the size counter. Used by tests after every mutation. */
    boolean checkInvariant() {
        List<Entry<K, V>> all = inOrder();
        if (all.size() != size) return false;
        for (int i = 1; i < all.size(); i++) {
            if (all.get(i - 1).key().compareTo(all.get(i).key()) >= 0) return false;
        }
        return true;
    }
}

/** Index of transactions by (timestamp, sequence) so that "everything between t1 and t2" is one range query. */
final class TransactionIndex {
    private final BinarySearchTree<TxKey, Transaction> tree = new BinarySearchTree<>();
    private long nextSequence;

    Transaction record(String id, long timestampMillis, Money amount) {
        Transaction tx = new Transaction(id, new TxKey(timestampMillis, nextSequence++), amount);
        tree.put(tx.key(), tx);
        return tx;
    }

    boolean remove(Transaction tx) { return tree.remove(tx.key()) != null; }

    /** Every transaction with fromInclusive <= timestamp <= toInclusive, oldest first. */
    List<Transaction> between(long fromInclusive, long toInclusive) {
        List<Transaction> out = new ArrayList<>();
        for (Entry<TxKey, Transaction> e : tree.rangeQuery(TxKey.lowest(fromInclusive), TxKey.highest(toInclusive))) {
            out.add(e.value());
        }
        return out;
    }

    Money sumBetween(long fromInclusive, long toInclusive, Currency currency) {
        Money total = Money.ofMinor(0, currency);
        for (Transaction tx : between(fromInclusive, toInclusive)) total = total.plus(tx.amount());
        return total;
    }

    int size() { return tree.size(); }
    int height() { return tree.height(); }
    BinarySearchTree<TxKey, Transaction> tree() { return tree; }
}

public final class Main {
    static final long T0 = 1_700_000_000_000L; // 2023-11-14T22:13:20Z, a fixed epoch for determinism
    static final long MINUTE = 60_000L;

    static void demoOrderedMap() {
        System.out.println("== Insert, search, delete with successor ==");
        BinarySearchTree<Integer, String> t = new BinarySearchTree<>();
        for (int k : new int[] {50, 30, 70, 20, 40, 60, 80, 35, 45}) t.put(k, "v" + k);
        System.out.println("in-order keys: " + t.inOrder().stream().map(e -> e.key().toString()).toList());
        System.out.println("size=" + t.size() + " height=" + t.height() + " min=" + t.minKey() + " max=" + t.maxKey());
        System.out.println("floor(42)=" + t.floorKey(42) + " ceiling(42)=" + t.ceilingKey(42) + " get(45)=" + t.get(45));
        t.remove(30); // two children: successor is 35
        System.out.println("after remove(30), in-order: " + t.inOrder().stream().map(e -> e.key().toString()).toList()
                + " invariant=" + t.checkInvariant());
        t.remove(50); // root with two children: successor is 60
        System.out.println("after remove(50), in-order: " + t.inOrder().stream().map(e -> e.key().toString()).toList()
                + " height=" + t.height());
        System.out.println();
    }

    static void demoRangeQuery() {
        System.out.println("== Range query over transactions by timestamp ==");
        TransactionIndex idx = new TransactionIndex();
        Random rnd = new Random(42);
        // 12 transactions spread over 90 minutes, inserted in arrival order (not timestamp order)
        long[] offsets = {5, 71, 12, 44, 44, 88, 3, 60, 29, 15, 52, 44};
        for (int i = 0; i < offsets.length; i++) {
            long cents = 100 + rnd.nextInt(9_900);
            idx.record("tx-" + (i + 1), T0 + offsets[i] * MINUTE, Money.ofMinor(cents, Currency.USD));
        }
        System.out.println("indexed " + idx.size() + " transactions, tree height " + idx.height());
        long from = T0 + 10 * MINUTE, to = T0 + 45 * MINUTE;
        List<Transaction> window = idx.between(from, to);
        System.out.println("between +10min and +45min (inclusive): " + window.size() + " transactions");
        for (Transaction tx : window) {
            System.out.printf("  %-6s +%2d min  %s%n", tx.id(), (tx.key().timestampMillis() - T0) / MINUTE, tx.amount());
        }
        System.out.println("sum in window = " + idx.sumBetween(from, to, Currency.USD));
        System.out.println("three transactions share +44 min and all three are kept (composite key)");
        System.out.println();
    }

    static void demoDegenerate() {
        System.out.println("== Why unbalanced trees need balancing ==");
        int n = 4_096;
        BinarySearchTree<Integer, Integer> sorted = new BinarySearchTree<>();
        for (int i = 0; i < n; i++) sorted.put(i, i);
        sorted.resetComparisons();
        for (int i = 0; i < n; i++) sorted.get(i);
        long sortedCmp = sorted.comparisons();

        BinarySearchTree<Integer, Integer> shuffled = new BinarySearchTree<>();
        int[] keys = new int[n];
        for (int i = 0; i < n; i++) keys[i] = i;
        Random rnd = new Random(7);
        for (int i = n - 1; i > 0; i--) { int j = rnd.nextInt(i + 1); int tmp = keys[i]; keys[i] = keys[j]; keys[j] = tmp; }
        for (int k : keys) shuffled.put(k, k);
        shuffled.resetComparisons();
        for (int i = 0; i < n; i++) shuffled.get(i);
        long shuffledCmp = shuffled.comparisons();

        System.out.printf("%d keys inserted in sorted order:   height %d, %.1f comparisons per lookup%n",
                n, sorted.height(), sortedCmp / (double) n);
        System.out.printf("%d keys inserted in shuffled order: height %d, %.1f comparisons per lookup%n",
                n, shuffled.height(), shuffledCmp / (double) n);
        System.out.printf("ideal balanced height for %d keys: %d%n", n, 31 - Integer.numberOfLeadingZeros(n));
        System.out.println("transactions arrive in timestamp order, which is the worst case; Phase 3 fixes this with self-balancing trees");
    }

    public static void main(String[] args) {
        demoOrderedMap();
        demoRangeQuery();
        demoDegenerate();
    }
}
