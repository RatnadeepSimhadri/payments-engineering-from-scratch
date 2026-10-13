// Lesson: phases/01-complexity-and-core-structures/04-linked-lists/docs/en.md
// Linked Lists: Singly, Doubly, and Intrusive
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: Knuth, TAOCP Vol. 1, 2.2.3-2.2.5 (linked allocation, circular and doubly linked lists);
//          Linux kernel include/linux/list.h (intrusive list_head). Java 21, standard library only.

import java.util.Iterator;
import java.util.NoSuchElementException;
import java.util.Objects;
import java.util.Random;

/**
 * Singly linked list with head and tail pointers.
 * O(1): addFirst, addLast, removeFirst. O(n): get, remove by value, removeLast (no back pointer).
 */
final class SinglyLinkedList<T> implements Iterable<T> {
    private static final class Node<T> {
        T value;
        Node<T> next;
        Node(T value) { this.value = value; }
    }

    private Node<T> head;
    private Node<T> tail;
    private int size;

    int size() { return size; }
    boolean isEmpty() { return size == 0; }

    void addFirst(T value) {
        Node<T> n = new Node<>(value);
        n.next = head;
        head = n;
        if (tail == null) tail = n;
        size++;
    }

    void addLast(T value) {
        Node<T> n = new Node<>(value);
        if (tail == null) { head = tail = n; }
        else { tail.next = n; tail = n; }
        size++;
    }

    T removeFirst() {
        if (head == null) throw new NoSuchElementException("empty");
        T v = head.value;
        head = head.next;
        if (head == null) tail = null;
        size--;
        return v;
    }

    /** O(n): walk from the head. There is no shortcut; this is the operation arrays do in O(1). */
    T get(int index) {
        Objects.checkIndex(index, size);
        Node<T> n = head;
        for (int i = 0; i < index; i++) n = n.next;
        return n.value;
    }

    /** O(n): find the node *before* the match so it can be unlinked. Singly linked lists cannot unlink a node given only that node. */
    boolean removeFirstOccurrence(T value) {
        Node<T> prev = null, cur = head;
        while (cur != null) {
            if (Objects.equals(cur.value, value)) {
                if (prev == null) head = cur.next; else prev.next = cur.next;
                if (cur == tail) tail = prev;
                size--;
                return true;
            }
            prev = cur;
            cur = cur.next;
        }
        return false;
    }

    /** In-place reversal with three pointers; O(n) time, O(1) extra space. */
    void reverse() {
        Node<T> prev = null, cur = head;
        tail = head;
        while (cur != null) {
            Node<T> next = cur.next;
            cur.next = prev;
            prev = cur;
            cur = next;
        }
        head = prev;
    }

    @Override public Iterator<T> iterator() {
        return new Iterator<>() {
            Node<T> cur = head;
            @Override public boolean hasNext() { return cur != null; }
            @Override public T next() {
                if (cur == null) throw new NoSuchElementException();
                T v = cur.value; cur = cur.next; return v;
            }
        };
    }

    @Override public String toString() {
        StringBuilder sb = new StringBuilder("[");
        for (Node<T> n = head; n != null; n = n.next) { sb.append(n.value); if (n.next != null) sb.append(", "); }
        return sb.append("]").toString();
    }
}

/**
 * Doubly linked list with a sentinel node. The sentinel's next is the first element and its prev is the last,
 * so there is never a null to check and insert/unlink are the same code at every position.
 * Handing out Node references makes unlink(node) and moveToFront(node) O(1): the basis of every LRU cache.
 */
final class DoublyLinkedList<T> implements Iterable<T> {
    static final class Node<T> {
        T value;
        Node<T> prev, next;
        DoublyLinkedList<T> owner;       // which list this node is linked into; null once unlinked
        Node(T value) { this.value = value; }
        T value() { return value; }
    }

    private final Node<T> sentinel = new Node<>(null);
    private int size;

    DoublyLinkedList() { sentinel.prev = sentinel.next = sentinel; }

    int size() { return size; }
    boolean isEmpty() { return size == 0; }
    Node<T> first() { return size == 0 ? null : sentinel.next; }
    Node<T> last() { return size == 0 ? null : sentinel.prev; }

    /** Link a fresh node holding `value` immediately before `succ`. Works for every position including the ends. */
    private Node<T> linkBefore(T value, Node<T> succ) {
        Node<T> n = new Node<>(value);
        Node<T> pred = succ.prev;
        n.prev = pred;
        n.next = succ;
        pred.next = n;
        succ.prev = n;
        n.owner = this;
        size++;
        return n;
    }

