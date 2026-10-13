// Lesson: phases/01-complexity-and-core-structures/05-stacks-queues-and-deques/docs/en.md
// Stacks, Queues, and Ring Buffers
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: Knuth, TAOCP Vol. 1, 2.2.1-2.2.2 (stacks, queues, deques; circular buffers);
//          LMAX Disruptor technical paper (2011) for sequence-numbered ring buffers; Apache Kafka docs (offsets). Java 21, stdlib only.

import java.util.Arrays;
import java.util.Iterator;
import java.util.NoSuchElementException;
import java.util.Objects;

/**
 * LIFO stack over a growable array. push/pop/peek are O(1) amortized; the top is always at index size-1,
 * so there is no wraparound and no head pointer: the dynamic array from Lesson 03 is already a stack.
 */
final class ArrayStack<T> implements Iterable<T> {
    private Object[] elements;
    private int size;

    ArrayStack(int initialCapacity) {
        if (initialCapacity < 1) throw new IllegalArgumentException("initialCapacity must be >= 1");
        elements = new Object[initialCapacity];
    }

    int size() { return size; }
    boolean isEmpty() { return size == 0; }
    int capacity() { return elements.length; }

    void push(T value) {
        if (size == elements.length) elements = Arrays.copyOf(elements, elements.length * 2);
        elements[size++] = value;
    }

    @SuppressWarnings("unchecked")
    T pop() {
        if (size == 0) throw new NoSuchElementException("stack is empty");
        T v = (T) elements[--size];
        elements[size] = null;          // do not pin the popped object
        return v;
    }

    @SuppressWarnings("unchecked")
    T peek() {
        if (size == 0) throw new NoSuchElementException("stack is empty");
        return (T) elements[size - 1];
    }

    /** Iterates from the top down: the order pop() would return. */
    @SuppressWarnings("unchecked")
    @Override public Iterator<T> iterator() {
        return new Iterator<>() {
            int cursor = size - 1;
            @Override public boolean hasNext() { return cursor >= 0; }
            @Override public T next() {
                if (cursor < 0) throw new NoSuchElementException();
                return (T) elements[cursor--];
            }
        };
    }
}

/** What a bounded buffer does when a producer offers into a full buffer. BLOCK belongs to Phase 4 (concurrency). */
enum OverflowPolicy {
    REJECT,        // offer returns false; the producer must slow down or retry: backpressure
    DROP_OLDEST,   // overwrite the oldest element; the consumer loses history but the producer never waits
}

/**
 * Bounded double-ended queue over a fixed array, addressed by monotonically increasing sequence numbers.
 * head is the sequence of the oldest element, tail the sequence the next offerLast will take; size = tail - head.
 * A sequence maps to a slot with seq % capacity, so the buffer wraps around without ever moving an element.
 * Because size is a difference of sequences, full (size == capacity) and empty (size == 0) are distinct
 * without wasting a slot, and the sequences double as consumer offsets: this is the shape of a broker partition.
 */
final class RingBuffer<T> implements Iterable<T> {
    private final Object[] slots;
    private final OverflowPolicy policy;
    private long head;       // sequence of the first (oldest) element
    private long tail;       // sequence one past the last (newest) element
    private long dropped;    // elements overwritten under DROP_OLDEST

    RingBuffer(int capacity, OverflowPolicy policy) {
        if (capacity < 1) throw new IllegalArgumentException("capacity must be >= 1");
        this.slots = new Object[capacity];
        this.policy = Objects.requireNonNull(policy);
    }

    int capacity() { return slots.length; }
    int size() { return (int) (tail - head); }
    boolean isEmpty() { return head == tail; }
    boolean isFull() { return tail - head == slots.length; }
    long headSequence() { return head; }
    long tailSequence() { return tail; }
    long dropped() { return dropped; }

    /** The slot for a sequence number. With a power-of-two capacity this could be `(int) (seq & (capacity - 1))`. */
    int indexOf(long seq) { return (int) (seq % slots.length); }

    /** Enqueue at the back. Returns false only under REJECT when full. O(1), no element ever moves. */
    boolean offerLast(T value) {
        if (isFull()) {
            if (policy == OverflowPolicy.REJECT) return false;
            slots[indexOf(head)] = null;        // DROP_OLDEST: the oldest slot is exactly the one tail is about to reuse
            head++;
            dropped++;
        }
        slots[indexOf(tail)] = value;
        tail++;
        return true;
    }

