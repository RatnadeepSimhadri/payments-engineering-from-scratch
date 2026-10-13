// Lesson: phases/01-complexity-and-core-structures/09-heaps-and-priority-queues/docs/en.md
// Heaps and Priority Queues from Scratch
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: Williams, "Algorithm 232: Heapsort" (CACM 1964); Floyd, "Algorithm 245: Treesort 3" (CACM 1964, the O(n) heapify);
//          CLRS ch. 6. FakeClock and SeededRandom copied from Phase 0, Lesson 03. Java 21, standard library only.

import java.util.ArrayList;
import java.util.Comparator;
import java.util.HashMap;
import java.util.List;
import java.util.Map;
import java.util.NoSuchElementException;

/** A clock that moves only when told to. Copied from Phase 0, Lesson 03. */
interface Clock { long nowMillis(); }

final class FakeClock implements Clock {
    private long now;
    FakeClock(long startMillis) { this.now = startMillis; }
    @Override public long nowMillis() { return now; }
    void advance(long millis) {
        if (millis < 0) throw new IllegalArgumentException("cannot advance by a negative duration: " + millis);
        now = Math.addExact(now, millis);
    }
}

/** SplitMix64, copied from Phase 0, Lesson 03: the same sequence for the same seed in Java, Python, and Go. */
final class SeededRandom {
    private long state;
    SeededRandom(long seed) { this.state = seed; }
    long nextLong() {
        state += 0x9E3779B97F4A7C15L;
        long z = state;
        z = (z ^ (z >>> 30)) * 0xBF58476D1CE4E5B9L;
        z = (z ^ (z >>> 27)) * 0x94D049BB133111EBL;
        return z ^ (z >>> 31);
    }
    int nextInt(int bound) {
        if (bound <= 0) throw new IllegalArgumentException("bound must be positive: " + bound);
        return (int) ((nextLong() >>> 33) % bound);
    }
}

/**
 * A binary min-heap in an array. Invariant: for every index i > 0, compare(items[(i-1)/2], items[i]) <= 0.
 * The root (index 0) is therefore the minimum. Heap order is partial: siblings are unordered.
 */
final class MinHeap<T> {
    private final List<T> items = new ArrayList<>();
    private final Comparator<? super T> comparator;

    MinHeap(Comparator<? super T> comparator) { this.comparator = comparator; }

    /** Floyd's bottom-up construction: sift down every internal node from the last parent to the root. O(n). */
    static <T> MinHeap<T> heapify(List<T> source, Comparator<? super T> comparator) {
        MinHeap<T> heap = new MinHeap<>(comparator);
        heap.items.addAll(source);
        for (int i = heap.items.size() / 2 - 1; i >= 0; i--) heap.siftDown(i);
        return heap;
    }

    void push(T item) {
        items.add(item);
        siftUp(items.size() - 1);
    }

    T peek() {
        if (items.isEmpty()) throw new NoSuchElementException("heap is empty");
        return items.get(0);
    }

    T pop() {
        if (items.isEmpty()) throw new NoSuchElementException("heap is empty");
        T top = items.get(0);
        T last = items.remove(items.size() - 1);
        if (!items.isEmpty()) {
            items.set(0, last);
            siftDown(0);
        }
        return top;
    }

    int size() { return items.size(); }
    boolean isEmpty() { return items.isEmpty(); }

    private void siftUp(int i) {
        while (i > 0) {
            int parent = (i - 1) / 2;
            if (comparator.compare(items.get(parent), items.get(i)) <= 0) return;
            swap(parent, i);
            i = parent;
        }
    }

    private void siftDown(int i) {
        int n = items.size();
        while (true) {
            int left = 2 * i + 1, right = left + 1, smallest = i;
            if (left < n && comparator.compare(items.get(left), items.get(smallest)) < 0) smallest = left;
            if (right < n && comparator.compare(items.get(right), items.get(smallest)) < 0) smallest = right;
            if (smallest == i) return;
            swap(i, smallest);
            i = smallest;
        }
    }

    private void swap(int a, int b) {
        T tmp = items.get(a);
        items.set(a, items.get(b));
        items.set(b, tmp);
    }

    /** Checks the invariant over the whole array. O(n); for tests and assertions, not for production paths. */
    boolean isHeap() {
        for (int i = 1; i < items.size(); i++) {
            if (comparator.compare(items.get((i - 1) / 2), items.get(i)) > 0) return false;
        }
        return true;
    }
}