    Node<T> addFirst(T value) { return linkBefore(value, sentinel.next); }
    Node<T> addLast(T value) { return linkBefore(value, sentinel); }

    Node<T> insertBefore(Node<T> node, T value) {
        requireOwned(node);
        return linkBefore(value, node);
    }

    /** O(1): the node knows both neighbours, so no search is needed. */
    T unlink(Node<T> node) {
        requireOwned(node);
        node.prev.next = node.next;
        node.next.prev = node.prev;
        node.prev = node.next = null;
        node.owner = null;
        size--;
        return node.value;
    }

    T removeFirst() {
        if (size == 0) throw new NoSuchElementException("empty");
        return unlink(sentinel.next);
    }

    T removeLast() {
        if (size == 0) throw new NoSuchElementException("empty");
        return unlink(sentinel.prev);
    }

    /** O(1) relink without allocation: the LRU "touch" operation. */
    void moveToFront(Node<T> node) {
        requireOwned(node);
        if (sentinel.next == node) return;
        node.prev.next = node.next;
        node.next.prev = node.prev;
        node.prev = sentinel;
        node.next = sentinel.next;
        sentinel.next.prev = node;
        sentinel.next = node;
    }

    /** O(1) regardless of either list's length: move every node of `other` to the end of this list, leaving `other` empty. */
    void spliceAll(DoublyLinkedList<T> other) {
        if (other == this) throw new IllegalArgumentException("cannot splice a list into itself");
        if (other.size == 0) return;
        Node<T> otherFirst = other.sentinel.next;
        Node<T> otherLast = other.sentinel.prev;
        Node<T> myLast = sentinel.prev;
        myLast.next = otherFirst;
        otherFirst.prev = myLast;
        otherLast.next = sentinel;
        sentinel.prev = otherLast;
        // Ownership must follow the nodes: this is the one O(n) walk the splice cannot avoid if nodes track their owner.
        for (Node<T> n = otherFirst; n != sentinel; n = n.next) n.owner = this;
        size += other.size;
        other.sentinel.next = other.sentinel.prev = other.sentinel;
        other.size = 0;
    }

    private void requireOwned(Node<T> node) {
        if (node == null || node.owner != this) throw new IllegalArgumentException("node is not linked into this list");
    }

    /** Structural check used by the tests: forward and backward walks agree with each other and with size. */
    boolean invariantHolds() {
        int forward = 0;
        for (Node<T> n = sentinel.next; n != sentinel; n = n.next) {
            if (n.next.prev != n || n.owner != this) return false;
            forward++;
        }
        int backward = 0;
        for (Node<T> n = sentinel.prev; n != sentinel; n = n.prev) backward++;
        return forward == size && backward == size;
    }

    @Override public Iterator<T> iterator() {
        return new Iterator<>() {
            Node<T> cur = sentinel.next;
            @Override public boolean hasNext() { return cur != sentinel; }
            @Override public T next() {
                if (cur == sentinel) throw new NoSuchElementException();
                T v = cur.value; cur = cur.next; return v;
            }
        };
    }

    @Override public String toString() {
        StringBuilder sb = new StringBuilder("[");
        for (Node<T> n = sentinel.next; n != sentinel; n = n.next) { sb.append(n.value); if (n.next != sentinel) sb.append(", "); }
        return sb.append("]").toString();
    }
}

/**
 * Base class for elements of an intrusive list: the element carries its own links, so there is no separate
 * node object, no allocation on insert, and removal is O(1) given only the element. An element can be in
 * at most one IntrusiveList at a time; `owner` enforces that.
 */
abstract class IntrusiveNode {
    IntrusiveNode prev, next;
    IntrusiveList<?> owner;
    boolean isLinked() { return owner != null; }
}

/** Intrusive doubly linked list with a sentinel. Compare Linux's list_head. */
final class IntrusiveList<T extends IntrusiveNode> implements Iterable<T> {
    private static final class Sentinel extends IntrusiveNode {}

    private final IntrusiveNode sentinel = new Sentinel();
    private int size;

    IntrusiveList() { sentinel.prev = sentinel.next = sentinel; }

    int size() { return size; }
    boolean isEmpty() { return size == 0; }

