// Lesson: phases/01-complexity-and-core-structures/03-dynamic-arrays/docs/en.md
// Dynamic Arrays from Scratch
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: Cormen et al., Introduction to Algorithms, ch. 17 (amortized analysis of table doubling);
//          OpenJDK java.util.ArrayList.grow() (1.5x policy); Facebook folly FBVector.md (why 1.5x). Java 21, stdlib only.

import java.util.Arrays;
import java.util.Iterator;
import java.util.NoSuchElementException;
import java.util.Objects;

/**
 * How capacity grows when the backing array is full. The factor is the whole trade-off:
 * a larger factor means fewer copies but more wasted memory and less chance to reuse freed blocks.
 */
enum GrowthPolicy {
    GROW_1_5X {
        @Override int next(int capacity) {
            // capacity + capacity/2, but always at least +1 so tiny arrays still grow (1 -> 2, 2 -> 3).
            return Math.max(capacity + 1, capacity + (capacity >> 1));
        }
    },
    GROW_2X {
        @Override int next(int capacity) {
            return Math.max(capacity + 1, capacity << 1);
        }
    };

    /** The capacity after one reallocation from {@code capacity}. */
    abstract int next(int capacity);
}

/** Counters that make the amortized argument measurable rather than believed. */
final class ArrayStats {
    long reallocations;      // how many times a new backing array was allocated
    long elementsCopied;     // total elements moved between backing arrays (grow + shrink)
    long shrinks;            // how many of the reallocations were shrinks
    int peakCapacity;        // largest backing array ever held

    @Override public String toString() {
        return "reallocations=" + reallocations + " copied=" + elementsCopied
                + " shrinks=" + shrinks + " peakCapacity=" + peakCapacity;
    }
}

/**
 * A growable array with explicit capacity, a pluggable growth factor, and a shrink policy with hysteresis.
 * Invariant: 0 <= size <= capacity, elements[0..size) are live, elements[size..capacity) are null.
 * Every public operation preserves it; the tests check it after every mutation.
 */
final class DynamicArray<T> implements Iterable<T> {
    static final int MIN_CAPACITY = 4;

    private Object[] elements;
    private int size;
    private final GrowthPolicy policy;
    private final boolean shrinkEnabled;
    private final ArrayStats stats = new ArrayStats();

    DynamicArray(int initialCapacity, GrowthPolicy policy) {
        this(initialCapacity, policy, true);
    }

    DynamicArray(int initialCapacity, GrowthPolicy policy, boolean shrinkEnabled) {
        if (initialCapacity < 1) throw new IllegalArgumentException("initialCapacity must be >= 1");
        this.elements = new Object[initialCapacity];
        this.policy = Objects.requireNonNull(policy);
        this.shrinkEnabled = shrinkEnabled;
        this.stats.peakCapacity = initialCapacity;
    }

    int size() { return size; }
    int capacity() { return elements.length; }
    boolean isEmpty() { return size == 0; }
    ArrayStats stats() { return stats; }

    /** Append. O(1) amortized: a copy of all `size` elements happens only when size == capacity. */
    void add(T value) {
        if (size == elements.length) resize(policy.next(elements.length));
        elements[size++] = value;
    }

    @SuppressWarnings("unchecked")
    T get(int index) {
        Objects.checkIndex(index, size);          // size, not capacity: a slot past size is not an element
        return (T) elements[index];
    }

    @SuppressWarnings("unchecked")
    T set(int index, T value) {
        Objects.checkIndex(index, size);
        T old = (T) elements[index];
        elements[index] = value;
        return old;
    }

    /** Insert at index, shifting the tail right. O(n - index): the cost the ring buffer lesson avoids. */
    void insert(int index, T value) {
        if (index < 0 || index > size) throw new IndexOutOfBoundsException("index " + index + " size " + size);
        if (size == elements.length) resize(policy.next(elements.length));
        System.arraycopy(elements, index, elements, index + 1, size - index);
        elements[index] = value;
        size++;
    }

    /** Remove at index, shifting the tail left, nulling the vacated slot so the GC can reclaim it. */
    @SuppressWarnings("unchecked")
    T remove(int index) {
        Objects.checkIndex(index, size);
        T old = (T) elements[index];
        int moved = size - index - 1;
        if (moved > 0) System.arraycopy(elements, index + 1, elements, index, moved);
        elements[--size] = null;
        maybeShrink();
        return old;
    }

    T removeLast() {
        if (size == 0) throw new NoSuchElementException("empty");
        return remove(size - 1);
    }

    void clear() {
        Arrays.fill(elements, 0, size, null);
        size = 0;
        maybeShrink();
    }

    /**
     * Shrink only when the array is less than a quarter full, and only to half capacity.
     * The gap between the shrink trigger (1/4) and the post-shrink load (1/2) is the hysteresis:
     * after a shrink you need `capacity/2` more appends before the next grow, and `capacity/4`
     * more removals before the next shrink, so an add/remove sequence at the boundary cannot thrash.
     */
    private void maybeShrink() {
        if (!shrinkEnabled) return;
        int capacity = elements.length;
        if (capacity > MIN_CAPACITY && size * 4L < capacity) {
            int target = Math.max(MIN_CAPACITY, capacity / 2);
            if (target < capacity) {
                stats.shrinks++;
                resize(target);
            }
        }
    }