/** What an indexed heap stores: a unique key, its priority, and the sequence number that breaks ties. */
record Entry<K>(K key, long priority, long seq) {
    /** Lower priority first; equal priorities fall back to insertion order, which makes pop() stable. */
    static <K> int compare(Entry<K> a, Entry<K> b) {
        int c = Long.compare(a.priority, b.priority);
        return c != 0 ? c : Long.compare(a.seq, b.seq);
    }
}

/**
 * A min-heap of unique keys with a position index, so decreaseKey(key) is O(log n) instead of O(n).
 * Invariant: index.get(entries.get(i).key()) == i for every i, in addition to heap order.
 */
final class IndexedMinHeap<K> {
    private final List<Entry<K>> entries = new ArrayList<>();
    private final Map<K, Integer> index = new HashMap<>();
    private long nextSeq = 0;

    void push(K key, long priority) {
        if (index.containsKey(key)) throw new IllegalArgumentException("key already present: " + key);
        entries.add(new Entry<>(key, priority, nextSeq++));
        index.put(key, entries.size() - 1);
        siftUp(entries.size() - 1);
    }

    /** Lower the priority of an existing key. The entry keeps its seq, so its tie-break rank is unchanged. */
    void decreaseKey(K key, long newPriority) {
        Integer i = index.get(key);
        if (i == null) throw new NoSuchElementException("key not present: " + key);
        Entry<K> current = entries.get(i);
        if (newPriority > current.priority()) {
            throw new IllegalArgumentException("decreaseKey cannot raise " + key + " from " + current.priority() + " to " + newPriority);
        }
        entries.set(i, new Entry<>(key, newPriority, current.seq()));
        siftUp(i);
    }

    Entry<K> peek() {
        if (entries.isEmpty()) throw new NoSuchElementException("heap is empty");
        return entries.get(0);
    }

    Entry<K> pop() {
        if (entries.isEmpty()) throw new NoSuchElementException("heap is empty");
        Entry<K> top = entries.get(0);
        Entry<K> last = entries.remove(entries.size() - 1);
        index.remove(top.key());
        if (!entries.isEmpty()) {
            entries.set(0, last);
            index.put(last.key(), 0);
            siftDown(0);
        }
        return top;
    }

    boolean contains(K key) { return index.containsKey(key); }

    long priorityOf(K key) {
        Integer i = index.get(key);
        if (i == null) throw new NoSuchElementException("key not present: " + key);
        return entries.get(i).priority();
    }

    int size() { return entries.size(); }
    boolean isEmpty() { return entries.isEmpty(); }

    private void siftUp(int i) {
        while (i > 0) {
            int parent = (i - 1) / 2;
            if (Entry.compare(entries.get(parent), entries.get(i)) <= 0) return;
            swap(parent, i);
            i = parent;
        }
    }

    private void siftDown(int i) {
        int n = entries.size();
        while (true) {
            int left = 2 * i + 1, right = left + 1, smallest = i;
            if (left < n && Entry.compare(entries.get(left), entries.get(smallest)) < 0) smallest = left;
            if (right < n && Entry.compare(entries.get(right), entries.get(smallest)) < 0) smallest = right;
            if (smallest == i) return;
            swap(i, smallest);
            i = smallest;
        }
    }

    /** The one place the index map is maintained: every move of an entry updates its recorded position. */
    private void swap(int a, int b) {
        Entry<K> ea = entries.get(a), eb = entries.get(b);
        entries.set(a, eb);
        entries.set(b, ea);
        index.put(eb.key(), a);
        index.put(ea.key(), b);
    }

    /** Both invariants: heap order and index consistency. For tests. */
    boolean isConsistent() {
        for (int i = 1; i < entries.size(); i++) {
            if (Entry.compare(entries.get((i - 1) / 2), entries.get(i)) > 0) return false;
        }
        if (index.size() != entries.size()) return false;
        for (int i = 0; i < entries.size(); i++) {
            Integer recorded = index.get(entries.get(i).key());
            if (recorded == null || recorded != i) return false;
        }
        return true;
    }
}

/** A retry due at a point in time: which payment, which attempt this will be. */
record RetryJob(String paymentId, int attempt, long dueAtMillis) {}

/**
 * Retry scheduler framing: the heap orders pending retries by due time, earliest first; ties (same
 * millisecond) go in the order they were scheduled. Backoff is deterministic: base * 2^attempt, capped.
 */
final class RetryScheduler {
    static final long BASE_MILLIS = 1_000;
    static final long CAP_MILLIS = 60_000;

    private final Clock clock;
    private final IndexedMinHeap<String> heap = new IndexedMinHeap<>();
    private final Map<String, RetryJob> jobs = new HashMap<>();

    RetryScheduler(Clock clock) { this.clock = clock; }

