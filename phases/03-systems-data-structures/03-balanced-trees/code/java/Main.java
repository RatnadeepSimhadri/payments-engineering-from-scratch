// Lesson: phases/03-systems-data-structures/03-balanced-trees/docs/en.md
// Balanced Trees: AVL and Red-Black
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: Adelson-Velsky & Landis (1962); Guibas & Sedgewick (1978), "A Dichromatic Framework for Balanced Trees";
//          Cormen, Leiserson, Rivest, Stein, Introduction to Algorithms, ch. 13 (RB-INSERT-FIXUP). java.util.TreeMap source.
// Java 21, standard library only.

import java.util.ArrayList;
import java.util.Collections;
import java.util.List;
import java.util.Random;

/**
 * Unbalanced binary search tree, kept only to show what balancing prevents.
 * Inserting sorted keys degenerates it into a linked list of height n.
 */
final class NaiveBst<K extends Comparable<K>, V> {
    private static final class Node<K, V> {
        K key; V value; Node<K, V> left, right;
        Node(K key, V value) { this.key = key; this.value = value; }
    }

    private Node<K, V> root;
    private int size;

    void put(K key, V value) {
        if (root == null) { root = new Node<>(key, value); size++; return; }
        Node<K, V> cur = root;
        while (true) {
            int c = key.compareTo(cur.key);
            if (c == 0) { cur.value = value; return; }
            Node<K, V> next = c < 0 ? cur.left : cur.right;
            if (next == null) {
                if (c < 0) cur.left = new Node<>(key, value); else cur.right = new Node<>(key, value);
                size++;
                return;
            }
            cur = next;
        }
    }

    int size() { return size; }

    int height() { return height(root); }

    private int height(Node<K, V> n) {
        return n == null ? 0 : 1 + Math.max(height(n.left), height(n.right));
    }
}

/**
 * AVL tree: for every node, |height(left) - height(right)| <= 1. Height is stored in each node and
 * recomputed on the way back up from every insert or delete; at most two rotations restore the
 * invariant after an insert, and O(log n) after a delete.
 */
final class AvlTree<K extends Comparable<K>, V> {
    static final class Node<K, V> {
        K key; V value; int height = 1; Node<K, V> left, right;
        Node(K key, V value) { this.key = key; this.value = value; }
    }

    private Node<K, V> root;
    private int size;
    private long rotations;

    static <K, V> int height(Node<K, V> n) { return n == null ? 0 : n.height; }

    static <K, V> int balanceFactor(Node<K, V> n) { return n == null ? 0 : height(n.left) - height(n.right); }

    private static <K, V> void update(Node<K, V> n) { n.height = 1 + Math.max(height(n.left), height(n.right)); }

    /** Right rotation around y: y's left child x becomes the subtree root; x's right subtree moves under y. */
    Node<K, V> rotateRight(Node<K, V> y) {
        Node<K, V> x = y.left;
        y.left = x.right;
        x.right = y;
        update(y);
        update(x);
        rotations++;
        return x;
    }

    /** Left rotation around x: x's right child y becomes the subtree root; y's left subtree moves under x. */
    Node<K, V> rotateLeft(Node<K, V> x) {
        Node<K, V> y = x.right;
        x.right = y.left;
        y.left = x;
        update(x);
        update(y);
        rotations++;
        return y;
    }

    /** Restore |balance| <= 1 at n. Four cases: LL and RR need one rotation, LR and RL need two. */
    private Node<K, V> rebalance(Node<K, V> n) {
        update(n);
        int bf = balanceFactor(n);
        if (bf > 1) {
            if (balanceFactor(n.left) < 0) n.left = rotateLeft(n.left);   // LR: straighten first
            return rotateRight(n);                                        // LL
        }
        if (bf < -1) {
            if (balanceFactor(n.right) > 0) n.right = rotateRight(n.right); // RL: straighten first
            return rotateLeft(n);                                           // RR
        }
        return n;
    }

    void put(K key, V value) { root = put(root, key, value); }

    private Node<K, V> put(Node<K, V> n, K key, V value) {
        if (n == null) { size++; return new Node<>(key, value); }
        int c = key.compareTo(n.key);
        if (c < 0) n.left = put(n.left, key, value);
        else if (c > 0) n.right = put(n.right, key, value);
        else { n.value = value; return n; }
        return rebalance(n);
    }

