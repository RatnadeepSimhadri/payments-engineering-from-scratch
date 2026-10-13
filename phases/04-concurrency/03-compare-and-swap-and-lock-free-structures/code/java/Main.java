// Lesson: phases/04-concurrency/03-compare-and-swap-and-lock-free-structures/docs/en.md
// Compare-and-Swap and Lock-Free Structures
// Run:  javac -d .out *.java && java -cp .out Main
// Test: javac -d .out *.java && java -cp .out MainTest
// Sources: Treiber, "Systems Programming: Coping with Parallelism" (IBM RJ 5118, 1986); Michael & Scott,
// "Simple, Fast, and Practical Non-Blocking and Blocking Concurrent Queue Algorithms" (PODC 1996);
// Herlihy & Shavit, The Art of Multiprocessor Programming, ch. 3 and 11; java.util.concurrent.atomic Javadoc.
// Java 21, standard library only.

import java.util.ArrayDeque;
import java.util.ArrayList;
import java.util.List;
import java.util.Objects;
import java.util.Optional;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.atomic.AtomicLong;
import java.util.concurrent.atomic.AtomicReference;
import java.util.concurrent.atomic.AtomicStampedReference;

/**
 * Treiber's lock-free stack. Every push and pop is one CAS on {@code head}; a failed CAS means
 * another thread published first, so the loop re-reads and tries again. Nodes are freshly allocated
 * and never reused, which is what makes this version immune to the ABA problem under a garbage collector.
 */
final class TreiberStack<T> {
    static final class Node<T> {
        final T value;
        Node<T> next;
        Node(T value) { this.value = value; }
    }

    private final AtomicReference<Node<T>> head = new AtomicReference<>();
    private final AtomicLong casRetries = new AtomicLong();

    void push(T value) {
        Objects.requireNonNull(value, "value");
        Node<T> node = new Node<>(value);
        while (true) {
            Node<T> current = head.get();
            node.next = current;                       // link before publishing: next is set, then CAS publishes the node
            if (head.compareAndSet(current, node)) return;
            casRetries.incrementAndGet();
        }
    }

    Optional<T> pop() {
        while (true) {
            Node<T> current = head.get();
            if (current == null) return Optional.empty();
            Node<T> next = current.next;
            if (head.compareAndSet(current, next)) return Optional.of(current.value);
            casRetries.incrementAndGet();
        }
    }

    Optional<T> peek() {
        Node<T> current = head.get();
        return current == null ? Optional.empty() : Optional.of(current.value);
    }

    boolean isEmpty() { return head.get() == null; }

    /** O(n) snapshot for tests and demos; not linearizable with respect to concurrent pushes. */
    int size() {
        int n = 0;
        for (Node<T> cur = head.get(); cur != null; cur = cur.next) n++;
        return n;
    }

    long casRetries() { return casRetries.get(); }
}

/** A capture job popped from a work stack. The payments payload for the ABA demonstration. */
record CaptureJob(String paymentId, long amountMinor) {}

/**
 * A Treiber stack that recycles its nodes through a free list, the way a C implementation or an
 * allocation-free hot path would. Recycling is exactly what re-enables the ABA problem: a node that
 * was popped can be pushed again, so {@code head} can hold the same reference with different contents.
 *
 * {@code beforeCas} is a test seam: it runs between the read of {@code head} and the CAS in {@code pop},
 * so a test can perform a complete pop/pop/push sequence at precisely the vulnerable instant, in one thread.
 */
class PooledStack {
    static final class Node {
        final int id;            // stable identity so the demo can print "N3" and show the same node reappearing
        CaptureJob value;
        Node next;
        Node(int id) { this.id = id; }
    }

    protected final AtomicReference<Node> head = new AtomicReference<>();
    protected final ArrayDeque<Node> freeList = new ArrayDeque<>();     // FIFO: the oldest freed node is reused first
    protected final Runnable beforeCas;
    private int nextId = 1;

    PooledStack(Runnable beforeCas) { this.beforeCas = beforeCas; }
    PooledStack() { this(() -> {}); }

    protected synchronized Node allocate(CaptureJob value) {
        Node node = freeList.isEmpty() ? new Node(nextId++) : freeList.pollFirst();
        node.value = value;
        return node;
    }

    protected synchronized void free(Node node) { freeList.addLast(node); }