    /** Enqueue at the front, stealing the slot just before head. Under DROP_OLDEST when full, drops the newest (the opposite end). */
    boolean offerFirst(T value) {
        if (isFull()) {
            if (policy == OverflowPolicy.REJECT) return false;
            tail--;
            slots[indexOf(tail)] = null;
            dropped++;
        }
        head--;                                 // may go negative conceptually; see indexOf guard below
        if (head < 0) { rebase(); }
        slots[indexOf(head)] = value;
        return true;
    }

    /** Sequences are non-negative so `%` is a valid index; if offerFirst would make head negative, shift both by capacity. */
    private void rebase() {
        head += slots.length;
        tail += slots.length;
    }

    @SuppressWarnings("unchecked")
    T pollFirst() {
        if (isEmpty()) return null;
        int i = indexOf(head);
        T v = (T) slots[i];
        slots[i] = null;
        head++;
        return v;
    }

    @SuppressWarnings("unchecked")
    T pollLast() {
        if (isEmpty()) return null;
        tail--;
        int i = indexOf(tail);
        T v = (T) slots[i];
        slots[i] = null;
        return v;
    }

    @SuppressWarnings("unchecked")
    T peekFirst() { return isEmpty() ? null : (T) slots[indexOf(head)]; }

    @SuppressWarnings("unchecked")
    T peekLast() { return isEmpty() ? null : (T) slots[indexOf(tail - 1)]; }

    /** O(1) random access to the i-th element from the front: the thing a linked deque cannot do. */
    @SuppressWarnings("unchecked")
    T get(int i) {
        Objects.checkIndex(i, size());
        return (T) slots[indexOf(head + i)];
    }

    void clear() {
        Arrays.fill(slots, null);
        head = tail = 0;
    }

    @SuppressWarnings("unchecked")
    @Override public Iterator<T> iterator() {
        return new Iterator<>() {
            long seq = head;
            @Override public boolean hasNext() { return seq < tail; }
            @Override public T next() {
                if (seq >= tail) throw new NoSuchElementException();
                return (T) slots[indexOf(seq++)];
            }
        };
    }

    /** Debug view of the raw slots, in physical order, with the head and tail positions marked. */
    String slotsView() {
        StringBuilder sb = new StringBuilder();
        for (int i = 0; i < slots.length; i++) {
            if (i > 0) sb.append(' ');
            String mark = "";
            if (!isEmpty() && i == indexOf(head)) mark += "H";
            if (i == indexOf(tail) && !isFull()) mark += "T";
            if (isFull() && i == indexOf(head)) mark += "T";
            sb.append('[').append(slots[i] == null ? "." : slots[i]).append(']').append(mark);
        }
        return sb.toString();
    }

    @Override public String toString() {
        StringBuilder sb = new StringBuilder("[");
        for (long s = head; s < tail; s++) { sb.append(slots[indexOf(s)]); if (s + 1 < tail) sb.append(", "); }
        return sb.append("] head=").append(head).append(" tail=").append(tail).append(" size=").append(size()).toString();
    }
}

public final class Main {

    /** Classic stack use: check that brackets in a message body are balanced. O(n) with the stack holding open brackets. */
    static boolean bracketsBalanced(String text) {
        ArrayStack<Character> open = new ArrayStack<>(8);
        for (int i = 0; i < text.length(); i++) {
            char c = text.charAt(i);
            if (c == '(' || c == '[' || c == '{') open.push(c);
            else if (c == ')' || c == ']' || c == '}') {
                if (open.isEmpty()) return false;
                char o = open.pop();
                if ((c == ')' && o != '(') || (c == ']' && o != '[') || (c == '}' && o != '{')) return false;
            }
        }
        return open.isEmpty();
    }