    V get(K key) {
        Node<K, V> cur = root;
        while (cur != null) {
            int c = key.compareTo(cur.key);
            if (c == 0) return cur.value;
            cur = c < 0 ? cur.left : cur.right;
        }
        return null;
    }

    boolean containsKey(K key) { return get(key) != null; }

    void remove(K key) { root = remove(root, key); }

    private Node<K, V> remove(Node<K, V> n, K key) {
        if (n == null) return null;
        int c = key.compareTo(n.key);
        if (c < 0) n.left = remove(n.left, key);
        else if (c > 0) n.right = remove(n.right, key);
        else {
            if (n.left == null || n.right == null) {          // zero or one child: splice out
                size--;
                return n.left == null ? n.right : n.left;
            }
            Node<K, V> successor = n.right;                   // two children: replace with in-order successor
            while (successor.left != null) successor = successor.left;
            n.key = successor.key;
            n.value = successor.value;
            n.right = remove(n.right, successor.key);        // the successor has no left child, so this splices
        }
        return rebalance(n);
    }

    /** Smallest key >= key, or null: the operation a hash map cannot do. */
    K ceilingKey(K key) {
        Node<K, V> cur = root; K best = null;
        while (cur != null) {
            int c = key.compareTo(cur.key);
            if (c == 0) return cur.key;
            if (c < 0) { best = cur.key; cur = cur.left; } else cur = cur.right;
        }
        return best;
    }

    /** Keys in [lo, hi], ascending, visiting only the subtrees that can contain them. */
    List<K> range(K lo, K hi) {
        List<K> out = new ArrayList<>();
        range(root, lo, hi, out);
        return out;
    }

    private void range(Node<K, V> n, K lo, K hi, List<K> out) {
        if (n == null) return;
        if (lo.compareTo(n.key) < 0) range(n.left, lo, hi, out);
        if (lo.compareTo(n.key) <= 0 && hi.compareTo(n.key) >= 0) out.add(n.key);
        if (hi.compareTo(n.key) > 0) range(n.right, lo, hi, out);
    }

    List<K> keys() {
        List<K> out = new ArrayList<>(size);
        inorder(root, out);
        return out;
    }

    private void inorder(Node<K, V> n, List<K> out) {
        if (n == null) return;
        inorder(n.left, out);
        out.add(n.key);
        inorder(n.right, out);
    }

    int size() { return size; }
    int height() { return height(root); }
    long rotations() { return rotations; }

    /** Upper bound from the AVL analysis: height < 1.4405 * log2(n + 2) - 0.3277. */
    static double heightBound(int n) { return 1.4405 * Math.log(n + 2) / Math.log(2) - 0.3277; }

    /** Every node: BST order, stored height correct, |balance| <= 1. Throws on the first violation. */
    boolean checkInvariants() {
        verify(root, null, null);
        return true;
    }

    static <K extends Comparable<K>, V> int verify(Node<K, V> n, K lo, K hi) {
        if (n == null) return 0;
        if (lo != null && n.key.compareTo(lo) <= 0) throw new IllegalStateException("BST order violated at " + n.key);
        if (hi != null && n.key.compareTo(hi) >= 0) throw new IllegalStateException("BST order violated at " + n.key);
        int hl = verify(n.left, lo, n.key);
        int hr = verify(n.right, n.key, hi);
        if (n.height != 1 + Math.max(hl, hr)) throw new IllegalStateException("stale height at " + n.key);
        if (Math.abs(hl - hr) > 1) throw new IllegalStateException("AVL balance violated at " + n.key);
        return n.height;
    }
}

/**
 * Red-black tree with the CLRS insert fix-up. Invariants: every node is red or black; the root is
 * black; null leaves are black; a red node has no red child; every path from a node to its null
 * descendants has the same number of black nodes. Height <= 2 * log2(n + 1).
 */
final class RedBlackTree<K extends Comparable<K>, V> {
    static final boolean RED = true, BLACK = false;

    static final class Node<K, V> {
        K key; V value; boolean color = RED; Node<K, V> left, right, parent;
        Node(K key, V value, Node<K, V> parent) { this.key = key; this.value = value; this.parent = parent; }
    }