    private void resize(int newCapacity) {
        if (newCapacity < size) throw new IllegalStateException("cannot shrink below size");
        elements = Arrays.copyOf(elements, newCapacity);
        stats.reallocations++;
        stats.elementsCopied += size;
        if (newCapacity > stats.peakCapacity) stats.peakCapacity = newCapacity;
    }

    /** The capacity sequence a policy produces from `start`, until it first reaches `target`. */
    static int[] capacitySequence(GrowthPolicy policy, int start, int target) {
        DynamicArray<Integer> seq = new DynamicArray<>(8, GrowthPolicy.GROW_2X, false);
        int c = start;
        seq.add(c);
        while (c < target) {
            c = policy.next(c);
            seq.add(c);
        }
        int[] out = new int[seq.size()];
        for (int i = 0; i < out.length; i++) out[i] = seq.get(i);
        return out;
    }

    @Override public Iterator<T> iterator() {
        return new Iterator<>() {
            private int cursor = 0;
            @Override public boolean hasNext() { return cursor < size; }
            @Override public T next() {
                if (cursor >= size) throw new NoSuchElementException();
                return get(cursor++);
            }
        };
    }

    @Override public String toString() {
        StringBuilder sb = new StringBuilder("[");
        for (int i = 0; i < size; i++) {
            if (i > 0) sb.append(", ");
            sb.append(elements[i]);
        }
        return sb.append("] size=").append(size).append(" capacity=").append(elements.length).toString();
    }
}

public final class Main {

    /** Append n values under a policy and return the counters. */
    static ArrayStats measureAppends(GrowthPolicy policy, int n, int initialCapacity) {
        DynamicArray<Integer> array = new DynamicArray<>(initialCapacity, policy, false);
        for (int i = 0; i < n; i++) array.add(i);
        return array.stats();
    }

    public static void main(String[] args) {
        System.out.println("== Capacity versus size ==");
        DynamicArray<String> postings = new DynamicArray<>(4, GrowthPolicy.GROW_2X);
        for (String p : new String[] {"debit:cash:1999", "credit:revenue:1941", "credit:fees:58"}) {
            postings.add(p);
            System.out.println("add " + p + " -> size=" + postings.size() + " capacity=" + postings.capacity());
        }
        postings.add("debit:cash:500");
        postings.add("credit:revenue:500");
        System.out.println("5th add forced a grow: " + postings);
        System.out.println();

        System.out.println("== Capacity sequences from 8 to 1,000,000 ==");
        for (GrowthPolicy policy : GrowthPolicy.values()) {
            int[] seq = DynamicArray.capacitySequence(policy, 8, 1_000_000);
            System.out.println(policy + ": " + (seq.length - 1) + " reallocations, final capacity " + seq[seq.length - 1]);
            System.out.println("  first ten: " + Arrays.toString(Arrays.copyOf(seq, Math.min(10, seq.length))));
        }
        System.out.println();

        System.out.println("== Measured copies for 1,000,000 appends (initial capacity 8) ==");
        int n = 1_000_000;
        for (GrowthPolicy policy : GrowthPolicy.values()) {
            ArrayStats s = measureAppends(policy, n, 8);
            System.out.printf("%s: %s  copies/append=%.3f  slack=%.1f%%%n", policy, s,
                    s.elementsCopied / (double) n, 100.0 * (s.peakCapacity - n) / n);
        }
        System.out.println("Both are O(1) amortized; the factor moves the constant between 'fewer copies' and 'less slack'.");
        System.out.println();

        System.out.println("== Can freed blocks be reused? (sum of earlier capacities vs the next one) ==");
        for (GrowthPolicy policy : GrowthPolicy.values()) {
            int[] seq = DynamicArray.capacitySequence(policy, 8, 200);
            long freed = 0;
            StringBuilder sb = new StringBuilder();
            for (int i = 1; i < seq.length; i++) {
                freed += seq[i - 1];
                sb.append(seq[i]).append(freed >= seq[i] ? "(fits)" : "(no)").append(' ');
            }
            System.out.println(policy + ": " + sb.toString().trim());
        }
        System.out.println();

        System.out.println("== Shrink with hysteresis: add/remove at the boundary does not thrash ==");
        DynamicArray<Integer> queue = new DynamicArray<>(4, GrowthPolicy.GROW_2X);
        for (int i = 0; i < 64; i++) queue.add(i);
        System.out.println("after 64 adds: size=" + queue.size() + " capacity=" + queue.capacity() + " " + queue.stats());
        while (queue.size() > 16) queue.removeLast();
        System.out.println("down to 16:    size=" + queue.size() + " capacity=" + queue.capacity() + " " + queue.stats());
        queue.removeLast();
        System.out.println("down to 15:    size=" + queue.size() + " capacity=" + queue.capacity() + " (15*4 < 64 -> shrink to 32)");
        long before = queue.stats().reallocations;
        for (int i = 0; i < 1000; i++) { queue.add(i); queue.removeLast(); }
        System.out.println("1000 add/remove pairs at the boundary: reallocations added = " + (queue.stats().reallocations - before));
    }
}