    void addLast(T element) {
        if (element.owner != null) throw new IllegalStateException("element is already in a list");
        IntrusiveNode last = sentinel.prev;
        element.prev = last;
        element.next = sentinel;
        last.next = element;
        sentinel.prev = element;
        element.owner = this;
        size++;
    }

    /** O(1), no lookup: the element is its own handle. */
    void remove(T element) {
        if (element.owner != this) throw new IllegalArgumentException("element is not in this list");
        element.prev.next = element.next;
        element.next.prev = element.prev;
        element.prev = element.next = null;
        element.owner = null;
        size--;
    }

    /** O(1) membership test, because ownership is recorded in the element. */
    boolean contains(T element) { return element.owner == this; }

    /** Detach every element so it can be linked elsewhere. O(n): each element must forget its owner. */
    void clear() {
        IntrusiveNode cur = sentinel.next;
        while (cur != sentinel) {
            IntrusiveNode next = cur.next;
            cur.prev = cur.next = null;
            cur.owner = null;
            cur = next;
        }
        sentinel.prev = sentinel.next = sentinel;
        size = 0;
    }

    @SuppressWarnings("unchecked")
    T first() { return size == 0 ? null : (T) sentinel.next; }

    @SuppressWarnings("unchecked")
    @Override public Iterator<T> iterator() {
        return new Iterator<>() {
            IntrusiveNode cur = sentinel.next;
            @Override public boolean hasNext() { return cur != sentinel; }
            @Override public T next() {
                if (cur == sentinel) throw new NoSuchElementException();
                T v = (T) cur; cur = cur.next; return v;
            }
        };
    }
}

/** A domain object that lives in an intrusive list: a pending card authorization awaiting capture or expiry. */
final class PendingAuthorization extends IntrusiveNode {
    final String id;
    final long amountMinor;      // integer minor units, never a float (Phase 10, Lesson 01)
    final long expiresAtMillis;

    PendingAuthorization(String id, long amountMinor, long expiresAtMillis) {
        this.id = id; this.amountMinor = amountMinor; this.expiresAtMillis = expiresAtMillis;
    }

    @Override public String toString() { return id + "(" + amountMinor + ")"; }
}

/** A cell used by the traversal benchmark: one long, linked intrusively so allocation order and list order can differ. */
final class Cell extends IntrusiveNode {
    final long value;
    Cell(long value) { this.value = value; }
}

public final class Main {

    static long sumArray(long[] values) {
        long sum = 0;
        for (long v : values) sum += v;
        return sum;
    }

    static long sumList(IntrusiveList<Cell> list) {
        long sum = 0;
        for (Cell c : list) sum += c.value;
        return sum;
    }

    /** Build an intrusive list over pre-allocated cells, linking them in the given order. */
    static IntrusiveList<Cell> linkInOrder(Cell[] cells, int[] order) {
        IntrusiveList<Cell> list = new IntrusiveList<>();
        for (int i : order) list.addLast(cells[i]);
        return list;
    }

    static int[] identity(int n) {
        int[] order = new int[n];
        for (int i = 0; i < n; i++) order[i] = i;
        return order;
    }

    static int[] shuffled(int n, long seed) {
        int[] order = identity(n);
        Random rnd = new Random(seed);
        for (int i = n - 1; i > 0; i--) {
            int j = rnd.nextInt(i + 1);
            int t = order[i]; order[i] = order[j]; order[j] = t;
        }
        return order;
    }

    static long bestOf(int rounds, java.util.function.LongSupplier work) {
        long best = Long.MAX_VALUE;
        for (int r = 0; r < rounds; r++) {
            long t0 = System.nanoTime();
            work.getAsLong();
            best = Math.min(best, System.nanoTime() - t0);
        }
        return best;
    }