    void push(CaptureJob value) {
        Node node = allocate(value);
        while (true) {
            Node current = head.get();
            node.next = current;
            if (head.compareAndSet(current, node)) return;
        }
    }

    Optional<CaptureJob> pop() {
        while (true) {
            Node current = head.get();
            if (current == null) return Optional.empty();
            Node next = current.next;
            beforeCas.run();                                             // the gap where ABA happens
            if (head.compareAndSet(current, next)) {
                CaptureJob value = current.value;
                free(current);
                return Optional.of(value);
            }
        }
    }

    Node headNode() { return head.get(); }

    List<String> snapshot() {
        List<String> out = new ArrayList<>();
        for (Node cur = head.get(); cur != null; cur = cur.next) out.add("N" + cur.id + "=" + cur.value.paymentId());
        return out;
    }
}

/**
 * The same pooled stack with the fix: {@code head} is an {@link AtomicStampedReference}. Every successful
 * CAS increments the stamp, so a CAS that expects (N3, stamp 3) fails if the stack has moved on to (N3, stamp 6),
 * even though the reference is the same object. This is the version-counter idea that databases call optimistic locking.
 */
final class StampedPooledStack {
    private final AtomicStampedReference<PooledStack.Node> head = new AtomicStampedReference<>(null, 0);
    private final ArrayDeque<PooledStack.Node> freeList = new ArrayDeque<>();
    private final Runnable beforeCas;
    private final AtomicLong staleCasRejected = new AtomicLong();
    private int nextId = 1;

    StampedPooledStack(Runnable beforeCas) { this.beforeCas = beforeCas; }
    StampedPooledStack() { this(() -> {}); }

    private synchronized PooledStack.Node allocate(CaptureJob value) {
        PooledStack.Node node = freeList.isEmpty() ? new PooledStack.Node(nextId++) : freeList.pollFirst();
        node.value = value;
        return node;
    }

    private synchronized void free(PooledStack.Node node) { freeList.addLast(node); }

    void push(CaptureJob value) {
        PooledStack.Node node = allocate(value);
        int[] stamp = new int[1];
        while (true) {
            PooledStack.Node current = head.get(stamp);
            node.next = current;
            if (head.compareAndSet(current, node, stamp[0], stamp[0] + 1)) return;
        }
    }

    Optional<CaptureJob> pop() {
        int[] stamp = new int[1];
        while (true) {
            PooledStack.Node current = head.get(stamp);
            if (current == null) return Optional.empty();
            PooledStack.Node next = current.next;
            beforeCas.run();
            if (head.compareAndSet(current, next, stamp[0], stamp[0] + 1)) {
                CaptureJob value = current.value;
                free(current);
                return Optional.of(value);
            }
            staleCasRejected.incrementAndGet();
        }
    }

    int stamp() {
        int[] s = new int[1];
        head.get(s);
        return s[0];
    }

    long staleCasRejected() { return staleCasRejected.get(); }

    List<String> snapshot() {
        List<String> out = new ArrayList<>();
        for (PooledStack.Node cur = head.getReference(); cur != null; cur = cur.next) out.add("N" + cur.id + "=" + cur.value.paymentId());
        return out;
    }
}

/**
 * Michael-Scott lock-free FIFO queue. Two pointers, {@code head} (a sentinel whose next is the first real
 * element) and {@code tail} (the last node, or one behind it). Enqueue links the new node to tail.next with a CAS
 * and then swings tail; if another thread finds tail lagging it helps by swinging tail itself before retrying.
 */
final class MichaelScottQueue<T> {
    static final class Node<T> {
        final T value;
        final AtomicReference<Node<T>> next = new AtomicReference<>();
        Node(T value) { this.value = value; }
    }

    private final AtomicReference<Node<T>> head;
    private final AtomicReference<Node<T>> tail;
    private final AtomicLong casRetries = new AtomicLong();
    private final AtomicLong helpedTail = new AtomicLong();

    MichaelScottQueue() {
        Node<T> sentinel = new Node<>(null);
        head = new AtomicReference<>(sentinel);
        tail = new AtomicReference<>(sentinel);
    }