    static long backoffMillis(int attempt) {
        if (attempt < 0) throw new IllegalArgumentException("attempt must be >= 0");
        long delay = BASE_MILLIS << Math.min(attempt, 20);
        return Math.min(delay, CAP_MILLIS);
    }

    /** Schedule the next attempt for a payment after a failure. Returns the due time. */
    long scheduleRetry(String paymentId, int attempt) {
        long dueAt = clock.nowMillis() + backoffMillis(attempt);
        jobs.put(paymentId, new RetryJob(paymentId, attempt, dueAt));
        heap.push(paymentId, dueAt);
        return dueAt;
    }

    /** Pull a retry forward (an operator or a webhook says "try now"). Never pushes it later. */
    void expedite(String paymentId, long newDueAtMillis) {
        heap.decreaseKey(paymentId, newDueAtMillis);
        RetryJob job = jobs.get(paymentId);
        jobs.put(paymentId, new RetryJob(paymentId, job.attempt(), newDueAtMillis));
    }

    /** Every job whose due time has arrived, earliest first. Removes them from the schedule. */
    List<RetryJob> pollDue() {
        List<RetryJob> due = new ArrayList<>();
        long now = clock.nowMillis();
        while (!heap.isEmpty() && heap.peek().priority() <= now) {
            Entry<String> e = heap.pop();
            due.add(jobs.remove(e.key()));
        }
        return due;
    }

    long nextDueAtMillis() { return heap.peek().priority(); }
    int pending() { return heap.size(); }
    boolean isScheduled(String paymentId) { return heap.contains(paymentId); }
}

public final class Main {
    public static void main(String[] args) {
        System.out.println("== heapify in O(n), then pop in sorted order ==");
        List<Integer> raw = List.of(9, 4, 7, 1, 8, 2, 6, 3, 5);
        MinHeap<Integer> heap = MinHeap.heapify(new ArrayList<>(raw), Comparator.naturalOrder());
        System.out.println("input      = " + raw);
        System.out.print("pop order  = [");
        StringBuilder sb = new StringBuilder();
        while (!heap.isEmpty()) sb.append(sb.length() == 0 ? "" : ", ").append(heap.pop());
        System.out.println(sb + "]");

        System.out.println("\n== 1,000 seeded random pushes and pops keep the heap property ==");
        SeededRandom rng = new SeededRandom(42);
        MinHeap<Integer> stress = new MinHeap<>(Comparator.naturalOrder());
        int pushes = 0, pops = 0;
        for (int i = 0; i < 1_000; i++) {
            if (stress.isEmpty() || rng.nextInt(3) != 0) { stress.push(rng.nextInt(10_000)); pushes++; }
            else { stress.pop(); pops++; }
        }
        System.out.printf("pushes=%d pops=%d size=%d isHeap=%b min=%d%n", pushes, pops, stress.size(), stress.isHeap(), stress.peek());

        System.out.println("\n== Retry scheduler: earliest due first, ties in scheduling order ==");
        FakeClock clock = new FakeClock(1_700_000_000_000L);
        RetryScheduler scheduler = new RetryScheduler(clock);
        System.out.printf("t+0ms: pay-A attempt 2 due at +%dms%n", scheduler.scheduleRetry("pay-A", 2) - clock.nowMillis());
        System.out.printf("t+0ms: pay-B attempt 0 due at +%dms%n", scheduler.scheduleRetry("pay-B", 0) - clock.nowMillis());
        System.out.printf("t+0ms: pay-C attempt 0 due at +%dms (same due time as pay-B)%n", scheduler.scheduleRetry("pay-C", 0) - clock.nowMillis());
        System.out.printf("t+0ms: pay-D attempt 6 due at +%dms (capped at 60s)%n", scheduler.scheduleRetry("pay-D", 6) - clock.nowMillis());
        scheduler.expedite("pay-D", clock.nowMillis() + 500);
        System.out.println("t+0ms: operator expedites pay-D to +500ms (decreaseKey)");
        System.out.println("next due in " + (scheduler.nextDueAtMillis() - clock.nowMillis()) + "ms, pending=" + scheduler.pending());

        clock.advance(1_000);
        System.out.println("t+1000ms: due = " + describe(scheduler.pollDue()));
        clock.advance(3_000);
        System.out.println("t+4000ms: due = " + describe(scheduler.pollDue()));
        System.out.println("pending=" + scheduler.pending());
    }

    static String describe(List<RetryJob> jobs) {
        StringBuilder sb = new StringBuilder("[");
        for (RetryJob j : jobs) sb.append(sb.length() == 1 ? "" : ", ").append(j.paymentId()).append("(attempt ").append(j.attempt()).append(")");
        return sb.append("]").toString();
    }
}