    private Node<K, V> root;
    private int size;
    private long rotations, recolorings;

    static <K, V> boolean isRed(Node<K, V> n) { return n != null && n.color == RED; }

    private void rotateLeft(Node<K, V> x) {
        Node<K, V> y = x.right;
        x.right = y.left;
        if (y.left != null) y.left.parent = x;
        y.parent = x.parent;
        if (x.parent == null) root = y;
        else if (x == x.parent.left) x.parent.left = y;
        else x.parent.right = y;
        y.left = x;
        x.parent = y;
        rotations++;
    }

    private void rotateRight(Node<K, V> y) {
        Node<K, V> x = y.left;
        y.left = x.right;
        if (x.right != null) x.right.parent = y;
        x.parent = y.parent;
        if (y.parent == null) root = x;
        else if (y == y.parent.right) y.parent.right = x;
        else y.parent.left = x;
        x.right = y;
        y.parent = x;
        rotations++;
    }

    void put(K key, V value) {
        Node<K, V> parent = null, cur = root;
        int c = 0;
        while (cur != null) {
            parent = cur;
            c = key.compareTo(cur.key);
            if (c == 0) { cur.value = value; return; }
            cur = c < 0 ? cur.left : cur.right;
        }
        Node<K, V> z = new Node<>(key, value, parent);
        if (parent == null) root = z;
        else if (c < 0) parent.left = z;
        else parent.right = z;
        size++;
        insertFixup(z);
    }

    /**
     * z is red and may have a red parent. Walk up while that is true. Three cases on each side:
     * (1) uncle red: recolor and move up; (2) z is an inner grandchild: rotate parent to make (3);
     * (3) z is an outer grandchild: recolor parent and grandparent, rotate grandparent. Done.
     */
    private void insertFixup(Node<K, V> z) {
        while (isRed(z.parent)) {
            Node<K, V> p = z.parent, g = p.parent;          // g exists: a red parent is never the root
            if (p == g.left) {
                Node<K, V> uncle = g.right;
                if (isRed(uncle)) {                         // case 1
                    p.color = BLACK; uncle.color = BLACK; g.color = RED; recolorings += 3;
                    z = g;
                } else {
                    if (z == p.right) { z = p; rotateLeft(z); p = z.parent; }   // case 2 -> 3
                    p.color = BLACK; g.color = RED; recolorings += 2;             // case 3
                    rotateRight(g);
                }
            } else {
                Node<K, V> uncle = g.left;
                if (isRed(uncle)) {
                    p.color = BLACK; uncle.color = BLACK; g.color = RED; recolorings += 3;
                    z = g;
                } else {
                    if (z == p.left) { z = p; rotateRight(z); p = z.parent; }
                    p.color = BLACK; g.color = RED; recolorings += 2;
                    rotateLeft(g);
                }
            }
        }
        if (root.color == RED) { root.color = BLACK; recolorings++; }
    }

    V get(K key) {
        Node<K, V> cur = root;
        while (cur != null) {
            int c = key.compareTo(cur.key);
            if (c == 0) return cur.value;
            cur = c < 0 ? cur.left : cur.right;
        }
        return null;
    }

    List<K> keys() {
        List<K> out = new ArrayList<>(size);
        inorder(root, out);
        return out;
    }

    private void inorder(Node<K, V> n, List<K> out) {
        if (n == null) return;
        inorder(n.left, out);
        out.add(n.key);
        inorder(n.right, out);
    }

    int size() { return size; }
    long rotations() { return rotations; }
    long recolorings() { return recolorings; }

    int height() { return height(root); }

    private int height(Node<K, V> n) { return n == null ? 0 : 1 + Math.max(height(n.left), height(n.right)); }

    /** Number of black nodes on any root-to-null path (they are all equal when the tree is valid). */
    int blackHeight() {
        int h = 0;
        for (Node<K, V> n = root; n != null; n = n.left) if (n.color == BLACK) h++;
        return h;
    }

    /** Upper bound from the red-black analysis: height <= 2 * log2(n + 1). */
    static double heightBound(int n) { return 2.0 * Math.log(n + 1) / Math.log(2); }

    /** BST order, root black, no red-red edge, equal black height on every path. Throws on the first violation. */
    boolean checkInvariants() {
        if (isRed(root)) throw new IllegalStateException("root is red");
        verify(root, null, null);
        return true;
    }