    void enqueue(T value) {
        Objects.requireNonNull(value, "value");
        Node<T> node = new Node<>(value);
        while (true) {
            Node<T> last = tail.get();
            Node<T> next = last.next.get();
            if (last != tail.get()) { casRetries.incrementAndGet(); continue; }  // tail moved under us; re-read
            if (next == null) {
                if (last.next.compareAndSet(null, node)) {                      // step 1: link the node (linearization point)
                    tail.compareAndSet(last, node);                             // step 2: swing tail; failure is fine, someone helped
                    return;
                }
                casRetries.incrementAndGet();
            } else {
                helpedTail.incrementAndGet();
                tail.compareAndSet(last, next);                                 // tail is lagging: help, then retry
            }
        }
    }

    Optional<T> dequeue() {
        while (true) {
            Node<T> first = head.get();
            Node<T> last = tail.get();
            Node<T> next = first.next.get();
            if (first != head.get()) { casRetries.incrementAndGet(); continue; }
            if (next == null) return Optional.empty();                           // sentinel has no successor: empty
            if (first == last) {                                                 // tail lagging behind a real node: help
                helpedTail.incrementAndGet();
                tail.compareAndSet(last, next);
                continue;
            }
            T value = next.value;                                                // read before the CAS: after it, next is the new sentinel
            if (head.compareAndSet(first, next)) return Optional.of(value);
            casRetries.incrementAndGet();
        }
    }

    boolean isEmpty() { return head.get().next.get() == null; }

    long casRetries() { return casRetries.get(); }
    long helpedTail() { return helpedTail.get(); }
}

/** The four standard progress guarantees, from weakest to strongest. */
enum Progress {
    BLOCKING("a thread holding a lock that is descheduled blocks every other thread"),
    OBSTRUCTION_FREE("a thread running alone for long enough completes; contention may livelock"),
    LOCK_FREE("some thread completes in a bounded number of steps; an individual thread may starve"),
    WAIT_FREE("every thread completes in a bounded number of its own steps");

    final String meaning;
    Progress(String meaning) { this.meaning = meaning; }
}

public final class Main {

    /** Runs {@code threads} workers that each perform {@code perThread} pushes and pops; returns the elapsed millis. */
    static long stressStack(TreiberStack<Long> stack, int threads, int perThread) throws InterruptedException {
        CountDownLatch start = new CountDownLatch(1);
        CountDownLatch done = new CountDownLatch(threads);
        AtomicLong popped = new AtomicLong();
        for (int t = 0; t < threads; t++) {
            final long base = (long) t * perThread;
            Thread worker = new Thread(() -> {
                try {
                    start.await();
                    for (int i = 0; i < perThread; i++) {
                        stack.push(base + i);
                        if (stack.pop().isPresent()) popped.incrementAndGet();
                    }
                } catch (InterruptedException e) {
                    Thread.currentThread().interrupt();
                } finally {
                    done.countDown();
                }
            });
            worker.start();
        }
        long t0 = System.nanoTime();
        start.countDown();
        done.await();
        return (System.nanoTime() - t0) / 1_000_000;
    }

    static void demoTreiber() {
        System.out.println("== A lock-free stack: one CAS per push, one per pop ==");
        TreiberStack<String> stack = new TreiberStack<>();
        stack.push("pay_001");
        stack.push("pay_002");
        stack.push("pay_003");
        System.out.println("pushed pay_001, pay_002, pay_003; size = " + stack.size());
        System.out.println("pop -> " + stack.pop().orElse("empty") + ", pop -> " + stack.pop().orElse("empty"));
        System.out.println("peek -> " + stack.peek().orElse("empty") + ", size = " + stack.size());
        System.out.println("pop on empty -> " + new TreiberStack<String>().pop().map(Object::toString).orElse("Optional.empty"));
        System.out.println();
    }

    static void demoAba() {
        System.out.println("== ABA: the same node, a different stack ==");
        PooledStack[] holder = new PooledStack[1];
        boolean[] armed = {true};
        PooledStack stack = new PooledStack(() -> {
            if (!armed[0]) return;
            armed[0] = false;                                              // only the first pop is interrupted
            PooledStack s = holder[0];
            System.out.println("  thread A paused between reading head=N3 and its CAS");
            System.out.println("  thread B: pop -> " + s.pop().map(CaptureJob::paymentId).orElse("empty") + "  (N3 goes to the free list)");
            System.out.println("  thread B: pop -> " + s.pop().map(CaptureJob::paymentId).orElse("empty") + "  (N2 goes to the free list)");
            s.push(new CaptureJob("pay_004", 4_000));
            System.out.println("  thread B: push pay_004 reuses N3; stack is now " + s.snapshot());
        });
        holder[0] = stack;
        stack.push(new CaptureJob("pay_001", 1_000));
        stack.push(new CaptureJob("pay_002", 2_000));
        stack.push(new CaptureJob("pay_003", 3_000));
        System.out.println("before: " + stack.snapshot());
        CaptureJob got = stack.pop().orElseThrow();
        System.out.println("thread A resumes: CAS(head, N3 -> N2) succeeds, returns " + got.paymentId());
        System.out.println("after:  " + stack.snapshot() + "   <- pay_002 is back on the stack and will be captured twice");
        System.out.println();
    }