    public static void main(String[] args) {
        System.out.println("== Stack: compensating a saga in reverse order ==");
        ArrayStack<String> compensations = new ArrayStack<>(2);
        String[] steps = {"reserve funds on wallet", "post ledger hold", "request capture at acquirer"};
        String[] undo = {"release wallet reservation", "reverse ledger hold", "void acquirer authorization"};
        for (int i = 0; i < steps.length; i++) {
            System.out.println("step " + (i + 1) + ": " + steps[i] + "  -> push undo: " + undo[i]);
            compensations.push(undo[i]);
        }
        System.out.println("step 4 failed; unwinding " + compensations.size() + " compensations (capacity grew to " + compensations.capacity() + "):");
        while (!compensations.isEmpty()) System.out.println("  pop -> " + compensations.pop());
        System.out.println("brackets balanced in {\"a\":[1,2,{\"b\":3}]} ? " + bracketsBalanced("{\"a\":[1,2,{\"b\":3}]}"));
        System.out.println("brackets balanced in {\"a\":[1,2}   ? " + bracketsBalanced("{\"a\":[1,2}"));
        System.out.println();

        System.out.println("== Ring buffer: wraparound with capacity 4 (sequence -> slot = seq % 4) ==");
        RingBuffer<String> ring = new RingBuffer<>(4, OverflowPolicy.REJECT);
        for (String e : new String[] {"e0", "e1", "e2", "e3"}) ring.offerLast(e);
        System.out.println("offer e0..e3   : " + ring.slotsView() + "   " + ring);
        System.out.println("offer e4 (full): accepted=" + ring.offerLast("e4") + "  (REJECT: producer must back off)");
        System.out.println("poll -> " + ring.pollFirst() + ", poll -> " + ring.pollFirst());
        System.out.println("after 2 polls  : " + ring.slotsView() + "   " + ring);
        ring.offerLast("e4"); ring.offerLast("e5");
        System.out.println("offer e4, e5   : " + ring.slotsView() + "   " + ring);
        System.out.println("e5 has sequence 5 and lives in slot 5 % 4 = " + ring.indexOf(5) + "; logical order is still " + ring);
        System.out.println("get(0)=" + ring.get(0) + " get(3)=" + ring.get(3) + "  (O(1) by index, through the wrap)");
        System.out.println();

        System.out.println("== Deque: both ends in O(1) ==");
        RingBuffer<Integer> deque = new RingBuffer<>(5, OverflowPolicy.REJECT);
        deque.offerLast(2); deque.offerLast(3); deque.offerFirst(1); deque.offerFirst(0);
        System.out.println("offerLast 2,3 then offerFirst 1,0 -> " + deque);
        System.out.println("pollLast -> " + deque.pollLast() + ", pollFirst -> " + deque.pollFirst() + ", now " + deque);
        System.out.println();

        System.out.println("== Bounded queue semantics: a slow consumer under two policies ==");
        RingBuffer<String> rejecting = new RingBuffer<>(4, OverflowPolicy.REJECT);
        RingBuffer<String> dropping = new RingBuffer<>(4, OverflowPolicy.DROP_OLDEST);
        int rejected = 0;
        for (int i = 0; i < 10; i++) {
            String event = "payment." + i;
            if (!rejecting.offerLast(event)) rejected++;
            dropping.offerLast(event);
        }
        System.out.println("10 events offered, consumer read none:");
        System.out.println("  REJECT      : holds " + rejecting + "; rejected=" + rejected + " (the producer saw every rejection)");
        System.out.println("  DROP_OLDEST : holds " + dropping + "; dropped=" + dropping.dropped() + " (the consumer resumes at offset " + dropping.headSequence() + ")");
        System.out.println();

        System.out.println("== Broker framing: sequences are offsets ==");
        RingBuffer<String> partition = new RingBuffer<>(8, OverflowPolicy.DROP_OLDEST);
        for (int i = 0; i < 12; i++) partition.offerLast("msg" + i);
        System.out.println("12 messages into a capacity-8 log: retained offsets " + partition.headSequence() + ".." + (partition.tailSequence() - 1)
                + ", evicted " + partition.dropped());
        long consumerOffset = 2;
        System.out.println("a consumer at offset " + consumerOffset + " is behind the retention window: it must reset to " + partition.headSequence()
                + " and has lost " + (partition.headSequence() - consumerOffset) + " messages");
        consumerOffset = 9;
        System.out.println("a consumer at offset " + consumerOffset + " reads " + partition.get((int) (consumerOffset - partition.headSequence()))
                + " next; " + (partition.tailSequence() - consumerOffset) + " messages of lag");
    }
}