    private int verify(Node<K, V> n, K lo, K hi) {
        if (n == null) return 1;                               // null leaves count as one black
        if (lo != null && n.key.compareTo(lo) <= 0) throw new IllegalStateException("BST order violated at " + n.key);
        if (hi != null && n.key.compareTo(hi) >= 0) throw new IllegalStateException("BST order violated at " + n.key);
        if (isRed(n) && (isRed(n.left) || isRed(n.right))) throw new IllegalStateException("red node with red child at " + n.key);
        if (n.left != null && n.left.parent != n) throw new IllegalStateException("broken parent link at " + n.left.key);
        if (n.right != null && n.right.parent != n) throw new IllegalStateException("broken parent link at " + n.right.key);
        int bl = verify(n.left, lo, n.key);
        int br = verify(n.right, n.key, hi);
        if (bl != br) throw new IllegalStateException("black height differs at " + n.key + ": " + bl + " vs " + br);
        return bl + (n.color == BLACK ? 1 : 0);
    }
}

public final class Main {

    static int log2(int n) { return 32 - Integer.numberOfLeadingZeros(n); }

    public static void main(String[] args) {
        int n = 100_000;

        System.out.println("== Sorted inserts: what a plain BST does with timestamps ==");
        NaiveBst<Integer, String> naive = new NaiveBst<>();
        for (int i = 0; i < 2_000; i++) naive.put(i, "auth");
        System.out.printf("naive BST, 2000 sorted keys: height=%d (a linked list; log2(2000)=%d)%n", naive.height(), log2(2000));

        System.out.println();
        System.out.println("== AVL vs red-black on " + n + " sorted keys (expiry timestamps arriving in order) ==");
        AvlTree<Integer, String> avl = new AvlTree<>();
        RedBlackTree<Integer, String> rb = new RedBlackTree<>();
        for (int i = 0; i < n; i++) { avl.put(i, "auth"); rb.put(i, "auth"); }
        avl.checkInvariants();
        rb.checkInvariants();
        System.out.printf("AVL        height=%d  bound=%.1f  rotations=%d%n", avl.height(), AvlTree.heightBound(n), avl.rotations());
        System.out.printf("red-black  height=%d  bound=%.1f  rotations=%d  recolorings=%d  black-height=%d%n",
                rb.height(), RedBlackTree.heightBound(n), rb.rotations(), rb.recolorings(), rb.blackHeight());

        System.out.println();
        System.out.println("== Same keys, random order (seeded) ==");
        List<Integer> shuffled = new ArrayList<>();
        for (int i = 0; i < n; i++) shuffled.add(i);
        Collections.shuffle(shuffled, new Random(2024));
        AvlTree<Integer, String> avl2 = new AvlTree<>();
        RedBlackTree<Integer, String> rb2 = new RedBlackTree<>();
        for (int k : shuffled) { avl2.put(k, "auth"); rb2.put(k, "auth"); }
        avl2.checkInvariants();
        rb2.checkInvariants();
        System.out.printf("AVL        height=%d  rotations=%d%n", avl2.height(), avl2.rotations());
        System.out.printf("red-black  height=%d  rotations=%d  recolorings=%d%n", rb2.height(), rb2.rotations(), rb2.recolorings());

        System.out.println();
        System.out.println("== The operation a hash map cannot do: expiries between two instants ==");
        AvlTree<Long, String> expiries = new AvlTree<>();
        long t0 = 1_700_000_000_000L;
        Random rng = new Random(7);
        for (int i = 0; i < 20; i++) expiries.put(t0 + rng.nextInt(60_000), "auth_" + i);
        System.out.println("expiries in [t0+10s, t0+20s): " + expiries.range(t0 + 10_000, t0 + 19_999).stream().map(t -> t - t0).toList());
        System.out.println("first expiry at or after t0+30s: +" + (expiries.ceilingKey(t0 + 30_000) - t0) + " ms");

        System.out.println();
        System.out.println("== Deletes keep the AVL invariant ==");
        for (int i = 0; i < n; i += 2) avl.remove(i);
        avl.checkInvariants();
        System.out.printf("after removing every even key: size=%d height=%d, invariants hold%n", avl.size(), avl.height());
    }
}