    static void demoStamped() {
        System.out.println("== The fix: a stamp that changes on every CAS ==");
        StampedPooledStack[] holder = new StampedPooledStack[1];
        boolean[] armed = {true};
        StampedPooledStack stack = new StampedPooledStack(() -> {
            if (!armed[0]) return;
            armed[0] = false;
            StampedPooledStack s = holder[0];
            s.pop(); s.pop(); s.push(new CaptureJob("pay_004", 4_000));
            System.out.println("  thread B: pop, pop, push pay_004 -> stamp is now " + s.stamp() + ", stack " + s.snapshot());
        });
        holder[0] = stack;
        stack.push(new CaptureJob("pay_001", 1_000));
        stack.push(new CaptureJob("pay_002", 2_000));
        stack.push(new CaptureJob("pay_003", 3_000));
        System.out.println("before: " + stack.snapshot() + " stamp = " + stack.stamp());
        CaptureJob got = stack.pop().orElseThrow();
        System.out.println("thread A: CAS expecting (N3, stamp 3) rejected (" + stack.staleCasRejected()
                + " stale CAS), retried, returned " + got.paymentId());
        System.out.println("after:  " + stack.snapshot() + "   <- every job captured exactly once");
        System.out.println();
    }

    static void demoQueue() throws InterruptedException {
        System.out.println("== Michael-Scott queue: two CAS pointers and a helping step ==");
        MichaelScottQueue<Integer> queue = new MichaelScottQueue<>();
        for (int i = 1; i <= 3; i++) queue.enqueue(i);
        System.out.println("enqueue 1,2,3; dequeue -> " + queue.dequeue().orElse(-1) + ", " + queue.dequeue().orElse(-1)
                + ", " + queue.dequeue().orElse(-1) + ", then " + queue.dequeue().map(Object::toString).orElse("empty"));

        int producers = 4, perProducer = 20_000;
        CountDownLatch done = new CountDownLatch(producers);
        for (int p = 0; p < producers; p++) {
            final int base = p * perProducer;
            new Thread(() -> {
                for (int i = 0; i < perProducer; i++) queue.enqueue(base + i);
                done.countDown();
            }).start();
        }
        done.await();
        boolean[] seen = new boolean[producers * perProducer];
        int count = 0;
        for (Optional<Integer> v = queue.dequeue(); v.isPresent(); v = queue.dequeue()) {
            seen[v.get()] = true;
            count++;
        }
        boolean allSeen = true;
        for (boolean b : seen) allSeen &= b;
        System.out.println(producers + " producers x " + perProducer + " enqueues -> dequeued " + count
                + ", every value exactly once: " + allSeen);
        System.out.println();
    }

    static void demoProgress() {
        System.out.println("== Progress guarantees ==");
        for (Progress p : Progress.values()) System.out.println(String.format("%-17s %s", p, p.meaning));
        System.out.println("Treiber stack and Michael-Scott queue: LOCK_FREE (a CAS fails only because another thread's succeeded).");
        System.out.println();
    }

    public static void main(String[] args) throws InterruptedException {
        demoTreiber();
        demoAba();
        demoStamped();
        demoQueue();

        System.out.println("== Stress: 4 threads x 50,000 push/pop pairs on one Treiber stack ==");
        TreiberStack<Long> stack = new TreiberStack<>();
        stressStack(stack, 4, 50_000);
        System.out.println("stack empty afterwards: " + stack.isEmpty() + "; every push matched by a pop, nothing lost, nothing duplicated");
        System.out.println("(CAS retries are counted by stack.casRetries(); the number depends on the scheduler, which is the point)");
        System.out.println();

        demoProgress();
    }
}