    public static void main(String[] args) {
        System.out.println("== Singly linked: O(1) at the head, O(n) everywhere else ==");
        SinglyLinkedList<String> singly = new SinglyLinkedList<>();
        singly.addLast("auth-1"); singly.addLast("auth-2"); singly.addLast("auth-3"); singly.addFirst("auth-0");
        System.out.println("list     = " + singly + " size=" + singly.size());
        System.out.println("get(2)   = " + singly.get(2) + "  (walked 2 links to find it)");
        singly.removeFirstOccurrence("auth-2");
        System.out.println("remove auth-2 by value -> " + singly + "  (walked from the head to find the predecessor)");
        singly.reverse();
        System.out.println("reverse  = " + singly);
        System.out.println();

        System.out.println("== Doubly linked with a sentinel: O(1) unlink given the node ==");
        DoublyLinkedList<String> lru = new DoublyLinkedList<>();
        DoublyLinkedList.Node<String> k1 = lru.addLast("idem-key-1");
        DoublyLinkedList.Node<String> k2 = lru.addLast("idem-key-2");
        DoublyLinkedList.Node<String> k3 = lru.addLast("idem-key-3");
        System.out.println("recency order (front = most recent): " + lru);
        lru.moveToFront(k3);
        System.out.println("touch key-3 -> " + lru);
        lru.moveToFront(k1);
        System.out.println("touch key-1 -> " + lru);
        System.out.println("evict least recent (removeLast) -> " + lru.removeLast() + ", now " + lru);
        System.out.println("unlink key-3 directly -> " + lru.unlink(k3) + ", now " + lru + " size=" + lru.size());
        System.out.println("k2 still linked? " + (k2.owner != null) + "; invariant holds? " + lru.invariantHolds());
        System.out.println();

        System.out.println("== Splice: merge two queues in O(1), not O(n) ==");
        DoublyLinkedList<String> shardA = new DoublyLinkedList<>();
        DoublyLinkedList<String> shardB = new DoublyLinkedList<>();
        for (int i = 1; i <= 3; i++) shardA.addLast("a" + i);
        for (int i = 1; i <= 4; i++) shardB.addLast("b" + i);
        shardA.spliceAll(shardB);
        System.out.println("A after splice = " + shardA + " size=" + shardA.size() + "; B = " + shardB + " size=" + shardB.size());
        System.out.println();

        System.out.println("== Intrusive list: the element is its own node ==");
        IntrusiveList<PendingAuthorization> pending = new IntrusiveList<>();
        PendingAuthorization a1 = new PendingAuthorization("auth-7f3a", 1999, 1_000);
        PendingAuthorization a2 = new PendingAuthorization("auth-9c21", 4500, 2_000);
        PendingAuthorization a3 = new PendingAuthorization("auth-0b77", 250, 3_000);
        pending.addLast(a1); pending.addLast(a2); pending.addLast(a3);
        StringBuilder sb = new StringBuilder();
        for (PendingAuthorization p : pending) sb.append(p).append(' ');
        System.out.println("pending: " + sb.toString().trim());
        System.out.println("capture auth-9c21: remove(a2) is O(1) with no search; linked before? " + pending.contains(a2));
        pending.remove(a2);
        sb.setLength(0);
        for (PendingAuthorization p : pending) sb.append(p).append(' ');
        System.out.println("pending: " + sb.toString().trim() + "  linked after? " + pending.contains(a2));
        try { pending.remove(a2); } catch (IllegalArgumentException e) { System.out.println("remove twice -> " + e.getMessage()); }
        System.out.println();

        System.out.println("== Measured: summing 1,000,000 longs, array vs linked list (sequential vs shuffled memory order) ==");
        int n = 1_000_000;
        long[] array = new long[n];
        Cell[] cellsA = new Cell[n];
        Cell[] cellsB = new Cell[n];
        for (int i = 0; i < n; i++) { array[i] = i; cellsA[i] = new Cell(i); cellsB[i] = new Cell(i); }
        IntrusiveList<Cell> sequential = linkInOrder(cellsA, identity(n));          // list order == allocation order
        IntrusiveList<Cell> scattered = linkInOrder(cellsB, shuffled(n, 42L));     // list order is a random walk through memory
        long expected = (long) n * (n - 1) / 2;
        long tArray = bestOf(5, () -> sumArray(array));
        long tSeq = bestOf(5, () -> sumList(sequential));
        long tScat = bestOf(5, () -> sumList(scattered));
        System.out.println("all three sums equal " + expected + ": " + (sumArray(array) == expected && sumList(sequential) == expected && sumList(scattered) == expected));
        System.out.printf("long[] sequential scan : %6.2f ms%n", tArray / 1e6);
        System.out.printf("list, nodes in memory order : %6.2f ms  (%.1fx the array)%n", tSeq / 1e6, tSeq / (double) tArray);
        System.out.printf("list, nodes in shuffled order: %6.2f ms  (%.1fx the array)%n", tScat / 1e6, tScat / (double) tArray);
        System.out.println("Same n, same O(n), same sum. The difference is cache misses; timings vary by machine, the ordering does not.");
    }
}
